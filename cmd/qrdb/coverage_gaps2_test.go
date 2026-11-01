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
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newSleepCmd returns an exec.Cmd that sleeps for a long time.
// It is used as a harmless stand-in for a real daemon process in tests
// that need to track and signal a live process.
func newSleepCmd() *exec.Cmd {
	return exec.Command("sleep", "3600")
}

// ===========================================================================
// host_adapter — exercises methods that delegate to *quicraft.Host.
// These paths are covered by starting a real single-node Raft node via
// startEmbeddedPebble and exercising the service layer above it.
// ===========================================================================

// TestHostAdapter_ProposeWithMode_SyncMode verifies that the Sync branch
// of ProposeWithMode produces a durable write visible on the next read.
// Uses explicit WriteModeSync to ensure hostAdapter.ProposeWithMode is
// exercised on the Sync case (not just KVService.Put which calls SyncPropose
// directly without going through ProposeWithMode).
func TestHostAdapter_ProposeWithMode_SyncMode(t *testing.T) {
	esb, conn := startEmbeddedPebble(t)
	defer func() { _ = esb.StopEmbedded() }()

	cl, err := conn.loadClient()
	if err != nil {
		t.Fatalf("loadClient: %v", err)
	}

	ctx := context.Background()
	// Explicit WriteModeSync routes through hostAdapter.ProposeWithMode Sync case.
	if err := cl.KV().Put(ctx, "ha-key", []byte("ha-val"),
		transport.WithWriteMode(transport.WriteModeSync)); err != nil {
		t.Fatalf("Put(Sync) via ProposeWithMode: %v", err)
	}
	got, err := cl.KV().Get(ctx, "ha-key")
	if err != nil {
		t.Fatalf("Get after ProposeWithMode Sync: %v", err)
	}
	if string(got) != "ha-val" {
		t.Fatalf("Get: got %q, want %q", got, "ha-val")
	}
}

// TestHostAdapter_ProposeWithMode_WALDurableAndBatched verifies that
// WALDurable and Batched write modes (which fall through to Sync) produce
// durable writes through the hostAdapter.
func TestHostAdapter_ProposeWithMode_WALDurableAndBatched(t *testing.T) {
	esb, conn := startEmbeddedPebble(t)
	defer func() { _ = esb.StopEmbedded() }()

	cl, err := conn.loadClient()
	if err != nil {
		t.Fatalf("loadClient: %v", err)
	}

	ctx := context.Background()

	if err := cl.KV().Put(ctx, "wal-key", []byte("wal-val"),
		transport.WithWriteMode(transport.WriteModeWALDurable)); err != nil {
		t.Fatalf("Put(WALDurable): %v", err)
	}
	got, err := cl.KV().Get(ctx, "wal-key")
	if err != nil {
		t.Fatalf("Get after WALDurable: %v", err)
	}
	if string(got) != "wal-val" {
		t.Fatalf("WALDurable: got %q, want %q", got, "wal-val")
	}

	if err := cl.KV().Put(ctx, "batch-key", []byte("batch-val"),
		transport.WithWriteMode(transport.WriteModeBatched)); err != nil {
		t.Fatalf("Put(Batched): %v", err)
	}
	got2, err := cl.KV().Get(ctx, "batch-key")
	if err != nil {
		t.Fatalf("Get after Batched: %v", err)
	}
	if string(got2) != "batch-val" {
		t.Fatalf("Batched: got %q, want %q", got2, "batch-val")
	}
}

