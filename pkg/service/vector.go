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

// Package service provides the unified service facade for qrdb.
// This file implements the VectorService interface.
package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	api "github.com/jeremyhahn/go-qrdb/pkg/api"
	qrdbErrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/interfaces"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/util/metrics"
	"github.com/jeremyhahn/go-qrdb/pkg/vector/hnsw"
)

// VectorShardID is the default shard ID for vector operations.
// Shard 1 is used for KV operations, Shard 2 is for vectors.
const VectorShardID uint64 = 2

// vectorService is the concrete implementation of VectorService.
// It orchestrates vector storage, HNSW indexing, and metadata operations.
type vectorService struct {
	storage        vector.Storage
	raftNode       interfaces.RaftNode
	shardID        uint64
	nodeID         string
	hnswIndexes    map[string]interfaces.HNSWIndex // Map namespace -> HNSW index
	indexFactory   interfaces.HNSWIndexFactory     // Factory for creating HNSW indexes
	logger         interfaces.Logger
	metrics        *vectorServiceMetrics
	mu             sync.RWMutex
	closed         bool
	indexMu        sync.RWMutex
	rebuildJobs    map[string]*RebuildJobState
	compactionJobs map[string]*CompactionJobState
	jobMu          sync.RWMutex
}

// vectorServiceMetrics holds Prometheus metrics for vector operations.
type vectorServiceMetrics struct {
	namespacesCreated *metrics.Counter
	namespacesDeleted *metrics.Counter
	vectorsUpserted   *metrics.Counter
	vectorsDeleted    *metrics.Counter
	searchesPerformed *metrics.Counter
	operationDuration *metrics.HistogramVec
	operationErrors   *metrics.CounterVec
	indexSizeBytes    *metrics.GaugeVec
	vectorCount       *metrics.GaugeVec
}

// RebuildJobState tracks the state of an index rebuild job.
type RebuildJobState struct {
	JobID            string
	Namespace        string
	InProgress       bool
	VectorsProcessed uint64
	TotalVectors     uint64
	StartedAt        time.Time
	CompletedAt      *time.Time
	Error            error
	mu               sync.RWMutex
}

// CompactionJobState tracks the state of a compaction job.
type CompactionJobState struct {
	OperationID    string
	Namespace      string
	InProgress     bool
	BytesCompacted int64
	TotalBytes     int64
	StartedAt      time.Time
	CompletedAt    *time.Time
	Error          error
	mu             sync.RWMutex
}

// VectorServiceOption is a functional option for configuring the vector service.
type VectorServiceOption func(*vectorService)

// WithRaftNode sets the Raft node for distributed consensus.
func WithRaftNode(raftNode interfaces.RaftNode) VectorServiceOption {
	return func(v *vectorService) {
		v.raftNode = raftNode
	}
}

// WithVectorShardID sets the shard ID for vector operations.
func WithVectorShardID(shardID uint64) VectorServiceOption {
	return func(v *vectorService) {
		v.shardID = shardID
	}
}

// WithNodeID sets the node ID for Raft commands.
func WithNodeID(nodeID string) VectorServiceOption {
	return func(v *vectorService) {
		v.nodeID = nodeID
	}
}

// WithHNSWIndexFactory sets a custom factory for creating HNSW indexes.
// This is useful for dependency injection and testing.
func WithHNSWIndexFactory(factory interfaces.HNSWIndexFactory) VectorServiceOption {
	return func(v *vectorService) {
		v.indexFactory = factory
	}
}

// NewVectorService creates a new vector service implementation.
func NewVectorService(
	storage vector.Storage,
	logger interfaces.Logger,
	opts ...VectorServiceOption,
) (VectorService, error) {
	if storage == nil {
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "service.NewVectorService",
			Err:  &api.RequiredFieldError{Field: "storage"},
		}
	}

	if logger == nil {
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "service.NewVectorService",
			Err:  &api.RequiredFieldError{Field: "logger"},
		}
	}

	svc := &vectorService{
		storage:        storage,
		shardID:        VectorShardID, // Default to shard 2 for vectors
		hnswIndexes:    make(map[string]interfaces.HNSWIndex),
		logger:         logger.WithField("component", "vector_service"),
		rebuildJobs:    make(map[string]*RebuildJobState),
		compactionJobs: make(map[string]*CompactionJobState),
	}

	// Apply functional options
	for _, opt := range opts {
		opt(svc)
	}

	// Initialize metrics
	svc.initMetrics()

	// Load existing namespaces and initialize indexes
	if err := svc.loadNamespaces(); err != nil {
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInternal,
			Op:   "service.NewVectorService",
			Err:  &api.OperationFailedError{Operation: "load namespaces", Cause: err},
		}
	}

	svc.logger.WithFields(map[string]interface{}{
		"shard_id":     svc.shardID,
		"raft_enabled": svc.raftNode != nil,
	}).Info("vector service initialized")

	return svc, nil
}

// initMetrics initializes Prometheus metrics for the vector service.
func (v *vectorService) initMetrics() {
	v.metrics = &vectorServiceMetrics{
		namespacesCreated: metrics.NewCounter(metrics.CounterOpts{
			Namespace: "qrdb",
			Subsystem: "vector",
			Name:      "namespaces_created_total",
			Help:      "Total number of vector namespaces created",
		}),

		namespacesDeleted: metrics.NewCounter(metrics.CounterOpts{
			Namespace: "qrdb",
			Subsystem: "vector",
			Name:      "namespaces_deleted_total",
			Help:      "Total number of vector namespaces deleted",
		}),

		vectorsUpserted: metrics.NewCounter(metrics.CounterOpts{
			Namespace: "qrdb",
			Subsystem: "vector",
			Name:      "vectors_upserted_total",
			Help:      "Total number of vectors upserted",
		}),

		vectorsDeleted: metrics.NewCounter(metrics.CounterOpts{
			Namespace: "qrdb",
			Subsystem: "vector",
			Name:      "vectors_deleted_total",
			Help:      "Total number of vectors deleted",
		}),

		searchesPerformed: metrics.NewCounter(metrics.CounterOpts{
			Namespace: "qrdb",
			Subsystem: "vector",
			Name:      "searches_total",
			Help:      "Total number of similarity searches performed",
		}),

		operationDuration: metrics.NewHistogramVec(metrics.HistogramOpts{
			Namespace: "qrdb",
			Subsystem: "vector",
			Name:      "operation_duration_seconds",
			Help:      "Duration of vector operations in seconds",
			Buckets:   []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
		}, []string{"operation"}),

		operationErrors: metrics.NewCounterVec(metrics.CounterOpts{
			Namespace: "qrdb",
			Subsystem: "vector",
			Name:      "operation_errors_total",
			Help:      "Total errors by operation type",
		}, []string{"operation", "error_type"}),

		indexSizeBytes: metrics.NewGaugeVec(metrics.GaugeOpts{
			Namespace: "qrdb",
			Subsystem: "vector",
			Name:      "index_size_bytes",
			Help:      "Size of HNSW index in bytes",
		}, []string{"namespace"}),

		vectorCount: metrics.NewGaugeVec(metrics.GaugeOpts{
			Namespace: "qrdb",
			Subsystem: "vector",
			Name:      "vector_count",
			Help:      "Number of vectors in namespace",
		}, []string{"namespace"}),
	}
}

// loadNamespaces loads existing namespaces and initializes HNSW indexes.
func (v *vectorService) loadNamespaces() error {
	ctx := context.Background()

	namespaces, err := v.storage.ListNamespaces(ctx)
	if err != nil {
		return err
	}

	v.logger.WithField("count", len(namespaces)).Debug("loading namespaces")

	for _, ns := range namespaces {
		// Initialize HNSW index for namespace
		config := &hnsw.Config{
			M:              16,
			MMax:           32,
			EfConstruction: 200,
			EfSearch:       50,
			DistanceMetric: hnsw.L2Distance,
		}

		index, err := hnsw.NewHNSWIndex(config)
		if err != nil {
			v.logger.WithFields(map[string]interface{}{
				"namespace": ns.Name,
				"error":     err,
			}).Warn("failed to create HNSW index for namespace")
			continue
		}

		v.indexMu.Lock()
		v.hnswIndexes[ns.Name] = hnsw.NewIndexAdapter(index)
		v.indexMu.Unlock()

		// Update metrics
		if v.metrics != nil {
			v.metrics.vectorCount.With(ns.Name).Set(float64(ns.VectorCount))
		}
	}

	return nil
}

// ensureHNSWIndex ensures that an HNSW index exists for the given namespace.
func (v *vectorService) ensureHNSWIndex(ctx context.Context, namespace string) (interfaces.HNSWIndex, error) {
	// First check if index already exists
	v.indexMu.RLock()
	index, exists := v.hnswIndexes[namespace]
	v.indexMu.RUnlock()

	if exists {
		return index, nil
	}

	// Index doesn't exist, check if namespace exists in storage
	info, err := v.storage.GetNamespaceInfo(ctx, namespace)
	if err != nil {
		return nil, err
	}

	// Parse distance metric from storage
	var distanceMetric hnsw.DistanceMetric
	switch info.DistanceMetric {
	case "COSINE", "cosine":
		distanceMetric = hnsw.CosineDistance
	case "DOT_PRODUCT", "dot_product":
		distanceMetric = hnsw.DotProductDistance
	default:
		distanceMetric = hnsw.L2Distance
	}

	// Create HNSW index
	config := &hnsw.Config{
		M:              16,
		MMax:           32,
		EfConstruction: 200,
		EfSearch:       50,
		DistanceMetric: distanceMetric,
	}

	newIndex, err := hnsw.NewHNSWIndex(config)
	if err != nil {
		return nil, &api.OperationFailedError{Operation: "create HNSW index", Cause: err}
	}

	// Load all vectors from storage into the index
	vectors, err := v.storage.ScanNamespace(ctx, namespace, nil)
	if err != nil {
		v.logger.WithFields(map[string]interface{}{
			"namespace": namespace,
			"error":     err,
		}).Warn("failed to scan vectors for HNSW index rebuild")
	} else {
		for _, vec := range vectors {
			if err := newIndex.Insert(vec.ID, vec.Embedding); err != nil {
				v.logger.WithFields(map[string]interface{}{
					"namespace": namespace,
					"vector_id": vec.ID,
					"error":     err,
				}).Warn("failed to insert vector into HNSW index")
			}
		}
	}

	// Store the index (check again in case another goroutine created it)
	v.indexMu.Lock()
	if existingIndex, exists := v.hnswIndexes[namespace]; exists {
		v.indexMu.Unlock()
		return existingIndex, nil
	}
	v.hnswIndexes[namespace] = hnsw.NewIndexAdapter(newIndex)
	v.indexMu.Unlock()

	v.logger.WithFields(map[string]interface{}{
		"namespace": namespace,
		"vectors":   len(vectors),
	}).Info("lazily created HNSW index for namespace")

	return hnsw.NewIndexAdapter(newIndex), nil
}

