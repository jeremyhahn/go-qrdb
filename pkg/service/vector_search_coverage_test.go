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

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/interfaces"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// =============================================================================
// Mock HNSW Index for controlling search behavior
// =============================================================================

// searchMockHNSWIndex implements interfaces.HNSWIndex with configurable functions.
type searchMockHNSWIndex struct {
	insertFn   func(id uint64, vector []float32) error
	deleteFn   func(id uint64) error
	searchFn   func(query []float32, k int) ([]*interfaces.HNSWSearchResult, error)
	countFn    func() int64
	getStatsFn func() *interfaces.HNSWStats
}

func (m *searchMockHNSWIndex) Insert(id uint64, vec []float32) error {
	if m.insertFn != nil {
		return m.insertFn(id, vec)
	}
	return nil
}

func (m *searchMockHNSWIndex) Delete(id uint64) error {
	if m.deleteFn != nil {
		return m.deleteFn(id)
	}
	return nil
}

func (m *searchMockHNSWIndex) Search(query []float32, k int) ([]*interfaces.HNSWSearchResult, error) {
	if m.searchFn != nil {
		return m.searchFn(query, k)
	}
	return []*interfaces.HNSWSearchResult{}, nil
}

func (m *searchMockHNSWIndex) Count() int64 {
	if m.countFn != nil {
		return m.countFn()
	}
	return 0
}

func (m *searchMockHNSWIndex) GetStats() *interfaces.HNSWStats {
	if m.getStatsFn != nil {
		return m.getStatsFn()
	}
	return &interfaces.HNSWStats{}
}

// =============================================================================
// Helper: create vectorService with a pre-injected mock HNSW index
// =============================================================================

func newSearchTestService(t *testing.T, namespace string, dimension uint32, mockIndex interfaces.HNSWIndex) (*vectorService, *mockVectorStorage) {
	t.Helper()
	storage := newMockVectorStorage()
	logger := newMockLogger()

	// The service scopes namespaces by tenant. Tests run without an explicit
	// tenant ID so tenant.FromContext returns SystemTenantID. Pre-populate the
	// storage using the scoped name so that init loads the index correctly.
	scoped := scopedNamespace(tenantid.System, namespace)
	require.NoError(t, storage.CreateNamespace(context.Background(), &vector.NamespaceInfo{
		Name:      scoped,
		Dimension: dimension,
	}))

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	vs := svc.(*vectorService)

	// Inject mock HNSW index keyed by the scoped name (the key the service uses).
	if mockIndex != nil {
		vs.indexMu.Lock()
		vs.hnswIndexes[scoped] = mockIndex
		vs.indexMu.Unlock()
	}

	return vs, storage
}

// =============================================================================
// matchesMetadataFilter Tests
// =============================================================================

func TestMatchesMetadataFilter_EmptyFilter(t *testing.T) {
	metadata := map[string]interface{}{"key": "value"}
	filter := map[string]interface{}{}

	result := matchesMetadataFilter(metadata, filter)

	assert.True(t, result, "empty filter should match all metadata")
}

func TestMatchesMetadataFilter_NilFilter(t *testing.T) {
	metadata := map[string]interface{}{"key": "value"}

	result := matchesMetadataFilter(metadata, nil)

	assert.True(t, result, "nil filter should match all metadata")
}

func TestMatchesMetadataFilter_ExactMatch(t *testing.T) {
	metadata := map[string]interface{}{
		"category": "science",
		"author":   "smith",
	}
	filter := map[string]interface{}{
		"category": "science",
	}

	result := matchesMetadataFilter(metadata, filter)

	assert.True(t, result, "exact match on existing key should return true")
}

func TestMatchesMetadataFilter_MultipleKeysAllMatch(t *testing.T) {
	metadata := map[string]interface{}{
		"category": "science",
		"author":   "smith",
		"year":     "2024",
	}
	filter := map[string]interface{}{
		"category": "science",
		"author":   "smith",
	}

	result := matchesMetadataFilter(metadata, filter)

	assert.True(t, result, "all filter keys matching should return true")
}

func TestMatchesMetadataFilter_KeyMissing(t *testing.T) {
	metadata := map[string]interface{}{
		"category": "science",
	}
	filter := map[string]interface{}{
		"nonexistent": "value",
	}

	result := matchesMetadataFilter(metadata, filter)

	assert.False(t, result, "missing key should return false")
}

