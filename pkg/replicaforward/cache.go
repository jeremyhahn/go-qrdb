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

// Package replicaforward resolves where a request that reached a node holding
// no replica of its database is forwarded: the database's replica leader,
// located through the placement record and the leader's member record, with a
// per-shard leader cache in front. See docs/replica-forwarding.
package replicaforward

import (
	"maps"
	"sync"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
)

// CachedLeader is the per-shard leader hint: the replica the last forward
// reached and the member endpoints it was reached on. It is only ever where a
// forward goes first, never an answer.
type CachedLeader struct {
	// TenantID and Database identify the database the shard belongs to; a
	// placement event names them, not the shard.
	TenantID string
	Database string

	// ReplicaID and RaftAddr identify the replica.
	ReplicaID uint64
	RaftAddr  string

	// Endpoints are the replica's member endpoints, per protocol.
	Endpoints map[cluster.APIProtocol]string
}

// LeaderCache maps a shard ID to its CachedLeader. Reads are lock-free: the
// map is copy-on-write behind an atomic pointer, so Get never waits on a
// writer. Writes serialize on a mutex and are rare -- one per leader change,
// eviction or invalidation.
type LeaderCache struct {
	entries atomic.Pointer[map[uint64]CachedLeader]
	mu      sync.Mutex
}

// NewLeaderCache returns an empty cache.
func NewLeaderCache() *LeaderCache {
	c := &LeaderCache{}
	empty := map[uint64]CachedLeader{}
	c.entries.Store(&empty)
	return c
}

// Get returns the shard's cached leader.
func (c *LeaderCache) Get(shardID uint64) (CachedLeader, bool) {
	l, ok := (*c.entries.Load())[shardID]
	return l, ok
}

// Put records leader as the shard's leader hint.
func (c *LeaderCache) Put(shardID uint64, leader CachedLeader) {
	c.update(func(m map[uint64]CachedLeader) { m[shardID] = leader })
}

// Evict drops the shard's entry.
func (c *LeaderCache) Evict(shardID uint64) {
	c.update(func(m map[uint64]CachedLeader) { delete(m, shardID) })
}

// EvictDatabase drops the entry of the database's shard. A placement change
// names the database, not the shard.
func (c *LeaderCache) EvictDatabase(tenantID, database string) {
	c.update(func(m map[uint64]CachedLeader) {
		for id, l := range m {
			if l.TenantID == tenantID && l.Database == database {
				delete(m, id)
			}
		}
	})
}

// EvictMember drops every entry pointing at the member at raftAddr: its record
// changed or expired, so the endpoints the entry holds may be stale.
func (c *LeaderCache) EvictMember(raftAddr string) {
	c.update(func(m map[uint64]CachedLeader) {
		for id, l := range m {
			if l.RaftAddr == raftAddr {
				delete(m, id)
			}
		}
	})
}

// Clear drops every entry. A node whose invalidation feed is down clears the
// cache rather than trust entries it can no longer keep fresh.
func (c *LeaderCache) Clear() {
	c.update(func(m map[uint64]CachedLeader) { clear(m) })
}

// Len reports the number of entries.
func (c *LeaderCache) Len() int {
	return len(*c.entries.Load())
}

// update applies fn to a copy of the map and publishes the copy.
func (c *LeaderCache) update(fn func(map[uint64]CachedLeader)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	next := maps.Clone(*c.entries.Load())
	fn(next)
	c.entries.Store(&next)
}