// =============================================================================
// Tenant Validation Helpers
// =============================================================================

// validateTenantAccess checks if the request context tenant matches the namespace tenant.
func (v *vectorService) validateTenantAccess(ctx context.Context, namespace string) error {
	requestTenant := tenant.FromContext(ctx)

	// No tenant in context = system/internal request, allow all
	if requestTenant == "" {
		return nil
	}

	// Get namespace info to check its tenant
	info, err := v.storage.GetNamespaceInfo(ctx, namespace)
	if err != nil {
		// Namespace doesn't exist - let the caller handle this
		return nil
	}

	// Namespace has no tenant = shared/system namespace, allow all
	if info.TenantID == "" {
		return nil
	}

	// Tenant mismatch = cross-tenant access attempt
	if info.TenantID != requestTenant {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrForbidden,
			Op:   "vector.validateTenantAccess",
			Err:  &api.InvalidInputError{Message: "cross-tenant access denied: namespace " + namespace + " belongs to tenant " + info.TenantID},
			Context: map[string]interface{}{
				"namespace":      namespace,
				"request_tenant": requestTenant,
				"owner_tenant":   info.TenantID,
			},
		}
	}

	return nil
}

// filterNamespacesByTenant filters namespace list based on tenant context.
func (v *vectorService) filterNamespacesByTenant(ctx context.Context, namespaces []vector.NamespaceInfo) []vector.NamespaceInfo {
	requestTenant := tenant.FromContext(ctx)

	// No tenant in context = return all namespaces
	if requestTenant == "" {
		return namespaces
	}

	filtered := make([]vector.NamespaceInfo, 0, len(namespaces))
	for _, ns := range namespaces {
		// Include if: no tenant (shared) or tenant matches
		if ns.TenantID == "" || ns.TenantID == requestTenant {
			filtered = append(filtered, ns)
		}
	}

	return filtered
}

// =============================================================================
// Raft Proposal Helpers
// =============================================================================

// proposeToRaft proposes a vector command to Raft for consensus.
func (v *vectorService) proposeToRaft(ctx context.Context, cmd *statemachine.VectorCommand) error {
	if v.raftNode == nil {
		return nil
	}

	cmdBytes, err := cmd.Encode()
	if err != nil {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInternal,
			Op:   "vector.proposeToRaft",
			Err:  &api.OperationFailedError{Operation: "encode vector command", Cause: err},
		}
	}

	if _, err := v.raftNode.SyncPropose(ctx, v.shardID, cmdBytes); err != nil {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrRaft,
			Op:   "vector.proposeToRaft",
			Err:  &api.OperationFailedError{Operation: "raft proposal", Cause: err},
		}
	}

	return nil
}

// =============================================================================
// Namespace Management
// =============================================================================

// CreateNamespace creates a new vector namespace with specified dimensions.
func (v *vectorService) CreateNamespace(ctx context.Context, req *CreateNamespaceRequest) (*CreateNamespaceResponse, error) {
	start := time.Now()
	defer func() {
		if v.metrics != nil {
			v.metrics.operationDuration.With("create_namespace").Observe(time.Since(start).Seconds())
		}
	}()

	// Validate request
	if err := v.validateCreateNamespaceRequest(req); err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("create_namespace", "invalid_input").Inc()
		}
		return nil, err
	}

	v.mu.Lock()
	defer v.mu.Unlock()

	if v.closed {
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInternal,
			Op:   "vector.CreateNamespace",
			Err:  &api.ServiceUnavailableError{Service: "vector"},
		}
	}

	// Check if namespace already exists
	existingInfo, err := v.storage.GetNamespaceInfo(ctx, req.Namespace)
	if err == nil && existingInfo != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("create_namespace", "already_exists").Inc()
		}
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrAlreadyExists,
			Op:   "vector.CreateNamespace",
			Err:  &api.InvalidInputError{Message: "namespace " + req.Namespace + " already exists"},
		}
	}

	v.logger.WithFields(map[string]interface{}{
		"namespace": req.Namespace,
		"dimension": req.Dimension,
	}).Debug("creating namespace")

	// Propose to Raft if enabled
	if v.raftNode != nil {
		distMetric := req.DistanceMetric
		if distMetric == "" {
			distMetric = "L2"
		}

		indexType := req.IndexType
		if indexType == "" {
			indexType = "HNSW"
		}

		var nsMeta map[string]string
		if req.Metadata != nil {
			nsMeta = make(map[string]string)
			for k, val := range req.Metadata {
				nsMeta[k] = fmt.Sprintf("%v", val)
			}
		}

		cmd := statemachine.NewCreateNamespaceCommandFull(&statemachine.NamespaceConfig{
			Namespace:      req.Namespace,
			Dimension:      req.Dimension,
			DistanceMetric: distMetric,
			IndexType:      indexType,
			IndexParams:    req.IndexConfig,
			Metadata:       nsMeta,
			NodeID:         v.nodeID,
		})

		if err := v.proposeToRaft(ctx, cmd); err != nil {
			if v.metrics != nil {
				v.metrics.operationErrors.With("create_namespace", "raft_error").Inc()
			}
			return nil, err
		}

		if v.metrics != nil {
			v.metrics.namespacesCreated.Inc()
			v.metrics.vectorCount.With(req.Namespace).Set(0)
		}

		v.logger.WithField("namespace", req.Namespace).Info("namespace created successfully via Raft")

		return &CreateNamespaceResponse{
			Namespace: req.Namespace,
			Dimension: req.Dimension,
			Timestamp: time.Now().UnixNano(),
			Success:   true,
		}, nil
	}

	// Non-Raft mode: Apply to local storage directly
	storageMetadata := make(map[string]string)
	if req.Metadata != nil {
		for k, val := range req.Metadata {
			storageMetadata[k] = fmt.Sprintf("%v", val)
		}
	}

	distanceMetric := req.DistanceMetric
	if distanceMetric == "" {
		distanceMetric = "L2"
	}

	indexType := req.IndexType
	if indexType == "" {
		indexType = "HNSW"
	}

	info := &vector.NamespaceInfo{
		Name:           req.Namespace,
		Dimension:      req.Dimension,
		VectorCount:    0,
		DistanceMetric: distanceMetric,
		IndexType:      indexType,
		IndexConfig:    req.IndexConfig,
		Metadata:       storageMetadata,
		CreatedAt:      time.Now().UnixNano(),
		UpdatedAt:      time.Now().UnixNano(),
	}

	if err := v.storage.CreateNamespace(ctx, info); err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("create_namespace", "storage_error").Inc()
		}
		v.logger.WithFields(map[string]interface{}{
			"namespace": req.Namespace,
			"error":     err,
		}).Error("failed to create namespace")
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrStorage,
			Op:   "vector.CreateNamespace",
			Err:  &api.OperationFailedError{Operation: "create namespace", Cause: err},
		}
	}

	// Initialize HNSW index for namespace
	config := &hnsw.Config{
		M:              16,
		MMax:           32,
		EfConstruction: 200,
		EfSearch:       50,
		DistanceMetric: hnsw.L2Distance,
	}

	if req.IndexConfig != nil {
		if m, ok := req.IndexConfig["m"]; ok {
			if _, err := fmt.Sscanf(m, "%d", &config.M); err != nil {
				v.logger.WithFields(map[string]interface{}{
					"parameter": "m",
					"value":     m,
					"error":     err,
				}).Warn("failed to parse index parameter")
			} else {
				config.MMax = config.M * 2
			}
		}
		if ef, ok := req.IndexConfig["ef_construction"]; ok {
			if _, err := fmt.Sscanf(ef, "%d", &config.EfConstruction); err != nil {
				v.logger.WithFields(map[string]interface{}{
					"parameter": "ef_construction",
					"value":     ef,
					"error":     err,
				}).Warn("failed to parse index parameter")
			}
		}
		if ef, ok := req.IndexConfig["ef_search"]; ok {
			if _, err := fmt.Sscanf(ef, "%d", &config.EfSearch); err != nil {
				v.logger.WithFields(map[string]interface{}{
					"parameter": "ef_search",
					"value":     ef,
					"error":     err,
				}).Warn("failed to parse index parameter")
			}
		}
	}

	index, err := hnsw.NewHNSWIndex(config)
	if err != nil {
		_ = v.storage.DeleteNamespace(ctx, req.Namespace)
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInternal,
			Op:   "vector.CreateNamespace",
			Err:  &api.OperationFailedError{Operation: "create HNSW index", Cause: err},
		}
	}

	v.indexMu.Lock()
	v.hnswIndexes[req.Namespace] = hnsw.NewIndexAdapter(index)
	v.indexMu.Unlock()

	if v.metrics != nil {
		v.metrics.namespacesCreated.Inc()
		v.metrics.vectorCount.With(req.Namespace).Set(0)
	}

	v.logger.WithField("namespace", req.Namespace).Info("namespace created successfully")

	return &CreateNamespaceResponse{
		Namespace: req.Namespace,
		Dimension: req.Dimension,
		Timestamp: time.Now().UnixNano(),
		Success:   true,
	}, nil
}

// DeleteNamespace removes a vector namespace and all its vectors.
func (v *vectorService) DeleteNamespace(ctx context.Context, req *DeleteNamespaceRequest) (*DeleteNamespaceResponse, error) {
	start := time.Now()
	defer func() {
		if v.metrics != nil {
			v.metrics.operationDuration.With("delete_namespace").Observe(time.Since(start).Seconds())
		}
	}()

	if err := v.validateDeleteNamespaceRequest(req); err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("delete_namespace", "invalid_input").Inc()
		}
		return nil, err
	}

	v.mu.Lock()
	defer v.mu.Unlock()

	if v.closed {
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInternal,
			Op:   "vector.DeleteNamespace",
			Err:  &api.ServiceUnavailableError{Service: "vector"},
		}
	}

	v.logger.WithField("namespace", req.Namespace).Debug("deleting namespace")

	info, err := v.storage.GetNamespaceInfo(ctx, req.Namespace)
	var vectorsDeleted uint64
	if err == nil {
		vectorsDeleted = info.VectorCount
	}

	if v.raftNode != nil {
		cmd := statemachine.NewDeleteNamespaceCommand(req.Namespace, v.nodeID)
		if err := v.proposeToRaft(ctx, cmd); err != nil {
			if v.metrics != nil {
				v.metrics.operationErrors.With("delete_namespace", "raft_error").Inc()
			}
			return nil, err
		}

		v.indexMu.Lock()
		delete(v.hnswIndexes, req.Namespace)
		v.indexMu.Unlock()

		if v.metrics != nil {
			v.metrics.namespacesDeleted.Inc()
			v.metrics.vectorCount.With(req.Namespace).Set(0)
		}

		v.logger.WithField("namespace", req.Namespace).Info("namespace deleted successfully via Raft")

		return &DeleteNamespaceResponse{
			Success:        true,
			VectorsDeleted: vectorsDeleted,
			Timestamp:      time.Now().UnixNano(),
		}, nil
	}

	if err := v.storage.DeleteNamespace(ctx, req.Namespace); err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("delete_namespace", "storage_error").Inc()
		}
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrStorage,
			Op:   "vector.DeleteNamespace",
			Err:  &api.OperationFailedError{Operation: "delete namespace", Cause: err},
		}
	}

	v.indexMu.Lock()
	delete(v.hnswIndexes, req.Namespace)
	v.indexMu.Unlock()

	if v.metrics != nil {
		v.metrics.namespacesDeleted.Inc()
		v.metrics.vectorCount.With(req.Namespace).Set(0)
	}

	v.logger.WithField("namespace", req.Namespace).Info("namespace deleted successfully")

	return &DeleteNamespaceResponse{
		Success:        true,
		VectorsDeleted: vectorsDeleted,
		Timestamp:      time.Now().UnixNano(),
	}, nil
}

