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

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// discardLogger is defined in user_auth_factor_migration_test.go.

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// fakeReclaimClock is a settable clock for WithReclaimClock.
type fakeReclaimClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeReclaimClock() *fakeReclaimClock {
	return &fakeReclaimClock{t: time.Unix(1_700_000_000, 0)}
}

func (c *fakeReclaimClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeReclaimClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// fakeReclaimResolver routes every entry to the store registered for its
// (tenant, database), and owns entries the way the production resolver
// does for StorePlacementShared unless ownsFn overrides it.
type fakeReclaimResolver struct {
	mu      sync.Mutex
	stores  map[string]ReclaimStore
	err     error
	ownsFn  func(ReclaimIndexEntry, bool) bool
	resolve int
}

var _ ReclaimStoreResolver = (*fakeReclaimResolver)(nil)

func newFakeReclaimResolver() *fakeReclaimResolver {
	return &fakeReclaimResolver{stores: make(map[string]ReclaimStore)}
}

func (f *fakeReclaimResolver) route(tenantID, database string, store ReclaimStore) *fakeReclaimResolver {
	f.stores[tenantID+"/"+database] = store
	return f
}

func (f *fakeReclaimResolver) Owns(entry ReclaimIndexEntry, systemLeader bool) bool {
	if f.ownsFn != nil {
		return f.ownsFn(entry, systemLeader)
	}
	return systemLeader
}

func (f *fakeReclaimResolver) ReclaimStore(_ context.Context, entry ReclaimIndexEntry) (ReclaimStore, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolve++
	if f.err != nil {
		return nil, f.err
	}
	store, ok := f.stores[entry.TenantID+"/"+entry.Database]
	if !ok {
		return nil, &ReclaimStoreGoneError{TenantID: entry.TenantID, Database: entry.Database}
	}
	return store, nil
}

func (f *fakeReclaimResolver) resolveCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.resolve
}

// tenantCtx returns a context carrying tenantID, the way the SQL layer binds
// a session's tenant before calling a reclaim-publishing TableService
// operation. It lives in this unconstrained file because callers in every
// build use it, the tagged coverage_extended_test.go among them.
func tenantCtx(tenantID string) context.Context {
	return tenant.WithTenantID(context.Background(), tenantID)
}

// truncatedTable creates acme/shop.t in store through a real TableService,
// writes rows under generation 0, and TRUNCATEs it -- so the job and index
// entry the sweeper sees are exactly what production publishes.
func truncatedTable(t *testing.T, rows int) (*fakeCatalogStore, *TableDef) {
	t.Helper()
	svc, store := newTestTableService()
	ctx := tenantCtx("acme")
	def, err := svc.CreateTable(ctx, &CreateTableRequest{
		Database: "shop", Name: "t", Columns: testColumns(),
		Indexes: []TableIndex{{Name: "idx_email", Columns: []IndexCol{{Name: "email"}}}},
	})
	require.NoError(t, err)
	for i := range rows {
		store.put(tableDataPrefixFor("shop", def.TableID, 0)+strconv.Itoa(i), []byte("row"))
		store.put(tableIndexPrefixFor("shop", def.TableID, 0, def.Indexes[0].Incarnation)+strconv.Itoa(i), nil)
	}
	_, err = svc.TruncateTable(ctx, "shop", "t", true)
	require.NoError(t, err)
	return store, def
}

func newTestSweeper(index ReclaimIndexStore, resolver ReclaimStoreResolver, clk *fakeReclaimClock) *ReclaimSweeper {
	rs := NewReclaimSweeper(index, WithReclaimClock(clk.Now))
	rs.SetStoreResolver(resolver)
	return rs
}

// sweepPastQuiescence observes the pending jobs, lets ReclaimQuiescence
// elapse, and sweeps again.
func sweepPastQuiescence(rs *ReclaimSweeper, clk *fakeReclaimClock) {
	ctx := context.Background()
	rs.SweepOnce(ctx, nil, discardLogger())
	clk.Advance(ReclaimQuiescence)
	rs.SweepOnce(ctx, nil, discardLogger())
}

// ---------------------------------------------------------------------------
// Encoding
// ---------------------------------------------------------------------------

func TestReclaimJobOp_EncodesAsAPutUnderTheJobKey(t *testing.T) {
	job := ReclaimJob{
		ID: "acme:table:shop:1:0:x", TenantID: "acme", Database: "shop",
		KeyPrefixes: []string{"__tbl:shop:1:0/", "__idx:shop:1:0:"}, CreatedAt: time.Now().UTC(),
	}
	op, err := ReclaimJobOp(job)
	require.NoError(t, err)
	assert.Equal(t, OpPut, op.Type)
	assert.Equal(t, reclaimJobKey(job.ID), string(op.Key))

	var decoded ReclaimJob
	require.NoError(t, json.Unmarshal(op.Value, &decoded))
	assert.Equal(t, job.ID, decoded.ID)
	assert.Equal(t, job.TenantID, decoded.TenantID)
	assert.Equal(t, job.KeyPrefixes, decoded.KeyPrefixes)
}

func TestReclaimIndexOp_RoundTripsEveryField(t *testing.T) {
	entry := ReclaimIndexEntry{
		JobID: "acme:table:shop:1:0:x", TenantID: "acme", Database: "shop",
		Placement: StorePlacementNode, Origin: "7",
		Fences: []ReclaimFence{{Key: "m", ModRevision: 3}, {Key: "g", ModRevision: 0}},
	}
	op, err := ReclaimIndexOp(entry)
	require.NoError(t, err)
	assert.Equal(t, reclaimIndexKey(entry.JobID), string(op.Key))

	var decoded ReclaimIndexEntry
	require.NoError(t, json.Unmarshal(op.Value, &decoded))
	assert.Equal(t, entry, decoded)
}

