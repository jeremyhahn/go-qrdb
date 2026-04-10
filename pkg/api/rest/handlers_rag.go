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

package rest

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// RAG request / response types
// ---------------------------------------------------------------------------

// ragIngestRequest is the JSON payload for POST /v1/rag/ingest.
type ragIngestRequest struct {
	DocumentID    string            `json:"document_id"`
	Text          string            `json:"text"`
	Namespace     string            `json:"namespace"`
	Model         string            `json:"model,omitempty"`
	Provider      string            `json:"provider,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	ChunkStrategy string            `json:"chunk_strategy,omitempty"`
	ChunkSize     int               `json:"chunk_size,omitempty"`
	ChunkOverlap  int               `json:"chunk_overlap,omitempty"`
	ChunkMinSize  int               `json:"chunk_min_size,omitempty"`
}

// ragIngestResponse is the JSON payload returned by POST /v1/rag/ingest.
type ragIngestResponse struct {
	DocumentID string `json:"document_id"`
	ChunkCount int    `json:"chunk_count"`
	Namespace  string `json:"namespace"`
}

// ragQueryRequest is the JSON payload for POST /v1/rag/query.
type ragQueryRequest struct {
	Query     string            `json:"query"`
	Namespace string            `json:"namespace"`
	TopK      int               `json:"top_k,omitempty"`
	Model     string            `json:"model,omitempty"`
	Provider  string            `json:"provider,omitempty"`
	Filter    map[string]string `json:"filter,omitempty"`
}

// ragQueryResultJSON is the JSON representation of a single retrieval result.
type ragQueryResultJSON struct {
	ChunkText  string            `json:"chunk_text"`
	Score      float32           `json:"score"`
	DocumentID string            `json:"document_id"`
	ChunkIndex int               `json:"chunk_index"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// ragQueryResponse is the JSON payload returned by POST /v1/rag/query.
type ragQueryResponse struct {
	Results []ragQueryResultJSON `json:"results"`
}

// ragDeleteDocumentResponse is the JSON payload returned by
// DELETE /v1/rag/documents/{id}.
type ragDeleteDocumentResponse struct {
	DocumentID    string `json:"document_id"`
	ChunksRemoved int    `json:"chunks_removed"`
}

// ragDocumentInfoJSON is the JSON representation of an ingested document.
type ragDocumentInfoJSON struct {
	ID         string            `json:"id"`
	Namespace  string            `json:"namespace"`
	ChunkCount int               `json:"chunk_count"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// ragListDocumentsResponse is the JSON payload returned by
// GET /v1/rag/documents.
type ragListDocumentsResponse struct {
	Documents []ragDocumentInfoJSON `json:"documents"`
	Total     int                   `json:"total"`
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// handleRAGIngest handles POST /v1/rag/ingest — chunks, embeds, and stores
// a document in the RAG pipeline.
func (s *Server) handleRAGIngest(w http.ResponseWriter, r *http.Request) {
	if s.ragSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "rag"})
		return
	}

	var req ragIngestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	// Build chunk config from request fields, applying defaults when zero.
	var chunkCfg *service.ChunkConfig
	if req.ChunkStrategy != "" || req.ChunkSize > 0 {
		chunkCfg = service.DefaultChunkConfig()
		if req.ChunkStrategy != "" {
			chunkCfg.Strategy = req.ChunkStrategy
		}
		if req.ChunkSize > 0 {
			chunkCfg.ChunkSize = req.ChunkSize
		}
		if req.ChunkOverlap > 0 {
			chunkCfg.Overlap = req.ChunkOverlap
		}
		if req.ChunkMinSize > 0 {
			chunkCfg.MinSize = req.ChunkMinSize
		}
	}

	resp, err := s.ragSvc.IngestDocument(r.Context(), &service.IngestDocumentRequest{
		DocumentID:  req.DocumentID,
		Text:        req.Text,
		Namespace:   req.Namespace,
		Model:       req.Model,
		Provider:    req.Provider,
		Metadata:    req.Metadata,
		ChunkConfig: chunkCfg,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, ragIngestResponse{
		DocumentID: resp.DocumentID,
		ChunkCount: resp.ChunkCount,
		Namespace:  resp.Namespace,
	})
}

// handleRAGQuery handles POST /v1/rag/query — embeds the query and retrieves
// the most semantically similar passages.
func (s *Server) handleRAGQuery(w http.ResponseWriter, r *http.Request) {
	if s.ragSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "rag"})
		return
	}

	var req ragQueryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	resp, err := s.ragSvc.Query(r.Context(), &service.RAGQueryRequest{
		Query:     req.Query,
		Namespace: req.Namespace,
		TopK:      req.TopK,
		Model:     req.Model,
		Provider:  req.Provider,
		Filter:    req.Filter,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	results := make([]ragQueryResultJSON, len(resp.Results))
	for i, res := range resp.Results {
		results[i] = ragQueryResultJSON{
			ChunkText:  res.ChunkText,
			Score:      res.Score,
			DocumentID: res.DocumentID,
			ChunkIndex: res.ChunkIndex,
			Metadata:   res.Metadata,
		}
	}
	writeJSON(w, ragQueryResponse{Results: results})
}

// handleRAGDeleteDocument handles DELETE /v1/rag/documents/{id} — removes a
// document and all its chunks from the RAG pipeline.
func (s *Server) handleRAGDeleteDocument(w http.ResponseWriter, r *http.Request) {
	if s.ragSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "rag"})
		return
	}

	// Extract document ID from path: /v1/rag/documents/{id}
	docID := r.PathValue("id")
	if docID == "" {
		// Fallback: query param.
		docID = r.URL.Query().Get("id")
	}
	namespace := r.URL.Query().Get("namespace")

	resp, err := s.ragSvc.DeleteDocument(r.Context(), &service.DeleteRAGDocumentRequest{
		DocumentID: docID,
		Namespace:  namespace,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, ragDeleteDocumentResponse{
		DocumentID:    resp.DocumentID,
		ChunksRemoved: resp.ChunksRemoved,
	})
}

// handleRAGListDocuments handles GET /v1/rag/documents — returns paginated
// metadata about ingested documents in a namespace.
func (s *Server) handleRAGListDocuments(w http.ResponseWriter, r *http.Request) {
	if s.ragSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "rag"})
		return
	}

	q := r.URL.Query()
	namespace := q.Get("namespace")
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))

	resp, err := s.ragSvc.ListDocuments(r.Context(), &service.ListRAGDocumentsRequest{
		Namespace: namespace,
		Limit:     limit,
		Offset:    offset,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	docs := make([]ragDocumentInfoJSON, len(resp.Documents))
	for i, doc := range resp.Documents {
		docs[i] = ragDocumentInfoJSON{
			ID:         doc.ID,
			Namespace:  doc.Namespace,
			ChunkCount: doc.ChunkCount,
			Metadata:   doc.Metadata,
		}
	}
	writeJSON(w, ragListDocumentsResponse{
		Documents: docs,
		Total:     resp.Total,
	})
}
