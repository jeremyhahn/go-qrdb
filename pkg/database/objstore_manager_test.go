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

package database_test

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
	objfactory "github.com/jeremyhahn/go-objstore/pkg/factory"

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
			// The marker the foreign object keeps is a release not done:
			// its ledger entry stays for the retry.
			if len(releaseLedgerFiles(t, dataDir)) == 0 {
				t.Fatal("a release that kept the owner marker forgot its ledger entry")
			}

			// Once the foreign object is gone, the boot's retry finishes the
			// release and a new database can claim the namespace.
			if err := os.RemoveAll(filepath.Join(root, "child")); err != nil {
				t.Fatalf("RemoveAll: %v", err)
			}
			if err := resumed.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			next := newManager(t, dataDir)
			if err := next.OpenAll(ctx); err != nil {
				t.Fatalf("OpenAll: %v", err)
			}
			if hasOwnerMarker(t, root) {
				t.Fatal("the retried release kept the owner marker")
			}
			if left := releaseLedgerFiles(t, dataDir); len(left) != 0 {
				t.Fatalf("the completed release was not forgotten: %v", left)
			}
			if _, err := next.Create(localObjstore("other", root)); err != nil {
				t.Fatalf("Create onto the released namespace: %v", err)
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

// errReleaseFault fails the delete of a namespace's owner marker.
var errReleaseFault = errors.New("injected owner marker delete fault")

// releaseFaultProvider is a local-directory provider whose store fails the
// owner marker's delete once, when the test arms it: the one call a drop's
// namespace release makes that can leave the namespace reserved.
const releaseFaultProvider = "release-fault-local"

var (
	releaseFaultArmed    atomic.Bool
	registerReleaseFault sync.Once
)

type releaseFaultStore struct {
	objcommon.Storage
}

func (s releaseFaultStore) Delete(name string) error {
	if name == "qrdb-objstore-owner" && releaseFaultArmed.CompareAndSwap(true, false) {
		return errReleaseFault
	}
	if name == "qrdb-objstore-owner" && releaseLostArmed.CompareAndSwap(true, false) {
		// The delete lands but its answer is lost: the caller is told
		// it failed although the marker is gone.
		if err := s.Storage.Delete(name); err != nil {
			return err
		}
		return errReleaseFault
	}
	return s.Storage.Delete(name)
}

// releaseLostArmed makes the owner marker's next delete land and then
// report errReleaseFault (releaseFaultStore.Delete).
var releaseLostArmed atomic.Bool

// journalReadFails makes every commit journal read fail while set: an open
// that claimed its namespace and then cannot recover the journal.
var journalReadFails atomic.Bool

func (s releaseFaultStore) Get(name string) (io.ReadCloser, error) {
	if name == "qrdb-objstore-journal" && journalReadFails.Load() {
		return nil, errReleaseFault
	}
	return s.Storage.Get(name)
}

// TestManager_FailedCreateClaimUndoIsRetried: a create whose open claimed
// the namespace, failed, and could not give the claim back records a release
// entry for its incarnation, and the next retry releases the namespace.
func TestManager_FailedCreateClaimUndoIsRetried(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "objects")
	otherRoot := filepath.Join(base, "other")
	dataDir := t.TempDir()
	mgr := newManager(t, dataDir)
	t.Cleanup(func() {
		journalReadFails.Store(false)
		releaseFaultArmed.Store(false)
	})
	journalReadFails.Store(true)
	releaseFaultArmed.Store(true)
	var undo *objstore.ClaimUndoError
	if _, err := mgr.Create(releaseFaultObjstore(t, "blobs", root)); !errors.As(err, &undo) {
		t.Fatalf("Create = %v, want *objstore.ClaimUndoError", err)
	}
	journalReadFails.Store(false)
	if !hasOwnerMarker(t, root) {
		t.Fatal("the failed undo left no owner marker; the test fault did not hold")
	}
	if len(releaseLedgerFiles(t, dataDir)) == 0 {
		t.Fatal("the failed create recorded no release entry for its claim")
	}
	// The next object-storage create retries the recorded release.
	if _, err := mgr.Create(localObjstore("other", otherRoot)); err != nil {
		t.Fatalf("Create other: %v", err)
	}
	if left := releaseLedgerFiles(t, dataDir); len(left) != 0 {
		t.Fatalf("the retried release kept %v", left)
	}
	if left := objects(t, root); len(left) != 0 {
		t.Fatalf("the retried release left %v", left)
	}
	if _, err := mgr.Create(releaseFaultObjstore(t, "blobs", root)); err != nil {
		t.Fatalf("Create after the retried release: %v", err)
	}
}

