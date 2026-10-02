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

package cakey

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// serialBits is the bit width of a generated CA certificate's serial number.
const serialBits = 128

// SoftwareProvider is qrdb's built-in Provider: an ECDSA P-256 CA key,
// barrier-encrypted at rest and held in memory only while unsealed. It is
// the default behind the Provider seam whenever an embedder does not inject
// one of its own (go-xkms HSM/TPM/PKCS#11/KMS, via cakey.Adapter).
//
// It persists through kvstore.KVStore, qrdb's storage abstraction -- the
// local store in standalone mode, the replicated store in Raft mode. No
// code path here assumes Raft: a SoftwareProvider built over either store
// behaves identically, and a key written in one mode reads back correctly
// in the other, because both are the same interface.
type SoftwareProvider struct {
	kvStore  kvstore.KVStore
	barrier  seal.EncryptionBarrier
	prefix   string
	subject  pkix.Name
	validity time.Duration

	// adoptKeyPEM / adoptCertPEM are in-memory bootstrap material to adopt
	// on first use instead of generating fresh, when the store holds none
	// yet. See WithAdoptMaterial.
	adoptKeyPEM  []byte
	adoptCertPEM []byte

	mu   sync.RWMutex
	key  *ecdsa.PrivateKey
	cert *x509.Certificate
}

// Compile-time interface check.
var _ Provider = (*SoftwareProvider)(nil)

const (
	keyWrappedKey = "wrapped_key"
	keyCertPEM    = "cert_pem"
)

// SoftwareProviderOption configures a SoftwareProvider at construction.
type SoftwareProviderOption func(*SoftwareProvider)

// WithAdoptMaterial supplies in-memory key material generated before any
// store existed -- the bootstrap crash window (see
// docs/certificate-authority/architecture.md). On first use (the first
// Signer/Certificate/Initialize call), the provider adopts this material
// into the store if the store holds none yet (AdoptIfAbsent), or loads the
// already-persisted material otherwise, exactly as if AdoptIfAbsent had
// been called directly. Without this option, first use generates fresh
// material when the store is empty (plain Initialize semantics).
func WithAdoptMaterial(keyPEM, certPEM []byte) SoftwareProviderOption {
	return func(p *SoftwareProvider) {
		p.adoptKeyPEM = keyPEM
		p.adoptCertPEM = certPEM
	}
}

