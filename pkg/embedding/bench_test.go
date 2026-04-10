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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fixedEmbeddingHandler is a minimal HTTP handler that returns a pre-encoded
// embedding response without any per-request allocation. The response body is
// computed once in newBenchServer and reused for every request.
type fixedEmbeddingHandler struct {
	body []byte
}

func (h *fixedEmbeddingHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write(h.body)
}

// newBenchServer starts a test HTTP server that returns n embeddings of the
// given dimensionality.  The server is closed by b.Cleanup.
func newBenchServer(b *testing.B, model string, dims, count int) (baseURL string) {
	b.Helper()

	data := make([]embeddingData, count)
	vec := make([]float32, dims)
	for j := range vec {
		vec[j] = 0.5
	}
	for i := range data {
		data[i] = embeddingData{
			Object:    "embedding",
			Embedding: vec,
			Index:     i,
		}
	}
	resp := embeddingResponse{
		Object: "list",
		Data:   data,
		Model:  model,
		Usage:  usageResponse{PromptTokens: count * 5, TotalTokens: count * 5},
	}
	body, err := json.Marshal(resp)
	if err != nil {
		b.Fatalf("marshal bench response: %v", err)
	}

	srv := httptest.NewServer(&fixedEmbeddingHandler{body: body})
	b.Cleanup(srv.Close)
	return srv.URL + "/v1"
}

// newBenchClient creates a Client pointed at baseURL with retries disabled so
// that benchmarks measure only the happy-path round trip.
func newBenchClient(b *testing.B, baseURL string) *Client {
	b.Helper()
	cfg := DefaultProviderConfig()
	cfg.BaseURL = baseURL
	cfg.Model = "nomic-embed-text"
	cfg.MaxRetries = 0
	c, err := NewClient(cfg)
	if err != nil {
		b.Fatalf("NewClient: %v", err)
	}
	return c
}

// BenchmarkCache_Get measures the throughput of Cache.Get for a cache that is
// fully warm (every key is present). The benchmark exercises the read path
// including key hashing, RWMutex acquisition, and LRU promotion.
func BenchmarkCache_Get(b *testing.B) {
	const capacity = 1024
	const dims = 128

	cache, err := NewCache(capacity)
	if err != nil {
		b.Fatalf("NewCache: %v", err)
	}

	vec := make([]float32, dims)
	for i := range vec {
		vec[i] = float32(i) * 0.01
	}

	// Warm the cache so every lookup hits.
	model := "bench-model"
	for i := 0; i < capacity; i++ {
		text := string(rune('a'+i%26)) + string(rune(i))
		cache.Put(model, text, vec)
	}

	// Use the first key for every iteration to guarantee a cache hit.
	hitText := string(rune('a')) + string(rune(0))

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, _ = cache.Get(model, hitText)
	}
}

// BenchmarkCache_Put measures the throughput of Cache.Put under an eviction
// pressure where every insertion displaces the LRU entry. This exercises the
// key hashing, write lock, doubly-linked-list manipulation, and eviction path.
func BenchmarkCache_Put(b *testing.B) {
	const capacity = 256
	const dims = 128

	cache, err := NewCache(capacity)
	if err != nil {
		b.Fatalf("NewCache: %v", err)
	}

	// Fill the cache to capacity so every subsequent Put triggers an eviction.
	vec := make([]float32, dims)
	for i := range vec {
		vec[i] = 0.1
	}
	model := "bench-model"
	for i := 0; i < capacity; i++ {
		cache.Put(model, string(rune(i)), vec)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// Use a unique key per iteration to always trigger the eviction path.
		cache.Put(model, string(rune(capacity+i)), vec)
	}
}

// BenchmarkCacheKey measures the throughput of cacheKey, which performs a
// SHA-256 hash over (model, NUL, text) and encodes the first 16 bytes as hex.
// This is on the critical path of every Cache.Get and Cache.Put call.
func BenchmarkCacheKey(b *testing.B) {
	model := "nomic-embed-text"
	text := "The quick brown fox jumps over the lazy dog"

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = cacheKey(model, text)
	}
}

// BenchmarkClient_Embed measures the end-to-end latency of Client.Embed against
// a local in-process HTTP server. This captures JSON serialisation, HTTP round
// trip, and response unmarshalling costs for a single-text request.
func BenchmarkClient_Embed(b *testing.B) {
	const dims = 128
	baseURL := newBenchServer(b, "nomic-embed-text", dims, 1)
	client := newBenchClient(b, baseURL)

	texts := []string{"The quick brown fox jumps over the lazy dog"}
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if _, err := client.Embed(ctx, texts, ""); err != nil {
			b.Fatalf("Embed: %v", err)
		}
	}
}

// BenchmarkClient_EmbedBatch measures the throughput of Client.Embed when
// embedding a batch of 32 texts in a single request.  This is the dominant
// production usage pattern for the RAG ingest pipeline.
func BenchmarkClient_EmbedBatch(b *testing.B) {
	const batchSize = 32
	const dims = 128
	baseURL := newBenchServer(b, "nomic-embed-text", dims, batchSize)
	client := newBenchClient(b, baseURL)

	texts := make([]string, batchSize)
	for i := range texts {
		texts[i] = "benchmark text sample number " + string(rune('0'+i%10))
	}
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if _, err := client.Embed(ctx, texts, ""); err != nil {
			b.Fatalf("Embed batch: %v", err)
		}
	}
}
