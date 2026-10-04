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
	"strconv"

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

// maxMCPImportFileSize bounds the file payload accepted by ImportFile so the
// client fails fast with a clear typed error instead of base64-encoding an
// oversized file only to have the server reject it. Mirrors the 100MB bound
// enforced server-side (see pkg/api/mcp/tools_batch.go maxImportFileSize).
const maxMCPImportFileSize = 100 << 20

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

// ImportFile reads filename's entire contents from data, base64-encodes it,
// and imports it as key-value pairs by calling the qrdb_batch_import MCP
// tool. The active database scope is carried automatically by callTool for
// data-plane tools (see isDataPlaneTool), so no explicit database argument
// is set here.
func (t *Transport) ImportFile(ctx context.Context, filename string, data io.Reader, opts transport.ImportOptions) (*transport.ImportResult, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	file, err := io.ReadAll(data)
	if err != nil {
		return nil, &transport.RequestError{Method: "ImportFile", Err: err}
	}
	if len(file) > maxMCPImportFileSize {
		return nil, &transport.RequestError{
			Method: "ImportFile",
			Err:    &importFileTooLargeError{Size: len(file), Max: maxMCPImportFileSize},
		}
	}

	args := map[string]any{
		"file":          base64.StdEncoding.EncodeToString(file),
		"format":        opts.Format,
		"filename":      filename,
		"key_field":     opts.KeyField,
		"value_mode":    opts.ValueMode,
		"value_field":   opts.ValueField,
		"skip_header":   opts.SkipHeader,
		"transactional": opts.Transactional,
	}

	text, callErr := t.callTool(ctx, "qrdb_batch_import", args)
	if callErr != nil {
		return nil, callErr
	}

	var raw serverImportResult
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "ImportFile", Err: &jsonDecodeError{Err: jsonErr}}
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

// importFileTooLargeError is returned when ImportFile's file exceeds
// maxMCPImportFileSize.
type importFileTooLargeError struct {
	Size int
	Max  int
}

// Error returns a human-readable description.
func (e *importFileTooLargeError) Error() string {
	return "mcp: import file too large: " + strconv.Itoa(e.Size) + " bytes exceeds max " + strconv.Itoa(e.Max) + " bytes"
}
