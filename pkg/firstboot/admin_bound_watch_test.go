// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package firstboot

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// hubNotifyingKVStore wraps the real in-memory store and notifies a REAL
// watch.Hub on every successful write, exactly as pkg/engine's KVEngine
// does for the production store (WithKVWatchHub). This is what makes
// TestWatchForAdminBound_RealHubObservesAddCertBinding evidence rather than
// a round-trip through a hand-built event: user.Store's own AddCertBinding
// runs unmodified against this store, so whatever key it actually commits
// to is the key the hub actually sees -- which is how go-qrdb#560 review A
// discovered that the key has to be the package's own idStringKey encoding,
// not a caller-built decimal string, and why user.PrimaryRecordKey exists.
type hubNotifyingKVStore struct {
	*kvstore.MemoryKVStore
	hub *watch.Hub
	rev atomic.Int64
}

var _ kvstore.AtomicKVStore = (*hubNotifyingKVStore)(nil)

func newHubNotifyingKVStore(hub *watch.Hub) *hubNotifyingKVStore {
	return &hubNotifyingKVStore{MemoryKVStore: kvstore.NewMemoryKVStore(), hub: hub}
}

// newSystemUserStore builds a user.UserServicer over the SAME
// tenant.IdentityNamespace(tenantid.System) confinement every production
// composer puts between the replicated KV plane and the user store
// (cmd/qrdbd/start.go's systemStore, sdk/go/node's identical construction)
// -- NOT the bare store a watch key built directly from
// user.PrimaryRecordKey would otherwise match by coincidence. The earlier
// shape of this fixture handed user.NewStore the un-namespaced hub store
// directly, which is exactly the bug WatchForAdminBound shipped with
// (go-qrdb#560 re-review item 4): a real namespaced store's writes commit
// under "system/" + the record key, which an un-namespaced test double
// can never expose. Returns the hub-notifying store too, so a caller that
// wants the raw commit-order guarantees (none of these tests currently
// do) still has it.
func newSystemUserStore(t *testing.T, hub *watch.Hub) (user.UserServicer, *hubNotifyingKVStore) {
	t.Helper()
	raw := newHubNotifyingKVStore(hub)
	namespaced, err := kvstore.NewNamespacedKVStore(raw, tenant.IdentityNamespace(tenantid.System))
	if err != nil {
		t.Fatalf("NewNamespacedKVStore: %v", err)
	}
	idgen, err := dao.NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatalf("NewSnowflakeGenerator: %v", err)
	}
	userSvc, err := user.NewStore(namespaced, idgen)
	if err != nil {
		t.Fatalf("user.NewStore: %v", err)
	}
	return userSvc, raw
}

func (s *hubNotifyingKVStore) Put(ctx context.Context, key string, value []byte) error {
	if err := s.MemoryKVStore.Put(ctx, key, value); err != nil {
		return err
	}
	rev := s.rev.Add(1)
	s.hub.NotifyPut([]byte(key), &kv.KeyValue{Key: []byte(key), Value: value, CreateRevision: rev, ModRevision: rev}, nil)
	return nil
}

func (s *hubNotifyingKVStore) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	swapped, err := s.MemoryKVStore.CompareAndSwap(ctx, key, expected, next)
	if err != nil || !swapped {
		return swapped, err
	}
	rev := s.rev.Add(1)
	s.hub.NotifyPut([]byte(key), &kv.KeyValue{Key: []byte(key), Value: next, CreateRevision: rev, ModRevision: rev}, nil)
	return true, nil
}

// capturingAdminBoundNotifier records whether and how many times AdminBound
// was called.
type capturingAdminBoundNotifier struct {
	calls atomic.Int32
}

func (n *capturingAdminBoundNotifier) AdminBound() { n.calls.Add(1) }

func validSPKIThumbprintForTest() string {
	return base64.RawURLEncoding.EncodeToString(make([]byte, 32))
}

