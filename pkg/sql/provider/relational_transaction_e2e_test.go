//go:build qrdb_sql

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

package provider

import (
	"context"
	"io"
	"testing"

	gms "github.com/dolthub/go-mysql-server"
	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/vitess/go/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/sql/engine"
)

// ---------------------------------------------------------------------------
// txConn: a single simulated connection -- one *sqle.Engine, one
// *QRDBSession reused across every statement issued through it, exactly as
// sdk/go/driver's embeddedConn holds one session for its whole lifetime
// (see that package's newEmbeddedSession doc comment). This is what lets
// BEGIN/COMMIT/ROLLBACK/SAVEPOINT state persist across the statements of a
// single test "connection", and what lets two independent txConns over the
// SAME underlying store simulate two concurrent client connections.
// ---------------------------------------------------------------------------

type txConn struct {
	eng  *gms.Engine
	sess *QRDBSession
	db   string
}

// newTxConn creates a txConn serving dbName through prov, with its own
// fresh session.
func newTxConn(t *testing.T, prov *Provider, dbName string) *txConn {
	t.Helper()
	eng := engine.New(engine.Config{Provider: prov})
	t.Cleanup(func() { _ = eng.Close() })
	return &txConn{
		eng:  eng,
		sess: NewSession(sqle.NewBaseSession(), nil),
		db:   dbName,
	}
}

// exec runs query on c's session, returning the result schema and
// materialized rows.
func (c *txConn) exec(query string) (sqle.Schema, []sqle.Row, error) {
	sqlCtx := sqle.NewContext(context.Background(), sqle.WithSession(c.sess))
	sqlCtx.SetCurrentDatabase(c.db)

	schema, iter, _, err := c.eng.Query(sqlCtx, query)
	if err != nil {
		return nil, nil, err
	}
	var rows []sqle.Row
	for {
		row, nerr := iter.Next(sqlCtx)
		if nerr == io.EOF {
			break
		}
		if nerr != nil {
			_ = iter.Close(sqlCtx)
			return nil, nil, nerr
		}
		rows = append(rows, row)
	}
	if cerr := iter.Close(sqlCtx); cerr != nil {
		return nil, nil, cerr
	}
	return schema, rows, nil
}

// mustExec runs exec and fails the test immediately on error.
func (c *txConn) mustExec(t *testing.T, query string) (sqle.Schema, []sqle.Row) {
	t.Helper()
	schema, rows, err := c.exec(query)
	require.NoError(t, err, "query %q", query)
	return schema, rows
}

// ---------------------------------------------------------------------------
// BEGIN / COMMIT / ROLLBACK
// ---------------------------------------------------------------------------

func TestTx_Insert_SelectSeesOwnWrite_Rollback_SelectDoesNotSeeIt(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	conn := newTxConn(t, prov, "shop")

	conn.mustExec(t, "CREATE TABLE widgets (id BIGINT UNSIGNED PRIMARY KEY, name VARCHAR(64))")
	conn.mustExec(t, "BEGIN")
	conn.mustExec(t, "INSERT INTO widgets (id, name) VALUES (1, 'gizmo')")

	_, rows := conn.mustExec(t, "SELECT name FROM widgets WHERE id = 1")
	require.Len(t, rows, 1, "the transaction must see its own uncommitted write")
	assert.Equal(t, "gizmo", rows[0][0])

	conn.mustExec(t, "ROLLBACK")

	_, rows = conn.mustExec(t, "SELECT name FROM widgets WHERE id = 1")
	assert.Empty(t, rows, "a rolled-back write must never be visible")
}

func TestTx_Commit_Persists(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	conn := newTxConn(t, prov, "shop")

	conn.mustExec(t, "CREATE TABLE widgets (id BIGINT UNSIGNED PRIMARY KEY, name VARCHAR(64))")
	conn.mustExec(t, "BEGIN")
	conn.mustExec(t, "INSERT INTO widgets (id, name) VALUES (1, 'gizmo')")
	conn.mustExec(t, "UPDATE widgets SET name = 'gadget' WHERE id = 1")
	conn.mustExec(t, "COMMIT")

	// A second, independent connection over the same store must see the
	// committed result.
	other := newTxConn(t, prov, "shop")
	_, rows := other.mustExec(t, "SELECT name FROM widgets WHERE id = 1")
	require.Len(t, rows, 1)
	assert.Equal(t, "gadget", rows[0][0])
}

