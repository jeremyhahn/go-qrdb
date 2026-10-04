// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package clienttrust

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cert"
)

// fakeCertProvider implements cert.CertProvider (every method a no-op) plus
// CACertPEMs/IsTenantIssuer, so it satisfies CertSource. It never persists
// or generates real key material: it only hands back whatever a test
// configured.
type fakeCertProvider struct {
	pems       [][]byte
	err        error
	registry   map[string]*x509.Certificate // tenantID -> registered Key B CA
	issuerErr  error
	callCounts *int // non-nil: incremented on every CACertPEMs call
}

var _ cert.CertProvider = (*fakeCertProvider)(nil)
var _ CertSource = (*fakeCertProvider)(nil)

func (f *fakeCertProvider) GenerateClientCert(context.Context, *cert.GenerateRequest) (*cert.CertBundle, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeCertProvider) ExportPKCS12(context.Context, []byte, []byte, string) ([]byte, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeCertProvider) RevokeCert(context.Context, string) (*cert.RevokeResult, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeCertProvider) IsRevoked(context.Context, string) (bool, error) { return false, nil }
func (f *fakeCertProvider) TenantForSerial(context.Context, string) (string, bool, error) {
	return "", false, nil
}
func (f *fakeCertProvider) ListCerts(context.Context, string) ([]*cert.CertRecord, error) {
	return nil, nil
}
func (f *fakeCertProvider) CACertPEM() []byte { return nil }

func (f *fakeCertProvider) CACertPEMs(context.Context) ([][]byte, error) {
	if f.callCounts != nil {
		*f.callCounts++
	}
	return f.pems, f.err
}

func (f *fakeCertProvider) IsTenantIssuer(_ context.Context, tenantID string, issuer *x509.Certificate) (bool, error) {
	if f.issuerErr != nil {
		return false, f.issuerErr
	}
	registered, ok := f.registry[tenantID]
	if !ok || issuer == nil {
		return false, nil
	}
	return registered.Raw != nil && issuer.Raw != nil && string(registered.Raw) == string(issuer.Raw), nil
}

// fakePlainCertProvider implements cert.CertProvider but NOT CertSource --
// the shape of a CertProvider an embedder swapped in wholesale via
// server.WithCertProvider, which is not required to support multi-tenant
// enumeration.
type fakePlainCertProvider struct{}

var _ cert.CertProvider = (*fakePlainCertProvider)(nil)

func (f *fakePlainCertProvider) GenerateClientCert(context.Context, *cert.GenerateRequest) (*cert.CertBundle, error) {
	return nil, errors.New("not implemented")
}
func (f *fakePlainCertProvider) ExportPKCS12(context.Context, []byte, []byte, string) ([]byte, error) {
	return nil, errors.New("not implemented")
}
func (f *fakePlainCertProvider) RevokeCert(context.Context, string) (*cert.RevokeResult, error) {
	return nil, errors.New("not implemented")
}
func (f *fakePlainCertProvider) IsRevoked(context.Context, string) (bool, error) {
	return false, nil
}
func (f *fakePlainCertProvider) TenantForSerial(context.Context, string) (string, bool, error) {
	return "", false, nil
}
func (f *fakePlainCertProvider) ListCerts(context.Context, string) ([]*cert.CertRecord, error) {
	return nil, nil
}
func (f *fakePlainCertProvider) CACertPEM() []byte { return nil }

// fakeClusterCA implements ClusterCA.
type fakeClusterCA struct {
	cert *x509.Certificate
	err  error
}

func (f *fakeClusterCA) CACertificate(context.Context) (*x509.Certificate, error) {
	return f.cert, f.err
}

// pemEncode wraps encoding/pem.EncodeToMemory for a CERTIFICATE block.
func pemEncode(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// genCAPEM generates a throwaway self-signed CA certificate and returns its
// PEM encoding.
func genCAPEM(t testing.TB) ([]byte, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	certPEM := pemEncode(der)
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return certPEM, c
}

// genCAWithKey generates a throwaway self-signed CA certificate with the
// given CommonName (distinct names matter when a test compares subjects
// to prove one is NOT another's) and returns its PEM encoding alongside
// the parsed certificate and its signing key, for a caller that also
// needs to sign a leaf with it.
func genCAWithKey(t testing.TB, cn string) ([]byte, *x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return pemEncode(der), c, key
}

// genLeafTLSCert signs a client-auth leaf certificate under caCert/caKey
// and returns it as a tls.Certificate ready for tls.Config.Certificates.
func genLeafTLSCert(t testing.TB, caCert *x509.Certificate, caKey *ecdsa.PrivateKey) tls.Certificate {
	t.Helper()
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey (leaf): %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "test-client"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("CreateCertificate (leaf): %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der, caCert.Raw}, PrivateKey: leafKey}
}

func baseConfig(t testing.TB, keyA *x509.CertPool) *tls.Config {
	t.Helper()
	return &tls.Config{
		ClientCAs:  keyA,
		ClientAuth: tls.VerifyClientCertIfGiven,
		MinVersion: tls.VersionTLS13,
	}
}

func getConfigForClient(t testing.TB, cfg *tls.Config) *tls.Config {
	t.Helper()
	if cfg.GetConfigForClient == nil {
		t.Fatal("GetConfigForClient was not installed")
	}
	got, err := cfg.GetConfigForClient(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetConfigForClient: %v", err)
	}
	return got
}

// assertTrustedByConnection proves cert would be accepted as a presented
// client certificate on got's VerifyConnection hook -- the widened pool's
// real verification surface since configForClient (go-qrdb#591's review,
// B2): got.ClientCAs is deliberately the static baseline only, never the
// widened pool, so a test asserting trust via x509.Verify(Roots:
// got.ClientCAs) would no longer be testing what this Pool actually
// enforces. A self-signed CA certificate presented as its own leaf
// verifies trivially against a pool containing it, which is sufficient to
// prove membership in the widened set without needing to mint a separate
// leaf per fixture.
func assertTrustedByConnection(t testing.TB, got *tls.Config, cert *x509.Certificate) {
	t.Helper()
	if got.VerifyConnection == nil {
		t.Fatal("expected VerifyConnection to be installed on the widened config")
	}
	if err := got.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}); err != nil {
		t.Errorf("VerifyConnection rejected a certificate expected to be trusted: %v", err)
	}
}