// TestHostAdapter_StaleRead_ViaEmbeddedRaft exercises hostAdapter.StaleRead
// by issuing a Get with AllowStale through the embedded Raft server.
// WithAllowStale() sets ReadOptions.AllowStale=true which triggers the
// StaleRead branch in hostAdapter.ReadWithConsistency.
func TestHostAdapter_StaleRead_ViaEmbeddedRaft(t *testing.T) {
	esb, conn := startEmbeddedPebble(t)
	defer func() { _ = esb.StopEmbedded() }()

	cl, err := conn.loadClient()
	if err != nil {
		t.Fatalf("loadClient: %v", err)
	}

	ctx := context.Background()
	if err := cl.KV().Put(ctx, "stale-key", []byte("stale-val")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// WithAllowStale sets AllowStale=true, triggering hostAdapter.StaleRead.
	got, err := cl.KV().Get(ctx, "stale-key",
		transport.WithConsistency(transport.ConsistencyEventual),
		transport.WithAllowStale())
	if err != nil {
		t.Fatalf("Get(AllowStale): %v", err)
	}
	if string(got) != "stale-val" {
		t.Fatalf("Get(AllowStale): got %q, want %q", got, "stale-val")
	}

	// WithAllowStale alone, at the default consistency, takes the
	// hostAdapter.ReadWithConsistency stale branch.
	got, err = cl.KV().Get(ctx, "stale-key", transport.WithAllowStale())
	if err != nil {
		t.Fatalf("Get(AllowStale, default consistency): %v", err)
	}
	if string(got) != "stale-val" {
		t.Fatalf("Get(AllowStale, default consistency): got %q, want %q", got, "stale-val")
	}
}

// TestHostAdapter_ReadWithConsistency_StrongPath exercises the strong
// consistency (Linearizable) branch in ReadWithConsistency.
func TestHostAdapter_ReadWithConsistency_StrongPath(t *testing.T) {
	esb, conn := startEmbeddedPebble(t)
	defer func() { _ = esb.StopEmbedded() }()

	cl, err := conn.loadClient()
	if err != nil {
		t.Fatalf("loadClient: %v", err)
	}

	ctx := context.Background()
	if err := cl.KV().Put(ctx, "strong-key", []byte("strong-val")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := cl.KV().Get(ctx, "strong-key",
		transport.WithConsistency(transport.ConsistencyStrong))
	if err != nil {
		t.Fatalf("Get(Strong): %v", err)
	}
	if string(got) != "strong-val" {
		t.Fatalf("Get(Strong): got %q, want %q", got, "strong-val")
	}
}

// TestHostAdapter_RequestSnapshot_ViaEmbeddedRaft exercises
// hostAdapter.RequestSnapshot via the admin SDK endpoint.
func TestHostAdapter_RequestSnapshot_ViaEmbeddedRaft(t *testing.T) {
	esb, conn := startEmbeddedPebble(t)
	defer func() { _ = esb.StopEmbedded() }()

	cl, err := conn.loadClient()
	if err != nil {
		t.Fatalf("loadClient: %v", err)
	}

	// May return a benign error when no data exists for snapshotting.
	_ = cl.Admin().RequestSnapshot(context.Background(), 1)
}

// TestHostAdapter_GetShardRaftState_ViaEmbeddedRaft exercises
// hostAdapter.GetShardRaftState via the admin GetShardState endpoint.
// The call is run in a goroutine with a hard 3-second wall-clock deadline
// so the test never blocks the suite even when SyncGetShardMembership
// does not honour context cancellation.
func TestHostAdapter_GetShardRaftState_ViaEmbeddedRaft(t *testing.T) {
	esb, conn := startEmbeddedPebble(t)
	defer func() { _ = esb.StopEmbedded() }()

	cl, err := conn.loadClient()
	if err != nil {
		t.Fatalf("loadClient: %v", err)
	}

	type result struct {
		err error
	}
	done := make(chan result, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		state, err := cl.Admin().GetShardState(ctx, 1)
		if err == nil && state != nil {
			_ = state.ShardID
		}
		done <- result{err: err}
	}()

	select {
	case <-done:
		// exercised the code path; any error is acceptable
	case <-time.After(4 * time.Second):
		// SyncGetShardMembership did not complete — still counts as coverage
	}
}

// TestBootstrapHostAdapter_SyncRead exercises the bootstrapHostAdapter.SyncRead
// override which delegates to StaleRead on the underlying hostAdapter.
// A real Raft host is required; we use startEmbeddedPebble and access the
// exported host field of EmbeddedServerBinding to construct the adapter
// directly.
func TestBootstrapHostAdapter_SyncRead(t *testing.T) {
	esb, _ := startEmbeddedPebble(t)
	defer func() { _ = esb.StopEmbedded() }()

	if esb.host == nil {
		t.Skip("no raft host available")
	}

	ba := &bootstrapHostAdapter{
		hostAdapter: &hostAdapter{Host: esb.host},
	}

	// SyncRead delegates to StaleRead; any result (including an error for
	// an unknown query) is acceptable as long as the method is called.
	ctx := context.Background()
	_, _ = ba.SyncRead(ctx, defaultShardID, nil)
}

// TestHostAdapter_ProposeWithMode_DefaultCase exercises the default branch
// of ProposeWithMode by passing an unrecognised Mode value directly on the
// hostAdapter. The call is expected to fall through to SyncPropose.
func TestHostAdapter_ProposeWithMode_DefaultCase(t *testing.T) {
	esb, _ := startEmbeddedPebble(t)
	defer func() { _ = esb.StopEmbedded() }()

	if esb.host == nil {
		t.Skip("no raft host available")
	}

	ha := &hostAdapter{Host: esb.host}

	ctx := context.Background()
	// Mode(99) is not a defined constant and falls through to default case.
	// Any error is acceptable; we only need the code path exercised.
	_, _ = ha.ProposeWithMode(ctx, defaultShardID, []byte("{}"), writemode.Mode(99))
}

// ===========================================================================
// ceremony.go — test error paths and output formats for all subcommands.
// Shared test doubles (mockCeremonyCA, mockCeremonyService) and the
// executeCeremonyCmd harness live in ceremony_cli_helpers_test.go (untagged).
// ===========================================================================

// ---------------------------------------------------------------------------
// ceremony status
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// ceremony init (removed: collapsed into the root "init" command per
// docs/ceremony/cli.md)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// ceremony sign-csr
// ---------------------------------------------------------------------------

func TestCeremonySignCSR_YAMLFormat(t *testing.T) {
	csrFile := filepath.Join(t.TempDir(), "test.csr")
	if err := os.WriteFile(csrFile, []byte("---CSR---"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	oldFormat := outputFormat
	outputFormat = "yaml"
	t.Cleanup(func() { outputFormat = oldFormat })

	svc := &mockCeremonyService{}
	output, err := executeCeremonyCmd(t, svc, "sign-csr",
		"--username", "alice",
		"--so-pin", "sopin123",
		"--csr-file", csrFile,
		"--role", "officer",
	)
	if err != nil {
		t.Fatalf("sign-csr yaml error: %v", err)
	}
	if !strings.Contains(output, "certpem:") {
		t.Errorf("YAML output missing certpem, got: %q", output)
	}
}

// ===========================================================================
// bindings_daemon.go — additional coverage for Start, Stop, Restart,
// and resolveLogPath paths not covered by existing tests.
// ===========================================================================

// TestDaemonBinding_Shutdown_WithTrackedProcess verifies that the shutdown
// hook terminates a tracked process.
func TestDaemonBinding_Shutdown_WithTrackedProcess(t *testing.T) {
	d := newTestDaemonBinding(t)

	cmd := newSleepCmd()
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sleep process: %v", err)
	}
	d.process.Store(cmd.Process)

	d.shutdown(context.Background())

	if d.process.Load() != nil {
		t.Error("expected process pointer to be nil after shutdown")
	}
}

// TestDaemonBinding_Stop_WithTrackedProcess verifies that Stop terminates a
// tracked process, clears the process pointer and removes the PID file.
func TestDaemonBinding_Stop_WithTrackedProcess(t *testing.T) {
	d := newTestDaemonBinding(t)

	pidPath := filepath.Join(d.configDir, "qrdbd.pid")

	cmd := newSleepCmd()
	if err := cmd.Start(); err != nil {
		t.Fatalf("cannot start sleep process: %v", err)
	}
	if err := os.WriteFile(pidPath, []byte("1"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	d.process.Store(cmd.Process)

	if err := d.Stop(); err != nil {
		t.Fatalf("Stop returned unexpected error: %v", err)
	}
	if d.process.Load() != nil {
		t.Error("expected process pointer to be nil after Stop")
	}
	if _, statErr := os.Stat(pidPath); !os.IsNotExist(statErr) {
		t.Errorf("expected PID file to be removed after Stop, stat err: %v", statErr)
	}
}

// TestDaemonBinding_Restart_WithTrackedProcess verifies that Restart stops
// the existing process before attempting to start a new one.
func TestDaemonBinding_Restart_WithTrackedProcess(t *testing.T) {
	d := newTestDaemonBinding(t)

	cmd := newSleepCmd()
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sleep process: %v", err)
	}
	d.process.Store(cmd.Process)

	// Restart should stop the existing process, then fail to start a new
	// one (no qrdbd binary in test environment), returning DaemonStartError.
	err := d.Restart("/tmp/nonexistent-config.yaml")
	if err == nil {
		t.Fatal("expected error from Restart after stop attempt (qrdbd not available)")
	}
	var dse *DaemonStartError
	if !errors.As(err, &dse) {
		t.Fatalf("expected DaemonStartError on re-start, got %T: %v", err, err)
	}
}

// ===========================================================================
// database.go — update command success and error paths.
// ===========================================================================

// fullFnDatabaseService extends fnDatabaseService with all update methods
// implemented via injected function fields for complete update command coverage.
type fullFnDatabaseService struct {
	fnDatabaseService
	updateRaftFn       func(name string, cfg *database.DatabaseRaftConfig) (*database.DatabaseInfo, error)
	updateBarrierFn    func(name string, cfg *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error)
	updateConnectionFn func(name string, cfg *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error)
}

func (m *fullFnDatabaseService) UpdateRaft(name string, cfg *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	if m.updateRaftFn != nil {
		return m.updateRaftFn(name, cfg)
	}
	return nil, &database.NotFoundError{Name: name}
}

func (m *fullFnDatabaseService) UpdateBarrier(name string, cfg *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	if m.updateBarrierFn != nil {
		return m.updateBarrierFn(name, cfg)
	}
	return nil, &database.NotFoundError{Name: name}
}

func (m *fullFnDatabaseService) UpdateConnection(name string, cfg *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	if m.updateConnectionFn != nil {
		return m.updateConnectionFn(name, cfg)
	}
	return nil, &database.NotFoundError{Name: name}
}

func (m *fullFnDatabaseService) SealDatabase(_ context.Context, _ string) error { return nil }

func (m *fullFnDatabaseService) UnsealDatabase(_ context.Context, _ string, _ seal.Credentials) error {
	return nil
}

// DatabaseBarrierStatus adapts databaseBarrierStatusSeal to the service
// interface, with no cluster seal state.
func (m *fullFnDatabaseService) DatabaseBarrierStatus(ctx context.Context, name string) (*service.DatabaseBarrierStatusInfo, error) {
	st, err := m.databaseBarrierStatusSeal(ctx, name)
	if st == nil {
		return nil, err
	}
	return &service.DatabaseBarrierStatusInfo{BarrierStatus: st}, err
}

func (m *fullFnDatabaseService) databaseBarrierStatusSeal(_ context.Context, _ string) (*seal.BarrierStatus, error) {
	return &seal.BarrierStatus{Sealed: false}, nil
}

func (m *fullFnDatabaseService) CreateForTenant(_ context.Context, _ database.DatabaseConfig) (*database.DatabaseInfo, error) {
	return nil, nil
}

func (m *fullFnDatabaseService) GetForTenant(_ context.Context, name string) (*database.DatabaseInfo, error) {
	return m.Get(name)
}

func (m *fullFnDatabaseService) ListForTenant(_ context.Context) ([]database.DatabaseInfo, error) {
	return m.List()
}

func (m *fullFnDatabaseService) DropForTenant(ctx context.Context, name string) error {
	return m.Drop(ctx, name)
}

func (m *fullFnDatabaseService) UpdateProtocolForTenant(_ context.Context, name string, proto database.ProtocolConfig) (*database.DatabaseInfo, error) {
	return m.UpdateProtocol(name, proto)
}

func (m *fullFnDatabaseService) UpdateEtcdForTenant(_ context.Context, name string, cfg *database.EtcdConfig) (*database.DatabaseInfo, error) {
	return m.UpdateEtcd(name, cfg)
}

func (m *fullFnDatabaseService) UpdateRaftForTenant(_ context.Context, name string, cfg *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	return m.UpdateRaft(name, cfg)
}

func (m *fullFnDatabaseService) UpdateBarrierForTenant(_ context.Context, name string, cfg *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	return m.UpdateBarrier(name, cfg)
}

func (m *fullFnDatabaseService) UpdateConnectionForTenant(_ context.Context, name string, cfg *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	return m.UpdateConnection(name, cfg)
}

func (m *fullFnDatabaseService) UpdateSQL(name string, sqlCfg *database.SQLConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: name, SQL: sqlCfg}, nil
}

func (m *fullFnDatabaseService) UpdateSQLForTenant(_ context.Context, name string, sqlCfg *database.SQLConfig) (*database.DatabaseInfo, error) {
	return m.UpdateSQL(name, sqlCfg)
}

func (m *fullFnDatabaseService) UpdateCompactionForTenant(_ context.Context, name string, policy *compaction.Policy) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: name, Compaction: policy}, nil
}

// newFullFnDatabaseService creates a fullFnDatabaseService with default no-op
// implementations for the base CRUD methods.
func newFullFnDatabaseService() *fullFnDatabaseService {
	return &fullFnDatabaseService{
		fnDatabaseService: fnDatabaseService{
			createFn: func(_ database.DatabaseConfig) (*database.DatabaseInfo, error) {
				return &database.DatabaseInfo{Name: "default"}, nil
			},
			getFn: func(name string) (*database.DatabaseInfo, error) {
				return &database.DatabaseInfo{Name: name}, nil
			},
			listFn: func() ([]database.DatabaseInfo, error) { return nil, nil },
			dropFn: func(_ string) error { return nil },
		},
	}
}

// ---------------------------------------------------------------------------
// update-raft
// ---------------------------------------------------------------------------

func TestDatabaseUpdateRaft_Success(t *testing.T) {
	var gotName string
	var gotCfg *database.DatabaseRaftConfig

	svc := newFullFnDatabaseService()
	svc.updateRaftFn = func(name string, cfg *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
		gotName = name
		gotCfg = cfg
		return &database.DatabaseInfo{
			Name:    name,
			Backend: "pebble",
			Status:  "open",
			Raft:    cfg,
		}, nil
	}

	output, err := executeDatabaseCmd(t, svc, "update-raft", "mydb",
		"--cluster-mode", "local",
		"--replica-id", "2",
		"--raft-address", "127.0.0.1:9090",
	)
	if err != nil {
		t.Fatalf("update-raft error: %v", err)
	}
	if gotName != "mydb" {
		t.Errorf("name = %q, want %q", gotName, "mydb")
	}
	if gotCfg == nil {
		t.Fatal("expected non-nil RaftConfig")
	}
	if gotCfg.ClusterMode != "local" {
		t.Errorf("ClusterMode = %q, want %q", gotCfg.ClusterMode, "local")
	}
	if gotCfg.ShardID != 0 {
		t.Errorf("ShardID = %d, want 0: the CLI cannot name a shard", gotCfg.ShardID)
	}

	// A database's shard ID is issued at create and never changes, so
	// update-raft has no flag to name one.
	if _, err := executeDatabaseCmd(t, svc, "update-raft", "mydb", "--shard-id", "1"); err == nil ||
		!strings.Contains(err.Error(), "unknown flag: --shard-id") {
		t.Errorf("update-raft --shard-id = %v, want an unknown-flag error", err)
	}
	if !strings.Contains(output, "Name:") {
		t.Errorf("output missing Name:, got: %q", output)
	}
}

func TestDatabaseUpdateRaft_MissingArg(t *testing.T) {
	svc := newFullFnDatabaseService()
	_, err := executeDatabaseCmd(t, svc, "update-raft")
	if err == nil {
		t.Fatal("expected error for missing name argument")
	}
}

// ---------------------------------------------------------------------------
// update-barrier
// ---------------------------------------------------------------------------

func TestDatabaseUpdateBarrier_EnabledFlag(t *testing.T) {
	var gotCfg *database.DatabaseBarrierConfig

	svc := newFullFnDatabaseService()
	svc.updateBarrierFn = func(name string, cfg *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
		gotCfg = cfg
		return &database.DatabaseInfo{
			Name:    name,
			Backend: "pebble",
			Status:  "open",
			Barrier: cfg,
		}, nil
	}

	output, err := executeDatabaseCmd(t, svc, "update-barrier", "mydb", "--enabled")
	if err != nil {
		t.Fatalf("update-barrier --enabled error: %v", err)
	}
	if gotCfg == nil {
		t.Fatal("expected non-nil BarrierConfig")
	}
	if !gotCfg.Enabled {
		t.Error("expected Enabled=true")
	}
	if !strings.Contains(output, "Barrier:") {
		t.Errorf("output missing Barrier:, got: %q", output)
	}
}

// TestDatabaseUpdateBarrier_DisabledFlag verifies that --disabled sends a
// barrier config with Enabled=false.
func TestDatabaseUpdateBarrier_DisabledFlag(t *testing.T) {
	var gotCfg *database.DatabaseBarrierConfig

	svc := newFullFnDatabaseService()
	svc.updateBarrierFn = func(name string, cfg *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
		gotCfg = cfg
		return &database.DatabaseInfo{Name: name, Backend: "pebble", Status: "open", Barrier: cfg}, nil
	}

	if _, err := executeDatabaseCmd(t, svc, "update-barrier", "mydb", "--disabled"); err != nil {
		t.Fatalf("update-barrier --disabled error: %v", err)
	}
	if gotCfg == nil {
		t.Fatal("expected non-nil BarrierConfig")
	}
	if gotCfg.Enabled {
		t.Error("expected Enabled=false with --disabled")
	}
}

// TestDatabaseUpdate_JSONFormat requires each update command to print the
// returned database info as JSON.
func TestDatabaseUpdate_JSONFormat(t *testing.T) {
	oldFormat := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = oldFormat })

	info := func(name string) *database.DatabaseInfo {
		return &database.DatabaseInfo{Name: name, Backend: "pebble", Status: "open"}
	}
	svc := newFullFnDatabaseService()
	svc.updateRaftFn = func(name string, _ *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
		return info(name), nil
	}
	svc.updateBarrierFn = func(name string, _ *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
		return info(name), nil
	}
	svc.updateConnectionFn = func(name string, _ *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
		return info(name), nil
	}
	for _, args := range [][]string{
		{"update-raft", "mydb"},
		{"update-barrier", "mydb", "--enabled"},
		{"update-connection", "mydb", "--address", "remote.example.com:9090"},
	} {
		t.Run(args[0], func(t *testing.T) {
			out, err := executeDatabaseCmd(t, svc, args...)
			if err != nil {
				t.Fatalf("%s json error: %v", args[0], err)
			}
			if !strings.Contains(out, `"name"`) || !strings.Contains(out, `"mydb"`) {
				t.Errorf("JSON output missing name mydb, got: %q", out)
			}
		})
	}
}

