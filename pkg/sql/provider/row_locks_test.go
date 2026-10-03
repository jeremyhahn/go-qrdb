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
	"errors"
	"testing"
	"time"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/vitess/go/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/rowlock"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

const accountsDDL = "CREATE TABLE accounts (id BIGINT PRIMARY KEY, balance BIGINT NOT NULL)"

func setupAccounts(t *testing.T, h *lockHarness) *lockConn {
	t.Helper()
	admin := h.conn(t)
	admin.must(accountsDDL)
	admin.must("INSERT INTO accounts VALUES (1, 100), (2, 200)")
	return admin
}

// ---------------------------------------------------------------------------
// FOR UPDATE / FOR SHARE in optimistic (default) mode
// ---------------------------------------------------------------------------

func TestForUpdate_SerialisesTwoSessions_AndTheSecondReadsTheCommittedRow(t *testing.T) {
	h := newLockHarness(t)
	setupAccounts(t, h)
	s1, s2 := h.conn(t), h.conn(t)

	s1.must("BEGIN")
	rows := s1.must("SELECT balance FROM accounts WHERE id = 1 FOR UPDATE")
	require.Len(t, rows, 1)

	s2.must("BEGIN")
	// s2's snapshot is taken now, before s1 commits; its locking read must
	// nevertheless see s1's committed write, not the snapshot.
	s2.must("SELECT balance FROM accounts WHERE id = 2")
	read := make(chan []sqle.Row, 1)
	done := make(chan error, 1)
	go func() {
		r, err := s2.exec("SELECT balance FROM accounts WHERE id = 1 FOR UPDATE")
		read <- r
		done <- err
	}()
	blockedFor(t, done, 200*time.Millisecond, "a second FOR UPDATE of the same row")

	s1.must("UPDATE accounts SET balance = balance - 10 WHERE id = 1")
	s1.must("COMMIT")
	require.NoError(t, awaitResult(t, done, "second FOR UPDATE"))
	got := <-read
	require.Len(t, got, 1)
	assert.EqualValues(t, 90, got[0][0], "a locking read is a current read")

	s2.must("UPDATE accounts SET balance = balance - 10 WHERE id = 1")
	s2.must("COMMIT")
	assert.EqualValues(t, 80, h.conn(t).must("SELECT balance FROM accounts WHERE id = 1")[0][0])
	assert.Zero(t, h.lockRecords(t), "every lock is released at commit")
}

func TestForShare_IsSharedAndExcludesWriters(t *testing.T) {
	h := newLockHarness(t)
	setupAccounts(t, h)
	s1, s2, s3 := h.conn(t), h.conn(t), h.conn(t)

	s1.must("BEGIN")
	s1.must("SELECT * FROM accounts WHERE id = 1 FOR SHARE")
	s2.must("BEGIN")
	s2.must("SELECT * FROM accounts WHERE id = 1 LOCK IN SHARE MODE")

	s3.must("BEGIN")
	_, err := s3.exec("SELECT * FROM accounts WHERE id = 1 FOR UPDATE NOWAIT")
	requireSQLError(t, err, rowlock.SQLCodeLockNowait)

	s1.must("ROLLBACK")
	s2.must("ROLLBACK")
	s3.must("SELECT * FROM accounts WHERE id = 1 FOR UPDATE NOWAIT")
	s3.must("COMMIT")
}

func TestForUpdate_LockWaitTimeout_FailsOnlyTheStatement(t *testing.T) {
	h := newLockHarness(t)
	setupAccounts(t, h)
	s1, s2 := h.conn(t), h.conn(t)

	s1.must("BEGIN")
	s1.must("SELECT * FROM accounts WHERE id = 1 FOR UPDATE")

	s2.must("SET innodb_lock_wait_timeout = 1")
	s2.must("BEGIN")
	s2.must("UPDATE accounts SET balance = 0 WHERE id = 2")
	start := time.Now()
	_, err := s2.exec("SELECT * FROM accounts WHERE id = 1 FOR UPDATE")
	requireSQLError(t, err, mysql.ERLockWaitTimeout)
	assert.GreaterOrEqual(t, time.Since(start), time.Second)

	// The transaction is still open and still holds its write.
	s2.must("COMMIT")
	assert.EqualValues(t, 0, h.conn(t).must("SELECT balance FROM accounts WHERE id = 2")[0][0])
	s1.must("COMMIT")
}

