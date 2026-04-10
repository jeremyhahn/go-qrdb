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

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// newTestAdapter creates a BackendStorageAdapter backed by in-memory storage
// for use in tests.
func newTestAdapter(t *testing.T) (*BackendStorageAdapter, *StandaloneBackend) {
	t.Helper()
	store := memory.NewStorage()
	t.Cleanup(func() { _ = store.Close() })
	inner := NewStandaloneBackend(store)
	return NewBackendStorageAdapter(inner), inner
}

func TestBackendStorageAdapter_InterfaceCompliance(t *testing.T) {
	var _ seal.StorageBackend = (*BackendStorageAdapter)(nil)
}

func TestBackendStorageAdapter_PutGet(t *testing.T) {
	a, _ := newTestAdapter(t)
	ctx := context.Background()

	if err := a.Put(ctx, "greeting", []byte("hello")); err != nil {
		t.Fatalf("Put: unexpected error: %v", err)
	}

	got, err := a.Get(ctx, "greeting")
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("Get: got %q, want %q", got, "hello")
	}
}

func TestBackendStorageAdapter_Get_NonExistent(t *testing.T) {
	a, _ := newTestAdapter(t)
	ctx := context.Background()

	got, err := a.Get(ctx, "missing")
	if err != nil {
		t.Fatalf("Get non-existent: unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("Get non-existent: got %q, want nil", got)
	}
}

func TestBackendStorageAdapter_Delete(t *testing.T) {
	a, _ := newTestAdapter(t)
	ctx := context.Background()

	if err := a.Put(ctx, "todelete", []byte("val")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := a.Delete(ctx, "todelete"); err != nil {
		t.Fatalf("Delete: unexpected error: %v", err)
	}

	got, err := a.Get(ctx, "todelete")
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if got != nil {
		t.Errorf("Get after delete: got %q, want nil", got)
	}
}

func TestBackendStorageAdapter_List(t *testing.T) {
	a, _ := newTestAdapter(t)
	ctx := context.Background()

	keys := []string{"ns/a", "ns/b", "other/c"}
	for _, k := range keys {
		if err := a.Put(ctx, k, []byte("v")); err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
	}

	got, err := a.List(ctx, "ns/")
	if err != nil {
		t.Fatalf("List: unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("List: got %d keys, want 2", len(got))
	}
}

func TestBackendStorageAdapter_List_Empty(t *testing.T) {
	a, _ := newTestAdapter(t)
	ctx := context.Background()

	got, err := a.List(ctx, "nonexistent/")
	if err != nil {
		t.Fatalf("List empty: unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("List empty: got %d keys, want 0", len(got))
	}
}

func TestBackendStorageAdapter_Scan(t *testing.T) {
	a, _ := newTestAdapter(t)
	ctx := context.Background()

	data := map[string]string{
		"scan/x": "xval",
		"scan/y": "yval",
		"other":  "zval",
	}
	for k, v := range data {
		if err := a.Put(ctx, k, []byte(v)); err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
	}

	got := make(map[string][]byte)
	if err := a.Scan(ctx, "scan/", func(key string, value []byte) error {
		got[key] = value
		return nil
	}); err != nil {
		t.Fatalf("Scan: unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Scan: got %d entries, want 2", len(got))
	}
	if string(got["scan/x"]) != "xval" {
		t.Errorf("Scan[scan/x] = %q, want %q", got["scan/x"], "xval")
	}
	if string(got["scan/y"]) != "yval" {
		t.Errorf("Scan[scan/y] = %q, want %q", got["scan/y"], "yval")
	}
}

func TestBackendStorageAdapter_Scan_Empty(t *testing.T) {
	a, _ := newTestAdapter(t)
	ctx := context.Background()

	got := make(map[string][]byte)
	if err := a.Scan(ctx, "empty/", func(key string, value []byte) error {
		got[key] = value
		return nil
	}); err != nil {
		t.Fatalf("Scan empty: unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Scan empty: got %d entries, want 0", len(got))
	}
}

func TestBackendStorageAdapter_Exists(t *testing.T) {
	a, _ := newTestAdapter(t)
	ctx := context.Background()

	if err := a.Put(ctx, "exists-key", []byte("val")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	exists, err := a.Exists(ctx, "exists-key")
	if err != nil {
		t.Fatalf("Exists: unexpected error: %v", err)
	}
	if !exists {
		t.Error("Exists: got false, want true")
	}
}

func TestBackendStorageAdapter_Exists_Missing(t *testing.T) {
	a, _ := newTestAdapter(t)
	ctx := context.Background()

	exists, err := a.Exists(ctx, "no-such-key")
	if err != nil {
		t.Fatalf("Exists missing: unexpected error: %v", err)
	}
	if exists {
		t.Error("Exists missing: got true, want false")
	}
}

func TestBackendStorageAdapter_Close(t *testing.T) {
	a, _ := newTestAdapter(t)
	if err := a.Close(); err != nil {
		t.Fatalf("Close: unexpected error: %v", err)
	}
}

func TestBackendStorageAdapter_Scan_ListError(t *testing.T) {
	// Use a mock inner backend that returns an error from List.
	listErr := &NotFoundError{Name: "scan-list-fail"}
	mock := &mockStorageAdapterBackend{
		listErr: listErr,
	}
	a := NewBackendStorageAdapter(mock)
	ctx := context.Background()

	err := a.Scan(ctx, "prefix/", func(_ string, _ []byte) error { return nil })
	if err == nil {
		t.Fatal("Scan: expected error from List, got nil")
	}
	if err != listErr {
		t.Errorf("Scan: got error %v, want %v", err, listErr)
	}
}

func TestBackendStorageAdapter_Exists_GetError(t *testing.T) {
	// Use a mock inner backend that returns an error from Get.
	getErr := &NotFoundError{Name: "exists-get-fail"}
	mock := &mockStorageAdapterBackend{
		getErr: getErr,
	}
	a := NewBackendStorageAdapter(mock)
	ctx := context.Background()

	exists, err := a.Exists(ctx, "some-key")
	if err == nil {
		t.Fatal("Exists: expected error from Get, got nil")
	}
	if err != getErr {
		t.Errorf("Exists: got error %v, want %v", err, getErr)
	}
	if exists {
		t.Error("Exists: got true, want false on error")
	}
}

// mockStorageAdapterBackend is a minimal Backend mock for adapter error tests.
type mockStorageAdapterBackend struct {
	getVal  []byte
	getErr  error
	listVal []string
	listErr error
}

func (m *mockStorageAdapterBackend) Get(_ context.Context, _ []byte) ([]byte, error) {
	return m.getVal, m.getErr
}
func (m *mockStorageAdapterBackend) Put(_ context.Context, _, _ []byte) error { return nil }
func (m *mockStorageAdapterBackend) Delete(_ context.Context, _ []byte) error { return nil }
func (m *mockStorageAdapterBackend) List(_ context.Context, _ string) ([]string, error) {
	return m.listVal, m.listErr
}
func (m *mockStorageAdapterBackend) Range(_ context.Context, _, _ []byte, _ int) ([]KeyValue, error) {
	return nil, nil
}
func (m *mockStorageAdapterBackend) Batch(_ context.Context, _ []BatchOp) error { return nil }
func (m *mockStorageAdapterBackend) Close() error                               { return nil }

func TestBackendStorageAdapter_KeyConversion(t *testing.T) {
	a, inner := newTestAdapter(t)
	ctx := context.Background()

	// Write through adapter with string key, verify inner backend
	// received it as []byte.
	if err := a.Put(ctx, "strkey", []byte("data")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Read from inner backend using []byte key.
	got, err := inner.Get(ctx, []byte("strkey"))
	if err != nil {
		t.Fatalf("inner.Get: %v", err)
	}
	if string(got) != "data" {
		t.Errorf("inner.Get: got %q, want %q", got, "data")
	}
}
