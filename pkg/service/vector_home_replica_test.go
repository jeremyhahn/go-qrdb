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
	"sort"
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