func TestInnodbLockWaitTimeout_AcceptsMySQLRange(t *testing.T) {
	h := newLockHarness(t)
	c := h.conn(t)
	// go-mysql-server registers this variable with the range [1, 1]; 50 is
	// MySQL's default and the value every client library sets.
	c.must("SET innodb_lock_wait_timeout = 50")
	c.must("SET SESSION innodb_lock_wait_timeout = 1073741824")
	rows := c.must("SELECT @@innodb_lock_wait_timeout")
	assert.EqualValues(t, 1073741824, rows[0][0])
	_, err := c.exec("SET innodb_lock_wait_timeout = 0")
	require.Error(t, err)
}

func TestForUpdate_Deadlock_VictimIsRolledBackAndAborted(t *testing.T) {
	h := newLockHarness(t)
	setupAccounts(t, h)
	s1, s2 := h.conn(t), h.conn(t)

	s1.must("BEGIN")
	s1.must("UPDATE accounts SET balance = balance + 1 WHERE id = 1")
	s1.must("SELECT * FROM accounts WHERE id = 1 FOR UPDATE")
	s2.must("BEGIN")
	s2.must("UPDATE accounts SET balance = balance + 1 WHERE id = 2")
	s2.must("SELECT * FROM accounts WHERE id = 2 FOR UPDATE")

	s1Wait := s1.async("SELECT * FROM accounts WHERE id = 2 FOR UPDATE")
	blockedFor(t, s1Wait, 200*time.Millisecond, "s1 waiting on s2")

	_, err := s2.exec("SELECT * FROM accounts WHERE id = 1 FOR UPDATE")
	requireSQLError(t, err, mysql.ERLockDeadlock)
	var dl *rowlock.DeadlockError
	require.True(t, service.AsCause(err, &dl), "the typed cause is reachable: %v", err)

	require.NoError(t, awaitResult(t, s1Wait, "the survivor"))

	// The victim's transaction is gone: its write is discarded, and every
	// further statement fails until it is ended.
	_, err = s2.exec("SELECT * FROM accounts WHERE id = 2")
	requireSQLError(t, err, mysql.ERLockDeadlock)
	var aborted *TransactionAbortedError
	require.True(t, service.AsCause(err, &aborted))
	s2.must("ROLLBACK")
	s2.must("SELECT * FROM accounts WHERE id = 2")

	s1.must("COMMIT")
	rows := h.conn(t).must("SELECT balance FROM accounts ORDER BY id")
	assert.EqualValues(t, 101, rows[0][0])
	assert.EqualValues(t, 200, rows[1][0], "the victim's buffered write never committed")
}

func TestForUpdate_DeadlockVictim_CommitReportsTheAbort(t *testing.T) {
	h := newLockHarness(t)
	setupAccounts(t, h)
	s1, s2 := h.conn(t), h.conn(t)
	s1.must("BEGIN")
	s1.must("SELECT * FROM accounts WHERE id = 1 FOR UPDATE")
	s2.must("BEGIN")
	s2.must("SELECT * FROM accounts WHERE id = 2 FOR UPDATE")
	s1Wait := s1.async("SELECT * FROM accounts WHERE id = 2 FOR UPDATE")
	blockedFor(t, s1Wait, 150*time.Millisecond, "s1")
	_, err := s2.exec("SELECT * FROM accounts WHERE id = 1 FOR UPDATE")
	requireSQLError(t, err, mysql.ERLockDeadlock)

	_, err = s2.exec("COMMIT")
	requireSQLError(t, err, mysql.ERLockDeadlock)

	// The COMMIT ended the transaction: the next statement is autocommit,
	// takes its own locks, and is refused while s1 holds the row.
	require.NoError(t, awaitResult(t, s1Wait, "s1"))
	_, err = s2.exec("SELECT * FROM accounts WHERE id = 1 FOR SHARE NOWAIT")
	requireSQLError(t, err, rowlock.SQLCodeLockNowait)
	s1.must("COMMIT")
	s2.must("SELECT * FROM accounts WHERE id = 1 FOR SHARE NOWAIT")
	assert.Zero(t, h.lockRecords(t))
}

