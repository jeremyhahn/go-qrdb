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

// sql_query_cli_test.go exercises the `qrdb sql query` CLI surface
// (cmd/qrdb/sql.go) end-to-end against a real, freshly-spawned qrdbd node:
// data seeded via `qrdb kv put` is read back through `qrdb sql query`, in
// both the positional-argument and --file forms, and in both table and
// JSON output.
package integration_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestCLI_SqlQuery_KVRoundTrip proves the SQL-over-KV round trip: a
// dedicated database is created, a key/value pair is written via
// `qrdb kv put --database`, and `qrdb sql query --database "SELECT * FROM
// kv"` reads it back, both as a positional argument and via --file.
func TestCLI_SqlQuery_KVRoundTrip(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)

	node := bootSelfGenCANode(t, bin, "seal:\n  enabled: true\n  strategy: none\n", "[sql-query] ")
	pollUntilSealedListenerUp(t, cli, node.restArgsNoAuth(), 30*time.Second)

	node.bootAdmin(t, cli, "SOPIN12345", "USERPIN12345", "--barrier-strategy", "none")
	waitForHealthy(t, node.dataDir, 60*time.Second)
	pollUntilCLISucceeds(t, cli, node.restArgsAdmin(), 60*time.Second)

	if out, err := runQRDB(t, cli, node.restArgsAdmin(), "database", "create", "sqltestdb",
		"--backend", "memory"); err != nil {
		t.Fatalf("database create: %v\noutput: %s", err, out)
	}

	if out, err := runQRDB(t, cli, node.restArgsAdmin(),
		"--database", "sqltestdb", "kv", "put", "greeting", "hello world"); err != nil {
		t.Fatalf("kv put: %v\noutput: %s", err, out)
	}

	// Positional-argument form, JSON output.
	out, err := runQRDB(t, cli, node.restArgsAdmin(), "sql", "query",
		"--database", "sqltestdb", "--output", "json", "SELECT * FROM kv")
	if err != nil {
		t.Fatalf("sql query: %v\noutput: %s", err, out)
	}
	var result struct {
		Columns  []string         `json:"columns"`
		Rows     []map[string]any `json:"rows"`
		RowCount int              `json:"rowCount"`
	}
	decodeJSON(t, out, &result)
	if result.RowCount != 1 {
		t.Fatalf("expected 1 row, got %d: %s", result.RowCount, out)
	}
	if fmt.Sprint(result.Rows[0]["key"]) != "greeting" {
		t.Errorf("expected row key 'greeting', got %v: %s", result.Rows[0]["key"], out)
	}
	if fmt.Sprint(result.Rows[0]["value"]) != "hello world" {
		t.Errorf("expected row value 'hello world', got %v: %s", result.Rows[0]["value"], out)
	}

	// --file form, table output.
	queryFile := writeTempFile(t, "query.sql", []byte("SELECT * FROM kv\n"))
	fileOut, err := runQRDB(t, cli, node.restArgsAdmin(), "sql", "query",
		"--database", "sqltestdb", "--file", queryFile)
	if err != nil {
		t.Fatalf("sql query --file: %v\noutput: %s", err, fileOut)
	}
	if !strings.Contains(fileOut, "greeting") || !strings.Contains(fileOut, "hello world") {
		t.Errorf("expected the seeded row in table output, got: %s", fileOut)
	}
	if !strings.Contains(fileOut, "1 rows") {
		t.Errorf("expected a row-count summary line, got: %s", fileOut)
	}
}

