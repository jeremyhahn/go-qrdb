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

package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// fakeStore is an in-memory MembershipStore for tests. Tests drive membership
// change notifications by sending on the events channel.
type fakeStore struct {
	mu       sync.Mutex
	puts     map[uint64][]byte
	putCalls int
	deletes  []uint64
	events   chan MemberEvent
	putErr   error
	watchErr error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		puts:   make(map[uint64][]byte),
		events: make(chan MemberEvent, 8),
	}
}

func (f *fakeStore) PutMember(_ context.Context, nodeID uint64, data []byte, _ time.Duration) error {
	if f.putErr != nil {
		return f.putErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts[nodeID] = data
	f.putCalls++
	return nil
}

func (f *fakeStore) DeleteMember(_ context.Context, nodeID uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, nodeID)
	return nil
}

func (f *fakeStore) WatchMembers(_ context.Context) (<-chan MemberEvent, error) {
	if f.watchErr != nil {
		return nil, f.watchErr
	}
	return f.events, nil
}

func (f *fakeStore) ListMembers(_ context.Context) ([]Member, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Member, 0, len(f.puts))
	for _, data := range f.puts {
		var m Member
		if json.Unmarshal(data, &m) == nil {
			out = append(out, m)
		}
	}
	return out, nil
}

func (f *fakeStore) putCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.puts)
}

func (f *fakeStore) putCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.putCalls
}

func (f *fakeStore) deleteCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.deletes)
}

// fakeReconfigurer records Raft add/remove calls and reports a fixed leader.
type fakeReconfigurer struct {
	leaderID  uint64
	hasLeader bool
	leaderErr error

	mu       sync.Mutex
	added    []uint64
	removed  []uint64
	addFails int // number of AddMember attempts to fail before succeeding
	addCh    chan uint64
	rmCh     chan uint64
}

func newFakeReconfigurer(leaderID uint64) *fakeReconfigurer {
	return &fakeReconfigurer{
		leaderID:  leaderID,
		hasLeader: true,
		addCh:     make(chan uint64, 4),
		rmCh:      make(chan uint64, 4),
	}
}

func (r *fakeReconfigurer) AddMember(_ context.Context, _, replicaID uint64, _ string) error {
	r.mu.Lock()
	if r.addFails > 0 {
		r.addFails--
		r.mu.Unlock()
		return errors.New("transient add failure")
	}
	r.added = append(r.added, replicaID)
	r.mu.Unlock()
	r.addCh <- replicaID
	return nil
}

func (r *fakeReconfigurer) RemoveMember(_ context.Context, _, replicaID uint64) error {
	r.mu.Lock()
	r.removed = append(r.removed, replicaID)
	r.mu.Unlock()
	r.rmCh <- replicaID
	return nil
}

func (r *fakeReconfigurer) Leader(_ uint64) (uint64, bool, error) {
	return r.leaderID, r.hasLeader, r.leaderErr
}

// IsMember reports membership based on the add/remove history: a replica is a
// member once AddMember succeeds and until RemoveMember succeeds.
func (r *fakeReconfigurer) IsMember(_, replicaID uint64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	member := false
	for _, id := range r.added {
		if id == replicaID {
			member = true
		}
	}
	for _, id := range r.removed {
		if id == replicaID {
			member = false
		}
	}
	return member, nil
}

func selfMember() Member {
	return Member{NodeID: 1, Address: "10.0.0.1:5000"}
}

