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
	"crypto"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"log/slog"
	"math/big"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

const (
	// defaultTTL is the default certificate validity period (1 year).
	defaultTTL = 8760 * time.Hour

	// caTTL is the CA certificate validity period (10 years).
	caTTL = 87600 * time.Hour

	// perTenantRevalidateTTL bounds how long a cached perTenant entry is
	// trusted without revalidation, independent of watch-driven eviction
	// (go-qrdb#591's review, non-blocking defense in depth): see
	// perTenant's own field doc comment for why this exists alongside,
	// not instead of, WatchForDeletions.
	perTenantRevalidateTTL = 5 * time.Minute

	// prefixCertCA is the KVStore key prefix for CA material (consumed by
	// cakey.SoftwareProvider, one instance per tenant -- see
	// resolveKeyProvider).
	prefixCertCA = "__sys:cert_ca/"

	// prefixCerts is the KVStore key prefix for issued cert records.
	prefixCerts = "__sys:certs/"

	// prefixCertsRevoked is the KVStore key prefix for revoked certs.
	prefixCertsRevoked = "__sys:certs_revoked/"

	// serialBitSize is the size in bits of the serial number.
	serialBitSize = 128
)

// Compile-time interface check.
var _ CertProvider = (*DefaultCertProvider)(nil)

// ProviderOption is a functional option for configuring a DefaultCertProvider.
type ProviderOption func(*providerOptions)

// providerOptions holds the resolved provider configuration.
type providerOptions struct {
	defaultTTL         time.Duration
	maxTTL             time.Duration
	trustDomain        string
	keyProviderFactory cakey.ProviderFactory
	watchSvc           WatchPrefixer
	onNewCA            func()
	audit              audit.AuditLogger
}

// WithDefaultTTL sets the default certificate validity period used when
// IssueRequest.TTL is zero.
func WithDefaultTTL(d time.Duration) ProviderOption {
	return func(o *providerOptions) {
		o.defaultTTL = d
	}
}

// WithMaxTTL bounds every issued client certificate's validity period: a
// requested IssueRequest.TTL longer than this is capped to it, same as the
// issuing CA's own remaining validity always caps it. The zero value (the
// default) means no configured cap beyond the CA's own.
func WithMaxTTL(d time.Duration) ProviderOption {
	return func(o *providerOptions) {
		o.maxTTL = d
	}
}

// WithKeyProviderFactory injects the cakey.ProviderFactory this
// DefaultCertProvider resolves Key B material through, one tenant at a
// time, replacing the built-in per-tenant cakey.SoftwareProvider entirely
// (the CA key provider seam, go-qrdb#555). When set, no software key is
// ever generated or stored under prefixCertCA for any tenant: every Signer
// and Certificate call is routed to f.ProviderForTenant(tenantID), so an
// embedder (go-xkms) backing Key B with an HSM, TPM, or KMS plugs in with
// no glue code beyond cakey.Adapter, constructed fresh (or looked up) per
// tenant.
//
// f MUST resolve a DIFFERENT Provider -- its own key -- for each distinct
// tenantID, exactly as the built-in default does (its own storage prefix
// and Subject per tenant): per-tenant keys are REQUIRED, not merely
// supported (owner ruling, go-qrdb#591's review). A factory that returns
// the SAME Provider for every tenant makes IsTenantIssuer's binding
// vacuous -- see cakey.ProviderFactory's own doc comment for exactly why --
// which is also why this package deliberately offers no adapter from a
// single bare cakey.Provider into a ProviderFactory: that shortcut is the
// defect this option replaces, not a convenience it preserves.
//
// A nil f is refused by NewDefaultProvider's caller indirectly: passing a
// nil factory here is equivalent to not calling this option at all (the
// built-in per-tenant default remains active), because a configuration
// mistake should fail by keeping the safe default, not by panicking inside
// a functional option.
func WithKeyProviderFactory(f cakey.ProviderFactory) ProviderOption {
	return func(o *providerOptions) {
		o.keyProviderFactory = f
	}
}

// WithWatchService enables cache eviction on a deleted tenant's Key B CA:
// see WatchForDeletions, which the caller must still call explicitly (this
// option only stores w; it starts nothing). A nil w (the default) leaves
// eviction entirely event-driven-off -- the built-in per-tenant cache
// (perTenant) and any subscribed downstream cache (pkg/clienttrust.Pool,
// via InvalidationSource) age out only by their own TTLs, same as before
// this option existed.
//
// w is WatchPrefixer, not service.WatchServicer: this package depends only
// on pkg/watch rather than the whole pkg/service, which itself depends on
// pkg/bootstrap, which depends back on pkg/cert for the onboarding seam
// (bootstrap.ProvisionTenant) -- an import cycle service.WatchServicer would
// otherwise close. Any service.WatchServicer satisfies this implicitly.
func WithWatchService(w WatchPrefixer) ProviderOption {
	return func(o *providerOptions) {
		o.watchSvc = w
	}
}

// WithNewCAHook registers fn to run whenever this provider comes to hold a
// login CA certificate it has not held before: a tenant's CA minted on its
// first issuance, or loaded on first use after a restart. That is the moment
// the set CACertPEMs reports grows, so a client-auth trust pool built from it
// (pkg/clienttrust) must drop its cached widening: without the hook, a
// tenant onboarded a moment ago could not log in with its brand-new
// certificate until the pool's cache expired. fn runs outside the provider's
// locks, after the CA is cached, and must not block.
func WithNewCAHook(fn func()) ProviderOption {
	return func(o *providerOptions) {
		o.onNewCA = fn
	}
}

// WithAuditLogger attaches a compliance-grade audit logger (FedRAMP AU-2,
// PCI-DSS 10.2) so every revocation (RevokeCert) leaves an audit trail --
// cert.revoke -- regardless of which transport or internal caller reaches
// it; issuance (cert.issue) and enrolment (cert.enroll) are audited one
// layer up, by CertEnrollmentService, since that is the one place every
// transport's issue/enroll verb goes through. RevokeCert has no such single
// caller (the certs:revoke verb calls it directly on every transport), so
// this provider is where its own trail belongs. A nil logger disables
// emission, so the option is safe to apply unconditionally; this is also how
// a composer whose compliance sink does not exist yet at construction time
// (lifecycle.BuildServices) wires one in without waiting -- pass an
// audit.Deferred and Bind it later.
func WithAuditLogger(logger audit.AuditLogger) ProviderOption {
	return func(o *providerOptions) {
		o.audit = logger
	}
}

