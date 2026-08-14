//go:build qrdb_embedded

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

package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// startEmbeddedPebble creates and starts an EmbeddedServerBinding with
// the pebble storage backend using Raft consensus (backward-compatible
// helper for existing tests). The caller must defer esb.StopEmbedded()
// after a successful return.
func startEmbeddedPebble(t *testing.T) (*EmbeddedServerBinding, *ConnectionBinding) {
	t.Helper()
	return startEmbeddedWithMode(t, "pebble", true)
}

// startEmbeddedWithBackend creates and starts an EmbeddedServerBinding
// with the given storage backend in Raft mode. For non-Raft backends,
// use startEmbeddedLocal.
func startEmbeddedWithBackend(t *testing.T, backend string) (*EmbeddedServerBinding, *ConnectionBinding) {
	t.Helper()
	return startEmbeddedWithMode(t, backend, true)
}

// startEmbeddedLocal creates and starts an EmbeddedServerBinding with
// the given storage backend in local (non-Raft) mode.
func startEmbeddedLocal(t *testing.T, backend string) (*EmbeddedServerBinding, *ConnectionBinding) {
	t.Helper()
	return startEmbeddedWithMode(t, backend, false)
}

// startEmbeddedWithMode creates and starts an EmbeddedServerBinding
// with the given storage backend and Raft mode setting.
func startEmbeddedWithMode(t *testing.T, backend string, raftEnabled bool) (*EmbeddedServerBinding, *ConnectionBinding) {
	t.Helper()
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	esb := NewEmbeddedServerBinding(log, conn, NewSQLBinding(log, conn))
	cfg := &EmbeddedConfig{
		StorageBackend: backend,
		DataDir:        t.TempDir(),
		RaftEnabled:    raftEnabled,
	}
	if err := esb.StartEmbedded(cfg); err != nil {
		t.Fatalf("StartEmbedded(%q, raft=%v): %v", backend, raftEnabled, err)
	}
	return esb, conn
}

// ---------------------------------------------------------------------------
// Memory mode tests (non-Raft)
// ---------------------------------------------------------------------------

func TestStartEmbedded_MemoryMode(t *testing.T) {
	esb, conn := startEmbeddedLocal(t, "memory")
	defer func() {
		if err := esb.StopEmbedded(); err != nil {
			t.Fatalf("StopEmbedded: %v", err)
		}
	}()

	if !conn.IsConnected() {
		t.Fatal("expected IsConnected() == true with memory mode")
	}
}

func TestStartEmbedded_MemoryMode_PutGetDelete(t *testing.T) {
	esb, conn := startEmbeddedLocal(t, "memory")
	defer func() {
		if err := esb.StopEmbedded(); err != nil {
			t.Fatalf("StopEmbedded: %v", err)
		}
	}()

	cl, err := conn.loadClient()
	if err != nil {
		t.Fatalf("loadClient: %v", err)
	}

	ctx := context.Background()
	key := "mem-key"
	value := []byte("mem-value")

	if err := cl.KV().Put(ctx, key, value); err != nil {
		t.Fatalf("Put(%q): %v", key, err)
	}

	got, err := cl.KV().Get(ctx, key)
	if err != nil {
		t.Fatalf("Get(%q): %v", key, err)
	}
	if !bytes.Equal(got, value) {
		t.Fatalf("Get(%q): got %q, want %q", key, got, value)
	}

	if err := cl.KV().Delete(ctx, key); err != nil {
		t.Fatalf("Delete(%q): %v", key, err)
	}

	_, err = cl.KV().Get(ctx, key)
	if err == nil {
		t.Fatalf("expected error from Get(%q) after Delete, got nil", key)
	}
}

func TestStartEmbedded_MemoryMode_UsesLocalBackend(t *testing.T) {
	esb, _ := startEmbeddedLocal(t, "memory")
	defer func() {
		if err := esb.StopEmbedded(); err != nil {
			t.Fatalf("StopEmbedded: %v", err)
		}
	}()

	esb.mu.Lock()
	hasLocal := esb.localBackend != nil
	hasHost := esb.host != nil
	esb.mu.Unlock()

	if !hasLocal {
		t.Fatal("expected localBackend to be set in memory mode")
	}
	if hasHost {
		t.Fatal("expected host to be nil in memory mode")
	}
}

