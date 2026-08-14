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

//go:build qrdb_embedded && !integration

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// ===========================================================================
// resolveConfigDir — covers the os.UserConfigDir() fallback path (27.3%)
// ===========================================================================

// TestResolveConfigDir_DefaultPath exercises the branch where configDir is
// empty and os.UserConfigDir() resolves via XDG_CONFIG_HOME.
func TestResolveConfigDir_DefaultPath(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	log := testLogger(t)
	d := NewDaemonBinding(log, "")
	// configDir is empty — forces the os.UserConfigDir() branch.

	got, err := d.resolveConfigDir()
	if err != nil {
		t.Fatalf("resolveConfigDir: %v", err)
	}
	if got == "" {
		t.Fatal("expected non-empty directory path")
	}
	if _, statErr := os.Stat(got); statErr != nil {
		t.Fatalf("expected returned directory to exist: %v", statErr)
	}
	if filepath.Base(got) != daemonConfigDir {
		t.Fatalf("expected base dir %q, got %q", daemonConfigDir, filepath.Base(got))
	}
}

// TestResolveConfigDir_DefaultPath_Idempotent verifies calling resolveConfigDir
// twice with an empty configDir yields the same path.
func TestResolveConfigDir_DefaultPath_Idempotent(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	log := testLogger(t)
	d := NewDaemonBinding(log, "")

	first, err := d.resolveConfigDir()
	if err != nil {
		t.Fatalf("first resolveConfigDir: %v", err)
	}
	second, err := d.resolveConfigDir()
	if err != nil {
		t.Fatalf("second resolveConfigDir: %v", err)
	}
	if first != second {
		t.Fatalf("expected idempotent path, got %q and %q", first, second)
	}
}

// ===========================================================================
// cleanupPIDFile — success and no-file paths (57.1%)
// ===========================================================================

// TestCleanupPIDFile_RemovesExistingFile verifies the normal success path.
func TestCleanupPIDFile_RemovesExistingFile(t *testing.T) {
	log := testLogger(t)
	d := NewDaemonBinding(log, "")
	d.configDir = t.TempDir()

	pidPath := filepath.Join(d.configDir, "qrdbd.pid")
	if err := os.WriteFile(pidPath, []byte("12345"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	d.cleanupPIDFile()

	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("expected PID file to be removed after cleanupPIDFile")
	}
}

// TestCleanupPIDFile_NoPIDFile verifies that cleanupPIDFile is a no-op when
// no PID file exists.
func TestCleanupPIDFile_NoPIDFile(t *testing.T) {
	log := testLogger(t)
	d := NewDaemonBinding(log, "")
	d.configDir = t.TempDir()

	// No PID file written; must not panic.
	d.cleanupPIDFile()
}

// ===========================================================================
// resolveDataDir — covers the os.UserConfigDir() fallback branch (77.8%)
// ===========================================================================

// TestResolveDataDir_DefaultPath exercises the branch where cfg.DataDir is
// empty and the path is derived from os.UserConfigDir().
func TestResolveDataDir_DefaultPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	log := testLogger(t)
	conn := NewConnectionBinding(log)
	esb := NewEmbeddedServerBinding(log, conn, NewSQLBinding(log, conn))

	cfg := &EmbeddedConfig{DataDir: ""}
	got, err := esb.resolveDataDir(cfg)
	if err != nil {
		t.Fatalf("resolveDataDir: %v", err)
	}
	if got == "" {
		t.Fatal("expected non-empty data directory path")
	}
	if _, statErr := os.Stat(got); statErr != nil {
		t.Fatalf("expected returned directory to exist: %v", statErr)
	}
}

// TestResolveDataDir_ExplicitPath verifies that an explicit DataDir is used
// as-is after being created.
func TestResolveDataDir_ExplicitPath(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	esb := NewEmbeddedServerBinding(log, conn, NewSQLBinding(log, conn))

	explicit := filepath.Join(t.TempDir(), "explicit-data")
	cfg := &EmbeddedConfig{DataDir: explicit}

	got, err := esb.resolveDataDir(cfg)
	if err != nil {
		t.Fatalf("resolveDataDir: %v", err)
	}
	if got != explicit {
		t.Fatalf("expected %q, got %q", explicit, got)
	}
	if _, statErr := os.Stat(got); statErr != nil {
		t.Fatalf("expected explicit data dir to be created: %v", statErr)
	}
}

