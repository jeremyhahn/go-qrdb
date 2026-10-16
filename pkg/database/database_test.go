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

package database

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// newTestDatabase creates a Database backed by in-memory storage for testing.
func newTestDatabase(name string, backend BackendType) *Database {
	store := memory.NewStorage()
	b := NewStandaloneBackend(store)
	cfg := DatabaseConfig{
		Name:    name,
		Backend: backend,
	}
	return NewDatabase(cfg, b, "/tmp/test/"+name)
}

func TestDatabase_NewDatabase(t *testing.T) {
	db := newTestDatabase("mydb", BackendMemory)

	if got := db.Name(); got != "mydb" {
		t.Errorf("Name() = %q, want %q", got, "mydb")
	}
	if got := db.BackendType(); got != BackendMemory {
		t.Errorf("BackendType() = %q, want %q", got, BackendMemory)
	}
	if got := db.Status(); got != StatusOpen {
		t.Errorf("Status() = %q, want %q", got, StatusOpen)
	}
}

func TestDatabase_NewDatabase_AllBackendTypes(t *testing.T) {
	tests := []BackendType{BackendMemory, BackendFile, BackendPebble}
	for _, backend := range tests {
		t.Run(string(backend), func(t *testing.T) {
			db := newTestDatabase("db-"+string(backend), backend)
			if got := db.BackendType(); got != backend {
				t.Errorf("BackendType() = %q, want %q", got, backend)
			}
		})
	}
}

func TestDatabase_Info(t *testing.T) {
	db := newTestDatabase("info-db", BackendPebble)

	info := db.Info()

	if info.Name != "info-db" {
		t.Errorf("Info().Name = %q, want %q", info.Name, "info-db")
	}
	if info.Backend != BackendPebble {
		t.Errorf("Info().Backend = %q, want %q", info.Backend, BackendPebble)
	}
	if info.Status != StatusOpen {
		t.Errorf("Info().Status = %q, want %q", info.Status, StatusOpen)
	}
	if info.DataDir != "/tmp/test/info-db" {
		t.Errorf("Info().DataDir = %q, want %q", info.DataDir, "/tmp/test/info-db")
	}
}

func TestDatabase_Info_ReflectsStatusChange(t *testing.T) {
	db := newTestDatabase("status-info-db", BackendMemory)

	infoBefore := db.Info()
	if infoBefore.Status != StatusOpen {
		t.Fatalf("Info().Status before close = %q, want %q", infoBefore.Status, StatusOpen)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("Close() returned unexpected error: %v", err)
	}

	infoAfter := db.Info()
	if infoAfter.Status != StatusClosed {
		t.Errorf("Info().Status after close = %q, want %q", infoAfter.Status, StatusClosed)
	}
}

func TestDatabase_Close(t *testing.T) {
	db := newTestDatabase("close-db", BackendMemory)

	if err := db.Close(); err != nil {
		t.Fatalf("Close() returned unexpected error: %v", err)
	}
	if got := db.Status(); got != StatusClosed {
		t.Errorf("Status() after Close() = %q, want %q", got, StatusClosed)
	}
}

func TestDatabase_Close_NotOpen(t *testing.T) {
	db := newTestDatabase("close-notopen-db", BackendMemory)

	if err := db.Close(); err != nil {
		t.Fatalf("first Close() returned unexpected error: %v", err)
	}

	err := db.Close()
	if err == nil {
		t.Fatal("second Close() returned nil, want *NotOpenError")
	}
	var notOpenErr *NotOpenError
	if !errors.As(err, &notOpenErr) {
		t.Fatalf("second Close() error type = %T, want *NotOpenError", err)
	}
	if notOpenErr.Name != "close-notopen-db" {
		t.Errorf("NotOpenError.Name = %q, want %q", notOpenErr.Name, "close-notopen-db")
	}
}

func TestDatabase_Config(t *testing.T) {
	db := newTestDatabase("config-db", BackendFile)

	cfg := db.Config()

	if cfg.Name != "config-db" {
		t.Errorf("Config().Name = %q, want %q", cfg.Name, "config-db")
	}
	if cfg.Backend != BackendFile {
		t.Errorf("Config().Backend = %q, want %q", cfg.Backend, BackendFile)
	}
}

func TestDatabase_Config_ReturnsCopy(t *testing.T) {
	db := newTestDatabase("copy-db", BackendMemory)

	cfg1 := db.Config()
	cfg2 := db.Config()

	// Modify the first copy; it must not affect the second.
	cfg1.Name = "modified"

	if cfg2.Name != "copy-db" {
		t.Errorf("Config() did not return independent copy: got %q after modifying first copy", cfg2.Name)
	}
}

