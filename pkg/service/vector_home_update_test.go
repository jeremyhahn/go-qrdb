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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore/kvstoretest"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// searchIDs runs a K-nearest search on the "docs" namespace and returns the
// IDs it found, sorted.
func searchIDs(t *testing.T, vs *vectorService, ctx context.Context, query []float32, k int) []uint64 {
	t.Helper()
	res, err := vs.SearchSimilar(ctx, &SearchSimilarRequest{Namespace: "docs", QueryVector: query, K: k})
	require.NoError(t, err)
	require.NotNil(t, res)
	ids := make([]uint64, 0, len(res.Results))
	for _, r := range res.Results {
		ids = append(ids, r.ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// TestVectorService_HomedUpdateAndSearch covers UpdateNamespace and the home
// index cache for a tenant whose vectors live in its home store: the second
// search reuses the cached index, and a later upsert is visible to the next
// search.
func TestVectorService_HomedUpdateAndSearch(t *testing.T) {
	_, vs := newRoutedStorage(t, &routedHomes{inHome: true, store: kvstoretest.NewGuardedMemoryStore()})
	ctx := tenant.WithTenantID(context.Background(), "acme")

	_, err := vs.CreateNamespace(ctx, &CreateNamespaceRequest{Namespace: "docs", Dimension: 2, DistanceMetric: "cosine"})
	require.NoError(t, err)
	_, err = vs.UpsertVector(ctx, &UpsertVectorRequest{Namespace: "docs", ID: 1, Vector: []float32{1, 0}})
	require.NoError(t, err)

	for range 2 {
		require.Equal(t, []uint64{1}, searchIDs(t, vs, ctx, []float32{1, 0}, 1))
	}
	_, err = vs.UpsertVector(ctx, &UpsertVectorRequest{Namespace: "docs", ID: 2, Vector: []float32{0, 1}})
	require.NoError(t, err)
	require.Equal(t, []uint64{1, 2}, searchIDs(t, vs, ctx, []float32{0, 1}, 2))

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
