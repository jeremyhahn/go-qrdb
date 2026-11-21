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

package routing

import (
	"sync"
	"testing"
)

func TestNewRouter_Default(t *testing.T) {
	r := New()
	got := r.Route("anything", 0)
	if got != 1 {
		t.Fatalf("New().Route(anything, 0) = %d, want 1", got)
	}
}

func TestNewRouter_Default_SingleShardModulo(t *testing.T) {
	r := New()
	// With a single shard [1], any id should route to shard 1.
	got := r.Route("users", 42)
	if got != 1 {
		t.Fatalf("New().Route(users, 42) = %d, want 1", got)
	}
}

func TestNewWithShards_CustomList_ZeroID(t *testing.T) {
	r := NewWithShards([]uint64{10, 20, 30})
	got := r.Route("orders", 0)
	if got != 10 {
		t.Fatalf("Route(orders, 0) = %d, want 10 (first shard)", got)
	}
}

func TestNewWithShards_NilDefaults(t *testing.T) {
	r := NewWithShards(nil)
	got := r.Route("users", 0)
	if got != 1 {
		t.Fatalf("NewWithShards(nil).Route(users, 0) = %d, want 1", got)
	}
}

func TestNewWithShards_NilDefaults_WithID(t *testing.T) {
	r := NewWithShards(nil)
	got := r.Route("users", 99)
	if got != 1 {
		t.Fatalf("NewWithShards(nil).Route(users, 99) = %d, want 1", got)
	}
}

func TestNewWithShards_CopiesInput(t *testing.T) {
	original := []uint64{5, 10, 15}
	r := NewWithShards(original)

	// Mutate the original slice after construction.
	original[0] = 999

	got := r.Route("test", 0)
	if got != 5 {
		t.Fatalf("Route(test, 0) = %d, want 5 (constructor should copy input)", got)
	}
}

func TestRoute_EntityPinningIgnoresID(t *testing.T) {
	r := NewWithShards([]uint64{10, 20, 30})
	r.SetEntityShard("accounts", 42)

	// The id parameter should be irrelevant for pinned entities.
	for _, id := range []uint64{0, 1, 2, 100, 999} {
		got := r.Route("accounts", id)
		if got != 42 {
			t.Fatalf("Route(accounts, %d) = %d, want 42 (pinned entity ignores id)", id, got)
		}
	}
}

func TestRoute_ModuloFallback(t *testing.T) {
	shards := []uint64{100, 200, 300, 400, 500}
	r := NewWithShards(shards)

	tests := []struct {
		id   uint64
		want uint64
	}{
		{id: 1, want: 200},  // 1 % 5 = 1 -> shards[1]
		{id: 2, want: 300},  // 2 % 5 = 2 -> shards[2]
		{id: 5, want: 100},  // 5 % 5 = 0 -> shards[0]
		{id: 7, want: 300},  // 7 % 5 = 2 -> shards[2]
		{id: 13, want: 400}, // 13 % 5 = 3 -> shards[3]
	}

	for _, tc := range tests {
		got := r.Route("unpinned", tc.id)
		if got != tc.want {
			t.Errorf("Route(unpinned, %d) = %d, want %d", tc.id, got, tc.want)
		}
	}
}

func TestSetEntityShard_Overwrite(t *testing.T) {
	r := New()
	r.SetEntityShard("logs", 5)
	got := r.Route("logs", 0)
	if got != 5 {
		t.Fatalf("Route(logs, 0) = %d, want 5", got)
	}

	r.SetEntityShard("logs", 99)
	got = r.Route("logs", 0)
	if got != 99 {
		t.Fatalf("Route(logs, 0) after overwrite = %d, want 99", got)
	}
}

