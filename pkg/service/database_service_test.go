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

package service

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// newTestDatabaseService creates a DatabaseService backed by a Manager
// rooted at a temporary directory.
func newTestDatabaseService(t *testing.T) DatabaseServicer {
	t.Helper()
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := claimingManager(t, logger, dataDir)
	t.Cleanup(func() { _ = mgr.Close() })
	return NewDatabaseService(mgr)
}

// testDBConfig returns a DatabaseConfig using the in-memory backend and
// KV engine, suitable for fast unit tests.
func testDBConfig(name string) database.DatabaseConfig {
	return database.DatabaseConfig{
		Name:    name,
		Engine:  database.EngineKV,
		Backend: database.BackendMemory,
	}
}

func TestDatabaseService_Create(t *testing.T) {
	svc := newTestDatabaseService(t)

	cfg := testDBConfig("mydb")
	info, err := svc.Create(cfg)
	if err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}
	if info.Name != "mydb" {
		t.Errorf("Info.Name = %q, want %q", info.Name, "mydb")
	}
	if info.Engine != database.EngineKV {
		t.Errorf("Info.Engine = %q, want %q", info.Engine, database.EngineKV)
	}
	if info.Backend != database.BackendMemory {
		t.Errorf("Info.Backend = %q, want %q", info.Backend, database.BackendMemory)
	}
	if info.Status != database.StatusOpen {
		t.Errorf("Info.Status = %q, want %q", info.Status, database.StatusOpen)
	}
}

func TestDatabaseService_Create_InvalidConfig(t *testing.T) {
	svc := newTestDatabaseService(t)

	cfg := database.DatabaseConfig{
		Name:    "",
		Engine:  database.EngineKV,
		Backend: database.BackendMemory,
	}
	_, err := svc.Create(cfg)
	if err == nil {
		t.Fatal("Create() with empty name returned nil, want error")
	}
	var nameErr *database.InvalidNameError
	if !errors.As(err, &nameErr) {
		t.Fatalf("Create() error type = %T, want *database.InvalidNameError", err)
	}
}

func TestDatabaseService_Get(t *testing.T) {
	svc := newTestDatabaseService(t)

	cfg := testDBConfig("getdb")
	created, err := svc.Create(cfg)
	if err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}

	got, err := svc.Get("getdb")
	if err != nil {
		t.Fatalf("Get() returned unexpected error: %v", err)
	}
	if got.Name != created.Name {
		t.Errorf("Get().Name = %q, want %q", got.Name, created.Name)
	}
	if got.Engine != created.Engine {
		t.Errorf("Get().Engine = %q, want %q", got.Engine, created.Engine)
	}
	if got.Backend != created.Backend {
		t.Errorf("Get().Backend = %q, want %q", got.Backend, created.Backend)
	}
	if got.Status != database.StatusOpen {
		t.Errorf("Get().Status = %q, want %q", got.Status, database.StatusOpen)
	}
}

func TestDatabaseService_Get_NotFound(t *testing.T) {
	svc := newTestDatabaseService(t)

	_, err := svc.Get("nonexistent")
	if err == nil {
		t.Fatal("Get() returned nil, want *database.NotFoundError")
	}
	var notFoundErr *database.NotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Fatalf("Get() error type = %T, want *database.NotFoundError", err)
	}
	if notFoundErr.Name != "nonexistent" {
		t.Errorf("NotFoundError.Name = %q, want %q", notFoundErr.Name, "nonexistent")
	}
}

func TestDatabaseService_List(t *testing.T) {
	svc := newTestDatabaseService(t)

	names := []string{"alpha", "beta"}
	for _, name := range names {
		if _, err := svc.Create(testDBConfig(name)); err != nil {
			t.Fatalf("Create(%q) returned unexpected error: %v", name, err)
		}
	}

	list, err := svc.List()
	if err != nil {
		t.Fatalf("List() returned unexpected error: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List() returned %d entries, want 2", len(list))
	}

	// Manager.List returns sorted results.
	if list[0].Name != "alpha" {
		t.Errorf("List()[0].Name = %q, want %q", list[0].Name, "alpha")
	}
	if list[1].Name != "beta" {
		t.Errorf("List()[1].Name = %q, want %q", list[1].Name, "beta")
	}
}

func TestDatabaseService_List_Empty(t *testing.T) {
	svc := newTestDatabaseService(t)

	list, err := svc.List()
	if err != nil {
		t.Fatalf("List() returned unexpected error: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("List() on empty service returned %d entries, want 0", len(list))
	}
}

func TestDatabaseService_Drop(t *testing.T) {
	svc := newTestDatabaseService(t)

	cfg := testDBConfig("dropdb")
	if _, err := svc.Create(cfg); err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}

	if err := svc.Drop(context.Background(), "dropdb"); err != nil {
		t.Fatalf("Drop() returned unexpected error: %v", err)
	}

	// Verify Get returns NotFoundError after drop.
	_, err := svc.Get("dropdb")
	if err == nil {
		t.Fatal("Get() after Drop() returned nil, want *database.NotFoundError")
	}
	var notFoundErr *database.NotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Fatalf("Get() after Drop() error type = %T, want *database.NotFoundError", err)
	}

	// Verify List no longer includes the dropped database.
	list, err := svc.List()
	if err != nil {
		t.Fatalf("List() returned unexpected error: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("List() after Drop() returned %d entries, want 0", len(list))
	}
}