// assertNotTrustedByConnection is assertTrustedByConnection's negative:
// cert must NOT verify against the widened pool.
func assertNotTrustedByConnection(t testing.TB, got *tls.Config, cert *x509.Certificate) {
	t.Helper()
	if got.VerifyConnection == nil {
		t.Fatal("expected VerifyConnection to be installed on the widened config")
	}
	if err := got.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}); err == nil {
		t.Error("VerifyConnection accepted a certificate expected to be untrusted")
	}
}

// ---------------------------------------------------------------------------
// B2-2: the pool and the issuer-tenant binding are the SAME object, and a
// cluster CA resolver's TWO failure shapes (never configured vs installed
// but currently failing) are deliberately NOT treated the same.
// ---------------------------------------------------------------------------

// TestPool_NoClusterCA_KeyBStillWidensOnPerTenantBindingAlone proves that a
// deployment with no cluster-CA concept at all (SetClusterCA never called)
// still trusts Key B once SetKeyBProvider is called: the per-tenant branch
// of VerifyIssuer (IsTenantIssuer) needs no cluster CA to operate, only the
// cluster-CA EXEMPTION does. The actual B2-2 invariant -- Key B is never
// trusted at the TLS layer without the binding also being enforceable at
// the authentication layer -- is a composer-wiring obligation (see
// SetKeyBProvider's doc comment): the composer must pass this SAME Pool as
// the auth.IssuerVerifier regardless of whether a cluster CA exists, which
// this package cannot verify from inside Pool itself.
func TestPool_NoClusterCA_KeyBStillWidensOnPerTenantBindingAlone(t *testing.T) {
	_, keyACert := genCAPEM(t)
	keyAPool := x509.NewCertPool()
	keyAPool.AddCert(keyACert)
	cfg := baseConfig(t, keyAPool)

	pool := New()
	pool.Wire(cfg)
	// Key B provider IS installed; ClusterCA is NEVER configured.
	keyBPEM, keyBCert := genCAPEM(t)
	pool.SetKeyBProvider(&fakeCertProvider{pems: [][]byte{keyBPEM}})

	got := getConfigForClient(t, cfg)
	if got == nil {
		t.Fatal("expected a widened config even with no ClusterCA configured")
	}
	assertTrustedByConnection(t, got, keyBCert)
}

func TestPool_ClusterCATransientError_KeyBNotTrustedThisCallOnly(t *testing.T) {
	_, keyACert := genCAPEM(t)
	keyAPool := x509.NewCertPool()
	keyAPool.AddCert(keyACert)
	cfg := baseConfig(t, keyAPool)

	pool := New()
	pool.Wire(cfg)
	keyBPEM, _ := genCAPEM(t)
	pool.SetKeyBProvider(&fakeCertProvider{pems: [][]byte{keyBPEM}})

	transientCA := &fakeClusterCA{err: errors.New("replicated store unreachable")}
	pool.SetClusterCA(transientCA)
	if got := getConfigForClient(t, cfg); got != cfg {
		t.Fatalf("expected the unmodified base config while ClusterCA fails, got %+v", got)
	}

	// The SAME Pool, no re-wiring: a later call where ClusterCA resolves
	// must succeed. A transient error must not disable the binding for the
	// life of the process.
	transientCA.err = nil
	transientCA.cert = keyACert
	got := getConfigForClient(t, cfg)
	if got == nil {
		t.Fatal("expected Key B to be trusted once ClusterCA resolves again")
	}
}

func TestPool_WidensPoolWithKeyBCerts(t *testing.T) {
	_, keyACert := genCAPEM(t)
	keyAPool := x509.NewCertPool()
	keyAPool.AddCert(keyACert)
	cfg := baseConfig(t, keyAPool)

	pool := New()
	pool.Wire(cfg)
	pool.SetClusterCA(&fakeClusterCA{cert: keyACert})

	keyBPEM, keyBCert := genCAPEM(t)
	pool.SetKeyBProvider(&fakeCertProvider{pems: [][]byte{keyBPEM}})

	got := getConfigForClient(t, cfg)
	if got == nil {
		t.Fatal("expected a widened config, got the static fallback (nil)")
	}
	assertTrustedByConnection(t, got, keyBCert)
	assertTrustedByConnection(t, got, keyACert)
}

func TestPool_PreservesOtherConfigFields(t *testing.T) {
	_, keyACert := genCAPEM(t)
	keyAPool := x509.NewCertPool()
	keyAPool.AddCert(keyACert)
	cfg := baseConfig(t, keyAPool)

	pool := New()
	pool.Wire(cfg)
	pool.SetClusterCA(&fakeClusterCA{cert: keyACert})
	keyBPEM, _ := genCAPEM(t)
	pool.SetKeyBProvider(&fakeCertProvider{pems: [][]byte{keyBPEM}})

	got := getConfigForClient(t, cfg)
	if got == nil {
		t.Fatal("expected a widened config")
	}
	if got.MinVersion != cfg.MinVersion {
		t.Errorf("MinVersion = %v, want %v", got.MinVersion, cfg.MinVersion)
	}
	// ClientAuth is remapped, not preserved: cfg's VerifyClientCertIfGiven
	// would have crypto/tls verify the presented certificate against
	// ClientCAs itself, which configForClient deliberately narrows to the
	// static baseline (go-qrdb#591's review, B2) -- so a Key-B-issued
	// certificate would fail crypto/tls's own verification before
	// VerifyConnection ever saw it. The non-verifying counterpart keeps
	// "is a certificate required at all" unchanged while moving
	// verification to VerifyConnection, which configForClient always
	// installs.
	if got.ClientAuth != tls.RequestClientCert {
		t.Errorf("ClientAuth = %v, want %v (VerifyClientCertIfGiven's remapped counterpart)", got.ClientAuth, tls.RequestClientCert)
	}
	if got.GetConfigForClient != nil {
		t.Error("expected GetConfigForClient to be cleared on the returned clone")
	}
}

