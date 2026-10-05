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

package featuregate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
)

const (
	systemShard    = 1
	dedicatedShard = 70
)

// fakeMembers is a MembershipSource whose groups a test edits.
type fakeMembers struct {
	mu     sync.Mutex
	groups map[uint64]GroupMembership
	err    error
	reads  int
}

func (f *fakeMembers) GroupMembership(shardID uint64) (GroupMembership, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.err != nil {
		return GroupMembership{}, f.err
	}
	g, ok := f.groups[shardID]
	if !ok {
		return GroupMembership{}, errors.New("shard not ready")
	}
	return g, nil
}

func (f *fakeMembers) set(shardID, ccid uint64, replicas map[uint64]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.groups[shardID] = GroupMembership{ConfigChangeID: ccid, Replicas: replicas}
}

func (f *fakeMembers) setWitnesses(shardID uint64, witnesses map[uint64]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := f.groups[shardID]
	g.Witnesses = witnesses
	f.groups[shardID] = g
}

func (f *fakeMembers) readCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}

// leaseStore is a cluster.MembershipStore whose entries live only while
// their lease does, the way the lease-backed system store behaves: expire
// deletes the entry and tells every watcher, exactly as a lease that a
// stopped or rolled-back node no longer refreshes does.
type leaseStore struct {
	mu       sync.Mutex
	entries  map[uint64][]byte
	watchers []chan cluster.MemberEvent
}

func newLeaseStore() *leaseStore { return &leaseStore{entries: map[uint64][]byte{}} }

func (s *leaseStore) PutMember(_ context.Context, nodeID uint64, data []byte, _ time.Duration) error {
	var m cluster.Member
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[nodeID] = append([]byte(nil), data...)
	for _, w := range s.watchers {
		w <- cluster.MemberEvent{Type: cluster.MemberJoined, Member: m}
	}
	return nil
}

func (s *leaseStore) DeleteMember(_ context.Context, nodeID uint64) error {
	s.expire(nodeID)
	return nil
}

func (s *leaseStore) expire(nodeID uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.entries[nodeID]; !ok {
		return
	}
	delete(s.entries, nodeID)
	for _, w := range s.watchers {
		w <- cluster.MemberEvent{Type: cluster.MemberLeft, Member: cluster.Member{NodeID: nodeID}}
	}
}

func (s *leaseStore) WatchMembers(ctx context.Context) (<-chan cluster.MemberEvent, error) {
	ch := make(chan cluster.MemberEvent, 64)
	s.mu.Lock()
	s.watchers = append(s.watchers, ch)
	s.mu.Unlock()
	return ch, nil
}

func (s *leaseStore) ListMembers(context.Context) ([]cluster.Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]cluster.Member, 0, len(s.entries))
	for _, data := range s.entries {
		var m cluster.Member
		if json.Unmarshal(data, &m) == nil {
			out = append(out, m)
		}
	}
	return out, nil
}

// advertise registers a node through the real cluster.Advertiser, as a
// running node does.
func (s *leaseStore) advertise(t *testing.T, id uint64, addr string, features ...string) {
	t.Helper()
	a, err := cluster.NewAdvertiser(s, cluster.Member{NodeID: id, Address: addr, Features: features}, cluster.AdvertiseConfig{})
	require.NoError(t, err)
	require.NoError(t, a.Register(context.Background()))
}

var _ cluster.MembershipStore = (*leaseStore)(nil)

// memKV is the replicated system KV the activations live in.
type memKV struct {
	mu      sync.Mutex
	data    map[string][]byte
	listErr error
	putErr  error
}

func newMemKV() *memKV { return &memKV{data: map[string][]byte{}} }

func (k *memKV) PutIfAbsent(_ context.Context, key string, value []byte) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.putErr != nil {
		return false, k.putErr
	}
	if _, ok := k.data[key]; ok {
		return false, nil
	}
	k.data[key] = value
	return true, nil
}