// NewSoftwareProvider returns a Provider backed by kvStore and barrier.
// prefix is the key-value namespace this provider's two records live under
// (e.g. "__sys:cluster_ca/" for the cluster-wide CA, or
// "__sys:cert_ca/<tenantID>/" for a per-tenant login CA) and must end in
// "/". subject and validity describe the self-signed CA certificate
// Initialize generates when the store holds none yet.
//
// Construction performs no I/O: Initialize/AdoptIfAbsent/Signer/Certificate
// are all lazy, consulting the store only on first use. This matters in
// Raft mode, where the replicated store is not yet readable (no leader
// elected) for a brief window right after the shard starts; eagerly reading
// it at construction would race that window on every boot.
func NewSoftwareProvider(kvStore kvstore.KVStore, barrier seal.EncryptionBarrier, prefix string, subject pkix.Name, validity time.Duration, opts ...SoftwareProviderOption) *SoftwareProvider {
	p := &SoftwareProvider{
		kvStore:  kvStore,
		barrier:  barrier,
		prefix:   prefix,
		subject:  subject,
		validity: validity,
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Initialize loads this provider's CA material from the store, generating
// and persisting a fresh key pair the first time it is called for a given
// prefix. It is idempotent and safe to call on every boot.
func (p *SoftwareProvider) Initialize(ctx context.Context) error {
	if p.barrier.IsSealed() {
		return &BarrierSealedError{}
	}

	wrappedPath := p.prefix + keyWrappedKey
	certPath := p.prefix + keyCertPEM

	existing, err := p.kvStore.Get(ctx, wrappedPath)
	if err != nil {
		return &StoreError{Op: "get", Detail: wrappedPath, Err: err}
	}
	if existing != nil {
		return p.load(ctx)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return &GenerateError{Err: err}
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), serialBits))
	if err != nil {
		return &GenerateError{Err: err}
	}

	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               p.subject,
		NotBefore:             now,
		NotAfter:              now.Add(p.validity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return &GenerateError{Err: err}
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return &GenerateError{Err: err}
	}

	wrapped, err := p.barrier.Encrypt(keyDER)
	secure.Wipe(keyDER)
	if err != nil {
		return &GenerateError{Err: err}
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	if err := p.kvStore.Put(ctx, wrappedPath, wrapped); err != nil {
		return &StoreError{Op: "put", Detail: wrappedPath, Err: err}
	}
	if err := p.kvStore.Put(ctx, certPath, certPEM); err != nil {
		return &StoreError{Op: "put", Detail: certPath, Err: err}
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return &GenerateError{Err: err}
	}

	p.mu.Lock()
	p.key = key
	p.cert = cert
	p.mu.Unlock()

	return nil
}

// AdoptIfAbsent persists keyPEM/certPEM (SEC1 "EC PRIVATE KEY" and
// "CERTIFICATE" PEM blocks respectively) as this provider's material if the
// store holds none yet, or discards them and loads the already-persisted
// material otherwise.
//
// This is how a key generated before any store existed -- the cluster CA,
// generated in memory on the first node before the cluster or its
// replicated store exist -- becomes this provider's permanent material
// exactly once, on whichever boot first reaches an unsealed store: the
// first caller's material wins and is written; every later call, on every
// later boot, is a no-op load that confirms the existing record instead of
// touching it. Because the check-then-write is a single call under no lock
// but the store's own, two callers racing to adopt will not corrupt the
// record, but only one write is EXPECTED to ever happen in practice: this
// is called once per cluster's lifetime, by the one node that ever holds an
// un-persisted cluster CA key.
//
// keyPEM/certPEM may both be empty, for a boot that holds no in-memory key
// of its own -- an ordinary restart once the key has already been adopted,
// when the caller's own on-disk cache of the key was deleted on confirmed
// persistence. In that case AdoptIfAbsent requires the store to already
// hold material and returns *NotInitializedError if it does not, rather
// than generating one, because it has no key to safely fall back on and
// generating a different one would silently diverge from whatever
// certificate this node (or a peer) already cached from the lost key.
func (p *SoftwareProvider) AdoptIfAbsent(ctx context.Context, keyPEM, certPEM []byte) error {
	if p.barrier.IsSealed() {
		return &BarrierSealedError{}
	}

	wrappedPath := p.prefix + keyWrappedKey
	certPath := p.prefix + keyCertPEM

	existing, err := p.kvStore.Get(ctx, wrappedPath)
	if err != nil {
		return &StoreError{Op: "get", Detail: wrappedPath, Err: err}
	}
	if existing != nil {
		return p.load(ctx)
	}

	if len(keyPEM) == 0 || len(certPEM) == 0 {
		return &NotInitializedError{}
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return &GenerateError{Err: errInvalidKeyPEM}
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return &GenerateError{Err: err}
	}
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return &GenerateError{Err: errInvalidCertPEM}
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return &GenerateError{Err: err}
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return &GenerateError{Err: err}
	}
	wrapped, err := p.barrier.Encrypt(keyDER)
	secure.Wipe(keyDER)
	if err != nil {
		return &GenerateError{Err: err}
	}

	if err := p.kvStore.Put(ctx, wrappedPath, wrapped); err != nil {
		return &StoreError{Op: "put", Detail: wrappedPath, Err: err}
	}
	if err := p.kvStore.Put(ctx, certPath, certPEM); err != nil {
		return &StoreError{Op: "put", Detail: certPath, Err: err}
	}

	p.mu.Lock()
	p.key = key
	p.cert = cert
	p.mu.Unlock()
	return nil
}

