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

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/clienttrust"
	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
)

// These tests prove TLSBundle wires pkg/clienttrust correctly -- the actual
// trust-pool widening/fallback behavior is pkg/clienttrust's own test
// suite's job (pkg/clienttrust/pool_test.go). qrdbd, sdk/go/node, and the
// qrdb_embedded composer all share that ONE implementation; this file only
// proves qrdbd's wiring of it.

// fakeKeyBCertProvider implements cert.CertProvider (every method a no-op)
// plus CACertPEMs, so it satisfies clienttrust.CertSource. It never
// persists or generates real key material: it only hands back whatever
// pems/err a test configured.
type fakeKeyBCertProvider struct {
	pems [][]byte
	err  error
}

var _ cert.CertProvider = (*fakeKeyBCertProvider)(nil)
var _ clienttrust.CertSource = (*fakeKeyBCertProvider)(nil)

func (f *fakeKeyBCertProvider) IssueClientCert(context.Context, *cert.IssueRequest) (*cert.IssuedCert, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeKeyBCertProvider) ExportPKCS12(context.Context, []byte, []byte, string) ([]byte, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeKeyBCertProvider) RevokeCert(context.Context, string) (*cert.RevokeResult, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeKeyBCertProvider) IsRevoked(context.Context, string) (bool, error) { return false, nil }
func (f *fakeKeyBCertProvider) TenantForSerial(context.Context, string) (string, bool, error) {
	return "", false, nil
}
func (f *fakeKeyBCertProvider) ListCerts(context.Context, string) ([]*cert.CertRecord, error) {
	return nil, nil
}
func (f *fakeKeyBCertProvider) CACertPEM() []byte { return nil }

func (f *fakeKeyBCertProvider) CACertPEMs(context.Context) ([][]byte, error) {
	return f.pems, f.err
}

func (f *fakeKeyBCertProvider) IsTenantIssuer(context.Context, string, *x509.Certificate) (bool, error) {
	return false, nil
}

// fakeClusterCA implements clienttrust.ClusterCA with a fixed answer, so
// these tests can install a cluster CA without standing up a real
// ceremony.CeremonyCA.
type fakeClusterCA struct {
	cert *x509.Certificate
	err  error
}

func (f *fakeClusterCA) CACertificate(context.Context) (*x509.Certificate, error) {
	return f.cert, f.err
}

var _ clienttrust.ClusterCA = (*fakeClusterCA)(nil)

func testBundle(t *testing.T) *TLSBundle {
	t.Helper()
	bundle, err := generateCATLS()
	if err != nil {
		t.Fatalf("generateCATLS: %v", err)
	}
	// Matches the production sequence in runStart: ClientCAs carries Key A
	// before wireClientTrustPool installs the hook that reads it fresh on
	// every connection.
	bundle.TLSConfig.ClientCAs = bundle.ClientCACertPool
	bundle.wireClientTrustPool()
	return bundle
}

// bundleClusterCA parses bundle's own self-signed CA certificate and wraps
// it as a clienttrust.ClusterCA, standing in for the cluster CA (Key A) a
// production node resolves via ceremony.CeremonyCA.
func bundleClusterCA(t *testing.T, bundle *TLSBundle) *fakeClusterCA {
	t.Helper()
	block, _ := pem.Decode(bundle.CACertPEM)
	if block == nil {
		t.Fatal("bundle.CACertPEM did not decode as PEM")
	}
	caCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate (cluster CA): %v", err)
	}
	return &fakeClusterCA{cert: caCert}
}

func genSecondCAPEM(t *testing.T) []byte {
	t.Helper()
	caCertPEM, _, _, _, err := clustertls.GenerateCA(clustertls.CAValidity)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	return caCertPEM
}

func getConfigForClient(t *testing.T, bundle *TLSBundle) *tls.Config {
	t.Helper()
	if bundle.TLSConfig.GetConfigForClient == nil {
		t.Fatal("GetConfigForClient was not installed")
	}
	cfg, err := bundle.TLSConfig.GetConfigForClient(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetConfigForClient: %v", err)
	}
	return cfg
}

// assertTrustedByConnection proves cert would be accepted as a presented
// client certificate on cfg's VerifyConnection hook -- the widened pool's
// real verification surface since pkg/clienttrust.Pool.configForClient
// (go-qrdb#591's review, B2): cfg.ClientCAs is deliberately the static
// baseline only, never the widened pool, so asserting trust via
// x509.Verify(Roots: cfg.ClientCAs) would no longer test what this bundle
// actually enforces. A self-signed CA certificate presented as its own
// leaf verifies trivially against a pool containing it.
func assertTrustedByConnection(t *testing.T, cfg *tls.Config, cert *x509.Certificate) {
	t.Helper()
	if cfg.VerifyConnection == nil {
		t.Fatal("expected VerifyConnection to be installed on the widened config")
	}
	if err := cfg.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}); err != nil {
		t.Errorf("VerifyConnection rejected a certificate expected to be trusted: %v", err)
	}
}