func TestTx_Delete_Rollback_RowSurvives(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	conn := newTxConn(t, prov, "shop")

	conn.mustExec(t, "CREATE TABLE widgets (id BIGINT UNSIGNED PRIMARY KEY, name VARCHAR(64))")
	conn.mustExec(t, "INSERT INTO widgets (id, name) VALUES (1, 'gizmo')")

	conn.mustExec(t, "BEGIN")
	conn.mustExec(t, "DELETE FROM widgets WHERE id = 1")
	_, rows := conn.mustExec(t, "SELECT name FROM widgets WHERE id = 1")
	assert.Empty(t, rows)
	conn.mustExec(t, "ROLLBACK")

	_, rows = conn.mustExec(t, "SELECT name FROM widgets WHERE id = 1")
	require.Len(t, rows, 1)
	assert.Equal(t, "gizmo", rows[0][0])
}

func TestTx_Update_PrimaryKeyChange_Commit(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	conn := newTxConn(t, prov, "shop")

	conn.mustExec(t, "CREATE TABLE widgets (id BIGINT UNSIGNED PRIMARY KEY, name VARCHAR(64))")
	conn.mustExec(t, "INSERT INTO widgets (id, name) VALUES (1, 'a')")

	conn.mustExec(t, "BEGIN")
	conn.mustExec(t, "UPDATE widgets SET id = 2 WHERE id = 1")
	_, rows := conn.mustExec(t, "SELECT id FROM widgets WHERE id = 1")
	assert.Empty(t, rows, "the transaction must see its own primary-key move")
	_, rows = conn.mustExec(t, "SELECT id FROM widgets WHERE id = 2")
	require.Len(t, rows, 1)
	conn.mustExec(t, "COMMIT")

	other := newTxConn(t, prov, "shop")
	_, rows = other.mustExec(t, "SELECT id FROM widgets ORDER BY id")
	require.Len(t, rows, 1)
	assert.Equal(t, uint64(2), rows[0][0])
}

func TestTx_Update_PrimaryKeyChange_ConflictsWithConcurrentInsert(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	setup := newTxConn(t, prov, "shop")
	setup.mustExec(t, "CREATE TABLE widgets (id BIGINT UNSIGNED PRIMARY KEY, name VARCHAR(64))")
	setup.mustExec(t, "INSERT INTO widgets (id, name) VALUES (1, 'a')")

	a := newTxConn(t, prov, "shop")
	b := newTxConn(t, prov, "shop")

	a.mustExec(t, "BEGIN")
	b.mustExec(t, "BEGIN")
	a.mustExec(t, "UPDATE widgets SET id = 2 WHERE id = 1")
	b.mustExec(t, "INSERT INTO widgets (id, name) VALUES (2, 'b')")

	b.mustExec(t, "COMMIT")
	_, _, err := a.exec("COMMIT")
	require.Error(t, err, "moving a row onto a primary key a concurrent transaction just claimed must conflict")
}

// ---------------------------------------------------------------------------
// Savepoints
// ---------------------------------------------------------------------------

func TestTx_Savepoint_MarkRollbackRelease(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	conn := newTxConn(t, prov, "shop")

	conn.mustExec(t, "CREATE TABLE widgets (id BIGINT UNSIGNED PRIMARY KEY, name VARCHAR(64))")
	conn.mustExec(t, "BEGIN")
	conn.mustExec(t, "INSERT INTO widgets (id, name) VALUES (1, 'a')")
	conn.mustExec(t, "SAVEPOINT sp1")
	conn.mustExec(t, "INSERT INTO widgets (id, name) VALUES (2, 'b')")

	_, rows := conn.mustExec(t, "SELECT id FROM widgets ORDER BY id")
	require.Len(t, rows, 2)

	conn.mustExec(t, "ROLLBACK TO SAVEPOINT sp1")
	_, rows = conn.mustExec(t, "SELECT id FROM widgets ORDER BY id")
	require.Len(t, rows, 1, "the insert made after the savepoint must be undone")
	assert.Equal(t, uint64(1), rows[0][0])

	// The savepoint itself survives a rollback to it: further statements
	// may run, and a second ROLLBACK TO SAVEPOINT to the same name is
	// still valid (replay correctness).
	conn.mustExec(t, "INSERT INTO widgets (id, name) VALUES (3, 'c')")
	conn.mustExec(t, "ROLLBACK TO SAVEPOINT sp1")
	_, rows = conn.mustExec(t, "SELECT id FROM widgets ORDER BY id")
	require.Len(t, rows, 1)

	conn.mustExec(t, "RELEASE SAVEPOINT sp1")
	conn.mustExec(t, "COMMIT")

	_, rows = conn.mustExec(t, "SELECT id FROM widgets ORDER BY id")
	require.Len(t, rows, 1, "only the pre-savepoint insert must have been committed")
	assert.Equal(t, uint64(1), rows[0][0])
}

// ---------------------------------------------------------------------------
// Conflicts: 1213 / SQLSTATE 40001
// ---------------------------------------------------------------------------

