//go:build raft

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
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vmihailenco/msgpack/v5"

	"github.com/jeremyhahn/go-qrdb/pkg/api"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
	"github.com/jeremyhahn/go-qrdb/pkg/vector/hnsw"
)

// =============================================================================
// Simple Getter Tests (0% coverage)
// =============================================================================

func TestVectorStateMachine_GetShardID(t *testing.T) {
	t.Run("returns configured shard ID", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(42, 7, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		assert.Equal(t, uint64(42), vsm.GetShardID())
	})

	t.Run("returns zero shard ID", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(0, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		assert.Equal(t, uint64(0), vsm.GetShardID())
	})
}

func TestVectorStateMachine_GetReplicaID(t *testing.T) {
	t.Run("returns configured replica ID", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 99, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		assert.Equal(t, uint64(99), vsm.GetReplicaID())
	})

	t.Run("returns zero replica ID", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 0, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		assert.Equal(t, uint64(0), vsm.GetReplicaID())
	})
}

func TestVectorStateMachine_GetStateMachineType(t *testing.T) {
	t.Run("returns vector type", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		assert.Equal(t, StateMachineTypeVector, vsm.GetStateMachineType())
	})
}

func TestVectorStateMachine_GetStorageAdapter(t *testing.T) {
	t.Run("returns configured adapter", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		assert.Equal(t, adapter, vsm.GetStorageAdapter())
	})
}

// =============================================================================
// HealthCheck Tests (0% coverage)
// =============================================================================

func TestVectorStateMachine_HealthCheck(t *testing.T) {
	t.Run("healthy state machine", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		err = vsm.HealthCheck()
		assert.NoError(t, err)
	})

	t.Run("closed state machine returns error", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		err = vsm.Close(context.Background())
		require.NoError(t, err)

		err = vsm.HealthCheck()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "closed")
	})

	t.Run("nil adapter returns error", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		// Forcibly set adapter to nil to test the nil check
		vsm.adapter = nil

		err = vsm.HealthCheck()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "nil")
	})
}

// =============================================================================
// handleGetIndexProgress Tests (0% coverage)
// =============================================================================

func TestVectorStateMachine_HandleGetIndexProgress(t *testing.T) {
	t.Run("job not found returns not in progress", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		mockHNSW := NewMockHNSWIndex()
		log := newTestLogger()

		vsm, err := newTestVectorStateMachine(adapter, mockHNSW, nil, log)
		require.NoError(t, err)

		cmd := &VectorCommand{
			Op:    OpGetIndexProgress,
			JobID: "nonexistent-job",
		}

		result, err := vsm.Lookup(context.Background(), cmd)
		require.NoError(t, err)

		vecResult := result.(*VectorResult)
		assert.True(t, vecResult.Success)
		require.NotNil(t, vecResult.IndexProgress)
		assert.False(t, vecResult.IndexProgress.InProgress)
		assert.Equal(t, float64(100.0), vecResult.IndexProgress.PercentComplete)
	})

	t.Run("existing job with progress", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		adapter.vecStor = NewMockVectorStorageWrapper()
		mockHNSW := NewMockHNSWIndex()
		log := newTestLogger()

		vsm, err := newTestVectorStateMachine(adapter, mockHNSW, nil, log)
		require.NoError(t, err)

		// Create namespace and start a rebuild via Update
		setupTestNamespace(vsm, "test_ns", 128, mockHNSW)
		err = createTestNamespaceInStorage(adapter, "test_ns", 128)
		require.NoError(t, err)

		// Set vector count for progress calculation
		vsm.namespaces["test_ns"].VectorCount = 100

		// Start rebuild through Update
		rebuildCmd := &VectorCommand{
			Op:        OpRebuildIndex,
			Namespace: "test_ns",
			JobID:     "rebuild-job-1",
			Timestamp: time.Now().UnixNano(),
		}
		rebuildData, err := rebuildCmd.Encode()
		require.NoError(t, err)

		err = vsm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: rebuildData}}, make([]sm.Result, 1))
		require.NoError(t, err)

		// Query progress
		progressCmd := &VectorCommand{
			Op:    OpGetIndexProgress,
			JobID: "rebuild-job-1",
		}

		result, err := vsm.Lookup(context.Background(), progressCmd)
		require.NoError(t, err)

		vecResult := result.(*VectorResult)
		assert.True(t, vecResult.Success)
		require.NotNil(t, vecResult.IndexProgress)
		assert.True(t, vecResult.IndexProgress.InProgress)
		assert.Equal(t, "rebuild-job-1", vecResult.IndexProgress.JobID)
		assert.Equal(t, uint64(100), vecResult.IndexProgress.TotalVectors)
	})

	t.Run("completed job shows zero percent when no vectors", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		adapter.vecStor = NewMockVectorStorageWrapper()
		mockHNSW := NewMockHNSWIndex()
		log := newTestLogger()

		vsm, err := newTestVectorStateMachine(adapter, mockHNSW, nil, log)
		require.NoError(t, err)

		// Setup namespace with zero vectors
		setupTestNamespace(vsm, "empty_ns", 128, mockHNSW)
		err = createTestNamespaceInStorage(adapter, "empty_ns", 128)
		require.NoError(t, err)

		// Start rebuild on empty namespace
		rebuildCmd := &VectorCommand{
			Op:        OpRebuildIndex,
			Namespace: "empty_ns",
			JobID:     "empty-job",
			Timestamp: time.Now().UnixNano(),
		}
		rebuildData, err := rebuildCmd.Encode()
		require.NoError(t, err)

		err = vsm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: rebuildData}}, make([]sm.Result, 1))
		require.NoError(t, err)

		// Query progress - total vectors is 0 so percent should be 0
		progressCmd := &VectorCommand{
			Op:    OpGetIndexProgress,
			JobID: "empty-job",
		}

		result, err := vsm.Lookup(context.Background(), progressCmd)
		require.NoError(t, err)

		vecResult := result.(*VectorResult)
		assert.True(t, vecResult.Success)
		assert.Equal(t, float64(0), vecResult.IndexProgress.PercentComplete)
	})
}

// =============================================================================
// handleHealthCheck (Lookup handler, 0% coverage)
// =============================================================================

func TestVectorStateMachine_HandleHealthCheckLookup(t *testing.T) {
	t.Run("returns healthy status with shard info", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()

		vsm, err := NewVectorStateMachine(5, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		// Add some namespaces to count
		vsm.namespaces["ns1"] = &NamespaceState{Name: "ns1"}
		vsm.namespaces["ns2"] = &NamespaceState{Name: "ns2"}

		cmd := &VectorCommand{
			Op: OpHealthCheck,
		}

		result, err := vsm.Lookup(context.Background(), cmd)
		require.NoError(t, err)

		vecResult := result.(*VectorResult)
		assert.True(t, vecResult.Success)
		require.NotNil(t, vecResult.HealthStatus)
		assert.True(t, vecResult.HealthStatus.Healthy)
		assert.Equal(t, uint64(5), vecResult.HealthStatus.ShardID)
		assert.Equal(t, 2, vecResult.HealthStatus.NodeCount)
		assert.Contains(t, vecResult.HealthStatus.Message, "operational")
	})

	t.Run("health check on empty state machine", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()

		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		cmd := &VectorCommand{
			Op: OpHealthCheck,
		}

		result, err := vsm.Lookup(context.Background(), cmd)
		require.NoError(t, err)

		vecResult := result.(*VectorResult)
		assert.True(t, vecResult.Success)
		assert.Equal(t, 0, vecResult.HealthStatus.NodeCount)
	})
}

// =============================================================================
// Sync Tests (0% coverage)
// =============================================================================

func TestVectorStateMachine_Sync(t *testing.T) {
	t.Run("sync returns no error", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		err = vsm.Sync()
		assert.NoError(t, err)
	})
}

// =============================================================================
// Close Tests (0% coverage)
// =============================================================================

