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

import "context"

// UsageInfo reports token usage for an embedding request.
type UsageInfo struct {
	// PromptTokens is the number of tokens in the input texts.
	PromptTokens int

	// TotalTokens is the total number of tokens consumed by the request.
	TotalTokens int
}

// ModelInfo describes an embedding model available from a provider.
type ModelInfo struct {
	// Name is the model identifier returned by the provider.
	Name string

	// Dimensions is the length of the embedding vector produced by the model.
	// Zero means the dimension count is unknown.
	Dimensions int

	// MaxTokens is the maximum number of tokens the model can embed at once.
	// Zero means the limit is unknown.
	MaxTokens int
}

// EmbeddingResult holds the embeddings returned by a provider for a batch of texts.
type EmbeddingResult struct {
	// Embeddings is the list of embedding vectors, one per input text,
	// in the same order as the input.
	Embeddings [][]float32

	// Model is the model that produced the embeddings.
	Model string

	// Usage contains token consumption information for the request.
	Usage *UsageInfo
}

// EmbeddingProvider generates vector embeddings from text using an
// OpenAI-compatible API. This interface mirrors the capabilities of
// pkg/embedding.Provider but lives in the service layer to avoid a direct
// import of the build-tag-guarded embedding package.
type EmbeddingProvider interface {
	// Embed generates embeddings for the given texts using the specified model.
	Embed(ctx context.Context, texts []string, model string) (*EmbeddingResult, error)

	// Models returns the list of embedding models available from the provider.
	Models(ctx context.Context) ([]ModelInfo, error)

	// DefaultModel returns the model Embed uses when it is called with an
	// empty model. The embedding service keys its cache on it, so a request
	// that names this model and one that names none share one entry.
	DefaultModel() string

	// Close releases any resources held by the provider.
	Close() error
}

// EmbeddingCacher provides an LRU cache for embedding vectors keyed by
// (model, text) pairs. This interface abstracts the concrete embedding.Cache
// type so the service layer does not import the build-tag-guarded package.
type EmbeddingCacher interface {
	// Get returns the cached embedding for (model, text) and true if present,
	// or nil and false if not found.
	Get(model, text string) ([]float32, bool)

	// Put stores an embedding for (model, text), evicting the
	// least-recently-used entry if the cache is at capacity.
	Put(model, text string, embedding []float32)
}

// EmbedRequest is the input to EmbeddingServicer.Embed.
type EmbedRequest struct {
	// Texts is the list of strings to embed. Must not be empty.
	Texts []string

	// Model is the embedding model to use. Leave empty to use the provider's
	// configured default model.
	Model string

	// Provider is the name of the registered provider to use. Leave empty to
	// use the default provider configured on the service.
	Provider string
}

// EmbedResponse contains the results of a single embedding call.
type EmbedResponse struct {
	// Embeddings contains one vector per input text, in the same order as
	// the input slice.
	Embeddings [][]float32

	// Model is the model that produced the embeddings.
	Model string

	// Usage contains token consumption information for the request.
	Usage *UsageInfo

	// Cached is true when all embeddings were served from the local cache.
	Cached bool
}

// BatchEmbedRequest is the input to EmbeddingServicer.BatchEmbed.
// Long text lists are automatically split into smaller provider calls.
type BatchEmbedRequest struct {
	// Texts is the complete list of strings to embed.
	Texts []string

	// Model is the embedding model to use for all batches.
	Model string

	// Provider is the name of the registered provider to use. Leave empty to
	// use the default provider.
	Provider string

	// BatchSize is the maximum number of texts per provider call.
	// Zero means the service selects a reasonable default (256).
	BatchSize int
}

// BatchEmbedResponse contains the aggregated results of a batch embedding run.
type BatchEmbedResponse struct {
	// Embeddings contains one vector per input text, in the same order as
	// the input slice.
	Embeddings [][]float32

	// Model is the model that produced the embeddings.
	Model string

	// TotalUsage is the sum of token usage across all provider calls.
	TotalUsage *UsageInfo
}

// ListEmbeddingModelsRequest is the input to EmbeddingServicer.ListModels.
type ListEmbeddingModelsRequest struct {
	// Provider is the name of the registered provider to query. Leave empty
	// to query the default provider.
	Provider string
}

// ListEmbeddingModelsResponse contains the models available from a provider.
type ListEmbeddingModelsResponse struct {
	// Models is the list of models available from the queried provider.
	Models []ModelInfo
}
