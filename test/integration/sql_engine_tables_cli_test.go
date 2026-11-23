//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	gomysql "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// sqlEngineTables names every built-in engine table with the schema columns
// it serves (OD-12 B, docs/databases/architecture.md). The daemon the
// integration fixtures build carries qrdb_ts and qrdb_vector
// (qrdbdharness.DefaultBuildTags), so all four are present on every node
// these tests start.
var sqlEngineTables = []struct {
	name    string
	columns []string
}{
	{"qrdb_ts_datapoints", []string{"metric", "timestamp", "value", "tags"}},
	{"qrdb_ts_metrics", []string{"name", "tag_count", "point_count"}},
	{"qrdb_vector_namespaces", []string{"name", "dimensions", "distance", "index_type"}},
	{"qrdb_vector_vectors", []string{"id", "embedding", "metadata"}},
}

// TestSQLEngineTables_CLI_PerProtocol drives `qrdb sql query` on every CLI
// transport against the four reserved engine tables: each answers zero rows
// with its schema columns, SHOW TABLES lists them beside kv, and the old
// unprefixed names are no longer tables at all.
func TestSQLEngineTables_CLI_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			for _, tbl := range sqlEngineTables {
				out, err := runCLIProto(t, node, proto, "sql", "query", "--output", "json",
					"SELECT * FROM "+tbl.name)
				if err != nil {
					t.Fatalf("[%s] SELECT * FROM %s: %v\noutput: %s", proto.name, tbl.name, err, out)
				}
				var res sqlCLIQueryResult
				if jerr := json.Unmarshal([]byte(out), &res); jerr != nil {
					t.Fatalf("[%s] decode SELECT * FROM %s JSON: %v\noutput: %s", proto.name, tbl.name, jerr, out)
				}
				if res.RowCount != 0 {
					t.Fatalf("[%s] SELECT * FROM %s rowCount = %d, want 0: %s", proto.name, tbl.name, res.RowCount, out)
				}
				if !slices.Equal(res.Columns, tbl.columns) {
					t.Fatalf("[%s] SELECT * FROM %s columns = %v, want %v", proto.name, tbl.name, res.Columns, tbl.columns)
				}
			}

			out, err := runCLIProto(t, node, proto, "sql", "query", "--output", "json", "SHOW TABLES")
			if err != nil {
				t.Fatalf("[%s] SHOW TABLES: %v\noutput: %s", proto.name, err, out)
			}
			var show sqlCLIQueryResult
			if jerr := json.Unmarshal([]byte(out), &show); jerr != nil {
				t.Fatalf("[%s] decode SHOW TABLES JSON: %v\noutput: %s", proto.name, jerr, out)
			}
			listed := map[string]bool{}
			for _, row := range show.Rows {
				for _, v := range row {
					listed[fmt.Sprint(v)] = true
				}
			}
			for _, want := range []string{"kv", "qrdb_ts_datapoints", "qrdb_ts_metrics", "qrdb_vector_namespaces", "qrdb_vector_vectors"} {
				if !listed[want] {
					t.Fatalf("[%s] SHOW TABLES = %v, want it to list %s", proto.name, show.Rows, want)
				}
			}

			for _, old := range []string{"metrics", "vectors"} {
				out, err := runCLIProto(t, node, proto, "sql", "query", "SELECT * FROM "+old)
				if err == nil {
					t.Fatalf("[%s] SELECT * FROM %s succeeded, want table not found (output: %s)", proto.name, old, out)
				}
				if !strings.Contains(out, "table not found") {
					t.Fatalf("[%s] SELECT * FROM %s = %q, want it to contain %q", proto.name, old, out, "table not found")
				}
			}
		})
	}
}

