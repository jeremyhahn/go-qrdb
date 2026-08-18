// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package dek

import (
	"context"
	"crypto/rand"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// StorageBackend is a context-aware interface for key-value storage using
// string keys. This is defined locally to avoid pulling in transitive
// dependencies on go-quicraft; it is structurally compatible with
// seal.StorageBackend.
type StorageBackend interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key string, data []byte) error
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]string, error)
	Close() error
}

// TenantDEKManager manages per-tenant Data Encryption Keys.
// Each tenant gets an isolated DEK that is wrapped by the cluster DEK.
//
// Key Hierarchy:
//  1. Master Key (per node) - from SealManager
//  2. Cluster DEK (shared) - wraps tenant DEKs
//  3. Tenant DEKs (per tenant) - encrypts tenant data
//
// Storage:
//
//	Tenant DEKs are stored at __sys:dek/tenant/{tenantID} in wrapped form.
type TenantDEKManager interface {
	// GetOrCreateTenantDEK returns the DEK for a tenant, creating if needed.
	GetOrCreateTenantDEK(ctx context.Context, tenantID string) ([]byte, error)

	// GetTenantDEK returns the DEK for a tenant.
	// Returns an error if the tenant DEK does not exist.
	GetTenantDEK(ctx context.Context, tenantID string) ([]byte, error)

	// RotateTenantDEK generates a new DEK version for a tenant.
	RotateTenantDEK(ctx context.Context, tenantID string) error

	// DeleteTenantDEK removes a tenant's DEK (tenant deletion).
	DeleteTenantDEK(ctx context.Context, tenantID string) error

	// GetTenantDEKStatus returns the status of a tenant's DEK.
	GetTenantDEKStatus(ctx context.Context, tenantID string) (*TenantDEKStatus, error)

	// ListTenantDEKs returns all tenant IDs that have DEKs.
	ListTenantDEKs(ctx context.Context) ([]string, error)

	// Close releases resources.
	Close() error
}

// TenantDEKStatus represents the status of a tenant DEK.
type TenantDEKStatus struct {
	TenantID   string    `json:"tenant_id"`
	Version    uint64    `json:"version"`
	Algorithm  string    `json:"algorithm"`
	KeyBytes   int       `json:"key_bytes"`
	CreatedAt  time.Time `json:"created_at"`
	RotatedAt  time.Time `json:"rotated_at,omitempty"`
	AccessedAt time.Time `json:"accessed_at,omitempty"`
}

// tenantDEKEntry stores a tenant's DEK metadata and wrapped key.
type tenantDEKEntry struct {
	TenantID    string    `json:"tenant_id"`
	WrappedDEK  []byte    `json:"wrapped_dek"`
	Version     uint64    `json:"version"`
	Algorithm   string    `json:"algorithm"`
	KeyBytes    int       `json:"key_bytes"`
	CreatedAt   time.Time `json:"created_at"`
	RotatedAt   time.Time `json:"rotated_at,omitempty"`
	accessedAt  time.Time // Not persisted, tracked in memory
	plaintextMu sync.RWMutex
	plaintext   []byte // Cached plaintext (memory only)
}

// tenantDEKMetrics holds metrics for tenant DEK operations.
type tenantDEKMetrics struct {
	creates  atomic.Uint64
	gets     atomic.Uint64
	rotates  atomic.Uint64
	deletes  atomic.Uint64
	cacheHit atomic.Uint64
}

// defaultTenantDEKManager is the production implementation of TenantDEKManager.
type defaultTenantDEKManager struct {
	clusterDEK *ClusterDEKManager
	storage    StorageBackend
	logger     *slog.Logger

	// In-memory cache of tenant DEKs (tenantID -> entry).
	cache   map[string]*tenantDEKEntry
	cacheMu sync.RWMutex

	// Default configuration.
	defaultAlgorithm string
	defaultKeyBytes  int

	// Metrics.
	metrics *tenantDEKMetrics

	closed atomic.Bool
}

