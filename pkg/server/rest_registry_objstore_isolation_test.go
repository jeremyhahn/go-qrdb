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
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/database/objstoredb"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// TestRESTServiceRegistry_ObjstoreDatabaseServedFromItsStoreUnderEveryIsolation
// pins #1600: a tenant's `backend: objstore` database is served from its own
// store whatever the tenant's isolation mode -- unset, prefix, barrier, raft
// or volume -- so a write through the registry lands in the database's
// provider and never in the node's shared store, and another tenant cannot
// reach it.
func TestRESTServiceRegistry_ObjstoreDatabaseServedFromItsStoreUnderEveryIsolation(t *testing.T) {
	modes := []tenant.IsolationType{
		"",
		tenant.IsolationPrefix,
		tenant.IsolationBarrier,
		tenant.IsolationRaft,
		tenant.IsolationVolume,
	}
	for _, mode := range modes {
		name := string(mode)
		if name == "" {
			name = "unset"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			mgr := databasetest.NewManager(nil, t.TempDir(), database.WithBackendFactory(objstoredb.NewBackendFactory()))
			t.Cleanup(func() { _ = mgr.Close() })
			db, err := mgr.Create(database.DatabaseConfig{
				TenantID: "acme",
				Name:     "blobs",
				Backend:  database.BackendObjstore,
				Objstore: &database.DatabaseObjstoreConfig{
					Provider: "local",
					Settings: map[string]string{"path": filepath.Join(t.TempDir(), "objects")},
				},
			})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			nodeStore := newFilledRegistry()
			reg := NewRESTServiceRegistry(nodeStore, nil, tenantid.System,
				WithRegistryDatabaseManager(mgr),
				WithTenantServicer(&isolationTenantServicer{isolation: mode}),
			)

			kv, err := reg.KVService("acme", "blobs")
			if err != nil {
				t.Fatalf("KVService under %s isolation: %v", name, err)
			}
			if err := kv.Put(ctx, "k", []byte("v")); err != nil {
				t.Fatalf("Put: %v", err)
			}
			if got, err := kv.Get(ctx, "k"); err != nil || string(got) != "v" {
				t.Fatalf("Get through the registry = %q, %v; want \"v\"", got, err)
			}
			if got, err := db.Backend().Get(ctx, []byte("k")); err != nil || string(got) != "v" {
				t.Fatalf("the write is not in the database's own store: %q, %v", got, err)
			}
			if got, err := nodeStore.Default().KV.Get(ctx, tenant.ComposeNamespace("acme", "blobs")+"k"); err == nil && got != nil {
				t.Fatalf("the write reached the node's shared store: %q, %v", got, err)
			}
			if _, err := reg.KVService("other", "blobs"); err == nil {
				t.Fatal("another tenant resolved acme's objstore database")
			}
		})
	}
}
