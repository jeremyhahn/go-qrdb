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
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeProvider is a test-only Provider implementation. It returns canned
// fixed-length embedding vectors and supports error injection via embedErr and
// closeErr. callCount tracks how many times Embed was invoked.
type fakeProvider struct {
	name     string
	dims     int
	embedErr error
	closeErr error
	mu       sync.Mutex
	calls    int
}

func (f *fakeProvider) Embed(_ context.Context, texts []string, model string) (*EmbeddingResult, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.embedErr != nil {
		return nil, f.embedErr
	}
	embeddings := make([][]float32, len(texts))
	for i := range texts {
		vec := make([]float32, f.dims)
		for j := range vec {
			vec[j] = float32(i+1) * 0.1
		}
		embeddings[i] = vec
	}
	return &EmbeddingResult{Embeddings: embeddings, Model: model}, nil
}

func (f *fakeProvider) Dimensions() int { return f.dims }
func (f *fakeProvider) Name() string    { return f.name }
func (f *fakeProvider) Models(_ context.Context) ([]ModelInfo, error) {
	return []ModelInfo{{Name: "fake-model", Dimensions: f.dims}}, nil
}
func (f *fakeProvider) Close() error { return f.closeErr }

func (f *fakeProvider) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// emptyEmbedProvider returns a valid but empty EmbeddingResult to trigger
// EmptyResponseError paths in the Manager.
type emptyEmbedProvider struct{}

func (e *emptyEmbedProvider) Embed(_ context.Context, _ []string, _ string) (*EmbeddingResult, error) {
	return &EmbeddingResult{Embeddings: nil}, nil
}
func (e *emptyEmbedProvider) Dimensions() int                               { return 0 }
func (e *emptyEmbedProvider) Name() string                                  { return "empty" }
func (e *emptyEmbedProvider) Models(_ context.Context) ([]ModelInfo, error) { return nil, nil }
func (e *emptyEmbedProvider) Close() error                                  { return nil }

// validManagerConfig returns a Config that satisfies validateManagerConfig.
func validManagerConfig() Config {
	return Config{
		Provider: "openai",
		APIURL:   "http://localhost:11434",
		Model:    "text-embedding-3-small",
	}
}

// ---------------------------------------------------------------------------
// NewManager
// ---------------------------------------------------------------------------

func TestNewManager_Valid(t *testing.T) {
	m, err := NewManager(validManagerConfig())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil {
		t.Fatal("expected non-nil Manager")
	}
}

func TestNewManager_WithCache(t *testing.T) {
	cfg := validManagerConfig()
	cfg.CacheEnabled = true
	cfg.CacheSize = 100
	cfg.CacheTTL = 60
	cfg.CacheCompress = true
	m, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.cache == nil {
		t.Error("expected cache to be initialised when CacheEnabled=true")
	}
}

func TestNewManager_InvalidProvider(t *testing.T) {
	cfg := validManagerConfig()
	cfg.Provider = ""
	_, err := NewManager(cfg)
	if !errors.Is(err, ErrProviderRequired) {
		t.Errorf("expected ErrProviderRequired, got: %v", err)
	}
}

func TestNewManager_CacheInvalidCapacity(t *testing.T) {
	cfg := validManagerConfig()
	cfg.CacheEnabled = true
	cfg.CacheSize = 0
	_, err := NewManager(cfg)
	if err == nil {
		t.Fatal("expected error for zero cache size")
	}
	var capErr *CacheCapacityError
	if !errors.As(err, &capErr) {
		t.Errorf("expected CacheCapacityError, got %T: %v", err, err)
	}
}

func TestNewManager_NoCacheWhenDisabled(t *testing.T) {
	cfg := validManagerConfig()
	cfg.CacheEnabled = false
	m, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.cache != nil {
		t.Error("expected nil cache when CacheEnabled=false")
	}
}

// ---------------------------------------------------------------------------
// validateManagerConfig — table-driven coverage of every branch
// ---------------------------------------------------------------------------

