// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"errors"
	"fmt"

	quicraftseal "github.com/jeremyhahn/go-quicraft/pkg/seal"
)

var (
	// ErrSealed is returned when an operation is attempted on a sealed barrier.
	ErrSealed = errors.New("qrdb/seal: barrier is sealed")

	// ErrAlreadyUnsealed is returned when attempting to unseal a barrier that
	// is already in the unsealed state.
	ErrAlreadyUnsealed = errors.New("qrdb/seal: barrier is already unsealed")

	// ErrAlreadyInitialized is returned when Initialize is called on a barrier
	// that has already been initialized with a root key.
	ErrAlreadyInitialized = errors.New("qrdb/seal: barrier already initialized")

	// ErrNotInitialized is returned when Unseal is called on a barrier that
	// has not been initialized. Call Initialize first.
	ErrNotInitialized = errors.New("qrdb/seal: barrier not initialized")

	// ErrInvalidCredentials is returned when the provided credentials fail to
	// unseal the root key (wrong password, wrong token, etc).
	ErrInvalidCredentials = errors.New("qrdb/seal: invalid credentials")

	// ErrDecryptionFailed is returned when an unseal decryption operation
	// fails, typically because the supplied passphrase is wrong (the AEAD tag
	// fails to verify). It is wrapped by go-quicraft's *UnsealError, so
	// errors.Is matches through the wrapper. A wrong passphrase is
	// client-supplied bad credential material, not a server fault.
	ErrDecryptionFailed = quicraftseal.ErrDecryptionFailed

	// ErrNoAvailableStrategy is returned when no registered sealing strategy
	// reports itself as available.
	ErrNoAvailableStrategy = errors.New("qrdb/seal: no available sealing strategy")

	// ErrNoStrategySpecified is returned when strategy selection is attempted
	// but no PreferenceOrder was configured. There is no implicit default —
	// the caller must explicitly specify which strategy to use.
	ErrNoStrategySpecified = errors.New("qrdb/seal: no sealing strategy specified (explicit selection required)")

	// ErrStrategyNotFound is returned when a requested strategy ID is not
	// registered with the barrier.
	ErrStrategyNotFound = errors.New("qrdb/seal: requested strategy not registered")

	// ErrStrategyMismatch is returned when attempting to unseal data with a
	// different strategy than the one that sealed it.
	ErrStrategyMismatch = errors.New("qrdb/seal: sealed with different strategy than requested")

	// ErrCorruptRootKey is returned when the root key blob stored in the
	// backend fails to deserialize or has an unexpected format.
	ErrCorruptRootKey = errors.New("qrdb/seal: root key blob corrupted")

	// ErrMigrationInvalidTarget is returned when an encryption migration
	// requests a target strategy that is not a valid migration destination
	// (e.g. migrating to "none", or to the same strategy already in effect).
	ErrMigrationInvalidTarget = errors.New("qrdb/seal: invalid migration target strategy")

	// ErrMigrationSourceNotNone is returned when an enable-encryption migration
	// is attempted on a barrier whose active strategy is not a credential-free
	// source. Valid sources are StrategyNone (plaintext) and StrategySystemBarrier
	// (auto-unseal; the tenant root key is wrapped by the system barrier).
	// User-credential-bound strategies (passphrase, Shamir) are not a valid
	// migration source; changing between keyed strategies is a rekey/re-provision,
	// not this migration path.
	ErrMigrationSourceNotNone = errors.New("qrdb/seal: encryption migration requires a credential-free source barrier (none or system-barrier)")

	// ErrMigrationInvalidMechanism is returned when an unknown re-encryption
	// mechanism is requested.
	ErrMigrationInvalidMechanism = errors.New("qrdb/seal: invalid migration mechanism")

	// ErrMigrationInvalidAvailability is returned when an unknown availability
	// mode is requested.
	ErrMigrationInvalidAvailability = errors.New("qrdb/seal: invalid migration availability mode")

	// ErrMigrationInProgress is returned when a migration is started while a
	// prior migration journal is still present in a non-terminal phase.
	ErrMigrationInProgress = errors.New("qrdb/seal: an encryption migration is already in progress")

	// ErrNilSealedData is returned when a nil SealedData pointer is passed
	// to an unseal operation.
	ErrNilSealedData = quicraftseal.ErrNilSealedData

	// ErrEncryptorNotAvailable is returned when the barrier's encryptor
	// is not available (barrier not unsealed or encryptor not initialized).
	ErrEncryptorNotAvailable = errors.New("qrdb/seal: encryptor not available")

	// ErrHardwareEncryptorRequired is returned when GetMasterKey is called
	// on a hardware-backed strategy that keeps keys in hardware.
	ErrHardwareEncryptorRequired = errors.New("qrdb/seal: master key not exportable from hardware-backed strategy")

	// ErrShamirNotConfigured is returned when a Shamir operation is attempted
	// on a barrier that does not have Shamir secret sharing configured.
	ErrShamirNotConfigured = errors.New("qrdb/seal: shamir not configured")

	// ErrShamirUnsealRequiresQuorum is returned by the Shamir strategy's
	// credential-style UnsealRootKey. Shamir shares are NEVER persisted
	// server-side: they are returned to the Security Officers once at
	// init/rekey and held only in memory during the unseal quorum. A Shamir
	// barrier must therefore be unsealed via the share quorum
	// (UnsealWithShare), not the sealing strategy; there is no disk
	// auto-unseal path.
	ErrShamirUnsealRequiresQuorum = errors.New("qrdb/seal: shamir barrier must be unsealed via the share quorum (UnsealWithShare), not the sealing strategy")

	// ErrShamirThresholdInvalid is returned when the Shamir threshold is less
	// than 2 or greater than the total number of shares.
	ErrShamirThresholdInvalid = quicraftseal.ErrShamirThresholdInvalid

	// ErrShamirDuplicateShare is returned when a share that has already been
	// submitted is submitted again to the quorum accumulator.
	ErrShamirDuplicateShare = errors.New("qrdb/seal: duplicate share submitted")

	// ErrShamirQuorumExpired is returned when the quorum TTL has elapsed and
	// all accumulated shares have been discarded.
	ErrShamirQuorumExpired = errors.New("qrdb/seal: quorum TTL expired, submit shares again")

	// ErrShamirQuorumIncomplete is returned when not enough shares have been
	// provided to meet the reconstruction threshold.
	ErrShamirQuorumIncomplete = quicraftseal.ErrShamirQuorumIncomplete

	// ErrShamirNoQuorum is returned when no active quorum accumulator exists.
	ErrShamirNoQuorum = errors.New("qrdb/seal: no active quorum")

	// ErrShamirCombineFailed is returned when the Shamir combine operation
	// fails to reconstruct the secret from the provided shares.
	ErrShamirCombineFailed = quicraftseal.ErrShamirCombineFailed

	// ErrShamirShareNotFound is returned when a requested Shamir share does
	// not exist in storage.
	ErrShamirShareNotFound = quicraftseal.ErrShamirShareNotFound

	// ErrShamirVerificationFailed is returned when share verification detects
	// inconsistent or corrupted shares.
	ErrShamirVerificationFailed = quicraftseal.ErrShamirVerificationFailed

	// ErrShamirNoSharesFound is returned when no shares exist in storage.
	ErrShamirNoSharesFound = quicraftseal.ErrShamirNoSharesFound

	// ErrShamirStorageFailed is returned when a share storage operation fails.
	ErrShamirStorageFailed = quicraftseal.ErrShamirStorageFailed

	// ErrShamirNilStorage is returned when a nil storage backend is provided
	// to the Shamir strategy constructor.
	ErrShamirNilStorage = quicraftseal.ErrShamirNilStorage

	// ErrShamirSplitFailed is returned when the Shamir secret splitting
	// operation fails.
	ErrShamirSplitFailed = quicraftseal.ErrShamirSplitFailed

	// ErrShamirSerializationFailed is returned when JSON serialization or
	// deserialization of a Shamir share fails.
	ErrShamirSerializationFailed = quicraftseal.ErrShamirSerializationFailed

	// ErrNilSealedBackend is returned when a nil sealed backend is provided.
	ErrNilSealedBackend = errors.New("qrdb/seal: sealed backend must not be nil")

	// ErrSecretNotFound is returned when a named secret is not found.
	ErrSecretNotFound = errors.New("qrdb/seal: secret not found")

	// ErrInvalidSecretName is returned when a secret name is empty.
	ErrInvalidSecretName = errors.New("qrdb/seal: secret name must not be empty")

	// ErrResealFailed is returned when re-sealing a secret fails.
	ErrResealFailed = errors.New("qrdb/seal: reseal failed")

	// ErrRecoveryKeysNotFound is returned when recovery key metadata does
	// not exist in storage.
	ErrRecoveryKeysNotFound = errors.New("qrdb/seal: recovery keys not found")

	// ErrRootTokenVerificationFailed is returned when the reconstructed
	// secret from Shamir shares does not produce a valid DEK.
	ErrRootTokenVerificationFailed = errors.New("qrdb/seal: root token share verification failed")

	// ErrTenantNotFound is returned when a requested tenant ID is not
	// registered in the barrier registry.
	ErrTenantNotFound = errors.New("qrdb/seal: tenant not found")

	// ErrTenantAlreadyExists is returned when attempting to register a
	// tenant that is already registered in the barrier registry.
	ErrTenantAlreadyExists = errors.New("qrdb/seal: tenant already exists")

	// ErrEmptyTenantID is returned when an empty tenant ID is provided
	// to a tenant barrier operation.
	ErrEmptyTenantID = errors.New("qrdb/seal: tenant ID must not be empty")

	// ErrNilSystemBarrier is returned when a nil system barrier is provided
	// to the barrier registry or tenant barrier constructor.
	ErrNilSystemBarrier = errors.New("qrdb/seal: system barrier must not be nil")

	// ErrTenantSealed is returned when an operation is attempted on a
	// sealed tenant barrier.
	ErrTenantSealed = errors.New("qrdb/seal: tenant barrier is sealed")

	// ErrTenantAlreadyInitialized is returned when Initialize is called
	// on a tenant barrier that already has a root key.
	ErrTenantAlreadyInitialized = errors.New("qrdb/seal: tenant barrier already initialized")

	// ErrTenantNotInitialized is returned when Unseal is called on a
	// tenant barrier that has not been initialized.
	ErrTenantNotInitialized = errors.New("qrdb/seal: tenant barrier not initialized")

	// ErrTenantAlreadyUnsealed is returned when Unseal is called on a
	// tenant barrier that is already unsealed.
	ErrTenantAlreadyUnsealed = errors.New("qrdb/seal: tenant barrier is already unsealed")

	// ErrNoStrategy is returned when a tenant is registered without a
	// sealing strategy.
	ErrNoStrategy = errors.New("qrdb/seal: sealing strategy is required")

	// ErrNilStorageBackend is returned when a nil storage backend is
	// provided to the tenant barrier constructor.
	ErrNilStorageBackend = errors.New("qrdb/seal: storage backend must not be nil")

	// ErrNilTenantBarrier is returned when a nil tenant barrier is provided
	// to NewTenantBarrierKVStore.
	ErrNilTenantBarrier = errors.New("qrdb/seal: tenant barrier must not be nil")

	// ErrInvalidPassphrase is returned when an empty password is provided
	// to ChangePassword.
	ErrInvalidPassphrase = errors.New("qrdb/seal: passphrase must not be empty")

	// ErrSealStoreSecretNotFound is returned when a named secret does
	// not exist in the seal store.
	ErrSealStoreSecretNotFound = errors.New("qrdb/seal: sealstore secret not found")

	// ErrSealStoreBarrierSealed is returned when a seal store operation
	// is attempted while the underlying barrier is sealed.
	ErrSealStoreBarrierSealed = errors.New("qrdb/seal: sealstore barrier is sealed")

	// ErrShamirBarrierRequiresQuorum is returned when a single-admin seal is
	// attempted on a Shamir-protected barrier. Use SubmitSealVote instead.
	ErrShamirBarrierRequiresQuorum = errors.New("seal: shamir barrier requires seal-vote quorum")

	// ErrCeremonyDistributorNotConfigured is returned when an operation
	// requires a CeremonyDistributor but none has been set on the barrier.
	ErrCeremonyDistributorNotConfigured = errors.New("seal: ceremony distributor not configured")

	// ErrSealVoteVerificationFailed is returned when the reconstructed secret
	// from seal-vote shares does not verify against the barrier's root key.
	ErrSealVoteVerificationFailed = errors.New("seal: seal vote verification failed")

	// ErrWrappedRootKeyCorrupt is returned when the wrapped root key blob
	// (the root key AEAD-encrypted under the current unseal key) fails to
	// deserialize or has an unexpected format.
	ErrWrappedRootKeyCorrupt = errors.New("qrdb/seal: wrapped root key blob corrupted")

	// ErrUnsealKeyMismatch is returned when the unseal key reconstructed from
	// Shamir shares fails to decrypt the wrapped root key. The submitted
	// shares are wrong, or they were revoked by a prior rekey.
	ErrUnsealKeyMismatch = errors.New("qrdb/seal: reconstructed unseal key failed to decrypt the wrapped root key (wrong shares, or shares revoked by a prior rekey)")

	// ErrPoolNotConfigured is returned by RegisterTenant when called
	// with a nil StorageBackend on a registry that has no volume pool
	// attached. Callers must either supply a backend explicitly or
	// configure the registry with WithVolumePool.
	ErrPoolNotConfigured = errors.New("qrdb/seal: volume pool not configured on barrier registry")

	// ErrPoolBackendNotLocal is returned by RegisterTenant when the
	// volume pool returns a backend that does not satisfy the
	// common.PathAccessor interface. Per-tenant Pebble instances must
	// run on a real local filesystem path; cloud backends are rejected
	// here (or earlier by the LocalOnly wrapper).
	ErrPoolBackendNotLocal = errors.New("qrdb/seal: volume pool returned a non-local backend")

	// ErrPerTenantRegistryNotConfigured is returned by RegisterTenant
	// when called with a nil StorageBackend on a registry that has a
	// volume pool but no PerTenantRegistry attached. Both must be
	// configured for the pooled-tenant flow to work.
	ErrPerTenantRegistryNotConfigured = errors.New("qrdb/seal: per-tenant pebble registry not configured")

	// ErrTenantPathEscapesPool is returned when a tenant id, used as a
	// path segment under a volume-pool root, resolves outside that root
	// (path traversal, e.g. a "../.." component). The tenant service
	// rejects such ids at creation; this is the fail-closed defense at
	// the filesystem sink.
	ErrTenantPathEscapesPool = errors.New("qrdb/seal: tenant path escapes volume pool root")

	// ErrPoolSlotUnresolved is returned internally when a recorded
	// cluster-wide relative slot cannot be mapped to a local pool candidate
	// (the pool is missing, or the slot index is out of range for this node's
	// candidate set). It signals the pooled-allocation path to fall back to a
	// fresh node-local Pick; it is not surfaced to API callers.
	ErrPoolSlotUnresolved = errors.New("qrdb/seal: recorded volume-pool slot cannot be resolved to a local candidate")

	// --- Root-key import seam (multi-node tenant Raft groups) ---.

	// ErrImportedKeyNil is returned when a nil *ImportedKey is passed to
	// ImportRootKey or Validate.
	ErrImportedKeyNil = errors.New("qrdb/seal: imported key must not be nil")

	// ErrImportedRootKeyEmpty is returned when ImportedKey.RootKey is empty.
	ErrImportedRootKeyEmpty = errors.New("qrdb/seal: imported root key must not be empty")

	// ErrImportedSaltInvalid is returned when ImportedKey.Salt is not exactly
	// 32 bytes. The HKDF salt must match the leader's value precisely so that
	// replica DEK derivation is identical.
	ErrImportedSaltInvalid = errors.New("qrdb/seal: imported salt must be exactly 32 bytes")

	// ErrImportedEpochZero is returned when ImportedKey.Epoch is zero. A
	// valid barrier epoch is always >= 1.
	ErrImportedEpochZero = errors.New("qrdb/seal: imported epoch must be greater than zero")

	// ErrImportedEpochNotInKnown is returned when ImportedKey.KnownEpochs
	// does not contain ImportedKey.Epoch. The current epoch must appear in
	// the known-epochs list for the EpochBarrier to validate the sealed blob.
	ErrImportedEpochNotInKnown = errors.New("qrdb/seal: imported KnownEpochs must contain Epoch")

	// ErrImportSealedBlobNil is returned when a nil blob is passed to
	// ImportSealedBlob.
	ErrImportSealedBlobNil = errors.New("qrdb/seal: sealed blob must not be nil")

	// ErrImportSealedBlobInvalid is returned when the blob passed to
	// ImportSealedBlob cannot be unmarshalled to a structurally valid
	// SealedRootKey (missing epoch, wrong salt length, epoch absent from
	// KnownEpochs, or malformed JSON).
	ErrImportSealedBlobInvalid = errors.New("qrdb/seal: sealed blob is not a valid sealed root key")
)

