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
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/database/objstoredb"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// objstoreDropBarrierManager returns a database barrier manager over an
// in-memory system barrier, so the manager wraps each database's backend in
// barrier encryption.
func objstoreDropBarrierManager(t *testing.T) *database.DatabaseBarrierManager {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	strategy, err := seal.NewSoftwareStrategy(context.Background(), []byte("test-master-password"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	system, err := seal.NewStorageBarrier(logger, seal.NewMemoryBackend(), seal.BarrierConfig{
		RootKeyPath: "system/root-key",
	}, strategy)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	bm, err := database.NewDatabaseBarrierManager(logger, system)
	if err != nil {
		t.Fatalf("NewDatabaseBarrierManager: %v", err)
	}
	return bm
}

// TestManager_DropReclaimsProviderObjectsInEveryBackendState pins #1599: a
// drop deletes an object-storage database's provider and journal objects
// whatever its backend state -- open, sealed, closed, or failed (the backend
// released under a database still listed as open) -- and whether or not a
// barrier wrapper sits over the store.
func TestManager_DropReclaimsProviderObjectsInEveryBackendState(t *testing.T) {
	states := []struct {
		name  string
		apply func(t *testing.T, db *database.Database)
	}{
		{"open", func(*testing.T, *database.Database) {}},
		{"sealed", func(_ *testing.T, db *database.Database) { db.SetStatus(database.StatusSealed) }},
		{"closed", func(t *testing.T, db *database.Database) {
			if err := db.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
		}},
		{"failed", func(t *testing.T, db *database.Database) {
			if err := db.Backend().Close(); err != nil {
				t.Fatalf("backend Close: %v", err)
			}
		}},
	}
	wrappers := []struct {
		name    string
		barrier bool
	}{
		{"raw store", false},
		{"barrier wrapped", true},
	}
	for _, w := range wrappers {
		for _, st := range states {
			t.Run(w.name+"/"+st.name, func(t *testing.T) {
				ctx := context.Background()
				root := filepath.Join(t.TempDir(), "objects")
				opts := []database.ManagerOption{database.WithBackendFactory(objstoredb.NewBackendFactory())}
				if w.barrier {
					opts = append(opts, database.WithBarrierManager(objstoreDropBarrierManager(t)))
				}
				mgr := databasetest.NewManager(nil, t.TempDir(), opts...)
				t.Cleanup(func() { _ = mgr.Close() })

				cfg := localObjstore("blobs", root)
				var db *database.Database
				var err error
				if w.barrier {
					cfg.DataDir = t.TempDir() // a barrier needs a dedicated store
					cfg.Barrier = &database.DatabaseBarrierConfig{Enabled: true, Mode: "passphrase"}
					db, err = mgr.CreateWithCredentials(ctx, cfg, seal.MustCredentials(ctx, []byte("test-password")))
				} else {
					db, err = mgr.Create(cfg)
				}
				if err != nil {
					t.Fatalf("Create: %v", err)
				}
				if _, wrapped := db.Backend().(*database.BarrierBackend); wrapped != w.barrier {
					t.Fatalf("Backend() barrier wrapped = %v; the %s case needs %v", wrapped, w.name, w.barrier)
				}
				if err := db.Backend().Put(ctx, []byte("key"), []byte("v")); err != nil {
					t.Fatalf("Put: %v", err)
				}
				if len(objects(t, root)) == 0 {
					t.Fatal("no objects written to the provider")
				}
				st.apply(t, db)

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
}