func TestTx_ConcurrentUpdate_OneGets1213(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	setup := newTxConn(t, prov, "shop")
	setup.mustExec(t, "CREATE TABLE counters (id BIGINT UNSIGNED PRIMARY KEY, n INT)")
	setup.mustExec(t, "INSERT INTO counters (id, n) VALUES (1, 0)")

	a := newTxConn(t, prov, "shop")
	b := newTxConn(t, prov, "shop")

	a.mustExec(t, "BEGIN")
	b.mustExec(t, "BEGIN")

	a.mustExec(t, "UPDATE counters SET n = 1 WHERE id = 1")
	b.mustExec(t, "UPDATE counters SET n = 2 WHERE id = 1")

	a.mustExec(t, "COMMIT")

	_, _, err := b.exec("COMMIT")
	require.Error(t, err, "the second transaction's commit must lose the race")

	sqlErr := sqle.CastSQLError(err)
	require.NotNil(t, sqlErr, "a transaction conflict must be classifiable as a go-mysql-server SQL error")
	assert.EqualValues(t, mysql.ERLockDeadlock, sqlErr.Num)
	assert.EqualValues(t, 1213, sqlErr.Num)
	assert.Equal(t, mysql.SSLockDeadlock, sqlErr.State)
	assert.Equal(t, "40001", sqlErr.State)

	other := newTxConn(t, prov, "shop")
	_, rows := other.mustExec(t, "SELECT n FROM counters WHERE id = 1")
	require.Len(t, rows, 1)
	assert.Equal(t, int32(1), rows[0][0], "only the winning transaction's write must be durable")
}

func TestTx_ConcurrentInsert_DisjointRows_BothCommit(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	setup := newTxConn(t, prov, "shop")
	setup.mustExec(t, "CREATE TABLE widgets (id BIGINT UNSIGNED PRIMARY KEY, name VARCHAR(64))")

	a := newTxConn(t, prov, "shop")
	b := newTxConn(t, prov, "shop")

	a.mustExec(t, "BEGIN")
	b.mustExec(t, "BEGIN")
	a.mustExec(t, "INSERT INTO widgets (id, name) VALUES (1, 'a')")
	b.mustExec(t, "INSERT INTO widgets (id, name) VALUES (2, 'b')")
	a.mustExec(t, "COMMIT")
	b.mustExec(t, "COMMIT")

	other := newTxConn(t, prov, "shop")
	_, rows := other.mustExec(t, "SELECT id FROM widgets ORDER BY id")
	require.Len(t, rows, 2, "two transactions touching disjoint rows must both commit")
}

// ---------------------------------------------------------------------------
// UNIQUE index enforcement inside a transaction
// ---------------------------------------------------------------------------

func TestTx_UniqueIndex_SelfConflictWithinSameTransaction(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	conn := newTxConn(t, prov, "shop")

	conn.mustExec(t, "CREATE TABLE users (id BIGINT UNSIGNED PRIMARY KEY, email VARCHAR(255))")
	conn.mustExec(t, "ALTER TABLE users ADD UNIQUE INDEX idx_email (email)")

	conn.mustExec(t, "BEGIN")
	conn.mustExec(t, "INSERT INTO users (id, email) VALUES (1, 'a@example.com')")

	_, _, err := conn.exec("INSERT INTO users (id, email) VALUES (2, 'a@example.com')")
	require.Error(t, err, "a duplicate unique-index value buffered within the SAME transaction must be caught immediately")
	sqlErr := sqle.CastSQLError(err)
	require.NotNil(t, sqlErr)
	assert.EqualValues(t, mysql.ERDupEntry, sqlErr.Num)

	conn.mustExec(t, "ROLLBACK")
}

func TestTx_UniqueIndex_ConflictAcrossTransactions(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	setup := newTxConn(t, prov, "shop")
	setup.mustExec(t, "CREATE TABLE users (id BIGINT UNSIGNED PRIMARY KEY, email VARCHAR(255))")
	setup.mustExec(t, "ALTER TABLE users ADD UNIQUE INDEX idx_email (email)")

	a := newTxConn(t, prov, "shop")
	b := newTxConn(t, prov, "shop")

	a.mustExec(t, "BEGIN")
	b.mustExec(t, "BEGIN")
	a.mustExec(t, "INSERT INTO users (id, email) VALUES (1, 'a@example.com')")
	b.mustExec(t, "INSERT INTO users (id, email) VALUES (2, 'a@example.com')")

	a.mustExec(t, "COMMIT")
	_, _, err := b.exec("COMMIT")
	require.Error(t, err, "a unique-index value claimed by a concurrently-committed transaction must fail this commit")

	other := newTxConn(t, prov, "shop")
	_, rows := other.mustExec(t, "SELECT id FROM users")
	require.Len(t, rows, 1, "only the winning transaction's row must be durable")
}

