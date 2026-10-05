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

package cert

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// FormatSerial
// ---------------------------------------------------------------------------

func TestFormatSerial_MatchesIssuedRecordKey(t *testing.T) {
	p, kv, _ := initializeProvider(t)
	ctx := context.Background()

	bundle, err := p.GenerateClientCert(ctx, &GenerateRequest{Username: "alice", TenantID: "acme"})
	if err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}

	block, _ := pem.Decode(bundle.CertPEM)
	if block == nil {
		t.Fatal("decode cert PEM: no block")
	}
	parsed, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	// The whole enforcement path depends on this equality: the auth layer
	// derives a lookup key from the parsed certificate, and the CA wrote the
	// record and marker from the big.Int it minted.
	if got := FormatSerial(parsed.SerialNumber); got != bundle.Serial {
		t.Fatalf("FormatSerial(parsed) = %q, issued serial = %q", got, bundle.Serial)
	}
	if _, ok := kv.data[prefixCerts+"acme/"+bundle.Serial]; !ok {
		t.Fatalf("no cert record under %q", prefixCerts+"acme/"+bundle.Serial)
	}
}

func TestFormatSerial_Nil(t *testing.T) {
	if got := FormatSerial(nil); got != "" {
		t.Fatalf("FormatSerial(nil) = %q, want empty", got)
	}
}

func TestFormatSerial_Value(t *testing.T) {
	if got := FormatSerial(big.NewInt(255)); got != "ff" {
		t.Fatalf("FormatSerial(255) = %q, want %q", got, "ff")
	}
}

// ---------------------------------------------------------------------------
// RevokeCert: marker payload, ordering, and unknown-serial reporting
// ---------------------------------------------------------------------------

// readEntry decodes the revocation marker for serial, failing the test when it
// is absent.
func readEntry(t *testing.T, kv *mockKVStore, serial string) revocationEntry {
	t.Helper()
	raw, ok := kv.data[prefixCertsRevoked+serial]
	if !ok {
		t.Fatalf("no revocation marker for serial %q", serial)
	}
	var entry revocationEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatalf("unmarshal revocation marker: %v", err)
	}
	return entry
}

func TestRevokeCert_MarkerCarriesCertificateExpiry(t *testing.T) {
	p, kv, _ := initializeProvider(t)
	ctx := context.Background()

	bundle, err := p.GenerateClientCert(ctx, &GenerateRequest{
		Username: "alice", TenantID: "acme", TTL: 2 * time.Hour,
	})
	if err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}
	if _, err := p.RevokeCert(ctx, bundle.Serial); err != nil {
		t.Fatalf("RevokeCert: %v", err)
	}

	entry := readEntry(t, kv, bundle.Serial)
	if entry.Serial != bundle.Serial {
		t.Errorf("entry.Serial = %q, want %q", entry.Serial, bundle.Serial)
	}
	if entry.RevokedAt.IsZero() {
		t.Error("entry.RevokedAt is zero")
	}
	// Without the expiry the marker can never be reaped, and the namespace
	// grows forever.
	if entry.NotAfter.IsZero() {
		t.Fatal("entry.NotAfter is zero for a certificate this node issued")
	}
	if delta := time.Until(entry.NotAfter); delta <= time.Hour || delta > 2*time.Hour {
		t.Errorf("entry.NotAfter = %v, want roughly 2h out", entry.NotAfter)
	}
}

func TestRevokeCert_UnknownSerial_StillRevokes(t *testing.T) {
	p, kv, _ := initializeProvider(t)
	ctx := context.Background()

	// Deny-by-serial: revoking a serial this node holds no record for is a
	// real CA operation — the certificate may have been issued by a peer, or
	// its record pruned — and it must take effect.
	if _, err := p.RevokeCert(ctx, "deadbeef"); err != nil {
		t.Fatalf("RevokeCert(unknown serial) = %v, want nil: both outcomes are successes", err)
	}
	if _, ok := kv.data[prefixCertsRevoked+"deadbeef"]; !ok {
		t.Fatal("revocation marker missing: deny-by-serial must still take effect")
	}
	revoked, err := p.IsRevoked(ctx, "deadbeef")
	if err != nil {
		t.Fatalf("IsRevoked: %v", err)
	}
	if !revoked {
		t.Fatal("IsRevoked(unknown but revoked serial) = false")
	}

	// No local record means no knowable expiry, so the marker is permanent.
	if entry := readEntry(t, kv, "deadbeef"); !entry.NotAfter.IsZero() {
		t.Errorf("entry.NotAfter = %v, want zero for a serial with no local record", entry.NotAfter)
	}
}

