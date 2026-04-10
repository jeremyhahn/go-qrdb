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

package driver

import (
	"context"
	"database/sql"
	sqld "database/sql/driver"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newMockSQLServer creates an httptest.Server that responds to POST /v1/sql/query.
func newMockSQLServer(t *testing.T, handler func(req *sqlRequest, w http.ResponseWriter)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/sql/query" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var req sqlRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		handler(&req, w)
	}))
}

// newNetworkConnFromServer returns a networkConn pointed at the test server.
func newNetworkConnFromServer(srv *httptest.Server, database string) *networkConn {
	return &networkConn{
		client:   srv.Client(),
		baseURL:  srv.URL,
		database: database,
	}
}

// TestNetworkConn_QueryContext_Success verifies QueryContext sends correct JSON and parses rows.
func TestNetworkConn_QueryContext_Success(t *testing.T) {
	var receivedReq *sqlRequest

	srv := newMockSQLServer(t, func(req *sqlRequest, w http.ResponseWriter) {
		receivedReq = req
		resp := sqlResponse{
			Columns:  []string{"id", "name"},
			Rows:     []map[string]interface{}{{"id": float64(1), "name": "alice"}},
			RowCount: 1,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	defer srv.Close()

	conn := newNetworkConnFromServer(srv, "testdb")

	rows, err := conn.QueryContext(context.Background(), "SELECT id, name FROM users", nil)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	defer func() { _ = rows.Close() }()

	if receivedReq == nil {
		t.Fatal("server did not receive a request")
	}
	if receivedReq.Database != "testdb" {
		t.Errorf("Database = %q, want testdb", receivedReq.Database)
	}
	if receivedReq.Query != "SELECT id, name FROM users" {
		t.Errorf("Query = %q, want SELECT ...", receivedReq.Query)
	}

	cols := rows.Columns()
	if len(cols) != 2 || cols[0] != "id" || cols[1] != "name" {
		t.Errorf("Columns() = %v, want [id name]", cols)
	}
}

// TestNetworkConn_ExecContext_Success verifies ExecContext sends correct JSON and returns result.
func TestNetworkConn_ExecContext_Success(t *testing.T) {
	srv := newMockSQLServer(t, func(req *sqlRequest, w http.ResponseWriter) {
		resp := sqlResponse{RowCount: 2}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	defer srv.Close()

	conn := newNetworkConnFromServer(srv, "testdb")

	res, err := conn.ExecContext(context.Background(), "DELETE FROM t WHERE x = 1", nil)
	if err != nil {
		t.Fatalf("ExecContext: %v", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		t.Fatalf("RowsAffected: %v", err)
	}
	if rows != 2 {
		t.Errorf("RowsAffected = %d, want 2", rows)
	}
}

// TestNetworkConn_ErrorResponse_NonOK verifies non-200 response returns NetworkRequestError.
func TestNetworkConn_ErrorResponse_NonOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal server error"}`))
	}))
	defer srv.Close()

	conn := newNetworkConnFromServer(srv, "testdb")

	_, err := conn.QueryContext(context.Background(), "SELECT 1", nil)
	if err == nil {
		t.Fatal("expected error for non-200 response")
	}

	var nre *NetworkRequestError
	if !errors.As(err, &nre) {
		t.Errorf("expected *NetworkRequestError, got %T: %v", err, err)
	}
	if nre.StatusCode != http.StatusInternalServerError {
		t.Errorf("StatusCode = %d, want %d", nre.StatusCode, http.StatusInternalServerError)
	}
}

// TestNetworkConn_ErrorResponse_PlainText verifies non-JSON error body is wrapped.
func TestNetworkConn_ErrorResponse_PlainText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("plain text error"))
	}))
	defer srv.Close()

	conn := newNetworkConnFromServer(srv, "testdb")

	_, err := conn.ExecContext(context.Background(), "BAD SQL", nil)
	if err == nil {
		t.Fatal("expected error for non-200 response")
	}

	var nre *NetworkRequestError
	if !errors.As(err, &nre) {
		t.Errorf("expected *NetworkRequestError, got %T: %v", err, err)
	}
}

// TestNetworkConn_Close verifies Close marks the connection as closed.
func TestNetworkConn_Close(t *testing.T) {
	conn := &networkConn{
		client:   &http.Client{},
		baseURL:  "http://localhost",
		database: "db",
	}

	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err := conn.QueryContext(context.Background(), "SELECT 1", nil)
	if err == nil {
		t.Fatal("expected error after Close")
	}
	var ce *ConnClosedError
	if !errors.As(err, &ce) {
		t.Errorf("expected *ConnClosedError, got %T: %v", err, err)
	}
}

// TestNetworkConn_PrepareOnClosed verifies Prepare on closed conn returns error.
func TestNetworkConn_PrepareOnClosed(t *testing.T) {
	conn := &networkConn{
		client:   &http.Client{},
		baseURL:  "http://localhost",
		database: "db",
	}
	_ = conn.Close()

	_, err := conn.Prepare("SELECT 1")
	if err == nil {
		t.Fatal("expected error for closed conn")
	}
	var ce *ConnClosedError
	if !errors.As(err, &ce) {
		t.Errorf("expected *ConnClosedError, got %T: %v", err, err)
	}
}

