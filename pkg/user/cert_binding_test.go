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

package user

import (
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

// makeCertForKey builds a self-signed certificate for the given key and returns
// the parsed *x509.Certificate (RawSubjectPublicKeyInfo populated).
func makeCertForKey(t *testing.T, key *ecdsa.PrivateKey, cn string, serial int64) *x509.Certificate {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: cn},
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

// TestComputeSPKIThumbprint_KeyStability verifies the canonical property: the
// thumbprint tracks the PUBLIC KEY, so two certificates for the same key (a
// renewal) share a thumbprint, while a different key yields a different one.
func TestComputeSPKIThumbprint_KeyStability(t *testing.T) {
	key1, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	cert1 := makeCertForKey(t, key1, "admin", 1)
	cert1Renewed := makeCertForKey(t, key1, "admin-renewed", 2) // same key, new cert

	if ComputeSPKIThumbprint(cert1) != ComputeSPKIThumbprint(cert1Renewed) {
		t.Fatal("same key must yield the same SPKI thumbprint across re-issuance")
	}

	key2, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	cert2 := makeCertForKey(t, key2, "admin", 1)
	if ComputeSPKIThumbprint(cert1) == ComputeSPKIThumbprint(cert2) {
		t.Fatal("different keys must yield different SPKI thumbprints")
	}

	if ComputeSPKIThumbprint(nil) != "" {
		t.Fatal("nil cert must yield empty thumbprint")
	}
}

// TestUser_CertBindingHelpers exercises AddCertBinding (dedup), HasCertBinding,
// and MatchesCert.
func TestUser_CertBindingHelpers(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	cert := makeCertForKey(t, key, "admin", 1)
	tp := ComputeSPKIThumbprint(cert)

	u := &User{ID: 1}
	if u.MatchesCert(cert) {
		t.Fatal("unbound user must not match")
	}
	u.AddCertBinding(CertBinding{Thumbprint: tp, Kind: CertBindingSPKIThumbprint})
	u.AddCertBinding(CertBinding{Thumbprint: tp, Kind: CertBindingSPKIThumbprint}) // dup
	if len(u.CertBindings) != 1 {
		t.Fatalf("AddCertBinding must de-dup by thumbprint, got %d", len(u.CertBindings))
	}
	if !u.HasCertBinding(tp) || !u.MatchesCert(cert) {
		t.Fatal("bound user must match its certificate")
	}
	if u.MatchesCert(nil) {
		t.Fatal("nil cert must not match")
	}
}

// TestKVUserStore_CertThumbprintRoundTrip verifies the SPKI-thumbprint secondary
// index: binding via UpdateUser is resolvable via UserByCertThumbprint, and
// DeleteUser removes the index entry.
func TestKVUserStore_CertThumbprintRoundTrip(t *testing.T) {
	s := &kvUserStore{store: newMemKVStore(), idgen: newTestIDGen(t)}
	ctx := context.Background()

	u, err := s.CreateUser(ctx, &CreateUserRequest{Username: "admin", TenantID: "system"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	cert := makeCertForKey(t, key, "admin", 1)
	tp := ComputeSPKIThumbprint(cert)

	if _, err := s.UpdateUser(ctx, &UpdateUserRequest{
		ID:           u.ID,
		CertBindings: []CertBinding{{Thumbprint: tp, Kind: CertBindingSPKIThumbprint}},
	}); err != nil {
		t.Fatalf("UpdateUser bind: %v", err)
	}

	got, err := s.UserByCertThumbprint(ctx, tp)
	if err != nil {
		t.Fatalf("UserByCertThumbprint: %v", err)
	}
	if got.ID != u.ID {
		t.Fatalf("resolved wrong user: got %d want %d", got.ID, u.ID)
	}

	if err := s.DeleteUser(ctx, u.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, err := s.UserByCertThumbprint(ctx, tp); err == nil {
		t.Fatal("expected UserNotFoundError after delete")
	} else {
		var nf *UserNotFoundError
		if !errors.As(err, &nf) {
			t.Fatalf("expected UserNotFoundError, got %v", err)
		}
	}
}

// TestIsValidSPKIThumbprint verifies canonical-format validation: only a
// base64url (no padding) encoding of exactly 32 bytes is accepted.
func TestIsValidSPKIThumbprint(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	valid := ComputeSPKIThumbprint(makeCertForKey(t, key, "u", 1))
	if !IsValidSPKIThumbprint(valid) {
		t.Fatalf("computed thumbprint %q must be valid", valid)
	}
	for _, bad := range []string{"", "not-base64!!", "YWJj", valid + "AA", "abc123"} {
		if IsValidSPKIThumbprint(bad) {
			t.Errorf("thumbprint %q must be rejected as non-canonical", bad)
		}
	}
}

// TestAddCertBinding_RejectsNonCanonical ensures the identity index cannot be
// polluted with an arbitrary caller-supplied string.
func TestAddCertBinding_RejectsNonCanonical(t *testing.T) {
	s := &kvUserStore{store: newMemKVStore(), idgen: newTestIDGen(t)}
	ctx := context.Background()
	u, err := s.CreateUser(ctx, &CreateUserRequest{Username: "admin", TenantID: "system"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := s.AddCertBinding(ctx, u.ID, "not-a-canonical-thumbprint", 0); err == nil {
		t.Fatal("expected ValidationError for non-canonical thumbprint")
	} else {
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("expected *ValidationError, got %T: %v", err, err)
		}
	}
}

// TestAddCertBinding_RejectsCrossUserConflict ensures a thumbprint already
// bound to one user cannot be silently rebound to another (identity hijack).
func TestAddCertBinding_RejectsCrossUserConflict(t *testing.T) {
	s := &kvUserStore{store: newMemKVStore(), idgen: newTestIDGen(t)}
	ctx := context.Background()
	victim, _ := s.CreateUser(ctx, &CreateUserRequest{Username: "victim", TenantID: "system"})
	attacker, _ := s.CreateUser(ctx, &CreateUserRequest{Username: "attacker", TenantID: "system"})

	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tp := ComputeSPKIThumbprint(makeCertForKey(t, key, "victim", 1))

	if _, err := s.AddCertBinding(ctx, victim.ID, tp, 0); err != nil {
		t.Fatalf("bind to victim: %v", err)
	}
	_, err := s.AddCertBinding(ctx, attacker.ID, tp, 0)
	if err == nil {
		t.Fatal("expected CertBindingConflictError when rebinding to a different user")
	}
	var cce *CertBindingConflictError
	if !errors.As(err, &cce) {
		t.Fatalf("expected *CertBindingConflictError, got %T: %v", err, err)
	}
	// The thumbprint must still resolve the original (victim) user.
	got, err := s.UserByCertThumbprint(ctx, tp)
	if err != nil || got.ID != victim.ID {
		t.Fatalf("thumbprint must still resolve victim: got %v (err %v)", got, err)
	}
}

// TestUserByCertThumbprint_EnforcesExpiry ensures an expired binding does not
// resolve a user (no replay of a decommissioned credential).
func TestUserByCertThumbprint_EnforcesExpiry(t *testing.T) {
	s := &kvUserStore{store: newMemKVStore(), idgen: newTestIDGen(t)}
	ctx := context.Background()
	u, _ := s.CreateUser(ctx, &CreateUserRequest{Username: "admin", TenantID: "system"})

	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tp := ComputeSPKIThumbprint(makeCertForKey(t, key, "admin", 1))

	// Bind with an expiry one hour in the past.
	expired := time.Now().Add(-time.Hour).Unix()
	if _, err := s.AddCertBinding(ctx, u.ID, tp, expired); err != nil {
		t.Fatalf("AddCertBinding: %v", err)
	}
	if _, err := s.UserByCertThumbprint(ctx, tp); err == nil {
		t.Fatal("expired binding must not resolve a user")
	} else {
		var nf *UserNotFoundError
		if !errors.As(err, &nf) {
			t.Fatalf("expected UserNotFoundError for expired binding, got %T: %v", err, err)
		}
	}
}
