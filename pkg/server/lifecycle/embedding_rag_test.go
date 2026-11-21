//go:build qrdb_vector

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

package lifecycle_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// ---------------------------------------------------------------------------
// Fake OpenAI-compatible embedding server
// ---------------------------------------------------------------------------

// fakeEmbeddingServer starts an httptest.Server that responds to:
//
//	POST /v1/embeddings  — returns a 3-dimensional embedding per input text.
//	GET  /v1/models      — returns a single model named "fake-model".
//
// The caller must close the server when the test is done.
func fakeEmbeddingServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		var reqBody struct {
			Input []string `json:"input"`
			Model string   `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		type embData struct {
			Object    string    `json:"object"`
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		}
		type usageResp struct {
			PromptTokens int `json:"prompt_tokens"`
			TotalTokens  int `json:"total_tokens"`
		}
		type embResp struct {
			Object string    `json:"object"`
			Data   []embData `json:"data"`
			Model  string    `json:"model"`
			Usage  usageResp `json:"usage"`
		}
		data := make([]embData, len(reqBody.Input))
		for i := range reqBody.Input {
			data[i] = embData{
				Object:    "embedding",
				Embedding: []float32{0.1, 0.2, 0.3},
				Index:     i,
			}
		}
		model := reqBody.Model
		if model == "" {
			model = "fake-model"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(embResp{
			Object: "list",
			Data:   data,
			Model:  model,
			Usage:  usageResp{PromptTokens: len(reqBody.Input), TotalTokens: len(reqBody.Input)},
		})
	})

	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		type modelData struct {
			ID     string `json:"id"`
			Object string `json:"object"`
		}
		type modelsResp struct {
			Object string      `json:"object"`
			Data   []modelData `json:"data"`
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(modelsResp{
			Object: "list",
			Data:   []modelData{{ID: "fake-model", Object: "model"}},
		})
	})

	return httptest.NewServer(mux)
}

// providerCfg builds an EmbeddingProviderConfig pointing at srv. MaxRetries=0
// ensures a single attempt so tests do not block on retry back-off.
func providerCfg(srv *httptest.Server, name string) qrdbconfig.EmbeddingProviderConfig {
	return qrdbconfig.EmbeddingProviderConfig{
		Name:       name,
		BaseURL:    srv.URL + "/v1",
		Model:      "fake-model",
		Timeout:    "5s",
		MaxRetries: 0,
	}
}

// ---------------------------------------------------------------------------
// BuildServices with embedding + RAG wired
// ---------------------------------------------------------------------------

// TestBuildServices_EmbeddingAndRAGNilWhenNoProviders confirms that Embedding
// and RAG remain nil when no providers are configured (default mode).
func TestBuildServices_EmbeddingAndRAGNilWhenNoProviders(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:     host,
		WatchHub: hub,
		DataDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()
	bindNodeRAG(node)

	if node.Embedding != nil {
		t.Error("Embedding must be nil when no providers are configured")
	}
	if node.RAG != nil {
		t.Error("RAG must be nil when no providers are configured")
	}
}

// TestBuildServices_EmbeddingProviderDefaultName verifies that an empty
// provider Name defaults to "default" inside buildEmbeddingAndRAG.
func TestBuildServices_EmbeddingProviderDefaultName(t *testing.T) {
	srv := fakeEmbeddingServer(t)
	defer srv.Close()

	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	cfg := providerCfg(srv, "")
	// Empty name — buildEmbeddingAndRAG must replace it with "default".
	cfg.Name = ""

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:     host,
		WatchHub: hub,
		DataDir:  t.TempDir(),
		Logger:   slog.Default(),
		EmbeddingConfig: qrdbconfig.EmbeddingConfig{
			DefaultProvider: "default",
			Providers:       []qrdbconfig.EmbeddingProviderConfig{cfg},
		},
	})
	if err != nil {
		t.Fatalf("BuildServices empty provider name: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()
	bindNodeRAG(node)

	if node.Embedding == nil {
		t.Error("Embedding is nil when provider name defaulted to 'default'")
	}
}

// ---------------------------------------------------------------------------
// openAIProviderAdapter — Embed, Models, Close, convertEmbeddingResult
// ---------------------------------------------------------------------------

// TestOpenAIProviderAdapter_Embed exercises openAIProviderAdapter.Embed and
// convertEmbeddingResult by calling EmbeddingService.Embed through the node.
// The fake HTTP server handles the actual /v1/embeddings call.
func TestOpenAIProviderAdapter_Embed(t *testing.T) {
	srv := fakeEmbeddingServer(t)
	defer srv.Close()

	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:     host,
		WatchHub: hub,
		DataDir:  t.TempDir(),
		Logger:   slog.Default(),
		EmbeddingConfig: qrdbconfig.EmbeddingConfig{
			DefaultProvider: "fake",
			Providers:       []qrdbconfig.EmbeddingProviderConfig{providerCfg(srv, "fake")},
		},
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()
	bindNodeRAG(node)

	resp, err := node.Embedding.Embed(context.Background(), &service.EmbedRequest{
		Texts:    []string{"hello world"},
		Model:    "fake-model",
		Provider: "fake",
	})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(resp.Embeddings) == 0 {
		t.Error("Embed returned empty embeddings")
	}
	if len(resp.Embeddings[0]) != 3 {
		t.Errorf("expected 3-dimensional embedding, got %d", len(resp.Embeddings[0]))
	}
	// Verify model field was propagated (exercises convertEmbeddingResult).
	if resp.Model != "fake-model" {
		t.Errorf("expected model 'fake-model', got %q", resp.Model)
	}
}

// TestOpenAIProviderAdapter_EmbedUsageInfo verifies the UsageInfo conversion
// in convertEmbeddingResult: Usage must be non-nil and have non-zero tokens.
func TestOpenAIProviderAdapter_EmbedUsageInfo(t *testing.T) {
	srv := fakeEmbeddingServer(t)
	defer srv.Close()

	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:     host,
		WatchHub: hub,
		DataDir:  t.TempDir(),
		Logger:   slog.Default(),
		EmbeddingConfig: qrdbconfig.EmbeddingConfig{
			DefaultProvider: "fake",
			Providers:       []qrdbconfig.EmbeddingProviderConfig{providerCfg(srv, "fake")},
		},
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()
	bindNodeRAG(node)

	resp, err := node.Embedding.Embed(context.Background(), &service.EmbedRequest{
		Texts:    []string{"one", "two"},
		Model:    "fake-model",
		Provider: "fake",
	})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	// Usage is non-nil when the fake server reports prompt/total tokens.
	if resp.Usage == nil {
		t.Error("Usage is nil; expected non-nil from fake server")
	}
}

// TestOpenAIProviderAdapter_Models exercises openAIProviderAdapter.Models via
// EmbeddingService.ListModels.
func TestOpenAIProviderAdapter_Models(t *testing.T) {
	srv := fakeEmbeddingServer(t)
	defer srv.Close()

	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:     host,
		WatchHub: hub,
		DataDir:  t.TempDir(),
		Logger:   slog.Default(),
		EmbeddingConfig: qrdbconfig.EmbeddingConfig{
			DefaultProvider: "fake",
			Providers:       []qrdbconfig.EmbeddingProviderConfig{providerCfg(srv, "fake")},
		},
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()
	bindNodeRAG(node)

	resp, err := node.Embedding.ListModels(context.Background(), &service.ListEmbeddingModelsRequest{
		Provider: "fake",
	})
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(resp.Models) == 0 {
		t.Error("ListModels returned no models")
	}
	if resp.Models[0].Name != "fake-model" {
		t.Errorf("expected 'fake-model', got %q", resp.Models[0].Name)
	}
}

// TestOpenAIProviderAdapter_Close exercises openAIProviderAdapter.Close via
// EmbeddingService.Close. Close releases idle HTTP connections and must not
// error on the first call.
func TestOpenAIProviderAdapter_Close(t *testing.T) {
	srv := fakeEmbeddingServer(t)
	defer srv.Close()

	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:     host,
		WatchHub: hub,
		DataDir:  t.TempDir(),
		Logger:   slog.Default(),
		EmbeddingConfig: qrdbconfig.EmbeddingConfig{
			DefaultProvider: "fake",
			Providers:       []qrdbconfig.EmbeddingProviderConfig{providerCfg(srv, "fake")},
		},
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}

	// Close embedding service — exercises openAIProviderAdapter.Close.
	if err := node.Embedding.Close(); err != nil {
		t.Errorf("Embedding.Close: %v", err)
	}
	// node.Stop cleans up remaining resources (vector storage, etc.).
	_ = node.Stop(context.Background())
}

// TestOpenAIProviderAdapter_DefaultModelSharesCacheEntry exercises
// openAIProviderAdapter.DefaultModel through a cache-enabled node: a request
// with no model resolves to the provider's configured model and is served from
// the entry a request naming that model stored.
func TestOpenAIProviderAdapter_DefaultModelSharesCacheEntry(t *testing.T) {
	srv := fakeEmbeddingServer(t)
	defer srv.Close()

	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:     host,
		WatchHub: hub,
		DataDir:  t.TempDir(),
		Logger:   slog.Default(),
		EmbeddingConfig: qrdbconfig.EmbeddingConfig{
			DefaultProvider: "fake",
			CacheCapacity:   16,
			Providers:       []qrdbconfig.EmbeddingProviderConfig{providerCfg(srv, "fake")},
		},
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()
	bindNodeRAG(node)

	ctx := context.Background()
	first, err := node.Embedding.Embed(ctx, &service.EmbedRequest{Texts: []string{"shared"}})
	if err != nil {
		t.Fatalf("no-model Embed: %v", err)
	}
	if first.Cached {
		t.Error("first Embed must reach the provider")
	}
	if first.Model != "fake-model" {
		t.Errorf("no-model Embed used %q, want the configured %q", first.Model, "fake-model")
	}

	second, err := node.Embedding.Embed(ctx, &service.EmbedRequest{Texts: []string{"shared"}, Model: "fake-model"})
	if err != nil {
		t.Fatalf("default-model Embed: %v", err)
	}
	if !second.Cached {
		t.Error("default-model request after a no-model request: want a cache hit")
	}
}

// ---------------------------------------------------------------------------
// ragIngestorAdapter.Ingest and ragRetrieverAdapter.Retrieve
// ---------------------------------------------------------------------------

// TestRAGIngestorAdapter_Ingest exercises ragIngestorAdapter.Ingest by calling
// RAGServicer.IngestDocument through the node. The fake server handles
// embeddings; the in-memory vector store (pebble, temp dir) handles persistence.
func TestRAGIngestorAdapter_Ingest(t *testing.T) {
	srv := fakeEmbeddingServer(t)
	defer srv.Close()

	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:     host,
		WatchHub: hub,
		DataDir:  t.TempDir(),
		Logger:   slog.Default(),
		EmbeddingConfig: qrdbconfig.EmbeddingConfig{
			DefaultProvider: "fake",
			Providers:       []qrdbconfig.EmbeddingProviderConfig{providerCfg(srv, "fake")},
		},
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()
	bindNodeRAG(node)

	resp, err := node.RAG.IngestDocument(context.Background(), &service.IngestDocumentRequest{
		DocumentID: "doc-1",
		Text:       strings.Repeat("The quick brown fox jumps over the lazy dog. ", 10),
		Namespace:  "test-ns",
		Model:      "fake-model",
		Provider:   "fake",
	})
	if err != nil {
		t.Fatalf("IngestDocument: %v", err)
	}
	if resp.DocumentID != "doc-1" {
		t.Errorf("expected DocumentID 'doc-1', got %q", resp.DocumentID)
	}
	if resp.ChunkCount == 0 {
		t.Error("expected at least one chunk")
	}
	if resp.Namespace != "test-ns" {
		t.Errorf("expected Namespace 'test-ns', got %q", resp.Namespace)
	}
}

// TestRAGService_DeleteDocumentRemovesVectors proves on the node's real
// vector and KV services that deleting a document removes every one of its
// chunk vectors, not only the chunk text, and leaves the other document's.
func TestRAGService_DeleteDocumentRemovesVectors(t *testing.T) {
	srv := fakeEmbeddingServer(t)
	defer srv.Close()

	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:     host,
		WatchHub: hub,
		DataDir:  t.TempDir(),
		Logger:   slog.Default(),
		EmbeddingConfig: qrdbconfig.EmbeddingConfig{
			DefaultProvider: "fake",
			Providers:       []qrdbconfig.EmbeddingProviderConfig{providerCfg(srv, "fake")},
		},
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()
	bindNodeRAG(node)

	ctx := context.Background()
	const ns = "del-ns"
	ingest := func(id, text string) int {
		t.Helper()
		resp, err := node.RAG.IngestDocument(ctx, &service.IngestDocumentRequest{
			DocumentID: id,
			Text:       text,
			Namespace:  ns,
			Model:      "fake-model",
			Provider:   "fake",
			ChunkConfig: &service.ChunkConfig{
				Strategy:  "fixed",
				ChunkSize: 32,
				Overlap:   8,
			},
		})
		if err != nil {
			t.Fatalf("IngestDocument %s: %v", id, err)
		}
		return resp.ChunkCount
	}
	gone := ingest("doc-gone", strings.Repeat("Deleted documents leave nothing. ", 8))
	kept := ingest("doc-kept", strings.Repeat("Kept documents stay. ", 4))

	if _, err := node.RAG.DeleteDocument(ctx, &service.DeleteRAGDocumentRequest{
		DocumentID: "doc-gone",
		Namespace:  ns,
	}); err != nil {
		t.Fatalf("DeleteDocument: %v", err)
	}

	list, err := node.Vector.ListVectors(ctx, &service.ListVectorsRequest{
		Namespace:       ns,
		Limit:           1000,
		IncludeMetadata: true,
	})
	if err != nil {
		t.Fatalf("ListVectors: %v", err)
	}
	if len(list.Vectors) != kept {
		t.Fatalf("namespace holds %d vectors after deleting a %d-chunk document, want doc-kept's %d",
			len(list.Vectors), gone, kept)
	}
	for _, v := range list.Vectors {
		if v.Metadata["doc_id"] != "doc-kept" {
			t.Errorf("vector %d of %v survived the delete", v.ID, v.Metadata["doc_id"])
		}
	}

	qResp, err := node.RAG.Query(ctx, &service.RAGQueryRequest{
		Query:     "Deleted documents",
		Namespace: ns,
		TopK:      gone + kept,
		Model:     "fake-model",
		Provider:  "fake",
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(qResp.Results) != kept {
		t.Fatalf("query returned %d results, want doc-kept's %d", len(qResp.Results), kept)
	}
	for _, r := range qResp.Results {
		if r.DocumentID != "doc-kept" {
			t.Errorf("query after delete returned a chunk of %q", r.DocumentID)
		}
	}
}

// TestRAGRetrieverAdapter_RetrieveWithFilter exercises the metadata filter
// conversion branch in ragRetrieverAdapter.Retrieve (req.Filter non-empty).
func TestRAGRetrieverAdapter_RetrieveWithFilter(t *testing.T) {
	srv := fakeEmbeddingServer(t)
	defer srv.Close()

	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:     host,
		WatchHub: hub,
		DataDir:  t.TempDir(),
		Logger:   slog.Default(),
		EmbeddingConfig: qrdbconfig.EmbeddingConfig{
			DefaultProvider: "fake",
			Providers:       []qrdbconfig.EmbeddingProviderConfig{providerCfg(srv, "fake")},
		},
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()
	bindNodeRAG(node)

	ctx := context.Background()

	_, err = node.RAG.IngestDocument(ctx, &service.IngestDocumentRequest{
		DocumentID: "doc-filter",
		Text:       strings.Repeat("Distributed key-value store concepts. ", 10),
		Namespace:  "filter-ns",
		Model:      "fake-model",
		Provider:   "fake",
	})
	if err != nil {
		t.Fatalf("IngestDocument for filter test: %v", err)
	}

	// Query with a filter exercises the Filter → map[string]interface{} branch
	// inside ragRetrieverAdapter.Retrieve.
	qResp, err := node.RAG.Query(ctx, &service.RAGQueryRequest{
		Query:     "key-value",
		Namespace: "filter-ns",
		TopK:      2,
		Model:     "fake-model",
		Provider:  "fake",
		Filter:    map[string]string{"namespace": "filter-ns"},
	})
	if err != nil {
		t.Fatalf("Query with filter: %v", err)
	}
	if qResp == nil {
		t.Error("Query with filter returned nil response")
	}
}

// TestBuildServices_EmbeddingProviderError_DuplicateName verifies that
// registering two providers with the same name triggers an
// EmbeddingProviderError from buildEmbeddingAndRAG.
func TestBuildServices_EmbeddingProviderError_DuplicateName(t *testing.T) {
	srv := fakeEmbeddingServer(t)
	defer srv.Close()

	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	// Two providers with the same name cause RegisterProvider to fail on the
	// second registration, which buildEmbeddingAndRAG wraps as
	// EmbeddingProviderError.
	p := providerCfg(srv, "dup")
	_, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:     host,
		WatchHub: hub,
		DataDir:  t.TempDir(),
		Logger:   slog.Default(),
		EmbeddingConfig: qrdbconfig.EmbeddingConfig{
			DefaultProvider: "dup",
			Providers:       []qrdbconfig.EmbeddingProviderConfig{p, p},
		},
	})
	if err == nil {
		t.Fatal("expected error for duplicate provider name, got nil")
	}
	var target *lifecycle.EmbeddingProviderError
	if !isEmbeddingProviderError(err, &target) {
		t.Errorf("expected *EmbeddingProviderError, got %T: %v", err, err)
	}
}

// isEmbeddingProviderError is a helper that avoids importing errors.As in the
// assertion (it just does the type assertion directly since we own the type).
func isEmbeddingProviderError(err error, target **lifecycle.EmbeddingProviderError) bool {
	if e, ok := err.(*lifecycle.EmbeddingProviderError); ok {
		*target = e
		return true
	}
	return false
}
