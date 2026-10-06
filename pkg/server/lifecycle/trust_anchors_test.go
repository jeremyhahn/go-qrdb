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

package lifecycle

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
	"github.com/jeremyhahn/go-qrdb/pkg/clienttrust"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
)

// emptyKeyBProvider is a cert.CertProvider implementing
// clienttrust.CertSource with no Key B certificates of its own, so a test
// can exercise the widened-pool path (which requires a Key B provider to be
// installed at all) without needing a real per-tenant CA.
type emptyKeyBProvider struct{ cert.CertProvider }

func (emptyKeyBProvider) CACertPEMs(context.Context) ([][]byte, error) { return nil, nil }
func (emptyKeyBProvider) IsTenantIssuer(context.Context, string, *x509.Certificate) (bool, error) {
	return false, nil
}

// genSelfSignedCA mints a throwaway self-signed CA certificate, valid as a
// trust anchor (trustanchor.ParseAnchors requires IsCA plus a validity
// window), and returns its PEM encoding alongside the parsed certificate.
func genSelfSignedCA(t *testing.T) ([]byte, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-anchor"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return certPEM, c
}

// listTenantsStub satisfies tenant.TenantServicer and nothing else, reporting
// a fixed ListTenants answer. The embedded nil interface panics if any
// un-overridden method is called, so trustAnchorTenants calling anything
// beyond ListTenants would be loud rather than silently passing.
type listTenantsStub struct {
	tenant.TenantServicer
	infos []tenant.TenantInfo
	err   error
}

func (s listTenantsStub) ListTenants(context.Context) ([]tenant.TenantInfo, error) {
	return s.infos, s.err
}

// ---------------------------------------------------------------------------
// trustAnchorTenants: the system tenant plus every listed tenant.
// ---------------------------------------------------------------------------

// TestTrustAnchorTenants_NilTenantService_SystemOnly proves a Node with no
// Tenant service wired reports only the system tenant.
func TestTrustAnchorTenants_NilTenantService_SystemOnly(t *testing.T) {
	n := &Node{}
	ids, err := n.trustAnchorTenants(context.Background())
	if err != nil {
		t.Fatalf("trustAnchorTenants: %v", err)
	}
	if len(ids) != 1 || ids[0] != tenantid.System {
		t.Fatalf("ids = %v, want [%s]", ids, tenantid.System)
	}
}

// TestTrustAnchorTenants_ListsSystemPlusEveryTenant proves the system
// tenant is always included alongside every tenant ListTenants reports.
func TestTrustAnchorTenants_ListsSystemPlusEveryTenant(t *testing.T) {
	n := &Node{Tenant: listTenantsStub{infos: []tenant.TenantInfo{
		{ID: "acme"},
		{ID: "globex"},
	}}}
	ids, err := n.trustAnchorTenants(context.Background())
	if err != nil {
		t.Fatalf("trustAnchorTenants: %v", err)
	}
	want := map[string]bool{tenantid.System: true, "acme": true, "globex": true}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want exactly %v", ids, want)
	}
	for _, id := range ids {
		if !want[id] {
			t.Errorf("unexpected tenant id %q", id)
		}
	}
}

// TestTrustAnchorTenants_SystemTenantNeverDuplicated proves that when
// ListTenants itself reports the system tenant (it is a real, listed
// tenant), trustAnchorTenants does not duplicate it in the returned slice.
func TestTrustAnchorTenants_SystemTenantNeverDuplicated(t *testing.T) {
	n := &Node{Tenant: listTenantsStub{infos: []tenant.TenantInfo{
		{ID: tenantid.System},
		{ID: "acme"},
	}}}
	ids, err := n.trustAnchorTenants(context.Background())
	if err != nil {
		t.Fatalf("trustAnchorTenants: %v", err)
	}
	count := 0
	for _, id := range ids {
		if id == tenantid.System {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("system tenant appeared %d times in %v, want exactly 1", count, ids)
	}
	if len(ids) != 2 {
		t.Fatalf("ids = %v, want exactly 2 entries (system, acme)", ids)
	}
}

// TestTrustAnchorTenants_ListTenantsErrorPropagates proves a ListTenants
// failure is returned rather than silently reporting the system tenant
// alone -- a trust pool must know enumeration failed, not treat it as "no
// tenants".
func TestTrustAnchorTenants_ListTenantsErrorPropagates(t *testing.T) {
	want := errors.New("store unavailable")
	n := &Node{Tenant: listTenantsStub{err: want}}
	_, err := n.trustAnchorTenants(context.Background())
	if !errors.Is(err, want) {
		t.Fatalf("trustAnchorTenants error = %v, want %v", err, want)
	}
}

// ---------------------------------------------------------------------------
// WireTrustAnchors: installs the pool and subscribes cache invalidation.
// ---------------------------------------------------------------------------

// TestWireTrustAnchors_NilPool_NoOp proves a nil pool is tolerated.
func TestWireTrustAnchors_NilPool_NoOp(t *testing.T) {
	n := &Node{TrustAnchors: trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore())}
	n.WireTrustAnchors(nil) // must not panic
}

