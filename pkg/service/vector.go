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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	api "github.com/jeremyhahn/go-qrdb/pkg/api"
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
	hnswIndexes    map[string]interfaces.HNSWIndex // Map scoped-namespace -> HNSW index
	indexFactory   interfaces.HNSWIndexFactory     // Factory for creating HNSW indexes
	logger         interfaces.Logger
	metrics        *vectorServiceMetrics
	mu             sync.RWMutex
	closed         bool
	indexMu        sync.RWMutex
	rebuildJobs    map[string]*RebuildJobState
	compactionJobs map[string]*CompactionJobState
	jobMu          sync.RWMutex
	strictTenant   bool // reject create when no tenant in context
}

// namespaceSep is the single-byte separator used to join a tenant ID and a
// logical namespace name into a physical storage key. Unit Separator (0x1f)
// cannot appear in a valid namespace name, which follows DNS label rules.
const namespaceSep = "\x1f"

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

// WithStrictTenantIsolation enables strict tenant isolation mode. When true,
// CreateNamespace rejects requests that carry no tenant ID in the context
// (i.e. the caller is operating as the system tenant). This mirrors the
// RBAC WithStrictSeparation pattern and is automatically engaged when the
// process runs under FIPS-140 or when the operator sets
// config.Tenant.StrictIsolation.
func WithStrictTenantIsolation(strict bool) VectorServiceOption {
	return func(v *vectorService) {
		v.strictTenant = strict
	}
}

// =============================================================================
// Tenant Namespace Scoping
// =============================================================================

// scopedNamespace converts a logical namespace name and a tenant ID into a
// physical storage key. The format is:
//
//	tenantID + "\x1f" + logical
//
// The system tenant ("system") and the empty string are both mapped to the
// empty-segment form "\x1f" + logical so that the system namespace is stable
// and distinct from any user-created tenant.
func scopedNamespace(tenantID, logical string) string {
	if tenant.IsSystemTenant(tenantID) {
		return namespaceSep + logical
	}
	return tenantID + namespaceSep + logical
}

// unscopedNamespace splits a physical storage key produced by scopedNamespace
// into its constituent tenant ID and logical name. The system namespace
// ("\x1f" + logical) returns tenant.SystemTenantID as the tenant.
func unscopedNamespace(physical string) (tenantID, logical string) {
	for i := 0; i < len(physical); i++ {
		if physical[i] == namespaceSep[0] {
			prefix := physical[:i]
			if prefix == "" {
				tenantID = tenant.SystemTenantID
			} else {
				tenantID = prefix
			}
			logical = physical[i+1:]
			return
		}
	}
	// No separator found — treat as a legacy unscoped name owned by system.
	return tenant.SystemTenantID, physical
}

// scopedFromContext returns the physical namespace key for the given logical
// namespace name, deriving the tenant from the provided context.
func scopedFromContext(ctx context.Context, logical string) string {
	return scopedNamespace(tenant.FromContext(ctx), logical)
}

// namespaceMetricLabels returns Prometheus label values for the two GaugeVecs
// (vectorCount, indexSizeBytes) keyed by a physical (scoped) namespace name.
// The "namespace" label carries the logical name (control-byte-free, tenant
// prefix stripped) and "tenant" carries the owner tenant ID.
//
// This ensures no \x1f byte or other control character appears in emitted
// label values, and prevents cross-tenant namespace name leakage on /metrics.
func namespaceMetricLabels(physical string) prometheus.Labels {
	tenantID, logical := unscopedNamespace(physical)
	return prometheus.Labels{
		"namespace": logical,
		"tenant":    tenantID,
	}
}

// NewVectorService creates a new vector service implementation.
func NewVectorService(
	storage vector.Storage,
	logger interfaces.Logger,
	opts ...VectorServiceOption,
) (VectorService, error) {
	if storage == nil {
		return nil, &api.RequiredFieldError{Field: "storage"}
	}

	if logger == nil {
		return nil, &api.RequiredFieldError{Field: "logger"}
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
		return nil, &VectorOperationError{Op: "load namespaces", Err: err}
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
			Help:      "Size of HNSW index in bytes, partitioned by logical namespace and tenant",
		}, []string{"namespace", "tenant"}),

		vectorCount: metrics.NewGaugeVec(metrics.GaugeOpts{
			Namespace: "qrdb",
			Subsystem: "vector",
			Name:      "vector_count",
			Help:      "Number of vectors in namespace, partitioned by logical namespace and tenant",
		}, []string{"namespace", "tenant"}),
	}
}

