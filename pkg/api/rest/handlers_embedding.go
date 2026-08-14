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

package rest

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Embedding request/response types
// ---------------------------------------------------------------------------

// embedTextRequest is the JSON payload for POST /v1/embeddings.
type embedTextRequest struct {
	Texts    []string `json:"texts"`
	Model    string   `json:"model,omitempty"`
	Provider string   `json:"provider,omitempty"`
}

// embedTextResponse is the JSON payload returned by POST /v1/embeddings.
type embedTextResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
	Model      string      `json:"model"`
	Cached     bool        `json:"cached"`
	Usage      *usageInfo  `json:"usage,omitempty"`
}

// batchEmbedRequest is the JSON payload for POST /v1/embeddings/batch.
type batchEmbedRequest struct {
	Texts     []string `json:"texts"`
	Model     string   `json:"model,omitempty"`
	Provider  string   `json:"provider,omitempty"`
	BatchSize int      `json:"batch_size,omitempty"`
}

// batchEmbedResponse is the JSON payload returned by POST /v1/embeddings/batch.
type batchEmbedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
	Model      string      `json:"model"`
	Usage      *usageInfo  `json:"usage,omitempty"`
}

// usageInfo reports token usage for an embedding request.
type usageInfo struct {
	PromptTokens int `json:"prompt_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// listModelsResponse is the JSON payload returned by GET /v1/embedding/models.
type listModelsResponse struct {
	Models []modelInfo `json:"models"`
}

// modelInfo describes an embedding model available from a provider.
type modelInfo struct {
	Name       string `json:"name"`
	Dimensions int    `json:"dimensions,omitempty"`
	MaxTokens  int    `json:"max_tokens,omitempty"`
}

// ---------------------------------------------------------------------------
// Embedding handlers
// ---------------------------------------------------------------------------

// handleEmbedText handles POST /v1/embeddings. It generates embeddings for
// the provided texts using the configured embedding service, returning one
// vector per input text in the same order.
func (s *Server) handleEmbedText(w http.ResponseWriter, r *http.Request) {
	if s.embeddingSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "embedding"})
		return
	}

	var req embedTextRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	if len(req.Texts) == 0 {
		writeErrorJSON(w, http.StatusBadRequest, &EmbeddingEmptyTextsError{})
		return
	}

	resp, err := s.embeddingSvc.Embed(r.Context(), &service.EmbedRequest{
		Texts:    req.Texts,
		Model:    req.Model,
		Provider: req.Provider,
	})
	if err != nil {
		writeErrorJSON(w, mapEmbeddingHTTPError(err), err)
		return
	}

	out := embedTextResponse{
		Embeddings: resp.Embeddings,
		Model:      resp.Model,
		Cached:     resp.Cached,
	}
	if resp.Usage != nil {
		out.Usage = &usageInfo{
			PromptTokens: resp.Usage.PromptTokens,
			TotalTokens:  resp.Usage.TotalTokens,
		}
	}
	writeJSON(w, out)
}

// handleBatchEmbed handles POST /v1/embeddings/batch. It splits large text
// lists into provider sub-batches and aggregates the results in input order.
func (s *Server) handleBatchEmbed(w http.ResponseWriter, r *http.Request) {
	if s.embeddingSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "embedding"})
		return
	}

	var req batchEmbedRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	if len(req.Texts) == 0 {
		writeErrorJSON(w, http.StatusBadRequest, &EmbeddingEmptyTextsError{})
		return
	}

	resp, err := s.embeddingSvc.BatchEmbed(r.Context(), &service.BatchEmbedRequest{
		Texts:     req.Texts,
		Model:     req.Model,
		Provider:  req.Provider,
		BatchSize: req.BatchSize,
	})
	if err != nil {
		writeErrorJSON(w, mapEmbeddingHTTPError(err), err)
		return
	}

	out := batchEmbedResponse{
		Embeddings: resp.Embeddings,
		Model:      resp.Model,
	}
	if resp.TotalUsage != nil {
		out.Usage = &usageInfo{
			PromptTokens: resp.TotalUsage.PromptTokens,
			TotalTokens:  resp.TotalUsage.TotalTokens,
		}
	}
	writeJSON(w, out)
}

// handleListEmbeddingModels handles GET /v1/embedding/models. It returns the
// list of embedding models available from the default or specified provider.
func (s *Server) handleListEmbeddingModels(w http.ResponseWriter, r *http.Request) {
	if s.embeddingSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "embedding"})
		return
	}

	provider := r.URL.Query().Get("provider")

	resp, err := s.embeddingSvc.ListModels(r.Context(), &service.ListEmbeddingModelsRequest{
		Provider: provider,
	})
	if err != nil {
		writeErrorJSON(w, mapEmbeddingHTTPError(err), err)
		return
	}

	models := make([]modelInfo, len(resp.Models))
	for i, m := range resp.Models {
		models[i] = modelInfo{
			Name:       m.Name,
			Dimensions: m.Dimensions,
			MaxTokens:  m.MaxTokens,
		}
	}
	writeJSON(w, listModelsResponse{Models: models})
}

// mapEmbeddingHTTPError maps embedding service errors to HTTP status codes.
func mapEmbeddingHTTPError(err error) int {
	var (
		providerNotFound     *service.EmbeddingProviderNotFoundError
		providerRegistration *service.EmbeddingProviderRegistrationError
		batchTooLarge        *service.EmbeddingBatchTooLargeError
		dimensionMismatch    *service.EmbeddingDimensionMismatchError
		serviceClosed        *service.EmbeddingServiceClosedError
	)
	// Registration failures wrap their cause (often a not-found), so the
	// wrapper is matched before anything it might wrap.
	switch {
	case errors.As(err, &providerRegistration):
		return http.StatusConflict
	case errors.As(err, &providerNotFound):
		return http.StatusNotFound
	case errors.As(err, &batchTooLarge), errors.As(err, &dimensionMismatch):
		return http.StatusBadRequest
	case errors.As(err, &serviceClosed):
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}