// TenantDEKManagerConfig contains configuration for TenantDEKManager.
type TenantDEKManagerConfig struct {
	// DefaultAlgorithm is the encryption algorithm for new tenant DEKs.
	// Default: "AES-256-GCM"
	DefaultAlgorithm string

	// DefaultKeyBytes is the key size for new tenant DEKs.
	// Default: 32 (256 bits)
	DefaultKeyBytes int
}

// NewTenantDEKManager creates a new tenant DEK manager.
//
// Parameters:
//   - clusterDEK: ClusterDEKManager for wrapping tenant DEKs
//   - storage: StorageBackend for persisting wrapped tenant DEKs
//   - logger: Logger for structured logging
//   - config: Optional configuration (nil for defaults)
func NewTenantDEKManager(
	clusterDEK *ClusterDEKManager,
	storage StorageBackend,
	logger *slog.Logger,
	config *TenantDEKManagerConfig,
) (TenantDEKManager, error) {
	if clusterDEK == nil {
		return nil, &NilInputError{Op: "NewTenantDEKManager", Field: "cluster DEK manager"}
	}
	if storage == nil {
		return nil, &NilInputError{Op: "NewTenantDEKManager", Field: "storage"}
	}
	if logger == nil {
		return nil, &NilInputError{Op: "NewTenantDEKManager", Field: "logger"}
	}

	// Apply defaults.
	algorithm := AlgorithmAES256GCM
	keyBytes := AESKeySize
	if config != nil {
		if config.DefaultAlgorithm != "" {
			algorithm = config.DefaultAlgorithm
		}
		if config.DefaultKeyBytes > 0 {
			keyBytes = config.DefaultKeyBytes
		}
	}

	mgr := &defaultTenantDEKManager{
		clusterDEK:       clusterDEK,
		storage:          storage,
		logger:           logger.With("component", "tenant_dek_manager"),
		cache:            make(map[string]*tenantDEKEntry),
		defaultAlgorithm: algorithm,
		defaultKeyBytes:  keyBytes,
		metrics:          &tenantDEKMetrics{},
	}

	mgr.logger.Info("tenant DEK manager created",
		"algorithm", algorithm, "key_bytes", keyBytes)

	return mgr, nil
}

// tenantDEKStorageKey returns the storage key for a tenant's DEK.
func tenantDEKStorageKey(tenantID string) string {
	return "__sys:dek/tenant/" + tenantID
}

// GetOrCreateTenantDEK returns the DEK for a tenant, creating if needed.
func (m *defaultTenantDEKManager) GetOrCreateTenantDEK(ctx context.Context, tenantID string) ([]byte, error) {
	if m.closed.Load() {
		return nil, &ManagerClosedError{Op: "TenantDEKManager.GetOrCreateTenantDEK", Resource: "tenant DEK manager"}
	}

	if tenantID == "" {
		return nil, &InvalidInputError{
			Op:  "TenantDEKManager.GetOrCreateTenantDEK",
			Err: &RequiredFieldError{Field: "tenant ID"},
		}
	}

	// Try to get existing DEK.
	dek, err := m.GetTenantDEK(ctx, tenantID)
	if err == nil {
		return dek, nil
	}

	// Check if it's a not found error.
	if !IsNotFound(err) {
		return nil, err
	}

	// Create new DEK for tenant.
	return m.createTenantDEK(ctx, tenantID)
}