// ListNamespaces returns all available vector namespaces.
func (v *vectorService) ListNamespaces(ctx context.Context, req *ListNamespacesRequest) (*ListNamespacesResponse, error) {
	start := time.Now()
	defer func() {
		if v.metrics != nil {
			v.metrics.operationDuration.With("list_namespaces").Observe(time.Since(start).Seconds())
		}
	}()

	v.mu.RLock()
	defer v.mu.RUnlock()

	if v.closed {
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInternal,
			Op:   "vector.ListNamespaces",
			Err:  &api.ServiceUnavailableError{Service: "vector"},
		}
	}

	namespaces, err := v.storage.ListNamespaces(ctx)
	if err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("list_namespaces", "storage_error").Inc()
		}
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrStorage,
			Op:   "vector.ListNamespaces",
			Err:  &api.OperationFailedError{Operation: "list namespaces", Cause: err},
		}
	}

	tenantFiltered := v.filterNamespacesByTenant(ctx, namespaces)

	filtered := make([]*NamespaceInfo, 0)
	for _, ns := range tenantFiltered {
		if req.Prefix != "" && len(ns.Name) >= len(req.Prefix) {
			if ns.Name[:len(req.Prefix)] != req.Prefix {
				continue
			}
		}
		filtered = append(filtered, &NamespaceInfo{
			Namespace:      ns.Name,
			Dimension:      ns.Dimension,
			VectorCount:    ns.VectorCount,
			DistanceMetric: ns.DistanceMetric,
			IndexType:      ns.IndexType,
			IndexConfig:    ns.IndexConfig,
			Metadata:       ns.Metadata,
			CreatedAt:      ns.CreatedAt,
			UpdatedAt:      ns.UpdatedAt,
		})
	}

	totalCount := len(filtered)
	offset := req.Offset
	limit := req.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	endIdx := offset + limit
	if endIdx > len(filtered) {
		endIdx = len(filtered)
	}
	if offset > len(filtered) {
		filtered = []*NamespaceInfo{}
	} else {
		filtered = filtered[offset:endIdx]
	}

	return &ListNamespacesResponse{
		Namespaces: filtered,
		TotalCount: totalCount,
		Timestamp:  time.Now().UnixNano(),
	}, nil
}

// GetNamespaceInfo retrieves metadata about a specific namespace.
func (v *vectorService) GetNamespaceInfo(ctx context.Context, req *GetNamespaceInfoRequest) (*GetNamespaceInfoResponse, error) {
	start := time.Now()
	defer func() {
		if v.metrics != nil {
			v.metrics.operationDuration.With("get_namespace_info").Observe(time.Since(start).Seconds())
		}
	}()

	if err := v.validateGetNamespaceInfoRequest(req); err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("get_namespace_info", "invalid_input").Inc()
		}
		return nil, err
	}

	if err := v.validateTenantAccess(ctx, req.Namespace); err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("get_namespace_info", "forbidden").Inc()
		}
		return nil, err
	}

	v.mu.RLock()
	defer v.mu.RUnlock()

	if v.closed {
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInternal,
			Op:   "vector.GetNamespaceInfo",
			Err:  &api.ServiceUnavailableError{Service: "vector"},
		}
	}

	info, err := v.storage.GetNamespaceInfo(ctx, req.Namespace)
	if err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("get_namespace_info", "not_found").Inc()
		}
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrNotFound,
			Op:   "vector.GetNamespaceInfo",
			Err:  &api.OperationFailedError{Operation: "find namespace", Cause: err},
		}
	}

	metadata := make(map[string]interface{})
	if info.Metadata != nil {
		for k, val := range info.Metadata {
			metadata[k] = val
		}
	}

	indexConfig := info.IndexConfig
	if indexConfig == nil {
		indexConfig = make(map[string]string)
	}

	distanceMetric := info.DistanceMetric
	if distanceMetric == "" {
		distanceMetric = "L2"
	}

	indexType := info.IndexType
	if indexType == "" {
		indexType = "HNSW"
	}

	return &GetNamespaceInfoResponse{
		Namespace:      info.Name,
		Dimension:      info.Dimension,
		VectorCount:    info.VectorCount,
		DistanceMetric: distanceMetric,
		IndexType:      indexType,
		IndexConfig:    indexConfig,
		Metadata:       metadata,
		CreatedAt:      info.CreatedAt,
		UpdatedAt:      info.UpdatedAt,
	}, nil
}

// =============================================================================
// Vector CRUD Operations
// =============================================================================

// UpsertVector inserts or updates a vector with optional metadata.
func (v *vectorService) UpsertVector(ctx context.Context, req *UpsertVectorRequest) (*UpsertVectorResponse, error) {
	start := time.Now()
	defer func() {
		if v.metrics != nil {
			v.metrics.operationDuration.With("upsert_vector").Observe(time.Since(start).Seconds())
		}
	}()

	if err := v.validateUpsertVectorRequest(req); err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("upsert_vector", "invalid_input").Inc()
		}
		return nil, err
	}

	if err := v.validateTenantAccess(ctx, req.Namespace); err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("upsert_vector", "forbidden").Inc()
		}
		return nil, err
	}

	v.mu.RLock()
	defer v.mu.RUnlock()

	if v.closed {
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInternal,
			Op:   "vector.UpsertVector",
			Err:  &api.ServiceUnavailableError{Service: "vector"},
		}
	}

	nsInfo, err := v.storage.GetNamespaceInfo(ctx, req.Namespace)
	if err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("upsert_vector", "namespace_not_found").Inc()
		}
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrNotFound,
			Op:   "vector.UpsertVector",
			Err:  &api.OperationFailedError{Operation: "find namespace", Cause: err},
		}
	}

	// #nosec G115 -- vector dimension bounded by practical limits
	if nsInfo.Dimension > 0 && uint32(len(req.Vector)) != nsInfo.Dimension {
		if v.metrics != nil {
			v.metrics.operationErrors.With("upsert_vector", "invalid_dimension").Inc()
		}
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.UpsertVector",
			Err:  &api.InvalidInputError{Message: fmt.Sprintf("vector dimension %d does not match namespace dimension %d", len(req.Vector), nsInfo.Dimension)},
		}
	}

	now := time.Now().UnixNano()

	if v.raftNode != nil {
		cmd := statemachine.NewUpsertCommand(req.Namespace, req.ID, req.Vector, req.Metadata, v.nodeID)
		if err := v.proposeToRaft(ctx, cmd); err != nil {
			if v.metrics != nil {
				v.metrics.operationErrors.With("upsert_vector", "raft_error").Inc()
			}
			return nil, err
		}

		index, err := v.ensureHNSWIndex(ctx, req.Namespace)
		if err != nil {
			v.logger.WithFields(map[string]interface{}{
				"namespace": req.Namespace,
				"error":     err,
			}).Warn("failed to ensure HNSW index after upsert")
		} else {
			if insertErr := index.Insert(req.ID, req.Vector); insertErr != nil {
				v.logger.WithFields(map[string]interface{}{
					"namespace": req.Namespace,
					"id":        req.ID,
					"error":     insertErr,
				}).Warn("failed to insert vector into local HNSW index")
			}
		}

		if v.metrics != nil {
			v.metrics.vectorsUpserted.Inc()
			v.metrics.vectorCount.With(req.Namespace).Inc()
		}

		v.logger.WithFields(map[string]interface{}{
			"namespace": req.Namespace,
			"id":        req.ID,
		}).Debug("vector upserted successfully via Raft")

		return &UpsertVectorResponse{
			ID:        req.ID,
			Version:   1,
			Timestamp: now,
			Success:   true,
		}, nil
	}

	vecValue := &vector.VectorValue{
		ID:        req.ID,
		Embedding: req.Vector,
		Metadata:  convertToStringMap(req.Metadata),
		CreatedAt: now,
		UpdatedAt: now,
		Version:   1,
	}

	if err := v.storage.UpsertVector(ctx, req.Namespace, vecValue); err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("upsert_vector", "storage_error").Inc()
		}
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrStorage,
			Op:   "vector.UpsertVector",
			Err:  &api.OperationFailedError{Operation: "upsert vector", Cause: err},
		}
	}

	v.indexMu.RLock()
	index, exists := v.hnswIndexes[req.Namespace]
	v.indexMu.RUnlock()

	if exists {
		if err := index.Insert(req.ID, req.Vector); err != nil {
			v.logger.WithFields(map[string]interface{}{
				"namespace": req.Namespace,
				"id":        req.ID,
				"error":     err,
			}).Warn("failed to insert vector into HNSW index")
		}
	}

	if v.metrics != nil {
		v.metrics.vectorsUpserted.Inc()
		v.metrics.vectorCount.With(req.Namespace).Inc()
	}

	v.logger.WithFields(map[string]interface{}{
		"namespace": req.Namespace,
		"id":        req.ID,
	}).Debug("vector upserted successfully")

	return &UpsertVectorResponse{
		ID:        req.ID,
		Version:   1,
		Timestamp: now,
		Success:   true,
	}, nil
}

// GetVector retrieves a vector by ID with optional metadata.
func (v *vectorService) GetVector(ctx context.Context, req *GetVectorRequest) (*GetVectorResponse, error) {
	start := time.Now()
	defer func() {
		if v.metrics != nil {
			v.metrics.operationDuration.With("get_vector").Observe(time.Since(start).Seconds())
		}
	}()

	if err := v.validateGetVectorRequest(req); err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("get_vector", "invalid_input").Inc()
		}
		return nil, err
	}

	if err := v.validateTenantAccess(ctx, req.Namespace); err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("get_vector", "forbidden").Inc()
		}
		return nil, err
	}

	v.mu.RLock()
	defer v.mu.RUnlock()

	if v.closed {
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInternal,
			Op:   "vector.GetVector",
			Err:  &api.ServiceUnavailableError{Service: "vector"},
		}
	}

	vecValue, err := v.storage.GetVector(ctx, req.Namespace, req.ID)
	if err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("get_vector", "not_found").Inc()
		}
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrNotFound,
			Op:   "vector.GetVector",
			Err:  &api.OperationFailedError{Operation: "find vector", Cause: err},
		}
	}

	resp := &GetVectorResponse{
		ID:        vecValue.ID,
		Version:   vecValue.Version,
		CreatedAt: vecValue.CreatedAt,
		UpdatedAt: vecValue.UpdatedAt,
		Found:     true,
	}

	if req.IncludeVector {
		resp.Vector = vecValue.Embedding
	}

	if req.IncludeMetadata {
		resp.Metadata = convertFromStringMap(vecValue.Metadata)
	}

	return resp, nil
}

