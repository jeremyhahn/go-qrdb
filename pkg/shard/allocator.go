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

import "sync/atomic"

// Allocator assigns unique shard IDs from a designated range.
// Each allocation is monotonically increasing and thread-safe.
// The default range starts at UserMin (1000) to avoid collision
// with reserved core and system shard ID ranges.
type Allocator struct {
	next atomic.Uint64
}

// NewAllocator creates an Allocator that begins allocating from
// the user shard range (UserMin = 1000).
func NewAllocator() *Allocator {
	a := &Allocator{}
	a.next.Store(UserMin)
	return a
}

// NewAllocatorFrom creates an Allocator that starts from the given ID.
// Use this to resume allocation after a restart by passing the highest
// known shard ID + 1. Panics if start is zero.
func NewAllocatorFrom(start uint64) *Allocator {
	if start == 0 {
		panic("shard: allocator start ID must not be zero")
	}
	a := &Allocator{}
	a.next.Store(start)
	return a
}

// Allocate returns the next available shard ID and advances the
// internal counter. The returned ID is guaranteed unique within
// this Allocator instance across concurrent callers.
func (a *Allocator) Allocate() uint64 {
	return a.next.Add(1) - 1
}

// Next returns the next shard ID that will be returned by Allocate
// without consuming it. The value may be stale by the time it is
// observed in concurrent usage.
func (a *Allocator) Next() uint64 {
	return a.next.Load()
}

// AdvanceTo moves the internal counter to at least target using a
// lock-free CAS loop. If the current counter is already >= target the
// call is a no-op. This is safe to call concurrently with Allocate.
func (a *Allocator) AdvanceTo(target uint64) {
	for {
		current := a.next.Load()
		if current >= target {
			return
		}
		if a.next.CompareAndSwap(current, target) {
			return
		}
	}
}