// WithTrustDomain names the cluster trust domain. Every client certificate the
// provider issues carries the tenant's SPIFFE URI SAN
// (spiffe://<tenantID>/user/<username>), under the tenant's own trust domain,
// and the provider refuses a tenant equal to trustDomain, which names no
// tenant (auth.TenantPrincipalURIs).
//
// It must be the same value the node's mTLS authenticator pins
// (auth.cluster_trust_domain), which refuses a certificate whose tenant is that
// trust domain.
//
// It is required to issue a client certificate: without it there is no SAN,
// a certificate without one names no tenant (auth.TenantFromClientCert), and
// IssueClientCert refuses with *SPIFFEIdentityError rather than mint one. A
// node passes auth.EffectiveClusterTrustDomain, which is never empty.
func WithTrustDomain(trustDomain string) ProviderOption {
	return func(o *providerOptions) {
		o.trustDomain = trustDomain
	}
}

// DefaultCertProvider implements CertProvider over the cakey.Provider seam.
// It never holds a CA private key itself: every signature is produced by
// resolving a cakey.Provider (the built-in per-tenant cakey.SoftwareProvider,
// persisted through kvStore exactly like the cluster CA (Key A) -- the
// replicated system store, with no additional per-node barrier wrap of its
// own; the barrier gates only whether this provider will touch key material
// at all (IsSealed), per the owner's CA-at-rest ruling -- or one resolved
// per tenant from an injected cakey.ProviderFactory, see
// WithKeyProviderFactory) and calling its Signer.
type DefaultCertProvider struct {
	// heldCAs is the set of login CA certificates (by SHA-256 of their DER)
	// this provider has cached; a certificate not yet in it fires onNewCA.
	// Guarded by mu.
	heldCAs map[[sha256.Size]byte]struct{}

	// onNewCA is WithNewCAHook's fn, or nil.
	onNewCA func()

	kvStore kvstore.MultiGuardedBatchKVStore
	barrier seal.EncryptionBarrier

	// keyProviderFactory, when non-nil, is an embedder-injected
	// cakey.ProviderFactory that replaces the built-in per-tenant default
	// entirely (WithKeyProviderFactory), resolved fresh for each tenant --
	// never a single Provider shared across all of them.
	keyProviderFactory cakey.ProviderFactory

	mu sync.RWMutex

	// perTenant holds, for each tenant resolved so far, keyed by tenant ID
	// and constructed lazily on first use, whether its records are in its
	// home store and the built-in cakey.SoftwareProvider over its Key B
	// material. Under an injected keyProviderFactory an entry carries the
	// routing decision only.
	//
	// Each entry also ages out on its own, independent of the watch-driven
	// eviction WatchForDeletions installs (go-qrdb#591's review, non-
	// blocking defense in depth): the watch is the fast, event-driven
	// path, but a watch that never fires -- the hub never delivered the
	// event, or a future teardown step this package has no visibility
	// into deletes the record some other way -- must not leave a deleted
	// tenant's key and certificate cached here forever. resolveKeyProvider
	// treats an entry older than perTenantRevalidateTTL as a miss and
	// rebuilds it from whatever the store currently holds.
	perTenant map[string]*perTenantEntry

	defaultTTL time.Duration

	// maxTTL bounds every issued client certificate's validity period,
	// including an explicit IssueRequest.TTL: a leaf is never issued for
	// longer than this regardless of what the caller asks for. See
	// WithMaxTTL.
	maxTTL time.Duration

	// trustDomain, when non-empty, is the SPIFFE trust domain every issued
	// client certificate's tenant SAN is minted under. See WithTrustDomain.
	trustDomain string

	// skippedTenantCAs counts every tenant CACertPEMs has skipped because
	// its own record failed to resolve or load (go-qrdb#591's review, B3):
	// a corrupt or unreadable tenant record withdraws only that tenant's
	// own anchor, never the whole call, and this is the counter an
	// operator or a test observes that withdrawal through. See
	// SkippedTenantCACount.
	skippedTenantCAs atomic.Int64

	// watchSvc, when non-nil (WithWatchService), is what WatchForDeletions
	// watches __sys:cert_ca/ and the tenant tombstone prefix through.
	watchSvc WatchPrefixer

	// invalidateHooks are called, in order, whenever a watched deletion
	// evicts a tenant's cached state (evictTenant's callers). Registered
	// by OnInvalidate -- pkg/clienttrust.Pool subscribes one from
	// SetKeyBProvider, so it rebuilds its trust snapshot without a deleted
	// tenant at once rather than at its next periodic refresh. Guarded by
	// mu, the same lock perTenant uses.
	invalidateHooks []func()

	// audit is the compliance audit sink RevokeCert records to
	// (WithAuditLogger). Nil disables emission.
	audit audit.AuditLogger

	// routing is the resolver isolated tenants' records are routed through
	// (SetStoreResolver), or nil, when every record is in kvStore.
	routing atomic.Pointer[storeRouting]

	// now is the clock perTenant entries are aged by against
	// perTenantRevalidateTTL; time.Now outside tests.
	now func() time.Time
}

// SkippedTenantCACount returns how many tenants CACertPEMs has skipped,
// cumulatively, because their own Key B CA record failed to resolve or
// load. A non-zero count is a signal worth alerting on -- it names a
// specific tenant's login CA as unreadable -- but it is never, by itself,
// a reason to refuse every OTHER tenant's.
func (p *DefaultCertProvider) SkippedTenantCACount() int64 {
	return p.skippedTenantCAs.Load()
}

