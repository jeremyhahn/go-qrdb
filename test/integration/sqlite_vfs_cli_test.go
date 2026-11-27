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
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
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

	testSQLiteStagedCommit(t, node, proto, db+"-staged")
	testSQLiteWriterContention(t, node, proto, db+"-contend")
	testSQLiteRestore(t, node, proto, db+"-restore")
	testSQLiteTenantQuota(t, node, proto, db+"-quota")
}

// sqliteTenantPermissions is what a tenant identity needs to drive `qrdb
// sqlite`: the RPCs the CLI's page backend, writer lock and restore send
// (Transaction, CurrentRevision, GetAtRevision, Range, GetRevision,
// MovePrefix, RestoreToRevision and the lock RPCs), the KV operations its
// page transactions carry, and admin:health to open a gRPC, QUIC or Unix
// connection (see dataPlanePermissions).
var sqliteTenantPermissions = []string{
	"transaction:execute",
	"mvcc:current_revision", "mvcc:get_at_revision", "mvcc:range", "mvcc:get_revision",
	"mvcc:move_prefix", "mvcc:restore",
	"locks:acquire", "locks:release", "locks:extend", "locks:status", "locks:list",
	"kv:put", "kv:get", "kv:delete",
	"admin:health",
}

// sqliteQuotaBytes caps the quota tenant's storage: room for a small table,
// not for the 256 KiB blob the exhaustion step inserts.
const sqliteQuotaBytes = 131072

// testSQLiteTenantQuota drives quota exhaustion as a tenant: an entitled
// tenant capped at sqliteQuotaBytes writes a small table, then a commit past
// the quota exits with SQLITE_FULL (13), and the database keeps its rows and
// passes integrity_check.
func testSQLiteTenantQuota(t *testing.T, node *multiProtoNode, proto multiProtocol, db string) {
	t.Helper()
	tenantID := "sqlq-" + proto.name
	createTenant(t, node, proto, tenantID, tenantID, "prefix", false)
	for _, args := range [][]string{
		{"tenant", "set-entitlement", tenantID, "--isolation-modes", "prefix", "--allow-sqlite-vfs"},
		{"tenant", "update-quotas", tenantID, "--max-sqlite-databases", "1", "--max-storage-bytes", strconv.Itoa(sqliteQuotaBytes)},
	} {
		if out, err := runCLIProto(t, node, proto, args...); err != nil {
			t.Fatalf("[%s] qrdb %s: %v\n%s", proto.name, strings.Join(args, " "), err, out)
		}
	}
	cert, key := provisionTenantIdentity(t, node, tenantID, "sqlite-rw", sqliteTenantPermissions)
	conn := tenantCLIArgsForProto(node, proto, cert, key)
	run := func(args ...string) (string, error) { return runQRDB(t, node.cli, conn, args...) }

	for _, stmt := range []string{
		"CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT, data BLOB)",
		"INSERT INTO t (name) VALUES ('alice'), ('bob')",
	} {
		if out, err := run("sqlite", "exec", db, stmt); err != nil {
			t.Fatalf("[%s] tenant sqlite exec %q: %v\n%s", proto.name, stmt, err, out)
		}
	}

	out, err := run("sqlite", "exec", db, "INSERT INTO t (name, data) VALUES ('big', zeroblob(262144))")
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 13 {
		t.Fatalf("[%s] commit past the tenant quota: err = %v (%s), want exit status 13 (SQLITE_FULL)", proto.name, err, out)
	}

	query := func(stmt string) []map[string]any {
		t.Helper()
		out, err := run("--output", "json", "sqlite", "query", db, stmt)
		if err != nil {
			t.Fatalf("[%s] tenant sqlite query %q: %v\n%s", proto.name, stmt, err, out)
		}
		return sqliteDecodeRows(t, proto, out)
	}
	if rows := query("SELECT name FROM t ORDER BY id"); len(rows) != 2 || rows[0]["name"] != "alice" || rows[1]["name"] != "bob" {
		t.Fatalf("[%s] rows after SQLITE_FULL = %v, want alice,bob", proto.name, rows)
	}
	if rows := query("PRAGMA integrity_check"); len(rows) != 1 || rows[0]["integrity_check"] != "ok" {
		t.Fatalf("[%s] integrity_check after SQLITE_FULL = %v, want ok", proto.name, rows)
	}
}