// TestSQLEngineTables_UserTablesNamedLikeEngineTables proves over the MySQL
// and PostgreSQL wires that the old engine table names are ordinary user
// table names now, and that the reserved qrdb_ names are refused for CREATE
// TABLE and RENAME TABLE in any letter case while the engine tables keep
// answering. Both wires share the node's default database, so each wire uses
// its own pair of user table names.
func TestSQLEngineTables_UserTablesNamedLikeEngineTables(t *testing.T) {
	_, mysqlPort, pgsqlPort, ca := startSingleNodeWithSQL(t)
	certPath, keyPath := ca.boundAdminCert(t, t.TempDir())

	assertReserved := func(t *testing.T, wire, stmt, name string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("[%s] %s succeeded, want the reserved table name refusal", wire, stmt)
		}
		msg := strings.ToLower(err.Error())
		if !strings.Contains(msg, "reserved") || !strings.Contains(msg, name) {
			t.Fatalf("[%s] %s error = %q, want it to name the reserved table %s", wire, stmt, err.Error(), name)
		}
		// Record the code each wire sends for the refusal today; neither
		// wire's error table maps *provider.ReservedTableNameError yet.
		var pgErr *pgconn.PgError
		var myErr *gomysql.MySQLError
		switch {
		case errors.As(err, &pgErr):
			t.Logf("[%s] %s refused with SQLSTATE %s", wire, stmt, pgErr.Code)
		case errors.As(err, &myErr):
			t.Logf("[%s] %s refused with MySQL error %d (SQLSTATE %s)", wire, stmt, myErr.Number, string(myErr.SQLState[:]))
		default:
			t.Logf("[%s] %s refused with %T", wire, stmt, err)
		}
	}

	wantListed := []string{"metrics", "vectors", "datapoints", "namespaces",
		"qrdb_ts_datapoints", "qrdb_ts_metrics", "qrdb_vector_namespaces", "qrdb_vector_vectors"}
	assertListed := func(t *testing.T, wire string, listed map[string]bool) {
		t.Helper()
		for _, want := range wantListed {
			if !listed[want] {
				t.Fatalf("[%s] SHOW TABLES = %v, want it to list %s", wire, listed, want)
			}
		}
	}

	t.Run("mysql", func(t *testing.T) {
		db := waitForMySQL(t, ca, certPath, keyPath, mysqlPort, "admin")
		for _, name := range []string{"metrics", "vectors"} {
			if _, err := db.Exec("CREATE TABLE " + name + " (id BIGINT PRIMARY KEY, label VARCHAR(64))"); err != nil {
				t.Fatalf("[mysql] CREATE TABLE %s: %v", name, err)
			}
			if _, err := db.Exec("INSERT INTO "+name+" (id, label) VALUES (?, ?)", 1, "mysql-"+name); err != nil {
				t.Fatalf("[mysql] INSERT INTO %s: %v", name, err)
			}
			var label string
			if err := db.QueryRow("SELECT label FROM "+name+" WHERE id = ?", 1).Scan(&label); err != nil {
				t.Fatalf("[mysql] SELECT FROM %s: %v", name, err)
			}
			if label != "mysql-"+name {
				t.Fatalf("[mysql] SELECT FROM %s label = %q, want %q", name, label, "mysql-"+name)
			}
		}

		_, err := db.Exec("CREATE TABLE qrdb_ts_metrics (id BIGINT PRIMARY KEY)")
		assertReserved(t, "mysql", "CREATE TABLE qrdb_ts_metrics", "qrdb_ts_metrics", err)
		_, err = db.Exec("CREATE TABLE QRDB_Vector_Vectors (id BIGINT PRIMARY KEY)")
		assertReserved(t, "mysql", "CREATE TABLE QRDB_Vector_Vectors", "qrdb_vector_vectors", err)
		_, err = db.Exec("RENAME TABLE metrics TO qrdb_ts_datapoints")
		assertReserved(t, "mysql", "RENAME TABLE metrics TO qrdb_ts_datapoints", "qrdb_ts_datapoints", err)

		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM qrdb_ts_metrics").Scan(&count); err != nil {
			t.Fatalf("[mysql] SELECT COUNT(*) FROM qrdb_ts_metrics: %v", err)
		}
		if count != 0 {
			t.Fatalf("[mysql] qrdb_ts_metrics count = %d, want 0", count)
		}
	})

	t.Run("pgsql", func(t *testing.T) {
		conn := waitForPgSQL(t, ca, certPath, keyPath, pgsqlPort, "admin")
		ctx := context.Background()
		for _, name := range []string{"datapoints", "namespaces"} {
			if _, err := conn.Exec(ctx, "CREATE TABLE "+name+" (id BIGINT PRIMARY KEY, label VARCHAR(64))"); err != nil {
				t.Fatalf("[pgsql] CREATE TABLE %s: %v", name, err)
			}
			if _, err := conn.Exec(ctx, "INSERT INTO "+name+" (id, label) VALUES ($1, $2)", 1, "pgsql-"+name); err != nil {
				t.Fatalf("[pgsql] INSERT INTO %s: %v", name, err)
			}
			var label string
			if err := conn.QueryRow(ctx, "SELECT label FROM "+name+" WHERE id = $1", 1).Scan(&label); err != nil {
				t.Fatalf("[pgsql] SELECT FROM %s: %v", name, err)
			}
			if label != "pgsql-"+name {
				t.Fatalf("[pgsql] SELECT FROM %s label = %q, want %q", name, label, "pgsql-"+name)
			}
		}

		_, err := conn.Exec(ctx, "CREATE TABLE qrdb_vector_namespaces (id BIGINT PRIMARY KEY)")
		assertReserved(t, "pgsql", "CREATE TABLE qrdb_vector_namespaces", "qrdb_vector_namespaces", err)
		_, err = conn.Exec(ctx, "CREATE TABLE QRDB_Vector_Vectors (id BIGINT PRIMARY KEY)")
		assertReserved(t, "pgsql", "CREATE TABLE QRDB_Vector_Vectors", "qrdb_vector_vectors", err)
		_, err = conn.Exec(ctx, "ALTER TABLE datapoints RENAME TO qrdb_ts_datapoints")
		assertReserved(t, "pgsql", "ALTER TABLE datapoints RENAME TO qrdb_ts_datapoints", "qrdb_ts_datapoints", err)

		var count int64
		if err := conn.QueryRow(ctx, "SELECT COUNT(*) FROM qrdb_ts_metrics").Scan(&count); err != nil {
			t.Fatalf("[pgsql] SELECT COUNT(*) FROM qrdb_ts_metrics: %v", err)
		}
		if count != 0 {
			t.Fatalf("[pgsql] qrdb_ts_metrics count = %d, want 0", count)
		}
	})

	// Both wires share the node's default database, so SHOW TABLES over
	// either wire lists every user table created above beside the engine
	// tables. Both wires are read: MySQL, then PostgreSQL with pgx's
	// default (extended) protocol and with the simple query protocol.
	t.Run("show tables mysql", func(t *testing.T) {
		db := waitForMySQL(t, ca, certPath, keyPath, mysqlPort, "admin")
		rows, err := db.Query("SHOW TABLES")
		if err != nil {
			t.Fatalf("[mysql] SHOW TABLES: %v", err)
		}
		defer rows.Close()
		listed := map[string]bool{}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatalf("[mysql] SHOW TABLES scan: %v", err)
			}
			listed[name] = true
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("[mysql] SHOW TABLES rows: %v", err)
		}
		assertListed(t, "mysql", listed)
	})

	for _, mode := range []struct {
		name string
		exec pgx.QueryExecMode
	}{
		{"show tables pgsql extended", pgx.QueryExecModeCacheStatement},
		{"show tables pgsql simple", pgx.QueryExecModeSimpleProtocol},
	} {
		t.Run(mode.name, func(t *testing.T) {
			conn := waitForPgSQL(t, ca, certPath, keyPath, pgsqlPort, "admin")
			rows, err := conn.Query(context.Background(), "SHOW TABLES", mode.exec)
			if err != nil {
				t.Fatalf("[%s] SHOW TABLES: %v", mode.name, err)
			}
			defer rows.Close()
			listed := map[string]bool{}
			for rows.Next() {
				var name string
				if err := rows.Scan(&name); err != nil {
					t.Fatalf("[%s] SHOW TABLES scan: %v", mode.name, err)
				}
				listed[name] = true
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("[%s] SHOW TABLES rows: %v", mode.name, err)
			}
			assertListed(t, mode.name, listed)
		})
	}
}
