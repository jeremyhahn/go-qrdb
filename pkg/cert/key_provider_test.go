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
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
)

// ---------------------------------------------------------------------------
// WithKeyProvider / the injected cakey.Provider path
//
// These tests close the "injected HSM-style provider double" requirement:
// an embedder's cakey.Provider (shaped like go-xkms's CASigningIdentity +
// CACertificateAccessor, composed through cakey.Adapter -- see
// docs/certificate-authority/architecture.md, "cakey.Adapter") signs every
// Key B certificate, and the built-in per-tenant software path is never
// touched: no wrapped key or cert PEM is ever written under prefixCertCA.
// ---------------------------------------------------------------------------

// hsmDouble is a same-shaped-as-xkms signing identity double: it holds its
// own in-memory key and certificate and counts Sign calls, so tests can
// prove every signature actually went through it.
type hsmDouble struct {
	key       *ecdsa.PrivateKey
	cert      *x509.Certificate
	signCalls int
	certCalls int
	signErr   error
	certErr   error
}

func newHSMDouble(t *testing.T) *hsmDouble {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(42),
		Subject:               pkix.Name{CommonName: "hsm-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return &hsmDouble{key: key, cert: cert}
}

// signerOnly wraps an *ecdsa.PrivateKey and exposes ONLY crypto.Signer's two
// methods (Public, Sign) -- never the concrete *ecdsa.PrivateKey, whose
// other fields and methods reach D, the private scalar, directly. A test
// (or production code) that type-asserted hsmDouble's returned
// crypto.Signer back to *ecdsa.PrivateKey would otherwise defeat the point
// of these tests: that DefaultCertProvider touches an injected provider's
// key ONLY by calling through crypto.Signer, exactly as it would a real
// HSM/TPM/KMS-backed cakey.Adapter (go-qrdb#560 review, non-blocking).
type signerOnly struct {
	key *ecdsa.PrivateKey
}

func (s signerOnly) Public() crypto.PublicKey { return s.key.Public() }

func (s signerOnly) Sign(rnd io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	return s.key.Sign(rnd, digest, opts)
}

var _ crypto.Signer = signerOnly{}

// provider returns a cakey.Adapter over this double, exactly the "no glue
// code beyond cakey.Adapter" shape docs/certificate-authority/architecture.md
// describes for an xkms-backed CA.
func (d *hsmDouble) provider() cakey.Adapter {
	return cakey.Adapter{
		SignerFunc: func(context.Context) (crypto.Signer, error) {
			d.signCalls++
			if d.signErr != nil {
				return nil, d.signErr
			}
			return signerOnly{key: d.key}, nil
		},
		CertificateFunc: func(context.Context) (*x509.Certificate, error) {
			d.certCalls++
			if d.certErr != nil {
				return nil, d.certErr
			}
			return d.cert, nil
		},
	}
}

// hsmFactory implements cakey.ProviderFactory over a set of per-tenant
// hsmDoubles, keyed by tenant ID -- so ProviderForTenant NEVER resolves the
// same Provider for two different tenants, the owner's per-tenant-keys
// ruling (go-qrdb#591's review). There is deliberately no constructor that
// wraps a single hsmDouble behind every tenant ID: a test that wants a
// single-tenant injected provider says so explicitly via
// singleTenantHSMFactory, naming the one tenant it backs.
type hsmFactory struct {
	byTenant map[string]*hsmDouble
}

// errUnknownHSMTenant is returned by ProviderForTenant for a tenant ID the
// factory was never configured with -- standing in for an HSM/KMS backend
// that genuinely has no key for a tenant it has never provisioned one for.
var errUnknownHSMTenant = errors.New("hsmFactory: no provider configured for this tenant")

// ProviderForTenant implements cakey.ProviderFactory.
func (f *hsmFactory) ProviderForTenant(tenantID string) (cakey.Provider, error) {
	d, ok := f.byTenant[tenantID]
	if !ok {
		return nil, errUnknownHSMTenant
	}
	return d.provider(), nil
}

// TenantIDs implements cakey.ProviderFactory.
func (f *hsmFactory) TenantIDs(context.Context) ([]string, error) {
	ids := make([]string, 0, len(f.byTenant))
	for id := range f.byTenant {
		ids = append(ids, id)
	}
	return ids, nil
}

// Compile-time interface check.
var _ cakey.ProviderFactory = (*hsmFactory)(nil)

// singleTenantHSMFactory wraps hsm behind cakey.ProviderFactory for exactly
// one tenant ID -- the shape every single-tenant test in this file uses,
// now that a bare cakey.Provider can no longer be injected directly (see
// cakey.ProviderFactory's own doc comment for why that shortcut is gone).
func singleTenantHSMFactory(tenantID string, hsm *hsmDouble) *hsmFactory {
	return &hsmFactory{byTenant: map[string]*hsmDouble{tenantID: hsm}}
}

func TestDefaultCertProvider_WithKeyProvider_SignsThroughInjectedProvider(t *testing.T) {
	hsm := newHSMDouble(t)
	kv := newMockKVStore()
	barrier := newMockBarrier()
	p := NewDefaultProvider(kv, barrier, WithKeyProviderFactory(singleTenantHSMFactory("acme", hsm)))
	ctx := context.Background()

	bundle, err := generateForTest(ctx, p, &testRequest{Username: "alice", TenantID: "acme"})
	if err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}
	if hsm.signCalls == 0 {
		t.Error("expected the injected provider's Signer to be called")
	}
	if hsm.certCalls == 0 {
		t.Error("expected the injected provider's Certificate to be called")
	}

	leaf := parsePEMCert(t, bundle.CertPEM)
	if err := leaf.CheckSignatureFrom(hsm.cert); err != nil {
		t.Errorf("leaf was not signed by the injected provider's certificate: %v", err)
	}

	// No software key or cert material was ever written under prefixCertCA:
	// the built-in per-tenant default is never constructed when a provider
	// is injected.
	keys, err := kv.List(ctx, prefixCertCA)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("expected no software CA material stored, got keys: %v", keys)
	}
}

