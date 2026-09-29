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
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sqlquery "github.com/jeremyhahn/go-qrdb/pkg/sql/query"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// newSQLTestServer builds a REST server whose SQL service is the canonical
// pkg/sql/query implementation over the given KV and database mocks.
func newSQLTestServer(kvSvc *mockKVService, dbSvc *mockDatabaseService) *Server {
	return NewServer(kvSvc, newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc),
		WithSQLService(sqlquery.NewService(kvSvc, dbSvc)))
}

// kvEngineGet returns a getFn that reports every database as an open
// KV-engine database.
func kvEngineGet(name string) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{
		Name:    name,
		Engine:  database.EngineKV,
		Backend: database.BackendMemory,
		Status:  database.StatusOpen,
	}, nil
}

func postSQL(srv *Server, path, body string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	return rec
}

// ---------------------------------------------------------------------------
// SQL query handler tests
// ---------------------------------------------------------------------------

func TestHandleSQLQuery_Success(t *testing.T) {
	kvSvc := newDefaultKVSvc()
	dbSvc := newMockDatabaseService()
	dbSvc.getFn = kvEngineGet
	srv := newSQLTestServer(kvSvc, dbSvc)

	rec := postSQL(srv, "/sql/query", `{"query":"SELECT 1 AS val","database":"testdb"}`, nil)
	assertStatus(t, rec, http.StatusOK)

	var resp service.SQLQueryResult
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

func TestHandleSQLQuery_SelectSeededKVData(t *testing.T) {
	kvSvc := newDefaultKVSvc()
	// The SQL data path is scoped to the composite "<tenant>/<db>/" namespace,
	// identical to every other transport (pkg/tenant.ComposeNamespace) — the
	// query names database "testdb" under the default system tenant.
	prefix := tenant.ComposeNamespace(tenantid.System, "testdb")
	kvSvc.scanFn = func(_ context.Context, p string) (map[string][]byte, error) {
		if p != prefix {
			t.Fatalf("scan prefix = %q, want %q", p, prefix)
		}
		return map[string][]byte{prefix + "sql/roundtrip": []byte("sql-visible-value")}, nil
	}
	dbSvc := newMockDatabaseService()
	dbSvc.getFn = kvEngineGet
	srv := newSQLTestServer(kvSvc, dbSvc)

	rec := postSQL(srv, "/sql/query", `{"query":"SELECT * FROM kv","database":"testdb"}`, nil)
	assertStatus(t, rec, http.StatusOK)

	var resp service.SQLQueryResult
	decodeJSON(t, rec, &resp)

	if resp.RowCount != 1 {
		t.Fatalf("rowCount = %d, want 1", resp.RowCount)
	}
	row := resp.Rows[0]
	if row["key"] != "sql/roundtrip" {
		t.Fatalf("row[key] = %v, want %q", row["key"], "sql/roundtrip")
	}
	// The wire carries the value as a plaintext string (the service decodes
	// []byte cells as UTF-8 before JSON encoding), never base64.
	if row["value"] != "sql-visible-value" {
		t.Fatalf("row[value] = %v, want %q", row["value"], "sql-visible-value")
	}
}

func TestHandleSQLQuery_V1MountDispatches(t *testing.T) {
	// The Go database/sql driver posts to /v1/sql/query; both mounts share
	// one handler.
	kvSvc := newDefaultKVSvc()
	dbSvc := newMockDatabaseService()
	dbSvc.getFn = kvEngineGet
	srv := newSQLTestServer(kvSvc, dbSvc)

	rec := postSQL(srv, "/v1/sql/query", `{"query":"SELECT 1 AS val","database":"testdb"}`, nil)
	assertStatus(t, rec, http.StatusOK)
}

func TestHandleSQLQuery_HeaderScopeFallback(t *testing.T) {
	// An empty body database falls back to the X-QRDB-Database header.
	kvSvc := newDefaultKVSvc()
	dbSvc := newMockDatabaseService()
	var resolved string
	dbSvc.getFn = func(name string) (*database.DatabaseInfo, error) {
		resolved = name
		return kvEngineGet(name)
	}
	srv := newSQLTestServer(kvSvc, dbSvc)

	rec := postSQL(srv, "/sql/query", `{"query":"SELECT 1","database":""}`,
		map[string]string{HeaderDatabase: "scoped-db"})
	assertStatus(t, rec, http.StatusOK)

	if resolved != "scoped-db" {
		t.Fatalf("resolved database = %q, want %q", resolved, "scoped-db")
	}
}

func TestHandleSQLQuery_ScopeConflict(t *testing.T) {
	// Body and header naming different databases is ambiguous — reject.
	kvSvc := newDefaultKVSvc()
	dbSvc := newMockDatabaseService()
	dbSvc.getFn = kvEngineGet
	srv := newSQLTestServer(kvSvc, dbSvc)

	rec := postSQL(srv, "/sql/query", `{"query":"SELECT 1","database":"bodydb"}`,
		map[string]string{HeaderDatabase: "headerdb"})
	assertStatus(t, rec, http.StatusBadRequest)

	var resp errorResponse
	decodeJSON(t, rec, &resp)
	if !strings.Contains(resp.Error, "scope conflict") {
		t.Fatalf("error message = %q, want mention of 'scope conflict'", resp.Error)
	}
}

func TestHandleSQLQuery_MatchingHeaderAccepted(t *testing.T) {
	// A header that agrees with the body is not a conflict.
	kvSvc := newDefaultKVSvc()
	dbSvc := newMockDatabaseService()
	dbSvc.getFn = kvEngineGet
	srv := newSQLTestServer(kvSvc, dbSvc)

	rec := postSQL(srv, "/sql/query", `{"query":"SELECT 1","database":"testdb"}`,
		map[string]string{HeaderDatabase: "testdb"})
	assertStatus(t, rec, http.StatusOK)
}

func TestHandleSQLQuery_EmptyQuery(t *testing.T) {
	srv := newSQLTestServer(newDefaultKVSvc(), newMockDatabaseService())

	rec := postSQL(srv, "/sql/query", `{"query":"","database":"testdb"}`, nil)
	assertStatus(t, rec, http.StatusBadRequest)

	var resp errorResponse
	decodeJSON(t, rec, &resp)
	if !strings.Contains(resp.Error, "query") {
		t.Fatalf("error message = %q, want mention of 'query'", resp.Error)
	}
}

func TestHandleSQLQuery_EmptyDatabase(t *testing.T) {
	srv := newSQLTestServer(newDefaultKVSvc(), newMockDatabaseService())

	rec := postSQL(srv, "/sql/query", `{"query":"SELECT 1","database":""}`, nil)
	assertStatus(t, rec, http.StatusBadRequest)

	var resp errorResponse
	decodeJSON(t, rec, &resp)
	if !strings.Contains(resp.Error, "database") {
		t.Fatalf("error message = %q, want mention of 'database'", resp.Error)
	}
}

func TestHandleSQLQuery_InvalidJSON(t *testing.T) {
	srv := newSQLTestServer(newDefaultKVSvc(), newMockDatabaseService())

	rec := postSQL(srv, "/sql/query", "{invalid", nil)
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
	srv := newSQLTestServer(kvSvc, dbSvc)

	rec := postSQL(srv, "/sql/query", `{"query":"SELECT 1","database":"missing-db"}`, nil)
	assertStatus(t, rec, http.StatusNotFound)

	var resp errorResponse
	decodeJSON(t, rec, &resp)
	if !strings.Contains(resp.Error, "missing-db") {
		t.Fatalf("error message = %q, want mention of 'missing-db'", resp.Error)
	}
}

func TestHandleSQLQuery_InvalidSyntax(t *testing.T) {
	kvSvc := newDefaultKVSvc()
	dbSvc := newMockDatabaseService()
	dbSvc.getFn = kvEngineGet
	srv := newSQLTestServer(kvSvc, dbSvc)

	rec := postSQL(srv, "/sql/query", `{"query":"SELECTX BAD","database":"testdb"}`, nil)
	assertStatus(t, rec, http.StatusBadRequest)

	var resp errorResponse
	decodeJSON(t, rec, &resp)
	if !strings.Contains(resp.Error, "sql query failed") {
		t.Fatalf("error message = %q, want mention of 'sql query failed'", resp.Error)
	}
}

func TestHandleSQLQuery_NilService(t *testing.T) {
	// Server created without WithSQLService: the routes are not registered,
	// so dispatch returns 404. The in-handler guard returns 501 when called
	// directly.
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(newMockDatabaseService()))

	rec := postSQL(srv, "/sql/query", `{"query":"SELECT 1","database":"testdb"}`, nil)
	assertStatus(t, rec, http.StatusNotFound)

	req := httptest.NewRequest(http.MethodPost, "/sql/query",
		strings.NewReader(`{"query":"SELECT 1","database":"testdb"}`))
	direct := httptest.NewRecorder()
	srv.handleSQLQuery(direct, req)
	assertStatus(t, direct, http.StatusNotImplemented)

	var resp errorResponse
	decodeJSON(t, direct, &resp)
	if !strings.Contains(resp.Error, "sql") {
		t.Fatalf("error message = %q, want mention of 'sql'", resp.Error)
	}
}

// ---------------------------------------------------------------------------
// sqlQueryRequest serialization
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

// ---------------------------------------------------------------------------
// Wire contract: response JSON casing
// ---------------------------------------------------------------------------

func TestSQLQueryResult_WireCasing(t *testing.T) {
	// The TypeScript SDK consumes columns/rows/rowCount/duration verbatim
	// (its deep snake_case→camelCase response transform leaves these keys
	// unchanged), so the JSON tags must not drift.
	data, err := json.Marshal(&service.SQLQueryResult{
		Columns:  []string{"key"},
		Rows:     []map[string]any{{"key": "k"}},
		RowCount: 1,
		Duration: "1ms",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, field := range []string{`"columns"`, `"rows"`, `"rowCount"`, `"duration"`} {
		if !strings.Contains(string(data), field) {
			t.Fatalf("wire JSON %s missing field %s", data, field)
		}
	}
}
