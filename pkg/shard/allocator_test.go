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

import (
	"sync"
	"testing"
)

func TestAllocatorStartsAt1000(t *testing.T) {
	a := NewAllocator()
	got := a.Allocate()
	if got != UserMin {
		t.Errorf("first Allocate() = %d, want %d (UserMin)", got, UserMin)
	}
}

func TestAllocatorMonotonicallyIncreasing(t *testing.T) {
	a := NewAllocator()
	const iterations = 100
	prev := a.Allocate()
	for i := 1; i < iterations; i++ {
		next := a.Allocate()
		if next <= prev {
			t.Errorf("Allocate()[%d] = %d is not greater than Allocate()[%d] = %d", i, next, i-1, prev)
		}
		prev = next
	}
}

func TestAllocatorConcurrentSafety(t *testing.T) {
	a := NewAllocator()
	const goroutines = 50
	const allocsPerGoroutine = 100
	total := goroutines * allocsPerGoroutine

	seen := make(map[uint64]struct{}, total)
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()
			ids := make([]uint64, allocsPerGoroutine)
			for i := range allocsPerGoroutine {
				ids[i] = a.Allocate()
			}
			mu.Lock()
			for _, id := range ids {
				if _, exists := seen[id]; exists {
					t.Errorf("duplicate shard ID allocated: %d", id)
				}
				seen[id] = struct{}{}
			}
			mu.Unlock()
		}()
	}

	wg.Wait()

	if len(seen) != total {
		t.Errorf("expected %d unique IDs, got %d", total, len(seen))
	}
}

func TestNewAllocatorFrom(t *testing.T) {
	tests := []struct {
		name      string
		start     uint64
		wantFirst uint64
	}{
		{
			name:      "user_range",
			start:     UserMin,
			wantFirst: UserMin,
		},
		{
			name:      "arbitrary_offset",
			start:     5000,
			wantFirst: 5000,
		},
		{
			name:      "one",
			start:     1,
			wantFirst: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := NewAllocatorFrom(tt.start)
			got := a.Allocate()
			if got != tt.wantFirst {
				t.Errorf("NewAllocatorFrom(%d).Allocate() = %d, want %d", tt.start, got, tt.wantFirst)
			}
			// Second allocation must be one higher.
			second := a.Allocate()
			if second != tt.wantFirst+1 {
				t.Errorf("second Allocate() = %d, want %d", second, tt.wantFirst+1)
			}
		})
	}
}

func TestNewAllocatorFromPanicsOnZero(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("NewAllocatorFrom(0) did not panic, want panic")
		}
	}()
	NewAllocatorFrom(0)
}

func TestAllocatorNext(t *testing.T) {
	a := NewAllocator()

	// Next() must return UserMin before any allocation.
	if got := a.Next(); got != UserMin {
		t.Errorf("Next() before Allocate() = %d, want %d", got, UserMin)
	}

	// Allocate one; Next must advance by one.
	first := a.Allocate()
	if first != UserMin {
		t.Errorf("first Allocate() = %d, want %d", first, UserMin)
	}
	if got := a.Next(); got != UserMin+1 {
		t.Errorf("Next() after one Allocate() = %d, want %d", got, UserMin+1)
	}

	// Next() must not consume IDs — repeated calls return the same value.
	n1 := a.Next()
	n2 := a.Next()
	if n1 != n2 {
		t.Errorf("consecutive Next() calls returned different values: %d vs %d", n1, n2)
	}
}

func TestAllocatorNextEqualsFirstAllocate(t *testing.T) {
	a := NewAllocatorFrom(2000)
	peeked := a.Next()
	allocated := a.Allocate()
	if peeked != allocated {
		t.Errorf("Next() = %d but Allocate() = %d; Next should equal the next allocated ID", peeked, allocated)
	}
}

func TestAllocatorAdvanceTo(t *testing.T) {
	t.Run("advances_when_target_is_higher", func(t *testing.T) {
		a := NewAllocator()
		a.AdvanceTo(5000)
		if got := a.Next(); got != 5000 {
			t.Errorf("Next() after AdvanceTo(5000) = %d, want 5000", got)
		}
		// First allocation after advance must yield exactly 5000.
		if got := a.Allocate(); got != 5000 {
			t.Errorf("Allocate() after AdvanceTo(5000) = %d, want 5000", got)
		}
	})

	t.Run("noop_when_target_is_lower", func(t *testing.T) {
		a := NewAllocatorFrom(9000)
		a.AdvanceTo(100)
		// Counter must not regress.
		if got := a.Next(); got != 9000 {
			t.Errorf("Next() after AdvanceTo(100) on counter=9000 = %d, want 9000", got)
		}
	})

	t.Run("noop_when_target_equals_current", func(t *testing.T) {
		a := NewAllocatorFrom(3000)
		a.AdvanceTo(3000)
		if got := a.Next(); got != 3000 {
			t.Errorf("Next() after AdvanceTo(equal) = %d, want 3000", got)
		}
	})

	t.Run("concurrent_advance_and_allocate", func(t *testing.T) {
		a := NewAllocator()
		const target = 2000
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			a.AdvanceTo(target)
		}()
		go func() {
			defer wg.Done()
			_ = a.Allocate()
		}()
		wg.Wait()
		// After both finish, Next must be >= target.
		if got := a.Next(); got < target {
			t.Errorf("Next() after concurrent AdvanceTo(%d) = %d, want >= %d", target, got, target)
		}
	})
}