func TestDatabaseService_Drop_NotFound(t *testing.T) {
	svc := newTestDatabaseService(t)

	err := svc.Drop(context.Background(), "phantom")
	if err == nil {
		t.Fatal("Drop() returned nil, want *database.NotFoundError")
	}
	var notFoundErr *database.NotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Fatalf("Drop() error type = %T, want *database.NotFoundError", err)
	}
	if notFoundErr.Name != "phantom" {
		t.Errorf("NotFoundError.Name = %q, want %q", notFoundErr.Name, "phantom")
	}
}

func TestDatabaseService_UpdateProtocol_Success(t *testing.T) {
	svc := newTestDatabaseService(t)

	// Create a database first.
	cfg := testDBConfig("sql-svc-db")
	created, err := svc.Create(cfg)
	if err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}

	// Initial protocol config should be zero.
	if created.Protocol.MySQL || created.Protocol.PostgreSQL {
		t.Fatal("initial protocol config is not zero value")
	}

	// Update protocol config via service.
	info, err := svc.UpdateProtocol("sql-svc-db", database.ProtocolConfig{MySQL: true, PostgreSQL: true})
	if err != nil {
		t.Fatalf("UpdateProtocol() returned unexpected error: %v", err)
	}

	if info.Name != "sql-svc-db" {
		t.Errorf("UpdateProtocol().Name = %q, want %q", info.Name, "sql-svc-db")
	}
	if !info.Protocol.MySQL {
		t.Error("UpdateProtocol().Protocol.MySQL = false, want true")
	}
	if !info.Protocol.PostgreSQL {
		t.Error("UpdateProtocol().Protocol.PostgreSQL = false, want true")
	}
	if info.Status != database.StatusOpen {
		t.Errorf("UpdateProtocol().Status = %q, want %q", info.Status, database.StatusOpen)
	}

	// Verify Get also reflects the update.
	got, err := svc.Get("sql-svc-db")
	if err != nil {
		t.Fatalf("Get() returned unexpected error: %v", err)
	}
	if !got.Protocol.MySQL {
		t.Error("Get().Protocol.MySQL = false after UpdateProtocol, want true")
	}
	if !got.Protocol.PostgreSQL {
		t.Error("Get().Protocol.PostgreSQL = false after UpdateProtocol, want true")
	}
}

func TestDatabaseService_UpdateProtocol_NotFound(t *testing.T) {
	svc := newTestDatabaseService(t)

	_, err := svc.UpdateProtocol("nonexistent-db", database.ProtocolConfig{MySQL: true})
	if err == nil {
		t.Fatal("UpdateProtocol() returned nil, want *database.NotFoundError")
	}
	var notFoundErr *database.NotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Fatalf("UpdateProtocol() error type = %T, want *database.NotFoundError", err)
	}
	if notFoundErr.Name != "nonexistent-db" {
		t.Errorf("NotFoundError.Name = %q, want %q", notFoundErr.Name, "nonexistent-db")
	}
}

// ---------------------------------------------------------------------------
// UpdateRaft tests
// ---------------------------------------------------------------------------

func TestDatabaseService_UpdateRaft_Success(t *testing.T) {
	svc := newTestDatabaseService(t)

	// Create a database first.
	cfg := testDBConfig("raft-db")
	if _, err := svc.Create(cfg); err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}

	// Update Raft config via service.
	raftCfg := &database.DatabaseRaftConfig{
		ClusterMode: database.RaftLocal,
		ReplicaID:   1,
	}
	info, err := svc.UpdateRaft("raft-db", raftCfg)
	if err != nil {
		t.Fatalf("UpdateRaft() returned unexpected error: %v", err)
	}

	if info.Name != "raft-db" {
		t.Errorf("UpdateRaft().Name = %q, want %q", info.Name, "raft-db")
	}
	if info.Raft == nil {
		t.Fatal("UpdateRaft().Raft is nil, want non-nil")
	}
	if info.Raft.ClusterMode != database.RaftLocal {
		t.Errorf("UpdateRaft().Raft.ClusterMode = %q, want %q", info.Raft.ClusterMode, database.RaftLocal)
	}
	// The database was created with no Raft configuration, so it was issued
	// no shard, and an update cannot give it one.
	if info.Raft.ShardID != 0 {
		t.Errorf("UpdateRaft().Raft.ShardID = %d, want 0", info.Raft.ShardID)
	}
	if info.Raft.ReplicaID != 1 {
		t.Errorf("UpdateRaft().Raft.ReplicaID = %d, want 1", info.Raft.ReplicaID)
	}

	// Verify Get also reflects the update.
	got, err := svc.Get("raft-db")
	if err != nil {
		t.Fatalf("Get() returned unexpected error: %v", err)
	}
	if got.Raft == nil {
		t.Fatal("Get().Raft is nil after UpdateRaft, want non-nil")
	}
	if got.Raft.ClusterMode != database.RaftLocal {
		t.Errorf("Get().Raft.ClusterMode = %q, want %q", got.Raft.ClusterMode, database.RaftLocal)
	}
}

