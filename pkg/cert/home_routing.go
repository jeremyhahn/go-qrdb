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
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"reflect"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// An isolated tenant's CA and certificate records live in its home store.
//
// See docs/multi-tenancy/architecture.md "Strict isolation". The CA key and
// certificate of an isolated tenant, the records of the certificates it issued
// and their revocation markers are held in the tenant's home store, never in
// the system shard. The one record that stays is the CA certificate, which is
// public: it is published to the system store under prefixCertCAPub, because
// a serving node verifies a client's chain before it reads anything else
// about the client, including whether the tenant is unsealed. The trust path
// (IsTenantIssuer, CACertPEMs) therefore reads only the published copy and
// never the home store.
//
// Revocation of a tenant-issued certificate is enforced by IsPeerRevoked on
// the serving replicas: a marker in the home store revokes, and a home store
// that cannot be read fails the check closed, but only for a certificate the
// tenant's own CA signed. A certificate the cluster CA issued naming the
// tenant is not the tenant's to revoke and is never blocked on its home.
// ---------------------------------------------------------------------------

// prefixCertCAPub is the system-store prefix under which the public
// certificate of an isolated tenant's CA is published, as
// prefixCertCAPub + tenantID + certRecordSuffix holding the DER. The trailing
// record segment keeps tenant deletion's "{prefix}{tenantID}/" reclaim from
// matching a longer tenant id.
const prefixCertCAPub = "__sys:cert_ca_pub/"

// TenantStoreRouter is implemented by a CertProvider that keeps an isolated
// tenant's CA and certificate records in the tenant's home store. The
// composer attaches the node's StoreResolver once it exists, which is after
// the provider is built.
type TenantStoreRouter interface {
	// SetStoreResolver routes isolated tenants' records through r. A nil r, or
	// one that cannot say which tenants are homed (tenant.HomeLocator),
	// detaches routing: every record is then kept in the system store.
	SetStoreResolver(r tenant.StoreResolver)
}

// PeerRevocationChecker is implemented by a CertProvider that can answer
// whether a presented certificate is revoked from the certificate itself,
// which a provider needs when revocation markers are not all in one store.
// RevocationCache prefers it to CertProvider.IsRevoked.
type PeerRevocationChecker interface {
	// IsPeerRevoked reports whether peer has been revoked. It follows
	// RevocationCache.IsCertRevoked's error contract: *BarrierSealedError
	// when the system barrier is sealed, any other error a lookup that
	// could have answered and did not.
	IsPeerRevoked(ctx context.Context, peer *x509.Certificate) (bool, error)
}

// Compile-time interface checks.
var (
	_ TenantStoreRouter     = (*DefaultCertProvider)(nil)
	_ PeerRevocationChecker = (*DefaultCertProvider)(nil)
)

// storeRouting is the resolver SetStoreResolver attached, with its
// HomeLocator view.
type storeRouting struct {
	resolver tenant.StoreResolver
	locator  tenant.HomeLocator
}

// SetStoreResolver implements TenantStoreRouter. Every cached tenant entry
// was routed under the previous resolver, so all of them are dropped.
func (p *DefaultCertProvider) SetStoreResolver(r tenant.StoreResolver) {
	locator, ok := r.(tenant.HomeLocator)
	if r == nil || !ok {
		p.routing.Store(nil)
	} else {
		p.routing.Store(&storeRouting{resolver: r, locator: locator})
	}
	p.invalidateAll()
}