func TestDatabase_Backend(t *testing.T) {
	store := memory.NewStorage()
	b := NewStandaloneBackend(store)
	cfg := DatabaseConfig{
		Name:    "backend-db",
		Backend: BackendMemory,
	}
	db := NewDatabase(cfg, b, "/tmp/test/backend-db")

	got := db.Backend()
	if got != b {
		t.Errorf("Backend() returned different instance than was provided")
	}
}

func TestDatabase_Backend_NotNil(t *testing.T) {
	db := newTestDatabase("backend-notnull", BackendMemory)

	if db.Backend() == nil {
		t.Error("Backend() returned nil, want non-nil Backend")
	}
}

func TestDatabase_Status(t *testing.T) {
	db := newTestDatabase("status-db", BackendMemory)

	// Initial state is open.
	if got := db.Status(); got != StatusOpen {
		t.Errorf("initial Status() = %q, want %q", got, StatusOpen)
	}

	// After close, status is closed.
	if err := db.Close(); err != nil {
		t.Fatalf("Close() returned unexpected error: %v", err)
	}
	if got := db.Status(); got != StatusClosed {
		t.Errorf("Status() after Close() = %q, want %q", got, StatusClosed)
	}
}

func TestDatabase_Status_ErrorState(t *testing.T) {
	// Manually store StatusError to verify the atomic value handles it.
	db := newTestDatabase("error-db", BackendMemory)
	db.status.Store(StatusError)

	if got := db.Status(); got != StatusError {
		t.Errorf("Status() = %q, want %q", got, StatusError)
	}
}

// failingBackendError is a sentinel error for the failing backend.
type failingBackendError struct{}

func (e *failingBackendError) Error() string {
	return "backend: simulated close failure"
}

// failingBackend implements Backend but returns an error on Close.
type failingBackend struct{}

func (b *failingBackend) Get(_ context.Context, _ []byte) ([]byte, error) { return nil, nil }
func (b *failingBackend) Put(_ context.Context, _, _ []byte) error        { return nil }
func (b *failingBackend) Delete(_ context.Context, _ []byte) error        { return nil }

func (b *failingBackend) DeleteRange(_ context.Context, _, _ []byte) error   { return nil }
func (b *failingBackend) List(_ context.Context, _ string) ([]string, error) { return nil, nil }
func (b *failingBackend) Range(_ context.Context, _, _ []byte, _ int) ([]KeyValue, error) {
	return nil, nil
}
func (b *failingBackend) Batch(_ context.Context, _ []BatchOp) error { return nil }
func (b *failingBackend) Close() error {
	return &failingBackendError{}
}

func TestDatabase_Close_BackendError(t *testing.T) {
	cfg := DatabaseConfig{
		Name:    "fail-close-db",
		Backend: BackendMemory,
	}
	db := NewDatabase(cfg, &failingBackend{}, "/tmp/test/fail-close-db")

	err := db.Close()
	if err == nil {
		t.Fatal("Close() returned nil, want *CloseError")
	}

	var closeErr *CloseError
	if !errors.As(err, &closeErr) {
		t.Fatalf("Close() error type = %T, want *CloseError", err)
	}
	if closeErr.Name != "fail-close-db" {
		t.Errorf("CloseError.Name = %q, want %q", closeErr.Name, "fail-close-db")
	}

	// Verify status transitions to StatusError.
	if got := db.Status(); got != StatusError {
		t.Errorf("Status() after failed Close() = %q, want %q", got, StatusError)
	}

	// Verify the underlying error is unwrapped correctly.
	var backendErr *failingBackendError
	if !errors.As(err, &backendErr) {
		t.Errorf("Close() error chain does not contain *failingBackendError")
	}
}

func TestDatabase_ProtocolConfig_Default(t *testing.T) {
	db := newTestDatabase("sql-default-db", BackendMemory)

	sql := db.ProtocolConfig()
	if sql.MySQL {
		t.Error("ProtocolConfig().MySQL = true, want false for new database")
	}
	if sql.PostgreSQL {
		t.Error("ProtocolConfig().PostgreSQL = true, want false for new database")
	}
}

