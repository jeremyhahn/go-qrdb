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

package memory

import (
	"errors"
	"sort"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

func TestMemoryEngine_PutGet(t *testing.T) {
	eng := New()
	defer eng.Close()

	if err := eng.Put("key1", []byte("value1")); err != nil {
		t.Fatalf("Put: unexpected error: %v", err)
	}

	got, err := eng.Get("key1")
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if string(got) != "value1" {
		t.Fatalf("Get: got %q, want %q", got, "value1")
	}
}

func TestMemoryEngine_PutOverwrite(t *testing.T) {
	eng := New()
	defer eng.Close()

	if err := eng.Put("key1", []byte("original")); err != nil {
		t.Fatalf("Put original: %v", err)
	}
	if err := eng.Put("key1", []byte("overwritten")); err != nil {
		t.Fatalf("Put overwrite: %v", err)
	}

	got, err := eng.Get("key1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "overwritten" {
		t.Fatalf("Get: got %q, want %q", got, "overwritten")
	}
}

func TestMemoryEngine_GetNotFound(t *testing.T) {
	eng := New()
	defer eng.Close()

	_, err := eng.Get("nonexistent")
	if !errors.Is(err, storage.ErrKeyNotFound) {
		t.Fatalf("Get nonexistent: got %v, want %v", err, storage.ErrKeyNotFound)
	}
}

func TestMemoryEngine_Delete(t *testing.T) {
	eng := New()
	defer eng.Close()

	if err := eng.Put("key1", []byte("value1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := eng.Delete("key1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := eng.Get("key1")
	if !errors.Is(err, storage.ErrKeyNotFound) {
		t.Fatalf("Get after Delete: got %v, want %v", err, storage.ErrKeyNotFound)
	}
}

func TestMemoryEngine_DeleteNonExistent(t *testing.T) {
	eng := New()
	defer eng.Close()

	if err := eng.Delete("nonexistent"); err != nil {
		t.Fatalf("Delete nonexistent: unexpected error: %v", err)
	}
}

func TestMemoryEngine_List(t *testing.T) {
	eng := New()
	defer eng.Close()

	entries := map[string]string{
		"users/alice": "a",
		"users/bob":   "b",
		"config/db":   "c",
	}
	for k, v := range entries {
		if err := eng.Put(k, []byte(v)); err != nil {
			t.Fatalf("Put %q: %v", k, err)
		}
	}

	keys, err := eng.List("users/")
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
}

func TestMemoryEngine_ListEmpty(t *testing.T) {
	eng := New()
	defer eng.Close()

	entries := map[string]string{
		"a": "1",
		"b": "2",
		"c": "3",
	}
	for k, v := range entries {
		if err := eng.Put(k, []byte(v)); err != nil {
			t.Fatalf("Put %q: %v", k, err)
		}
	}

	keys, err := eng.List("")
	if err != nil {
		t.Fatalf("List empty prefix: %v", err)
	}
	if len(keys) != 3 {
		t.Fatalf("List empty prefix: got %d keys, want 3", len(keys))
	}
}

func TestMemoryEngine_Exists(t *testing.T) {
	eng := New()
	defer eng.Close()

	if err := eng.Put("key1", []byte("value1")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	exists, err := eng.Exists("key1")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Fatal("Exists: got false, want true")
	}

	exists, err = eng.Exists("nonexistent")
	if err != nil {
		t.Fatalf("Exists nonexistent: %v", err)
	}
	if exists {
		t.Fatal("Exists nonexistent: got true, want false")
	}
}

func TestMemoryEngine_EmptyKey(t *testing.T) {
	eng := New()
	defer eng.Close()

	if _, err := eng.Get(""); !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("Get empty key: got %v, want %v", err, storage.ErrEmptyKey)
	}
	if err := eng.Put("", []byte("data")); !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("Put empty key: got %v, want %v", err, storage.ErrEmptyKey)
	}
	if err := eng.Delete(""); !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("Delete empty key: got %v, want %v", err, storage.ErrEmptyKey)
	}
	if _, err := eng.Exists(""); !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("Exists empty key: got %v, want %v", err, storage.ErrEmptyKey)
	}
}

func TestMemoryEngine_Closed(t *testing.T) {
	eng := New()

	if err := eng.Put("key1", []byte("value1")); err != nil {
		t.Fatalf("Put before close: %v", err)
	}

	if err := eng.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := eng.Get("key1"); !errors.Is(err, storage.ErrStorageClosed) {
		t.Fatalf("Get after close: got %v, want %v", err, storage.ErrStorageClosed)
	}
	if err := eng.Put("key2", []byte("value2")); !errors.Is(err, storage.ErrStorageClosed) {
		t.Fatalf("Put after close: got %v, want %v", err, storage.ErrStorageClosed)
	}
	if err := eng.Delete("key1"); !errors.Is(err, storage.ErrStorageClosed) {
		t.Fatalf("Delete after close: got %v, want %v", err, storage.ErrStorageClosed)
	}
	if _, err := eng.List(""); !errors.Is(err, storage.ErrStorageClosed) {
		t.Fatalf("List after close: got %v, want %v", err, storage.ErrStorageClosed)
	}
	if _, err := eng.Exists("key1"); !errors.Is(err, storage.ErrStorageClosed) {
		t.Fatalf("Exists after close: got %v, want %v", err, storage.ErrStorageClosed)
	}
}

func TestMemoryEngine_DataIsolation(t *testing.T) {
	eng := New()
	defer eng.Close()

	original := []byte("original")
	if err := eng.Put("key1", original); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Mutate the original slice after Put.
	original[0] = 'X'

	got, err := eng.Get("key1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "original" {
		t.Fatalf("Data isolation (put): stored data was mutated, got %q", got)
	}

	// Mutate the returned slice and verify storage is unaffected.
	got[0] = 'Y'

	got2, err := eng.Get("key1")
	if err != nil {
		t.Fatalf("Get second: %v", err)
	}
	if string(got2) != "original" {
		t.Fatalf("Data isolation (get): stored data was mutated, got %q", got2)
	}
}