func TestVectorStateMachine_Close(t *testing.T) {
	t.Run("close healthy state machine", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		err = vsm.Close(context.Background())
		assert.NoError(t, err)
		assert.True(t, vsm.closed)
	})

	t.Run("close already closed state machine is idempotent", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		err = vsm.Close(context.Background())
		assert.NoError(t, err)

		// Close again should be a no-op
		err = vsm.Close(context.Background())
		assert.NoError(t, err)
	})

	t.Run("close invokes adapter close", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		err = vsm.Close(context.Background())
		assert.NoError(t, err)

		// Verify adapter was closed
		adapter.mu.RLock()
		assert.True(t, adapter.closed)
		adapter.mu.RUnlock()
	})
}

// =============================================================================
// GetMetrics Tests (0% coverage)
// =============================================================================

func TestVectorStateMachine_GetMetrics(t *testing.T) {
	t.Run("returns zero metrics on new state machine", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		metrics := vsm.GetMetrics()
		require.NotNil(t, metrics)
		assert.Equal(t, uint64(0), metrics.UpsertCount)
		assert.Equal(t, uint64(0), metrics.DeleteCount)
		assert.Equal(t, uint64(0), metrics.SearchCount)
		assert.Equal(t, uint64(0), metrics.SnapshotCount)
		assert.Equal(t, uint64(0), metrics.RecoveryCount)
		assert.Equal(t, uint64(0), metrics.ErrorCount)
	})

	t.Run("returns copy of metrics not reference", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		metrics1 := vsm.GetMetrics()
		metrics1.UpsertCount = 999

		// Original should not be affected
		metrics2 := vsm.GetMetrics()
		assert.Equal(t, uint64(0), metrics2.UpsertCount)
	})

	t.Run("metrics reflect operations", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		adapter.vecStor = NewMockVectorStorageWrapper()
		mockHNSW := NewMockHNSWIndex()
		cache := NewMockCache()
		log := newTestLogger()

		vsm, err := newTestVectorStateMachine(adapter, mockHNSW, cache, log)
		require.NoError(t, err)

		// Create namespace and upsert to increment counters
		setupTestNamespace(vsm, "test_ns", 128, mockHNSW)
		err = createTestNamespaceInStorage(adapter, "test_ns", 128)
		require.NoError(t, err)

		upsertCmd := &VectorCommand{
			Op:        OpUpsertVector,
			Namespace: "test_ns",
			ID:        1,
			Vector:    make([]float32, 128),
			Timestamp: time.Now().UnixNano(),
			Version:   1,
		}
		data, err := upsertCmd.Encode()
		require.NoError(t, err)

		err = vsm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: data}}, make([]sm.Result, 1))
		require.NoError(t, err)

		metrics := vsm.GetMetrics()
		assert.Equal(t, uint64(1), metrics.UpsertCount)
		assert.Greater(t, metrics.UpsertLatencyNs, uint64(0))
	})
}

// =============================================================================
// convertToStringMap Tests (33.3% coverage)
// =============================================================================

func TestConvertToStringMap(t *testing.T) {
	t.Run("nil map returns nil", func(t *testing.T) {
		result := convertToStringMap(nil)
		assert.Nil(t, result)
	})

	t.Run("empty map returns empty map", func(t *testing.T) {
		result := convertToStringMap(map[string]interface{}{})
		assert.NotNil(t, result)
		assert.Len(t, result, 0)
	})

	t.Run("converts various types to strings", func(t *testing.T) {
		input := map[string]interface{}{
			"string": "hello",
			"int":    42,
			"float":  3.14,
			"bool":   true,
			"nil":    nil,
		}
		result := convertToStringMap(input)
		assert.Equal(t, `"hello"`, result["string"])
		assert.Equal(t, "42", result["int"])
		assert.Equal(t, "3.14", result["float"])
		assert.Equal(t, "true", result["bool"])
		assert.Equal(t, "null", result["nil"])
	})
}

func TestConvertFromStringMap(t *testing.T) {
	t.Run("nil map returns nil", func(t *testing.T) {
		result := convertFromStringMap(nil)
		assert.Nil(t, result)
	})

	t.Run("converts strings to interface", func(t *testing.T) {
		input := map[string]string{
			"key1": "value1",
			"key2": "value2",
		}
		result := convertFromStringMap(input)
		assert.Equal(t, "value1", result["key1"])
		assert.Equal(t, "value2", result["key2"])
	})
}

// =============================================================================
// buildIndexConfig Tests (15% coverage)
// =============================================================================

func TestVectorStateMachine_BuildIndexConfig(t *testing.T) {
	t.Run("returns defaults when namespace not found", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		config := vsm.buildIndexConfig("nonexistent_ns")
		require.NotNil(t, config)

		defaults := hnsw.DefaultConfig()
		assert.Equal(t, defaults.M, config.M)
		assert.Equal(t, defaults.EfConstruction, config.EfConstruction)
		assert.Equal(t, defaults.EfSearch, config.EfSearch)
	})

	t.Run("returns defaults when namespace has nil index params", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		vsm.namespaces["ns1"] = &NamespaceState{
			Name:        "ns1",
			IndexParams: nil,
		}

		config := vsm.buildIndexConfig("ns1")
		require.NotNil(t, config)
		defaults := hnsw.DefaultConfig()
		assert.Equal(t, defaults.M, config.M)
	})

	t.Run("applies custom M parameter", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		vsm.namespaces["ns1"] = &NamespaceState{
			Name: "ns1",
			IndexParams: map[string]string{
				"m": "32",
			},
		}

		config := vsm.buildIndexConfig("ns1")
		assert.Equal(t, 32, config.M)
		assert.Equal(t, 64, config.MMax) // M * 2
	})

	t.Run("applies custom ef_construction", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		vsm.namespaces["ns1"] = &NamespaceState{
			Name: "ns1",
			IndexParams: map[string]string{
				"ef_construction": "400",
			},
		}

		config := vsm.buildIndexConfig("ns1")
		assert.Equal(t, 400, config.EfConstruction)
	})

	t.Run("applies custom ef_search", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		vsm.namespaces["ns1"] = &NamespaceState{
			Name: "ns1",
			IndexParams: map[string]string{
				"ef_search": "200",
			},
		}

		config := vsm.buildIndexConfig("ns1")
		assert.Equal(t, 200, config.EfSearch)
	})

	t.Run("applies distance metric l2", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		vsm.namespaces["ns1"] = &NamespaceState{
			Name: "ns1",
			IndexParams: map[string]string{
				"distance_metric": "l2",
			},
		}

		config := vsm.buildIndexConfig("ns1")
		assert.Equal(t, hnsw.L2Distance, config.DistanceMetric)
	})

	t.Run("applies distance metric euclidean", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		vsm.namespaces["ns1"] = &NamespaceState{
			Name: "ns1",
			IndexParams: map[string]string{
				"distance_metric": "euclidean",
			},
		}

		config := vsm.buildIndexConfig("ns1")
		assert.Equal(t, hnsw.L2Distance, config.DistanceMetric)
	})

	t.Run("applies distance metric cosine", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		vsm.namespaces["ns1"] = &NamespaceState{
			Name: "ns1",
			IndexParams: map[string]string{
				"distance_metric": "cosine",
			},
		}

		config := vsm.buildIndexConfig("ns1")
		assert.Equal(t, hnsw.CosineDistance, config.DistanceMetric)
	})

	t.Run("applies distance metric dot product", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		vsm.namespaces["ns1"] = &NamespaceState{
			Name: "ns1",
			IndexParams: map[string]string{
				"distance_metric": "dot",
			},
		}

		config := vsm.buildIndexConfig("ns1")
		assert.Equal(t, hnsw.DotProductDistance, config.DistanceMetric)
	})

	t.Run("applies distance metric inner_product", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		vsm.namespaces["ns1"] = &NamespaceState{
			Name: "ns1",
			IndexParams: map[string]string{
				"distance_metric": "inner_product",
			},
		}

		config := vsm.buildIndexConfig("ns1")
		assert.Equal(t, hnsw.DotProductDistance, config.DistanceMetric)
	})

	t.Run("ignores invalid M parameter", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		vsm.namespaces["ns1"] = &NamespaceState{
			Name: "ns1",
			IndexParams: map[string]string{
				"m": "invalid",
			},
		}

		config := vsm.buildIndexConfig("ns1")
		defaults := hnsw.DefaultConfig()
		assert.Equal(t, defaults.M, config.M)
	})

	t.Run("ignores zero M parameter", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		vsm.namespaces["ns1"] = &NamespaceState{
			Name: "ns1",
			IndexParams: map[string]string{
				"m": "0",
			},
		}

		config := vsm.buildIndexConfig("ns1")
		defaults := hnsw.DefaultConfig()
		assert.Equal(t, defaults.M, config.M)
	})

	t.Run("ignores invalid ef_construction", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		vsm.namespaces["ns1"] = &NamespaceState{
			Name: "ns1",
			IndexParams: map[string]string{
				"ef_construction": "not_a_number",
			},
		}

		config := vsm.buildIndexConfig("ns1")
		defaults := hnsw.DefaultConfig()
		assert.Equal(t, defaults.EfConstruction, config.EfConstruction)
	})

	t.Run("ignores invalid ef_search", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		vsm.namespaces["ns1"] = &NamespaceState{
			Name: "ns1",
			IndexParams: map[string]string{
				"ef_search": "bad",
			},
		}

		config := vsm.buildIndexConfig("ns1")
		defaults := hnsw.DefaultConfig()
		assert.Equal(t, defaults.EfSearch, config.EfSearch)
	})

	t.Run("applies all params together", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		vsm.namespaces["ns1"] = &NamespaceState{
			Name: "ns1",
			IndexParams: map[string]string{
				"m":               "24",
				"ef_construction": "300",
				"ef_search":       "150",
				"distance_metric": "cosine",
			},
		}

		config := vsm.buildIndexConfig("ns1")
		assert.Equal(t, 24, config.M)
		assert.Equal(t, 48, config.MMax)
		assert.Equal(t, 300, config.EfConstruction)
		assert.Equal(t, 150, config.EfSearch)
		assert.Equal(t, hnsw.CosineDistance, config.DistanceMetric)
	})
}