// sqliteSeries is a SELECT of n rows, each a 4 KiB blob: every row puts
// one page (plus its overflow) in the commit.
func sqliteSeries(n int, column string) string {
	return fmt.Sprintf("SELECT %s FROM (WITH RECURSIVE c(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM c WHERE i < %d) SELECT i FROM c)", column, n)
}

// sqliteExecThreshold runs stmt with --stage-threshold and returns how
// many revisions the exec took: a staged commit writes its chunks as
// revisions of their own before the one publish, a direct commit none.
func sqliteExecThreshold(t *testing.T, node *multiProtoNode, proto multiProtocol, db, stmt string, threshold int) int64 {
	t.Helper()
	before := kvCLIRevision(t, node, proto)
	out, err := runCLIProto(t, node, proto, "sqlite", "exec", db, stmt, "--stage-threshold", strconv.Itoa(threshold))
	if err != nil {
		t.Fatalf("[%s] sqlite exec --stage-threshold %d %q: %v\n%s", proto.name, threshold, stmt, err, out)
	}
	return kvCLIRevision(t, node, proto) - before
}

func sqliteCount(t *testing.T, node *multiProtoNode, proto multiProtocol, db, query string) int64 {
	t.Helper()
	rows := sqliteQueryJSON(t, node, proto, db, query)
	if len(rows) != 1 {
		t.Fatalf("[%s] %q rows = %v, want one", proto.name, query, rows)
	}
	n, ok := rows[0]["n"].(float64)
	if !ok {
		t.Fatalf("[%s] %q row = %v, want a numeric n", proto.name, query, rows[0])
	}
	return int64(n)
}

func sqliteIntegrityOK(t *testing.T, node *multiProtoNode, proto multiProtocol, db string) {
	t.Helper()
	rows := sqliteQueryJSON(t, node, proto, db, "PRAGMA integrity_check")
	if len(rows) != 1 || rows[0]["integrity_check"] != "ok" {
		t.Fatalf("[%s] integrity_check of %s = %v, want ok", proto.name, db, rows)
	}
}

// testSQLiteStagedCommit drives staged commits through --stage-threshold
// (docs/decisions/sqlite-vfs-build.md): a 2 MiB insert stages only when
// the flag lowers the threshold below it, then a bulk insert and a VACUUM
// above the 4 MiB default publish whole.
func testSQLiteStagedCommit(t *testing.T, node *multiProtoNode, proto multiProtocol, db string) {
	t.Helper()
	const mib = 1 << 20
	sqliteExec(t, node, proto, db, "CREATE TABLE s (id INTEGER PRIMARY KEY, data BLOB)")
	insert := func(n int) string { return "INSERT INTO s (data) " + sqliteSeries(n, "zeroblob(4096)") }
	direct := sqliteExecThreshold(t, node, proto, db, insert(512), 0)
	staged := sqliteExecThreshold(t, node, proto, db, insert(512), mib)
	if staged <= direct {
		t.Fatalf("[%s] a 2 MiB insert at --stage-threshold %d took %d revisions, the default-threshold one %d: want the staged commit's chunks on top", proto.name, mib, staged, direct)
	}
	sqliteExecThreshold(t, node, proto, db, insert(1400), mib)
	if got := sqliteCount(t, node, proto, db, "SELECT count(*) AS n FROM s"); got != 512+512+1400 {
		t.Fatalf("[%s] rows after the staged bulk insert = %d, want %d", proto.name, got, 512+512+1400)
	}
	sqliteExec(t, node, proto, db, "DELETE FROM s WHERE id % 3 = 0")
	sqliteExecThreshold(t, node, proto, db, "VACUUM", mib)
	if got := sqliteCount(t, node, proto, db, "SELECT count(*) AS n FROM s"); got != 1616 {
		t.Fatalf("[%s] rows after the staged VACUUM = %d, want 1616", proto.name, got)
	}
	if got := sqliteCount(t, node, proto, db, "SELECT sum(length(data)) AS n FROM s"); got != 1616*4096 {
		t.Fatalf("[%s] blob bytes after the staged VACUUM = %d, want %d", proto.name, got, 1616*4096)
	}
	sqliteIntegrityOK(t, node, proto, db)
}

