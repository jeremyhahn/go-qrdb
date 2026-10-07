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
)

// ---------------------------------------------------------------------------
// Mock transport for RemoteBackend tests
// ---------------------------------------------------------------------------

// mockRemoteTransport implements RemoteTransport and records all calls for
// verification. Each method returns the pre-configured result fields.
type mockRemoteTransport struct {
	// Return values
	getValue   []byte
	getErr     error
	putErr     error
	deleteErr  error
	listValue  []string
	listErr    error
	rangeValue []KeyValue
	rangeErr   error
	batchErr   error
	closeErr   error

	// Recorded call arguments
	getCalled    bool
	getKey       []byte
	putCalled    bool
	putKey       []byte
	putValue     []byte
	deleteCalled bool
	deleteKey    []byte
	listCalled   bool
	listPrefix   string
	rangeCalled  bool
	rangeStart   []byte
	rangeEnd     []byte
	rangeLimit   int
	batchCalled  bool
	batchOps     []BatchOp
	closeCalled  bool
}

func (m *mockRemoteTransport) Get(_ context.Context, key []byte) ([]byte, error) {
	m.getCalled = true
	m.getKey = key
	return m.getValue, m.getErr
}

func (m *mockRemoteTransport) Put(_ context.Context, key, value []byte) error {
	m.putCalled = true
	m.putKey = key
	m.putValue = value
	return m.putErr
}

func (m *mockRemoteTransport) Delete(_ context.Context, key []byte) error {
	m.deleteCalled = true
	m.deleteKey = key
	return m.deleteErr
}

func (m *mockRemoteTransport) List(_ context.Context, prefix string) ([]string, error) {
	m.listCalled = true
	m.listPrefix = prefix
	return m.listValue, m.listErr
}

func (m *mockRemoteTransport) Range(_ context.Context, start, end []byte, limit int) ([]KeyValue, error) {
	m.rangeCalled = true
	m.rangeStart = start
	m.rangeEnd = end
	m.rangeLimit = limit
	return m.rangeValue, m.rangeErr
}

func (m *mockRemoteTransport) Batch(_ context.Context, ops []BatchOp) error {
	m.batchCalled = true
	m.batchOps = ops
	return m.batchErr
}

func (m *mockRemoteTransport) Close() error {
	m.closeCalled = true
	return m.closeErr
}

// ---------------------------------------------------------------------------
// EmbeddedBackendFactory tests
// ---------------------------------------------------------------------------

func TestEmbeddedBackendFactory_InterfaceCompliance(t *testing.T) {
	var _ BackendFactory = (*EmbeddedBackendFactory)(nil)
}

func TestEmbeddedBackendFactory_Create_Memory(t *testing.T) {
	factory := NewEmbeddedBackendFactory()
	cfg := DatabaseConfig{
		Name:    "test-mem",
		Backend: BackendMemory,
	}

	backend, err := factory.Create(cfg, "")
	if err != nil {
		t.Fatalf("Create memory backend: unexpected error: %v", err)
	}
	t.Cleanup(func() {
		_ = backend.Close()
	})

	// Verify the backend is functional by performing a round-trip.
	ctx := context.Background()
	if err := backend.Put(ctx, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("Put: unexpected error: %v", err)
	}
	got, err := backend.Get(ctx, []byte("k"))
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if string(got) != "v" {
		t.Errorf("Get: got %q, want %q", got, "v")
	}
}

func TestEmbeddedBackendFactory_Create_File(t *testing.T) {
	factory := NewEmbeddedBackendFactory()
	dataDir := t.TempDir()
	cfg := DatabaseConfig{
		Name:    "test-file",
		Backend: BackendFile,
	}

	backend, err := factory.Create(cfg, dataDir)
	if err != nil {
		t.Fatalf("Create file backend: unexpected error: %v", err)
	}
	t.Cleanup(func() {
		_ = backend.Close()
	})

	// Verify the backend is functional by performing a round-trip.
	ctx := context.Background()
	if err := backend.Put(ctx, []byte("file-key"), []byte("file-value")); err != nil {
		t.Fatalf("Put: unexpected error: %v", err)
	}
	got, err := backend.Get(ctx, []byte("file-key"))
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if string(got) != "file-value" {
		t.Errorf("Get: got %q, want %q", got, "file-value")
	}
}