// TestCLI_SqlQuery_EmptyDatabase verifies an empty SELECT against a
// freshly created, unseeded database returns zero rows rather than an
// error.
func TestCLI_SqlQuery_EmptyDatabase(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)

	node := bootSelfGenCANode(t, bin, "seal:\n  enabled: true\n  strategy: none\n", "[sql-query-empty] ")
	pollUntilSealedListenerUp(t, cli, node.restArgsNoAuth(), 30*time.Second)

	node.bootAdmin(t, cli, "SOPIN12345", "USERPIN12345", "--barrier-strategy", "none")
	waitForHealthy(t, node.dataDir, 60*time.Second)
	pollUntilCLISucceeds(t, cli, node.restArgsAdmin(), 60*time.Second)

	if out, err := runQRDB(t, cli, node.restArgsAdmin(), "database", "create", "emptysqldb",
		"--backend", "memory"); err != nil {
		t.Fatalf("database create: %v\noutput: %s", err, out)
	}

	out, err := runQRDB(t, cli, node.restArgsAdmin(), "sql", "query",
		"--database", "emptysqldb", "--output", "json", "SELECT * FROM kv")
	if err != nil {
		t.Fatalf("sql query: %v\noutput: %s", err, out)
	}
	var result struct {
		RowCount int `json:"rowCount"`
	}
	decodeJSON(t, out, &result)
	if result.RowCount != 0 {
		t.Errorf("expected 0 rows for an unseeded database, got %d: %s", result.RowCount, out)
	}
}

// TestCLI_SqlQuery_MissingQueryFails verifies the local flag-validation
// error (neither a positional SQL argument nor --file supplied) fails
// before any network call.
func TestCLI_SqlQuery_MissingQueryFails(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)

	node := bootSelfGenCANode(t, bin, "seal:\n  enabled: true\n  strategy: none\n", "[sql-query-missing] ")
	pollUntilSealedListenerUp(t, cli, node.restArgsNoAuth(), 30*time.Second)

	node.bootAdmin(t, cli, "SOPIN12345", "USERPIN12345", "--barrier-strategy", "none")
	waitForHealthy(t, node.dataDir, 60*time.Second)
	pollUntilCLISucceeds(t, cli, node.restArgsAdmin(), 60*time.Second)

	out, err := runQRDB(t, cli, node.restArgsAdmin(), "sql", "query", "--database", "sqltestdb")
	if err == nil {
		t.Fatalf("expected an error when neither a positional query nor --file is supplied; output: %s", out)
	}
	if !strings.Contains(out, "missing required argument") {
		t.Errorf("expected the typed MissingArgError message, got: %s", out)
	}
}

// ---------------------------------------------------------------------------
// Multi-protocol coverage
// ---------------------------------------------------------------------------
//
// TestSQLQuery_CLI_PerProtocol closes the "SQL is REST-only" gap: it drives
// `qrdb sql query` through the real client CLI binary against every
// transport the daemon serves (REST, gRPC, QUIC, MCP, Unix) over one shared
// startMultiProtocolNode fixture, mirroring the convention
// TestKV_CLI_PerProtocol (kv_cli_test.go) established for `qrdb kv`.
//
// The ad-hoc SQL query surface (the SQLQuery RPC / POST /sql/query route
// every transport exposes) is READ-ONLY BY CONSTRUCTION, not by omission:
// pkg/server/server.go's newSQLService wires sqlquery.WithReadOnly() into
// the single service.SQLServicer instance shared by every transport (see
// that function's doc comment), and pkg/sql/provider/readonly_table.go
// enforces it structurally (a *readOnlyTable's static type never satisfies
// sql.InsertableTable/UpdatableTable/DeletableTable, regardless of what the
// wrapped concrete table implements). The full DML/DDL surface is served
// separately, over the MySQL/PostgreSQL wire protocol listeners (see
// sql_test.go), which authenticate a dedicated per-connection session. So
// this test proves two properties identically across all five transports:
//
//  1. SELECT -- including WHERE, ORDER BY, and LIMIT -- returns correct,
//     byte-identical results.
//  2. INSERT/UPDATE/DELETE/CREATE TABLE are all rejected with the engine's
//     typed read-only/unsupported error, and no row is ever created despite
//     the rejected INSERT -- proving the rejection isn't cosmetic.
//
// It also exercises the flag surface: --output json/table, --file, and
// --database (both the implicit default database and an explicitly created
// named one), plus the not-found error for an unknown --database.
func TestSQLQuery_CLI_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testSQLQueryOverProtocol(t, node, proto)
		})
	}
}

