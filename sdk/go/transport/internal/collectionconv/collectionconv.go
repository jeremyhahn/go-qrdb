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

// Package collectionconv converts between the transport collection types and
// the protobuf messages. It is shared by the gRPC, Unix (gRPC-over-UDS) and
// QUIC transports, which exchange the SAME protobuf messages, so the
// conversion lives in exactly one place instead of being duplicated per
// transport. It is internal: only the transport implementations may import it.
package collectionconv

import (
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ToCreateRequest converts a transport create request into its protobuf
// message, index definitions and storage codec included.
func ToCreateRequest(req transport.CreateCollectionRequest) *pb.CreateCollectionRequest {
	out := &pb.CreateCollectionRequest{
		Database:     req.Database,
		Name:         req.Name,
		IdStrategy:   req.IDStrategy,
		FieldHash:    req.FieldHash,
		StorageCodec: req.StorageCodec,
	}
	for _, d := range req.Indexes {
		out.Indexes = append(out.Indexes, &pb.CollectionIndexDefinition{FieldName: d.FieldName, Unique: d.Unique, Sparse: d.Sparse})
	}
	return out
}

// ToCollectionInfo converts a protobuf CollectionInfoResponse into a
// transport.CollectionInfo, with its index definitions and lifecycle state.
func ToCollectionInfo(p *pb.CollectionInfoResponse) *transport.CollectionInfo {
	info := &transport.CollectionInfo{
		Name:          p.GetName(),
		Database:      p.GetDatabase(),
		IDStrategy:    p.GetIdStrategy(),
		FieldHash:     p.GetFieldHash(),
		Schema:        p.GetSchema(),
		DocumentCount: p.GetDocumentCount(),
		StorageCodec:  p.GetStorageCodec(),
		CreatedAt:     p.GetCreatedAt(),
		State:         p.GetState(),
		StateSince:    p.GetStateSince(),
	}
	for _, d := range p.GetIndexes() {
		info.Indexes = append(info.Indexes, transport.CollectionIndexDefinition{
			FieldName: d.GetFieldName(), Unique: d.GetUnique(), Sparse: d.GetSparse(),
		})
	}
	return info
}
