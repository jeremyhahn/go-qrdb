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
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Column defaults and SHOW CREATE TABLE over the node:// DSN.
//
// Everything below is asserted where a real client sees it -- through
// database/sql against the full in-process relational stack -- because both
// behaviours pinned here were lost between layers that each looked correct in
// isolation: pkg/sql/provider's DDL path persisted every column default, and
// pkg/sql/catalog's SchemaOf built a go-mysql-server schema that never read it
// back, so a default survived CREATE TABLE and then vanished.
//
// embedded:// deliberately cannot be used here: it has no relational catalog
// at all (see TestEmbeddedScheme_CreateTableStillUnavailable).
// ---------------------------------------------------------------------------

// TestNode_ColumnDefaultIsVisibleInInformationSchema verifies a declared
// DEFAULT reaches information_schema.COLUMNS, which is what a MySQL client --
// GORM's migrator among them -- reads to decide whether a column already
// matches the model.
func TestNode_ColumnDefaultIsVisibleInInformationSchema(t *testing.T) {
	db := openNode(t, "node://memory?database=defmeta")
	if _, err := db.Exec(`CREATE TABLE defaults_meta (
		id BIGINT PRIMARY KEY,
		s VARCHAR(64) DEFAULT 'null',
		b BOOLEAN DEFAULT false,
		n BIGINT DEFAULT 7,
		plain VARCHAR(64)
	)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	want := map[string]sql.NullString{
		"id":    {},
		"s":     {String: "null", Valid: true},
		"b":     {String: "0", Valid: true},
		"n":     {String: "7", Valid: true},
		"plain": {},
	}

	rows, err := db.Query(`SELECT COLUMN_NAME, COLUMN_DEFAULT FROM information_schema.COLUMNS WHERE TABLE_NAME = 'defaults_meta'`)
	if err != nil {
		t.Fatalf("query information_schema.COLUMNS: %v", err)
	}
	defer rows.Close()

	seen := 0
	for rows.Next() {
		var name string
		var got sql.NullString
		if err := rows.Scan(&name, &got); err != nil {
			t.Fatalf("scan: %v", err)
		}
		expected, ok := want[name]
		if !ok {
			t.Errorf("unexpected column %q", name)
			continue
		}
		if got != expected {
			t.Errorf("COLUMN_DEFAULT for %q = %#v, want %#v", name, got, expected)
		}
		seen++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if seen != len(want) {
		t.Fatalf("saw %d columns, want %d", seen, len(want))
	}
}

// TestNode_ColumnDefaultIsAppliedOnInsert verifies an INSERT omitting a
// column stores that column's declared default rather than NULL. This is the
// half of the defect that no amount of metadata reporting would cover: the
// default has to reach the engine's insert plan, not just the catalog view.
func TestNode_ColumnDefaultIsAppliedOnInsert(t *testing.T) {
	db := openNode(t, "node://memory?database=definsert")
	if _, err := db.Exec(`CREATE TABLE defaults_row (
		id BIGINT PRIMARY KEY,
		s VARCHAR(64) DEFAULT 'null',
		b BOOLEAN DEFAULT false,
		n BIGINT DEFAULT 7
	)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO defaults_row (id) VALUES (1)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	var s sql.NullString
	var b sql.NullBool
	var n sql.NullInt64
	if err := db.QueryRow(`SELECT s, b, n FROM defaults_row WHERE id = 1`).Scan(&s, &b, &n); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if !s.Valid || s.String != "null" {
		t.Errorf("s = %#v, want the literal string \"null\"", s)
	}
	if !b.Valid || b.Bool {
		t.Errorf("b = %#v, want false", b)
	}
	if !n.Valid || n.Int64 != 7 {
		t.Errorf("n = %#v, want 7", n)
	}
}

// TestNode_ColumnDefaultOverriddenByExplicitValue verifies a supplied value
// still wins over the declared default, including an explicit NULL, so the
// default is applied only where MySQL applies it.
func TestNode_ColumnDefaultOverriddenByExplicitValue(t *testing.T) {
	db := openNode(t, "node://memory?database=defoverride")
	if _, err := db.Exec(`CREATE TABLE defaults_override (
		id BIGINT PRIMARY KEY,
		n BIGINT DEFAULT 7
	)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO defaults_override (id, n) VALUES (1, 99), (2, NULL)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	var supplied, explicitNull sql.NullInt64
	if err := db.QueryRow(`SELECT n FROM defaults_override WHERE id = 1`).Scan(&supplied); err != nil {
		t.Fatalf("SELECT id=1: %v", err)
	}
	if !supplied.Valid || supplied.Int64 != 99 {
		t.Errorf("supplied value = %#v, want 99", supplied)
	}
	if err := db.QueryRow(`SELECT n FROM defaults_override WHERE id = 2`).Scan(&explicitNull); err != nil {
		t.Fatalf("SELECT id=2: %v", err)
	}
	if explicitNull.Valid {
		t.Errorf("explicit NULL = %#v, want NULL", explicitNull)
	}
}

// TestNode_ShowCreateTableWithoutAutoIncrement verifies SHOW CREATE TABLE
// works for a table with no AUTO_INCREMENT column, which is the common case.
//
// go-mysql-server calls PeekNextAutoIncrementValue unconditionally while
// rendering the statement and tolerates exactly one failure from it, matched
// by comparing the rendered error message against
// sql.ErrNoAutoIncrementCol.Error() (sql/rowexec/show_iters.go). Any other
// message aborts the whole statement.
func TestNode_ShowCreateTableWithoutAutoIncrement(t *testing.T) {
	db := openNode(t, "node://memory?database=showcreate")
	if _, err := db.Exec(`CREATE TABLE noai (
		id BIGINT PRIMARY KEY,
		name VARCHAR(64) NOT NULL DEFAULT 'x'
	)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	var name, ddl string
	if err := db.QueryRow(`SHOW CREATE TABLE noai`).Scan(&name, &ddl); err != nil {
		t.Fatalf("SHOW CREATE TABLE: %v", err)
	}
	for _, want := range []string{"`id` bigint NOT NULL", "`name` varchar(64) NOT NULL DEFAULT 'x'", "PRIMARY KEY (`id`)"} {
		if !strings.Contains(ddl, want) {
			t.Errorf("emitted DDL is missing %q:\n%s", want, ddl)
		}
	}
	if strings.Contains(ddl, "AUTO_INCREMENT") {
		t.Errorf("emitted DDL declares AUTO_INCREMENT for a table that has none:\n%s", ddl)
	}
}

// TestNode_ShowCreateTableRoundTrips verifies the DDL SHOW CREATE TABLE emits
// can be fed back to CREATE TABLE and produces a table that renders
// identically -- the property that makes the output usable as a schema dump
// rather than merely as documentation.
//
// The composite secondary index is deliberately absent from the assertions
// here: it is emitted with only its leading column, which is a separate,
// independently-tracked defect in pkg/sql/provider's index adapter. The
// output is still round-trip stable, just lossy about that one clause.
//
// The keyless subtest is the SHOW CREATE TABLE half of the hidden row id
// (pkg/sql/catalog's rowid.go): a table with no declared PRIMARY KEY is
// physically keyed by a hidden system column, and neither that column nor a
// PRIMARY KEY clause naming it may appear in the emitted DDL -- if either
// did, the emitted DDL would not even be replayable, since the parser
// rejects a statement that names a hidden system column.
func TestNode_ShowCreateTableRoundTrips(t *testing.T) {
	cases := []struct {
		name    string
		table   string
		ddl     string
		want    []string
		notWant []string
	}{
		{
			name:  "declared primary key",
			table: "orig",
			ddl: "CREATE TABLE orig (" +
				"id BIGINT AUTO_INCREMENT," +
				"code VARCHAR(32) NOT NULL," +
				"qty INT NOT NULL DEFAULT 3," +
				"note VARCHAR(64) DEFAULT 'hi'," +
				"created DATETIME DEFAULT CURRENT_TIMESTAMP," +
				"PRIMARY KEY (id)," +
				"UNIQUE KEY uq_code (code))",
			want: []string{
				"`id` bigint NOT NULL AUTO_INCREMENT",
				"`qty` int NOT NULL DEFAULT '3'",
				"`note` varchar(64) DEFAULT 'hi'",
				"`created` datetime DEFAULT CURRENT_TIMESTAMP",
				"PRIMARY KEY (`id`)",
				"UNIQUE KEY `uq_code` (`code`)",
			},
		},
		{
			name:  "no primary key",
			table: "keyless",
			ddl: "CREATE TABLE keyless (" +
				"name VARCHAR(64)," +
				"n BIGINT UNSIGNED," +
				"KEY idx_n (n))",
			want: []string{
				"`name` varchar(64)",
				"`n` bigint unsigned",
				"KEY `idx_n` (`n`)",
			},
			notWant: []string{"PRIMARY KEY", "hidden", "rowid"},
		},
	}

	db := openNode(t, "node://memory?database=roundtrip")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.Exec(tc.ddl); err != nil {
				t.Fatalf("CREATE TABLE %s: %v", tc.table, err)
			}

			var name, emitted string
			if err := db.QueryRow(`SHOW CREATE TABLE `+tc.table).Scan(&name, &emitted); err != nil {
				t.Fatalf("SHOW CREATE TABLE %s: %v", tc.table, err)
			}
			for _, want := range tc.want {
				if !strings.Contains(emitted, want) {
					t.Errorf("emitted DDL is missing %q:\n%s", want, emitted)
				}
			}
			for _, notWant := range tc.notWant {
				if strings.Contains(strings.ToLower(emitted), strings.ToLower(notWant)) {
					t.Errorf("emitted DDL leaks %q:\n%s", notWant, emitted)
				}
			}

			replayedName := tc.table + "_replayed"
			replayed := strings.Replace(emitted, "`"+tc.table+"`", "`"+replayedName+"`", 1)
			if _, err := db.Exec(replayed); err != nil {
				t.Fatalf("replaying the emitted DDL failed: %v\n%s", err, replayed)
			}

			var reemitted string
			if err := db.QueryRow(`SHOW CREATE TABLE `+replayedName).Scan(&name, &reemitted); err != nil {
				t.Fatalf("SHOW CREATE TABLE %s: %v", replayedName, err)
			}
			if reemitted != replayed {
				t.Errorf("SHOW CREATE TABLE is not round-trip stable:\nfed in:\n%s\ngot back:\n%s", replayed, reemitted)
			}
		})
	}
}