func TestSetEntityShard_Overwrite_PreservesOtherPins(t *testing.T) {
	r := New()
	r.SetEntityShard("logs", 5)
	r.SetEntityShard("metrics", 10)

	r.SetEntityShard("logs", 99)

	if got := r.Route("metrics", 0); got != 10 {
		t.Fatalf("Route(metrics, 0) = %d, want 10 (other pin should be preserved)", got)
	}
}

func TestSetEntityShard_MultiplePins(t *testing.T) {
	r := NewWithShards([]uint64{10, 20, 30})
	r.SetEntityShard("users", 100)
	r.SetEntityShard("orders", 200)
	r.SetEntityShard("products", 300)

	tests := []struct {
		entityType string
		want       uint64
	}{
		{"users", 100},
		{"orders", 200},
		{"products", 300},
	}

	for _, tc := range tests {
		got := r.Route(tc.entityType, 42)
		if got != tc.want {
			t.Errorf("Route(%s, 42) = %d, want %d", tc.entityType, got, tc.want)
		}
	}
}

func TestSetEntityShard_MultiplePins_UnpinnedStillRoutes(t *testing.T) {
	r := NewWithShards([]uint64{10, 20, 30})
	r.SetEntityShard("users", 100)
	r.SetEntityShard("orders", 200)

	// Unpinned entity should still use modulo fallback.
	got := r.Route("events", 1) // 1 % 3 = 1 -> shards[1] = 20
	if got != 20 {
		t.Fatalf("Route(events, 1) = %d, want 20 (unpinned modulo fallback)", got)
	}
}

func TestSetShards_Replaces(t *testing.T) {
	r := NewWithShards([]uint64{10, 20, 30})
	r.SetShards([]uint64{100, 200})

	got := r.Route("test", 1) // 1 % 2 = 1 -> shards[1] = 200
	if got != 200 {
		t.Fatalf("Route(test, 1) = %d, want 200 (after SetShards replacement)", got)
	}
}

func TestSetShards_Replaces_FirstShard(t *testing.T) {
	r := NewWithShards([]uint64{10, 20, 30})
	r.SetShards([]uint64{100, 200})

	got := r.Route("test", 0)
	if got != 100 {
		t.Fatalf("Route(test, 0) = %d, want 100 (first shard after replacement)", got)
	}
}

func TestSetShards_NilResetsToDefault(t *testing.T) {
	r := NewWithShards([]uint64{10, 20, 30})
	r.SetShards(nil)

	got := r.Route("test", 0)
	if got != 1 {
		t.Fatalf("Route(test, 0) = %d, want 1 (nil resets to default)", got)
	}
}

func TestSetShards_NilResetsToDefault_WithID(t *testing.T) {
	r := NewWithShards([]uint64{10, 20, 30})
	r.SetShards(nil)

	got := r.Route("test", 50)
	if got != 1 {
		t.Fatalf("Route(test, 50) = %d, want 1 (nil resets to default, single shard)", got)
	}
}

func TestSetShards_CopiesInput(t *testing.T) {
	r := New()
	input := []uint64{10, 20, 30}
	r.SetShards(input)

	// Mutate the input slice after SetShards.
	input[0] = 999

	got := r.Route("test", 0)
	if got != 10 {
		t.Fatalf("Route(test, 0) = %d, want 10 (SetShards should copy input)", got)
	}
}

func TestRoute_ConcurrentSafe(t *testing.T) {
	r := NewWithShards([]uint64{10, 20, 30, 40, 50})

	const goroutines = 50
	const iterations = 1000

	var wg sync.WaitGroup
	wg.Add(goroutines * 3)

	// Concurrent readers.
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_ = r.Route("entity", uint64(j))
			}
		}()
	}

	// Concurrent SetEntityShard writers.
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				r.SetEntityShard("entity", uint64(idx*1000+j))
			}
		}(i)
	}

	// Concurrent SetShards writers.
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				r.SetShards([]uint64{uint64(idx*100 + j + 1), uint64(idx*100 + j + 2)})
			}
		}(i)
	}

	wg.Wait()
}
