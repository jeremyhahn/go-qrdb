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
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// The STORE fence -- the key every write to the database is guarded by --
// goes in before anything is reclaimed. A reclamation that ran first would
// scan a keyspace writes could still land in.
func TestManager_Drop_FencesTheStoreBeforeReclaiming(t *testing.T) {
	mgr, spy := newReclaimManager(t, "orders")
	_, unfencedAtCreate, _ := spy.fenceCalls()

	require.NoError(t, mgr.Drop(context.Background(), "orders"))

	fenced, unfenced, events := spy.fenceCalls()
	assert.Equal(t, []string{"/orders"}, fenced)
	assert.Equal(t, unfencedAtCreate, unfenced, "a completed drop leaves its fence in place, refusing handles resolved before it")
	assert.Equal(t, []string{"fence", "reclaim"}, events[len(events)-2:])
}

// A fence write that reports failure fails the drop before anything is
// reclaimed. The failure does not mean the fence is absent -- a proposal can
// commit and still report an error -- so the rollback clears it anyway, and
// only then clears the local fence: the database ends up live in both.
func TestManager_Drop_FenceFailureRollsBackAndReclaimsNothing(t *testing.T) {
	mgr, spy := newReclaimManager(t, "orders")
	db, err := mgr.Get("orders")
	require.NoError(t, err)
	spy.fenceErr = errors.New("proposal timed out after it committed")

	err = mgr.Drop(context.Background(), "orders")
	var fenceErr *KeyspaceFenceError
	require.ErrorAs(t, err, &fenceErr)
	assert.Equal(t, "orders", fenceErr.Name)
	assert.ErrorIs(t, err, spy.fenceErr)
	assert.Contains(t, fenceErr.Error(), "orders")

	tenants, _ := spy.calls()
	assert.Empty(t, tenants, "nothing may be reclaimed after a fence write reported failure")
	assert.Equal(t, db.Incarnation(), spy.markerOf("/orders"),
		"the committed-but-errored fence must be rolled back to the database's own incarnation")
	_, getErr := mgr.Get("orders")
	assert.NoError(t, getErr, "the failed drop must leave the database resolvable")
}

// A reclamation failure restores the database's own incarnation in the
// store, so the surviving database accepts writes again.
func TestManager_Drop_ReclaimFailureRemovesTheStoreFence(t *testing.T) {
	mgr, spy := newReclaimManager(t, "orders")
	db, err := mgr.Get("orders")
	require.NoError(t, err)
	_, unfencedAtCreate, _ := spy.fenceCalls()
	spy.err = errors.New("reclaim failed")

	var reclaimErr *KeyspaceReclaimError
	require.ErrorAs(t, mgr.Drop(context.Background(), "orders"), &reclaimErr)

	_, unfenced, _ := spy.fenceCalls()
	assert.Equal(t, append(unfencedAtCreate, "/orders"), unfenced)
	assert.Equal(t, db.Incarnation(), spy.markerOf("/orders"))
	_, getErr := mgr.Get("orders")
	assert.NoError(t, getErr)
}

// When the store fence cannot be cleared, the database must not look live:
// its local fence stays, so it is refused honestly until the drop is
// retried, instead of resolving while the store refuses its writes.
func TestManager_Drop_UnclearableFenceKeepsTheDatabaseFenced(t *testing.T) {
	mgr, spy := newReclaimManager(t, "orders")
	spy.err = errors.New("reclaim failed")
	spy.unfenceErr = errors.New("store unavailable")

	var reclaimErr *KeyspaceReclaimError
	require.ErrorAs(t, mgr.Drop(context.Background(), "orders"), &reclaimErr)
	assert.ErrorIs(t, reclaimErr, spy.err)

	assert.Equal(t, spyTombstone, spy.markerOf("/orders"))
	_, getErr := mgr.Get("orders")
	var dropping *DroppingError
	assert.ErrorAs(t, getErr, &dropping, "a database whose store fence is still set must stay refused")
}

// The rollback runs even when the drop failed because its request was
// cancelled: a rollback under the same context would fail the same way.
func TestManager_Drop_RollbackOutlivesACancelledRequest(t *testing.T) {
	mgr, spy := newReclaimManager(t, "orders")
	db, err := mgr.Get("orders")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	spy.before = cancel
	spy.err = context.Canceled
	spy.unfenceHonorsCtx = true

	var reclaimErr *KeyspaceReclaimError
	require.ErrorAs(t, mgr.Drop(ctx, "orders"), &reclaimErr)
	assert.Equal(t, db.Incarnation(), spy.markerOf("/orders"))
	_, getErr := mgr.Get("orders")
	assert.NoError(t, getErr)
}

