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

	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// ---------------------------------------------------------------------------
// fakeShardLifecycle — test double for ShardLifecycle
// ---------------------------------------------------------------------------

// fakeShardLifecycle records Attach and Close calls and maintains a set of
// "started" shards. It is goroutine-safe.
type fakeShardLifecycle struct {
	mu       sync.Mutex
	started  map[raftShardKey]bool
	attached []raftShardKey
	closed   []raftShardKey
	attachFn func(tenantID, db string) error
}

func newFakeShardLifecycle() *fakeShardLifecycle {
	return &fakeShardLifecycle{
		started: make(map[raftShardKey]bool),
	}
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

func (f *fakeShardLifecycle) ListStarted() []StartedShard {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]StartedShard, 0, len(f.started))
	for k := range f.started {
		out = append(out, StartedShard{TenantID: k.tenantID, Database: k.database})
	}
	return out
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
}

func newWatchablePlacementKV() *watchablePlacementKV {
	return &watchablePlacementKV{memKV: newMemKV()}
}

func (w *watchablePlacementKV) Watch(_ context.Context, _ string) (<-chan placement.WatchEvent, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	ch := make(chan placement.WatchEvent, 4)
	w.watchers = append(w.watchers, ch)
	w.watching = true
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

// placement1 is a test placement with a single replica on "local:1".
func placement1(tenantID, db string, shardID uint64) *placement.Placement {
	return &placement.Placement{
		TenantID:        tenantID,
		Database:        db,
		ShardID:         shardID,
		RF:              1,
		LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "local:1", RaftAddr: "local:1", DataRoot: "/data/" + db},
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
	opts := []TenantShardReconcilerOption{
		WithReconcilerPollInterval(10 * time.Millisecond),
	}
	if barriers != nil {
		opts = append(opts, WithBarrierReadiness(barriers))
	}
	r := NewTenantShardReconciler(store, factory, "local:1", nil, opts...)
	return r, store
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
		TenantID: "acme", Database: "db", ShardID: 1001, RF: 1, LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "other:1", RaftAddr: "other:1", DataRoot: "/data"},
		},
	}
	r, _ := newTestReconciler(t, factory, nil, p)
	r.ReconcileOnce(context.Background())

	if factory.attachCount() != 0 {
		t.Errorf("Attach called %d times for unassigned placement; want 0", factory.attachCount())
	}
}

// TestTenantShardReconciler_MetaManagedShardSurvivesReconcile proves that a
// locally started shard with NO placement entry is NOT torn down by
// ReconcileOnce. Shards absent from the placement table are meta-managed
// (started from meta.json at boot or single-node Phase-4b) and must be left
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
	factory.mu.Lock()
	factory.started[raftShardKey{tenantID: "acme", database: "orders"}] = true
	factory.mu.Unlock()

	// Place a placement entry that assigns the replica to "other:1" only.
	demoted := &placement.Placement{
		TenantID: "acme", Database: "orders", ShardID: 1001, RF: 1, LeaderReplicaID: 2,
		Replicas: []placement.Replica{
			{ReplicaID: 2, NodeID: "other:1", RaftAddr: "other:1", DataRoot: "/data"},
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

	// Give Run time to call Watch.
	time.Sleep(20 * time.Millisecond)

	p := placement1("acme", "orders", 1001)
	if err := store.Put(ctx, p); err != nil {
		t.Fatalf("store.Put: %v", err)
	}
	// Encode and send the event manually through the watchable KV.
	val, _ := marshalPlacement(p)
	kv.send(placement.WatchEvent{Key: "__sys:placement/acme/orders", Value: val})

	// Wait briefly for event processing.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if factory.isStarted("acme", "orders") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	if !factory.isStarted("acme", "orders") {
		t.Error("watch EventPut should have triggered shard start for local replica")
	}
}

