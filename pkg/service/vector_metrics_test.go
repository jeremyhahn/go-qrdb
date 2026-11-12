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

package service

import (
	"context"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	pkgmetrics "github.com/jeremyhahn/go-qrdb/pkg/util/metrics"
)

// isolatedRegistry installs a fresh Prometheus registry for the duration of
// the calling test, then restores the previous one. This prevents metric
// registration conflicts across tests and ensures each test sees only the
// gauges it creates.
func isolatedRegistry(t *testing.T) *prometheus.Registry {
	t.Helper()
	reg := prometheus.NewRegistry()
	pkgmetrics.SetRegistry(reg)
	t.Cleanup(func() {
		pkgmetrics.SetRegistry(prometheus.NewRegistry())
	})
	return reg
}

// gatherGaugeVec gathers all metric families from reg and returns the
// MetricFamily for the given fully-qualified metric name, or nil if absent.
func gatherGaugeVec(t *testing.T, reg *prometheus.Registry, name string) *dto.MetricFamily {
	t.Helper()
	mfs, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() == name {
			return mf
		}
	}
	return nil
}

// labelValue returns the value of a named label from a dto.Metric, or "" if absent.
func labelValue(m *dto.Metric, name string) string {
	for _, lp := range m.GetLabel() {
		if lp.GetName() == name {
			return lp.GetValue()
		}
	}
	return ""
}

// containsControlBytes returns true when s contains any byte below 0x20.
func containsControlBytes(s string) bool {
	for _, b := range []byte(s) {
		if b < 0x20 {
			return true
		}
	}
	return false
}

// TestVectorMetrics_NoNamespaceNameInLabels verifies that a /metrics scrape
// after vector writes in named namespaces shows no qrdb_vector_* label named
// "namespace" and no label value carrying a tenant's namespace name
// (strict-tenant-isolation phase 10).
func TestVectorMetrics_NoNamespaceNameInLabels(t *testing.T) {
	reg := isolatedRegistry(t)

	svc, err := NewVectorService(newMockVectorStorage(), newMockLogger())
	require.NoError(t, err)

	ctx := tenant.WithTenantID(context.Background(), "acme")
	names := []string{"embeddings", "secret-project"}
	for i, name := range names {
		_, err = svc.CreateNamespace(ctx, &CreateNamespaceRequest{Namespace: name, Dimension: 2})
		require.NoError(t, err)
		_, err = svc.UpsertVector(ctx, &UpsertVectorRequest{
			Namespace: name,
			ID:        uint64(i + 1),
			Vector:    []float32{1.0, 2.0},
		})
		require.NoError(t, err)
	}

	mfs, err := reg.Gather()
	require.NoError(t, err)
	seen := 0
	for _, mf := range mfs {
		if !strings.HasPrefix(mf.GetName(), "qrdb_vector_") {
			continue
		}
		for _, m := range mf.GetMetric() {
			seen++
			for _, lp := range m.GetLabel() {
				assert.NotEqual(t, "namespace", lp.GetName(), "%s must not carry a namespace label", mf.GetName())
				for _, name := range names {
					assert.NotContains(t, lp.GetValue(), name,
						"%s label %s leaks the tenant's namespace name", mf.GetName(), lp.GetName())
				}
			}
		}
	}
	require.NotZero(t, seen, "qrdb_vector_* series must be present")
}

// TestVectorCountMetric_AggregatesPerTenant verifies that the vector count of
// every namespace a tenant owns lands in one series labelled only by tenant ID.
func TestVectorCountMetric_AggregatesPerTenant(t *testing.T) {
	reg := isolatedRegistry(t)

	svc, err := NewVectorService(newMockVectorStorage(), newMockLogger())
	require.NoError(t, err)

	ctx := tenant.WithTenantID(context.Background(), "tenant-x")
	for i, name := range []string{"vecs-a", "vecs-b"} {
		_, err = svc.CreateNamespace(ctx, &CreateNamespaceRequest{Namespace: name, Dimension: 2})
		require.NoError(t, err)
		_, err = svc.UpsertVector(ctx, &UpsertVectorRequest{
			Namespace: name,
			ID:        uint64(i + 1),
			Vector:    []float32{1.0, 2.0},
		})
		require.NoError(t, err)
	}

	mf := gatherGaugeVec(t, reg, "qrdb_vector_vector_count")
	require.NotNil(t, mf)
	require.Len(t, mf.GetMetric(), 1, "one series per tenant, not per namespace")

	m := mf.GetMetric()[0]
	require.Len(t, m.GetLabel(), 1)
	assert.Equal(t, "tenant-x", labelValue(m, "tenant"))
	assert.False(t, containsControlBytes(labelValue(m, "tenant")))
	assert.Equal(t, float64(2), m.GetGauge().GetValue(), "the tenant total sums its namespaces")
}

