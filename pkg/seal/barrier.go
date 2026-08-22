// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	quicraftseal "github.com/jeremyhahn/go-quicraft/pkg/seal"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// stateSealed and stateUnsealed are the two possible states for the barrier.
const (
	stateSealed   int32 = 0
	stateUnsealed int32 = 1
)

// metadataPathSuffix is appended to the configured RootKeyPath to form the
// storage key under which non-secret barrier metadata (e.g. the
// initialization timestamp) is persisted. It must be a SIBLING of the root key
// (a "." suffix), never a child ("/meta"): a child key nests metadata under the
// root key's own value path, which a filesystem-backed KV cannot represent
// (the same path would be both a file holding the root key and a directory
// holding the metadata). The metadata blob is stored in plaintext alongside the
// sealed root key envelope; it carries no key material.
const metadataPathSuffix = ".meta"

// barrierMetadata is the non-secret, persisted barrier metadata. It is
// serialised as plaintext JSON to the metadata path so that warm-boot
// callers can recover state (notably InitializedAt) that is not embedded in
// the sealed root key envelope.
type barrierMetadata struct {
	// InitializedAt is the UTC timestamp at which the barrier was first
	// initialized. Zero means never initialized.
	InitializedAt time.Time `json:"initialized_at"`
}

// metadataPath returns the storage key for the barrier's persisted metadata.
func (b *StorageBarrier) metadataPath() string {
	return b.config.RootKeyPath + metadataPathSuffix
}

// persistMetadata writes the barrier metadata blob to the underlying backend
// in plaintext. The metadata contains no secret material.
func (b *StorageBarrier) persistMetadata(ctx context.Context, meta barrierMetadata) error {
	blob, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return b.base.Put(ctx, b.metadataPath(), blob)
}

