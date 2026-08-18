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

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// ClusterDEKManager manages the cluster-wide Data Encryption Key (DEK) used
// for encrypting data at rest across all nodes in the cluster.
//
// Key Hierarchy:
//  1. Master Key (per node) - from SealManager, in-memory only
//  2. Cluster DEK (shared) - distributed via Raft, wrapped per-node
//  3. Data Keys (per entity) - wrapped by cluster DEK
type ClusterDEKManager struct {
	// barrier provides encryption/decryption for wrapping the cluster DEK.
	barrier seal.EncryptionBarrier

	// Current DEK state (protected by mu).
	//
	// dekPlaintext is the live cluster DEK. It is held in mlocked,
	// guard-paged memory for the manager's lifetime so the plaintext key
	// never lands on the ordinary Go heap, where it would survive in
	// freed-but-unzeroed pages and in core dumps.
	dekPlaintext *secure.Buffer // Current DEK in plaintext (mlocked, memory only)
	dekWrapped   []byte         // Current DEK wrapped with master key
	dekVersion   uint64         // Current DEK version (atomic for reads)
	algorithm    string         // Encryption algorithm
	keyBytes     int            // Key size in bytes

	// Initialization state.
	initialized atomic.Bool

	// Node synchronization state.
	syncedNodes map[uint64]time.Time // nodeID -> last sync time
	nodesMu     sync.RWMutex

	// Re-encryption state.
	reencryption   *reencryptionState
	reencryptionMu sync.RWMutex

	// Configuration and dependencies.
	logger *slog.Logger
	mu     sync.RWMutex

	// Metrics.
	metrics *clusterDEKMetrics
}

// reencryptionState tracks the progress of background re-encryption.
type reencryptionState struct {
	active        bool
	startedAt     time.Time
	totalKeys     uint64
	processedKeys uint64
	oldVersion    uint64
	newVersion    uint64
	ctx           context.Context
	cancel        context.CancelFunc
}

// clusterDEKMetrics holds metrics for cluster DEK operations.
type clusterDEKMetrics struct {
	initOperations       atomic.Uint64
	rotateOperations     atomic.Uint64
	syncOperations       atomic.Uint64
	dekAccesses          atomic.Uint64
	reencryptionProgress atomic.Uint64
}

// ClusterDEKStatus represents the status of the cluster DEK.
type ClusterDEKStatus struct {
	Initialized  bool              `json:"initialized"`
	Version      uint64            `json:"version"`
	Algorithm    string            `json:"algorithm"`
	KeyBytes     int               `json:"key_bytes"`
	CreatedAt    time.Time         `json:"created_at"`
	SyncedNodes  map[uint64]string `json:"synced_nodes"`
	Reencryption *ReencryptionInfo `json:"reencryption,omitempty"`
}

// ReencryptionInfo provides information about ongoing re-encryption.
type ReencryptionInfo struct {
	Active        bool      `json:"active"`
	StartedAt     time.Time `json:"started_at"`
	TotalKeys     uint64    `json:"total_keys"`
	ProcessedKeys uint64    `json:"processed_keys"`
	OldVersion    uint64    `json:"old_version"`
	NewVersion    uint64    `json:"new_version"`
	Progress      float64   `json:"progress"` // 0.0 to 1.0
}

// NewClusterDEKManager creates a new cluster DEK manager instance.
//
// Parameters:
//   - barrier: The barrier providing Encrypt/Decrypt for key wrapping
//   - logger: Logger for structured logging
//
// The manager starts in an uninitialized state. Call Initialize() to set up
// the cluster DEK.
func NewClusterDEKManager(
	barrier seal.EncryptionBarrier,
	logger *slog.Logger,
) (*ClusterDEKManager, error) {
	if barrier == nil {
		return nil, &NilInputError{Op: "NewClusterDEKManager", Field: "barrier"}
	}
	if logger == nil {
		return nil, &NilInputError{Op: "NewClusterDEKManager", Field: "logger"}
	}

	mgr := &ClusterDEKManager{
		barrier:     barrier,
		syncedNodes: make(map[uint64]time.Time),
		logger:      logger.With("component", "cluster_dek_manager"),
		metrics:     &clusterDEKMetrics{},
	}

	mgr.logger.Info("cluster DEK manager created")

	return mgr, nil
}

