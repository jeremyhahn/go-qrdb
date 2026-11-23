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

//go:build qrdb_vector

// vector_metric_index_test.go holds the pkg/service vector tests that build,
// fill and search a real namespace index. Without qrdb_vector,
// pkg/vector/hnsw compiles only its stubs, which refuse every index with
// "vector support not compiled", so these tests run only with the tag (as
// CI and the Makefile do).
package service

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/vector/hnsw"
)

func TestNewNamespaceIndex_BuildsForStoredMetric(t *testing.T) {
	svc, _ := newBasicVecSvc(t)
	vs := svc.(*vectorService)

	idx, err := vs.newNamespaceIndex(&vector.NamespaceInfo{Name: "ns", Dimension: 2, DistanceMetric: "MANHATTAN"}, nil)
	require.NoError(t, err)
	require.NoError(t, idx.Insert(1, []float32{0, 0}))
	require.NoError(t, idx.Insert(2, []float32{3, 4}))

	hits, err := idx.Search([]float32{0, 0}, 2)
	require.NoError(t, err)
	require.Len(t, hits, 2)
	assert.Equal(t, uint64(1), hits[0].ID)
	assert.InDelta(t, 7.0, hits[1].Distance, 1e-6, "Manhattan distance of (3,4) from the origin")
}

func TestVecCreateNamespace_ManhattanAccepted(t *testing.T) {
	svc, storage := newBasicVecSvc(t)
	var createdInfo *vector.NamespaceInfo
	storage.createNamespaceFn = func(ctx context.Context, info *vector.NamespaceInfo) error {
		createdInfo = info
		return nil
	}

	_, err := svc.CreateNamespace(context.Background(), &CreateNamespaceRequest{
		Namespace:      "manhattan-ns",
		Dimension:      4,
		DistanceMetric: "MANHATTAN",
	})

	require.NoError(t, err)
	require.NotNil(t, createdInfo)
	assert.Equal(t, "MANHATTAN", createdInfo.DistanceMetric)
}

func TestNewNamespaceIndex_FlatSearchesExactly(t *testing.T) {
	svc, _ := newBasicVecSvc(t)
	vs := svc.(*vectorService)

	idx, err := vs.newNamespaceIndex(&vector.NamespaceInfo{Name: "ns", Dimension: 2, DistanceMetric: "L2", IndexType: "flat"}, nil)
	require.NoError(t, err)
	require.IsType(t, &liveIndexAdapter{}, idx)

	_, err = idx.Search([]float32{0, 0}, 1)
	require.ErrorIs(t, err, hnsw.ErrEmptyIndex, "an empty FLAT index answers as an empty HNSW one")

	require.NoError(t, idx.Insert(1, []float32{0, 0}))
	require.NoError(t, idx.Insert(2, []float32{3, 4}))
	require.NoError(t, idx.Insert(2, []float32{6, 8}), "a second insert of an id replaces it")
	assert.Equal(t, int64(2), idx.Count())

	hits, err := idx.Search([]float32{6, 8}, 2)
	require.NoError(t, err)
	require.Len(t, hits, 2)
	assert.Equal(t, uint64(2), hits[0].ID)
	assert.Equal(t, uint64(1), hits[1].ID)

	require.NoError(t, idx.Delete(2))
	assert.Equal(t, int64(1), idx.Count())
	stats := idx.GetStats()
	assert.Equal(t, int64(1), stats.NodeCount)
	assert.Equal(t, 2, stats.Dimensions)
}

func TestNewNamespaceIndex_IVFRefusesSearchUntilTrained(t *testing.T) {
	svc, _ := newBasicVecSvc(t)
	vs := svc.(*vectorService)
	info := &vector.NamespaceInfo{
		Name:           scopedNamespace("acme", "docs"),
		Dimension:      2,
		DistanceMetric: "L2",
		IndexType:      "IVF_FLAT",
		IndexConfig:    map[string]string{"num_centroids": "2"},
	}

	idx, err := vs.newNamespaceIndex(info, nil)
	require.NoError(t, err)
	require.NoError(t, idx.Insert(1, []float32{0, 0}))

	_, err = idx.Search([]float32{0, 0}, 1)
	untrained := requireErrorAs[*VectorIndexUntrainedError](t, err)
	assert.Equal(t, "docs", untrained.Namespace, "the error names the logical namespace, not the tenant-scoped key")

	require.NoError(t, idx.Insert(2, []float32{10, 10}))
	require.NoError(t, idx.Insert(3, []float32{9, 9}))
	hits, err := idx.Search([]float32{10, 10}, 2)
	require.NoError(t, err)
	require.Len(t, hits, 2)
	assert.Equal(t, uint64(2), hits[0].ID)
	assert.Equal(t, uint64(3), hits[1].ID)
}

