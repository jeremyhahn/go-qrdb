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
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal/clustersealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// ---------------------------------------------------------------------------
// fakeShardLifecycle — test double for ShardLifecycle
// ---------------------------------------------------------------------------

// fakeShardLifecycle records Attach, Close, and CloseAndPurge calls and
// maintains a set of "started" shards. It is goroutine-safe.
type fakeShardLifecycle struct {
	mu        sync.Mutex
	started   map[raftShardKey]bool
	observers map[raftShardKey]bool
	attached  []raftShardKey
	closed    []raftShardKey
	purged    []raftShardKey
	attachFn  func(tenantID, db string) error
	// shardIDs records the raft shard each started shard serves, as the
	// real factory's raftShard.shardID does; ListStarted and ListOnHost
	// report it.
	shardIDs map[raftShardKey]uint64
	// shardIDOf resolves the shard an Attach starts (the real factory
	// reads it from the placement); nil leaves it 0.
	shardIDOf func(tenantID, db string) uint64
	// passes counts ListStarted calls: ReconcileOnce makes exactly one per
	// pass, so this counts reconcile passes.
	passes int
}

func newFakeShardLifecycle() *fakeShardLifecycle {
	return &fakeShardLifecycle{
		started:   make(map[raftShardKey]bool),
		observers: make(map[raftShardKey]bool),
		shardIDs:  make(map[raftShardKey]uint64),
	}
}

// seedStarted marks (tenantID, db) started on shard shardID, as if a
// previous assignment had started it here.
func (f *fakeShardLifecycle) seedStarted(tenantID, db string, shardID uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := raftShardKey{tenantID: tenantID, database: db}
	f.started[key] = true
	f.shardIDs[key] = shardID
}

func (f *fakeShardLifecycle) ListOnHost() []StartedShard {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listLocked()
}

func (f *fakeShardLifecycle) listLocked() []StartedShard {
	out := make([]StartedShard, 0, len(f.started))
	for k := range f.started {
		out = append(out, StartedShard{TenantID: k.tenantID, Database: k.database, ShardID: f.shardIDs[k]})
	}
	return out
}

func (f *fakeShardLifecycle) Attach(_ context.Context, tenantID, db string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := raftShardKey{tenantID: tenantID, database: db}
	if f.attachFn != nil {
		if err := f.attachFn(tenantID, db); err != nil {
			return err
		}
	}
	f.started[key] = true
	if f.shardIDOf != nil {
		f.shardIDs[key] = f.shardIDOf(tenantID, db)
	}
	f.attached = append(f.attached, key)
	return nil
}

func (f *fakeShardLifecycle) Close(tenantID, db string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := raftShardKey{tenantID: tenantID, database: db}
	delete(f.started, key)
	f.closed = append(f.closed, key)
	return nil
}

// CloseAndPurge mirrors RaftBackendFactory.CloseAndPurge: it stops the shard
// (same bookkeeping as Close, so existing tests asserting closeCount() after
// a demotion continue to hold) and additionally records the purge.
func (f *fakeShardLifecycle) CloseAndPurge(tenantID, db string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := raftShardKey{tenantID: tenantID, database: db}
	delete(f.started, key)
	f.closed = append(f.closed, key)
	f.purged = append(f.purged, key)
	return nil
}

func (f *fakeShardLifecycle) ListStarted() []StartedShard {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.passes++
	return f.listLocked()
}

// StartedAsObserver reports the role recorded via setStartedAsObserver;
// a shard the fake never started (or never marked) reports false, matching
// the real factory's behavior for unknown or voter-started shards.
func (f *fakeShardLifecycle) StartedAsObserver(tenantID, db string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := raftShardKey{tenantID: tenantID, database: db}
	return f.started[key] && f.observers[key]
}

// setStartedAsObserver marks the named shard as having been started with
// the observer role latched, for tests driving the promoted-replica restart.
func (f *fakeShardLifecycle) setStartedAsObserver(tenantID, db string, observer bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observers[raftShardKey{tenantID: tenantID, database: db}] = observer
}

func (f *fakeShardLifecycle) isStarted(tenantID, db string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started[raftShardKey{tenantID: tenantID, database: db}]
}

func (f *fakeShardLifecycle) attachCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.attached)
}

func (f *fakeShardLifecycle) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.closed)
}

func (f *fakeShardLifecycle) purgeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.purged)
}

func (f *fakeShardLifecycle) isPurged(tenantID, db string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := raftShardKey{tenantID: tenantID, database: db}
	for _, k := range f.purged {
		if k == key {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// fakeJoinObserver — test double for JoinObserver
// ---------------------------------------------------------------------------

// fakeJoinObserver reports a per-shard joining state controlled by the test.
// Unset shard IDs default to "still joining" (true) so a test must
// explicitly mark a shard caught-up.
type fakeJoinObserver struct {
	mu      sync.Mutex
	joining map[uint64]bool
}

func newFakeJoinObserver() *fakeJoinObserver {
	return &fakeJoinObserver{joining: make(map[uint64]bool)}
}

func (f *fakeJoinObserver) IsShardJoining(shardID uint64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.joining[shardID]; ok {
		return v
	}
	return true
}

func (f *fakeJoinObserver) setCaughtUp(shardID uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.joining[shardID] = false
}

// ---------------------------------------------------------------------------
// fakeBarrierReadiness — test double for BarrierReadiness
// ---------------------------------------------------------------------------

// fakeBarrierReadiness allows tests to control which tenant barriers are
// ready (unsealed) and which are absent/sealed.
type fakeBarrierReadiness struct {
	mu      sync.Mutex
	ready   map[string]bool // tenantID → ready
	missing []string        // tenantIDs that should return an error
}

func newFakeBarrierReadiness() *fakeBarrierReadiness {
	return &fakeBarrierReadiness{ready: make(map[string]bool)}
}

func (f *fakeBarrierReadiness) markReady(tenantID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ready[tenantID] = true
}

func (f *fakeBarrierReadiness) markMissing(tenantID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.missing = append(f.missing, tenantID)
}

// GetTenant returns a sealed/nil TenantBarrier (nil error) when the tenant is
// not in the ready map, and a real error when in the missing list, so the
// reconciler defers correctly in both cases.
func (f *fakeBarrierReadiness) GetTenant(tenantID string) (*seal.TenantBarrier, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.missing {
		if m == tenantID {
			return nil, errors.New("tenant barrier not registered: " + tenantID)
		}
	}
	if f.ready[tenantID] {
		// Return a real, initialized (unsealed) TenantBarrier backed by a
		// memory store. Initialize auto-unseals the barrier when using
		// NoneStrategy, so IsSealed() returns false after this call.
		logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
		tb, err := seal.NewTenantBarrier(tenantID, logger, seal.NewMemoryBackend(), seal.BarrierConfig{}, seal.NewNoneStrategy())
		if err != nil {
			return nil, err
		}
		if initErr := tb.Initialize(context.Background(), seal.Credentials{}); initErr != nil {
			return nil, initErr
		}
		return tb, nil
	}
	// Not in ready set — return nil TB (sealed/absent).
	return nil, errors.New("not ready: " + tenantID)
}

// ---------------------------------------------------------------------------
// watchablePlacementKV — memKV that also implements placement.Watcher
// ---------------------------------------------------------------------------

// watchablePlacementKV is a memKV with a test-controlled Watch method.
type watchablePlacementKV struct {
	*memKV
	mu       sync.Mutex
	watchers []chan placement.WatchEvent
	watching bool

	// subscribed is closed by the first Watch call. A test that sends an
	// event must wait on it first: send delivers only to REGISTERED
	// watchers, so an event sent before the reconciler has subscribed is
	// dropped, and a fixed sleep "long enough for Run to call Watch" is
	// exactly the wait a loaded machine overruns (go-qrdb#418).
	subscribed     chan struct{}
	subscribedOnce sync.Once
}

func newWatchablePlacementKV() *watchablePlacementKV {
	return &watchablePlacementKV{memKV: newMemKV(), subscribed: make(chan struct{})}
}

func (w *watchablePlacementKV) Watch(_ context.Context, _ string) (<-chan placement.WatchEvent, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	ch := make(chan placement.WatchEvent, 4)
	w.watchers = append(w.watchers, ch)
	w.watching = true
	w.subscribedOnce.Do(func() { close(w.subscribed) })
	return ch, nil
}

// send delivers a WatchEvent to all registered watchers.
func (w *watchablePlacementKV) send(ev placement.WatchEvent) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, ch := range w.watchers {
		ch <- ev
	}
}

// closeAll closes all watcher channels.
func (w *watchablePlacementKV) closeAll() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, ch := range w.watchers {
		close(ch)
	}
	w.watchers = nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// reconcileConvergeBound bounds how long a reconciler test waits for the
// reconciler to act. It is a FAILURE bound, not a pacing interval: every wait
// below returns as soon as its condition holds, so an idle machine never
// spends it. It is generous because the one thing a loaded CI host reliably
// does is starve goroutines — the 500ms these waits used to allow reddened a
// pull request that touched no file in this package (go-qrdb#418).
const reconcileConvergeBound = 30 * time.Second

// awaitSubscribed blocks until the reconciler has registered its watch on kv.
func awaitSubscribed(t *testing.T, kv *watchablePlacementKV) {
	t.Helper()
	select {
	case <-kv.subscribed:
	case <-time.After(reconcileConvergeBound):
		t.Fatalf("reconciler did not subscribe to the placement watch within %s", reconcileConvergeBound)
	}
}

// awaitCondition polls cond until it holds or reconcileConvergeBound passes,
// and reports which.
func awaitCondition(cond func() bool) bool {
	deadline := time.Now().Add(reconcileConvergeBound)
	for !cond() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
	return true
}

// reconcilePasses reports how many reconcile passes factory has observed.
func (f *fakeShardLifecycle) reconcilePasses() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.passes
}

// assertConfiguredIntervalDrove proves that a convergence which only a
// periodic pass could produce came from the CONFIGURED interval, not the
// default: it took extra passes beyond the one counted in before, and it
// arrived sooner than defaultReconcilerPollInterval, before a default-paced
// ticker would have fired even once. The bound is the default itself, so it
// fails only if the configured option were ignored.
func assertConfiguredIntervalDrove(t *testing.T, factory *fakeShardLifecycle, passesBefore int, took time.Duration) {
	t.Helper()
	if got := factory.reconcilePasses(); got <= passesBefore {
		t.Fatalf("converged with no reconcile pass after the change (passes %d, before %d): the periodic path was not exercised", got, passesBefore)
	}
	if took >= defaultReconcilerPollInterval {
		t.Fatalf("converged after %s, not below the %s default: the configured poll interval was not honoured", took, defaultReconcilerPollInterval)
	}
}

// placement1 is a test placement with a single replica on "local:1".
func placement1(tenantID, db string, shardID uint64) *placement.Placement {
	return &placement.Placement{
		TenantID:        tenantID,
		DatabaseID:      db,
		Database:        db,
		ShardID:         shardID,
		RF:              1,
		LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "local:1", RaftAddr: "local:1"},
		},
	}
}

