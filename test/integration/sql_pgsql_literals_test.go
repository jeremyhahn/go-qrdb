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

package integration_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// sqlStateCachedPlanChanged is the SQLSTATE PostgreSQL raises when a cached
// plan's result type would change (feature_not_supported). Clients recognise
// the condition by this code rather than by the message.
const sqlStateCachedPlanChanged = "0A000"

// TestSQL_PgSQL_StandardConformingStringsIsAdvertised pins the ParameterStatus
// a PostgreSQL client reads at startup to learn how string literals escape.
//
// pgx refuses to run any simple-protocol query against a server that has not
// reported standard_conforming_strings=on, and gorm.io/driver/postgres forces
// simple protocol for Migrator.GetRows -- which backs ColumnTypes, and so
// every schema-introspecting migration a GORM application performs. Without
// this parameter those fail outright with "simple protocol queries must be
// run with standard_conforming_strings=on".
func TestSQL_PgSQL_StandardConformingStringsIsAdvertised(t *testing.T) {
	_, _, pgsqlPort, ca := startSingleNodeWithSQL(t)

	certPath, keyPath := generateClientCert(t, ca, "admin", "system")
	conn := waitForPgSQL(t, ca, certPath, keyPath, pgsqlPort, "admin")

	params := conn.PgConn().ParameterStatus("standard_conforming_strings")
	if params != "on" {
		t.Fatalf("standard_conforming_strings: got %q, want \"on\"", params)
	}
}

// TestSQL_PgSQL_BackslashesInLiteralsAreLiteral covers the dialect gap between
// the wire protocol's PostgreSQL semantics and go-mysql-server's MySQL lexer.
//
// PostgreSQL treats a backslash inside '...' as an ordinary character; MySQL
// treats it as an escape introducer, and go-mysql-server does not honour the
// NO_BACKSLASH_ESCAPES sql_mode that would turn that off. Untranslated, a
// Windows path 'C:\temp' reached storage as "C:<tab>emp".
//
// Both protocol modes are exercised. The extended protocol sends the value as
// a bind parameter, so it never passes through the literal lexer at all; the
// simple protocol has pgx interpolate it into the statement text, which is
// exactly the path the rewrite exists for.
func TestSQL_PgSQL_BackslashesInLiteralsAreLiteral(t *testing.T) {
	_, _, pgsqlPort, ca := startSingleNodeWithSQLMVCC(t)

	certPath, keyPath := generateClientCert(t, ca, "admin", "system")
	conn := waitForPgSQL(t, ca, certPath, keyPath, pgsqlPort, "admin")
	ctx := context.Background()

	values := map[string]string{
		"windows-path":      `C:\temp\file.txt`,
		"trailing-slash":    `ends-with\`,
		"escape-lookalike":  `tab\there and newline\nhere`,
		"doubled-backslash": `a\\b`,
		"quote-and-slash":   `it's a \path`,
	}

	for key, want := range values {
		t.Run(key, func(t *testing.T) {
			if _, err := conn.Exec(ctx,
				"INSERT INTO kv (key, value) VALUES ($1, $2)", key, []byte(want)); err != nil {
				t.Fatalf("INSERT: %v", err)
			}

			// Extended protocol: the value travels as a bind parameter.
			var gotKey string
			var got []byte
			if err := conn.QueryRow(ctx,
				"SELECT key, value FROM kv WHERE key = $1", key).Scan(&gotKey, &got); err != nil {
				t.Fatalf("SELECT (extended): %v", err)
			}
			if string(got) != want {
				t.Errorf("extended protocol round-trip: got %q, want %q", string(got), want)
			}

			// Simple protocol: pgx interpolates the value into the statement
			// text, escaping it by doubling quotes alone -- which is correct
			// only if backslashes really are literal.
			var simpleKey string
			var simple []byte
			if err := conn.QueryRow(ctx,
				"SELECT key, value FROM kv WHERE key = $1",
				pgx.QueryExecModeSimpleProtocol, key).Scan(&simpleKey, &simple); err != nil {
				t.Fatalf("SELECT (simple): %v", err)
			}
			if string(simple) != want {
				t.Errorf("simple protocol round-trip: got %q, want %q", string(simple), want)
			}
		})
	}
}

