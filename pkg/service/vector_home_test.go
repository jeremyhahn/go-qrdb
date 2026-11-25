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

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// prefixOnlyHomes places every tenant in the node-wide store, the placement
// of a node whose tenants are all prefix tenants.
type prefixOnlyHomes struct{}

func (prefixOnlyHomes) InHome(context.Context, string) (bool, error) { return false, nil }

func (prefixOnlyHomes) Resolve(context.Context, string) (kvstore.KVStore, error) {
	return nil, errors.New("prefix tenants have no home store")
}

// attachPrefixHomes attaches prefixOnlyHomes to svc.
func attachPrefixHomes(t testing.TB, svc VectorService) {
	t.Helper()
	homed, ok := svc.(interface{ SetTenantHomeResolver(TenantHomeResolver) })
	require.True(t, ok, "vector service takes a tenant home resolver")
	homed.SetTenantHomeResolver(prefixOnlyHomes{})
}

// fakeVectorHomes keeps "acme" in a home store and every other tenant
// prefix-scoped; resolveErr, when set, is the error Resolve returns.
type fakeVectorHomes struct {
	mu         sync.Mutex
	homes      map[string]*kvstore.MemoryKVStore
	resolveErr error
}

func newFakeVectorHomes() *fakeVectorHomes {
	return &fakeVectorHomes{homes: map[string]*kvstore.MemoryKVStore{}}
}

func (f *fakeVectorHomes) InHome(_ context.Context, tenantID string) (bool, error) {
	return tenantID == "acme", nil
}

func (f *fakeVectorHomes) Resolve(_ context.Context, tenantID string) (kvstore.KVStore, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.resolveErr != nil {
		return nil, f.resolveErr
	}
	home, ok := f.homes[tenantID]
	if !ok {
		home = kvstore.NewMemoryKVStore()
		f.homes[tenantID] = home
	}
	return home, nil
}

func (f *fakeVectorHomes) home(tenantID string) *kvstore.MemoryKVStore {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.homes[tenantID]
}

func newHomedVectorService(t *testing.T, homes TenantHomeResolver) (VectorService, *vector.MemoryVectorStorage) {
	t.Helper()
	nodeWide := vector.NewMemoryVectorStorage()
	svc, err := NewVectorService(nodeWide, newMockLogger())
	require.NoError(t, err)
	if homes != nil {
		svc.(interface{ SetTenantHomeResolver(TenantHomeResolver) }).SetTenantHomeResolver(homes)
	}
	return svc, nodeWide
}

// exerciseVectorNamespace creates "docs", upserts, gets, searches and lists
// it as the tenant in ctx.
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

func vectorHomeKeys(t *testing.T, home kvstore.KVStore) []string {
	t.Helper()
	keys, err := home.List(context.Background(), tenantVectorHomePrefix)
	require.NoError(t, err)
	return keys
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

// requireVectorHomeRefusal asserts every read and write of tenant in ctx is
// refused with *TenantVectorHomeError, and returns the last refusal.
func requireVectorHomeRefusal(t *testing.T, ctx context.Context, svc VectorService) *TenantVectorHomeError {
	t.Helper()
	var last *TenantVectorHomeError
	check := func(op string, err error) {
		t.Helper()
		homeErr, ok := errors.AsType[*TenantVectorHomeError](err)
		require.True(t, ok, "%s: got %v, want *TenantVectorHomeError", op, err)
		last = homeErr
	}
	_, err := svc.CreateNamespace(ctx, &CreateNamespaceRequest{Namespace: "docs", Dimension: 2, DistanceMetric: "cosine"})
	check("create", err)
	_, err = svc.UpsertVector(ctx, &UpsertVectorRequest{Namespace: "docs", ID: 1, Vector: []float32{1, 0}})
	check("upsert", err)
	_, err = svc.GetVector(ctx, &GetVectorRequest{Namespace: "docs", ID: 1})
	check("get", err)
	_, err = svc.SearchSimilar(ctx, &SearchSimilarRequest{Namespace: "docs", QueryVector: []float32{1, 0}, K: 1})
	check("search", err)
	_, err = svc.ListNamespaces(ctx, &ListNamespacesRequest{})
	check("list", err)
	_, err = svc.DeleteVector(ctx, &DeleteVectorRequest{Namespace: "docs", ID: 1})
	check("delete vector", err)
	_, err = svc.DeleteNamespace(ctx, &DeleteNamespaceRequest{Namespace: "docs"})
	check("delete namespace", err)
	return last
}

func TestVectorService_IsolatedTenantFailsClosedWithoutHome(t *testing.T) {
	homes := newFakeVectorHomes()
	homes.resolveErr = tenant.ErrTenantSealed
	svc, nodeWide := newHomedVectorService(t, homes)
	ctx := tenant.WithTenantID(context.Background(), "acme")

	refusal := requireVectorHomeRefusal(t, ctx, svc)
	require.Equal(t, "acme", refusal.TenantID)
	require.ErrorIs(t, refusal, tenant.ErrTenantSealed)

	nodeNS, err := nodeWide.ListNamespaces(context.Background())
	require.NoError(t, err)
	require.Empty(t, nodeNS, "a refused tenant never reaches the node-wide store")
	vecs, err := nodeWide.ScanNamespace(context.Background(), scopedNamespace("acme", "docs"), nil)
	require.NoError(t, err)
	require.Empty(t, vecs)
}

func TestVectorService_IsolatedTenantRefusedWithoutResolver(t *testing.T) {
	svc, nodeWide := newHomedVectorService(t, nil)
	ctx := tenant.WithTenantID(context.Background(), "acme")

	refusal := requireVectorHomeRefusal(t, ctx, svc)
	require.Equal(t, "acme", refusal.TenantID)

	nodeNS, err := nodeWide.ListNamespaces(context.Background())
	require.NoError(t, err)
	require.Empty(t, nodeNS, "without a resolver no tenant reaches the node-wide store")
}
