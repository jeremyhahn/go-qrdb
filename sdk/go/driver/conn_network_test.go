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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// fakeSQLClient records the calls a network connection makes and returns a
// canned result, standing in for a remote qrdb server.
type fakeSQLClient struct {
	result *transport.SQLQueryResult
	err    error

	calls    int
	lastDB   string
	lastStmt string
}

func (f *fakeSQLClient) Query(_ context.Context, database, query string) (*transport.SQLQueryResult, error) {
	f.calls++
	f.lastDB = database
	f.lastStmt = query
	if f.err != nil {
		return nil, f.err
	}
	if f.result != nil {
		return f.result, nil
	}
	return &transport.SQLQueryResult{}, nil
}

// newFakeNetworkConn returns a network connection backed by the given client.
func newFakeNetworkConn(client *fakeSQLClient, database string) *networkConn {
	return &networkConn{sqlClient: client, database: database, scheme: SchemeREST}
}

// TestNetworkConn_QueryContext_Success verifies the statement and database
// reach the transport and the result set is exposed.
func TestNetworkConn_QueryContext_Success(t *testing.T) {
	client := &fakeSQLClient{result: &transport.SQLQueryResult{
		Columns:  []string{"id", "name"},
		Rows:     []map[string]any{{"id": "1", "name": "alice"}},
		RowCount: 1,
	}}
	conn := newFakeNetworkConn(client, "testdb")

	rows, err := conn.QueryContext(context.Background(), "SELECT id, name FROM users", nil)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	defer func() { _ = rows.Close() }()

	if client.lastDB != "testdb" {
		t.Errorf("database = %q, want testdb", client.lastDB)
	}
	if client.lastStmt != "SELECT id, name FROM users" {
		t.Errorf("query = %q, want the original statement", client.lastStmt)
	}

	cols := rows.Columns()
	if len(cols) != 2 || cols[0] != "id" || cols[1] != "name" {
		t.Errorf("Columns() = %v, want [id name]", cols)
	}
}

// TestNetworkConn_ExecContext_MetadataUnsupported verifies the result declares
// affected-row and insert-id metadata unavailable rather than fabricating it
// from the row count.
func TestNetworkConn_ExecContext_MetadataUnsupported(t *testing.T) {
	client := &fakeSQLClient{result: &transport.SQLQueryResult{RowCount: 2}}
	conn := newFakeNetworkConn(client, "testdb")

	res, err := conn.ExecContext(context.Background(), "DELETE FROM t WHERE x = 1", nil)
	if err != nil {
		t.Fatalf("ExecContext: %v", err)
	}

	var unsupported *ResultMetadataUnsupportedError
	if _, err := res.RowsAffected(); !errors.As(err, &unsupported) {
		t.Errorf("RowsAffected error = %v, want *ResultMetadataUnsupportedError", err)
	}
	if _, err := res.LastInsertId(); !errors.As(err, &unsupported) {
		t.Errorf("LastInsertId error = %v, want *ResultMetadataUnsupportedError", err)
	}
}

// TestNetworkConn_RejectsParameters verifies bound arguments are refused
// rather than spliced into the SQL text, and that nothing is sent.
func TestNetworkConn_RejectsParameters(t *testing.T) {
	client := &fakeSQLClient{}
	conn := newFakeNetworkConn(client, "testdb")

	args := []sqld.NamedValue{{Ordinal: 1, Value: int64(42)}}
	ctx := context.Background()

	var unsupported *ParameterizedQueryUnsupportedError

	if _, err := conn.QueryContext(ctx, "SELECT * FROM t WHERE id = ?", args); !errors.As(err, &unsupported) {
		t.Errorf("QueryContext error = %v, want *ParameterizedQueryUnsupportedError", err)
	}
	if _, err := conn.ExecContext(ctx, "DELETE FROM t WHERE id = ?", args); !errors.As(err, &unsupported) {
		t.Errorf("ExecContext error = %v, want *ParameterizedQueryUnsupportedError", err)
	}

	// Nothing may reach the server: the value cannot leak into the SQL text.
	if client.calls != 0 {
		t.Errorf("transport received %d calls, want 0", client.calls)
	}
}

