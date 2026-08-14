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

package transport

import "context"

// SQLQueryResult holds the result of a SQL query execution. The JSON field
// names (columns/rows/rowCount/duration) mirror service.SQLQueryResult, the
// wire contract shared by the REST, gRPC, QUIC, MCP, and Unix transports.
type SQLQueryResult struct {
	// Columns lists the result-set column names in schema order.
	Columns []string `json:"columns"`

	// Rows holds the result rows as column-name to string-value maps.
	Rows []map[string]any `json:"rows"`

	// RowCount is the number of rows returned.
	RowCount int `json:"rowCount"`

	// Duration is the human-readable query execution duration.
	Duration string `json:"duration"`
}

// SQLClient executes ad-hoc SQL queries against a named database over any
// transport.
// Authorization: requires the same permission as a KV range scan
// (permission.KVRange) since SQL query is a read-through over the KV
// range surface.
type SQLClient interface {
	// Query runs query against the named database and returns the result
	// set.
	Query(ctx context.Context, database, query string) (*SQLQueryResult, error)
}

// SQLProvider yields a SQLClient. Every transport (REST, gRPC, QUIC, MCP,
// Unix, embedded) implements this; a transport with no database/SQL
// service configured returns a client whose Query call fails with a typed
// NotConfiguredError rather than nil, so callers get a clear error instead
// of a nil-pointer panic.
type SQLProvider interface {
	SQLClient() SQLClient
}
