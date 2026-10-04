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
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"

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

// ImportFile streams a CSV/JSON/archive file to POST /batch/import as a
// multipart form and returns the per-row import result. The active
// database scope is carried by the transport's data-plane header
// round-tripper (/batch is a data-plane prefix), so no explicit database
// field is sent on the wire.
func (t *Transport) ImportFile(ctx context.Context, filename string, data io.Reader, opts transport.ImportOptions) (*transport.ImportResult, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)

	// The multipart filename must be non-empty: Go's server-side
	// ParseMultipartForm treats a part whose Content-Disposition carries an
	// empty filename as a form VALUE rather than a FILE, so r.FormFile
	// would not find it. Stdin imports pass an empty filename, so default
	// it here. The value is only used server-side for "auto" format
	// detection, which explicit-format callers bypass.
	partName := filename
	if partName == "" {
		partName = "upload"
	}
	fw, err := mw.CreateFormFile("file", partName)
	if err != nil {
		return nil, &transport.RequestError{Method: "ImportFile", Err: err}
	}
	if _, err := io.Copy(fw, data); err != nil {
		return nil, &transport.RequestError{Method: "ImportFile", Err: err}
	}

	fields := map[string]string{
		"format":        opts.Format,
		"keyField":      opts.KeyField,
		"valueMode":     opts.ValueMode,
		"valueField":    opts.ValueField,
		"skipHeader":    strconv.FormatBool(opts.SkipHeader),
		"transactional": strconv.FormatBool(opts.Transactional),
	}
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			return nil, &transport.RequestError{Method: "ImportFile", Err: err}
		}
	}
	if err := mw.Close(); err != nil {
		return nil, &transport.RequestError{Method: "ImportFile", Err: err}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/batch/import", &body)
	if err != nil {
		return nil, &transport.RequestError{Method: "ImportFile", Err: err}
	}
	httpReq.Header.Set("Content-Type", mw.FormDataContentType())

	httpResp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "ImportFile", Err: err}
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode >= 400 {
		return nil, t.decodeErrorResponse("ImportFile", httpResp)
	}

	var wire batchImportWireResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&wire); err != nil {
		return nil, &transport.RequestError{Method: "ImportFile", Err: &jsonDecodeError{Err: err}}
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
