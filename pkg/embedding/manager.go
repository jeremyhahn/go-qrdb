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
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// ManagerInterface defines the contract that Manager satisfies.
// Callers that need to substitute a mock or alternative implementation
// should depend on this interface rather than *Manager directly.
type ManagerInterface interface {
	Embed(ctx context.Context, text string) ([]float32, error)
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
	Dimensions() (int, error)
	GetProvider(name string) (Provider, error)
	GetStats() ManagerStats
}

// Compile-time assertion that Manager satisfies ManagerInterface.
var _ ManagerInterface = (*Manager)(nil)

// Manager manages multiple embedding providers with caching.
// Providers are registered by name; one provider is designated as the default.
// A single shared LRU cache spans all providers.
type Manager struct {
	providers map[string]Provider
	cache     *managerCache
	config    Config
	mu        sync.RWMutex
}

// NewManager creates a new Manager from the given Config.
// It returns an error if the config fails validation.
func NewManager(config Config) (*Manager, error) {
	if err := validateManagerConfig(config); err != nil {
		return nil, err
	}

	m := &Manager{
		providers: make(map[string]Provider),
		config:    config,
	}

	if config.CacheEnabled {
		var err error
		m.cache, err = newManagerCache(config.CacheSize, config.CacheTTL, config.CacheCompress)
		if err != nil {
			return nil, &CacheCapacityError{Capacity: config.CacheSize}
		}
	}

	return m, nil
}

// validateManagerConfig validates the Config used to create a Manager.
func validateManagerConfig(c Config) error {
	if c.Provider == "" {
		return ErrProviderRequired
	}
	switch c.Provider {
	case "openai", "ollama", "lmstudio", "voyage":
		// valid
	default:
		return ErrProviderInvalid
	}
	if c.APIURL == "" {
		return ErrAPIURLRequired
	}
	if c.Provider == "voyage" && c.APIKey == "" {
		return ErrAPIKeyRequired
	}
	if c.Model == "" {
		return ErrModelRequired
	}
	if c.Dimensions < 0 {
		return ErrDimensionsPositive
	}
	if c.MaxBatchSize < 0 {
		return ErrMaxBatchSizePositive
	}
	if c.Timeout < 0 {
		return ErrTimeoutPositive
	}
	return nil
}

// RegisterProvider registers a new embedding provider under the given name.
// It returns ProviderAlreadyRegisteredError if the name is already in use.
func (m *Manager) RegisterProvider(name string, provider Provider) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.providers[name]; exists {
		return &ProviderAlreadyRegisteredError{Name: name}
	}

	m.providers[name] = provider
	return nil
}

// GetProvider returns the registered provider for the given name.
// It returns ProviderNotFoundError if the name is not registered.
func (m *Manager) GetProvider(name string) (Provider, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	provider, exists := m.providers[name]
	if !exists {
		return nil, &ProviderNotFoundError{Name: name}
	}

	return provider, nil
}

// Embed generates an embedding for a single text using the configured default
// provider. It checks the cache before calling the provider and stores
// successful results in the cache.
func (m *Manager) Embed(ctx context.Context, text string) ([]float32, error) {
	if m.cache != nil {
		if cached, ok := m.cache.get(text); ok {
			return cached, nil
		}
	}

	provider, err := m.GetProvider(m.config.Provider)
	if err != nil {
		return nil, err
	}

	model := m.config.Model
	result, err := provider.Embed(ctx, []string{text}, model)
	if err != nil {
		return nil, err
	}

	if len(result.Embeddings) == 0 {
		return nil, &EmptyResponseError{}
	}

	embedding := result.Embeddings[0]

	if m.cache != nil {
		m.cache.put(text, embedding)
	}

	return embedding, nil
}

