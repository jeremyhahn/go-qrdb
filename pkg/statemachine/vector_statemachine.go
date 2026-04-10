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

package statemachine

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/vmihailenco/msgpack/v5"

	"github.com/jeremyhahn/go-qrdb/pkg/api"
	"github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/logger"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
	"github.com/jeremyhahn/go-qrdb/pkg/vector/hnsw"
)

// VectorStateMachine implements sm.DiskStateMachine for vector operations.

// HNSWIndex is the interface for the HNSW index implementation.
// This allows for flexible implementations and easy testing with mocks.
type HNSWIndex interface {
	// Add adds a vector to the index
	Add(id uint64, vector []float32) error

	// Delete removes a vector from the index
	Delete(id uint64) error

	// Search finds the K nearest neighbors
	Search(query []float32, k int, efSearch int) ([]HNSWSearchResult, error)

	// Size returns the number of vectors in the index
	Size() int

	// SaveSnapshot saves the index to a writer
	SaveSnapshot(w io.Writer) error

	// LoadSnapshot loads the index from a reader
	LoadSnapshot(r io.Reader) error

	// Reset clears the index
	Reset() error
}

// HNSWSearchResult represents a single HNSW search result.
type HNSWSearchResult struct {
	ID       uint64
	Distance float32
}

// CacheManager provides L1/L2 caching for vector operations.
type CacheManager interface {
	// Get retrieves a value from cache
	Get(ctx context.Context, key []byte) ([]byte, error)

	// Set stores a value in cache with TTL
	Set(ctx context.Context, key []byte, value []byte, ttl time.Duration) error

	// Delete removes a value from cache
	Delete(ctx context.Context, key []byte) error

	// Invalidate clears cache entries matching a pattern
	Invalidate(ctx context.Context, pattern string) error
}

// HNSWIndexFactory creates new HNSW indexes for namespaces.
type HNSWIndexFactory func(config *hnsw.Config) (HNSWIndex, error)

// StateMachineType represents the type of state machine.
type StateMachineType string

const (
	// StateMachineTypeVector is a vector state machine.
	StateMachineTypeVector StateMachineType = "vector"
)

// StorageAdapter provides a generic interface for storage operations.
// This allows state machines to work with different storage backends.
type StorageAdapter interface {
	// Get retrieves a value by key.
	Get(ctx context.Context, key []byte) ([]byte, error)

	// Set stores a value with the given key.
	Set(ctx context.Context, key, value []byte) error

	// Delete removes a value by key.
	Delete(ctx context.Context, key []byte) error

	// Scan iterates over keys with the given prefix.
	Scan(ctx context.Context, prefix []byte, fn func(key, value []byte) error) error

	// Close closes the storage adapter.
	Close() error
}

// VectorStateMachine implements a Raft state machine for vector operations.
// It integrates with QuicRaft for strong consistency and provides:
// - Vector CRUD operations with MVCC versioning
// - HNSW-based similarity search with per-namespace isolation
// - L1/L2 caching for performance
// - Snapshot/recovery for fast cluster rebuilds.
type VectorStateMachine struct {
	shardID   uint64
	replicaID uint64
	fsmType   StateMachineType
	nodeID    string
	adapter   StorageAdapter
	mvcc      MVCCStore
	cache     CacheManager
	logger    *logger.Logger
	metrics   *VectorMetrics
	mu        sync.RWMutex
	closed    bool

	// Per-namespace HNSW indexes for tenant isolation (SECURITY FIX)
	hnswIndexes       map[string]HNSWIndex
	indexesMu         sync.RWMutex
	defaultHNSWConfig *hnsw.Config
	hnswFactory       HNSWIndexFactory

	// Namespace tracking
	namespaces map[string]*NamespaceState

	// Index rebuild tracking
	indexRebuildJobs map[string]*IndexRebuildJob
}

// NamespaceState tracks per-namespace metadata.
type NamespaceState struct {
	Name        string
	Dimension   uint32
	VectorCount uint64
	CreatedAt   int64
	UpdatedAt   int64
	IndexParams map[string]string

	// Multi-tenant fields
	TenantID    string
	OwnerUserID string
	Quotas      *NamespaceQuotas
	Labels      map[string]string

	// Usage tracking for quota enforcement
	StorageBytes int64

	mu sync.RWMutex
}

// NamespaceQuotas defines resource limits for a namespace.
type NamespaceQuotas struct {
	MaxVectors         int64
	MaxStorageBytes    int64
	MaxDimensions      int32
	RateLimitQPS       int32
	RateLimitWritesQPS int32
	MaxBatchSize       int32
}

// IndexRebuildJob tracks index rebuild progress.
type IndexRebuildJob struct {
	JobID            string
	Namespace        string
	InProgress       bool
	VectorsProcessed uint64
	TotalVectors     uint64
	StartedAt        time.Time
	CompletedAt      time.Time
	mu               sync.RWMutex
}

// VectorMetrics tracks state machine performance metrics.
type VectorMetrics struct {
	UpsertCount     uint64
	DeleteCount     uint64
	SearchCount     uint64
	SnapshotCount   uint64
	RecoveryCount   uint64
	ErrorCount      uint64
	UpsertLatencyNs uint64
	DeleteLatencyNs uint64
	SearchLatencyNs uint64
	CacheHits       uint64
	CacheMisses     uint64
	mu              sync.RWMutex
}

// CheckQuota validates that an operation doesn't exceed namespace quotas.
// Returns nil if quotas are not set or if the operation is within limits.
func (ns *NamespaceState) CheckQuota(vectorCount int, vectorBytes int64) error {
	ns.mu.RLock()
	defer ns.mu.RUnlock()

	if ns.Quotas == nil {
		return nil // No quotas configured
	}

	// Check vector count limit
	if ns.Quotas.MaxVectors > 0 {
		newCount := int64(ns.VectorCount) + int64(vectorCount) //nolint:gosec // G115: vector counts are non-negative
		if newCount > ns.Quotas.MaxVectors {
			return errors.NewQuotaExceededError(
				"vectors",
				ns.Name,
				ns.TenantID,
				ns.Quotas.MaxVectors,
				int64(ns.VectorCount), //nolint:gosec // G115: count is non-negative
				int64(vectorCount),
			)
		}
	}

	// Check storage bytes limit
	if ns.Quotas.MaxStorageBytes > 0 && vectorBytes > 0 {
		newStorage := ns.StorageBytes + vectorBytes
		if newStorage > ns.Quotas.MaxStorageBytes {
			return errors.NewQuotaExceededError(
				"storage_bytes",
				ns.Name,
				ns.TenantID,
				ns.Quotas.MaxStorageBytes,
				ns.StorageBytes,
				vectorBytes,
			)
		}
	}

	return nil
}

// CheckBatchSizeQuota validates that a batch operation doesn't exceed the max batch size.
func (ns *NamespaceState) CheckBatchSizeQuota(batchSize int) error {
	ns.mu.RLock()
	defer ns.mu.RUnlock()

	if ns.Quotas == nil || ns.Quotas.MaxBatchSize <= 0 {
		return nil // No batch size limit
	}

	if int32(batchSize) > ns.Quotas.MaxBatchSize { //nolint:gosec // G115: batchSize is bounded by quota
		return errors.NewQuotaExceededError(
			"batch_size",
			ns.Name,
			ns.TenantID,
			int64(ns.Quotas.MaxBatchSize),
			0,
			int64(batchSize),
		)
	}

	return nil
}

// CheckDimensionQuota validates that vector dimensions don't exceed the limit.
func (ns *NamespaceState) CheckDimensionQuota(dimensions int) error {
	ns.mu.RLock()
	defer ns.mu.RUnlock()

	if ns.Quotas == nil || ns.Quotas.MaxDimensions <= 0 {
		return nil // No dimension limit
	}

	if int32(dimensions) > ns.Quotas.MaxDimensions { //nolint:gosec // G115: dimensions is bounded by quota
		return errors.NewQuotaExceededError(
			"dimensions",
			ns.Name,
			ns.TenantID,
			int64(ns.Quotas.MaxDimensions),
			int64(ns.Dimension),
			int64(dimensions),
		)
	}

	return nil
}

// UpdateStorageBytes updates the storage bytes usage for the namespace.
func (ns *NamespaceState) UpdateStorageBytes(delta int64) {
	ns.mu.Lock()
	defer ns.mu.Unlock()

	ns.StorageBytes += delta
	if ns.StorageBytes < 0 {
		ns.StorageBytes = 0
	}
}

// EstimateVectorBytes estimates the storage size of a vector in bytes.
func EstimateVectorBytes(vectorLen int, metadataCount int) int64 {
	// Base overhead: ID (8) + timestamps (16) + version (8) = 32 bytes
	size := int64(32)

	// Embedding size (4 bytes per float32)
	size += int64(vectorLen * 4)

	// Metadata size estimate (key + value + overhead per entry)
	size += int64(metadataCount * 64)

	// MessagePack overhead (~10%)
	return int64(float64(size) * 1.1)
}