// TestManager_FailedCreateClaimUndoOnExistingDataKeepsData: a create onto a
// namespace holding data no database owns, whose open and claim undo both
// fail, records a release that, retried, removes only the owner marker: the
// pre-existing records and format marker survive.
func TestManager_FailedCreateClaimUndoOnExistingDataKeepsData(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "objects")
	otherRoot := filepath.Join(base, "other")
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
	dataDir := t.TempDir()
	mgr := newManager(t, dataDir)
	t.Cleanup(func() {
		journalReadFails.Store(false)
		releaseFaultArmed.Store(false)
	})
	journalReadFails.Store(true)
	releaseFaultArmed.Store(true)
	var undo *objstore.ClaimUndoError
	if _, err := mgr.Create(releaseFaultObjstore(t, "blobs", root)); !errors.As(err, &undo) {
		t.Fatalf("Create = %v, want *objstore.ClaimUndoError", err)
	}
	journalReadFails.Store(false)
	if !hasOwnerMarker(t, root) {
		t.Fatal("the failed undo left no owner marker; the test fault did not hold")
	}
	if len(releaseLedgerFiles(t, dataDir)) == 0 {
		t.Fatal("the failed create recorded no release entry for its claim")
	}
	// The next object-storage create retries the recorded release.
	if _, err := mgr.Create(localObjstore("other", otherRoot)); err != nil {
		t.Fatalf("Create other: %v", err)
	}
	if left := releaseLedgerFiles(t, dataDir); len(left) != 0 {
		t.Fatalf("the retried release kept %v", left)
	}
	if hasOwnerMarker(t, root) {
		t.Fatal("the retried release kept the owner marker")
	}
	if after := objects(t, root); !slices.Equal(after, before) {
		t.Fatalf("the retried release left %v, want the pre-existing %v", after, before)
	}
}

// fileContents maps every regular file under each of roots to its bytes.
func fileContents(t *testing.T, roots ...string) map[string]string {
	t.Helper()
	files := map[string]string{}
	for _, rootDir := range roots {
		root, err := os.OpenRoot(rootDir)
		if err != nil {
			t.Fatalf("OpenRoot %s: %v", rootDir, err)
		}
		err = fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			body, err := root.ReadFile(rel)
			if err != nil {
				return err
			}
			files[filepath.Join(rootDir, rel)] = string(body)
			return nil
		})
		if cerr := root.Close(); cerr != nil {
			t.Fatalf("Close root %s: %v", rootDir, cerr)
		}
		if err != nil {
			t.Fatalf("WalkDir %s: %v", rootDir, err)
		}
	}
	return files
}

// TestManager_ClaimOnlyReleaseOverCorruptJournal: a create onto existing,
// unowned data whose journal is corrupt fails on the journal, and the claim
// undo fails too, so a claim-only release is recorded. Every open of that
// namespace fails on the journal again, so the retry deletes the owner
// marker without opening the engine; the records and the journal stay byte
// for byte.
func TestManager_ClaimOnlyReleaseOverCorruptJournal(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "objects")
	otherRoot := filepath.Join(base, "other")
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
	// A journal no open can recover, written by the real provider where the
	// fault provider (not "local", so its journal is in the data tree)
	// reads it.
	journal, err := objfactory.NewStorage("local", map[string]string{"path": root}, objcommon.Credentials{})
	if err != nil {
		t.Fatalf("journal store: %v", err)
	}
	if err := journal.Put("qrdb-objstore-journal", strings.NewReader("not a journal image")); err != nil {
		t.Fatalf("Put journal: %v", err)
	}
	before := fileContents(t, root)

	dataDir := t.TempDir()
	mgr := newManager(t, dataDir)
	t.Cleanup(func() { releaseFaultArmed.Store(false) })
	releaseFaultArmed.Store(true)
	var corrupt *objstore.JournalCorruptError
	var undo *objstore.ClaimUndoError
	_, err = mgr.Create(releaseFaultObjstore(t, "blobs", root))
	if !errors.As(err, &undo) || !errors.As(err, &corrupt) {
		t.Fatalf("Create = %v, want *objstore.ClaimUndoError over *objstore.JournalCorruptError", err)
	}
	if !hasOwnerMarker(t, root) {
		t.Fatal("the failed undo left no owner marker; the test fault did not hold")
	}
	if len(releaseLedgerFiles(t, dataDir)) == 0 {
		t.Fatal("the failed create recorded no release entry for its claim")
	}

	// The next object-storage create retries the recorded release.
	if _, err := mgr.Create(localObjstore("other", otherRoot)); err != nil {
		t.Fatalf("Create other: %v", err)
	}
	if left := releaseLedgerFiles(t, dataDir); len(left) != 0 {
		t.Fatalf("the retried claim-only release kept %v", left)
	}
	if hasOwnerMarker(t, root) {
		t.Fatal("the retried claim-only release kept the owner marker")
	}
	after := fileContents(t, root)
	if len(after) != len(before) {
		t.Fatalf("the retried release left %d files, want the pre-existing %d", len(after), len(before))
	}
	for path, body := range before {
		if after[path] != body {
			t.Fatalf("the retried release changed %s", path)
		}
	}
}

