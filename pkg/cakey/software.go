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
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"sync"
	"sync/atomic"
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
// It persists through kvstore.MultiGuardedBatchKVStore, qrdb's atomic
// multi-key batch capability -- the local store in standalone mode (backed
// by the same in-process transaction plane every other guarded store in
// this tree uses), the replicated store in Raft mode. No code path here
// assumes Raft: a SoftwareProvider built over either store behaves
// identically in either, and material adopted or generated in one mode
// reads back correctly in the other, because both are the same interface.
//
// The private key and the public certificate are TWO separate records
// ("material" and "certificate"), written together in ONE atomic batch
// (kvstore.MultiGuardedBatchKVStore.PutAllIfAllUnchanged), never as two
// independent Puts. An earlier revision combined them into one JSON-encoded
// record specifically to make their creation atomic with a single
// PutIfAbsent; splitting them back out without losing that atomicity is
// exactly what the guarded batch primitive is for, and it buys something
// the combined record could not: a caller that only ever needs the
// certificate -- trust-pool building, an issuer check, CACertPEM -- now
// reads and parses ONLY the certificate record. The private key bytes are
// never even fetched from the store, let alone parsed, unless something
// calls Signer.
//
// The records carry no additional AEAD wrap of their own. An earlier
// revision wrapped the key with THIS node's seal.EncryptionBarrier before
// the Put; that made the record readable only by the node that wrote it, so
// every other node sharing the same replicated record failed to decrypt it
// with ITS OWN barrier -- a different key, even under the same seal
// strategy, because barriers are not shared cluster-wide. (Owner ruling,
// go-qrdb review: the replicated cluster CA key is protected like every
// other system record -- this node's own at-rest storage encryption, when
// configured (pkg/seal/encfs), protects the copy on disk; cluster mTLS
// protects it in flight during replication; with seal strategy "none" it is
// unencrypted at rest, like everything else in that mode.) barrier
// therefore gates ONLY whether this provider will touch key material at
// all (IsSealed()), exactly as cert.CertProvider's own seal gate does -- it
// no longer performs any encryption of its own.
type SoftwareProvider struct {
	kvStore  kvstore.MultiGuardedBatchKVStore
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

	// gen counts Seal calls. Every barrierCheckedSigner captures the
	// generation current when Signer handed it out; Seal bumps it, so a
	// Signer obtained before a Seal call refuses on its next Sign even
	// though the caller is still holding the same crypto.Signer value and
	// the barrier itself may still be unsealed (Seal and barrier.IsSealed
	// are two independent facts -- this provider's own key can be
	// revoked without the barrier resealing). Lock-free: read on every
	// Sign call, so a mutex here would serialize concurrent signing for
	// no reason.
	gen atomic.Uint64
}

// Compile-time interface check.
var _ Provider = (*SoftwareProvider)(nil)

// keyMaterial is the KVStore key suffix for this provider's private-key
// record: the SEC1 DER-encoded EC private key, and nothing else. Reading
// this record is reserved for paths that genuinely need to sign -- Signer,
// and the full-material load/adopt paths used after Seal or on an ordinary
// restart.
const keyMaterial = "material"