// =============================================================================
// handleDelete Tests (21.6% coverage - need more paths)
// =============================================================================

func TestVectorStateMachine_HandleDelete(t *testing.T) {
	t.Run("single delete success", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		adapter.vecStor = NewMockVectorStorageWrapper()
		mockHNSW := NewMockHNSWIndex()
		cache := NewMockCache()
		log := newTestLogger()

		vsm, err := newTestVectorStateMachine(adapter, mockHNSW, cache, log)
		require.NoError(t, err)

		setupTestNamespace(vsm, "test_ns", 128, mockHNSW)
		err = createTestNamespaceInStorage(adapter, "test_ns", 128)
		require.NoError(t, err)

		// Upsert a vector first
		vec := &vector.VectorValue{
			ID:        1,
			Embedding: make([]float32, 128),
		}
		err = adapter.VectorStorage().UpsertVector(context.Background(), "test_ns", vec)
		require.NoError(t, err)
		require.NoError(t, mockHNSW.Add(1, make([]float32, 128)))

		vsm.namespaces["test_ns"].VectorCount = 1

		// Delete the vector
		deleteCmd := &VectorCommand{
			Op:        OpDeleteVector,
			Namespace: "test_ns",
			ID:        1,
			Timestamp: time.Now().UnixNano(),
		}
		data, err := deleteCmd.Encode()
		require.NoError(t, err)

		results := make([]sm.Result, 1)
		err = vsm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: data}}, results)
		require.NoError(t, err)
		assert.Equal(t, uint64(1), results[0].Value) // 1 vector deleted

		// VectorCount should be decremented
		assert.Equal(t, uint64(0), vsm.namespaces["test_ns"].VectorCount)
	})

	t.Run("batch delete with IDs array", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		adapter.vecStor = NewMockVectorStorageWrapper()
		mockHNSW := NewMockHNSWIndex()
		log := newTestLogger()

		vsm, err := newTestVectorStateMachine(adapter, mockHNSW, nil, log)
		require.NoError(t, err)

		setupTestNamespace(vsm, "test_ns", 128, mockHNSW)
		err = createTestNamespaceInStorage(adapter, "test_ns", 128)
		require.NoError(t, err)

		// Upsert multiple vectors
		for i := uint64(1); i <= 3; i++ {
			vec := &vector.VectorValue{
				ID:        i,
				Embedding: make([]float32, 128),
			}
			err = adapter.VectorStorage().UpsertVector(context.Background(), "test_ns", vec)
			require.NoError(t, err)
		}
		vsm.namespaces["test_ns"].VectorCount = 3

		deleteCmd := &VectorCommand{
			Op:        OpDeleteVector,
			Namespace: "test_ns",
			IDs:       []uint64{1, 2, 3},
			Timestamp: time.Now().UnixNano(),
		}
		data, err := deleteCmd.Encode()
		require.NoError(t, err)

		results := make([]sm.Result, 1)
		err = vsm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: data}}, results)
		require.NoError(t, err)
		assert.Equal(t, uint64(3), results[0].Value) // 3 vectors deleted
	})

	t.Run("delete with cache invalidation", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		adapter.vecStor = NewMockVectorStorageWrapper()
		mockHNSW := NewMockHNSWIndex()
		cache := NewMockCache()
		log := newTestLogger()

		vsm, err := newTestVectorStateMachine(adapter, mockHNSW, cache, log)
		require.NoError(t, err)

		setupTestNamespace(vsm, "test_ns", 128, mockHNSW)
		err = createTestNamespaceInStorage(adapter, "test_ns", 128)
		require.NoError(t, err)

		// Put something in cache
		err = cache.Set(context.Background(), []byte("vector:test_ns:1"), []byte("cached"), 0)
		require.NoError(t, err)

		vec := &vector.VectorValue{ID: 1, Embedding: make([]float32, 128)}
		err = adapter.VectorStorage().UpsertVector(context.Background(), "test_ns", vec)
		require.NoError(t, err)
		vsm.namespaces["test_ns"].VectorCount = 1

		deleteCmd := &VectorCommand{
			Op:        OpDeleteVector,
			Namespace: "test_ns",
			ID:        1,
			Timestamp: time.Now().UnixNano(),
		}
		data, err := deleteCmd.Encode()
		require.NoError(t, err)

		err = vsm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: data}}, make([]sm.Result, 1))
		require.NoError(t, err)

		// Cache should be invalidated
		_, cacheErr := cache.Get(context.Background(), []byte("vector:test_ns:1"))
		assert.Error(t, cacheErr)
	})

	t.Run("delete when vector does not exist continues silently", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		adapter.vecStor = NewMockVectorStorageWrapper()
		mockHNSW := NewMockHNSWIndex()
		log := newTestLogger()

		vsm, err := newTestVectorStateMachine(adapter, mockHNSW, nil, log)
		require.NoError(t, err)

		setupTestNamespace(vsm, "test_ns", 128, mockHNSW)
		err = createTestNamespaceInStorage(adapter, "test_ns", 128)
		require.NoError(t, err)

		// Delete non-existent vector - should not error, just return 0 count
		deleteCmd := &VectorCommand{
			Op:        OpDeleteVector,
			Namespace: "test_ns",
			ID:        999,
			Timestamp: time.Now().UnixNano(),
		}
		data, err := deleteCmd.Encode()
		require.NoError(t, err)

		results := make([]sm.Result, 1)
		err = vsm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: data}}, results)
		require.NoError(t, err)
		// MockVectorStorageWrapper.DeleteVector returns not found error for nonexistent vectors
		// The handler logs the error and continues, so deleted count is 0
		assert.Equal(t, uint64(0), results[0].Value)
	})
}

// =============================================================================
// handleBatchUpsert Tests (17.8% coverage)
// =============================================================================