// TestManager_FailedCreateReleaseClaimFailureIsRetried: an open that
// succeeded, a later create step that failed, and a claim give-back that
// failed too leave a release entry; the retry releases a fresh namespace
// whole and, on existing data, removes only the owner marker.
func TestManager_FailedCreateReleaseClaimFailureIsRetried(t *testing.T) {
	run := func(t *testing.T, root string, want func(t *testing.T, after []string)) {
		t.Helper()
		dataDir := t.TempDir()
		mgr := newManager(t, dataDir)
		t.Cleanup(func() { releaseFaultArmed.Store(false) })
		mgr.SetKeyspaceReclaimer(failUnfenceReclaimer{})
		releaseFaultArmed.Store(true)
		if _, err := mgr.Create(releaseFaultObjstore(t, "blobs", root)); !errors.Is(err, errUnfenceFault) {
			t.Fatalf("Create = %v, want the unfence fault", err)
		}
		if releaseFaultArmed.Load() {
			t.Fatal("the claim give-back did not fail; the test fault did not hold")
		}
		if !hasOwnerMarker(t, root) {
			t.Fatal("the failed give-back left no owner marker")
		}
		if len(releaseLedgerFiles(t, dataDir)) == 0 {
			t.Fatal("the failed create recorded no release entry for its claim")
		}
		mgr.SetKeyspaceReclaimer(nil)
		if _, err := mgr.Create(localObjstore("other", filepath.Join(t.TempDir(), "other"))); err != nil {
			t.Fatalf("Create other: %v", err)
		}
		if left := releaseLedgerFiles(t, dataDir); len(left) != 0 {
			t.Fatalf("the retried release kept %v", left)
		}
		if hasOwnerMarker(t, root) {
			t.Fatal("the retried release kept the owner marker")
		}
		want(t, objects(t, root))
	}
	t.Run("fresh namespace is released whole", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "objects")
		run(t, root, func(t *testing.T, after []string) {
			if len(after) != 0 {
				t.Fatalf("the retried release left %v", after)
			}
		})
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
		run(t, root, func(t *testing.T, after []string) {
			if !slices.Equal(after, before) {
				t.Fatalf("the retried release left %v, want the pre-existing %v", after, before)
			}
		})
	})
}

// TestManager_ReleaseRetryKeepsEntryOfOwnIncarnation: a failed create whose
// claim give-back and catalog delete both fail leaves its own record live,
// of the entry's incarnation and not dropping. That record is not a
// re-created name: the retry keeps the entry and reclaims nothing, and the
// record's drop releases the namespace and forgets the entry.
func TestManager_ReleaseRetryKeepsEntryOfOwnIncarnation(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	root := filepath.Join(base, "objects")
	dataDir := t.TempDir()
	mgr := newManager(t, dataDir)
	cat := &failDeleteOnceCatalog{Catalog: databasetest.Catalog(dataDir)}
	mgr.SetCatalog(cat)
	mgr.SetKeyspaceReclaimer(failUnfenceReclaimer{})
	t.Cleanup(func() { releaseFaultArmed.Store(false) })
	releaseFaultArmed.Store(true)
	var orphan *database.CatalogOrphanError
	if _, err := mgr.Create(releaseFaultObjstore(t, "blobs", root)); !errors.As(err, &orphan) {
		t.Fatalf("Create = %v, want *database.CatalogOrphanError", err)
	}
	if releaseFaultArmed.Load() {
		t.Fatal("the claim give-back did not fail; the test fault did not hold")
	}
	entries := releaseLedgerFiles(t, dataDir)
	if len(entries) == 0 {
		t.Fatal("the failed create recorded no release entry for its claim")
	}
	before := objects(t, root)
	mgr.SetKeyspaceReclaimer(nil)
	// The next object-storage create retries the recorded release.
	if _, err := mgr.Create(localObjstore("other", filepath.Join(base, "other"))); err != nil {
		t.Fatalf("Create other: %v", err)
	}
	if left := releaseLedgerFiles(t, dataDir); !slices.Equal(left, entries) {
		t.Fatalf("the retry left the entries %v, want %v kept", left, entries)
	}
	if after := objects(t, root); !slices.Equal(after, before) {
		t.Fatalf("the retry left %v, want %v untouched", after, before)
	}
	if err := mgr.DropForTenant(ctx, "blobs"); err != nil {
		t.Fatalf("drop of the failed create's record: %v", err)
	}
	if left := releaseLedgerFiles(t, dataDir); len(left) != 0 {
		t.Fatalf("the drop kept %v", left)
	}
	if hasOwnerMarker(t, root) {
		t.Fatal("the drop kept the owner marker")
	}
}