// TestNewReclaimJobID_IsTenantQualifiedAndUniquePerCall pins both halves of
// the index-key collision fix: two tenants' identical labels (their
// dedicated stores allocate the same TableID) never share a key, and neither
// do two attempts by the same tenant.
func TestNewReclaimJobID_IsTenantQualifiedAndUniquePerCall(t *testing.T) {
	a := newReclaimJobID("acme", "table:shop:1:0")
	b := newReclaimJobID("globex", "table:shop:1:0")
	c := newReclaimJobID("acme", "table:shop:1:0")
	assert.True(t, strings.HasPrefix(a, "acme:table:shop:1:0:"))
	assert.True(t, strings.HasPrefix(b, "globex:table:shop:1:0:"))
	assert.NotEqual(t, a, c, "two attempts must never share an ID")
}

// ---------------------------------------------------------------------------
// Reader quiescence
// ---------------------------------------------------------------------------

// TestReclaimSweeper_NothingIsDeletedBeforeQuiescenceElapses is the reader-
// quiescence guarantee: the sweep that first sees a committed job, and every
// sweep until ReclaimQuiescence has elapsed since, deletes nothing -- however
// old the job's own CreatedAt claims to be.
func TestReclaimSweeper_NothingIsDeletedBeforeQuiescenceElapses(t *testing.T) {
	store, def := truncatedTable(t, 3)
	clk := newFakeReclaimClock()
	rs := newTestSweeper(store, newFakeReclaimResolver().route("acme", "shop", store), clk)
	ctx := context.Background()

	rs.SweepOnce(ctx, nil, discardLogger())
	clk.Advance(ReclaimQuiescence - time.Nanosecond)
	rs.SweepOnce(ctx, nil, discardLogger())

	assert.Len(t, store.keysWithPrefix(tableDataPrefixFor("shop", def.TableID, 0)), 3,
		"no key may be deleted before ReclaimQuiescence has elapsed since the job was first observed")
	assert.Len(t, store.keysWithPrefix(reclaimJobPrefix), 1)
	assert.Len(t, store.keysWithPrefix(reclaimIndexPrefix), 1)

	clk.Advance(time.Nanosecond)
	rs.SweepOnce(ctx, nil, discardLogger())
	assert.Empty(t, store.keysWithPrefix(tableDataPrefixFor("shop", def.TableID, 0)))
}

// TestReclaimQuiescence_OutlastsEveryGenerationPin pins the relation the
// guarantee rests on: a reader re-proves its pin after TableGenerationPinTTL,
// so the sweeper must wait strictly longer than that.
func TestReclaimQuiescence_OutlastsEveryGenerationPin(t *testing.T) {
	assert.Greater(t, ReclaimQuiescence, TableGenerationPinTTL)
}

// TestReclaimSweeper_ANewSweeperRestartsTheWait proves observation is per
// sweeper: a sweeper that takes over (leader change, restart) waits its own
// full ReclaimQuiescence rather than inheriting a predecessor's progress.
func TestReclaimSweeper_ANewSweeperRestartsTheWait(t *testing.T) {
	store, def := truncatedTable(t, 2)
	clk := newFakeReclaimClock()
	resolver := newFakeReclaimResolver().route("acme", "shop", store)
	ctx := context.Background()

	first := newTestSweeper(store, resolver, clk)
	first.SweepOnce(ctx, nil, discardLogger())
	clk.Advance(ReclaimQuiescence)

	successor := newTestSweeper(store, resolver, clk)
	successor.SweepOnce(ctx, nil, discardLogger())
	assert.Len(t, store.keysWithPrefix(tableDataPrefixFor("shop", def.TableID, 0)), 2,
		"a successor's first sighting starts its own wait")

	clk.Advance(ReclaimQuiescence)
	successor.SweepOnce(ctx, nil, discardLogger())
	assert.Empty(t, store.keysWithPrefix(tableDataPrefixFor("shop", def.TableID, 0)))
}

// ---------------------------------------------------------------------------
// Draining
// ---------------------------------------------------------------------------

// TestReclaimSweeper_DrainsAProductionPublishedJob drives TruncateTable's own
// output end to end: the old generation's rows AND every index entry at that
// generation go, the new generation is untouched, and the job record and its
// index entry are removed.
func TestReclaimSweeper_DrainsAProductionPublishedJob(t *testing.T) {
	store, def := truncatedTable(t, 4)
	store.put(tableDataPrefixFor("shop", def.TableID, 1)+"live", []byte("row"))
	// Entries of an incarnation the catalog no longer lists (a dropped
	// index), at the truncated generation.
	store.put(tableIndexPrefixFor("shop", def.TableID, 0, 99)+"k", nil)

	clk := newFakeReclaimClock()
	rs := newTestSweeper(store, newFakeReclaimResolver().route("acme", "shop", store), clk)
	sweepPastQuiescence(rs, clk)

	assert.Empty(t, store.keysWithPrefix(tableDataPrefixFor("shop", def.TableID, 0)))
	assert.Empty(t, store.keysWithPrefix(tableIndexGenerationPrefixFor("shop", def.TableID, 0)),
		"every index entry at the old generation goes, catalogued name or not")
	assert.Len(t, store.keysWithPrefix(tableDataPrefixFor("shop", def.TableID, 1)), 1, "the live generation is untouched")
	assert.Empty(t, store.keysWithPrefix(reclaimJobPrefix))
	assert.Empty(t, store.keysWithPrefix(reclaimIndexPrefix))
}

