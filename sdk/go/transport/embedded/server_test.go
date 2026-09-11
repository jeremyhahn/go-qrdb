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

package embedded

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Nil / missing config
// ---------------------------------------------------------------------------

func TestNewServer_NilConfig(t *testing.T) {
	ctx := context.Background()
	_, err := NewServer(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil config, got nil")
	}
	var cfgErr *ServerConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("expected *ServerConfigError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Backend creation (happy paths)
// ---------------------------------------------------------------------------

func TestNewServer_MemoryBackend(t *testing.T) {
	ctx := context.Background()
	srv, err := NewServer(ctx, &ServerConfig{
		Backend: database.BackendMemory,
	})
	if err != nil {
		t.Fatalf("NewServer(memory): %v", err)
	}
	defer func() {
		if err := srv.Shutdown(ctx); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	}()

	client := srv.Client()
	if client == nil {
		t.Fatal("Client() returned nil")
	}

	// Basic KV round-trip.
	kv := client.KV()
	if err := kv.Put(ctx, "hello", []byte("world")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := kv.Get(ctx, "hello")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "world" {
		t.Fatalf("Get: got %q, want %q", got, "world")
	}
}

func TestNewServer_PebbleBackend(t *testing.T) {
	ctx := context.Background()
	srv, err := NewServer(ctx, &ServerConfig{
		Backend: database.BackendPebble,
		DataDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewServer(pebble): %v", err)
	}
	defer func() {
		if err := srv.Shutdown(ctx); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	}()

	kv := srv.Client().KV()
	if err := kv.Put(ctx, "pk", []byte("pv")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := kv.Get(ctx, "pk")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "pv" {
		t.Fatalf("Get: got %q, want %q", got, "pv")
	}
}

func TestNewServer_FileBackend(t *testing.T) {
	ctx := context.Background()
	srv, err := NewServer(ctx, &ServerConfig{
		Backend: database.BackendFile,
		DataDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewServer(file): %v", err)
	}
	defer func() {
		if err := srv.Shutdown(ctx); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	}()

	kv := srv.Client().KV()
	if err := kv.Put(ctx, "fk", []byte("fv")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := kv.Get(ctx, "fk")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "fv" {
		t.Fatalf("Get: got %q, want %q", got, "fv")
	}
}

func TestNewServer_DefaultBackend(t *testing.T) {
	ctx := context.Background()
	// Zero-value Backend should default to BackendPebble, which needs DataDir.
	srv, err := NewServer(ctx, &ServerConfig{
		DataDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewServer(default backend): %v", err)
	}
	defer func() {
		if err := srv.Shutdown(ctx); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	}()

	kv := srv.Client().KV()
	if err := kv.Put(ctx, "dk", []byte("dv")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := kv.Get(ctx, "dk")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "dv" {
		t.Fatalf("Get: got %q, want %q", got, "dv")
	}
}

// ---------------------------------------------------------------------------
// Validation error paths (table-driven)
// ---------------------------------------------------------------------------

func TestNewServer_ValidationErrors(t *testing.T) {
	tests := []struct {
		name   string
		config *ServerConfig
		field  string
	}{
		{
			name: "MissingDataDir_Pebble",
			config: &ServerConfig{
				Backend: database.BackendPebble,
			},
			field: "DataDir",
		},
		{
			name: "MissingDataDir_File",
			config: &ServerConfig{
				Backend: database.BackendFile,
			},
			field: "DataDir",
		},
		{
			name: "InvalidBackend",
			config: &ServerConfig{
				Backend: database.BackendType("badbackend"),
				DataDir: t.TempDir(),
			},
			field: "Backend",
		},
		{
			name: "Barrier_ShamirMissingConfig",
			config: &ServerConfig{
				Backend: database.BackendPebble,
				DataDir: t.TempDir(),
				Barrier: &BarrierConfig{
					Strategy: seal.StrategyShamir,
				},
			},
			field: "Barrier.Shamir",
		},
		{
			name: "Barrier_ShamirThresholdTooLow",
			config: &ServerConfig{
				Backend: database.BackendPebble,
				DataDir: t.TempDir(),
				Barrier: &BarrierConfig{
					Strategy: seal.StrategyShamir,
					Shamir:   &ShamirConfig{Threshold: 1, Total: 3},
				},
			},
			field: "Barrier.Shamir.Threshold",
		},
		{
			name: "Barrier_ShamirTotalLessThanThreshold",
			config: &ServerConfig{
				Backend: database.BackendPebble,
				DataDir: t.TempDir(),
				Barrier: &BarrierConfig{
					Strategy: seal.StrategyShamir,
					Shamir:   &ShamirConfig{Threshold: 3, Total: 2},
				},
			},
			field: "Barrier.Shamir.Total",
		},
		{
			name: "Barrier_RequiresDataDir",
			config: &ServerConfig{
				Backend: database.BackendMemory,
				Barrier: &BarrierConfig{
					Strategy: seal.StrategyPassphrase,
				},
			},
			field: "DataDir",
		},
	}

	ctx := context.Background()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewServer(ctx, tt.config)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			var cfgErr *ServerConfigError
			if !errors.As(err, &cfgErr) {
				t.Fatalf("expected *ServerConfigError, got %T: %v", err, err)
			}
			if cfgErr.Field != tt.field {
				t.Fatalf("expected Field=%q, got %q", tt.field, cfgErr.Field)
			}
		})
	}
}

func TestNewServer_MemoryBackend_NoDataDir(t *testing.T) {
	ctx := context.Background()
	srv, err := NewServer(ctx, &ServerConfig{
		Backend: database.BackendMemory,
	})
	if err != nil {
		t.Fatalf("NewServer(memory, no DataDir): %v", err)
	}
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Barrier (software strategy)
// ---------------------------------------------------------------------------

func TestNewServer_WithBarrier_Software(t *testing.T) {
	ctx := context.Background()
	// Use the memory backend so the deferred SDK init flow is valid.
	// The disk-backed pebble path requires AutoUnseal because the
	// encrypting FS cannot be wired to a sealed barrier; that path is
	// covered separately in server_encfs_test.go.
	srv, err := NewServer(ctx, &ServerConfig{
		Backend: database.BackendMemory,
		DataDir: t.TempDir(),
		Barrier: &BarrierConfig{
			Strategy: seal.StrategyPassphrase,
		},
	})
	if err != nil {
		t.Fatalf("NewServer(barrier/software): %v", err)
	}
	defer func() {
		if err := srv.Shutdown(ctx); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	}()

	status, err := srv.Client().Barrier().BarrierStatus(ctx)
	if err != nil {
		t.Fatalf("BarrierStatus: %v", err)
	}
	if !status.Sealed {
		t.Fatal("expected barrier to be sealed before initialization")
	}
	if status.InitializedAt != "" {
		t.Fatal("expected barrier to not be initialized")
	}
}

// ---------------------------------------------------------------------------
// Barrier (shamir strategy)
// ---------------------------------------------------------------------------

func TestNewServer_WithBarrier_Shamir(t *testing.T) {
	ctx := context.Background()
	// Memory backend: covers the deferred SDK init path. Disk-backed
	// pebble + shamir + AutoUnseal is exercised in server_encfs_test.go.
	srv, err := NewServer(ctx, &ServerConfig{
		Backend: database.BackendMemory,
		DataDir: t.TempDir(),
		Barrier: &BarrierConfig{
			Strategy: seal.StrategyShamir,
			Shamir:   &ShamirConfig{Threshold: 2, Total: 3},
		},
	})
	if err != nil {
		t.Fatalf("NewServer(barrier/shamir): %v", err)
	}
	defer func() {
		if err := srv.Shutdown(ctx); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	}()

	status, err := srv.Client().Barrier().BarrierStatus(ctx)
	if err != nil {
		t.Fatalf("BarrierStatus: %v", err)
	}
	if !status.Sealed {
		t.Fatal("expected barrier to be sealed before initialization")
	}
}

// ---------------------------------------------------------------------------
// Shutdown idempotency
// ---------------------------------------------------------------------------

func TestNewServer_Shutdown_Idempotent(t *testing.T) {
	ctx := context.Background()
	srv, err := NewServer(ctx, &ServerConfig{
		Backend: database.BackendMemory,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("first Shutdown: %v", err)
	}
	// Second call must not panic or return an error.
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
}

// ---------------------------------------------------------------------------
// KV operations (Put, Get, Delete)
// ---------------------------------------------------------------------------

func TestNewServer_KVOperations(t *testing.T) {
	ctx := context.Background()
	srv, err := NewServer(ctx, &ServerConfig{
		Backend: database.BackendMemory,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer func() {
		if err := srv.Shutdown(ctx); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	}()

	kv := srv.Client().KV()

	// Put + Get
	if err := kv.Put(ctx, "key1", []byte("val1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := kv.Get(ctx, "key1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "val1" {
		t.Fatalf("Get: got %q, want %q", got, "val1")
	}

	// Overwrite
	if err := kv.Put(ctx, "key1", []byte("val2")); err != nil {
		t.Fatalf("Put overwrite: %v", err)
	}
	got, err = kv.Get(ctx, "key1")
	if err != nil {
		t.Fatalf("Get after overwrite: %v", err)
	}
	if string(got) != "val2" {
		t.Fatalf("Get after overwrite: got %q, want %q", got, "val2")
	}

	// Delete
	if err := kv.Delete(ctx, "key1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err = kv.Get(ctx, "key1")
	if err == nil {
		t.Fatal("expected error after Delete, got nil")
	}
	var knf *transport.KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Fatalf("expected *transport.KeyNotFoundError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Barrier initialize and unseal lifecycle
// ---------------------------------------------------------------------------

func TestNewServer_BarrierInitializeAndUnseal(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()

	// Memory backend keeps the deferred SDK init/unseal API surface
	// exercisable. Disk-backed pebble requires AutoUnseal so that the
	// encrypting FS opens behind an unsealed barrier; see
	// server_encfs_test.go for that flow.
	srv, err := NewServer(ctx, &ServerConfig{
		Backend: database.BackendMemory,
		DataDir: dataDir,
		Barrier: &BarrierConfig{
			Strategy: seal.StrategyPassphrase,
		},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer func() {
		if err := srv.Shutdown(ctx); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	}()

	barrierClient := srv.Client().Barrier()

	// Verify initial state: sealed, not initialized.
	status, err := barrierClient.BarrierStatus(ctx)
	if err != nil {
		t.Fatalf("BarrierStatus (initial): %v", err)
	}
	if !status.Sealed {
		t.Fatal("expected barrier sealed before init")
	}
	if status.InitializedAt != "" {
		t.Fatal("expected barrier not initialized before init")
	}

	// Build the bootstrap passphrase (matches deriveBootstrapPassphrase(0)).
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, 0)
	passphrase := make([]byte, 0, 32)
	passphrase = append(passphrase, []byte("qrdb-seal-bootstrap:")...)
	passphrase = append(passphrase, buf...)

	// Initialize the barrier.
	err = barrierClient.BarrierInitialize(ctx, &transport.BarrierInitializeRequest{
		Secret: passphrase,
	})
	if err != nil {
		t.Fatalf("BarrierInitialize: %v", err)
	}

	// After initialization the barrier should be unsealed.
	status, err = barrierClient.BarrierStatus(ctx)
	if err != nil {
		t.Fatalf("BarrierStatus (after init): %v", err)
	}
	if status.Sealed {
		t.Fatal("expected barrier to be unsealed after initialization")
	}
	if status.Strategy == "" {
		t.Fatal("expected Strategy to be set after initialization")
	}
	if status.InitializedAt == "" {
		t.Fatal("expected InitializedAt to be populated after initialization")
	}

	// KV operations should work while unsealed.
	kv := srv.Client().KV()
	if err := kv.Put(ctx, "sealed-key", []byte("sealed-val")); err != nil {
		t.Fatalf("Put (unsealed): %v", err)
	}
	got, err := kv.Get(ctx, "sealed-key")
	if err != nil {
		t.Fatalf("Get (unsealed): %v", err)
	}
	if string(got) != "sealed-val" {
		t.Fatalf("Get (unsealed): got %q, want %q", got, "sealed-val")
	}

	// Seal the barrier.
	if err := barrierClient.BarrierSeal(ctx); err != nil {
		t.Fatalf("BarrierSeal: %v", err)
	}

	// Verify sealed.
	status, err = barrierClient.BarrierStatus(ctx)
	if err != nil {
		t.Fatalf("BarrierStatus (after seal): %v", err)
	}
	if !status.Sealed {
		t.Fatal("expected barrier to be sealed after BarrierSeal")
	}
}
