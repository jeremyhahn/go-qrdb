//go:build qrdb_vector

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

// Package service provides unit tests for the VectorService implementation.
package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// testScopedNS returns the physical (scoped) storage key for a logical
// namespace name under the system tenant. Tests that seed mock storage
// directly (bypassing the service) must use this to produce the key the
// service will look up.
func testScopedNS(logical string) string {
	return scopedNamespace(tenant.SystemTenantID, logical)
}

// =============================================================================
// Mock Vector Storage for Unit Testing
// =============================================================================

// mockVectorStorage implements vector.Storage for testing.
type mockVectorStorage struct {
	mu         sync.RWMutex
	namespaces map[string]*mockNamespaceData
	vectors    map[string]map[uint64]*vector.VectorValue // namespace -> id -> vector
	idCounters map[string]uint64                         // namespace -> next ID

	// Function overrides for testing specific scenarios
	createNamespaceFn       func(ctx context.Context, info *vector.NamespaceInfo) error
	deleteNamespaceFn       func(ctx context.Context, namespace string) error
	listNamespacesFn        func(ctx context.Context) ([]vector.NamespaceInfo, error)
	getNamespaceInfoFn      func(ctx context.Context, namespace string) (*vector.NamespaceInfo, error)
	upsertVectorFn          func(ctx context.Context, namespace string, vec *vector.VectorValue) error
	getVectorFn             func(ctx context.Context, namespace string, id uint64) (*vector.VectorValue, error)
	deleteVectorFn          func(ctx context.Context, namespace string, id uint64) error
	batchUpsertVectorsFn    func(ctx context.Context, namespace string, vecs []*vector.VectorValue) error
	scanByMultipleFiltersFn func(ctx context.Context, namespace string, filters []vector.MetadataFilter, opts *vector.ScanOptions) ([]*vector.VectorValue, error)
	scanNamespaceFn         func(ctx context.Context, namespace string, opts *vector.ScanOptions) ([]*vector.VectorValue, error)
	compactFn               func(ctx context.Context, namespace string) error
	getStatsFn              func(ctx context.Context, namespace string) (*vector.NamespaceStats, error)
}

type mockNamespaceData struct {
	info         *vector.NamespaceInfo
	vectorCount  uint64
	totalInserts uint64
	totalDeletes uint64
}

func newMockVectorStorage() *mockVectorStorage {
	return &mockVectorStorage{
		namespaces: make(map[string]*mockNamespaceData),
		vectors:    make(map[string]map[uint64]*vector.VectorValue),
		idCounters: make(map[string]uint64),
	}
}