// ---------------------------------------------------------------------------
// File mode tests (non-Raft)
// ---------------------------------------------------------------------------

func TestStartEmbedded_FileMode(t *testing.T) {
	esb, conn := startEmbeddedLocal(t, "file")
	defer func() {
		if err := esb.StopEmbedded(); err != nil {
			t.Fatalf("StopEmbedded: %v", err)
		}
	}()

	if !conn.IsConnected() {
		t.Fatal("expected IsConnected() == true with file mode")
	}
}

func TestStartEmbedded_FileMode_PutGet(t *testing.T) {
	esb, conn := startEmbeddedLocal(t, "file")
	defer func() {
		if err := esb.StopEmbedded(); err != nil {
			t.Fatalf("StopEmbedded: %v", err)
		}
	}()

	cl, err := conn.loadClient()
	if err != nil {
		t.Fatalf("loadClient: %v", err)
	}

	ctx := context.Background()
	if err := cl.KV().Put(ctx, "file-key", []byte("file-value")); err != nil {
		t.Fatalf("Put with file backend: %v", err)
	}

	got, err := cl.KV().Get(ctx, "file-key")
	if err != nil {
		t.Fatalf("Get with file backend: %v", err)
	}
	if !bytes.Equal(got, []byte("file-value")) {
		t.Fatalf("Get: got %q, want %q", got, "file-value")
	}
}

// ---------------------------------------------------------------------------
// PebbleDB mode tests (non-Raft, direct)
// ---------------------------------------------------------------------------

func TestStartEmbedded_PebbleMode_NoRaft(t *testing.T) {
	esb, conn := startEmbeddedLocal(t, "pebble")
	defer func() {
		if err := esb.StopEmbedded(); err != nil {
			t.Fatalf("StopEmbedded: %v", err)
		}
	}()

	if !conn.IsConnected() {
		t.Fatal("expected IsConnected() == true with pebble non-Raft mode")
	}
}

func TestStartEmbedded_PebbleMode_NoRaft_PutGet(t *testing.T) {
	esb, conn := startEmbeddedLocal(t, "pebble")
	defer func() {
		if err := esb.StopEmbedded(); err != nil {
			t.Fatalf("StopEmbedded: %v", err)
		}
	}()

	cl, err := conn.loadClient()
	if err != nil {
		t.Fatalf("loadClient: %v", err)
	}

	ctx := context.Background()
	key := "pebble-key"
	value := []byte("pebble-value")

	if err := cl.KV().Put(ctx, key, value); err != nil {
		t.Fatalf("Put(%q): %v", key, err)
	}

	got, err := cl.KV().Get(ctx, key)
	if err != nil {
		t.Fatalf("Get(%q): %v", key, err)
	}
	if !bytes.Equal(got, value) {
		t.Fatalf("Get(%q): got %q, want %q", key, got, value)
	}
}

func TestStartEmbedded_PebbleMode_NoRaft_NoLeaderError(t *testing.T) {
	// This is the core bug fix test. PebbleDB mode without Raft should
	// not produce "not leader" errors.
	esb, conn := startEmbeddedLocal(t, "pebble")
	defer func() {
		if err := esb.StopEmbedded(); err != nil {
			t.Fatalf("StopEmbedded: %v", err)
		}
	}()

	cl, err := conn.loadClient()
	if err != nil {
		t.Fatalf("loadClient: %v", err)
	}

	ctx := context.Background()
	// Immediately perform operations -- should succeed without leader election.
	if err := cl.KV().Put(ctx, "immediate-key", []byte("immediate-value")); err != nil {
		t.Fatalf("immediate Put should not fail: %v", err)
	}

	got, err := cl.KV().Get(ctx, "immediate-key")
	if err != nil {
		t.Fatalf("immediate Get should not fail: %v", err)
	}
	if !bytes.Equal(got, []byte("immediate-value")) {
		t.Fatalf("Get: got %q, want %q", got, "immediate-value")
	}
}

// ---------------------------------------------------------------------------
// Raft mode tests
// ---------------------------------------------------------------------------

func TestStartEmbedded_RaftMode_PebbleBackend(t *testing.T) {
	esb, conn := startEmbeddedPebble(t)
	defer func() {
		if err := esb.StopEmbedded(); err != nil {
			t.Fatalf("StopEmbedded: %v", err)
		}
	}()

	if !conn.IsConnected() {
		t.Fatal("expected IsConnected() == true after StartEmbedded")
	}
}