// NewDefaultProvider creates a new DefaultCertProvider. kvStore must support
// atomic multi-key batch writes (kvstore.MultiGuardedBatchKVStore):
// cakey.NewSoftwareProvider, which every per-tenant Key B provider resolves
// through, requires it for the same reason the cluster CA
// (clusterca.NewProvider) does -- see cakey.SoftwareProvider's own doc
// comment. The provider must be initialized via Initialize and unsealed via
// Unseal before certificate operations are available.
func NewDefaultProvider(kvStore kvstore.MultiGuardedBatchKVStore, barrier seal.EncryptionBarrier, opts ...ProviderOption) *DefaultCertProvider {
	o := &providerOptions{
		defaultTTL: defaultTTL,
	}
	for _, fn := range opts {
		fn(o)
	}
	return &DefaultCertProvider{
		kvStore:            kvStore,
		barrier:            barrier,
		keyProviderFactory: o.keyProviderFactory,
		defaultTTL:         o.defaultTTL,
		maxTTL:             o.maxTTL,
		trustDomain:        o.trustDomain,
		watchSvc:           o.watchSvc,
		onNewCA:            o.onNewCA,
		audit:              o.audit,
		now:                time.Now,
	}
}

// Initialize resolves this provider's cakey.Provider for the system tenant
// (tenant.FromContext(ctx)) and ensures its CA key and certificate exist,
// generating them on first call. If CA material already exists it is loaded
// instead. For the built-in cakey.SoftwareProvider this calls its own
// Initialize (create-or-load, atomic via PutIfAbsent -- see
// cakey.SoftwareProvider.Initialize, which is what makes two callers racing
// a brand-new tenant's first-ever boot converge on exactly one CA rather
// than each writing its own candidate); a Provider resolved from an
// injected ProviderFactory (WithKeyProviderFactory) has no such distinction
// on the Provider interface, so it is proven ready by calling Signer
// directly -- an embedder's CA is established by construction, not by this
// call.
func (p *DefaultCertProvider) Initialize(ctx context.Context) error {
	if p.barrier.IsSealed() {
		return &BarrierSealedError{}
	}
	tenantID := tenant.FromContext(ctx)
	prov, entry, err := p.resolveKeyProvider(ctx, tenantID)
	if err != nil {
		return err
	}
	if sp, ok := prov.(*cakey.SoftwareProvider); ok {
		if err := sp.Initialize(ctx); err != nil {
			return mapProviderError(err)
		}
	} else if _, err := prov.Signer(ctx); err != nil {
		return mapProviderError(err)
	}
	cert, err := prov.Certificate(ctx)
	if err != nil {
		return mapProviderError(err)
	}
	return p.holdCA(ctx, tenantID, entry, cert)
}

// Unseal resolves this provider's cakey.Provider for the system tenant and
// loads its CA key and certificate. Unlike Initialize it never mints a new
// CA: for the built-in cakey.SoftwareProvider it calls the provider's own
// Unseal (load-only), which returns *CAKeyNotAvailableError (translated from
// cakey.NotInitializedError) when nothing has ever been established for this
// tenant, rather than silently generating a fresh key. An injected Provider
// has no such distinction, so it is proven ready the same way Initialize
// does.
func (p *DefaultCertProvider) Unseal(ctx context.Context) error {
	if p.barrier.IsSealed() {
		return &BarrierSealedError{}
	}
	tenantID := tenant.FromContext(ctx)
	prov, entry, err := p.resolveKeyProvider(ctx, tenantID)
	if err != nil {
		return err
	}
	if sp, ok := prov.(*cakey.SoftwareProvider); ok {
		if err := sp.Unseal(ctx); err != nil {
			return mapProviderError(err)
		}
	} else if _, err := prov.Signer(ctx); err != nil {
		return mapProviderError(err)
	}
	cert, err := prov.Certificate(ctx)
	if err != nil {
		return mapProviderError(err)
	}
	return p.holdCA(ctx, tenantID, entry, cert)
}

// perTenantEntry is one cached entry in DefaultCertProvider.perTenant: the
// built-in cakey.SoftwareProvider for a tenant, alongside when it was
// constructed, so resolveKeyProvider can revalidate it independent of
// watch-driven eviction (go-qrdb#591's review, non-blocking defense in
// depth; see perTenant's own field doc comment).
type perTenantEntry struct {
	// sp is the built-in provider over store. It is nil under an injected
	// keyProviderFactory, and for a homed tenant until a signing path has
	// resolved its home store (homeKeyProvider).
	sp *cakey.SoftwareProvider

	// store is the store sp was built over: the system store, or the
	// tenant's home store.
	store kvstore.MultiGuardedBatchKVStore

	// home reports whether the tenant's records are in its home store.
	home bool

	builtAt time.Time

	// published is a homed tenant's CA certificate as the system store is
	// known to hold it (prefixCertCAPub), or nil until it has been read or
	// written.
	published atomic.Pointer[x509.Certificate]
}

// noteHeldCA records that this provider now holds cert as a login CA, and
// fires WithNewCAHook's fn the first time this exact certificate (keyed by
// its SHA-256 DER fingerprint) is observed -- the moment a tenant's Key B CA,
// freshly minted or loaded after a restart, must be reflected in a
// client-facing trust pool (pkg/clienttrust). Deliberately caches no
// certificate of its own: CACertPEMs reads every tenant's CA straight from
// the store on every call (go-qrdb#560 review, non-blocking: a cached "most
// recently resolved" certificate would miss a runtime reseal, and could hand
// one tenant's caller a DIFFERENT tenant's certificate under concurrent
// resolution), so this is purely a seen-before set for the hook.
func (p *DefaultCertProvider) noteHeldCA(cert *x509.Certificate) {
	fp := sha256.Sum256(cert.Raw)
	p.mu.Lock()
	_, known := p.heldCAs[fp]
	if !known {
		if p.heldCAs == nil {
			p.heldCAs = make(map[[sha256.Size]byte]struct{})
		}
		p.heldCAs[fp] = struct{}{}
	}
	p.mu.Unlock()
	if !known && p.onNewCA != nil {
		p.onNewCA()
	}
}

