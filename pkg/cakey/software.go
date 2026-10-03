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
	"math/big"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// serialBits is the bit width of a generated CA certificate's serial number.
const serialBits = 128

// SoftwareProvider is qrdb's built-in Provider: an ECDSA P-256 CA key held
// in memory only while unsealed. It is the default behind the Provider seam
// whenever an embedder does not inject one of its own (go-xkms
// HSM/TPM/PKCS#11/KMS, via cakey.Adapter).
//
// It persists through kvstore.AtomicKVStore, qrdb's storage abstraction --
// the local store in standalone mode, the replicated store in Raft mode. No
// code path here assumes Raft: a SoftwareProvider built over either store
// behaves identically, and material written in one mode reads back
// correctly in the other, because both are the same interface.
//
// The record is stored exactly as generated or adopted -- it carries no
// additional AEAD wrap of its own. An earlier revision wrapped the key with
// THIS node's seal.EncryptionBarrier before the Put; that made the record
// readable only by the node that wrote it, so every other node sharing the
// same replicated record failed to decrypt it with ITS OWN barrier -- a
// different key, even under the same seal strategy, because barriers are
// not shared cluster-wide. (Owner ruling, go-qrdb review: the replicated
// cluster CA key is protected like every other system record -- this
// node's own at-rest storage encryption, when configured (pkg/seal/encfs),
// protects the copy on disk; cluster mTLS protects it in flight during
// replication; with seal strategy "none" it is unencrypted at rest, like
// everything else in that mode.) barrier therefore gates ONLY whether this
// provider will touch key material at all (IsSealed()), exactly as
// cert.CertProvider's own seal gate does -- it no longer performs any
// encryption of its own.
type SoftwareProvider struct {
	kvStore  kvstore.AtomicKVStore
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

// keyMaterial is the KVStore key suffix for this provider's ONE combined
// record: the key and certificate written and read together, never as two
// separate Puts. Two separate records (an earlier revision's "wrapped_key"
// and "cert_pem") left a window between them: a crash, or two callers
// racing to adopt or generate, could leave the key from one write beside
// the certificate from another -- a torn pair neither writer intended and
// no reader could detect. One record makes that pair atomic by
// construction: there is no state in which the key exists without its
// matching certificate, or vice versa.
const keyMaterial = "material"

// storedMaterial is the JSON envelope keyMaterial holds: the CA's SEC1 EC
// private key and self-signed certificate, both DER, written and read
// together as one []byte value.
type storedMaterial struct {
	KeyDER  []byte `json:"key_der"`
	CertDER []byte `json:"cert_der"`
}

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
// prefix is the key-value namespace this provider's one record lives under
// (e.g. "__sys:cluster_ca/" for the cluster-wide CA, or
// "__sys:cert_ca/<tenantID>/" for a per-tenant login CA) and must end in
// "/". subject and validity describe the self-signed CA certificate
// Initialize generates when the store holds none yet.
//
// kvStore must support atomic conditional writes (PutIfAbsent): this is
// what makes concurrent generation or adoption from several callers --
// several nodes racing to become the cluster's first, or one node racing
// its own retried boot -- converge on exactly one CA rather than each
// writing its own candidate over the other's. See Initialize and
// AdoptIfAbsent.
//
// Construction performs no I/O: Initialize/AdoptIfAbsent/Signer/Certificate
// are all lazy, consulting the store only on first use. This matters in
// Raft mode, where the replicated store is not yet readable (no leader
// elected) for a brief window right after the shard starts; eagerly reading
// it at construction would race that window on every boot.
func NewSoftwareProvider(kvStore kvstore.AtomicKVStore, barrier seal.EncryptionBarrier, prefix string, subject pkix.Name, validity time.Duration, opts ...SoftwareProviderOption) *SoftwareProvider {
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
//
// A fresh candidate key and certificate are generated on every call -- even
// when the store already holds material -- and the single PutIfAbsent
// below is what actually decides whether they are used. That looks
// wasteful on the overwhelmingly common path (an ordinary restart, where
// the candidate is immediately discarded), but an ECDSA P-256 key and a
// small self-signed certificate cost microseconds to generate, and the
// alternative -- check existence, then generate, then write -- is exactly
// the race this function exists to close: two callers (two first-boot
// nodes, or a retried boot racing its own prior attempt) could both pass
// the existence check before either writes, and the second Put would
// silently replace the first CA with a different one. PutIfAbsent makes
// "does it already exist" and "write it" one indivisible step, so there is
// no window in which two candidates can both believe they are first.
func (p *SoftwareProvider) Initialize(ctx context.Context) error {
	if p.barrier.IsSealed() {
		return &BarrierSealedError{}
	}

	key, cert, keyDER, certDER, err := p.generate()
	if err != nil {
		return err
	}
	record, err := encodeMaterial(keyDER, certDER)
	secure.Wipe(keyDER)
	if err != nil {
		return err
	}

	materialPath := p.prefix + keyMaterial
	stored, err := p.kvStore.PutIfAbsent(ctx, materialPath, record)
	if err != nil {
		return &StoreError{Op: "putifabsent", Detail: materialPath, Err: err}
	}
	if !stored {
		// Lost the race, or material already existed from an earlier boot:
		// either way, the store now holds the record that counts. Load it
		// rather than assume our own candidate was the one written.
		return p.load(ctx)
	}

	p.mu.Lock()
	p.key = key
	p.cert = cert
	p.mu.Unlock()
	return nil
}

// generate creates a fresh ECDSA P-256 key and a self-signed CA certificate
// for it, returning both the parsed values and their DER encodings.
func (p *SoftwareProvider) generate() (*ecdsa.PrivateKey, *x509.Certificate, []byte, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, nil, &GenerateError{Err: err}
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), serialBits))
	if err != nil {
		return nil, nil, nil, nil, &GenerateError{Err: err}
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
		return nil, nil, nil, nil, &GenerateError{Err: err}
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, nil, nil, nil, &GenerateError{Err: err}
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, nil, nil, &GenerateError{Err: err}
	}

	return key, cert, keyDER, certDER, nil
}

