// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	quicraftseal "github.com/jeremyhahn/go-quicraft/pkg/seal"
)

// stateSealed and stateUnsealed are the two possible states for the barrier.
const (
	stateSealed   int32 = 0
	stateUnsealed int32 = 1
)

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
	logger     *slog.Logger
	base       StorageBackend
	barrier    *quicraftseal.Barrier
	config     BarrierConfig
	strategies map[StrategyID]SealingStrategy
	prefOrder  []StrategyID

	// state: 0 = sealed, 1 = unsealed
	state atomic.Int32

	// activeStrategy is the strategy ID that sealed the root key.
	activeStrategy atomic.Value

	// accumulator holds the Shamir share accumulator for quorum-based unsealing.
	accumulator atomic.Value
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

	prefOrder := config.PreferenceOrder
	if len(prefOrder) == 0 {
		prefOrder = DefaultPreferenceOrder
	}

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
	epochBarrier := quicraftseal.NewBarrier(epochConfig)

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

	// Delegate initialization to the EpochBarrier. It generates the root
	// key, derives the initial DEK, and seals the root key via the strategy.
	if err := b.barrier.Initialize(ctx, strategy, creds); err != nil {
		emitAudit(b.config.AuditLogger, "initialize", "barrier", b.config.RootKeyPath,
			"deny", map[string]string{"error": err.Error(), "strategy": string(strategy.ID())})
		return err
	}

	// Persist the sealed root key blob.
	sealed := b.barrier.SealedRootKeyData()
	blob, err := json.Marshal(sealed)
	if err != nil {
		return err
	}
	if err := b.base.Put(ctx, b.config.RootKeyPath, blob); err != nil {
		return err
	}

	// Transition to unsealed.
	b.activeStrategy.Store(strategy.ID())
	b.state.Store(stateUnsealed)

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
		if err == ErrSecretNotFound {
			return ErrNotInitialized
		}
		return err
	}

	var sealed SealedRootKey
	if err := json.Unmarshal(blob, &sealed); err != nil {
		return ErrCorruptRootKey
	}

	strategy, ok := b.strategies[sealed.Strategy]
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

// unsealDirect obtains an encryptor from the Shamir strategy using the raw
// root key and transitions to the unsealed state. The EpochBarrier is
// initialized with the root key directly.
// Used by StrategyShamir where shares reconstruct the root key directly.
func (b *StorageBarrier) unsealDirect(rootKey []byte) error {
	shamirStrat, ok := b.strategies[StrategyShamir]
	if !ok {
		return ErrShamirNotConfigured
	}

	// Initialize the EpochBarrier with the reconstructed root key.
	if err := b.barrier.Initialize(context.Background(), shamirStrat, Credentials{}); err != nil {
		// If already initialized, try unseal instead.
		if err == quicraftseal.ErrBarrierAlreadyInit {
			// Load the sealed data so the barrier has the epoch/salt state.
			blob, getErr := b.base.Get(context.Background(), b.config.RootKeyPath)
			if getErr != nil {
				return getErr
			}
			var sealed SealedRootKey
			if jsonErr := json.Unmarshal(blob, &sealed); jsonErr != nil {
				return ErrCorruptRootKey
			}
			if setErr := b.barrier.SetSealedRootKey(&sealed); setErr != nil {
				return setErr
			}
			if unsealErr := b.barrier.Unseal(context.Background(), shamirStrat, Credentials{}); unsealErr != nil {
				return unsealErr
			}
		} else {
			return err
		}
	} else {
		// Persist the sealed root key blob from the new initialization.
		sealed := b.barrier.SealedRootKeyData()
		sealedBlob, marshalErr := json.Marshal(sealed)
		if marshalErr != nil {
			return marshalErr
		}
		if putErr := b.base.Put(context.Background(), b.config.RootKeyPath, sealedBlob); putErr != nil {
			return putErr
		}
	}

	b.activeStrategy.Store(StrategyShamir)
	b.state.Store(stateUnsealed)
	return nil
}

// ShamirStrategy returns the registered ShamirStrategy or nil if not
// registered. This accessor allows callers to inspect the Shamir
// configuration and manage shares.
func (b *StorageBarrier) ShamirStrategy() *ShamirStrategy {
	s, ok := b.strategies[StrategyShamir]
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
func (b *StorageBarrier) Status() *BarrierStatus {
	status := &BarrierStatus{
		Sealed: b.IsSealed(),
	}
	if raw := b.activeStrategy.Load(); raw != nil {
		if id, ok := raw.(StrategyID); ok {
			status.Strategy = id
			if s, exists := b.strategies[id]; exists {
				status.HardwareBacked = s.HardwareBacked()
			}
		}
	}
	return status
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

// Get retrieves and decrypts the value for the given key.
func (b *StorageBarrier) Get(ctx context.Context, key string) ([]byte, error) {
	if b.state.Load() != stateUnsealed {
		return nil, ErrSealed
	}
	ciphertext, err := b.base.Get(ctx, key)
	if err != nil {
		return nil, err
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

// bestStrategy returns the first available strategy in preference order.
func (b *StorageBarrier) bestStrategy() (SealingStrategy, error) {
	for _, id := range b.prefOrder {
		if s, ok := b.strategies[id]; ok && s.Available() {
			return s, nil
		}
	}
	return nil, ErrNoAvailableStrategy
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
