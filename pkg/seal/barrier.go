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
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/awnumar/memguard"
)

// Compile-time interface check.
var _ StorageBackend = (*Barrier)(nil)

// Barrier wraps a StorageBackend with transparent authenticated encryption.
// All values are encrypted before being written to the underlying backend
// and decrypted on read. Keys remain in plaintext.
//
// The encryption is delegated to the active strategy's SymmetricEncrypter.
// Hardware-backed strategies (TPM2, PKCS#11, cloud KMS) use hardware-resident
// keys for data encryption. Software strategies derive a DEK from the root key.
//
// The barrier lifecycle is:
//  1. Initialize: generate root key, seal with best strategy, obtain encryptor
//  2. Unseal: load sealed root key, unseal, obtain encryptor
//  3. Seal: release encryptor, block further operations
type Barrier struct {
	logger     *slog.Logger
	base       StorageBackend
	config     BarrierConfig
	strategies map[StrategyID]SealingStrategy
	prefOrder  []StrategyID

	// state: 0 = sealed, 1 = unsealed
	state atomic.Int32

	// encryptor holds the active SymmetricEncrypter. Only set while unsealed.
	encryptor atomic.Value

	// activeStrategy is the strategy ID that sealed the root key.
	activeStrategy atomic.Value

	// accumulator holds the Shamir share accumulator for quorum-based unsealing.
	accumulator atomic.Value
}

// NewBarrier creates a new Barrier that encrypts all values stored in
// base. At least one SealingStrategy must be provided.
func NewBarrier(
	logger *slog.Logger,
	base StorageBackend,
	config BarrierConfig,
	strategies ...SealingStrategy,
) (*Barrier, error) {
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

	b := &Barrier{
		logger:     logger,
		base:       base,
		config:     config,
		strategies: stratMap,
		prefOrder:  prefOrder,
	}
	// Start sealed (state = 0 is zero value).
	return b, nil
}

// Initialize generates a new 32-byte root key, seals it with the best
// available strategy, stores the sealed blob in the backend, and obtains
// the strategy's encryptor. The barrier transitions to unsealed state.
//
// This must be called exactly once for a new barrier. Subsequent calls
// return ErrAlreadyInitialized.
func (b *Barrier) Initialize(ctx context.Context, creds Credentials) error {
	exists, err := b.base.Exists(b.config.RootKeyPath)
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

	// Generate root key.
	rootKey := make([]byte, aesKeySize)
	if _, err := io.ReadFull(rand.Reader, rootKey); err != nil {
		return err
	}

	// Seal the root key.
	sealed, err := strategy.SealRootKey(ctx, rootKey, creds)
	if err != nil {
		emitAudit(b.config.AuditLogger, "initialize", "barrier", b.config.RootKeyPath,
			"deny", map[string]string{"error": err.Error(), "strategy": string(strategy.ID())})
		memguard.WipeBytes(rootKey)
		return err
	}

	// Persist the sealed blob.
	blob, err := json.Marshal(sealed)
	if err != nil {
		memguard.WipeBytes(rootKey)
		return err
	}
	if err := b.base.Put(b.config.RootKeyPath, blob); err != nil {
		memguard.WipeBytes(rootKey)
		return err
	}

	// Obtain the strategy's encryptor for data operations.
	enc, err := strategy.BarrierEncryptor(ctx, rootKey)
	if err != nil {
		memguard.WipeBytes(rootKey)
		return err
	}

	// Zero root key.
	memguard.WipeBytes(rootKey)

	// Transition to unsealed.
	b.encryptor.Store(enc)
	b.activeStrategy.Store(strategy.ID())
	b.state.Store(1)

	emitAudit(b.config.AuditLogger, "initialize", "barrier", b.config.RootKeyPath,
		"allow", map[string]string{"strategy": string(strategy.ID())})

	b.logger.Info("barrier initialized",
		slog.String("strategy", string(strategy.ID())),
		slog.Bool("hardware_backed", strategy.HardwareBacked()))

	return nil
}

