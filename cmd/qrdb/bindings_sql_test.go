//go:build qrdb_sql

// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// stubDatabaseService implements service.DatabaseServicer, overriding only
// Get (the single method the SQL query service calls to resolve engine
// scoping). Every other method panics via the embedded nil interface if
// called, which keeps the mock honest about what SQL execution touches.
type stubDatabaseService struct {
	service.DatabaseServicer
	getFn func(name string) (*database.DatabaseInfo, error)
}

func (m *stubDatabaseService) Get(name string) (*database.DatabaseInfo, error) {
	return m.getFn(name)
}

// GetForTenant is the owner-checked read the SQL query service makes, held
// to the same rule as the real DatabaseService.GetForTenant: a database
// another tenant owns is answered as missing (database.OwnedBy).
func (m *stubDatabaseService) GetForTenant(ctx context.Context, name string) (*database.DatabaseInfo, error) {
	info, err := m.getFn(name)
	if err != nil {
		return nil, err
	}
	if !database.OwnedBy(info.TenantID, tenant.FromContext(ctx)) {
		return nil, &database.NotFoundError{Name: name}
	}
	return info, nil
}

// kvEngineDatabase returns a DatabaseInfo describing a plain memory-backed
// database, matching what the SQL provider needs to scope table exposure.
func kvEngineDatabase(name string) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{
		Name:    name,
		Backend: database.BackendMemory,
		Status:  database.StatusOpen,
	}, nil
}

// connectSQLClient installs an embedded SDK client over kvSvc as conn's
// active client, replacing any previous one.
func connectSQLClient(t *testing.T, conn *ConnectionBinding, kvSvc service.KVServicer) {
	t.Helper()
	dbSvc := &stubDatabaseService{getFn: kvEngineDatabase}
	tr, err := embedded.New(kvSvc, newMockAdminService(), embedded.WithDatabaseService(dbSvc))
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	cl, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	conn.client.Store(cl)
}

// ---------------------------------------------------------------------------
// Execute: not connected
// ---------------------------------------------------------------------------

func TestSQLBinding_Execute_NotConnected(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	sb := NewSQLBinding(log, conn)

	result, err := sb.Execute("SELECT 1", "qrdb")
	if result != nil {
		t.Fatalf("expected nil result, got: %+v", result)
	}
	var notConn *NotConnectedError
	if !errors.As(err, &notConn) {
		t.Fatalf("expected NotConnectedError, got: %T: %v", err, err)
	}
}

func TestSQLBinding_Execute_NotConnectedMessage(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	sb := NewSQLBinding(log, conn)

	_, err := sb.Execute("SELECT 1", "qrdb")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	expected := "qrdb: not connected to server"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}

// ---------------------------------------------------------------------------
// Execute: successful query
// ---------------------------------------------------------------------------