func TestVectorStateMachine_HandleBatchUpsert(t *testing.T) {
	t.Run("successful batch upsert", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		adapter.vecStor = NewMockVectorStorageWrapper()
		mockHNSW := NewMockHNSWIndex()
		log := newTestLogger()

		vsm, err := newTestVectorStateMachine(adapter, mockHNSW, nil, log)
		require.NoError(t, err)

		setupTestNamespace(vsm, "test_ns", 4, mockHNSW)
		err = createTestNamespaceInStorage(adapter, "test_ns", 4)
		require.NoError(t, err)

		batchCmd := &VectorCommand{
			Op:        OpBatchUpsert,
			Namespace: "test_ns",
			IDs:       []uint64{1, 2, 3},
			Vectors:   [][]float32{{0.1, 0.2, 0.3, 0.4}, {0.5, 0.6, 0.7, 0.8}, {0.9, 1.0, 1.1, 1.2}},
			Timestamp: time.Now().UnixNano(),
			Version:   1,
		}
		data, err := batchCmd.Encode()
		require.NoError(t, err)

		results := make([]sm.Result, 1)
		err = vsm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: data}}, results)
		require.NoError(t, err)
		assert.Equal(t, uint64(3), results[0].Value)
		assert.Equal(t, uint64(3), vsm.namespaces["test_ns"].VectorCount)
	})

	t.Run("batch upsert with metadata", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		adapter.vecStor = NewMockVectorStorageWrapper()
		mockHNSW := NewMockHNSWIndex()
		log := newTestLogger()

		vsm, err := newTestVectorStateMachine(adapter, mockHNSW, nil, log)
		require.NoError(t, err)

		setupTestNamespace(vsm, "test_ns", 2, mockHNSW)
		err = createTestNamespaceInStorage(adapter, "test_ns", 2)
		require.NoError(t, err)

		batchCmd := &VectorCommand{
			Op:        OpBatchUpsert,
			Namespace: "test_ns",
			IDs:       []uint64{1, 2},
			Vectors:   [][]float32{{0.1, 0.2}, {0.3, 0.4}},
			BatchMeta: []map[string]interface{}{
				{"color": "red", "size": 10},
				{"color": "blue", "size": 20},
			},
			Timestamp: time.Now().UnixNano(),
			Version:   1,
		}
		data, err := batchCmd.Encode()
		require.NoError(t, err)

		results := make([]sm.Result, 1)
		err = vsm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: data}}, results)
		require.NoError(t, err)
		assert.Equal(t, uint64(2), results[0].Value)
	})

	t.Run("batch upsert with dimension mismatch", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		adapter.vecStor = NewMockVectorStorageWrapper()
		mockHNSW := NewMockHNSWIndex()
		log := newTestLogger()

		vsm, err := newTestVectorStateMachine(adapter, mockHNSW, nil, log)
		require.NoError(t, err)

		setupTestNamespace(vsm, "test_ns", 4, mockHNSW) // dimension = 4
		err = createTestNamespaceInStorage(adapter, "test_ns", 4)
		require.NoError(t, err)

		batchCmd := &VectorCommand{
			Op:        OpBatchUpsert,
			Namespace: "test_ns",
			IDs:       []uint64{1, 2},
			Vectors:   [][]float32{{0.1, 0.2}, {0.3, 0.4}}, // dimension = 2, expected 4
			Timestamp: time.Now().UnixNano(),
			Version:   1,
		}
		data, err := batchCmd.Encode()
		require.NoError(t, err)

		err = vsm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: data}}, make([]sm.Result, 1))
		assert.Error(t, err)
	})

	t.Run("batch upsert updates storage bytes", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		adapter.vecStor = NewMockVectorStorageWrapper()
		mockHNSW := NewMockHNSWIndex()
		log := newTestLogger()

		vsm, err := newTestVectorStateMachine(adapter, mockHNSW, nil, log)
		require.NoError(t, err)

		setupTestNamespace(vsm, "test_ns", 4, mockHNSW)
		err = createTestNamespaceInStorage(adapter, "test_ns", 4)
		require.NoError(t, err)

		batchCmd := &VectorCommand{
			Op:        OpBatchUpsert,
			Namespace: "test_ns",
			IDs:       []uint64{1},
			Vectors:   [][]float32{{0.1, 0.2, 0.3, 0.4}},
			Timestamp: time.Now().UnixNano(),
			Version:   1,
		}
		data, err := batchCmd.Encode()
		require.NoError(t, err)

		err = vsm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: data}}, make([]sm.Result, 1))
		require.NoError(t, err)

		ns := vsm.namespaces["test_ns"]
		assert.Greater(t, ns.StorageBytes, int64(0))
	})
}

// =============================================================================
// handleUpdateNamespace Tests (10% coverage)
// =============================================================================

func TestVectorStateMachine_HandleUpdateNamespace(t *testing.T) {
	t.Run("update with new index params", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		adapter.vecStor = NewMockVectorStorageWrapper()
		mockHNSW := NewMockHNSWIndex()
		log := newTestLogger()

		vsm, err := newTestVectorStateMachine(adapter, mockHNSW, nil, log)
		require.NoError(t, err)

		setupTestNamespace(vsm, "test_ns", 128, mockHNSW)
		err = createTestNamespaceInStorage(adapter, "test_ns", 128)
		require.NoError(t, err)

		updateCmd := &VectorCommand{
			Op:        OpUpdateNamespace,
			Namespace: "test_ns",
			IndexParams: map[string]string{
				"m":               "32",
				"ef_construction": "400",
			},
			Timestamp: time.Now().UnixNano(),
		}
		data, err := updateCmd.Encode()
		require.NoError(t, err)

		results := make([]sm.Result, 1)
		err = vsm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: data}}, results)
		require.NoError(t, err)

		// Decode the result to check requires_rebuild
		var updateResult UpdateNamespaceResult
		err = msgpack.Unmarshal(results[0].Data, &updateResult)
		require.NoError(t, err)
		assert.True(t, updateResult.Updated)
		assert.True(t, updateResult.RequiresRebuild) // New params require rebuild

		// Verify namespace params were updated
		ns := vsm.namespaces["test_ns"]
		assert.Equal(t, "32", ns.IndexParams["m"])
		assert.Equal(t, "400", ns.IndexParams["ef_construction"])
	})

	t.Run("update with same index params does not require rebuild", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		adapter.vecStor = NewMockVectorStorageWrapper()
		mockHNSW := NewMockHNSWIndex()
		log := newTestLogger()

		vsm, err := newTestVectorStateMachine(adapter, mockHNSW, nil, log)
		require.NoError(t, err)

		vsm.namespaces["test_ns"] = &NamespaceState{
			Name: "test_ns",
			IndexParams: map[string]string{
				"m": "16",
			},
		}
		vsm.indexesMu.Lock()
		vsm.hnswIndexes["test_ns"] = mockHNSW
		vsm.indexesMu.Unlock()

		updateCmd := &VectorCommand{
			Op:        OpUpdateNamespace,
			Namespace: "test_ns",
			IndexParams: map[string]string{
				"m": "16", // Same value
			},
			Timestamp: time.Now().UnixNano(),
		}
		data, err := updateCmd.Encode()
		require.NoError(t, err)

		results := make([]sm.Result, 1)
		err = vsm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: data}}, results)
		require.NoError(t, err)

		var updateResult UpdateNamespaceResult
		err = msgpack.Unmarshal(results[0].Data, &updateResult)
		require.NoError(t, err)
		assert.True(t, updateResult.Updated)
		assert.False(t, updateResult.RequiresRebuild)
	})

	t.Run("update namespace creates index params map if nil", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		adapter.vecStor = NewMockVectorStorageWrapper()
		mockHNSW := NewMockHNSWIndex()
		log := newTestLogger()

		vsm, err := newTestVectorStateMachine(adapter, mockHNSW, nil, log)
		require.NoError(t, err)

		vsm.namespaces["test_ns"] = &NamespaceState{
			Name:        "test_ns",
			IndexParams: nil, // Start with nil
		}
		vsm.indexesMu.Lock()
		vsm.hnswIndexes["test_ns"] = mockHNSW
		vsm.indexesMu.Unlock()

		updateCmd := &VectorCommand{
			Op:        OpUpdateNamespace,
			Namespace: "test_ns",
			IndexParams: map[string]string{
				"m": "32",
			},
			Timestamp: time.Now().UnixNano(),
		}
		data, err := updateCmd.Encode()
		require.NoError(t, err)

		err = vsm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: data}}, make([]sm.Result, 1))
		require.NoError(t, err)

		ns := vsm.namespaces["test_ns"]
		assert.NotNil(t, ns.IndexParams)
		assert.Equal(t, "32", ns.IndexParams["m"])
	})

	t.Run("update with empty index params still updates timestamp", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		adapter.vecStor = NewMockVectorStorageWrapper()
		mockHNSW := NewMockHNSWIndex()
		log := newTestLogger()

		vsm, err := newTestVectorStateMachine(adapter, mockHNSW, nil, log)
		require.NoError(t, err)

		oldTime := time.Now().UnixNano() - 1000000
		vsm.namespaces["test_ns"] = &NamespaceState{
			Name:      "test_ns",
			UpdatedAt: oldTime,
		}
		vsm.indexesMu.Lock()
		vsm.hnswIndexes["test_ns"] = mockHNSW
		vsm.indexesMu.Unlock()

		updateCmd := &VectorCommand{
			Op:        OpUpdateNamespace,
			Namespace: "test_ns",
			Timestamp: time.Now().UnixNano(),
		}
		data, err := updateCmd.Encode()
		require.NoError(t, err)

		err = vsm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: data}}, make([]sm.Result, 1))
		require.NoError(t, err)

		ns := vsm.namespaces["test_ns"]
		assert.Greater(t, ns.UpdatedAt, oldTime)
	})
}