// createTenantDEK creates a new DEK for a tenant.
func (m *defaultTenantDEKManager) createTenantDEK(ctx context.Context, tenantID string) ([]byte, error) {
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()

	// Double-check after acquiring lock.
	if entry, ok := m.cache[tenantID]; ok {
		entry.plaintextMu.RLock()
		dek := make([]byte, len(entry.plaintext))
		copy(dek, entry.plaintext)
		entry.plaintextMu.RUnlock()
		return dek, nil
	}

	// Generate new DEK.
	dekPlaintext := make([]byte, m.defaultKeyBytes)
	if _, err := io.ReadFull(rand.Reader, dekPlaintext); err != nil {
		return nil, &CryptoError{Op: "TenantDEKManager.createTenantDEK", Detail: "generate DEK", Err: err}
	}

	// Wrap with cluster DEK.
	wrappedDEK, err := m.wrapDEK(dekPlaintext)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	entry := &tenantDEKEntry{
		TenantID:   tenantID,
		WrappedDEK: wrappedDEK,
		Version:    1,
		Algorithm:  m.defaultAlgorithm,
		KeyBytes:   m.defaultKeyBytes,
		CreatedAt:  now,
		accessedAt: now,
		plaintext:  dekPlaintext,
	}

	// Store to persistent storage.
	if err := m.persistEntry(ctx, entry); err != nil {
		return nil, err
	}

	// Add to cache.
	m.cache[tenantID] = entry
	m.metrics.creates.Add(1)

	m.logger.Info("created tenant DEK",
		"tenant_id", tenantID, "algorithm", entry.Algorithm, "key_bytes", entry.KeyBytes)

	// Return a copy.
	result := make([]byte, len(dekPlaintext))
	copy(result, dekPlaintext)
	return result, nil
}

// GetTenantDEK returns the DEK for a tenant.
func (m *defaultTenantDEKManager) GetTenantDEK(ctx context.Context, tenantID string) ([]byte, error) {
	if m.closed.Load() {
		return nil, &ManagerClosedError{Op: "TenantDEKManager.GetTenantDEK", Resource: "tenant DEK manager"}
	}

	if tenantID == "" {
		return nil, &InvalidInputError{
			Op:  "TenantDEKManager.GetTenantDEK",
			Err: &RequiredFieldError{Field: "tenant ID"},
		}
	}

	m.metrics.gets.Add(1)

	// Check cache first.
	m.cacheMu.RLock()
	entry, ok := m.cache[tenantID]
	m.cacheMu.RUnlock()

	if ok {
		entry.plaintextMu.Lock()
		if entry.plaintext != nil {
			dek := make([]byte, len(entry.plaintext))
			copy(dek, entry.plaintext)
			entry.accessedAt = time.Now()
			entry.plaintextMu.Unlock()
			m.metrics.cacheHit.Add(1)
			return dek, nil
		}
		entry.plaintextMu.Unlock()
	}

	// Load from storage.
	return m.loadTenantDEK(ctx, tenantID)
}

// loadTenantDEK loads a tenant DEK from storage.
func (m *defaultTenantDEKManager) loadTenantDEK(ctx context.Context, tenantID string) ([]byte, error) {
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()

	// Double-check cache after acquiring lock.
	if entry, ok := m.cache[tenantID]; ok {
		entry.plaintextMu.RLock()
		if entry.plaintext != nil {
			dek := make([]byte, len(entry.plaintext))
			copy(dek, entry.plaintext)
			entry.plaintextMu.RUnlock()
			return dek, nil
		}
		entry.plaintextMu.RUnlock()
	}

	// Load from storage.
	key := tenantDEKStorageKey(tenantID)
	data, err := m.storage.Get(ctx, key)
	if err != nil {
		return nil, &NotFoundError{Resource: "tenant DEK", ID: tenantID}
	}

	// Decode entry.
	entry, err := decodeTenantDEKEntry(data)
	if err != nil {
		return nil, &DecodeError{Op: "TenantDEKManager.loadTenantDEK", Resource: "tenant DEK", Err: err}
	}

	// Unwrap DEK.
	plaintext, err := m.unwrapDEK(entry.WrappedDEK)
	if err != nil {
		return nil, err
	}

	entry.plaintext = plaintext
	entry.accessedAt = time.Now()
	m.cache[tenantID] = entry

	// Return a copy.
	result := make([]byte, len(plaintext))
	copy(result, plaintext)
	return result, nil
}

