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
	"fmt"
	"testing"
)

func TestHashRouter_Route(t *testing.T) {
	r := NewHashRouter(4)

	// Same key must always route to the same partition.
	key := []byte("test-key")
	first := r.Route(key)
	for i := 0; i < 100; i++ {
		if got := r.Route(key); got != first {
			t.Fatalf("Route(%q) = %d on iteration %d, want %d", key, got, i, first)
		}
	}

	// Result must be within bounds.
	if first >= 4 {
		t.Errorf("Route returned %d, want < 4", first)
	}
}

func TestHashRouter_PartitionCount(t *testing.T) {
	r := NewHashRouter(16)
	if got := r.PartitionCount(); got != 16 {
		t.Errorf("PartitionCount() = %d, want 16", got)
	}
}

func TestHashRouter_Distribution(t *testing.T) {
	const partitions = 8
	const keyCount = 10000
	r := NewHashRouter(partitions)

	counts := make([]int, partitions)
	for i := 0; i < keyCount; i++ {
		key := []byte(fmt.Sprintf("key-%06d", i))
		id := r.Route(key)
		if id >= partitions {
			t.Fatalf("Route returned %d, want < %d", id, partitions)
		}
		counts[id]++
	}

	// Each partition should get a reasonable share of keys.
	// With 10000 keys and 8 partitions, average is 1250. Allow 50% deviation.
	minExpected := keyCount / partitions / 2
	for i, count := range counts {
		if count < minExpected {
			t.Errorf("partition %d has %d keys, expected at least %d", i, count, minExpected)
		}
	}
}

func TestHashRouter_EmptyKey(t *testing.T) {
	r := NewHashRouter(4)
	// Empty key should not panic and should produce a valid partition.
	id := r.Route([]byte{})
	if id >= 4 {
		t.Errorf("Route(empty) = %d, want < 4", id)
	}
}

func TestHashRouter_SinglePartition(t *testing.T) {
	r := NewHashRouter(1)
	// All keys must route to partition 0.
	for i := 0; i < 100; i++ {
		key := []byte(fmt.Sprintf("key-%d", i))
		if got := r.Route(key); got != 0 {
			t.Errorf("Route(%q) = %d, want 0 (single partition)", key, got)
		}
	}
}

func TestEntityRouter_Route_WithMapping(t *testing.T) {
	fallback := NewHashRouter(4)
	r := NewEntityRouter(4, fallback)

	r.MapPrefix("users/", 1)
	r.MapPrefix("orders/", 2)

	// Mapped prefixes should route to their assigned partitions.
	if got := r.Route([]byte("users/alice")); got != 1 {
		t.Errorf("Route(users/alice) = %d, want 1", got)
	}
	if got := r.Route([]byte("orders/123")); got != 2 {
		t.Errorf("Route(orders/123) = %d, want 2", got)
	}

	// Unmapped key should fall through to the hash router.
	unmappedID := r.Route([]byte("settings/theme"))
	if unmappedID >= 4 {
		t.Errorf("Route(settings/theme) = %d, want < 4", unmappedID)
	}
}

func TestEntityRouter_Route_LongestPrefixMatch(t *testing.T) {
	fallback := NewHashRouter(4)
	r := NewEntityRouter(4, fallback)

	r.MapPrefix("data/", 0)
	r.MapPrefix("data/users/", 1)

	// Should match the longer prefix.
	if got := r.Route([]byte("data/users/alice")); got != 1 {
		t.Errorf("Route(data/users/alice) = %d, want 1", got)
	}

	// Should match the shorter prefix.
	if got := r.Route([]byte("data/config")); got != 0 {
		t.Errorf("Route(data/config) = %d, want 0", got)
	}
}

func TestEntityRouter_PartitionCount(t *testing.T) {
	fallback := NewHashRouter(8)
	r := NewEntityRouter(8, fallback)
	if got := r.PartitionCount(); got != 8 {
		t.Errorf("PartitionCount() = %d, want 8", got)
	}
}

func TestEntityRouter_MapPrefix_Overwrite(t *testing.T) {
	fallback := NewHashRouter(4)
	r := NewEntityRouter(4, fallback)

	r.MapPrefix("users/", 1)
	r.MapPrefix("users/", 3) // overwrite

	if got := r.Route([]byte("users/alice")); got != 3 {
		t.Errorf("Route(users/alice) = %d, want 3 after overwrite", got)
	}
}

func TestEntityRouter_FallbackForUnmappedKeys(t *testing.T) {
	fallback := NewHashRouter(4)
	r := NewEntityRouter(4, fallback)

	// No mappings: everything goes to fallback.
	key := []byte("anything")
	entityID := r.Route(key)
	hashID := fallback.Route(key)
	if entityID != hashID {
		t.Errorf("unmapped Route() = %d, want fallback %d", entityID, hashID)
	}
}
