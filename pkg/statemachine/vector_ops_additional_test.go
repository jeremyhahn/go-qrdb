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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVectorResult_EncodeDecode(t *testing.T) {
	t.Run("successful result", func(t *testing.T) {
		result := &VectorResult{
			Success:   true,
			Version:   1,
			Timestamp: 123456789,
			Metadata: map[string]interface{}{
				"key": "value",
			},
		}

		// Encode
		data, err := result.Encode()
		require.NoError(t, err)
		assert.NotEmpty(t, data)

		// Decode
		decoded := &VectorResult{}
		err = decoded.Decode(data)
		require.NoError(t, err)

		assert.Equal(t, result.Success, decoded.Success)
		assert.Equal(t, result.Version, decoded.Version)
		assert.Equal(t, result.Timestamp, decoded.Timestamp)
	})

	t.Run("result with error", func(t *testing.T) {
		result := &VectorResult{
			Success: false,
			Error:   "test error",
		}

		data, err := result.Encode()
		require.NoError(t, err)

		decoded := &VectorResult{}
		err = decoded.Decode(data)
		require.NoError(t, err)

		assert.False(t, decoded.Success)
		assert.Equal(t, "test error", decoded.Error)
	})
}

func TestUpdateNamespaceResult_Decode(t *testing.T) {
	t.Run("successful decode", func(t *testing.T) {
		result := &UpdateNamespaceResult{
			Updated:         true,
			RequiresRebuild: true,
		}

		// Encode manually using msgpack
		data, err := result.Encode()
		require.NoError(t, err)

		// Decode
		decoded := &UpdateNamespaceResult{}
		err = decoded.Decode(data)
		require.NoError(t, err)

		assert.True(t, decoded.Updated)
		assert.True(t, decoded.RequiresRebuild)
	})

	t.Run("invalid data", func(t *testing.T) {
		result := &UpdateNamespaceResult{}
		err := result.Decode([]byte("invalid msgpack data"))
		assert.Error(t, err)
	})
}

func TestNewUpdateMetadataCommand(t *testing.T) {
	metadata := map[string]interface{}{
		"field1": "value1",
		"field2": 123,
	}

	cmd := NewUpdateMetadataCommand("test_ns", 100, metadata, true, "test-node")
	require.NotNil(t, cmd)

	assert.Equal(t, OpUpdateMetadata, cmd.Op)
	assert.Equal(t, "test_ns", cmd.Namespace)
	assert.Equal(t, uint64(100), cmd.ID)
	assert.Equal(t, metadata, cmd.Metadata)
	assert.True(t, cmd.Merge)
	assert.Equal(t, "test-node", cmd.NodeID)
	assert.Greater(t, cmd.Timestamp, int64(0))
}

func TestNewBatchDeleteCommand(t *testing.T) {
	ids := []uint64{1, 2, 3, 4, 5}

	cmd := NewBatchDeleteCommand("test_ns", ids, "test-node")
	require.NotNil(t, cmd)

	assert.Equal(t, OpDeleteVector, cmd.Op)
	assert.Equal(t, "test_ns", cmd.Namespace)
	assert.Equal(t, ids, cmd.IDs)
	assert.Equal(t, "test-node", cmd.NodeID)
	assert.Greater(t, cmd.Timestamp, int64(0))
}