// DeleteVector removes a vector from the namespace.
func (v *vectorService) DeleteVector(ctx context.Context, req *DeleteVectorRequest) (*DeleteVectorResponse, error) {
	start := time.Now()
	defer func() {
		if v.metrics != nil {
			v.metrics.operationDuration.With("delete_vector").Observe(time.Since(start).Seconds())
		}
	}()

	if err := v.validateDeleteVectorRequest(req); err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("delete_vector", "invalid_input").Inc()
		}
		return nil, err
	}

	if err := v.validateTenantAccess(ctx, req.Namespace); err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("delete_vector", "forbidden").Inc()
		}
		return nil, err
	}

	v.mu.RLock()
	defer v.mu.RUnlock()

	if v.closed {
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInternal,
			Op:   "vector.DeleteVector",
			Err:  &api.ServiceUnavailableError{Service: "vector"},
		}
	}

	if v.raftNode != nil {
		cmd := statemachine.NewDeleteCommand(req.Namespace, req.ID, v.nodeID)
		if err := v.proposeToRaft(ctx, cmd); err != nil {
			if v.metrics != nil {
				v.metrics.operationErrors.With("delete_vector", "raft_error").Inc()
			}
			return nil, err
		}

		v.indexMu.RLock()
		index, exists := v.hnswIndexes[req.Namespace]
		v.indexMu.RUnlock()
		if exists {
			if deleteErr := index.Delete(req.ID); deleteErr != nil {
				v.logger.WithFields(map[string]interface{}{
					"namespace": req.Namespace,
					"id":        req.ID,
					"error":     deleteErr,
				}).Warn("failed to delete vector from local HNSW index")
			}
		}

		if v.metrics != nil {
			v.metrics.vectorsDeleted.Inc()
			v.metrics.vectorCount.With(req.Namespace).Dec()
		}

		v.logger.WithFields(map[string]interface{}{
			"namespace": req.Namespace,
			"id":        req.ID,
		}).Debug("vector deleted successfully via Raft")

		return &DeleteVectorResponse{
			Success:   true,
			Timestamp: time.Now().UnixNano(),
		}, nil
	}

	if err := v.storage.DeleteVector(ctx, req.Namespace, req.ID); err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("delete_vector", "storage_error").Inc()
		}
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrStorage,
			Op:   "vector.DeleteVector",
			Err:  &api.OperationFailedError{Operation: "delete vector", Cause: err},
		}
	}

	v.indexMu.RLock()
	index, exists := v.hnswIndexes[req.Namespace]
	v.indexMu.RUnlock()

	if exists {
		if err := index.Delete(req.ID); err != nil {
			v.logger.WithFields(map[string]interface{}{
				"namespace": req.Namespace,
				"id":        req.ID,
				"error":     err,
			}).Warn("failed to delete vector from HNSW index")
		}
	}

	if v.metrics != nil {
		v.metrics.vectorsDeleted.Inc()
		v.metrics.vectorCount.With(req.Namespace).Dec()
	}

	v.logger.WithFields(map[string]interface{}{
		"namespace": req.Namespace,
		"id":        req.ID,
	}).Debug("vector deleted successfully")

	return &DeleteVectorResponse{
		Success:   true,
		Timestamp: time.Now().UnixNano(),
	}, nil
}

// BatchUpsert inserts or updates multiple vectors atomically.
func (v *vectorService) BatchUpsert(ctx context.Context, req *BatchUpsertRequest) (*BatchUpsertResponse, error) {
	start := time.Now()
	defer func() {
		if v.metrics != nil {
			v.metrics.operationDuration.With("batch_upsert").Observe(time.Since(start).Seconds())
		}
	}()

	if err := v.validateBatchUpsertRequest(req); err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("batch_upsert", "invalid_input").Inc()
		}
		return nil, err
	}

	if err := v.validateTenantAccess(ctx, req.Namespace); err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("batch_upsert", "forbidden").Inc()
		}
		return nil, err
	}

	v.mu.RLock()
	defer v.mu.RUnlock()

	if v.closed {
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInternal,
			Op:   "vector.BatchUpsert",
			Err:  &api.ServiceUnavailableError{Service: "vector"},
		}
	}

	now := time.Now().UnixNano()
	ids := make([]uint64, len(req.Vectors))
	versions := make([]uint64, len(req.Vectors))

	for i, vec := range req.Vectors {
		ids[i] = vec.ID
		versions[i] = 1
	}

	if v.raftNode != nil {
		batchIDs := make([]uint64, len(req.Vectors))
		batchVectors := make([][]float32, len(req.Vectors))
		for i, vec := range req.Vectors {
			batchIDs[i] = vec.ID
			batchVectors[i] = vec.Vector
		}

		cmd := statemachine.NewBatchUpsertCommand(req.Namespace, batchIDs, batchVectors, v.nodeID)
		if err := v.proposeToRaft(ctx, cmd); err != nil {
			if v.metrics != nil {
				v.metrics.operationErrors.With("batch_upsert", "raft_error").Inc()
			}
			return nil, err
		}

		index, err := v.ensureHNSWIndex(ctx, req.Namespace)
		if err != nil {
			v.logger.WithFields(map[string]interface{}{
				"namespace": req.Namespace,
				"error":     err,
			}).Warn("failed to ensure HNSW index after batch upsert")
		} else {
			for i := range req.Vectors {
				if insertErr := index.Insert(batchIDs[i], batchVectors[i]); insertErr != nil {
					v.logger.WithFields(map[string]interface{}{
						"namespace": req.Namespace,
						"id":        batchIDs[i],
						"error":     insertErr,
					}).Warn("failed to insert vector into local HNSW index")
				}
			}
		}

		if v.metrics != nil {
			v.metrics.vectorsUpserted.Add(float64(len(req.Vectors)))
			v.metrics.vectorCount.With(req.Namespace).Add(float64(len(req.Vectors)))
		}

		v.logger.WithFields(map[string]interface{}{
			"namespace": req.Namespace,
			"count":     len(req.Vectors),
		}).Debug("batch upsert completed successfully via Raft")

		return &BatchUpsertResponse{
			IDs:           ids,
			Versions:      versions,
			Timestamp:     now,
			Success:       true,
			InsertedCount: len(req.Vectors),
		}, nil
	}

	vecs := make([]*vector.VectorValue, len(req.Vectors))
	for i, vec := range req.Vectors {
		vecs[i] = &vector.VectorValue{
			ID:        vec.ID,
			Embedding: vec.Vector,
			Metadata:  convertToStringMap(vec.Metadata),
			CreatedAt: now,
			UpdatedAt: now,
			Version:   1,
		}
	}

	if err := v.storage.BatchUpsertVectors(ctx, req.Namespace, vecs); err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("batch_upsert", "storage_error").Inc()
		}
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrStorage,
			Op:   "vector.BatchUpsert",
			Err:  &api.OperationFailedError{Operation: "batch upsert vectors", Cause: err},
		}
	}

	v.indexMu.RLock()
	index, exists := v.hnswIndexes[req.Namespace]
	v.indexMu.RUnlock()

	if exists {
		for _, vec := range req.Vectors {
			if err := index.Insert(vec.ID, vec.Vector); err != nil {
				v.logger.WithFields(map[string]interface{}{
					"namespace": req.Namespace,
					"id":        vec.ID,
					"error":     err,
				}).Warn("failed to insert vector into HNSW index")
			}
		}
	}

	if v.metrics != nil {
		v.metrics.vectorsUpserted.Add(float64(len(req.Vectors)))
		v.metrics.vectorCount.With(req.Namespace).Add(float64(len(req.Vectors)))
	}

	v.logger.WithFields(map[string]interface{}{
		"namespace": req.Namespace,
		"count":     len(req.Vectors),
	}).Debug("batch upsert completed successfully")

	return &BatchUpsertResponse{
		IDs:           ids,
		Versions:      versions,
		Timestamp:     now,
		Success:       true,
		InsertedCount: len(req.Vectors),
	}, nil
}

// =============================================================================
// Search Operations
// =============================================================================

// SearchSimilar performs k-nearest neighbor similarity search.
func (v *vectorService) SearchSimilar(ctx context.Context, req *SearchSimilarRequest) (*SearchSimilarResponse, error) {
	start := time.Now()
	defer func() {
		if v.metrics != nil {
			v.metrics.operationDuration.With("search_similar").Observe(time.Since(start).Seconds())
			v.metrics.searchesPerformed.Inc()
		}
	}()

	if err := v.validateSearchSimilarRequest(req); err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("search_similar", "invalid_input").Inc()
		}
		return nil, err
	}

	if err := v.validateTenantAccess(ctx, req.Namespace); err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("search_similar", "forbidden").Inc()
		}
		return nil, err
	}

	v.mu.RLock()
	defer v.mu.RUnlock()

	if v.closed {
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInternal,
			Op:   "vector.SearchSimilar",
			Err:  &api.ServiceUnavailableError{Service: "vector"},
		}
	}

	index, err := v.ensureHNSWIndex(ctx, req.Namespace)
	if err != nil {
		v.logger.WithFields(map[string]interface{}{
			"namespace": req.Namespace,
			"error":     err,
		}).Debug("namespace not available on this node yet, returning empty results")
		return &SearchSimilarResponse{
			Results:       []*VectorSearchResult{},
			QueryDuration: 0,
			Timestamp:     time.Now().UnixNano(),
		}, nil
	}

	searchStart := time.Now()
	results, err := index.Search(req.QueryVector, req.K)
	if err != nil {
		if errors.Is(err, hnsw.ErrEmptyIndex) {
			return &SearchSimilarResponse{
				Results:       []*VectorSearchResult{},
				QueryDuration: time.Since(searchStart).Nanoseconds(),
				Timestamp:     time.Now().UnixNano(),
			}, nil
		}
		if v.metrics != nil {
			v.metrics.operationErrors.With("search_similar", "search_failed").Inc()
		}
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInternal,
			Op:   "vector.SearchSimilar",
			Err:  &api.OperationFailedError{Operation: "HNSW search", Cause: err},
		}
	}
	queryDuration := time.Since(searchStart).Nanoseconds()

	searchResults := make([]*VectorSearchResult, 0, len(results))
	for i, res := range results {
		if req.MinScore > 0 && res.Distance > req.MinScore {
			continue
		}

		result := &VectorSearchResult{
			ID:       res.ID,
			Score:    res.Distance,
			Distance: res.Distance,
			Rank:     i + 1,
		}

		if req.IncludeVectors || req.IncludeMetadata {
			vec, err := v.storage.GetVector(ctx, req.Namespace, res.ID)
			if err == nil {
				if req.IncludeVectors {
					result.Vector = vec.Embedding
				}
				if req.IncludeMetadata {
					result.Metadata = convertFromStringMap(vec.Metadata)

					if req.MetadataFilter != nil && !matchesMetadataFilter(result.Metadata, req.MetadataFilter) {
						continue
					}
				}
			}
		}

		searchResults = append(searchResults, result)
	}

	return &SearchSimilarResponse{
		Results:       searchResults,
		Timestamp:     time.Now().UnixNano(),
		QueryDuration: queryDuration,
	}, nil
}