func TestDatabase_UpdateProtocol(t *testing.T) {
	db := newTestDatabase("sql-update-db", BackendMemory)

	// Verify initial zero value.
	sql := db.ProtocolConfig()
	if sql.MySQL || sql.PostgreSQL {
		t.Fatal("initial ProtocolConfig() is not zero value")
	}

	// Enable MySQL only.
	db.UpdateProtocol(ProtocolConfig{MySQL: true, PostgreSQL: false})
	sql = db.ProtocolConfig()
	if !sql.MySQL {
		t.Error("ProtocolConfig().MySQL = false after UpdateProtocol(MySQL: true), want true")
	}
	if sql.PostgreSQL {
		t.Error("ProtocolConfig().PostgreSQL = true after UpdateProtocol(PostgreSQL: false), want false")
	}

	// Enable both.
	db.UpdateProtocol(ProtocolConfig{MySQL: true, PostgreSQL: true})
	sql = db.ProtocolConfig()
	if !sql.MySQL {
		t.Error("ProtocolConfig().MySQL = false, want true")
	}
	if !sql.PostgreSQL {
		t.Error("ProtocolConfig().PostgreSQL = false, want true")
	}

	// Disable both.
	db.UpdateProtocol(ProtocolConfig{MySQL: false, PostgreSQL: false})
	sql = db.ProtocolConfig()
	if sql.MySQL {
		t.Error("ProtocolConfig().MySQL = true after disabling, want false")
	}
	if sql.PostgreSQL {
		t.Error("ProtocolConfig().PostgreSQL = true after disabling, want false")
	}
}

func TestDatabase_UpdateProtocol_ConcurrencySafe(t *testing.T) {
	db := newTestDatabase("sql-concurrent-db", BackendMemory)

	const goroutines = 100
	done := make(chan struct{})

	for i := 0; i < goroutines; i++ {
		go func(n int) {
			defer func() { done <- struct{}{} }()
			// Alternate between enabling MySQL and PostgreSQL.
			if n%2 == 0 {
				db.UpdateProtocol(ProtocolConfig{MySQL: true, PostgreSQL: false})
			} else {
				db.UpdateProtocol(ProtocolConfig{MySQL: false, PostgreSQL: true})
			}
		}(i)
	}

	for i := 0; i < goroutines; i++ {
		<-done
	}

	// After all goroutines complete, the Protocol config should be one of
	// the two valid states written by the goroutines.
	sql := db.ProtocolConfig()
	validMySQL := sql.MySQL && !sql.PostgreSQL
	validPostgres := !sql.MySQL && sql.PostgreSQL
	if !validMySQL && !validPostgres {
		t.Errorf("ProtocolConfig() = {MySQL:%v, PostgreSQL:%v}, want one of {true,false} or {false,true}",
			sql.MySQL, sql.PostgreSQL)
	}

	// Also verify that other config fields were not corrupted.
	if db.Name() != "sql-concurrent-db" {
		t.Errorf("Name() = %q after concurrent UpdateProtocol, want %q", db.Name(), "sql-concurrent-db")
	}
	if db.BackendType() != BackendMemory {
		t.Errorf("BackendType() = %q after concurrent UpdateProtocol, want %q", db.BackendType(), BackendMemory)
	}
}

func TestDatabase_Info_WithRaftConfig(t *testing.T) {
	// Create a database with a Raft config to exercise the ShardID branch.
	store := memory.NewStorage()
	t.Cleanup(func() { _ = store.Close() })
	b := NewStandaloneBackend(store)
	cfg := DatabaseConfig{
		Name:    "raft-info-db",
		Backend: BackendMemory,
		Raft: &DatabaseRaftConfig{
			ClusterMode: RaftNone,
			ShardID:     42,
			ReplicaID:   1,
		},
	}
	db := NewDatabase(cfg, b, "/tmp/test/raft-info-db")

	info := db.Info()
	if info.ShardID != 42 {
		t.Errorf("Info().ShardID = %d, want 42", info.ShardID)
	}
	if info.Raft == nil {
		t.Fatal("Info().Raft = nil, want non-nil")
	}
	if info.Raft.ClusterMode != RaftNone {
		t.Errorf("Info().Raft.ClusterMode = %q, want %q", info.Raft.ClusterMode, RaftNone)
	}
}

