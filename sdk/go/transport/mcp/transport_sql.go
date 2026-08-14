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

package mcp

import (
	"context"
	"encoding/json"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertions.
var (
	_ transport.SQLClient   = (*Transport)(nil)
	_ transport.SQLProvider = (*Transport)(nil)
)

// Query executes an ad-hoc SQL query against a named database via the
// qrdb_sql_query tool. The tool marshals service.SQLQueryResult directly,
// whose JSON tags (columns/rows/rowCount/duration) match transport.SQLQueryResult.
func (t *Transport) Query(ctx context.Context, database, query string) (*transport.SQLQueryResult, error) {
	text, err := t.callTool(ctx, "qrdb_sql_query", map[string]any{
		"query":    query,
		"database": database,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "Query", Err: err}
	}

	var result transport.SQLQueryResult
	if jsonErr := json.Unmarshal([]byte(text), &result); jsonErr != nil {
		return nil, &transport.RequestError{Method: "Query", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return &result, nil
}

// SQLClient returns the MCP transport itself, which implements
// transport.SQLClient directly.
func (t *Transport) SQLClient() transport.SQLClient {
	return t
}