// indexSizeOf returns the IndexSizeBytes GetIndexStats reports for a
// namespace of the tenant in ctx: the same estimate the gauge carries.
func indexSizeOf(t *testing.T, svc VectorService, ctx context.Context, name string) float64 {
	t.Helper()
	stats, err := svc.GetIndexStats(ctx, &GetIndexStatsRequest{Namespace: name})
	require.NoError(t, err)
	return float64(stats.IndexSizeBytes)
}

// tenantGauge returns the value of the single series of a tenant-labelled
// gauge, failing unless exactly one series exists.
func tenantGauge(t *testing.T, reg *prometheus.Registry, name, tenantID string) float64 {
	t.Helper()
	mf := gatherGaugeVec(t, reg, name)
	require.NotNil(t, mf, "%s has no series", name)
	require.Len(t, mf.GetMetric(), 1, "one series per tenant, not per namespace")
	m := mf.GetMetric()[0]
	require.Len(t, m.GetLabel(), 1)
	require.Equal(t, tenantID, labelValue(m, "tenant"))
	return m.GetGauge().GetValue()
}

func TestIndexSizeBytesMetric_SumsTenantNamespaces(t *testing.T) {
	reg := isolatedRegistry(t)

	svc, err := NewVectorService(newMockVectorStorage(), newMockLogger())
	require.NoError(t, err)

	ctx := tenant.WithTenantID(context.Background(), "tenant-x")
	names := []string{"size-a", "size-b"}
	for i, name := range names {
		_, err = svc.CreateNamespace(ctx, &CreateNamespaceRequest{Namespace: name, Dimension: 4})
		require.NoError(t, err)
		for j := 0; j <= i+1; j++ {
			_, err = svc.UpsertVector(ctx, &UpsertVectorRequest{
				Namespace: name,
				ID:        uint64(j + 1),
				Vector:    []float32{float32(j), 1, 2, 3},
			})
			require.NoError(t, err)
		}
	}
	_, err = svc.BatchUpsert(ctx, &BatchUpsertRequest{
		Namespace: "size-b",
		Vectors: []*VectorData{
			{ID: 10, Vector: []float32{4, 5, 6, 7}},
			{ID: 11, Vector: []float32{5, 6, 7, 8}},
		},
	})
	require.NoError(t, err)
	_, err = svc.DeleteVector(ctx, &DeleteVectorRequest{Namespace: "size-a", ID: 1})
	require.NoError(t, err)

	sizeA := indexSizeOf(t, svc, ctx, "size-a")
	sizeB := indexSizeOf(t, svc, ctx, "size-b")
	require.Positive(t, sizeA)
	require.Positive(t, sizeB)
	assert.Equal(t, sizeA+sizeB,
		tenantGauge(t, reg, "qrdb_vector_index_size_bytes", "tenant-x"),
		"the tenant series sums both namespaces' index estimates")

	_, err = svc.DeleteNamespace(ctx, &DeleteNamespaceRequest{Namespace: "size-a"})
	require.NoError(t, err)
	assert.Equal(t, sizeB,
		tenantGauge(t, reg, "qrdb_vector_index_size_bytes", "tenant-x"),
		"deleting a namespace drops its share")
}

// TestVectorCountMetric_NamespaceDeleteSubtractsItsVectors checks that
// deleting a namespace holding N vectors lowers its tenant's vector_count
// by exactly N, leaving the other namespace's share, both when storage
// reports the live count and when its stored record count is stale (a
// count the storage does not maintain): the gauge subtracts the share it
// tracked for the namespace, not the storage record.
func TestVectorCountMetric_NamespaceDeleteSubtractsItsVectors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		staleInfo bool
	}{
		{name: "live stored count"},
		{name: "stale stored count", staleInfo: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := isolatedRegistry(t)

			storage := newMockVectorStorage()
			if tc.staleInfo {
				storage.getNamespaceInfoFn = func(_ context.Context, namespace string) (*vector.NamespaceInfo, error) {
					storage.mu.RLock()
					defer storage.mu.RUnlock()
					data, ok := storage.namespaces[namespace]
					if !ok {
						return nil, vector.ErrNamespaceNotFound
					}
					info := *data.info
					info.VectorCount = 0
					return &info, nil
				}
			}
			svc, err := NewVectorService(storage, newMockLogger())
			require.NoError(t, err)

			ctx := tenant.WithTenantID(context.Background(), "tenant-d")
			counts := map[string]int{"del-a": 3, "del-b": 2}
			for _, name := range []string{"del-a", "del-b"} {
				_, err = svc.CreateNamespace(ctx, &CreateNamespaceRequest{Namespace: name, Dimension: 2})
				require.NoError(t, err)
				for j := 0; j < counts[name]; j++ {
					_, err = svc.UpsertVector(ctx, &UpsertVectorRequest{
						Namespace: name,
						ID:        uint64(j + 1),
						Vector:    []float32{float32(j), 1},
					})
					require.NoError(t, err)
				}
			}
			require.Equal(t, float64(5), tenantGauge(t, reg, "qrdb_vector_vector_count", "tenant-d"))

			_, err = svc.DeleteNamespace(ctx, &DeleteNamespaceRequest{Namespace: "del-a"})
			require.NoError(t, err)
			assert.Equal(t, float64(2), tenantGauge(t, reg, "qrdb_vector_vector_count", "tenant-d"),
				"deleting a namespace of 3 vectors lowers the tenant total by exactly 3")
		})
	}
}

