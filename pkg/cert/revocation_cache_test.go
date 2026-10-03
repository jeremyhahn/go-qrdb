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
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// countingProvider counts IsRevoked calls and answers from a script, so a test
// can tell a cached answer from a fresh lookup.
type countingProvider struct {
	CertProvider

	calls   atomic.Int64
	revoked bool
	err     error
}

func (c *countingProvider) IsRevoked(_ context.Context, _ string) (bool, error) {
	c.calls.Add(1)
	if c.err != nil {
		return false, c.err
	}
	return c.revoked, nil
}

// certWithSerial returns a certificate carrying the given serial number. Only
// the serial matters to the cache.
func certWithSerial(n int64) *x509.Certificate {
	return &x509.Certificate{SerialNumber: big.NewInt(n)}
}

func TestNewRevocationCache_NilProvider(t *testing.T) {
	// A nil provider must not yield a checker that answers "not revoked" for
	// everything; the wiring layer relies on the nil to mean "no enforcement".
	if c := NewRevocationCache(nil, time.Minute); c != nil {
		t.Fatalf("NewRevocationCache(nil, …) = %v, want nil", c)
	}
}

func TestRevocationCache_TTL(t *testing.T) {
	c := NewRevocationCache(&countingProvider{}, 42*time.Second)
	if got := c.TTL(); got != 42*time.Second {
		t.Fatalf("TTL() = %v, want 42s", got)
	}
	var nilCache *RevocationCache
	if got := nilCache.TTL(); got != 0 {
		t.Fatalf("nil cache TTL() = %v, want 0", got)
	}
}

func TestRevocationCache_RevokedIsReported(t *testing.T) {
	prov := &countingProvider{revoked: true}
	c := NewRevocationCache(prov, time.Minute)

	revoked, err := c.IsCertRevoked(context.Background(), certWithSerial(1))
	if err != nil {
		t.Fatalf("IsCertRevoked: %v", err)
	}
	if !revoked {
		t.Fatal("IsCertRevoked = false for a revoked serial")
	}
}

func TestRevocationCache_NotRevokedIsCachedForTTL(t *testing.T) {
	prov := &countingProvider{}
	c := NewRevocationCache(prov, time.Hour)
	ctx := context.Background()
	peer := certWithSerial(7)

	for range 5 {
		revoked, err := c.IsCertRevoked(ctx, peer)
		if err != nil {
			t.Fatalf("IsCertRevoked: %v", err)
		}
		if revoked {
			t.Fatal("IsCertRevoked = true for an unrevoked serial")
		}
	}
	if got := prov.calls.Load(); got != 1 {
		t.Fatalf("provider consulted %d times, want 1 (the rest served from cache)", got)
	}
}

func TestRevocationCache_NotRevokedExpires(t *testing.T) {
	prov := &countingProvider{}
	// A TTL this short is expired by the time the second call runs, without
	// the test sleeping.
	c := NewRevocationCache(prov, time.Nanosecond)
	ctx := context.Background()
	peer := certWithSerial(8)

	if _, err := c.IsCertRevoked(ctx, peer); err != nil {
		t.Fatalf("IsCertRevoked: %v", err)
	}
	if _, err := c.IsCertRevoked(ctx, peer); err != nil {
		t.Fatalf("IsCertRevoked: %v", err)
	}
	if got := prov.calls.Load(); got != 2 {
		t.Fatalf("provider consulted %d times, want 2 (the cached answer must expire)", got)
	}
}

func TestRevocationCache_ZeroTTLDisablesCaching(t *testing.T) {
	prov := &countingProvider{}
	c := NewRevocationCache(prov, 0)
	ctx := context.Background()
	peer := certWithSerial(9)

	for range 3 {
		if _, err := c.IsCertRevoked(ctx, peer); err != nil {
			t.Fatalf("IsCertRevoked: %v", err)
		}
	}
	if got := prov.calls.Load(); got != 3 {
		t.Fatalf("provider consulted %d times, want 3 (zero TTL means no caching)", got)
	}
}

func TestRevocationCache_RevokedAnswerNeverExpires(t *testing.T) {
	prov := &countingProvider{revoked: true}
	// Even with a TTL of a nanosecond, a REVOKED answer stays cached:
	// revocation is a one-way door, so the answer cannot become wrong.
	c := NewRevocationCache(prov, time.Nanosecond)
	ctx := context.Background()
	peer := certWithSerial(10)

	for range 3 {
		revoked, err := c.IsCertRevoked(ctx, peer)
		if err != nil {
			t.Fatalf("IsCertRevoked: %v", err)
		}
		if !revoked {
			t.Fatal("IsCertRevoked = false for a revoked serial")
		}
	}
	if got := prov.calls.Load(); got != 1 {
		t.Fatalf("provider consulted %d times, want 1", got)
	}
}

func TestRevocationCache_SealedBarrierIsUnavailableNotDenial(t *testing.T) {
	prov := &countingProvider{err: &BarrierSealedError{}}
	c := NewRevocationCache(prov, time.Minute)

	revoked, err := c.IsCertRevoked(context.Background(), certWithSerial(11))
	if revoked {
		t.Error("IsCertRevoked = true for a sealed barrier: that is not a revocation")
	}
	var unavailable *RevocationStateUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("error = %v (%T), want *RevocationStateUnavailableError", err, err)
	}
	if !unavailable.RevocationStateUnavailable() {
		t.Error("RevocationStateUnavailable() = false")
	}
	// The cause must survive, so an operator sees WHY there was no answer.
	var sealed *BarrierSealedError
	if !errors.As(err, &sealed) {
		t.Error("the sealed-barrier cause did not survive wrapping")
	}
}