// TestReclaimSweeper_WorkIsBoundedPerSweep proves neither side of a sweep is
// listed whole: no Range asks for more than a page, a sweep deletes at most
// reclaimMaxKeysPerSweep keys, and a job too large for one sweep finishes
// over several without its record being removed early.
func TestReclaimSweeper_WorkIsBoundedPerSweep(t *testing.T) {
	store, def := truncatedTable(t, reclaimMaxKeysPerSweep) // 2x keys: rows + index entries
	clk := newFakeReclaimClock()
	rs := newTestSweeper(store, newFakeReclaimResolver().route("acme", "shop", store), clk)
	sweepPastQuiescence(rs, clk)

	remaining := len(store.keysWithPrefix(tableDataPrefixFor("shop", def.TableID, 0))) +
		len(store.keysWithPrefix(tableIndexGenerationPrefixFor("shop", def.TableID, 0)))
	assert.Equal(t, reclaimMaxKeysPerSweep, remaining, "one sweep deletes exactly its key budget")
	assert.Len(t, store.keysWithPrefix(reclaimJobPrefix), 1, "a partially drained job keeps its record")

	store.mu.Lock()
	for _, limit := range store.rangeLimits {
		assert.Positive(t, limit, "every listing must be bounded")
		assert.LessOrEqual(t, limit, int64(max(reclaimKeyPageSize, reclaimIndexPageSize)))
	}
	store.mu.Unlock()

	// The second budget empties the index keys; only a third listing can
	// confirm both prefixes are empty and retire the record.
	rs.SweepOnce(context.Background(), nil, discardLogger())
	assert.Len(t, store.keysWithPrefix(reclaimJobPrefix), 1)
	rs.SweepOnce(context.Background(), nil, discardLogger())
	assert.Empty(t, store.keysWithPrefix(tableDataPrefixFor("shop", def.TableID, 0)))
	assert.Empty(t, store.keysWithPrefix(tableIndexGenerationPrefixFor("shop", def.TableID, 0)))
	assert.Empty(t, store.keysWithPrefix(reclaimJobPrefix))
	assert.Empty(t, store.keysWithPrefix(reclaimIndexPrefix))
}

// TestReclaimSweeper_FailingEntriesDoNotStarveLaterOnes fills the front of
// the index with more than a page of entries whose store can never be
// resolved and proves a job sorted after all of them is still reached: the
// sweep resumes after the last key it read.
func TestReclaimSweeper_FailingEntriesDoNotStarveLaterOnes(t *testing.T) {
	system := newFakeCatalogStore()
	good := newFakeCatalogStore()
	for i := range reclaimIndexPageSize + 5 {
		op, err := ReclaimIndexOp(ReclaimIndexEntry{
			JobID: "a-failing-" + strconv.Itoa(1000+i), TenantID: "broken", Database: "db",
			Placement: StorePlacementShared,
		})
		require.NoError(t, err)
		system.put(string(op.Key), op.Value)
	}
	publishRawJob(t, system, good, ReclaimJob{ID: "z-good", TenantID: "acme", Database: "shop", KeyPrefixes: []string{"rows/"}}, nil)
	good.put("rows/1", []byte("v"))

	resolver := newFakeReclaimResolver().route("acme", "shop", good)
	resolver.ownsFn = func(e ReclaimIndexEntry, leader bool) bool { return leader }
	failing := &erroringResolver{fakeReclaimResolver: resolver, failTenant: "broken"}
	clk := newFakeReclaimClock()
	rs := newTestSweeper(system, failing, clk)

	ctx := context.Background()
	for range 6 {
		rs.SweepOnce(ctx, nil, discardLogger())
		clk.Advance(ReclaimQuiescence)
	}
	assert.False(t, good.has("rows/1"), "the job after the failing entries must be reached and drained")
	assert.Len(t, system.keysWithPrefix(reclaimIndexPrefix), reclaimIndexPageSize+5,
		"the failing entries are left for a later sweep, not dropped")
}

// erroringResolver fails resolution for one tenant and delegates the rest.
type erroringResolver struct {
	*fakeReclaimResolver
	failTenant string
}

func (e *erroringResolver) ReclaimStore(ctx context.Context, entry ReclaimIndexEntry) (ReclaimStore, error) {
	if entry.TenantID == e.failTenant {
		return nil, errors.New("store unreachable")
	}
	return e.fakeReclaimResolver.ReclaimStore(ctx, entry)
}

// publishRawJob writes an index entry into system and a job record into
// store, the two halves TableService publishes.
func publishRawJob(t *testing.T, system, store *fakeCatalogStore, job ReclaimJob, fences []ReclaimFence) {
	t.Helper()
	indexOp, err := ReclaimIndexOp(ReclaimIndexEntry{
		JobID: job.ID, TenantID: job.TenantID, Database: job.Database,
		Placement: StorePlacementShared, Fences: fences,
	})
	require.NoError(t, err)
	system.put(string(indexOp.Key), indexOp.Value)
	if store != nil {
		jobOp, err := ReclaimJobOp(job)
		require.NoError(t, err)
		store.put(string(jobOp.Key), jobOp.Value)
	}
}