func (m *mockVectorStorage) CreateNamespace(ctx context.Context, info *vector.NamespaceInfo) error {
	if m.createNamespaceFn != nil {
		return m.createNamespaceFn(ctx, info)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.namespaces[info.Name]; exists {
		return vector.ErrNamespaceExists
	}

	m.namespaces[info.Name] = &mockNamespaceData{
		info: &vector.NamespaceInfo{
			Name:        info.Name,
			VectorCount: 0,
			Dimension:   info.Dimension,
			CreatedAt:   time.Now().UnixNano(),
			UpdatedAt:   time.Now().UnixNano(),
		},
	}
	m.vectors[info.Name] = make(map[uint64]*vector.VectorValue)
	m.idCounters[info.Name] = 0

	return nil
}

func (m *mockVectorStorage) DeleteNamespace(ctx context.Context, namespace string) error {
	if m.deleteNamespaceFn != nil {
		return m.deleteNamespaceFn(ctx, namespace)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.namespaces[namespace]; !exists {
		return vector.ErrNamespaceNotFound
	}

	delete(m.namespaces, namespace)
	delete(m.vectors, namespace)
	delete(m.idCounters, namespace)

	return nil
}

func (m *mockVectorStorage) ListNamespaces(ctx context.Context) ([]vector.NamespaceInfo, error) {
	if m.listNamespacesFn != nil {
		return m.listNamespacesFn(ctx)
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]vector.NamespaceInfo, 0, len(m.namespaces))
	for _, nsData := range m.namespaces {
		result = append(result, *nsData.info)
	}

	return result, nil
}

func (m *mockVectorStorage) GetNamespaceInfo(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
	if m.getNamespaceInfoFn != nil {
		return m.getNamespaceInfoFn(ctx, namespace)
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	if nsData, exists := m.namespaces[namespace]; exists {
		info := *nsData.info
		info.VectorCount = uint64(len(m.vectors[namespace]))
		return &info, nil
	}

	return nil, vector.ErrNamespaceNotFound
}

func (m *mockVectorStorage) UpsertVector(ctx context.Context, namespace string, vec *vector.VectorValue) error {
	if m.upsertVectorFn != nil {
		return m.upsertVectorFn(ctx, namespace, vec)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.namespaces[namespace]; !exists {
		return vector.ErrNamespaceNotFound
	}

	m.vectors[namespace][vec.ID] = vec
	m.namespaces[namespace].vectorCount++
	m.namespaces[namespace].totalInserts++

	return nil
}

func (m *mockVectorStorage) GetVector(ctx context.Context, namespace string, id uint64) (*vector.VectorValue, error) {
	if m.getVectorFn != nil {
		return m.getVectorFn(ctx, namespace, id)
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	if _, exists := m.namespaces[namespace]; !exists {
		return nil, vector.ErrNamespaceNotFound
	}

	if vec, exists := m.vectors[namespace][id]; exists {
		return vec, nil
	}

	return nil, vector.ErrVectorNotFound
}

func (m *mockVectorStorage) DeleteVector(ctx context.Context, namespace string, id uint64) error {
	if m.deleteVectorFn != nil {
		return m.deleteVectorFn(ctx, namespace, id)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.namespaces[namespace]; !exists {
		return vector.ErrNamespaceNotFound
	}

	if _, exists := m.vectors[namespace][id]; !exists {
		return vector.ErrVectorNotFound
	}

	delete(m.vectors[namespace], id)
	m.namespaces[namespace].vectorCount--
	m.namespaces[namespace].totalDeletes++

	return nil
}

func (m *mockVectorStorage) BatchUpsertVectors(ctx context.Context, namespace string, vecs []*vector.VectorValue) error {
	if m.batchUpsertVectorsFn != nil {
		return m.batchUpsertVectorsFn(ctx, namespace, vecs)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.namespaces[namespace]; !exists {
		return vector.ErrNamespaceNotFound
	}

	for _, vec := range vecs {
		m.vectors[namespace][vec.ID] = vec
		m.namespaces[namespace].vectorCount++
		m.namespaces[namespace].totalInserts++
	}

	return nil
}

func (m *mockVectorStorage) BatchDeleteVectors(ctx context.Context, namespace string, ids []uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.namespaces[namespace]; !exists {
		return vector.ErrNamespaceNotFound
	}

	for _, id := range ids {
		if _, exists := m.vectors[namespace][id]; exists {
			delete(m.vectors[namespace], id)
			m.namespaces[namespace].vectorCount--
			m.namespaces[namespace].totalDeletes++
		}
	}

	return nil
}

func (m *mockVectorStorage) ScanByMetadata(ctx context.Context, namespace, field, value string, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if _, exists := m.namespaces[namespace]; !exists {
		return nil, vector.ErrNamespaceNotFound
	}

	result := make([]*vector.VectorValue, 0)
	for _, vec := range m.vectors[namespace] {
		if metaVal, ok := vec.Metadata[field]; ok && metaVal == value {
			result = append(result, vec)
		}
	}

	return result, nil
}

func (m *mockVectorStorage) ScanByMultipleFilters(ctx context.Context, namespace string, filters []vector.MetadataFilter, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
	if m.scanByMultipleFiltersFn != nil {
		return m.scanByMultipleFiltersFn(ctx, namespace, filters, opts)
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	if _, exists := m.namespaces[namespace]; !exists {
		return nil, vector.ErrNamespaceNotFound
	}

	result := make([]*vector.VectorValue, 0)
	for _, vec := range m.vectors[namespace] {
		matches := true
		for _, filter := range filters {
			metaVal, ok := vec.Metadata[filter.Field]
			if !ok || metaVal != filter.Value {
				matches = false
				break
			}
		}
		if matches {
			result = append(result, vec)
		}
	}

	// Apply pagination
	if opts != nil {
		start := opts.Offset
		if start > len(result) {
			return []*vector.VectorValue{}, nil
		}
		end := start + opts.Limit
		if end > len(result) {
			end = len(result)
		}
		result = result[start:end]
	}

	return result, nil
}

func (m *mockVectorStorage) ScanNamespace(ctx context.Context, namespace string, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
	if m.scanNamespaceFn != nil {
		return m.scanNamespaceFn(ctx, namespace, opts)
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	if _, exists := m.namespaces[namespace]; !exists {
		return nil, vector.ErrNamespaceNotFound
	}

	result := make([]*vector.VectorValue, 0, len(m.vectors[namespace]))
	for _, vec := range m.vectors[namespace] {
		result = append(result, vec)
	}

	// Apply pagination
	if opts != nil {
		start := opts.Offset
		if start > len(result) {
			return []*vector.VectorValue{}, nil
		}
		end := start + opts.Limit
		if end > len(result) {
			end = len(result)
		}
		result = result[start:end]
	}

	return result, nil
}

func (m *mockVectorStorage) GetStats(ctx context.Context, namespace string) (*vector.NamespaceStats, error) {
	if m.getStatsFn != nil {
		return m.getStatsFn(ctx, namespace)
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	if nsData, exists := m.namespaces[namespace]; exists {
		return &vector.NamespaceStats{
			Namespace:    namespace,
			Count:        uint64(len(m.vectors[namespace])),
			SizeBytes:    1024, // Default mock size
			TotalInserts: nsData.totalInserts,
			TotalDeletes: nsData.totalDeletes,
		}, nil
	}

	return nil, vector.ErrNamespaceNotFound
}

func (m *mockVectorStorage) NextID(namespace string) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.namespaces[namespace]; !exists {
		return 0, vector.ErrNamespaceNotFound
	}

	m.idCounters[namespace]++
	return m.idCounters[namespace], nil
}

func (m *mockVectorStorage) BatchAllocateIDs(namespace string, count int) ([]uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.namespaces[namespace]; !exists {
		return nil, vector.ErrNamespaceNotFound
	}

	result := make([]uint64, count)
	for i := 0; i < count; i++ {
		m.idCounters[namespace]++
		result[i] = m.idCounters[namespace]
	}

	return result, nil
}

func (m *mockVectorStorage) Compact(ctx context.Context, namespace string) error {
	if m.compactFn != nil {
		return m.compactFn(ctx, namespace)
	}
	return nil
}

func (m *mockVectorStorage) CleanupTombstones(ctx context.Context, olderThan time.Duration) error {
	return nil
}

func (m *mockVectorStorage) Close() error {
	return nil
}

// =============================================================================
// CreateNamespace Tests
// =============================================================================

func TestVectorService_CreateNamespace_Success(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)
	require.NotNil(t, svc)

	req := &CreateNamespaceRequest{
		Namespace: "test-namespace",
		Dimension: 128,
		IndexConfig: map[string]string{
			"m":               "16",
			"ef_construction": "200",
		},
		Metadata: map[string]interface{}{"desc": "test"},
		ShardID:  1,
	}

	resp, err := svc.CreateNamespace(context.Background(), req)

	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, "test-namespace", resp.Namespace)
	assert.Equal(t, uint32(128), resp.Dimension)
	assert.True(t, resp.Success)
	assert.NotZero(t, resp.Timestamp)
}

func TestVectorService_CreateNamespace_NilRequest(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	resp, err := svc.CreateNamespace(context.Background(), nil)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "request cannot be nil")
}

func TestVectorService_CreateNamespace_MissingNamespace(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &CreateNamespaceRequest{
		Namespace: "",
		Dimension: 128,
	}

	resp, err := svc.CreateNamespace(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "namespace is required")
}

func TestVectorService_CreateNamespace_ZeroDimension(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &CreateNamespaceRequest{
		Namespace: "test-namespace",
		Dimension: 0,
	}

	resp, err := svc.CreateNamespace(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "dimension must be greater than 0")
}

func TestVectorService_CreateNamespace_StorageError(t *testing.T) {
	storage := newMockVectorStorage()
	storage.createNamespaceFn = func(ctx context.Context, info *vector.NamespaceInfo) error {
		return errors.New("storage backend failure")
	}
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &CreateNamespaceRequest{
		Namespace: "test-namespace",
		Dimension: 128,
	}

	resp, err := svc.CreateNamespace(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
	requireErrorAs[*VectorOperationError](t, err)
}

// =============================================================================
// DeleteNamespace Tests
// =============================================================================

func TestVectorService_DeleteNamespace_Success(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	// Create namespace first
	require.NoError(t, storage.CreateNamespace(context.Background(), &vector.NamespaceInfo{
		Name:      testScopedNS("test-namespace"),
		Dimension: 128,
	}))

	req := &DeleteNamespaceRequest{
		Namespace: "test-namespace",
		ShardID:   1,
	}

	resp, err := svc.DeleteNamespace(context.Background(), req)

	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.True(t, resp.Success)
	assert.NotZero(t, resp.Timestamp)
}

func TestVectorService_DeleteNamespace_NilRequest(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	resp, err := svc.DeleteNamespace(context.Background(), nil)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "request cannot be nil")
}

func TestVectorService_DeleteNamespace_MissingNamespace(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &DeleteNamespaceRequest{
		Namespace: "",
	}

	resp, err := svc.DeleteNamespace(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "namespace is required")
}

func TestVectorService_DeleteNamespace_NotFound(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &DeleteNamespaceRequest{
		Namespace: "nonexistent-namespace",
	}

	resp, err := svc.DeleteNamespace(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
}

// =============================================================================
// ListNamespaces Tests
// =============================================================================

func TestVectorService_ListNamespaces_Success(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	// Create multiple namespaces
	for i := 0; i < 5; i++ {
		require.NoError(t, storage.CreateNamespace(context.Background(), &vector.NamespaceInfo{
			Name:      "namespace-" + string(rune('a'+i)),
			Dimension: 128,
		}))
	}

	req := &ListNamespacesRequest{
		Limit: 10,
	}

	resp, err := svc.ListNamespaces(context.Background(), req)

	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, 5, resp.TotalCount)
	assert.Len(t, resp.Namespaces, 5)
	assert.NotZero(t, resp.Timestamp)
}

func TestVectorService_ListNamespaces_WithPrefix(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	// Create namespaces with different prefixes
	require.NoError(t, storage.CreateNamespace(context.Background(), &vector.NamespaceInfo{
		Name:      "prod-namespace-1",
		Dimension: 128,
	}))
	require.NoError(t, storage.CreateNamespace(context.Background(), &vector.NamespaceInfo{
		Name:      "prod-namespace-2",
		Dimension: 128,
	}))
	require.NoError(t, storage.CreateNamespace(context.Background(), &vector.NamespaceInfo{
		Name:      "dev-namespace-1",
		Dimension: 128,
	}))

	req := &ListNamespacesRequest{
		Prefix: "prod",
		Limit:  10,
	}

	resp, err := svc.ListNamespaces(context.Background(), req)

	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, 2, resp.TotalCount)
	assert.Len(t, resp.Namespaces, 2)
}

func TestVectorService_ListNamespaces_Empty(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &ListNamespacesRequest{}

	resp, err := svc.ListNamespaces(context.Background(), req)

	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, 0, resp.TotalCount)
	assert.Empty(t, resp.Namespaces)
}

func TestVectorService_ListNamespaces_WithPagination(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	// Create 10 namespaces
	for i := 0; i < 10; i++ {
		_ = storage.CreateNamespace(context.Background(), &vector.NamespaceInfo{
			Name:      "namespace-" + string(rune('a'+i)),
			Dimension: 128,
		})
	}

	req := &ListNamespacesRequest{
		Limit:  5,
		Offset: 0,
	}

	resp, err := svc.ListNamespaces(context.Background(), req)

	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, 10, resp.TotalCount)
	assert.Len(t, resp.Namespaces, 5)
}

// =============================================================================
// GetNamespaceInfo Tests
// =============================================================================

func TestVectorService_GetNamespaceInfo_Success(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	_ = storage.CreateNamespace(context.Background(), &vector.NamespaceInfo{
		Name:      testScopedNS("test-namespace"),
		Dimension: 128,
	})

	req := &GetNamespaceInfoRequest{
		Namespace: "test-namespace",
		ShardID:   1,
	}

	resp, err := svc.GetNamespaceInfo(context.Background(), req)

	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, "test-namespace", resp.Namespace)
	assert.Equal(t, uint32(128), resp.Dimension)
	assert.Equal(t, "HNSW", resp.IndexType)
}

func TestVectorService_GetNamespaceInfo_NilRequest(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	resp, err := svc.GetNamespaceInfo(context.Background(), nil)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "request cannot be nil")
}