func TestDefaultCertProvider_WithKeyProvider_InitializeAndUnseal(t *testing.T) {
	hsm := newHSMDouble(t)
	p := NewDefaultProvider(newMockKVStore(), newMockBarrier(), WithKeyProviderFactory(singleTenantHSMFactory("system", hsm)))
	ctx := context.Background()

	if err := p.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if pems, err := p.CACertPEMs(ctx); err != nil || len(pems) != 1 || len(pems[0]) == 0 {
		t.Errorf("expected the injected provider's certificate after Initialize, got %v, err=%v", pems, err)
	}

	if err := p.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if err := p.Unseal(ctx); err != nil {
		t.Fatalf("Unseal: %v", err)
	}
}

func TestDefaultCertProvider_WithKeyProvider_SignerError(t *testing.T) {
	hsm := newHSMDouble(t)
	hsm.signErr = errors.New("device unavailable")
	p := NewDefaultProvider(newMockKVStore(), newMockBarrier(), WithKeyProviderFactory(singleTenantHSMFactory("acme", hsm)))

	_, err := generateForTest(context.Background(), p, &testRequest{Username: "bob", TenantID: "acme"})
	if err == nil {
		t.Fatal("expected error when the injected provider's Signer fails")
	}
	var cge *CertGenerateError
	if !errors.As(err, &cge) {
		t.Fatalf("expected *CertGenerateError, got %T: %v", err, err)
	}
}

func TestDefaultCertProvider_WithKeyProvider_CertificateError(t *testing.T) {
	hsm := newHSMDouble(t)
	hsm.certErr = errors.New("device unavailable")
	p := NewDefaultProvider(newMockKVStore(), newMockBarrier(), WithKeyProviderFactory(singleTenantHSMFactory("acme", hsm)))

	_, err := generateForTest(context.Background(), p, &testRequest{Username: "bob", TenantID: "acme"})
	if err == nil {
		t.Fatal("expected error when the injected provider's Certificate fails")
	}
}

// ---------------------------------------------------------------------------
// CACertPEMs
// ---------------------------------------------------------------------------

