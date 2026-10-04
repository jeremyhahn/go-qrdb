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
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
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

	store := newHubNotifyingKVStore(hub)
	idgen, err := dao.NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatalf("NewSnowflakeGenerator: %v", err)
	}
	userSvc, err := user.NewStore(store, idgen)
	if err != nil {
		t.Fatalf("user.NewStore: %v", err)
	}

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

	store := newHubNotifyingKVStore(hub)
	idgen, err := dao.NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatalf("NewSnowflakeGenerator: %v", err)
	}
	userSvc, err := user.NewStore(store, idgen)
	if err != nil {
		t.Fatalf("user.NewStore: %v", err)
	}

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

	store := newHubNotifyingKVStore(hub)
	idgen, err := dao.NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatalf("NewSnowflakeGenerator: %v", err)
	}
	userSvc, err := user.NewStore(store, idgen)
	if err != nil {
		t.Fatalf("user.NewStore: %v", err)
	}

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

	store := newHubNotifyingKVStore(hub)
	idgen, err := dao.NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatalf("NewSnowflakeGenerator: %v", err)
	}
	userSvc, err = user.NewStore(store, idgen)
	if err != nil {
		t.Fatalf("user.NewStore: %v", err)
	}

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
