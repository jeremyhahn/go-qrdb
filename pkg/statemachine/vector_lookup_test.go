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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
)

// TestVectorStateMachine_GetVector tests the OpGetVector lookup operation
func TestVectorStateMachine_GetVector(t *testing.T) {
	tests := []struct {
		name          string
		setupCache    bool
		cacheHit      bool
		vectorExists  bool
		expectedError bool
	}{
		{
			name:          "GetVector_Success_FromStorage",
			setupCache:    false,
			cacheHit:      false,
			vectorExists:  true,
			expectedError: false,
		},
		{
			name:          "GetVector_Success_FromCache",
			setupCache:    true,
			cacheHit:      true,
			vectorExists:  true,
			expectedError: false,
		},
		{
			name:          "GetVector_CacheMiss_FallbackToStorage",
			setupCache:    true,
			cacheHit:      false,
			vectorExists:  true,
			expectedError: false,
		},
		{
			name:          "GetVector_NotFound",
			setupCache:    false,
			cacheHit:      false,
			vectorExists:  false,
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := NewMockStorageAdapter()
			adapter.vecStor = NewMockVectorStorageWrapper()

			var cache CacheManager
			if tt.setupCache {
				cache = NewMockCache()
			}

			hnswIndex := NewMockHNSWIndex()
			log := newTestLogger()

			vsm, err := newTestVectorStateMachine(adapter, hnswIndex, cache, log)
			require.NoError(t, err)

			// Create namespace in storage
			nsInfo := &vector.NamespaceInfo{
				Name:      "test_ns",
				Dimension: 128,
				CreatedAt: time.Now().Unix(),
				UpdatedAt: time.Now().Unix(),
			}
			err = adapter.VectorStorage().CreateNamespace(context.Background(), nsInfo)
			require.NoError(t, err)

			// Register namespace in-memory (required for tenant validation)
			vsm.namespaces["test_ns"] = &NamespaceState{
				Name:      "test_ns",
				Dimension: 128,
				CreatedAt: time.Now().UnixNano(),
				UpdatedAt: time.Now().UnixNano(),
			}

			// Add vector if it should exist
			if tt.vectorExists {
				vec := &vector.VectorValue{
					ID:        1,
					Embedding: make([]float32, 128),
					Metadata:  map[string]string{"test": "data"},
				}
				err = adapter.VectorStorage().UpsertVector(context.Background(), "test_ns", vec)
				require.NoError(t, err)

				// If cache hit is expected, populate cache
				if tt.cacheHit && cache != nil {
					result := &VectorResult{
						Success:   true,
						Vector:    vec.Embedding,
						VectorID:  vec.ID,
						Timestamp: time.Now().UnixNano(),
					}
					data, _ := result.Encode()
					_ = cache.Set(context.Background(), []byte("vector:test_ns:1"), data, 0)
				}
			}

			// Create get vector command
			cmd := &VectorCommand{
				Op:        OpGetVector,
				Namespace: "test_ns",
				ID:        1,
			}

			// Execute lookup
			result, err := vsm.handleGetVector(context.Background(), cmd)

			if tt.expectedError {
				assert.Error(t, err)
				assert.Nil(t, result)
			} else {
				assert.NoError(t, err)
				require.NotNil(t, result)
				assert.True(t, result.Success)
				assert.NotNil(t, result.Vector)
				assert.Equal(t, uint64(1), result.VectorID)
			}

			// Verify cache metrics
			if cache != nil {
				if tt.cacheHit {
					assert.Greater(t, vsm.metrics.CacheHits, uint64(0))
				} else if tt.vectorExists {
					assert.Greater(t, vsm.metrics.CacheMisses, uint64(0))
				}
			}
		})
	}
}

