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
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Batch request/response types
// ---------------------------------------------------------------------------

// batchRequest is the JSON payload for POST /batch.
type batchRequest struct {
	Ops []opJSON `json:"ops"`
}

// batchResponse is the JSON payload returned by POST /batch.
type batchResponse struct {
	Responses []opResponseJSON `json:"responses"`
}

// ---------------------------------------------------------------------------
// Batch handler
// ---------------------------------------------------------------------------

// handleBatch handles POST /batch -- executes multiple operations atomically.
// All operations in the batch succeed or fail together within a single MVCC
// revision.
func (s *Server) handleBatch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	batchSvc := s.batchSvcFor(w, ctx)
	if batchSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "batch"})
		}
		return
	}

	var req batchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	ops := jsonToOps(req.Ops)

	results, err := batchSvc.Execute(ctx, ops)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	responses := make([]opResponseJSON, len(results))
	for i, opResp := range results {
		responses[i] = opResponseJSON{
			Kvs:       kvsToResponse(opResp.Kvs),
			Count:     opResp.Count,
			Succeeded: opResp.Succeeded,
		}
	}

	writeJSON(w, batchResponse{Responses: responses})
}

// ---------------------------------------------------------------------------
// Batch import types
// ---------------------------------------------------------------------------

// batchImportResponse is the JSON payload returned by POST /batch/import.
type batchImportResponse struct {
	Total     int                    `json:"total"`
	Succeeded int                    `json:"succeeded"`
	Failed    int                    `json:"failed"`
	Errors    []batchImportErrorJSON `json:"errors,omitempty"`
}

// batchImportErrorJSON represents a single import error in the JSON response.
type batchImportErrorJSON struct {
	Row     int    `json:"row"`
	Key     string `json:"key"`
	Message string `json:"message"`
}

// ---------------------------------------------------------------------------
// Batch import handler
// ---------------------------------------------------------------------------

// maxImportFileSize is the maximum size for imported files (100MB).
const maxImportFileSize = 100 << 20

// handleBatchImport handles POST /batch/import -- imports CSV, JSON, or
// archive files containing key-value data.
func (s *Server) handleBatchImport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	batchSvc := s.batchSvcFor(w, ctx)
	if batchSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "batch"})
		}
		return
	}

	// Limit request body size before parsing to prevent memory exhaustion.
	r.Body = http.MaxBytesReader(w, r.Body, maxImportFileSize)

	// Parse multipart form with 100MB max (body already bounded by MaxBytesReader above).
	if err := r.ParseMultipartForm(maxImportFileSize); err != nil { // #nosec G120 -- bounded by MaxBytesReader on line 113
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	// Extract file.
	file, _, err := r.FormFile("file")
	if err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "file"})
		return
	}
	defer func() { _ = file.Close() }()

	// Extract and validate format parameter.
	format := r.FormValue("format")
	if format == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "format"})
		return
	}
	switch format {
	case "csv", "json", "archive":
	default:
		writeErrorJSON(w, http.StatusBadRequest, &service.ImportInvalidFormatError{Format: format})
		return
	}

	// Extract and validate keyField parameter.
	keyField := r.FormValue("keyField")
	if keyField == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "keyField"})
		return
	}

	// Extract and validate valueMode parameter.
	valueMode := r.FormValue("valueMode")
	if valueMode == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "valueMode"})
		return
	}
	switch valueMode {
	case "field", "row":
	default:
		writeErrorJSON(w, http.StatusBadRequest, &service.ImportInvalidValueModeError{Mode: valueMode})
		return
	}

	// Extract valueField (required if valueMode is "field").
	valueField := r.FormValue("valueField")
	if valueMode == "field" && valueField == "" {
		writeErrorJSON(w, http.StatusBadRequest, &service.ImportMissingValueFieldError{})
		return
	}

	// Parse optional skipHeader parameter (default: true).
	skipHeader := true
	if skipHeaderStr := r.FormValue("skipHeader"); skipHeaderStr != "" {
		var parseErr error
		skipHeader, parseErr = strconv.ParseBool(skipHeaderStr)
		if parseErr != nil {
			writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "skipHeader", Err: parseErr})
			return
		}
	}

	// Parse optional transactional parameter (default: false).
	transactional := false
	if txStr := r.FormValue("transactional"); txStr != "" {
		var parseErr error
		transactional, parseErr = strconv.ParseBool(txStr)
		if parseErr != nil {
			writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "transactional", Err: parseErr})
			return
		}
	}

	opts := service.ImportOptions{
		Format:        format,
		KeyField:      keyField,
		ValueMode:     valueMode,
		ValueField:    valueField,
		SkipHeader:    skipHeader,
		Transactional: transactional,
	}

	result, err := batchSvc.Import(ctx, file, opts)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	// Convert service errors to JSON response format.
	importErrors := make([]batchImportErrorJSON, len(result.Errors))
	for i, e := range result.Errors {
		importErrors[i] = batchImportErrorJSON{
			Row:     e.Row,
			Key:     e.Key,
			Message: e.Message,
		}
	}

	writeJSON(w, batchImportResponse{
		Total:     result.Total,
		Succeeded: result.Succeeded,
		Failed:    result.Failed,
		Errors:    importErrors,
	})
}