func TestDatabaseService_UpdateRaft_NotFound(t *testing.T) {
	svc := newTestDatabaseService(t)

	raftCfg := &database.DatabaseRaftConfig{
		ClusterMode: database.RaftLocal,
	}
	_, err := svc.UpdateRaft("nonexistent-raft-db", raftCfg)
	if err == nil {
		t.Fatal("UpdateRaft() returned nil, want *database.NotFoundError")
	}
	var notFoundErr *database.NotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Fatalf("UpdateRaft() error type = %T, want *database.NotFoundError", err)
	}
	if notFoundErr.Name != "nonexistent-raft-db" {
		t.Errorf("NotFoundError.Name = %q, want %q", notFoundErr.Name, "nonexistent-raft-db")
	}
}

func TestDatabaseService_UpdateRaft_InvalidClusterMode(t *testing.T) {
	svc := newTestDatabaseService(t)

	// Create a database first.
	cfg := testDBConfig("raft-invalid-db")
	if _, err := svc.Create(cfg); err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}

	// Attempt to update with an invalid cluster mode.
	raftCfg := &database.DatabaseRaftConfig{
		ClusterMode: database.RaftClusterMode("bogus"),
	}
	_, err := svc.UpdateRaft("raft-invalid-db", raftCfg)
	if err == nil {
		t.Fatal("UpdateRaft() returned nil, want *database.InvalidRaftClusterModeError")
	}
	var modeErr *database.InvalidRaftClusterModeError
	if !errors.As(err, &modeErr) {
		t.Fatalf("UpdateRaft() error type = %T, want *database.InvalidRaftClusterModeError", err)
	}
	if modeErr.Mode != "bogus" {
		t.Errorf("InvalidRaftClusterModeError.Mode = %q, want %q", modeErr.Mode, "bogus")
	}
}

// ---------------------------------------------------------------------------
// UpdateBarrier tests
// ---------------------------------------------------------------------------

// TestDatabaseService_UpdateBarrier_Success verifies the one safe
// transition UpdateBarrier performs: re-affirming an already-active
// barrier, which refreshes its non-secret bookkeeping fields
// (Mode/SharesRequired/SharesTotal) without changing the active encryption
// state. Enabling a barrier for the first time via UpdateBarrier is
// rejected (database.BarrierMigrationNotSupportedError) since it would
// require re-encrypting any existing plaintext data, a migration this API
// does not perform -- so the database is created WITH its barrier already
// active. Shamir is not yet an implemented strategy (EnsureBarrier only
// supports passphrase), so the refreshed Mode here is passphrase.
func TestDatabaseService_UpdateBarrier_Success(t *testing.T) {
	svc, _ := newTestDatabaseServiceWithBarrier(t)

	cfg := testDBConfig("barrier-db")
	cfg.DataDir = t.TempDir()
	cfg.Barrier = &database.DatabaseBarrierConfig{Enabled: true, Mode: database.BarrierModePassphrase}
	if _, err := svc.CreateWithCredentials(context.Background(), cfg, seal.MustCredentials([]byte("s3cret"))); err != nil {
		t.Fatalf("CreateWithCredentials() returned unexpected error: %v", err)
	}

	// Update barrier config via service.
	barrierCfg := &database.DatabaseBarrierConfig{
		Enabled:        true,
		Mode:           database.BarrierModePassphrase,
		SharesRequired: 3,
		SharesTotal:    5,
	}
	info, err := svc.UpdateBarrier("barrier-db", barrierCfg)
	if err != nil {
		t.Fatalf("UpdateBarrier() returned unexpected error: %v", err)
	}

	if info.Name != "barrier-db" {
		t.Errorf("UpdateBarrier().Name = %q, want %q", info.Name, "barrier-db")
	}
	if info.Barrier == nil {
		t.Fatal("UpdateBarrier().Barrier is nil, want non-nil")
	}
	if !info.Barrier.Enabled {
		t.Error("UpdateBarrier().Barrier.Enabled = false, want true")
	}
	if info.Barrier.Mode != database.BarrierModePassphrase {
		t.Errorf("UpdateBarrier().Barrier.Mode = %q, want %q", info.Barrier.Mode, database.BarrierModePassphrase)
	}
	if info.Barrier.SharesRequired != 3 {
		t.Errorf("UpdateBarrier().Barrier.SharesRequired = %d, want 3", info.Barrier.SharesRequired)
	}
	if info.Barrier.SharesTotal != 5 {
		t.Errorf("UpdateBarrier().Barrier.SharesTotal = %d, want 5", info.Barrier.SharesTotal)
	}

	// Verify Get also reflects the update.
	got, err := svc.Get("barrier-db")
	if err != nil {
		t.Fatalf("Get() returned unexpected error: %v", err)
	}
	if got.Barrier == nil {
		t.Fatal("Get().Barrier is nil after UpdateBarrier, want non-nil")
	}
	if !got.Barrier.Enabled {
		t.Error("Get().Barrier.Enabled = false after UpdateBarrier, want true")
	}
}

func TestDatabaseService_UpdateBarrier_NotFound(t *testing.T) {
	svc := newTestDatabaseService(t)

	barrierCfg := &database.DatabaseBarrierConfig{
		Enabled: true,
		Mode:    "passphrase",
	}
	_, err := svc.UpdateBarrier("nonexistent-barrier-db", barrierCfg)
	if err == nil {
		t.Fatal("UpdateBarrier() returned nil, want *database.NotFoundError")
	}
	var notFoundErr *database.NotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Fatalf("UpdateBarrier() error type = %T, want *database.NotFoundError", err)
	}
	if notFoundErr.Name != "nonexistent-barrier-db" {
		t.Errorf("NotFoundError.Name = %q, want %q", notFoundErr.Name, "nonexistent-barrier-db")
	}
}

