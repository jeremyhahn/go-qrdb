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
	"sort"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// In-memory mock KVClient for testing (avoids circular imports)
// ---------------------------------------------------------------------------

type mockKV struct {
	data map[string][]byte
}

func newMockKV() *mockKV {
	return &mockKV{data: make(map[string][]byte)}
}

func (m *mockKV) Put(_ context.Context, key string, value []byte, _ ...WriteOption) error {
	m.data[key] = append([]byte(nil), value...)
	return nil
}

func (m *mockKV) Get(_ context.Context, key string, _ ...ReadOption) ([]byte, error) {
	v, ok := m.data[key]
	if !ok {
		return nil, &KeyNotFoundError{Key: key}
	}
	return v, nil
}

func (m *mockKV) Delete(_ context.Context, key string, _ ...WriteOption) error {
	delete(m.data, key)
	return nil
}

func (m *mockKV) List(_ context.Context, prefix string, _ ...ReadOption) ([]string, error) {
	var keys []string
	for k := range m.data {
		if prefix == "" || strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func (m *mockKV) Scan(_ context.Context, prefix string, _ ...ReadOption) (map[string][]byte, error) {
	result := make(map[string][]byte)
	for k, v := range m.data {
		if prefix == "" || strings.HasPrefix(k, prefix) {
			result[k] = v
		}
	}
	return result, nil
}

func (m *mockKV) Exists(_ context.Context, key string, _ ...ReadOption) (bool, error) {
	_, ok := m.data[key]
	return ok, nil
}

// failingKV returns errors for all operations.
type failingKV struct {
	err error
}

func (f *failingKV) Put(_ context.Context, _ string, _ []byte, _ ...WriteOption) error {
	return f.err
}

func (f *failingKV) Get(_ context.Context, _ string, _ ...ReadOption) ([]byte, error) {
	return nil, f.err
}

func (f *failingKV) Delete(_ context.Context, _ string, _ ...WriteOption) error {
	return f.err
}

func (f *failingKV) List(_ context.Context, _ string, _ ...ReadOption) ([]string, error) {
	return nil, f.err
}

func (f *failingKV) Scan(_ context.Context, _ string, _ ...ReadOption) (map[string][]byte, error) {
	return nil, f.err
}

func (f *failingKV) Exists(_ context.Context, _ string, _ ...ReadOption) (bool, error) {
	return false, f.err
}

// ---------------------------------------------------------------------------
// WithTenant constructor tests
// ---------------------------------------------------------------------------

func TestWithTenant_Success(t *testing.T) {
	kv := newMockKV()
	tc, err := WithTenant(kv, "acme")
	if err != nil {
		t.Fatalf("WithTenant failed: %v", err)
	}
	if tc == nil {
		t.Fatal("expected non-nil TenantKVClient")
	}
	if tc.TenantID() != "acme" {
		t.Errorf("expected tenantID 'acme', got %q", tc.TenantID())
	}
}

func TestWithTenant_EmptyTenantID(t *testing.T) {
	kv := newMockKV()
	_, err := WithTenant(kv, "")
	if err == nil {
		t.Fatal("expected error for empty tenantID")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("expected ConfigError, got %T", err)
	}
	if cfgErr.Field != "tenantID" {
		t.Errorf("expected field 'tenantID', got %q", cfgErr.Field)
	}
	if !strings.Contains(cfgErr.Reason, "empty") {
		t.Errorf("expected reason to mention 'empty', got %q", cfgErr.Reason)
	}
}

func TestWithTenant_ForwardSlash(t *testing.T) {
	kv := newMockKV()
	_, err := WithTenant(kv, "acme/corp")
	if err == nil {
		t.Fatal("expected error for tenantID with forward slash")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("expected ConfigError, got %T", err)
	}
	if !strings.Contains(cfgErr.Reason, "path separator") {
		t.Errorf("expected reason about path separators, got %q", cfgErr.Reason)
	}
}

func TestWithTenant_BackSlash(t *testing.T) {
	kv := newMockKV()
	_, err := WithTenant(kv, "acme\\corp")
	if err == nil {
		t.Fatal("expected error for tenantID with backslash")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("expected ConfigError, got %T", err)
	}
}

func TestWithTenant_DotDot(t *testing.T) {
	kv := newMockKV()
	_, err := WithTenant(kv, "acme..corp")
	if err == nil {
		t.Fatal("expected error for tenantID with '..'")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("expected ConfigError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// TenantID accessor
// ---------------------------------------------------------------------------

func TestTenantKVClient_TenantID(t *testing.T) {
	kv := newMockKV()
	tc, err := WithTenant(kv, "tenant-42")
	if err != nil {
		t.Fatal(err)
	}
	if tc.TenantID() != "tenant-42" {
		t.Errorf("expected 'tenant-42', got %q", tc.TenantID())
	}
}

// ---------------------------------------------------------------------------
// Put tests
// ---------------------------------------------------------------------------

func TestTenantKVClient_Put_PrefixesKey(t *testing.T) {
	kv := newMockKV()
	tc, _ := WithTenant(kv, "acme")
	ctx := context.Background()

	if err := tc.Put(ctx, "config", []byte("data")); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Verify the underlying store has the prefixed key.
	if _, ok := kv.data["acme/config"]; !ok {
		t.Error("expected key 'acme/config' in underlying store")
	}
	// Verify the un-prefixed key does NOT exist.
	if _, ok := kv.data["config"]; ok {
		t.Error("unexpected un-prefixed key 'config' in underlying store")
	}
}

func TestTenantKVClient_Put_InnerError(t *testing.T) {
	innerErr := &RequestError{Method: "Put", Err: errors.New("disk full")}
	tc, _ := WithTenant(&failingKV{err: innerErr}, "acme")

	err := tc.Put(context.Background(), "key", []byte("val"))
	if err == nil {
		t.Fatal("expected error from inner Put")
	}
	var reqErr *RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// Get tests
// ---------------------------------------------------------------------------

func TestTenantKVClient_Get_PrefixesKey(t *testing.T) {
	kv := newMockKV()
	tc, _ := WithTenant(kv, "acme")
	ctx := context.Background()

	// Seed the underlying store directly with a prefixed key.
	kv.data["acme/settings"] = []byte("value123")

	val, err := tc.Get(ctx, "settings")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if string(val) != "value123" {
		t.Errorf("expected 'value123', got %q", string(val))
	}
}

func TestTenantKVClient_Get_NotFound(t *testing.T) {
	kv := newMockKV()
	tc, _ := WithTenant(kv, "acme")

	_, err := tc.Get(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing key")
	}
	var knf *KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Errorf("expected KeyNotFoundError, got %T", err)
	}
	// The error key should be the prefixed key.
	if knf.Key != "acme/nonexistent" {
		t.Errorf("expected key 'acme/nonexistent', got %q", knf.Key)
	}
}

func TestTenantKVClient_Get_InnerError(t *testing.T) {
	innerErr := &RequestError{Method: "Get", Err: errors.New("timeout")}
	tc, _ := WithTenant(&failingKV{err: innerErr}, "acme")

	_, err := tc.Get(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error from inner Get")
	}
	var reqErr *RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// Delete tests
// ---------------------------------------------------------------------------

func TestTenantKVClient_Delete_PrefixesKey(t *testing.T) {
	kv := newMockKV()
	tc, _ := WithTenant(kv, "acme")
	ctx := context.Background()

	kv.data["acme/temp"] = []byte("to-be-deleted")

	if err := tc.Delete(ctx, "temp"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if _, ok := kv.data["acme/temp"]; ok {
		t.Error("expected key 'acme/temp' to be deleted")
	}
}

func TestTenantKVClient_Delete_InnerError(t *testing.T) {
	innerErr := &RequestError{Method: "Delete", Err: errors.New("permission denied")}
	tc, _ := WithTenant(&failingKV{err: innerErr}, "acme")

	err := tc.Delete(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error from inner Delete")
	}
}

// ---------------------------------------------------------------------------
// List tests
// ---------------------------------------------------------------------------

func TestTenantKVClient_List_StripsPrefix(t *testing.T) {
	kv := newMockKV()
	tc, _ := WithTenant(kv, "acme")
	ctx := context.Background()

	// Seed underlying store with prefixed keys.
	kv.data["acme/users/alice"] = []byte("1")
	kv.data["acme/users/bob"] = []byte("2")
	kv.data["acme/config"] = []byte("3")
	kv.data["other/users/charlie"] = []byte("4") // different tenant

	keys, err := tc.List(ctx, "users/")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d: %v", len(keys), keys)
	}
	// Keys should be sorted and have tenant prefix stripped.
	if keys[0] != "users/alice" {
		t.Errorf("expected 'users/alice', got %q", keys[0])
	}
	if keys[1] != "users/bob" {
		t.Errorf("expected 'users/bob', got %q", keys[1])
	}
}

func TestTenantKVClient_List_EmptyPrefix(t *testing.T) {
	kv := newMockKV()
	tc, _ := WithTenant(kv, "acme")
	ctx := context.Background()

	kv.data["acme/a"] = []byte("1")
	kv.data["acme/b"] = []byte("2")

	keys, err := tc.List(ctx, "")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(keys) != 2 {
		t.Errorf("expected 2 keys, got %d", len(keys))
	}
}

func TestTenantKVClient_List_NoMatches(t *testing.T) {
	kv := newMockKV()
	tc, _ := WithTenant(kv, "acme")
	ctx := context.Background()

	kv.data["other/key"] = []byte("val")

	keys, err := tc.List(ctx, "")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("expected 0 keys, got %d", len(keys))
	}
}

func TestTenantKVClient_List_InnerError(t *testing.T) {
	innerErr := &RequestError{Method: "List", Err: errors.New("unavailable")}
	tc, _ := WithTenant(&failingKV{err: innerErr}, "acme")

	_, err := tc.List(context.Background(), "")
	if err == nil {
		t.Fatal("expected error from inner List")
	}
}

// ---------------------------------------------------------------------------
// Scan tests
// ---------------------------------------------------------------------------

func TestTenantKVClient_Scan_StripsPrefix(t *testing.T) {
	kv := newMockKV()
	tc, _ := WithTenant(kv, "acme")
	ctx := context.Background()

	kv.data["acme/cfg/db"] = []byte("postgres")
	kv.data["acme/cfg/cache"] = []byte("redis")
	kv.data["acme/data"] = []byte("other")
	kv.data["other/cfg/x"] = []byte("ignored")

	pairs, err := tc.Scan(ctx, "cfg/")
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(pairs) != 2 {
		t.Fatalf("expected 2 pairs, got %d", len(pairs))
	}
	if string(pairs["cfg/db"]) != "postgres" {
		t.Errorf("expected 'postgres' for 'cfg/db', got %q", string(pairs["cfg/db"]))
	}
	if string(pairs["cfg/cache"]) != "redis" {
		t.Errorf("expected 'redis' for 'cfg/cache', got %q", string(pairs["cfg/cache"]))
	}
}

func TestTenantKVClient_Scan_EmptyPrefix(t *testing.T) {
	kv := newMockKV()
	tc, _ := WithTenant(kv, "acme")
	ctx := context.Background()

	kv.data["acme/x"] = []byte("1")
	kv.data["acme/y"] = []byte("2")

	pairs, err := tc.Scan(ctx, "")
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(pairs) != 2 {
		t.Errorf("expected 2 pairs, got %d", len(pairs))
	}
}

func TestTenantKVClient_Scan_NoMatches(t *testing.T) {
	kv := newMockKV()
	tc, _ := WithTenant(kv, "acme")
	ctx := context.Background()

	kv.data["other/key"] = []byte("val")

	pairs, err := tc.Scan(ctx, "")
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(pairs) != 0 {
		t.Errorf("expected 0 pairs, got %d", len(pairs))
	}
}

func TestTenantKVClient_Scan_InnerError(t *testing.T) {
	innerErr := &RequestError{Method: "Scan", Err: errors.New("unavailable")}
	tc, _ := WithTenant(&failingKV{err: innerErr}, "acme")

	_, err := tc.Scan(context.Background(), "")
	if err == nil {
		t.Fatal("expected error from inner Scan")
	}
}

// ---------------------------------------------------------------------------
// Exists tests
// ---------------------------------------------------------------------------

func TestTenantKVClient_Exists_Found(t *testing.T) {
	kv := newMockKV()
	tc, _ := WithTenant(kv, "acme")
	ctx := context.Background()

	kv.data["acme/present"] = []byte("val")

	exists, err := tc.Exists(ctx, "present")
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}
	if !exists {
		t.Error("expected key to exist")
	}
}

func TestTenantKVClient_Exists_NotFound(t *testing.T) {
	kv := newMockKV()
	tc, _ := WithTenant(kv, "acme")
	ctx := context.Background()

	exists, err := tc.Exists(ctx, "missing")
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}
	if exists {
		t.Error("expected key to not exist")
	}
}

func TestTenantKVClient_Exists_Isolation(t *testing.T) {
	kv := newMockKV()
	ctx := context.Background()

	tc1, _ := WithTenant(kv, "tenant1")
	tc2, _ := WithTenant(kv, "tenant2")

	// Only tenant1 has the key.
	kv.data["tenant1/secret"] = []byte("data")

	exists1, err := tc1.Exists(ctx, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if !exists1 {
		t.Error("tenant1 should see the key")
	}

	exists2, err := tc2.Exists(ctx, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if exists2 {
		t.Error("tenant2 should not see tenant1's key")
	}
}

func TestTenantKVClient_Exists_InnerError(t *testing.T) {
	innerErr := &RequestError{Method: "Exists", Err: errors.New("unavailable")}
	tc, _ := WithTenant(&failingKV{err: innerErr}, "acme")

	_, err := tc.Exists(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error from inner Exists")
	}
	var reqErr *RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// CRUD roundtrip test
// ---------------------------------------------------------------------------

func TestTenantKVClient_PutGetDeleteRoundtrip(t *testing.T) {
	kv := newMockKV()
	tc, _ := WithTenant(kv, "tenant1")
	ctx := context.Background()

	// Put.
	if err := tc.Put(ctx, "key", []byte("value")); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Exists after put.
	exists, err := tc.Exists(ctx, "key")
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}
	if !exists {
		t.Error("expected key to exist after Put")
	}

	// Get.
	val, err := tc.Get(ctx, "key")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if string(val) != "value" {
		t.Errorf("expected 'value', got %q", string(val))
	}

	// Delete.
	if err := tc.Delete(ctx, "key"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Exists after delete.
	exists, err = tc.Exists(ctx, "key")
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}
	if exists {
		t.Error("expected key to not exist after Delete")
	}

	// Get after delete.
	_, err = tc.Get(ctx, "key")
	if err == nil {
		t.Fatal("expected error after delete")
	}
	var knf *KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Errorf("expected KeyNotFoundError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// Tenant isolation test
// ---------------------------------------------------------------------------

func TestTenantKVClient_Isolation(t *testing.T) {
	kv := newMockKV()
	ctx := context.Background()

	tc1, _ := WithTenant(kv, "tenant1")
	tc2, _ := WithTenant(kv, "tenant2")

	// Each tenant writes to the same logical key.
	if err := tc1.Put(ctx, "shared", []byte("from-t1")); err != nil {
		t.Fatal(err)
	}
	if err := tc2.Put(ctx, "shared", []byte("from-t2")); err != nil {
		t.Fatal(err)
	}

	// Each tenant reads its own value.
	v1, err := tc1.Get(ctx, "shared")
	if err != nil {
		t.Fatal(err)
	}
	if string(v1) != "from-t1" {
		t.Errorf("tenant1 expected 'from-t1', got %q", string(v1))
	}

	v2, err := tc2.Get(ctx, "shared")
	if err != nil {
		t.Fatal(err)
	}
	if string(v2) != "from-t2" {
		t.Errorf("tenant2 expected 'from-t2', got %q", string(v2))
	}

	// Listing for tenant1 should not include tenant2 keys.
	keys1, err := tc1.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys1) != 1 {
		t.Errorf("tenant1 expected 1 key, got %d", len(keys1))
	}

	keys2, err := tc2.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys2) != 1 {
		t.Errorf("tenant2 expected 1 key, got %d", len(keys2))
	}

	// Scan isolation.
	pairs1, err := tc1.Scan(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs1) != 1 {
		t.Errorf("tenant1 scan expected 1 pair, got %d", len(pairs1))
	}

	// Exists isolation.
	exists1, err := tc1.Exists(ctx, "shared")
	if err != nil {
		t.Fatal(err)
	}
	if !exists1 {
		t.Error("tenant1 should see its own key")
	}

	// Delete tenant1's key, tenant2 should be unaffected.
	if err := tc1.Delete(ctx, "shared"); err != nil {
		t.Fatal(err)
	}
	exists2, err := tc2.Exists(ctx, "shared")
	if err != nil {
		t.Fatal(err)
	}
	if !exists2 {
		t.Error("tenant2 key should still exist after tenant1 delete")
	}
}
