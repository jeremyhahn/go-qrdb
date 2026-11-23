//go:build qrdb_embedded && !integration

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

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// startRegistryNode starts an embedded node on the memory backend and
// returns the service registry embeddedServiceRegistry builds over it, with
// the node's database manager.
func startRegistryNode(t *testing.T) (service.Registry, *database.Manager) {
	t.Helper()
	esb := newTestEmbeddedServerBinding(t)
	if err := esb.StartEmbedded(&EmbeddedConfig{StorageBackend: "memory", DataDir: t.TempDir()}); err != nil {
		t.Fatalf("StartEmbedded: %v", err)
	}
	t.Cleanup(func() { _ = esb.StopEmbedded() })
	if esb.dbManager == nil {
		t.Fatal("the embedded node holds no database manager")
	}
	return embeddedServiceRegistry(esb.svcNode, esb.dbManager, nil, false), esb.dbManager
}

func createMemoryDatabase(t *testing.T, mgr *database.Manager, name string) *database.Database {
	t.Helper()
	db, err := mgr.Create(database.DatabaseConfig{Name: name, Backend: database.BackendMemory})
	if err != nil {
		t.Fatalf("Create %s: %v", name, err)
	}
	return db
}

// assertRefusedAfterDrop checks that a write through a handle resolved
// before DROP DATABASE is refused as the dropped database's.
func assertRefusedAfterDrop(t *testing.T, err error, name string) {
	t.Helper()
	var dropping *database.DroppingError
	var notFound *database.NotFoundError
	if !errors.As(err, &dropping) && !errors.As(err, &notFound) {
		t.Fatalf("held write to %s err = %v, want a dropping or not-found refusal", name, err)
	}
}

// failingCatalog is a database catalog whose every read fails with a
// catalog read error that is not a not-found answer.
type failingCatalog struct {
	database.Catalog
}

func (failingCatalog) Get(context.Context, string, string) (database.DatabaseMeta, error) {
	return database.DatabaseMeta{}, &database.CatalogReadError{Key: "catalog", Err: errors.New("catalog unavailable")}
}

// TestEmbeddedServiceRegistry_RefusesSealedDatabase checks that the
// embedded service registry refuses a sealed database on every engine and
// on a stream, as the REST server does, and never serves its scope from the
// shared service graph.
func TestEmbeddedServiceRegistry_RefusesSealedDatabase(t *testing.T) {
	reg, mgr := startRegistryNode(t)
	createMemoryDatabase(t, mgr, "vault").SetStatus(database.StatusSealed)

	var sealed *service.BarrierSealedError
	if _, err := reg.KVService(tenantid.System, "vault"); !errors.As(err, &sealed) || sealed.Database != "vault" {
		t.Fatalf("KVService err = %v, want BarrierSealedError for vault", err)
	}
	if _, err := reg.TimeSeriesService(tenantid.System, "vault"); !errors.As(err, &sealed) {
		t.Fatalf("TimeSeriesService err = %v, want BarrierSealedError", err)
	}
	refuser, ok := reg.(service.StreamRefuser)
	if !ok {
		t.Fatal("the embedded registry refuses no stream")
	}
	if err := refuser.StreamRefusal(tenantid.System, "vault"); !errors.As(err, &sealed) {
		t.Fatalf("StreamRefusal err = %v, want BarrierSealedError", err)
	}
}

// TestEmbeddedServiceRegistry_UnknownDatabaseIsNotFound checks that a name
// the node's database manager holds no record for is not served.
func TestEmbeddedServiceRegistry_UnknownDatabaseIsNotFound(t *testing.T) {
	reg, _ := startRegistryNode(t)

	_, err := reg.KVService(tenantid.System, "unknown")
	var notFound *service.DatabaseNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("KVService(unknown) err = %v, want DatabaseNotFoundError", err)
	}
}

// TestEmbeddedServiceRegistry_ServesPlainAndDefault checks that a database
// with no barrier of its own and the default database resolve, each to its
// own keyspace.
func TestEmbeddedServiceRegistry_ServesPlainAndDefault(t *testing.T) {
	reg, mgr := startRegistryNode(t)
	createMemoryDatabase(t, mgr, "plain")
	ctx := context.Background()

	plain, err := reg.KVService(tenantid.System, "plain")
	if err != nil {
		t.Fatalf("KVService(plain): %v", err)
	}
	def, err := reg.KVService(tenantid.System, "")
	if err != nil {
		t.Fatalf("KVService(default): %v", err)
	}
	if err := plain.Put(ctx, "k", []byte("plain")); err != nil {
		t.Fatalf("Put(plain): %v", err)
	}
	if got, err := def.Get(ctx, "k"); err == nil && got != nil {
		t.Fatalf("the default database reads plain's key: %q", got)
	}
}

