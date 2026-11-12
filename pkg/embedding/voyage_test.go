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
	"testing"
	"time"
)

// makeVoyageEmbeddingResponse builds a valid Voyage AI response body.
func makeVoyageEmbeddingResponse(model string, dims, count int) []byte {
	data := make([]voyageEmbedding, count)
	for i := range data {
		vec := make([]float32, dims)
		for j := range vec {
			vec[j] = float32(i+1) * 0.1
		}
		data[i] = voyageEmbedding{
			Object:    "embedding",
			Embedding: vec,
			Index:     i,
		}
	}
	resp := voyageResponse{
		Object: "list",
		Data:   data,
		Model:  model,
		Usage:  voyageUsage{TotalTokens: count * 5},
	}
	b, _ := json.Marshal(resp)
	return b
}

// newVoyageTestServer creates an httptest.Server and returns a Config with
// APIURL pointing directly at the server root.
func newVoyageTestServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, Config) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cfg := Config{
		APIKey:         "sk-test-voyage",
		APIURL:         srv.URL,
		Model:          "voyage-code-2",
		MaxRetries:     0,
		RetryBackoffMS: 1,
	}
	return srv, cfg
}

// ---------------------------------------------------------------------------
// NewVoyageProvider
// ---------------------------------------------------------------------------

func TestNewVoyageProvider_MissingAPIKey(t *testing.T) {
	cfg := Config{Model: "voyage-code-2", APIURL: "http://fake"}
	_, err := NewVoyageProvider(cfg)
	if !errors.Is(err, ErrAPIKeyRequired) {
		t.Errorf("expected ErrAPIKeyRequired, got: %v", err)
	}
}

func TestNewVoyageProvider_UnknownModel(t *testing.T) {
	cfg := Config{
		APIKey: "sk-test",
		APIURL: "http://fake",
		Model:  "voyage-unknown-model",
	}
	_, err := NewVoyageProvider(cfg)
	if err == nil {
		t.Fatal("expected error for unknown model")
	}
	var notFound *ModelNotFoundError
	if !errors.As(err, &notFound) {
		t.Errorf("expected ModelNotFoundError, got %T: %v", err, err)
	}
	if notFound.Model != "voyage-unknown-model" {
		t.Errorf("expected Model=voyage-unknown-model, got %q", notFound.Model)
	}
}

func TestNewVoyageProvider_DefaultModel(t *testing.T) {
	// Empty model defaults to DefaultVoyageModel ("voyage-code-2").
	cfg := Config{
		APIKey: "sk-test",
		APIURL: "http://fake",
	}
	p, err := NewVoyageProvider(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
}

func TestNewVoyageProvider_DefaultURL(t *testing.T) {
	// Empty APIURL defaults to DefaultVoyageAPIURL.
	cfg := Config{
		APIKey: "sk-test",
		Model:  "voyage-code-2",
	}
	p, err := NewVoyageProvider(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
}

func TestNewVoyageProvider_CustomDimensions(t *testing.T) {
	cfg := Config{
		APIKey:     "sk-test",
		APIURL:     "http://fake",
		Model:      "voyage-code-2",
		Dimensions: 512,
	}
	p, err := NewVoyageProvider(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Dimensions() != 512 {
		t.Errorf("expected 512 custom dims, got %d", p.Dimensions())
	}
}

func TestNewVoyageProvider_KnownModelDimensions(t *testing.T) {
	cfg := Config{
		APIKey: "sk-test",
		APIURL: "http://fake",
		Model:  "voyage-code-2", // 1536 dims in voyageModels table
	}
	p, err := NewVoyageProvider(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Dimensions() != 1536 {
		t.Errorf("expected 1536, got %d", p.Dimensions())
	}
}

// ---------------------------------------------------------------------------
// Embed
// ---------------------------------------------------------------------------

func TestVoyageProvider_Embed_Success(t *testing.T) {
	_, cfg := newVoyageTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(makeVoyageEmbeddingResponse("voyage-code-2", 1536, 1))
	})
	p, _ := NewVoyageProvider(cfg)

	result, err := p.Embed(context.Background(), []string{"hello"}, "voyage-code-2")
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

func TestVoyageProvider_Embed_EmptyInput(t *testing.T) {
	_, cfg := newVoyageTestServer(t, func(w http.ResponseWriter, _ *http.Request) {})
	p, _ := NewVoyageProvider(cfg)

	_, err := p.Embed(context.Background(), []string{}, "voyage-code-2")
	var inv *InvalidInputError
	if !errors.As(err, &inv) {
		t.Errorf("expected InvalidInputError, got %T: %v", err, err)
	}
}

func TestVoyageProvider_Embed_FallbackModel(t *testing.T) {
	_, cfg := newVoyageTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(makeVoyageEmbeddingResponse("voyage-code-2", 1536, 1))
	})
	p, _ := NewVoyageProvider(cfg)

	// Empty model → provider falls back to its configured model.
	result, err := p.Embed(context.Background(), []string{"hi"}, "")
	if err != nil {
		t.Fatalf("Embed with empty model: %v", err)
	}
	if len(result.Embeddings) != 1 {
		t.Errorf("expected 1 embedding, got %d", len(result.Embeddings))
	}
}

func TestVoyageProvider_Embed_HTTPError_4xx(t *testing.T) {
	_, cfg := newVoyageTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
	p, _ := NewVoyageProvider(cfg)

	_, err := p.Embed(context.Background(), []string{"text"}, "voyage-code-2")
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Errorf("expected HTTPError, got %T: %v", err, err)
	}
	if httpErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", httpErr.StatusCode)
	}
}

