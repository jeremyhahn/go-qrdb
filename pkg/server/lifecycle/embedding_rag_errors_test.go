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
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// failingEmbeddingServer returns 500 for both /v1/embeddings and /v1/models so
// that provider Embed/Models calls surface an error through the adapter. The
// provider constructor does not probe the server, so registration still
// succeeds and buildEmbeddingAndRAG returns a wired (but failing) provider.
func failingEmbeddingServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	return httptest.NewServer(mux)
}

func newFailingEmbeddingNode(t *testing.T) *lifecycle.Node {
	t.Helper()
	srv := failingEmbeddingServer(t)
	t.Cleanup(srv.Close)

	host := newLocalHost()
	t.Cleanup(func() { _ = host.Close() })

	hub := watch.NewHub()
	t.Cleanup(hub.Close)

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:     host,
		WatchHub: hub,
		DataDir:  t.TempDir(),
		Logger:   slog.Default(),
		EmbeddingConfig: qrdbconfig.EmbeddingConfig{
			DefaultProvider: "boom",
			Providers: []qrdbconfig.EmbeddingProviderConfig{{
				Name:       "boom",
				BaseURL:    srv.URL + "/v1",
				Model:      "fake-model",
				Timeout:    "5s",
				MaxRetries: 0,
			}},
		},
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	t.Cleanup(func() { _ = node.Stop(context.Background()) })
	return node
}

// TestOpenAIProviderAdapter_EmbedError exercises the error branch of
// openAIProviderAdapter.Embed: the provider's underlying HTTP call fails and
// the adapter must propagate the error (returning nil result).
func TestOpenAIProviderAdapter_EmbedError(t *testing.T) {
	node := newFailingEmbeddingNode(t)

	_, err := node.Embedding.Embed(context.Background(), &service.EmbedRequest{
		Texts:    []string{"hello"},
		Model:    "fake-model",
		Provider: "boom",
	})
	if err == nil {
		t.Fatal("expected error from failing embedding server, got nil")
	}
}

// TestOpenAIProviderAdapter_ModelsError exercises the error branch of
// openAIProviderAdapter.Models when the upstream /v1/models call fails.
func TestOpenAIProviderAdapter_ModelsError(t *testing.T) {
	node := newFailingEmbeddingNode(t)

	_, err := node.Embedding.ListModels(context.Background(), &service.ListEmbeddingModelsRequest{
		Provider: "boom",
	})
	if err == nil {
		t.Fatal("expected error from failing models endpoint, got nil")
	}
}

// TestRAGIngestorAdapter_IngestError exercises the error branch of
// ragIngestorAdapter.Ingest: embedding fails during ingestion, so Ingest must
// return an error rather than a result.
func TestRAGIngestorAdapter_IngestError(t *testing.T) {
	node := newFailingEmbeddingNode(t)

	_, err := node.RAG.IngestDocument(context.Background(), &service.IngestDocumentRequest{
		DocumentID: "doc-err",
		Text:       "some text that cannot be embedded",
		Namespace:  "err-ns",
		Model:      "fake-model",
		Provider:   "boom",
	})
	if err == nil {
		t.Fatal("expected ingest error from failing embedding server, got nil")
	}
}

// TestRAGRetrieverAdapter_RetrieveError exercises the error branch of
// ragRetrieverAdapter.Retrieve: the query embedding fails, so Retrieve must
// propagate the error.
func TestRAGRetrieverAdapter_RetrieveError(t *testing.T) {
	node := newFailingEmbeddingNode(t)

	_, err := node.RAG.Query(context.Background(), &service.RAGQueryRequest{
		Query:     "anything",
		Namespace: "err-ns",
		TopK:      3,
		Model:     "fake-model",
		Provider:  "boom",
	})
	if err == nil {
		t.Fatal("expected retrieve error from failing embedding server, got nil")
	}
}