// loadNamespaces loads existing namespaces and initializes HNSW indexes.
// Physical namespace names stored in the backend are already scoped; they are
// used directly as map keys so every index lookup stays consistent.
func (v *vectorService) loadNamespaces() error {
	ctx := context.Background()

	namespaces, err := v.storage.ListNamespaces(ctx)
	if err != nil {
		return err
	}

	v.logger.WithField("count", len(namespaces)).Debug("loading namespaces")

	for _, ns := range namespaces {
		// Initialize HNSW index for namespace. The map key is the physical
		// (scoped) namespace name that the storage layer already holds.
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

		// Update metrics with logical namespace name and tenant label so
		// that no scoped key (containing \x1f) appears in emitted label values.
		if v.metrics != nil {
			v.metrics.vectorCount.WithLabels(namespaceMetricLabels(ns.Name)).Set(float64(ns.VectorCount))
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

// validateTenantCreate enforces strict isolation on namespace creation.
// When strictTenant is enabled and the context carries the system tenant
// (meaning no explicit tenant was bound), the create is rejected.
func (v *vectorService) validateTenantCreate(ctx context.Context) error {
	if !v.strictTenant {
		return nil
	}
	if tenant.IsSystemTenant(tenant.FromContext(ctx)) {
		return &TenantRequiredError{Op: "CreateNamespace"}
	}
	return nil
}

// filterNamespacesByTenant filters a raw storage namespace list to only
// those belonging to the calling tenant. It unscopess physical names and
// returns logical NamespaceInfo values with the storage Name field set to
// the logical (user-visible) name.
func (v *vectorService) filterNamespacesByTenant(ctx context.Context, namespaces []vector.NamespaceInfo) []vector.NamespaceInfo {
	callerTenant := tenant.FromContext(ctx)
	filtered := make([]vector.NamespaceInfo, 0, len(namespaces))
	for _, ns := range namespaces {
		ownerTenant, logical := unscopedNamespace(ns.Name)
		if ownerTenant != callerTenant {
			continue
		}
		// Rewrite the Name field to the logical (user-visible) form.
		copy := ns
		copy.Name = logical
		filtered = append(filtered, copy)
	}
	return filtered
}

// =============================================================================
// Error Translation
// =============================================================================

// vectorStorageTranslation is one row of vectorStorageErrors: a storage
// sentinel and the typed error it becomes.
type vectorStorageTranslation struct {
	sentinel error
	typed    func(namespace string, id uint64, err error) error
}

// vectorStorageErrors maps each vector-storage sentinel onto the service's
// typed error for it, first match wins. An ORDERED slice, not a map: storage
// wraps its sentinels, a wrapped error can carry more than one, and the answer
// must not depend on map iteration order. The closed store comes first — a
// closed store makes every other refusal moot — then the absences, then the
// input refusals.
var vectorStorageErrors = []vectorStorageTranslation{
	{vector.ErrStorageClosed, func(string, uint64, error) error {
		return &VectorServiceClosedError{}
	}},
	{vector.ErrNamespaceNotFound, func(namespace string, _ uint64, _ error) error {
		return &NamespaceNotFoundError{Namespace: namespace}
	}},
	{vector.ErrVectorNotFound, func(namespace string, id uint64, _ error) error {
		return &VectorNotFoundError{Namespace: namespace, ID: strconv.FormatUint(id, 10)}
	}},
	{vector.ErrNamespaceExists, func(namespace string, _ uint64, _ error) error {
		return &NamespaceExistsError{Namespace: namespace}
	}},
	{vector.ErrInvalidNamespace, invalidVectorFromStorage},
	{vector.ErrInvalidID, invalidVectorFromStorage},
	{vector.ErrInvalidVector, invalidVectorFromStorage},
	{vector.ErrEmptyBatch, invalidVectorFromStorage},
	{vector.ErrBatchTooLarge, invalidVectorFromStorage},
}

// invalidVectorFromStorage reports a request the storage layer refused on its
// merits as the caller's error.
func invalidVectorFromStorage(_ string, _ uint64, err error) error {
	return &InvalidVectorError{Reason: err.Error()}
}

// vectorStorageError translates a vector-storage failure into the service's
// typed vocabulary, so every transport can classify it (go-qrdb#331). A
// missing namespace or vector, a duplicate namespace, a closed store and a
// refused input each get their own type; anything else is the server's fault
// and is reported as a VectorOperationError naming op.
//
// namespace is the caller's LOGICAL namespace, never the tenant-scoped storage
// key, so an error message cannot leak another tenant's key layout.
func vectorStorageError(op, namespace string, id uint64, err error) error {
	for _, row := range vectorStorageErrors {
		if errors.Is(err, row.sentinel) {
			return row.typed(namespace, id, err)
		}
	}
	return &VectorOperationError{Op: op, Err: err}
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
		return &EncodeRequestError{Err: err}
	}

	result, err := v.raftNode.SyncPropose(ctx, v.shardID, cmdBytes)
	if err != nil {
		return translateProposeError(err)
	}

	// A proposal that Raft accepted can still have been REFUSED by the state
	// machine. Since go-qrdb#129, an operation the state machine does not
	// implement is reported in Result.Data instead of being returned as an
	// Apply error — returning it tripped the shard's circuit breaker and cost
	// the cluster the shard. That fix means a discarded Result is now a
	// fail-open: the refusal would be read as a successful write, and the
	// caller would go on believing a vector was stored that never was. Every
	// other propose path in this package already checks.
	// interfaces.RaftNode types the result as interface{}, so the concrete
	// sm.Result has to be recovered before the refusal can be decoded.
	//
	// A value of any other dynamic type is refused rather than skipped. Passing
	// it through would mean reporting a stored vector on the strength of a
	// result this code could not read — the same fail-open in a new place, and
	// a harder one to see, since it would depend on which RaftNode
	// implementation happened to be wired in. Not being able to check an
	// outcome is not evidence that the outcome was good.
	smResult, ok := result.(sm.Result)
	if !ok {
		return &VectorOperationError{Op: fmt.Sprintf("raft proposal (result type %T carries no readable state-machine outcome)", result)}
	}
	if resultErr := checkResultError(smResult); resultErr != nil {
		return resultErr
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

	// Enforce strict tenant isolation before acquiring any locks.
	if err := v.validateTenantCreate(ctx); err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("create_namespace", "tenant_required").Inc()
		}
		return nil, err
	}

	// Compute the physical (scoped) namespace key for this tenant.
	scoped := scopedFromContext(ctx, req.Namespace)
	tenantID := tenant.FromContext(ctx)

	v.mu.Lock()
	defer v.mu.Unlock()

	if v.closed {
		return nil, &VectorServiceClosedError{}
	}

	// Check if namespace already exists under this tenant's scoped key.
	existingInfo, err := v.storage.GetNamespaceInfo(ctx, scoped)
	if err == nil && existingInfo != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("create_namespace", "already_exists").Inc()
		}
		return nil, &NamespaceExistsError{Namespace: req.Namespace}
	}

	v.logger.WithFields(map[string]interface{}{
		"namespace": req.Namespace,
		"scoped":    scoped,
		"tenant":    tenantID,
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

		nsMeta := make(map[string]string)
		if req.Metadata != nil {
			for k, val := range req.Metadata {
				nsMeta[k] = fmt.Sprintf("%v", val)
			}
		}

		cmd := statemachine.NewCreateNamespaceCommandFull(&statemachine.NamespaceConfig{
			Namespace:      scoped,
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
			v.metrics.vectorCount.WithLabels(namespaceMetricLabels(scoped)).Set(0)
		}

		v.logger.WithFields(map[string]interface{}{
			"namespace": req.Namespace,
			"tenant":    tenantID,
		}).Info("namespace created successfully via Raft")

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
		Name:           scoped,
		TenantID:       tenantID,
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
			"scoped":    scoped,
			"error":     err,
		}).Error("failed to create namespace")
		return nil, vectorStorageError("create namespace", req.Namespace, 0, err)
	}

	// Initialize HNSW index for namespace keyed by the scoped name.
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
		_ = v.storage.DeleteNamespace(ctx, scoped)
		return nil, &VectorOperationError{Op: "create HNSW index", Err: err}
	}

	v.indexMu.Lock()
	v.hnswIndexes[scoped] = hnsw.NewIndexAdapter(index)
	v.indexMu.Unlock()

	if v.metrics != nil {
		v.metrics.namespacesCreated.Inc()
		v.metrics.vectorCount.WithLabels(namespaceMetricLabels(scoped)).Set(0)
	}

	v.logger.WithFields(map[string]interface{}{
		"namespace": req.Namespace,
		"tenant":    tenantID,
	}).Info("namespace created successfully")

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

	scoped := scopedFromContext(ctx, req.Namespace)

	v.mu.Lock()
	defer v.mu.Unlock()

	if v.closed {
		return nil, &VectorServiceClosedError{}
	}

	v.logger.WithField("namespace", req.Namespace).Debug("deleting namespace")

	info, err := v.storage.GetNamespaceInfo(ctx, scoped)
	var vectorsDeleted uint64
	if err == nil {
		vectorsDeleted = info.VectorCount
	}

	if v.raftNode != nil {
		cmd := statemachine.NewDeleteNamespaceCommand(scoped, v.nodeID)
		if err := v.proposeToRaft(ctx, cmd); err != nil {
			if v.metrics != nil {
				v.metrics.operationErrors.With("delete_namespace", "raft_error").Inc()
			}
			return nil, err
		}

		v.indexMu.Lock()
		delete(v.hnswIndexes, scoped)
		v.indexMu.Unlock()

		if v.metrics != nil {
			v.metrics.namespacesDeleted.Inc()
			v.metrics.vectorCount.WithLabels(namespaceMetricLabels(scoped)).Set(0)
		}

		v.logger.WithField("namespace", req.Namespace).Info("namespace deleted successfully via Raft")

		return &DeleteNamespaceResponse{
			Success:        true,
			VectorsDeleted: vectorsDeleted,
			Timestamp:      time.Now().UnixNano(),
		}, nil
	}

	if err := v.storage.DeleteNamespace(ctx, scoped); err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("delete_namespace", "storage_error").Inc()
		}
		return nil, vectorStorageError("delete namespace", req.Namespace, 0, err)
	}

	v.indexMu.Lock()
	delete(v.hnswIndexes, scoped)
	v.indexMu.Unlock()

	if v.metrics != nil {
		v.metrics.namespacesDeleted.Inc()
		v.metrics.vectorCount.WithLabels(namespaceMetricLabels(scoped)).Set(0)
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
		return nil, &VectorServiceClosedError{}
	}

	namespaces, err := v.storage.ListNamespaces(ctx)
	if err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("list_namespaces", "storage_error").Inc()
		}
		return nil, vectorStorageError("list namespaces", "", 0, err)
	}

	// filterNamespacesByTenant returns only the caller's namespaces with
	// logical (unscoped) names in the Name field.
	tenantFiltered := v.filterNamespacesByTenant(ctx, namespaces)

	filtered := make([]*NamespaceInfo, 0)
	for _, ns := range tenantFiltered {
		// ns.Name is already the logical name after filterNamespacesByTenant.
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

	scoped := scopedFromContext(ctx, req.Namespace)

	v.mu.RLock()
	defer v.mu.RUnlock()

	if v.closed {
		return nil, &VectorServiceClosedError{}
	}

	info, err := v.storage.GetNamespaceInfo(ctx, scoped)
	if err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("get_namespace_info", "not_found").Inc()
		}
		return nil, vectorStorageError("find namespace", req.Namespace, 0, err)
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
		Namespace:      req.Namespace, // logical name, not scoped storage key
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

	scoped := scopedFromContext(ctx, req.Namespace)

	v.mu.RLock()
	defer v.mu.RUnlock()

	if v.closed {
		return nil, &VectorServiceClosedError{}
	}

	nsInfo, err := v.storage.GetNamespaceInfo(ctx, scoped)
	if err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("upsert_vector", "namespace_not_found").Inc()
		}
		return nil, vectorStorageError("find namespace", req.Namespace, 0, err)
	}

	// #nosec G115 -- vector dimension bounded by practical limits
	if nsInfo.Dimension > 0 && uint32(len(req.Vector)) != nsInfo.Dimension {
		if v.metrics != nil {
			v.metrics.operationErrors.With("upsert_vector", "invalid_dimension").Inc()
		}
		return nil, &DimensionMismatchError{Expected: int(nsInfo.Dimension), Actual: len(req.Vector)}
	}

	now := time.Now().UnixNano()

	if v.raftNode != nil {
		cmd := statemachine.NewUpsertCommand(scoped, req.ID, req.Vector, req.Metadata, v.nodeID)
		if err := v.proposeToRaft(ctx, cmd); err != nil {
			if v.metrics != nil {
				v.metrics.operationErrors.With("upsert_vector", "raft_error").Inc()
			}
			return nil, err
		}

		index, err := v.ensureHNSWIndex(ctx, scoped)
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
			v.metrics.vectorCount.WithLabels(namespaceMetricLabels(scoped)).Inc()
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

	if err := v.storage.UpsertVector(ctx, scoped, vecValue); err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("upsert_vector", "storage_error").Inc()
		}
		return nil, vectorStorageError("upsert vector", req.Namespace, req.ID, err)
	}

	v.indexMu.RLock()
	index, exists := v.hnswIndexes[scoped]
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
		v.metrics.vectorCount.WithLabels(namespaceMetricLabels(scoped)).Inc()
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

	scoped := scopedFromContext(ctx, req.Namespace)

	v.mu.RLock()
	defer v.mu.RUnlock()

	if v.closed {
		return nil, &VectorServiceClosedError{}
	}

	vecValue, err := v.storage.GetVector(ctx, scoped, req.ID)
	if err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("get_vector", "not_found").Inc()
		}
		return nil, vectorStorageError("find vector", req.Namespace, req.ID, err)
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

	scoped := scopedFromContext(ctx, req.Namespace)

	v.mu.RLock()
	defer v.mu.RUnlock()

	if v.closed {
		return nil, &VectorServiceClosedError{}
	}

	if v.raftNode != nil {
		cmd := statemachine.NewDeleteCommand(scoped, req.ID, v.nodeID)
		if err := v.proposeToRaft(ctx, cmd); err != nil {
			if v.metrics != nil {
				v.metrics.operationErrors.With("delete_vector", "raft_error").Inc()
			}
			return nil, err
		}

		v.indexMu.RLock()
		index, exists := v.hnswIndexes[scoped]
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
			v.metrics.vectorCount.WithLabels(namespaceMetricLabels(scoped)).Dec()
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

	if err := v.storage.DeleteVector(ctx, scoped, req.ID); err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("delete_vector", "storage_error").Inc()
		}
		return nil, vectorStorageError("delete vector", req.Namespace, req.ID, err)
	}

	v.indexMu.RLock()
	index, exists := v.hnswIndexes[scoped]
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
		v.metrics.vectorCount.WithLabels(namespaceMetricLabels(scoped)).Dec()
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

	scoped := scopedFromContext(ctx, req.Namespace)

	v.mu.RLock()
	defer v.mu.RUnlock()

	if v.closed {
		return nil, &VectorServiceClosedError{}
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

		cmd := statemachine.NewBatchUpsertCommand(scoped, batchIDs, batchVectors, v.nodeID)
		if err := v.proposeToRaft(ctx, cmd); err != nil {
			if v.metrics != nil {
				v.metrics.operationErrors.With("batch_upsert", "raft_error").Inc()
			}
			return nil, err
		}

		index, err := v.ensureHNSWIndex(ctx, scoped)
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
			v.metrics.vectorCount.WithLabels(namespaceMetricLabels(scoped)).Add(float64(len(req.Vectors)))
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

	if err := v.storage.BatchUpsertVectors(ctx, scoped, vecs); err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("batch_upsert", "storage_error").Inc()
		}
		return nil, vectorStorageError("batch upsert vectors", req.Namespace, 0, err)
	}

	v.indexMu.RLock()
	index, exists := v.hnswIndexes[scoped]
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
		v.metrics.vectorCount.WithLabels(namespaceMetricLabels(scoped)).Add(float64(len(req.Vectors)))
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

	scoped := scopedFromContext(ctx, req.Namespace)

	v.mu.RLock()
	defer v.mu.RUnlock()

	if v.closed {
		return nil, &VectorServiceClosedError{}
	}

	index, err := v.ensureHNSWIndex(ctx, scoped)
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
		return nil, &VectorOperationError{Op: "HNSW search", Err: err}
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
			vec, err := v.storage.GetVector(ctx, scoped, res.ID)
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

	scoped := scopedFromContext(ctx, req.Namespace)

	v.mu.RLock()
	defer v.mu.RUnlock()

	if v.closed {
		return nil, &VectorServiceClosedError{}
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

	vecs, err := v.storage.ScanByMultipleFilters(ctx, scoped, filters, opts)
	if err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("search_by_metadata", "scan_failed").Inc()
		}
		return nil, vectorStorageError("metadata scan", req.Namespace, 0, err)
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
	scoped := scopedFromContext(ctx, req.Namespace)

	info, err := v.storage.GetNamespaceInfo(ctx, scoped)
	if err != nil {
		return nil, vectorStorageError("find namespace", req.Namespace, 0, err)
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
			if err := v.performIndexRebuild(ctx, jobID, scoped, req.IndexConfig); err != nil {
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

	if err := v.performIndexRebuild(ctx, jobID, scoped, req.IndexConfig); err != nil {
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
		id := req.JobID
		if id == "" {
			id = req.Namespace
		}
		return nil, &VectorJobNotFoundError{Job: "rebuild", ID: id}
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
	switch {
	case job.Error != nil:
		status = "failed"
	case job.CompletedAt != nil:
		status = "completed"
	case job.InProgress:
		status = "running"
	default:
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
		return &VectorJobNotFoundError{Job: "rebuild", ID: jobID}
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
		return &VectorOperationError{Op: "create new index", Err: err}
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
			return &VectorOperationError{Op: "scan vectors", Err: err}
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
		return nil, &VectorServiceClosedError{}
	}

	scoped := scopedFromContext(ctx, req.Namespace)

	info, err := v.storage.GetNamespaceInfo(ctx, scoped)
	if err != nil {
		return nil, vectorStorageError("find namespace", req.Namespace, 0, err)
	}

	v.indexMu.RLock()
	index, exists := v.hnswIndexes[scoped]
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

// maxNamespaceNameLen is the maximum byte length for a namespace name.
// DNS label rules cap component length at 63; we allow slightly more (255)
// to accommodate hierarchical logical names while still bounding storage key
// sizes without truncation.
const maxNamespaceNameLen = 255

// validateNamespaceName rejects namespace names that are empty, exceed the
// maximum length, or contain any byte below 0x20 (control characters) or the
// unit-separator byte (0x1f) used as the scoped-key separator. The check
// applies a conservative allowlist approach: only printable, non-control bytes
// are accepted. Callers use this helper before constructing scoped storage keys.
func validateNamespaceName(name string) error {
	if name == "" {
		return &InvalidNamespaceNameError{Name: name, Reason: "name must not be empty"}
	}
	if len(name) > maxNamespaceNameLen {
		return &InvalidNamespaceNameError{
			Name:   name,
			Reason: fmt.Sprintf("name exceeds maximum length of %d bytes", maxNamespaceNameLen),
		}
	}
	// Reject control characters (< 0x20) and the explicit separator byte (0x1f).
	// 0x1f (unit separator) is used as namespaceSep and must never appear in a
	// user-supplied name to prevent key-prefix collisions across tenants.
	for _, b := range []byte(name) {
		if b < 0x20 {
			return &InvalidNamespaceNameError{
				Name:   name,
				Reason: fmt.Sprintf("name contains illegal control character 0x%02x", b),
			}
		}
	}
	// Additionally reject a name that is only whitespace.
	if strings.TrimSpace(name) == "" {
		return &InvalidNamespaceNameError{Name: name, Reason: "name must not be blank"}
	}
	return nil
}

func (v *vectorService) validateCreateNamespaceRequest(req *CreateNamespaceRequest) error {
	if req == nil {
		return &InvalidVectorError{Reason: "request cannot be nil"}
	}
	if req.Namespace == "" {
		return &InvalidVectorError{Reason: "namespace is required"}
	}
	if err := validateNamespaceName(req.Namespace); err != nil {
		return err
	}
	if req.Dimension == 0 {
		return &InvalidVectorError{Reason: "dimension must be greater than 0"}
	}
	return nil
}

func (v *vectorService) validateDeleteNamespaceRequest(req *DeleteNamespaceRequest) error {
	if req == nil {
		return &InvalidVectorError{Reason: "request cannot be nil"}
	}
	if req.Namespace == "" {
		return &InvalidVectorError{Reason: "namespace is required"}
	}
	return nil
}

func (v *vectorService) validateGetNamespaceInfoRequest(req *GetNamespaceInfoRequest) error {
	if req == nil {
		return &InvalidVectorError{Reason: "request cannot be nil"}
	}
	if req.Namespace == "" {
		return &InvalidVectorError{Reason: "namespace is required"}
	}
	return nil
}

func (v *vectorService) validateUpsertVectorRequest(req *UpsertVectorRequest) error {
	if req == nil {
		return &InvalidVectorError{Reason: "request cannot be nil"}
	}
	if req.Namespace == "" {
		return &InvalidVectorError{Reason: "namespace is required"}
	}
	if req.ID == 0 {
		return &InvalidVectorError{Reason: "ID must be greater than 0"}
	}
	if len(req.Vector) == 0 {
		return &InvalidVectorError{Reason: "vector is required"}
	}
	return nil
}

func (v *vectorService) validateGetVectorRequest(req *GetVectorRequest) error {
	if req == nil {
		return &InvalidVectorError{Reason: "request cannot be nil"}
	}
	if req.Namespace == "" {
		return &InvalidVectorError{Reason: "namespace is required"}
	}
	if req.ID == 0 {
		return &InvalidVectorError{Reason: "ID must be greater than 0"}
	}
	return nil
}

func (v *vectorService) validateDeleteVectorRequest(req *DeleteVectorRequest) error {
	if req == nil {
		return &InvalidVectorError{Reason: "request cannot be nil"}
	}
	if req.Namespace == "" {
		return &InvalidVectorError{Reason: "namespace is required"}
	}
	if req.ID == 0 {
		return &InvalidVectorError{Reason: "ID must be greater than 0"}
	}
	return nil
}

func (v *vectorService) validateBatchUpsertRequest(req *BatchUpsertRequest) error {
	if req == nil {
		return &InvalidVectorError{Reason: "request cannot be nil"}
	}
	if req.Namespace == "" {
		return &InvalidVectorError{Reason: "namespace is required"}
	}
	if len(req.Vectors) == 0 {
		return &InvalidVectorError{Reason: "vectors is required"}
	}
	return nil
}

func (v *vectorService) validateSearchSimilarRequest(req *SearchSimilarRequest) error {
	if req == nil {
		return &InvalidVectorError{Reason: "request cannot be nil"}
	}
	if req.Namespace == "" {
		return &InvalidVectorError{Reason: "namespace is required"}
	}
	if len(req.QueryVector) == 0 {
		return &InvalidVectorError{Reason: "query vector is required"}
	}
	if req.K <= 0 {
		return &InvalidVectorError{Reason: "k must be greater than 0"}
	}
	return nil
}

func (v *vectorService) validateSearchByMetadataRequest(req *SearchByMetadataRequest) error {
	if req == nil {
		return &InvalidVectorError{Reason: "request cannot be nil"}
	}
	if req.Namespace == "" {
		return &InvalidVectorError{Reason: "namespace is required"}
	}
	return nil
}

func (v *vectorService) validateRebuildIndexRequest(req *RebuildIndexRequest) error {
	if req == nil {
		return &InvalidVectorError{Reason: "request cannot be nil"}
	}
	if req.Namespace == "" {
		return &InvalidVectorError{Reason: "namespace is required"}
	}
	return nil
}

func (v *vectorService) validateGetRebuildProgressRequest(req *GetRebuildProgressRequest) error {
	if req == nil {
		return &InvalidVectorError{Reason: "request cannot be nil"}
	}
	if req.JobID == "" && req.Namespace == "" {
		return &InvalidVectorError{Reason: "job_id or namespace is required"}
	}
	return nil
}

func (v *vectorService) validateGetIndexStatsRequest(req *GetIndexStatsRequest) error {
	if req == nil {
		return &InvalidVectorError{Reason: "request cannot be nil"}
	}
	if req.Namespace == "" {
		return &InvalidVectorError{Reason: "namespace is required"}
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

	scoped := scopedFromContext(ctx, req.Namespace)

	v.mu.RLock()
	if v.closed {
		v.mu.RUnlock()
		return nil, &VectorServiceClosedError{}
	}
	v.mu.RUnlock()

	info, err := v.storage.GetNamespaceInfo(ctx, scoped)
	if err != nil {
		if v.metrics != nil {
			v.metrics.operationErrors.With("compact_namespace", "not_found").Inc()
		}
		return nil, vectorStorageError("find namespace", req.Namespace, 0, err)
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
			if err := v.performCompaction(context.Background(), operationID, scoped); err != nil {
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

	if err := v.performCompaction(ctx, operationID, scoped); err != nil {
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
		return &VectorJobNotFoundError{Job: "compaction", ID: operationID}
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
		return &VectorOperationError{Op: "compaction", Err: err}
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
		return nil, &VectorServiceClosedError{}
	}
	v.mu.RUnlock()

	v.jobMu.RLock()
	defer v.jobMu.RUnlock()

	if req.OperationID != "" {
		job, exists := v.compactionJobs[req.OperationID]
		if !exists {
			return nil, &VectorJobNotFoundError{Job: "compaction", ID: req.OperationID}
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
		return nil, &VectorJobNotFoundError{Job: "compaction", ID: req.Namespace}
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
		return &InvalidVectorError{Reason: "request cannot be nil"}
	}
	if req.Namespace == "" {
		return &InvalidVectorError{Reason: "namespace is required"}
	}
	return nil
}

// validateGetCompactionProgressRequest validates the get compaction progress request.
func (v *vectorService) validateGetCompactionProgressRequest(req *GetCompactionProgressRequest) error {
	if req == nil {
		return &InvalidVectorError{Reason: "request cannot be nil"}
	}
	if req.OperationID == "" && req.Namespace == "" {
		return &InvalidVectorError{Reason: "either operation_id or namespace is required"}
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
		return nil, &InvalidVectorError{Reason: "request cannot be nil"}
	}
	if req.Namespace == "" {
		return nil, &InvalidVectorError{Reason: "namespace is required"}
	}
	if len(req.IDs) == 0 {
		return nil, &InvalidVectorError{Reason: "vector IDs must not be empty"}
	}

	scoped := scopedFromContext(ctx, req.Namespace)

	err := v.storage.BatchDeleteVectors(ctx, scoped, req.IDs)
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
		return nil, &InvalidVectorError{Reason: "request cannot be nil"}
	}
	if req.Namespace == "" {
		return nil, &InvalidVectorError{Reason: "namespace is required"}
	}

	scoped := scopedFromContext(ctx, req.Namespace)

	namespaceInfo, err := v.storage.GetNamespaceInfo(ctx, scoped)
	if err != nil {
		return nil, vectorStorageError("find namespace", req.Namespace, 0, err)
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
	vectors, err := v.storage.ScanNamespace(ctx, scoped, scanOpts)
	if err != nil {
		return nil, vectorStorageError("list vectors", req.Namespace, 0, err)
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
		return nil, &InvalidVectorError{Reason: "request cannot be nil"}
	}
	if req.Namespace == "" {
		return nil, &InvalidVectorError{Reason: "namespace is required"}
	}
	if req.ID == 0 {
		return nil, &InvalidVectorError{Reason: "vector ID is required"}
	}

	scoped := scopedFromContext(ctx, req.Namespace)

	existingVector, err := v.storage.GetVector(ctx, scoped, req.ID)
	if err != nil {
		return nil, vectorStorageError("find vector", req.Namespace, req.ID, err)
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
		return nil, err
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
		return nil, &InvalidVectorError{Reason: "request cannot be nil"}
	}
	if req.Namespace == "" {
		return nil, &InvalidVectorError{Reason: "namespace is required"}
	}
	if req.ID == 0 {
		return nil, &InvalidVectorError{Reason: "source vector ID is required"}
	}
	if req.K <= 0 {
		return nil, &InvalidVectorError{Reason: "k must be positive"}
	}

	scoped := scopedFromContext(ctx, req.Namespace)

	sourceVector, err := v.storage.GetVector(ctx, scoped, req.ID)
	if err != nil {
		return nil, vectorStorageError("find source vector", req.Namespace, req.ID, err)
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
