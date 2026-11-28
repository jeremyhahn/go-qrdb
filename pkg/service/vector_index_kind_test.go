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
		Namespace: name, Dimension: 2, IndexType: indexType, IndexConfig: config,
	})
	require.NoError(t, err)
	for i, vec := range vectors {
		_, err = svc.UpsertVector(ctx, &UpsertVectorRequest{Namespace: name, ID: uint64(i + 1), Vector: vec})
		require.NoError(t, err)
	}
}

func TestVecSearchSimilar_UntrainedIVFRefused(t *testing.T) {
	svc, err := NewVectorService(vector.NewMemoryVectorStorage(), newVecTestLogger())
	require.NoError(t, err)
	createIndexedNamespace(t, svc, "ns", "IVF", nil, []float32{1, 1})

	resp, err := svc.SearchSimilar(context.Background(), &SearchSimilarRequest{
		Namespace: "ns", QueryVector: []float32{1, 1}, K: 1,
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