// afterDeleteCatalog runs after once, the first time a catalog delete
// succeeds: a drop paused between its catalog delete and its namespace
// release.
type afterDeleteCatalog struct {
	database.Catalog
	after func()
}

func (c *afterDeleteCatalog) Delete(ctx context.Context, tenantID, name, incarnation, dropClaim string) error {
	if err := c.Catalog.Delete(ctx, tenantID, name, incarnation, dropClaim); err != nil {
		return err
	}
	if after := c.after; after != nil {
		c.after = nil
		after()
	}
	return nil
}

// releaseLedgerFiles lists the release entries dataDir's Manager holds.
func releaseLedgerFiles(t *testing.T, dataDir string) []string {
	t.Helper()
	dir := filepath.Join(dataDir, "releasing")
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return objects(t, dir)
}

// hasOwnerMarker reports whether root holds an owner marker.
func hasOwnerMarker(t *testing.T, root string) bool {
	t.Helper()
	return slices.ContainsFunc(objects(t, root), func(p string) bool {
		return strings.Contains(p, "qrdb-objstore-owner")
	})
}

// TestManager_ReleaseSparesSuccessorIncarnation: the owner a namespace
// records names the database incarnation, so a database of the same tenant
// and name created by another Manager while an old drop's release is pending
// is refused, and once it holds the namespace the old incarnation's release
// reclaims nothing of it.
func TestManager_ReleaseSparesSuccessorIncarnation(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	root := filepath.Join(base, "objects")
	otherRoot := filepath.Join(base, "other")
	dataA, dataB := t.TempDir(), t.TempDir()
	t.Cleanup(func() { releaseLostArmed.Store(false) })
	mgrA := newManager(t, dataA)
	if _, err := mgrA.Create(releaseFaultObjstore(t, "blobs", root)); err != nil {
		t.Fatalf("Create on A: %v", err)
	}
	mgrB := newManager(t, dataB)
	var hookErr error
	hooked := false
	mgrA.SetCatalog(&afterDeleteCatalog{Catalog: databasetest.Catalog(dataA), after: func() {
		hooked = true
		_, hookErr = mgrB.Create(releaseFaultObjstore(t, "blobs", root))
	}})
	releaseLostArmed.Store(true)
	if err := mgrA.Drop(ctx, "blobs"); err != nil {
		t.Fatalf("Drop on A: %v", err)
	}
	var owned *objstore.NamespaceOwnedError
	if !hooked || !errors.As(hookErr, &owned) {
		t.Fatalf("successor create during the pending release = %v (ran %v), want *NamespaceOwnedError", hookErr, hooked)
	}
	if releaseLostArmed.Load() {
		t.Fatal("the drop never deleted the owner marker")
	}
	if len(releaseLedgerFiles(t, dataA)) == 0 {
		t.Fatal("the drop whose release reported a failure kept no ledger entry")
	}

	successor, err := mgrB.Create(releaseFaultObjstore(t, "blobs", root))
	if err != nil {
		t.Fatalf("successor Create after the release: %v", err)
	}
	if err := successor.Backend().Put(ctx, []byte("key"), []byte("w")); err != nil {
		t.Fatalf("successor Put: %v", err)
	}
	// A's next object-storage create retries the recorded release of the
	// dropped incarnation.
	if _, err := mgrA.Create(localObjstore("other", otherRoot)); err != nil {
		t.Fatalf("Create on A: %v", err)
	}
	got, err := successor.Backend().Get(ctx, []byte("key"))
	if err != nil || string(got) != "w" {
		t.Fatalf("successor Get(key) after the old release = %q, %v; want \"w\"", got, err)
	}
	if !hasOwnerMarker(t, root) {
		t.Fatal("the old incarnation's release removed the successor's owner marker")
	}
	if left := releaseLedgerFiles(t, dataA); len(left) != 0 {
		t.Fatalf("the release of a namespace another incarnation owns was not forgotten: %v", left)
	}
}

