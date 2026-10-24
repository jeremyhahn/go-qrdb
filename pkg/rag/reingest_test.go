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
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/rag/chunker"
)

// memVecStore is a VectorStore that keeps what it is given: a search returns
// every live vector of the namespace, so a vector the pipeline failed to
// remove is visible to the retriever exactly as it would be in a real index.
type memVecStore struct {
	vectors   map[string]map[uint64]map[string]interface{}
	deleteErr error
}

var _ VectorStore = (*memVecStore)(nil)

func newMemVecStore() *memVecStore {
	return &memVecStore{vectors: make(map[string]map[uint64]map[string]interface{})}
}

func (m *memVecStore) UpsertVector(_ context.Context, req *UpsertVecRequest) (*UpsertVecResponse, error) {
	if m.vectors[req.Namespace] == nil {
		m.vectors[req.Namespace] = make(map[uint64]map[string]interface{})
	}
	m.vectors[req.Namespace][req.ID] = req.Metadata
	return &UpsertVecResponse{Success: true}, nil
}

func (m *memVecStore) SearchSimilar(_ context.Context, req *SearchVecRequest) (*SearchVecResponse, error) {
	ids := make([]uint64, 0, len(m.vectors[req.Namespace]))
	for id := range m.vectors[req.Namespace] {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	resp := &SearchVecResponse{}
	for _, id := range ids {
		if len(resp.Results) == req.K {
			break
		}
		resp.Results = append(resp.Results, &VecSearchResult{ID: id, Metadata: m.vectors[req.Namespace][id]})
	}
	return resp, nil
}

func (m *memVecStore) EnsureNamespace(context.Context, string, int, string) error {
	return nil
}

func (m *memVecStore) DeleteVector(_ context.Context, namespace string, id uint64) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	delete(m.vectors[namespace], id)
	return nil
}

// smallChunks makes every ten characters a chunk, so a long text has many.
var smallChunks = &chunker.Config{Strategy: "fixed", ChunkSize: 10, Overlap: 0, MinSize: 0}

func ingestText(t *testing.T, ing *Ingestor, docID, text string) *IngestResponse {
	t.Helper()
	resp, err := ing.Ingest(context.Background(), &IngestRequest{
		Document:    &Document{ID: docID, Text: text},
		Namespace:   "ns",
		ChunkConfig: smallChunks,
	})
	require.NoError(t, err)
	return resp
}

func TestIngestor_ReingestShorterRemovesOldChunks(t *testing.T) {
	emb := newMockEmbProvider(nil)
	vec := newMemVecStore()
	kv := newMockKVStore()
	ing := NewIngestor(emb, vec, kv, 0)

	long := ingestText(t, ing, "doc", strings.Repeat("old text ", 20))
	require.Greater(t, long.ChunkCount, 5)
	short := ingestText(t, ing, "doc", "new text")
	require.Equal(t, 1, short.ChunkCount)

	resp, err := NewRetriever(emb, vec, kv).Retrieve(context.Background(), &RetrieveRequest{
		Query: "text", Namespace: "ns", TopK: 100,
	})
	require.NoError(t, err)
	require.Len(t, resp.Results, 1, "only the new version's chunk is searchable")
	assert.Equal(t, "new text", resp.Results[0].ChunkText)
	assert.Len(t, vec.vectors["ns"], 1, "the old version's extra vectors are removed")

	stale, err := ing.staleChunks(context.Background(), "ns", "doc", 1)
	require.NoError(t, err)
	assert.Empty(t, stale, "the old version's extra chunk text is removed")

	var info DocumentInfo
	require.NoError(t, json.Unmarshal(kv.store[docMetaKVKey("ns", "doc")], &info))
	assert.Equal(t, 1, info.ChunkCount)
}

