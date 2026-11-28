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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
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
	// incarnations records the incarnation each reclamation named.
	incarnations []string
	err          error
	before       func()

	// fenced and unfenced record FenceDatabaseKeyspace and
	// UnfenceDatabaseKeyspace calls as "<tenant>/<database>"; fenceErr and
	// unfenceErr make them fail. events records every call in order, so a
	// test can assert the fence precedes the reclamation.
	//
	// marker models the store: the value each database's fence marker
	// holds ("" absent). A failing call is modelled the way a Raft proposal
	// fails in production -- the write COMMITS and the caller is still told
	// it failed (a timeout, a lost leader) -- so marker changes whether or
	// not the call reports an error. A test double that dropped the write
	// on error would assert that a failed fence needs no rollback, which is
	// the defect.
	fenced     []string
	unfenced   []string
	fenceErr   error
	unfenceErr error
	events     []string
	marker     map[string]string

	// reclaimed records each reclamation whose deletes the store applied:
	// the marker held the tombstone when they reached it.
	reclaimed []string

	// unfenceHonorsCtx makes UnfenceDatabaseKeyspace fail on a done
	// context, as a real replicated store does.
	unfenceHonorsCtx bool

	// beforeUnfence runs at the start of UnfenceDatabaseKeyspace, before
	// the store applies it: where a test interleaves another node's drop
	// taking this one over.
	beforeUnfence func()
}

// spyTombstone is the value the spy's fence leaves in its marker.
const spyTombstone = "<dropped>"

func (s *spyReclaimer) FenceDatabaseKeyspace(_ context.Context, tenantID, database, incarnation, claim string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fenced = append(s.fenced, tenantID+"/"+database)
	s.events = append(s.events, "fence")
	// The store swaps the marker to this claim's tombstone only from
	// incarnation (absent, for the empty one) or from a tombstone, which
	// a takeover re-stamps with its own claim; any other value is a
	// successor's live incarnation and nothing is written.
	key := tenantID + "/" + database
	if current := s.marker[key]; current != incarnation && !strings.HasPrefix(current, spyTombstone) {
		return &DropSupersededError{Name: database, TenantID: tenantID}
	}
	s.setMarker(key, spyTombstone+claim)
	return s.fenceErr
}

func (s *spyReclaimer) UnfenceDatabaseKeyspace(ctx context.Context, tenantID, database, incarnation, claim string) error {
	if s.beforeUnfence != nil {
		s.beforeUnfence()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unfenced = append(s.unfenced, tenantID+"/"+database)
	s.events = append(s.events, "unfence")
	if s.unfenceHonorsCtx && ctx.Err() != nil {
		return ctx.Err()
	}
	if s.unfenceErr != nil {
		return s.unfenceErr
	}
	// The store claims the marker only from absent or a tombstone, or
	// re-marks the incarnation it already holds; another live incarnation
	// is a database of this name created elsewhere and is left alone. A
	// rollback (a non-empty claim) takes the marker out of its own claim's
	// tombstone only: another claim's is a drop that took it over.
	key := tenantID + "/" + database
	current := s.marker[key]
	if current != "" && !strings.HasPrefix(current, spyTombstone) && current != incarnation {
		return &AlreadyExistsError{Name: database, Elsewhere: true}
	}
	if claim != "" && strings.HasPrefix(current, spyTombstone) && current != spyTombstone+claim {
		return &DropSupersededError{Name: database, TenantID: tenantID}
	}
	s.setMarker(key, incarnation)
	return nil
}

// setMarker records a marker value. The caller holds s.mu.
func (s *spyReclaimer) setMarker(key, value string) {
	if s.marker == nil {
		s.marker = map[string]string{}
	}
	s.marker[key] = value
}

// markerOf returns the marker value the spy's store holds for key.
func (s *spyReclaimer) markerOf(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.marker[key]
}

// isFenced reports whether key's marker holds a tombstone, of any drop's
// claim.
func (s *spyReclaimer) isFenced(key string) bool {
	return strings.HasPrefix(s.markerOf(key), spyTombstone)
}

func (s *spyReclaimer) fenceCalls() (fenced, unfenced, events []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.fenced...), append([]string(nil), s.unfenced...), append([]string(nil), s.events...)
}