// GetError is returned when a barrier Get operation fails during decryption.
type GetError struct {
	Key string
	Err error
}

// Error returns a human-readable description of the get failure.
func (e *GetError) Error() string {
	return fmt.Sprintf("qrdb/seal: get failed for key %q: %v", e.Key, e.Err)
}

// Unwrap returns the underlying error.
func (e *GetError) Unwrap() error {
	return e.Err
}

// PutError is returned when a barrier Put operation fails during encryption.
type PutError struct {
	Key string
	Err error
}

// Error returns a human-readable description of the put failure.
func (e *PutError) Error() string {
	return fmt.Sprintf("qrdb/seal: put failed for key %q: %v", e.Key, e.Err)
}

// Unwrap returns the underlying error.
func (e *PutError) Unwrap() error {
	return e.Err
}

// SealStoreError is returned when a seal store operation fails.
type SealStoreError struct {
	Op  string // operation name (put, get, delete, list, reseal, status)
	Key string // secret name (empty for list/status)
	Err error  // underlying error
}

// Error returns a human-readable description of the seal store failure.
func (e *SealStoreError) Error() string {
	if e.Key != "" {
		return "sealstore: " + e.Op + " " + e.Key + ": " + e.Err.Error()
	}
	return "sealstore: " + e.Op + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *SealStoreError) Unwrap() error { return e.Err }

