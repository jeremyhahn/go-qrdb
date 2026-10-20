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
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// homedTenant is the strict tenant the routing tests home; acme is a tenant
// whose records stay in the system store.
const homedTenant = "vault"

// errNotHomed is what homeRoutingResolver answers when asked to resolve a
// tenant it does not home. The real resolver would hand back the tenant's
// confined view of the system store; the provider never asks for one, and
// the double makes asking a failure rather than an answer.
var errNotHomed = errors.New("resolve asked for a tenant that is not homed")

// homeRoutingResolver is a tenant.StoreResolver and tenant.HomeLocator that
// homes the tenants in homes, answering InHome by the same set Resolve
// routes by, as defaultStoreResolver does.
type homeRoutingResolver struct {
	mu         sync.Mutex
	homes      map[string]kvstore.KVStore
	inHomeErr  error
	resolveErr error
	resolves   int
	inHomes    int
}

var (
	_ tenant.StoreResolver = (*homeRoutingResolver)(nil)
	_ tenant.HomeLocator   = (*homeRoutingResolver)(nil)
)

func newHomeRoutingResolver() (*homeRoutingResolver, *mockKVStore) {
	home := newMockKVStore()
	return &homeRoutingResolver{homes: map[string]kvstore.KVStore{homedTenant: home}}, home
}

func (r *homeRoutingResolver) InHome(_ context.Context, tenantID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inHomes++
	if r.inHomeErr != nil {
		return false, r.inHomeErr
	}
	_, ok := r.homes[tenantID]
	return ok, nil
}

func (r *homeRoutingResolver) Resolve(_ context.Context, tenantID string) (kvstore.KVStore, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resolves++
	store, ok := r.homes[tenantID]
	if !ok {
		return nil, errNotHomed
	}
	if r.resolveErr != nil {
		return nil, r.resolveErr
	}
	return store, nil
}

func (r *homeRoutingResolver) set(fn func(r *homeRoutingResolver)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fn(r)
}

func (r *homeRoutingResolver) resolveCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.resolves
}

func (r *homeRoutingResolver) inHomeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inHomes
}

// createHome registers tenantID as an isolated tenant with a fresh home
// store, the way tenant creation makes the real resolver home it.
func (r *homeRoutingResolver) createHome(tenantID string) *mockKVStore {
	home := newMockKVStore()
	r.set(func(r *homeRoutingResolver) { r.homes[tenantID] = home })
	return home
}

// manualClock is the clock the provider ages its perTenant cache by,
// advanced by the test rather than by waiting.
type manualClock struct{ t time.Time }

func (c *manualClock) now() time.Time { return c.t }