// noopReclaimer is a data plane whose keyspaces hold nothing of the
// object-storage databases these tests drive: every fence, reclaim and
// unfence lands.
type noopReclaimer struct{}

func (noopReclaimer) ReclaimDatabaseKeyspace(context.Context, string, string, string, string) error {
	return nil
}

func (noopReclaimer) FenceDatabaseKeyspace(context.Context, string, string, string, string) error {
	return nil
}

func (noopReclaimer) UnfenceDatabaseKeyspace(context.Context, string, string, string, string) error {
	return nil
}

// withReclaimer runs fn standalone (no reclaimer) and with a data plane
// attached.
func withReclaimer(t *testing.T, fn func(t *testing.T, attach func(*database.Manager))) {
	t.Run("standalone", func(t *testing.T) { fn(t, func(*database.Manager) {}) })
	t.Run("reclaimer", func(t *testing.T) {
		fn(t, func(m *database.Manager) { m.SetKeyspaceReclaimer(noopReclaimer{}) })
	})
}

// TestManager_ProvisionedReleaseSparesSuccessor: a provisioned
// object-storage database owns its namespace as its own incarnation, so a
// successor provisioned onto the same namespace after the drop is never
// reclaimed by the dropped one's release.
func TestManager_ProvisionedReleaseSparesSuccessor(t *testing.T) {
	withReclaimer(t, func(t *testing.T, attach func(*database.Manager)) {
		ctx := context.Background()
		base := t.TempDir()
		root := filepath.Join(base, "objects")
		otherRoot := filepath.Join(base, "other")
		dataA, dataB := t.TempDir(), t.TempDir()
		t.Cleanup(func() { releaseLostArmed.Store(false) })
		mgrA := newManager(t, dataA)
		attach(mgrA)
		if _, err := mgrA.Provision(ctx, releaseFaultObjstore(t, "blobs", root)); err != nil {
			t.Fatalf("Provision on A: %v", err)
		}
		mgrB := newManager(t, dataB)
		attach(mgrB)
		var hookErr error
		hooked := false
		mgrA.SetCatalog(&afterDeleteCatalog{Catalog: databasetest.Catalog(dataA), after: func() {
			hooked = true
			_, hookErr = mgrB.Provision(ctx, releaseFaultObjstore(t, "blobs", root))
		}})
		releaseLostArmed.Store(true)
		if err := mgrA.Drop(ctx, "blobs"); err != nil {
			t.Fatalf("Drop on A: %v", err)
		}
		var owned *objstore.NamespaceOwnedError
		if !hooked || !errors.As(hookErr, &owned) {
			t.Fatalf("successor Provision during the pending release = %v (ran %v), want *NamespaceOwnedError", hookErr, hooked)
		}
		if len(releaseLedgerFiles(t, dataA)) == 0 {
			t.Fatal("the drop whose release reported a failure kept no ledger entry")
		}

		successor, err := mgrB.Provision(ctx, releaseFaultObjstore(t, "blobs", root))
		if err != nil {
			t.Fatalf("successor Provision after the release: %v", err)
		}
		if err := successor.Backend().Put(ctx, []byte("key"), []byte("w")); err != nil {
			t.Fatalf("successor Put: %v", err)
		}
		// A's next object-storage create retries the recorded release of
		// the dropped incarnation.
		if _, err := mgrA.Provision(ctx, localObjstore("other", otherRoot)); err != nil {
			t.Fatalf("Provision on A: %v", err)
		}
		got, err := successor.Backend().Get(ctx, []byte("key"))
		if err != nil || string(got) != "w" {
			t.Fatalf("successor Get(key) after the old release = %q, %v; want \"w\"", got, err)
		}
		if !hasOwnerMarker(t, root) {
			t.Fatal("the old incarnation's release removed the successor's owner marker")
		}
		if left := releaseLedgerFiles(t, dataA); len(left) != 0 {
			t.Fatalf("the release of a namespace another incarnation owns was not forgotten: %v", left)
		}
	})
}

