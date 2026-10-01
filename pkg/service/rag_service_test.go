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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// Minimal mocks local to the service package test.
// ---------------------------------------------------------------------------

// ragMockEmbSvc satisfies EmbeddingServicer for RAG service tests.
type ragMockEmbSvc struct {
	batchErr error
	embedErr error
}

var _ EmbeddingServicer = (*ragMockEmbSvc)(nil)

func (m *ragMockEmbSvc) Embed(_ context.Context, req *EmbedRequest) (*EmbedResponse, error) {
	if m.embedErr != nil {
		return nil, m.embedErr
	}
	embs := make([][]float32, len(req.Texts))
	for i := range embs {
		embs[i] = []float32{0.1, 0.2}
	}
	return &EmbedResponse{Embeddings: embs}, nil
}

func (m *ragMockEmbSvc) BatchEmbed(_ context.Context, req *BatchEmbedRequest) (*BatchEmbedResponse, error) {
	if m.batchErr != nil {
		return nil, m.batchErr
	}
	embs := make([][]float32, len(req.Texts))
	for i := range embs {
		embs[i] = []float32{0.1, 0.2}
	}
	return &BatchEmbedResponse{Embeddings: embs}, nil
}

func (m *ragMockEmbSvc) ListModels(_ context.Context, _ *ListEmbeddingModelsRequest) (*ListEmbeddingModelsResponse, error) {
	return &ListEmbeddingModelsResponse{}, nil
}

func (m *ragMockEmbSvc) RegisterProvider(_ string, _ EmbeddingProvider) error { return nil }
func (m *ragMockEmbSvc) DeregisterProvider(_ string) error                    { return nil }
func (m *ragMockEmbSvc) Close() error                                         { return nil }

// ragMockVecSvc satisfies VectorService for RAG service tests.
type ragMockVecSvc struct {
	upsertErr error
	searchErr error
	searchFn  func(context.Context, *SearchSimilarRequest) (*SearchSimilarResponse, error)
}

var _ VectorService = (*ragMockVecSvc)(nil)