// SearchByMetadata performs metadata-based vector search.
func (v *vectorService) SearchByMetadata(ctx context.Context, req *SearchByMetadataRequest) (*SearchByMetadataResponse, error) {
	start := time.Now()
	defer func() {
		if v.metrics != nil {
			v.metrics.operationDuration.With("search_by_metadata").Observe(time.Since(start).Seconds())
		}
	}()

	if err := v.validateSearchByMetadataRequest(req); err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("search_by_metadata", "invalid_input").Inc()
		}
		return nil, err
	}

	if err := v.validateTenantAccess(ctx, req.Namespace); err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("search_by_metadata", "forbidden").Inc()
		}
		return nil, err
	}

	v.mu.RLock()
	defer v.mu.RUnlock()

	if v.closed {
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInternal,
			Op:   "vector.SearchByMetadata",
			Err:  &api.ServiceUnavailableError{Service: "vector"},
		}
	}

	filters := make([]vector.MetadataFilter, 0, len(req.Filters))
	for key, value := range req.Filters {
		filters = append(filters, vector.MetadataFilter{
			Field:    key,
			Value:    fmt.Sprintf("%v", value),
			Operator: vector.FilterEqual,
		})
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	opts := &vector.ScanOptions{
		Limit:  limit,
		Offset: req.Offset,
	}

	vecs, err := v.storage.ScanByMultipleFilters(ctx, req.Namespace, filters, opts)
	if err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("search_by_metadata", "scan_failed").Inc()
		}
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInternal,
			Op:   "vector.SearchByMetadata",
			Err:  &api.OperationFailedError{Operation: "metadata scan", Cause: err},
		}
	}

	results := make([]*VectorData, len(vecs))
	for i, vec := range vecs {
		results[i] = &VectorData{
			ID:        vec.ID,
			Version:   vec.Version,
			CreatedAt: vec.CreatedAt,
			UpdatedAt: vec.UpdatedAt,
		}

		if req.IncludeVectors {
			results[i].Vector = vec.Embedding
		}

		if req.IncludeMetadata {
			results[i].Metadata = convertFromStringMap(vec.Metadata)
		}
	}

	return &SearchByMetadataResponse{
		Results:    results,
		TotalCount: uint64(len(results)),
		Timestamp:  time.Now().UnixNano(),
		NextCursor: "",
	}, nil
}

// HybridSearch combines similarity and metadata filtering.
func (v *vectorService) HybridSearch(ctx context.Context, req *HybridSearchRequest) (*HybridSearchResponse, error) {
	start := time.Now()
	defer func() {
		if v.metrics != nil {
			v.metrics.operationDuration.With("hybrid_search").Observe(time.Since(start).Seconds())
		}
	}()

	similarReq := &SearchSimilarRequest{
		Namespace:       req.Namespace,
		QueryVector:     req.QueryVector,
		K:               req.K * 2,
		EfSearch:        req.EfSearch,
		MinScore:        req.MinScore,
		IncludeVectors:  req.IncludeVectors,
		IncludeMetadata: true,
		ShardID:         req.ShardID,
		Consistency:     req.Consistency,
	}

	similarResp, err := v.SearchSimilar(ctx, similarReq)
	if err != nil {
		return nil, err
	}

	filtered := make([]*VectorSearchResult, 0)
	for _, result := range similarResp.Results {
		if matchesMetadataFilter(result.Metadata, req.MetadataFilter) {
			filtered = append(filtered, result)
			if len(filtered) >= req.K {
				break
			}
		}
	}

	return &HybridSearchResponse{
		Results:         filtered,
		TotalCandidates: len(similarResp.Results),
		TotalFiltered:   len(filtered),
		Timestamp:       time.Now().UnixNano(),
		QueryDuration:   similarResp.QueryDuration,
	}, nil
}

// =============================================================================
// Index Management
// =============================================================================

// RebuildIndex rebuilds the vector index for a namespace.
func (v *vectorService) RebuildIndex(ctx context.Context, req *RebuildIndexRequest) (*RebuildIndexResponse, error) {
	start := time.Now()
	defer func() {
		if v.metrics != nil {
			v.metrics.operationDuration.With("rebuild_index").Observe(time.Since(start).Seconds())
		}
	}()

	if err := v.validateRebuildIndexRequest(req); err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("rebuild_index", "invalid_input").Inc()
		}
		return nil, err
	}

	jobID := uuid.New().String()

	info, err := v.storage.GetNamespaceInfo(ctx, req.Namespace)
	if err != nil {
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrNotFound,
			Op:   "vector.RebuildIndex",
			Err:  &api.OperationFailedError{Operation: "find namespace", Cause: err},
		}
	}

	job := &RebuildJobState{
		JobID:            jobID,
		Namespace:        req.Namespace,
		InProgress:       true,
		VectorsProcessed: 0,
		TotalVectors:     info.VectorCount,
		StartedAt:        time.Now(),
	}

	v.jobMu.Lock()
	v.rebuildJobs[jobID] = job
	v.jobMu.Unlock()

	if req.Async {
		go func() {
			if err := v.performIndexRebuild(ctx, jobID, req.Namespace, req.IndexConfig); err != nil {
				v.logger.WithFields(map[string]interface{}{
					"job_id":    jobID,
					"namespace": req.Namespace,
					"error":     err,
				}).Error("background index rebuild failed")
			}
		}()

		return &RebuildIndexResponse{
			JobID:                  jobID,
			Status:                 "in_progress",
			VectorsProcessed:       0,
			TotalVectors:           info.VectorCount,
			PercentComplete:        0.0,
			EstimatedTimeRemaining: 0,
			Timestamp:              time.Now().UnixNano(),
			Success:                false,
		}, nil
	}

	if err := v.performIndexRebuild(ctx, jobID, req.Namespace, req.IndexConfig); err != nil {
		return nil, err
	}

	job.mu.RLock()
	completed := job.CompletedAt
	job.mu.RUnlock()

	var ts int64
	if completed != nil {
		ts = completed.UnixNano()
	}

	return &RebuildIndexResponse{
		JobID:                  jobID,
		Status:                 "completed",
		VectorsProcessed:       info.VectorCount,
		TotalVectors:           info.VectorCount,
		PercentComplete:        100.0,
		EstimatedTimeRemaining: 0,
		Timestamp:              ts,
		Success:                true,
	}, nil
}

// GetRebuildProgress retrieves the progress of an index rebuild operation.
func (v *vectorService) GetRebuildProgress(ctx context.Context, req *GetRebuildProgressRequest) (*GetRebuildProgressResponse, error) {
	if err := v.validateGetRebuildProgressRequest(req); err != nil {
		return nil, err
	}

	var job *RebuildJobState
	var found bool

	v.jobMu.RLock()
	if req.JobID != "" {
		job, found = v.rebuildJobs[req.JobID]
	} else {
		var latestStartTime time.Time
		for _, j := range v.rebuildJobs {
			j.mu.RLock()
			if j.Namespace == req.Namespace && j.StartedAt.After(latestStartTime) {
				job = j
				latestStartTime = j.StartedAt
				found = true
			}
			j.mu.RUnlock()
		}
	}
	v.jobMu.RUnlock()

	if !found {
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrNotFound,
			Op:   "vector.GetRebuildProgress",
			Err:  &api.NotFoundError{Resource: "rebuild job", ID: ""},
		}
	}

	job.mu.RLock()
	defer job.mu.RUnlock()

	var percentComplete float64
	if job.CompletedAt != nil && job.Error == nil {
		percentComplete = 100.0
	} else if job.TotalVectors > 0 {
		percentComplete = float64(job.VectorsProcessed) / float64(job.TotalVectors) * 100.0
	}

	elapsed := time.Since(job.StartedAt)
	elapsedMs := elapsed.Milliseconds()

	var estimatedRemainingMs int64
	if job.InProgress && job.VectorsProcessed > 0 && job.TotalVectors > job.VectorsProcessed {
		rate := float64(job.VectorsProcessed) / float64(elapsedMs)
		if rate > 0 {
			remaining := job.TotalVectors - job.VectorsProcessed
			estimatedRemainingMs = int64(float64(remaining) / rate)
		}
	}

	var status string
	if job.Error != nil {
		status = "failed"
	} else if job.CompletedAt != nil {
		status = "completed"
	} else if job.InProgress {
		status = "running"
	} else {
		status = "pending"
	}

	var errMsg string
	if job.Error != nil {
		errMsg = job.Error.Error()
	}

	var completedAt int64
	if job.CompletedAt != nil {
		completedAt = job.CompletedAt.Unix()
	}

	return &GetRebuildProgressResponse{
		JobID:                    job.JobID,
		Namespace:                job.Namespace,
		Status:                   status,
		VectorsProcessed:         job.VectorsProcessed,
		TotalVectors:             job.TotalVectors,
		PercentComplete:          percentComplete,
		ElapsedTimeMs:            elapsedMs,
		EstimatedTimeRemainingMs: estimatedRemainingMs,
		StartedAt:                job.StartedAt.Unix(),
		CompletedAt:              completedAt,
		Error:                    errMsg,
	}, nil
}