// ---------------------------------------------------------------------------
// UpdateConnection tests
// ---------------------------------------------------------------------------

func TestDatabaseService_UpdateConnection_Success(t *testing.T) {
	svc := newTestDatabaseService(t)

	// Create a database first.
	cfg := testDBConfig("conn-db")
	if _, err := svc.Create(cfg); err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}

	// Update connection config via service.
	connCfg := &database.DatabaseConnectionConfig{
		Transport: "grpc",
		Address:   "localhost:9090",
		CAFile:    "/etc/certs/ca.pem",
	}
	info, err := svc.UpdateConnection("conn-db", connCfg)
	if err != nil {
		t.Fatalf("UpdateConnection() returned unexpected error: %v", err)
	}

	if info.Name != "conn-db" {
		t.Errorf("UpdateConnection().Name = %q, want %q", info.Name, "conn-db")
	}
	if info.Connection == nil {
		t.Fatal("UpdateConnection().Connection is nil, want non-nil")
	}
	if info.Connection.Transport != "grpc" {
		t.Errorf("UpdateConnection().Connection.Transport = %q, want %q", info.Connection.Transport, "grpc")
	}
	if info.Connection.Address != "localhost:9090" {
		t.Errorf("UpdateConnection().Connection.Address = %q, want %q", info.Connection.Address, "localhost:9090")
	}
	if info.Connection.CAFile != "/etc/certs/ca.pem" {
		t.Errorf("UpdateConnection().Connection.CAFile = %q, want %q", info.Connection.CAFile, "/etc/certs/ca.pem")
	}

	// Verify Get also reflects the update.
	got, err := svc.Get("conn-db")
	if err != nil {
		t.Fatalf("Get() returned unexpected error: %v", err)
	}
	if got.Connection == nil {
		t.Fatal("Get().Connection is nil after UpdateConnection, want non-nil")
	}
	if got.Connection.Transport != "grpc" {
		t.Errorf("Get().Connection.Transport = %q after UpdateConnection, want %q", got.Connection.Transport, "grpc")
	}
}

func TestDatabaseService_UpdateConnection_NotFound(t *testing.T) {
	svc := newTestDatabaseService(t)

	connCfg := &database.DatabaseConnectionConfig{
		Transport: "grpc",
		Address:   "localhost:9090",
	}
	_, err := svc.UpdateConnection("nonexistent-conn-db", connCfg)
	if err == nil {
		t.Fatal("UpdateConnection() returned nil, want *database.NotFoundError")
	}
	var notFoundErr *database.NotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Fatalf("UpdateConnection() error type = %T, want *database.NotFoundError", err)
	}
	if notFoundErr.Name != "nonexistent-conn-db" {
		t.Errorf("NotFoundError.Name = %q, want %q", notFoundErr.Name, "nonexistent-conn-db")
	}
}

func TestDatabaseService_UpdateConnection_InvalidMissingFields(t *testing.T) {
	svc := newTestDatabaseService(t)

	// Create a database first.
	cfg := testDBConfig("conn-invalid-db")
	if _, err := svc.Create(cfg); err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}

	// Attempt to update with missing required fields.
	connCfg := &database.DatabaseConnectionConfig{
		Transport: "",
		Address:   "",
	}
	_, err := svc.UpdateConnection("conn-invalid-db", connCfg)
	if err == nil {
		t.Fatal("UpdateConnection() returned nil, want *database.ConnectionRequiredError")
	}
	var connErr *database.ConnectionRequiredError
	if !errors.As(err, &connErr) {
		t.Fatalf("UpdateConnection() error type = %T, want *database.ConnectionRequiredError", err)
	}
}

// ---------------------------------------------------------------------------
// SealDatabase / UnsealDatabase / DatabaseBarrierStatus tests
// ---------------------------------------------------------------------------

func TestDatabaseService_SealDatabase_NoBarrierManager(t *testing.T) {
	svc := newTestDatabaseService(t)

	err := svc.SealDatabase(context.Background(), "some-db")
	if err == nil {
		t.Fatal("SealDatabase() returned nil, want *BarrierManagerNotConfiguredError")
	}
	var bmErr *BarrierManagerNotConfiguredError
	if !errors.As(err, &bmErr) {
		t.Fatalf("SealDatabase() error type = %T, want *BarrierManagerNotConfiguredError", err)
	}
}

func TestDatabaseService_DatabaseBarrierStatus_NoBarrierManager(t *testing.T) {
	svc := newTestDatabaseService(t)

	_, err := svc.DatabaseBarrierStatus(context.Background(), "some-db")
	if err == nil {
		t.Fatal("DatabaseBarrierStatus() returned nil, want *BarrierManagerNotConfiguredError")
	}
	var bmErr *BarrierManagerNotConfiguredError
	if !errors.As(err, &bmErr) {
		t.Fatalf("DatabaseBarrierStatus() error type = %T, want *BarrierManagerNotConfiguredError", err)
	}
}

// ---------------------------------------------------------------------------
// Quota enforcement tests
// ---------------------------------------------------------------------------

