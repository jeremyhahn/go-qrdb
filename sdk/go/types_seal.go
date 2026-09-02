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

import "github.com/jeremyhahn/go-qrdb/pkg/seal"

// ---------------------------------------------------------------------------
// Seal mode
// ---------------------------------------------------------------------------

// SealMode identifies the sealing mode (auto vs Shamir).
type SealMode = seal.SealMode

// SealMode constants.
const (
	SealModeAuto   = seal.SealModeAuto
	SealModeShamir = seal.SealModeShamir
)

// ---------------------------------------------------------------------------
// Core seal interfaces
// ---------------------------------------------------------------------------

// EncryptionBarrier is the interface for encrypt/decrypt operations
// backed by a sealed barrier.
type EncryptionBarrier = seal.EncryptionBarrier

// TenantKeyManager manages per-tenant DEK derivation.
type TenantKeyManager = seal.TenantKeyManager

// CeremonyDistributor seals Shamir share material to enrolled Security
// Officers. Implementations split the share secret and seal one share to
// each officer's enclave; the secret is wiped before either method returns.
type CeremonyDistributor = seal.CeremonyDistributor

// ---------------------------------------------------------------------------
// Strategy types (re-exported from go-quicraft via pkg/seal)
// ---------------------------------------------------------------------------

// StrategyID identifies a sealing strategy (software, TPM2, PKCS11, etc.).
type StrategyID = seal.StrategyID

// SealingStrategy is the interface all sealing strategy implementations satisfy.
type SealingStrategy = seal.SealingStrategy

// SymmetricEncrypter provides authenticated encryption/decryption.
type SymmetricEncrypter = seal.SymmetricEncrypter

// StorageBackend is the backend interface for the seal subsystem.
type StorageBackend = seal.StorageBackend

// Credentials holds authentication credentials for barrier operations.
type Credentials = seal.Credentials

// SealedRootKey is the serialized, encrypted root key blob.
type SealedRootKey = seal.SealedRootKey

// ShareAccumulator collects protected Shamir shares toward quorum, holding
// every share in protected memory and returning the reconstructed secret in an
// enclave (go-quicraft's ProtectedShareAccumulator; the string-valued sibling
// was removed in go-quicraft v0.9.0-alpha).
type ShareAccumulator = seal.ShareAccumulator

// ShareSplitter splits a secret into Shamir shares whose values live in
// protected memory (go-quicraft's ProtectedShareSplitter).
type ShareSplitter = seal.ShareSplitter

// QuorumProgress reports current progress toward share quorum.
type QuorumProgress = seal.QuorumProgress

// ShamirInitResult holds the result of a Shamir initialization.
type ShamirInitResult = seal.ShamirInitResult

// ShamirConfig configures Shamir secret sharing parameters.
type ShamirConfig = seal.ShamirConfig

// SoftwareStrategy is the software-only sealing strategy.
type SoftwareStrategy = seal.SoftwareStrategy

// EpochBarrier is the go-quicraft epoch barrier type.
type EpochBarrier = seal.EpochBarrier

// EpochBarrierConfig configures the epoch barrier.
type EpochBarrierConfig = seal.EpochBarrierConfig

// InvocationCounter tracks seal/unseal invocation counts.
type InvocationCounter = seal.InvocationCounter

// NonceTrackerProvider tracks nonces for replay prevention.
type NonceTrackerProvider = seal.NonceTrackerProvider

// AEADProvider creates AEAD ciphers for symmetric encryption.
type AEADProvider = seal.AEADProvider

// SymmetricAlgorithm identifies a symmetric encryption algorithm.
type SymmetricAlgorithm = seal.SymmetricAlgorithm

// ---------------------------------------------------------------------------
// Barrier types
// ---------------------------------------------------------------------------

// StorageBarrier is the core barrier that encrypts data at rest.
type StorageBarrier = seal.StorageBarrier

// Barrier is an alias for StorageBarrier.
type Barrier = seal.Barrier

// BarrierConfig holds configuration for a StorageBarrier.
type BarrierConfig = seal.BarrierConfig

// BarrierStatus reports the current state of a barrier.
type BarrierStatus = seal.BarrierStatus

// BarrierRegistry manages system and tenant barriers.
type BarrierRegistry = seal.BarrierRegistry

// TenantBarrier wraps a barrier with tenant-scoped key namespacing.
type TenantBarrier = seal.TenantBarrier

// TenantBarrierConfig holds configuration for a tenant barrier.
type TenantBarrierConfig = seal.TenantBarrierConfig

// MemoryBackend is an in-memory StorageBackend for testing.
type MemoryBackend = seal.MemoryBackend

// NamespacedBackend prefixes all keys with a namespace.
type NamespacedBackend = seal.NamespacedBackend

// ---------------------------------------------------------------------------
// Shamir strategy
// ---------------------------------------------------------------------------

// ShamirStrategy implements Shamir secret sharing as a sealing strategy.
type ShamirStrategy = seal.ShamirStrategy

// ---------------------------------------------------------------------------
// Seal store
// ---------------------------------------------------------------------------

// SealStore provides named secret storage backed by a barrier.
type SealStore = seal.SealStore

// SealStoreStatus reports the status of a SealStore.
type SealStoreStatus = seal.SealStoreStatus

// ---------------------------------------------------------------------------
// Storage adapter
// ---------------------------------------------------------------------------

// LegacyStorage is the interface for legacy storage backends that the
// StorageAdapter wraps.
type LegacyStorage = seal.LegacyStorage

// StorageAdapter adapts a LegacyStorage to the StorageBackend interface.
type StorageAdapter = seal.StorageAdapter

// ---------------------------------------------------------------------------
// Recovery and root token
// ---------------------------------------------------------------------------