// TestNetworkConn_TransportError verifies a transport failure is wrapped.
func TestNetworkConn_TransportError(t *testing.T) {
	sentinel := errors.New("transport unavailable")
	conn := newFakeNetworkConn(&fakeSQLClient{err: sentinel}, "testdb")

	_, err := conn.QueryContext(context.Background(), "SELECT 1", nil)
	if !errors.Is(err, sentinel) {
		t.Errorf("error = %v, want it to wrap %v", err, sentinel)
	}

	var execErr *QueryExecError
	if !errors.As(err, &execErr) {
		t.Errorf("error = %v, want *QueryExecError", err)
	}
}

// TestNetworkConn_TransactionsUnsupported verifies a transaction is refused
// rather than returning a handle whose Rollback silently does nothing.
func TestNetworkConn_TransactionsUnsupported(t *testing.T) {
	conn := newFakeNetworkConn(&fakeSQLClient{}, "testdb")

	_, err := conn.BeginTx(context.Background(), sqld.TxOptions{})

	var unsupported *TransactionUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("BeginTx error = %v, want *TransactionUnsupportedError", err)
	}
	if unsupported.Scheme != SchemeREST {
		t.Errorf("Scheme = %q, want %q", unsupported.Scheme, SchemeREST)
	}
}

// TestNetworkConn_BeginTxUnsupportedIsolation verifies a non-default isolation
// level is reported before the transaction refusal.
func TestNetworkConn_BeginTxUnsupportedIsolation(t *testing.T) {
	conn := newFakeNetworkConn(&fakeSQLClient{}, "testdb")

	_, err := conn.BeginTx(context.Background(), sqld.TxOptions{
		Isolation: sqld.IsolationLevel(sql.LevelSerializable),
	})

	var iso *UnsupportedIsolationError
	if !errors.As(err, &iso) {
		t.Errorf("error = %v, want *UnsupportedIsolationError", err)
	}
}

// TestNetworkConn_ClosedRejectsEverything verifies a closed connection refuses
// all work and reports itself invalid so the pool discards it.
func TestNetworkConn_ClosedRejectsEverything(t *testing.T) {
	conn := newFakeNetworkConn(&fakeSQLClient{}, "testdb")

	if !conn.IsValid() {
		t.Fatal("fresh connection reported invalid")
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if conn.IsValid() {
		t.Error("closed connection reported valid")
	}

	ctx := context.Background()
	var closedErr *ConnClosedError

	if _, err := conn.QueryContext(ctx, "SELECT 1", nil); !errors.As(err, &closedErr) {
		t.Errorf("QueryContext error = %v, want *ConnClosedError", err)
	}
	if _, err := conn.ExecContext(ctx, "SELECT 1", nil); !errors.As(err, &closedErr) {
		t.Errorf("ExecContext error = %v, want *ConnClosedError", err)
	}
	if _, err := conn.Prepare("SELECT 1"); !errors.As(err, &closedErr) {
		t.Errorf("Prepare error = %v, want *ConnClosedError", err)
	}
	if _, err := conn.BeginTx(ctx, sqld.TxOptions{}); !errors.As(err, &closedErr) {
		t.Errorf("BeginTx error = %v, want *ConnClosedError", err)
	}
}

// TestNetworkConn_Begin verifies the legacy Begin path reports the same
// refusal as BeginTx.
func TestNetworkConn_Begin(t *testing.T) {
	conn := newFakeNetworkConn(&fakeSQLClient{}, "testdb")

	_, err := conn.Begin()

	var unsupported *TransactionUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Errorf("Begin error = %v, want *TransactionUnsupportedError", err)
	}
}