func TestMatchesMetadataFilter_ValueMismatch(t *testing.T) {
	metadata := map[string]interface{}{
		"category": "science",
	}
	filter := map[string]interface{}{
		"category": "math",
	}

	result := matchesMetadataFilter(metadata, filter)

	assert.False(t, result, "value mismatch should return false")
}

func TestMatchesMetadataFilter_PartialKeyMismatch(t *testing.T) {
	metadata := map[string]interface{}{
		"category": "science",
		"author":   "smith",
	}
	filter := map[string]interface{}{
		"category": "science",
		"author":   "jones",
	}

	result := matchesMetadataFilter(metadata, filter)

	assert.False(t, result, "partial mismatch should return false")
}

func TestMatchesMetadataFilter_NumericValueComparison(t *testing.T) {
	metadata := map[string]interface{}{
		"count": 42,
	}
	filter := map[string]interface{}{
		"count": 42,
	}

	result := matchesMetadataFilter(metadata, filter)

	assert.True(t, result, "numeric values compared via Sprintf should match")
}

func TestMatchesMetadataFilter_TypeMismatchSameStringRepresentation(t *testing.T) {
	metadata := map[string]interface{}{
		"count": "42",
	}
	filter := map[string]interface{}{
		"count": 42,
	}

	result := matchesMetadataFilter(metadata, filter)

	assert.True(t, result, "values with same string representation should match")
}

func TestMatchesMetadataFilter_NilMetadata(t *testing.T) {
	filter := map[string]interface{}{
		"key": "value",
	}

	result := matchesMetadataFilter(nil, filter)

	assert.False(t, result, "nil metadata with non-empty filter should return false")
}

func TestMatchesMetadataFilter_BothNil(t *testing.T) {
	result := matchesMetadataFilter(nil, nil)

	assert.True(t, result, "both nil should return true (empty filter)")
}

// =============================================================================
// ensureHNSWIndex Tests
// =============================================================================

func TestEnsureHNSWIndex_ExistingIndex(t *testing.T) {
	mockIdx := &searchMockHNSWIndex{}

	vs, _ := newSearchTestService(t, "test-ns", 3, mockIdx)

	// ensureHNSWIndex operates on the physical (scoped) namespace key.
	scoped := testScopedNS("test-ns")
	idx, err := vs.ensureHNSWIndex(context.Background(), scoped)

	require.NoError(t, err)
	assert.NotNil(t, idx)
}

func TestEnsureHNSWIndex_NamespaceNotFound(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	vs := svc.(*vectorService)

	// Attempt to ensure index for a namespace that does not exist in storage
	idx, err := vs.ensureHNSWIndex(context.Background(), "nonexistent-ns")

	require.Error(t, err)
	assert.Nil(t, idx)
}

func TestEnsureHNSWIndex_LazyCreationCosine(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	vs := svc.(*vectorService)

	// Add namespace to storage with COSINE metric but without a pre-existing index
	require.NoError(t, storage.CreateNamespace(context.Background(), &vector.NamespaceInfo{
		Name:           "cosine-ns",
		Dimension:      4,
		DistanceMetric: "COSINE",
	}))

	idx, err := vs.ensureHNSWIndex(context.Background(), "cosine-ns")

	require.NoError(t, err)
	assert.NotNil(t, idx)
}

func TestEnsureHNSWIndex_LazyCreationDotProduct(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	vs := svc.(*vectorService)

	require.NoError(t, storage.CreateNamespace(context.Background(), &vector.NamespaceInfo{
		Name:           "dot-ns",
		Dimension:      4,
		DistanceMetric: "DOT_PRODUCT",
	}))

	idx, err := vs.ensureHNSWIndex(context.Background(), "dot-ns")

	require.NoError(t, err)
	assert.NotNil(t, idx)
}

func TestEnsureHNSWIndex_LazyCreationDefaultL2(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	vs := svc.(*vectorService)

	// Use an unknown metric string to exercise the default (L2) branch
	require.NoError(t, storage.CreateNamespace(context.Background(), &vector.NamespaceInfo{
		Name:           "default-ns",
		Dimension:      4,
		DistanceMetric: "UNKNOWN_METRIC",
	}))

	idx, err := vs.ensureHNSWIndex(context.Background(), "default-ns")

	require.NoError(t, err)
	assert.NotNil(t, idx)
}

