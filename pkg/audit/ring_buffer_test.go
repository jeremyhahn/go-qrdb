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
	"testing"
	"time"
)

func TestRingBuffer_EmitAndQuery(t *testing.T) {
	r := NewRingBuffer(100)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		r.Emit(ctx, testEvent("alice", "kv", "put"))
	}
	page, err := r.Query(ctx, Filter{Limit: 20})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Events) != 10 {
		t.Fatalf("expected 10 events, got %d", len(page.Events))
	}
}

func TestRingBuffer_Overflow(t *testing.T) {
	capacity := 5
	r := NewRingBuffer(capacity)
	ctx := context.Background()

	// Emit 10 events into a capacity-5 buffer.
	for i := 0; i < 10; i++ {
		ev := testEvent("overflower", "kv", "put")
		ev.RequestID = itoa(i)
		r.Emit(ctx, ev)
	}

	snapshot := r.Snapshot()
	if len(snapshot) != capacity {
		t.Fatalf("expected %d events (capacity), got %d", capacity, len(snapshot))
	}

	// The 5 newest events should be present (requestID 5-9).
	seen := make(map[string]bool)
	for _, ev := range snapshot {
		seen[ev.RequestID] = true
	}
	for i := 5; i < 10; i++ {
		if !seen[itoa(i)] {
			t.Errorf("expected event with requestID %d to be present", i)
		}
	}
}

func TestRingBuffer_FilterByActor(t *testing.T) {
	r := NewRingBuffer(100)
	ctx := context.Background()
	r.Emit(ctx, testEvent("alice", "kv", "put"))
	r.Emit(ctx, testEvent("bob", "kv", "put"))
	r.Emit(ctx, testEvent("alice", "barrier", "unseal"))

	page, err := r.Query(ctx, Filter{Actor: "alice", Limit: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Events) != 2 {
		t.Fatalf("expected 2, got %d", len(page.Events))
	}
}

func TestRingBuffer_FilterByTimeRange(t *testing.T) {
	r := NewRingBuffer(100)
	ctx := context.Background()
	t0 := time.Now().UTC()
	for i := 0; i < 5; i++ {
		ev := testEvent("ian", "kv", "get")
		ev.Timestamp = t0.Add(time.Duration(i) * time.Second)
		r.Emit(ctx, ev)
	}
	page, err := r.Query(ctx, Filter{
		From:  t0.Add(2 * time.Second),
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Events) != 3 {
		t.Fatalf("expected 3, got %d", len(page.Events))
	}
}

func TestRingBuffer_DefaultCapacity(t *testing.T) {
	r := NewRingBuffer(0)
	if r.capacity != defaultRingCapacity {
		t.Fatalf("expected default capacity %d, got %d", defaultRingCapacity, r.capacity)
	}
}

func TestRingBuffer_ConcurrentEmit_NoRace(t *testing.T) {
	r := NewRingBuffer(1000)
	ctx := context.Background()
	const goroutines = 20
	const eventsEach = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < eventsEach; j++ {
				r.Emit(ctx, testEvent("concurrent", "kv", "put"))
			}
		}()
	}
	wg.Wait()
	// No panic; count should be <= capacity.
	snap := r.Snapshot()
	if len(snap) > 1000 {
		t.Fatalf("snapshot length %d exceeds capacity 1000", len(snap))
	}
}

func TestRingBuffer_Close(t *testing.T) {
	r := NewRingBuffer(10)
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestRingBuffer_Emit_ZeroTimestamp(t *testing.T) {
	// Emit with zero timestamp should fill in current time.
	r := NewRingBuffer(10)
	ctx := context.Background()
	ev := AuditEvent{Actor: "nots", Resource: "kv", Action: "get", Outcome: OutcomeSuccess}
	r.Emit(ctx, ev)
	snap := r.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 event, got %d", len(snap))
	}
	if snap[0].Timestamp.IsZero() {
		t.Fatal("expected non-zero timestamp after emit")
	}
}

func TestRingBuffer_EmptyQuery(t *testing.T) {
	r := NewRingBuffer(10)
	ctx := context.Background()
	page, err := r.Query(ctx, Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Events) != 0 {
		t.Fatalf("expected 0 events, got %d", len(page.Events))
	}
}
