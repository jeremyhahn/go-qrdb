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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
	"github.com/jeremyhahn/go-qrdb/pkg/vector/hnsw"
)

func TestParseIndexType_AcceptsEveryWireName(t *testing.T) {
	cases := map[string]string{
		"":         "HNSW",
		"HNSW":     "HNSW",
		"hnsw":     "HNSW",
		"FLAT":     "FLAT",
		"flat":     "FLAT",
		"IVF":      "IVF",
		"ivf_flat": "IVF",
		"IVF_FLAT": "IVF",
	}
	for name, want := range cases {
		got, err := parseIndexType(name)
		require.NoError(t, err, "index type %q", name)
		assert.Equal(t, want, got, "index type %q", name)
	}
}

func TestParseIndexType_RefusesUnknownType(t *testing.T) {
	for _, name := range []string{"LSH", "ivf-flat", " FLAT", "PQ"} {
		_, err := parseIndexType(name)
		typeErr := requireErrorAs[*UnknownIndexTypeError](t, err)
		assert.Equal(t, name, typeErr.IndexType)
	}
}

func TestReportedIndexType_CanonicalWhenKnown(t *testing.T) {
	assert.Equal(t, "HNSW", reportedIndexType(""))
	assert.Equal(t, "IVF", reportedIndexType("ivf_flat"))
	assert.Equal(t, "FLAT", reportedIndexType("flat"))
}

func TestReportedIndexType_StoredValueWhenUnknown(t *testing.T) {
	assert.Equal(t, "LSH", reportedIndexType("LSH"))
}

func TestReportedDistanceMetric_StoredName(t *testing.T) {
	for _, name := range []string{"COSINE", "dot_product", "MANHATTAN"} {
		assert.Equal(t, name, reportedDistanceMetric(name))
	}
}

func TestReportedDistanceMetric_L2WhenUnrecorded(t *testing.T) {
	assert.Equal(t, "L2", reportedDistanceMetric(""))
}

func TestGetIndexStats_ReportsTheNamespaceMetric(t *testing.T) {
	for _, tt := range []struct{ stored, indexType, want string }{
		{"DOT_PRODUCT", "FLAT", "DOT_PRODUCT"},
		{"cosine", "IVF", "cosine"},
		{"", "IVF", "L2"},
	} {
		svc, storage := newBasicVecSvc(t)
		storage.getNamespaceInfoFn = func(_ context.Context, ns string) (*vector.NamespaceInfo, error) {
			return &vector.NamespaceInfo{Name: ns, Dimension: 2, DistanceMetric: tt.stored, IndexType: tt.indexType}, nil
		}
		stats, err := svc.GetIndexStats(context.Background(), &GetIndexStatsRequest{Namespace: "ns"})
		require.NoError(t, err)
		assert.Equal(t, tt.want, stats.DistanceMetric, "stored %q", tt.stored)
		assert.Equal(t, tt.indexType, stats.IndexType, "stored %q", tt.stored)
	}
}

func TestParseNamespaceGeometry_ResolvesMetricAndType(t *testing.T) {
	metric, indexType, err := parseNamespaceGeometry("cosine", "ivf_flat")
	require.NoError(t, err)
	assert.Equal(t, hnsw.CosineDistance, metric)
	assert.Equal(t, "IVF", indexType)

	metric, indexType, err = parseNamespaceGeometry("MANHATTAN", "")
	require.NoError(t, err, "HNSW implements Manhattan")
	assert.Equal(t, hnsw.ManhattanDistance, metric)
	assert.Equal(t, "HNSW", indexType)
}

func TestParseNamespaceGeometry_RefusesUnknownNames(t *testing.T) {
	_, _, err := parseNamespaceGeometry("HAMMING", "FLAT")
	requireErrorAs[*UnknownDistanceMetricError](t, err)
	_, _, err = parseNamespaceGeometry("L2", "LSH")
	requireErrorAs[*UnknownIndexTypeError](t, err)
}