// Creating a database marks its new incarnation live in the store, and the
// incarnation is recorded with the database and survives a restart.
func TestManager_Create_MarksANewIncarnationLive(t *testing.T) {
	mgr, spy := newReclaimManager(t, "orders")

	db, err := mgr.Create(DatabaseConfig{Name: "billing", Engine: EngineKV, Backend: BackendMemory})
	require.NoError(t, err)
	require.NotEmpty(t, db.Incarnation())
	assert.Equal(t, db.Incarnation(), spy.markerOf("/billing"))

	orders, err := mgr.Get("orders")
	require.NoError(t, err)
	assert.NotEqual(t, orders.Incarnation(), db.Incarnation(), "two databases must not share an incarnation")

	restarted := NewManager(nil, mgr.DataDir())
	t.Cleanup(func() { _ = restarted.Close() })
	reopened, err := restarted.Open("billing")
	require.NoError(t, err)
	assert.Equal(t, db.Incarnation(), reopened.Incarnation(), "the incarnation must survive a restart")
}

// A database created again under a dropped name is a new incarnation, so a
// handle of the old one stays refused.
func TestManager_Create_ReCreateIsANewIncarnation(t *testing.T) {
	mgr, spy := newReclaimManager(t, "orders")
	old, err := mgr.Get("orders")
	require.NoError(t, err)
	require.NoError(t, mgr.Drop(context.Background(), "orders"))
	assert.Equal(t, spyTombstone, spy.markerOf("/orders"))

	fresh, err := mgr.Create(DatabaseConfig{Name: "orders", Engine: EngineKV, Backend: BackendMemory})
	require.NoError(t, err)
	assert.NotEqual(t, old.Incarnation(), fresh.Incarnation())
	assert.Equal(t, fresh.Incarnation(), spy.markerOf("/orders"))
}

// A create that fails before it marks the name live leaves the store
// exactly as the drop left it: fenced. Marking it earlier would unfence a
// name with no database for the dropped incarnation's handles.
func TestManager_Create_FailureBeforeMarkingLeavesTheFence(t *testing.T) {
	failing := &failingBackendFactory{BackendFactory: NewEmbeddedBackendFactory()}
	mgr := NewManager(nil, t.TempDir(), WithBackendFactory(failing))
	spy := &spyReclaimer{}
	mgr.SetKeyspaceReclaimer(spy)
	_, err := mgr.Create(DatabaseConfig{Name: "orders", Engine: EngineKV, Backend: BackendMemory})
	require.NoError(t, err)
	require.NoError(t, mgr.Drop(context.Background(), "orders"))

	failing.err = errors.New("disk full")
	_, err = mgr.Create(DatabaseConfig{Name: "orders", Engine: EngineKV, Backend: BackendMemory})
	require.ErrorIs(t, err, failing.err)
	assert.Equal(t, spyTombstone, spy.markerOf("/orders"), "a failed create must not unfence the dropped name")
}

// A marker that cannot be written fails the create, and nothing of it is
// left: no catalog entry, no directory.
func TestManager_Create_UnfenceFailureCreatesNothing(t *testing.T) {
	mgr, spy := newReclaimManager(t, "orders")
	spy.unfenceErr = errors.New("store unavailable")

	_, err := mgr.Create(DatabaseConfig{Name: "billing", Engine: EngineKV, Backend: BackendMemory})
	var fenceErr *KeyspaceFenceError
	require.ErrorAs(t, err, &fenceErr)
	_, getErr := mgr.Get("billing")
	var notFound *NotFoundError
	assert.ErrorAs(t, getErr, &notFound)
	_, statErr := os.Stat(mgr.dbDir("billing"))
	assert.True(t, os.IsNotExist(statErr), "a failed create must not leave its directory behind")
}

// With no reclaimer there is no store to mark, so the database has no
// incarnation of its own and is live while its marker is absent.
func TestManager_Create_WithoutAReclaimerHasNoIncarnation(t *testing.T) {
	mgr := NewManager(nil, t.TempDir())
	db, err := mgr.Create(DatabaseConfig{Name: "orders", Engine: EngineKV, Backend: BackendMemory})
	require.NoError(t, err)
	assert.Empty(t, db.Incarnation())
}

