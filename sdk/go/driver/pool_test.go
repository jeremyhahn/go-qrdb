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

package driver

import (
	"context"
	"database/sql"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Pool-release regression tests
//
// database/sql hands a driver connection out of a bounded pool and takes it
// back only when the driver.Tx, driver.Stmt, or driver.Rows built on it is
// closed or reports its error the way the package expects. A driver that
// forgets to release on ONE error path does not fail that operation -- it
// removes a connection from the pool permanently. Under a pool limit the
// symptom is that every later caller blocks in sql.(*DB).conn forever, with
// no goroutine anywhere in this package's code and nothing in any log: a
// process-wide hang whose only visible evidence is a test timeout minutes
// later, in whatever suite happened to run next.
//
// These tests pin the property directly, under a pool of exactly one
// connection so that a single leak is immediately fatal rather than merely
// probable, and under bounded timeouts so that a regression fails loudly in
// seconds instead of hanging a CI job. Every workload below is a sequence a
// real client library issues -- notably gorm.io/gorm's PreparedStmtDB, which
// prepares on the pool and then rebinds the statement into a transaction,
// rolls back after a failed statement, and calls Commit and Rollback on an
// already-finished transaction.
// ---------------------------------------------------------------------------

const (
	// poolWorkers is how many clients contend for the single connection.
	poolWorkers = 24

	// poolRounds is how many times each worker runs the whole workload table.
	poolRounds = 6

	// poolOpBudget bounds one database/sql call. A driver that has lost the
	// connection makes every acquisition block, so an expired budget on a
	// workload this small means starvation, not slowness.
	poolOpBudget = 10 * time.Second

	// poolRunBudget bounds the whole exerciser, so a leak surfaces as an
	// error from the operation that starved rather than as a hang.
	poolRunBudget = 60 * time.Second

	// poolSettleBudget bounds how long the pool may take to quiesce after the
	// last worker returns; database/sql releases a context-cancelled
	// transaction's connection asynchronously.
	poolSettleBudget = 10 * time.Second
)

// SQL the pool workloads run. The "missing" statements name a table that was
// never created, which is the shape of failure that matters here: it fails at
// prepare time, on the engine, after the connection has already been handed
// out.
const (
	poolCreateSQL       = "CREATE TABLE pool_rows (id BIGINT UNSIGNED PRIMARY KEY, name VARCHAR(64))"
	poolInsertSQL       = "INSERT INTO pool_rows (id, name) VALUES (?, ?)"
	poolSelectSQL       = "SELECT id, name FROM pool_rows WHERE id = ?"
	poolMissingTableSQL = "INSERT INTO pool_rows_absent (id, name) VALUES (?, ?)"
	poolBadSyntaxSQL    = "SELECT FROM WHERE"
)

// poolRowID hands out a distinct primary key per insert so that the workloads
// exercise release paths rather than each other's duplicate-key errors.
var poolRowID atomic.Int64

func nextPoolRowID() int64 { return poolRowID.Add(1) }

// poolWorkload is one client interaction run against a pool-limited *sql.DB.
//
// run's error is inspected only for starvation (see starvedOfConnection): a
// workload that deliberately issues a failing statement returns that failure,
// because what is under test is the connection's return to the pool, not the
// statement's outcome.
type poolWorkload struct {
	name string
	run  func(ctx context.Context, db *sql.DB) error
}

// poolWorkloads enumerates the sequences a connection must survive. Each one
// either closes everything it opened or ends the transaction it began; if the
// driver disagrees, the pool runs dry and every later workload starves.
var poolWorkloads = []poolWorkload{
	{"commit", func(ctx context.Context, db *sql.DB) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, poolInsertSQL, nextPoolRowID(), "commit"); err != nil {
			return errors.Join(err, tx.Rollback())
		}
		return tx.Commit()
	}},

	{"rollback", func(ctx context.Context, db *sql.DB) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, poolInsertSQL, nextPoolRowID(), "rollback"); err != nil {
			return errors.Join(err, tx.Rollback())
		}
		return tx.Rollback()
	}},

	{"failed statement then rollback", func(ctx context.Context, db *sql.DB) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		_, execErr := tx.ExecContext(ctx, poolMissingTableSQL, nextPoolRowID(), "x")
		return errors.Join(execErr, tx.Rollback())
	}},

	{"failed statement then commit", func(ctx context.Context, db *sql.DB) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		_, execErr := tx.ExecContext(ctx, poolMissingTableSQL, nextPoolRowID(), "x")
		return errors.Join(execErr, tx.Commit())
	}},

	{"rollback twice", func(ctx context.Context, db *sql.DB) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		return errors.Join(tx.Rollback(), tx.Rollback())
	}},

	{"commit after rollback", func(ctx context.Context, db *sql.DB) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		return errors.Join(tx.Rollback(), tx.Commit())
	}},

	{"failed prepare on the pool", func(ctx context.Context, db *sql.DB) error {
		st, err := db.PrepareContext(ctx, poolMissingTableSQL)
		if st != nil {
			return errors.Join(err, st.Close())
		}
		return err
	}},

	{"failed prepare in a transaction", func(ctx context.Context, db *sql.DB) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		st, prepErr := tx.PrepareContext(ctx, poolMissingTableSQL)
		if st != nil {
			prepErr = errors.Join(prepErr, st.Close())
		}
		return errors.Join(prepErr, tx.Rollback())
	}},

	{"unparseable prepare", func(ctx context.Context, db *sql.DB) error {
		st, err := db.PrepareContext(ctx, poolBadSyntaxSQL)
		if st != nil {
			return errors.Join(err, st.Close())
		}
		return err
	}},

	{"prepared exec", func(ctx context.Context, db *sql.DB) error {
		st, err := db.PrepareContext(ctx, poolInsertSQL)
		if err != nil {
			return err
		}
		_, execErr := st.ExecContext(ctx, nextPoolRowID(), "prepared")
		return errors.Join(execErr, st.Close())
	}},

	{"prepared query", func(ctx context.Context, db *sql.DB) error {
		st, err := db.PrepareContext(ctx, poolSelectSQL)
		if err != nil {
			return err
		}
		rows, queryErr := st.QueryContext(ctx, nextPoolRowID())
		if queryErr == nil {
			queryErr = drainResultSet(rows)
		}
		return errors.Join(queryErr, st.Close())
	}},

	// The gorm PreparedStmtDB shape: prepare on the pool, then rebind the
	// statement into a transaction that owns a different connection.
	{"pooled statement rebound into a transaction", func(ctx context.Context, db *sql.DB) error {
		st, err := db.PrepareContext(ctx, poolInsertSQL)
		if err != nil {
			return err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return errors.Join(err, st.Close())
		}
		bound := tx.StmtContext(ctx, st)
		_, execErr := bound.ExecContext(ctx, nextPoolRowID(), "rebound")
		return errors.Join(execErr, bound.Close(), tx.Commit(), st.Close())
	}},

	{"query error", func(ctx context.Context, db *sql.DB) error {
		rows, err := db.QueryContext(ctx, "SELECT id FROM pool_rows_absent")
		if rows != nil {
			return errors.Join(err, rows.Close())
		}
		return err
	}},

	{"query drained", func(ctx context.Context, db *sql.DB) error {
		rows, err := db.QueryContext(ctx, poolSelectSQL, nextPoolRowID())
		if err != nil {
			return err
		}
		return drainResultSet(rows)
	}},

	{"query closed undrained", func(ctx context.Context, db *sql.DB) error {
		rows, err := db.QueryContext(ctx, "SELECT id, name FROM pool_rows")
		if err != nil {
			return err
		}
		return rows.Close()
	}},

	{"query row with no match", func(ctx context.Context, db *sql.DB) error {
		var (
			id   int64
			name string
		)
		err := db.QueryRowContext(ctx, poolSelectSQL, int64(-1)).Scan(&id, &name)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}},

	{"autocommit exec", func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, poolInsertSQL, nextPoolRowID(), "autocommit")
		return err
	}},

	{"exec on a closed statement", func(ctx context.Context, db *sql.DB) error {
		st, err := db.PrepareContext(ctx, poolInsertSQL)
		if err != nil {
			return err
		}
		if err := st.Close(); err != nil {
			return err
		}
		_, err = st.ExecContext(ctx, nextPoolRowID(), "closed")
		return err
	}},

	{"ping", func(ctx context.Context, db *sql.DB) error {
		return db.PingContext(ctx)
	}},
}