func (c *manualClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// useManualClock makes p age its cache by a clock the test advances.
func useManualClock(p *DefaultCertProvider) *manualClock {
	c := &manualClock{t: time.Now()}
	p.now = c.now
	return c
}

// sealHome makes the homed tenant's home store unresolvable the way the real
// resolver reports a sealed or unattached home.
func (r *homeRoutingResolver) sealHome() {
	r.set(func(r *homeRoutingResolver) {
		r.resolveErr = &tenant.TenantHomeUnavailableError{TenantID: homedTenant, Err: tenant.ErrTenantHomeNotAttached}
	})
}

// newHomeRoutedProvider returns a provider over a fresh system store with a
// resolver homing homedTenant attached.
func newHomeRoutedProvider(t *testing.T, opts ...ProviderOption) (*DefaultCertProvider, *mockKVStore, *mockKVStore, *homeRoutingResolver) {
	t.Helper()
	p, sys, _ := newTestProvider(opts...)
	r, home := newHomeRoutingResolver()
	p.SetStoreResolver(r)
	return p, sys, home, r
}

// issueFor issues a client certificate for tenantID and returns its serial
// and parsed certificate.
func issueFor(t *testing.T, p *DefaultCertProvider, tenantID string) (string, *x509.Certificate) {
	t.Helper()
	bundle, err := generateForTest(context.Background(), p, &testRequest{Username: "alice", TenantID: tenantID, TTL: time.Hour})
	if err != nil {
		t.Fatalf("issue for %s: %v", tenantID, err)
	}
	block, _ := pem.Decode(bundle.CertPEM)
	if block == nil {
		t.Fatal("issued CertPEM is not PEM")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse issued certificate: %v", err)
	}
	return bundle.Serial, leaf
}

// mustGet returns key's value in kv, or nil.
func mustGet(t *testing.T, kv kvstore.KVStore, key string) []byte {
	t.Helper()
	v, err := kv.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	return v
}

// publishedCAFor parses the CA certificate published for tenantID in sys.
func publishedCAFor(t *testing.T, sys kvstore.KVStore, tenantID string) *x509.Certificate {
	t.Helper()
	der := mustGet(t, sys, publishedCAKey(tenantID))
	if der == nil {
		t.Fatalf("no CA published for %s", tenantID)
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse published CA: %v", err)
	}
	return ca
}

// foreignLeaf returns a client certificate naming tenantID that a CA other
// than the tenant's signed, standing in for a cluster-CA holder certificate.
func foreignLeaf(t *testing.T, tenantID string) *x509.Certificate {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "other-ca"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// The SPIFFE ID SAN names the tenant (auth.TenantFromClientCert); a
	// leaf for no tenant carries none.
	var uris []*url.URL
	if tenantID != "" {
		if uris, err = ClientSPIFFEURIs(sanTestDomain, tenantID, "holder"); err != nil {
			t.Fatal(err)
		}
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(0xbeef),
		Subject:      pkix.Name{CommonName: "holder", OrganizationalUnit: []string{tenantID}},
		URIs:         uris,
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		t.Fatal(err)
	}
	return leaf
}

// failPutKVStore fails every Put of a key with the given prefix.
type failPutKVStore struct {
	*mockKVStore
	prefix string
}

func (f *failPutKVStore) Put(ctx context.Context, key string, value []byte) error {
	if len(key) >= len(f.prefix) && key[:len(f.prefix)] == f.prefix {
		return errors.New("put refused")
	}
	return f.mockKVStore.Put(ctx, key, value)
}

// plainKVStore is a store with no guarded writes.
type plainKVStore struct{ kvstore.KVStore }

// valueKVStore is a store of a non-pointer dynamic type.
type valueKVStore struct{ *mockKVStore }

func TestHomeRouting_IssueKeepsCAAndRecordInHome(t *testing.T) {
	p, sys, home, _ := newHomeRoutedProvider(t)

	serial, leaf := issueFor(t, p, homedTenant)

	for _, key := range []string{
		prefixCertCA + homedTenant + materialRecordSuffix,
		prefixCertCA + homedTenant + certRecordSuffix,
		prefixCerts + homedTenant + "/" + serial,
	} {
		if mustGet(t, sys, key) != nil {
			t.Errorf("system store holds %s", key)
		}
		if mustGet(t, home, key) == nil {
			t.Errorf("home store lacks %s", key)
		}
	}
	if err := leaf.CheckSignatureFrom(publishedCAFor(t, sys, homedTenant)); err != nil {
		t.Fatalf("issued certificate does not verify against the published CA: %v", err)
	}
}

func TestHomeRouting_TenantNotHomedStaysInSystemStore(t *testing.T) {
	p, sys, home, r := newHomeRoutedProvider(t)

	serial, _ := issueFor(t, p, "acme")

	if mustGet(t, sys, prefixCertCA+"acme"+materialRecordSuffix) == nil {
		t.Error("acme's CA is not in the system store")
	}
	if mustGet(t, sys, prefixCerts+"acme/"+serial) == nil {
		t.Error("acme's record is not in the system store")
	}
	if mustGet(t, sys, publishedCAKey("acme")) != nil {
		t.Error("a tenant that is not homed has no published copy")
	}
	if len(home.data) != 0 {
		t.Errorf("home store written for another tenant: %d keys", len(home.data))
	}
	if n := r.resolveCount(); n != 0 {
		t.Errorf("Resolve called %d times for a tenant that is not homed", n)
	}
}

// TestHomeRouting_LookupBeforeCreateRoutesToTheHome is the Codex P1 on #812:
// a trust-path lookup naming a tenant before it is registered must not leave
// a system-store routing behind for the isolated tenant created next. Its CA
// and records land in its home and stay reachable after the cache window.
func TestHomeRouting_LookupBeforeCreateRoutesToTheHome(t *testing.T) {
	ctx := context.Background()
	p, sys, _, r := newHomeRoutedProvider(t)
	clock := useManualClock(p)
	const late = "late"

	if ok, err := p.IsTenantIssuer(ctx, late, foreignLeaf(t, "")); err != nil || ok {
		t.Fatalf("IsTenantIssuer before creation = %v, %v; want false, nil", ok, err)
	}
	if recs, err := p.ListCerts(ctx, late); err != nil || len(recs) != 0 {
		t.Fatalf("ListCerts before creation = %d records, %v; want none", len(recs), err)
	}

	home := r.createHome(late)
	serial, leaf := issueFor(t, p, late)

	records := []string{
		prefixCertCA + late + materialRecordSuffix,
		prefixCertCA + late + certRecordSuffix,
		prefixCerts + late + "/" + serial,
	}
	for _, key := range records {
		if mustGet(t, sys, key) != nil {
			t.Errorf("system store holds %s", key)
		}
		if mustGet(t, home, key) == nil {
			t.Errorf("home store lacks %s", key)
		}
	}
	ca := publishedCAFor(t, sys, late)
	if err := leaf.CheckSignatureFrom(ca); err != nil {
		t.Fatalf("issued certificate does not verify against the published CA: %v", err)
	}

	clock.advance(perTenantRevalidateTTL + time.Second)

	recs, err := p.ListCerts(ctx, late)
	if err != nil || len(recs) != 1 || recs[0].Serial != serial {
		t.Fatalf("ListCerts after the cache window = %+v, %v; want the one issued record", recs, err)
	}
	if ok, err := p.IsTenantIssuer(ctx, late, ca); err != nil || !ok {
		t.Fatalf("IsTenantIssuer after the cache window = %v, %v; want true, nil", ok, err)
	}
	_, again := issueFor(t, p, late)
	if err := again.CheckSignatureFrom(ca); err != nil {
		t.Fatalf("issuance after the cache window did not use the home CA: %v", err)
	}
	for _, key := range records[:2] {
		if mustGet(t, sys, key) != nil {
			t.Errorf("system store holds %s after the cache window", key)
		}
	}
}

// TestHomeRouting_OnlyAHomedAnswerIsCached pins the cache contract route
// keeps: a tenant that is not homed is asked about on every call, reusing
// the system-store provider while the answer holds, and a homed one is asked
// about again only once its entry has aged past perTenantRevalidateTTL.
func TestHomeRouting_OnlyAHomedAnswerIsCached(t *testing.T) {
	ctx := context.Background()
	p, _, _, r := newHomeRoutedProvider(t)
	clock := useManualClock(p)

	first, err := p.route(ctx, "acme")
	if err != nil || first.home {
		t.Fatalf("route(acme) = %+v, %v; want a system-store entry", first, err)
	}
	asked := r.inHomeCount()
	second, err := p.route(ctx, "acme")
	if err != nil || second != first {
		t.Fatalf("second route(acme) = %p, %v; want the cached entry %p", second, err, first)
	}
	if got := r.inHomeCount(); got != asked+1 {
		t.Fatalf("InHome calls for a tenant that is not homed = %d, want %d: the negative answer was cached", got, asked+1)
	}

	homed, err := p.route(ctx, homedTenant)
	if err != nil || !homed.home {
		t.Fatalf("route(%s) = %+v, %v; want a homed entry", homedTenant, homed, err)
	}
	asked = r.inHomeCount()
	if again, err := p.route(ctx, homedTenant); err != nil || again != homed {
		t.Fatalf("second route(%s) = %p, %v; want the cached entry %p", homedTenant, again, err, homed)
	}
	if got := r.inHomeCount(); got != asked {
		t.Fatalf("InHome asked %d times for a cached homed tenant, want 0", got-asked)
	}

	clock.advance(perTenantRevalidateTTL)
	rebuilt, err := p.route(ctx, homedTenant)
	if err != nil || rebuilt == homed || !rebuilt.home {
		t.Fatalf("route(%s) past the window = %p, %v; want a rebuilt homed entry", homedTenant, rebuilt, err)
	}
	if got := r.inHomeCount(); got != asked+1 {
		t.Fatalf("InHome calls past the window = %d, want %d", got, asked+1)
	}
}

func TestHomeRouting_InitializePublishesTheCA(t *testing.T) {
	p, sys, home, _ := newHomeRoutedProvider(t)
	ctx := tenant.WithTenantID(context.Background(), homedTenant)

	if err := p.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if mustGet(t, home, prefixCertCA+homedTenant+certRecordSuffix) == nil {
		t.Fatal("Initialize did not establish the CA in the home store")
	}
	publishedCAFor(t, sys, homedTenant)

	// Unseal re-establishes from the home store and keeps the published copy.
	if err := p.Seal(); err != nil {
		t.Fatal(err)
	}
	if err := p.Unseal(ctx); err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	publishedCAFor(t, sys, homedTenant)
}

func TestHomeRouting_PublishFailureRefusesToSign(t *testing.T) {
	sys := &failPutKVStore{mockKVStore: newMockKVStore(), prefix: prefixCertCAPub}
	p := NewDefaultProvider(sys, newMockBarrier(), WithTrustDomain(sanTestDomain))
	r, home := newHomeRoutingResolver()
	p.SetStoreResolver(r)

	_, err := generateForTest(context.Background(), p, &testRequest{Username: "alice", TenantID: homedTenant, TTL: time.Hour})
	var storeErr *StoreError
	if !errors.As(err, &storeErr) || storeErr.Detail != publishedCAKey(homedTenant) {
		t.Fatalf("err = %v; want a *StoreError on the published CA", err)
	}
	keys, _ := home.List(context.Background(), prefixCerts)
	if len(keys) != 0 {
		t.Fatalf("a certificate was recorded under an unpublished CA: %v", keys)
	}
}

func TestHomeRouting_TrustPathReadsOnlyThePublishedCopy(t *testing.T) {
	p, _, _, r := newHomeRoutedProvider(t)
	_, leaf := issueFor(t, p, homedTenant)
	p.invalidateAll()
	r.sealHome()
	before := r.resolveCount()
	ctx := context.Background()

	pems, err := p.CACertPEMs(ctx)
	if err != nil {
		t.Fatalf("CACertPEMs: %v", err)
	}
	if len(pems) != 1 || p.SkippedTenantCACount() != 0 {
		t.Fatalf("CACertPEMs = %d anchors, %d skipped; want the published CA alone", len(pems), p.SkippedTenantCACount())
	}
	block, _ := pem.Decode(pems[0])
	issuer, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := leaf.CheckSignatureFrom(issuer); err != nil {
		t.Fatalf("anchor did not sign the tenant's leaf: %v", err)
	}

	ok, err := p.IsTenantIssuer(ctx, homedTenant, issuer)
	if err != nil || !ok {
		t.Fatalf("IsTenantIssuer(published CA) = %v, %v; want true", ok, err)
	}
	ok, err = p.IsTenantIssuer(ctx, homedTenant, foreignLeaf(t, homedTenant))
	if err != nil || ok {
		t.Fatalf("IsTenantIssuer(another certificate) = %v, %v; want false", ok, err)
	}
	if n := r.resolveCount() - before; n != 0 {
		t.Fatalf("the trust path resolved the home store %d times", n)
	}
}

func TestHomeRouting_IsTenantIssuer_NothingPublished(t *testing.T) {
	p, _, _, _ := newHomeRoutedProvider(t)

	ok, err := p.IsTenantIssuer(context.Background(), homedTenant, foreignLeaf(t, homedTenant))
	if err != nil || ok {
		t.Fatalf("IsTenantIssuer with no published CA = %v, %v; want false, nil", ok, err)
	}
}

func TestHomeRouting_CorruptPublishedCA(t *testing.T) {
	p, sys, _, _ := newHomeRoutedProvider(t)
	ctx := context.Background()
	if err := sys.Put(ctx, publishedCAKey(homedTenant), []byte("not-der")); err != nil {
		t.Fatal(err)
	}

	pems, err := p.CACertPEMs(ctx)
	if err != nil || len(pems) != 0 || p.SkippedTenantCACount() != 1 {
		t.Fatalf("CACertPEMs = %d, %v, skipped %d; want the corrupt anchor skipped and counted", len(pems), err, p.SkippedTenantCACount())
	}
	var storeErr *StoreError
	if _, err := p.IsTenantIssuer(ctx, homedTenant, foreignLeaf(t, homedTenant)); !errors.As(err, &storeErr) || storeErr.Op != "decode" {
		t.Fatalf("IsTenantIssuer err = %v; want a decode *StoreError", err)
	}
}

func TestHomeRouting_RevokeKeepsMarkerInHome(t *testing.T) {
	p, sys, home, _ := newHomeRoutedProvider(t)
	serial, leaf := issueFor(t, p, homedTenant)
	vault := tenant.WithTenantID(context.Background(), homedTenant)

	owner, found, err := p.TenantForSerial(vault, serial)
	if err != nil || !found || owner != homedTenant {
		t.Fatalf("TenantForSerial = %q, %v, %v; want the homed tenant", owner, found, err)
	}
	res, err := p.RevokeCert(vault, serial)
	if err != nil || res.Outcome != RevokeOutcomeRecordUpdated {
		t.Fatalf("RevokeCert = %+v, %v; want the record updated", res, err)
	}
	if mustGet(t, sys, prefixCertsRevoked+serial) != nil {
		t.Error("the revocation marker is in the system store")
	}
	if mustGet(t, home, prefixCertsRevoked+serial) == nil {
		t.Error("the revocation marker is not in the home store")
	}
	var rec CertRecord
	if err := json.Unmarshal(mustGet(t, home, prefixCerts+homedTenant+"/"+serial), &rec); err != nil || !rec.Revoked {
		t.Errorf("home record not marked revoked: %+v, %v", rec, err)
	}

	if revoked, err := p.IsRevoked(vault, serial); err != nil || !revoked {
		t.Errorf("IsRevoked(homed caller) = %v, %v; want true", revoked, err)
	}
	if revoked, err := p.IsPeerRevoked(context.Background(), leaf); err != nil || !revoked {
		t.Errorf("IsPeerRevoked = %v, %v; want true", revoked, err)
	}
	certs, err := p.ListCerts(vault, homedTenant)
	if err != nil || len(certs) != 1 || !certs[0].Revoked {
		t.Errorf("ListCerts = %+v, %v; want the one revoked record", certs, err)
	}
}

func TestHomeRouting_OtherCallersDoNotSeeTheHome(t *testing.T) {
	p, _, _, _ := newHomeRoutedProvider(t)
	serial, _ := issueFor(t, p, homedTenant)

	for _, caller := range []string{"system", "acme"} {
		ctx := tenant.WithTenantID(context.Background(), caller)
		if _, found, err := p.TenantForSerial(ctx, serial); err != nil || found {
			t.Errorf("TenantForSerial as %s = %v, %v; want not found", caller, found, err)
		}
	}
}

func TestHomeRouting_RevokeReapsTheHomeStore(t *testing.T) {
	p, _, home, _ := newHomeRoutedProvider(t)
	serial, _ := issueFor(t, p, homedTenant)
	ctx := context.Background()
	expired, _ := json.Marshal(&revocationEntry{Serial: "dead", NotAfter: time.Now().Add(-time.Hour)})
	if err := home.Put(ctx, prefixCertsRevoked+"dead", expired); err != nil {
		t.Fatal(err)
	}

	if _, err := p.RevokeCert(tenant.WithTenantID(ctx, homedTenant), serial); err != nil {
		t.Fatalf("RevokeCert: %v", err)
	}
	if mustGet(t, home, prefixCertsRevoked+"dead") != nil {
		t.Error("an expired marker in the home store was not reaped")
	}
}

func TestHomeRouting_PeerRevocation(t *testing.T) {
	t.Run("sealed home fails closed for the tenant's own certificate", func(t *testing.T) {
		p, _, _, r := newHomeRoutedProvider(t)
		_, leaf := issueFor(t, p, homedTenant)
		r.sealHome()

		_, err := p.IsPeerRevoked(context.Background(), leaf)
		if !errors.Is(err, tenant.ErrTenantSealed) {
			t.Fatalf("err = %v; want the home's ErrTenantSealed", err)
		}
		cache := NewRevocationCache(p, time.Minute)
		_, err = cache.IsCertRevoked(context.Background(), leaf)
		var unavailable *RevocationStateUnavailableError
		if err == nil || errors.As(err, &unavailable) {
			t.Fatalf("cache err = %v; want a lookup failure the caller fails closed on", err)
		}
	})

	t.Run("a certificate another CA signed is never blocked on the home", func(t *testing.T) {
		p, _, _, r := newHomeRoutedProvider(t)
		issueFor(t, p, homedTenant)
		r.sealHome()

		revoked, err := p.IsPeerRevoked(context.Background(), foreignLeaf(t, homedTenant))
		if err != nil || revoked {
			t.Fatalf("IsPeerRevoked = %v, %v; want false, nil", revoked, err)
		}
	})

	t.Run("a system-store marker revokes any certificate", func(t *testing.T) {
		p, sys, _, r := newHomeRoutedProvider(t)
		_, leaf := issueFor(t, p, homedTenant)
		r.sealHome()
		if err := sys.Put(context.Background(), prefixCertsRevoked+FormatSerial(leaf.SerialNumber), []byte("1")); err != nil {
			t.Fatal(err)
		}

		revoked, err := p.IsPeerRevoked(context.Background(), leaf)
		if err != nil || !revoked {
			t.Fatalf("IsPeerRevoked = %v, %v; want true", revoked, err)
		}
	})

	t.Run("the revocation cache asks about the certificate", func(t *testing.T) {
		p, _, _, _ := newHomeRoutedProvider(t)
		serial, leaf := issueFor(t, p, homedTenant)
		if _, err := p.RevokeCert(tenant.WithTenantID(context.Background(), homedTenant), serial); err != nil {
			t.Fatal(err)
		}

		revoked, err := NewRevocationCache(p, time.Minute).IsCertRevoked(context.Background(), leaf)
		if err != nil || !revoked {
			t.Fatalf("IsCertRevoked = %v, %v; want the home marker to revoke", revoked, err)
		}
	})

	t.Run("a tenant that is not homed has no home marker", func(t *testing.T) {
		p, _, _, r := newHomeRoutedProvider(t)
		_, leaf := issueFor(t, p, "acme")

		revoked, err := p.IsPeerRevoked(context.Background(), leaf)
		if err != nil || revoked {
			t.Fatalf("IsPeerRevoked = %v, %v; want false, nil", revoked, err)
		}
		if n := r.resolveCount(); n != 0 {
			t.Fatalf("Resolve called %d times", n)
		}
	})

	t.Run("a homed tenant with no CA has no tenant-issued certificate", func(t *testing.T) {
		p, _, _, _ := newHomeRoutedProvider(t)

		revoked, err := p.IsPeerRevoked(context.Background(), foreignLeaf(t, homedTenant))
		if err != nil || revoked {
			t.Fatalf("IsPeerRevoked = %v, %v; want false, nil", revoked, err)
		}
	})

	t.Run("a certificate naming no tenant", func(t *testing.T) {
		p, _, _, _ := newHomeRoutedProvider(t)

		revoked, err := p.IsPeerRevoked(context.Background(), foreignLeaf(t, ""))
		if err != nil || revoked {
			t.Fatalf("IsPeerRevoked = %v, %v; want false, nil", revoked, err)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		p, _, barrier := newTestProvider()
		var validation *ValidationError
		if _, err := p.IsPeerRevoked(context.Background(), nil); !errors.As(err, &validation) {
			t.Errorf("nil peer: err = %v; want *ValidationError", err)
		}
		if _, err := p.IsPeerRevoked(context.Background(), &x509.Certificate{}); !errors.As(err, &validation) {
			t.Errorf("no serial: err = %v; want *ValidationError", err)
		}
		barrier.seal()
		var sealed *BarrierSealedError
		if _, err := p.IsPeerRevoked(context.Background(), foreignLeaf(t, "acme")); !errors.As(err, &sealed) {
			t.Errorf("sealed: err = %v; want *BarrierSealedError", err)
		}
	})

	t.Run("system store read failure", func(t *testing.T) {
		p := NewDefaultProvider(&errorKVStore{err: errors.New("down")}, newMockBarrier())
		var storeErr *StoreError
		if _, err := p.IsPeerRevoked(context.Background(), foreignLeaf(t, "acme")); !errors.As(err, &storeErr) {
			t.Fatalf("err = %v; want *StoreError", err)
		}
	})
}

func TestHomeRouting_SealedHomeRefusesRecordsAndSigning(t *testing.T) {
	p, _, _, r := newHomeRoutedProvider(t)
	serial, _ := issueFor(t, p, homedTenant)
	r.sealHome()
	vault := tenant.WithTenantID(context.Background(), homedTenant)

	if _, err := generateForTest(vault, p, &testRequest{Username: "bob", TenantID: homedTenant, TTL: time.Hour}); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Errorf("issue: err = %v; want ErrTenantSealed", err)
	}
	if _, err := p.ListCerts(vault, homedTenant); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Errorf("ListCerts: err = %v; want ErrTenantSealed", err)
	}
	if _, err := p.RevokeCert(vault, serial); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Errorf("RevokeCert: err = %v; want ErrTenantSealed", err)
	}
	if _, err := p.IsRevoked(vault, serial); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Errorf("IsRevoked: err = %v; want ErrTenantSealed", err)
	}
}

