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
)

// openaiProviderHandler returns a handler that routes /v1/embeddings and
// /v1/models for OpenAI provider tests.
func openaiProviderHandler(dims int, model string) http.HandlerFunc {
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

// newOpenAIProviderConfig returns a Config targeting srv.URL with sensible defaults.
func newOpenAIProviderConfig(srvURL string) Config {
	return Config{
		APIURL: srvURL,
		Model:  "text-embedding-3-small",
	}
}

// ---------------------------------------------------------------------------
// NewOpenAIProvider
// ---------------------------------------------------------------------------

func TestNewOpenAIProvider_Success(t *testing.T) {
	srv, _ := embeddingServer(t, openaiProviderHandler(1536, "text-embedding-3-small"))
	cfg := newOpenAIProviderConfig(srv.URL)

	p, err := NewOpenAIProvider(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
}

func TestNewOpenAIProvider_DefaultURL(t *testing.T) {
	// Empty APIURL must fall back to DefaultOpenAIBaseURL without error.
	cfg := Config{Model: "text-embedding-3-small"}
	p, err := NewOpenAIProvider(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
}

func TestNewOpenAIProvider_DefaultModel(t *testing.T) {
	srv, _ := embeddingServer(t, openaiProviderHandler(1536, "text-embedding-3-small"))
	cfg := Config{APIURL: srv.URL} // empty model → default
	p, err := NewOpenAIProvider(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
}

func TestNewOpenAIProvider_KnownModelDimensions(t *testing.T) {
	srv, _ := embeddingServer(t, openaiProviderHandler(1536, "text-embedding-3-small"))
	cfg := newOpenAIProviderConfig(srv.URL)
	cfg.Model = "text-embedding-3-small" // in openaiModelDimensions map

	p, err := NewOpenAIProvider(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Dimensions() != 1536 {
		t.Errorf("expected 1536 dims from known model, got %d", p.Dimensions())
	}
}

func TestNewOpenAIProvider_CustomDimensions(t *testing.T) {
	srv, _ := embeddingServer(t, openaiProviderHandler(512, "custom-model"))
	cfg := newOpenAIProviderConfig(srv.URL)
	cfg.Model = "custom-model"
	cfg.Dimensions = 512

	p, err := NewOpenAIProvider(cfg)
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

func TestOpenAIProvider_Embed_Success(t *testing.T) {
	srv, _ := embeddingServer(t, openaiProviderHandler(1536, "text-embedding-3-small"))
	cfg := newOpenAIProviderConfig(srv.URL)
	p, _ := NewOpenAIProvider(cfg)

	result, err := p.Embed(context.Background(), []string{"hello"}, "text-embedding-3-small")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(result.Embeddings) != 1 {
		t.Fatalf("expected 1 embedding, got %d", len(result.Embeddings))
	}
	if len(result.Embeddings[0]) != 1536 {
		t.Errorf("expected 1536 dims, got %d", len(result.Embeddings[0]))
	}
}

func TestOpenAIProvider_Embed_AutoDetectDimensions(t *testing.T) {
	srv, _ := embeddingServer(t, openaiProviderHandler(768, "nomic-embed-text"))
	cfg := Config{APIURL: srv.URL, Model: "unknown-model", Dimensions: 0}
	p, _ := NewOpenAIProvider(cfg)

	if p.Dimensions() != 0 {
		t.Fatalf("expected 0 dims before embed, got %d", p.Dimensions())
	}

	p.Embed(context.Background(), []string{"text"}, "nomic-embed-text")

	if p.Dimensions() != 768 {
		t.Errorf("expected 768 dims after embed, got %d", p.Dimensions())
	}
}

func TestOpenAIProvider_Embed_EmptyInput(t *testing.T) {
	srv, _ := embeddingServer(t, openaiProviderHandler(1536, "m"))
	cfg := newOpenAIProviderConfig(srv.URL)
	p, _ := NewOpenAIProvider(cfg)

	_, err := p.Embed(context.Background(), []string{}, "m")
	var inv *InvalidInputError
	if !errors.As(err, &inv) {
		t.Errorf("expected InvalidInputError, got %T: %v", err, err)
	}
}

func TestOpenAIProvider_Embed_FallbackModel(t *testing.T) {
	srv, _ := embeddingServer(t, openaiProviderHandler(1536, "text-embedding-3-small"))
	cfg := newOpenAIProviderConfig(srv.URL)
	p, _ := NewOpenAIProvider(cfg)

	// Empty model string → provider falls back to its configured model.
	result, err := p.Embed(context.Background(), []string{"hello"}, "")
	if err != nil {
		t.Fatalf("Embed with empty model: %v", err)
	}
	if len(result.Embeddings) != 1 {
		t.Errorf("expected 1 embedding, got %d", len(result.Embeddings))
	}
}

func TestOpenAIProvider_Embed_ServerError(t *testing.T) {
	srv, _ := embeddingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
	cfg := newOpenAIProviderConfig(srv.URL)
	cfg.Model = "m"
	cfg.MaxRetries = 0
	p, _ := NewOpenAIProvider(cfg)

	_, err := p.Embed(context.Background(), []string{"text"}, "m")
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Errorf("expected HTTPError, got %T: %v", err, err)
	}
	if httpErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", httpErr.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// Dimensions / Name
// ---------------------------------------------------------------------------

func TestOpenAIProvider_Dimensions_KnownModel(t *testing.T) {
	srv, _ := embeddingServer(t, openaiProviderHandler(1536, "text-embedding-3-small"))
	cfg := newOpenAIProviderConfig(srv.URL)
	p, _ := NewOpenAIProvider(cfg)
	if p.Dimensions() != 1536 {
		t.Errorf("expected 1536, got %d", p.Dimensions())
	}
}

func TestOpenAIProvider_Name(t *testing.T) {
	srv, _ := embeddingServer(t, openaiProviderHandler(1536, "m"))
	cfg := newOpenAIProviderConfig(srv.URL)
	p, _ := NewOpenAIProvider(cfg)
	if p.Name() != "openai" {
		t.Errorf("expected Name=openai, got %q", p.Name())
	}
}

func TestOpenAIProvider_DefaultModel_Configured(t *testing.T) {
	srv, _ := embeddingServer(t, openaiProviderHandler(1536, "m"))
	cfg := newOpenAIProviderConfig(srv.URL)
	cfg.Model = "custom-embed"
	p, err := NewOpenAIProvider(cfg)
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}
	if got := p.DefaultModel(); got != "custom-embed" {
		t.Errorf("DefaultModel = %q, want %q", got, "custom-embed")
	}
}

func TestOpenAIProvider_DefaultModel_Unset(t *testing.T) {
	srv, _ := embeddingServer(t, openaiProviderHandler(1536, "m"))
	cfg := newOpenAIProviderConfig(srv.URL)
	cfg.Model = ""
	p, err := NewOpenAIProvider(cfg)
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}
	if got := p.DefaultModel(); got != "text-embedding-3-small" {
		t.Errorf("DefaultModel = %q, want %q", got, "text-embedding-3-small")
	}
}

// ---------------------------------------------------------------------------
// Models
// ---------------------------------------------------------------------------

func TestOpenAIProvider_Models_Success(t *testing.T) {
	srv, _ := embeddingServer(t, openaiProviderHandler(1536, "text-embedding-3-small"))
	cfg := newOpenAIProviderConfig(srv.URL)
	p, _ := NewOpenAIProvider(cfg)

	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) == 0 {
		t.Error("expected at least one model")
	}
}

func TestOpenAIProvider_Models_ServerError(t *testing.T) {
	srv, _ := embeddingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	})
	cfg := Config{APIURL: srv.URL, Model: "m", MaxRetries: 0}
	p, _ := NewOpenAIProvider(cfg)

	_, err := p.Models(context.Background())
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Errorf("expected HTTPError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func TestOpenAIProvider_Close_Idempotent(t *testing.T) {
	srv, _ := embeddingServer(t, openaiProviderHandler(1536, "m"))
	cfg := newOpenAIProviderConfig(srv.URL)
	p, _ := NewOpenAIProvider(cfg)

	if err := p.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}
