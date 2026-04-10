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
	"fmt"
	"testing"
)

// BenchmarkWatchManagerExactMatch benchmarks exact key matching performance.
func BenchmarkWatchManagerExactMatch(b *testing.B) {
	mgr := NewManager()
	stream := newMockManagerWatchStream()

	// Create watch
	_, err := mgr.Add([]byte("bench-key"), nil, 0, WatchFilters{}, stream)
	if err != nil {
		b.Fatalf("Failed to add watch: %v", err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		mgr.NotifyPut([]byte("bench-key"), nil, []byte("value"), int64(i), int64(i))
	}
}

// BenchmarkWatchManagerNoWatches benchmarks write performance with no active watches.
func BenchmarkWatchManagerNoWatches(b *testing.B) {
	mgr := NewManager()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// Notify will do atomic pointer check and return immediately
		mgr.NotifyPut([]byte("bench-key"), nil, []byte("value"), int64(i), int64(i))
	}
}

// BenchmarkWatchManagerPrefixMatch benchmarks prefix matching performance.
func BenchmarkWatchManagerPrefixMatch(b *testing.B) {
	mgr := NewManager()
	stream := newMockManagerWatchStream()

	// Create prefix watch
	rangeEnd := []byte("/config0")
	_, err := mgr.Add([]byte("/config/"), rangeEnd, 0, WatchFilters{}, stream)
	if err != nil {
		b.Fatalf("Failed to add watch: %v", err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		mgr.NotifyPut([]byte("/config/app"), nil, []byte("value"), int64(i), int64(i))
	}
}

// BenchmarkWatchManagerMultipleExactWatches benchmarks performance with multiple exact watches.
func BenchmarkWatchManagerMultipleExactWatches(b *testing.B) {
	mgr := NewManager()

	// Create 100 watches on different keys
	for i := 0; i < 100; i++ {
		stream := newMockManagerWatchStream()
		key := []byte(fmt.Sprintf("key-%d", i))
		_, err := mgr.Add(key, nil, 0, WatchFilters{}, stream)
		if err != nil {
			b.Fatalf("Failed to add watch: %v", err)
		}
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// Notify will only match one watch
		key := []byte(fmt.Sprintf("key-%d", i%100))
		mgr.NotifyPut(key, nil, []byte("value"), int64(i), int64(i))
	}
}

// BenchmarkWatchManagerMultiplePrefixWatches benchmarks performance with multiple prefix watches.
func BenchmarkWatchManagerMultiplePrefixWatches(b *testing.B) {
	mgr := NewManager()

	// Create 10 prefix watches
	for i := 0; i < 10; i++ {
		stream := newMockManagerWatchStream()
		prefix := []byte(fmt.Sprintf("/prefix%d/", i))
		rangeEnd := []byte(fmt.Sprintf("/prefix%d0", i))
		_, err := mgr.Add(prefix, rangeEnd, 0, WatchFilters{}, stream)
		if err != nil {
			b.Fatalf("Failed to add watch: %v", err)
		}
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// This key will match one prefix watch
		key := []byte(fmt.Sprintf("/prefix%d/key", i%10))
		mgr.NotifyPut(key, nil, []byte("value"), int64(i), int64(i))
	}
}

// BenchmarkWatchManagerConcurrentNotifications benchmarks concurrent notifications.
func BenchmarkWatchManagerConcurrentNotifications(b *testing.B) {
	mgr := NewManager()
	stream := newMockManagerWatchStream()

	// Create watch
	_, err := mgr.Add([]byte("concurrent-key"), nil, 0, WatchFilters{}, stream)
	if err != nil {
		b.Fatalf("Failed to add watch: %v", err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			mgr.NotifyPut([]byte("concurrent-key"), nil, []byte("value"), int64(i), int64(i))
			i++
		}
	})
}

// BenchmarkWatchManagerAddRemove benchmarks watch add/remove operations.
func BenchmarkWatchManagerAddRemove(b *testing.B) {
	mgr := NewManager()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		stream := newMockManagerWatchStream()
		watch, err := mgr.Add([]byte("bench-key"), nil, 0, WatchFilters{}, stream)
		if err != nil {
			b.Fatalf("Failed to add watch: %v", err)
		}
		mgr.Remove(watch.ID)
	}
}

// BenchmarkWatchManagerSharding benchmarks shard distribution.
func BenchmarkWatchManagerSharding(b *testing.B) {
	mgr := NewManager()

	// Pre-create watches across all shards
	for i := 0; i < 256; i++ {
		stream := newMockManagerWatchStream()
		key := []byte(fmt.Sprintf("shard-key-%d", i))
		_, err := mgr.Add(key, nil, 0, WatchFilters{}, stream)
		if err != nil {
			b.Fatalf("Failed to add watch: %v", err)
		}
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		key := []byte(fmt.Sprintf("shard-key-%d", i%256))
		mgr.NotifyPut(key, nil, []byte("value"), int64(i), int64(i))
	}
}