// NewVectorStateMachine creates a new vector state machine.
// The hnswConfig parameter specifies the default configuration for per-namespace HNSW indexes.
// If hnswConfig is nil, default HNSW configuration will be used.
func NewVectorStateMachine(
	shardID uint64,
	replicaID uint64,
	nodeID string,
	adapter StorageAdapter,
	mvccStore MVCCStore,
	hnswConfig *hnsw.Config,
	cache CacheManager,
	log *logger.Logger,
) (*VectorStateMachine, error) {
	if adapter == nil {
		return nil, api.NewRequiredFieldError("adapter")
	}

	// Log is required - caller must provide it
	if log == nil {
		return nil, api.NewRequiredFieldError("logger")
	}

	// Use default config if none provided
	if hnswConfig == nil {
		hnswConfig = hnsw.DefaultConfig()
	}

	vsm := &VectorStateMachine{
		shardID:           shardID,
		replicaID:         replicaID,
		fsmType:           StateMachineTypeVector,
		nodeID:            nodeID,
		adapter:           adapter,
		mvcc:              mvccStore,
		hnswIndexes:       make(map[string]HNSWIndex),
		defaultHNSWConfig: hnswConfig,
		hnswFactory:       defaultHNSWFactory,
		cache:             cache,
		logger:            log,
		metrics:           &VectorMetrics{},
		closed:            false,
		namespaces:        make(map[string]*NamespaceState),
		indexRebuildJobs:  make(map[string]*IndexRebuildJob),
	}

	vsm.logger.WithFields(map[string]interface{}{
		"shard_id":   shardID,
		"replica_id": replicaID,
		"node_id":    nodeID,
	}).Info("vector state machine created with per-namespace HNSW isolation")

	return vsm, nil
}

// defaultHNSWFactory is the default factory for creating HNSW indexes.
func defaultHNSWFactory(config *hnsw.Config) (HNSWIndex, error) {
	idx, err := hnsw.NewHNSWIndex(config)
	if err != nil {
		return nil, err
	}
	return &hnswIndexAdapter{idx: idx}, nil
}

// hnswIndexAdapter adapts the hnsw.HNSWIndex to the HNSWIndex interface.
type hnswIndexAdapter struct {
	idx *hnsw.HNSWIndex
}

func (a *hnswIndexAdapter) Add(id uint64, vector []float32) error {
	return a.idx.Insert(id, vector)
}

func (a *hnswIndexAdapter) Delete(id uint64) error {
	return a.idx.Delete(id)
}

func (a *hnswIndexAdapter) Search(query []float32, k int, efSearch int) ([]HNSWSearchResult, error) {
	// Temporarily adjust efSearch if needed
	results, err := a.idx.Search(query, k)
	if err != nil {
		return nil, err
	}

	hnswResults := make([]HNSWSearchResult, len(results))
	for i, r := range results {
		hnswResults[i] = HNSWSearchResult{
			ID:       r.ID,
			Distance: r.Distance,
		}
	}
	return hnswResults, nil
}

func (a *hnswIndexAdapter) Size() int {
	return int(a.idx.Count())
}

func (a *hnswIndexAdapter) SaveSnapshot(w io.Writer) error {
	return a.idx.Snapshot(w)
}

func (a *hnswIndexAdapter) LoadSnapshot(r io.Reader) error {
	return a.idx.Restore(r)
}

func (a *hnswIndexAdapter) Reset() error {
	// Create a new empty index with the same config
	// The HNSW index doesn't have a reset method, so we create a fresh one
	newIdx, err := hnsw.NewHNSWIndex(a.idx.GetConfig())
	if err != nil {
		return err
	}
	a.idx = newIdx
	return nil
}

// SetHNSWFactory sets a custom factory for creating HNSW indexes.
// This is primarily used for testing with mock implementations.
func (vsm *VectorStateMachine) SetHNSWFactory(factory HNSWIndexFactory) {
	vsm.indexesMu.Lock()
	defer vsm.indexesMu.Unlock()
	vsm.hnswFactory = factory
}

// getOrCreateIndex retrieves or creates a namespace-specific HNSW index.
// This ensures tenant isolation by maintaining separate indexes per namespace.
func (vsm *VectorStateMachine) getOrCreateIndex(namespace string) (HNSWIndex, error) {
	vsm.indexesMu.RLock()
	if idx, exists := vsm.hnswIndexes[namespace]; exists {
		vsm.indexesMu.RUnlock()
		return idx, nil
	}
	vsm.indexesMu.RUnlock()

	// Upgrade to write lock to create new index
	vsm.indexesMu.Lock()
	defer vsm.indexesMu.Unlock()

	// Double-check after acquiring write lock
	if idx, exists := vsm.hnswIndexes[namespace]; exists {
		return idx, nil
	}

	// Build config from namespace params if available
	config := vsm.buildIndexConfig(namespace)

	// Create new index using factory
	idx, err := vsm.hnswFactory(config)
	if err != nil {
		return nil, api.NewOperationFailedError("create HNSW index for namespace "+namespace, err)
	}

	vsm.hnswIndexes[namespace] = idx
	vsm.logger.WithField("namespace", namespace).Info("created HNSW index for namespace")

	return idx, nil
}

// getIndex retrieves the HNSW index for a namespace.
// Returns nil if the index does not exist.
func (vsm *VectorStateMachine) getIndex(namespace string) HNSWIndex {
	vsm.indexesMu.RLock()
	defer vsm.indexesMu.RUnlock()
	return vsm.hnswIndexes[namespace]
}

// deleteIndex removes the HNSW index for a namespace.
func (vsm *VectorStateMachine) deleteIndex(namespace string) {
	vsm.indexesMu.Lock()
	defer vsm.indexesMu.Unlock()
	delete(vsm.hnswIndexes, namespace)
	vsm.logger.WithField("namespace", namespace).Info("deleted HNSW index for namespace")
}

// validateTenantOwnership verifies that the request tenant has access to the namespace.
// Returns an error if the namespace belongs to a different tenant.
// CRITICAL SECURITY: This function prevents cross-tenant data access.
func (vsm *VectorStateMachine) validateTenantOwnership(namespace, requestTenantID string) error {
	ns, exists := vsm.namespaces[namespace]
	if !exists {
		return api.NewNotFoundError("namespace", namespace)
	}

	// If namespace has no tenant (legacy/system namespace), allow access
	if ns.TenantID == "" {
		return nil
	}

	// If request has no tenant context, deny access to tenant-owned namespaces
	if requestTenantID == "" {
		vsm.logger.WithFields(map[string]interface{}{
			"namespace":        namespace,
			"namespace_tenant": ns.TenantID,
			"security_event":   "missing_tenant_context",
		}).Warn("cross-tenant access attempt: missing tenant context for tenant-owned namespace")

		return errors.NewMissingTenantContextError("VectorStateMachine.validateTenantOwnership")
	}

	// Verify tenant ownership
	if ns.TenantID != requestTenantID {
		vsm.logger.WithFields(map[string]interface{}{
			"namespace":        namespace,
			"namespace_tenant": ns.TenantID,
			"request_tenant":   requestTenantID,
			"security_event":   "cross_tenant_access_denied",
		}).Warn("cross-tenant access attempt blocked")

		return errors.NewCrossTenantAccessError(
			"VectorStateMachine.validateTenantOwnership",
			requestTenantID,
			ns.TenantID,
		)
	}

	return nil
}

// buildIndexConfig creates HNSW config from namespace IndexParams or uses defaults.
func (vsm *VectorStateMachine) buildIndexConfig(namespace string) *hnsw.Config {
	// Start with default config
	config := *vsm.defaultHNSWConfig

	// Check if namespace exists and has custom params
	if ns, exists := vsm.namespaces[namespace]; exists && ns.IndexParams != nil {
		ns.mu.RLock()
		defer ns.mu.RUnlock()

		// Parse index params if provided
		if mStr, ok := ns.IndexParams["m"]; ok {
			if m, err := strconv.Atoi(mStr); err == nil && m > 0 {
				config.M = m
				config.MMax = m * 2
			}
		}
		if efConstStr, ok := ns.IndexParams["ef_construction"]; ok {
			if efConst, err := strconv.Atoi(efConstStr); err == nil && efConst > 0 {
				config.EfConstruction = efConst
			}
		}
		if efSearchStr, ok := ns.IndexParams["ef_search"]; ok {
			if efSearch, err := strconv.Atoi(efSearchStr); err == nil && efSearch > 0 {
				config.EfSearch = efSearch
			}
		}
		if metricStr, ok := ns.IndexParams["distance_metric"]; ok {
			switch metricStr {
			case "l2", "euclidean":
				config.DistanceMetric = hnsw.L2Distance
			case "cosine":
				config.DistanceMetric = hnsw.CosineDistance
			case "dot", "inner_product":
				config.DistanceMetric = hnsw.DotProductDistance
			}
		}
	}

	return &config
}