// loadInitializedAt reads the persisted initialization timestamp from the
// metadata blob, returning the zero time when no metadata is present. A
// missing metadata blob (ErrSecretNotFound or a nil value from backends that
// signal "missing" that way) is not an error: pre-metadata barriers simply
// have no persisted timestamp.
func (b *StorageBarrier) loadInitializedAt(ctx context.Context) (time.Time, error) {
	blob, err := b.base.Get(ctx, b.metadataPath())
	if err != nil {
		if errors.Is(err, ErrSecretNotFound) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	if blob == nil {
		return time.Time{}, nil
	}
	var meta barrierMetadata
	if jsonErr := json.Unmarshal(blob, &meta); jsonErr != nil {
		return time.Time{}, jsonErr
	}
	return meta.InitializedAt, nil
}

// Compile-time interface check.
var _ StorageBackend = (*StorageBarrier)(nil)

// Barrier is a backward-compatible alias for StorageBarrier.
type Barrier = StorageBarrier

// NewBarrier is a backward-compatible alias for NewStorageBarrier.
var NewBarrier = NewStorageBarrier

// StorageBarrier wraps a StorageBackend with transparent authenticated
// encryption. All values are encrypted before being written to the
// underlying backend and decrypted on read. Keys remain in plaintext.
//
// Encryption is delegated to go-quicraft's EpochBarrier, which provides
// algorithm-agile AEAD with epoch-based key rotation, HKDF DEK derivation,
// and AEAD safety tracking (nonce reuse detection, byte limits).
//
// Hardware-backed strategies (TPM2, PKCS#11, cloud KMS) are supported
// through the EpochBarrier's hardware encryptor delegation.
//
// The barrier lifecycle is:
//  1. Initialize: generate root key, seal with best strategy, obtain encryptor
//  2. Unseal: load sealed root key, unseal, obtain encryptor
//  3. Seal: release encryptor, block further operations
type StorageBarrier struct {
	logger    *slog.Logger
	base      StorageBackend
	barrier   *quicraftseal.Barrier
	config    BarrierConfig
	prefOrder []StrategyID

	// strategiesMu guards strategies. The map is mutated after construction by
	// ChangePassword, Rekey, updateSealedShamirParams, and
	// AdoptMigratedRootKey, while Status/Unseal/bestStrategy/Strategies/
	// ShamirStrategy used to read it with no lock of their own — a Status()
	// poll concurrent with any of those mutating operations is a genuine
	// concurrent map read/write (fatal, not just a race). All access to
	// strategies MUST go through the strategyByID/setStrategy accessors below,
	// or take strategiesMu directly (as Strategies() and bestStrategy() do for
	// a whole-map read); the field itself must never be read or written
	// directly outside of NewStorageBarrier (which owns it exclusively before
	// the barrier is published to any other goroutine).
	strategiesMu sync.RWMutex
	strategies   map[StrategyID]SealingStrategy

	// state: 0 = sealed, 1 = unsealed
	state atomic.Int32

	// activeStrategy is the strategy ID that sealed the root key.
	activeStrategy atomic.Value

	// persistedStrategyCache caches the strategy ID recovered from the
	// persisted sealed root-key blob for sealed-but-initialized barriers
	// that have not yet unsealed (see strategyOrPersisted). atomic.Value
	// stores StrategyID.
	persistedStrategyCache atomic.Value

	// accumulator holds the Shamir share accumulator for quorum-based unsealing.
	accumulator atomic.Value

	// accumulatorMu serializes load-or-create of the accumulator so concurrent
	// first shares cannot each construct a separate accumulator (which would
	// lose quorum progress), and so an expired accumulator is wiped before it
	// is replaced.
	accumulatorMu sync.Mutex

	// initializedAt records when the barrier was first initialized. Used by
	// callers (notably the GUI's connection manager) to distinguish a never-
	// initialized barrier from a sealed-but-initialized one. atomic.Value
	// stores time.Time. Zero value means "never initialized".
	initializedAt atomic.Value

	// distributor is the ceremony distributor for system barrier share
	// distribution (init, rekey). Set via SetCeremonyDistributor.
	distributor CeremonyDistributor

	// recoveryDistributor is the ceremony distributor for recovery key
	// share distribution. Set via SetRecoveryDistributor.
	recoveryDistributor CeremonyDistributor

	// sealVoteAcc holds the Shamir share accumulator used exclusively for
	// quorum-gated sealing. Separate from the unseal accumulator so that
	// seal votes and unseal submissions never interfere with each other.
	sealVoteAcc atomic.Value

	// sealVoteMu serializes load-or-create of the seal-vote accumulator,
	// mirroring the accumulatorMu pattern for the unseal accumulator.
	sealVoteMu sync.Mutex
}

// NewStorageBarrier creates a new StorageBarrier that encrypts all values
// stored in base. At least one SealingStrategy must be provided.
//
// The EpochBarrier is configured with the deployment ID from the BarrierConfig.
// If DeploymentID is 0, the default epoch barrier config is used.
func NewStorageBarrier(
	logger *slog.Logger,
	base StorageBackend,
	config BarrierConfig,
	strategies ...SealingStrategy,
) (*StorageBarrier, error) {
	if len(strategies) == 0 {
		return nil, ErrNoAvailableStrategy
	}

	stratMap := make(map[StrategyID]SealingStrategy, len(strategies))
	for _, s := range strategies {
		stratMap[s.ID()] = s
	}

	// Strategy selection is explicit: the caller's PreferenceOrder is the
	// ordered candidate list. There is no implicit default preference order.
	prefOrder := config.PreferenceOrder

	epochConfig := quicraftseal.DefaultBarrierConfig(config.DeploymentID)
	if config.InvocationLimit != 0 {
		epochConfig.InvocationLimit = config.InvocationLimit
	}
	if config.RotationThreshold != 0 {
		epochConfig.RotationThreshold = config.RotationThreshold
	}
	if config.InvocationCounter != nil {
		epochConfig.InvocationCounter = config.InvocationCounter
	}
	if config.NonceTrackerProvider != nil {
		epochConfig.NonceTrackerProvider = config.NonceTrackerProvider
	}
	if config.AEADProvider != nil {
		epochConfig.AEADProvider = config.AEADProvider
	}

	// Epoch rotation must be durable BEFORE it takes effect. quicraft
	// activates a new epoch only after this persister returns nil, and
	// fails closed with ErrNoStatePersister when none is configured —
	// without it, every write made under the new epoch would be
	// permanently undecryptable after a restart, because the reloaded
	// blob would still name the old epoch and derive only its DEK.
	//
	// Custody of the blob is ours, not the consensus layer's: this writes
	// the canonical root-key record through the RAW base store, exactly as
	// Rekey and Migrate do. It must never route through the StorageBarrier
	// itself — the persister runs with the quicraft barrier lock held and
	// must not re-enter it, and the root-key blob is by definition the one
	// value that cannot be barrier-encrypted.
	epochConfig.StatePersister = func(sealed *quicraftseal.SealedRootKey) error {
		if sealed == nil {
			return ErrNilSealedData
		}
		blob, err := json.Marshal(sealed)
		if err != nil {
			return err
		}
		return base.Put(context.Background(), config.RootKeyPath, blob)
	}

	// The epoch config is assembled at runtime from the caller's BarrierConfig,
	// so an invalid algorithm (or a non-FIPS cipher under FIPS mode) is an
	// operator error to report, not a programmer error to panic on.
	epochBarrier, err := quicraftseal.NewBarrier(epochConfig)
	if err != nil {
		return nil, err
	}

	b := &StorageBarrier{
		logger:     logger,
		base:       base,
		barrier:    epochBarrier,
		config:     config,
		strategies: stratMap,
		prefOrder:  prefOrder,
	}
	// Start sealed (state = 0 is zero value).
	return b, nil
}

// Initialize generates a new root key, seals it with the best available
// strategy, stores the sealed blob in the backend, and transitions the
// barrier to the unsealed state via the EpochBarrier.
//
// This must be called exactly once for a new barrier. Subsequent calls
// return ErrAlreadyInitialized.
func (b *StorageBarrier) Initialize(ctx context.Context, creds Credentials) error {
	exists, err := b.base.Exists(ctx, b.config.RootKeyPath)
	if err != nil {
		return err
	}
	if exists {
		return ErrAlreadyInitialized
	}

	strategy, err := b.bestStrategy()
	if err != nil {
		emitAudit(b.config.AuditLogger, "initialize", "barrier", b.config.RootKeyPath,
			"deny", map[string]string{"error": err.Error()})
		return err
	}
	return b.initializeWithStrategy(ctx, strategy, creds)
}

// InitializeWithStrategy initializes the barrier using EXACTLY the supplied
// strategy, bypassing best-strategy preference selection. The strategy is
// registered under its ID (replacing any registration the barrier was
// constructed with under that ID) so a later Unseal resolves the persisted
// blob's strategy in-process. Like Initialize, it refuses an
// already-initialized barrier with ErrAlreadyInitialized.
//
// This is the seam that honors a tenant admin's explicit strategy choice at
// barrier init on a node where the barrier shell was pre-registered with
// the node's default strategy set (see
// tenant.TenantService.TenantBarrierInitWithStrategy) — without it the
// requested strategy was silently ignored and initialization ran against
// whatever the shell happened to carry.
func (b *StorageBarrier) InitializeWithStrategy(ctx context.Context, strategy SealingStrategy, creds Credentials) error {
	if strategy == nil {
		return ErrNoStrategySpecified
	}
	exists, err := b.base.Exists(ctx, b.config.RootKeyPath)
	if err != nil {
		return err
	}
	if exists {
		return ErrAlreadyInitialized
	}
	b.setStrategy(strategy.ID(), strategy)
	return b.initializeWithStrategy(ctx, strategy, creds)
}

// initializeWithStrategy is the shared initialization core: it delegates to
// the EpochBarrier with the chosen strategy, persists the sealed root key
// blob under an existence CAS, and transitions to unsealed. Callers have
// already verified the barrier is uninitialized and chosen the strategy.
func (b *StorageBarrier) initializeWithStrategy(ctx context.Context, strategy SealingStrategy, creds Credentials) error {
	// Delegate initialization to the EpochBarrier. It generates the root
	// key, derives the initial DEK, and seals the root key via the strategy.
	if err := b.barrier.Initialize(ctx, strategy, creds); err != nil {
		emitAudit(b.config.AuditLogger, "initialize", "barrier", b.config.RootKeyPath,
			"deny", map[string]string{"error": err.Error(), "strategy": string(strategy.ID())})
		return err
	}

	// Persist the sealed root key blob with an atomic compare-and-set on key
	// existence. The Exists fast-path above is advisory only; this CAS is the
	// authoritative guard against two concurrent initializers each generating
	// a root key and the second silently overwriting the first.
	sealed := b.barrier.SealedRootKeyData()
	blob, err := json.Marshal(sealed)
	if err != nil {
		return err
	}
	stored, err := b.base.PutIfAbsent(ctx, b.config.RootKeyPath, blob)
	if err != nil {
		return err
	}
	if !stored {
		// Another initializer won the race. Discard this call's root key and
		// DEKs by sealing the epoch barrier — the loser must not come up
		// unsealed with a root key that was never persisted.
		if sealErr := b.barrier.Seal(); sealErr != nil {
			b.logger.Error("failed to seal epoch barrier after lost initialization race",
				slog.String("error", sealErr.Error()))
		}
		emitAudit(b.config.AuditLogger, "initialize", "barrier", b.config.RootKeyPath,
			"deny", map[string]string{"error": ErrAlreadyInitialized.Error(), "strategy": string(strategy.ID())})
		return ErrAlreadyInitialized
	}

	// Transition to unsealed.
	b.activeStrategy.Store(strategy.ID())
	b.state.Store(stateUnsealed)
	initAt := time.Now().UTC()
	b.initializedAt.Store(initAt)

	// Persist the initialization timestamp so that a warm-booted barrier
	// (a fresh StorageBarrier constructed over the same store) reports a
	// non-zero InitializedAt without having to unseal first.
	if err := b.persistMetadata(ctx, barrierMetadata{InitializedAt: initAt}); err != nil {
		emitAudit(b.config.AuditLogger, "initialize", "barrier", b.config.RootKeyPath,
			"deny", map[string]string{"error": err.Error(), "strategy": string(strategy.ID())})
		return err
	}

	emitAudit(b.config.AuditLogger, "initialize", "barrier", b.config.RootKeyPath,
		"allow", map[string]string{"strategy": string(strategy.ID())})

	b.logger.Info("barrier initialized",
		slog.String("strategy", string(strategy.ID())),
		slog.Bool("hardware_backed", strategy.HardwareBacked()))

	return nil
}

// Unseal loads the sealed root key blob from the backend, unseals it
// with the matching strategy via the EpochBarrier, and transitions
// the barrier to the unsealed state.
func (b *StorageBarrier) Unseal(ctx context.Context, creds Credentials) error {
	if b.state.Load() == stateUnsealed {
		return ErrAlreadyUnsealed
	}

	blob, err := b.base.Get(ctx, b.config.RootKeyPath)
	if err != nil {
		if errors.Is(err, ErrSecretNotFound) {
			return ErrNotInitialized
		}
		return err
	}

	var sealed SealedRootKey
	if err := json.Unmarshal(blob, &sealed); err != nil {
		return ErrCorruptRootKey
	}

	strategy, ok := b.strategyByID(sealed.Strategy)
	if !ok {
		emitAudit(b.config.AuditLogger, "unseal", "barrier", b.config.RootKeyPath,
			"deny", map[string]string{"error": ErrStrategyNotFound.Error(), "strategy": string(sealed.Strategy)})
		return ErrStrategyNotFound
	}

	// Load the sealed root key data into the EpochBarrier for unseal.
	if err := b.barrier.SetSealedRootKey(&sealed); err != nil {
		return err
	}

	// Delegate unseal to the EpochBarrier.
	if err := b.barrier.Unseal(ctx, strategy, creds); err != nil {
		emitAudit(b.config.AuditLogger, "unseal", "barrier", b.config.RootKeyPath,
			"deny", map[string]string{"error": err.Error(), "strategy": string(sealed.Strategy)})
		return err
	}

	b.activeStrategy.Store(sealed.Strategy)
	b.state.Store(stateUnsealed)
	// Recover the initialization timestamp. The original timestamp is
	// persisted in the barrier metadata (not in the sealed root key blob),
	// so a warm-booted barrier reports the real InitializedAt. If no
	// metadata is present (a pre-metadata barrier) we fall back to the
	// unseal time as a non-zero sentinel so callers can still distinguish
	// initialized from never-initialized.
	b.restoreInitializedAt(ctx)

	// Seed the invocation counter with the number of encrypted entries
	// in the current epoch. Each stored key represents one encrypt invocation.
	if ic := b.barrier.InvocationCounter(); ic != nil && ic.IsEnabled() {
		count, countErr := b.countCurrentEntries(ctx)
		if countErr != nil {
			b.logger.Warn("failed to count entries for invocation counter seeding",
				slog.String("error", countErr.Error()))
		} else if count > 0 {
			ic.Seed(count)
			b.logger.Info("seeded invocation counter",
				slog.Int64("count", count),
				slog.Float64("usage_pct", ic.GetUsagePercentage()))
		}
	}

	emitAudit(b.config.AuditLogger, "unseal", "barrier", b.config.RootKeyPath,
		"allow", map[string]string{"strategy": string(sealed.Strategy)})

	b.logger.Info("barrier unsealed",
		slog.String("strategy", string(sealed.Strategy)))

	return nil
}

// unsealDirect resolves the quorum-reconstructed secret to the barrier root
// key (unwrapping it from the unseal-key blob when present, see
// unsealkey.go) and transitions to the unsealed state. The EpochBarrier is
// initialized with the root key directly.
// Used by StrategyShamir where shares reconstruct the unseal key (or, on
// legacy barriers, the root key itself).
func (b *StorageBarrier) unsealDirect(ctx context.Context, secret []byte) error {
	shamirStrat, ok := b.strategyByID(StrategyShamir)
	if !ok {
		return ErrShamirNotConfigured
	}

	// Resolve the reconstructed secret to the root key. In unseal-key mode
	// the AEAD unwrap doubles as share verification: wrong or rekey-revoked
	// shares fail here with ErrUnsealKeyMismatch.
	rootKey, err := b.resolveDirectSecret(ctx, secret)
	if err != nil {
		return err
	}
	defer secure.Wipe(rootKey)

	// The SOs have reconstructed the root key in memory (rootKey). The real
	// ShamirStrategy can no longer reconstruct it (shares are never persisted),
	// so we unseal via a recovery strategy that simply hands the EpochBarrier
	// the in-hand root key. The persisted (key-material-free) envelope supplies
	// the epoch/salt state needed to re-derive DEKs. This mirrors RecoverWithKeys
	// and works whether the EpochBarrier is freshly opened or sealed in-process.
	blob, getErr := b.base.Get(ctx, b.config.RootKeyPath)
	if getErr != nil {
		return getErr
	}
	var sealed SealedRootKey
	if jsonErr := json.Unmarshal(blob, &sealed); jsonErr != nil {
		return ErrCorruptRootKey
	}
	if stampErr := b.stampNoneOriginSealedState(rootKey, &sealed); stampErr != nil {
		return stampErr
	}
	if setErr := b.barrier.SetSealedRootKey(&sealed); setErr != nil {
		return setErr
	}
	recStrat := newRecoveryStrategy(rootKey, shamirStrat)
	defer func() { _ = recStrat.Close() }()
	if unsealErr := b.barrier.Unseal(ctx, recStrat, Credentials{}); unsealErr != nil {
		return unsealErr
	}

	b.activeStrategy.Store(StrategyShamir)
	b.state.Store(stateUnsealed)
	b.restoreInitializedAt(ctx)
	return nil
}

// restoreInitializedAt populates the in-memory initialization timestamp on an
// unseal. It prefers the persisted metadata timestamp (the real
// initialization time) and falls back to the current time as a non-zero
// sentinel when no metadata is present, preserving the prior "signal
// initialized" semantics for pre-metadata barriers.
func (b *StorageBarrier) restoreInitializedAt(ctx context.Context) {
	if v := b.initializedAt.Load(); v != nil {
		if t, ok := v.(time.Time); ok && !t.IsZero() {
			return
		}
	}
	persisted, err := b.loadInitializedAt(ctx)
	if err != nil {
		b.logger.Warn("failed to load persisted barrier metadata on unseal",
			slog.String("error", err.Error()))
		persisted = time.Time{}
	}
	if persisted.IsZero() {
		persisted = time.Now().UTC()
	}
	b.initializedAt.Store(persisted)
}

// strategyByID returns the registered strategy for id and whether it was
// found. Safe for concurrent use with setStrategy and any other reader.
func (b *StorageBarrier) strategyByID(id StrategyID) (SealingStrategy, bool) {
	b.strategiesMu.RLock()
	defer b.strategiesMu.RUnlock()
	s, ok := b.strategies[id]
	return s, ok
}

// setStrategy registers (or replaces) the strategy under id. Safe for
// concurrent use with strategyByID and any other reader.
func (b *StorageBarrier) setStrategy(id StrategyID, s SealingStrategy) {
	b.strategiesMu.Lock()
	defer b.strategiesMu.Unlock()
	b.strategies[id] = s
}

// ShamirStrategy returns the registered ShamirStrategy or nil if not
// registered. This accessor allows callers to inspect the Shamir
// configuration and manage shares.
func (b *StorageBarrier) ShamirStrategy() *ShamirStrategy {
	s, ok := b.strategyByID(StrategyShamir)
	if !ok {
		return nil
	}
	shamirStrat, ok := s.(*ShamirStrategy)
	if !ok {
		return nil
	}
	return shamirStrat
}

// GetMasterKey returns a copy of the root key from the EpochBarrier.
// The barrier must be unsealed.
//
// For hardware-backed strategies where the key never leaves hardware,
// the EpochBarrier returns an error.
func (b *StorageBarrier) GetMasterKey() ([]byte, error) {
	if b.IsSealed() {
		return nil, ErrSealed
	}
	return b.barrier.RootKey()
}

// Encryptor returns nil for the refactored barrier. Encryption is now
// delegated entirely to the EpochBarrier and is not directly accessible.
// Callers that need direct encryption should use the Encrypt/Decrypt
// methods on the StorageBarrier itself.
func (b *StorageBarrier) Encryptor() (SymmetricEncrypter, error) {
	if b.IsSealed() {
		return nil, ErrSealed
	}
	return nil, ErrEncryptorNotAvailable
}

// Seal transitions the barrier to sealed state and seals the EpochBarrier.
// All subsequent storage operations will return ErrSealed until Unseal
// is called again. Seal is idempotent.
func (b *StorageBarrier) Seal() error {
	if b.state.Load() == stateSealed {
		return nil // already sealed, idempotent
	}

	// Seal the EpochBarrier (zeroes root key and DEKs).
	_ = b.barrier.Seal()

	b.state.Store(stateSealed)

	emitAudit(b.config.AuditLogger, "seal", "barrier", b.config.RootKeyPath,
		"allow", nil)

	b.logger.Info("barrier sealed")
	return nil
}

// IsSealed reports whether the barrier is in sealed state.
func (b *StorageBarrier) IsSealed() bool {
	return b.state.Load() == stateSealed
}

// Status returns the current barrier status.
//
// InitializedAt is reported from the in-memory timestamp when available and
// otherwise recovered from the persisted barrier metadata. This means a
// warm-booted barrier (a fresh StorageBarrier constructed over the same
// store) reports the original initialization timestamp even while still
// sealed, without requiring an unseal first.
//
// Strategy is likewise self-describing while sealed: a fresh StorageBarrier
// constructed over an already-initialized store (daemon restart, or the
// in-process data-plane re-init after enable-encryption) reports the strategy
// recorded in the persisted sealed root-key blob, so status consumers can
// render mode-aware unseal flows before the first unseal. An uninitialized
// barrier still reports an empty strategy.
//
// When the active strategy is Shamir, ShamirThreshold and ShamirTotal reflect
// the scheme that is actually in effect. The in-memory ShamirStrategy is
// preferred (updated by initializeShamirDirect and Rekey); the config values
// are used as the fallback for cold-booted barriers that have not yet unsealed.
func (b *StorageBarrier) Status() *BarrierStatus {
	status := &BarrierStatus{
		Sealed: b.IsSealed(),
	}
	if id := b.strategyOrPersisted(); id != "" {
		status.Strategy = id
		if s, exists := b.strategyByID(id); exists {
			status.HardwareBacked = s.HardwareBacked()
		}
	}
	if t := b.initializedAtOrPersisted(); !t.IsZero() {
		status.InitializedAt = t
	}
	// Populate Shamir scheme fields. The in-memory ShamirStrategy is the
	// most accurate source (it is replaced by initializeShamirDirect and
	// Rekey); fall back to the config for cold-booted / sealed barriers.
	if shamirStrategy := b.ShamirStrategy(); shamirStrategy != nil {
		status.ShamirThreshold = shamirStrategy.Threshold()
		status.ShamirTotal = shamirStrategy.TotalShares()
	} else if b.config.Shamir != nil && b.config.Shamir.Threshold >= 2 {
		status.ShamirThreshold = b.config.Shamir.Threshold
		status.ShamirTotal = b.config.Shamir.TotalShares
	}
	return status
}

// initializedAtOrPersisted returns the in-memory initialization timestamp
// when set, falling back to the persisted metadata on a warm boot. On the
// fallback path it caches the recovered value in memory so subsequent calls
// avoid the backend read. A backend read error or absent metadata yields the
// zero time (the barrier is treated as never initialized).
func (b *StorageBarrier) initializedAtOrPersisted() time.Time {
	if v := b.initializedAt.Load(); v != nil {
		if t, ok := v.(time.Time); ok && !t.IsZero() {
			return t
		}
	}
	persisted, err := b.loadInitializedAt(context.Background())
	if err != nil {
		b.logger.Warn("failed to load persisted barrier metadata",
			slog.String("error", err.Error()))
		return time.Time{}
	}
	if !persisted.IsZero() {
		b.initializedAt.Store(persisted)
	}
	return persisted
}

// strategyOrPersisted returns the in-memory active strategy when set, falling
// back to the strategy recorded in the persisted sealed root-key blob. The
// fallback makes a sealed-but-initialized barrier self-describing before its
// first unseal (daemon restart, or the in-process data-plane re-init after an
// enable-encryption migration), so every status transport reports the real
// keyed strategy instead of an empty string. On the fallback path the
// recovered value is cached so subsequent calls avoid the backend read; once
// the barrier unseals, activeStrategy takes precedence over the cache. An
// uninitialized barrier (no persisted root-key blob) yields "".
func (b *StorageBarrier) strategyOrPersisted() StrategyID {
	if raw := b.activeStrategy.Load(); raw != nil {
		if id, ok := raw.(StrategyID); ok && id != "" {
			return id
		}
	}
	if raw := b.persistedStrategyCache.Load(); raw != nil {
		if id, ok := raw.(StrategyID); ok && id != "" {
			return id
		}
	}
	persisted := b.PersistedStrategy(context.Background())
	if persisted != "" {
		b.persistedStrategyCache.Store(persisted)
	}
	return persisted
}

// SetShamirConfig sets or replaces the Shamir configuration on the barrier.
// This allows the servicer layer to configure Shamir parameters from
// transport request DTOs before calling InitializeShamir.
func (b *StorageBarrier) SetShamirConfig(cfg *ShamirConfig) {
	b.config.Shamir = cfg
}

// ActiveStrategy returns the strategy ID that is currently protecting
// the root key. Returns empty string if the barrier has not been
// initialized or unsealed.
func (b *StorageBarrier) ActiveStrategy() StrategyID {
	if raw := b.activeStrategy.Load(); raw != nil {
		if id, ok := raw.(StrategyID); ok {
			return id
		}
	}
	return ""
}

// SetCeremonyDistributor installs the ceremony distributor used to seal
// shares to enrolled Security Officers during system barrier init and rekey.
func (b *StorageBarrier) SetCeremonyDistributor(d CeremonyDistributor) {
	b.distributor = d
}

// CeremonyDistributor returns the ceremony distributor configured for
// system barrier share distribution, or nil if not set.
func (b *StorageBarrier) CeremonyDistributor() CeremonyDistributor {
	return b.distributor
}

// SetRecoveryDistributor installs the ceremony distributor used to seal
// shares to enrolled recovery Security Officers.
func (b *StorageBarrier) SetRecoveryDistributor(d CeremonyDistributor) {
	b.recoveryDistributor = d
}

// RecoveryDistributor returns the ceremony distributor configured for
// recovery key share distribution, or nil if not set.
func (b *StorageBarrier) RecoveryDistributor() CeremonyDistributor {
	return b.recoveryDistributor
}

// Config returns the barrier's configuration.
func (b *StorageBarrier) Config() BarrierConfig {
	return b.config
}

// EpochBarrier returns the underlying go-quicraft EpochBarrier for
// direct access to epoch-based operations (Rotate, PurgeEpochsBefore, etc).
func (b *StorageBarrier) EpochBarrier() *quicraftseal.Barrier {
	return b.barrier
}

// InvocationCounter returns the barrier's AEAD invocation counter, or nil
// if invocation counting is disabled.
func (b *StorageBarrier) InvocationCounter() InvocationCounter {
	return b.barrier.InvocationCounter()
}

// NonceTracker returns the barrier's nonce reuse detection tracker, or nil
// if nonce tracking is disabled.
func (b *StorageBarrier) NonceTracker() NonceTrackerProvider {
	return b.barrier.NonceTracker()
}

// Strategies returns a copy of the registered sealing strategies.
func (b *StorageBarrier) Strategies() map[StrategyID]SealingStrategy {
	b.strategiesMu.RLock()
	defer b.strategiesMu.RUnlock()
	cp := make(map[StrategyID]SealingStrategy, len(b.strategies))
	for id, s := range b.strategies {
		cp[id] = s
	}
	return cp
}

// Logger returns the barrier's logger.
func (b *StorageBarrier) Logger() *slog.Logger {
	return b.logger
}

// Get retrieves and decrypts the value for the given key. Returns
// ErrSecretNotFound when the underlying backend has no record for the key.
// This normalises across backends that signal "missing" differently:
// MemoryBackend returns ErrSecretNotFound directly, but PebbleStorage (via
// StorageAdapter) returns (nil, nil) which would otherwise propagate into
// Decrypt and surface as a spurious "decryption failed".
func (b *StorageBarrier) Get(ctx context.Context, key string) ([]byte, error) {
	if b.state.Load() != stateUnsealed {
		return nil, ErrSealed
	}
	ciphertext, err := b.base.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if ciphertext == nil {
		return nil, ErrSecretNotFound
	}
	plaintext, err := b.barrier.Decrypt(nil, ciphertext)
	if err != nil {
		return nil, &GetError{Key: key, Err: err}
	}
	return plaintext, nil
}

// Put encrypts the value and stores it under the given key.
func (b *StorageBarrier) Put(ctx context.Context, key string, value []byte) error {
	if b.state.Load() != stateUnsealed {
		return ErrSealed
	}
	encrypted, err := b.barrier.Encrypt(nil, value)
	if err != nil {
		return &PutError{Key: key, Err: err}
	}
	return b.base.Put(ctx, key, encrypted)
}

// PutIfAbsent encrypts the value and atomically stores it under the given key
// only if the key does not already exist. The compare-and-set is delegated to
// the base backend, which owns the atomicity for its consistency domain; the
// CAS condition is key existence, so the non-deterministic ciphertext produced
// by each call does not affect the outcome.
func (b *StorageBarrier) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if b.state.Load() != stateUnsealed {
		return false, ErrSealed
	}
	encrypted, err := b.barrier.Encrypt(nil, value)
	if err != nil {
		return false, &PutError{Key: key, Err: err}
	}
	return b.base.PutIfAbsent(ctx, key, encrypted)
}