// newTestReconciler creates a reconciler backed by a poll-path placement store.
func newTestReconciler(
	t *testing.T,
	factory ShardLifecycle,
	barriers BarrierReadiness,
	extra ...*placement.Placement,
) (*TenantShardReconciler, *placement.Store) {
	t.Helper()
	store, err := placement.NewStore(newMemKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	for _, p := range extra {
		if err := store.Put(context.Background(), p); err != nil {
			t.Fatalf("store.Put: %v", err)
		}
	}
	if fake, ok := factory.(*fakeShardLifecycle); ok && fake.shardIDOf == nil {
		fake.shardIDOf = shardIDFromStore(store, "local:1")
	}
	opts := []TenantShardReconcilerOption{
		WithReconcilerPollInterval(10 * time.Millisecond),
	}
	if barriers != nil {
		opts = append(opts, WithBarrierReadiness(barriers))
	}
	r := NewTenantShardReconciler(store, factory, "local:1", nil, opts...)
	return r, store
}

// shardIDFromStore resolves the shard an Attach starts the way the real
// factory does: from the placement of (tenantID, db) that assigns self.
func shardIDFromStore(store *placement.Store, self string) func(tenantID, db string) uint64 {
	return func(tenantID, db string) uint64 {
		ps, err := store.List(context.Background())
		if err != nil {
			return 0
		}
		for _, p := range ps {
			if p.TenantID != tenantID || p.Database != db {
				continue
			}
			if _, ok := p.ReplicaForNode(self); ok {
				return p.ShardID
			}
		}
		return 0
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestTenantShardReconciler_BootReattach proves ReconcileOnce starts every
// shard whose placement assigns a replica to the local node.
func TestTenantShardReconciler_BootReattach(t *testing.T) {
	factory := newFakeShardLifecycle()
	p1 := placement1("acme", "orders", 1001)
	p2 := placement1("globex", "invoices", 1002)

	r, _ := newTestReconciler(t, factory, nil, p1, p2)
	r.ReconcileOnce(context.Background())

	if !factory.isStarted("acme", "orders") {
		t.Error("acme/orders should be started after boot reattach")
	}
	if !factory.isStarted("globex", "invoices") {
		t.Error("globex/invoices should be started after boot reattach")
	}
}

// TestTenantShardReconciler_IgnoresUnassigned proves shards not assigned to
// the local node are not started.
func TestTenantShardReconciler_IgnoresUnassigned(t *testing.T) {
	factory := newFakeShardLifecycle()

	// Placement assigns to "other:1", not "local:1".
	p := &placement.Placement{
		TenantID: "acme", DatabaseID: "db", Database: "db", ShardID: 1001, RF: 1, LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "other:1", RaftAddr: "other:1"},
		},
	}
	r, _ := newTestReconciler(t, factory, nil, p)
	r.ReconcileOnce(context.Background())

	if factory.attachCount() != 0 {
		t.Errorf("Attach called %d times for unassigned placement; want 0", factory.attachCount())
	}
}

// TestTenantShardReconciler_SkipsLocalBinding confirms the reconciler never
// attaches a Raft shard for a local binding (go-qrdb Lane 2B, OD-4 A): the
// placement names this node as the sole replica, but Backend is not raft, so
// there is no Raft group to start. A local binding is materialized through
// database resolution (pkg/server), not this reconciler.
func TestTenantShardReconciler_SkipsLocalBinding(t *testing.T) {
	factory := newFakeShardLifecycle()

	p := &placement.Placement{
		TenantID: "acme", DatabaseID: "cache", Database: "cache", ShardID: 1001, RF: 1, LeaderReplicaID: 1,
		Backend: placement.BackendMemory,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "local:1", RaftAddr: "local:1"},
		},
	}
	r, _ := newTestReconciler(t, factory, nil, p)
	r.ReconcileOnce(context.Background())

	if factory.attachCount() != 0 {
		t.Errorf("Attach called %d times for a local binding; want 0", factory.attachCount())
	}
}

// TestTenantShardReconciler_MetaManagedShardSurvivesReconcile proves that a
// locally started shard with NO placement entry is NOT torn down by
// ReconcileOnce. Shards absent from the placement table are catalog-managed
// (opened from the database catalog at boot or single-node Phase-4b) and must be left
// running — absence is not evidence of orphan status (BUG 2 fix).
func TestTenantShardReconciler_MetaManagedShardSurvivesReconcile(t *testing.T) {
	factory := newFakeShardLifecycle()
	// Pre-start a shard that is NOT in the placement table.
	metaKey := raftShardKey{tenantID: "sys", database: "meta"}
	factory.mu.Lock()
	factory.started[metaKey] = true
	factory.mu.Unlock()

	r, _ := newTestReconciler(t, factory, nil) // empty placement table
	r.ReconcileOnce(context.Background())

	if !factory.isStarted("sys", "meta") {
		t.Error("meta-managed shard (no placement entry) must NOT be torn down by reconciler")
	}
	if factory.closeCount() != 0 {
		t.Errorf("Close called %d times; want 0 (meta-managed shard must survive)", factory.closeCount())
	}
}

// TestTenantShardReconciler_DemotionTeardown proves that a locally started shard
// whose placement entry EXISTS but no longer assigns a replica to this node is
// stopped. This is the demotion case (BUG 2 fix companion).
func TestTenantShardReconciler_DemotionTeardown(t *testing.T) {
	factory := newFakeShardLifecycle()
	// Pre-start the shard as if it was previously assigned here.
	factory.seedStarted("acme", "orders", 1001)

	// Place a placement entry that assigns the replica to "other:1" only.
	demoted := &placement.Placement{
		TenantID: "acme", DatabaseID: "orders", Database: "orders", ShardID: 1001, RF: 1, LeaderReplicaID: 2,
		Replicas: []placement.Replica{
			{ReplicaID: 2, NodeID: "other:1", RaftAddr: "other:1"},
		},
	}
	r, _ := newTestReconciler(t, factory, nil, demoted)
	r.ReconcileOnce(context.Background())

	if factory.isStarted("acme", "orders") {
		t.Error("demoted shard (placement exists, not assigned here) must be torn down")
	}
	if factory.closeCount() != 1 {
		t.Errorf("Close called %d times; want 1", factory.closeCount())
	}
}