// RecoveryKeyResult holds the result of generating recovery keys.
type RecoveryKeyResult = seal.RecoveryKeyResult

// RootToken holds a generated root token and its metadata.
type RootToken = seal.RootToken

// ---------------------------------------------------------------------------
// Audit
// ---------------------------------------------------------------------------

// AuditEvent describes a seal subsystem audit event.
type AuditEvent = seal.AuditEvent

// AuditLogger is the interface for seal audit logging.
type AuditLogger = seal.AuditLogger

// ---------------------------------------------------------------------------
// Strategy ID constants
// ---------------------------------------------------------------------------

// Hardware/cloud strategy IDs (tpm2/pkcs11/awskms/gcpkms/azurekv/vault) are
// owned by go-xkms and exported from its SDK; go-qrdb does not define them.
const (
	// StrategyShamir identifies the Shamir secret sharing sealing strategy.
	StrategyShamir = seal.StrategyShamir

	// StrategyPassphrase identifies the canonical passphrase-protected software
	// sealing strategy.
	StrategyPassphrase = seal.StrategyPassphrase
)

// DefaultQuorumTTL is the default time-to-live for a Shamir quorum session.
const DefaultQuorumTTL = seal.DefaultQuorumTTL

// ---------------------------------------------------------------------------
// Constructors
// ---------------------------------------------------------------------------

// NewStorageBarrier creates a new StorageBarrier with the given configuration.
var NewStorageBarrier = seal.NewStorageBarrier

// NewBarrier is an alias for NewStorageBarrier.
var NewBarrier = seal.NewBarrier

// NewBarrierRegistry creates a new BarrierRegistry for the given system barrier.
var NewBarrierRegistry = seal.NewBarrierRegistry

// BarrierRegistryOption configures a BarrierRegistry at construction
// time. Re-exported so SDK consumers can attach the volume pool and
// per-tenant Pebble registry without importing pkg/seal directly.
type BarrierRegistryOption = seal.Option

// WithVolumePool attaches a volume pool to the BarrierRegistry. After
// this option is applied, RegisterTenant calls that pass a nil
// backend allocate a per-tenant path from the named pool.
var WithVolumePool = seal.WithVolumePool

// WithPerTenantRegistry attaches the per-tenant Pebble registry that
// owns the lifecycle of the dedicated Pebble each barrier-mode tenant
// runs on.
var WithPerTenantRegistry = seal.WithPerTenantRegistry

// NewTenantBarrier creates a new TenantBarrier for the given tenant.
var NewTenantBarrier = seal.NewTenantBarrier

// NewMemoryBackend creates a new in-memory StorageBackend.
var NewMemoryBackend = seal.NewMemoryBackend

// NewNamespacedBackend creates a new namespace-prefixed StorageBackend.
var NewNamespacedBackend = seal.NewNamespacedBackend

// NewShamirStrategy creates a new Shamir sealing strategy.
var NewShamirStrategy = seal.NewShamirStrategy

// NewSoftwareStrategy creates a new software-only sealing strategy, binding the
// given passphrase at construction.
//
// The returned strategy IGNORES THE CREDENTIALS PASSED TO
// SealRootKey/UnsealRootKey — UnsealRootKey succeeds for ANY credentials,
// including wrong ones, so a nil error from it is not evidence that the
// supplied passphrase was correct. Building a "change barrier password" or
// "rotate barrier passphrase" feature on a bound strategy reproduces go-qrdb
// #157: an authentication bypass in which, after the password change, the
// wrong passphrase, the old one and the new one all unsealed the barrier.
//
// Use NewDeferredSoftwareStrategy for a barrier that authenticates its
// unsealer. A test asserting that a wrong credential is REJECTED must use the
// deferred constructor too: against a bound strategy that assertion cannot
// hold, because the bound strategy accepts the wrong credential and the test
// then goes red over its own fixture rather than over the code under test. The
// rejection test is the right test; it just cannot be written on this
// constructor.
//
// The shape that proves nothing is the opposite one — asserting only that the
// root key comes back succeeds against both constructors, and so says nothing
// about whether credentials are checked at all.
var NewSoftwareStrategy = seal.NewSoftwareStrategy

// NewDeferredSoftwareStrategy creates a SoftwareStrategy that defers passphrase
// binding to Seal/Unseal time. The passphrase is extracted from
// Credentials.Secret on each call, enabling scenarios where the passphrase is
// not known at construction time.
//
// This is the constructor that VALIDATES credentials: an unseal with the wrong
// secret fails. Prefer it wherever the barrier is expected to authenticate the
// caller that unseals it, and in any test asserting that it does.
var NewDeferredSoftwareStrategy = seal.NewDeferredSoftwareStrategy

// NewSealStore creates a new SealStore backed by the given barrier.
var NewSealStore = seal.NewSealStore

// NewStorageAdapter creates a StorageBackend from a LegacyStorage.
var NewStorageAdapter = seal.NewStorageAdapter

// NewShamirShareAccumulator creates a new share accumulator for Shamir quorum.
var NewShamirShareAccumulator = seal.NewShamirShareAccumulator

// NewShamirShareSplitter creates a ShareSplitter that divides a secret into
// threshold-of-total Shamir shares.
var NewShamirShareSplitter = seal.NewShamirShareSplitter

// NewInvocationCounter creates a new invocation counter.
var NewInvocationCounter = seal.NewInvocationCounter

// NewNonceTracker creates a new nonce tracker.
var NewNonceTracker = seal.NewNonceTracker

// NewSoftwareAEADProvider creates a new software AEAD provider.
var NewSoftwareAEADProvider = seal.NewSoftwareAEADProvider

// DefaultEpochBarrierConfig returns the default epoch barrier configuration.
var DefaultEpochBarrierConfig = seal.DefaultEpochBarrierConfig