func TestEmbeddedBackendFactory_Create_Pebble(t *testing.T) {
	factory := NewEmbeddedBackendFactory()
	dataDir := t.TempDir()
	cfg := DatabaseConfig{
		Name:    "test-pebble",
		Backend: BackendPebble,
	}

	backend, err := factory.Create(cfg, dataDir)
	if err != nil {
		t.Fatalf("Create pebble backend: unexpected error: %v", err)
	}
	t.Cleanup(func() {
		_ = backend.Close()
	})

	// Verify the backend is functional by performing a round-trip.
	ctx := context.Background()
	if err := backend.Put(ctx, []byte("pebble-key"), []byte("pebble-value")); err != nil {
		t.Fatalf("Put: unexpected error: %v", err)
	}
	got, err := backend.Get(ctx, []byte("pebble-key"))
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if string(got) != "pebble-value" {
		t.Errorf("Get: got %q, want %q", got, "pebble-value")
	}
}

func TestEmbeddedBackendFactory_Create_InvalidBackend(t *testing.T) {
	factory := NewEmbeddedBackendFactory()
	cfg := DatabaseConfig{
		Name:    "test-invalid",
		Backend: BackendType("nosql"),
	}

	_, err := factory.Create(cfg, "")
	if err == nil {
		t.Fatal("Create invalid backend: expected error, got nil")
	}

	var target *InvalidBackendError
	if !errors.As(err, &target) {
		t.Fatalf("Create invalid backend: expected *InvalidBackendError, got %T: %v", err, err)
	}
	if target.Backend != "nosql" {
		t.Errorf("InvalidBackendError.Backend: got %q, want %q", target.Backend, "nosql")
	}
}

func TestEmbeddedBackendFactory_Create_RaftBackend(t *testing.T) {
	factory := NewEmbeddedBackendFactory()
	cfg := DatabaseConfig{
		Name:    "test-raft",
		Backend: BackendRaft,
	}

	// A dedicated-raft database's authoritative data plane is a per-database
	// shard on the shared system Host managed by the server-layer
	// RaftBackendFactory (IsolationRaft). The Manager still opens a Backend so
	// the database's meta.json, catalog membership, and shard-ID allocation are
	// tracked uniformly; that bookkeeping role is served by a lightweight
	// in-memory placeholder, which must be created without error.
	backend, err := factory.Create(cfg, "")
	if err != nil {
		t.Fatalf("Create raft backend: unexpected error: %v", err)
	}
	if backend == nil {
		t.Fatal("Create raft backend: expected non-nil placeholder backend")
	}
	if err := backend.Close(); err != nil {
		t.Fatalf("Close raft placeholder backend: %v", err)
	}
}