// performIndexRebuild performs the actual index rebuild operation.
func (v *vectorService) performIndexRebuild(ctx context.Context, jobID, namespace string, indexConfig map[string]string) error {
	v.jobMu.RLock()
	job, exists := v.rebuildJobs[jobID]
	v.jobMu.RUnlock()

	if !exists {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrNotFound,
			Op:   "vector.performIndexRebuild",
			Err:  &api.NotFoundError{Resource: "rebuild job", ID: jobID},
		}
	}

	defer func() {
		job.mu.Lock()
		job.InProgress = false
		now := time.Now()
		job.CompletedAt = &now
		job.mu.Unlock()
	}()

	config := &hnsw.Config{
		M:              16,
		MMax:           32,
		EfConstruction: 200,
		EfSearch:       50,
		DistanceMetric: hnsw.L2Distance,
	}

	if indexConfig != nil {
		if m, ok := indexConfig["m"]; ok {
			if _, err := fmt.Sscanf(m, "%d", &config.M); err != nil {
				v.logger.WithFields(map[string]interface{}{
					"parameter": "m",
					"value":     m,
					"error":     err,
				}).Warn("failed to parse index parameter")
			} else {
				config.MMax = config.M * 2
			}
		}
		if ef, ok := indexConfig["ef_construction"]; ok {
			if _, err := fmt.Sscanf(ef, "%d", &config.EfConstruction); err != nil {
				v.logger.WithFields(map[string]interface{}{
					"parameter": "ef_construction",
					"value":     ef,
					"error":     err,
				}).Warn("failed to parse index parameter")
			}
		}
		if ef, ok := indexConfig["ef_search"]; ok {
			if _, err := fmt.Sscanf(ef, "%d", &config.EfSearch); err != nil {
				v.logger.WithFields(map[string]interface{}{
					"parameter": "ef_search",
					"value":     ef,
					"error":     err,
				}).Warn("failed to parse index parameter")
			}
		}
	}

	newIndex, err := hnsw.NewHNSWIndex(config)
	if err != nil {
		job.mu.Lock()
		job.Error = err
		job.mu.Unlock()
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInternal,
			Op:   "vector.performIndexRebuild",
			Err:  &api.OperationFailedError{Operation: "create new index", Cause: err},
		}
	}

	opts := &vector.ScanOptions{
		Limit: 1000,
	}

	offset := 0
	for {
		opts.Offset = offset
		vecs, err := v.storage.ScanNamespace(ctx, namespace, opts)
		if err != nil {
			job.mu.Lock()
			job.Error = err
			job.mu.Unlock()
			return &qrdbErrors.QRDBError{
				Code: qrdbErrors.ErrInternal,
				Op:   "vector.performIndexRebuild",
				Err:  &api.OperationFailedError{Operation: "scan vectors", Cause: err},
			}
		}

		if len(vecs) == 0 {
			break
		}

		for _, vec := range vecs {
			if err := newIndex.Insert(vec.ID, vec.Embedding); err != nil {
				v.logger.WithFields(map[string]interface{}{
					"namespace": namespace,
					"id":        vec.ID,
					"error":     err,
				}).Warn("failed to insert vector into new index")
			}

			job.mu.Lock()
			job.VectorsProcessed++
			job.mu.Unlock()
		}

		offset += len(vecs)

		if len(vecs) < opts.Limit {
			break
		}
	}

	v.indexMu.Lock()
	v.hnswIndexes[namespace] = hnsw.NewIndexAdapter(newIndex)
	v.indexMu.Unlock()

	v.logger.WithFields(map[string]interface{}{
		"namespace": namespace,
		"job_id":    jobID,
	}).Info("index rebuild completed successfully")

	return nil
}

// GetIndexStats retrieves statistics about the vector index.
func (v *vectorService) GetIndexStats(ctx context.Context, req *GetIndexStatsRequest) (*GetIndexStatsResponse, error) {
	start := time.Now()
	defer func() {
		if v.metrics != nil {
			v.metrics.operationDuration.With("get_index_stats").Observe(time.Since(start).Seconds())
		}
	}()

	if err := v.validateGetIndexStatsRequest(req); err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("get_index_stats", "invalid_input").Inc()
		}
		return nil, err
	}

	v.mu.RLock()
	defer v.mu.RUnlock()

	if v.closed {
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInternal,
			Op:   "vector.GetIndexStats",
			Err:  &api.ServiceUnavailableError{Service: "vector"},
		}
	}

	info, err := v.storage.GetNamespaceInfo(ctx, req.Namespace)
	if err != nil {
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrNotFound,
			Op:   "vector.GetIndexStats",
			Err:  &api.OperationFailedError{Operation: "find namespace", Cause: err},
		}
	}

	v.indexMu.RLock()
	index, exists := v.hnswIndexes[req.Namespace]
	v.indexMu.RUnlock()

	var indexCount int64
	var indexSizeBytes uint64
	var memoryUsageBytes uint64

	if exists {
		indexCount = index.Count()

		stats := index.GetStats()
		if stats != nil && stats.NodeCount > 0 {
			const baseNodeOverhead = 8 + 8 + 24 + 48 // 88 bytes

			vectorDataSize := uint64(stats.Dimensions) * 4 //nolint:gosec // G115: Dimensions is validated positive

			avgConnectionsPerNode := stats.AvgConnections
			maxLayers := stats.MaxLayer + 1
			if maxLayers < 1 {
				maxLayers = 1
			}
			connectionDataSize := uint64(avgConnectionsPerNode * float64(maxLayers) * 8)

			perNodeSize := uint64(baseNodeOverhead) + vectorDataSize + connectionDataSize

			indexSizeBytes = uint64(stats.NodeCount) * perNodeSize // #nosec G115 -- NodeCount is int64 from HNSW stats, always non-negative

			memoryUsageBytes = indexSizeBytes + (indexSizeBytes / 5)
		}
	}

	var vectorCount uint64
	if indexCount < 0 {
		v.logger.WithFields(map[string]interface{}{
			"namespace":   req.Namespace,
			"index_count": indexCount,
		}).Warn("negative index count detected, setting to 0")
		vectorCount = 0
	} else {
		vectorCount = uint64(indexCount)
	}

	return &GetIndexStatsResponse{
		Namespace:        req.Namespace,
		IndexType:        "HNSW",
		VectorCount:      vectorCount,
		Dimension:        info.Dimension,
		IndexSizeBytes:   indexSizeBytes,
		MemoryUsageBytes: memoryUsageBytes,
		IndexConfig:      make(map[string]string),
		SearchPerformance: &IndexPerformanceMetrics{
			AverageSearchTimeMs: 0.0,
			P50SearchTimeMs:     0.0,
			P95SearchTimeMs:     0.0,
			P99SearchTimeMs:     0.0,
			TotalSearches:       0,
			CacheHitRate:        0.0,
		},
		Timestamp: time.Now().UnixNano(),
	}, nil
}

// =============================================================================
// Validation Methods
// =============================================================================

func (v *vectorService) validateCreateNamespaceRequest(req *CreateNamespaceRequest) error {
	if req == nil {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.validateCreateNamespaceRequest",
			Err:  &api.NilInputError{Name: "request"},
		}
	}
	if req.Namespace == "" {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.validateCreateNamespaceRequest",
			Err:  &api.RequiredFieldError{Field: "namespace"},
		}
	}
	if req.Dimension == 0 {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.validateCreateNamespaceRequest",
			Err:  &api.InvalidInputError{Message: "dimension must be greater than 0"},
		}
	}
	return nil
}

func (v *vectorService) validateDeleteNamespaceRequest(req *DeleteNamespaceRequest) error {
	if req == nil {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.validateDeleteNamespaceRequest",
			Err:  &api.NilInputError{Name: "request"},
		}
	}
	if req.Namespace == "" {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.validateDeleteNamespaceRequest",
			Err:  &api.RequiredFieldError{Field: "namespace"},
		}
	}
	return nil
}

func (v *vectorService) validateGetNamespaceInfoRequest(req *GetNamespaceInfoRequest) error {
	if req == nil {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.validateGetNamespaceInfoRequest",
			Err:  &api.NilInputError{Name: "request"},
		}
	}
	if req.Namespace == "" {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.validateGetNamespaceInfoRequest",
			Err:  &api.RequiredFieldError{Field: "namespace"},
		}
	}
	return nil
}

func (v *vectorService) validateUpsertVectorRequest(req *UpsertVectorRequest) error {
	if req == nil {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.validateUpsertVectorRequest",
			Err:  &api.NilInputError{Name: "request"},
		}
	}
	if req.Namespace == "" {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.validateUpsertVectorRequest",
			Err:  &api.RequiredFieldError{Field: "namespace"},
		}
	}
	if req.ID == 0 {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.validateUpsertVectorRequest",
			Err:  &api.InvalidInputError{Message: "ID must be greater than 0"},
		}
	}
	if len(req.Vector) == 0 {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.validateUpsertVectorRequest",
			Err:  &api.RequiredFieldError{Field: "vector"},
		}
	}
	return nil
}

func (v *vectorService) validateGetVectorRequest(req *GetVectorRequest) error {
	if req == nil {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.validateGetVectorRequest",
			Err:  &api.NilInputError{Name: "request"},
		}
	}
	if req.Namespace == "" {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.validateGetVectorRequest",
			Err:  &api.RequiredFieldError{Field: "namespace"},
		}
	}
	if req.ID == 0 {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.validateGetVectorRequest",
			Err:  &api.InvalidInputError{Message: "ID must be greater than 0"},
		}
	}
	return nil
}

func (v *vectorService) validateDeleteVectorRequest(req *DeleteVectorRequest) error {
	if req == nil {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.validateDeleteVectorRequest",
			Err:  &api.NilInputError{Name: "request"},
		}
	}
	if req.Namespace == "" {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.validateDeleteVectorRequest",
			Err:  &api.RequiredFieldError{Field: "namespace"},
		}
	}
	if req.ID == 0 {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.validateDeleteVectorRequest",
			Err:  &api.InvalidInputError{Message: "ID must be greater than 0"},
		}
	}
	return nil
}

func (v *vectorService) validateBatchUpsertRequest(req *BatchUpsertRequest) error {
	if req == nil {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.validateBatchUpsertRequest",
			Err:  &api.NilInputError{Name: "request"},
		}
	}
	if req.Namespace == "" {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.validateBatchUpsertRequest",
			Err:  &api.RequiredFieldError{Field: "namespace"},
		}
	}
	if len(req.Vectors) == 0 {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.validateBatchUpsertRequest",
			Err:  &api.RequiredFieldError{Field: "vectors"},
		}
	}
	return nil
}

func (v *vectorService) validateSearchSimilarRequest(req *SearchSimilarRequest) error {
	if req == nil {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.validateSearchSimilarRequest",
			Err:  &api.NilInputError{Name: "request"},
		}
	}
	if req.Namespace == "" {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.validateSearchSimilarRequest",
			Err:  &api.RequiredFieldError{Field: "namespace"},
		}
	}
	if len(req.QueryVector) == 0 {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.validateSearchSimilarRequest",
			Err:  &api.RequiredFieldError{Field: "query vector"},
		}
	}
	if req.K <= 0 {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.validateSearchSimilarRequest",
			Err:  &api.InvalidInputError{Message: "k must be greater than 0"},
		}
	}
	return nil
}

func (v *vectorService) validateSearchByMetadataRequest(req *SearchByMetadataRequest) error {
	if req == nil {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.validateSearchByMetadataRequest",
			Err:  &api.NilInputError{Name: "request"},
		}
	}
	if req.Namespace == "" {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.validateSearchByMetadataRequest",
			Err:  &api.RequiredFieldError{Field: "namespace"},
		}
	}
	return nil
}

