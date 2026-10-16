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
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
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

	// blockUntil, when non-nil, makes CACertPEMs wait for it to close
	// before returning -- after already having incremented
	// blockingCalls -- so a test can hold a rebuild inside its store read
	// (TestPool_HandshakeNeverWaitsOnABlockedRead,
	// TestPool_Refresh_SetDuringFlightWins). Separate from callCounts
	// because it is incremented with atomic, safe for the test goroutine
	// that polls it while the rebuild's goroutine is blocked.
	blockUntil    chan struct{}
	blockingCalls int32
}

var _ cert.CertProvider = (*fakeCertProvider)(nil)
var _ CertSource = (*fakeCertProvider)(nil)

// fakeInvalidatingCertProvider extends fakeCertProvider with
// InvalidationSource, so a test can prove Pool.SetKeyBProvider subscribes
// to it, and that firing the subscribed hook schedules the rebuild that
// drops a deleted tenant's CA (go-qrdb#591's review).
type fakeInvalidatingCertProvider struct {
	fakeCertProvider
	hooks []func()
}

func (f *fakeInvalidatingCertProvider) OnInvalidate(fn func()) {
	f.hooks = append(f.hooks, fn)
}

func (f *fakeInvalidatingCertProvider) fire() {
	for _, fn := range f.hooks {
		fn()
	}
}

var _ InvalidationSource = (*fakeInvalidatingCertProvider)(nil)

