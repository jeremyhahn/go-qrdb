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

package remote

import (
	"context"
	"encoding/json"
	"fmt"

	"google.golang.org/grpc"

	proto "github.com/jeremyhahn/go-qrdb/pkg/vector/gpu/remote/proto"
)

// vectorIndexServiceClient is the hand-written gRPC client for VectorIndexService.
// It uses grpc.Invoke directly with JSON encoding over the wire until protoc-generated
// code is available. When protoc output replaces proto/vector_index.go this file
// should be deleted and replaced with the generated NewVectorIndexServiceClient call.
type vectorIndexServiceClient struct {
	cc grpc.ClientConnInterface
}

// newVectorIndexServiceClient returns a proto.VectorIndexServiceClient backed by cc.
func newVectorIndexServiceClient(cc grpc.ClientConnInterface) proto.VectorIndexServiceClient {
	return &vectorIndexServiceClient{cc: cc}
}

func (c *vectorIndexServiceClient) Insert(ctx context.Context, in *proto.InsertRequest, opts ...grpc.CallOption) (*proto.InsertResponse, error) {
	out := &proto.InsertResponse{}
	if err := c.invoke(ctx, proto.VectorIndexService_Insert_FullMethodName, in, out, opts...); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *vectorIndexServiceClient) Delete(ctx context.Context, in *proto.DeleteRequest, opts ...grpc.CallOption) (*proto.DeleteResponse, error) {
	out := &proto.DeleteResponse{}
	if err := c.invoke(ctx, proto.VectorIndexService_Delete_FullMethodName, in, out, opts...); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *vectorIndexServiceClient) Search(ctx context.Context, in *proto.SearchRequest, opts ...grpc.CallOption) (*proto.SearchResponse, error) {
	out := &proto.SearchResponse{}
	if err := c.invoke(ctx, proto.VectorIndexService_Search_FullMethodName, in, out, opts...); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *vectorIndexServiceClient) BatchInsert(ctx context.Context, in *proto.BatchInsertRequest, opts ...grpc.CallOption) (*proto.BatchInsertResponse, error) {
	out := &proto.BatchInsertResponse{}
	if err := c.invoke(ctx, proto.VectorIndexService_BatchInsert_FullMethodName, in, out, opts...); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *vectorIndexServiceClient) Count(ctx context.Context, in *proto.CountRequest, opts ...grpc.CallOption) (*proto.CountResponse, error) {
	out := &proto.CountResponse{}
	if err := c.invoke(ctx, proto.VectorIndexService_Count_FullMethodName, in, out, opts...); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *vectorIndexServiceClient) GetStats(ctx context.Context, in *proto.StatsRequest, opts ...grpc.CallOption) (*proto.StatsResponse, error) {
	out := &proto.StatsResponse{}
	if err := c.invoke(ctx, proto.VectorIndexService_GetStats_FullMethodName, in, out, opts...); err != nil {
		return nil, err
	}
	return out, nil
}

// invoke encodes req as JSON, calls the remote method, and decodes the response.
// This bridges the hand-written types to the gRPC transport until protoc output
// with proper protobuf wire encoding is available.
func (c *vectorIndexServiceClient) invoke(ctx context.Context, method string, req, resp any, opts ...grpc.CallOption) error {
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request for %s: %w", method, err)
	}

	var respBytes []byte
	if err := c.cc.Invoke(ctx, method, reqBytes, &respBytes, opts...); err != nil {
		return err
	}

	if len(respBytes) == 0 {
		return nil
	}
	if err := json.Unmarshal(respBytes, resp); err != nil {
		return fmt.Errorf("unmarshal response for %s: %w", method, err)
	}
	return nil
}
