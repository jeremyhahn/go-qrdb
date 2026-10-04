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

package replicaforward

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
)

// members is a MemberSource that counts its reads.
type members struct {
	mu    sync.Mutex
	list  []cluster.Member
	reads int
	err   error
}

func (m *members) ListMembers(context.Context) ([]cluster.Member, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads++
	return m.list, m.err
}

func (m *members) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reads
}

func member(id uint64, addr, grpc string) cluster.Member {
	return cluster.Member{NodeID: id, Address: addr, APIEndpoints: map[cluster.APIProtocol]string{cluster.APIProtocolGRPC: grpc}}
}

// threeNodes: node A (leader hint, replica 1), node B (replica 2), node C (self,
// replica 3 is not in the placement -- C holds no replica).
func threeNodes() (*members, *placement.NotAReplicaError) {
	ms := &members{list: []cluster.Member{
		member(1, "a:1", "a:grpc"),
		member(2, "b:1", "b:grpc"),
		member(3, "c:1", "c:grpc"),
	}}
	refusal := &placement.NotAReplicaError{
		TenantID: "acme", Database: "orders", ShardID: 1000, NodeID: "c:1", LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "a:1", RaftAddr: "a:1"},
			{ReplicaID: 2, NodeID: "b:1", RaftAddr: "b:1"},
		},
	}
	return ms, refusal
}

// sender records the targets it was sent to and answers from a script.
type sender struct {
	sent    []Target
	answers map[string]Result
	errs    map[string]error
}

func (s *sender) send(_ context.Context, t Target) (Result, error) {
	s.sent = append(s.sent, t)
	if err, ok := s.errs[t.RaftAddr]; ok {
		return Result{}, err
	}
	if r, ok := s.answers[t.RaftAddr]; ok {
		return r, nil
	}
	return Result{Outcome: Served}, nil
}

func TestForward_LeaderHintFirstThenCacheHitSkipsTheMemberRead(t *testing.T) {
	ms, refusal := threeNodes()
	l := NewLocator(ms, NewLeaderCache(), "c:1", nil)
	s := &sender{}

	got, err := l.Forward(context.Background(), refusal, cluster.APIProtocolGRPC, s.send)
	if err != nil || got.RaftAddr != "a:1" || got.Endpoint != "a:grpc" || got.ReplicaID != 1 {
		t.Fatalf("Forward = %+v, %v; want the leader hint a:1 over its gRPC endpoint", got, err)
	}
	if ms.count() != 1 {
		t.Fatalf("member reads = %d; want 1 on a miss", ms.count())
	}
	if c, ok := l.Cache().Get(1000); !ok || c.RaftAddr != "a:1" || c.TenantID != "acme" {
		t.Fatalf("cache = %+v, %v; want the served leader cached for the shard", c, ok)
	}

	if _, err := l.Forward(context.Background(), refusal, cluster.APIProtocolGRPC, s.send); err != nil {
		t.Fatal(err)
	}
	if ms.count() != 1 {
		t.Fatalf("member reads = %d after a cache hit; want the hit to skip resolution", ms.count())
	}
	if len(s.sent) != 2 || s.sent[1].RaftAddr != "a:1" {
		t.Fatalf("sent = %+v; want the hit forwarded to the cached leader", s.sent)
	}
}

func TestForward_NotLeaderUpdatesTheEntryToTheNamedLeader(t *testing.T) {
	ms, refusal := threeNodes()
	l := NewLocator(ms, NewLeaderCache(), "c:1", nil)
	s := &sender{answers: map[string]Result{"a:1": {Outcome: NotLeader, LeaderRaftAddr: "b:1"}}}

	got, err := l.Forward(context.Background(), refusal, cluster.APIProtocolGRPC, s.send)
	if err != nil || got.RaftAddr != "b:1" || got.ReplicaID != 2 {
		t.Fatalf("Forward = %+v, %v; want the retry to reach the named leader b:1", got, err)
	}
	if c, _ := l.Cache().Get(1000); c.RaftAddr != "b:1" {
		t.Fatalf("cache = %+v; want it updated to the new leader", c)
	}
	if len(s.sent) != 2 {
		t.Fatalf("sent %d times; want exactly one retry", len(s.sent))
	}
}

