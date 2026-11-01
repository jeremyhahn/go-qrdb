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

package server

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// A database the manager opens over a file store of its own, with no
// per-database barrier, is still served from the node's store: its file store
// holds no data. That is why the at-rest report (capabilities
// UnencryptedAtRest) judges the node's own store and not every database's
// backend -- a database's data is encrypted exactly when the node's store is,
// unless it carries a per-database barrier over its own store, which
// encrypts it separately.
func TestRESTServiceRegistry_FileBackendDatabaseServedFromNodeStore(t *testing.T) {
	mgr := databasetest.NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	db, err := mgr.Create(database.DatabaseConfig{
		Name:    "files",
		Backend: database.BackendFile,
		DataDir: filepath.Join(t.TempDir(), "files"),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	nodeStore := newFilledRegistry()
	reg := NewRESTServiceRegistry(nodeStore, nil, tenantid.System,
		WithDatabaseCatalog(newManagerCatalog(mgr)),
		withDatabaseManager(mgr),
	)

	kv, err := reg.KVService(tenantid.System, "files")
	if err != nil {
		t.Fatalf("KVService: %v", err)
	}
	ctx := context.Background()
	if err := kv.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got, err := nodeStore.Default().KV.Get(ctx, tenant.ComposeNamespace(tenantid.System, "files")+"k"); err != nil || string(got) != "v" {
		t.Fatalf("the write is not in the node's store: %q, %v", got, err)
	}
	own, err := db.Backend().List(ctx, "")
	if err != nil {
		t.Fatalf("List own store: %v", err)
	}
	if len(own) != 0 {
		t.Fatalf("the database's own file store holds data: %v", own)
	}
}

// objstore is a node-store backend only: the database manager refuses to
// open a database over it, so no runtime-created database stores data there.
func TestDatabaseManager_ObjstoreDatabaseRefused(t *testing.T) {
	mgr := databasetest.NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	_, err := mgr.Create(database.DatabaseConfig{Name: "blobs", Backend: database.BackendObjstore})
	var invalid *database.InvalidBackendError
	if !errors.As(err, &invalid) {
		t.Fatalf("Create(objstore) = %v, want *database.InvalidBackendError", err)
	}
}
