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
	"io"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// testBarrierLogger returns a discard logger for tests.
func testBarrierLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// testBarrierSoftwareStrategy creates a SoftwareStrategy with a test passphrase.
func testBarrierSoftwareStrategy(t *testing.T) *seal.SoftwareStrategy {
	t.Helper()
	s, err := seal.NewSoftwareStrategy([]byte("test-master-password"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	return s
}

// newTestSystemBarrier creates a system-level barrier for test registries.
func newTestSystemBarrier(t *testing.T) *seal.StorageBarrier {
	t.Helper()
	store := seal.NewMemoryBackend()
	b, err := seal.NewBarrier(testBarrierLogger(), store, seal.BarrierConfig{
		RootKeyPath: "system/root-key",
	}, testBarrierSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewBarrier (system): %v", err)
	}
	return b
}

// newTestBarrierManager creates a DatabaseBarrierManager backed by a fresh
// registry for testing.
func newTestBarrierManager(t *testing.T) *DatabaseBarrierManager {
	t.Helper()
	system := newTestSystemBarrier(t)
	registry, err := seal.NewBarrierRegistry(testBarrierLogger(), system)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	return NewDatabaseBarrierManager(testBarrierLogger(), registry)
}

// testCreds returns standard test credentials.
func testCreds() seal.Credentials {
	return seal.Credentials{Secret: []byte("test-password")}
}

// ---------- NewDatabaseBarrierManager tests ----------

func TestDatabaseBarrierManager_New_Success(t *testing.T) {
	mgr := newTestBarrierManager(t)
	if mgr == nil {
		t.Fatal("expected non-nil DatabaseBarrierManager")
	}
	if mgr.logger == nil {
		t.Error("expected non-nil logger")
	}
	if mgr.registry == nil {
		t.Error("expected non-nil registry")
	}
}

func TestDatabaseBarrierManager_New_Fields(t *testing.T) {
	logger := testBarrierLogger()
	system := newTestSystemBarrier(t)
	registry, err := seal.NewBarrierRegistry(logger, system)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}

	mgr := NewDatabaseBarrierManager(logger, registry)
	if mgr.logger != logger {
		t.Error("logger mismatch")
	}
	if mgr.registry != registry {
		t.Error("registry mismatch")
	}
}

// ---------- EnsureBarrier tests ----------

func TestDatabaseBarrierManager_EnsureBarrier_Success(t *testing.T) {
	mgr := newTestBarrierManager(t)
	ctx := context.Background()
	storage := seal.NewMemoryBackend()

	cfg := &DatabaseBarrierConfig{
		Enabled: true,
		Mode:    "passphrase",
	}

	err := mgr.EnsureBarrier(ctx, "testdb", cfg, storage, testCreds())
	if err != nil {
		t.Fatalf("EnsureBarrier: %v", err)
	}

	// Verify the barrier was registered and initialized (unsealed).
	status, err := mgr.BarrierStatus("testdb")
	if err != nil {
		t.Fatalf("BarrierStatus: %v", err)
	}
	if status.Sealed {
		t.Error("expected barrier to be unsealed after EnsureBarrier")
	}
}

func TestDatabaseBarrierManager_EnsureBarrier_DisabledNoop(t *testing.T) {
	mgr := newTestBarrierManager(t)
	ctx := context.Background()
	storage := seal.NewMemoryBackend()

	cfg := &DatabaseBarrierConfig{
		Enabled: false,
	}

	err := mgr.EnsureBarrier(ctx, "testdb", cfg, storage, testCreds())
	if err != nil {
		t.Fatalf("EnsureBarrier with disabled config should succeed: %v", err)
	}

	// Verify no barrier was registered.
	_, err = mgr.BarrierStatus("testdb")
	if err == nil {
		t.Error("expected error for unregistered database barrier")
	}
}

func TestDatabaseBarrierManager_EnsureBarrier_NilConfig(t *testing.T) {
	mgr := newTestBarrierManager(t)
	ctx := context.Background()
	storage := seal.NewMemoryBackend()

	err := mgr.EnsureBarrier(ctx, "testdb", nil, storage, testCreds())
	if err != nil {
		t.Fatalf("EnsureBarrier with nil config should succeed: %v", err)
	}

	// Verify no barrier was registered.
	_, err = mgr.BarrierStatus("testdb")
	if err == nil {
		t.Error("expected error for unregistered database barrier")
	}
}

func TestDatabaseBarrierManager_EnsureBarrier_DuplicateError(t *testing.T) {
	mgr := newTestBarrierManager(t)
	ctx := context.Background()
	storage := seal.NewMemoryBackend()

	cfg := &DatabaseBarrierConfig{
		Enabled: true,
		Mode:    "passphrase",
	}

	err := mgr.EnsureBarrier(ctx, "testdb", cfg, storage, testCreds())
	if err != nil {
		t.Fatalf("first EnsureBarrier: %v", err)
	}

	// Second call with same name should fail.
	err = mgr.EnsureBarrier(ctx, "testdb", cfg, storage, testCreds())
	if err == nil {
		t.Fatal("expected error on duplicate EnsureBarrier")
	}
	var createErr *BarrierCreateError
	if !errors.As(err, &createErr) {
		t.Errorf("expected BarrierCreateError, got %T: %v", err, err)
	}
	if createErr.Name != "testdb" {
		t.Errorf("expected Name=testdb, got %s", createErr.Name)
	}
}

// ---------- SealDatabase tests ----------

func TestDatabaseBarrierManager_SealDatabase_Success(t *testing.T) {
	mgr := newTestBarrierManager(t)
	ctx := context.Background()
	storage := seal.NewMemoryBackend()

	cfg := &DatabaseBarrierConfig{Enabled: true, Mode: "passphrase"}
	if err := mgr.EnsureBarrier(ctx, "testdb", cfg, storage, testCreds()); err != nil {
		t.Fatalf("EnsureBarrier: %v", err)
	}

	// Seal the barrier.
	if err := mgr.SealDatabase("testdb"); err != nil {
		t.Fatalf("SealDatabase: %v", err)
	}

	status, err := mgr.BarrierStatus("testdb")
	if err != nil {
		t.Fatalf("BarrierStatus: %v", err)
	}
	if !status.Sealed {
		t.Error("expected barrier to be sealed after SealDatabase")
	}
}

func TestDatabaseBarrierManager_SealDatabase_NotFound(t *testing.T) {
	mgr := newTestBarrierManager(t)

	err := mgr.SealDatabase("nonexistent")
	if err == nil {
		t.Fatal("expected error for sealing nonexistent database barrier")
	}
	var sealErr *BarrierSealError
	if !errors.As(err, &sealErr) {
		t.Errorf("expected BarrierSealError, got %T: %v", err, err)
	}
	if sealErr.Name != "nonexistent" {
		t.Errorf("expected Name=nonexistent, got %s", sealErr.Name)
	}
}

// ---------- UnsealDatabase tests ----------

func TestDatabaseBarrierManager_UnsealDatabase_Success(t *testing.T) {
	mgr := newTestBarrierManager(t)
	ctx := context.Background()
	storage := seal.NewMemoryBackend()

	cfg := &DatabaseBarrierConfig{Enabled: true, Mode: "passphrase"}
	if err := mgr.EnsureBarrier(ctx, "testdb", cfg, storage, testCreds()); err != nil {
		t.Fatalf("EnsureBarrier: %v", err)
	}

	// Seal first, then unseal.
	if err := mgr.SealDatabase("testdb"); err != nil {
		t.Fatalf("SealDatabase: %v", err)
	}

	if err := mgr.UnsealDatabase(ctx, "testdb", testCreds()); err != nil {
		t.Fatalf("UnsealDatabase: %v", err)
	}

	status, err := mgr.BarrierStatus("testdb")
	if err != nil {
		t.Fatalf("BarrierStatus: %v", err)
	}
	if status.Sealed {
		t.Error("expected barrier to be unsealed after UnsealDatabase")
	}
}

func TestDatabaseBarrierManager_UnsealDatabase_NotFound(t *testing.T) {
	mgr := newTestBarrierManager(t)
	ctx := context.Background()

	err := mgr.UnsealDatabase(ctx, "nonexistent", testCreds())
	if err == nil {
		t.Fatal("expected error for unsealing nonexistent database barrier")
	}
	var unsealErr *BarrierUnsealError
	if !errors.As(err, &unsealErr) {
		t.Errorf("expected BarrierUnsealError, got %T: %v", err, err)
	}
	if unsealErr.Name != "nonexistent" {
		t.Errorf("expected Name=nonexistent, got %s", unsealErr.Name)
	}
}

// ---------- BarrierStatus tests ----------

func TestDatabaseBarrierManager_BarrierStatus_Success(t *testing.T) {
	mgr := newTestBarrierManager(t)
	ctx := context.Background()
	storage := seal.NewMemoryBackend()

	cfg := &DatabaseBarrierConfig{Enabled: true, Mode: "passphrase"}
	if err := mgr.EnsureBarrier(ctx, "testdb", cfg, storage, testCreds()); err != nil {
		t.Fatalf("EnsureBarrier: %v", err)
	}

	status, err := mgr.BarrierStatus("testdb")
	if err != nil {
		t.Fatalf("BarrierStatus: %v", err)
	}
	if status == nil {
		t.Fatal("expected non-nil status")
	}
	if status.Sealed {
		t.Error("expected unsealed status after initialization")
	}
}

func TestDatabaseBarrierManager_BarrierStatus_NotFound(t *testing.T) {
	mgr := newTestBarrierManager(t)

	_, err := mgr.BarrierStatus("nonexistent")
	if err == nil {
		t.Fatal("expected error for status of nonexistent database barrier")
	}
}

// ---------- RemoveBarrier tests ----------

func TestDatabaseBarrierManager_RemoveBarrier_Success(t *testing.T) {
	mgr := newTestBarrierManager(t)
	ctx := context.Background()
	storage := seal.NewMemoryBackend()

	cfg := &DatabaseBarrierConfig{Enabled: true, Mode: "passphrase"}
	if err := mgr.EnsureBarrier(ctx, "testdb", cfg, storage, testCreds()); err != nil {
		t.Fatalf("EnsureBarrier: %v", err)
	}

	if err := mgr.RemoveBarrier("testdb"); err != nil {
		t.Fatalf("RemoveBarrier: %v", err)
	}

	// Verify the barrier is gone.
	_, err := mgr.BarrierStatus("testdb")
	if err == nil {
		t.Error("expected error after RemoveBarrier")
	}
}

func TestDatabaseBarrierManager_RemoveBarrier_NotFound(t *testing.T) {
	mgr := newTestBarrierManager(t)

	err := mgr.RemoveBarrier("nonexistent")
	if err == nil {
		t.Fatal("expected error for removing nonexistent database barrier")
	}
}

// ---------- Full lifecycle test ----------

func TestDatabaseBarrierManager_FullLifecycle(t *testing.T) {
	mgr := newTestBarrierManager(t)
	ctx := context.Background()
	storage := seal.NewMemoryBackend()
	creds := testCreds()

	cfg := &DatabaseBarrierConfig{Enabled: true, Mode: "passphrase"}

	// Create barrier.
	if err := mgr.EnsureBarrier(ctx, "lifecycle-db", cfg, storage, creds); err != nil {
		t.Fatalf("EnsureBarrier: %v", err)
	}

	// Verify unsealed after creation.
	status, err := mgr.BarrierStatus("lifecycle-db")
	if err != nil {
		t.Fatalf("BarrierStatus: %v", err)
	}
	if status.Sealed {
		t.Error("expected unsealed after EnsureBarrier")
	}

	// Seal.
	if err := mgr.SealDatabase("lifecycle-db"); err != nil {
		t.Fatalf("SealDatabase: %v", err)
	}
	status, err = mgr.BarrierStatus("lifecycle-db")
	if err != nil {
		t.Fatalf("BarrierStatus: %v", err)
	}
	if !status.Sealed {
		t.Error("expected sealed after SealDatabase")
	}

	// Unseal.
	if err := mgr.UnsealDatabase(ctx, "lifecycle-db", creds); err != nil {
		t.Fatalf("UnsealDatabase: %v", err)
	}
	status, err = mgr.BarrierStatus("lifecycle-db")
	if err != nil {
		t.Fatalf("BarrierStatus: %v", err)
	}
	if status.Sealed {
		t.Error("expected unsealed after UnsealDatabase")
	}

	// Remove.
	if err := mgr.RemoveBarrier("lifecycle-db"); err != nil {
		t.Fatalf("RemoveBarrier: %v", err)
	}
	_, err = mgr.BarrierStatus("lifecycle-db")
	if err == nil {
		t.Error("expected error after RemoveBarrier")
	}
}

// ---------- Error type tests ----------

func TestBarrierCreateError_ErrorMessage(t *testing.T) {
	inner := errors.New("strategy unavailable")
	err := &BarrierCreateError{Name: "mydb", Err: inner}

	expected := `database: failed to create barrier for database "mydb": strategy unavailable`
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}

func TestBarrierCreateError_Unwrap(t *testing.T) {
	inner := errors.New("strategy unavailable")
	err := &BarrierCreateError{Name: "mydb", Err: inner}

	if !errors.Is(err, inner) {
		t.Error("Unwrap should return the inner error")
	}
}

func TestBarrierSealError_ErrorMessage(t *testing.T) {
	inner := errors.New("tenant not found")
	err := &BarrierSealError{Name: "mydb", Err: inner}

	expected := `database: failed to seal barrier for database "mydb": tenant not found`
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}

func TestBarrierSealError_Unwrap(t *testing.T) {
	inner := errors.New("tenant not found")
	err := &BarrierSealError{Name: "mydb", Err: inner}

	if !errors.Is(err, inner) {
		t.Error("Unwrap should return the inner error")
	}
}

func TestBarrierUnsealError_ErrorMessage(t *testing.T) {
	inner := errors.New("bad credentials")
	err := &BarrierUnsealError{Name: "mydb", Err: inner}

	expected := `database: failed to unseal barrier for database "mydb": bad credentials`
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}

func TestBarrierUnsealError_Unwrap(t *testing.T) {
	inner := errors.New("bad credentials")
	err := &BarrierUnsealError{Name: "mydb", Err: inner}

	if !errors.Is(err, inner) {
		t.Error("Unwrap should return the inner error")
	}
}
