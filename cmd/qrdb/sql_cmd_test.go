//go:build qrdb_sql && !integration

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

// sql_cmd_test.go verifies the `qrdb sql query` command using the
// in-process embedded transport mock pattern (see watch_cmd_test.go /
// lock_cmd_test.go): a real sqlquery.Service is wired over a mock
// service.KVServicer + service.DatabaseServicer so the CLI is exercised
// against the actual SQL execution core, not a stub.
package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// seedSQLKV stores a key/value pair under the composite (system tenant,
// database) namespace the SQL "kv" table scans, matching
// pkg/sql/query/service_test.go's seed helper.
func seedSQLKV(t *testing.T, kv *mockKVService, dbName, key, value string) {
	t.Helper()
	prefixed := tenant.ComposeNamespace(tenantid.System, dbName) + key
	if err := kv.Put(context.Background(), prefixed, []byte(value)); err != nil {
		t.Fatalf("seedSQLKV: %v", err)
	}
}

// executeSQLCmd builds an embedded SDK client over kvSvc/dbSvc, installs
// it as the package-level client, and executes the "sql" command tree
// with args.
func executeSQLCmd(t *testing.T, kvSvc service.KVServicer, dbSvc service.DatabaseServicer, format string, args ...string) (string, error) {
	t.Helper()

	tr, err := embedded.New(kvSvc, newMockAdminService(), embedded.WithDatabaseService(dbSvc))
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	sdkClient, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = sdkClient.Close() })

	oldClient := client
	oldWriter := outputWriter
	oldFormat := outputFormat
	t.Cleanup(func() {
		client = oldClient
		outputWriter = oldWriter
		outputFormat = oldFormat
	})

	client = sdkClient
	outputFormat = format
	var buf bytes.Buffer
	outputWriter = &buf

	cmd := newSQLCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)

	execErr := cmd.ExecuteContext(context.Background())
	return buf.String(), execErr
}

// executeSQLCmdViaRoot runs args through the FULL root command tree (as
// `qrdb --database <x> sql query ...` runs in production), instead of the
// standalone "sql" subtree executeSQLCmd uses. This is required to
// exercise the persistent root --database flag's interaction with sql
// query's own local --database flag (L7: the two share a name but must
// not silently diverge — see effectiveDatabaseScope in collection.go).
func executeSQLCmdViaRoot(t *testing.T, kvSvc service.KVServicer, dbSvc service.DatabaseServicer, format string, args ...string) (string, error) {
	t.Helper()

	root := newRootCmd()
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if cmd.Name() == "version" || cmd.Name() == "help" {
			return nil
		}
		outFmt, _ := cmd.Root().PersistentFlags().GetString("output")
		if err := validateOutputFormat(outFmt); err != nil {
			return err
		}
		outputFormat = outFmt

		tr, err := embedded.New(kvSvc, newMockAdminService(), embedded.WithDatabaseService(dbSvc))
		if err != nil {
			return err
		}
		c, err := sdk.NewClient(tr)
		if err != nil {
			return err
		}
		client = c
		return nil
	}

	oldClient := client
	oldWriter := outputWriter
	oldFormat := outputFormat
	t.Cleanup(func() {
		client = oldClient
		outputWriter = oldWriter
		outputFormat = oldFormat
	})

	outputFormat = format
	var buf bytes.Buffer
	outputWriter = &buf
	root.SetOut(io.Discard)
	root.SetErr(os.Stderr)
	root.SetArgs(args)

	execErr := root.ExecuteContext(context.Background())
	return buf.String(), execErr
}

// TestSQLQueryCmd_HonorsGlobalDatabaseFlag proves the persistent root
// --database flag is NOT silently ignored by 'sql query': without the fix,
// `qrdb --database mydb sql query "..."` always targets "system"
// (transport.DefaultDatabaseScope) regardless of the global flag.
func TestSQLQueryCmd_HonorsGlobalDatabaseFlag(t *testing.T) {
	kv := newMockKVService()
	seedSQLKV(t, kv, "globaldb", "gk", "gv")
	dbSvc := &stubDatabaseService{getFn: kvEngineDatabase}

	out, err := executeSQLCmdViaRoot(t, kv, dbSvc, "table", "--database", "globaldb", "sql", "query", "SELECT * FROM kv")
	if err != nil {
		t.Fatalf("sql query via root: %v", err)
	}
	if !strings.Contains(out, "gk") {
		t.Errorf("expected row from the global-scoped database, got: %q", out)
	}
}

// TestSQLQueryCmd_LocalDatabaseFlagOverridesGlobal proves the more specific
// local --database flag wins when both are supplied.
func TestSQLQueryCmd_LocalDatabaseFlagOverridesGlobal(t *testing.T) {
	kv := newMockKVService()
	seedSQLKV(t, kv, "globaldb", "gk", "gv")
	seedSQLKV(t, kv, "localdb", "lk", "lv")
	dbSvc := &stubDatabaseService{getFn: kvEngineDatabase}

	out, err := executeSQLCmdViaRoot(t, kv, dbSvc, "table",
		"--database", "globaldb", "sql", "query", "--database", "localdb", "SELECT * FROM kv")
	if err != nil {
		t.Fatalf("sql query via root: %v", err)
	}
	if !strings.Contains(out, "lk") {
		t.Errorf("expected row from the explicit local scope, got: %q", out)
	}
	if strings.Contains(out, "gk") {
		t.Errorf("must not see the global-scoped row once local --database overrides it, got: %q", out)
	}
}