func (m *ragMockVecSvc) CreateNamespace(_ context.Context, _ *CreateNamespaceRequest) (*CreateNamespaceResponse, error) {
	return &CreateNamespaceResponse{}, nil
}
func (m *ragMockVecSvc) DeleteNamespace(_ context.Context, _ *DeleteNamespaceRequest) (*DeleteNamespaceResponse, error) {
	return &DeleteNamespaceResponse{}, nil
}
func (m *ragMockVecSvc) ListNamespaces(_ context.Context, _ *ListNamespacesRequest) (*ListNamespacesResponse, error) {
	return &ListNamespacesResponse{}, nil
}
func (m *ragMockVecSvc) GetNamespaceInfo(_ context.Context, _ *GetNamespaceInfoRequest) (*GetNamespaceInfoResponse, error) {
	return &GetNamespaceInfoResponse{}, nil
}
func (m *ragMockVecSvc) UpsertVector(_ context.Context, _ *UpsertVectorRequest) (*UpsertVectorResponse, error) {
	if m.upsertErr != nil {
		return nil, m.upsertErr
	}
	return &UpsertVectorResponse{Success: true}, nil
}
func (m *ragMockVecSvc) GetVector(_ context.Context, _ *GetVectorRequest) (*GetVectorResponse, error) {
	return &GetVectorResponse{}, nil
}
func (m *ragMockVecSvc) DeleteVector(_ context.Context, _ *DeleteVectorRequest) (*DeleteVectorResponse, error) {
	return &DeleteVectorResponse{Success: true}, nil
}
func (m *ragMockVecSvc) BatchUpsert(_ context.Context, _ *BatchUpsertRequest) (*BatchUpsertResponse, error) {
	return &BatchUpsertResponse{}, nil
}
func (m *ragMockVecSvc) BatchDeleteVectors(_ context.Context, _ *BatchDeleteVectorsRequest) (*BatchDeleteVectorsResponse, error) {
	return &BatchDeleteVectorsResponse{}, nil
}
func (m *ragMockVecSvc) ListVectors(_ context.Context, _ *ListVectorsRequest) (*ListVectorsResponse, error) {
	return &ListVectorsResponse{}, nil
}
func (m *ragMockVecSvc) UpdateMetadata(_ context.Context, _ *UpdateVectorMetadataRequest) (*UpdateVectorMetadataResponse, error) {
	return &UpdateVectorMetadataResponse{}, nil
}
func (m *ragMockVecSvc) SearchSimilar(ctx context.Context, req *SearchSimilarRequest) (*SearchSimilarResponse, error) {
	if m.searchFn != nil {
		return m.searchFn(ctx, req)
	}
	if m.searchErr != nil {
		return nil, m.searchErr
	}
	return &SearchSimilarResponse{}, nil
}
func (m *ragMockVecSvc) SearchByMetadata(_ context.Context, _ *SearchByMetadataRequest) (*SearchByMetadataResponse, error) {
	return &SearchByMetadataResponse{}, nil
}
func (m *ragMockVecSvc) HybridSearch(_ context.Context, _ *HybridSearchRequest) (*HybridSearchResponse, error) {
	return &HybridSearchResponse{}, nil
}
func (m *ragMockVecSvc) SearchByID(_ context.Context, _ *SearchByIDRequest) (*SearchByIDResponse, error) {
	return &SearchByIDResponse{}, nil
}
func (m *ragMockVecSvc) RebuildIndex(_ context.Context, _ *RebuildIndexRequest) (*RebuildIndexResponse, error) {
	return &RebuildIndexResponse{}, nil
}
func (m *ragMockVecSvc) GetRebuildProgress(_ context.Context, _ *GetRebuildProgressRequest) (*GetRebuildProgressResponse, error) {
	return &GetRebuildProgressResponse{}, nil
}
func (m *ragMockVecSvc) GetIndexStats(_ context.Context, _ *GetIndexStatsRequest) (*GetIndexStatsResponse, error) {
	return &GetIndexStatsResponse{}, nil
}
func (m *ragMockVecSvc) CompactNamespace(_ context.Context, _ *CompactNamespaceRequest) (*CompactNamespaceResponse, error) {
	return &CompactNamespaceResponse{}, nil
}
func (m *ragMockVecSvc) GetCompactionProgress(_ context.Context, _ *GetCompactionProgressRequest) (*GetCompactionProgressResponse, error) {
	return &GetCompactionProgressResponse{}, nil
}

// ragMockKVSvc is an in-memory KVServicer for RAG service tests.
type ragMockKVSvc struct {
	kvStore map[string][]byte
	scanFn  func(ctx context.Context, prefix string) (map[string][]byte, error)
}

var _ KVServicer = (*ragMockKVSvc)(nil)

func newRAGMockKV() *ragMockKVSvc {
	return &ragMockKVSvc{kvStore: make(map[string][]byte)}
}

func (m *ragMockKVSvc) Put(_ context.Context, key string, val []byte) error {
	m.kvStore[key] = val
	return nil
}

// PutIfAbsent stores only when the key is absent, matching the real engines.
func (m *ragMockKVSvc) PutIfAbsent(_ context.Context, key string, val []byte) (bool, error) {
	if _, exists := m.kvStore[key]; exists {
		return false, nil
	}
	m.kvStore[key] = val
	return true, nil
}

// CompareAndDelete mirrors PutIfAbsent's guard on the release side:
// it removes the key only when the stored value still equals expected.
func (m *ragMockKVSvc) CompareAndDelete(_ context.Context, key string, expected []byte) (bool, error) {
	current, ok := m.kvStore[key]
	if !ok {
		return false, nil
	}
	if !bytes.Equal(current, expected) {
		return false, nil
	}
	delete(m.kvStore, key)
	return true, nil
}

func (m *ragMockKVSvc) Get(_ context.Context, key string) ([]byte, error) {
	v, ok := m.kvStore[key]
	if !ok {
		return nil, &KeyNotFoundError{Key: key}
	}
	return v, nil
}

