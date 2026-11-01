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

package service

import (
	"context"
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// Mock provider
// ---------------------------------------------------------------------------

// mockProvider is a test double for EmbeddingProvider.
type mockProvider struct {
	embedFn  func(ctx context.Context, texts []string, model string) (*EmbeddingResult, error)
	modelsFn func(ctx context.Context) ([]ModelInfo, error)
	closeFn  func() error
	// defaultModel is what Embed resolves an empty model to.
	defaultModel string
}

func (m *mockProvider) DefaultModel() string { return m.defaultModel }

func (m *mockProvider) Embed(ctx context.Context, texts []string, model string) (*EmbeddingResult, error) {
	if m.embedFn != nil {
		return m.embedFn(ctx, texts, model)
	}
	embeddings := make([][]float32, len(texts))
	for i := range texts {
		embeddings[i] = []float32{float32(i+1) * 0.1}
	}
	return &EmbeddingResult{
		Embeddings: embeddings,
		Model:      model,
		Usage:      &UsageInfo{PromptTokens: len(texts), TotalTokens: len(texts)},
	}, nil
}

func (m *mockProvider) Models(ctx context.Context) ([]ModelInfo, error) {
	if m.modelsFn != nil {
		return m.modelsFn(ctx)
	}
	return []ModelInfo{{Name: "default-model"}}, nil
}

func (m *mockProvider) Close() error {
	if m.closeFn != nil {
		return m.closeFn()
	}
	return nil
}

// ---------------------------------------------------------------------------
// Mock cache
// ---------------------------------------------------------------------------

// mockCache is a simple in-memory EmbeddingCacher for testing.
type mockCache struct {
	store map[string][]float32
}

func newMockCache() *mockCache {
	return &mockCache{store: make(map[string][]float32)}
}

func (c *mockCache) Get(model, text string) ([]float32, bool) {
	v, ok := c.store[model+"\x00"+text]
	return v, ok
}

func (c *mockCache) Put(model, text string, embedding []float32) {
	c.store[model+"\x00"+text] = embedding
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newServiceWithProvider builds a service with one named provider registered.
func newServiceWithProvider(name string, p EmbeddingProvider) EmbeddingServicer {
	svc := NewEmbeddingService(WithDefaultProvider(name))
	svc.RegisterProvider(name, p) //nolint:errcheck
	return svc
}

// newServiceWithCache builds a service with a cache and one named provider.
func newServiceWithCache(name string, p EmbeddingProvider, cache EmbeddingCacher) EmbeddingServicer {
	svc := NewEmbeddingService(
		WithDefaultProvider(name),
		WithEmbeddingCache(cache),
	)
	svc.RegisterProvider(name, p) //nolint:errcheck
	return svc
}

// ---------------------------------------------------------------------------
// Embed tests
// ---------------------------------------------------------------------------

func TestEmbeddingService_Embed_Success(t *testing.T) {
	svc := newServiceWithProvider("local", &mockProvider{})

	resp, err := svc.Embed(context.Background(), &EmbedRequest{
		Texts: []string{"hello", "world"},
		Model: "m",
	})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(resp.Embeddings) != 2 {
		t.Fatalf("expected 2 embeddings, got %d", len(resp.Embeddings))
	}
	if resp.Cached {
		t.Error("expected Cached=false on first call")
	}
}

func TestEmbeddingService_Embed_WithCache_Miss(t *testing.T) {
	svc := newServiceWithCache("local", &mockProvider{}, newMockCache())

	resp, err := svc.Embed(context.Background(), &EmbedRequest{
		Texts: []string{"miss"},
		Model: "m",
	})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if resp.Cached {
		t.Error("expected Cached=false on cache miss")
	}
}

func TestEmbeddingService_Embed_CacheHit(t *testing.T) {
	calls := 0
	mock := &mockProvider{
		embedFn: func(ctx context.Context, texts []string, model string) (*EmbeddingResult, error) {
			calls++
			return &EmbeddingResult{
				Embeddings: [][]float32{{0.9, 0.8}},
				Model:      model,
				Usage:      &UsageInfo{PromptTokens: 1, TotalTokens: 1},
			}, nil
		},
	}
	svc := newServiceWithCache("local", mock, newMockCache())

	req := &EmbedRequest{Texts: []string{"cached text"}, Model: "m"}

	// First call — goes to provider.
	if _, err := svc.Embed(context.Background(), req); err != nil {
		t.Fatalf("first Embed: %v", err)
	}

	// Second call — must be served from cache.
	resp, err := svc.Embed(context.Background(), req)
	if err != nil {
		t.Fatalf("second Embed: %v", err)
	}
	if !resp.Cached {
		t.Error("expected Cached=true on second call")
	}
	if calls != 1 {
		t.Errorf("expected 1 provider call, got %d", calls)
	}
}

// countingProvider returns a mockProvider whose default model is
// defaultModel and which counts its Embed calls per model it was called with.
func countingProvider(defaultModel string, calls map[string]int) *mockProvider {
	return &mockProvider{
		defaultModel: defaultModel,
		embedFn: func(_ context.Context, texts []string, model string) (*EmbeddingResult, error) {
			if model == "" {
				model = defaultModel
			}
			calls[model]++
			embeddings := make([][]float32, len(texts))
			for i := range texts {
				embeddings[i] = []float32{float32(len(model))}
			}
			return &EmbeddingResult{Embeddings: embeddings, Model: model}, nil
		},
	}
}

func TestEmbeddingService_Embed_NoModelHitsDefaultModelEntry(t *testing.T) {
	calls := map[string]int{}
	svc := newServiceWithCache("local", countingProvider("dflt", calls), newMockCache())

	if _, err := svc.Embed(context.Background(), &EmbedRequest{Texts: []string{"t"}, Model: "dflt"}); err != nil {
		t.Fatalf("default-model Embed: %v", err)
	}
	resp, err := svc.Embed(context.Background(), &EmbedRequest{Texts: []string{"t"}})
	if err != nil {
		t.Fatalf("no-model Embed: %v", err)
	}
	if !resp.Cached {
		t.Error("no-model request after a default-model request: want a cache hit")
	}
	if resp.Model != "dflt" {
		t.Errorf("Model = %q, want %q", resp.Model, "dflt")
	}
	if calls["dflt"] != 1 {
		t.Errorf("provider calls = %d, want 1", calls["dflt"])
	}
}

func TestEmbeddingService_Embed_DefaultModelHitsNoModelEntry(t *testing.T) {
	calls := map[string]int{}
	svc := newServiceWithCache("local", countingProvider("dflt", calls), newMockCache())

	if _, err := svc.Embed(context.Background(), &EmbedRequest{Texts: []string{"t"}}); err != nil {
		t.Fatalf("no-model Embed: %v", err)
	}
	resp, err := svc.Embed(context.Background(), &EmbedRequest{Texts: []string{"t"}, Model: "dflt"})
	if err != nil {
		t.Fatalf("default-model Embed: %v", err)
	}
	if !resp.Cached {
		t.Error("default-model request after a no-model request: want a cache hit")
	}
	if calls["dflt"] != 1 {
		t.Errorf("provider calls = %d, want 1", calls["dflt"])
	}
}

func TestEmbeddingService_Embed_DifferentModelsDoNotShareEntry(t *testing.T) {
	calls := map[string]int{}
	svc := newServiceWithCache("local", countingProvider("dflt", calls), newMockCache())

	for _, model := range []string{"a", "bb", "", "a", "bb", ""} {
		if _, err := svc.Embed(context.Background(), &EmbedRequest{Texts: []string{"t"}, Model: model}); err != nil {
			t.Fatalf("Embed(%q): %v", model, err)
		}
	}
	resp, err := svc.Embed(context.Background(), &EmbedRequest{Texts: []string{"t"}, Model: "bb"})
	if err != nil {
		t.Fatalf("Embed(bb): %v", err)
	}
	if got := resp.Embeddings[0][0]; got != 2 {
		t.Errorf("model bb served the embedding %v of another model", got)
	}
	for _, model := range []string{"a", "bb", "dflt"} {
		if calls[model] != 1 {
			t.Errorf("provider calls for %q = %d, want 1", model, calls[model])
		}
	}
}

func TestEmbeddingService_Embed_ProviderNotFound(t *testing.T) {
	svc := NewEmbeddingService(WithDefaultProvider("missing"))

	_, err := svc.Embed(context.Background(), &EmbedRequest{Texts: []string{"x"}, Model: "m"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var target *EmbeddingProviderNotFoundError
	if !errors.As(err, &target) {
		t.Errorf("expected EmbeddingProviderNotFoundError, got %T: %v", err, err)
	}
}

func TestEmbeddingService_Embed_DefaultProvider(t *testing.T) {
	// Register two providers; the default is "primary".
	svc := NewEmbeddingService(WithDefaultProvider("primary"))
	svc.RegisterProvider("primary", &mockProvider{}) //nolint:errcheck
	svc.RegisterProvider("other", &mockProvider{})   //nolint:errcheck

	// A request with no explicit Provider should use "primary".
	resp, err := svc.Embed(context.Background(), &EmbedRequest{
		Texts: []string{"test"},
		Model: "m",
	})
	if err != nil {
		t.Fatalf("Embed with default provider: %v", err)
	}
	if len(resp.Embeddings) != 1 {
		t.Errorf("expected 1 embedding, got %d", len(resp.Embeddings))
	}
}

func TestEmbeddingService_Embed_ExplicitProvider(t *testing.T) {
	called := ""
	makeProvider := func(name string) *mockProvider {
		return &mockProvider{
			embedFn: func(_ context.Context, texts []string, model string) (*EmbeddingResult, error) {
				called = name
				return &EmbeddingResult{
					Embeddings: [][]float32{{1.0}},
					Model:      model,
				}, nil
			},
		}
	}

	svc := NewEmbeddingService(WithDefaultProvider("a"))
	svc.RegisterProvider("a", makeProvider("a")) //nolint:errcheck
	svc.RegisterProvider("b", makeProvider("b")) //nolint:errcheck

	if _, err := svc.Embed(context.Background(), &EmbedRequest{
		Texts:    []string{"text"},
		Model:    "m",
		Provider: "b",
	}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if called != "b" {
		t.Errorf("expected provider 'b' to be called, got %q", called)
	}
}

func TestEmbeddingService_Embed_EmptyTexts(t *testing.T) {
	mock := &mockProvider{
		embedFn: func(_ context.Context, texts []string, model string) (*EmbeddingResult, error) {
			return nil, &EmbeddingProviderNotFoundError{Provider: "input-error"}
		},
	}
	svc := newServiceWithProvider("local", mock)

	_, err := svc.Embed(context.Background(), &EmbedRequest{Texts: []string{}, Model: "m"})
	// The provider rejects empty input; verify the error propagates.
	if err == nil {
		t.Fatal("expected error for empty texts, got nil")
	}
}

func TestEmbeddingService_Embed_Closed(t *testing.T) {
	svc := newServiceWithProvider("local", &mockProvider{})
	svc.Close() //nolint:errcheck

	_, err := svc.Embed(context.Background(), &EmbedRequest{Texts: []string{"x"}, Model: "m"})
	if err == nil {
		t.Fatal("expected EmbeddingServiceClosedError, got nil")
	}
	var target *EmbeddingServiceClosedError
	if !errors.As(err, &target) {
		t.Errorf("expected EmbeddingServiceClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// BatchEmbed tests
// ---------------------------------------------------------------------------

func TestEmbeddingService_BatchEmbed_Success(t *testing.T) {
	texts := make([]string, 10)
	for i := range texts {
		texts[i] = "text"
	}

	svc := newServiceWithProvider("local", &mockProvider{})

	resp, err := svc.BatchEmbed(context.Background(), &BatchEmbedRequest{
		Texts: texts,
		Model: "m",
	})
	if err != nil {
		t.Fatalf("BatchEmbed: %v", err)
	}
	if len(resp.Embeddings) != 10 {
		t.Fatalf("expected 10 embeddings, got %d", len(resp.Embeddings))
	}
}

func TestEmbeddingService_BatchEmbed_CustomBatchSize(t *testing.T) {
	calls := 0
	mock := &mockProvider{
		embedFn: func(_ context.Context, texts []string, model string) (*EmbeddingResult, error) {
			calls++
			embeddings := make([][]float32, len(texts))
			for i := range texts {
				embeddings[i] = []float32{float32(i)}
			}
			return &EmbeddingResult{
				Embeddings: embeddings,
				Model:      model,
				Usage:      &UsageInfo{PromptTokens: len(texts), TotalTokens: len(texts)},
			}, nil
		},
	}
	svc := newServiceWithProvider("local", mock)

	resp, err := svc.BatchEmbed(context.Background(), &BatchEmbedRequest{
		Texts:     make([]string, 9),
		Model:     "m",
		BatchSize: 3, // 9 texts / 3 per batch = 3 provider calls
	})
	if err != nil {
		t.Fatalf("BatchEmbed: %v", err)
	}
	if len(resp.Embeddings) != 9 {
		t.Fatalf("expected 9 embeddings, got %d", len(resp.Embeddings))
	}
	if calls != 3 {
		t.Errorf("expected 3 provider calls with BatchSize=3, got %d", calls)
	}
}

func TestEmbeddingService_BatchEmbed_Closed(t *testing.T) {
	svc := newServiceWithProvider("local", &mockProvider{})
	svc.Close() //nolint:errcheck

	_, err := svc.BatchEmbed(context.Background(), &BatchEmbedRequest{
		Texts: []string{"x"},
		Model: "m",
	})
	if err == nil {
		t.Fatal("expected EmbeddingServiceClosedError, got nil")
	}
	var target *EmbeddingServiceClosedError
	if !errors.As(err, &target) {
		t.Errorf("expected EmbeddingServiceClosedError, got %T: %v", err, err)
	}
}

func TestEmbeddingService_BatchEmbed_AggregatesUsage(t *testing.T) {
	mock := &mockProvider{
		embedFn: func(_ context.Context, texts []string, model string) (*EmbeddingResult, error) {
			embeddings := make([][]float32, len(texts))
			for i := range texts {
				embeddings[i] = []float32{0.1}
			}
			return &EmbeddingResult{
				Embeddings: embeddings,
				Model:      model,
				Usage:      &UsageInfo{PromptTokens: len(texts) * 10, TotalTokens: len(texts) * 10},
			}, nil
		},
	}
	svc := newServiceWithProvider("local", mock)

	resp, err := svc.BatchEmbed(context.Background(), &BatchEmbedRequest{
		Texts:     make([]string, 4),
		Model:     "m",
		BatchSize: 2,
	})
	if err != nil {
		t.Fatalf("BatchEmbed: %v", err)
	}
	// 2 batches x 2 texts x 10 tokens = 40 total tokens.
	if resp.TotalUsage.TotalTokens != 40 {
		t.Errorf("expected TotalTokens=40, got %d", resp.TotalUsage.TotalTokens)
	}
}

// ---------------------------------------------------------------------------
// ListModels tests
// ---------------------------------------------------------------------------

func TestEmbeddingService_ListModels_Success(t *testing.T) {
	mock := &mockProvider{
		modelsFn: func(_ context.Context) ([]ModelInfo, error) {
			return []ModelInfo{
				{Name: "text-embedding-3-small"},
				{Name: "text-embedding-3-large"},
			}, nil
		},
	}
	svc := newServiceWithProvider("openai", mock)

	resp, err := svc.ListModels(context.Background(), &ListEmbeddingModelsRequest{})
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(resp.Models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(resp.Models))
	}
}

func TestEmbeddingService_ListModels_ProviderNotFound(t *testing.T) {
	svc := NewEmbeddingService(WithDefaultProvider("missing"))

	_, err := svc.ListModels(context.Background(), &ListEmbeddingModelsRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var target *EmbeddingProviderNotFoundError
	if !errors.As(err, &target) {
		t.Errorf("expected EmbeddingProviderNotFoundError, got %T: %v", err, err)
	}
}

func TestEmbeddingService_ListModels_Closed(t *testing.T) {
	svc := newServiceWithProvider("local", &mockProvider{})
	svc.Close() //nolint:errcheck

	_, err := svc.ListModels(context.Background(), &ListEmbeddingModelsRequest{})
	if err == nil {
		t.Fatal("expected EmbeddingServiceClosedError, got nil")
	}
	var target *EmbeddingServiceClosedError
	if !errors.As(err, &target) {
		t.Errorf("expected EmbeddingServiceClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// RegisterProvider tests
// ---------------------------------------------------------------------------

func TestEmbeddingService_RegisterProvider_Success(t *testing.T) {
	svc := NewEmbeddingService()

	if err := svc.RegisterProvider("local", &mockProvider{}); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
}

func TestEmbeddingService_RegisterProvider_Duplicate(t *testing.T) {
	svc := NewEmbeddingService()
	svc.RegisterProvider("local", &mockProvider{}) //nolint:errcheck

	err := svc.RegisterProvider("local", &mockProvider{})
	if err == nil {
		t.Fatal("expected error for duplicate provider, got nil")
	}
	var target *EmbeddingProviderRegistrationError
	if !errors.As(err, &target) {
		t.Errorf("expected EmbeddingProviderRegistrationError, got %T: %v", err, err)
	}
	if target.Provider != "local" {
		t.Errorf("expected Provider=%q, got %q", "local", target.Provider)
	}
}

// ---------------------------------------------------------------------------
// DeregisterProvider tests
// ---------------------------------------------------------------------------

func TestEmbeddingService_DeregisterProvider_Success(t *testing.T) {
	svc := NewEmbeddingService()
	svc.RegisterProvider("local", &mockProvider{}) //nolint:errcheck

	if err := svc.DeregisterProvider("local"); err != nil {
		t.Fatalf("DeregisterProvider: %v", err)
	}

	// The provider must no longer be usable.
	_, err := svc.Embed(context.Background(), &EmbedRequest{
		Texts:    []string{"x"},
		Model:    "m",
		Provider: "local",
	})
	if err == nil {
		t.Fatal("expected error after deregistration, got nil")
	}
}

func TestEmbeddingService_DeregisterProvider_NotFound(t *testing.T) {
	svc := NewEmbeddingService()

	err := svc.DeregisterProvider("ghost")
	if err == nil {
		t.Fatal("expected EmbeddingProviderNotFoundError, got nil")
	}
	var target *EmbeddingProviderNotFoundError
	if !errors.As(err, &target) {
		t.Errorf("expected EmbeddingProviderNotFoundError, got %T: %v", err, err)
	}
	if target.Provider != "ghost" {
		t.Errorf("expected Provider=%q, got %q", "ghost", target.Provider)
	}
}

// ---------------------------------------------------------------------------
// Close tests
// ---------------------------------------------------------------------------

func TestEmbeddingService_Close_Idempotent(t *testing.T) {
	svc := newServiceWithProvider("local", &mockProvider{})

	if err := svc.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := svc.Close(); err != nil {
		t.Errorf("second Close (idempotent): %v", err)
	}
}

func TestEmbeddingService_Close_PropagatesProviderErrors(t *testing.T) {
	closeErr := errors.New("provider close failed")
	mock := &mockProvider{
		closeFn: func() error { return closeErr },
	}
	svc := newServiceWithProvider("local", mock)

	err := svc.Close()
	if err == nil {
		t.Fatal("expected error from provider Close, got nil")
	}
	var target *EmbeddingCloseError
	if !errors.As(err, &target) {
		t.Errorf("expected EmbeddingCloseError, got %T: %v", err, err)
	}
}