// ShamirError wraps a Shamir sentinel (such as ErrShamirSplitFailed or
// ErrShamirCombineFailed) or ErrCorruptRootKey together with the low-level
// cause returned by the secret-sharing library. It preserves errors.Is
// against the sentinel while surfacing the underlying detail, replacing the
// inline "%w: %v" sentinel-wrapping idiom.
type ShamirError struct {
	// Sentinel is the wrapped seal sentinel; errors.Is matches against it.
	Sentinel error
	// Op is an optional sub-operation that failed (e.g. "seal share",
	// "open share"). It may be empty.
	Op string
	// Cause is the underlying error from the secret-sharing library.
	Cause error
}

// Error returns the sentinel message followed by the optional sub-operation
// and the underlying cause.
func (e *ShamirError) Error() string {
	msg := e.Sentinel.Error()
	if e.Op != "" {
		msg += ": " + e.Op
	}
	if e.Cause != nil {
		msg += ": " + e.Cause.Error()
	}
	return msg
}

// Unwrap returns the wrapped sentinel so errors.Is(err, sentinel) succeeds,
// matching the original inline "%w" sentinel wrapping.
func (e *ShamirError) Unwrap() error { return e.Sentinel }

// DataRootMountMissingError is returned by ReattachVolumeTenant (the boot
// re-attach path) when a tenant's persisted barrier store is absent at the
// recorded DataRoot. The record was written on a prior run, so the barrier
// Pebble directory ({dataRoot}/barrier) MUST already exist; when it does not —
// typically because the operator's dedicated disk/mount is not mounted at boot
// — re-attach fails closed with this error rather than silently opening a
// fresh, empty barrier on the underlying root filesystem (a fail-open
// availability and data-confusion hazard).
type DataRootMountMissingError struct {
	TenantID    string
	BarrierPath string
	Err         error
}

// Error returns a human-readable description of the missing-mount failure.
func (e *DataRootMountMissingError) Error() string {
	subject := "qrdb/seal: tenant barrier store"
	if e.TenantID != "" {
		subject = "qrdb/seal: tenant " + e.TenantID + " barrier store"
	}
	msg := subject + " " + e.BarrierPath +
		" is absent on re-attach (dedicated mount not present?); refusing to create a fresh empty barrier"
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error, if any.
func (e *DataRootMountMissingError) Unwrap() error { return e.Err }

// MigrationRecoveryError is returned by RegisterVolumeTenant when the
// configured MigrationRecoverer fails to reconcile an interrupted encryption
// migration recorded in the tenant's journal. Registration fails closed: the
// barrier must not be built over half-swapped stores or an unreconciled root
// key.
type MigrationRecoveryError struct {
	TenantID string
	Err      error
}

// Error returns a human-readable description of the recovery failure.
func (e *MigrationRecoveryError) Error() string {
	return "qrdb/seal: migration recovery for tenant " + e.TenantID + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *MigrationRecoveryError) Unwrap() error { return e.Err }
