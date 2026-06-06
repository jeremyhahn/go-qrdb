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

package mcp

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Mock: EmbeddingServicer
// ---------------------------------------------------------------------------

type mockEmbeddingService struct {
	embedFn      func(ctx context.Context, req *service.EmbedRequest) (*service.EmbedResponse, error)
	batchEmbedFn func(ctx context.Context, req *service.BatchEmbedRequest) (*service.BatchEmbedResponse, error)
	listModelsFn func(ctx context.Context, req *service.ListEmbeddingModelsRequest) (*service.ListEmbeddingModelsResponse, error)
}

func (m *mockEmbeddingService) Embed(ctx context.Context, req *service.EmbedRequest) (*service.EmbedResponse, error) {
	if m.embedFn != nil {
		return m.embedFn(ctx, req)
	}
	return &service.EmbedResponse{
		Embeddings: [][]float32{{0.1, 0.2, 0.3}},
		Model:      "text-embedding-3-small",
		Cached:     false,
	}, nil
}

func (m *mockEmbeddingService) BatchEmbed(ctx context.Context, req *service.BatchEmbedRequest) (*service.BatchEmbedResponse, error) {
	if m.batchEmbedFn != nil {
		return m.batchEmbedFn(ctx, req)
	}
	vecs := make([][]float32, len(req.Texts))
	for i := range req.Texts {
		vecs[i] = []float32{float32(i) * 0.1}
	}
	return &service.BatchEmbedResponse{
		Embeddings: vecs,
		Model:      "text-embedding-3-small",
	}, nil
}

func (m *mockEmbeddingService) ListModels(ctx context.Context, req *service.ListEmbeddingModelsRequest) (*service.ListEmbeddingModelsResponse, error) {
	if m.listModelsFn != nil {
		return m.listModelsFn(ctx, req)
	}
	return &service.ListEmbeddingModelsResponse{
		Models: []service.ModelInfo{
			{Name: "text-embedding-3-small", Dimensions: 1536, MaxTokens: 8191},
		},
	}, nil
}

func (m *mockEmbeddingService) RegisterProvider(name string, provider service.EmbeddingProvider) error {
	return nil
}

func (m *mockEmbeddingService) DeregisterProvider(name string) error {
	return nil
}

func (m *mockEmbeddingService) Close() error {
	return nil
}

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

func findEmbeddingHandler(t *testing.T, svc service.EmbeddingServicer, toolName string) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	t.Helper()
	tools := embeddingTools(svc)
	for _, tool := range tools {
		if tool.Tool.Name == toolName {
			return tool.Handler
		}
	}
	t.Fatalf("tool %q not found in embeddingTools", toolName)
	return nil
}

// ---------------------------------------------------------------------------
// Tests: tool registration
// ---------------------------------------------------------------------------

func TestEmbeddingTools_Registration(t *testing.T) {
	svc := &mockEmbeddingService{}
	tools := embeddingTools(svc)

	expectedNames := []string{
		"qrdb_embed_text",
		"qrdb_batch_embed",
		"qrdb_list_embedding_models",
	}

	registered := make(map[string]bool, len(tools))
	for _, tool := range tools {
		registered[tool.Tool.Name] = true
	}

	for _, name := range expectedNames {
		if !registered[name] {
			t.Errorf("tool %q not registered", name)
		}
	}
	if len(tools) != len(expectedNames) {
		t.Errorf("expected %d tools, got %d", len(expectedNames), len(tools))
	}
	for _, tool := range tools {
		if tool.Handler == nil {
			t.Errorf("tool %q has nil handler", tool.Tool.Name)
		}
	}
}

// ---------------------------------------------------------------------------
// Tests: handleEmbedText
// ---------------------------------------------------------------------------

func TestEmbedText_Success(t *testing.T) {
	svc := &mockEmbeddingService{}
	handler := findEmbeddingHandler(t, svc, "qrdb_embed_text")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"texts": []any{"hello world"},
		"model": "text-embedding-3-small",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %s", resultText(t, result))
	}
	text := resultText(t, result)
	if text == "" {
		t.Error("expected non-empty result text")
	}
}

