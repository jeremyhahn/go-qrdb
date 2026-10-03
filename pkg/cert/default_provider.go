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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"math/big"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"

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
	defaultTTL  time.Duration
	trustDomain string
	keyProvider cakey.Provider
}

// WithDefaultTTL sets the default certificate validity period used when
// GenerateRequest.TTL is zero.
func WithDefaultTTL(d time.Duration) ProviderOption {
	return func(o *providerOptions) {
		o.defaultTTL = d
	}
}

// WithKeyProvider injects the cakey.Provider this DefaultCertProvider signs
// through, replacing the built-in per-tenant cakey.SoftwareProvider entirely
// (the CA key provider seam, go-qrdb#555). When set, no software key is ever
// generated or stored under prefixCertCA for any tenant: every Signer and
// Certificate call is routed to p, so an embedder (go-xkms) backing Key B
// with an HSM, TPM, or KMS plugs in with no glue code beyond cakey.Adapter.
//
// A nil p is refused by NewDefaultProvider's caller indirectly: passing a nil
// Provider here is equivalent to not calling this option at all (the
// built-in per-tenant default remains active), because a configuration
// mistake should fail by keeping the safe default, not by panicking inside a
// functional option.
func WithKeyProvider(p cakey.Provider) ProviderOption {
	return func(o *providerOptions) {
		o.keyProvider = p
	}
}

// WithTrustDomain makes every client certificate the provider issues carry a
// tenant-scoped SPIFFE URI SAN under trustDomain
// (spiffe://<trust-domain>/tenant/<tenantID>/user/<username>), so the
// authenticator's pinned-trust-domain derivation is the one that resolves the
// certificate's tenant (go-qrdb#519).
//
// It must be the same value the node's mTLS authenticator pins
// (auth.spiffe_trust_domain): a certificate minted under one trust domain and
// presented to a node pinned to another is refused, which is the control the
// pin exists to provide.
//
// An empty trustDomain is the default and emits no SAN at all, so a deployment
// that has not configured a trust domain issues certificates byte-for-byte as
// it did before; their tenant resolves through the Subject OU fallback.
func WithTrustDomain(trustDomain string) ProviderOption {
	return func(o *providerOptions) {
		o.trustDomain = trustDomain
	}
}

// DefaultCertProvider implements CertProvider over the cakey.Provider seam.
// It never holds a CA private key itself: every signature is produced by
// resolving a cakey.Provider (the built-in per-tenant cakey.SoftwareProvider,
// barrier-encrypted at rest and persisted through kvStore, or an injected
// one -- see WithKeyProvider) and calling its Signer. caCert/caCertPEM cache
// only the CA's own certificate, which is public data and is deliberately
// still served while sealed (see CACertPEM).
type DefaultCertProvider struct {
	kvStore kvstore.MultiGuardedBatchKVStore
	barrier seal.EncryptionBarrier

	// keyProvider, when non-nil, is an embedder-injected cakey.Provider that
	// replaces the built-in per-tenant default entirely (WithKeyProvider).
	keyProvider cakey.Provider

	mu sync.RWMutex

	// perTenant holds the built-in cakey.SoftwareProvider for each tenant
	// that has resolved Key B material, keyed by tenant ID, constructed
	// lazily on first use. Empty and never consulted when keyProvider is
	// injected.
	perTenant map[string]*cakey.SoftwareProvider

	// caCert/caCertPEM cache the most recently resolved CA certificate --
	// public data, read by CACertPEM without a barrier check and never wiped
	// by Seal.
	caCert    *x509.Certificate
	caCertPEM []byte

	// tenantInitLocks holds one *sync.Mutex per tenant ID (lazily created,
	// never removed), serializing every call into that tenant's built-in
	// cakey.SoftwareProvider that could create its CA key for the first
	// time. cakey.SoftwareProvider.Initialize's own existence check
	// (GET-then-PUT-if-absent) is not atomic against a second concurrent
	// caller that resolved the SAME *SoftwareProvider instance
	// (resolveKeyProvider caches one per tenant): two goroutines racing
	// Initialize for a brand-new tenant could both observe "nothing
	// exists" and both generate and persist a CA key, with whichever Put
	// lands last silently winning over in-memory state the other caller
	// already cached.
	//
	// This is a pkg/cert-side mitigation, not a pkg/cakey one -- the owning
	// fix (an atomic create-if-absent primitive) lands in
	// pkg/cakey/software.go under go-qrdb#560's companion lane
	// (lane/ca-keys-replicated); until this package rebases onto it, every
	// entry point that can reach SoftwareProvider.Initialize (Initialize,
	// Unseal, and getCAMaterial's Signer call, which auto-initializes via
	// material() on first use) takes this lock first. An injected Provider
	// (WithKeyProvider) has no such race -- its CA is established by
	// construction -- so it is never routed through this lock.
	tenantInitLocks sync.Map

	defaultTTL time.Duration

	// trustDomain, when non-empty, is the SPIFFE trust domain every issued
	// client certificate's tenant SAN is minted under. See WithTrustDomain.
	trustDomain string
}