// Unseal loads the sealed root key blob from the backend, unseals it
// with the matching strategy, and obtains the strategy's encryptor.
func (b *Barrier) Unseal(ctx context.Context, creds Credentials) error {
	if b.state.Load() == 1 {
		return ErrAlreadyUnsealed
	}

	blob, err := b.base.Get(b.config.RootKeyPath)
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

	rootKey, err := strategy.UnsealRootKey(ctx, &sealed, creds)
	if err != nil {
		emitAudit(b.config.AuditLogger, "unseal", "barrier", b.config.RootKeyPath,
			"deny", map[string]string{"error": err.Error(), "strategy": string(sealed.Strategy)})
		return err
	}

	// Obtain the strategy's encryptor for data operations.
	enc, err := strategy.BarrierEncryptor(ctx, rootKey)
	if err != nil {
		memguard.WipeBytes(rootKey)
		return err
	}

	// Zero root key immediately.
	memguard.WipeBytes(rootKey)

	b.encryptor.Store(enc)
	b.activeStrategy.Store(sealed.Strategy)
	b.state.Store(1)

	emitAudit(b.config.AuditLogger, "unseal", "barrier", b.config.RootKeyPath,
		"allow", map[string]string{"strategy": string(sealed.Strategy)})

	b.logger.Info("barrier unsealed",
		slog.String("strategy", string(sealed.Strategy)))

	return nil
}

// unsealDirect obtains an encryptor from the Shamir strategy using the raw
// root key and transitions to unsealed state. The root key is zeroed after
// the encryptor is obtained.
// Used by StrategyShamir where shares reconstruct the root key directly.
func (b *Barrier) unsealDirect(rootKey []byte) error {
	shamirStrat, ok := b.strategies[StrategyShamir]
	if !ok {
		memguard.WipeBytes(rootKey)
		return ErrShamirNotConfigured
	}

	enc, err := shamirStrat.BarrierEncryptor(context.Background(), rootKey)
	if err != nil {
		memguard.WipeBytes(rootKey)
		return err
	}
	memguard.WipeBytes(rootKey)

	b.encryptor.Store(enc)
	b.activeStrategy.Store(StrategyShamir)
	b.state.Store(1)
	return nil
}

