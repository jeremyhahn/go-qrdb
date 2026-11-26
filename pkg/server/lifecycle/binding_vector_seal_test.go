//go:build qrdb_vector

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

package lifecycle_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestLocalBindingFactory_BarrierTenantSealFailsCachedVectorSearch pins that
// a materialized database's vector service, retained across the seal of its
// tenant barrier, refuses a search its warm in-memory index could answer:
// the cached read goes through the sealed binding store first. The unseal
// restores it.
//
// The file store only: an open pebble store still serves reads (KV and
// vector alike) after its tenant barrier seals, since its seal watch gates
// writes, flushes and opens but not reads; that store-level gap is outside
// the vector service and is tracked on the row.
func TestLocalBindingFactory_BarrierTenantSealFailsCachedVectorSearch(t *testing.T) {
	ctx := context.Background()
	search := &service.SearchSimilarRequest{Namespace: "ns", QueryVector: []float32{1, 0}, K: 2}
	for _, b := range []database.BackendType{database.BackendFile, database.BackendMemory, database.BackendPebble} {
		t.Run(string(b), func(t *testing.T) {
			dir := t.TempDir()
			reg, tb := newRegisteredTenantBarrier(t)
			mgr := newBarrierTenantManager(t, dir, atrest.New(newUnsealedBarrier(t)),
				lifecycle.TenantBarrierEncryptorLookup(func() *seal.BarrierRegistry { return reg }))
			defer func() { _ = mgr.Close() }()
			db, err := mgr.Create(database.DatabaseConfig{TenantID: bindingTenant, Name: "vault", Backend: b})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			vec := materialized(t, db).Vector()
			if vec == nil {
				t.Fatal("the materialized database has no vector service")
			}
			if _, err := vec.CreateNamespace(ctx, &service.CreateNamespaceRequest{Namespace: "ns", Dimension: 2, DistanceMetric: "COSINE"}); err != nil {
				t.Fatalf("CreateNamespace: %v", err)
			}
			for id, v := range map[uint64][]float32{1: {1, 0}, 2: {0, 1}} {
				if _, err := vec.UpsertVector(ctx, &service.UpsertVectorRequest{Namespace: "ns", ID: id, Vector: v}); err != nil {
					t.Fatalf("UpsertVector(%d): %v", id, err)
				}
			}
			assertSearchFindsOne := func(when string) {
				t.Helper()
				resp, err := vec.SearchSimilar(ctx, search)
				if err != nil {
					t.Fatalf("SearchSimilar %s: %v", when, err)
				}
				if len(resp.Results) == 0 || resp.Results[0].ID != 1 {
					t.Fatalf("SearchSimilar %s = %+v, want id 1 first", when, resp.Results)
				}
			}
			assertSearchFindsOne("before the seal")

			if err := tb.Seal(); err != nil {
				t.Fatalf("Seal: %v", err)
			}
			resp, err := vec.SearchSimilar(ctx, search)
			var sealed *service.BarrierSealedError
			if !errors.As(err, &sealed) {
				t.Fatalf("SearchSimilar on a sealed tenant barrier = %+v, %v; want *service.BarrierSealedError", resp, err)
			}
			if resp != nil && len(resp.Results) != 0 {
				t.Fatalf("a sealed tenant barrier served vector IDs %+v", resp.Results)
			}

			if err := tb.Unseal(ctx, seal.MustCredentials(ctx, []byte("localstore-at-rest-passphrase"))); err != nil {
				t.Fatalf("Unseal: %v", err)
			}
			assertSearchFindsOne("after the unseal")
		})
	}
}