// NewDefaultProvider creates a new DefaultCertProvider. The provider
// must be initialized via Initialize and unsealed via Unseal before
// certificate operations are available.
func NewDefaultProvider(kvStore kvstore.MultiGuardedBatchKVStore, barrier seal.EncryptionBarrier, opts ...ProviderOption) *DefaultCertProvider {
	o := &providerOptions{
		defaultTTL: defaultTTL,
	}
	for _, fn := range opts {
		fn(o)
	}
	return &DefaultCertProvider{
		kvStore:     kvStore,
		barrier:     barrier,
		keyProvider: o.keyProvider,
		defaultTTL:  o.defaultTTL,
		trustDomain: o.trustDomain,
	}
}

// Initialize resolves this provider's cakey.Provider for the system tenant
// (tenant.FromContext(ctx)) and ensures its CA key and certificate exist,
// generating them on first call. If CA material already exists it is loaded
// instead. For the built-in cakey.SoftwareProvider this calls its own
// Initialize (create-or-load); an injected Provider (WithKeyProvider) has no
// such distinction on the Provider interface, so it is proven ready by
// calling Signer directly -- an embedder's CA is established by construction,
// not by this call.
func (p *DefaultCertProvider) Initialize(ctx context.Context) error {
	if p.barrier.IsSealed() {
		return &BarrierSealedError{}
	}
	tenantID := tenant.FromContext(ctx)
	prov, err := p.resolveKeyProvider(tenantID)
	if err != nil {
		return err
	}
	if err := p.tenantInitLocked(prov, tenantID, func() error {
		if sp, ok := prov.(*cakey.SoftwareProvider); ok {
			return sp.Initialize(ctx)
		}
		_, err := prov.Signer(ctx)
		return err
	}); err != nil {
		return mapProviderError(err)
	}
	return p.warmCertCache(ctx, prov)
}

// tenantInitLocked serializes fn against every other call that could
// create tenantID's built-in cakey.SoftwareProvider CA key for the first
// time -- see tenantInitLocks. prov is consulted only to decide whether
// locking applies: an injected Provider (WithKeyProvider) runs fn
// unlocked.
func (p *DefaultCertProvider) tenantInitLocked(prov cakey.Provider, tenantID string, fn func() error) error {
	if _, ok := prov.(*cakey.SoftwareProvider); !ok {
		return fn()
	}
	v, _ := p.tenantInitLocks.LoadOrStore(tenantID, &sync.Mutex{})
	lock := v.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	return fn()
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
	prov, err := p.resolveKeyProvider(tenantID)
	if err != nil {
		return err
	}
	if err := p.tenantInitLocked(prov, tenantID, func() error {
		if sp, ok := prov.(*cakey.SoftwareProvider); ok {
			return sp.Unseal(ctx)
		}
		_, err := prov.Signer(ctx)
		return err
	}); err != nil {
		return mapProviderError(err)
	}
	return p.warmCertCache(ctx, prov)
}