// TestReclaimSweeper_PrunesObservationsOfJobsFinishedElsewhere proves the
// in-memory observation map does not grow without bound: a job another
// sweeper finished is forgotten once the index cursor wraps.
func TestReclaimSweeper_PrunesObservationsOfJobsFinishedElsewhere(t *testing.T) {
	store, _ := truncatedTable(t, 1)
	clk := newFakeReclaimClock()
	rs := newTestSweeper(store, newFakeReclaimResolver().route("acme", "shop", store), clk)
	ctx := context.Background()

	rs.SweepOnce(ctx, nil, discardLogger())
	require.Len(t, rs.progress, 1)

	for _, k := range store.keysWithPrefix(reclaimIndexPrefix) {
		require.NoError(t, store.Delete(ctx, k))
	}
	rs.SweepOnce(ctx, nil, discardLogger())
	rs.SweepOnce(ctx, nil, discardLogger())
	assert.Empty(t, rs.progress)
}

// ---------------------------------------------------------------------------
// Ownership and resolution
// ---------------------------------------------------------------------------

func TestReclaimSweeper_WithoutAResolverTouchesNothing(t *testing.T) {
	store, def := truncatedTable(t, 1)
	clk := newFakeReclaimClock()
	rs := NewReclaimSweeper(store, WithReclaimClock(clk.Now))
	sweepPastQuiescence(rs, clk)
	assert.Len(t, store.keysWithPrefix(tableDataPrefixFor("shop", def.TableID, 0)), 1)
	assert.Len(t, store.keysWithPrefix(reclaimIndexPrefix), 1)
}

// TestReclaimSweeper_SharedEntriesNeedTheSystemLeader proves isLeader gates
// StorePlacementShared entries without stopping the loop: a follower's sweep
// resolves nothing.
func TestReclaimSweeper_SharedEntriesNeedTheSystemLeader(t *testing.T) {
	store, def := truncatedTable(t, 1)
	clk := newFakeReclaimClock()
	resolver := newFakeReclaimResolver().route("acme", "shop", store)
	rs := newTestSweeper(store, resolver, clk)
	ctx := context.Background()
	follower := func() bool { return false }

	rs.SweepOnce(ctx, follower, discardLogger())
	clk.Advance(ReclaimQuiescence)
	rs.SweepOnce(ctx, follower, discardLogger())
	assert.Zero(t, resolver.resolveCalls())
	assert.Len(t, store.keysWithPrefix(tableDataPrefixFor("shop", def.TableID, 0)), 1)

	sweepPastQuiescence(rs, clk)
	assert.Empty(t, store.keysWithPrefix(tableDataPrefixFor("shop", def.TableID, 0)))
}

// TestReclaimSweeper_NonSharedEntriesAreOwnedByTheResolverNotTheLeader
// proves a node-local job is swept by the node that owns it even when that
// node is not the system leader -- the loop runs everywhere.
func TestReclaimSweeper_NonSharedEntriesAreOwnedByTheResolverNotTheLeader(t *testing.T) {
	store, def := truncatedTable(t, 1)
	clk := newFakeReclaimClock()
	resolver := newFakeReclaimResolver().route("acme", "shop", store)
	resolver.ownsFn = func(ReclaimIndexEntry, bool) bool { return true }
	rs := newTestSweeper(store, resolver, clk)
	ctx := context.Background()
	follower := func() bool { return false }

	rs.SweepOnce(ctx, follower, discardLogger())
	clk.Advance(ReclaimQuiescence)
	rs.SweepOnce(ctx, follower, discardLogger())
	assert.Empty(t, store.keysWithPrefix(tableDataPrefixFor("shop", def.TableID, 0)))
}

// TestReclaimSweeper_GoneStoreFinishesTheEntry proves a store the resolver
// definitively reports gone ends the entry without touching any keys.
func TestReclaimSweeper_GoneStoreFinishesTheEntry(t *testing.T) {
	store, def := truncatedTable(t, 1)
	clk := newFakeReclaimClock()
	rs := newTestSweeper(store, newFakeReclaimResolver(), clk)
	rs.SweepOnce(context.Background(), nil, discardLogger())

	assert.Empty(t, store.keysWithPrefix(reclaimIndexPrefix))
	assert.Len(t, store.keysWithPrefix(tableDataPrefixFor("shop", def.TableID, 0)), 1,
		"a gone store's keys belong to whatever removed it")
}

// TestReclaimSweeper_TransientResolveErrorLeavesTheEntry proves any other
// resolution failure is retried, never treated as gone.
func TestReclaimSweeper_TransientResolveErrorLeavesTheEntry(t *testing.T) {
	store, _ := truncatedTable(t, 1)
	clk := newFakeReclaimClock()
	resolver := newFakeReclaimResolver()
	resolver.err = errors.New("barrier sealed")
	rs := newTestSweeper(store, resolver, clk)
	sweepPastQuiescence(rs, clk)
	assert.Len(t, store.keysWithPrefix(reclaimIndexPrefix), 1)
	assert.Len(t, store.keysWithPrefix(reclaimJobPrefix), 1)
}

// ---------------------------------------------------------------------------
// Index entries with no job: the fence proof
// ---------------------------------------------------------------------------

