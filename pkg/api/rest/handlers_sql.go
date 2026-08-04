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

// sqlQueryRequest is the JSON payload for POST /sql/query (and its
// /v1/sql/query mount). The database field is the canonical database
// selector: it is what the TypeScript SDK's SQLClient and the Go
// database/sql driver transmit.
type sqlQueryRequest struct {
	Query    string `json:"query"`
	Database string `json:"database"`
}

// handleSQLQuery handles POST /sql/query and POST /v1/sql/query --
// executes a SQL query against the named database and returns the results
// as JSON. The response shape (columns/rows/rowCount/duration, produced by
// service.SQLQueryResult) is the wire contract consumed by the TypeScript
// SDK and mirrors the Wails desktop binding.
//
// Database resolution: the request body's database field is canonical.
// When the request also carries an X-QRDB-Database scope header the two
// must agree; a conflict is rejected with 400 rather than silently
// preferring one. A body without a database falls back to the header.
func (s *Server) handleSQLQuery(w http.ResponseWriter, r *http.Request) {
	if s.sqlSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "sql"})
		return
	}

	var req sqlQueryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	if req.Query == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "query"})
		return
	}

	dbName := req.Database
	if headerDB := r.Header.Get(HeaderDatabase); headerDB != "" {
		switch {
		case dbName == "":
			dbName = headerDB
		case dbName != headerDB:
			writeErrorJSON(w, http.StatusBadRequest,
				&DatabaseScopeConflictError{Body: req.Database, Header: headerDB})
			return
		}
	}
	if dbName == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "database"})
		return
	}

	result, err := s.sqlSvc.Execute(r.Context(), dbName, req.Query)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, result)
}