// warmCertCache reads prov's certificate and caches it (and its PEM
// encoding) on p, so CACertPEM can keep serving public data without a
// barrier check or a call into prov.
func (p *DefaultCertProvider) warmCertCache(ctx context.Context, prov cakey.Provider) error {
	cert, err := prov.Certificate(ctx)
	if err != nil {
		return mapProviderError(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	p.mu.Lock()
	p.caCert = cert
	p.caCertPEM = certPEM
	p.mu.Unlock()
	return nil
}

// resolveKeyProvider returns the cakey.Provider that signs Key B material
// for tenantID: the injected keyProvider when WithKeyProvider was used
// (shared across every tenant, never persisting or generating a software
// key), otherwise the built-in cakey.SoftwareProvider for tenantID,
// constructed lazily and cached in perTenant on first use.
func (p *DefaultCertProvider) resolveKeyProvider(tenantID string) (cakey.Provider, error) {
	if p.keyProvider != nil {
		return p.keyProvider, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if sp, ok := p.perTenant[tenantID]; ok {
		return sp, nil
	}
	if p.perTenant == nil {
		p.perTenant = make(map[string]*cakey.SoftwareProvider)
	}
	subject := pkix.Name{
		CommonName:         "qrdb-ca",
		OrganizationalUnit: []string{tenantID},
	}
	sp := cakey.NewSoftwareProvider(p.kvStore, p.barrier, prefixCertCA+tenantID+"/", subject, caTTL)
	p.perTenant[tenantID] = sp
	return sp, nil
}

// mapProviderError translates a cakey package error into the equivalent
// pkg/cert typed error, so CertProvider's callers see only this package's
// error vocabulary regardless of which cakey.Provider implementation signed
// (the built-in software one or an injected HSM/TPM/KMS adapter).
func mapProviderError(err error) error {
	switch e := err.(type) {
	case *cakey.BarrierSealedError:
		return &BarrierSealedError{}
	case *cakey.NotInitializedError:
		return &CAKeyNotAvailableError{}
	case *cakey.StoreError:
		return &StoreError{Op: e.Op, Detail: e.Detail, Err: e.Err}
	case *cakey.GenerateError:
		return &CertGenerateError{Err: e.Err}
	default:
		return &CertGenerateError{Err: err}
	}
}

// Seal clears the cached CA certificate and, for every tenant whose Key B
// uses the built-in software provider, destroys its in-memory private key
// (cakey.SoftwareProvider.Seal). An injected provider (WithKeyProvider) is
// never sealed here: this process holds none of its key material to
// destroy, and the barrier's own sealed state is what every subsequent
// Signer/Certificate call is checked against.
func (p *DefaultCertProvider) Seal() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, sp := range p.perTenant {
		sp.Seal()
	}
	p.caCert = nil
	// caCertPEM is public data and does not need zeroing.
	return nil
}

// GenerateClientCert creates a client certificate signed by the CA.
// The certificate uses ECDSA P-256, sets CN to req.Username and OU to
// req.TenantID, and is valid only for client authentication.
//
// When the provider was built WithTrustDomain, the certificate also carries
// the tenant-scoped SPIFFE URI SAN for the same tenant and username, so the
// authenticator derives its tenant from the pinned SAN rather than the OU.
// The SAN is checked before anything is signed: a SAN that would not parse
// back to exactly req.TenantID and req.Username — a path separator in either,
// or a trust domain that is not a bare host — is refused with
// *SPIFFEIdentityError, because a certificate whose SAN and OU disagree
// authenticates as different tenants depending on which node's pin is set.
func (p *DefaultCertProvider) GenerateClientCert(ctx context.Context, req *GenerateRequest) (*CertBundle, error) {
	if err := validateGenerateRequest(req); err != nil {
		return nil, err
	}

	// Derived before the CA key is touched, so a refused identity never costs
	// a signature and a certificate that would be refused is never minted.
	uris, err := p.clientSPIFFEURIs(req)
	if err != nil {
		return nil, err
	}

	caSigner, caCert, err := p.getCAMaterial(ctx, req.TenantID)
	if err != nil {
		return nil, err
	}

	// Generate client key pair.
	clientPrivKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, &CertGenerateError{Err: err}
	}

	serialNumber, err := generateSerial()
	if err != nil {
		return nil, &CertGenerateError{Err: err}
	}

	ttl := req.TTL
	if ttl == 0 {
		ttl = p.defaultTTL
	}

	now := time.Now().UTC()
	clientTemplate := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:         req.Username,
			OrganizationalUnit: []string{req.TenantID},
		},
		NotBefore: now,
		NotAfter:  now.Add(ttl),
		KeyUsage:  x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageClientAuth,
		},
		URIs: uris,
	}

	clientCertDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, caCert, &clientPrivKey.PublicKey, caSigner)
	if err != nil {
		return nil, &CertGenerateError{Err: err}
	}

	// Encode client cert PEM.
	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: clientCertDER,
	})

	// Encode client key PEM.
	clientKeyDER, err := x509.MarshalECPrivateKey(clientPrivKey)
	if err != nil {
		return nil, &CertGenerateError{Err: err}
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: clientKeyDER,
	})
	secure.Wipe(clientKeyDER)

	serialHex := FormatSerial(serialNumber)

	// Persist the cert record.
	record := &CertRecord{
		Serial:    serialHex,
		Username:  req.Username,
		TenantID:  req.TenantID,
		IssuedAt:  now,
		ExpiresAt: now.Add(ttl),
		Revoked:   false,
	}
	recordKey := prefixCerts + req.TenantID + "/" + serialHex
	recordBytes, err := json.Marshal(record)
	if err != nil {
		return nil, &CertGenerateError{Err: err}
	}
	if err := p.kvStore.Put(ctx, recordKey, recordBytes); err != nil {
		return nil, &StoreError{Op: "put", Detail: recordKey, Err: err}
	}

	return &CertBundle{
		CertPEM: certPEM,
		KeyPEM:  keyPEM,
		Serial:  serialHex,
	}, nil
}

