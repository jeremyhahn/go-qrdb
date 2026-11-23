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

//go:build !integration

package grpc

import (
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
)

// TestVectorRequests_ShardIDReserved asserts that no vector request message
// has a shard_id field and that each one reserves the name, so the field
// number and name can never be reused: the database's binding decides the
// vector shard (OD-14 A).
func TestVectorRequests_ShardIDReserved(t *testing.T) {
	requests := []proto.Message{
		&pb.VectorCreateNamespaceRequest{},
		&pb.VectorDeleteNamespaceRequest{},
		&pb.VectorListNamespacesRequest{},
		&pb.VectorGetNamespaceInfoRequest{},
		&pb.VectorUpsertRequest{},
		&pb.VectorGetRequest{},
		&pb.VectorDeleteRequest{},
		&pb.VectorBatchUpsertRequest{},
		&pb.VectorBatchDeleteRequest{},
		&pb.VectorListRequest{},
		&pb.VectorUpdateMetadataRequest{},
		&pb.VectorSearchSimilarRequest{},
		&pb.VectorSearchByMetadataRequest{},
		&pb.VectorHybridSearchRequest{},
		&pb.VectorSearchByIDRequest{},
		&pb.VectorRebuildIndexRequest{},
		&pb.VectorGetRebuildProgressRequest{},
		&pb.VectorGetIndexStatsRequest{},
		&pb.VectorCompactNamespaceRequest{},
		&pb.VectorGetCompactionProgressRequest{},
	}
	for _, msg := range requests {
		desc := msg.ProtoReflect().Descriptor()
		if f := desc.Fields().ByName("shard_id"); f != nil {
			t.Errorf("%s has field shard_id = %d", desc.FullName(), f.Number())
		}
		if !desc.ReservedNames().Has("shard_id") {
			t.Errorf("%s does not reserve the name shard_id", desc.FullName())
		}
	}
}
