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

package main

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/sqlitevfs"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	sdksqlitevfs "github.com/jeremyhahn/go-qrdb/sdk/go/sqlitevfs"
	"github.com/jeremyhahn/go-qrdb/sdk/go/standalone"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newSQLiteTestServer starts an in-memory standalone server and returns its
// client with a root command connected to it.
func newSQLiteTestServer(t *testing.T) (*sdk.Client, func() *cobra.Command) {
	t.Helper()
	ctx := context.Background()
	srv, err := standalone.NewServer(ctx, &standalone.ServerConfig{Backend: database.BackendMemory, DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("standalone.NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(ctx) })
	c := srv.Client()
	root := func() *cobra.Command {
		r := newRootCmd()
		r.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
			format, _ := cmd.Root().PersistentFlags().GetString("output")
			if err := validateOutputFormat(format); err != nil {
				return err
			}
			outputFormat = format
			client = c
			return nil
		}
		return r
	}
	return c, root
}

// runSQLite runs one qrdb command and fails the test on an error.
func runSQLite(t *testing.T, root func() *cobra.Command, args ...string) string {
	t.Helper()
	out, err := executeCommand(root(), args...)
	if err != nil {
		t.Fatalf("qrdb %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// queryNames returns the name column of table t of db, in id order.
func queryNames(t *testing.T, root func() *cobra.Command, db string) []string {
	t.Helper()
	out := runSQLite(t, root, "--output", "json", "sqlite", "query", db, "SELECT name FROM t ORDER BY id")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("query output %q: %v", out, err)
	}
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		s, _ := r["name"].(string)
		names = append(names, s)
	}
	return names
}

func exitCode(err error) (int, bool) {
	var ec interface{ ExitCode() int }
	if errors.As(err, &ec) {
		return ec.ExitCode(), true
	}
	return 0, false
}

func TestSQLiteCmd_ExecThenQuery(t *testing.T) {
	_, root := newSQLiteTestServer(t)
	runSQLite(t, root, "sqlite", "exec", "app", "CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)")
	out := runSQLite(t, root, "sqlite", "exec", "app", "INSERT INTO t (name) VALUES ('alice'), ('bob')")
	if !strings.Contains(out, "2 rows affected") {
		t.Fatalf("exec output = %q, want 2 rows affected", out)
	}
	out = runSQLite(t, root, "--output", "json", "sqlite", "exec", "app", "UPDATE t SET name = 'carol' WHERE id = 2", "--concurrency", "optimistic")
	var affected map[string]int64
	if err := json.Unmarshal([]byte(out), &affected); err != nil || affected["rows_affected"] != 1 {
		t.Fatalf("exec json output = %q (%v), want rows_affected 1", out, err)
	}
	if got := strings.Join(queryNames(t, root, "app"), ","); got != "alice,carol" {
		t.Fatalf("names = %s, want alice,carol", got)
	}
	out = runSQLite(t, root, "sqlite", "query", "app", "SELECT id, name FROM t ORDER BY id")
	if !strings.Contains(out, "alice") || !strings.Contains(out, "carol") || !strings.Contains(strings.ToUpper(out), "NAME") {
		t.Fatalf("table output = %q, want the header and both rows", out)
	}
}

func TestSQLiteCmd_RefusesBadArguments(t *testing.T) {
	_, root := newSQLiteTestServer(t)
	cases := [][]string{
		{"sqlite", "exec", "app", "SELECT 1", "--concurrency", "pessimistic"},
		{"sqlite", "exec", "app", "SELECT 1", "--stage-threshold", "-1"},
		{"sqlite", "exec", "bad/name", "SELECT 1"},
		{"sqlite", "restore", "bad/name", "--revision", "1"},
		{"sqlite", "restore", "app"},
	}
	for _, args := range cases {
		if out, err := executeCommand(root(), args...); err == nil {
			t.Errorf("qrdb %s succeeded (%q), want an error", strings.Join(args, " "), out)
		}
	}
}

func TestSQLiteCmd_BusyExitCode(t *testing.T) {
	c, root := newSQLiteTestServer(t)
	runSQLite(t, root, "sqlite", "exec", "app", "CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)")
	key, err := sqlitevfs.WriterLockKey("app")
	if err != nil {
		t.Fatalf("WriterLockKey: %v", err)
	}
	if _, err := c.Lock().LockTryLock(t.Context(), &transport.LockAcquireRequest{Key: key, HolderID: "another-writer", TTL: 30}); err != nil {
		t.Fatalf("hold the writer lock: %v", err)
	}
	out, err := executeCommand(root(), "sqlite", "exec", "app", "INSERT INTO t (name) VALUES ('alice')")
	var busy *SQLiteBusyError
	if !errors.As(err, &busy) || busy.DB != "app" {
		t.Fatalf("exec while another writer holds the lock = %v (%q), want SQLiteBusyError for app", err, out)
	}
	if code, ok := exitCode(err); !ok || code != 5 {
		t.Fatalf("exit code = %d, %v; want 5", code, ok)
	}
	if got := queryNames(t, root, "app"); len(got) != 0 {
		t.Fatalf("names after the refused insert = %v, want none", got)
	}
}

// failingTxnClient refuses every transaction with err.
type failingTxnClient struct {
	transport.TransactionClient
	err error
}

func (f failingTxnClient) ExecuteTransaction(context.Context, *transport.TransactionRequest, ...transport.WriteOption) (*transport.TransactionResponse, error) {
	return nil, f.err
}

func TestSQLiteCmd_FullExitCode(t *testing.T) {
	_, root := newSQLiteTestServer(t)
	ex := &quota.ExceededError{TenantID: "tenant-a", Resource: quota.ResourceStorageBytes, Current: 100, Limit: 64}
	refusal := &transport.RequestError{
		Method:     "grpc",
		StatusCode: 8,
		Reason:     quota.ReasonQuotaExceeded,
		Metadata:   ex.Metadata(),
		Err:        errors.New(ex.Error()),
	}
	saved := sqliteBackend
	sqliteBackend = func() (sqlitevfs.Backend, error) {
		return sdksqlitevfs.NewBackend(client.MVCC(), failingTxnClient{TransactionClient: client.Transaction(), err: refusal})
	}
	t.Cleanup(func() { sqliteBackend = saved })
	out, err := executeCommand(root(), "sqlite", "exec", "app", "CREATE TABLE t (id INTEGER PRIMARY KEY)", "--concurrency", "optimistic")
	var full *SQLiteFullError
	if !errors.As(err, &full) || full.DB != "app" {
		t.Fatalf("exec past the quota = %v (%q), want SQLiteFullError for app", err, out)
	}
	if code, ok := exitCode(err); !ok || code != 13 {
		t.Fatalf("exit code = %d, %v; want 13", code, ok)
	}
}

func TestSQLiteCmd_RestoreToRevision(t *testing.T) {
	c, root := newSQLiteTestServer(t)
	runSQLite(t, root, "sqlite", "exec", "app", "CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)")
	runSQLite(t, root, "sqlite", "exec", "app", "INSERT INTO t (name) VALUES ('alice')")
	rev, err := c.MVCC().CurrentRevision(t.Context())
	if err != nil {
		t.Fatalf("CurrentRevision: %v", err)
	}
	runSQLite(t, root, "sqlite", "exec", "app", "INSERT INTO t (name) VALUES ('bob')")
	if got := strings.Join(queryNames(t, root, "app"), ","); got != "alice,bob" {
		t.Fatalf("names before restore = %s, want alice,bob", got)
	}
	runSQLite(t, root, "sqlite", "restore", "app", "--revision", strconv.FormatInt(rev, 10), "--dry-run")
	if got := strings.Join(queryNames(t, root, "app"), ","); got != "alice,bob" {
		t.Fatalf("names after a dry run = %s, want alice,bob", got)
	}
	runSQLite(t, root, "sqlite", "restore", "app", "--revision", strconv.FormatInt(rev, 10))
	if got := strings.Join(queryNames(t, root, "app"), ","); got != "alice" {
		t.Fatalf("names after restore = %s, want alice", got)
	}
}

func TestSQLiteCmd_RefusesREST(t *testing.T) {
	c, root := newSQLiteTestServer(t)
	saved := clientTransport
	t.Cleanup(func() { clientTransport = saved })
	before, err := c.MVCC().CurrentRevision(t.Context())
	if err != nil {
		t.Fatalf("CurrentRevision: %v", err)
	}
	for _, tr := range []string{transportREST, transportMCP} {
		clientTransport = tr
		for _, args := range [][]string{
			{"sqlite", "exec", "app", "CREATE TABLE t (id INTEGER PRIMARY KEY)"},
			{"sqlite", "query", "app", "SELECT 1"},
			{"sqlite", "restore", "app", "--revision", "1"},
		} {
			out, err := executeCommand(root(), args...)
			if err == nil || !strings.Contains(err.Error(), "the "+tr+" transport") {
				t.Fatalf("qrdb %s over %s: err = %v (%s), want a refusal naming %s", strings.Join(args, " "), tr, err, out, tr)
			}
		}
	}
	after, err := c.MVCC().CurrentRevision(t.Context())
	if err != nil {
		t.Fatalf("CurrentRevision: %v", err)
	}
	if after != before {
		t.Fatalf("revision moved from %d to %d, want nothing written over REST or MCP", before, after)
	}
}

func TestSQLiteCmd_NameReachesOpenerUnchanged(t *testing.T) {
	c, root := newSQLiteTestServer(t)
	hasMeta := func(db string) bool {
		t.Helper()
		key, err := sqlitevfs.MetaKey(db)
		if err != nil {
			t.Fatalf("MetaKey(%q): %v", db, err)
		}
		resp, err := c.MVCC().Range(t.Context(), key, append(append([]byte(nil), key...), 0), transport.RangeOptions{Limit: 1})
		if err != nil {
			t.Fatalf("Range(%q): %v", key, err)
		}
		return len(resp.Kvs) == 1
	}
	runSQLite(t, root, "sqlite", "exec", "app", "CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)")
	runSQLite(t, root, "sqlite", "exec", "app", "INSERT INTO t (name) VALUES ('alice')")
	for _, name := range []string{"a%61pp", "app#x"} {
		runSQLite(t, root, "sqlite", "exec", name, "CREATE TABLE u (id INTEGER PRIMARY KEY)")
		if !hasMeta(name) {
			t.Fatalf("no meta record under %q, want the database stored under its own name", name)
		}
		if _, err := executeCommand(root(), "sqlite", "query", name, "SELECT name FROM t"); err == nil {
			t.Fatalf("query on %q read the table of app, want its own database", name)
		}
	}
	if got := strings.Join(queryNames(t, root, "app"), ","); got != "alice" {
		t.Fatalf("names in app = %s, want alice", got)
	}
	if hasMeta("x") || hasMeta("a") {
		t.Fatal("a meta record exists under a truncated name")
	}
}
