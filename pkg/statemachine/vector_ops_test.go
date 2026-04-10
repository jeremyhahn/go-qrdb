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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVectorCommand_Encode_Decode(t *testing.T) {
	t.Run("UpsertCommand", func(t *testing.T) {
		vector := make([]float32, 256)
		for i := range vector {
			vector[i] = float32(i) * 0.1
		}

		metadata := map[string]interface{}{
			"source": "test",
			"type":   "embeddings",
		}

		cmd := NewUpsertCommand("test_ns", 100, vector, metadata, "test-node")
		require.NotNil(t, cmd)

		// Encode
		data, err := cmd.Encode()
		require.NoError(t, err)
		require.NotEmpty(t, data)

		// Decode
		decoded, err := DecodeVectorCommand(data)
		require.NoError(t, err)
		require.NotNil(t, decoded)

		// Verify
		assert.Equal(t, OpUpsertVector, decoded.Op)
		assert.Equal(t, "test_ns", decoded.Namespace)
		assert.Equal(t, uint64(100), decoded.ID)
		assert.Len(t, decoded.Vector, 256)
		assert.Equal(t, "test-node", decoded.NodeID)
		assert.NotEmpty(t, decoded.Metadata)
	})

	t.Run("DeleteCommand", func(t *testing.T) {
		cmd := NewDeleteCommand("test_ns", 200, "test-node")
		require.NotNil(t, cmd)

		data, err := cmd.Encode()
		require.NoError(t, err)

		decoded, err := DecodeVectorCommand(data)
		require.NoError(t, err)

		assert.Equal(t, OpDeleteVector, decoded.Op)
		assert.Equal(t, "test_ns", decoded.Namespace)
		assert.Equal(t, uint64(200), decoded.ID)
	})

	t.Run("BatchUpsertCommand", func(t *testing.T) {
		ids := []uint64{1, 2, 3}
		vectors := make([][]float32, 3)
		for i := range vectors {
			vectors[i] = make([]float32, 128)
		}

		cmd := NewBatchUpsertCommand("test_ns", ids, vectors, "test-node")
		require.NotNil(t, cmd)

		data, err := cmd.Encode()
		require.NoError(t, err)

		decoded, err := DecodeVectorCommand(data)
		require.NoError(t, err)

		assert.Equal(t, OpBatchUpsert, decoded.Op)
		assert.Len(t, decoded.IDs, 3)
		assert.Len(t, decoded.Vectors, 3)
	})

	t.Run("CreateNamespaceCommand", func(t *testing.T) {
		cmd := NewCreateNamespaceCommand("new_ns", 512, "test-node")
		require.NotNil(t, cmd)

		data, err := cmd.Encode()
		require.NoError(t, err)

		decoded, err := DecodeVectorCommand(data)
		require.NoError(t, err)

		assert.Equal(t, OpCreateNamespace, decoded.Op)
		assert.Equal(t, "new_ns", decoded.Namespace)
		assert.Equal(t, uint32(512), decoded.Dimension)
	})

	t.Run("SearchCommand", func(t *testing.T) {
		queryVector := make([]float32, 256)
		cmd := NewSearchCommand("test_ns", queryVector, 10, 100, "test-node")
		require.NotNil(t, cmd)

		data, err := cmd.Encode()
		require.NoError(t, err)

		decoded, err := DecodeVectorCommand(data)
		require.NoError(t, err)

		assert.Equal(t, OpSearchSimilar, decoded.Op)
		assert.Equal(t, 10, decoded.K)
		assert.Equal(t, 100, decoded.EfSearch)
	})
}

