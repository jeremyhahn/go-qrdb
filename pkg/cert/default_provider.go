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
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

const (
	// defaultTTL is the default certificate validity period (1 year).
	defaultTTL = 8760 * time.Hour

	// caTTL is the CA certificate validity period (10 years).
	caTTL = 87600 * time.Hour

	// keyWrappedKey is the KVStore key suffix for the wrapped CA key.
	keyWrappedKey = "wrapped_key"

	// keyCertPEM is the KVStore key suffix for the CA certificate PEM.
	keyCertPEM = "cert_pem"

	// prefixCertCA is the KVStore key prefix for CA material.
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
}

// WithDefaultTTL sets the default certificate validity period used when
// GenerateRequest.TTL is zero.
func WithDefaultTTL(d time.Duration) ProviderOption {
	return func(o *providerOptions) {
		o.defaultTTL = d
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

// DefaultCertProvider implements CertProvider using barrier-sealed CA key
// material. The CA private key is encrypted at rest via the
// EncryptionBarrier and held in a secure.Wipe'd buffer while
// unsealed.
type DefaultCertProvider struct {
	kvStore    kvstore.KVStore
	barrier    seal.EncryptionBarrier
	caKey      []byte
	caCert     *x509.Certificate
	caCertPEM  []byte
	mu         sync.RWMutex
	defaultTTL time.Duration

	// trustDomain, when non-empty, is the SPIFFE trust domain every issued
	// client certificate's tenant SAN is minted under. See WithTrustDomain.
	trustDomain string
}

// NewDefaultProvider creates a new DefaultCertProvider. The provider
// must be initialized via Initialize and unsealed via Unseal before
// certificate operations are available.
func NewDefaultProvider(kvStore kvstore.KVStore, barrier seal.EncryptionBarrier, opts ...ProviderOption) *DefaultCertProvider {
	o := &providerOptions{
		defaultTTL: defaultTTL,
	}
	for _, fn := range opts {
		fn(o)
	}
	return &DefaultCertProvider{
		kvStore:     kvStore,
		barrier:     barrier,
		defaultTTL:  o.defaultTTL,
		trustDomain: o.trustDomain,
	}
}

// Initialize generates a CA key pair, seals the private key via the
// barrier, and persists both the wrapped key and CA certificate PEM to
// KVStore. If CA material already exists in KVStore, it is loaded
// instead.
func (p *DefaultCertProvider) Initialize(ctx context.Context) error {
	if p.barrier.IsSealed() {
		return &BarrierSealedError{}
	}

	tenantID := tenant.FromContext(ctx)
	wrappedKeyPath := prefixCertCA + tenantID + "/" + keyWrappedKey
	certPEMPath := prefixCertCA + tenantID + "/" + keyCertPEM

	// Check if CA already exists.
	existing, err := p.kvStore.Get(ctx, wrappedKeyPath)
	if err != nil {
		return &StoreError{Op: "get", Detail: wrappedKeyPath, Err: err}
	}
	if existing != nil {
		return p.loadCA(ctx, tenantID)
	}

	// Generate new CA key pair.
	caPrivKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return &CertGenerateError{Err: err}
	}

	serialNumber, err := generateSerial()
	if err != nil {
		return &CertGenerateError{Err: err}
	}

	now := time.Now().UTC()
	caTemplate := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:         "qrdb-ca",
			OrganizationalUnit: []string{tenantID},
		},
		NotBefore:             now,
		NotAfter:              now.Add(caTTL),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}

	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caPrivKey.PublicKey, caPrivKey)
	if err != nil {
		return &CertGenerateError{Err: err}
	}

	// Marshal private key to DER.
	caKeyDER, err := x509.MarshalECPrivateKey(caPrivKey)
	if err != nil {
		return &CertGenerateError{Err: err}
	}

	// Encrypt the private key via the barrier.
	wrappedKey, err := p.barrier.Encrypt(caKeyDER)
	if err != nil {
		secure.Wipe(caKeyDER)
		return &CertGenerateError{Err: err}
	}

	// Encode CA cert to PEM.
	caCertPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: caCertDER,
	})

	// Persist wrapped key and CA cert PEM.
	if err := p.kvStore.Put(ctx, wrappedKeyPath, wrappedKey); err != nil {
		secure.Wipe(caKeyDER)
		return &StoreError{Op: "put", Detail: wrappedKeyPath, Err: err}
	}
	if err := p.kvStore.Put(ctx, certPEMPath, caCertPEM); err != nil {
		secure.Wipe(caKeyDER)
		return &StoreError{Op: "put", Detail: certPEMPath, Err: err}
	}

	// Parse the CA cert for signing.
	caCert, err := x509.ParseCertificate(caCertDER)
	if err != nil {
		secure.Wipe(caKeyDER)
		return &CertGenerateError{Err: err}
	}

	// Store the key material in memory.
	p.mu.Lock()
	p.caKey = caKeyDER
	p.caCert = caCert
	p.caCertPEM = caCertPEM
	p.mu.Unlock()

	return nil
}