func TestSkipLocked_IsRefusedByName(t *testing.T) {
	h := newLockHarness(t)
	setupAccounts(t, h)
	c := h.conn(t)
	_, err := c.exec("SELECT * FROM accounts FOR UPDATE SKIP LOCKED")
	requireSQLError(t, err, SQLCodeSkipLockedUnsupported)
	_, err = c.exec("SELECT * FROM accounts FOR SHARE SKIP LOCKED")
	requireSQLError(t, err, SQLCodeSkipLockedUnsupported)
}

func TestForUpdate_FullScanLocksEveryRow(t *testing.T) {
	h := newLockHarness(t)
	setupAccounts(t, h)
	s1, s2 := h.conn(t), h.conn(t)
	s1.must("BEGIN")
	s1.must("SELECT * FROM accounts WHERE balance > 150 FOR UPDATE")
	// The predicate is not indexed: the engine filters above the table, so
	// the table locks every row it returned, as InnoDB does.
	_, err := s2.exec("SELECT * FROM accounts WHERE id = 1 FOR UPDATE NOWAIT")
	requireSQLError(t, err, rowlock.SQLCodeLockNowait)
	s1.must("ROLLBACK")
	assert.Zero(t, h.lockRecords(t))
}

func TestForUpdate_SubqueryWithoutClause_LocksNothing(t *testing.T) {
	h := newLockHarness(t)
	admin := setupAccounts(t, h)
	admin.must("CREATE TABLE owners (id BIGINT PRIMARY KEY, account_id BIGINT)")
	admin.must("INSERT INTO owners VALUES (7, 1)")
	s1, s2 := h.conn(t), h.conn(t)
	s1.must("BEGIN")
	s1.must("SELECT * FROM accounts WHERE id IN (SELECT account_id FROM owners WHERE id = 7) FOR UPDATE")
	s2.must("SELECT * FROM owners WHERE id = 7 FOR UPDATE NOWAIT")
	s1.must("COMMIT")
}

// ---------------------------------------------------------------------------
// Pessimistic mode
// ---------------------------------------------------------------------------

func TestPessimistic_ConcurrentIncrements_WaitInsteadOfAborting(t *testing.T) {
	h := newLockHarness(t)
	setupAccounts(t, h)
	s1, s2 := h.conn(t), h.conn(t)
	for _, c := range []*lockConn{s1, s2} {
		c.must("SET qrdb_concurrency = 'pessimistic'")
	}

	s1.must("BEGIN")
	s2.must("BEGIN")
	s1.must("UPDATE accounts SET balance = balance + 1 WHERE id = 1")
	s2Done := s2.async("UPDATE accounts SET balance = balance + 1 WHERE id = 1")
	blockedFor(t, s2Done, 200*time.Millisecond, "a second pessimistic writer of the row")
	s1.must("COMMIT")
	require.NoError(t, awaitResult(t, s2Done, "second writer"))
	s2.must("COMMIT")

	assert.EqualValues(t, 102, h.conn(t).must("SELECT balance FROM accounts WHERE id = 1")[0][0],
		"both increments applied, neither lost nor aborted")
}

func TestOptimistic_ConcurrentIncrements_SecondCommitAborts(t *testing.T) {
	// The contrast case, unchanged by this feature: the default mode takes
	// no locks and settles the race at COMMIT.
	h := newLockHarness(t)
	setupAccounts(t, h)
	s1, s2 := h.conn(t), h.conn(t)
	s1.must("BEGIN")
	s2.must("BEGIN")
	s1.must("UPDATE accounts SET balance = balance + 1 WHERE id = 1")
	s2.must("UPDATE accounts SET balance = balance + 1 WHERE id = 1")
	s1.must("COMMIT")
	_, err := s2.exec("COMMIT")
	requireSQLError(t, err, mysql.ERLockDeadlock)
	assert.Zero(t, h.lockRecords(t))
}

