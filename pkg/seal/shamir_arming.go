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

package seal

import (
	"bytes"
	"context"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// UnsealKeyCheckSize is the length of an unseal key's check value.
const UnsealKeyCheckSize = 32

// unsealKeyCheckInfo binds the check value's HKDF derivation to its purpose,
// so no other key derived from the unseal key can equal it.
const unsealKeyCheckInfo = "qrdb/seal/unseal-key-check/v1"

// UnsealKeyCheck returns the check value of a Shamir unseal key: HKDF-SHA256
// of the key under a fixed purpose label. The key is 256 bits of fresh
// randomness, so the check value can be replicated and signed without
// helping anyone guess the key; a node that holds no key yet compares the key
// its first quorum reconstructs against it (ArmShamir).
func UnsealKeyCheck(unsealKey []byte) ([]byte, error) {
	if len(unsealKey) != unsealKeySize {
		return nil, &InvalidUnsealKeyError{Length: len(unsealKey)}
	}
	return hkdf.Key(sha256.New, unsealKey, nil, unsealKeyCheckInfo, UnsealKeyCheckSize)
}

// InvalidUnsealKeyError is returned when an unseal key is not 256 bits.
type InvalidUnsealKeyError struct {
	Length int
}

func (e *InvalidUnsealKeyError) Error() string {
	return "qrdb/seal: an unseal key is " + strconv.Itoa(unsealKeySize) + " bytes, got " + strconv.Itoa(e.Length)
}

// InvalidUnsealKeyCheckError is returned by ArmShamir when the check value is
// not UnsealKeyCheckSize bytes.
type InvalidUnsealKeyCheckError struct {
	Length int
}

func (e *InvalidUnsealKeyCheckError) Error() string {
	return "qrdb/seal: an unseal key check is " + strconv.Itoa(UnsealKeyCheckSize) + " bytes, got " + strconv.Itoa(e.Length)
}

// UnsealKeyCheckMismatchError is returned when a quorum on an armed node
// reconstructs a key whose check value is not the init statement's: the
// shares belong to another distribution. The node stays sealed and keyless,
// and keeps nothing from the quorum. It matches ErrUnsealKeyMismatch.
type UnsealKeyCheckMismatchError struct{}

func (e *UnsealKeyCheckMismatchError) Error() string {
	return "qrdb/seal: the holders' quorum reconstructs a key that does not match the init statement's unseal key check"
}

// Unwrap returns ErrUnsealKeyMismatch.
func (e *UnsealKeyCheckMismatchError) Unwrap() error { return ErrUnsealKeyMismatch }

// PreparedShamirMismatchError is returned by CompleteShamir when the signed
// statement's roster or check value is not the split this node prepared.
// Field names which one. The node stays sealed and keeps its prepared key.
type PreparedShamirMismatchError struct {
	Field string
}

func (e *PreparedShamirMismatchError) Error() string {
	return "qrdb/seal: the signed " + e.Field + " does not match the shamir unseal key this node prepared"
}

// shamirArming is what an armed node checks its first unseal quorum against.
type shamirArming struct {
	roster *HolderRoster
	digest []byte
	check  []byte
	// wrapped, when set, runs once the wrapped root key is durable and before
	// anything else can fail: the first node's distribution goes live there.
	wrapped func(context.Context) error
}

// shamirPending is a first node's unseal key between PrepareShamir and
// CompleteShamir. It is held in memory only, so a restart discards it.
type shamirPending struct {
	arming *shamirArming
	key    *secure.Buffer
	// params is what PrepareShamir returned, which the owner signs.
	params ShamirInitParams
}

// ErrShamirNotPrepared is returned by CompleteShamir when this node holds no
// unseal key from PrepareShamir: none was prepared, it was completed already,
// or the node restarted since.
var ErrShamirNotPrepared = errors.New("qrdb/seal: no prepared shamir unseal key on this node; prepare the barrier again")

// PrepareShamir is the first half of the first node's Shamir initialization.
// It generates the unseal key, splits it to the holders the distributor
// enrols, and returns its check value and the encoded roster, which go into
// the owner's signed init statement. The unseal key stays in this node's
// memory, never on disk, until CompleteShamir; another PrepareShamir
// replaces it, and a restart discards it.
func (b *StorageBarrier) PrepareShamir(ctx context.Context, policy FactorPolicy) (*ShamirInitParams, error) {
	cfg := b.shamirConfig()
	if cfg == nil || b.ShamirStrategy() == nil {
		return nil, ErrShamirNotConfigured
	}
	if b.distributor == nil {
		return nil, ErrCeremonyDistributorNotConfigured
	}
	if cfg.Threshold < 2 || cfg.Threshold > cfg.TotalShares {
		return nil, ErrShamirThresholdInvalid
	}
	keyed, err := b.base.Exists(ctx, b.config.RootKeyPath)
	if err != nil {
		return nil, &ShamirError{Sentinel: ErrCorruptRootKey, Cause: err}
	}
	if keyed {
		return nil, ErrAlreadyInitialized
	}
	if !policy.Valid() {
		return nil, &InvalidRosterError{Reason: "unknown factor policy " + string(policy)}
	}
	// The distributor stages one share set per domain: serialise against
	// any other distribution of it, exactly as redistributeTo does.
	release, err := b.lockRedistribution(ctx, DomainUnseal)
	if err != nil {
		return nil, err
	}
	defer release()
	roster, err := b.distributor.PrepareRoster(ctx, DomainUnseal, cfg.Threshold, policy.OrDefault(), nil)
	if err != nil {
		return nil, err
	}
	if roster.Total != cfg.TotalShares {
		return nil, &HolderCountMismatchError{Want: cfg.TotalShares, Enrolled: roster.Total}
	}
	distribution := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, distribution); err != nil {
		return nil, err
	}
	roster.Distribution = hex.EncodeToString(distribution)
	raw, digest, err := encodeRoster(roster)
	if err != nil {
		return nil, err
	}
	key, err := newUnsealKey()
	if err != nil {
		return nil, err
	}
	var check []byte
	if err := key.WithBytes(func(k []byte) error {
		var cerr error
		check, cerr = UnsealKeyCheck(k)
		return cerr
	}); err != nil {
		key.Destroy()
		return nil, err
	}
	count, err := b.distributor.Distribute(ctx, key, roster, digest)
	if err != nil {
		key.Destroy()
		return nil, err
	}
	a := &shamirArming{roster: roster, digest: digest, check: check}
	a.wrapped = func(ctx context.Context) error { return b.distributor.Committed(ctx, roster, nil) }
	params := ShamirInitParams{
		Threshold:      roster.Threshold,
		TotalShares:    roster.Total,
		ShareCount:     count,
		UnsealKeyCheck: check,
		Roster:         raw,
	}
	if old := b.pending.Swap(&shamirPending{arming: a, key: key, params: params}); old != nil {
		old.key.Destroy()
	}
	return params.clone(), nil
}

