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
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
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
	if _, err := svc.CreateWithCredentials(context.Background(), cfg, seal.MustCredentials(context.Background(), []byte("s3cret"))); err != nil {
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
	if _, err := svc.Create(testDBConfig("some-db")); err != nil {
		t.Fatal(err)
	}

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
	if _, err := svc.Create(testDBConfig("some-db")); err != nil {
		t.Fatal(err)
	}

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
	mgr := databasetest.NewManager(logger, dataDir)
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
	mgr := databasetest.NewManager(logger, dataDir)
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
	mgr := databasetest.NewManager(logger, dataDir)
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
	mgr := databasetest.NewManager(logger, dataDir)
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
	mgr := databasetest.NewManager(logger, dataDir)
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
	bm, err := database.NewDatabaseBarrierManager(logger, reg.SystemBarrier())
	if err != nil {
		t.Fatal(err)
	}
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

func TestDatabaseService_CreateWithCredentials_DedicatedStore(t *testing.T) {
	svc, _ := newTestDatabaseServiceWithBarrier(t)

	cfg := testDBConfig("cred-db")
	cfg.DataDir = t.TempDir()
	cfg.Barrier = &database.DatabaseBarrierConfig{Enabled: true, Mode: database.BarrierModePassphrase}

	info, err := svc.CreateWithCredentials(context.Background(), cfg, seal.MustCredentials(context.Background(), []byte("s3cret-passphrase")))
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

	_, err := svc.CreateWithCredentials(context.Background(), cfg, seal.MustCredentials(context.Background(), []byte("s3cret")))
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

	info, err := svc.CreateForTenantWithCredentials(ctx, cfg, seal.MustCredentials(context.Background(), []byte("s3cret")))
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
			}}
			svc, ok := NewDatabaseService(mgr, WithBarrierManager(base.barrierMgr), WithTenantInfoLookup(lookup)).(*DatabaseService)
			if !ok {
				t.Fatal("NewDatabaseService() did not return *DatabaseService")
			}
			ctx := tenant.WithTenantID(context.Background(), "acme")

			cfg := testDBConfig("cred-tenant-db")
			cfg.Barrier = &database.DatabaseBarrierConfig{Enabled: true, Mode: database.BarrierModePassphrase}

			_, err := svc.CreateForTenantWithCredentials(ctx, cfg, seal.MustCredentials(context.Background(), []byte("s3cret")))
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
	if _, err := svc.CreateWithCredentials(context.Background(), cfg, seal.MustCredentials(context.Background(), []byte("s3cret"))); err != nil {
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
	if _, err := svc.CreateWithCredentials(context.Background(), cfg, seal.MustCredentials(context.Background(), bytes.Clone(secret))); err != nil {
		t.Fatalf("CreateWithCredentials() error = %v", err)
	}
	if err := svc.SealDatabase(context.Background(), "unseal-status-db"); err != nil {
		t.Fatalf("SealDatabase() error = %v", err)
	}

	// Wrong secret must fail and leave status sealed.
	if err := svc.UnsealDatabase(context.Background(), "unseal-status-db", seal.MustCredentials(context.Background(), []byte("wrong"))); err == nil {
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
	if err := svc.UnsealDatabase(context.Background(), "unseal-status-db", seal.MustCredentials(context.Background(), bytes.Clone(secret))); err != nil {
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

// TestCreateForTenant_IsolationRaft_ClusterModeDefault pins that an
// IsolationRaft tenant create omitting its cluster mode records the node's
// database_defaults.raft.clusterMode, and falls back to local only when the
// node names none.
func TestCreateForTenant_IsolationRaft_ClusterModeDefault(t *testing.T) {
	tests := []struct {
		name     string
		defaults database.Defaults
		want     database.RaftClusterMode
	}{
		{"distributed default", database.Defaults{RaftClusterMode: database.RaftDistributed}, database.RaftDistributed},
		{"no default", database.Defaults{}, database.RaftLocal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
			mgr := databasetest.NewManager(logger, t.TempDir(), database.WithDefaults(tt.defaults))
			mgr.SetShardIDClaimer(testShardClaimer(t))
			t.Cleanup(func() { _ = mgr.Close() })
			store, err := placement.NewStore(newMemPlacementKV())
			if err != nil {
				t.Fatalf("placement.NewStore: %v", err)
			}
			scheduler, err := placement.NewScheduler(&staticTopology{nodes: healthyNodes("n1", "n2", "n3")})
			if err != nil {
				t.Fatalf("placement.NewScheduler: %v", err)
			}
			f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}}
			svc := NewDatabaseService(mgr,
				WithEntitlementChecker(&mockEntitlementChecker{}),
				WithTenantInfoLookup(&fakeTenantInfoLookup{info: raftTenant(t)}),
				WithQuotaChecker(newReplicaQuotaChecker(t, f)),
				WithQuotaUsageRecorder(f),
				WithPlacementStore(store),
				WithPlacementScheduler(scheduler),
				WithDatabaseLogger(logger),
			)
			ctx := tenant.WithTenantID(context.Background(), "rt")

			info, err := svc.CreateForTenant(ctx, database.DatabaseConfig{Name: "orders"})
			if err != nil {
				t.Fatalf("CreateForTenant: %v", err)
			}
			if info.Raft == nil || info.Raft.ClusterMode != tt.want {
				t.Fatalf("Raft = %+v, want cluster mode %q", info.Raft, tt.want)
			}
		})
	}
}

