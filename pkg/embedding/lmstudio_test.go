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

// lmstudioProviderHandler routes /v1/embeddings and /v1/models for LMStudio tests.
func lmstudioProviderHandler(dims int, model string) http.HandlerFunc {
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
// DefaultLMStudioConfig
// ---------------------------------------------------------------------------

func TestDefaultLMStudioConfig_Defaults(t *testing.T) {
	cfg := DefaultLMStudioConfig()
	if cfg.BaseURL != DefaultLMStudioBaseURL {
		t.Errorf("expected BaseURL=%q, got %q", DefaultLMStudioBaseURL, cfg.BaseURL)
	}
	if cfg.Model != DefaultLMStudioModel {
		t.Errorf("expected Model=%q, got %q", DefaultLMStudioModel, cfg.Model)
	}
	if cfg.MaxRetries != 3 {
		t.Errorf("expected MaxRetries=3, got %d", cfg.MaxRetries)
	}
}

func TestDefaultLMStudioConfig_Timeout(t *testing.T) {
	cfg := DefaultLMStudioConfig()
	expectedTimeout := int(DefaultLMStudioTimeout.Seconds())
	if cfg.Timeout != expectedTimeout {
		t.Errorf("expected Timeout=%d, got %d", expectedTimeout, cfg.Timeout)
	}
	if cfg.RetryBackoffMS != 1000 {
		t.Errorf("expected RetryBackoffMS=1000, got %d", cfg.RetryBackoffMS)
	}
}

// ---------------------------------------------------------------------------
// NewLMStudioProvider
// ---------------------------------------------------------------------------

func TestNewLMStudioProvider_Success(t *testing.T) {
	srv, _ := embeddingServer(t, lmstudioProviderHandler(768, DefaultLMStudioModel))
	cfg := LMStudioConfig{
		BaseURL:    srv.URL,
		Model:      DefaultLMStudioModel,
		MaxRetries: 0,
	}
	p, err := NewLMStudioProvider(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
}

func TestNewLMStudioProvider_DefaultsApplied(t *testing.T) {
	// Empty BaseURL, Model, Timeout, MaxRetries, RetryBackoffMS → all defaulted.
	p, err := NewLMStudioProvider(LMStudioConfig{})
	if err != nil {
		t.Fatalf("unexpected error with empty config: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
}

func TestNewLMStudioProvider_KnownModelDimensions(t *testing.T) {
	srv, _ := embeddingServer(t, lmstudioProviderHandler(768, DefaultLMStudioModel))
	cfg := LMStudioConfig{
		BaseURL: srv.URL,
		Model:   DefaultLMStudioModel, // in lmstudioModelDimensions map → 768
	}
	p, err := NewLMStudioProvider(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Dimensions() != 768 {
		t.Errorf("expected 768 dims for default model, got %d", p.Dimensions())
	}
}

func TestNewLMStudioProvider_SmallModel(t *testing.T) {
	srv, _ := embeddingServer(t, lmstudioProviderHandler(384, "BAAI/bge-small-en-v1.5-GGUF"))
	cfg := LMStudioConfig{
		BaseURL: srv.URL,
		Model:   "BAAI/bge-small-en-v1.5-GGUF", // 384 dims in table
	}
	p, err := NewLMStudioProvider(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Dimensions() != 384 {
		t.Errorf("expected 384, got %d", p.Dimensions())
	}
}

func TestNewLMStudioProvider_CustomDimensions(t *testing.T) {
	srv, _ := embeddingServer(t, lmstudioProviderHandler(512, "custom-model"))
	cfg := LMStudioConfig{
		BaseURL:    srv.URL,
		Model:      "custom-model",
		Dimensions: 512,
	}
	p, err := NewLMStudioProvider(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Dimensions() != 512 {
		t.Errorf("expected 512, got %d", p.Dimensions())
	}
}

func TestNewLMStudioProvider_UnknownModel_ZeroDims(t *testing.T) {
	srv, _ := embeddingServer(t, lmstudioProviderHandler(256, "unknown-model"))
	cfg := LMStudioConfig{
		BaseURL: srv.URL,
		Model:   "unknown-model", // not in lmstudioModelDimensions, Dimensions=0 → 0 until embed
	}
	p, err := NewLMStudioProvider(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Unknown model with no explicit Dimensions = 0 before embed.
	if p.Dimensions() != 0 {
		t.Errorf("expected 0 dims for unknown model before embed, got %d", p.Dimensions())
	}
}

// ---------------------------------------------------------------------------
// Embed
// ---------------------------------------------------------------------------

func TestLMStudioProvider_Embed_Success(t *testing.T) {
	srv, _ := embeddingServer(t, lmstudioProviderHandler(768, DefaultLMStudioModel))
	cfg := LMStudioConfig{BaseURL: srv.URL, Model: DefaultLMStudioModel, MaxRetries: 0}
	p, _ := NewLMStudioProvider(cfg)

	result, err := p.Embed(context.Background(), []string{"hello"}, DefaultLMStudioModel)
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

func TestLMStudioProvider_Embed_BatchTexts(t *testing.T) {
	srv, _ := embeddingServer(t, lmstudioProviderHandler(768, DefaultLMStudioModel))
	cfg := LMStudioConfig{BaseURL: srv.URL, Model: DefaultLMStudioModel, MaxRetries: 0}
	p, _ := NewLMStudioProvider(cfg)

	result, err := p.Embed(context.Background(), []string{"a", "b"}, DefaultLMStudioModel)
	if err != nil {
		t.Fatalf("Embed batch: %v", err)
	}
	if len(result.Embeddings) != 2 {
		t.Errorf("expected 2 embeddings, got %d", len(result.Embeddings))
	}
}

func TestLMStudioProvider_Embed_ServerError(t *testing.T) {
	srv, _ := embeddingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
	cfg := LMStudioConfig{BaseURL: srv.URL, Model: "m", MaxRetries: 0}
	p, _ := NewLMStudioProvider(cfg)

	_, err := p.Embed(context.Background(), []string{"text"}, "m")
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Errorf("expected HTTPError, got %T: %v", err, err)
	}
}

func TestLMStudioProvider_Embed_InvalidJSONResponse(t *testing.T) {
	srv, _ := embeddingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{not valid json`))
	})
	cfg := LMStudioConfig{BaseURL: srv.URL, Model: "m", MaxRetries: 0}
	p, _ := NewLMStudioProvider(cfg)

	_, err := p.Embed(context.Background(), []string{"text"}, "m")
	var unmarshal *UnmarshalError
	if !errors.As(err, &unmarshal) {
		t.Errorf("expected UnmarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Dimensions / Name
// ---------------------------------------------------------------------------

func TestLMStudioProvider_Name(t *testing.T) {
	srv, _ := embeddingServer(t, lmstudioProviderHandler(768, "m"))
	cfg := LMStudioConfig{BaseURL: srv.URL, Model: "m"}
	p, _ := NewLMStudioProvider(cfg)
	if p.Name() != "lmstudio" {
		t.Errorf("expected Name=lmstudio, got %q", p.Name())
	}
}

// ---------------------------------------------------------------------------
// Models
// ---------------------------------------------------------------------------

func TestLMStudioProvider_Models_Success(t *testing.T) {
	srv, _ := embeddingServer(t, lmstudioProviderHandler(768, DefaultLMStudioModel))
	cfg := LMStudioConfig{BaseURL: srv.URL, Model: DefaultLMStudioModel, MaxRetries: 0}
	p, _ := NewLMStudioProvider(cfg)

	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) == 0 {
		t.Error("expected at least one model")
	}
}

func TestLMStudioProvider_Models_ServerError(t *testing.T) {
	srv, _ := embeddingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	cfg := LMStudioConfig{BaseURL: srv.URL, Model: "m", MaxRetries: 0}
	p, _ := NewLMStudioProvider(cfg)

	_, err := p.Models(context.Background())
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Errorf("expected HTTPError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func TestLMStudioProvider_Close_Idempotent(t *testing.T) {
	srv, _ := embeddingServer(t, lmstudioProviderHandler(768, "m"))
	cfg := LMStudioConfig{BaseURL: srv.URL, Model: "m"}
	p, _ := NewLMStudioProvider(cfg)

	if err := p.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}
