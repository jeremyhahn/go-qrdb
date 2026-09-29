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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
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

// TestWithEmbedding verifies that WithEmbedding sets the embedding service option.
func TestWithEmbedding(t *testing.T) {
	embSvc := &mockEmbeddingService{}
	cfg := testConfig(t, false, false)

	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithEmbedding(embSvc))

	if srv.opts.embeddingSvc == nil {
		t.Fatal("expected embeddingSvc to be set via WithEmbedding")
	}
	if srv.opts.embeddingSvc != embSvc {
		t.Error("embeddingSvc should be the same instance passed to WithEmbedding")
	}
}

// TestWithRAG verifies that WithRAG sets the RAG service option.
func TestWithRAG(t *testing.T) {
	ragSvc := &mockRAGService{}
	cfg := testConfig(t, false, false)

	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithRAG(ragSvc))

	if srv.opts.ragSvc == nil {
		t.Fatal("expected ragSvc to be set via WithRAG")
	}
	if srv.opts.ragSvc != ragSvc {
		t.Error("ragSvc should be the same instance passed to WithRAG")
	}
}

// TestWithDatabaseServiceRegistry verifies that WithDatabaseServiceRegistry
// sets the database service registry option.
func TestWithDatabaseServiceRegistry(t *testing.T) {
	reg := &DatabaseServiceRegistry{}
	cfg := testConfig(t, false, false)

	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithDatabaseServiceRegistry(reg))

	if srv.opts.dbRegistry == nil {
		t.Fatal("expected dbRegistry to be set via WithDatabaseServiceRegistry")
	}
	if srv.opts.dbRegistry != reg {
		t.Error("dbRegistry should be the same instance passed to WithDatabaseServiceRegistry")
	}
}

// TestServiceRegistry_NilWithoutDatabaseRegistry verifies the exported
// accessor reports the absence of a configured registry the same way every
// internal call site observes it, rather than handing back an empty resolver
// that would silently answer for every (tenant, database).
func TestServiceRegistry_NilWithoutDatabaseRegistry(t *testing.T) {
	srv := New(testConfig(t, false, false), newMockKVService(), &mockAdminService{})

	if reg := srv.ServiceRegistry(); reg != nil {
		t.Fatalf("ServiceRegistry() = %#v, want nil when no database registry is configured", reg)
	}
}

// TestServiceRegistry_ResolvesThroughDatabaseRegistry verifies the accessor
// returns a working registry built over the configured database registry, and
// that it is the same construction the wire listeners get: a fresh instance
// per call (see buildServiceRegistry for why it is not memoized).
func TestServiceRegistry_ResolvesThroughDatabaseRegistry(t *testing.T) {
	dbReg := NewDatabaseServiceRegistry("system")
	kv := newMockKVService()
	dbReg.Register("system", &DatabaseServices{Name: "system", KV: kv})

	srv := New(testConfig(t, false, false), kv, &mockAdminService{},
		WithDatabaseServiceRegistry(dbReg))

	reg := srv.ServiceRegistry()
	if reg == nil {
		t.Fatal("ServiceRegistry() = nil, want a registry")
	}

	resolved, err := reg.KVService(tenantid.System, "system")
	if err != nil {
		t.Fatalf("KVService: %v", err)
	}
	if resolved == nil {
		t.Fatal("KVService returned a nil servicer")
	}

	if second := srv.ServiceRegistry(); second == reg {
		t.Error("ServiceRegistry() must build a fresh registry per call, matching every internal call site")
	}
}