// ===========================================================================
// ClearSavedConfig — covers the os.Remove error branch (66.7%)
// ===========================================================================

// TestClearSavedConfig_RemoveError triggers the path where os.Remove fails
// with an error other than "not exist" (directory planted at file path).
func TestClearSavedConfig_RemoveError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	log := testLogger(t)
	conn := NewConnectionBinding(log)
	esb := NewEmbeddedServerBinding(log, conn, NewSQLBinding(log, conn))

	path, err := esb.configPath()
	if err != nil {
		t.Fatalf("configPath: %v", err)
	}

	// Plant a non-empty directory where the config file should be.
	// os.Remove on an empty directory succeeds on Linux, so we must add a
	// child file to make the directory non-empty, causing ENOTEMPTY.
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "block"), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile block: %v", err)
	}

	removeErr := esb.ClearSavedConfig()
	if removeErr == nil {
		t.Fatal("expected error when config path is a non-empty directory")
	}
	var cse *ConfigSaveError
	if !errors.As(removeErr, &cse) {
		t.Fatalf("expected ConfigSaveError, got %T: %v", removeErr, removeErr)
	}
}

// ===========================================================================
// saveConfig — covers the os.WriteFile error branch (70%)
// ===========================================================================

// TestSaveConfig_WriteError triggers the path where os.WriteFile fails
// because the config path is occupied by a directory.
func TestSaveConfig_WriteError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	log := testLogger(t)
	conn := NewConnectionBinding(log)
	esb := NewEmbeddedServerBinding(log, conn, NewSQLBinding(log, conn))

	path, err := esb.configPath()
	if err != nil {
		t.Fatalf("configPath: %v", err)
	}
	// Plant a directory at the file path.
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("MkdirAll to block write: %v", err)
	}

	cfg := &EmbeddedConfig{StorageBackend: "memory"}
	saveErr := esb.saveConfig(cfg)
	if saveErr == nil {
		t.Fatal("expected error when config file path is a directory")
	}
	var cse *ConfigSaveError
	if !errors.As(saveErr, &cse) {
		t.Fatalf("expected ConfigSaveError, got %T: %v", saveErr, saveErr)
	}
}

// ===========================================================================
// configPath — covers the MkdirAll error branch (71.4%)
// ===========================================================================

// TestConfigPath_MkdirAllError exercises the branch where directory creation
// fails because a regular file blocks the intended parent path.
func TestConfigPath_MkdirAllError(t *testing.T) {
	// XDG_CONFIG_HOME pointed at a regular file makes MkdirAll("file/qrdb") fail.
	blockFile := filepath.Join(t.TempDir(), "block")
	if err := os.WriteFile(blockFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", blockFile)

	log := testLogger(t)
	conn := NewConnectionBinding(log)
	esb := NewEmbeddedServerBinding(log, conn, NewSQLBinding(log, conn))

	_, err := esb.configPath()
	if err == nil {
		t.Fatal("expected error when MkdirAll cannot create config directory")
	}
	var dde *DataDirCreateError
	if !errors.As(err, &dde) {
		t.Fatalf("expected DataDirCreateError, got %T: %v", err, err)
	}
}

// ===========================================================================
// buildClient (config.go) — covers error return branches (73.3%)
// ===========================================================================

// TestBuildClient_UnknownTransport verifies that an unsupported transport
// type returns a TransportError.
func TestBuildClient_UnknownTransport(t *testing.T) {
	ctx := context.Background()
	_, err := buildClient(ctx, "badtransport", "localhost:1234", "", "", "", 0)
	if err == nil {
		t.Fatal("expected error for unknown transport type")
	}
	var te *TransportError
	if !errors.As(err, &te) {
		t.Fatalf("expected TransportError, got %T: %v", err, err)
	}
}

// TestBuildClient_UnknownTransport_Message verifies the error message includes
// the transport type name.
func TestBuildClient_UnknownTransport_Message(t *testing.T) {
	ctx := context.Background()
	_, err := buildClient(ctx, "xyzzy", "localhost:1234", "", "", "", 0)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "xyzzy") {
		t.Fatalf("expected error to mention transport name, got: %q", err.Error())
	}
}

