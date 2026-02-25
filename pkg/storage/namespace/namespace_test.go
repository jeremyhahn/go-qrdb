// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package namespace

import (
	"errors"
	"sort"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

func TestNamespaceEngine_PutGet(t *testing.T) {
	inner := memory.New()
	defer inner.Close()

	ns := New(inner, "tenant1")

	if err := ns.Put("key1", []byte("value1")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := ns.Get("key1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "value1" {
		t.Fatalf("Get: got %q, want %q", got, "value1")
	}

	// Verify key is stored with prefix in inner engine.
	innerVal, err := inner.Get("tenant1/key1")
	if err != nil {
		t.Fatalf("inner.Get: %v", err)
	}
	if string(innerVal) != "value1" {
		t.Fatalf("inner.Get: got %q, want %q", innerVal, "value1")
	}
}

func TestNamespaceEngine_Delete(t *testing.T) {
	inner := memory.New()
	defer inner.Close()

	ns := New(inner, "tenant1")

	if err := ns.Put("key1", []byte("value1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := ns.Delete("key1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := ns.Get("key1")
	if !errors.Is(err, storage.ErrKeyNotFound) {
		t.Fatalf("Get after Delete: got %v, want %v", err, storage.ErrKeyNotFound)
	}

	// Verify gone from inner engine as well.
	_, err = inner.Get("tenant1/key1")
	if !errors.Is(err, storage.ErrKeyNotFound) {
		t.Fatalf("inner.Get after Delete: got %v, want %v", err, storage.ErrKeyNotFound)
	}
}

func TestNamespaceEngine_List(t *testing.T) {
	inner := memory.New()
	defer inner.Close()

	ns := New(inner, "tenant1")

	if err := ns.Put("users/alice", []byte("a")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := ns.Put("users/bob", []byte("b")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := ns.Put("config/db", []byte("c")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	keys, err := ns.List("users/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	sort.Strings(keys)

	if len(keys) != 2 {
		t.Fatalf("List: got %d keys, want 2", len(keys))
	}
	if keys[0] != "users/alice" || keys[1] != "users/bob" {
		t.Fatalf("List: got %v, want [users/alice users/bob]", keys)
	}

	// Empty prefix returns all keys within namespace.
	allKeys, err := ns.List("")
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(allKeys) != 3 {
		t.Fatalf("List all: got %d keys, want 3", len(allKeys))
	}
}

func TestNamespaceEngine_Exists(t *testing.T) {
	inner := memory.New()
	defer inner.Close()

	ns := New(inner, "tenant1")

	if err := ns.Put("key1", []byte("value1")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	exists, err := ns.Exists("key1")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Fatal("Exists: got false, want true")
	}

	exists, err = ns.Exists("nonexistent")
	if err != nil {
		t.Fatalf("Exists nonexistent: %v", err)
	}
	if exists {
		t.Fatal("Exists nonexistent: got true, want false")
	}
}

func TestNamespaceEngine_Isolation(t *testing.T) {
	inner := memory.New()
	defer inner.Close()

	ns1 := New(inner, "tenant1")
	ns2 := New(inner, "tenant2")

	if err := ns1.Put("shared-key", []byte("tenant1-value")); err != nil {
		t.Fatalf("ns1 Put: %v", err)
	}
	if err := ns2.Put("shared-key", []byte("tenant2-value")); err != nil {
		t.Fatalf("ns2 Put: %v", err)
	}

	got1, err := ns1.Get("shared-key")
	if err != nil {
		t.Fatalf("ns1 Get: %v", err)
	}
	if string(got1) != "tenant1-value" {
		t.Fatalf("ns1 Get: got %q, want %q", got1, "tenant1-value")
	}

	got2, err := ns2.Get("shared-key")
	if err != nil {
		t.Fatalf("ns2 Get: %v", err)
	}
	if string(got2) != "tenant2-value" {
		t.Fatalf("ns2 Get: got %q, want %q", got2, "tenant2-value")
	}

	// List in ns1 should not show ns2 keys.
	keys1, err := ns1.List("")
	if err != nil {
		t.Fatalf("ns1 List: %v", err)
	}
	if len(keys1) != 1 || keys1[0] != "shared-key" {
		t.Fatalf("ns1 List: got %v, want [shared-key]", keys1)
	}

	keys2, err := ns2.List("")
	if err != nil {
		t.Fatalf("ns2 List: %v", err)
	}
	if len(keys2) != 1 || keys2[0] != "shared-key" {
		t.Fatalf("ns2 List: got %v, want [shared-key]", keys2)
	}
}

func TestNamespaceEngine_EmptyKey(t *testing.T) {
	inner := memory.New()
	defer inner.Close()

	ns := New(inner, "tenant1")

	if _, err := ns.Get(""); !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("Get empty key: got %v, want %v", err, storage.ErrEmptyKey)
	}
	if err := ns.Put("", []byte("data")); !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("Put empty key: got %v, want %v", err, storage.ErrEmptyKey)
	}
	if err := ns.Delete(""); !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("Delete empty key: got %v, want %v", err, storage.ErrEmptyKey)
	}
	if _, err := ns.Exists(""); !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("Exists empty key: got %v, want %v", err, storage.ErrEmptyKey)
	}
}

func TestNamespaceEngine_Namespace(t *testing.T) {
	inner := memory.New()
	defer inner.Close()

	ns := New(inner, "my-tenant")
	if ns.Namespace() != "my-tenant" {
		t.Fatalf("Namespace: got %q, want %q", ns.Namespace(), "my-tenant")
	}
}