func TestPessimistic_DatabaseDefault_AppliesWithoutSessionOverride(t *testing.T) {
	h := newLockHarness(t, WithSQLConfig(func(_, name string) *database.SQLConfig {
		return &database.SQLConfig{Concurrency: database.ConcurrencyPessimistic}
	}))
	setupAccounts(t, h)
	s1, s2 := h.conn(t), h.conn(t)
	s1.must("BEGIN")
	s1.must("UPDATE accounts SET balance = 1 WHERE id = 1")
	s2.must("SET innodb_lock_wait_timeout = 1")
	_, err := s2.exec("UPDATE accounts SET balance = 2 WHERE id = 1") // autocommit
	requireSQLError(t, err, mysql.ERLockWaitTimeout)

	// A session can opt back out: its write takes no lock and is settled
	// by the commit CAS instead.
	s2.must("SET qrdb_concurrency = 'optimistic'")
	s2.must("UPDATE accounts SET balance = 2 WHERE id = 1")
	_, err = s1.exec("COMMIT")
	requireSQLError(t, err, mysql.ERLockDeadlock)
}

func TestPessimistic_ConcurrentInsertIgnore_SecondSeesTheFirstRow(t *testing.T) {
	h := newLockHarness(t)
	admin := h.conn(t)
	admin.must("CREATE TABLE langs (code VARCHAR(16) PRIMARY KEY, n BIGINT)")
	s1, s2 := h.conn(t), h.conn(t)
	for _, c := range []*lockConn{s1, s2} {
		c.must("SET qrdb_concurrency = 'pessimistic'")
		c.must("BEGIN")
	}
	s1.must("INSERT INTO langs VALUES ('en', 1)")
	s2Done := s2.async("INSERT IGNORE INTO langs VALUES ('en', 2)")
	blockedFor(t, s2Done, 200*time.Millisecond, "second insert of the same key")
	s1.must("COMMIT")
	require.NoError(t, awaitResult(t, s2Done, "second insert"))
	s2.must("COMMIT")
	assert.EqualValues(t, 1, admin.must("SELECT n FROM langs WHERE code = 'en'")[0][0])
}

func TestPessimistic_ConcurrentInsert_SecondGetsDuplicateKey(t *testing.T) {
	h := newLockHarness(t)
	admin := h.conn(t)
	admin.must("CREATE TABLE langs (code VARCHAR(16) PRIMARY KEY, n BIGINT)")
	s1, s2 := h.conn(t), h.conn(t)
	for _, c := range []*lockConn{s1, s2} {
		c.must("SET qrdb_concurrency = 'pessimistic'")
		c.must("BEGIN")
	}
	s1.must("INSERT INTO langs VALUES ('en', 1)")
	s2Done := s2.async("INSERT INTO langs VALUES ('en', 2)")
	s1.must("COMMIT")
	requireSQLError(t, awaitResult(t, s2Done, "second insert"), mysql.ERDupEntry)
	s2.must("ROLLBACK")
}

func TestPessimistic_AutocommitUpdate_WaitsForForUpdateHolder(t *testing.T) {
	h := newLockHarness(t)
	setupAccounts(t, h)
	holder, writer := h.conn(t), h.conn(t)
	writer.must("SET qrdb_concurrency = 'pessimistic'")

	holder.must("BEGIN")
	holder.must("SELECT * FROM accounts WHERE id = 1 FOR UPDATE")
	done := writer.async("UPDATE accounts SET balance = balance * 2 WHERE id = 1")
	blockedFor(t, done, 200*time.Millisecond, "autocommit pessimistic UPDATE")
	holder.must("UPDATE accounts SET balance = 7 WHERE id = 1")
	holder.must("COMMIT")
	require.NoError(t, awaitResult(t, done, "writer"))
	assert.EqualValues(t, 14, h.conn(t).must("SELECT balance FROM accounts WHERE id = 1")[0][0])
	assert.Zero(t, h.lockRecords(t), "an autocommit statement's locks last the statement")
}

