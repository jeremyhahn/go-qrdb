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

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dragonErrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/interfaces"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
)

// =============================================================================
// Mock Vector Storage for Unit Testing
// =============================================================================

// vecMockStorage implements vector.Storage for testing vector service.
type vecMockStorage struct {
	upsertVectorFn       func(ctx context.Context, namespace string, vec *vector.VectorValue) error
	getVectorFn          func(ctx context.Context, namespace string, id uint64) (*vector.VectorValue, error)
	deleteVectorFn       func(ctx context.Context, namespace string, id uint64) error
	batchUpsertVectorsFn func(ctx context.Context, namespace string, vecs []*vector.VectorValue) error
	batchDeleteVectorsFn func(ctx context.Context, namespace string, ids []uint64) error
	scanByMetadataFn     func(ctx context.Context, namespace, field, value string, opts *vector.ScanOptions) ([]*vector.VectorValue, error)
	scanByMultiFiltersFn func(ctx context.Context, namespace string, filters []vector.MetadataFilter, opts *vector.ScanOptions) ([]*vector.VectorValue, error)
	scanNamespaceFn      func(ctx context.Context, namespace string, opts *vector.ScanOptions) ([]*vector.VectorValue, error)
	createNamespaceFn    func(ctx context.Context, info *vector.NamespaceInfo) error
	deleteNamespaceFn    func(ctx context.Context, namespace string) error
	listNamespacesFn     func(ctx context.Context) ([]vector.NamespaceInfo, error)
	getNamespaceInfoFn   func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error)
	getStatsFn           func(ctx context.Context, namespace string) (*vector.NamespaceStats, error)
	nextIDFn             func(namespace string) (uint64, error)
	batchAllocateIDsFn   func(namespace string, count int) ([]uint64, error)
	compactFn            func(ctx context.Context, namespace string) error
	cleanupTombstonesFn  func(ctx context.Context, olderThan time.Duration) error
	closeFn              func() error
}

func newVecMockStorage() *vecMockStorage {
	return &vecMockStorage{}
}

func (m *vecMockStorage) UpsertVector(ctx context.Context, namespace string, vec *vector.VectorValue) error {
	if m.upsertVectorFn != nil {
		return m.upsertVectorFn(ctx, namespace, vec)
	}
	return nil
}

func (m *vecMockStorage) GetVector(ctx context.Context, namespace string, id uint64) (*vector.VectorValue, error) {
	if m.getVectorFn != nil {
		return m.getVectorFn(ctx, namespace, id)
	}
	return nil, errors.New("not found")
}

func (m *vecMockStorage) DeleteVector(ctx context.Context, namespace string, id uint64) error {
	if m.deleteVectorFn != nil {
		return m.deleteVectorFn(ctx, namespace, id)
	}
	return nil
}

func (m *vecMockStorage) BatchUpsertVectors(ctx context.Context, namespace string, vecs []*vector.VectorValue) error {
	if m.batchUpsertVectorsFn != nil {
		return m.batchUpsertVectorsFn(ctx, namespace, vecs)
	}
	return nil
}

func (m *vecMockStorage) BatchDeleteVectors(ctx context.Context, namespace string, ids []uint64) error {
	if m.batchDeleteVectorsFn != nil {
		return m.batchDeleteVectorsFn(ctx, namespace, ids)
	}
	return nil
}

func (m *vecMockStorage) ScanByMetadata(ctx context.Context, namespace, field, value string, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
	if m.scanByMetadataFn != nil {
		return m.scanByMetadataFn(ctx, namespace, field, value, opts)
	}
	return nil, nil
}

func (m *vecMockStorage) ScanByMultipleFilters(ctx context.Context, namespace string, filters []vector.MetadataFilter, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
	if m.scanByMultiFiltersFn != nil {
		return m.scanByMultiFiltersFn(ctx, namespace, filters, opts)
	}
	return nil, nil
}

func (m *vecMockStorage) ScanNamespace(ctx context.Context, namespace string, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
	if m.scanNamespaceFn != nil {
		return m.scanNamespaceFn(ctx, namespace, opts)
	}
	return nil, nil
}

func (m *vecMockStorage) CreateNamespace(ctx context.Context, info *vector.NamespaceInfo) error {
	if m.createNamespaceFn != nil {
		return m.createNamespaceFn(ctx, info)
	}
	return nil
}

func (m *vecMockStorage) DeleteNamespace(ctx context.Context, namespace string) error {
	if m.deleteNamespaceFn != nil {
		return m.deleteNamespaceFn(ctx, namespace)
	}
	return nil
}

func (m *vecMockStorage) ListNamespaces(ctx context.Context) ([]vector.NamespaceInfo, error) {
	if m.listNamespacesFn != nil {
		return m.listNamespacesFn(ctx)
	}
	return []vector.NamespaceInfo{}, nil
}