func TestVectorService_GetNamespaceInfo_MissingNamespace(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &GetNamespaceInfoRequest{
		Namespace: "",
	}

	resp, err := svc.GetNamespaceInfo(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "namespace is required")
}

func TestVectorService_GetNamespaceInfo_NotFound(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &GetNamespaceInfoRequest{
		Namespace: "nonexistent-namespace",
	}

	resp, err := svc.GetNamespaceInfo(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
	requireErrorAs[*NamespaceNotFoundError](t, err)
}

// =============================================================================
// UpsertVector Tests
// =============================================================================

func TestVectorService_UpsertVector_Success(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	// Create namespace first
	_ = storage.CreateNamespace(context.Background(), &vector.NamespaceInfo{
		Name:      testScopedNS("test-namespace"),
		Dimension: 3,
	})

	req := &UpsertVectorRequest{
		Namespace: "test-namespace",
		ID:        1,
		Vector:    []float32{1.0, 2.0, 3.0},
		Metadata:  map[string]interface{}{"type": "test"},
		ShardID:   1,
	}

	resp, err := svc.UpsertVector(context.Background(), req)

	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, uint64(1), resp.ID)
	assert.Equal(t, uint64(1), resp.Version)
	assert.True(t, resp.Success)
	assert.NotZero(t, resp.Timestamp)
}

func TestVectorService_UpsertVector_NilRequest(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	resp, err := svc.UpsertVector(context.Background(), nil)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "request cannot be nil")
}

func TestVectorService_UpsertVector_MissingNamespace(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &UpsertVectorRequest{
		Namespace: "",
		ID:        1,
		Vector:    []float32{1.0, 2.0, 3.0},
	}

	resp, err := svc.UpsertVector(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "namespace is required")
}

func TestVectorService_UpsertVector_ZeroID(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &UpsertVectorRequest{
		Namespace: "test-namespace",
		ID:        0,
		Vector:    []float32{1.0, 2.0, 3.0},
	}

	resp, err := svc.UpsertVector(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "ID must be greater than 0")
}

func TestVectorService_UpsertVector_EmptyVector(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &UpsertVectorRequest{
		Namespace: "test-namespace",
		ID:        1,
		Vector:    []float32{},
	}

	resp, err := svc.UpsertVector(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "vector is required")
}