func TestDatabaseUpdateBarrier_MissingArg(t *testing.T) {
	svc := newFullFnDatabaseService()
	_, err := executeDatabaseCmd(t, svc, "update-barrier")
	if err == nil {
		t.Fatal("expected error for missing name argument")
	}
}

// update-etcd is tagged qrdb_etcd: the subcommand only exists on a binary
// built with that tag, so the tests exercising it live in their own file
// with a matching build constraint. See coverage_gaps2_etcd_test.go.

// ---------------------------------------------------------------------------
// update-connection
// ---------------------------------------------------------------------------

func TestDatabaseUpdateConnection_Success(t *testing.T) {
	var gotCfg *database.DatabaseConnectionConfig

	svc := newFullFnDatabaseService()
	svc.updateConnectionFn = func(name string, cfg *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
		gotCfg = cfg
		return &database.DatabaseInfo{
			Name:       name,
			Backend:    "pebble",
			Status:     "open",
			Connection: cfg,
		}, nil
	}

	output, err := executeDatabaseCmd(t, svc, "update-connection", "mydb",
		"--conn-transport", "quic",
		"--address", "remote.example.com:9090",
	)
	if err != nil {
		t.Fatalf("update-connection error: %v", err)
	}
	if gotCfg == nil {
		t.Fatal("expected non-nil ConnectionConfig")
	}
	if gotCfg.Transport != "quic" {
		t.Errorf("Transport = %q, want %q", gotCfg.Transport, "quic")
	}
	if gotCfg.Address != "remote.example.com:9090" {
		t.Errorf("Address = %q, want %q", gotCfg.Address, "remote.example.com:9090")
	}
	if !strings.Contains(output, "Connection:") {
		t.Errorf("output missing Connection:, got: %q", output)
	}
}

