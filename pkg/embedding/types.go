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

// Package embedding provides an OpenAI-compatible HTTP client for generating
// vector embeddings from any provider (Ollama, OpenAI, vLLM, etc.) that
// implements the /v1/embeddings API.
package embedding

import "time"

// ProviderConfig holds configuration for an embedding provider.
type ProviderConfig struct {
	// Name is the human-readable identifier for this provider.
	Name string

	// BaseURL is the base URL of the OpenAI-compatible embeddings API,
	// e.g. "http://localhost:11434/v1" for Ollama.
	BaseURL string

	// APIKey is the bearer token sent in the Authorization header.
	// Leave empty to omit the Authorization header entirely.
	APIKey string

	// Model is the default embedding model to use when none is specified.
	Model string

	// Timeout is the per-request HTTP timeout.
	Timeout time.Duration

	// MaxRetries is the maximum number of retry attempts for transient failures.
	MaxRetries int
}

// DefaultProviderConfig returns a ProviderConfig populated with sensible defaults.
// Callers should override BaseURL and Model before use.
func DefaultProviderConfig() *ProviderConfig {
	return &ProviderConfig{
		Timeout:    30 * time.Second,
		MaxRetries: 3,
	}
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

// UsageInfo reports token usage for an embedding request.
type UsageInfo struct {
	// PromptTokens is the number of tokens in the input texts.
	PromptTokens int

	// TotalTokens is the total number of tokens consumed by the request.
	TotalTokens int
}

// embeddingRequest is the OpenAI-compatible request body for /v1/embeddings.
type embeddingRequest struct {
	Input          []string `json:"input"`
	Model          string   `json:"model"`
	EncodingFormat string   `json:"encoding_format,omitempty"`
}

// embeddingResponse is the OpenAI-compatible response body from /v1/embeddings.
type embeddingResponse struct {
	Object string          `json:"object"`
	Data   []embeddingData `json:"data"`
	Model  string          `json:"model"`
	Usage  usageResponse   `json:"usage"`
}

// embeddingData is a single embedding entry in an embeddingResponse.
type embeddingData struct {
	Object    string    `json:"object"`
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

// usageResponse is the token-usage section of an embeddingResponse.
type usageResponse struct {
	PromptTokens int `json:"prompt_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// modelListResponse is the OpenAI-compatible response body from GET /v1/models.
type modelListResponse struct {
	Object string      `json:"object"`
	Data   []modelData `json:"data"`
}

// modelData is a single model entry in a modelListResponse.
type modelData struct {
	ID     string `json:"id"`
	Object string `json:"object"`
}