// clientSPIFFEURIs returns the URI SANs a client certificate for req carries:
// none when no trust domain is configured, otherwise exactly one tenant-scoped
// SPIFFE ID of kind user, named for the certificate's CN.
//
// The ID is round-tripped through the SAME parser the authenticator applies to
// a presented certificate (spiffe.ParseID, over the URL as x509 will encode
// it), and the result must equal what was asked for field by field. That makes
// "the SAN and the OU agree" a checked property rather than an assumption
// about how url.URL escapes a path: the OU carries req.TenantID verbatim, so a
// SAN that parses back to req.TenantID agrees with it by construction, and one
// that does not is never signed.
func (p *DefaultCertProvider) clientSPIFFEURIs(req *GenerateRequest) ([]*url.URL, error) {
	if p.trustDomain == "" {
		return nil, nil
	}
	want := spiffe.ID{
		TrustDomain: p.trustDomain,
		TenantID:    req.TenantID,
		Kind:        spiffe.KindUser,
		Name:        req.Username,
	}
	refuse := func(reason string) error {
		return &SPIFFEIdentityError{
			TrustDomain: p.trustDomain,
			TenantID:    req.TenantID,
			Username:    req.Username,
			Reason:      reason,
		}
	}
	encoded := want.String()
	if encoded == "" {
		return nil, refuse("the identity is not a valid tenant SPIFFE ID")
	}
	u, err := url.Parse(encoded)
	if err != nil {
		return nil, refuse("the SPIFFE ID does not parse as a URI")
	}
	got, ok, err := spiffe.ParseID(u)
	if err != nil || !ok {
		return nil, refuse("the SPIFFE ID does not parse back as a tenant identity")
	}
	if got != want {
		return nil, refuse("the SPIFFE ID parses back to a different identity than the Subject names")
	}
	return []*url.URL{u}, nil
}

// ExportPKCS12 exports a certificate and private key as a PKCS#12 archive.
// This requires the software.sslmate.com/src/go-pkcs12 module. If the
// module is not available, this method returns a CertExportError.
//
// The certificate's serial is checked against revocation state before
// encoding, and export is refused with *CertRevokedError when it is
// revoked. There is no override: a caller that wants a forensic or
// archival export of a revoked bundle needs that added deliberately, not
// defaulted. This is an OPERATOR SIGNAL, not a containment control -- the
// caller already supplied certPEM and keyPEM, so refusing the export
// withholds nothing it did not already have. Its value is that the system
// stops silently packaging a credential it will not honour anywhere in the
// cluster. See go-qrdb#287.
//
// PKCS#12 export is delegated to the encodePKCS12 function, which can be
// replaced by builds that include a PKCS#12 encoder.
func (p *DefaultCertProvider) ExportPKCS12(ctx context.Context, certPEM, keyPEM []byte, passphrase string) ([]byte, error) {
	if p.barrier.IsSealed() {
		return nil, &BarrierSealedError{}
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, &CertExportError{Err: &ValidationError{Field: "certPEM", Reason: "invalid PEM data"}}
	}
	parsedCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, &CertExportError{Err: err}
	}

	serial := FormatSerial(parsedCert.SerialNumber)
	revoked, err := p.IsRevoked(ctx, serial)
	if err != nil {
		return nil, err
	}
	if revoked {
		return nil, &CertRevokedError{Serial: serial}
	}

	// The CA bundled into the archive is parsedCert's OWN issuing tenant's
	// Key B CA, resolved through getCAMaterial directly -- never p.caCertPEM,
	// the single shared "most recently resolved" cache CACertPEM also reads.
	// Reading that shared field here would bundle whichever tenant's CA
	// happened to resolve most recently across the whole provider, not
	// necessarily certPEM's own tenant (go-qrdb#560 review, non-blocking,
	// "the same way" as getCAMaterial's own fix).
	tenantID := tenant.SystemTenantID
	if len(parsedCert.Subject.OrganizationalUnit) > 0 && parsedCert.Subject.OrganizationalUnit[0] != "" {
		tenantID = parsedCert.Subject.OrganizationalUnit[0]
	}
	_, caCert, err := p.getCAMaterial(ctx, tenantID)
	if err != nil {
		return nil, &CertExportError{Err: err}
	}
	caCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw})

	return encodePKCS12(certPEM, keyPEM, caCertPEM, passphrase)
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
func (p *DefaultCertProvider) RevokeCert(ctx context.Context, serial string) (*RevokeResult, error) {
	if p.barrier.IsSealed() {
		return nil, &BarrierSealedError{}
	}

	if serial == "" {
		return nil, &ValidationError{Field: "serial", Reason: "must not be empty"}
	}

	// Locate the certificate record for this serial. This runs first so that
	// a scan failure aborts before anything is written.
	recordKey, record, err := p.findBySerial(ctx, serial)
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
	if err := p.kvStore.Put(ctx, revokedKey, payload); err != nil {
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
		if err := p.kvStore.Put(ctx, recordKey, updated); err != nil {
			return nil, &StoreError{Op: "put", Detail: recordKey, Err: err}
		}
	}

	// Best-effort: a reap failure must not turn a completed revocation into a
	// reported failure. The marker for this serial is already durable.
	if _, err := p.ReapExpiredRevocations(ctx); err != nil {
		slog.Warn("qrdb/cert: reaping expired revocation markers failed",
			"error", err,
		)
	}

	return &RevokeResult{Serial: serial, Outcome: outcome}, nil
}

