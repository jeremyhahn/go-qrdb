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

package watch

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewManager verifies manager initialization.
func TestNewManager(t *testing.T) {
	mgr := NewManager()
	require.NotNil(t, mgr)

	// Verify all shards are initialized
	for i := range mgr.shards {
		require.NotNil(t, mgr.shards[i])
		require.NotNil(t, mgr.shards[i].exactWatches)
		require.NotNil(t, mgr.shards[i].prefixWatches)
	}

	// Verify initial state
	assert.Equal(t, uint64(0), mgr.idSeq)
	assert.Equal(t, uint64(0), mgr.watchCount)

	// Verify event pool
	event := mgr.eventPool.Get().(*ManagerWatchEvent)
	require.NotNil(t, event)
	mgr.eventPool.Put(event)
}

// TestManagerWithPrefix verifies the WithPrefix option.
func TestManagerWithPrefix(t *testing.T) {
	opts := &managerWatchOptions{}
	assert.False(t, opts.prefix)

	// Apply option
	fn := WithPrefix()
	fn(opts)

	assert.True(t, opts.prefix)
}

// TestManagerGetPrefix verifies prefix range calculation.
func TestManagerGetPrefix(t *testing.T) {
	tests := []struct {
		name     string
		prefix   []byte
		expected []byte
	}{
		{
			name:     "empty prefix",
			prefix:   []byte{},
			expected: []byte{},
		},
		{
			name:     "simple prefix",
			prefix:   []byte("foo/"),
			expected: []byte("foo0"),
		},
		{
			name:     "config prefix",
			prefix:   []byte("/config/"),
			expected: []byte("/config0"),
		},
		{
			name:     "single char",
			prefix:   []byte("a"),
			expected: []byte("b"),
		},
		{
			name:     "all 0xff bytes",
			prefix:   []byte{0xff, 0xff, 0xff},
			expected: []byte{},
		},
		{
			name:     "mixed bytes ending in 0xfe",
			prefix:   []byte{0x01, 0x02, 0xfe},
			expected: []byte{0x01, 0x02, 0xff},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := managerGetPrefix(tt.prefix)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestManagerFnv1a verifies hash function distribution.
func TestManagerFnv1a(t *testing.T) {
	tests := []struct {
		name string
		key  []byte
	}{
		{name: "simple key", key: []byte("test")},
		{name: "empty key", key: []byte{}},
		{name: "long key", key: []byte("very-long-key-name-for-testing")},
		{name: "numeric key", key: []byte("12345")},
		{name: "path key", key: []byte("/config/server/port")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash := managerFnv1a(tt.key)
			// Hash should be in valid range [0, 255]
			assert.LessOrEqual(t, hash, uint8(255))
		})
	}

	// Verify same key produces same hash
	key := []byte("test-key")
	hash1 := managerFnv1a(key)
	hash2 := managerFnv1a(key)
	assert.Equal(t, hash1, hash2)

	// Verify different keys produce different hashes (usually)
	key2 := []byte("test-key2")
	hash3 := managerFnv1a(key2)
	// While collision is possible, these specific keys should differ
	assert.NotEqual(t, hash1, hash3)
}

// TestManagerGetShard verifies shard selection.
func TestManagerGetShard(t *testing.T) {
	mgr := NewManager()

	key := []byte("test-key")
	shard := mgr.managerGetShard(key)
	require.NotNil(t, shard)

	// Verify same key always returns same shard
	shard2 := mgr.managerGetShard(key)
	assert.Equal(t, shard, shard2)

	// Verify shard is one of the manager's shards
	found := false
	for i := range mgr.shards {
		if mgr.shards[i] == shard {
			found = true
			break
		}
	}
	assert.True(t, found, "returned shard should be from manager's shard array")
}

// TestManagerMatchesRange verifies key range matching logic.
func TestManagerMatchesRange(t *testing.T) {
	mgr := NewManager()

	tests := []struct {
		name     string
		key      []byte
		start    []byte
		end      []byte
		expected bool
	}{
		{
			name:     "exact match with no range",
			key:      []byte("foo"),
			start:    []byte("foo"),
			end:      nil,
			expected: true,
		},
		{
			name:     "key before range",
			key:      []byte("aaa"),
			start:    []byte("bbb"),
			end:      []byte("zzz"),
			expected: false,
		},
		{
			name:     "key after range",
			key:      []byte("zzz"),
			start:    []byte("aaa"),
			end:      []byte("bbb"),
			expected: false,
		},
		{
			name:     "key within range",
			key:      []byte("mmm"),
			start:    []byte("aaa"),
			end:      []byte("zzz"),
			expected: true,
		},
		{
			name:     "key at start of range",
			key:      []byte("aaa"),
			start:    []byte("aaa"),
			end:      []byte("zzz"),
			expected: true,
		},
		{
			name:     "key at end of range (exclusive)",
			key:      []byte("zzz"),
			start:    []byte("aaa"),
			end:      []byte("zzz"),
			expected: false,
		},
		{
			name:     "prefix match - /config/server in /config/",
			key:      []byte("/config/server"),
			start:    []byte("/config/"),
			end:      []byte("/config0"),
			expected: true,
		},
		{
			name:     "prefix match - /config/db in /config/",
			key:      []byte("/config/db"),
			start:    []byte("/config/"),
			end:      []byte("/config0"),
			expected: true,
		},
		{
			name:     "prefix no match - /data/x not in /config/",
			key:      []byte("/data/x"),
			start:    []byte("/config/"),
			end:      []byte("/config0"),
			expected: false,
		},
		{
			name:     "empty range end matches all after start",
			key:      []byte("zzz"),
			start:    []byte("aaa"),
			end:      []byte{}, // empty but non-nil
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mgr.managerMatchesRange(tt.key, tt.start, tt.end)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestManagerAddInternal_ExactWatch verifies exact key watch registration.
func TestManagerAddInternal_ExactWatch(t *testing.T) {
	mgr := NewManager()
	stream := newMockManagerWatchStream()

	key := []byte("test-key")
	watch, err := mgr.addInternal(key, nil, 0, WatchFilters{}, stream, &managerWatchOptions{}, true)

	require.NoError(t, err)
	require.NotNil(t, watch)

	// Verify watch properties
	assert.Equal(t, uint64(1), watch.ID)
	assert.Equal(t, key, watch.Key)
	assert.Nil(t, watch.RangeEnd)
	assert.Equal(t, int64(0), watch.StartRev)
	assert.NotNil(t, watch.Events)
	assert.NotNil(t, watch.ctx)
	assert.NotNil(t, watch.cancel)
	assert.True(t, watch.skipDeliver)

	// Verify watch was added to correct shard
	shard := mgr.managerGetShard(key)
	shard.mu.RLock()
	watches := shard.exactWatches[string(key)]
	shard.mu.RUnlock()

	require.Len(t, watches, 1)
	assert.Equal(t, watch, watches[0])

	// Verify watch count
	assert.Equal(t, uint64(1), mgr.watchCount)
}

// TestManagerAddInternal_PrefixWatch verifies prefix watch registration.
func TestManagerAddInternal_PrefixWatch(t *testing.T) {
	mgr := NewManager()
	stream := newMockManagerWatchStream()

	key := []byte("/config/")
	rangeEnd := []byte("/config0")
	watch, err := mgr.addInternal(key, rangeEnd, 0, WatchFilters{}, stream, &managerWatchOptions{}, true)

	require.NoError(t, err)
	require.NotNil(t, watch)

	// Verify watch properties
	assert.Equal(t, uint64(1), watch.ID)
	assert.Equal(t, key, watch.Key)
	assert.Equal(t, rangeEnd, watch.RangeEnd)

	// Verify watch was added to shard's prefix watches
	shard := mgr.managerGetShard(key)
	shard.mu.RLock()
	prefixWatches := shard.prefixWatches
	shard.mu.RUnlock()

	require.Len(t, prefixWatches, 1)
	assert.Equal(t, watch, prefixWatches[0])

	// Verify watch count
	assert.Equal(t, uint64(1), mgr.watchCount)
}

// TestManagerAddInternal_MultipleWatchesSameKey verifies multiple watches on same key.
func TestManagerAddInternal_MultipleWatchesSameKey(t *testing.T) {
	mgr := NewManager()
	stream := newMockManagerWatchStream()

	key := []byte("test-key")

	// Add first watch
	watch1, err := mgr.addInternal(key, nil, 0, WatchFilters{}, stream, &managerWatchOptions{}, true)
	require.NoError(t, err)
	require.NotNil(t, watch1)

	// Add second watch
	watch2, err := mgr.addInternal(key, nil, 0, WatchFilters{}, stream, &managerWatchOptions{}, true)
	require.NoError(t, err)
	require.NotNil(t, watch2)

	// Verify unique IDs
	assert.NotEqual(t, watch1.ID, watch2.ID)
	assert.Equal(t, uint64(1), watch1.ID)
	assert.Equal(t, uint64(2), watch2.ID)

	// Verify both watches are in shard
	shard := mgr.managerGetShard(key)
	shard.mu.RLock()
	watches := shard.exactWatches[string(key)]
	shard.mu.RUnlock()

	require.Len(t, watches, 2)
	assert.Equal(t, uint64(2), mgr.watchCount)
}

// TestManagerAddInternal_WithFilters verifies filter configuration.
func TestManagerAddInternal_WithFilters(t *testing.T) {
	mgr := NewManager()
	stream := newMockManagerWatchStream()

	filters := WatchFilters{
		NoPut:    true,
		NoDelete: false,
	}

	watch, err := mgr.addInternal([]byte("key"), nil, 0, filters, stream, &managerWatchOptions{}, true)
	require.NoError(t, err)

	assert.Equal(t, filters, watch.Filters)
}

// TestManagerAdd verifies public Add method.
func TestManagerAdd(t *testing.T) {
	mgr := NewManager()
	stream := newMockManagerWatchStream()

	watch, err := mgr.Add([]byte("key"), nil, 0, WatchFilters{}, stream)
	require.NoError(t, err)
	require.NotNil(t, watch)

	// Verify skipDeliver is false (deliverEvents should start)
	assert.False(t, watch.skipDeliver)

	// Clean up
	watch.cancel()
}

// TestManagerRemove_ExactWatch verifies removing exact watch.
func TestManagerRemove_ExactWatch(t *testing.T) {
	mgr := NewManager()
	stream := newMockManagerWatchStream()

	key := []byte("test-key")
	watch, err := mgr.addInternal(key, nil, 0, WatchFilters{}, stream, &managerWatchOptions{}, true)
	require.NoError(t, err)

	// Verify watch exists
	assert.Equal(t, uint64(1), mgr.watchCount)

	// Remove watch
	mgr.Remove(watch.ID)

	// Verify watch removed
	assert.Equal(t, uint64(0), mgr.watchCount)

	// Verify shard is clean
	shard := mgr.managerGetShard(key)
	shard.mu.RLock()
	watches := shard.exactWatches[string(key)]
	shard.mu.RUnlock()

	assert.Len(t, watches, 0)
}

// TestManagerRemove_PrefixWatch verifies removing prefix watch.
func TestManagerRemove_PrefixWatch(t *testing.T) {
	mgr := NewManager()
	stream := newMockManagerWatchStream()

	key := []byte("/config/")
	rangeEnd := []byte("/config0")
	watch, err := mgr.addInternal(key, rangeEnd, 0, WatchFilters{}, stream, &managerWatchOptions{}, true)
	require.NoError(t, err)

	// Verify watch exists
	assert.Equal(t, uint64(1), mgr.watchCount)

	// Remove watch
	mgr.Remove(watch.ID)

	// Verify watch removed
	assert.Equal(t, uint64(0), mgr.watchCount)

	// Verify shard is clean
	shard := mgr.managerGetShard(key)
	shard.mu.RLock()
	prefixWatches := shard.prefixWatches
	shard.mu.RUnlock()

	assert.Len(t, prefixWatches, 0)
}

// TestManagerRemove_NonExistentWatch verifies removing non-existent watch doesn't panic.
func TestManagerRemove_NonExistentWatch(t *testing.T) {
	mgr := NewManager()

	// Should not panic
	assert.NotPanics(t, func() {
		mgr.Remove(999)
	})
}

// TestManagerRemove_OneOfMultipleWatches verifies removing one watch leaves others.
func TestManagerRemove_OneOfMultipleWatches(t *testing.T) {
	mgr := NewManager()
	stream := newMockManagerWatchStream()

	key := []byte("test-key")

	// Add three watches
	watch1, _ := mgr.addInternal(key, nil, 0, WatchFilters{}, stream, &managerWatchOptions{}, true)
	watch2, _ := mgr.addInternal(key, nil, 0, WatchFilters{}, stream, &managerWatchOptions{}, true)
	watch3, _ := mgr.addInternal(key, nil, 0, WatchFilters{}, stream, &managerWatchOptions{}, true)

	assert.Equal(t, uint64(3), mgr.watchCount)

	// Remove middle watch
	mgr.Remove(watch2.ID)

	assert.Equal(t, uint64(2), mgr.watchCount)

	// Verify remaining watches
	shard := mgr.managerGetShard(key)
	shard.mu.RLock()
	watches := shard.exactWatches[string(key)]
	shard.mu.RUnlock()

	require.Len(t, watches, 2)
	assert.Equal(t, watch1.ID, watches[0].ID)
	assert.Equal(t, watch3.ID, watches[1].ID)
}

// TestManagerSendEvent_WithFilters verifies event filtering.
func TestManagerSendEvent_WithFilters(t *testing.T) {
	mgr := NewManager()

	tests := []struct {
		name       string
		filters    WatchFilters
		eventType  ManagerEventType
		shouldSend bool
	}{
		{
			name:       "no filters - PUT event",
			filters:    WatchFilters{},
			eventType:  PUT,
			shouldSend: true,
		},
		{
			name:       "no filters - DELETE event",
			filters:    WatchFilters{},
			eventType:  DELETE,
			shouldSend: true,
		},
		{
			name:       "filter PUT - PUT event",
			filters:    WatchFilters{NoPut: true},
			eventType:  PUT,
			shouldSend: false,
		},
		{
			name:       "filter PUT - DELETE event",
			filters:    WatchFilters{NoPut: true},
			eventType:  DELETE,
			shouldSend: true,
		},
		{
			name:       "filter DELETE - DELETE event",
			filters:    WatchFilters{NoDelete: true},
			eventType:  DELETE,
			shouldSend: false,
		},
		{
			name:       "filter DELETE - PUT event",
			filters:    WatchFilters{NoDelete: true},
			eventType:  PUT,
			shouldSend: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			watch := &ManagerWatch{
				ID:      1,
				Filters: tt.filters,
				Events:  make(chan *ManagerWatchEvent, 10),
				ctx:     ctx,
				cancel:  cancel,
			}

			event := &ManagerWatchEvent{
				Type: tt.eventType,
				Kv: &ManagerKeyValue{
					Key:   []byte("key"),
					Value: []byte("value"),
				},
			}

			mgr.sendEvent(watch, event)

			// Check if event was sent
			select {
			case <-watch.Events:
				if !tt.shouldSend {
					t.Error("Event was sent when it should have been filtered")
				}
			default:
				if tt.shouldSend {
					t.Error("Event was not sent when it should have been")
				}
			}
		})
	}
}

// TestManagerSendEvent_ContextCancelled verifies handling of cancelled context.
func TestManagerSendEvent_ContextCancelled(t *testing.T) {
	mgr := NewManager()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	watch := &ManagerWatch{
		ID:      1,
		Filters: WatchFilters{},
		Events:  make(chan *ManagerWatchEvent, 1),
		ctx:     ctx,
		cancel:  cancel,
	}

	event := &ManagerWatchEvent{
		Type: PUT,
		Kv: &ManagerKeyValue{
			Key:   []byte("key"),
			Value: []byte("value"),
		},
	}

	// Should not panic or block
	assert.NotPanics(t, func() {
		mgr.sendEvent(watch, event)
	})
}

// TestManagerNotifyPut_NoWatches verifies fast path when no watches exist.
func TestManagerNotifyPut_NoWatches(t *testing.T) {
	mgr := NewManager()

	// Should return immediately without error
	assert.NotPanics(t, func() {
		mgr.NotifyPut([]byte("key"), nil, []byte("value"), 1, 1)
	})

	// Verify watchCount is still 0
	assert.Equal(t, uint64(0), mgr.watchCount)
}

// TestManagerNotifyDelete_NoWatches verifies fast path when no watches exist.
func TestManagerNotifyDelete_NoWatches(t *testing.T) {
	mgr := NewManager()

	// Should return immediately without error
	assert.NotPanics(t, func() {
		mgr.NotifyDelete([]byte("key"), []byte("prev-value"), 1, 1)
	})

	assert.Equal(t, uint64(0), mgr.watchCount)
}

// TestManagerNotify_ExactMatch verifies exact key notification.
func TestManagerNotify_ExactMatch(t *testing.T) {
	mgr := NewManager()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream := &mockManagerWatchStream{ctx: ctx}
	watch, err := mgr.addInternal([]byte("test-key"), nil, 0, WatchFilters{}, stream, &managerWatchOptions{}, true)
	require.NoError(t, err)

	// Notify PUT
	mgr.NotifyPut([]byte("test-key"), nil, []byte("test-value"), 1, 1)

	// Check event received
	select {
	case event := <-watch.Events:
		assert.Equal(t, PUT, event.Type)
		assert.Equal(t, []byte("test-key"), event.Kv.Key)
		assert.Equal(t, []byte("test-value"), event.Kv.Value)
		assert.Equal(t, int64(1), event.Revision)
		assert.Equal(t, int64(1), event.Kv.ModRevision)
	default:
		t.Error("Expected event not received")
	}
}

// TestManagerNotify_PrefixMatch verifies prefix key notification.
func TestManagerNotify_PrefixMatch(t *testing.T) {
	mgr := NewManager()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream := &mockManagerWatchStream{ctx: ctx}
	prefix := []byte("/config/")
	rangeEnd := managerGetPrefix(prefix)

	watch, err := mgr.addInternal(prefix, rangeEnd, 0, WatchFilters{}, stream, &managerWatchOptions{}, true)
	require.NoError(t, err)

	// Notify PUT for key matching prefix
	mgr.NotifyPut([]byte("/config/server"), nil, []byte("localhost"), 1, 1)

	// Check event received
	select {
	case event := <-watch.Events:
		assert.Equal(t, PUT, event.Type)
		assert.Equal(t, []byte("/config/server"), event.Kv.Key)
		assert.Equal(t, []byte("localhost"), event.Kv.Value)
	default:
		t.Error("Expected event not received")
	}
}

// TestManagerNotify_NoMatch verifies non-matching keys don't trigger events.
func TestManagerNotify_NoMatch(t *testing.T) {
	mgr := NewManager()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream := &mockManagerWatchStream{ctx: ctx}
	watch, err := mgr.addInternal([]byte("key-a"), nil, 0, WatchFilters{}, stream, &managerWatchOptions{}, true)
	require.NoError(t, err)

	// Notify for different key
	mgr.NotifyPut([]byte("key-b"), nil, []byte("value"), 1, 1)

	// Should not receive event
	select {
	case <-watch.Events:
		t.Error("Should not receive event for non-matching key")
	default:
		// Expected
	}
}

// TestManagerNotify_DeleteEvent verifies DELETE event handling.
func TestManagerNotify_DeleteEvent(t *testing.T) {
	mgr := NewManager()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream := &mockManagerWatchStream{ctx: ctx}
	watch, err := mgr.addInternal([]byte("test-key"), nil, 0, WatchFilters{}, stream, &managerWatchOptions{}, true)
	require.NoError(t, err)

	// Notify DELETE
	mgr.NotifyDelete([]byte("test-key"), []byte("old-value"), 2, 2)

	// Check event received
	select {
	case event := <-watch.Events:
		assert.Equal(t, DELETE, event.Type)
		assert.Equal(t, []byte("test-key"), event.Kv.Key)
		assert.Nil(t, event.Kv.Value) // DELETE events have nil value
		assert.NotNil(t, event.PrevKv)
		assert.Equal(t, []byte("old-value"), event.PrevKv.Value)
		assert.Equal(t, int64(2), event.Revision)
	default:
		t.Error("Expected event not received")
	}
}

// TestManagerGetStats verifies statistics collection.
func TestManagerGetStats(t *testing.T) {
	mgr := NewManager()
	stream := newMockManagerWatchStream()

	// Initially no watches
	stats := mgr.GetStats()
	assert.Equal(t, 0, stats.TotalWatches)
	assert.Equal(t, 0, stats.ExactWatches)
	assert.Equal(t, 0, stats.PrefixWatches)

	// Add exact watches
	_, _ = mgr.addInternal([]byte("key1"), nil, 0, WatchFilters{}, stream, &managerWatchOptions{}, true)
	_, _ = mgr.addInternal([]byte("key2"), nil, 0, WatchFilters{}, stream, &managerWatchOptions{}, true)

	stats = mgr.GetStats()
	assert.Equal(t, 2, stats.TotalWatches)
	assert.Equal(t, 2, stats.ExactWatches)
	assert.Equal(t, 0, stats.PrefixWatches)

	// Add prefix watch
	_, _ = mgr.addInternal([]byte("/config/"), []byte("/config0"), 0, WatchFilters{}, stream, &managerWatchOptions{}, true)

	stats = mgr.GetStats()
	assert.Equal(t, 3, stats.TotalWatches)
	assert.Equal(t, 2, stats.ExactWatches)
	assert.Equal(t, 1, stats.PrefixWatches)
}

// TestManagerNoopStream verifies noop stream implementation.
func TestManagerNoopStream(t *testing.T) {
	ctx := context.Background()
	stream := &managerNoopStream{ctx: ctx}

	// Send should not error
	err := stream.Send(&ManagerWatchResponse{})
	assert.NoError(t, err)

	// Context should return correct context
	assert.Equal(t, ctx, stream.Context())
}

// TestManagerWatchContextCancellation verifies watch cleanup on context cancel.
// TestManagerConcurrentAddRemove verifies thread safety of add/remove operations.
func TestManagerConcurrentAddRemove(t *testing.T) {
	mgr := NewManager()
	stream := newMockManagerWatchStream()

	var wg sync.WaitGroup
	watchIDs := make(chan uint64, 100)

	// Concurrent adds
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := []byte("key")
			watch, err := mgr.addInternal(key, nil, 0, WatchFilters{}, stream, &managerWatchOptions{}, true)
			if err == nil {
				watchIDs <- watch.ID
			}
		}(i)
	}

	wg.Wait()
	close(watchIDs)

	// Verify all watches added
	addedCount := 0
	ids := []uint64{}
	for id := range watchIDs {
		ids = append(ids, id)
		addedCount++
	}
	assert.Equal(t, 50, addedCount)
	assert.Equal(t, uint64(50), mgr.watchCount)

	// Concurrent removes
	for _, id := range ids {
		wg.Add(1)
		go func(watchID uint64) {
			defer wg.Done()
			mgr.Remove(watchID)
		}(id)
	}

	wg.Wait()

	// Verify all watches removed
	assert.Equal(t, uint64(0), mgr.watchCount)
}

// TestManagerEventPool verifies event pooling reduces allocations.
// TestManagerWatchFilters verifies filter struct.
