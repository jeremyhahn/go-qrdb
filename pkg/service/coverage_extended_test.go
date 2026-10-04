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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package service

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newTestDatabaseServiceWithOpts creates a DatabaseService backed by a real
// Manager, with optional functional options applied.
func newTestDatabaseServiceWithOpts(t *testing.T, opts ...DatabaseServiceOption) DatabaseServicer {
	t.Helper()
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := database.NewManager(logger, dataDir)
	t.Cleanup(func() { _ = mgr.Close() })
	return NewDatabaseService(mgr, opts...)
}

// ---------------------------------------------------------------------------
// errors.go - import/barrier error types (0% covered)
// ---------------------------------------------------------------------------

func TestImportInvalidFormatError_Message(t *testing.T) {
	err := &ImportInvalidFormatError{Format: "parquet"}
	want := "service: invalid import format: parquet (expected csv, json, yaml, ndjson, archive, or auto)"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}

func TestImportInvalidFormatError_ErrorsAs(t *testing.T) {
	var err error = &ImportInvalidFormatError{Format: "xml"}
	var target *ImportInvalidFormatError
	if !errors.As(err, &target) {
		t.Fatal("expected errors.As to match *ImportInvalidFormatError")
	}
	if target.Format != "xml" {
		t.Fatalf("expected Format 'xml', got %q", target.Format)
	}
}

func TestImportMissingKeyFieldError_Message(t *testing.T) {
	err := &ImportMissingKeyFieldError{}
	want := "service: import keyField is required"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}

func TestImportInvalidValueModeError_Message(t *testing.T) {
	err := &ImportInvalidValueModeError{Mode: "column"}
	want := "service: invalid import valueMode: column (expected field or row)"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}

func TestImportInvalidValueModeError_ErrorsAs(t *testing.T) {
	var err error = &ImportInvalidValueModeError{Mode: "col"}
	var target *ImportInvalidValueModeError
	if !errors.As(err, &target) {
		t.Fatal("expected errors.As to match *ImportInvalidValueModeError")
	}
	if target.Mode != "col" {
		t.Fatalf("expected Mode 'col', got %q", target.Mode)
	}
}

func TestImportMissingValueFieldError_Message(t *testing.T) {
	err := &ImportMissingValueFieldError{}
	want := "service: import valueField is required when valueMode is 'field'"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}

func TestImportParseError_Message(t *testing.T) {
	inner := errors.New("unexpected EOF")
	err := &ImportParseError{Err: inner}
	want := "service: import parse failed: unexpected EOF"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}

func TestImportParseError_Unwrap(t *testing.T) {
	inner := errors.New("parse issue")
	err := &ImportParseError{Err: inner}
	if !errors.Is(err, inner) {
		t.Fatal("expected errors.Is to find inner via Unwrap")
	}
}

func TestImportTransactionAbortedError_Message(t *testing.T) {
	err := &ImportTransactionAbortedError{Row: 5, Key: "k5", Message: "key not found"}
	want := "service: import aborted at row 5 (key=k5): key not found"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}

func TestImportTransactionAbortedError_ErrorsAs(t *testing.T) {
	var err error = &ImportTransactionAbortedError{Row: 2, Key: "k", Message: "msg"}
	var target *ImportTransactionAbortedError
	if !errors.As(err, &target) {
		t.Fatal("expected errors.As to match *ImportTransactionAbortedError")
	}
	if target.Row != 2 {
		t.Fatalf("expected Row=2, got %d", target.Row)
	}
}

func TestImportArchiveError_Message(t *testing.T) {
	inner := errors.New("zip: not a valid zip file")
	err := &ImportArchiveError{Err: inner}
	want := "service: import archive extraction failed: zip: not a valid zip file"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}

func TestImportArchiveError_Unwrap(t *testing.T) {
	inner := errors.New("corrupt archive")
	err := &ImportArchiveError{Err: inner}
	if !errors.Is(err, inner) {
		t.Fatal("expected errors.Is to find inner via Unwrap")
	}
}

func TestBarrierManagerNotConfiguredError_Message(t *testing.T) {
	err := &BarrierManagerNotConfiguredError{}
	want := "service: database barrier manager not configured"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}

func TestBarrierManagerNotConfiguredError_ErrorsAs(t *testing.T) {
	var err error = &BarrierManagerNotConfiguredError{}
	var target *BarrierManagerNotConfiguredError
	if !errors.As(err, &target) {
		t.Fatal("expected errors.As to match *BarrierManagerNotConfiguredError")
	}
}

// ---------------------------------------------------------------------------
// DatabaseService - WithBarrierManager option (0% covered)
// ---------------------------------------------------------------------------

func TestWithBarrierManager_Option(t *testing.T) {
	// Calling NewDatabaseService with a nil BarrierManager option exercises
	// WithBarrierManager, which was at 0% coverage.
	svc := newTestDatabaseServiceWithOpts(t, WithBarrierManager(nil))
	if svc == nil {
		t.Fatal("expected non-nil service")
	}
	// SealDatabase on the caller's own database should still return
	// BarrierManagerNotConfiguredError since we passed nil.
	if _, err := svc.Create(testDBConfig("db")); err != nil {
		t.Fatal(err)
	}
	err := svc.SealDatabase(context.Background(), "db")
	var bmErr *BarrierManagerNotConfiguredError
	if !errors.As(err, &bmErr) {
		t.Fatalf("expected *BarrierManagerNotConfiguredError, got %T: %v", err, bmErr)
	}
}

// ---------------------------------------------------------------------------
// DatabaseService - UpdateEtcd (0% covered)
// ---------------------------------------------------------------------------

func TestDatabaseService_UpdateEtcd_Success(t *testing.T) {
	svc := newTestDatabaseService(t)

	if _, err := svc.Create(testDBConfig("etcd-db")); err != nil {
		t.Fatalf("Create() error: %v", err)
	}

	etcdCfg := &database.EtcdConfig{
		Enabled:    true,
		ListenAddr: "localhost:2379",
	}
	info, err := svc.UpdateEtcd("etcd-db", etcdCfg)
	if err != nil {
		t.Fatalf("UpdateEtcd() unexpected error: %v", err)
	}
	if info.Name != "etcd-db" {
		t.Errorf("UpdateEtcd().Name = %q, want %q", info.Name, "etcd-db")
	}
}

