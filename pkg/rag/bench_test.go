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

package rag

import (
	"context"
	"strconv"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/rag/chunker"
)

// ---------------------------------------------------------------------------
// Benchmark helpers
// ---------------------------------------------------------------------------

// benchEmbProvider is an allocation-free EmbedProvider that returns
// pre-allocated zero vectors without touching the heap on each call.
type benchEmbProvider struct {
	embeddings [][]float32
}

var _ EmbedProvider = (*benchEmbProvider)(nil)

func newBenchEmbProvider(dims, count int) *benchEmbProvider {
	embs := make([][]float32, count)
	vec := make([]float32, dims)
	for i := range embs {
		embs[i] = vec // same backing array – safe for read-only benchmarks
	}
	return &benchEmbProvider{embeddings: embs}
}

func (p *benchEmbProvider) Embed(_ context.Context, req *EmbedRequest) (*EmbedResponse, error) {
	embs := make([][]float32, len(req.Texts))
	copy(embs, p.embeddings)
	return &EmbedResponse{Embeddings: embs}, nil
}

func (p *benchEmbProvider) BatchEmbed(_ context.Context, req *BatchEmbedRequest) (*BatchEmbedResponse, error) {
	embs := make([][]float32, len(req.Texts))
	copy(embs, p.embeddings)
	return &BatchEmbedResponse{Embeddings: embs}, nil
}

// benchVecStore is an allocation-minimal VectorStore that accepts upserts and
// returns a fixed set of pre-populated search results.
type benchVecStore struct {
	results []*VecSearchResult
}

var _ VectorStore = (*benchVecStore)(nil)

func newBenchVecStore(namespace, docID string, chunkCount int) *benchVecStore {
	results := make([]*VecSearchResult, chunkCount)
	for i := range results {
		results[i] = &VecSearchResult{
			ID:       chunkVectorID(namespace, docID, i),
			Distance: float32(i) * 0.01,
			Metadata: map[string]interface{}{
				"doc_id":      docID,
				"chunk_index": strconv.Itoa(i),
				"namespace":   namespace,
			},
		}
	}
	return &benchVecStore{results: results}
}

func (v *benchVecStore) UpsertVector(_ context.Context, _ *UpsertVecRequest) (*UpsertVecResponse, error) {
	return &UpsertVecResponse{Success: true}, nil
}

func (v *benchVecStore) SearchSimilar(_ context.Context, req *SearchVecRequest) (*SearchVecResponse, error) {
	k := req.K
	if k > len(v.results) {
		k = len(v.results)
	}
	return &SearchVecResponse{Results: v.results[:k]}, nil
}

// benchKVStore is an in-memory KVStore backed by a pre-populated map.
// It is not safe for concurrent writes, but benchmarks only read from it.
type benchKVStore struct {
	store map[string][]byte
}

var _ KVStore = (*benchKVStore)(nil)

func newBenchKVStore() *benchKVStore {
	return &benchKVStore{store: make(map[string][]byte)}
}

func (k *benchKVStore) Put(_ context.Context, key string, value []byte) error {
	k.store[key] = value
	return nil
}

func (k *benchKVStore) Get(_ context.Context, key string) ([]byte, error) {
	v, ok := k.store[key]
	if !ok {
		return nil, &RetrievalError{}
	}
	return v, nil
}

func (k *benchKVStore) Delete(_ context.Context, key string) error {
	delete(k.store, key)
	return nil
}

func (k *benchKVStore) Scan(_ context.Context, prefix string) (map[string][]byte, error) {
	result := make(map[string][]byte)
	for key, val := range k.store {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			result[key] = val
		}
	}
	return result, nil
}

// benchDoc returns a synthetic document of approximately targetBytes characters.
func benchDoc(targetBytes int) string {
	const sentence = "The quick brown fox jumps over the lazy dog. "
	buf := make([]byte, 0, targetBytes)
	for len(buf) < targetBytes {
		buf = append(buf, sentence...)
	}
	return string(buf[:targetBytes])
}

// ---------------------------------------------------------------------------
// Ingestor benchmarks
// ---------------------------------------------------------------------------

