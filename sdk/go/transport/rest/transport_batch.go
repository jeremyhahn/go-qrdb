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

package rest

import (
	"context"
	"io"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.BatchClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// Wire types for batch REST communication
// ---------------------------------------------------------------------------

// batchWireRequest matches the JSON payload for POST /batch.
type batchWireRequest struct {
	Ops       []txWireOp `json:"ops"`
	WriteMode string     `json:"write_mode,omitempty"`
}

// batchWireResponse matches the JSON payload returned by POST /batch.
type batchWireResponse struct {
	Responses []txWireOpResponse `json:"responses"`
}

// ---------------------------------------------------------------------------
// BatchClient implementation
// ---------------------------------------------------------------------------

// ExecuteBatch applies the given operations atomically. Maps to
// POST /batch with a JSON body containing the ops array. All operations
// succeed or fail together within a single MVCC revision.
func (t *Transport) ExecuteBatch(ctx context.Context, ops []transport.Op, opts ...transport.WriteOption) (*transport.BatchResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	wireReq := batchWireRequest{
		Ops: opsToWire(ops),
	}

	wo := transport.ResolveWriteOptions(opts...)
	if wo.ModeSet {
		wireReq.WriteMode = wo.Mode.String()
	}

	var wireResp batchWireResponse
	if err := t.postJSONDecode(ctx, "ExecuteBatch", "/batch", wireReq, &wireResp); err != nil {
		return nil, err
	}

	responses := make([]transport.OpResponse, len(wireResp.Responses))
	for i, wr := range wireResp.Responses {
		kvs := make([]*transport.MVCCKeyValue, len(wr.Kvs))
		for j, wkv := range wr.Kvs {
			kvs[j] = wireKVToTransport(wkv)
		}
		responses[i] = transport.OpResponse{
			Kvs:       kvs,
			Count:     wr.Count,
			Succeeded: wr.Succeeded,
		}
	}

	return &transport.BatchResponse{
		Responses: responses,
	}, nil
}

// ImportFile is not yet implemented for the REST transport.
func (t *Transport) ImportFile(_ context.Context, _ string, _ io.Reader, _ transport.ImportOptions) (*transport.ImportResult, error) {
	return nil, &transport.NotImplementedError{Method: "ImportFile"}
}