func TestDatabaseService_UpdateEtcd_NotFound(t *testing.T) {
	svc := newTestDatabaseService(t)

	_, err := svc.UpdateEtcd("phantom-db", &database.EtcdConfig{})
	if err == nil {
		t.Fatal("UpdateEtcd() returned nil, want error")
	}
	var notFound *database.NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected *database.NotFoundError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// DatabaseService - UnsealDatabase (0% covered)
// ---------------------------------------------------------------------------

func TestDatabaseService_UnsealDatabase_NoBarrierManager(t *testing.T) {
	svc := newTestDatabaseService(t)
	if _, err := svc.Create(testDBConfig("db")); err != nil {
		t.Fatal(err)
	}

	err := svc.UnsealDatabase(context.Background(), "db", seal.Credentials{})
	if err == nil {
		t.Fatal("UnsealDatabase() returned nil, want *BarrierManagerNotConfiguredError")
	}
	var bmErr *BarrierManagerNotConfiguredError
	if !errors.As(err, &bmErr) {
		t.Fatalf("expected *BarrierManagerNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// DatabaseService - DatabaseBarrierStatus (66% covered - add nil-barrierMgr path)
// ---------------------------------------------------------------------------

func TestDatabaseService_DatabaseBarrierStatus_NilBarrierMgrTyped(t *testing.T) {
	// Use the typed struct directly to access unexported field.
	svc := newTestDatabaseService(t)
	if _, err := svc.Create(testDBConfig("db")); err != nil {
		t.Fatal(err)
	}
	s, ok := svc.(*DatabaseService)
	if !ok || s.barrierMgr != nil {
		t.Fatal("the test needs a *DatabaseService with no barrier manager")
	}
	_, err := s.DatabaseBarrierStatus(context.Background(), "db")
	var bmErr *BarrierManagerNotConfiguredError
	if !errors.As(err, &bmErr) {
		t.Fatalf("expected *BarrierManagerNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// DatabaseService - Tenant-scoped operations (all 0% covered)
// ---------------------------------------------------------------------------

func TestDatabaseService_CreateForTenant_Success(t *testing.T) {
	svc := newTestDatabaseService(t)
	ctx := tenantCtx("tenant-alpha")

	info, err := svc.CreateForTenant(ctx, testDBConfig("t-create-db"))
	if err != nil {
		t.Fatalf("CreateForTenant() error: %v", err)
	}
	if info.Name != "t-create-db" {
		t.Errorf("Name = %q, want %q", info.Name, "t-create-db")
	}
}

func TestDatabaseService_CreateForTenant_SystemTenantRoutesToAdmin(t *testing.T) {
	svc := newTestDatabaseService(t)

	// A system/no-tenant context routes to the admin (cross-tenant) path,
	// which is not ownership-restricted, so the create succeeds.
	info, err := svc.CreateForTenant(context.Background(), testDBConfig("sys-create-db"))
	if err != nil {
		t.Fatalf("CreateForTenant() as system error: %v", err)
	}
	if info.Name != "sys-create-db" {
		t.Errorf("Name = %q, want %q", info.Name, "sys-create-db")
	}
}

func TestDatabaseService_GetForTenant_Success(t *testing.T) {
	svc := newTestDatabaseService(t)
	ctx := tenantCtx("tenant-beta")

	if _, err := svc.CreateForTenant(ctx, testDBConfig("t-get-db")); err != nil {
		t.Fatalf("CreateForTenant() error: %v", err)
	}

	info, err := svc.GetForTenant(ctx, "t-get-db")
	if err != nil {
		t.Fatalf("GetForTenant() error: %v", err)
	}
	if info.Name != "t-get-db" {
		t.Errorf("Name = %q, want %q", info.Name, "t-get-db")
	}
}

func TestDatabaseService_GetForTenant_SystemTenantRoutesToAdmin(t *testing.T) {
	svc := newTestDatabaseService(t)

	// System context routes to the admin path: create then get succeed.
	if _, err := svc.CreateForTenant(context.Background(), testDBConfig("sys-get-db")); err != nil {
		t.Fatalf("CreateForTenant() as system error: %v", err)
	}
	info, err := svc.GetForTenant(context.Background(), "sys-get-db")
	if err != nil {
		t.Fatalf("GetForTenant() as system error: %v", err)
	}
	if info.Name != "sys-get-db" {
		t.Errorf("Name = %q, want %q", info.Name, "sys-get-db")
	}
}

func TestDatabaseService_ListForTenant_Success(t *testing.T) {
	svc := newTestDatabaseService(t)
	ctx := tenantCtx("tenant-gamma")

	if _, err := svc.CreateForTenant(ctx, testDBConfig("t-list-db1")); err != nil {
		t.Fatalf("CreateForTenant() error: %v", err)
	}
	if _, err := svc.CreateForTenant(ctx, testDBConfig("t-list-db2")); err != nil {
		t.Fatalf("CreateForTenant() error: %v", err)
	}

	list, err := svc.ListForTenant(ctx)
	if err != nil {
		t.Fatalf("ListForTenant() error: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 databases, got %d", len(list))
	}
}

func TestDatabaseService_ListForTenant_SystemTenantRoutesToAdmin(t *testing.T) {
	svc := newTestDatabaseService(t)

	// System context routes to the admin (cross-tenant) list, which succeeds.
	if _, err := svc.CreateForTenant(context.Background(), testDBConfig("sys-list-db")); err != nil {
		t.Fatalf("CreateForTenant() as system error: %v", err)
	}
	list, err := svc.ListForTenant(context.Background())
	if err != nil {
		t.Fatalf("ListForTenant() as system error: %v", err)
	}
	if len(list) == 0 {
		t.Fatal("ListForTenant() as system returned no databases, want >= 1")
	}
}

func TestDatabaseService_DropForTenant_Success(t *testing.T) {
	svc := newTestDatabaseService(t)
	ctx := tenantCtx("tenant-delta")

	if _, err := svc.CreateForTenant(ctx, testDBConfig("t-drop-db")); err != nil {
		t.Fatalf("CreateForTenant() error: %v", err)
	}

	if err := svc.DropForTenant(ctx, "t-drop-db"); err != nil {
		t.Fatalf("DropForTenant() error: %v", err)
	}
}

func TestDatabaseService_DropForTenant_SystemTenantRoutesToAdmin(t *testing.T) {
	svc := newTestDatabaseService(t)

	// System context routes to the admin path: create then drop succeed.
	if _, err := svc.CreateForTenant(context.Background(), testDBConfig("sys-drop-db")); err != nil {
		t.Fatalf("CreateForTenant() as system error: %v", err)
	}
	if err := svc.DropForTenant(context.Background(), "sys-drop-db"); err != nil {
		t.Fatalf("DropForTenant() as system error: %v", err)
	}
}

func TestDatabaseService_UpdateProtocolForTenant_Success(t *testing.T) {
	svc := newTestDatabaseService(t)
	ctx := tenantCtx("tenant-proto")

	if _, err := svc.CreateForTenant(ctx, testDBConfig("t-proto-db")); err != nil {
		t.Fatalf("CreateForTenant() error: %v", err)
	}

	info, err := svc.UpdateProtocolForTenant(ctx, "t-proto-db", database.ProtocolConfig{MySQL: true})
	if err != nil {
		t.Fatalf("UpdateProtocolForTenant() error: %v", err)
	}
	if info.Name != "t-proto-db" {
		t.Errorf("Name = %q, want %q", info.Name, "t-proto-db")
	}
	if !info.Protocol.MySQL {
		t.Error("Protocol.MySQL = false, want true")
	}
}

func TestDatabaseService_UpdateProtocolForTenant_SystemTenantRoutesToAdmin(t *testing.T) {
	svc := newTestDatabaseService(t)

	if _, err := svc.CreateForTenant(context.Background(), testDBConfig("sys-proto-db")); err != nil {
		t.Fatalf("CreateForTenant() as system error: %v", err)
	}
	info, err := svc.UpdateProtocolForTenant(context.Background(), "sys-proto-db", database.ProtocolConfig{MySQL: true})
	if err != nil {
		t.Fatalf("UpdateProtocolForTenant() as system error: %v", err)
	}
	if !info.Protocol.MySQL {
		t.Error("Protocol.MySQL = false, want true")
	}
}

func TestDatabaseService_UpdateEtcdForTenant_Success(t *testing.T) {
	svc := newTestDatabaseService(t)
	ctx := tenantCtx("tenant-etcd")

	if _, err := svc.CreateForTenant(ctx, testDBConfig("t-etcd-db")); err != nil {
		t.Fatalf("CreateForTenant() error: %v", err)
	}

	etcdCfg := &database.EtcdConfig{
		Enabled:    true,
		ListenAddr: "localhost:2379",
	}
	info, err := svc.UpdateEtcdForTenant(ctx, "t-etcd-db", etcdCfg)
	if err != nil {
		t.Fatalf("UpdateEtcdForTenant() error: %v", err)
	}
	if info.Name != "t-etcd-db" {
		t.Errorf("Name = %q, want %q", info.Name, "t-etcd-db")
	}
}

func TestDatabaseService_UpdateEtcdForTenant_SystemTenantRoutesToAdmin(t *testing.T) {
	svc := newTestDatabaseService(t)

	if _, err := svc.CreateForTenant(context.Background(), testDBConfig("sys-etcd-db")); err != nil {
		t.Fatalf("CreateForTenant() as system error: %v", err)
	}
	info, err := svc.UpdateEtcdForTenant(context.Background(), "sys-etcd-db", &database.EtcdConfig{
		Enabled:    true,
		ListenAddr: "localhost:2379",
	})
	if err != nil {
		t.Fatalf("UpdateEtcdForTenant() as system error: %v", err)
	}
	if info.Name != "sys-etcd-db" {
		t.Errorf("Name = %q, want %q", info.Name, "sys-etcd-db")
	}
}

func TestDatabaseService_UpdateRaftForTenant_Success(t *testing.T) {
	svc := newTestDatabaseService(t)
	ctx := tenantCtx("tenant-raft")

	if _, err := svc.CreateForTenant(ctx, testDBConfig("t-raft-db")); err != nil {
		t.Fatalf("CreateForTenant() error: %v", err)
	}

	raftCfg := &database.DatabaseRaftConfig{
		ClusterMode: database.RaftLocal,
		ReplicaID:   1,
	}
	info, err := svc.UpdateRaftForTenant(ctx, "t-raft-db", raftCfg)
	if err != nil {
		t.Fatalf("UpdateRaftForTenant() error: %v", err)
	}
	if info.Name != "t-raft-db" {
		t.Errorf("Name = %q, want %q", info.Name, "t-raft-db")
	}
}

func TestDatabaseService_UpdateRaftForTenant_SystemTenantRoutesToAdmin(t *testing.T) {
	svc := newTestDatabaseService(t)

	if _, err := svc.CreateForTenant(context.Background(), testDBConfig("sys-raft-db")); err != nil {
		t.Fatalf("CreateForTenant() as system error: %v", err)
	}
	info, err := svc.UpdateRaftForTenant(context.Background(), "sys-raft-db", &database.DatabaseRaftConfig{
		ClusterMode: database.RaftLocal,
		ReplicaID:   1,
	})
	if err != nil {
		t.Fatalf("UpdateRaftForTenant() as system error: %v", err)
	}
	if info.Name != "sys-raft-db" {
		t.Errorf("Name = %q, want %q", info.Name, "sys-raft-db")
	}
}

// TestDatabaseService_UpdateBarrierForTenant_Success verifies tenant-scoped
// routing for UpdateBarrierForTenant. Enabling a barrier for the first time
// via UpdateBarrier is a re-encryption migration this API does not perform
// (database.BarrierMigrationNotSupportedError), so the database is created
// WITH its barrier already active; the update below exercises the one safe
// transition UpdateBarrier does perform: re-affirming an already-active
// barrier (refreshing non-secret bookkeeping). A barrier is only accepted on
// a non-raft store of the database's own, which the tenant-scoped create path
// never builds from caller input, so the tenant's database is provisioned
// through the Manager with its owner and DataDir set.
func TestDatabaseService_UpdateBarrierForTenant_Success(t *testing.T) {
	svc, mgr := newTestDatabaseServiceWithBarrier(t)
	ctx := tenantCtx("tenant-barrier")

	cfg := testDBConfig("t-barrier-db")
	cfg.TenantID = "tenant-barrier"
	cfg.DataDir = t.TempDir()
	cfg.Barrier = &database.DatabaseBarrierConfig{Enabled: true, Mode: database.BarrierModePassphrase}
	if _, err := mgr.CreateWithCredentials(context.Background(), cfg, seal.MustCredentials(context.Background(), []byte("s3cret"))); err != nil {
		t.Fatalf("CreateWithCredentials() error: %v", err)
	}

	barrierCfg := &database.DatabaseBarrierConfig{Enabled: true, Mode: database.BarrierModePassphrase}
	info, err := svc.UpdateBarrierForTenant(ctx, "t-barrier-db", barrierCfg)
	if err != nil {
		t.Fatalf("UpdateBarrierForTenant() error: %v", err)
	}
	if info.Name != "t-barrier-db" {
		t.Errorf("Name = %q, want %q", info.Name, "t-barrier-db")
	}
	if info.Barrier == nil || !info.Barrier.Enabled {
		t.Error("Barrier.Enabled = false, want true")
	}
}

// TestDatabaseService_UpdateBarrierForTenant_SystemTenantRoutesToAdmin
// verifies that an unscoped (system-tenant) context routes
// UpdateBarrierForTenant to the admin path. As above, the database is
// created WITH its barrier already active (enabling via UpdateBarrier is
// not supported) and the update re-affirms that active state.
func TestDatabaseService_UpdateBarrierForTenant_SystemTenantRoutesToAdmin(t *testing.T) {
	svc, _ := newTestDatabaseServiceWithBarrier(t)

	cfg := testDBConfig("sys-barrier-db")
	cfg.DataDir = t.TempDir()
	cfg.Barrier = &database.DatabaseBarrierConfig{Enabled: true, Mode: database.BarrierModePassphrase}
	if _, err := svc.CreateForTenantWithCredentials(context.Background(), cfg, seal.MustCredentials(context.Background(), []byte("s3cret"))); err != nil {
		t.Fatalf("CreateForTenantWithCredentials() as system error: %v", err)
	}
	info, err := svc.UpdateBarrierForTenant(context.Background(), "sys-barrier-db", &database.DatabaseBarrierConfig{
		Enabled: true,
		Mode:    database.BarrierModePassphrase,
	})
	if err != nil {
		t.Fatalf("UpdateBarrierForTenant() as system error: %v", err)
	}
	if info.Barrier == nil || !info.Barrier.Enabled {
		t.Error("Barrier.Enabled = false, want true")
	}
}

func TestDatabaseService_UpdateConnectionForTenant_Success(t *testing.T) {
	svc := newTestDatabaseService(t)
	ctx := tenantCtx("tenant-conn")

	if _, err := svc.CreateForTenant(ctx, testDBConfig("t-conn-db")); err != nil {
		t.Fatalf("CreateForTenant() error: %v", err)
	}

	connCfg := &database.DatabaseConnectionConfig{
		Transport: "grpc",
		Address:   "localhost:9090",
		CAFile:    "/certs/ca.pem",
	}
	info, err := svc.UpdateConnectionForTenant(ctx, "t-conn-db", connCfg)
	if err != nil {
		t.Fatalf("UpdateConnectionForTenant() error: %v", err)
	}
	if info.Name != "t-conn-db" {
		t.Errorf("Name = %q, want %q", info.Name, "t-conn-db")
	}
	if info.Connection == nil || info.Connection.Transport != "grpc" {
		t.Error("Connection.Transport != grpc")
	}
}

func TestDatabaseService_UpdateConnectionForTenant_SystemTenantRoutesToAdmin(t *testing.T) {
	svc := newTestDatabaseService(t)

	if _, err := svc.CreateForTenant(context.Background(), testDBConfig("sys-conn-db")); err != nil {
		t.Fatalf("CreateForTenant() as system error: %v", err)
	}
	info, err := svc.UpdateConnectionForTenant(context.Background(), "sys-conn-db", &database.DatabaseConnectionConfig{
		Transport: "grpc",
		Address:   "localhost:9090",
	})
	if err != nil {
		t.Fatalf("UpdateConnectionForTenant() as system error: %v", err)
	}
	if info.Connection == nil || info.Connection.Transport != "grpc" {
		t.Error("Connection.Transport != grpc")
	}
}

// ---------------------------------------------------------------------------
// BatchService - ExecuteWithMode (0% covered)
// ---------------------------------------------------------------------------

func TestBatchService_ExecuteWithMode_WritePutOnly(t *testing.T) {
	var capturedMode writemode.Mode
	host := &mockHost{
		proposeWithModeFn: func(_ context.Context, _ uint64, _ []byte, mode writemode.Mode) (sm.Result, error) {
			capturedMode = mode
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewBatchService(host, 1)
	ops := []Op{
		{Type: OpPut, Key: []byte("k1"), Value: []byte("v1")},
		{Type: OpPut, Key: []byte("k2"), Value: []byte("v2")},
	}

	responses, err := svc.ExecuteWithMode(context.Background(), ops, writemode.WALDurable)
	if err != nil {
		t.Fatalf("ExecuteWithMode() unexpected error: %v", err)
	}
	if len(responses) != 2 {
		t.Fatalf("expected 2 responses, got %d", len(responses))
	}
	for i, r := range responses {
		if !r.Succeeded {
			t.Fatalf("response[%d].Succeeded = false, want true", i)
		}
	}
	if capturedMode != writemode.WALDurable {
		t.Fatalf("expected Relaxed mode, got %v", capturedMode)
	}
}

func TestBatchService_ExecuteWithMode_WriteDeleteOnly(t *testing.T) {
	host := &mockHost{
		proposeWithModeFn: func(_ context.Context, _ uint64, _ []byte, _ writemode.Mode) (sm.Result, error) {
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewBatchService(host, 1)
	ops := []Op{
		{Type: OpDelete, Key: []byte("k1")},
	}

	responses, err := svc.ExecuteWithMode(context.Background(), ops, writemode.Sync)
	if err != nil {
		t.Fatalf("ExecuteWithMode() unexpected error: %v", err)
	}
	if len(responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(responses))
	}
	if !responses[0].Succeeded {
		t.Fatal("response[0].Succeeded = false, want true")
	}
}

func TestBatchService_ExecuteWithMode_ReadOpGet(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			return &KeyValue{Key: []byte("k1"), Value: []byte("v1")}, nil
		},
	}

	svc := NewBatchService(host, 1)
	ops := []Op{
		{Type: OpGet, Key: []byte("k1")},
	}

	responses, err := svc.ExecuteWithMode(context.Background(), ops, writemode.WALDurable)
	if err != nil {
		t.Fatalf("ExecuteWithMode() unexpected error: %v", err)
	}
	if len(responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(responses))
	}
	if !responses[0].Succeeded {
		t.Fatal("response[0].Succeeded = false, want true")
	}
	if len(responses[0].Kvs) != 1 {
		t.Fatalf("expected 1 kv, got %d", len(responses[0].Kvs))
	}
}

func TestBatchService_ExecuteWithMode_ReadOpRange(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			return &MVCCRangeResult{
				KeyValues: []*KeyValue{
					{Key: []byte("k1"), Value: []byte("v1")},
					{Key: []byte("k2"), Value: []byte("v2")},
				},
			}, nil
		},
	}

	svc := NewBatchService(host, 1)
	ops := []Op{
		{Type: OpRange, Key: []byte("k"), RangeEnd: []byte("z")},
	}

	responses, err := svc.ExecuteWithMode(context.Background(), ops, writemode.WALDurable)
	if err != nil {
		t.Fatalf("ExecuteWithMode() unexpected error: %v", err)
	}
	if len(responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(responses))
	}
	if responses[0].Count != 2 {
		t.Fatalf("expected Count=2, got %d", responses[0].Count)
	}
}

func TestBatchService_ExecuteWithMode_ProposeError(t *testing.T) {
	sentinel := errors.New("raft timeout")
	host := &mockHost{
		proposeWithModeFn: func(_ context.Context, _ uint64, _ []byte, _ writemode.Mode) (sm.Result, error) {
			return sm.Result{}, sentinel
		},
	}

	svc := NewBatchService(host, 1)
	ops := []Op{
		{Type: OpPut, Key: []byte("k"), Value: []byte("v")},
	}

	_, err := svc.ExecuteWithMode(context.Background(), ops, writemode.Sync)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var pe *ProposeError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProposeError, got %T: %v", err, err)
	}
}

func TestBatchService_ExecuteWithMode_NotLeaderError(t *testing.T) {
	host := &mockHost{
		proposeWithModeFn: func(_ context.Context, _ uint64, _ []byte, _ writemode.Mode) (sm.Result, error) {
			return sm.Result{}, &quicraft.NotLeaderError{
				ShardID:       1,
				LeaderID:      2,
				LeaderAddress: "10.0.0.2:9100",
			}
		},
	}

	svc := NewBatchService(host, 1)
	ops := []Op{
		{Type: OpPut, Key: []byte("k"), Value: []byte("v")},
	}

	_, err := svc.ExecuteWithMode(context.Background(), ops, writemode.WALDurable)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
	if nle.LeaderID != 2 {
		t.Fatalf("expected LeaderID 2, got %d", nle.LeaderID)
	}
}

func TestBatchService_ExecuteWithMode_NoWriteOps(t *testing.T) {
	var proposeCalled bool
	host := &mockHost{
		proposeWithModeFn: func(_ context.Context, _ uint64, _ []byte, _ writemode.Mode) (sm.Result, error) {
			proposeCalled = true
			return sm.Result{}, nil
		},
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return &KeyValue{Key: []byte("k"), Value: []byte("v")}, nil
		},
	}

	svc := NewBatchService(host, 1)
	// Only GET ops - no writes should be proposed.
	ops := []Op{
		{Type: OpGet, Key: []byte("k")},
	}

	_, err := svc.ExecuteWithMode(context.Background(), ops, writemode.Sync)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if proposeCalled {
		t.Fatal("ProposeWithMode should not be called when there are no write ops")
	}
}

// ---------------------------------------------------------------------------
// BatchService - executeGet (0% covered)
// ---------------------------------------------------------------------------

func TestBatchService_executeGet_Success(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return &KeyValue{Key: []byte("mykey"), Value: []byte("myval")}, nil
		},
	}

	svc := &BatchService{host: host, shardID: 1}
	resp, err := svc.executeGet(context.Background(), Op{Type: OpGet, Key: []byte("mykey")})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Succeeded {
		t.Fatal("Succeeded = false, want true")
	}
	if len(resp.Kvs) != 1 {
		t.Fatalf("expected 1 kv, got %d", len(resp.Kvs))
	}
}