func TestValidateManagerConfig_AllErrorBranches(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr error
	}{
		{
			name:    "empty provider",
			mutate:  func(c *Config) { c.Provider = "" },
			wantErr: ErrProviderRequired,
		},
		{
			name:    "unknown provider",
			mutate:  func(c *Config) { c.Provider = "unknown" },
			wantErr: ErrProviderInvalid,
		},
		{
			name:    "empty APIURL",
			mutate:  func(c *Config) { c.APIURL = "" },
			wantErr: ErrAPIURLRequired,
		},
		{
			name:    "voyage without API key",
			mutate:  func(c *Config) { c.Provider = "voyage"; c.APIKey = "" },
			wantErr: ErrAPIKeyRequired,
		},
		{
			name:    "empty model",
			mutate:  func(c *Config) { c.Model = "" },
			wantErr: ErrModelRequired,
		},
		{
			name:    "negative dimensions",
			mutate:  func(c *Config) { c.Dimensions = -1 },
			wantErr: ErrDimensionsPositive,
		},
		{
			name:    "negative max batch size",
			mutate:  func(c *Config) { c.MaxBatchSize = -1 },
			wantErr: ErrMaxBatchSizePositive,
		},
		{
			name:    "negative timeout",
			mutate:  func(c *Config) { c.Timeout = -1 },
			wantErr: ErrTimeoutPositive,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validManagerConfig()
			tt.mutate(&cfg)
			err := validateManagerConfig(cfg)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("expected %v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestValidateManagerConfig_AllValidProviders(t *testing.T) {
	for _, provider := range []string{"openai", "ollama", "lmstudio"} {
		t.Run(provider, func(t *testing.T) {
			cfg := validManagerConfig()
			cfg.Provider = provider
			if err := validateManagerConfig(cfg); err != nil {
				t.Errorf("provider %q: unexpected error: %v", provider, err)
			}
		})
	}
}

func TestValidateManagerConfig_VoyageWithKey(t *testing.T) {
	cfg := validManagerConfig()
	cfg.Provider = "voyage"
	cfg.APIKey = "sk-test"
	if err := validateManagerConfig(cfg); err != nil {
		t.Errorf("unexpected error for voyage with API key: %v", err)
	}
}

func TestValidateManagerConfig_Success(t *testing.T) {
	if err := validateManagerConfig(validManagerConfig()); err != nil {
		t.Errorf("unexpected error for valid config: %v", err)
	}
}

// ---------------------------------------------------------------------------
// RegisterProvider / GetProvider
// ---------------------------------------------------------------------------

func TestRegisterProvider_Success(t *testing.T) {
	m, _ := NewManager(validManagerConfig())
	p := &fakeProvider{name: "openai", dims: 768}
	if err := m.RegisterProvider("openai", p); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRegisterProvider_Duplicate(t *testing.T) {
	m, _ := NewManager(validManagerConfig())
	p := &fakeProvider{name: "openai", dims: 768}
	if err := m.RegisterProvider("openai", p); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	err := m.RegisterProvider("openai", p)
	if err == nil {
		t.Fatal("expected error for duplicate registration")
	}
	var dup *ProviderAlreadyRegisteredError
	if !errors.As(err, &dup) {
		t.Errorf("expected ProviderAlreadyRegisteredError, got %T: %v", err, err)
	}
	if dup.Name != "openai" {
		t.Errorf("expected Name=openai, got %q", dup.Name)
	}
}

func TestGetProvider_Found(t *testing.T) {
	m, _ := NewManager(validManagerConfig())
	p := &fakeProvider{name: "openai", dims: 768}
	m.RegisterProvider("openai", p)

	got, err := m.GetProvider("openai")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != p {
		t.Error("expected the registered provider instance to be returned")
	}
}

func TestGetProvider_NotFound(t *testing.T) {
	m, _ := NewManager(validManagerConfig())
	_, err := m.GetProvider("nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
	var notFound *ProviderNotFoundError
	if !errors.As(err, &notFound) {
		t.Errorf("expected ProviderNotFoundError, got %T: %v", err, err)
	}
	if notFound.Name != "nonexistent" {
		t.Errorf("expected Name=nonexistent, got %q", notFound.Name)
	}
}

// ---------------------------------------------------------------------------
// Embed
// ---------------------------------------------------------------------------

func TestManagerEmbed_Success(t *testing.T) {
	m, _ := NewManager(validManagerConfig())
	m.RegisterProvider("openai", &fakeProvider{name: "openai", dims: 4})

	vec, err := m.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vec) != 4 {
		t.Errorf("expected 4 dims, got %d", len(vec))
	}
}

func TestManagerEmbed_ProviderNotFound(t *testing.T) {
	m, _ := NewManager(validManagerConfig())
	// No provider registered.
	_, err := m.Embed(context.Background(), "hello")
	var notFound *ProviderNotFoundError
	if !errors.As(err, &notFound) {
		t.Errorf("expected ProviderNotFoundError, got %T: %v", err, err)
	}
}

func TestManagerEmbed_ProviderError(t *testing.T) {
	m, _ := NewManager(validManagerConfig())
	providerErr := &HTTPError{StatusCode: 503, Body: "unavailable"}
	m.RegisterProvider("openai", &fakeProvider{name: "openai", dims: 4, embedErr: providerErr})

	_, err := m.Embed(context.Background(), "hello")
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Errorf("expected HTTPError, got %T: %v", err, err)
	}
	if httpErr.StatusCode != 503 {
		t.Errorf("expected 503, got %d", httpErr.StatusCode)
	}
}

func TestManagerEmbed_EmptyResponse(t *testing.T) {
	m, _ := NewManager(validManagerConfig())
	m.RegisterProvider("openai", &emptyEmbedProvider{})

	_, err := m.Embed(context.Background(), "hello")
	var emptyErr *EmptyResponseError
	if !errors.As(err, &emptyErr) {
		t.Errorf("expected EmptyResponseError, got %T: %v", err, err)
	}
}

func TestManagerEmbed_CacheHit(t *testing.T) {
	cfg := validManagerConfig()
	cfg.CacheEnabled = true
	cfg.CacheSize = 10
	m, _ := NewManager(cfg)
	p := &fakeProvider{name: "openai", dims: 4}
	m.RegisterProvider("openai", p)

	// First call populates the cache.
	vec1, err := m.Embed(context.Background(), "cached text")
	if err != nil {
		t.Fatalf("first Embed: %v", err)
	}

	// Second call must be served from cache.
	vec2, err := m.Embed(context.Background(), "cached text")
	if err != nil {
		t.Fatalf("second Embed: %v", err)
	}

	if len(vec1) != len(vec2) {
		t.Errorf("vector lengths differ: %d vs %d", len(vec1), len(vec2))
	}
	if p.callCount() != 1 {
		t.Errorf("expected provider called exactly once, got %d", p.callCount())
	}
}

func TestManagerEmbed_CacheMissStoresResult(t *testing.T) {
	cfg := validManagerConfig()
	cfg.CacheEnabled = true
	cfg.CacheSize = 10
	m, _ := NewManager(cfg)
	m.RegisterProvider("openai", &fakeProvider{name: "openai", dims: 4})

	_, err := m.Embed(context.Background(), "store me")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	// Cache should now hold one entry.
	stats := m.GetStats()
	if stats.CacheSize != 1 {
		t.Errorf("expected CacheSize=1 after first embed, got %d", stats.CacheSize)
	}
}

// ---------------------------------------------------------------------------
// EmbedBatch
// ---------------------------------------------------------------------------

func TestManagerEmbedBatch_EmptyInput(t *testing.T) {
	m, _ := NewManager(validManagerConfig())
	_, err := m.EmbedBatch(context.Background(), []string{})
	var inv *InvalidInputError
	if !errors.As(err, &inv) {
		t.Errorf("expected InvalidInputError, got %T: %v", err, err)
	}
}

func TestManagerEmbedBatch_NilInput(t *testing.T) {
	m, _ := NewManager(validManagerConfig())
	_, err := m.EmbedBatch(context.Background(), nil)
	var inv *InvalidInputError
	if !errors.As(err, &inv) {
		t.Errorf("expected InvalidInputError, got %T: %v", err, err)
	}
}

func TestManagerEmbedBatch_ProviderNotFound(t *testing.T) {
	m, _ := NewManager(validManagerConfig())
	_, err := m.EmbedBatch(context.Background(), []string{"a", "b"})
	var notFound *ProviderNotFoundError
	if !errors.As(err, &notFound) {
		t.Errorf("expected ProviderNotFoundError, got %T: %v", err, err)
	}
}

func TestManagerEmbedBatch_NoCache(t *testing.T) {
	m, _ := NewManager(validManagerConfig())
	m.RegisterProvider("openai", &fakeProvider{name: "openai", dims: 4})

	results, err := m.EmbedBatch(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	for i, r := range results {
		if len(r) != 4 {
			t.Errorf("result[%d]: expected 4 dims, got %d", i, len(r))
		}
	}
}

func TestManagerEmbedBatch_WithCache_AllMiss(t *testing.T) {
	cfg := validManagerConfig()
	cfg.CacheEnabled = true
	cfg.CacheSize = 20
	m, _ := NewManager(cfg)
	p := &fakeProvider{name: "openai", dims: 4}
	m.RegisterProvider("openai", p)

	_, err := m.EmbedBatch(context.Background(), []string{"x", "y", "z"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.callCount() != 1 {
		t.Errorf("expected 1 provider call, got %d", p.callCount())
	}
}

func TestManagerEmbedBatch_WithCache_AllHit(t *testing.T) {
	cfg := validManagerConfig()
	cfg.CacheEnabled = true
	cfg.CacheSize = 20
	m, _ := NewManager(cfg)
	p := &fakeProvider{name: "openai", dims: 4}
	m.RegisterProvider("openai", p)

	texts := []string{"x", "y", "z"}
	// Warm the cache.
	for _, text := range texts {
		if _, err := m.Embed(context.Background(), text); err != nil {
			t.Fatalf("cache warm Embed(%q): %v", text, err)
		}
	}
	callsBefore := p.callCount()

	results, err := m.EmbedBatch(context.Background(), texts)
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	if p.callCount() != callsBefore {
		t.Errorf("expected no new provider calls for all-cached batch, got %d new calls",
			p.callCount()-callsBefore)
	}
}

func TestManagerEmbedBatch_WithCache_Mixed(t *testing.T) {
	cfg := validManagerConfig()
	cfg.CacheEnabled = true
	cfg.CacheSize = 20
	m, _ := NewManager(cfg)
	p := &fakeProvider{name: "openai", dims: 4}
	m.RegisterProvider("openai", p)

	// Cache only the first two.
	for _, text := range []string{"cached1", "cached2"} {
		if _, err := m.Embed(context.Background(), text); err != nil {
			t.Fatalf("warm: %v", err)
		}
	}
	callsBefore := p.callCount()

	results, err := m.EmbedBatch(context.Background(), []string{"cached1", "cached2", "uncached3"})
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	// Exactly one new provider call for the uncached text.
	if p.callCount() != callsBefore+1 {
		t.Errorf("expected 1 new provider call, got %d", p.callCount()-callsBefore)
	}
}

func TestManagerEmbedBatch_ProviderError(t *testing.T) {
	m, _ := NewManager(validManagerConfig())
	providerErr := &HTTPError{StatusCode: 500, Body: "server error"}
	m.RegisterProvider("openai", &fakeProvider{name: "openai", dims: 4, embedErr: providerErr})

	_, err := m.EmbedBatch(context.Background(), []string{"a", "b"})
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Errorf("expected HTTPError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Dimensions
// ---------------------------------------------------------------------------

func TestManagerDimensions_Success(t *testing.T) {
	m, _ := NewManager(validManagerConfig())
	m.RegisterProvider("openai", &fakeProvider{name: "openai", dims: 1536})

	dims, err := m.Dimensions()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dims != 1536 {
		t.Errorf("expected 1536, got %d", dims)
	}
}

func TestManagerDimensions_ProviderNotFound(t *testing.T) {
	m, _ := NewManager(validManagerConfig())
	_, err := m.Dimensions()
	var notFound *ProviderNotFoundError
	if !errors.As(err, &notFound) {
		t.Errorf("expected ProviderNotFoundError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func TestManagerClose_Success(t *testing.T) {
	m, _ := NewManager(validManagerConfig())
	m.RegisterProvider("openai", &fakeProvider{name: "openai", dims: 4})
	if err := m.Close(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestManagerClose_ProviderError(t *testing.T) {
	m, _ := NewManager(validManagerConfig())
	closeErr := errors.New("transport closed")
	m.RegisterProvider("openai", &fakeProvider{name: "openai", dims: 4, closeErr: closeErr})

	err := m.Close()
	if err == nil {
		t.Fatal("expected close error")
	}
	var closeErrs *CloseError
	if !errors.As(err, &closeErrs) {
		t.Errorf("expected CloseError, got %T: %v", err, err)
	}
	if len(closeErrs.Errors) == 0 {
		t.Error("expected at least one inner error")
	}
}

func TestManagerClose_ClearsCache(t *testing.T) {
	cfg := validManagerConfig()
	cfg.CacheEnabled = true
	cfg.CacheSize = 10
	m, _ := NewManager(cfg)
	m.RegisterProvider("openai", &fakeProvider{name: "openai", dims: 4})

	if _, err := m.Embed(context.Background(), "populate cache"); err != nil {
		t.Fatalf("Embed: %v", err)
	}

	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if m.cache.size() != 0 {
		t.Errorf("expected cache cleared after Close, got size %d", m.cache.size())
	}
}

// ---------------------------------------------------------------------------
// GetStats
// ---------------------------------------------------------------------------

func TestManagerGetStats_NoCache(t *testing.T) {
	m, _ := NewManager(validManagerConfig())
	m.RegisterProvider("openai", &fakeProvider{name: "openai", dims: 4})

	stats := m.GetStats()
	if stats.CacheEnabled {
		t.Error("expected CacheEnabled=false")
	}
	if stats.ProvidersRegistered != 1 {
		t.Errorf("expected 1 provider, got %d", stats.ProvidersRegistered)
	}
	if stats.DefaultProvider != "openai" {
		t.Errorf("expected DefaultProvider=openai, got %q", stats.DefaultProvider)
	}
	if len(stats.ProviderNames) != 1 {
		t.Errorf("expected 1 provider name, got %v", stats.ProviderNames)
	}
}

func TestManagerGetStats_WithCacheCounters(t *testing.T) {
	cfg := validManagerConfig()
	cfg.CacheEnabled = true
	cfg.CacheSize = 10
	m, _ := NewManager(cfg)
	m.RegisterProvider("openai", &fakeProvider{name: "openai", dims: 4})

	// One miss (first embed) then one hit.
	m.Embed(context.Background(), "unique-text")
	m.Embed(context.Background(), "unique-text")

	stats := m.GetStats()
	if !stats.CacheEnabled {
		t.Error("expected CacheEnabled=true")
	}
	if stats.CacheSize != 1 {
		t.Errorf("expected CacheSize=1, got %d", stats.CacheSize)
	}
	if stats.CacheHits != 1 {
		t.Errorf("expected CacheHits=1, got %d", stats.CacheHits)
	}
	if stats.CacheMisses != 1 {
		t.Errorf("expected CacheMisses=1, got %d", stats.CacheMisses)
	}
	if stats.CacheHitRate != 0.5 {
		t.Errorf("expected CacheHitRate=0.5, got %f", stats.CacheHitRate)
	}
}

func TestManagerGetStats_HitRateZeroWhenNoLookups(t *testing.T) {
	cfg := validManagerConfig()
	cfg.CacheEnabled = true
	cfg.CacheSize = 10
	m, _ := NewManager(cfg)

	stats := m.GetStats()
	if stats.CacheHitRate != 0 {
		t.Errorf("expected HitRate=0 with no lookups, got %f", stats.CacheHitRate)
	}
}

// ---------------------------------------------------------------------------
// newManagerCache — constructor validation
// ---------------------------------------------------------------------------

func TestNewManagerCache_Valid(t *testing.T) {
	c, err := newManagerCache(10, 60, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil cache")
	}
}

func TestNewManagerCache_WithCompress(t *testing.T) {
	c, err := newManagerCache(5, 0, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !c.compress {
		t.Error("expected compress=true")
	}
}

func TestNewManagerCache_ZeroCapacity(t *testing.T) {
	_, err := newManagerCache(0, 0, false)
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

func TestNewManagerCache_NegativeCapacity(t *testing.T) {
	_, err := newManagerCache(-3, 0, false)
	if err == nil {
		t.Fatal("expected error for negative capacity")
	}
	var capErr *CacheCapacityError
	if !errors.As(err, &capErr) {
		t.Errorf("expected CacheCapacityError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// managerCache — hashKey
// ---------------------------------------------------------------------------

func TestManagerCache_HashKey_Deterministic(t *testing.T) {
	c, _ := newManagerCache(10, 0, false)
	k1 := c.hashKey("hello")
	k2 := c.hashKey("hello")
	if k1 != k2 {
		t.Errorf("same input produced different hashes: %q vs %q", k1, k2)
	}
}

func TestManagerCache_HashKey_Distinct(t *testing.T) {
	c, _ := newManagerCache(10, 0, false)
	k1 := c.hashKey("hello")
	k2 := c.hashKey("world")
	if k1 == k2 {
		t.Error("different inputs produced the same hash")
	}
}

// ---------------------------------------------------------------------------
// managerCache — get / put / size / hits / misses
// ---------------------------------------------------------------------------

func TestManagerCache_GetMiss(t *testing.T) {
	c, _ := newManagerCache(10, 0, false)
	_, ok := c.get("nonexistent")
	if ok {
		t.Error("expected cache miss for unknown key")
	}
	if c.misses() != 1 {
		t.Errorf("expected misses=1, got %d", c.misses())
	}
}

func TestManagerCache_PutAndGet(t *testing.T) {
	c, _ := newManagerCache(10, 0, false)
	vec := []float32{1.0, 2.0, 3.0}
	c.put("hello", vec)

	got, ok := c.get("hello")
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
	if c.hits() != 1 {
		t.Errorf("expected hits=1, got %d", c.hits())
	}
}

func TestManagerCache_UpdateExisting(t *testing.T) {
	c, _ := newManagerCache(10, 0, false)
	c.put("key", []float32{1.0})
	c.put("key", []float32{9.9}) // overwrite same key

	got, ok := c.get("key")
	if !ok {
		t.Fatal("expected cache hit after update")
	}
	if got[0] != 9.9 {
		t.Errorf("expected updated value 9.9, got %f", got[0])
	}
	if c.size() != 1 {
		t.Errorf("expected size=1 after update, got %d", c.size())
	}
}

func TestManagerCache_LRUEviction(t *testing.T) {
	c, _ := newManagerCache(3, 0, false)
	c.put("a", []float32{1})
	c.put("b", []float32{2})
	c.put("c", []float32{3})

	// "a" is LRU; inserting "d" must evict "a".
	c.put("d", []float32{4})

	if _, ok := c.get("a"); ok {
		t.Error("expected 'a' to be evicted (LRU)")
	}
	if _, ok := c.get("d"); !ok {
		t.Error("expected 'd' to be present after eviction")
	}
	if c.size() != 3 {
		t.Errorf("expected size=3, got %d", c.size())
	}
}

func TestManagerCache_LRUPromotion(t *testing.T) {
	c, _ := newManagerCache(3, 0, false)
	c.put("a", []float32{1})
	c.put("b", []float32{2})
	c.put("c", []float32{3})

	// Access "a" to promote it; "b" becomes LRU.
	c.get("a")

	// Adding "d" should evict "b", not "a".
	c.put("d", []float32{4})

	if _, ok := c.get("b"); ok {
		t.Error("expected 'b' to be evicted after 'a' was promoted")
	}
	if _, ok := c.get("a"); !ok {
		t.Error("expected 'a' to still be present after promotion")
	}
}

func TestManagerCache_HitsAndMisses(t *testing.T) {
	c, _ := newManagerCache(10, 0, false)
	c.put("k", []float32{1})

	c.get("k")    // hit
	c.get("k")    // hit
	c.get("nope") // miss

	if c.hits() != 2 {
		t.Errorf("expected 2 hits, got %d", c.hits())
	}
	if c.misses() != 1 {
		t.Errorf("expected 1 miss, got %d", c.misses())
	}
}

// ---------------------------------------------------------------------------
// managerCache — clear
// ---------------------------------------------------------------------------

func TestManagerCache_Clear_RemovesEntries(t *testing.T) {
	c, _ := newManagerCache(10, 0, false)
	c.put("a", []float32{1})
	c.put("b", []float32{2})
	c.get("a") // hit
	c.get("x") // miss

	c.clear()

	if c.size() != 0 {
		t.Errorf("expected size=0 after clear, got %d", c.size())
	}
	if c.hits() != 0 {
		t.Errorf("expected hits=0 after clear, got %d", c.hits())
	}
	if c.misses() != 0 {
		t.Errorf("expected misses=0 after clear, got %d", c.misses())
	}
	if _, ok := c.get("a"); ok {
		t.Error("expected 'a' gone after clear")
	}
}

func TestManagerCache_Clear_ResetsList(t *testing.T) {
	c, _ := newManagerCache(5, 0, false)
	for i := 0; i < 5; i++ {
		c.put("k", []float32{float32(i)})
	}
	c.clear()

	// After clear, the linked list sentinels must be properly linked.
	if c.head.next != c.tail {
		t.Error("expected head.next == tail after clear")
	}
	if c.tail.prev != c.head {
		t.Error("expected tail.prev == head after clear")
	}
}

// ---------------------------------------------------------------------------
// managerCache — TTL
// ---------------------------------------------------------------------------

func TestManagerCache_TTLExpiry(t *testing.T) {
	c, _ := newManagerCache(10, 1, false) // TTL = 1 second
	c.put("hello", []float32{1.0})

	// Back-date the entry to simulate TTL expiry (same package access).
	k := c.hashKey("hello")
	c.mu.Lock()
	c.items[k].timestamp = time.Now().Add(-2 * time.Second)
	c.mu.Unlock()

	_, ok := c.get("hello")
	if ok {
		t.Error("expected TTL-expired entry to be a miss")
	}
	if c.misses() != 1 {
		t.Errorf("expected misses=1 after TTL expiry, got %d", c.misses())
	}
	// Expired entry must be removed from the map.
	if c.size() != 0 {
		t.Errorf("expected cache empty after TTL eviction, got size %d", c.size())
	}
}

func TestManagerCache_NoTTL_DoesNotExpire(t *testing.T) {
	c, _ := newManagerCache(10, 0, false) // TTL = 0 disables expiry
	c.put("hello", []float32{1.0})

	k := c.hashKey("hello")
	c.mu.Lock()
	c.items[k].timestamp = time.Now().Add(-9999 * time.Second)
	c.mu.Unlock()

	_, ok := c.get("hello")
	if !ok {
		t.Error("expected cache hit when TTL is disabled (ttl=0)")
	}
}

// ---------------------------------------------------------------------------
// managerCache — encode / decode round-trips
// ---------------------------------------------------------------------------

func TestManagerCacheEncode_NoCompression(t *testing.T) {
	c, _ := newManagerCache(10, 0, false)
	vec := []float32{1.0, 2.0, 3.0, 4.0}
	encoded, compressed, err := c.encode(vec)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if compressed {
		t.Error("expected compressed=false when compress is disabled")
	}
	// 4 × float32 × 4 bytes = 16 bytes.
	if len(encoded) != 16 {
		t.Errorf("expected 16 raw bytes, got %d", len(encoded))
	}
}

func TestManagerCacheEncode_Decode_RoundTrip_NoCompression(t *testing.T) {
	c, _ := newManagerCache(10, 0, false)
	original := []float32{0.1, 0.2, 0.3, 0.4, 0.5}

	encoded, compressed, err := c.encode(original)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := c.decode(encoded, compressed)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded) != len(original) {
		t.Fatalf("expected %d floats, got %d", len(original), len(decoded))
	}
	for i, v := range original {
		if decoded[i] != v {
			t.Errorf("float[%d]: expected %f, got %f", i, v, decoded[i])
		}
	}
}

func TestManagerCacheEncode_Decode_RoundTrip_WithCompression(t *testing.T) {
	c, _ := newManagerCache(10, 0, true) // compress=true
	// 512 zeros: highly compressible, ensures gzip saves space.
	original := make([]float32, 512)

	encoded, compressed, err := c.encode(original)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	// decode must work regardless of the compression flag.
	decoded, err := c.decode(encoded, compressed)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded) != len(original) {
		t.Errorf("expected %d floats, got %d", len(original), len(decoded))
	}
}

func TestManagerCacheDecode_InvalidGzip(t *testing.T) {
	c, _ := newManagerCache(10, 0, false)
	_, err := c.decode([]byte("not valid gzip data"), true)
	if err == nil {
		t.Error("expected error when decoding invalid gzip payload")
	}
}

func TestManagerCache_PutAndGet_WithCompression(t *testing.T) {
	c, _ := newManagerCache(10, 0, true)
	vec := make([]float32, 512) // zeros compress well
	c.put("big-vector", vec)

	got, ok := c.get("big-vector")
	if !ok {
		t.Fatal("expected cache hit for compressed entry")
	}
	if len(got) != len(vec) {
		t.Errorf("expected %d floats, got %d", len(vec), len(got))
	}
}