// ===========================================================================
// newBarrierSealCmd — error path (80%)
// ===========================================================================

// TestBarrierSealCmd_ServiceError verifies that a service error from
// BarrierSeal is propagated.
func TestBarrierSealCmd_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		sealFn: func(_ context.Context) error {
			return errors.New("barrier: seal failed")
		},
	}
	root := newBarrierTestRoot(t, svc)
	// -y skips the destructive-action confirmation prompt so the call
	// reaches the service and surfaces its error.
	_, err := executeCommand(root, "barrier", "seal", "-y")
	if err == nil {
		t.Fatal("expected error from barrier seal when service fails")
	}
	if !strings.Contains(err.Error(), "seal failed") {
		t.Fatalf("expected 'seal failed' in error, got: %q", err.Error())
	}
}

// TestBarrierSealCmd_NoExtraArgs verifies that cobra.NoArgs rejects
// positional arguments.
func TestBarrierSealCmd_NoExtraArgs(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	_, err := executeCommand(root, "barrier", "seal", "unexpected-arg")
	if err == nil {
		t.Fatal("expected error for unexpected positional argument")
	}
}

// ===========================================================================
// newBarrierRecoveryStatusCmd — JSON / YAML output paths (66.7%)
// ===========================================================================

// TestBarrierRecoveryStatusCmd_JSONOutput exercises the JSON output branch.
func TestBarrierRecoveryStatusCmd_JSONOutput(t *testing.T) {
	svc := &mockBarrierService{
		hasRecoveryKeysFn: func(_ context.Context) (bool, error) {
			return true, nil
		},
	}
	root := newBarrierTestRoot(t, svc)
	out, err := executeCommand(root, "--output", "json", "barrier", "recovery", "status")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// JSON output must be non-empty.
	if strings.TrimSpace(out) == "" {
		t.Errorf("expected non-empty JSON output")
	}
}

// TestBarrierRecoveryStatusCmd_YAMLOutput exercises the YAML output branch.
func TestBarrierRecoveryStatusCmd_YAMLOutput(t *testing.T) {
	svc := &mockBarrierService{
		hasRecoveryKeysFn: func(_ context.Context) (bool, error) {
			return false, nil
		},
	}
	root := newBarrierTestRoot(t, svc)
	_, err := executeCommand(root, "--output", "yaml", "barrier", "recovery", "status")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestBarrierRecoveryStatusCmd_ServiceError verifies that a service error is
// propagated.
func TestBarrierRecoveryStatusCmd_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		hasRecoveryKeysFn: func(_ context.Context) (bool, error) {
			return false, errors.New("recovery: storage unavailable")
		},
	}
	root := newBarrierTestRoot(t, svc)
	_, err := executeCommand(root, "barrier", "recovery", "status")
	if err == nil {
		t.Fatal("expected error from barrier recovery status when service fails")
	}
}

// ===========================================================================
// newIndexListCmd — JSON / YAML / error output paths (76.9%)
// ===========================================================================

// TestIndexListCmd_ServiceError verifies that a service error from IndexList
// is propagated.
func TestIndexListCmd_ServiceError(t *testing.T) {
	errSvc := &errorIndexListService{}
	_, err := executeIndexCmd(t, errSvc, "list")
	if err == nil {
		t.Fatal("expected error from index list when service fails")
	}
}

