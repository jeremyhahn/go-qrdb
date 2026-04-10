// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

// mockLegacyStorage implements LegacyStorage for testing the StorageAdapter.
type mockLegacyStorage struct {
	data     map[string][]byte
	getErr   error
	setErr   error
	delErr   error
	listErr  error
	existErr error
	closeErr error
}

func newMockLegacyStorage() *mockLegacyStorage {
	return &mockLegacyStorage{data: make(map[string][]byte)}
}

func (m *mockLegacyStorage) Get(key []byte) ([]byte, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	v, ok := m.data[string(key)]
	if !ok {
		return nil, errors.New("not found")
	}
	return v, nil
}

func (m *mockLegacyStorage) Set(key, value []byte) error {
	if m.setErr != nil {
		return m.setErr
	}
	m.data[string(key)] = append([]byte(nil), value...)
	return nil
}

func (m *mockLegacyStorage) Delete(key []byte) error {
	if m.delErr != nil {
		return m.delErr
	}
	delete(m.data, string(key))
	return nil
}

func (m *mockLegacyStorage) List(prefix []byte) ([][]byte, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	var result [][]byte
	p := string(prefix)
	for k := range m.data {
		if len(k) >= len(p) && k[:len(p)] == p {
			result = append(result, []byte(k))
		}
	}
	return result, nil
}

func (m *mockLegacyStorage) Exists(key []byte) (bool, error) {
	if m.existErr != nil {
		return false, m.existErr
	}
	_, ok := m.data[string(key)]
	return ok, nil
}

func (m *mockLegacyStorage) Close() error {
	return m.closeErr
}

func TestStorageAdapter_GetPutDeleteRoundTrip(t *testing.T) {
	inner := newMockLegacyStorage()
	adapter := NewStorageAdapter(inner)
	ctx := context.Background()

	// Put a value.
	if err := adapter.Put(ctx, "key1", []byte("value1")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Get the value back.
	val, err := adapter.Get(ctx, "key1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(val, []byte("value1")) {
		t.Errorf("Get: expected %q, got %q", "value1", val)
	}

	// Delete the value.
	if err := adapter.Delete(ctx, "key1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Get should fail after delete.
	_, err = adapter.Get(ctx, "key1")
	if err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestStorageAdapter_List(t *testing.T) {
	inner := newMockLegacyStorage()
	adapter := NewStorageAdapter(inner)
	ctx := context.Background()

	inner.data["prefix/a"] = []byte("1")
	inner.data["prefix/b"] = []byte("2")
	inner.data["other/c"] = []byte("3")

	keys, err := adapter.List(ctx, "prefix/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 2 {
		t.Errorf("List: expected 2 keys, got %d", len(keys))
	}
}

func TestStorageAdapter_ListError(t *testing.T) {
	inner := newMockLegacyStorage()
	inner.listErr = errors.New("list failure")
	adapter := NewStorageAdapter(inner)
	ctx := context.Background()

	_, err := adapter.List(ctx, "prefix/")
	if err == nil {
		t.Fatal("expected error from List")
	}
}

func TestStorageAdapter_Scan(t *testing.T) {
	inner := newMockLegacyStorage()
	adapter := NewStorageAdapter(inner)
	ctx := context.Background()

	inner.data["scan/a"] = []byte("val-a")
	inner.data["scan/b"] = []byte("val-b")
	inner.data["other/c"] = []byte("val-c")

	result := make(map[string][]byte)
	if err := adapter.Scan(ctx, "scan/", func(key string, value []byte) error {
		result[key] = value
		return nil
	}); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("Scan: expected 2 entries, got %d", len(result))
	}
	if !bytes.Equal(result["scan/a"], []byte("val-a")) {
		t.Errorf("Scan: expected val-a, got %q", result["scan/a"])
	}
}

func TestStorageAdapter_ScanListError(t *testing.T) {
	inner := newMockLegacyStorage()
	inner.listErr = errors.New("list failure")
	adapter := NewStorageAdapter(inner)
	ctx := context.Background()

	err := adapter.Scan(ctx, "prefix/", func(_ string, _ []byte) error { return nil })
	if err == nil {
		t.Fatal("expected error from Scan when List fails")
	}
}

func TestStorageAdapter_ScanGetError(t *testing.T) {
	inner := newMockLegacyStorage()
	inner.data["scan/a"] = []byte("val")
	adapter := NewStorageAdapter(inner)
	ctx := context.Background()

	// Inject Get error after List succeeds.
	inner.getErr = errors.New("get failure")

	err := adapter.Scan(ctx, "scan/", func(_ string, _ []byte) error { return nil })
	if err == nil {
		t.Fatal("expected error from Scan when Get fails")
	}
}

func TestStorageAdapter_Exists(t *testing.T) {
	inner := newMockLegacyStorage()
	adapter := NewStorageAdapter(inner)
	ctx := context.Background()

	inner.data["existing-key"] = []byte("data")

	exists, err := adapter.Exists(ctx, "existing-key")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Error("Exists: expected true for existing key")
	}

	exists, err = adapter.Exists(ctx, "missing-key")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if exists {
		t.Error("Exists: expected false for missing key")
	}
}

func TestStorageAdapter_ExistsError(t *testing.T) {
	inner := newMockLegacyStorage()
	inner.existErr = errors.New("exists failure")
	adapter := NewStorageAdapter(inner)
	ctx := context.Background()

	_, err := adapter.Exists(ctx, "key")
	if err == nil {
		t.Fatal("expected error from Exists")
	}
}

func TestStorageAdapter_Close(t *testing.T) {
	inner := newMockLegacyStorage()
	adapter := NewStorageAdapter(inner)

	if err := adapter.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestStorageAdapter_CloseError(t *testing.T) {
	inner := newMockLegacyStorage()
	inner.closeErr = errors.New("close failure")
	adapter := NewStorageAdapter(inner)

	err := adapter.Close()
	if err == nil {
		t.Fatal("expected error from Close")
	}
}
