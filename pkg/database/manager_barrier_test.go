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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// noopBackend implements Backend with no-op operations for testing.
type noopBackend struct{}

func newNoopBackend() *noopBackend { return &noopBackend{} }

func (b *noopBackend) Get(_ context.Context, _ []byte) ([]byte, error) { return nil, nil }
func (b *noopBackend) Put(_ context.Context, _, _ []byte) error        { return nil }
func (b *noopBackend) Delete(_ context.Context, _ []byte) error        { return nil }
func (b *noopBackend) List(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}
func (b *noopBackend) Range(_ context.Context, _, _ []byte, _ int) ([]KeyValue, error) {
	return nil, nil
}
func (b *noopBackend) Batch(_ context.Context, _ []BatchOp) error { return nil }
func (b *noopBackend) Close() error                               { return nil }

func TestManager_Create_BarrierEnabled_WithoutBarrierManager(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	cfg := validDedicatedMemoryConfig(t, "barrier-db")
	cfg.Barrier = &DatabaseBarrierConfig{Enabled: true, Mode: "passphrase"}

	db, err := mgr.Create(cfg)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if db.BarrierEnabled() {
		t.Error("BarrierEnabled() = true without barrier manager, want false")
	}
}

func TestManager_WithBarrierManager_NilOption(t *testing.T) {
	dataDir := t.TempDir()
	mgr := NewManager(nil, dataDir, WithBarrierManager(nil))
	defer func() { _ = mgr.Close() }()

	if mgr.BarrierManager() != nil {
		t.Error("BarrierManager() = non-nil, want nil")
	}
}

func TestManager_Open_BarrierEnabled_NoBarrierManager(t *testing.T) {
	dataDir := t.TempDir()
	mgr1 := NewManager(nil, dataDir)

	cfg := validDedicatedMemoryConfig(t, "barrier-open-db")
	cfg.Barrier = &DatabaseBarrierConfig{Enabled: true, Mode: "passphrase"}
	if _, err := mgr1.Create(cfg); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := mgr1.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	mgr2 := NewManager(nil, dataDir)
	defer func() { _ = mgr2.Close() }()

	db, err := mgr2.Open("barrier-open-db")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	// Without barrier manager, should remain open (not sealed).
	if db.Status() != StatusOpen {
		t.Errorf("Status() = %q, want %q", db.Status(), StatusOpen)
	}
}

func TestManager_Open_BarrierEnabled_WithBarrierManager(t *testing.T) {
	dataDir := t.TempDir()

	// Create with barrier config but no barrier manager.
	mgr1 := NewManager(nil, dataDir)
	cfg := validDedicatedMemoryConfig(t, "barrier-sealed-db")
	cfg.Barrier = &DatabaseBarrierConfig{Enabled: true, Mode: "passphrase"}
	if _, err := mgr1.Create(cfg); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := mgr1.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	// Reopen with a barrier manager set (using a real test barrier manager).
	bm := newTestBarrierManager(t)
	mgr2 := NewManager(nil, dataDir, WithBarrierManager(bm))
	defer func() { _ = mgr2.Close() }()

	db, err := mgr2.Open("barrier-sealed-db")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	// With barrier manager present, the database should be sealed.
	if db.Status() != StatusSealed {
		t.Errorf("Status() = %q, want %q", db.Status(), StatusSealed)
	}
	if !db.BarrierEnabled() {
		t.Error("BarrierEnabled() = false, want true")
	}
}

func TestDatabase_BarrierEnabled_Default(t *testing.T) {
	cfg := validMemoryConfig("test-db")
	db := NewDatabase(cfg, &failingCloseBackend{}, "/tmp/test")
	if db.BarrierEnabled() {
		t.Error("BarrierEnabled() default = true, want false")
	}
}