// encodeMaterial builds the JSON envelope Initialize/AdoptIfAbsent store.
func encodeMaterial(keyDER, certDER []byte) ([]byte, error) {
	data, err := json.Marshal(storedMaterial{KeyDER: keyDER, CertDER: certDER})
	if err != nil {
		return nil, &GenerateError{Err: err}
	}
	return data, nil
}

// decodeMaterial parses the JSON envelope load reads back. A failure here
// means the STORED record itself cannot be used -- it is not valid JSON, or
// its fields do not parse as the key/certificate they claim to be -- which
// is a materially different condition from GenerateError (this node failed
// to CREATE material) and is reported as *MaterialCorruptError instead, so
// an operator is not told "key generation failed" about a record this
// process never tried to generate.
func decodeMaterial(data []byte) (*ecdsa.PrivateKey, *x509.Certificate, []byte, error) {
	var m storedMaterial
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, nil, nil, &MaterialCorruptError{Err: err}
	}
	key, err := x509.ParseECPrivateKey(m.KeyDER)
	if err != nil {
		return nil, nil, nil, &MaterialCorruptError{Err: err}
	}
	cert, err := x509.ParseCertificate(m.CertDER)
	if err != nil {
		return nil, nil, nil, &MaterialCorruptError{Err: err}
	}
	return key, cert, m.CertDER, nil
}