func TestEnsureHNSWIndex_LoadsVectorsFromStorage(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	vs := svc.(*vectorService)

	// Create namespace and add vectors using the scoped key, since
	// ensureHNSWIndex is an internal function that receives a scoped key.
	scoped := testScopedNS("load-ns")
	require.NoError(t, storage.CreateNamespace(context.Background(), &vector.NamespaceInfo{
		Name:      scoped,
		Dimension: 3,
	}))
	require.NoError(t, storage.UpsertVector(context.Background(), scoped, &vector.VectorValue{
		ID:        1,
		Embedding: []float32{1.0, 0.0, 0.0},
	}))
	require.NoError(t, storage.UpsertVector(context.Background(), scoped, &vector.VectorValue{
		ID:        2,
		Embedding: []float32{0.0, 1.0, 0.0},
	}))

	idx, err := vs.ensureHNSWIndex(context.Background(), scoped)

	require.NoError(t, err)
	assert.NotNil(t, idx)
	assert.Equal(t, int64(2), idx.Count())
}

func TestEnsureHNSWIndex_ScanFailureContinues(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	vs := svc.(*vectorService)

	// Create namespace in storage so GetNamespaceInfo succeeds
	require.NoError(t, storage.CreateNamespace(context.Background(), &vector.NamespaceInfo{
		Name:      "scan-fail-ns",
		Dimension: 3,
	}))

	// Override ScanNamespace to fail, simulating a storage error during loading
	storage.scanNamespaceFn = func(ctx context.Context, namespace string, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
		return nil, errors.New("scan failed")
	}

	idx, err := vs.ensureHNSWIndex(context.Background(), "scan-fail-ns")

	require.NoError(t, err)
	assert.NotNil(t, idx, "index should still be created even if scan fails")
	assert.Equal(t, int64(0), idx.Count(), "index should be empty since scan failed")
}

// =============================================================================
// SearchSimilar Tests - Coverage Improvement
// =============================================================================

