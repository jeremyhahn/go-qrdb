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

package provider

import (
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Constraint DDL on a relational table, through the engine over the real
// in-memory store: CHECK constraints added and dropped by ALTER TABLE, and
// the single PostgreSQL-style constraint namespace DROP CONSTRAINT resolves.

func TestRelationalChecks_AddEnforceDrop(t *testing.T) {
	h := newLockHarness(t)
	c := h.conn(t)
	c.must("CREATE TABLE items (id BIGINT PRIMARY KEY, qty BIGINT)")
	c.must("ALTER TABLE items ADD CONSTRAINT qty_positive CHECK (qty > 0)")

	_, err := c.exec("INSERT INTO items VALUES (1, 0)")
	require.Error(t, err, "the added CHECK is enforced")
	c.must("INSERT INTO items VALUES (1, 5)")

	c.must("ALTER TABLE items DROP CONSTRAINT qty_positive")
	c.must("INSERT INTO items VALUES (2, 0)")
	assert.Len(t, c.must("SELECT * FROM items"), 2)
}

func TestRelationalChecks_AddInsideATransactionCommitsItFirst(t *testing.T) {
	// DDL commits implicitly: the buffered row is committed before the
	// constraint is added, and survives the ROLLBACK that follows.
	h := newLockHarness(t)
	c := h.conn(t)
	c.must("CREATE TABLE items (id BIGINT PRIMARY KEY, qty BIGINT)")
	c.must("BEGIN")
	c.must("INSERT INTO items VALUES (1, 5)")
	c.must("ALTER TABLE items ADD CONSTRAINT qty_positive CHECK (qty > 0)")
	c.must("ROLLBACK")
	assert.Len(t, c.must("SELECT * FROM items"), 1)
}

func TestRelationalDropConstraint_ResolvesUniqueIndexAndForeignKey(t *testing.T) {
	h := newLockHarness(t)
	c := h.conn(t)
	c.must("CREATE TABLE parents (id BIGINT PRIMARY KEY)")
	c.must("CREATE TABLE children (id BIGINT PRIMARY KEY, code BIGINT, parent_id BIGINT, " +
		"CONSTRAINT uq_code UNIQUE (code), " +
		"CONSTRAINT fk_parent FOREIGN KEY (parent_id) REFERENCES parents (id))")
	c.must("INSERT INTO parents VALUES (1)")
	c.must("INSERT INTO children VALUES (10, 7, 1)")

	c.must("ALTER TABLE children DROP CONSTRAINT uq_code")
	c.must("INSERT INTO children VALUES (11, 7, 1)") // the duplicate is now allowed

	c.must("ALTER TABLE children DROP CONSTRAINT fk_parent")
	c.must("INSERT INTO children VALUES (12, 8, 99)") // no parent 99, no FK left

	_, err := c.exec("ALTER TABLE children DROP CONSTRAINT no_such_constraint")
	require.Error(t, err)
}

func TestRelationalIndexOrigin_StatementIndexAndForeignKeyIndex(t *testing.T) {
	h := newLockHarness(t)
	c := h.conn(t)
	c.must("CREATE TABLE parents (id BIGINT PRIMARY KEY)")
	c.must("CREATE TABLE children (id BIGINT PRIMARY KEY, code BIGINT, tag BIGINT, parent_id BIGINT, " +
		"CONSTRAINT uq_code UNIQUE (code), " +
		"CONSTRAINT fk_parent FOREIGN KEY (parent_id) REFERENCES parents (id))")

	// The PostgreSQL wire marks a standalone CREATE INDEX this way.
	c.sess.SetStatementIndexOrigin(true)
	c.must("CREATE UNIQUE INDEX uq_tag ON children (tag)")
	c.sess.SetStatementIndexOrigin(false)
	assert.False(t, c.sess.StatementIndexOrigin())

	tbl, _ := lockTestTable(t, h, c, "children")

	statementIndex, ok := tbl.IsStatementIndex("uq_tag")
	require.True(t, ok)
	assert.True(t, statementIndex, "created by CREATE INDEX")
	statementIndex, ok = tbl.IsStatementIndex("uq_code")
	require.True(t, ok)
	assert.False(t, statementIndex, "declared as a constraint")
	_, ok = tbl.IsStatementIndex("missing")
	assert.False(t, ok)

	backsFK, ok := tbl.IsForeignKeyIndex("uq_code")
	require.True(t, ok)
	assert.False(t, backsFK)
	var fkIndex string
	for _, idx := range tbl.def.Indexes {
		if idx.ForeignKey {
			fkIndex = idx.Name
		}
	}
	require.NotEmpty(t, fkIndex, "the foreign key is backed by a generated index")
	backsFK, ok = tbl.IsForeignKeyIndex(fkIndex)
	require.True(t, ok)
	assert.True(t, backsFK)
	_, ok = tbl.IsForeignKeyIndex("missing")
	assert.False(t, ok)
}

func TestTransactionToken_NamesItsMode(t *testing.T) {
	sess := NewSession(sqle.NewBaseSession(), nil)
	ctx := sqle.NewContext(t.Context(), sqle.WithSession(sess))

	tx, err := sess.StartTransaction(ctx, sqle.ReadOnly)
	require.NoError(t, err)
	assert.Equal(t, "qrdb read-only transaction", tx.String())

	tx, err = sess.StartTransaction(ctx, sqle.ReadWrite)
	require.NoError(t, err)
	assert.Equal(t, "qrdb transaction", tx.String())
}

func TestLockingScan_IncludesRowsAlreadyLocked(t *testing.T) {
	// A full-table locking read after a point locking read in the same
	// transaction: the row locked first is part of the scan, alongside the
	// transaction's own uncommitted insert.
	h := newLockHarness(t)
	admin := h.conn(t)
	admin.must("CREATE TABLE acct (id BIGINT PRIMARY KEY, bal BIGINT)")
	admin.must("INSERT INTO acct VALUES (1, 10), (2, 20)")

	c := h.conn(t)
	c.must("SET qrdb_concurrency = 'pessimistic'")
	c.must("BEGIN")
	require.Len(t, c.must("SELECT * FROM acct WHERE id = 1 FOR UPDATE"), 1)
	c.must("INSERT INTO acct VALUES (3, 30)")
	rows := c.must("SELECT id FROM acct ORDER BY id FOR UPDATE")
	assert.Equal(t, []any{[]any{int64(1)}, []any{int64(2)}, []any{int64(3)}}, rowsOf(rows))

	// Every committed row is now locked: another session cannot take one.
	other := h.conn(t)
	other.must("BEGIN")
	_, err := other.exec("SELECT * FROM acct WHERE id = 2 FOR UPDATE NOWAIT")
	require.Error(t, err)
	other.must("ROLLBACK")
	c.must("COMMIT")
}
