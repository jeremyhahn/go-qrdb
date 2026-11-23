package transport

import (
	"reflect"
	"strings"
	"testing"
)

// TestVectorRequests_HaveNoShardID asserts that no vector request type
// declared in vector_types.go carries a shard ID: the database's binding
// decides the vector shard, never a client field (OD-14 A).
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
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if strings.EqualFold(field.Name, "ShardID") {
				t.Errorf("%s has a %s field", typ.Name(), field.Name)
			}
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if name == "shard_id" {
				t.Errorf("%s.%s has the JSON tag shard_id", typ.Name(), field.Name)
			}
		}
	}
}