func TestVoyageProvider_Embed_HTTPError_VoyageErrorBody(t *testing.T) {
	// Voyage AI returns structured error JSON on failures.
	_, cfg := newVoyageTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		errBody := voyageErrorResponse{}
		errBody.Error.Message = "invalid input"
		errBody.Error.Type = "invalid_request_error"
		b, _ := json.Marshal(errBody)
		w.Write(b)
	})
	p, _ := NewVoyageProvider(cfg)

	_, err := p.Embed(context.Background(), []string{"text"}, "voyage-code-2")
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Errorf("expected HTTPError, got %T: %v", err, err)
	}
	if httpErr.Body != "invalid input" {
		t.Errorf("expected Voyage error message in Body, got %q", httpErr.Body)
	}
}

func TestVoyageProvider_Embed_UnmarshalError(t *testing.T) {
	_, cfg := newVoyageTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{not valid`))
	})
	p, _ := NewVoyageProvider(cfg)

	_, err := p.Embed(context.Background(), []string{"text"}, "voyage-code-2")
	var unmarshal *UnmarshalError
	if !errors.As(err, &unmarshal) {
		t.Errorf("expected UnmarshalError, got %T: %v", err, err)
	}
}

func TestVoyageProvider_Embed_EmptyData(t *testing.T) {
	_, cfg := newVoyageTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := voyageResponse{Object: "list", Data: nil, Model: "voyage-code-2"}
		b, _ := json.Marshal(resp)
		w.Write(b)
	})
	p, _ := NewVoyageProvider(cfg)

	_, err := p.Embed(context.Background(), []string{"text"}, "voyage-code-2")
	var emptyErr *EmptyResponseError
	if !errors.As(err, &emptyErr) {
		t.Errorf("expected EmptyResponseError, got %T: %v", err, err)
	}
}

func TestVoyageProvider_Embed_RetryExhausted(t *testing.T) {
	// 503 triggers retry; with MaxRetries=1 and 1ms backoff the test is fast.
	_, cfg := newVoyageTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	cfg.MaxRetries = 1
	cfg.RetryBackoffMS = 1
	p, _ := NewVoyageProvider(cfg)

	_, err := p.Embed(context.Background(), []string{"text"}, "voyage-code-2")
	var retryErr *RetryExhaustedError
	if !errors.As(err, &retryErr) {
		t.Errorf("expected RetryExhaustedError, got %T: %v", err, err)
	}
	// MaxRetries=1 → 2 attempts.
	if retryErr.Attempts != 2 {
		t.Errorf("expected Attempts=2, got %d", retryErr.Attempts)
	}
}

func TestVoyageProvider_Embed_ContextCancelled_DuringBackoff(t *testing.T) {
	// Trigger backoff by having the first attempt fail with 503, then cancel
	// the context before the second attempt's delay expires.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	cfg := Config{
		APIKey:         "sk-test",
		APIURL:         srv.URL,
		Model:          "voyage-code-2",
		MaxRetries:     2,
		RetryBackoffMS: 500, // 500ms backoff → context cancellation fires first
	}
	p, _ := NewVoyageProvider(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately so the second attempt's backoff select fires ctx.Done.
	cancel()

	_, err := p.Embed(ctx, []string{"text"}, "voyage-code-2")
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestVoyageProvider_Embed_BatchTexts(t *testing.T) {
	_, cfg := newVoyageTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req voyageRequest
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		w.Write(makeVoyageEmbeddingResponse("voyage-code-2", 1536, len(req.Input)))
	})
	p, _ := NewVoyageProvider(cfg)

	result, err := p.Embed(context.Background(), []string{"a", "b", "c"}, "voyage-code-2")
	if err != nil {
		t.Fatalf("Embed batch: %v", err)
	}
	if len(result.Embeddings) != 3 {
		t.Errorf("expected 3 embeddings, got %d", len(result.Embeddings))
	}
}

func TestVoyageProvider_Embed_InvalidIndex(t *testing.T) {
	// Server returns an out-of-bounds index to trigger the invalid-index branch.
	_, cfg := newVoyageTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// 1 input text but index=99
		data := []voyageEmbedding{{
			Object:    "embedding",
			Embedding: []float32{0.1},
			Index:     99,
		}}
		resp := voyageResponse{Object: "list", Data: data, Model: "voyage-code-2"}
		b, _ := json.Marshal(resp)
		w.Write(b)
	})
	p, _ := NewVoyageProvider(cfg)

	_, err := p.Embed(context.Background(), []string{"text"}, "voyage-code-2")
	if err == nil {
		t.Fatal("expected error for out-of-bounds index")
	}
}

func TestVoyageProvider_Embed_MissingEmbeddingAtIndex(t *testing.T) {
	// 2 input texts but server only returns index=0; index=1 is nil.
	_, cfg := newVoyageTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		data := []voyageEmbedding{{
			Object:    "embedding",
			Embedding: []float32{0.1},
			Index:     0, // only index 0; index 1 missing
		}}
		resp := voyageResponse{Object: "list", Data: data, Model: "voyage-code-2"}
		b, _ := json.Marshal(resp)
		w.Write(b)
	})
	p, _ := NewVoyageProvider(cfg)

	_, err := p.Embed(context.Background(), []string{"text1", "text2"}, "voyage-code-2")
	if err == nil {
		t.Fatal("expected error for missing embedding at index 1")
	}
}

// ---------------------------------------------------------------------------
// Dimensions / Name
// ---------------------------------------------------------------------------

func TestVoyageProvider_Name(t *testing.T) {
	cfg := Config{
		APIKey: "sk-test",
		APIURL: "http://fake",
		Model:  "voyage-code-2",
	}
	p, _ := NewVoyageProvider(cfg)
	if p.Name() != "voyage" {
		t.Errorf("expected Name=voyage, got %q", p.Name())
	}
}

// ---------------------------------------------------------------------------
// Models (static list)
// ---------------------------------------------------------------------------

func TestVoyageProvider_Models_ContainsExpectedModel(t *testing.T) {
	cfg := Config{
		APIKey: "sk-test",
		APIURL: "http://fake",
		Model:  "voyage-code-2",
	}
	p, _ := NewVoyageProvider(cfg)

	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	found := false
	for _, m := range models {
		if m.Name == "voyage-code-2" {
			found = true
			if m.Dimensions != 1536 {
				t.Errorf("expected 1536 dims for voyage-code-2, got %d", m.Dimensions)
			}
		}
	}
	if !found {
		t.Error("expected voyage-code-2 in static model list")
	}
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func TestVoyageProvider_Close_Idempotent(t *testing.T) {
	cfg := Config{
		APIKey: "sk-test",
		APIURL: "http://fake",
		Model:  "voyage-code-2",
	}
	p, _ := NewVoyageProvider(cfg)

	if err := p.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// voyageBackoff
// ---------------------------------------------------------------------------

func TestVoyageBackoff_Values(t *testing.T) {
	tests := []struct {
		attempt int
		baseMS  int
		minMS   int64
		maxMS   int64
	}{
		// attempt=1: 1000ms * 2^0 = 1000ms
		{attempt: 1, baseMS: 1000, minMS: 900, maxMS: 1100},
		// attempt=2: 1000ms * 2^1 = 2000ms
		{attempt: 2, baseMS: 1000, minMS: 1800, maxMS: 2200},
		// attempt=1, baseMS=500: 500ms * 2^0 = 500ms
		{attempt: 1, baseMS: 500, minMS: 450, maxMS: 550},
	}

	for _, tt := range tests {
		d := voyageBackoff(tt.attempt, tt.baseMS)
		ms := d.Milliseconds()
		if ms < tt.minMS || ms > tt.maxMS {
			t.Errorf("voyageBackoff(%d, %d) = %dms, expected [%d, %d]ms",
				tt.attempt, tt.baseMS, ms, tt.minMS, tt.maxMS)
		}
	}
}

func TestVoyageBackoff_Cap(t *testing.T) {
	// attempt=5, baseMS=1000: 1000ms * 2^4 = 16000ms = 16s > 10s cap.
	// Using a safe attempt value that does not overflow int64.
	d := voyageBackoff(5, 1000)
	if d != 10*time.Second {
		t.Errorf("expected backoff capped at 10s, got %v", d)
	}
}