func TestVectorCommand_Validate(t *testing.T) {
	t.Run("ValidUpsert", func(t *testing.T) {
		cmd := NewUpsertCommand("test", 1, make([]float32, 128), nil, "node")
		err := cmd.Validate()
		assert.NoError(t, err)
	})

	t.Run("MissingNamespace", func(t *testing.T) {
		cmd := &VectorCommand{
			Op: OpUpsertVector,
			ID: 1,
		}
		err := cmd.Validate()
		assert.Error(t, err)
	})

	t.Run("UpsertMissingID", func(t *testing.T) {
		cmd := &VectorCommand{
			Op:        OpUpsertVector,
			Namespace: "test",
			Vector:    make([]float32, 128),
		}
		err := cmd.Validate()
		assert.Error(t, err)
	})

	t.Run("UpsertMissingVector", func(t *testing.T) {
		cmd := &VectorCommand{
			Op:        OpUpsertVector,
			Namespace: "test",
			ID:        1,
		}
		err := cmd.Validate()
		assert.Error(t, err)
	})

	t.Run("DeleteMissingID", func(t *testing.T) {
		cmd := &VectorCommand{
			Op:        OpDeleteVector,
			Namespace: "test",
		}
		err := cmd.Validate()
		assert.Error(t, err)
	})

	t.Run("BatchUpsertMismatch", func(t *testing.T) {
		cmd := &VectorCommand{
			Op:        OpBatchUpsert,
			Namespace: "test",
			IDs:       []uint64{1, 2},
			Vectors:   [][]float32{{0.1}}, // Length mismatch
		}
		err := cmd.Validate()
		assert.Error(t, err)
	})

	t.Run("CreateNamespaceMissingDimension", func(t *testing.T) {
		cmd := &VectorCommand{
			Op:        OpCreateNamespace,
			Namespace: "test",
		}
		err := cmd.Validate()
		assert.Error(t, err)
	})

	t.Run("SearchMissingVector", func(t *testing.T) {
		cmd := &VectorCommand{
			Op:        OpSearchSimilar,
			Namespace: "test",
			K:         10,
		}
		err := cmd.Validate()
		assert.Error(t, err)
	})

	t.Run("SearchInvalidK", func(t *testing.T) {
		cmd := &VectorCommand{
			Op:        OpSearchSimilar,
			Namespace: "test",
			Vector:    make([]float32, 128),
			K:         0,
		}
		err := cmd.Validate()
		assert.Error(t, err)
	})
}

func TestVectorCommand_Size(t *testing.T) {
	t.Run("SmallCommand", func(t *testing.T) {
		cmd := NewDeleteCommand("test", 1, "node")
		size := cmd.Size()
		assert.Greater(t, size, 0)
	})

	t.Run("LargeCommand", func(t *testing.T) {
		vector := make([]float32, 1024)
		metadata := make(map[string]interface{})
		for i := 0; i < 10; i++ {
			metadata[string(rune('a'+i))] = "value"
		}

		cmd := NewUpsertCommand("test", 1, vector, metadata, "node")
		size := cmd.Size()
		assert.Greater(t, size, 4096) // Should be > 4KB with vector data
	})

	t.Run("BatchCommand", func(t *testing.T) {
		ids := make([]uint64, 100)
		vectors := make([][]float32, 100)
		for i := range vectors {
			ids[i] = uint64(i)
			vectors[i] = make([]float32, 256)
		}

		cmd := NewBatchUpsertCommand("test", ids, vectors, "node")
		size := cmd.Size()
		assert.Greater(t, size, 100000) // Should be > 100KB
	})
}

func TestVectorOp_String(t *testing.T) {
	tests := []struct {
		op       VectorOp
		expected string
	}{
		{OpUpsertVector, "UpsertVector"},
		{OpDeleteVector, "DeleteVector"},
		{OpBatchUpsert, "BatchUpsert"},
		{OpCreateNamespace, "CreateNamespace"},
		{OpDeleteNamespace, "DeleteNamespace"},
		{OpUpdateMetadata, "UpdateMetadata"},
		{OpSearchSimilar, "SearchSimilar"},
		{VectorOp(99), "Unknown(99)"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.op.String())
		})
	}
}

func TestEncodeDecodeUint64(t *testing.T) {
	tests := []uint64{0, 1, 100, 1000, 1<<32 - 1, 1<<64 - 1}

	for _, val := range tests {
		t.Run("", func(t *testing.T) {
			encoded := EncodeUint64(val)
			assert.Len(t, encoded, 8)

			decoded := DecodeUint64(encoded)
			assert.Equal(t, val, decoded)
		})
	}
}