func TestEmbedText_MissingTexts(t *testing.T) {
	svc := &mockEmbeddingService{}
	handler := findEmbeddingHandler(t, svc, "qrdb_embed_text")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"model": "text-embedding-3-small",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing texts")
	}
}

func TestEmbedText_EmptyTexts(t *testing.T) {
	svc := &mockEmbeddingService{}
	handler := findEmbeddingHandler(t, svc, "qrdb_embed_text")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"texts": []any{},
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for empty texts")
	}
}

func TestEmbedText_InvalidTextType(t *testing.T) {
	svc := &mockEmbeddingService{}
	handler := findEmbeddingHandler(t, svc, "qrdb_embed_text")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"texts": []any{42, "hello"},
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for non-string element")
	}
}

func TestEmbedText_ServiceError(t *testing.T) {
	svc := &mockEmbeddingService{
		embedFn: func(_ context.Context, _ *service.EmbedRequest) (*service.EmbedResponse, error) {
			return nil, &service.EmbeddingProviderNotFoundError{Provider: "missing"}
		},
	}
	handler := findEmbeddingHandler(t, svc, "qrdb_embed_text")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"texts": []any{"hello"},
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for provider not found")
	}
}

// ---------------------------------------------------------------------------
// Tests: handleBatchEmbed
// ---------------------------------------------------------------------------

func TestBatchEmbed_Success(t *testing.T) {
	svc := &mockEmbeddingService{}
	handler := findEmbeddingHandler(t, svc, "qrdb_batch_embed")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"texts":      []any{"first", "second", "third"},
		"batch_size": float64(2),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %s", resultText(t, result))
	}
	text := resultText(t, result)
	if text == "" {
		t.Error("expected non-empty result text")
	}
}

func TestBatchEmbed_MissingTexts(t *testing.T) {
	svc := &mockEmbeddingService{}
	handler := findEmbeddingHandler(t, svc, "qrdb_batch_embed")

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing texts")
	}
}

func TestBatchEmbed_EmptyTexts(t *testing.T) {
	svc := &mockEmbeddingService{}
	handler := findEmbeddingHandler(t, svc, "qrdb_batch_embed")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"texts": []any{},
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for empty texts")
	}
}

func TestBatchEmbed_ServiceError(t *testing.T) {
	svc := &mockEmbeddingService{
		batchEmbedFn: func(_ context.Context, _ *service.BatchEmbedRequest) (*service.BatchEmbedResponse, error) {
			return nil, &service.EmbeddingServiceClosedError{}
		},
	}
	handler := findEmbeddingHandler(t, svc, "qrdb_batch_embed")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"texts": []any{"hello"},
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for closed service")
	}
}

// ---------------------------------------------------------------------------
// Tests: handleListEmbeddingModels
// ---------------------------------------------------------------------------

func TestListEmbeddingModels_Success(t *testing.T) {
	svc := &mockEmbeddingService{}
	handler := findEmbeddingHandler(t, svc, "qrdb_list_embedding_models")

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %s", resultText(t, result))
	}
	text := resultText(t, result)
	if text == "" {
		t.Error("expected non-empty result text")
	}
}

func TestListEmbeddingModels_WithProvider(t *testing.T) {
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
	handler := findEmbeddingHandler(t, svc, "qrdb_list_embedding_models")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"provider": "ollama",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %s", resultText(t, result))
	}
}

func TestListEmbeddingModels_Empty(t *testing.T) {
	svc := &mockEmbeddingService{
		listModelsFn: func(_ context.Context, _ *service.ListEmbeddingModelsRequest) (*service.ListEmbeddingModelsResponse, error) {
			return &service.ListEmbeddingModelsResponse{Models: nil}, nil
		},
	}
	handler := findEmbeddingHandler(t, svc, "qrdb_list_embedding_models")

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result for empty models: %s", resultText(t, result))
	}
}

func TestListEmbeddingModels_ServiceError(t *testing.T) {
	svc := &mockEmbeddingService{
		listModelsFn: func(_ context.Context, _ *service.ListEmbeddingModelsRequest) (*service.ListEmbeddingModelsResponse, error) {
			return nil, &service.EmbeddingProviderNotFoundError{Provider: ""}
		},
	}
	handler := findEmbeddingHandler(t, svc, "qrdb_list_embedding_models")

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for provider not found")
	}
}