// resolveKeyProvider returns the cakey.Provider that signs Key B material
// for tenantID: f.ProviderForTenant(tenantID) when WithKeyProviderFactory
// was used -- resolved FRESH for this specific tenant, never a value shared
// across tenants -- otherwise the built-in cakey.SoftwareProvider for
// tenantID, constructed lazily and cached in perTenant on first use, over
// the system store or, for a homed tenant, its home store. The tenant's
// entry is returned with it (nil under a factory), for holdCA.
//
// The common case -- tenantID already cached and not yet due for
// revalidation -- takes ONLY a read lock (go-qrdb#591's review, non-
// blocking; see route): an exclusive lock per call serialized every
// tenant's authentication through one lock for a map read that never
// writes anything. The slow path (construct and cache a new entry, or
// rebuild one older than perTenantRevalidateTTL) still takes the exclusive
// lock, and re-checks the map's freshness after acquiring it: another
// goroutine may have already won the race to rebuild tenantID's entry
// between this call's RUnlock and Lock.
func (p *DefaultCertProvider) resolveKeyProvider(ctx context.Context, tenantID string) (cakey.Provider, *perTenantEntry, error) {
	if p.keyProviderFactory != nil {
		prov, err := p.keyProviderFactory.ProviderForTenant(tenantID)
		return prov, nil, err
	}
	entry, err := p.route(ctx, tenantID)
	if err != nil {
		return nil, nil, err
	}
	if !entry.home {
		return entry.sp, entry, nil
	}
	sp, entry, err := p.homeKeyProvider(ctx, tenantID, entry)
	if err != nil {
		return nil, nil, err
	}
	return sp, entry, nil
}

// mapProviderError translates a cakey package error into the equivalent
// pkg/cert typed error, so CertProvider's callers see only this package's
// error vocabulary regardless of which cakey.Provider implementation signed
// (the built-in software one or an injected HSM/TPM/KMS adapter).
func mapProviderError(err error) error {
	var sealedErr *cakey.BarrierSealedError
	if errors.As(err, &sealedErr) {
		return &BarrierSealedError{}
	}
	var notInitErr *cakey.NotInitializedError
	if errors.As(err, &notInitErr) {
		return &CAKeyNotAvailableError{}
	}
	var storeErr *cakey.StoreError
	if errors.As(err, &storeErr) {
		return &StoreError{Op: storeErr.Op, Detail: storeErr.Detail, Err: storeErr.Err}
	}
	var genErr *cakey.GenerateError
	if errors.As(err, &genErr) {
		return &CertGenerateError{Err: genErr.Err}
	}
	return &CertGenerateError{Err: err}
}

// Seal destroys, for every tenant whose Key B uses the built-in software
// provider, its in-memory private key (cakey.SoftwareProvider.Seal). An
// injected provider (WithKeyProviderFactory) is never sealed here: this
// process holds none of its key material to destroy, and the barrier's own
// sealed state is what every subsequent Signer/Certificate call is checked
// against.
func (p *DefaultCertProvider) Seal() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, entry := range p.perTenant {
		if entry.sp != nil {
			entry.sp.Seal()
		}
	}
	return nil
}

// IssueClientCert signs a client certificate for req.PublicKey with
// req.TenantID's own Key B CA. It sets CN to req.Username and OU to
// req.TenantID, and is valid only for client authentication. It never
// generates or sees the client's private key.
//
// The certificate also carries the tenant-scoped SPIFFE URI SAN for the same
// tenant and username under the provider's trust domain (WithTrustDomain),
// built by auth.TenantPrincipalURIs, because the authenticator requires the
// OU and the SAN to agree (auth.TenantFromClientCert). The SAN is checked
// before anything is signed: no trust domain, or a SAN that would not parse
// back to exactly req.TenantID and req.Username — a path separator in either,
// or a trust domain that is not a bare host — is refused with
// *SPIFFEIdentityError, because the authenticator would refuse the
// certificate.
//
// With req.Delegation set the certificate is a delegation (pkg/auth/delegation):
// the grant rides in a critical extension, so a verifier that does not know it
// refuses the certificate outright, and the lifetime is the delegation's own
// (delegation.DefaultLifetime, at most delegation.MaxLifetime) rather than the
// provider's. A grant for another tenant, for the system tenant, or whose
// principal is not req.Username's is refused with *ValidationError.
func (p *DefaultCertProvider) IssueClientCert(ctx context.Context, req *IssueRequest) (*IssuedCert, error) {
	if err := validateIssueRequest(req); err != nil {
		return nil, err
	}

	// Derived before the CA key is touched, so a refused identity never costs
	// a signature and a certificate that would be refused is never minted.
	var extensions []pkix.Extension
	if req.Delegation != nil {
		ext, err := delegationExtension(req)
		if err != nil {
			return nil, err
		}
		extensions = []pkix.Extension{ext}
	}
	uris, err := p.clientSPIFFEURIs(req)
	if err != nil {
		return nil, err
	}

	caSigner, caCert, err := p.getCAMaterial(ctx, req.TenantID)
	if err != nil {
		return nil, err
	}

	serialNumber, err := generateSerial()
	if err != nil {
		return nil, &CertGenerateError{Err: err}
	}

	ttl := req.TTL
	switch {
	case req.Delegation != nil:
		ttl = delegationTTL(ttl)
	case ttl == 0:
		ttl = p.defaultTTL
	}
	// A leaf never outlives its issuer: the configured maximum (WithMaxTTL)
	// and the CA's own remaining validity both cap it, whichever is
	// shorter, regardless of what the caller asked for.
	if p.maxTTL > 0 && ttl > p.maxTTL {
		ttl = p.maxTTL
	}

	now := time.Now().UTC().Truncate(time.Second)
	notAfter := now.Add(ttl)
	if notAfter.After(caCert.NotAfter) {
		notAfter = caCert.NotAfter
	}
	clientTemplate := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:         req.Username,
			OrganizationalUnit: []string{req.TenantID},
		},
		NotBefore: now,
		NotAfter:  notAfter,
		KeyUsage:  x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageClientAuth,
		},
		URIs:            uris,
		ExtraExtensions: extensions,
	}

	clientCertDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, caCert, req.PublicKey, caSigner)
	if err != nil {
		return nil, &CertGenerateError{Err: err}
	}

	// Encode client cert PEM.
	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: clientCertDER,
	})

	serialHex := FormatSerial(serialNumber)

	// Persist the cert record.
	record := &CertRecord{
		Serial:     serialHex,
		Username:   req.Username,
		TenantID:   req.TenantID,
		IssuedAt:   now,
		ExpiresAt:  now.Add(ttl),
		Revoked:    false,
		Delegation: req.Delegation,
	}
	recordKey := prefixCerts + req.TenantID + "/" + serialHex
	recordBytes, err := json.Marshal(record)
	if err != nil {
		return nil, &CertGenerateError{Err: err}
	}
	store, _, err := p.recordStore(ctx, req.TenantID)
	if err != nil {
		return nil, err
	}
	if err := store.Put(ctx, recordKey, recordBytes); err != nil {
		return nil, &StoreError{Op: "put", Detail: recordKey, Err: err}
	}

	return &IssuedCert{
		CertPEM:  certPEM,
		Serial:   serialHex,
		NotAfter: clientTemplate.NotAfter,
	}, nil
}