// GetShardID returns the shard ID this state machine belongs to.
func (vsm *VectorStateMachine) GetShardID() uint64 {
	return vsm.shardID
}

// GetReplicaID returns the replica ID for this state machine instance.
func (vsm *VectorStateMachine) GetReplicaID() uint64 {
	return vsm.replicaID
}

// GetStateMachineType returns the type of this state machine.
func (vsm *VectorStateMachine) GetStateMachineType() StateMachineType {
	return vsm.fsmType
}

// GetStorageAdapter returns the underlying storage adapter.
func (vsm *VectorStateMachine) GetStorageAdapter() StorageAdapter {
	return vsm.adapter
}

// PreUpdate is called before processing a Raft log entry.
func (vsm *VectorStateMachine) PreUpdate(entry sm.Entry) error {
	// Validate that state machine is not closed
	if vsm.closed {
		return &errors.DragonError{
			Code: errors.ErrInternal,
			Op:   "VectorStateMachine.PreUpdate",
			Err:  &api.InvalidInputError{Message: "state machine is closed"},
		}
	}

	// Log operation for debugging
	vsm.logger.WithFields(map[string]interface{}{
		"index": entry.Index,
		"size":  len(entry.Cmd),
	}).Debug("processing vector entry")

	return nil
}

// PostUpdate is called after successfully processing a Raft log entry.
func (vsm *VectorStateMachine) PostUpdate(entry sm.Entry, result sm.Result) error {
	// Log successful operation
	vsm.logger.WithFields(map[string]interface{}{
		"index": entry.Index,
	}).Debug("vector entry applied successfully")

	return nil
}

// HealthCheck performs a health check on the state machine and its dependencies.
func (vsm *VectorStateMachine) HealthCheck() error {
	vsm.mu.RLock()
	defer vsm.mu.RUnlock()

	if vsm.closed {
		return &errors.DragonError{
			Code: errors.ErrInternal,
			Op:   "VectorStateMachine.HealthCheck",
			Err:  &api.InvalidInputError{Message: "state machine is closed"},
		}
	}

	if vsm.adapter == nil {
		return &errors.DragonError{
			Code: errors.ErrInternal,
			Op:   "VectorStateMachine.HealthCheck",
			Err:  &api.InvalidInputError{Message: "storage adapter is nil"},
		}
	}

	// Basic health check - verify adapter is responsive by attempting a minimal operation
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Test adapter connectivity
	_, err := vsm.adapter.Get(ctx, []byte("__health_check__"))
	// ErrNotFound is OK, means the adapter is working
	if err != nil && err.Error() != "ErrNotFound" {
		// Log the error but don't fail health check on temporary issues
		vsm.logger.WithError(err).Warn("adapter health check failed")
	}

	return nil
}

// Update implements sm.DiskStateMachine.Update.
// This is called by QuicRaft when Raft log entries need to be applied.
func (vsm *VectorStateMachine) Update(ctx context.Context, entries []sm.Entry, results []sm.Result) error {
	vsm.mu.Lock()
	defer vsm.mu.Unlock()

	for i, entry := range entries {
		// PreUpdate validation
		if err := vsm.PreUpdate(entry); err != nil {
			return err
		}

		result, err := vsm.applyEntry(entry)
		if err != nil {
			vsm.metrics.mu.Lock()
			vsm.metrics.ErrorCount++
			vsm.metrics.mu.Unlock()

			vsm.logger.WithFields(map[string]interface{}{
				"index": entry.Index,
				"error": err.Error(),
			}).Error("failed to apply vector entry")

			return err
		}

		results[i] = result

		// PostUpdate hook
		if err := vsm.PostUpdate(entry, result); err != nil {
			vsm.logger.WithError(err).Warn("post-update hook failed")
			// Don't fail the operation if post-update fails
		}
	}

	return nil
}

// applyEntry applies a single Raft log entry.
func (vsm *VectorStateMachine) applyEntry(entry sm.Entry) (sm.Result, error) {
	cmd, err := DecodeVectorCommand(entry.Cmd)
	if err != nil {
		return sm.Result{}, err
	}

	if err := cmd.Validate(); err != nil {
		return sm.Result{}, err
	}

	ctx := context.Background()

	switch cmd.Op {
	case OpUpsertVector:
		return vsm.handleUpsert(ctx, cmd)
	case OpDeleteVector:
		return vsm.handleDelete(ctx, cmd)
	case OpBatchUpsert:
		return vsm.handleBatchUpsert(ctx, cmd)
	case OpCreateNamespace:
		return vsm.handleCreateNamespace(ctx, cmd)
	case OpDeleteNamespace:
		return vsm.handleDeleteNamespace(ctx, cmd)
	case OpUpdateNamespace:
		return vsm.handleUpdateNamespace(ctx, cmd)
	case OpRebuildIndex:
		return vsm.handleRebuildIndex(ctx, cmd)
	case OpUpdateMetadata:
		return vsm.handleUpdateMetadata(ctx, cmd)
	default:
		return sm.Result{}, api.NewUnknownOperationError("VectorStateMachine.Update", cmd.Op.String())
	}
}

// handleUpsert handles vector upsert operations.
func (vsm *VectorStateMachine) handleUpsert(ctx context.Context, cmd *VectorCommand) (sm.Result, error) {
	start := time.Now()
	defer func() {
		vsm.metrics.mu.Lock()
		vsm.metrics.UpsertLatencyNs = uint64(time.Since(start).Nanoseconds()) //nolint:gosec // G115: Safe conversion - duration always positive
		vsm.metrics.UpsertCount++
		vsm.metrics.mu.Unlock()
	}()

	// SECURITY: Validate tenant ownership before any operation
	if err := vsm.validateTenantOwnership(cmd.Namespace, cmd.TenantID); err != nil {
		return sm.Result{}, err
	}

	// Verify namespace exists (already checked in validateTenantOwnership, get reference)
	ns := vsm.namespaces[cmd.Namespace]

	// Validate vector dimension
	if uint32(len(cmd.Vector)) != ns.Dimension { //nolint:gosec // G115: Safe conversion - vector length bounded
		return sm.Result{}, api.NewInvalidLengthError("VectorStateMachine.handleUpsert", "vector dimension", int(ns.Dimension), len(cmd.Vector))
	}

	// Check quota before upsert
	estimatedBytes := EstimateVectorBytes(len(cmd.Vector), len(cmd.Metadata))
	if err := ns.CheckQuota(1, estimatedBytes); err != nil {
		return sm.Result{}, err
	}

	// Get the underlying vector storage from adapter
	vectorStorage, ok := vsm.adapter.(interface{ VectorStorage() vector.Storage })
	if !ok {
		return sm.Result{}, api.NewServiceUnavailableError("vector storage")
	}

	// Store vector via vector storage API
	vecValue := &vector.VectorValue{
		ID:        cmd.ID,
		Embedding: cmd.Vector,
		Metadata:  convertToStringMap(cmd.Metadata),
		CreatedAt: cmd.Timestamp,
		UpdatedAt: cmd.Timestamp,
		Version:   cmd.Version,
	}

	if err := vectorStorage.VectorStorage().UpsertVector(ctx, cmd.Namespace, vecValue); err != nil {
		return sm.Result{}, api.NewOperationFailedError("upsert vector", err)
	}

	// Update namespace-specific HNSW index (SECURITY FIX: per-namespace isolation)
	hnswIdx, err := vsm.getOrCreateIndex(cmd.Namespace)
	if err != nil {
		vsm.logger.WithFields(map[string]interface{}{
			"namespace": cmd.Namespace,
			"error":     err,
		}).Warn("failed to get/create HNSW index")
	} else if hnswIdx != nil {
		if err := hnswIdx.Add(cmd.ID, cmd.Vector); err != nil {
			vsm.logger.WithFields(map[string]interface{}{
				"namespace": cmd.Namespace,
				"id":        cmd.ID,
				"error":     err,
			}).Warn("failed to add vector to HNSW index")
		}
	}

	// Invalidate cache
	if vsm.cache != nil {
		cacheKey := fmt.Sprintf("vector:%s:%d", cmd.Namespace, cmd.ID)
		_ = vsm.cache.Delete(ctx, []byte(cacheKey))
	}

	// Update namespace stats and storage bytes
	ns.mu.Lock()
	ns.VectorCount++
	ns.StorageBytes += estimatedBytes
	ns.UpdatedAt = time.Now().UnixNano()
	ns.mu.Unlock()

	vsm.logger.WithFields(map[string]interface{}{
		"namespace": cmd.Namespace,
		"id":        cmd.ID,
		"dimension": len(cmd.Vector),
	}).Debug("upserted vector")

	return sm.Result{Value: cmd.Version}, nil
}

