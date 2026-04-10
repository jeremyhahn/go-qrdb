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
// MVCC response types
// ---------------------------------------------------------------------------

// rangeResponse is the JSON payload returned by GET /kv/range.
type rangeResponse struct {
	Kvs      []*kvResponse `json:"kvs"`
	Count    int64         `json:"count"`
	Revision int64         `json:"revision,omitempty"`
}

// revisionResponse is the JSON payload returned by GET /kv/revision.
type revisionResponse struct {
	Revision int64 `json:"revision"`
}

// compactRequest is the JSON payload for POST /kv/compact.
type compactRequest struct {
	Revision int64 `json:"revision"`
}

// ---------------------------------------------------------------------------
// Range handler
// ---------------------------------------------------------------------------

// handleRange handles GET /kv/range -- retrieves key-value pairs in a range.
// Query parameters:
//   - start: range start key (required)
//   - end: range end key (optional, single key when absent)
//   - limit: maximum number of results
//   - revision: read at historical revision
//   - count-only: return count without values (true/false)
//   - keys-only: return keys without values (true/false)
//   - sort-order: none, ascend, descend
//   - sort-target: key, version, create_revision, mod_revision, value
func (s *Server) handleRange(w http.ResponseWriter, r *http.Request) {
	if s.mvccSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "mvcc"})
		return
	}

	q := r.URL.Query()

	startStr := q.Get("start")
	if startStr == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "start"})
		return
	}
	start := []byte(startStr)

	var end []byte
	if endStr := q.Get("end"); endStr != "" {
		end = []byte(endStr)
	}

	var opts []service.RangeOption

	if limitStr := q.Get("limit"); limitStr != "" {
		limit, err := strconv.ParseInt(limitStr, 10, 64)
		if err != nil {
			writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "limit", Err: err})
			return
		}
		opts = append(opts, service.WithLimit(limit))
	}

	if revStr := q.Get("revision"); revStr != "" {
		rev, err := strconv.ParseInt(revStr, 10, 64)
		if err != nil {
			writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "revision", Err: err})
			return
		}
		opts = append(opts, service.WithRevision(rev))
	}

	if q.Get("count-only") == "true" {
		opts = append(opts, service.WithCountOnly())
	}

	if q.Get("keys-only") == "true" {
		opts = append(opts, service.WithKeysOnly())
	}

	sortOrder := parseSortOrder(q.Get("sort-order"))
	sortTarget := parseSortTarget(q.Get("sort-target"))
	if sortOrder != service.SortNone || sortTarget != service.SortByKey {
		opts = append(opts, service.WithSort(sortOrder, sortTarget))
	}

	kvs, count, err := s.mvccSvc.Range(r.Context(), start, end, opts...)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, rangeResponse{
		Kvs:   kvsToResponse(kvs),
		Count: count,
	})
}

// ---------------------------------------------------------------------------
// Compact handler
// ---------------------------------------------------------------------------

// handleCompact handles POST /kv/compact -- compacts MVCC revisions.
func (s *Server) handleCompact(w http.ResponseWriter, r *http.Request) {
	if s.mvccSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "mvcc"})
		return
	}

	var req compactRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	if err := s.mvccSvc.Compact(r.Context(), req.Revision); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, okResponse{Status: "ok"})
}

// ---------------------------------------------------------------------------
// Current revision handler
// ---------------------------------------------------------------------------

// handleCurrentRevision handles GET /kv/revision -- returns the current
// MVCC revision.
func (s *Server) handleCurrentRevision(w http.ResponseWriter, r *http.Request) {
	if s.mvccSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "mvcc"})
		return
	}

	rev, err := s.mvccSvc.CurrentRevision(r.Context())
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, revisionResponse{Revision: rev})
}

// ---------------------------------------------------------------------------
// Sort parsing helpers
// ---------------------------------------------------------------------------

// sortOrderMap maps query parameter string values to SortOrder constants.
var sortOrderMap = map[string]service.SortOrder{
	"":        service.SortNone,
	"none":    service.SortNone,
	"ascend":  service.SortAscend,
	"descend": service.SortDescend,
}

// sortTargetMap maps query parameter string values to SortTarget constants.
var sortTargetMap = map[string]service.SortTarget{
	"":                service.SortByKey,
	"key":             service.SortByKey,
	"version":         service.SortByVersion,
	"create_revision": service.SortByCreateRevision,
	"mod_revision":    service.SortByModRevision,
	"value":           service.SortByValue,
}

// parseSortOrder converts a query parameter string to a SortOrder constant.
func parseSortOrder(s string) service.SortOrder {
	if order, ok := sortOrderMap[s]; ok {
		return order
	}
	return service.SortNone
}

// parseSortTarget converts a query parameter string to a SortTarget constant.
func parseSortTarget(s string) service.SortTarget {
	if target, ok := sortTargetMap[s]; ok {
		return target
	}
	return service.SortByKey
}