// drainResultSet consumes and closes a result set, reporting the first error.
func drainResultSet(rows *sql.Rows) error {
	for rows.Next() {
		var (
			id   int64
			name sql.NullString
		)
		if err := rows.Scan(&id, &name); err != nil {
			return errors.Join(err, rows.Close())
		}
	}
	return errors.Join(rows.Err(), rows.Close())
}

// starvedOfConnection reports whether err is the signature of a connection
// that never came back: the operation's own budget expired while it waited in
// sql.(*DB).conn. Every other error a workload can produce is a statement
// outcome, which these tests deliberately do not judge.
func starvedOfConnection(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// newPoolTestDB opens a relational embedded database limited to a single
// connection, with the workloads' table created.
func newPoolTestDB(t *testing.T, name string) *sql.DB {
	t.Helper()
	db, _ := openRelationalTestDB(t, name)
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(poolCreateSQL); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	return db
}

// TestEmbeddedPool_ConcurrentClientsNeverLoseTheConnection runs every workload
// in poolWorkloads from many goroutines against a pool of one connection. If
// any workload fails to return the connection, the workers that follow it
// starve and the test fails with the offending workload's name.
func TestEmbeddedPool_ConcurrentClientsNeverLoseTheConnection(t *testing.T) {
	db := newPoolTestDB(t, "poolexercise")

	ctx, cancel := context.WithTimeout(context.Background(), poolRunBudget)
	defer cancel()

	var wg sync.WaitGroup
	for range poolWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range poolRounds {
				for _, workload := range poolWorkloads {
					opCtx, opCancel := context.WithTimeout(ctx, poolOpBudget)
					err := workload.run(opCtx, db)
					opCancel()
					if starvedOfConnection(err) {
						t.Errorf("workload %q starved: the connection it used was never returned to the pool: %v (stats=%+v)",
							workload.name, err, db.Stats())
						return
					}
				}
			}
		}()
	}
	awaitWorkers(t, &wg, db)

	if stats := db.Stats(); stats.WaitCount == 0 {
		t.Errorf("pool WaitCount = 0: the workers never contended for the single connection, so this run proves nothing (stats=%+v)", stats)
	}
	assertPoolDrained(t, db)
}

