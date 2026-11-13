//go:build !integration

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

package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Mock: EmbeddingServicer
// ---------------------------------------------------------------------------

type mockEmbeddingService struct {
	embedFn      func(ctx context.Context, req *service.EmbedRequest) (*service.EmbedResponse, error)
	batchEmbedFn func(ctx context.Context, req *service.BatchEmbedRequest) (*service.BatchEmbedResponse, error)
	listModelsFn func(ctx context.Context, req *service.ListEmbeddingModelsRequest) (*service.ListEmbeddingModelsResponse, error)
	registerFn   func(name string, provider service.EmbeddingProvider) error
	deregisterFn func(name string) error
	closeFn      func() error
}

func (m *mockEmbeddingService) Embed(ctx context.Context, req *service.EmbedRequest) (*service.EmbedResponse, error) {
	if m.embedFn != nil {
		return m.embedFn(ctx, req)
	}
	return &service.EmbedResponse{
		Embeddings: [][]float32{{0.1, 0.2, 0.3}},
		Model:      "text-embedding-3-small",
		Cached:     false,
		Usage:      &service.UsageInfo{PromptTokens: 5, TotalTokens: 5},
	}, nil
}

func (m *mockEmbeddingService) BatchEmbed(ctx context.Context, req *service.BatchEmbedRequest) (*service.BatchEmbedResponse, error) {
	if m.batchEmbedFn != nil {
		return m.batchEmbedFn(ctx, req)
	}
	vecs := make([][]float32, len(req.Texts))
	for i := range req.Texts {
		vecs[i] = []float32{float32(i) * 0.1, float32(i) * 0.2}
	}
	return &service.BatchEmbedResponse{
		Embeddings: vecs,
		Model:      "text-embedding-3-small",
		TotalUsage: &service.UsageInfo{PromptTokens: 10, TotalTokens: 10},
	}, nil
}

func (m *mockEmbeddingService) ListModels(ctx context.Context, req *service.ListEmbeddingModelsRequest) (*service.ListEmbeddingModelsResponse, error) {
	if m.listModelsFn != nil {
		return m.listModelsFn(ctx, req)
	}
	return &service.ListEmbeddingModelsResponse{
		Models: []service.ModelInfo{
			{Name: "text-embedding-3-small", Dimensions: 1536, MaxTokens: 8191},
			{Name: "text-embedding-3-large", Dimensions: 3072, MaxTokens: 8191},
		},
	}, nil
}

func (m *mockEmbeddingService) RegisterProvider(name string, provider service.EmbeddingProvider) error {
	if m.registerFn != nil {
		return m.registerFn(name, provider)
	}
	return nil
}

func (m *mockEmbeddingService) DeregisterProvider(name string) error {
	if m.deregisterFn != nil {
		return m.deregisterFn(name)
	}
	return nil
}

func (m *mockEmbeddingService) Close() error {
	if m.closeFn != nil {
		return m.closeFn()
	}
	return nil
}

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

func newEmbeddingTestServer(svc service.EmbeddingServicer) *Server {
	s := &Server{
		kvSvc:        &mockKVService{},
		adminSvc:     &mockAdminService{},
		embeddingSvc: svc,
		routes:       make(map[string]route),
	}
	if svc != nil {
		s.routes["/v1/embeddings"] = route{method: http.MethodPost, handler: s.handleEmbedText}
		s.routes["/v1/embeddings/batch"] = route{method: http.MethodPost, handler: s.handleBatchEmbed}
		s.routes["/v1/embedding/models"] = route{method: http.MethodGet, handler: s.handleListEmbeddingModels}
	}
	return s
}

// ---------------------------------------------------------------------------
// Tests: handleEmbedText
// ---------------------------------------------------------------------------

func TestHandleEmbedText_Success(t *testing.T) {
	svc := &mockEmbeddingService{}
	s := newEmbeddingTestServer(svc)

	body, _ := json.Marshal(embedTextRequest{
		Texts:    []string{"hello world"},
		Model:    "text-embedding-3-small",
		Provider: "openai",
	})
	r := httptest.NewRequest(http.MethodPost, "/v1/embeddings", bytes.NewReader(body))
	w := httptest.NewRecorder()

	s.handleEmbedText(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp embedTextResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Embeddings) != 1 {
		t.Errorf("expected 1 embedding, got %d", len(resp.Embeddings))
	}
	if resp.Model != "text-embedding-3-small" {
		t.Errorf("unexpected model: %s", resp.Model)
	}
	if resp.Cached {
		t.Error("expected Cached=false")
	}
	if resp.Usage == nil {
		t.Error("expected usage info")
	}
}

