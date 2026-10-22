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
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/testutil/memdir"
)

func TestFileEngine_SetGet(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	if err := eng.Set([]byte("key1"), []byte("value1")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := eng.Get([]byte("key1"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "value1" {
		t.Fatalf("Get: got %q, want %q", got, "value1")
	}
}

func TestFileEngine_SetOverwrite(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	if err := eng.Set([]byte("key1"), []byte("original")); err != nil {
		t.Fatalf("Set original: %v", err)
	}
	if err := eng.Set([]byte("key1"), []byte("overwritten")); err != nil {
		t.Fatalf("Set overwrite: %v", err)
	}

	got, err := eng.Get([]byte("key1"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "overwritten" {
		t.Fatalf("Get: got %q, want %q", got, "overwritten")
	}
}

func TestFileEngine_GetNotFound(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	got, getErr := eng.Get([]byte("nonexistent"))
	if getErr != nil {
		t.Fatalf("Get nonexistent: expected nil error, got %v", getErr)
	}
	if got != nil {
		t.Fatalf("Get nonexistent: expected nil value, got %q", got)
	}
}

func TestFileEngine_Delete(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	if err := eng.Set([]byte("key1"), []byte("value1")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := eng.Delete([]byte("key1")); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got, getErr := eng.Get([]byte("key1"))
	if getErr != nil {
		t.Fatalf("Get after Delete: expected nil error, got %v", getErr)
	}
	if got != nil {
		t.Fatalf("Get after Delete: expected nil value, got %q", got)
	}
}

func TestFileEngine_DeleteNonExistent(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	if err := eng.Delete([]byte("nonexistent")); err != nil {
		t.Fatalf("Delete nonexistent: unexpected error: %v", err)
	}
}

func TestFileEngine_List(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	entries := map[string]string{
		"users/alice": "a",
		"users/bob":   "b",
		"config/db":   "c",
	}
	for k, v := range entries {
		if err := eng.Set([]byte(k), []byte(v)); err != nil {
			t.Fatalf("Set %q: %v", k, err)
		}
	}

	keys, err := eng.List([]byte("users/"))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	sort.Slice(keys, func(i, j int) bool {
		return bytes.Compare(keys[i], keys[j]) < 0
	})

	if len(keys) != 2 {
		t.Fatalf("List: got %d keys, want 2", len(keys))
	}
	if !bytes.Equal(keys[0], []byte("users/alice")) || !bytes.Equal(keys[1], []byte("users/bob")) {
		t.Fatalf("List: got %v, want [users/alice users/bob]", keys)
	}
}

func TestFileEngine_ListEmptyDir(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	keys, err := eng.List(nil)
	if err != nil {
		t.Fatalf("List empty dir: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("List empty dir: got %d keys, want 0", len(keys))
	}
}

func TestFileEngine_ListAllKeys(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	if err := eng.Set([]byte("alpha"), []byte("1")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := eng.Set([]byte("beta"), []byte("2")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	keys, err := eng.List(nil)
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	sort.Slice(keys, func(i, j int) bool {
		return bytes.Compare(keys[i], keys[j]) < 0
	})
	if len(keys) != 2 {
		t.Fatalf("List all: got %d keys, want 2", len(keys))
	}
	if !bytes.Equal(keys[0], []byte("alpha")) || !bytes.Equal(keys[1], []byte("beta")) {
		t.Fatalf("List all: got %v, want [alpha beta]", keys)
	}
}

// TestFileEngine_List_DecodesEscapedKeys pins List against binary keys
// (here a NUL byte matching pkg/lease.TablePrefix's shape) round-tripping
// through the hashed, content-addressed layout: the record stores the raw
// key bytes alongside the value, and List decodes them back verbatim rather
// than returning anything derived from the (hash-named, content-free)
// filename.
func TestFileEngine_List_DecodesEscapedKeys(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	const tablePrefix = "\x00qrdb.lease/"
	tableKeys := [][]byte{
		[]byte(tablePrefix + "b/k1"),
		[]byte(tablePrefix + "b/k2"),
	}
	for _, k := range tableKeys {
		if err := eng.Set(k, []byte("v")); err != nil {
			t.Fatalf("Set(%q): %v", k, err)
		}
	}

	got, err := eng.List([]byte(tablePrefix))
	if err != nil {
		t.Fatalf("List(%q): %v", tablePrefix, err)
	}
	if len(got) != len(tableKeys) {
		t.Fatalf("List(%q) returned %d keys, want %d: %q", tablePrefix, len(got), len(tableKeys), got)
	}
	gotSet := make(map[string]bool, len(got))
	for _, k := range got {
		gotSet[string(k)] = true
	}
	for _, want := range tableKeys {
		if !gotSet[string(want)] {
			t.Errorf("List(%q) missing decoded key %q, got %q", tablePrefix, want, got)
		}
	}
}

func TestFileEngine_ListNoMatch(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	if err := eng.Set([]byte("alpha"), []byte("1")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	keys, err := eng.List([]byte("nonexistent/"))
	if err != nil {
		t.Fatalf("List no match: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("List no match: got %d keys, want 0", len(keys))
	}
}

func TestFileEngine_Exists(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	if err := eng.Set([]byte("key1"), []byte("value1")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	exists, err := eng.Exists([]byte("key1"))
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Fatal("Exists: got false, want true")
	}

	exists, err = eng.Exists([]byte("nonexistent"))
	if err != nil {
		t.Fatalf("Exists nonexistent: %v", err)
	}
	if exists {
		t.Fatal("Exists nonexistent: got true, want false")
	}
}

func TestFileEngine_NestedKeys(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	key := []byte("a/b/c/deep")
	data := []byte("nested-value")

	if err := eng.Set(key, data); err != nil {
		t.Fatalf("Set nested: %v", err)
	}

	got, err := eng.Get(key)
	if err != nil {
		t.Fatalf("Get nested: %v", err)
	}
	if string(got) != "nested-value" {
		t.Fatalf("Get nested: got %q, want %q", got, "nested-value")
	}

	keys, err := eng.List([]byte("a/b/"))
	if err != nil {
		t.Fatalf("List nested: %v", err)
	}
	if len(keys) != 1 || !bytes.Equal(keys[0], []byte("a/b/c/deep")) {
		t.Fatalf("List nested: got %v, want [a/b/c/deep]", keys)
	}
}

func TestFileEngine_EmptyKey(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	if _, err := eng.Get(nil); !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("Get empty key: got %v, want %v", err, storage.ErrEmptyKey)
	}
	if err := eng.Set(nil, []byte("data")); !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("Set empty key: got %v, want %v", err, storage.ErrEmptyKey)
	}
	if err := eng.Delete(nil); !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("Delete empty key: got %v, want %v", err, storage.ErrEmptyKey)
	}
	if _, err := eng.Exists(nil); !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("Exists empty key: got %v, want %v", err, storage.ErrEmptyKey)
	}
}

func TestFileEngine_Closed(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := eng.Set([]byte("key1"), []byte("value1")); err != nil {
		t.Fatalf("Set before close: %v", err)
	}

	if err := eng.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var closedErr *storage.ClosedError

	if _, err := eng.Get([]byte("key1")); !errors.As(err, &closedErr) {
		t.Fatalf("Get after close: got %v, want ClosedError", err)
	}
	if err := eng.Set([]byte("key2"), []byte("value2")); !errors.As(err, &closedErr) {
		t.Fatalf("Set after close: got %v, want ClosedError", err)
	}
	if err := eng.Delete([]byte("key1")); !errors.As(err, &closedErr) {
		t.Fatalf("Delete after close: got %v, want ClosedError", err)
	}
	if _, err := eng.List(nil); !errors.As(err, &closedErr) {
		t.Fatalf("List after close: got %v, want ClosedError", err)
	}
	if _, err := eng.Exists([]byte("key1")); !errors.As(err, &closedErr) {
		t.Fatalf("Exists after close: got %v, want ClosedError", err)
	}
	if _, err := eng.Snapshot(); !errors.As(err, &closedErr) {
		t.Fatalf("Snapshot after close: got %v, want ClosedError", err)
	}
}

func TestFileEngine_InvalidPath(t *testing.T) {
	_, err := New("")
	if !errors.Is(err, storage.ErrInvalidPath) {
		t.Fatalf("New empty path: got %v, want %v", err, storage.ErrInvalidPath)
	}
}

// TestNew_MkdirAllError verifies that New returns an OS error when the base directory
// cannot be created because a regular file already occupies a component of the path.
func TestNew_MkdirAllError(t *testing.T) {
	base := memdir.TempDir(t)
	// Create a regular file at the path we intend to use as a directory.
	blockingFile := filepath.Join(base, "obstacle")
	if err := os.WriteFile(blockingFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("setup: WriteFile: %v", err)
	}
	// Attempt to create an engine whose base dir requires "obstacle" to be a directory.
	_, err := New(filepath.Join(blockingFile, "subdir"))
	if err == nil {
		t.Fatal("New: expected error when MkdirAll cannot create dir over a file, got nil")
	}
	// The error should be a real OS error, not one of our sentinel errors.
	if errors.Is(err, storage.ErrInvalidPath) {
		t.Fatalf("New: got ErrInvalidPath, want an OS mkdir error")
	}
}

// TestGet_OSError verifies that Get propagates a non-not-exist OS error (e.g.,
// reading a directory) rather than returning nil, nil.
func TestGet_OSError(t *testing.T) {
	base := memdir.TempDir(t)
	eng, err := New(base)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	key := []byte("secret/data")
	if err := eng.Set(key, []byte("value")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Replace the record file with a directory of the same name.
	// os.ReadFile on a directory always fails, regardless of user.
	_, recordPath := mustRecordPaths(t, eng, key)
	if err := os.Remove(recordPath); err != nil {
		t.Fatalf("setup: Remove: %v", err)
	}
	if err := os.Mkdir(recordPath, 0o700); err != nil {
		t.Fatalf("setup: Mkdir: %v", err)
	}

	_, getErr := eng.Get(key)
	if getErr == nil {
		t.Fatal("Get: expected error when path is a directory, got nil")
	}
}

// TestSet_MkdirAllError verifies that Set returns an OS error when its
// key's shard directory cannot be created because a regular file occupies
// one of the two shard path components.
func TestSet_MkdirAllError(t *testing.T) {
	base := memdir.TempDir(t)
	eng, err := New(base)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	key := []byte("blocked-key")
	shardDir, _ := mustRecordPaths(t, eng, key)
	// Occupy the FIRST shard level (shardDir's parent) with a regular file,
	// so MkdirAll cannot create shardDir under it.
	obstacle := filepath.Dir(shardDir)
	if err := os.RemoveAll(obstacle); err != nil {
		t.Fatalf("setup: RemoveAll: %v", err)
	}
	if err := os.WriteFile(obstacle, []byte("blocker"), 0o600); err != nil {
		t.Fatalf("setup: WriteFile: %v", err)
	}

	setErr := eng.Set(key, []byte("value"))
	if setErr == nil {
		t.Fatal("Set: expected error when MkdirAll cannot mkdir over a file, got nil")
	}
	var closedErr *storage.ClosedError
	if errors.Is(setErr, storage.ErrEmptyKey) || errors.As(setErr, &closedErr) {
		t.Fatalf("Set: unexpected typed error %v, want an OS mkdir error", setErr)
	}
}

// TestDelete_OSError verifies that Delete returns an OS error when os.Remove fails for
// a reason other than the path not existing (e.g., attempting to remove a non-empty directory).
func TestDelete_OSError(t *testing.T) {
	base := memdir.TempDir(t)
	eng, err := New(base)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	key := []byte("nonemptydir-key")
	_, recordPath := mustRecordPaths(t, eng, key)
	if err := os.MkdirAll(recordPath, 0o700); err != nil {
		t.Fatalf("setup: MkdirAll: %v", err)
	}
	childFile := filepath.Join(recordPath, "child")
	if err := os.WriteFile(childFile, []byte("data"), 0o600); err != nil {
		t.Fatalf("setup: WriteFile child: %v", err)
	}

	// os.Remove on a non-empty directory returns ENOTEMPTY, which is not os.IsNotExist.
	deleteErr := eng.Delete(key)
	if deleteErr == nil {
		t.Fatal("Delete: expected error when removing non-empty directory, got nil")
	}
	var closedErr *storage.ClosedError
	if errors.Is(deleteErr, storage.ErrEmptyKey) || errors.As(deleteErr, &closedErr) {
		t.Fatalf("Delete: unexpected typed error %v, want an OS remove error", deleteErr)
	}
}

// TestList_WalkError verifies that List propagates an error when filepath.Walk
// encounters a path it cannot traverse.
func TestList_WalkError(t *testing.T) {
	base := memdir.TempDir(t)
	eng, err := New(base)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	// Create a nested entry so the engine root has content.
	if err := eng.Set([]byte("restricted/entry"), []byte("secret")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Remove the engine directory entirely so filepath.Walk fails on the
	// root path with a "no such file or directory" error. This works
	// regardless of whether we are running as root.
	if err := os.RemoveAll(base); err != nil {
		t.Fatalf("setup: RemoveAll: %v", err)
	}
	// Restore a directory so TempDir cleanup can succeed.
	t.Cleanup(func() {
		_ = os.MkdirAll(base, 0o700)
	})

	_, listErr := eng.List(nil)
	if listErr == nil {
		t.Fatal("List: expected error when engine directory is removed, got nil")
	}
}

// TestExists_OSError verifies that Exists returns a non-nil error (not false) when
// os.Stat fails for a reason other than the path not existing.
func TestExists_OSError(t *testing.T) {
	base := memdir.TempDir(t)
	eng, err := New(base)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	key := []byte("private-record")
	if err := eng.Set(key, []byte("value")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Replace the key's second-level shard directory with a regular file.
	// os.Stat on the record path will then fail with ENOTDIR, because an
	// ancestor is now a file, not a directory. This error is not
	// os.IsNotExist, so Exists must return it. Works regardless of whether
	// we run as root.
	shardDir, _ := mustRecordPaths(t, eng, key)
	if err := os.RemoveAll(shardDir); err != nil {
		t.Fatalf("setup: RemoveAll: %v", err)
	}
	if err := os.WriteFile(shardDir, []byte("blocker"), 0o600); err != nil {
		t.Fatalf("setup: WriteFile: %v", err)
	}

	exists, existsErr := eng.Exists(key)
	if existsErr == nil {
		t.Fatal("Exists: expected error when path component is not a directory, got nil")
	}
	if exists {
		t.Fatal("Exists: got true alongside an error, want false")
	}
	var closedErr *storage.ClosedError
	if errors.Is(existsErr, storage.ErrEmptyKey) || errors.As(existsErr, &closedErr) {
		t.Fatalf("Exists: unexpected typed error %v, want an OS stat error", existsErr)
	}
}

// seedKeys stores each key with its own name as the value.
func seedKeys(t *testing.T, eng *Engine, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if err := eng.Set([]byte(k), []byte(k)); err != nil {
			t.Fatalf("Set(%q): %v", k, err)
		}
	}
}

// listStrings returns every key in eng, in order, as strings.
func listStrings(t *testing.T, eng *Engine) []string {
	t.Helper()
	keys, err := eng.List(nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = string(k)
	}
	return out
}

// TestFileEngine_DeleteRange pins DeleteRange's [start, end) bounds against
// the engine itself: hashed filenames carry no key order, so the range is
// found by filtering a full listing, and both bounds must be exact.
func TestFileEngine_DeleteRange(t *testing.T) {
	cases := []struct {
		name       string
		start, end string
		want       []string
	}{
		{"start inclusive, end exclusive", "b", "d", []string{"a", "d", "e"}},
		{"empty end is unbounded", "c", "", []string{"a", "b"}},
		{"range below every key", "0", "1", []string{"a", "b", "c", "d", "e"}},
		{"range covering every key", "a", "z", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng, err := New(memdir.TempDir(t))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			defer func() { _ = eng.Close() }()
			seedKeys(t, eng, "a", "b", "c", "d", "e")

			var end []byte
			if tc.end != "" {
				end = []byte(tc.end)
			}
			if err := eng.DeleteRange([]byte(tc.start), end); err != nil {
				t.Fatalf("DeleteRange: %v", err)
			}
			got := listStrings(t, eng)
			if len(got) != len(tc.want) {
				t.Fatalf("keys after DeleteRange = %q, want %q", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("keys after DeleteRange = %q, want %q", got, tc.want)
				}
			}
			for _, k := range tc.want {
				v, err := eng.Get([]byte(k))
				if err != nil || string(v) != k {
					t.Fatalf("surviving Get(%q) = (%q, %v), want (%q, nil)", k, v, err, k)
				}
			}
		})
	}
}

// TestFileEngine_DeleteRange_Refusals covers the refusals DeleteRange makes
// before touching a record: a closed engine, an empty start, and an inverted
// or empty range -- each leaving the stored keys intact.
func TestFileEngine_DeleteRange_Refusals(t *testing.T) {
	eng, err := New(memdir.TempDir(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	seedKeys(t, eng, "a", "b")

	if err := eng.DeleteRange(nil, []byte("b")); !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("DeleteRange(empty start) = %v, want %v", err, storage.ErrEmptyKey)
	}
	if err := eng.DeleteRange([]byte("b"), []byte("a")); !errors.Is(err, storage.ErrInvalidRange) {
		t.Fatalf("DeleteRange(inverted) = %v, want %v", err, storage.ErrInvalidRange)
	}
	if err := eng.DeleteRange([]byte("a"), []byte("a")); !errors.Is(err, storage.ErrInvalidRange) {
		t.Fatalf("DeleteRange(empty range) = %v, want %v", err, storage.ErrInvalidRange)
	}
	if got := listStrings(t, eng); len(got) != 2 {
		t.Fatalf("a refused DeleteRange removed keys: %q", got)
	}

	if err := eng.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	var closedErr *storage.ClosedError
	if err := eng.DeleteRange([]byte("a"), nil); !errors.As(err, &closedErr) {
		t.Fatalf("DeleteRange on a closed engine = %v, want *storage.ClosedError", err)
	}
}

// TestFileEngine_DeleteRange_ListErrorPropagates covers the listing that
// finds the range failing: a corrupt record makes List refuse, and
// DeleteRange must return that refusal rather than delete a partial range.
func TestFileEngine_DeleteRange_ListErrorPropagates(t *testing.T) {
	eng, err := New(memdir.TempDir(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()
	seedKeys(t, eng, "a")

	dir, path := mustRecordPaths(t, eng, []byte("corrupt"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte{0, 0}, 0o600); err != nil {
		t.Fatal(err)
	}

	var rce *RecordCorruptError
	if err := eng.DeleteRange([]byte("a"), nil); !errors.As(err, &rce) {
		t.Fatalf("DeleteRange = %v, want List's *RecordCorruptError", err)
	}
	if ok, err := eng.Exists([]byte("a")); err != nil || !ok {
		t.Fatalf("Exists(a) after a refused DeleteRange = (%v, %v), want (true, nil)", ok, err)
	}
}

// TestFileEngine_DeleteRange_DeleteErrorPropagates covers a delete inside the
// range failing. List recovers a key from the record it finds, not from the
// file's name, so a record misplaced under another key's hash -- the
// corruption Get refuses as KeyMismatchError -- lists as "b" while Delete
// targets "b"'s own path. A non-empty directory there makes os.Remove fail
// with ENOTEMPTY, and DeleteRange must report it rather than succeed.
func TestFileEngine_DeleteRange_DeleteErrorPropagates(t *testing.T) {
	eng, err := New(memdir.TempDir(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	misplacedDir, misplacedPath := mustRecordPaths(t, eng, []byte("x"))
	if err := os.MkdirAll(misplacedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(misplacedPath, encodeRecord([]byte("b"), []byte("v")), 0o600); err != nil {
		t.Fatal(err)
	}
	_, ownPath := mustRecordPaths(t, eng, []byte("b"))
	if err := os.MkdirAll(ownPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ownPath, "occupied"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	err = eng.DeleteRange([]byte("a"), nil)
	if err == nil {
		t.Fatal("DeleteRange: expected the failed delete to propagate, got nil")
	}
	var closedErr *storage.ClosedError
	if errors.Is(err, storage.ErrInvalidRange) || errors.As(err, &closedErr) {
		t.Fatalf("DeleteRange = %v, want the OS remove error", err)
	}
}

// ---------------------------------------------------------------------------
// Batch tests
// ---------------------------------------------------------------------------

func TestFileEngine_BatchSetAndCommit(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	batch := eng.Batch()
	if err := batch.Set([]byte("b1"), []byte("v1")); err != nil {
		t.Fatalf("batch Set: %v", err)
	}
	if err := batch.Set([]byte("b2"), []byte("v2")); err != nil {
		t.Fatalf("batch Set: %v", err)
	}

	// Values should not be visible before commit.
	got, getErr := eng.Get([]byte("b1"))
	if getErr != nil {
		t.Fatalf("Get before commit: %v", getErr)
	}
	if got != nil {
		t.Fatalf("Get before commit: expected nil, got %q", got)
	}

	if err := batch.Commit(); err != nil {
		t.Fatalf("batch Commit: %v", err)
	}

	got, getErr = eng.Get([]byte("b1"))
	if getErr != nil {
		t.Fatalf("Get after commit: %v", getErr)
	}
	if string(got) != "v1" {
		t.Fatalf("Get after commit: got %q, want %q", got, "v1")
	}
}

func TestFileEngine_BatchDeleteAndCommit(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	if err := eng.Set([]byte("del1"), []byte("val")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	batch := eng.Batch()
	if err := batch.Delete([]byte("del1")); err != nil {
		t.Fatalf("batch Delete: %v", err)
	}
	if err := batch.Commit(); err != nil {
		t.Fatalf("batch Commit: %v", err)
	}

	got, getErr := eng.Get([]byte("del1"))
	if getErr != nil {
		t.Fatalf("Get after batch delete: %v", getErr)
	}
	if got != nil {
		t.Fatalf("Get after batch delete: expected nil, got %q", got)
	}
}

// TestFileEngine_BatchRangeDeleteKeepsOrder pins that a batch range delete
// applies in its place between the point writes around it: a write the batch
// made before the range inside it is deleted, and one it made after survives.
func TestFileEngine_BatchRangeDeleteKeepsOrder(t *testing.T) {
	eng, err := New(memdir.TempDir(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()
	seedKeys(t, eng, "a", "b", "c", "d")

	batch := eng.Batch()
	steps := []func() error{
		func() error { return batch.Set([]byte("a"), []byte("a2")) },
		func() error { return batch.Set([]byte("b2"), []byte("b2")) },
		func() error { return batch.DeleteRange([]byte("b"), []byte("d")) },
		func() error { return batch.Set([]byte("c"), []byte("c2")) },
	}
	for i, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("batch step %d: %v", i, err)
		}
	}
	if err := batch.Commit(); err != nil {
		t.Fatalf("batch Commit: %v", err)
	}

	want := map[string]string{"a": "a2", "c": "c2", "d": "d"}
	got := listStrings(t, eng)
	if len(got) != len(want) {
		t.Fatalf("keys after commit = %q, want the keys of %v", got, want)
	}
	for k, v := range want {
		if val, err := eng.Get([]byte(k)); err != nil || string(val) != v {
			t.Fatalf("Get(%q) = (%q, %v), want (%q, nil)", k, val, err, v)
		}
	}
}

func TestFileEngine_BatchRollback(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	batch := eng.Batch()
	if err := batch.Set([]byte("rb1"), []byte("v1")); err != nil {
		t.Fatalf("batch Set: %v", err)
	}
	if err := batch.Rollback(); err != nil {
		t.Fatalf("batch Rollback: %v", err)
	}

	got, getErr := eng.Get([]byte("rb1"))
	if getErr != nil {
		t.Fatalf("Get after rollback: %v", getErr)
	}
	if got != nil {
		t.Fatalf("Get after rollback: expected nil, got %q", got)
	}
}

func TestFileEngine_BatchClose(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	batch := eng.Batch()
	if err := batch.Set([]byte("c1"), []byte("v1")); err != nil {
		t.Fatalf("batch Set: %v", err)
	}
	if err := batch.Close(); err != nil {
		t.Fatalf("batch Close: %v", err)
	}

	// Double close should be a no-op.
	if err := batch.Close(); err != nil {
		t.Fatalf("batch double Close: %v", err)
	}
}

func TestFileEngine_BatchEmptyKey(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	batch := eng.Batch()
	defer func() { _ = batch.Close() }()

	if err := batch.Set(nil, []byte("v")); err == nil {
		t.Fatal("batch Set nil key: expected error, got nil")
	}
	if err := batch.Delete(nil); err == nil {
		t.Fatal("batch Delete nil key: expected error, got nil")
	}
}

func TestFileEngine_BatchClosedOperations(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	batch := eng.Batch()
	if err := batch.Commit(); err != nil {
		t.Fatalf("batch Commit: %v", err)
	}

	// All operations on a closed batch should return BatchError.
	var batchErr *storage.BatchError
	if err := batch.Set([]byte("k"), []byte("v")); !errors.As(err, &batchErr) {
		t.Fatalf("Set on closed batch: got %v, want BatchError", err)
	}
	if err := batch.Delete([]byte("k")); !errors.As(err, &batchErr) {
		t.Fatalf("Delete on closed batch: got %v, want BatchError", err)
	}
	if err := batch.Commit(); !errors.As(err, &batchErr) {
		t.Fatalf("Commit on closed batch: got %v, want BatchError", err)
	}
	if err := batch.Rollback(); !errors.As(err, &batchErr) {
		t.Fatalf("Rollback on closed batch: got %v, want BatchError", err)
	}
}

// ---------------------------------------------------------------------------
// Iterator tests
// ---------------------------------------------------------------------------

func TestFileEngine_IteratorForwardScan(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	for _, k := range []string{"c", "a", "b"} {
		if err := eng.Set([]byte(k), []byte(k+"v")); err != nil {
			t.Fatalf("Set %q: %v", k, err)
		}
	}

	it := eng.NewIterator(nil)
	defer func() { _ = it.Close() }()

	if !it.SeekToFirst() {
		t.Fatal("SeekToFirst: expected true")
	}
	var keys []string
	for it.Valid() {
		keys = append(keys, string(it.Key()))
		it.Next()
	}
	expected := []string{"a", "b", "c"}
	if len(keys) != len(expected) {
		t.Fatalf("iterator forward: got %v, want %v", keys, expected)
	}
	for i, k := range keys {
		if k != expected[i] {
			t.Fatalf("iterator forward[%d]: got %q, want %q", i, k, expected[i])
		}
	}
}

func TestFileEngine_IteratorReverse(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	for _, k := range []string{"c", "a", "b"} {
		if err := eng.Set([]byte(k), []byte(k+"v")); err != nil {
			t.Fatalf("Set %q: %v", k, err)
		}
	}

	it := eng.NewIterator(&storage.IteratorOptions{Reverse: true})
	defer func() { _ = it.Close() }()

	if !it.SeekToFirst() {
		t.Fatal("SeekToFirst reverse: expected true")
	}
	var keys []string
	for it.Valid() {
		keys = append(keys, string(it.Key()))
		it.Next()
	}
	expected := []string{"c", "b", "a"}
	if len(keys) != len(expected) {
		t.Fatalf("iterator reverse: got %v, want %v", keys, expected)
	}
	for i, k := range keys {
		if k != expected[i] {
			t.Fatalf("iterator reverse[%d]: got %q, want %q", i, k, expected[i])
		}
	}
}

func TestFileEngine_IteratorBounds(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	for _, k := range []string{"a", "b", "c", "d", "e"} {
		if err := eng.Set([]byte(k), []byte(k)); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	it := eng.NewIterator(&storage.IteratorOptions{
		LowerBound: []byte("b"),
		UpperBound: []byte("d"),
	})
	defer func() { _ = it.Close() }()

	if !it.SeekToFirst() {
		t.Fatal("SeekToFirst bounded: expected true")
	}
	var keys []string
	for it.Valid() {
		keys = append(keys, string(it.Key()))
		it.Next()
	}
	expected := []string{"b", "c"}
	if len(keys) != len(expected) {
		t.Fatalf("iterator bounded: got %v, want %v", keys, expected)
	}
	for i, k := range keys {
		if k != expected[i] {
			t.Fatalf("iterator bounded[%d]: got %q, want %q", i, k, expected[i])
		}
	}
}

func TestFileEngine_IteratorSeek(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	for _, k := range []string{"a", "c", "e"} {
		if err := eng.Set([]byte(k), []byte(k)); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	it := eng.NewIterator(nil)
	defer func() { _ = it.Close() }()

	// Seek to "b" should land on "c".
	if !it.Seek([]byte("b")) {
		t.Fatal("Seek b: expected true")
	}
	if string(it.Key()) != "c" {
		t.Fatalf("Seek b: got %q, want %q", it.Key(), "c")
	}

	// Seek past all entries should return false.
	if it.Seek([]byte("z")) {
		t.Fatal("Seek z: expected false")
	}
}

func TestFileEngine_IteratorSeekToLast(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	for _, k := range []string{"a", "b", "c"} {
		if err := eng.Set([]byte(k), []byte(k)); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	it := eng.NewIterator(nil)
	defer func() { _ = it.Close() }()

	if !it.SeekToLast() {
		t.Fatal("SeekToLast: expected true")
	}
	if string(it.Key()) != "c" {
		t.Fatalf("SeekToLast: got %q, want %q", it.Key(), "c")
	}
}

func TestFileEngine_IteratorPrev(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	for _, k := range []string{"a", "b", "c"} {
		if err := eng.Set([]byte(k), []byte(k)); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	it := eng.NewIterator(nil)
	defer func() { _ = it.Close() }()

	if !it.SeekToLast() {
		t.Fatal("SeekToLast: expected true")
	}
	if string(it.Key()) != "c" {
		t.Fatalf("SeekToLast key: got %q, want %q", it.Key(), "c")
	}
	if !it.Prev() {
		t.Fatal("Prev from c: expected true")
	}
	if string(it.Key()) != "b" {
		t.Fatalf("Prev key: got %q, want %q", it.Key(), "b")
	}
}

func TestFileEngine_IteratorEmpty(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	it := eng.NewIterator(nil)
	defer func() { _ = it.Close() }()

	if it.SeekToFirst() {
		t.Fatal("SeekToFirst on empty: expected false")
	}
	if it.SeekToLast() {
		t.Fatal("SeekToLast on empty: expected false")
	}
	if it.Valid() {
		t.Fatal("Valid on empty: expected false")
	}
	if it.Key() != nil {
		t.Fatal("Key on invalid: expected nil")
	}
	if it.Value() != nil {
		t.Fatal("Value on invalid: expected nil")
	}
}

func TestFileEngine_IteratorOnlyKeys(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	if err := eng.Set([]byte("k1"), []byte("v1")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	it := eng.NewIterator(&storage.IteratorOptions{OnlyKeys: true})
	defer func() { _ = it.Close() }()

	if !it.SeekToFirst() {
		t.Fatal("SeekToFirst: expected true")
	}
	if string(it.Key()) != "k1" {
		t.Fatalf("Key: got %q, want %q", it.Key(), "k1")
	}
	// Value should be nil when OnlyKeys is set.
	if it.Value() != nil {
		t.Fatalf("Value with OnlyKeys: expected nil, got %q", it.Value())
	}
}

func TestFileEngine_IteratorNextWithoutSeek(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	if err := eng.Set([]byte("k1"), []byte("v1")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	it := eng.NewIterator(nil)
	defer func() { _ = it.Close() }()

	// Next/Prev without initialization should return false.
	if it.Next() {
		t.Fatal("Next without seek: expected false")
	}
	if it.Prev() {
		t.Fatal("Prev without seek: expected false")
	}
}

// ---------------------------------------------------------------------------
// Snapshot tests
// ---------------------------------------------------------------------------

func TestFileEngine_SnapshotGet(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	if err := eng.Set([]byte("sk1"), []byte("sv1")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	snap, err := eng.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer func() { _ = snap.Close() }()

	got, err := snap.Get([]byte("sk1"))
	if err != nil {
		t.Fatalf("snapshot Get: %v", err)
	}
	if string(got) != "sv1" {
		t.Fatalf("snapshot Get: got %q, want %q", got, "sv1")
	}

	// Snapshot should be isolated from subsequent mutations.
	if err := eng.Set([]byte("sk1"), []byte("modified")); err != nil {
		t.Fatalf("Set after snapshot: %v", err)
	}
	got, err = snap.Get([]byte("sk1"))
	if err != nil {
		t.Fatalf("snapshot Get after mutation: %v", err)
	}
	if string(got) != "sv1" {
		t.Fatalf("snapshot Get after mutation: got %q, want %q (not isolated)", got, "sv1")
	}
}

func TestFileEngine_SnapshotGetNotFound(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	snap, err := eng.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer func() { _ = snap.Close() }()

	got, getErr := snap.Get([]byte("nonexistent"))
	if getErr != nil {
		t.Fatalf("snapshot Get nonexistent: %v", getErr)
	}
	if got != nil {
		t.Fatalf("snapshot Get nonexistent: expected nil, got %q", got)
	}
}

func TestFileEngine_SnapshotGetEmptyKey(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	snap, err := eng.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer func() { _ = snap.Close() }()

	_, getErr := snap.Get(nil)
	if getErr == nil {
		t.Fatal("snapshot Get nil key: expected error, got nil")
	}
}

func TestFileEngine_SnapshotIterator(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	for _, k := range []string{"a", "b", "c"} {
		if err := eng.Set([]byte(k), []byte(k+"v")); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	snap, err := eng.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer func() { _ = snap.Close() }()

	it := snap.NewIterator(nil)
	defer func() { _ = it.Close() }()

	if !it.SeekToFirst() {
		t.Fatal("snapshot SeekToFirst: expected true")
	}
	var keys []string
	for it.Valid() {
		keys = append(keys, string(it.Key()))
		it.Next()
	}
	expected := []string{"a", "b", "c"}
	if len(keys) != len(expected) {
		t.Fatalf("snapshot iterator: got %v, want %v", keys, expected)
	}
	for i, k := range keys {
		if k != expected[i] {
			t.Fatalf("snapshot iterator[%d]: got %q, want %q", i, k, expected[i])
		}
	}
}

func TestFileEngine_SnapshotClosed(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	snap, err := eng.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if err := snap.Close(); err != nil {
		t.Fatalf("snapshot Close: %v", err)
	}

	// Double close should be a no-op.
	if err := snap.Close(); err != nil {
		t.Fatalf("snapshot double Close: %v", err)
	}

	// Get on closed snapshot should return an error.
	_, getErr := snap.Get([]byte("k"))
	if getErr == nil {
		t.Fatal("snapshot Get after close: expected error, got nil")
	}

	// Iterator on closed snapshot should be empty.
	it := snap.NewIterator(nil)
	if it.SeekToFirst() {
		t.Fatal("snapshot iterator after close: expected SeekToFirst false")
	}
	_ = it.Close()
}

func TestFileEngine_SnapshotIteratorBounds(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	for _, k := range []string{"a", "b", "c", "d"} {
		if err := eng.Set([]byte(k), []byte(k)); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	snap, err := eng.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer func() { _ = snap.Close() }()

	it := snap.NewIterator(&storage.IteratorOptions{
		LowerBound: []byte("b"),
		UpperBound: []byte("d"),
	})
	defer func() { _ = it.Close() }()

	if !it.SeekToFirst() {
		t.Fatal("snapshot bounded SeekToFirst: expected true")
	}
	var keys []string
	for it.Valid() {
		keys = append(keys, string(it.Key()))
		it.Next()
	}
	expected := []string{"b", "c"}
	if len(keys) != len(expected) {
		t.Fatalf("snapshot bounded iterator: got %v, want %v", keys, expected)
	}
}

func TestFileEngine_SnapshotIteratorReverse(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	for _, k := range []string{"a", "b", "c"} {
		if err := eng.Set([]byte(k), []byte(k)); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	snap, err := eng.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer func() { _ = snap.Close() }()

	it := snap.NewIterator(&storage.IteratorOptions{Reverse: true})
	defer func() { _ = it.Close() }()

	if !it.SeekToFirst() {
		t.Fatal("snapshot reverse SeekToFirst: expected true")
	}
	var keys []string
	for it.Valid() {
		keys = append(keys, string(it.Key()))
		it.Next()
	}
	expected := []string{"c", "b", "a"}
	if len(keys) != len(expected) {
		t.Fatalf("snapshot reverse iterator: got %v, want %v", keys, expected)
	}
}

// ---------------------------------------------------------------------------
// go-qrdb#592 coverage: error paths in mkdirAllDurable, writeFormatMarker,
// Set's fsync.Rename, and cleanupTempSetFiles.
// ---------------------------------------------------------------------------

// TestMkdirAllDurable_StatErrorPropagates covers the branch where os.Stat
// fails with something other than ENOENT (a real fault, not "does not
// exist yet"): the PARENT is a regular file, so Stat on the child fails
// with ENOTDIR -- a fault no permission bit grants, so it holds as root.
func TestMkdirAllDurable_StatErrorPropagates(t *testing.T) {
	parent := filepath.Join(memdir.TempDir(t), "parent")
	if err := os.WriteFile(parent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := mkdirAllDurable(filepath.Join(parent, "child"), nil)
	if err == nil {
		t.Fatal("expected ENOTDIR stat'ing under a regular file, got nil")
	}
	if os.IsNotExist(err) {
		t.Fatalf("expected a stat error other than ENOENT, got IsNotExist: %v", err)
	}
}

// TestMkdirAllDurable_MkdirErrorPropagates covers os.Mkdir failing with an
// error OTHER than IsExist: the parent exists (so mkdirAllDurable's
// recursive call for it takes the fast "already exists" path) but denies
// write, so creating the new child directory itself fails.
func TestMkdirAllDurable_MkdirErrorPropagates(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission denial cannot be exercised")
	}
	base := memdir.TempDir(t)
	parent := filepath.Join(base, "parent")
	if err := os.Mkdir(parent, 0o500); err != nil { // r-x: no write
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) }) // #nosec G302 -- restores the mode memdir.TempDir(t) needs to remove the directory; not a secret file's mode

	err := mkdirAllDurable(filepath.Join(parent, "child"), nil)
	if err == nil {
		t.Fatal("expected a permission error creating the child, got nil")
	}
	if os.IsExist(err) {
		t.Fatalf("expected a permission error, got IsExist: %v", err)
	}
}

// TestWriteFormatMarker_CreateTempErrorPropagates covers writeFormatMarker's
// own first error path: an engine root that no longer exists, so the temp
// file cannot be created in it.
func TestWriteFormatMarker_CreateTempErrorPropagates(t *testing.T) {
	e := &Engine{dir: filepath.Join(memdir.TempDir(t), "gone"), fs: vfs.Default, atRest: atRestPlaintext}
	if err := e.writeFormatMarker(); err == nil {
		t.Fatal("expected an error creating the marker's temp file in a missing root, got nil")
	}
}

// TestWriteFormatMarker_RenameErrorPropagates covers writeFormatMarker's
// publish step failing: the marker's path is occupied by a non-empty
// directory, so the rename cannot land, and the temp file it wrote is
// removed rather than left for the next Open to find.
func TestWriteFormatMarker_RenameErrorPropagates(t *testing.T) {
	dir := memdir.TempDir(t)
	markerPath := filepath.Join(dir, formatMarkerFileName)
	if err := os.Mkdir(markerPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(markerPath, "occupied"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	e := &Engine{dir: dir, fs: vfs.Default, atRest: atRestPlaintext}
	if err := e.writeFormatMarker(); err == nil {
		t.Fatal("expected writeFormatMarker to fail renaming onto a non-empty directory")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, ent := range entries {
		if ent.Name() != formatMarkerFileName {
			t.Fatalf("failed publish left %q behind", ent.Name())
		}
	}
}

// TestSet_RenameErrorPropagates covers fsync.Rename failing: the record's
// target path is occupied by a non-empty directory, so the rename that
// publishes the temp file cannot succeed.
func TestSet_RenameErrorPropagates(t *testing.T) {
	e, err := New(memdir.TempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()

	key := []byte("blocked-by-a-directory")
	_, recordPath := mustRecordPaths(t, e, key)
	if err := os.MkdirAll(recordPath, 0o700); err != nil {
		t.Fatal(err)
	}
	// A non-empty directory at the target makes the rename fail; an empty
	// one could rename-over on some platforms.
	if err := os.WriteFile(filepath.Join(recordPath, "occupied"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := e.Set(key, []byte("v")); err == nil {
		t.Fatal("expected Set to fail renaming onto a non-empty directory")
	}
}

// TestCleanupTempSetFiles_RemovesStrayTempFiles proves New's own crash-
// recovery cleanup actually removes a ".tmp-set-*" leftover under a shard
// directory, not merely that it compiles.
func TestCleanupTempSetFiles_RemovesStrayTempFiles(t *testing.T) {
	dir := memdir.TempDir(t)
	e, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	shardDir, _ := mustRecordPaths(t, e, []byte("k"))
	if err := os.MkdirAll(shardDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(shardDir, ".tmp-set-stray")
	if err := os.WriteFile(stray, []byte("leftover"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	e2, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = e2.Close() }()
	if _, statErr := os.Stat(stray); !os.IsNotExist(statErr) {
		t.Fatalf("stray temp file not removed at Open: stat = %v", statErr)
	}
}

// TestCleanupTempSetFiles_RemoveErrorPropagates covers the branch where
// os.Remove itself fails for a reason other than the file already being
// gone: the shard directory denies write, so removing the stray temp file
// inside it fails.
func TestCleanupTempSetFiles_RemoveErrorPropagates(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission denial cannot be exercised")
	}
	e, err := New(memdir.TempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()

	shardDir, _ := mustRecordPaths(t, e, []byte("k"))
	if err := os.MkdirAll(shardDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(shardDir, ".tmp-set-stray")
	if err := os.WriteFile(stray, []byte("leftover"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shardDir, 0o500); err != nil { // #nosec G302 -- r-x: the directory's own permission bits are the fault this negative-permission test injects, not a secret file's mode
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(shardDir, 0o700) }) // #nosec G302 -- restores the mode memdir.TempDir(t) needs to remove the directory; not a secret file's mode

	if err := e.cleanupTempSetFiles(); err == nil {
		t.Fatal("expected an error removing a temp file in a read-only directory")
	}
}

// TestCleanupTempSetFiles_WalkErrorPropagates covers the branch where the
// walk itself fails for a reason other than ENOENT: a shard directory this
// engine cannot read at all.
func TestCleanupTempSetFiles_WalkErrorPropagates(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission denial cannot be exercised")
	}
	e, err := New(memdir.TempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()

	shardDir, _ := mustRecordPaths(t, e, []byte("k"))
	if err := os.MkdirAll(shardDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shardDir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(shardDir, 0o700) }) // #nosec G302 -- restores the mode memdir.TempDir(t) needs to remove the directory; not a secret file's mode

	if err := e.cleanupTempSetFiles(); err == nil {
		t.Fatal("expected a permission error reading the shard directory")
	}
}

// TestList_WalkErrorOnAShardDirectoryPropagates covers List's non-ENOENT
// walk-error branch distinctly from TestList_WalkError, which removes the
// engine's own root: here the root is intact and a single shard directory
// is unreadable.
func TestList_WalkErrorOnAShardDirectoryPropagates(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission denial cannot be exercised")
	}
	e, err := New(memdir.TempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()

	shardDir, _ := mustRecordPaths(t, e, []byte("k"))
	if err := os.MkdirAll(shardDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shardDir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(shardDir, 0o700) }) // #nosec G302 -- restores the mode memdir.TempDir(t) needs to remove the directory; not a secret file's mode

	if _, err := e.List(nil); err == nil {
		t.Fatal("expected a permission error reading the shard directory")
	}
}

// TestIterator_WalkErrorOnAShardDirectoryPropagates is List's test above,
// for the iterator's own walk.
func TestIterator_WalkErrorOnAShardDirectoryPropagates(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission denial cannot be exercised")
	}
	e, err := New(memdir.TempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()

	shardDir, _ := mustRecordPaths(t, e, []byte("k"))
	if err := os.MkdirAll(shardDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shardDir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(shardDir, 0o700) }) // #nosec G302 -- restores the mode memdir.TempDir(t) needs to remove the directory; not a secret file's mode

	it := e.NewIterator(nil)
	defer func() { _ = it.Close() }()
	for it.SeekToFirst(); it.Valid(); it.Next() {
	}
	if err := it.Error(); err == nil {
		t.Fatal("expected a permission error reading the shard directory")
	}
}

// TestSync_ClosedEngine covers Sync's own closed-engine check, otherwise
// only exercised on an open engine elsewhere.
func TestSync_ClosedEngine(t *testing.T) {
	e, err := New(memdir.TempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	var closedErr *storage.ClosedError
	if err := e.Sync(); !errors.As(err, &closedErr) {
		t.Fatalf("Sync on a closed engine = %v, want *storage.ClosedError", err)
	}
}

// TestIsHashFileName exercises every branch directly: the right length and
// alphabet, the wrong length, and a disqualifying character at every
// position the loop can reach.
func TestIsHashFileName(t *testing.T) {
	validHash := make([]byte, hashHexLen)
	for i := range validHash {
		validHash[i] = "0123456789abcdef"[i%16]
	}
	if !isHashFileName(string(validHash)) {
		t.Errorf("isHashFileName(%q) = false, want true", validHash)
	}
	if isHashFileName("") {
		t.Error("isHashFileName(\"\") = true, want false")
	}
	if isHashFileName(string(validHash[:len(validHash)-1])) {
		t.Error("isHashFileName(too short) = true, want false")
	}
	if isHashFileName(string(validHash) + "0") {
		t.Error("isHashFileName(too long) = true, want false")
	}
	upper := append([]byte(nil), validHash...)
	upper[0] = 'A'
	if isHashFileName(string(upper)) {
		t.Error("isHashFileName(uppercase hex) = true, want false")
	}
	nonHex := append([]byte(nil), validHash...)
	nonHex[len(nonHex)-1] = 'g'
	if isHashFileName(string(nonHex)) {
		t.Error("isHashFileName(non-hex tail) = true, want false")
	}
}

// TestIsShardDirName is isHashFileName's test, for the 2-character shard
// directory name shape.
func TestIsShardDirName(t *testing.T) {
	cases := map[string]bool{
		"ab":         true,
		"00":         true,
		"ff":         true,
		"":           false,
		"a":          false,
		"abc":        false,
		"AB":         false,
		"lost+found": false,
		"g0":         false,
		"0g":         false,
	}
	for name, want := range cases {
		if got := isShardDirName(name); got != want {
			t.Errorf("isShardDirName(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestDecodeRecord_CorruptRecords exercises decodeRecord's two corruption
// branches directly, rather than only through RecordCorruptError.Error().
func TestDecodeRecord_CorruptRecords(t *testing.T) {
	t.Run("shorter than the header", func(t *testing.T) {
		_, _, err := decodeRecord("/some/path", []byte{0x00, 0x00})
		var rce *RecordCorruptError
		if !errors.As(err, &rce) {
			t.Fatalf("decodeRecord = %v, want *RecordCorruptError", err)
		}
	})
	t.Run("declared key length exceeds the record", func(t *testing.T) {
		record := make([]byte, keyHeaderLen+2)
		binary.BigEndian.PutUint32(record[0:keyHeaderLen], 999)
		_, _, err := decodeRecord("/some/path", record)
		var rce *RecordCorruptError
		if !errors.As(err, &rce) {
			t.Fatalf("decodeRecord = %v, want *RecordCorruptError", err)
		}
	})
	t.Run("well-formed record round-trips", func(t *testing.T) {
		record := encodeRecord([]byte("key"), []byte("value"))
		key, value, err := decodeRecord("/some/path", record)
		if err != nil {
			t.Fatalf("decodeRecord: %v", err)
		}
		if string(key) != "key" || string(value) != "value" {
			t.Fatalf("decodeRecord = (%q, %q), want (\"key\", \"value\")", key, value)
		}
	})
}

// TestGet_KeyMismatchError covers the branch where a record's stored key
// does not match the key whose hash named the file -- corruption, not a
// realistic SHA-256 collision -- exercised through Get itself rather than
// only through KeyMismatchError.Error().
func TestGet_KeyMismatchError(t *testing.T) {
	e, err := New(memdir.TempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()

	key := []byte("a")
	if err := e.Set(key, []byte("v")); err != nil {
		t.Fatal(err)
	}
	_, path := mustRecordPaths(t, e, key)
	corrupt := encodeRecord([]byte("not-a"), []byte("v"))
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = e.Get(key)
	var kme *KeyMismatchError
	if !errors.As(err, &kme) {
		t.Fatalf("Get = %v, want *KeyMismatchError", err)
	}
}

// TestEnsureFormatMarker_ReadFileErrorPropagates covers the "neither nil
// nor IsNotExist" branch of os.ReadFile(markerPath): the marker path is
// occupied by a directory, so reading it as a file fails for a reason
// other than absence.
func TestEnsureFormatMarker_ReadFileErrorPropagates(t *testing.T) {
	dir := memdir.TempDir(t)
	if err := os.Mkdir(filepath.Join(dir, formatMarkerFileName), 0o700); err != nil {
		t.Fatal(err)
	}

	_, err := New(dir)
	if err == nil {
		t.Fatal("expected an error reading a format marker occupied by a directory")
	}
}

// TestSnapshot_PropagatesIteratorError covers newSnapshot's error branch:
// a load the underlying iterator could not complete must fail the
// Snapshot call, not return a silently truncated snapshot (go-qrdb#592 F2).
func TestSnapshot_PropagatesIteratorError(t *testing.T) {
	e, err := New(memdir.TempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()

	dir, path := mustRecordPaths(t, e, []byte("corrupt"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte{0, 0}, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = e.Snapshot()
	var rce *RecordCorruptError
	if !errors.As(err, &rce) {
		t.Fatalf("Snapshot = %v, want the iterator's *RecordCorruptError", err)
	}
}