// TestEmbeddedPool_FailedPrepareInTransactionReleasesConnection pins the
// narrowest sequence that can strand a pool: a prepare that fails on the
// engine AFTER the transaction has taken the pool's only connection. Nothing
// the driver returns from here is allowed to cost the pool that connection.
func TestEmbeddedPool_FailedPrepareInTransactionReleasesConnection(t *testing.T) {
	db := newPoolTestDB(t, "poolprepare")

	ctx, cancel := context.WithTimeout(context.Background(), poolOpBudget)
	defer cancel()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if st, err := tx.PrepareContext(ctx, poolMissingTableSQL); err == nil {
		_ = st.Close()
		t.Fatal("PrepareContext against a missing table must fail")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	assertPoolDrained(t, db)
	assertPoolUsable(t, db)
}

// TestEmbeddedPool_CancelledTransactionReleasesConnection covers the release
// path database/sql drives itself: a transaction abandoned by cancelling its
// context is rolled back and its connection reset for the next caller. It is
// the path embeddedConn.ResetSession's open-transaction branch exists for.
func TestEmbeddedPool_CancelledTransactionReleasesConnection(t *testing.T) {
	db := newPoolTestDB(t, "poolcancel")

	txCtx, txCancel := context.WithCancel(context.Background())
	tx, err := db.BeginTx(txCtx, nil)
	if err != nil {
		txCancel()
		t.Fatalf("BeginTx: %v", err)
	}
	if _, err := tx.ExecContext(txCtx, poolInsertSQL, nextPoolRowID(), "cancelled"); err != nil {
		txCancel()
		t.Fatalf("ExecContext: %v", err)
	}
	txCancel()

	assertPoolDrained(t, db)
	assertPoolUsable(t, db)

	// The abandoned transaction must not have committed.
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM pool_rows WHERE name = 'cancelled'").Scan(&n); err != nil {
		t.Fatalf("QueryRow: %v", err)
	}
	if n != 0 {
		t.Errorf("rows from the cancelled transaction = %d, want 0", n)
	}
}

// awaitWorkers waits for the exerciser to finish, failing with a full
// goroutine dump rather than hanging if a driver blocks past its context.
func awaitWorkers(t *testing.T, wg *sync.WaitGroup, db *sql.DB) {
	t.Helper()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(poolRunBudget + poolSettleBudget):
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		t.Fatalf("pool exerciser did not finish within %s and did not honour its context: the driver is holding the pool's only connection (stats=%+v)\n%s",
			poolRunBudget+poolSettleBudget, db.Stats(), buf[:n])
	}
}

// assertPoolDrained waits for the pool to quiesce with no connection checked
// out, polling because database/sql returns a context-cancelled
// transaction's connection on its own goroutine.
func assertPoolDrained(t *testing.T, db *sql.DB) {
	t.Helper()

	deadline := time.Now().Add(poolSettleBudget)
	for {
		stats := db.Stats()
		if stats.InUse == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pool still has %d connection(s) checked out %s after the last caller finished: they were never returned (stats=%+v)",
				stats.InUse, poolSettleBudget, stats)
		}
		time.Sleep(time.Millisecond)
	}
}

// assertPoolUsable proves the pool still serves a new caller, which a pool
// whose only connection was stranded cannot do.
func assertPoolUsable(t *testing.T, db *sql.DB) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), poolOpBudget)
	defer cancel()

	if _, err := db.ExecContext(ctx, poolInsertSQL, nextPoolRowID(), "after"); err != nil {
		t.Fatalf("the pool no longer serves new callers: %v (stats=%+v)", err, db.Stats())
	}
}