// guardedDeleteBackend is the release half of the storage-backend contract:
// a delete conditioned on the value the caller last observed.
//
// It is ASSERTED rather than required because seal.StorageBackend is a type
// alias for go-quicraft's interface, which carries PutIfAbsent but not its
// guarded delete. This module cannot add a method to an upstream interface,
// so the capability is discovered and a backend genuinely lacking it refuses
// with ErrGuardedDeleteUnsupported rather than degrading to a read followed
// by an unguarded delete — which is precisely the window the operation
// exists to close (go-qrdb#41). Every backend in this repository implements
// it; the assertion exists for the upstream gap, not for a local one.
type guardedDeleteBackend interface {
	CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error)
}

// guardedSwapBackend is the edit half of the same contract. It is a separate
// interface from guardedDeleteBackend rather than one combined one so a
// backend can offer either capability independently — a store may gain the
// guarded swap without gaining the guarded delete, and a type assertion that
// demanded both would refuse it for the wrong reason.
type guardedSwapBackend interface {
	CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error)
}

// ErrGuardedSwapUnsupported is returned when a storage backend offers no
// guarded swap. As with the delete, there is no safe fallback: a read followed
// by an unguarded write silently overwrites a value another caller
// legitimately stored in between, which is the race the guard exists to
// eliminate.
var ErrGuardedSwapUnsupported = errors.New("seal: storage backend has no guarded swap")

