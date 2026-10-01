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
	"time"

	"github.com/dolthub/vitess/go/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A pessimistic DELETE must never delete a row that, once its lock is
// granted, no longer matches what the statement matched: that is a lost
// update of the writer it waited for.

func TestPessimistic_AliasedDelete_DoesNotDeleteARowThatStoppedMatching(t *testing.T) {
	h := newLockHarness(t)
	setupAccounts(t, h)
	s1, s2 := h.conn(t), h.conn(t)
	s1.must("SET qrdb_concurrency = 'pessimistic'")
	s2.must("SET qrdb_concurrency = 'pessimistic'")

	s1.must("BEGIN")
	s1.must("UPDATE accounts SET balance = -5 WHERE id = 1")
	done := s2.async("DELETE a FROM accounts a WHERE a.balance >= 100")
	blockedFor(t, done, 200*time.Millisecond, "aliased pessimistic DELETE")
	s1.must("COMMIT")
	require.NoError(t, awaitResult(t, done, "aliased delete"))

	rows := h.conn(t).must("SELECT id, balance FROM accounts ORDER BY id")
	require.Len(t, rows, 1, "row 2 (balance 200) is deleted, row 1 (now -5) is not")
	assert.EqualValues(t, 1, rows[0][0])
	assert.EqualValues(t, -5, rows[0][1])
}

func TestPessimistic_CascadeDelete_RefusesAChildReparentedMeanwhile(t *testing.T) {
	h := newLockHarness(t)
	admin := h.conn(t)
	admin.must("CREATE TABLE parents (id BIGINT PRIMARY KEY)")
	admin.must("CREATE TABLE children (id BIGINT PRIMARY KEY, parent_id BIGINT, " +
		"CONSTRAINT fk_parent FOREIGN KEY (parent_id) REFERENCES parents (id) ON DELETE CASCADE)")
	admin.must("INSERT INTO parents VALUES (1), (2)")
	admin.must("INSERT INTO children VALUES (10, 1)")

	mover, deleter := h.conn(t), h.conn(t)
	mover.must("SET qrdb_concurrency = 'pessimistic'")
	deleter.must("SET qrdb_concurrency = 'pessimistic'")
	mover.must("BEGIN")
	mover.must("UPDATE children SET parent_id = 2 WHERE id = 10")

	deleter.must("BEGIN")
	done := deleter.async("DELETE FROM parents WHERE id = 1")
	blockedFor(t, done, 200*time.Millisecond, "cascade delete of the child")
	mover.must("COMMIT")

	// The child the cascade matched now belongs to parent 2: deleting it
	// would lose the move. The statement fails and is undone.
	requireSQLError(t, awaitResult(t, done, "cascade delete"), mysql.ERLockDeadlock)
	deleter.must("ROLLBACK")

	rows := admin.must("SELECT id, parent_id FROM children")
	require.Len(t, rows, 1)
	assert.EqualValues(t, 2, rows[0][1])
	assert.Len(t, admin.must("SELECT * FROM parents"), 2)
}

func TestPessimistic_Delete_RowDeletedMeanwhileFailsTheStatement(t *testing.T) {
	// The matched row was deleted by the writer the DELETE waited for: the
	// statement fails rather than report a delete it did not make.
	h := newLockHarness(t)
	admin := h.conn(t)
	admin.must("CREATE TABLE parents (id BIGINT PRIMARY KEY)")
	admin.must("CREATE TABLE children (id BIGINT PRIMARY KEY, parent_id BIGINT, " +
		"CONSTRAINT fk_parent FOREIGN KEY (parent_id) REFERENCES parents (id) ON DELETE CASCADE)")
	admin.must("INSERT INTO parents VALUES (1)")
	admin.must("INSERT INTO children VALUES (10, 1)")

	remover, deleter := h.conn(t), h.conn(t)
	remover.must("SET qrdb_concurrency = 'pessimistic'")
	deleter.must("SET qrdb_concurrency = 'pessimistic'")
	remover.must("BEGIN")
	remover.must("DELETE FROM children WHERE id = 10")

	deleter.must("BEGIN")
	done := deleter.async("DELETE FROM parents WHERE id = 1")
	blockedFor(t, done, 200*time.Millisecond, "cascade delete of the child")
	remover.must("COMMIT")
	requireSQLError(t, awaitResult(t, done, "cascade delete"), mysql.ERLockDeadlock)
	deleter.must("ROLLBACK")
	assert.Len(t, admin.must("SELECT * FROM parents"), 1)
}