func TestVectorSearchSimilar_SuccessWithResults(t *testing.T) {
	mockIdx := &searchMockHNSWIndex{
		searchFn: func(query []float32, k int) ([]*interfaces.HNSWSearchResult, error) {
			return []*interfaces.HNSWSearchResult{
				{ID: 1, Distance: 0.1},
				{ID: 2, Distance: 0.5},
			}, nil
		},
	}

	vs, storage := newSearchTestService(t, "search-ns", 3, mockIdx)

	// Populate storage with vectors for IncludeVectors/IncludeMetadata path.
	// Use the scoped name — this is what SearchSimilar queries against.
	scopedNS := scopedNamespace(tenantid.System, "search-ns")
	require.NoError(t, storage.UpsertVector(context.Background(), scopedNS, &vector.VectorValue{
		ID:        1,
		Embedding: []float32{1.0, 0.0, 0.0},
		Metadata:  map[string]string{"category": "A"},
	}))
	require.NoError(t, storage.UpsertVector(context.Background(), scopedNS, &vector.VectorValue{
		ID:        2,
		Embedding: []float32{0.0, 1.0, 0.0},
		Metadata:  map[string]string{"category": "B"},
	}))

	req := &SearchSimilarRequest{
		Namespace:       "search-ns",
		QueryVector:     []float32{1.0, 0.0, 0.0},
		K:               5,
		IncludeVectors:  true,
		IncludeMetadata: true,
	}

	resp, err := vs.SearchSimilar(context.Background(), req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Len(t, resp.Results, 2)
	assert.Equal(t, uint64(1), resp.Results[0].ID)
	assert.NotNil(t, resp.Results[0].Vector)
	assert.NotNil(t, resp.Results[0].Metadata)
	assert.NotZero(t, resp.Timestamp)
}

func TestVectorSearchSimilar_WithMinScoreFiltering(t *testing.T) {
	mockIdx := &searchMockHNSWIndex{
		searchFn: func(query []float32, k int) ([]*interfaces.HNSWSearchResult, error) {
			return []*interfaces.HNSWSearchResult{
				{ID: 1, Distance: 0.1},
				{ID: 2, Distance: 0.8},
				{ID: 3, Distance: 1.5},
			}, nil
		},
	}

	vs, _ := newSearchTestService(t, "minscore-ns", 3, mockIdx)

	req := &SearchSimilarRequest{
		Namespace:   "minscore-ns",
		QueryVector: []float32{1.0, 0.0, 0.0},
		K:           10,
		MinScore:    1.0, // Only results with Distance <= 1.0
	}

	resp, err := vs.SearchSimilar(context.Background(), req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	// Results with distance > MinScore are filtered out
	assert.Len(t, resp.Results, 2, "only results with distance <= MinScore should remain")
}

func TestVectorSearchSimilar_WithMetadataFilter(t *testing.T) {
	mockIdx := &searchMockHNSWIndex{
		searchFn: func(query []float32, k int) ([]*interfaces.HNSWSearchResult, error) {
			return []*interfaces.HNSWSearchResult{
				{ID: 1, Distance: 0.1},
				{ID: 2, Distance: 0.2},
			}, nil
		},
	}

	vs, storage := newSearchTestService(t, "metafilter-ns", 3, mockIdx)

	require.NoError(t, storage.UpsertVector(context.Background(), testScopedNS("metafilter-ns"), &vector.VectorValue{
		ID:        1,
		Embedding: []float32{1.0, 0.0, 0.0},
		Metadata:  map[string]string{"category": "A"},
	}))
	require.NoError(t, storage.UpsertVector(context.Background(), testScopedNS("metafilter-ns"), &vector.VectorValue{
		ID:        2,
		Embedding: []float32{0.0, 1.0, 0.0},
		Metadata:  map[string]string{"category": "B"},
	}))

	req := &SearchSimilarRequest{
		Namespace:       "metafilter-ns",
		QueryVector:     []float32{1.0, 0.0, 0.0},
		K:               10,
		IncludeMetadata: true,
		MetadataFilter:  map[string]interface{}{"category": "A"},
	}

	resp, err := vs.SearchSimilar(context.Background(), req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Len(t, resp.Results, 1, "only vectors matching metadata filter should remain")
	assert.Equal(t, uint64(1), resp.Results[0].ID)
}

func TestVectorSearchSimilar_IndexSearchError(t *testing.T) {
	mockIdx := &searchMockHNSWIndex{
		searchFn: func(query []float32, k int) ([]*interfaces.HNSWSearchResult, error) {
			return nil, errors.New("search engine failure")
		},
	}

	vs, _ := newSearchTestService(t, "error-ns", 3, mockIdx)

	req := &SearchSimilarRequest{
		Namespace:   "error-ns",
		QueryVector: []float32{1.0, 0.0, 0.0},
		K:           5,
	}

	resp, err := vs.SearchSimilar(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)

	requireErrorAs[*VectorOperationError](t, err)
}

func TestVectorSearchSimilar_EmptyIndexReturnsEmptyResults(t *testing.T) {
	mockIdx := &searchMockHNSWIndex{
		searchFn: func(query []float32, k int) ([]*interfaces.HNSWSearchResult, error) {
			return nil, errors.New("index is empty")
		},
	}

	vs, _ := newSearchTestService(t, "empty-ns", 3, mockIdx)

	req := &SearchSimilarRequest{
		Namespace:   "empty-ns",
		QueryVector: []float32{1.0, 0.0, 0.0},
		K:           5,
	}

	// The error from mock is not hnsw.ErrEmptyIndex (different error value),
	// so it returns as a search error
	resp, err := vs.SearchSimilar(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVectorSearchSimilar_ClosedService(t *testing.T) {
	mockIdx := &searchMockHNSWIndex{}
	vs, _ := newSearchTestService(t, "closed-ns", 3, mockIdx)

	// Mark the service as closed
	vs.mu.Lock()
	vs.closed = true
	vs.mu.Unlock()

	req := &SearchSimilarRequest{
		Namespace:   "closed-ns",
		QueryVector: []float32{1.0, 0.0, 0.0},
		K:           5,
	}

	resp, err := vs.SearchSimilar(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)

	requireErrorAs[*VectorServiceClosedError](t, err)
}

func TestVectorSearchSimilar_TenantNamespaceIsolation(t *testing.T) {
	// Scoped storage keys guarantee that tenant "my-tenant" cannot retrieve
	// search results from tenant "other-tenant"'s namespace. Each tenant's
	// namespace resolves to a different storage key, so searching the same
	// logical name from a different tenant returns empty results (the intruder
	// has no index for their own scoped key).
	mockIdx := &searchMockHNSWIndex{
		searchFn: func(query []float32, k int) ([]*interfaces.HNSWSearchResult, error) {
			return []*interfaces.HNSWSearchResult{{ID: 1, Distance: 0.1}}, nil
		},
	}

	// Set up the namespace under the system tenant (default when no ctx tenant).
	vs, _ := newSearchTestService(t, "tenant-ns", 3, mockIdx)

	// A completely different tenant ("my-tenant") searches the same logical
	// name → they see their own (empty) scoped namespace.
	ctx := tenant.WithTenantID(context.Background(), "my-tenant")
	req := &SearchSimilarRequest{
		Namespace:   "tenant-ns",
		QueryVector: []float32{1.0, 0.0, 0.0},
		K:           5,
	}

	resp, err := vs.SearchSimilar(ctx, req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	// Empty because "my-tenant" has no vectors in their own scoped namespace.
	assert.Empty(t, resp.Results)
}

func TestVectorSearchSimilar_NamespaceDoesNotExist(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	vs := svc.(*vectorService)

	req := &SearchSimilarRequest{
		Namespace:   "does-not-exist",
		QueryVector: []float32{1.0, 0.0, 0.0},
		K:           5,
	}

	// When ensureHNSWIndex fails, SearchSimilar returns empty results (not error)
	resp, err := vs.SearchSimilar(context.Background(), req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Empty(t, resp.Results)
}

func TestVectorSearchSimilar_GetVectorErrorDuringEnrichment(t *testing.T) {
	mockIdx := &searchMockHNSWIndex{
		searchFn: func(query []float32, k int) ([]*interfaces.HNSWSearchResult, error) {
			return []*interfaces.HNSWSearchResult{
				{ID: 1, Distance: 0.1},
				{ID: 99, Distance: 0.2}, // This ID won't exist in storage
			}, nil
		},
	}

	vs, storage := newSearchTestService(t, "enrich-ns", 3, mockIdx)

	require.NoError(t, storage.UpsertVector(context.Background(), testScopedNS("enrich-ns"), &vector.VectorValue{
		ID:        1,
		Embedding: []float32{1.0, 0.0, 0.0},
		Metadata:  map[string]string{"k": "v"},
	}))

	req := &SearchSimilarRequest{
		Namespace:       "enrich-ns",
		QueryVector:     []float32{1.0, 0.0, 0.0},
		K:               10,
		IncludeVectors:  true,
		IncludeMetadata: true,
	}

	resp, err := vs.SearchSimilar(context.Background(), req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	// Both results should be included, but ID 99 won't have vector/metadata
	assert.Len(t, resp.Results, 2)
	// ID 1 has vector enrichment
	assert.NotNil(t, resp.Results[0].Vector)
	// ID 99 - GetVector fails, but result is still included without enrichment
	assert.Nil(t, resp.Results[1].Vector)
}

// =============================================================================
// SearchByID Tests
// =============================================================================

func TestVectorSearchByID_Success(t *testing.T) {
	mockIdx := &searchMockHNSWIndex{
		searchFn: func(query []float32, k int) ([]*interfaces.HNSWSearchResult, error) {
			return []*interfaces.HNSWSearchResult{
				{ID: 1, Distance: 0.0}, // self
				{ID: 2, Distance: 0.3},
				{ID: 3, Distance: 0.7},
			}, nil
		},
	}

	vs, storage := newSearchTestService(t, "byid-ns", 3, mockIdx)

	// Source vector that SearchByID will fetch first
	require.NoError(t, storage.UpsertVector(context.Background(), testScopedNS("byid-ns"), &vector.VectorValue{
		ID:        1,
		Embedding: []float32{1.0, 0.0, 0.0},
	}))

	req := &SearchByIDRequest{
		Namespace:   "byid-ns",
		ID:          1,
		K:           2,
		ExcludeSelf: true,
	}

	resp, err := vs.SearchByID(context.Background(), req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, uint64(1), resp.SourceID)
	// ExcludeSelf=true should filter out ID 1
	for _, r := range resp.Results {
		assert.NotEqual(t, uint64(1), r.ID, "source ID should be excluded")
	}
	assert.LessOrEqual(t, len(resp.Results), 2)
}

func TestVectorSearchByID_NilRequest(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	vs := svc.(*vectorService)

	resp, err := vs.SearchByID(context.Background(), nil)

	require.Error(t, err)
	assert.Nil(t, resp)

	requireErrorAs[*InvalidVectorError](t, err)
}

func TestVectorSearchByID_ZeroID(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	vs := svc.(*vectorService)

	req := &SearchByIDRequest{
		Namespace: "test-ns",
		ID:        0,
		K:         5,
	}

	resp, err := vs.SearchByID(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)

	requireErrorAs[*InvalidVectorError](t, err)
}

func TestVectorSearchByID_InvalidK(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	vs := svc.(*vectorService)

	req := &SearchByIDRequest{
		Namespace: "test-ns",
		ID:        1,
		K:         0,
	}

	resp, err := vs.SearchByID(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)

	requireErrorAs[*InvalidVectorError](t, err)
}

func TestVectorSearchByID_SourceVectorNotFound(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()

	// Create namespace in storage
	require.NoError(t, storage.CreateNamespace(context.Background(), &vector.NamespaceInfo{
		Name:      "notfound-ns",
		Dimension: 3,
	}))

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	vs := svc.(*vectorService)

	req := &SearchByIDRequest{
		Namespace: "notfound-ns",
		ID:        999, // Does not exist in storage
		K:         5,
	}

	resp, err := vs.SearchByID(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)

	requireErrorAs[*NamespaceNotFoundError](t, err)
}

func TestVectorSearchByID_WithoutExcludeSelf(t *testing.T) {
	mockIdx := &searchMockHNSWIndex{
		searchFn: func(query []float32, k int) ([]*interfaces.HNSWSearchResult, error) {
			return []*interfaces.HNSWSearchResult{
				{ID: 1, Distance: 0.0},
				{ID: 2, Distance: 0.5},
			}, nil
		},
	}

	vs, storage := newSearchTestService(t, "noexclude-ns", 3, mockIdx)

	require.NoError(t, storage.UpsertVector(context.Background(), testScopedNS("noexclude-ns"), &vector.VectorValue{
		ID:        1,
		Embedding: []float32{1.0, 0.0, 0.0},
	}))

	req := &SearchByIDRequest{
		Namespace:   "noexclude-ns",
		ID:          1,
		K:           5,
		ExcludeSelf: false,
	}

	resp, err := vs.SearchByID(context.Background(), req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, uint64(1), resp.SourceID)
	// Without ExcludeSelf, the source vector may appear in results
	assert.Len(t, resp.Results, 2)
}

func TestVectorSearchByID_TenantNamespaceIsolation(t *testing.T) {
	// A tenant that never inserted a vector into their scoped namespace gets
	// ErrNotFound when searching by ID (the source vector does not exist in
	// their own scoped store, regardless of whether another tenant has it).
	storage := newMockVectorStorage()
	logger := newMockLogger()

	// Seed the namespace under "owner-tenant" using the scoped key.
	ownerScoped := scopedNamespace("owner-tenant", "tenant-byid-ns")
	require.NoError(t, storage.CreateNamespace(context.Background(), &vector.NamespaceInfo{
		Name:      ownerScoped,
		Dimension: 3,
		TenantID:  "owner-tenant",
	}))
	require.NoError(t, storage.UpsertVector(context.Background(), ownerScoped, &vector.VectorValue{
		ID:        1,
		Embedding: []float32{1.0, 0.0, 0.0},
	}))

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	vs := svc.(*vectorService)

	// "different-tenant" cannot find the vector because their scoped key is
	// different → storage returns ErrNotFound, bubbling up to the caller.
	ctx := tenant.WithTenantID(context.Background(), "different-tenant")
	req := &SearchByIDRequest{
		Namespace: "tenant-byid-ns",
		ID:        1,
		K:         5,
	}

	resp, err := vs.SearchByID(ctx, req)

	require.Error(t, err)
	assert.Nil(t, resp)

	requireErrorAs[*NamespaceNotFoundError](t, err)
}

func TestVectorSearchByID_ExcludeSelfTruncatesResults(t *testing.T) {
	// Return more results than K to exercise the truncation path
	mockIdx := &searchMockHNSWIndex{
		searchFn: func(query []float32, k int) ([]*interfaces.HNSWSearchResult, error) {
			results := make([]*interfaces.HNSWSearchResult, 0, k)
			for i := 0; i < k; i++ {
				results = append(results, &interfaces.HNSWSearchResult{
					ID:       uint64(i + 1),
					Distance: float32(i) * 0.1,
				})
			}
			return results, nil
		},
	}

	vs, storage := newSearchTestService(t, "truncate-ns", 3, mockIdx)

	require.NoError(t, storage.UpsertVector(context.Background(), testScopedNS("truncate-ns"), &vector.VectorValue{
		ID:        5,
		Embedding: []float32{1.0, 0.0, 0.0},
	}))

	req := &SearchByIDRequest{
		Namespace:   "truncate-ns",
		ID:          5,
		K:           2,
		ExcludeSelf: true,
	}

	resp, err := vs.SearchByID(context.Background(), req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	// With ExcludeSelf, the source ID is filtered and results truncated to K
	assert.LessOrEqual(t, len(resp.Results), 2)
	for _, r := range resp.Results {
		assert.NotEqual(t, uint64(5), r.ID)
	}
}

// =============================================================================
// HybridSearch Tests - Coverage Improvement
// =============================================================================

func TestVectorHybridSearch_SuccessWithMetadataFiltering(t *testing.T) {
	mockIdx := &searchMockHNSWIndex{
		searchFn: func(query []float32, k int) ([]*interfaces.HNSWSearchResult, error) {
			return []*interfaces.HNSWSearchResult{
				{ID: 1, Distance: 0.1},
				{ID: 2, Distance: 0.2},
				{ID: 3, Distance: 0.3},
			}, nil
		},
	}

	vs, storage := newSearchTestService(t, "hybrid-ns", 3, mockIdx)

	require.NoError(t, storage.UpsertVector(context.Background(), testScopedNS("hybrid-ns"), &vector.VectorValue{
		ID:        1,
		Embedding: []float32{1.0, 0.0, 0.0},
		Metadata:  map[string]string{"category": "A"},
	}))
	require.NoError(t, storage.UpsertVector(context.Background(), testScopedNS("hybrid-ns"), &vector.VectorValue{
		ID:        2,
		Embedding: []float32{0.0, 1.0, 0.0},
		Metadata:  map[string]string{"category": "B"},
	}))
	require.NoError(t, storage.UpsertVector(context.Background(), testScopedNS("hybrid-ns"), &vector.VectorValue{
		ID:        3,
		Embedding: []float32{0.0, 0.0, 1.0},
		Metadata:  map[string]string{"category": "A"},
	}))

	req := &HybridSearchRequest{
		Namespace:       "hybrid-ns",
		QueryVector:     []float32{1.0, 0.0, 0.0},
		K:               2,
		MetadataFilter:  map[string]interface{}{"category": "A"},
		IncludeMetadata: true,
		IncludeVectors:  true,
	}

	resp, err := vs.HybridSearch(context.Background(), req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, 3, resp.TotalCandidates)
	// Only category "A" matches (IDs 1 and 3)
	assert.LessOrEqual(t, resp.TotalFiltered, 2)
	assert.NotZero(t, resp.Timestamp)
}

func TestVectorHybridSearch_EmptyNamespace(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	vs := svc.(*vectorService)

	req := &HybridSearchRequest{
		Namespace:   "",
		QueryVector: []float32{1.0, 0.0, 0.0},
		K:           5,
	}

	resp, err := vs.HybridSearch(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVectorHybridSearch_NoMetadataFilter(t *testing.T) {
	mockIdx := &searchMockHNSWIndex{
		searchFn: func(query []float32, k int) ([]*interfaces.HNSWSearchResult, error) {
			return []*interfaces.HNSWSearchResult{
				{ID: 1, Distance: 0.1},
				{ID: 2, Distance: 0.2},
			}, nil
		},
	}

	vs, storage := newSearchTestService(t, "hybrid-nofilter-ns", 3, mockIdx)

	require.NoError(t, storage.UpsertVector(context.Background(), testScopedNS("hybrid-nofilter-ns"), &vector.VectorValue{
		ID:        1,
		Embedding: []float32{1.0, 0.0, 0.0},
		Metadata:  map[string]string{"k": "v"},
	}))
	require.NoError(t, storage.UpsertVector(context.Background(), testScopedNS("hybrid-nofilter-ns"), &vector.VectorValue{
		ID:        2,
		Embedding: []float32{0.0, 1.0, 0.0},
		Metadata:  map[string]string{"k": "v2"},
	}))

	req := &HybridSearchRequest{
		Namespace:       "hybrid-nofilter-ns",
		QueryVector:     []float32{1.0, 0.0, 0.0},
		K:               5,
		MetadataFilter:  nil, // No filter means all results pass
		IncludeMetadata: true,
	}

	resp, err := vs.HybridSearch(context.Background(), req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	// All candidates should pass with no metadata filter
	assert.Equal(t, resp.TotalCandidates, resp.TotalFiltered)
}

func TestVectorHybridSearch_SearchSimilarFailure(t *testing.T) {
	// Trigger SearchSimilar to fail by having an invalid request propagated
	storage := newMockVectorStorage()
	logger := newMockLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	vs := svc.(*vectorService)

	req := &HybridSearchRequest{
		Namespace:   "test-ns",
		QueryVector: []float32{}, // Empty vector fails validation in SearchSimilar
		K:           5,
	}

	resp, err := vs.HybridSearch(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestVectorHybridSearch_KLimitsFilteredResults(t *testing.T) {
	mockIdx := &searchMockHNSWIndex{
		searchFn: func(query []float32, k int) ([]*interfaces.HNSWSearchResult, error) {
			return []*interfaces.HNSWSearchResult{
				{ID: 1, Distance: 0.1},
				{ID: 2, Distance: 0.2},
				{ID: 3, Distance: 0.3},
				{ID: 4, Distance: 0.4},
			}, nil
		},
	}

	vs, storage := newSearchTestService(t, "klimit-ns", 3, mockIdx)

	for i := uint64(1); i <= 4; i++ {
		require.NoError(t, storage.UpsertVector(context.Background(), testScopedNS("klimit-ns"), &vector.VectorValue{
			ID:        i,
			Embedding: []float32{float32(i), 0.0, 0.0},
			Metadata:  map[string]string{"pass": "yes"},
		}))
	}

	req := &HybridSearchRequest{
		Namespace:       "klimit-ns",
		QueryVector:     []float32{1.0, 0.0, 0.0},
		K:               2, // Request only 2 results
		MetadataFilter:  map[string]interface{}{"pass": "yes"},
		IncludeMetadata: true,
	}

	resp, err := vs.HybridSearch(context.Background(), req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.LessOrEqual(t, resp.TotalFiltered, 2, "should not exceed K")
}

func TestVectorHybridSearch_TenantNamespacesAreIsolated(t *testing.T) {
	// Scoped namespaces guarantee that "intruder" cannot retrieve vectors
	// stored under "owner" even when both use the same logical namespace name.
	// "owner" creates the namespace and inserts a vector.
	// "intruder" HybridSearch on the same logical name receives empty results
	// because the service resolves a different scoped storage key.
	storage := newMockVectorStorage()
	logger := newMockLogger()

	ownerCtx := tenant.WithTenantID(context.Background(), "owner")
	ownerScoped := scopedNamespace("owner", "hybrid-tenant-ns")

	require.NoError(t, storage.CreateNamespace(context.Background(), &vector.NamespaceInfo{
		Name:      ownerScoped,
		Dimension: 3,
		TenantID:  "owner",
	}))
	require.NoError(t, storage.UpsertVector(ownerCtx, ownerScoped, &vector.VectorValue{
		ID:        1,
		Embedding: []float32{1.0, 0.0, 0.0},
	}))

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	// Owner can search their own namespace.
	ownerReq := &HybridSearchRequest{
		Namespace:   "hybrid-tenant-ns",
		QueryVector: []float32{1.0, 0.0, 0.0},
		K:           5,
	}
	ownerResp, err := svc.HybridSearch(ownerCtx, ownerReq)
	require.NoError(t, err)
	require.NotNil(t, ownerResp)

	// Intruder searches the same logical namespace but gets empty results —
	// they see their own (empty) scoped namespace, not owner's data.
	intruderCtx := tenant.WithTenantID(context.Background(), "intruder")
	intruderReq := &HybridSearchRequest{
		Namespace:   "hybrid-tenant-ns",
		QueryVector: []float32{1.0, 0.0, 0.0},
		K:           5,
	}
	intruderResp, err := svc.HybridSearch(intruderCtx, intruderReq)
	require.NoError(t, err)
	require.NotNil(t, intruderResp)
	assert.Empty(t, intruderResp.Results, "intruder must not see owner's vectors")
}