// PreparedShamir returns what PrepareShamir returned for the split
// CompleteShamir has not used yet, or false when there is none. It carries no
// key material.
func (b *StorageBarrier) PreparedShamir() (*ShamirInitParams, bool) {
	p := b.pending.Load()
	if p == nil {
		return nil, false
	}
	return p.params.clone(), true
}

func (p ShamirInitParams) clone() *ShamirInitParams {
	p.UnsealKeyCheck = bytes.Clone(p.UnsealKeyCheck)
	p.Roster = bytes.Clone(p.Roster)
	return &p
}

// CompleteShamir is the second half of the first node's Shamir
// initialization, run once the owner's statement carrying PrepareShamir's
// check value is claimed. It generates this node's root key, wraps it under
// the prepared unseal key with the roster's digest in the AAD, puts the
// holders' shares live, and leaves the barrier unsealed. A failure before the
// wrapped key is durable keeps the prepared key for another attempt.
//
// roster and check are the Roster and UnsealKeyCheck the owner signed. They
// must be exactly what PrepareShamir returned: a statement naming another
// split is refused with PreparedShamirMismatchError and the node stays
// sealed, keeping the prepared key for the right statement.
func (b *StorageBarrier) CompleteShamir(ctx context.Context, roster, check []byte) error {
	p := b.pending.Load()
	if p == nil {
		return ErrShamirNotPrepared
	}
	if subtle.ConstantTimeCompare(check, p.arming.check) != 1 {
		return &PreparedShamirMismatchError{Field: "unseal key check"}
	}
	if sum := sha256.Sum256(roster); !bytes.Equal(sum[:], p.arming.digest) {
		return &PreparedShamirMismatchError{Field: "roster"}
	}
	if err := p.key.WithBytes(func(k []byte) error { return b.keyFromArming(ctx, p.arming, k) }); err != nil {
		return err
	}
	if b.pending.CompareAndSwap(p, nil) {
		p.key.Destroy()
	}
	return nil
}

