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

func TestParseDistanceMetric_AcceptsEveryImplementedMetric(t *testing.T) {
	cases := map[string]hnsw.DistanceMetric{
		"":            hnsw.L2Distance,
		"L2":          hnsw.L2Distance,
		"l2":          hnsw.L2Distance,
		"EUCLIDEAN":   hnsw.L2Distance,
		"COSINE":      hnsw.CosineDistance,
		"cosine":      hnsw.CosineDistance,
		"DOT_PRODUCT": hnsw.DotProductDistance,
		"MANHATTAN":   hnsw.ManhattanDistance,
		"Manhattan":   hnsw.ManhattanDistance,
	}
	for name, want := range cases {
		got, err := parseDistanceMetric(name)
		require.NoError(t, err, "metric %q", name)
		assert.Equal(t, want, got, "metric %q", name)
	}
}

func TestParseDistanceMetric_RefusesUnknownMetric(t *testing.T) {
	for _, name := range []string{"HAMMING", "UNKNOWN_METRIC", " L2", "DOT"} {
		_, err := parseDistanceMetric(name)
		var metricErr *UnknownDistanceMetricError
		require.ErrorAs(t, err, &metricErr, "metric %q", name)
		assert.Equal(t, name, metricErr.Metric)
	}
}

func TestUnknownDistanceMetricError_NamesTheMetricAndTheChoices(t *testing.T) {
	msg := (&UnknownDistanceMetricError{Metric: "HAMMING"}).Error()
	assert.Contains(t, msg, `"HAMMING"`)
	assert.Contains(t, msg, "MANHATTAN")
}

func TestUnknownDistanceMetricError_QuotesControlCharacters(t *testing.T) {
	msg := (&UnknownDistanceMetricError{Metric: "a\nb"}).Error()
	assert.NotContains(t, msg, "\n")
	assert.Contains(t, msg, `"a\nb"`)
}

func TestVecCreateNamespace_UnknownMetricRefusedBeforeStorage(t *testing.T) {
	svc, storage := newBasicVecSvc(t)
	stored := false
	storage.createNamespaceFn = func(ctx context.Context, info *vector.NamespaceInfo) error {
		stored = true
		return nil
	}

	resp, err := svc.CreateNamespace(context.Background(), &CreateNamespaceRequest{
		Namespace:      "hamming-ns",
		Dimension:      4,
		DistanceMetric: "HAMMING",
	})

	require.Error(t, err)
	assert.Nil(t, resp)
	requireErrorAs[*UnknownDistanceMetricError](t, err)
	assert.False(t, stored, "a refused namespace must not reach storage")
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

func TestValidateCreateNamespaceRequest_Metric(t *testing.T) {
	vs := &vectorService{}
	require.NoError(t, vs.validateCreateNamespaceRequest(&CreateNamespaceRequest{
		Namespace: "ns", Dimension: 4, DistanceMetric: "cosine",
	}))
	err := vs.validateCreateNamespaceRequest(&CreateNamespaceRequest{
		Namespace: "ns", Dimension: 4, DistanceMetric: "JACCARD",
	})
	requireErrorAs[*UnknownDistanceMetricError](t, err)
}

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

func TestNewNamespaceIndex_RefusesUnknownMetric(t *testing.T) {
	svc, _ := newBasicVecSvc(t)
	vs := svc.(*vectorService)

	idx, err := vs.newNamespaceIndex(&vector.NamespaceInfo{Name: "ns", Dimension: 2, DistanceMetric: "HAMMING"}, nil)
	assert.Nil(t, idx)
	requireErrorAs[*UnknownDistanceMetricError](t, err)
}

func TestApplyHNSWParams_ParsesKnownParameters(t *testing.T) {
	svc, _ := newBasicVecSvc(t)
	vs := svc.(*vectorService)
	config := &hnsw.Config{M: 16, MMax: 32, EfConstruction: 200, EfSearch: 50}

	vs.applyHNSWParams(config, map[string]string{"m": "8", "ef_construction": "100", "ef_search": "20", "other": "x"})

	assert.Equal(t, 8, config.M)
	assert.Equal(t, 16, config.MMax)
	assert.Equal(t, 100, config.EfConstruction)
	assert.Equal(t, 20, config.EfSearch)
}

func TestApplyHNSWParams_KeepsDefaultOnUnparsableValue(t *testing.T) {
	svc, _ := newBasicVecSvc(t)
	vs := svc.(*vectorService)
	config := &hnsw.Config{M: 16, MMax: 32, EfConstruction: 200, EfSearch: 50}

	vs.applyHNSWParams(config, map[string]string{"m": "many", "ef_search": "?"})

	assert.Equal(t, 16, config.M)
	assert.Equal(t, 32, config.MMax)
	assert.Equal(t, 50, config.EfSearch)
}