func TestPool_ProviderErrorFallsBackToStatic(t *testing.T) {
	_, keyACert := genCAPEM(t)
	cfg := baseConfig(t, x509.NewCertPool())
	pool := New()
	pool.Wire(cfg)
	pool.SetClusterCA(&fakeClusterCA{cert: keyACert})
	pool.SetKeyBProvider(&fakeCertProvider{err: errors.New("barrier is sealed")})

	if got := getConfigForClient(t, cfg); got != cfg {
		t.Fatalf("expected the unmodified base config when CACertPEMs fails (simulating sealed), got %+v", got)
	}
}

func TestPool_NoKeyBCertsYetStillTrustsClusterCA(t *testing.T) {
	_, keyACert := genCAPEM(t)
	cfg := baseConfig(t, x509.NewCertPool())
	pool := New()
	pool.Wire(cfg)
	pool.SetClusterCA(&fakeClusterCA{cert: keyACert})
	pool.SetKeyBProvider(&fakeCertProvider{})

	// No tenant has established Key B yet (CACertPEMs succeeds with zero
	// entries): the widened pool trusts Key A, same as the static fallback
	// would, since there is nothing else to add.
	got := getConfigForClient(t, cfg)
	if got == nil {
		t.Fatal("expected a config (possibly equivalent to the static one)")
	}
	assertTrustedByConnection(t, got, keyACert)
}

func TestPool_ProviderWithoutCertSourceFallsBackToStatic(t *testing.T) {
	_, keyACert := genCAPEM(t)
	cfg := baseConfig(t, x509.NewCertPool())
	pool := New()
	pool.Wire(cfg)
	pool.SetClusterCA(&fakeClusterCA{cert: keyACert})
	pool.SetKeyBProvider(&fakePlainCertProvider{})

	if got := getConfigForClient(t, cfg); got != cfg {
		t.Fatalf("expected the unmodified base config for a CertProvider that does not implement CertSource, got %+v", got)
	}
}

func TestPool_NilKeyBProviderRevertsToStatic(t *testing.T) {
	_, keyACert := genCAPEM(t)
	cfg := baseConfig(t, x509.NewCertPool())
	pool := New()
	pool.Wire(cfg)
	pool.SetClusterCA(&fakeClusterCA{cert: keyACert})
	keyBPEM, _ := genCAPEM(t)
	pool.SetKeyBProvider(&fakeCertProvider{pems: [][]byte{keyBPEM}})
	if got := getConfigForClient(t, cfg); got == nil {
		t.Fatal("expected a widened config before reverting")
	}

	pool.SetKeyBProvider(nil)
	if got := getConfigForClient(t, cfg); got != cfg {
		t.Fatalf("expected the unmodified base config after SetKeyBProvider(nil), got %+v", got)
	}
}

func TestPool_NilStaticClientCAs(t *testing.T) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS13} // ClientCAs intentionally nil
	pool := New()
	pool.Wire(cfg)
	_, keyACert := genCAPEM(t)
	pool.SetClusterCA(&fakeClusterCA{cert: keyACert})
	keyBPEM, keyBCert := genCAPEM(t)
	pool.SetKeyBProvider(&fakeCertProvider{pems: [][]byte{keyBPEM}})

	got := getConfigForClient(t, cfg)
	if got == nil {
		t.Fatal("expected a widened config even when the base ClientCAs was nil")
	}
	assertTrustedByConnection(t, got, keyBCert)
}

// ---------------------------------------------------------------------------
// B2-4: caching bounds the pre-auth cost of scanning every tenant's CA.
// ---------------------------------------------------------------------------

func TestPool_CachesWidenedPoolAcrossCalls(t *testing.T) {
	_, keyACert := genCAPEM(t)
	cfg := baseConfig(t, x509.NewCertPool())
	pool := New()
	pool.Wire(cfg)
	pool.SetClusterCA(&fakeClusterCA{cert: keyACert})

	keyBPEM, _ := genCAPEM(t)
	calls := 0
	pool.SetKeyBProvider(&fakeCertProvider{pems: [][]byte{keyBPEM}, callCounts: &calls})

	for i := 0; i < 5; i++ {
		if got := getConfigForClient(t, cfg); got == nil {
			t.Fatalf("call %d: expected a widened config", i)
		}
	}
	if calls != 1 {
		t.Errorf("CACertPEMs called %d times across 5 connections, want 1 (cached)", calls)
	}
}

func TestPool_SetKeyBProviderInvalidatesCache(t *testing.T) {
	_, keyACert := genCAPEM(t)
	cfg := baseConfig(t, x509.NewCertPool())
	pool := New()
	pool.Wire(cfg)
	pool.SetClusterCA(&fakeClusterCA{cert: keyACert})

	keyBPEM1, _ := genCAPEM(t)
	calls1 := 0
	pool.SetKeyBProvider(&fakeCertProvider{pems: [][]byte{keyBPEM1}, callCounts: &calls1})
	getConfigForClient(t, cfg)

	keyBPEM2, keyBCert2 := genCAPEM(t)
	calls2 := 0
	pool.SetKeyBProvider(&fakeCertProvider{pems: [][]byte{keyBPEM2}, callCounts: &calls2})
	got := getConfigForClient(t, cfg)
	if calls2 != 1 {
		t.Errorf("expected the new provider to be consulted immediately after SetKeyBProvider, calls=%d", calls2)
	}
	assertTrustedByConnection(t, got, keyBCert2)
}

// ---------------------------------------------------------------------------
// IsClusterCA / VerifyIssuer -- the issuer-tenant binding surface shared
// with pkg/auth (MTLSAuthenticator) and pkg/auth/middleware (on-behalf).
// ---------------------------------------------------------------------------

func TestPool_IsClusterCA(t *testing.T) {
	_, keyACert := genCAPEM(t)
	_, otherCert := genCAPEM(t)
	pool := New()

	if pool.IsClusterCA(t.Context(), keyACert) {
		t.Error("expected false with no ClusterCA configured")
	}
	pool.SetClusterCA(&fakeClusterCA{cert: keyACert})
	if !pool.IsClusterCA(t.Context(), keyACert) {
		t.Error("expected true for the configured cluster CA certificate")
	}
	if pool.IsClusterCA(t.Context(), otherCert) {
		t.Error("expected false for a different certificate")
	}
	if pool.IsClusterCA(t.Context(), nil) {
		t.Error("expected false for a nil certificate")
	}
}

