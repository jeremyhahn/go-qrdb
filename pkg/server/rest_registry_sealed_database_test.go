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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// sealedResolution is one data-plane accessor of the registry, reduced to the
// error it returns, so a test can assert the same outcome across every engine.
type sealedResolution struct {
	engine  string
	resolve func(r *restServiceRegistry, tenantID, db string) error
}

// everyDataPlaneAccessor lists every per-(tenant, database) accessor the
// registry serves. A sealed database must be refused by all of them, because
// each is a separate way into the same data.
var everyDataPlaneAccessor = []sealedResolution{
	{"kv", func(r *restServiceRegistry, tn, db string) error { _, err := r.KVService(tn, db); return err }},
	{"mvcc", func(r *restServiceRegistry, tn, db string) error { _, err := r.MVCCService(tn, db); return err }},
	{"watch", func(r *restServiceRegistry, tn, db string) error { _, err := r.WatchService(tn, db); return err }},
	{"lease", func(r *restServiceRegistry, tn, db string) error { _, err := r.LeaseService(tn, db); return err }},
	{"index", func(r *restServiceRegistry, tn, db string) error { _, err := r.IndexService(tn, db); return err }},
	{"timeseries", func(r *restServiceRegistry, tn, db string) error { _, err := r.TimeSeriesService(tn, db); return err }},
	{"vector", func(r *restServiceRegistry, tn, db string) error { _, err := r.VectorService(tn, db); return err }},
	{"transaction", func(r *restServiceRegistry, tn, db string) error { _, err := r.TransactionService(tn, db); return err }},
	{"batch", func(r *restServiceRegistry, tn, db string) error { _, err := r.BatchService(tn, db); return err }},
	{"table", func(r *restServiceRegistry, tn, db string) error { _, err := r.TableService(tn, db); return err }},
	{"sequence", func(r *restServiceRegistry, tn, db string) error { _, err := r.SequenceService(tn, db); return err }},
	{"privilege", func(r *restServiceRegistry, tn, db string) error { _, err := r.PrivilegeService(tn, db); return err }},
	{"lock", func(r *restServiceRegistry, tn, db string) error { _, err := r.LockService(tn, db); return err }},
	{"collection", func(r *restServiceRegistry, tn, db string) error { _, err := r.CollectionService(tn, db); return err }},
}

// newSealedDatabaseRegistry builds a registry over a real database.Manager
// holding one database, owned by owner, and returns both.
func newSealedDatabaseRegistry(t *testing.T, owner, name string) (*restServiceRegistry, *database.Database) {
	t.Helper()
	mgr := database.NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	db, err := mgr.Create(database.DatabaseConfig{
		TenantID: owner,
		Name:     name,
		Engine:   database.EngineKV,
		Backend:  database.BackendMemory,
	})
	if err != nil {
		t.Fatalf("Create(%q): %v", name, err)
	}
	reg, ok := NewRESTServiceRegistry(newFilledRegistry(), nil, tenantid.System,
		WithDatabaseCatalog(newManagerCatalog(mgr, tenantid.System)),
		withDatabaseManager(mgr),
	).(*restServiceRegistry)
	if !ok {
		t.Fatal("NewRESTServiceRegistry did not return *restServiceRegistry")
	}
	return reg, db
}

// TestRESTServiceRegistry_SealedDatabase_RefusedOnEveryEngine pins SEC-1: a
// database whose status is sealed is served from the shared store, so nothing
// on the store itself refuses the request. The registry is the one place every
// transport resolves a (tenant, database) pair, so it must refuse every engine
// with the typed sealed error -- before the fix every accessor below returned
// the shared store's servicer and the sealed data stayed readable and writable.
func TestRESTServiceRegistry_SealedDatabase_RefusedOnEveryEngine(t *testing.T) {
	reg, db := newSealedDatabaseRegistry(t, "", "vault")

	// Open: every accessor resolves.
	if err := everyDataPlaneAccessor[0].resolve(reg, tenantid.System, "vault"); err != nil {
		t.Fatalf("KVService on an open database: %v", err)
	}

	db.SetStatus(database.StatusSealed)
	for _, a := range everyDataPlaneAccessor {
		t.Run(a.engine, func(t *testing.T) {
			err := a.resolve(reg, tenantid.System, "vault")
			var sealed *service.BarrierSealedError
			if !errors.As(err, &sealed) {
				t.Fatalf("%s on a sealed database: err = %v (%T), want *service.BarrierSealedError", a.engine, err, err)
			}
			if sealed.Database != "vault" {
				t.Errorf("Database = %q, want %q", sealed.Database, "vault")
			}
		})
	}

	// Unsealing restores access.
	db.SetStatus(database.StatusOpen)
	if err := everyDataPlaneAccessor[0].resolve(reg, tenantid.System, "vault"); err != nil {
		t.Fatalf("KVService after unseal: %v", err)
	}
}

// A sealed database is only reported to a tenant that may address it. Another
// tenant naming it gets the ordinary not-found answer, so the sealed state is
// not an oracle for a database the caller does not own.
func TestRESTServiceRegistry_SealedDatabase_OtherTenantSeesNotFound(t *testing.T) {
	reg, db := newSealedDatabaseRegistry(t, "owner", "vault")
	db.SetStatus(database.StatusSealed)

	err := everyDataPlaneAccessor[0].resolve(reg, "intruder", "vault")
	var sealed *service.BarrierSealedError
	if errors.As(err, &sealed) {
		t.Fatalf("a non-owner was told the database is sealed: %v", err)
	}
	var notFound *service.DatabaseNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("err = %v (%T), want *service.DatabaseNotFoundError", err, err)
	}

	if err := everyDataPlaneAccessor[0].resolve(reg, "owner", "vault"); !errors.As(err, &sealed) {
		t.Fatalf("owner on its sealed database: err = %v (%T), want *service.BarrierSealedError", err, err)
	}
}

func TestBarrierSealedError_DatabaseMessage(t *testing.T) {
	tenantOnly := (&service.BarrierSealedError{TenantID: "acme"}).Error()
	if tenantOnly != "service: barrier sealed for tenant: acme" {
		t.Errorf("tenant message = %q", tenantOnly)
	}
	withDB := (&service.BarrierSealedError{TenantID: "acme", Database: "vault"}).Error()
	if withDB != "service: barrier sealed for database: vault (tenant: acme)" {
		t.Errorf("database message = %q", withDB)
	}
}