// RotateTenantDEK generates a new DEK version for a tenant.
func (m *defaultTenantDEKManager) RotateTenantDEK(ctx context.Context, tenantID string) error {
	if m.closed.Load() {
		return &ManagerClosedError{Op: "TenantDEKManager.RotateTenantDEK", Resource: "tenant DEK manager"}
	}

	if tenantID == "" {
		return &InvalidInputError{
			Op:  "TenantDEKManager.RotateTenantDEK",
			Err: &RequiredFieldError{Field: "tenant ID"},
		}
	}

	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()

	// Get current entry.
	entry, ok := m.cache[tenantID]
	if !ok {
		// Try loading from storage.
		key := tenantDEKStorageKey(tenantID)
		data, err := m.storage.Get(ctx, key)
		if err != nil {
			return &NotFoundError{Resource: "tenant DEK", ID: tenantID}
		}
		entry, err = decodeTenantDEKEntry(data)
		if err != nil {
			return &DecodeError{Op: "TenantDEKManager.RotateTenantDEK", Resource: "tenant DEK", Err: err}
		}
	}

	// Generate new DEK.
	newDEK := make([]byte, entry.KeyBytes)
	if _, err := io.ReadFull(rand.Reader, newDEK); err != nil {
		return &CryptoError{Op: "TenantDEKManager.RotateTenantDEK", Detail: "generate new DEK", Err: err}
	}

	// Wrap new DEK.
	wrappedDEK, err := m.wrapDEK(newDEK)
	if err != nil {
		return err
	}

	// Update entry.
	now := time.Now()
	entry.WrappedDEK = wrappedDEK
	entry.Version++
	entry.RotatedAt = now
	entry.plaintextMu.Lock()
	entry.plaintext = newDEK
	entry.plaintextMu.Unlock()
	entry.accessedAt = now

	// Persist.
	if err := m.persistEntry(ctx, entry); err != nil {
		return err
	}

	m.cache[tenantID] = entry
	m.metrics.rotates.Add(1)

	m.logger.Info("rotated tenant DEK",
		"tenant_id", tenantID, "version", entry.Version)

	return nil
}

// DeleteTenantDEK removes a tenant's DEK.
func (m *defaultTenantDEKManager) DeleteTenantDEK(ctx context.Context, tenantID string) error {
	if m.closed.Load() {
		return &ManagerClosedError{Op: "TenantDEKManager.DeleteTenantDEK", Resource: "tenant DEK manager"}
	}

	if tenantID == "" {
		return &InvalidInputError{
			Op:  "TenantDEKManager.DeleteTenantDEK",
			Err: &RequiredFieldError{Field: "tenant ID"},
		}
	}

	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()

	// Remove from cache.
	if entry, ok := m.cache[tenantID]; ok {
		entry.plaintextMu.Lock()
		SecureMemZero(entry.plaintext)
		entry.plaintext = nil
		entry.plaintextMu.Unlock()
		delete(m.cache, tenantID)
	}

	// Delete from storage.
	key := tenantDEKStorageKey(tenantID)
	if err := m.storage.Delete(ctx, key); err != nil {
		// Ignore not found errors for idempotent deletes.
		if !IsNotFound(err) {
			return &StorageOperationError{Op: "TenantDEKManager.DeleteTenantDEK", Detail: "delete tenant DEK", Err: err}
		}
	}

	m.metrics.deletes.Add(1)

	m.logger.Info("deleted tenant DEK", "tenant_id", tenantID)

	return nil
}

