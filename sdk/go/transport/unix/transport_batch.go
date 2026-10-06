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

package unix

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

// ExecuteBatch applies the given operations atomically. All operations
// succeed or fail together.
func (t *Transport) ExecuteBatch(ctx context.Context, ops []transport.Op, opts ...transport.WriteOption) (*transport.BatchResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	ctx = applyWriteMetadata(ctx, opts...)

	pbOps := make([]*pb.Op, len(ops))
	for i, op := range ops {
		pbOps[i] = opToProto(op)
	}

	resp, err := t.client.Batch(ctx, &pb.BatchRequest{
		Ops: pbOps,
	})
	if err != nil {
		return nil, mapOpsNotFoundError("ExecuteBatch", ops, err)
	}

	responses := make([]transport.OpResponse, len(resp.GetResponses()))
	for i, r := range resp.GetResponses() {
		responses[i] = protoToOpResponse(r)
	}

	return &transport.BatchResponse{
		Responses: responses,
	}, nil
}

// ImportFile reads filename's entire contents from data and imports it as
// key-value pairs by driving a chunked import session (ImportBegin/
// ImportAppend/ImportCommit below) -- see transport.ImportFileChunked. The
// active database scope is carried by the transport's x-qrdb-database
// metadata interceptor (the same mechanism used by every other data-plane
// RPC), so no explicit database field is sent on the wire.
func (t *Transport) ImportFile(ctx context.Context, filename string, data io.Reader, opts transport.ImportOptions) (*transport.ImportResult, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return transport.ImportFileChunked(ctx, t, filename, data, opts, 0)
}

// ImportBegin starts a chunked import session.
func (t *Transport) ImportBegin(ctx context.Context, opts transport.ImportOptions) (string, error) {
	if t.closed.Load() {
		return "", &transport.ClosedError{}
	}

	resp, err := t.client.ImportBegin(ctx, &pb.ImportBeginRequest{
		Format:        opts.Format,
		Filename:      opts.Filename,
		KeyField:      opts.KeyField,
		ValueMode:     opts.ValueMode,
		ValueField:    opts.ValueField,
		SkipHeader:    opts.SkipHeader,
		Transactional: opts.Transactional,
	})
	if err != nil {
		return "", mapGRPCError("ImportBegin", err)
	}
	return resp.GetSessionId(), nil
}

// ImportAppend stages a chunk of a chunked import session.
func (t *Transport) ImportAppend(ctx context.Context, sessionID string, seq uint64, chunk []byte) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.client.ImportAppend(ctx, &pb.ImportAppendRequest{
		SessionId: sessionID,
		Seq:       seq,
		Chunk:     chunk,
	})
	if err != nil {
		return mapGRPCError("ImportAppend", err)
	}
	return nil
}

// ImportCommit applies every staged chunk of a chunked import session
// atomically.
func (t *Transport) ImportCommit(ctx context.Context, sessionID string) (*transport.ImportResult, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.ImportCommit(ctx, &pb.ImportCommitRequest{SessionId: sessionID})
	if err != nil {
		return nil, mapGRPCError("ImportCommit", err)
	}

	errs := make([]transport.ImportError, len(resp.GetErrors()))
	for i, e := range resp.GetErrors() {
		errs[i] = transport.ImportError{Row: int(e.GetRow()), Key: e.GetKey(), Message: e.GetMessage()}
	}
	return &transport.ImportResult{
		Total:     int(resp.GetTotal()),
		Succeeded: int(resp.GetSucceeded()),
		Failed:    int(resp.GetFailed()),
		Errors:    errs,
	}, nil
}

// ImportAbort discards a chunked import session without applying anything.
func (t *Transport) ImportAbort(ctx context.Context, sessionID string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.client.ImportAbort(ctx, &pb.ImportAbortRequest{SessionId: sessionID})
	if err != nil {
		return mapGRPCError("ImportAbort", err)
	}
	return nil
}
