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
)

// ---------------------------------------------------------------------------
// mockEmbProvider — minimal EmbedProvider mock for unit tests.
// ---------------------------------------------------------------------------

type mockEmbProvider struct {
	batchErr     error
	batchEmbedFn func(ctx context.Context, req *BatchEmbedRequest) (*BatchEmbedResponse, error)
	embedFn      func(ctx context.Context, req *EmbedRequest) (*EmbedResponse, error)
}

var _ EmbedProvider = (*mockEmbProvider)(nil)

func newMockEmbProvider(batchErr error) *mockEmbProvider {
	return &mockEmbProvider{batchErr: batchErr}
}

func newMockEmbProviderWithFn(fn func(context.Context, *BatchEmbedRequest) (*BatchEmbedResponse, error)) *mockEmbProvider {
	return &mockEmbProvider{batchEmbedFn: fn}
}

func (m *mockEmbProvider) Embed(ctx context.Context, req *EmbedRequest) (*EmbedResponse, error) {
	if m.embedFn != nil {
		return m.embedFn(ctx, req)
	}
	embs := make([][]float32, len(req.Texts))
	for i := range embs {
		embs[i] = []float32{0.1, 0.2, 0.3}
	}
	return &EmbedResponse{Embeddings: embs}, nil
}

func (m *mockEmbProvider) BatchEmbed(ctx context.Context, req *BatchEmbedRequest) (*BatchEmbedResponse, error) {
	if m.batchEmbedFn != nil {
		return m.batchEmbedFn(ctx, req)
	}
	if m.batchErr != nil {
		return nil, m.batchErr
	}
	embs := make([][]float32, len(req.Texts))
	for i := range embs {
		embs[i] = []float32{0.1, 0.2, 0.3}
	}
	return &BatchEmbedResponse{Embeddings: embs}, nil
}

// ---------------------------------------------------------------------------
// mockVecStore — minimal VectorStore mock for unit tests.
// ---------------------------------------------------------------------------

type mockVecStore struct {
	upsertErr error
	searchErr error
	upsertFn  func(context.Context, *UpsertVecRequest) (*UpsertVecResponse, error)
	searchFn  func(context.Context, *SearchVecRequest) (*SearchVecResponse, error)
}

var _ VectorStore = (*mockVecStore)(nil)

func newMockVecStore(upsertErr, searchErr error) *mockVecStore {
	return &mockVecStore{upsertErr: upsertErr, searchErr: searchErr}
}

func (m *mockVecStore) UpsertVector(ctx context.Context, req *UpsertVecRequest) (*UpsertVecResponse, error) {
	if m.upsertFn != nil {
		return m.upsertFn(ctx, req)
	}
	if m.upsertErr != nil {
		return nil, m.upsertErr
	}
	return &UpsertVecResponse{Success: true}, nil
}

func (m *mockVecStore) SearchSimilar(ctx context.Context, req *SearchVecRequest) (*SearchVecResponse, error) {
	if m.searchFn != nil {
		return m.searchFn(ctx, req)
	}
	if m.searchErr != nil {
		return nil, m.searchErr
	}
	return &SearchVecResponse{}, nil
}

// ---------------------------------------------------------------------------
// mockKVStore — minimal KVStore mock for unit tests.
// ---------------------------------------------------------------------------

type mockKVStore struct {
	store map[string][]byte
	putFn func(context.Context, string, []byte) error
	getFn func(context.Context, string) ([]byte, error)
}

var _ KVStore = (*mockKVStore)(nil)

func newMockKVStore() *mockKVStore {
	return &mockKVStore{store: make(map[string][]byte)}
}

func (m *mockKVStore) Put(ctx context.Context, key string, value []byte) error {
	if m.putFn != nil {
		return m.putFn(ctx, key, value)
	}
	m.store[key] = value
	return nil
}

func (m *mockKVStore) Get(ctx context.Context, key string) ([]byte, error) {
	if m.getFn != nil {
		return m.getFn(ctx, key)
	}
	v, ok := m.store[key]
	if !ok {
		return nil, errors.New("key not found: " + key)
	}
	return v, nil
}

func (m *mockKVStore) Delete(_ context.Context, key string) error {
	delete(m.store, key)
	return nil
}

func (m *mockKVStore) Scan(_ context.Context, prefix string) (map[string][]byte, error) {
	result := make(map[string][]byte)
	for k, v := range m.store {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			result[k] = v
		}
	}
	return result, nil
}