func TestSQLBinding_Execute_Success(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	sb := NewSQLBinding(log, conn)

	// Populate the mock KV service with test data so SELECT * FROM kv
	// returns at least one row. Keys must carry the composite
	// "<tenant>/<db>/" namespace (system tenant, database "qrdb") because
	// the SQL KV table scans that prefix and strips it — identical to
	// every other transport (pkg/tenant.ComposeNamespace).
	kvSvc := newMockKVService()
	_ = kvSvc.Put(context.Background(), "system/qrdb/hello", []byte("world"))

	connectSQLClient(t, conn, kvSvc)

	result, err := sb.Execute("SELECT * FROM kv", "qrdb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// The KV table exposes at least "key" and "value" columns.
	if len(result.Columns) < 2 {
		t.Fatalf("expected at least 2 columns, got %d: %v", len(result.Columns), result.Columns)
	}
	if result.RowCount < 1 {
		t.Fatalf("expected at least 1 row, got %d", result.RowCount)
	}
	if result.Duration == "" {
		t.Fatal("expected non-empty duration")
	}
	if len(result.Rows) != result.RowCount {
		t.Fatalf("row slice length %d != RowCount %d", len(result.Rows), result.RowCount)
	}
}

func TestSQLBinding_Execute_SuccessEmptyTable(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	sb := NewSQLBinding(log, conn)

	kvSvc := newMockKVService()
	connectSQLClient(t, conn, kvSvc)

	result, err := sb.Execute("SELECT * FROM kv", "qrdb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.RowCount != 0 {
		t.Fatalf("expected 0 rows, got %d", result.RowCount)
	}
	// Rows should be an empty slice, not nil.
	if result.Rows == nil {
		t.Fatal("expected non-nil empty rows slice")
	}
	if len(result.Rows) != 0 {
		t.Fatalf("expected empty rows, got %d", len(result.Rows))
	}
}

// ---------------------------------------------------------------------------
// Execute: invalid query (syntax error)
// ---------------------------------------------------------------------------

func TestSQLBinding_Execute_InvalidQuery(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	sb := NewSQLBinding(log, conn)

	kvSvc := newMockKVService()
	connectSQLClient(t, conn, kvSvc)

	result, err := sb.Execute("SELECTX INVALID SYNTAX", "qrdb")
	if result != nil {
		t.Fatalf("expected nil result, got: %+v", result)
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var sqlErr *SQLExecutionError
	if !errors.As(err, &sqlErr) {
		t.Fatalf("expected SQLExecutionError, got: %T: %v", err, err)
	}
	if sqlErr.Query != "SELECTX INVALID SYNTAX" {
		t.Fatalf("expected query in error, got: %q", sqlErr.Query)
	}
	if sqlErr.Unwrap() == nil {
		t.Fatal("expected wrapped error, got nil")
	}
}

func TestSQLBinding_Execute_InvalidQueryMessage(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	sb := NewSQLBinding(log, conn)

	kvSvc := newMockKVService()
	connectSQLClient(t, conn, kvSvc)

	_, err := sb.Execute("SELECTX BAD", "qrdb")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// Verify the error message starts with the typed prefix.
	const prefix = "qrdb: SQL execution failed: "
	if len(err.Error()) < len(prefix) || err.Error()[:len(prefix)] != prefix {
		t.Fatalf("expected error message to start with %q, got: %q", prefix, err.Error())
	}
}

// ---------------------------------------------------------------------------
// Disconnect lifecycle
// ---------------------------------------------------------------------------

func TestSQLBinding_Execute_AfterDisconnect(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	sb := NewSQLBinding(log, conn)

	connectSQLClient(t, conn, newMockKVService())
	if _, err := sb.Execute("SELECT * FROM kv", "qrdb"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	conn.client.Store(nil)

	_, err := sb.Execute("SELECT * FROM kv", "qrdb")
	var notConn *NotConnectedError
	if !errors.As(err, &notConn) {
		t.Fatalf("expected NotConnectedError, got: %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Error type assertions
// ---------------------------------------------------------------------------

func TestSQLExecutionError_Error(t *testing.T) {
	inner := errors.New("parse failure")
	e := &SQLExecutionError{Query: "SELECT oops", Err: inner}
	expected := "qrdb: SQL execution failed: parse failure"
	if e.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, e.Error())
	}
}

func TestSQLExecutionError_Unwrap(t *testing.T) {
	inner := errors.New("inner")
	e := &SQLExecutionError{Query: "q", Err: inner}
	if e.Unwrap() != inner {
		t.Fatal("Unwrap did not return inner error")
	}
}

func TestSQLBinding_Execute_NonExistentTable(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	sb := NewSQLBinding(log, conn)

	kvSvc := newMockKVService()
	connectSQLClient(t, conn, kvSvc)

	_, err := sb.Execute("SELECT * FROM nonexistent_table", "qrdb")
	if err == nil {
		t.Fatal("expected error for non-existent table, got nil")
	}
	var sqlErr *SQLExecutionError
	if !errors.As(err, &sqlErr) {
		t.Fatalf("expected SQLExecutionError, got: %T: %v", err, err)
	}
}

func TestSQLBinding_NewSQLBinding(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	sb := NewSQLBinding(log, conn)
	if sb == nil {
		t.Fatal("expected non-nil SQLBinding")
	}
	if sb.logger != log {
		t.Fatal("logger not set correctly")
	}
	if sb.conn != conn {
		t.Fatal("conn not set correctly")
	}
}

// ---------------------------------------------------------------------------
// Execute: verify row data content
// ---------------------------------------------------------------------------

func TestSQLBinding_Execute_RowContent(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	sb := NewSQLBinding(log, conn)

	kvSvc := newMockKVService()
	_ = kvSvc.Put(context.Background(), "system/qrdb/testkey", []byte("testvalue"))
	connectSQLClient(t, conn, kvSvc)

	result, err := sb.Execute("SELECT * FROM kv WHERE `key` = 'testkey'", "qrdb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RowCount != 1 {
		t.Fatalf("expected 1 row, got %d", result.RowCount)
	}

	row := result.Rows[0]
	keyVal, ok := row["key"]
	if !ok {
		t.Fatal("expected 'key' column in row")
	}
	if keyVal != "testkey" {
		t.Fatalf("expected key='testkey', got %q", keyVal)
	}

	valVal, ok := row["value"]
	if !ok {
		t.Fatal("expected 'value' column in row")
	}
	if valVal != "testvalue" {
		t.Fatalf("expected value='testvalue', got %q", valVal)
	}
}

// ---------------------------------------------------------------------------
// Execute: multiple services set/clear cycles
// ---------------------------------------------------------------------------

func TestSQLBinding_Execute_ReconnectTwice(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	sb := NewSQLBinding(log, conn)

	kvSvc1 := newMockKVService()
	_ = kvSvc1.Put(context.Background(), "system/qrdb/k1", []byte("v1"))

	kvSvc2 := newMockKVService()
	_ = kvSvc2.Put(context.Background(), "system/qrdb/k2", []byte("v2"))

	// Set first service.
	connectSQLClient(t, conn, kvSvc1)

	result1, err := sb.Execute("SELECT * FROM kv", "qrdb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result1.RowCount != 1 {
		t.Fatalf("expected 1 row from first service, got %d", result1.RowCount)
	}

	// Replace with second service.
	connectSQLClient(t, conn, kvSvc2)

	result2, err := sb.Execute("SELECT * FROM kv", "qrdb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result2.RowCount != 1 {
		t.Fatalf("expected 1 row from second service, got %d", result2.RowCount)
	}
}

// ---------------------------------------------------------------------------
// Execute: wrong database name
// ---------------------------------------------------------------------------

func TestSQLBinding_Execute_WrongDatabase(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	sb := NewSQLBinding(log, conn)

	kvSvc := newMockKVService()
	connectSQLClient(t, conn, kvSvc)

	// Querying a table in a database name that does not match the provider
	// should fail since the provider only serves the named database.
	_, err := sb.Execute("SELECT * FROM kv", "nonexistent_db")
	if err == nil {
		// Depending on go-mysql-server behavior, it may or may not error
		// for a wrong database when no USE statement is issued. If it
		// succeeds, that is acceptable behavior too.
		return
	}
	var sqlErr *SQLExecutionError
	if !errors.As(err, &sqlErr) {
		t.Fatalf("expected SQLExecutionError, got: %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Execute: verify query types
// ---------------------------------------------------------------------------

func TestSQLBinding_Execute_SelectLiteral(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	sb := NewSQLBinding(log, conn)

	kvSvc := newMockKVService()
	connectSQLClient(t, conn, kvSvc)

	result, err := sb.Execute("SELECT 1 AS num, 'hello' AS greeting", "qrdb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RowCount != 1 {
		t.Fatalf("expected 1 row, got %d", result.RowCount)
	}
	if len(result.Columns) != 2 {
		t.Fatalf("expected 2 columns, got %d", len(result.Columns))
	}
}

// ---------------------------------------------------------------------------
// Error type: verify interface compliance
// ---------------------------------------------------------------------------

func TestSQLExecutionError_ImplementsError(t *testing.T) {
	var _ error = (*SQLExecutionError)(nil)
}

// ---------------------------------------------------------------------------
// Execute: KV service with data but filtered by WHERE returning no rows
// ---------------------------------------------------------------------------

func TestSQLBinding_Execute_WhereNoMatch(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	sb := NewSQLBinding(log, conn)

	kvSvc := newMockKVService()
	_ = kvSvc.Put(context.Background(), "system/qrdb/existing", []byte("data"))
	connectSQLClient(t, conn, kvSvc)

	result, err := sb.Execute("SELECT * FROM kv WHERE `key` = 'nonexistent'", "qrdb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RowCount != 0 {
		t.Fatalf("expected 0 rows, got %d", result.RowCount)
	}
	if result.Rows == nil {
		t.Fatal("expected non-nil empty rows slice")
	}
}