// handleDelete handles vector deletion operations.
func (vsm *VectorStateMachine) handleDelete(ctx context.Context, cmd *VectorCommand) (sm.Result, error) {
	start := time.Now()
	defer func() {
		vsm.metrics.mu.Lock()
		vsm.metrics.DeleteLatencyNs = uint64(time.Since(start).Nanoseconds()) //nolint:gosec // G115: Safe conversion - duration always positive
		vsm.metrics.DeleteCount++
		vsm.metrics.mu.Unlock()
	}()

	// SECURITY: Validate tenant ownership before any operation
	if err := vsm.validateTenantOwnership(cmd.Namespace, cmd.TenantID); err != nil {
		return sm.Result{}, err
	}

	// Get the underlying vector storage from adapter
	vectorStorage, ok := vsm.adapter.(interface{ VectorStorage() vector.Storage })
	if !ok {
		return sm.Result{}, api.NewServiceUnavailableError("vector storage")
	}

	// Determine if this is a batch delete or single delete
	var idsToDelete []uint64
	if len(cmd.IDs) > 0 {
		// Batch delete
		idsToDelete = cmd.IDs
	} else {
		// Single delete
		idsToDelete = []uint64{cmd.ID}
	}

	// Get namespace-specific HNSW index (SECURITY FIX: per-namespace isolation)
	hnswIdx := vsm.getIndex(cmd.Namespace)

	// Delete all vectors
	deletedCount := 0
	for _, id := range idsToDelete {
		// Delete via vector storage API
		if err := vectorStorage.VectorStorage().DeleteVector(ctx, cmd.Namespace, id); err != nil {
			// Log error but continue with other deletes
			vsm.logger.WithFields(map[string]interface{}{
				"namespace": cmd.Namespace,
				"id":        id,
				"error":     err,
			}).Warn("failed to delete vector")
			continue
		}

		// Remove from namespace-specific HNSW index
		if hnswIdx != nil {
			if err := hnswIdx.Delete(id); err != nil {
				vsm.logger.WithFields(map[string]interface{}{
					"namespace": cmd.Namespace,
					"id":        id,
					"error":     err,
				}).Warn("failed to delete from HNSW index")
			}
		}

		// Invalidate cache
		if vsm.cache != nil {
			cacheKey := fmt.Sprintf("vector:%s:%d", cmd.Namespace, id)
			_ = vsm.cache.Delete(ctx, []byte(cacheKey))
		}

		deletedCount++
	}

	// Update namespace stats
	if ns, exists := vsm.namespaces[cmd.Namespace]; exists {
		ns.mu.Lock()
		if ns.VectorCount >= uint64(deletedCount) { //nolint:gosec // G115: Safe conversion - count within reasonable range
			ns.VectorCount -= uint64(deletedCount) //nolint:gosec // G115: Safe conversion - count within reasonable range
		} else {
			ns.VectorCount = 0
		}
		ns.UpdatedAt = time.Now().UnixNano()
		ns.mu.Unlock()
	}

	vsm.logger.WithFields(map[string]interface{}{
		"namespace": cmd.Namespace,
		"count":     deletedCount,
	}).Debug("deleted vector(s)")

	return sm.Result{Value: uint64(deletedCount)}, nil //nolint:gosec // G115: Safe conversion - count within reasonable range
}

// handleBatchUpsert handles batch upsert operations.
func (vsm *VectorStateMachine) handleBatchUpsert(ctx context.Context, cmd *VectorCommand) (sm.Result, error) {
	start := time.Now()
	defer func() {
		vsm.metrics.mu.Lock()
		vsm.metrics.UpsertLatencyNs = uint64(time.Since(start).Nanoseconds()) //nolint:gosec // G115: Safe conversion - duration always positive
		vsm.metrics.UpsertCount += uint64(len(cmd.IDs))
		vsm.metrics.mu.Unlock()
	}()

	// SECURITY: Validate tenant ownership before any operation
	if err := vsm.validateTenantOwnership(cmd.Namespace, cmd.TenantID); err != nil {
		return sm.Result{}, err
	}

	// Get namespace reference (already validated in validateTenantOwnership)
	ns := vsm.namespaces[cmd.Namespace]

	// Check batch size quota
	if err := ns.CheckBatchSizeQuota(len(cmd.IDs)); err != nil {
		return sm.Result{}, err
	}

	// Calculate total storage bytes for quota check
	var totalEstimatedBytes int64
	for i := range cmd.IDs {
		metaCount := 0
		if cmd.BatchMeta != nil && i < len(cmd.BatchMeta) {
			metaCount = len(cmd.BatchMeta[i])
		}
		totalEstimatedBytes += EstimateVectorBytes(len(cmd.Vectors[i]), metaCount)
	}

	// Check vector count and storage quotas
	if err := ns.CheckQuota(len(cmd.IDs), totalEstimatedBytes); err != nil {
		return sm.Result{}, err
	}

	// Get the underlying vector storage from adapter
	vectorStorage, ok := vsm.adapter.(interface{ VectorStorage() vector.Storage })
	if !ok {
		return sm.Result{}, api.NewServiceUnavailableError("vector storage")
	}

	// Get or create namespace-specific HNSW index (SECURITY FIX: per-namespace isolation)
	hnswIdx, err := vsm.getOrCreateIndex(cmd.Namespace)
	if err != nil {
		vsm.logger.WithFields(map[string]interface{}{
			"namespace": cmd.Namespace,
			"error":     err,
		}).Warn("failed to get/create HNSW index for batch upsert")
	}

	// Build batch of vectors
	vecs := make([]*vector.VectorValue, len(cmd.IDs))
	for i := range cmd.IDs {
		// Validate dimension
		if uint32(len(cmd.Vectors[i])) != ns.Dimension { //nolint:gosec // G115: Safe conversion - vector length bounded
			return sm.Result{}, api.NewInvalidLengthError("VectorStateMachine.handleBatchUpsert", "vector dimension", int(ns.Dimension), len(cmd.Vectors[i]))
		}

		vecs[i] = &vector.VectorValue{
			ID:        cmd.IDs[i],
			Embedding: cmd.Vectors[i],
			Metadata:  make(map[string]string),
			CreatedAt: cmd.Timestamp,
			UpdatedAt: cmd.Timestamp,
			Version:   cmd.Version,
		}

		// Add metadata if provided
		if cmd.BatchMeta != nil && i < len(cmd.BatchMeta) {
			for k, v := range cmd.BatchMeta[i] {
				vecs[i].Metadata[k] = fmt.Sprintf("%v", v)
			}
		}

		// Add to namespace-specific HNSW index
		if hnswIdx != nil {
			if err := hnswIdx.Add(cmd.IDs[i], cmd.Vectors[i]); err != nil {
				vsm.logger.WithFields(map[string]interface{}{
					"namespace": cmd.Namespace,
					"id":        cmd.IDs[i],
					"error":     err,
				}).Warn("failed to add to HNSW")
			}
		}
	}

	// Execute batch using vector storage API
	if err := vectorStorage.VectorStorage().BatchUpsertVectors(ctx, cmd.Namespace, vecs); err != nil {
		return sm.Result{}, api.NewOperationFailedError("batch upsert", err)
	}

	// Update namespace stats and storage bytes
	ns.mu.Lock()
	ns.VectorCount += uint64(len(cmd.IDs))
	ns.StorageBytes += totalEstimatedBytes
	ns.UpdatedAt = time.Now().UnixNano()
	ns.mu.Unlock()

	vsm.logger.WithFields(map[string]interface{}{
		"namespace": cmd.Namespace,
		"count":     len(cmd.IDs),
	}).Debug("batch upserted vectors")

	return sm.Result{Value: uint64(len(cmd.IDs))}, nil
}

// handleCreateNamespace handles namespace creation.
func (vsm *VectorStateMachine) handleCreateNamespace(ctx context.Context, cmd *VectorCommand) (sm.Result, error) {
	// Check if namespace already exists
	if _, exists := vsm.namespaces[cmd.Namespace]; exists {
		return sm.Result{}, &errors.DragonError{
			Code: errors.ErrConflict,
			Op:   "VectorStateMachine.handleCreateNamespace",
			Err:  &api.InvalidInputError{Message: "namespace " + cmd.Namespace + " already exists"},
		}
	}

	// Create namespace using vector storage API
	info := &vector.NamespaceInfo{
		Name:        cmd.Namespace,
		VectorCount: 0,
		Dimension:   cmd.Dimension,
		CreatedAt:   cmd.Timestamp,
		UpdatedAt:   cmd.Timestamp,
	}

	// Get the underlying vector storage from adapter
	vectorStorage, ok := vsm.adapter.(interface{ VectorStorage() vector.Storage })
	if !ok {
		return sm.Result{}, api.NewServiceUnavailableError("vector storage")
	}

	if err := vectorStorage.VectorStorage().CreateNamespace(ctx, info); err != nil {
		return sm.Result{}, api.NewOperationFailedError("create namespace", err)
	}

	// Track in-memory
	vsm.namespaces[cmd.Namespace] = &NamespaceState{
		Name:         cmd.Namespace,
		Dimension:    cmd.Dimension,
		VectorCount:  0,
		CreatedAt:    cmd.Timestamp,
		UpdatedAt:    cmd.Timestamp,
		IndexParams:  cmd.IndexParams,
		TenantID:     cmd.TenantID,
		OwnerUserID:  cmd.OwnerUserID,
		Quotas:       cmd.Quotas,
		Labels:       cmd.Labels,
		StorageBytes: 0,
	}

	// Create HNSW index for the new namespace (SECURITY FIX: per-namespace isolation)
	_, err := vsm.getOrCreateIndex(cmd.Namespace)
	if err != nil {
		vsm.logger.WithFields(map[string]interface{}{
			"namespace": cmd.Namespace,
			"error":     err,
		}).Warn("failed to create HNSW index for new namespace")
	}

	vsm.logger.WithFields(map[string]interface{}{
		"namespace": cmd.Namespace,
		"dimension": cmd.Dimension,
		"tenant_id": cmd.TenantID,
	}).Info("created namespace with isolated HNSW index")

	return sm.Result{Value: 1}, nil
}