func TestIngestor_ReingestLongerKeepsEveryChunk(t *testing.T) {
	emb := newMockEmbProvider(nil)
	vec := newMemVecStore()
	kv := newMockKVStore()
	ing := NewIngestor(emb, vec, kv, 0)

	ingestText(t, ing, "doc", "new text")
	long := ingestText(t, ing, "doc", strings.Repeat("old text ", 20))

	assert.Len(t, vec.vectors["ns"], long.ChunkCount)
	stale, err := ing.staleChunks(context.Background(), "ns", "doc", 0)
	require.NoError(t, err)
	assert.Len(t, stale, long.ChunkCount)
}

func TestIngestor_ReingestLeavesDocumentWithLongerIDAlone(t *testing.T) {
	emb := newMockEmbProvider(nil)
	vec := newMemVecStore()
	kv := newMockKVStore()
	ing := NewIngestor(emb, vec, kv, 0)

	other := ingestText(t, ing, "doc:2", strings.Repeat("other ", 20))
	ingestText(t, ing, "doc", strings.Repeat("old text ", 20))
	ingestText(t, ing, "doc", "new text")

	stale, err := ing.staleChunks(context.Background(), "ns", "doc:2", 0)
	require.NoError(t, err)
	assert.Len(t, stale, other.ChunkCount, "doc:2 keeps every chunk")
	assert.Len(t, vec.vectors["ns"], other.ChunkCount+1)
}

func TestIngestor_ReingestScanErrorWritesNothing(t *testing.T) {
	emb := newMockEmbProvider(nil)
	vec := newMemVecStore()
	kv := &scanErrKVStore{mockKVStore: newMockKVStore(), err: errors.New("scan down")}
	ing := NewIngestor(emb, vec, kv, 0)

	_, err := ing.Ingest(context.Background(), &IngestRequest{
		Document:    &Document{ID: "doc", Text: "new text"},
		Namespace:   "ns",
		ChunkConfig: smallChunks,
	})
	var storeErr *StoreError
	require.ErrorAs(t, err, &storeErr)
	assert.ErrorIs(t, err, kv.err)
	assert.Empty(t, vec.vectors["ns"])
	assert.Empty(t, kv.store)
}

func TestIngestor_ReingestRemovalErrorIsRetryable(t *testing.T) {
	emb := newMockEmbProvider(nil)
	vec := newMemVecStore()
	kv := newMockKVStore()
	ing := NewIngestor(emb, vec, kv, 0)

	long := ingestText(t, ing, "doc", strings.Repeat("old text ", 20))

	vec.deleteErr = errors.New("vector store down")
	_, err := ing.Ingest(context.Background(), &IngestRequest{
		Document:    &Document{ID: "doc", Text: "new text"},
		Namespace:   "ns",
		ChunkConfig: smallChunks,
	})
	var storeErr *StoreError
	require.ErrorAs(t, err, &storeErr)
	assert.ErrorIs(t, err, vec.deleteErr)

	var info DocumentInfo
	require.NoError(t, json.Unmarshal(kv.store[docMetaKVKey("ns", "doc")], &info))
	assert.Equal(t, long.ChunkCount, info.ChunkCount, "the document record is not rewritten")

	vec.deleteErr = nil
	ingestText(t, ing, "doc", "new text")
	assert.Len(t, vec.vectors["ns"], 1, "the retry removes what the failed ingest left")
}

func TestIngestor_ReingestTextRemovalError(t *testing.T) {
	emb := newMockEmbProvider(nil)
	vec := newMemVecStore()
	kv := &deleteErrKVStore{mockKVStore: newMockKVStore()}
	ing := NewIngestor(emb, vec, kv, 0)

	ingestText(t, ing, "doc", strings.Repeat("old text ", 20))
	kv.err = errors.New("kv down")
	_, err := ing.Ingest(context.Background(), &IngestRequest{
		Document:    &Document{ID: "doc", Text: "new text"},
		Namespace:   "ns",
		ChunkConfig: smallChunks,
	})
	var storeErr *StoreError
	require.ErrorAs(t, err, &storeErr)
	assert.ErrorIs(t, err, kv.err)
	assert.Contains(t, err.Error(), "remove chunk text")
}