func (m *vecMockStorage) GetNamespaceInfo(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
	if m.getNamespaceInfoFn != nil {
		return m.getNamespaceInfoFn(ctx, namespace)
	}
	return nil, errors.New("namespace not found")
}

func (m *vecMockStorage) GetStats(ctx context.Context, namespace string) (*vector.NamespaceStats, error) {
	if m.getStatsFn != nil {
		return m.getStatsFn(ctx, namespace)
	}
	return &vector.NamespaceStats{}, nil
}

func (m *vecMockStorage) NextID(namespace string) (uint64, error) {
	if m.nextIDFn != nil {
		return m.nextIDFn(namespace)
	}
	return 1, nil
}

func (m *vecMockStorage) BatchAllocateIDs(namespace string, count int) ([]uint64, error) {
	if m.batchAllocateIDsFn != nil {
		return m.batchAllocateIDsFn(namespace, count)
	}
	ids := make([]uint64, count)
	for i := range ids {
		ids[i] = uint64(i + 1)
	}
	return ids, nil
}

func (m *vecMockStorage) Compact(ctx context.Context, namespace string) error {
	if m.compactFn != nil {
		return m.compactFn(ctx, namespace)
	}
	return nil
}

func (m *vecMockStorage) CleanupTombstones(ctx context.Context, olderThan time.Duration) error {
	if m.cleanupTombstonesFn != nil {
		return m.cleanupTombstonesFn(ctx, olderThan)
	}
	return nil
}

func (m *vecMockStorage) Close() error {
	if m.closeFn != nil {
		return m.closeFn()
	}
	return nil
}

// =============================================================================
// Mock Vector Logger (unique name to avoid collision)
// =============================================================================

type vecTestLogger struct{}

func newVecTestLogger() *vecTestLogger                                              { return &vecTestLogger{} }
func (l *vecTestLogger) Debug(msg string, args ...any)                              {}
func (l *vecTestLogger) Debugf(format string, args ...interface{})                  {}
func (l *vecTestLogger) Info(msg string, args ...any)                               {}
func (l *vecTestLogger) Infof(format string, args ...interface{})                   {}
func (l *vecTestLogger) Warn(msg string, args ...any)                               {}
func (l *vecTestLogger) Warnf(format string, args ...interface{})                   {}
func (l *vecTestLogger) Error(msg string, args ...any)                              {}
func (l *vecTestLogger) Errorf(format string, args ...interface{})                  {}
func (l *vecTestLogger) Fatal(msg string)                                           {}
func (l *vecTestLogger) Fatalf(format string, args ...interface{})                  {}
func (l *vecTestLogger) WithField(key string, value interface{}) interfaces.Logger  { return l }
func (l *vecTestLogger) WithFields(fields map[string]interface{}) interfaces.Logger { return l }
func (l *vecTestLogger) WithError(err error) interfaces.Logger                      { return l }

// =============================================================================
// NewVectorService Construction Tests
// =============================================================================

func TestNewVectorService_LoadNamespacesError(t *testing.T) {
	storage := newVecMockStorage()
	storage.listNamespacesFn = func(ctx context.Context) ([]vector.NamespaceInfo, error) {
		return nil, errors.New("storage unavailable")
	}
	logger := newVecTestLogger()

	svc, err := NewVectorService(storage, logger)

	require.Error(t, err)
	assert.Nil(t, svc)

	var dragonErr *dragonErrors.DragonError
	require.True(t, errors.As(err, &dragonErr))
	assert.Equal(t, dragonErrors.ErrInternal, dragonErr.Code)
	assert.Contains(t, err.Error(), "load namespaces failed")
}

// =============================================================================
// Functional Options Tests
// =============================================================================

func TestWithRaftNode_Option(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()
	// reuse existing mockRaftNode from dragondb_test.go
	raftNode := &mockRaftNode{}

	svc, err := NewVectorService(storage, logger, WithRaftNode(raftNode))

	require.NoError(t, err)
	require.NotNil(t, svc)
}

func TestWithVectorShardID_Option(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	svc, err := NewVectorService(storage, logger, WithVectorShardID(42))

	require.NoError(t, err)
	require.NotNil(t, svc)
}

func TestWithNodeID_Option(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	svc, err := NewVectorService(storage, logger, WithNodeID("node-123"))

	require.NoError(t, err)
	require.NotNil(t, svc)
}

func TestNewVectorService_AllOptions(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()
	raftNode := &mockRaftNode{}

	svc, err := NewVectorService(
		storage,
		logger,
		WithRaftNode(raftNode),
		WithVectorShardID(3),
		WithNodeID("node-456"),
	)

	require.NoError(t, err)
	require.NotNil(t, svc)
}