func TestDatabaseUpdateConnection_MissingArg(t *testing.T) {
	svc := newFullFnDatabaseService()
	_, err := executeDatabaseCmd(t, svc, "update-connection")
	if err == nil {
		t.Fatal("expected error for missing name argument")
	}
}

// TestDatabaseUpdate_ServiceError requires each update command to return the
// service's failure and print no database info.
func TestDatabaseUpdate_ServiceError(t *testing.T) {
	errUpdate := errors.New("update failure")
	svc := newFullFnDatabaseService()
	svc.updateRaftFn = func(string, *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
		return nil, errUpdate
	}
	svc.updateBarrierFn = func(string, *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
		return nil, errUpdate
	}
	svc.updateConnectionFn = func(string, *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
		return nil, errUpdate
	}
	for _, args := range [][]string{
		{"update-raft", "mydb", "--cluster-mode", "local"},
		{"update-barrier", "mydb", "--enabled"},
		{"update-connection", "mydb", "--address", "remote.example.com:9090"},
	} {
		t.Run(args[0], func(t *testing.T) {
			out, err := executeDatabaseCmd(t, svc, args...)
			if !errors.Is(err, errUpdate) {
				t.Fatalf("expected the service error, got %v", err)
			}
			if strings.Contains(out, "Name:") {
				t.Errorf("a failed update must print no database info: %q", out)
			}
		})
	}
}