func TestHomeRouting_LocatorFailure(t *testing.T) {
	p, _, _, r := newHomeRoutedProvider(t)
	boom := errors.New("registry unavailable")
	r.set(func(r *homeRoutingResolver) { r.inHomeErr = boom })

	if _, err := generateForTest(context.Background(), p, &testRequest{Username: "alice", TenantID: homedTenant, TTL: time.Hour}); !errors.Is(err, boom) {
		t.Errorf("issue: err = %v; want the locator's error", err)
	}
	if _, err := p.IsTenantIssuer(context.Background(), homedTenant, foreignLeaf(t, homedTenant)); !errors.Is(err, boom) {
		t.Errorf("IsTenantIssuer: err = %v; want the locator's error", err)
	}
	if _, err := p.IsPeerRevoked(context.Background(), foreignLeaf(t, homedTenant)); !errors.Is(err, boom) {
		t.Errorf("IsPeerRevoked: err = %v; want the locator's error", err)
	}
}

func TestHomeRouting_HomeStoreChangeIsFollowed(t *testing.T) {
	p, sys, home, r := newHomeRoutedProvider(t)
	issueFor(t, p, homedTenant)
	p.mu.RLock()
	first := p.perTenant[homedTenant].sp
	p.mu.RUnlock()

	// The same replicated records, reached through a rebuilt store.
	rebuilt := newMockKVStore()
	for k, v := range home.data {
		rebuilt.data[k] = v
	}
	r.set(func(r *homeRoutingResolver) { r.homes[homedTenant] = rebuilt })

	serial, leaf := issueFor(t, p, homedTenant)
	p.mu.RLock()
	second := p.perTenant[homedTenant].sp
	p.mu.RUnlock()
	if second == first {
		t.Fatal("the provider over the replaced home store was reused")
	}
	if mustGet(t, rebuilt, prefixCerts+homedTenant+"/"+serial) == nil {
		t.Fatal("the record was not written to the rebuilt home store")
	}
	if err := leaf.CheckSignatureFrom(publishedCAFor(t, sys, homedTenant)); err != nil {
		t.Fatalf("the CA changed across the rebuild: %v", err)
	}

	// Unchanged, the provider is reused.
	issueFor(t, p, homedTenant)
	p.mu.RLock()
	third := p.perTenant[homedTenant].sp
	p.mu.RUnlock()
	if third != second {
		t.Fatal("the provider was rebuilt over an unchanged home store")
	}
}