func TestVectorService_UpsertVector_NamespaceNotFound(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &UpsertVectorRequest{
		Namespace: "nonexistent-namespace",
		ID:        1,
		Vector:    []float32{1.0, 2.0, 3.0},
	}

	resp, err := svc.UpsertVector(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
	requireErrorAs[*NamespaceNotFoundError](t, err)
}

// =============================================================================
// GetVector Tests
// =============================================================================

func TestVectorService_GetVector_Success(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	// Create namespace and insert vector
	require.NoError(t, storage.CreateNamespace(context.Background(), &vector.NamespaceInfo{
		Name:      testScopedNS("test-namespace"),
		Dimension: 3,
	}))
	require.NoError(t, storage.UpsertVector(context.Background(), testScopedNS("test-namespace"), &vector.VectorValue{
		ID:        1,
		Embedding: []float32{1.0, 2.0, 3.0},
		Metadata:  map[string]string{"type": "test"},
		Version:   1,
	}))

	req := &GetVectorRequest{
		Namespace:       "test-namespace",
		ID:              1,
		IncludeVector:   true,
		IncludeMetadata: true,
		ShardID:         1,
	}

	resp, err := svc.GetVector(context.Background(), req)

	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, uint64(1), resp.ID)
	assert.True(t, resp.Found)
	assert.NotNil(t, resp.Vector)
	assert.NotNil(t, resp.Metadata)
}

func TestVectorService_GetVector_NilRequest(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	resp, err := svc.GetVector(context.Background(), nil)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "request cannot be nil")
}

func TestVectorService_GetVector_MissingNamespace(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &GetVectorRequest{
		Namespace: "",
		ID:        1,
	}

	resp, err := svc.GetVector(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "namespace is required")
}

func TestVectorService_GetVector_ZeroID(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &GetVectorRequest{
		Namespace: "test-namespace",
		ID:        0,
	}

	resp, err := svc.GetVector(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "ID must be greater than 0")
}

func TestVectorService_GetVector_NotFound(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	_ = storage.CreateNamespace(context.Background(), &vector.NamespaceInfo{
		Name:      testScopedNS("test-namespace"),
		Dimension: 3,
	})

	req := &GetVectorRequest{
		Namespace: "test-namespace",
		ID:        999,
	}

	resp, err := svc.GetVector(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
	requireErrorAs[*VectorNotFoundError](t, err)
}

// =============================================================================
// DeleteVector Tests
// =============================================================================

func TestVectorService_DeleteVector_Success(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	// Create namespace and insert vector
	require.NoError(t, storage.CreateNamespace(context.Background(), &vector.NamespaceInfo{
		Name:      testScopedNS("test-namespace"),
		Dimension: 3,
	}))
	require.NoError(t, storage.UpsertVector(context.Background(), testScopedNS("test-namespace"), &vector.VectorValue{
		ID:        1,
		Embedding: []float32{1.0, 2.0, 3.0},
	}))

	req := &DeleteVectorRequest{
		Namespace: "test-namespace",
		ID:        1,
		ShardID:   1,
	}

	resp, err := svc.DeleteVector(context.Background(), req)

	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.True(t, resp.Success)
	assert.NotZero(t, resp.Timestamp)
}

func TestVectorService_DeleteVector_NilRequest(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	resp, err := svc.DeleteVector(context.Background(), nil)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "request cannot be nil")
}

func TestVectorService_DeleteVector_MissingNamespace(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &DeleteVectorRequest{
		Namespace: "",
		ID:        1,
	}

	resp, err := svc.DeleteVector(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "namespace is required")
}

func TestVectorService_DeleteVector_ZeroID(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &DeleteVectorRequest{
		Namespace: "test-namespace",
		ID:        0,
	}

	resp, err := svc.DeleteVector(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "ID must be greater than 0")
}

// =============================================================================
// BatchUpsert Tests
// =============================================================================

func TestVectorService_BatchUpsert_Success(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	// Create namespace
	_ = storage.CreateNamespace(context.Background(), &vector.NamespaceInfo{
		Name:      testScopedNS("test-namespace"),
		Dimension: 3,
	})

	vectors := []*VectorData{
		{
			ID:       1,
			Vector:   []float32{1.0, 2.0, 3.0},
			Metadata: map[string]interface{}{"type": "test1"},
		},
		{
			ID:       2,
			Vector:   []float32{4.0, 5.0, 6.0},
			Metadata: map[string]interface{}{"type": "test2"},
		},
	}

	req := &BatchUpsertRequest{
		Namespace: "test-namespace",
		Vectors:   vectors,
		ShardID:   1,
	}

	resp, err := svc.BatchUpsert(context.Background(), req)

	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.True(t, resp.Success)
	assert.Equal(t, 2, resp.InsertedCount)
	assert.Len(t, resp.IDs, 2)
	assert.Len(t, resp.Versions, 2)
}

func TestVectorService_BatchUpsert_NilRequest(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	resp, err := svc.BatchUpsert(context.Background(), nil)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "request cannot be nil")
}

func TestVectorService_BatchUpsert_MissingNamespace(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &BatchUpsertRequest{
		Namespace: "",
		Vectors:   []*VectorData{{ID: 1, Vector: []float32{1.0}}},
	}

	resp, err := svc.BatchUpsert(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "namespace is required")
}

func TestVectorService_BatchUpsert_EmptyVectors(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &BatchUpsertRequest{
		Namespace: "test-namespace",
		Vectors:   []*VectorData{},
	}

	resp, err := svc.BatchUpsert(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "vectors is required")
}

// =============================================================================
// SearchSimilar Tests
// =============================================================================

func TestVectorService_SearchSimilar_Success(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	// Create namespace and insert vectors
	ctx := context.Background()
	_ = storage.CreateNamespace(ctx, &vector.NamespaceInfo{
		Name:      testScopedNS("test-namespace"),
		Dimension: 3,
	})

	// Create namespace through service to ensure HNSW index is created
	_, err = svc.CreateNamespace(ctx, &CreateNamespaceRequest{
		Namespace: "search-namespace",
		Dimension: 3,
		ShardID:   1,
	})
	require.NoError(t, err)

	// Insert some vectors
	_, err = svc.UpsertVector(ctx, &UpsertVectorRequest{
		Namespace: "search-namespace",
		ID:        1,
		Vector:    []float32{1.0, 0.0, 0.0},
		ShardID:   1,
	})
	require.NoError(t, err)
	_, err = svc.UpsertVector(ctx, &UpsertVectorRequest{
		Namespace: "search-namespace",
		ID:        2,
		Vector:    []float32{0.0, 1.0, 0.0},
		ShardID:   1,
	})
	require.NoError(t, err)

	req := &SearchSimilarRequest{
		Namespace:       "search-namespace",
		QueryVector:     []float32{1.0, 0.0, 0.0},
		K:               2,
		EfSearch:        50,
		IncludeVectors:  true,
		IncludeMetadata: true,
		ShardID:         1,
	}

	resp, err := svc.SearchSimilar(ctx, req)

	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.NotEmpty(t, resp.Results)
	assert.NotZero(t, resp.Timestamp)
	assert.NotZero(t, resp.QueryDuration)
}

