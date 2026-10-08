// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package seal provides barrier encryption for go-qrdb's storage layer.
// The StorageBarrier wraps a StorageBackend with transparent authenticated
// encryption, supporting multiple sealing strategies (passphrase, Shamir,
// hardware via go-xkms), multi-tenant isolation, and disaster recovery
// operations.
//
// Core types (StrategyID, SealingStrategy, SymmetricEncrypter, etc.) are
// defined in go-quicraft/pkg/seal and re-exported here as type aliases so
// that go-qrdb consumers do not need to import go-quicraft directly.
package seal

import (
	"context"
	"time"

	quicraftseal "github.com/jeremyhahn/go-quicraft/pkg/seal"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// --- Re-exported types from go-quicraft/pkg/seal ---

// StrategyID uniquely identifies a sealing strategy.
type StrategyID = quicraftseal.StrategyID

// SealingStrategy defines the interface for root key seal/unseal operations.
type SealingStrategy = quicraftseal.SealingStrategy

// SymmetricEncrypter provides symmetric encryption/decryption operations.
type SymmetricEncrypter = quicraftseal.SymmetricEncrypter

// StorageBackend is a context-aware interface for key-value storage using
// string keys. All methods accept context.Context and use string keys.
type StorageBackend = quicraftseal.StorageBackend

// Credentials holds authentication material for seal/unseal operations. The
// secret is protected memory — a *enclave.SealedEnclave, not a []byte — so a
// caller cannot hand the barrier a passphrase that also lives on the heap.
// Build one with NewCredentials; the zero value means "no credentials
// supplied", which is what strategies that ignore credentials take.
//
// The caller OWNS the credential: destroy it (DestroyCredentials, nil-safe)
// once the seal or unseal it was built for has returned. Nothing downstream
// destroys it, because a credential that is passed through two layers would
// otherwise be released by the first to finish with it.
type Credentials = quicraftseal.Credentials

// NewCredentials moves a secret into protected memory and returns the
// Credentials holding it. The source slice is WIPED on return, success or
// failure — that is the point of the constructor: material that reaches here
// already existed unprotected, and the only thing left worth doing about it is
// not leaving it there. An empty secret is refused with go-quicraft's own
// seal.ErrInvalidPassphrase — a different sentinel from this package's
// ErrInvalidPassphrase, which ChangePassword returns for the same fault.
//
// It allocates locked pages the kernel can refuse
// (enclave.ErrLockedMemoryUnavailable), and on a GOEXPERIMENT=boringcrypto
// build it ALWAYS fails with enclave.ErrSealFailed: go-enclave seals through
// cipher.NewGCMWithRandomNonce, which requires the standard library's AES
// block, and boringcrypto's is a different type. FIPS-certified crypto is
// GOFIPS140=latest, which is unaffected.
var NewCredentials = quicraftseal.NewCredentials

// DestroyCredentials releases creds' secret and logs any problem the release
// reports (secure.LogRelease): it is how a caller destroys a credential it
// owns, typically deferred, where creds.Secret.Destroy() would drop the
// error. It is nil-safe and idempotent, like the Destroy it wraps, and a zero
// Credentials is a no-op.
func DestroyCredentials(creds Credentials) {
	secure.LogRelease("seal credentials", creds.Secret.Destroy())
}

// MustCredentials is NewCredentials for fixtures: a test's passphrase, a
// constant a program cannot run without. It panics on the error NewCredentials
// would return, which at such a site is a programming error or a host that
// cannot lock memory at all — not a condition the caller has a way to recover
// from. Production code takes secrets from callers and uses NewCredentials.
func MustCredentials(ctx context.Context, secret []byte) Credentials {
	creds, err := NewCredentials(ctx, secret)
	if err != nil {
		panic("qrdb/seal: MustCredentials: " + err.Error())
	}
	return creds
}

// CredentialsFor builds Credentials from a secret the caller has BORROWED — a
// request field, a config value, an SDK argument the documented contract says
// the caller owns. The secret is copied into protected memory and left exactly
// as it was: whoever handed it over keeps what they had and stays responsible
// for wiping it, and a transport that treats its request as a borrow (every
// one of them does; see sdk/go/transport BarrierInitRequest) is not turned into
// one that consumes it by the service beneath it. NewCredentials is the
// consuming form, for material the caller made and will not read again.
//
// A secret that MAY be absent is the other reason this exists: zero-length
// material yields the zero Credentials — "no credentials supplied", which is
// what the none strategy takes and what a passphrase strategy refuses with its
// own error — so the empty case is decided in one place rather than at every
// site that turns an optional secret into a credential, and no such site can
// seal an empty slice and turn "absent" into an error naming the wrong fault.
//
// The caller owns the result: destroy it when the operation it was built for
// returns.
func CredentialsFor(ctx context.Context, secret []byte) (Credentials, error) {
	if len(secret) == 0 {
		return Credentials{}, nil
	}
	return NewCredentials(ctx, append([]byte(nil), secret...))
}

// SealedRootKey is the persisted form of an encrypted root key.
type SealedRootKey = quicraftseal.SealedRootKey

// HKDFVersion identifies the HKDF info-string layout used to derive
// epoch DEKs from the root key. Replicas must derive with the same
// layout as the leader or DEKs diverge.
type HKDFVersion = quicraftseal.HKDFVersion

// ShareAccumulator collects protected Shamir shares until the quorum
// threshold is met, holding every one in protected memory on the way in and
// returning the reconstructed secret in an enclave on the way out.
//
// It aliases go-quicraft's ProtectedShareAccumulator. The string-valued
// sibling that existed through go-quicraft v0.8.0-alpha was removed upstream
// (go-quicraft #35/#37): an accumulator that took a Go string still admitted
// an immortal, un-wipeable heap copy of the share, which is exactly the
// exposure the ProtectedShare family exists to close.
type ShareAccumulator = quicraftseal.ProtectedShareAccumulator

// ShareSplitter splits a secret into M-of-N Shamir shares whose values live in
// protected memory. It aliases go-quicraft's ProtectedShareSplitter; the
// string-valued sibling was removed upstream for the reason above.
type ShareSplitter = quicraftseal.ProtectedShareSplitter

// QuorumProgress reports the current state of a quorum accumulator.
type QuorumProgress = quicraftseal.QuorumProgress

// ShamirInitResult contains the output of Shamir initialization.
type ShamirInitResult = quicraftseal.ShamirInitResult

// ShamirConfig configures Shamir secret sharing for barrier operations.
type ShamirConfig = quicraftseal.ShamirConfig

// SoftwareStrategy is a type alias for go-quicraft's SoftwareStrategy.
// This enables go-qrdb consumers to use the passphrase (software) sealing
// strategy without importing go-quicraft directly.
//
// The software strategy auto-selects the KDF at construction time via
// DefaultKDFVersion(): PBKDF2-SHA256 (600,000 iterations, NIST SP 800-132)
// in FIPS 140 mode (see FIPSEnabled), Argon2id (OWASP defaults) otherwise.
// The KDF version is stored in SealedRootKey.Metadata[0] for cross-version
// routing, and the routing is one-way: a PBKDF2 blob unseals identically in
// either mode, but a blob wrapped under Argon2id is REFUSED while FIPS is
// active (ErrKDFNotFIPSApproved — unsealing re-derives the wrapping key with a
// KDF the mode forbids) and cannot be derived at all in a -tags fips build,
// which carries no Argon2id deriver. Migrate by unsealing once with FIPS off
// and re-sealing with NewSoftwareStrategyFIPS.
//
// Strategy preference order (highest to lowest):
//
//	TPM2 > PKCS#11 > Passphrase (Software)
//
// qrdb owns only the Passphrase/Software baseline (and the StrategyNone
// passthrough). Hardware-backed strategies (TPM2, PKCS#11, cloud KMS, Vault)
// live exclusively in go-xkms and are injected at composition time via the
// server option WithStrategyRegistry. The preference order above is the
// canonical ordering applied at runtime when multiple strategies are
// registered; when only Passphrase (or only None) is registered, that
// strategy is selected unconditionally.
type SoftwareStrategy = quicraftseal.SoftwareStrategy

// NewSoftwareStrategy is a constructor alias for go-quicraft's software
// strategy. It creates a SoftwareStrategy that auto-selects the KDF based on
// FIPS mode: PBKDF2-SHA256 in FIPS 140 mode, Argon2id otherwise.
//
// The returned strategy BINDS ITS PASSPHRASE AT CONSTRUCTION AND IGNORES THE
// CREDENTIALS PASSED TO SealRootKey/UnsealRootKey. UnsealRootKey therefore
// returns the root key for ANY credentials, including wrong ones, and a nil
// error from it is not evidence that the caller supplied the right passphrase.
// Use NewDeferredSoftwareStrategy for a barrier that authenticates its
// unsealer; that is what pkg/database registers and what ChangePassword
// installs, and TestSoftwareStrategyBindingModes_CredentialSemantics pins both
// halves of this comment against the code.
//
// A TEST ASSERTING THAT A WRONG CREDENTIAL IS REJECTED MUST USE THE DEFERRED
// CONSTRUCTOR. Against a bound strategy that assertion cannot hold at all: the
// bound strategy accepts the wrong credential, so the test goes red over its
// own fixture rather than over the code it meant to cover. Do not read that red
// as the rejection test being the wrong test — it is the right test on the
// wrong constructor.
//
// The shape that proves nothing is the opposite one: a test asserting only that
// the root key comes back passes against BOTH constructors, so it says nothing
// about credential validation. That is how an entire pkg/database suite stayed
// green over a barrier that checked nothing.
//
// Measured three times: the #157 production bypass (after a password change
// every passphrase unsealed the barrier), that green pkg/database suite, and a
// #158 fixture whose wrong-passphrase assertion went red because the bound
// fixture it was built on accepted the wrong passphrase (#162).
var NewSoftwareStrategy = quicraftseal.NewSoftwareStrategy

// NewSoftwareStrategyWithParams is NewSoftwareStrategy with explicit Argon2id
// work factors (time, memory in KiB, threads). It selects Argon2id explicitly
// and is therefore refused under FIPS 140 (ErrKDFNotFIPSApproved at runtime,
// ErrKDFUnsupportedVersion in a -tags fips build); NewSoftwareStrategyFIPS is
// the tunable PBKDF2 constructor for that leg.
var NewSoftwareStrategyWithParams = quicraftseal.NewSoftwareStrategyWithParams

// NewSoftwareStrategyFIPS is NewSoftwareStrategy with an explicit PBKDF2-SHA256
// iteration count; accepted under every FIPS policy.
var NewSoftwareStrategyFIPS = quicraftseal.NewSoftwareStrategyFIPS

// KDFVersion identifies the passphrase KDF a software strategy derives with
// (KDFArgon2id or KDFPbkdf2SHA256). It is recorded in the sealed root-key blob
// so an unseal routes to the KDF that sealed it — subject to policy: an
// Argon2id blob is refused while FIPS is active, and a PBKDF2 blob opens in
// either mode (see SoftwareStrategy).
type KDFVersion = quicraftseal.KDFVersion

// KDFParams carries a software strategy's work factor. It is NOT recorded in
// the sealed blob: a blob sealed at a non-default factor can only be unsealed
// by a strategy carrying the same one, which is why ChangePassword reads it
// off the strategy being replaced.
type KDFParams = quicraftseal.KDFParams

const (
	// KDFArgon2id is the standard (non-FIPS) passphrase KDF.
	KDFArgon2id = quicraftseal.KDFArgon2id
	// KDFPbkdf2SHA256 is the FIPS 140 passphrase KDF.
	KDFPbkdf2SHA256 = quicraftseal.KDFPbkdf2SHA256
)

// FIPSEnabled is the single FIPS 140 mode predicate for the stack,
// re-exported from go-quicraft/pkg/seal. Detection keys off the Go runtime's
// FIPS 140-3 module state (crypto/fips140.Enabled) first and falls back to
// the GOFIPS140 environment variable for deployments that signal FIPS intent
// that way; the explicit value "off" is honored as disabled rather than
// misread as enabled.
//
// Every FIPS-conditional path in go-qrdb routes through this one predicate —
// SO PIN hashing (pkg/auth), PIN hash configuration (pkg/pin) and KDF
// selection (pkg/seal/kdf) — so a given environment yields one consistent
// answer across the process.
var FIPSEnabled = quicraftseal.FIPSEnabled

// NewDeferredSoftwareStrategy creates a SoftwareStrategy that defers passphrase
// binding to Seal/Unseal time. The passphrase is extracted from
// Credentials.Secret on each SealRootKey and UnsealRootKey call, enabling
// scenarios where the passphrase is not known at construction time.
//
// This is the constructor that VALIDATES credentials: an unseal with the wrong
// secret fails, where NewSoftwareStrategy's bound strategy would succeed. Use
// it wherever the barrier is expected to authenticate the caller that unseals
// it, and in any test asserting that it does.
//
// It takes no KDF parameters, so a deferred strategy always carries the library
// defaults for the selected algorithm. A tuned work factor is carried across a
// strategy replacement by NewDeferredSoftwareStrategyWithParams instead.
var NewDeferredSoftwareStrategy = quicraftseal.NewDeferredSoftwareStrategy

// NewDeferredSoftwareStrategyWithParams is NewDeferredSoftwareStrategy carrying
// an explicit KDF version and work factor, read off the strategy being
// replaced with KDFVersion() and KDFParams(). It exists for the replacement
// case (go-quicraft#72, requested by #156): a sealed root-key blob records the
// KDF VERSION byte and never the params, so a blob sealed at a non-default work
// factor can only be unsealed by a strategy carrying that same factor — the
// live strategy object is the only place the number exists, and a replacement
// built with the no-argument constructor silently resets a hardened deployment
// to the defaults. FIPS behaviour matches the bound constructors: a -tags fips
// build has no Argon2id deriver and answers ErrKDFUnsupportedVersion; runtime
// FIPS answers ErrKDFNotFIPSApproved.
var NewDeferredSoftwareStrategyWithParams = quicraftseal.NewDeferredSoftwareStrategyWithParams

// EpochBarrier is a type alias for go-quicraft's Barrier. The EpochBarrier
// provides algorithm-agile authenticated encryption with epoch-based key
// rotation, HKDF DEK derivation, and AEAD safety tracking.
type EpochBarrier = quicraftseal.Barrier

// BarrierConfig is a type alias for go-quicraft's BarrierConfig.
type EpochBarrierConfig = quicraftseal.BarrierConfig

// DefaultBarrierConfig is a constructor alias for go-quicraft's
// DefaultBarrierConfig.
var DefaultEpochBarrierConfig = quicraftseal.DefaultBarrierConfig

// NewEpochBarrier is a constructor alias for go-quicraft's NewBarrier. It
// completes the EpochBarrier alias set: callers that already use the
// EpochBarrier type and EpochBarrierConfig can construct one without reaching
// past this package into go-quicraft directly.
//
// It returns an error for an invalid configuration (unknown algorithm, or a
// non-FIPS-approved cipher under FIPS mode) rather than panicking, because a
// barrier config assembled at runtime from a file or per-tenant settings is
// the normal case here.
//
// The returned barrier is sealed. It must be Initialize-d (or unsealed) before
// use — a WAL opened over a sealed barrier is rejected, because a sealed
// barrier cannot report whether it authenticates caller context.
var NewEpochBarrier = quicraftseal.NewBarrier

// MustNewEpochBarrier is a constructor alias for go-quicraft's
// MustNewBarrier. Use it only when the configuration is built entirely from
// compile-time constants, where an invalid value is a programmer error.
var MustNewEpochBarrier = quicraftseal.MustNewBarrier

// InvocationCounter tracks AEAD encrypt operations and enforces limits.
type InvocationCounter = quicraftseal.InvocationCounter

// NonceTrackerProvider provides nonce reuse detection for AEAD ciphers.
type NonceTrackerProvider = quicraftseal.NonceTrackerProvider

// AEADProvider creates cipher.AEAD instances for a given algorithm and key.
type AEADProvider = quicraftseal.AEADProvider

// SymmetricAlgorithm identifies a symmetric encryption algorithm.
type SymmetricAlgorithm = quicraftseal.SymmetricAlgorithm

// AlgAES256GCM selects AES-256-GCM (32-byte key, 12-byte nonce). Used as the
// fixed algorithm for the unseal-key root-key wrap.
const AlgAES256GCM = quicraftseal.AlgAES256GCM

// NewInvocationCounter creates an InMemoryInvocationCounter.
var NewInvocationCounter = quicraftseal.NewInvocationCounter

// NewNonceTracker creates an InMemoryNonceTracker.
var NewNonceTracker = quicraftseal.NewNonceTracker

// NewSoftwareAEADProvider creates a software AEAD provider.
var NewSoftwareAEADProvider = quicraftseal.NewSoftwareAEADProvider

// ErrInvocationLimitExceeded is returned when the AEAD invocation limit is exceeded.
var ErrInvocationLimitExceeded = quicraftseal.ErrInvocationLimitExceeded

// ErrNonceReuse is returned when nonce reuse is detected.
var ErrNonceReuse = quicraftseal.ErrNonceReuse

// --- Re-exported constants ---

// go-qrdb names only the strategies it implements (shamir) plus the L1
// strategies it aliases (none/passphrase). It does NOT define hardware/cloud
// strategy IDs — those are owned by go-xkms (the implementing layer). The
// barrier registry routes by the StrategyID string read from config / the
// sealed blob, so go-qrdb never needs the hardware/cloud IDs as named consts.
const (
	// StrategyShamir splits the root key into M-of-N Shamir shares (impl: go-qrdb).
	StrategyShamir StrategyID = "shamir"
)

const (
	// StrategyPassphrase is the canonical operator-facing identifier for the
	// passphrase-protected software seal strategy (Argon2id / PBKDF2-FIPS via
	// the deferred software strategy). Implemented by go-quicraft (L1); aliased
	// here so there is a single source of truth for the on-disk and AAD string.
	StrategyPassphrase = quicraftseal.StrategyPassphrase

	// StrategyNone disables barrier encryption (impl: go-quicraft L1).
	StrategyNone = quicraftseal.StrategyNone
)

// IsPassphraseStrategy reports whether id selects the passphrase-protected
// software seal strategy.
func IsPassphraseStrategy(id StrategyID) bool {
	return id == StrategyPassphrase
}

// NewNoneStrategy is a constructor alias for go-quicraft's no-encryption
// barrier strategy. Selecting this strategy disables barrier-level
// encryption entirely; data is stored plaintext. Intended for dev/test or
// deployments that rely on OS-level full-disk encryption.
var NewNoneStrategy = quicraftseal.NewNoneStrategy

// DefaultQuorumTTL is the default time-to-live for a quorum accumulator
// session, re-exported from go-quicraft.
const DefaultQuorumTTL = quicraftseal.DefaultQuorumTTL

// DefaultShareTTL is qrdb's default time-to-live for a share-holder quorum
// round when seal.share_ttl is not configured. Submitted shares that do not
// reach the threshold within this window are expired and wiped, requiring the
// holders to resubmit. Overridable via the seal.share_ttl config key
// (env QRDB_SEAL_SHARE_TTL).
const DefaultShareTTL = 15 * time.Minute

// BarrierStatus reports the current state of a StorageBarrier.
type BarrierStatus struct {
	// Sealed is true when the barrier is in sealed state.
	Sealed bool `json:"sealed"`

	// Strategy identifies which sealing strategy protects the root key.
	Strategy StrategyID `json:"strategy"`

	// HardwareBacked is true when the active strategy uses dedicated
	// cryptographic hardware.
	HardwareBacked bool `json:"hardware_backed"`

	// InitializedAt records when the barrier was first initialized.
	InitializedAt time.Time `json:"initialized_at"`

	// ShamirThreshold is the minimum number of Shamir shares required to
	// unseal. Zero when the active strategy is not Shamir.
	ShamirThreshold int `json:"shamir_threshold,omitempty"`

	// ShamirTotal is the total number of Shamir shares generated at
	// initialization or last rekey. Zero when the active strategy is not Shamir.
	ShamirTotal int `json:"shamir_total,omitempty"`
}