// clientSPIFFEURIs returns the URI SANs a client certificate for req carries:
// exactly one tenant-scoped SPIFFE ID -- of kind user named for the certificate's CN, or for a
// delegation of kind svc named for the delegated principal, the SAN the
// authenticator requires a delegation's to be.
//
// The ID is round-tripped through the SAME parser the authenticator applies to
// a presented certificate (spiffe.ParseID, over the URL as x509 will encode
// it), and the result must equal what was asked for field by field. That makes
// "the SAN and the OU agree" a checked property rather than an assumption
// about how url.URL escapes a path: the OU carries req.TenantID verbatim, so a
// SAN that parses back to req.TenantID agrees with it by construction, and one
// that does not is never signed.
func (p *DefaultCertProvider) clientSPIFFEURIs(req *IssueRequest) ([]*url.URL, error) {
	if req.Delegation != nil {
		return tenantSPIFFEURIs(p.trustDomain, req.TenantID, spiffe.KindService, req.Delegation.Principal)
	}
	return ClientSPIFFEURIs(p.trustDomain, req.TenantID, req.Username)
}

// ClientSPIFFEURIs is the URI SANs a client certificate for username in
// tenantID would carry under trustDomain, or *SPIFFEIdentityError when the
// identity cannot survive the SPIFFE round trip -- in which case the
// provider would refuse to sign it. A caller that is about to write state on
// the strength of a later signature (tenant onboarding) asks it first, so a
// name that can never be certified is refused before anything is written.
// An empty trustDomain is refused: no certificate is issued without the SAN.
func ClientSPIFFEURIs(trustDomain, tenantID, username string) ([]*url.URL, error) {
	return tenantSPIFFEURIs(trustDomain, tenantID, spiffe.KindUser, username)
}

// tenantSPIFFEURIs is ClientSPIFFEURIs for an identity of any kind: the one
// SAN auth.TenantPrincipalURIs builds, its refusal typed as this package's.
func tenantSPIFFEURIs(trustDomain, tenantID, kind, name string) ([]*url.URL, error) {
	uris, err := auth.TenantPrincipalURIs(trustDomain, tenantID, kind, name)
	if err != nil {
		return nil, &SPIFFEIdentityError{
			TrustDomain: trustDomain,
			TenantID:    tenantID,
			Username:    name,
			Err:         err,
		}
	}
	return uris, nil
}

// RevokeCert marks a certificate serial as revoked by writing a revocation
// marker to KVStore and updating the CertRecord this node holds for it, if
// any.
//
// Revocation is DENY-BY-SERIAL: the marker is written whether or not this node
// holds a certificate record for the serial, because revoking a certificate
// issued by a peer — or one whose record has been pruned — is a real CA
// operation. The marker is the authoritative state that IsRevoked reads;
// CertRecord.Revoked is a display field updated best-effort beside it.
//
// It reports the outcome through RevokeResult rather than through the error
// return: both outcomes are successes, so reporting one of them as an error
// would tell a caller that a revocation failed when the marker is durable
// and enforcement is already reading it — and the case it would misreport is
// the deliberate revocation of a peer-issued serial, the very operation
// deny-by-serial exists for. A caller that saw an error would retry, alert
// and escalate over an operation that worked. See go-qrdb#287.
//
// BREAKING (go-qrdb#287): RevokeCert previously returned only error. It now
// returns (*RevokeResult, error). There were zero external consumers to
// migrate at the time of this change -- RevokeCert is referenced only by
// go-qrdb's own transports and handlers -- so the signature was changed
// directly rather than adding a parallel method, per the project's
// pre-publication breaking-change allowance.
//
// The same rule governs the reaping call below, where it is already applied: a
// completed revocation must not be reported as a failure, because the marker
// is already durable. An earlier draft returned *CertNotFoundError here and
// was reverted for contradicting that four lines later.
//
// The lookup happens BEFORE the marker is written, so a store failure during
// the lookup leaves no marker behind.
//
// Every revocation also reaps expired markers (ReapExpiredRevocations), which
// is what bounds the namespace without a background sweeper.
// emitAudit records a compliance-grade audit event (FedRAMP AU-2, PCI-DSS
// 10.2) for a Key B operation. It captures the acting identity (Subject +
// TenantID, when the call runs with one in context -- a first-boot or
// tenant-onboarding call frequently does not, and then Actor and the
// top-level TenantID are both empty), and is a no-op when no audit logger
// is configured. Emission is best-effort per the AuditLogger contract and
// never affects the certificate operation's outcome.
func (p *DefaultCertProvider) emitAudit(ctx context.Context, action, outcome string, attrs map[string]any) {
	if p.audit == nil {
		return
	}
	var actor, callerTenant string
	if id := auth.IdentityFromContext(ctx); id != nil {
		actor = id.Subject
		callerTenant = id.TenantID
	}
	// The resource is the certificate, "cert:{serial}", as for every other
	// audited object; a refusal before any serial exists is plain "cert".
	resource := "cert"
	if serial, ok := attrs["serial"].(string); ok && serial != "" {
		resource = "cert:" + serial
	}
	audit.EmitBestEffort(ctx, p.audit, "cert", audit.AuditEvent{
		Actor:      actor,
		TenantID:   callerTenant,
		Resource:   resource,
		Action:     action,
		Outcome:    outcome,
		Attributes: attrs,
	})
}