// TestReclaimSweeper_InFlightPublishIsLeftAlone reproduces the race the
// index-first order opens: the entry exists, the publishing transaction has
// not committed yet. While the fences hold the sweeper must keep the entry,
// however long that takes -- and once the transaction does commit, the job
// is found and drained normally.
func TestReclaimSweeper_InFlightPublishIsLeftAlone(t *testing.T) {
	system := newFakeCatalogStore()
	store := newFakeCatalogStore()
	store.put("meta", []byte("def"))
	meta, err := store.GetRevision(context.Background(), "meta")
	require.NoError(t, err)
	job := ReclaimJob{ID: "acme:t:1", TenantID: "acme", Database: "shop", KeyPrefixes: []string{"old/"}}
	publishRawJob(t, system, nil, job, []ReclaimFence{{Key: "meta", ModRevision: meta.ModRevision}, {Key: "gen", ModRevision: 0}})

	clk := newFakeReclaimClock()
	rs := newTestSweeper(system, newFakeReclaimResolver().route("acme", "shop", store), clk)
	for range 3 {
		rs.SweepOnce(context.Background(), nil, discardLogger())
		clk.Advance(time.Hour)
	}
	require.Len(t, system.keysWithPrefix(reclaimIndexPrefix), 1, "an entry whose publish can still commit must survive")

	// The publishing transaction commits: generation bumped, job written.
	store.put("gen", []byte{1})
	jobOp, err := ReclaimJobOp(job)
	require.NoError(t, err)
	store.put(string(jobOp.Key), jobOp.Value)
	store.put("old/1", []byte("v"))

	sweepPastQuiescence(rs, clk)
	assert.False(t, store.has("old/1"))
	assert.Empty(t, system.keysWithPrefix(reclaimIndexPrefix))
}

// TestReclaimSweeper_SettledEntryWithNoJobIsDeleted covers the other two
// "entry, no job" shapes -- a publish that lost its compare-and-swap, and a
// finished job whose entry delete never landed: a fence has moved, so the
// publish can never commit, and the entry goes.
func TestReclaimSweeper_SettledEntryWithNoJobIsDeleted(t *testing.T) {
	system := newFakeCatalogStore()
	store := newFakeCatalogStore()
	store.put("meta", []byte("def"))
	meta, err := store.GetRevision(context.Background(), "meta")
	require.NoError(t, err)
	publishRawJob(t, system, nil, ReclaimJob{ID: "acme:t:1", TenantID: "acme", Database: "shop"},
		[]ReclaimFence{{Key: "meta", ModRevision: meta.ModRevision}})
	store.put("meta", []byte("def v2"))

	rs := newTestSweeper(system, newFakeReclaimResolver().route("acme", "shop", store), newFakeReclaimClock())
	rs.SweepOnce(context.Background(), nil, discardLogger())
	assert.Empty(t, system.keysWithPrefix(reclaimIndexPrefix))
}

// TestReclaimSweeper_FencedKeyDeletedCountsAsMoved proves an absent fence
// key reads as ModRevision 0: a table dropped since publish settles it.
func TestReclaimSweeper_FencedKeyDeletedCountsAsMoved(t *testing.T) {
	system := newFakeCatalogStore()
	store := newFakeCatalogStore()
	publishRawJob(t, system, nil, ReclaimJob{ID: "acme:t:1", TenantID: "acme", Database: "shop"},
		[]ReclaimFence{{Key: "meta", ModRevision: 9}})
	rs := newTestSweeper(system, newFakeReclaimResolver().route("acme", "shop", store), newFakeReclaimClock())
	rs.SweepOnce(context.Background(), nil, discardLogger())
	assert.Empty(t, system.keysWithPrefix(reclaimIndexPrefix))
}

// TestReclaimSweeper_UnfencedEntryWithNoJobIsKept proves an entry that
// carries no fences is never guessed about.
func TestReclaimSweeper_UnfencedEntryWithNoJobIsKept(t *testing.T) {
	system := newFakeCatalogStore()
	publishRawJob(t, system, nil, ReclaimJob{ID: "acme:t:1", TenantID: "acme", Database: "shop"}, nil)
	rs := newTestSweeper(system, newFakeReclaimResolver().route("acme", "shop", newFakeCatalogStore()), newFakeReclaimClock())
	rs.SweepOnce(context.Background(), nil, discardLogger())
	assert.Len(t, system.keysWithPrefix(reclaimIndexPrefix), 1)
}

// TestReclaimSweeper_FenceReadErrorKeepsTheEntry proves a failed fence read
// is not mistaken for a moved fence.
func TestReclaimSweeper_FenceReadErrorKeepsTheEntry(t *testing.T) {
	system := newFakeCatalogStore()
	store := newFakeCatalogStore()
	publishRawJob(t, system, nil, ReclaimJob{ID: "acme:t:1", TenantID: "acme", Database: "shop"},
		[]ReclaimFence{{Key: "meta", ModRevision: 9}})
	store.injectGetRevisionErr = errors.New("read failed")
	rs := newTestSweeper(system, newFakeReclaimResolver().route("acme", "shop", store), newFakeReclaimClock())
	rs.SweepOnce(context.Background(), nil, discardLogger())
	assert.Len(t, system.keysWithPrefix(reclaimIndexPrefix), 1)
}

// ---------------------------------------------------------------------------
// Failure handling
// ---------------------------------------------------------------------------

func TestReclaimSweeper_CorruptRecordsAreLeftForInspection(t *testing.T) {
	system := newFakeCatalogStore()
	system.put(reclaimIndexKey("bad-entry"), []byte("{not json"))
	store := newFakeCatalogStore()
	publishRawJob(t, system, nil, ReclaimJob{ID: "bad-job", TenantID: "acme", Database: "shop"}, nil)
	store.put(reclaimJobKey("bad-job"), []byte("{not json"))

	clk := newFakeReclaimClock()
	rs := newTestSweeper(system, newFakeReclaimResolver().route("acme", "shop", store), clk)
	sweepPastQuiescence(rs, clk)
	assert.True(t, system.has(reclaimIndexKey("bad-entry")))
	assert.True(t, system.has(reclaimIndexKey("bad-job")))
	assert.True(t, store.has(reclaimJobKey("bad-job")))
}

