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
	"errors"
	"net/http"
	"testing"
	"time"
)

// ollamaProviderHandler routes /v1/embeddings and /v1/models for Ollama tests.
func ollamaProviderHandler(dims int, model string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			resp := modelListResponse{Data: []modelData{{ID: model, Object: "model"}}}
			b, _ := json.Marshal(resp)
			w.Header().Set("Content-Type", "application/json")
			w.Write(b)
		default:
			var req embeddingRequest
			json.NewDecoder(r.Body).Decode(&req)
			n := len(req.Input)
			if n == 0 {
				n = 1
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(makeEmbeddingResponse(model, dims, n))
		}
	}
}

// ---------------------------------------------------------------------------
// DefaultOllamaConfig
// ---------------------------------------------------------------------------

func TestDefaultOllamaConfig_Defaults(t *testing.T) {
	cfg := DefaultOllamaConfig()
	if cfg.BaseURL != DefaultOllamaBaseURL {
		t.Errorf("expected BaseURL=%q, got %q", DefaultOllamaBaseURL, cfg.BaseURL)
	}
	if cfg.Model != DefaultOllamaModel {
		t.Errorf("expected Model=%q, got %q", DefaultOllamaModel, cfg.Model)
	}
	if cfg.Timeout != DefaultOllamaTimeout {
		t.Errorf("expected Timeout=%v, got %v", DefaultOllamaTimeout, cfg.Timeout)
	}
	if cfg.MaxRetries != 3 {
		t.Errorf("expected MaxRetries=3, got %d", cfg.MaxRetries)
	}
}

func TestDefaultOllamaConfig_RetryBackoff(t *testing.T) {
	cfg := DefaultOllamaConfig()
	if cfg.RetryBackoff != time.Second {
		t.Errorf("expected RetryBackoff=1s, got %v", cfg.RetryBackoff)
	}
}

// ---------------------------------------------------------------------------
// NewOllamaProvider
// ---------------------------------------------------------------------------