// Unseal loads the wrapped CA key from KVStore, decrypts it via the
// barrier, and caches the key in memory for signing operations.
func (p *DefaultCertProvider) Unseal(ctx context.Context) error {
	if p.barrier.IsSealed() {
		return &BarrierSealedError{}
	}

	tenantID := tenant.FromContext(ctx)
	return p.loadCA(ctx, tenantID)
}

// Seal destroys the in-memory CA key material and clears the cached
// signer. All certificate operations will return BarrierSealedError
// until Unseal is called again.
func (p *DefaultCertProvider) Seal() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.caKey != nil {
		secure.Wipe(p.caKey)
		p.caKey = nil
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

	caKey, caCert, err := p.getCAMaterial()
	if err != nil {
		return nil, err
	}

	// Parse CA private key.
	privKey, err := x509.ParseECPrivateKey(caKey)
	if err != nil {
		return nil, &CertGenerateError{Err: err}
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

	clientCertDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, caCert, &clientPrivKey.PublicKey, privKey)
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

	p.mu.RLock()
	caCertPEM := p.caCertPEM
	p.mu.RUnlock()

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

// loadCA loads the CA key and certificate from KVStore, decrypts the
// key via the barrier, and caches both in memory.
func (p *DefaultCertProvider) loadCA(ctx context.Context, tenantID string) error {
	wrappedKeyPath := prefixCertCA + tenantID + "/" + keyWrappedKey
	certPEMPath := prefixCertCA + tenantID + "/" + keyCertPEM

	// Load wrapped key.
	wrappedKey, err := p.kvStore.Get(ctx, wrappedKeyPath)
	if err != nil {
		return &StoreError{Op: "get", Detail: wrappedKeyPath, Err: err}
	}
	if wrappedKey == nil {
		return &CAKeyNotAvailableError{}
	}

	// Load CA cert PEM.
	caCertPEM, err := p.kvStore.Get(ctx, certPEMPath)
	if err != nil {
		return &StoreError{Op: "get", Detail: certPEMPath, Err: err}
	}
	if caCertPEM == nil {
		return &CAKeyNotAvailableError{}
	}

	// Decrypt the CA private key.
	caKeyDER, err := p.barrier.Decrypt(wrappedKey)
	if err != nil {
		return &CertGenerateError{Err: err}
	}

	// Parse the CA certificate.
	block, _ := pem.Decode(caCertPEM)
	if block == nil {
		return &CertGenerateError{Err: &ValidationError{Field: "ca_cert_pem", Reason: "invalid PEM data"}}
	}
	caCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		secure.Wipe(caKeyDER)
		return &CertGenerateError{Err: err}
	}

	p.mu.Lock()
	// Wipe old key if present.
	if p.caKey != nil {
		secure.Wipe(p.caKey)
	}
	p.caKey = caKeyDER
	p.caCert = caCert
	p.caCertPEM = caCertPEM
	p.mu.Unlock()

	return nil
}

// getCAMaterial returns the CA key DER and certificate under the read
// lock. Returns BarrierSealedError when the barrier is sealed.
//
// If the barrier is unsealed but the CA key is not yet loaded (e.g.,
// after a barrier unseal or server restart), getCAMaterial
// auto-initializes by calling Initialize, which loads or generates
// the CA material.
func (p *DefaultCertProvider) getCAMaterial() ([]byte, *x509.Certificate, error) {
	if p.barrier.IsSealed() {
		return nil, nil, &BarrierSealedError{}
	}

	p.mu.RLock()
	key, cert := p.caKey, p.caCert
	p.mu.RUnlock()

	if key == nil || cert == nil {
		// Auto-initialize: barrier is unsealed but CA not loaded.
		if err := p.Initialize(context.Background()); err != nil {
			return nil, nil, err
		}
		p.mu.RLock()
		key, cert = p.caKey, p.caCert
		p.mu.RUnlock()
		if key == nil || cert == nil {
			return nil, nil, &CAKeyNotAvailableError{}
		}
	}

	// Return a copy of the key to avoid aliasing.
	keyCopy := make([]byte, len(key))
	copy(keyCopy, key)
	return keyCopy, cert, nil
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