// TestEmbeddedServiceRegistry_HeldWriteRefusedAcrossDropAndRecreate checks
// that a write through a handle resolved before DROP DATABASE is refused
// once the name is dropped and created again, and lands in neither
// incarnation: the new database holds no key from it.
func TestEmbeddedServiceRegistry_HeldWriteRefusedAcrossDropAndRecreate(t *testing.T) {
	reg, mgr := startRegistryNode(t)
	createMemoryDatabase(t, mgr, "orders")
	ctx := context.Background()

	held, err := reg.KVService(tenantid.System, "orders")
	if err != nil {
		t.Fatalf("KVService: %v", err)
	}
	if err := mgr.Drop(ctx, "orders"); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	createMemoryDatabase(t, mgr, "orders")

	assertRefusedAfterDrop(t, held.Put(ctx, "k", []byte("into the new database")), "orders")

	fresh, err := reg.KVService(tenantid.System, "orders")
	if err != nil {
		t.Fatalf("KVService after re-create: %v", err)
	}
	if got, err := fresh.Get(ctx, "k"); err == nil && got != nil {
		t.Fatalf("the re-created database holds the held handle's write: %q", got)
	}
}

// TestEmbeddedServiceRegistry_HeldWriteRefusedAfterCompletedDrop checks
// that a write through a handle resolved before a completed DROP DATABASE is
// refused, and the name no longer resolves.
func TestEmbeddedServiceRegistry_HeldWriteRefusedAfterCompletedDrop(t *testing.T) {
	reg, mgr := startRegistryNode(t)
	createMemoryDatabase(t, mgr, "orders")
	ctx := context.Background()

	held, err := reg.KVService(tenantid.System, "orders")
	if err != nil {
		t.Fatalf("KVService: %v", err)
	}
	if err := mgr.Drop(ctx, "orders"); err != nil {
		t.Fatalf("Drop: %v", err)
	}

	assertRefusedAfterDrop(t, held.Put(ctx, "k", []byte("after the drop")), "orders")
	if _, err := reg.KVService(tenantid.System, "orders"); err == nil {
		t.Fatal("a dropped database still resolves")
	}
}

// TestEmbeddedServiceRegistry_LookupErrorFailsClosed checks that a database
// manager lookup failing with anything other than not-found refuses the
// resolution: no service is returned and the lookup's error is the answer,
// rather than the name falling through to the shared service graph.
func TestEmbeddedServiceRegistry_LookupErrorFailsClosed(t *testing.T) {
	reg, mgr := startRegistryNode(t)
	mgr.SetCatalog(failingCatalog{})

	svc, err := reg.KVService(tenantid.System, "ledger")
	var readErr *database.CatalogReadError
	if !errors.As(err, &readErr) {
		t.Fatalf("KVService err = %v, want the catalog read error", err)
	}
	if svc != nil {
		t.Fatal("a failed lookup returned a service")
	}
	if _, err := reg.VectorService(tenantid.System, "ledger"); !errors.As(err, &readErr) {
		t.Fatalf("VectorService err = %v, want the catalog read error", err)
	}
}

// TestEmbeddedServiceRegistry_NotFoundThenSealedCreateIsRefused checks that
// a name first resolved as not found, then created sealed, resolves to the
// sealed refusal: the record read at resolution decides, never an earlier
// answer or a second catalog lookup.
func TestEmbeddedServiceRegistry_NotFoundThenSealedCreateIsRefused(t *testing.T) {
	reg, mgr := startRegistryNode(t)

	var notFound *service.DatabaseNotFoundError
	if _, err := reg.KVService(tenantid.System, "vault"); !errors.As(err, &notFound) {
		t.Fatalf("KVService before create err = %v, want DatabaseNotFoundError", err)
	}
	createMemoryDatabase(t, mgr, "vault").SetStatus(database.StatusSealed)

	svc, err := reg.KVService(tenantid.System, "vault")
	var sealed *service.BarrierSealedError
	if !errors.As(err, &sealed) || sealed.Database != "vault" {
		t.Fatalf("KVService after sealed create err = %v, want BarrierSealedError for vault", err)
	}
	if svc != nil {
		t.Fatal("a sealed database returned a service")
	}
}