// TestTenantShardReconciler_FencedPredecessorDoesNotPurgeSuccessor: a fenced
// predecessor entry (id A, shard 100) that survived a failed delete sits
// beside its recreated successor (id B, shard 200) under the same name. The
// predecessor's replica set excludes this node, the successor's includes
// it: the successor's running shard must never be closed or purged by the
// predecessor's demotion, whichever entry the table lists last.
func TestTenantShardReconciler_FencedPredecessorDoesNotPurgeSuccessor(t *testing.T) {
	predecessor := func() *placement.Placement {
		return &placement.Placement{
			TenantID: "acme", DatabaseID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Database: "orders",
			ShardID: 100, RF: 1, LeaderReplicaID: 2,
			Replicas: []placement.Replica{{ReplicaID: 2, NodeID: "other:1", RaftAddr: "other:1"}},
		}
	}
	successor := func() *placement.Placement {
		p := placement1("acme", "orders", 200)
		p.DatabaseID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		return p
	}
	orders := map[string][]*placement.Placement{
		"predecessor first": {predecessor(), successor()},
		"successor first":   {successor(), predecessor()},
	}
	for name, entries := range orders {
		t.Run(name, func(t *testing.T) {
			factory := newFakeShardLifecycle()
			factory.seedStarted("acme", "orders", 200)
			r, _ := newTestReconciler(t, factory, nil, entries...)
			r.ReconcileOnce(context.Background())

			if !factory.isStarted("acme", "orders") {
				t.Error("successor shard 200 must keep running beside its fenced predecessor")
			}
			if factory.closeCount() != 0 || factory.purgeCount() != 0 {
				t.Errorf("close=%d purge=%d; want 0, 0 (the predecessor owns shard 100, not 200)",
					factory.closeCount(), factory.purgeCount())
			}
		})
	}
}

// TestTenantShardReconciler_EventDelete_PredecessorIDLeavesSuccessor: the
// delete of a predecessor entry (id A, shard 100) while the recreated
// successor's shard 200 runs under the same name closes nothing.
func TestTenantShardReconciler_EventDelete_PredecessorIDLeavesSuccessor(t *testing.T) {
	factory := newFakeShardLifecycle()
	factory.seedStarted("acme", "orders", 200)
	store, _ := placement.NewStore(newMemKV())
	r := NewTenantShardReconciler(store, factory, "local:1", nil)
	r.rememberName("acme", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "orders", 100)
	r.rememberName("acme", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "orders", 200)

	r.handleEvent(context.Background(), placement.Event{
		Type:       placement.EventDelete,
		TenantID:   "acme",
		DatabaseID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})

	if !factory.isStarted("acme", "orders") {
		t.Error("successor shard 200 must keep running after its predecessor's delete")
	}
	if factory.closeCount() != 0 {
		t.Errorf("closeCount() = %d, want 0", factory.closeCount())
	}
}

// TestTenantShardReconciler_BarrierNotReadyDefers proves that when the tenant
// barrier is absent/sealed the shard start is silently deferred and retried on
// the next reconcile pass once the barrier becomes ready.
func TestTenantShardReconciler_BarrierNotReadyDefers(t *testing.T) {
	factory := newFakeShardLifecycle()
	barriers := newFakeBarrierReadiness() // "acme" barrier not ready yet
	p := placement1("acme", "orders", 1001)

	r, _ := newTestReconciler(t, factory, barriers, p)

	// First pass: barrier not ready → no attach.
	r.ReconcileOnce(context.Background())
	if factory.attachCount() != 0 {
		t.Fatalf("Attach should not be called while barrier is absent; got %d", factory.attachCount())
	}

	// Make the barrier ready.
	barriers.markReady("acme")

	// Second pass: barrier ready → shard starts.
	r.ReconcileOnce(context.Background())
	if factory.attachCount() != 1 {
		t.Errorf("Attach should be called once after barrier becomes ready; got %d", factory.attachCount())
	}
	if !factory.isStarted("acme", "orders") {
		t.Error("acme/orders should be started after barrier becomes ready")
	}
}

// deferralSignal is a slog.Handler that reports, on deferred, each time the
// reconciler logs that it deferred a shard for its barrier: the record is
// written after the barrier was found sealed, so a test acting on it knows
// the pass that saw the barrier sealed is over.
type deferralSignal struct{ deferred chan struct{} }

func (h deferralSignal) Enabled(context.Context, slog.Level) bool { return true }

func (h deferralSignal) Handle(_ context.Context, rec slog.Record) error {
	if strings.Contains(rec.Message, "deferring shard start") {
		select {
		case h.deferred <- struct{}{}:
		default:
		}
	}
	return nil
}

func (h deferralSignal) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h deferralSignal) WithGroup(string) slog.Handler      { return h }

// runDeferredReconciler places tenantID's "orders" database on this node,
// runs a reconciler over the real registry reg with a poll an hour away, and
// returns once the reconciler has deferred the shard for its sealed barrier.
func runDeferredReconciler(t *testing.T, reg *seal.BarrierRegistry, tenantID string, opts ...TenantShardReconcilerOption) *fakeShardLifecycle {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	store, err := placement.NewStore(newMemKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Put(ctx, placement1(tenantID, "orders", 1001)); err != nil {
		t.Fatalf("store.Put: %v", err)
	}
	factory := newFakeShardLifecycle()
	signal := deferralSignal{deferred: make(chan struct{}, 1)}
	opts = append([]TenantShardReconcilerOption{
		WithReconcilerPollInterval(time.Hour),
		WithBarrierReadiness(reg),
	}, opts...)
	r := NewTenantShardReconciler(store, factory, "local:1", slog.New(signal), opts...)
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	select {
	case <-signal.deferred:
	case <-time.After(5 * time.Second):
		t.Fatal("the reconciler never deferred the shard for its sealed barrier")
	}
	if n := factory.attachCount(); n != 0 {
		t.Fatalf("Attach called %d times while the barrier was sealed", n)
	}
	return factory
}

// registerSystemManagedShell registers tenantID's barrier, wrapped by the
// node barrier, uninitialized and sealed -- the shell placement registers on
// a node before the tenant's barrier is initialized there.
func registerSystemManagedShell(t *testing.T, reg *seal.BarrierRegistry, tenantID string) {
	t.Helper()
	if _, err := reg.RegisterTenant(context.Background(), tenantID, seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "tenants/" + tenantID + "/root-key"},
		seal.NewSystemBarrierStrategy(reg.SystemBarrier())); err != nil {
		t.Fatalf("RegisterTenant(%s): %v", tenantID, err)
	}
}

// TestTenantShardReconciler_StartsDeferredShardWhenBarrierInitializes is main
// red 61101: a tenant's home group is placed before the node holds its
// barrier, so the shard is deferred, and the barrier initialized next must
// start it then -- not on the next poll, an hour away here.
func TestTenantShardReconciler_StartsDeferredShardWhenBarrierInitializes(t *testing.T) {
	reg := newEnforcerRegistry(t)
	registerSystemManagedShell(t, reg, "rt2")
	factory := runDeferredReconciler(t, reg, "rt2", WithBarrierUnsealNotifier(reg))

	if err := reg.InitializeTenant(context.Background(), "rt2", seal.Credentials{}); err != nil {
		t.Fatalf("InitializeTenant: %v", err)
	}
	waitFor(t, func() bool { return factory.isStarted("rt2", "orders") })
}

// TestTenantShardReconciler_StartsDeferredShardWhenBarrierUnseals defers a
// shard for a barrier sealed after its initialization: the unseal starts it.
func TestTenantShardReconciler_StartsDeferredShardWhenBarrierUnseals(t *testing.T) {
	reg := newEnforcerRegistry(t)
	registerSystemManagedShell(t, reg, "rt2")
	if err := reg.InitializeTenant(context.Background(), "rt2", seal.Credentials{}); err != nil {
		t.Fatalf("InitializeTenant: %v", err)
	}
	if err := reg.SealTenant("rt2"); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}
	factory := runDeferredReconciler(t, reg, "rt2", WithBarrierUnsealNotifier(reg))

	if err := reg.UnsealTenant(context.Background(), "rt2", seal.Credentials{}); err != nil {
		t.Fatalf("UnsealTenant: %v", err)
	}
	waitFor(t, func() bool { return factory.isStarted("rt2", "orders") })
}

// TestTenantShardReconciler_QueuePassNeverBlocks queues passes with no Run
// draining them: the first is held, the rest are absorbed by it.
func TestTenantShardReconciler_QueuePassNeverBlocks(t *testing.T) {
	r := NewTenantShardReconciler(nil, newFakeShardLifecycle(), "local:1", nil)
	for range 3 {
		r.queuePass(context.Background(), "rt2")
	}
	if n := len(r.kick); n != 1 {
		t.Fatalf("queued passes = %d, want 1", n)
	}
}

// TestTenantShardReconciler_IdempotentAttach proves ReconcileOnce does not
// re-attach an already-started shard.
func TestTenantShardReconciler_IdempotentAttach(t *testing.T) {
	factory := newFakeShardLifecycle()
	p := placement1("acme", "orders", 1001)
	r, _ := newTestReconciler(t, factory, nil, p)

	r.ReconcileOnce(context.Background())
	r.ReconcileOnce(context.Background()) // second pass should be a no-op from Close perspective

	// Attach is called on each ReconcileOnce, but the factory's idempotency
	// (already in started set) means no harm. The reconciler itself does not
	// gate on ListStarted before calling Attach — Attach is idempotent by contract.
	if factory.closeCount() != 0 {
		t.Errorf("Close called %d times; want 0 (no orphans)", factory.closeCount())
	}
}