// GetTenantDEKStatus returns the status of a tenant's DEK.
func (m *defaultTenantDEKManager) GetTenantDEKStatus(ctx context.Context, tenantID string) (*TenantDEKStatus, error) {
	if m.closed.Load() {
		return nil, &ManagerClosedError{Op: "TenantDEKManager.GetTenantDEKStatus", Resource: "tenant DEK manager"}
	}

	if tenantID == "" {
		return nil, &InvalidInputError{
			Op:  "TenantDEKManager.GetTenantDEKStatus",
			Err: &RequiredFieldError{Field: "tenant ID"},
		}
	}

	// Check cache first.
	m.cacheMu.RLock()
	entry, ok := m.cache[tenantID]
	m.cacheMu.RUnlock()

	if !ok {
		// Load from storage.
		key := tenantDEKStorageKey(tenantID)
		data, err := m.storage.Get(ctx, key)
		if err != nil {
			return nil, &NotFoundError{Resource: "tenant DEK", ID: tenantID}
		}
		entry, err = decodeTenantDEKEntry(data)
		if err != nil {
			return nil, &DecodeError{Op: "TenantDEKManager.GetTenantDEKStatus", Resource: "tenant DEK", Err: err}
		}
	}

	// Read accessedAt safely.
	entry.plaintextMu.RLock()
	accessedAt := entry.accessedAt
	entry.plaintextMu.RUnlock()

	return &TenantDEKStatus{
		TenantID:   entry.TenantID,
		Version:    entry.Version,
		Algorithm:  entry.Algorithm,
		KeyBytes:   entry.KeyBytes,
		CreatedAt:  entry.CreatedAt,
		RotatedAt:  entry.RotatedAt,
		AccessedAt: accessedAt,
	}, nil
}

// ListTenantDEKs returns all tenant IDs that have DEKs.
func (m *defaultTenantDEKManager) ListTenantDEKs(ctx context.Context) ([]string, error) {
	if m.closed.Load() {
		return nil, &ManagerClosedError{Op: "TenantDEKManager.ListTenantDEKs", Resource: "tenant DEK manager"}
	}

	prefix := "__sys:dek/tenant/"
	keys, err := m.storage.List(ctx, prefix)
	if err != nil {
		return nil, &StorageOperationError{Op: "TenantDEKManager.ListTenantDEKs", Detail: "list tenant DEKs", Err: err}
	}

	tenantIDs := make([]string, 0, len(keys))
	for _, key := range keys {
		if len(key) > len(prefix) {
			tenantID := key[len(prefix):]
			tenantIDs = append(tenantIDs, tenantID)
		}
	}

	return tenantIDs, nil
}

// Close releases resources.
func (m *defaultTenantDEKManager) Close() error {
	if m.closed.Swap(true) {
		return nil // Already closed
	}

	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()

	// Zero out all cached plaintexts.
	for _, entry := range m.cache {
		entry.plaintextMu.Lock()
		SecureMemZero(entry.plaintext)
		entry.plaintext = nil
		entry.plaintextMu.Unlock()
	}
	m.cache = nil

	m.logger.Info("tenant DEK manager closed")
	return nil
}

// wrapDEK wraps a plaintext DEK with the cluster DEK using AES-256-GCM. The
// cluster manager applies the key internally, so the cluster DEK never leaves
// its protected memory.
func (m *defaultTenantDEKManager) wrapDEK(plaintext []byte) ([]byte, error) {
	ciphertext, err := m.clusterDEK.WrapKey(plaintext)
	if err != nil {
		return nil, &CryptoError{Op: "TenantDEKManager.wrapDEK", Detail: "encrypt DEK", Err: err}
	}

	return ciphertext, nil
}

// unwrapDEK unwraps a wrapped DEK using the cluster DEK with AES-256-GCM. As
// in wrapDEK, the cluster DEK stays inside the cluster manager's protected
// memory.
func (m *defaultTenantDEKManager) unwrapDEK(wrapped []byte) ([]byte, error) {
	if len(wrapped) < AESGCMOverhead {
		return nil, &CiphertextTooShortError{Op: "TenantDEKManager.unwrapDEK", Detail: "wrapped DEK too short"}
	}

	dekPlaintext, err := m.clusterDEK.UnwrapKey(wrapped)
	if err != nil {
		return nil, &CryptoError{Op: "TenantDEKManager.unwrapDEK", Detail: "decrypt DEK", Err: err}
	}

	return dekPlaintext, nil
}

// persistEntry saves a tenant DEK entry to storage.
func (m *defaultTenantDEKManager) persistEntry(ctx context.Context, entry *tenantDEKEntry) error {
	data, err := encodeTenantDEKEntry(entry)
	if err != nil {
		return &EncodeError{Op: "TenantDEKManager.persistEntry", Resource: "tenant DEK entry", Err: err}
	}

	key := tenantDEKStorageKey(entry.TenantID)
	if err := m.storage.Put(ctx, key, data); err != nil {
		return &StorageOperationError{Op: "TenantDEKManager.persistEntry", Detail: "store tenant DEK entry", Err: err}
	}

	return nil
}

