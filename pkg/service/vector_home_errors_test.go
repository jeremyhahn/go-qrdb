package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

var errHomeFault = errors.New("injected home fault")

// routedHomes is a TenantHomeResolver whose answers each test sets.
type routedHomes struct {
	inHome     bool
	inHomeErr  error
	resolveErr error
	store      kvstore.KVStore
}

func (h *routedHomes) InHome(context.Context, string) (bool, error) {
	return h.inHome, h.inHomeErr
}

func (h *routedHomes) Resolve(context.Context, string) (kvstore.KVStore, error) {
	return h.store, h.resolveErr
}

func newRoutedStorage(t *testing.T, homes TenantHomeResolver) (*tenantRoutedVectorStorage, *vectorService) {
	t.Helper()
	svc, err := NewVectorService(vector.NewMemoryVectorStorage(), newVecTestLogger())
	require.NoError(t, err)
	vs, ok := svc.(*vectorService)
	require.True(t, ok)
	if homes != nil {
		vs.SetTenantHomeResolver(homes)
	}
	return &tenantRoutedVectorStorage{v: vs}, vs
}

// TestTenantRoutedVectorStorage_HomeRoundTrip drives every routed method
// against a tenant's home store.
func TestTenantRoutedVectorStorage_HomeRoundTrip(t *testing.T) {
	homeKV := kvstore.NewMemoryKVStore()
	r, vs := newRoutedStorage(t, &routedHomes{inHome: true, store: homeKV})
	ctx := context.Background()
	ns := scopedNamespace("acme", "docs")

	require.NoError(t, r.CreateNamespace(ctx, &vector.NamespaceInfo{Name: ns, Dimension: 2, DistanceMetric: "cosine"}))
	require.NoError(t, r.UpsertVector(ctx, ns, &vector.VectorValue{ID: 1, Embedding: []float32{1, 0}, Metadata: map[string]string{"k": "v"}}))
	require.NoError(t, r.BatchUpsertVectors(ctx, ns, []*vector.VectorValue{
		{ID: 2, Embedding: []float32{0, 1}, Metadata: map[string]string{"k": "w"}},
		{ID: 3, Embedding: []float32{1, 1}},
	}))
	got, err := r.GetVector(ctx, ns, 1)
	require.NoError(t, err)
	require.Equal(t, uint64(1), got.ID)

	_, err = r.ScanByMetadata(ctx, ns, "k", "v", nil)
	require.NoError(t, err)
	_, err = r.ScanByMultipleFilters(ctx, ns, []vector.MetadataFilter{{Field: "k", Value: "w"}}, nil)
	require.NoError(t, err)
	all, err := r.ScanNamespace(ctx, ns, nil)
	require.NoError(t, err)
	require.Len(t, all, 3)

	require.NoError(t, r.UpdateNamespace(ctx, ns, &vector.NamespaceUpdate{Metadata: map[string]string{"m": "1"}}))
	info, err := r.GetNamespaceInfo(ctx, ns)
	require.NoError(t, err)
	require.Equal(t, ns, info.Name)
	_, err = r.GetStats(ctx, ns)
	require.NoError(t, err)
	_, err = r.ListNamespaces(ctx)
	require.NoError(t, err)

	_, err = r.NextID(ns)
	require.NoError(t, err)
	ids, err := r.BatchAllocateIDs(ns, 2)
	require.NoError(t, err)
	require.Len(t, ids, 2)

	vs.noteHomeWrite(ctx, mustStorageFor(t, vs, "acme"), ns)
	require.NoError(t, r.DeleteVector(ctx, ns, 1))
	require.NoError(t, r.BatchDeleteVectors(ctx, ns, []uint64{2}))
	require.NoError(t, r.Compact(ctx, ns))
	require.NoError(t, r.CleanupTombstones(ctx, time.Hour))
	require.NoError(t, r.DeleteNamespace(ctx, ns))

	nodeNS, err := vs.storage.ListNamespaces(ctx)
	require.NoError(t, err)
	require.Empty(t, nodeNS, "home writes never reach the node-wide store")
	require.NoError(t, r.Close())
}