// ErrGuardedDeleteUnsupported is returned when a storage backend offers no
// guarded delete. There is no safe fallback: a read followed by an
// unguarded delete can remove a value another caller legitimately wrote in
// between.
var ErrGuardedDeleteUnsupported = errors.New("seal: storage backend has no guarded delete")

// CompareAndDelete removes key only when the value stored there still
// decrypts to expected.
//
// It guards on the CIPHERTEXT this call read rather than a fresh encryption
// of expected: encryption is randomised, so encrypting the same plaintext
// twice yields different bytes and such a guard would refuse every time.
// Reading the stored ciphertext, decrypting to compare in plaintext, and
// guarding the delete on those exact bytes keeps the guarantee — any write
// landing in between replaces them, so the guard still refuses.
func (b *StorageBarrier) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if b.state.Load() != stateUnsealed {
		return false, ErrSealed
	}
	stored, err := b.base.Get(ctx, key)
	if err != nil {
		// Absent is an ordinary unchanged outcome, not a failure, and it must
		// read the same way for every atomic verb on this type — see
		// CompareAndSwap for why StorageBackend reports it as an error.
		if errors.Is(err, ErrSecretNotFound) {
			return false, nil
		}
		return false, err
	}
	if stored == nil {
		return false, nil
	}
	plaintext, derr := b.barrier.Decrypt(nil, stored)
	if derr != nil {
		return false, &GetError{Key: key, Err: derr}
	}
	if !bytes.Equal(plaintext, expected) {
		return false, nil
	}
	guarded, ok := b.base.(guardedDeleteBackend)
	if !ok {
		return false, ErrGuardedDeleteUnsupported
	}
	return guarded.CompareAndDelete(ctx, key, stored)
}