// Two creates of one name never interleave: the loser is refused before it
// can mark its own incarnation live over the winner's.
func TestManager_Create_ConcurrentCreatesOfOneNameAreSerialized(t *testing.T) {
	mgr, spy := newReclaimManager(t, "orders")
	release := make(chan struct{})
	blocking := &blockingBackendFactory{BackendFactory: NewEmbeddedBackendFactory(), entered: make(chan struct{}), release: release}
	mgr.factory = blocking

	done := make(chan error, 1)
	go func() {
		_, err := mgr.Create(DatabaseConfig{Name: "billing", Engine: EngineKV, Backend: BackendMemory})
		done <- err
	}()
	<-blocking.entered

	second := make(chan error, 1)
	go func() {
		_, err := mgr.Create(DatabaseConfig{Name: "billing", Engine: EngineKV, Backend: BackendMemory})
		second <- err
	}()
	select {
	case err := <-second:
		var exists *AlreadyExistsError
		require.ErrorAs(t, err, &exists)
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("a second create of the same name was not refused while the first was in flight")
	}

	close(release)
	require.NoError(t, <-done)
	winner, err := mgr.Get("billing")
	require.NoError(t, err)
	assert.Equal(t, winner.Incarnation(), spy.markerOf("/billing"))
}

// failingBackendFactory fails Create once err is set.
type failingBackendFactory struct {
	BackendFactory
	err error
}

func (f *failingBackendFactory) Create(cfg DatabaseConfig, dataDir string) (Backend, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.BackendFactory.Create(cfg, dataDir)
}

// blockingBackendFactory holds the first Create until released.
type blockingBackendFactory struct {
	BackendFactory
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (f *blockingBackendFactory) Create(cfg DatabaseConfig, dataDir string) (Backend, error) {
	f.once.Do(func() {
		close(f.entered)
		<-f.release
	})
	return f.BackendFactory.Create(cfg, dataDir)
}

// The default database cannot be dropped, through any entry point.
func TestManager_Drop_RefusesTheDefaultDatabase(t *testing.T) {
	mgr, spy := newReclaimManager(t, "orders")
	_, err := mgr.Create(DatabaseConfig{Name: tenant.DefaultDatabase, Engine: EngineKV, Backend: BackendMemory})
	require.NoError(t, err)

	var refused *DefaultDatabaseDropError
	require.ErrorAs(t, mgr.Drop(context.Background(), tenant.DefaultDatabase), &refused)
	assert.Contains(t, refused.Error(), tenant.DefaultDatabase)

	ctx := tenant.WithTenantID(context.Background(), "acme")
	require.ErrorAs(t, mgr.DropForTenant(ctx, tenant.DefaultDatabase), &refused)

	fenced, _, _ := spy.fenceCalls()
	assert.Empty(t, fenced, "a refused drop must not fence anything")
	_, getErr := mgr.Get(tenant.DefaultDatabase)
	assert.NoError(t, getErr)
}

func TestCheckDroppable(t *testing.T) {
	var refused *DefaultDatabaseDropError
	assert.ErrorAs(t, CheckDroppable(tenant.DefaultDatabase), &refused)
	assert.NoError(t, CheckDroppable("orders"))
	assert.NoError(t, CheckDroppable(""))
}

// GetForTenantIgnoringFence resolves the owner's own fenced database -- the
// one lookup a resumed drop needs -- and refuses everyone else exactly as
// GetForTenant does.
func TestManager_GetForTenantIgnoringFence(t *testing.T) {
	mgr, _ := newReclaimManager(t, "orders")
	owner := tenant.WithTenantID(context.Background(), "acme")
	db, err := mgr.CreateForTenant(owner, DatabaseConfig{Name: "billing", Engine: EngineKV, Backend: BackendMemory})
	require.NoError(t, err)
	db.SetDropping(true)

	got, err := mgr.GetForTenantIgnoringFence(owner, "billing")
	require.NoError(t, err, "the owner's fenced database must resolve for its own drop")
	assert.Same(t, db, got)

	var crossTenant *NotFoundError
	_, err = mgr.GetForTenantIgnoringFence(tenant.WithTenantID(context.Background(), "other"), "billing")
	assert.ErrorAs(t, err, &crossTenant, "another tenant's database must be indistinguishable from a missing one (SEC-8(b))")

	var notFound *NotFoundError
	_, err = mgr.GetForTenantIgnoringFence(owner, "missing")
	assert.ErrorAs(t, err, &notFound)

	// No tenant is the system tenant, held to the one ownership rule rather
	// than refused: acme's database is missing to it.
	var systemNotFound *NotFoundError
	_, err = mgr.GetForTenantIgnoringFence(context.Background(), "billing")
	assert.ErrorAs(t, err, &systemNotFound)
}