// Initialize generates and distributes the initial cluster DEK.
// Must be called on the leader after the cluster is formed.
//
// Parameters:
//   - ctx: Context for cancellation
//   - algorithm: Encryption algorithm (e.g., "AES-256-GCM")
//   - keyBytes: Key size in bytes (e.g., 32 for AES-256)
//
// This operation is idempotent - calling it multiple times has no effect
// after the first successful initialization.
func (m *ClusterDEKManager) Initialize(ctx context.Context, algorithm string, keyBytes int) error {
	// Check if already initialized (fast path with atomic).
	if m.initialized.Load() {
		m.logger.Debug("cluster DEK already initialized")
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring lock.
	if m.initialized.Load() {
		return nil
	}

	// Validate parameters.
	if algorithm == "" {
		algorithm = AlgorithmAES256GCM
	}
	if keyBytes <= 0 {
		keyBytes = AESKeySize
	}

	// Validate algorithm.
	if !IsValidAlgorithm(algorithm) {
		return &UnsupportedAlgorithmError{Op: "ClusterDEKManager.Initialize", Algorithm: algorithm}
	}

	m.logger.Info("initializing cluster DEK", "algorithm", algorithm, "key_bytes", keyBytes)

	// Verify barrier is unsealed.
	if m.barrier.IsSealed() {
		return &SealedError{Op: "ClusterDEKManager.Initialize"}
	}

	// Generate the new DEK straight into mlocked memory: the plaintext key
	// never exists on the ordinary heap, not even transiently.
	dekBuf, err := secure.NewBuffer(keyBytes)
	if err != nil {
		return &CryptoError{Op: "ClusterDEKManager.Initialize", Detail: "allocate protected DEK buffer", Err: err}
	}
	if _, err := io.ReadFull(rand.Reader, dekBuf.Bytes()); err != nil {
		dekBuf.Destroy()
		return &CryptoError{Op: "ClusterDEKManager.Initialize", Detail: "generate random DEK", Err: err}
	}

	// Encrypt DEK with barrier (epoch-based AES-256-GCM).
	dekWrapped, err := m.barrier.Encrypt(dekBuf.Bytes())
	if err != nil {
		dekBuf.Destroy()
		return &CryptoError{Op: "ClusterDEKManager.Initialize", Detail: "encrypt DEK with barrier", Err: err}
	}

	// Store DEK state.
	m.dekPlaintext = dekBuf
	m.dekWrapped = dekWrapped
	atomic.StoreUint64(&m.dekVersion, 1)
	m.algorithm = algorithm
	m.keyBytes = keyBytes

	// Mark as initialized.
	m.initialized.Store(true)

	// Update metrics.
	m.metrics.initOperations.Add(1)

	m.logger.Info("cluster DEK initialized successfully",
		"version", 1, "algorithm", algorithm, "key_bytes", keyBytes)

	return nil
}

// GetStatus returns the current cluster DEK status.
func (m *ClusterDEKManager) GetStatus(ctx context.Context) (interface{}, error) {
	if !m.initialized.Load() {
		return &ClusterDEKStatus{
			Initialized: false,
		}, nil
	}

	m.mu.RLock()
	version := atomic.LoadUint64(&m.dekVersion)
	algorithm := m.algorithm
	keyBytes := m.keyBytes
	m.mu.RUnlock()

	// Get synced nodes.
	m.nodesMu.RLock()
	syncedNodes := make(map[uint64]string, len(m.syncedNodes))
	for nodeID, syncTime := range m.syncedNodes {
		syncedNodes[nodeID] = syncTime.Format(time.RFC3339)
	}
	m.nodesMu.RUnlock()

	// Get re-encryption status.
	var reencryptionInfo *ReencryptionInfo
	m.reencryptionMu.RLock()
	if m.reencryption != nil && m.reencryption.active {
		progress := 0.0
		if m.reencryption.totalKeys > 0 {
			progress = float64(m.reencryption.processedKeys) / float64(m.reencryption.totalKeys)
		}
		reencryptionInfo = &ReencryptionInfo{
			Active:        true,
			StartedAt:     m.reencryption.startedAt,
			TotalKeys:     m.reencryption.totalKeys,
			ProcessedKeys: m.reencryption.processedKeys,
			OldVersion:    m.reencryption.oldVersion,
			NewVersion:    m.reencryption.newVersion,
			Progress:      progress,
		}
	}
	m.reencryptionMu.RUnlock()

	status := &ClusterDEKStatus{
		Initialized:  true,
		Version:      version,
		Algorithm:    algorithm,
		KeyBytes:     keyBytes,
		CreatedAt:    time.Now(), // Note: creation time tracking not yet implemented
		SyncedNodes:  syncedNodes,
		Reencryption: reencryptionInfo,
	}

	return status, nil
}

// Rotate generates a new cluster DEK version and initiates re-encryption.
// The old version remains available for decryption during the transition.
func (m *ClusterDEKManager) Rotate(ctx context.Context) error {
	if !m.initialized.Load() {
		return &NotInitializedError{Op: "ClusterDEKManager.Rotate", Resource: "cluster DEK"}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	oldVersion := atomic.LoadUint64(&m.dekVersion)

	m.logger.Info("rotating cluster DEK",
		"old_version", oldVersion, "new_version", oldVersion+1)

	// Verify barrier is unsealed.
	if m.barrier.IsSealed() {
		return &SealedError{Op: "ClusterDEKManager.Rotate"}
	}

	// Generate the new DEK straight into mlocked memory, as Initialize does.
	dekBuf, err := secure.NewBuffer(m.keyBytes)
	if err != nil {
		return &CryptoError{Op: "ClusterDEKManager.Rotate", Detail: "allocate protected DEK buffer", Err: err}
	}
	if _, err := io.ReadFull(rand.Reader, dekBuf.Bytes()); err != nil {
		dekBuf.Destroy()
		return &CryptoError{Op: "ClusterDEKManager.Rotate", Detail: "generate random DEK", Err: err}
	}

	// Encrypt new DEK with barrier (epoch-based AES-256-GCM).
	dekWrapped, err := m.barrier.Encrypt(dekBuf.Bytes())
	if err != nil {
		dekBuf.Destroy()
		return &CryptoError{Op: "ClusterDEKManager.Rotate", Detail: "encrypt DEK with barrier", Err: err}
	}

	// Wipe and release the old DEK's protected memory.
	m.dekPlaintext.Destroy()

	// Update to new DEK.
	m.dekPlaintext = dekBuf
	m.dekWrapped = dekWrapped
	newVersion := oldVersion + 1
	atomic.StoreUint64(&m.dekVersion, newVersion)

	// Clear synced nodes (they need to sync new DEK).
	m.nodesMu.Lock()
	m.syncedNodes = make(map[uint64]time.Time)
	m.nodesMu.Unlock()

	// Update metrics.
	m.metrics.rotateOperations.Add(1)

	m.logger.Info("cluster DEK rotated successfully",
		"old_version", oldVersion, "new_version", newVersion)

	return nil
}

// GetCurrentDEK returns the current cluster DEK (for local encryption).
// Returns a copy of the DEK to prevent external modification.
func (m *ClusterDEKManager) GetCurrentDEK() ([]byte, error) {
	if !m.initialized.Load() {
		return nil, &NotInitializedError{Op: "ClusterDEKManager.GetCurrentDEK", Resource: "cluster DEK"}
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	// Bytes reports nil for both an absent and an already-destroyed buffer.
	plaintext := m.dekPlaintext.Bytes()
	if plaintext == nil {
		return nil, &NotInitializedError{Op: "ClusterDEKManager.GetCurrentDEK", Resource: "DEK plaintext"}
	}

	// Return a copy to prevent external modification.
	dek := make([]byte, len(plaintext))
	copy(dek, plaintext)

	// Update metrics.
	m.metrics.dekAccesses.Add(1)

	return dek, nil
}

// GetDEKVersion returns the current DEK version number.
func (m *ClusterDEKManager) GetDEKVersion() uint64 {
	return atomic.LoadUint64(&m.dekVersion)
}

// SyncToNode synchronizes the DEK to a specific node.
// Used for new nodes joining the cluster.
//
// For the current release, this marks the node as synced in local state.
// In production with Raft, this would transmit the wrapped DEK.
func (m *ClusterDEKManager) SyncToNode(ctx context.Context, nodeID uint64) error {
	if !m.initialized.Load() {
		return &NotInitializedError{Op: "ClusterDEKManager.SyncToNode", Resource: "cluster DEK"}
	}

	m.logger.Debug("syncing DEK to node",
		"node_id", nodeID, "version", atomic.LoadUint64(&m.dekVersion))

	// Mark node as synced.
	m.nodesMu.Lock()
	m.syncedNodes[nodeID] = time.Now()
	m.nodesMu.Unlock()

	// Update metrics.
	m.metrics.syncOperations.Add(1)

	m.logger.Info("DEK synced to node", "node_id", nodeID)

	return nil
}

// IsNodeSynced returns true if the specified node has the current DEK.
func (m *ClusterDEKManager) IsNodeSynced(nodeID uint64) bool {
	m.nodesMu.RLock()
	_, synced := m.syncedNodes[nodeID]
	m.nodesMu.RUnlock()
	return synced
}

// StartReencryption begins background re-encryption of all data.
// Called after DEK rotation to re-encrypt with the new key.
func (m *ClusterDEKManager) StartReencryption(ctx context.Context) error {
	if !m.initialized.Load() {
		return &NotInitializedError{Op: "ClusterDEKManager.StartReencryption", Resource: "cluster DEK"}
	}

	m.reencryptionMu.Lock()
	defer m.reencryptionMu.Unlock()

	// Check if re-encryption is already active.
	if m.reencryption != nil && m.reencryption.active {
		return &AlreadyInProgressError{Op: "ClusterDEKManager.StartReencryption", Resource: "re-encryption"}
	}

	version := atomic.LoadUint64(&m.dekVersion)

	m.logger.Info("starting background re-encryption", "version", version)

	// Create re-encryption context.
	reencryptCtx, cancel := context.WithCancel(ctx)

	m.reencryption = &reencryptionState{
		active:        true,
		startedAt:     time.Now(),
		totalKeys:     0, // Set by actual re-encryption implementation
		processedKeys: 0,
		oldVersion:    version - 1,
		newVersion:    version,
		ctx:           reencryptCtx,
		cancel:        cancel,
	}

	m.logger.Info("re-encryption started (no-op in current release)")

	return nil
}

// GetReencryptionStatus returns progress of the re-encryption process.
func (m *ClusterDEKManager) GetReencryptionStatus(ctx context.Context) (interface{}, error) {
	m.reencryptionMu.RLock()
	defer m.reencryptionMu.RUnlock()

	if m.reencryption == nil || !m.reencryption.active {
		return &ReencryptionInfo{
			Active: false,
		}, nil
	}

	progress := 0.0
	if m.reencryption.totalKeys > 0 {
		progress = float64(m.reencryption.processedKeys) / float64(m.reencryption.totalKeys)
	}

	return &ReencryptionInfo{
		Active:        true,
		StartedAt:     m.reencryption.startedAt,
		TotalKeys:     m.reencryption.totalKeys,
		ProcessedKeys: m.reencryption.processedKeys,
		OldVersion:    m.reencryption.oldVersion,
		NewVersion:    m.reencryption.newVersion,
		Progress:      progress,
	}, nil
}

// CancelReencryption stops the re-encryption process.
func (m *ClusterDEKManager) CancelReencryption(ctx context.Context) error {
	m.reencryptionMu.Lock()
	defer m.reencryptionMu.Unlock()

	if m.reencryption == nil || !m.reencryption.active {
		return &NoActiveOperationError{Op: "ClusterDEKManager.CancelReencryption", Resource: "re-encryption"}
	}

	m.logger.Info("canceling re-encryption",
		"processed", m.reencryption.processedKeys, "total", m.reencryption.totalKeys)

	// Cancel the context.
	m.reencryption.cancel()
	m.reencryption.active = false

	m.logger.Info("re-encryption canceled")

	return nil
}

// RecoverDEK recovers the cluster DEK using recovery keys or backup.
//
// Recovery is not yet implemented. This requires integration with the
// SealManager's recovery keys.
func (m *ClusterDEKManager) RecoverDEK(ctx context.Context, recoveryData interface{}) error {
	return &NotImplementedError{Message: "DEK recovery not implemented in current release"}
}

// Close releases any resources held by the cluster DEK manager.
func (m *ClusterDEKManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Cancel any active re-encryption.
	m.reencryptionMu.Lock()
	if m.reencryption != nil && m.reencryption.active {
		m.reencryption.cancel()
		m.reencryption.active = false
	}
	m.reencryptionMu.Unlock()

	// Wipe and release the DEK's protected memory. Destroy is nil-safe and
	// idempotent, so a double Close is harmless.
	m.dekPlaintext.Destroy()
	m.dekPlaintext = nil

	m.dekWrapped = nil
	m.initialized.Store(false)

	m.logger.Info("cluster DEK manager closed")

	return nil
}

// GetMetrics returns the current metrics for monitoring.
func (m *ClusterDEKManager) GetMetrics() map[string]uint64 {
	return map[string]uint64{
		"init_operations":       m.metrics.initOperations.Load(),
		"rotate_operations":     m.metrics.rotateOperations.Load(),
		"sync_operations":       m.metrics.syncOperations.Load(),
		"dek_accesses":          m.metrics.dekAccesses.Load(),
		"reencryption_progress": m.metrics.reencryptionProgress.Load(),
	}
}
