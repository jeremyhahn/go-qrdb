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