func TestPool_VerifyIssuer_ClusterCAExempt(t *testing.T) {
	_, keyACert := genCAPEM(t)
	pool := New()
	pool.SetClusterCA(&fakeClusterCA{cert: keyACert})
	// No Key B provider at all -- the cluster CA exemption must still work.
	if err := pool.VerifyIssuer(context.Background(), "any-tenant", keyACert); err != nil {
		t.Errorf("expected the cluster CA to be exempt regardless of tenant, got: %v", err)
	}
}

func TestPool_VerifyIssuer_OwnTenantCAAccepted(t *testing.T) {
	_, keyACert := genCAPEM(t)
	_, keyBCertA := genCAPEM(t)
	pool := New()
	pool.SetClusterCA(&fakeClusterCA{cert: keyACert})
	pool.SetKeyBProvider(&fakeCertProvider{registry: map[string]*x509.Certificate{"A": keyBCertA}})

	if err := pool.VerifyIssuer(context.Background(), "A", keyBCertA); err != nil {
		t.Errorf("expected tenant A's own Key B CA to be accepted, got: %v", err)
	}
}

func TestPool_VerifyIssuer_ForeignTenantCARefused(t *testing.T) {
	_, keyACert := genCAPEM(t)
	_, keyBCertA := genCAPEM(t)
	_, keyBCertB := genCAPEM(t)
	pool := New()
	pool.SetClusterCA(&fakeClusterCA{cert: keyACert})
	pool.SetKeyBProvider(&fakeCertProvider{registry: map[string]*x509.Certificate{
		"A": keyBCertA,
		"B": keyBCertB,
	}})

	err := pool.VerifyIssuer(context.Background(), "B", keyBCertA)
	if err == nil {
		t.Fatal("expected tenant A's Key B CA to be refused for a leaf claiming tenant B")
	}
	var mismatch *IssuerNotTrustedError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected *IssuerNotTrustedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// B3: a Key B or cluster CA failure fails closed and IN ISOLATION -- never
// taking the static exemption (Key A, the operator's own additional client
// CAs, a test harness's ad-hoc CA) down with it.
// ---------------------------------------------------------------------------

// TestPool_ClusterCABroken_StaticExemptionStillWorks is go-qrdb#591's B3
// regression proof: before the fix, resolveClientCAs returned on a BROKEN
// (not merely unconfigured) cluster CA resolver before ever reaching
// CACertPEMs, so the Key-B-only cache isRegisteredKeyBCA reads never
// refreshed and stayed "uncertain" (true) forever -- which
// IsStaticallyTrusted treats as "assume this issuer might be a Key B CA,
// refuse the exemption", for every issuer, including the harness's own
// static CA that has nothing to do with Key B at all. A real connection
// triggers this through configForClient/GetConfigForClient; this test
// calls resolveClientCAs directly, the seam both that path and
// VerifyPeerChain share, to observe its effect on IsStaticallyTrusted
// without needing a live TLS handshake.
func TestPool_ClusterCABroken_StaticExemptionStillWorks(t *testing.T) {
	harnessCAPEM, harnessCACert := genCAPEM(t)
	harnessPool := x509.NewCertPool()
	harnessPool.AppendCertsFromPEM(harnessCAPEM)
	cfg := baseConfig(t, harnessPool)

	pool := New()
	pool.Wire(cfg)

	keyBPEM, _ := genCAPEM(t)
	pool.SetKeyBProvider(&fakeCertProvider{pems: [][]byte{keyBPEM}})
	// The cluster CA resolver IS installed (configured), but every call
	// fails -- a corrupt stored record, in production terms -- not merely
	// absent (*ClusterCANotConfiguredError), which is the OTHER, already
	// non-blocking case.
	pool.SetClusterCA(&fakeClusterCA{err: errors.New("stored cluster CA record is corrupt")})

	// Simulates the live connection that would normally trigger this via
	// GetConfigForClient: the widened pool itself correctly fails closed
	// (the cluster CA entry and exemption are unavailable this call), but
	// the Key-B-only cache underneath must still have refreshed.
	if _, err := pool.resolveClientCAs(context.Background(), cfg.ClientCAs); err == nil {
		t.Fatal("expected resolveClientCAs to fail closed while the cluster CA resolver is broken")
	}

	if !pool.IsStaticallyTrusted(harnessCACert) {
		t.Fatal("harness login (static exemption) was refused by a cluster CA failure that has nothing to do with it")
	}
}

// TestPool_KeyBNeverSucceeded_StaticExemptionStillUncertainButSafe proves
// the OTHER direction is unaffected by this fix: a Key B provider that has
// NEVER once succeeded (CACertPEMs always fails, e.g. sealed) genuinely
// cannot rule out whether a given issuer might be some tenant's Key B CA,
// so isRegisteredKeyBCA's conservative "uncertain, so true" answer for a
// cache that was never built even once is unchanged -- this is a
// correctness floor, not the B3 bug (which was specifically a cluster CA
// failure blocking a Key-B-only cache refresh that would otherwise have
// succeeded).
func TestPool_KeyBNeverSucceeded_StaticExemptionStillUncertainButSafe(t *testing.T) {
	harnessCAPEM, harnessCACert := genCAPEM(t)
	harnessPool := x509.NewCertPool()
	harnessPool.AppendCertsFromPEM(harnessCAPEM)
	cfg := baseConfig(t, harnessPool)

	pool := New()
	pool.Wire(cfg)
	pool.SetKeyBProvider(&fakeCertProvider{err: errors.New("barrier is sealed")})

	if _, err := pool.resolveClientCAs(context.Background(), cfg.ClientCAs); err == nil {
		t.Fatal("expected resolveClientCAs to fail while Key B has never once succeeded")
	}

	// Unchanged, conservative behavior: a Key B provider IS installed, but
	// nothing proves harnessCACert is NOT one of its tenants' CAs yet, so
	// the exemption is refused -- this is NOT the B3 regression, which
	// was specifically about a cluster CA failure preventing a refresh
	// that Key B's OWN health would otherwise have allowed to succeed.
	if pool.IsStaticallyTrusted(harnessCACert) {
		t.Fatal("expected the exemption to stay refused while Key B has never once resolved (conservative-uncertain, unrelated to B3)")
	}
}

func TestPool_VerifyIssuer_NoProviderRefused(t *testing.T) {
	_, keyACert := genCAPEM(t)
	_, keyBCert := genCAPEM(t)
	pool := New()
	pool.SetClusterCA(&fakeClusterCA{cert: keyACert})

	if err := pool.VerifyIssuer(context.Background(), "A", keyBCert); err == nil {
		t.Fatal("expected refusal with no Key B provider installed")
	}
}

// ---------------------------------------------------------------------------
// IsStaticallyTrusted: a CA already in the listener's OWN static ClientCAs
// (an operator-supplied additional client CA, or a test harness's own CA)
// is pre-existing trust the composer granted before Key B ever widened the
// pool, and the binding must not retroactively impose tenant-matching on
// it. This exemption is scoped tightly: it must never let a Key B CA
// escape the per-tenant check just because some OTHER, unrelated CA is
// also in the static base.
// ---------------------------------------------------------------------------

func TestPool_IsStaticallyTrusted_NoHandshakeYetRefusesClosed(t *testing.T) {
	_, staticCert := genCAPEM(t)
	pool := New()
	if pool.IsStaticallyTrusted(staticCert) {
		t.Error("expected false before any handshake has run (Wire never called)")
	}
	if pool.IsStaticallyTrusted(nil) {
		t.Error("expected false for a nil certificate")
	}
}

// TestPool_VerifyIssuer_StaticClientCAExempt is the red/green proof: a CA
// that was already in cfg.ClientCAs before Wire ever ran -- neither the
// cluster CA nor any tenant's Key B CA -- is exempt from the per-tenant
// binding once a handshake has observed it, exactly like the cluster-CA
// exemption.
func TestPool_VerifyIssuer_StaticClientCAExempt(t *testing.T) {
	_, staticCert := genCAPEM(t)
	staticPool := x509.NewCertPool()
	staticPool.AddCert(staticCert)
	cfg := baseConfig(t, staticPool)

	pool := New()
	pool.Wire(cfg) // captures staticPool as the fixed static-base snapshot
	// No ClusterCA, no Key B provider at all: staticCert is trusted ONLY
	// because it was already in cfg.ClientCAs.

	if err := pool.VerifyIssuer(context.Background(), "any-tenant", staticCert); err != nil {
		t.Errorf("expected a CA already in the static ClientCAs to be exempt regardless of tenant, got: %v", err)
	}
}

// TestPool_VerifyIssuer_StaticExemptionDoesNotLeakToKeyB proves the
// exemption's scope: with an UNRELATED CA in the static base, a Key B CA
// that is genuinely a cross-tenant mismatch is still refused -- the static
// exemption must never be used as a side door for a Key B issuer that
// fails its own per-tenant check.
func TestPool_VerifyIssuer_StaticExemptionDoesNotLeakToKeyB(t *testing.T) {
	_, staticCert := genCAPEM(t)
	staticPool := x509.NewCertPool()
	staticPool.AddCert(staticCert)
	cfg := baseConfig(t, staticPool)

	_, keyBCertA := genCAPEM(t)
	_, keyBCertB := genCAPEM(t)

	pool := New()
	pool.Wire(cfg)
	pool.SetKeyBProvider(&fakeCertProvider{registry: map[string]*x509.Certificate{
		"A": keyBCertA,
		"B": keyBCertB,
	}})
	getConfigForClient(t, cfg) // populates staticBase with staticPool

	err := pool.VerifyIssuer(context.Background(), "B", keyBCertA)
	if err == nil {
		t.Fatal("expected tenant A's Key B CA to still be refused for a leaf claiming tenant B, static exemption notwithstanding")
	}
	var mismatch *IssuerNotTrustedError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected *IssuerNotTrustedError, got %T: %v", err, err)
	}
}