func TestHandleEmbedText_EmptyTexts(t *testing.T) {
	svc := &mockEmbeddingService{}
	s := newEmbeddingTestServer(svc)

	body, _ := json.Marshal(embedTextRequest{Texts: []string{}})
	r := httptest.NewRequest(http.MethodPost, "/v1/embeddings", bytes.NewReader(body))
	w := httptest.NewRecorder()

	s.handleEmbedText(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleEmbedText_MissingTexts(t *testing.T) {
	svc := &mockEmbeddingService{}
	s := newEmbeddingTestServer(svc)

	body, _ := json.Marshal(map[string]string{"model": "test"})
	r := httptest.NewRequest(http.MethodPost, "/v1/embeddings", bytes.NewReader(body))
	w := httptest.NewRecorder()

	s.handleEmbedText(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing texts, got %d", w.Code)
	}
}

func TestHandleEmbedText_InvalidJSON(t *testing.T) {
	svc := &mockEmbeddingService{}
	s := newEmbeddingTestServer(svc)

	r := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader("{bad json"))
	w := httptest.NewRecorder()

	s.handleEmbedText(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleEmbedText_ProviderError(t *testing.T) {
	svc := &mockEmbeddingService{
		embedFn: func(_ context.Context, _ *service.EmbedRequest) (*service.EmbedResponse, error) {
			return nil, &service.EmbeddingProviderNotFoundError{Provider: "missing"}
		},
	}
	s := newEmbeddingTestServer(svc)

	body, _ := json.Marshal(embedTextRequest{Texts: []string{"hi"}})
	r := httptest.NewRequest(http.MethodPost, "/v1/embeddings", bytes.NewReader(body))
	w := httptest.NewRecorder()

	s.handleEmbedText(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for provider not found, got %d", w.Code)
	}
}

func TestHandleEmbedText_ServiceNotConfigured(t *testing.T) {
	s := newEmbeddingTestServer(nil)
	s.routes["/v1/embeddings"] = route{method: http.MethodPost, handler: s.handleEmbedText}

	body, _ := json.Marshal(embedTextRequest{Texts: []string{"hi"}})
	r := httptest.NewRequest(http.MethodPost, "/v1/embeddings", bytes.NewReader(body))
	w := httptest.NewRecorder()

	s.handleEmbedText(w, r)

	if w.Code != http.StatusNotImplemented {
		t.Errorf("expected 501, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Tests: handleBatchEmbed
// ---------------------------------------------------------------------------

func TestHandleBatchEmbed_Success(t *testing.T) {
	svc := &mockEmbeddingService{}
	s := newEmbeddingTestServer(svc)

	body, _ := json.Marshal(batchEmbedRequest{
		Texts:     []string{"first text", "second text"},
		Model:     "text-embedding-3-small",
		BatchSize: 100,
	})
	r := httptest.NewRequest(http.MethodPost, "/v1/embeddings/batch", bytes.NewReader(body))
	w := httptest.NewRecorder()

	s.handleBatchEmbed(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp batchEmbedResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Embeddings) != 2 {
		t.Errorf("expected 2 embeddings, got %d", len(resp.Embeddings))
	}
	if resp.Model != "text-embedding-3-small" {
		t.Errorf("unexpected model: %s", resp.Model)
	}
	if resp.Usage == nil {
		t.Error("expected usage info")
	}
}

func TestHandleBatchEmbed_EmptyTexts(t *testing.T) {
	svc := &mockEmbeddingService{}
	s := newEmbeddingTestServer(svc)

	body, _ := json.Marshal(batchEmbedRequest{Texts: []string{}})
	r := httptest.NewRequest(http.MethodPost, "/v1/embeddings/batch", bytes.NewReader(body))
	w := httptest.NewRecorder()

	s.handleBatchEmbed(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleBatchEmbed_InvalidJSON(t *testing.T) {
	svc := &mockEmbeddingService{}
	s := newEmbeddingTestServer(svc)

	r := httptest.NewRequest(http.MethodPost, "/v1/embeddings/batch", strings.NewReader("{invalid"))
	w := httptest.NewRecorder()

	s.handleBatchEmbed(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleBatchEmbed_ServiceNotConfigured(t *testing.T) {
	s := newEmbeddingTestServer(nil)
	s.routes["/v1/embeddings/batch"] = route{method: http.MethodPost, handler: s.handleBatchEmbed}

	body, _ := json.Marshal(batchEmbedRequest{Texts: []string{"hi"}})
	r := httptest.NewRequest(http.MethodPost, "/v1/embeddings/batch", bytes.NewReader(body))
	w := httptest.NewRecorder()

	s.handleBatchEmbed(w, r)

	if w.Code != http.StatusNotImplemented {
		t.Errorf("expected 501, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Tests: handleListEmbeddingModels
// ---------------------------------------------------------------------------

func TestHandleListEmbeddingModels_Success(t *testing.T) {
	svc := &mockEmbeddingService{}
	s := newEmbeddingTestServer(svc)

	r := httptest.NewRequest(http.MethodGet, "/v1/embedding/models", nil)
	w := httptest.NewRecorder()

	s.handleListEmbeddingModels(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp listModelsResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Models) != 2 {
		t.Errorf("expected 2 models, got %d", len(resp.Models))
	}
	if resp.Models[0].Name != "text-embedding-3-small" {
		t.Errorf("unexpected model name: %s", resp.Models[0].Name)
	}
	if resp.Models[0].Dimensions != 1536 {
		t.Errorf("unexpected dimensions: %d", resp.Models[0].Dimensions)
	}
}

func TestHandleListEmbeddingModels_WithProvider(t *testing.T) {
	svc := &mockEmbeddingService{
		listModelsFn: func(_ context.Context, req *service.ListEmbeddingModelsRequest) (*service.ListEmbeddingModelsResponse, error) {
			if req.Provider != "ollama" {
				t.Errorf("expected provider=ollama, got %q", req.Provider)
			}
			return &service.ListEmbeddingModelsResponse{
				Models: []service.ModelInfo{{Name: "nomic-embed-text", Dimensions: 768}},
			}, nil
		},
	}
	s := newEmbeddingTestServer(svc)

	r := httptest.NewRequest(http.MethodGet, "/v1/embedding/models?provider=ollama", nil)
	w := httptest.NewRecorder()

	s.handleListEmbeddingModels(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp listModelsResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Models) != 1 || resp.Models[0].Name != "nomic-embed-text" {
		t.Errorf("unexpected models: %v", resp.Models)
	}
}

func TestHandleListEmbeddingModels_Error(t *testing.T) {
	svc := &mockEmbeddingService{
		listModelsFn: func(_ context.Context, _ *service.ListEmbeddingModelsRequest) (*service.ListEmbeddingModelsResponse, error) {
			return nil, &service.EmbeddingProviderNotFoundError{Provider: "unknown"}
		},
	}
	s := newEmbeddingTestServer(svc)

	r := httptest.NewRequest(http.MethodGet, "/v1/embedding/models", nil)
	w := httptest.NewRecorder()

	s.handleListEmbeddingModels(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestHandleListEmbeddingModels_ServiceNotConfigured(t *testing.T) {
	s := newEmbeddingTestServer(nil)
	s.routes["/v1/embedding/models"] = route{method: http.MethodGet, handler: s.handleListEmbeddingModels}

	r := httptest.NewRequest(http.MethodGet, "/v1/embedding/models", nil)
	w := httptest.NewRecorder()

	s.handleListEmbeddingModels(w, r)

	if w.Code != http.StatusNotImplemented {
		t.Errorf("expected 501, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Tests: mapHTTPError
// ---------------------------------------------------------------------------

func TestMapEmbeddingHTTPError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
	}{
		{
			name:     "provider not found",
			err:      &service.EmbeddingProviderNotFoundError{Provider: "missing"},
			wantCode: http.StatusNotFound,
		},
		{
			name:     "provider already registered",
			err:      &service.EmbeddingProviderRegistrationError{Provider: "dup", Cause: &service.EmbeddingProviderNotFoundError{}},
			wantCode: http.StatusConflict,
		},
		{
			name:     "service closed",
			err:      &service.EmbeddingServiceClosedError{},
			wantCode: http.StatusServiceUnavailable,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mapHTTPError(tc.err)
			if got != tc.wantCode {
				t.Errorf("mapHTTPError(%T) = %d, want %d", tc.err, got, tc.wantCode)
			}
		})
	}
}

// TestServerOption_WithEmbedding is the only check that WithEmbedding wires
// the service: the handler tests above build a Server literal.
func TestServerOption_WithEmbedding(t *testing.T) {
	embSvc := &mockEmbeddingService{}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0", WithEmbedding(embSvc))
	if s.embeddingSvc != embSvc {
		t.Error("embeddingSvc not set")
	}
}