// TestReclaimSweeper_DeleteFailuresAreRetried proves a failed key delete,
// job-record delete, or index-entry delete leaves the job resumable, and the
// next sweep finishes it.
func TestReclaimSweeper_DeleteFailuresAreRetried(t *testing.T) {
	for _, stage := range []string{"key", "job", "index"} {
		t.Run(stage, func(t *testing.T) {
			store, def := truncatedTable(t, 1)
			clk := newFakeReclaimClock()
			rs := newTestSweeper(store, newFakeReclaimResolver().route("acme", "shop", store), clk)
			ctx := context.Background()
			rs.SweepOnce(ctx, nil, discardLogger())
			clk.Advance(ReclaimQuiescence)

			switch stage {
			case "key":
				// The orphaned keys go in one transaction per page.
				store.injectExecuteErr = errors.New("delete failed")
			case "job":
				// The keys' transactions go through; the job record's delete
				// is the first single-key Delete.
				failNthDelete(store, 1)
			case "index":
				failNthDelete(store, 2)
			}
			rs.SweepOnce(ctx, nil, discardLogger())
			rs.SweepOnce(ctx, nil, discardLogger())

			assert.Empty(t, store.keysWithPrefix(tableDataPrefixFor("shop", def.TableID, 0)))
			assert.Empty(t, store.keysWithPrefix(reclaimJobPrefix))
			assert.Empty(t, store.keysWithPrefix(reclaimIndexPrefix))
		})
	}
}

// failNthDelete arranges for the nth Delete from now to fail once.
func failNthDelete(store *fakeCatalogStore, n int) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.failDeleteAfter = n
}

func TestReclaimSweeper_ListFailuresAreRetried(t *testing.T) {
	store, def := truncatedTable(t, 1)
	clk := newFakeReclaimClock()
	rs := newTestSweeper(store, newFakeReclaimResolver().route("acme", "shop", store), clk)
	ctx := context.Background()

	store.injectRangeErr = errors.New("index unavailable")
	rs.SweepOnce(ctx, nil, discardLogger())
	rs.SweepOnce(ctx, nil, discardLogger())
	clk.Advance(ReclaimQuiescence)

	store.injectRangeErr = nil
	failNextRangeAfter(store, 2) // the index page succeeds, the key listing fails
	rs.SweepOnce(ctx, nil, discardLogger())
	assert.Len(t, store.keysWithPrefix(tableDataPrefixFor("shop", def.TableID, 0)), 1)

	rs.SweepOnce(ctx, nil, discardLogger())
	assert.Empty(t, store.keysWithPrefix(tableDataPrefixFor("shop", def.TableID, 0)))
}

// A system shard whose state machine keeps no MVCC keyspace refuses the
// index listing on every pass, permanently: raft.sm_type does not change
// while the process runs. The sweeper says so ONCE and stops asking, rather
// than logging an operator-unactionable WARN every interval.
func TestReclaimSweeper_UnlistableIndexWarnsOnceAndStopsListing(t *testing.T) {
	store, _ := truncatedTable(t, 1)
	clk := newFakeReclaimClock()
	rs := newTestSweeper(store, newFakeReclaimResolver().route("acme", "shop", store), clk)
	ctx := context.Background()

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	before := len(store.rangeLimits)
	for range 3 {
		// The fake's injected error is one-shot; the refusal is not.
		store.injectRangeErr = &MVCCBackendRequiredError{Err: errors.New("unsupported query type")}
		rs.SweepOnce(ctx, nil, logger)
	}

	assert.Equal(t, 1, strings.Count(logs.String(), "level=WARN"), logs.String())
	assert.Contains(t, logs.String(), "raft.sm_type")
	assert.Len(t, store.rangeLimits, before+1, "a permanent refusal must not be re-listed every pass")
}

// A transient listing failure is still reported on every pass: only the
// permanent MVCC refusal is reported once.
func TestReclaimSweeper_TransientListFailureWarnsEveryPass(t *testing.T) {
	store, _ := truncatedTable(t, 1)
	clk := newFakeReclaimClock()
	rs := newTestSweeper(store, newFakeReclaimResolver().route("acme", "shop", store), clk)
	ctx := context.Background()

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	for range 2 {
		store.injectRangeErr = errors.New("index unavailable")
		rs.SweepOnce(ctx, nil, logger)
	}

	assert.Equal(t, 2, strings.Count(logs.String(), "level=WARN"), logs.String())
}

func failNextRangeAfter(store *fakeCatalogStore, n int) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.failRangeAfter = n
}

func TestReclaimKeysBounded_RejectsAnEmptyPrefix(t *testing.T) {
	_, _, err := reclaimKeysBounded(context.Background(), newFakeCatalogStore(), []string{""}, 10, &reclaimProgress{})
	var scope *ReclaimScopeError
	require.ErrorAs(t, err, &scope)
}

func TestReclaimSweeper_NilIndexIsANoOp(t *testing.T) {
	rs := NewReclaimSweeper(nil)
	rs.SetStoreResolver(newFakeReclaimResolver())
	rs.SweepOnce(context.Background(), nil, nil)
}

// TestReclaimSweeper_OverlappingSweepReturnsImmediately proves SweepOnce
// admits one sweep at a time.
func TestReclaimSweeper_OverlappingSweepReturnsImmediately(t *testing.T) {
	store, def := truncatedTable(t, 1)
	clk := newFakeReclaimClock()
	rs := newTestSweeper(store, newFakeReclaimResolver().route("acme", "shop", store), clk)
	rs.SweepOnce(context.Background(), nil, discardLogger())
	clk.Advance(ReclaimQuiescence)

	rs.sweeping.Store(true)
	rs.SweepOnce(context.Background(), nil, discardLogger())
	assert.Len(t, store.keysWithPrefix(tableDataPrefixFor("shop", def.TableID, 0)), 1)

	rs.sweeping.Store(false)
	rs.SweepOnce(context.Background(), nil, discardLogger())
	assert.Empty(t, store.keysWithPrefix(tableDataPrefixFor("shop", def.TableID, 0)))
}