// route returns tenantID's cached entry, building it when it is missing or
// older than perTenantRevalidateTTL. The entry records whether the tenant is
// homed; for a tenant that is not, under the built-in provider, it also
// carries the cakey.SoftwareProvider over the system store.
//
// Only a homed answer is trusted from the cache. A tenant the locator does
// not home -- including one not registered yet, which an IsTenantIssuer or
// ListCerts may name before its creation -- is asked again on every call,
// and the cached system-store provider is reused only while the answer still
// holds. An isolated tenant created after such a lookup is therefore routed
// to its home on its first write, never into the system store; a cached
// homed answer that has gone stale fails closed on the home store instead.
func (p *DefaultCertProvider) route(ctx context.Context, tenantID string) (*perTenantEntry, error) {
	p.mu.RLock()
	cached, ok := p.perTenant[tenantID]
	p.mu.RUnlock()
	fresh := ok && p.now().Sub(cached.builtAt) < perTenantRevalidateTTL
	if fresh && cached.home {
		return cached, nil
	}

	home, err := p.inHome(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if fresh && !home {
		return cached, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if entry, ok := p.perTenant[tenantID]; ok && entry.home == home && p.now().Sub(entry.builtAt) < perTenantRevalidateTTL {
		return entry, nil
	}
	entry := &perTenantEntry{home: home, builtAt: p.now()}
	if !home && p.keyProviderFactory == nil {
		entry.store = p.kvStore
		entry.sp = p.newSoftwareProvider(p.kvStore, tenantID)
	}
	if p.perTenant == nil {
		p.perTenant = make(map[string]*perTenantEntry)
	}
	p.perTenant[tenantID] = entry
	return entry, nil
}

// inHome reports whether tenantID's records are in its home store.
func (p *DefaultCertProvider) inHome(ctx context.Context, tenantID string) (bool, error) {
	r := p.routing.Load()
	if r == nil {
		return false, nil
	}
	return r.locator.InHome(ctx, tenantID)
}

// homeStore resolves tenantID's home store. A resolver error is returned
// as-is, so a sealed or unattached home keeps its ErrTenantSealed meaning.
func (p *DefaultCertProvider) homeStore(ctx context.Context, tenantID string) (kvstore.MultiGuardedBatchKVStore, error) {
	r := p.routing.Load()
	if r == nil {
		return nil, &tenant.TenantHomeUnavailableError{TenantID: tenantID, Err: tenant.ErrTenantHomeNotAttached}
	}
	store, err := r.resolver.Resolve(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	guarded, ok := store.(kvstore.MultiGuardedBatchKVStore)
	if !ok {
		return nil, &HomeStoreNotGuardedError{TenantID: tenantID}
	}
	return guarded, nil
}

// recordStore returns the store tenantID's certificate records and
// revocation markers are in, and whether it is the tenant's home store.
func (p *DefaultCertProvider) recordStore(ctx context.Context, tenantID string) (kvstore.KVStore, bool, error) {
	entry, err := p.route(ctx, tenantID)
	if err != nil {
		return nil, false, err
	}
	if !entry.home {
		return p.kvStore, false, nil
	}
	store, err := p.homeStore(ctx, tenantID)
	if err != nil {
		return nil, true, err
	}
	return store, true, nil
}

// homeKeyProvider returns the cakey.SoftwareProvider over a homed tenant's
// CA records. The home store is resolved on every call and the provider is
// rebuilt when it is not the store the cached one was built over, so a
// rebuilt home store is followed rather than a stale one signed through.
func (p *DefaultCertProvider) homeKeyProvider(ctx context.Context, tenantID string, entry *perTenantEntry) (*cakey.SoftwareProvider, *perTenantEntry, error) {
	store, err := p.homeStore(ctx, tenantID)
	if err != nil {
		return nil, nil, err
	}
	if entry.sp != nil && sameStore(entry.store, store) {
		return entry.sp, entry, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if cur, ok := p.perTenant[tenantID]; ok && cur.home && cur.sp != nil && sameStore(cur.store, store) {
		return cur.sp, cur, nil
	}
	rebuilt := &perTenantEntry{
		sp:      p.newSoftwareProvider(store, tenantID),
		store:   store,
		home:    true,
		builtAt: entry.builtAt,
	}
	if p.perTenant == nil {
		p.perTenant = make(map[string]*perTenantEntry)
	}
	p.perTenant[tenantID] = rebuilt
	return rebuilt.sp, rebuilt, nil
}

// newSoftwareProvider returns the built-in provider of tenantID's CA over
// store.
func (p *DefaultCertProvider) newSoftwareProvider(store kvstore.MultiGuardedBatchKVStore, tenantID string) *cakey.SoftwareProvider {
	subject := pkix.Name{
		CommonName:         "qrdb-ca",
		OrganizationalUnit: []string{tenantID},
	}
	return cakey.NewSoftwareProvider(store, p.barrier, prefixCertCA+tenantID+"/", subject, caTTL)
}

// sameStore reports whether a and b are the same store. Only pointer-typed
// stores are compared: comparing two interface values of a non-comparable
// dynamic type panics, and a store that cannot be compared is treated as
// changed, which costs a rebuild and never a wrong answer.
func sameStore(a, b kvstore.MultiGuardedBatchKVStore) bool {
	if a == nil || b == nil {
		return false
	}
	ta := reflect.TypeOf(a)
	if ta != reflect.TypeOf(b) || ta.Kind() != reflect.Pointer {
		return false
	}
	return a == b
}

// publishedCAKey is the system-store key of tenantID's published CA
// certificate.
func publishedCAKey(tenantID string) string {
	return prefixCertCAPub + tenantID + certRecordSuffix
}

// holdCA records that this provider holds cert as tenantID's CA. A homed
// tenant's certificate is first published to the system store, so a node
// that admits the tenant's clients can verify their chains; a caller that
// fails to publish is refused, and nothing is signed under a CA no serving
// node can recognise.
func (p *DefaultCertProvider) holdCA(ctx context.Context, tenantID string, entry *perTenantEntry, cert *x509.Certificate) error {
	if entry != nil && entry.home {
		if err := p.publishCA(ctx, tenantID, entry, cert); err != nil {
			return err
		}
	}
	p.noteHeldCA(cert)
	return nil
}

// publishCA writes cert as tenantID's published CA certificate unless the
// system store already holds it.
func (p *DefaultCertProvider) publishCA(ctx context.Context, tenantID string, entry *perTenantEntry, cert *x509.Certificate) error {
	if pub := entry.published.Load(); pub != nil && bytes.Equal(pub.Raw, cert.Raw) {
		return nil
	}
	key := publishedCAKey(tenantID)
	cur, err := p.kvStore.Get(ctx, key)
	if err != nil {
		return &StoreError{Op: "get", Detail: key, Err: err}
	}
	if !bytes.Equal(cur, cert.Raw) {
		if err := p.kvStore.Put(ctx, key, cert.Raw); err != nil {
			return &StoreError{Op: "put", Detail: key, Err: err}
		}
	}
	entry.published.Store(cert)
	return nil
}

// publishedCA returns a homed tenant's published CA certificate, or
// *CAKeyNotAvailableError when none has been published.
func (p *DefaultCertProvider) publishedCA(ctx context.Context, tenantID string, entry *perTenantEntry) (*x509.Certificate, error) {
	if pub := entry.published.Load(); pub != nil {
		return pub, nil
	}
	key := publishedCAKey(tenantID)
	der, err := p.kvStore.Get(ctx, key)
	if err != nil {
		return nil, &StoreError{Op: "get", Detail: key, Err: err}
	}
	if der == nil {
		return nil, &CAKeyNotAvailableError{}
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, &StoreError{Op: "decode", Detail: key, Err: err}
	}
	entry.published.Store(cert)
	return cert, nil
}

// tenantCACert returns tenantID's CA certificate without establishing one:
// for a homed tenant the published copy, so the trust path never reads the
// home store.
func (p *DefaultCertProvider) tenantCACert(ctx context.Context, tenantID string) (*x509.Certificate, error) {
	if p.keyProviderFactory != nil {
		prov, err := p.keyProviderFactory.ProviderForTenant(tenantID)
		if err != nil {
			return nil, err
		}
		return p.loadOnlyCertificate(ctx, prov)
	}
	entry, err := p.route(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if entry.home {
		return p.publishedCA(ctx, tenantID, entry)
	}
	return p.loadOnlyCertificate(ctx, entry.sp)
}

// IsPeerRevoked implements PeerRevocationChecker.
//
// A marker in the system store revokes any certificate, which keeps
// deny-by-serial working for every issuer. A homed tenant's own markers are
// in its home store and are consulted for a certificate its CA signed; that
// read failing is an error, so the caller fails closed. A certificate whose
// tenant cannot be derived, or that the tenant's CA did not sign, has no
// marker in any home store.
func (p *DefaultCertProvider) IsPeerRevoked(ctx context.Context, peer *x509.Certificate) (bool, error) {
	if p.barrier.IsSealed() {
		return false, &BarrierSealedError{}
	}
	if peer == nil {
		return false, &ValidationError{Field: "peer", Reason: "must not be nil"}
	}
	serial := FormatSerial(peer.SerialNumber)
	if serial == "" {
		return false, &ValidationError{Field: "serial", Reason: "must not be empty"}
	}
	revokedKey := prefixCertsRevoked + serial
	val, err := p.kvStore.Get(ctx, revokedKey)
	if err != nil {
		return false, &StoreError{Op: "get", Detail: revokedKey, Err: err}
	}
	if val != nil {
		return true, nil
	}

	tenantID, err := auth.TenantFromClientCert(peer, p.trustDomain)
	if err != nil || tenantID == "" {
		return false, nil
	}
	entry, err := p.route(ctx, tenantID)
	if err != nil {
		return false, err
	}
	if !entry.home {
		return false, nil
	}
	ca, err := p.tenantCACert(ctx, tenantID)
	if err != nil {
		var notAvailable *CAKeyNotAvailableError
		if errors.As(err, &notAvailable) {
			return false, nil
		}
		return false, err
	}
	if peer.CheckSignatureFrom(ca) != nil {
		return false, nil
	}
	store, err := p.homeStore(ctx, tenantID)
	if err != nil {
		return false, err
	}
	val, err = store.Get(ctx, revokedKey)
	if err != nil {
		return false, &StoreError{Op: "get", Detail: revokedKey, Err: err}
	}
	return val != nil, nil
}
