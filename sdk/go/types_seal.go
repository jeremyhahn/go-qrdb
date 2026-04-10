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

// ShareAccumulator collects Shamir shares toward quorum.
type ShareAccumulator = seal.ShareAccumulator

// ShareSplitter splits a secret into Shamir shares.
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

const (
	// StrategyTPM2 identifies the TPM 2.0 sealing strategy.
	StrategyTPM2 = seal.StrategyTPM2

	// StrategyPKCS11 identifies the PKCS#11 sealing strategy.
	StrategyPKCS11 = seal.StrategyPKCS11

	// StrategyAWSKMS identifies the AWS KMS sealing strategy.
	StrategyAWSKMS = seal.StrategyAWSKMS

	// StrategyGCPKMS identifies the GCP KMS sealing strategy.
	StrategyGCPKMS = seal.StrategyGCPKMS

	// StrategyAzureKV identifies the Azure Key Vault sealing strategy.
	StrategyAzureKV = seal.StrategyAzureKV

	// StrategyVault identifies the HashiCorp Vault sealing strategy.
	StrategyVault = seal.StrategyVault

	// StrategyShamir identifies the Shamir secret sharing sealing strategy.
	StrategyShamir = seal.StrategyShamir

	// StrategySoftware identifies the software-only sealing strategy.
	StrategySoftware = seal.StrategySoftware
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

// NewTenantBarrier creates a new TenantBarrier for the given tenant.
var NewTenantBarrier = seal.NewTenantBarrier

// NewMemoryBackend creates a new in-memory StorageBackend.
var NewMemoryBackend = seal.NewMemoryBackend

// NewNamespacedBackend creates a new namespace-prefixed StorageBackend.
var NewNamespacedBackend = seal.NewNamespacedBackend

// NewShamirStrategy creates a new Shamir sealing strategy.
var NewShamirStrategy = seal.NewShamirStrategy

// NewSoftwareStrategy creates a new software-only sealing strategy.
var NewSoftwareStrategy = seal.NewSoftwareStrategy

// NewSealStore creates a new SealStore backed by the given barrier.
var NewSealStore = seal.NewSealStore

// NewStorageAdapter creates a StorageBackend from a LegacyStorage.
var NewStorageAdapter = seal.NewStorageAdapter

// NewShamirShareAccumulator creates a new share accumulator for Shamir quorum.
var NewShamirShareAccumulator = seal.NewShamirShareAccumulator

// NewInvocationCounter creates a new invocation counter.
var NewInvocationCounter = seal.NewInvocationCounter

// NewNonceTracker creates a new nonce tracker.
var NewNonceTracker = seal.NewNonceTracker

// NewSoftwareAEADProvider creates a new software AEAD provider.
var NewSoftwareAEADProvider = seal.NewSoftwareAEADProvider

// DefaultEpochBarrierConfig returns the default epoch barrier configuration.
var DefaultEpochBarrierConfig = seal.DefaultEpochBarrierConfig

// DefaultPreferenceOrder is the default strategy preference order.
var DefaultPreferenceOrder = seal.DefaultPreferenceOrder
