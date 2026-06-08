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
	"time"

	quicraftseal "github.com/jeremyhahn/go-quicraft/pkg/seal"
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

// Credentials holds authentication material for seal/unseal operations.
type Credentials = quicraftseal.Credentials

// SealedRootKey is the persisted form of an encrypted root key.
type SealedRootKey = quicraftseal.SealedRootKey

// ShareAccumulator collects shares until the quorum threshold is met.
type ShareAccumulator = quicraftseal.ShareAccumulator

// ShareSplitter splits a secret into M-of-N Shamir shares.
type ShareSplitter = quicraftseal.ShareSplitter

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
// when GOFIPS140 is set, Argon2id (OWASP defaults) otherwise. Both KDF
// adapters are always registered so a FIPS binary can unseal a key that was
// originally sealed by a non-FIPS binary, and vice-versa. The KDF version
// is stored in SealedRootKey.Metadata[0] for cross-version routing.
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
// strategy. It creates a SoftwareStrategy that auto-selects the KDF
// based on FIPS mode: PBKDF2-SHA256 when GOFIPS140 is set, Argon2id otherwise.
var NewSoftwareStrategy = quicraftseal.NewSoftwareStrategy

// NewDeferredSoftwareStrategy creates a SoftwareStrategy that defers passphrase
// binding to Seal/Unseal time. The passphrase is extracted from
// Credentials.Secret on each SealRootKey and UnsealRootKey call, enabling
// scenarios where the passphrase is not known at construction time.
var NewDeferredSoftwareStrategy = quicraftseal.NewDeferredSoftwareStrategy

// EpochBarrier is a type alias for go-quicraft's Barrier. The EpochBarrier
// provides algorithm-agile authenticated encryption with epoch-based key
// rotation, HKDF DEK derivation, and AEAD safety tracking.
type EpochBarrier = quicraftseal.Barrier

// BarrierConfig is a type alias for go-quicraft's BarrierConfig.
type EpochBarrierConfig = quicraftseal.BarrierConfig

// DefaultBarrierConfig is a constructor alias for go-quicraft's
// DefaultBarrierConfig.
var DefaultEpochBarrierConfig = quicraftseal.DefaultBarrierConfig

// InvocationCounter tracks AEAD encrypt operations and enforces limits.
type InvocationCounter = quicraftseal.InvocationCounter

// NonceTrackerProvider provides nonce reuse detection for AEAD ciphers.
type NonceTrackerProvider = quicraftseal.NonceTrackerProvider

// AEADProvider creates cipher.AEAD instances for a given algorithm and key.
type AEADProvider = quicraftseal.AEADProvider

// SymmetricAlgorithm identifies a symmetric encryption algorithm.
type SymmetricAlgorithm = quicraftseal.SymmetricAlgorithm

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

// DefaultShareTTL is qrdb's default time-to-live for an unseal-share quorum
// session when seal.share_ttl is not configured. Submitted shares that do not
// reach the threshold within this window are expired and discarded, requiring
// Security Officers to resubmit. Overridable via the seal.share_ttl config key
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
