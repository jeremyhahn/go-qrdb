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

package partition

import (
	"bytes"
	"sync"
	"sync/atomic"

	"github.com/cespare/xxhash/v2"
)

// Router determines which partition a key belongs to.
type Router interface {
	// Route returns the partition ID for the given key.
	Route(key []byte) uint32

	// PartitionCount returns the total number of partitions.
	PartitionCount() uint32
}

// HashRouter uses xxhash64 for consistent, high-performance partition routing.
// It is stateless and safe for concurrent use.
type HashRouter struct {
	partitions uint32
}

// NewHashRouter creates a new hash-based router with the given partition count.
func NewHashRouter(partitions uint32) *HashRouter {
	return &HashRouter{partitions: partitions}
}

// Route returns the partition ID for the given key using xxhash64.
func (r *HashRouter) Route(key []byte) uint32 {
	h := xxhash.Sum64(key)
	return uint32(h % uint64(r.partitions))
}

// PartitionCount returns the total number of partitions.
func (r *HashRouter) PartitionCount() uint32 {
	return r.partitions
}

// EntityRouter maps entity types (derived from key prefixes) to specific partitions.
// Uses copy-on-write via atomic.Value for lock-free reads. Writes are serialized
// by a mutex to ensure consistent copy-on-write semantics.
type EntityRouter struct {
	mappings   atomic.Value // map[string]uint32
	writeMu    sync.Mutex   // serializes copy-on-write updates
	fallback   Router
	partitions uint32
}

// NewEntityRouter creates a new entity-based router. Keys that do not match
// any mapped prefix are routed by the fallback router. The fallback router's
// partition count is used as the total partition count.
func NewEntityRouter(partitions uint32, fallback Router) *EntityRouter {
	r := &EntityRouter{
		fallback:   fallback,
		partitions: partitions,
	}
	r.mappings.Store(make(map[string]uint32))
	return r
}

// MapPrefix maps keys starting with prefix to the given partition ID.
// Uses copy-on-write for lock-free concurrent reads. Writes are serialized
// by a mutex to prevent lost updates.
func (r *EntityRouter) MapPrefix(prefix string, partitionID uint32) {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	old := r.mappings.Load().(map[string]uint32) //nolint:errcheck // type assertion on a value this container only ever holds
	next := make(map[string]uint32, len(old)+1)
	for k, v := range old {
		next[k] = v
	}
	next[prefix] = partitionID
	r.mappings.Store(next)
}

// Route returns the partition ID for the given key. It checks mapped prefixes
// first (longest prefix match) and falls back to the hash router for unmapped keys.
func (r *EntityRouter) Route(key []byte) uint32 {
	mappings := r.mappings.Load().(map[string]uint32) //nolint:errcheck // type assertion on a value this container only ever holds

	var bestPrefix string
	var bestID uint32
	found := false

	for prefix, id := range mappings {
		if bytes.HasPrefix(key, []byte(prefix)) {
			if !found || len(prefix) > len(bestPrefix) {
				bestPrefix = prefix
				bestID = id
				found = true
			}
		}
	}

	if found {
		return bestID
	}

	return r.fallback.Route(key)
}

// PartitionCount returns the total number of partitions.
func (r *EntityRouter) PartitionCount() uint32 {
	return r.partitions
}