// TestVectorCountMetric_CountsStoredVectorsExactly: the tenant's
// vector_count series equals the number of vectors stored, across a
// re-upsert of a stored id, a batch holding a stored id and a duplicate id,
// and deletes of a stored and a missing id.
func TestVectorCountMetric_CountsStoredVectorsExactly(t *testing.T) {
	reg := isolatedRegistry(t)
	storage := newMockVectorStorage()
	svc, err := NewVectorService(storage, newMockLogger())
	require.NoError(t, err)

	ctx := tenant.WithTenantID(context.Background(), "tenant-r")
	_, err = svc.CreateNamespace(ctx, &CreateNamespaceRequest{Namespace: "reup", Dimension: 2})
	require.NoError(t, err)

	stored := func() float64 {
		t.Helper()
		storage.mu.RLock()
		defer storage.mu.RUnlock()
		return float64(len(storage.vectors[scopedFromContext(ctx, "reup")]))
	}
	gauge := func() float64 {
		t.Helper()
		return tenantGauge(t, reg, "qrdb_vector_vector_count", "tenant-r")
	}

	for i := 0; i < 2; i++ {
		_, err = svc.UpsertVector(ctx, &UpsertVectorRequest{Namespace: "reup", ID: 1, Vector: []float32{1, float32(i)}})
		require.NoError(t, err)
	}
	require.Equal(t, float64(1), stored())
	assert.Equal(t, stored(), gauge(), "a re-upsert of a stored id does not count again")

	_, err = svc.BatchUpsert(ctx, &BatchUpsertRequest{Namespace: "reup", Vectors: []*VectorData{
		{ID: 1, Vector: []float32{1, 2}},
		{ID: 2, Vector: []float32{2, 2}},
		{ID: 2, Vector: []float32{2, 3}},
		{ID: 3, Vector: []float32{3, 3}},
	}})
	require.NoError(t, err)
	require.Equal(t, float64(3), stored())
	assert.Equal(t, stored(), gauge(), "a batch counts only ids not stored before, each once")

	_, err = svc.DeleteVector(ctx, &DeleteVectorRequest{Namespace: "reup", ID: 2})
	require.NoError(t, err)
	_, _ = svc.DeleteVector(ctx, &DeleteVectorRequest{Namespace: "reup", ID: 99})
	require.Equal(t, float64(2), stored())
	assert.Equal(t, stored(), gauge(), "a delete subtracts only an id that was stored")

	_, err = svc.BatchDeleteVectors(ctx, &BatchDeleteVectorsRequest{Namespace: "reup", IDs: []uint64{1, 1, 77}})
	require.NoError(t, err)
	require.Equal(t, float64(1), stored())
	assert.Equal(t, stored(), gauge(), "a batch delete subtracts each stored id once")
}

// TestVectorCountMetric_SystemTenantLabel verifies that namespaces created
// without an explicit tenant (pooling into system) carry tenant="system" in
// the gauge label — not an empty string or \x1f.
func TestVectorCountMetric_SystemTenantLabel(t *testing.T) {
	reg := isolatedRegistry(t)

	storage := newMockVectorStorage()
	logger := newMockLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	// context.Background() has no tenant — resolves to system.
	_, err = svc.CreateNamespace(context.Background(), &CreateNamespaceRequest{
		Namespace: "global",
		Dimension: 3,
	})
	require.NoError(t, err)

	mf := gatherGaugeVec(t, reg, "qrdb_vector_vector_count")
	require.NotNil(t, mf)
	require.NotEmpty(t, mf.GetMetric())

	m := mf.GetMetric()[0]
	ten := labelValue(m, "tenant")

	assert.Empty(t, labelValue(m, "namespace"), "no namespace label")
	assert.Equal(t, tenantid.System, ten, "system tenant must appear as its canonical ID, not empty string")
	assert.False(t, containsControlBytes(ten))
}

// TestTenantMetricLabels_TenantOnly verifies that tenantMetricLabels returns
// only the control-byte-free tenant ID, never the namespace name.
func TestTenantMetricLabels_TenantOnly(t *testing.T) {
	cases := []struct {
		physical   string
		wantTenant string
	}{
		{"alice\x1fembeddings", "alice"},
		{"\x1fglobal", tenantid.System},
		{"tenant-b\x1fmy-vecs", "tenant-b"},
		// Legacy unscoped name (no separator) falls through to system.
		{"legacy-ns", tenantid.System},
	}

	for _, tc := range cases {
		labels := tenantMetricLabels(tc.physical)
		assert.Len(t, labels, 1, "physical=%q", tc.physical)
		assert.Equal(t, tc.wantTenant, labels["tenant"], "physical=%q", tc.physical)
		assert.False(t, containsControlBytes(labels["tenant"]), "tenant must be control-byte-free for physical=%q", tc.physical)
	}
}