func (p *DefaultCertProvider) RevokeCert(ctx context.Context, serial string) (*RevokeResult, error) {
	if p.barrier.IsSealed() {
		return nil, &BarrierSealedError{}
	}

	if serial == "" {
		return nil, &ValidationError{Field: "serial", Reason: "must not be empty"}
	}

	// Locate the certificate record for this serial. This runs first so that
	// a scan failure aborts before anything is written.
	store, recordKey, record, err := p.findBySerial(ctx, serial)
	if err != nil {
		return nil, err
	}

	entry := revocationEntry{Serial: serial, RevokedAt: time.Now().UTC()}
	if record != nil {
		// Only a known certificate has a knowable expiry, and only a marker
		// with an expiry is ever reaped.
		entry.NotAfter = record.ExpiresAt
	}
	payload, err := json.Marshal(&entry)
	if err != nil {
		return nil, &StoreError{Op: "marshal", Detail: prefixCertsRevoked + serial, Err: err}
	}

	revokedKey := prefixCertsRevoked + serial
	if err := store.Put(ctx, revokedKey, payload); err != nil {
		return nil, &StoreError{Op: "put", Detail: revokedKey, Err: err}
	}

	outcome := RevokeOutcomeDenyBySerial
	if record != nil {
		outcome = RevokeOutcomeRecordUpdated
		record.Revoked = true
		updated, err := json.Marshal(record)
		if err != nil {
			return nil, &StoreError{Op: "marshal", Detail: recordKey, Err: err}
		}
		if err := store.Put(ctx, recordKey, updated); err != nil {
			return nil, &StoreError{Op: "put", Detail: recordKey, Err: err}
		}
	}

	// Best-effort: a reap failure must not turn a completed revocation into a
	// reported failure. The marker for this serial is already durable.
	if _, err := p.reapIn(ctx, store); err != nil {
		slog.Warn("qrdb/cert: reaping expired revocation markers failed",
			"error", err,
		)
	}

	// Audited unconditionally -- including deny-by-serial, where this node
	// holds no record at all: that is still a real revocation (the marker
	// above is durable either way), and FedRAMP AU-2/PCI-DSS 10.2 call for a
	// trail of it regardless of which case this was. recordTenant is empty
	// for deny-by-serial, since a serial this node holds no record for
	// names no tenant here (see findBySerial).
	var recordTenant string
	if record != nil {
		recordTenant = record.TenantID
	}
	p.emitAudit(ctx, "cert.revoke", audit.OutcomeSuccess, map[string]any{
		"serial":       serial,
		"record_found": record != nil,
		"tenant_id":    recordTenant,
	})

	return &RevokeResult{Serial: serial, Outcome: outcome}, nil
}

// findBySerial locates the issued certificate record for a serial and
// returns the store holding it and its key alongside it. A serial this node
// holds no record for yields (system store, "", nil, nil) -- not an error,
// because that is the deny-by-serial case RevokeCert exists to serve, and
// its marker is kept in the system store.
//
// The scan covers EVERY tenant whose records are in the system store because
// the caller supplies only a serial, and a serial carries no tenant. That is
// exactly why the result must reach the authorization layer: TenantForSerial
// is the only way a caller's claim on a serial can be checked at all. A
// homed tenant's records are in its home store, which is searched first
// when the caller is that tenant and by no other caller.
func (p *DefaultCertProvider) findBySerial(ctx context.Context, serial string) (kvstore.KVStore, string, *CertRecord, error) {
	if store, home, err := p.recordStore(ctx, tenant.FromContext(ctx)); err != nil {
		return nil, "", nil, err
	} else if home {
		recordKey, record, err := scanForSerial(ctx, store, serial)
		if err != nil || record != nil {
			return store, recordKey, record, err
		}
	}
	recordKey, record, err := scanForSerial(ctx, p.kvStore, serial)
	return p.kvStore, recordKey, record, err
}

// scanForSerial returns the certificate record for serial in store, with
// its key, or ("", nil, nil) when store holds none.
func scanForSerial(ctx context.Context, store kvstore.KVStore, serial string) (string, *CertRecord, error) {
	var (
		recordKey string
		record    *CertRecord
	)
	if err := store.Scan(ctx, prefixCerts, func(key string, value []byte) error {
		if record != nil || !hasSerialSuffix(key, serial) {
			return nil
		}
		var rec CertRecord
		if err := json.Unmarshal(value, &rec); err != nil {
			return nil // skip corrupt records
		}
		recordKey, record = key, &rec
		return nil
	}); err != nil {
		return "", nil, &StoreError{Op: "scan", Detail: prefixCerts, Err: err}
	}
	return recordKey, record, nil
}

// TenantForSerial reports which tenant owns the issued certificate bearing
// the given serial, and whether this node holds such a record at all.
//
// It is the seam that lets an authorization layer answer "is this serial the
// caller's to act on?", a question RevokeCert's signature cannot answer on its
// own. found == false is the deny-by-serial case: the serial names no tenant
// here, so no tenant-local caller can claim it (see
// permission.CertsRevokeCross).
//
// Reading the record is not itself privileged -- it returns a tenant name, not
// certificate material -- so it deliberately takes no authorization argument;
// the decision belongs to the caller that has an identity.
func (p *DefaultCertProvider) TenantForSerial(ctx context.Context, serial string) (tenantID string, found bool, err error) {
	if p.barrier.IsSealed() {
		return "", false, &BarrierSealedError{}
	}
	if serial == "" {
		return "", false, &ValidationError{Field: "serial", Reason: "must not be empty"}
	}
	_, _, record, err := p.findBySerial(ctx, serial)
	if err != nil {
		return "", false, err
	}
	if record == nil {
		return "", false, nil
	}
	return record.TenantID, true, nil
}

// IsRevoked checks whether a certificate serial is marked as revoked.
func (p *DefaultCertProvider) IsRevoked(ctx context.Context, serial string) (bool, error) {
	if p.barrier.IsSealed() {
		return false, &BarrierSealedError{}
	}

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

	// A homed caller's own markers are in its home store.
	store, home, err := p.recordStore(ctx, tenant.FromContext(ctx))
	if err != nil || !home {
		return false, err
	}
	val, err = store.Get(ctx, revokedKey)
	if err != nil {
		return false, &StoreError{Op: "get", Detail: revokedKey, Err: err}
	}
	return val != nil, nil
}

