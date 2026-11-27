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
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// TestManager_CreateOverlapRollbackKeepsTheSibling interleaves a create of
// data_dir /x with one of /x/child: /x is held inside the backend factory,
// after its root checks passed, while /x/child is created. /x/child must
// either be refused for the overlap, or, when committed, keep its store and
// its record after /x fails its post-open conflict check and rolls back the
// /x directory it made.
func TestManager_CreateOverlapRollbackKeepsTheSibling(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(t.TempDir(), "x")
	child := filepath.Join(parent, "child")
	f := &blockingBackendFactory{
		BackendFactory: NewEmbeddedBackendFactory(),
		entered:        make(chan struct{}),
		release:        make(chan struct{}),
	}
	m := newCatalogManager(slog.Default(), dir, WithBackendFactory(f))
	t.Cleanup(func() { _ = m.Close() })

	parentErr := make(chan error, 1)
	go func() {
		_, err := m.Create(DatabaseConfig{Name: "outer", Backend: BackendFile, DataDir: parent})
		parentErr <- err
	}()
	<-f.entered

	_, childErr := m.Create(DatabaseConfig{Name: "inner", Backend: BackendFile, DataDir: child})
	close(f.release)
	outerErr := <-parentErr

	if childErr == nil {
		// Committed: the rollback of /x must not have taken it.
		if _, err := os.Stat(child); err != nil {
			t.Fatalf("the committed sibling's data dir %s is gone after the overlapping create rolled back: %v", child, err)
		}
		cat, err := m.catalog("test")
		if err != nil {
			t.Fatalf("catalog: %v", err)
		}
		if _, err := cat.Get(context.Background(), "", "inner"); err != nil {
			t.Fatalf("the committed sibling's record is gone: %v", err)
		}
		return
	}
	var conflict *DataDirConflictError
	if !errors.As(childErr, &conflict) {
		t.Fatalf("Create(/x/child) during the create of /x = %v, want *DataDirConflictError", childErr)
	}
	if outerErr != nil {
		t.Fatalf("Create(/x) = %v, want it to commit once the overlapping create was refused", outerErr)
	}
	if _, err := os.Stat(parent); err != nil {
		t.Fatalf("the committed data dir %s: %v", parent, err)
	}
}

// TestManager_CreateRootReservation covers the reservation itself: a root
// held by an in-flight create refuses an equal, containing or contained root
// of another name, admits the same name (the name claim answers it) and a
// disjoint root, and is gone once released.
func TestManager_CreateRootReservation(t *testing.T) {
	dir := t.TempDir()
	m := newCatalogManager(slog.Default(), dir)
	t.Cleanup(func() { _ = m.Close() })
	base := filepath.Join(t.TempDir(), "x")

	release, err := m.reserveCreateRoots(DatabaseConfig{Name: "a", DataDir: base})
	if err != nil {
		t.Fatalf("reserve a: %v", err)
	}
	for _, root := range []string{base, filepath.Join(base, "child"), filepath.Dir(base)} {
		_, err := m.reserveCreateRoots(DatabaseConfig{Name: "b", DataDir: root})
		var conflict *DataDirConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("reserve b at %s while a holds %s = %v, want *DataDirConflictError", root, base, err)
		}
	}
	if !m.heldByOtherCreate(keyFor("", "b"), filepath.Dir(base)) {
		t.Fatal("a directory holding a reserved root is not reported as held")
	}
	same, err := m.reserveCreateRoots(DatabaseConfig{Name: "a", DataDir: base})
	if err != nil {
		t.Fatalf("reserve the same name: %v", err)
	}
	same()
	other, err := m.reserveCreateRoots(DatabaseConfig{Name: "c", DataDir: filepath.Join(t.TempDir(), "y")})
	if err != nil {
		t.Fatalf("reserve a disjoint root: %v", err)
	}
	other()
	release()
	again, err := m.reserveCreateRoots(DatabaseConfig{Name: "b", DataDir: filepath.Join(base, "child")})
	if err != nil {
		t.Fatalf("reserve after release: %v", err)
	}
	again()
	if m.heldByOtherCreate(keyFor("", "b"), filepath.Dir(base)) {
		t.Fatal("a released reservation is still held")
	}
}

// TestCreateUndo_RunKeepsAnotherDatabasesDirectory rolls back a create that
// made /x after a database of another name committed at /x/child, and one
// that made /y while another create holds /y/child: neither rollback may
// remove the directory, which now holds the other database's store.
func TestCreateUndo_RunKeepsAnotherDatabasesDirectory(t *testing.T) {
	dir := t.TempDir()
	m := newCatalogManager(slog.Default(), dir)
	t.Cleanup(func() { _ = m.Close() })

	parent := filepath.Join(t.TempDir(), "x")
	child := filepath.Join(parent, "child")
	if _, err := m.Create(DatabaseConfig{Name: "inner", Backend: BackendFile, DataDir: child}); err != nil {
		t.Fatalf("Create(inner): %v", err)
	}
	undo := &createUndo{m: m, name: "outer", dirs: []string{parent}}
	if err := undo.run(context.Background(), nil); err != nil {
		t.Fatalf("undo: %v", err)
	}
	if _, err := os.Stat(child); err != nil {
		t.Fatalf("the rollback removed the registered database's data dir %s: %v", child, err)
	}

	held := filepath.Join(t.TempDir(), "y")
	release, err := m.reserveCreateRoots(DatabaseConfig{Name: "pending", DataDir: filepath.Join(held, "child")})
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	t.Cleanup(release)
	if err := os.MkdirAll(held, 0o750); err != nil {
		t.Fatal(err)
	}
	undo = &createUndo{m: m, name: "outer", dirs: []string{held}}
	if err := undo.run(context.Background(), nil); err != nil {
		t.Fatalf("undo: %v", err)
	}
	if _, err := os.Stat(held); err != nil {
		t.Fatalf("the rollback removed %s, which holds an in-flight create's root: %v", held, err)
	}
}