// CompareAndSwap replaces the key's value with next only when the DECRYPTED
// stored value still equals expected.
//
// Two different values are compared at two different layers, and that is the
// point. The caller's expectation is a plaintext, so it is compared against
// the decrypted value here; the guard handed to the backend is the CIPHERTEXT
// that was read, so the backend compares the exact bytes it holds. Comparing
// ciphertexts against the caller's expectation would never match — the AEAD is
// nonce-randomised, so encrypting the same plaintext twice gives different
// bytes — and comparing plaintexts at the backend would require the backend to
// hold the key.
func (b *StorageBarrier) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if b.state.Load() != stateUnsealed {
		return false, ErrSealed
	}
	stored, err := b.base.Get(ctx, key)
	if err != nil {
		// A StorageBackend signals a missing key with ErrSecretNotFound, not
		// with (nil, nil) — MemoryBackend and the lifecycle kvSealBackend both
		// do. Propagating it would report "absent" as a failure, when absent
		// is an ordinary unswapped outcome (go-qrdb#56).
		if errors.Is(err, ErrSecretNotFound) {
			return false, nil
		}
		return false, err
	}
	if stored == nil {
		// A swap does not create. See kvstore.AtomicKVStore.CompareAndSwap.
		return false, nil
	}
	plaintext, derr := b.barrier.Decrypt(nil, stored)
	if derr != nil {
		return false, &GetError{Key: key, Err: derr}
	}
	if !bytes.Equal(plaintext, expected) {
		return false, nil
	}
	guarded, ok := b.base.(guardedSwapBackend)
	if !ok {
		return false, ErrGuardedSwapUnsupported
	}
	ciphertext, eerr := b.barrier.Encrypt(nil, next)
	if eerr != nil {
		return false, eerr
	}
	return guarded.CompareAndSwap(ctx, key, stored, ciphertext)
}