// TestTLSBundle_TrustPoolIsTheWiredInstance proves bundle.TrustPool()
// returns the SAME *clienttrust.Pool that SetKeyBProvider/SetClusterCA
// populate and Wire installed on TLSConfig -- the object a composer passes
// as auth.IssuerVerifier to bootstrapAuthEnforcement. A detached copy here
// would let Key B be trusted at the TLS layer while the issuer-tenant
// binding checked a pool that never saw the same state (go-qrdb#560
// review, B2-2).
func TestTLSBundle_TrustPoolIsTheWiredInstance(t *testing.T) {
	bundle := testBundle(t)
	keyB := genSecondCAPEM(t)
	bundle.SetKeyBProvider(&fakeKeyBCertProvider{pems: [][]byte{keyB}})
	clusterCA := bundleClusterCA(t, bundle)
	bundle.SetClusterCA(clusterCA)

	pool := bundle.TrustPool()
	if pool == nil {
		t.Fatal("TrustPool() returned nil after wireClientTrustPool")
	}
	if !pool.IsClusterCA(t.Context(), clusterCA.cert) {
		t.Fatal("TrustPool() is not the instance SetClusterCA populated")
	}
}

func TestWireClientTrustPool_InstallsHook(t *testing.T) {
	bundle := testBundle(t)
	// Never the literal bundle.TLSConfig pointer: wireClientTrustPool's own
	// hook (ahead of Pool's) always returns a clone carrying the CURRENT
	// atomic ClientAuth/ClientCAs override, by design (go-qrdb#591's
	// review, item 3) -- so "unmodified" is asserted on CONTENT here, not
	// identity.
	cfg := getConfigForClient(t, bundle)
	if cfg == nil {
		t.Fatal("expected a non-nil config before SetKeyBProvider")
	}
	if cfg.ClientAuth != bundle.TLSConfig.ClientAuth {
		t.Errorf("ClientAuth = %v, want %v (the unmodified baseline)", cfg.ClientAuth, bundle.TLSConfig.ClientAuth)
	}
	if cfg.ClientCAs != bundle.TLSConfig.ClientCAs {
		t.Error("ClientCAs is not the unmodified baseline pool")
	}
}

func TestWireClientTrustPool_SetKeyBProviderWidensPool(t *testing.T) {
	bundle := testBundle(t)
	keyB := genSecondCAPEM(t)
	bundle.SetKeyBProvider(&fakeKeyBCertProvider{pems: [][]byte{keyB}})
	// Key B is trusted only once BOTH SetKeyBProvider and SetClusterCA have
	// been called (go-qrdb#560 review, B2-2): the pool and the
	// issuer-tenant binding are the same object.
	bundle.SetClusterCA(bundleClusterCA(t, bundle))

	cfg := getConfigForClient(t, bundle)
	if cfg == nil {
		t.Fatal("expected a widened config, got the static fallback (nil)")
	}

	block, _ := pem.Decode(keyB)
	if block == nil {
		t.Fatal("genSecondCAPEM did not produce valid PEM")
	}
	keyBCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	assertTrustedByConnection(t, cfg, keyBCert)

	block, _ = pem.Decode(bundle.CACertPEM)
	if block == nil {
		t.Fatal("bundle.CACertPEM did not decode as PEM")
	}
	keyACert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate (Key A): %v", err)
	}
	assertTrustedByConnection(t, cfg, keyACert)
}

func TestWireClientTrustPool_ProviderErrorFallsBackToStatic(t *testing.T) {
	bundle := testBundle(t)
	bundle.SetKeyBProvider(&fakeKeyBCertProvider{err: errors.New("barrier is sealed")})
	// Cluster CA IS configured here, specifically so this test exercises the
	// CACertPEMs failure path rather than being masked by the separate
	// "no cluster CA configured" fallback.
	bundle.SetClusterCA(bundleClusterCA(t, bundle))

	cfg := getConfigForClient(t, bundle)
	if cfg == nil {
		t.Fatal("expected a non-nil config when CACertPEMs fails (simulating sealed)")
	}
	if cfg.ClientAuth != bundle.TLSConfig.ClientAuth {
		t.Errorf("ClientAuth = %v, want %v (the unmodified baseline)", cfg.ClientAuth, bundle.TLSConfig.ClientAuth)
	}
	if cfg.ClientCAs != bundle.TLSConfig.ClientCAs {
		t.Error("ClientCAs is not the unmodified baseline pool")
	}
}

// TestWireClientTrustPool_NoClusterCA_KeyBStillWidensOnPerTenantBindingAlone
// pins the corrected B2-2 semantics at the qrdbd wiring layer:
// SetKeyBProvider alone, with SetClusterCA NEVER called (a deployment shape
// with no cluster-CA concept at all), still widens the pool with Key B --
// the issuer-tenant binding's per-tenant check (VerifyIssuer's
// IsTenantIssuer branch) needs no cluster CA to operate, only the
// cluster-CA EXEMPTION does. This is different from a cluster CA resolver
// that WAS installed and is failing right now (sealed, transient): see
// TestWireClientTrustPool_ProviderErrorFallsBackToStatic, which fails
// closed because an installed-but-currently-broken resolver is a signal
// this function must not paper over.
func TestWireClientTrustPool_NoClusterCA_KeyBStillWidensOnPerTenantBindingAlone(t *testing.T) {
	bundle := testBundle(t)
	keyB := genSecondCAPEM(t)
	bundle.SetKeyBProvider(&fakeKeyBCertProvider{pems: [][]byte{keyB}})

	cfg := getConfigForClient(t, bundle)
	if cfg == nil {
		t.Fatal("expected a widened config even with no cluster CA configured")
	}
	block, _ := pem.Decode(keyB)
	if block == nil {
		t.Fatal("genSecondCAPEM did not produce valid PEM")
	}
	keyBCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	assertTrustedByConnection(t, cfg, keyBCert)
}
