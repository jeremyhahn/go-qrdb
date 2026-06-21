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

package sdk

import (
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	quicraftseal "github.com/jeremyhahn/go-quicraft/pkg/seal"
)

// ---------------------------------------------------------------------------
// Seal sentinel errors
// ---------------------------------------------------------------------------

var (
	// ErrSealed is returned when an operation is attempted on a sealed barrier.
	ErrSealed = seal.ErrSealed

	// ErrAlreadyUnsealed is returned when unsealing an already-unsealed barrier.
	ErrAlreadyUnsealed = seal.ErrAlreadyUnsealed

	// ErrAlreadyInitialized is returned when initializing an already-initialized barrier.
	ErrAlreadyInitialized = seal.ErrAlreadyInitialized

	// ErrNotInitialized is returned when unsealing an uninitialized barrier.
	ErrNotInitialized = seal.ErrNotInitialized

	// ErrInvalidCredentials is returned when credentials fail to unseal the root key.
	ErrInvalidCredentials = seal.ErrInvalidCredentials

	// ErrNoAvailableStrategy is returned when no sealing strategy is available.
	ErrNoAvailableStrategy = seal.ErrNoAvailableStrategy

	// ErrStrategyNotFound is returned when a requested strategy ID is not registered.
	ErrStrategyNotFound = seal.ErrStrategyNotFound

	// ErrStrategyMismatch is returned when unsealing with a different strategy.
	ErrStrategyMismatch = seal.ErrStrategyMismatch

	// ErrCorruptRootKey is returned when the root key blob is corrupted.
	ErrCorruptRootKey = seal.ErrCorruptRootKey

	// ErrNilSealedData is returned when a nil SealedData pointer is provided.
	ErrNilSealedData = seal.ErrNilSealedData

	// ErrEncryptorNotAvailable is returned when the encryptor is unavailable.
	ErrEncryptorNotAvailable = seal.ErrEncryptorNotAvailable

	// ErrHardwareEncryptorRequired is returned when GetMasterKey is called on hardware.
	ErrHardwareEncryptorRequired = seal.ErrHardwareEncryptorRequired

	// ErrShamirNotConfigured is returned when Shamir is not configured.
	ErrShamirNotConfigured = seal.ErrShamirNotConfigured

	// ErrShamirThresholdInvalid is returned when the threshold is invalid.
	ErrShamirThresholdInvalid = seal.ErrShamirThresholdInvalid

	// ErrShamirDuplicateShare is returned when a duplicate share is submitted.
	ErrShamirDuplicateShare = seal.ErrShamirDuplicateShare

	// ErrShamirQuorumExpired is returned when the quorum TTL has elapsed.
	ErrShamirQuorumExpired = seal.ErrShamirQuorumExpired

	// ErrShamirQuorumIncomplete is returned when the quorum is not yet met.
	ErrShamirQuorumIncomplete = seal.ErrShamirQuorumIncomplete

	// ErrShamirNoQuorum is returned when no active quorum accumulator exists.
	ErrShamirNoQuorum = seal.ErrShamirNoQuorum

	// ErrShamirCombineFailed is returned when shares fail to reconstruct the secret.
	ErrShamirCombineFailed = seal.ErrShamirCombineFailed

	// ErrShamirShareNotFound is returned when a Shamir share does not exist.
	ErrShamirShareNotFound = seal.ErrShamirShareNotFound

	// ErrShamirVerificationFailed is returned when share verification fails.
	ErrShamirVerificationFailed = seal.ErrShamirVerificationFailed

	// ErrShamirNoSharesFound is returned when no shares exist in storage.
	ErrShamirNoSharesFound = seal.ErrShamirNoSharesFound

	// ErrShamirStorageFailed is returned when a share storage operation fails.
	ErrShamirStorageFailed = seal.ErrShamirStorageFailed

	// ErrShamirNilStorage is returned when a nil storage backend is provided.
	ErrShamirNilStorage = seal.ErrShamirNilStorage

	// ErrShamirSplitFailed is returned when Shamir splitting fails.
	ErrShamirSplitFailed = seal.ErrShamirSplitFailed

	// ErrShamirSerializationFailed is returned when Shamir serialization fails.
	ErrShamirSerializationFailed = seal.ErrShamirSerializationFailed

	// ErrNilSealedBackend is returned when a nil sealed backend is provided.
	ErrNilSealedBackend = seal.ErrNilSealedBackend

	// ErrSecretNotFound is returned when a named secret is not found.
	ErrSecretNotFound = seal.ErrSecretNotFound

	// ErrInvalidSecretName is returned when a secret name is empty.
	ErrInvalidSecretName = seal.ErrInvalidSecretName

	// ErrResealFailed is returned when re-sealing a secret fails.
	ErrResealFailed = seal.ErrResealFailed

	// ErrRecoveryKeysNotFound is returned when recovery keys do not exist.
	ErrRecoveryKeysNotFound = seal.ErrRecoveryKeysNotFound

	// ErrRootTokenVerificationFailed is returned when root token verification fails.
	ErrRootTokenVerificationFailed = seal.ErrRootTokenVerificationFailed

	// ErrTenantNotFound is returned when a tenant ID is not registered.
	ErrTenantNotFound = seal.ErrTenantNotFound

	// ErrTenantAlreadyExists is returned when a tenant is already registered.
	ErrTenantAlreadyExists = seal.ErrTenantAlreadyExists

	// ErrEmptyTenantID is returned when an empty tenant ID is provided.
	ErrEmptyTenantID = seal.ErrEmptyTenantID

	// ErrNilSystemBarrier is returned when a nil system barrier is provided.
	ErrNilSystemBarrier = seal.ErrNilSystemBarrier

	// ErrTenantSealed is returned when a sealed tenant barrier is accessed.
	ErrTenantSealed = seal.ErrTenantSealed

	// ErrTenantAlreadyInitialized is returned when a tenant is already initialized.
	ErrTenantAlreadyInitialized = seal.ErrTenantAlreadyInitialized

	// ErrTenantNotInitialized is returned when a tenant is not initialized.
	ErrTenantNotInitialized = seal.ErrTenantNotInitialized

	// ErrTenantAlreadyUnsealed is returned when a tenant is already unsealed.
	ErrTenantAlreadyUnsealed = seal.ErrTenantAlreadyUnsealed

	// ErrNoStrategy is returned when no sealing strategy is provided.
	ErrNoStrategy = seal.ErrNoStrategy

	// ErrNilStorageBackend is returned when a nil storage backend is provided.
	ErrNilStorageBackend = seal.ErrNilStorageBackend

	// ErrInvalidPassphrase is returned when an empty passphrase is provided.
	ErrInvalidPassphrase = seal.ErrInvalidPassphrase

	// ErrDecryptionFailed is returned when a decryption operation fails,
	// typically indicating an incorrect passphrase or tampered ciphertext.
	ErrDecryptionFailed = quicraftseal.ErrDecryptionFailed

	// ErrSealStoreSecretNotFound is returned when a seal store secret is missing.
	ErrSealStoreSecretNotFound = seal.ErrSealStoreSecretNotFound

	// ErrSealStoreBarrierSealed is returned when the seal store barrier is sealed.
	ErrSealStoreBarrierSealed = seal.ErrSealStoreBarrierSealed

	// ErrInvocationLimitExceeded is returned when the invocation limit is exceeded.
	ErrInvocationLimitExceeded = seal.ErrInvocationLimitExceeded

	// ErrNonceReuse is returned when a nonce is reused.
	ErrNonceReuse = seal.ErrNonceReuse

	// ErrMigrationInvalidTarget is returned when the requested target strategy
	// for an enable-encryption migration is invalid (e.g. "none").
	ErrMigrationInvalidTarget = seal.ErrMigrationInvalidTarget

	// ErrMigrationSourceNotNone is returned when enable-encryption is requested
	// on a barrier that is not using the none strategy (already keyed).
	ErrMigrationSourceNotNone = seal.ErrMigrationSourceNotNone

	// ErrMigrationInvalidMechanism is returned for an unknown enable-encryption
	// migration mechanism.
	ErrMigrationInvalidMechanism = seal.ErrMigrationInvalidMechanism

	// ErrMigrationInvalidAvailability is returned for an unknown enable-encryption
	// migration availability mode.
	ErrMigrationInvalidAvailability = seal.ErrMigrationInvalidAvailability

	// ErrMigrationInProgress is returned when an enable-encryption migration is
	// already running for the scope.
	ErrMigrationInProgress = seal.ErrMigrationInProgress
)

// ---------------------------------------------------------------------------
// Seal error types
// ---------------------------------------------------------------------------

// SealGetError is returned when a barrier Get operation fails.
type SealGetError = seal.GetError

// SealPutError is returned when a barrier Put operation fails.
type SealPutError = seal.PutError

// SealStoreError is returned when a seal store operation fails.
type SealStoreError = seal.SealStoreError

// SealInvalidShareError is returned when an invalid Shamir share is submitted.
type SealInvalidShareError = seal.InvalidShareError

// SealBarrierSealedError is returned when the barrier is sealed.
type SealBarrierSealedError = seal.BarrierSealedError

// SealDEKDerivationError is returned when DEK derivation fails.
type SealDEKDerivationError = seal.DEKDerivationError

// SealInvalidConfigError is returned when seal configuration is invalid.
type SealInvalidConfigError = seal.InvalidConfigError

// SealTenantDEKNotFoundError is returned when a tenant DEK is not found.
type SealTenantDEKNotFoundError = seal.TenantDEKNotFoundError