// ---------------------------------------------------------------------------
// Legacy (non-relational) tables inside an explicit transaction
// ---------------------------------------------------------------------------

func TestTx_LegacyKVTable_InsideBegin_Rejected(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	conn := newTxConn(t, prov, "shop")

	conn.mustExec(t, "BEGIN")
	_, _, err := conn.exec("INSERT INTO `kv` (`key`, `value`) VALUES ('k1', 'v1')")
	require.Error(t, err)
	// go-mysql-server's INSERT execution loop wraps the writer's error in
	// its own sql.WrappedInsertError, which (unlike this package's own
	// error types) does not implement the standard library's Unwrap
	// contract -- see sql.UnwrapError's doc comment -- so it must be
	// peeled off explicitly before errors.As can reach our typed error.
	var nonTx *NonTransactionalTableError
	require.ErrorAs(t, sqle.UnwrapError(err), &nonTx)
	assert.Equal(t, tableName, nonTx.Table)

	conn.mustExec(t, "ROLLBACK")
}

func TestTx_LegacyKVTable_Autocommit_StillWorks(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	conn := newTxConn(t, prov, "shop")

	conn.mustExec(t, "INSERT INTO `kv` (`key`, `value`) VALUES ('k1', 'v1')")
	_, rows := conn.mustExec(t, "SELECT `value` FROM `kv` WHERE `key` = 'k1'")
	require.Len(t, rows, 1)
}

// ---------------------------------------------------------------------------
// DDL implicitly flushes a pending buffer
// ---------------------------------------------------------------------------

func TestTx_DDL_FlushesPendingBufferFirst(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	conn := newTxConn(t, prov, "shop")

	conn.mustExec(t, "CREATE TABLE widgets (id BIGINT UNSIGNED PRIMARY KEY, name VARCHAR(64))")
	conn.mustExec(t, "BEGIN")
	conn.mustExec(t, "INSERT INTO widgets (id, name) VALUES (1, 'a')")

	// CREATE TABLE, as DDL, must implicitly commit the pending INSERT
	// above before it runs -- matching MySQL.
	conn.mustExec(t, "CREATE TABLE gadgets (id BIGINT UNSIGNED PRIMARY KEY)")

	other := newTxConn(t, prov, "shop")
	_, rows := other.mustExec(t, "SELECT id FROM widgets")
	require.Len(t, rows, 1, "DDL must implicitly commit a pending transaction before it runs")

	// A later ROLLBACK on the original connection has nothing left to
	// undo: the DDL's implicit commit already ended the transaction.
	_, _, err := conn.exec("ROLLBACK")
	require.NoError(t, err)
	_, rows = other.mustExec(t, "SELECT id FROM widgets")
	require.Len(t, rows, 1, "DDL's implicit commit is never undone by a later ROLLBACK")
}

func TestTx_Truncate_FlushesPendingBufferFirst(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	conn := newTxConn(t, prov, "shop")

	conn.mustExec(t, "CREATE TABLE widgets (id BIGINT UNSIGNED PRIMARY KEY, name VARCHAR(64))")
	conn.mustExec(t, "INSERT INTO widgets (id, name) VALUES (1, 'a')")

	conn.mustExec(t, "BEGIN")
	conn.mustExec(t, "INSERT INTO widgets (id, name) VALUES (2, 'b')")
	conn.mustExec(t, "TRUNCATE TABLE widgets")

	other := newTxConn(t, prov, "shop")
	_, rows := other.mustExec(t, "SELECT id FROM widgets")
	assert.Empty(t, rows, "TRUNCATE must implicitly commit the pending insert, then remove every row including it")
}

// ---------------------------------------------------------------------------
// Read-only transactions
//
// WriteSet's own READ ONLY enforcement (WriteSet.recordTouch ->
// *ReadOnlyTransactionError) is covered directly at the WriteSet level --
// see TestWriteSet_ReadOnly_RejectsWrites in writeset_test.go -- rather
// than through a real SQL statement here: go-mysql-server's own
// validateReadOnlyTransaction analyzer rule
// (sql/analyzer/validation_rules.go) unconditionally calls
// tt.IsTemporary() on a sql.TemporaryTable type assertion result without
// checking the assertion's ok value first, which panics with a nil
// pointer dereference for any ordinary (non-temporary) destination table
// -- a pre-existing bug in the vendored engine version this module
// depends on, unrelated to this package's own transaction support, and
// not something a test in this repository can safely route around.
// ---------------------------------------------------------------------------
