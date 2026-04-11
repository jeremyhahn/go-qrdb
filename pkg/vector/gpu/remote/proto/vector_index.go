// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

// Package proto contains the hand-written Go types that correspond to the
// protobuf definitions in vector_index.proto. When protoc is available the
// generated file should replace this one; until then these types are used
// directly by the remote GPU index implementation.
package proto

import (
	"context"

	"google.golang.org/grpc"
)

// Request and response message types -------------------------------------------

// InsertRequest carries a single vector with its unique ID.
type InsertRequest struct {
	ID     uint64    `json:"id"`
	Vector []float32 `json:"vector"`
}

// InsertResponse is empty; errors are conveyed via gRPC status codes.
type InsertResponse struct{}

// DeleteRequest identifies the vector to remove.
type DeleteRequest struct {
	ID uint64 `json:"id"`
}

// DeleteResponse is empty; errors are conveyed via gRPC status codes.
type DeleteResponse struct{}

// SearchRequest carries the query vector and the desired result count.
type SearchRequest struct {
	Query []float32 `json:"query"`
	K     int32     `json:"k"`
}

// SearchResponse holds the ordered list of nearest neighbours.
type SearchResponse struct {
	Results []*SearchResult `json:"results"`
}

// SearchResult is one entry in a nearest-neighbour result set.
type SearchResult struct {
	ID       uint64  `json:"id"`
	Distance float32 `json:"distance"`
}

// BatchInsertRequest groups multiple inserts into a single RPC.
type BatchInsertRequest struct {
	Items []*InsertRequest `json:"items"`
}

// BatchInsertResponse reports how many vectors were actually inserted.
type BatchInsertResponse struct {
	Count int32 `json:"count"`
}

// CountRequest has no fields.
type CountRequest struct{}

// CountResponse carries the current vector count.
type CountResponse struct {
	Count int64 `json:"count"`
}

// StatsRequest has no fields.
type StatsRequest struct{}

// StatsResponse mirrors interfaces.HNSWStats.
type StatsResponse struct {
	Count      int64 `json:"count"`
	Capacity   int64 `json:"capacity"`
	Dimensions int32 `json:"dimensions"`
}

// Service interface and client -------------------------------------------------

// VectorIndexServiceClient is the client API for VectorIndexService.
// It matches what protoc would generate and allows the remote package to
// depend on this interface rather than a concrete gRPC stub.
type VectorIndexServiceClient interface {
	Insert(ctx context.Context, in *InsertRequest, opts ...grpc.CallOption) (*InsertResponse, error)
	Delete(ctx context.Context, in *DeleteRequest, opts ...grpc.CallOption) (*DeleteResponse, error)
	Search(ctx context.Context, in *SearchRequest, opts ...grpc.CallOption) (*SearchResponse, error)
	BatchInsert(ctx context.Context, in *BatchInsertRequest, opts ...grpc.CallOption) (*BatchInsertResponse, error)
	Count(ctx context.Context, in *CountRequest, opts ...grpc.CallOption) (*CountResponse, error)
	GetStats(ctx context.Context, in *StatsRequest, opts ...grpc.CallOption) (*StatsResponse, error)
}

// Service descriptor constants used when registering the server with grpc.Server.
const (
	VectorIndexService_Insert_FullMethodName      = "/qrdb.vector.gpu.remote.VectorIndexService/Insert"
	VectorIndexService_Delete_FullMethodName      = "/qrdb.vector.gpu.remote.VectorIndexService/Delete"
	VectorIndexService_Search_FullMethodName      = "/qrdb.vector.gpu.remote.VectorIndexService/Search"
	VectorIndexService_BatchInsert_FullMethodName = "/qrdb.vector.gpu.remote.VectorIndexService/BatchInsert"
	VectorIndexService_Count_FullMethodName       = "/qrdb.vector.gpu.remote.VectorIndexService/Count"
	VectorIndexService_GetStats_FullMethodName    = "/qrdb.vector.gpu.remote.VectorIndexService/GetStats"
)
