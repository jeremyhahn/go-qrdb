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
	"github.com/dolthub/vitess/go/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Statement-level atomicity inside a transaction: a statement that fails
// part way leaves none of its effects behind, whatever made it fail, while
// every earlier statement of the transaction is kept (InnoDB semantics).

func TestStatementAtomicity_DuplicateKeyUndoesTheWholeStatement(t *testing.T) {
	h := newLockHarness(t)
	c := h.conn(t)
	c.must("CREATE TABLE p (id BIGINT PRIMARY KEY)")
	c.must("INSERT INTO p VALUES (5)")

	c.must("BEGIN")
	c.must("INSERT INTO p VALUES (7)")
	_, err := c.exec("INSERT INTO p VALUES (1), (2), (5)")
	require.Error(t, err)
	assert.Equal(t, []any{[]any{int64(5)}, []any{int64(7)}}, rowsOf(c.must("SELECT id FROM p ORDER BY id")),
		"inside the transaction the failed statement left nothing; the earlier one is kept")
	c.must("COMMIT")

	assert.Equal(t, []any{[]any{int64(5)}, []any{int64(7)}}, rowsOf(c.must("SELECT id FROM p ORDER BY id")))
}

func TestStatementAtomicity_FailedUpdateRestoresEveryRow(t *testing.T) {
	h := newLockHarness(t)
	c := h.conn(t)
	c.must("CREATE TABLE u (id BIGINT PRIMARY KEY, code BIGINT, UNIQUE KEY uq_code (code))")
	c.must("INSERT INTO u VALUES (1, 10), (2, 20), (3, 30)")

	c.must("BEGIN")
	// The second UPDATE rewrites id 2 to 32, then collides on id 3.
	c.must("UPDATE u SET code = code + 1")
	_, err := c.exec("UPDATE u SET code = 32 WHERE id >= 2")
	require.Error(t, err)
	c.must("COMMIT")

	assert.Equal(t,
		[]any{[]any{int64(1), int64(11)}, []any{int64(2), int64(21)}, []any{int64(3), int64(31)}},
		rowsOf(c.must("SELECT id, code FROM u ORDER BY id")))
}

func TestStatementAtomicity_InsertIgnoreKeepsTheGoodRows(t *testing.T) {
	// INSERT IGNORE checkpoints each row on its own: only the ignored row is
	// discarded, the statement's other rows stay.
	h := newLockHarness(t)
	c := h.conn(t)
	c.must("CREATE TABLE p (id BIGINT PRIMARY KEY)")
	c.must("INSERT INTO p VALUES (5)")

	c.must("BEGIN")
	c.must("INSERT IGNORE INTO p VALUES (1), (5), (2)")
	c.must("COMMIT")

	assert.Equal(t, []any{[]any{int64(1)}, []any{int64(2)}, []any{int64(5)}},
		rowsOf(c.must("SELECT id FROM p ORDER BY id")))
}

func TestStatementAtomicity_UserSavepointsStillWork(t *testing.T) {
	h := newLockHarness(t)
	c := h.conn(t)
	c.must("CREATE TABLE p (id BIGINT PRIMARY KEY)")

	c.must("BEGIN")
	c.must("INSERT INTO p VALUES (1)")
	c.must("SAVEPOINT s1")
	c.must("INSERT INTO p VALUES (2)")
	_, err := c.exec("INSERT INTO p VALUES (3), (2)")
	require.Error(t, err)
	c.must("ROLLBACK TO SAVEPOINT s1")
	c.must("INSERT INTO p VALUES (4)")
	c.must("COMMIT")

	assert.Equal(t, []any{[]any{int64(1)}, []any{int64(4)}},
		rowsOf(c.must("SELECT id FROM p ORDER BY id")))
}

func TestStatementAtomicity_CascadeTimeoutLeavesTheParent(t *testing.T) {
	h := newLockHarness(t)
	admin := h.conn(t)
	admin.must("CREATE TABLE parents (id BIGINT PRIMARY KEY)")
	admin.must("CREATE TABLE children (id BIGINT PRIMARY KEY, parent_id BIGINT, " +
		"CONSTRAINT fk_parent FOREIGN KEY (parent_id) REFERENCES parents (id) ON DELETE SET NULL)")
	admin.must("INSERT INTO parents VALUES (1)")
	admin.must("INSERT INTO children VALUES (10, 1)")

	holder, deleter := h.conn(t), h.conn(t)
	holder.must("BEGIN")
	holder.must("SELECT * FROM children WHERE id = 10 FOR UPDATE")

	deleter.must("SET qrdb_concurrency = 'pessimistic'")
	deleter.must("SET innodb_lock_wait_timeout = 1")
	deleter.must("BEGIN")
	_, err := deleter.exec("DELETE FROM parents WHERE id = 1")
	requireSQLError(t, err, mysql.ERLockWaitTimeout)
	assert.Len(t, deleter.must("SELECT * FROM parents"), 1,
		"the timed-out cascade undid the parent delete it had already made")

	holder.must("COMMIT")
	// The transaction is still open after a lock-wait timeout, and COMMIT
	// keeps the parent: the failed statement is gone, not deferred.
	deleter.must("COMMIT")
	assert.Len(t, admin.must("SELECT * FROM parents"), 1)
	rows := admin.must("SELECT parent_id FROM children WHERE id = 10")
	require.Len(t, rows, 1)
	assert.EqualValues(t, 1, rows[0][0])
}

// rowsOf converts engine rows to plain slices for comparison.
func rowsOf(rows []sqle.Row) []any {
	out := make([]any, len(rows))
	for i, r := range rows {
		out[i] = []any(r)
	}
	return out
}
