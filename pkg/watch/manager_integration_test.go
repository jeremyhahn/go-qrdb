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

//go:build integration
// +build integration

package watch

import (
	"sync"
	"testing"
)

// These tests drive Manager's real per-watch delivery goroutines, which is what
// makes them integration tests rather than pure unit tests. They synchronise on
// the mock stream's sent signal (waitResponses, testing_manager_wait_test.go) —
// never on elapsed time. Where a test asserts that something was NOT delivered,
// it does so by waiting for a later event that WAS: delivery for one watch is a
// single goroutine draining one channel in order, so the arrival of the second
// event proves the first was already processed and dropped. That barrier also
// makes each of these tests fail if the watch was never registered, which the
// sleeps they replaced did not.
// The mockManagerWatchStream helper is defined in testing_manager.go for use by both
// integration tests and benchmarks.

// TestWatchManagerExactMatch tests exact key matching
func TestWatchManagerExactMatch(t *testing.T) {
	mgr := NewManager()
	stream := newMockManagerWatchStream()

	// Create watch for specific key
	watch, err := mgr.Add([]byte("test-key"), nil, 0, WatchFilters{}, stream)
	if err != nil {
		t.Fatalf("Failed to add watch: %v", err)
	}

	// Notify PUT event
	mgr.NotifyPut([]byte("test-key"), nil, []byte("test-value"), 1, 1)

	// Verify event was delivered
	responses := waitResponses(t, stream, 1)
	if len(responses) != 1 {
		t.Fatalf("Expected 1 response, got %d", len(responses))
	}

	resp := responses[0]
	if resp.WatchID != int64(watch.ID) {
		t.Errorf("Expected watch ID %d, got %d", watch.ID, resp.WatchID)
	}
	if len(resp.Events) != 1 {
		t.Fatalf("Expected 1 event, got %d", len(resp.Events))
	}

	event := resp.Events[0]
	if event.Type != PUT {
		t.Errorf("Expected PUT event, got %v", event.Type)
	}
	if string(event.Kv.Key) != "test-key" {
		t.Errorf("Expected key 'test-key', got '%s'", string(event.Kv.Key))
	}
	if string(event.Kv.Value) != "test-value" {
		t.Errorf("Expected value 'test-value', got '%s'", string(event.Kv.Value))
	}
	if event.Revision != 1 {
		t.Errorf("Expected revision 1, got %d", event.Revision)
	}
}

// TestWatchManagerNoMatchDifferentKey tests that events for other keys are not delivered
func TestWatchManagerNoMatchDifferentKey(t *testing.T) {
	mgr := NewManager()
	stream := newMockManagerWatchStream()

	// Create watch for "key-a"
	_, err := mgr.Add([]byte("key-a"), nil, 0, WatchFilters{}, stream)
	if err != nil {
		t.Fatalf("Failed to add watch: %v", err)
	}

	// Notify PUT event for "key-b" (should not match)
	mgr.NotifyPut([]byte("key-b"), nil, []byte("value-b"), 1, 1)

	// Barrier: a matching event issued after the non-matching one. The watch has
	// one delivery goroutine draining one channel in order, so once this arrives
	// "key-b" has already been offered to the watch and rejected. Waiting for it
	// also proves the watch is live, which a bare sleep-then-assert-empty could
	// not distinguish from a watch that was never registered.
	mgr.NotifyPut([]byte("key-a"), nil, []byte("value-a"), 2, 2)

	responses := waitResponses(t, stream, 1)
	if len(responses) != 1 {
		t.Fatalf("Expected only the barrier response, got %d", len(responses))
	}
	if got := string(responses[0].Events[0].Kv.Key); got != "key-a" {
		t.Errorf("Expected 0 responses for non-matching key; first delivered key was '%s'", got)
	}
}

// TestWatchManagerPrefixMatch tests prefix/range matching
func TestWatchManagerPrefixMatch(t *testing.T) {
	mgr := NewManager()
	stream := newMockManagerWatchStream()

	// Create prefix watch for "/config/"
	// Range end = prefix + 1 byte (e.g., "/config/" -> "/config0")
	rangeEnd := []byte("/config0") // Matches all keys starting with "/config/"
	watch, err := mgr.Add([]byte("/config/"), rangeEnd, 0, WatchFilters{}, stream)
	if err != nil {
		t.Fatalf("Failed to add watch: %v", err)
	}

	// Notify events for keys with matching prefix
	mgr.NotifyPut([]byte("/config/app"), nil, []byte("v1"), 1, 1)
	mgr.NotifyPut([]byte("/config/db"), nil, []byte("v2"), 2, 2)

	// Notify event for key without matching prefix (should not match)
	mgr.NotifyPut([]byte("/other/key"), nil, []byte("v3"), 3, 3)

	// Barrier, issued last and in range: its arrival means "/other/key" has
	// already been offered to the watch, so a third response here would be the
	// non-matching key rather than a delivery still in flight.
	mgr.NotifyPut([]byte("/config/zz"), nil, []byte("v4"), 4, 4)

	responses := waitResponses(t, stream, 3)
	if len(responses) != 3 {
		t.Fatalf("Expected 2 prefix matches plus the barrier, got %d", len(responses))
	}
	if got := string(responses[2].Events[0].Kv.Key); got != "/config/zz" {
		t.Fatalf("Expected the barrier '/config/zz' third; got '%s' — a non-matching key was delivered", got)
	}
	responses = responses[:2]

	// Verify watch ID
	for _, resp := range responses {
		if resp.WatchID != int64(watch.ID) {
			t.Errorf("Expected watch ID %d, got %d", watch.ID, resp.WatchID)
		}
	}

	// Verify first event
	if len(responses[0].Events) != 1 {
		t.Fatalf("Expected 1 event in first response, got %d", len(responses[0].Events))
	}
	if string(responses[0].Events[0].Kv.Key) != "/config/app" {
		t.Errorf("Expected key '/config/app', got '%s'", string(responses[0].Events[0].Kv.Key))
	}

	// Verify second event
	if len(responses[1].Events) != 1 {
		t.Fatalf("Expected 1 event in second response, got %d", len(responses[1].Events))
	}
	if string(responses[1].Events[0].Kv.Key) != "/config/db" {
		t.Errorf("Expected key '/config/db', got '%s'", string(responses[1].Events[0].Kv.Key))
	}
}