// ListCerts returns all issued certificate records for the given tenant.
func (p *DefaultCertProvider) ListCerts(ctx context.Context, tenantID string) ([]*CertRecord, error) {
	if p.barrier.IsSealed() {
		return nil, &BarrierSealedError{}
	}

	if tenantID == "" {
		return nil, &ValidationError{Field: "tenantID", Reason: "must not be empty"}
	}

	store, _, err := p.recordStore(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	prefix := prefixCerts + tenantID + "/"
	var records []*CertRecord
	if err := store.Scan(ctx, prefix, func(_ string, value []byte) error {
		var rec CertRecord
		if err := json.Unmarshal(value, &rec); err != nil {
			return nil // skip corrupt records
		}
		records = append(records, &rec)
		return nil
	}); err != nil {
		return nil, &StoreError{Op: "scan", Detail: prefix, Err: err}
	}

	if records == nil {
		records = []*CertRecord{}
	}
	return records, nil
}

// materialRecordSuffix is the KVStore record name a tenant's Key B CA
// private key is stored under (prefixCertCA + tenantID + "/" +
// materialRecordSuffix), cakey.SoftwareProvider's split "material" record.
// It mirrors cakey.SoftwareProvider's own unexported keyMaterial constant
// -- duplicated here, rather than imported, because it names a storage
// record shape the cakey package does not export, exactly as
// prefixCertCA already must stay in sync with cakey's own prefix
// convention. Nothing in this package Scans for it (see certRecordSuffix,
// below, for why): it names the record only for a direct, single-key Put
// in this package's own tests.
const materialRecordSuffix = "/material"

// certRecordSuffix is cakey.SoftwareProvider's companion record name to
// materialRecordSuffix: the public certificate half of the same atomic
// batch write. CACertPEMs' tenant-discovery scan (below) matches THIS
// suffix, not materialRecordSuffix, because the certificate is public
// data (pkg/cakey/software.go's own Seal doc comment) and never
// hard-confined (pkg/service/hard_confined_keys.go), where the material
// record is -- including from pkg/service.KVService.Scan's own
// owner-enumerates escape this provider's kvStore rides on. Every tenant
// with a material record has a certificate record too, written together
// in cakey's one atomic batch, so discovering tenant IDs from this
// sibling finds the identical set without ever asking the filtered Scan
// to surface a record it must refuse every other caller.
const certRecordSuffix = "/certificate"

// CACertPEMs returns the PEM-encoded Key B CA certificate for every tenant
// that has established one -- or, when WithKeyProviderFactory replaced the
// built-in per-tenant default, every tenant the factory names via its own
// TenantIDs, each resolved and loaded through ProviderForTenant exactly like
// the built-in path resolves its own. This is the seam the client-facing
// listeners' dynamic trust pool uses to trust every tenant's login CA (see
// docs/certificate-authority/architecture.md, "Key B trust on the
// client-facing listeners").
//
// The scan only discovers tenant IDs from the record layout; this method
// checks the barrier explicitly up front and refuses with
// *BarrierSealedError while sealed, rather than relying on the KVStore scan
// to fail on its own. In Raft mode the replicated store happens to be
// unreadable while sealed (encfs) and the scan would fail regardless; in
// every OTHER storage mode the plaintext records are reachable even while
// sealed, and a runtime reseal (see Seal) must make the dynamic trust pool
// drop Key B immediately -- caching this answer anywhere past that point,
// or reaching the store at all, would miss that transition (go-qrdb#560
// review, non-blocking: "after a runtime reseal, the pool must drop Key
// B"). See docs/certificate-authority/architecture.md's "while sealed, Key
// B isn't available, so the pool holds Key A only" rule; the returned error
// is the caller's (pkg/clienttrust.Pool's) signal to fall back rather than
// block.
func (p *DefaultCertProvider) CACertPEMs(ctx context.Context) ([][]byte, error) {
	if p.barrier.IsSealed() {
		return nil, &BarrierSealedError{}
	}

	var tenantIDs []string
	if p.keyProviderFactory != nil {
		ids, err := p.keyProviderFactory.TenantIDs(ctx)
		if err != nil {
			return nil, mapProviderError(err)
		}
		tenantIDs = ids
	} else {
		// A tenant whose records are in the system store has its CA under
		// prefixCertCA; a homed tenant's is published under prefixCertCAPub.
		for _, prefix := range []string{prefixCertCA, prefixCertCAPub} {
			if err := p.kvStore.Scan(ctx, prefix, func(key string, _ []byte) error {
				if !strings.HasSuffix(key, certRecordSuffix) {
					return nil
				}
				rel := strings.TrimPrefix(key, prefix)
				tenantID := strings.TrimSuffix(rel, certRecordSuffix)
				if tenantID == "" {
					return nil
				}
				tenantIDs = append(tenantIDs, tenantID)
				return nil
			}); err != nil {
				return nil, &StoreError{Op: "scan", Detail: prefix, Err: err}
			}
		}
	}

	// A single tenant's corrupt or unreadable record withdraws only that
	// tenant's own anchor from the widened pool -- logged and counted, not
	// propagated as this call's own error (go-qrdb#591's review, B3): the
	// alternative made one bad record fail Key B trust for EVERY tenant,
	// cascading into the dynamic trust pool's isRegisteredKeyBCA check
	// (pkg/clienttrust.Pool) answering "uncertain" forever, which in turn
	// refuses the static exemption Key A and operator/harness logins rely
	// on -- locking out principals that have nothing to do with the
	// broken tenant at all.
	out := make([][]byte, 0, len(tenantIDs))
	seen := make(map[string]struct{}, len(tenantIDs))
	var skipped int
	for _, tenantID := range tenantIDs {
		if _, dup := seen[tenantID]; dup {
			continue
		}
		seen[tenantID] = struct{}{}
		cert, err := p.tenantCACert(ctx, tenantID)
		if err != nil {
			skipped++
			slog.Warn("qrdb/cert: skipping a tenant's Key B CA in CACertPEMs; its own anchor is withdrawn, every other tenant's is not",
				"tenant_id", tenantID, "error", err)
			continue
		}
		out = append(out, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}))
	}
	if skipped > 0 {
		p.skippedTenantCAs.Add(int64(skipped))
	}
	return out, nil
}