func TestEmbeddedBackendFactory_Close(t *testing.T) {
	factory := NewEmbeddedBackendFactory()
	if err := factory.Close(); err != nil {
		t.Fatalf("Close: unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// RemoteBackend tests
// ---------------------------------------------------------------------------

func TestRemoteBackend_InterfaceCompliance(t *testing.T) {
	var _ Backend = (*RemoteBackend)(nil)
}

func TestRemoteBackend_Get(t *testing.T) {
	mock := &mockRemoteTransport{
		getValue: []byte("remote-value"),
	}
	backend := NewRemoteBackend(mock)
	ctx := context.Background()

	got, err := backend.Get(ctx, []byte("remote-key"))
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if string(got) != "remote-value" {
		t.Errorf("Get: got %q, want %q", got, "remote-value")
	}
	if !mock.getCalled {
		t.Error("Get: transport.Get was not called")
	}
	if string(mock.getKey) != "remote-key" {
		t.Errorf("Get: transport received key %q, want %q", mock.getKey, "remote-key")
	}
}

func TestRemoteBackend_Get_Error(t *testing.T) {
	transportErr := errors.New("connection refused")
	mock := &mockRemoteTransport{
		getErr: transportErr,
	}
	backend := NewRemoteBackend(mock)
	ctx := context.Background()

	_, err := backend.Get(ctx, []byte("key"))
	if err == nil {
		t.Fatal("Get error: expected error, got nil")
	}
	if !errors.Is(err, transportErr) {
		t.Errorf("Get error: got %v, want %v", err, transportErr)
	}
}

func TestRemoteBackend_Put(t *testing.T) {
	mock := &mockRemoteTransport{}
	backend := NewRemoteBackend(mock)
	ctx := context.Background()

	err := backend.Put(ctx, []byte("put-key"), []byte("put-value"))
	if err != nil {
		t.Fatalf("Put: unexpected error: %v", err)
	}
	if !mock.putCalled {
		t.Error("Put: transport.Put was not called")
	}
	if string(mock.putKey) != "put-key" {
		t.Errorf("Put: transport received key %q, want %q", mock.putKey, "put-key")
	}
	if string(mock.putValue) != "put-value" {
		t.Errorf("Put: transport received value %q, want %q", mock.putValue, "put-value")
	}
}

func TestRemoteBackend_Put_Error(t *testing.T) {
	transportErr := errors.New("write failed")
	mock := &mockRemoteTransport{
		putErr: transportErr,
	}
	backend := NewRemoteBackend(mock)
	ctx := context.Background()

	err := backend.Put(ctx, []byte("k"), []byte("v"))
	if err == nil {
		t.Fatal("Put error: expected error, got nil")
	}
	if !errors.Is(err, transportErr) {
		t.Errorf("Put error: got %v, want %v", err, transportErr)
	}
}

func TestRemoteBackend_Delete(t *testing.T) {
	mock := &mockRemoteTransport{}
	backend := NewRemoteBackend(mock)
	ctx := context.Background()

	err := backend.Delete(ctx, []byte("del-key"))
	if err != nil {
		t.Fatalf("Delete: unexpected error: %v", err)
	}
	if !mock.deleteCalled {
		t.Error("Delete: transport.Delete was not called")
	}
	if string(mock.deleteKey) != "del-key" {
		t.Errorf("Delete: transport received key %q, want %q", mock.deleteKey, "del-key")
	}
}

func TestRemoteBackend_Delete_Error(t *testing.T) {
	transportErr := errors.New("delete failed")
	mock := &mockRemoteTransport{
		deleteErr: transportErr,
	}
	backend := NewRemoteBackend(mock)
	ctx := context.Background()

	err := backend.Delete(ctx, []byte("k"))
	if err == nil {
		t.Fatal("Delete error: expected error, got nil")
	}
	if !errors.Is(err, transportErr) {
		t.Errorf("Delete error: got %v, want %v", err, transportErr)
	}
}

func TestRemoteBackend_List(t *testing.T) {
	mock := &mockRemoteTransport{
		listValue: []string{"users/alice", "users/bob"},
	}
	backend := NewRemoteBackend(mock)
	ctx := context.Background()

	got, err := backend.List(ctx, "users/")
	if err != nil {
		t.Fatalf("List: unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List: got %d keys, want 2", len(got))
	}
	if got[0] != "users/alice" || got[1] != "users/bob" {
		t.Errorf("List: got %v, want [users/alice users/bob]", got)
	}
	if !mock.listCalled {
		t.Error("List: transport.List was not called")
	}
	if mock.listPrefix != "users/" {
		t.Errorf("List: transport received prefix %q, want %q", mock.listPrefix, "users/")
	}
}

func TestRemoteBackend_List_Error(t *testing.T) {
	transportErr := errors.New("list failed")
	mock := &mockRemoteTransport{
		listErr: transportErr,
	}
	backend := NewRemoteBackend(mock)
	ctx := context.Background()

	_, err := backend.List(ctx, "prefix")
	if err == nil {
		t.Fatal("List error: expected error, got nil")
	}
	if !errors.Is(err, transportErr) {
		t.Errorf("List error: got %v, want %v", err, transportErr)
	}
}

func TestRemoteBackend_Range(t *testing.T) {
	expected := []KeyValue{
		{Key: []byte("b"), Value: []byte("val-b")},
		{Key: []byte("c"), Value: []byte("val-c")},
	}
	mock := &mockRemoteTransport{
		rangeValue: expected,
	}
	backend := NewRemoteBackend(mock)
	ctx := context.Background()

	got, err := backend.Range(ctx, []byte("b"), []byte("d"), 10)
	if err != nil {
		t.Fatalf("Range: unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Range: got %d results, want 2", len(got))
	}
	if string(got[0].Key) != "b" || string(got[1].Key) != "c" {
		t.Errorf("Range: got unexpected keys: %v", got)
	}
	if !mock.rangeCalled {
		t.Error("Range: transport.Range was not called")
	}
	if string(mock.rangeStart) != "b" {
		t.Errorf("Range: transport received start %q, want %q", mock.rangeStart, "b")
	}
	if string(mock.rangeEnd) != "d" {
		t.Errorf("Range: transport received end %q, want %q", mock.rangeEnd, "d")
	}
	if mock.rangeLimit != 10 {
		t.Errorf("Range: transport received limit %d, want 10", mock.rangeLimit)
	}
}

func TestRemoteBackend_Range_Error(t *testing.T) {
	transportErr := errors.New("range failed")
	mock := &mockRemoteTransport{
		rangeErr: transportErr,
	}
	backend := NewRemoteBackend(mock)
	ctx := context.Background()

	_, err := backend.Range(ctx, []byte("a"), []byte("z"), 0)
	if err == nil {
		t.Fatal("Range error: expected error, got nil")
	}
	if !errors.Is(err, transportErr) {
		t.Errorf("Range error: got %v, want %v", err, transportErr)
	}
}

func TestRemoteBackend_Batch(t *testing.T) {
	mock := &mockRemoteTransport{}
	backend := NewRemoteBackend(mock)
	ctx := context.Background()

	ops := []BatchOp{
		{Op: BatchOpPut, Key: []byte("a"), Value: []byte("1")},
		{Op: BatchOpDelete, Key: []byte("b")},
	}

	err := backend.Batch(ctx, ops)
	if err != nil {
		t.Fatalf("Batch: unexpected error: %v", err)
	}
	if !mock.batchCalled {
		t.Error("Batch: transport.Batch was not called")
	}
	if len(mock.batchOps) != 2 {
		t.Fatalf("Batch: transport received %d ops, want 2", len(mock.batchOps))
	}
	if mock.batchOps[0].Op != BatchOpPut {
		t.Errorf("Batch: ops[0].Op = %d, want %d", mock.batchOps[0].Op, BatchOpPut)
	}
	if mock.batchOps[1].Op != BatchOpDelete {
		t.Errorf("Batch: ops[1].Op = %d, want %d", mock.batchOps[1].Op, BatchOpDelete)
	}
}

func TestRemoteBackend_Batch_Error(t *testing.T) {
	transportErr := errors.New("batch failed")
	mock := &mockRemoteTransport{
		batchErr: transportErr,
	}
	backend := NewRemoteBackend(mock)
	ctx := context.Background()

	err := backend.Batch(ctx, []BatchOp{{Op: BatchOpPut, Key: []byte("k"), Value: []byte("v")}})
	if err == nil {
		t.Fatal("Batch error: expected error, got nil")
	}
	if !errors.Is(err, transportErr) {
		t.Errorf("Batch error: got %v, want %v", err, transportErr)
	}
}

func TestRemoteBackend_Close(t *testing.T) {
	mock := &mockRemoteTransport{}
	backend := NewRemoteBackend(mock)

	err := backend.Close()
	if err != nil {
		t.Fatalf("Close: unexpected error: %v", err)
	}
	if !mock.closeCalled {
		t.Error("Close: transport.Close was not called")
	}
}

func TestRemoteBackend_Close_Error(t *testing.T) {
	transportErr := errors.New("close failed")
	mock := &mockRemoteTransport{
		closeErr: transportErr,
	}
	backend := NewRemoteBackend(mock)

	err := backend.Close()
	if err == nil {
		t.Fatal("Close error: expected error, got nil")
	}
	if !errors.Is(err, transportErr) {
		t.Errorf("Close error: got %v, want %v", err, transportErr)
	}
}

// ---------------------------------------------------------------------------
// openStorage tests (exercised indirectly through factory, but also directly)
// ---------------------------------------------------------------------------

func TestOpenStorage_Memory(t *testing.T) {
	store, err := openStorage("mem-db", BackendMemory, "")
	if err != nil {
		t.Fatalf("openStorage memory: unexpected error: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	if store == nil {
		t.Fatal("openStorage memory: returned nil store")
	}
}

func TestOpenStorage_File(t *testing.T) {
	dataDir := t.TempDir()
	store, err := openStorage("file-db", BackendFile, dataDir)
	if err != nil {
		t.Fatalf("openStorage file: unexpected error: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	if store == nil {
		t.Fatal("openStorage file: returned nil store")
	}
}

func TestOpenStorage_Pebble(t *testing.T) {
	dataDir := t.TempDir()
	store, err := openStorage("pebble-db", BackendPebble, dataDir)
	if err != nil {
		t.Fatalf("openStorage pebble: unexpected error: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	if store == nil {
		t.Fatal("openStorage pebble: returned nil store")
	}
}

func TestOpenStorage_Raft(t *testing.T) {
	// openStorage returns a lightweight in-memory placeholder for the raft
	// backend: the authoritative data plane is the per-database shard on the
	// shared system Host (IsolationRaft), wired at the server layer. The
	// placeholder lets the Manager track meta.json/catalog/shard-ID uniformly.
	store, err := openStorage("raft-db", BackendRaft, "")
	if err != nil {
		t.Fatalf("openStorage raft: unexpected error: %v", err)
	}
	if store == nil {
		t.Fatal("openStorage raft: returned nil placeholder store")
	}
}

func TestOpenStorage_InvalidBackend(t *testing.T) {
	_, err := openStorage("bad-db", BackendType("unknown"), "")
	if err == nil {
		t.Fatal("openStorage invalid: expected error, got nil")
	}

	var target *InvalidBackendError
	if !errors.As(err, &target) {
		t.Fatalf("openStorage invalid: expected *InvalidBackendError, got %T: %v", err, err)
	}
	if target.Backend != "unknown" {
		t.Errorf("InvalidBackendError.Backend: got %q, want %q", target.Backend, "unknown")
	}
}