// TestPool_IsStaticallyTrusted_RefusesIssuerAlsoRegisteredAsKeyBCA is the
// red/green proof for the misconfiguration the static exemption must not
// paper over: tenant A's Key B CA certificate ends up ALSO present in the
// listener's static ClientCAs (an operator copy/paste error, or a test
// harness that reuses the same CA for both roles). Without the
// isRegisteredKeyBCA check, IsStaticallyTrusted would grant that CA a
// blanket, any-tenant pass merely because it verifies against the static
// pool; a leaf claiming to be tenant B, signed by tenant A's Key B CA,
// would then authenticate as tenant B.
func TestPool_IsStaticallyTrusted_RefusesIssuerAlsoRegisteredAsKeyBCA(t *testing.T) {
	keyBPEMA, keyBCertA := genCAPEM(t)

	// The misconfiguration: tenant A's own Key B CA is ALSO in the static
	// ClientCAs pool.
	staticPool := x509.NewCertPool()
	staticPool.AddCert(keyBCertA)
	cfg := baseConfig(t, staticPool)

	pool := New()
	pool.Wire(cfg)
	pool.SetKeyBProvider(&fakeCertProvider{
		pems:     [][]byte{keyBPEMA},
		registry: map[string]*x509.Certificate{"A": keyBCertA},
	})
	// Run the handshake path once so the Key-B-only cache is built from the
	// SAME CACertPEMs the registry mirrors.
	if got := getConfigForClient(t, cfg); got == nil {
		t.Fatal("expected a widened config")
	}

	if pool.IsStaticallyTrusted(keyBCertA) {
		t.Fatal("expected the static exemption to be refused for an issuer that is also a registered Key B CA")
	}

	// The per-tenant binding still correctly scopes it to tenant A alone:
	// accepted for A, refused for a leaf claiming any other tenant.
	if err := pool.VerifyIssuer(context.Background(), "A", keyBCertA); err != nil {
		t.Errorf("expected tenant A's own Key B CA to still be accepted for tenant A, got: %v", err)
	}
	err := pool.VerifyIssuer(context.Background(), "B", keyBCertA)
	if err == nil {
		t.Fatal("expected tenant A's Key B CA, even though it is ALSO in the static ClientCAs, to be refused for a leaf claiming tenant B")
	}
	var mismatch *IssuerNotTrustedError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected *IssuerNotTrustedError, got %T: %v", err, err)
	}
}