// =============================================================================
// handleUpdateMetadata Tests (6.1% coverage)
// =============================================================================

func TestVectorStateMachine_HandleUpdateMetadata(t *testing.T) {
	t.Run("replace metadata", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		adapter.vecStor = NewMockVectorStorageWrapper()
		mockHNSW := NewMockHNSWIndex()
		cache := NewMockCache()
		log := newTestLogger()

		vsm, err := newTestVectorStateMachine(adapter, mockHNSW, cache, log)
		require.NoError(t, err)

		setupTestNamespace(vsm, "test_ns", 4, mockHNSW)
		err = createTestNamespaceInStorage(adapter, "test_ns", 4)
		require.NoError(t, err)

		// Upsert a vector with initial metadata
		vec := &vector.VectorValue{
			ID:        1,
			Embedding: []float32{0.1, 0.2, 0.3, 0.4},
			Metadata:  map[string]string{"old_key": "old_value", "keep": "this"},
			Version:   1,
		}
		err = adapter.VectorStorage().UpsertVector(context.Background(), "test_ns", vec)
		require.NoError(t, err)

		// Replace metadata (Merge = false)
		updateCmd := &VectorCommand{
			Op:        OpUpdateMetadata,
			Namespace: "test_ns",
			ID:        1,
			Metadata:  map[string]interface{}{"new_key": "new_value"},
			Merge:     false,
			Timestamp: time.Now().UnixNano(),
		}
		data, err := updateCmd.Encode()
		require.NoError(t, err)

		results := make([]sm.Result, 1)
		err = vsm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: data}}, results)
		require.NoError(t, err)
		assert.Equal(t, uint64(2), results[0].Value) // Version incremented

		// Verify vector was updated in storage
		updatedVec, err := adapter.VectorStorage().GetVector(context.Background(), "test_ns", 1)
		require.NoError(t, err)
		assert.Equal(t, `"new_value"`, updatedVec.Metadata["new_key"])
		// old_key should NOT be present since Merge=false
		_, hasOldKey := updatedVec.Metadata["old_key"]
		assert.False(t, hasOldKey)
	})

	t.Run("merge metadata", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		adapter.vecStor = NewMockVectorStorageWrapper()
		mockHNSW := NewMockHNSWIndex()
		log := newTestLogger()

		vsm, err := newTestVectorStateMachine(adapter, mockHNSW, nil, log)
		require.NoError(t, err)

		setupTestNamespace(vsm, "test_ns", 4, mockHNSW)
		err = createTestNamespaceInStorage(adapter, "test_ns", 4)
		require.NoError(t, err)

		vec := &vector.VectorValue{
			ID:        1,
			Embedding: []float32{0.1, 0.2, 0.3, 0.4},
			Metadata:  map[string]string{"old_key": "old_value", "keep": "this"},
			Version:   1,
		}
		err = adapter.VectorStorage().UpsertVector(context.Background(), "test_ns", vec)
		require.NoError(t, err)

		// Merge metadata
		updateCmd := &VectorCommand{
			Op:        OpUpdateMetadata,
			Namespace: "test_ns",
			ID:        1,
			Metadata:  map[string]interface{}{"new_key": "new_value", "keep": "updated"},
			Merge:     true,
			Timestamp: time.Now().UnixNano(),
		}
		data, err := updateCmd.Encode()
		require.NoError(t, err)

		results := make([]sm.Result, 1)
		err = vsm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: data}}, results)
		require.NoError(t, err)

		// Verify merged metadata
		updatedVec, err := adapter.VectorStorage().GetVector(context.Background(), "test_ns", 1)
		require.NoError(t, err)
		assert.Equal(t, `"new_value"`, updatedVec.Metadata["new_key"])
		assert.Equal(t, "old_value", updatedVec.Metadata["old_key"]) // Preserved
		assert.Equal(t, `"updated"`, updatedVec.Metadata["keep"])    // Updated
	})

	t.Run("update metadata on nonexistent vector returns error", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		adapter.vecStor = NewMockVectorStorageWrapper()
		mockHNSW := NewMockHNSWIndex()
		log := newTestLogger()

		vsm, err := newTestVectorStateMachine(adapter, mockHNSW, nil, log)
		require.NoError(t, err)

		setupTestNamespace(vsm, "test_ns", 128, mockHNSW)
		err = createTestNamespaceInStorage(adapter, "test_ns", 128)
		require.NoError(t, err)

		updateCmd := &VectorCommand{
			Op:        OpUpdateMetadata,
			Namespace: "test_ns",
			ID:        999, // Does not exist
			Metadata:  map[string]interface{}{"key": "value"},
			Timestamp: time.Now().UnixNano(),
		}
		data, err := updateCmd.Encode()
		require.NoError(t, err)

		err = vsm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: data}}, make([]sm.Result, 1))
		assert.Error(t, err)
	})

	t.Run("update metadata invalidates cache", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		adapter.vecStor = NewMockVectorStorageWrapper()
		mockHNSW := NewMockHNSWIndex()
		cache := NewMockCache()
		log := newTestLogger()

		vsm, err := newTestVectorStateMachine(adapter, mockHNSW, cache, log)
		require.NoError(t, err)

		setupTestNamespace(vsm, "test_ns", 4, mockHNSW)
		err = createTestNamespaceInStorage(adapter, "test_ns", 4)
		require.NoError(t, err)

		vec := &vector.VectorValue{
			ID:        1,
			Embedding: []float32{0.1, 0.2, 0.3, 0.4},
			Metadata:  map[string]string{"key": "old"},
			Version:   1,
		}
		err = adapter.VectorStorage().UpsertVector(context.Background(), "test_ns", vec)
		require.NoError(t, err)

		// Populate cache
		err = cache.Set(context.Background(), []byte("vector:test_ns:1"), []byte("cached"), 0)
		require.NoError(t, err)

		updateCmd := &VectorCommand{
			Op:        OpUpdateMetadata,
			Namespace: "test_ns",
			ID:        1,
			Metadata:  map[string]interface{}{"key": "new"},
			Merge:     false,
			Timestamp: time.Now().UnixNano(),
		}
		data, err := updateCmd.Encode()
		require.NoError(t, err)

		err = vsm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: data}}, make([]sm.Result, 1))
		require.NoError(t, err)

		// Verify cache was invalidated
		_, cacheErr := cache.Get(context.Background(), []byte("vector:test_ns:1"))
		assert.Error(t, cacheErr)
	})
}

// =============================================================================
// handleRebuildIndex Tests (28.6% coverage)
// =============================================================================

