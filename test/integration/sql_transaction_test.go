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

//go:build integration

// sql_transaction_test.go proves qrdb's interactive transactions behave like
// MySQL's across a real 3-node cluster.
//
// A qrdb transaction buffers its writes in a session-local WriteSet and
// commits the whole set as ONE revision-guarded compare-and-swap through Raft
// (see pkg/sql/provider/session.go and writeset.go). Isolation is therefore
// snapshot / first-committer-wins, NOT two-phase locking: a losing writer is
// not blocked, it is REJECTED at COMMIT. The single most important assertion
// in this file is that the rejection surfaces as MySQL error 1213 / SQLSTATE
// 40001 (ER_LOCK_DEADLOCK) -- the code every mature MySQL client and ORM
// already knows means "retry the transaction". That is what makes qrdb's
// optimistic concurrency survivable for an application that was never
// modified for it, and it is only meaningful when the two racing sessions are
// on DIFFERENT REPLICAS, which is what this file exercises.
package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
)

// erLockDeadlock is MySQL's ER_LOCK_DEADLOCK. go-mysql-server's
// sql.CastSQLError maps sql.ErrLockDeadlock -- which QRDBSession.
// CommitTransaction wraps every compare-and-swap conflict in -- onto this
// code and SQLSTATE 40001.
const erLockDeadlock = 1213

// sqlStateSerializationFailure is the SQLSTATE that accompanies
// ER_LOCK_DEADLOCK. Clients that key off SQLSTATE rather than the vendor
// code (and the SQL standard's own serialization-failure class) see this.
const sqlStateSerializationFailure = "40001"

// assertLockDeadlock fails unless err is a MySQL protocol error carrying
// ER_LOCK_DEADLOCK and SQLSTATE 40001. Both are asserted: a client that only
// understands the vendor code and a client that only understands SQLSTATE
// must each be able to recognize the conflict.
func assertLockDeadlock(t *testing.T, label string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected MySQL error %d (SQLSTATE %s), got nil",
			label, erLockDeadlock, sqlStateSerializationFailure)
	}
	var myErr *gomysql.MySQLError
	if !errors.As(err, &myErr) {
		t.Fatalf("%s: expected a *mysql.MySQLError carrying %d, got %T: %v",
			label, erLockDeadlock, err, err)
	}
	if myErr.Number != erLockDeadlock {
		t.Fatalf("%s: MySQL error number = %d, want %d (ER_LOCK_DEADLOCK); full error: %v",
			label, myErr.Number, erLockDeadlock, err)
	}
	if got := string(myErr.SQLState[:]); got != sqlStateSerializationFailure {
		t.Fatalf("%s: SQLSTATE = %q, want %q; full error: %v",
			label, got, sqlStateSerializationFailure, err)
	}
}

// isLockDeadlock reports whether err is ER_LOCK_DEADLOCK, without failing.
func isLockDeadlock(err error) bool {
	var myErr *gomysql.MySQLError
	return errors.As(err, &myErr) && myErr.Number == erLockDeadlock
}

// scanBalance returns the balance column of row id from q, which may be a
// *sql.DB or a *sql.Tx.
func scanBalance(t *testing.T, q interface {
	QueryRow(string, ...any) *sql.Row
}, label, table string, id int) int {
	t.Helper()
	var balance int
	if err := q.QueryRow(fmt.Sprintf("SELECT balance FROM %s WHERE id = ?", table), id).Scan(&balance); err != nil {
		t.Fatalf("%s: SELECT balance WHERE id=%d: %v", label, id, err)
	}
	return balance
}

