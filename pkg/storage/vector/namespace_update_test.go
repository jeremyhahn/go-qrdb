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

package vector

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// namespaceUpdateStores builds each Storage implementation fresh, so every
// UpdateNamespace contract test runs against both.
func namespaceUpdateStores(t *testing.T) map[string]Storage {
	t.Helper()
	pebbleStore, cleanup := setupStorage(t)
	t.Cleanup(cleanup)
	return map[string]Storage{
		"memory": NewMemoryVectorStorage(),
		"pebble": pebbleStore,
	}
}

func createUpdateNamespace(t *testing.T, s Storage) {
	t.Helper()
	require.NoError(t, s.CreateNamespace(context.Background(), &NamespaceInfo{
		Name:           "docs",
		Dimension:      3,
		CreatedAt:      10,
		UpdatedAt:      10,
		DistanceMetric: "cosine",
		IndexType:      "hnsw",
		IndexConfig:    map[string]string{"m": "16"},
		Metadata:       map[string]string{"owner": "a"},
	}))
	require.NoError(t, s.UpsertVector(context.Background(), "docs", &VectorValue{
		ID: 1, Embedding: []float32{1, 2, 3}, Metadata: map[string]string{"k": "v"},
	}))
}

func TestUpdateNamespace_replacesMutableFieldsOnly(t *testing.T) {
	for name, s := range namespaceUpdateStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			createUpdateNamespace(t, s)

			update := &NamespaceUpdate{
				IndexConfig: map[string]string{"m": "32", "ef": "200"},
				Metadata:    map[string]string{"team": "b"},
				UpdatedAt:   99,
			}
			require.NoError(t, s.UpdateNamespace(ctx, "docs", update))

			// The stored maps are copies: the caller's later writes do not leak in.
			update.IndexConfig["m"] = "64"
			update.Metadata["team"] = "c"

			info, err := s.GetNamespaceInfo(ctx, "docs")
			require.NoError(t, err)
			require.Equal(t, map[string]string{"m": "32", "ef": "200"}, info.IndexConfig)
			require.Equal(t, map[string]string{"team": "b"}, info.Metadata)
			require.Equal(t, int64(99), info.UpdatedAt)
			require.Equal(t, uint32(3), info.Dimension)
			require.Equal(t, int64(10), info.CreatedAt)
			require.Equal(t, "cosine", info.DistanceMetric)
			require.Equal(t, "hnsw", info.IndexType)

			vec, err := s.GetVector(ctx, "docs", 1)
			require.NoError(t, err)
			require.Equal(t, []float32{1, 2, 3}, vec.Embedding)
		})
	}
}

func TestUpdateNamespace_emptyMapsClearFields(t *testing.T) {
	for name, s := range namespaceUpdateStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			createUpdateNamespace(t, s)

			require.NoError(t, s.UpdateNamespace(ctx, "docs", &NamespaceUpdate{UpdatedAt: 20}))

			info, err := s.GetNamespaceInfo(ctx, "docs")
			require.NoError(t, err)
			require.Empty(t, info.IndexConfig)
			require.Empty(t, info.Metadata)
			require.Equal(t, int64(20), info.UpdatedAt)
		})
	}
}

func TestUpdateNamespace_refusals(t *testing.T) {
	tests := []struct {
		name      string
		namespace string
		update    *NamespaceUpdate
		want      error
	}{
		{name: "empty namespace", namespace: "", update: &NamespaceUpdate{}, want: ErrInvalidNamespace},
		{name: "nil update", namespace: "docs", update: nil, want: ErrInvalidNamespace},
		{name: "unknown namespace", namespace: "missing", update: &NamespaceUpdate{}, want: ErrNamespaceNotFound},
	}
	for name, s := range namespaceUpdateStores(t) {
		t.Run(name, func(t *testing.T) {
			createUpdateNamespace(t, s)
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					err := s.UpdateNamespace(context.Background(), tt.namespace, tt.update)
					require.ErrorIs(t, err, tt.want)
				})
			}
			// A refused update leaves the namespace as it was.
			info, err := s.GetNamespaceInfo(context.Background(), "docs")
			require.NoError(t, err)
			require.Equal(t, map[string]string{"m": "16"}, info.IndexConfig)
			require.Equal(t, int64(10), info.UpdatedAt)
		})
	}
}

func TestUpdateNamespace_closedStore(t *testing.T) {
	for name, s := range namespaceUpdateStores(t) {
		t.Run(name, func(t *testing.T) {
			createUpdateNamespace(t, s)
			require.NoError(t, s.Close())
			err := s.UpdateNamespace(context.Background(), "docs", &NamespaceUpdate{})
			require.ErrorIs(t, err, ErrStorageClosed)
		})
	}
}

