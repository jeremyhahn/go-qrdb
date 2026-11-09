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
	"io"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
)

func memoryObjstoreOpener(opened *[]string) ObjstoreOpener {
	return func(name, _ string) (storage.Storage, ObjstoreReclaim, error) {
		*opened = append(*opened, name)
		e, err := objstore.New("memory", map[string]string{})
		if err != nil {
			return nil, nil, err
		}
		return e, e.Destroy, nil
	}
}

// A backend: objstore database opens on the objstore engine the node's opener
// returns and round-trips a key through it.
func TestEmbeddedBackendFactory_ObjstoreOpensThroughOpener(t *testing.T) {
	var opened []string
	f := NewEmbeddedBackendFactory(WithObjstoreOpener(memoryObjstoreOpener(&opened)))
	b, err := f.Create(DatabaseConfig{Name: "blobs", Backend: BackendObjstore}, t.TempDir())
	if err != nil {
		t.Fatalf("Create(objstore): %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	if len(opened) != 1 || opened[0] != "blobs" {
		t.Fatalf("opener calls = %q, want [blobs]", opened)
	}
	ctx := context.Background()
	if err := b.Put(ctx, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := b.Get(ctx, []byte("k"))
	if err != nil || string(got) != "v" {
		t.Fatalf("Get = %q, %v; want v", got, err)
	}
}

// Without an opener the factory cannot reach the node's provider, and says so
// with a typed error rather than InvalidBackendError.
func TestEmbeddedBackendFactory_ObjstoreWithoutOpener(t *testing.T) {
	_, err := NewEmbeddedBackendFactory().Create(DatabaseConfig{Name: "blobs", Backend: BackendObjstore}, t.TempDir())
	var notConfigured *ObjstoreNotConfiguredError
	if !errors.As(err, &notConfigured) {
		t.Fatalf("Create(objstore) = %v, want *ObjstoreNotConfiguredError", err)
	}
	if notConfigured.Name != "blobs" || notConfigured.Error() == "" {
		t.Fatalf("ObjstoreNotConfiguredError = %+v", notConfigured)
	}
	var invalid *InvalidBackendError
	if errors.As(err, &invalid) {
		t.Fatal("objstore is a known backend, not InvalidBackendError")
	}
}

// An opener that fails is reported as a StorageCreateError naming the backend.
func TestEmbeddedBackendFactory_ObjstoreOpenerFails(t *testing.T) {
	boom := errors.New("bucket unreachable")
	f := NewEmbeddedBackendFactory(WithObjstoreOpener(func(string, string) (storage.Storage, ObjstoreReclaim, error) {
		return nil, nil, boom
	}))
	_, err := f.Create(DatabaseConfig{Name: "blobs", Backend: BackendObjstore}, t.TempDir())
	var createErr *StorageCreateError
	if !errors.As(err, &createErr) || createErr.Backend != string(BackendObjstore) || !errors.Is(err, boom) {
		t.Fatalf("Create = %v, want StorageCreateError wrapping the opener's error", err)
	}
}

// TestEmbeddedBackendFactory_ObjstoreOpenerWithoutReclaim checks an opener
// that hands back no reclaim fails the open: such a database could never be
// dropped.
func TestEmbeddedBackendFactory_ObjstoreOpenerWithoutReclaim(t *testing.T) {
	f := NewEmbeddedBackendFactory(WithObjstoreOpener(func(string, string) (storage.Storage, ObjstoreReclaim, error) {
		e, err := objstore.New("memory", map[string]string{})
		return e, nil, err
	}))
	_, err := f.Create(DatabaseConfig{Name: "blobs", Backend: BackendObjstore}, t.TempDir())
	var createErr *StorageCreateError
	if !errors.As(err, &createErr) || !errors.Is(err, errObjstoreNoReclaim) {
		t.Fatalf("Create = %v, want StorageCreateError for the missing reclaim", err)
	}
}

// localObjstoreOpener opens every database on one local root beneath the
// object prefix "<name>/", as databases sharing a bucket are, and returns the
// engine's Destroy as the reclaim.
func localObjstoreOpener(root string) ObjstoreOpener {
	return func(name, _ string) (storage.Storage, ObjstoreReclaim, error) {
		e, err := objstore.New("local", map[string]string{"path": root}, objstore.WithObjectPrefix(name+"/"))
		if err != nil {
			return nil, nil, err
		}
		return e, e.Destroy, nil
	}
}

func newObjstoreManager(t *testing.T, open ObjstoreOpener) *Manager {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := newCatalogManager(logger, t.TempDir(), WithBackendFactory(NewEmbeddedBackendFactory(WithObjstoreOpener(open))))
	t.Cleanup(func() { _ = m.Close() })
	return m
}

// objectFiles returns the regular files beneath dir.
func objectFiles(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return files
}

// TestManager_DropReclaimsTheObjstoreObjects checks a Drop deletes every
// object of an objstore database, so a database recreated under the same name
// and prefix does not see the old keys.
func TestManager_DropReclaimsTheObjstoreObjects(t *testing.T) {
	root := t.TempDir()
	m := newObjstoreManager(t, localObjstoreOpener(root))
	ctx := context.Background()
	cfg := DatabaseConfig{Name: "blobs", Backend: BackendObjstore}

	db, err := m.Create(cfg)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := db.Backend().Put(ctx, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := m.Drop(ctx, "blobs"); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if left := objectFiles(t, root); len(left) != 0 {
		t.Fatalf("objects left after Drop: %q", left)
	}

	db, err = m.Create(cfg)
	if err != nil {
		t.Fatalf("recreate: %v", err)
	}
	got, err := db.Backend().Get(ctx, []byte("k"))
	if err != nil || got != nil {
		t.Fatalf("Get after recreate = %q, %v; want the dropped key absent", got, err)
	}
}

// TestManager_DropReclaimsABarrierEnabledObjstoreDatabase: the barrier wraps
// the objstore backend, and the drop must still reach the reclaim beneath it
// rather than report success over the objects it left.
func TestManager_DropReclaimsABarrierEnabledObjstoreDatabase(t *testing.T) {
	root := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := newCatalogManager(logger, t.TempDir(),
		WithBackendFactory(NewEmbeddedBackendFactory(WithObjstoreOpener(localObjstoreOpener(root)))),
		WithBarrierManager(newTestBarrierManager(t)))
	t.Cleanup(func() { _ = m.Close() })
	ctx := context.Background()
	cfg := DatabaseConfig{
		Name:    "sealed-blobs",
		Backend: BackendObjstore,
		DataDir: t.TempDir(),
		Barrier: &DatabaseBarrierConfig{Enabled: true, Mode: "passphrase"},
	}

	db, err := m.CreateWithCredentials(ctx, cfg, testCreds())
	if err != nil {
		t.Fatalf("CreateWithCredentials: %v", err)
	}
	if _, ok := db.Backend().(*BarrierBackend); !ok {
		t.Fatalf("Backend() = %T, want the barrier wrapper", db.Backend())
	}
	if err := db.Backend().Put(ctx, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if len(objectFiles(t, root)) == 0 {
		t.Fatal("no object written before Drop")
	}
	if err := m.Drop(ctx, "sealed-blobs"); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if left := objectFiles(t, root); len(left) != 0 {
		t.Fatalf("objects left after Drop: %q", left)
	}
}

// TestManager_DropFailsWhenTheReclaimFails checks a Drop whose objects cannot
// be deleted fails rather than reporting success, and that the retried drop
// completes once the reclaim succeeds.
func TestManager_DropFailsWhenTheReclaimFails(t *testing.T) {
	boom := errors.New("bucket unreachable")
	failing := true
	m := newObjstoreManager(t, func(string, string) (storage.Storage, ObjstoreReclaim, error) {
		e, err := objstore.New("memory", map[string]string{})
		if err != nil {
			return nil, nil, err
		}
		return e, func() error {
			if failing {
				return boom
			}
			return e.Destroy()
		}, nil
	})
	ctx := context.Background()
	if _, err := m.Create(DatabaseConfig{Name: "blobs", Backend: BackendObjstore}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	err := m.Drop(ctx, "blobs")
	var dropErr *DropError
	if !errors.As(err, &dropErr) || !errors.Is(err, boom) {
		t.Fatalf("Drop = %v, want DropError wrapping the reclaim's error", err)
	}
	failing = false
	if err := m.Drop(ctx, "blobs"); err != nil {
		t.Fatalf("retried Drop: %v", err)
	}
}

func TestObjstoreRaftModeError_NamesTheReason(t *testing.T) {
	err := &ObjstoreRaftModeError{Name: "blobs"}
	if msg := err.Error(); msg == "" || !strings.Contains(msg, ObjstoreRaftModeReason) {
		t.Fatalf("Error() = %q, want the hot-apply-path reason", msg)
	}
}