// TestWireTrustAnchors_NilTrustAnchors_NoOp proves a Node with no
// certificate provider (no anchors at all) is a no-op: it must not panic
// and must leave the pool trusting nothing it was not already told to
// trust.
func TestWireTrustAnchors_NilTrustAnchors_NoOp(t *testing.T) {
	n := &Node{} // TrustAnchors deliberately nil
	pool := clienttrust.New()
	n.WireTrustAnchors(pool) // must not panic

	_, anchorCert := genSelfSignedCA(t)
	if err := pool.VerifyIssuer(context.Background(), "tenant-a", []*x509.Certificate{{Raw: []byte("leaf")}, anchorCert}); err == nil {
		t.Error("expected no anchor to be trusted when WireTrustAnchors was never given a registry")
	}
}

// TestWireTrustAnchors_WiresPoolAndSubscribesInvalidation is the end-to-end
// proof: the installed registry's anchors are trusted through the SAME
// Pool, and a change announced on ClientTrustChanges invalidates the pool's
// cache so the very next handshake sees it -- not merely after the cache's
// TTL lapses.
func TestWireTrustAnchors_WiresPoolAndSubscribesInvalidation(t *testing.T) {
	ctx := context.Background()
	reg := trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore())
	anchorPEM, anchorCert := genSelfSignedCA(t)
	if _, err := reg.Add(ctx, "tenant-a", anchorPEM); err != nil {
		t.Fatalf("Add: %v", err)
	}

	n := &Node{
		TrustAnchors:       reg,
		ClientTrustChanges: &trustanchor.Changes{},
		Tenant:             listTenantsStub{infos: []tenant.TenantInfo{{ID: "tenant-a"}}},
	}

	cfg := &tls.Config{ClientCAs: x509.NewCertPool(), MinVersion: tls.VersionTLS13}
	pool := clienttrust.New()
	pool.Wire(cfg)
	pool.SetKeyBProvider(emptyKeyBProvider{})
	n.WireTrustAnchors(pool)

	// The issuer-tenant binding is wired through the SAME pool regardless of
	// the TLS-layer widening cache below.
	if err := pool.VerifyIssuer(ctx, "tenant-a", []*x509.Certificate{{Raw: []byte("leaf")}, anchorCert}); err != nil {
		t.Fatalf("expected tenant-a's registered anchor to be trusted, got: %v", err)
	}

	// Populate the widened-pool cache without the second anchor yet.
	cfg.GetConfigForClient(&tls.ClientHelloInfo{})

	secondPEM, secondCert := genSelfSignedCA(t)
	if _, err := reg.Add(ctx, "tenant-a", secondPEM); err != nil {
		t.Fatalf("Add (second): %v", err)
	}

	// Without invalidation this would still read the stale cached pool
	// (poolCacheTTL has not lapsed): the subscription is what makes the new
	// anchor visible on the very next handshake.
	n.ClientTrustChanges.Notify()

	got, err := cfg.GetConfigForClient(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetConfigForClient: %v", err)
	}
	if got == nil {
		t.Fatal("expected a widened config")
	}
	if _, verr := pool.VerifyPeerChain(ctx, secondCert, nil); verr != nil {
		t.Errorf("expected the anchor added after the cache was populated to be trusted post-invalidation: %v", verr)
	}
}