// =============================================================================
// Namespace Operation Tests - Unique Tests Only
// =============================================================================

func TestVectorService_CreateNamespace_WithIndexType(t *testing.T) {
	storage := newVecMockStorage()
	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return nil, errors.New("not found")
	}
	logger := newVecTestLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &CreateNamespaceRequest{
		Namespace: "test-ns",
		Dimension: 128,
		IndexType: "FLAT",
	}

	// With FLAT index type, may succeed or fall through to HNSW creation
	// The test validates the code path reaches past validation
	resp, err := svc.CreateNamespace(context.Background(), req)
	if err != nil {
		// Expected: HNSW stub fails without vector build tag
		var dragonErr *dragonErrors.DragonError
		require.True(t, errors.As(err, &dragonErr))
	} else {
		require.NotNil(t, resp)
		assert.Equal(t, "test-ns", resp.Namespace)
	}
}

func TestVectorService_CreateNamespace_EmptyName(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &CreateNamespaceRequest{
		Namespace: "",
		Dimension: 128,
	}

	resp, err := svc.CreateNamespace(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)

	var dragonErr *dragonErrors.DragonError
	require.True(t, errors.As(err, &dragonErr))
	assert.Equal(t, dragonErrors.ErrInvalidInput, dragonErr.Code)
}

func TestVectorService_CreateNamespace_AlreadyExists(t *testing.T) {
	storage := newVecMockStorage()
	storage.getNamespaceInfoFn = func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
		return &vector.NamespaceInfo{
			Name:      namespace,
			Dimension: 128,
		}, nil
	}
	logger := newVecTestLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &CreateNamespaceRequest{
		Namespace: "existing-ns",
		Dimension: 128,
	}

	resp, err := svc.CreateNamespace(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)

	var dragonErr *dragonErrors.DragonError
	require.True(t, errors.As(err, &dragonErr))
	assert.Equal(t, dragonErrors.ErrAlreadyExists, dragonErr.Code)
}

