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

// sql_autoincrement_test.go attacks the one property qrdb's Raft-committed
// sequence exists to guarantee: an allocated identifier is NEVER handed out
// twice, not even across a leader election.
//
// The contract is deliberately asymmetric, and the tests encode that:
//
//   - GAPS ARE PERMITTED. MySQL behaves this way too under
//     innodb_autoinc_lock_mode=2, and qrdb block-allocates from the sequence,
//     so a crashed leader strands whatever remained of its block. A test that
//     demanded a dense 1..N sequence would be testing something qrdb never
//     promised, and something MySQL does not promise either.
//   - DUPLICATES ARE FATAL. Two rows sharing an identifier is not a
//     performance wart, it is data loss: the second row is written over the
//     first at the same primary key, and the application is told both
//     INSERTs succeeded.
//
// The failure is injected the ugly way -- SIGKILL to the shard leader while
// writers on every replica are mid-run -- because a graceful drain would
// hand the sequence a clean shutdown it will not get in production.
//
// KNOWN FAILING. Both tests in this file currently fail, and they are left
// failing deliberately: the sequence DOES hand out duplicates. The leader
// kill turns out not to be required to provoke it -- a single node with four
// concurrent writers and no failover at all is enough (44 confirmed INSERTs
// yielded 27 distinct ids; 17 rows were silently overwritten), while a single
// writer on one node is clean. The cause is not in the sequence code but
// under it: pkg/store/mvcc.go's MVCCStateMachine.Update applies all entries
// of one Raft apply batch into a single uncommitted storage.Batch, while
// pkg/engine/mvcc_engine.go's handleTransaction evaluates each transaction's
// compares with mvcc.Store.Get, which reads only COMMITTED storage. Sequence
// allocation is a compare-and-swap, so two allocations that land in the same
// apply batch both see the pre-batch counter and both return the same block.
// Most of the resulting collisions are caught downstream by the primary
// key's own Version==0 precondition (they surface as MySQL error 1062), but
// the ones that are not silently overwrite an existing row.
package integration_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"
)

// insertOutcome is one writer's record of one INSERT attempt.
type insertOutcome struct {
	node    int
	seq     int
	id      int64 // LastInsertId, when the driver reported one
	err     error
	confirm bool // the server confirmed the INSERT; the row MUST exist
}

// runConcurrentInserters drives writers goroutines per live replica, each
// issuing rows INSERTs through insertFn, and kills the shard leader partway
// through. It returns every attempt's outcome.
//
// killAfter is the number of successful inserts (across all writers) to wait
// for before killing the leader, so the failure lands in the middle of the
// run rather than before it starts or after it finishes.
func runConcurrentInserters(
	t *testing.T,
	c *sqlCluster,
	dbs map[int]*sql.DB,
	writers, rows int,
	killAfter int,
	insertFn func(ctx context.Context, db *sql.DB, node, seq int) (sql.Result, error),
) []insertOutcome {
	t.Helper()

	leader := c.leaderIndex(t, 60*time.Second)
	live := c.liveIndexes()

	var (
		mu        sync.Mutex
		outcomes  []insertOutcome
		succeeded int
		killOnce  sync.Once
		killed    = make(chan struct{})
	)

	maybeKill := func() {
		killOnce.Do(func() {
			t.Logf("killing shard leader (replica %d) mid-run", leader+1)
			c.kill(t, leader)
			close(killed)
		})
	}

	var wg sync.WaitGroup
	for _, node := range live {
		for w := range writers {
			wg.Add(1)
			go func(node, w int) {
				defer wg.Done()
				db := dbs[node]
				for r := range rows {
					seq := w*rows + r
					ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
					res, err := insertFn(ctx, db, node, seq)
					cancel()

					out := insertOutcome{node: node, seq: seq, err: err}
					if err == nil {
						out.confirm = true
						if res != nil {
							if id, idErr := res.LastInsertId(); idErr == nil {
								out.id = id
							}
						}
					}

					mu.Lock()
					outcomes = append(outcomes, out)
					if err == nil {
						succeeded++
					}
					ready := succeeded >= killAfter
					mu.Unlock()

					if ready {
						maybeKill()
					}
				}
			}(node, w)
		}
	}
	wg.Wait()

	// The kill must actually have happened; otherwise this test degenerates
	// into a plain concurrency test and proves nothing about failover.
	select {
	case <-killed:
	default:
		t.Fatal("the leader was never killed: fewer than killAfter inserts succeeded, so no failover was exercised")
	}
	return outcomes
}

