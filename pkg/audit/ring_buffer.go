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

package audit

import (
	"context"
	"sync"
	"time"
)

const defaultRingCapacity = 10_000

// RingBuffer is a thread-safe in-memory circular ring buffer for recent audit
// events. It holds up to capacity events; when full, the oldest event is
// overwritten. Reads take a snapshot to avoid locking the writer.
type RingBuffer struct {
	mu       sync.RWMutex
	buf      []AuditEvent
	capacity int
	head     int // index of the oldest event
	count    int // number of valid events in the buffer
}

// NewRingBuffer returns a RingBuffer with the given capacity. When capacity
// is zero or negative, defaultRingCapacity (10,000) is used.
func NewRingBuffer(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = defaultRingCapacity
	}
	return &RingBuffer{
		buf:      make([]AuditEvent, capacity),
		capacity: capacity,
	}
}

// Emit appends ev to the ring buffer and reports success. If the buffer is
// full, the oldest event is overwritten.
//
// It cannot fail: the backing array is allocated at construction, so there is
// no write to a resource that could refuse it. Overwriting the oldest event is
// the buffer's stated capacity policy, not a write failure -- a caller that
// needs the older event to survive configures a durable sink beside this one.
func (r *RingBuffer) Emit(_ context.Context, ev AuditEvent) error {
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	r.mu.Lock()
	if r.count < r.capacity {
		r.buf[(r.head+r.count)%r.capacity] = ev
		r.count++
	} else {
		// Overwrite oldest.
		r.buf[r.head] = ev
		r.head = (r.head + 1) % r.capacity
	}
	r.mu.Unlock()
	return nil
}

// Query returns a paginated Page of events matching f from the ring buffer
// snapshot. The snapshot is copied while holding the read lock; filtering and
// pagination happen without any lock.
func (r *RingBuffer) Query(_ context.Context, f Filter) (*Page, error) {
	f.Limit = normalizeLimit(f.Limit)

	r.mu.RLock()
	snapshot := r.snapshot()
	r.mu.RUnlock()

	var matched []AuditEvent
	for _, ev := range snapshot {
		if matchesFilter(ev, f) {
			matched = append(matched, ev)
		}
	}
	return buildPage(matched, f), nil
}

// Close is a no-op; the ring buffer holds no external resources.
func (r *RingBuffer) Close() error { return nil }

// Snapshot returns a copy of all events in chronological order.
// It is exported for testing.
func (r *RingBuffer) Snapshot() []AuditEvent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.snapshot()
}

// snapshot returns all events in chronological order. Must be called with
// r.mu read lock held.
func (r *RingBuffer) snapshot() []AuditEvent {
	if r.count == 0 {
		return nil
	}
	out := make([]AuditEvent, r.count)
	for i := 0; i < r.count; i++ {
		out[i] = r.buf[(r.head+i)%r.capacity]
	}
	return out
}