// TestTenantShardReconciler_WatchPath_Put proves the watch path calls
// ensureStarted when an EventPut is received for the local node.
func TestTenantShardReconciler_WatchPath_Put(t *testing.T) {
	factory := newFakeShardLifecycle()
	kv := newWatchablePlacementKV()
	store, err := placement.NewStore(kv)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	r := NewTenantShardReconciler(store, factory, "local:1", nil,
		WithReconcilerPollInterval(10*time.Millisecond))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx)
	}()

	awaitSubscribed(t, kv)

	p := placement1("acme", "orders", 1001)
	if err := store.Put(ctx, p); err != nil {
		t.Fatalf("store.Put: %v", err)
	}
	// Encode and send the event manually through the watchable KV.
	val, _ := marshalPlacement(p)
	kv.send(placement.WatchEvent{Key: "__sys:placement/acme/orders", Value: val})

	started := awaitCondition(func() bool { return factory.isStarted("acme", "orders") })
	cancel()
	<-done

	if !started {
		t.Error("watch EventPut should have triggered shard start for local replica")
	}
}

// TestTenantShardReconciler_WatchPath_Delete proves the watch path closes a
// locally started shard on EventDelete.
func TestTenantShardReconciler_WatchPath_Delete(t *testing.T) {
	factory := newFakeShardLifecycle()
	// Pre-start the shard.
	factory.seedStarted("acme", "orders", 1001)

	kv := newWatchablePlacementKV()
	p := placement1("acme", "orders", 1001)
	if err := kv.Put(context.Background(), "__sys:placement/acme/orders", mustMarshal(t, p)); err != nil {
		t.Fatalf("kv.Put: %v", err)
	}
	store, err := placement.NewStore(kv)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	r := NewTenantShardReconciler(store, factory, "local:1", nil,
		WithReconcilerPollInterval(10*time.Millisecond))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx)
	}()

	awaitSubscribed(t, kv)

	// Delete from the backing KV BEFORE sending the watch event so that the
	// safety ticker's ReconcileOnce (M1) does not see the placement and restart
	// the shard after the watch event closes it.
	if err := kv.Delete(context.Background(), "__sys:placement/acme/orders"); err != nil {
		t.Fatalf("kv.Delete: %v", err)
	}

	// Send a delete watch event to drive the event handler path.
	val := mustMarshal(t, p)
	kv.send(placement.WatchEvent{
		Key: "__sys:placement/acme/orders", Deleted: true,
		PrevValue: val,
	})

	closed := awaitCondition(func() bool { return !factory.isStarted("acme", "orders") })
	cancel()
	<-done

	if !closed {
		t.Error("watch EventDelete should have caused shard close")
	}
}

// TestTenantShardReconciler_WatchChannelClose_FallsBackToPoll proves that when
// the watch channel is closed, the reconciler switches to the poll path and
// continues reconciling.
func TestTenantShardReconciler_WatchChannelClose_FallsBackToPoll(t *testing.T) {
	factory := newFakeShardLifecycle()
	kv := newWatchablePlacementKV()
	store, err := placement.NewStore(kv)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	r := NewTenantShardReconciler(store, factory, "local:1", nil,
		WithReconcilerPollInterval(20*time.Millisecond))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx)
	}()

	awaitSubscribed(t, kv)

	// Add a placement and close the watch channel to trigger fallback to poll.
	p := placement1("acme", "orders", 1001)
	if err := store.Put(ctx, p); err != nil {
		t.Fatalf("store.Put: %v", err)
	}
	before := factory.reconcilePasses()
	start := time.Now()
	kv.closeAll()

	// Poll should pick it up within a few ticks.
	started := awaitCondition(func() bool { return factory.isStarted("acme", "orders") })
	took := time.Since(start)
	cancel()
	<-done

	if !started {
		t.Fatal("after watch channel close, poll path should have started the shard")
	}
	assertConfiguredIntervalDrove(t, factory, before, took)
}

// TestTenantShardReconciler_PollPath proves the poll path runs ReconcileOnce
// periodically and converges on a newly added placement.
func TestTenantShardReconciler_PollPath(t *testing.T) {
	factory := newFakeShardLifecycle()
	// Use a non-watchable KV so the reconciler always polls.
	store, err := placement.NewStore(newMemKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	r := NewTenantShardReconciler(store, factory, "local:1", nil,
		WithReconcilerPollInterval(20*time.Millisecond))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	// Measured from before Run starts, so a ticker Run creates at the default
	// interval could not fire inside the window however late the change
	// lands.
	runStart := time.Now()
	go func() {
		defer close(done)
		r.Run(ctx)
	}()

	// Added only after Run's initial pass has finished, so only a later,
	// periodic pass can converge on it.
	if !awaitCondition(func() bool { return factory.reconcilePasses() >= 1 }) {
		t.Fatal("Run never completed its initial reconcile pass")
	}
	before := factory.reconcilePasses()
	p := placement1("acme", "orders", 1001)
	if err := store.Put(ctx, p); err != nil {
		t.Fatalf("store.Put: %v", err)
	}

	started := awaitCondition(func() bool { return factory.isStarted("acme", "orders") })
	took := time.Since(runStart)
	cancel()
	<-done

	if !started {
		t.Fatal("poll path should have started the shard after placement was added")
	}
	assertConfiguredIntervalDrove(t, factory, before, took)
}

// TestTenantShardReconciler_CtxCancelStopsCleanly proves Run returns promptly
// when ctx is cancelled.
func TestTenantShardReconciler_CtxCancelStopsCleanly(t *testing.T) {
	factory := newFakeShardLifecycle()
	r, _ := newTestReconciler(t, factory, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2 s after context cancellation")
	}
}

// TestTenantShardReconciler_AttachErrorLogged proves an Attach error does not
// crash the reconciler — the error is logged and the next pass retries.
func TestTenantShardReconciler_AttachErrorLogged(t *testing.T) {
	factory := newFakeShardLifecycle()
	attachErr := errors.New("attach-failed")
	factory.attachFn = func(tenantID, _ string) error {
		if tenantID == "flaky" {
			return attachErr
		}
		return nil
	}

	p := placement1("flaky", "db", 1001)
	r, _ := newTestReconciler(t, factory, nil, p)

	// Should not panic or return an error — errors are logged and skipped.
	r.ReconcileOnce(context.Background())
}

// TestWithReconcilerPollInterval proves the option sets the interval; a
// zero/negative value is ignored.
func TestWithReconcilerPollInterval(t *testing.T) {
	factory := newFakeShardLifecycle()
	store, _ := placement.NewStore(newMemKV())

	r := NewTenantShardReconciler(store, factory, "local:1", nil,
		WithReconcilerPollInterval(42*time.Second))
	if r.pollInterval != 42*time.Second {
		t.Errorf("pollInterval = %v, want 42s", r.pollInterval)
	}

	r2 := NewTenantShardReconciler(store, factory, "local:1", nil,
		WithReconcilerPollInterval(0)) // no-op
	if r2.pollInterval != defaultReconcilerPollInterval {
		t.Errorf("zero interval should not override default; got %v", r2.pollInterval)
	}
}

// TestTenantShardReconciler_HandleEvent_PutDemotion proves an EventPut for a
// placement that does not assign a replica to the local node triggers a Close
// (demotion path — M1). Attach must not be called.
func TestTenantShardReconciler_HandleEvent_PutDemotion(t *testing.T) {
	factory := newFakeShardLifecycle()
	// Pre-start the shard to simulate the "was assigned, now demoted" scenario.
	factory.seedStarted("acme", "db", 1001)

	store, _ := placement.NewStore(newMemKV())
	r := NewTenantShardReconciler(store, factory, "local:1", nil)

	p := &placement.Placement{
		TenantID: "acme", DatabaseID: "db", Database: "db", ShardID: 1001, RF: 1, LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "other:1", RaftAddr: "other:1"},
		},
	}
	r.handleEvent(context.Background(), placement.Event{
		Type:       placement.EventPut,
		TenantID:   "acme",
		DatabaseID: "db",
		Database:   "db",
		Placement:  p,
	})

	if factory.attachCount() != 0 {
		t.Errorf("Attach called %d times for demotion EventPut; want 0", factory.attachCount())
	}
	if factory.closeCount() != 1 {
		t.Errorf("Close called %d times for demotion EventPut; want 1", factory.closeCount())
	}
	if factory.isStarted("acme", "db") {
		t.Error("demoted shard must not be running after demotion EventPut")
	}
}