// TestDropForTenant_ResumesWithANewPlacementStoreAfterCatalogRemoval: the
// first drop removes the catalog record, then fails to delete the placement
// entry. The retry runs in a new process -- a newly constructed placement
// Store, which learns names only from the catalog and so cannot name the
// entry -- and still finds and removes it, through the tenant's drop record,
// which it then clears.
func TestDropForTenant_ResumesWithANewPlacementStoreAfterCatalogRemoval(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}}
	svc, _, kv := newFlakyPlacementDatabaseService(t, raftTenant(t), f, healthyNodes("n1"))
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("orders")
	cfg.Replicas = 1
	created, err := svc.CreateForTenant(ctx, cfg)
	if err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	kv.failNextDelete = true
	if err := svc.DropForTenant(ctx, "orders"); err == nil {
		t.Fatal("first DropForTenant should have failed on the placement delete")
	}
	mgr := svc.(*DatabaseService).manager
	if id, err := mgr.DroppedID(ctx, "rt", "orders"); err != nil || id != created.ID {
		t.Fatalf("drop record after the failed teardown = (%q, %v), want %q", id, err, created.ID)
	}

	fresh, err := placement.NewStore(kv)
	if err != nil {
		t.Fatalf("placement.NewStore: %v", err)
	}
	if _, err := fresh.GetByName(ctx, "rt", "orders"); !errors.Is(err, placement.ErrPlacementNotFound) {
		t.Fatalf("a new Store named the entry without the catalog (err = %v); the test would not exercise the drop record", err)
	}
	scheduler, err := placement.NewScheduler(&staticTopology{nodes: healthyNodes("n1")})
	if err != nil {
		t.Fatalf("placement.NewScheduler: %v", err)
	}
	restarted := NewDatabaseService(mgr,
		WithEntitlementChecker(&mockEntitlementChecker{}),
		WithTenantInfoLookup(&fakeTenantInfoLookup{info: raftTenant(t)}),
		WithQuotaChecker(newReplicaQuotaChecker(t, f)),
		WithQuotaUsageRecorder(f),
		WithPlacementStore(fresh),
		WithPlacementScheduler(scheduler),
		WithDatabaseLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)

	if err := restarted.DropForTenant(ctx, "orders"); err != nil {
		t.Fatalf("resumed DropForTenant: %v", err)
	}
	if _, err := fresh.Get(ctx, "rt", created.ID); !errors.Is(err, placement.ErrPlacementNotFound) {
		t.Fatalf("placement entry after the resumed drop: err = %v, want ErrPlacementNotFound", err)
	}
	if id, err := mgr.DroppedID(ctx, "rt", "orders"); err != nil || id != "" {
		t.Fatalf("drop record after the resumed drop = (%q, %v), want cleared", id, err)
	}
}

// deleteRefusingCatalog fails every Delete, so a refused create's cleanup
// cannot remove its provisional record and strands it in the catalog.
type deleteRefusingCatalog struct {
	database.Catalog
	err error
}

func (c deleteRefusingCatalog) Delete(context.Context, string, string, string, string) error {
	return c.err
}

