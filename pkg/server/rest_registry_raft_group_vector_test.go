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

package server

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestRestRegistry_RaftGroupDatabaseServesVectorSearch pins that a
// backend: raft database's bundle carries its own vector service, over its
// own group, and that the service answers a search.
func TestRestRegistry_RaftGroupDatabaseServesVectorSearch(t *testing.T) {
	for _, isolation := range []tenant.IsolationType{tenant.IsolationPrefix, tenant.IsolationBarrier} {
		t.Run(string(isolation), func(t *testing.T) {
			ctx := context.Background()
			rr, _, _ := raftGroupRegistry(t, "vec", isolation, t.TempDir(), isolation == tenant.IsolationBarrier)
			vec, err := rr.VectorService("vec", "app")
			if err != nil {
				t.Fatalf("VectorService: %v", err)
			}
			if _, err := vec.CreateNamespace(ctx, &service.CreateNamespaceRequest{Namespace: "ns", Dimension: 2, DistanceMetric: "COSINE"}); err != nil {
				t.Fatalf("CreateNamespace: %v", err)
			}
			for id, v := range map[uint64][]float32{1: {1, 0}, 2: {0, 1}} {
				if _, err := vec.UpsertVector(ctx, &service.UpsertVectorRequest{Namespace: "ns", ID: id, Vector: v}); err != nil {
					t.Fatalf("UpsertVector(%d): %v", id, err)
				}
			}
			resp, err := vec.SearchSimilar(ctx, &service.SearchSimilarRequest{Namespace: "ns", QueryVector: []float32{1, 0}, K: 2})
			if err != nil {
				t.Fatalf("SearchSimilar: %v", err)
			}
			if len(resp.Results) == 0 || resp.Results[0].ID != 1 {
				t.Fatalf("SearchSimilar = %+v, want id 1 first", resp.Results)
			}
		})
	}
}
