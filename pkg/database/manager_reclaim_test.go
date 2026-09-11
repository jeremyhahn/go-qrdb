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
)

// This file covers the drop-ordering half of go-qrdb#412. The reclamation
// itself is service.KeyspaceReclaimService's business and is tested there;
// what is tested here is that Drop calls it, calls it with the right scope,
// calls it BEFORE the catalog record goes, and refuses to report success when
// it fails.

// spyReclaimer records the calls Drop makes and can be made to fail. The
// before hook runs inside the reclamation, which is how a test observes the
// state of the catalog at the moment the data is being removed.
type spyReclaimer struct {
	mu      sync.Mutex
	tenants []string
	names   []string
	err     error
	before  func()
}

func (s *spyReclaimer) ReclaimDatabaseKeyspace(_ context.Context, tenantID, database string) error {
	s.mu.Lock()
	s.tenants = append(s.tenants, tenantID)
	s.names = append(s.names, database)
	before, err := s.before, s.err
	s.mu.Unlock()
	if before != nil {
		before()
	}
	return err
}

func (s *spyReclaimer) calls() (tenants, names []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.tenants...), append([]string(nil), s.names...)
}

// newReclaimManager returns a manager rooted at a temp dir holding one open
// memory-backed database, together with the reclaimer wired into it.
func newReclaimManager(t *testing.T, name string) (*Manager, *spyReclaimer) {
	t.Helper()
	mgr := NewManager(nil, t.TempDir())
	spy := &spyReclaimer{}
	mgr.SetKeyspaceReclaimer(spy)
	_, err := mgr.Create(DatabaseConfig{Name: name, Engine: EngineKV, Backend: BackendMemory})
	require.NoError(t, err)
	return mgr, spy
}

// TestManager_Drop_ReclaimsTheSharedStoreKeyspace is the failing-first test
// for go-qrdb#412 at the manager. Before the fix Drop removed the database's
// directory and nothing else, so for a shared-store database -- whose
// directory holds configuration and no data -- the drop reclaimed nothing at
// all.
func TestManager_Drop_ReclaimsTheSharedStoreKeyspace(t *testing.T) {
	mgr, spy := newReclaimManager(t, "reports")

	require.NoError(t, mgr.Drop(context.Background(), "reports"))

	_, names := spy.calls()
	assert.Equal(t, []string{"reports"}, names,
		"dropping a database must reclaim the data it owns in the shared store, not only its directory")
}

// TestManager_Drop_ReclaimsUnderTheDatabasesOwnTenant pins the scope. The
// keys are addressed by the composite (tenant, database) namespace, so a
// reclamation handed the wrong tenant deletes nothing and reports success.
func TestManager_Drop_ReclaimsUnderTheDatabasesOwnTenant(t *testing.T) {
	mgr := NewManager(nil, t.TempDir())
	spy := &spyReclaimer{}
	mgr.SetKeyspaceReclaimer(spy)
	_, err := mgr.Create(DatabaseConfig{
		Name: "reports", Engine: EngineKV, Backend: BackendMemory, TenantID: "acme",
	})
	require.NoError(t, err)

	require.NoError(t, mgr.Drop(context.Background(), "reports"))

	tenants, names := spy.calls()
	assert.Equal(t, []string{"acme"}, tenants)
	assert.Equal(t, []string{"reports"}, names)
}

// TestManager_Drop_ReclaimsBeforeRemovingTheCatalogRecord is the ordering
// test, and it is the whole defect.
//
// Until this change Drop removed the catalog entry FIRST. That is what made
// the residue permanent rather than merely present: once the record is gone
// nothing in the system names the keyspace again, so no later drop, no
// startup scan and no operator command can reach it. Reclaiming first means
// the worst an interrupted drop can leave is a live, empty database, which a
// re-issued drop finishes.
func TestManager_Drop_ReclaimsBeforeRemovingTheCatalogRecord(t *testing.T) {
	mgr, spy := newReclaimManager(t, "reports")

	var resolvableDuringReclaim bool
	var dirPresentDuringReclaim bool
	spy.before = func() {
		_, err := mgr.Get("reports")
		resolvableDuringReclaim = err == nil
		_, statErr := os.Stat(mgr.dbDir("reports"))
		dirPresentDuringReclaim = statErr == nil
	}

	require.NoError(t, mgr.Drop(context.Background(), "reports"))

	assert.True(t, resolvableDuringReclaim,
		"the database must still be resolvable while its data is being reclaimed, so an interrupted drop is retryable")
	assert.True(t, dirPresentDuringReclaim,
		"the durable catalog record must outlive the reclamation")

	_, err := mgr.Get("reports")
	assert.Error(t, err, "the catalog entry must be gone once the drop completes")
	_, statErr := os.Stat(mgr.dbDir("reports"))
	assert.True(t, os.IsNotExist(statErr))
}

// TestManager_Drop_ReclaimFailureLeavesEverythingIntact holds the refusal.
// Reporting a successful DROP DATABASE over data that is still in the store
// is the defect itself, so a reclamation failure fails the whole drop and
// changes nothing.
func TestManager_Drop_ReclaimFailureLeavesEverythingIntact(t *testing.T) {
	mgr, spy := newReclaimManager(t, "reports")
	boom := errors.New("store unreachable")
	spy.err = boom

	err := mgr.Drop(context.Background(), "reports")

	var want *KeyspaceReclaimError
	require.ErrorAs(t, err, &want)
	assert.Equal(t, "reports", want.Name)
	assert.ErrorIs(t, err, boom)

	db, getErr := mgr.Get("reports")
	require.NoError(t, getErr, "a failed drop must leave the database resolvable")
	assert.Equal(t, StatusOpen, db.Status(), "a failed drop must not have closed the database")
	_, statErr := os.Stat(mgr.dbDir("reports"))
	assert.NoError(t, statErr, "a failed drop must not have removed the directory")
}