// TestManager_ProvisionedObjstoreKeepsIncarnationAcrossRestart: a
// provisioned object-storage database gets its own incarnation, records it,
// and a restart's Provision reopens it as the same owner.
func TestManager_ProvisionedObjstoreKeepsIncarnationAcrossRestart(t *testing.T) {
	withReclaimer(t, func(t *testing.T, attach func(*database.Manager)) {
		ctx := context.Background()
		dataDir := t.TempDir()
		cfg := localObjstore("blobs", filepath.Join(t.TempDir(), "objects"))

		first := newManager(t, dataDir)
		attach(first)
		db, err := first.Provision(ctx, cfg)
		if err != nil {
			t.Fatalf("Provision: %v", err)
		}
		incarnation := db.Incarnation()
		if incarnation == "" {
			t.Fatal("a provisioned object-storage database got the empty shared incarnation")
		}
		if again, err := first.Provision(ctx, cfg); err != nil || again != db {
			t.Fatalf("second Provision = %p, %v; want the same database %p", again, err, db)
		}
		if err := db.Backend().Put(ctx, []byte("k"), []byte("v")); err != nil {
			t.Fatalf("Put: %v", err)
		}
		if err := first.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		second := newManager(t, dataDir)
		attach(second)
		reopened, err := second.Provision(ctx, cfg)
		if err != nil {
			t.Fatalf("Provision after restart: %v", err)
		}
		if reopened.Incarnation() != incarnation {
			t.Fatalf("incarnation after restart = %q, want %q", reopened.Incarnation(), incarnation)
		}
		got, err := reopened.Backend().Get(ctx, []byte("k"))
		if err != nil || string(got) != "v" {
			t.Fatalf("Get(k) after restart = %q, %v; want \"v\"", got, err)
		}
		if again, err := second.Provision(ctx, cfg); err != nil || again != reopened {
			t.Fatalf("Provision after the reopen = %p, %v; want the same database %p", again, err, reopened)
		}
	})
}

// TestManager_ReleaseRetrySparesSuccessorIncarnation: a release retried from
// the ledger opens the namespace as the dropped incarnation, so a successor
// of the same tenant and name on the same namespace keeps its objects and
// owner marker, and the entry is forgotten.
func TestManager_ReleaseRetrySparesSuccessorIncarnation(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	root := filepath.Join(base, "objects")
	dataA, dataB := t.TempDir(), t.TempDir()
	t.Cleanup(func() { releaseLostArmed.Store(false) })
	mgrA := newManager(t, dataA)
	if _, err := mgrA.Create(releaseFaultObjstore(t, "blobs", root)); err != nil {
		t.Fatalf("Create on A: %v", err)
	}
	releaseLostArmed.Store(true)
	if err := mgrA.Drop(ctx, "blobs"); err != nil {
		t.Fatalf("Drop on A: %v", err)
	}
	if len(releaseLedgerFiles(t, dataA)) == 0 {
		t.Fatal("the drop whose release reported a failure kept no ledger entry")
	}
	mgrB := newManager(t, dataB)
	successor, err := mgrB.Create(releaseFaultObjstore(t, "blobs", root))
	if err != nil {
		t.Fatalf("successor Create: %v", err)
	}
	if err := successor.Backend().Put(ctx, []byte("key"), []byte("w")); err != nil {
		t.Fatalf("successor Put: %v", err)
	}
	if err := mgrA.Close(); err != nil {
		t.Fatalf("Close A: %v", err)
	}
	next := newManager(t, dataA)
	if err := next.OpenAll(ctx); err != nil {
		t.Fatalf("OpenAll: %v", err)
	}
	got, err := successor.Backend().Get(ctx, []byte("key"))
	if err != nil || string(got) != "w" {
		t.Fatalf("successor Get(key) after the retried release = %q, %v; want \"w\"", got, err)
	}
	if !hasOwnerMarker(t, root) {
		t.Fatal("the retried release removed the successor's owner marker")
	}
	if left := releaseLedgerFiles(t, dataA); len(left) != 0 {
		t.Fatalf("the retried release of a namespace another incarnation owns was not forgotten: %v", left)
	}
}

func releaseFaultObjstore(t *testing.T, name, root string) database.DatabaseConfig {
	t.Helper()
	registerReleaseFault.Do(func() {
		err := objstore.RegisterProvider(releaseFaultProvider, objstore.ProviderCredentials{},
			func(settings map[string]string, creds objcommon.Credentials) (objcommon.Storage, error) {
				inner, err := objfactory.NewStorage("local", settings, creds)
				if err != nil {
					return nil, err
				}
				return releaseFaultStore{Storage: inner}, nil
			})
		if err != nil {
			t.Fatalf("RegisterProvider: %v", err)
		}
	})
	return database.DatabaseConfig{Name: name, Backend: database.BackendObjstore, Objstore: &database.DatabaseObjstoreConfig{
		Provider: releaseFaultProvider,
		Settings: map[string]string{"path": root},
	}}
}

