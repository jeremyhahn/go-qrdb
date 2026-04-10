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

package mcp

import (
	"context"
	"encoding/json"
	"io"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.BatchClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// BatchClient implementation
// ---------------------------------------------------------------------------

// ExecuteBatch applies the given operations atomically within a single
// MVCC revision. All operations succeed or fail together. Calls the
// qrdb_batch_execute MCP tool.
func (t *Transport) ExecuteBatch(ctx context.Context, ops []transport.Op, opts ...transport.WriteOption) (*transport.BatchResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	opsJSON, err := marshalOpsWire(ops)
	if err != nil {
		return nil, &transport.RequestError{Method: "ExecuteBatch", Err: &jsonDecodeError{Err: err}}
	}

	args := map[string]any{
		"ops": string(opsJSON),
	}

	wo := transport.ResolveWriteOptions(opts...)
	if wo.ModeSet {
		args["write_mode"] = wo.Mode.String()
	}

	text, callErr := t.callTool(ctx, "qrdb_batch_execute", args)
	if callErr != nil {
		return nil, callErr
	}

	var raw []serverOpResponse
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "ExecuteBatch", Err: &jsonDecodeError{Err: jsonErr}}
	}

	responses := make([]transport.OpResponse, len(raw))
	for i, r := range raw {
		kvs := make([]*transport.MVCCKeyValue, len(r.Kvs))
		for j := range r.Kvs {
			kvs[j] = serverKeyValueToTransport(&r.Kvs[j])
		}
		responses[i] = transport.OpResponse{
			Kvs:       kvs,
			Count:     r.Count,
			Succeeded: r.Succeeded,
		}
	}

	return &transport.BatchResponse{
		Responses: responses,
	}, nil
}

// ImportFile is not yet implemented for the MCP transport.
func (t *Transport) ImportFile(_ context.Context, _ string, _ io.Reader, _ transport.ImportOptions) (*transport.ImportResult, error) {
	return nil, &transport.NotImplementedError{Method: "ImportFile"}
}
