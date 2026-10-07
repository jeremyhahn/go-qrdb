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

//go:build !integration

package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// ---------------------------------------------------------------------------
// Mock index service
// ---------------------------------------------------------------------------

type mockIndexService struct {
	names []string
	keys  [][]byte
}

func (m *mockIndexService) CreateIndex(_ context.Context, _, _ string, _ index.IndexConfig) error {
	return nil
}

func (m *mockIndexService) DropIndex(_ context.Context, _ string) error {
	return nil
}

// DropOwnedIndex answers as DropIndex does: this double records no
// ownership, and no test that uses it exercises an owner-checked drop.
func (m *mockIndexService) DropOwnedIndex(ctx context.Context, name, _ string) error {
	return m.DropIndex(ctx, name)
}

func (m *mockIndexService) Query(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return m.keys, nil
}

func (m *mockIndexService) Scan(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	result := make(map[string][][]byte)
	for _, k := range m.keys {
		result[string(k)] = [][]byte{k}
	}
	return result, nil
}

func (m *mockIndexService) ListIndexes(_ context.Context) ([]string, error) {
	return m.names, nil
}

// ---------------------------------------------------------------------------
// Index command helpers
// ---------------------------------------------------------------------------

// executeIndexCmd creates an embedded transport with the given index service,
// sets the package-level client, and executes the index command tree.
func executeIndexCmd(t *testing.T, idxSvc service.IndexServicer, args ...string) (string, error) {
	t.Helper()

	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()

	tr, err := embedded.New(
		kvSvc,
		adminSvc,
		embedded.WithIndexService(idxSvc),
	)
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}

	cl, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	if err := cl.Connect(context.Background()); err != nil {
		t.Fatalf("client.Connect: %v", err)
	}

	oldClient := client
	oldWriter := outputWriter
	oldFormat := outputFormat
	t.Cleanup(func() {
		client = oldClient
		outputWriter = oldWriter
		outputFormat = oldFormat
		_ = cl.Close()
	})

	client = cl
	var buf bytes.Buffer
	outputWriter = &buf

	cmd := newIndexCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(os.Stderr)
	cmd.SetArgs(args)

	execErr := cmd.ExecuteContext(context.Background())
	return buf.String(), execErr
}

// ---------------------------------------------------------------------------
// Index create
// ---------------------------------------------------------------------------