// mockDatabaseQuotaChecker is a test double for quota.Checker.
type mockDatabaseQuotaChecker struct {
	checkFn func(ctx context.Context, tenantID string, resource quota.Resource) error
}

func (m *mockDatabaseQuotaChecker) Check(ctx context.Context, tenantID string, resource quota.Resource) error {
	if m.checkFn != nil {
		return m.checkFn(ctx, tenantID, resource)
	}
	return nil
}

func (m *mockDatabaseQuotaChecker) CheckN(ctx context.Context, tenantID string, resource quota.Resource, n int64) error {
	return nil
}

func (m *mockDatabaseQuotaChecker) Usage(ctx context.Context, tenantID string, resource quota.Resource) (int64, error) {
	return 0, nil
}

// newTestDatabaseServiceWithQuota creates a DatabaseService wired with the
// given quota.Checker.
func newTestDatabaseServiceWithQuota(t *testing.T, checker quota.Checker) DatabaseServicer {
	t.Helper()
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := database.NewManager(logger, dataDir)
	t.Cleanup(func() { _ = mgr.Close() })
	return NewDatabaseService(mgr, WithQuotaChecker(checker))
}

func TestDatabaseService_CreateForTenant_QuotaExceeded(t *testing.T) {
	exceeded := &quota.ExceededError{
		TenantID: "acme",
		Resource: quota.ResourceDatabases,
		Current:  5,
		Limit:    5,
	}
	checker := &mockDatabaseQuotaChecker{
		checkFn: func(_ context.Context, _ string, _ quota.Resource) error {
			return exceeded
		},
	}

	svc := newTestDatabaseServiceWithQuota(t, checker)
	ctx := tenant.WithTenantID(context.Background(), "acme")

	_, err := svc.CreateForTenant(ctx, testDBConfig("blocked"))
	if err == nil {
		t.Fatal("CreateForTenant() returned nil, want *quota.ExceededError")
	}
	var qErr *quota.ExceededError
	if !errors.As(err, &qErr) {
		t.Fatalf("CreateForTenant() error type = %T, want *quota.ExceededError", err)
	}
	if qErr.Resource != quota.ResourceDatabases {
		t.Errorf("ExceededError.Resource = %v, want ResourceDatabases", qErr.Resource)
	}
}

func TestDatabaseService_CreateForTenant_NoQuotaChecker(t *testing.T) {
	// Without a quota checker, creation must succeed normally.
	svc := newTestDatabaseService(t)
	ctx := tenant.WithTenantID(context.Background(), "acme")

	info, err := svc.CreateForTenant(ctx, testDBConfig("noquota"))
	if err != nil {
		t.Fatalf("CreateForTenant() returned unexpected error: %v", err)
	}
	if info.Name != "noquota" {
		t.Errorf("CreateForTenant().Name = %q, want %q", info.Name, "noquota")
	}
}

func TestDatabaseService_CreateForTenant_QuotaPasses(t *testing.T) {
	var checkedTenantID string
	var checkedResource quota.Resource
	checker := &mockDatabaseQuotaChecker{
		checkFn: func(_ context.Context, tenantID string, resource quota.Resource) error {
			checkedTenantID = tenantID
			checkedResource = resource
			return nil // quota allows creation
		},
	}

	svc := newTestDatabaseServiceWithQuota(t, checker)
	ctx := tenant.WithTenantID(context.Background(), "acme")

	info, err := svc.CreateForTenant(ctx, testDBConfig("allowed"))
	if err != nil {
		t.Fatalf("CreateForTenant() returned unexpected error: %v", err)
	}
	if info.Name != "allowed" {
		t.Errorf("CreateForTenant().Name = %q, want %q", info.Name, "allowed")
	}
	if checkedTenantID != "acme" {
		t.Errorf("quota.Check() tenantID = %q, want %q", checkedTenantID, "acme")
	}
	if checkedResource != quota.ResourceDatabases {
		t.Errorf("quota.Check() resource = %v, want ResourceDatabases", checkedResource)
	}
}

// ---------------------------------------------------------------------------
// Entitlement + raft-group quota enforcement tests
// ---------------------------------------------------------------------------

// mockEntitlementChecker is a test double for quota.EntitlementChecker.
type mockEntitlementChecker struct {
	isolationFn func(ctx context.Context, tenantID, mode string) error
	featureFn   func(ctx context.Context, tenantID, feature string) error
}

func (m *mockEntitlementChecker) CheckIsolation(ctx context.Context, tenantID, mode string) error {
	if m.isolationFn != nil {
		return m.isolationFn(ctx, tenantID, mode)
	}
	return nil
}

func (m *mockEntitlementChecker) CheckFeature(ctx context.Context, tenantID, feature string) error {
	if m.featureFn != nil {
		return m.featureFn(ctx, tenantID, feature)
	}
	return nil
}

func raftDBConfig(name string) database.DatabaseConfig {
	cfg := testDBConfig(name)
	cfg.Backend = database.BackendRaft
	return cfg
}