func TestForward_TransportErrorEvictsAndDoesNotRetry(t *testing.T) {
	ms, refusal := threeNodes()
	cache := NewLeaderCache()
	cache.Put(1000, CachedLeader{TenantID: "acme", Database: "orders", ReplicaID: 1, RaftAddr: "a:1",
		Endpoints: map[cluster.APIProtocol]string{cluster.APIProtocolGRPC: "a:grpc"}})
	l := NewLocator(ms, cache, "c:1", nil)
	boom := errors.New("stream reset after send")
	s := &sender{errs: map[string]error{"a:1": boom}}

	if _, err := l.Forward(context.Background(), refusal, cluster.APIProtocolGRPC, s.send); !errors.Is(err, boom) {
		t.Fatalf("Forward = %v; want the transport error", err)
	}
	if _, ok := cache.Get(1000); ok {
		t.Fatal("a transport error left the entry in the cache")
	}
	if len(s.sent) != 1 {
		t.Fatalf("sent %d times; a failure after send must never be retried", len(s.sent))
	}
}

func TestForward_UnreachableRetriesTheNextReplicaOnce(t *testing.T) {
	ms, refusal := threeNodes()
	l := NewLocator(ms, NewLeaderCache(), "c:1", nil)
	s := &sender{answers: map[string]Result{"a:1": {Outcome: Unreachable}}}

	got, err := l.Forward(context.Background(), refusal, cluster.APIProtocolGRPC, s.send)
	if err != nil || got.RaftAddr != "b:1" {
		t.Fatalf("Forward = %+v, %v; want the next replica b:1", got, err)
	}
}

func TestForward_BothRefusedIsNoTargetServed(t *testing.T) {
	ms, refusal := threeNodes()
	l := NewLocator(ms, NewLeaderCache(), "c:1", nil)
	s := &sender{answers: map[string]Result{"a:1": {Outcome: NotAReplica}, "b:1": {Outcome: NotLeader}}}

	_, err := l.Forward(context.Background(), refusal, cluster.APIProtocolGRPC, s.send)
	var none *NoTargetServedError
	if !errors.As(err, &none) || none.Error() == "" {
		t.Fatalf("Forward = %v; want *NoTargetServedError", err)
	}
	if len(s.sent) != 2 {
		t.Fatalf("sent %d times; want at most two attempts", len(s.sent))
	}
	if _, ok := l.Cache().Get(1000); ok {
		t.Fatal("a refused forward left an entry in the cache")
	}
}

func TestForward_NoMemberAdvertisesTheProtocol(t *testing.T) {
	ms, refusal := threeNodes()
	l := NewLocator(ms, NewLeaderCache(), "c:1", nil)
	_, err := l.Forward(context.Background(), refusal, cluster.APIProtocolQUIC, (&sender{}).send)
	var none *NoTargetServedError
	if !errors.As(err, &none) || none.Protocol != cluster.APIProtocolQUIC {
		t.Fatalf("Forward over an unadvertised protocol = %v; want *NoTargetServedError", err)
	}
}

func TestForward_NeverTargetsItself(t *testing.T) {
	ms, refusal := threeNodes()
	l := NewLocator(ms, NewLeaderCache(), "a:1", nil) // self is the leader hint's node
	s := &sender{}
	got, err := l.Forward(context.Background(), refusal, cluster.APIProtocolGRPC, s.send)
	if err != nil || got.RaftAddr != "b:1" {
		t.Fatalf("Forward = %+v, %v; want b:1, never self", got, err)
	}
}

func TestForward_MemberReadFailure(t *testing.T) {
	ms, refusal := threeNodes()
	ms.err = errors.New("store down")
	l := NewLocator(ms, NewLeaderCache(), "c:1", nil)
	_, err := l.Forward(context.Background(), refusal, cluster.APIProtocolGRPC, (&sender{}).send)
	var lookup *MemberLookupError
	if !errors.As(err, &lookup) || !errors.Is(err, ms.err) || lookup.Error() == "" {
		t.Fatalf("Forward = %v; want *MemberLookupError wrapping the read failure", err)
	}
}

func TestLeaderCache_Invalidation(t *testing.T) {
	c := NewLeaderCache()
	c.Put(1, CachedLeader{TenantID: "acme", Database: "orders", RaftAddr: "a:1"})
	c.Put(2, CachedLeader{TenantID: "acme", Database: "invoices", RaftAddr: "b:1"})
	c.Put(3, CachedLeader{TenantID: "globex", Database: "orders", RaftAddr: "a:1"})

	c.EvictDatabase("acme", "orders") // a placement change names the database
	if _, ok := c.Get(1); ok {
		t.Fatal("a placement change left its shard cached")
	}
	if c.Len() != 2 {
		t.Fatalf("Len = %d; the eviction reached another database", c.Len())
	}
	c.EvictMember("a:1") // a member record changed or expired
	if _, ok := c.Get(3); ok {
		t.Fatal("a member change left an entry pointing at it")
	}
	if _, ok := c.Get(2); !ok {
		t.Fatal("a member change evicted an entry pointing elsewhere")
	}
	c.Clear()
	if c.Len() != 0 {
		t.Fatal("Clear left entries")
	}
}