// ---------------------------------------------------------------------------
// sql query -- happy paths
// ---------------------------------------------------------------------------

func TestSQLQueryCmd_PositionalArg_TableOutput(t *testing.T) {
	kv := newMockKVService()
	seedSQLKV(t, kv, "testdb", "greeting", "hello world")
	dbSvc := &stubDatabaseService{getFn: kvEngineDatabase}

	out, err := executeSQLCmd(t, kv, dbSvc, "table", "query", "--database", "testdb", "SELECT * FROM kv")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "greeting") || !strings.Contains(out, "hello world") {
		t.Errorf("expected seeded row in table output, got: %q", out)
	}
	if !strings.Contains(out, "1 rows") {
		t.Errorf("expected row-count summary line, got: %q", out)
	}
}

func TestSQLQueryCmd_JSONOutput(t *testing.T) {
	kv := newMockKVService()
	seedSQLKV(t, kv, "testdb", "k1", "v1")
	dbSvc := &stubDatabaseService{getFn: kvEngineDatabase}

	out, err := executeSQLCmd(t, kv, dbSvc, "json", "query", "--database", "testdb", "SELECT * FROM kv")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "\"columns\"") || !strings.Contains(out, "\"rowCount\": 1") {
		t.Errorf("unexpected JSON output: %q", out)
	}
}

func TestSQLQueryCmd_YAMLOutput(t *testing.T) {
	kv := newMockKVService()
	seedSQLKV(t, kv, "testdb", "k1", "v1")
	dbSvc := &stubDatabaseService{getFn: kvEngineDatabase}

	out, err := executeSQLCmd(t, kv, dbSvc, "yaml", "query", "--database", "testdb", "SELECT * FROM kv")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "columns:") {
		t.Errorf("unexpected YAML output: %q", out)
	}
}

func TestSQLQueryCmd_DefaultDatabaseScope(t *testing.T) {
	kv := newMockKVService()
	seedSQLKV(t, kv, "system", "onlykey", "onlyval")
	dbSvc := &stubDatabaseService{getFn: kvEngineDatabase}

	// No --database flag: the "database" local flag defaults to
	// transport.DefaultDatabaseScope ("system"), mirroring 'collection
	// create --database'.
	out, err := executeSQLCmd(t, kv, dbSvc, "table", "query", "SELECT * FROM kv")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "onlykey") {
		t.Errorf("expected default-scope row, got: %q", out)
	}
}

func TestSQLQueryCmd_FileForm(t *testing.T) {
	kv := newMockKVService()
	seedSQLKV(t, kv, "testdb", "fk", "fv")
	dbSvc := &stubDatabaseService{getFn: kvEngineDatabase}

	path := filepath.Join(t.TempDir(), "query.sql")
	if err := os.WriteFile(path, []byte("SELECT * FROM kv\n"), 0o600); err != nil {
		t.Fatalf("write query file: %v", err)
	}

	out, err := executeSQLCmd(t, kv, dbSvc, "table", "query", "--database", "testdb", "--file", path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "fk") {
		t.Errorf("expected file-sourced query result, got: %q", out)
	}
}

func TestSQLQueryCmd_StdinForm(t *testing.T) {
	kv := newMockKVService()
	seedSQLKV(t, kv, "testdb", "sk", "sv")
	dbSvc := &stubDatabaseService{getFn: kvEngineDatabase}

	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { os.Stdin = oldStdin })
	os.Stdin = r
	go func() {
		_, _ = w.WriteString("SELECT * FROM kv")
		_ = w.Close()
	}()

	out, err := executeSQLCmd(t, kv, dbSvc, "table", "query", "--database", "testdb", "--file", "-")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "sk") {
		t.Errorf("expected stdin-sourced query result, got: %q", out)
	}
}

func TestSQLQueryCmd_EmptyResultSet(t *testing.T) {
	kv := newMockKVService()
	dbSvc := &stubDatabaseService{getFn: kvEngineDatabase}

	out, err := executeSQLCmd(t, kv, dbSvc, "table", "query", "--database", "testdb", "SELECT * FROM kv")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "0 rows") {
		t.Errorf("expected zero-row summary line, got: %q", out)
	}
}

// ---------------------------------------------------------------------------
// sql query -- error paths
// ---------------------------------------------------------------------------

func TestSQLQueryCmd_MissingQuery(t *testing.T) {
	kv := newMockKVService()
	dbSvc := &stubDatabaseService{getFn: kvEngineDatabase}

	_, err := executeSQLCmd(t, kv, dbSvc, "table", "query", "--database", "testdb")
	if err == nil {
		t.Fatal("expected error when neither a positional query nor --file is supplied")
	}
	var missing *MissingArgError
	if !errors.As(err, &missing) {
		t.Errorf("expected *MissingArgError, got: %v (%T)", err, err)
	}
}