// TestCreateForTenant_EntitlementRejected verifies a raft-backed create is
// rejected (before the manager is touched) when the tenant's plan does not
// permit the dedicated-raft feature.
func TestCreateForTenant_EntitlementRejected(t *testing.T) {
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := database.NewManager(logger, dataDir)
	t.Cleanup(func() { _ = mgr.Close() })

	entChecker := &mockEntitlementChecker{
		featureFn: func(_ context.Context, tenantID, feature string) error {
			return &quota.EntitlementError{TenantID: tenantID, Feature: feature}
		},
	}
	svc := NewDatabaseService(mgr, WithEntitlementChecker(entChecker))
	ctx := tenant.WithTenantID(context.Background(), "acme")

	_, err := svc.CreateForTenant(ctx, raftDBConfig("dedicated"))
	var entErr *quota.EntitlementError
	if !errors.As(err, &entErr) {
		t.Fatalf("CreateForTenant() error = %v, want *quota.EntitlementError", err)
	}
	if entErr.Feature != quota.FeatureDedicatedRaft {
		t.Errorf("EntitlementError.Feature = %q, want %q", entErr.Feature, quota.FeatureDedicatedRaft)
	}
}

// TestCreateForTenant_IsolationModeRejected verifies the isolation-mode
// entitlement gate fires for raft-backed databases.
func TestCreateForTenant_IsolationModeRejected(t *testing.T) {
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := database.NewManager(logger, dataDir)
	t.Cleanup(func() { _ = mgr.Close() })

	var checkedMode string
	entChecker := &mockEntitlementChecker{
		isolationFn: func(_ context.Context, tenantID, mode string) error {
			checkedMode = mode
			return &quota.EntitlementError{TenantID: tenantID, Mode: mode}
		},
	}
	svc := NewDatabaseService(mgr, WithEntitlementChecker(entChecker))
	ctx := tenant.WithTenantID(context.Background(), "acme")

	_, err := svc.CreateForTenant(ctx, raftDBConfig("dedicated"))
	var entErr *quota.EntitlementError
	if !errors.As(err, &entErr) {
		t.Fatalf("CreateForTenant() error = %v, want *quota.EntitlementError", err)
	}
	if checkedMode != quota.IsolationModeRaft {
		t.Errorf("CheckIsolation mode = %q, want %q", checkedMode, quota.IsolationModeRaft)
	}
}

// TestCreateForTenant_RaftGroupQuotaExceeded verifies that the
// ResourceRaftGroups quota is checked (in addition to ResourceDatabases) for
// raft-backed databases.
func TestCreateForTenant_RaftGroupQuotaExceeded(t *testing.T) {
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := database.NewManager(logger, dataDir)
	t.Cleanup(func() { _ = mgr.Close() })

	var checkedResources []quota.Resource
	checker := &mockDatabaseQuotaChecker{
		checkFn: func(_ context.Context, _ string, resource quota.Resource) error {
			checkedResources = append(checkedResources, resource)
			if resource == quota.ResourceRaftGroups {
				return &quota.ExceededError{Resource: resource, Current: 0, Limit: 0}
			}
			return nil
		},
	}
	svc := NewDatabaseService(mgr,
		WithEntitlementChecker(&mockEntitlementChecker{}),
		WithQuotaChecker(checker),
	)
	ctx := tenant.WithTenantID(context.Background(), "acme")

	_, err := svc.CreateForTenant(ctx, raftDBConfig("dedicated"))
	var qErr *quota.ExceededError
	if !errors.As(err, &qErr) {
		t.Fatalf("CreateForTenant() error = %v, want *quota.ExceededError", err)
	}
	if qErr.Resource != quota.ResourceRaftGroups {
		t.Errorf("ExceededError.Resource = %v, want ResourceRaftGroups", qErr.Resource)
	}
	// Both the databases and raft-groups resources must have been checked.
	sawDatabases, sawRaft := false, false
	for _, r := range checkedResources {
		if r == quota.ResourceDatabases {
			sawDatabases = true
		}
		if r == quota.ResourceRaftGroups {
			sawRaft = true
		}
	}
	if !sawDatabases || !sawRaft {
		t.Errorf("checked resources = %v, want both ResourceDatabases and ResourceRaftGroups", checkedResources)
	}
}

// TestCreateForTenant_SystemTenantBypassesChecks verifies the system tenant
// short-circuits ahead of both the entitlement and quota planes.
func TestCreateForTenant_SystemTenantBypassesChecks(t *testing.T) {
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := database.NewManager(logger, dataDir)
	t.Cleanup(func() { _ = mgr.Close() })

	entChecker := &mockEntitlementChecker{
		featureFn: func(_ context.Context, _, _ string) error {
			t.Fatal("entitlement checker must not be consulted for the system tenant")
			return nil
		},
	}
	svc := NewDatabaseService(mgr, WithEntitlementChecker(entChecker))
	ctx := tenant.WithTenantID(context.Background(), tenantid.System)

	if _, err := svc.CreateForTenant(ctx, testDBConfig("sysdb")); err != nil {
		t.Fatalf("CreateForTenant(system) error = %v", err)
	}
}

// ---------------------------------------------------------------------------
// CreateWithCredentials / CreateForTenantWithCredentials / DatabaseCredentialCreator
// ---------------------------------------------------------------------------