func TestNewNamespaceIndex_ManhattanOnLiveTypes(t *testing.T) {
	svc, _ := newBasicVecSvc(t)
	vs := svc.(*vectorService)

	// From the origin, (5,0) is nearer by L1 (5 < 6) and (3,3) by L2
	// (4.24 < 5): the first hit says which metric ranked them.
	for _, indexType := range []string{"FLAT", "IVF_FLAT"} {
		info := &vector.NamespaceInfo{
			Name:           "ns",
			Dimension:      2,
			DistanceMetric: "MANHATTAN",
			IndexType:      indexType,
			IndexConfig:    map[string]string{"num_centroids": "1"},
		}
		idx, err := vs.newNamespaceIndex(info, nil)
		require.NoError(t, err, indexType)
		require.NoError(t, idx.Insert(1, []float32{3, 3}))
		require.NoError(t, idx.Insert(2, []float32{5, 0}))

		hits, err := idx.Search([]float32{0, 0}, 2)
		require.NoError(t, err, indexType)
		require.Len(t, hits, 2, indexType)
		assert.Equal(t, uint64(2), hits[0].ID, "%s ranks by Manhattan distance", indexType)
		assert.InDelta(t, 5, hits[0].Distance, 1e-6, indexType)
	}
}

func TestVectorService_SupportedStoredGeometryStillSearches(t *testing.T) {
	dir := t.TempDir()
	seedStoredGeometry(t, dir, "legacy", "L2", "HNSW")
	svc := reopenVectorService(t, dir)

	resp, err := svc.SearchSimilar(context.Background(), &SearchSimilarRequest{Namespace: "legacy", QueryVector: []float32{1, 0}, K: 1})
	if err != nil {
		t.Fatalf("SearchSimilar: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].ID != 1 {
		t.Fatalf("SearchSimilar = %+v, want vector 1", resp.Results)
	}
}

func TestPerformIndexRebuild_PagesEveryVectorOnce(t *testing.T) {
	for name, ids := range map[string][]uint64{
		"pages":     listCursorIDs(),
		"maxUint64": {1, math.MaxUint64},
	} {
		t.Run(name, func(t *testing.T) {
			svc, _ := newListCursorService(t, "docs", ids)
			ns := scopedNamespace(tenantid.System, "docs")
			job := &RebuildJobState{JobID: "rebuild-" + name, Namespace: ns, InProgress: true, StartedAt: time.Now()}
			svc.jobMu.Lock()
			svc.rebuildJobs[job.JobID] = job
			svc.jobMu.Unlock()

			require.NoError(t, svc.performIndexRebuild(context.Background(), job.JobID, ns, &vector.NamespaceInfo{Name: ns, Dimension: 2}, nil))

			job.mu.RLock()
			defer job.mu.RUnlock()
			require.Equal(t, uint64(len(ids)), job.VectorsProcessed)
		})
	}
}

func TestVecSearchSimilar_FlatAndIVFNamespaces(t *testing.T) {
	for _, indexType := range []string{"flat", "ivf_flat"} {
		t.Run(indexType, func(t *testing.T) {
			svc, err := NewVectorService(vector.NewMemoryVectorStorage(), newVecTestLogger())
			require.NoError(t, err)
			createIndexedNamespace(t, svc, "ns", indexType, map[string]string{"num_centroids": "2"},
				[]float32{0, 0}, []float32{10, 10}, []float32{9, 9})

			resp, err := svc.SearchSimilar(context.Background(), &SearchSimilarRequest{
				Namespace: "ns", QueryVector: []float32{10, 10}, K: 1, ShardID: 1,
			})
			require.NoError(t, err)
			require.Len(t, resp.Results, 1)
			assert.Equal(t, uint64(2), resp.Results[0].ID)

			stats, err := svc.GetIndexStats(context.Background(), &GetIndexStatsRequest{Namespace: "ns", ShardID: 1})
			require.NoError(t, err)
			want, _ := parseIndexType(indexType)
			assert.Equal(t, want, stats.IndexType)
			assert.Equal(t, uint64(3), stats.VectorCount)
		})
	}
}

func TestVecBatchUpsert_IVFRefusesWrongDimensionBeforeWriting(t *testing.T) {
	ctx := context.Background()
	svc, err := NewVectorService(vector.NewMemoryVectorStorage(), newVecTestLogger())
	require.NoError(t, err)
	createIndexedNamespace(t, svc, "ns", "ivf_flat", map[string]string{"num_centroids": "2"}, []float32{0, 0})

	resp, err := svc.BatchUpsert(ctx, &BatchUpsertRequest{Namespace: "ns", Vectors: []*VectorData{
		{ID: 2, Vector: []float32{10, 10}},
		{ID: 3, Vector: []float32{1, 2, 3}},
	}})
	assert.Nil(t, resp)
	mismatch := requireErrorAs[*DimensionMismatchError](t, err)
	assert.Equal(t, 2, mismatch.Expected)
	assert.Equal(t, 3, mismatch.Actual)

	stats, err := svc.GetIndexStats(ctx, &GetIndexStatsRequest{Namespace: "ns", ShardID: 1})
	require.NoError(t, err)
	assert.Equal(t, uint64(1), stats.VectorCount, "no vector of a refused batch is written")

	_, err = svc.BatchUpsert(ctx, &BatchUpsertRequest{Namespace: "ns", Vectors: []*VectorData{
		{ID: 2, Vector: []float32{10, 10}},
		{ID: 3, Vector: []float32{9, 9}},
	}})
	require.NoError(t, err)

	found, err := svc.SearchSimilar(ctx, &SearchSimilarRequest{
		Namespace: "ns", QueryVector: []float32{10, 10}, K: 1, ShardID: 1,
	})
	require.NoError(t, err)
	require.Len(t, found.Results, 1)
	assert.Equal(t, uint64(2), found.Results[0].ID)
}
