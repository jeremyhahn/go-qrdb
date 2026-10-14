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
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/testutil/memdir"
)

func newCASEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := New(memdir.TempDir(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func TestCompareAndSwap_Success(t *testing.T) {
	e := newCASEngine(t)
	if err := e.Set([]byte("k"), []byte("a")); err != nil {
		t.Fatal(err)
	}
	swapped, err := e.CompareAndSwap(context.Background(), []byte("k"), []byte("a"), []byte("b"))
	if err != nil || !swapped {
		t.Fatalf("CompareAndSwap = %v, %v; want true, nil", swapped, err)
	}
	if got, _ := e.Get([]byte("k")); string(got) != "b" {
		t.Fatalf("value = %q, want b", got)
	}
}

func TestCompareAndSwap_MismatchLeavesValue(t *testing.T) {
	e := newCASEngine(t)
	if err := e.Set([]byte("k"), []byte("a")); err != nil {
		t.Fatal(err)
	}
	swapped, err := e.CompareAndSwap(context.Background(), []byte("k"), []byte("x"), []byte("b"))
	if err != nil || swapped {
		t.Fatalf("CompareAndSwap = %v, %v; want false, nil", swapped, err)
	}
	if got, _ := e.Get([]byte("k")); string(got) != "a" {
		t.Fatalf("value = %q, want a", got)
	}
}

func TestCompareAndSwap_AbsentKeyIsNotCreated(t *testing.T) {
	e := newCASEngine(t)
	swapped, err := e.CompareAndSwap(context.Background(), []byte("k"), nil, []byte("b"))
	if err != nil || swapped {
		t.Fatalf("CompareAndSwap = %v, %v; want false, nil", swapped, err)
	}
	if got, _ := e.Get([]byte("k")); got != nil {
		t.Fatalf("absent key was created: %q", got)
	}
}

func TestCompareAndSwap_ArgumentErrors(t *testing.T) {
	e := newCASEngine(t)
	if _, err := e.CompareAndSwap(context.Background(), nil, nil, nil); !errors.Is(err, storage.ErrEmptyKey) {
		t.Errorf("empty key = %v, want ErrEmptyKey", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.CompareAndSwap(ctx, []byte("k"), nil, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled ctx = %v, want context.Canceled", err)
	}
}

func TestCompareAndSwap_ClosedStore(t *testing.T) {
	e := newCASEngine(t)
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.CompareAndSwap(context.Background(), []byte("k"), nil, nil); !errors.Is(err, storage.ErrStorageClosed) {
		t.Errorf("closed store = %v, want ErrStorageClosed", err)
	}
}

// A journal left by a dead process that cannot be replayed stops the swap before
// it reads anything, and the lock is released for the next caller.
func TestCompareAndSwap_ReplayFailureRefusesWrite(t *testing.T) {
	e := newCASEngine(t)
	if err := os.WriteFile(filepath.Join(e.dir, journalFileName), []byte("not a journal"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.CompareAndSwap(context.Background(), []byte("k"), []byte("a"), []byte("b")); err == nil {
		t.Fatal("CompareAndSwap over an unreadable journal succeeded")
	}
	if err := e.Set([]byte("k"), []byte("v")); err == nil {
		t.Fatal("Set over an unreadable journal succeeded: the lock was not released cleanly or the journal was ignored")
	}
	if err := os.Remove(filepath.Join(e.dir, journalFileName)); err != nil {
		t.Fatal(err)
	}
	if err := e.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set after the journal is gone: %v", err)
	}
}

// A lock file that cannot be locked fails the write without leaving writeMu held.
func TestLockWrites_LockFailureReleasesWriteMu(t *testing.T) {
	e := newCASEngine(t)
	if err := e.lock.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := e.Set([]byte("k"), []byte("v")); err == nil {
			t.Fatal("Set with a closed lock file succeeded")
		}
	}
}

// A record path that is a non-empty directory cannot be replaced: the write
// fails after the temp file was created, and the temp file is not left behind.
func TestSetAndCompareAndSwap_RenameFailureDiscardsTemp(t *testing.T) {
	e := newCASEngine(t)
	key := []byte("k")
	dir, path := e.recordPaths(key)
	if err := os.MkdirAll(filepath.Join(path, "occupied"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := e.Set(key, []byte("v")); err == nil {
		t.Fatal("Set over a directory succeeded")
	}
	if _, err := e.CompareAndSwap(context.Background(), key, []byte("a"), []byte("b")); err == nil {
		t.Fatal("CompareAndSwap over a directory succeeded")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, ent := range entries {
		if ent.Name() != filepath.Base(path) {
			t.Errorf("stray entry %q left in the shard directory", ent.Name())
		}
	}
}

// The *os.Root helpers refuse a path they cannot express relative to the store
// and a store directory they cannot open.
func TestRootHelpers_Errors(t *testing.T) {
	relative := &Engine{dir: "relative-store"}
	if err := relative.removeRecord("/absolute/elsewhere"); err == nil {
		t.Error("removeRecord outside the store succeeded")
	}
	if err := relative.mkdirShardDurable("/absolute/elsewhere"); err == nil {
		t.Error("mkdirShardDurable outside the store succeeded")
	}
	relative.discardTemp("/absolute/elsewhere") // best effort: must not panic

	gone := &Engine{dir: filepath.Join(memdir.TempDir(t), "missing")}
	inside := filepath.Join(gone.dir, "ab", "cd")
	if err := gone.removeRecord(filepath.Join(inside, "rec")); err == nil {
		t.Error("removeRecord in a missing store succeeded")
	}
	if err := gone.mkdirShardDurable(inside); err == nil {
		t.Error("mkdirShardDurable in a missing store succeeded")
	}
	gone.discardTemp(filepath.Join(inside, ".tmp-set-1"))
}

// A shard directory component that is a regular file is refused, not replaced.
func TestMkdirShardDurable_ComponentIsFile(t *testing.T) {
	e := newCASEngine(t)
	if err := os.WriteFile(filepath.Join(e.dir, "ab"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := e.mkdirShardDurable(filepath.Join(e.dir, "ab", "cd"))
	var pe *os.PathError
	if !errors.As(err, &pe) || !errors.Is(err, os.ErrExist) {
		t.Fatalf("mkdirShardDurable = %v, want a PathError wrapping ErrExist", err)
	}
}

// Delete of a key whose record is a non-empty directory reports the failure
// instead of treating it as already gone.
func TestDelete_RecordIsDirectory(t *testing.T) {
	e := newCASEngine(t)
	_, path := e.recordPaths([]byte("k"))
	if err := os.MkdirAll(filepath.Join(path, "occupied"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := e.Delete([]byte("k")); err == nil {
		t.Fatal("Delete of a directory-shaped record succeeded")
	}
}