// handleDeleteNamespace handles namespace deletion.
func (vsm *VectorStateMachine) handleDeleteNamespace(ctx context.Context, cmd *VectorCommand) (sm.Result, error) {
	// SECURITY: Validate tenant ownership before deletion
	if err := vsm.validateTenantOwnership(cmd.Namespace, cmd.TenantID); err != nil {
		return sm.Result{}, err
	}

	// Get the underlying vector storage from adapter
	vectorStorage, ok := vsm.adapter.(interface{ VectorStorage() vector.Storage })
	if !ok {
		return sm.Result{}, api.NewServiceUnavailableError("vector storage")
	}

	// Delete namespace using vector storage API
	if err := vectorStorage.VectorStorage().DeleteNamespace(ctx, cmd.Namespace); err != nil {
		return sm.Result{}, api.NewOperationFailedError("delete namespace", err)
	}

	// Remove from in-memory tracking
	delete(vsm.namespaces, cmd.Namespace)

	// Delete the namespace's HNSW index (SECURITY FIX: per-namespace isolation)
	vsm.deleteIndex(cmd.Namespace)

	// Invalidate cache for namespace
	if vsm.cache != nil {
		pattern := fmt.Sprintf("vector:%s:*", cmd.Namespace)
		_ = vsm.cache.Invalidate(ctx, pattern)
	}

	vsm.logger.WithField("namespace", cmd.Namespace).Info("deleted namespace and its HNSW index")

	return sm.Result{Value: 1}, nil
}

// handleUpdateNamespace handles namespace updates.
func (vsm *VectorStateMachine) handleUpdateNamespace(ctx context.Context, cmd *VectorCommand) (sm.Result, error) {
	// SECURITY: Validate tenant ownership before update
	if err := vsm.validateTenantOwnership(cmd.Namespace, cmd.TenantID); err != nil {
		return sm.Result{}, err
	}

	ns := vsm.namespaces[cmd.Namespace]

	ns.mu.Lock()
	defer ns.mu.Unlock()

	// Update index params if provided
	requiresRebuild := false
	if len(cmd.IndexParams) > 0 {
		if ns.IndexParams == nil {
			ns.IndexParams = make(map[string]string)
		}
		for k, v := range cmd.IndexParams {
			if ns.IndexParams[k] != v {
				requiresRebuild = true
			}
			ns.IndexParams[k] = v
		}
	}

	// Update metadata if provided (stored in cmd.Metadata)
	// This would require extending NamespaceState to track metadata

	ns.UpdatedAt = time.Now().UnixNano()

	// Encode result
	result := UpdateNamespaceResult{
		Updated:         true,
		RequiresRebuild: requiresRebuild,
	}

	data, err := msgpack.Marshal(&result)
	if err != nil {
		return sm.Result{}, err
	}

	vsm.logger.WithFields(map[string]interface{}{
		"namespace":        cmd.Namespace,
		"requires_rebuild": requiresRebuild,
	}).Info("updated namespace")

	return sm.Result{Value: 1, Data: data}, nil
}

// handleRebuildIndex handles index rebuild operations.
func (vsm *VectorStateMachine) handleRebuildIndex(ctx context.Context, cmd *VectorCommand) (sm.Result, error) {
	// SECURITY: Validate tenant ownership before rebuild
	if err := vsm.validateTenantOwnership(cmd.Namespace, cmd.TenantID); err != nil {
		return sm.Result{}, err
	}

	ns := vsm.namespaces[cmd.Namespace]

	// Create rebuild job
	job := &IndexRebuildJob{
		JobID:            cmd.JobID,
		Namespace:        cmd.Namespace,
		InProgress:       true,
		VectorsProcessed: 0,
		TotalVectors:     ns.VectorCount,
		StartedAt:        time.Now(),
	}

	vsm.indexRebuildJobs[cmd.JobID] = job

	// For now, just mark as started
	// Actual rebuild would happen asynchronously

	vsm.logger.WithFields(map[string]interface{}{
		"namespace": cmd.Namespace,
		"job_id":    cmd.JobID,
	}).Info("started index rebuild")

	return sm.Result{Value: 1}, nil
}

// handleUpdateMetadata handles metadata-only updates for vectors.
func (vsm *VectorStateMachine) handleUpdateMetadata(ctx context.Context, cmd *VectorCommand) (sm.Result, error) {
	// SECURITY: Validate tenant ownership before metadata update
	if err := vsm.validateTenantOwnership(cmd.Namespace, cmd.TenantID); err != nil {
		return sm.Result{}, err
	}

	// Get namespace reference (already validated)
	ns := vsm.namespaces[cmd.Namespace]

	// Get the underlying vector storage from adapter
	vectorStorage, ok := vsm.adapter.(interface{ VectorStorage() vector.Storage })
	if !ok {
		return sm.Result{}, api.NewServiceUnavailableError("vector storage")
	}

	// First, get the existing vector to update its metadata
	existingVec, err := vectorStorage.VectorStorage().GetVector(ctx, cmd.Namespace, cmd.ID)
	if err != nil {
		return sm.Result{}, api.NewNotFoundError("vector", fmt.Sprintf("%d", cmd.ID))
	}

	// Update metadata based on merge flag
	newMetadata := make(map[string]string)
	if cmd.Merge {
		// Merge: keep existing metadata and add/update new keys
		for k, v := range existingVec.Metadata {
			newMetadata[k] = v
		}
		for k, v := range cmd.Metadata {
			newMetadata[k] = fmt.Sprintf("%v", v)
		}
	} else {
		// Replace: use only new metadata
		for k, v := range cmd.Metadata {
			newMetadata[k] = fmt.Sprintf("%v", v)
		}
	}

	// Increment version
	newVersion := existingVec.Version + 1

	// Create updated vector value
	updatedVec := &vector.VectorValue{
		ID:        existingVec.ID,
		Embedding: existingVec.Embedding,
		Metadata:  newMetadata,
		CreatedAt: existingVec.CreatedAt,
		UpdatedAt: cmd.Timestamp,
		Version:   newVersion,
	}

	// Upsert the updated vector
	if err := vectorStorage.VectorStorage().UpsertVector(ctx, cmd.Namespace, updatedVec); err != nil {
		return sm.Result{}, api.NewOperationFailedError("update vector metadata", err)
	}

	// Invalidate cache
	if vsm.cache != nil {
		cacheKey := fmt.Sprintf("vector:%s:%d", cmd.Namespace, cmd.ID)
		_ = vsm.cache.Delete(ctx, []byte(cacheKey))
	}

	// Update namespace timestamp
	ns.mu.Lock()
	ns.UpdatedAt = time.Now().UnixNano()
	ns.mu.Unlock()

	vsm.logger.WithFields(map[string]interface{}{
		"namespace": cmd.Namespace,
		"id":        cmd.ID,
		"merge":     cmd.Merge,
		"version":   newVersion,
	}).Debug("updated vector metadata")

	// Encode result with new version
	result := &VectorResult{
		Success:   true,
		Version:   newVersion,
		Timestamp: time.Now().UnixNano(),
	}

	data, err := result.Encode()
	if err != nil {
		return sm.Result{Value: newVersion}, nil
	}

	return sm.Result{Value: newVersion, Data: data}, nil
}