func (k *memKV) ListAll(_ context.Context, prefix string) ([]string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.listErr != nil {
		return nil, k.listErr
	}
	var keys []string
	for key := range k.data {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func (k *memKV) has(key string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	_, ok := k.data[key]
	return ok
}

var _ ActivationKV = (*memKV)(nil)

// threeNodes is a cluster of three nodes, the local one being node 1: its
// system shard, and a dedicated shard placed on nodes 1 and 2 under replica
// IDs of its own.
func threeNodes() *fakeMembers {
	m := &fakeMembers{groups: map[uint64]GroupMembership{}}
	m.set(systemShard, 3, map[uint64]string{1: "n1:7000", 2: "n2:7000", 3: "n3:7000"})
	m.set(dedicatedShard, 5, map[uint64]string{101: "n1:7000", 102: "n2:7000"})
	return m
}

func newGate(t *testing.T, members MembershipSource) *Gate {
	t.Helper()
	g, err := New(Config{
		Self:            Self{ReplicaID: 1, Address: "n1:7000"},
		SystemShardID:   systemShard,
		Members:         members,
		RefreshInterval: time.Hour,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = g.Close() })
	return g
}

// eventually polls cond for up to two seconds.
func eventually(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A group holding a replica whose binary does not advertise the feature --
// a node still on the previous release -- never has it enabled, and the
// feature is not activated.
func TestGate_MixedGroupIsNeverEnabled(t *testing.T) {
	store, kv := newLeaseStore(), newMemKV()
	store.advertise(t, 1, "n1:7000", SupportedNames()...)
	store.advertise(t, 2, "n2:7000", SupportedNames()...)
	store.advertise(t, 3, "n3:7000") // the previous release: nothing advertised
	g := newGate(t, threeNodes())
	require.NoError(t, g.Start(context.Background(), kv, store))

	assert.False(t, g.Enabled(systemShard, GuardedWrites))
	assert.False(t, g.Activated(GuardedWrites))
	assert.False(t, kv.has(ActivationPrefix+string(GuardedWrites)), "a mixed cluster must not activate")
}

// Once every member advertises the feature it is enabled, activated, and
// stays enabled -- activation is permanent -- even when a record lapses.
func TestGate_EveryMemberAdvertisingActivates(t *testing.T) {
	store, kv := newLeaseStore(), newMemKV()
	store.advertise(t, 2, "n2:7000", SupportedNames()...)
	g := newGate(t, threeNodes())
	require.NoError(t, g.Start(context.Background(), kv, store))
	require.False(t, g.Enabled(systemShard, GuardedWrites))

	store.advertise(t, 3, "n3:7000", SupportedNames()...)
	eventually(t, func() bool { return g.Activated(GuardedWrites) }, "the feature was not activated once every node advertised it")
	assert.True(t, g.Enabled(systemShard, GuardedWrites))
	assert.True(t, g.Enabled(dedicatedShard, GuardedWrites))
	assert.True(t, kv.has(ActivationPrefix+string(GuardedWrites)))

	store.expire(3)
	time.Sleep(20 * time.Millisecond)
	assert.True(t, g.Enabled(systemShard, GuardedWrites), "activation is permanent")
}

// Before cluster-wide activation, Enabled answers false for every group --
// including one whose own members currently all advertise the feature --
// because Enabled is Activated and nothing else: a later membership change
// could replace a supporting replica with a rolled-back one, and go-quicraft
// would replay that group's committed log onto it. AdmitReplica's own,
// separate per-group check (decide) is what a rolled-back node's lapsed
// lease still affects: while node 2's advertisement is live, the dedicated
// shard's current members (this node, unconditionally, and node 2) all
// support the feature, so a brand-new node that advertises nothing is
// refused admission. Once node 2's lease expires, decide no longer finds
// every current member supporting the feature, so the dedicated shard stops
// requiring it of a new member at all -- the same probe now succeeds.
func TestGate_RolledBackNodeStopsCountingWhenItsLeaseExpires(t *testing.T) {
	store, kv := newLeaseStore(), newMemKV()
	store.advertise(t, 2, "n2:7000", SupportedNames()...)
	g := newGate(t, threeNodes())
	require.NoError(t, g.Start(context.Background(), kv, store))

	require.False(t, g.Enabled(dedicatedShard, GuardedWrites), "nothing is activated cluster-wide yet")
	require.False(t, g.Activated(GuardedWrites), "node 3 does not advertise, so nothing is activated")

	var refused *UnsupportedReplicaError
	require.ErrorAs(t, g.AdmitReplica(dedicatedShard, "n4:7000"), &refused,
		"every current replica of the dedicated shard advertises, so a new one that advertises nothing must be refused")

	store.expire(2) // node 2 rolled back to a binary that never re-advertises
	eventually(t, func() bool { return g.AdmitReplica(dedicatedShard, "n4:7000") == nil },
		"the dedicated shard kept requiring the feature after a member's advertisement lapsed")
}

// A binary refuses to start under a cluster that activated a feature it
// does not implement.
func TestGate_RefusesToStartUnderAnUnknownActivatedFeature(t *testing.T) {
	kv := newMemKV()
	_, err := kv.PutIfAbsent(context.Background(), ActivationPrefix+"future-opcode", []byte(`{}`))
	require.NoError(t, err)

	g := newGate(t, threeNodes())
	require.NoError(t, g.Refusal())
	err = g.Start(context.Background(), kv, newLeaseStore())
	var unsupported *UnsupportedFeatureError
	require.ErrorAs(t, err, &unsupported)
	require.ErrorAs(t, g.Refusal(), &unsupported, "the refusal is kept for health reporting")
	assert.Equal(t, Feature("future-opcode"), unsupported.Feature)
	assert.Contains(t, unsupported.Error(), "future-opcode")

	require.ErrorAs(t, CheckActivated(context.Background(), kv), &unsupported)
}

// An activated feature this binary implements is enabled from startup on,
// before any advertisement is read.
func TestGate_StartAdoptsAnActivatedFeature(t *testing.T) {
	kv := newMemKV()
	_, err := kv.PutIfAbsent(context.Background(), ActivationPrefix+string(GuardedWrites), []byte(`{}`))
	require.NoError(t, err)
	require.NoError(t, CheckActivated(context.Background(), kv))

	g := newGate(t, threeNodes())
	require.NoError(t, g.Start(context.Background(), kv, newLeaseStore()))
	assert.True(t, g.Enabled(systemShard, GuardedWrites))
}

// A record counts only while its (replica ID, address) is a current
// system-shard member: one naming an address its replica does not hold --
// a replaced node's leftover, or another node reusing the address -- does
// not.
func TestGate_RecordMustMatchASystemMember(t *testing.T) {
	store, kv := newLeaseStore(), newMemKV()
	store.advertise(t, 2, "elsewhere:7000", SupportedNames()...)
	store.advertise(t, 3, "n3:7000", SupportedNames()...)
	g := newGate(t, threeNodes())
	require.NoError(t, g.Start(context.Background(), kv, store))
	assert.False(t, g.Enabled(systemShard, GuardedWrites))
	assert.False(t, g.Enabled(dedicatedShard, GuardedWrites))
}

// A group whose only replica is the local node -- a single-node cluster --
// is enabled and activated at once.
func TestGate_SingleNodeActivatesImmediately(t *testing.T) {
	m := &fakeMembers{groups: map[uint64]GroupMembership{}}
	m.set(systemShard, 1, map[uint64]string{1: "n1:7000"})
	g := newGate(t, m)
	assert.False(t, g.Enabled(systemShard, GuardedWrites), "nothing is activated before Start runs")
	kv := newMemKV()
	require.NoError(t, g.Start(context.Background(), kv, newLeaseStore()))
	assert.True(t, g.Activated(GuardedWrites))
	assert.True(t, g.Enabled(systemShard, GuardedWrites), "Enabled follows Activated once Start runs it")
}

// Enabled reads no membership at all -- it is one atomic load of the
// cluster-wide activation flag. AdmitReplica's per-group decide check is the
// one that reads membership, and it reads it fresh on every call, so a
// membership change reaches it immediately, with no refresh delay needed.
func TestGate_AdmitReplicaReadsMembershipFresh(t *testing.T) {
	store, kv := newLeaseStore(), newMemKV()
	store.advertise(t, 2, "n2:7000", SupportedNames()...)
	m := threeNodes()
	g := newGate(t, m)
	require.NoError(t, g.Start(context.Background(), kv, store))
	require.False(t, g.Enabled(dedicatedShard, GuardedWrites), "nothing is activated cluster-wide yet")

	before := m.readCount()
	for range 1000 {
		g.Enabled(dedicatedShard, GuardedWrites)
	}
	assert.Equal(t, before, m.readCount(), "Enabled must not read membership at all")

	var refused *UnsupportedReplicaError
	require.ErrorAs(t, g.AdmitReplica(dedicatedShard, "n4:7000"), &refused,
		"every current replica of the dedicated shard advertises, so a new one must too")

	m.set(dedicatedShard, 6, map[uint64]string{101: "n1:7000", 102: "n2:7000", 103: "n3:7000"})
	assert.NoError(t, g.AdmitReplica(dedicatedShard, "n4:7000"),
		"a replica on a non-advertising node must stop the group from requiring the feature of new members, visible on the very next call")
}

// A replica is admitted to a group only on a node that advertises every
// feature the group uses: before activation, what decide finds the group's
// current members already supporting.
func TestGate_AdmitReplicaRefusesANodeMissingAnEnabledFeature(t *testing.T) {
	store, kv := newLeaseStore(), newMemKV()
	store.advertise(t, 2, "n2:7000", SupportedNames()...)
	store.advertise(t, 3, "n3:7000") // the previous release
	g := newGate(t, threeNodes())
	require.NoError(t, g.Start(context.Background(), kv, store))
	require.False(t, g.Enabled(dedicatedShard, GuardedWrites), "nothing is activated cluster-wide yet")

	var refused *UnsupportedReplicaError
	err := g.AdmitReplica(dedicatedShard, "n3:7000")
	require.ErrorAs(t, err, &refused)
	assert.Equal(t, UnsupportedReplicaError{ShardID: dedicatedShard, Address: "n3:7000", Feature: GuardedWrites}, *refused)
	assert.Contains(t, refused.Error(), "n3:7000")
	assert.NoError(t, g.AdmitReplica(dedicatedShard, "n2:7000"))
	assert.NoError(t, g.AdmitReplica(dedicatedShard, "n1:7000"), "this node supports its own features")

	// The system shard is mixed, so it uses no feature and admits anyone.
	require.False(t, g.Enabled(systemShard, GuardedWrites))
	assert.NoError(t, g.AdmitReplica(systemShard, "n3:7000"))
}

// After activation every group uses the feature, so a new node is admitted
// only once its own lease-backed record -- written before it is a member --
// advertises it.
func TestGate_AdmitReplicaAfterActivationNeedsTheNodesOwnAdvertisement(t *testing.T) {
	store, kv := newLeaseStore(), newMemKV()
	store.advertise(t, 2, "n2:7000", SupportedNames()...)
	store.advertise(t, 3, "n3:7000", SupportedNames()...)
	g := newGate(t, threeNodes())
	require.NoError(t, g.Start(context.Background(), kv, store))
	eventually(t, func() bool { return g.Activated(GuardedWrites) }, "not activated")

	var refused *UnsupportedReplicaError
	require.ErrorAs(t, g.AdmitReplica(900, "n4:7000"), &refused, "a node that advertises nothing")
	require.False(t, g.Supports("n4:7000", GuardedWrites))

	store.advertise(t, 4, "n4:7000", SupportedNames()...)
	eventually(t, func() bool { return g.AdmitReplica(900, "n4:7000") == nil }, "the advertising node was never admitted")
	assert.NoError(t, g.AdmitReplica(systemShard, "n4:7000"))

	store.expire(4)
	eventually(t, func() bool { return g.AdmitReplica(systemShard, "n4:7000") != nil },
		"a node whose advertisement lapsed was still admitted")
}

// A member registration that carries features -- dynamic membership's --
// admits the node as its feature advertisement would.
func TestGate_MemberRegistrationsAdmitTheNodesTheyDescribe(t *testing.T) {
	adverts, registrations, kv := newLeaseStore(), newLeaseStore(), newMemKV()
	adverts.advertise(t, 2, "n2:7000", SupportedNames()...)
	adverts.advertise(t, 3, "n3:7000", SupportedNames()...)
	g := newGate(t, threeNodes())
	require.NoError(t, g.Start(context.Background(), kv, adverts, registrations))
	eventually(t, func() bool { return g.Activated(GuardedWrites) }, "not activated")

	registrations.advertise(t, 4, "n4:7000") // registered by an older binary: no features
	time.Sleep(20 * time.Millisecond)
	require.Error(t, g.AdmitReplica(systemShard, "n4:7000"))
	registrations.advertise(t, 5, "n5:7000", SupportedNames()...)
	eventually(t, func() bool { return g.AdmitReplica(systemShard, "n5:7000") == nil },
		"a registration carrying the features did not admit its node")
}

// Witnesses apply nothing, so they do not decide whether a group uses a
// feature; activation enables it everywhere, so it waits for them too.
func TestGate_ActivationCountsWitnesses(t *testing.T) {
	store, kv := newLeaseStore(), newMemKV()
	store.advertise(t, 2, "n2:7000", SupportedNames()...)
	store.advertise(t, 3, "n3:7000", SupportedNames()...)
	m := threeNodes()
	m.setWitnesses(systemShard, map[uint64]string{4: "n4:7000"})
	g := newGate(t, m)
	require.NoError(t, g.Start(context.Background(), kv, store))
	assert.False(t, g.Enabled(systemShard, GuardedWrites), "nothing is activated cluster-wide yet")
	assert.False(t, g.Activated(GuardedWrites), "a witness on an older node must hold activation back")

	store.advertise(t, 4, "n4:7000", SupportedNames()...)
	eventually(t, func() bool { return g.Activated(GuardedWrites) }, "the witness advertised and activation still did not happen")
	assert.True(t, g.Enabled(systemShard, GuardedWrites), "Enabled follows Activated once the witness catches up")
}

// Anything the gate cannot decide is reported as not enabled.
func TestGate_UndecidableIsNotEnabled(t *testing.T) {
	m := threeNodes()
	g := newGate(t, m)
	assert.False(t, g.Enabled(systemShard, Feature("unknown")))
	assert.False(t, g.Enabled(999, GuardedWrites), "an unknown shard")
	m.set(dedicatedShard, 9, map[uint64]string{})
	assert.False(t, g.Enabled(dedicatedShard, GuardedWrites), "an empty group")
	m.err = errors.New("closed")
	assert.False(t, g.Enabled(systemShard, GuardedWrites))
}

// Activation that cannot be recorded is retried rather than assumed.
func TestGate_ActivationWriteFailureIsNotActivation(t *testing.T) {
	m := &fakeMembers{groups: map[uint64]GroupMembership{}}
	m.set(systemShard, 1, map[uint64]string{1: "n1:7000"})
	kv := newMemKV()
	kv.putErr = errors.New("not leader")
	g := newGate(t, m)
	require.NoError(t, g.Start(context.Background(), kv, newLeaseStore()))
	assert.False(t, g.Activated(GuardedWrites))
}

// Start reports a store it cannot read, and refuses misuse.
func TestGate_StartFailures(t *testing.T) {
	g := newGate(t, threeNodes())
	var cfgErr *ConfigError
	require.ErrorAs(t, g.Start(context.Background(), newMemKV(), nil), &cfgErr)
	assert.Contains(t, cfgErr.Error(), "store")

	kv := newMemKV()
	kv.listErr = errors.New("unavailable")
	var storeErr *StoreError
	require.ErrorAs(t, g.Start(context.Background(), kv, newLeaseStore()), &storeErr)
	assert.ErrorIs(t, storeErr, kv.listErr)
	assert.Contains(t, storeErr.Error(), "list activations")

	require.NoError(t, g.Start(context.Background(), newMemKV(), newLeaseStore()))
	require.ErrorAs(t, g.Start(context.Background(), newMemKV(), newLeaseStore()), &cfgErr)
	require.NoError(t, g.Close())
	require.NoError(t, g.Close())
}

// New refuses an incomplete configuration.
func TestNew_Validates(t *testing.T) {
	var cfgErr *ConfigError
	_, err := New(Config{Self: Self{ReplicaID: 1, Address: "a"}})
	require.ErrorAs(t, err, &cfgErr)
	_, err = New(Config{Members: threeNodes(), Self: Self{Address: "a"}})
	require.ErrorAs(t, err, &cfgErr)
}

// The advertised names are exactly this binary's features.
func TestSupported(t *testing.T) {
	assert.Equal(t, []Feature{GuardedWrites, ClusterShardIDs, PrefixCompaction, NodeCompaction, ClusterSeal, KeyLengthCap}, Supported())
	assert.Equal(t, []string{string(GuardedWrites), string(ClusterShardIDs), string(PrefixCompaction), string(NodeCompaction), string(ClusterSeal), string(KeyLengthCap)}, SupportedNames())
}

// fakeReader is a quicraft membership reader.
type fakeReader struct {
	ms  *quicraft.Membership
	err error
}

func (r fakeReader) GetShardMembership(uint64) (*quicraft.Membership, error) { return r.ms, r.err }

// HostMembership counts voters and observers, which apply the log, and not
// witnesses, which keep no state machine.
func TestHostMembership_CountsReplicasThatApplyTheLog(t *testing.T) {
	src := HostMembership(fakeReader{ms: &quicraft.Membership{
		ConfigChangeID: 12,
		Nodes:          map[uint64]string{1: "a", 2: "b"},
		Observers:      map[uint64]string{3: "c"},
		Witnesses:      map[uint64]string{4: "d"},
	}})
	got, err := src.GroupMembership(1)
	require.NoError(t, err)
	assert.Equal(t, uint64(12), got.ConfigChangeID)
	assert.Equal(t, map[uint64]string{1: "a", 2: "b", 3: "c"}, got.Replicas)
	assert.Equal(t, map[uint64]string{4: "d"}, got.Witnesses)

	_, err = HostMembership(fakeReader{err: errors.New("not ready")}).GroupMembership(1)
	assert.Error(t, err)
}

// The advertised address is the one the system shard committed for the
// local node, whatever address it was configured to listen on.
func TestGate_AdvertisedSelf(t *testing.T) {
	m := threeNodes()
	g := newGate(t, m)
	self, err := g.AdvertisedSelf()
	require.NoError(t, err)
	assert.Equal(t, Self{ReplicaID: 1, Address: "n1:7000"}, self)

	m.set(systemShard, 4, map[uint64]string{2: "n2:7000"})
	var notMember *NotAMemberError
	_, err = g.AdvertisedSelf()
	require.ErrorAs(t, err, &notMember)
	assert.Contains(t, notMember.Error(), "replica 1")

	m.err = errors.New("closed")
	_, err = g.AdvertisedSelf()
	assert.Error(t, err)
}

// noWatchStore is a lease store whose watch is unavailable, so the gate
// learns of advertisements only through its periodic refresh.
type noWatchStore struct{ *leaseStore }

func (noWatchStore) WatchMembers(context.Context) (<-chan cluster.MemberEvent, error) {
	return nil, errors.New("watch unavailable")
}

// Without a watch the background refresh still follows advertisements, and
// activation follows them.
func TestGate_RefreshFollowsAdvertisementsWithoutAWatch(t *testing.T) {
	store, kv := noWatchStore{newLeaseStore()}, newMemKV()
	store.advertise(t, 2, "n2:7000", SupportedNames()...)
	g, err := New(Config{
		Self:            Self{ReplicaID: 1},
		SystemShardID:   systemShard,
		Members:         threeNodes(),
		RefreshInterval: 10 * time.Millisecond,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = g.Close() })
	require.NoError(t, g.Start(context.Background(), kv, store))
	require.False(t, g.Activated(GuardedWrites))

	store.advertise(t, 3, "n3:7000", SupportedNames()...)
	eventually(t, func() bool { return g.Activated(GuardedWrites) }, "the refresh did not pick up the new advertisement")
}

// Another node's activation is adopted on refresh.
func TestGate_RefreshAdoptsAnotherNodesActivation(t *testing.T) {
	kv := newMemKV()
	g, err := New(Config{
		Self:            Self{ReplicaID: 1},
		SystemShardID:   systemShard,
		Members:         threeNodes(),
		RefreshInterval: 10 * time.Millisecond,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = g.Close() })
	require.NoError(t, g.Start(context.Background(), kv, newLeaseStore()))
	require.False(t, g.Enabled(systemShard, GuardedWrites))

	_, err = kv.PutIfAbsent(context.Background(), ActivationPrefix+string(GuardedWrites), []byte(`{}`))
	require.NoError(t, err)
	eventually(t, func() bool { return g.Enabled(systemShard, GuardedWrites) }, "another node's activation was not adopted")
}

// failingListStore cannot list advertisements.
type failingListStore struct{ *leaseStore }

func (failingListStore) ListMembers(context.Context) ([]cluster.Member, error) {
	return nil, errors.New("unavailable")
}

// Start fails, rather than running blind, when advertisements cannot be read.
func TestGate_StartFailsWhenAdvertisementsCannotBeRead(t *testing.T) {
	g := newGate(t, threeNodes())
	var storeErr *StoreError
	require.ErrorAs(t, g.Start(context.Background(), newMemKV(), failingListStore{newLeaseStore()}), &storeErr)
	assert.Equal(t, "list advertisements", storeErr.Op)
}

// laterActivationKV is a system store another node activated the feature in
// after this node read the activations: the read shows none, and the claim
// finds the key already there.
type laterActivationKV struct{ *memKV }

func (laterActivationKV) ListAll(context.Context, string) ([]string, error) { return nil, nil }

// Only the node whose claim created the activation record reports the
// activation; a node that finds it already there reports observing it.
func TestGate_ActivationIsLoggedOnlyByTheNodeThatRecordsIt(t *testing.T) {
	logs := func(kv ActivationKV) string {
		var buf bytes.Buffer
		m := &fakeMembers{groups: map[uint64]GroupMembership{}}
		m.set(systemShard, 1, map[uint64]string{1: "n1:7000"})
		g, err := New(Config{Self: Self{ReplicaID: 1}, SystemShardID: systemShard, Members: m,
			RefreshInterval: time.Hour, Logger: slog.New(slog.NewTextHandler(&buf, nil))})
		require.NoError(t, err)
		t.Cleanup(func() { _ = g.Close() })
		require.NoError(t, g.Start(context.Background(), kv, newLeaseStore()))
		require.True(t, g.Activated(GuardedWrites))
		return buf.String()
	}

	created := logs(newMemKV())
	assert.Contains(t, created, "feature activated cluster-wide")
	assert.NotContains(t, created, "activation observed")

	// Another node activated every feature this binary supports; this node
	// only observes them.
	existing := newMemKV()
	for _, f := range Supported() {
		_, err := existing.PutIfAbsent(context.Background(), ActivationPrefix+string(f), []byte(`{}`))
		require.NoError(t, err)
	}
	observed := logs(laterActivationKV{existing})
	assert.Contains(t, observed, "feature activation observed")
	assert.NotContains(t, observed, "activated cluster-wide")
}

// TestGate_ActivationsLoaded reports whether the gate has read the cluster's
// activations: not before, so a caller cannot mistake "not read yet" for "not
// activated"; yes after a successful read; and not after a read that found a
// feature this binary lacks, since this node must not serve at all then.
func TestGate_ActivationsLoaded(t *testing.T) {
	g := newGate(t, threeNodes())
	assert.False(t, g.ActivationsLoaded(), "loaded before any read")
	require.NoError(t, g.CheckActivated(context.Background(), newMemKV()))
	assert.True(t, g.ActivationsLoaded(), "not loaded after a successful read")

	refused := newGate(t, threeNodes())
	kv := newMemKV()
	_, err := kv.PutIfAbsent(context.Background(), ActivationPrefix+"feature-from-the-future", []byte(`{}`))
	require.NoError(t, err)
	var unsupported *UnsupportedFeatureError
	require.ErrorAs(t, refused.CheckActivated(context.Background(), kv), &unsupported)
	assert.False(t, refused.ActivationsLoaded(), "loaded after a refused read")
}
