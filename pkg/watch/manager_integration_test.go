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
	"time"
)

// All tests in this file use time.Sleep for goroutine synchronization,
// making them integration tests rather than pure unit tests.
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

	// Give event delivery time to complete
	time.Sleep(50 * time.Millisecond)

	// Verify event was delivered
	responses := stream.getResponses()
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

	// Give event delivery time to complete
	time.Sleep(50 * time.Millisecond)

	// Verify no events were delivered
	responses := stream.getResponses()
	if len(responses) != 0 {
		t.Errorf("Expected 0 responses for non-matching key, got %d", len(responses))
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

	// Give event delivery time to complete
	time.Sleep(100 * time.Millisecond)

	// Verify 2 events were delivered (matching prefix)
	responses := stream.getResponses()
	if len(responses) != 2 {
		t.Fatalf("Expected 2 responses for prefix match, got %d", len(responses))
	}

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

	// Give event delivery time to complete
	time.Sleep(50 * time.Millisecond)

	// Verify event was delivered
	responses := stream.getResponses()
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

		// Send PUT event (should be filtered)
		mgr.NotifyPut([]byte("test-key"), nil, []byte("value"), 1, 1)
		time.Sleep(50 * time.Millisecond)

		responses := stream.getResponses()
		if len(responses) != 0 {
			t.Errorf("Expected PUT event to be filtered, got %d responses", len(responses))
		}

		// Send DELETE event (should not be filtered)
		mgr.NotifyDelete([]byte("test-key"), []byte("value"), 2, 2)
		time.Sleep(50 * time.Millisecond)

		responses = stream.getResponses()
		if len(responses) != 1 {
			t.Errorf("Expected DELETE event to pass through, got %d responses", len(responses))
		}
	})

	t.Run("NoDelete filter", func(t *testing.T) {
		stream := newMockManagerWatchStream()
		filters := WatchFilters{NoDelete: true}
		_, err := mgr.Add([]byte("test-key-2"), nil, 0, filters, stream)
		if err != nil {
			t.Fatalf("Failed to add watch: %v", err)
		}

		// Send DELETE event (should be filtered)
		mgr.NotifyDelete([]byte("test-key-2"), []byte("value"), 1, 1)
		time.Sleep(50 * time.Millisecond)

		responses := stream.getResponses()
		if len(responses) != 0 {
			t.Errorf("Expected DELETE event to be filtered, got %d responses", len(responses))
		}

		// Send PUT event (should not be filtered)
		mgr.NotifyPut([]byte("test-key-2"), nil, []byte("value"), 2, 2)
		time.Sleep(50 * time.Millisecond)

		responses = stream.getResponses()
		if len(responses) != 1 {
			t.Errorf("Expected PUT event to pass through, got %d responses", len(responses))
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

	// Notify event after removal (should not be delivered)
	mgr.NotifyPut([]byte("test-key"), nil, []byte("value"), 1, 1)
	time.Sleep(50 * time.Millisecond)

	// Verify no events were delivered
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

	// Give event delivery time to complete
	time.Sleep(50 * time.Millisecond)

	// Verify both watches received the event
	responses1 := stream1.getResponses()
	if len(responses1) != 1 {
		t.Errorf("Expected 1 response for watch1, got %d", len(responses1))
	} else if responses1[0].WatchID != int64(watch1.ID) {
		t.Errorf("Expected watch ID %d for watch1, got %d", watch1.ID, responses1[0].WatchID)
	}

	responses2 := stream2.getResponses()
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

	// Give event delivery time to complete
	time.Sleep(200 * time.Millisecond)

	// Verify all events were delivered
	responses := stream.getResponses()
	if len(responses) != numEvents {
		t.Errorf("Expected %d responses, got %d", numEvents, len(responses))
	}
}