// Lookup implements sm.DiskStateMachine.Lookup for read-only queries.
func (vsm *VectorStateMachine) Lookup(ctx context.Context, query interface{}) (interface{}, error) {
	vsm.mu.RLock()
	defer vsm.mu.RUnlock()

	if vsm.closed {
		return nil, &errors.DragonError{
			Code: errors.ErrInternal,
			Op:   "VectorStateMachine.Lookup",
			Err:  &api.InvalidInputError{Message: "state machine is closed"},
		}
	}

	// Accept both raw bytes and VectorCommand
	var cmd *VectorCommand
	switch v := query.(type) {
	case []byte:
		var err error
		cmd, err = DecodeVectorCommand(v)
		if err != nil {
			return nil, err
		}
	case *VectorCommand:
		cmd = v
	default:
		return nil, api.NewInvalidFormatMessageError("query", "must be []byte or *VectorCommand")
	}

	switch cmd.Op {
	case OpGetVector:
		return vsm.handleGetVector(ctx, cmd)
	case OpSearchSimilar:
		return vsm.handleSearch(ctx, cmd)
	case OpSearchByID:
		return vsm.handleSearchByID(ctx, cmd)
	case OpListVectors:
		return vsm.handleListVectors(ctx, cmd)
	case OpGetNamespaceInfo:
		return vsm.handleGetNamespaceInfo(ctx, cmd)
	case OpListNamespaces:
		return vsm.handleListNamespaces(ctx, cmd)
	case OpGetStats:
		return vsm.handleGetStats(ctx, cmd)
	case OpGetIndexProgress:
		return vsm.handleGetIndexProgress(ctx, cmd)
	case OpHealthCheck:
		return vsm.handleHealthCheck(ctx, cmd)
	default:
		return nil, api.NewUnknownOperationError("VectorStateMachine.Lookup", cmd.Op.String())
	}
}

// handleGetVector retrieves a single vector by ID.
func (vsm *VectorStateMachine) handleGetVector(ctx context.Context, cmd *VectorCommand) (*VectorResult, error) {
	// SECURITY: Validate tenant ownership before get
	if err := vsm.validateTenantOwnership(cmd.Namespace, cmd.TenantID); err != nil {
		return nil, err
	}

	// Try cache first
	if vsm.cache != nil {
		cacheKey := fmt.Sprintf("vector:%s:%d", cmd.Namespace, cmd.ID)
		if data, err := vsm.cache.Get(ctx, []byte(cacheKey)); err == nil {
			vsm.metrics.mu.Lock()
			vsm.metrics.CacheHits++
			vsm.metrics.mu.Unlock()

			var result VectorResult
			if err := result.Decode(data); err == nil {
				return &result, nil
			}
		} else {
			vsm.metrics.mu.Lock()
			vsm.metrics.CacheMisses++
			vsm.metrics.mu.Unlock()
		}
	}

	// Get the underlying vector storage from adapter
	vectorStorage, ok := vsm.adapter.(interface{ VectorStorage() vector.Storage })
	if !ok {
		return nil, api.NewServiceUnavailableError("vector storage")
	}

	// Get from vector storage
	vec, err := vectorStorage.VectorStorage().GetVector(ctx, cmd.Namespace, cmd.ID)
	if err != nil {
		return nil, api.NewNotFoundError("vector", fmt.Sprintf("%d", cmd.ID))
	}

	result := &VectorResult{
		Success:   true,
		VectorID:  vec.ID,
		Vector:    vec.Embedding,
		Metadata:  convertFromStringMap(vec.Metadata),
		Version:   vec.Version,
		Timestamp: time.Now().UnixNano(),
	}

	// Cache the result
	if vsm.cache != nil {
		if cacheData, err := result.Encode(); err == nil {
			cacheKey := fmt.Sprintf("vector:%s:%d", cmd.Namespace, cmd.ID)
			_ = vsm.cache.Set(ctx, []byte(cacheKey), cacheData, 5*time.Minute)
		}
	}

	return result, nil
}

// handleSearch performs HNSW similarity search with namespace isolation.
// CRITICAL SECURITY FIX: Uses per-namespace HNSW index to prevent cross-tenant data leakage.
func (vsm *VectorStateMachine) handleSearch(ctx context.Context, cmd *VectorCommand) (*VectorResult, error) {
	start := time.Now()
	defer func() {
		vsm.metrics.mu.Lock()
		vsm.metrics.SearchLatencyNs = uint64(time.Since(start).Nanoseconds()) //nolint:gosec // G115: Safe conversion - duration always positive
		vsm.metrics.SearchCount++
		vsm.metrics.mu.Unlock()
	}()

	// SECURITY: Validate tenant ownership before search
	if err := vsm.validateTenantOwnership(cmd.Namespace, cmd.TenantID); err != nil {
		return nil, err
	}

	// Get namespace-specific HNSW index (SECURITY FIX: per-namespace isolation)
	hnswIdx := vsm.getIndex(cmd.Namespace)
	if hnswIdx == nil {
		return nil, api.NewNotFoundError("HNSW index", cmd.Namespace)
	}

	// Perform HNSW search on namespace-specific index
	results, err := hnswIdx.Search(cmd.Vector, cmd.K, cmd.EfSearch)
	if err != nil {
		return nil, api.NewOperationFailedError("HNSW search", err)
	}

	// Get the underlying vector storage from adapter for fetching metadata
	vectorStorage, ok := vsm.adapter.(interface{ VectorStorage() vector.Storage })
	if !ok {
		return nil, api.NewServiceUnavailableError("vector storage")
	}

	// Convert results
	searchResults := make([]VectorSearchResult, len(results))
	for i, res := range results {
		searchResults[i] = VectorSearchResult{
			ID:    res.ID,
			Score: res.Distance,
		}

		// Optionally fetch full vector and metadata from storage
		if vec, err := vectorStorage.VectorStorage().GetVector(ctx, cmd.Namespace, res.ID); err == nil {
			searchResults[i].Vector = vec.Embedding
			searchResults[i].Metadata = convertFromStringMap(vec.Metadata)
		}
	}

	return &VectorResult{
		Success:   true,
		Results:   searchResults,
		Timestamp: time.Now().UnixNano(),
	}, nil
}

// handleSearchByID performs search using an existing vector as the query.
func (vsm *VectorStateMachine) handleSearchByID(ctx context.Context, cmd *VectorCommand) (*VectorResult, error) {
	// SECURITY: Validate tenant ownership before search
	if err := vsm.validateTenantOwnership(cmd.Namespace, cmd.TenantID); err != nil {
		return nil, err
	}

	// Get the underlying vector storage from adapter
	vectorStorage, ok := vsm.adapter.(interface{ VectorStorage() vector.Storage })
	if !ok {
		return nil, api.NewServiceUnavailableError("vector storage")
	}

	// First, get the vector
	vec, err := vectorStorage.VectorStorage().GetVector(ctx, cmd.Namespace, cmd.ID)
	if err != nil {
		return nil, api.NewNotFoundError("vector", fmt.Sprintf("%d", cmd.ID))
	}

	// Use the vector's embedding to perform search
	// Note: TenantID is already validated, pass it through for the search
	searchCmd := &VectorCommand{
		Op:        OpSearchSimilar,
		Namespace: cmd.Namespace,
		Vector:    vec.Embedding,
		K:         cmd.K,
		EfSearch:  cmd.EfSearch,
		TenantID:  cmd.TenantID,
	}

	return vsm.handleSearch(ctx, searchCmd)
}

// handleListVectors lists all vectors in a namespace.
func (vsm *VectorStateMachine) handleListVectors(ctx context.Context, cmd *VectorCommand) (*VectorResult, error) {
	// SECURITY: Validate tenant ownership before listing
	if err := vsm.validateTenantOwnership(cmd.Namespace, cmd.TenantID); err != nil {
		return nil, err
	}

	// Get the underlying vector storage from adapter
	vectorStorage, ok := vsm.adapter.(interface{ VectorStorage() vector.Storage })
	if !ok {
		return nil, api.NewServiceUnavailableError("vector storage")
	}

	// Set default limit if not specified
	limit := cmd.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	// Scan namespace using vector storage API
	opts := &vector.ScanOptions{
		Limit:  int(limit),
		Offset: int(cmd.Offset),
	}

	vecs, err := vectorStorage.VectorStorage().ScanNamespace(ctx, cmd.Namespace, opts)
	if err != nil {
		return nil, api.NewOperationFailedError("scan namespace", err)
	}

	// Convert to VectorData
	vectors := make([]VectorData, 0, len(vecs))
	for _, vec := range vecs {
		vectors = append(vectors, VectorData{
			ID:        vec.ID,
			Vector:    vec.Embedding,
			Metadata:  convertFromStringMap(vec.Metadata),
			Version:   vec.Version,
			CreatedAt: vec.CreatedAt,
			UpdatedAt: vec.UpdatedAt,
		})
	}

	return &VectorResult{
		Success:    true,
		Vectors:    vectors,
		TotalCount: uint64(len(vectors)),
		Timestamp:  time.Now().UnixNano(),
	}, nil
}

