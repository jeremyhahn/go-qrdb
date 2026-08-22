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

// White-box tests for kvStoreAdapter. Uses package lifecycle (not lifecycle_test)
// to access unexported constructor and struct.
package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// mockKVServicer mirrors the pattern in cmd/qrdbd/adapters_test.go.
// ---------------------------------------------------------------------------

type mockKVServicer struct {
	putFn         func(ctx context.Context, key string, value []byte) error
	putIfAbsentFn func(ctx context.Context, key string, value []byte) (bool, error)
	getFn         func(ctx context.Context, key string) ([]byte, error)
	deleteFn      func(ctx context.Context, key string) error
	scanFn        func(ctx context.Context, prefix string) (map[string][]byte, error)
	listFn        func(ctx context.Context, prefix string) ([]string, error)
}

func (m *mockKVServicer) Put(ctx context.Context, key string, value []byte) error {
	if m.putFn != nil {
		return m.putFn(ctx, key, value)
	}
	return nil
}

// PutIfAbsent reports a successful create by default. The adapter under test
// only forwards the call, so the mock's job is to satisfy the interface and let
// an individual test override the outcome it cares about.
func (m *mockKVServicer) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if m.putIfAbsentFn != nil {
		return m.putIfAbsentFn(ctx, key, value)
	}
	return true, nil
}

// CompareAndDelete mirrors PutIfAbsent's guard on the release side:
// it removes the key only when the stored value still equals expected.
func (m *mockKVServicer) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	return false, nil
}

func (m *mockKVServicer) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return false, nil
}

func (m *mockKVServicer) Get(ctx context.Context, key string) ([]byte, error) {
	if m.getFn != nil {
		return m.getFn(ctx, key)
	}
	return nil, nil
}

func (m *mockKVServicer) Delete(ctx context.Context, key string) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, key)
	}
	return nil
}

func (m *mockKVServicer) Scan(ctx context.Context, prefix string) (map[string][]byte, error) {
	if m.scanFn != nil {
		return m.scanFn(ctx, prefix)
	}
	return nil, nil
}

func (m *mockKVServicer) List(ctx context.Context, prefix string) ([]string, error) {
	if m.listFn != nil {
		return m.listFn(ctx, prefix)
	}
	return nil, nil
}

func (m *mockKVServicer) GetStale(ctx context.Context, key string) ([]byte, error) {
	return m.Get(ctx, key)
}

func (m *mockKVServicer) ListStale(ctx context.Context, prefix string) ([]string, error) {
	return m.List(ctx, prefix)
}

func (m *mockKVServicer) ScanStale(ctx context.Context, prefix string) (map[string][]byte, error) {
	return m.Scan(ctx, prefix)
}

func (m *mockKVServicer) PutWithMode(_ context.Context, _ string, _ []byte, _ writemode.Mode) error {
	return nil
}

func (m *mockKVServicer) DeleteWithMode(_ context.Context, _ string, _ writemode.Mode) error {
	return nil
}

func (m *mockKVServicer) GetWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) ([]byte, error) {
	return nil, nil
}

func (m *mockKVServicer) ListWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) ([]string, error) {
	return nil, nil
}

func (m *mockKVServicer) ScanWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) (map[string][]byte, error) {
	return nil, nil
}

func (m *mockKVServicer) ExistsWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) (bool, error) {
	return false, nil
}

func (m *mockKVServicer) PutEntity(_ context.Context, _ *service.PutEntityRequest) (*service.PutEntityResponse, error) {
	return nil, nil
}

func (m *mockKVServicer) GetEntity(_ context.Context, _ *service.GetEntityRequest) (*service.GetEntityResponse, error) {
	return nil, nil
}

func (m *mockKVServicer) DeleteEntity(_ context.Context, _ *service.DeleteEntityRequest) (*service.DeleteEntityResponse, error) {
	return nil, nil
}

func (m *mockKVServicer) ListEntities(_ context.Context, _ *service.ListEntitiesRequest) (*service.ListEntitiesResponse, error) {
	return nil, nil
}

// compile-time check that mockKVServicer satisfies service.KVServicer.
var _ service.KVServicer = (*mockKVServicer)(nil)