// BenchmarkWatchEventDelivery benchmarks end-to-end event delivery.
func BenchmarkWatchEventDelivery(b *testing.B) {
	mgr := NewManager()
	stream := newMockManagerWatchStream()

	_, err := mgr.Add([]byte("delivery-key"), nil, 0, WatchFilters{}, stream)
	if err != nil {
		b.Fatalf("Failed to add watch: %v", err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	// Just benchmark notification, not end-to-end delivery
	// (End-to-end delivery is tested in integration tests)
	for i := 0; i < b.N; i++ {
		mgr.NotifyPut([]byte("delivery-key"), nil, []byte("value"), int64(i), int64(i))
	}
}

// BenchmarkWatchManagerMemoryPooling benchmarks event pool efficiency.
func BenchmarkWatchManagerMemoryPooling(b *testing.B) {
	mgr := NewManager()
	stream := newMockManagerWatchStream()

	_, err := mgr.Add([]byte("pool-key"), nil, 0, WatchFilters{}, stream)
	if err != nil {
		b.Fatalf("Failed to add watch: %v", err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	// This should reuse event objects from the pool
	for i := 0; i < b.N; i++ {
		mgr.NotifyPut([]byte("pool-key"), nil, []byte("value"), int64(i), int64(i))
	}
}

// BenchmarkWatchManagerStats benchmarks statistics collection.
func BenchmarkWatchManagerStats(b *testing.B) {
	mgr := NewManager()

	// Create some watches
	for i := 0; i < 100; i++ {
		stream := newMockManagerWatchStream()
		key := []byte(fmt.Sprintf("stats-key-%d", i))
		_, err := mgr.Add(key, nil, 0, WatchFilters{}, stream)
		if err != nil {
			b.Fatalf("Failed to add watch: %v", err)
		}
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = mgr.GetStats()
	}
}

// BenchmarkWatchManagerScalability tests scalability with increasing watch counts.
func BenchmarkWatchManagerScalability(b *testing.B) {
	watchCounts := []int{10, 100, 1000, 10000}

	for _, count := range watchCounts {
		b.Run(fmt.Sprintf("Watches-%d", count), func(b *testing.B) {
			mgr := NewManager()

			// Create specified number of watches
			for i := 0; i < count; i++ {
				stream := newMockManagerWatchStream()
				key := []byte(fmt.Sprintf("scale-key-%d", i))
				_, err := mgr.Add(key, nil, 0, WatchFilters{}, stream)
				if err != nil {
					b.Fatalf("Failed to add watch: %v", err)
				}
			}

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				// Notify will match one watch
				key := []byte(fmt.Sprintf("scale-key-%d", i%count))
				mgr.NotifyPut(key, nil, []byte("value"), int64(i), int64(i))
			}
		})
	}
}

// BenchmarkWatchManagerFiltering benchmarks event filtering performance.
func BenchmarkWatchManagerFiltering(b *testing.B) {
	mgr := NewManager()
	stream := newMockManagerWatchStream()

	// Create watch with PUT filter
	filters := WatchFilters{NoPut: true}
	_, err := mgr.Add([]byte("filter-key"), nil, 0, filters, stream)
	if err != nil {
		b.Fatalf("Failed to add watch: %v", err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// These PUT events should be filtered out
		mgr.NotifyPut([]byte("filter-key"), nil, []byte("value"), int64(i), int64(i))
	}
}

// BenchmarkWatchManagerDifferentKeyTypes benchmarks performance with different key patterns.
func BenchmarkWatchManagerDifferentKeyTypes(b *testing.B) {
	keyPatterns := []struct {
		name string
		key  []byte
	}{
		{"ShortKey", []byte("k")},
		{"MediumKey", []byte("medium-length-key-name")},
		{"LongKey", []byte("/very/long/hierarchical/key/with/many/segments/path/name")},
		{"UUIDKey", []byte("550e8400-e29b-41d4-a716-446655440000")},
	}

	for _, pattern := range keyPatterns {
		b.Run(pattern.name, func(b *testing.B) {
			mgr := NewManager()
			stream := newMockManagerWatchStream()

			_, err := mgr.Add(pattern.key, nil, 0, WatchFilters{}, stream)
			if err != nil {
				b.Fatalf("Failed to add watch: %v", err)
			}

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				mgr.NotifyPut(pattern.key, nil, []byte("value"), int64(i), int64(i))
			}
		})
	}
}

// BenchmarkCompareWatchOverhead compares overhead of watch system.
func BenchmarkCompareWatchOverhead(b *testing.B) {
	b.Run("NoWatchSystem", func(b *testing.B) {
		// Baseline: just function call overhead
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			// Simulate minimal work
			_ = i + 1
		}
	})

	b.Run("WithWatchSystemNoWatches", func(b *testing.B) {
		mgr := NewManager()
		b.ResetTimer()
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			mgr.NotifyPut([]byte("key"), nil, []byte("value"), int64(i), int64(i))
		}
	})

	b.Run("WithWatchSystemOneWatch", func(b *testing.B) {
		mgr := NewManager()
		stream := newMockManagerWatchStream()
		_, err := mgr.Add([]byte("key"), nil, 0, WatchFilters{}, stream)
		if err != nil {
			b.Fatalf("Failed to add watch: %v", err)
		}

		b.ResetTimer()
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			mgr.NotifyPut([]byte("key"), nil, []byte("value"), int64(i), int64(i))
		}
	})
}