func TestVectorStateMachine_HandleRebuildIndex(t *testing.T) {
	t.Run("creates rebuild job", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		adapter.vecStor = NewMockVectorStorageWrapper()
		mockHNSW := NewMockHNSWIndex()
		log := newTestLogger()

		vsm, err := newTestVectorStateMachine(adapter, mockHNSW, nil, log)
		require.NoError(t, err)

		setupTestNamespace(vsm, "test_ns", 128, mockHNSW)
		err = createTestNamespaceInStorage(adapter, "test_ns", 128)
		require.NoError(t, err)
		vsm.namespaces["test_ns"].VectorCount = 500

		rebuildCmd := &VectorCommand{
			Op:        OpRebuildIndex,
			Namespace: "test_ns",
			JobID:     "rebuild-123",
			Timestamp: time.Now().UnixNano(),
		}
		data, err := rebuildCmd.Encode()
		require.NoError(t, err)

		results := make([]sm.Result, 1)
		err = vsm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: data}}, results)
		require.NoError(t, err)
		assert.Equal(t, uint64(1), results[0].Value)

		// Verify job was created
		job, exists := vsm.indexRebuildJobs["rebuild-123"]
		require.True(t, exists)
		assert.True(t, job.InProgress)
		assert.Equal(t, "test_ns", job.Namespace)
		assert.Equal(t, uint64(500), job.TotalVectors)
		assert.Equal(t, uint64(0), job.VectorsProcessed)
		assert.False(t, job.StartedAt.IsZero())
	})

	t.Run("rebuild on different namespace", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		adapter.vecStor = NewMockVectorStorageWrapper()
		mockHNSW := NewMockHNSWIndex()
		log := newTestLogger()

		vsm, err := newTestVectorStateMachine(adapter, mockHNSW, nil, log)
		require.NoError(t, err)

		setupTestNamespace(vsm, "ns_a", 64, mockHNSW)
		err = createTestNamespaceInStorage(adapter, "ns_a", 64)
		require.NoError(t, err)
		vsm.namespaces["ns_a"].VectorCount = 200

		rebuildCmd := &VectorCommand{
			Op:        OpRebuildIndex,
			Namespace: "ns_a",
			JobID:     "job-a",
			Timestamp: time.Now().UnixNano(),
		}
		data, err := rebuildCmd.Encode()
		require.NoError(t, err)

		err = vsm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: data}}, make([]sm.Result, 1))
		require.NoError(t, err)

		job := vsm.indexRebuildJobs["job-a"]
		require.NotNil(t, job)
		assert.Equal(t, "ns_a", job.Namespace)
		assert.Equal(t, uint64(200), job.TotalVectors)
	})
}

// =============================================================================
// handleGetStats Tests (15.4% coverage)
// =============================================================================

func TestVectorStateMachine_HandleGetStats(t *testing.T) {
	t.Run("returns stats for namespace", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		adapter.vecStor = NewMockVectorStorageWrapper()
		mockHNSW := NewMockHNSWIndex()
		log := newTestLogger()

		vsm, err := newTestVectorStateMachine(adapter, mockHNSW, nil, log)
		require.NoError(t, err)

		setupTestNamespace(vsm, "test_ns", 128, mockHNSW)
		vsm.namespaces["test_ns"].VectorCount = 1000

		cmd := &VectorCommand{
			Op:        OpGetStats,
			Namespace: "test_ns",
		}

		result, err := vsm.Lookup(context.Background(), cmd)
		require.NoError(t, err)

		vecResult := result.(*VectorResult)
		assert.True(t, vecResult.Success)
		require.NotNil(t, vecResult.VectorStats)
		assert.Equal(t, uint64(1000), vecResult.VectorStats.VectorCount)
		assert.Equal(t, uint32(128), vecResult.VectorStats.Dimension)
		assert.Greater(t, vecResult.VectorStats.IndexSize, uint64(0))
		assert.Greater(t, vecResult.VectorStats.StorageSize, uint64(0))
	})

	t.Run("returns zero stats for empty namespace", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		adapter.vecStor = NewMockVectorStorageWrapper()
		mockHNSW := NewMockHNSWIndex()
		log := newTestLogger()

		vsm, err := newTestVectorStateMachine(adapter, mockHNSW, nil, log)
		require.NoError(t, err)

		setupTestNamespace(vsm, "empty_ns", 64, mockHNSW)

		cmd := &VectorCommand{
			Op:        OpGetStats,
			Namespace: "empty_ns",
		}

		result, err := vsm.Lookup(context.Background(), cmd)
		require.NoError(t, err)

		vecResult := result.(*VectorResult)
		assert.True(t, vecResult.Success)
		assert.Equal(t, uint64(0), vecResult.VectorStats.VectorCount)
		assert.Equal(t, uint64(0), vecResult.VectorStats.IndexSize)
		assert.Equal(t, uint64(0), vecResult.VectorStats.StorageSize)
	})

	t.Run("stats for nonexistent namespace returns error", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		adapter.vecStor = NewMockVectorStorageWrapper()
		mockHNSW := NewMockHNSWIndex()
		log := newTestLogger()

		vsm, err := newTestVectorStateMachine(adapter, mockHNSW, nil, log)
		require.NoError(t, err)

		cmd := &VectorCommand{
			Op:        OpGetStats,
			Namespace: "nonexistent",
		}

		_, err = vsm.Lookup(context.Background(), cmd)
		assert.Error(t, err)
	})
}

// =============================================================================
// SaveSnapshot Tests (0% coverage)
// =============================================================================

func TestVectorStateMachine_SaveSnapshot(t *testing.T) {
	t.Run("saves empty snapshot", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		var buf bytes.Buffer
		stopper := make(chan struct{})

		err = vsm.SaveSnapshot(context.Background(), nil, &buf, stopper)
		require.NoError(t, err)
		assert.Greater(t, buf.Len(), 0)
	})

	t.Run("saves snapshot with namespaces and indexes", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		adapter.vecStor = NewMockVectorStorageWrapper()
		mockHNSW := NewMockHNSWIndex()
		log := newTestLogger()

		vsm, err := newTestVectorStateMachine(adapter, mockHNSW, nil, log)
		require.NoError(t, err)

		setupTestNamespace(vsm, "ns1", 128, mockHNSW)
		setupTestNamespace(vsm, "ns2", 256, mockHNSW)
		vsm.namespaces["ns1"].TenantID = "tenant-A"
		vsm.namespaces["ns1"].VectorCount = 50

		var buf bytes.Buffer
		stopper := make(chan struct{})

		err = vsm.SaveSnapshot(context.Background(), nil, &buf, stopper)
		require.NoError(t, err)
		assert.Greater(t, buf.Len(), 16) // At least size headers
	})

	t.Run("increments snapshot count metric", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		var buf bytes.Buffer
		stopper := make(chan struct{})

		err = vsm.SaveSnapshot(context.Background(), nil, &buf, stopper)
		require.NoError(t, err)

		metrics := vsm.GetMetrics()
		assert.Equal(t, uint64(1), metrics.SnapshotCount)
	})
}

// =============================================================================
// RecoverFromSnapshot Tests (0% coverage)
// =============================================================================