func TestRevocationStateUnavailableError_Error(t *testing.T) {
	t.Run("without an underlying error", func(t *testing.T) {
		err := &RevocationStateUnavailableError{}
		if !strings.Contains(err.Error(), "barrier is sealed") {
			t.Errorf("unexpected message: %q", err.Error())
		}
	})

	t.Run("with an underlying error", func(t *testing.T) {
		cause := errors.New("decode failed")
		err := &RevocationStateUnavailableError{Err: cause}
		if !strings.Contains(err.Error(), "decode failed") {
			t.Errorf("expected the underlying cause in the message, got %q", err.Error())
		}
	})
}

func TestRevocationCache_LookupFailureIsNotCached(t *testing.T) {
	boom := errors.New("kv read failed")
	prov := &countingProvider{err: boom}
	c := NewRevocationCache(prov, time.Hour)
	ctx := context.Background()
	peer := certWithSerial(12)

	for range 2 {
		revoked, err := c.IsCertRevoked(ctx, peer)
		if revoked {
			t.Fatal("IsCertRevoked = true on a lookup failure")
		}
		if !errors.Is(err, boom) {
			t.Fatalf("error = %v, want the underlying lookup failure", err)
		}
	}
	// A failed lookup must not be cached as "not revoked": that would turn one
	// transient read error into a TTL-long fail-open.
	if got := prov.calls.Load(); got != 2 {
		t.Fatalf("provider consulted %d times, want 2", got)
	}
}

func TestRevocationCache_NilReceiverAndNilCert(t *testing.T) {
	ctx := context.Background()

	var nilCache *RevocationCache
	if revoked, err := nilCache.IsCertRevoked(ctx, certWithSerial(1)); revoked || err != nil {
		t.Fatalf("nil cache = (%v, %v), want (false, nil)", revoked, err)
	}

	c := NewRevocationCache(&countingProvider{}, time.Minute)
	if revoked, err := c.IsCertRevoked(ctx, nil); revoked || err != nil {
		t.Fatalf("nil cert = (%v, %v), want (false, nil)", revoked, err)
	}
}

func TestRevocationCache_CertWithoutSerialIsDenied(t *testing.T) {
	prov := &countingProvider{}
	c := NewRevocationCache(prov, time.Minute)

	// A certificate with no serial cannot be matched against a marker, so no
	// revocation decision could ever apply to it. Deny rather than admit.
	revoked, err := c.IsCertRevoked(context.Background(), &x509.Certificate{})
	if err != nil {
		t.Fatalf("IsCertRevoked: %v", err)
	}
	if !revoked {
		t.Fatal("a certificate with no serial was admitted")
	}
	if got := prov.calls.Load(); got != 0 {
		t.Fatalf("provider consulted %d times for a serial-less certificate, want 0", got)
	}
}

func TestRevocationCache_ResetsAtBound(t *testing.T) {
	prov := &countingProvider{}
	c := NewRevocationCache(prov, time.Hour)
	ctx := context.Background()

	for i := range int64(defaultRevocationCacheMaxEntries + 2) {
		if _, err := c.IsCertRevoked(ctx, certWithSerial(i+1)); err != nil {
			t.Fatalf("IsCertRevoked: %v", err)
		}
	}
	if got := c.count.Load(); got > defaultRevocationCacheMaxEntries {
		t.Fatalf("cache holds %d entries, want at most %d", got, defaultRevocationCacheMaxEntries)
	}
}

// TestRevocationCache_AgainstRealProvider is the end-to-end shape: issue a
// certificate through the real CA, revoke it through the real service, and
// assert the cache reports it revoked from the parsed certificate alone —
// which is all the authentication layer has.
func TestRevocationCache_AgainstRealProvider(t *testing.T) {
	p, _, _ := initializeProvider(t)
	ctx := context.Background()

	bundle, err := p.GenerateClientCert(ctx, &GenerateRequest{Username: "alice", TenantID: "acme"})
	if err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}
	block, _ := pem.Decode(bundle.CertPEM)
	if block == nil {
		t.Fatal("decode cert PEM: no block")
	}
	peer, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	// No caching, so the two answers below are two real lookups.
	c := NewRevocationCache(p, 0)

	revoked, err := c.IsCertRevoked(ctx, peer)
	if err != nil {
		t.Fatalf("IsCertRevoked before revocation: %v", err)
	}
	if revoked {
		t.Fatal("a freshly issued certificate reported as revoked")
	}

	if _, err := p.RevokeCert(ctx, bundle.Serial); err != nil {
		t.Fatalf("RevokeCert: %v", err)
	}

	revoked, err = c.IsCertRevoked(ctx, peer)
	if err != nil {
		t.Fatalf("IsCertRevoked after revocation: %v", err)
	}
	if !revoked {
		t.Fatal("a revoked certificate reported as not revoked")
	}
}