func TestVectorCommand_AllConstructors(t *testing.T) {
	t.Run("NewDeleteNamespaceCommand", func(t *testing.T) {
		cmd := NewDeleteNamespaceCommand("test_ns", "test-node")
		assert.Equal(t, OpDeleteNamespace, cmd.Op)
		assert.Equal(t, "test_ns", cmd.Namespace)
		assert.Equal(t, "test-node", cmd.NodeID)
	})

	t.Run("NewGetVectorCommand", func(t *testing.T) {
		cmd := NewGetVectorCommand("test_ns", 123, "test-node")
		assert.Equal(t, OpGetVector, cmd.Op)
		assert.Equal(t, "test_ns", cmd.Namespace)
		assert.Equal(t, uint64(123), cmd.ID)
	})

	t.Run("NewSearchByIDCommand", func(t *testing.T) {
		cmd := NewSearchByIDCommand("test_ns", 456, 10, 100, "test-node")
		assert.Equal(t, OpSearchByID, cmd.Op)
		assert.Equal(t, "test_ns", cmd.Namespace)
		assert.Equal(t, uint64(456), cmd.ID)
		assert.Equal(t, 10, cmd.K)
		assert.Equal(t, 100, cmd.EfSearch)
	})

	t.Run("NewListVectorsCommand", func(t *testing.T) {
		cmd := NewListVectorsCommand("test_ns", 100, "cursor123", "test-node")
		assert.Equal(t, OpListVectors, cmd.Op)
		assert.Equal(t, "test_ns", cmd.Namespace)
		assert.Equal(t, 100, cmd.Limit)
		assert.Equal(t, "cursor123", cmd.Cursor)
	})

	t.Run("NewGetNamespaceInfoCommand", func(t *testing.T) {
		cmd := NewGetNamespaceInfoCommand("test_ns", "test-node")
		assert.Equal(t, OpGetNamespaceInfo, cmd.Op)
		assert.Equal(t, "test_ns", cmd.Namespace)
	})

	t.Run("NewListNamespacesCommand", func(t *testing.T) {
		cmd := NewListNamespacesCommand("test-node")
		assert.Equal(t, OpListNamespaces, cmd.Op)
		assert.Equal(t, "test-node", cmd.NodeID)
	})

	t.Run("NewUpdateNamespaceCommand", func(t *testing.T) {
		indexParams := map[string]string{"M": "16", "efConstruction": "200"}
		metadata := map[string]interface{}{"description": "test"}
		cmd := NewUpdateNamespaceCommand("test_ns", indexParams, metadata, "test-node")
		assert.Equal(t, OpUpdateNamespace, cmd.Op)
		assert.Equal(t, "test_ns", cmd.Namespace)
		assert.Equal(t, indexParams, cmd.IndexParams)
		assert.Equal(t, metadata, cmd.Metadata)
	})

	t.Run("NewGetStatsCommand", func(t *testing.T) {
		cmd := NewGetStatsCommand("test_ns", "test-node")
		assert.Equal(t, OpGetStats, cmd.Op)
		assert.Equal(t, "test_ns", cmd.Namespace)
	})

	t.Run("NewRebuildIndexCommand", func(t *testing.T) {
		cmd := NewRebuildIndexCommand("test_ns", "job-123", "test-node")
		assert.Equal(t, OpRebuildIndex, cmd.Op)
		assert.Equal(t, "test_ns", cmd.Namespace)
		assert.Equal(t, "job-123", cmd.JobID)
	})

	t.Run("NewGetIndexProgressCommand", func(t *testing.T) {
		cmd := NewGetIndexProgressCommand("test_ns", "job-456", "test-node")
		assert.Equal(t, OpGetIndexProgress, cmd.Op)
		assert.Equal(t, "test_ns", cmd.Namespace)
		assert.Equal(t, "job-456", cmd.JobID)
	})

	t.Run("NewHealthCheckCommand", func(t *testing.T) {
		cmd := NewHealthCheckCommand("test-node")
		assert.Equal(t, OpHealthCheck, cmd.Op)
		assert.Equal(t, "test-node", cmd.NodeID)
	})
}