func TestSQLQueryCmd_BothArgAndFileRejected(t *testing.T) {
	kv := newMockKVService()
	dbSvc := &stubDatabaseService{getFn: kvEngineDatabase}

	path := filepath.Join(t.TempDir(), "q.sql")
	if err := os.WriteFile(path, []byte("SELECT 1"), 0o600); err != nil {
		t.Fatalf("write query file: %v", err)
	}

	_, err := executeSQLCmd(t, kv, dbSvc, "table", "query", "--database", "testdb", "--file", path, "SELECT 1")
	if err == nil {
		t.Fatal("expected error when both a positional query and --file are supplied")
	}
	var invalid *InvalidArgError
	if !errors.As(err, &invalid) {
		t.Errorf("expected *InvalidArgError, got: %v (%T)", err, err)
	}
}

func TestSQLQueryCmd_MissingFile(t *testing.T) {
	kv := newMockKVService()
	dbSvc := &stubDatabaseService{getFn: kvEngineDatabase}

	_, err := executeSQLCmd(t, kv, dbSvc, "table", "query", "--database", "testdb",
		"--file", filepath.Join(t.TempDir(), "missing.sql"))
	if err == nil {
		t.Fatal("expected a file-read error")
	}
	var fileErr *FileReadError
	if !errors.As(err, &fileErr) {
		t.Errorf("expected *FileReadError, got: %v (%T)", err, err)
	}
}

func TestSQLQueryCmd_EmptyFile(t *testing.T) {
	kv := newMockKVService()
	dbSvc := &stubDatabaseService{getFn: kvEngineDatabase}

	path := filepath.Join(t.TempDir(), "empty.sql")
	if err := os.WriteFile(path, []byte("   \n"), 0o600); err != nil {
		t.Fatalf("write empty query file: %v", err)
	}

	_, err := executeSQLCmd(t, kv, dbSvc, "table", "query", "--database", "testdb", "--file", path)
	if err == nil {
		t.Fatal("expected an error for an empty query file")
	}
	var invalid *InvalidArgError
	if !errors.As(err, &invalid) {
		t.Errorf("expected *InvalidArgError, got: %v (%T)", err, err)
	}
}

func TestSQLQueryCmd_UnknownDatabase(t *testing.T) {
	kv := newMockKVService()
	dbSvc := &stubDatabaseService{
		getFn: func(name string) (*database.DatabaseInfo, error) {
			return nil, &database.NotFoundError{Name: name}
		},
	}

	_, err := executeSQLCmd(t, kv, dbSvc, "table", "query", "--database", "missingdb", "SELECT * FROM kv")
	if err == nil {
		t.Fatal("expected the resolver's not-found error to propagate")
	}
	var nf *database.NotFoundError
	if !errors.As(err, &nf) {
		t.Errorf("expected *database.NotFoundError, got: %v (%T)", err, err)
	}
}

func TestSQLQueryCmd_SyntaxError(t *testing.T) {
	kv := newMockKVService()
	dbSvc := &stubDatabaseService{getFn: kvEngineDatabase}

	_, err := executeSQLCmd(t, kv, dbSvc, "table", "query", "--database", "testdb", "SELECTX BAD SYNTAX")
	if err == nil {
		t.Fatal("expected a SQL syntax error to propagate")
	}
}

// ---------------------------------------------------------------------------
// resolveSQLQuery / sqlCellString -- direct unit tests
// ---------------------------------------------------------------------------

func TestResolveSQLQuery_PositionalArg(t *testing.T) {
	got, err := resolveSQLQuery([]string{"SELECT 1"}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "SELECT 1" {
		t.Errorf("got %q, want %q", got, "SELECT 1")
	}
}

func TestResolveSQLQuery_MissingBoth(t *testing.T) {
	_, err := resolveSQLQuery(nil, "")
	if err == nil {
		t.Fatal("expected an error when neither form is supplied")
	}
}

func TestSQLCellString_Nil(t *testing.T) {
	if got := sqlCellString(nil); got != "NULL" {
		t.Errorf("sqlCellString(nil) = %q, want NULL", got)
	}
}

func TestSQLCellString_NonNil(t *testing.T) {
	if got := sqlCellString(42); got != "42" {
		t.Errorf("sqlCellString(42) = %q, want 42", got)
	}
}

// ---------------------------------------------------------------------------
// Verb-tree wiring
// ---------------------------------------------------------------------------

func TestSQLCmd_RegisteredOnRoot(t *testing.T) {
	root := newRootCmd()
	cmd, _, err := root.Find([]string{"sql", "query"})
	if err != nil {
		t.Fatalf("sql query not registered: %v", err)
	}
	for _, flagName := range []string{"database", "file"} {
		if cmd.Flags().Lookup(flagName) == nil {
			t.Errorf("sql query missing flag --%s", flagName)
		}
	}
}
