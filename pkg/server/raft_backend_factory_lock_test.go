package server

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// lockTestDeadline bounds a call that must not wait on another caller's
// held read. It is far longer than any local start on an in-memory host, so
// only a call queued behind the held read reaches it.
const lockTestDeadline = 10 * time.Second

// shardOfa and shardOfb are the raft shards these tests give databases
// "a" and "b": ListStarted and ListOnHost report them with each shard.
const (
	shardOfa uint64 = 1001
	shardOfb uint64 = 1002
)

// heldRead is an identitiesOverride that holds the FIRST read of one shard's
// member identities until unhold, as a system shard that has stopped
// answering holds it, and answers every other read at once. It answers what
// service.MemberIdentities answers on a host without a transport -- no
// identities -- so a start that gets past it is an ordinary start.
type heldRead struct {
	shardID uint64
	calls   atomic.Int64
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

// holdRead installs a heldRead for shardID on f. The test's cleanup releases
// the read before the factory's own cleanup stops its shards.
func holdRead(t *testing.T, f *RaftBackendFactory, shardID uint64) *heldRead {
	t.Helper()
	h := &heldRead{shardID: shardID, entered: make(chan struct{}, 16), release: make(chan struct{})}
	f.identitiesOverride = h.identities
	t.Cleanup(h.unhold)
	return h
}

func (h *heldRead) identities(ctx context.Context, shardID, _ uint64, _ bool, _ map[uint64]string) (map[uint64]string, error) {
	if shardID != h.shardID {
		return nil, nil
	}
	n := h.calls.Add(1)
	h.entered <- struct{}{}
	if n == 1 {
		select {
		case <-h.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, nil
}

func (h *heldRead) unhold() { h.once.Do(func() { close(h.release) }) }

// awaitHeld waits until the held read has begun.
func (h *heldRead) awaitHeld(t *testing.T) {
	t.Helper()
	select {
	case <-h.entered:
	case <-time.After(lockTestDeadline):
		t.Fatalf("no read of shard %d began", h.shardID)
	}
}

// within runs fn and fails the test if fn has not returned by the deadline:
// a call that queues behind another caller's held read is the defect.
func within(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(lockTestDeadline):
		t.Fatalf("%s waited on another caller's held system-shard read", what)
	}
}

type servicesResult struct {
	svcs *DatabaseServices
	err  error
}

// resolveAsync calls Services in its own goroutine and delivers the answer.
func resolveAsync(ctx context.Context, f *RaftBackendFactory, tenantID, db string) <-chan servicesResult {
	out := make(chan servicesResult, 1)
	go func() {
		svcs, err := f.Services(ctx, tenantID, db)
		out <- servicesResult{svcs: svcs, err: err}
	}()
	return out
}

// TestRaftBackendFactory_HeldReadBlocksNoOtherCaller: while one database's
// start is held in its system-shard read, every other caller of the factory
// -- another database's resolve, ListStarted, LeadsShard -- still answers.
func TestRaftBackendFactory_HeldReadBlocksNoOtherCaller(t *testing.T) {
	f, reg, src := newRaftFactory(t)
	dataRoot, _ := registerVolumeTenant(t, reg, "rt")
	src.add("rt", "a", 1001, dataRoot)
	src.add("rt", "b", 1002, dataRoot)
	held := holdRead(t, f, 1001)
	ctx := context.Background()

	a := resolveAsync(ctx, f, "rt", "a")
	held.awaitHeld(t)

	within(t, "Services for another database", func() {
		if _, err := f.Services(ctx, "rt", "b"); err != nil {
			t.Errorf("Services(b) = %v, want nil", err)
		}
	})
	within(t, "ListStarted", func() {
		if got := f.ListStarted(); !slices.Equal(got, []StartedShard{{TenantID: "rt", Database: "b", ShardID: shardOfb}}) {
			t.Errorf("ListStarted = %v, want only b: a is still being planned", got)
		}
	})
	within(t, "LeadsShard", func() { _ = f.LeadsShard("rt", "b") })

	held.unhold()
	if r := <-a; r.err != nil {
		t.Fatalf("Services(a) after the read answered = %v, want nil", r.err)
	}
}

// TestRaftBackendFactory_ShardStartedDuringReadIsServedNotStartedAgain: a
// second caller starts the shard while the first caller's read is held. The
// first, once its read answers, serves the shard the second started rather
// than starting it again.
func TestRaftBackendFactory_ShardStartedDuringReadIsServedNotStartedAgain(t *testing.T) {
	f, reg, src := newRaftFactory(t)
	dataRoot, _ := registerVolumeTenant(t, reg, "rt")
	src.add("rt", "a", 1001, dataRoot)
	held := holdRead(t, f, 1001)
	ctx := context.Background()

	first := resolveAsync(ctx, f, "rt", "a")
	held.awaitHeld(t)

	var second *DatabaseServices
	within(t, "a second Services for the same database", func() {
		var err error
		if second, err = f.Services(ctx, "rt", "a"); err != nil {
			t.Errorf("second Services(a) = %v, want nil", err)
		}
	})

	held.unhold()
	r := <-first
	if r.err != nil {
		t.Fatalf("first Services(a) = %v, want the shard the second caller started", r.err)
	}
	if r.svcs != second {
		t.Fatal("the first caller came back with services other than the started shard's")
	}
	if n := held.calls.Load(); n != 2 {
		t.Fatalf("identity reads for shard 1001 = %d, want 2: one per caller, none after the shard was cached", n)
	}
	if got := f.ListStarted(); len(got) != 1 {
		t.Fatalf("ListStarted = %v, want the one shard", got)
	}
}

// TestRaftBackendFactory_ShardStoppedDuringReadPlansAgain: a shard is stopped
// while a start's read is held. The start's plan may rest on a placement
// that no longer holds, so it is planned again before anything starts.
func TestRaftBackendFactory_ShardStoppedDuringReadPlansAgain(t *testing.T) {
	f, reg, src := newRaftFactory(t)
	dataRoot, _ := registerVolumeTenant(t, reg, "rt")
	src.add("rt", "a", 1001, dataRoot)
	src.add("rt", "b", 1002, dataRoot)
	ctx := context.Background()
	if _, err := f.Services(ctx, "rt", "b"); err != nil {
		t.Fatalf("Services(b) = %v, want nil", err)
	}
	held := holdRead(t, f, 1001)

	a := resolveAsync(ctx, f, "rt", "a")
	held.awaitHeld(t)
	within(t, "Close of another database", func() {
		if err := f.Close("rt", "b"); err != nil {
			t.Errorf("Close(b) = %v, want nil", err)
		}
	})

	held.unhold()
	if r := <-a; r.err != nil {
		t.Fatalf("Services(a) = %v, want nil", r.err)
	}
	if n := held.calls.Load(); n != 2 {
		t.Fatalf("identity reads for shard 1001 = %d, want 2: the stop during the read must send the start back to plan", n)
	}
	if got := f.ListStarted(); !slices.Equal(got, []StartedShard{{TenantID: "rt", Database: "a", ShardID: shardOfa}}) {
		t.Fatalf("ListStarted = %v, want only a", got)
	}
}

// noQuorumPlacement places shardID on this node and on two replicas that do
// not exist, so the shard starts on the host but never elects a leader: a
// founding start whose quorum never forms.
func noQuorumPlacement(tenantID, db string, shardID uint64) *placement.Placement {
	return &placement.Placement{TenantID: tenantID, DatabaseID: db, Database: db, ShardID: shardID, Replicas: []placement.Replica{
		{ReplicaID: 1, NodeID: sharedHostRaftAddr, RaftAddr: sharedHostRaftAddr},
		{ReplicaID: 2, NodeID: "127.0.0.1:9", RaftAddr: "127.0.0.1:9"},
		{ReplicaID: 3, NodeID: "127.0.0.1:11", RaftAddr: "127.0.0.1:11"},
	}}
}

// awaitStarting waits until (tenantID, db)'s start is on the host and
// waiting for the shard to serve.
func awaitStarting(t *testing.T, f *RaftBackendFactory, tenantID, db string) {
	t.Helper()
	key := raftShardKey{tenantID: tenantID, database: db}
	deadline := time.Now().Add(lockTestDeadline)
	for {
		f.mu.Lock()
		_, inFlight := f.starting[key]
		f.mu.Unlock()
		if inFlight {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the start of %s/%s never reached its wait for a leader", tenantID, db)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// requireAwaitLeader fails unless err is the start's wait for a leader
// ending without one.
func requireAwaitLeader(t *testing.T, what string, err error) {
	t.Helper()
	var rbe *RaftBackendError
	if !errors.As(err, &rbe) || rbe.Op != "await-leader" {
		t.Fatalf("%s = %v, want the await-leader *RaftBackendError", what, err)
	}
}

// TestRaftBackendFactory_StartWithoutQuorumBlocksNoOtherCaller: a founding
// start whose quorum never forms waits for its leader without holding the
// factory: another database resolves and ListStarted answers meanwhile. The
// start is not listed until it serves, and failing leaves nothing behind.
func TestRaftBackendFactory_StartWithoutQuorumBlocksNoOtherCaller(t *testing.T) {
	f, reg, src := newRaftFactory(t)
	dataRoot, _ := registerVolumeTenant(t, reg, "rt")
	src.add("rt", "a", 1001, dataRoot)
	src.add("rt", "b", 1002, dataRoot)
	src.placements = map[raftShardKey]*placement.Placement{{tenantID: "rt", database: "a"}: noQuorumPlacement("rt", "a", 1001)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := resolveAsync(ctx, f, "rt", "a")
	awaitStarting(t, f, "rt", "a")

	within(t, "Services for another database", func() {
		if _, err := f.Services(context.Background(), "rt", "b"); err != nil {
			t.Errorf("Services(b) = %v, want nil", err)
		}
	})
	within(t, "ListStarted", func() {
		if got := f.ListStarted(); !slices.Equal(got, []StartedShard{{TenantID: "rt", Database: "b", ShardID: shardOfb}}) {
			t.Errorf("ListStarted = %v, want only b: a has not served", got)
		}
	})
	within(t, "LeadsShard", func() { _ = f.LeadsShard("rt", "b") })

	cancel()
	requireAwaitLeader(t, "Services(a)", (<-a).err)
	if got := f.ListStarted(); !slices.Equal(got, []StartedShard{{TenantID: "rt", Database: "b", ShardID: shardOfb}}) {
		t.Fatalf("ListStarted after a's failed start = %v, want only b", got)
	}
	if _, err := f.host.ShardInfo(1001); err == nil {
		t.Fatal("a's failed start left its shard running on the host")
	}
}

// TestRaftBackendFactory_StartInFlightIsAwaitedNotRepeated: callers that
// queued behind a start on the factory lock -- the same database's resolve,
// its Close -- wait for that start's outcome instead, and act on it as they
// did then: the resolve makes its own start once the first fails, and the
// Close, whether it acts before that start or after it fails, finds nothing
// to stop.
func TestRaftBackendFactory_StartInFlightIsAwaitedNotRepeated(t *testing.T) {
	f, reg, src := newRaftFactory(t)
	dataRoot, _ := registerVolumeTenant(t, reg, "rt")
	src.add("rt", "a", 1001, dataRoot)
	src.placements = map[raftShardKey]*placement.Placement{{tenantID: "rt", database: "a"}: noQuorumPlacement("rt", "a", 1001)}
	var reads atomic.Int64
	f.identitiesOverride = func(context.Context, uint64, uint64, bool, map[uint64]string) (map[uint64]string, error) {
		reads.Add(1)
		return nil, nil
	}
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	secondCtx, cancelSecond := context.WithCancel(context.Background())
	defer cancelSecond()

	first := resolveAsync(firstCtx, f, "rt", "a")
	awaitStarting(t, f, "rt", "a")
	second := resolveAsync(secondCtx, f, "rt", "a")
	closed := make(chan error, 1)
	go func() { closed <- f.Close("rt", "a") }()

	select {
	case r := <-second:
		t.Fatalf("second Services(a) = %v while the first start was in flight, want it to wait", r.err)
	case err := <-closed:
		t.Fatalf("Close(a) = %v while the start was in flight, want it to wait", err)
	case <-time.After(300 * time.Millisecond):
	}
	if n := reads.Load(); n != 1 {
		t.Fatalf("plans while one start was in flight = %d, want 1", n)
	}

	cancelFirst()
	requireAwaitLeader(t, "first Services(a)", (<-first).err)
	// The failed start wakes the second caller and Close together, in either
	// order. Close waits on every start of a it finds in flight, so when the
	// second caller's start is registered first, Close waits on that one too
	// and returns only after it ends: Close is judged once both have.
	awaitStarting(t, f, "rt", "a")
	cancelSecond()
	requireAwaitLeader(t, "second Services(a)", (<-second).err)
	if err := <-closed; err != nil {
		t.Fatalf("Close(a) after the failed starts = %v, want nil", err)
	}
	if n := reads.Load(); n != 2 {
		t.Fatalf("plans = %d, want 2: the second caller starts again once the first start fails", n)
	}
	if got := f.ListStarted(); len(got) != 0 {
		t.Fatalf("ListStarted = %v, want none", got)
	}
}

// TestRaftBackendFactory_ListOnHostIncludesStartsInFlight pins the two
// listings apart: a founding start waiting for a quorum runs on the host
// before it is cached, so ListOnHost, which teardown enumerates through,
// names it while ListStarted, which the schedulers walk, does not.
func TestRaftBackendFactory_ListOnHostIncludesStartsInFlight(t *testing.T) {
	f, reg, src := newRaftFactory(t)
	dataRoot, _ := registerVolumeTenant(t, reg, "rt")
	src.add("rt", "a", 1001, dataRoot)
	src.add("rt", "b", 1002, dataRoot)
	src.placements = map[raftShardKey]*placement.Placement{{tenantID: "rt", database: "a"}: noQuorumPlacement("rt", "a", 1001)}
	f.identitiesOverride = func(context.Context, uint64, uint64, bool, map[uint64]string) (map[uint64]string, error) {
		return nil, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := f.Services(ctx, "rt", "b"); err != nil {
		t.Fatalf("Services(b) = %v, want nil", err)
	}

	a := resolveAsync(ctx, f, "rt", "a")
	awaitStarting(t, f, "rt", "a")
	if got := f.ListStarted(); !slices.Equal(got, []StartedShard{{TenantID: "rt", Database: "b", ShardID: shardOfb}}) {
		t.Fatalf("ListStarted = %v, want only the serving b", got)
	}
	got := f.ListOnHost()
	slices.SortFunc(got, func(x, y StartedShard) int { return strings.Compare(x.Database, y.Database) })
	if want := []StartedShard{{TenantID: "rt", Database: "a", ShardID: shardOfa}, {TenantID: "rt", Database: "b", ShardID: shardOfb}}; !slices.Equal(got, want) {
		t.Fatalf("ListOnHost = %v, want %v: the start in flight runs on the host", got, want)
	}

	cancel()
	requireAwaitLeader(t, "Services(a)", (<-a).err)
	if got := f.ListOnHost(); !slices.Equal(got, []StartedShard{{TenantID: "rt", Database: "b", ShardID: shardOfb}}) {
		t.Fatalf("ListOnHost after the failed start = %v, want only b", got)
	}
}

// endStartOnClose is the factory as the seal enforcer sees it, except that
// Close first ends the start in flight it is about to wait for, the way the
// start's own caller giving up would. It overrides Close alone, so the
// listing the enforcer walks is whichever one the enforcer asks for.
type endStartOnClose struct {
	*RaftBackendFactory
	endStart context.CancelFunc
}

func (e endStartOnClose) Close(tenantID, db string) error {
	e.endStart()
	return e.RaftBackendFactory.Close(tenantID, db)
}

// TestSealEnforcer_SealTenantStopsAStartInFlight is the Codex finding on
// #772: a founding shard running on the host but not yet serving sat only in
// the factory's starts in flight, which the seal never enumerated, so the
// tenant's key was wiped under a running shard. SealTenant must reach that
// start and see it stopped before it seals the barrier.
func TestSealEnforcer_SealTenantStopsAStartInFlight(t *testing.T) {
	f, reg, src := newRaftFactory(t)
	dataRoot, _ := registerVolumeTenant(t, reg, "rt")
	src.add("rt", "a", 1001, dataRoot)
	src.placements = map[raftShardKey]*placement.Placement{{tenantID: "rt", database: "a"}: noQuorumPlacement("rt", "a", 1001)}
	f.identitiesOverride = func(context.Context, uint64, uint64, bool, map[uint64]string) (map[uint64]string, error) {
		return nil, nil
	}
	startCtx, endStart := context.WithCancel(context.Background())
	defer endStart()

	started := resolveAsync(startCtx, f, "rt", "a")
	awaitStarting(t, f, "rt", "a")
	e := &SealEnforcer{Barriers: reg, Shards: endStartOnClose{RaftBackendFactory: f, endStart: endStart}}
	within(t, "SealTenant", func() {
		if err := e.SealTenant(context.Background(), "rt"); err != nil {
			t.Errorf("SealTenant = %v, want nil", err)
		}
	})

	f.mu.Lock()
	_, inFlight := f.starting[raftShardKey{tenantID: "rt", database: "a"}]
	f.mu.Unlock()
	if inFlight {
		t.Fatal("SealTenant returned with the tenant's start still running on the host: the key was wiped under it")
	}
	requireAwaitLeader(t, "Services(a)", (<-started).err)
	if got := f.ListOnHost(); len(got) != 0 {
		t.Fatalf("ListOnHost after SealTenant = %v, want none", got)
	}
	if e.TenantUnsealed("rt") {
		t.Fatal("SealTenant left the tenant's barrier unsealed")
	}
}

// TestRaftBackendFactory_ConcurrentColdServicesRegisterSealObserversSafely is
// the first Codex finding on #812: planStart registered each tenant's seal
// observer in sealWatched without f.mu, so cold Services calls racing for two
// tenants wrote the map at once. Run under -race, it fails without the lock.
func TestRaftBackendFactory_ConcurrentColdServicesRegisterSealObserversSafely(t *testing.T) {
	host, mux := newSharedTestHostWithMux(t)
	reg := newVolumePooledRegistry(t)
	src := newFakeRaftInfoSource()
	f := NewRaftBackendFactory(host, sharedHostReplicaID, sharedHostRaftAddr, reg, src,
		volTestLogger(), WithMultiplexLogDB(mux))
	t.Cleanup(func() { _ = f.CloseAll() })

	tenants := []struct {
		id    string
		shard uint64
	}{{"ca", 2400}, {"cb", 2401}}
	for _, tn := range tenants {
		root, _ := registerVolumeTenant(t, reg, tn.id)
		src.add(tn.id, "app", tn.shard, root)
		src.setDedicated(tn.id, "app")
	}

	const callersPerTenant = 4
	var wg sync.WaitGroup
	errs := make(chan error, len(tenants)*callersPerTenant)
	for _, tn := range tenants {
		for range callersPerTenant {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := f.Services(context.Background(), tn.id, "app"); err != nil {
					errs <- err
				}
			}()
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent cold Services: %v", err)
	}

	f.mu.Lock()
	watched := len(f.sealWatched)
	f.mu.Unlock()
	if watched != len(tenants) {
		t.Fatalf("%d barriers watched; want one per tenant (%d)", watched, len(tenants))
	}
}

// TestRaftBackendFactory_BarrierSealStopsAStartInFlight is the second Codex
// finding on #812: a founding shard waiting in WaitForShardServing runs on the
// host but sits only in f.starting, so the sealing observer, which listed
// f.shards alone, let the barrier wipe the key under its dedicated WAL. The
// seal now waits for that start to end and returns only once the shard is
// stopped. The start ends here as it does on a node: its wait for the shard to
// serve returns, by the caller's context ending.
func TestRaftBackendFactory_BarrierSealStopsAStartInFlight(t *testing.T) {
	f, reg, src := newRaftFactory(t)
	dataRoot, tb := registerVolumeTenant(t, reg, "rt")
	src.add("rt", "a", 1001, dataRoot)
	src.placements = map[raftShardKey]*placement.Placement{{tenantID: "rt", database: "a"}: noQuorumPlacement("rt", "a", 1001)}
	f.identitiesOverride = func(context.Context, uint64, uint64, bool, map[uint64]string) (map[uint64]string, error) {
		return nil, nil
	}
	startCtx, endStart := context.WithCancel(context.Background())
	defer endStart()

	started := resolveAsync(startCtx, f, "rt", "a")
	awaitStarting(t, f, "rt", "a")
	sealed := make(chan error, 1)
	go func() { sealed <- tb.Seal() }()

	// The seal lists the barrier as sealing, then waits on the start.
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		_, sealing := f.sealing[tb.Barrier()]
		f.mu.Unlock()
		if sealing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the seal never listed the tenant's barrier as sealing")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case err := <-sealed:
		t.Fatalf("Seal = %v while the tenant's start was in flight, want it to wait", err)
	case <-time.After(300 * time.Millisecond):
	}

	endStart()
	within(t, "Seal", func() {
		if err := <-sealed; err != nil {
			t.Errorf("Seal = %v, want nil", err)
		}
	})
	f.mu.Lock()
	_, inFlight := f.starting[raftShardKey{tenantID: "rt", database: "a"}]
	f.mu.Unlock()
	if inFlight {
		t.Fatal("Seal returned with the tenant's start still running on the host: the key was wiped under it")
	}
	requireAwaitLeader(t, "Services(a)", (<-started).err)
	if got := f.ListOnHost(); len(got) != 0 {
		t.Fatalf("ListOnHost after Seal = %v, want none", got)
	}
	if !tb.IsSealed() {
		t.Fatal("Seal left the tenant's barrier unsealed")
	}
}

// TestRaftBackendFactory_StartSucceedsAgainAfterUnseal checks that the seal's
// refusal of new starts lasts only until the barrier is unsealed: a start
// planned while the barrier was listed as sealing is refused, and once the
// unseal clears the listing the tenant's shard starts and serves again.
func TestRaftBackendFactory_StartSucceedsAgainAfterUnseal(t *testing.T) {
	host, mux := newSharedTestHostWithMux(t)
	reg := newVolumePooledRegistry(t)
	src := newFakeRaftInfoSource()
	f := NewRaftBackendFactory(host, sharedHostReplicaID, sharedHostRaftAddr, reg, src,
		volTestLogger(), WithMultiplexLogDB(mux))
	t.Cleanup(func() { _ = f.CloseAll() })

	ctx := context.Background()
	root, tb := registerVolumeTenant(t, reg, "su")
	const shardID = uint64(2402)
	src.add("su", "app", shardID, root)
	src.setDedicated("su", "app")
	if _, err := f.Services(ctx, "su", "app"); err != nil {
		t.Fatalf("Services: %v", err)
	}
	// A plan made while the barrier was unsealed, then started after the
	// seal began: startPlanned refuses it.
	plan, err := f.planStart(ctx, "su", "app", true, false)
	if err != nil {
		t.Fatalf("planStart: %v", err)
	}
	if err := tb.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	f.mu.Lock()
	generation := f.generation
	f.mu.Unlock()
	if _, _, err := f.startPlanned(ctx, raftShardKey{tenantID: "su", database: "app"}, plan, generation); !errors.Is(err, seal.ErrSealed) {
		t.Fatalf("start planned before the seal, started after it = %v; want seal.ErrSealed", err)
	}

	if err := reg.UnsealTenant(ctx, "su", seal.Credentials{}); err != nil {
		t.Fatalf("UnsealTenant: %v", err)
	}
	f.mu.Lock()
	_, sealing := f.sealing[tb.Barrier()]
	f.mu.Unlock()
	if sealing {
		t.Fatal("the unseal left the barrier listed as sealing")
	}
	svcs, err := f.Services(ctx, "su", "app")
	if err != nil {
		t.Fatalf("Services after unseal: %v", err)
	}
	if !mux.IsDedicated(shardID) {
		t.Fatal("the dedicated WAL was not registered again after unseal")
	}
	if err := svcs.KV.Put(ctx, "k", []byte("after-unseal")); err != nil {
		t.Fatalf("Put after unseal: %v", err)
	}
}

// TestRaftBackendFactory_PurgeDuringReadDropsThePlan is the second Codex
// finding on #772: a start that has read the placement and is reading its
// members' identities when the placement delete purges the database holds
// neither a cached shard nor a start in flight, so the purge left the
// generation alone and the plan went on to start the deleted shard, which
// nothing would ever stop. The purge must send the plan back to read the
// placement, which no longer names the database.
func TestRaftBackendFactory_PurgeDuringReadDropsThePlan(t *testing.T) {
	f, reg, src := newRaftFactory(t)
	dataRoot, _ := registerVolumeTenant(t, reg, "rt")
	src.add("rt", "a", 1001, dataRoot)
	held := holdRead(t, f, 1001)

	a := resolveAsync(context.Background(), f, "rt", "a")
	held.awaitHeld(t)
	delete(src.entries, raftShardKey{tenantID: "rt", database: "a"})
	within(t, "CloseAndPurge of the database being planned", func() {
		if err := f.CloseAndPurge("rt", "a"); err != nil {
			t.Errorf("CloseAndPurge(a) = %v, want nil", err)
		}
	})

	held.unhold()
	if r := <-a; !errors.Is(r.err, ErrRaftDatabaseUnknown) {
		t.Fatalf("Services(a) = %v, want ErrRaftDatabaseUnknown: the purge must void the plan made before it", r.err)
	}
	if got := f.ListOnHost(); len(got) != 0 {
		t.Fatalf("ListOnHost = %v, want none: the purged database was started", got)
	}
}