// TestManager_Drop_RetryAfterReclaimFailureCompletes drives the interruption
// end to end rather than asserting it: the second drop is what an operator
// issues, and it must finish the job.
func TestManager_Drop_RetryAfterReclaimFailureCompletes(t *testing.T) {
	ctx := context.Background()
	mgr, spy := newReclaimManager(t, "reports")
	spy.err = errors.New("transient")

	require.Error(t, mgr.Drop(ctx, "reports"))

	spy.err = nil
	require.NoError(t, mgr.Drop(ctx, "reports"))

	_, names := spy.calls()
	assert.Equal(t, []string{"reports", "reports"}, names,
		"the retry must re-run the reclamation over whatever the first attempt finished")
	_, err := mgr.Get("reports")
	assert.Error(t, err)
}

// TestManager_Drop_ConcurrentDropsProduceExactlyOneSuccess covers the race
// the reordering opened.
//
// LoadAndDelete used to decide it: the first caller took the entry and the
// second got NotFoundError. Reclaiming first means the entry is still there
// when the second caller arrives, and two successes would be reported for one
// database -- which DropForTenant turns into releasing the tenant's quota
// twice.
func TestManager_Drop_ConcurrentDropsProduceExactlyOneSuccess(t *testing.T) {
	mgr, spy := newReclaimManager(t, "reports")

	release := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once
	spy.before = func() {
		once.Do(func() { close(entered) })
		<-release
	}

	errs := make(chan error, 2)
	go func() { errs <- mgr.Drop(context.Background(), "reports") }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("the first drop never reached the reclamation, so the race this guards is not being exercised")
	}
	go func() { errs <- mgr.Drop(context.Background(), "reports") }()

	// The second caller must be refused without waiting for the first.
	var second error
	select {
	case second = <-errs:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("the second drop blocked behind the first instead of being refused")
	}
	var inFlight *DropInProgressError
	require.ErrorAs(t, second, &inFlight)
	assert.Equal(t, "reports", inFlight.Name)

	close(release)
	require.NoError(t, <-errs)
}

// TestManager_Drop_WithoutAReclaimerStillRemovesTheDirectory is the control
// for a Manager with no shared store behind it -- a standalone
// single-database embedding, where the directory IS the data.
func TestManager_Drop_WithoutAReclaimerStillRemovesTheDirectory(t *testing.T) {
	mgr := NewManager(nil, t.TempDir())
	require.Nil(t, mgr.KeyspaceReclaimer())
	_, err := mgr.Create(DatabaseConfig{Name: "solo", Engine: EngineKV, Backend: BackendMemory})
	require.NoError(t, err)

	require.NoError(t, mgr.Drop(context.Background(), "solo"))

	_, getErr := mgr.Get("solo")
	assert.Error(t, getErr)
	_, statErr := os.Stat(mgr.dbDir("solo"))
	assert.True(t, os.IsNotExist(statErr))
}

// TestManager_Drop_UnknownDatabaseDoesNotReclaim pins that the not-found gate
// still runs first: a typo must not start a reclamation.
func TestManager_Drop_UnknownDatabaseDoesNotReclaim(t *testing.T) {
	mgr, spy := newReclaimManager(t, "reports")

	err := mgr.Drop(context.Background(), "no-such-database")

	var want *NotFoundError
	require.ErrorAs(t, err, &want)
	_, names := spy.calls()
	assert.Empty(t, names)
}

// TestManager_WithKeyspaceReclaimer covers the construction-time option, the
// peer of SetKeyspaceReclaimer.
func TestManager_WithKeyspaceReclaimer(t *testing.T) {
	spy := &spyReclaimer{}
	mgr := NewManager(nil, t.TempDir(), WithKeyspaceReclaimer(spy))
	require.Same(t, spy, mgr.KeyspaceReclaimer())

	_, err := mgr.Create(DatabaseConfig{Name: "reports", Engine: EngineKV, Backend: BackendMemory})
	require.NoError(t, err)
	require.NoError(t, mgr.Drop(context.Background(), "reports"))

	_, names := spy.calls()
	assert.Equal(t, []string{"reports"}, names)
}

// TestManager_DropForTenant_ReclaimsTheKeyspace pins that the tenant-scoped
// drop inherits the reclamation rather than routing around it, which is what
// go-qrdb#413's refusal will rest on: "no databases left" has to mean
// "nothing of this tenant's data remains under that database".
func TestManager_DropForTenant_ReclaimsTheKeyspace(t *testing.T) {
	mgr := NewManager(nil, t.TempDir())
	spy := &spyReclaimer{}
	mgr.SetKeyspaceReclaimer(spy)
	_, err := mgr.Create(DatabaseConfig{
		Name: "reports", Engine: EngineKV, Backend: BackendMemory, TenantID: "acme",
	})
	require.NoError(t, err)

	ctx := tenantContext("acme")
	require.NoError(t, mgr.DropForTenant(ctx, "reports"))

	tenants, names := spy.calls()
	assert.Equal(t, []string{"acme"}, tenants)
	assert.Equal(t, []string{"reports"}, names)
}