// Delete removes the key from storage.
func (b *StorageBarrier) Delete(ctx context.Context, key string) error {
	if b.IsSealed() {
		return ErrSealed
	}
	return b.base.Delete(ctx, key)
}

// List returns all keys matching the given prefix. Keys are not encrypted.
func (b *StorageBarrier) List(ctx context.Context, prefix string) ([]string, error) {
	if b.IsSealed() {
		return nil, ErrSealed
	}
	return b.base.List(ctx, prefix)
}

// Scan iterates over all keys matching the given prefix, decrypts each value,
// and invokes fn for every entry. Iteration stops early if fn returns an error.
func (b *StorageBarrier) Scan(ctx context.Context, prefix string, fn func(key string, value []byte) error) error {
	if b.state.Load() != stateUnsealed {
		return ErrSealed
	}
	return b.base.Scan(ctx, prefix, func(key string, ciphertext []byte) error {
		plaintext, decErr := b.barrier.Decrypt(nil, ciphertext)
		if decErr != nil {
			return &GetError{Key: key, Err: decErr}
		}
		return fn(key, plaintext)
	})
}

// Exists checks if a key exists in storage.
func (b *StorageBarrier) Exists(ctx context.Context, key string) (bool, error) {
	if b.IsSealed() {
		return false, ErrSealed
	}
	return b.base.Exists(ctx, key)
}