// TestWatchManagerDeleteEvent tests DELETE event delivery
func TestWatchManagerDeleteEvent(t *testing.T) {
	mgr := NewManager()
	stream := newMockManagerWatchStream()

	// Create watch
	watch, err := mgr.Add([]byte("test-key"), nil, 0, WatchFilters{}, stream)
	if err != nil {
		t.Fatalf("Failed to add watch: %v", err)
	}

	// Notify DELETE event
	mgr.NotifyDelete([]byte("test-key"), []byte("old-value"), 1, 1)

	// Verify event was delivered
	responses := waitResponses(t, stream, 1)
	if len(responses) != 1 {
		t.Fatalf("Expected 1 response, got %d", len(responses))
	}

	resp := responses[0]
	if resp.WatchID != int64(watch.ID) {
		t.Errorf("Expected watch ID %d, got %d", watch.ID, resp.WatchID)
	}

	event := resp.Events[0]
	if event.Type != DELETE {
		t.Errorf("Expected DELETE event, got %v", event.Type)
	}
	if string(event.Kv.Key) != "test-key" {
		t.Errorf("Expected key 'test-key', got '%s'", string(event.Kv.Key))
	}
	// Previous value should be set for DELETE events
	if event.PrevKv == nil {
		t.Error("Expected PrevKv to be set for DELETE event")
	} else if string(event.PrevKv.Value) != "old-value" {
		t.Errorf("Expected previous value 'old-value', got '%s'", string(event.PrevKv.Value))
	}
}

// TestWatchManagerFilters tests event filtering
func TestWatchManagerFilters(t *testing.T) {
	mgr := NewManager()

	t.Run("NoPut filter", func(t *testing.T) {
		stream := newMockManagerWatchStream()
		filters := WatchFilters{NoPut: true}
		_, err := mgr.Add([]byte("test-key"), nil, 0, filters, stream)
		if err != nil {
			t.Fatalf("Failed to add watch: %v", err)
		}

		// Send PUT event (should be filtered), then a DELETE that must pass. The
		// DELETE is the barrier: it is enqueued after the PUT and drained by the
		// same goroutine, so its arrival means the PUT was already offered and
		// dropped. Exactly one response total is the whole assertion.
		mgr.NotifyPut([]byte("test-key"), nil, []byte("value"), 1, 1)
		mgr.NotifyDelete([]byte("test-key"), []byte("value"), 2, 2)

		responses := waitResponses(t, stream, 1)
		if len(responses) != 1 {
			t.Fatalf("Expected only the DELETE to pass the NoPut filter, got %d responses", len(responses))
		}
		if got := responses[0].Events[0].Type; got != DELETE {
			t.Errorf("Expected PUT event to be filtered; first delivered event was %v", got)
		}
	})

	t.Run("NoDelete filter", func(t *testing.T) {
		stream := newMockManagerWatchStream()
		filters := WatchFilters{NoDelete: true}
		_, err := mgr.Add([]byte("test-key-2"), nil, 0, filters, stream)
		if err != nil {
			t.Fatalf("Failed to add watch: %v", err)
		}

		// Mirror of the NoPut case: the PUT is the barrier for the filtered DELETE.
		mgr.NotifyDelete([]byte("test-key-2"), []byte("value"), 1, 1)
		mgr.NotifyPut([]byte("test-key-2"), nil, []byte("value"), 2, 2)

		responses := waitResponses(t, stream, 1)
		if len(responses) != 1 {
			t.Fatalf("Expected only the PUT to pass the NoDelete filter, got %d responses", len(responses))
		}
		if got := responses[0].Events[0].Type; got != PUT {
			t.Errorf("Expected DELETE event to be filtered; first delivered event was %v", got)
		}
	})
}