// TestSQLCluster_Transaction drives the interactive-transaction surface
// across a 3-node cluster. All phases share one cluster and use disjoint
// tables, so nothing one phase commits can be mistaken for another's.
func TestSQLCluster_Transaction(t *testing.T) {
	c := startThreeNodeClusterWithSQL(t)
	leader := c.leaderIndex(t, 60*time.Second)
	followers := c.followerIndexes(leader)
	if len(followers) != 2 {
		t.Fatalf("expected 2 followers in a 3-node cluster, got %d", len(followers))
	}
	t.Logf("shard 1 leader is replica %d; followers %v", leader+1, followers)

	leaderDB := c.mysql(t, leader)
	followerDB := c.mysql(t, followers[0])
	otherFollowerDB := c.mysql(t, followers[1])
	ctx := context.Background()

	// --- Read-your-own-writes ---------------------------------------------
	//
	// Buffered writes are invisible to the store until COMMIT, so the session
	// that made them must still read them back through its own WriteSet --
	// otherwise every read-modify-write inside a transaction silently sees
	// stale data.
	t.Run("ReadYourOwnWrites", func(t *testing.T) {
		const table = "tx_ryow"
		mustExec(t, leaderDB, fmt.Sprintf(
			"CREATE TABLE %s (id INT PRIMARY KEY, balance INT)", table))
		mustExec(t, leaderDB, fmt.Sprintf("INSERT INTO %s (id, balance) VALUES (1, 10)", table))

		tx, err := leaderDB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("BEGIN: %v", err)
		}
		defer tx.Rollback()

		// An UPDATE of an existing row.
		if _, err := tx.Exec(fmt.Sprintf("UPDATE %s SET balance = 99 WHERE id = 1", table)); err != nil {
			t.Fatalf("UPDATE inside transaction: %v", err)
		}
		if got := scanBalance(t, tx, "in-tx", table, 1); got != 99 {
			t.Fatalf("read-your-own-write of an UPDATE: balance = %d, want 99", got)
		}

		// An INSERT of a brand-new row.
		if _, err := tx.Exec(fmt.Sprintf("INSERT INTO %s (id, balance) VALUES (2, 20)", table)); err != nil {
			t.Fatalf("INSERT inside transaction: %v", err)
		}
		if got := scanBalance(t, tx, "in-tx", table, 2); got != 20 {
			t.Fatalf("read-your-own-write of an INSERT: balance = %d, want 20", got)
		}

		// A DELETE inside the same transaction.
		if _, err := tx.Exec(fmt.Sprintf("DELETE FROM %s WHERE id = 2", table)); err != nil {
			t.Fatalf("DELETE inside transaction: %v", err)
		}
		var balance int
		err = tx.QueryRow(fmt.Sprintf("SELECT balance FROM %s WHERE id = 2", table)).Scan(&balance)
		if err != sql.ErrNoRows {
			t.Fatalf("read-your-own-DELETE: got (%d, %v), want sql.ErrNoRows", balance, err)
		}

		// None of it is visible outside the transaction yet -- on this node
		// or any other.
		if got := scanBalance(t, leaderDB, "leader-outside-tx", table, 1); got != 10 {
			t.Fatalf("uncommitted write leaked to another session on the leader: balance = %d, want 10", got)
		}
		if got := scanBalance(t, followerDB, "follower-outside-tx", table, 1); got != 10 {
			t.Fatalf("uncommitted write leaked to a follower: balance = %d, want 10", got)
		}

		if err := tx.Commit(); err != nil {
			t.Fatalf("COMMIT: %v", err)
		}
	})

	// --- ROLLBACK leaves no trace; COMMIT reaches every replica ------------
	t.Run("RollbackAndCommit", func(t *testing.T) {
		const table = "tx_rollback"
		mustExec(t, leaderDB, fmt.Sprintf(
			"CREATE TABLE %s (id INT PRIMARY KEY, balance INT)", table))
		mustExec(t, leaderDB, fmt.Sprintf("INSERT INTO %s (id, balance) VALUES (1, 10)", table))

		// ROLLBACK.
		tx, err := leaderDB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("BEGIN: %v", err)
		}
		mustTxExec(t, tx, fmt.Sprintf("UPDATE %s SET balance = 777 WHERE id = 1", table))
		mustTxExec(t, tx, fmt.Sprintf("INSERT INTO %s (id, balance) VALUES (2, 222)", table))
		if err := tx.Rollback(); err != nil {
			t.Fatalf("ROLLBACK: %v", err)
		}

		for label, db := range map[string]*sql.DB{
			"leader": leaderDB, "follower": followerDB, "other-follower": otherFollowerDB,
		} {
			if got := scanBalance(t, db, label, table, 1); got != 10 {
				t.Fatalf("%s: balance after ROLLBACK = %d, want 10 (the rolled-back UPDATE left a trace)", label, got)
			}
			var count int
			if err := db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&count); err != nil {
				t.Fatalf("%s: COUNT(*): %v", label, err)
			}
			if count != 1 {
				t.Fatalf("%s: row count after ROLLBACK = %d, want 1 (the rolled-back INSERT left a trace)", label, count)
			}
		}

		// COMMIT.
		tx, err = leaderDB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("BEGIN: %v", err)
		}
		mustTxExec(t, tx, fmt.Sprintf("UPDATE %s SET balance = 555 WHERE id = 1", table))
		mustTxExec(t, tx, fmt.Sprintf("INSERT INTO %s (id, balance) VALUES (3, 333)", table))
		if err := tx.Commit(); err != nil {
			t.Fatalf("COMMIT: %v", err)
		}

		for label, db := range map[string]*sql.DB{
			"leader": leaderDB, "follower": followerDB, "other-follower": otherFollowerDB,
		} {
			if got := scanBalance(t, db, label, table, 1); got != 555 {
				t.Fatalf("%s: balance after COMMIT = %d, want 555", label, got)
			}
			if got := scanBalance(t, db, label, table, 3); got != 333 {
				t.Fatalf("%s: inserted row after COMMIT = %d, want 333", label, got)
			}
		}
	})

	// --- Savepoints --------------------------------------------------------
	//
	// MySQL's rule, which WriteSet.RollbackTo implements: rolling back to a
	// savepoint discards every savepoint established AFTER it, while the
	// savepoint itself remains usable.
	t.Run("Savepoints", func(t *testing.T) {
		const table = "tx_savepoint"
		mustExec(t, leaderDB, fmt.Sprintf(
			"CREATE TABLE %s (id INT PRIMARY KEY, balance INT)", table))

		tx, err := leaderDB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("BEGIN: %v", err)
		}
		defer tx.Rollback()

		mustTxExec(t, tx, fmt.Sprintf("INSERT INTO %s (id, balance) VALUES (1, 1)", table))
		mustTxExec(t, tx, "SAVEPOINT sp1")
		mustTxExec(t, tx, fmt.Sprintf("INSERT INTO %s (id, balance) VALUES (2, 2)", table))
		mustTxExec(t, tx, "SAVEPOINT sp2")
		mustTxExec(t, tx, fmt.Sprintf("INSERT INTO %s (id, balance) VALUES (3, 3)", table))

		// Rows are counted by enumerating them, not with COUNT(*):
		// SELECT COUNT(*) inside a transaction does not see the write-set at
		// all (see AggregateInsideTransactionSeesBufferedRows below, which
		// pins that defect on its own). Enumerating is the instrument that
		// measures what this phase is actually about.
		if got := txIDs(t, tx, table); len(got) != 3 {
			t.Fatalf("rows buffered before any rollback = %v, want 3 rows", got)
		}

		// ROLLBACK TO sp2 undoes only the third INSERT.
		mustTxExec(t, tx, "ROLLBACK TO SAVEPOINT sp2")
		if got := txIDs(t, tx, table); len(got) != 2 || got[0] != 1 || got[1] != 2 {
			t.Fatalf("rows after ROLLBACK TO sp2 = %v, want [1 2]", got)
		}

		// ROLLBACK TO sp1 undoes the second as well. sp1 predates sp2, so it
		// is still live.
		mustTxExec(t, tx, "ROLLBACK TO SAVEPOINT sp1")
		if got := txIDs(t, tx, table); len(got) != 1 || got[0] != 1 {
			t.Fatalf("rows after ROLLBACK TO sp1 = %v, want [1]", got)
		}

		// sp2 was established AFTER sp1 and must have been discarded by the
		// rollback to sp1 -- MySQL semantics. Referencing it now is an error.
		if _, err := tx.Exec("ROLLBACK TO SAVEPOINT sp2"); err == nil {
			t.Fatal("ROLLBACK TO sp2 after rolling back to the earlier sp1 succeeded; MySQL discards savepoints established after the one rolled back to")
		}

		// RELEASE drops a savepoint without touching buffered data.
		mustTxExec(t, tx, fmt.Sprintf("INSERT INTO %s (id, balance) VALUES (4, 4)", table))
		mustTxExec(t, tx, "SAVEPOINT sp3")
		mustTxExec(t, tx, "RELEASE SAVEPOINT sp3")
		if got := txIDs(t, tx, table); len(got) != 2 || got[0] != 1 || got[1] != 4 {
			t.Fatalf("rows after RELEASE SAVEPOINT sp3 = %v, want [1 4] (RELEASE must not discard data)", got)
		}
		if _, err := tx.Exec("ROLLBACK TO SAVEPOINT sp3"); err == nil {
			t.Fatal("ROLLBACK TO a released savepoint succeeded, want an error")
		}

		if err := tx.Commit(); err != nil {
			t.Fatalf("COMMIT: %v", err)
		}

		// Exactly the surviving rows -- id 1 and id 4 -- reach every replica.
		for label, db := range map[string]*sql.DB{
			"leader": leaderDB, "follower": followerDB, "other-follower": otherFollowerDB,
		} {
			ids := selectIDs(t, db, label, table)
			if len(ids) != 2 || ids[0] != 1 || ids[1] != 4 {
				t.Fatalf("%s: committed ids = %v, want [1 4]", label, ids)
			}
		}
	})

	// --- Aggregates must read through the write-set too --------------------
	//
	// Read-your-own-writes is not a property of point lookups; it is a
	// property of the transaction. A full scan inside the transaction does
	// see the buffered rows, so the write-set overlay is wired into the
	// table's own row source -- but SELECT COUNT(*) is answered from a
	// row-count fast path that never consults it, and returns the STORE's
	// count. An application that counts before inserting inside a
	// transaction therefore reads a number that contradicts the rows it can
	// enumerate one statement later.
	t.Run("AggregateInsideTransactionSeesBufferedRows", func(t *testing.T) {
		const table = "tx_aggregate"
		mustExec(t, leaderDB, fmt.Sprintf(
			"CREATE TABLE %s (id INT PRIMARY KEY, balance INT)", table))

		tx, err := leaderDB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("BEGIN: %v", err)
		}
		defer tx.Rollback()

		mustTxExec(t, tx, fmt.Sprintf("INSERT INTO %s (id, balance) VALUES (1, 1)", table))
		mustTxExec(t, tx, fmt.Sprintf("INSERT INTO %s (id, balance) VALUES (2, 2)", table))

		enumerated := txIDs(t, tx, table)
		var counted int
		if err := tx.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&counted); err != nil {
			t.Fatalf("in-transaction COUNT(*): %v", err)
		}
		if counted != len(enumerated) {
			t.Fatalf("in-transaction SELECT COUNT(*) = %d but the same transaction enumerates %v (%d rows): the count fast path does not read through the transaction's write-set",
				counted, enumerated, len(enumerated))
		}
		if counted != 2 {
			t.Fatalf("in-transaction SELECT COUNT(*) = %d, want 2", counted)
		}
	})

	// --- Two sessions on two replicas racing one row ----------------------
	t.Run("ConcurrentWritersConflictAsLockDeadlock", func(t *testing.T) {
		const table = "tx_conflict"
		mustExec(t, leaderDB, fmt.Sprintf(
			"CREATE TABLE %s (id INT PRIMARY KEY, balance INT)", table))
		mustExec(t, leaderDB, fmt.Sprintf("INSERT INTO %s (id, balance) VALUES (1, 0)", table))

		// Both sessions read the row (the UPDATE's own read binds each
		// write-set to a snapshot revision) before either commits. The
		// interleaving is driven explicitly rather than by goroutine timing,
		// so the outcome is a property of the isolation model and not of the
		// scheduler.
		winner, err := leaderDB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("BEGIN winner (leader): %v", err)
		}
		defer winner.Rollback()
		loser, err := followerDB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("BEGIN loser (follower): %v", err)
		}
		defer loser.Rollback()

		mustTxExec(t, winner, fmt.Sprintf("UPDATE %s SET balance = 100 WHERE id = 1", table))
		mustTxExec(t, loser, fmt.Sprintf("UPDATE %s SET balance = 200 WHERE id = 1", table))

		if err := winner.Commit(); err != nil {
			t.Fatalf("first committer (leader session) must win, but its COMMIT failed: %v", err)
		}

		loserErr := loser.Commit()
		assertLockDeadlock(t, "second committer (follower session)", loserErr)
		t.Logf("losing committer correctly rejected: %v", loserErr)

		// The loser's write must be nowhere -- on any replica.
		for label, db := range map[string]*sql.DB{
			"leader": leaderDB, "follower": followerDB, "other-follower": otherFollowerDB,
		} {
			if got := scanBalance(t, db, label, table, 1); got != 100 {
				t.Fatalf("%s: balance = %d, want 100 (the rejected transaction's write is present)", label, got)
			}
		}
	})

	// --- The same race, run concurrently, spread over all three replicas ---
	//
	// The deterministic phase above pins the semantics; this one pins that
	// they hold under real contention: N sessions, one per replica, all
	// racing the same row with no coordination. Exactly one must commit and
	// every other must be told 1213 -- never a silent lost update, and never
	// two winners.
	//
	// KNOWN FAILING -- this exposes a live correctness defect, and is left
	// failing deliberately rather than weakened. When conflicting
	// compare-and-swap commits are proposed closely enough together to land
	// in the SAME Raft apply batch, every one of them reports success and
	// all but the last-applied write is silently lost. The cause is below
	// the SQL layer entirely: pkg/store/mvcc.go's MVCCStateMachine.Update
	// applies an apply batch's entries into one uncommitted storage.Batch,
	// while pkg/engine/mvcc_engine.go's handleTransaction evaluates its
	// compares with mvcc.Store.Get, which reads only COMMITTED storage. Two
	// transactions in one apply batch therefore both evaluate their
	// preconditions against the pre-batch state and both pass. The same
	// defect is reproducible with no SQL involved, straight against POST
	// /tx: N concurrent transactions carrying an identical ModRevision
	// precondition on one key can all return succeeded=true.
	t.Run("ConcurrentRaceHasExactlyOneWinner", func(t *testing.T) {
		const table = "tx_race"
		mustExec(t, leaderDB, fmt.Sprintf(
			"CREATE TABLE %s (id INT PRIMARY KEY, balance INT)", table))
		mustExec(t, leaderDB, fmt.Sprintf("INSERT INTO %s (id, balance) VALUES (1, 0)", table))

		// Several independent sessions per replica, not one. A single
		// session per replica leaves the outcome at the mercy of how the
		// three commits happen to be spaced: when they land in separate Raft
		// apply batches the conflict IS detected and the phase passes, so a
		// 3-way race is an intermittent test of a defect that is not
		// intermittent. Widening it to a dozen sessions makes the assertion
		// strictly stronger (exactly one winner out of twelve, not out of
		// three) and, incidentally, deterministic.
		const sessionsPerReplica = 4
		type racer struct {
			label   string
			db      *sql.DB
			balance int
		}
		var racers []racer
		for _, r := range []struct {
			name string
			db   *sql.DB
		}{
			{"leader", leaderDB}, {"follower", followerDB}, {"other-follower", otherFollowerDB},
		} {
			for s := range sessionsPerReplica {
				racers = append(racers, racer{
					label:   fmt.Sprintf("%s#%d", r.name, s),
					db:      r.db,
					balance: 100 + len(racers),
				})
			}
		}

		// Every racer opens its transaction and performs its UPDATE first,
		// so all of them are bound to the same pre-race snapshot; only then
		// are the commits released together.
		txs := make([]*sql.Tx, len(racers))
		for i, r := range racers {
			tx, err := r.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("%s: BEGIN: %v", r.label, err)
			}
			defer tx.Rollback()
			mustTxExec(t, tx, fmt.Sprintf("UPDATE %s SET balance = %d WHERE id = 1", table, r.balance))
			txs[i] = tx
		}

		var (
			start   = make(chan struct{})
			wg      sync.WaitGroup
			mu      sync.Mutex
			winners []string
			errs    = make(map[string]error, len(racers))
		)
		wg.Add(len(racers))
		for i, r := range racers {
			go func(i int, label string) {
				defer wg.Done()
				<-start
				err := txs[i].Commit()
				mu.Lock()
				defer mu.Unlock()
				errs[label] = err
				if err == nil {
					winners = append(winners, label)
				}
			}(i, r.label)
		}
		close(start)
		wg.Wait()

		if len(winners) != 1 {
			t.Fatalf("commit outcomes = %v, want exactly one winner (winners: %v)", errs, winners)
		}
		for label, err := range errs {
			if err == nil {
				continue
			}
			if !isLockDeadlock(err) {
				t.Fatalf("%s lost the race but was not told %d (ER_LOCK_DEADLOCK): %v",
					label, erLockDeadlock, err)
			}
		}
		t.Logf("winner: %s; losers rejected with 1213: %v", winners[0], errs)

		// Whatever the winner wrote is what every replica must hold, and it
		// must be one of the racers' values -- never a blend, never the
		// pre-race 0.
		var want int
		for _, r := range racers {
			if r.label == winners[0] {
				want = r.balance
			}
		}
		for label, db := range map[string]*sql.DB{
			"leader": leaderDB, "follower": followerDB, "other-follower": otherFollowerDB,
		} {
			if got := scanBalance(t, db, label, table, 1); got != want {
				t.Fatalf("%s: balance = %d, want %d (the value the single winning transaction committed)", label, got, want)
			}
		}
	})

	// --- DDL inside an open transaction implicitly commits it -------------
	//
	// MySQL's rule: DDL is not transactional and implicitly commits whatever
	// is pending, so a later ROLLBACK cannot undo the work that preceded it.
	// pkg/sql/provider's flushPendingWriteSet is the implementation.
	//
	// KNOWN FAILING, AND NOT ON ITS OWN ACCOUNT. Run in isolation this phase
	// passes; it fails only when ConcurrentRaceHasExactlyOneWinner has run
	// first, and it fails on the catalog half (a follower still reporting the
	// pre-ALTER column list) while the data half -- the implicitly committed
	// INSERT -- is correct everywhere. That is the batched compare-and-swap
	// defect surfacing as REPLICA DIVERGENCE: because each replica evaluates
	// a transaction's compares against its own store at apply time, and
	// apply-batch boundaries differ from replica to replica, two replicas can
	// reach OPPOSITE verdicts on the same log entry and permanently disagree
	// about the resulting state. Reproducible with no SQL involved: after a
	// burst of concurrent POST /tx compare-and-swaps on one key, a
	// LINEARIZABLE GET of that key returns different values depending on
	// which replica serves it, and stays different indefinitely.
	t.Run("DDLImplicitlyCommits", func(t *testing.T) {
		const table = "tx_implicit"
		mustExec(t, leaderDB, fmt.Sprintf(
			"CREATE TABLE %s (id INT PRIMARY KEY, balance INT)", table))

		tx, err := leaderDB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("BEGIN: %v", err)
		}

		mustTxExec(t, tx, fmt.Sprintf("INSERT INTO %s (id, balance) VALUES (1, 42)", table))

		// This DDL must implicitly commit the INSERT above.
		mustTxExec(t, tx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN note VARCHAR(32)", table))

		// The explicit ROLLBACK that follows must NOT undo it.
		if err := tx.Rollback(); err != nil {
			t.Fatalf("ROLLBACK after DDL: %v", err)
		}

		for label, db := range map[string]*sql.DB{
			"leader": leaderDB, "follower": followerDB, "other-follower": otherFollowerDB,
		} {
			if got := scanBalance(t, db, label, table, 1); got != 42 {
				t.Fatalf("%s: balance = %d, want 42 -- the ROLLBACK undid work that DDL had already implicitly committed", label, got)
			}
			cols := sqlColumnNames(t, db, label, table)
			if len(cols) != 3 {
				t.Fatalf("%s: columns after the implicitly-committing ALTER = %v, want 3", label, cols)
			}
		}
	})
}