// ---------------------------------------------------------------------------
// Put
// ---------------------------------------------------------------------------

func TestKVStoreAdapter_Put_Success(t *testing.T) {
	var capturedKey string
	var capturedVal []byte
	mock := &mockKVServicer{
		putFn: func(_ context.Context, key string, value []byte) error {
			capturedKey = key
			capturedVal = value
			return nil
		},
	}
	adapter := newKVStoreAdapter(mock, nil)

	if err := adapter.Put(context.Background(), "hello", []byte("world")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if capturedKey != "hello" {
		t.Errorf("key: got %q want %q", capturedKey, "hello")
	}
	if string(capturedVal) != "world" {
		t.Errorf("value: got %q want %q", capturedVal, "world")
	}
}

func TestKVStoreAdapter_Put_Error(t *testing.T) {
	sentinel := errors.New("put failed")
	mock := &mockKVServicer{
		putFn: func(_ context.Context, _ string, _ []byte) error { return sentinel },
	}
	adapter := newKVStoreAdapter(mock, nil)

	if err := adapter.Put(context.Background(), "k", []byte("v")); !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Get
// ---------------------------------------------------------------------------

func TestKVStoreAdapter_Get_Success(t *testing.T) {
	mock := &mockKVServicer{
		getFn: func(_ context.Context, _ string) ([]byte, error) {
			return []byte("value"), nil
		},
	}
	adapter := newKVStoreAdapter(mock, nil)

	val, err := adapter.Get(context.Background(), "key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(val) != "value" {
		t.Errorf("value: got %q want %q", val, "value")
	}
}

func TestKVStoreAdapter_Get_KeyNotFound_ReturnsNilNil(t *testing.T) {
	mock := &mockKVServicer{
		getFn: func(_ context.Context, _ string) ([]byte, error) {
			return nil, &service.KeyNotFoundError{Key: "missing"}
		},
	}
	adapter := newKVStoreAdapter(mock, nil)

	val, err := adapter.Get(context.Background(), "missing")
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
	if val != nil {
		t.Errorf("expected nil value, got %v", val)
	}
}

func TestKVStoreAdapter_Get_OtherError(t *testing.T) {
	sentinel := errors.New("storage error")
	mock := &mockKVServicer{
		getFn: func(_ context.Context, _ string) ([]byte, error) {
			return nil, sentinel
		},
	}
	adapter := newKVStoreAdapter(mock, nil)

	_, err := adapter.Get(context.Background(), "key")
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

func TestKVStoreAdapter_Delete_Success(t *testing.T) {
	var capturedKey string
	mock := &mockKVServicer{
		deleteFn: func(_ context.Context, key string) error {
			capturedKey = key
			return nil
		},
	}
	adapter := newKVStoreAdapter(mock, nil)

	if err := adapter.Delete(context.Background(), "delkey"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if capturedKey != "delkey" {
		t.Errorf("key: got %q want %q", capturedKey, "delkey")
	}
}

func TestKVStoreAdapter_Delete_Error(t *testing.T) {
	sentinel := errors.New("delete failed")
	mock := &mockKVServicer{
		deleteFn: func(_ context.Context, _ string) error { return sentinel },
	}
	adapter := newKVStoreAdapter(mock, nil)

	if err := adapter.Delete(context.Background(), "k"); !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Scan
// ---------------------------------------------------------------------------

func TestKVStoreAdapter_Scan_Success(t *testing.T) {
	mock := &mockKVServicer{
		scanFn: func(_ context.Context, _ string) (map[string][]byte, error) {
			return map[string][]byte{"a": []byte("1"), "b": []byte("2")}, nil
		},
	}
	adapter := newKVStoreAdapter(mock, nil)

	collected := make(map[string]string)
	err := adapter.Scan(context.Background(), "pfx/", func(key string, value []byte) error {
		collected[key] = string(value)
		return nil
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(collected) != 2 {
		t.Errorf("expected 2 entries, got %d", len(collected))
	}
}

func TestKVStoreAdapter_Scan_ServiceError(t *testing.T) {
	sentinel := errors.New("scan failed")
	mock := &mockKVServicer{
		scanFn: func(_ context.Context, _ string) (map[string][]byte, error) {
			return nil, sentinel
		},
	}
	adapter := newKVStoreAdapter(mock, nil)

	err := adapter.Scan(context.Background(), "", func(_ string, _ []byte) error { return nil })
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got %v", err)
	}
}

func TestKVStoreAdapter_Scan_CallbackError(t *testing.T) {
	mock := &mockKVServicer{
		scanFn: func(_ context.Context, _ string) (map[string][]byte, error) {
			return map[string][]byte{"k": []byte("v")}, nil
		},
	}
	adapter := newKVStoreAdapter(mock, nil)

	sentinel := errors.New("callback abort")
	err := adapter.Scan(context.Background(), "", func(_ string, _ []byte) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

func TestKVStoreAdapter_List_Success(t *testing.T) {
	mock := &mockKVServicer{
		listFn: func(_ context.Context, _ string) ([]string, error) {
			return []string{"a", "b"}, nil
		},
	}
	adapter := newKVStoreAdapter(mock, nil)

	result, err := adapter.List(context.Background(), "ns/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("expected 2 keys, got %d", len(result))
	}
}

func TestKVStoreAdapter_List_Error(t *testing.T) {
	sentinel := errors.New("list failed")
	mock := &mockKVServicer{
		listFn: func(_ context.Context, _ string) ([]string, error) { return nil, sentinel },
	}
	adapter := newKVStoreAdapter(mock, nil)

	_, err := adapter.List(context.Background(), "ns/")
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Exists
// ---------------------------------------------------------------------------

func TestKVStoreAdapter_Exists_True(t *testing.T) {
	mock := &mockKVServicer{
		getFn: func(_ context.Context, _ string) ([]byte, error) {
			return []byte("data"), nil
		},
	}
	adapter := newKVStoreAdapter(mock, nil)

	ok, err := adapter.Exists(context.Background(), "key")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !ok {
		t.Error("expected true, got false")
	}
}

func TestKVStoreAdapter_Exists_KeyNotFound_ReturnsFalseNil(t *testing.T) {
	mock := &mockKVServicer{
		getFn: func(_ context.Context, _ string) ([]byte, error) {
			return nil, &service.KeyNotFoundError{Key: "absent"}
		},
	}
	adapter := newKVStoreAdapter(mock, nil)

	ok, err := adapter.Exists(context.Background(), "absent")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if ok {
		t.Error("expected false, got true")
	}
}

func TestKVStoreAdapter_Exists_OtherError(t *testing.T) {
	sentinel := errors.New("storage error")
	mock := &mockKVServicer{
		getFn: func(_ context.Context, _ string) ([]byte, error) { return nil, sentinel },
	}
	adapter := newKVStoreAdapter(mock, nil)

	_, err := adapter.Exists(context.Background(), "key")
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// kvSealBackend
// ---------------------------------------------------------------------------

// mockKVStore is a minimal kvstore.KVStore implementation for testing
// kvSealBackend. Only the Get method needs a meaningful body; the rest
// delegate to no-ops.
type mockKVStore struct {
	getFn func(ctx context.Context, key string) ([]byte, error)
}

func (m *mockKVStore) Put(_ context.Context, _ string, _ []byte) error { return nil }
func (m *mockKVStore) Delete(_ context.Context, _ string) error        { return nil }
func (m *mockKVStore) List(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}
func (m *mockKVStore) Scan(_ context.Context, _ string, _ func(string, []byte) error) error {
	return nil
}
func (m *mockKVStore) Exists(_ context.Context, _ string) (bool, error)              { return false, nil }
func (m *mockKVStore) RegisterEntityIndexes(_ string, _ []kvstore.EntityIndex) error { return nil }
func (m *mockKVStore) QueryIndex(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return nil, nil
}
func (m *mockKVStore) ScanIndex(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}
func (m *mockKVStore) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, nil
}
func (m *mockKVStore) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}
func (m *mockKVStore) Get(ctx context.Context, key string) ([]byte, error) {
	if m.getFn != nil {
		return m.getFn(ctx, key)
	}
	return nil, nil
}

var _ kvstore.KVStore = (*mockKVStore)(nil)

// TestKVSealBackend_Close_IsNoop verifies that Close on a kvSealBackend
// returns nil without touching the wrapped store.
func TestKVSealBackend_Close_IsNoop(t *testing.T) {
	b := kvSealBackend{KVStore: &mockKVStore{}}
	if err := b.Close(); err != nil {
		t.Errorf("Close: expected nil, got %v", err)
	}
}

// TestKVSealBackend_Get_ReturnsValue verifies that a non-nil response from
// the underlying store is passed through unchanged.
func TestKVSealBackend_Get_ReturnsValue(t *testing.T) {
	want := []byte("secret-data")
	b := kvSealBackend{KVStore: &mockKVStore{
		getFn: func(_ context.Context, _ string) ([]byte, error) { return want, nil },
	}}
	got, err := b.Get(context.Background(), "key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("Get: got %q, want %q", got, want)
	}
}

// TestKVSealBackend_Get_NilValue_ReturnsErrSecretNotFound verifies that a nil
// (missing-key) response from the underlying store is converted to
// seal.ErrSecretNotFound so StorageBackend consumers can distinguish "absent"
// from "empty value".
func TestKVSealBackend_Get_NilValue_ReturnsErrSecretNotFound(t *testing.T) {
	b := kvSealBackend{KVStore: &mockKVStore{
		getFn: func(_ context.Context, _ string) ([]byte, error) { return nil, nil },
	}}
	_, err := b.Get(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected seal.ErrSecretNotFound, got nil")
	}
}

// TestKVSealBackend_Get_StoreError_Propagates verifies that a storage error
// from the underlying KVStore is returned directly without wrapping.
func TestKVSealBackend_Get_StoreError_Propagates(t *testing.T) {
	sentinel := errors.New("storage failure")
	b := kvSealBackend{KVStore: &mockKVStore{
		getFn: func(_ context.Context, _ string) ([]byte, error) { return nil, sentinel },
	}}
	_, err := b.Get(context.Background(), "key")
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got %v", err)
	}
}

// mockTransactionServicer is a minimal service.TransactionServicer for
// testing kvSealBackend.PutIfAbsent. Only Execute carries behaviour; the
// remaining interface methods are unreachable from PutIfAbsent.
type mockTransactionServicer struct {
	executeFn func(ctx context.Context, compares []service.Compare, successOps, failureOps []service.Op) (*service.TransactionResponse, error)
}

func (m *mockTransactionServicer) Execute(ctx context.Context, compares []service.Compare, successOps, failureOps []service.Op) (*service.TransactionResponse, error) {
	return m.executeFn(ctx, compares, successOps, failureOps)
}

func (m *mockTransactionServicer) ExecuteWithMode(_ context.Context, _ []service.Compare, _, _ []service.Op, _ writemode.Mode) (*service.TransactionResponse, error) {
	return nil, nil
}

func (m *mockTransactionServicer) ExecuteEntity(_ context.Context, _ *service.TransactionEntityRequest) (*service.TransactionEntityResponse, error) {
	return nil, nil
}

var _ service.TransactionServicer = (*mockTransactionServicer)(nil)

// TestKVSealBackend_PutIfAbsent_NilTx verifies that a backend constructed
// without a transaction service rejects PutIfAbsent with
// ErrNoTransactionService instead of attempting a non-atomic write.
func TestKVSealBackend_PutIfAbsent_NilTx(t *testing.T) {
	b := kvSealBackend{KVStore: &mockKVStore{}}
	stored, err := b.PutIfAbsent(context.Background(), "key", []byte("v"))
	if !errors.Is(err, ErrNoTransactionService) {
		t.Fatalf("expected ErrNoTransactionService, got %v", err)
	}
	if stored {
		t.Error("stored must be false when the transaction service is missing")
	}
}

// TestKVSealBackend_PutIfAbsent_FirstWriter verifies the success path: the
// version==0 precondition passes, the Put executes, and the caller observes
// stored=true. The compare and op shapes are asserted so a regression in the
// absent-key precondition (version 0) is caught here.
func TestKVSealBackend_PutIfAbsent_FirstWriter(t *testing.T) {
	tx := &mockTransactionServicer{
		executeFn: func(_ context.Context, compares []service.Compare, successOps, failureOps []service.Op) (*service.TransactionResponse, error) {
			if len(compares) != 1 || compares[0].Target != service.CompareVersion || compares[0].Version != 0 {
				t.Errorf("expected single version==0 compare, got %+v", compares)
			}
			if string(compares[0].Key) != "key" {
				t.Errorf("compare key: got %q, want %q", compares[0].Key, "key")
			}
			if len(successOps) != 1 || successOps[0].Type != service.OpPut || string(successOps[0].Value) != "v" {
				t.Errorf("expected single Put success op, got %+v", successOps)
			}
			if failureOps != nil {
				t.Errorf("expected nil failure ops, got %+v", failureOps)
			}
			return &service.TransactionResponse{Succeeded: true}, nil
		},
	}
	b := kvSealBackend{KVStore: &mockKVStore{}, tx: tx}
	stored, err := b.PutIfAbsent(context.Background(), "key", []byte("v"))
	if err != nil {
		t.Fatalf("PutIfAbsent: %v", err)
	}
	if !stored {
		t.Error("expected stored=true for the first writer")
	}
}

// TestKVSealBackend_PutIfAbsent_KeyExists verifies that a failed compare
// (key already written) yields stored=false with no error.
func TestKVSealBackend_PutIfAbsent_KeyExists(t *testing.T) {
	tx := &mockTransactionServicer{
		executeFn: func(_ context.Context, _ []service.Compare, _, _ []service.Op) (*service.TransactionResponse, error) {
			return &service.TransactionResponse{Succeeded: false}, nil
		},
	}
	b := kvSealBackend{KVStore: &mockKVStore{}, tx: tx}
	stored, err := b.PutIfAbsent(context.Background(), "key", []byte("v"))
	if err != nil {
		t.Fatalf("PutIfAbsent: %v", err)
	}
	if stored {
		t.Error("expected stored=false when the key already exists")
	}
}

// TestKVSealBackend_PutIfAbsent_ExecuteError verifies that a transaction
// failure propagates and reports stored=false.
func TestKVSealBackend_PutIfAbsent_ExecuteError(t *testing.T) {
	sentinel := errors.New("raft unavailable")
	tx := &mockTransactionServicer{
		executeFn: func(_ context.Context, _ []service.Compare, _, _ []service.Op) (*service.TransactionResponse, error) {
			return nil, sentinel
		},
	}
	b := kvSealBackend{KVStore: &mockKVStore{}, tx: tx}
	stored, err := b.PutIfAbsent(context.Background(), "key", []byte("v"))
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
	if stored {
		t.Error("stored must be false on transaction error")
	}
}

// ---------------------------------------------------------------------------
// resolveReplicaID
// ---------------------------------------------------------------------------

// TestResolveReplicaID_ZeroMapsToOne verifies that a zero ReplicaID (the
// standalone default not yet materialised) is mapped to node 1, matching the
// qrdb standalone Snowflake convention.
func TestResolveReplicaID_ZeroMapsToOne(t *testing.T) {
	if got := resolveReplicaID(0); got != 1 {
		t.Errorf("resolveReplicaID(0): got %d, want 1", got)
	}
}

// TestResolveReplicaID_NonZeroPassesThrough verifies that non-zero replica
// IDs are returned unchanged.
func TestResolveReplicaID_NonZeroPassesThrough(t *testing.T) {
	for _, id := range []uint64{1, 42, 1023} {
		if got := resolveReplicaID(id); got != id {
			t.Errorf("resolveReplicaID(%d): got %d, want %d", id, got, id)
		}
	}
}

// ---------------------------------------------------------------------------
// The five TestKVStoreAdapter_*_NoOp tests that used to live here asserted
// that every index method returned (nil, nil). They passed, and that is
// precisely why the defect survived: a green test declared "answers every
// index query with an empty result and no error" to be the intended
// contract. They are replaced by kv_adapter_index_test.go, which asserts
// the engine-backed answers and the typed refusal.
// ---------------------------------------------------------------------------