func TestBatchService_executeGet_KeyNotFound(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, &mockKeyNotFoundError{key: "absent"}
		},
	}

	svc := &BatchService{host: host, shardID: 1}
	resp, err := svc.executeGet(context.Background(), Op{Type: OpGet, Key: []byte("absent")})
	if err != nil {
		t.Fatalf("key not found should return empty response, got error: %v", err)
	}
	if !resp.Succeeded {
		t.Fatal("Succeeded = false, want true (key not found is not an error for batch GET)")
	}
	if len(resp.Kvs) != 0 {
		t.Fatalf("expected 0 kvs for missing key, got %d", len(resp.Kvs))
	}
}

func TestBatchService_executeGet_WithRangeEnd_DelegatestoRange(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return &MVCCRangeResult{
				KeyValues: []*KeyValue{
					{Key: []byte("ka"), Value: []byte("va")},
				},
			}, nil
		},
	}

	svc := &BatchService{host: host, shardID: 1}
	// When RangeEnd is set on an OpGet, it should delegate to executeRange.
	resp, err := svc.executeGet(context.Background(), Op{
		Type:     OpGet,
		Key:      []byte("ka"),
		RangeEnd: []byte("kz"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Count != 1 {
		t.Fatalf("expected Count=1, got %d", resp.Count)
	}
}

func TestBatchService_executeGet_ReadError(t *testing.T) {
	sentinel := errors.New("shard not ready")
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, sentinel
		},
	}

	svc := &BatchService{host: host, shardID: 1}
	_, err := svc.executeGet(context.Background(), Op{Type: OpGet, Key: []byte("k")})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestBatchService_executeGet_UnexpectedResultType(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return "not a kv", nil
		},
	}

	svc := &BatchService{host: host, shardID: 1}
	_, err := svc.executeGet(context.Background(), Op{Type: OpGet, Key: []byte("k")})
	if err == nil {
		t.Fatal("expected error for unexpected result type, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// BatchService - executeRange (0% covered)
// ---------------------------------------------------------------------------

func TestBatchService_executeRange_Success(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return &MVCCRangeResult{
				KeyValues: []*KeyValue{
					{Key: []byte("a"), Value: []byte("1")},
					{Key: []byte("b"), Value: []byte("2")},
				},
			}, nil
		},
	}

	svc := &BatchService{host: host, shardID: 1}
	resp, err := svc.executeRange(context.Background(), Op{
		Type:     OpRange,
		Key:      []byte("a"),
		RangeEnd: []byte("z"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Count != 2 {
		t.Fatalf("expected Count=2, got %d", resp.Count)
	}
	if !resp.Succeeded {
		t.Fatal("Succeeded = false, want true")
	}
}

func TestBatchService_executeRange_ReadError(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, &quicraft.NotLeaderError{ShardID: 1, LeaderID: 2}
		},
	}

	svc := &BatchService{host: host, shardID: 1}
	_, err := svc.executeRange(context.Background(), Op{
		Type:     OpRange,
		Key:      []byte("a"),
		RangeEnd: []byte("z"),
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
}

func TestBatchService_executeRange_UnexpectedResultType(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return "wrong type", nil
		},
	}

	svc := &BatchService{host: host, shardID: 1}
	_, err := svc.executeRange(context.Background(), Op{
		Type:     OpRange,
		Key:      []byte("a"),
		RangeEnd: []byte("z"),
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// BatchService - translateReadError (0% covered)
// ---------------------------------------------------------------------------

func TestBatchService_translateReadError_NotLeader(t *testing.T) {
	svc := &BatchService{host: &mockHost{}, shardID: 1}
	err := svc.translateReadError(&quicraft.NotLeaderError{
		ShardID:       7,
		LeaderID:      3,
		LeaderAddress: "10.0.0.3:9100",
	})

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
	if nle.ShardID != 7 {
		t.Fatalf("expected ShardID 7, got %d", nle.ShardID)
	}
}

func TestBatchService_translateReadError_Generic(t *testing.T) {
	svc := &BatchService{host: &mockHost{}, shardID: 1}
	sentinel := errors.New("disk I/O error")

	err := svc.translateReadError(sentinel)

	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
	if !errors.Is(re.Unwrap(), sentinel) {
		t.Fatalf("expected inner sentinel, got %v", re.Unwrap())
	}
}

// ---------------------------------------------------------------------------
// TransactionService - ExecuteWithMode (0% covered)
// ---------------------------------------------------------------------------

func TestTransactionService_ExecuteWithMode_Success(t *testing.T) {
	host := &mockHost{
		proposeWithModeFn: func(_ context.Context, _ uint64, cmd []byte, mode writemode.Mode) (sm.Result, error) {
			decoded, err := protocol.DecodeCommand(cmd)
			if err != nil {
				return sm.Result{}, err
			}
			if decoded.Op != protocol.OpTransaction {
				return sm.Result{}, errors.New("expected OpTransaction")
			}
			return sm.Result{Value: 10, Data: []byte{1}}, nil
		},
	}

	svc := NewTransactionService(host, 1)
	compares := []Compare{
		{Key: []byte("k"), Target: CompareVersion, Result: CompareEqual, Version: 1},
	}
	successOps := []Op{{Type: OpPut, Key: []byte("k"), Value: []byte("v")}}

	resp, err := svc.ExecuteWithMode(context.Background(), compares, successOps, nil, writemode.WALDurable)
	if err != nil {
		t.Fatalf("ExecuteWithMode() unexpected error: %v", err)
	}
	if !resp.Succeeded {
		t.Fatal("Succeeded = false, want true")
	}
	if resp.Revision != 10 {
		t.Fatalf("expected Revision=10, got %d", resp.Revision)
	}
	if len(resp.Responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(resp.Responses))
	}
}

func TestTransactionService_ExecuteWithMode_ProposeError(t *testing.T) {
	sentinel := errors.New("raft unavailable")
	host := &mockHost{
		proposeWithModeFn: func(_ context.Context, _ uint64, _ []byte, _ writemode.Mode) (sm.Result, error) {
			return sm.Result{}, sentinel
		},
	}

	svc := NewTransactionService(host, 1)
	_, err := svc.ExecuteWithMode(context.Background(), nil, nil, nil, writemode.WALDurable)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var pe *ProposeError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProposeError, got %T: %v", err, err)
	}
}

func TestTransactionService_ExecuteWithMode_NotLeader(t *testing.T) {
	host := &mockHost{
		proposeWithModeFn: func(_ context.Context, _ uint64, _ []byte, _ writemode.Mode) (sm.Result, error) {
			return sm.Result{}, &quicraft.NotLeaderError{
				ShardID:       1,
				LeaderID:      3,
				LeaderAddress: "10.0.0.3:9100",
			}
		},
	}

	svc := NewTransactionService(host, 1)
	_, err := svc.ExecuteWithMode(context.Background(), nil, nil, nil, writemode.Sync)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
	if nle.LeaderID != 3 {
		t.Fatalf("expected LeaderID 3, got %d", nle.LeaderID)
	}
}

func TestTransactionService_ExecuteWithMode_FailureBranch(t *testing.T) {
	host := &mockHost{
		proposeWithModeFn: func(_ context.Context, _ uint64, _ []byte, _ writemode.Mode) (sm.Result, error) {
			// Data[0] = 0 means compares failed → failure branch.
			return sm.Result{Value: 7, Data: []byte{0}}, nil
		},
	}

	svc := NewTransactionService(host, 1)
	failureOps := []Op{
		{Type: OpDelete, Key: []byte("stale")},
		{Type: OpDelete, Key: []byte("old")},
	}

	resp, err := svc.ExecuteWithMode(context.Background(), nil, nil, failureOps, writemode.WALDurable)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Succeeded {
		t.Fatal("Succeeded = true, want false (failure branch)")
	}
	if len(resp.Responses) != 2 {
		t.Fatalf("expected 2 responses for failure ops, got %d", len(resp.Responses))
	}
}

// ---------------------------------------------------------------------------
// IndexService - ListIndexes (0% covered)
// ---------------------------------------------------------------------------

func TestIndexService_ListIndexes_Success(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return []string{"idx_name", "idx_email"}, nil
		},
	}

	svc := NewIndexService(host, 1)
	names, err := svc.ListIndexes(context.Background())
	if err != nil {
		t.Fatalf("ListIndexes() unexpected error: %v", err)
	}
	if len(names) != 2 {
		t.Fatalf("expected 2 names, got %d", len(names))
	}
	if names[0] != "idx_name" {
		t.Fatalf("expected idx_name, got %q", names[0])
	}
}