func TestEncodeDecodeUint32(t *testing.T) {
	tests := []uint32{0, 1, 100, 1000, 1<<16 - 1, 1<<32 - 1}

	for _, val := range tests {
		t.Run("", func(t *testing.T) {
			encoded := EncodeUint32(val)
			assert.Len(t, encoded, 4)

			decoded := DecodeUint32(encoded)
			assert.Equal(t, val, decoded)
		})
	}
}

func TestDecodeUint64_ShortBuffer(t *testing.T) {
	result := DecodeUint64([]byte{1, 2, 3}) // Too short
	assert.Equal(t, uint64(0), result)
}

func TestDecodeUint32_ShortBuffer(t *testing.T) {
	result := DecodeUint32([]byte{1, 2}) // Too short
	assert.Equal(t, uint32(0), result)
}

func TestVectorCommand_Timestamp(t *testing.T) {
	before := time.Now().UnixNano()
	cmd := NewUpsertCommand("test", 1, make([]float32, 128), nil, "node")
	after := time.Now().UnixNano()

	assert.GreaterOrEqual(t, cmd.Timestamp, before)
	assert.LessOrEqual(t, cmd.Timestamp, after)
}

// TestNewCreateNamespaceCommandFull tests that all namespace configuration fields
// are properly encoded and decoded, guarding against regression in namespace
// field propagation through the Raft chain.
func TestNewCreateNamespaceCommandFull(t *testing.T) {
	t.Run("AllFieldsPopulated", func(t *testing.T) {
		cfg := &NamespaceConfig{
			Namespace:      "test-ns-full",
			Dimension:      512,
			DistanceMetric: "L2",
			IndexType:      "HNSW",
			IndexParams: map[string]string{
				"M":              "16",
				"efConstruction": "200",
			},
			Metadata: map[string]string{
				"description": "Test namespace",
				"owner":       "test-user",
			},
			NodeID: "node-1",
		}

		cmd := NewCreateNamespaceCommandFull(cfg)
		require.NotNil(t, cmd)

		// Verify command fields before encoding
		assert.Equal(t, OpCreateNamespace, cmd.Op)
		assert.Equal(t, "test-ns-full", cmd.Namespace)
		assert.Equal(t, uint32(512), cmd.Dimension)
		assert.Equal(t, "L2", cmd.DistanceMetric)
		assert.Equal(t, "HNSW", cmd.IndexType)
		assert.Equal(t, map[string]string{"M": "16", "efConstruction": "200"}, cmd.IndexParams)
		assert.Equal(t, map[string]string{"description": "Test namespace", "owner": "test-user"}, cmd.NamespaceMetadata)
		assert.Equal(t, "node-1", cmd.NodeID)

		// Encode and decode
		data, err := cmd.Encode()
		require.NoError(t, err)

		decoded, err := DecodeVectorCommand(data)
		require.NoError(t, err)

		// Verify all fields are preserved after encode/decode round-trip
		assert.Equal(t, OpCreateNamespace, decoded.Op)
		assert.Equal(t, "test-ns-full", decoded.Namespace)
		assert.Equal(t, uint32(512), decoded.Dimension)
		assert.Equal(t, "L2", decoded.DistanceMetric)
		assert.Equal(t, "HNSW", decoded.IndexType)
		assert.Equal(t, "16", decoded.IndexParams["M"])
		assert.Equal(t, "200", decoded.IndexParams["efConstruction"])
		assert.Equal(t, "Test namespace", decoded.NamespaceMetadata["description"])
		assert.Equal(t, "test-user", decoded.NamespaceMetadata["owner"])
		assert.Equal(t, "node-1", decoded.NodeID)
	})

	t.Run("MinimalFieldsOnly", func(t *testing.T) {
		cfg := &NamespaceConfig{
			Namespace: "minimal-ns",
			Dimension: 128,
			NodeID:    "node-1",
		}

		cmd := NewCreateNamespaceCommandFull(cfg)
		require.NotNil(t, cmd)

		data, err := cmd.Encode()
		require.NoError(t, err)

		decoded, err := DecodeVectorCommand(data)
		require.NoError(t, err)

		assert.Equal(t, "minimal-ns", decoded.Namespace)
		assert.Equal(t, uint32(128), decoded.Dimension)
		assert.Empty(t, decoded.DistanceMetric)  // Should be empty
		assert.Empty(t, decoded.IndexType)       // Should be empty
		assert.Nil(t, decoded.IndexParams)       // Should be nil
		assert.Nil(t, decoded.NamespaceMetadata) // Should be nil
	})

	t.Run("DifferentDistanceMetrics", func(t *testing.T) {
		metrics := []string{"L2", "COSINE", "IP", "DOT_PRODUCT"}
		for _, metric := range metrics {
			t.Run(metric, func(t *testing.T) {
				cfg := &NamespaceConfig{
					Namespace:      "metric-test",
					Dimension:      256,
					DistanceMetric: metric,
					NodeID:         "node-1",
				}

				cmd := NewCreateNamespaceCommandFull(cfg)
				data, err := cmd.Encode()
				require.NoError(t, err)

				decoded, err := DecodeVectorCommand(data)
				require.NoError(t, err)

				assert.Equal(t, metric, decoded.DistanceMetric)
			})
		}
	})

	t.Run("DifferentIndexTypes", func(t *testing.T) {
		indexTypes := []string{"HNSW", "FLAT", "IVF_FLAT", "IVF_PQ"}
		for _, indexType := range indexTypes {
			t.Run(indexType, func(t *testing.T) {
				cfg := &NamespaceConfig{
					Namespace: "index-test",
					Dimension: 256,
					IndexType: indexType,
					NodeID:    "node-1",
				}

				cmd := NewCreateNamespaceCommandFull(cfg)
				data, err := cmd.Encode()
				require.NoError(t, err)

				decoded, err := DecodeVectorCommand(data)
				require.NoError(t, err)

				assert.Equal(t, indexType, decoded.IndexType)
			})
		}
	})

	t.Run("LargeMetadata", func(t *testing.T) {
		metadata := make(map[string]string)
		for i := 0; i < 50; i++ {
			metadata[fmt.Sprintf("key_%d", i)] = fmt.Sprintf("value_%d_with_some_longer_content", i)
		}

		cfg := &NamespaceConfig{
			Namespace: "large-meta-ns",
			Dimension: 128,
			Metadata:  metadata,
			NodeID:    "node-1",
		}

		cmd := NewCreateNamespaceCommandFull(cfg)
		data, err := cmd.Encode()
		require.NoError(t, err)

		decoded, err := DecodeVectorCommand(data)
		require.NoError(t, err)

		assert.Len(t, decoded.NamespaceMetadata, 50)
		for i := 0; i < 50; i++ {
			assert.Equal(t, fmt.Sprintf("value_%d_with_some_longer_content", i), decoded.NamespaceMetadata[fmt.Sprintf("key_%d", i)])
		}
	})
}

// TestNamespaceConfigValidation tests that NamespaceConfig struct properly validates.
func TestNamespaceConfigValidation(t *testing.T) {
	t.Run("ValidConfig", func(t *testing.T) {
		cfg := &NamespaceConfig{
			Namespace: "valid",
			Dimension: 128,
			NodeID:    "node-1",
		}
		cmd := NewCreateNamespaceCommandFull(cfg)
		err := cmd.Validate()
		assert.NoError(t, err)
	})

	t.Run("EmptyNamespace", func(t *testing.T) {
		cfg := &NamespaceConfig{
			Namespace: "",
			Dimension: 128,
			NodeID:    "node-1",
		}
		cmd := NewCreateNamespaceCommandFull(cfg)
		err := cmd.Validate()
		assert.Error(t, err)
	})

	t.Run("ZeroDimension", func(t *testing.T) {
		cfg := &NamespaceConfig{
			Namespace: "zero-dim",
			Dimension: 0,
			NodeID:    "node-1",
		}
		cmd := NewCreateNamespaceCommandFull(cfg)
		err := cmd.Validate()
		assert.Error(t, err)
	})
}
