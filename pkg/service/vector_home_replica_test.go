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

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore/kvstoretest"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// homeSearchIDs searches namespace docs through svc and returns the result
// IDs in ascending order.
func homeSearchIDs(t *testing.T, svc *vectorService, ctx context.Context) []uint64 {
	t.Helper()
	res, err := svc.SearchSimilar(ctx, &SearchSimilarRequest{Namespace: "docs", QueryVector: []float32{1, 1}, K: 10})
	require.NoError(t, err)
	ids := make([]uint64, 0, len(res.Results))
	for _, r := range res.Results {
		ids = append(ids, r.ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func upsertHomeIDs(t *testing.T, svc *vectorService, ctx context.Context, ids ...uint64) {
	t.Helper()
	for _, id := range ids {
		_, err := svc.UpsertVector(ctx, &UpsertVectorRequest{Namespace: "docs", ID: id, Vector: []float32{float32(id), 1}})
		require.NoError(t, err)
	}
}

// TestVectorService_ReplicaSeesRecreatedHomeNamespace runs two services --
// two replicas -- over one tenant home store. Replica B caches its index of
// docs; replica A then deletes docs, recreates it and fills it with other
// IDs. B must search the new incarnation, not its cached index, whether the
// recreated namespace's generation count equals the cached one or is lower.
func TestVectorService_ReplicaSeesRecreatedHomeNamespace(t *testing.T) {
	for _, tc := range []struct {
		name   string
		before []uint64
		after  []uint64
	}{
		{name: "equal generation", before: []uint64{1, 2}, after: []uint64{10, 11}},
		{name: "lower generation", before: []uint64{1, 2, 3}, after: []uint64{10}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			homes := &routedHomes{inHome: true, store: kvstoretest.NewGuardedMemoryStore()}
			_, a := newRoutedStorage(t, homes)
			_, b := newRoutedStorage(t, homes)
			ctx := tenant.WithTenantID(context.Background(), "acme")

			_, err := a.CreateNamespace(ctx, &CreateNamespaceRequest{Namespace: "docs", Dimension: 2})
			require.NoError(t, err)
			upsertHomeIDs(t, a, ctx, tc.before...)
			require.Equal(t, tc.before, homeSearchIDs(t, b, ctx), "replica B indexes the first incarnation")

			_, err = a.DeleteNamespace(ctx, &DeleteNamespaceRequest{Namespace: "docs"})
			require.NoError(t, err)
			_, err = a.CreateNamespace(ctx, &CreateNamespaceRequest{Namespace: "docs", Dimension: 2})
			require.NoError(t, err)
			upsertHomeIDs(t, a, ctx, tc.after...)

			require.Equal(t, tc.after, homeSearchIDs(t, b, ctx),
				"replica B searches the recreated namespace, not its cached index of the deleted one")
			require.Equal(t, tc.after, homeSearchIDs(t, a, ctx))
		})
	}
}

var errHomeRead = errors.New("home store read failed")

// failingHome is a guarded home store that fails, while armed, only the Get
// of a key or the Scan of a prefix containing the named segment; every other
// call, and every call while disarmed, is the real store's.
type failingHome struct {
	*kvstoretest.GuardedMemoryStore
	mu       sync.Mutex
	failGet  string
	failScan string
}

func (f *failingHome) arm(get, scan string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failGet, f.failScan = get, scan
}

func (f *failingHome) Get(ctx context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	fail := f.failGet != "" && strings.Contains(key, f.failGet)
	f.mu.Unlock()
	if fail {
		return nil, errHomeRead
	}
	return f.GuardedMemoryStore.Get(ctx, key)
}

func (f *failingHome) Scan(ctx context.Context, prefix string, fn func(key string, value []byte) error) error {
	f.mu.Lock()
	fail := f.failScan != "" && strings.Contains(prefix, f.failScan)
	f.mu.Unlock()
	if fail {
		return errHomeRead
	}
	return f.GuardedMemoryStore.Scan(ctx, prefix, fn)
}

func requireHomeReadError(t *testing.T, svc *vectorService, ctx context.Context) {
	t.Helper()
	res, err := svc.SearchSimilar(ctx, &SearchSimilarRequest{Namespace: "docs", QueryVector: []float32{1, 1}, K: 10})
	require.Nil(t, res, "a failed home read is not an empty result")
	homeErr, ok := errors.AsType[*TenantVectorHomeError](err)
	require.True(t, ok, "got %v", err)
	require.Equal(t, "acme", homeErr.TenantID)
	require.ErrorIs(t, err, errHomeRead)
}

// TestVectorService_HomeReadFailureFailsTheSearch: a search whose home store
// read fails after the home resolved -- the generation read, cold or over a
// cached index, or the vector scan of a rebuild -- returns the tenant's
// TenantVectorHomeError instead of an empty result, and the same search
// answers once the store reads again.
func TestVectorService_HomeReadFailureFailsTheSearch(t *testing.T) {
	for _, tc := range []struct {
		name      string
		warm      bool
		get, scan string
	}{
		{name: "generation read, cold", get: "/gen/"},
		{name: "generation read, cached index", warm: true, get: "/gen/"},
		{name: "vector scan during rebuild", scan: "/v/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &failingHome{GuardedMemoryStore: kvstoretest.NewGuardedMemoryStore()}
			homes := &routedHomes{inHome: true, store: store}
			_, svc := newRoutedStorage(t, homes)
			ctx := tenant.WithTenantID(context.Background(), "acme")
			_, err := svc.CreateNamespace(ctx, &CreateNamespaceRequest{Namespace: "docs", Dimension: 2})
			require.NoError(t, err)
			upsertHomeIDs(t, svc, ctx, 1, 2)
			if tc.warm {
				require.Equal(t, []uint64{1, 2}, homeSearchIDs(t, svc, ctx))
			} else {
				_, svc = newRoutedStorage(t, homes)
			}

			store.arm(tc.get, tc.scan)
			requireHomeReadError(t, svc, ctx)

			store.arm("", "")
			require.Equal(t, []uint64{1, 2}, homeSearchIDs(t, svc, ctx), "the healthy home answers")
		})
	}
}
