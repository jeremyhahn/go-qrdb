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
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// embeddingServer builds a test HTTP server that returns a fixed embedding response.
func embeddingServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, srv.URL + "/v1"
}

// makeEmbeddingResponse returns a valid embeddingResponse JSON for the given texts.
func makeEmbeddingResponse(model string, dims int, count int) []byte {
	data := make([]embeddingData, count)
	for i := range data {
		vec := make([]float32, dims)
		for j := range vec {
			vec[j] = float32(i+1) * 0.1
		}
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
	b, _ := json.Marshal(resp)
	return b
}

func newTestClient(t *testing.T, baseURL string, opts ...func(*ProviderConfig)) *Client {
	t.Helper()
	cfg := DefaultProviderConfig()
	cfg.BaseURL = baseURL
	cfg.Model = "text-embedding-3-small"
	cfg.MaxRetries = 2
	cfg.Timeout = 5 * time.Second
	for _, o := range opts {
		o(cfg)
	}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

// ---------------------------------------------------------------------------
// NewClient tests
// ---------------------------------------------------------------------------

func TestNewClient_NilConfig(t *testing.T) {
	// nil config should use defaults but fail because BaseURL is empty.
	_, err := NewClient(nil)
	if err == nil {
		t.Fatal("expected error for nil/empty BaseURL")
	}
	var inv *InvalidInputError
	if !errors.As(err, &inv) {
		t.Errorf("expected InvalidInputError, got %T: %v", err, err)
	}
}

func TestNewClient_EmptyBaseURL(t *testing.T) {
	cfg := DefaultProviderConfig()
	_, err := NewClient(cfg)
	if err == nil {
		t.Fatal("expected error for empty BaseURL")
	}
	var inv *InvalidInputError
	if !errors.As(err, &inv) {
		t.Errorf("expected InvalidInputError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Embed tests
// ---------------------------------------------------------------------------

func TestEmbed_SingleText(t *testing.T) {
	_, baseURL := embeddingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "bad method", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(makeEmbeddingResponse("text-embedding-3-small", 1536, 1))
	})

	c := newTestClient(t, baseURL)
	result, err := c.Embed(context.Background(), []string{"hello world"}, "")
	if err != nil {
		t.Fatalf("Embed returned unexpected error: %v", err)
	}
	if len(result.Embeddings) != 1 {
		t.Fatalf("expected 1 embedding, got %d", len(result.Embeddings))
	}
	if len(result.Embeddings[0]) != 1536 {
		t.Errorf("expected 1536 dims, got %d", len(result.Embeddings[0]))
	}
	if result.Model != "text-embedding-3-small" {
		t.Errorf("expected model=text-embedding-3-small, got %q", result.Model)
	}
}

func TestEmbed_BatchTexts(t *testing.T) {
	_, baseURL := embeddingServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req embeddingRequest
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		w.Write(makeEmbeddingResponse("nomic-embed-text", 768, len(req.Input)))
	})

	c := newTestClient(t, baseURL)
	texts := []string{"foo", "bar", "baz"}
	result, err := c.Embed(context.Background(), texts, "nomic-embed-text")
	if err != nil {
		t.Fatalf("Embed returned unexpected error: %v", err)
	}
	if len(result.Embeddings) != 3 {
		t.Fatalf("expected 3 embeddings, got %d", len(result.Embeddings))
	}
}

func TestEmbed_EmptyInput(t *testing.T) {
	_, baseURL := embeddingServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	c := newTestClient(t, baseURL)
	_, err := c.Embed(context.Background(), []string{}, "")
	if err == nil {
		t.Fatal("expected error for empty input")
	}
	var inv *InvalidInputError
	if !errors.As(err, &inv) {
		t.Errorf("expected InvalidInputError, got %T: %v", err, err)
	}
}

func TestEmbed_EmptyResponse(t *testing.T) {
	_, baseURL := embeddingServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Return valid JSON but with no data entries.
		resp := embeddingResponse{Object: "list", Data: nil, Model: "m"}
		b, _ := json.Marshal(resp)
		w.Write(b)
	})

	c := newTestClient(t, baseURL)
	_, err := c.Embed(context.Background(), []string{"text"}, "m")
	var empty *EmptyResponseError
	if !errors.As(err, &empty) {
		t.Errorf("expected EmptyResponseError, got %T: %v", err, err)
	}
}

