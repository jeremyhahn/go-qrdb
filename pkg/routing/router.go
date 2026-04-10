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

// Package routing maps (entityType, id) pairs to shard IDs using explicit
// entity-type pinning with a deterministic modulo-hash fallback.
package routing

import "sync"

// Router maps (entityType, id) to a shard ID. It supports explicit
// entity-type pinning and a simple shard list fallback using modulo
// hashing for deterministic routing.
type Router interface {
	// Route returns the shard ID for the given entity type and id.
	Route(entityType string, id uint64) uint64

	// SetEntityShard pins an entity type to a specific shard ID.
	SetEntityShard(entityType string, shardID uint64)

	// SetShards sets the list of available shard IDs used for fallback
	// routing. When id > 0 and the entity is not pinned, a simple modulo
	// is used over the list. When id == 0, the first shard is returned.
	SetShards(shardIDs []uint64)
}

// router is the concrete Router implementation.
type router struct {
	mu        sync.RWMutex
	entityMap map[string]uint64
	shardIDs  []uint64
}

// New creates a Router with a single default shard [1].
func New() Router {
	return &router{
		entityMap: make(map[string]uint64),
		shardIDs:  []uint64{1},
	}
}

// NewWithShards creates a Router with the provided shard IDs. If shards
// is nil or empty, the router defaults to [1].
func NewWithShards(shards []uint64) Router {
	ids := defaultIfEmpty(shards)
	return &router{
		entityMap: make(map[string]uint64),
		shardIDs:  ids,
	}
}

// Route returns the shard ID for the given entity type and id. Pinned
// entity types always return their configured shard. Unpinned entities
// fall back to modulo hashing over the shard list. An id of 0 always
// returns the first shard in the list.
func (r *router) Route(entityType string, id uint64) uint64 {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if shardID, pinned := r.entityMap[entityType]; pinned {
		return shardID
	}

	if id == 0 {
		return r.shardIDs[0]
	}

	idx := id % uint64(len(r.shardIDs)) //nolint:gosec // bounded by len(shardIDs)
	return r.shardIDs[idx]
}

// SetEntityShard pins an entity type to a specific shard ID.
func (r *router) SetEntityShard(entityType string, shardID uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.entityMap[entityType] = shardID
}

// SetShards replaces the shard list used for fallback routing. If shardIDs
// is nil or empty, the list resets to the default [1].
func (r *router) SetShards(shardIDs []uint64) {
	ids := defaultIfEmpty(shardIDs)

	r.mu.Lock()
	defer r.mu.Unlock()

	r.shardIDs = ids
}

// defaultIfEmpty returns a copy of ids if non-empty, or the default [1].
func defaultIfEmpty(ids []uint64) []uint64 {
	if len(ids) == 0 {
		return []uint64{1}
	}
	out := make([]uint64, len(ids))
	copy(out, ids)
	return out
}