// newTestDatabaseServiceWithBarrier creates a DatabaseService backed by a
// real Manager and a real DatabaseBarrierManager (wrapping a fresh, none
// -strategy system barrier registry), suitable for exercising the credentialed
// create path and Seal/Unseal end to end.
func newTestDatabaseServiceWithBarrier(t *testing.T) (*DatabaseService, *database.Manager) {
	t.Helper()
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := claimingManager(t, logger, dataDir)
	t.Cleanup(func() { _ = mgr.Close() })

	reg, _ := initNoneSystemRegistry(t)
	bm := database.NewDatabaseBarrierManager(logger, reg)
	// Wire the same barrier manager into both the Manager (Create/Open call
	// EnsureBarrier/RegisterBarrier through it) and the DatabaseService
	// (Seal/Unseal/Status read it directly) -- mirroring how
	// pkg/server/lifecycle.BuildServices wires production daemons.
	mgr.SetBarrierManager(bm)

	svc, ok := NewDatabaseService(mgr, WithBarrierManager(bm)).(*DatabaseService)
	if !ok {
		t.Fatal("NewDatabaseService() did not return *DatabaseService")
	}
	return svc, mgr
}

func TestDatabaseService_ImplementsDatabaseCredentialCreator(t *testing.T) {
	svc, _ := newTestDatabaseServiceWithBarrier(t)
	var _ DatabaseCredentialCreator = svc
	if _, ok := DatabaseServicer(svc).(DatabaseCredentialCreator); !ok {
		t.Fatal("*DatabaseService does not satisfy DatabaseCredentialCreator via the DatabaseServicer interface value")
	}
}

func TestDatabaseService_CreateWithCredentials_DedicatedStore(t *testing.T) {
	svc, _ := newTestDatabaseServiceWithBarrier(t)

	cfg := testDBConfig("cred-db")
	cfg.DataDir = t.TempDir()
	cfg.Barrier = &database.DatabaseBarrierConfig{Enabled: true, Mode: database.BarrierModePassphrase}

	info, err := svc.CreateWithCredentials(context.Background(), cfg, seal.MustCredentials([]byte("s3cret-passphrase")))
	if err != nil {
		t.Fatalf("CreateWithCredentials() returned unexpected error: %v", err)
	}
	if info.Barrier == nil || !info.Barrier.Enabled {
		t.Fatal("CreateWithCredentials() info.Barrier.Enabled = false, want true")
	}

	status, err := svc.DatabaseBarrierStatus(context.Background(), "cred-db")
	if err != nil {
		t.Fatalf("DatabaseBarrierStatus() returned unexpected error: %v", err)
	}
	if status.Sealed {
		t.Error("DatabaseBarrierStatus().Sealed = true immediately after CreateWithCredentials, want false (EnsureBarrier initializes+unseals)")
	}
}

func TestDatabaseService_CreateWithCredentials_SharedStoreRejected(t *testing.T) {
	svc, _ := newTestDatabaseServiceWithBarrier(t)

	cfg := testDBConfig("cred-shared-db") // no DataDir: shared store
	cfg.Barrier = &database.DatabaseBarrierConfig{Enabled: true, Mode: database.BarrierModePassphrase}

	_, err := svc.CreateWithCredentials(context.Background(), cfg, seal.MustCredentials([]byte("s3cret")))
	if err == nil {
		t.Fatal("CreateWithCredentials() on a shared-store database = nil, want *SharedStoreBarrierUnsupportedError")
	}
	var sharedErr *database.SharedStoreBarrierUnsupportedError
	if !errors.As(err, &sharedErr) {
		t.Fatalf("error type = %T, want *database.SharedStoreBarrierUnsupportedError", err)
	}
}

func TestDatabaseService_CreateForTenantWithCredentials_SystemTenant(t *testing.T) {
	svc, _ := newTestDatabaseServiceWithBarrier(t)
	ctx := tenant.WithTenantID(context.Background(), tenantid.System)

	cfg := testDBConfig("cred-sys-db")
	cfg.DataDir = t.TempDir()
	cfg.Barrier = &database.DatabaseBarrierConfig{Enabled: true, Mode: database.BarrierModePassphrase}

	info, err := svc.CreateForTenantWithCredentials(ctx, cfg, seal.MustCredentials([]byte("s3cret")))
	if err != nil {
		t.Fatalf("CreateForTenantWithCredentials(system) returned unexpected error: %v", err)
	}
	if info.Name != "cred-sys-db" {
		t.Errorf("Name = %q, want %q", info.Name, "cred-sys-db")
	}
}