// Close seals the barrier and closes the underlying storage backend.
func (b *StorageBarrier) Close() error {
	_ = b.Seal()
	return b.base.Close()
}

// Encrypt encrypts plaintext using the epoch barrier's master key.
// Returns ErrSealed if the barrier is sealed.
func (b *StorageBarrier) Encrypt(plaintext []byte) ([]byte, error) {
	if b.state.Load() != stateUnsealed {
		return nil, ErrSealed
	}
	return b.barrier.Encrypt(nil, plaintext)
}

// Decrypt decrypts ciphertext using the epoch barrier's master key.
// Returns ErrSealed if the barrier is sealed.
func (b *StorageBarrier) Decrypt(ciphertext []byte) ([]byte, error) {
	if b.state.Load() != stateUnsealed {
		return nil, ErrSealed
	}
	return b.barrier.Decrypt(nil, ciphertext)
}

// bestStrategy selects the sealing strategy. There is NO implicit multi-strategy
// ranking (the former DefaultPreferenceOrder is gone). Selection rules:
//   - If an explicit PreferenceOrder is configured, use the first available
//     strategy in that order.
//   - Otherwise, if exactly ONE strategy is registered, use it — the operator
//     explicitly configured a single strategy at setup, so the choice is
//     unambiguous (not a hidden default).
//   - If multiple strategies are registered with no PreferenceOrder, refuse to
//     guess: return ErrNoStrategySpecified. The caller must specify the order.
func (b *StorageBarrier) bestStrategy() (SealingStrategy, error) {
	b.strategiesMu.RLock()
	defer b.strategiesMu.RUnlock()
	if len(b.prefOrder) > 0 {
		for _, id := range b.prefOrder {
			if s, ok := b.strategies[id]; ok && s.Available() {
				return s, nil
			}
		}
		return nil, ErrNoAvailableStrategy
	}
	if len(b.strategies) == 1 {
		for _, s := range b.strategies {
			if s.Available() {
				return s, nil
			}
			return nil, ErrNoAvailableStrategy
		}
	}
	return nil, ErrNoStrategySpecified
}