func TestNewOllamaProvider_Success(t *testing.T) {
	srv, _ := embeddingServer(t, ollamaProviderHandler(768, "nomic-embed-text"))
	cfg := OllamaConfig{
		BaseURL:    srv.URL,
		Model:      "nomic-embed-text",
		Timeout:    5 * time.Second,
		MaxRetries: 0,
	}
	p, err := NewOllamaProvider(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
}

func TestNewOllamaProvider_DefaultsApplied(t *testing.T) {
	// Empty BaseURL, Model, Timeout, MaxRetries, RetryBackoff → all defaulted.
	p, err := NewOllamaProvider(OllamaConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
}

func TestNewOllamaProvider_KnownModelDimensions(t *testing.T) {
	srv, _ := embeddingServer(t, ollamaProviderHandler(768, "nomic-embed-text"))
	cfg := OllamaConfig{
		BaseURL: srv.URL,
		Model:   "nomic-embed-text", // in ollamaModelDimensions
	}
	p, err := NewOllamaProvider(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Dimensions() != 768 {
		t.Errorf("expected 768 dims for nomic-embed-text, got %d", p.Dimensions())
	}
}

func TestNewOllamaProvider_CustomDimensions(t *testing.T) {
	srv, _ := embeddingServer(t, ollamaProviderHandler(512, "custom-model"))
	cfg := OllamaConfig{
		BaseURL:    srv.URL,
		Model:      "custom-model",
		Dimensions: 512,
	}
	p, err := NewOllamaProvider(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Dimensions() != 512 {
		t.Errorf("expected 512, got %d", p.Dimensions())
	}
}

// ---------------------------------------------------------------------------
// Embed
// ---------------------------------------------------------------------------

func TestOllamaProvider_Embed_Success(t *testing.T) {
	srv, _ := embeddingServer(t, ollamaProviderHandler(768, "nomic-embed-text"))
	cfg := OllamaConfig{BaseURL: srv.URL, Model: "nomic-embed-text", MaxRetries: 0}
	p, _ := NewOllamaProvider(cfg)

	result, err := p.Embed(context.Background(), []string{"hello"}, "nomic-embed-text")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(result.Embeddings) != 1 {
		t.Fatalf("expected 1 embedding, got %d", len(result.Embeddings))
	}
	if len(result.Embeddings[0]) != 768 {
		t.Errorf("expected 768 dims, got %d", len(result.Embeddings[0]))
	}
}

func TestOllamaProvider_Embed_FallbackModel(t *testing.T) {
	srv, _ := embeddingServer(t, ollamaProviderHandler(768, "nomic-embed-text"))
	cfg := OllamaConfig{BaseURL: srv.URL, Model: "nomic-embed-text", MaxRetries: 0}
	p, _ := NewOllamaProvider(cfg)

	// Empty model → provider falls back to its configured model.
	result, err := p.Embed(context.Background(), []string{"hi"}, "")
	if err != nil {
		t.Fatalf("Embed with empty model: %v", err)
	}
	if len(result.Embeddings) != 1 {
		t.Errorf("expected 1 embedding, got %d", len(result.Embeddings))
	}
}

func TestOllamaProvider_Embed_AutoDetectDimensions(t *testing.T) {
	srv, _ := embeddingServer(t, ollamaProviderHandler(384, "all-minilm"))
	cfg := OllamaConfig{BaseURL: srv.URL, Model: "unknown-model", Dimensions: 0, MaxRetries: 0}
	p, _ := NewOllamaProvider(cfg)

	if p.Dimensions() != 0 {
		t.Fatalf("expected 0 dims before embed, got %d", p.Dimensions())
	}
	p.Embed(context.Background(), []string{"text"}, "all-minilm")
	if p.Dimensions() != 384 {
		t.Errorf("expected 384 dims after embed, got %d", p.Dimensions())
	}
}

func TestOllamaProvider_Embed_BatchTexts(t *testing.T) {
	srv, _ := embeddingServer(t, ollamaProviderHandler(768, "nomic-embed-text"))
	cfg := OllamaConfig{BaseURL: srv.URL, Model: "nomic-embed-text", MaxRetries: 0}
	p, _ := NewOllamaProvider(cfg)

	result, err := p.Embed(context.Background(), []string{"a", "b", "c"}, "nomic-embed-text")
	if err != nil {
		t.Fatalf("Embed batch: %v", err)
	}
	if len(result.Embeddings) != 3 {
		t.Errorf("expected 3 embeddings, got %d", len(result.Embeddings))
	}
}

func TestOllamaProvider_Embed_ServerError(t *testing.T) {
	srv, _ := embeddingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	cfg := OllamaConfig{BaseURL: srv.URL, Model: "m", MaxRetries: 0}
	p, _ := NewOllamaProvider(cfg)

	_, err := p.Embed(context.Background(), []string{"text"}, "m")
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Errorf("expected HTTPError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Dimensions / Name
// ---------------------------------------------------------------------------

func TestOllamaProvider_Name(t *testing.T) {
	srv, _ := embeddingServer(t, ollamaProviderHandler(768, "m"))
	cfg := OllamaConfig{BaseURL: srv.URL, Model: "m"}
	p, _ := NewOllamaProvider(cfg)
	if p.Name() != "ollama" {
		t.Errorf("expected Name=ollama, got %q", p.Name())
	}
}

// ---------------------------------------------------------------------------
// Models
// ---------------------------------------------------------------------------

func TestOllamaProvider_Models_Success(t *testing.T) {
	srv, _ := embeddingServer(t, ollamaProviderHandler(768, "nomic-embed-text"))
	cfg := OllamaConfig{BaseURL: srv.URL, Model: "nomic-embed-text", MaxRetries: 0}
	p, _ := NewOllamaProvider(cfg)

	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) == 0 {
		t.Error("expected at least one model")
	}
}

func TestOllamaProvider_Models_ServerError(t *testing.T) {
	srv, _ := embeddingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	})
	cfg := OllamaConfig{BaseURL: srv.URL, Model: "m", MaxRetries: 0}
	p, _ := NewOllamaProvider(cfg)

	_, err := p.Models(context.Background())
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Errorf("expected HTTPError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func TestOllamaProvider_Close_Idempotent(t *testing.T) {
	srv, _ := embeddingServer(t, ollamaProviderHandler(768, "m"))
	cfg := OllamaConfig{BaseURL: srv.URL, Model: "m"}
	p, _ := NewOllamaProvider(cfg)

	if err := p.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}
