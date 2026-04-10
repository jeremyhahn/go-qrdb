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

package shard

import "sync"

// Registry tracks active shards on this node. It is safe for
// concurrent use.
type Registry struct {
	mu     sync.RWMutex
	shards map[uint64]Type
}

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		shards: make(map[uint64]Type),
	}
}

// Register adds a shard to the registry with its resolved type.
// Returns InvalidShardIDError if the shard ID is zero, or
// ShardAlreadyRegisteredError if the shard is already registered.
func (r *Registry) Register(shardID uint64, shardType Type) error {
	if shardID == 0 {
		return &InvalidShardIDError{ShardID: shardID}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.shards[shardID]; exists {
		return &ShardAlreadyRegisteredError{ShardID: shardID}
	}
	r.shards[shardID] = shardType
	return nil
}

// Unregister removes a shard from the registry. Returns
// ShardNotRegisteredError if the shard is not found.
func (r *Registry) Unregister(shardID uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.shards[shardID]; !exists {
		return &ShardNotRegisteredError{ShardID: shardID}
	}
	delete(r.shards, shardID)
	return nil
}

// Get returns the type for a registered shard. Returns
// ShardNotRegisteredError if not found.
func (r *Registry) Get(shardID uint64) (Type, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, exists := r.shards[shardID]
	if !exists {
		return "", &ShardNotRegisteredError{ShardID: shardID}
	}
	return t, nil
}

// List returns all registered shard IDs and their types. The returned
// map is a copy; callers may mutate it without affecting the registry.
func (r *Registry) List() map[uint64]Type {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[uint64]Type, len(r.shards))
	for id, t := range r.shards {
		out[id] = t
	}
	return out
}

// Count returns the number of registered shards.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.shards)
}

// IsRegistered returns true if the shard is registered.
func (r *Registry) IsRegistered(shardID uint64) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, exists := r.shards[shardID]
	return exists
}