// A drop on a service with no placement store reclaims a stranded
// provisional record instead of answering NotFound for ever, so the name
// can be created again.
func TestDropForTenant_ReclaimsAStrandedProvisionalRecord(t *testing.T) {
	mgr := newCoverageDatabaseManager(t)
	svc := NewDatabaseService(mgr)
	cat := databasetest.Catalog(mgr.DataDir())

	mgr.SetCatalog(deleteRefusingCatalog{Catalog: cat, err: errors.New("catalog delete timed out")})
	refused := database.WithCreateGuard(context.Background(), func(context.Context) error { return errors.New("refused") })
	_, err := mgr.CreateWithCredentials(refused,
		database.DatabaseConfig{TenantID: "acme", Name: "orders", Backend: database.BackendMemory}, seal.Credentials{})
	var orphan *database.CatalogOrphanError
	if !errors.As(err, &orphan) {
		t.Fatalf("refused create = %v, want *database.CatalogOrphanError", err)
	}
	mgr.SetCatalog(cat)
	meta, err := cat.Get(context.Background(), "acme", "orders")
	if err != nil {
		t.Fatalf("catalog Get of the stranded record: %v", err)
	}
	if !meta.Provisional {
		t.Fatal("stranded record is not provisional; the test would not exercise the reclaim")
	}

	ctx := tenant.WithTenantID(context.Background(), "acme")
	if err := svc.DropForTenant(ctx, "orders"); err != nil {
		t.Fatalf("DropForTenant of the stranded name: %v", err)
	}
	var notFound *database.NotFoundError
	if _, err := cat.Get(context.Background(), "acme", "orders"); !errors.As(err, &notFound) {
		t.Fatalf("catalog Get after the reclaim = %v, want *database.NotFoundError", err)
	}
	if err := svc.DropForTenant(ctx, "orders"); !errors.As(err, &notFound) {
		t.Fatalf("second DropForTenant = %v, want *database.NotFoundError", err)
	}
	if _, err := svc.CreateForTenant(ctx, testDBConfig("orders")); err != nil {
		t.Fatalf("CreateForTenant after the reclaim: %v", err)
	}
}

// strictRaftTenant is a raft-isolated tenant whose own isolation policy is
// strict (docs/multi-tenancy/engineering.md phase 5, G1).
func strictRaftTenant() *tenant.TenantInfo {
	return &tenant.TenantInfo{ID: "rt", IsolationType: tenant.IsolationRaft, IsolationPolicy: tenant.TenantIsolationPolicyStrict}
}

// TestCreateForTenant_StrictTenant_SharedWALRefused: a strict tenant's raft
// database requested without a dedicated WAL would put its log on the shared
// WAL under a node key; the create is refused with a typed error before any
// quota is reserved.
func TestCreateForTenant_StrictTenant_SharedWALRefused(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 10, MaxDedicatedWALGroups: 5}}
	svc := newReplicaDatabaseService(t, strictRaftTenant(), f)
	ctx := tenant.WithTenantID(context.Background(), "rt")

	_, err := svc.CreateForTenant(ctx, testDBConfig("app"))
	var refused *database.StrictTenantDedicatedWALRequiredError
	if !errors.As(err, &refused) {
		t.Fatalf("CreateForTenant err = %v, want *database.StrictTenantDedicatedWALRequiredError", err)
	}
	if refused.Name != "app" || refused.Tenant != "rt" {
		t.Errorf("refusal = %+v, want Name app, Tenant rt", refused)
	}
	if f.incWAL != 0 {
		t.Errorf("dedicated-WAL increment = %d, want 0", f.incWAL)
	}
}

// TestCreateForTenant_StrictTenant_DedicatedWALCreated: with the dedicated WAL
// requested, the strict tenant's raft database is created on it.
func TestCreateForTenant_StrictTenant_DedicatedWALCreated(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 10, MaxDedicatedWALGroups: 5}}
	svc := newReplicaDatabaseService(t, strictRaftTenant(), f)
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("app")
	cfg.DedicatedWAL = true
	got, err := svc.CreateForTenant(ctx, cfg)
	if err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	if !got.DedicatedWAL {
		t.Errorf("persisted DedicatedWAL = false, want true")
	}
	if f.incWAL != 1 {
		t.Errorf("dedicated-WAL increment = %d, want 1", f.incWAL)
	}
}

// TestCreateForTenant_StrictVolumeTenant_Created: a strict volume tenant's
// log is already under its own key on its own host, so its create succeeds
// without a per-database WAL (which a volume tenant cannot carry) and meters
// no dedicated-WAL group.
func TestCreateForTenant_StrictVolumeTenant_Created(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 10, MaxDedicatedWALGroups: 5}}
	info := &tenant.TenantInfo{ID: "vt", IsolationType: tenant.IsolationVolume, IsolationPolicy: tenant.TenantIsolationPolicyStrict}
	svc := newReplicaDatabaseService(t, info, f)
	ctx := tenant.WithTenantID(context.Background(), "vt")

	got, err := svc.CreateForTenant(ctx, testDBConfig("app"))
	if err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	if got.DedicatedWAL {
		t.Errorf("persisted DedicatedWAL = true, want false")
	}
	if f.incWAL != 0 {
		t.Errorf("dedicated-WAL increment = %d, want 0", f.incWAL)
	}
}