// TestManager_FailedReleaseRetriedAtOpenAll: a drop whose namespace release
// fails still completes, and the namespace stays reserved; the release is
// recorded durably, so the next Manager's OpenAll on the same data
// directory releases it and another database may then use the namespace.
func TestManager_FailedReleaseRetriedAtOpenAll(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "objects")
	dataDir := t.TempDir()
	mgr := newManager(t, dataDir)
	db, err := mgr.Create(releaseFaultObjstore(t, "blobs", root))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := db.Backend().Put(ctx, []byte("key"), []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	releaseFaultArmed.Store(true)
	t.Cleanup(func() { releaseFaultArmed.Store(false) })
	if err := mgr.Drop(ctx, "blobs"); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if releaseFaultArmed.Load() {
		t.Fatal("the drop never deleted the owner marker")
	}
	// A create retries the recorded release first; that retry fails too,
	// so the namespace is still reserved and the create is refused.
	releaseFaultArmed.Store(true)
	var owned *objstore.NamespaceOwnedError
	if _, err := mgr.Create(releaseFaultObjstore(t, "other", root)); !errors.As(err, &owned) {
		t.Fatalf("Create onto the unreleased namespace = %v, want *NamespaceOwnedError", err)
	}
	if releaseFaultArmed.Load() {
		t.Fatal("the create did not retry the recorded release")
	}
	if err := mgr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	next := newManager(t, dataDir)
	if err := next.OpenAll(ctx); err != nil {
		t.Fatalf("OpenAll: %v", err)
	}
	other, err := next.Create(releaseFaultObjstore(t, "other", root))
	if err != nil {
		t.Fatalf("Create after the retried release: %v", err)
	}
	if err := other.Backend().Put(ctx, []byte("key"), []byte("w")); err != nil {
		t.Fatalf("Put: %v", err)
	}
}

// TestManager_ReleaseLedgerKeepsEveryIncarnation pins that the release
// ledger holds one entry per database incarnation: a name dropped, re-created
// on another namespace and dropped again, each release failing, leaves both
// namespaces to be released, not only the last one.
func TestManager_ReleaseLedgerKeepsEveryIncarnation(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	rootA := filepath.Join(base, "a")
	rootB := filepath.Join(base, "b")
	dataDir := t.TempDir()
	mgr := newManager(t, dataDir)
	t.Cleanup(func() { releaseFaultArmed.Store(false) })
	if _, err := mgr.Create(releaseFaultObjstore(t, "blobs", rootA)); err != nil {
		t.Fatalf("Create on rootA: %v", err)
	}
	releaseFaultArmed.Store(true)
	if err := mgr.Drop(ctx, "blobs"); err != nil {
		t.Fatalf("Drop on rootA: %v", err)
	}
	if releaseFaultArmed.Load() {
		t.Fatal("the first drop never deleted the owner marker")
	}
	// The re-create retries the recorded rootA release first; it fails too.
	releaseFaultArmed.Store(true)
	if _, err := mgr.Create(releaseFaultObjstore(t, "blobs", rootB)); err != nil {
		t.Fatalf("Create on rootB: %v", err)
	}
	if releaseFaultArmed.Load() {
		t.Fatal("the re-create did not retry the recorded release")
	}
	releaseFaultArmed.Store(true)
	if err := mgr.Drop(ctx, "blobs"); err != nil {
		t.Fatalf("Drop on rootB: %v", err)
	}
	if releaseFaultArmed.Load() {
		t.Fatal("the second drop never deleted the owner marker")
	}
	if err := mgr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	next := newManager(t, dataDir)
	if err := next.OpenAll(ctx); err != nil {
		t.Fatalf("OpenAll: %v", err)
	}
	if _, err := next.Create(releaseFaultObjstore(t, "a", rootA)); err != nil {
		t.Fatalf("Create on rootA after the retried releases: %v", err)
	}
	if _, err := next.Create(releaseFaultObjstore(t, "b", rootB)); err != nil {
		t.Fatalf("Create on rootB after the retried releases: %v", err)
	}
}

