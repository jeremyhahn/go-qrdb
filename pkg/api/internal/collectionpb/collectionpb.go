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

// Package collectionpb converts between the collection service types and the
// protobuf messages the gRPC, Unix and QUIC servers exchange. Those servers
// speak the SAME messages, so the conversion lives in exactly one place: a
// field added to a collection -- its index definitions, its lifecycle state --
// reaches every protobuf transport or none of them.
package collectionpb

import (
	"time"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// CreateRequestFromProto converts a protobuf create request into the service
// request, index definitions included.
func CreateRequestFromProto(req *pb.CreateCollectionRequest) *service.CreateCollectionRequest {
	return &service.CreateCollectionRequest{
		Database:     req.GetDatabase(),
		Name:         req.GetName(),
		IDStrategy:   req.GetIdStrategy(),
		FieldHash:    req.GetFieldHash(),
		StorageCodec: req.GetStorageCodec(),
		Indexes:      indexesFromProto(req.GetIndexes()),
	}
}

// InfoToProto converts a service CollectionInfo into its protobuf
// representation, with its index definitions and lifecycle state.
func InfoToProto(info *service.CollectionInfo) *pb.CollectionInfoResponse {
	resp := &pb.CollectionInfoResponse{
		Name:          info.Name,
		Database:      info.Database,
		IdStrategy:    info.IDStrategy,
		FieldHash:     info.FieldHash,
		Schema:        info.Schema,
		DocumentCount: info.DocumentCount,
		CreatedAt:     info.CreatedAt.Format(time.RFC3339Nano),
		StorageCodec:  info.StorageCodec,
		Indexes:       indexesToProto(info.Indexes),
		State:         info.State,
	}
	if !info.StateSince.IsZero() {
		resp.StateSince = info.StateSince.Format(time.RFC3339Nano)
	}
	return resp
}

// indexesFromProto converts protobuf index definitions to the service's.
func indexesFromProto(defs []*pb.CollectionIndexDefinition) []service.IndexDefinition {
	if len(defs) == 0 {
		return nil
	}
	out := make([]service.IndexDefinition, len(defs))
	for i, d := range defs {
		out[i] = service.IndexDefinition{FieldName: d.GetFieldName(), Unique: d.GetUnique(), Sparse: d.GetSparse()}
	}
	return out
}

// indexesToProto converts the service's index definitions to protobuf.
func indexesToProto(defs []service.IndexDefinition) []*pb.CollectionIndexDefinition {
	if len(defs) == 0 {
		return nil
	}
	out := make([]*pb.CollectionIndexDefinition, len(defs))
	for i, d := range defs {
		out[i] = &pb.CollectionIndexDefinition{FieldName: d.FieldName, Unique: d.Unique, Sparse: d.Sparse}
	}
	return out
}
