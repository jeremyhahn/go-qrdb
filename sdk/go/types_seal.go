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

// Credentials holds authentication credentials for barrier operations. The
// secret is protected memory — an enclave.Sealed, never a []byte — and
// is built with NewCredentials (consuming) or CredentialsFor (borrowing). The
// zero value means "no credentials supplied". The caller OWNS a credential it
// builds: destroy it (seal.DestroyCredentials(creds), nil-safe) once the seal or
// unseal it was built for has returned; nothing downstream destroys it.
type Credentials = seal.Credentials

// NewCredentials moves a secret into protected memory and returns the
// Credentials holding it. The source slice is WIPED on return, success or
// failure — use it for material the caller made and will not read again. An
// empty secret is refused. It allocates locked pages the kernel can refuse, and
// on a GOEXPERIMENT=boringcrypto build it always fails (go-enclave seals through
// the standard library's AES block, which boringcrypto replaces).
var NewCredentials = seal.NewCredentials

// CredentialsFor builds Credentials from a secret the caller has borrowed — a
// request field, a config value — copying it into protected memory and leaving
// the caller's slice as it was for the caller to wipe. Zero-length material
// yields the zero Credentials ("no credentials supplied").
var CredentialsFor = seal.CredentialsFor

// MustCredentials is NewCredentials for fixtures; it panics where
// NewCredentials would return an error.
var MustCredentials = seal.MustCredentials

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

// KDFDescriber reports a strategy's KDF version and work factor. ChangePassword
// identifies the strategy it replaces through this interface, so a strategy that
// composes a software one is accepted rather than refused for not being the
// concrete type.
type KDFDescriber = seal.KDFDescriber

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

// NewBarrierRegistry creates a new BarrierRegistry for the given system barrier.
var NewBarrierRegistry = seal.NewBarrierRegistry

// BarrierRegistryOption configures a BarrierRegistry at construction
// time. Re-exported so SDK consumers can attach the volume allocator,
// volume resolver, and per-tenant Pebble registry without importing
// pkg/seal directly.
type BarrierRegistryOption = seal.Option

// WithVolumeAllocator attaches the catalog (pkg/volume.Catalog, typed as
// its narrower volume.Allocator seam) RegisterTenant's nil-backend flow
// and RegisterVolumeTenant/ReattachVolumeTenant resolve a tenant's pool
// and tenant-relative path through.
var WithVolumeAllocator = seal.WithVolumeAllocator

// WithVolumeResolver attaches the resolver that maps a pool name to this
// node's own opened volume root directory (typically *volume.OpenVolumes).
var WithVolumeResolver = seal.WithVolumeResolver

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

// NewSoftwareStrategyWithParams is NewSoftwareStrategy with explicit Argon2id
// work factors (time, memory in KiB, threads). Argon2id is refused under
// FIPS 140; NewSoftwareStrategyFIPS is the tunable constructor for that leg.
var NewSoftwareStrategyWithParams = seal.NewSoftwareStrategyWithParams

// NewSoftwareStrategyFIPS is NewSoftwareStrategy with an explicit
// PBKDF2-SHA256 iteration count; accepted under every FIPS policy.
var NewSoftwareStrategyFIPS = seal.NewSoftwareStrategyFIPS

// KDFVersion identifies the passphrase KDF a software strategy derives with:
// KDFArgon2id or KDFPbkdf2SHA256.
type KDFVersion = seal.KDFVersion

// KDFParams carries a software strategy's work factor, read off it with
// KDFVersion() and KDFParams(). It is not recorded in the sealed blob, so a
// replacement strategy must carry the same factor to open it.
type KDFParams = seal.KDFParams

const (
	// KDFArgon2id is the standard (non-FIPS) passphrase KDF.
	KDFArgon2id = seal.KDFArgon2id
	// KDFPbkdf2SHA256 is the FIPS 140 passphrase KDF.
	KDFPbkdf2SHA256 = seal.KDFPbkdf2SHA256
)

// NewDeferredSoftwareStrategy creates a SoftwareStrategy that defers passphrase
// binding to Seal/Unseal time. The passphrase is extracted from
// Credentials.Secret on each call, enabling scenarios where the passphrase is
// not known at construction time.
//
// This is the constructor that VALIDATES credentials: an unseal with the wrong
// secret fails. Prefer it wherever the barrier is expected to authenticate the
// caller that unseals it, and in any test asserting that it does.
var NewDeferredSoftwareStrategy = seal.NewDeferredSoftwareStrategy

// NewDeferredSoftwareStrategyWithParams is NewDeferredSoftwareStrategy carrying
// an explicit KDF version and work factor, read off the strategy being replaced
// with KDFVersion() and KDFParams(). A sealed root-key blob records the KDF
// version and never the params, so a replacement strategy must carry the same
// factor or the blob cannot be unsealed; the no-argument constructor resets a
// hardened deployment to the defaults.
var NewDeferredSoftwareStrategyWithParams = seal.NewDeferredSoftwareStrategyWithParams

// NewSealStore creates a new SealStore backed by the given barrier.
var NewSealStore = seal.NewSealStore

// NewStorageAdapter creates a StorageBackend from a LegacyStorage.
var NewStorageAdapter = seal.NewStorageAdapter

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