func TestDefaultCertProvider_CACertPEMs_BuiltInMultiTenant(t *testing.T) {
	p, _, _ := newTestProvider()
	ctx := context.Background()

	for _, tenantID := range []string{"acme", "globex"} {
		if _, err := generateForTest(ctx, p, &testRequest{Username: "alice", TenantID: tenantID}); err != nil {
			t.Fatalf("GenerateClientCert(%s): %v", tenantID, err)
		}
	}

	pems, err := p.CACertPEMs(ctx)
	if err != nil {
		t.Fatalf("CACertPEMs: %v", err)
	}
	if len(pems) != 2 {
		t.Fatalf("CACertPEMs returned %d entries, want 2", len(pems))
	}
	for _, certPEM := range pems {
		if parsePEMCert(t, certPEM) == nil {
			t.Error("CACertPEMs entry did not parse as a certificate")
		}
	}
}

// TestDefaultCertProvider_ResolveKeyProvider_RevalidatesPastTTL is
// go-qrdb#591's review (non-blocking defense in depth) red/green proof: a
// perTenant entry older than perTenantRevalidateTTL must be rebuilt by
// resolveKeyProvider on the next call, independent of whatever
// WatchForDeletions would otherwise have evicted it for -- the TTL is the
// backstop for a watch event that never arrives (the hub never delivered
// it, or some future teardown path deletes the record a way this package
// has no watch on).
func TestDefaultCertProvider_ResolveKeyProvider_RevalidatesPastTTL(t *testing.T) {
	p, _, _ := newTestProvider()

	first, err := p.resolveKeyProvider("acme")
	if err != nil {
		t.Fatalf("resolveKeyProvider: %v", err)
	}

	p.mu.Lock()
	entry, ok := p.perTenant["acme"]
	if !ok {
		p.mu.Unlock()
		t.Fatal("expected acme to be cached in perTenant after the first resolve")
	}
	entry.builtAt = time.Now().Add(-perTenantRevalidateTTL - time.Second)
	p.mu.Unlock()

	second, err := p.resolveKeyProvider("acme")
	if err != nil {
		t.Fatalf("resolveKeyProvider (after TTL expiry): %v", err)
	}
	if second == first {
		t.Fatal("expected a perTenant entry older than perTenantRevalidateTTL to be rebuilt, not reused")
	}

	// A fresh resolve immediately afterward must NOT rebuild again: the
	// revalidation is bounded by the TTL, not unconditional.
	third, err := p.resolveKeyProvider("acme")
	if err != nil {
		t.Fatalf("resolveKeyProvider (immediately after revalidation): %v", err)
	}
	if third != second {
		t.Fatal("expected the freshly-revalidated entry to be reused, not rebuilt again immediately")
	}
}

func TestDefaultCertProvider_CACertPEMs_Empty(t *testing.T) {
	p, _, _ := newTestProvider()
	pems, err := p.CACertPEMs(context.Background())
	if err != nil {
		t.Fatalf("CACertPEMs: %v", err)
	}
	if len(pems) != 0 {
		t.Errorf("expected no entries before any tenant's CA is established, got %d", len(pems))
	}
}

func TestDefaultCertProvider_CACertPEMs_ScanError(t *testing.T) {
	p := NewDefaultProvider(&errorKVStore{err: errors.New("disk read failed")}, newMockBarrier())
	_, err := p.CACertPEMs(context.Background())
	if err == nil {
		t.Fatal("expected error when the KVStore scan fails")
	}
	var se *StoreError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

// TestDefaultCertProvider_CACertPEMs_BarrierSealed proves CACertPEMs checks
// the barrier explicitly rather than relying on the KVStore scan to fail on
// its own: outside Raft mode the plaintext cert_pem records are reachable
// even while sealed, so without this check a runtime reseal would NOT make
// the dynamic client-auth trust pool drop Key B (go-qrdb#560 review,
// non-blocking).
func TestDefaultCertProvider_CACertPEMs_BarrierSealed(t *testing.T) {
	p, _, barrier := initializeProvider(t)
	if _, err := generateForTest(context.Background(), p, &testRequest{Username: "alice", TenantID: "system"}); err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}

	barrier.seal()

	_, err := p.CACertPEMs(context.Background())
	if err == nil {
		t.Fatal("expected an error once the barrier is sealed, even though the records are plaintext")
	}
	var bsErr *BarrierSealedError
	if !errors.As(err, &bsErr) {
		t.Fatalf("expected *BarrierSealedError, got %T: %v", err, err)
	}
}

