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
	"encoding/hex"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	vectorStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
)

// encodingFixtureCommand returns a VectorCommand for op with the namespace,
// vector, metadata and node fields set. Every map holds one key: MessagePack
// map order is not sorted, so a multi-key map has no single encoding.
func encodingFixtureCommand(op VectorOp) *VectorCommand {
	return &VectorCommand{
		Op:        op,
		Namespace: "docs",
		ID:        42,
		Vector:    []float32{0.5, -1.25, 3},
		Metadata:  map[string]interface{}{"color": "red"},
		Timestamp: 1700000000,
		NodeID:    "node-1",
	}
}

// TestVectorEncoding_BytesUnchanged pins the MessagePack encoding of every
// VectorCommand op, VectorResult and UpdateNamespaceResult: Raft log entries
// already written must decode unchanged after a codec change.
func TestVectorEncoding_BytesUnchanged(t *testing.T) {
	type cmdCase struct {
		name string
		cmd  *VectorCommand
		want string
	}
	with := func(op VectorOp, edit func(*VectorCommand)) *VectorCommand {
		c := encodingFixtureCommand(op)
		if edit != nil {
			edit(c)
		}
		return c
	}
	cmds := []cmdCase{
		{"UpsertVector", with(OpUpsertVector, nil), "87a26f70cc01a26e73a4646f6373a26964cf000000000000002aa376656393ca3f000000cabfa00000ca40400000a46d65746181a5636f6c6f72a3726564a27473d3000000006553f100a46e6f6465a66e6f64652d31"},
		{"DeleteVector", with(OpDeleteVector, nil), "87a26f70cc02a26e73a4646f6373a26964cf000000000000002aa376656393ca3f000000cabfa00000ca40400000a46d65746181a5636f6c6f72a3726564a27473d3000000006553f100a46e6f6465a66e6f64652d31"},
		{"BatchUpsert", with(OpBatchUpsert, func(c *VectorCommand) {
			c.IDs = []uint64{1, 2}
			c.Vectors = [][]float32{{1, 2, 3}, {4, 5, 6}}
			c.BatchMeta = []map[string]interface{}{{"k": "a"}, {"k": "b"}}
		}), "8aa26f70cc03a26e73a4646f6373a26964cf000000000000002aa376656393ca3f000000cabfa00000ca40400000a46d65746181a5636f6c6f72a3726564a27473d3000000006553f100a46e6f6465a66e6f64652d31a369647392cf0000000000000001cf0000000000000002a4766563739293ca3f800000ca40000000ca4040000093ca40800000ca40a00000ca40c00000aa62617463685f6d6574619281a16ba16181a16ba162"},
		{"CreateNamespace", with(OpCreateNamespace, func(c *VectorCommand) {
			c.Dimension = 3
			c.DistanceMetric = "cosine"
			c.IndexType = "hnsw"
			c.IndexParams = map[string]string{"m": "16"}
			c.NamespaceMetadata = map[string]string{"team": "search"}
			c.TenantID = "tenant-a"
			c.OwnerUserID = "user-1"
			c.Quotas = &NamespaceQuotas{MaxVectors: 1000, MaxStorageBytes: 1 << 20, MaxDimensions: 512, RateLimitQPS: 10, RateLimitWritesQPS: 5, MaxBatchSize: 100}
			c.Labels = map[string]string{"env": "test"}
		}), "de0010a26f70cc04a26e73a4646f6373a26964cf000000000000002aa376656393ca3f000000cabfa00000ca40400000a46d65746181a5636f6c6f72a3726564a27473d3000000006553f100a46e6f6465a66e6f64652d31a364696dce00000003ab646973745f6d6574726963a6636f73696e65a86964785f74797065a4686e7377aa6964785f706172616d7381a16da23136a76e735f6d65746181a47465616da6736561726368a974656e616e745f6964a874656e616e742d61ad6f776e65725f757365725f6964a6757365722d31a671756f74617386aa4d6178566563746f7273d300000000000003e8af4d617853746f726167654279746573d30000000000100000ad4d617844696d656e73696f6e73d200000200ac526174654c696d6974515053d20000000ab2526174654c696d6974577269746573515053d200000005ac4d6178426174636853697a65d200000064a66c6162656c7381a3656e76a474657374"},
		{"DeleteNamespace", with(OpDeleteNamespace, nil), "87a26f70cc05a26e73a4646f6373a26964cf000000000000002aa376656393ca3f000000cabfa00000ca40400000a46d65746181a5636f6c6f72a3726564a27473d3000000006553f100a46e6f6465a66e6f64652d31"},
		{"UpdateMetadata", with(OpUpdateMetadata, func(c *VectorCommand) { c.Merge = true; c.Version = 3 }), "89a26f70cc06a26e73a4646f6373a26964cf000000000000002aa376656393ca3f000000cabfa00000ca40400000a46d65746181a5636f6c6f72a3726564a56d65726765c3a3766572cf0000000000000003a27473d3000000006553f100a46e6f6465a66e6f64652d31"},
		{"SearchSimilar", with(OpSearchSimilar, func(c *VectorCommand) {
			c.K = 5
			c.EfSearch = 64
			c.MaxResults = 10
			c.MinScore = 0.25
		}), "8ba26f70cc07a26e73a4646f6373a26964cf000000000000002aa376656393ca3f000000cabfa00000ca40400000a46d65746181a5636f6c6f72a3726564a27473d3000000006553f100a46e6f6465a66e6f64652d31a16b05a2656640a36d61780aa96d696e5f73636f7265ca3e800000"},
		{"GetVector", with(OpGetVector, nil), "87a26f70cc08a26e73a4646f6373a26964cf000000000000002aa376656393ca3f000000cabfa00000ca40400000a46d65746181a5636f6c6f72a3726564a27473d3000000006553f100a46e6f6465a66e6f64652d31"},
		{"SearchByID", with(OpSearchByID, func(c *VectorCommand) { c.K = 5; c.EfSearch = 64 }), "89a26f70cc09a26e73a4646f6373a26964cf000000000000002aa376656393ca3f000000cabfa00000ca40400000a46d65746181a5636f6c6f72a3726564a27473d3000000006553f100a46e6f6465a66e6f64652d31a16b05a2656640"},
		{"ListVectors", with(OpListVectors, func(c *VectorCommand) { c.Limit = 20; c.Offset = 40; c.Cursor = "c1" }), "8aa26f70cc0aa26e73a4646f6373a26964cf000000000000002aa376656393ca3f000000cabfa00000ca40400000a46d65746181a5636f6c6f72a3726564a27473d3000000006553f100a46e6f6465a66e6f64652d31a56c696d697414a66f666673657428a6637572736f72a26331"},
		{"GetNamespaceInfo", with(OpGetNamespaceInfo, nil), "87a26f70cc0ba26e73a4646f6373a26964cf000000000000002aa376656393ca3f000000cabfa00000ca40400000a46d65746181a5636f6c6f72a3726564a27473d3000000006553f100a46e6f6465a66e6f64652d31"},
		{"ListNamespaces", with(OpListNamespaces, nil), "87a26f70cc0ca26e73a4646f6373a26964cf000000000000002aa376656393ca3f000000cabfa00000ca40400000a46d65746181a5636f6c6f72a3726564a27473d3000000006553f100a46e6f6465a66e6f64652d31"},
		{"UpdateNamespace", with(OpUpdateNamespace, func(c *VectorCommand) {
			c.IndexParams = map[string]string{"ef_construction": "200"}
		}), "88a26f70cc0da26e73a4646f6373a26964cf000000000000002aa376656393ca3f000000cabfa00000ca40400000a46d65746181a5636f6c6f72a3726564a27473d3000000006553f100a46e6f6465a66e6f64652d31aa6964785f706172616d7381af65665f636f6e737472756374696f6ea3323030"},
		{"GetStats", with(OpGetStats, nil), "87a26f70cc0ea26e73a4646f6373a26964cf000000000000002aa376656393ca3f000000cabfa00000ca40400000a46d65746181a5636f6c6f72a3726564a27473d3000000006553f100a46e6f6465a66e6f64652d31"},
		{"RebuildIndex", with(OpRebuildIndex, func(c *VectorCommand) { c.JobID = "job-1" }), "88a26f70cc0fa26e73a4646f6373a26964cf000000000000002aa376656393ca3f000000cabfa00000ca40400000a46d65746181a5636f6c6f72a3726564a27473d3000000006553f100a46e6f6465a66e6f64652d31a66a6f625f6964a56a6f622d31"},
		{"GetIndexProgress", with(OpGetIndexProgress, func(c *VectorCommand) { c.JobID = "job-1" }), "88a26f70cc10a26e73a4646f6373a26964cf000000000000002aa376656393ca3f000000cabfa00000ca40400000a46d65746181a5636f6c6f72a3726564a27473d3000000006553f100a46e6f6465a66e6f64652d31a66a6f625f6964a56a6f622d31"},
		{"HealthCheck", with(OpHealthCheck, nil), "87a26f70cc11a26e73a4646f6373a26964cf000000000000002aa376656393ca3f000000cabfa00000ca40400000a46d65746181a5636f6c6f72a3726564a27473d3000000006553f100a46e6f6465a66e6f64652d31"},
	}
	for _, tc := range cmds {
		t.Run("Command/"+tc.name, func(t *testing.T) {
			got, err := tc.cmd.Encode()
			require.NoError(t, err)
			if hex.EncodeToString(got) != tc.want {
				t.Fatalf("encoding changed:\n got %s\nwant %s", hex.EncodeToString(got), tc.want)
			}
			raw, err := hex.DecodeString(tc.want)
			require.NoError(t, err)
			decoded, err := DecodeVectorCommand(raw)
			require.NoError(t, err)
			if !reflect.DeepEqual(decoded, tc.cmd) {
				t.Fatalf("decoded %+v, want %+v", decoded, tc.cmd)
			}
		})
	}

	result := &VectorResult{
		Success:   true,
		Error:     "partial",
		Version:   9,
		Timestamp: 1700000000,
		Results: []VectorSearchResult{
			{ID: 1, Score: 0.75, Vector: []float32{1, 2}, Metadata: map[string]interface{}{"k": "v"}},
		},
		Metadata:        map[string]interface{}{"color": "red"},
		Vector:          []float32{0.5, -1.25},
		VectorID:        42,
		Vectors:         []VectorData{{ID: 2, Vector: []float32{3, 4}, Metadata: map[string]interface{}{"k": "w"}, Version: 1, CreatedAt: 10, UpdatedAt: 11}},
		NamespaceInfo:   &NamespaceData{Name: "docs", Dimension: 2, VectorCount: 1, CreatedAt: 10, UpdatedAt: 11},
		Namespaces:      []string{"docs", "img"},
		NextCursor:      "c2",
		TotalCount:      7,
		Updated:         true,
		RequiresRebuild: true,
		VectorStats:     &VectorStats{VectorCount: 1, Dimension: 2, IndexSize: 3, StorageSize: 4},
		IndexProgress:   &IndexProgress{InProgress: true, VectorsProcessed: 5, TotalVectors: 10, PercentComplete: 50, JobID: "job-1"},
		HealthStatus:    &HealthStatus{Healthy: true, Message: "ok", LeaderID: 1, ShardID: 2, NodeCount: 3},
	}
	t.Run("VectorResult", func(t *testing.T) {
		const want = "de0012a773756363657373c3a56572726f72a77061727469616ca3766572cf0000000000000009a27473d3000000006553f100a7726573756c74739184a26964cf0000000000000001a573636f7265ca3f400000a376656392ca3f800000ca40000000a46d65746181a16ba176a46d65746181a5636f6c6f72a3726564a376656392ca3f000000cabfa00000a67665635f6964cf000000000000002aa7766563746f72739186a26964cf0000000000000002a376656392ca40400000ca40800000a46d65746181a16ba177a3766572cf0000000000000001a763726561746564d3000000000000000aa775706461746564d3000000000000000ba76e735f696e666f85a46e616d65a4646f6373a364696dce00000002a5636f756e74cf0000000000000001a763726561746564d3000000000000000aa775706461746564d3000000000000000baa6e616d6573706163657392a4646f6373a3696d67ab6e6578745f637572736f72a26332a5746f74616ccf0000000000000007a775706461746564c3b072657175697265735f72656275696c64c3ac766563746f725f737461747384ac766563746f725f636f756e74cf0000000000000001a964696d656e73696f6ece00000002aa696e6465785f73697a65cf0000000000000003ac73746f726167655f73697a65cf0000000000000004ae696e6465785f70726f677265737385ab696e5f70726f6772657373c3b1766563746f72735f70726f636573736564cf0000000000000005ad746f74616c5f766563746f7273cf000000000000000ab070657263656e745f636f6d706c657465cb4049000000000000a66a6f625f6964a56a6f622d31ad6865616c74685f73746174757385a76865616c746879c3a76d657373616765a26f6ba96c65616465725f6964cf0000000000000001a873686172645f6964cf0000000000000002aa6e6f64655f636f756e7403"
		got, err := result.Encode()
		require.NoError(t, err)
		if hex.EncodeToString(got) != want {
			t.Fatalf("encoding changed:\n got %s\nwant %s", hex.EncodeToString(got), want)
		}
		raw, err := hex.DecodeString(want)
		require.NoError(t, err)
		decoded := &VectorResult{}
		require.NoError(t, decoded.Decode(raw))
		if !reflect.DeepEqual(decoded, result) {
			t.Fatalf("decoded %+v, want %+v", decoded, result)
		}
	})

	update := &UpdateNamespaceResult{Updated: true, RequiresRebuild: true}
	t.Run("UpdateNamespaceResult", func(t *testing.T) {
		const want = "82a775706461746564c3b072657175697265735f72656275696c64c3"
		got, err := update.Encode()
		require.NoError(t, err)
		if hex.EncodeToString(got) != want {
			t.Fatalf("encoding changed:\n got %s\nwant %s", hex.EncodeToString(got), want)
		}
		raw, err := hex.DecodeString(want)
		require.NoError(t, err)
		decoded := &UpdateNamespaceResult{}
		require.NoError(t, decoded.Decode(raw))
		if !reflect.DeepEqual(decoded, update) {
			t.Fatalf("decoded %+v, want %+v", decoded, update)
		}
	})
}

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

		cmd := NewUpsertCommand("test_ns", 100, vector, metadata, vectorStorage.MetadataFormatJSON, "test-node")
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

		cmd := NewBatchUpsertCommand("test_ns", ids, vectors, nil, vectorStorage.MetadataFormatJSON, "test-node")
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
		cmd := NewUpsertCommand("test", 1, make([]float32, 128), nil, vectorStorage.MetadataFormatJSON, "node")
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

		cmd := NewUpsertCommand("test", 1, vector, metadata, vectorStorage.MetadataFormatJSON, "node")
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

		cmd := NewBatchUpsertCommand("test", ids, vectors, nil, vectorStorage.MetadataFormatJSON, "node")
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
	cmd := NewUpsertCommand("test", 1, make([]float32, 128), nil, vectorStorage.MetadataFormatJSON, "node")
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