func TestIndexService_ListIndexes_Empty(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return []string{}, nil
		},
	}

	svc := NewIndexService(host, 1)
	names, err := svc.ListIndexes(context.Background())
	if err != nil {
		t.Fatalf("ListIndexes() unexpected error: %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("expected 0 names, got %d", len(names))
	}
}

func TestIndexService_ListIndexes_ReadError(t *testing.T) {
	sentinel := errors.New("shard error")
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, sentinel
		},
	}

	svc := NewIndexService(host, 1)
	_, err := svc.ListIndexes(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestIndexService_ListIndexes_UnexpectedType(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return "not a string slice", nil
		},
	}

	svc := NewIndexService(host, 1)
	_, err := svc.ListIndexes(context.Background())
	if err == nil {
		t.Fatal("expected error for unexpected type, got nil")
	}
	var urt *unexpectedResultTypeError
	if !errors.As(err, &urt) {
		t.Fatalf("expected *unexpectedResultTypeError, got %T: %v", err, err)
	}
}

func TestIndexService_ListIndexes_NotLeader(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, &quicraft.NotLeaderError{ShardID: 1, LeaderID: 2}
		},
	}

	svc := NewIndexService(host, 1)
	_, err := svc.ListIndexes(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// MVCCKVService - Stale reads (0% covered)
// ---------------------------------------------------------------------------

func TestMVCCKVService_GetStale_Success(t *testing.T) {
	host := &mockHost{
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return &KeyValue{Key: []byte("k"), Value: []byte("stale-val")}, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	val, err := svc.GetStale(context.Background(), "k")
	if err != nil {
		t.Fatalf("GetStale() unexpected error: %v", err)
	}
	if string(val) != "stale-val" {
		t.Fatalf("expected 'stale-val', got %q", val)
	}
}

func TestMVCCKVService_GetStale_ReadError(t *testing.T) {
	host := &mockHost{
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, &quicraft.NotLeaderError{ShardID: 1}
		},
	}

	svc := NewMVCCKVService(host, 1)
	_, err := svc.GetStale(context.Background(), "k")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
}

func TestMVCCKVService_GetStale_UnexpectedType(t *testing.T) {
	host := &mockHost{
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return "wrong", nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	_, err := svc.GetStale(context.Background(), "k")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestMVCCKVService_ListStale_Success(t *testing.T) {
	host := &mockHost{
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return &MVCCRangeResult{
				KeyValues: []*KeyValue{
					{Key: []byte("user/1"), Value: []byte("v1")},
					{Key: []byte("user/2"), Value: []byte("v2")},
				},
			}, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	keys, err := svc.ListStale(context.Background(), "user/")
	if err != nil {
		t.Fatalf("ListStale() unexpected error: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
}

func TestMVCCKVService_ListStale_EmptyPrefix(t *testing.T) {
	host := &mockHost{
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return &MVCCRangeResult{
				KeyValues: []*KeyValue{
					{Key: []byte("a"), Value: []byte("1")},
				},
			}, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	keys, err := svc.ListStale(context.Background(), "")
	if err != nil {
		t.Fatalf("ListStale() unexpected error: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(keys))
	}
}

func TestMVCCKVService_ListStale_ReadError(t *testing.T) {
	host := &mockHost{
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, errors.New("stale read error")
		},
	}

	svc := NewMVCCKVService(host, 1)
	_, err := svc.ListStale(context.Background(), "prefix/")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestMVCCKVService_ListStale_UnexpectedType(t *testing.T) {
	host := &mockHost{
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return "bad type", nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	_, err := svc.ListStale(context.Background(), "prefix/")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestMVCCKVService_ScanStale_NilResult(t *testing.T) {
	host := &mockHost{
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	data, err := svc.ScanStale(context.Background(), "prefix/")
	if err != nil {
		t.Fatalf("ScanStale() unexpected error: %v", err)
	}
	if data != nil {
		t.Fatalf("expected nil result, got %v", data)
	}
}

func TestMVCCKVService_ScanStale_Success(t *testing.T) {
	host := &mockHost{
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return map[string][]byte{
				"prefix/k1": []byte("v1"),
				"prefix/k2": []byte("v2"),
			}, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	data, err := svc.ScanStale(context.Background(), "prefix/")
	if err != nil {
		t.Fatalf("ScanStale() unexpected error: %v", err)
	}
	if len(data) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(data))
	}
}

func TestMVCCKVService_ScanStale_ReadError(t *testing.T) {
	host := &mockHost{
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, errors.New("backend error")
		},
	}

	svc := NewMVCCKVService(host, 1)
	_, err := svc.ScanStale(context.Background(), "prefix/")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestMVCCKVService_ScanStale_UnexpectedType(t *testing.T) {
	host := &mockHost{
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return []string{"bad"}, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	_, err := svc.ScanStale(context.Background(), "prefix/")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// MVCCKVService - PutWithMode (0% covered)
// ---------------------------------------------------------------------------

func TestMVCCKVService_PutWithMode_Success(t *testing.T) {
	var capturedMode writemode.Mode
	host := &mockHost{
		proposeWithModeFn: func(_ context.Context, _ uint64, _ []byte, mode writemode.Mode) (sm.Result, error) {
			capturedMode = mode
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	err := svc.PutWithMode(context.Background(), "key", []byte("val"), writemode.WALDurable)
	if err != nil {
		t.Fatalf("PutWithMode() unexpected error: %v", err)
	}
	if capturedMode != writemode.WALDurable {
		t.Fatalf("expected Relaxed mode, got %v", capturedMode)
	}
}

func TestMVCCKVService_PutWithMode_ProposeError(t *testing.T) {
	sentinel := errors.New("raft error")
	host := &mockHost{
		proposeWithModeFn: func(_ context.Context, _ uint64, _ []byte, _ writemode.Mode) (sm.Result, error) {
			return sm.Result{}, sentinel
		},
	}

	svc := NewMVCCKVService(host, 1)
	err := svc.PutWithMode(context.Background(), "key", []byte("val"), writemode.Sync)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var pe *ProposeError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProposeError, got %T: %v", err, err)
	}
}

func TestMVCCKVService_PutWithMode_NotLeader(t *testing.T) {
	host := &mockHost{
		proposeWithModeFn: func(_ context.Context, _ uint64, _ []byte, _ writemode.Mode) (sm.Result, error) {
			return sm.Result{}, &quicraft.NotLeaderError{ShardID: 1, LeaderID: 2}
		},
	}

	svc := NewMVCCKVService(host, 1)
	err := svc.PutWithMode(context.Background(), "key", []byte("val"), writemode.WALDurable)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// MVCCKVService - DeleteWithMode (0% covered)
// ---------------------------------------------------------------------------

func TestMVCCKVService_DeleteWithMode_Success(t *testing.T) {
	var capturedMode writemode.Mode
	host := &mockHost{
		proposeWithModeFn: func(_ context.Context, _ uint64, _ []byte, mode writemode.Mode) (sm.Result, error) {
			capturedMode = mode
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	err := svc.DeleteWithMode(context.Background(), "key", writemode.Sync)
	if err != nil {
		t.Fatalf("DeleteWithMode() unexpected error: %v", err)
	}
	if capturedMode != writemode.Sync {
		t.Fatalf("expected Strict mode, got %v", capturedMode)
	}
}

func TestMVCCKVService_DeleteWithMode_ProposeError(t *testing.T) {
	sentinel := errors.New("raft unavailable")
	host := &mockHost{
		proposeWithModeFn: func(_ context.Context, _ uint64, _ []byte, _ writemode.Mode) (sm.Result, error) {
			return sm.Result{}, sentinel
		},
	}

	svc := NewMVCCKVService(host, 1)
	err := svc.DeleteWithMode(context.Background(), "key", writemode.WALDurable)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var pe *ProposeError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProposeError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// MVCCKVService - GetWithConsistency (0% covered)
// ---------------------------------------------------------------------------

func TestMVCCKVService_GetWithConsistency_Success(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return &KeyValue{Key: []byte("k"), Value: []byte("v")}, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	val, err := svc.GetWithConsistency(context.Background(), "k", consistency.ReadOptions{})
	if err != nil {
		t.Fatalf("GetWithConsistency() unexpected error: %v", err)
	}
	if string(val) != "v" {
		t.Fatalf("expected 'v', got %q", val)
	}
}

func TestMVCCKVService_GetWithConsistency_ReadError(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return nil, errors.New("consistency error")
		},
	}

	svc := NewMVCCKVService(host, 1)
	_, err := svc.GetWithConsistency(context.Background(), "k", consistency.ReadOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestMVCCKVService_GetWithConsistency_UnexpectedType(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return "bad type", nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	_, err := svc.GetWithConsistency(context.Background(), "k", consistency.ReadOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// MVCCKVService - ListWithConsistency (0% covered)
// ---------------------------------------------------------------------------

func TestMVCCKVService_ListWithConsistency_Success(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return &MVCCRangeResult{
				KeyValues: []*KeyValue{
					{Key: []byte("user/1"), Value: []byte("v1")},
					{Key: []byte("user/2"), Value: []byte("v2")},
				},
			}, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	keys, err := svc.ListWithConsistency(context.Background(), "user/", consistency.ReadOptions{})
	if err != nil {
		t.Fatalf("ListWithConsistency() unexpected error: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
}

func TestMVCCKVService_ListWithConsistency_EmptyPrefix(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return &MVCCRangeResult{
				KeyValues: []*KeyValue{{Key: []byte("a"), Value: []byte("1")}},
			}, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	keys, err := svc.ListWithConsistency(context.Background(), "", consistency.ReadOptions{})
	if err != nil {
		t.Fatalf("ListWithConsistency() unexpected error: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(keys))
	}
}

func TestMVCCKVService_ListWithConsistency_ReadError(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return nil, errors.New("read error")
		},
	}

	svc := NewMVCCKVService(host, 1)
	_, err := svc.ListWithConsistency(context.Background(), "prefix/", consistency.ReadOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestMVCCKVService_ListWithConsistency_UnexpectedType(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return "not a range result", nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	_, err := svc.ListWithConsistency(context.Background(), "prefix/", consistency.ReadOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// MVCCKVService - ScanWithConsistency (0% covered)
// ---------------------------------------------------------------------------

func TestMVCCKVService_ScanWithConsistency_NilResult(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return nil, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	data, err := svc.ScanWithConsistency(context.Background(), "prefix/", consistency.ReadOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if data != nil {
		t.Fatalf("expected nil, got %v", data)
	}
}

func TestMVCCKVService_ScanWithConsistency_Success(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return map[string][]byte{
				"prefix/k1": []byte("v1"),
			}, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	data, err := svc.ScanWithConsistency(context.Background(), "prefix/", consistency.ReadOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(data) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(data))
	}
}

func TestMVCCKVService_ScanWithConsistency_ReadError(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return nil, errors.New("backend error")
		},
	}

	svc := NewMVCCKVService(host, 1)
	_, err := svc.ScanWithConsistency(context.Background(), "prefix/", consistency.ReadOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestMVCCKVService_ScanWithConsistency_UnexpectedType(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return []string{"bad"}, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	_, err := svc.ScanWithConsistency(context.Background(), "prefix/", consistency.ReadOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// MVCCKVService - ExistsWithConsistency (0% covered)
// ---------------------------------------------------------------------------

func TestMVCCKVService_ExistsWithConsistency_Found(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return &KeyValue{Key: []byte("k"), Value: []byte("v")}, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	exists, err := svc.ExistsWithConsistency(context.Background(), "k", consistency.ReadOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Fatal("expected exists=true, got false")
	}
}

func TestMVCCKVService_ExistsWithConsistency_NotFound(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return nil, &mockKeyNotFoundError{key: "missing"}
		},
	}

	svc := NewMVCCKVService(host, 1)
	exists, err := svc.ExistsWithConsistency(context.Background(), "missing", consistency.ReadOptions{})
	if err != nil {
		t.Fatalf("unexpected error for missing key: %v", err)
	}
	if exists {
		t.Fatal("expected exists=false for missing key")
	}
}

func TestMVCCKVService_ExistsWithConsistency_ReadError(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return nil, errors.New("consistency failure")
		},
	}

	svc := NewMVCCKVService(host, 1)
	_, err := svc.ExistsWithConsistency(context.Background(), "k", consistency.ReadOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// MVCCKVService - PutWithLeaseAndMode (0% covered)
// ---------------------------------------------------------------------------

func TestMVCCKVService_PutWithLeaseAndMode_Success(t *testing.T) {
	var capturedMode writemode.Mode
	host := &mockHost{
		proposeWithModeFn: func(_ context.Context, _ uint64, _ []byte, mode writemode.Mode) (sm.Result, error) {
			capturedMode = mode
			return sm.Result{Value: 5}, nil
		},
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return &KeyValue{
				Key:         []byte("k"),
				Value:       []byte("v"),
				ModRevision: 5,
				LeaseID:     42,
			}, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	kv, err := svc.PutWithLeaseAndMode(context.Background(), "k", []byte("v"), 42, writemode.WALDurable)
	if err != nil {
		t.Fatalf("PutWithLeaseAndMode() unexpected error: %v", err)
	}
	if kv == nil {
		t.Fatal("expected non-nil KeyValue")
	}
	if kv.ModRevision != 5 {
		t.Fatalf("expected ModRevision=5, got %d", kv.ModRevision)
	}
	if kv.LeaseID != 42 {
		t.Fatalf("expected LeaseID=42, got %d", kv.LeaseID)
	}
	if capturedMode != writemode.WALDurable {
		t.Fatalf("expected WALDurable mode, got %v", capturedMode)
	}
}

func TestMVCCKVService_PutWithLeaseAndMode_ProposeError(t *testing.T) {
	sentinel := errors.New("raft error")
	host := &mockHost{
		proposeWithModeFn: func(_ context.Context, _ uint64, _ []byte, _ writemode.Mode) (sm.Result, error) {
			return sm.Result{}, sentinel
		},
	}

	svc := NewMVCCKVService(host, 1)
	_, err := svc.PutWithLeaseAndMode(context.Background(), "k", []byte("v"), 1, writemode.Sync)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var pe *ProposeError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProposeError, got %T: %v", err, err)
	}
}

func TestMVCCKVService_PutWithLeaseAndMode_ReadError(t *testing.T) {
	host := &mockHost{
		proposeWithModeFn: func(_ context.Context, _ uint64, _ []byte, _ writemode.Mode) (sm.Result, error) {
			return sm.Result{Value: 3}, nil
		},
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, errors.New("read after write failed")
		},
	}

	svc := NewMVCCKVService(host, 1)
	_, err := svc.PutWithLeaseAndMode(context.Background(), "k", []byte("v"), 1, writemode.WALDurable)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// MVCCKVService - GetRevisionWithConsistency (0% covered)
// ---------------------------------------------------------------------------

func TestMVCCKVService_GetRevisionWithConsistency_Success(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return &KeyValue{Key: []byte("k"), Value: []byte("v"), ModRevision: 7}, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	kv, err := svc.GetRevisionWithConsistency(context.Background(), "k", consistency.ReadOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if kv.ModRevision != 7 {
		t.Fatalf("expected ModRevision=7, got %d", kv.ModRevision)
	}
}

func TestMVCCKVService_GetRevisionWithConsistency_ReadError(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return nil, errors.New("read error")
		},
	}

	svc := NewMVCCKVService(host, 1)
	_, err := svc.GetRevisionWithConsistency(context.Background(), "k", consistency.ReadOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestMVCCKVService_GetRevisionWithConsistency_UnexpectedType(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return "bad", nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	_, err := svc.GetRevisionWithConsistency(context.Background(), "k", consistency.ReadOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// MVCCKVService - GetAtRevisionWithConsistency (0% covered)
// ---------------------------------------------------------------------------

func TestMVCCKVService_GetAtRevisionWithConsistency_Success(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return &KeyValue{Key: []byte("k"), Value: []byte("historical"), ModRevision: 3}, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	kv, err := svc.GetAtRevisionWithConsistency(context.Background(), "k", 3, consistency.ReadOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if kv.ModRevision != 3 {
		t.Fatalf("expected ModRevision=3, got %d", kv.ModRevision)
	}
	if string(kv.Value) != "historical" {
		t.Fatalf("expected 'historical', got %q", kv.Value)
	}
}

func TestMVCCKVService_GetAtRevisionWithConsistency_ReadError(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return nil, errors.New("compacted")
		},
	}

	svc := NewMVCCKVService(host, 1)
	_, err := svc.GetAtRevisionWithConsistency(context.Background(), "k", 1, consistency.ReadOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestMVCCKVService_GetAtRevisionWithConsistency_UnexpectedType(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return 42, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	_, err := svc.GetAtRevisionWithConsistency(context.Background(), "k", 1, consistency.ReadOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// MVCCKVService - RangeWithConsistency (0% covered)
// ---------------------------------------------------------------------------

func TestMVCCKVService_RangeWithConsistency_Success(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return &MVCCRangeResult{
				KeyValues: []*KeyValue{
					{Key: []byte("a"), Value: []byte("1")},
					{Key: []byte("b"), Value: []byte("2")},
				},
				// Count and Revision are deliberately different values so
				// this test cannot pass by coincidence if
				// RangeWithConsistency regresses to returning the revision
				// in the count's place.
				Count:    2,
				Revision: 10,
			}, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	kvs, count, revision, err := svc.RangeWithConsistency(context.Background(), []byte("a"), []byte("z"), consistency.ReadOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(kvs) != 2 {
		t.Fatalf("expected 2 kvs, got %d", len(kvs))
	}
	// RangeWithConsistency's second return value is the match count, not
	// the MVCC revision (see MVCCKVServicer.Range's doc comment, which
	// RangeWithConsistency shares the contract of).
	if count != 2 {
		t.Fatalf("expected count 2, got %d", count)
	}
	// The third return value is the MVCC revision the read was performed
	// at, threaded through distinctly from the count.
	if revision != 10 {
		t.Fatalf("expected revision 10, got %d", revision)
	}
}

// TestMVCCKVService_RangeWithConsistency_ThreadsSortOptions verifies
// WithSort's SortOrder and SortTarget are copied onto the MVCCRangeQuery
// built by RangeWithConsistency, reproducing (as a regression guard) the
// bug where these fields were silently dropped before ever reaching the
// query.
func TestMVCCKVService_RangeWithConsistency_ThreadsSortOptions(t *testing.T) {
	var capturedQuery interface{}

	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, query interface{}, _ consistency.ReadOptions) (interface{}, error) {
			capturedQuery = query
			return &MVCCRangeResult{KeyValues: []*KeyValue{}}, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	_, _, _, err := svc.RangeWithConsistency(context.Background(), []byte("a"), []byte("z"), consistency.ReadOptions{}, WithSort(SortAscend, SortByValue))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rq, ok := capturedQuery.(MVCCRangeQuery)
	if !ok {
		t.Fatalf("expected MVCCRangeQuery, got %T", capturedQuery)
	}
	if rq.SortOrder != SortAscend {
		t.Fatalf("expected SortOrder=SortAscend, got %v", rq.SortOrder)
	}
	if rq.SortTarget != SortByValue {
		t.Fatalf("expected SortTarget=SortByValue, got %v", rq.SortTarget)
	}
}

func TestMVCCKVService_RangeWithConsistency_ReadError(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return nil, errors.New("shard unavailable")
		},
	}

	svc := NewMVCCKVService(host, 1)
	_, _, _, err := svc.RangeWithConsistency(context.Background(), []byte("a"), []byte("z"), consistency.ReadOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestMVCCKVService_RangeWithConsistency_UnexpectedType(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return "nope", nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	_, _, _, err := svc.RangeWithConsistency(context.Background(), []byte("a"), []byte("z"), consistency.ReadOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// KVService - ListStale / ScanStale / PutWithMode / DeleteWithMode /
//             GetWithConsistency / ListWithConsistency / ScanWithConsistency /
//             ExistsWithConsistency (all 0% covered)
// ---------------------------------------------------------------------------

func TestKVService_ListStale_Success(t *testing.T) {
	host := &mockHost{
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return []string{"k1", "k2"}, nil
		},
	}

	svc := NewKVService(host, 1)
	keys, err := svc.ListStale(context.Background(), "prefix/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
}

func TestKVService_ListStale_NilResult(t *testing.T) {
	host := &mockHost{
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, nil
		},
	}

	svc := NewKVService(host, 1)
	keys, err := svc.ListStale(context.Background(), "prefix/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if keys != nil {
		t.Fatalf("expected nil keys, got %v", keys)
	}
}

func TestKVService_ListStale_ReadError(t *testing.T) {
	host := &mockHost{
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, errors.New("stale error")
		},
	}

	svc := NewKVService(host, 1)
	_, err := svc.ListStale(context.Background(), "p/")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestKVService_ListStale_UnexpectedType(t *testing.T) {
	host := &mockHost{
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return 42, nil
		},
	}

	svc := NewKVService(host, 1)
	_, err := svc.ListStale(context.Background(), "p/")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestKVService_ScanStale_Success(t *testing.T) {
	host := &mockHost{
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return map[string][]byte{"p/k1": []byte("v1")}, nil
		},
	}

	svc := NewKVService(host, 1)
	data, err := svc.ScanStale(context.Background(), "p/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(data) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(data))
	}
}

func TestKVService_ScanStale_NilResult(t *testing.T) {
	host := &mockHost{
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, nil
		},
	}

	svc := NewKVService(host, 1)
	data, err := svc.ScanStale(context.Background(), "p/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if data != nil {
		t.Fatalf("expected nil, got %v", data)
	}
}

func TestKVService_ScanStale_ReadError(t *testing.T) {
	host := &mockHost{
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, errors.New("io error")
		},
	}

	svc := NewKVService(host, 1)
	_, err := svc.ScanStale(context.Background(), "p/")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestKVService_ScanStale_UnexpectedType(t *testing.T) {
	host := &mockHost{
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return 99, nil
		},
	}

	svc := NewKVService(host, 1)
	_, err := svc.ScanStale(context.Background(), "p/")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestKVService_PutWithMode_Success(t *testing.T) {
	var capturedMode writemode.Mode
	host := &mockHost{
		proposeWithModeFn: func(_ context.Context, _ uint64, _ []byte, mode writemode.Mode) (sm.Result, error) {
			capturedMode = mode
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewKVService(host, 1)
	err := svc.PutWithMode(context.Background(), "key", []byte("val"), writemode.WALDurable)
	if err != nil {
		t.Fatalf("PutWithMode() unexpected error: %v", err)
	}
	if capturedMode != writemode.WALDurable {
		t.Fatalf("expected Relaxed, got %v", capturedMode)
	}
}

func TestKVService_PutWithMode_ProposeError(t *testing.T) {
	sentinel := errors.New("raft error")
	host := &mockHost{
		proposeWithModeFn: func(_ context.Context, _ uint64, _ []byte, _ writemode.Mode) (sm.Result, error) {
			return sm.Result{}, sentinel
		},
	}

	svc := NewKVService(host, 1)
	err := svc.PutWithMode(context.Background(), "k", []byte("v"), writemode.Sync)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var pe *ProposeError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProposeError, got %T: %v", err, err)
	}
}

func TestKVService_DeleteWithMode_Success(t *testing.T) {
	var capturedMode writemode.Mode
	host := &mockHost{
		proposeWithModeFn: func(_ context.Context, _ uint64, _ []byte, mode writemode.Mode) (sm.Result, error) {
			capturedMode = mode
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewKVService(host, 1)
	err := svc.DeleteWithMode(context.Background(), "key", writemode.Sync)
	if err != nil {
		t.Fatalf("DeleteWithMode() unexpected error: %v", err)
	}
	if capturedMode != writemode.Sync {
		t.Fatalf("expected Strict, got %v", capturedMode)
	}
}

func TestKVService_DeleteWithMode_ProposeError(t *testing.T) {
	sentinel := errors.New("raft error")
	host := &mockHost{
		proposeWithModeFn: func(_ context.Context, _ uint64, _ []byte, _ writemode.Mode) (sm.Result, error) {
			return sm.Result{}, sentinel
		},
	}

	svc := NewKVService(host, 1)
	err := svc.DeleteWithMode(context.Background(), "k", writemode.WALDurable)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var pe *ProposeError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProposeError, got %T: %v", err, err)
	}
}

func TestKVService_GetWithConsistency_Success(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return []byte("consistent-val"), nil
		},
	}

	svc := NewKVService(host, 1)
	val, err := svc.GetWithConsistency(context.Background(), "k", consistency.ReadOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(val) != "consistent-val" {
		t.Fatalf("expected 'consistent-val', got %q", val)
	}
}

func TestKVService_GetWithConsistency_NotFound(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return nil, nil
		},
	}

	svc := NewKVService(host, 1)
	_, err := svc.GetWithConsistency(context.Background(), "missing", consistency.ReadOptions{})
	if err == nil {
		t.Fatal("expected error for nil result")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestKVService_GetWithConsistency_ReadError(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return nil, errors.New("consistency error")
		},
	}

	svc := NewKVService(host, 1)
	_, err := svc.GetWithConsistency(context.Background(), "k", consistency.ReadOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestKVService_GetWithConsistency_UnexpectedType(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return 42, nil
		},
	}

	svc := NewKVService(host, 1)
	_, err := svc.GetWithConsistency(context.Background(), "k", consistency.ReadOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestKVService_ListWithConsistency_Success(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return []string{"k1", "k2"}, nil
		},
	}

	svc := NewKVService(host, 1)
	keys, err := svc.ListWithConsistency(context.Background(), "prefix/", consistency.ReadOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
}

func TestKVService_ListWithConsistency_NilResult(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return nil, nil
		},
	}

	svc := NewKVService(host, 1)
	keys, err := svc.ListWithConsistency(context.Background(), "p/", consistency.ReadOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if keys != nil {
		t.Fatalf("expected nil, got %v", keys)
	}
}

func TestKVService_ListWithConsistency_ReadError(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return nil, errors.New("read error")
		},
	}

	svc := NewKVService(host, 1)
	_, err := svc.ListWithConsistency(context.Background(), "p/", consistency.ReadOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestKVService_ScanWithConsistency_Success(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return map[string][]byte{"p/k": []byte("v")}, nil
		},
	}

	svc := NewKVService(host, 1)
	data, err := svc.ScanWithConsistency(context.Background(), "p/", consistency.ReadOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(data) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(data))
	}
}

func TestKVService_ScanWithConsistency_NilResult(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return nil, nil
		},
	}

	svc := NewKVService(host, 1)
	data, err := svc.ScanWithConsistency(context.Background(), "p/", consistency.ReadOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if data != nil {
		t.Fatalf("expected nil, got %v", data)
	}
}

func TestKVService_ScanWithConsistency_ReadError(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return nil, errors.New("error")
		},
	}

	svc := NewKVService(host, 1)
	_, err := svc.ScanWithConsistency(context.Background(), "p/", consistency.ReadOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestKVService_ExistsWithConsistency_Found(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return []byte("val"), nil
		},
	}

	svc := NewKVService(host, 1)
	exists, err := svc.ExistsWithConsistency(context.Background(), "k", consistency.ReadOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Fatal("expected exists=true")
	}
}

func TestKVService_ExistsWithConsistency_NotFound_KVKeyNotFoundError(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return nil, &KeyNotFoundError{Key: "k"}
		},
	}

	svc := NewKVService(host, 1)
	exists, err := svc.ExistsWithConsistency(context.Background(), "k", consistency.ReadOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Fatal("expected exists=false for not found key")
	}
}

func TestKVService_ExistsWithConsistency_ReadError(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return nil, errors.New("read error")
		},
	}

	svc := NewKVService(host, 1)
	_, err := svc.ExistsWithConsistency(context.Background(), "k", consistency.ReadOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// KVService - GetStale (partial coverage improvement)
// ---------------------------------------------------------------------------

func TestKVService_GetStale_NilResult(t *testing.T) {
	host := &mockHost{
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, nil
		},
	}

	svc := NewKVService(host, 1)
	_, err := svc.GetStale(context.Background(), "k")
	if err == nil {
		t.Fatal("expected error for nil result")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// FilterInternalKeys / FilterInternalMap (partial coverage)
// ---------------------------------------------------------------------------

func TestFilterSystemKeys_WithSystemKey(t *testing.T) {
	keys := []string{"user/1", "__sys:meta", "user/2"}
	result := FilterInternalKeys(keys, "user/")
	if len(result) != 2 {
		t.Fatalf("expected 2 user keys, got %d: %v", len(result), result)
	}
	for _, k := range result {
		if k == "__sys:meta" {
			t.Fatal("system key should be filtered")
		}
	}
}

func TestFilterSystemKeys_SystemPrefix_NoFilter(t *testing.T) {
	keys := []string{"__sys:meta", "__sys:config"}
	result := FilterInternalKeys(keys, "__sys:")
	if len(result) != 2 {
		t.Fatalf("expected 2 keys (no filtering for system prefix), got %d", len(result))
	}
}

func TestFilterSystemMap_WithSystemKey(t *testing.T) {
	data := map[string][]byte{
		"user/1":     []byte("v1"),
		"__sys:meta": []byte("system"),
	}
	result := FilterInternalMap(data, "user/")
	if len(result) != 1 {
		t.Fatalf("expected 1 entry after filtering, got %d", len(result))
	}
	if _, ok := result["user/1"]; !ok {
		t.Fatal("expected 'user/1' to be present")
	}
}

func TestFilterSystemMap_SystemPrefix_NoFilter(t *testing.T) {
	data := map[string][]byte{
		"__sys:a": []byte("1"),
		"__sys:b": []byte("2"),
	}
	result := FilterInternalMap(data, "__sys:")
	if len(result) != 2 {
		t.Fatalf("expected 2 entries (no filtering for system prefix), got %d", len(result))
	}
}