// TestNetworkConn_ExecContextOnClosed verifies ExecContext on closed conn returns error.
func TestNetworkConn_ExecContextOnClosed(t *testing.T) {
	conn := &networkConn{
		client:   &http.Client{},
		baseURL:  "http://localhost",
		database: "db",
	}
	_ = conn.Close()

	_, err := conn.ExecContext(context.Background(), "SELECT 1", nil)
	if err == nil {
		t.Fatal("expected error for closed conn")
	}
	var ce *ConnClosedError
	if !errors.As(err, &ce) {
		t.Errorf("expected *ConnClosedError, got %T: %v", err, err)
	}
}

// TestNetworkConn_BeginTxUnsupportedIsolation verifies unsupported isolation returns error.
func TestNetworkConn_BeginTxUnsupportedIsolation(t *testing.T) {
	conn := &networkConn{
		client:   &http.Client{},
		baseURL:  "http://localhost",
		database: "db",
	}

	_, err := conn.BeginTx(context.Background(), sqld.TxOptions{
		Isolation: sqld.IsolationLevel(sql.LevelReadCommitted),
	})
	if err == nil {
		t.Fatal("expected error for unsupported isolation")
	}
	var ie *UnsupportedIsolationError
	if !errors.As(err, &ie) {
		t.Errorf("expected *UnsupportedIsolationError, got %T: %v", err, err)
	}
}

// TestNetworkConn_BeginTxOnClosed verifies BeginTx on closed conn returns error.
func TestNetworkConn_BeginTxOnClosed(t *testing.T) {
	conn := &networkConn{
		client:   &http.Client{},
		baseURL:  "http://localhost",
		database: "db",
	}
	_ = conn.Close()

	_, err := conn.BeginTx(context.Background(), sqld.TxOptions{})
	if err == nil {
		t.Fatal("expected error on closed conn")
	}
	var ce *ConnClosedError
	if !errors.As(err, &ce) {
		t.Errorf("expected *ConnClosedError, got %T: %v", err, err)
	}
}

// TestNetworkConn_Begin verifies Begin returns valid transaction.
func TestNetworkConn_Begin(t *testing.T) {
	conn := &networkConn{
		client:   &http.Client{},
		baseURL:  "http://localhost",
		database: "db",
	}

	tx, err := conn.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if tx == nil {
		t.Fatal("expected non-nil transaction")
	}
	_ = tx.Rollback()
}

// TestNetworkConn_QueryContextWithArgs verifies arg substitution before sending.
func TestNetworkConn_QueryContextWithArgs(t *testing.T) {
	var receivedQuery string

	srv := newMockSQLServer(t, func(req *sqlRequest, w http.ResponseWriter) {
		receivedQuery = req.Query
		resp := sqlResponse{
			Columns:  []string{"id"},
			Rows:     []map[string]interface{}{},
			RowCount: 0,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	defer srv.Close()

	conn := newNetworkConnFromServer(srv, "testdb")

	args := []sqld.NamedValue{{Ordinal: 1, Value: int64(42)}}
	_, err := conn.QueryContext(context.Background(), "SELECT * FROM t WHERE id = ?", args)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}

	if !strings.Contains(receivedQuery, "42") {
		t.Errorf("query %q should contain substituted value 42", receivedQuery)
	}
}

// TestNetworkConn_InvalidJSON verifies malformed JSON response is handled.
func TestNetworkConn_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not-json"))
	}))
	defer srv.Close()

	conn := newNetworkConnFromServer(srv, "testdb")

	_, err := conn.QueryContext(context.Background(), "SELECT 1", nil)
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
	}
}

// TestNetworkConn_ClientDoError verifies network error from client.Do is wrapped.
func TestNetworkConn_ClientDoError(t *testing.T) {
	// Point at a server that immediately closes.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // Close immediately so client.Do will fail.

	conn := &networkConn{
		client:   &http.Client{},
		baseURL:  url,
		database: "testdb",
	}

	_, err := conn.QueryContext(context.Background(), "SELECT 1", nil)
	if err == nil {
		t.Fatal("expected network error")
	}
	var nre *NetworkRequestError
	if !errors.As(err, &nre) {
		t.Errorf("expected *NetworkRequestError, got %T: %v", err, err)
	}
}

// TestNetworkConn_BeginTxDefaultIsolation verifies BeginTx with LevelDefault succeeds.
func TestNetworkConn_BeginTxDefaultIsolation(t *testing.T) {
	conn := &networkConn{
		client:   &http.Client{},
		baseURL:  "http://localhost",
		database: "db",
	}
	tx, err := conn.BeginTx(context.Background(), sqld.TxOptions{
		Isolation: sqld.IsolationLevel(0), // LevelDefault
	})
	if err != nil {
		t.Fatalf("BeginTx with default isolation: %v", err)
	}
	if tx == nil {
		t.Fatal("expected non-nil tx")
	}
	_ = tx.Rollback()
}

// TestNetworkConn_BeginTxSerializableIsolation verifies serializable isolation is accepted.
func TestNetworkConn_BeginTxSerializableIsolation(t *testing.T) {
	conn := &networkConn{
		client:   &http.Client{},
		baseURL:  "http://localhost",
		database: "db",
	}
	tx, err := conn.BeginTx(context.Background(), sqld.TxOptions{
		Isolation: sqld.IsolationLevel(6), // LevelSerializable
	})
	if err != nil {
		t.Fatalf("BeginTx with serializable: %v", err)
	}
	if tx == nil {
		t.Fatal("expected non-nil tx")
	}
	_ = tx.Commit()
}