func TestEmbed_InvalidJSONResponse(t *testing.T) {
	_, baseURL := embeddingServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{not valid json`))
	})

	c := newTestClient(t, baseURL)
	_, err := c.Embed(context.Background(), []string{"text"}, "m")
	var unmarshal *UnmarshalError
	if !errors.As(err, &unmarshal) {
		t.Errorf("expected UnmarshalError, got %T: %v", err, err)
	}
}

func TestEmbed_Unauthorized(t *testing.T) {
	_, baseURL := embeddingServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})

	c := newTestClient(t, baseURL)
	_, err := c.Embed(context.Background(), []string{"text"}, "m")
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Errorf("expected HTTPError, got %T: %v", err, err)
	}
	if httpErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", httpErr.StatusCode)
	}
}

func TestEmbed_RetryOn500_SucceedsOnSecondAttempt(t *testing.T) {
	var attempts atomic.Int32
	_, baseURL := embeddingServer(t, func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(makeEmbeddingResponse("m", 4, 1))
	})

	// Use 0 timeout so backoff is skipped in test via very short delay.
	cfg := DefaultProviderConfig()
	cfg.BaseURL = baseURL
	cfg.Model = "m"
	cfg.MaxRetries = 2
	cfg.Timeout = 5 * time.Second
	c, _ := NewClient(cfg)
	// Patch base delay to zero for test speed.
	result, err := c.Embed(context.Background(), []string{"text"}, "m")
	if err != nil {
		t.Fatalf("expected success on retry, got: %v", err)
	}
	if len(result.Embeddings) != 1 {
		t.Errorf("expected 1 embedding, got %d", len(result.Embeddings))
	}
	if attempts.Load() < 2 {
		t.Errorf("expected at least 2 attempts, got %d", attempts.Load())
	}
}

func TestEmbed_RetryExhausted(t *testing.T) {
	_, baseURL := embeddingServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	cfg := DefaultProviderConfig()
	cfg.BaseURL = baseURL
	cfg.Model = "m"
	cfg.MaxRetries = 1
	cfg.Timeout = 5 * time.Second
	c, _ := NewClient(cfg)

	_, err := c.Embed(context.Background(), []string{"text"}, "m")
	var retryErr *RetryExhaustedError
	if !errors.As(err, &retryErr) {
		t.Errorf("expected RetryExhaustedError, got %T: %v", err, err)
	}
	// MaxRetries=1 means 2 total attempts.
	if retryErr.Attempts != 2 {
		t.Errorf("expected Attempts=2, got %d", retryErr.Attempts)
	}
}

func TestEmbed_RateLimit_Retried(t *testing.T) {
	var attempts atomic.Int32
	_, baseURL := embeddingServer(t, func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(makeEmbeddingResponse("m", 4, 1))
	})

	cfg := DefaultProviderConfig()
	cfg.BaseURL = baseURL
	cfg.Model = "m"
	cfg.MaxRetries = 3
	cfg.Timeout = 5 * time.Second
	c, _ := NewClient(cfg)

	result, err := c.Embed(context.Background(), []string{"text"}, "m")
	if err != nil {
		t.Fatalf("expected success after rate-limit retries, got: %v", err)
	}
	if len(result.Embeddings) != 1 {
		t.Errorf("expected 1 embedding, got %d", len(result.Embeddings))
	}
}

func TestEmbed_APIKeyHeader_Present(t *testing.T) {
	receivedKey := ""
	_, baseURL := embeddingServer(t, func(w http.ResponseWriter, r *http.Request) {
		receivedKey = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write(makeEmbeddingResponse("m", 4, 1))
	})

	c := newTestClient(t, baseURL, func(cfg *ProviderConfig) {
		cfg.APIKey = "sk-test-key"
	})
	c.Embed(context.Background(), []string{"text"}, "m")
	if receivedKey != "Bearer sk-test-key" {
		t.Errorf("expected 'Bearer sk-test-key', got %q", receivedKey)
	}
}

func TestEmbed_APIKeyHeader_OmittedWhenEmpty(t *testing.T) {
	receivedKey := "present"
	_, baseURL := embeddingServer(t, func(w http.ResponseWriter, r *http.Request) {
		receivedKey = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write(makeEmbeddingResponse("m", 4, 1))
	})

	c := newTestClient(t, baseURL) // no APIKey set
	c.Embed(context.Background(), []string{"text"}, "m")
	if receivedKey != "" {
		t.Errorf("expected no Authorization header, got %q", receivedKey)
	}
}

func TestEmbed_CancelledContext(t *testing.T) {
	_, baseURL := embeddingServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Slow handler that won't complete before cancellation.
		time.Sleep(500 * time.Millisecond)
		w.Write(makeEmbeddingResponse("m", 4, 1))
	})

	c := newTestClient(t, baseURL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := c.Embed(ctx, []string{"text"}, "m")
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

// ---------------------------------------------------------------------------
// Models tests
// ---------------------------------------------------------------------------

func TestModels_Success(t *testing.T) {
	_, baseURL := embeddingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		resp := modelListResponse{
			Object: "list",
			Data: []modelData{
				{ID: "nomic-embed-text", Object: "model"},
				{ID: "all-minilm", Object: "model"},
			},
		}
		b, _ := json.Marshal(resp)
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	})

	c := newTestClient(t, baseURL)
	models, err := c.Models(context.Background())
	if err != nil {
		t.Fatalf("Models returned error: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].Name != "nomic-embed-text" {
		t.Errorf("expected first model=nomic-embed-text, got %q", models[0].Name)
	}
}

func TestModels_UnmarshalError(t *testing.T) {
	_, baseURL := embeddingServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{broken`))
	})

	c := newTestClient(t, baseURL)
	_, err := c.Models(context.Background())
	var unmarshal *UnmarshalError
	if !errors.As(err, &unmarshal) {
		t.Errorf("expected UnmarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Close tests
// ---------------------------------------------------------------------------

func TestClose_Idempotent(t *testing.T) {
	_, baseURL := embeddingServer(t, func(w http.ResponseWriter, r *http.Request) {})
	c := newTestClient(t, baseURL)

	if err := c.Close(); err != nil {
		t.Errorf("first Close() returned error: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("second Close() returned error: %v", err)
	}
}