// encodeTenantDEKEntry encodes a tenant DEK entry to bytes.
// Format: version(8) + algorithm_len(2) + algorithm + tenant_id_len(2) + tenant_id
//
//   - key_bytes(4) + wrapped_dek_len(4) + wrapped_dek + created_at(8) + rotated_at(8)
func encodeTenantDEKEntry(entry *tenantDEKEntry) ([]byte, error) {
	algorithmBytes := []byte(entry.Algorithm)
	tenantIDBytes := []byte(entry.TenantID)

	// Calculate total size.
	size := 8 + // version
		2 + len(algorithmBytes) + // algorithm
		2 + len(tenantIDBytes) + // tenant ID
		4 + // key_bytes
		4 + len(entry.WrappedDEK) + // wrapped DEK
		8 + // created_at
		8 // rotated_at

	data := make([]byte, size)
	offset := 0

	// Version (8 bytes, big-endian).
	data[offset] = byte(entry.Version >> 56)
	data[offset+1] = byte(entry.Version >> 48)
	data[offset+2] = byte(entry.Version >> 40)
	data[offset+3] = byte(entry.Version >> 32)
	data[offset+4] = byte(entry.Version >> 24)
	data[offset+5] = byte(entry.Version >> 16)
	data[offset+6] = byte(entry.Version >> 8)
	data[offset+7] = byte(entry.Version)
	offset += 8

	// Algorithm length and bytes.
	data[offset] = byte(len(algorithmBytes) >> 8)
	data[offset+1] = byte(len(algorithmBytes))
	offset += 2
	copy(data[offset:], algorithmBytes)
	offset += len(algorithmBytes)

	// Tenant ID length and bytes.
	data[offset] = byte(len(tenantIDBytes) >> 8)
	data[offset+1] = byte(len(tenantIDBytes))
	offset += 2
	copy(data[offset:], tenantIDBytes)
	offset += len(tenantIDBytes)

	// Key bytes (4 bytes, big-endian).
	data[offset] = byte(entry.KeyBytes >> 24)
	data[offset+1] = byte(entry.KeyBytes >> 16)
	data[offset+2] = byte(entry.KeyBytes >> 8)
	data[offset+3] = byte(entry.KeyBytes)
	offset += 4

	// Wrapped DEK length and bytes.
	data[offset] = byte(len(entry.WrappedDEK) >> 24)
	data[offset+1] = byte(len(entry.WrappedDEK) >> 16)
	data[offset+2] = byte(len(entry.WrappedDEK) >> 8)
	data[offset+3] = byte(len(entry.WrappedDEK))
	offset += 4
	copy(data[offset:], entry.WrappedDEK)
	offset += len(entry.WrappedDEK)

	// Created at (8 bytes, Unix nano).
	createdNano := entry.CreatedAt.UnixNano()
	data[offset] = byte(createdNano >> 56)
	data[offset+1] = byte(createdNano >> 48)
	data[offset+2] = byte(createdNano >> 40)
	data[offset+3] = byte(createdNano >> 32)
	data[offset+4] = byte(createdNano >> 24)
	data[offset+5] = byte(createdNano >> 16)
	data[offset+6] = byte(createdNano >> 8)
	data[offset+7] = byte(createdNano)
	offset += 8

	// Rotated at (8 bytes, Unix nano).
	rotatedNano := entry.RotatedAt.UnixNano()
	data[offset] = byte(rotatedNano >> 56)
	data[offset+1] = byte(rotatedNano >> 48)
	data[offset+2] = byte(rotatedNano >> 40)
	data[offset+3] = byte(rotatedNano >> 32)
	data[offset+4] = byte(rotatedNano >> 24)
	data[offset+5] = byte(rotatedNano >> 16)
	data[offset+6] = byte(rotatedNano >> 8)
	data[offset+7] = byte(rotatedNano)

	return data, nil
}

