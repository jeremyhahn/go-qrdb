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

package transport

import (
	"context"
	"errors"
	"testing"
)

// mockKVClient is a minimal KVClient implementation used to verify
// the interface contract at compile time and in unit tests.
type mockKVClient struct {
	store map[string][]byte
}

func (m *mockKVClient) Put(_ context.Context, key string, value []byte, _ ...WriteOption) error {
	m.store[key] = append([]byte(nil), value...)
	return nil
}

func (m *mockKVClient) Get(_ context.Context, key string, _ ...ReadOption) ([]byte, error) {
	v, ok := m.store[key]
	if !ok {
		return nil, &KeyNotFoundError{Key: key}
	}
	return v, nil
}

func (m *mockKVClient) Delete(_ context.Context, key string, _ ...WriteOption) error {
	delete(m.store, key)
	return nil
}

func (m *mockKVClient) List(_ context.Context, _ string, _ ...ReadOption) ([]string, error) {
	keys := make([]string, 0, len(m.store))
	for k := range m.store {
		keys = append(keys, k)
	}
	return keys, nil
}

func (m *mockKVClient) Scan(_ context.Context, _ string, _ ...ReadOption) (map[string][]byte, error) {
	return m.store, nil
}

func (m *mockKVClient) Exists(ctx context.Context, key string, opts ...ReadOption) (bool, error) {
	_, err := m.Get(ctx, key, opts...)
	if err != nil {
		var knf *KeyNotFoundError
		if errors.As(err, &knf) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Compile-time assertion: mockKVClient satisfies KVClient.
var _ KVClient = (*mockKVClient)(nil)

func TestKVClient_Exists_InterfaceSatisfaction(t *testing.T) {
	var client KVClient = &mockKVClient{store: make(map[string][]byte)}
	ctx := context.Background()

	// Exists on missing key returns false, nil.
	exists, err := client.Exists(ctx, "no-key")
	if err != nil {
		t.Fatalf("Exists returned unexpected error: %v", err)
	}
	if exists {
		t.Error("expected false for missing key")
	}

	// Put a key and verify Exists returns true.
	if err := client.Put(ctx, "test-key", []byte("val")); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	exists, err = client.Exists(ctx, "test-key")
	if err != nil {
		t.Fatalf("Exists returned unexpected error: %v", err)
	}
	if !exists {
		t.Error("expected true for present key")
	}
}

func TestKVClient_Exists_AfterDelete(t *testing.T) {
	var client KVClient = &mockKVClient{store: make(map[string][]byte)}
	ctx := context.Background()

	if err := client.Put(ctx, "del-key", []byte("val")); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	if err := client.Delete(ctx, "del-key"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	exists, err := client.Exists(ctx, "del-key")
	if err != nil {
		t.Fatalf("Exists returned unexpected error: %v", err)
	}
	if exists {
		t.Error("expected false after delete")
	}
}
