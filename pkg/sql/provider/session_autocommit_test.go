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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestAutocommitOff_IsOneTransactionUntilCommit: with SET autocommit = 0 a
// session's statements form one transaction, as in InnoDB: nothing it
// writes is visible to another session until COMMIT, everything is after
// it, and ROLLBACK undoes all of it.
func TestAutocommitOff_IsOneTransactionUntilCommit(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	a, b := newTxConn(t, prov, "shop"), newTxConn(t, prov, "shop")
	a.mustExec(t, "CREATE TABLE t (id BIGINT PRIMARY KEY, v VARCHAR(8))")

	a.mustExec(t, "SET autocommit = 0")
	a.mustExec(t, "INSERT INTO t VALUES (1, 'a')")
	a.mustExec(t, "INSERT INTO t VALUES (2, 'b')")
	_, rows := a.mustExec(t, "SELECT id FROM t")
	assert.Len(t, rows, 2, "the transaction reads its own writes")
	_, rows = b.mustExec(t, "SELECT id FROM t")
	assert.Empty(t, rows, "nothing is visible to another session before COMMIT")

	a.mustExec(t, "COMMIT")
	_, rows = b.mustExec(t, "SELECT id FROM t")
	assert.Len(t, rows, 2, "COMMIT makes every statement's writes durable")

	a.mustExec(t, "INSERT INTO t VALUES (3, 'c')")
	a.mustExec(t, "ROLLBACK")
	_, rows = b.mustExec(t, "SELECT id FROM t")
	assert.Len(t, rows, 2, "ROLLBACK undoes the transaction")

	a.mustExec(t, "SET autocommit = 1")
	a.mustExec(t, "INSERT INTO t VALUES (4, 'd')")
	_, rows = b.mustExec(t, "SELECT id FROM t")
	assert.Len(t, rows, 3, "with autocommit back on, a statement commits on its own")
}

func TestExplicitTxn(t *testing.T) {
	ctx, sess := newTxContext(false)
	explicit, err := explicitTxn(ctx)
	require.NoError(t, err)
	assert.False(t, explicit, "autocommit on, no BEGIN: each statement is its own")
	require.NoError(t, requireAutocommit(ctx, "kv"))

	require.NoError(t, sess.SetSessionVariable(ctx, "autocommit", int8(0)))
	explicit, err = explicitTxn(ctx)
	require.NoError(t, err)
	assert.True(t, explicit, "autocommit off: the session's statements form one transaction")
	var nonTx *NonTransactionalTableError
	require.ErrorAs(t, requireAutocommit(ctx, "kv"), &nonTx,
		"a write that cannot join the transaction is refused rather than escape it")

	begun, _ := newTxContext(true)
	explicit, err = explicitTxn(begun)
	require.NoError(t, err)
	assert.True(t, explicit, "BEGIN opens an explicit transaction")
}

// noopLease and noopWatch only stand in for the servicers rowlock.New
// requires; the test that uses them takes no lock.
type noopLease struct{ service.LeaseServicer }
type noopWatch struct{ service.WatchServicer }

// TestStoreID_LockScopeIsSharedByLocksAndCommits: the row-lock table and the
// write-set name a store's lock scope through the one helper, so a
// transaction's lock release folds into its commit.
func TestStoreID_LockScopeIsSharedByLocksAndCommits(t *testing.T) {
	id := storeID{tenant: "acme", db: "shop"}
	assert.Equal(t, "acme/shop", id.lockScope())

	ws := newWriteSet(false, IsolationWritten)
	assert.Empty(t, ws.lockScope(), "an unbound write-set commits to no store")
	ws.bound, ws.tenantID, ws.database = true, "acme", "shop"
	assert.Equal(t, id.lockScope(), ws.lockScope())

	reg := &fakeRelationalRegistry{leaseSvc: noopLease{}, watchSvc: noopWatch{}}
	d := &Database{name: "shop", registry: reg}
	tbl, err := d.rowLockTable(reg, "acme", &memTxn{store: newMemStore()}, newMemStore())
	require.NoError(t, err)
	assert.Equal(t, id.lockScope(), tbl.Scope())
}

// TestRelationalWrite_RequiresASession: every relational write is part of a
// transaction, which lives on a QRDB session; a context without one has
// nowhere to put it and is refused rather than written row by row.
func TestRelationalWrite_RequiresASession(t *testing.T) {
	h := newDDLHarness()
	tbl, _ := newWriterHarnessTable(t, h, ordersCreateRequest())
	plain := newTestContext()
	row := orderRow(1, "SKU-1", "east", int32(1))

	var noSession *TransactionSessionRequiredError
	require.ErrorAs(t, tbl.Inserter(plain).Insert(plain, row), &noSession)
	assert.Equal(t, tbl.def.Name, noSession.Table)
	require.ErrorAs(t, tbl.Updater(plain).Update(plain, row, row), &noSession)
	require.ErrorAs(t, tbl.Deleter(plain).Delete(plain, row), &noSession)
	assert.Contains(t, noSession.Error(), "requires a QRDB session")
}

// TestAutocommitOff_CommandBoundariesKeepTheTransaction is the same over a
// surface that brackets each statement as a command (every SQL surface
// does): the command boundary must not discard the open transaction.
func TestAutocommitOff_CommandBoundariesKeepTheTransaction(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	a := commandConn{newTxConn(t, prov, "shop")}
	b := commandConn{newTxConn(t, prov, "shop")}
	a.run(t, "CREATE TABLE t (id BIGINT PRIMARY KEY)")

	a.run(t, "SET autocommit = 0")
	a.run(t, "INSERT INTO t VALUES (1)")
	a.run(t, "INSERT INTO t VALUES (2)")
	a.run(t, "COMMIT")
	rows := b.run(t, "SELECT id FROM t")
	require.Len(t, rows, 2, "no write of an autocommit = 0 transaction may be lost at a command boundary")
}