func TestPessimistic_Delete_WaitsAndSeesCurrentState(t *testing.T) {
	h := newLockHarness(t)
	setupAccounts(t, h)
	s1, s2 := h.conn(t), h.conn(t)
	s1.must("SET qrdb_concurrency = 'pessimistic'")
	s2.must("SET qrdb_concurrency = 'pessimistic'")
	s1.must("BEGIN")
	s1.must("UPDATE accounts SET balance = 0 WHERE id = 2")
	done := s2.async("DELETE FROM accounts WHERE id = 2")
	blockedFor(t, done, 200*time.Millisecond, "pessimistic DELETE")
	s1.must("COMMIT")
	require.NoError(t, awaitResult(t, done, "delete"))
	assert.Empty(t, h.conn(t).must("SELECT * FROM accounts WHERE id = 2"))
}

// ---------------------------------------------------------------------------
// Session variables and release points
// ---------------------------------------------------------------------------

func TestQrdbConcurrency_Variable(t *testing.T) {
	h := newLockHarness(t)
	c := h.conn(t)
	assert.Equal(t, ConcurrencyDatabase, c.must("SELECT @@qrdb_concurrency")[0][0])
	c.must("SET qrdb_concurrency = 'pessimistic'")
	assert.Equal(t, "pessimistic", c.must("SELECT @@qrdb_concurrency")[0][0])
	_, err := c.exec("SET qrdb_concurrency = 'eager'")
	require.Error(t, err)
	_, err = c.exec("SET GLOBAL qrdb_concurrency = 'pessimistic'")
	require.Error(t, err, "the setting is session-scoped only")
}

func TestRelease_RollbackAndSessionEnd_ReleaseLocks(t *testing.T) {
	h := newLockHarness(t)
	setupAccounts(t, h)
	s1 := h.conn(t)
	s1.must("BEGIN")
	s1.must("SELECT * FROM accounts FOR UPDATE")
	require.NotZero(t, h.lockRecords(t))
	s1.must("ROLLBACK")
	assert.Zero(t, h.lockRecords(t))

	s2 := h.conn(t)
	s2.must("BEGIN")
	s2.must("SELECT * FROM accounts WHERE id = 1 FOR SHARE")
	require.NotZero(t, h.lockRecords(t))
	s2.sess.SessionEnd()
	assert.Zero(t, h.lockRecords(t), "a closed connection's locks are released, not left to expire")
}

func TestRelease_DDLImplicitCommitReleasesLocks(t *testing.T) {
	h := newLockHarness(t)
	setupAccounts(t, h)
	s1 := h.conn(t)
	s1.must("BEGIN")
	s1.must("SELECT * FROM accounts WHERE id = 1 FOR UPDATE")
	s1.must("CREATE TABLE other (id BIGINT PRIMARY KEY)")
	assert.Zero(t, h.lockRecords(t))
}

func TestForUpdate_WithoutLockTable_IsRefused(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	conn := newTxConn(t, prov, "shop")
	conn.mustExec(t, accountsDDL)
	conn.mustExec(t, "INSERT INTO accounts VALUES (1, 1)")

	ctx := sqle.NewContext(t.Context(), sqle.WithSession(conn.sess), sqle.WithQuery("SELECT * FROM accounts WHERE id = 1 FOR UPDATE"))
	ctx.SetCurrentDatabase("shop")
	_, iter, _, err := conn.eng.Query(ctx, ctx.Query())
	if err == nil {
		_, err = sqle.RowIterToRows(ctx, iter)
	}
	var missing *rowlock.MissingDependencyError
	require.True(t, errors.As(err, &missing), "a composition with no lease service cannot lock: %v", err)
}