func mustStorageFor(t *testing.T, vs *vectorService, tenantID string) vector.Storage {
	t.Helper()
	st, err := vs.storageFor(context.Background(), tenantID)
	require.NoError(t, err)
	return st
}

// TestTenantRoutedVectorStorage_RefusesEveryMethod checks each routed method
// fails closed with a TenantVectorHomeError when the home cannot be reached.
func TestTenantRoutedVectorStorage_RefusesEveryMethod(t *testing.T) {
	ns := scopedNamespace("acme", "docs")
	calls := map[string]func(context.Context, *tenantRoutedVectorStorage) error{
		"upsert": func(ctx context.Context, r *tenantRoutedVectorStorage) error {
			return r.UpsertVector(ctx, ns, &vector.VectorValue{ID: 1, Embedding: []float32{1, 0}})
		},
		"get": func(ctx context.Context, r *tenantRoutedVectorStorage) error {
			_, err := r.GetVector(ctx, ns, 1)
			return err
		},
		"delete": func(ctx context.Context, r *tenantRoutedVectorStorage) error { return r.DeleteVector(ctx, ns, 1) },
		"batch upsert": func(ctx context.Context, r *tenantRoutedVectorStorage) error {
			return r.BatchUpsertVectors(ctx, ns, []*vector.VectorValue{{ID: 1, Embedding: []float32{1, 0}}})
		},
		"batch delete": func(ctx context.Context, r *tenantRoutedVectorStorage) error {
			return r.BatchDeleteVectors(ctx, ns, []uint64{1})
		},
		"scan metadata": func(ctx context.Context, r *tenantRoutedVectorStorage) error {
			_, err := r.ScanByMetadata(ctx, ns, "k", "v", nil)
			return err
		},
		"scan filters": func(ctx context.Context, r *tenantRoutedVectorStorage) error {
			_, err := r.ScanByMultipleFilters(ctx, ns, nil, nil)
			return err
		},
		"scan namespace": func(ctx context.Context, r *tenantRoutedVectorStorage) error {
			_, err := r.ScanNamespace(ctx, ns, nil)
			return err
		},
		"create": func(ctx context.Context, r *tenantRoutedVectorStorage) error {
			return r.CreateNamespace(ctx, &vector.NamespaceInfo{Name: ns, Dimension: 2})
		},
		"update": func(ctx context.Context, r *tenantRoutedVectorStorage) error {
			return r.UpdateNamespace(ctx, ns, &vector.NamespaceUpdate{})
		},
		"delete namespace": func(ctx context.Context, r *tenantRoutedVectorStorage) error { return r.DeleteNamespace(ctx, ns) },
		"info": func(ctx context.Context, r *tenantRoutedVectorStorage) error {
			_, err := r.GetNamespaceInfo(ctx, ns)
			return err
		},
		"stats": func(ctx context.Context, r *tenantRoutedVectorStorage) error {
			_, err := r.GetStats(ctx, ns)
			return err
		},
		"next id": func(_ context.Context, r *tenantRoutedVectorStorage) error {
			_, err := r.NextID(ns)
			return err
		},
		"batch ids": func(_ context.Context, r *tenantRoutedVectorStorage) error {
			_, err := r.BatchAllocateIDs(ns, 2)
			return err
		},
		"compact": func(ctx context.Context, r *tenantRoutedVectorStorage) error { return r.Compact(ctx, ns) },
	}
	homes := map[string]TenantHomeResolver{
		"no resolver":   nil,
		"in-home fault": &routedHomes{inHomeErr: errHomeFault},
		"resolve fault": &routedHomes{inHome: true, resolveErr: errHomeFault},
		"nil store":     &routedHomes{inHome: true},
	}
	for homeName, h := range homes {
		for callName, call := range calls {
			t.Run(homeName+"/"+callName, func(t *testing.T) {
				r, _ := newRoutedStorage(t, h)
				err := call(context.Background(), r)
				homeErr, ok := errors.AsType[*TenantVectorHomeError](err)
				require.True(t, ok, "got %v", err)
				require.Equal(t, "acme", homeErr.TenantID)
			})
		}
	}
}