// TestCreateDatabase_StrictTenantRefusedUntilStrictIsolationActive: until
// the cluster activates strict-isolation (a mixed-version cluster), a strict
// tenant's database create is refused with the gate's *feature.NotActiveError
// and nothing is persisted or reserved; once the gate passes, today's
// behaviour (the dedicated-WAL create) holds.
func TestCreateDatabase_StrictTenantRefusedUntilStrictIsolationActive(t *testing.T) {
	ctx := tenant.WithTenantID(context.Background(), "rt")
	cfg := testDBConfig("app")
	cfg.DedicatedWAL = true

	t.Run("inactive", func(t *testing.T) {
		f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 10, MaxDedicatedWALGroups: 5}}
		svc := newReplicaDatabaseService(t, strictRaftTenant(), f)
		WithStrictIsolationGate(func() error {
			return &feature.NotActiveError{Feature: featuregate.StrictIsolation}
		})(svc.(*DatabaseService))

		_, err := svc.CreateForTenant(ctx, cfg)
		var notActive *feature.NotActiveError
		if !errors.As(err, &notActive) || notActive.Feature != featuregate.StrictIsolation {
			t.Fatalf("CreateForTenant before activation = %v, want NotActiveError(%s)", err, featuregate.StrictIsolation)
		}
		if f.incWAL != 0 {
			t.Errorf("dedicated-WAL increment = %d, want 0", f.incWAL)
		}
		if got, gerr := svc.GetForTenant(ctx, "app"); gerr == nil {
			t.Errorf("refused create persisted %+v", got)
		}
	})

	t.Run("active", func(t *testing.T) {
		f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 10, MaxDedicatedWALGroups: 5}}
		svc := newReplicaDatabaseService(t, strictRaftTenant(), f)
		WithStrictIsolationGate(func() error { return nil })(svc.(*DatabaseService))

		got, err := svc.CreateForTenant(ctx, cfg)
		if err != nil {
			t.Fatalf("CreateForTenant after activation: %v", err)
		}
		if !got.DedicatedWAL || f.incWAL != 1 {
			t.Errorf("created DedicatedWAL=%v incWAL=%d, want true, 1", got.DedicatedWAL, f.incWAL)
		}
	})
}

// TestCreateForTenant_StrictTenant_QuotaStillApplies: the strict policy does
// not bypass the MaxDedicatedWALGroups quota.
func TestCreateForTenant_StrictTenant_QuotaStillApplies(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 10, MaxDedicatedWALGroups: 1, CurrentDedicatedWALGroups: 1}}
	svc := newReplicaDatabaseService(t, strictRaftTenant(), f)
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("app")
	cfg.DedicatedWAL = true
	_, err := svc.CreateForTenant(ctx, cfg)
	var exceeded *quota.ExceededError
	if !errors.As(err, &exceeded) || exceeded.Resource != quota.ResourceDedicatedWALGroups {
		t.Fatalf("CreateForTenant err = %v, want the dedicated-WAL quota refusal", err)
	}
}

// fixedDatabaseFloor is a tenant.ClusterIsolationPolicyReader returning a
// fixed cluster floor.
type fixedDatabaseFloor struct{ policy tenant.ClusterIsolationPolicy }

func (r fixedDatabaseFloor) ClusterIsolationPolicy(context.Context) (tenant.ClusterIsolationPolicy, error) {
	return r.policy, nil
}

// TestCreateForTenant_StrictFloor_SharedWALRefused: under a strict cluster
// floor every isolated tenant is strict, whatever its own policy.
func TestCreateForTenant_StrictFloor_SharedWALRefused(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 10, MaxDedicatedWALGroups: 5}}
	svc := newReplicaDatabaseService(t, raftTenant(t), f)
	WithDatabaseIsolationPolicyReader(fixedDatabaseFloor{policy: tenant.ClusterIsolationPolicyStrict})(svc.(*DatabaseService))
	ctx := tenant.WithTenantID(context.Background(), "rt")

	_, err := svc.CreateForTenant(ctx, testDBConfig("app"))
	var refused *database.StrictTenantDedicatedWALRequiredError
	if !errors.As(err, &refused) {
		t.Fatalf("CreateForTenant err = %v, want *database.StrictTenantDedicatedWALRequiredError", err)
	}
}

// TestCreateForTenant_NonStrictTenant_SharedWALAllowed: an isolated tenant
// under operator-access keeps today's behaviour (shared WAL allowed).
func TestCreateForTenant_NonStrictTenant_SharedWALAllowed(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 10, MaxDedicatedWALGroups: 5}}
	svc := newReplicaDatabaseService(t, raftTenant(t), f)
	WithDatabaseIsolationPolicyReader(fixedDatabaseFloor{policy: tenant.ClusterIsolationPolicyOperatorAccess})(svc.(*DatabaseService))
	ctx := tenant.WithTenantID(context.Background(), "rt")

	got, err := svc.CreateForTenant(ctx, testDBConfig("app"))
	if err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	if got.DedicatedWAL {
		t.Errorf("persisted DedicatedWAL = true, want false")
	}
}