func (v *vectorService) validateRebuildIndexRequest(req *RebuildIndexRequest) error {
	if req == nil {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.validateRebuildIndexRequest",
			Err:  &api.NilInputError{Name: "request"},
		}
	}
	if req.Namespace == "" {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.validateRebuildIndexRequest",
			Err:  &api.RequiredFieldError{Field: "namespace"},
		}
	}
	return nil
}

func (v *vectorService) validateGetRebuildProgressRequest(req *GetRebuildProgressRequest) error {
	if req == nil {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.validateGetRebuildProgressRequest",
			Err:  &api.NilInputError{Name: "request"},
		}
	}
	if req.JobID == "" && req.Namespace == "" {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.validateGetRebuildProgressRequest",
			Err:  &api.InvalidInputError{Message: "job_id or namespace is required"},
		}
	}
	return nil
}

func (v *vectorService) validateGetIndexStatsRequest(req *GetIndexStatsRequest) error {
	if req == nil {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.validateGetIndexStatsRequest",
			Err:  &api.NilInputError{Name: "request"},
		}
	}
	if req.Namespace == "" {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.validateGetIndexStatsRequest",
			Err:  &api.RequiredFieldError{Field: "namespace"},
		}
	}
	return nil
}

// =============================================================================
// Helper Functions
// =============================================================================

// convertToStringMap converts map[string]interface{} to map[string]string.
func convertToStringMap(m map[string]interface{}) map[string]string {
	if m == nil {
		return nil
	}
	result := make(map[string]string, len(m))
	for k, val := range m {
		result[k] = fmt.Sprintf("%v", val)
	}
	return result
}

// convertFromStringMap converts map[string]string to map[string]interface{}.
func convertFromStringMap(m map[string]string) map[string]interface{} {
	if m == nil {
		return nil
	}
	result := make(map[string]interface{}, len(m))
	for k, val := range m {
		result[k] = val
	}
	return result
}

// matchesMetadataFilter checks if metadata matches filter criteria.
func matchesMetadataFilter(metadata, filter map[string]interface{}) bool {
	if len(filter) == 0 {
		return true
	}
	for key, value := range filter {
		metaValue, exists := metadata[key]
		if !exists {
			return false
		}
		if fmt.Sprintf("%v", metaValue) != fmt.Sprintf("%v", value) {
			return false
		}
	}
	return true
}

// =============================================================================
// Compaction Operations
// =============================================================================

// CompactNamespace triggers compaction for a vector namespace.
func (v *vectorService) CompactNamespace(ctx context.Context, req *CompactNamespaceRequest) (*CompactNamespaceResponse, error) {
	start := time.Now()
	defer func() {
		if v.metrics != nil {
			v.metrics.operationDuration.With("compact_namespace").Observe(time.Since(start).Seconds())
		}
	}()

	if err := v.validateCompactNamespaceRequest(req); err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("compact_namespace", "invalid_input").Inc()
		}
		return nil, err
	}

	if err := v.validateTenantAccess(ctx, req.Namespace); err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("compact_namespace", "forbidden").Inc()
		}
		return nil, err
	}

	v.mu.RLock()
	if v.closed {
		v.mu.RUnlock()
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInternal,
			Op:   "vector.CompactNamespace",
			Err:  &api.ServiceUnavailableError{Service: "vector"},
		}
	}
	v.mu.RUnlock()

	info, err := v.storage.GetNamespaceInfo(ctx, req.Namespace)
	if err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("compact_namespace", "not_found").Inc()
		}
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrNotFound,
			Op:   "vector.CompactNamespace",
			Err:  &api.OperationFailedError{Operation: "find namespace", Cause: err},
		}
	}

	operationID := uuid.New().String()

	job := &CompactionJobState{
		OperationID: operationID,
		Namespace:   req.Namespace,
		InProgress:  true,
		TotalBytes:  int64(info.StorageBytes),
		StartedAt:   time.Now(),
	}

	v.jobMu.Lock()
	v.compactionJobs[operationID] = job
	v.jobMu.Unlock()

	v.logger.WithFields(map[string]interface{}{
		"namespace":    req.Namespace,
		"operation_id": operationID,
		"async":        req.Async,
	}).Info("starting namespace compaction")

	if req.Async {
		go func() { // #nosec G118 -- async compaction must outlive request
			if err := v.performCompaction(context.Background(), operationID, req.Namespace); err != nil {
				v.logger.WithFields(map[string]interface{}{
					"operation_id": operationID,
					"namespace":    req.Namespace,
					"error":        err,
				}).Error("background compaction failed")
			}
		}()

		return &CompactNamespaceResponse{
			OperationID: operationID,
			StartedAt:   job.StartedAt.UnixNano(),
			Success:     false,
		}, nil
	}

	if err := v.performCompaction(ctx, operationID, req.Namespace); err != nil {
		return nil, err
	}

	job.mu.RLock()
	bytesCompacted := job.BytesCompacted
	completedAt := job.CompletedAt
	job.mu.RUnlock()

	var completedAtNano *int64
	if completedAt != nil {
		ts := completedAt.UnixNano()
		completedAtNano = &ts
	}

	return &CompactNamespaceResponse{
		OperationID:    operationID,
		StartedAt:      job.StartedAt.UnixNano(),
		CompletedAt:    completedAtNano,
		BytesCompacted: bytesCompacted,
		Success:        true,
	}, nil
}

// performCompaction executes the actual compaction operation.
func (v *vectorService) performCompaction(ctx context.Context, operationID, namespace string) error {
	v.jobMu.RLock()
	job, exists := v.compactionJobs[operationID]
	v.jobMu.RUnlock()

	if !exists {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrNotFound,
			Op:   "vector.performCompaction",
			Err:  &api.NotFoundError{Resource: "compaction job", ID: operationID},
		}
	}

	defer func() {
		job.mu.Lock()
		job.InProgress = false
		now := time.Now()
		job.CompletedAt = &now
		job.mu.Unlock()
	}()

	preStats, _ := v.storage.GetStats(ctx, namespace)
	var preSizeBytes int64
	if preStats != nil {
		preSizeBytes = int64(preStats.SizeBytes) //nolint:gosec // G115: SizeBytes is bounded by storage capacity
	}

	if err := v.storage.Compact(ctx, namespace); err != nil {
		job.mu.Lock()
		job.Error = err
		job.mu.Unlock()
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrStorage,
			Op:   "vector.performCompaction",
			Err:  &api.OperationFailedError{Operation: "compaction", Cause: err},
		}
	}

	postStats, _ := v.storage.GetStats(ctx, namespace)
	var postSizeBytes int64
	if postStats != nil {
		postSizeBytes = int64(postStats.SizeBytes) //nolint:gosec // G115: SizeBytes is bounded by storage capacity
	}

	bytesCompacted := preSizeBytes - postSizeBytes
	if bytesCompacted < 0 {
		bytesCompacted = 0
	}

	job.mu.Lock()
	job.BytesCompacted = bytesCompacted
	job.mu.Unlock()

	v.logger.WithFields(map[string]interface{}{
		"namespace":       namespace,
		"operation_id":    operationID,
		"bytes_compacted": bytesCompacted,
	}).Info("compaction completed successfully")

	return nil
}

// GetCompactionProgress retrieves the progress of a compaction operation.
func (v *vectorService) GetCompactionProgress(ctx context.Context, req *GetCompactionProgressRequest) (*GetCompactionProgressResponse, error) {
	start := time.Now()
	defer func() {
		if v.metrics != nil {
			v.metrics.operationDuration.With("get_compaction_progress").Observe(time.Since(start).Seconds())
		}
	}()

	if err := v.validateGetCompactionProgressRequest(req); err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("get_compaction_progress", "invalid_input").Inc()
		}
		return nil, err
	}

	v.mu.RLock()
	if v.closed {
		v.mu.RUnlock()
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInternal,
			Op:   "vector.GetCompactionProgress",
			Err:  &api.ServiceUnavailableError{Service: "vector"},
		}
	}
	v.mu.RUnlock()

	v.jobMu.RLock()
	defer v.jobMu.RUnlock()

	if req.OperationID != "" {
		job, exists := v.compactionJobs[req.OperationID]
		if !exists {
			return nil, &qrdbErrors.QRDBError{
				Code: qrdbErrors.ErrNotFound,
				Op:   "vector.GetCompactionProgress",
				Err:  &api.NotFoundError{Resource: "compaction operation", ID: req.OperationID},
			}
		}

		job.mu.RLock()
		defer job.mu.RUnlock()

		var completedAt *int64
		if job.CompletedAt != nil {
			ts := job.CompletedAt.UnixNano()
			completedAt = &ts
		}

		var errMsg string
		if job.Error != nil {
			errMsg = job.Error.Error()
		}

		percentComplete := 0.0
		if job.TotalBytes > 0 {
			percentComplete = float64(job.BytesCompacted) / float64(job.TotalBytes) * 100
		}
		if !job.InProgress && job.Error == nil {
			percentComplete = 100.0
		}

		return &GetCompactionProgressResponse{
			OperationID:     job.OperationID,
			Namespace:       job.Namespace,
			BytesCompacted:  job.BytesCompacted,
			TotalBytes:      job.TotalBytes,
			PercentComplete: percentComplete,
			InProgress:      job.InProgress,
			StartedAt:       job.StartedAt.UnixNano(),
			CompletedAt:     completedAt,
			Error:           errMsg,
		}, nil
	}

	var latestJob *CompactionJobState
	for _, job := range v.compactionJobs {
		if job.Namespace == req.Namespace {
			if latestJob == nil || job.StartedAt.After(latestJob.StartedAt) {
				latestJob = job
			}
		}
	}

	if latestJob == nil {
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrNotFound,
			Op:   "vector.GetCompactionProgress",
			Err:  &api.NotFoundError{Resource: "compaction operations for namespace", ID: req.Namespace},
		}
	}

	latestJob.mu.RLock()
	defer latestJob.mu.RUnlock()

	var completedAt *int64
	if latestJob.CompletedAt != nil {
		ts := latestJob.CompletedAt.UnixNano()
		completedAt = &ts
	}

	var errMsg string
	if latestJob.Error != nil {
		errMsg = latestJob.Error.Error()
	}

	percentComplete := 0.0
	if latestJob.TotalBytes > 0 {
		percentComplete = float64(latestJob.BytesCompacted) / float64(latestJob.TotalBytes) * 100
	}
	if !latestJob.InProgress && latestJob.Error == nil {
		percentComplete = 100.0
	}

	return &GetCompactionProgressResponse{
		OperationID:     latestJob.OperationID,
		Namespace:       latestJob.Namespace,
		BytesCompacted:  latestJob.BytesCompacted,
		TotalBytes:      latestJob.TotalBytes,
		PercentComplete: percentComplete,
		InProgress:      latestJob.InProgress,
		StartedAt:       latestJob.StartedAt.UnixNano(),
		CompletedAt:     completedAt,
		Error:           errMsg,
	}, nil
}

