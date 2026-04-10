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

// Package service provides adapters that satisfy the narrow interfaces defined
// in pkg/rag so that service-layer types can be passed to the RAG pipeline
// without creating an import cycle.
package service

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/rag"
)

// ---------------------------------------------------------------------------
// EmbedProviderAdapter
// ---------------------------------------------------------------------------

// EmbedProviderAdapter wraps EmbeddingServicer so it satisfies rag.EmbedProvider.
type EmbedProviderAdapter struct {
	svc EmbeddingServicer
}

// NewEmbedProviderAdapter wraps svc as a rag.EmbedProvider.
func NewEmbedProviderAdapter(svc EmbeddingServicer) *EmbedProviderAdapter {
	return &EmbedProviderAdapter{svc: svc}
}

// Compile-time assertion.
var _ rag.EmbedProvider = (*EmbedProviderAdapter)(nil)

// Embed implements rag.EmbedProvider by delegating to the underlying service.
func (a *EmbedProviderAdapter) Embed(ctx context.Context, req *rag.EmbedRequest) (*rag.EmbedResponse, error) {
	resp, err := a.svc.Embed(ctx, &EmbedRequest{
		Texts:    req.Texts,
		Model:    req.Model,
		Provider: req.Provider,
	})
	if err != nil {
		return nil, err
	}
	return &rag.EmbedResponse{Embeddings: resp.Embeddings}, nil
}

// BatchEmbed implements rag.EmbedProvider by delegating to the underlying service.
func (a *EmbedProviderAdapter) BatchEmbed(ctx context.Context, req *rag.BatchEmbedRequest) (*rag.BatchEmbedResponse, error) {
	resp, err := a.svc.BatchEmbed(ctx, &BatchEmbedRequest{
		Texts:    req.Texts,
		Model:    req.Model,
		Provider: req.Provider,
	})
	if err != nil {
		return nil, err
	}
	return &rag.BatchEmbedResponse{Embeddings: resp.Embeddings}, nil
}

// ---------------------------------------------------------------------------
// VectorStoreAdapter
// ---------------------------------------------------------------------------

// VectorStoreAdapter wraps VectorService so it satisfies rag.VectorStore.
type VectorStoreAdapter struct {
	svc VectorService
}

// NewVectorStoreAdapter wraps svc as a rag.VectorStore.
func NewVectorStoreAdapter(svc VectorService) *VectorStoreAdapter {
	return &VectorStoreAdapter{svc: svc}
}

// Compile-time assertion.
var _ rag.VectorStore = (*VectorStoreAdapter)(nil)

// UpsertVector implements rag.VectorStore by delegating to the underlying service.
func (a *VectorStoreAdapter) UpsertVector(ctx context.Context, req *rag.UpsertVecRequest) (*rag.UpsertVecResponse, error) {
	resp, err := a.svc.UpsertVector(ctx, &UpsertVectorRequest{
		Namespace: req.Namespace,
		ID:        req.ID,
		Vector:    req.Vector,
		Metadata:  req.Metadata,
	})
	if err != nil {
		return nil, err
	}
	return &rag.UpsertVecResponse{Success: resp.Success}, nil
}

// SearchSimilar implements rag.VectorStore by delegating to the underlying service.
func (a *VectorStoreAdapter) SearchSimilar(ctx context.Context, req *rag.SearchVecRequest) (*rag.SearchVecResponse, error) {
	resp, err := a.svc.SearchSimilar(ctx, &SearchSimilarRequest{
		Namespace:       req.Namespace,
		QueryVector:     req.QueryVector,
		K:               req.K,
		IncludeMetadata: req.IncludeMetadata,
		MetadataFilter:  req.MetadataFilter,
	})
	if err != nil {
		return nil, err
	}

	results := make([]*rag.VecSearchResult, len(resp.Results))
	for i, r := range resp.Results {
		results[i] = &rag.VecSearchResult{
			ID:       r.ID,
			Distance: r.Distance,
			Metadata: r.Metadata,
		}
	}
	return &rag.SearchVecResponse{Results: results}, nil
}

// ---------------------------------------------------------------------------
// KVStoreAdapter
// ---------------------------------------------------------------------------

// KVStoreAdapter wraps KVServicer so it satisfies rag.KVStore.
type KVStoreAdapter struct {
	svc KVServicer
}

// NewKVStoreAdapter wraps svc as a rag.KVStore.
func NewKVStoreAdapter(svc KVServicer) *KVStoreAdapter {
	return &KVStoreAdapter{svc: svc}
}

// Compile-time assertion.
var _ rag.KVStore = (*KVStoreAdapter)(nil)

// Put implements rag.KVStore.
func (a *KVStoreAdapter) Put(ctx context.Context, key string, value []byte) error {
	return a.svc.Put(ctx, key, value)
}

// Get implements rag.KVStore.
func (a *KVStoreAdapter) Get(ctx context.Context, key string) ([]byte, error) {
	return a.svc.Get(ctx, key)
}

// Delete implements rag.KVStore.
func (a *KVStoreAdapter) Delete(ctx context.Context, key string) error {
	return a.svc.Delete(ctx, key)
}

// Scan implements rag.KVStore.
func (a *KVStoreAdapter) Scan(ctx context.Context, prefix string) (map[string][]byte, error) {
	return a.svc.Scan(ctx, prefix)
}