func TestDatabase_SetBarrierEnabled(t *testing.T) {
	cfg := validMemoryConfig("test-db")
	db := NewDatabase(cfg, &failingCloseBackend{}, "/tmp/test")
	db.SetBarrierEnabled(true)
	if !db.BarrierEnabled() {
		t.Error("BarrierEnabled() after SetBarrierEnabled(true) = false")
	}
	db.SetBarrierEnabled(false)
	if db.BarrierEnabled() {
		t.Error("BarrierEnabled() after SetBarrierEnabled(false) = true")
	}
}

func TestDatabase_SetStatus(t *testing.T) {
	cfg := validMemoryConfig("test-db")
	db := NewDatabase(cfg, &failingCloseBackend{}, "/tmp/test")
	if db.Status() != StatusOpen {
		t.Fatalf("initial Status() = %q, want %q", db.Status(), StatusOpen)
	}

	db.SetStatus(StatusSealed)
	if db.Status() != StatusSealed {
		t.Errorf("Status() after SetStatus(sealed) = %q, want %q", db.Status(), StatusSealed)
	}
}

func TestDatabase_SetStatus_AllStates(t *testing.T) {
	cfg := validMemoryConfig("test-db")
	db := NewDatabase(cfg, newNoopBackend(), "/tmp/test")

	states := []StatusType{StatusOpen, StatusSealed, StatusError, StatusClosed}
	for _, s := range states {
		db.SetStatus(s)
		if db.Status() != s {
			t.Errorf("Status() after SetStatus(%q) = %q", s, db.Status())
		}
	}
}

func TestDatabase_Close_WhenSealed(t *testing.T) {
	cfg := validMemoryConfig("sealed-close-db")
	db := NewDatabase(cfg, newNoopBackend(), "/tmp/test")
	db.SetStatus(StatusSealed)

	if err := db.Close(); err != nil {
		t.Fatalf("Close() from sealed state error = %v", err)
	}
	if db.Status() != StatusClosed {
		t.Errorf("Status() after Close() = %q, want %q", db.Status(), StatusClosed)
	}
}

func TestDatabase_Close_WhenSealed_BackendError(t *testing.T) {
	cfg := validMemoryConfig("sealed-err-db")
	db := NewDatabase(cfg, &failingCloseBackend{}, "/tmp/test")
	db.SetStatus(StatusSealed)

	err := db.Close()
	if err == nil {
		t.Fatal("Close() from sealed state with failing backend = nil, want error")
	}
	if db.Status() != StatusError {
		t.Errorf("Status() after failed Close() = %q, want %q", db.Status(), StatusError)
	}
}

func TestDatabase_TenantID(t *testing.T) {
	cfg := DatabaseConfig{
		TenantID: "tenant-xyz",
		Name:     "test-db",
		Engine:   EngineKV,
		Backend:  BackendMemory,
	}
	db := NewDatabase(cfg, newNoopBackend(), "/tmp/test")
	if db.TenantID() != "tenant-xyz" {
		t.Errorf("TenantID() = %q, want %q", db.TenantID(), "tenant-xyz")
	}
}

func TestDatabase_TenantID_Empty(t *testing.T) {
	cfg := validMemoryConfig("no-tenant-db")
	db := NewDatabase(cfg, newNoopBackend(), "/tmp/test")
	if db.TenantID() != "" {
		t.Errorf("TenantID() = %q, want empty string", db.TenantID())
	}
}

func TestManager_BarrierManager_Default(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	if mgr.BarrierManager() != nil {
		t.Error("BarrierManager() on default manager = non-nil, want nil")
	}
}

func TestManager_BarrierManager_Set(t *testing.T) {
	bm := newTestBarrierManager(t)
	dataDir := t.TempDir()
	mgr := NewManager(nil, dataDir, WithBarrierManager(bm))
	defer func() { _ = mgr.Close() }()

	if mgr.BarrierManager() != bm {
		t.Error("BarrierManager() does not match the barrier manager passed via option")
	}
}