// TestIndexListCmd_YAMLOutput exercises the YAML output branch by setting
// the global outputFormat before executing the command.
func TestIndexListCmd_YAMLOutput(t *testing.T) {
	idxSvc := &mockIndexService{names: []string{"idx-a"}}
	oldFormat := outputFormat
	t.Cleanup(func() { outputFormat = oldFormat })
	outputFormat = "yaml"
	_, err := executeIndexCmd(t, idxSvc, "list")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// errorIndexListService returns errors from ListIndexes; all other
// methods delegate to the embedded mockIndexService.
type errorIndexListService struct {
	mockIndexService
}

func (e *errorIndexListService) ListIndexes(_ context.Context) ([]string, error) {
	return nil, errors.New("index: list failed")
}

func (e *errorIndexListService) CreateIndex(_ context.Context, _, _ string, _ index.IndexConfig) error {
	return nil
}

// ===========================================================================
// newBatchExecuteCmd — stdin read branch (77.4%)
// ===========================================================================

// TestBatchExecuteCmd_StdinSuccess exercises the stdin read path ("-").
func TestBatchExecuteCmd_StdinSuccess(t *testing.T) {
	root := newTestRootCmdWithOpts(newMockKVService(), newMockAdminService(),
		embedded.WithBatchService(&mockBatchService{}))

	batchReq := transport.BatchRequest{
		Ops: []transport.Op{
			{Type: transport.OpPut, Key: []byte("k1"), Value: []byte("v1")},
		},
	}
	data, _ := json.Marshal(batchReq)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	oldStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = oldStdin
		_ = r.Close()
	})

	if _, writeErr := w.Write(data); writeErr != nil {
		t.Fatalf("write to pipe: %v", writeErr)
	}
	_ = w.Close()

	out, cmdErr := executeCommand(root, "batch", "execute", "--file", "-")
	if cmdErr != nil {
		t.Fatalf("expected no error from stdin batch, got: %v\noutput: %s", cmdErr, out)
	}
}

// TestBatchExecuteCmd_StdinInvalidJSON verifies malformed stdin JSON returns
// an error.
func TestBatchExecuteCmd_StdinInvalidJSON(t *testing.T) {
	root := newTestRootCmdWithOpts(newMockKVService(), newMockAdminService(),
		embedded.WithBatchService(&mockBatchService{}))

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	oldStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = oldStdin
		_ = r.Close()
	})

	if _, writeErr := w.Write([]byte("{invalid json")); writeErr != nil {
		t.Fatalf("write to pipe: %v", writeErr)
	}
	_ = w.Close()

	_, cmdErr := executeCommand(root, "batch", "execute", "--file", "-")
	if cmdErr == nil {
		t.Fatal("expected error from stdin batch with invalid JSON")
	}
}

// ===========================================================================
// newTxExecuteCmd — stdin read branch (78.1%)
// ===========================================================================

// TestTxExecuteCmd_StdinSuccess exercises the stdin read path ("-").
func TestTxExecuteCmd_StdinSuccess(t *testing.T) {
	root := newTestRootCmdWithOpts(newMockKVService(), newMockAdminService(),
		embedded.WithTransactionService(&mockTransactionService{}))

	txReq := transport.TransactionRequest{
		SuccessOps: []transport.Op{
			{Type: transport.OpPut, Key: []byte("k"), Value: []byte("v")},
		},
	}
	data, _ := json.Marshal(txReq)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	oldStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = oldStdin
		_ = r.Close()
	})

	if _, writeErr := w.Write(data); writeErr != nil {
		t.Fatalf("write to pipe: %v", writeErr)
	}
	_ = w.Close()

	out, cmdErr := executeCommand(root, "tx", "execute", "--file", "-")
	if cmdErr != nil {
		t.Fatalf("expected no error from stdin tx, got: %v\noutput: %s", cmdErr, out)
	}
}

// TestTxExecuteCmd_StdinInvalidJSON verifies malformed stdin JSON returns
// an error.
func TestTxExecuteCmd_StdinInvalidJSON(t *testing.T) {
	root := newTestRootCmdWithOpts(newMockKVService(), newMockAdminService(),
		embedded.WithTransactionService(&mockTransactionService{}))

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	oldStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = oldStdin
		_ = r.Close()
	})

	if _, writeErr := w.Write([]byte("{bad json!!")); writeErr != nil {
		t.Fatalf("write to pipe: %v", writeErr)
	}
	_ = w.Close()

	_, cmdErr := executeCommand(root, "tx", "execute", "--file", "-")
	if cmdErr == nil {
		t.Fatal("expected error from stdin tx with invalid JSON")
	}
}

// ===========================================================================
// embeddedDatabaseInstance.Close — additional branch coverage (53.8%)
// ===========================================================================