// testSQLiteWriterContention runs two `qrdb sqlite exec` on one database at
// once. A writer refused with the busy mapping retries; no sleeps: each
// retry is a new exec once the last one has returned. Both inserts land.
func testSQLiteWriterContention(t *testing.T, node *multiProtoNode, proto multiProtocol, db string) {
	t.Helper()
	const rows, attempts = 300, 200
	sqliteExec(t, node, proto, db, "CREATE TABLE c (id INTEGER PRIMARY KEY, w TEXT)")
	var wg sync.WaitGroup
	errs := make([]error, 2)
	busy := make([]int, 2)
	for w := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stmt := fmt.Sprintf("INSERT INTO c (w) %s", sqliteSeries(rows, fmt.Sprintf("'w%d'", w)))
			for range attempts {
				out, err := runCLIProto(t, node, proto, "sqlite", "exec", db, stmt)
				if err == nil {
					return
				}
				if !strings.Contains(out, "is busy") {
					errs[w] = fmt.Errorf("%w\n%s", err, out)
					return
				}
				busy[w]++
			}
			errs[w] = fmt.Errorf("still busy after %d attempts", attempts)
		}()
	}
	wg.Wait()
	for w, err := range errs {
		if err != nil {
			t.Fatalf("[%s] writer %d: %v", proto.name, w, err)
		}
	}
	for w := range errs {
		if got := sqliteCount(t, node, proto, db, fmt.Sprintf("SELECT count(*) AS n FROM c WHERE w = 'w%d'", w)); got != rows {
			t.Fatalf("[%s] writer %d rows = %d, want %d (busy retries %v)", proto.name, w, got, rows, busy)
		}
	}
	if got := sqliteCount(t, node, proto, db, "SELECT count(*) AS n FROM c"); got != 2*rows {
		t.Fatalf("[%s] rows after two concurrent writers = %d, want %d", proto.name, got, 2*rows)
	}
}

// testSQLiteRestore returns a database to a recorded revision with
// `qrdb sqlite restore`: the query shows the earlier rows again.
func testSQLiteRestore(t *testing.T, node *multiProtoNode, proto multiProtocol, db string) {
	t.Helper()
	sqliteExec(t, node, proto, db, "CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)")
	sqliteExec(t, node, proto, db, "INSERT INTO t (name) VALUES ('before')")
	rev := kvCLIRevision(t, node, proto)
	sqliteExec(t, node, proto, db, "INSERT INTO t (name) VALUES ('after')")
	sqliteExec(t, node, proto, db, "DELETE FROM t WHERE name = 'before'")
	if got := sqliteNames(t, node, proto, db); got != "after" {
		t.Fatalf("[%s] names before the restore = %s, want after", proto.name, got)
	}
	out, err := runCLIProto(t, node, proto, "sqlite", "restore", db, "--revision", strconv.FormatInt(rev, 10))
	if err != nil {
		t.Fatalf("[%s] sqlite restore --revision %d: %v\n%s", proto.name, rev, err, out)
	}
	if got := sqliteNames(t, node, proto, db); got != "before" {
		t.Fatalf("[%s] names after restoring revision %d = %s, want before", proto.name, rev, got)
	}
	sqliteIntegrityOK(t, node, proto, db)
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
	return sqliteDecodeRows(t, proto, out)
}

// sqliteDecodeRows decodes the JSON row array `qrdb --output json sqlite
// query` prints, skipping any output before it.
func sqliteDecodeRows(t *testing.T, proto multiProtocol, out string) []map[string]any {
	t.Helper()
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