func (f *fakeCertProvider) IssueClientCert(context.Context, *cert.IssueRequest) (*cert.IssuedCert, error) {
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
	if f.blockUntil != nil {
		atomic.AddInt32(&f.blockingCalls, 1)
		<-f.blockUntil
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

func (f *fakePlainCertProvider) IssueClientCert(context.Context, *cert.IssueRequest) (*cert.IssuedCert, error) {
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

// parseTLSLeaf returns tc's leaf certificate, failing the test on error.
func parseTLSLeaf(t testing.TB, tc tls.Certificate) *x509.Certificate {
	t.Helper()
	leaf, err := x509.ParseCertificate(tc.Certificate[0])
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return leaf
}

// genLeafWithEKU signs a leaf certificate under caCert/caKey carrying ONLY
// the given extended key usages -- no ExtKeyUsageClientAuth unless eku
// names it -- for a test that needs a cert a client-auth verification must
// refuse on EKU grounds alone, with trust otherwise intact.
func genLeafWithEKU(t testing.TB, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, eku []x509.ExtKeyUsage) *x509.Certificate {
	t.Helper()
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey (leaf): %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "server-auth-only-leaf"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  eku,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("CreateCertificate (leaf): %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate (leaf): %v", err)
	}
	return leaf
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

// rebuild runs one Refresh, the rebuild Run performs in production, so the
// next handshake sees the trust state the test just configured. Its error
// is the test's to assert where it matters; most callers only need the
// snapshot published.
func rebuild(t testing.TB, p *Pool) {
	t.Helper()
	_ = p.Refresh(context.Background())
}

// assertKickPending proves Invalidate (directly, or through a hook that
// calls it) asked Run for a rebuild, and consumes that request.
func assertKickPending(t testing.TB, p *Pool) {
	t.Helper()
	select {
	case <-p.kick:
	default:
		t.Fatal("expected a rebuild request pending on the pool")
	}
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
	rebuild(t, pool)

	got := getConfigForClient(t, cfg)
	if got == cfg {
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
	rebuild(t, pool)
	if got := getConfigForClient(t, cfg); got != cfg {
		t.Fatalf("expected the unmodified base config while ClusterCA fails, got %+v", got)
	}

	// The SAME Pool, no re-wiring: a later rebuild where ClusterCA resolves
	// must succeed. A transient error must not disable the binding for the
	// life of the process.
	transientCA.err = nil
	transientCA.cert = keyACert
	rebuild(t, pool)
	got := getConfigForClient(t, cfg)
	if got == cfg {
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
	rebuild(t, pool)

	got := getConfigForClient(t, cfg)
	if got == cfg {
		t.Fatal("expected a widened config, got the static fallback")
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
	rebuild(t, pool)

	got := getConfigForClient(t, cfg)
	if got == cfg {
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
	rebuild(t, pool)

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
	rebuild(t, pool)

	// No tenant has established Key B yet (CACertPEMs succeeds with zero
	// entries): the widened pool trusts Key A, same as the static fallback
	// would, since there is nothing else to add.
	got := getConfigForClient(t, cfg)
	if got == cfg {
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
	rebuild(t, pool)

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
	rebuild(t, pool)
	if got := getConfigForClient(t, cfg); got == cfg {
		t.Fatal("expected a widened config before reverting")
	}

	// No rebuild: SetKeyBProvider withdraws the snapshot itself, so the
	// revert is immediate.
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
	rebuild(t, pool)

	got := getConfigForClient(t, cfg)
	if got == cfg {
		t.Fatal("expected a widened config even when the base ClientCAs was nil")
	}
	assertTrustedByConnection(t, got, keyBCert)
}

// ---------------------------------------------------------------------------
// B2-4: the handshake never pays the pre-auth cost of scanning every
// tenant's CA; only a rebuild does.
// ---------------------------------------------------------------------------

func TestPool_HandshakesNeverReadTheStore(t *testing.T) {
	_, keyACert := genCAPEM(t)
	cfg := baseConfig(t, x509.NewCertPool())
	pool := New()
	pool.Wire(cfg)
	pool.SetClusterCA(&fakeClusterCA{cert: keyACert})

	keyBPEM, _ := genCAPEM(t)
	calls := 0
	pool.SetKeyBProvider(&fakeCertProvider{pems: [][]byte{keyBPEM}, callCounts: &calls})
	rebuild(t, pool)

	for i := 0; i < 5; i++ {
		if got := getConfigForClient(t, cfg); got == cfg {
			t.Fatalf("call %d: expected a widened config", i)
		}
	}
	if calls != 1 {
		t.Errorf("CACertPEMs called %d times for one rebuild and 5 connections, want 1 (handshakes never read)", calls)
	}
}

func TestPool_SetKeyBProviderWithdrawsSnapshot(t *testing.T) {
	_, keyACert := genCAPEM(t)
	cfg := baseConfig(t, x509.NewCertPool())
	pool := New()
	pool.Wire(cfg)
	pool.SetClusterCA(&fakeClusterCA{cert: keyACert})

	keyBPEM1, _ := genCAPEM(t)
	calls1 := 0
	pool.SetKeyBProvider(&fakeCertProvider{pems: [][]byte{keyBPEM1}, callCounts: &calls1})
	rebuild(t, pool)

	keyBPEM2, keyBCert2 := genCAPEM(t)
	calls2 := 0
	pool.SetKeyBProvider(&fakeCertProvider{pems: [][]byte{keyBPEM2}, callCounts: &calls2})
	if got := getConfigForClient(t, cfg); got != cfg {
		t.Fatal("expected trust read from the replaced provider to be withdrawn at once")
	}
	assertKickPending(t, pool)
	rebuild(t, pool)
	if calls2 != 1 {
		t.Errorf("expected the new provider to be read by the rebuild SetKeyBProvider asked for, calls=%d", calls2)
	}
	assertTrustedByConnection(t, getConfigForClient(t, cfg), keyBCert2)
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
	if err := pool.VerifyIssuer(context.Background(), "any-tenant", chainOf(keyACert)); err != nil {
		t.Errorf("expected the cluster CA to be exempt regardless of tenant, got: %v", err)
	}
}

func TestPool_VerifyIssuer_OwnTenantCAAccepted(t *testing.T) {
	_, keyACert := genCAPEM(t)
	_, keyBCertA := genCAPEM(t)
	pool := New()
	pool.SetClusterCA(&fakeClusterCA{cert: keyACert})
	pool.SetKeyBProvider(&fakeCertProvider{registry: map[string]*x509.Certificate{"A": keyBCertA}})

	if err := pool.VerifyIssuer(context.Background(), "A", chainOf(keyBCertA)); err != nil {
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

	err := pool.VerifyIssuer(context.Background(), "B", chainOf(keyBCertA))
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
// regression proof: before the fix, the pool's resolution returned on a
// BROKEN (not merely unconfigured) cluster CA resolver before ever reaching
// CACertPEMs, so the Key-B-only pool isRegisteredKeyBCA reads never
// refreshed and stayed "uncertain" (true) forever -- which
// IsStaticallyTrusted treats as "assume this issuer might be a Key B CA,
// refuse the exemption", for every issuer, including the harness's own
// static CA that has nothing to do with Key B at all. This test runs the
// rebuild directly to observe its effect on IsStaticallyTrusted without
// needing a live TLS handshake.
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

	// The widened pool itself correctly fails closed (the cluster CA entry
	// and exemption are unavailable this rebuild), but the Key-B-only pool
	// underneath must still have been read.
	if err := pool.Refresh(context.Background()); err == nil {
		t.Fatal("expected Refresh to fail closed while the cluster CA resolver is broken")
	}

	if !pool.IsStaticallyTrusted(harnessCACert) {
		t.Fatal("harness login (static exemption) was refused by a cluster CA failure that has nothing to do with it")
	}
}

// TestPool_KeyBNeverSucceeded_StaticExemptionStillHonored is the B3
// remainder's red/green proof (go-qrdb#591's review): on a self-issued
// cluster, Key B's CACertPEMs has NEVER succeeded before the barrier is
// first unsealed -- that is the ordinary boot sequence, not an edge case
// -- and the static exemption must still accept a certificate that
// verifies against a CONFIGURED static anchor (an operator's
// mtls.client_ca_files entry, or a test harness's own CA) regardless of
// Key B's state. B3's actual requirement is narrower than the prior
// behavior enforced: static, Key A and operator anchors are ALWAYS
// honored, whatever Key B's state; only an ACTUAL Key B-issued
// certificate -- one that affirmatively verifies against a BUILT
// Key-B-only cache -- may fail closed. An unbuilt cache proves nothing
// about the issuer either way, so it must not be treated as "assume
// it's a Key B CA"; isRegisteredKeyBCA now answers false, not true,
// when the cache has never been built, and the confirmed-match case
// TestPool_IsStaticallyTrusted_RefusesIssuerAlsoRegisteredAsKeyBCA pins
// is unaffected: that test builds the cache first, so its match is
// never "uncertain".
func TestPool_KeyBNeverSucceeded_StaticExemptionStillHonored(t *testing.T) {
	harnessCAPEM, harnessCACert := genCAPEM(t)
	harnessPool := x509.NewCertPool()
	harnessPool.AppendCertsFromPEM(harnessCAPEM)
	cfg := baseConfig(t, harnessPool)

	pool := New()
	pool.Wire(cfg)
	pool.SetKeyBProvider(&fakeCertProvider{err: errors.New("barrier is sealed")})

	if err := pool.Refresh(context.Background()); err == nil {
		t.Fatal("expected Refresh to fail while Key B has never once succeeded")
	}

	// The static anchor must still be honored: it is not, and has never
	// been claimed to be, a Key B CA -- Key B's own unresolved state is
	// not evidence against it.
	if !pool.IsStaticallyTrusted(harnessCACert) {
		t.Fatal("expected the static exemption to be honored even though Key B has never once resolved (B3 remainder)")
	}
}

func TestPool_VerifyIssuer_NoProviderRefused(t *testing.T) {
	_, keyACert := genCAPEM(t)
	_, keyBCert := genCAPEM(t)
	pool := New()
	pool.SetClusterCA(&fakeClusterCA{cert: keyACert})

	if err := pool.VerifyIssuer(context.Background(), "A", chainOf(keyBCert)); err == nil {
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

	if err := pool.VerifyIssuer(context.Background(), "any-tenant", chainOf(staticCert)); err != nil {
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

	err := pool.VerifyIssuer(context.Background(), "B", chainOf(keyBCertA))
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
	// Rebuild once so the Key-B-only pool is built from the SAME
	// CACertPEMs the registry mirrors.
	rebuild(t, pool)
	if got := getConfigForClient(t, cfg); got == cfg {
		t.Fatal("expected a widened config")
	}

	if pool.IsStaticallyTrusted(keyBCertA) {
		t.Fatal("expected the static exemption to be refused for an issuer that is also a registered Key B CA")
	}

	// The per-tenant binding still correctly scopes it to tenant A alone:
	// accepted for A, refused for a leaf claiming any other tenant.
	if err := pool.VerifyIssuer(context.Background(), "A", chainOf(keyBCertA)); err != nil {
		t.Errorf("expected tenant A's own Key B CA to still be accepted for tenant A, got: %v", err)
	}
	err := pool.VerifyIssuer(context.Background(), "B", chainOf(keyBCertA))
	if err == nil {
		t.Fatal("expected tenant A's Key B CA, even though it is ALSO in the static ClientCAs, to be refused for a leaf claiming tenant B")
	}
	var mismatch *IssuerNotTrustedError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected *IssuerNotTrustedError, got %T: %v", err, err)
	}
}

// TestPool_IsStaticallyTrusted_UncertainBeforeCacheIsHonored is
// isRegisteredKeyBCA's "uncertain" branch, corrected per the B3 remainder
// (go-qrdb#591's review): a Key B provider IS installed (so there is a
// Key B concept in play for this Pool), but no rebuild has run yet, so
// trustSnapshot.keyBOnly does not exist. An unbuilt cache is not evidence
// that staticCert IS a Key B CA -- it proves nothing about it either way
// -- so IsStaticallyTrusted must honor the static exemption rather than
// refuse on an unproven suspicion; only an AFFIRMATIVELY confirmed Key B
// CA match (a built cache the issuer actually verifies against, see
// TestPool_IsStaticallyTrusted_RefusesIssuerAlsoRegisteredAsKeyBCA) may
// still refuse it.
func TestPool_IsStaticallyTrusted_UncertainBeforeCacheIsHonored(t *testing.T) {
	_, staticCert := genCAPEM(t)
	staticPool := x509.NewCertPool()
	staticPool.AddCert(staticCert)
	cfg := baseConfig(t, staticPool)

	pool := New()
	pool.Wire(cfg)
	pool.SetKeyBProvider(&fakeCertProvider{})
	// No rebuild run: the snapshot is the withdrawal SetKeyBProvider
	// published, with no Key-B-only pool.

	if !pool.IsStaticallyTrusted(staticCert) {
		t.Fatal("expected the static exemption to be honored while the Key-B-only cache is still unbuilt (B3 remainder)")
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

	err := pool.VerifyIssuer(context.Background(), "A", chainOf(keyBCert))
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

	err := pool.VerifyIssuer(context.Background(), "A", chainOf(keyBCert))
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

func (m *manyTenantProvider) IssueClientCert(context.Context, *cert.IssueRequest) (*cert.IssuedCert, error) {
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
// re-scans and re-parses every tenant's CA certificate (rebuilding the pool
// from the PEMs directly, the shape DefaultCertProvider.CACertPEMs' caller
// used to pay on every handshake before the pool was cached).
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

// BenchmarkPool_Handshake_1000Tenants is the fixed path: the same
// 1,000-tenant cost is paid once per rebuild, in the background, and a
// handshake only loads the snapshot.
func BenchmarkPool_Handshake_1000Tenants(b *testing.B) {
	_, keyACert := genCAPEM(b)
	cfg := baseConfig(b, x509.NewCertPool())
	pool := New()
	pool.Wire(cfg)
	pool.SetClusterCA(&fakeClusterCA{cert: keyACert})
	pool.SetKeyBProvider(newManyTenantProvider(b, 1000))
	if err := pool.Refresh(context.Background()); err != nil {
		b.Fatalf("Refresh: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := pool.configForClient(cfg); got == cfg {
			b.Fatal("expected a widened config")
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
	rebuild(t, pool)

	var acceptableCAs [][]byte
	clientCfg := &tls.Config{
		InsecureSkipVerify: true, // #nosec G402 -- this test measures the SERVER's advertised CAs, not the client's verification of the server
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
// done, then reports ctx's own error: a store that has stopped answering,
// as one with no Raft leader does while it holds a read through an
// election.
type blockingClusterCA struct{}

func (blockingClusterCA) CACertificate(ctx context.Context) (*x509.Certificate, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestPool_Refresh_BoundedByRefreshTimeout proves the second line of
// defence the go-qrdb#713 ruling keeps: a rebuild's reads are bounded by
// refreshTimeout even when the caller's own context never ends, so a store
// that stops answering costs one failed rebuild -- which withdraws Key B
// trust (go-qrdb#591, blocker 2) -- rather than a Run loop stuck forever.
//
// refreshTimeout is shortened so the assertion is cheap; the caller's
// context is context.Background(), so any bound observed can only be
// Refresh's own.
func TestPool_Refresh_BoundedByRefreshTimeout(t *testing.T) {
	saved := refreshTimeout
	refreshTimeout = 50 * time.Millisecond
	t.Cleanup(func() { refreshTimeout = saved })

	keyAPool := x509.NewCertPool()
	cfg := baseConfig(t, keyAPool)
	keyBPEM, keyBCert := genCAPEM(t)
	pool := New()
	pool.Wire(cfg)
	pool.SetKeyBProvider(&fakeCertProvider{pems: [][]byte{keyBPEM}})
	rebuild(t, pool)
	assertTrustedByConnection(t, getConfigForClient(t, cfg), keyBCert)

	pool.SetClusterCA(blockingClusterCA{})
	done := make(chan error, 1)
	go func() { done <- pool.Refresh(context.Background()) }()
	select {
	case err := <-done:
		var unavailable *ClusterCAUnavailableError
		if !errors.As(err, &unavailable) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Refresh error = %v, want *ClusterCAUnavailableError wrapping context.DeadlineExceeded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Refresh did not return: its reads are not bounded by refreshTimeout")
	}
	if got := getConfigForClient(t, cfg); got != cfg {
		t.Fatal("expected a timed-out rebuild to withdraw Key B trust")
	}
}

// TestPool_HandshakeNeverWaitsOnABlockedRead is go-qrdb#713's red/green
// proof: with a rebuild stuck inside a storage read -- a store with no
// leader holds a read through an election for longer than the 5 s TLS
// handshake deadline -- a handshake must return at once, widened from the
// last snapshot built, rather than wait on that read and time out.
func TestPool_HandshakeNeverWaitsOnABlockedRead(t *testing.T) {
	_, keyACert := genCAPEM(t)
	keyAPool := x509.NewCertPool()
	keyAPool.AddCert(keyACert)
	cfg := baseConfig(t, keyAPool)

	keyBPEM, keyBCert := genCAPEM(t)
	prov := &fakeCertProvider{pems: [][]byte{keyBPEM}}
	pool := New()
	pool.Wire(cfg)
	pool.SetClusterCA(&fakeClusterCA{cert: keyACert})
	pool.SetKeyBProvider(prov)
	if err := pool.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	// From here every Key B read blocks until released, and a rebuild is
	// already inside one when the handshake arrives.
	release := make(chan struct{})
	defer close(release)
	prov.blockUntil = release
	pool.Invalidate()
	go func() { _ = pool.Refresh(context.Background()) }()
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&prov.blockingCalls) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the rebuild to enter CACertPEMs")
		}
		time.Sleep(time.Millisecond)
	}

	done := make(chan *tls.Config, 1)
	go func() {
		got, _ := cfg.GetConfigForClient(&tls.ClientHelloInfo{})
		done <- got
	}()
	select {
	case got := <-done:
		assertTrustedByConnection(t, got, keyBCert)
	case <-time.After(time.Second):
		t.Fatal("the handshake waited on a blocked trust read; it must return at once with the last built pool")
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
	// No SetKeyBProvider call and no rebuild at all: there is no snapshot,
	// the ordinary "nothing installed yet" case.

	got := getConfigForClient(t, cfg)
	if got == nil {
		t.Fatal("expected the pre-existing hook's config to survive a failed resolution")
	}
	if len(got.NextProtos) != 1 || got.NextProtos[0] != "existing-hook-marker" {
		t.Errorf("expected the pre-existing hook's config to survive untouched, got NextProtos=%v", got.NextProtos)
	}
}

// TestPool_SetKeyBProvider_SubscribesToInvalidationSource proves Pool
// subscribes its rebuild to a provider that implements InvalidationSource,
// so a deleted tenant's CA stops being trusted as soon as the rebuild that
// hook schedules lands -- not merely at the next poolCacheTTL tick
// (go-qrdb#591's review).
func TestPool_SetKeyBProvider_SubscribesToInvalidationSource(t *testing.T) {
	_, keyACert := genCAPEM(t)
	keyAPool := x509.NewCertPool()
	keyAPool.AddCert(keyACert)
	cfg := baseConfig(t, keyAPool)

	pool := New()
	pool.Wire(cfg)
	pool.SetClusterCA(&fakeClusterCA{cert: keyACert})

	keyBPEM, keyBCert := genCAPEM(t)
	prov := &fakeInvalidatingCertProvider{fakeCertProvider: fakeCertProvider{pems: [][]byte{keyBPEM}}}
	pool.SetKeyBProvider(prov)
	rebuild(t, pool)

	got := getConfigForClient(t, cfg)
	if got == cfg {
		t.Fatal("expected a widened config")
	}
	assertTrustedByConnection(t, got, keyBCert)

	// Simulate the tenant's CA being deleted (CACertPEMs now returns
	// nothing for it) and fire the invalidation hook: it must schedule a
	// rebuild, and that rebuild must drop keyBCert.
	prov.pems = nil
	prov.fire()
	assertKickPending(t, pool)
	rebuild(t, pool)

	got2 := getConfigForClient(t, cfg)
	if got2 == nil {
		t.Fatal("expected a config (possibly the static fallback) after invalidation")
	}
	assertNotTrustedByConnection(t, got2, keyBCert)
}

// TestPool_Invalidate_CollapsesAndNeverBlocks proves Invalidate is safe to
// call from a store watcher's own goroutine: any number of calls with no
// Run loop draining them neither blocks nor queues more than the one
// pending rebuild.
func TestPool_Invalidate_CollapsesAndNeverBlocks(t *testing.T) {
	pool := New()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			pool.Invalidate()
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Invalidate blocked with no Run loop draining it")
	}
	if got := len(pool.kick); got != 1 {
		t.Errorf("expected exactly one pending rebuild after 100 invalidations, got %d", got)
	}
}

// TestPool_Refresh_SetDuringFlightWins is go-qrdb#591's review red/green
// proof, carried to the snapshot design: a configuration change
// (SetKeyBProvider here) landing WHILE a rebuild's own CACertPEMs call is
// still in flight must not be undone by that rebuild's publish once it
// finally returns. Refresh publishes with a compare-and-swap against the
// snapshot it started from, so the setter's withdrawal wins and the stale,
// pre-change result is discarded.
func TestPool_Refresh_SetDuringFlightWins(t *testing.T) {
	_, keyACert := genCAPEM(t)
	keyAPool := x509.NewCertPool()
	keyAPool.AddCert(keyACert)
	cfg := baseConfig(t, keyAPool)

	keyBPEM, keyBCert := genCAPEM(t)
	prov := &fakeCertProvider{pems: [][]byte{keyBPEM}, blockUntil: make(chan struct{})}
	pool := New()
	pool.Wire(cfg)
	pool.SetKeyBProvider(prov)

	refreshDone := make(chan struct{})
	go func() {
		defer close(refreshDone)
		_ = pool.Refresh(context.Background())
	}()

	// Wait for the rebuild to actually be inside the blocking CACertPEMs
	// call before the change, so it genuinely lands mid-flight.
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&prov.blockingCalls) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the rebuild to enter CACertPEMs")
		}
		time.Sleep(time.Millisecond)
	}

	pool.SetKeyBProvider(nil)
	close(prov.blockUntil)
	<-refreshDone

	if got := getConfigForClient(t, cfg); got != cfg {
		assertNotTrustedByConnection(t, got, keyBCert)
		t.Fatal("expected the in-flight rebuild's stale result to be discarded after a change mid-flight")
	}
}

// steppedCertProvider hands every CACertPEMs call to the test, which answers
// it, so a test decides what each rebuild reads and when its read returns.
type steppedCertProvider struct {
	*fakeCertProvider
	reads chan chan [][]byte
}

func (s *steppedCertProvider) CACertPEMs(ctx context.Context) ([][]byte, error) {
	reply := make(chan [][]byte)
	select {
	case s.reads <- reply:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case pems := <-reply:
		return pems, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// TestPool_Refresh_PublishesARebuildThatBeganAfterTheCall pins what a
// caller of Refresh relies on (a tenant create answering with a certificate
// its own CA has just signed): when Refresh returns, the snapshot handshakes
// load was built after the call began. An earlier rebuild that read the
// store before the CA existed and lands first must not leave the caller's
// handshake without the CA. Refresh used to discard its own rebuild on
// losing that swap and return nil, so the next handshake was refused with
// "certificate signed by unknown authority" (run 53320).
func TestPool_Refresh_PublishesARebuildThatBeganAfterTheCall(t *testing.T) {
	_, keyACert := genCAPEM(t)
	keyAPool := x509.NewCertPool()
	keyAPool.AddCert(keyACert)
	cfg := baseConfig(t, keyAPool)

	keyBPEM, keyBCert := genCAPEM(t)
	prov := &steppedCertProvider{fakeCertProvider: &fakeCertProvider{}, reads: make(chan chan [][]byte)}
	pool := New()
	pool.Wire(cfg)
	pool.SetKeyBProvider(prov)

	// An earlier rebuild is inside its store read when the CA comes into use.
	earlierDone := make(chan error, 1)
	go func() { earlierDone <- pool.Refresh(context.Background()) }()
	earlier := <-prov.reads

	// The caller's Refresh begins after the CA is in use, so its read sees it.
	laterDone := make(chan error, 1)
	go func() { laterDone <- pool.Refresh(context.Background()) }()
	later := <-prov.reads

	// The earlier rebuild lands first, with what it read before the CA...
	earlier <- nil
	if err := <-earlierDone; err != nil {
		t.Fatalf("earlier Refresh: %v", err)
	}
	// ... so the caller's rebuild loses the swap. Every read it makes from
	// here on sees the CA.
	later <- [][]byte{keyBPEM}
	for {
		select {
		case reply := <-prov.reads:
			reply <- [][]byte{keyBPEM}
			continue
		case err := <-laterDone:
			if err != nil {
				t.Fatalf("Refresh: %v", err)
			}
		}
		break
	}

	assertTrustedByConnection(t, getConfigForClient(t, cfg), keyBCert)
}

// TestPool_Refresh_SupersededUntilItsBoundRunsOut proves a Refresh whose
// rebuild a concurrent change superseded, and whose bound then ran out,
// reports that nothing it built was published rather than returning nil.
func TestPool_Refresh_SupersededUntilItsBoundRunsOut(t *testing.T) {
	cfg := baseConfig(t, x509.NewCertPool())
	prov := &steppedCertProvider{fakeCertProvider: &fakeCertProvider{}, reads: make(chan chan [][]byte)}
	pool := New()
	pool.Wire(cfg)
	pool.SetKeyBProvider(prov)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- pool.Refresh(ctx) }()

	// The rebuild is inside its read when a change lands, and its bound runs
	// out before the read is ever answered.
	<-prov.reads
	pool.SetKeyBProvider(prov)
	cancel()

	err := <-done
	var superseded *RefreshSupersededError
	if !errors.As(err, &superseded) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Refresh error = %v, want *RefreshSupersededError wrapping context.Canceled", err)
	}
}

// TestPool_Reseal_DropsKeyBTrust is go-qrdb#591's review (blocker 2) red/
// green proof: docs/certificate-authority/architecture.md promises that a
// runtime reseal makes the dynamic trust pool drop Key B at the next
// rebuild, never indefinitely: a rebuild whose CACertPEMs fails publishes a
// snapshot with no Key B trust rather than keeping the last good one (the
// pre-#591 contract for a merely transient blip), so a reseal never leaves
// the listener widening from certificates read before the seal.
//
// A Key B-issued leaf authenticates while the provider is healthy; once
// CACertPEMs starts failing (simulating a reseal) and a rebuild has run,
// the SAME leaf must be refused, and the listener's GetConfigForClient
// hook must have reverted to the static baseline: no VerifyConnection
// override installed, so a subsequent real handshake's verification runs
// through crypto/tls's OWN path and populates
// ConnectionState.VerifiedChains -- never populated for a connection this
// Pool's own VerifyConnection hook handled (see VerifyPeerChain's doc
// comment) -- directly proving the pool, not merely Pool's own
// bookkeeping, reverted.
func TestPool_Reseal_DropsKeyBTrust(t *testing.T) {
	operatorCAPEM, operatorCACert, operatorCAKey := genCAWithKey(t, "operator-static-ca")
	operatorPool := x509.NewCertPool()
	operatorPool.AppendCertsFromPEM(operatorCAPEM)

	keyBPEM, keyBCACert, keyBCAKey := genCAWithKey(t, "tenant-key-b-ca")

	serverLeaf := genLeafTLSCert(t, operatorCACert, operatorCAKey)
	keyBClientLeaf := genLeafTLSCert(t, keyBCACert, keyBCAKey)
	operatorClientLeaf := genLeafTLSCert(t, operatorCACert, operatorCAKey)

	serverCfg := &tls.Config{
		Certificates: []tls.Certificate{serverLeaf},
		ClientCAs:    operatorPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}
	pool := New()
	pool.Wire(serverCfg)
	prov := &fakeCertProvider{pems: [][]byte{keyBPEM}}
	pool.SetKeyBProvider(prov)
	rebuild(t, pool)

	ln, err := tls.Listen("tcp", "127.0.0.1:0", serverCfg)
	if err != nil {
		t.Fatalf("tls.Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	handshake := func(clientLeaf tls.Certificate) (serverErr, clientErr error, serverCS tls.ConnectionState) {
		type acceptResult struct {
			conn *tls.Conn
			err  error
		}
		accepted := make(chan acceptResult, 1)
		go func() {
			conn, aerr := ln.Accept()
			if aerr != nil {
				accepted <- acceptResult{err: aerr}
				return
			}
			tc := conn.(*tls.Conn)
			accepted <- acceptResult{conn: tc, err: tc.Handshake()}
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		rawConn, derr := (&net.Dialer{}).DialContext(ctx, "tcp", ln.Addr().String())
		if derr != nil {
			t.Fatalf("dial: %v", derr)
		}
		clientTLS := tls.Client(rawConn, &tls.Config{
			// GetClientCertificate, not a static Certificates field: Go's
			// own client-side selection logic for a static field filters
			// against the server's advertised AcceptableCAs -- which this
			// Pool deliberately never widens (see configForClient's doc
			// comment) -- and silently sends no certificate at all when
			// none of the client's configured certs match, exactly the
			// case a Key B-signed leaf hits. GetClientCertificate bypasses
			// that selection entirely, matching what a real client
			// presenting a Key B leaf actually does.
			GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &clientLeaf, nil },
			InsecureSkipVerify:   true, // #nosec G402 -- this test measures the SERVER's verification of the client cert, not the reverse
			MinVersion:           tls.VersionTLS13,
		})
		defer func() { _ = clientTLS.Close() }()
		clientErr = clientTLS.HandshakeContext(ctx)

		select {
		case res := <-accepted:
			serverErr = res.err
			if res.conn != nil {
				defer func() { _ = res.conn.Close() }()
				serverCS = res.conn.ConnectionState()
			}
		case <-ctx.Done():
			t.Fatal("timed out waiting for the server-side handshake")
		}
		return serverErr, clientErr, serverCS
	}

	// Healthy: the Key B-issued leaf authenticates, through Pool's own
	// VerifyConnection hook (which never populates VerifiedChains itself
	// -- see VerifyPeerChain's doc comment).
	if serverErr, clientErr, cs := handshake(keyBClientLeaf); serverErr != nil || clientErr != nil {
		t.Fatalf("expected the Key B leaf to authenticate while healthy: server=%v client=%v", serverErr, clientErr)
	} else if len(cs.VerifiedChains) != 0 {
		t.Error("expected VerifiedChains empty while Pool's own VerifyConnection hook is active")
	}

	// Simulate a reseal: CACertPEMs starts failing, and the next rebuild
	// runs -- it must publish a snapshot with no Key B trust rather than
	// keep the last good one.
	prov.err = errors.New("barrier is sealed")
	rebuild(t, pool)

	// The Key B leaf must now be refused outright.
	if serverErr, clientErr, _ := handshake(keyBClientLeaf); serverErr == nil && clientErr == nil {
		t.Fatal("expected the Key B leaf to be refused once a rebuild has seen CACertPEMs failing")
	}

	// A leaf the STATIC baseline (the operator's own CA) signs must still
	// authenticate -- through crypto/tls's OWN native verification this
	// time, not Pool's VerifyConnection hook, proving the hook reverted to
	// the static baseline rather than merely refusing everything.
	serverErr, clientErr, cs := handshake(operatorClientLeaf)
	if serverErr != nil || clientErr != nil {
		t.Fatalf("expected the operator's own static-anchor leaf to still authenticate after the reseal: server=%v client=%v", serverErr, clientErr)
	}
	if len(cs.VerifiedChains) == 0 {
		t.Fatal("expected VerifiedChains populated by crypto/tls's own verification, proving the hook returned the unmodified static-baseline config")
	}
}

// TestPool_ClientAuthEKU_RefusesServerAuthOnlyLeaf is the red/green proof
// for go-qrdb#591's review (blocker 3): a leaf carrying ONLY
// ExtKeyUsageServerAuth -- never issued for client authentication, but
// trust-wise signed by a CA this Pool DOES trust (Key A, or an operator's
// own static anchor) -- must still be refused, by EKU, at both
// verification points every connection this Pool wired is checked
// against: the handshake's own VerifyConnection hook (verifyPeerConnection)
// and the authenticator's re-derivation path (VerifyPeerChain). Using
// ExtKeyUsageAny for these two checks (as opposed to IsStaticallyTrusted
// and isRegisteredKeyBCA, which stay on Any -- they decide TRUST, not EKU
// policy) would accept a certificate this cluster issued, or an operator
// configured, for an entirely different purpose as a client identity.
func TestPool_ClientAuthEKU_RefusesServerAuthOnlyLeaf(t *testing.T) {
	_, keyACert, keyAKey := genCAWithKey(t, "key-a")
	operatorPEM, operatorCert, operatorKey := genCAWithKey(t, "operator-ca")

	staticPool := x509.NewCertPool()
	staticPool.AppendCertsFromPEM(operatorPEM)
	cfg := baseConfig(t, staticPool)

	pool := New()
	pool.Wire(cfg)
	pool.SetClusterCA(&fakeClusterCA{cert: keyACert})
	pool.SetKeyBProvider(&fakeCertProvider{})
	rebuild(t, pool)

	widened := pool.widenedFor(cfg.ClientCAs)
	if widened == nil {
		t.Fatal("expected a widened pool after a successful rebuild")
	}

	cases := []struct {
		name string
		leaf *x509.Certificate
	}{
		{"signed by Key A", genLeafWithEKU(t, keyACert, keyAKey, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})},
		{"signed by the operator CA", genLeafWithEKU(t, operatorCert, operatorKey, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cs := tls.ConnectionState{PeerCertificates: []*x509.Certificate{c.leaf}}
			if err := pool.verifyPeerConnection(cs, widened); err == nil {
				t.Error("expected verifyPeerConnection to refuse a serverAuth-only leaf")
			}
			if _, err := pool.VerifyPeerChain(context.Background(), c.leaf, nil); err == nil {
				t.Error("expected VerifyPeerChain to refuse a serverAuth-only leaf")
			}
		})
	}
}

// TestRV794_LongLivedConnStaticAnchorAfterReseal is the red/green proof for
// go-qrdb#591's final delta review (re-review blocker 2 regression): a
// long-lived connection's own TLS handshake is never re-run after a
// reseal, but pkg/auth's authenticator calls VerifyPeerChain on every
// request once VerifiedChains comes back empty -- which Wire's ClientAuth
// remap makes true for EVERY connection this Pool wires, healthy or not
// (see VerifyPeerChain's own doc comment). Before this fix, a
// failed Key B resolution (sealed, or any other transient CACertPEMs
// failure) made VerifyPeerChain refuse EVERY leaf
// outright, including a Key A-issued or operator-CA-issued leaf that a
// FRESH handshake made in that exact same sealed state would still accept
// (configForClient's own fallback to the unmodified cfg, verified by
// crypto/tls directly against cfg's static ClientCAs). A pooled connection
// that authenticated while Key B was healthy must keep letting
// CallerPrincipal/quorumCaller see a verified chain across a reseal; only
// a Key B-issued leaf, never part of the static baseline, is refused once
// Key B stops resolving.
func TestRV794_LongLivedConnStaticAnchorAfterReseal(t *testing.T) {
	keyAPEM, keyACert, keyAKey := genCAWithKey(t, "key-a")
	operatorPEM, operatorCert, operatorKey := genCAWithKey(t, "operator-ca")
	keyBPEM, keyBCert, keyBKey := genCAWithKey(t, "tenant-key-b-ca")

	staticPool := x509.NewCertPool()
	staticPool.AppendCertsFromPEM(keyAPEM)
	staticPool.AppendCertsFromPEM(operatorPEM)
	cfg := baseConfig(t, staticPool)

	pool := New()
	pool.Wire(cfg)
	pool.SetClusterCA(&fakeClusterCA{cert: keyACert})
	prov := &fakeCertProvider{pems: [][]byte{keyBPEM}}
	pool.SetKeyBProvider(prov)
	rebuild(t, pool)

	parseLeaf := func(tc tls.Certificate) *x509.Certificate {
		leaf, err := x509.ParseCertificate(tc.Certificate[0])
		if err != nil {
			t.Fatalf("ParseCertificate: %v", err)
		}
		return leaf
	}
	keyALeaf := parseLeaf(genLeafTLSCert(t, keyACert, keyAKey))
	operatorLeaf := parseLeaf(genLeafTLSCert(t, operatorCert, operatorKey))
	keyBLeaf := parseLeaf(genLeafTLSCert(t, keyBCert, keyBKey))

	ctx := context.Background()

	// Healthy: every leaf -- Key A, the operator's own static anchor, and
	// Key B -- authenticates on a connection re-verified through
	// VerifyPeerChain, matching what a fresh handshake's own widened pool
	// would accept.
	for name, leaf := range map[string]*x509.Certificate{"keyA": keyALeaf, "operator": operatorLeaf, "keyB": keyBLeaf} {
		if _, err := pool.VerifyPeerChain(ctx, leaf, nil); err != nil {
			t.Fatalf("%s: expected VerifyPeerChain to accept while healthy: %v", name, err)
		}
	}

	// Simulate a reseal: CACertPEMs starts failing, and the next rebuild
	// runs, so the snapshot is not merely a stale pre-reseal one. The
	// cluster CA resolver itself stays healthy throughout, matching an
	// ordinary reseal where Key A's own certificate is still readable.
	prov.err = errors.New("barrier is sealed")
	rebuild(t, pool)

	// A long-lived connection presenting a Key A or operator-CA leaf must
	// keep authenticating -- the regression this test guards against.
	for name, leaf := range map[string]*x509.Certificate{"keyA": keyALeaf, "operator": operatorLeaf} {
		if _, err := pool.VerifyPeerChain(ctx, leaf, nil); err != nil {
			t.Errorf("%s: static anchor refused on an existing connection after reseal: %v", name, err)
		}
	}

	// A Key B leaf on a long-lived connection must still be refused once
	// Key B itself is unavailable: the static-baseline fallback must never
	// widen to include Key B.
	if _, err := pool.VerifyPeerChain(ctx, keyBLeaf, nil); err == nil {
		t.Error("expected a Key B leaf to be refused on a long-lived connection after Key B became unavailable")
	}
}

// chainOf is a verified chain whose direct issuer is issuer: the shape
// VerifyIssuer receives from the mTLS authenticator.
func chainOf(issuer *x509.Certificate) []*x509.Certificate {
	return []*x509.Certificate{{Raw: []byte("leaf")}, issuer}
}

// ---------------------------------------------------------------------------
// SetTrustAnchors / Invalidate / anchorBundles / VerifyIssuer's anchor
// branch -- the bring-your-own trust anchor surface (pkg/trustanchor). The
// REAL trustanchor.KVRegistry over an in-process kvstore.MemoryKVStore is
// used throughout, rather than a hand-rolled fake, so these tests exercise
// the actual per-tenant confinement the registry provides, not an assumption
// about it.
// ---------------------------------------------------------------------------

// newRegistry returns a real trustanchor.KVRegistry over an empty in-process
// store.
func newRegistry(t testing.TB) *trustanchor.KVRegistry {
	t.Helper()
	return trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore())
}

// registerAnchor adds certPEM as a trust anchor for tenantID in reg, failing
// the test on error.
func registerAnchor(t testing.TB, reg *trustanchor.KVRegistry, tenantID string, certPEM []byte) {
	t.Helper()
	if _, err := reg.Add(context.Background(), tenantID, certPEM); err != nil {
		t.Fatalf("Add(%s): %v", tenantID, err)
	}
}

// listerOf adapts a fixed tenant ID slice to TenantLister via
// TenantListerFunc -- also the only direct exercise of that adapter.
func listerOf(ids ...string) TenantLister {
	return TenantListerFunc(func(context.Context) ([]string, error) { return ids, nil })
}

func TestTenantListerFunc_AdaptsFunction(t *testing.T) {
	f := TenantListerFunc(func(context.Context) ([]string, error) { return []string{"x", "y"}, nil })
	ids, err := f.TenantIDs(context.Background())
	if err != nil {
		t.Fatalf("TenantIDs: %v", err)
	}
	if len(ids) != 2 || ids[0] != "x" || ids[1] != "y" {
		t.Fatalf("TenantIDs = %v, want [x y]", ids)
	}
}

// TestPool_VerifyIssuer_TenantAnchorAccepted proves a tenant's own registered
// anchor, present anywhere in the chain above the leaf, admits that tenant --
// with no Key B provider and no cluster CA configured at all.
func TestPool_VerifyIssuer_TenantAnchorAccepted(t *testing.T) {
	reg := newRegistry(t)
	anchorPEM, anchorCert := genCAPEM(t)
	registerAnchor(t, reg, "tenant-a", anchorPEM)

	pool := New()
	pool.SetTrustAnchors(reg, nil)

	if err := pool.VerifyIssuer(context.Background(), "tenant-a", chainOf(anchorCert)); err != nil {
		t.Errorf("expected tenant-a's own anchor to be accepted, got: %v", err)
	}
}

// TestPool_VerifyIssuer_CrossTenantAnchorRefused is the security proof the
// brief calls for: tenant-a registers an anchor, tenant-b registers none,
// and a leaf CLAIMING tenant-b presents a chain through tenant-a's anchor.
// VerifyIssuer reads only the CLAIMED tenant's bundle (ChainContainsAnchor
// scopes by tenantID), so tenant-b's empty bundle never matches -- this
// would wrongly PASS if VerifyIssuer (or ChainContainsAnchor) consulted
// every tenant's anchors instead of only the claimed one's.
func TestPool_VerifyIssuer_CrossTenantAnchorRefused(t *testing.T) {
	reg := newRegistry(t)
	anchorPEM, anchorCertA := genCAPEM(t)
	registerAnchor(t, reg, "tenant-a", anchorPEM)

	pool := New()
	pool.SetTrustAnchors(reg, nil)

	err := pool.VerifyIssuer(context.Background(), "tenant-b", chainOf(anchorCertA))
	if err == nil {
		t.Fatal("expected tenant-a's anchor to be refused for a leaf claiming tenant-b")
	}
	var mismatch *IssuerNotTrustedError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected *IssuerNotTrustedError, got %T: %v", err, err)
	}
}

// TestPool_VerifyIssuer_AnchorDoesNotVouchForOtherTenant strengthens the
// cross-tenant proof: tenant-b has its OWN anchor registered too, so the
// refusal above cannot be explained away as "tenant-b simply has nothing
// registered" -- tenant-a's anchor specifically never vouches for tenant-b,
// while tenant-b's own anchor still does.
func TestPool_VerifyIssuer_AnchorDoesNotVouchForOtherTenant(t *testing.T) {
	reg := newRegistry(t)
	anchorPEMA, anchorCertA := genCAPEM(t)
	anchorPEMB, anchorCertB := genCAPEM(t)
	registerAnchor(t, reg, "tenant-a", anchorPEMA)
	registerAnchor(t, reg, "tenant-b", anchorPEMB)

	pool := New()
	pool.SetTrustAnchors(reg, nil)

	if err := pool.VerifyIssuer(context.Background(), "tenant-b", chainOf(anchorCertA)); err == nil {
		t.Fatal("expected tenant-a's anchor to be refused for tenant-b, even though tenant-b has its own anchor")
	}
	if err := pool.VerifyIssuer(context.Background(), "tenant-b", chainOf(anchorCertB)); err != nil {
		t.Errorf("expected tenant-b's own anchor to be accepted, got: %v", err)
	}
}

// TestPool_VerifyIssuer_AnchorSourceErrorWraps proves a Source failure
// surfaces as *IssuerNotTrustedError with the underlying error reachable via
// errors.Unwrap -- VerifyIssuer never panics or silently admits on a broken
// anchor read.
func TestPool_VerifyIssuer_AnchorSourceErrorWraps(t *testing.T) {
	want := errors.New("store unavailable")
	pool := New()
	pool.SetTrustAnchors(&erroringSource{err: want}, nil)

	_, anchorCert := genCAPEM(t)
	err := pool.VerifyIssuer(context.Background(), "tenant-a", chainOf(anchorCert))
	if err == nil {
		t.Fatal("expected an error when the anchor Source fails")
	}
	var mismatch *IssuerNotTrustedError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected *IssuerNotTrustedError, got %T: %v", err, err)
	}
	if !errors.Is(err, want) {
		t.Errorf("expected Unwrap to reach the underlying Source error, got: %v", err)
	}
}

// erroringSource is a trustanchor.Source whose CABundle always fails.
type erroringSource struct{ err error }

func (e *erroringSource) CABundle(context.Context, string) ([]byte, error) { return nil, e.err }

// TestPool_SetTrustAnchors_WidensPoolWithAnchor proves a registered anchor
// joins the widened pool VerifyConnection checks against, so a bring-your-own
// certificate chaining to it completes a handshake. ClientCAs itself is never
// widened (go-qrdb#591's review, B2: it would advertise the anchor's Subject
// in the clear), so the proof is VerifyPeerChain -- which resolves and
// verifies against the SAME widened pool verifyPeerConnection does -- not a
// direct x509.Verify against ClientCAs.
func TestPool_SetTrustAnchors_WidensPoolWithAnchor(t *testing.T) {
	cfg := baseConfig(t, x509.NewCertPool())
	pool := New()
	pool.Wire(cfg)
	pool.SetKeyBProvider(&fakeCertProvider{})

	reg := newRegistry(t)
	anchorPEM, anchorCert := genCAPEM(t)
	registerAnchor(t, reg, "tenant-a", anchorPEM)
	pool.SetTrustAnchors(reg, listerOf("tenant-a"))
	rebuild(t, pool)

	got := getConfigForClient(t, cfg)
	if got == cfg {
		t.Fatal("expected a widened config")
	}
	if _, err := pool.VerifyPeerChain(context.Background(), anchorCert, nil); err != nil {
		t.Errorf("expected the registered anchor in the widened pool: %v", err)
	}
}

// TestPool_SetTrustAnchors_NilSourceRemovesAnchorTrust proves a nil Source
// (SetTrustAnchors(nil, nil)) drops anchor trust from the widened pool
// immediately, before any rebuild -- SetTrustAnchors withdraws the
// snapshot itself.
func TestPool_SetTrustAnchors_NilSourceRemovesAnchorTrust(t *testing.T) {
	cfg := baseConfig(t, x509.NewCertPool())
	pool := New()
	pool.Wire(cfg)
	pool.SetKeyBProvider(&fakeCertProvider{})

	reg := newRegistry(t)
	anchorPEM, anchorCert := genCAPEM(t)
	registerAnchor(t, reg, "tenant-a", anchorPEM)
	pool.SetTrustAnchors(reg, listerOf("tenant-a"))
	rebuild(t, pool)

	if _, err := pool.VerifyPeerChain(context.Background(), anchorCert, nil); err != nil {
		t.Fatalf("expected the anchor to be trusted before removal: %v", err)
	}

	pool.SetTrustAnchors(nil, nil)
	if _, err := pool.VerifyPeerChain(context.Background(), anchorCert, nil); err == nil {
		t.Error("expected the anchor to no longer be trusted after SetTrustAnchors(nil, nil)")
	}
}

// TestPool_RemoveAnchor_InvalidateDropsFromPool proves withdrawing a
// registered anchor and calling Invalidate drops it from the widened pool
// once the rebuild Invalidate schedules lands, and from VerifyIssuer's
// acceptance on the very next call (VerifyIssuer reads the store directly)
// -- cluster CA / Key B trust is untouched.
func TestPool_RemoveAnchor_InvalidateDropsFromPool(t *testing.T) {
	_, keyACert := genCAPEM(t)
	cfg := baseConfig(t, x509.NewCertPool())
	pool := New()
	pool.Wire(cfg)
	pool.SetClusterCA(&fakeClusterCA{cert: keyACert})
	keyBPEM, keyBCert := genCAPEM(t)
	pool.SetKeyBProvider(&fakeCertProvider{pems: [][]byte{keyBPEM}})

	reg := newRegistry(t)
	anchorPEM, anchorCert := genCAPEM(t)
	registerAnchor(t, reg, "tenant-a", anchorPEM)
	pool.SetTrustAnchors(reg, listerOf("tenant-a"))
	rebuild(t, pool)

	got := getConfigForClient(t, cfg)
	if got == cfg {
		t.Fatal("expected a widened config")
	}
	if _, err := pool.VerifyPeerChain(context.Background(), anchorCert, nil); err != nil {
		t.Fatalf("expected the anchor to be trusted before removal: %v", err)
	}
	if err := pool.VerifyIssuer(context.Background(), "tenant-a", chainOf(anchorCert)); err != nil {
		t.Fatalf("expected VerifyIssuer to accept the anchor before removal: %v", err)
	}

	if err := reg.Remove(context.Background(), "tenant-a", trustanchor.Fingerprint(anchorCert)); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	pool.Invalidate()
	assertKickPending(t, pool)
	rebuild(t, pool)

	got = getConfigForClient(t, cfg)
	if got == cfg {
		t.Fatal("expected a config after invalidation (cluster CA and Key B still trusted)")
	}
	if _, err := pool.VerifyPeerChain(context.Background(), anchorCert, nil); err == nil {
		t.Error("expected the removed anchor to no longer verify against the pool")
	}
	if _, err := pool.VerifyPeerChain(context.Background(), keyBCert, nil); err != nil {
		t.Errorf("expected Key B to remain trusted after the anchor was removed: %v", err)
	}
	if _, err := pool.VerifyPeerChain(context.Background(), keyACert, nil); err != nil {
		t.Errorf("expected the cluster CA to remain trusted after the anchor was removed: %v", err)
	}

	if err := pool.VerifyIssuer(context.Background(), "tenant-a", chainOf(anchorCert)); err == nil {
		t.Error("expected VerifyIssuer to refuse the removed anchor")
	}
}

// TestPool_AnchorBundles_TenantListerErrorOmitsAnchorsOnly proves a failing
// TenantLister omits every anchor from the widened pool while leaving the
// cluster CA and Key B trust completely unaffected.
func TestPool_AnchorBundles_TenantListerErrorOmitsAnchorsOnly(t *testing.T) {
	_, keyACert := genCAPEM(t)
	cfg := baseConfig(t, x509.NewCertPool())
	pool := New()
	pool.Wire(cfg)
	pool.SetClusterCA(&fakeClusterCA{cert: keyACert})
	keyBPEM, keyBCert := genCAPEM(t)
	pool.SetKeyBProvider(&fakeCertProvider{pems: [][]byte{keyBPEM}})

	reg := newRegistry(t)
	anchorPEM, anchorCert := genCAPEM(t)
	registerAnchor(t, reg, "tenant-a", anchorPEM)
	failingLister := TenantListerFunc(func(context.Context) ([]string, error) {
		return nil, errors.New("tenant service unavailable")
	})
	pool.SetTrustAnchors(reg, failingLister)
	rebuild(t, pool)

	got := getConfigForClient(t, cfg)
	if got == cfg {
		t.Fatal("expected a config despite the TenantLister failing")
	}
	if _, err := pool.VerifyPeerChain(context.Background(), keyACert, nil); err != nil {
		t.Errorf("expected the cluster CA to remain trusted: %v", err)
	}
	if _, err := pool.VerifyPeerChain(context.Background(), keyBCert, nil); err != nil {
		t.Errorf("expected Key B to remain trusted: %v", err)
	}
	if _, err := pool.VerifyPeerChain(context.Background(), anchorCert, nil); err == nil {
		t.Error("expected the anchor to be omitted while the TenantLister fails")
	}
}

// partialErrSource wraps a real trustanchor.Source and fails CABundle for
// exactly one tenant, so a test can prove a per-tenant read failure omits
// only that tenant's anchors.
type partialErrSource struct {
	real       trustanchor.Source
	failTenant string
}

func (s *partialErrSource) CABundle(ctx context.Context, tenantID string) ([]byte, error) {
	if tenantID == s.failTenant {
		return nil, errors.New("read failed for this tenant")
	}
	return s.real.CABundle(ctx, tenantID)
}

// TestPool_AnchorBundles_PerTenantSourceErrorOmitsThatTenantOnly proves that
// when the Source fails to read ONE tenant's anchors, only that tenant's
// bundle is omitted -- every other tenant's registered anchor still widens
// the pool.
func TestPool_AnchorBundles_PerTenantSourceErrorOmitsThatTenantOnly(t *testing.T) {
	cfg := baseConfig(t, x509.NewCertPool())
	pool := New()
	pool.Wire(cfg)
	pool.SetKeyBProvider(&fakeCertProvider{})

	reg := newRegistry(t)
	anchorPEM, anchorCert := genCAPEM(t)
	registerAnchor(t, reg, "tenant-a", anchorPEM)
	src := &partialErrSource{real: reg, failTenant: "tenant-b"}
	pool.SetTrustAnchors(src, listerOf("tenant-a", "tenant-b"))
	rebuild(t, pool)

	got := getConfigForClient(t, cfg)
	if got == cfg {
		t.Fatal("expected a widened config despite tenant-b's anchor read failing")
	}
	if _, err := pool.VerifyPeerChain(context.Background(), anchorCert, nil); err != nil {
		t.Errorf("expected tenant-a's anchor to still widen the pool: %v", err)
	}
}

// TestPool_Run_RebuildsOnInvalidate proves the background loop is what
// keeps the snapshot current: with Run started, an anchor registered after
// the first build is trusted once Invalidate kicks it, with no test-driven
// Refresh call, and Run returns when its context is cancelled.
func TestPool_Run_RebuildsOnInvalidate(t *testing.T) {
	cfg := baseConfig(t, x509.NewCertPool())
	pool := New()
	pool.Wire(cfg)
	pool.SetKeyBProvider(&fakeCertProvider{})
	reg := newRegistry(t)
	pool.SetTrustAnchors(reg, listerOf("tenant-a"))

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		pool.Run(ctx)
	}()

	anchorPEM, anchorCert := genCAPEM(t)
	registerAnchor(t, reg, "tenant-a", anchorPEM)
	pool.Invalidate()

	deadline := time.Now().Add(poolCacheTTL / 2)
	for {
		if _, err := pool.VerifyPeerChain(context.Background(), anchorCert, nil); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("Run did not rebuild on Invalidate before the next TTL tick")
		}
		time.Sleep(5 * time.Millisecond)
	}

	cancel()
	select {
	case <-runDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}

// TestPool_StaleSnapshotIsWithdrawn proves a snapshot older than
// snapshotMaxAge is not served: if the background loop has stopped
// rebuilding, the handshake reverts to the static baseline and
// VerifyPeerChain to the static fallback, rather than trusting Key B from
// a read that is no longer being repeated.
func TestPool_StaleSnapshotIsWithdrawn(t *testing.T) {
	_, keyACert, keyAKey := genCAWithKey(t, "key-a")
	keyBPEM, keyBCert, keyBKey := genCAWithKey(t, "tenant-key-b-ca")
	staticPool := x509.NewCertPool()
	staticPool.AddCert(keyACert)
	cfg := baseConfig(t, staticPool)

	pool := New()
	pool.Wire(cfg)
	pool.SetKeyBProvider(&fakeCertProvider{pems: [][]byte{keyBPEM}})
	rebuild(t, pool)

	fresh := pool.snapshot.Load()
	pool.snapshot.Store(&trustSnapshot{
		built:    fresh.built.Add(-snapshotMaxAge - time.Second),
		base:     fresh.base,
		extra:    fresh.extra,
		widened:  fresh.widened,
		keyBOnly: fresh.keyBOnly,
	})

	if got := getConfigForClient(t, cfg); got != cfg {
		t.Error("expected the handshake to revert to the static baseline on a stale snapshot")
	}
	keyBLeaf := parseTLSLeaf(t, genLeafTLSCert(t, keyBCert, keyBKey))
	if _, err := pool.VerifyPeerChain(context.Background(), keyBLeaf, nil); err == nil {
		t.Error("expected a Key B leaf to be refused on a stale snapshot")
	}
	keyALeaf := parseTLSLeaf(t, genLeafTLSCert(t, keyACert, keyAKey))
	if _, err := pool.VerifyPeerChain(context.Background(), keyALeaf, nil); err != nil {
		t.Errorf("expected a static-anchor leaf to still verify on a stale snapshot: %v", err)
	}
}

// TestPool_WidenedFor_DerivesForAnotherBase proves a listener whose own
// ClientCAs differs from the one Wire recorded (a GetConfigForClient hook
// returning its own pool) is widened from ITS base, and that the derived
// pool is cached for the next handshake on that base.
func TestPool_WidenedFor_DerivesForAnotherBase(t *testing.T) {
	_, keyACert := genCAPEM(t)
	_, otherCert := genCAPEM(t)
	keyBPEM, keyBCert := genCAPEM(t)
	staticPool := x509.NewCertPool()
	staticPool.AddCert(keyACert)
	cfg := baseConfig(t, staticPool)

	pool := New()
	pool.Wire(cfg)
	pool.SetKeyBProvider(&fakeCertProvider{pems: [][]byte{keyBPEM}})
	rebuild(t, pool)

	other := x509.NewCertPool()
	other.AddCert(otherCert)
	first := pool.widenedFor(other)
	if first == nil {
		t.Fatal("expected a widened pool for another base")
	}
	if first == pool.widenedFor(cfg.ClientCAs) {
		t.Fatal("expected a pool derived from the other base, not Wire's own")
	}
	if second := pool.widenedFor(other); second != first {
		t.Error("expected the derived pool to be cached for the same base")
	}
	for name, c := range map[string]*x509.Certificate{"other base": otherCert, "key B": keyBCert} {
		if _, err := c.Verify(x509.VerifyOptions{Roots: first, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
			t.Errorf("%s: expected the derived pool to trust it: %v", name, err)
		}
	}
	if pool.widenedFor(nil) == nil {
		t.Error("expected a widened pool for a nil base")
	}
}

// TestParsePEMCertificates_SkipsWhatIsNotACertificate proves the anchor
// bundle parser keeps only CERTIFICATE blocks that parse, so one malformed
// or foreign block in a tenant's bundle never drops that tenant's other
// anchors.
func TestParsePEMCertificates_SkipsWhatIsNotACertificate(t *testing.T) {
	goodPEM, goodCert := genCAPEM(t)
	bundle := append([]byte{}, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("not a cert")})...)
	bundle = append(bundle, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("bad der")})...)
	bundle = append(bundle, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Headers: map[string]string{"Proc-Type": "4,ENCRYPTED"}, Bytes: goodCert.Raw})...)
	bundle = append(bundle, goodPEM...)

	got := parsePEMCertificates(bundle)
	if len(got) != 1 || !got[0].Equal(goodCert) {
		t.Fatalf("expected exactly the one well-formed certificate, got %d", len(got))
	}
	if got := parsePEMCertificates(nil); len(got) != 0 {
		t.Errorf("expected nothing from an empty bundle, got %d", len(got))
	}
}
