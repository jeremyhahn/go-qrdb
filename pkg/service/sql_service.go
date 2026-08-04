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

package service

import "context"

// SQLQueryResult holds the result of a SQL query execution. The JSON field
// names (columns/rows/rowCount/duration) are the wire contract consumed by
// the TypeScript SDK's SQLQueryResult type over both REST and the Wails
// desktop IPC, so they must not change casing.
type SQLQueryResult struct {
	// Columns lists the result-set column names in schema order.
	Columns []string `json:"columns"`

	// Rows holds the result rows as column-name to string-value maps.
	// All cell values are pre-formatted as strings ([]byte cells are
	// decoded as UTF-8, nil cells are empty strings).
	Rows []map[string]any `json:"rows"`

	// RowCount is the number of rows returned.
	RowCount int `json:"rowCount"`

	// Duration is the human-readable query execution duration.
	Duration string `json:"duration"`
}

// SQLServicer executes ad-hoc SQL queries against a named database. It is
// the transport-neutral seam shared by the REST API handler and the Wails
// desktop SQL binding; the canonical implementation lives in pkg/sql/query
// (it cannot live here because pkg/sql/provider imports this package).
type SQLServicer interface {
	// Execute runs query against the named database and returns the
	// result set. Returns a *SQLEmptyQueryError or *SQLEmptyDatabaseError
	// when the corresponding argument is empty, the database resolver's
	// typed error when the database does not exist, and a
	// *SQLExecutionError when the SQL engine rejects or fails the query.
	Execute(ctx context.Context, database, query string) (*SQLQueryResult, error)
}

// SQLEmptyQueryError is returned when a SQL execution request carries an
// empty query string.
type SQLEmptyQueryError struct{}

// Error returns a human-readable description.
func (e *SQLEmptyQueryError) Error() string {
	return "service: sql query must not be empty"
}

// SQLEmptyDatabaseError is returned when a SQL execution request carries an
// empty database name.
type SQLEmptyDatabaseError struct{}

// Error returns a human-readable description.
func (e *SQLEmptyDatabaseError) Error() string {
	return "service: sql database must not be empty"
}

// SQLExecutionError is returned when the SQL engine fails to parse,
// analyze, or execute a query.
type SQLExecutionError struct {
	Query string
	Err   error
}

// Error returns a human-readable description.
func (e *SQLExecutionError) Error() string {
	return "service: sql query failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *SQLExecutionError) Unwrap() error {
	return e.Err
}