// ShamirStrategy returns the registered ShamirStrategy or nil if not
// registered. This accessor allows callers to inspect the Shamir
// configuration and manage shares.
func (b *Barrier) ShamirStrategy() *ShamirStrategy {
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

// GetMasterKey returns a copy of the data encryption key (DEK).
// This is only available for software-backed strategies that derive a DEK.
// For hardware-backed strategies, the key never leaves hardware and
// ErrHardwareEncryptorRequired is returned.
//
// The barrier must be unsealed. Returns ErrSealed if sealed,
// ErrEncryptorNotAvailable if the encryptor has not been initialized.
func (b *Barrier) GetMasterKey() ([]byte, error) {
	enc, err := b.getEncryptor()
	if err != nil {
		return nil, err
	}
	sw, ok := enc.(*softwareBarrierEncryptor)
	if !ok {
		return nil, ErrHardwareEncryptorRequired
	}
	return sw.DEK(), nil
}

// Encryptor returns the active SymmetricEncrypter used for barrier data
// operations. The barrier must be unsealed.
func (b *Barrier) Encryptor() (SymmetricEncrypter, error) {
	return b.getEncryptor()
}

// Seal transitions the barrier to sealed state and releases the encryptor.
// For software encryptors, the DEK memory is zeroed.
// All subsequent storage operations will return ErrSealed until Unseal
// is called again. Seal is idempotent.
func (b *Barrier) Seal() error {
	if b.state.Load() == 0 {
		return nil // already sealed, idempotent
	}

	// Zero the encryptor's key material if it supports it.
	if raw := b.encryptor.Load(); raw != nil {
		if enc, ok := raw.(SymmetricEncrypter); ok {
			if sw, isSoftware := enc.(*softwareBarrierEncryptor); isSoftware {
				sw.Zero()
			}
		}
	}

	// Transition to sealed state. The state flag gates all operations via
	// getEncryptor(), so we do not need to nil out the encryptor value.
	// atomic.Value does not support storing nil interface values.
	b.state.Store(0)

	emitAudit(b.config.AuditLogger, "seal", "barrier", b.config.RootKeyPath,
		"allow", nil)

	b.logger.Info("barrier sealed")
	return nil
}

// IsSealed reports whether the barrier is in sealed state.
func (b *Barrier) IsSealed() bool {
	return b.state.Load() == 0
}

// Status returns the current barrier status.
func (b *Barrier) Status() *BarrierStatus {
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
func (b *Barrier) SetShamirConfig(cfg *ShamirConfig) {
	b.config.Shamir = cfg
}

// ActiveStrategy returns the strategy ID that is currently protecting
// the root key. Returns empty string if the barrier has not been
// initialized or unsealed.
func (b *Barrier) ActiveStrategy() StrategyID {
	if raw := b.activeStrategy.Load(); raw != nil {
		if id, ok := raw.(StrategyID); ok {
			return id
		}
	}
	return ""
}

// Get retrieves and decrypts the value for the given key.
func (b *Barrier) Get(key string) ([]byte, error) {
	enc, err := b.getEncryptor()
	if err != nil {
		return nil, err
	}
	ciphertext, err := b.base.Get(key)
	if err != nil {
		return nil, err
	}
	return barrierDecrypt(enc, ciphertext)
}

// Put encrypts the value and stores it under the given key.
func (b *Barrier) Put(key string, value []byte) error {
	enc, err := b.getEncryptor()
	if err != nil {
		return err
	}
	ciphertext, err := barrierEncrypt(enc, value)
	if err != nil {
		return err
	}
	return b.base.Put(key, ciphertext)
}

// Delete removes the key from storage.
func (b *Barrier) Delete(key string) error {
	if b.IsSealed() {
		return ErrSealed
	}
	return b.base.Delete(key)
}

// List returns all keys matching the given prefix. Keys are not encrypted.
func (b *Barrier) List(prefix string) ([]string, error) {
	if b.IsSealed() {
		return nil, ErrSealed
	}
	return b.base.List(prefix)
}

// Exists checks if a key exists in storage.
func (b *Barrier) Exists(key string) (bool, error) {
	if b.IsSealed() {
		return false, ErrSealed
	}
	return b.base.Exists(key)
}

// Close seals the barrier and closes the underlying storage backend.
func (b *Barrier) Close() error {
	_ = b.Seal()
	return b.base.Close()
}

// getEncryptor returns the current encryptor or ErrSealed/ErrEncryptorNotAvailable.
func (b *Barrier) getEncryptor() (SymmetricEncrypter, error) {
	if b.IsSealed() {
		return nil, ErrSealed
	}
	raw := b.encryptor.Load()
	if raw == nil {
		return nil, ErrEncryptorNotAvailable
	}
	enc, ok := raw.(SymmetricEncrypter)
	if !ok || enc == nil {
		return nil, ErrEncryptorNotAvailable
	}
	return enc, nil
}

// bestStrategy returns the first available strategy in preference order.
func (b *Barrier) bestStrategy() (SealingStrategy, error) {
	for _, id := range b.prefOrder {
		if s, ok := b.strategies[id]; ok && s.Available() {
			return s, nil
		}
	}
	return nil, ErrNoAvailableStrategy
}

// barrierEncrypt produces [version:1][enc.Encrypt(plaintext)]
// using the strategy's SymmetricEncrypter.
func barrierEncrypt(enc SymmetricEncrypter, plaintext []byte) ([]byte, error) {
	encrypted, err := enc.Encrypt(plaintext)
	if err != nil {
		return nil, err
	}

	// Prepend version byte.
	out := make([]byte, 1+len(encrypted))
	out[0] = barrierVersion
	copy(out[1:], encrypted)
	return out, nil
}

// barrierDecrypt parses encrypted data and returns plaintext.
// Wire format: [version:1][enc.Encrypt(plaintext)]
func barrierDecrypt(enc SymmetricEncrypter, data []byte) ([]byte, error) {
	if len(data) < 2 {
		return nil, ErrCorruptRootKey
	}
	if data[0] != barrierVersion {
		return nil, ErrCorruptRootKey
	}

	plaintext, err := enc.Decrypt(data[1:])
	if err != nil {
		return nil, ErrInvalidCredentials
	}

	if plaintext == nil {
		plaintext = []byte{}
	}
	return plaintext, nil
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
func (m *MemoryBackend) Get(key string) ([]byte, error) {
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
func (m *MemoryBackend) Put(key string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	stored := make([]byte, len(data))
	copy(stored, data)
	m.data[key] = stored
	return nil
}

// Delete removes the key from storage.
func (m *MemoryBackend) Delete(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

// List returns all keys matching the given prefix.
func (m *MemoryBackend) List(prefix string) ([]string, error) {
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

// Exists reports whether the given key exists in storage.
func (m *MemoryBackend) Exists(key string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.data[key]
	return ok, nil
}

// Close is a no-op for the in-memory backend.
func (m *MemoryBackend) Close() error {
	return nil
}