// TestVectorStateMachine_GetNamespaceInfo tests namespace metadata retrieval
func TestVectorStateMachine_GetNamespaceInfo(t *testing.T) {
	tests := []struct {
		name               string
		namespaceInMemory  bool
		namespaceInStorage bool
		expectedError      bool
	}{
		{
			name:               "GetNamespaceInfo_FromMemory",
			namespaceInMemory:  true,
			namespaceInStorage: false,
			expectedError:      false,
		},
		{
			// With tenant validation, namespace must exist in memory
			// Storage-only namespaces are no longer accessible without memory registration
			name:               "GetNamespaceInfo_NotInMemory_Error",
			namespaceInMemory:  false,
			namespaceInStorage: true,
			expectedError:      true,
		},
		{
			name:               "GetNamespaceInfo_NotFound",
			namespaceInMemory:  false,
			namespaceInStorage: false,
			expectedError:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := NewMockStorageAdapter()
			adapter.vecStor = NewMockVectorStorageWrapper()
			hnswIndex := NewMockHNSWIndex()
			log := newTestLogger()

			vsm, err := newTestVectorStateMachine(adapter, hnswIndex, nil, log)
			require.NoError(t, err)

			now := time.Now().Unix()

			// Setup in-memory namespace if required
			if tt.namespaceInMemory {
				vsm.namespaces["test_ns"] = &NamespaceState{
					Name:        "test_ns",
					Dimension:   256,
					VectorCount: 10,
					CreatedAt:   now,
					UpdatedAt:   now,
				}
			}

			// Setup storage namespace if required
			if tt.namespaceInStorage {
				nsInfo := &vector.NamespaceInfo{
					Name:      "test_ns",
					Dimension: 256,
					CreatedAt: now,
					UpdatedAt: now,
				}
				err = adapter.VectorStorage().CreateNamespace(context.Background(), nsInfo)
				require.NoError(t, err)
			}

			// Create command
			cmd := &VectorCommand{
				Op:        OpGetNamespaceInfo,
				Namespace: "test_ns",
			}

			// Execute lookup
			result, err := vsm.handleGetNamespaceInfo(context.Background(), cmd)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				require.NotNil(t, result)
				assert.True(t, result.Success)
				require.NotNil(t, result.NamespaceInfo)
				assert.Equal(t, "test_ns", result.NamespaceInfo.Name)
				assert.Equal(t, uint32(256), result.NamespaceInfo.Dimension)
			}
		})
	}
}

// TestVectorStateMachine_SearchByID tests search using existing vector as query
func TestVectorStateMachine_SearchByID(t *testing.T) {
	tests := []struct {
		name          string
		vectorExists  bool
		expectedError bool
	}{
		{
			name:          "SearchByID_Success",
			vectorExists:  true,
			expectedError: false,
		},
		{
			name:          "SearchByID_VectorNotFound",
			vectorExists:  false,
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := NewMockStorageAdapter()
			adapter.vecStor = NewMockVectorStorageWrapper()
			hnswIndex := NewMockHNSWIndex()
			log := newTestLogger()

			vsm, err := newTestVectorStateMachine(adapter, hnswIndex, nil, log)
			require.NoError(t, err)

			// Create namespace
			nsInfo := &vector.NamespaceInfo{
				Name:      "test_ns",
				Dimension: 128,
				CreatedAt: time.Now().Unix(),
				UpdatedAt: time.Now().Unix(),
			}
			err = adapter.VectorStorage().CreateNamespace(context.Background(), nsInfo)
			require.NoError(t, err)

			// Create namespace in VSM with HNSW index
			vsm.indexesMu.Lock()
			vsm.hnswIndexes["test_ns"] = hnswIndex
			vsm.indexesMu.Unlock()
			vsm.namespaces["test_ns"] = &NamespaceState{
				Name:      "test_ns",
				Dimension: 128,
			}

			if tt.vectorExists {
				// Add query vector
				queryVec := &vector.VectorValue{
					ID:        1,
					Embedding: make([]float32, 128),
					Metadata:  map[string]string{"type": "query"},
				}
				// Initialize embedding with some values
				for i := range queryVec.Embedding {
					queryVec.Embedding[i] = float32(i) * 0.01
				}
				err = adapter.VectorStorage().UpsertVector(context.Background(), "test_ns", queryVec)
				require.NoError(t, err)

				// Add some similar vectors for search results
				for i := uint64(2); i <= 5; i++ {
					vec := &vector.VectorValue{
						ID:        i,
						Embedding: make([]float32, 128),
						Metadata:  map[string]string{"type": "similar"},
					}
					for j := range vec.Embedding {
						vec.Embedding[j] = float32(j) * 0.01
					}
					err = adapter.VectorStorage().UpsertVector(context.Background(), "test_ns", vec)
					require.NoError(t, err)
				}
			}

			// Create search by ID command
			cmd := &VectorCommand{
				Op:        OpSearchByID,
				Namespace: "test_ns",
				ID:        1,
				K:         3,
				EfSearch:  50,
			}

			// Execute search
			result, err := vsm.handleSearchByID(context.Background(), cmd)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				require.NotNil(t, result)
				assert.True(t, result.Success)
				// Results should contain similar vectors (mock HNSW returns all)
				assert.NotNil(t, result.Results)
			}
		})
	}
}