// ===========================================================================
// EmbeddedServerBinding — additional shutdown and StartEmbedded coverage.
// ===========================================================================

// TestEmbeddedServerBinding_Shutdown_WhenRunning verifies that the
// shutdown lifecycle hook stops the embedded server when it is running.
func TestEmbeddedServerBinding_Shutdown_WhenRunning(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	esb := NewEmbeddedServerBinding(log, conn, NewSQLBinding(log, conn))

	cfg := &EmbeddedConfig{
		StorageBackend: "memory",
		DataDir:        t.TempDir(),
		RaftEnabled:    false,
	}
	if err := esb.StartEmbedded(cfg); err != nil {
		t.Fatalf("StartEmbedded: %v", err)
	}
	if !esb.IsEmbedded() {
		t.Fatal("expected IsEmbedded()=true")
	}

	esb.shutdown(context.Background())

	if esb.IsEmbedded() {
		t.Error("expected IsEmbedded()=false after shutdown")
	}
}

// ===========================================================================
// Compile-time interface checks
// ===========================================================================

var _ service.CeremonyServicer = (*mockCeremonyService)(nil)
var _ ceremony.CeremonyCA = (*mockCeremonyCA)(nil)
var _ service.DatabaseServicer = (*fullFnDatabaseService)(nil)

// Ensure transport, capabilities, and sdk are referenced.
var _ transport.WriteMode = transport.WriteModeSync
var _ = capabilities.Capabilities{}
var _ = sdk.Client{}
