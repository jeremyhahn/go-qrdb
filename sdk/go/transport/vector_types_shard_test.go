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

package transport

import (
	"reflect"
	"strings"
	"testing"
)

// TestVectorRequests_HaveNoShardID pins OD-14 A: the binding decides the
// vector shard, so no vector request type carries a shard ID.
func TestVectorRequests_HaveNoShardID(t *testing.T) {
	requests := []any{
		CreateNamespaceRequest{},
		UpdateNamespaceRequest{},
		DeleteNamespaceRequest{},
		ListNamespacesRequest{},
		GetNamespaceInfoRequest{},
		UpsertVectorRequest{},
		GetVectorRequest{},
		DeleteVectorRequest{},
		BatchUpsertRequest{},
		BatchDeleteVectorsRequest{},
		ListVectorsRequest{},
		UpdateVectorMetadataRequest{},
		SearchSimilarRequest{},
		SearchByMetadataRequest{},
		HybridSearchRequest{},
		SearchByIDRequest{},
		RebuildIndexRequest{},
		GetRebuildProgressRequest{},
		GetIndexStatsRequest{},
		CompactNamespaceRequest{},
		GetCompactionProgressRequest{},
	}
	for _, req := range requests {
		typ := reflect.TypeOf(req)
		t.Run(typ.Name(), func(t *testing.T) {
			for i := range typ.NumField() {
				f := typ.Field(i)
				if strings.EqualFold(f.Name, "ShardID") {
					t.Errorf("%s has field %s", typ.Name(), f.Name)
				}
				name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
				if name == "shard_id" {
					t.Errorf("%s field %s has JSON tag shard_id", typ.Name(), f.Name)
				}
			}
		})
	}
}