func TestVectorService_SearchSimilar_NilRequest(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	resp, err := svc.SearchSimilar(context.Background(), nil)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "request cannot be nil")
}

func TestVectorService_SearchSimilar_MissingNamespace(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &SearchSimilarRequest{
		Namespace:   "",
		QueryVector: []float32{1.0, 2.0, 3.0},
		K:           5,
	}

	resp, err := svc.SearchSimilar(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "namespace is required")
}

func TestVectorService_SearchSimilar_EmptyQueryVector(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &SearchSimilarRequest{
		Namespace:   "test-namespace",
		QueryVector: []float32{},
		K:           5,
	}

	resp, err := svc.SearchSimilar(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "query vector is required")
}

func TestVectorService_SearchSimilar_ZeroK(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &SearchSimilarRequest{
		Namespace:   "test-namespace",
		QueryVector: []float32{1.0, 2.0, 3.0},
		K:           0,
	}

	resp, err := svc.SearchSimilar(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "k must be greater than 0")
}

// =============================================================================
// SearchByMetadata Tests
// =============================================================================

func TestVectorService_SearchByMetadata_Success(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	// Create namespace and insert vectors
	ctx := context.Background()
	require.NoError(t, storage.CreateNamespace(ctx, &vector.NamespaceInfo{
		Name:      testScopedNS("test-namespace"),
		Dimension: 3,
	}))
	require.NoError(t, storage.UpsertVector(ctx, testScopedNS("test-namespace"), &vector.VectorValue{
		ID:        1,
		Embedding: []float32{1.0, 2.0, 3.0},
		Metadata:  map[string]string{"category": "A"},
	}))
	require.NoError(t, storage.UpsertVector(ctx, testScopedNS("test-namespace"), &vector.VectorValue{
		ID:        2,
		Embedding: []float32{4.0, 5.0, 6.0},
		Metadata:  map[string]string{"category": "B"},
	}))

	req := &SearchByMetadataRequest{
		Namespace:       "test-namespace",
		Filters:         map[string]interface{}{"category": "A"},
		Limit:           10,
		IncludeVectors:  true,
		IncludeMetadata: true,
		ShardID:         1,
	}

	resp, err := svc.SearchByMetadata(ctx, req)

	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.NotEmpty(t, resp.Results)
	assert.NotZero(t, resp.Timestamp)
}

func TestVectorService_SearchByMetadata_NilRequest(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	resp, err := svc.SearchByMetadata(context.Background(), nil)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "request cannot be nil")
}

func TestVectorService_SearchByMetadata_MissingNamespace(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &SearchByMetadataRequest{
		Namespace: "",
		Filters:   map[string]interface{}{"key": "value"},
	}

	resp, err := svc.SearchByMetadata(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "namespace is required")
}

// =============================================================================
// HybridSearch Tests
// =============================================================================

func TestVectorService_HybridSearch_Success(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	// Create namespace through service
	ctx := context.Background()
	_, err = svc.CreateNamespace(ctx, &CreateNamespaceRequest{
		Namespace: "hybrid-namespace",
		Dimension: 3,
		ShardID:   1,
	})
	require.NoError(t, err)

	// Insert vectors with metadata
	_, err = svc.UpsertVector(ctx, &UpsertVectorRequest{
		Namespace: "hybrid-namespace",
		ID:        1,
		Vector:    []float32{1.0, 0.0, 0.0},
		Metadata:  map[string]interface{}{"category": "A"},
		ShardID:   1,
	})
	require.NoError(t, err)
	_, err = svc.UpsertVector(ctx, &UpsertVectorRequest{
		Namespace: "hybrid-namespace",
		ID:        2,
		Vector:    []float32{0.9, 0.1, 0.0},
		Metadata:  map[string]interface{}{"category": "A"},
		ShardID:   1,
	})
	require.NoError(t, err)
	_, err = svc.UpsertVector(ctx, &UpsertVectorRequest{
		Namespace: "hybrid-namespace",
		ID:        3,
		Vector:    []float32{0.8, 0.2, 0.0},
		Metadata:  map[string]interface{}{"category": "B"},
		ShardID:   1,
	})
	require.NoError(t, err)

	req := &HybridSearchRequest{
		Namespace:       "hybrid-namespace",
		QueryVector:     []float32{1.0, 0.0, 0.0},
		K:               2,
		EfSearch:        50,
		MetadataFilter:  map[string]interface{}{"category": "A"},
		IncludeVectors:  true,
		IncludeMetadata: true,
		ShardID:         1,
	}

	resp, err := svc.HybridSearch(ctx, req)

	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.NotZero(t, resp.Timestamp)
	assert.NotZero(t, resp.QueryDuration)
}

// =============================================================================
// RebuildIndex Tests
// =============================================================================

func TestVectorService_RebuildIndex_Sync_Success(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	// Create namespace and add vectors
	ctx := context.Background()
	_ = storage.CreateNamespace(ctx, &vector.NamespaceInfo{
		Name:        testScopedNS("test-namespace"),
		Dimension:   3,
		VectorCount: 2,
	})
	_ = storage.UpsertVector(ctx, testScopedNS("test-namespace"), &vector.VectorValue{
		ID:        1,
		Embedding: []float32{1.0, 2.0, 3.0},
	})

	req := &RebuildIndexRequest{
		Namespace: "test-namespace",
		Async:     false,
		ShardID:   1,
	}

	resp, err := svc.RebuildIndex(ctx, req)

	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.NotEmpty(t, resp.JobID)
	assert.True(t, resp.Success)
	assert.Equal(t, "completed", resp.Status)
}

func TestVectorService_RebuildIndex_Async_Success(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	// Create namespace
	ctx := context.Background()
	_ = storage.CreateNamespace(ctx, &vector.NamespaceInfo{
		Name:        testScopedNS("test-namespace"),
		Dimension:   3,
		VectorCount: 2,
	})

	req := &RebuildIndexRequest{
		Namespace: "test-namespace",
		Async:     true,
		ShardID:   1,
	}

	resp, err := svc.RebuildIndex(ctx, req)

	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.NotEmpty(t, resp.JobID)
	assert.Equal(t, "in_progress", resp.Status)
	assert.False(t, resp.Success)
}