// TestTenantShardReconciler_HandleEvent_PutNoLocalReplica_NotRunning proves
// that an EventPut for a placement that does not assign a replica to the local
// node calls Close (idempotent no-op when not started) and does not Attach.
func TestTenantShardReconciler_HandleEvent_PutNoLocalReplica_NotRunning(t *testing.T) {
	factory := newFakeShardLifecycle()
	store, _ := placement.NewStore(newMemKV())
	r := NewTenantShardReconciler(store, factory, "local:1", nil)

	p := &placement.Placement{
		TenantID: "acme", DatabaseID: "db", Database: "db", ShardID: 1001, RF: 1, LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "other:1", RaftAddr: "other:1"},
		},
	}
	r.handleEvent(context.Background(), placement.Event{
		Type:       placement.EventPut,
		TenantID:   "acme",
		DatabaseID: "db",
		Database:   "db",
		Placement:  p,
	})

	if factory.attachCount() != 0 {
		t.Errorf("Attach called %d times for non-local EventPut; want 0", factory.attachCount())
	}
	// No shard of this entry runs on the host, so nothing is closed.
	if factory.closeCount() != 0 {
		t.Errorf("Close called %d times; want 0 (no shard of the entry on this host)", factory.closeCount())
	}
}

// TestTenantShardReconciler_HandleEvent_PutLocalBinding_NeverAttached mirrors
// TestTenantShardReconciler_SkipsLocalBinding for the watch-driven path: an
// EventPut naming this node as a local binding's sole replica must not
// attach a Raft shard (go-qrdb Lane 2B, OD-4 A), and must not run the
// demotion Close either -- this node IS the binding's owner, not demoted.
func TestTenantShardReconciler_HandleEvent_PutLocalBinding_NeverAttached(t *testing.T) {
	factory := newFakeShardLifecycle()
	store, _ := placement.NewStore(newMemKV())
	r := NewTenantShardReconciler(store, factory, "local:1", nil)

	p := &placement.Placement{
		TenantID: "acme", DatabaseID: "cache", Database: "cache", ShardID: 1001, RF: 1, LeaderReplicaID: 1,
		Backend: placement.BackendMemory,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "local:1", RaftAddr: "local:1"},
		},
	}
	r.handleEvent(context.Background(), placement.Event{
		Type:       placement.EventPut,
		TenantID:   "acme",
		DatabaseID: "cache",
		Database:   "cache",
		Placement:  p,
	})

	if factory.attachCount() != 0 {
		t.Errorf("Attach called %d times for a local binding EventPut; want 0", factory.attachCount())
	}
	if factory.closeCount() != 0 {
		t.Errorf("Close called %d times for a local binding EventPut; want 0", factory.closeCount())
	}
}

// TestTenantShardReconciler_HandleEvent_NilPlacement proves an EventPut with
// nil Placement is safely ignored.
func TestTenantShardReconciler_HandleEvent_NilPlacement(t *testing.T) {
	factory := newFakeShardLifecycle()
	store, _ := placement.NewStore(newMemKV())
	r := NewTenantShardReconciler(store, factory, "local:1", nil)

	r.handleEvent(context.Background(), placement.Event{
		Type:       placement.EventPut,
		TenantID:   "acme",
		DatabaseID: "db",
		Database:   "db",
		Placement:  nil, // nil must not panic
	})

	if factory.attachCount() != 0 {
		t.Errorf("Attach called %d times for nil-placement EventPut; want 0", factory.attachCount())
	}
}

// TestNewTenantShardReconciler_NilLoggerDefault proves NewTenantShardReconciler
// uses slog.Default() when logger is nil.
func TestNewTenantShardReconciler_NilLoggerDefault(t *testing.T) {
	factory := newFakeShardLifecycle()
	store, _ := placement.NewStore(newMemKV())
	r := NewTenantShardReconciler(store, factory, "local:1", nil)
	if r.logger == nil {
		t.Error("logger must not be nil when nil is passed")
	}
}

// TestTenantShardReconciler_WatchPath_SafetyTick proves that during the watch
// path the safety ticker fires ReconcileOnce periodically. A placement is added
// to the store WITHOUT sending a watch event; the safety tick must still pick it
// up (M1 hardening).
func TestTenantShardReconciler_WatchPath_SafetyTick(t *testing.T) {
	factory := newFakeShardLifecycle()
	kv := newWatchablePlacementKV()
	store, err := placement.NewStore(kv)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	const tick = 30 * time.Millisecond
	r := NewTenantShardReconciler(store, factory, "local:1", nil,
		WithReconcilerPollInterval(tick))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	// Measured from before Run starts, so a ticker Run creates at the default
	// interval could not fire inside the window however late the change
	// lands.
	runStart := time.Now()
	go func() {
		defer close(done)
		r.Run(ctx)
	}()

	awaitSubscribed(t, kv)

	// Add a placement to the store WITHOUT sending a watch event. Run's
	// initial pass precedes its Watch, so it is already counted.
	before := factory.reconcilePasses()
	p := placement1("acme", "safetydb", 2001)
	if err := store.Put(ctx, p); err != nil {
		t.Fatalf("store.Put: %v", err)
	}

	// No event is sent, so only the safety tick can start it.
	started := awaitCondition(func() bool { return factory.isStarted("acme", "safetydb") })
	took := time.Since(runStart)
	cancel()
	<-done

	if !started {
		t.Fatal("safety tick in watch mode must fire ReconcileOnce and start the shard")
	}
	assertConfiguredIntervalDrove(t, factory, before, took)
}

// ---------------------------------------------------------------------------
// helpers used only in this test file
// ---------------------------------------------------------------------------

// marshalPlacement JSON-encodes a Placement for insertion into a watch event.
// It mirrors the encoding used by placement.Store.
func marshalPlacement(p *placement.Placement) ([]byte, error) {
	// Delegate to placement.Store via a scratch in-memory store (avoids
	// duplicating the JSON encoding logic).
	kv := newMemKV()
	s, _ := placement.NewStore(kv)
	if err := s.Put(context.Background(), p); err != nil {
		return nil, err
	}
	key := "__sys:placement/" + p.TenantID + "/" + p.Database
	return kv.Get(context.Background(), key)
}

// mustMarshal is like marshalPlacement but fails the test on error.
func mustMarshal(t *testing.T, p *placement.Placement) []byte {
	t.Helper()
	b, err := marshalPlacement(p)
	if err != nil {
		t.Fatalf("marshalPlacement(%s/%s): %v", p.TenantID, p.Database, err)
	}
	return b
}

// TestTenantShardReconciler_ListStarted_Factory verifies RaftBackendFactory.ListStarted
// returns the correct started shards and is available to satisfy ShardLifecycle.
func TestTenantShardReconciler_ListStarted_Factory(t *testing.T) {
	f, reg, src := newRaftFactory(t)
	ctx := context.Background()

	root1, _ := registerVolumeTenant(t, reg, "ls1")
	root2, _ := registerVolumeTenant(t, reg, "ls2")
	src.add("ls1", "db", 2001, root1)
	src.add("ls2", "db", 2002, root2)

	if len(f.ListStarted()) != 0 {
		t.Fatal("ListStarted should be empty before any Attach")
	}

	if err := f.Attach(ctx, "ls1", "db"); err != nil {
		t.Fatalf("Attach ls1/db: %v", err)
	}
	if err := f.Attach(ctx, "ls2", "db"); err != nil {
		t.Fatalf("Attach ls2/db: %v", err)
	}

	started := f.ListStarted()
	if len(started) != 2 {
		t.Fatalf("ListStarted = %d entries, want 2", len(started))
	}
	seen := map[string]bool{}
	for _, s := range started {
		seen[s.TenantID+"/"+s.Database] = true
	}
	if !seen["ls1/db"] || !seen["ls2/db"] {
		t.Errorf("ListStarted = %v; want ls1/db and ls2/db", started)
	}

	if err := f.Close("ls1", "db"); err != nil {
		t.Fatalf("Close ls1/db: %v", err)
	}
	if len(f.ListStarted()) != 1 {
		t.Fatalf("ListStarted after Close = %d entries, want 1", len(f.ListStarted()))
	}
	_ = strings.TrimSpace("") // satisfy import
}

// ---------------------------------------------------------------------------
// CloseAndPurge / WithPurgeOnDemotion tests
// ---------------------------------------------------------------------------

// TestTenantShardReconciler_DemotionPurgesByDefault proves that, absent an
// explicit WithPurgeOnDemotion option, a demoted shard is torn down via
// CloseAndPurge (default true) rather than plain Close.
func TestTenantShardReconciler_DemotionPurgesByDefault(t *testing.T) {
	factory := newFakeShardLifecycle()
	factory.seedStarted("acme", "orders", 1001)

	demoted := &placement.Placement{
		TenantID: "acme", DatabaseID: "orders", Database: "orders", ShardID: 1001, RF: 1, LeaderReplicaID: 2,
		Replicas: []placement.Replica{
			{ReplicaID: 2, NodeID: "other:1", RaftAddr: "other:1"},
		},
	}
	r, _ := newTestReconciler(t, factory, nil, demoted)
	r.ReconcileOnce(context.Background())

	if factory.purgeCount() != 1 {
		t.Errorf("purgeCount() = %d, want 1 (CloseAndPurge is the default demotion path)", factory.purgeCount())
	}
	if !factory.isPurged("acme", "orders") {
		t.Error("acme/orders should have been purged on demotion")
	}
}

