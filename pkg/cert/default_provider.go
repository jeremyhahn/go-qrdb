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
	"math/big"
	"sync"
	"time"

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
	defaultTTL time.Duration
}

// WithDefaultTTL sets the default certificate validity period used when
// GenerateRequest.TTL is zero.
func WithDefaultTTL(d time.Duration) ProviderOption {
	return func(o *providerOptions) {
		o.defaultTTL = d
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
		kvStore:    kvStore,
		barrier:    barrier,
		defaultTTL: o.defaultTTL,
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
func (p *DefaultCertProvider) GenerateClientCert(ctx context.Context, req *GenerateRequest) (*CertBundle, error) {
	if err := validateGenerateRequest(req); err != nil {
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

	serialHex := serialNumber.Text(16)

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

// ExportPKCS12 exports a certificate and private key as a PKCS#12 archive.
// This requires the software.sslmate.com/src/go-pkcs12 module. If the
// module is not available, this method returns a CertExportError.
//
// PKCS#12 export is delegated to the encodePKCS12 function, which can be
// replaced by builds that include a PKCS#12 encoder.
func (p *DefaultCertProvider) ExportPKCS12(_ context.Context, certPEM, keyPEM []byte, passphrase string) ([]byte, error) {
	if p.barrier.IsSealed() {
		return nil, &BarrierSealedError{}
	}

	p.mu.RLock()
	caCertPEM := p.caCertPEM
	p.mu.RUnlock()

	return encodePKCS12(certPEM, keyPEM, caCertPEM, passphrase)
}

// RevokeCert marks a certificate serial as revoked by writing a
// revocation entry to KVStore and updating the CertRecord.
func (p *DefaultCertProvider) RevokeCert(ctx context.Context, serial string) error {
	if p.barrier.IsSealed() {
		return &BarrierSealedError{}
	}

	if serial == "" {
		return &ValidationError{Field: "serial", Reason: "must not be empty"}
	}

	// Write revocation marker.
	revokedKey := prefixCertsRevoked + serial
	if err := p.kvStore.Put(ctx, revokedKey, []byte("1")); err != nil {
		return &StoreError{Op: "put", Detail: revokedKey, Err: err}
	}

	// Update the cert record if it exists. We scan all tenants because
	// the caller provides only the serial.
	if err := p.kvStore.Scan(ctx, prefixCerts, func(key string, value []byte) error {
		if !hasSerialSuffix(key, serial) {
			return nil
		}
		var rec CertRecord
		if err := json.Unmarshal(value, &rec); err != nil {
			return nil // skip corrupt records
		}
		rec.Revoked = true
		updated, err := json.Marshal(&rec)
		if err != nil {
			return nil
		}
		return p.kvStore.Put(ctx, key, updated)
	}); err != nil {
		return &StoreError{Op: "scan", Detail: prefixCerts, Err: err}
	}

	return nil
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