func TestVectorService_RebuildIndex_NilRequest(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	resp, err := svc.RebuildIndex(context.Background(), nil)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "request cannot be nil")
}

func TestVectorService_RebuildIndex_MissingNamespace(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &RebuildIndexRequest{
		Namespace: "",
	}

	resp, err := svc.RebuildIndex(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "namespace is required")
}

func TestVectorService_RebuildIndex_NamespaceNotFound(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &RebuildIndexRequest{
		Namespace: "nonexistent-namespace",
	}

	resp, err := svc.RebuildIndex(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
	requireErrorAs[*NamespaceNotFoundError](t, err)
}

// =============================================================================
// GetRebuildProgress Tests
// =============================================================================

func TestVectorService_GetRebuildProgress_ByJobID(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	// Create namespace
	ctx := context.Background()
	_ = storage.CreateNamespace(ctx, &vector.NamespaceInfo{
		Name:        testScopedNS("test-namespace"),
		Dimension:   128,
		VectorCount: 1000,
	})

	// Start async rebuild
	rebuildReq := &RebuildIndexRequest{
		Namespace: "test-namespace",
		Async:     true,
	}
	rebuildResp, err := svc.RebuildIndex(ctx, rebuildReq)
	require.NoError(t, err)
	assert.NotEmpty(t, rebuildResp.JobID)

	// Get progress by job ID
	req := &GetRebuildProgressRequest{
		JobID: rebuildResp.JobID,
	}
	resp, err := svc.GetRebuildProgress(ctx, req)

	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, rebuildResp.JobID, resp.JobID)
	assert.Equal(t, "test-namespace", resp.Namespace)
	assert.NotZero(t, resp.StartedAt)
}

func TestVectorService_GetRebuildProgress_ByNamespace(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	// Create namespace
	ctx := context.Background()
	_ = storage.CreateNamespace(ctx, &vector.NamespaceInfo{
		Name:        testScopedNS("test-namespace"),
		Dimension:   128,
		VectorCount: 1000,
	})

	// Start async rebuild
	rebuildReq := &RebuildIndexRequest{
		Namespace: "test-namespace",
		Async:     true,
	}
	rebuildResp, err := svc.RebuildIndex(ctx, rebuildReq)
	require.NoError(t, err)

	// Get progress by namespace
	req := &GetRebuildProgressRequest{
		Namespace: "test-namespace",
	}
	resp, err := svc.GetRebuildProgress(ctx, req)

	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, rebuildResp.JobID, resp.JobID)
	assert.Equal(t, "test-namespace", resp.Namespace)
}

func TestVectorService_GetRebuildProgress_Completed(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	// Create namespace
	ctx := context.Background()
	_ = storage.CreateNamespace(ctx, &vector.NamespaceInfo{
		Name:        testScopedNS("test-namespace"),
		Dimension:   128,
		VectorCount: 100,
	})

	// Start sync rebuild (will complete immediately)
	rebuildReq := &RebuildIndexRequest{
		Namespace: "test-namespace",
		Async:     false,
	}
	rebuildResp, err := svc.RebuildIndex(ctx, rebuildReq)
	require.NoError(t, err)
	assert.Equal(t, "completed", rebuildResp.Status)

	// Get progress
	req := &GetRebuildProgressRequest{
		JobID: rebuildResp.JobID,
	}
	resp, err := svc.GetRebuildProgress(ctx, req)

	require.NoError(t, err)
	assert.Equal(t, "completed", resp.Status)
	assert.Equal(t, 100.0, resp.PercentComplete)
	assert.NotZero(t, resp.CompletedAt)
}

func TestVectorService_GetRebuildProgress_NotFound_ByJobID(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &GetRebuildProgressRequest{
		JobID: "nonexistent-job-id",
	}
	resp, err := svc.GetRebuildProgress(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "rebuild job not found")
}

func TestVectorService_GetRebuildProgress_NotFound_ByNamespace(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &GetRebuildProgressRequest{
		Namespace: "nonexistent-namespace",
	}
	resp, err := svc.GetRebuildProgress(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "rebuild job not found")
}

func TestVectorService_GetRebuildProgress_NilRequest(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	resp, err := svc.GetRebuildProgress(context.Background(), nil)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "request cannot be nil")
}

func TestVectorService_GetRebuildProgress_EmptyRequest(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &GetRebuildProgressRequest{}
	resp, err := svc.GetRebuildProgress(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "job_id or namespace is required")
}

func TestVectorService_GetRebuildProgress_EstimatedTimeRemaining(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	// Create namespace with vectors
	ctx := context.Background()
	_ = storage.CreateNamespace(ctx, &vector.NamespaceInfo{
		Name:        testScopedNS("test-namespace"),
		Dimension:   128,
		VectorCount: 10000,
	})

	// Start async rebuild
	rebuildReq := &RebuildIndexRequest{
		Namespace: "test-namespace",
		Async:     true,
	}
	rebuildResp, err := svc.RebuildIndex(ctx, rebuildReq)
	require.NoError(t, err)

	// Wait a bit to get some elapsed time
	time.Sleep(10 * time.Millisecond)

	// Get progress (may show estimate if in progress)
	req := &GetRebuildProgressRequest{
		JobID: rebuildResp.JobID,
	}
	resp, err := svc.GetRebuildProgress(ctx, req)

	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.GreaterOrEqual(t, resp.ElapsedTimeMs, int64(0))
}

// =============================================================================
// GetIndexStats Tests
// =============================================================================

func TestVectorService_GetIndexStats_Success(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	// Create namespace
	ctx := context.Background()
	_ = storage.CreateNamespace(ctx, &vector.NamespaceInfo{
		Name:      testScopedNS("test-namespace"),
		Dimension: 128,
	})

	req := &GetIndexStatsRequest{
		Namespace: "test-namespace",
		ShardID:   1,
	}

	resp, err := svc.GetIndexStats(ctx, req)

	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, "test-namespace", resp.Namespace)
	assert.Equal(t, "HNSW", resp.IndexType)
	assert.Equal(t, uint32(128), resp.Dimension)
	assert.NotNil(t, resp.SearchPerformance)
	assert.NotZero(t, resp.Timestamp)
}

func TestVectorService_GetIndexStats_NilRequest(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	resp, err := svc.GetIndexStats(context.Background(), nil)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "request cannot be nil")
}