// TestTenantShardReconciler_WithPurgeOnDemotionFalse proves that
// WithPurgeOnDemotion(false) opts a reconciler back into the plain Close
// path (no data-dir purge) for demotion teardown.
func TestTenantShardReconciler_WithPurgeOnDemotionFalse(t *testing.T) {
	factory := newFakeShardLifecycle()
	factory.seedStarted("acme", "orders", 1001)

	demoted := &placement.Placement{
		TenantID: "acme", DatabaseID: "orders", Database: "orders", ShardID: 1001, RF: 1, LeaderReplicaID: 2,
		Replicas: []placement.Replica{
			{ReplicaID: 2, NodeID: "other:1", RaftAddr: "other:1"},
		},
	}
	store, err := placement.NewStore(newMemKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Put(context.Background(), demoted); err != nil {
		t.Fatalf("store.Put: %v", err)
	}
	r := NewTenantShardReconciler(store, factory, "local:1", nil,
		WithReconcilerPollInterval(10*time.Millisecond),
		WithPurgeOnDemotion(false))
	r.ReconcileOnce(context.Background())

	if factory.closeCount() != 1 {
		t.Errorf("closeCount() = %d, want 1", factory.closeCount())
	}
	if factory.purgeCount() != 0 {
		t.Errorf("purgeCount() = %d, want 0 (WithPurgeOnDemotion(false) must use plain Close)", factory.purgeCount())
	}
}

// TestTenantShardReconciler_EventDelete_PurgesByDefault proves the
// EventDelete (full DropForTenant) path also purges by default.
func TestTenantShardReconciler_EventDelete_PurgesByDefault(t *testing.T) {
	factory := newFakeShardLifecycle()
	factory.seedStarted("acme", "orders", 1001)

	store, _ := placement.NewStore(newMemKV())
	r := NewTenantShardReconciler(store, factory, "local:1", nil)
	// The entry was seen while it existed (a pass or a put event).
	r.rememberName("acme", "orders", "orders", 1001)

	r.handleEvent(context.Background(), placement.Event{
		Type:       placement.EventDelete,
		TenantID:   "acme",
		DatabaseID: "orders",
		Database:   "orders",
	})

	if factory.purgeCount() != 1 {
		t.Errorf("purgeCount() = %d, want 1 (EventDelete purges by default)", factory.purgeCount())
	}
}

// TestTenantShardReconciler_EventDelete_ByIDClosesTheNamedShard: the table
// is keyed by the opaque database id and the watch carries no prior value,
// so a delete event names only the id. The reconciler closes the shard of
// the database the entry named when it last saw it.
func TestTenantShardReconciler_EventDelete_ByIDClosesTheNamedShard(t *testing.T) {
	factory := newFakeShardLifecycle()
	factory.seedStarted("acme", "orders", 1001)

	store, _ := placement.NewStore(newMemKV())
	p := placement1("acme", "orders", 1001)
	p.DatabaseID = "0f4d9c2b7a6e5d4c3b2a19f8e7d6c5b4"
	if err := store.Put(context.Background(), p); err != nil {
		t.Fatalf("Put: %v", err)
	}
	r := NewTenantShardReconciler(store, factory, "local:1", nil)
	r.ReconcileOnce(context.Background())

	r.handleEvent(context.Background(), placement.Event{
		Type:       placement.EventDelete,
		TenantID:   "acme",
		DatabaseID: "0f4d9c2b7a6e5d4c3b2a19f8e7d6c5b4",
	})

	if factory.purgeCount() != 1 || factory.isStarted("acme", "orders") {
		t.Errorf("purgeCount() = %d, started = %v: want the shard of orders closed", factory.purgeCount(), factory.isStarted("acme", "orders"))
	}
}

// ---------------------------------------------------------------------------
// JoinObserver / Observer→ObserverReady→Active promotion tests
// ---------------------------------------------------------------------------

// joiningPlacement1 is a test placement with a single OBSERVER replica on
// "local:1".
func joiningPlacement1(tenantID, db string, shardID uint64) *placement.Placement {
	return &placement.Placement{
		TenantID:        tenantID,
		DatabaseID:      db,
		Database:        db,
		ShardID:         shardID,
		RF:              1,
		LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "local:1", RaftAddr: "local:1", State: placement.ReplicaStateObserver},
		},
	}
}

// fakePromoter is a test double for Promoter. leaderID/leaderOK configure
// LeaderReplicaID's return value; promoteErr, when non-nil, is returned by
// every PromoteObserver call. promoted records every successfully promoted
// (shardID, replicaID) pair.
type fakePromoter struct {
	mu          sync.Mutex
	leaderID    uint64
	leaderOK    bool
	leaderErr   error
	promoteErr  error
	promoted    []promotedCall
	promoteCall int
}

type promotedCall struct {
	shardID   uint64
	replicaID uint64
	raftAddr  string
}

func (f *fakePromoter) LeaderReplicaID(_ context.Context, _ uint64) (uint64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.leaderID, f.leaderOK, f.leaderErr
}

func (f *fakePromoter) PromoteObserver(_ context.Context, shardID, replicaID uint64, raftAddr string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.promoteCall++
	if f.promoteErr != nil {
		return f.promoteErr
	}
	f.promoted = append(f.promoted, promotedCall{shardID: shardID, replicaID: replicaID, raftAddr: raftAddr})
	return nil
}

func (f *fakePromoter) promotedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.promoted)
}

