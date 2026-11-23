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
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
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

// failDeleteOnceCatalog fails the first Delete with errCatalogDeleteFault and
// passes every other call to the catalog it wraps: a drop that stops after
// the reclamation and before the catalog record is gone.
type failDeleteOnceCatalog struct {
	database.Catalog
	failed atomic.Bool
}

var errCatalogDeleteFault = errors.New("catalog delete fault")

func (c *failDeleteOnceCatalog) Delete(ctx context.Context, tenantID, name, incarnation, dropClaim string) error {
	if c.failed.CompareAndSwap(false, true) {
		return errCatalogDeleteFault
	}
	return c.Catalog.Delete(ctx, tenantID, name, incarnation, dropClaim)
}

// TestManager_DropResumesAfterReclaimWithForeignObject is the #1577 Codex
// round-3 P1: a drop that reclaimed a namespace holding a foreign object and
// then stopped (the catalog delete failed) must be resumable, on the same
// manager and after a restart. Reclaim keeps the owner marker while the
// foreign object remains, and the database's owner reopens the namespace to
// finish the drop.
func TestManager_DropResumesAfterReclaimWithForeignObject(t *testing.T) {
	cases := map[string]func(t *testing.T, mgr *database.Manager, dataDir string, cat database.Catalog) *database.Manager{
		"retry on the same manager": func(_ *testing.T, mgr *database.Manager, _ string, _ database.Catalog) *database.Manager {
			return mgr
		},
		"after a restart": func(t *testing.T, mgr *database.Manager, dataDir string, cat database.Catalog) *database.Manager {
			if err := mgr.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			restarted := newManager(t, dataDir)
			restarted.SetCatalog(cat)
			return restarted
		},
	}
	for name, resume := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			root := filepath.Join(t.TempDir(), "objects")
			dataDir := t.TempDir()
			mgr := newManager(t, dataDir)
			db, err := mgr.Create(localObjstore("blobs", root))
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			tenantID := db.TenantID()
			if err := db.Backend().Put(ctx, []byte("key"), []byte("v")); err != nil {
				t.Fatalf("Put: %v", err)
			}
			foreign := filepath.Join(root, "child", "x")
			if err := os.MkdirAll(filepath.Dir(foreign), 0o700); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
			if err := os.WriteFile(foreign, []byte("foreign"), 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			cat := &failDeleteOnceCatalog{Catalog: databasetest.Catalog(dataDir)}
			mgr.SetCatalog(cat)

			if err := mgr.Drop(ctx, "blobs"); !errors.Is(err, errCatalogDeleteFault) {
				t.Fatalf("Drop = %v, want the catalog delete fault", err)
			}
			resumed := resume(t, mgr, dataDir, cat)
			if err := resumed.DropForTenant(ctx, "blobs"); err != nil {
				t.Fatalf("resumed drop: %v", err)
			}

			var notFound *database.NotFoundError
			if _, err := cat.Get(ctx, tenantID, "blobs"); !errors.As(err, &notFound) {
				t.Fatalf("catalog Get after the drop = %v, want *NotFoundError", err)
			}
			foundForeign := false
			for _, path := range objects(t, root) {
				switch {
				case path == foreign:
					foundForeign = true
				case filepath.Dir(path) == root && strings.HasPrefix(filepath.Base(path), "qrdb-objstore-owner"):
					// The owner marker (and the local provider's metadata
					// file for it) stays while the foreign object does.
				default:
					t.Errorf("the drop left %s", path)
				}
			}
			if !foundForeign {
				t.Fatal("the drop deleted the foreign object")
			}
		})
	}
}

// TestManager_DropKeepsNamespaceUntilCatalogDelete is PR #1577 Codex round
// 4 (A): a drop whose catalog delete fails after the reclamation still owns
// the namespace, so a second database configured onto it is refused, the
// drop's retry finishes, and only then does the namespace open for another
// database.
func TestManager_DropKeepsNamespaceUntilCatalogDelete(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "objects")
	dataDir := t.TempDir()
	mgr := newManager(t, dataDir)
	db, err := mgr.Create(localObjstore("blobs", root))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := db.Backend().Put(ctx, []byte("key"), []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	cat := &failDeleteOnceCatalog{Catalog: databasetest.Catalog(dataDir)}
	mgr.SetCatalog(cat)

	if err := mgr.Drop(ctx, "blobs"); !errors.Is(err, errCatalogDeleteFault) {
		t.Fatalf("Drop = %v, want the catalog delete fault", err)
	}
	var owned *objstore.NamespaceOwnedError
	if _, err := mgr.Create(localObjstore("other", root)); !errors.As(err, &owned) {
		t.Fatalf("Create onto the dropping namespace = %v, want *NamespaceOwnedError", err)
	}
	if err := mgr.DropForTenant(ctx, "blobs"); err != nil {
		t.Fatalf("drop retry: %v", err)
	}
	if left := objects(t, root); len(left) != 0 {
		t.Fatalf("the drop left %v", left)
	}
	other, err := mgr.Create(localObjstore("other", root))
	if err != nil {
		t.Fatalf("Create after the drop: %v", err)
	}
	if err := other.Backend().Put(ctx, []byte("key"), []byte("w")); err != nil {
		t.Fatalf("Put: %v", err)
	}
}

