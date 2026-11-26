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
	"encoding/json"
	"strings"
	"testing"
)

// TestSQLiteVFS_CLI_StandalonePerProtocol drives `qrdb sqlite` against a
// standalone node (no Raft) over every CLI transport.
func TestSQLiteVFS_CLI_StandalonePerProtocol(t *testing.T) {
	t.Parallel()
	node := startStandaloneModeNode(t, "pebble")
	for _, proto := range multiProtocols {
		t.Run(proto.name, func(t *testing.T) {
			testSQLiteVFSOverProtocol(t, node, proto)
		})
	}
}

// TestSQLiteVFS_CLI_RaftPerProtocol drives `qrdb sqlite` against a Raft node
// over every CLI transport.
func TestSQLiteVFS_CLI_RaftPerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)
	for _, proto := range multiProtocols {
		t.Run(proto.name, func(t *testing.T) {
			testSQLiteVFSOverProtocol(t, node, proto)
		})
	}
}

// sqliteRefusingTransports have no binary-safe transaction and range wire;
// the CLI refuses SQLite over them before it writes anything.
var sqliteRefusingTransports = map[string]bool{"rest": true, "mcp": true}

// testSQLiteVFSOverProtocol creates a database, writes rows (one spanning
// many pages), reads them back, then updates and reads again. Every CLI
// invocation opens and closes its own connection, so each read after a
// write is a reopen of the stored database.
func testSQLiteVFSOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()
	db := "e2e-" + proto.name

	if sqliteRefusingTransports[proto.name] {
		for _, args := range [][]string{
			{"sqlite", "exec", db, "CREATE TABLE t (id INTEGER PRIMARY KEY)"},
			{"sqlite", "query", db, "SELECT 1"},
		} {
			out, err := runCLIProto(t, node, proto, args...)
			if err == nil || !strings.Contains(out, "the "+proto.name+" transport") {
				t.Fatalf("[%s] qrdb %s: err = %v (%s), want a refusal naming %s", proto.name, strings.Join(args, " "), err, out, proto.name)
			}
		}
		return
	}

	sqliteExec(t, node, proto, db, "CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT, data BLOB)")
	out := sqliteExec(t, node, proto, db, "INSERT INTO t (name) VALUES ('alice'), ('bob')")
	if !strings.Contains(out, "2 rows affected") {
		t.Fatalf("[%s] insert output = %q, want 2 rows affected", proto.name, out)
	}
	// A 256 KiB blob spans many 4 KiB pages.
	sqliteExec(t, node, proto, db, "INSERT INTO t (name, data) VALUES ('big', zeroblob(262144))")
	if got := sqliteNames(t, node, proto, db); got != "alice,bob,big" {
		t.Fatalf("[%s] names after insert = %s, want alice,bob,big", proto.name, got)
	}
	rows := sqliteQueryJSON(t, node, proto, db, "SELECT length(data) AS n FROM t WHERE name = 'big'")
	if len(rows) != 1 || rows[0]["n"] != float64(262144) {
		t.Fatalf("[%s] blob length rows = %v, want n=262144", proto.name, rows)
	}

	out, err := runCLIProto(t, node, proto, "sqlite", "exec", db, "UPDATE t SET name = 'carol' WHERE id = 2", "--concurrency", "optimistic")
	if err != nil {
		t.Fatalf("[%s] optimistic update: %v\n%s", proto.name, err, out)
	}
	sqliteExec(t, node, proto, db, "DELETE FROM t WHERE name = 'big'")
	if got := sqliteNames(t, node, proto, db); got != "alice,carol" {
		t.Fatalf("[%s] names after update+delete = %s, want alice,carol", proto.name, got)
	}

	// A bad statement fails and leaves the database as it was.
	if out, err := runCLIProto(t, node, proto, "sqlite", "exec", db, "INSERT INTO missing VALUES (1)"); err == nil {
		t.Fatalf("[%s] exec on a missing table succeeded: %s", proto.name, out)
	}
	if got := sqliteNames(t, node, proto, db); got != "alice,carol" {
		t.Fatalf("[%s] names after failed exec = %s, want alice,carol", proto.name, got)
	}
}

func sqliteExec(t *testing.T, node *multiProtoNode, proto multiProtocol, db, stmt string) string {
	t.Helper()
	out, err := runCLIProto(t, node, proto, "sqlite", "exec", db, stmt)
	if err != nil {
		t.Fatalf("[%s] sqlite exec %q: %v\n%s", proto.name, stmt, err, out)
	}
	return out
}

func sqliteQueryJSON(t *testing.T, node *multiProtoNode, proto multiProtocol, db, query string) []map[string]any {
	t.Helper()
	out, err := runCLIProto(t, node, proto, "--output", "json", "sqlite", "query", db, query)
	if err != nil {
		t.Fatalf("[%s] sqlite query %q: %v\n%s", proto.name, query, err, out)
	}
	start := strings.Index(out, "[")
	if start < 0 {
		t.Fatalf("[%s] sqlite query output carries no JSON array: %s", proto.name, out)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out[start:]), &rows); err != nil {
		t.Fatalf("[%s] decode sqlite query output %q: %v", proto.name, out, err)
	}
	return rows
}

func sqliteNames(t *testing.T, node *multiProtoNode, proto multiProtocol, db string) string {
	t.Helper()
	rows := sqliteQueryJSON(t, node, proto, db, "SELECT name FROM t ORDER BY id")
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		s, _ := r["name"].(string)
		names = append(names, s)
	}
	return strings.Join(names, ",")
}