func (m *ragMockKVSvc) Delete(_ context.Context, key string) error {
	delete(m.kvStore, key)
	return nil
}

func (m *ragMockKVSvc) List(_ context.Context, _ string) ([]string, error) { return nil, nil }

func (m *ragMockKVSvc) Scan(ctx context.Context, prefix string) (map[string][]byte, error) {
	if m.scanFn != nil {
		return m.scanFn(ctx, prefix)
	}
	result := make(map[string][]byte)
	for k, v := range m.kvStore {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			result[k] = v
		}
	}
	return result, nil
}

func (m *ragMockKVSvc) GetStale(_ context.Context, _ string) ([]byte, error)    { return nil, nil }
func (m *ragMockKVSvc) ListStale(_ context.Context, _ string) ([]string, error) { return nil, nil }
func (m *ragMockKVSvc) ScanStale(_ context.Context, _ string) (map[string][]byte, error) {
	return nil, nil
}
func (m *ragMockKVSvc) PutWithMode(_ context.Context, _ string, _ []byte, _ writemode.Mode) error {
	return nil
}
func (m *ragMockKVSvc) DeleteWithMode(_ context.Context, _ string, _ writemode.Mode) error {
	return nil
}
func (m *ragMockKVSvc) GetWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) ([]byte, error) {
	return nil, nil
}
func (m *ragMockKVSvc) ListWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) ([]string, error) {
	return nil, nil
}
func (m *ragMockKVSvc) ScanWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) (map[string][]byte, error) {
	return nil, nil
}
func (m *ragMockKVSvc) ExistsWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) (bool, error) {
	return false, nil
}
func (m *ragMockKVSvc) PutEntity(_ context.Context, _ *PutEntityRequest) (*PutEntityResponse, error) {
	return &PutEntityResponse{}, nil
}
func (m *ragMockKVSvc) GetEntity(_ context.Context, _ *GetEntityRequest) (*GetEntityResponse, error) {
	return &GetEntityResponse{}, nil
}
func (m *ragMockKVSvc) DeleteEntity(_ context.Context, _ *DeleteEntityRequest) (*DeleteEntityResponse, error) {
	return &DeleteEntityResponse{}, nil
}
func (m *ragMockKVSvc) ListEntities(_ context.Context, _ *ListEntitiesRequest) (*ListEntitiesResponse, error) {
	return &ListEntitiesResponse{}, nil
}

// ---------------------------------------------------------------------------
// Mock RAGIngestor
// ---------------------------------------------------------------------------

type ragMockIngestor struct {
	ingestFn func(ctx context.Context, req *IngestDocumentRequest) (*RAGIngestResult, error)
}

var _ RAGIngestor = (*ragMockIngestor)(nil)

func (m *ragMockIngestor) Ingest(ctx context.Context, req *IngestDocumentRequest) (*RAGIngestResult, error) {
	if m.ingestFn != nil {
		return m.ingestFn(ctx, req)
	}
	return &RAGIngestResult{
		DocumentID: req.DocumentID,
		ChunkCount: 3,
		Namespace:  req.Namespace,
	}, nil
}

// ---------------------------------------------------------------------------
// Mock RAGRetriever
// ---------------------------------------------------------------------------

type ragMockRetriever struct {
	retrieveFn func(ctx context.Context, req *RAGQueryRequest) (*RAGRetrieveResult, error)
}

var _ RAGRetriever = (*ragMockRetriever)(nil)

func (m *ragMockRetriever) Retrieve(ctx context.Context, req *RAGQueryRequest) (*RAGRetrieveResult, error) {
	if m.retrieveFn != nil {
		return m.retrieveFn(ctx, req)
	}
	return &RAGRetrieveResult{}, nil
}

// ---------------------------------------------------------------------------
// Helper to build a RAGServicer backed by mocks.
// ---------------------------------------------------------------------------