// mustExec runs a statement against db or fails the test.
func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// mustTxExec runs a statement inside tx or fails the test.
func mustTxExec(t *testing.T, tx *sql.Tx, query string, args ...any) {
	t.Helper()
	if _, err := tx.Exec(query, args...); err != nil {
		t.Fatalf("exec %q inside transaction: %v", query, err)
	}
}

// txIDs returns every id visible from inside tx, ascending -- that is,
// the store's rows overlaid with the transaction's own buffered write-set.
func txIDs(t *testing.T, tx *sql.Tx, table string) []int {
	t.Helper()
	rows, err := tx.Query("SELECT id FROM " + table + " ORDER BY id ASC")
	if err != nil {
		t.Fatalf("in-transaction SELECT id FROM %s: %v", table, err)
	}
	defer rows.Close()
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("in-transaction scan id: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("in-transaction iterate ids: %v", err)
	}
	return ids
}

// selectIDs returns every id in table, ascending.
func selectIDs(t *testing.T, db *sql.DB, label, table string) []int {
	t.Helper()
	rows, err := db.Query("SELECT id FROM " + table + " ORDER BY id ASC")
	if err != nil {
		t.Fatalf("%s: SELECT id FROM %s: %v", label, table, err)
	}
	defer rows.Close()
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("%s: scan id: %v", label, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%s: iterate ids: %v", label, err)
	}
	return ids
}