// dropThenRecreate creates "blobs" on rootA, drops it with the namespace
// release failing, then re-creates "blobs" on root (the create's own retry
// of that release failing too), puts a key in it and closes the manager.
func dropThenRecreate(t *testing.T, dataDir, rootA, root string) {
	t.Helper()
	ctx := context.Background()
	mgr := newManager(t, dataDir)
	t.Cleanup(func() { releaseFaultArmed.Store(false) })
	if _, err := mgr.Create(releaseFaultObjstore(t, "blobs", rootA)); err != nil {
		t.Fatalf("Create on rootA: %v", err)
	}
	releaseFaultArmed.Store(true)
	if err := mgr.Drop(ctx, "blobs"); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if releaseFaultArmed.Load() {
		t.Fatal("the drop never deleted the owner marker")
	}
	releaseFaultArmed.Store(true)
	db, err := mgr.Create(releaseFaultObjstore(t, "blobs", root))
	if err != nil {
		t.Fatalf("re-Create: %v", err)
	}
	if releaseFaultArmed.Load() {
		t.Fatal("the re-create did not retry the recorded release")
	}
	if err := db.Backend().Put(ctx, []byte("key"), []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := mgr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// reopenAndGetBlobs opens every database of dataDir in a new Manager and
// checks the re-created "blobs" still reads its key.
func reopenAndGetBlobs(t *testing.T, dataDir string) *database.Manager {
	t.Helper()
	ctx := context.Background()
	next := newManager(t, dataDir)
	if err := next.OpenAll(ctx); err != nil {
		t.Fatalf("OpenAll: %v", err)
	}
	blobs, err := next.Get("blobs")
	if err != nil {
		t.Fatalf("Get(blobs): %v", err)
	}
	got, err := blobs.Backend().Get(ctx, []byte("key"))
	if err != nil || string(got) != "v" {
		t.Fatalf("blobs Get(key) = %q, %v; want \"v\"", got, err)
	}
	return next
}

// TestManager_RecreatedNameOnAnotherNamespaceReleasesTheOld pins that the
// release retry gives back a dropped database's namespace when its name was
// re-created on another namespace, and leaves the live one alone.
func TestManager_RecreatedNameOnAnotherNamespaceReleasesTheOld(t *testing.T) {
	base := t.TempDir()
	rootA := filepath.Join(base, "a")
	rootB := filepath.Join(base, "b")
	dataDir := t.TempDir()
	dropThenRecreate(t, dataDir, rootA, rootB)

	next := reopenAndGetBlobs(t, dataDir)
	if _, err := next.Create(releaseFaultObjstore(t, "other", rootA)); err != nil {
		t.Fatalf("Create on the old namespace after the retried release: %v", err)
	}
}

// TestManager_RecreatedNameOnTheSameNamespaceKeepsIt pins that the release
// retry never releases a namespace the re-created database holds: the entry
// is forgotten and the owner marker stays.
func TestManager_RecreatedNameOnTheSameNamespaceKeepsIt(t *testing.T) {
	ctx := context.Background()
	rootA := filepath.Join(t.TempDir(), "a")
	dataDir := t.TempDir()
	mgr := newManager(t, dataDir)
	t.Cleanup(func() { releaseFaultArmed.Store(false) })
	if _, err := mgr.Create(releaseFaultObjstore(t, "blobs", rootA)); err != nil {
		t.Fatalf("Create on rootA: %v", err)
	}
	releaseFaultArmed.Store(true)
	if err := mgr.Drop(ctx, "blobs"); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	// The dropped incarnation still owns the namespace while its release
	// fails: a re-create of the name, a new incarnation, is refused rather
	// than taking over what the release will reclaim.
	releaseFaultArmed.Store(true)
	var ownedNow *objstore.NamespaceOwnedError
	if _, err := mgr.Create(releaseFaultObjstore(t, "blobs", rootA)); !errors.As(err, &ownedNow) {
		t.Fatalf("re-Create while the release fails = %v, want *NamespaceOwnedError", err)
	}
	if releaseFaultArmed.Load() {
		t.Fatal("the re-create did not retry the recorded release")
	}
	db, err := mgr.Create(releaseFaultObjstore(t, "blobs", rootA))
	if err != nil {
		t.Fatalf("re-Create after the retried release: %v", err)
	}
	if err := db.Backend().Put(ctx, []byte("key"), []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := mgr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	next := reopenAndGetBlobs(t, dataDir)
	var owned *objstore.NamespaceOwnedError
	var inUse *objstore.NamespaceInUseError
	if _, err := next.Create(releaseFaultObjstore(t, "other", rootA)); !errors.As(err, &owned) && !errors.As(err, &inUse) {
		t.Fatalf("Create on the live namespace = %v, want an objstore owner refusal", err)
	}
	if left := releaseLedgerFiles(t, dataDir); len(left) != 0 {
		t.Fatalf("release ledger still holds %v, want no entry", left)
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
