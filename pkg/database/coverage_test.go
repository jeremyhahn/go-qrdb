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
	"log/slog"
	"os"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// ---------------------------------------------------------------------------
// DatabaseConfig.Validate — remote mode paths
// ---------------------------------------------------------------------------

func TestDatabaseConfig_Validate_RemoteMode_NilConnection(t *testing.T) {
	cfg := DatabaseConfig{
		Name:       "remote-db",
		Engine:     EngineKV,
		Mode:       ModeRemote,
		Connection: nil,
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() returned nil, want ConnectionRequiredError")
	}
	var ce *ConnectionRequiredError
	if !errors.As(err, &ce) {
		t.Errorf("Validate() error type = %T, want *ConnectionRequiredError", err)
	}
}

func TestDatabaseConfig_Validate_RemoteMode_EmptyAddress(t *testing.T) {
	cfg := DatabaseConfig{
		Name:   "remote-db",
		Engine: EngineKV,
		Mode:   ModeRemote,
		Connection: &DatabaseConnectionConfig{
			Transport: "grpc",
			Address:   "", // empty address should fail
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() returned nil, want error for empty address")
	}
}

func TestDatabaseConfig_Validate_RemoteMode_Valid(t *testing.T) {
	cfg := DatabaseConfig{
		Name:   "remote-db",
		Engine: EngineKV,
		Mode:   ModeRemote,
		Connection: &DatabaseConnectionConfig{
			Transport: "grpc",
			Address:   "localhost:9000",
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() returned unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// EtcdConfig.Validate
// ---------------------------------------------------------------------------

func TestEtcdConfig_Validate_EnabledEmptyListenAddr(t *testing.T) {
	cfg := &EtcdConfig{
		Enabled:    true,
		ListenAddr: "",
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() returned nil, want EtcdListenAddrRequiredError")
	}
	var eErr *EtcdListenAddrRequiredError
	if !errors.As(err, &eErr) {
		t.Errorf("Validate() error type = %T, want *EtcdListenAddrRequiredError", err)
	}
}

func TestEtcdConfig_Validate_EnabledWithListenAddr(t *testing.T) {
	cfg := &EtcdConfig{
		Enabled:    true,
		ListenAddr: "0.0.0.0:2379",
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() returned unexpected error: %v", err)
	}
}

func TestEtcdConfig_Validate_Disabled(t *testing.T) {
	cfg := &EtcdConfig{
		Enabled:    false,
		ListenAddr: "",
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() returned unexpected error for disabled config: %v", err)
	}
}

// ---------------------------------------------------------------------------
// DatabaseRaftConfig.Validate — discovery edge cases
// ---------------------------------------------------------------------------

func TestDatabaseRaftConfig_Validate_DistributedMulticastNilConfig(t *testing.T) {
	cfg := &DatabaseRaftConfig{
		ClusterMode: RaftDistributed,
		Discovery:   DiscoveryMulticast,
		Multicast:   nil,
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() returned nil, want DiscoveryConfigRequiredError")
	}
	var dErr *DiscoveryConfigRequiredError
	if !errors.As(err, &dErr) {
		t.Errorf("Validate() error type = %T, want *DiscoveryConfigRequiredError", err)
	}
}

func TestDatabaseRaftConfig_Validate_DistributedDNSNilConfig(t *testing.T) {
	cfg := &DatabaseRaftConfig{
		ClusterMode: RaftDistributed,
		Discovery:   DiscoveryDNS,
		DNS:         nil,
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() returned nil, want DiscoveryConfigRequiredError")
	}
}

func TestDatabaseRaftConfig_Validate_DistributedTokenNilConfig(t *testing.T) {
	cfg := &DatabaseRaftConfig{
		ClusterMode: RaftDistributed,
		Discovery:   DiscoveryToken,
		Token:       nil,
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() returned nil, want DiscoveryConfigRequiredError")
	}
}

func TestDatabaseRaftConfig_Validate_DistributedStaticNoConfig(t *testing.T) {
	// Static discovery with no config is valid (uses top-level Peers field).
	cfg := &DatabaseRaftConfig{
		ClusterMode: RaftDistributed,
		Discovery:   DiscoveryStatic,
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() returned unexpected error for static discovery: %v", err)
	}
}

func TestDatabaseRaftConfig_Validate_DefaultsToStaticWhenEmpty(t *testing.T) {
	// Empty discovery should default to static and pass validation.
	cfg := &DatabaseRaftConfig{
		ClusterMode: RaftDistributed,
		Discovery:   "",
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() returned unexpected error with empty discovery: %v", err)
	}
	if cfg.Discovery != DiscoveryStatic {
		t.Errorf("Discovery was not defaulted to static, got %q", cfg.Discovery)
	}
}

// ---------------------------------------------------------------------------
// BackendStorageAdapter.Scan — callback error path
// ---------------------------------------------------------------------------

func TestBackendStorageAdapter_Scan_CallbackError(t *testing.T) {
	a, _ := newTestAdapter(t)
	ctx := context.Background()

	if err := a.Put(ctx, "cb/key1", []byte("v1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := a.Put(ctx, "cb/key2", []byte("v2")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	cbErr := errors.New("callback error")
	err := a.Scan(ctx, "cb/", func(_ string, _ []byte) error {
		return cbErr
	})
	if !errors.Is(err, cbErr) {
		t.Errorf("Scan: expected callback error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Manager.CreateWithCredentials — barrier manager paths
// ---------------------------------------------------------------------------

func TestManager_CreateWithCredentials_BarrierConfigNoManager(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	cfg := DatabaseConfig{
		Name:    "barrier-db",
		Engine:  EngineKV,
		Backend: BackendMemory,
		DataDir: t.TempDir(),
		Barrier: &DatabaseBarrierConfig{
			Enabled: true,
		},
	}

	// Without a barrier manager configured on the Manager, the barrier block
	// is skipped and the database should be created normally.
	db, err := mgr.CreateWithCredentials(context.Background(), cfg, seal.Credentials{})
	if err != nil {
		t.Fatalf("CreateWithCredentials() returned unexpected error: %v", err)
	}
	if db.Name() != "barrier-db" {
		t.Errorf("Name() = %q, want %q", db.Name(), "barrier-db")
	}
}

// ---------------------------------------------------------------------------
// DatabaseBarrierManager.GetBarrier — returns non-nil barrier after EnsureBarrier
// ---------------------------------------------------------------------------

func TestDatabaseBarrierManager_GetBarrier_ReturnsEncryptor(t *testing.T) {
	mgr := newTestBarrierManager(t)
	ctx := context.Background()
	storage := seal.NewMemoryBackend()

	cfg := &DatabaseBarrierConfig{Enabled: true, Mode: "passphrase"}
	creds := seal.MustCredentials([]byte("test-password"))

	if err := mgr.EnsureBarrier(ctx, "enc-getbarrier", "", cfg, storage, creds); err != nil {
		t.Fatalf("EnsureBarrier: %v", err)
	}

	barrier := mgr.GetBarrier("enc-getbarrier")
	if barrier == nil {
		t.Error("GetBarrier() returned nil, want non-nil BarrierEncryptor")
	}
}

func TestDatabaseBarrierManager_GetBarrier_NotRegistered(t *testing.T) {
	mgr := newTestBarrierManager(t)

	barrier := mgr.GetBarrier("nonexistent")
	if barrier != nil {
		t.Error("GetBarrier() returned non-nil for unregistered database, want nil")
	}
}

// ---------------------------------------------------------------------------
// DatabaseBarrierManager.RegisterBarrier — register for re-open scenario
// ---------------------------------------------------------------------------

func TestDatabaseBarrierManager_RegisterBarrier_Success(t *testing.T) {
	mgr := newTestBarrierManager(t)
	storage := seal.NewMemoryBackend()

	cfg := &DatabaseBarrierConfig{Enabled: true, Mode: "passphrase"}

	err := mgr.RegisterBarrier("reg-barrier-db", "", cfg, storage)
	if err != nil {
		t.Fatalf("RegisterBarrier: %v", err)
	}

	// Barrier should be registered but sealed (no init was done).
	status, err := mgr.BarrierStatus("reg-barrier-db")
	if err != nil {
		t.Fatalf("BarrierStatus: %v", err)
	}
	if !status.Sealed {
		t.Error("expected barrier to be sealed after RegisterBarrier (not initialized)")
	}
}

func TestDatabaseBarrierManager_RegisterBarrier_DisabledNoop(t *testing.T) {
	mgr := newTestBarrierManager(t)
	storage := seal.NewMemoryBackend()

	cfg := &DatabaseBarrierConfig{Enabled: false}

	err := mgr.RegisterBarrier("disabled-db", "", cfg, storage)
	if err != nil {
		t.Fatalf("RegisterBarrier with disabled config returned unexpected error: %v", err)
	}

	_, err = mgr.BarrierStatus("disabled-db")
	if err == nil {
		t.Error("expected error for unregistered (disabled) database barrier")
	}
}

func TestDatabaseBarrierManager_RegisterBarrier_NilConfig(t *testing.T) {
	mgr := newTestBarrierManager(t)
	storage := seal.NewMemoryBackend()

	err := mgr.RegisterBarrier("nil-cfg-db", "", nil, storage)
	if err != nil {
		t.Fatalf("RegisterBarrier with nil config returned unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------

func TestManager_CreateWithCredentials_WithBarrierManager(t *testing.T) {
	bm := newTestBarrierManager(t)
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := NewManager(logger, dataDir, WithBarrierManager(bm))
	defer func() { _ = mgr.Close() }()

	cfg := DatabaseConfig{
		Name:    "enc-db",
		Engine:  EngineKV,
		Backend: BackendMemory,
		DataDir: t.TempDir(),
		Barrier: &DatabaseBarrierConfig{
			Enabled: true,
		},
	}
	creds := seal.MustCredentials([]byte("test-password"))

	// With barrier manager configured, the barrier should be created and
	// the database should be barrier-enabled.
	db, err := mgr.CreateWithCredentials(context.Background(), cfg, creds)
	if err != nil {
		t.Fatalf("CreateWithCredentials() returned unexpected error: %v", err)
	}
	if db.Name() != "enc-db" {
		t.Errorf("Name() = %q, want %q", db.Name(), "enc-db")
	}
	if !db.BarrierEnabled() {
		t.Error("BarrierEnabled() = false, want true after CreateWithCredentials with barrier manager")
	}
}

// ---------------------------------------------------------------------------
// Manager.Open — mode default path
// ---------------------------------------------------------------------------

func TestManager_Open_ModeDefault(t *testing.T) {
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mgr := NewManager(logger, dataDir)

	// Create a database with memory backend.
	cfg := validMemoryConfig("mode-default-db")
	if _, err := mgr.Create(cfg); err != nil {
		t.Fatalf("Create(): %v", err)
	}
	if err := mgr.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	// Re-open with a fresh manager to exercise meta.json reconstruction.
	mgr2 := NewManager(logger, dataDir)
	defer func() { _ = mgr2.Close() }()

	db, err := mgr2.Open("mode-default-db")
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	if db.Name() != "mode-default-db" {
		t.Errorf("Name() = %q, want %q", db.Name(), "mode-default-db")
	}
}

// ---------------------------------------------------------------------------
// Manager.Close — sealed database path
// ---------------------------------------------------------------------------

func TestManager_Close_SealedDatabase(t *testing.T) {
	mgr := newTestManager(t)

	// Create a database and manually set it to sealed status.
	cfg := validMemoryConfig("sealed-close-db")
	db, err := mgr.Create(cfg)
	if err != nil {
		t.Fatalf("Create(): %v", err)
	}
	db.SetStatus(StatusSealed)

	// Close should handle sealed databases without error.
	if err := mgr.Close(); err != nil {
		t.Errorf("Close() returned unexpected error for sealed database: %v", err)
	}
}