// TestPool_IsStaticallyTrusted_UncertainBeforeCacheRefusesClosed proves
// isRegisteredKeyBCA's "uncertain" branch: a Key B provider IS installed
// (so there is a Key B concept in play for this Pool), but no handshake has
// run yet, so poolCache.keyBOnly does not exist. IsStaticallyTrusted must
// refuse the exemption rather than guess, because granting a blanket pass
// on an unproven negative is the wrong direction to be wrong in.
func TestPool_IsStaticallyTrusted_UncertainBeforeCacheRefusesClosed(t *testing.T) {
	_, staticCert := genCAPEM(t)
	staticPool := x509.NewCertPool()
	staticPool.AddCert(staticCert)
	cfg := baseConfig(t, staticPool)

	pool := New()
	pool.Wire(cfg)
	pool.SetKeyBProvider(&fakeCertProvider{})
	// No handshake run: cfg.GetConfigForClient is never invoked, so
	// p.cache.Load() is still nil here.

	if pool.IsStaticallyTrusted(staticCert) {
		t.Fatal("expected the static exemption to be refused while the Key-B-only cache is still unbuilt")
	}
}

// TestPool_VerifyIssuer_ProviderWithoutCertSourceRefused proves VerifyIssuer
// itself -- not just the widening path TestPool_ProviderWithoutCertSourceFallsBackToStatic
// covers -- refuses an issuer when a CertProvider is installed but does not
// implement CertSource (the shape of a CertProvider an embedder swapped in
// wholesale via server.WithCertProvider, not required to support multi-
// tenant enumeration).
func TestPool_VerifyIssuer_ProviderWithoutCertSourceRefused(t *testing.T) {
	_, keyACert := genCAPEM(t)
	_, keyBCert := genCAPEM(t)
	pool := New()
	pool.SetClusterCA(&fakeClusterCA{cert: keyACert})
	pool.SetKeyBProvider(&fakePlainCertProvider{})

	err := pool.VerifyIssuer(context.Background(), "A", keyBCert)
	if err == nil {
		t.Fatal("expected refusal when the installed CertProvider does not implement CertSource")
	}
	var refused *IssuerNotTrustedError
	if !errors.As(err, &refused) {
		t.Fatalf("expected *IssuerNotTrustedError, got %T: %v", err, err)
	}
}

