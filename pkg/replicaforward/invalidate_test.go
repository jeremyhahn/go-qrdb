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
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
)

// feeds hands out one channel per subscription and counts subscriptions.
type feeds struct {
	placements chan placement.Event
	members    chan cluster.MemberEvent
	placeSubs  atomic.Int32
	failFirst  bool
}

func (f *feeds) Watch(context.Context) (<-chan placement.Event, error) {
	if f.placeSubs.Add(1) == 1 && f.failFirst {
		return nil, errors.New("not leader yet")
	}
	return f.placements, nil
}

func (f *feeds) WatchMembers(context.Context) (<-chan cluster.MemberEvent, error) {
	return f.members, nil
}

func eventually(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRunInvalidation_PlacementAndMemberChangesEvict(t *testing.T) {
	cache := NewLeaderCache()
	cache.Put(1, CachedLeader{TenantID: "acme", Database: "orders", RaftAddr: "a:1"})
	cache.Put(2, CachedLeader{TenantID: "acme", Database: "invoices", RaftAddr: "b:1"})
	cache.Put(3, CachedLeader{TenantID: "acme", Database: "ledger", RaftAddr: "c:1"})
	f := &feeds{placements: make(chan placement.Event), members: make(chan cluster.MemberEvent)}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { RunInvalidation(ctx, cache, f, f, nil); close(stopped) }()

	f.placements <- placement.Event{Type: placement.EventPut, TenantID: "acme", Database: "orders"}
	eventually(t, func() bool { _, ok := cache.Get(1); return !ok }, "a placement change did not evict its shard")
	f.members <- cluster.MemberEvent{Type: cluster.MemberLeft, Member: cluster.Member{Address: "b:1"}}
	eventually(t, func() bool { _, ok := cache.Get(2); return !ok }, "an expired member left its entries cached")
	if _, ok := cache.Get(3); !ok {
		t.Fatal("an unrelated entry was evicted")
	}

	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("RunInvalidation did not stop with its context")
	}
}

func TestRunInvalidation_ClearsTheCacheWhileAFeedIsDown(t *testing.T) {
	cache := NewLeaderCache()
	cache.Put(1, CachedLeader{TenantID: "acme", Database: "orders", RaftAddr: "a:1"})
	f := &feeds{placements: make(chan placement.Event), members: make(chan cluster.MemberEvent), failFirst: true}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunInvalidation(ctx, cache, f, f, nil)

	eventually(t, func() bool { return cache.Len() == 0 }, "a feed that could not subscribe left the cache trusted")
	eventually(t, func() bool { return f.placeSubs.Load() >= 2 }, "the failed feed was never re-subscribed")

	cache.Put(1, CachedLeader{TenantID: "acme", Database: "orders", RaftAddr: "a:1"})
	close(f.placements) // the feed ends
	eventually(t, func() bool { return cache.Len() == 0 }, "a feed that ended left the cache trusted")
}
