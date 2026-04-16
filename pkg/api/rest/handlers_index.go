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

	"github.com/jeremyhahn/go-qrdb/pkg/index"
)

// ---------------------------------------------------------------------------
// Index request/response types
// ---------------------------------------------------------------------------

// indexCreateRequest is the JSON payload for POST /index/create.
type indexCreateRequest struct {
	Name      string `json:"name"`
	FieldName string `json:"field_name"`
	Unique    bool   `json:"unique"`
	Sparse    bool   `json:"sparse"`
}

// indexDropRequest is the JSON payload for POST /index/drop.
type indexDropRequest struct {
	Name string `json:"name"`
}

// indexQueryResponse is the JSON payload returned by GET /index/query.
type indexQueryResponse struct {
	Keys []string `json:"keys"`
}

// indexScanResponse is the JSON payload returned by GET /index/scan.
type indexScanResponse struct {
	Entries map[string][]string `json:"entries"`
}

// indexListResponse is the JSON payload returned by GET /index/list.
type indexListResponse struct {
	Names []string `json:"names"`
}

// ---------------------------------------------------------------------------
// Index handlers
// ---------------------------------------------------------------------------

// handleIndexCreate handles POST /index/create -- creates a new secondary
// index with the specified configuration.
func (s *Server) handleIndexCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	indexSvc := s.indexSvcFor(w, ctx)
	if indexSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "index"})
		}
		return
	}

	var req indexCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	config := index.IndexConfig{
		Unique: req.Unique,
		Sparse: req.Sparse,
	}

	if err := indexSvc.CreateIndex(ctx, req.Name, req.FieldName, config); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, okResponse{Status: "ok"})
}

// handleIndexDrop handles POST /index/drop -- removes a secondary index
// by name.
func (s *Server) handleIndexDrop(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	indexSvc := s.indexSvcFor(w, ctx)
	if indexSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "index"})
		}
		return
	}

	var req indexDropRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	if err := indexSvc.DropIndex(ctx, req.Name); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, okResponse{Status: "ok"})
}

// handleIndexQuery handles GET /index/query -- returns keys matching
// an exact value in the specified index.
// Query parameters: index_name (required), value (required).
func (s *Server) handleIndexQuery(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	indexSvc := s.indexSvcFor(w, ctx)
	if indexSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "index"})
		}
		return
	}

	q := r.URL.Query()

	indexName := q.Get("index_name")
	if indexName == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "index_name"})
		return
	}

	value := q.Get("value")
	if value == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "value"})
		return
	}

	keys, err := indexSvc.Query(ctx, indexName, []byte(value))
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	stringKeys := make([]string, len(keys))
	for i, k := range keys {
		stringKeys[i] = string(k)
	}

	writeJSON(w, indexQueryResponse{Keys: stringKeys})
}

// handleIndexScan handles GET /index/scan -- returns entries in the specified
// index within the value range [start, end).
// Query parameters: index_name (required), start (required), end (required).
func (s *Server) handleIndexScan(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	indexSvc := s.indexSvcFor(w, ctx)
	if indexSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "index"})
		}
		return
	}

	q := r.URL.Query()

	indexName := q.Get("index_name")
	if indexName == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "index_name"})
		return
	}

	start := q.Get("start")
	if start == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "start"})
		return
	}

	end := q.Get("end")
	if end == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "end"})
		return
	}

	entries, err := indexSvc.Scan(ctx, indexName, []byte(start), []byte(end))
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	stringEntries := make(map[string][]string, len(entries))
	for val, keys := range entries {
		skeys := make([]string, len(keys))
		for i, k := range keys {
			skeys[i] = string(k)
		}
		stringEntries[val] = skeys
	}

	writeJSON(w, indexScanResponse{Entries: stringEntries})
}

// handleIndexList handles GET /index/list -- returns the names of all
// registered secondary indexes.
func (s *Server) handleIndexList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	indexSvc := s.indexSvcFor(w, ctx)
	if indexSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "index"})
		}
		return
	}

	names, err := indexSvc.ListIndexes(ctx)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, indexListResponse{Names: names})
}