// countCurrentEntries counts the number of entries in the base backend by
// listing all keys with an empty prefix. Each key represents one encrypt
// invocation for the purposes of seeding the invocation counter.
func (b *StorageBarrier) countCurrentEntries(ctx context.Context) (int64, error) {
	keys, err := b.base.List(ctx, "")
	if err != nil {
		return 0, err
	}
	return int64(len(keys)), nil
}

// MemoryBackend is a simple in-memory StorageBackend for testing
// and standalone usage.
type MemoryBackend struct {
	mu   sync.RWMutex
	data map[string][]byte
}

// Compile-time interface check.
var _ StorageBackend = (*MemoryBackend)(nil)

// NewMemoryBackend returns a new in-memory StorageBackend.
func NewMemoryBackend() *MemoryBackend {
	return &MemoryBackend{
		data: make(map[string][]byte),
	}
}

// Get returns the value for the given key or ErrSecretNotFound.
func (m *MemoryBackend) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.data[key]
	if !ok {
		return nil, ErrSecretNotFound
	}
	out := make([]byte, len(v))
	copy(out, v)
	return out, nil
}

// Put stores the value under the given key.
func (m *MemoryBackend) Put(_ context.Context, key string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	stored := make([]byte, len(data))
	copy(stored, data)
	m.data[key] = stored
	return nil
}

// PutIfAbsent atomically stores the value under the given key only if the key
// does not already exist. The single write lock makes the check-then-write
// atomic for this in-process backend.
func (m *MemoryBackend) PutIfAbsent(_ context.Context, key string, data []byte) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.data[key]; ok {
		return false, nil
	}
	stored := make([]byte, len(data))
	copy(stored, data)
	m.data[key] = stored
	return true, nil
}

// CompareAndDelete removes key only when its stored value still equals
// expected, under the same lock that makes PutIfAbsent indivisible.
func (m *MemoryBackend) CompareAndDelete(_ context.Context, key string, expected []byte) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.data[key]
	if !ok || !bytes.Equal(current, expected) {
		return false, nil
	}
	delete(m.data, key)
	return true, nil
}

// CompareAndSwap replaces the key's value only when it still equals expected.
// An absent key is left absent — a swap is not a create (go-qrdb#56).
func (m *MemoryBackend) CompareAndSwap(_ context.Context, key string, expected, next []byte) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.data[key]
	if !ok || !bytes.Equal(current, expected) {
		return false, nil
	}
	stored := make([]byte, len(next))
	copy(stored, next)
	m.data[key] = stored
	return true, nil
}

// Delete removes the key from storage.
func (m *MemoryBackend) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

// List returns all keys matching the given prefix.
func (m *MemoryBackend) List(_ context.Context, prefix string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var keys []string
	for k := range m.data {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

// Scan iterates over all key-value pairs matching the given prefix and invokes
// fn for each entry. Iteration stops early if fn returns an error.
func (m *MemoryBackend) Scan(_ context.Context, prefix string, fn func(key string, value []byte) error) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for k, v := range m.data {
		if strings.HasPrefix(k, prefix) {
			cp := make([]byte, len(v))
			copy(cp, v)
			if err := fn(k, cp); err != nil {
				return err
			}
		}
	}
	return nil
}

// Exists reports whether the given key exists in storage.
func (m *MemoryBackend) Exists(_ context.Context, key string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.data[key]
	return ok, nil
}

// Close is a no-op for the in-memory backend.
func (m *MemoryBackend) Close() error {
	return nil
}