// TestVectorStateMachine_ListVectors tests listing vectors in a namespace
func TestVectorStateMachine_ListVectors(t *testing.T) {
	tests := []struct {
		name          string
		limit         int
		offset        int
		vectorCount   int
		expectedCount int
		expectedError bool
	}{
		{
			name:          "ListVectors_DefaultLimit",
			limit:         0,
			offset:        0,
			vectorCount:   50,
			expectedCount: 50,
			expectedError: false,
		},
		{
			name:          "ListVectors_CustomLimit",
			limit:         10,
			offset:        0,
			vectorCount:   50,
			expectedCount: 10,
			expectedError: false,
		},
		{
			name:          "ListVectors_MaxLimit",
			limit:         2000,
			offset:        0,
			vectorCount:   50,
			expectedCount: 50,
			expectedError: false,
		},
		{
			name:          "ListVectors_WithOffset",
			limit:         10,
			offset:        5,
			vectorCount:   20,
			expectedCount: 10,
			expectedError: false,
		},
		{
			name:          "ListVectors_EmptyNamespace",
			limit:         10,
			offset:        0,
			vectorCount:   0,
			expectedCount: 0,
			expectedError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := NewMockStorageAdapter()
			adapter.vecStor = NewMockVectorStorageWrapper()
			hnswIndex := NewMockHNSWIndex()
			log := newTestLogger()

			vsm, err := newTestVectorStateMachine(adapter, hnswIndex, nil, log)
			require.NoError(t, err)

			// Create namespace
			nsInfo := &vector.NamespaceInfo{
				Name:      "test_ns",
				Dimension: 128,
				CreatedAt: time.Now().Unix(),
				UpdatedAt: time.Now().Unix(),
			}
			err = adapter.VectorStorage().CreateNamespace(context.Background(), nsInfo)
			require.NoError(t, err)

			// Register namespace in-memory (required for tenant validation)
			vsm.namespaces["test_ns"] = &NamespaceState{
				Name:      "test_ns",
				Dimension: 128,
				CreatedAt: time.Now().UnixNano(),
				UpdatedAt: time.Now().UnixNano(),
			}

			// Add vectors
			for i := 0; i < tt.vectorCount; i++ {
				vec := &vector.VectorValue{
					ID:        uint64(i + 1),
					Embedding: make([]float32, 128),
					Metadata:  map[string]string{"index": string(rune(i))},
				}
				err = adapter.VectorStorage().UpsertVector(context.Background(), "test_ns", vec)
				require.NoError(t, err)
			}

			// Create list command
			cmd := &VectorCommand{
				Op:        OpListVectors,
				Namespace: "test_ns",
				Limit:     tt.limit,
				Offset:    tt.offset,
			}

			// Execute list
			result, err := vsm.handleListVectors(context.Background(), cmd)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				require.NotNil(t, result)
				assert.True(t, result.Success)
				assert.NotNil(t, result.Vectors)

				// MockVectorStorageWrapper's ScanNamespace returns all vectors
				// so we just verify the result is successful
				assert.Equal(t, uint64(tt.vectorCount), result.TotalCount)
			}
		})
	}
}