func TestPebbleVectorStorage_UpdateNamespaceSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	s, err := NewPebbleVectorStorage(DefaultConfig(dir), &testLogger{})
	require.NoError(t, err)
	createUpdateNamespace(t, s)
	require.NoError(t, s.UpdateNamespace(ctx, "docs", &NamespaceUpdate{
		IndexConfig: map[string]string{"ef": "400"},
		Metadata:    map[string]string{"tier": "gold"},
		UpdatedAt:   55,
	}))
	require.NoError(t, s.Close())

	reopened, err := NewPebbleVectorStorage(DefaultConfig(dir), &testLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })

	info, err := reopened.GetNamespaceInfo(ctx, "docs")
	require.NoError(t, err)
	require.Equal(t, map[string]string{"ef": "400"}, info.IndexConfig)
	require.Equal(t, map[string]string{"tier": "gold"}, info.Metadata)
	require.Equal(t, int64(55), info.UpdatedAt)
	require.Equal(t, uint32(3), info.Dimension)

	vec, err := reopened.GetVector(ctx, "docs", 1)
	require.NoError(t, err)
	require.Equal(t, []float32{1, 2, 3}, vec.Embedding)
}

func TestPebbleVectorStorage_UpdateNamespaceKeepsReaderCopy(t *testing.T) {
	s, cleanup := setupStorage(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	createUpdateNamespace(t, s)

	before, err := s.GetNamespaceInfo(ctx, "docs")
	require.NoError(t, err)
	require.NoError(t, s.UpdateNamespace(ctx, "docs", &NamespaceUpdate{
		IndexConfig: map[string]string{"m": "48"},
		UpdatedAt:   77,
	}))
	require.Equal(t, map[string]string{"m": "16"}, before.IndexConfig)
	require.Equal(t, int64(10), before.UpdatedAt)
}

func TestMergeNamespaceUpdate(t *testing.T) {
	stored := &NamespaceInfo{
		IndexConfig: map[string]string{"m": "16", "ef": "100"},
		Metadata:    map[string]string{"owner": "a", "tier": "silver"},
	}
	tests := []struct {
		name        string
		info        *NamespaceInfo
		indexConfig map[string]string
		metadata    map[string]string
		wantIndex   map[string]string
		wantMeta    map[string]string
		wantRebuild bool
	}{
		{
			name:      "no change",
			info:      stored,
			wantIndex: map[string]string{"m": "16", "ef": "100"},
			wantMeta:  map[string]string{"owner": "a", "tier": "silver"},
		},
		{
			name:        "same value is not a rebuild",
			info:        stored,
			indexConfig: map[string]string{"m": "16"},
			metadata:    map[string]string{"tier": "gold"},
			wantIndex:   map[string]string{"m": "16", "ef": "100"},
			wantMeta:    map[string]string{"owner": "a", "tier": "gold"},
		},
		{
			name:        "changed parameter",
			info:        stored,
			indexConfig: map[string]string{"ef": "200"},
			wantIndex:   map[string]string{"m": "16", "ef": "200"},
			wantMeta:    map[string]string{"owner": "a", "tier": "silver"},
			wantRebuild: true,
		},
		{
			name:        "added parameter",
			info:        stored,
			indexConfig: map[string]string{"metric_k": "8"},
			wantIndex:   map[string]string{"m": "16", "ef": "100", "metric_k": "8"},
			wantMeta:    map[string]string{"owner": "a", "tier": "silver"},
			wantRebuild: true,
		},
		{
			name:        "empty stored maps",
			info:        &NamespaceInfo{},
			indexConfig: map[string]string{"m": "8"},
			metadata:    map[string]string{"owner": "b"},
			wantIndex:   map[string]string{"m": "8"},
			wantMeta:    map[string]string{"owner": "b"},
			wantRebuild: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			update, rebuild := MergeNamespaceUpdate(tt.info, tt.indexConfig, tt.metadata, 42)
			require.Equal(t, tt.wantRebuild, rebuild)
			require.Equal(t, tt.wantIndex, update.IndexConfig)
			require.Equal(t, tt.wantMeta, update.Metadata)
			require.Equal(t, int64(42), update.UpdatedAt)
		})
	}
}

func TestMergeNamespaceUpdate_leavesInputsUntouched(t *testing.T) {
	info := &NamespaceInfo{
		IndexConfig: map[string]string{"m": "16"},
		Metadata:    map[string]string{"owner": "a"},
	}
	update, _ := MergeNamespaceUpdate(info, map[string]string{"m": "32"}, map[string]string{"owner": "b"}, 1)
	update.IndexConfig["x"] = "y"
	update.Metadata["x"] = "y"

	require.Equal(t, map[string]string{"m": "16"}, info.IndexConfig)
	require.Equal(t, map[string]string{"owner": "a"}, info.Metadata)
}