func TestVectorCommand_ValidateEdgeCases(t *testing.T) {
	t.Run("UpdateMetadata missing ID", func(t *testing.T) {
		cmd := &VectorCommand{
			Op:        OpUpdateMetadata,
			Namespace: "test",
			Metadata:  map[string]interface{}{"key": "value"},
		}
		err := cmd.Validate()
		assert.Error(t, err)
	})

	t.Run("UpdateMetadata missing metadata", func(t *testing.T) {
		cmd := &VectorCommand{
			Op:        OpUpdateMetadata,
			Namespace: "test",
			ID:        1,
		}
		err := cmd.Validate()
		assert.Error(t, err)
	})

	t.Run("Delete with IDs array", func(t *testing.T) {
		cmd := &VectorCommand{
			Op:        OpDeleteVector,
			Namespace: "test",
			IDs:       []uint64{1, 2, 3},
		}
		err := cmd.Validate()
		assert.NoError(t, err)
	})

	t.Run("BatchUpsert missing IDs", func(t *testing.T) {
		cmd := &VectorCommand{
			Op:        OpBatchUpsert,
			Namespace: "test",
			Vectors:   [][]float32{{0.1}},
		}
		err := cmd.Validate()
		assert.Error(t, err)
	})

	t.Run("SearchByID missing ID", func(t *testing.T) {
		cmd := &VectorCommand{
			Op:        OpSearchByID,
			Namespace: "test",
			K:         10,
		}
		err := cmd.Validate()
		assert.Error(t, err)
	})

	t.Run("SearchByID invalid K", func(t *testing.T) {
		cmd := &VectorCommand{
			Op:        OpSearchByID,
			Namespace: "test",
			ID:        1,
			K:         0,
		}
		err := cmd.Validate()
		assert.Error(t, err)
	})

	t.Run("GetVector missing ID", func(t *testing.T) {
		cmd := &VectorCommand{
			Op:        OpGetVector,
			Namespace: "test",
		}
		err := cmd.Validate()
		assert.Error(t, err)
	})

	t.Run("HealthCheck no namespace required", func(t *testing.T) {
		cmd := &VectorCommand{
			Op: OpHealthCheck,
		}
		err := cmd.Validate()
		assert.NoError(t, err)
	})

	t.Run("ListNamespaces no namespace required", func(t *testing.T) {
		cmd := &VectorCommand{
			Op: OpListNamespaces,
		}
		err := cmd.Validate()
		assert.NoError(t, err)
	})
}

func TestVectorCommand_SizeCalculation(t *testing.T) {
	t.Run("command with index params", func(t *testing.T) {
		cmd := &VectorCommand{
			Op:        OpCreateNamespace,
			Namespace: "test",
			Dimension: 128,
			IndexParams: map[string]string{
				"M":              "16",
				"efConstruction": "200",
			},
		}
		size := cmd.Size()
		assert.Greater(t, size, 200) // Should include index params
	})

	t.Run("command with batch metadata", func(t *testing.T) {
		cmd := &VectorCommand{
			Op:        OpBatchUpsert,
			Namespace: "test",
			IDs:       []uint64{1, 2, 3},
			Vectors:   [][]float32{{0.1}, {0.2}, {0.3}},
			BatchMeta: []map[string]interface{}{
				{"field": "value1"},
				{"field": "value2"},
				{"field": "value3"},
			},
		}
		size := cmd.Size()
		assert.Greater(t, size, 50)
	})
}

func TestVectorOp_StringAllOperations(t *testing.T) {
	tests := []struct {
		op       VectorOp
		expected string
	}{
		{OpGetVector, "GetVector"},
		{OpSearchByID, "SearchByID"},
		{OpListVectors, "ListVectors"},
		{OpGetNamespaceInfo, "GetNamespaceInfo"},
		{OpListNamespaces, "ListNamespaces"},
		{OpUpdateNamespace, "UpdateNamespace"},
		{OpGetStats, "GetStats"},
		{OpRebuildIndex, "RebuildIndex"},
		{OpGetIndexProgress, "GetIndexProgress"},
		{OpHealthCheck, "HealthCheck"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.op.String())
		})
	}
}

func TestVectorResult_EncodeError(t *testing.T) {
	// Test encode with complex nested structures
	result := &VectorResult{
		Success: true,
		Results: []VectorSearchResult{
			{
				ID:     1,
				Score:  0.95,
				Vector: []float32{0.1, 0.2, 0.3},
				Metadata: map[string]interface{}{
					"nested": map[string]interface{}{
						"deep": "value",
					},
				},
			},
		},
	}

	data, err := result.Encode()
	require.NoError(t, err)
	assert.NotEmpty(t, data)

	decoded := &VectorResult{}
	err = decoded.Decode(data)
	require.NoError(t, err)
	assert.True(t, decoded.Success)
	assert.Len(t, decoded.Results, 1)
}

