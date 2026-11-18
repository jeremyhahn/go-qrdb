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

package objstoredb_test

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/database/objstoredb"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
)

// localObjstore is a `backend: objstore` database on go-objstore's "local"
// provider rooted at root, so its objects outlive one open.
func localObjstore(name, root string) database.DatabaseConfig {
	return database.DatabaseConfig{Name: name, Backend: database.BackendObjstore, Objstore: &database.DatabaseObjstoreConfig{
		Provider: "local",
		Settings: map[string]string{"path": root},
	}}
}

func newManager(t *testing.T, dataDir string) *database.Manager {
	t.Helper()
	mgr := databasetest.NewManager(nil, dataDir, database.WithBackendFactory(objstoredb.NewBackendFactory()))
	t.Cleanup(func() { _ = mgr.Close() })
	return mgr
}

// The catalog record carries the database's provider, so a manager opened
// after a restart reopens the same provider and serves what was written.
func TestManager_ObjstoreDatabaseReopensAfterRestart(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	cfg := localObjstore("blobs", filepath.Join(t.TempDir(), "objects"))

	first := newManager(t, dataDir)
	db, err := first.Create(cfg)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := db.Backend().Put(ctx, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second := newManager(t, dataDir)
	reopened, err := second.Open(ctx, "", "blobs")
	if err != nil {
		t.Fatalf("Open after restart: %v", err)
	}
	if got := reopened.Config().Objstore; got == nil || got.Provider != "local" || got.Settings["path"] != cfg.Objstore.Settings["path"] {
		t.Fatalf("reopened Objstore = %+v, want %+v", got, cfg.Objstore)
	}
	got, err := reopened.Backend().Get(ctx, []byte("k"))
	if err != nil || string(got) != "v" {
		t.Fatalf("Get(k) after restart = %q, %v; want \"v\"", got, err)
	}
}

// objects lists the files the "local" provider holds under root.
func objects(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return found
}

// A drop deletes the database's objects from its provider before it reports
// the database dropped: none is left behind, and a database created again on
// the same provider starts empty.
func TestManager_DropReclaimsProviderObjects(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "objects")
	cfg := localObjstore("blobs", root)
	mgr := newManager(t, t.TempDir())

	db, err := mgr.Create(cfg)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, k := range []string{"a", "b", string(make([]byte, 400))} {
		if err := db.Backend().Put(ctx, []byte("key-"+k), []byte("v")); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	if len(objects(t, root)) == 0 {
		t.Fatal("no objects written to the provider")
	}

	if err := mgr.Drop(ctx, "blobs"); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if left := objects(t, root); len(left) != 0 {
		t.Fatalf("Drop left provider objects: %v", left)
	}
	if left := objects(t, root+".journal"); len(left) != 0 {
		t.Fatalf("Drop left journal objects: %v", left)
	}

	again, err := mgr.Create(cfg)
	if err != nil {
		t.Fatalf("Create after drop: %v", err)
	}
	keys, err := again.Backend().List(ctx, "")
	if err != nil || len(keys) != 0 {
		t.Fatalf("List after re-create = %q, %v; want no keys", keys, err)
	}
}

// TestManager_DropReclaimsAClosedOrFailedBackendsObjects pins that a drop
// reclaims an object-storage database's objects whatever its backend's state:
// a database closed before the drop, and one whose backend was released under
// it (a failed backend, still listed as open), leave nothing in the provider.
func TestManager_DropReclaimsAClosedOrFailedBackendsObjects(t *testing.T) {
	cases := map[string]func(t *testing.T, db *database.Database){
		"database closed": func(t *testing.T, db *database.Database) {
			if err := db.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
		},
		"backend failed": func(t *testing.T, db *database.Database) {
			if err := db.Backend().Close(); err != nil {
				t.Fatalf("backend Close: %v", err)
			}
		},
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			root := filepath.Join(t.TempDir(), "objects")
			mgr := newManager(t, t.TempDir())

			db, err := mgr.Create(localObjstore("blobs", root))
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if err := db.Backend().Put(ctx, []byte("key"), []byte("v")); err != nil {
				t.Fatalf("Put: %v", err)
			}
			breakIt(t, db)

			if err := mgr.Drop(ctx, "blobs"); err != nil {
				t.Fatalf("Drop: %v", err)
			}
			if left := objects(t, root); len(left) != 0 {
				t.Fatalf("Drop left provider objects: %v", left)
			}
			if left := objects(t, root+".journal"); len(left) != 0 {
				t.Fatalf("Drop left journal objects: %v", left)
			}
		})
	}
}

// TestManager_SecondDatabaseCannotReachAnothersNamespace pins that a database
// owns its provider namespace: a second database configured onto the first's
// path is refused, cannot read or reclaim its objects, and the first still
// serves them; once the first is dropped the path may be reused.
func TestManager_SecondDatabaseCannotReachAnothersNamespace(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "objects")
	mgr := newManager(t, t.TempDir())

	first, err := mgr.Create(localObjstore("first", root))
	if err != nil {
		t.Fatalf("Create(first): %v", err)
	}
	if err := first.Backend().Put(ctx, []byte("key"), []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	_, err = mgr.Create(localObjstore("second", root))
	// Open in this process by first, the namespace refuses second before
	// its owner marker is read; either refusal keeps first's objects.
	var owned *objstore.NamespaceOwnedError
	var inUse *objstore.NamespaceInUseError
	if !errors.As(err, &owned) && !errors.As(err, &inUse) {
		t.Fatalf("Create(second) on first's path = %v, want an objstore owner refusal", err)
	}
	if _, err := mgr.Get("second"); err == nil {
		t.Fatal("the refused database is served")
	}
	got, err := first.Backend().Get(ctx, []byte("key"))
	if err != nil || string(got) != "v" {
		t.Fatalf("first Get = %q, %v; want v", got, err)
	}

	if err := mgr.Drop(ctx, "first"); err != nil {
		t.Fatalf("Drop(first): %v", err)
	}
	if _, err := mgr.Create(localObjstore("second", root)); err != nil {
		t.Fatalf("Create(second) after first's drop: %v", err)
	}
}