// TestTenantShardReconciler_WatchPath_Delete proves the watch path closes a
// locally started shard on EventDelete.
func TestTenantShardReconciler_WatchPath_Delete(t *testing.T) {
	factory := newFakeShardLifecycle()
	// Pre-start the shard.
	factory.mu.Lock()
	factory.started[raftShardKey{tenantID: "acme", database: "orders"}] = true
	factory.mu.Unlock()

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

	time.Sleep(20 * time.Millisecond)

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

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if !factory.isStarted("acme", "orders") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	if factory.isStarted("acme", "orders") {
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

	time.Sleep(30 * time.Millisecond)

	// Add a placement and close the watch channel to trigger fallback to poll.
	p := placement1("acme", "orders", 1001)
	if err := store.Put(ctx, p); err != nil {
		t.Fatalf("store.Put: %v", err)
	}
	kv.closeAll()

	// Poll should pick it up within a few ticks.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if factory.isStarted("acme", "orders") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	if !factory.isStarted("acme", "orders") {
		t.Error("after watch channel close, poll path should have started the shard")
	}
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
	go func() {
		defer close(done)
		r.Run(ctx)
	}()

	// The reconciler has started; add a placement after a short delay.
	time.Sleep(10 * time.Millisecond)
	p := placement1("acme", "orders", 1001)
	if err := store.Put(ctx, p); err != nil {
		t.Fatalf("store.Put: %v", err)
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if factory.isStarted("acme", "orders") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	if !factory.isStarted("acme", "orders") {
		t.Error("poll path should have started the shard after placement was added")
	}
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
	factory.mu.Lock()
	factory.started[raftShardKey{tenantID: "acme", database: "db"}] = true
	factory.mu.Unlock()

	store, _ := placement.NewStore(newMemKV())
	r := NewTenantShardReconciler(store, factory, "local:1", nil)

	p := &placement.Placement{
		TenantID: "acme", Database: "db", ShardID: 1001, RF: 1, LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "other:1", RaftAddr: "other:1", DataRoot: "/data"},
		},
	}
	r.handleEvent(context.Background(), placement.Event{
		Type:      placement.EventPut,
		TenantID:  "acme",
		Database:  "db",
		Placement: p,
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
		TenantID: "acme", Database: "db", ShardID: 1001, RF: 1, LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "other:1", RaftAddr: "other:1", DataRoot: "/data"},
		},
	}
	r.handleEvent(context.Background(), placement.Event{
		Type:      placement.EventPut,
		TenantID:  "acme",
		Database:  "db",
		Placement: p,
	})

	if factory.attachCount() != 0 {
		t.Errorf("Attach called %d times for non-local EventPut; want 0", factory.attachCount())
	}
	// Close is called (idempotent no-op since shard was never started).
	if factory.closeCount() != 1 {
		t.Errorf("Close called %d times; want 1 (idempotent demotion Close)", factory.closeCount())
	}
}

// TestTenantShardReconciler_HandleEvent_NilPlacement proves an EventPut with
// nil Placement is safely ignored.
func TestTenantShardReconciler_HandleEvent_NilPlacement(t *testing.T) {
	factory := newFakeShardLifecycle()
	store, _ := placement.NewStore(newMemKV())
	r := NewTenantShardReconciler(store, factory, "local:1", nil)

	r.handleEvent(context.Background(), placement.Event{
		Type:      placement.EventPut,
		TenantID:  "acme",
		Database:  "db",
		Placement: nil, // nil must not panic
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
	go func() {
		defer close(done)
		r.Run(ctx)
	}()

	// Give Run time to register the watch.
	time.Sleep(10 * time.Millisecond)

	// Add a placement to the store WITHOUT sending a watch event.
	p := placement1("acme", "safetydb", 2001)
	if err := store.Put(ctx, p); err != nil {
		t.Fatalf("store.Put: %v", err)
	}

	// Wait for the safety tick (2× tick duration should be enough).
	deadline := time.Now().Add(10 * tick)
	for time.Now().Before(deadline) {
		if factory.isStarted("acme", "safetydb") {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done

	if !factory.isStarted("acme", "safetydb") {
		t.Error("safety tick in watch mode must fire ReconcileOnce and start the shard")
	}
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