// EmbedBatch generates embeddings for multiple texts using the configured
// default provider. Cache-hit texts are returned from cache; the remainder
// are sent to the provider in a single batch request.
func (m *Manager) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, &InvalidInputError{}
	}

	provider, err := m.GetProvider(m.config.Provider)
	if err != nil {
		return nil, err
	}

	results := make([][]float32, len(texts))

	var uncachedTexts []string
	var uncachedIndices []int

	if m.cache != nil {
		for i, text := range texts {
			if cached, ok := m.cache.get(text); ok {
				results[i] = cached
			} else {
				uncachedTexts = append(uncachedTexts, text)
				uncachedIndices = append(uncachedIndices, i)
			}
		}
	} else {
		uncachedTexts = texts
		uncachedIndices = make([]int, len(texts))
		for i := range texts {
			uncachedIndices[i] = i
		}
	}

	if len(uncachedTexts) > 0 {
		model := m.config.Model
		batchResult, err := provider.Embed(ctx, uncachedTexts, model)
		if err != nil {
			return nil, err
		}

		for i, embedding := range batchResult.Embeddings {
			idx := uncachedIndices[i]
			results[idx] = embedding

			if m.cache != nil {
				m.cache.put(uncachedTexts[i], embedding)
			}
		}
	}

	return results, nil
}

// Dimensions returns the embedding vector dimensionality reported by the
// configured default provider. Returns ProviderNotFoundError if the default
// provider has not been registered.
func (m *Manager) Dimensions() (int, error) {
	provider, err := m.GetProvider(m.config.Provider)
	if err != nil {
		return 0, err
	}
	return provider.Dimensions(), nil
}

// Close closes all registered providers and clears the cache.
// If multiple providers fail to close, the errors are collected and returned
// as a single CloseError.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []error
	for name, provider := range m.providers {
		if err := provider.Close(); err != nil {
			errs = append(errs, fmt.Errorf("provider %s: %w", name, err))
		}
	}

	if m.cache != nil {
		m.cache.clear()
	}

	if len(errs) > 0 {
		return &CloseError{Errors: errs}
	}

	return nil
}

// GetStats returns a snapshot of Manager activity counters.
func (m *Manager) GetStats() ManagerStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := ManagerStats{
		ProvidersRegistered: len(m.providers),
		ProviderNames:       make([]string, 0, len(m.providers)),
		DefaultProvider:     m.config.Provider,
		CacheEnabled:        m.cache != nil,
	}

	for name := range m.providers {
		stats.ProviderNames = append(stats.ProviderNames, name)
	}

	if m.cache != nil {
		stats.CacheSize = m.cache.size()
		stats.CacheHits = m.cache.hits()
		stats.CacheMisses = m.cache.misses()
		total := stats.CacheHits + stats.CacheMisses
		if total > 0 {
			stats.CacheHitRate = float64(stats.CacheHits) / float64(total)
		}
	}

	return stats
}

// =============================================================================
// managerCache — LRU cache with TTL and optional gzip compression
// =============================================================================

// managerCacheEntry stores a single cached embedding value.
type managerCacheEntry struct {
	hashKey    string
	value      []byte // gzip-compressed or raw little-endian float32 slice
	compressed bool
	timestamp  time.Time
	prev       *managerCacheEntry
	next       *managerCacheEntry
}

// managerCache is an LRU cache for embedding vectors keyed by text.
// It supports optional TTL expiry and gzip compression of stored values.
type managerCache struct {
	capacity  int
	ttl       int // seconds; zero means no expiry
	compress  bool
	items     map[string]*managerCacheEntry
	head      *managerCacheEntry // most-recently-used sentinel
	tail      *managerCacheEntry // least-recently-used sentinel
	mu        sync.Mutex
	cacheHits atomic.Uint64
	cacheMiss atomic.Uint64
}

// newManagerCache creates a new managerCache.
// capacity must be positive; ttlSeconds of 0 disables TTL expiry.
func newManagerCache(capacity, ttlSeconds int, compress bool) (*managerCache, error) {
	if capacity <= 0 {
		return nil, &CacheCapacityError{Capacity: capacity}
	}
	c := &managerCache{
		capacity: capacity,
		ttl:      ttlSeconds,
		compress: compress,
		items:    make(map[string]*managerCacheEntry, capacity),
		head:     &managerCacheEntry{},
		tail:     &managerCacheEntry{},
	}
	c.head.next = c.tail
	c.tail.prev = c.head
	return c, nil
}

// hashKey returns a hex-encoded SHA-256 hash of the text for use as a map key.
func (c *managerCache) hashKey(text string) string {
	sum := sha256.Sum256([]byte(text))
	return fmt.Sprintf("%x", sum)
}