func TestHomeRouting_HomeStoreWithoutGuardedWrites(t *testing.T) {
	p, _, _, r := newHomeRoutedProvider(t)
	r.set(func(r *homeRoutingResolver) { r.homes[homedTenant] = plainKVStore{newMockKVStore()} })

	_, err := generateForTest(context.Background(), p, &testRequest{Username: "alice", TenantID: homedTenant, TTL: time.Hour})
	var notGuarded *HomeStoreNotGuardedError
	if !errors.As(err, &notGuarded) || notGuarded.TenantID != homedTenant {
		t.Fatalf("err = %v; want *HomeStoreNotGuardedError for the tenant", err)
	}
	if notGuarded.Error() == "" {
		t.Fatal("empty error string")
	}
}

func TestHomeRouting_SetStoreResolverDetaches(t *testing.T) {
	for name, r := range map[string]tenant.StoreResolver{
		"nil":             nil,
		"no locator view": resolveOnly{},
	} {
		t.Run(name, func(t *testing.T) {
			p, sys, home, _ := newHomeRoutedProvider(t)
			p.SetStoreResolver(r)

			serial, _ := issueFor(t, p, homedTenant)
			if mustGet(t, sys, prefixCerts+homedTenant+"/"+serial) == nil {
				t.Error("a detached provider did not keep the record in the system store")
			}
			if len(home.data) != 0 {
				t.Error("a detached provider wrote the home store")
			}
		})
	}
}

