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

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/rag"
)

// ---------------------------------------------------------------------------
// EmbedProviderAdapter tests
// ---------------------------------------------------------------------------

func TestEmbedProviderAdapter_Embed_Success(t *testing.T) {
	svc := &ragMockEmbSvc{}
	adapter := NewEmbedProviderAdapter(svc)

	resp, err := adapter.Embed(context.Background(), &rag.EmbedRequest{
		Texts:    []string{"hello"},
		Model:    "test-model",
		Provider: "test-provider",
	})
	require.NoError(t, err)
	require.Len(t, resp.Embeddings, 1)
}

func TestEmbedProviderAdapter_Embed_Error(t *testing.T) {
	svc := &ragMockEmbSvc{embedErr: errors.New("embed failed")}
	adapter := NewEmbedProviderAdapter(svc)

	_, err := adapter.Embed(context.Background(), &rag.EmbedRequest{Texts: []string{"hello"}})
	require.Error(t, err)
}

func TestEmbedProviderAdapter_BatchEmbed_Success(t *testing.T) {
	svc := &ragMockEmbSvc{}
	adapter := NewEmbedProviderAdapter(svc)

	resp, err := adapter.BatchEmbed(context.Background(), &rag.BatchEmbedRequest{
		Texts: []string{"hello", "world"},
	})
	require.NoError(t, err)
	require.Len(t, resp.Embeddings, 2)
}

func TestEmbedProviderAdapter_BatchEmbed_Error(t *testing.T) {
	svc := &ragMockEmbSvc{batchErr: errors.New("batch failed")}
	adapter := NewEmbedProviderAdapter(svc)

	_, err := adapter.BatchEmbed(context.Background(), &rag.BatchEmbedRequest{
		Texts: []string{"hello"},
	})
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// VectorStoreAdapter tests
// ---------------------------------------------------------------------------

func TestVectorStoreAdapter_UpsertVector_Success(t *testing.T) {
	svc := &ragMockVecSvc{}
	adapter := NewVectorStoreAdapter(svc)

	resp, err := adapter.UpsertVector(context.Background(), &rag.UpsertVecRequest{
		Namespace: "ns",
		ID:        42,
		Vector:    []float32{0.1, 0.2},
		Metadata:  map[string]interface{}{"key": "val"},
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

func TestVectorStoreAdapter_UpsertVector_Error(t *testing.T) {
	svc := &ragMockVecSvc{upsertErr: errors.New("upsert failed")}
	adapter := NewVectorStoreAdapter(svc)

	_, err := adapter.UpsertVector(context.Background(), &rag.UpsertVecRequest{
		Namespace: "ns",
		ID:        1,
		Vector:    []float32{0.1},
	})
	require.Error(t, err)
}

func TestVectorStoreAdapter_SearchSimilar_Success(t *testing.T) {
	svc := &ragMockVecSvc{}
	svc.searchFn = func(_ context.Context, req *SearchSimilarRequest) (*SearchSimilarResponse, error) {
		return &SearchSimilarResponse{
			Results: []*VectorSearchResult{
				{
					ID:       1,
					Distance: 0.1,
					Metadata: map[string]interface{}{"doc_id": "d1"},
				},
			},
		}, nil
	}
	adapter := NewVectorStoreAdapter(svc)

	resp, err := adapter.SearchSimilar(context.Background(), &rag.SearchVecRequest{
		Namespace:       "ns",
		QueryVector:     []float32{0.1, 0.2},
		K:               5,
		IncludeMetadata: true,
	})
	require.NoError(t, err)
	require.Len(t, resp.Results, 1)
	assert.Equal(t, uint64(1), resp.Results[0].ID)
	assert.InDelta(t, 0.1, resp.Results[0].Distance, 0.001)
}

func TestVectorStoreAdapter_SearchSimilar_Error(t *testing.T) {
	svc := &ragMockVecSvc{searchErr: errors.New("search failed")}
	adapter := NewVectorStoreAdapter(svc)

	_, err := adapter.SearchSimilar(context.Background(), &rag.SearchVecRequest{
		Namespace:   "ns",
		QueryVector: []float32{0.1},
	})
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// KVStoreAdapter tests
// ---------------------------------------------------------------------------

func TestKVStoreAdapter_PutGetDeleteScan(t *testing.T) {
	kv := newRAGMockKV()
	adapter := NewKVStoreAdapter(kv)
	ctx := context.Background()

	// Put
	require.NoError(t, adapter.Put(ctx, "key1", []byte("val1")))

	// Get
	val, err := adapter.Get(ctx, "key1")
	require.NoError(t, err)
	assert.Equal(t, []byte("val1"), val)

	// Scan
	require.NoError(t, adapter.Put(ctx, "pfx:a", []byte("a")))
	require.NoError(t, adapter.Put(ctx, "pfx:b", []byte("b")))
	results, err := adapter.Scan(ctx, "pfx:")
	require.NoError(t, err)
	assert.Len(t, results, 2)

	// Delete
	require.NoError(t, adapter.Delete(ctx, "key1"))
	_, err = adapter.Get(ctx, "key1")
	require.Error(t, err)
}

func TestKVStoreAdapter_Get_NotFound(t *testing.T) {
	kv := newRAGMockKV()
	adapter := NewKVStoreAdapter(kv)

	_, err := adapter.Get(context.Background(), "missing")
	require.Error(t, err)
}
