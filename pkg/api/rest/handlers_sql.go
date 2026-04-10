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
	"fmt"
	"io"
	"net/http"
	"time"

	sqle "github.com/dolthub/go-mysql-server"
	"github.com/dolthub/go-mysql-server/sql"

	"github.com/jeremyhahn/go-qrdb/pkg/sql/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/provider"
)

// ---------------------------------------------------------------------------
// SQL query request/response types
// ---------------------------------------------------------------------------

// sqlQueryRequest is the JSON payload for POST /v1/sql/query.
type sqlQueryRequest struct {
	Query    string `json:"query"`
	Database string `json:"database"`
}

// sqlQueryResponse is the JSON payload returned by POST /v1/sql/query.
type sqlQueryResponse struct {
	Columns  []string                 `json:"columns"`
	Rows     []map[string]interface{} `json:"rows"`
	RowCount int                      `json:"rowCount"`
	Duration string                   `json:"duration"`
}

// ---------------------------------------------------------------------------
// SQL query handler
// ---------------------------------------------------------------------------

// handleSQLQuery handles POST /v1/sql/query -- executes a SQL query against
// the named database and returns the results as JSON.
func (s *Server) handleSQLQuery(w http.ResponseWriter, r *http.Request) {
	if s.kvSvc == nil || s.databaseSvc == nil {
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
	if req.Database == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "database"})
		return
	}

	// Verify the database exists and retrieve its engine type.
	dbInfo, err := s.databaseSvc.Get(req.Database)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	// Create provider and engine for query execution.
	prov := provider.NewProviderWithEngine(req.Database, s.kvSvc, dbInfo.Engine)
	eng := engine.New(engine.Config{Provider: prov})
	defer func() { _ = eng.Close() }()

	start := time.Now()
	result, err := executeSQLQuery(eng, req.Query, req.Database)
	if err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &SQLQueryError{Query: req.Query, Err: err})
		return
	}
	result.Duration = time.Since(start).String()

	writeJSON(w, result)
}

// executeSQLQuery creates a context, runs the query, and iterates the
// result rows.
func executeSQLQuery(eng *sqle.Engine, query string, database string) (*sqlQueryResponse, error) {
	ctx := sql.NewEmptyContext()
	ctx.SetCurrentDatabase(database)

	schema, iter, _, err := eng.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = iter.Close(ctx) }()

	// Build column names from the schema.
	columns := make([]string, len(schema))
	for i, col := range schema {
		columns[i] = col.Name
	}

	var rows []map[string]interface{}
	for {
		row, err := iter.Next(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		rowMap := make(map[string]interface{}, len(columns))
		for i, col := range columns {
			rowMap[col] = formatSQLValue(row[i])
		}
		rows = append(rows, rowMap)
	}

	if rows == nil {
		rows = []map[string]interface{}{}
	}

	return &sqlQueryResponse{
		Columns:  columns,
		Rows:     rows,
		RowCount: len(rows),
	}, nil
}

// formatSQLValue converts a SQL row cell value to a string suitable for
// JSON serialization. Byte slices are decoded as UTF-8 strings; all
// other types are formatted with fmt.Sprintf.
func formatSQLValue(v interface{}) string {
	switch val := v.(type) {
	case []byte:
		return string(val)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", val)
	}
}
