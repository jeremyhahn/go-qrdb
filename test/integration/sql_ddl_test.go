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

// sql_ddl_test.go proves the relational DDL surface is REPLICATED, not merely
// implemented. Every DDL statement qrdb serves mutates a catalog record that
// lives in the same Raft log as user data, so the property that matters at
// this tier is not "CREATE TABLE succeeds" (pkg/sql/provider's unit tests
// already pin that) but "CREATE TABLE succeeds HERE and is immediately true
// THERE" -- on a replica that never saw the statement.
//
// Each phase therefore ends with a catalog-coherence assertion across every
// live replica, driven over the real MySQL wire protocol (mTLS, no password
// path) rather than in-process.
package integration_test

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
)

// sqlShowTables returns the table names replica idx currently serves, over
// the MySQL wire protocol.
func sqlShowTables(t *testing.T, db *sql.DB, label string) []string {
	t.Helper()
	rows, err := db.Query("SHOW TABLES")
	if err != nil {
		t.Fatalf("%s: SHOW TABLES: %v", label, err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("%s: SHOW TABLES scan: %v", label, err)
		}
		names = append(names, strings.ToLower(name))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%s: SHOW TABLES iteration: %v", label, err)
	}
	sort.Strings(names)
	return names
}

// sqlColumnNames returns the column names of table on replica idx, in
// declaration order, over the MySQL wire protocol.
func sqlColumnNames(t *testing.T, db *sql.DB, label, table string) []string {
	t.Helper()
	rows, err := db.Query("SHOW COLUMNS FROM " + table)
	if err != nil {
		t.Fatalf("%s: SHOW COLUMNS FROM %s: %v", label, table, err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("%s: SHOW COLUMNS columns: %v", label, err)
	}

	var names []string
	for rows.Next() {
		cells := make([]any, len(cols))
		for i := range cells {
			cells[i] = new(sql.RawBytes)
		}
		if err := rows.Scan(cells...); err != nil {
			t.Fatalf("%s: SHOW COLUMNS scan: %v", label, err)
		}
		names = append(names, string(*cells[0].(*sql.RawBytes)))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%s: SHOW COLUMNS iteration: %v", label, err)
	}
	return names
}

// contains reports whether names includes want.
func contains(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// waitForTableVisible polls SHOW TABLES on db until table is present (want
// true) or absent (want false). Catalog reads are linearizable, so this
// normally succeeds on the first poll; the loop exists so a transient
// leader-transfer retry does not turn into a flake, and its expiry is a real
// failure.
func waitForTableVisible(t *testing.T, db *sql.DB, label, table string, want bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var got []string
	for time.Now().Before(deadline) {
		got = sqlShowTables(t, db, label)
		if contains(got, table) == want {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s: timed out after %v waiting for table %q present=%v; SHOW TABLES = %v",
		label, timeout, table, want, got)
}

// TestSQLCluster_DDL_ReplicatedCatalog drives the full DDL lifecycle through
// the LEADER and asserts, after every step, that the catalog and the data it
// describes are coherent on every replica -- including the ones that only
// ever saw the Raft entry.
//
// The phases are sequenced deliberately: the index is created on a table that
// is ALREADY NON-EMPTY, because that is the case with a silent failure mode.
// go-mysql-server calls IndexBuildingTable.BuildIndex to backfill the rows
// that predate the index; if that backfill is skipped the index exists,
// EXPLAIN happily plans an IndexedTableAccess over it, and the query returns
// ZERO rows with no error at all.
func TestSQLCluster_DDL_ReplicatedCatalog(t *testing.T) {
	c := startThreeNodeClusterWithSQL(t)
	leader := c.leaderIndex(t, 60*time.Second)
	followers := c.followerIndexes(leader)
	if len(followers) != 2 {
		t.Fatalf("expected 2 followers in a 3-node cluster, got %d", len(followers))
	}
	t.Logf("shard 1 leader is replica %d; followers %v", leader+1, followers)

	leaderDB := c.mysql(t, leader)
	dbs := make(map[int]*sql.DB, len(c.nodes))
	labels := make(map[int]string, len(c.nodes))
	for _, i := range c.liveIndexes() {
		if i == leader {
			dbs[i] = leaderDB
			labels[i] = fmt.Sprintf("replica%d(leader)", i+1)
			continue
		}
		dbs[i] = c.mysql(t, i)
		labels[i] = fmt.Sprintf("replica%d(follower)", i+1)
	}

	const table = "ddl_accounts"

	// --- Phase 1: CREATE TABLE + INSERT on the leader ---------------------
	//
	// The assertion that matters is the follower one: a replica that never
	// received the CREATE TABLE statement must serve the table, its schema,
	// and its rows purely from the replicated catalog.
	t.Run("CreateTableVisibleOnFollowers", func(t *testing.T) {
		if _, err := leaderDB.Exec(fmt.Sprintf(
			"CREATE TABLE %s (id INT PRIMARY KEY, owner VARCHAR(64), balance INT)", table)); err != nil {
			t.Fatalf("CREATE TABLE on leader: %v", err)
		}
		for i := 1; i <= 6; i++ {
			if _, err := leaderDB.Exec(fmt.Sprintf(
				"INSERT INTO %s (id, owner, balance) VALUES (?, ?, ?)", table),
				i, fmt.Sprintf("owner-%d", i), i*100); err != nil {
				t.Fatalf("INSERT id=%d on leader: %v", i, err)
			}
		}

		for _, i := range c.liveIndexes() {
			waitForTableVisible(t, dbs[i], labels[i], table, true, 30*time.Second)

			cols := sqlColumnNames(t, dbs[i], labels[i], table)
			want := []string{"id", "owner", "balance"}
			if len(cols) != len(want) {
				t.Fatalf("%s: SHOW COLUMNS FROM %s = %v, want %v", labels[i], table, cols, want)
			}
			for j := range want {
				if !strings.EqualFold(cols[j], want[j]) {
					t.Fatalf("%s: SHOW COLUMNS FROM %s = %v, want %v", labels[i], table, cols, want)
				}
			}

			// The data, not just the schema, must be readable here.
			var count int
			if err := dbs[i].QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
				t.Fatalf("%s: SELECT COUNT(*): %v", labels[i], err)
			}
			if count != 6 {
				t.Fatalf("%s: SELECT COUNT(*) FROM %s = %d, want 6", labels[i], table, count)
			}
			var owner string
			var balance int
			if err := dbs[i].QueryRow(
				fmt.Sprintf("SELECT owner, balance FROM %s WHERE id = ?", table), 4).
				Scan(&owner, &balance); err != nil {
				t.Fatalf("%s: SELECT WHERE id=4: %v", labels[i], err)
			}
			if owner != "owner-4" || balance != 400 {
				t.Fatalf("%s: row id=4 = (%q, %d), want (owner-4, 400)", labels[i], owner, balance)
			}
		}
	})

	// --- Phase 2: ALTER TABLE ... ADD COLUMN ------------------------------
	//
	// Adding a column is a metadata-only catalog mutation (TableService.
	// AlterTable), so the property under test is that the NEW schema version
	// reaches every replica and that rows written under the OLD schema still
	// decode against it -- with the added column reading as its zero value
	// rather than erroring or shifting the other columns.
	t.Run("AddColumn", func(t *testing.T) {
		if _, err := leaderDB.Exec(fmt.Sprintf(
			"ALTER TABLE %s ADD COLUMN currency VARCHAR(8)", table)); err != nil {
			t.Fatalf("ALTER TABLE ADD COLUMN on leader: %v", err)
		}

		for _, i := range c.liveIndexes() {
			cols := sqlColumnNames(t, dbs[i], labels[i], table)
			if len(cols) != 4 || !strings.EqualFold(cols[3], "currency") {
				t.Fatalf("%s: SHOW COLUMNS FROM %s after ADD COLUMN = %v, want [id owner balance currency]",
					labels[i], table, cols)
			}

			// A pre-ALTER row must still decode, with the new column NULL.
			var owner string
			var currency sql.NullString
			if err := dbs[i].QueryRow(
				fmt.Sprintf("SELECT owner, currency FROM %s WHERE id = ?", table), 2).
				Scan(&owner, &currency); err != nil {
				t.Fatalf("%s: SELECT after ADD COLUMN: %v", labels[i], err)
			}
			if owner != "owner-2" {
				t.Fatalf("%s: owner after ADD COLUMN = %q, want owner-2 (column shift?)", labels[i], owner)
			}
			if currency.Valid {
				t.Fatalf("%s: currency of a pre-ALTER row = %q, want NULL", labels[i], currency.String)
			}
		}

		// A write using the new column must round-trip on every replica.
		if _, err := leaderDB.Exec(fmt.Sprintf(
			"INSERT INTO %s (id, owner, balance, currency) VALUES (?, ?, ?, ?)", table),
			7, "owner-7", 700, "USD"); err != nil {
			t.Fatalf("INSERT with new column on leader: %v", err)
		}
		for _, i := range c.liveIndexes() {
			var currency string
			if err := dbs[i].QueryRow(
				fmt.Sprintf("SELECT currency FROM %s WHERE id = ?", table), 7).Scan(&currency); err != nil {
				t.Fatalf("%s: SELECT currency of the post-ALTER row: %v", labels[i], err)
			}
			if currency != "USD" {
				t.Fatalf("%s: currency of row id=7 = %q, want USD", labels[i], currency)
			}
		}
	})

	// --- Phase 3: CREATE INDEX on a NON-EMPTY table -----------------------
	t.Run("CreateIndexOnNonEmptyTableIsBackfilled", func(t *testing.T) {
		// Baseline: with no index on `owner`, the planner must NOT choose an
		// indexed access path. Capturing this first is what turns the
		// post-CREATE assertion below into a differential one -- the plan
		// provably changed because of this statement, so the row assertions
		// that follow really are being answered through the new index.
		const probe = "SELECT id FROM " + table + " WHERE owner = 'owner-3'"
		before := sqlExplain(t, leaderDB, labels[leader], probe)
		if strings.Contains(before, "IndexedTableAccess") {
			t.Fatalf("%s: the probe query already plans an indexed access BEFORE CREATE INDEX, so this phase cannot attribute anything to the index:\n%s",
				labels[leader], before)
		}

		if _, err := leaderDB.Exec(fmt.Sprintf(
			"CREATE INDEX idx_%s_owner ON %s (owner)", table, table)); err != nil {
			t.Fatalf("CREATE INDEX on leader: %v", err)
		}

		for _, i := range c.liveIndexes() {
			// The index must be in the replicated catalog...
			names := sqlIndexNames(t, dbs[i], labels[i], table)
			if !contains(names, "idx_"+table+"_owner") {
				t.Fatalf("%s: SHOW INDEX FROM %s = %v, want it to contain idx_%s_owner",
					labels[i], table, names, table)
			}

			// ...and the planner must actually READ through it. Without this
			// the row assertions below would also pass over a full table
			// scan, which would prove nothing about the index at all. The
			// only index that can serve a predicate on `owner` is the one
			// just created -- the table's other index is the primary key on
			// `id` -- so the plan flipping from the baseline captured above
			// to IndexedTableAccess is attributable to it and nothing else.
			plan := sqlExplain(t, dbs[i], labels[i], probe)
			t.Logf("%s: EXPLAIN after CREATE INDEX:\n%s", labels[i], plan)
			if !strings.Contains(plan, "IndexedTableAccess") {
				t.Fatalf("%s: EXPLAIN of the owner lookup is still a table scan after CREATE INDEX (baseline was:\n%s\nnow:\n%s)",
					labels[i], before, plan)
			}

			// ...and it must be POPULATED. Every one of these 7 rows predates
			// the index, so each lookup can only be answered from the
			// backfill BuildIndex performed. An unpopulated index is not an
			// error -- it is an empty result set, which is exactly why this
			// asserts on rows returned rather than on the absence of an error.
			for id := 1; id <= 7; id++ {
				owner := fmt.Sprintf("owner-%d", id)
				var gotID int
				err := dbs[i].QueryRow(
					fmt.Sprintf("SELECT id FROM %s WHERE owner = ?", table), owner).Scan(&gotID)
				if err == sql.ErrNoRows {
					t.Fatalf("%s: SELECT id FROM %s WHERE owner = %q returned NO ROWS -- the index created on a non-empty table was not backfilled",
						labels[i], table, owner)
				}
				if err != nil {
					t.Fatalf("%s: indexed lookup owner=%q: %v", labels[i], owner, err)
				}
				if gotID != id {
					t.Fatalf("%s: indexed lookup owner=%q returned id %d, want %d",
						labels[i], owner, gotID, id)
				}
			}

			// A row inserted AFTER the index exists must be reachable too, so
			// the assertion above cannot pass on backfill alone.
			if i == leader {
				if _, err := leaderDB.Exec(fmt.Sprintf(
					"INSERT INTO %s (id, owner, balance) VALUES (?, ?, ?)", table),
					8, "owner-8", 800); err != nil {
					t.Fatalf("post-index INSERT on leader: %v", err)
				}
			}
		}
		for _, i := range c.liveIndexes() {
			var gotID int
			if err := dbs[i].QueryRow(
				fmt.Sprintf("SELECT id FROM %s WHERE owner = ?", table), "owner-8").Scan(&gotID); err != nil {
				t.Fatalf("%s: indexed lookup of the post-index row: %v", labels[i], err)
			}
			if gotID != 8 {
				t.Fatalf("%s: indexed lookup owner=owner-8 returned id %d, want 8", labels[i], gotID)
			}
		}
	})

	// --- Phase 4: DROP TABLE ----------------------------------------------
	t.Run("DropTable", func(t *testing.T) {
		if _, err := leaderDB.Exec("DROP TABLE " + table); err != nil {
			t.Fatalf("DROP TABLE on leader: %v", err)
		}
		for _, i := range c.liveIndexes() {
			waitForTableVisible(t, dbs[i], labels[i], table, false, 30*time.Second)
			if _, err := dbs[i].Query("SELECT * FROM " + table); err == nil {
				t.Fatalf("%s: SELECT from the dropped table succeeded, want a not-found error", labels[i])
			}
		}
	})
}

// sqlExplain returns the EXPLAIN output for query as a single newline-joined
// string, so a caller can assert on the shape of the chosen plan.
//
// FORMAT=TREE is required, not stylistic. go-mysql-server's bare EXPLAIN
// returns the classic 12-column MySQL schema whose `rows` column is typed
// Uint64 while rowexec/show.go writes the literal string "NULL" into it, so
// any real MySQL client fails to decode the result set
// (`strconv.ParseUint: parsing "NULL"`). That is an upstream defect, not a
// qrdb one. FORMAT=TREE selects DescribePlanSchema -- a single VARCHAR
// column -- and decodes cleanly.
func sqlExplain(t *testing.T, db *sql.DB, label, query string) string {
	t.Helper()
	rows, err := db.Query("EXPLAIN FORMAT=TREE_DEBUG " + query)
	if err != nil {
		t.Fatalf("%s: EXPLAIN %s: %v", label, query, err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("%s: EXPLAIN columns: %v", label, err)
	}
	var lines []string
	for rows.Next() {
		cells := make([]any, len(cols))
		for i := range cells {
			cells[i] = new(sql.RawBytes)
		}
		if err := rows.Scan(cells...); err != nil {
			t.Fatalf("%s: EXPLAIN scan: %v", label, err)
		}
		parts := make([]string, len(cells))
		for i, cell := range cells {
			parts[i] = string(*cell.(*sql.RawBytes))
		}
		lines = append(lines, strings.Join(parts, " | "))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%s: EXPLAIN iteration: %v", label, err)
	}
	return strings.Join(lines, "\n")
}

// sqlIndexNames returns the index names declared on table, lowercased, over
// the MySQL wire protocol.
func sqlIndexNames(t *testing.T, db *sql.DB, label, table string) []string {
	t.Helper()
	rows, err := db.Query("SHOW INDEX FROM " + table)
	if err != nil {
		t.Fatalf("%s: SHOW INDEX FROM %s: %v", label, table, err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("%s: SHOW INDEX columns: %v", label, err)
	}
	keyNameCol := -1
	for i, c := range cols {
		if strings.EqualFold(c, "Key_name") {
			keyNameCol = i
			break
		}
	}
	if keyNameCol < 0 {
		t.Fatalf("%s: SHOW INDEX FROM %s has no Key_name column (columns: %v)", label, table, cols)
	}

	var names []string
	for rows.Next() {
		cells := make([]any, len(cols))
		for i := range cells {
			cells[i] = new(sql.RawBytes)
		}
		if err := rows.Scan(cells...); err != nil {
			t.Fatalf("%s: SHOW INDEX scan: %v", label, err)
		}
		names = append(names, strings.ToLower(string(*cells[keyNameCol].(*sql.RawBytes))))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%s: SHOW INDEX iteration: %v", label, err)
	}
	return names
}

// TestSQLCluster_DDL_FollowerOriginatedDDL is the mirror image of
// TestSQLCluster_DDL_ReplicatedCatalog: the DDL is issued against a FOLLOWER
// rather than the leader.
//
// This is not redundant. A follower cannot commit anything itself; the
// catalog compare-and-swap behind CREATE TABLE only reaches the log because
// quicraft's Host.Propose forwards a proposal to the known leader instead of
// rejecting it (see Host.Propose). If that forwarding regressed, a MySQL
// client that happened to connect to a follower would get a NotLeader error
// for every DDL statement -- a failure mode invisible to any test that only
// ever talks to the leader.
func TestSQLCluster_DDL_FollowerOriginatedDDL(t *testing.T) {
	c := startThreeNodeClusterWithSQL(t)
	leader := c.leaderIndex(t, 60*time.Second)
	followers := c.followerIndexes(leader)
	follower := followers[0]
	t.Logf("issuing DDL against follower replica %d (leader is replica %d)", follower+1, leader+1)

	followerDB := c.mysql(t, follower)
	leaderDB := c.mysql(t, leader)

	const table = "ddl_from_follower"
	if _, err := followerDB.Exec(fmt.Sprintf(
		"CREATE TABLE %s (id INT PRIMARY KEY, note VARCHAR(64))", table)); err != nil {
		t.Fatalf("CREATE TABLE on follower: %v", err)
	}
	if _, err := followerDB.Exec(fmt.Sprintf(
		"INSERT INTO %s (id, note) VALUES (?, ?)", table), 1, "written-on-a-follower"); err != nil {
		t.Fatalf("INSERT on follower: %v", err)
	}

	for _, i := range c.liveIndexes() {
		label := fmt.Sprintf("replica%d", i+1)
		db := followerDB
		if i == leader {
			db = leaderDB
		} else if i != follower {
			db = c.mysql(t, i)
		}
		waitForTableVisible(t, db, label, table, true, 30*time.Second)
		var note string
		if err := db.QueryRow(fmt.Sprintf("SELECT note FROM %s WHERE id = ?", table), 1).Scan(&note); err != nil {
			t.Fatalf("%s: SELECT of the follower-originated row: %v", label, err)
		}
		if note != "written-on-a-follower" {
			t.Fatalf("%s: note = %q, want written-on-a-follower", label, note)
		}
	}
}