// TestPool_VerifyIssuer_IsTenantIssuerErrorWrapped proves VerifyIssuer wraps
// a CertSource.IsTenantIssuer failure (e.g. the barrier sealing mid-check)
// in *IssuerNotTrustedError with Err set, rather than losing the cause or
// returning nil as if the issuer were accepted.
func TestPool_VerifyIssuer_IsTenantIssuerErrorWrapped(t *testing.T) {
	_, keyACert := genCAPEM(t)
	_, keyBCert := genCAPEM(t)
	issuerErr := errors.New("barrier sealed mid-check")
	pool := New()
	pool.SetClusterCA(&fakeClusterCA{cert: keyACert})
	pool.SetKeyBProvider(&fakeCertProvider{issuerErr: issuerErr})

	err := pool.VerifyIssuer(context.Background(), "A", keyBCert)
	if err == nil {
		t.Fatal("expected refusal when CertSource.IsTenantIssuer itself fails")
	}
	var refused *IssuerNotTrustedError
	if !errors.As(err, &refused) {
		t.Fatalf("expected *IssuerNotTrustedError, got %T: %v", err, err)
	}
	if !errors.Is(err, issuerErr) {
		t.Fatalf("expected the underlying IsTenantIssuer error to be reachable via errors.Is/Unwrap, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// B2-4 benchmark: proves the per-handshake scan cost the cache amortizes.
// ---------------------------------------------------------------------------

// manyTenantProvider simulates CACertPEMs over N tenants: each call re-scans
// and re-parses N CA certificates, exactly like DefaultCertProvider's
// built-in path scanning __sys:cert_ca/.
type manyTenantProvider struct {
	pems [][]byte
}

var _ cert.CertProvider = (*manyTenantProvider)(nil)
var _ CertSource = (*manyTenantProvider)(nil)

func (m *manyTenantProvider) GenerateClientCert(context.Context, *cert.GenerateRequest) (*cert.CertBundle, error) {
	return nil, errors.New("not implemented")
}
func (m *manyTenantProvider) ExportPKCS12(context.Context, []byte, []byte, string) ([]byte, error) {
	return nil, errors.New("not implemented")
}
func (m *manyTenantProvider) RevokeCert(context.Context, string) (*cert.RevokeResult, error) {
	return nil, errors.New("not implemented")
}
func (m *manyTenantProvider) IsRevoked(context.Context, string) (bool, error) { return false, nil }
func (m *manyTenantProvider) TenantForSerial(context.Context, string) (string, bool, error) {
	return "", false, nil
}
func (m *manyTenantProvider) ListCerts(context.Context, string) ([]*cert.CertRecord, error) {
	return nil, nil
}
func (m *manyTenantProvider) CACertPEM() []byte { return nil }
func (m *manyTenantProvider) CACertPEMs(context.Context) ([][]byte, error) {
	return m.pems, nil
}
func (m *manyTenantProvider) IsTenantIssuer(context.Context, string, *x509.Certificate) (bool, error) {
	return false, nil
}

func newManyTenantProvider(b testing.TB, n int) *manyTenantProvider {
	b.Helper()
	pems := make([][]byte, n)
	for i := range pems {
		pem, _ := genCAPEM(b)
		pems[i] = pem
	}
	return &manyTenantProvider{pems: pems}
}

// BenchmarkResolveClientCAs_NoCache_1000Tenants is B2-4's proof: every call
// re-scans and re-parses every tenant's CA certificate with no cache at all
// (bypassing Pool's cache by rebuilding the pool from the PEMs directly, the
// shape DefaultCertProvider.CACertPEMs' caller used to pay on every
// handshake before this fix).
func BenchmarkResolveClientCAs_NoCache_1000Tenants(b *testing.B) {
	prov := newManyTenantProvider(b, 1000)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pems, _ := prov.CACertPEMs(ctx)
		pool := x509.NewCertPool()
		for _, p := range pems {
			pool.AppendCertsFromPEM(p)
		}
	}
}

// BenchmarkPool_ResolveClientCAs_1000Tenants_Cached is the fixed path: the
// same 1,000-tenant cost is paid once per poolCacheTTL window, not once per
// handshake.
func BenchmarkPool_ResolveClientCAs_1000Tenants_Cached(b *testing.B) {
	_, keyACert := genCAPEM(b)
	prov := newManyTenantProvider(b, 1000)
	pool := New()
	pool.SetClusterCA(&fakeClusterCA{cert: keyACert})
	pool.SetKeyBProvider(prov)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := pool.resolveClientCAs(ctx, nil); err != nil {
			b.Fatalf("resolveClientCAs: %v", err)
		}
	}
}

// ---------------------------------------------------------------------------
// B2: a real TLS handshake, at scale, never advertises a tenant's CA name.
// ---------------------------------------------------------------------------

// TestPool_RealHandshake_3000Tenants_AdvertisesNoTenantCA is go-qrdb#591's
// B2 regression proof over an ACTUAL crypto/tls handshake, not a unit check
// of Pool's internal state: with 3,000 tenant Key B CAs behind the pool --
// twice the point past which a naive ClientCAs assignment (every tenant's
// Subject in the TLS CertificateRequest's certificate_authorities
// extension) exceeds the protocol's 64 KiB handshake message limit and
// fails EVERY mTLS login, Key A and the operator's own static CA included
// -- the handshake still completes, and the server's CertificateRequest
// advertises NONE of the 3,000 tenant CAs: AcceptableCAs (captured via the
// client's own GetClientCertificate hook, exactly what a real peer sees)
// stays empty, because configForClient leaves ClientCAs at the static
// baseline and moves verification to VerifyConnection (see
// configForClient's own doc comment). A client certificate issued by ONE
// of the 3,000 tenant CAs -- picked arbitrarily, not the first or last --
// still authenticates, proving the widened pool verifies it despite never
// naming it.
func TestPool_RealHandshake_3000Tenants_AdvertisesNoTenantCA(t *testing.T) {
	const tenantCount = 3000
	const trustedTenantIndex = 1500

	_, operatorCACert, operatorCAKey := genCAWithKey(t, "operator-static-ca")
	operatorPool := x509.NewCertPool()
	operatorPool.AddCert(operatorCACert)

	// serverLeaf: the listener's OWN identity, signed by the operator CA --
	// irrelevant to what this test measures (the CLIENT's view of the
	// handshake), but tls.Config.Certificates must be non-empty for the
	// server side to complete a handshake at all.
	serverLeaf := genLeafTLSCert(t, operatorCACert, operatorCAKey)

	tenantCAPEMs := make([][]byte, tenantCount)
	var trustedTenantCACert *x509.Certificate
	var trustedTenantCAKey *ecdsa.PrivateKey
	for i := 0; i < tenantCount; i++ {
		pem, cert, key := genCAWithKey(t, fmt.Sprintf("tenant-%d-key-b-ca", i))
		tenantCAPEMs[i] = pem
		if i == trustedTenantIndex {
			trustedTenantCACert, trustedTenantCAKey = cert, key
		}
	}
	if trustedTenantCACert == nil {
		t.Fatal("trustedTenantIndex out of range")
	}
	clientLeaf := genLeafTLSCert(t, trustedTenantCACert, trustedTenantCAKey)

	serverCfg := &tls.Config{
		Certificates: []tls.Certificate{serverLeaf},
		ClientCAs:    operatorPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}
	pool := New()
	pool.Wire(serverCfg)
	pool.SetKeyBProvider(&fakeCertProvider{pems: tenantCAPEMs})

	var acceptableCAs [][]byte
	clientCfg := &tls.Config{
		InsecureSkipVerify: true, // this test measures the SERVER's advertised CAs, not the client's verification of the server
		MinVersion:         tls.VersionTLS13,
		GetClientCertificate: func(info *tls.CertificateRequestInfo) (*tls.Certificate, error) {
			acceptableCAs = info.AcceptableCAs
			return &clientLeaf, nil
		},
	}

	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	// Bounded, not context.Background(): an unbounded ClientCAs assignment
	// (the regression this test guards) does not merely error on a
	// net.Pipe -- a CertificateRequest this large blocks on the
	// unbuffered pipe's synchronous write with nothing reading the other
	// half yet, hanging the handshake rather than failing it fast. The
	// deadline turns that hang into a reported failure instead of a test
	// that never returns.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- tls.Server(serverConn, serverCfg).HandshakeContext(ctx)
	}()

	clientTLS := tls.Client(clientConn, clientCfg)
	if err := clientTLS.HandshakeContext(ctx); err != nil {
		t.Fatalf("client handshake failed with %d tenant CAs behind the pool: %v", tenantCount, err)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("server handshake failed with %d tenant CAs behind the pool: %v", tenantCount, err)
	}

	// Exactly the operator's static baseline (one CA) is advertised, never
	// the dynamic pool: a plain ClientCAs assignment would have exploded
	// this to 1 + 3,000 and failed the handshake outright past the 64 KiB
	// message limit, which the earlier success check already disproves;
	// here the concern is which CAs the message actually NAMES.
	if len(acceptableCAs) != 1 {
		t.Fatalf("CertificateRequest advertised %d acceptable CA(s), want exactly 1 (the operator's static baseline)", len(acceptableCAs))
	}
	if !bytes.Equal(acceptableCAs[0], operatorCACert.RawSubject) {
		t.Error("the one advertised CA is not the operator's static baseline")
	}
	for i, tenantPEM := range tenantCAPEMs {
		block, _ := pem.Decode(tenantPEM)
		if block == nil {
			t.Fatalf("tenant %d: genCAWithKey did not produce valid PEM", i)
		}
		tenantCert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatalf("tenant %d: ParseCertificate: %v", i, err)
		}
		if bytes.Equal(acceptableCAs[0], tenantCert.RawSubject) {
			t.Fatalf("tenant %d's CA subject was advertised in the CertificateRequest", i)
		}
	}
}