// TestWatchManagerRemove tests watch removal
func TestWatchManagerRemove(t *testing.T) {
	mgr := NewManager()
	stream := newMockManagerWatchStream()

	// Create watch
	watch, err := mgr.Add([]byte("test-key"), nil, 0, WatchFilters{}, stream)
	if err != nil {
		t.Fatalf("Failed to add watch: %v", err)
	}

	// Remove watch
	mgr.Remove(watch.ID)

	// A second watch on the same key, registered after the removal, is the
	// witness: one notification now reaches it and must not reach the removed
	// watch's stream. Without it, an empty stream would be equally consistent
	// with a notification that never matched anything at all.
	witness := newMockManagerWatchStream()
	if _, err := mgr.Add([]byte("test-key"), nil, 0, WatchFilters{}, witness); err != nil {
		t.Fatalf("Failed to add witness watch: %v", err)
	}

	// Notify event after removal (should not be delivered)
	mgr.NotifyPut([]byte("test-key"), nil, []byte("value"), 1, 1)
	waitResponses(t, witness, 1)

	// The removed watch was deregistered under the shard lock before the notify
	// ran, so it could not even have been enqueued: the witness having been
	// served is the proof that the notify itself happened.
	responses := stream.getResponses()
	if len(responses) != 0 {
		t.Errorf("Expected 0 responses after watch removal, got %d", len(responses))
	}
}

// TestWatchManagerMultipleWatches tests multiple watches on the same key
func TestWatchManagerMultipleWatches(t *testing.T) {
	mgr := NewManager()
	stream1 := newMockManagerWatchStream()
	stream2 := newMockManagerWatchStream()

	// Create two watches for the same key
	watch1, err := mgr.Add([]byte("shared-key"), nil, 0, WatchFilters{}, stream1)
	if err != nil {
		t.Fatalf("Failed to add watch1: %v", err)
	}

	watch2, err := mgr.Add([]byte("shared-key"), nil, 0, WatchFilters{}, stream2)
	if err != nil {
		t.Fatalf("Failed to add watch2: %v", err)
	}

	// Notify event
	mgr.NotifyPut([]byte("shared-key"), nil, []byte("value"), 1, 1)

	// Each watch has its own delivery goroutine and its own stream, so each is
	// waited for on its own signal; there is no ordering between the two.
	// Verify both watches received the event
	responses1 := waitResponses(t, stream1, 1)
	if len(responses1) != 1 {
		t.Errorf("Expected 1 response for watch1, got %d", len(responses1))
	} else if responses1[0].WatchID != int64(watch1.ID) {
		t.Errorf("Expected watch ID %d for watch1, got %d", watch1.ID, responses1[0].WatchID)
	}

	responses2 := waitResponses(t, stream2, 1)
	if len(responses2) != 1 {
		t.Errorf("Expected 1 response for watch2, got %d", len(responses2))
	} else if responses2[0].WatchID != int64(watch2.ID) {
		t.Errorf("Expected watch ID %d for watch2, got %d", watch2.ID, responses2[0].WatchID)
	}
}

// TestWatchManagerStats tests watch statistics
func TestWatchManagerStats(t *testing.T) {
	mgr := NewManager()
	stream1 := newMockManagerWatchStream()
	stream2 := newMockManagerWatchStream()

	// Create exact watch
	_, err := mgr.Add([]byte("key1"), nil, 0, WatchFilters{}, stream1)
	if err != nil {
		t.Fatalf("Failed to add exact watch: %v", err)
	}

	// Create prefix watch
	_, err = mgr.Add([]byte("/prefix/"), []byte("/prefix0"), 0, WatchFilters{}, stream2)
	if err != nil {
		t.Fatalf("Failed to add prefix watch: %v", err)
	}

	// Get stats
	stats := mgr.GetStats()
	if stats.TotalWatches != 2 {
		t.Errorf("Expected 2 total watches, got %d", stats.TotalWatches)
	}
	if stats.ExactWatches != 1 {
		t.Errorf("Expected 1 exact watch, got %d", stats.ExactWatches)
	}
	if stats.PrefixWatches != 1 {
		t.Errorf("Expected 1 prefix watch, got %d", stats.PrefixWatches)
	}
}

// TestWatchManagerConcurrentNotifications tests concurrent event notifications
func TestWatchManagerConcurrentNotifications(t *testing.T) {
	mgr := NewManager()
	stream := newMockManagerWatchStream()

	// Create watch
	_, err := mgr.Add([]byte("concurrent-key"), nil, 0, WatchFilters{}, stream)
	if err != nil {
		t.Fatalf("Failed to add watch: %v", err)
	}

	// Send multiple events concurrently
	const numEvents = 100
	var wg sync.WaitGroup
	wg.Add(numEvents)

	for i := 0; i < numEvents; i++ {
		go func(rev int64) {
			defer wg.Done()
			mgr.NotifyPut([]byte("concurrent-key"), nil, []byte("value"), rev, rev)
		}(int64(i + 1))
	}

	wg.Wait()

	// Verify all events were delivered. waitResponses returns as soon as the
	// count is reached, so the equality check below still catches an over-count.
	responses := waitResponses(t, stream, numEvents)
	if len(responses) != numEvents {
		t.Errorf("Expected %d responses, got %d", numEvents, len(responses))
	}
}
