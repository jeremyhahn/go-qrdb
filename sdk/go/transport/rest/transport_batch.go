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
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"

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
		return nil, mapOpsNotFoundError(ops, err)
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

// batchImportWireResponse matches the JSON payload returned by
// POST /batch/import.
type batchImportWireResponse struct {
	Total     int `json:"total"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	Errors    []struct {
		Row     int    `json:"row"`
		Key     string `json:"key"`
		Message string `json:"message"`
	} `json:"errors"`
}

// ---------------------------------------------------------------------------
// Chunked import session
// ---------------------------------------------------------------------------

// importBeginWireRequest matches the JSON payload for
// POST /batch/import/begin.
type importBeginWireRequest struct {
	Format        string `json:"format"`
	Filename      string `json:"filename"`
	KeyField      string `json:"key_field"`
	ValueMode     string `json:"value_mode"`
	ValueField    string `json:"value_field"`
	SkipHeader    bool   `json:"skip_header"`
	Transactional bool   `json:"transactional"`
}

// importBeginWireResponse matches the JSON payload returned by
// POST /batch/import/begin.
type importBeginWireResponse struct {
	SessionID string `json:"session_id"`
}

// ImportFile reads filename's entire contents from data and imports it as
// key-value pairs by driving a chunked import session (ImportBegin/
// ImportAppend/ImportCommit below) -- see transport.ImportFileChunked.
func (t *Transport) ImportFile(ctx context.Context, filename string, data io.Reader, opts transport.ImportOptions) (*transport.ImportResult, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return transport.ImportFileChunked(ctx, t, filename, data, opts, 0)
}

// ImportBegin starts a chunked import session via POST /batch/import/begin.
func (t *Transport) ImportBegin(ctx context.Context, opts transport.ImportOptions) (string, error) {
	if t.closed.Load() {
		return "", &transport.ClosedError{}
	}

	wireReq := importBeginWireRequest{
		Format:        opts.Format,
		Filename:      opts.Filename,
		KeyField:      opts.KeyField,
		ValueMode:     opts.ValueMode,
		ValueField:    opts.ValueField,
		SkipHeader:    opts.SkipHeader,
		Transactional: opts.Transactional,
	}
	var wireResp importBeginWireResponse
	if err := t.postJSONDecode(ctx, "ImportBegin", "/batch/import/begin", wireReq, &wireResp); err != nil {
		return "", err
	}
	return wireResp.SessionID, nil
}

// ImportAppend stages chunk as sequence seq of sessionID via
// POST /batch/import/{session}/append?seq=N, with chunk as the raw
// request body (not JSON/base64), so the universal pre-auth cap bounds
// it with no encoding overhead.
func (t *Transport) ImportAppend(ctx context.Context, sessionID string, seq uint64, chunk []byte) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	path := fmt.Sprintf("/batch/import/%s/append?seq=%d", url.PathEscape(sessionID), seq)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+path, bytes.NewReader(chunk))
	if err != nil {
		return &transport.RequestError{Method: "ImportAppend", Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/octet-stream")

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return &transport.RequestError{Method: "ImportAppend", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return t.decodeErrorResponse("ImportAppend", resp)
	}
	return nil
}

// ImportCommit applies every staged chunk of sessionID atomically via
// POST /batch/import/{session}/commit.
func (t *Transport) ImportCommit(ctx context.Context, sessionID string) (*transport.ImportResult, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	path := fmt.Sprintf("/batch/import/%s/commit", url.PathEscape(sessionID))
	var wire batchImportWireResponse
	if err := t.postJSONDecode(ctx, "ImportCommit", path, struct{}{}, &wire); err != nil {
		return nil, err
	}

	result := &transport.ImportResult{
		Total:     wire.Total,
		Succeeded: wire.Succeeded,
		Failed:    wire.Failed,
		Errors:    make([]transport.ImportError, len(wire.Errors)),
	}
	for i, e := range wire.Errors {
		result.Errors[i] = transport.ImportError{Row: e.Row, Key: e.Key, Message: e.Message}
	}
	return result, nil
}

// ImportAbort discards sessionID without applying anything via
// POST /batch/import/{session}/abort.
func (t *Transport) ImportAbort(ctx context.Context, sessionID string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	path := fmt.Sprintf("/batch/import/%s/abort", url.PathEscape(sessionID))
	return t.postJSONDecode(ctx, "ImportAbort", path, struct{}{}, nil)
}