// ShamirPrepared reports whether this node holds a prepared unseal key that
// CompleteShamir has not used yet.
func (b *StorageBarrier) ShamirPrepared() bool {
	return b.pending.Load() != nil
}

// ArmShamir prepares a Shamir barrier that holds no key yet to take its first
// key from its holders' first unseal quorum. roster is the encoded roster the
// first node split the unseal key to, and check that key's UnsealKeyCheck,
// both from the owner's signed init statement.
//
// At the quorum the node reconstructs the unseal key and compares its check
// value with check. On a match it generates its OWN root key, wraps it under
// the unseal key with roster's digest in the AAD, and unseals; on a mismatch
// it refuses with UnsealKeyCheckMismatchError and stays sealed and keyless.
// The unseal key is never relayed between nodes, and the arming holds no key
// material: it is rebuilt from the statement after a restart.
//
// Arming a barrier that already holds a key is a no-op, so the call is
// idempotent across the statement watcher's polls.
func (b *StorageBarrier) ArmShamir(ctx context.Context, roster, check []byte) error {
	if b.ShamirStrategy() == nil {
		return ErrShamirNotConfigured
	}
	if len(check) != UnsealKeyCheckSize {
		return &InvalidUnsealKeyCheckError{Length: len(check)}
	}
	decoded, digest, err := decodeRoster(roster)
	if err != nil {
		return err
	}
	if decoded.Domain != DomainUnseal {
		return &InvalidRosterError{Reason: "an armed roster must belong to the unseal domain"}
	}
	keyed, err := b.base.Exists(ctx, b.config.RootKeyPath)
	if err != nil {
		return err
	}
	if keyed {
		return nil
	}
	b.arming.Store(&shamirArming{roster: decoded, digest: digest, check: bytes.Clone(check)})
	return nil
}

// ShamirArmed reports whether the barrier is armed and still waiting for the
// unseal quorum that gives it its first key.
func (b *StorageBarrier) ShamirArmed() bool {
	return b.arming.Load() != nil
}

// ArmTenantShamir arms tenantID's barrier on this node from the owner's init
// statement: it registers the tenant's Shamir barrier with the roster's
// threshold and total when the node has none, switches a keyless shell
// registered under another strategy to Shamir, and arms it (ArmShamir). A
// barrier this node has already keyed is not armed again, but a cold one
// rebuilt without its Shamir strategy (a restart rebuilds it from the system's
// strategies) gets it back from the scheme its persisted key records, so the
// holders' next quorum can unseal it.
func (r *BarrierRegistry) ArmTenantShamir(ctx context.Context, tenantID string, roster, check []byte) error {
	decoded, _, err := decodeRoster(roster)
	if err != nil {
		return err
	}
	tb, err := r.GetTenant(tenantID)
	if err != nil {
		if tb, err = r.RegisterTenantPooledShamir(ctx, tenantID, decoded.Threshold, decoded.Total, 0); err != nil {
			return err
		}
		return tb.Barrier().ArmShamir(ctx, roster, check)
	}
	if tb.Barrier().ShamirStrategy() == nil {
		sealed, err := tb.Barrier().persistedRootKey(ctx)
		if err != nil {
			return err
		}
		threshold, total := decoded.Threshold, decoded.Total
		switch {
		case sealed == nil:
		case sealed.Strategy != StrategyShamir:
			return nil
		case sealed.ShamirThreshold > 0:
			threshold, total = sealed.ShamirThreshold, sealed.ShamirTotal
		}
		if err := r.ReconfigureTenantShamir(tenantID, threshold, total, 0); err != nil {
			return err
		}
	}
	if !tb.Barrier().Status().InitializedAt.IsZero() {
		return nil
	}
	return tb.Barrier().ArmShamir(ctx, roster, check)
}