// tighteningLookup is a tenant lookup that is also the
// tenant.IsolationTighteningReader a create re-reads after persisting a
// database on the shared WAL. afterFirst is the tenant record every read but
// the first returns: a tightening that committed while the create ran.
type tighteningLookup struct {
	info       *tenant.TenantInfo
	afterFirst *tenant.TenantInfo
	marker     bool
	reads      int
}

func (l *tighteningLookup) GetTenant(context.Context, string) (*tenant.TenantInfo, error) {
	l.reads++
	if l.reads > 1 && l.afterFirst != nil {
		return l.afterFirst, nil
	}
	return l.info, nil
}

func (l *tighteningLookup) IsolationTightening(context.Context, string) (bool, error) {
	return l.marker, nil
}

func looseRaftTenant() *tenant.TenantInfo {
	return &tenant.TenantInfo{ID: "rt", IsolationType: tenant.IsolationRaft}
}

func newTighteningRaceService(t *testing.T, l *tighteningLookup) DatabaseServicer {
	t.Helper()
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 10, MaxDedicatedWALGroups: 5}}
	svc := newReplicaDatabaseService(t, l.info, f)
	svc.(*DatabaseService).tenantInfo = l
	return svc
}

// TestCreateForTenant_SharedWALRolledBackUnderATighteningMarker: a create on
// the shared WAL that, after its catalog write, finds a tightening to strict
// in flight rolls itself back, so the tightening -- which may have listed the
// databases before that write -- never commits over a node-keyed log.
func TestCreateForTenant_SharedWALRolledBackUnderATighteningMarker(t *testing.T) {
	l := &tighteningLookup{info: looseRaftTenant(), marker: true}
	svc := newTighteningRaceService(t, l)
	ctx := tenant.WithTenantID(context.Background(), "rt")

	_, err := svc.CreateForTenant(ctx, testDBConfig("app"))
	var refused *database.StrictTenantDedicatedWALRequiredError
	if !errors.As(err, &refused) || refused.Name != "app" || refused.Tenant != "rt" {
		t.Fatalf("CreateForTenant under a tightening marker = %v, want *StrictTenantDedicatedWALRequiredError", err)
	}
	if got, gerr := svc.GetForTenant(ctx, "app"); gerr == nil {
		t.Fatalf("refused create left %+v in the catalog", got)
	}
}

// TestCreateForTenant_SharedWALRolledBackWhenStrictCommittedMeanwhile: a
// tightening that committed strict between the create's first read and its
// catalog write is seen by the re-read, and the create rolls back.
func TestCreateForTenant_SharedWALRolledBackWhenStrictCommittedMeanwhile(t *testing.T) {
	strict := looseRaftTenant()
	strict.IsolationPolicy = tenant.TenantIsolationPolicyStrict
	l := &tighteningLookup{info: looseRaftTenant(), afterFirst: strict}
	svc := newTighteningRaceService(t, l)
	ctx := tenant.WithTenantID(context.Background(), "rt")

	_, err := svc.CreateForTenant(ctx, testDBConfig("app"))
	var refused *database.StrictTenantDedicatedWALRequiredError
	if !errors.As(err, &refused) {
		t.Fatalf("CreateForTenant after strict committed = %v, want *StrictTenantDedicatedWALRequiredError", err)
	}
	if got, gerr := svc.GetForTenant(ctx, "app"); gerr == nil {
		t.Fatalf("refused create left %+v in the catalog", got)
	}
}

// TestCreateForTenant_SharedWALKeptWithNoTightening: with no tightening in
// flight the create stands, and the probe a tightening consults lists it.
func TestCreateForTenant_SharedWALKeptWithNoTightening(t *testing.T) {
	l := &tighteningLookup{info: looseRaftTenant()}
	svc := newTighteningRaceService(t, l)
	ctx := tenant.WithTenantID(context.Background(), "rt")

	if _, err := svc.CreateForTenant(ctx, testDBConfig("app")); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	dbs, err := NewNodeKeyLogProbe(svc.(*DatabaseService).manager).NodeKeyLogDatabases(context.Background(), "rt")
	if err != nil || len(dbs) != 1 || dbs[0] != "app" {
		t.Fatalf("NodeKeyLogDatabases = (%v, %v), want [app]", dbs, err)
	}
}

// windowLookup models a tightening that commits strict and removes its
// marker just before the create reads the markers: the marker read finds
// none, and every tenant read after it returns the committed strict policy.
type windowLookup struct {
	info      *tenant.TenantInfo
	committed bool
}

func (l *windowLookup) GetTenant(context.Context, string) (*tenant.TenantInfo, error) {
	if l.committed {
		strict := *l.info
		strict.IsolationPolicy = tenant.TenantIsolationPolicyStrict
		return &strict, nil
	}
	return l.info, nil
}

func (l *windowLookup) IsolationTightening(context.Context, string) (bool, error) {
	l.committed = true
	return false, nil
}

