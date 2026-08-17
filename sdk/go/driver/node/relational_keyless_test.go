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

package node

import (
	"database/sql"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// A table with no declared PRIMARY KEY, asserted where a real client sees it
// -- through database/sql against the full in-process relational stack.
//
// Such a table is physically keyed by a hidden system row id (see
// pkg/sql/catalog's rowid.go), and the entire point of that column is that no
// client can tell it is there. Every assertion below is therefore about
// something a MySQL client can actually observe: how many rows come back, what
// SELECT * yields, what information_schema reports, and what happens when a
// statement names the column outright.
//
// embedded:// deliberately cannot be used here: it has no relational catalog
// at all (see TestEmbeddedScheme_CreateTableStillUnavailable).
// ---------------------------------------------------------------------------

// TestNode_KeylessTableIdenticalRowsAreDistinct is the regression test for
// silent data loss. Before the hidden row id existed, a keyless table whose
// last column happened to be BIGINT UNSIGNED had that column read as its
// physical primary key, so a second row sharing the value was rejected as a
// duplicate and two distinct rows became one.
func TestNode_KeylessTableIdenticalRowsAreDistinct(t *testing.T) {
	db := openNode(t, "node://memory?database=keylessdup")
	if _, err := db.Exec(`CREATE TABLE h (name VARCHAR(64), n BIGINT UNSIGNED)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	for _, stmt := range []string{
		`INSERT INTO h VALUES ('a', 7)`,
		`INSERT INTO h VALUES ('b', 7)`,
		`INSERT INTO h VALUES ('a', 7)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM h`).Scan(&count); err != nil {
		t.Fatalf("SELECT COUNT(*): %v", err)
	}
	if count != 3 {
		t.Fatalf("COUNT(*) = %d, want 3 (two of the rows are identical and must still be two rows)", count)
	}

	var sevens int
	if err := db.QueryRow(`SELECT COUNT(*) FROM h WHERE n = 7`).Scan(&sevens); err != nil {
		t.Fatalf("SELECT COUNT(*) WHERE n = 7: %v", err)
	}
	if sevens != 3 {
		t.Fatalf("COUNT(*) WHERE n = 7 = %d, want 3", sevens)
	}
}

// TestNode_KeylessTableSelectStarExcludesRowID verifies the hidden column is
// absent from star expansion and from the result set's column list, so an
// application scanning SELECT * sees exactly the columns it declared.
func TestNode_KeylessTableSelectStarExcludesRowID(t *testing.T) {
	db := openNode(t, "node://memory?database=keylessstar")
	if _, err := db.Exec(`CREATE TABLE h (name VARCHAR(64), n BIGINT UNSIGNED)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO h VALUES ('a', 7)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	rows, err := db.Query(`SELECT * FROM h`)
	if err != nil {
		t.Fatalf("SELECT *: %v", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("Columns: %v", err)
	}
	if len(cols) != 2 || cols[0] != "name" || cols[1] != "n" {
		t.Fatalf("SELECT * columns = %v, want [name n]", cols)
	}

	if !rows.Next() {
		t.Fatal("SELECT * returned no rows")
	}
	var name string
	var n uint64
	if err := rows.Scan(&name, &n); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if name != "a" || n != 7 {
		t.Fatalf("row = (%q, %d), want (\"a\", 7)", name, n)
	}
}

// TestNode_KeylessTableRowIDCannotBeReferenced verifies the engine rejects
// every statement that names the hidden column, so it can never be selected,
// filtered on, inserted into, or updated.
func TestNode_KeylessTableRowIDCannotBeReferenced(t *testing.T) {
	db := openNode(t, "node://memory?database=keylessref")
	if _, err := db.Exec(`CREATE TABLE h (name VARCHAR(64))`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO h VALUES ('a')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	const hidden = "`!hidden!rowid`"
	statements := []string{
		"SELECT " + hidden + " FROM h",
		"SELECT name FROM h WHERE " + hidden + " = 1",
		"INSERT INTO h (name, " + hidden + ") VALUES ('b', 1)",
		"UPDATE h SET " + hidden + " = 2",
	}
	for _, stmt := range statements {
		t.Run(stmt, func(t *testing.T) {
			if _, err := db.Exec(stmt); err == nil {
				t.Fatal("statement naming the hidden row id succeeded, want an error")
			}
		})
	}
}

// TestNode_KeylessTableInformationSchemaOmitsRowID verifies the hidden column
// is absent from information_schema.COLUMNS -- which is what GORM's migrator
// and every schema-introspection tool reads -- and that the declared columns
// keep their natural ORDINAL_POSITION and report no COLUMN_KEY.
func TestNode_KeylessTableInformationSchemaOmitsRowID(t *testing.T) {
	db := openNode(t, "node://memory?database=keylessis")
	if _, err := db.Exec(`CREATE TABLE h (name VARCHAR(64), n BIGINT UNSIGNED, KEY idx_n (n))`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	rows, err := db.Query(`SELECT COLUMN_NAME, ORDINAL_POSITION, COLUMN_KEY
		FROM information_schema.COLUMNS WHERE TABLE_NAME = 'h' ORDER BY ORDINAL_POSITION`)
	if err != nil {
		t.Fatalf("query information_schema.COLUMNS: %v", err)
	}
	defer rows.Close()

	type column struct {
		name     string
		position int
		key      string
	}
	var got []column
	for rows.Next() {
		var c column
		if err := rows.Scan(&c.name, &c.position, &c.key); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	want := []column{{"name", 1, ""}, {"n", 2, "MUL"}}
	if len(got) != len(want) {
		t.Fatalf("information_schema.COLUMNS reported %+v, want %+v", got, want)
	}
	for i, c := range got {
		if c != want[i] {
			t.Errorf("column %d = %+v, want %+v", i, c, want[i])
		}
	}
}

// TestNode_KeylessTableStatisticsOmitsRowID verifies the hidden row id is not
// reported as an index either: information_schema.STATISTICS must list only
// the user's own secondary index, with no PRIMARY entry.
func TestNode_KeylessTableStatisticsOmitsRowID(t *testing.T) {
	db := openNode(t, "node://memory?database=keylessstats")
	if _, err := db.Exec(`CREATE TABLE h (name VARCHAR(64), n BIGINT UNSIGNED, KEY idx_n (n))`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	rows, err := db.Query(`SELECT INDEX_NAME, COLUMN_NAME FROM information_schema.STATISTICS
		WHERE TABLE_NAME = 'h' ORDER BY INDEX_NAME, SEQ_IN_INDEX`)
	if err != nil {
		t.Fatalf("query information_schema.STATISTICS: %v", err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var indexName, columnName string
		if err := rows.Scan(&indexName, &columnName); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, indexName+"("+columnName+")")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(got) != 1 || got[0] != "idx_n(n)" {
		t.Fatalf("STATISTICS reported %v, want exactly [idx_n(n)]", got)
	}
}

// TestNode_KeylessTableDescribeOmitsRowID verifies DESCRIBE / SHOW COLUMNS,
// the other surface a client reads a table's shape from, is also clean.
func TestNode_KeylessTableDescribeOmitsRowID(t *testing.T) {
	db := openNode(t, "node://memory?database=keylessdesc")
	if _, err := db.Exec(`CREATE TABLE h (name VARCHAR(64), n BIGINT UNSIGNED)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	rows, err := db.Query(`DESCRIBE h`)
	if err != nil {
		t.Fatalf("DESCRIBE: %v", err)
	}
	defer rows.Close()

	var fields []string
	for rows.Next() {
		var field, colType, null, key string
		var def, extra sql.NullString
		if err := rows.Scan(&field, &colType, &null, &key, &def, &extra); err != nil {
			t.Fatalf("scan: %v", err)
		}
		fields = append(fields, field)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(fields) != 2 || fields[0] != "name" || fields[1] != "n" {
		t.Fatalf("DESCRIBE reported %v, want [name n]", fields)
	}
}

// TestNode_KeylessTableUpdateAndDelete verifies the two statements that need
// the hidden row id to arrive back in the row: an UPDATE must modify the
// matching row in place (not duplicate it, and not touch its neighbours), and
// a DELETE must remove exactly the matching rows.
func TestNode_KeylessTableUpdateAndDelete(t *testing.T) {
	db := openNode(t, "node://memory?database=keylessdml")
	if _, err := db.Exec(`CREATE TABLE h (name VARCHAR(64), n BIGINT)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO h VALUES ('a',1),('b',2),('c',3)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	res, err := db.Exec(`UPDATE h SET n = 99 WHERE name = 'b'`)
	if err != nil {
		t.Fatalf("UPDATE: %v", err)
	}
	if affected, _ := res.RowsAffected(); affected != 1 {
		t.Fatalf("UPDATE affected %d rows, want 1", affected)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM h`).Scan(&count); err != nil {
		t.Fatalf("COUNT after UPDATE: %v", err)
	}
	if count != 3 {
		t.Fatalf("COUNT(*) = %d after UPDATE, want 3 (the update must not have duplicated a row)", count)
	}
	var n int64
	if err := db.QueryRow(`SELECT n FROM h WHERE name = 'b'`).Scan(&n); err != nil {
		t.Fatalf("SELECT after UPDATE: %v", err)
	}
	if n != 99 {
		t.Fatalf("n = %d after UPDATE, want 99", n)
	}

	res, err = db.Exec(`DELETE FROM h WHERE name = 'a'`)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	if affected, _ := res.RowsAffected(); affected != 1 {
		t.Fatalf("DELETE affected %d rows, want 1", affected)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM h`).Scan(&count); err != nil {
		t.Fatalf("COUNT after DELETE: %v", err)
	}
	if count != 2 {
		t.Fatalf("COUNT(*) = %d after DELETE, want 2", count)
	}
}

// TestNode_KeylessTableIndexedDeleteKeepsRowID is the projection-pushdown
// case. An index-accelerated DELETE reaches the writer through a different
// plan shape than a full scan does, and a planner that pruned the hidden
// column out of that plan would leave the writer without a key. The index
// entries must go with the rows.
func TestNode_KeylessTableIndexedDeleteKeepsRowID(t *testing.T) {
	db := openNode(t, "node://memory?database=keylessidx")
	if _, err := db.Exec(`CREATE TABLE h (name VARCHAR(64), n BIGINT, KEY idx_n (n))`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO h VALUES ('a',1),('b',2),('c',2),('d',3)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	res, err := db.Exec(`DELETE FROM h WHERE n = 2`)
	if err != nil {
		t.Fatalf("indexed DELETE: %v", err)
	}
	if affected, _ := res.RowsAffected(); affected != 2 {
		t.Fatalf("indexed DELETE affected %d rows, want 2", affected)
	}

	for _, tc := range []struct {
		query string
		want  int
	}{
		{`SELECT COUNT(*) FROM h`, 2},
		{`SELECT COUNT(*) FROM h WHERE n = 2`, 0},
		{`SELECT COUNT(*) FROM h WHERE n = 1`, 1},
	} {
		var got int
		if err := db.QueryRow(tc.query).Scan(&got); err != nil {
			t.Fatalf("%s: %v", tc.query, err)
		}
		if got != tc.want {
			t.Errorf("%s = %d, want %d", tc.query, got, tc.want)
		}
	}

	// An index-accelerated UPDATE must move the entry with the row.
	if _, err := db.Exec(`UPDATE h SET n = 5 WHERE n = 3`); err != nil {
		t.Fatalf("indexed UPDATE: %v", err)
	}
	var stale, moved int
	if err := db.QueryRow(`SELECT COUNT(*) FROM h WHERE n = 3`).Scan(&stale); err != nil {
		t.Fatalf("COUNT n=3: %v", err)
	}
	if stale != 0 {
		t.Errorf("the old index entry outlived the UPDATE (COUNT n=3 = %d)", stale)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM h WHERE n = 5`).Scan(&moved); err != nil {
		t.Fatalf("COUNT n=5: %v", err)
	}
	if moved != 1 {
		t.Errorf("COUNT n=5 = %d, want 1", moved)
	}
}

// TestNode_KeylessTableScanReturnsInsertionOrder verifies the row id's
// order-preserving encoding gives a keyless table the scan order InnoDB gives
// one: the order the rows were inserted in.
func TestNode_KeylessTableScanReturnsInsertionOrder(t *testing.T) {
	db := openNode(t, "node://memory?database=keylessorder")
	if _, err := db.Exec(`CREATE TABLE h (name VARCHAR(64))`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	inserted := []string{"c", "a", "b", "a"}
	for _, name := range inserted {
		if _, err := db.Exec(`INSERT INTO h VALUES (?)`, name); err != nil {
			t.Fatalf("INSERT %q: %v", name, err)
		}
	}

	rows, err := db.Query(`SELECT name FROM h`)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if strings.Join(got, ",") != strings.Join(inserted, ",") {
		t.Fatalf("scan order = %v, want insertion order %v", got, inserted)
	}
}

// TestNode_KeylessTableInsertSelectAndReplace verifies the two INSERT shapes
// that do not go through a plain VALUES list still allocate a row id per row.
func TestNode_KeylessTableInsertSelectAndReplace(t *testing.T) {
	db := openNode(t, "node://memory?database=keylessins")
	if _, err := db.Exec(`CREATE TABLE src (a INT, b VARCHAR(8))`); err != nil {
		t.Fatalf("CREATE src: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE dst (a INT, b VARCHAR(8))`); err != nil {
		t.Fatalf("CREATE dst: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO src (a, b) VALUES (1,'x'),(2,'y')`); err != nil {
		t.Fatalf("INSERT src: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO dst SELECT a, b FROM src`); err != nil {
		t.Fatalf("INSERT ... SELECT: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM dst`).Scan(&count); err != nil {
		t.Fatalf("COUNT: %v", err)
	}
	if count != 2 {
		t.Fatalf("COUNT(*) after INSERT ... SELECT = %d, want 2", count)
	}

	// With no unique key to conflict on, REPLACE is an INSERT -- and, as in
	// MySQL, appends rather than replacing anything.
	if _, err := db.Exec(`REPLACE INTO dst (a, b) VALUES (1,'x')`); err != nil {
		t.Fatalf("REPLACE: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM dst`).Scan(&count); err != nil {
		t.Fatalf("COUNT after REPLACE: %v", err)
	}
	if count != 3 {
		t.Fatalf("COUNT(*) after REPLACE = %d, want 3", count)
	}
}

// TestNode_KeylessTableWithAutoIncrementColumn verifies both generated values
// coexist on one table: MySQL allows an AUTO_INCREMENT column keyed by a
// UNIQUE index rather than a PRIMARY KEY, and such a table has BOTH an
// AUTO_INCREMENT sequence and a hidden row id sequence live at once. They are
// separate counters, so neither consumes the other's values.
func TestNode_KeylessTableWithAutoIncrementColumn(t *testing.T) {
	db := openNode(t, "node://memory?database=keylessai")
	if _, err := db.Exec(`CREATE TABLE ai (
		id BIGINT AUTO_INCREMENT,
		name VARCHAR(8),
		UNIQUE KEY uq_id (id))`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO ai (name) VALUES ('p'),('q'),('r')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	rows, err := db.Query(`SELECT id, name FROM ai ORDER BY id`)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	defer rows.Close()

	want := []string{"1:p", "2:q", "3:r"}
	var got []string
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, strconv.FormatInt(id, 10)+":"+name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("rows = %v, want %v", got, want)
	}
}