// Unseal loads the wrapped key from the store and decrypts it via the
// barrier, caching it in memory for signing. It is an alias of the load
// half of Initialize, exposed separately so a caller that already knows
// material exists (an ordinary restart) does not pay for the existence
// check Initialize performs.
func (p *SoftwareProvider) Unseal(ctx context.Context) error {
	if p.barrier.IsSealed() {
		return &BarrierSealedError{}
	}
	return p.load(ctx)
}

// load reads this provider's wrapped key and certificate from the store,
// decrypts the key, and caches both in memory.
func (p *SoftwareProvider) load(ctx context.Context) error {
	wrappedPath := p.prefix + keyWrappedKey
	certPath := p.prefix + keyCertPEM

	wrapped, err := p.kvStore.Get(ctx, wrappedPath)
	if err != nil {
		return &StoreError{Op: "get", Detail: wrappedPath, Err: err}
	}
	if wrapped == nil {
		return &NotInitializedError{}
	}
	certPEM, err := p.kvStore.Get(ctx, certPath)
	if err != nil {
		return &StoreError{Op: "get", Detail: certPath, Err: err}
	}
	if certPEM == nil {
		return &NotInitializedError{}
	}

	keyDER, err := p.barrier.Decrypt(wrapped)
	if err != nil {
		return &GenerateError{Err: err}
	}
	key, err := x509.ParseECPrivateKey(keyDER)
	secure.Wipe(keyDER)
	if err != nil {
		return &GenerateError{Err: err}
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		return &GenerateError{Err: errInvalidCertPEM}
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return &GenerateError{Err: err}
	}

	p.mu.Lock()
	p.key = key
	p.cert = cert
	p.mu.Unlock()
	return nil
}

// Seal destroys the in-memory key. Certificate, like cert.CertProvider's
// CACertPEM, remains cached: it is public data.
func (p *SoftwareProvider) Seal() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.key = nil
}

// Signer returns the cached key, auto-initializing from the store first
// when the barrier is unsealed but nothing has been loaded into memory yet
// (the ordinary case on every boot after the first).
func (p *SoftwareProvider) Signer(ctx context.Context) (crypto.Signer, error) {
	key, _, err := p.material(ctx)
	if err != nil {
		return nil, err
	}
	return key, nil
}

// Certificate returns the cached CA certificate, auto-initializing as
// Signer does.
func (p *SoftwareProvider) Certificate(ctx context.Context) (*x509.Certificate, error) {
	_, cert, err := p.material(ctx)
	if err != nil {
		return nil, err
	}
	return cert, nil
}

// material returns the cached key and certificate, loading them from the
// store first if this is the first call since the process started (or
// since the barrier was last unsealed).
func (p *SoftwareProvider) material(ctx context.Context) (*ecdsa.PrivateKey, *x509.Certificate, error) {
	if p.barrier.IsSealed() {
		return nil, nil, &BarrierSealedError{}
	}

	p.mu.RLock()
	key, cert := p.key, p.cert
	p.mu.RUnlock()

	if key == nil || cert == nil {
		var err error
		if len(p.adoptKeyPEM) > 0 || len(p.adoptCertPEM) > 0 {
			err = p.AdoptIfAbsent(ctx, p.adoptKeyPEM, p.adoptCertPEM)
		} else {
			err = p.Initialize(ctx)
		}
		if err != nil {
			return nil, nil, err
		}
		p.mu.RLock()
		key, cert = p.key, p.cert
		p.mu.RUnlock()
		if key == nil || cert == nil {
			return nil, nil, &NotInitializedError{}
		}
	}
	return key, cert, nil
}

// errInvalidCertPEM is the underlying error wrapped by GenerateError when a
// stored certificate record does not decode as PEM.
var errInvalidCertPEM = staticMaterialError("cakey: stored certificate is not valid PEM")

// errInvalidKeyPEM is the underlying error wrapped by GenerateError when
// AdoptIfAbsent is given a key that does not decode as SEC1 "EC PRIVATE KEY"
// PEM.
var errInvalidKeyPEM = staticMaterialError("cakey: adopted key is not valid EC PRIVATE KEY PEM")