func TestVectorResult_DecodeInvalidData(t *testing.T) {
	result := &VectorResult{}
	err := result.Decode([]byte("completely invalid msgpack data here"))
	assert.Error(t, err)
}

func TestUpdateNamespaceResult_Encode(t *testing.T) {
	result := &UpdateNamespaceResult{
		Updated:         true,
		RequiresRebuild: false,
	}

	data, err := result.Encode()
	require.NoError(t, err)
	assert.NotEmpty(t, data)

	decoded := &UpdateNamespaceResult{}
	err = decoded.Decode(data)
	require.NoError(t, err)
	assert.True(t, decoded.Updated)
	assert.False(t, decoded.RequiresRebuild)
}

func TestDecodeVectorCommand_InvalidData(t *testing.T) {
	_, err := DecodeVectorCommand([]byte("invalid msgpack"))
	assert.Error(t, err)
}

func TestVectorCommand_EncodeDecodeAllCommands(t *testing.T) {
	commands := []struct {
		name string
		cmd  *VectorCommand
	}{
		{
			name: "UpdateMetadata",
			cmd:  NewUpdateMetadataCommand("test", 1, map[string]interface{}{"key": "val"}, false, "node"),
		},
		{
			name: "BatchDelete",
			cmd:  NewBatchDeleteCommand("test", []uint64{1, 2, 3}, "node"),
		},
		{
			name: "DeleteNamespace",
			cmd:  NewDeleteNamespaceCommand("test", "node"),
		},
		{
			name: "GetVector",
			cmd:  NewGetVectorCommand("test", 123, "node"),
		},
		{
			name: "SearchByID",
			cmd:  NewSearchByIDCommand("test", 456, 10, 100, "node"),
		},
		{
			name: "ListVectors",
			cmd:  NewListVectorsCommand("test", 50, "cursor", "node"),
		},
		{
			name: "GetNamespaceInfo",
			cmd:  NewGetNamespaceInfoCommand("test", "node"),
		},
		{
			name: "ListNamespaces",
			cmd:  NewListNamespacesCommand("node"),
		},
		{
			name: "UpdateNamespace",
			cmd:  NewUpdateNamespaceCommand("test", map[string]string{"M": "16"}, map[string]interface{}{"desc": "test"}, "node"),
		},
		{
			name: "GetStats",
			cmd:  NewGetStatsCommand("test", "node"),
		},
		{
			name: "RebuildIndex",
			cmd:  NewRebuildIndexCommand("test", "job-1", "node"),
		},
		{
			name: "GetIndexProgress",
			cmd:  NewGetIndexProgressCommand("test", "job-2", "node"),
		},
		{
			name: "HealthCheck",
			cmd:  NewHealthCheckCommand("node"),
		},
	}

	for _, tc := range commands {
		t.Run(tc.name, func(t *testing.T) {
			// Encode
			data, err := tc.cmd.Encode()
			require.NoError(t, err, "Encode should succeed for %s", tc.name)
			assert.NotEmpty(t, data)

			// Decode
			decoded, err := DecodeVectorCommand(data)
			require.NoError(t, err, "Decode should succeed for %s", tc.name)
			assert.Equal(t, tc.cmd.Op, decoded.Op)
			assert.Equal(t, tc.cmd.Namespace, decoded.Namespace)
			assert.Equal(t, tc.cmd.NodeID, decoded.NodeID)
		})
	}
}

func TestUpdateNamespaceResult_EncodeDecode(t *testing.T) {
	result := &VectorResult{
		Success:         true,
		Updated:         true,
		RequiresRebuild: true,
	}

	data, err := result.Encode()
	require.NoError(t, err)

	decoded := &VectorResult{}
	err = decoded.Decode(data)
	require.NoError(t, err)
	assert.True(t, decoded.Updated)
	assert.True(t, decoded.RequiresRebuild)
}