func TestIngestor_RemoveChunksLeavesNothingSearchable(t *testing.T) {
	emb := newMockEmbProvider(nil)
	vec := newMemVecStore()
	kv := newMockKVStore()
	ing := NewIngestor(emb, vec, kv, 0)

	other := ingestText(t, ing, "doc:2", strings.Repeat("other ", 20))
	ingestText(t, ing, "doc", strings.Repeat("old text ", 20))

	require.NoError(t, ing.RemoveChunks(context.Background(), "ns", "doc"))

	resp, err := NewRetriever(emb, vec, kv).Retrieve(context.Background(), &RetrieveRequest{
		Query: "text", Namespace: "ns", TopK: 100,
	})
	require.NoError(t, err)
	require.Len(t, resp.Results, other.ChunkCount, "only doc:2 is searchable")
	for _, r := range resp.Results {
		assert.Equal(t, "doc:2", r.DocumentID)
	}
	stale, err := ing.staleChunks(context.Background(), "ns", "doc", 0)
	require.NoError(t, err)
	assert.Empty(t, stale, "every chunk's text is removed")
	assert.Contains(t, kv.store, docMetaKVKey("ns", "doc"), "the record is the caller's to remove")
}

func TestIngestor_RemoveChunksErrorIsRetryable(t *testing.T) {
	emb := newMockEmbProvider(nil)
	vec := newMemVecStore()
	kv := newMockKVStore()
	ing := NewIngestor(emb, vec, kv, 0)
	ingestText(t, ing, "doc", strings.Repeat("old text ", 20))

	vec.deleteErr = errors.New("vector store down")
	err := ing.RemoveChunks(context.Background(), "ns", "doc")
	var storeErr *StoreError
	require.ErrorAs(t, err, &storeErr)
	assert.ErrorIs(t, err, vec.deleteErr)
	assert.Contains(t, err.Error(), "remove chunk vector")

	vec.deleteErr = nil
	require.NoError(t, ing.RemoveChunks(context.Background(), "ns", "doc"))
	assert.Empty(t, vec.vectors["ns"], "the retry removes every vector")
}

func TestIngestor_RemoveChunksScanError(t *testing.T) {
	kv := &scanErrKVStore{mockKVStore: newMockKVStore(), err: errors.New("scan down")}
	ing := NewIngestor(newMockEmbProvider(nil), newMemVecStore(), kv, 0)

	err := ing.RemoveChunks(context.Background(), "ns", "doc")
	var storeErr *StoreError
	require.ErrorAs(t, err, &storeErr)
	assert.ErrorIs(t, err, kv.err)
}

func TestIngestor_StoreErrorsAreStoreErrors(t *testing.T) {
	putErr := errors.New("put failed")
	kv := newMockKVStore()
	kv.putFn = func(context.Context, string, []byte) error { return putErr }
	ing := NewIngestor(newMockEmbProvider(nil), newMemVecStore(), kv, 0)

	_, err := ing.Ingest(context.Background(), &IngestRequest{
		Document:  &Document{ID: "doc", Text: "some text"},
		Namespace: "ns",
	})
	var storeErr *StoreError
	require.ErrorAs(t, err, &storeErr)
	var chunkErr *ChunkingError
	assert.False(t, errors.As(err, &chunkErr), "a store failure is not a chunking failure")
	assert.ErrorIs(t, err, putErr)
}

type scanErrKVStore struct {
	*mockKVStore
	err error
}

func (s *scanErrKVStore) Scan(context.Context, string) (map[string][]byte, error) {
	return nil, s.err
}

type deleteErrKVStore struct {
	*mockKVStore
	err error
}

func (s *deleteErrKVStore) Delete(ctx context.Context, key string) error {
	if s.err != nil {
		return s.err
	}
	return s.mockKVStore.Delete(ctx, key)
}