func TestDefaultCertProvider_CACertPEMs_InjectedProvider(t *testing.T) {
	hsm := newHSMDouble(t)
	p := NewDefaultProvider(newMockKVStore(), newMockBarrier(), WithKeyProviderFactory(singleTenantHSMFactory("acme", hsm)))

	pems, err := p.CACertPEMs(context.Background())
	if err != nil {
		t.Fatalf("CACertPEMs: %v", err)
	}
	if len(pems) != 1 {
		t.Fatalf("CACertPEMs returned %d entries, want 1", len(pems))
	}
	if parsePEMCert(t, pems[0]).Subject.CommonName != "hsm-ca" {
		t.Error("CACertPEMs did not return the injected provider's certificate")
	}
}

// TestDefaultCertProvider_CACertPEMs_InjectedProviderError proves a single
// factory-backed tenant's load failure is skipped, not propagated as
// CACertPEMs' own error -- the same B3 skip-and-count behavior the built-in
// per-tenant path gets (go-qrdb#591's review): one tenant's broken Key B CA
// must not withdraw Key B trust for every OTHER tenant the dynamic trust
// pool would otherwise widen with.
func TestDefaultCertProvider_CACertPEMs_InjectedProviderError(t *testing.T) {
	hsm := newHSMDouble(t)
	hsm.certErr = errors.New("device unavailable")
	p := NewDefaultProvider(newMockKVStore(), newMockBarrier(), WithKeyProviderFactory(singleTenantHSMFactory("acme", hsm)))

	pems, err := p.CACertPEMs(context.Background())
	if err != nil {
		t.Fatalf("CACertPEMs: %v, want the broken tenant skipped rather than propagated", err)
	}
	if len(pems) != 0 {
		t.Errorf("expected no entries (the one configured tenant's load failed), got %d", len(pems))
	}
	if got := p.SkippedTenantCACount(); got != 1 {
		t.Errorf("SkippedTenantCACount() = %d, want 1", got)
	}
}