// TestEmbeddedDatabaseInstance_Close_AllNilFields verifies that Close on an
// instance with all nil fields returns nil (exercises the nil-guard branches).
func TestEmbeddedDatabaseInstance_Close_AllNilFields(t *testing.T) {
	inst := &embeddedDatabaseInstance{}
	if err := inst.Close(); err != nil {
		t.Fatalf("Close on empty instance: %v", err)
	}
}

// TestEmbeddedDatabaseInstance_Close_NilFieldsAreSkipped verifies that Close
// returns nil regardless of nil fields, a second time for completeness.
func TestEmbeddedDatabaseInstance_Close_NilFieldsAreSkipped(t *testing.T) {
	inst := &embeddedDatabaseInstance{
		watchHub:      nil,
		vectorStorage: nil,
		host:          nil,
		localBackend:  nil,
	}
	if err := inst.Close(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ===========================================================================
// newTransport — unknown type returns InvalidArgError
// ===========================================================================

// TestNewTransport_UnknownType verifies that an unrecognised transport name
// returns an InvalidArgError.
func TestNewTransport_UnknownType(t *testing.T) {
	_, err := newTransport("unknown-transport-xyz")
	if err == nil {
		t.Fatal("expected error for unknown transport type")
	}
	var iae *InvalidArgError
	if !errors.As(err, &iae) {
		t.Fatalf("expected InvalidArgError, got %T: %v", err, err)
	}
}

// TestNewTransport_UnknownType_Message verifies the error message contains
// the transport type name.
func TestNewTransport_UnknownType_Message(t *testing.T) {
	_, err := newTransport("badtype")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "badtype") {
		t.Fatalf("expected error to contain transport name, got: %q", err.Error())
	}
}

// ===========================================================================
// resolveTransport — additional scheme branches
// ===========================================================================

// TestResolveTransport_GRPCSScheme verifies grpcs:// stripping.
func TestResolveTransport_GRPCSScheme(t *testing.T) {
	gotType, gotAddr := resolveTransport("grpcs://myhost:443", "")
	if gotType != transportGRPC {
		t.Fatalf("transport: got %q, want %q", gotType, transportGRPC)
	}
	if gotAddr != "myhost:443" {
		t.Fatalf("address: got %q, want %q", gotAddr, "myhost:443")
	}
}

// TestResolveTransport_GRPCScheme verifies grpc:// stripping.
func TestResolveTransport_GRPCScheme(t *testing.T) {
	gotType, gotAddr := resolveTransport("grpc://myhost:63003", "")
	if gotType != transportGRPC {
		t.Fatalf("transport: got %q, want %q", gotType, transportGRPC)
	}
	if gotAddr != "myhost:63003" {
		t.Fatalf("address: got %q, want %q", gotAddr, "myhost:63003")
	}
}

// TestResolveTransport_QUICScheme verifies quic:// stripping.
func TestResolveTransport_QUICScheme(t *testing.T) {
	gotType, gotAddr := resolveTransport("quic://myhost:63004", "")
	if gotType != transportQUIC {
		t.Fatalf("transport: got %q, want %q", gotType, transportQUIC)
	}
	if gotAddr != "myhost:63004" {
		t.Fatalf("address: got %q, want %q", gotAddr, "myhost:63004")
	}
}

// TestResolveTransport_QUICSScheme verifies quics:// stripping.
func TestResolveTransport_QUICSScheme(t *testing.T) {
	gotType, gotAddr := resolveTransport("quics://myhost:63004", "")
	if gotType != transportQUIC {
		t.Fatalf("transport: got %q, want %q", gotType, transportQUIC)
	}
	if gotAddr != "myhost:63004" {
		t.Fatalf("address: got %q, want %q", gotAddr, "myhost:63004")
	}
}

// TestResolveTransport_MCPScheme verifies mcp:// is converted to https URL.
func TestResolveTransport_MCPScheme(t *testing.T) {
	gotType, gotAddr := resolveTransport("mcp://myhost:8080", "")
	if gotType != transportMCP {
		t.Fatalf("transport: got %q, want %q", gotType, transportMCP)
	}
	if !strings.HasPrefix(gotAddr, "https://") {
		t.Fatalf("expected https:// prefix for MCP address, got: %q", gotAddr)
	}
}

// TestResolveTransport_ExplicitUnix verifies explicit unix transport uses
// the default socket when no --server is given.
func TestResolveTransport_ExplicitUnix(t *testing.T) {
	gotType, gotAddr := resolveTransport("", transportUnix)
	if gotType != transportUnix {
		t.Fatalf("transport: got %q, want %q", gotType, transportUnix)
	}
	if gotAddr != defaultUnixSocket {
		t.Fatalf("address: got %q, want %q", gotAddr, defaultUnixSocket)
	}
}

// TestResolveTransport_ExplicitQUIC verifies explicit quic transport uses
// the default QUIC address when no --server is given.
func TestResolveTransport_ExplicitQUIC(t *testing.T) {
	gotType, gotAddr := resolveTransport("", transportQUIC)
	if gotType != transportQUIC {
		t.Fatalf("transport: got %q, want %q", gotType, transportQUIC)
	}
	if gotAddr != defaultQUICAddress {
		t.Fatalf("address: got %q, want %q", gotAddr, defaultQUICAddress)
	}
}

// TestResolveTransport_ExplicitUnknown verifies that an unknown explicit
// transport falls back to the default gRPC address.
func TestResolveTransport_ExplicitUnknown(t *testing.T) {
	gotType, gotAddr := resolveTransport("", "custom-proto")
	if gotType != "custom-proto" {
		t.Fatalf("transport: got %q, want %q", gotType, "custom-proto")
	}
	if gotAddr != defaultGRPCAddress {
		t.Fatalf("address: got %q, want %q", gotAddr, defaultGRPCAddress)
	}
}

// ===========================================================================
// HasSavedConfig — UserConfigDir-based path
// ===========================================================================

// TestHasSavedConfig_WithDefaultConfigDir_NoFile verifies HasSavedConfig
// returns false when XDG_CONFIG_HOME is set but no config exists.
func TestHasSavedConfig_WithDefaultConfigDir_NoFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	log := testLogger(t)
	conn := NewConnectionBinding(log)
	esb := NewEmbeddedServerBinding(log, conn, NewSQLBinding(log, conn))

	if esb.HasSavedConfig() {
		t.Error("expected HasSavedConfig()=false before any saveConfig call")
	}
}