func TestVectorStateMachine_RecoverFromSnapshot(t *testing.T) {
	t.Run("round trip save and recover", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		adapter.vecStor = NewMockVectorStorageWrapper()
		mockHNSW := NewMockHNSWIndex()
		log := newTestLogger()

		vsm, err := newTestVectorStateMachine(adapter, mockHNSW, nil, log)
		require.NoError(t, err)

		// Setup state
		setupTestNamespace(vsm, "ns1", 128, mockHNSW)
		vsm.namespaces["ns1"].TenantID = "tenant-A"
		vsm.namespaces["ns1"].VectorCount = 42
		vsm.namespaces["ns1"].StorageBytes = 9999

		setupTestNamespace(vsm, "ns2", 256, mockHNSW)
		vsm.namespaces["ns2"].TenantID = "tenant-B"

		// Save
		var buf bytes.Buffer
		stopper := make(chan struct{})
		err = vsm.SaveSnapshot(context.Background(), nil, &buf, stopper)
		require.NoError(t, err)

		// Create fresh VSM and recover
		adapter2 := NewMockStorageAdapter()
		adapter2.vecStor = NewMockVectorStorageWrapper()
		mockHNSW2 := NewMockHNSWIndex()

		vsm2, err := newTestVectorStateMachine(adapter2, mockHNSW2, nil, log)
		require.NoError(t, err)

		reader := bytes.NewReader(buf.Bytes())
		stopper2 := make(chan struct{})
		err = vsm2.RecoverFromSnapshot(context.Background(), reader, stopper2)
		require.NoError(t, err)

		// Verify state was restored
		require.Contains(t, vsm2.namespaces, "ns1")
		assert.Equal(t, "tenant-A", vsm2.namespaces["ns1"].TenantID)
		assert.Equal(t, uint64(42), vsm2.namespaces["ns1"].VectorCount)
		assert.Equal(t, int64(9999), vsm2.namespaces["ns1"].StorageBytes)

		require.Contains(t, vsm2.namespaces, "ns2")
		assert.Equal(t, "tenant-B", vsm2.namespaces["ns2"].TenantID)
		assert.Equal(t, uint32(256), vsm2.namespaces["ns2"].Dimension)
	})

	t.Run("recover increments recovery metric", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		// Save minimal snapshot
		var buf bytes.Buffer
		stopper := make(chan struct{})
		err = vsm.SaveSnapshot(context.Background(), nil, &buf, stopper)
		require.NoError(t, err)

		// Recover
		mockHNSW := NewMockHNSWIndex()
		adapter2 := NewMockStorageAdapter()
		vsm2, err := newTestVectorStateMachine(adapter2, mockHNSW, nil, log)
		require.NoError(t, err)

		reader := bytes.NewReader(buf.Bytes())
		stopper2 := make(chan struct{})
		err = vsm2.RecoverFromSnapshot(context.Background(), reader, stopper2)
		require.NoError(t, err)

		metrics := vsm2.GetMetrics()
		assert.Equal(t, uint64(1), metrics.RecoveryCount)
	})

	t.Run("recover with truncated snapshot size", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		mockHNSW := NewMockHNSWIndex()
		vsm, err := newTestVectorStateMachine(adapter, mockHNSW, nil, log)
		require.NoError(t, err)

		// Write only 3 bytes where 8 are expected for size
		reader := bytes.NewReader([]byte{0x01, 0x02, 0x03})
		stopper := make(chan struct{})
		err = vsm.RecoverFromSnapshot(context.Background(), reader, stopper)
		assert.Error(t, err)
	})

	t.Run("recover with invalid msgpack data", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		mockHNSW := NewMockHNSWIndex()
		vsm, err := newTestVectorStateMachine(adapter, mockHNSW, nil, log)
		require.NoError(t, err)

		// Write valid size but garbage data
		var buf bytes.Buffer
		sizeBytes := EncodeUint64(10) // 10 bytes of data
		buf.Write(sizeBytes)
		buf.Write([]byte("invalidmsg")) // 10 bytes of garbage

		reader := bytes.NewReader(buf.Bytes())
		stopper := make(chan struct{})
		err = vsm.RecoverFromSnapshot(context.Background(), reader, stopper)
		assert.Error(t, err)
	})

	t.Run("recover gracefully handles missing index count", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		mockHNSW := NewMockHNSWIndex()
		vsm, err := newTestVectorStateMachine(adapter, mockHNSW, nil, log)
		require.NoError(t, err)

		// Write a valid snapshot with no index data following
		snap := &VectorSnapshot{
			Namespaces: map[string]*NamespaceState{
				"ns1": {Name: "ns1", Dimension: 128},
			},
			Timestamp: time.Now().UnixNano(),
		}
		snapData, err := msgpack.Marshal(snap)
		require.NoError(t, err)

		var buf bytes.Buffer
		buf.Write(EncodeUint64(uint64(len(snapData))))
		buf.Write(snapData)
		// No index count bytes - simulates older snapshot format

		reader := bytes.NewReader(buf.Bytes())
		stopper := make(chan struct{})
		err = vsm.RecoverFromSnapshot(context.Background(), reader, stopper)
		assert.NoError(t, err) // Should handle gracefully

		require.Contains(t, vsm.namespaces, "ns1")
	})
}

// =============================================================================
// Open Tests (0% coverage)
// =============================================================================

func TestVectorStateMachine_Open(t *testing.T) {
	t.Run("open with empty storage", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		adapter.vecStor = NewMockVectorStorageWrapper()
		mockHNSW := NewMockHNSWIndex()
		log := newTestLogger()

		vsm, err := newTestVectorStateMachine(adapter, mockHNSW, nil, log)
		require.NoError(t, err)

		stopper := make(chan struct{})
		idx, err := vsm.Open(context.Background(), "/tmp/test", stopper)
		require.NoError(t, err)
		assert.Equal(t, uint64(0), idx)
		assert.Len(t, vsm.namespaces, 0)
	})

	t.Run("open loads existing namespaces", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		adapter.vecStor = NewMockVectorStorageWrapper()
		mockHNSW := NewMockHNSWIndex()
		log := newTestLogger()

		// Pre-create namespaces in storage
		err := adapter.VectorStorage().CreateNamespace(context.Background(), &vector.NamespaceInfo{
			Name:        "ns1",
			Dimension:   128,
			VectorCount: 10,
			CreatedAt:   time.Now().UnixNano(),
			UpdatedAt:   time.Now().UnixNano(),
		})
		require.NoError(t, err)

		err = adapter.VectorStorage().CreateNamespace(context.Background(), &vector.NamespaceInfo{
			Name:        "ns2",
			Dimension:   256,
			VectorCount: 20,
			CreatedAt:   time.Now().UnixNano(),
			UpdatedAt:   time.Now().UnixNano(),
		})
		require.NoError(t, err)

		vsm, err := newTestVectorStateMachine(adapter, mockHNSW, nil, log)
		require.NoError(t, err)

		stopper := make(chan struct{})
		idx, err := vsm.Open(context.Background(), "/tmp/test", stopper)
		require.NoError(t, err)
		assert.Equal(t, uint64(0), idx)

		// Verify namespaces were loaded
		assert.Len(t, vsm.namespaces, 2)
		assert.Contains(t, vsm.namespaces, "ns1")
		assert.Contains(t, vsm.namespaces, "ns2")
		assert.Equal(t, uint32(128), vsm.namespaces["ns1"].Dimension)
		assert.Equal(t, uint32(256), vsm.namespaces["ns2"].Dimension)
	})

	t.Run("open without vector storage logs warning", func(t *testing.T) {
		// Adapter without VectorStorage method
		adapter := &MockStorageAdapter{
			data: make(map[string][]byte),
		}
		log := newTestLogger()

		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		stopper := make(chan struct{})
		idx, err := vsm.Open(context.Background(), "/tmp/test", stopper)
		require.NoError(t, err)
		assert.Equal(t, uint64(0), idx)
	})
}

// =============================================================================
// byteSliceWriter / byteSliceReader Tests (0% coverage)
// =============================================================================

func TestByteSliceWriter_Write(t *testing.T) {
	t.Run("writes data to buffer", func(t *testing.T) {
		var buf []byte
		w := &byteSliceWriter{buf: &buf}

		n, err := w.Write([]byte("hello"))
		require.NoError(t, err)
		assert.Equal(t, 5, n)
		assert.Equal(t, []byte("hello"), buf)
	})

	t.Run("appends multiple writes", func(t *testing.T) {
		var buf []byte
		w := &byteSliceWriter{buf: &buf}

		w.Write([]byte("hello")) //nolint:errcheck
		w.Write([]byte(" "))     //nolint:errcheck
		w.Write([]byte("world")) //nolint:errcheck

		assert.Equal(t, []byte("hello world"), buf)
	})
}

