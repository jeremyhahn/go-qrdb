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

// sql_row_lock_test.go proves SQL row locks (docs/row-locking) across a real
// 3-node cluster: two sessions on DIFFERENT replicas serialise on
// SELECT ... FOR UPDATE, a deadlock between them makes exactly one a victim
// with MySQL 1213, and a lock held across a leader failover is still held
// afterwards -- the lock table is replicated state, not a node-local map.
package integration_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"testing"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
)

// erLockNowait is MySQL's ER_LOCK_NOWAIT.
const erLockNowait = 3572

// rowLockConn pins one connection of db: one session, which ends with the
// test that opened it.
func rowLockConn(t *testing.T, db *sql.DB) *sql.Conn {
	t.Helper()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("db.Conn: %v", err)
	}
	t.Cleanup(func() {
		// Discard the connection rather than return it to db's pool: its
		// session may still be inside a transaction -- one a failed
		// assertion abandoned, or one aborted as a deadlock victim -- and
		// the next subtest's session must not inherit it.
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		_ = conn.Close()
	})
	return conn
}

// connExec runs a statement on a pinned session.
func connExec(t *testing.T, conn *sql.Conn, query string) {
	t.Helper()
	if _, err := conn.ExecContext(context.Background(), query); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

// connBalance reads one row's balance on a pinned session.
func connBalance(conn *sql.Conn, query string) (int, error) {
	var balance int
	err := conn.QueryRowContext(context.Background(), query).Scan(&balance)
	return balance, err
}

// asyncBalance runs connBalance on its own goroutine.
func asyncBalance(conn *sql.Conn, query string) (<-chan int, <-chan error) {
	val, done := make(chan int, 1), make(chan error, 1)
	go func() {
		b, err := connBalance(conn, query)
		val <- b
		done <- err
	}()
	return val, done
}

// mysqlErrNumber returns the MySQL error number of err, or 0.
func mysqlErrNumber(err error) uint16 {
	var myErr *gomysql.MySQLError
	if errors.As(err, &myErr) {
		return myErr.Number
	}
	return 0
}

func TestSQLCluster_RowLocks(t *testing.T) {
	c := startThreeNodeClusterWithSQL(t)
	leader := c.leaderIndex(t, 60*time.Second)
	followers := c.followerIndexes(leader)
	if len(followers) != 2 {
		t.Fatalf("expected 2 followers in a 3-node cluster, got %d", len(followers))
	}
	adminDB := c.mysql(t, leader)
	aDB := c.mysql(t, followers[0])
	bDB := c.mysql(t, followers[1])

	setup := func(t *testing.T, table string) {
		t.Helper()
		mustExec(t, adminDB, fmt.Sprintf("CREATE TABLE %s (id INT PRIMARY KEY, balance INT)", table))
		mustExec(t, adminDB, fmt.Sprintf("INSERT INTO %s (id, balance) VALUES (1, 100), (2, 200)", table))
	}

	// Two sessions on different replicas: the second FOR UPDATE waits for
	// the first transaction and then reads what it committed.
	t.Run("ForUpdateSerialisesAcrossReplicas", func(t *testing.T) {
		const table = "lock_serial"
		setup(t, table)
		s1, s2 := rowLockConn(t, aDB), rowLockConn(t, bDB)

		connExec(t, s1, "BEGIN")
		if _, err := connBalance(s1, fmt.Sprintf("SELECT balance FROM %s WHERE id = 1 FOR UPDATE", table)); err != nil {
			t.Fatalf("s1 FOR UPDATE: %v", err)
		}
		connExec(t, s2, "BEGIN")
		val, done := asyncBalance(s2, fmt.Sprintf("SELECT balance FROM %s WHERE id = 1 FOR UPDATE", table))
		select {
		case err := <-done:
			t.Fatalf("s2's FOR UPDATE returned while s1 held the row: %v", err)
		case <-time.After(500 * time.Millisecond):
		}

		connExec(t, s1, fmt.Sprintf("UPDATE %s SET balance = balance + 1 WHERE id = 1", table))
		connExec(t, s1, "COMMIT")
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("s2 FOR UPDATE: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("s2 never acquired the row after s1 committed")
		}
		if got := <-val; got != 101 {
			t.Fatalf("s2 read balance %d after waiting, want 101 (a locking read is a current read)", got)
		}
		connExec(t, s2, fmt.Sprintf("UPDATE %s SET balance = balance + 1 WHERE id = 1", table))
		connExec(t, s2, "COMMIT")
		if got := scanBalance(t, adminDB, "final", table, 1); got != 102 {
			t.Fatalf("final balance %d, want 102: an increment was lost", got)
		}
	})

	// A deadlock across replicas makes one transaction the victim (1213) and
	// lets the other finish.
	t.Run("DeadlockVictim", func(t *testing.T) {
		const table = "lock_deadlock"
		setup(t, table)
		s1, s2 := rowLockConn(t, aDB), rowLockConn(t, bDB)

		connExec(t, s1, "BEGIN")
		if _, err := connBalance(s1, fmt.Sprintf("SELECT balance FROM %s WHERE id = 1 FOR UPDATE", table)); err != nil {
			t.Fatalf("s1 locks row 1: %v", err)
		}
		connExec(t, s2, "BEGIN")
		if _, err := connBalance(s2, fmt.Sprintf("SELECT balance FROM %s WHERE id = 2 FOR UPDATE", table)); err != nil {
			t.Fatalf("s2 locks row 2: %v", err)
		}
		_, s1Done := asyncBalance(s1, fmt.Sprintf("SELECT balance FROM %s WHERE id = 2 FOR UPDATE", table))
		select {
		case err := <-s1Done:
			t.Fatalf("s1 returned while s2 held row 2: %v", err)
		case <-time.After(500 * time.Millisecond):
		}

		_, err := connBalance(s2, fmt.Sprintf("SELECT balance FROM %s WHERE id = 1 FOR UPDATE", table))
		if mysqlErrNumber(err) != erLockDeadlock {
			t.Fatalf("the cycle-closing request got %v, want MySQL %d", err, erLockDeadlock)
		}
		connExec(t, s2, "ROLLBACK")

		select {
		case err := <-s1Done:
			if err != nil {
				t.Fatalf("the survivor's FOR UPDATE: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("the survivor never acquired row 2 after the victim rolled back")
		}
		connExec(t, s1, "COMMIT")
	})

	// A lock held across a leader failover is still held afterwards: the
	// lock records and the lease they are bound to are replicated state.
	t.Run("LeaderFailoverPreservesLocks", func(t *testing.T) {
		const table = "lock_failover"
		setup(t, table)
		holder, waiter := rowLockConn(t, aDB), rowLockConn(t, bDB)

		connExec(t, holder, "BEGIN")
		if _, err := connBalance(holder, fmt.Sprintf("SELECT balance FROM %s WHERE id = 1 FOR UPDATE", table)); err != nil {
			t.Fatalf("holder FOR UPDATE: %v", err)
		}

		c.kill(t, leader)
		newLeader := c.leaderIndex(t, 90*time.Second)
		t.Logf("leader replica %d killed; replica %d now leads", leader+1, newLeader+1)

		// After the election the row is still locked: NOWAIT is refused.
		deadline := time.Now().Add(60 * time.Second)
		for {
			_, err := connBalance(waiter, fmt.Sprintf("SELECT balance FROM %s WHERE id = 1 FOR UPDATE NOWAIT", table))
			if mysqlErrNumber(err) == erLockNowait {
				break
			}
			if err == nil {
				t.Fatal("the row was lockable after the failover: the leader change dropped the lock")
			}
			if time.Now().After(deadline) {
				t.Fatalf("waiting for the cluster to serve again after failover: %v", err)
			}
			time.Sleep(250 * time.Millisecond)
		}

		connExec(t, holder, fmt.Sprintf("UPDATE %s SET balance = 7 WHERE id = 1", table))
		connExec(t, holder, "COMMIT")
		got, err := connBalance(waiter, fmt.Sprintf("SELECT balance FROM %s WHERE id = 1 FOR UPDATE NOWAIT", table))
		if err != nil {
			t.Fatalf("the row was not released by the holder's commit: %v", err)
		}
		if got != 7 {
			t.Fatalf("waiter read %d, want the holder's committed 7", got)
		}
	})
}