// summarize reports how many attempts succeeded, and logs a sample of the
// failures so a run that degrades into "everything failed" is obvious.
func summarize(t *testing.T, outcomes []insertOutcome) (confirmed []insertOutcome) {
	t.Helper()
	var failures int
	var sample error
	for _, o := range outcomes {
		if o.confirm {
			confirmed = append(confirmed, o)
			continue
		}
		failures++
		if sample == nil {
			sample = o.err
		}
	}
	t.Logf("%d/%d inserts confirmed by the server; %d failed (sample: %v)",
		len(confirmed), len(outcomes), failures, sample)
	return confirmed
}

// TestSQLCluster_AutoIncrement_SurvivesLeaderKill drives concurrent INSERTs
// into an AUTO_INCREMENT table from every replica, kills the leader
// mid-flight, and then asserts on identifier uniqueness.
func TestSQLCluster_AutoIncrement_SurvivesLeaderKill(t *testing.T) {
	c := startThreeNodeClusterWithSQL(t)
	leader := c.leaderIndex(t, 60*time.Second)
	t.Logf("initial shard 1 leader is replica %d", leader+1)

	const table = "ai_events"
	dbs := make(map[int]*sql.DB, len(c.nodes))
	for _, i := range c.liveIndexes() {
		dbs[i] = c.mysql(t, i)
	}
	mustExec(t, dbs[leader], fmt.Sprintf(
		"CREATE TABLE %s (id INT AUTO_INCREMENT PRIMARY KEY, node INT, seq INT)", table))

	const (
		writersPerNode = 4
		rowsPerWriter  = 12
		killAfter      = 20
	)
	outcomes := runConcurrentInserters(t, c, dbs, writersPerNode, rowsPerWriter, killAfter,
		func(ctx context.Context, db *sql.DB, node, seq int) (sql.Result, error) {
			return db.ExecContext(ctx,
				fmt.Sprintf("INSERT INTO %s (node, seq) VALUES (?, ?)", table), node, seq)
		})
	confirmed := summarize(t, outcomes)
	if len(confirmed) < killAfter {
		t.Fatalf("only %d inserts were confirmed, fewer than the %d needed before the kill: the cluster did not survive the failover well enough for this test to mean anything",
			len(confirmed), killAfter)
	}

	// Every identifier the server handed back must be distinct. This is the
	// assertion the Raft-committed sequence exists to satisfy.
	byID := make(map[int64][]insertOutcome, len(confirmed))
	var withID int
	for _, o := range confirmed {
		if o.id == 0 {
			continue
		}
		withID++
		byID[o.id] = append(byID[o.id], o)
	}
	for id, dupes := range byID {
		if len(dupes) > 1 {
			t.Errorf("AUTO_INCREMENT id %d was handed to %d different INSERTs: %+v", id, len(dupes), dupes)
		}
	}
	t.Logf("%d confirmed inserts reported a LastInsertId; %d distinct values", withID, len(byID))

	// And the rows must actually all be there. A duplicate allocation does
	// not have to surface as an error: two rows sharing a primary key means
	// the second is written OVER the first, so the table simply ends up with
	// fewer rows than the server confirmed. Reading the surviving replicas is
	// what catches that.
	surviving := c.liveIndexes()
	newLeader := c.leaderIndex(t, 90*time.Second)
	t.Logf("post-kill leader is replica %d; surviving replicas %v", newLeader+1, surviving)

	for _, i := range surviving {
		db := dbs[i]
		label := fmt.Sprintf("replica%d", i+1)

		ids := map[int64]int{}
		pairs := map[[2]int]int{}
		rows, err := db.Query(fmt.Sprintf("SELECT id, node, seq FROM %s", table))
		if err != nil {
			t.Fatalf("%s: SELECT: %v", label, err)
		}
		for rows.Next() {
			var id int64
			var node, seq int
			if err := rows.Scan(&id, &node, &seq); err != nil {
				rows.Close()
				t.Fatalf("%s: scan: %v", label, err)
			}
			ids[id]++
			pairs[[2]int{node, seq}]++
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("%s: iterate: %v", label, err)
		}
		rows.Close()

		total := 0
		for id, n := range ids {
			total += n
			if n > 1 {
				t.Errorf("%s: id %d appears %d times in the table", label, id, n)
			}
		}
		t.Logf("%s: %d rows, %d distinct ids", label, total, len(ids))

		// Every insert the server confirmed must be present exactly once.
		// A missing one means its row was overwritten by another insert that
		// was allocated the same identifier.
		var missing int
		for _, o := range confirmed {
			switch pairs[[2]int{o.node, o.seq}] {
			case 1:
			case 0:
				missing++
				if missing <= 5 {
					t.Errorf("%s: the row for confirmed insert (node=%d seq=%d, reported id %d) is not in the table -- another insert was allocated the same identifier and overwrote it",
						label, o.node, o.seq, o.id)
				}
			default:
				t.Errorf("%s: the row for insert (node=%d seq=%d) appears %d times",
					label, o.node, o.seq, pairs[[2]int{o.node, o.seq}])
			}
		}
		if missing > 5 {
			t.Errorf("%s: %d confirmed inserts are missing from the table in total", label, missing)
		}
	}
}

