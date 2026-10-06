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
