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
	putFn    func(ctx context.Context, key string, value []byte) error
	getFn    func(ctx context.Context, key string) ([]byte, error)
	deleteFn func(ctx context.Context, key string) error
	scanFn   func(ctx context.Context, prefix string) (map[string][]byte, error)
	listFn   func(ctx context.Context, prefix string) ([]string, error)
}

func (m *mockKVServicer) Put(ctx context.Context, key string, value []byte) error {
	if m.putFn != nil {
		return m.putFn(ctx, key, value)
	}
	return nil
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
	adapter := newKVStoreAdapter(mock)

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
	adapter := newKVStoreAdapter(mock)

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
	adapter := newKVStoreAdapter(mock)

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
	adapter := newKVStoreAdapter(mock)

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
	adapter := newKVStoreAdapter(mock)

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
	adapter := newKVStoreAdapter(mock)

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
	adapter := newKVStoreAdapter(mock)

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
	adapter := newKVStoreAdapter(mock)

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
	adapter := newKVStoreAdapter(mock)

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
	adapter := newKVStoreAdapter(mock)

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
	adapter := newKVStoreAdapter(mock)

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
	adapter := newKVStoreAdapter(mock)

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
	adapter := newKVStoreAdapter(mock)

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
	adapter := newKVStoreAdapter(mock)

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
	adapter := newKVStoreAdapter(mock)

	_, err := adapter.Exists(context.Background(), "key")
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// No-op index methods
// ---------------------------------------------------------------------------

func TestKVStoreAdapter_RegisterEntityIndexes_NoOp(t *testing.T) {
	adapter := newKVStoreAdapter(&mockKVServicer{})
	if err := adapter.RegisterEntityIndexes("entity", []kvstore.EntityIndex{}); err != nil {
		t.Errorf("RegisterEntityIndexes: %v", err)
	}
}

func TestKVStoreAdapter_QueryIndex_NoOp(t *testing.T) {
	adapter := newKVStoreAdapter(&mockKVServicer{})
	result, err := adapter.QueryIndex(context.Background(), "idx", []byte("val"))
	if err != nil {
		t.Errorf("QueryIndex: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil result, got %v", result)
	}
}

func TestKVStoreAdapter_ScanIndex_NoOp(t *testing.T) {
	adapter := newKVStoreAdapter(&mockKVServicer{})
	result, err := adapter.ScanIndex(context.Background(), "idx", []byte("start"), []byte("end"))
	if err != nil {
		t.Errorf("ScanIndex: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil result, got %v", result)
	}
}

func TestKVStoreAdapter_QueryCompoundIndex_NoOp(t *testing.T) {
	adapter := newKVStoreAdapter(&mockKVServicer{})
	result, err := adapter.QueryCompoundIndex(context.Background(), "idx", [][]byte{[]byte("v1")})
	if err != nil {
		t.Errorf("QueryCompoundIndex: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil result, got %v", result)
	}
}

func TestKVStoreAdapter_ScanCompoundIndex_NoOp(t *testing.T) {
	adapter := newKVStoreAdapter(&mockKVServicer{})
	result, err := adapter.ScanCompoundIndex(context.Background(), "idx", [][]byte{[]byte("v1")}, []byte("s"), []byte("e"))
	if err != nil {
		t.Errorf("ScanCompoundIndex: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil result, got %v", result)
	}
}