// certMaterial is the KVStore key suffix for this provider's certificate
// record: the DER-encoded CA certificate, and nothing else. It is public
// data -- the same contract cert.CertProvider's CACertPEM documents -- and
// every read of it (Certificate, the steady-state half of AdoptIfAbsent's
// mismatch check) never touches keyMaterial.
const certMaterial = "certificate"

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
// prefix is the key-value namespace this provider's two records (the
// private key and the certificate) live under (e.g. "__sys:cluster_ca/" for
// the cluster-wide CA, or "__sys:cert_ca/<tenantID>/" for a per-tenant login
// CA) and must end in "/". subject and validity describe the self-signed CA
// certificate Initialize generates when the store holds none yet.
//
// kvStore must support atomic multi-key batch writes
// (kvstore.MultiGuardedBatchKVStore): this is what makes writing the key and
// certificate records together, and concurrent generation or adoption from
// several callers -- several nodes racing to become the cluster's first, or
// one node racing its own retried boot -- converge on exactly one CA rather
// than each writing its own candidate over the other's, or a crash tearing
// the pair. See Initialize and AdoptIfAbsent.
//
// Construction performs no I/O: Initialize/AdoptIfAbsent/Signer/Certificate
// are all lazy, consulting the store only on first use. This matters in
// Raft mode, where the replicated store is not yet readable (no leader
// elected) for a brief window right after the shard starts; eagerly reading
// it at construction would race that window on every boot.
func NewSoftwareProvider(kvStore kvstore.MultiGuardedBatchKVStore, barrier seal.EncryptionBarrier, prefix string, subject pkix.Name, validity time.Duration, opts ...SoftwareProviderOption) *SoftwareProvider {
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
// when the store already holds material -- and the single guarded batch
// write below is what actually decides whether they are used. That looks
// wasteful on the overwhelmingly common path (an ordinary restart, where the
// candidate is immediately discarded), but an ECDSA P-256 key and a small
// self-signed certificate cost microseconds to generate, and the
// alternative -- check existence, then generate, then write -- is exactly
// the race this function exists to close: two callers (two first-boot
// nodes, or a retried boot racing its own prior attempt) could both pass
// the existence check before either writes, and the second write would
// silently replace the first CA with a different one. Guarding both
// records' absence and writing both in one batch makes "do either already
// exist" and "write both" one indivisible step, so there is no window in
// which two candidates can both believe they are first, and no window in
// which one record exists without the other.
func (p *SoftwareProvider) Initialize(ctx context.Context) error {
	if p.barrier.IsSealed() {
		return &BarrierSealedError{}
	}

	key, cert, keyDER, certDER, err := p.generate()
	if err != nil {
		return err
	}
	defer secure.Wipe(keyDER)

	applied, err := p.writeIfBothAbsent(ctx, keyDER, certDER)
	if err != nil {
		return err
	}
	if !applied {
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

// writeIfBothAbsent writes keyDER and certDER to this provider's two
// records in one atomic batch, guarded on BOTH being absent -- the
// generalization of the single combined record's PutIfAbsent to two
// records that must come into existence together or not at all.
func (p *SoftwareProvider) writeIfBothAbsent(ctx context.Context, keyDER, certDER []byte) (bool, error) {
	materialPath := p.prefix + keyMaterial
	certPath := p.prefix + certMaterial
	applied, err := p.kvStore.PutAllIfAllUnchanged(ctx,
		[]kvstore.Guard{{Key: materialPath}, {Key: certPath}},
		[]kvstore.KV{{Key: materialPath, Value: keyDER}, {Key: certPath, Value: certDER}},
	)
	if err != nil {
		return false, &StoreError{Op: "putallifallunchanged", Detail: materialPath, Err: err}
	}
	return applied, nil
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

// decodeKey parses a stored private-key record. A failure here means the
// STORED record itself cannot be used -- it is not a valid SEC1 EC private
// key -- which is reported as *MaterialCorruptError, distinct from
// GenerateError (this node failing to CREATE material).
func decodeKey(data []byte) (*ecdsa.PrivateKey, error) {
	key, err := x509.ParseECPrivateKey(data)
	if err != nil {
		return nil, &MaterialCorruptError{Err: err}
	}
	return key, nil
}

// decodeCert parses a stored certificate record, reported as
// *MaterialCorruptError on failure for the same reason decodeKey is.
func decodeCert(data []byte) (*x509.Certificate, error) {
	cert, err := x509.ParseCertificate(data)
	if err != nil {
		return nil, &MaterialCorruptError{Err: err}
	}
	return cert, nil
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
// first caller's material wins and is written (via the same guarded batch
// Initialize uses, for the same reason); every later call, on every later
// boot or every other node, loads the winning record instead of writing
// again.
//
// A caller whose own in-memory certificate does not match the winning
// record's is NOT silently overridden and is NOT treated as a successful
// adoption: it is a *MaterialMismatchError, because two different CAs
// claiming to be the cluster's one Key A is a split-brain the caller must
// stop and report, not paper over by keeping whichever happened to be
// asked about second. Checking that is a certificate-only comparison --
// this path never loads or parses the losing side's private key. The
// caller's own on-disk cache of the rejected material (if any) is left
// untouched -- this function has no filesystem access and never deletes
// anything; cmd/qrdbd's cache-eviction step runs only after
// Certificate/Signer succeeds, which a *MaterialMismatchError here prevents.
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
	defer secure.Wipe(keyDER)

	applied, err := p.writeIfBothAbsent(ctx, keyDER, cert.Raw)
	if err != nil {
		return err
	}
	if applied {
		p.mu.Lock()
		p.key = key
		p.cert = cert
		p.mu.Unlock()
		return nil
	}

	// Lost the race, or material already existed: load the winning
	// certificate -- cert-only, the key stays untouched -- and verify it is
	// OUR material before accepting it silently.
	winnerCert, winnerCertDER, err := p.loadCertRecord(ctx)
	if err != nil {
		return err
	}
	if !bytes.Equal(winnerCertDER, cert.Raw) {
		return &MaterialMismatchError{}
	}
	p.mu.Lock()
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

// load reads this provider's key AND certificate records from the store and
// caches both in memory. Every caller of load intends to sign (Unseal, the
// post-Seal reload inside signingKey, AdoptIfAbsent/Initialize's "already
// exists" fallback), so loading the key here is not a violation of the
// cert-only reads Certificate uses -- it is load's whole purpose.
func (p *SoftwareProvider) load(ctx context.Context) error {
	key, err := p.loadKeyRecord(ctx)
	if err != nil {
		return err
	}
	cert, _, err := p.loadCertRecord(ctx)
	if err != nil {
		return err
	}
	if err := checkKeyMatchesCert(key, cert); err != nil {
		return err
	}
	p.mu.Lock()
	p.key = key
	p.cert = cert
	p.mu.Unlock()
	return nil
}

// checkKeyMatchesCert refuses with *KeyCertMismatchError when key's public
// half does not match cert's -- a stored key and certificate that do not
// belong together, which the atomic batch write (Initialize/AdoptIfAbsent)
// should make impossible in normal operation, but a corrupted store, a
// manually edited record, or a bug elsewhere is not a condition this
// provider should ever sign under without noticing. Called by every path
// that loads the key record and is about to trust it against a
// (cached or freshly loaded) certificate: load, and signingKey's
// cert-only-cached tail.
func checkKeyMatchesCert(key *ecdsa.PrivateKey, cert *x509.Certificate) error {
	certPub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !certPub.Equal(&key.PublicKey) {
		return &KeyCertMismatchError{}
	}
	return nil
}

// loadKeyRecord reads and decodes this provider's stored private-key
// record, without touching the certificate record.
func (p *SoftwareProvider) loadKeyRecord(ctx context.Context) (*ecdsa.PrivateKey, error) {
	materialPath := p.prefix + keyMaterial
	data, err := p.kvStore.Get(ctx, materialPath)
	if err != nil {
		return nil, &StoreError{Op: "get", Detail: materialPath, Err: err}
	}
	if data == nil {
		return nil, &NotInitializedError{}
	}
	return decodeKey(data)
}

// loadCertRecord reads and decodes this provider's stored certificate
// record, without touching the private-key record -- the read path every
// certificate-only caller (Certificate, AdoptIfAbsent's mismatch check)
// goes through.
func (p *SoftwareProvider) loadCertRecord(ctx context.Context) (*x509.Certificate, []byte, error) {
	certPath := p.prefix + certMaterial
	data, err := p.kvStore.Get(ctx, certPath)
	if err != nil {
		return nil, nil, &StoreError{Op: "get", Detail: certPath, Err: err}
	}
	if data == nil {
		return nil, nil, &NotInitializedError{}
	}
	cert, err := decodeCert(data)
	if err != nil {
		return nil, nil, err
	}
	return cert, data, nil
}

// Seal destroys the in-memory key and revokes every crypto.Signer Signer
// has already handed out: it bumps gen, which every barrierCheckedSigner
// compares its own captured generation against on each Sign call. A caller
// holding a Signer obtained before this call cannot sign again, even
// though it never goes back through this provider to find out. Certificate,
// like cert.CertProvider's CACertPEM, remains cached: it is public data.
func (p *SoftwareProvider) Seal() {
	p.mu.Lock()
	p.key = nil
	p.mu.Unlock()
	p.gen.Add(1)
}

// Signer returns a crypto.Signer backed by the cached key, auto-initializing
// from the store first when the barrier is unsealed but nothing has been
// loaded into memory yet (the ordinary case on every boot after the first,
// or right after Seal). The returned Signer is opaque: it cannot be type-
// asserted back to *ecdsa.PrivateKey, re-checks the barrier on every Sign
// call rather than only at the time Signer was fetched, and is revoked by
// Seal -- see barrierCheckedSigner's own doc comment.
func (p *SoftwareProvider) Signer(ctx context.Context) (crypto.Signer, error) {
	key, err := p.signingKey(ctx)
	if err != nil {
		return nil, err
	}
	return &barrierCheckedSigner{provider: p, key: key, gen: p.gen.Load()}, nil
}

// signingKey returns the cached private key, loading it from the store
// first if nothing is cached (the first call since the process started, or
// since Seal last ran).
func (p *SoftwareProvider) signingKey(ctx context.Context) (*ecdsa.PrivateKey, error) {
	if p.barrier.IsSealed() {
		return nil, &BarrierSealedError{}
	}

	p.mu.RLock()
	key := p.key
	p.mu.RUnlock()
	if key != nil {
		return key, nil
	}

	if err := p.ensureEstablished(ctx); err != nil {
		return nil, err
	}
	p.mu.RLock()
	key = p.key
	p.mu.RUnlock()
	if key != nil {
		return key, nil
	}

	// ensureEstablished confirmed this provider's material is the one the
	// store holds, but did not necessarily cache the key: AdoptIfAbsent's
	// "lost the race, but it is ours" path verifies and caches the
	// CERTIFICATE only (loadCertRecord), deliberately never touching the
	// key record for that check. Now that establishment is confirmed, load
	// the key directly -- one more Get, not a re-run of Initialize or
	// AdoptIfAbsent, so this cannot loop.
	key, err := p.loadKeyRecord(ctx)
	if err != nil {
		return nil, err
	}
	// p.cert is guaranteed non-nil here: every path that reaches this line
	// already confirmed establishment (ensureEstablished above), which
	// always caches the certificate even when it does not cache the key.
	p.mu.RLock()
	cert := p.cert
	p.mu.RUnlock()
	if err := checkKeyMatchesCert(key, cert); err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.key = key
	p.mu.Unlock()
	return key, nil
}

// Certificate returns the cached CA certificate, reading ONLY the
// certificate record on a cold cache -- the private-key record is never
// fetched or parsed to answer this call, except on the one-time bootstrap
// path (the store holds no certificate at all yet), where establishing a
// self-signed CA certificate necessarily generates a key too.
func (p *SoftwareProvider) Certificate(ctx context.Context) (*x509.Certificate, error) {
	if p.barrier.IsSealed() {
		return nil, &BarrierSealedError{}
	}

	p.mu.RLock()
	cert := p.cert
	p.mu.RUnlock()
	if cert != nil {
		return cert, nil
	}

	cert, _, err := p.loadCertRecord(ctx)
	switch {
	case err == nil:
		p.mu.Lock()
		p.cert = cert
		p.mu.Unlock()
		return cert, nil
	case isNotInitialized(err):
		if err := p.ensureEstablished(ctx); err != nil {
			return nil, err
		}
		p.mu.RLock()
		cert = p.cert
		p.mu.RUnlock()
		if cert == nil {
			return nil, &NotInitializedError{}
		}
		return cert, nil
	default:
		return nil, err
	}
}

// ensureEstablished runs this provider's one-time bootstrap -- adopting
// in-memory material supplied through WithAdoptMaterial, or generating
// fresh material -- if the store holds none yet. It is the shared tail of
// signingKey's and Certificate's cold-cache paths.
func (p *SoftwareProvider) ensureEstablished(ctx context.Context) error {
	if len(p.adoptKeyPEM) > 0 || len(p.adoptCertPEM) > 0 {
		return p.AdoptIfAbsent(ctx, p.adoptKeyPEM, p.adoptCertPEM)
	}
	return p.Initialize(ctx)
}

// isNotInitialized reports whether err is (or wraps) *NotInitializedError.
func isNotInitialized(err error) bool {
	var notInit *NotInitializedError
	return errors.As(err, &notInit)
}

// barrierCheckedSigner is the crypto.Signer SoftwareProvider.Signer
// returns. It holds the *ecdsa.PrivateKey privately -- there is no
// exported accessor, and its dynamic type is never *ecdsa.PrivateKey, so a
// type assertion against the Signer a caller was handed cannot recover it,
// and Public returns a deep copy rather than a pointer into the key, so a
// caller cannot corrupt (or, via the embedded-struct layout
// *ecdsa.PrivateKey.Public relies on, read back D from) the live key by
// mutating what Public returned. That is the guarantee: ordinary use
// through this type's exported surface cannot extract or corrupt the key.
// It is NOT a guarantee against unsafe.Pointer or reflection reaching into
// this struct's own fields from elsewhere in the SAME process -- no
// exported Go API defends against that, and this type does not claim to.
//
// It also re-checks the barrier on EVERY Sign, not only when Signer was
// fetched, and compares its own captured generation against the
// provider's current one (bumped by Seal): a Signer obtained before
// either Seal or an external reseal refuses once either has run, even
// though the caller is still holding the same crypto.Signer value.
type barrierCheckedSigner struct {
	provider *SoftwareProvider
	key      *ecdsa.PrivateKey
	gen      uint64
}

// Compile-time interface check.
var _ crypto.Signer = (*barrierCheckedSigner)(nil)

// Public returns a deep copy of the signer's public key. Unlike Sign, this
// is not secret and is served regardless of barrier or revocation state,
// mirroring Certificate's own seal-independent contract for public data --
// but it is a COPY, never a pointer into the live *ecdsa.PrivateKey: the
// stdlib's own PrivateKey.Public returns `&priv.PublicKey`, a pointer into
// the same struct the private scalar D lives in, so handing that out
// directly would let a caller corrupt the signer's key by mutating the
// "public" key it received, or walk the embedded-struct layout to reach D.
func (s *barrierCheckedSigner) Public() crypto.PublicKey {
	pub := s.key.PublicKey
	return &ecdsa.PublicKey{
		Curve: pub.Curve,
		X:     new(big.Int).Set(pub.X),
		Y:     new(big.Int).Set(pub.Y),
	}
}

// Sign produces a signature over digest, refusing with *BarrierSealedError
// if the barrier has been sealed since this Signer was obtained, or
// *SignerRevokedError if the provider's Seal has run since -- an
// independent fact from the barrier's own sealed state. Every other
// Provider implementation (Adapter, StaticProvider) is trusted to enforce
// its own equivalent policy; this is SoftwareProvider's.
func (s *barrierCheckedSigner) Sign(rand io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	if s.provider.barrier.IsSealed() {
		return nil, &BarrierSealedError{}
	}
	if s.provider.gen.Load() != s.gen {
		return nil, &SignerRevokedError{}
	}
	return s.key.Sign(rand, digest, opts)
}

// errInvalidCertPEM is the underlying error wrapped by GenerateError when
// AdoptIfAbsent is given a certificate that does not decode as PEM.
var errInvalidCertPEM = staticMaterialError("cakey: adopted certificate is not valid PEM")

// errInvalidKeyPEM is the underlying error wrapped by GenerateError when
// AdoptIfAbsent is given a key that does not decode as SEC1 "EC PRIVATE KEY"
// PEM.
var errInvalidKeyPEM = staticMaterialError("cakey: adopted key is not valid EC PRIVATE KEY PEM")