// get retrieves the embedding for text if it exists and has not expired.
func (c *managerCache) get(text string) ([]float32, bool) {
	k := c.hashKey(text)

	c.mu.Lock()
	defer c.mu.Unlock()

	entry, exists := c.items[k]
	if !exists {
		c.cacheMiss.Add(1)
		return nil, false
	}

	// TTL check.
	if c.ttl > 0 && time.Since(entry.timestamp) > time.Duration(c.ttl)*time.Second {
		c.detach(entry)
		delete(c.items, k)
		c.cacheMiss.Add(1)
		return nil, false
	}

	// Promote to MRU.
	c.detach(entry)
	c.insertFront(entry)

	embedding, err := c.decode(entry.value, entry.compressed)
	if err != nil {
		c.cacheMiss.Add(1)
		return nil, false
	}

	c.cacheHits.Add(1)
	return embedding, true
}

// put stores an embedding for text, evicting the LRU entry if at capacity.
func (c *managerCache) put(text string, embedding []float32) {
	k := c.hashKey(text)

	encoded, compressed, err := c.encode(embedding)
	if err != nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if entry, exists := c.items[k]; exists {
		entry.value = encoded
		entry.compressed = compressed
		entry.timestamp = time.Now()
		c.detach(entry)
		c.insertFront(entry)
		return
	}

	// Evict LRU entry if at capacity.
	if len(c.items) >= c.capacity {
		lru := c.tail.prev
		if lru != c.head {
			c.detach(lru)
			delete(c.items, lru.hashKey)
		}
	}

	entry := &managerCacheEntry{
		hashKey:    k,
		value:      encoded,
		compressed: compressed,
		timestamp:  time.Now(),
	}
	c.items[k] = entry
	c.insertFront(entry)
}

// size returns the current number of entries in the cache.
func (c *managerCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// hits returns the cumulative hit count.
func (c *managerCache) hits() uint64 {
	return c.cacheHits.Load()
}

// misses returns the cumulative miss count.
func (c *managerCache) misses() uint64 {
	return c.cacheMiss.Load()
}

// clear removes all entries and resets counters.
func (c *managerCache) clear() {
	c.mu.Lock()
	c.items = make(map[string]*managerCacheEntry, c.capacity)
	c.head.next = c.tail
	c.tail.prev = c.head
	c.mu.Unlock()
	c.cacheHits.Store(0)
	c.cacheMiss.Store(0)
}

// detach removes entry from the doubly-linked list. Caller must hold mu.
func (c *managerCache) detach(e *managerCacheEntry) {
	e.prev.next = e.next
	e.next.prev = e.prev
}

// insertFront places entry immediately after head (MRU). Caller must hold mu.
func (c *managerCache) insertFront(e *managerCacheEntry) {
	e.next = c.head.next
	e.prev = c.head
	c.head.next.prev = e
	c.head.next = e
}

// encode serialises a float32 slice to little-endian bytes, optionally
// compressing the result with gzip. Returns the encoded bytes, whether they
// are compressed, and any encoding error.
func (c *managerCache) encode(embedding []float32) ([]byte, bool, error) {
	buf := new(bytes.Buffer)
	if err := binary.Write(buf, binary.LittleEndian, embedding); err != nil {
		return nil, false, err
	}
	raw := buf.Bytes()

	if c.compress {
		compressed := new(bytes.Buffer)
		gz := gzip.NewWriter(compressed)
		if _, err := gz.Write(raw); err != nil {
			return nil, false, err
		}
		if err := gz.Close(); err != nil {
			return nil, false, err
		}
		// Only use compression if it actually saves space.
		if compressed.Len() < len(raw) {
			return compressed.Bytes(), true, nil
		}
	}

	return raw, false, nil
}

// decode deserialises bytes back to a float32 slice, decompressing if needed.
func (c *managerCache) decode(data []byte, compressed bool) ([]float32, error) {
	var buf *bytes.Buffer

	if compressed {
		gr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		defer gr.Close()
		decompressed, err := io.ReadAll(gr)
		if err != nil {
			return nil, err
		}
		buf = bytes.NewBuffer(decompressed)
	} else {
		buf = bytes.NewBuffer(data)
	}

	var embedding []float32
	for buf.Len() > 0 {
		var f float32
		if err := binary.Read(buf, binary.LittleEndian, &f); err != nil {
			return nil, err
		}
		embedding = append(embedding, f)
	}

	return embedding, nil
}