// handleGetNamespaceInfo retrieves namespace metadata.
func (vsm *VectorStateMachine) handleGetNamespaceInfo(ctx context.Context, cmd *VectorCommand) (*VectorResult, error) {
	// SECURITY: Validate tenant ownership before getting namespace info
	if err := vsm.validateTenantOwnership(cmd.Namespace, cmd.TenantID); err != nil {
		return nil, err
	}

	// Check in-memory first (already validated by validateTenantOwnership)
	ns := vsm.namespaces[cmd.Namespace]
	ns.mu.RLock()
	defer ns.mu.RUnlock()

	return &VectorResult{
		Success: true,
		NamespaceInfo: &NamespaceData{
			Name:        ns.Name,
			Dimension:   ns.Dimension,
			VectorCount: ns.VectorCount,
			CreatedAt:   ns.CreatedAt,
			UpdatedAt:   ns.UpdatedAt,
		},
		Timestamp: time.Now().UnixNano(),
	}, nil
}

// handleListNamespaces lists all namespaces, filtered by tenant.
func (vsm *VectorStateMachine) handleListNamespaces(ctx context.Context, cmd *VectorCommand) (*VectorResult, error) {
	namespaces := make([]string, 0, len(vsm.namespaces))

	for name, ns := range vsm.namespaces {
		// SECURITY: Filter namespaces by tenant ownership
		// Include namespace if:
		// 1. It has no tenant (system/legacy namespace) - accessible to all
		// 2. It matches the request's tenant ID
		if ns.TenantID == "" || ns.TenantID == cmd.TenantID {
			namespaces = append(namespaces, name)
		}
	}

	return &VectorResult{
		Success:    true,
		Namespaces: namespaces,
		Timestamp:  time.Now().UnixNano(),
	}, nil
}

// handleGetStats retrieves namespace statistics.
func (vsm *VectorStateMachine) handleGetStats(ctx context.Context, cmd *VectorCommand) (*VectorResult, error) {
	// SECURITY: Validate tenant ownership before getting stats
	if err := vsm.validateTenantOwnership(cmd.Namespace, cmd.TenantID); err != nil {
		return nil, err
	}

	ns := vsm.namespaces[cmd.Namespace]

	ns.mu.RLock()
	defer ns.mu.RUnlock()

	// Estimate index size: each vector stores float32 values + HNSW graph overhead
	// HNSW graph: ~M * 4 bytes per node for neighbor pointers (assuming M=16 default)
	const hnswMFactor = 16
	vectorDataSize := uint64(ns.VectorCount) * uint64(ns.Dimension) * 4 // float32 = 4 bytes
	hnswOverhead := uint64(ns.VectorCount) * hnswMFactor * 4            // neighbor pointers
	indexSize := vectorDataSize + hnswOverhead

	// Estimate storage size: vector data + metadata overhead (~100 bytes per vector)
	metadataOverhead := uint64(ns.VectorCount) * 100
	storageSize := vectorDataSize + metadataOverhead

	stats := &VectorStats{
		VectorCount: ns.VectorCount,
		Dimension:   ns.Dimension,
		IndexSize:   indexSize,
		StorageSize: storageSize,
	}

	return &VectorResult{
		Success:     true,
		VectorStats: stats,
		Timestamp:   time.Now().UnixNano(),
	}, nil
}

// handleGetIndexProgress retrieves index rebuild progress.
func (vsm *VectorStateMachine) handleGetIndexProgress(ctx context.Context, cmd *VectorCommand) (*VectorResult, error) {
	job, exists := vsm.indexRebuildJobs[cmd.JobID]
	if !exists {
		// Return not in progress if job not found
		return &VectorResult{
			Success: true,
			IndexProgress: &IndexProgress{
				InProgress:       false,
				VectorsProcessed: 0,
				TotalVectors:     0,
				PercentComplete:  100.0,
			},
			Timestamp: time.Now().UnixNano(),
		}, nil
	}

	job.mu.RLock()
	defer job.mu.RUnlock()

	percentComplete := 0.0
	if job.TotalVectors > 0 {
		percentComplete = float64(job.VectorsProcessed) / float64(job.TotalVectors) * 100.0
	}

	return &VectorResult{
		Success: true,
		IndexProgress: &IndexProgress{
			InProgress:       job.InProgress,
			VectorsProcessed: job.VectorsProcessed,
			TotalVectors:     job.TotalVectors,
			PercentComplete:  percentComplete,
			JobID:            job.JobID,
		},
		Timestamp: time.Now().UnixNano(),
	}, nil
}

// handleHealthCheck performs health check.
func (vsm *VectorStateMachine) handleHealthCheck(ctx context.Context, cmd *VectorCommand) (*VectorResult, error) {
	return &VectorResult{
		Success: true,
		HealthStatus: &HealthStatus{
			Healthy:   true,
			Message:   "vector state machine operational",
			ShardID:   vsm.shardID,
			NodeCount: len(vsm.namespaces),
		},
		Timestamp: time.Now().UnixNano(),
	}, nil
}

// Sync implements sm.DiskStateMachine.Sync.
func (vsm *VectorStateMachine) Sync() error {
	vsm.logger.Debug("syncing vector state machine")
	// Storage handles its own syncing
	return nil
}

// Close implements sm.DiskStateMachine.Close.
func (vsm *VectorStateMachine) Close(ctx context.Context) error {
	vsm.mu.Lock()
	defer vsm.mu.Unlock()

	if vsm.closed {
		return nil
	}

	vsm.closed = true

	if vsm.adapter != nil {
		if err := vsm.adapter.Close(); err != nil {
			vsm.logger.WithError(err).Error("failed to close storage adapter")
		}
	}

	vsm.logger.Info("closed vector state machine")
	return nil
}

// GetMetrics returns current performance metrics.
func (vsm *VectorStateMachine) GetMetrics() *VectorMetrics {
	vsm.metrics.mu.RLock()
	defer vsm.metrics.mu.RUnlock()

	// Create a copy to avoid race conditions
	return &VectorMetrics{
		UpsertCount:     vsm.metrics.UpsertCount,
		DeleteCount:     vsm.metrics.DeleteCount,
		SearchCount:     vsm.metrics.SearchCount,
		SnapshotCount:   vsm.metrics.SnapshotCount,
		RecoveryCount:   vsm.metrics.RecoveryCount,
		ErrorCount:      vsm.metrics.ErrorCount,
		UpsertLatencyNs: vsm.metrics.UpsertLatencyNs,
		DeleteLatencyNs: vsm.metrics.DeleteLatencyNs,
		SearchLatencyNs: vsm.metrics.SearchLatencyNs,
		CacheHits:       vsm.metrics.CacheHits,
		CacheMisses:     vsm.metrics.CacheMisses,
	}
}

// convertToStringMap converts map[string]interface{} to map[string]string.
func convertToStringMap(m map[string]interface{}) map[string]string {
	if m == nil {
		return nil
	}
	result := make(map[string]string, len(m))
	for k, v := range m {
		result[k] = fmt.Sprintf("%v", v)
	}
	return result
}

