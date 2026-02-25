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

package file

import (
	"errors"
	"sort"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

func TestFileEngine_PutGet(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Close()

	if err := eng.Put("key1", []byte("value1")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := eng.Get("key1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "value1" {
		t.Fatalf("Get: got %q, want %q", got, "value1")
	}
}

func TestFileEngine_PutOverwrite(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
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

func TestFileEngine_GetNotFound(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Close()

	_, getErr := eng.Get("nonexistent")
	if !errors.Is(getErr, storage.ErrKeyNotFound) {
		t.Fatalf("Get nonexistent: got %v, want %v", getErr, storage.ErrKeyNotFound)
	}
}

func TestFileEngine_Delete(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Close()

	if err := eng.Put("key1", []byte("value1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := eng.Delete("key1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, getErr := eng.Get("key1")
	if !errors.Is(getErr, storage.ErrKeyNotFound) {
		t.Fatalf("Get after Delete: got %v, want %v", getErr, storage.ErrKeyNotFound)
	}
}

func TestFileEngine_DeleteNonExistent(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Close()

	if err := eng.Delete("nonexistent"); err != nil {
		t.Fatalf("Delete nonexistent: unexpected error: %v", err)
	}
}

func TestFileEngine_List(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
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

func TestFileEngine_ListEmptyDir(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Close()

	keys, err := eng.List("")
	if err != nil {
		t.Fatalf("List empty dir: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("List empty dir: got %d keys, want 0", len(keys))
	}
}

func TestFileEngine_ListAllKeys(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Close()

	if err := eng.Put("alpha", []byte("1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := eng.Put("beta", []byte("2")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	keys, err := eng.List("")
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	sort.Strings(keys)
	if len(keys) != 2 {
		t.Fatalf("List all: got %d keys, want 2", len(keys))
	}
	if keys[0] != "alpha" || keys[1] != "beta" {
		t.Fatalf("List all: got %v, want [alpha beta]", keys)
	}
}

func TestFileEngine_ListNoMatch(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Close()

	if err := eng.Put("alpha", []byte("1")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	keys, err := eng.List("nonexistent/")
	if err != nil {
		t.Fatalf("List no match: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("List no match: got %d keys, want 0", len(keys))
	}
}

func TestFileEngine_Exists(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
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

func TestFileEngine_NestedKeys(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Close()

	key := "a/b/c/deep"
	data := []byte("nested-value")

	if err := eng.Put(key, data); err != nil {
		t.Fatalf("Put nested: %v", err)
	}

	got, err := eng.Get(key)
	if err != nil {
		t.Fatalf("Get nested: %v", err)
	}
	if string(got) != "nested-value" {
		t.Fatalf("Get nested: got %q, want %q", got, "nested-value")
	}

	keys, err := eng.List("a/b/")
	if err != nil {
		t.Fatalf("List nested: %v", err)
	}
	if len(keys) != 1 || keys[0] != "a/b/c/deep" {
		t.Fatalf("List nested: got %v, want [a/b/c/deep]", keys)
	}
}

func TestFileEngine_EmptyKey(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
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

func TestFileEngine_Closed(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

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

func TestFileEngine_InvalidPath(t *testing.T) {
	_, err := New("")
	if !errors.Is(err, storage.ErrInvalidPath) {
		t.Fatalf("New empty path: got %v, want %v", err, storage.ErrInvalidPath)
	}
}