func TestVectorService_GetIndexStats_MissingNamespace(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &GetIndexStatsRequest{
		Namespace: "",
	}

	resp, err := svc.GetIndexStats(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "namespace is required")
}

func TestVectorService_GetIndexStats_NamespaceNotFound(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	req := &GetIndexStatsRequest{
		Namespace: "nonexistent-namespace",
	}

	resp, err := svc.GetIndexStats(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
	requireErrorAs[*NamespaceNotFoundError](t, err)
}

// =============================================================================
// Service Construction Tests
// =============================================================================

func TestNewVectorService_NilStorage(t *testing.T) {
	logger := newMockLogger()

	svc, err := NewVectorService(nil, logger)

	require.Error(t, err)
	assert.Nil(t, svc)
	assert.Contains(t, err.Error(), "storage is required")
}

func TestNewVectorService_NilLogger(t *testing.T) {
	storage := newMockVectorStorage()

	svc, err := NewVectorService(storage, nil)

	require.Error(t, err)
	assert.Nil(t, svc)
	assert.Contains(t, err.Error(), "logger is required")
}

func TestNewVectorService_Success(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()

	svc, err := NewVectorService(storage, logger)

	require.NoError(t, err)
	assert.NotNil(t, svc)
}

func TestNewVectorService_LoadsExistingNamespaces(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()

	// Create namespaces before service initialization
	_ = storage.CreateNamespace(context.Background(), &vector.NamespaceInfo{
		Name:      "namespace-1",
		Dimension: 128,
	})
	_ = storage.CreateNamespace(context.Background(), &vector.NamespaceInfo{
		Name:      "namespace-2",
		Dimension: 256,
	})

	svc, err := NewVectorService(storage, logger)

	require.NoError(t, err)
	assert.NotNil(t, svc)

	// Verify namespaces are loaded
	resp, err := svc.ListNamespaces(context.Background(), &ListNamespacesRequest{})
	require.NoError(t, err)
	assert.Equal(t, 2, resp.TotalCount)
}

// =============================================================================
// Context Cancellation Tests
// =============================================================================

func TestVectorService_CreateNamespace_ContextCanceled(t *testing.T) {
	storage := newMockVectorStorage()
	storage.createNamespaceFn = func(ctx context.Context, info *vector.NamespaceInfo) error {
		return ctx.Err()
	}
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	req := &CreateNamespaceRequest{
		Namespace: "test-namespace",
		Dimension: 128,
	}

	resp, err := svc.CreateNamespace(ctx, req)

	require.Error(t, err)
	assert.Nil(t, resp)
}

// =============================================================================
// Edge Cases and Additional Coverage
// =============================================================================

func TestVectorService_SearchSimilar_WithMinScore(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	ctx := context.Background()
	_, err = svc.CreateNamespace(ctx, &CreateNamespaceRequest{
		Namespace: "filter-namespace",
		Dimension: 3,
		ShardID:   1,
	})
	require.NoError(t, err)

	_, err = svc.UpsertVector(ctx, &UpsertVectorRequest{
		Namespace: "filter-namespace",
		ID:        1,
		Vector:    []float32{1.0, 0.0, 0.0},
		ShardID:   1,
	})
	require.NoError(t, err)

	req := &SearchSimilarRequest{
		Namespace:   "filter-namespace",
		QueryVector: []float32{1.0, 0.0, 0.0},
		K:           5,
		MinScore:    0.5, // Filter by minimum score
		ShardID:     1,
	}

	resp, err := svc.SearchSimilar(ctx, req)

	require.NoError(t, err)
	assert.NotNil(t, resp)
}

func TestVectorService_BatchUpsert_LargeBatch(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	// Create namespace
	_ = storage.CreateNamespace(context.Background(), &vector.NamespaceInfo{
		Name:      testScopedNS("test-namespace"),
		Dimension: 3,
	})

	// Create 100 vectors
	vectors := make([]*VectorData, 100)
	for i := 0; i < 100; i++ {
		vectors[i] = &VectorData{
			ID:     uint64(i + 1),
			Vector: []float32{float32(i), float32(i + 1), float32(i + 2)},
		}
	}

	req := &BatchUpsertRequest{
		Namespace: "test-namespace",
		Vectors:   vectors,
		ShardID:   1,
	}

	resp, err := svc.BatchUpsert(context.Background(), req)

	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, 100, resp.InsertedCount)
}

func TestVectorService_SearchByMetadata_WithPagination(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	ctx := context.Background()
	_ = storage.CreateNamespace(ctx, &vector.NamespaceInfo{
		Name:      testScopedNS("test-namespace"),
		Dimension: 3,
	})

	// Insert multiple vectors with same metadata
	for i := 0; i < 10; i++ {
		_ = storage.UpsertVector(ctx, testScopedNS("test-namespace"), &vector.VectorValue{
			ID:        uint64(i + 1),
			Embedding: []float32{float32(i), float32(i), float32(i)},
			Metadata:  map[string]string{"category": "A"},
		})
	}

	req := &SearchByMetadataRequest{
		Namespace: "test-namespace",
		Filters:   map[string]interface{}{"category": "A"},
		Limit:     5,
		Offset:    0,
		ShardID:   1,
	}

	resp, err := svc.SearchByMetadata(ctx, req)

	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Len(t, resp.Results, 5)
}

func TestVectorService_RebuildIndex_WithCustomConfig(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	ctx := context.Background()
	_ = storage.CreateNamespace(ctx, &vector.NamespaceInfo{
		Name:        testScopedNS("test-namespace"),
		Dimension:   3,
		VectorCount: 2,
	})

	req := &RebuildIndexRequest{
		Namespace: "test-namespace",
		IndexConfig: map[string]string{
			"m":               "32",
			"ef_construction": "400",
		},
		Async:   false,
		ShardID: 1,
	}

	resp, err := svc.RebuildIndex(ctx, req)

	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.True(t, resp.Success)
}

// =============================================================================
// Compaction Tests
// =============================================================================

func TestVectorService_CompactNamespace_Success(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	ctx := context.Background()
	_ = storage.CreateNamespace(ctx, &vector.NamespaceInfo{
		Name:      testScopedNS("test-namespace"),
		Dimension: 128,
	})

	req := &CompactNamespaceRequest{
		Namespace: "test-namespace",
		Async:     false,
	}

	resp, err := svc.CompactNamespace(ctx, req)

	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.True(t, resp.Success)
	assert.NotEmpty(t, resp.OperationID)
	assert.Greater(t, resp.StartedAt, int64(0))
	assert.NotNil(t, resp.CompletedAt)
}