func startManager(t *testing.T, store MembershipStore, opts ...MembershipOption) (*MembershipManager, context.CancelFunc) {
	t.Helper()
	cfg := MembershipConfig{
		LeaseTTL:          time.Second,
		KeepAliveInterval: time.Hour, // avoid keepalive noise during tests
		MinQuorum:         2,
	}
	m, err := NewMembershipManager(store, selfMember(), 100, cfg, opts...)
	if err != nil {
		t.Fatalf("NewMembershipManager: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := m.Start(ctx); err != nil {
		cancel()
		t.Fatalf("Start: %v", err)
	}
	return m, cancel
}

func TestNewMembershipManager_NilStore(t *testing.T) {
	_, err := NewMembershipManager(nil, selfMember(), 1, MembershipConfig{})
	var me *MembershipError
	if !errors.As(err, &me) {
		t.Fatalf("error = %T (%v), want *MembershipError", err, err)
	}
}

func TestMembershipManager_RegistersSelf(t *testing.T) {
	store := newFakeStore()
	m, cancel := startManager(t, store)
	defer cancel()
	defer func() { _ = m.Close() }()

	if store.putCount() != 1 {
		t.Fatalf("put count = %d, want 1 (self registration)", store.putCount())
	}
	members := m.Members()
	if len(members) != 1 || members[0].NodeID != 1 {
		t.Fatalf("members = %+v, want [self]", members)
	}
}

func TestMembershipManager_StartTwice(t *testing.T) {
	store := newFakeStore()
	m, cancel := startManager(t, store)
	defer cancel()
	defer func() { _ = m.Close() }()

	err := m.Start(context.Background())
	var me *MembershipError
	if !errors.As(err, &me) {
		t.Fatalf("second Start error = %T (%v), want *MembershipError", err, err)
	}
}

func TestMembershipManager_JoinAsLeaderAddsToRaft(t *testing.T) {
	store := newFakeStore()
	reconf := newFakeReconfigurer(1) // self (node 1) is leader
	m, cancel := startManager(t, store, WithReconfigurer(reconf))
	defer cancel()
	defer func() { _ = m.Close() }()

	store.events <- MemberEvent{Type: MemberJoined, Member: Member{NodeID: 2, Address: "10.0.0.2:5000"}}

	select {
	case id := <-reconf.addCh:
		if id != 2 {
			t.Fatalf("AddMember replicaID = %d, want 2", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for AddMember")
	}
}

func TestMembershipManager_JoinAsFollowerSkipsRaft(t *testing.T) {
	store := newFakeStore()
	reconf := newFakeReconfigurer(99) // someone else is leader
	m, cancel := startManager(t, store, WithReconfigurer(reconf))
	defer cancel()
	defer func() { _ = m.Close() }()

	store.events <- MemberEvent{Type: MemberJoined, Member: Member{NodeID: 2, Address: "10.0.0.2:5000"}}

	select {
	case <-reconf.addCh:
		t.Fatal("AddMember should not be called on a follower")
	case <-time.After(300 * time.Millisecond):
		// expected: no reconfiguration
	}
}

func TestMembershipManager_LeaveAsLeaderRemovesFromRaft(t *testing.T) {
	store := newFakeStore()
	reconf := newFakeReconfigurer(1)
	m, cancel := startManager(t, store, WithReconfigurer(reconf))
	defer cancel()
	defer func() { _ = m.Close() }()

	// Two members join so removal stays at/above MinQuorum (2).
	store.events <- MemberEvent{Type: MemberJoined, Member: Member{NodeID: 2, Address: "10.0.0.2:5000"}}
	store.events <- MemberEvent{Type: MemberJoined, Member: Member{NodeID: 3, Address: "10.0.0.3:5000"}}
	<-reconf.addCh
	<-reconf.addCh

	store.events <- MemberEvent{Type: MemberLeft, Member: Member{NodeID: 3, Address: "10.0.0.3:5000"}}

	select {
	case id := <-reconf.rmCh:
		if id != 3 {
			t.Fatalf("RemoveMember replicaID = %d, want 3", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for RemoveMember")
	}
}

func TestMembershipManager_JoinExistingMemberSkipsAdd(t *testing.T) {
	store := newFakeStore()
	reconf := newFakeReconfigurer(1)
	reconf.added = []uint64{2} // node 2 is already in the Raft group
	m, cancel := startManager(t, store, WithReconfigurer(reconf))
	defer cancel()
	defer func() { _ = m.Close() }()

	store.events <- MemberEvent{Type: MemberJoined, Member: Member{NodeID: 2, Address: "10.0.0.2:5000"}}

	select {
	case <-reconf.addCh:
		t.Fatal("AddMember should be skipped for a replica already in the group")
	case <-time.After(300 * time.Millisecond):
		// expected: idempotent skip
	}
}

func TestMembershipManager_LeaveAbsentMemberSkipsRemove(t *testing.T) {
	store := newFakeStore()
	reconf := newFakeReconfigurer(1)
	m, cancel := startManager(t, store, WithReconfigurer(reconf))
	defer cancel()
	defer func() { _ = m.Close() }()

	// Two peers join (members: self + 2 + 3) so the quorum guard would not
	// otherwise block removing node 3.
	store.events <- MemberEvent{Type: MemberJoined, Member: Member{NodeID: 2, Address: "10.0.0.2:5000"}}
	store.events <- MemberEvent{Type: MemberJoined, Member: Member{NodeID: 3, Address: "10.0.0.3:5000"}}
	<-reconf.addCh
	<-reconf.addCh

	// Mark node 3 as no longer in the Raft group: its removal must be skipped.
	reconf.mu.Lock()
	reconf.removed = append(reconf.removed, 3)
	reconf.mu.Unlock()

	store.events <- MemberEvent{Type: MemberLeft, Member: Member{NodeID: 3, Address: "10.0.0.3:5000"}}

	select {
	case <-reconf.rmCh:
		t.Fatal("RemoveMember should be skipped for a replica already absent")
	case <-time.After(300 * time.Millisecond):
		// expected: idempotent skip
	}
}

func TestMembershipManager_QuorumGuardBlocksRemoval(t *testing.T) {
	store := newFakeStore()
	reconf := newFakeReconfigurer(1)
	m, cancel := startManager(t, store, WithReconfigurer(reconf))
	defer cancel()
	defer func() { _ = m.Close() }()

	// One peer joins (members: self + node 2 = 2). Removing node 2 would
	// leave 1 member, below MinQuorum (2): the guard must block it.
	store.events <- MemberEvent{Type: MemberJoined, Member: Member{NodeID: 2, Address: "10.0.0.2:5000"}}
	<-reconf.addCh

	store.events <- MemberEvent{Type: MemberLeft, Member: Member{NodeID: 2, Address: "10.0.0.2:5000"}}

	select {
	case <-reconf.rmCh:
		t.Fatal("RemoveMember should be blocked by the quorum guard")
	case <-time.After(300 * time.Millisecond):
		// expected: removal blocked
	}
}

func TestMembershipManager_NoReconfigurerTracksOnly(t *testing.T) {
	store := newFakeStore()
	m, cancel := startManager(t, store) // no WithReconfigurer
	defer cancel()
	defer func() { _ = m.Close() }()

	store.events <- MemberEvent{Type: MemberJoined, Member: Member{NodeID: 2, Address: "10.0.0.2:5000"}}

	// Give the watch loop time to process.
	deadline := time.After(time.Second)
	for len(m.Members()) != 2 {
		select {
		case <-deadline:
			t.Fatalf("member not tracked; members = %+v", m.Members())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestMembershipManager_KeepAliveRefreshes(t *testing.T) {
	store := newFakeStore()
	cfg := MembershipConfig{LeaseTTL: time.Second, KeepAliveInterval: 15 * time.Millisecond}
	m, err := NewMembershipManager(store, selfMember(), 100, cfg)
	if err != nil {
		t.Fatalf("NewMembershipManager: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = m.Close() }()

	deadline := time.After(2 * time.Second)
	for {
		if store.putCallCount() >= 3 { // initial register + >=2 keepalives
			return
		}
		select {
		case <-deadline:
			t.Fatalf("put call count = %d, want >= 3", store.putCallCount())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestMembershipManager_ReconfigRetrySucceeds(t *testing.T) {
	store := newFakeStore()
	reconf := newFakeReconfigurer(1)
	reconf.addFails = 2 // fail twice, succeed on the third attempt
	cfg := MembershipConfig{
		LeaseTTL:          time.Second,
		KeepAliveInterval: time.Hour,
		MaxRetries:        5,
		RetryBaseDelay:    5 * time.Millisecond,
	}
	m, err := NewMembershipManager(store, selfMember(), 100, cfg, WithReconfigurer(reconf))
	if err != nil {
		t.Fatalf("NewMembershipManager: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = m.Close() }()

	store.events <- MemberEvent{Type: MemberJoined, Member: Member{NodeID: 2, Address: "10.0.0.2:5000"}}

	select {
	case id := <-reconf.addCh:
		if id != 2 {
			t.Fatalf("AddMember replicaID = %d, want 2", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for AddMember after retries")
	}
}

func TestMembershipManager_NotLeaderWhenLeaderUnknown(t *testing.T) {
	store := newFakeStore()
	reconf := newFakeReconfigurer(1)
	reconf.hasLeader = false // no leader known -> isLeader false
	m, cancel := startManager(t, store, WithReconfigurer(reconf))
	defer cancel()
	defer func() { _ = m.Close() }()

	store.events <- MemberEvent{Type: MemberJoined, Member: Member{NodeID: 2, Address: "10.0.0.2:5000"}}

	select {
	case <-reconf.addCh:
		t.Fatal("AddMember should not run when no leader is known")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestMembershipManager_StartPutError(t *testing.T) {
	store := newFakeStore()
	store.putErr = errors.New("disk full")
	m, err := NewMembershipManager(store, selfMember(), 1, MembershipConfig{})
	if err != nil {
		t.Fatalf("NewMembershipManager: %v", err)
	}
	err = m.Start(context.Background())
	var ce *MembershipCauseError
	if !errors.As(err, &ce) {
		t.Fatalf("Start error = %T (%v), want *MembershipCauseError", err, err)
	}
}

func TestMembershipManager_StartWatchError(t *testing.T) {
	store := newFakeStore()
	store.watchErr = errors.New("watch unavailable")
	m, err := NewMembershipManager(store, selfMember(), 1, MembershipConfig{})
	if err != nil {
		t.Fatalf("NewMembershipManager: %v", err)
	}
	err = m.Start(context.Background())
	var ce *MembershipCauseError
	if !errors.As(err, &ce) {
		t.Fatalf("Start error = %T (%v), want *MembershipCauseError", err, err)
	}
}

func TestMembershipManager_StartAfterClose(t *testing.T) {
	store := newFakeStore()
	m, err := NewMembershipManager(store, selfMember(), 1, MembershipConfig{})
	if err != nil {
		t.Fatalf("NewMembershipManager: %v", err)
	}
	_ = m.Close()
	err = m.Start(context.Background())
	var me *MembershipError
	if !errors.As(err, &me) {
		t.Fatalf("Start after close = %T (%v), want *MembershipError", err, err)
	}
}

func TestWithMembershipLogger(t *testing.T) {
	store := newFakeStore()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	m, err := NewMembershipManager(store, selfMember(), 1, MembershipConfig{}, WithMembershipLogger(logger))
	if err != nil {
		t.Fatalf("NewMembershipManager: %v", err)
	}
	if m.logger != logger {
		t.Fatal("WithMembershipLogger did not set the logger")
	}
	// A nil logger must be ignored, leaving the default in place.
	WithMembershipLogger(nil)(m)
	if m.logger != logger {
		t.Fatal("WithMembershipLogger(nil) overwrote the logger")
	}
}

func TestMembershipManager_SeedSnapshotAddsExistingPeer(t *testing.T) {
	store := newFakeStore()
	// A peer registered before this node starts watching: only the snapshot
	// (ListMembers) can surface it, since a keepalive emits no PUT event.
	data, _ := json.Marshal(Member{NodeID: 2, Address: "10.0.0.2:5000"})
	store.puts[2] = data

	reconf := newFakeReconfigurer(1)
	m, cancel := startManager(t, store, WithReconfigurer(reconf))
	defer cancel()
	defer func() { _ = m.Close() }()

	select {
	case id := <-reconf.addCh:
		if id != 2 {
			t.Fatalf("AddMember replicaID = %d, want 2", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("seed snapshot did not surface the pre-existing peer")
	}
}

// resubscribeStore hands out a fresh watch channel per WatchMembers call so a
// test can close one and observe re-subscription.
type resubscribeStore struct {
	*fakeStore
	mu    sync.Mutex
	calls int
	chans []chan MemberEvent
}

func (s *resubscribeStore) WatchMembers(_ context.Context) (<-chan MemberEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	ch := make(chan MemberEvent, 4)
	s.chans = append(s.chans, ch)
	return ch, nil
}

func (s *resubscribeStore) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func TestMembershipManager_WatchResubscribes(t *testing.T) {
	store := &resubscribeStore{fakeStore: newFakeStore()}
	cfg := MembershipConfig{LeaseTTL: time.Second, KeepAliveInterval: time.Hour, RetryBaseDelay: 5 * time.Millisecond}
	m, err := NewMembershipManager(store, selfMember(), 100, cfg)
	if err != nil {
		t.Fatalf("NewMembershipManager: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = m.Close() }()

	// Close the first watch channel to simulate a stream error; the manager
	// must re-subscribe.
	store.mu.Lock()
	first := store.chans[0]
	store.mu.Unlock()
	close(first)

	deadline := time.After(2 * time.Second)
	for {
		if store.callCount() >= 2 {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("watch did not re-subscribe; WatchMembers calls = %d", store.callCount())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestMembershipManager_ReconcileRemovesDeparted(t *testing.T) {
	store := &resubscribeStore{fakeStore: newFakeStore()}
	reconf := newFakeReconfigurer(1)
	cfg := MembershipConfig{LeaseTTL: time.Second, KeepAliveInterval: time.Hour, RetryBaseDelay: 5 * time.Millisecond, MinQuorum: 1}
	m, err := NewMembershipManager(store, selfMember(), 100, cfg, WithReconfigurer(reconf))
	if err != nil {
		t.Fatalf("NewMembershipManager: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = m.Close() }()

	// Node 2 joins via the watch (added to the Raft group, tracked locally) —
	// but it is never written to the store snapshot.
	store.mu.Lock()
	first := store.chans[0]
	store.mu.Unlock()
	first <- MemberEvent{Type: MemberJoined, Member: Member{NodeID: 2, Address: "10.0.0.2:5000"}}
	if id := <-reconf.addCh; id != 2 {
		t.Fatalf("AddMember = %d, want 2", id)
	}

	// Closing the watch forces a re-subscribe + reconcile; the snapshot
	// (self only) lacks node 2, so the manager must remove the departed node.
	close(first)
	select {
	case id := <-reconf.rmCh:
		if id != 2 {
			t.Fatalf("RemoveMember = %d, want 2", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reconcile did not remove the departed member")
	}
}

func TestMembershipManager_StartRollbackAllowsRetry(t *testing.T) {
	store := newFakeStore()
	store.watchErr = errors.New("watch unavailable")
	m, err := NewMembershipManager(store, selfMember(), 100, MembershipConfig{LeaseTTL: time.Second, KeepAliveInterval: time.Hour})
	if err != nil {
		t.Fatalf("NewMembershipManager: %v", err)
	}

	// First Start fails on the watch subscription.
	if err := m.Start(context.Background()); err == nil {
		t.Fatal("expected Start to fail when WatchMembers errors")
	}
	// Rollback should have deregistered self.
	if store.deleteCount() == 0 {
		t.Fatal("failed Start should deregister self")
	}

	// started must have been reset, so a retry (now succeeding) works.
	store.watchErr = nil
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("retry Start after rollback: %v", err)
	}
	_ = m.Close()
}

func TestMembershipManager_CloseDeregisters(t *testing.T) {
	store := newFakeStore()
	m, cancel := startManager(t, store)
	defer cancel()

	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if store.deleteCount() != 1 {
		t.Fatalf("delete count = %d, want 1", store.deleteCount())
	}
	// Close is idempotent.
	if err := m.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