func TestDatabase_Info_IncludesSQL(t *testing.T) {
	db := newTestDatabase("sql-info-db", BackendMemory)

	// Update Protocol config.
	db.UpdateProtocol(ProtocolConfig{MySQL: true, PostgreSQL: true})

	info := db.Info()
	if !info.Protocol.MySQL {
		t.Error("Info().Protocol.MySQL = false, want true")
	}
	if !info.Protocol.PostgreSQL {
		t.Error("Info().Protocol.PostgreSQL = false, want true")
	}

	// Change and verify again.
	db.UpdateProtocol(ProtocolConfig{MySQL: false, PostgreSQL: true})
	info = db.Info()
	if info.Protocol.MySQL {
		t.Error("Info().Protocol.MySQL = true after update, want false")
	}
	if !info.Protocol.PostgreSQL {
		t.Error("Info().Protocol.PostgreSQL = false after update, want true")
	}
}

func TestDatabase_ShardID_NilRaft(t *testing.T) {
	db := newTestDatabase("no-raft-db", BackendMemory)
	if got := db.ShardID(); got != 0 {
		t.Errorf("ShardID() = %d, want 0 when Raft config is nil", got)
	}
}

func TestDatabase_ShardID_WithRaft(t *testing.T) {
	store := memory.NewStorage()
	b := NewStandaloneBackend(store)
	cfg := DatabaseConfig{
		Name:    "raft-shard-db",
		Backend: BackendMemory,
		Raft: &DatabaseRaftConfig{
			ClusterMode: RaftNone,
			ShardID:     1042,
		},
	}
	db := NewDatabase(cfg, b, "/tmp/test/raft-shard-db")

	if got := db.ShardID(); got != 1042 {
		t.Errorf("ShardID() = %d, want 1042", got)
	}
}

// ---------------------------------------------------------------------------
// go-qrdb#482: Dropping is independent of Status.
// ---------------------------------------------------------------------------

// TestDatabase_Dropping_DefaultsFalse pins the zero value: a freshly created
// database is never dropping.
func TestDatabase_Dropping_DefaultsFalse(t *testing.T) {
	db := newTestDatabase("fresh-db", BackendMemory)
	if db.Dropping() {
		t.Error("Dropping() = true for a freshly created database, want false")
	}
}

// TestDatabase_Close_DoesNotClearTheDropFlag is the direct, non-racy pin for
// the bug a review of go-qrdb#482's first fix caught: Close used to fold the
// fence into Status (StatusDropping), and Close itself unconditionally
// overwrote Status to StatusClosed/StatusError -- so completing Drop's own
// step 2 silently cleared the fence between the reclamation finishing and
// the catalog record actually being removed in step 4, reopening exactly
// the window the fence exists to close. Dropping is now a field of its own
// that Close never touches.
func TestDatabase_Close_DoesNotClearTheDropFlag(t *testing.T) {
	db := newTestDatabase("closing-drop-db", BackendMemory)
	db.SetDropping(true)

	if err := db.Close(); err != nil {
		t.Fatalf("Close() returned unexpected error: %v", err)
	}

	if got := db.Status(); got != StatusClosed {
		t.Errorf("Status() after Close() = %q, want %q", got, StatusClosed)
	}
	if !db.Dropping() {
		t.Error("Dropping() = false after Close(), want true: Close must not clear the drop fence")
	}
}

// TestDatabase_SetDropping_RoundTrips is the direct unit test for the
// setter/getter pair Manager.Drop and Manager.Open depend on.
func TestDatabase_SetDropping_RoundTrips(t *testing.T) {
	db := newTestDatabase("dropping-roundtrip-db", BackendMemory)

	db.SetDropping(true)
	if !db.Dropping() {
		t.Error("Dropping() = false after SetDropping(true)")
	}

	db.SetDropping(false)
	if db.Dropping() {
		t.Error("Dropping() = true after SetDropping(false)")
	}
}

// TestDatabase_Info_ReportsDroppingOverStatus pins that Info() -- and
// therefore Manager.List/GetForTenant -- surfaces the fence honestly even
// though the underlying Status field never itself becomes StatusDropping.
func TestDatabase_Info_ReportsDroppingOverStatus(t *testing.T) {
	db := newTestDatabase("info-dropping-db", BackendMemory)
	db.SetDropping(true)

	info := db.Info()
	if info.Status != StatusDropping {
		t.Errorf("Info().Status = %q, want %q while dropping", info.Status, StatusDropping)
	}
	if got := db.Status(); got != StatusOpen {
		t.Errorf("Status() = %q, want %q -- Dropping must not disturb the underlying status", got, StatusOpen)
	}

	db.SetDropping(false)
	info = db.Info()
	if info.Status != StatusOpen {
		t.Errorf("Info().Status = %q after clearing the fence, want %q", info.Status, StatusOpen)
	}
}