func TestStartEmbedded_RaftMode_PebbleBackend_IsEmbeddedTrue(t *testing.T) {
	esb, _ := startEmbeddedPebble(t)
	defer func() {
		if err := esb.StopEmbedded(); err != nil {
			t.Fatalf("StopEmbedded: %v", err)
		}
	}()

	if !esb.IsEmbedded() {
		t.Fatal("expected IsEmbedded() == true after StartEmbedded")
	}
}

func TestStartEmbedded_RaftMode_SDKClientFunctional(t *testing.T) {
	esb, conn := startEmbeddedPebble(t)
	defer func() {
		if err := esb.StopEmbedded(); err != nil {
			t.Fatalf("StopEmbedded: %v", err)
		}
	}()

	cl, err := conn.loadClient()
	if err != nil {
		t.Fatalf("loadClient: %v", err)
	}

	ctx := context.Background()
	key := "test-key"
	value := []byte("test-value")

	if err := cl.KV().Put(ctx, key, value); err != nil {
		t.Fatalf("Put(%q): %v", key, err)
	}

	got, err := cl.KV().Get(ctx, key)
	if err != nil {
		t.Fatalf("Get(%q): %v", key, err)
	}

	if !bytes.Equal(got, value) {
		t.Fatalf("Get(%q): got %q, want %q", key, got, value)
	}
}

func TestStartEmbedded_RaftMode_SDKClientFunctional_Delete(t *testing.T) {
	esb, conn := startEmbeddedPebble(t)
	defer func() {
		if err := esb.StopEmbedded(); err != nil {
			t.Fatalf("StopEmbedded: %v", err)
		}
	}()

	cl, err := conn.loadClient()
	if err != nil {
		t.Fatalf("loadClient: %v", err)
	}

	ctx := context.Background()
	key := "delete-me"
	value := []byte("ephemeral")

	if err := cl.KV().Put(ctx, key, value); err != nil {
		t.Fatalf("Put(%q): %v", key, err)
	}

	if err := cl.KV().Delete(ctx, key); err != nil {
		t.Fatalf("Delete(%q): %v", key, err)
	}

	_, err = cl.KV().Get(ctx, key)
	if err == nil {
		t.Fatalf("expected error from Get(%q) after Delete, got nil", key)
	}
}

func TestStartEmbedded_RaftMode_DefaultDataDir(t *testing.T) {
	// Override XDG_CONFIG_HOME so the default data directory resolves
	// into a temporary location rather than the real user config dir.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	log := testLogger(t)
	conn := NewConnectionBinding(log)
	esb := NewEmbeddedServerBinding(log, conn, NewSQLBinding(log, conn))
	cfg := &EmbeddedConfig{
		StorageBackend: "pebble",
		DataDir:        "", // triggers UserConfigDir fallback
		RaftEnabled:    true,
	}

	if err := esb.StartEmbedded(cfg); err != nil {
		t.Fatalf("StartEmbedded with empty DataDir: %v", err)
	}
	defer func() {
		if err := esb.StopEmbedded(); err != nil {
			t.Fatalf("StopEmbedded: %v", err)
		}
	}()

	if !conn.IsConnected() {
		t.Fatal("expected IsConnected() == true after StartEmbedded with default DataDir")
	}
}

func TestStartEmbedded_RaftMode_DefaultDataDir_IsUsable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	log := testLogger(t)
	conn := NewConnectionBinding(log)
	esb := NewEmbeddedServerBinding(log, conn, NewSQLBinding(log, conn))
	cfg := &EmbeddedConfig{
		StorageBackend: "pebble",
		DataDir:        "",
		RaftEnabled:    true,
	}

	if err := esb.StartEmbedded(cfg); err != nil {
		t.Fatalf("StartEmbedded: %v", err)
	}
	defer func() {
		if err := esb.StopEmbedded(); err != nil {
			t.Fatalf("StopEmbedded: %v", err)
		}
	}()

	cl, err := conn.loadClient()
	if err != nil {
		t.Fatalf("loadClient: %v", err)
	}

	ctx := context.Background()
	if err := cl.KV().Put(ctx, "default-dir-key", []byte("works")); err != nil {
		t.Fatalf("Put failed with default DataDir: %v", err)
	}

	got, err := cl.KV().Get(ctx, "default-dir-key")
	if err != nil {
		t.Fatalf("Get failed with default DataDir: %v", err)
	}
	if !bytes.Equal(got, []byte("works")) {
		t.Fatalf("Get: got %q, want %q", got, "works")
	}
}