func TestRevokeCert_KnownSerial_ReportsSuccess(t *testing.T) {
	p, _, _ := initializeProvider(t)
	ctx := context.Background()

	bundle, err := p.GenerateClientCert(ctx, &GenerateRequest{Username: "alice", TenantID: "acme"})
	if err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}
	if _, err := p.RevokeCert(ctx, bundle.Serial); err != nil {
		t.Fatalf("RevokeCert(known serial) = %v, want nil", err)
	}
}

func TestRevokeCert_ScanFailureLeavesNoMarker(t *testing.T) {
	kv := &scanErrorKVStore{mockKVStore: newMockKVStore()}
	p := NewDefaultProvider(kv, newMockBarrier())
	ctx := context.Background()

	if _, err := p.RevokeCert(ctx, "some-serial"); err == nil {
		t.Fatal("RevokeCert with a failing scan returned nil")
	}
	// The lookup runs BEFORE the write, so a failed revocation must not leave
	// a marker behind that nothing reported.
	if _, ok := kv.data[prefixCertsRevoked+"some-serial"]; ok {
		t.Fatal("revocation marker written despite the revocation failing")
	}
}

// scanErrorKVStore fails every Scan and records Puts through the embedded
// store, so a test can assert on what was written before the failure.
type scanErrorKVStore struct {
	*mockKVStore
}

func (s *scanErrorKVStore) Scan(_ context.Context, _ string, _ func(key string, value []byte) error) error {
	return errors.New("scan failed")
}

// ---------------------------------------------------------------------------
// IsRevoked reads the marker's PRESENCE, never its payload
// ---------------------------------------------------------------------------