func TestParseNamespaceGeometry_ManhattanOnEveryIndexType(t *testing.T) {
	for _, indexType := range []string{"HNSW", "FLAT", "IVF", "IVF_FLAT"} {
		metric, _, err := parseNamespaceGeometry("manhattan", indexType)
		require.NoError(t, err, indexType)
		assert.Equal(t, hnsw.ManhattanDistance, metric, indexType)
	}
}

func TestVectorIndexErrors_NameTheirSubject(t *testing.T) {
	assert.Contains(t, (&UnknownIndexTypeError{IndexType: "LSH"}).Error(), `"LSH"`)
	assert.Contains(t, (&VectorIndexUntrainedError{Namespace: "docs"}).Error(), `"docs"`)
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

func TestNewLiveNamespaceIndex_CarriesNamespaceAndDimension(t *testing.T) {
	idx, err := newLiveNamespaceIndex(&vector.NamespaceInfo{Name: "ns", Dimension: 3}, liveIndexTypes["IVF"], hnsw.CosineDistance, nil)
	require.NoError(t, err)
	adapter := idx.(*liveIndexAdapter)
	assert.Equal(t, "ns", adapter.namespace)
	assert.Equal(t, uint32(3), adapter.dimension)
	assert.Equal(t, 3, idx.GetStats().Dimensions)
}

func TestValidateCreateNamespaceRequest_IndexType(t *testing.T) {
	vs := &vectorService{}
	require.NoError(t, vs.validateCreateNamespaceRequest(&CreateNamespaceRequest{
		Namespace: "ns", Dimension: 4, IndexType: "ivf_flat",
	}))
	err := vs.validateCreateNamespaceRequest(&CreateNamespaceRequest{
		Namespace: "ns", Dimension: 4, IndexType: "LSH",
	})
	requireErrorAs[*UnknownIndexTypeError](t, err)
	err = vs.validateCreateNamespaceRequest(&CreateNamespaceRequest{
		Namespace: "ns", Dimension: 4, IndexType: "FLAT", DistanceMetric: "MANHATTAN",
	})
	require.NoError(t, err, "FLAT implements Manhattan")
}

// createIndexedNamespace creates a namespace of the given index type through
// the service and upserts vectors, ids from 1, in order.
func createIndexedNamespace(t *testing.T, svc VectorService, name, indexType string, config map[string]string, vectors ...[]float32) {
	t.Helper()
	ctx := context.Background()
	_, err := svc.CreateNamespace(ctx, &CreateNamespaceRequest{
		Namespace: name, Dimension: 2, IndexType: indexType, IndexConfig: config, ShardID: 1,
	})
	require.NoError(t, err)
	for i, vec := range vectors {
		_, err = svc.UpsertVector(ctx, &UpsertVectorRequest{Namespace: name, ID: uint64(i + 1), Vector: vec, ShardID: 1})
		require.NoError(t, err)
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

func TestVecSearchSimilar_UntrainedIVFRefused(t *testing.T) {
	svc, err := NewVectorService(vector.NewMemoryVectorStorage(), newVecTestLogger())
	require.NoError(t, err)
	createIndexedNamespace(t, svc, "ns", "IVF", nil, []float32{1, 1})

	resp, err := svc.SearchSimilar(context.Background(), &SearchSimilarRequest{
		Namespace: "ns", QueryVector: []float32{1, 1}, K: 1, ShardID: 1,
	})
	assert.Nil(t, resp)
	untrained := requireErrorAs[*VectorIndexUntrainedError](t, err)
	assert.Equal(t, "ns", untrained.Namespace)
}

func TestVecCreateNamespace_UnknownIndexTypeRefusedBeforeStorage(t *testing.T) {
	svc, storage := newBasicVecSvc(t)
	stored := false
	storage.createNamespaceFn = func(ctx context.Context, info *vector.NamespaceInfo) error {
		stored = true
		return nil
	}

	resp, err := svc.CreateNamespace(context.Background(), &CreateNamespaceRequest{
		Namespace: "lsh-ns", Dimension: 4, IndexType: "LSH",
	})

	assert.Nil(t, resp)
	requireErrorAs[*UnknownIndexTypeError](t, err)
	assert.False(t, stored, "a refused namespace must not reach storage")
}
