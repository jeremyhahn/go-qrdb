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

package rag

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetriever_Success(t *testing.T) {
	kvStore := newMockKVStore()
	kvStore.store["rag:test-ns:doc1:0"] = []byte("The quick brown fox")

	vecStore := newMockVecStore(nil, nil)
	vecStore.searchFn = func(_ context.Context, _ *SearchVecRequest) (*SearchVecResponse, error) {
		return &SearchVecResponse{
			Results: []*VecSearchResult{
				{
					ID:       1,
					Distance: 0.1,
					Metadata: map[string]interface{}{
						"doc_id":      "doc1",
						"chunk_index": "0",
					},
				},
			},
		}, nil
	}

	r := NewRetriever(newMockEmbProvider(nil), vecStore, kvStore)
	resp, err := r.Retrieve(context.Background(), &RetrieveRequest{
		Query:     "fox",
		Namespace: "test-ns",
		TopK:      5,
	})
	require.NoError(t, err)
	require.Len(t, resp.Results, 1)
	assert.Equal(t, "The quick brown fox", resp.Results[0].ChunkText)
	assert.Equal(t, "doc1", resp.Results[0].DocumentID)
	assert.Equal(t, 0, resp.Results[0].ChunkIndex)
}

func TestRetriever_EmptyQuery(t *testing.T) {
	r := NewRetriever(newMockEmbProvider(nil), newMockVecStore(nil, nil), newMockKVStore())
	_, err := r.Retrieve(context.Background(), &RetrieveRequest{
		Query:     "",
		Namespace: "ns",
	})
	require.Error(t, err)
	assert.IsType(t, &EmptyQueryError{}, err)
}

func TestRetriever_EmptyNamespace(t *testing.T) {
	r := NewRetriever(newMockEmbProvider(nil), newMockVecStore(nil, nil), newMockKVStore())
	_, err := r.Retrieve(context.Background(), &RetrieveRequest{
		Query:     "hello",
		Namespace: "",
	})
	require.Error(t, err)
	assert.IsType(t, &InvalidNamespaceError{}, err)
}

func TestRetriever_EmbeddingError(t *testing.T) {
	embProvider := newMockEmbProvider(nil)
	embProvider.embedFn = func(_ context.Context, _ *EmbedRequest) (*EmbedResponse, error) {
		return nil, errors.New("provider down")
	}
	r := NewRetriever(embProvider, newMockVecStore(nil, nil), newMockKVStore())
	_, err := r.Retrieve(context.Background(), &RetrieveRequest{
		Query:     "test query",
		Namespace: "ns",
	})
	require.Error(t, err)
	var embErr *EmbeddingError
	assert.True(t, errors.As(err, &embErr))
}

func TestRetriever_SearchError(t *testing.T) {
	vecStore := newMockVecStore(nil, errors.New("index unavailable"))
	r := NewRetriever(newMockEmbProvider(nil), vecStore, newMockKVStore())
	_, err := r.Retrieve(context.Background(), &RetrieveRequest{
		Query:     "test query",
		Namespace: "ns",
	})
	require.Error(t, err)
	var retErr *RetrievalError
	assert.True(t, errors.As(err, &retErr))
}

func TestRetriever_KVFetchError_SkipsResult(t *testing.T) {
	// When a chunk's KV key is missing, the result is silently skipped.
	kvStore := newMockKVStore() // empty store — Get will return error

	vecStore := newMockVecStore(nil, nil)
	vecStore.searchFn = func(_ context.Context, _ *SearchVecRequest) (*SearchVecResponse, error) {
		return &SearchVecResponse{
			Results: []*VecSearchResult{
				{
					ID:       99,
					Distance: 0.05,
					Metadata: map[string]interface{}{
						"doc_id":      "missing-doc",
						"chunk_index": "0",
					},
				},
			},
		}, nil
	}

	r := NewRetriever(newMockEmbProvider(nil), vecStore, kvStore)
	resp, err := r.Retrieve(context.Background(), &RetrieveRequest{
		Query:     "something",
		Namespace: "ns",
	})
	require.NoError(t, err)
	// The result is skipped because the KV key doesn't exist.
	assert.Empty(t, resp.Results)
}

func TestRetriever_DefaultTopK(t *testing.T) {
	var capturedK int
	vecStore := newMockVecStore(nil, nil)
	vecStore.searchFn = func(_ context.Context, req *SearchVecRequest) (*SearchVecResponse, error) {
		capturedK = req.K
		return &SearchVecResponse{}, nil
	}

	r := NewRetriever(newMockEmbProvider(nil), vecStore, newMockKVStore())
	_, err := r.Retrieve(context.Background(), &RetrieveRequest{
		Query:     "query",
		Namespace: "ns",
		TopK:      0, // should use defaultTopK
	})
	require.NoError(t, err)
	assert.Equal(t, defaultTopK, capturedK)
}

func TestRetriever_MetadataFilter(t *testing.T) {
	var capturedFilter map[string]interface{}
	vecStore := newMockVecStore(nil, nil)
	vecStore.searchFn = func(_ context.Context, req *SearchVecRequest) (*SearchVecResponse, error) {
		capturedFilter = req.MetadataFilter
		return &SearchVecResponse{}, nil
	}

	r := NewRetriever(newMockEmbProvider(nil), vecStore, newMockKVStore())
	_, err := r.Retrieve(context.Background(), &RetrieveRequest{
		Query:     "query",
		Namespace: "ns",
		Filter:    map[string]string{"category": "tech"},
	})
	require.NoError(t, err)
	assert.Equal(t, "tech", capturedFilter["category"])
}