// persistedRootKey returns the sealed root key the barrier has persisted, or
// nil when it has none yet.
func (b *StorageBarrier) persistedRootKey(ctx context.Context) (*SealedRootKey, error) {
	keyed, err := b.base.Exists(ctx, b.config.RootKeyPath)
	if err != nil || !keyed {
		return nil, err
	}
	blob, err := b.base.Get(ctx, b.config.RootKeyPath)
	if err != nil {
		return nil, err
	}
	var sealed SealedRootKey
	if err := json.Unmarshal(blob, &sealed); err != nil {
		return nil, ErrCorruptRootKey
	}
	return &sealed, nil
}

// quorumRoster returns the roster and digest a submission for domain and op
// is admitted against: the one wrapped with this node's key or, on an armed
// node that has none yet, the armed roster for an unseal.
func (b *StorageBarrier) quorumRoster(ctx context.Context, domain QuorumDomain, op QuorumOp) (*HolderRoster, []byte, error) {
	_, roster, digest, err := b.loadWrapped(ctx, domain)
	if errors.Is(err, ErrHolderRosterMissing) && domain == DomainUnseal && op == OpUnseal {
		if a := b.arming.Load(); a != nil {
			return a.roster, a.digest, nil
		}
	}
	return roster, digest, err
}

// armingFor returns the arming a completed round takes this node's first key
// from, or nil when the round resolves an existing key.
func (b *StorageBarrier) armingFor(round *quorumRound) *shamirArming {
	a := b.arming.Load()
	if a == nil || round.domain != DomainUnseal || round.op != OpUnseal || !bytes.Equal(round.rosterDigest, a.digest) {
		return nil
	}
	return a
}

// keyFromArming checks the reconstructed unseal key against the arming, then
// generates this node's root key, wraps it under the unseal key and leaves the
// barrier unsealed. A failure after the key exists rolls it back, so the node
// stays armed for another quorum.
func (b *StorageBarrier) keyFromArming(ctx context.Context, a *shamirArming, unsealKey []byte) error {
	check, err := UnsealKeyCheck(unsealKey)
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare(check, a.check) != 1 {
		b.logger.Warn("share-holder quorum refused: the reconstructed key does not match the init statement's check value",
			slog.String("root_key_path", b.config.RootKeyPath))
		return &UnsealKeyCheckMismatchError{}
	}
	aead, err := b.unsealKeyAEAD(unsealKey)
	if err != nil {
		return err
	}
	if err := b.initializeShamirKey(ctx); err != nil {
		return err
	}
	if err := b.wrapArmedRootKey(ctx, a, aead); err != nil {
		if rbErr := b.rollbackInitialization(ctx); rbErr != nil {
			return &ShamirInitRollbackError{Distribution: err, Rollback: rbErr}
		}
		return err
	}
	b.arming.CompareAndSwap(a, nil)
	b.logger.Info("shamir barrier keyed by its holders' first quorum",
		slog.String("root_key_path", b.config.RootKeyPath))
	return nil
}

// wrapArmedRootKey wraps the freshly generated root key under the unseal key
// with the armed roster, persists it and records the Shamir parameters.
func (b *StorageBarrier) wrapArmedRootKey(ctx context.Context, a *shamirArming, aead cipher.AEAD) error {
	rootKey, err := b.masterKeyBuffer(ctx)
	if err != nil {
		return err
	}
	defer rootKey.Destroy()
	var blob, digest []byte
	if err := rootKey.WithBytes(func(rk []byte) error {
		var werr error
		blob, digest, werr = b.wrapRootKeyWith(DomainUnseal, aead, rk, a.roster)
		return werr
	}); err != nil {
		return err
	}
	if !bytes.Equal(digest, a.digest) {
		return &InvalidRosterError{Reason: "the armed roster does not re-encode to the digest it was armed with"}
	}
	if err := b.base.Put(ctx, b.wrappedKeyPath(DomainUnseal), blob); err != nil {
		return err
	}
	if a.wrapped != nil {
		if err := a.wrapped(ctx); err != nil {
			return err
		}
	}
	return b.recordShamirParams(ctx, a.roster.Threshold, a.roster.Total)
}
