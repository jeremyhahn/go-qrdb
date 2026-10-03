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
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
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

func (f *fakeClusterCA) CACertificate() (*x509.Certificate, error) { return f.cert, f.err }

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
	if _, err := keyBCert.Verify(x509.VerifyOptions{Roots: got.ClientCAs, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		t.Errorf("Key B CA certificate was not added to the widened pool: %v", err)
	}
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
	if got := getConfigForClient(t, cfg); got != nil {
		t.Fatalf("expected (nil, nil) while ClusterCA fails, got %+v", got)
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
	if got.ClientCAs == nil {
		t.Fatal("expected ClientCAs to be set on the widened config")
	}
	if _, err := keyBCert.Verify(x509.VerifyOptions{Roots: got.ClientCAs, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		t.Errorf("Key B CA certificate was not added to the widened pool: %v", err)
	}
	if _, err := keyACert.Verify(x509.VerifyOptions{Roots: got.ClientCAs, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		t.Errorf("Key A was dropped from the widened pool: %v", err)
	}
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
	if got.ClientAuth != cfg.ClientAuth {
		t.Errorf("ClientAuth = %v, want %v", got.ClientAuth, cfg.ClientAuth)
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

	if got := getConfigForClient(t, cfg); got != nil {
		t.Fatalf("expected (nil, nil) when CACertPEMs fails (simulating sealed), got %+v", got)
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
	if _, err := keyACert.Verify(x509.VerifyOptions{Roots: got.ClientCAs, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		t.Errorf("expected Key A still trusted: %v", err)
	}
}

func TestPool_ProviderWithoutCertSourceFallsBackToStatic(t *testing.T) {
	_, keyACert := genCAPEM(t)
	cfg := baseConfig(t, x509.NewCertPool())
	pool := New()
	pool.Wire(cfg)
	pool.SetClusterCA(&fakeClusterCA{cert: keyACert})
	pool.SetKeyBProvider(&fakePlainCertProvider{})

	if got := getConfigForClient(t, cfg); got != nil {
		t.Fatalf("expected (nil, nil) for a CertProvider that does not implement CertSource, got %+v", got)
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
	if got := getConfigForClient(t, cfg); got != nil {
		t.Fatalf("expected (nil, nil) after SetKeyBProvider(nil), got %+v", got)
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
	if _, err := keyBCert.Verify(x509.VerifyOptions{Roots: got.ClientCAs, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		t.Errorf("Key B CA certificate was not added to the pool: %v", err)
	}
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
	if _, err := keyBCert2.Verify(x509.VerifyOptions{Roots: got.ClientCAs, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		t.Errorf("expected the NEW provider's cert in the pool: %v", err)
	}
}

// ---------------------------------------------------------------------------
// IsClusterCA / VerifyIssuer -- the issuer-tenant binding surface shared
// with pkg/auth (MTLSAuthenticator) and pkg/auth/middleware (on-behalf).
// ---------------------------------------------------------------------------

func TestPool_IsClusterCA(t *testing.T) {
	_, keyACert := genCAPEM(t)
	_, otherCert := genCAPEM(t)
	pool := New()

	if pool.IsClusterCA(keyACert) {
		t.Error("expected false with no ClusterCA configured")
	}
	pool.SetClusterCA(&fakeClusterCA{cert: keyACert})
	if !pool.IsClusterCA(keyACert) {
		t.Error("expected true for the configured cluster CA certificate")
	}
	if pool.IsClusterCA(otherCert) {
		t.Error("expected false for a different certificate")
	}
	if pool.IsClusterCA(nil) {
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
	pool.Wire(cfg)
	// No ClusterCA, no Key B provider at all: staticCert is trusted ONLY
	// because it was already in cfg.ClientCAs.
	getConfigForClient(t, cfg) // runs the handshake path, populating staticBase

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

func newManyTenantProvider(b *testing.B, n int) *manyTenantProvider {
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