// ---------------------------------------------------------------------------
// Shared-store barrier rejection (SharedStoreBarrierUnsupportedError)
// ---------------------------------------------------------------------------

func TestManager_Create_BarrierEnabled_SharedStoreRejected(t *testing.T) {
	bm := newTestBarrierManager(t)
	mgr := NewManager(nil, t.TempDir(), WithBarrierManager(bm))
	defer func() { _ = mgr.Close() }()

	cfg := validMemoryConfig("shared-barrier-db") // no DataDir, non-raft backend
	cfg.Barrier = &DatabaseBarrierConfig{Enabled: true, Mode: "passphrase"}

	_, err := mgr.CreateWithCredentials(context.Background(), cfg, testCreds())
	if err == nil {
		t.Fatal("CreateWithCredentials() on a shared-store database = nil, want *SharedStoreBarrierUnsupportedError")
	}
	var sharedErr *SharedStoreBarrierUnsupportedError
	if !errors.As(err, &sharedErr) {
		t.Fatalf("error type = %T, want *SharedStoreBarrierUnsupportedError", err)
	}

	// The database must not have been left half-created.
	if _, getErr := mgr.Get("shared-barrier-db"); getErr == nil {
		t.Error("Get() succeeded after a rejected barrier-enabled create, want NotFoundError")
	}
}

func TestManager_Create_BarrierDisabled_SharedStoreAllowed(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	// No barrier requested: the shared-store restriction does not apply.
	cfg := validMemoryConfig("shared-nobarrier-db")
	if _, err := mgr.Create(cfg); err != nil {
		t.Fatalf("Create() without a barrier on a shared-store database returned unexpected error: %v", err)
	}
}

func TestManager_Open_BarrierEnabled_SharedStoreRejected(t *testing.T) {
	dataDir := t.TempDir()

	// Create WITHOUT a barrier manager present so the shared-store guard at
	// create time never fires, and hand-write meta.json to simulate
	// inconsistent on-disk state predating the guard.
	mgr1 := NewManager(nil, dataDir)
	cfg := validMemoryConfig("shared-open-db")
	if _, err := mgr1.Create(cfg); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := mgr1.UpdateBarrier("shared-open-db", &DatabaseBarrierConfig{Enabled: true, Mode: "passphrase"}); err == nil {
		t.Fatal("UpdateBarrier() on shared-store database = nil, want *SharedStoreBarrierUnsupportedError")
	}
	if err := mgr1.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	// Directly rewrite meta.json with Barrier.Enabled=true to simulate a
	// pre-guard on-disk record, then confirm Open fails closed rather than
	// silently registering an inert barrier.
	meta := DatabaseMeta{
		Name:    "shared-open-db",
		Engine:  EngineKV,
		Backend: BackendMemory,
		Barrier: &DatabaseBarrierConfig{Enabled: true, Mode: "passphrase"},
	}
	mgr2 := NewManager(nil, dataDir, WithBarrierManager(newTestBarrierManager(t)))
	defer func() { _ = mgr2.Close() }()
	if err := mgr2.writeMeta(mgr2.dbDir("shared-open-db"), meta); err != nil {
		t.Fatalf("writeMeta: %v", err)
	}

	_, err := mgr2.Open("shared-open-db")
	if err == nil {
		t.Fatal("Open() on shared-store barrier-enabled meta = nil, want *SharedStoreBarrierUnsupportedError")
	}
	var sharedErr *SharedStoreBarrierUnsupportedError
	if !errors.As(err, &sharedErr) {
		t.Fatalf("error type = %T, want *SharedStoreBarrierUnsupportedError", err)
	}
}

