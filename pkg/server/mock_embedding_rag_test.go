// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0

package server

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Mock embedding and RAG service stubs for option testing only.
// ---------------------------------------------------------------------------

type mockEmbeddingService struct{}

func (m *mockEmbeddingService) Embed(_ context.Context, _ *service.EmbedRequest) (*service.EmbedResponse, error) {
	return nil, nil
}
func (m *mockEmbeddingService) BatchEmbed(_ context.Context, _ *service.BatchEmbedRequest) (*service.BatchEmbedResponse, error) {
	return nil, nil
}
func (m *mockEmbeddingService) ListModels(_ context.Context, _ *service.ListEmbeddingModelsRequest) (*service.ListEmbeddingModelsResponse, error) {
	return nil, nil
}
func (m *mockEmbeddingService) RegisterProvider(_ string, _ service.EmbeddingProvider) error {
	return nil
}
func (m *mockEmbeddingService) DeregisterProvider(_ string) error { return nil }
func (m *mockEmbeddingService) Close() error                      { return nil }

type mockRAGService struct{}

func (m *mockRAGService) IngestDocument(_ context.Context, _ *service.IngestDocumentRequest) (*service.IngestDocumentResponse, error) {
	return nil, nil
}
func (m *mockRAGService) Query(_ context.Context, _ *service.RAGQueryRequest) (*service.RAGQueryResponse, error) {
	return nil, nil
}
func (m *mockRAGService) DeleteDocument(_ context.Context, _ *service.DeleteRAGDocumentRequest) (*service.DeleteRAGDocumentResponse, error) {
	return nil, nil
}
func (m *mockRAGService) ListDocuments(_ context.Context, _ *service.ListRAGDocumentsRequest) (*service.ListRAGDocumentsResponse, error) {
	return nil, nil
}
func (m *mockRAGService) Close() error { return nil }