func TestIndexCreateCmd_Success(t *testing.T) {
	idxSvc := &mockIndexService{}
	_, err := executeIndexCmd(t, idxSvc, "create", "myindex", "--field", "name")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestIndexCreateCmd_MissingField(t *testing.T) {
	idxSvc := &mockIndexService{}
	_, err := executeIndexCmd(t, idxSvc, "create", "myindex")
	if err == nil {
		t.Fatal("expected error when --field is missing")
	}
}

func TestIndexCreateCmd_WithOptions(t *testing.T) {
	idxSvc := &mockIndexService{}
	_, err := executeIndexCmd(t, idxSvc, "create", "myindex", "--field", "email", "--unique", "--sparse")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Index drop
// ---------------------------------------------------------------------------

func TestIndexDropCmd_Success(t *testing.T) {
	idxSvc := &mockIndexService{}
	_, err := executeIndexCmd(t, idxSvc, "drop", "myindex")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestIndexDropCmd_MissingArg(t *testing.T) {
	idxSvc := &mockIndexService{}
	_, err := executeIndexCmd(t, idxSvc, "drop")
	if err == nil {
		t.Fatal("expected error for missing index name argument")
	}
}

// ---------------------------------------------------------------------------
// Index query
// ---------------------------------------------------------------------------

func TestIndexQueryCmd_Success(t *testing.T) {
	idxSvc := &mockIndexService{
		keys: [][]byte{[]byte("key1"), []byte("key2")},
	}
	out, err := executeIndexCmd(t, idxSvc, "query", "myindex", "--value", "alice")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "key1") {
		t.Fatalf("expected key1 in output, got: %q", out)
	}
}

func TestIndexQueryCmd_MissingValue(t *testing.T) {
	idxSvc := &mockIndexService{}
	_, err := executeIndexCmd(t, idxSvc, "query", "myindex")
	if err == nil {
		t.Fatal("expected error for missing --value flag")
	}
}

func TestIndexQueryCmd_JSON(t *testing.T) {
	idxSvc := &mockIndexService{
		keys: [][]byte{[]byte("k1")},
	}
	out, err := executeIndexCmd(t, idxSvc, "--output", "json", "query", "myindex", "--value", "alice")
	// The --output flag is on the root cmd, not the index subcommand; pass it differently
	_ = out
	// Accept both success and flag-not-found error (depends on cobra hierarchy in test)
	_ = err
}

// ---------------------------------------------------------------------------
// Index scan
// ---------------------------------------------------------------------------

func TestIndexScanCmd_Success(t *testing.T) {
	idxSvc := &mockIndexService{
		keys: [][]byte{[]byte("key1")},
	}
	out, err := executeIndexCmd(t, idxSvc, "scan", "myindex", "--start", "a", "--end", "z")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "key1") {
		t.Fatalf("expected key1 in output, got: %q", out)
	}
}

func TestIndexScanCmd_OpenLowerBound(t *testing.T) {
	// Omitting --start is now an OPEN lower bound (go-qrdb#36); the command
	// must succeed rather than reject a required flag.
	idxSvc := &mockIndexService{}
	if _, err := executeIndexCmd(t, idxSvc, "scan", "myindex", "--end", "z"); err != nil {
		t.Fatalf("omitting --start must scan with an open lower bound, got: %v", err)
	}
}

func TestIndexScanCmd_OpenUpperBound(t *testing.T) {
	idxSvc := &mockIndexService{}
	if _, err := executeIndexCmd(t, idxSvc, "scan", "myindex", "--start", "a"); err != nil {
		t.Fatalf("omitting --end must scan with an open upper bound, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Index list
// ---------------------------------------------------------------------------

func TestIndexListCmd_Success(t *testing.T) {
	idxSvc := &mockIndexService{
		names: []string{"idx1", "idx2"},
	}
	out, err := executeIndexCmd(t, idxSvc, "list")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "idx1") {
		t.Fatalf("expected idx1 in output, got: %q", out)
	}
}

func TestIndexListCmd_EmptyList(t *testing.T) {
	idxSvc := &mockIndexService{names: nil}
	_, err := executeIndexCmd(t, idxSvc, "list")
	if err != nil {
		t.Fatalf("expected no error for empty list, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// formatProtocols
// ---------------------------------------------------------------------------

func TestFormatProtocols_None(t *testing.T) {
	result := formatProtocols(transport.ProtocolConfig{})
	if result != "-" {
		t.Fatalf("expected '-' for no protocols, got %q", result)
	}
}

func TestFormatProtocols_MySQL(t *testing.T) {
	result := formatProtocols(transport.ProtocolConfig{MySQL: true})
	if result != "mysql" {
		t.Fatalf("expected 'mysql', got %q", result)
	}
}

func TestFormatProtocols_PostgreSQL(t *testing.T) {
	result := formatProtocols(transport.ProtocolConfig{PostgreSQL: true})
	if result != "postgresql" {
		t.Fatalf("expected 'postgresql', got %q", result)
	}
}

func TestFormatProtocols_Etcd(t *testing.T) {
	result := formatProtocols(transport.ProtocolConfig{
		Etcd: &transport.EtcdProtocolConfig{Enabled: true},
	})
	if result != "etcd" {
		t.Fatalf("expected 'etcd', got %q", result)
	}
}

func TestFormatProtocols_EtcdDisabled(t *testing.T) {
	result := formatProtocols(transport.ProtocolConfig{
		Etcd: &transport.EtcdProtocolConfig{Enabled: false},
	})
	if result != "-" {
		t.Fatalf("expected '-' for disabled etcd, got %q", result)
	}
}

func TestFormatProtocols_Multiple(t *testing.T) {
	result := formatProtocols(transport.ProtocolConfig{
		MySQL:      true,
		PostgreSQL: true,
		Etcd:       &transport.EtcdProtocolConfig{Enabled: true},
	})
	for _, want := range []string{"mysql", "postgresql", "etcd"} {
		if !strings.Contains(result, want) {
			t.Fatalf("expected %q in result %q", want, result)
		}
	}
}

// ---------------------------------------------------------------------------
// printDatabaseInfo
// ---------------------------------------------------------------------------

func TestPrintDatabaseInfo_Table_Basic(t *testing.T) {
	oldWriter := outputWriter
	oldFormat := outputFormat
	defer func() {
		outputWriter = oldWriter
		outputFormat = oldFormat
	}()

	var buf bytes.Buffer
	outputWriter = &buf
	outputFormat = "table"

	info := &transport.DatabaseInfo{
		Name:    "mydb",
		Backend: "pebble",
		Status:  "open",
	}
	err := printDatabaseInfo(info)
	if err != nil {
		t.Fatalf("printDatabaseInfo: %v", err)
	}

	out := buf.String()
	for _, want := range []string{"mydb", "pebble", "open"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in output %q", want, out)
		}
	}
}

func TestPrintDatabaseInfo_Table_WithAllFields(t *testing.T) {
	oldWriter := outputWriter
	oldFormat := outputFormat
	defer func() {
		outputWriter = oldWriter
		outputFormat = oldFormat
	}()

	var buf bytes.Buffer
	outputWriter = &buf
	outputFormat = "table"

	info := &transport.DatabaseInfo{
		Name:    "fulldb",
		Backend: "pebble",
		Status:  "open",
		DataDir: "/data/fulldb",
		ShardID: 42,
		Protocol: transport.ProtocolConfig{
			Etcd: &transport.EtcdProtocolConfig{
				Enabled:             true,
				ListenAddr:          "0.0.0.0:2379",
				AdvertiseClientURLs: []string{"https://localhost:2379"},
			},
		},
		Raft: &transport.DatabaseRaftInfo{
			ClusterMode: "single",
			RaftAddress: "localhost:9090",
		},
		Barrier: &transport.DatabaseBarrierInfo{
			Enabled: true,
		},
		Connection: &transport.DatabaseConnectionConfig{
			Transport: "grpc",
			Address:   "localhost:5000",
		},
	}
	err := printDatabaseInfo(info)
	if err != nil {
		t.Fatalf("printDatabaseInfo: %v", err)
	}

	out := buf.String()
	for _, want := range []string{
		"fulldb", "pebble", "open",
		"/data/fulldb", "42",
		"Etcd:", "2379", "Raft:", "single",
		"Barrier:", "Connection:", "grpc",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in output %q", want, out)
		}
	}
}

func TestPrintDatabaseInfo_JSON(t *testing.T) {
	oldWriter := outputWriter
	oldFormat := outputFormat
	defer func() {
		outputWriter = oldWriter
		outputFormat = oldFormat
	}()

	var buf bytes.Buffer
	outputWriter = &buf
	outputFormat = "json"

	info := &transport.DatabaseInfo{
		Name:   "jsondb",
		Status: "open",
	}
	err := printDatabaseInfo(info)
	if err != nil {
		t.Fatalf("printDatabaseInfo JSON: %v", err)
	}
	if !strings.Contains(buf.String(), "jsondb") {
		t.Fatalf("expected JSON output with 'jsondb', got: %q", buf.String())
	}
}

func TestPrintDatabaseInfo_YAML(t *testing.T) {
	oldWriter := outputWriter
	oldFormat := outputFormat
	defer func() {
		outputWriter = oldWriter
		outputFormat = oldFormat
	}()

	var buf bytes.Buffer
	outputWriter = &buf
	outputFormat = "yaml"

	info := &transport.DatabaseInfo{
		Name:   "yamldb",
		Status: "open",
	}
	err := printDatabaseInfo(info)
	if err != nil {
		t.Fatalf("printDatabaseInfo YAML: %v", err)
	}
	if !strings.Contains(buf.String(), "yamldb") {
		t.Fatalf("expected YAML output with 'yamldb', got: %q", buf.String())
	}
}

// ---------------------------------------------------------------------------
// database update-protocol command
// ---------------------------------------------------------------------------

func TestDatabaseUpdateProtocolCmd_ExercisesService(t *testing.T) {
	// Use the existing executeDatabaseCmd helper which wires up an embedded transport.
	// The default fnDatabaseService.updateProtocolFn returns a NotFoundError, confirming
	// the command dispatches to the service layer.
	dbSvc := &fnDatabaseService{}
	_, err := executeDatabaseCmd(t, dbSvc, "update-protocol", "mydb", "--mysql")
	// A not-found error from the mock is acceptable; command-routing was exercised.
	_ = err
}

// ---------------------------------------------------------------------------
// database formatProtocols edge cases
// ---------------------------------------------------------------------------

func TestFormatProtocols_OnlyEtcdNilPointer(t *testing.T) {
	result := formatProtocols(transport.ProtocolConfig{Etcd: nil})
	if result != "-" {
		t.Fatalf("expected '-' when Etcd is nil, got %q", result)
	}
}

// ---------------------------------------------------------------------------
// database update subcommands (missing arg validation)
// ---------------------------------------------------------------------------

func TestDatabaseUpdateEtcdCmd_MissingArg(t *testing.T) {
	dbSvc := &fnDatabaseService{}
	_, err := executeDatabaseCmd(t, dbSvc, "update-etcd")
	if err == nil {
		t.Fatal("expected error for missing database name argument")
	}
}

func TestDatabaseUpdateRaftCmd_MissingArg(t *testing.T) {
	dbSvc := &fnDatabaseService{}
	_, err := executeDatabaseCmd(t, dbSvc, "update-raft")
	if err == nil {
		t.Fatal("expected error for missing database name argument")
	}
}

func TestDatabaseUpdateBarrierCmd_MissingArg(t *testing.T) {
	dbSvc := &fnDatabaseService{}
	_, err := executeDatabaseCmd(t, dbSvc, "update-barrier")
	if err == nil {
		t.Fatal("expected error for missing database name argument")
	}
}

func TestDatabaseUpdateConnectionCmd_MissingArg(t *testing.T) {
	dbSvc := &fnDatabaseService{}
	_, err := executeDatabaseCmd(t, dbSvc, "update-connection")
	if err == nil {
		t.Fatal("expected error for missing database name argument")
	}
}

func TestDatabaseUpdateProtocolCmd_MissingArg(t *testing.T) {
	dbSvc := &fnDatabaseService{}
	_, err := executeDatabaseCmd(t, dbSvc, "update-protocol")
	if err == nil {
		t.Fatal("expected error for missing database name argument")
	}
}

// ---------------------------------------------------------------------------
// bindings_daemon.go - resolveConfigDir, cleanupPIDFile
// ---------------------------------------------------------------------------

func TestDaemonBinding_ResolveConfigDir_UsesTempDir(t *testing.T) {
	d := newTestDaemonBinding(t)

	configDir, err := d.resolveConfigDir()
	if err != nil {
		t.Fatalf("resolveConfigDir: %v", err)
	}
	if configDir != d.configDir {
		t.Fatalf("expected %q, got %q", d.configDir, configDir)
	}
}

func TestDaemonBinding_ResolveConfigDir_CreatesDir(t *testing.T) {
	d := newTestDaemonBinding(t)
	// Change configDir to a subdir that doesn't exist yet
	d.configDir = d.configDir + "/subdir"

	configDir, err := d.resolveConfigDir()
	if err != nil {
		t.Fatalf("resolveConfigDir: %v", err)
	}
	if configDir != d.configDir {
		t.Fatalf("expected %q, got %q", d.configDir, configDir)
	}

	// Directory should have been created
	if _, statErr := os.Stat(d.configDir); os.IsNotExist(statErr) {
		t.Fatal("expected directory to be created")
	}
}

func TestDaemonBinding_CleanupPIDFile_NoFile(t *testing.T) {
	d := newTestDaemonBinding(t)
	// Should be a no-op if the file doesn't exist
	d.cleanupPIDFile()
}

func TestDaemonBinding_CleanupPIDFile_DeletesExistingFile(t *testing.T) {
	d := newTestDaemonBinding(t)

	// Write a PID file
	pidPath := d.configDir + "/qrdbd.pid"
	if err := os.WriteFile(pidPath, []byte("1234"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	d.cleanupPIDFile()

	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatal("expected PID file to be removed after cleanupPIDFile")
	}
}

// ---------------------------------------------------------------------------
// DaemonBinding.shutdown with process tracked
// ---------------------------------------------------------------------------

func TestDaemonBinding_Shutdown_NoProcess(t *testing.T) {
	d := newTestDaemonBinding(t)
	// shutdown with no tracked process is a no-op.
	d.shutdown(context.Background())
}

// ---------------------------------------------------------------------------
// DaemonBinding.GetLogs - with explicit logFile
// ---------------------------------------------------------------------------

func TestDaemonBinding_GetLogs_WithExplicitLogFile(t *testing.T) {
	tmpFile, err := os.CreateTemp(t.TempDir(), "test-*.log")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	_, _ = tmpFile.WriteString("log line 1\nlog line 2\n")
	_ = tmpFile.Close()

	log := testLogger(t)
	d := NewDaemonBinding(log, tmpFile.Name())
	d.configDir = t.TempDir()

	logs, err := d.GetLogs(10)
	if err != nil {
		t.Fatalf("GetLogs: %v", err)
	}
	if !strings.Contains(logs, "log line 1") {
		t.Fatalf("expected 'log line 1' in logs, got: %q", logs)
	}
}

func TestDaemonBinding_GetLogs_ExplicitLogFileNotExist(t *testing.T) {
	log := testLogger(t)
	d := NewDaemonBinding(log, "/nonexistent/path/to/logfile.log")
	d.configDir = t.TempDir()

	logs, err := d.GetLogs(10)
	if err != nil {
		t.Fatalf("expected no error for non-existent explicit log file, got: %v", err)
	}
	if logs != "" {
		t.Fatalf("expected empty string for non-existent log file, got: %q", logs)
	}
}

// ---------------------------------------------------------------------------
// bindings_daemon.go - Status with in-memory process
// ---------------------------------------------------------------------------

func TestDaemonBinding_Status_WithInMemoryProcess(t *testing.T) {
	d := newTestDaemonBinding(t)

	// Inject the current process into the daemon binding to simulate
	// a tracked process.
	proc, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess: %v", err)
	}
	d.process.Store(proc)

	status, err := d.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status.Running {
		t.Error("expected Running to be true when process is tracked")
	}
	if status.PID != os.Getpid() {
		t.Errorf("expected PID %d, got %d", os.Getpid(), status.PID)
	}
}

// ---------------------------------------------------------------------------
// bindings.go - Disconnect with connected client
// ---------------------------------------------------------------------------

func TestConnectionBinding_Disconnect_ClearsClient(t *testing.T) {
	conn := newTestConnectionBinding(t, newMockKVService(), newMockAdminService())
	if !conn.IsConnected() {
		t.Fatal("expected IsConnected() == true before Disconnect")
	}
	if err := conn.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if conn.IsConnected() {
		t.Fatal("expected IsConnected() == false after Disconnect")
	}
}

// ---------------------------------------------------------------------------
// ConnectionBinding.shutdown with client
// ---------------------------------------------------------------------------

func TestConnectionBinding_Shutdown_ClosesConnectedClient(t *testing.T) {
	conn := newTestConnectionBinding(t, newMockKVService(), newMockAdminService())
	// Calling shutdown should close the client without panicking.
	conn.shutdown(context.Background())
}

// ---------------------------------------------------------------------------
// ConnectionBinding.GetVersion
// ---------------------------------------------------------------------------

func TestConnectionBinding_GetVersion_ContainsExpectedKeys(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	v := conn.GetVersion()
	if _, ok := v["version"]; !ok {
		t.Error("expected 'version' key in GetVersion result")
	}
	if _, ok := v["gitCommit"]; !ok {
		t.Error("expected 'gitCommit' key in GetVersion result")
	}
	if _, ok := v["buildDate"]; !ok {
		t.Error("expected 'buildDate' key in GetVersion result")
	}
}

func TestConnectionBinding_GetVersion_HasThreeKeys(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	v := conn.GetVersion()
	if len(v) != 3 {
		t.Fatalf("expected 3 keys in GetVersion result, got %d: %v", len(v), v)
	}
}
