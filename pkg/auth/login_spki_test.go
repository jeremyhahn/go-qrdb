// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package auth

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"testing"
	"time"
)

// spkiTestCert issues a self-signed certificate from the given key. Two
// certificates issued from the same key model a renewal: different
// cert.Raw bytes, identical SubjectPublicKeyInfo.
func spkiTestCert(t *testing.T, key *ecdsa.PrivateKey, serial int64) *x509.Certificate {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: "spki-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return cert
}

// ---------------------------------------------------------------------------
// SPKICredentialKey
// ---------------------------------------------------------------------------

func TestSPKICredentialKey_matchesCredentialKeyOverSPKI(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	cert := spkiTestCert(t, key, 1)

	got := SPKICredentialKey(cert)
	want := CredentialKey(cert.RawSubjectPublicKeyInfo)
	if !bytes.Equal(got, want) {
		t.Fatalf("SPKICredentialKey != CredentialKey(RawSubjectPublicKeyInfo)")
	}
	if len(got) != 32 {
		t.Fatalf("expected 32-byte digest, got %d", len(got))
	}
}

func TestSPKICredentialKey_stableAcrossRenewal(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	orig := spkiTestCert(t, key, 1)
	renewed := spkiTestCert(t, key, 2)

	if bytes.Equal(orig.Raw, renewed.Raw) {
		t.Fatal("test setup: renewal produced identical cert bytes")
	}
	if !bytes.Equal(SPKICredentialKey(orig), SPKICredentialKey(renewed)) {
		t.Fatal("SPKI key changed across renewal with the same keypair")
	}
	if bytes.Equal(CredentialKey(orig.Raw), CredentialKey(renewed.Raw)) {
		t.Fatal("test setup: SHA-256(cert) keys unexpectedly equal")
	}
}

func TestSPKICredentialKey_nilAndEmptyCert(t *testing.T) {
	if got := SPKICredentialKey(nil); got != nil {
		t.Fatalf("expected nil for nil cert, got %v", got)
	}
	if got := SPKICredentialKey(&x509.Certificate{}); got != nil {
		t.Fatalf("expected nil for cert without SPKI, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// ResolveFirst
// ---------------------------------------------------------------------------

func TestResolveFirst_firstKeyHits(t *testing.T) {
	store := newStubFactorStore()
	users := &stubUserLookup{users: map[uint64]*Principal{
		1: {ID: 1, Username: "alice", Active: true},
	}}
	spkiKey := []byte("spki-key")
	store.put(AuthFactorMTLS, spkiKey, &AuthFactor{ID: "f1", UserID: 1, Type: AuthFactorMTLS})

	r := NewLoginResolver(store, users)
	principal, factor, err := r.ResolveFirst(context.Background(), AuthFactorMTLS, spkiKey, []byte("legacy-key"))
	if err != nil {
		t.Fatalf("ResolveFirst: %v", err)
	}
	if principal.ID != 1 || factor.ID != "f1" {
		t.Fatalf("unexpected resolution: principal=%+v factor=%+v", principal, factor)
	}
}

func TestResolveFirst_fallbackKeyHits(t *testing.T) {
	store := newStubFactorStore()
	users := &stubUserLookup{users: map[uint64]*Principal{
		1: {ID: 1, Username: "alice", Active: true},
	}}
	legacyKey := []byte("legacy-key")
	store.put(AuthFactorMTLS, legacyKey, &AuthFactor{ID: "f1", UserID: 1, Type: AuthFactorMTLS})

	r := NewLoginResolver(store, users)
	principal, factor, err := r.ResolveFirst(context.Background(), AuthFactorMTLS, []byte("spki-key-miss"), legacyKey)
	if err != nil {
		t.Fatalf("ResolveFirst fallback: %v", err)
	}
	if principal.ID != 1 || factor.ID != "f1" {
		t.Fatalf("unexpected resolution: principal=%+v factor=%+v", principal, factor)
	}
}

func TestResolveFirst_revokedAbortsWithoutFallback(t *testing.T) {
	store := newStubFactorStore()
	users := &stubUserLookup{users: map[uint64]*Principal{
		1: {ID: 1, Username: "alice", Active: true},
	}}
	now := time.Now()
	revokedKey := []byte("revoked-key")
	liveKey := []byte("live-key")
	store.put(AuthFactorMTLS, revokedKey, &AuthFactor{ID: "f-revoked", UserID: 1, Type: AuthFactorMTLS, RevokedAt: &now})
	store.put(AuthFactorMTLS, liveKey, &AuthFactor{ID: "f-live", UserID: 1, Type: AuthFactorMTLS})

	r := NewLoginResolver(store, users)
	_, _, err := r.ResolveFirst(context.Background(), AuthFactorMTLS, revokedKey, liveKey)
	var revoked *FactorRevokedError
	if !errors.As(err, &revoked) {
		t.Fatalf("expected FactorRevokedError (no fallback past revocation), got %v", err)
	}
}

func TestResolveFirst_allMiss(t *testing.T) {
	r := NewLoginResolver(newStubFactorStore(), &stubUserLookup{})
	_, _, err := r.ResolveFirst(context.Background(), AuthFactorMTLS, []byte("k1"), []byte("k2"))
	var notFound *FactorNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected FactorNotFoundError, got %v", err)
	}
}

func TestResolveFirst_noKeys(t *testing.T) {
	r := NewLoginResolver(newStubFactorStore(), &stubUserLookup{})
	_, _, err := r.ResolveFirst(context.Background(), AuthFactorMTLS)
	var invalid *InvalidFactorError
	if !errors.As(err, &invalid) {
		t.Fatalf("expected InvalidFactorError, got %v", err)
	}
}

func TestResolveFirst_storeErrorAborts(t *testing.T) {
	store := newStubFactorStore()
	store.lookupErr = errors.New("kv unavailable")
	r := NewLoginResolver(store, &stubUserLookup{})
	_, _, err := r.ResolveFirst(context.Background(), AuthFactorMTLS, []byte("k1"), []byte("k2"))
	if err == nil || err.Error() != "kv unavailable" {
		t.Fatalf("expected store error to abort, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Clone deep-copies SPKICredential
// ---------------------------------------------------------------------------

func TestClone_copiesSPKICredential(t *testing.T) {
	orig := &AuthFactor{
		ID:             "f1",
		Type:           AuthFactorMTLS,
		Credential:     []byte{1, 2, 3},
		SPKICredential: []byte{4, 5, 6},
	}
	clone := orig.Clone()
	if !bytes.Equal(clone.SPKICredential, orig.SPKICredential) {
		t.Fatal("clone SPKICredential differs")
	}
	clone.SPKICredential[0] = 0xFF
	if orig.SPKICredential[0] == 0xFF {
		t.Fatal("clone shares SPKICredential backing array with original")
	}
}