// BenchmarkIngestor_Ingest measures the end-to-end ingestion pipeline:
// chunking, batch embedding, vector upsert, and KV persistence for a 4 KB
// document using the default fixed-size chunking strategy.
func BenchmarkIngestor_Ingest(b *testing.B) {
	const (
		namespace = "bench-ns"
		docID     = "bench-doc-001"
		docSize   = 4 * 1024 // 4 KB
		dims      = 128
	)

	text := benchDoc(docSize)
	embProvider := newBenchEmbProvider(dims, 32)
	vecStore := &benchVecStore{}
	kvStore := newBenchKVStore()
	ing := NewIngestor(embProvider, vecStore, kvStore, 0)

	req := &IngestRequest{
		Document: &Document{
			ID:   docID,
			Text: text,
		},
		Namespace:   namespace,
		ChunkConfig: chunker.DefaultConfig(),
	}

	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// Use a unique document ID per iteration so the KV store does not
		// accumulate state that could mask allocation regressions.
		req.Document.ID = docID + strconv.Itoa(i)
		if _, err := ing.Ingest(ctx, req); err != nil {
			b.Fatalf("Ingest: %v", err)
		}
	}
}

// BenchmarkIngestor_Ingest_LargeDoc measures ingestion throughput for a
// larger 64 KB document, highlighting how pipeline latency scales with
// document size and chunk count.
func BenchmarkIngestor_Ingest_LargeDoc(b *testing.B) {
	const (
		namespace = "bench-ns"
		docID     = "bench-large-doc"
		docSize   = 64 * 1024 // 64 KB
		dims      = 128
	)

	text := benchDoc(docSize)
	// Pre-allocate enough embedding slots for a 64 KB doc with 512-char chunks
	// and 50-char overlap → roughly 140 chunks.
	embProvider := newBenchEmbProvider(dims, 256)
	vecStore := &benchVecStore{}
	kvStore := newBenchKVStore()
	ing := NewIngestor(embProvider, vecStore, kvStore, 0)

	req := &IngestRequest{
		Document: &Document{
			ID:   docID,
			Text: text,
		},
		Namespace:   namespace,
		ChunkConfig: chunker.DefaultConfig(),
	}

	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		req.Document.ID = docID + strconv.Itoa(i)
		if _, err := ing.Ingest(ctx, req); err != nil {
			b.Fatalf("Ingest large: %v", err)
		}
	}
}

// ---------------------------------------------------------------------------
// Retriever benchmarks
// ---------------------------------------------------------------------------

// BenchmarkRetriever_Retrieve measures the retrieval pipeline latency:
// query embedding, similarity search, and KV chunk-text hydration for a
// result set of 5 chunks (the default TopK).
func BenchmarkRetriever_Retrieve(b *testing.B) {
	const (
		namespace  = "bench-ns"
		docID      = "bench-doc-001"
		chunkCount = 5
		dims       = 128
	)

	embProvider := newBenchEmbProvider(dims, 1)
	vecStore := newBenchVecStore(namespace, docID, chunkCount)

	// Pre-populate the KV store with chunk text so hydration succeeds.
	kvStore := newBenchKVStore()
	ctx := context.Background()
	chunkText := []byte("A benchmark chunk of text content.")
	for i := 0; i < chunkCount; i++ {
		key := chunkKVKey(namespace, docID, i)
		if err := kvStore.Put(ctx, key, chunkText); err != nil {
			b.Fatalf("KV Put: %v", err)
		}
	}

	ret := NewRetriever(embProvider, vecStore, kvStore)
	req := &RetrieveRequest{
		Query:     "What does the quick brown fox do?",
		Namespace: namespace,
		TopK:      chunkCount,
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if _, err := ret.Retrieve(ctx, req); err != nil {
			b.Fatalf("Retrieve: %v", err)
		}
	}
}

// BenchmarkRetriever_Retrieve_TopK20 measures retrieval throughput for a
// larger result set (TopK=20) to show how hydration cost scales with the
// number of returned chunks.
func BenchmarkRetriever_Retrieve_TopK20(b *testing.B) {
	const (
		namespace  = "bench-ns"
		docID      = "bench-doc-001"
		chunkCount = 20
		dims       = 128
	)

	embProvider := newBenchEmbProvider(dims, 1)
	vecStore := newBenchVecStore(namespace, docID, chunkCount)

	kvStore := newBenchKVStore()
	ctx := context.Background()
	chunkText := []byte("A benchmark chunk of text content for top-k twenty retrieval.")
	for i := 0; i < chunkCount; i++ {
		key := chunkKVKey(namespace, docID, i)
		if err := kvStore.Put(ctx, key, chunkText); err != nil {
			b.Fatalf("KV Put: %v", err)
		}
	}

	ret := NewRetriever(embProvider, vecStore, kvStore)
	req := &RetrieveRequest{
		Query:     "What does the quick brown fox do?",
		Namespace: namespace,
		TopK:      chunkCount,
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if _, err := ret.Retrieve(ctx, req); err != nil {
			b.Fatalf("Retrieve TopK20: %v", err)
		}
	}
}