// TestWatchForAdminBound_RealHubObservesAddCertBinding is the go-qrdb#560
// review A evidence test: it watches the admin's primary record through a
// REAL watch.Hub -- the same Watch surface svcNode.Watch exposes -- while a
// REAL user.Store (not a double) commits a real AddCertBinding, and asserts
// the notifier fires. This is what established the exact key form the hub
// sees: user.Store writes the primary record under idStringKey's fixed-width
// big-endian encoding, which only user.PrimaryRecordKey can reproduce from
// outside the package.
func TestWatchForAdminBound_RealHubObservesAddCertBinding(t *testing.T) {
	ctx := context.Background()
	hub := watch.NewHub()
	t.Cleanup(hub.Close)

	userSvc, _ := newSystemUserStore(t, hub)

	created, err := userSvc.CreateUser(ctx, &user.CreateUserRequest{
		Username:             user.BootstrapAdminUsername,
		TenantID:             tenantid.System,
		System:               true,
		WithoutEmailLoginKey: true,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	notifier := &capturingAdminBoundNotifier{}
	done := make(chan error, 1)
	go func() {
		done <- WatchForAdminBound(ctx, userSvc, hub, notifier)
	}()

	// WatchForAdminBound resolves the admin's ID and establishes its watch
	// before this test writes the binding; poll for the watcher's
	// registration rather than sleeping a fixed duration, so the test is
	// not flaky under load.
	deadline := time.Now().Add(5 * time.Second)
	for hub.WatchCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("WatchForAdminBound never registered a watcher on the hub")
		}
		time.Sleep(time.Millisecond)
	}

	if _, err := userSvc.AddCertBinding(ctx, created.ID, validSPKIThumbprintForTest(), 0); err != nil {
		t.Fatalf("AddCertBinding: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WatchForAdminBound returned an error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WatchForAdminBound never observed the binding event")
	}

	if notifier.calls.Load() != 1 {
		t.Fatalf("AdminBound called %d times, want 1", notifier.calls.Load())
	}
}

// TestWatchForAdminBound_NoAdminYetIsANoOp confirms WatchForAdminBound
// returns cleanly, calling the notifier zero times, when the admin account
// does not exist yet -- entry into "awaiting" is decided once at boot from
// the account's own state, never by this watch.
func TestWatchForAdminBound_NoAdminYetIsANoOp(t *testing.T) {
	ctx := context.Background()
	hub := watch.NewHub()
	t.Cleanup(hub.Close)

	userSvc, _ := newSystemUserStore(t, hub)

	notifier := &capturingAdminBoundNotifier{}
	if err := WatchForAdminBound(ctx, userSvc, hub, notifier); err != nil {
		t.Fatalf("WatchForAdminBound: %v", err)
	}
	if notifier.calls.Load() != 0 {
		t.Fatalf("AdminBound called %d times, want 0", notifier.calls.Load())
	}
}