func TestVectorService_CompactNamespace_Async(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	ctx := context.Background()
	_ = storage.CreateNamespace(ctx, &vector.NamespaceInfo{
		Name:      testScopedNS("test-namespace"),
		Dimension: 128,
	})

	req := &CompactNamespaceRequest{
		Namespace: "test-namespace",
		Async:     true,
	}

	resp, err := svc.CompactNamespace(ctx, req)

	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.False(t, resp.Success) // Async returns false until complete
	assert.NotEmpty(t, resp.OperationID)
	assert.Greater(t, resp.StartedAt, int64(0))

	// Wait for background compaction to complete
	time.Sleep(100 * time.Millisecond)

	// Check progress
	progressReq := &GetCompactionProgressRequest{
		OperationID: resp.OperationID,
	}
	progressResp, err := svc.GetCompactionProgress(ctx, progressReq)
	require.NoError(t, err)
	assert.False(t, progressResp.InProgress)
	assert.Equal(t, float64(100), progressResp.PercentComplete)
}

func TestVectorService_CompactNamespace_NamespaceNotFound(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	ctx := context.Background()

	req := &CompactNamespaceRequest{
		Namespace: "nonexistent-namespace",
		Async:     false,
	}

	resp, err := svc.CompactNamespace(ctx, req)

	assert.Nil(t, resp)
	assert.Error(t, err)
}

func TestVectorService_CompactNamespace_NilRequest(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	ctx := context.Background()

	resp, err := svc.CompactNamespace(ctx, nil)

	assert.Nil(t, resp)
	assert.Error(t, err)
}

func TestVectorService_CompactNamespace_EmptyNamespace(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	ctx := context.Background()

	req := &CompactNamespaceRequest{
		Namespace: "",
		Async:     false,
	}

	resp, err := svc.CompactNamespace(ctx, req)

	assert.Nil(t, resp)
	assert.Error(t, err)
}

func TestVectorService_CompactNamespace_StorageError(t *testing.T) {
	storage := newMockVectorStorage()
	storage.compactFn = func(ctx context.Context, namespace string) error {
		return errors.New("storage compaction error")
	}
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	ctx := context.Background()
	_ = storage.CreateNamespace(ctx, &vector.NamespaceInfo{
		Name:      testScopedNS("test-namespace"),
		Dimension: 128,
	})

	req := &CompactNamespaceRequest{
		Namespace: "test-namespace",
		Async:     false,
	}

	resp, err := svc.CompactNamespace(ctx, req)

	assert.Nil(t, resp)
	assert.Error(t, err)
}

func TestVectorService_GetCompactionProgress_ByOperationID(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	ctx := context.Background()
	_ = storage.CreateNamespace(ctx, &vector.NamespaceInfo{
		Name:      testScopedNS("test-namespace"),
		Dimension: 128,
	})

	// First compact to create a job
	compactReq := &CompactNamespaceRequest{
		Namespace: "test-namespace",
		Async:     false,
	}
	compactResp, err := svc.CompactNamespace(ctx, compactReq)
	require.NoError(t, err)

	// Get progress by operation ID
	req := &GetCompactionProgressRequest{
		OperationID: compactResp.OperationID,
	}

	resp, err := svc.GetCompactionProgress(ctx, req)

	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, compactResp.OperationID, resp.OperationID)
	assert.Equal(t, "test-namespace", resp.Namespace)
	assert.False(t, resp.InProgress)
	assert.Equal(t, float64(100), resp.PercentComplete)
}

func TestVectorService_GetCompactionProgress_ByNamespace(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	ctx := context.Background()
	_ = storage.CreateNamespace(ctx, &vector.NamespaceInfo{
		Name:      testScopedNS("test-namespace"),
		Dimension: 128,
	})

	// First compact to create a job
	compactReq := &CompactNamespaceRequest{
		Namespace: "test-namespace",
		Async:     false,
	}
	_, err = svc.CompactNamespace(ctx, compactReq)
	require.NoError(t, err)

	// Get progress by namespace
	req := &GetCompactionProgressRequest{
		Namespace: "test-namespace",
	}

	resp, err := svc.GetCompactionProgress(ctx, req)

	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, "test-namespace", resp.Namespace)
	assert.False(t, resp.InProgress)
}

func TestVectorService_GetCompactionProgress_NotFound_ByOperationID(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	ctx := context.Background()

	req := &GetCompactionProgressRequest{
		OperationID: "nonexistent-operation-id",
	}

	resp, err := svc.GetCompactionProgress(ctx, req)

	assert.Nil(t, resp)
	assert.Error(t, err)
}

func TestVectorService_GetCompactionProgress_NotFound_ByNamespace(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	ctx := context.Background()

	req := &GetCompactionProgressRequest{
		Namespace: "nonexistent-namespace",
	}

	resp, err := svc.GetCompactionProgress(ctx, req)

	assert.Nil(t, resp)
	assert.Error(t, err)
}

func TestVectorService_GetCompactionProgress_NilRequest(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	ctx := context.Background()

	resp, err := svc.GetCompactionProgress(ctx, nil)

	assert.Nil(t, resp)
	assert.Error(t, err)
}

func TestVectorService_GetCompactionProgress_EmptyRequest(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	ctx := context.Background()

	req := &GetCompactionProgressRequest{}

	resp, err := svc.GetCompactionProgress(ctx, req)

	assert.Nil(t, resp)
	assert.Error(t, err)
}

func TestVectorService_CompactNamespace_MultipleOperations(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()
	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	ctx := context.Background()
	_ = storage.CreateNamespace(ctx, &vector.NamespaceInfo{
		Name:      testScopedNS("test-namespace"),
		Dimension: 128,
	})

	// Run multiple compactions
	var operationIDs []string
	for i := 0; i < 3; i++ {
		req := &CompactNamespaceRequest{
			Namespace: "test-namespace",
			Async:     false,
		}
		resp, err := svc.CompactNamespace(ctx, req)
		require.NoError(t, err)
		operationIDs = append(operationIDs, resp.OperationID)
	}

	// Each operation should have a unique ID
	assert.Len(t, operationIDs, 3)
	assert.NotEqual(t, operationIDs[0], operationIDs[1])
	assert.NotEqual(t, operationIDs[1], operationIDs[2])

	// Getting progress by namespace should return the most recent job
	req := &GetCompactionProgressRequest{
		Namespace: "test-namespace",
	}
	resp, err := svc.GetCompactionProgress(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, operationIDs[2], resp.OperationID)
}