func newTestRAGService(ingestErr, retrieveErr error) (RAGServicer, *ragMockKVSvc) {
	kvSvc := newRAGMockKV()

	ingestor := &ragMockIngestor{}
	if ingestErr != nil {
		ingestor.ingestFn = func(_ context.Context, _ *IngestDocumentRequest) (*RAGIngestResult, error) {
			return nil, ingestErr
		}
	}

	retriever := &ragMockRetriever{}
	if retrieveErr != nil {
		retriever.retrieveFn = func(_ context.Context, _ *RAGQueryRequest) (*RAGRetrieveResult, error) {
			return nil, retrieveErr
		}
	}

	svc := NewRAGService(ingestor, retriever, newBoundRAGStore(kvSvc))
	return svc, kvSvc
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestRAGService_IngestDocument_Success(t *testing.T) {
	svc, _ := newTestRAGService(nil, nil)
	resp, err := svc.IngestDocument(context.Background(), &IngestDocumentRequest{
		DocumentID: "doc1",
		Text:       "The quick brown fox jumps over the lazy dog.",
		Namespace:  "test-ns",
		ChunkConfig: &ChunkConfig{
			Strategy:  "fixed",
			ChunkSize: 20,
			Overlap:   0,
			MinSize:   0,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "doc1", resp.DocumentID)
	assert.Equal(t, "test-ns", resp.Namespace)
	assert.GreaterOrEqual(t, resp.ChunkCount, 1)
}

func TestRAGService_IngestDocument_EmbeddingError(t *testing.T) {
	svc, _ := newTestRAGService(errors.New("embed down"), nil)
	_, err := svc.IngestDocument(context.Background(), &IngestDocumentRequest{
		DocumentID: "doc1",
		Text:       "Some text.",
		Namespace:  "ns",
	})
	require.Error(t, err)
	var ragErr *RAGIngestError
	assert.True(t, errors.As(err, &ragErr))
}

func TestRAGService_Query_Success(t *testing.T) {
	kvSvc := newRAGMockKV()
	// Pre-populate the KV store with a chunk.
	kvSvc.kvStore["rag:ns:doc1:0"] = []byte("Brown fox text")

	retriever := &ragMockRetriever{
		retrieveFn: func(_ context.Context, _ *RAGQueryRequest) (*RAGRetrieveResult, error) {
			return &RAGRetrieveResult{
				Results: []*RAGRetrievalResult{
					{
						ChunkText:  "Brown fox text",
						Score:      0.05,
						DocumentID: "doc1",
						ChunkIndex: 0,
					},
				},
			}, nil
		},
	}

	querySvc := NewRAGService(&ragMockIngestor{}, retriever, newBoundRAGStore(kvSvc))

	resp, err := querySvc.Query(context.Background(), &RAGQueryRequest{
		Query:     "fox",
		Namespace: "ns",
		TopK:      3,
	})
	require.NoError(t, err)
	require.Len(t, resp.Results, 1)
	assert.Equal(t, "Brown fox text", resp.Results[0].ChunkText)
}

func TestRAGService_Query_EmptyQuery(t *testing.T) {
	retriever := &ragMockRetriever{
		retrieveFn: func(_ context.Context, _ *RAGQueryRequest) (*RAGRetrieveResult, error) {
			return nil, errors.New("empty query")
		},
	}
	svc := NewRAGService(&ragMockIngestor{}, retriever, newBoundRAGStore(newRAGMockKV()))
	_, err := svc.Query(context.Background(), &RAGQueryRequest{
		Query:     "",
		Namespace: "ns",
	})
	require.Error(t, err)
	var ragErr *RAGQueryError
	assert.True(t, errors.As(err, &ragErr))
}

func TestRAGService_DeleteDocument_Success(t *testing.T) {
	svc, kv := newTestRAGService(nil, nil)

	// Pre-store document metadata and chunk.
	docInfo := ragDocumentMeta{ID: "doc1", Namespace: "ns", ChunkCount: 2}
	raw, _ := json.Marshal(docInfo)
	kv.kvStore["rag:ns:doc1:meta"] = raw
	kv.kvStore["rag:ns:doc1:0"] = []byte("chunk0")
	kv.kvStore["rag:ns:doc1:1"] = []byte("chunk1")

	resp, err := svc.DeleteDocument(context.Background(), &DeleteRAGDocumentRequest{
		DocumentID: "doc1",
		Namespace:  "ns",
	})
	require.NoError(t, err)
	assert.Equal(t, "doc1", resp.DocumentID)
	assert.Equal(t, 2, resp.ChunksRemoved)
	assert.NotContains(t, kv.kvStore, "rag:ns:doc1:meta")
}

func TestRAGService_DeleteDocument_NotFound(t *testing.T) {
	svc, _ := newTestRAGService(nil, nil)
	_, err := svc.DeleteDocument(context.Background(), &DeleteRAGDocumentRequest{
		DocumentID: "missing",
		Namespace:  "ns",
	})
	require.Error(t, err)
	assert.IsType(t, &RAGDocumentNotFoundError{}, err)
}

func TestRAGService_ListDocuments_Success(t *testing.T) {
	svc, kv := newTestRAGService(nil, nil)

	doc1 := ragDocumentMeta{ID: "doc1", Namespace: "ns", ChunkCount: 3}
	doc2 := ragDocumentMeta{ID: "doc2", Namespace: "ns", ChunkCount: 1}
	raw1, _ := json.Marshal(doc1)
	raw2, _ := json.Marshal(doc2)
	kv.kvStore["rag:ns:doc1:meta"] = raw1
	kv.kvStore["rag:ns:doc2:meta"] = raw2

	resp, err := svc.ListDocuments(context.Background(), &ListRAGDocumentsRequest{
		Namespace: "ns",
	})
	require.NoError(t, err)
	assert.Equal(t, 2, resp.Total)
	assert.Len(t, resp.Documents, 2)
}

func TestRAGService_ListDocuments_Pagination(t *testing.T) {
	svc, kv := newTestRAGService(nil, nil)

	for _, id := range []string{"d1", "d2", "d3", "d4", "d5"} {
		info := ragDocumentMeta{ID: id, Namespace: "ns", ChunkCount: 1}
		raw, _ := json.Marshal(info)
		kv.kvStore["rag:ns:"+id+":meta"] = raw
	}

	resp, err := svc.ListDocuments(context.Background(), &ListRAGDocumentsRequest{
		Namespace: "ns",
		Limit:     2,
		Offset:    1,
	})
	require.NoError(t, err)
	assert.Equal(t, 5, resp.Total)
	assert.Len(t, resp.Documents, 2)
}

func TestRAGService_ClosedService(t *testing.T) {
	svc, _ := newTestRAGService(nil, nil)
	require.NoError(t, svc.Close())

	_, err := svc.IngestDocument(context.Background(), &IngestDocumentRequest{DocumentID: "x", Text: "y", Namespace: "z"})
	assert.IsType(t, &RAGServiceClosedError{}, err)

	_, err = svc.Query(context.Background(), &RAGQueryRequest{Query: "q", Namespace: "z"})
	assert.IsType(t, &RAGServiceClosedError{}, err)

	_, err = svc.DeleteDocument(context.Background(), &DeleteRAGDocumentRequest{DocumentID: "x", Namespace: "z"})
	assert.IsType(t, &RAGServiceClosedError{}, err)

	_, err = svc.ListDocuments(context.Background(), &ListRAGDocumentsRequest{Namespace: "z"})
	assert.IsType(t, &RAGServiceClosedError{}, err)
}

// CompareAndSwap satisfies the guarded-swap half of the interface (go-qrdb#56).
func (*ragMockKVSvc) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return false, nil
}

// ListAll mirrors List on this double: it applies no internal-key guard,
// so the filtered and unfiltered enumerations are the same call.
func (m *ragMockKVSvc) ListAll(a0 context.Context, a1 string) ([]string, error) {
	return m.List(a0, a1)
}