func (s *spyReclaimer) ReclaimDatabaseKeyspace(_ context.Context, tenantID, database, incarnation, claim string) error {
	s.mu.Lock()
	s.incarnations = append(s.incarnations, incarnation)
	s.events = append(s.events, "reclaim")
	s.tenants = append(s.tenants, tenantID)
	s.names = append(s.names, database)
	before, err := s.before, s.err
	s.mu.Unlock()
	if before != nil {
		before()
	}
	// Every delete carries the guard that the marker holds this drop's
	// claim's tombstone, evaluated where the delete applies: read it after
	// before(), which is where a test interleaves another node's drop and
	// create.
	s.mu.Lock()
	fenced := s.marker[tenantID+"/"+database] == spyTombstone+claim
	if fenced && err == nil {
		s.reclaimed = append(s.reclaimed, tenantID+"/"+database)
	}
	s.mu.Unlock()
	if !fenced {
		return &DropSupersededError{Name: database, TenantID: tenantID}
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
	mgr := newCatalogManager(nil, t.TempDir())
	spy := &spyReclaimer{}
	mgr.SetKeyspaceReclaimer(spy)
	_, err := mgr.Create(DatabaseConfig{Name: name, Backend: BackendMemory})
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

// TestManager_Drop_ReclaimsTheDroppedIncarnation: the reclamation names the
// incarnation being dropped, so it clears only what that incarnation set --
// never a record a database created again under the name wrote.
func TestManager_Drop_ReclaimsTheDroppedIncarnation(t *testing.T) {
	mgr, spy := newReclaimManager(t, "reports")
	db, err := mgr.Get("reports")
	require.NoError(t, err)
	want := db.Incarnation()
	require.NotEmpty(t, want)

	require.NoError(t, mgr.Drop(context.Background(), "reports"))

	spy.mu.Lock()
	defer spy.mu.Unlock()
	assert.Equal(t, []string{want}, spy.incarnations)
}

// TestManager_Drop_ReclaimsUnderTheDatabasesOwnTenant pins the scope. The
// keys are addressed by the composite (tenant, database) namespace, so a
// reclamation handed the wrong tenant deletes nothing and reports success.
func TestManager_Drop_ReclaimsUnderTheDatabasesOwnTenant(t *testing.T) {
	mgr := newCatalogManager(nil, t.TempDir())
	spy := &spyReclaimer{}
	mgr.SetKeyspaceReclaimer(spy)
	_, err := mgr.Create(DatabaseConfig{
		Name: "reports", Backend: BackendMemory, TenantID: "acme",
	})
	require.NoError(t, err)

	require.NoError(t, mgr.Drop(context.Background(), "reports"))

	tenants, names := spy.calls()
	assert.Equal(t, []string{"acme"}, tenants)
	assert.Equal(t, []string{"reports"}, names)
}

// TestManager_Drop_ReclaimsBeforeRemovingTheCatalogRecord is the ordering
// test, and it is the whole defect #412 fixed plus the fence #482 added on
// top of it.
//
// Until #412, Drop removed the catalog entry FIRST. That is what made the
// residue permanent rather than merely present: once the record is gone
// nothing in the system names the keyspace again, so no later drop, no
// startup scan and no operator command can reach it. Reclaiming before the
// catalog record goes means the worst an interrupted drop can leave is a
// fenced, empty database, which a re-issued drop finishes.
//
// #412 alone left Manager.Get resolving the database throughout the
// reclamation, which is exactly the window #482 reports: a write landing in
// that window survived, orphaned, once the catalog entry was removed. This
// test now pins the fenced replacement: Get refuses DURING the reclamation
// (so a concurrent write is refused rather than served into a keyspace
// already being reclaimed), while the durable catalog record outlives it
// (so a retry can still find the database).
func TestManager_Drop_ReclaimsBeforeRemovingTheCatalogRecord(t *testing.T) {
	mgr, spy := newReclaimManager(t, "reports")

	var getErrDuringReclaim error
	var dirPresentDuringReclaim bool
	spy.before = func() {
		_, getErrDuringReclaim = mgr.Get("reports")
		_, statErr := os.Stat(mgr.dbDir("", "reports"))
		dirPresentDuringReclaim = statErr == nil
	}

	require.NoError(t, mgr.Drop(context.Background(), "reports"))

	var dropping *DroppingError
	require.ErrorAs(t, getErrDuringReclaim, &dropping,
		"a caller resolving the database while its data is being reclaimed must be refused, not served into a keyspace already being reclaimed (go-qrdb#482)")
	assert.Equal(t, "reports", dropping.Name)
	assert.True(t, dirPresentDuringReclaim,
		"the durable catalog record must outlive the reclamation, so an interrupted drop is retryable")

	_, err := mgr.Get("reports")
	var notFound *NotFoundError
	assert.ErrorAs(t, err, &notFound, "the catalog entry must be gone once the drop completes")
	_, statErr := os.Stat(mgr.dbDir("", "reports"))
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
	_, statErr := os.Stat(mgr.dbDir("", "reports"))
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
	mgr := newCatalogManager(nil, t.TempDir())
	require.Nil(t, mgr.KeyspaceReclaimer())
	_, err := mgr.Create(DatabaseConfig{Name: "solo", Backend: BackendMemory})
	require.NoError(t, err)

	require.NoError(t, mgr.Drop(context.Background(), "solo"))

	_, getErr := mgr.Get("solo")
	assert.Error(t, getErr)
	_, statErr := os.Stat(mgr.dbDir("", "solo"))
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
	mgr := newCatalogManager(nil, t.TempDir(), WithKeyspaceReclaimer(spy))
	require.Same(t, spy, mgr.KeyspaceReclaimer())

	_, err := mgr.Create(DatabaseConfig{Name: "reports", Backend: BackendMemory})
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
	mgr := newCatalogManager(nil, t.TempDir())
	spy := &spyReclaimer{}
	mgr.SetKeyspaceReclaimer(spy)
	_, err := mgr.Create(DatabaseConfig{
		Name: "reports", Backend: BackendMemory, TenantID: "acme",
	})
	require.NoError(t, err)

	ctx := tenantContext("acme")
	require.NoError(t, mgr.DropForTenant(ctx, "reports"))

	tenants, names := spy.calls()
	assert.Equal(t, []string{"acme"}, tenants)
	assert.Equal(t, []string{"reports"}, names)
}

// ---------------------------------------------------------------------------
// go-qrdb#482: fencing a database against concurrent writes during Drop.
//
// #412 (above) closed the window between the catalog record going first and
// the reclamation running at all. It left one window open: Manager.Get kept
// resolving the database throughout the reclamation, so a write landing
// after the keyspace was scanned but before the catalog record was removed
// survived, orphaned. These tests pin the fence Drop now sets before it
// reclaims anything (step 0), which closes that window by making Get refuse
// for the duration of the drop.
// ---------------------------------------------------------------------------

// TestManager_Drop_ConcurrentWriteIsRefusedNotOrphaned is the failing-first
// test for go-qrdb#482: a write that races a Drop must be refused before it
// can reach the keyspace, never served successfully into one already being
// reclaimed and about to be orphaned by the catalog removal that follows.
//
// The hook runs from inside ReclaimDatabaseKeyspace, i.e. strictly between
// the fence Drop sets in step 0 and the catalog removal in step 4, which is
// exactly the race window #482 reports. It resolves the database the same
// way every real write path does -- Manager.Get -- and, if that ever
// unexpectedly succeeds, proves the failure mode by performing the write
// the fence exists to prevent.
func TestManager_Drop_ConcurrentWriteIsRefusedNotOrphaned(t *testing.T) {
	mgr, spy := newReclaimManager(t, "reports")

	var attempted bool
	var writeErr error
	spy.before = func() {
		attempted = true
		db, getErr := mgr.Get("reports")
		if getErr != nil {
			writeErr = getErr
			return
		}
		// Only reachable if the fence failed to hold. Performing the write
		// demonstrates the exact defect #482 reports rather than merely
		// asserting Get failed.
		writeErr = db.Backend().Put(context.Background(), []byte("orders/17"), []byte("v"))
	}

	require.NoError(t, mgr.Drop(context.Background(), "reports"))

	require.True(t, attempted, "the race this test exercises was never reached")
	var dropping *DroppingError
	require.ErrorAs(t, writeErr, &dropping,
		"a write racing the drop must be refused with DroppingError, never served into a keyspace already being reclaimed")
	assert.Equal(t, "reports", dropping.Name)
}

// TestManager_Get_RefusesDroppingDatabase is the direct, non-racy unit test
// for the fence Manager.Get enforces: once a database's Dropping flag is
// set, Get refuses with DroppingError even though the catalog entry
// (m.databases) still holds it and Status is untouched.
func TestManager_Get_RefusesDroppingDatabase(t *testing.T) {
	mgr := newTestManager(t)
	db, err := mgr.Create(DatabaseConfig{Name: "reports", Backend: BackendMemory})
	require.NoError(t, err)

	db.SetDropping(true)

	_, err = mgr.Get("reports")
	var dropping *DroppingError
	require.ErrorAs(t, err, &dropping)
	assert.Equal(t, "reports", dropping.Name)
	assert.Equal(t, StatusOpen, db.Status(),
		"the fence must not disturb Status, which Close (Drop step 2) still depends on")
}

// TestManager_Drop_PersistsFenceAcrossRestart proves the fence is durable,
// not merely in-memory: a second, independent Manager opening the same
// on-disk catalog while the first Drop is still inside its reclamation --
// standing in for a crash between Drop's step 0 (mark) and step 4 (remove
// the catalog record) -- must see the database fenced, not as an ordinary
// open database a write could reach.
func TestManager_Drop_PersistsFenceAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	mgr := newCatalogManager(nil, dir)
	spy := &spyReclaimer{}
	mgr.SetKeyspaceReclaimer(spy)
	_, err := mgr.Create(DatabaseConfig{Name: "reports", Backend: BackendMemory})
	require.NoError(t, err)

	var restartErr error
	spy.before = func() {
		restarted := newCatalogManager(nil, dir)
		require.NoError(t, restarted.OpenAll(context.Background()))
		_, restartErr = restarted.Get("reports")
	}

	require.NoError(t, mgr.Drop(context.Background(), "reports"))

	var dropping *DroppingError
	require.ErrorAs(t, restartErr, &dropping,
		"a node that restarts mid-drop must reopen the database fenced, not as an ordinary database a write could reach")
}

// TestManager_Drop_ReclaimFailureRestoresPriorStatusEvenWhenSealed extends
// TestManager_Drop_ReclaimFailureLeavesEverythingIntact: the rollback on a
// failed reclaim must restore whatever status the database had before Drop
// began, not unconditionally StatusOpen -- a sealed database whose drop
// failed must come back sealed, not spuriously unsealed.
func TestManager_Drop_ReclaimFailureRestoresPriorStatusEvenWhenSealed(t *testing.T) {
	mgr, spy := newReclaimManager(t, "reports")
	spy.err = errors.New("store unreachable")

	db, err := mgr.Get("reports")
	require.NoError(t, err)
	db.SetStatus(StatusSealed)

	dropErr := mgr.Drop(context.Background(), "reports")
	var reclaimErr *KeyspaceReclaimError
	require.ErrorAs(t, dropErr, &reclaimErr)

	got, getErr := mgr.Get("reports")
	require.NoError(t, getErr)
	assert.Equal(t, StatusSealed, got.Status(),
		"a failed drop must restore the exact prior status, not force the database open")
}

// TestManager_Drop_ReclaimFailureClearsThePersistedFence proves the rollback
// on a failed reclaim is durable too: a restart after a failed drop must see
// an ordinary, resolvable database, not one still fenced from the attempt
// that failed.
func TestManager_Drop_ReclaimFailureClearsThePersistedFence(t *testing.T) {
	dir := t.TempDir()
	mgr := newCatalogManager(nil, dir)
	spy := &spyReclaimer{}
	mgr.SetKeyspaceReclaimer(spy)
	spy.err = errors.New("store unreachable")
	_, err := mgr.Create(DatabaseConfig{Name: "reports", Backend: BackendMemory})
	require.NoError(t, err)

	require.Error(t, mgr.Drop(context.Background(), "reports"))

	restarted := newCatalogManager(nil, dir)
	require.NoError(t, restarted.OpenAll(context.Background()))
	got, getErr := restarted.Get("reports")
	require.NoError(t, getErr, "a restart after a failed drop must not still see the database fenced")
	assert.Equal(t, StatusOpen, got.Status())
}

// A failed drop reads its own claim from the catalog, and another node's
// drop takes the claim over and re-fences the store before the rollback's
// unfence lands. The unfence names the failed drop's claim, so the store
// refuses it: the takeover's fence stays, and the failed drop reports that
// it did not roll back and leaves the database fenced (go-qrdb#893).
func TestManager_Drop_RollbackAfterATakeoverLeavesItsFence(t *testing.T) {
	mgr, spy := newReclaimManager(t, "reports")
	db, err := mgr.Get("reports")
	require.NoError(t, err)
	incarnation := db.Incarnation()
	spy.err = errors.New("store unreachable")
	var once sync.Once
	spy.beforeUnfence = func() {
		once.Do(func() {
			require.NoError(t, spy.FenceDatabaseKeyspace(context.Background(), "", "reports", incarnation, "claim-b"))
		})
	}

	err = mgr.Drop(context.Background(), "reports")
	var reclaimErr *KeyspaceReclaimError
	require.ErrorAs(t, err, &reclaimErr)
	assert.False(t, reclaimErr.RolledBack, "a rollback the store refused is not a rollback")
	assert.Equal(t, spyTombstone+"claim-b", spy.markerOf("/reports"), "the takeover's fence must survive the rollback")
	var dropping *DroppingError
	_, err = mgr.Get("reports")
	assert.ErrorAs(t, err, &dropping, "the database stays fenced for the drop that took it over")
}

// Without a takeover the rollback clears the fence this drop stamped.
func TestManager_Drop_RollbackClearsItsOwnClaimsFence(t *testing.T) {
	mgr, spy := newReclaimManager(t, "reports")
	db, err := mgr.Get("reports")
	require.NoError(t, err)
	incarnation := db.Incarnation()
	spy.err = errors.New("store unreachable")

	err = mgr.Drop(context.Background(), "reports")
	var reclaimErr *KeyspaceReclaimError
	require.ErrorAs(t, err, &reclaimErr)
	assert.True(t, reclaimErr.RolledBack)
	assert.Equal(t, incarnation, spy.markerOf("/reports"))
	_, err = mgr.Get("reports")
	assert.NoError(t, err)
}

// TestManager_Drop_RetryAfterRestartCompletesTheDrop is the go-qrdb#482
// finding (f) regression: a crash between Drop's step 0 (fence, persisted)
// and step 4 (remove the catalog record) leaves the fence set with NONE of
// Drop's rollback having run -- unlike a controlled reclaim failure, a real
// crash never reaches it. The database.Dropping/Status split is what makes
// the retry work: a restart reopens the database as StatusOpen (Status was
// never touched by the fence) with Dropping true, so the retried Drop's own
// step 2 still finds an open backend to close instead of skipping Close
// because Status reads as though a drop already got that far.
func TestManager_Drop_RetryAfterRestartCompletesTheDrop(t *testing.T) {
	dir := t.TempDir()
	mgr := newCatalogManager(nil, dir)
	spy := &spyReclaimer{}
	mgr.SetKeyspaceReclaimer(spy)
	db, err := mgr.Create(DatabaseConfig{Name: "reports", Backend: BackendMemory})
	require.NoError(t, err)

	// Simulate the crash directly, rather than through a failing
	// reclaimer: set the fence exactly as Drop's step 0 does and stop,
	// never running reclaim, close, or Drop's own rollback.
	require.NoError(t, mgr.claimDrop(context.Background(), db, "test-claim"))
	db.SetDropping(true)

	restarted := newCatalogManager(nil, dir)
	restarted.SetKeyspaceReclaimer(spy)
	require.NoError(t, restarted.OpenAll(context.Background()))

	_, err = restarted.Get("reports")
	var dropping *DroppingError
	require.ErrorAs(t, err, &dropping, "a restart after a crash mid-drop must reopen the database fenced")

	require.NoError(t, restarted.Drop(context.Background(), "reports"),
		"the retry must complete the drop the crash interrupted")

	_, names := spy.calls()
	assert.Equal(t, []string{"reports"}, names, "the retry must reclaim the keyspace the crash never reached")

	_, err = restarted.Get("reports")
	var notFound *NotFoundError
	require.ErrorAs(t, err, &notFound, "the retried drop must complete: the catalog entry is gone")
	_, statErr := os.Stat(restarted.dbDir("", "reports"))
	assert.True(t, os.IsNotExist(statErr))
}

// TestManager_DropForTenant_RetryAfterRestartCompletesTheDrop is go-qrdb#482
// finding (g): DropForTenant's ownership check must not route through Get,
// or the owning tenant could never retry a drop a restart left fenced --
// Get refuses a dropping database for everyone, owner included.
func TestManager_DropForTenant_RetryAfterRestartCompletesTheDrop(t *testing.T) {
	dir := t.TempDir()
	mgr := newCatalogManager(nil, dir)
	spy := &spyReclaimer{}
	mgr.SetKeyspaceReclaimer(spy)
	db, err := mgr.Create(DatabaseConfig{
		Name: "reports", Backend: BackendMemory, TenantID: "acme",
	})
	require.NoError(t, err)
	require.NoError(t, mgr.claimDrop(context.Background(), db, "test-claim"))
	db.SetDropping(true)

	restarted := newCatalogManager(nil, dir)
	restarted.SetKeyspaceReclaimer(spy)
	require.NoError(t, restarted.OpenAll(context.Background()))

	ctx := tenantContext("acme")

	// Get refuses even the owner -- pinning why DropForTenant cannot use it.
	_, err = restarted.GetForTenant(ctx, "reports")
	var dropping *DroppingError
	require.ErrorAs(t, err, &dropping)

	require.NoError(t, restarted.DropForTenant(ctx, "reports"),
		"the owning tenant must be able to retry the drop after a restart")

	_, err = restarted.Get("reports")
	var notFound *NotFoundError
	require.ErrorAs(t, err, &notFound)
}

// TestManager_DropForTenant_CrossTenantDeniedEvenWhileDropping guards the
// bypass DropForTenant needs (it checks ownership directly against
// m.databases instead of through Get, see its doc comment): a non-owner
// must still be refused -- with NotFoundError, exactly as a missing database
// is, never revealing that the name is held by someone else -- dropping
// database or not. The bypass exists to let the OWNER retry, not to skip
// ownership enforcement.
func TestManager_DropForTenant_CrossTenantDeniedEvenWhileDropping(t *testing.T) {
	mgr, _ := newReclaimManager(t, "reports")
	db, err := mgr.Get("reports")
	require.NoError(t, err)
	db.SetDropping(true)

	err = mgr.DropForTenant(tenantContext("mallory"), "reports")
	var notFound *NotFoundError
	require.ErrorAs(t, err, &notFound)
}

// TestManager_UpdateMethods_RefuseWhileDropping is go-qrdb#482 finding (e)'s
// refusal half: every non-tenant Update* method resolves the database
// directly against m.databases (like Get, but not through it), and each
// must still refuse a dropping database rather than silently mutating its
// configuration mid-teardown.
func TestManager_UpdateMethods_RefuseWhileDropping(t *testing.T) {
	tests := []struct {
		name string
		call func(mgr *Manager) error
	}{
		{"UpdateProtocol", func(mgr *Manager) error {
			return mgr.UpdateProtocol("reports", ProtocolConfig{MySQL: true})
		}},
		{"UpdateEtcd", func(mgr *Manager) error {
			return mgr.UpdateEtcd("reports", nil)
		}},
		{"UpdateRaft", func(mgr *Manager) error {
			return mgr.UpdateRaft("reports", nil)
		}},
		{"UpdateReplicas", func(mgr *Manager) error {
			return mgr.UpdateReplicasForTenant(context.Background(), "reports", 3)
		}},
		{"UpdateBarrier", func(mgr *Manager) error {
			return mgr.UpdateBarrier("reports", nil)
		}},
		{"UpdateConnection", func(mgr *Manager) error {
			return mgr.UpdateConnection("reports", nil)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := newTestManager(t)
			db, err := mgr.Create(DatabaseConfig{Name: "reports", Backend: BackendMemory})
			require.NoError(t, err)
			// A drop fences in memory and in the catalog, which a
			// tenant-scoped lookup revalidates against.
			db.SetDropping(true)
			require.NoError(t, mgr.claimDrop(context.Background(), db, "claim"))

			err = tt.call(mgr)
			var dropping *DroppingError
			require.ErrorAs(t, err, &dropping)
		})
	}
}

// TestManager_RewriteMeta_PreservesTheDropFence is go-qrdb#482 finding (e)'s
// data-loss half, isolated from the refusal added above: even if something
// calls rewriteMeta directly through a handle that does not know the
// database is fenced -- another node's, or one that resolved it before the
// fence -- it must not persist Dropping=false, or a crash immediately
// afterward would restart into a database that looks like an ordinary open
// one. It refuses, and fences the handle.
func TestManager_RewriteMeta_PreservesTheDropFence(t *testing.T) {
	dir := t.TempDir()
	mgr := newCatalogManager(nil, dir)
	db, err := mgr.Create(DatabaseConfig{Name: "reports", Backend: BackendMemory})
	require.NoError(t, err)
	require.NoError(t, mgr.claimDrop(context.Background(), db, "test-claim"))

	err = mgr.rewriteMeta(db, func(meta *DatabaseMeta) { meta.Replicas = 3 })
	var refused *DroppingError
	require.ErrorAs(t, err, &refused)
	require.True(t, db.Dropping(), "a refused rewrite fences the handle that made it")

	restarted := newCatalogManager(nil, dir)
	require.NoError(t, restarted.OpenAll(context.Background()))
	_, err = restarted.Get("reports")
	var dropping *DroppingError
	require.ErrorAs(t, err, &dropping, "rewriteMeta must not clear a fence it did not set")
}

// ---------------------------------------------------------------------------
// go-qrdb#482 round 3's second review, finding 10: Drop's step 0 used to
// write the DURABLE fence (the catalog record) before the IN-MEMORY one
// (db.SetDropping), leaving a window in which a concurrent Update* call
// that had already passed loadDatabase (observing Dropping()==false) could
// run rewriteMeta -- which always reads db.Dropping() fresh, never a cached
// value -- and silently clobber the just-committed durable fence back to
// false. Reordering closes the window: loadDatabase refuses the INSTANT
// the in-memory flag flips, so nothing can reach rewriteMeta while
// Dropping() still reads false and the durable write has not committed yet.
// ---------------------------------------------------------------------------

// TestManager_Drop_InMemoryFenceAloneAlreadyRefusesUpdates is the direct
// proof the reordering fix depends on: TestManager_UpdateMethods_
// RefuseWhileDropping already establishes it generally, but this pins it
// specifically as "step 0's first half is already sufficient on its own" --
// db.SetDropping(true) with NO durable write performed at all yet still
// refuses every Update* method, so a concurrent caller can never observe
// the in-memory flag false while racing to persist a stale rewriteMeta over
// Drop's own durable write moments later.
func TestManager_Drop_InMemoryFenceAloneAlreadyRefusesUpdates(t *testing.T) {
	mgr := newTestManager(t)
	db, err := mgr.Create(DatabaseConfig{Name: "reports", Backend: BackendMemory})
	require.NoError(t, err)

	// Simulate the instant immediately after Drop's step 0 flips the
	// in-memory flag, before its own durable write (claimDrop) has
	// run at all.
	db.SetDropping(true)

	err = mgr.UpdateProtocol("reports", ProtocolConfig{MySQL: true})
	var dropping *DroppingError
	require.ErrorAs(t, err, &dropping, "an Update* call must already be refused before any durable write for this drop has committed")
}

// TestManager_Drop_StaleDropperLeavesTheSuccessorIntact drives the takeover
// go-qrdb#893 describes through two managers sharing one catalog and one
// store: node A fences "orders" and stalls before its reclamation reaches
// the store; meanwhile node B takes the drop over, finishes it, and creates
// the name again. A resumes into a store whose marker holds B's
// incarnation, and nothing of B's is deleted, closed or uncatalogued.
func TestManager_Drop_StaleDropperLeavesTheSuccessorIntact(t *testing.T) {
	cat, err := NewKVCatalog(kvstore.NewMemoryKVStore(), nil)
	require.NoError(t, err)
	spy := &spyReclaimer{}
	a := NewManager(nil, t.TempDir(), WithKeyspaceReclaimer(spy))
	a.SetCatalog(cat)
	defer func() { _ = a.Close() }()
	b := NewManager(nil, t.TempDir(), WithKeyspaceReclaimer(spy))
	b.SetCatalog(cat)
	defer func() { _ = b.Close() }()

	ctx := tenantContext("tenant-a")
	stale, err := a.CreateForTenant(ctx, validMemoryConfig("orders"))
	require.NoError(t, err)

	var successor *Database
	spy.before = func() {
		spy.mu.Lock()
		spy.before = nil
		spy.mu.Unlock()
		require.NoError(t, b.DropForTenant(ctx, "orders"), "B takes the drop over and finishes it")
		successor, err = b.CreateForTenant(ctx, validMemoryConfig("orders"))
		require.NoError(t, err, "B creates the name again")
	}

	err = a.DropForTenant(ctx, "orders")
	var superseded *DropSupersededError
	require.ErrorAs(t, err, &superseded, "the stale drop must learn it was superseded")
	require.NotEqual(t, stale.Incarnation(), successor.Incarnation())

	spy.mu.Lock()
	reclaimed, marker := append([]string(nil), spy.reclaimed...), spy.marker["tenant-a/orders"]
	spy.mu.Unlock()
	assert.Equal(t, []string{"tenant-a/orders"}, reclaimed, "only B's drop may reach the store")
	assert.Equal(t, successor.Incarnation(), marker, "the successor's incarnation must stay live")

	meta, err := cat.Get(ctx, "tenant-a", "orders")
	require.NoError(t, err)
	assert.Equal(t, successor.Incarnation(), meta.Incarnation, "the successor's record must survive")
	assert.False(t, meta.Dropping, "the stale drop must not fence the successor's record")
	got, err := b.GetForTenant(ctx, "orders")
	require.NoError(t, err)
	assert.Equal(t, StatusOpen, got.Status(), "the successor must stay open")
}

// TestManager_Drop_SupersededLocallyLeavesTheNameAlone covers the window
// after the store: a drop whose reclamation finished, but whose catalog
// record was meanwhile finished by another drop and created again, takes
// none of the node-local steps that act by name.
func TestManager_Drop_SupersededLocallyLeavesTheNameAlone(t *testing.T) {
	cat, err := NewKVCatalog(kvstore.NewMemoryKVStore(), nil)
	require.NoError(t, err)
	spy := &spyReclaimer{}
	m := NewManager(nil, t.TempDir(), WithKeyspaceReclaimer(spy))
	m.SetCatalog(cat)
	defer func() { _ = m.Close() }()

	ctx := tenantContext("tenant-a")
	stale, err := m.CreateForTenant(ctx, validMemoryConfig("orders"))
	require.NoError(t, err)
	dir := m.dbDir("tenant-a", "orders")
	require.NoError(t, os.MkdirAll(dir, 0o700))

	// Another drop finishes the record and a create catalogs the name again
	// while this drop's reclamation is in flight; the successor's store
	// marker is not written yet, so the store still admits the reclamation.
	spy.before = func() {
		meta, err := cat.Get(ctx, "tenant-a", "orders")
		require.NoError(t, err)
		require.NoError(t, cat.Delete(ctx, "tenant-a", "orders", meta.Incarnation, meta.DropClaim))
		require.NoError(t, cat.Create(ctx, DatabaseMeta{ID: "test-id", TenantID: "tenant-a", Name: "orders", Backend: BackendMemory, Incarnation: "successor"}))
	}

	var superseded *DropSupersededError
	require.ErrorAs(t, m.DropForTenant(ctx, "orders"), &superseded)

	_, statErr := os.Stat(dir)
	assert.NoError(t, statErr, "the superseded drop removed the directory the name now belongs to")
	meta, err := cat.Get(ctx, "tenant-a", "orders")
	require.NoError(t, err)
	assert.Equal(t, "successor", meta.Incarnation, "the superseded drop removed the successor's record")
	assert.NotEqual(t, StatusClosed, stale.Status(), "the superseded drop went on to the node-local steps")
}