// IsTenantIssuer reports whether issuer is tenantID's registered Key B CA
// certificate: byte-identical DER, not merely matching Subject fields. It is
// the seam the mTLS authenticator's issuer-tenant binding check uses (see
// auth.IssuerVerifier, implemented by pkg/clienttrust.Pool) so a leaf naming
// tenantID is refused unless the CA that actually verified it is tenantID's
// own registered CA -- the enforcement, at authentication time, of the
// separate-trust-domains rule that every tenant's Key B CA being trusted on
// one shared client-facing listener would otherwise let a leaf defeat
// simply by naming a foreign tenant in its Subject OU.
//
// This is LOAD-ONLY: a tenant with no established CA answers (false, nil),
// never *CAKeyNotAvailableError and never a newly-minted CA. It is called
// on the authentication path for whatever tenant a presented leaf CLAIMS to
// be -- including tenants that do not exist -- so letting it auto-create
// (as prov.Certificate alone would, via cakey.SoftwareProvider.material's
// create-or-load) turned every such probe into a CA-creation side effect
// (go-qrdb#560 review, B2-5).
//
// An empty tenantID is folded to the system tenant, matching every other
// tenant-scoped accessor in this package. A nil issuer is never anyone's
// CA.
func (p *DefaultCertProvider) IsTenantIssuer(ctx context.Context, tenantID string, issuer *x509.Certificate) (bool, error) {
	if issuer == nil {
		return false, nil
	}
	if tenantID == "" {
		tenantID = tenantid.System
	}
	if p.barrier.IsSealed() {
		return false, &BarrierSealedError{}
	}

	registered, err := p.tenantCACert(ctx, tenantID)
	if err != nil {
		var notAvailable *CAKeyNotAvailableError
		if errors.As(err, &notAvailable) {
			// No CA has ever been established for this tenant: it cannot
			// be anyone's issuer. This is the load-only answer --
			// Certificate would have silently minted one just to answer
			// this probe.
			return false, nil
		}
		return false, err
	}
	return bytes.Equal(registered.Raw, issuer.Raw), nil
}

// loadOnlyCertificate resolves prov's CA certificate WITHOUT ever creating
// one AND WITHOUT loading or decoding key material, so a trust decision
// (CACertPEMs' tenant enumeration, IsTenantIssuer's issuer comparison) never
// touches a private key it has no reason to. For the built-in
// cakey.SoftwareProvider this calls CertificateOnly -- cert-only, load-only
// -- never Certificate (which would auto-initialize a brand-new CA just to
// answer a probe against a tenant with no established CA) and never Unseal
// (which loads the key record into memory alongside the certificate one,
// on every call). A Provider resolved from an injected ProviderFactory
// (WithKeyProviderFactory) has no such distinction -- its CA is established
// by construction, not by this call,
// and this package never holds or parses its key material either way -- so
// Certificate is called directly, as every other accessor on that path
// already does.
func (p *DefaultCertProvider) loadOnlyCertificate(ctx context.Context, prov cakey.Provider) (*x509.Certificate, error) {
	if sp, ok := prov.(*cakey.SoftwareProvider); ok {
		cert, err := sp.CertificateOnly(ctx)
		if err != nil {
			return nil, mapProviderError(err)
		}
		return cert, nil
	}
	cert, err := prov.Certificate(ctx)
	if err != nil {
		return nil, mapProviderError(err)
	}
	return cert, nil
}

// getCAMaterial resolves tenantID's cakey.Provider and returns its signer
// and certificate. Unlike the pre-convergence version, the caller never sees
// raw key bytes: every signature is produced by calling the returned
// crypto.Signer. Signer auto-initializes the built-in cakey.SoftwareProvider
// on first use (create-or-load, atomic via PutIfAbsent -- see
// cakey.SoftwareProvider.Initialize), matching IssueClientCert's existing
// auto-init-on-first-issuance behavior.
//
// The certificate returned is prov.Certificate's OWN answer for tenantID,
// read directly -- this package caches no certificate of its own (see
// noteHeldCA). Two different tenants' calls can interleave (this is called
// from IssueClientCert and ExportPKCS12, neither of which holds any lock
// across the call), so returning anything other than THIS resolution's own
// answer would risk handing one tenant's caller a DIFFERENT tenant's CA
// certificate (go-qrdb#560 review, non-blocking). noteHeldCA is still called,
// for WithNewCAHook's benefit, but its bookkeeping is never what this
// function returns.
func (p *DefaultCertProvider) getCAMaterial(ctx context.Context, tenantID string) (crypto.Signer, *x509.Certificate, error) {
	if p.barrier.IsSealed() {
		return nil, nil, &BarrierSealedError{}
	}

	prov, entry, err := p.resolveKeyProvider(ctx, tenantID)
	if err != nil {
		return nil, nil, err
	}
	signer, err := prov.Signer(ctx)
	if err != nil {
		return nil, nil, mapProviderError(err)
	}
	cert, err := prov.Certificate(ctx)
	if err != nil {
		return nil, nil, mapProviderError(err)
	}
	if err := p.holdCA(ctx, tenantID, entry, cert); err != nil {
		return nil, nil, err
	}
	return signer, cert, nil
}

// generateSerial returns a cryptographically random serial number for
// X.509 certificates.
func generateSerial() (*big.Int, error) {
	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), serialBitSize))
	if err != nil {
		return nil, err
	}
	return serialNumber, nil
}

// validateIssueRequest validates the fields of an IssueRequest.
func validateIssueRequest(req *IssueRequest) error {
	if req == nil {
		return &ValidationError{Field: "request", Reason: "must not be nil"}
	}
	if req.Username == "" {
		return &ValidationError{Field: "username", Reason: "must not be empty"}
	}
	if req.TenantID == "" {
		return &ValidationError{Field: "tenantID", Reason: "must not be empty"}
	}
	if req.TTL < 0 {
		return &ValidationError{Field: "ttl", Reason: "must not be negative"}
	}
	if req.PublicKey == nil {
		return &ValidationError{Field: "publicKey", Reason: "must not be nil"}
	}
	return CheckClientKey(req.PublicKey)
}

// hasSerialSuffix reports whether a key path ends with "/" + serial.
func hasSerialSuffix(key, serial string) bool {
	suffix := "/" + serial
	if len(key) < len(suffix) {
		return false
	}
	return key[len(key)-len(suffix):] == suffix
}
