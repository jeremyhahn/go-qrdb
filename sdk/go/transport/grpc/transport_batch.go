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

package grpc

import (
	"context"
	"io"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.BatchClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// BatchClient implementation
// ---------------------------------------------------------------------------

// ExecuteBatch applies the given operations atomically through the gRPC
// transport. All operations succeed or fail together.
func (t *Transport) ExecuteBatch(ctx context.Context, ops []transport.Op, opts ...transport.WriteOption) (*transport.BatchResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	ctx = applyWriteMetadata(ctx, opts...)

	pbOps := make([]*pb.Op, len(ops))
	for i, op := range ops {
		pbOps[i] = sdkOpToProto(op)
	}

	resp, err := t.client.Batch(ctx, &pb.BatchRequest{
		Ops: pbOps,
	})
	if err != nil {
		return nil, mapGRPCError("ExecuteBatch", err)
	}

	responses := make([]transport.OpResponse, len(resp.GetResponses()))
	for i, r := range resp.GetResponses() {
		responses[i] = protoOpResponseToSDK(r)
	}

	return &transport.BatchResponse{
		Responses: responses,
	}, nil
}

// ImportFile reads filename's entire contents from data and imports it as
// key-value pairs through the gRPC transport's unary BatchImport RPC. The
// active database scope is carried by the transport's x-qrdb-database
// metadata interceptor (the same mechanism used by every other data-plane
// RPC), so no explicit database field is sent on the wire.
func (t *Transport) ImportFile(ctx context.Context, filename string, data io.Reader, opts transport.ImportOptions) (*transport.ImportResult, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	file, err := io.ReadAll(data)
	if err != nil {
		return nil, &transport.RequestError{Method: "ImportFile", Err: err}
	}

	resp, err := t.client.BatchImport(ctx, &pb.BatchImportRequest{
		File:          file,
		Format:        opts.Format,
		Filename:      filename,
		KeyField:      opts.KeyField,
		ValueMode:     opts.ValueMode,
		ValueField:    opts.ValueField,
		SkipHeader:    opts.SkipHeader,
		Transactional: opts.Transactional,
	})
	if err != nil {
		return nil, mapGRPCError("ImportFile", err)
	}

	return batchImportResponseFromProto(resp), nil
}

// batchImportResponseFromProto converts a proto BatchImportResponse to a
// transport.ImportResult.
func batchImportResponseFromProto(resp *pb.BatchImportResponse) *transport.ImportResult {
	errs := make([]transport.ImportError, len(resp.GetErrors()))
	for i, e := range resp.GetErrors() {
		errs[i] = transport.ImportError{
			Row:     int(e.GetRow()),
			Key:     e.GetKey(),
			Message: e.GetMessage(),
		}
	}
	return &transport.ImportResult{
		Total:     int(resp.GetTotal()),
		Succeeded: int(resp.GetSucceeded()),
		Failed:    int(resp.GetFailed()),
		Errors:    errs,
	}
}