// ---------------------------------------------------------------------------
// Run / SetStoreResolver
// ---------------------------------------------------------------------------

// TestReclaimSweeper_RunSweepsOnEveryTick proves Run keeps sweeping -- on a
// node that is not the system leader too -- and returns when ctx ends.
func TestReclaimSweeper_RunSweepsOnEveryTick(t *testing.T) {
	store, def := truncatedTable(t, 1)
	resolver := newFakeReclaimResolver().route("acme", "shop", store)
	resolver.ownsFn = func(ReclaimIndexEntry, bool) bool { return true }
	// Every reading of this clock is a full ReclaimQuiescence after the
	// last, so the tick after a job is first observed may drain it.
	var ticks atomic.Int64
	rs := NewReclaimSweeper(store, WithReclaimClock(func() time.Time {
		return time.Unix(0, 0).Add(time.Duration(ticks.Add(1)) * ReclaimQuiescence)
	}))
	rs.SetStoreResolver(resolver)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		rs.Run(ctx, time.Millisecond, func() bool { return false }, nil)
		close(done)
	}()
	require.Eventually(t, func() bool {
		return !store.has(tableDataPrefixFor("shop", def.TableID, 0) + "0")
	}, 5*time.Second, time.Millisecond)
	cancel()
	<-done
}

// TestReclaimSweeper_SetStoreResolverIsRaceFreeWithRun is run under -race:
// a composer that starts Run before the server installs the resolver (the
// SDK's own order) must not race.
func TestReclaimSweeper_SetStoreResolverIsRaceFreeWithRun(t *testing.T) {
	store := newFakeCatalogStore()
	rs := NewReclaimSweeper(store)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() {
		rs.Run(ctx, time.Millisecond, nil, discardLogger())
		close(done)
	}()
	for i := 0; ctx.Err() == nil; i++ {
		if i%2 == 0 {
			rs.SetStoreResolver(newFakeReclaimResolver())
		} else {
			rs.SetStoreResolver(nil)
		}
		time.Sleep(time.Millisecond)
	}
	<-done
	rs.SetStoreResolver(nil)
	assert.Nil(t, rs.storeResolver())
}

// ---------------------------------------------------------------------------
// Small contracts
// ---------------------------------------------------------------------------

func TestReclaimErrors_DescribeAndUnwrap(t *testing.T) {
	cause := errors.New("boom")
	enc := &ReclaimJobEncodeError{ID: "job-7", Err: cause}
	assert.Contains(t, enc.Error(), `"job-7"`)
	assert.ErrorIs(t, enc, cause)

	stale := &TableDefinitionStaleError{Database: "shop", Name: "t", Reason: "index idx_a was renamed or dropped"}
	assert.Contains(t, stale.Error(), "shop/t")
	assert.Contains(t, stale.Error(), "idx_a")
	assert.Contains(t, stale.Error(), "retry")

	gone := &ReclaimStoreGoneError{TenantID: "acme", Database: "shop"}
	assert.Contains(t, gone.Error(), `"acme"`)
	assert.Contains(t, gone.Error(), `"shop"`)
}

// TestTableIndexGenerationPrefix_CoversEveryIndexAtOneGenerationOnly pins
// what a TRUNCATE job and DROP TABLE rely on: the prefix covers every index
// name at one generation of one table, and nothing of another generation or
// another table whose ID shares its digits.
func TestTableIndexGenerationPrefix_CoversEveryIndexAtOneGenerationOnly(t *testing.T) {
	prefix := TableIndexGenerationPrefix("shop", 1, 1)
	for _, incarnation := range []uint64{1, 7} {
		assert.True(t, strings.HasPrefix(TableIndexPrefix("shop", 1, 1, incarnation), prefix), incarnation)
	}
	assert.False(t, strings.HasPrefix(TableIndexPrefix("shop", 1, 10, 1), prefix), "generation 10")
	assert.False(t, strings.HasPrefix(TableIndexPrefix("shop", 10, 1, 1), prefix), "table 10")
	assert.False(t, strings.HasPrefix(TableDataPrefix("shop", 1, 1), prefix), "row keys")
}

// TestReclaimSweeper_ResolverReturningNoStoreIsRetried proves a resolver
// answering with no store and no error is treated as a transient failure,
// never as "nothing to find".
func TestReclaimSweeper_ResolverReturningNoStoreIsRetried(t *testing.T) {
	store, _ := truncatedTable(t, 1)
	clk := newFakeReclaimClock()
	rs := newTestSweeper(store, newFakeReclaimResolver().route("acme", "shop", nil), clk)
	sweepPastQuiescence(rs, clk)
	assert.Len(t, store.keysWithPrefix(reclaimIndexPrefix), 1)
	assert.Len(t, store.keysWithPrefix(reclaimJobPrefix), 1)
}