// TestTenantShardReconciler_ObserverPromotedToObserverReady proves that once
// the JoinObserver reports the join bootstrap window closed, ReconcileOnce
// promotes the local replica's placement State from Observer to
// ObserverReady with exactly one placementStore.Put -- and, with no Promoter
// wired, promotion stops there (never reaches Active): only the shard's
// leader may perform that second transition (see
// TestTenantShardReconciler_ObserverReadyPromotedToActiveByLeader).
func TestTenantShardReconciler_ObserverPromotedToObserverReady(t *testing.T) {
	factory := newFakeShardLifecycle()
	p := joiningPlacement1("acme", "orders", 1001)
	observer := newFakeJoinObserver() // defaults to "still joining"

	store, err := placement.NewStore(newMemKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Put(context.Background(), p); err != nil {
		t.Fatalf("store.Put: %v", err)
	}
	r := NewTenantShardReconciler(store, factory, "local:1", nil,
		WithReconcilerPollInterval(10*time.Millisecond),
		WithJoinObserver(observer))

	// First pass: still joining, no promotion.
	r.ReconcileOnce(context.Background())
	got, err := store.Get(context.Background(), "acme", "orders")
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	local, ok := got.ReplicaForNode("local:1")
	if !ok || local.State != placement.ReplicaStateObserver {
		t.Fatalf("after first pass State = %v, want ReplicaStateObserver (still bootstrapping)", local.State)
	}

	// Join bootstrap window closes.
	observer.setCaughtUp(1001)

	// Second pass: promotion to ObserverReady happens.
	r.ReconcileOnce(context.Background())
	got, err = store.Get(context.Background(), "acme", "orders")
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	local, ok = got.ReplicaForNode("local:1")
	if !ok || local.State != placement.ReplicaStateObserverReady {
		t.Fatalf("after second pass State = %v, want ReplicaStateObserverReady", local.State)
	}
	if local.ReplicaID != 1 || local.NodeID != "local:1" {
		t.Errorf("promotion mutated replica identity: got %+v", local)
	}

	// Third pass: already ObserverReady, no-op (must not error or re-promote
	// a non-Observer replica, and -- with no Promoter wired -- must never
	// reach Active).
	r.ReconcileOnce(context.Background())
	got, err = store.Get(context.Background(), "acme", "orders")
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	local, _ = got.ReplicaForNode("local:1")
	if local.State != placement.ReplicaStateObserverReady {
		t.Fatalf("third pass State = %v, want ReplicaStateObserverReady (stable, no Promoter wired)", local.State)
	}
}

// TestTenantShardReconciler_AnObserverThatNeverStartedIsNotReady: a
// replica whose shard could not start here -- its tenant's key has not
// arrived, say -- is not joining because it is not running at all, and must
// not be reported caught up. Marking it ObserverReady would let the leader
// promote a replica that holds no data and is not even an observer member,
// adding it straight to the voting set. It is marked once its shard runs and
// has finished joining, by the watch path as by the poll.
func TestTenantShardReconciler_AnObserverThatNeverStartedIsNotReady(t *testing.T) {
	for name, drive := range map[string]func(r *TenantShardReconciler, p *placement.Placement){
		"poll": func(r *TenantShardReconciler, _ *placement.Placement) { r.ReconcileOnce(context.Background()) },
		"watch": func(r *TenantShardReconciler, p *placement.Placement) {
			r.handleEvent(context.Background(), placement.Event{Type: placement.EventPut, TenantID: p.TenantID, DatabaseID: p.Database, Database: p.Database, Placement: p})
		},
	} {
		t.Run(name, func(t *testing.T) {
			factory := newFakeShardLifecycle()
			startable := false
			factory.attachFn = func(string, string) error {
				if !startable {
					return errors.New("tenant key not delivered yet")
				}
				return nil
			}
			p := joiningPlacement1("acme", "orders", 1001)
			observer := newFakeJoinObserver()
			observer.setCaughtUp(1001) // not joining -- because it never started
			store, err := placement.NewStore(newMemKV())
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Put(context.Background(), p); err != nil {
				t.Fatal(err)
			}
			r := NewTenantShardReconciler(store, factory, "local:1", nil, WithJoinObserver(observer))

			state := func() placement.ReplicaState {
				t.Helper()
				got, err := store.Get(context.Background(), "acme", "orders")
				if err != nil {
					t.Fatal(err)
				}
				local, _ := got.ReplicaForNode("local:1")
				return local.State
			}
			drive(r, p)
			if s := state(); s != placement.ReplicaStateObserver {
				t.Fatalf("a replica whose shard never started is %v; want it left an observer", s)
			}

			startable = true
			current, err := store.Get(context.Background(), "acme", "orders")
			if err != nil {
				t.Fatal(err)
			}
			drive(r, current)
			if s := state(); s != placement.ReplicaStateObserverReady {
				t.Fatalf("a running, caught-up observer is %v; want ObserverReady", s)
			}
		})
	}
}

// TestTenantShardReconciler_ObserverReadyPromotedToActiveByLeader proves
// that once a replica reaches ReplicaStateObserverReady, the reconciler
// promotes it to ReplicaStateActive (a full voting member) exactly when this
// node is confirmed the shard's current Raft leader, via Promoter.
func TestTenantShardReconciler_ObserverReadyPromotedToActiveByLeader(t *testing.T) {
	factory := newFakeShardLifecycle()
	p := &placement.Placement{
		TenantID: "acme", DatabaseID: "orders", Database: "orders", ShardID: 1001, RF: 2, LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "local:1", RaftAddr: "local:1"},
			{ReplicaID: 2, NodeID: "remote:1", RaftAddr: "remote:1", State: placement.ReplicaStateObserverReady},
		},
	}
	store, err := placement.NewStore(newMemKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Put(context.Background(), p); err != nil {
		t.Fatalf("store.Put: %v", err)
	}

	// Local node ("local:1", replica 1) IS the confirmed leader.
	promoter := &fakePromoter{leaderID: 1, leaderOK: true}
	r := NewTenantShardReconciler(store, factory, "local:1", nil,
		WithReconcilerPollInterval(10*time.Millisecond),
		WithPromoter(promoter))

	r.ReconcileOnce(context.Background())

	if promoter.promotedCount() != 1 {
		t.Fatalf("PromoteObserver called %d times, want 1", promoter.promotedCount())
	}
	call := promoter.promoted[0]
	if call.shardID != 1001 || call.replicaID != 2 || call.raftAddr != "remote:1" {
		t.Errorf("PromoteObserver call = %+v, want shard=1001 replica=2 addr=remote:1", call)
	}

	got, err := store.Get(context.Background(), "acme", "orders")
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	remote, ok := got.ReplicaForNode("remote:1")
	if !ok || remote.State != placement.ReplicaStateActive {
		t.Fatalf("remote replica State = %v, want ReplicaStateActive", remote.State)
	}

	// Second pass: already Active, no-op (must not re-promote).
	r.ReconcileOnce(context.Background())
	if promoter.promotedCount() != 1 {
		t.Errorf("PromoteObserver called %d times after second pass, want still 1 (no re-promotion)", promoter.promotedCount())
	}
}

// ---------------------------------------------------------------------------
// go-qrdb#482 round-3 finding (3): every placement mutation the reconciler
// makes -- not only the drop fence -- must go through a compare-and-swap
// against the exact snapshot it read, and must never promote a replica of a
// database that is mid-Drop (which would also overwrite the replicated
// Dropping fence with a stale, pre-Drop copy of the entry).
// ---------------------------------------------------------------------------

// TestTenantShardReconciler_DroppingPlacement_ObserverNeverPromotedToObserverReady
// proves maybePromoteToObserverReady refuses outright when p.Dropping is
// true, even though every other precondition (join window closed) is met.
func TestTenantShardReconciler_DroppingPlacement_ObserverNeverPromotedToObserverReady(t *testing.T) {
	factory := newFakeShardLifecycle()
	p := joiningPlacement1("acme", "orders", 1001)
	p.Dropping = true
	observer := newFakeJoinObserver()
	observer.setCaughtUp(1001)

	store, err := placement.NewStore(newMemKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Put(context.Background(), p); err != nil {
		t.Fatalf("store.Put: %v", err)
	}
	r := NewTenantShardReconciler(store, factory, "local:1", nil,
		WithReconcilerPollInterval(10*time.Millisecond),
		WithJoinObserver(observer))

	r.ReconcileOnce(context.Background())

	got, err := store.Get(context.Background(), "acme", "orders")
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if !got.Dropping {
		t.Error("Dropping = false, want true (unchanged)")
	}
	local, ok := got.ReplicaForNode("local:1")
	if !ok || local.State != placement.ReplicaStateObserver {
		t.Fatalf("State = %v, want ReplicaStateObserver (a dropping database is never promoted)", local.State)
	}
}

// TestTenantShardReconciler_DroppingPlacement_ReadyObserverNeverPromotedToActive
// is the same guarantee for maybePromoteReadyObservers: a confirmed leader
// must still refuse to promote an ObserverReady replica of a database that
// is mid-Drop.
func TestTenantShardReconciler_DroppingPlacement_ReadyObserverNeverPromotedToActive(t *testing.T) {
	factory := newFakeShardLifecycle()
	p := &placement.Placement{
		TenantID: "acme", DatabaseID: "orders", Database: "orders", ShardID: 1001, RF: 2, LeaderReplicaID: 1,
		Dropping: true,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "local:1", RaftAddr: "local:1"},
			{ReplicaID: 2, NodeID: "remote:1", RaftAddr: "remote:1", State: placement.ReplicaStateObserverReady},
		},
	}
	store, err := placement.NewStore(newMemKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Put(context.Background(), p); err != nil {
		t.Fatalf("store.Put: %v", err)
	}

	promoter := &fakePromoter{leaderID: 1, leaderOK: true}
	r := NewTenantShardReconciler(store, factory, "local:1", nil,
		WithReconcilerPollInterval(10*time.Millisecond),
		WithPromoter(promoter))

	r.ReconcileOnce(context.Background())

	if promoter.promotedCount() != 0 {
		t.Fatalf("PromoteObserver called %d times, want 0 (a dropping database is never promoted)", promoter.promotedCount())
	}
	got, err := store.Get(context.Background(), "acme", "orders")
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	remote, ok := got.ReplicaForNode("remote:1")
	if !ok || remote.State != placement.ReplicaStateObserverReady {
		t.Fatalf("remote replica State = %v, want ReplicaStateObserverReady (unchanged)", remote.State)
	}
}

// TestTenantShardReconciler_PromotionCAS_RejectsStaleSnapshot proves
// maybePromoteToObserverReady persists via compare-and-swap against the
// exact snapshot it was handed: a mutation that commits directly against the
// store after that snapshot was read -- standing in for a concurrent Drop
// fence, scale, or another promotion -- must survive untouched, and the
// promotion this call computed from the now-stale snapshot must never be
// written over it.
func TestTenantShardReconciler_PromotionCAS_RejectsStaleSnapshot(t *testing.T) {
	factory := newFakeShardLifecycle()
	p := joiningPlacement1("acme", "orders", 1001)
	observer := newFakeJoinObserver()
	observer.setCaughtUp(1001)

	store, err := placement.NewStore(newMemKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Put(context.Background(), p); err != nil {
		t.Fatalf("store.Put: %v", err)
	}
	r := NewTenantShardReconciler(store, factory, "local:1", nil,
		WithReconcilerPollInterval(10*time.Millisecond),
		WithJoinObserver(observer))

	// A concurrent mutation -- e.g. a Drop's fence -- commits directly
	// against the store, after p was already read but before this call's
	// own compare-and-swap.
	racing := *p
	racing.Dropping = true
	if err := store.Put(context.Background(), &racing); err != nil {
		t.Fatalf("racing Put: %v", err)
	}

	r.maybePromoteToObserverReady(context.Background(), p)

	got, err := store.Get(context.Background(), "acme", "orders")
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if !got.Dropping {
		t.Error("Dropping = false, want true (the racing writer's fence must survive)")
	}
	local, ok := got.ReplicaForNode("local:1")
	if !ok || local.State != placement.ReplicaStateObserver {
		t.Fatalf("State = %v, want ReplicaStateObserver (the stale promotion must have been rejected, not applied over the fence)", local.State)
	}
}

// TestTenantShardReconciler_ObserverReadyNotPromoted_WhenNotLeader proves
// that a node hosting (or merely seeing) an ObserverReady replica does NOT
// attempt promotion when it is not confirmed as the shard's current leader
// -- go-quicraft hard-rejects the underlying config change on a non-leader,
// so attempting it would be pointless and is skipped instead.
func TestTenantShardReconciler_ObserverReadyNotPromoted_WhenNotLeader(t *testing.T) {
	factory := newFakeShardLifecycle()
	p := &placement.Placement{
		TenantID: "acme", DatabaseID: "orders", Database: "orders", ShardID: 1001, RF: 2, LeaderReplicaID: 9,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "local:1", RaftAddr: "local:1", State: placement.ReplicaStateObserverReady},
			{ReplicaID: 9, NodeID: "remote:1", RaftAddr: "remote:1"},
		},
	}
	store, err := placement.NewStore(newMemKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Put(context.Background(), p); err != nil {
		t.Fatalf("store.Put: %v", err)
	}

	// The confirmed leader is replica 9 ("remote:1"), not the local node.
	promoter := &fakePromoter{leaderID: 9, leaderOK: true}
	r := NewTenantShardReconciler(store, factory, "local:1", nil,
		WithReconcilerPollInterval(10*time.Millisecond),
		WithPromoter(promoter))

	r.ReconcileOnce(context.Background())

	if promoter.promotedCount() != 0 {
		t.Errorf("PromoteObserver called %d times, want 0 (local node is not the leader)", promoter.promotedCount())
	}
	got, err := store.Get(context.Background(), "acme", "orders")
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	local, ok := got.ReplicaForNode("local:1")
	if !ok || local.State != placement.ReplicaStateObserverReady {
		t.Fatalf("local replica State = %v, want ReplicaStateObserverReady (unchanged)", local.State)
	}
}