// AdoptIfAbsent persists keyPEM/certPEM (SEC1 "EC PRIVATE KEY" and
// "CERTIFICATE" PEM blocks respectively) as this provider's material if the
// store holds none yet, or verifies them against the already-persisted
// material otherwise.
//
// This is how a key generated before any store existed -- the cluster CA,
// generated in memory on the first node before the cluster or its
// replicated store exist -- becomes this provider's permanent material
// exactly once, on whichever boot first reaches an unsealed store: the
// first caller's material wins and is written (via the same PutIfAbsent
// Initialize uses, for the same reason); every later call, on every later
// boot or every other node, loads the winning record instead of writing
// again.
//
// A caller whose own in-memory certificate does not match the winning
// record's is NOT silently overridden and is NOT treated as a successful
// adoption: it is a *MaterialMismatchError, because two different CAs
// claiming to be the cluster's one Key A is a split-brain the caller must
// stop and report, not paper over by keeping whichever happened to be
// asked about second. The caller's own on-disk cache of the rejected
// material (if any) is left untouched -- this function has no filesystem
// access and never deletes anything; cmd/qrdbd's cache-eviction step runs
// only after Certificate/Signer succeeds, which a *MaterialMismatchError
// here prevents.
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

	if len(keyPEM) == 0 && len(certPEM) == 0 {
		return p.load(ctx)
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
	certPub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !certPub.Equal(&key.PublicKey) {
		return &KeyCertMismatchError{}
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return &GenerateError{Err: err}
	}
	record, err := encodeMaterial(keyDER, cert.Raw)
	secure.Wipe(keyDER)
	if err != nil {
		return err
	}

	materialPath := p.prefix + keyMaterial
	stored, err := p.kvStore.PutIfAbsent(ctx, materialPath, record)
	if err != nil {
		return &StoreError{Op: "putifabsent", Detail: materialPath, Err: err}
	}
	if stored {
		p.mu.Lock()
		p.key = key
		p.cert = cert
		p.mu.Unlock()
		return nil
	}

	// Lost the race, or material already existed: load the winning record
	// and verify it is OUR material before accepting it silently.
	winnerKey, winnerCert, winnerCertDER, err := p.loadRecord(ctx)
	if err != nil {
		return err
	}
	if !bytes.Equal(winnerCertDER, cert.Raw) {
		return &MaterialMismatchError{}
	}
	p.mu.Lock()
	p.key = winnerKey
	p.cert = winnerCert
	p.mu.Unlock()
	return nil
}

// Unseal loads this provider's material from the store, caching it in
// memory for signing. It is an alias of load, exposed separately so a
// caller that already knows material exists (an ordinary restart) does not
// pay for the existence check Initialize performs.
func (p *SoftwareProvider) Unseal(ctx context.Context) error {
	if p.barrier.IsSealed() {
		return &BarrierSealedError{}
	}
	return p.load(ctx)
}

// load reads this provider's material from the store and caches it in
// memory.
func (p *SoftwareProvider) load(ctx context.Context) error {
	key, cert, _, err := p.loadRecord(ctx)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.key = key
	p.cert = cert
	p.mu.Unlock()
	return nil
}

// loadRecord reads and decodes this provider's stored record without
// caching it, so AdoptIfAbsent can compare it against a candidate before
// deciding whether to accept it.
func (p *SoftwareProvider) loadRecord(ctx context.Context) (*ecdsa.PrivateKey, *x509.Certificate, []byte, error) {
	materialPath := p.prefix + keyMaterial
	data, err := p.kvStore.Get(ctx, materialPath)
	if err != nil {
		return nil, nil, nil, &StoreError{Op: "get", Detail: materialPath, Err: err}
	}
	if data == nil {
		return nil, nil, nil, &NotInitializedError{}
	}
	return decodeMaterial(data)
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

// errInvalidCertPEM is the underlying error wrapped by GenerateError when
// AdoptIfAbsent is given a certificate that does not decode as PEM.
var errInvalidCertPEM = staticMaterialError("cakey: adopted certificate is not valid PEM")

// errInvalidKeyPEM is the underlying error wrapped by GenerateError when
// AdoptIfAbsent is given a key that does not decode as SEC1 "EC PRIVATE KEY"
// PEM.
var errInvalidKeyPEM = staticMaterialError("cakey: adopted key is not valid EC PRIVATE KEY PEM")