func TestManager_UpdateBarrier_SharedStoreRejected(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	cfg := validMemoryConfig("shared-update-db")
	if _, err := mgr.Create(cfg); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	err := mgr.UpdateBarrier("shared-update-db", &DatabaseBarrierConfig{Enabled: true, Mode: "passphrase"})
	if err == nil {
		t.Fatal("UpdateBarrier() on shared-store database = nil, want *SharedStoreBarrierUnsupportedError")
	}
	var sharedErr *SharedStoreBarrierUnsupportedError
	if !errors.As(err, &sharedErr) {
		t.Fatalf("error type = %T, want *SharedStoreBarrierUnsupportedError", err)
	}

	// The rejected update must not have been persisted.
	db, getErr := mgr.Get("shared-update-db")
	if getErr != nil {
		t.Fatalf("Get() error = %v", getErr)
	}
	if db.Config().Barrier != nil && db.Config().Barrier.Enabled {
		t.Error("Barrier.Enabled = true after a rejected UpdateBarrier, want unchanged (nil/false)")
	}
}

func TestManager_UpdateBarrier_Disable_SharedStoreAllowed(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	cfg := validMemoryConfig("shared-disable-db")
	if _, err := mgr.Create(cfg); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// Disabling (Enabled: false) is always allowed regardless of store class.
	if err := mgr.UpdateBarrier("shared-disable-db", &DatabaseBarrierConfig{Enabled: false}); err != nil {
		t.Fatalf("UpdateBarrier(disable) returned unexpected error: %v", err)
	}
}

func TestSharedStoreBarrierUnsupportedError_Error(t *testing.T) {
	err := &SharedStoreBarrierUnsupportedError{Name: "mydb", Backend: BackendPebble}
	msg := err.Error()
	for _, want := range []string{"mydb", "pebble", "dedicated store"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error() = %q, want it to contain %q", msg, want)
		}
	}
}

// ---------------------------------------------------------------------------
// UnsupportedBarrierStrategyError (Mode validation in EnsureBarrier)
// ---------------------------------------------------------------------------

func TestDatabaseBarrierManager_EnsureBarrier_UnsupportedMode(t *testing.T) {
	mgr := newTestBarrierManager(t)
	storage := seal.NewMemoryBackend()

	cfg := &DatabaseBarrierConfig{Enabled: true, Mode: "shamir"}
	err := mgr.EnsureBarrier(context.Background(), "shamir-mode-db", cfg, storage, testCreds())
	if err == nil {
		t.Fatal("EnsureBarrier() with Mode=shamir = nil, want *UnsupportedBarrierStrategyError")
	}
	var modeErr *UnsupportedBarrierStrategyError
	if !errors.As(err, &modeErr) {
		t.Fatalf("error type = %T, want *UnsupportedBarrierStrategyError", err)
	}
}