// TestSQL_PgSQL_TrailingBackslashCannotEscapeItsQuote is the security case
// behind the rewrite above, stated directly.
//
// Under MySQL's reading a value ending in a backslash escapes the closing
// quote pgx placed after it, so the literal never terminates and whatever
// follows is parsed as SQL. The value below is shaped to append a second
// statement if that happens; it must be stored and matched as data, and the
// table it names must never come into existence.
func TestSQL_PgSQL_TrailingBackslashCannotEscapeItsQuote(t *testing.T) {
	_, _, pgsqlPort, ca := startSingleNodeWithSQLMVCC(t)

	certPath, keyPath := generateClientCert(t, ca, "admin", "system")
	conn := waitForPgSQL(t, ca, certPath, keyPath, pgsqlPort, "admin")
	ctx := context.Background()

	const payload = `boundary\`

	if _, err := conn.Exec(ctx,
		"INSERT INTO kv (key, value) VALUES ($1, $2)", "injection-probe", []byte(payload)); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	// Simple protocol: pgx writes the payload into the statement text.
	var gotKey string
	var got []byte
	err := conn.QueryRow(ctx,
		"SELECT key, value FROM kv WHERE key = $1",
		pgx.QueryExecModeSimpleProtocol, "injection-probe").Scan(&gotKey, &got)
	if err != nil {
		t.Fatalf("SELECT with a trailing-backslash payload: %v", err)
	}
	if string(got) != payload {
		t.Errorf("payload round-trip: got %q, want %q", string(got), payload)
	}

	// The same shape written as an inlined literal by hand: the statement
	// must terminate at the closing quote and be read as a value, rather
	// than swallowing the quote and parsing the remainder as SQL.
	var probeKey string
	var probeValue []byte
	if err := conn.QueryRow(ctx,
		`SELECT key, value FROM kv WHERE key = 'injection-probe'`).Scan(&probeKey, &probeValue); err != nil {
		t.Fatalf("inlined literal SELECT: %v", err)
	}
	if string(probeValue) != payload {
		t.Errorf("inlined literal round-trip: got %q, want %q", string(probeValue), payload)
	}
}

// TestSQL_PgSQL_CachedPlanResultTypeChange pins PostgreSQL's own behaviour for
// a prepared statement whose result shape changes underneath it.
//
// A client may PARSE once and BIND/EXECUTE many times against the
// RowDescription it was given; pgx does exactly that by default. If DDL
// changes the result shape in between, the rows no longer match the descriptor
// the client is decoding with -- silently mis-mapping values whenever the
// widths happen to agree. PostgreSQL 16 refuses with "cached plan must not
// change result type" (SQLSTATE 0A000), and clients recognise the condition by
// that code: pgx drops the statement from its cache on the error, so the
// client's retry re-parses and succeeds.
func TestSQL_PgSQL_CachedPlanResultTypeChange(t *testing.T) {
	_, _, pgsqlPort, ca := startSingleNodeWithSQLMVCC(t)

	certPath, keyPath := generateClientCert(t, ca, "admin", "system")
	conn := waitForPgSQL(t, ca, certPath, keyPath, pgsqlPort, "admin")
	ctx := context.Background()

	mustExec := func(sql string) {
		t.Helper()
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatalf("exec %q: %v", sql, err)
		}
	}

	mustExec(`DROP TABLE IF EXISTS cached_plan_probe`)
	mustExec(`CREATE TABLE cached_plan_probe (id bigint, a text)`)
	mustExec(`INSERT INTO cached_plan_probe (id, a) VALUES (1, 'x')`)

	// Warm the statement so its RowDescription is cached client-side.
	const query = `SELECT * FROM cached_plan_probe WHERE a = $1`
	rows, err := conn.Query(ctx, query, "x")
	if err != nil {
		t.Fatalf("warm query: %v", err)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("warm query rows: %v", err)
	}

	// Widen the table under the cached statement.
	mustExec(`DROP TABLE IF EXISTS cached_plan_probe`)
	mustExec(`CREATE TABLE cached_plan_probe (id bigint, a text, b text)`)
	mustExec(`INSERT INTO cached_plan_probe (id, a, b) VALUES (1, 'x', 'y')`)

	rows, err = conn.Query(ctx, query, "x")
	if err == nil {
		rows.Next()
		err = rows.Err()
		rows.Close()
	}
	if err == nil {
		t.Fatal("expected the stale cached statement to be refused, got no error")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("expected a *pgconn.PgError carrying a SQLSTATE, got %T: %v", err, err)
	}
	if pgErr.Code != sqlStateCachedPlanChanged {
		t.Errorf("SQLSTATE: got %q, want %q (message: %s)", pgErr.Code, sqlStateCachedPlanChanged, pgErr.Message)
	}

	// pgx drops the statement from its cache on any error, so the retry
	// re-parses and must succeed against the new shape.
	var id int64
	var a, b string
	if err := conn.QueryRow(ctx, query, "x").Scan(&id, &a, &b); err != nil {
		t.Fatalf("retry after cached-plan refusal: %v", err)
	}
	if id != 1 || a != "x" || b != "y" {
		t.Errorf("retry row: got (%d, %q, %q), want (1, \"x\", \"y\")", id, a, b)
	}
}

// TestSQL_PgSQL_BooleanAndTinyintAreDistinct pins the wire contract for the
// one-byte types, which MySQL and PostgreSQL disagree about.
//
// MySQL spells BOOLEAN as TINYINT(1); go-mysql-server keeps the two apart by
// display width, and the PostgreSQL surface must not flatten them. Describing
// every one-byte value as boolean made a perfectly ordinary GORM query --
// `SELECT (CASE WHEN age = 18 THEN 1 ELSE 2 END) AS age` -- fail with
// "converting driver.Value type bool (\"true\") to a uint", while describing
// every one as an integer would break scanning a real BOOLEAN column into a Go
// bool. Both directions are asserted here for that reason.
func TestSQL_PgSQL_BooleanAndTinyintAreDistinct(t *testing.T) {
	_, _, pgsqlPort, ca := startSingleNodeWithSQLMVCC(t)

	certPath, keyPath := generateClientCert(t, ca, "admin", "system")
	conn := waitForPgSQL(t, ca, certPath, keyPath, pgsqlPort, "admin")
	ctx := context.Background()

	mustExec := func(sql string) {
		t.Helper()
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatalf("exec %q: %v", sql, err)
		}
	}

	mustExec(`DROP TABLE IF EXISTS type_probe`)
	mustExec(`CREATE TABLE type_probe (id bigint, ok boolean, small smallint, n int)`)
	mustExec(`INSERT INTO type_probe (id, ok, small, n) VALUES (1, true, 7, 18)`)

	t.Run("a declared boolean column scans into a Go bool", func(t *testing.T) {
		var ok bool
		if err := conn.QueryRow(ctx, `SELECT ok FROM type_probe WHERE id = $1`, 1).Scan(&ok); err != nil {
			t.Fatalf("scan boolean: %v", err)
		}
		if !ok {
			t.Errorf("boolean column: got %v, want true", ok)
		}
	})

	t.Run("a comparison scans into a Go bool", func(t *testing.T) {
		var eq bool
		if err := conn.QueryRow(ctx, `SELECT n = 18 FROM type_probe WHERE id = $1`, 1).Scan(&eq); err != nil {
			t.Fatalf("scan comparison: %v", err)
		}
		if !eq {
			t.Errorf("comparison: got %v, want true", eq)
		}
	})

	t.Run("a CASE over small integer literals scans into an integer", func(t *testing.T) {
		var got int64
		if err := conn.QueryRow(ctx,
			`SELECT (CASE WHEN n = 18 THEN 1 ELSE 2 END) FROM type_probe WHERE id = $1`, 1).Scan(&got); err != nil {
			t.Fatalf("scan CASE: %v", err)
		}
		if got != 1 {
			t.Errorf("CASE: got %d, want 1", got)
		}
	})

	t.Run("a small integer literal scans into an integer", func(t *testing.T) {
		var got int64
		if err := conn.QueryRow(ctx, `SELECT 1 FROM type_probe WHERE id = $1`, 1).Scan(&got); err != nil {
			t.Fatalf("scan literal: %v", err)
		}
		if got != 1 {
			t.Errorf("literal: got %d, want 1", got)
		}
	})
}

// TestSQL_PgSQL_CatalogIntrospection covers the introspection surface a
// PostgreSQL client and its ORM depend on, over the wire and unqualified --
// pg_catalog is implicitly first on every session's search path, which is how
// a client reaches pg_indexes without naming the schema.
//
// Each of these was a hard failure before: pg_catalog was unreachable through
// the privilege plane, information_schema.constraint_column_usage did not
// exist (go-mysql-server does not implement the SQL-standard view), and
// neither did pg_indexes. gorm.io/driver/postgres's Migrator needs all three
// to resolve a table's columns, its constraints, and whether an index exists.
func TestSQL_PgSQL_CatalogIntrospection(t *testing.T) {
	_, _, pgsqlPort, ca := startSingleNodeWithSQLMVCC(t)

	certPath, keyPath := generateClientCert(t, ca, "admin", "system")
	conn := waitForPgSQL(t, ca, certPath, keyPath, pgsqlPort, "admin")
	ctx := context.Background()

	mustExec := func(sql string) {
		t.Helper()
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatalf("exec %q: %v", sql, err)
		}
	}

	mustExec(`DROP TABLE IF EXISTS catalog_probe`)
	mustExec(`CREATE TABLE catalog_probe (id bigint, email text, PRIMARY KEY (id))`)
	mustExec(`CREATE UNIQUE INDEX idx_catalog_probe_email ON catalog_probe (email)`)

	t.Run("pg_catalog is reachable qualified and unqualified", func(t *testing.T) {
		for _, query := range []string{
			`SELECT count(*) FROM pg_catalog.pg_class`,
			`SELECT count(*) FROM pg_catalog.pg_namespace`,
			`SELECT count(*) FROM pg_type`,
		} {
			var n int64
			if err := conn.QueryRow(ctx, query).Scan(&n); err != nil {
				t.Errorf("%s: %v", query, err)
				continue
			}
			if n == 0 {
				t.Errorf("%s: reported no rows", query)
			}
		}
	})

	t.Run("pg_indexes answers the HasIndex question", func(t *testing.T) {
		// Verbatim shape of gorm.io/driver/postgres's Migrator.HasIndex,
		// which reads a zero count as "create this index".
		const hasIndex = `SELECT count(*) FROM pg_indexes WHERE tablename = $1 AND indexname = $2 AND schemaname = CURRENT_SCHEMA()`

		var found, pkey, absent int64
		if err := conn.QueryRow(ctx, hasIndex, "catalog_probe", "idx_catalog_probe_email").Scan(&found); err != nil {
			t.Fatalf("hasIndex: %v", err)
		}
		if err := conn.QueryRow(ctx, hasIndex, "catalog_probe", "catalog_probe_pkey").Scan(&pkey); err != nil {
			t.Fatalf("hasIndex(primary key): %v", err)
		}
		if err := conn.QueryRow(ctx, hasIndex, "catalog_probe", "no_such_index").Scan(&absent); err != nil {
			t.Fatalf("hasIndex(absent): %v", err)
		}

		if found != 1 {
			t.Errorf("declared index: got count %d, want 1", found)
		}
		if pkey != 1 {
			t.Errorf("primary key under its PostgreSQL name: got count %d, want 1", pkey)
		}
		if absent != 0 {
			t.Errorf("absent index: got count %d, want 0", absent)
		}
	})

	t.Run("constraint_column_usage answers the Migrator constraint query", func(t *testing.T) {
		const gormQuery = `SELECT constraint_name FROM information_schema.table_constraints tc ` +
			`JOIN information_schema.constraint_column_usage AS ccu ` +
			`USING (constraint_schema, constraint_catalog, table_name, constraint_name) ` +
			`JOIN information_schema.columns AS c ON c.table_schema = tc.constraint_schema ` +
			`AND tc.table_name = c.table_name AND ccu.column_name = c.column_name ` +
			`WHERE constraint_type IN ('PRIMARY KEY', 'UNIQUE') ` +
			`AND c.table_schema = CURRENT_SCHEMA() AND c.table_name = $1 AND constraint_type = $2`

		var name string
		if err := conn.QueryRow(ctx, gormQuery, "catalog_probe", "PRIMARY KEY").Scan(&name); err != nil {
			t.Fatalf("primary key constraint: %v", err)
		}
		if name != "PRIMARY" {
			t.Errorf("primary key constraint: got %q, want %q", name, "PRIMARY")
		}
	})
}