// TestSQLCluster_HiddenRowID_SurvivesLeaderKill is the same failure injected
// against a table with NO PRIMARY KEY.
//
// Such a table's rows are keyed by a hidden row id allocated from its own
// Raft-committed sequence (see pkg/sql/provider/relational_rowid.go), which
// is the only thing distinguishing two rows that are otherwise byte-for-byte
// identical. Every insert here writes the SAME (a, b) values on purpose: if
// the row-id sequence ever repeats a value across an election, two of these
// indistinguishable rows collapse into one and the loss is completely silent
// -- there is no unique constraint to violate and no error to raise. The
// only way to see it is to count.
func TestSQLCluster_HiddenRowID_SurvivesLeaderKill(t *testing.T) {
	c := startThreeNodeClusterWithSQL(t)
	leader := c.leaderIndex(t, 60*time.Second)
	t.Logf("initial shard 1 leader is replica %d", leader+1)

	const table = "rowid_dupes"
	dbs := make(map[int]*sql.DB, len(c.nodes))
	for _, i := range c.liveIndexes() {
		dbs[i] = c.mysql(t, i)
	}
	mustExec(t, dbs[leader], fmt.Sprintf(
		"CREATE TABLE %s (a INT, b VARCHAR(32))", table))

	const (
		writersPerNode = 4
		rowsPerWriter  = 12
		killAfter      = 20
	)
	outcomes := runConcurrentInserters(t, c, dbs, writersPerNode, rowsPerWriter, killAfter,
		func(ctx context.Context, db *sql.DB, _, _ int) (sql.Result, error) {
			// Byte-identical on every insert: nothing but the hidden row id
			// can tell these rows apart.
			return db.ExecContext(ctx,
				fmt.Sprintf("INSERT INTO %s (a, b) VALUES (?, ?)", table), 1, "identical")
		})
	confirmed := summarize(t, outcomes)
	if len(confirmed) < killAfter {
		t.Fatalf("only %d inserts were confirmed, fewer than the %d needed before the kill", len(confirmed), killAfter)
	}

	newLeader := c.leaderIndex(t, 90*time.Second)
	t.Logf("post-kill leader is replica %d", newLeader+1)

	for _, i := range c.liveIndexes() {
		db := dbs[i]
		label := fmt.Sprintf("replica%d", i+1)
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatalf("%s: COUNT(*): %v", label, err)
		}
		t.Logf("%s: %d rows survive for %d confirmed inserts", label, count, len(confirmed))

		// The bound is one-sided, and deliberately so. Every CONFIRMED
		// insert must have produced its own row, so a count below the
		// confirmed total means two byte-identical rows landed on one key
		// -- the hidden row id repeated, which is the defect this test
		// exists to catch.
		//
		// A count ABOVE it is correct, not a defect: killing the leader
		// mid-run makes inserts fail with "invalid connection" after the
		// server already committed them, and a client with no idempotency
		// key cannot distinguish "never committed" from "committed, reply
		// lost". Asserting equality here would be asserting exactly-once
		// delivery over a lossy channel, which no database provides.
		if count < len(confirmed) {
			t.Errorf("%s: table holds %d rows but %d inserts were confirmed by the server -- indistinguishable rows collapsed, so the hidden row id repeated a value",
				label, count, len(confirmed))
		}
	}
}