// TestDefaultCertProvider_CACertPEMs_SkipsNonMaterialKeys proves the scan
// callback ignores any key under the CA prefix that is not itself a
// "/material" record -- e.g. a future sibling record type sharing the same
// tenant prefix -- rather than mis-parsing it as a tenant ID.
func TestDefaultCertProvider_CACertPEMs_SkipsNonMaterialKeys(t *testing.T) {
	kv := newMockKVStore()
	p := NewDefaultProvider(kv, newMockBarrier())
	ctx := context.Background()

	if _, err := generateForTest(ctx, p, &testRequest{Username: "alice", TenantID: "acme"}); err != nil {
		t.Fatalf("IssueClientCert: %v", err)
	}
	// A sibling record under the same tenant's CA prefix that is NOT the
	// "/material" record.
	if err := kv.Put(ctx, prefixCertCA+"acme/some-other-record", []byte("irrelevant")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	pems, err := p.CACertPEMs(ctx)
	if err != nil {
		t.Fatalf("CACertPEMs: %v", err)
	}
	if len(pems) != 1 {
		t.Fatalf("CACertPEMs returned %d entries, want 1 (the sibling record must be skipped, not double-counted or mis-parsed)", len(pems))
	}
}

// TestDefaultCertProvider_CACertPEMs_SkipsEmptyTenantIDKey proves the scan
// callback ignores a key that reduces to an empty tenant ID -- the
// degenerate "__sys:cert_ca//material" shape, with no tenant segment
// between the prefix and the suffix -- rather than resolving a key
// provider for "".
func TestDefaultCertProvider_CACertPEMs_SkipsEmptyTenantIDKey(t *testing.T) {
	kv := newMockKVStore()
	p := NewDefaultProvider(kv, newMockBarrier())
	ctx := context.Background()

	if err := kv.Put(ctx, prefixCertCA+materialRecordSuffix, []byte("irrelevant")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	pems, err := p.CACertPEMs(ctx)
	if err != nil {
		t.Fatalf("CACertPEMs: %v", err)
	}
	if len(pems) != 0 {
		t.Fatalf("CACertPEMs returned %d entries, want 0 (the empty-tenant-ID key must be skipped)", len(pems))
	}
}

// TestDefaultCertProvider_CACertPEMs_DiscoveredTenantLoadFails proves
// CACertPEMs propagates a failure to load a DISCOVERED tenant's
// certificate -- rather than silently omitting it or returning a partial,
// unsorted result -- when its certificate record does not decode.
//
// The material record (which discovery scans for) is left intact and
// valid-looking so "ghost" is actually discovered as a tenant; only the
// certificate record is corrupted. This also proves the load-only,
// cert-only read (loadOnlyCertificate -> cakey.SoftwareProvider.
// CertificateOnly) never touches the key record for this failure to
// surface: if it did, a corrupt KEY would have to be the one that fails
// this call, not a corrupt certificate.
// TestDefaultCertProvider_CACertPEMs_DiscoveredTenantLoadFails proves a
// single tenant's corrupt or unreadable record withdraws only that
// tenant's own anchor, never the whole call (go-qrdb#591's review, B3): the
// old behavior -- CACertPEMs failing outright -- cascaded into
// pkg/clienttrust.Pool's isRegisteredKeyBCA answering "uncertain" forever
// for EVERY issuer whenever one tenant's record was corrupt, which in turn
// refused the static exemption Key A and every operator/harness login
// relies on, locking out principals that have nothing to do with the
// broken tenant at all.
func TestDefaultCertProvider_CACertPEMs_DiscoveredTenantLoadFails(t *testing.T) {
	kv := newMockKVStore()
	p := NewDefaultProvider(kv, newMockBarrier())
	ctx := context.Background()

	// Any non-empty value makes "ghost" discoverable -- discovery only
	// checks for the key's existence, never its content.
	if err := kv.Put(ctx, prefixCertCA+"ghost/material", []byte("irrelevant-to-discovery")); err != nil {
		t.Fatalf("Put material: %v", err)
	}
	// The certificate record itself is corrupt: not valid DER.
	if err := kv.Put(ctx, prefixCertCA+"ghost/certificate", []byte("not-der-data")); err != nil {
		t.Fatalf("Put certificate: %v", err)
	}
	// A second, healthy tenant alongside the corrupt one, so a pass here
	// is not merely "the only tenant was skipped and nothing was ever
	// enumerated" -- it proves the healthy tenant's own anchor survives.
	if _, err := generateForTest(ctx, p, &testRequest{Username: "alice", TenantID: "healthy-tenant"}); err != nil {
		t.Fatalf("establish the healthy tenant's own Key B CA: %v", err)
	}

	pems, err := p.CACertPEMs(ctx)
	if err != nil {
		t.Fatalf("expected CACertPEMs to succeed, skipping the corrupt tenant, got: %v", err)
	}
	if len(pems) != 1 {
		t.Fatalf("CACertPEMs returned %d certificate(s), want exactly 1 (the healthy tenant's, with the corrupt one skipped)", len(pems))
	}
	if got := p.SkippedTenantCACount(); got != 1 {
		t.Errorf("SkippedTenantCACount() = %d, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// IsTenantIssuer -- the mTLS authenticator's issuer-tenant binding seam
// ---------------------------------------------------------------------------

func TestDefaultCertProvider_IsTenantIssuer_OwnCAMatches(t *testing.T) {
	p, _, _ := newTestProvider()
	ctx := context.Background()

	if _, err := generateForTest(ctx, p, &testRequest{Username: "alice", TenantID: "acme"}); err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}
	pems, err := p.CACertPEMs(ctx)
	if err != nil || len(pems) != 1 {
		t.Fatalf("CACertPEMs: %v (len=%d)", err, len(pems))
	}
	acmeCA := parsePEMCert(t, pems[0])

	ok, err := p.IsTenantIssuer(ctx, "acme", acmeCA)
	if err != nil {
		t.Fatalf("IsTenantIssuer: %v", err)
	}
	if !ok {
		t.Error("expected acme's own CA certificate to match IsTenantIssuer(ctx, \"acme\", ...)")
	}
}

func TestDefaultCertProvider_IsTenantIssuer_ForeignCADoesNotMatch(t *testing.T) {
	p, _, _ := newTestProvider()
	ctx := context.Background()

	for _, tenantID := range []string{"acme", "globex"} {
		if _, err := generateForTest(ctx, p, &testRequest{Username: "alice", TenantID: tenantID}); err != nil {
			t.Fatalf("GenerateClientCert(%s): %v", tenantID, err)
		}
	}
	_, globexCA, err := p.getCAMaterial(ctx, "globex")
	if err != nil {
		t.Fatalf("getCAMaterial: %v", err)
	}

	ok, err := p.IsTenantIssuer(ctx, "acme", globexCA)
	if err != nil {
		t.Fatalf("IsTenantIssuer: %v", err)
	}
	if ok {
		t.Error("expected globex's CA certificate NOT to match IsTenantIssuer(ctx, \"acme\", ...)")
	}
}

// TestDefaultCertProvider_IsTenantIssuer_InjectedFactory_ForeignTenantDoesNotMatch
// is the owner's required regression test (go-qrdb#591's review): an
// embedder's injected cakey.ProviderFactory backing TWO tenants with two
// DIFFERENT keys must bind each tenant's logins to its OWN issuer only --
// tenant-x's CA certificate must never satisfy IsTenantIssuer for
// tenant-y, or vice versa. Before resolveKeyProvider routed through
// ProviderFactory.ProviderForTenant(tenantID), a single injected
// cakey.Provider was shared across every tenant, which made this check
// vacuously true for any tenant a presented leaf's Subject OU merely
// claimed.
func TestDefaultCertProvider_IsTenantIssuer_InjectedFactory_ForeignTenantDoesNotMatch(t *testing.T) {
	hsmX := newHSMDouble(t)
	hsmY := newHSMDouble(t)
	factory := &hsmFactory{byTenant: map[string]*hsmDouble{
		"tenant-x": hsmX,
		"tenant-y": hsmY,
	}}
	p := NewDefaultProvider(newMockKVStore(), newMockBarrier(), WithKeyProviderFactory(factory))
	ctx := context.Background()

	if _, err := generateForTest(ctx, p, &testRequest{Username: "alice", TenantID: "tenant-x"}); err != nil {
		t.Fatalf("IssueClientCert(tenant-x): %v", err)
	}
	if _, err := generateForTest(ctx, p, &testRequest{Username: "bob", TenantID: "tenant-y"}); err != nil {
		t.Fatalf("IssueClientCert(tenant-y): %v", err)
	}

	tenantXIssuer := hsmX.cert
	tenantYIssuer := hsmY.cert

	ok, err := p.IsTenantIssuer(ctx, "tenant-x", tenantXIssuer)
	if err != nil {
		t.Fatalf("IsTenantIssuer(tenant-x, its own CA): %v", err)
	}
	if !ok {
		t.Error("expected tenant-x's own CA certificate to match IsTenantIssuer(ctx, \"tenant-x\", ...)")
	}

	ok, err = p.IsTenantIssuer(ctx, "tenant-y", tenantYIssuer)
	if err != nil {
		t.Fatalf("IsTenantIssuer(tenant-y, its own CA): %v", err)
	}
	if !ok {
		t.Error("expected tenant-y's own CA certificate to match IsTenantIssuer(ctx, \"tenant-y\", ...)")
	}

	// The security property: tenant-x's CA must NOT satisfy tenant-y's
	// binding, and vice versa -- a tenant-x-issued certificate cannot log
	// in as tenant-y.
	ok, err = p.IsTenantIssuer(ctx, "tenant-y", tenantXIssuer)
	if err != nil {
		t.Fatalf("IsTenantIssuer(tenant-y, tenant-x's CA): %v", err)
	}
	if ok {
		t.Fatal("tenant-x's CA certificate satisfied IsTenantIssuer for tenant-y: a tenant-x certificate could log in as tenant-y")
	}

	ok, err = p.IsTenantIssuer(ctx, "tenant-x", tenantYIssuer)
	if err != nil {
		t.Fatalf("IsTenantIssuer(tenant-x, tenant-y's CA): %v", err)
	}
	if ok {
		t.Fatal("tenant-y's CA certificate satisfied IsTenantIssuer for tenant-x: a tenant-y certificate could log in as tenant-x")
	}
}

func TestDefaultCertProvider_IsTenantIssuer_NilIssuer(t *testing.T) {
	p, _, _ := newTestProvider()
	ok, err := p.IsTenantIssuer(context.Background(), "acme", nil)
	if err != nil {
		t.Fatalf("IsTenantIssuer: %v", err)
	}
	if ok {
		t.Error("expected a nil issuer to never match")
	}
}

func TestDefaultCertProvider_IsTenantIssuer_EmptyTenantFoldsToSystem(t *testing.T) {
	p, _, _ := newTestProvider()
	ctx := context.Background()

	if _, err := generateForTest(ctx, p, &testRequest{Username: "alice", TenantID: "system"}); err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}
	_, systemCA, err := p.getCAMaterial(ctx, "system")
	if err != nil {
		t.Fatalf("getCAMaterial: %v", err)
	}

	ok, err := p.IsTenantIssuer(ctx, "", systemCA)
	if err != nil {
		t.Fatalf("IsTenantIssuer: %v", err)
	}
	if !ok {
		t.Error("expected an empty tenantID to fold to the system tenant")
	}
}

// TestDefaultCertProvider_IsTenantIssuer_LoadFailurePropagates proves
// IsTenantIssuer propagates a load failure that is NOT
// *CAKeyNotAvailableError -- a tenant that DOES have a certificate record,
// but one that no longer decodes -- rather than folding every
// loadOnlyCertificate failure into the load-only "no CA yet" (false, nil)
// answer. TestDefaultCertProvider_IsTenantIssuer_UnknownTenantNeverCreatesACA
// already covers the "never established" case; this covers "established,
// but corrupt." Only the certificate record is written: IsTenantIssuer's
// load-only, cert-only read (loadOnlyCertificate ->
// cakey.SoftwareProvider.CertificateOnly) never reads the key record at
// all, so there is no "material" record here to corrupt in the first
// place.
func TestDefaultCertProvider_IsTenantIssuer_LoadFailurePropagates(t *testing.T) {
	kv := newMockKVStore()
	p := NewDefaultProvider(kv, newMockBarrier())
	ctx := context.Background()

	if err := kv.Put(ctx, prefixCertCA+"ghost2/certificate", []byte("not-der-data")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	someCert := newHSMDouble(t).cert

	ok, err := p.IsTenantIssuer(ctx, "ghost2", someCert)
	if err == nil {
		t.Fatal("expected IsTenantIssuer to propagate a corrupt certificate record's load failure")
	}
	if ok {
		t.Error("expected false alongside the error")
	}
	var genErr *CertGenerateError
	if !errors.As(err, &genErr) {
		t.Fatalf("expected *CertGenerateError, got %T: %v", err, err)
	}
}

// TestDefaultCertProvider_IsTenantIssuer_UnknownTenantNeverCreatesACA is the
// B2-5 proof: a probe naming a tenant that has NEVER established a Key B CA
// must answer (false, nil) and must NOT mint one as a side effect. Before
// this fix, IsTenantIssuer called prov.Certificate directly, which
// auto-initializes the built-in cakey.SoftwareProvider's CA via material()
// on first use -- turning every such probe (reached on the authentication
// path for whatever tenant a presented leaf CLAIMS to be, including
// tenants that do not exist) into a CA-creation side effect.
func TestDefaultCertProvider_IsTenantIssuer_UnknownTenantNeverCreatesACA(t *testing.T) {
	p, kv, _ := newTestProvider()
	ctx := context.Background()

	const ghostTenant = "no-such-tenant"
	someCert := newHSMDouble(t).cert // an unrelated certificate, standing in for an arbitrary leaf's issuer

	ok, err := p.IsTenantIssuer(ctx, ghostTenant, someCert)
	if err != nil {
		t.Fatalf("IsTenantIssuer: %v", err)
	}
	if ok {
		t.Error("expected an unrelated certificate to never match a tenant with no established CA")
	}

	// No record under this tenant's CA prefix exists -- the probe must not
	// have created one. "wrapped_key" mirrors cakey.SoftwareProvider's own
	// internal record name (unexported; duplicated here exactly as
	// certPEMRecordSuffix already duplicates "cert_pem" in
	// default_provider.go, for the same reason).
	exists, err := kv.Exists(ctx, prefixCertCA+ghostTenant+"/wrapped_key")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if exists {
		t.Fatal("IsTenantIssuer created a CA key for a tenant that never had one")
	}

	// A second probe must answer identically -- this is a stable, repeatable
	// "no" rather than a transient one that flips once something gets
	// created on the first call.
	ok, err = p.IsTenantIssuer(ctx, ghostTenant, someCert)
	if err != nil {
		t.Fatalf("IsTenantIssuer (second call): %v", err)
	}
	if ok {
		t.Error("expected the second probe to also answer false")
	}
}

// TestDefaultCertProvider_ConcurrentFirstIssuanceSharesOneCA is the B2-5
// concurrency proof: N goroutines each issue the FIRST-EVER client
// certificate for the SAME brand-new tenant concurrently. Each reaches
// getCAMaterial -> resolveKeyProvider (which caches one
// *cakey.SoftwareProvider per tenant) -> Signer, which
// auto-initializes the tenant's CA key via cakey.SoftwareProvider's own
// material()/Initialize on first use. Without tenantInitLocked serializing
// that auto-init per tenant, two goroutines could both observe "no CA
// exists yet" and both generate and persist one, with whichever Put landed
// last silently winning over the in-memory key the other goroutine already
// cached and signed a leaf with -- producing a leaf whose issuer does not
// match the CA this tenant's own CACertPEMs now serves.
//
// Run with -race (as this package's test gate requires): a missing lock
// here is a data race on cakey.SoftwareProvider's internal fields, not
// merely a logic bug, since resolveKeyProvider hands every goroutine the
// SAME *SoftwareProvider instance for this tenant.
func TestDefaultCertProvider_ConcurrentFirstIssuanceSharesOneCA(t *testing.T) {
	p, _, _ := newTestProvider()
	ctx := context.Background()
	const tenantID = "racy-tenant"
	const n = 16

	type result struct {
		bundle *testBundle
		err    error
	}
	results := make([]result, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			bundle, err := generateForTest(ctx, p, &testRequest{
				Username: "user" + strconv.Itoa(i),
				TenantID: tenantID,
			})
			results[i] = result{bundle: bundle, err: err}
		}(i)
	}
	wg.Wait()

	pems, err := p.CACertPEMs(ctx)
	if err != nil {
		t.Fatalf("CACertPEMs: %v", err)
	}
	// Exactly one CA was ever persisted for this tenant: CACertPEMs scans
	// every tenant's record, and this test created only one tenant.
	if len(pems) != 1 {
		t.Fatalf("expected exactly one persisted CA across all goroutines, got %d", len(pems))
	}
	servedCA := parsePEMCert(t, pems[0])
	pool := x509.NewCertPool()
	pool.AddCert(servedCA)

	for i, r := range results {
		if r.err != nil {
			t.Fatalf("goroutine %d: GenerateClientCert: %v", i, r.err)
		}
		block, _ := pem.Decode(r.bundle.CertPEM)
		if block == nil {
			t.Fatalf("goroutine %d: CertPEM did not decode as PEM", i)
		}
		leaf, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatalf("goroutine %d: ParseCertificate: %v", i, err)
		}
		if _, err := leaf.Verify(x509.VerifyOptions{
			Roots:     pool,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
		}); err != nil {
			t.Errorf("goroutine %d: leaf does not verify against the tenant's own served CA (split-brain CA creation): %v", i, err)
		}
	}
}

func TestDefaultCertProvider_IsTenantIssuer_BarrierSealed(t *testing.T) {
	p, _, barrier := initializeProvider(t)
	barrier.seal()

	_, err := p.IsTenantIssuer(context.Background(), "system", &x509.Certificate{})
	if err == nil {
		t.Fatal("expected an error when the barrier is sealed")
	}
	var bsErr *BarrierSealedError
	if !errors.As(err, &bsErr) {
		t.Fatalf("expected *BarrierSealedError, got %T: %v", err, err)
	}
}