func TestDatabaseBarrierManager_EnsureBarrier_EmptyModeDefaultsToPassphrase(t *testing.T) {
	mgr := newTestBarrierManager(t)
	storage := seal.NewMemoryBackend()

	cfg := &DatabaseBarrierConfig{Enabled: true} // Mode left empty
	if err := mgr.EnsureBarrier(context.Background(), "empty-mode-db", cfg, storage, testCreds()); err != nil {
		t.Fatalf("EnsureBarrier() with empty Mode returned unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Manager.SetBarrierManager
// ---------------------------------------------------------------------------

func TestManager_SetBarrierManager_Nil(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	mgr.SetBarrierManager(nil)
	if mgr.BarrierManager() != nil {
		t.Error("BarrierManager() after SetBarrierManager(nil) = non-nil, want nil")
	}
}

// TestManager_SetBarrierManager_ReregistersAlreadyOpenDatabases covers the
// daemon boot ordering problem: OpenAll rehydrates database metadata before
// the seal subsystem's BarrierRegistry exists, so RegisterBarrier could not
// run at Open time. SetBarrierManager, called once the registry becomes
// available, must retroactively register the barrier for every already-open
// database that has one enabled on a dedicated store.
func TestManager_SetBarrierManager_ReregistersAlreadyOpenDatabases(t *testing.T) {
	dataDir := t.TempDir()

	// First run: create a dedicated-store, barrier-enabled database with a
	// barrier manager present (as Create requires).
	bm1 := newTestBarrierManager(t)
	mgr1 := NewManager(nil, dataDir, WithBarrierManager(bm1))
	cfg := validDedicatedMemoryConfig(t, "reopen-barrier-db")
	cfg.Barrier = &DatabaseBarrierConfig{Enabled: true, Mode: "passphrase"}
	if _, err := mgr1.CreateWithCredentials(context.Background(), cfg, testCreds()); err != nil {
		t.Fatalf("CreateWithCredentials() error = %v", err)
	}
	if err := mgr1.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	// Second run: simulate the daemon boot ordering -- construct the Manager
	// and OpenAll BEFORE any barrier manager is available.
	mgr2 := NewManager(nil, dataDir)
	defer func() { _ = mgr2.Close() }()
	if err := mgr2.OpenAll(); err != nil {
		t.Fatalf("OpenAll() error = %v", err)
	}
	db, err := mgr2.Get("reopen-barrier-db")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if mgr2.BarrierManager() != nil {
		t.Fatal("BarrierManager() before SetBarrierManager = non-nil, want nil")
	}

	// Now wire the barrier manager, as lifecycle.BuildServices does once the
	// seal subsystem's registry becomes available.
	bm2 := newTestBarrierManager(t)
	mgr2.SetBarrierManager(bm2)

	if mgr2.BarrierManager() != bm2 {
		t.Error("BarrierManager() after SetBarrierManager does not match")
	}
	if !db.BarrierEnabled() {
		t.Error("BarrierEnabled() after SetBarrierManager = false, want true")
	}
	if db.Status() != StatusSealed {
		t.Errorf("Status() after SetBarrierManager = %q, want %q", db.Status(), StatusSealed)
	}
	status, statusErr := bm2.BarrierStatus("reopen-barrier-db")
	if statusErr != nil {
		t.Fatalf("BarrierStatus() after SetBarrierManager error = %v", statusErr)
	}
	if !status.Sealed {
		t.Error("registry BarrierStatus().Sealed = false after re-registration, want true (sealed until explicitly unsealed)")
	}
}

// TestManager_SetBarrierManager_DoesNotClearTheDropFlag pins go-qrdb#482
// finding (h): SetBarrierManager unconditionally sets Status to
// StatusSealed for every barrier-enabled database it re-registers (see the
// assertion above). Status used to be where the drop fence lived too, so
// replaying this boot-time barrier wiring over a database a crash left
// mid-drop would have silently resolved it again. Dropping is a field of
// its own, independent of Status, so SetBarrierManager cannot touch it.
func TestManager_SetBarrierManager_DoesNotClearTheDropFlag(t *testing.T) {
	dataDir := t.TempDir()

	bm1 := newTestBarrierManager(t)
	mgr1 := NewManager(nil, dataDir, WithBarrierManager(bm1))
	cfg := validDedicatedMemoryConfig(t, "sealed-dropping-db")
	cfg.Barrier = &DatabaseBarrierConfig{Enabled: true, Mode: "passphrase"}
	if _, err := mgr1.CreateWithCredentials(context.Background(), cfg, testCreds()); err != nil {
		t.Fatalf("CreateWithCredentials() error = %v", err)
	}
	if err := mgr1.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	mgr2 := NewManager(nil, dataDir)
	defer func() { _ = mgr2.Close() }()
	if err := mgr2.OpenAll(); err != nil {
		t.Fatalf("OpenAll() error = %v", err)
	}
	db, err := mgr2.Get("sealed-dropping-db")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	db.SetDropping(true)

	bm2 := newTestBarrierManager(t)
	mgr2.SetBarrierManager(bm2)

	if db.Status() != StatusSealed {
		t.Errorf("Status() after SetBarrierManager = %q, want %q", db.Status(), StatusSealed)
	}
	if !db.Dropping() {
		t.Error("Dropping() = false after SetBarrierManager, want true: re-registering the barrier must not clear the drop fence")
	}
}

// TestManager_SetBarrierManager_CalledTwice_RewrapsWithoutNesting covers
// case (2) from SetBarrierManager's doc comment: an in-process cluster
// migration rebuilds the registry against a new root key and calls
// SetBarrierManager again. The second call must re-wrap the immutable raw
// backend (not the already-wrapped one), and must close the first barrier
// metadata backend it opened rather than leaking its file handle.
func TestManager_SetBarrierManager_CalledTwice_RewrapsWithoutNesting(t *testing.T) {
	dataDir := t.TempDir()

	mgr1 := NewManager(nil, dataDir)
	cfg := validDedicatedMemoryConfig(t, "rewrap-db")
	cfg.Barrier = &DatabaseBarrierConfig{Enabled: true, Mode: "passphrase"}
	if _, err := mgr1.Create(cfg); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := mgr1.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	mgr2 := NewManager(nil, dataDir)
	defer func() { _ = mgr2.Close() }()
	if err := mgr2.OpenAll(); err != nil {
		t.Fatalf("OpenAll() error = %v", err)
	}
	db, err := mgr2.Get("rewrap-db")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	rawBefore := db.rawDataBackend()

	bmA := newTestBarrierManager(t)
	mgr2.SetBarrierManager(bmA)
	if !db.BarrierEnabled() {
		t.Fatal("BarrierEnabled() after first SetBarrierManager = false, want true")
	}
	wrappedAfterFirst := db.Backend()

	// Second call (simulating a migration rebuild against a new registry).
	bmB := newTestBarrierManager(t)
	mgr2.SetBarrierManager(bmB)
	if !db.BarrierEnabled() {
		t.Fatal("BarrierEnabled() after second SetBarrierManager = false, want true")
	}
	wrappedAfterSecond := db.Backend()

	if wrappedAfterFirst == wrappedAfterSecond {
		t.Error("Backend() did not change across the second SetBarrierManager call")
	}
	// The raw backend underneath must never change -- proving the second
	// wrap was built on the pristine raw backend, not the already-wrapped
	// one (which would nest BarrierBackend instances).
	if db.rawDataBackend() != rawBefore {
		t.Error("rawDataBackend() changed across SetBarrierManager calls, want stable")
	}

	status, statusErr := bmB.BarrierStatus("rewrap-db")
	if statusErr != nil {
		t.Fatalf("BarrierStatus() on the second registry error = %v", statusErr)
	}
	if !status.Sealed {
		t.Error("registry BarrierStatus().Sealed = false after re-registration, want true")
	}
}

// TestManager_SetBarrierManager_SkipsNonBarrierDatabases verifies that
// SetBarrierManager does not attempt to register a barrier for databases
// that never had one enabled.
// TestManager_openBarrierMetaBackend_MkdirFails covers the error path when
// the barrier metadata directory cannot be created (e.g. an ancestor path
// component is a regular file, not a directory).
func TestManager_openBarrierMetaBackend_MkdirFails(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := mgr.openBarrierMetaBackend(blocked, "somedb")
	if err == nil {
		t.Fatal("openBarrierMetaBackend() over a file path = nil, want error")
	}
	var openErr *OpenError
	if !errors.As(err, &openErr) {
		t.Fatalf("error type = %T, want *OpenError", err)
	}
}

// TestManager_CreateWithCredentials_EnsureBarrierFails_CleansUp verifies
// that when EnsureBarrier fails after the barrier metadata backend has
// already been opened (an unsupported strategy mode), CreateWithCredentials
// closes both the data-plane backend and the barrier metadata backend and
// leaves no half-created database registered.
func TestManager_CreateWithCredentials_EnsureBarrierFails_CleansUp(t *testing.T) {
	bm := newTestBarrierManager(t)
	mgr := NewManager(nil, t.TempDir(), WithBarrierManager(bm))
	defer func() { _ = mgr.Close() }()

	cfg := validDedicatedMemoryConfig(t, "shamir-fail-db")
	cfg.Barrier = &DatabaseBarrierConfig{Enabled: true, Mode: "shamir"}

	_, err := mgr.CreateWithCredentials(context.Background(), cfg, testCreds())
	if err == nil {
		t.Fatal("CreateWithCredentials() with Mode=shamir = nil, want *UnsupportedBarrierStrategyError")
	}
	var modeErr *UnsupportedBarrierStrategyError
	if !errors.As(err, &modeErr) {
		t.Fatalf("error type = %T, want *UnsupportedBarrierStrategyError", err)
	}
	if _, getErr := mgr.Get("shamir-fail-db"); getErr == nil {
		t.Error("Get() succeeded after a failed EnsureBarrier, want NotFoundError")
	}
}

// TestManager_Open_RegisterBarrierFails_CleansUp verifies that when
// RegisterBarrier fails during Open (here: the database name is already
// registered in the shared barrier registry), Open closes both the
// data-plane backend and the barrier metadata backend it had opened and
// returns the underlying error rather than leaving a half-open database.
func TestManager_Open_RegisterBarrierFails_CleansUp(t *testing.T) {
	bm := newTestBarrierManager(t)

	// Register "collide-db" in the shared registry via a normal create on a
	// throwaway manager instance.
	mgr1 := NewManager(nil, t.TempDir(), WithBarrierManager(bm))
	cfg := validDedicatedMemoryConfig(t, "collide-db")
	cfg.Barrier = &DatabaseBarrierConfig{Enabled: true, Mode: "passphrase"}
	if _, err := mgr1.CreateWithCredentials(context.Background(), cfg, testCreds()); err != nil {
		t.Fatalf("CreateWithCredentials() error = %v", err)
	}
	// Deliberately do not close mgr1 -- closing would remove the barrier
	// from the shared registry via db.Close() semantics only if Drop is
	// called, which it is not here; the registration must persist so the
	// second manager's Open collides with it.

	// A second manager, at a different data root, whose on-disk meta.json
	// happens to name the same database. Sharing the barrier manager's
	// registry (as SetBarrierManager wiring does across an in-process
	// migration) means RegisterBarrier for "collide-db" fails with
	// ErrTenantAlreadyExists.
	mgr2 := NewManager(nil, t.TempDir(), WithBarrierManager(bm))
	defer func() { _ = mgr2.Close() }()
	meta := DatabaseMeta{
		Name:    "collide-db",
		Engine:  EngineKV,
		Backend: BackendMemory,
		DataDir: t.TempDir(),
		Barrier: &DatabaseBarrierConfig{Enabled: true, Mode: "passphrase"},
	}
	if err := os.MkdirAll(mgr2.dbDir("collide-db"), 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := mgr2.writeMeta(mgr2.dbDir("collide-db"), meta); err != nil {
		t.Fatalf("writeMeta: %v", err)
	}

	_, err := mgr2.Open("collide-db")
	if err == nil {
		t.Fatal("Open() with a barrier name collision = nil, want error")
	}
	var createErr *BarrierCreateError
	if !errors.As(err, &createErr) {
		t.Fatalf("error type = %T, want *BarrierCreateError", err)
	}
	if _, getErr := mgr2.Get("collide-db"); getErr == nil {
		t.Error("Get() succeeded after a failed Open/RegisterBarrier, want NotFoundError")
	}

	_ = mgr1.Close()
}

func TestManager_SetBarrierManager_SkipsNonBarrierDatabases(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	cfg := validMemoryConfig("no-barrier-db")
	if _, err := mgr.Create(cfg); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	bm := newTestBarrierManager(t)
	mgr.SetBarrierManager(bm)

	if _, statusErr := bm.BarrierStatus("no-barrier-db"); statusErr == nil {
		t.Error("BarrierStatus() succeeded for a database that never had a barrier enabled, want error")
	}
}