// resolveOnly is a StoreResolver with no HomeLocator view.
type resolveOnly struct{}

func (resolveOnly) Resolve(context.Context, string) (kvstore.KVStore, error) {
	return nil, errNotHomed
}

func TestHomeRouting_HomeStoreWithNoRouting(t *testing.T) {
	p, _, _ := newTestProvider()

	_, err := p.homeStore(context.Background(), homedTenant)
	if !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("err = %v; want the unattached home's ErrTenantSealed", err)
	}
}

func TestHomeRouting_SealWithRoutingOnlyEntries(t *testing.T) {
	p, _, _, _ := newHomeRoutedProvider(t)
	if _, err := p.IsTenantIssuer(context.Background(), homedTenant, foreignLeaf(t, homedTenant)); err != nil {
		t.Fatal(err)
	}
	p.mu.RLock()
	entry := p.perTenant[homedTenant]
	p.mu.RUnlock()
	if entry == nil || entry.sp != nil {
		t.Fatalf("entry = %+v; want a routing-only entry", entry)
	}
	if err := p.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
}

func TestSameStore(t *testing.T) {
	a, b := newMockKVStore(), newMockKVStore()
	cases := []struct {
		name string
		x, y kvstore.MultiGuardedBatchKVStore
		want bool
	}{
		{"same pointer", a, a, true},
		{"different pointers", a, b, false},
		{"nil", nil, a, false},
		{"different types", a, &failPutKVStore{mockKVStore: a}, false},
		{"value type", valueKVStore{a}, valueKVStore{a}, false},
	}
	for _, c := range cases {
		if got := sameStore(c.x, c.y); got != c.want {
			t.Errorf("%s: sameStore = %v; want %v", c.name, got, c.want)
		}
	}
}

func TestHomeRouting_PublishSkipsAWriteWhenTheStoreHoldsTheCA(t *testing.T) {
	p, sys, _, _ := newHomeRoutedProvider(t)
	issueFor(t, p, homedTenant)
	der := mustGet(t, sys, publishedCAKey(homedTenant))

	// A fresh entry has no cached publication: it reads, finds the same
	// certificate, and does not write.
	p.invalidateAll()
	guard := &failPutKVStore{mockKVStore: sys, prefix: prefixCertCAPub}
	p.kvStore = guard
	issueFor(t, p, homedTenant)
	if !bytes.Equal(mustGet(t, sys, publishedCAKey(homedTenant)), der) {
		t.Fatal("the published CA changed")
	}
}