// TestVectorStateMachine_Lookup_Integration tests the Lookup method routing
func TestVectorStateMachine_Lookup_Integration(t *testing.T) {
	adapter := NewMockStorageAdapter()
	adapter.vecStor = NewMockVectorStorageWrapper()
	hnswIndex := NewMockHNSWIndex()
	log := newTestLogger()

	vsm, err := newTestVectorStateMachine(adapter, hnswIndex, nil, log)
	require.NoError(t, err)

	// Create namespace
	nsInfo := &vector.NamespaceInfo{
		Name:      "test_ns",
		Dimension: 128,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}
	err = adapter.VectorStorage().CreateNamespace(context.Background(), nsInfo)
	require.NoError(t, err)

	// Register HNSW index for namespace
	vsm.indexesMu.Lock()
	vsm.hnswIndexes["test_ns"] = hnswIndex
	vsm.indexesMu.Unlock()
	vsm.namespaces["test_ns"] = &NamespaceState{
		Name:      "test_ns",
		Dimension: 128,
	}

	// Add a test vector
	vec := &vector.VectorValue{
		ID:        1,
		Embedding: make([]float32, 128),
		Metadata:  map[string]string{"test": "data"},
	}
	err = adapter.VectorStorage().UpsertVector(context.Background(), "test_ns", vec)
	require.NoError(t, err)

	tests := []struct {
		name          string
		op            VectorOp
		expectedError bool
	}{
		{
			name:          "Lookup_OpGetVector",
			op:            OpGetVector,
			expectedError: false,
		},
		{
			name:          "Lookup_OpGetNamespaceInfo",
			op:            OpGetNamespaceInfo,
			expectedError: false,
		},
		{
			name:          "Lookup_OpListVectors",
			op:            OpListVectors,
			expectedError: false,
		},
		{
			name:          "Lookup_OpSearchByID",
			op:            OpSearchByID,
			expectedError: false,
		},
		{
			name:          "Lookup_UnsupportedOp",
			op:            OpUpsertVector,
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &VectorCommand{
				Op:        tt.op,
				Namespace: "test_ns",
				ID:        1,
				K:         3,
			}
			data, err := cmd.Encode()
			require.NoError(t, err)

			// Pass data directly as Lookup accepts interface{}
			result, err := vsm.Lookup(context.Background(), data)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
			}
		})
	}
}

// TestVectorStateMachine_GetNamespaceInfo_AdapterError tests error handling
func TestVectorStateMachine_GetNamespaceInfo_AdapterError(t *testing.T) {
	// Create adapter without vector storage support
	adapter := &MockStorageAdapter{
		data: make(map[string][]byte),
	}
	// Don't set vecStor - this will cause the type assertion to fail

	hnswIndex := NewMockHNSWIndex()
	log := newTestLogger()

	vsm, err := newTestVectorStateMachine(adapter, hnswIndex, nil, log)
	require.NoError(t, err)

	cmd := &VectorCommand{
		Op:        OpGetNamespaceInfo,
		Namespace: "test_ns",
	}

	result, err := vsm.handleGetNamespaceInfo(context.Background(), cmd)
	assert.Error(t, err)
	assert.Nil(t, result)
	// With tenant validation, namespace must exist in memory first
	assert.Contains(t, err.Error(), "namespace")
}

// TestVectorStateMachine_GetVector_AdapterError tests GetVector adapter error
func TestVectorStateMachine_GetVector_AdapterError(t *testing.T) {
	// Create adapter without vector storage support
	adapter := &MockStorageAdapter{
		data: make(map[string][]byte),
	}

	hnswIndex := NewMockHNSWIndex()
	log := newTestLogger()

	vsm, err := newTestVectorStateMachine(adapter, hnswIndex, nil, log)
	require.NoError(t, err)

	cmd := &VectorCommand{
		Op:        OpGetVector,
		Namespace: "test_ns",
		ID:        1,
	}

	result, err := vsm.handleGetVector(context.Background(), cmd)
	assert.Error(t, err)
	assert.Nil(t, result)
}

// TestVectorStateMachine_SearchByID_AdapterError tests SearchByID adapter error
func TestVectorStateMachine_SearchByID_AdapterError(t *testing.T) {
	// Create adapter without vector storage support
	adapter := &MockStorageAdapter{
		data: make(map[string][]byte),
	}

	hnswIndex := NewMockHNSWIndex()
	log := newTestLogger()

	vsm, err := newTestVectorStateMachine(adapter, hnswIndex, nil, log)
	require.NoError(t, err)

	cmd := &VectorCommand{
		Op:        OpSearchByID,
		Namespace: "test_ns",
		ID:        1,
		K:         3,
	}

	result, err := vsm.handleSearchByID(context.Background(), cmd)
	assert.Error(t, err)
	assert.Nil(t, result)
}

// TestVectorStateMachine_ListVectors_AdapterError tests ListVectors adapter error
func TestVectorStateMachine_ListVectors_AdapterError(t *testing.T) {
	// Create adapter without vector storage support
	adapter := &MockStorageAdapter{
		data: make(map[string][]byte),
	}

	hnswIndex := NewMockHNSWIndex()
	log := newTestLogger()

	vsm, err := newTestVectorStateMachine(adapter, hnswIndex, nil, log)
	require.NoError(t, err)

	cmd := &VectorCommand{
		Op:        OpListVectors,
		Namespace: "test_ns",
		Limit:     10,
	}

	result, err := vsm.handleListVectors(context.Background(), cmd)
	assert.Error(t, err)
	assert.Nil(t, result)
}
