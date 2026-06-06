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

	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	pkgmetrics "github.com/jeremyhahn/go-qrdb/pkg/util/metrics"

	"github.com/prometheus/client_golang/prometheus"
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

// TestVectorCountMetric_LabelsAreLogicalAndTenantOnCreate verifies that after
// CreateNamespace the qrdb_vector_vector_count gauge is emitted with
// {namespace=<logical>, tenant=<tenantID>} and contains no control bytes.
func TestVectorCountMetric_LabelsAreLogicalAndTenantOnCreate(t *testing.T) {
	reg := isolatedRegistry(t)

	storage := newMockVectorStorage()
	logger := newMockLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	ctx := tenant.WithTenantID(context.Background(), "acme")
	_, err = svc.CreateNamespace(ctx, &CreateNamespaceRequest{
		Namespace: "embeddings",
		Dimension: 4,
	})
	require.NoError(t, err)

	mf := gatherGaugeVec(t, reg, "qrdb_vector_vector_count")
	require.NotNil(t, mf, "qrdb_vector_vector_count metric family must be present")
	require.NotEmpty(t, mf.GetMetric())

	m := mf.GetMetric()[0]
	ns := labelValue(m, "namespace")
	ten := labelValue(m, "tenant")

	assert.Equal(t, "embeddings", ns, "namespace label must be the logical name")
	assert.Equal(t, "acme", ten, "tenant label must be the tenant ID")
	assert.False(t, containsControlBytes(ns), "namespace label must not contain control bytes")
	assert.False(t, containsControlBytes(ten), "tenant label must not contain control bytes")
	assert.False(t, strings.ContainsRune(ns, '\x1f'), "namespace label must not contain the scoping separator")
}

// TestVectorCountMetric_LabelsAreLogicalAndTenantOnUpsert verifies that after
// UpsertVector the counter is incremented and carries the correct two-label set.
func TestVectorCountMetric_LabelsAreLogicalAndTenantOnUpsert(t *testing.T) {
	reg := isolatedRegistry(t)

	storage := newMockVectorStorage()
	logger := newMockLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	ctx := tenant.WithTenantID(context.Background(), "tenant-x")
	_, err = svc.CreateNamespace(ctx, &CreateNamespaceRequest{
		Namespace: "vecs",
		Dimension: 2,
	})
	require.NoError(t, err)

	_, err = svc.UpsertVector(ctx, &UpsertVectorRequest{
		Namespace: "vecs",
		ID:        42,
		Vector:    []float32{1.0, 2.0},
	})
	require.NoError(t, err)

	mf := gatherGaugeVec(t, reg, "qrdb_vector_vector_count")
	require.NotNil(t, mf)

	found := false
	for _, m := range mf.GetMetric() {
		ns := labelValue(m, "namespace")
		ten := labelValue(m, "tenant")
		if ns == "vecs" && ten == "tenant-x" {
			found = true
			assert.False(t, containsControlBytes(ns), "namespace label must not contain control bytes")
			assert.False(t, containsControlBytes(ten), "tenant label must not contain control bytes")
		}
	}
	assert.True(t, found, "expected metric with {namespace=vecs, tenant=tenant-x}")
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
	ns := labelValue(m, "namespace")
	ten := labelValue(m, "tenant")

	assert.Equal(t, "global", ns)
	assert.Equal(t, tenant.SystemTenantID, ten, "system tenant must appear as its canonical ID, not empty string")
	assert.False(t, containsControlBytes(ns))
	assert.False(t, containsControlBytes(ten))
}

// TestNamespaceMetricLabels_NoControlBytes verifies that namespaceMetricLabels
// never returns label values containing control characters regardless of input.
func TestNamespaceMetricLabels_NoControlBytes(t *testing.T) {
	cases := []struct {
		physical      string
		wantNamespace string
		wantTenant    string
	}{
		{"alice\x1fembeddings", "embeddings", "alice"},
		{"\x1fglobal", "global", tenant.SystemTenantID},
		{"tenant-b\x1fmy-vecs", "my-vecs", "tenant-b"},
		// Legacy unscoped name (no separator) falls through to system.
		{"legacy-ns", "legacy-ns", tenant.SystemTenantID},
	}

	for _, tc := range cases {
		labels := namespaceMetricLabels(tc.physical)
		ns := labels["namespace"]
		ten := labels["tenant"]

		assert.Equal(t, tc.wantNamespace, ns, "physical=%q", tc.physical)
		assert.Equal(t, tc.wantTenant, ten, "physical=%q", tc.physical)
		assert.False(t, containsControlBytes(ns), "namespace must be control-byte-free for physical=%q", tc.physical)
		assert.False(t, containsControlBytes(ten), "tenant must be control-byte-free for physical=%q", tc.physical)
	}
}
