//go:build raft && qrdb_vector

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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/vector/hnsw"
)

// TestDefaultHNSWFactory exercises the default factory used by
// VectorStateMachine to create HNSW indexes. The factory returns an
// adapter implementing HNSWIndex over the concrete HNSW index from
// pkg/vector/hnsw, which is only compiled when the qrdb_vector build
// tag is set.
func TestDefaultHNSWFactory(t *testing.T) {
	t.Run("creates HNSW index with config", func(t *testing.T) {
		config := hnsw.DefaultConfig()

		idx, err := defaultHNSWFactory(config)
		require.NoError(t, err)
		require.NotNil(t, idx)

		assert.Equal(t, 0, idx.Size())

		err = idx.Add(1, []float32{0.1, 0.2, 0.3, 0.4})
		require.NoError(t, err)
		assert.Equal(t, 1, idx.Size())
	})

	t.Run("creates index with different distance metrics", func(t *testing.T) {
		for _, metric := range []hnsw.DistanceMetric{hnsw.L2Distance, hnsw.CosineDistance, hnsw.DotProductDistance} {
			config := hnsw.DefaultConfig()
			config.DistanceMetric = metric

			idx, err := defaultHNSWFactory(config)
			require.NoError(t, err)
			require.NotNil(t, idx)
		}
	})
}

// TestHnswIndexAdapter exercises the adapter methods (Add, Delete, Search,
// SaveSnapshot, LoadSnapshot, Reset) that wrap the concrete HNSW index.
func TestHnswIndexAdapter(t *testing.T) {
	t.Run("Add and Size", func(t *testing.T) {
		config := hnsw.DefaultConfig()

		idx, err := defaultHNSWFactory(config)
		require.NoError(t, err)

		err = idx.Add(1, []float32{0.1, 0.2, 0.3, 0.4})
		require.NoError(t, err)

		err = idx.Add(2, []float32{0.5, 0.6, 0.7, 0.8})
		require.NoError(t, err)

		assert.Equal(t, 2, idx.Size())
	})

	t.Run("Delete", func(t *testing.T) {
		config := hnsw.DefaultConfig()

		idx, err := defaultHNSWFactory(config)
		require.NoError(t, err)

		err = idx.Add(1, []float32{0.1, 0.2, 0.3, 0.4})
		require.NoError(t, err)

		err = idx.Delete(1)
		require.NoError(t, err)
	})

	t.Run("Search", func(t *testing.T) {
		config := hnsw.DefaultConfig()

		idx, err := defaultHNSWFactory(config)
		require.NoError(t, err)

		err = idx.Add(1, []float32{0.1, 0.2, 0.3, 0.4})
		require.NoError(t, err)

		err = idx.Add(2, []float32{0.5, 0.6, 0.7, 0.8})
		require.NoError(t, err)

		results, err := idx.Search([]float32{0.1, 0.2, 0.3, 0.4}, 2, 100)
		require.NoError(t, err)
		assert.NotEmpty(t, results)
		assert.LessOrEqual(t, len(results), 2)
	})

	t.Run("SaveSnapshot and LoadSnapshot", func(t *testing.T) {
		config := hnsw.DefaultConfig()

		idx, err := defaultHNSWFactory(config)
		require.NoError(t, err)

		err = idx.Add(1, []float32{0.1, 0.2, 0.3, 0.4})
		require.NoError(t, err)

		var buf bytes.Buffer
		err = idx.SaveSnapshot(&buf)
		require.NoError(t, err)
		assert.Greater(t, buf.Len(), 0)

		idx2, err := defaultHNSWFactory(config)
		require.NoError(t, err)

		err = idx2.LoadSnapshot(&buf)
		require.NoError(t, err)
	})

	t.Run("Reset", func(t *testing.T) {
		config := hnsw.DefaultConfig()

		idx, err := defaultHNSWFactory(config)
		require.NoError(t, err)

		err = idx.Add(1, []float32{0.1, 0.2, 0.3, 0.4})
		require.NoError(t, err)
		assert.Equal(t, 1, idx.Size())

		err = idx.Reset()
		require.NoError(t, err)
		assert.Equal(t, 0, idx.Size())
	})
}