// TestCreateForTenant_SharedWALRolledBackWhenTheMarkerIsRemovedAfterCommit is
// Codex round 3 on #1576: the create reads the markers before a fresh policy
// read, so a tightening that committed strict and removed its marker between
// the create's catalog write and its marker read is still seen. Read the
// other way round, the create saw neither and kept a node-keyed log. Both a
// raft tenant and a barrier tenant on the shared store are governed.
func TestCreateForTenant_SharedWALRolledBackWhenTheMarkerIsRemovedAfterCommit(t *testing.T) {
	for _, info := range []*tenant.TenantInfo{
		looseRaftTenant(),
		{ID: "rt", IsolationType: tenant.IsolationBarrier},
	} {
		t.Run(string(info.IsolationType), func(t *testing.T) {
			l := &windowLookup{info: info}
			f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 10, MaxDedicatedWALGroups: 5}}
			svc := newReplicaDatabaseService(t, info, f)
			svc.(*DatabaseService).tenantInfo = l
			ctx := tenant.WithTenantID(context.Background(), "rt")

			cfg := testDBConfig("app")
			cfg.Backend = database.BackendRaft
			_, err := svc.CreateForTenant(ctx, cfg)
			var refused *database.StrictTenantDedicatedWALRequiredError
			if !errors.As(err, &refused) || refused.Name != "app" || refused.Tenant != "rt" {
				t.Fatalf("CreateForTenant across a tightening's commit = %v, want *StrictTenantDedicatedWALRequiredError", err)
			}
			if got, gerr := svc.GetForTenant(ctx, "app"); gerr == nil {
				t.Fatalf("refused create left %+v in the catalog", got)
			}
		})
	}
}

// TestCreateForTenant_BarrierSharedWALRolledBackUnderATighteningMarker: a
// barrier tenant's raft database on the shared store meets the same
// handshake as a raft tenant's.
func TestCreateForTenant_BarrierSharedWALRolledBackUnderATighteningMarker(t *testing.T) {
	info := &tenant.TenantInfo{ID: "rt", IsolationType: tenant.IsolationBarrier}
	l := &tighteningLookup{info: info, marker: true}
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 10, MaxDedicatedWALGroups: 5}}
	svc := newReplicaDatabaseService(t, info, f)
	svc.(*DatabaseService).tenantInfo = l
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("app")
	cfg.Backend = database.BackendRaft
	_, err := svc.CreateForTenant(ctx, cfg)
	var refused *database.StrictTenantDedicatedWALRequiredError
	if !errors.As(err, &refused) {
		t.Fatalf("CreateForTenant(barrier, shared store) under a tightening marker = %v, want *StrictTenantDedicatedWALRequiredError", err)
	}
}

// publishedProbeLookup is a tighteningLookup that, when the create reads the
// tightening markers, records whether the database is already published --
// resolvable, and so writable, through the manager.
type publishedProbeLookup struct {
	tighteningLookup
	mgr       *database.Manager
	published bool
	probed    bool
}

func (l *publishedProbeLookup) IsolationTightening(ctx context.Context, tenantID string) (bool, error) {
	l.probed = true
	if db, err := l.mgr.GetOwned("app", tenantID); err == nil && db != nil {
		l.published = true
	}
	return l.tighteningLookup.IsolationTightening(ctx, tenantID)
}

// TestCreateForTenant_StrictCheckRunsBeforeTheDatabaseIsWritable is Codex
// round 2 on #1576: the strict-isolation handshake runs after the catalog
// write but before the database is opened, unfenced or published, so a
// create refused under a tightening was never writable -- no concurrent
// request could land data on the node-keyed log before the refusal.
func TestCreateForTenant_StrictCheckRunsBeforeTheDatabaseIsWritable(t *testing.T) {
	for _, info := range []*tenant.TenantInfo{
		looseRaftTenant(),
		{ID: "rt", IsolationType: tenant.IsolationBarrier},
	} {
		t.Run(string(info.IsolationType), func(t *testing.T) {
			l := &publishedProbeLookup{tighteningLookup: tighteningLookup{info: info, marker: true}}
			f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 10, MaxDedicatedWALGroups: 5}}
			svc := newReplicaDatabaseService(t, info, f)
			svc.(*DatabaseService).tenantInfo = l
			l.mgr = svc.(*DatabaseService).manager
			ctx := tenant.WithTenantID(context.Background(), "rt")

			cfg := testDBConfig("app")
			cfg.Backend = database.BackendRaft
			_, err := svc.CreateForTenant(ctx, cfg)
			var refused *database.StrictTenantDedicatedWALRequiredError
			if !errors.As(err, &refused) {
				t.Fatalf("CreateForTenant under a tightening marker = %v, want *StrictTenantDedicatedWALRequiredError", err)
			}
			if !l.probed {
				t.Fatal("the create never read the tightening markers")
			}
			if l.published {
				t.Fatal("the database was published, and so writable, before the strict-isolation check refused it")
			}
			if got, gerr := svc.GetForTenant(ctx, "app"); gerr == nil {
				t.Fatalf("refused create left %+v in the catalog", got)
			}
		})
	}
}