// TestTenantShardReconciler_NoPromoter_ObserverReadyNeverPromoted proves
// that with no Promoter wired (the default), an ObserverReady replica is
// never promoted to Active, even when the local node is the leader.
func TestTenantShardReconciler_NoPromoter_ObserverReadyNeverPromoted(t *testing.T) {
	factory := newFakeShardLifecycle()
	p := &placement.Placement{
		TenantID: "acme", DatabaseID: "orders", Database: "orders", ShardID: 1001, RF: 1, LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "local:1", RaftAddr: "local:1", State: placement.ReplicaStateObserverReady},
		},
	}
	r, store := newTestReconciler(t, factory, nil, p)

	r.ReconcileOnce(context.Background())

	got, err := store.Get(context.Background(), "acme", "orders")
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	local, ok := got.ReplicaForNode("local:1")
	if !ok || local.State != placement.ReplicaStateObserverReady {
		t.Fatalf("State = %v, want ReplicaStateObserverReady (no Promoter wired, must not promote)", local.State)
	}
}

// TestTenantShardReconciler_ObserverReadyPromotion_FailurePreservesState
// proves that a failing PromoteObserver call leaves the replica
// ObserverReady (not silently advanced to Active) so the next tick retries.
func TestTenantShardReconciler_ObserverReadyPromotion_FailurePreservesState(t *testing.T) {
	factory := newFakeShardLifecycle()
	p := &placement.Placement{
		TenantID: "acme", DatabaseID: "orders", Database: "orders", ShardID: 1001, RF: 1, LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "local:1", RaftAddr: "local:1", State: placement.ReplicaStateObserverReady},
		},
	}
	store, err := placement.NewStore(newMemKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Put(context.Background(), p); err != nil {
		t.Fatalf("store.Put: %v", err)
	}

	promoter := &fakePromoter{leaderID: 1, leaderOK: true, promoteErr: errors.New("config change rejected")}
	r := NewTenantShardReconciler(store, factory, "local:1", nil,
		WithReconcilerPollInterval(10*time.Millisecond),
		WithPromoter(promoter))

	r.ReconcileOnce(context.Background())

	if promoter.promoteCall != 1 {
		t.Fatalf("PromoteObserver attempted %d times, want 1", promoter.promoteCall)
	}
	got, err := store.Get(context.Background(), "acme", "orders")
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	local, ok := got.ReplicaForNode("local:1")
	if !ok || local.State != placement.ReplicaStateObserverReady {
		t.Fatalf("State = %v, want ReplicaStateObserverReady (failed promotion must not advance state)", local.State)
	}
}

// TestTenantShardReconciler_JoiningStaysJoiningWhileBootstrapping proves
// that while JoinObserver reports the shard still joining, ReconcileOnce
// never promotes the placement entry.
func TestTenantShardReconciler_JoiningStaysJoiningWhileBootstrapping(t *testing.T) {
	factory := newFakeShardLifecycle()
	p := joiningPlacement1("acme", "orders", 1001)
	observer := newFakeJoinObserver() // always "still joining"

	store, err := placement.NewStore(newMemKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Put(context.Background(), p); err != nil {
		t.Fatalf("store.Put: %v", err)
	}
	r := NewTenantShardReconciler(store, factory, "local:1", nil,
		WithJoinObserver(observer))

	for i := 0; i < 3; i++ {
		r.ReconcileOnce(context.Background())
	}

	got, err := store.Get(context.Background(), "acme", "orders")
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	local, ok := got.ReplicaForNode("local:1")
	if !ok || local.State != placement.ReplicaStateObserver {
		t.Fatalf("State = %v, want ReplicaStateObserver (bootstrap window never closed)", local.State)
	}
}

// TestTenantShardReconciler_NoJoinObserver_NoPromotion proves that with no
// JoinObserver wired (the default), a Joining replica's state is never
// touched.
func TestTenantShardReconciler_NoJoinObserver_NoPromotion(t *testing.T) {
	factory := newFakeShardLifecycle()
	p := joiningPlacement1("acme", "orders", 1001)
	r, store := newTestReconciler(t, factory, nil, p)

	r.ReconcileOnce(context.Background())

	got, err := store.Get(context.Background(), "acme", "orders")
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	local, ok := got.ReplicaForNode("local:1")
	if !ok || local.State != placement.ReplicaStateObserver {
		t.Fatalf("State = %v, want ReplicaStateObserver (no observer wired, must not promote)", local.State)
	}
}

// TestTenantShardReconciler_SealedRecordDefers proves a tenant's shard is not
// started while the tenant's cluster seal record is sealed -- a system-managed
// tenant auto-unseals at boot, before this node has read the records -- and
// starts once the record is cleared.
func TestTenantShardReconciler_SealedRecordDefers(t *testing.T) {
	factory := newFakeShardLifecycle()
	p := placement1("acme", "orders", 1001)
	sealer := clustersealtest.New(true)
	sealer.Put(clusterseal.Record{Barrier: clusterseal.TenantBarrier("acme"), Epoch: 1, Sealed: true})

	store, err := placement.NewStore(newMemKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Put(context.Background(), p); err != nil {
		t.Fatalf("store.Put: %v", err)
	}
	r := NewTenantShardReconciler(store, factory, "local:1", nil,
		WithReconcilerPollInterval(10*time.Millisecond), WithSealRecords(sealer))

	r.ReconcileOnce(context.Background())
	if factory.attachCount() != 0 {
		t.Fatalf("Attach called %d times while the tenant's record is sealed", factory.attachCount())
	}
	sealer.Put(clusterseal.Record{Barrier: clusterseal.TenantBarrier("acme"), Epoch: 2})
	r.ReconcileOnce(context.Background())
	if factory.attachCount() != 1 {
		t.Fatalf("Attach called %d times after the record cleared, want 1", factory.attachCount())
	}
}

// TestTenantShardReconciler_SealedDatabaseRecordDefers: a database's own
// cluster seal record holds that database's dedicated shard -- the tenant's
// record is clear, so only the database record can hold it -- while another
// database of the same tenant starts.
func TestTenantShardReconciler_SealedDatabaseRecordDefers(t *testing.T) {
	factory := newFakeShardLifecycle()
	sealer := clustersealtest.New(true)
	sealer.Put(clusterseal.Record{Barrier: clusterseal.DatabaseBarrier("acme", "orders"), Epoch: 1, Sealed: true})

	store, err := placement.NewStore(newMemKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Put(context.Background(), placement1("acme", "orders", 1001)); err != nil {
		t.Fatalf("store.Put: %v", err)
	}
	r := NewTenantShardReconciler(store, factory, "local:1", nil,
		WithReconcilerPollInterval(10*time.Millisecond), WithSealRecords(sealer))

	r.ReconcileOnce(context.Background())
	if factory.attachCount() != 0 {
		t.Fatalf("Attach called %d times while the database's record is sealed", factory.attachCount())
	}
	sealer.Put(clusterseal.Record{Barrier: clusterseal.DatabaseBarrier("acme", "orders"), Epoch: 2})
	r.ReconcileOnce(context.Background())
	if factory.attachCount() != 1 {
		t.Fatalf("Attach called %d times after the database's record cleared, want 1", factory.attachCount())
	}
}
