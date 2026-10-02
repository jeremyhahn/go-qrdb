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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// go-qrdb#482 finding (c): resolve's dbReg.GetOrDefault short-circuit
// returns a pre-registered *DatabaseServices bundle WITHOUT ever calling
// databaseExists/database.Manager.Get, so a name that happens to be
// pre-registered (e.g. the default database, or one an embedding registered
// directly) bypassed the drop fence entirely. refuseIfDropping closes that
// by checking database.Manager.Get -- unconditionally, before dbReg is even
// consulted -- whenever a database.Manager is wired in.
// ---------------------------------------------------------------------------

// TestRESTServiceRegistry_RefusesPreRegisteredDatabaseWhileDropping is the
// failing-first test for finding (c): "mydb" is pre-registered in dbReg
// (see newFilledRegistry), which is exactly the path that used to bypass
// the fence, and it is ALSO tracked by a real database.Manager whose
// Dropping flag is set directly (standing in for a Manager.Drop already in
// flight).
func TestRESTServiceRegistry_RefusesPreRegisteredDatabaseWhileDropping(t *testing.T) {
	dbMgr := database.NewManager(nil, t.TempDir())
	db, err := dbMgr.Create(database.DatabaseConfig{
		Name: "mydb", Engine: database.EngineKV, Backend: database.BackendMemory,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	db.SetDropping(true)

	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "system", withDatabaseManager(dbMgr))

	_, err = r.KVService("system", "mydb")
	var dropping *database.DroppingError
	if !errors.As(err, &dropping) {
		t.Fatalf("KVService(%q) error = %v (%T), want *database.DroppingError", "mydb", err, err)
	}
}

// TestRESTServiceRegistry_PreRegisteredDatabaseResolvesWhenNotDropping is
// the control: a database.Manager wired in must not itself break ordinary
// resolution of a pre-registered database that is not being dropped.
func TestRESTServiceRegistry_PreRegisteredDatabaseResolvesWhenNotDropping(t *testing.T) {
	dbMgr := database.NewManager(nil, t.TempDir())
	if _, err := dbMgr.Create(database.DatabaseConfig{
		Name: "mydb", Engine: database.EngineKV, Backend: database.BackendMemory,
	}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "system", withDatabaseManager(dbMgr))

	svc, err := r.KVService("system", "mydb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil KVServicer")
	}
}