func TestIsRevoked_LegacyMarkerValue(t *testing.T) {
	p, kv, _ := initializeProvider(t)
	ctx := context.Background()

	// A marker written by a build that stored the literal "1".
	if err := kv.Put(ctx, prefixCertsRevoked+"legacy", []byte("1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	revoked, err := p.IsRevoked(ctx, "legacy")
	if err != nil {
		t.Fatalf("IsRevoked: %v", err)
	}
	if !revoked {
		t.Fatal("IsRevoked(legacy marker) = false: a marker must revoke whatever its payload")
	}
}

func TestIsRevoked_CorruptMarkerStillRevokes(t *testing.T) {
	p, kv, _ := initializeProvider(t)
	ctx := context.Background()

	if err := kv.Put(ctx, prefixCertsRevoked+"corrupt", []byte("{not json")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	revoked, err := p.IsRevoked(ctx, "corrupt")
	if err != nil {
		t.Fatalf("IsRevoked: %v", err)
	}
	if !revoked {
		t.Fatal("IsRevoked(corrupt marker) = false: reading a revocation must fail closed")
	}
}

// ---------------------------------------------------------------------------
// ReapExpiredRevocations
// ---------------------------------------------------------------------------

// putEntry writes a revocation marker directly, for reaping tests that need a
// specific expiry.
func putEntry(t *testing.T, kv *mockKVStore, serial string, notAfter time.Time) {
	t.Helper()
	raw, err := json.Marshal(&revocationEntry{
		Serial:    serial,
		RevokedAt: time.Now().UTC(),
		NotAfter:  notAfter,
	})
	if err != nil {
		t.Fatalf("marshal entry: %v", err)
	}
	if err := kv.Put(context.Background(), prefixCertsRevoked+serial, raw); err != nil {
		t.Fatalf("Put: %v", err)
	}
}

func TestReapExpiredRevocations_DropsOnlyExpired(t *testing.T) {
	p, kv, _ := initializeProvider(t)
	ctx := context.Background()

	now := time.Now().UTC()
	putEntry(t, kv, "expired", now.Add(-time.Hour))
	putEntry(t, kv, "live", now.Add(time.Hour))
	putEntry(t, kv, "undated", time.Time{})
	if err := kv.Put(ctx, prefixCertsRevoked+"legacy", []byte("1")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	reaped, err := p.ReapExpiredRevocations(ctx)
	if err != nil {
		t.Fatalf("ReapExpiredRevocations: %v", err)
	}
	if reaped != 1 {
		t.Fatalf("reaped = %d, want 1", reaped)
	}

	for _, tc := range []struct {
		serial string
		want   bool
	}{
		{"expired", false},
		{"live", true},
		{"undated", true},
		{"legacy", true},
	} {
		revoked, err := p.IsRevoked(ctx, tc.serial)
		if err != nil {
			t.Fatalf("IsRevoked(%q): %v", tc.serial, err)
		}
		if revoked != tc.want {
			t.Errorf("IsRevoked(%q) = %v, want %v", tc.serial, revoked, tc.want)
		}
	}
}

func TestReapExpiredRevocations_WhenSealed(t *testing.T) {
	p, _, barrier := initializeProvider(t)
	barrier.seal()

	_, err := p.ReapExpiredRevocations(context.Background())
	var sealed *BarrierSealedError
	if !errors.As(err, &sealed) {
		t.Fatalf("ReapExpiredRevocations while sealed = %v, want *BarrierSealedError", err)
	}
}

func TestReapExpiredRevocations_ScanError(t *testing.T) {
	kv := &scanErrorKVStore{mockKVStore: newMockKVStore()}
	p := NewDefaultProvider(kv, newMockBarrier())

	if _, err := p.ReapExpiredRevocations(context.Background()); err == nil {
		t.Fatal("ReapExpiredRevocations with a failing scan returned nil")
	}
}

func TestRevokeCert_ReapsExpiredMarkers(t *testing.T) {
	p, kv, _ := initializeProvider(t)
	ctx := context.Background()

	putEntry(t, kv, "stale", time.Now().UTC().Add(-time.Hour))

	bundle, err := p.GenerateClientCert(ctx, &GenerateRequest{Username: "alice", TenantID: "acme"})
	if err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}
	if _, err := p.RevokeCert(ctx, bundle.Serial); err != nil {
		t.Fatalf("RevokeCert: %v", err)
	}

	// Every revocation bounds the namespace; without this the only bound is
	// the number of certificates ever issued.
	if _, ok := kv.data[prefixCertsRevoked+"stale"]; ok {
		t.Fatal("expired marker survived a revocation")
	}
	if _, ok := kv.data[prefixCertsRevoked+bundle.Serial]; !ok {
		t.Fatal("the marker just written was reaped")
	}
}

// TestRevokeCert_ReapFailureIsWarnedNotReturned proves a failure reaping
// expired markers never turns a completed revocation into a reported
// failure: the new marker this call just wrote is already durable by the
// time the best-effort reap runs, so RevokeCert must still succeed even
// though an expired marker could not be cleaned up.
func TestRevokeCert_ReapFailureIsWarnedNotReturned(t *testing.T) {
	p, kv, _ := initializeProvider(t)
	ctx := context.Background()

	putEntry(t, kv, "stale", time.Now().UTC().Add(-time.Hour))

	bundle, err := p.GenerateClientCert(ctx, &GenerateRequest{Username: "bob", TenantID: "acme"})
	if err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}

	p.kvStore = &partialErrorKVStore{mockKVStore: kv, failDelete: true}

	result, err := p.RevokeCert(ctx, bundle.Serial)
	if err != nil {
		t.Fatalf("RevokeCert must succeed even when the best-effort reap fails: %v", err)
	}
	if result.Serial != bundle.Serial {
		t.Fatalf("result.Serial = %q, want %q", result.Serial, bundle.Serial)
	}
	// The expired marker the failing reap could not delete is still there.
	if _, ok := kv.data[prefixCertsRevoked+"stale"]; !ok {
		t.Fatal("expired marker should still be present: the injected delete failure must have left it")
	}
}