// TestHasSavedConfig_WithDefaultConfigDir_AfterSave verifies HasSavedConfig
// returns true after saveConfig with XDG_CONFIG_HOME set.
func TestHasSavedConfig_WithDefaultConfigDir_AfterSave(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	log := testLogger(t)
	conn := NewConnectionBinding(log)
	esb := NewEmbeddedServerBinding(log, conn, NewSQLBinding(log, conn))

	if err := esb.saveConfig(&EmbeddedConfig{StorageBackend: "memory"}); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}

	if !esb.HasSavedConfig() {
		t.Error("expected HasSavedConfig()=true after saveConfig")
	}
}

// ===========================================================================
// loadConfig — unreadable config file
// ===========================================================================

// TestLoadConfig_ReadFileError exercises the path where the config path
// exists but cannot be read as a file. loadConfig must distinguish that from
// "no config saved yet": the first is a real fault the caller has to see, the
// second is the ordinary first-run case.
//
// The fault is a directory at the config path, which os.ReadFile rejects with
// ENOTDIR-class EISDIR for every uid. A mode-0000 file would not do: CI job
// containers run as root and read it successfully, which is why this test
// previously skipped itself there and covered nothing
// (testing-guidelines.md, Fault Injection).
func TestLoadConfig_ReadFileError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	log := testLogger(t)
	conn := NewConnectionBinding(log)
	esb := NewEmbeddedServerBinding(log, conn, NewSQLBinding(log, conn))

	path, err := esb.configPath()
	if err != nil {
		t.Fatalf("configPath: %v", err)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("MkdirAll over the config path: %v", err)
	}

	_, loadErr := esb.loadConfig()
	if loadErr == nil {
		t.Fatal("expected error from loadConfig when the config path is unreadable")
	}
	var cle *ConfigLoadError
	if !errors.As(loadErr, &cle) {
		t.Fatalf("expected ConfigLoadError, got %T: %v", loadErr, loadErr)
	}
	var nsc *NoSavedConfigError
	if errors.As(loadErr, &nsc) {
		t.Fatal("an unreadable config was reported as no-saved-config")
	}
}