// TestVectorService_StorageForRoutesAndCaches covers the node-wide fallback,
// the home cache and resolver replacement.
func TestVectorService_StorageForRoutesAndCaches(t *testing.T) {
	ctx := context.Background()
	homes := &routedHomes{}
	_, vs := newRoutedStorage(t, homes)

	st, err := vs.storageFor(ctx, "acme")
	require.NoError(t, err)
	require.Same(t, vs.storage, st, "a tenant outside its home uses the node-wide store")

	homes.inHome = true
	homes.store = kvstore.NewMemoryKVStore()
	first := mustStorageFor(t, vs, "acme")
	require.NotSame(t, vs.storage, first)
	require.Same(t, first, mustStorageFor(t, vs, "acme"), "the same home store is cached")

	homes.store = kvstore.NewMemoryKVStore()
	require.NotSame(t, first, mustStorageFor(t, vs, "acme"), "a replaced home store is rebuilt")

	vs.SetTenantHomeResolver(nil)
	_, err = vs.storageFor(ctx, "acme")
	_, ok := errors.AsType[*TenantVectorHomeError](err)
	require.True(t, ok, "got %v", err)

	// noteHomeWrite ignores the node-wide store.
	vs.noteHomeWrite(ctx, vs.storage, scopedNamespace("acme", "docs"))
}

// TestVectorService_HomedUpdateAndSearch covers UpdateNamespace and the home
// index cache for a tenant whose vectors live in its home store.
func TestVectorService_HomedUpdateAndSearch(t *testing.T) {
	_, vs := newRoutedStorage(t, &routedHomes{inHome: true, store: kvstore.NewMemoryKVStore()})
	ctx := tenant.WithTenantID(context.Background(), "acme")

	_, err := vs.CreateNamespace(ctx, &CreateNamespaceRequest{Namespace: "docs", Dimension: 2, DistanceMetric: "cosine"})
	require.NoError(t, err)
	_, err = vs.UpsertVector(ctx, &UpsertVectorRequest{Namespace: "docs", ID: 1, Vector: []float32{1, 0}})
	require.NoError(t, err)

	for range 2 {
		res, err := vs.SearchSimilar(ctx, &SearchSimilarRequest{Namespace: "docs", QueryVector: []float32{1, 0}, K: 1})
		require.NoError(t, err)
		require.NotNil(t, res)
	}
	_, err = vs.UpsertVector(ctx, &UpsertVectorRequest{Namespace: "docs", ID: 2, Vector: []float32{0, 1}})
	require.NoError(t, err)
	_, err = vs.SearchSimilar(ctx, &SearchSimilarRequest{Namespace: "docs", QueryVector: []float32{0, 1}, K: 2})
	require.NoError(t, err)

	upd, err := vs.UpdateNamespace(ctx, &UpdateNamespaceRequest{Namespace: "docs", Dimension: 2, Metadata: map[string]interface{}{"owner": "acme"}})
	require.NoError(t, err)
	require.True(t, upd.Success)

	_, err = vs.UpdateNamespace(ctx, &UpdateNamespaceRequest{Namespace: "docs", Dimension: 3})
	_, ok := errors.AsType[*DimensionMismatchError](err)
	require.True(t, ok, "got %v", err)
	_, err = vs.UpdateNamespace(ctx, &UpdateNamespaceRequest{Namespace: "missing"})
	require.Error(t, err)
	_, err = vs.UpdateNamespace(ctx, nil)
	_, ok = errors.AsType[*InvalidVectorError](err)
	require.True(t, ok, "got %v", err)
	_, err = vs.UpdateNamespace(ctx, &UpdateNamespaceRequest{})
	_, ok = errors.AsType[*InvalidVectorError](err)
	require.True(t, ok, "got %v", err)

	require.NoError(t, vs.Close())
	_, err = vs.UpdateNamespace(ctx, &UpdateNamespaceRequest{Namespace: "docs"})
	_, ok = errors.AsType[*VectorServiceClosedError](err)
	require.True(t, ok, "got %v", err)
}