// TestWatchForAdminBound_ContextCanceledReturnsNil confirms a canceled
// context ends the watch cleanly rather than reporting an error -- ordinary
// shutdown of a node that was still awaiting.
func TestWatchForAdminBound_ContextCanceledReturnsNil(t *testing.T) {
	hub := watch.NewHub()
	t.Cleanup(hub.Close)

	userSvc, _ := newSystemUserStore(t, hub)

	ctx := context.Background()
	if _, err := userSvc.CreateUser(ctx, &user.CreateUserRequest{
		Username:             user.BootstrapAdminUsername,
		TenantID:             tenantid.System,
		System:               true,
		WithoutEmailLoginKey: true,
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	watchCtx, cancel := context.WithCancel(ctx)
	notifier := &capturingAdminBoundNotifier{}
	done := make(chan error, 1)
	go func() {
		done <- WatchForAdminBound(watchCtx, userSvc, hub, notifier)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for hub.WatchCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("WatchForAdminBound never registered a watcher on the hub")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WatchForAdminBound returned an error on cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WatchForAdminBound never returned after context cancellation")
	}
	if notifier.calls.Load() != 0 {
		t.Fatalf("AdminBound called %d times, want 0", notifier.calls.Load())
	}
}

// ===========================================================================
// go-qrdb#560 review A acceptance: a simulated cluster where EVERY node
// boots "awaiting admin credential", one node binds the admin, and every
// OTHER node leaves "awaiting" through WatchForAdminBound alone -- no
// restart, no per-request re-read.
//
// This models the cluster the way multi_node_admin_credential_test.go's own
// N-node tests do (B4, already accepted in this codebase for the identical
// class of scenario): ONE shared store and ONE shared hub stand in for
// Raft's single replicated record and the identical committed writes every
// node's own hub observes, and each "node" is its own
// AwaitingAdminCredentialProbe plus its own WatchForAdminBound goroutine --
// the exact production types, wired exactly as cmd/qrdbd/start.go and
// sdk/go/node/node.go wire them, not a double for either. What a real
// 3-process cluster adds on top -- separate OS processes, separate TCP
// listeners, separate Raft log replication -- is infrastructure this
// package does not own and does not change which node's probe flips.
// ===========================================================================

// threeNodeAwaitingClusterFixture builds the shared store, hub and user
// service a cluster's 3 nodes all observe identically, with the bootstrap
// admin already created (System tenant) and NOT YET bound -- the state every
// node's own boot-time VerifyAdminCertBindingWithin check would observe
// before ceremony ever ran.
func threeNodeAwaitingClusterFixture(t *testing.T) (userSvc user.UserServicer, hub *watch.Hub, adminID uint64) {
	t.Helper()
	hub = watch.NewHub()
	t.Cleanup(hub.Close)

	userSvc, _ = newSystemUserStore(t, hub)

	created, err := userSvc.CreateUser(context.Background(), &user.CreateUserRequest{
		Username:             user.BootstrapAdminUsername,
		TenantID:             tenantid.System,
		System:               true,
		WithoutEmailLoginKey: true,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return userSvc, hub, created.ID
}

// bootNodeAwaiting runs the SAME boot-time sequence
// bootstrapAuthEnforcement/wireClientAndServer runs on a real node: resolve
// the lookup error against the shared store, Initialize the probe from it,
// and -- when that leaves the probe awaiting -- start WatchForAdminBound on
// background ctx, exactly as production code does. Returns the live probe.
func bootNodeAwaiting(t *testing.T, ctx context.Context, userSvc user.UserServicer, hub *watch.Hub) *AwaitingAdminCredentialProbe {
	t.Helper()
	lookupErr := VerifyAdminCertBinding(ctx, userSvc, time.Now().UTC())
	probe := NewAwaitingAdminCredentialProbe()
	if err := probe.Initialize(auth.MTLSBindingRequired, lookupErr); err != nil {
		t.Fatalf("probe.Initialize: %v", err)
	}
	if !probe.AwaitingAdminCredential() {
		t.Fatalf("bootNodeAwaiting: probe did not boot into awaiting; this fixture is supposed to start every node unbound")
	}
	go func() {
		_ = WatchForAdminBound(ctx, userSvc, hub, probe)
	}()
	return probe
}

// awaitNotAwaiting polls probe until it reports not-awaiting or the timeout
// elapses, failing the test in the latter case. Polling, not a fixed sleep,
// is what makes this deterministic under load rather than merely likely to
// pass: the watch event's delivery latency is not bounded by this test.
func awaitNotAwaiting(t *testing.T, probe *AwaitingAdminCredentialProbe, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for probe.AwaitingAdminCredential() {
		if time.Now().After(deadline) {
			t.Fatalf("probe still reports awaiting after %v", timeout)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestThreeNodeCluster_FollowersLeaveAwaitingViaWatch_NoRestart is the
// go-qrdb#560 review A acceptance test: all 3 simulated nodes boot
// "awaiting", the admin is bound on node 1 (standing in for the Raft
// leader, where CeremonyServiceWithAdminRebind's reissue actually runs and
// calls AdminBound directly -- the fast path this test exercises
// explicitly rather than through the watch, matching
// AwaitingAdminCredentialProbe's own documented "keep the leader's local
// fast path" contract), and nodes 2 and 3 (the followers) leave "awaiting"
// purely through WatchForAdminBound observing the SAME replicated write --
// with no restart of either and no per-request re-read.
func TestThreeNodeCluster_FollowersLeaveAwaitingViaWatch_NoRestart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	userSvc, hub, adminID := threeNodeAwaitingClusterFixture(t)

	node1 := bootNodeAwaiting(t, ctx, userSvc, hub) // the leader
	node2 := bootNodeAwaiting(t, ctx, userSvc, hub) // a follower
	node3 := bootNodeAwaiting(t, ctx, userSvc, hub) // a follower

	// All 3 watchers must be registered before the bind commits, or the
	// write could land before a watch exists to observe it.
	deadline := time.Now().Add(5 * time.Second)
	for hub.WatchCount() < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("only %d of 3 watchers registered on the hub", hub.WatchCount())
		}
		time.Sleep(time.Millisecond)
	}

	// Node 1 performs the reissue: the write commits through the
	// replicated user store (here: the shared store every node reads and
	// watches), and the node that ran it is told directly --
	// CeremonyServiceWithAdminRebind's own AdminBound call on its own
	// probe, never through this node's own watch.
	if _, err := userSvc.AddCertBinding(ctx, adminID, validSPKIThumbprintForTest(), 0); err != nil {
		t.Fatalf("AddCertBinding (simulated leader reissue): %v", err)
	}
	node1.AdminBound()

	if node1.AwaitingAdminCredential() {
		t.Fatal("node 1 (the leader) still reports awaiting immediately after its own AdminBound call")
	}
	awaitNotAwaiting(t, node2, 5*time.Second)
	awaitNotAwaiting(t, node3, 5*time.Second)
}

// TestThreeNodeCluster_LeaderChange_RemainingNodesLeaveAwaitingViaWatch is
// the leader-change variant go-qrdb#560 review A also asks for: the node
// that actually performs the bind is NOT the one the cluster would have
// called leader when it first booted "awaiting" (node 1), but a DIFFERENT
// node (node 3) -- standing in for an election that moved leadership
// between boot and the eventual `qrdb init` retry. WatchForAdminBound does
// not care which node committed the write; every node that did not perform
// it, including the former leader, still only learns of the bind through
// its own watch on the replicated record.
func TestThreeNodeCluster_LeaderChange_RemainingNodesLeaveAwaitingViaWatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	userSvc, hub, adminID := threeNodeAwaitingClusterFixture(t)

	node1 := bootNodeAwaiting(t, ctx, userSvc, hub) // leader at boot
	node2 := bootNodeAwaiting(t, ctx, userSvc, hub) // follower throughout
	node3 := bootNodeAwaiting(t, ctx, userSvc, hub) // wins an election, binds the admin

	deadline := time.Now().Add(5 * time.Second)
	for hub.WatchCount() < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("only %d of 3 watchers registered on the hub", hub.WatchCount())
		}
		time.Sleep(time.Millisecond)
	}

	// Node 3, the NEW leader after the simulated election, performs the
	// reissue and is told directly, exactly as node 1 was in the
	// no-leader-change test.
	if _, err := userSvc.AddCertBinding(ctx, adminID, validSPKIThumbprintForTest(), 0); err != nil {
		t.Fatalf("AddCertBinding (simulated post-election reissue): %v", err)
	}
	node3.AdminBound()

	if node3.AwaitingAdminCredential() {
		t.Fatal("node 3 (the new leader) still reports awaiting immediately after its own AdminBound call")
	}
	// Node 1 -- the former leader -- and node 2 -- the follower throughout --
	// both only ever learn of the bind through their own watch.
	awaitNotAwaiting(t, node1, 5*time.Second)
	awaitNotAwaiting(t, node2, 5*time.Second)
}

// ===========================================================================
// go-qrdb#560 re-review item 4: the lookup/watch-registration race, and a
// failed lookup retrying rather than ending the watch.
// ===========================================================================

// bindsOnSecondLookup is an AdminLookup that delegates to real, except that
// its SECOND call binds the admin (via bind) as a side effect BEFORE
// delegating -- simulating a bind that commits in the exact gap between
// WatchForAdminBound's initial lookup (resolving the ID, call 1) and its
// own post-registration re-check (call 2), so the test controls precisely
// when the race window closes.
type bindsOnSecondLookup struct {
	real  AdminLookup
	bind  func(ctx context.Context) error
	calls atomic.Int32
}

func (l *bindsOnSecondLookup) GetUserByUsername(ctx context.Context, tenantID, username string) (*user.User, error) {
	if l.calls.Add(1) == 2 {
		if err := l.bind(ctx); err != nil {
			return nil, err
		}
	}
	return l.real.GetUserByUsername(ctx, tenantID, username)
}

// neverDeliveringWatcher is a watch.Watcher whose Events channel never
// receives anything: a caller that only reacted to delivered events would
// block on it forever.
type neverDeliveringWatcher struct {
	events chan watch.WatchResponse
}

func newNeverDeliveringWatcher() *neverDeliveringWatcher {
	return &neverDeliveringWatcher{events: make(chan watch.WatchResponse)}
}
func (w *neverDeliveringWatcher) Events() <-chan watch.WatchResponse { return w.events }
func (w *neverDeliveringWatcher) WatchID() int64                     { return 1 }
func (w *neverDeliveringWatcher) Close() error                       { return nil }
func (w *neverDeliveringWatcher) Err() error                         { return nil }

// neverDeliveringAdminWatcher implements AdminRecordWatcher by registering
// a neverDeliveringWatcher, so the ONLY way WatchForAdminBound's caller can
// observe a bind through this double is the post-registration re-check,
// never an event.
type neverDeliveringAdminWatcher struct{}

func (neverDeliveringAdminWatcher) Watch(context.Context, string, ...watch.WatchOption) (watch.Watcher, error) {
	return newNeverDeliveringWatcher(), nil
}

// TestWatchForAdminBound_BindBetweenLookupAndWatchIsCaughtByRecheck is the
// go-qrdb#560 re-review item 4 regression test: a bind that commits in the
// gap between WatchForAdminBound's initial lookup and its own watch
// registration must still be observed. The watcher double here never
// delivers a single event, so a pass is only possible through the
// post-registration re-check this fix added, not the watch loop.
func TestWatchForAdminBound_BindBetweenLookupAndWatchIsCaughtByRecheck(t *testing.T) {
	ctx := context.Background()
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	userSvc, _ := newSystemUserStore(t, hub)

	created, err := userSvc.CreateUser(ctx, &user.CreateUserRequest{
		Username:             user.BootstrapAdminUsername,
		TenantID:             tenantid.System,
		System:               true,
		WithoutEmailLoginKey: true,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	lookup := &bindsOnSecondLookup{
		real: userSvc,
		bind: func(ctx context.Context) error {
			_, err := userSvc.AddCertBinding(ctx, created.ID, validSPKIThumbprintForTest(), 0)
			return err
		},
	}
	notifier := &capturingAdminBoundNotifier{}
	done := make(chan error, 1)
	go func() { done <- WatchForAdminBound(ctx, lookup, neverDeliveringAdminWatcher{}, notifier) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WatchForAdminBound returned an error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WatchForAdminBound never returned; the post-registration re-check did not catch the bind")
	}
	if notifier.calls.Load() != 1 {
		t.Fatalf("AdminBound called %d times, want 1", notifier.calls.Load())
	}
}

// failsNTimesThenSucceeds is an AdminLookup that fails every call until its
// internal counter reaches zero, then delegates to real -- simulating a
// transient store fault (no leader yet, a degraded shard) at the exact
// moment WatchForAdminBound starts.
type failsNTimesThenSucceeds struct {
	real   AdminLookup
	remain atomic.Int32
	fail   error
	calls  atomic.Int32
}

func (l *failsNTimesThenSucceeds) GetUserByUsername(ctx context.Context, tenantID, username string) (*user.User, error) {
	l.calls.Add(1)
	if l.remain.Add(-1) >= 0 {
		return nil, l.fail
	}
	return l.real.GetUserByUsername(ctx, tenantID, username)
}

// errTransientLookupFault is the sentinel failsNTimesThenSucceeds returns: any
// error other than *user.UserNotFoundError is a transient fault
// WatchForAdminBound must retry rather than treat as "nothing to watch."
var errTransientLookupFault = errors.New("firstboot_test: simulated transient store fault")

// TestWatchForAdminBound_TransientLookupFailureRetries is the go-qrdb#560
// re-review item 4 regression test for the other half of the fix: a
// lookup failure that is NOT "no admin account yet" must retry rather
// than end the watch. Without the fix, WatchForAdminBound returns the
// first such error immediately; this test proves it instead keeps trying
// until the lookup succeeds and the watch it registers afterward observes
// the bind.
func TestWatchForAdminBound_TransientLookupFailureRetries(t *testing.T) {
	ctx := context.Background()
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	userSvc, _ := newSystemUserStore(t, hub)

	created, err := userSvc.CreateUser(ctx, &user.CreateUserRequest{
		Username:             user.BootstrapAdminUsername,
		TenantID:             tenantid.System,
		System:               true,
		WithoutEmailLoginKey: true,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	lookup := &failsNTimesThenSucceeds{real: userSvc, fail: errTransientLookupFault}
	lookup.remain.Store(3)
	notifier := &capturingAdminBoundNotifier{}
	done := make(chan error, 1)
	go func() { done <- WatchForAdminBound(ctx, lookup, hub, notifier) }()

	// A watch without the fix ends (returns the transient error) on the
	// very first failed lookup; assert it has NOT ended well before the
	// 3 retries (50ms, 100ms, 200ms backoff) could possibly have run out.
	select {
	case err := <-done:
		t.Fatalf("WatchForAdminBound ended early on a transient lookup failure instead of retrying: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	// The retries exhaust well within this wait; the lookup then succeeds,
	// the watch registers, and the bind below must be observed exactly as
	// the real-hub evidence test proves for a lookup that never failed.
	deadline := time.Now().Add(5 * time.Second)
	for hub.WatchCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("WatchForAdminBound never registered a watcher after its retries succeeded")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := userSvc.AddCertBinding(ctx, created.ID, validSPKIThumbprintForTest(), 0); err != nil {
		t.Fatalf("AddCertBinding: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WatchForAdminBound returned an error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WatchForAdminBound never observed the binding event after its retries succeeded")
	}
	if notifier.calls.Load() != 1 {
		t.Fatalf("AdminBound called %d times, want 1", notifier.calls.Load())
	}
	if lookup.calls.Load() < 4 {
		t.Fatalf("lookup called %d times, want at least 4 (3 failures + 1 success)", lookup.calls.Load())
	}
}

// failsReCheckNTimesThenSucceeds is an AdminLookup whose FIRST call --
// WatchForAdminBound's initial ID resolution -- always succeeds, and whose
// NEXT `remain` calls -- the post-registration re-check's own retries --
// fail with a transient fault before delegating to real. It isolates a
// transient failure to the re-check specifically, distinct from
// failsNTimesThenSucceeds above, which fails the initial lookup instead.
type failsReCheckNTimesThenSucceeds struct {
	real   AdminLookup
	remain atomic.Int32
	fail   error
	calls  atomic.Int32
}

func (l *failsReCheckNTimesThenSucceeds) GetUserByUsername(ctx context.Context, tenantID, username string) (*user.User, error) {
	if n := l.calls.Add(1); n > 1 && l.remain.Add(-1) >= 0 {
		return nil, l.fail
	}
	return l.real.GetUserByUsername(ctx, tenantID, username)
}

// TestWatchForAdminBound_TransientRecheckFailureRetries is the go-qrdb#591
// re-review item B regression test: admin_bound_watch.go's post-
// registration re-check used to swallow any error from its own lookup,
// treating a transient read failure identically to "no bind landed in the
// gap." The admin is already bound BEFORE WatchForAdminBound is even
// called here, and the watcher double never delivers a single event, so
// the ONLY way this test can pass is the re-check itself recovering from
// the transient failure via the same retry lookupAdminRetrying already
// gives the initial lookup -- never by falling through to an event that
// will never arrive.
func TestWatchForAdminBound_TransientRecheckFailureRetries(t *testing.T) {
	ctx := context.Background()
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	userSvc, _ := newSystemUserStore(t, hub)

	created, err := userSvc.CreateUser(ctx, &user.CreateUserRequest{
		Username:             user.BootstrapAdminUsername,
		TenantID:             tenantid.System,
		System:               true,
		WithoutEmailLoginKey: true,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	// Bind BEFORE the watch starts at all: WatchForAdminBound's own
	// Watch() registration happens strictly after this, so an event can
	// never deliver it -- only the re-check can.
	if _, err := userSvc.AddCertBinding(ctx, created.ID, validSPKIThumbprintForTest(), 0); err != nil {
		t.Fatalf("AddCertBinding: %v", err)
	}

	lookup := &failsReCheckNTimesThenSucceeds{real: userSvc, fail: errTransientLookupFault}
	lookup.remain.Store(3)
	notifier := &capturingAdminBoundNotifier{}
	done := make(chan error, 1)
	go func() { done <- WatchForAdminBound(ctx, lookup, neverDeliveringAdminWatcher{}, notifier) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WatchForAdminBound returned an error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WatchForAdminBound never returned; a transient re-check failure must retry, " +
			"not be silently read as \"no bind landed in the gap\"")
	}
	if notifier.calls.Load() != 1 {
		t.Fatalf("AdminBound called %d times, want 1", notifier.calls.Load())
	}
	if lookup.calls.Load() < 5 {
		t.Fatalf("lookup called %d times, want at least 5 (1 initial + 3 re-check failures + 1 re-check success)", lookup.calls.Load())
	}
}

// ===========================================================================
// go-qrdb#591 re-review item B coverage-ratchet follow-up: WatchForAdminBound
// and its two helpers each have a handful of error/edge paths the tests
// above never exercise -- the initial lookup's own permanent failure, a
// watch-registration failure, a re-check that fails for the life of the
// watch, the event channel ending on its own, and lookupAdminRetrying's and
// adminBoundFromEvents' own edge branches.
// ===========================================================================

// TestWatchForAdminBound_ContextCanceledDuringInitialLookupRetryPropagatesError
// is the "first lookup never succeeds" counterpart to
// TestWatchForAdminBound_ContextCanceledReturnsNil: when the context is
// canceled while the INITIAL lookupAdminRetrying call is still retrying a
// transient fault (never a *user.UserNotFoundError), WatchForAdminBound must
// propagate that fault rather than reading the cancellation as "no admin
// account yet."
func TestWatchForAdminBound_ContextCanceledDuringInitialLookupRetryPropagatesError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	userSvc, _ := newSystemUserStore(t, hub)

	lookup := &failsNTimesThenSucceeds{real: userSvc, fail: errTransientLookupFault}
	lookup.remain.Store(1_000_000) // never succeeds within this test's lifetime

	notifier := &capturingAdminBoundNotifier{}
	done := make(chan error, 1)
	go func() { done <- WatchForAdminBound(ctx, lookup, neverDeliveringAdminWatcher{}, notifier) }()

	time.Sleep(10 * time.Millisecond) // let the first failing call enter its backoff wait
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, errTransientLookupFault) {
			t.Fatalf("WatchForAdminBound returned %v, want %v", err, errTransientLookupFault)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WatchForAdminBound never returned after context cancellation during the initial lookup's retry")
	}
	if notifier.calls.Load() != 0 {
		t.Fatalf("AdminBound called %d times, want 0", notifier.calls.Load())
	}
}

// failingAdminWatcher is an AdminRecordWatcher whose Watch call always
// fails, simulating a watch-registration failure distinct from any error
// the subsequent read path can produce.
type failingAdminWatcher struct{ err error }

func (w failingAdminWatcher) Watch(context.Context, string, ...watch.WatchOption) (watch.Watcher, error) {
	return nil, w.err
}

// TestWatchForAdminBound_WatchRegistrationFailurePropagates confirms a
// failure to register the watch itself -- as opposed to any failure in the
// lookups around it -- is returned to the caller.
func TestWatchForAdminBound_WatchRegistrationFailurePropagates(t *testing.T) {
	ctx := context.Background()
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	userSvc, _ := newSystemUserStore(t, hub)

	if _, err := userSvc.CreateUser(ctx, &user.CreateUserRequest{
		Username:             user.BootstrapAdminUsername,
		TenantID:             tenantid.System,
		System:               true,
		WithoutEmailLoginKey: true,
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	wantErr := errors.New("firstboot_test: simulated watch registration failure")
	notifier := &capturingAdminBoundNotifier{}
	if err := WatchForAdminBound(ctx, userSvc, failingAdminWatcher{err: wantErr}, notifier); !errors.Is(err, wantErr) {
		t.Fatalf("WatchForAdminBound returned %v, want %v", err, wantErr)
	}
	if notifier.calls.Load() != 0 {
		t.Fatalf("AdminBound called %d times, want 0", notifier.calls.Load())
	}
}

// failsAfterFirstCall is an AdminLookup whose first call -- WatchForAdminBound's
// initial ID resolution -- delegates to real, and every call after that
// fails with a transient fault forever. It isolates "the post-registration
// re-check can never succeed, for the life of the watch" from the admin
// ever being found at all, distinct from failsReCheckNTimesThenSucceeds,
// which eventually recovers.
type failsAfterFirstCall struct {
	real  AdminLookup
	fail  error
	calls atomic.Int32
}

func (l *failsAfterFirstCall) GetUserByUsername(ctx context.Context, tenantID, username string) (*user.User, error) {
	if l.calls.Add(1) == 1 {
		return l.real.GetUserByUsername(ctx, tenantID, username)
	}
	return nil, l.fail
}

// TestWatchForAdminBound_ContextCanceledDuringRecheckRetryReturnsNil is the
// re-check's own counterpart to the initial-lookup test above: when the
// re-check's lookupAdminRetrying is retrying a transient fault that never
// clears, and the context is canceled while it does, WatchForAdminBound
// must end the watch cleanly (nil) -- an ordinary shutdown, not a verdict
// that the re-check found no binding.
func TestWatchForAdminBound_ContextCanceledDuringRecheckRetryReturnsNil(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	userSvc, _ := newSystemUserStore(t, hub)

	if _, err := userSvc.CreateUser(ctx, &user.CreateUserRequest{
		Username:             user.BootstrapAdminUsername,
		TenantID:             tenantid.System,
		System:               true,
		WithoutEmailLoginKey: true,
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	lookup := &failsAfterFirstCall{real: userSvc, fail: errTransientLookupFault}
	notifier := &capturingAdminBoundNotifier{}
	done := make(chan error, 1)
	go func() { done <- WatchForAdminBound(ctx, lookup, neverDeliveringAdminWatcher{}, notifier) }()

	time.Sleep(10 * time.Millisecond) // let the re-check's first failing call enter its backoff wait
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WatchForAdminBound returned a non-nil error on context cancellation during the re-check's retry: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WatchForAdminBound never returned after context cancellation during the re-check's retry")
	}
	if notifier.calls.Load() != 0 {
		t.Fatalf("AdminBound called %d times, want 0", notifier.calls.Load())
	}
}

// closedEventsWatcher is a watch.Watcher whose Events channel is already
// closed, simulating the watch ending on its own -- e.g. the underlying hub
// shutting down -- rather than via context cancellation.
type closedEventsWatcher struct {
	events chan watch.WatchResponse
	err    error
}

func newClosedEventsWatcher(err error) *closedEventsWatcher {
	w := &closedEventsWatcher{events: make(chan watch.WatchResponse), err: err}
	close(w.events)
	return w
}
func (w *closedEventsWatcher) Events() <-chan watch.WatchResponse { return w.events }
func (w *closedEventsWatcher) WatchID() int64                     { return 1 }
func (w *closedEventsWatcher) Close() error                       { return nil }
func (w *closedEventsWatcher) Err() error                         { return w.err }

// closedEventsAdminWatcher implements AdminRecordWatcher by registering a
// closedEventsWatcher, so WatchForAdminBound's event loop observes the
// channel closing on its very first receive.
type closedEventsAdminWatcher struct{ err error }

func (w closedEventsAdminWatcher) Watch(context.Context, string, ...watch.WatchOption) (watch.Watcher, error) {
	return newClosedEventsWatcher(w.err), nil
}

// TestWatchForAdminBound_ClosedEventsChannelReturnsWatcherErr confirms the
// event loop reports the watcher's own Err() -- not nil, not a generic
// sentinel -- when the Events channel closes rather than delivering a
// binding.
func TestWatchForAdminBound_ClosedEventsChannelReturnsWatcherErr(t *testing.T) {
	ctx := context.Background()
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	userSvc, _ := newSystemUserStore(t, hub)

	if _, err := userSvc.CreateUser(ctx, &user.CreateUserRequest{
		Username:             user.BootstrapAdminUsername,
		TenantID:             tenantid.System,
		System:               true,
		WithoutEmailLoginKey: true,
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	wantErr := errors.New("firstboot_test: simulated watch end")
	notifier := &capturingAdminBoundNotifier{}
	if err := WatchForAdminBound(ctx, userSvc, closedEventsAdminWatcher{err: wantErr}, notifier); !errors.Is(err, wantErr) {
		t.Fatalf("WatchForAdminBound returned %v, want %v", err, wantErr)
	}
	if notifier.calls.Load() != 0 {
		t.Fatalf("AdminBound called %d times, want 0", notifier.calls.Load())
	}
}

// TestLookupAdminRetrying_BackoffCapsAtOneSecond drives lookupAdminRetrying
// through 5 transient failures -- enough for its doubling backoff (50 ->
// 100 -> 200 -> 400 -> 800 -> 1600, capped to adminBoundWatchRetryCap) to
// exercise the cap-enforcement branch at least once -- before the 6th call
// succeeds.
func TestLookupAdminRetrying_BackoffCapsAtOneSecond(t *testing.T) {
	ctx := context.Background()
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	userSvc, _ := newSystemUserStore(t, hub)

	created, err := userSvc.CreateUser(ctx, &user.CreateUserRequest{
		Username:             user.BootstrapAdminUsername,
		TenantID:             tenantid.System,
		System:               true,
		WithoutEmailLoginKey: true,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	lookup := &failsNTimesThenSucceeds{real: userSvc, fail: errTransientLookupFault}
	lookup.remain.Store(5)

	u, err := lookupAdminRetrying(ctx, lookup)
	if err != nil {
		t.Fatalf("lookupAdminRetrying: %v", err)
	}
	if u.ID != created.ID {
		t.Fatalf("lookupAdminRetrying returned user %d, want %d", u.ID, created.ID)
	}
	if lookup.calls.Load() < 6 {
		t.Fatalf("lookup called %d times, want at least 6 (5 failures + 1 success)", lookup.calls.Load())
	}
}

// TestAdminBoundFromEvents_SkipsInvalidEntriesAndReturnsFalseWithoutABoundEvent
// covers adminBoundFromEvents' skip branches -- a nil event, an event with a
// nil Kv, a delete event, and an event whose value fails to decode as a
// user.User -- and confirms it returns false when nothing in the list
// carries a live binding, rather than panicking or returning true.
func TestAdminBoundFromEvents_SkipsInvalidEntriesAndReturnsFalseWithoutABoundEvent(t *testing.T) {
	if adminBoundFromEvents(nil) {
		t.Fatal("adminBoundFromEvents(nil) = true, want false")
	}

	ctx := context.Background()
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	userSvc, _ := newSystemUserStore(t, hub)
	if _, err := userSvc.CreateUser(ctx, &user.CreateUserRequest{
		Username:             user.BootstrapAdminUsername,
		TenantID:             tenantid.System,
		System:               true,
		WithoutEmailLoginKey: true,
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	unbound, err := userSvc.GetUserByUsername(ctx, tenantid.System, user.BootstrapAdminUsername)
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	unboundJSON, err := json.Marshal(unbound)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	evts := []*watch.Event{
		nil,
		{Kv: nil},
		{Type: watch.EventDelete, Kv: &kv.KeyValue{Value: unboundJSON}},
		{Type: watch.EventPut, Kv: &kv.KeyValue{Value: []byte("not valid json")}},
		{Type: watch.EventPut, Kv: &kv.KeyValue{Value: unboundJSON}},
	}
	if adminBoundFromEvents(evts) {
		t.Fatal("adminBoundFromEvents = true, want false: no event in the list carries a live binding")
	}
}

// TestAdminBoundFromEvents_TrueWhenAnEventCarriesALiveBinding is the
// positive counterpart: a real, bound admin record decodes cleanly and is
// recognized.
func TestAdminBoundFromEvents_TrueWhenAnEventCarriesALiveBinding(t *testing.T) {
	ctx := context.Background()
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	userSvc, _ := newSystemUserStore(t, hub)

	created, err := userSvc.CreateUser(ctx, &user.CreateUserRequest{
		Username:             user.BootstrapAdminUsername,
		TenantID:             tenantid.System,
		System:               true,
		WithoutEmailLoginKey: true,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := userSvc.AddCertBinding(ctx, created.ID, validSPKIThumbprintForTest(), 0); err != nil {
		t.Fatalf("AddCertBinding: %v", err)
	}
	bound, err := userSvc.GetUserByUsername(ctx, tenantid.System, user.BootstrapAdminUsername)
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	boundJSON, err := json.Marshal(bound)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	evts := []*watch.Event{
		{Type: watch.EventPut, Kv: &kv.KeyValue{Value: boundJSON}},
	}
	if !adminBoundFromEvents(evts) {
		t.Fatal("adminBoundFromEvents = false, want true: the event carries a live certificate binding")
	}
}