func TestVectorService_DeleteNamespace_EmptyNamespace(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &DeleteNamespaceRequest{
		Namespace: "",
	}

	resp, err := svc.DeleteNamespace(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVectorService_ListNamespaces_EmptyRequest(t *testing.T) {
	storage := newVecMockStorage()
	calls := 0
	storage.listNamespacesFn = func(ctx context.Context) ([]vector.NamespaceInfo, error) {
		calls++
		return []vector.NamespaceInfo{}, nil
	}
	logger := newVecTestLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	resp, err := svc.ListNamespaces(context.Background(), &ListNamespacesRequest{})

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Empty(t, resp.Namespaces)
}

func TestVectorService_ListNamespaces_StorageError(t *testing.T) {
	storage := newVecMockStorage()
	calls := 0
	storage.listNamespacesFn = func(ctx context.Context) ([]vector.NamespaceInfo, error) {
		calls++
		if calls == 1 {
			return []vector.NamespaceInfo{}, nil
		}
		return nil, errors.New("storage unavailable")
	}
	logger := newVecTestLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	resp, err := svc.ListNamespaces(context.Background(), &ListNamespacesRequest{})

	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVectorService_GetNamespaceInfo_EmptyNamespace(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	resp, err := svc.GetNamespaceInfo(context.Background(), &GetNamespaceInfoRequest{
		Namespace: "",
	})

	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVectorService_UpsertVector_EmptyNamespace(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &UpsertVectorRequest{
		Namespace: "",
		Vector:    []float32{0.1, 0.2, 0.3},
	}

	resp, err := svc.UpsertVector(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
}

// =============================================================================
// GetVector Tests - Unique Tests Only
// =============================================================================

func TestVectorService_GetVector_EmptyNamespace(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &GetVectorRequest{
		Namespace: "",
		ID:        1,
	}

	resp, err := svc.GetVector(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
}

// =============================================================================
// DeleteVector Tests - Unique Tests Only
// =============================================================================

func TestVectorService_DeleteVector_EmptyNamespace(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &DeleteVectorRequest{
		Namespace: "",
		ID:        1,
	}

	resp, err := svc.DeleteVector(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
}

// =============================================================================
// Search Validation Tests - Unique Tests Only
// =============================================================================

func TestVectorService_SearchSimilar_EmptyNamespace(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &SearchSimilarRequest{
		Namespace:   "",
		QueryVector: []float32{0.1, 0.2},
		K:           10,
	}

	resp, err := svc.SearchSimilar(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVectorService_SearchSimilar_EmptyVector(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &SearchSimilarRequest{
		Namespace:   "test-ns",
		QueryVector: []float32{},
		K:           10,
	}

	resp, err := svc.SearchSimilar(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVectorService_SearchSimilar_ZeroTopK(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &SearchSimilarRequest{
		Namespace:   "test-ns",
		QueryVector: []float32{0.1, 0.2},
		K:           0,
	}

	resp, err := svc.SearchSimilar(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
}

// =============================================================================
// Batch Operation Validation Tests - Unique Tests Only
// =============================================================================

func TestVectorService_BatchUpsert_EmptyNamespace(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &BatchUpsertRequest{
		Namespace: "",
		Vectors: []*VectorData{
			{Vector: []float32{0.1, 0.2}},
		},
	}

	resp, err := svc.BatchUpsert(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVectorService_SearchByMetadata_EmptyNamespace(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &SearchByMetadataRequest{
		Namespace: "",
		Filters:   map[string]interface{}{"key": "value"},
	}

	resp, err := svc.SearchByMetadata(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVectorService_SearchByMetadata_NoFilters(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &SearchByMetadataRequest{
		Namespace: "test-ns",
		Filters:   map[string]interface{}{},
	}

	resp, err := svc.SearchByMetadata(context.Background(), req)

	// Empty filters returns empty results, not an error
	require.NoError(t, err)
	require.NotNil(t, resp)
}

// =============================================================================
// Index and Compaction Validation Tests - Unique Tests Only
// =============================================================================

func TestVectorService_RebuildIndex_EmptyNamespace(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &RebuildIndexRequest{
		Namespace: "",
	}

	resp, err := svc.RebuildIndex(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVectorService_GetRebuildProgress_EmptyJobID(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &GetRebuildProgressRequest{
		JobID: "",
	}

	resp, err := svc.GetRebuildProgress(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVectorService_GetIndexStats_EmptyNamespace(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &GetIndexStatsRequest{
		Namespace: "",
	}

	resp, err := svc.GetIndexStats(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVectorService_BatchDeleteVectors_EmptyNamespace(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &BatchDeleteVectorsRequest{
		Namespace: "",
		IDs:       []uint64{1, 2},
	}

	resp, err := svc.BatchDeleteVectors(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVectorService_BatchDeleteVectors_EmptyIDs(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &BatchDeleteVectorsRequest{
		Namespace: "test-ns",
		IDs:       []uint64{},
	}

	resp, err := svc.BatchDeleteVectors(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVectorService_ListVectors_EmptyNamespace(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &ListVectorsRequest{
		Namespace: "",
	}

	resp, err := svc.ListVectors(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVectorService_UpdateMetadata_EmptyNamespace(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &UpdateVectorMetadataRequest{
		Namespace: "",
		ID:        1,
		Metadata:  map[string]interface{}{"key": "value"},
	}

	resp, err := svc.UpdateMetadata(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVectorService_SearchByID_EmptyNamespace(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &SearchByIDRequest{
		Namespace: "",
		ID:        1,
		K:         10,
	}

	resp, err := svc.SearchByID(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVectorService_HybridSearch_EmptyRequest(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &HybridSearchRequest{
		Namespace:   "test-ns",
		QueryVector: []float32{0.1, 0.2},
		K:           0,
	}

	resp, err := svc.HybridSearch(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVectorService_HybridSearch_EmptyNamespace(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &HybridSearchRequest{
		Namespace:   "",
		QueryVector: []float32{0.1, 0.2},
		K:           10,
	}

	resp, err := svc.HybridSearch(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVectorService_GetCompactionProgress_EmptyOperationID(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &GetCompactionProgressRequest{
		OperationID: "",
	}

	resp, err := svc.GetCompactionProgress(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
}

// =============================================================================
// RebuildJobState and CompactionJobState Struct Tests
// =============================================================================

func TestRebuildJobState_Fields(t *testing.T) {
	now := time.Now()
	completed := now.Add(time.Minute)

	state := &RebuildJobState{
		JobID:            "rebuild-123",
		Namespace:        "test-ns",
		InProgress:       true,
		VectorsProcessed: 500,
		TotalVectors:     1000,
		StartedAt:        now,
		CompletedAt:      &completed,
		Error:            nil,
	}

	assert.Equal(t, "rebuild-123", state.JobID)
	assert.Equal(t, "test-ns", state.Namespace)
	assert.True(t, state.InProgress)
	assert.Equal(t, uint64(500), state.VectorsProcessed)
	assert.Equal(t, uint64(1000), state.TotalVectors)
}

func TestCompactionJobState_Fields(t *testing.T) {
	now := time.Now()

	state := &CompactionJobState{
		OperationID:    "compact-456",
		Namespace:      "test-ns",
		InProgress:     false,
		BytesCompacted: 1024,
		TotalBytes:     2048,
		StartedAt:      now,
		Error:          errors.New("compaction failed"),
	}

	assert.Equal(t, "compact-456", state.OperationID)
	assert.False(t, state.InProgress)
	assert.Equal(t, int64(1024), state.BytesCompacted)
	assert.Error(t, state.Error)
}