// TestRESTServiceRegistry_CrossTenantDroppingDatabaseIndistinguishableFromMissing
// is SEC-8(b): managedDatabase used to resolve the catalog record through
// database.Manager.Get, whose drop-fence refusal (DroppingError) fires before
// any ownership check. A request for another tenant's DROPPING database
// therefore answered 409/ABORTED while the identical request for a name
// nobody holds answered 404/NOT_FOUND -- an oracle telling the caller that
// the other tenant's database exists and is mid-drop. Both must now answer
// the same NotFoundError.
func TestRESTServiceRegistry_CrossTenantDroppingDatabaseIndistinguishableFromMissing(t *testing.T) {
	dbMgr := database.NewManager(nil, t.TempDir())
	db, err := dbMgr.Create(database.DatabaseConfig{
		Name: "acme-ledger", TenantID: "acme", Engine: database.EngineKV, Backend: database.BackendMemory,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	db.SetDropping(true)

	// A real DatabaseCatalog, not the no-catalog fallback: without one,
	// resolve's dbReg.GetOrDefault/databaseExists short-circuit answers
	// DatabaseNotFoundError for any unregistered name before managedDatabase's
	// record is ever consulted, which would make this test pass without
	// exercising the fix at all.
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "system",
		withDatabaseManager(dbMgr), WithDatabaseCatalog(newManagerCatalog(dbMgr)))

	_, ownedErr := r.KVService("globex", "acme-ledger")
	_, missingErr := r.KVService("globex", "nobody-has-this")

	var ownedNotFound, missingNotFound *rest.DatabaseNotFoundError
	if !errors.As(ownedErr, &ownedNotFound) {
		t.Fatalf("KVService on another tenant's DROPPING database = %v (%T), want *rest.DatabaseNotFoundError", ownedErr, ownedErr)
	}
	if !errors.As(missingErr, &missingNotFound) {
		t.Fatalf("KVService on a missing database = %v (%T), want *rest.DatabaseNotFoundError", missingErr, missingErr)
	}

	// The owning tenant must still see the real refusal: the fence is not
	// disabled, only scoped to the caller that may observe it.
	_, err = r.KVService("acme", "acme-ledger")
	var dropping *database.DroppingError
	if !errors.As(err, &dropping) {
		t.Fatalf("KVService(owner) on a DROPPING database = %v (%T), want *database.DroppingError", err, err)
	}
}

// workingVec is a VectorServicer whose UpsertVector actually returns instead
// of reaching the nil-embedded interface every other stubVectorService
// panics on: the stale-handle case in the test below never reaches it (the
// fence refuses first), but the fresh-handle control case does, and must
// not panic.
type workingVec struct{ service.VectorServicer }

func (workingVec) UpsertVector(context.Context, *service.UpsertVectorRequest) (*service.UpsertVectorResponse, error) {
	return &service.UpsertVectorResponse{}, nil
}

// TestNamespacedVectorService_StaleHandleRefusedAfterDropAndRecreate is N1:
// vector writes are not Raft proposals (see dropFence's doc comment), so
// they carry no write guard and are instead refused by a check made before
// each write. That check must compare the INCARNATION the handle was
// resolved for, not merely whether the database is dropping right now: a
// handle resolved before a drop+recreate cycle names the OLD incarnation,
// and by the time it writes again the live record for the name is a
// different, healthy database that answers "not dropping" on its own.
// Without the incarnation compare, that stale handle would keep writing
// into the namespace the new database now owns. A real database.Manager
// with its reclaimer wired in is required -- without one every database
// shares the empty incarnation on purpose (see Manager.Create), and the
// compare this test exists for would never run at all.
func TestNamespacedVectorService_StaleHandleRefusedAfterDropAndRecreate(t *testing.T) {
	f := newSharedStoreFixture(t)
	f.raw.Vector = workingVec{}

	stale, err := f.reg.VectorService("system", "orders")
	if err != nil {
		t.Fatalf("VectorService (stale resolution): %v", err)
	}

	if err := f.mgr.Drop(context.Background(), "orders"); err != nil {
		t.Fatalf("Drop() error = %v", err)
	}
	if _, err := f.mgr.Create(database.DatabaseConfig{Name: "orders", Engine: database.EngineKV, Backend: database.BackendMemory}); err != nil {
		t.Fatalf("re-Create() error = %v", err)
	}

	req := &service.UpsertVectorRequest{Namespace: "ns", Vector: []float32{1, 2, 3, 4}}
	_, err = stale.UpsertVector(context.Background(), req)
	var dropping *database.DroppingError
	if !errors.As(err, &dropping) {
		t.Fatalf("stale handle UpsertVector = %v (%T), want *database.DroppingError", err, err)
	}

	fresh, err := f.reg.VectorService("system", "orders")
	if err != nil {
		t.Fatalf("VectorService (fresh resolution): %v", err)
	}
	if _, err := fresh.UpsertVector(context.Background(), req); err != nil {
		t.Fatalf("fresh handle UpsertVector = %v, want nil", err)
	}
}

// TestNamespacedVectorService_StaleHandleRefusedAfterCompletedDrop is the
// blocker from the re-review of the first incarnation fix: refuseIfDropping
// treated a database.Manager.Get failure OTHER than DroppingError --
// typically NotFoundError -- as "allow", on the theory that a name the
// catalog has no record for is simply unmanaged. That theory holds for a
// caller with nothing pinned, but not for the vector fence: a COMPLETED drop
// (no recreate at all, unlike the test above) removes the catalog entry
// entirely, so Get returns NotFoundError, not DroppingError -- and a stale
// handle's write sailed through as "allow", orphaning vectors that the
// name's NEXT incarnation would silently inherit. The fix is that a PINNED
// caller (sharedStoreFence always pins the record it resolved its handle
// against) treats ANY lookup failure as a refusal, not only DroppingError.
func TestNamespacedVectorService_StaleHandleRefusedAfterCompletedDrop(t *testing.T) {
	f := newSharedStoreFixture(t)
	f.raw.Vector = workingVec{}

	stale, err := f.reg.VectorService("system", "orders")
	if err != nil {
		t.Fatalf("VectorService (stale resolution): %v", err)
	}

	if err := f.mgr.Drop(context.Background(), "orders"); err != nil {
		t.Fatalf("Drop() error = %v", err)
	}
	// No re-Create: the catalog entry for "orders" is simply gone now.

	req := &service.UpsertVectorRequest{Namespace: "ns", Vector: []float32{1, 2, 3, 4}}
	_, err = stale.UpsertVector(context.Background(), req)
	var dropping *database.DroppingError
	if !errors.As(err, &dropping) {
		t.Fatalf("stale handle UpsertVector after a completed drop = %v (%T), want *database.DroppingError", err, err)
	}
}

// TestNamespacedVectorService_StaleHandleRefusedAfterCompletedDrop_EmptyIncarnation
// is the same completed-drop scenario, but the database.Manager has no
// reclaimer wired in, so every database -- including "orders" -- shares the
// empty incarnation by design (see Manager.Create). With an empty incarnation
// there is nothing for refuseIfDropping's incarnation compare to catch, so
// this proves the pinned/any-lookup-failure check that covers a completed
// drop works independently of incarnation tracking: a legacy or provisioned
// database needs the same protection a reclaimer-tracked one does.
func TestNamespacedVectorService_StaleHandleRefusedAfterCompletedDrop_EmptyIncarnation(t *testing.T) {
	dbMgr := database.NewManager(nil, t.TempDir())
	if _, err := dbMgr.Create(database.DatabaseConfig{Name: "orders", Engine: database.EngineKV, Backend: database.BackendMemory}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	reg := NewDatabaseServiceRegistry("default")
	svcs := newFilledServices("orders")
	svcs.Vector = workingVec{}
	reg.Register("orders", svcs)

	r := NewRESTServiceRegistry(reg, nil, "system", withDatabaseManager(dbMgr))

	stale, err := r.VectorService("system", "orders")
	if err != nil {
		t.Fatalf("VectorService (stale resolution): %v", err)
	}

	if err := dbMgr.Drop(context.Background(), "orders"); err != nil {
		t.Fatalf("Drop() error = %v", err)
	}

	req := &service.UpsertVectorRequest{Namespace: "ns", Vector: []float32{1, 2, 3, 4}}
	_, err = stale.UpsertVector(context.Background(), req)
	var dropping *database.DroppingError
	if !errors.As(err, &dropping) {
		t.Fatalf("stale handle (empty incarnation) UpsertVector after a completed drop = %v (%T), want *database.DroppingError", err, err)
	}
}

// TestRESTServiceRegistry_UnmanagedDatabaseUnaffectedByDropFence pins the
// no-op cases refuseIfDropping documents: a database.Manager with no entry
// for the requested name (a pre-registered database an embedding manages
// itself, entirely outside database.Manager's catalog) must resolve exactly
// as it did before the fence existed -- a NotFoundError from Manager.Get is
// not a refusal.
func TestRESTServiceRegistry_UnmanagedDatabaseUnaffectedByDropFence(t *testing.T) {
	dbMgr := database.NewManager(nil, t.TempDir())

	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "system", withDatabaseManager(dbMgr))

	svc, err := r.KVService("system", "mydb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil KVServicer")
	}
}