// newVolumeTenantService is a DatabaseService for one volume-isolated
// tenant, "acme", whose data root is dataRoot.
func newVolumeTenantService(t *testing.T, dataRoot string) (DatabaseServicer, *database.Manager, context.Context) {
	t.Helper()
	mgr := databasetest.NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	lookup := &fakeTenantInfoLookup{info: &tenant.TenantInfo{
		ID:            "acme",
		IsolationType: tenant.IsolationVolume,
		VolumePool:    "default",
	}}
	svc := NewDatabaseService(mgr, WithTenantInfoLookup(lookup),
		WithTenantDataRootResolver(&fakeDataRootResolver{roots: map[string]string{"acme": dataRoot}}))
	return svc, mgr, tenant.WithTenantID(context.Background(), "acme")
}

// TestDatabaseService_VolumeTenant_RefusesDedicatedWAL is the Codex finding
// on #1517: a volume tenant's databases are served through the tenant's
// shared shard, so a raft database with a dedicated WAL there would persist
// and meter a group that never carries its data. The create is refused with
// a typed error before anything is made.
func TestDatabaseService_VolumeTenant_RefusesDedicatedWAL(t *testing.T) {
	dataRoot := t.TempDir()
	svc, _, ctx := newVolumeTenantService(t, dataRoot)

	cfg := testDBConfig("orders")
	cfg.Backend = database.BackendRaft
	cfg.Raft = &database.DatabaseRaftConfig{ClusterMode: database.RaftLocal}
	cfg.DedicatedWAL = true
	_, err := svc.CreateForTenant(ctx, cfg)
	var offPath *database.DedicatedWALOffDataPathError
	if !errors.As(err, &offPath) {
		t.Fatalf("CreateForTenant(raft, dedicated WAL) error = %v (%T), want *database.DedicatedWALOffDataPathError", err, err)
	}
	if offPath.ServedBy != database.ServedByVolumeTenantStore || offPath.Tenant != "acme" {
		t.Fatalf("refusal = %+v, want the volume tenant's store named", offPath)
	}
	if _, statErr := os.Stat(filepath.Join(dataRoot, "databases", "orders")); !os.IsNotExist(statErr) {
		t.Fatalf("the refused create touched its data dir: %v", statErr)
	}
}

// A volume tenant's database without a dedicated WAL still creates and is
// served from the tenant's store.
func TestDatabaseService_VolumeTenant_CreatesWithoutDedicatedWAL(t *testing.T) {
	dataRoot := t.TempDir()
	svc, mgr, ctx := newVolumeTenantService(t, dataRoot)

	if _, err := svc.CreateForTenant(ctx, testDBConfig("orders")); err != nil {
		t.Fatalf("CreateForTenant(no dedicated WAL) = %v, want nil", err)
	}
	if _, err := mgr.GetForTenant(ctx, "orders"); err != nil {
		t.Fatalf("GetForTenant(orders) = %v, want the created database served", err)
	}
}

// barrierTenant is a barrier-isolated tenant, "bt", under the given policy.
func barrierTenant(policy tenant.TenantIsolationPolicy) *tenant.TenantInfo {
	return &tenant.TenantInfo{ID: "bt", IsolationType: tenant.IsolationBarrier, IsolationPolicy: policy}
}

