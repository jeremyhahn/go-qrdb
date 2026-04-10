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
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

// ---------------------------------------------------------------------------
// SQL query handler tests
// ---------------------------------------------------------------------------

func TestHandleSQLQuery_Success(t *testing.T) {
	kvSvc := newDefaultKVSvc()
	dbSvc := newMockDatabaseService()
	dbSvc.getFn = func(name string) (*database.DatabaseInfo, error) {
		return &database.DatabaseInfo{
			Name:    name,
			Engine:  database.EngineKV,
			Backend: database.BackendMemory,
			Status:  database.StatusOpen,
		}, nil
	}

	srv := NewServer(kvSvc, newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))

	body := `{"query":"SELECT 1 AS val","database":"testdb"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/sql/query", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp sqlQueryResponse
	decodeJSON(t, rec, &resp)

	if len(resp.Columns) == 0 {
		t.Fatal("expected at least one column in response")
	}
	if resp.Columns[0] != "val" {
		t.Fatalf("columns[0] = %q, want %q", resp.Columns[0], "val")
	}
	if resp.RowCount != 1 {
		t.Fatalf("rowCount = %d, want 1", resp.RowCount)
	}
	if resp.Duration == "" {
		t.Fatal("expected non-empty duration")
	}
	if len(resp.Rows) != 1 {
		t.Fatalf("rows length = %d, want 1", len(resp.Rows))
	}
	if resp.Rows[0]["val"] != "1" {
		t.Fatalf("rows[0][val] = %v, want %q", resp.Rows[0]["val"], "1")
	}
}

func TestHandleSQLQuery_EmptyQuery(t *testing.T) {
	kvSvc := newDefaultKVSvc()
	dbSvc := newMockDatabaseService()

	srv := NewServer(kvSvc, newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))

	body := `{"query":"","database":"testdb"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/sql/query", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)

	var resp errorResponse
	decodeJSON(t, rec, &resp)
	if !strings.Contains(resp.Error, "query") {
		t.Fatalf("error message = %q, want mention of 'query'", resp.Error)
	}
}

func TestHandleSQLQuery_EmptyDatabase(t *testing.T) {
	kvSvc := newDefaultKVSvc()
	dbSvc := newMockDatabaseService()

	srv := NewServer(kvSvc, newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))

	body := `{"query":"SELECT 1","database":""}`
	req := httptest.NewRequest(http.MethodPost, "/v1/sql/query", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)

	var resp errorResponse
	decodeJSON(t, rec, &resp)
	if !strings.Contains(resp.Error, "database") {
		t.Fatalf("error message = %q, want mention of 'database'", resp.Error)
	}
}

func TestHandleSQLQuery_InvalidJSON(t *testing.T) {
	kvSvc := newDefaultKVSvc()
	dbSvc := newMockDatabaseService()

	srv := NewServer(kvSvc, newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))

	req := httptest.NewRequest(http.MethodPost, "/v1/sql/query", strings.NewReader("{invalid"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)

	var resp errorResponse
	decodeJSON(t, rec, &resp)
	if resp.Error == "" {
		t.Fatal("expected non-empty error message")
	}
}

func TestHandleSQLQuery_DatabaseNotFound(t *testing.T) {
	kvSvc := newDefaultKVSvc()
	dbSvc := newMockDatabaseService()
	dbSvc.getFn = func(name string) (*database.DatabaseInfo, error) {
		return nil, &database.NotFoundError{Name: name}
	}

	srv := NewServer(kvSvc, newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))

	body := `{"query":"SELECT 1","database":"missing-db"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/sql/query", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)

	var resp errorResponse
	decodeJSON(t, rec, &resp)
	if !strings.Contains(resp.Error, "missing-db") {
		t.Fatalf("error message = %q, want mention of 'missing-db'", resp.Error)
	}
}

func TestHandleSQLQuery_NilService(t *testing.T) {
	// Server created without WithDatabaseService, so databaseSvc is nil.
	// The SQL route is only registered when both kvSvc and databaseSvc are
	// non-nil. When databaseSvc is nil, /v1/sql/query is not in the route
	// table, so dispatch returns 404. We also test the guard inside
	// handleSQLQuery directly by calling it on a server with nil services.
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	// Call handleSQLQuery directly to test the nil-service guard.
	body := `{"query":"SELECT 1","database":"testdb"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/sql/query", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.handleSQLQuery(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)

	var resp errorResponse
	decodeJSON(t, rec, &resp)
	if !strings.Contains(resp.Error, "sql") {
		t.Fatalf("error message = %q, want mention of 'sql'", resp.Error)
	}
}

func TestHandleSQLQuery_NilKVService(t *testing.T) {
	// Server with nil kvSvc but non-nil databaseSvc -- handleSQLQuery
	// should return 501 because kvSvc == nil.
	dbSvc := newMockDatabaseService()
	srv := &Server{
		kvSvc:       nil,
		databaseSvc: dbSvc,
		adminSvc:    newDefaultAdminSvc(),
	}

	body := `{"query":"SELECT 1","database":"testdb"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/sql/query", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.handleSQLQuery(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

// ---------------------------------------------------------------------------
// formatSQLValue tests
// ---------------------------------------------------------------------------

func TestFormatSQLValue_ByteSlice(t *testing.T) {
	result := formatSQLValue([]byte("hello"))
	if result != "hello" {
		t.Fatalf("formatSQLValue([]byte) = %q, want %q", result, "hello")
	}
}

func TestFormatSQLValue_Nil(t *testing.T) {
	result := formatSQLValue(nil)
	if result != "" {
		t.Fatalf("formatSQLValue(nil) = %q, want %q", result, "")
	}
}

func TestFormatSQLValue_Int(t *testing.T) {
	result := formatSQLValue(42)
	if result != "42" {
		t.Fatalf("formatSQLValue(42) = %q, want %q", result, "42")
	}
}

func TestFormatSQLValue_String(t *testing.T) {
	result := formatSQLValue("test")
	if result != "test" {
		t.Fatalf("formatSQLValue(string) = %q, want %q", result, "test")
	}
}

// ---------------------------------------------------------------------------
// sqlQueryRequest / sqlQueryResponse serialization
// ---------------------------------------------------------------------------

func TestSQLQueryRequest_JSON(t *testing.T) {
	input := `{"query":"SELECT * FROM t","database":"mydb"}`
	var req sqlQueryRequest
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.Query != "SELECT * FROM t" {
		t.Fatalf("query = %q, want %q", req.Query, "SELECT * FROM t")
	}
	if req.Database != "mydb" {
		t.Fatalf("database = %q, want %q", req.Database, "mydb")
	}
}

func TestSQLQueryResponse_JSON(t *testing.T) {
	resp := sqlQueryResponse{
		Columns:  []string{"id", "name"},
		Rows:     []map[string]interface{}{{"id": "1", "name": "test"}},
		RowCount: 1,
		Duration: "100ms",
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded sqlQueryResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.RowCount != 1 {
		t.Fatalf("rowCount = %d, want 1", decoded.RowCount)
	}
	if decoded.Duration != "100ms" {
		t.Fatalf("duration = %q, want %q", decoded.Duration, "100ms")
	}
	if len(decoded.Columns) != 2 {
		t.Fatalf("columns length = %d, want 2", len(decoded.Columns))
	}
}