// errUnfenceFault fails a create after its backend opened.
var errUnfenceFault = errors.New("injected unfence fault")

// failUnfenceReclaimer refuses UnfenceDatabaseKeyspace, the create step
// that runs after the database's backend has opened (and claimed its
// namespace).
type failUnfenceReclaimer struct{}

func (failUnfenceReclaimer) ReclaimDatabaseKeyspace(context.Context, string, string, string, string) error {
	return nil
}

func (failUnfenceReclaimer) FenceDatabaseKeyspace(context.Context, string, string, string, string) error {
	return nil
}

func (failUnfenceReclaimer) UnfenceDatabaseKeyspace(context.Context, string, string, string, string) error {
	return errUnfenceFault
}

// TestManager_FailedCreateReleasesFreshNamespace is PR #1577 Codex round 4
// (B): a create that fails after its opener claimed a new namespace
// releases it, so another database may use it; a create onto a namespace
// that already held data leaves that data as it was.
func TestManager_FailedCreateReleasesFreshNamespace(t *testing.T) {
	t.Run("fresh namespace is released", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "objects")
		mgr := newManager(t, t.TempDir())
		mgr.SetKeyspaceReclaimer(failUnfenceReclaimer{})
		if _, err := mgr.Create(localObjstore("blobs", root)); !errors.Is(err, errUnfenceFault) {
			t.Fatalf("Create = %v, want the unfence fault", err)
		}
		if left := objects(t, root); len(left) != 0 {
			t.Fatalf("the failed create left %v", left)
		}
		mgr.SetKeyspaceReclaimer(nil)
		if _, err := mgr.Create(localObjstore("other", root)); err != nil {
			t.Fatalf("Create after the failed create: %v", err)
		}
	})
	t.Run("pre-existing data is kept", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "objects")
		unowned, err := objstore.New("local", map[string]string{"path": root})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := unowned.Set([]byte("k"), []byte("v")); err != nil {
			t.Fatalf("Set: %v", err)
		}
		if err := unowned.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		before := objects(t, root)
		mgr := newManager(t, t.TempDir())
		mgr.SetKeyspaceReclaimer(failUnfenceReclaimer{})
		if _, err := mgr.Create(localObjstore("blobs", root)); !errors.Is(err, errUnfenceFault) {
			t.Fatalf("Create = %v, want the unfence fault", err)
		}
		after := objects(t, root)
		for _, path := range before {
			found := false
			for _, p := range after {
				found = found || p == path
			}
			if !found {
				t.Errorf("the failed create deleted %s", path)
			}
		}
	})
}

// TestManager_FailedCreateOntoExistingDataDropsOwnerMarker: a create that
// fails after its opener claimed a namespace that already held data gives
// the claim back. The data and the format marker stay, the owner marker it
// wrote goes, so a differently named database may open the namespace.
func TestManager_FailedCreateOntoExistingDataDropsOwnerMarker(t *testing.T) {
	root := filepath.Join(t.TempDir(), "objects")
	unowned, err := objstore.New("local", map[string]string{"path": root})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := unowned.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := unowned.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	before := objects(t, root)
	mgr := newManager(t, t.TempDir())
	mgr.SetKeyspaceReclaimer(failUnfenceReclaimer{})
	if _, err := mgr.Create(localObjstore("blobs", root)); !errors.Is(err, errUnfenceFault) {
		t.Fatalf("Create = %v, want the unfence fault", err)
	}
	if after := objects(t, root); !slices.Equal(after, before) {
		t.Fatalf("the failed create left %v, want %v", after, before)
	}
	mgr.SetKeyspaceReclaimer(nil)
	if _, err := mgr.Create(localObjstore("other", root)); err != nil {
		t.Fatalf("Create of another database after the failed create: %v", err)
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