// TestReclaimSweeper_KeyBudgetIsSharedAcrossJobs proves the per-sweep key
// budget bounds the whole sweep, not each job: once one job has used it up,
// a second eligible job is left for the next sweep.
func TestReclaimSweeper_KeyBudgetIsSharedAcrossJobs(t *testing.T) {
	system := newFakeCatalogStore()
	for _, id := range []string{"a-job", "b-job"} {
		publishRawJob(t, system, system, ReclaimJob{ID: id, TenantID: "acme", Database: "shop", KeyPrefixes: []string{id + "/"}}, nil)
		for i := range reclaimMaxKeysPerSweep {
			system.put(id+"/"+strconv.Itoa(i), nil)
		}
	}
	clk := newFakeReclaimClock()
	rs := newTestSweeper(system, newFakeReclaimResolver().route("acme", "shop", system), clk)
	sweepPastQuiescence(rs, clk)

	assert.Empty(t, system.keysWithPrefix("a-job/"))
	assert.Len(t, system.keysWithPrefix("b-job/"), reclaimMaxKeysPerSweep, "the second job waits for the next sweep")
}

// TestReclaimSweeper_FencesAreReadBeforeTheJob reproduces the check-then-act
// race in orphan resolution: a publish that commits the instant after the
// sweeper finds no job. Read in the old order -- job, then fences -- the
// sweeper would see the job absent, then the fence moved by that very
// commit, and delete the only entry naming a live job. Read fences-first,
// an unmoved fence ends the check before the job is ever read.
func TestReclaimSweeper_FencesAreReadBeforeTheJob(t *testing.T) {
	system := newFakeCatalogStore()
	store := newFakeCatalogStore()
	store.put("meta", []byte("def"))
	meta, err := store.GetRevision(context.Background(), "meta")
	require.NoError(t, err)
	job := ReclaimJob{ID: "acme:t:1", TenantID: "acme", Database: "shop", KeyPrefixes: []string{"old/"}}
	publishRawJob(t, system, nil, job, []ReclaimFence{{Key: "meta", ModRevision: meta.ModRevision}})

	commit := func(f *fakeCatalogStore) {
		f.put("meta", []byte("def v2"))
		jobOp, jerr := ReclaimJobOp(job)
		require.NoError(t, jerr)
		f.put(string(jobOp.Key), jobOp.Value)
		f.put("old/1", []byte("v"))
	}
	committed := false
	store.afterGet = map[string]func(*fakeCatalogStore){
		reclaimJobKey(job.ID): func(f *fakeCatalogStore) { committed = true; commit(f) },
	}

	clk := newFakeReclaimClock()
	rs := newTestSweeper(system, newFakeReclaimResolver().route("acme", "shop", store), clk)
	rs.SweepOnce(context.Background(), nil, discardLogger())
	require.Len(t, system.keysWithPrefix(reclaimIndexPrefix), 1,
		"an entry must not be settled by a job read that raced its own publish")

	if !committed {
		store.afterGet = nil
		commit(store)
	}
	sweepPastQuiescence(rs, clk)
	assert.False(t, store.has("old/1"), "the job, once committed, is drained normally")
	assert.Empty(t, system.keysWithPrefix(reclaimIndexPrefix))
}

// TestReclaimSweeper_ResumesAfterTheLastDeletedKey proves a job too large
// for one sweep continues where it stopped: the next sweep lists from just
// after the last key it deleted, never from the start of the prefix, so it
// does not re-walk what it already deleted.
func TestReclaimSweeper_ResumesAfterTheLastDeletedKey(t *testing.T) {
	system := newFakeCatalogStore()
	publishRawJob(t, system, system, ReclaimJob{ID: "big", TenantID: "acme", Database: "shop", KeyPrefixes: []string{"rows/"}}, nil)
	var keys []string
	for i := range reclaimMaxKeysPerSweep + 10 {
		k := "rows/" + strconv.Itoa(100000+i)
		keys = append(keys, k)
		system.put(k, nil)
	}
	clk := newFakeReclaimClock()
	rs := newTestSweeper(system, newFakeReclaimResolver().route("acme", "shop", system), clk)
	sweepPastQuiescence(rs, clk)
	require.Len(t, system.keysWithPrefix("rows/"), 10)
	lastDeleted := keys[reclaimMaxKeysPerSweep-1]

	system.mu.Lock()
	system.rangeStarts = nil
	system.mu.Unlock()
	rs.SweepOnce(context.Background(), nil, discardLogger())

	assert.Empty(t, system.keysWithPrefix("rows/"))
	assert.Empty(t, system.keysWithPrefix(reclaimJobPrefix))
	system.mu.Lock()
	defer system.mu.Unlock()
	var rowStarts []string
	for _, st := range system.rangeStarts {
		if strings.HasPrefix(st, "rows/") {
			rowStarts = append(rowStarts, st)
		}
	}
	require.NotEmpty(t, rowStarts)
	assert.Equal(t, lastDeleted+"\x00", rowStarts[0], "the resumed listing starts just after the last deleted key")
}

// TestReclaimSweeper_DeletesEachPageInOneTransaction (go-qrdb#547 N7): a
// page of orphaned keys is one Raft proposal, not one per key.
func TestReclaimSweeper_DeletesEachPageInOneTransaction(t *testing.T) {
	const rows = 600 // 1200 keys: rows and index entries
	store, def := truncatedTable(t, rows)
	clk := newFakeReclaimClock()
	rs := newTestSweeper(store, newFakeReclaimResolver().route("acme", "shop", store), clk)
	before := store.executeCallsSnapshot()
	sweepPastQuiescence(rs, clk)

	assert.Empty(t, store.keysWithPrefix(tableDataPrefixFor("shop", def.TableID, 0)))
	assert.Empty(t, store.keysWithPrefix(tableIndexGenerationPrefixFor("shop", def.TableID, 0)))
	// 600 rows: a page of 500 and one of 100; the same for the index entries.
	assert.Equal(t, 4, store.executeCallsSnapshot()-before, "one transaction per page of at most reclaimKeyPageSize keys")
}