// findBySerial locates the issued certificate record for a serial and
// returns its store key alongside it. A serial this node holds no record for
// yields ("", nil, nil) -- not an error, because that is the deny-by-serial
// case RevokeCert exists to serve.
//
// The scan covers EVERY tenant because the caller supplies only a serial, and
// a serial carries no tenant. That is exactly why the result must reach the
// authorization layer: TenantForSerial is the only way a caller's claim on a
// serial can be checked at all.
func (p *DefaultCertProvider) findBySerial(ctx context.Context, serial string) (string, *CertRecord, error) {
	var (
		recordKey string
		record    *CertRecord
	)
	if err := p.kvStore.Scan(ctx, prefixCerts, func(key string, value []byte) error {
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
	_, record, err := p.findBySerial(ctx, serial)
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

	prefix := prefixCerts + tenantID + "/"
	var records []*CertRecord
	if err := p.kvStore.Scan(ctx, prefix, func(_ string, value []byte) error {
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

// CACertPEM returns the PEM-encoded CA certificate. This is public data
// and does not require the barrier to be unsealed.
func (p *DefaultCertProvider) CACertPEM() []byte {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]byte, len(p.caCertPEM))
	copy(out, p.caCertPEM)
	return out
}

// certPEMRecordSuffix is the KVStore record name a tenant's Key B CA
// certificate is stored under (prefixCertCA + tenantID + "/" +
// certPEMRecordSuffix), mirroring cakey.SoftwareProvider's own internal
// layout for the same prefix: the record holds the certificate's raw DER,
// not PEM, exactly as cakey's own certMaterial record does. It is duplicated
// here, rather than imported, because it names a storage record shape the
// cakey package does not export -- the two must stay in sync, exactly as
// prefixCertCA already must.
const certPEMRecordSuffix = "/certificate"

// CACertPEMs returns the PEM-encoded Key B CA certificate for every tenant
// that has established one -- or, when WithKeyProvider replaced the built-in
// per-tenant default, the single injected provider's certificate. This is
// the seam the client-facing listeners' dynamic trust pool uses to trust
// every tenant's login CA (see docs/certificate-authority/architecture.md,
// "Key B trust on the client-facing listeners").
//
// Every record scanned here is public data stored in plaintext (never
// barrier-encrypted, like CACertPEM) -- but this method checks the barrier
// explicitly anyway, and refuses with *BarrierSealedError while sealed,
// rather than relying on the KVStore scan to fail on its own. In Raft mode
// the replicated store happens to be unreadable while sealed (encfs) and
// the scan would fail regardless; in every OTHER storage mode the plaintext
// records are reachable even while sealed, and a runtime reseal (see Seal)
// must make the dynamic trust pool drop Key B immediately -- caching this
// answer anywhere past that point, or reaching the store at all, would miss
// that transition (go-qrdb#560 review, non-blocking: "after a runtime
// reseal, the pool must drop Key B"). See
// docs/certificate-authority/architecture.md's "while sealed, Key B isn't
// available, so the pool holds Key A only" rule; the returned error is the
// caller's (pkg/clienttrust.Pool's) signal to fall back rather than block.
func (p *DefaultCertProvider) CACertPEMs(ctx context.Context) ([][]byte, error) {
	if p.barrier.IsSealed() {
		return nil, &BarrierSealedError{}
	}
	if p.keyProvider != nil {
		cert, err := p.keyProvider.Certificate(ctx)
		if err != nil {
			return nil, mapProviderError(err)
		}
		return [][]byte{pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})}, nil
	}

	var out [][]byte
	if err := p.kvStore.Scan(ctx, prefixCertCA, func(key string, value []byte) error {
		if !strings.HasSuffix(key, certPEMRecordSuffix) {
			return nil
		}
		cert, err := x509.ParseCertificate(value)
		if err != nil {
			return &StoreError{Op: "scan", Detail: key, Err: err}
		}
		out = append(out, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}))
		return nil
	}); err != nil {
		return nil, &StoreError{Op: "scan", Detail: prefixCertCA, Err: err}
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

	prov, err := p.resolveKeyProvider(tenantID)
	if err != nil {
		return false, err
	}
	registered, err := p.loadOnlyCertificate(ctx, prov, tenantID)
	if err != nil {
		if _, ok := err.(*CAKeyNotAvailableError); ok {
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
// one. For the built-in cakey.SoftwareProvider it calls Unseal (load-only)
// before Certificate, under the same per-tenant lock Initialize/Unseal use,
// so a tenant with no established CA answers *CAKeyNotAvailableError
// instead of material() auto-initializing a brand new CA just to answer the
// probe. An injected Provider (WithKeyProvider) has no such distinction --
// its CA is established by construction, not by this call -- so
// Certificate is called directly, as every other accessor on that path
// already does.
func (p *DefaultCertProvider) loadOnlyCertificate(ctx context.Context, prov cakey.Provider, tenantID string) (*x509.Certificate, error) {
	if err := p.tenantInitLocked(prov, tenantID, func() error {
		if sp, ok := prov.(*cakey.SoftwareProvider); ok {
			return sp.Unseal(ctx)
		}
		return nil
	}); err != nil {
		return nil, mapProviderError(err)
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
// on first use (create-or-load, serialized per tenant -- see
// tenantInitLocked), matching GenerateClientCert's existing
// auto-init-on-first-issuance behavior.
//
// The certificate returned is prov.Certificate's OWN answer for tenantID,
// read directly -- never p.caCert, the single shared "most recently
// resolved" cache CACertPEM also reads. Two different tenants' calls can
// interleave (this is called from GenerateClientCert and ExportPKCS12,
// neither of which holds any lock across the call), and reading the shared
// field back after resolving a DIFFERENT tenant's provider would hand that
// tenant tenant A's CA certificate whenever B's warmCertCache call landed
// between A's and this read (go-qrdb#560 review, non-blocking). warmCertCache
// is still called, for CACertPEM's public-data cache, but its result is not
// what this function returns.
func (p *DefaultCertProvider) getCAMaterial(ctx context.Context, tenantID string) (crypto.Signer, *x509.Certificate, error) {
	if p.barrier.IsSealed() {
		return nil, nil, &BarrierSealedError{}
	}

	prov, err := p.resolveKeyProvider(tenantID)
	if err != nil {
		return nil, nil, err
	}
	var signer crypto.Signer
	if err := p.tenantInitLocked(prov, tenantID, func() error {
		s, err := prov.Signer(ctx)
		if err != nil {
			return err
		}
		signer = s
		return nil
	}); err != nil {
		return nil, nil, mapProviderError(err)
	}
	cert, err := prov.Certificate(ctx)
	if err != nil {
		return nil, nil, mapProviderError(err)
	}
	if err := p.warmCertCache(ctx, prov); err != nil {
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

// validateGenerateRequest validates the fields of a GenerateRequest.
func validateGenerateRequest(req *GenerateRequest) error {
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
	return nil
}

// hasSerialSuffix reports whether a key path ends with "/" + serial.
func hasSerialSuffix(key, serial string) bool {
	suffix := "/" + serial
	if len(key) < len(suffix) {
		return false
	}
	return key[len(key)-len(suffix):] == suffix
}