// validateCompactNamespaceRequest validates the compact namespace request.
func (v *vectorService) validateCompactNamespaceRequest(req *CompactNamespaceRequest) error {
	if req == nil {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.validateCompactNamespaceRequest",
			Err:  &api.NilInputError{Name: "request"},
		}
	}
	if req.Namespace == "" {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.validateCompactNamespaceRequest",
			Err:  &api.RequiredFieldError{Field: "namespace"},
		}
	}
	return nil
}

// validateGetCompactionProgressRequest validates the get compaction progress request.
func (v *vectorService) validateGetCompactionProgressRequest(req *GetCompactionProgressRequest) error {
	if req == nil {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.validateGetCompactionProgressRequest",
			Err:  &api.NilInputError{Name: "request"},
		}
	}
	if req.OperationID == "" && req.Namespace == "" {
		return &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.validateGetCompactionProgressRequest",
			Err:  &api.InvalidInputError{Message: "either operation_id or namespace is required"},
		}
	}
	return nil
}

// =============================================================================
// Extended Vector Operations
// =============================================================================

// BatchDeleteVectors removes multiple vectors from the namespace.
func (v *vectorService) BatchDeleteVectors(ctx context.Context, req *BatchDeleteVectorsRequest) (*BatchDeleteVectorsResponse, error) {
	start := time.Now()

	if req == nil {
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.BatchDelete",
			Err:  &api.NilInputError{Name: "request"},
		}
	}
	if req.Namespace == "" {
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.BatchDelete",
			Err:  &api.RequiredFieldError{Field: "namespace"},
		}
	}
	if len(req.IDs) == 0 {
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.BatchDelete",
			Err:  &api.EmptyCollectionError{Field: "vector IDs"},
		}
	}

	if err := v.validateTenantAccess(ctx, req.Namespace); err != nil {
		return nil, err
	}

	err := v.storage.BatchDeleteVectors(ctx, req.Namespace, req.IDs)
	if err != nil {
		deletedCount := 0
		var failedIDs []uint64

		for _, id := range req.IDs {
			deleteReq := &DeleteVectorRequest{
				Namespace: req.Namespace,
				ID:        id,
				ShardID:   req.ShardID,
			}

			_, deleteErr := v.DeleteVector(ctx, deleteReq)
			if deleteErr != nil {
				failedIDs = append(failedIDs, id)
			} else {
				deletedCount++
			}
		}

		if v.metrics != nil {
			v.metrics.operationDuration.With("batch_delete").Observe(time.Since(start).Seconds())
		}

		return &BatchDeleteVectorsResponse{
			DeletedCount: deletedCount,
			FailedIDs:    failedIDs,
			Timestamp:    time.Now().UnixNano(),
			Success:      len(failedIDs) == 0,
		}, nil
	}

	if v.metrics != nil {
		v.metrics.operationDuration.With("batch_delete").Observe(time.Since(start).Seconds())
	}

	return &BatchDeleteVectorsResponse{
		DeletedCount: len(req.IDs),
		FailedIDs:    nil,
		Timestamp:    time.Now().UnixNano(),
		Success:      true,
	}, nil
}

// ListVectors returns vectors in a namespace with pagination.
func (v *vectorService) ListVectors(ctx context.Context, req *ListVectorsRequest) (*ListVectorsResponse, error) {
	start := time.Now()

	if req == nil {
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.ListVectors",
			Err:  &api.NilInputError{Name: "request"},
		}
	}
	if req.Namespace == "" {
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.ListVectors",
			Err:  &api.RequiredFieldError{Field: "namespace"},
		}
	}

	if err := v.validateTenantAccess(ctx, req.Namespace); err != nil {
		return nil, err
	}

	namespaceInfo, err := v.storage.GetNamespaceInfo(ctx, req.Namespace)
	if err != nil {
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrNotFound,
			Op:   "vector.ListVectors",
			Err:  &api.OperationFailedError{Operation: "find namespace " + req.Namespace, Cause: err},
		}
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	offset := 0
	if req.Cursor != "" {
		if _, parseErr := fmt.Sscanf(req.Cursor, "%d", &offset); parseErr != nil {
			offset = 0
		}
	}

	scanOpts := &vector.ScanOptions{
		Limit:  limit + 1,
		Offset: offset,
	}
	vectors, err := v.storage.ScanNamespace(ctx, req.Namespace, scanOpts)
	if err != nil {
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInternal,
			Op:   "vector.ListVectors",
			Err:  &api.OperationFailedError{Operation: "list vectors", Cause: err},
		}
	}

	var nextCursor string
	if len(vectors) > limit {
		vectors = vectors[:limit]
		nextCursor = fmt.Sprintf("%d", offset+limit)
	}

	result := make([]*VectorData, len(vectors))
	for i, vec := range vectors {
		var metadata map[string]interface{}
		if req.IncludeMetadata && vec.Metadata != nil {
			metadata = make(map[string]interface{})
			for k, val := range vec.Metadata {
				metadata[k] = val
			}
		}

		var embeddingData []float32
		if req.IncludeVectors {
			embeddingData = vec.Embedding
		}

		result[i] = &VectorData{
			ID:        vec.ID,
			Vector:    embeddingData,
			Metadata:  metadata,
			Version:   vec.Version,
			CreatedAt: vec.CreatedAt,
			UpdatedAt: vec.UpdatedAt,
		}
	}

	if v.metrics != nil {
		v.metrics.operationDuration.With("list_vectors").Observe(time.Since(start).Seconds())
	}

	return &ListVectorsResponse{
		Vectors:    result,
		NextCursor: nextCursor,
		TotalCount: namespaceInfo.VectorCount,
		Timestamp:  time.Now().UnixNano(),
	}, nil
}

// UpdateMetadata updates only the metadata of a vector without modifying the vector values.
func (v *vectorService) UpdateMetadata(ctx context.Context, req *UpdateVectorMetadataRequest) (*UpdateVectorMetadataResponse, error) {
	start := time.Now()

	if req == nil {
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.UpdateMetadata",
			Err:  &api.NilInputError{Name: "request"},
		}
	}
	if req.Namespace == "" {
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.UpdateMetadata",
			Err:  &api.RequiredFieldError{Field: "namespace"},
		}
	}
	if req.ID == 0 {
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.UpdateMetadata",
			Err:  &api.RequiredFieldError{Field: "vector ID"},
		}
	}

	if err := v.validateTenantAccess(ctx, req.Namespace); err != nil {
		return nil, err
	}

	existingVector, err := v.storage.GetVector(ctx, req.Namespace, req.ID)
	if err != nil {
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrNotFound,
			Op:   "vector.UpdateMetadata",
			Err:  &api.OperationFailedError{Operation: fmt.Sprintf("find vector %d in namespace %s", req.ID, req.Namespace), Cause: err},
		}
	}

	var newMetadata map[string]interface{}
	if req.MergeMetadata {
		newMetadata = make(map[string]interface{})
		if existingVector.Metadata != nil {
			for k, val := range existingVector.Metadata {
				newMetadata[k] = val
			}
		}
		for k, val := range req.Metadata {
			newMetadata[k] = val
		}
		for _, key := range req.DeleteKeys {
			delete(newMetadata, key)
		}
	} else {
		newMetadata = req.Metadata
	}

	upsertReq := &UpsertVectorRequest{
		Namespace: req.Namespace,
		ID:        req.ID,
		Vector:    existingVector.Embedding,
		Metadata:  newMetadata,
		ShardID:   req.ShardID,
	}

	resp, err := v.UpsertVector(ctx, upsertReq)
	if err != nil {
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInternal,
			Op:   "vector.UpdateMetadata",
			Err:  &api.OperationFailedError{Operation: "update metadata", Cause: err},
		}
	}

	if v.metrics != nil {
		v.metrics.operationDuration.With("update_metadata").Observe(time.Since(start).Seconds())
	}

	return &UpdateVectorMetadataResponse{
		ID:        req.ID,
		Version:   resp.Version,
		Timestamp: resp.Timestamp,
		Success:   true,
	}, nil
}

// SearchByID finds similar vectors using an existing vector's ID as the query.
func (v *vectorService) SearchByID(ctx context.Context, req *SearchByIDRequest) (*SearchByIDResponse, error) {
	start := time.Now()

	if req == nil {
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.SearchByID",
			Err:  &api.NilInputError{Name: "request"},
		}
	}
	if req.Namespace == "" {
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.SearchByID",
			Err:  &api.RequiredFieldError{Field: "namespace"},
		}
	}
	if req.ID == 0 {
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.SearchByID",
			Err:  &api.RequiredFieldError{Field: "source vector ID"},
		}
	}
	if req.K <= 0 {
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrInvalidInput,
			Op:   "vector.SearchByID",
			Err:  &api.InvalidInputError{Message: "k must be positive"},
		}
	}

	if err := v.validateTenantAccess(ctx, req.Namespace); err != nil {
		return nil, err
	}

	sourceVector, err := v.storage.GetVector(ctx, req.Namespace, req.ID)
	if err != nil {
		return nil, &qrdbErrors.QRDBError{
			Code: qrdbErrors.ErrNotFound,
			Op:   "vector.SearchByID",
			Err:  &api.OperationFailedError{Operation: fmt.Sprintf("find source vector %d in namespace %s", req.ID, req.Namespace), Cause: err},
		}
	}

	k := req.K
	if req.ExcludeSelf {
		k++
	}

	searchReq := &SearchSimilarRequest{
		Namespace:       req.Namespace,
		QueryVector:     sourceVector.Embedding,
		K:               k,
		EfSearch:        req.EfSearch,
		MinScore:        req.MinScore,
		IncludeVectors:  req.IncludeVectors,
		IncludeMetadata: req.IncludeMetadata,
		MetadataFilter:  req.MetadataFilter,
		ShardID:         req.ShardID,
		Consistency:     req.Consistency,
	}

	searchResp, err := v.SearchSimilar(ctx, searchReq)
	if err != nil {
		return nil, err
	}

	var results []*VectorSearchResult
	if req.ExcludeSelf {
		for _, result := range searchResp.Results {
			if result.ID != req.ID {
				results = append(results, result)
			}
		}
		if len(results) > req.K {
			results = results[:req.K]
		}
	} else {
		results = searchResp.Results
	}

	if v.metrics != nil {
		v.metrics.operationDuration.With("search_by_id").Observe(time.Since(start).Seconds())
	}

	return &SearchByIDResponse{
		SourceID:      req.ID,
		Results:       results,
		Timestamp:     time.Now().UnixNano(),
		QueryDuration: searchResp.QueryDuration,
	}, nil
}
