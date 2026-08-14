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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// mcpSQLServiceMock is a minimal service.SQLServicer test double. Defined
// locally (not in a build-tag-gated mocks file) so these tests compile and
// run under both plain and full tag sets.
type mcpSQLServiceMock struct {
	result          *service.SQLQueryResult
	err             error
	gotDB, gotQuery string
}

func (m *mcpSQLServiceMock) Execute(_ context.Context, database, query string) (*service.SQLQueryResult, error) {
	m.gotDB, m.gotQuery = database, query
	if m.err != nil {
		return nil, m.err
	}
	if m.result != nil {
		return m.result, nil
	}
	return &service.SQLQueryResult{
		Columns:  []string{"val"},
		Rows:     []map[string]any{{"val": "1"}},
		RowCount: 1,
		Duration: "1ms",
	}, nil
}

func TestHandleSQLQuery_MCP_Success(t *testing.T) {
	mock := &mcpSQLServiceMock{}
	handler := handleSQLQuery(mock)

	req := buildCallToolRequest(t, "qrdb_sql_query", map[string]any{
		"query":    "SELECT 1 AS val",
		"database": "testdb",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleSQLQuery: %v", err)
	}

	var resp service.SQLQueryResult
	if jsonErr := json.Unmarshal([]byte(resultText(t, result)), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if len(resp.Columns) != 1 || resp.Columns[0] != "val" {
		t.Fatalf("unexpected columns: %v", resp.Columns)
	}
	if resp.RowCount != 1 {
		t.Fatalf("rowCount = %d, want 1", resp.RowCount)
	}
	if mock.gotDB != "testdb" || mock.gotQuery != "SELECT 1 AS val" {
		t.Fatalf("service not invoked with expected args: db=%q query=%q", mock.gotDB, mock.gotQuery)
	}
}

func TestHandleSQLQuery_MCP_MissingQuery(t *testing.T) {
	handler := handleSQLQuery(&mcpSQLServiceMock{})
	req := buildCallToolRequest(t, "qrdb_sql_query", map[string]any{"database": "db"})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for missing query")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("expected *ToolError, got %T: %v", err, err)
	}
}

func TestHandleSQLQuery_MCP_MissingDatabase(t *testing.T) {
	handler := handleSQLQuery(&mcpSQLServiceMock{})
	req := buildCallToolRequest(t, "qrdb_sql_query", map[string]any{"query": "SELECT 1"})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for missing database")
	}
}

func TestHandleSQLQuery_MCP_ServiceError(t *testing.T) {
	mock := &mcpSQLServiceMock{err: &service.SQLExecutionError{Query: "bad", Err: context.DeadlineExceeded}}
	handler := handleSQLQuery(mock)
	req := buildCallToolRequest(t, "qrdb_sql_query", map[string]any{"query": "bad", "database": "db"})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from failing SQL execution")
	}
}

func TestSQLTools_Registration(t *testing.T) {
	tools := sqlTools(&mcpSQLServiceMock{})
	if len(tools) != 1 {
		t.Fatalf("expected 1 SQL tool, got %d", len(tools))
	}
	if tools[0].Tool.Name != "qrdb_sql_query" {
		t.Fatalf("unexpected tool name: %q", tools[0].Tool.Name)
	}
}
