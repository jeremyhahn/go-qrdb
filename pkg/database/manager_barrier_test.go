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
	"testing"
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

	cfg := validMemoryConfig("barrier-db")
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

	cfg := validMemoryConfig("barrier-open-db")
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
	cfg := validMemoryConfig("barrier-sealed-db")
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