func TestByteSliceReader_Read(t *testing.T) {
	t.Run("reads data from buffer", func(t *testing.T) {
		r := &byteSliceReader{buf: []byte("hello world")}

		p := make([]byte, 5)
		n, err := r.Read(p)
		require.NoError(t, err)
		assert.Equal(t, 5, n)
		assert.Equal(t, []byte("hello"), p)
	})

	t.Run("returns EOF at end of buffer", func(t *testing.T) {
		r := &byteSliceReader{buf: []byte("hi")}

		p := make([]byte, 5)
		n, err := r.Read(p)
		require.NoError(t, err)
		assert.Equal(t, 2, n)

		// Next read should return EOF
		n, err = r.Read(p)
		assert.Equal(t, io.EOF, err)
		assert.Equal(t, 0, n)
	})

	t.Run("reads empty buffer returns EOF", func(t *testing.T) {
		r := &byteSliceReader{buf: []byte{}}

		p := make([]byte, 5)
		n, err := r.Read(p)
		assert.Equal(t, io.EOF, err)
		assert.Equal(t, 0, n)
	})
}

// =============================================================================
// NewVectorStateMachine Validation Tests
// =============================================================================

func TestNewVectorStateMachine_Validation(t *testing.T) {
	t.Run("nil adapter returns error", func(t *testing.T) {
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", nil, nil, nil, nil, log)
		assert.Error(t, err)
		assert.Nil(t, vsm)
	})

	t.Run("nil logger returns error", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, nil)
		assert.Error(t, err)
		assert.Nil(t, vsm)
	})

	t.Run("nil hnsw config uses defaults", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)
		require.NotNil(t, vsm)
		assert.NotNil(t, vsm.defaultHNSWConfig)
	})

	t.Run("custom hnsw config is used", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		customConfig := &hnsw.Config{
			M:              32,
			MMax:           64,
			EfConstruction: 400,
			EfSearch:       200,
			DistanceMetric: hnsw.CosineDistance,
		}
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, customConfig, nil, log)
		require.NoError(t, err)
		assert.Equal(t, 32, vsm.defaultHNSWConfig.M)
		assert.Equal(t, hnsw.CosineDistance, vsm.defaultHNSWConfig.DistanceMetric)
	})
}

// =============================================================================
// Lookup Routing Edge Cases
// =============================================================================

func TestVectorStateMachine_Lookup_ClosedStateMachine(t *testing.T) {
	t.Run("lookup on closed state machine returns error", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		err = vsm.Close(context.Background())
		require.NoError(t, err)

		cmd := &VectorCommand{Op: OpHealthCheck}
		_, err = vsm.Lookup(context.Background(), cmd)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "closed")
	})

	t.Run("lookup with invalid query type returns error", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		_, err = vsm.Lookup(context.Background(), 42) // int, not []byte or *VectorCommand
		assert.Error(t, err)
	})

	t.Run("lookup with invalid byte data returns error", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		_, err = vsm.Lookup(context.Background(), []byte("bad msgpack data"))
		assert.Error(t, err)
	})

	t.Run("lookup with VectorCommand directly", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		cmd := &VectorCommand{Op: OpHealthCheck}
		result, err := vsm.Lookup(context.Background(), cmd)
		require.NoError(t, err)
		assert.NotNil(t, result)
	})
}

// =============================================================================
// PreUpdate / PostUpdate Tests
// =============================================================================

func TestVectorStateMachine_PreUpdate(t *testing.T) {
	t.Run("succeeds on open state machine", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		entry := sm.Entry{Index: 1, Cmd: []byte("test")}
		err = vsm.PreUpdate(entry)
		assert.NoError(t, err)
	})

	t.Run("fails on closed state machine", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		vsm.closed = true

		entry := sm.Entry{Index: 1, Cmd: []byte("test")}
		err = vsm.PreUpdate(entry)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "closed")
	})
}

// =============================================================================
// PrepareSnapshot Tests
// =============================================================================

func TestVectorStateMachine_PrepareSnapshot(t *testing.T) {
	t.Run("prepares snapshot with namespace data", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		mockHNSW := NewMockHNSWIndex()
		log := newTestLogger()

		vsm, err := newTestVectorStateMachine(adapter, mockHNSW, nil, log)
		require.NoError(t, err)

		setupTestNamespace(vsm, "ns1", 128, mockHNSW)
		vsm.namespaces["ns1"].TenantID = "t1"
		vsm.namespaces["ns1"].OwnerUserID = "user1"
		vsm.namespaces["ns1"].Quotas = &NamespaceQuotas{MaxVectors: 1000}
		vsm.namespaces["ns1"].Labels = map[string]string{"env": "test"}
		vsm.namespaces["ns1"].StorageBytes = 5000

		snapshot, err := vsm.PrepareSnapshot()
		require.NoError(t, err)

		vecSnapshot := snapshot.(*VectorSnapshot)
		require.Contains(t, vecSnapshot.Namespaces, "ns1")

		ns := vecSnapshot.Namespaces["ns1"]
		assert.Equal(t, "t1", ns.TenantID)
		assert.Equal(t, "user1", ns.OwnerUserID)
		assert.NotNil(t, ns.Quotas)
		assert.Equal(t, int64(1000), ns.Quotas.MaxVectors)
		assert.Equal(t, "test", ns.Labels["env"])
		assert.Equal(t, int64(5000), ns.StorageBytes)
		assert.Greater(t, vecSnapshot.Timestamp, int64(0))
	})
}

// =============================================================================
// Update Error Handling Tests
// =============================================================================

func TestVectorStateMachine_Update_InvalidCommand(t *testing.T) {
	t.Run("invalid msgpack data returns error", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		adapter.vecStor = NewMockVectorStorageWrapper()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		err = vsm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: []byte("garbage")}}, make([]sm.Result, 1))
		assert.Error(t, err)
	})

	// An operation this state machine does not implement is REFUSED in the
	// result payload rather than returned to Raft. Returning it halted the
	// apply loop, after which every linearizable read on the shard burned the
	// caller's deadline (go-qrdb#520).
	t.Run("unknown operation is refused, not fatal", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		adapter.vecStor = NewMockVectorStorageWrapper()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		cmd := &VectorCommand{
			Op:        VectorOp(200), // Unknown
			Namespace: "test",
		}
		data, err := cmd.Encode()
		require.NoError(t, err)

		results := make([]sm.Result, 1)
		require.NoError(t, vsm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: data}}, results))

		msg, kind, ok := protocol.DecodeAppErrorKind(results[0].Data)
		require.True(t, ok, "refusal carries no encoded application error")
		assert.Equal(t, protocol.AppErrorCapability, kind, "refusal %q lost the capability marker", msg)
		assert.Contains(t, msg, "unknown operation")
	})

	// A refusal is not a fault, so it does not move the fault counter. The
	// counter still exists and still counts: the case above it — undecodable
	// bytes in a committed entry — is genuine divergence and does halt.
	t.Run("a refusal does not increment the error count", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		adapter.vecStor = NewMockVectorStorageWrapper()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		cmd := &VectorCommand{
			Op:        VectorOp(200), // Unknown operation
			Namespace: "test",
		}
		data, err := cmd.Encode()
		require.NoError(t, err)

		_ = vsm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: data}}, make([]sm.Result, 1))
		assert.Equal(t, uint64(0), vsm.GetMetrics().ErrorCount)
	})

	t.Run("a decode fault increments the error count", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		adapter.vecStor = NewMockVectorStorageWrapper()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		_ = vsm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: []byte("garbage")}}, make([]sm.Result, 1))
		assert.Equal(t, uint64(1), vsm.GetMetrics().ErrorCount)
	})
}

// Tests for defaultHNSWFactory and hnswIndexAdapter require the qrdb_vector
// build tag and live in vector_hnsw_coverage_test.go.

// =============================================================================
// Lookup unknown operation
// =============================================================================

func TestVectorStateMachine_Lookup_UnknownOp(t *testing.T) {
	t.Run("returns error for unknown lookup op", func(t *testing.T) {
		adapter := NewMockStorageAdapter()
		log := newTestLogger()
		vsm, err := NewVectorStateMachine(1, 1, "node1", adapter, nil, nil, nil, log)
		require.NoError(t, err)

		cmd := &VectorCommand{
			Op:        VectorOp(250), // Unknown lookup op
			Namespace: "test",
		}

		_, err = vsm.Lookup(context.Background(), cmd)
		assert.Error(t, err)
	})
}

// suppress unused import warnings
var (
	_ = api.NewRequiredFieldError
	_ = fmt.Sprintf
	_ = vector.Storage(nil)
)
