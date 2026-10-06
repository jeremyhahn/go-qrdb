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
	"encoding/base64"
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
		return nil, mapOpsNotFoundError(ops, callErr)
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

// serverImportError mirrors the JSON encoding of service.ImportError, which
// carries lowercase json tags.
type serverImportError struct {
	Row     int    `json:"row"`
	Key     string `json:"key"`
	Message string `json:"message"`
}

// serverImportResult mirrors the JSON encoding of service.ImportResult,
// which has no json tags and therefore serializes using its exported Go
// field names.
type serverImportResult struct {
	Total     int                 `json:"Total"`
	Succeeded int                 `json:"Succeeded"`
	Failed    int                 `json:"Failed"`
	Errors    []serverImportError `json:"Errors"`
}

// importResultFromJSON decodes text as a serverImportResult, the JSON shape
// qrdb_batch_import, qrdb_batch_import_commit, and qrdb_batch_import_begin's
// (the server ImportResult type has no json tags) response text all share.
func importResultFromJSON(method, text string) (*transport.ImportResult, error) {
	var raw serverImportResult
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return nil, &transport.RequestError{Method: method, Err: &jsonDecodeError{Err: err}}
	}
	errs := make([]transport.ImportError, len(raw.Errors))
	for i, e := range raw.Errors {
		errs[i] = transport.ImportError{Row: e.Row, Key: e.Key, Message: e.Message}
	}
	return &transport.ImportResult{
		Total:     raw.Total,
		Succeeded: raw.Succeeded,
		Failed:    raw.Failed,
		Errors:    errs,
	}, nil
}

// ImportFile reads filename's entire contents from data and imports it as
// key-value pairs by driving a chunked import session (ImportBegin/
// ImportAppend/ImportCommit below) -- see transport.ImportFileChunked. The
// active database scope is carried automatically by callTool for
// data-plane tools (see isDataPlaneTool), so no explicit database argument
// is set on any call.
//
// It chunks at transport.DefaultMCPImportChunkBytes, not the
// transport-agnostic transport.DefaultImportChunkBytes every other
// transport's ImportFile uses: ImportAppend below carries the chunk as
// base64 text in a JSON-RPC body, which has no binary framing to absorb
// base64's 4/3 expansion the way a protobuf `bytes` field or a QUIC wire
// frame does.
func (t *Transport) ImportFile(ctx context.Context, filename string, data io.Reader, opts transport.ImportOptions) (*transport.ImportResult, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return transport.ImportFileChunked(ctx, t, filename, data, opts, transport.DefaultMCPImportChunkBytes)
}

// serverImportBeginResult mirrors the JSON encoding of
// pkg/api/mcp.handleImportBegin's response.
type serverImportBeginResult struct {
	SessionID string `json:"session_id"`
}

// ImportBegin starts a chunked import session by calling the
// qrdb_batch_import_begin MCP tool.
func (t *Transport) ImportBegin(ctx context.Context, opts transport.ImportOptions) (string, error) {
	if t.closed.Load() {
		return "", &transport.ClosedError{}
	}

	args := map[string]any{
		"format":        opts.Format,
		"filename":      opts.Filename,
		"key_field":     opts.KeyField,
		"value_mode":    opts.ValueMode,
		"value_field":   opts.ValueField,
		"skip_header":   opts.SkipHeader,
		"transactional": opts.Transactional,
	}

	text, callErr := t.callTool(ctx, "qrdb_batch_import_begin", args)
	if callErr != nil {
		return "", callErr
	}

	var raw serverImportBeginResult
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return "", &transport.RequestError{Method: "ImportBegin", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return raw.SessionID, nil
}

// ImportAppend stages a base64-encoded chunk of a chunked import session
// by calling the qrdb_batch_import_append MCP tool.
func (t *Transport) ImportAppend(ctx context.Context, sessionID string, seq uint64, chunk []byte) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	args := map[string]any{
		"session_id": sessionID,
		"seq":        float64(seq),
		"chunk":      base64.StdEncoding.EncodeToString(chunk),
	}

	if _, callErr := t.callTool(ctx, "qrdb_batch_import_append", args); callErr != nil {
		return callErr
	}
	return nil
}

// ImportCommit applies every staged chunk of a chunked import session
// atomically by calling the qrdb_batch_import_commit MCP tool.
func (t *Transport) ImportCommit(ctx context.Context, sessionID string) (*transport.ImportResult, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, callErr := t.callTool(ctx, "qrdb_batch_import_commit", map[string]any{"session_id": sessionID})
	if callErr != nil {
		return nil, callErr
	}
	return importResultFromJSON("ImportCommit", text)
}

// ImportAbort discards a chunked import session without applying anything
// by calling the qrdb_batch_import_abort MCP tool.
func (t *Transport) ImportAbort(ctx context.Context, sessionID string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	if _, callErr := t.callTool(ctx, "qrdb_batch_import_abort", map[string]any{"session_id": sessionID}); callErr != nil {
		return callErr
	}
	return nil
}
