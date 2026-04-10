//go:build qrdb_vector

// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package embedding

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

func mustCache(t *testing.T, capacity int) *Cache {
	t.Helper()
	c, err := NewCache(capacity)
	if err != nil {
		t.Fatalf("NewCache(%d): %v", capacity, err)
	}
	return c
}

// ---------------------------------------------------------------------------
// Constructor tests
// ---------------------------------------------------------------------------

func TestNewCache_ValidCapacity(t *testing.T) {
	c, err := NewCache(10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil cache")
	}
}

func TestNewCache_ZeroCapacity(t *testing.T) {
	_, err := NewCache(0)
	if err == nil {
		t.Fatal("expected error for capacity=0")
	}
	var capErr *CacheCapacityError
	if !errors.As(err, &capErr) {
		t.Errorf("expected CacheCapacityError, got %T: %v", err, err)
	}
	if capErr.Capacity != 0 {
		t.Errorf("expected Capacity=0, got %d", capErr.Capacity)
	}
}

func TestNewCache_NegativeCapacity(t *testing.T) {
	_, err := NewCache(-1)
	if err == nil {
		t.Fatal("expected error for negative capacity")
	}
	var capErr *CacheCapacityError
	if !errors.As(err, &capErr) {
		t.Errorf("expected CacheCapacityError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Get / Put tests
// ---------------------------------------------------------------------------

func TestCache_PutAndGet_Hit(t *testing.T) {
	c := mustCache(t, 10)
	vec := []float32{0.1, 0.2, 0.3}
	c.Put("m1", "hello", vec)

	got, ok := c.Get("m1", "hello")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if len(got) != len(vec) {
		t.Fatalf("expected %d dims, got %d", len(vec), len(got))
	}
	for i, v := range vec {
		if got[i] != v {
			t.Errorf("dim[%d]: expected %f, got %f", i, v, got[i])
		}
	}
}

func TestCache_Get_Miss(t *testing.T) {
	c := mustCache(t, 10)
	got, ok := c.Get("m1", "nonexistent")
	if ok {
		t.Error("expected cache miss")
	}
	if got != nil {
		t.Error("expected nil embedding on miss")
	}
}

func TestCache_ModelKeyIsolation(t *testing.T) {
	c := mustCache(t, 10)
	c.Put("model-a", "text", []float32{1.0})
	c.Put("model-b", "text", []float32{2.0})

	a, ok := c.Get("model-a", "text")
	if !ok || a[0] != 1.0 {
		t.Error("model-a entry incorrect")
	}
	b, ok := c.Get("model-b", "text")
	if !ok || b[0] != 2.0 {
		t.Error("model-b entry incorrect")
	}
}

func TestCache_Update_ExistingEntry(t *testing.T) {
	c := mustCache(t, 10)
	c.Put("m", "k", []float32{1.0})
	c.Put("m", "k", []float32{9.9}) // update

	got, ok := c.Get("m", "k")
	if !ok {
		t.Fatal("expected hit after update")
	}
	if got[0] != 9.9 {
		t.Errorf("expected updated value 9.9, got %f", got[0])
	}
	// Size should still be 1.
	if c.Stats().Size != 1 {
		t.Errorf("expected size=1 after update, got %d", c.Stats().Size)
	}
}

// ---------------------------------------------------------------------------
// LRU eviction tests
// ---------------------------------------------------------------------------

func TestCache_LRUEviction_AtCapacity(t *testing.T) {
	c := mustCache(t, 3)
	c.Put("m", "a", []float32{1})
	c.Put("m", "b", []float32{2})
	c.Put("m", "c", []float32{3})

	// "a" is LRU; inserting "d" must evict "a".
	c.Put("m", "d", []float32{4})

	if _, ok := c.Get("m", "a"); ok {
		t.Error("expected 'a' to be evicted")
	}
	if _, ok := c.Get("m", "d"); !ok {
		t.Error("expected 'd' to be present")
	}
}

func TestCache_LRUEviction_PromotesOnGet(t *testing.T) {
	c := mustCache(t, 3)
	c.Put("m", "a", []float32{1})
	c.Put("m", "b", []float32{2})
	c.Put("m", "c", []float32{3})

	// Access "a" to promote it; "b" becomes LRU.
	c.Get("m", "a")

	// Adding "d" should evict "b", not "a".
	c.Put("m", "d", []float32{4})

	if _, ok := c.Get("m", "b"); ok {
		t.Error("expected 'b' to be evicted after 'a' was promoted")
	}
	if _, ok := c.Get("m", "a"); !ok {
		t.Error("expected 'a' to still be present after promotion")
	}
}

func TestCache_LRUEviction_EvictionCounter(t *testing.T) {
	c := mustCache(t, 2)
	c.Put("m", "a", []float32{1})
	c.Put("m", "b", []float32{2})
	c.Put("m", "c", []float32{3}) // evicts "a"
	c.Put("m", "d", []float32{4}) // evicts "b"

	stats := c.Stats()
	if stats.Evictions != 2 {
		t.Errorf("expected 2 evictions, got %d", stats.Evictions)
	}
}

// ---------------------------------------------------------------------------
// Stats tests
// ---------------------------------------------------------------------------

func TestCache_Stats_HitsAndMisses(t *testing.T) {
	c := mustCache(t, 10)
	c.Put("m", "k", []float32{1})

	c.Get("m", "k")    // hit
	c.Get("m", "k")    // hit
	c.Get("m", "nope") // miss

	stats := c.Stats()
	if stats.Hits != 2 {
		t.Errorf("expected 2 hits, got %d", stats.Hits)
	}
	if stats.Misses != 1 {
		t.Errorf("expected 1 miss, got %d", stats.Misses)
	}
	if stats.Size != 1 {
		t.Errorf("expected size=1, got %d", stats.Size)
	}
}

func TestCache_Stats_AfterClear(t *testing.T) {
	c := mustCache(t, 10)
	c.Put("m", "k", []float32{1})
	c.Get("m", "k")
	c.Clear()

	stats := c.Stats()
	if stats.Hits != 0 || stats.Misses != 0 || stats.Evictions != 0 || stats.Size != 0 {
		t.Errorf("expected all zeros after Clear, got %+v", stats)
	}
}

// ---------------------------------------------------------------------------
// Clear tests
// ---------------------------------------------------------------------------

func TestCache_Clear_RemovesAllEntries(t *testing.T) {
	c := mustCache(t, 10)
	for i := 0; i < 5; i++ {
		c.Put("m", fmt.Sprintf("k%d", i), []float32{float32(i)})
	}
	c.Clear()

	for i := 0; i < 5; i++ {
		if _, ok := c.Get("m", fmt.Sprintf("k%d", i)); ok {
			t.Errorf("expected key k%d to be gone after Clear", i)
		}
	}
	if c.Stats().Size != 0 {
		t.Errorf("expected size=0 after Clear, got %d", c.Stats().Size)
	}
}

// ---------------------------------------------------------------------------
// Concurrency test
// ---------------------------------------------------------------------------

func TestCache_ConcurrentReadWrite(t *testing.T) {
	c := mustCache(t, 50)
	const goroutines = 20
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		g := g
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				key := fmt.Sprintf("g%d-k%d", g, i%10)
				c.Put("model", key, []float32{float32(i)})
				c.Get("model", key)
			}
		}()
	}
	wg.Wait()

	// Just verify the cache is in a consistent state (no panic, valid size).
	stats := c.Stats()
	if stats.Size < 0 || stats.Size > 50 {
		t.Errorf("unexpected cache size after concurrent access: %d", stats.Size)
	}
}