// sqlCLIQueryResult mirrors cmd/qrdb/sql.go's renderSQLResult JSON shape
// (transport.SQLQueryResult), decoded from `sql query --output json`.
type sqlCLIQueryResult struct {
	Columns  []string         `json:"columns"`
	Rows     []map[string]any `json:"rows"`
	RowCount int              `json:"rowCount"`
	Duration string           `json:"duration"`
}

// testSQLQueryOverProtocol exercises the full `sql query` surface -- SELECT
// (WHERE/ORDER BY/LIMIT), --output json/table, --file, --database (default
// and an explicitly created named database), the not-found error for an
// unknown database, and the read-only rejection of INSERT/UPDATE/DELETE/
// CREATE TABLE -- against a single protocol.
func testSQLQueryOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	// --- seed the default database ("system") ---------------------------
	//
	// `sql query` with no --database resolves the same default database
	// unscoped `kv put` writes to (transport.DefaultDatabaseScope), exactly
	// as TestKV_CLI_PerProtocol relies on for `kv`. Keys are namespaced by
	// protocol so the five protocol subtests, which share one node and
	// therefore one "system" database keyspace, never contend.
	prefix := fmt.Sprintf("sqlcli/%s/", proto.name)
	keyAlpha := prefix + "alpha"
	keyBravo := prefix + "bravo"
	keyCharlie := prefix + "charlie"

	for _, kv := range [][2]string{{keyAlpha, "1"}, {keyBravo, "2"}, {keyCharlie, "3"}} {
		out, err := runCLIProto(t, node, proto, "kv", "put", kv[0], kv[1])
		if err != nil {
			t.Fatalf("[%s] kv put %s: %v\noutput: %s", proto.name, kv[0], err, out)
		}
	}

	// --- SELECT ... WHERE `key` = <exact> --------------------------------
	out, err := runCLIProto(t, node, proto, "sql", "query", "--output", "json",
		fmt.Sprintf("SELECT * FROM kv WHERE `key` = '%s'", keyAlpha))
	if err != nil {
		t.Fatalf("[%s] sql query WHERE =: %v\noutput: %s", proto.name, err, out)
	}
	var whereResult sqlCLIQueryResult
	if jerr := json.Unmarshal([]byte(out), &whereResult); jerr != nil {
		t.Fatalf("[%s] decode `sql query` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if whereResult.RowCount != 1 {
		t.Fatalf("[%s] sql query WHERE = rowCount = %d, want 1: %s", proto.name, whereResult.RowCount, out)
	}
	if fmt.Sprint(whereResult.Rows[0]["key"]) != keyAlpha || fmt.Sprint(whereResult.Rows[0]["value"]) != "1" {
		t.Fatalf("[%s] sql query WHERE = row = %+v, want key=%s value=1", proto.name, whereResult.Rows[0], keyAlpha)
	}

	// --- SELECT ... WHERE `key` LIKE <prefix> ORDER BY DESC LIMIT --------
	//
	// Values are chosen so lexicographic key order is unambiguous:
	// charlie > bravo > alpha.
	out, err = runCLIProto(t, node, proto, "sql", "query", "--output", "json",
		fmt.Sprintf("SELECT * FROM kv WHERE `key` LIKE '%s%%' ORDER BY `key` DESC LIMIT 1", prefix))
	if err != nil {
		t.Fatalf("[%s] sql query ORDER BY DESC LIMIT 1: %v\noutput: %s", proto.name, err, out)
	}
	var descResult sqlCLIQueryResult
	if jerr := json.Unmarshal([]byte(out), &descResult); jerr != nil {
		t.Fatalf("[%s] decode `sql query` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if descResult.RowCount != 1 || fmt.Sprint(descResult.Rows[0]["key"]) != keyCharlie {
		t.Fatalf("[%s] sql query ORDER BY DESC LIMIT 1 = %+v, want exactly %s", proto.name, descResult.Rows, keyCharlie)
	}

	out, err = runCLIProto(t, node, proto, "sql", "query", "--output", "json",
		fmt.Sprintf("SELECT * FROM kv WHERE `key` LIKE '%s%%' ORDER BY `key` ASC LIMIT 2", prefix))
	if err != nil {
		t.Fatalf("[%s] sql query ORDER BY ASC LIMIT 2: %v\noutput: %s", proto.name, err, out)
	}
	var ascResult sqlCLIQueryResult
	if jerr := json.Unmarshal([]byte(out), &ascResult); jerr != nil {
		t.Fatalf("[%s] decode `sql query` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if ascResult.RowCount != 2 ||
		fmt.Sprint(ascResult.Rows[0]["key"]) != keyAlpha ||
		fmt.Sprint(ascResult.Rows[1]["key"]) != keyBravo {
		t.Fatalf("[%s] sql query ORDER BY ASC LIMIT 2 = %+v, want [%s, %s]", proto.name, ascResult.Rows, keyAlpha, keyBravo)
	}

	// --- default --output table -------------------------------------------
	out, err = runCLIProto(t, node, proto, "sql", "query",
		fmt.Sprintf("SELECT * FROM kv WHERE `key` LIKE '%s%%' ORDER BY `key` ASC", prefix))
	if err != nil {
		t.Fatalf("[%s] sql query --output table: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, keyAlpha) || !strings.Contains(out, keyCharlie) {
		t.Fatalf("[%s] sql query --output table = %q, want it to contain %s and %s", proto.name, out, keyAlpha, keyCharlie)
	}
	if !strings.Contains(out, "(3 rows,") {
		t.Fatalf("[%s] sql query --output table = %q, want a '(3 rows, ...)' summary line", proto.name, out)
	}

	// --- --file form -------------------------------------------------------
	queryFile := writeTempFile(t, "sqlcli-"+proto.name+".sql",
		[]byte(fmt.Sprintf("SELECT * FROM kv WHERE `key` = '%s'\n", keyBravo)))
	out, err = runCLIProto(t, node, proto, "sql", "query", "--file", queryFile)
	if err != nil {
		t.Fatalf("[%s] sql query --file: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, keyBravo) || !strings.Contains(out, "2") {
		t.Fatalf("[%s] sql query --file = %q, want it to contain %s and its value", proto.name, out, keyBravo)
	}

	// --- --database targeting an explicitly created named database --------
	dbName := "sqlclidb" + proto.name
	out, err = runCLIProto(t, node, proto, "database", "create", dbName, "--backend", "memory")
	if err != nil {
		t.Fatalf("[%s] database create %s: %v\noutput: %s", proto.name, dbName, err, out)
	}
	if out, err = runCLIProto(t, node, proto, "--database", dbName, "kv", "put", "one", "uno"); err != nil {
		t.Fatalf("[%s] kv put --database %s: %v\noutput: %s", proto.name, dbName, err, out)
	}
	if out, err = runCLIProto(t, node, proto, "--database", dbName, "kv", "put", "two", "dos"); err != nil {
		t.Fatalf("[%s] kv put --database %s: %v\noutput: %s", proto.name, dbName, err, out)
	}
	out, err = runCLIProto(t, node, proto, "sql", "query", "--database", dbName, "--output", "json",
		"SELECT * FROM kv ORDER BY `key` ASC")
	if err != nil {
		t.Fatalf("[%s] sql query --database %s: %v\noutput: %s", proto.name, dbName, err, out)
	}
	var namedDBResult sqlCLIQueryResult
	if jerr := json.Unmarshal([]byte(out), &namedDBResult); jerr != nil {
		t.Fatalf("[%s] decode `sql query --database` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if namedDBResult.RowCount != 2 ||
		fmt.Sprint(namedDBResult.Rows[0]["key"]) != "one" || fmt.Sprint(namedDBResult.Rows[0]["value"]) != "uno" ||
		fmt.Sprint(namedDBResult.Rows[1]["key"]) != "two" || fmt.Sprint(namedDBResult.Rows[1]["value"]) != "dos" {
		t.Fatalf("[%s] sql query --database %s = %+v, want [one=uno, two=dos]", proto.name, dbName, namedDBResult.Rows)
	}

	// --- an unknown --database must fail with the resolver's typed error --
	out, err = runCLIProto(t, node, proto, "sql", "query", "--database", "no-such-db-"+proto.name, "SELECT * FROM kv")
	if err == nil {
		t.Fatalf("[%s] sql query --database no-such-db-%s succeeded, want a not-found error (output: %s)", proto.name, proto.name, out)
	}
	if !strings.Contains(out, "database not found") {
		t.Fatalf("[%s] sql query against an unknown database = %q, want it to contain %q", proto.name, out, "database not found")
	}

	// --- the ad-hoc SQL surface is read-only: DML/DDL must be rejected ---
	//
	// Every rejection is asserted to actually surface the engine's own
	// typed message (not just "any error"), and a follow-up SELECT proves
	// the rejected INSERT never created a row -- the read-only boundary is
	// structural (readOnlyTable), not merely cosmetic.
	writeStmts := []struct {
		name  string
		query string
		want  string
	}{
		{"insert", fmt.Sprintf("INSERT INTO kv (`key`, `value`) VALUES ('%sshould-fail', 'x')", prefix), "table doesn't support INSERT INTO"},
		{"update", fmt.Sprintf("UPDATE kv SET `value` = 'x' WHERE `key` = '%s'", keyAlpha), "table doesn't support UPDATE"},
		{"delete", fmt.Sprintf("DELETE FROM kv WHERE `key` = '%s'", keyAlpha), "table doesn't support DELETE FROM"},
		{"create_table", fmt.Sprintf("CREATE TABLE sqlclifoo%s (id INT)", proto.name), "tables cannot be created on database"},
	}
	for _, ws := range writeStmts {
		out, err = runCLIProto(t, node, proto, "sql", "query", ws.query)
		if err == nil {
			t.Fatalf("[%s] sql query %s succeeded, want the read-only rejection (output: %s)", proto.name, ws.name, out)
		}
		if !strings.Contains(out, ws.want) {
			t.Fatalf("[%s] sql query %s = %q, want it to contain %q", proto.name, ws.name, out, ws.want)
		}
	}

	// The rejected INSERT above must not have created a row.
	out, err = runCLIProto(t, node, proto, "sql", "query", "--output", "json",
		fmt.Sprintf("SELECT * FROM kv WHERE `key` = '%sshould-fail'", prefix))
	if err != nil {
		t.Fatalf("[%s] post-rejection SELECT: %v\noutput: %s", proto.name, err, out)
	}
	var postRejectResult sqlCLIQueryResult
	if jerr := json.Unmarshal([]byte(out), &postRejectResult); jerr != nil {
		t.Fatalf("[%s] decode post-rejection SELECT JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if postRejectResult.RowCount != 0 {
		t.Fatalf("[%s] post-rejection SELECT rowCount = %d, want 0 (the rejected INSERT must not have written a row)", proto.name, postRejectResult.RowCount)
	}

	// alpha/bravo/charlie must be untouched by the rejected UPDATE/DELETE.
	out, err = runCLIProto(t, node, proto, "sql", "query", "--output", "json",
		fmt.Sprintf("SELECT * FROM kv WHERE `key` LIKE '%s%%' ORDER BY `key` ASC", prefix))
	if err != nil {
		t.Fatalf("[%s] post-rejection prefix SELECT: %v\noutput: %s", proto.name, err, out)
	}
	var finalResult sqlCLIQueryResult
	if jerr := json.Unmarshal([]byte(out), &finalResult); jerr != nil {
		t.Fatalf("[%s] decode post-rejection prefix SELECT JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if finalResult.RowCount != 3 || fmt.Sprint(finalResult.Rows[0]["value"]) != "1" {
		t.Fatalf("[%s] post-rejection prefix SELECT = %+v, want the original 3 rows untouched (alpha=1)", proto.name, finalResult.Rows)
	}
}