func TestStartEmbedded_RaftMode_MvccBackend(t *testing.T) {
	esb, conn := startEmbeddedWithBackend(t, "mvcc")
	defer func() {
		if err := esb.StopEmbedded(); err != nil {
			t.Fatalf("StopEmbedded: %v", err)
		}
	}()

	if !conn.IsConnected() {
		t.Fatal("expected IsConnected() == true with mvcc backend")
	}
}

func TestStartEmbedded_RaftMode_MvccBackend_ClientAccessible(t *testing.T) {
	esb, conn := startEmbeddedWithBackend(t, "mvcc")
	defer func() {
		if err := esb.StopEmbedded(); err != nil {
			t.Fatalf("StopEmbedded: %v", err)
		}
	}()

	cl, err := conn.loadClient()
	if err != nil {
		t.Fatalf("loadClient: %v", err)
	}

	ctx := context.Background()
	health, err := cl.Admin().Health(ctx)
	if err != nil {
		t.Fatalf("Health with mvcc backend: %v", err)
	}
	if health.Status == "" {
		t.Fatal("expected non-empty health status with mvcc backend")
	}
}

func TestStartEmbedded_RaftMode_UsesRaftHost(t *testing.T) {
	esb, _ := startEmbeddedPebble(t)
	defer func() {
		if err := esb.StopEmbedded(); err != nil {
			t.Fatalf("StopEmbedded: %v", err)
		}
	}()

	esb.mu.Lock()
	hasHost := esb.host != nil
	hasLocal := esb.localBackend != nil
	esb.mu.Unlock()

	if !hasHost {
		t.Fatal("expected host to be set in Raft mode")
	}
	if hasLocal {
		t.Fatal("expected localBackend to be nil in Raft mode")
	}
}

// ---------------------------------------------------------------------------
// Unknown backend test
// ---------------------------------------------------------------------------

func TestStartEmbedded_UnknownBackend_NoRaft(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	esb := NewEmbeddedServerBinding(log, conn, NewSQLBinding(log, conn))
	cfg := &EmbeddedConfig{
		StorageBackend: "nonexistent-backend-xyz",
		DataDir:        t.TempDir(),
		RaftEnabled:    false,
	}
	err := esb.StartEmbedded(cfg)
	if err == nil {
		defer func() { _ = esb.StopEmbedded() }()
		t.Fatal("expected error for unknown backend in non-Raft mode")
	}
	var ube *UnknownBackendError
	if !errors.As(err, &ube) {
		t.Fatalf("expected UnknownBackendError (possibly wrapped), got %T: %v", err, err)
	}
}

func TestStartEmbedded_UnknownBackend_Raft_FallsBackToPebble(t *testing.T) {
	// In Raft mode, unknown backends default to "pebble" via
	// resolveStorageSM. This verifies backward compatibility.
	esb, conn := startEmbeddedWithBackend(t, "unknown-engine")
	defer func() {
		if err := esb.StopEmbedded(); err != nil {
			t.Fatalf("StopEmbedded: %v", err)
		}
	}()

	if !conn.IsConnected() {
		t.Fatal("expected IsConnected() == true with fallback pebble backend")
	}
}

// ---------------------------------------------------------------------------
// StopEmbedded tests
// ---------------------------------------------------------------------------

func TestStopEmbedded_ClosesHost(t *testing.T) {
	esb, conn := startEmbeddedPebble(t)

	if !conn.IsConnected() {
		t.Fatal("expected IsConnected() == true before StopEmbedded")
	}

	if err := esb.StopEmbedded(); err != nil {
		t.Fatalf("StopEmbedded: %v", err)
	}

	if conn.IsConnected() {
		t.Fatal("expected IsConnected() == false after StopEmbedded")
	}

	esb.mu.Lock()
	hostNil := esb.host == nil
	esb.mu.Unlock()
	if !hostNil {
		t.Fatal("expected esb.host to be nil after StopEmbedded")
	}
}

