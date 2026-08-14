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
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"sync/atomic"
)

// CacheStats holds counters that describe cache activity.
type CacheStats struct {
	// Hits is the number of successful cache lookups.
	Hits int64

	// Misses is the number of lookups that did not find a cached entry.
	Misses int64

	// Evictions is the number of entries evicted to make room for new ones.
	Evictions int64

	// Size is the current number of entries in the cache.
	Size int64
}

// cacheEntry holds a single cached embedding together with its LRU list node.
type cacheEntry struct {
	key       string
	embedding []float32
	prev      *cacheEntry
	next      *cacheEntry
}

// Cache is a fixed-capacity LRU cache for embedding vectors keyed by
// (model, text) pairs. Reads use a shared RWMutex lock; writes use an
// exclusive lock. Stats counters are updated with atomic operations.
type Cache struct {
	mu       sync.RWMutex
	capacity int
	items    map[string]*cacheEntry
	head     *cacheEntry // most-recently-used sentinel
	tail     *cacheEntry // least-recently-used sentinel

	hits      atomic.Int64
	misses    atomic.Int64
	evictions atomic.Int64
	size      atomic.Int64
}

// NewCache creates a new LRU Cache with the given capacity.
// It returns CacheCapacityError if capacity is not positive.
func NewCache(capacity int) (*Cache, error) {
	if capacity <= 0 {
		return nil, &CacheCapacityError{Capacity: capacity}
	}
	c := &Cache{
		capacity: capacity,
		items:    make(map[string]*cacheEntry, capacity),
		head:     &cacheEntry{},
		tail:     &cacheEntry{},
	}
	c.head.next = c.tail
	c.tail.prev = c.head
	return c, nil
}

// cacheKey returns a 32-character hex string derived from sha256(model + NUL + text).
// Using a fixed-width key keeps map memory predictable.
func cacheKey(model, text string) string {
	h := sha256.New()
	h.Write([]byte(model))
	h.Write([]byte{0x00})
	h.Write([]byte(text))
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:16]) // 16 bytes → 32 hex chars
}

// Get returns the cached embedding for (model, text) and true if present,
// or nil and false if not found.
func (c *Cache) Get(model, text string) ([]float32, bool) {
	k := cacheKey(model, text)

	c.mu.RLock()
	_, ok := c.items[k]
	c.mu.RUnlock()

	if !ok {
		c.misses.Add(1)
		return nil, false
	}

	// Promote to MRU under write lock.
	c.mu.Lock()
	// Re-check presence; another goroutine may have evicted this entry between
	// the RUnlock and Lock.
	entry, ok := c.items[k]
	if !ok {
		c.mu.Unlock()
		c.misses.Add(1)
		return nil, false
	}
	c.detach(entry)
	c.insertFront(entry)
	c.mu.Unlock()

	c.hits.Add(1)
	return entry.embedding, true
}

// Put stores an embedding for (model, text), evicting the least-recently-used
// entry if the cache is at capacity.
func (c *Cache) Put(model, text string, embedding []float32) {
	k := cacheKey(model, text)

	c.mu.Lock()
	defer c.mu.Unlock()

	if entry, ok := c.items[k]; ok {
		// Update existing entry and move to front.
		entry.embedding = embedding
		c.detach(entry)
		c.insertFront(entry)
		return
	}

	// Evict LRU entry if at capacity.
	if len(c.items) >= c.capacity {
		lru := c.tail.prev
		if lru != c.head {
			c.detach(lru)
			delete(c.items, lru.key)
			c.size.Add(-1)
			c.evictions.Add(1)
		}
	}

	entry := &cacheEntry{key: k, embedding: embedding}
	c.items[k] = entry
	c.insertFront(entry)
	c.size.Add(1)
}

// Stats returns a snapshot of the cache activity counters.
func (c *Cache) Stats() CacheStats {
	return CacheStats{
		Hits:      c.hits.Load(),
		Misses:    c.misses.Load(),
		Evictions: c.evictions.Load(),
		Size:      c.size.Load(),
	}
}

// Clear removes all entries from the cache and resets all counters.
func (c *Cache) Clear() {
	c.mu.Lock()
	c.items = make(map[string]*cacheEntry, c.capacity)
	c.head.next = c.tail
	c.tail.prev = c.head
	c.mu.Unlock()

	c.hits.Store(0)
	c.misses.Store(0)
	c.evictions.Store(0)
	c.size.Store(0)
}

// detach removes entry from the doubly-linked list without altering the map.
// Callers must hold c.mu (write lock).
func (c *Cache) detach(e *cacheEntry) {
	e.prev.next = e.next
	e.next.prev = e.prev
}

// insertFront places entry immediately after the head sentinel (MRU position).
// Callers must hold c.mu (write lock).
func (c *Cache) insertFront(e *cacheEntry) {
	e.next = c.head.next
	e.prev = c.head
	c.head.next.prev = e
	c.head.next = e
}