// convertFromStringMap converts map[string]string to map[string]interface{}.
func convertFromStringMap(m map[string]string) map[string]interface{} {
	if m == nil {
		return nil
	}
	result := make(map[string]interface{}, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}

// PrepareSnapshot implements sm.DiskStateMachine.PrepareSnapshot.
func (vsm *VectorStateMachine) PrepareSnapshot() (interface{}, error) {
	vsm.mu.RLock()
	defer vsm.mu.RUnlock()

	vsm.logger.Info("preparing vector snapshot")

	// Return namespace metadata for snapshot
	snapshot := &VectorSnapshot{
		Namespaces: make(map[string]*NamespaceState),
		Timestamp:  time.Now().UnixNano(),
	}

	for name, ns := range vsm.namespaces {
		snapshot.Namespaces[name] = &NamespaceState{
			Name:         ns.Name,
			Dimension:    ns.Dimension,
			VectorCount:  ns.VectorCount,
			CreatedAt:    ns.CreatedAt,
			UpdatedAt:    ns.UpdatedAt,
			IndexParams:  ns.IndexParams,
			TenantID:     ns.TenantID,
			OwnerUserID:  ns.OwnerUserID,
			Quotas:       ns.Quotas,
			Labels:       ns.Labels,
			StorageBytes: ns.StorageBytes,
		}
	}

	return snapshot, nil
}

// SaveSnapshot implements sm.DiskStateMachine.SaveSnapshot.
// Saves all namespace indexes independently for proper isolation.
func (vsm *VectorStateMachine) SaveSnapshot(ctx context.Context, snapshotCtx interface{}, w io.Writer, stopper <-chan struct{}) error {
	vsm.mu.RLock()
	defer vsm.mu.RUnlock()

	start := time.Now()
	defer func() {
		vsm.metrics.mu.Lock()
		vsm.metrics.SnapshotCount++
		vsm.metrics.mu.Unlock()

		vsm.logger.WithField("duration", time.Since(start)).Info("saved vector snapshot")
	}()

	// Create snapshot with namespace index count
	snap := &VectorSnapshot{
		Namespaces: make(map[string]*NamespaceState),
		Timestamp:  time.Now().UnixNano(),
	}

	for name, ns := range vsm.namespaces {
		snap.Namespaces[name] = &NamespaceState{
			Name:         ns.Name,
			Dimension:    ns.Dimension,
			VectorCount:  ns.VectorCount,
			CreatedAt:    ns.CreatedAt,
			UpdatedAt:    ns.UpdatedAt,
			IndexParams:  ns.IndexParams,
			TenantID:     ns.TenantID,
			OwnerUserID:  ns.OwnerUserID,
			Quotas:       ns.Quotas,
			Labels:       ns.Labels,
			StorageBytes: ns.StorageBytes,
		}
	}

	// Encode snapshot metadata
	data, err := msgpack.Marshal(snap)
	if err != nil {
		return api.NewOperationFailedError("encode snapshot", err)
	}

	// Write snapshot size
	size := EncodeUint64(uint64(len(data)))
	if _, err := w.Write(size); err != nil {
		return err
	}

	// Write snapshot data
	if _, err := w.Write(data); err != nil {
		return err
	}

	// Save count of HNSW indexes
	vsm.indexesMu.RLock()
	indexCount := uint64(len(vsm.hnswIndexes))
	vsm.indexesMu.RUnlock()

	indexCountBytes := EncodeUint64(indexCount)
	if _, err := w.Write(indexCountBytes); err != nil {
		return err
	}

	// Save each namespace's HNSW index
	vsm.indexesMu.RLock()
	defer vsm.indexesMu.RUnlock()

	for namespace, idx := range vsm.hnswIndexes {
		// Write namespace name length and name
		nameBytes := []byte(namespace)
		nameLenBytes := EncodeUint64(uint64(len(nameBytes)))
		if _, err := w.Write(nameLenBytes); err != nil {
			return err
		}
		if _, err := w.Write(nameBytes); err != nil {
			return err
		}

		// Save the HNSW index to a buffer first to get its size
		var indexBuf []byte
		indexWriter := &byteSliceWriter{buf: &indexBuf}
		if err := idx.SaveSnapshot(indexWriter); err != nil {
			vsm.logger.WithFields(map[string]interface{}{
				"namespace": namespace,
				"error":     err,
			}).Warn("failed to save HNSW snapshot for namespace")
			// Write zero size to indicate empty/failed index
			if _, err := w.Write(EncodeUint64(0)); err != nil {
				return err
			}
			continue
		}

		// Write index size and data
		indexSizeBytes := EncodeUint64(uint64(len(indexBuf)))
		if _, err := w.Write(indexSizeBytes); err != nil {
			return err
		}
		if _, err := w.Write(indexBuf); err != nil {
			return err
		}
	}

	return nil
}

// byteSliceWriter is a simple writer that appends to a byte slice.
type byteSliceWriter struct {
	buf *[]byte
}

func (w *byteSliceWriter) Write(p []byte) (n int, err error) {
	*w.buf = append(*w.buf, p...)
	return len(p), nil
}

// RecoverFromSnapshot implements sm.DiskStateMachine.RecoverFromSnapshot.
// Restores all namespace indexes independently for proper isolation.
func (vsm *VectorStateMachine) RecoverFromSnapshot(ctx context.Context, r io.Reader, stopper <-chan struct{}) error {
	vsm.mu.Lock()
	defer vsm.mu.Unlock()

	start := time.Now()
	defer func() {
		vsm.metrics.mu.Lock()
		vsm.metrics.RecoveryCount++
		vsm.metrics.mu.Unlock()

		vsm.logger.WithField("duration", time.Since(start)).Info("recovered from vector snapshot")
	}()

	// Read snapshot size
	sizeBuf := make([]byte, 8)
	if _, err := io.ReadFull(r, sizeBuf); err != nil {
		return api.NewOperationFailedError("read snapshot size", err)
	}

	size := DecodeUint64(sizeBuf)

	// Read snapshot data
	data := make([]byte, size)
	if _, err := io.ReadFull(r, data); err != nil {
		return api.NewOperationFailedError("read snapshot data", err)
	}

	// Decode snapshot
	var snap VectorSnapshot
	if err := msgpack.Unmarshal(data, &snap); err != nil {
		return api.NewOperationFailedError("decode snapshot", err)
	}

	// Restore namespaces
	vsm.namespaces = snap.Namespaces

	// Read index count
	indexCountBuf := make([]byte, 8)
	if _, err := io.ReadFull(r, indexCountBuf); err != nil {
		// If we can't read index count, the snapshot might be from an older version
		vsm.logger.Warn("failed to read HNSW index count from snapshot, indexes will be rebuilt")
		return nil
	}

	indexCount := DecodeUint64(indexCountBuf)

	// Clear existing indexes
	vsm.indexesMu.Lock()
	vsm.hnswIndexes = make(map[string]HNSWIndex)
	vsm.indexesMu.Unlock()

	// Restore each namespace's HNSW index
	for i := uint64(0); i < indexCount; i++ {
		// Read namespace name length
		nameLenBuf := make([]byte, 8)
		if _, err := io.ReadFull(r, nameLenBuf); err != nil {
			vsm.logger.WithError(err).Warn("failed to read namespace name length")
			break
		}
		nameLen := DecodeUint64(nameLenBuf)

		// Read namespace name
		nameBytes := make([]byte, nameLen)
		if _, err := io.ReadFull(r, nameBytes); err != nil {
			vsm.logger.WithError(err).Warn("failed to read namespace name")
			break
		}
		namespace := string(nameBytes)

		// Read index size
		indexSizeBuf := make([]byte, 8)
		if _, err := io.ReadFull(r, indexSizeBuf); err != nil {
			vsm.logger.WithError(err).Warn("failed to read HNSW index size")
			break
		}
		indexSize := DecodeUint64(indexSizeBuf)

		// Skip if index was empty/failed during save
		if indexSize == 0 {
			continue
		}

		// Read index data
		indexData := make([]byte, indexSize)
		if _, err := io.ReadFull(r, indexData); err != nil {
			vsm.logger.WithError(err).Warn("failed to read HNSW index data")
			break
		}

		// Create new index and restore from snapshot
		config := vsm.buildIndexConfig(namespace)
		idx, err := vsm.hnswFactory(config)
		if err != nil {
			vsm.logger.WithFields(map[string]interface{}{
				"namespace": namespace,
				"error":     err,
			}).Warn("failed to create HNSW index during recovery")
			continue
		}

		// Load the snapshot data
		indexReader := &byteSliceReader{buf: indexData}
		if err := idx.LoadSnapshot(indexReader); err != nil {
			vsm.logger.WithFields(map[string]interface{}{
				"namespace": namespace,
				"error":     err,
			}).Warn("failed to load HNSW snapshot for namespace")
			continue
		}

		vsm.indexesMu.Lock()
		vsm.hnswIndexes[namespace] = idx
		vsm.indexesMu.Unlock()

		vsm.logger.WithField("namespace", namespace).Debug("recovered HNSW index for namespace")
	}

	vsm.logger.WithField("namespaces", len(vsm.namespaces)).Info("recovered vector state machine with per-namespace HNSW indexes")
	return nil
}

// byteSliceReader is a simple reader that reads from a byte slice.
type byteSliceReader struct {
	buf []byte
	pos int
}

func (r *byteSliceReader) Read(p []byte) (n int, err error) {
	if r.pos >= len(r.buf) {
		return 0, io.EOF
	}
	n = copy(p, r.buf[r.pos:])
	r.pos += n
	return n, nil
}

// Open implements sm.DiskStateMachine.Open.
// Creates HNSW indexes for existing namespaces.
func (vsm *VectorStateMachine) Open(ctx context.Context, initialDir string, stopper <-chan struct{}) (uint64, error) {
	vsm.logger.Info("opening vector state machine")

	// Load namespaces from vector storage
	vectorStorage, ok := vsm.adapter.(interface{ VectorStorage() vector.Storage })
	if !ok {
		vsm.logger.Warn("adapter does not provide vector storage, skipping namespace loading")
	} else {
		namespaces, err := vectorStorage.VectorStorage().ListNamespaces(ctx)
		if err != nil {
			vsm.logger.WithError(err).Warn("failed to load namespaces")
		} else {
			for _, info := range namespaces {
				vsm.namespaces[info.Name] = &NamespaceState{
					Name:        info.Name,
					Dimension:   info.Dimension,
					VectorCount: info.VectorCount,
					CreatedAt:   info.CreatedAt,
					UpdatedAt:   info.UpdatedAt,
				}

				// Create HNSW index for each namespace (SECURITY FIX: per-namespace isolation)
				if _, err := vsm.getOrCreateIndex(info.Name); err != nil {
					vsm.logger.WithFields(map[string]interface{}{
						"namespace": info.Name,
						"error":     err,
					}).Warn("failed to create HNSW index for namespace during open")
				}
			}
		}
	}

	vsm.logger.WithFields(map[string]interface{}{
		"namespaces":   len(vsm.namespaces),
		"hnsw_indexes": len(vsm.hnswIndexes),
	}).Info("vector state machine opened with per-namespace HNSW isolation")
	return 0, nil
}
