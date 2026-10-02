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
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStatement_WritingRelationalAndKVTablesIsRefused: an autocommit
// statement is atomic, and a kv write cannot join its transaction, so a
// statement that writes both kinds is refused before it writes either.
func TestStatement_WritingRelationalAndKVTablesIsRefused(t *testing.T) {
	h := newDDLHarness()
	c := newTxConn(t, h.newDDLProvider("shop"), "shop")
	c.mustExec(t, "CREATE TABLE t (id BIGINT PRIMARY KEY, v VARCHAR(8))")
	c.mustExec(t, "INSERT INTO t VALUES (1, 'a')")
	c.mustExec(t, "INSERT INTO kv (`key`, value) VALUES ('k', 'a')")

	_, _, err := c.exec("UPDATE t JOIN kv ON kv.`key` = 'k' SET t.v = 'x', kv.value = 'x'")
	var mixed *MixedTransactionalWriteError
	require.ErrorAs(t, err, &mixed)
	assert.Equal(t, "t", mixed.Relational)
	assert.Equal(t, "kv", mixed.NonTransactional)

	_, rows := c.mustExec(t, "SELECT v FROM t WHERE id = 1")
	assert.Equal(t, "a", rows[0][0], "the relational row is unchanged")
	raw, err := h.store.Get(context.Background(), "k")
	require.NoError(t, err)
	assert.Equal(t, "a", string(raw), "the kv row is unchanged")
}

// TestMixedWriteMarks_EndWithTheTransaction: which kinds of table a
// statement writes is noted on the write-set for the statement's own
// mixed-write check, and must never reach a later statement -- even one no
// command bracket precedes. A transaction starts with no marks, and its
// commit clears them, including the commit that fails and keeps its
// write-set for a retry.
func TestMixedWriteMarks_EndWithTheTransaction(t *testing.T) {
	ctx, sess := newTxContext(true)
	_, err := sess.StartTransaction(ctx, sqle.ReadWrite)
	require.NoError(t, err)
	ws := sess.writeSet()
	require.NoError(t, ws.bind(ctx, "t1", "db1", &errTxn{}, newMemStore()))
	require.NoError(t, ws.Put(ctx, "k1", []byte("v1")))
	ws.noteRelationalWriter("t1")
	noteNonTransactionalWriter(ctx, "kv")
	require.Equal(t, "kv", ws.nonTransactionalWriter, "the statement's marks are noted")

	require.ErrorIs(t, sess.CommitTransaction(ctx, nil), errBoom)
	require.Same(t, ws, sess.writeSet(), "a commit that fails on infrastructure keeps its write-set")
	assert.Empty(t, ws.relationalWriter, "the kept write-set carries no statement's marks")
	assert.Empty(t, ws.nonTransactionalWriter, "the kept write-set carries no statement's marks")

	ws.noteRelationalWriter("t1")
	_, err = sess.StartTransaction(ctx, sqle.ReadWrite)
	require.NoError(t, err)
	fresh := sess.writeSet()
	assert.Empty(t, fresh.relationalWriter, "a transaction starts with no marks")
	assert.Empty(t, fresh.nonTransactionalWriter, "a transaction starts with no marks")
}