// TestDatabaseService_BarrierTenant_RefusesDedicatedWAL is Codex round 4 on
// #1517: restServiceRegistry.resolve serves a barrier tenant's raft-backed
// database from the shared system store (a raft database never has a
// per-database barrier route), so a dedicated group and WAL would be
// persisted and metered while every KV write still reached the shared WAL.
// The create is refused with the typed error, nothing is metered, and the
// name stays free. A strict barrier tenant gets the same refusal, not
// StrictTenantDedicatedWALRequiredError.
func TestDatabaseService_BarrierTenant_RefusesDedicatedWAL(t *testing.T) {
	for _, policy := range []tenant.TenantIsolationPolicy{"", tenant.TenantIsolationPolicyStrict} {
		t.Run("policy="+string(policy), func(t *testing.T) {
			f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 10, MaxDedicatedWALGroups: 5}}
			svc := newReplicaDatabaseService(t, barrierTenant(policy), f)
			ctx := tenant.WithTenantID(context.Background(), "bt")

			cfg := testDBConfig("orders")
			cfg.Backend = database.BackendRaft
			cfg.DedicatedWAL = true
			_, err := svc.CreateForTenant(ctx, cfg)
			var offPath *database.DedicatedWALOffDataPathError
			if !errors.As(err, &offPath) {
				t.Fatalf("CreateForTenant(raft, dedicated WAL) error = %v (%T), want *database.DedicatedWALOffDataPathError", err, err)
			}
			if offPath.ServedBy != database.ServedBySharedStore || offPath.Tenant != "bt" {
				t.Fatalf("refusal = %+v, want the shared store named", offPath)
			}
			if f.incWAL != 0 || f.incRaft != 0 || f.incTotal != 0 {
				t.Fatalf("refused create metered: wal=%d raft=%d total=%d, want 0", f.incWAL, f.incRaft, f.incTotal)
			}
			if policy == tenant.TenantIsolationPolicyStrict {
				return
			}
			// Nothing was persisted: the same name creates once the
			// dedicated WAL is dropped from the request.
			cfg.DedicatedWAL = false
			got, err := svc.CreateForTenant(ctx, cfg)
			if err != nil {
				t.Fatalf("CreateForTenant(no dedicated WAL) after the refusal = %v, want nil", err)
			}
			if got.DedicatedWAL || f.incWAL != 0 {
				t.Fatalf("created DedicatedWAL=%v incWAL=%d, want a shared-WAL database", got.DedicatedWAL, f.incWAL)
			}
		})
	}
}

// TestDatabaseService_RaftTenant_DedicatedWALStillCreates pins the one
// isolation that serves a database from its own group: the off-data-path
// refusal leaves IsolationRaft + DedicatedWAL alone.
func TestDatabaseService_RaftTenant_DedicatedWALStillCreates(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 10, MaxDedicatedWALGroups: 5}}
	svc := newReplicaDatabaseService(t, raftTenant(t), f)
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("orders")
	cfg.Backend = database.BackendRaft
	cfg.DedicatedWAL = true
	got, err := svc.CreateForTenant(ctx, cfg)
	if err != nil {
		t.Fatalf("CreateForTenant(raft tenant, dedicated WAL) = %v, want nil", err)
	}
	if !got.DedicatedWAL || f.incWAL != 1 {
		t.Fatalf("created DedicatedWAL=%v incWAL=%d, want a dedicated WAL metered once", got.DedicatedWAL, f.incWAL)
	}
}

// TestDatabaseService_OffPathDedicatedWAL_RefusedUnderARaftDefault is Codex
// round 3 on #1576: a request that omits its backend on a node whose
// database_defaults name raft resolves to a raft database, so a dedicated WAL
// on it is judged as the raft database it becomes. Both a volume and a
// barrier tenant -- neither serves a database from its own group -- are
// refused before anything is made.
func TestDatabaseService_OffPathDedicatedWAL_RefusedUnderARaftDefault(t *testing.T) {
	for _, tc := range []struct {
		name     string
		info     *tenant.TenantInfo
		servedBy string
	}{
		{name: "volume", info: &tenant.TenantInfo{ID: "acme", IsolationType: tenant.IsolationVolume, VolumePool: "default"}, servedBy: database.ServedByVolumeTenantStore},
		{name: "barrier", info: &tenant.TenantInfo{ID: "acme", IsolationType: tenant.IsolationBarrier}, servedBy: database.ServedBySharedStore},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dataRoot := t.TempDir()
			mgr := databasetest.NewManager(nil, t.TempDir(), database.WithDefaults(database.Defaults{Backend: database.BackendRaft}))
			t.Cleanup(func() { _ = mgr.Close() })
			svc := NewDatabaseService(mgr, WithTenantInfoLookup(&fakeTenantInfoLookup{info: tc.info}),
				WithTenantDataRootResolver(&fakeDataRootResolver{roots: map[string]string{"acme": dataRoot}}))
			ctx := tenant.WithTenantID(context.Background(), "acme")

			cfg := testDBConfig("orders")
			cfg.Backend = ""
			cfg.Raft = &database.DatabaseRaftConfig{ClusterMode: database.RaftLocal}
			cfg.DedicatedWAL = true
			_, err := svc.CreateForTenant(ctx, cfg)
			var offPath *database.DedicatedWALOffDataPathError
			if !errors.As(err, &offPath) {
				t.Fatalf("CreateForTenant(omitted backend under a raft default, dedicated WAL) error = %v (%T), want *database.DedicatedWALOffDataPathError", err, err)
			}
			if offPath.ServedBy != tc.servedBy || offPath.Tenant != "acme" {
				t.Fatalf("refusal = %+v, want %s named", offPath, tc.servedBy)
			}
			if _, gerr := mgr.GetForTenant(ctx, "orders"); gerr == nil {
				t.Fatal("the refused create left the database in the catalog")
			}
		})
	}
}
