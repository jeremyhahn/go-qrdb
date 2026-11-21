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
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Chunked import session handlers
//
// See docs/transactions/architecture.md ("Import session") for the design
// and docs/transactions/api.md for the wire shapes. A chunk's bytes travel
// as the raw request body (not JSON/base64), so the universal pre-auth
// message cap (transport.max_message_bytes) bounds it directly with no
// encoding overhead counted against it; seq is a query parameter for the
// same reason.
// ---------------------------------------------------------------------------

// batchImportBeginResponse is the JSON payload returned by
// POST /batch/import/begin.
type batchImportBeginResponse struct {
	SessionID string `json:"session_id"`
}

// CollectionImportUnsupportedError is returned by POST /batch/import/begin
// when the request names a collection: batch import writes the raw KV
// keyspace only, the same refusal the desktop binding's ImportFile makes.
type CollectionImportUnsupportedError struct {
	Collection string
}

func (e *CollectionImportUnsupportedError) Error() string {
	return "batch: importing into a collection (\"" + e.Collection + "\") is not supported; " +
		"batch import always writes to the raw KV keyspace -- insert documents into the collection instead"
}

// handleBatchImportBegin handles POST /batch/import/begin -- starts a
// chunked import session and returns its ID.
func (s *Server) handleBatchImportBegin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	batchSvc := s.batchSvcFor(w, ctx)
	if batchSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "batch"})
		}
		return
	}

	var body struct {
		Format        string `json:"format"`
		Filename      string `json:"filename"`
		KeyField      string `json:"key_field"`
		ValueMode     string `json:"value_mode"`
		ValueField    string `json:"value_field"`
		SkipHeader    bool   `json:"skip_header"`
		Transactional bool   `json:"transactional"`
		Collection    string `json:"collection"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
			writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
			return
		}
	}
	// Mirrors the desktop binding's ImportFile (ruling 109): batch import
	// writes the raw KV keyspace only, so a collection target is refused
	// rather than silently imported into KV.
	if body.Collection != "" {
		writeErrorJSON(w, http.StatusUnprocessableEntity, &CollectionImportUnsupportedError{Collection: body.Collection})
		return
	}

	opts := service.ImportOptions{
		Format:        body.Format,
		Filename:      body.Filename,
		KeyField:      body.KeyField,
		ValueMode:     body.ValueMode,
		ValueField:    body.ValueField,
		SkipHeader:    body.SkipHeader,
		Transactional: body.Transactional,
	}

	sessionID, err := batchSvc.ImportBegin(ctx, opts)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, batchImportBeginResponse{SessionID: sessionID})
}

// importSessionIDFromPath extracts {session} from a request path of the
// form /batch/import/{session}/{verb}, mirroring handleDatabaseByName's own
// manual path parsing (handlers_database.go) rather than chi.URLParam, for
// the same reason: canonicalPathMiddleware has already normalized r.URL.Path
// by the time any handler sees it.
func importSessionIDFromPath(urlPath, verb string) (sessionID string, ok bool) {
	remainder := strings.TrimPrefix(urlPath, "/batch/import/")
	sessionID, suffix, cut := strings.Cut(remainder, "/")
	if !cut || suffix != verb || sessionID == "" {
		return "", false
	}
	return sessionID, true
}

// handleBatchImportAppend handles
// POST /batch/import/{session}/append?seq=N -- stages the request body as
// chunk seq of {session}.
func (s *Server) handleBatchImportAppend(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	batchSvc := s.batchSvcFor(w, ctx)
	if batchSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "batch"})
		}
		return
	}

	sessionID, ok := importSessionIDFromPath(r.URL.Path, "append")
	if !ok {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "session"})
		return
	}

	seqStr := r.URL.Query().Get("seq")
	seq, err := strconv.ParseUint(seqStr, 10, 64)
	if err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "seq", Err: err})
		return
	}

	chunk, err := io.ReadAll(r.Body)
	if err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	if err := batchSvc.ImportAppend(ctx, sessionID, seq, chunk); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleBatchImportCommit handles POST /batch/import/{session}/commit --
// applies every staged chunk of {session} atomically.
func (s *Server) handleBatchImportCommit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	batchSvc := s.batchSvcFor(w, ctx)
	if batchSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "batch"})
		}
		return
	}

	sessionID, ok := importSessionIDFromPath(r.URL.Path, "commit")
	if !ok {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "session"})
		return
	}

	result, err := batchSvc.ImportCommit(ctx, sessionID)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	importErrors := make([]batchImportErrorJSON, len(result.Errors))
	for i, e := range result.Errors {
		importErrors[i] = batchImportErrorJSON{Row: e.Row, Key: e.Key, Message: e.Message}
	}

	writeJSON(w, batchImportResponse{
		Total:     result.Total,
		Succeeded: result.Succeeded,
		Failed:    result.Failed,
		Errors:    importErrors,
	})
}

// handleBatchImportAbort handles POST /batch/import/{session}/abort --
// discards {session} and everything staged under it.
func (s *Server) handleBatchImportAbort(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	batchSvc := s.batchSvcFor(w, ctx)
	if batchSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "batch"})
		}
		return
	}

	sessionID, ok := importSessionIDFromPath(r.URL.Path, "abort")
	if !ok {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "session"})
		return
	}

	if err := batchSvc.ImportAbort(ctx, sessionID); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