// decodeTenantDEKEntry decodes a tenant DEK entry from bytes.
func decodeTenantDEKEntry(data []byte) (*tenantDEKEntry, error) {
	// Minimum size: version(8) + algLen(2) + tenantLen(2) + keyBytes(4) + dekLen(4) + created(8) + rotated(8) = 36
	if len(data) < 36 {
		return nil, &DataTruncatedError{Message: "data too short"}
	}

	offset := 0

	// Version.
	version := uint64(data[offset])<<56 |
		uint64(data[offset+1])<<48 |
		uint64(data[offset+2])<<40 |
		uint64(data[offset+3])<<32 |
		uint64(data[offset+4])<<24 |
		uint64(data[offset+5])<<16 |
		uint64(data[offset+6])<<8 |
		uint64(data[offset+7])
	offset += 8

	// Algorithm.
	algLen := int(data[offset])<<8 | int(data[offset+1])
	offset += 2
	if offset+algLen > len(data) {
		return nil, &DataTruncatedError{Field: "algorithm", Message: "length exceeds data"}
	}
	algorithm := string(data[offset : offset+algLen])
	offset += algLen

	// Tenant ID.
	if offset+2 > len(data) {
		return nil, &DataTruncatedError{Field: "tenant ID", Message: "length exceeds data"}
	}
	tenantLen := int(data[offset])<<8 | int(data[offset+1])
	offset += 2
	if offset+tenantLen > len(data) {
		return nil, &DataTruncatedError{Field: "tenant ID", Message: "length exceeds data"}
	}
	tenantID := string(data[offset : offset+tenantLen])
	offset += tenantLen

	// Key bytes.
	if offset+4 > len(data) {
		return nil, &DataTruncatedError{Field: "key bytes", Message: "length exceeds data"}
	}
	keyBytes := int(data[offset])<<24 |
		int(data[offset+1])<<16 |
		int(data[offset+2])<<8 |
		int(data[offset+3])
	offset += 4

	// Wrapped DEK.
	if offset+4 > len(data) {
		return nil, &DataTruncatedError{Field: "wrapped DEK", Message: "length exceeds data"}
	}
	dekLen := int(data[offset])<<24 |
		int(data[offset+1])<<16 |
		int(data[offset+2])<<8 |
		int(data[offset+3])
	offset += 4
	if offset+dekLen > len(data) {
		return nil, &DataTruncatedError{Field: "wrapped DEK", Message: "length exceeds data"}
	}
	wrappedDEK := make([]byte, dekLen)
	copy(wrappedDEK, data[offset:offset+dekLen])
	offset += dekLen

	// Created at.
	if offset+8 > len(data) {
		return nil, &DataTruncatedError{Field: "created_at", Message: "length exceeds data"}
	}
	createdNano := int64(data[offset])<<56 |
		int64(data[offset+1])<<48 |
		int64(data[offset+2])<<40 |
		int64(data[offset+3])<<32 |
		int64(data[offset+4])<<24 |
		int64(data[offset+5])<<16 |
		int64(data[offset+6])<<8 |
		int64(data[offset+7])
	offset += 8

	// Rotated at.
	if offset+8 > len(data) {
		return nil, &DataTruncatedError{Field: "rotated_at", Message: "length exceeds data"}
	}
	rotatedNano := int64(data[offset])<<56 |
		int64(data[offset+1])<<48 |
		int64(data[offset+2])<<40 |
		int64(data[offset+3])<<32 |
		int64(data[offset+4])<<24 |
		int64(data[offset+5])<<16 |
		int64(data[offset+6])<<8 |
		int64(data[offset+7])

	return &tenantDEKEntry{
		TenantID:   tenantID,
		WrappedDEK: wrappedDEK,
		Version:    version,
		Algorithm:  algorithm,
		KeyBytes:   keyBytes,
		CreatedAt:  time.Unix(0, createdNano),
		RotatedAt:  time.Unix(0, rotatedNano),
	}, nil
}