// TestDatabaseService_CreateForTenantWithCredentials_IsolatedTenantBarrierRefused
// pins SEC-1: a volume- or raft-isolated tenant serves every database from the
// tenant's own store, never from the per-database store a barrier would wrap.
// The create used to succeed and leave a decorative barrier whose seal
// protected nothing; it must now fail closed with the typed refusal and leave
// no database behind.
func TestDatabaseService_CreateForTenantWithCredentials_IsolatedTenantBarrierRefused(t *testing.T) {
	cases := []struct {
		isolation tenant.IsolationType
		servedBy  string
	}{
		{tenant.IsolationVolume, database.ServedByVolumeTenantStore},
		{tenant.IsolationRaft, database.ServedByRaftTenantGroup},
	}
	for _, c := range cases {
		t.Run(string(c.isolation), func(t *testing.T) {
			base, mgr := newTestDatabaseServiceWithBarrier(t)
			lookup := &fakeTenantInfoLookup{info: &tenant.TenantInfo{
				ID:            "acme",
				IsolationType: c.isolation,
				DataRoot:      t.TempDir(),
			}}
			svc, ok := NewDatabaseService(mgr, WithBarrierManager(base.barrierMgr), WithTenantInfoLookup(lookup)).(*DatabaseService)
			if !ok {
				t.Fatal("NewDatabaseService() did not return *DatabaseService")
			}
			ctx := tenant.WithTenantID(context.Background(), "acme")

			cfg := testDBConfig("cred-tenant-db")
			cfg.Barrier = &database.DatabaseBarrierConfig{Enabled: true, Mode: database.BarrierModePassphrase}

			_, err := svc.CreateForTenantWithCredentials(ctx, cfg, seal.MustCredentials([]byte("s3cret")))
			var off *database.BarrierOffDataPathError
			if !errors.As(err, &off) {
				t.Fatalf("CreateForTenantWithCredentials(%s tenant + barrier) error = %v (%T), want *database.BarrierOffDataPathError", c.isolation, err, err)
			}
			if off.ServedBy != c.servedBy {
				t.Errorf("ServedBy = %q, want %q", off.ServedBy, c.servedBy)
			}
			if _, getErr := mgr.Get("cred-tenant-db"); getErr == nil {
				t.Error("the refused database was left in the catalog")
			}

			// Without a barrier the same tenant's create still succeeds.
			cfg.Barrier = nil
			if _, err := svc.CreateForTenant(ctx, cfg); err != nil {
				t.Fatalf("CreateForTenant(%s tenant, no barrier) error = %v", c.isolation, err)
			}
		})
	}
}

func TestDatabaseService_Create_EmptyCredentials_BarrierDisabledStillWorks(t *testing.T) {
	svc, _ := newTestDatabaseServiceWithBarrier(t)

	// Create (no credentials param) must still work for a database with no
	// barrier requested.
	info, err := svc.Create(testDBConfig("no-cred-db"))
	if err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}
	if info.Barrier != nil {
		t.Errorf("Barrier = %+v, want nil", info.Barrier)
	}
}

// ---------------------------------------------------------------------------
// SealDatabase / UnsealDatabase status propagation
// ---------------------------------------------------------------------------

func TestDatabaseService_SealDatabase_UpdatesDatabaseStatus(t *testing.T) {
	svc, _ := newTestDatabaseServiceWithBarrier(t)

	cfg := testDBConfig("seal-status-db")
	cfg.DataDir = t.TempDir()
	cfg.Barrier = &database.DatabaseBarrierConfig{Enabled: true, Mode: database.BarrierModePassphrase}
	if _, err := svc.CreateWithCredentials(context.Background(), cfg, seal.MustCredentials([]byte("s3cret"))); err != nil {
		t.Fatalf("CreateWithCredentials() error = %v", err)
	}

	info, err := svc.Get("seal-status-db")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if info.Status != database.StatusOpen {
		t.Fatalf("Status() before seal = %q, want %q", info.Status, database.StatusOpen)
	}

	if err := svc.SealDatabase(context.Background(), "seal-status-db"); err != nil {
		t.Fatalf("SealDatabase() returned unexpected error: %v", err)
	}

	info, err = svc.Get("seal-status-db")
	if err != nil {
		t.Fatalf("Get() after seal error = %v", err)
	}
	if info.Status != database.StatusSealed {
		t.Errorf("Status() after SealDatabase = %q, want %q", info.Status, database.StatusSealed)
	}
}

func TestDatabaseService_UnsealDatabase_UpdatesDatabaseStatus(t *testing.T) {
	svc, _ := newTestDatabaseServiceWithBarrier(t)

	cfg := testDBConfig("unseal-status-db")
	cfg.DataDir = t.TempDir()
	cfg.Barrier = &database.DatabaseBarrierConfig{Enabled: true, Mode: database.BarrierModePassphrase}
	secret := []byte("s3cret-passphrase")
	if _, err := svc.CreateWithCredentials(context.Background(), cfg, seal.MustCredentials(bytes.Clone(secret))); err != nil {
		t.Fatalf("CreateWithCredentials() error = %v", err)
	}
	if err := svc.SealDatabase(context.Background(), "unseal-status-db"); err != nil {
		t.Fatalf("SealDatabase() error = %v", err)
	}

	// Wrong secret must fail and leave status sealed.
	if err := svc.UnsealDatabase(context.Background(), "unseal-status-db", seal.MustCredentials([]byte("wrong"))); err == nil {
		t.Fatal("UnsealDatabase() with wrong secret = nil, want error")
	}
	info, err := svc.Get("unseal-status-db")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if info.Status != database.StatusSealed {
		t.Fatalf("Status() after failed unseal = %q, want %q", info.Status, database.StatusSealed)
	}

	// Correct secret succeeds and flips status back to open.
	if err := svc.UnsealDatabase(context.Background(), "unseal-status-db", seal.MustCredentials(bytes.Clone(secret))); err != nil {
		t.Fatalf("UnsealDatabase() with correct secret returned unexpected error: %v", err)
	}
	info, err = svc.Get("unseal-status-db")
	if err != nil {
		t.Fatalf("Get() after unseal error = %v", err)
	}
	if info.Status != database.StatusOpen {
		t.Errorf("Status() after successful unseal = %q, want %q", info.Status, database.StatusOpen)
	}
}