func TestStopEmbedded_ClosesLocalBackend(t *testing.T) {
	esb, conn := startEmbeddedLocal(t, "memory")

	if !conn.IsConnected() {
		t.Fatal("expected IsConnected() == true before StopEmbedded")
	}

	if err := esb.StopEmbedded(); err != nil {
		t.Fatalf("StopEmbedded: %v", err)
	}

	if conn.IsConnected() {
		t.Fatal("expected IsConnected() == false after StopEmbedded")
	}

	esb.mu.Lock()
	localNil := esb.localBackend == nil
	esb.mu.Unlock()
	if !localNil {
		t.Fatal("expected esb.localBackend to be nil after StopEmbedded")
	}
}

func TestStopEmbedded_ClearsRunningFlag(t *testing.T) {
	esb, _ := startEmbeddedPebble(t)

	if !esb.IsEmbedded() {
		t.Fatal("expected IsEmbedded() == true before StopEmbedded")
	}

	if err := esb.StopEmbedded(); err != nil {
		t.Fatalf("StopEmbedded: %v", err)
	}

	if esb.IsEmbedded() {
		t.Fatal("expected IsEmbedded() == false after StopEmbedded")
	}
}

func TestStopEmbedded_DisconnectsClient(t *testing.T) {
	esb, conn := startEmbeddedPebble(t)

	// Verify the client is connected and functional before stop.
	cl, err := conn.loadClient()
	if err != nil {
		t.Fatalf("loadClient before stop: %v", err)
	}

	ctx := context.Background()
	if err := cl.KV().Put(ctx, "disconnect-key", []byte("disconnect-value")); err != nil {
		t.Fatalf("Put before stop: %v", err)
	}

	if err := esb.StopEmbedded(); err != nil {
		t.Fatalf("StopEmbedded: %v", err)
	}

	if conn.IsConnected() {
		t.Fatal("expected IsConnected() == false after StopEmbedded")
	}

	// loadClient should return a NotConnectedError after stop.
	_, err = conn.loadClient()
	if err == nil {
		t.Fatal("expected error from loadClient after StopEmbedded")
	}

	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestStopEmbedded_DoubleStopReturnsError(t *testing.T) {
	esb, _ := startEmbeddedPebble(t)

	if err := esb.StopEmbedded(); err != nil {
		t.Fatalf("first StopEmbedded: %v", err)
	}

	err := esb.StopEmbedded()
	if err == nil {
		t.Fatal("expected error from second StopEmbedded call")
	}

	var ese *EmbeddedServerError
	if !errors.As(err, &ese) {
		t.Fatalf("expected EmbeddedServerError, got %T: %v", err, err)
	}

	var nre *NotRunningError
	if !errors.As(err, &nre) {
		t.Fatalf("expected NotRunningError wrapped in EmbeddedServerError, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// resolveStorageSM tests
// ---------------------------------------------------------------------------

func TestResolveStorageSM_Pebble(t *testing.T) {
	if got := resolveStorageSM("pebble"); got != "pebble" {
		t.Fatalf("expected 'pebble', got %q", got)
	}
}

func TestResolveStorageSM_Mvcc(t *testing.T) {
	if got := resolveStorageSM("mvcc"); got != "mvcc" {
		t.Fatalf("expected 'mvcc', got %q", got)
	}
}

func TestResolveStorageSM_UnknownDefaultsPebble(t *testing.T) {
	if got := resolveStorageSM("unknown"); got != "pebble" {
		t.Fatalf("expected 'pebble' for unknown, got %q", got)
	}
}

func TestResolveStorageSM_PebbleDB(t *testing.T) {
	if got := resolveStorageSM("pebbledb"); got != "pebble" {
		t.Fatalf("expected 'pebble' for pebbledb, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Database service wiring tests
// ---------------------------------------------------------------------------

func TestStartEmbedded_PebbleMode_DatabaseServiceWired(t *testing.T) {
	esb, conn := startEmbeddedLocal(t, "pebble")
	defer func() {
		if err := esb.StopEmbedded(); err != nil {
			t.Fatalf("StopEmbedded: %v", err)
		}
	}()

	// Verify the database service was created via the lifecycle node.
	esb.mu.Lock()
	hasDatabaseSvc := esb.svcNode != nil && esb.svcNode.Database != nil
	esb.mu.Unlock()
	if !hasDatabaseSvc {
		t.Fatal("expected database service to be set in pebble mode")
	}

	// Verify we can use the database service through the SDK client.
	cl, err := conn.loadClient()
	if err != nil {
		t.Fatalf("loadClient: %v", err)
	}

	ctx := context.Background()

	// List should return empty initially.
	dbs, err := cl.Database().ListDatabases(ctx)
	if err != nil {
		t.Fatalf("ListDatabases: %v", err)
	}
	if len(dbs) != 0 {
		t.Fatalf("expected 0 databases, got %d", len(dbs))
	}
}

func TestStartEmbedded_PebbleMode_DatabaseCreateAndList(t *testing.T) {
	esb, conn := startEmbeddedLocal(t, "pebble")
	defer func() {
		if err := esb.StopEmbedded(); err != nil {
			t.Fatalf("StopEmbedded: %v", err)
		}
	}()

	cl, err := conn.loadClient()
	if err != nil {
		t.Fatalf("loadClient: %v", err)
	}

	ctx := context.Background()

	// Create a database through the SDK.
	info, err := cl.Database().CreateDatabase(ctx, transport.CreateDatabaseRequest{
		Name:    "test-db",
		Engine:  "kv",
		Backend: "pebble",
	})
	if err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if info.Name != "test-db" {
		t.Errorf("Name = %q, want %q", info.Name, "test-db")
	}
	if info.Engine != "kv" {
		t.Errorf("Engine = %q, want %q", info.Engine, "kv")
	}

	// List should now include the new database.
	dbs, err := cl.Database().ListDatabases(ctx)
	if err != nil {
		t.Fatalf("ListDatabases: %v", err)
	}
	if len(dbs) != 1 {
		t.Fatalf("expected 1 database, got %d", len(dbs))
	}
	if dbs[0].Name != "test-db" {
		t.Errorf("dbs[0].Name = %q, want %q", dbs[0].Name, "test-db")
	}
}

func TestStartEmbedded_MemoryMode_NoDatabaseService(t *testing.T) {
	// Memory mode has no data directory, so resolveDataDir returns
	// an error and the database service is NOT wired. This verifies
	// graceful degradation — the server starts fine without it.
	esb, conn := startEmbeddedLocal(t, "memory")
	defer func() {
		if err := esb.StopEmbedded(); err != nil {
			t.Fatalf("StopEmbedded: %v", err)
		}
	}()

	if !conn.IsConnected() {
		t.Fatal("expected server to start successfully without database service")
	}

	// Memory mode should still have a database service since startEmbeddedLocal
	// uses t.TempDir() as the data dir.
	esb.mu.Lock()
	hasDatabaseSvc := esb.svcNode != nil && esb.svcNode.Database != nil
	esb.mu.Unlock()
	if !hasDatabaseSvc {
		t.Fatal("expected database service to be set even in memory mode (temp data dir)")
	}
}

func TestStopEmbedded_CleansUpDatabaseManager(t *testing.T) {
	esb, _ := startEmbeddedLocal(t, "pebble")

	esb.mu.Lock()
	hasSvcNode := esb.svcNode != nil
	esb.mu.Unlock()
	if !hasSvcNode {
		t.Fatal("expected svcNode to be set before stop")
	}

	if err := esb.StopEmbedded(); err != nil {
		t.Fatalf("StopEmbedded: %v", err)
	}

	esb.mu.Lock()
	svcNodeNil := esb.svcNode == nil
	esb.mu.Unlock()
	if !svcNodeNil {
		t.Fatal("expected svcNode to be nil after StopEmbedded")
	}
}

// ---------------------------------------------------------------------------
// AlreadyRunning test
// ---------------------------------------------------------------------------

func TestStartEmbedded_AlreadyRunning(t *testing.T) {
	esb, _ := startEmbeddedLocal(t, "memory")
	defer func() { _ = esb.StopEmbedded() }()

	err := esb.StartEmbedded(&EmbeddedConfig{
		StorageBackend: "memory",
		DataDir:        t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected error on double start")
	}
	var ese *EmbeddedServerError
	if !errors.As(err, &ese) {
		t.Fatalf("expected EmbeddedServerError, got %T: %v", err, err)
	}
	var are *AlreadyRunningError
	if !errors.As(err, &are) {
		t.Fatalf("expected AlreadyRunningError wrapped, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Error type tests
// ---------------------------------------------------------------------------

func TestUnknownBackendError_Message(t *testing.T) {
	err := &UnknownBackendError{Backend: "baddb"}
	if err.Error() == "" {
		t.Fatal("expected non-empty error")
	}
}

func TestUnknownBackendError_ContainsName(t *testing.T) {
	err := &UnknownBackendError{Backend: "baddb"}
	if !bytes.Contains([]byte(err.Error()), []byte("baddb")) {
		t.Fatalf("expected error to contain backend name, got %q", err.Error())
	}
}

func TestSMOpenError_Message(t *testing.T) {
	err := &SMOpenError{Backend: "pebble", Err: errors.New("disk full")}
	if err.Error() == "" {
		t.Fatal("expected non-empty error")
	}
}

func TestSMOpenError_Unwrap(t *testing.T) {
	inner := errors.New("disk full")
	err := &SMOpenError{Backend: "pebble", Err: inner}
	if !errors.Is(err, inner) {
		t.Fatal("expected SMOpenError to unwrap to inner error")
	}
}

func TestLeaderElectionTimeoutError_Message(t *testing.T) {
	err := &LeaderElectionTimeoutError{ShardID: 1}
	if err.Error() == "" {
		t.Fatal("expected non-empty error")
	}
}

func TestLeaderElectionTimeoutError_ContainsShard(t *testing.T) {
	err := &LeaderElectionTimeoutError{ShardID: 42}
	if !bytes.Contains([]byte(err.Error()), []byte("42")) {
		t.Fatalf("expected error to contain shard ID, got %q", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Regression: stop/restart cycle must release pebble file locks
// ---------------------------------------------------------------------------

// TestStartEmbedded_PebbleRestart_NoLockConflict verifies that stopping
// and restarting an embedded server with the same pebble data directory
// succeeds. This is a regression test for the "lock held by current
// process" error observed on 2026-04-06 when the underlying storage was
// not closed during StopEmbedded.
func TestStartEmbedded_PebbleRestart_NoLockConflict(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	esb := NewEmbeddedServerBinding(log, conn, NewSQLBinding(log, conn))

	// Use a fixed data directory so the second start reuses the same
	// pebble files — this is what triggers the lock contention if the
	// first instance didn't release its file lock.
	dataDir := t.TempDir()
	cfg := &EmbeddedConfig{
		StorageBackend: "pebble",
		DataDir:        dataDir,
		RaftEnabled:    false,
	}

	if err := esb.StartEmbedded(cfg); err != nil {
		t.Fatalf("first StartEmbedded: %v", err)
	}
	if err := esb.StopEmbedded(); err != nil {
		t.Fatalf("StopEmbedded: %v", err)
	}

	// Second start on the same data directory must succeed.
	if err := esb.StartEmbedded(cfg); err != nil {
		t.Fatalf("second StartEmbedded (regression of pebble lock bug): %v", err)
	}
	if err := esb.StopEmbedded(); err != nil {
		t.Fatalf("second StopEmbedded: %v", err)
	}
}

// TestStartEmbedded_PebbleRestart_NoLockConflict_Raft verifies the same
// stop/restart cycle works in Raft mode.
func TestStartEmbedded_PebbleRestart_NoLockConflict_Raft(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	esb := NewEmbeddedServerBinding(log, conn, NewSQLBinding(log, conn))

	dataDir := t.TempDir()
	cfg := &EmbeddedConfig{
		StorageBackend: "pebble",
		DataDir:        dataDir,
		RaftEnabled:    true,
	}

	if err := esb.StartEmbedded(cfg); err != nil {
		t.Fatalf("first StartEmbedded: %v", err)
	}
	if err := esb.StopEmbedded(); err != nil {
		t.Fatalf("StopEmbedded: %v", err)
	}

	// Second start on the same data directory must succeed.
	if err := esb.StartEmbedded(cfg); err != nil {
		t.Fatalf("second StartEmbedded (regression of pebble lock bug): %v", err)
	}
	if err := esb.StopEmbedded(); err != nil {
		t.Fatalf("second StopEmbedded: %v", err)
	}
}