// blockingClusterCA is a ClusterCA whose CACertificate blocks until ctx is
// done, then reports ctx's own error. It exists to prove that
// configForClient's resolution is bounded by a context DERIVED FROM the
// handshake -- never context.Background(), which nothing can ever cancel --
// so a resolver that never otherwise returns still lets that one
// connection's handshake conclude rather than hang forever (go-qrdb#591's
// review, B5).
type blockingClusterCA struct{}

func (blockingClusterCA) CACertificate(ctx context.Context) (*x509.Certificate, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestPool_ConfigForClient_HandshakeBoundedNotBackground proves
// configForClient derives a BOUNDED context from the handshake's own
// hello.Context(), never context.Background(), for its trust resolution.
//
// handshakeTrustTimeout is shortened for this test (see its own doc
// comment) so the assertion below is cheap at the production value too: a
// resolver that blocks forever still causes THIS test's own handshake to
// conclude well within the test's own outer deadline, which is set to many
// multiples of the shortened timeout -- proving a bound shorter than
// "forever" exists, independent of its exact production duration.
//
// The outer HandshakeContext call is given a context with NO deadline of
// its own (context.WithCancel, never fired early): any bound observed
// cannot be credited to the caller's own context, only to configForClient's
// own internal derivation from hello.Context().
func TestPool_ConfigForClient_HandshakeBoundedNotBackground(t *testing.T) {
	savedTimeout := handshakeTrustTimeout
	handshakeTrustTimeout = 50 * time.Millisecond
	t.Cleanup(func() { handshakeTrustTimeout = savedTimeout })

	_, operatorCACert, operatorCAKey := genCAWithKey(t, "operator-static-ca")
	operatorPool := x509.NewCertPool()
	operatorPool.AddCert(operatorCACert)
	serverLeaf := genLeafTLSCert(t, operatorCACert, operatorCAKey)
	clientLeaf := genLeafTLSCert(t, operatorCACert, operatorCAKey)

	serverCfg := &tls.Config{
		Certificates: []tls.Certificate{serverLeaf},
		ClientCAs:    operatorPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}
	pool := New()
	pool.Wire(serverCfg)
	pool.SetClusterCA(blockingClusterCA{})
	pool.SetKeyBProvider(&fakeCertProvider{})

	clientCfg := &tls.Config{
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS13,
		Certificates:       []tls.Certificate{clientLeaf},
	}

	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	// No deadline of its own -- see doc comment above.
	outerCtx, outerCancel := context.WithCancel(context.Background())
	defer outerCancel()

	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		// An error here (the expected outcome: configForClient's own
		// resolveClientCAs failed, so GetConfigForClient returns (nil,
		// nil) and crypto/tls falls back to serverCfg's static ClientCAs,
		// OR the resolution's failure surfaces some other way) is not
		// itself the point -- only that this returns at all, promptly.
		_ = tls.Server(serverConn, serverCfg).HandshakeContext(outerCtx)
	}()
	go func() {
		_ = tls.Client(clientConn, clientCfg).HandshakeContext(outerCtx)
	}()

	select {
	case <-serverDone:
		// configForClient's own bounded context expired and the handshake
		// concluded -- the behavior under test.
	case <-time.After(5 * time.Second):
		t.Fatal("server handshake did not conclude: configForClient is not bounding its trust resolution to a context derived from the handshake (handshakeTrustTimeout), so a resolver that never returns hangs the connection forever")
	}
}

// TestPool_Wire_ComposesWithExistingGetConfigForClient proves Wire does not
// silently discard a GetConfigForClient hook cfg already carries when Wire
// is called: the pre-existing hook must still run, on every handshake, and
// its own returned config must still be the base Pool's OWN widening
// applies on top of (go-qrdb#591's review, non-blocking).
func TestPool_Wire_ComposesWithExistingGetConfigForClient(t *testing.T) {
	var existingCalled bool
	existing := func(*tls.ClientHelloInfo) (*tls.Config, error) {
		existingCalled = true
		return &tls.Config{NextProtos: []string{"existing-hook-marker"}}, nil
	}

	cfg := &tls.Config{GetConfigForClient: existing}
	pool := New()
	pool.Wire(cfg)

	got := getConfigForClient(t, cfg)
	if !existingCalled {
		t.Fatal("expected the pre-existing GetConfigForClient hook to be called")
	}
	if got == nil {
		t.Fatal("expected a non-nil config")
	}
	if len(got.NextProtos) != 1 || got.NextProtos[0] != "existing-hook-marker" {
		t.Errorf("expected the pre-existing hook's own config to be the base Pool's widening applied to, got NextProtos=%v", got.NextProtos)
	}
}

// TestPool_Wire_ExistingHookSurvivesAFailedResolution proves that when Key
// B resolution itself fails, the pre-existing hook's config is still what
// is returned -- not silently discarded in favor of a bare nil, which
// would also discard whatever that hook's own override did.
func TestPool_Wire_ExistingHookSurvivesAFailedResolution(t *testing.T) {
	existing := func(*tls.ClientHelloInfo) (*tls.Config, error) {
		return &tls.Config{NextProtos: []string{"existing-hook-marker"}}, nil
	}

	cfg := &tls.Config{GetConfigForClient: existing}
	pool := New()
	pool.Wire(cfg)
	// No SetKeyBProvider call at all: resolveClientCAs fails with
	// *KeyBUnavailableError, the ordinary "nothing installed yet" case.

	got := getConfigForClient(t, cfg)
	if got == nil {
		t.Fatal("expected the pre-existing hook's config to survive a failed resolution")
	}
	if len(got.NextProtos) != 1 || got.NextProtos[0] != "existing-hook-marker" {
		t.Errorf("expected the pre-existing hook's config to survive untouched, got NextProtos=%v", got.NextProtos)
	}
}
