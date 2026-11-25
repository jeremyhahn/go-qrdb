//go:build qrdb_vector

// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package service

// These tests create namespaces and store vectors, which needs the vector
// engine; without the qrdb_vector tag the engine stubs refuse with "vector
// support not compiled". The home fakes they use live in vector_home_test.go,
// which builds in every tag set.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

func exerciseVectorNamespace(t *testing.T, ctx context.Context, svc VectorService) {
	t.Helper()
	_, err := svc.CreateNamespace(ctx, &CreateNamespaceRequest{Namespace: "docs", Dimension: 2, DistanceMetric: "cosine"})
	require.NoError(t, err)
	_, err = svc.UpsertVector(ctx, &UpsertVectorRequest{Namespace: "docs", ID: 1, Vector: []float32{1, 0}})
	require.NoError(t, err)
	_, err = svc.UpsertVector(ctx, &UpsertVectorRequest{Namespace: "docs", ID: 2, Vector: []float32{0, 1}})
	require.NoError(t, err)

	got, err := svc.GetVector(ctx, &GetVectorRequest{Namespace: "docs", ID: 1})
	require.NoError(t, err)
	require.True(t, got.Found)

	res, err := svc.SearchSimilar(ctx, &SearchSimilarRequest{Namespace: "docs", QueryVector: []float32{1, 0}, K: 1})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)
	require.Equal(t, uint64(1), res.Results[0].ID)

	list, err := svc.ListNamespaces(ctx, &ListNamespacesRequest{})
	require.NoError(t, err)
	require.Len(t, list.Namespaces, 1)
	require.Equal(t, "docs", list.Namespaces[0].Namespace)
}

func TestVectorService_IsolatedTenantVectorsLiveInHomeStore(t *testing.T) {
	homes := newFakeVectorHomes()
	svc, nodeWide := newHomedVectorService(t, homes)
	ctx := tenant.WithTenantID(context.Background(), "acme")

	exerciseVectorNamespace(t, ctx, svc)

	home := homes.home("acme")
	require.NotNil(t, home, "acme's home store was resolved")
	keys := vectorHomeKeys(t, home)
	require.NotEmpty(t, keys)
	for _, k := range keys {
		require.True(t, strings.HasPrefix(k, tenantVectorHomePrefix), "key %q outside the vector keyspace", k)
	}
	nodeNS, err := nodeWide.ListNamespaces(context.Background())
	require.NoError(t, err)
	require.Empty(t, nodeNS, "the node-wide store holds no namespace of an isolated tenant")

	_, err = svc.DeleteVector(ctx, &DeleteVectorRequest{Namespace: "docs", ID: 2})
	require.NoError(t, err)
	_, err = svc.DeleteNamespace(ctx, &DeleteNamespaceRequest{Namespace: "docs"})
	require.NoError(t, err)
	require.Empty(t, vectorHomeKeys(t, home), "deleting the namespace leaves no vector key in the home store")
}

// TestVectorService_CachedSearchRunsTheHomeCheck: once a namespace's index is
// cached, a search for a tenant its home refuses is refused, as the first
// search would be.
func TestVectorService_CachedSearchRunsTheHomeCheck(t *testing.T) {
	homes := newFakeVectorHomes()
	svc, _ := newHomedVectorService(t, homes)
	ctx := tenant.WithTenantID(context.Background(), "acme")

	exerciseVectorNamespace(t, ctx, svc)

	homes.mu.Lock()
	homes.resolveErr = errors.New("home unavailable")
	homes.mu.Unlock()
	search := &SearchSimilarRequest{Namespace: "docs", QueryVector: []float32{1, 0}, K: 1}
	_, err := svc.SearchSimilar(ctx, search)
	var homeErr *TenantVectorHomeError
	require.ErrorAs(t, err, &homeErr, "a cached search for a refused tenant")
	_, err = svc.SearchByID(ctx, &SearchByIDRequest{Namespace: "docs", ID: 1, K: 1})
	require.ErrorAs(t, err, &homeErr, "a cached search by ID for a refused tenant")
}

func TestVectorService_PrefixTenantStaysNodeWide(t *testing.T) {
	homes := newFakeVectorHomes()
	svc, nodeWide := newHomedVectorService(t, homes)

	exerciseVectorNamespace(t, tenant.WithTenantID(context.Background(), "beta"), svc)
	exerciseVectorNamespace(t, tenant.WithTenantID(context.Background(), tenantid.System), svc)

	nodeNS, err := nodeWide.ListNamespaces(context.Background())
	require.NoError(t, err)
	names := map[string]bool{}
	for _, ns := range nodeNS {
		names[ns.Name] = true
	}
	require.True(t, names[scopedNamespace("beta", "docs")], "beta keeps its prefix-scoped namespace node-wide")
	require.True(t, names[scopedNamespace(tenantid.System, "docs")], "the system tenant stays node-wide")
	require.Nil(t, homes.home("beta"), "a prefix tenant's home is never resolved")
}

func TestKeyspaceReclaim_IsolatedTenantDropLeavesNoHomeVectorKey(t *testing.T) {
	homes := newFakeVectorHomes()
	vecSvc, nodeWide := newHomedVectorService(t, homes)
	ctx := tenant.WithTenantID(context.Background(), "acme")
	dropped := tenant.ComposeNamespace("acme", "reports")
	kept := tenant.ComposeNamespace("acme", "ledger") + "docs"
	for _, name := range []string{dropped + "docs", dropped + "images", kept} {
		_, err := vecSvc.CreateNamespace(ctx, &CreateNamespaceRequest{Namespace: name, Dimension: 2, DistanceMetric: "cosine"})
		require.NoError(t, err)
		_, err = vecSvc.UpsertVector(ctx, &UpsertVectorRequest{Namespace: name, ID: 1, Vector: []float32{1, 0}})
		require.NoError(t, err)
	}
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: newMemoryKVSvc(), Vector: vecSvc})

	require.NoError(t, fenceThenReclaim(ctx, svc, "acme", "reports", ""))

	for _, k := range vectorHomeKeys(t, homes.home("acme")) {
		assert.NotContains(t, k, dropped, "the dropped database left vector key %q in the home store", k)
	}
	list, err := vecSvc.ListNamespaces(ctx, &ListNamespacesRequest{})
	require.NoError(t, err)
	require.Len(t, list.Namespaces, 1)
	assert.Equal(t, kept, list.Namespaces[0].Namespace)
	nodeNS, err := nodeWide.ListNamespaces(context.Background())
	require.NoError(t, err)
	assert.Empty(t, nodeNS, "the node-wide store never held the tenant's vectors")
}
