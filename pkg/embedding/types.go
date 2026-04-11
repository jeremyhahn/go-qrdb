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

// Package embedding provides clients, providers, and a multi-provider manager
// for generating vector embeddings from any backend (OpenAI, Ollama, Voyage AI,
// LMStudio, etc.).
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

// Config holds the full configuration for the embedding subsystem, including
// provider selection, model settings, caching, and retry behaviour.
// It is consumed by NewManager to construct and configure a Manager instance.
type Config struct {
	// Provider selects the backend: "openai", "ollama", "lmstudio", or "voyage".
	Provider string `yaml:"provider" json:"provider"`

	// APIURL is the base URL of the embedding API endpoint.
	APIURL string `yaml:"api_url" json:"api_url"`

	// APIKey is the bearer token. Leave empty for providers that do not require
	// authentication (e.g. local Ollama or LMStudio instances).
	APIKey string `yaml:"api_key" json:"api_key"`

	// Model is the default embedding model name.
	Model string `yaml:"model" json:"model"`

	// Dimensions is the expected embedding vector size. Zero means auto-detect.
	Dimensions int `yaml:"dimensions" json:"dimensions"`

	// MaxBatchSize is the maximum number of texts per embedding request.
	MaxBatchSize int `yaml:"max_batch_size" json:"max_batch_size"`

	// Timeout is the per-request HTTP timeout in seconds.
	Timeout int `yaml:"timeout_seconds" json:"timeout_seconds"`

	// CacheEnabled enables the LRU embedding cache.
	CacheEnabled bool `yaml:"cache_enabled" json:"cache_enabled"`

	// CacheSize is the maximum number of entries in the LRU cache.
	CacheSize int `yaml:"cache_size" json:"cache_size"`

	// CacheTTL is the cache entry time-to-live in seconds. Zero disables TTL.
	CacheTTL int `yaml:"cache_ttl_seconds" json:"cache_ttl_seconds"`

	// CacheCompress enables gzip compression of cached embedding vectors.
	CacheCompress bool `yaml:"cache_compress" json:"cache_compress"`

	// MaxRetries is the maximum number of retry attempts on transient failures.
	MaxRetries int `yaml:"max_retries" json:"max_retries"`

	// RetryBackoffMS is the base backoff duration in milliseconds between retries.
	RetryBackoffMS int `yaml:"retry_backoff_ms" json:"retry_backoff_ms"`
}

// DefaultConfig returns a Config populated with sensible defaults that target
// a local Voyage AI-compatible setup. Callers must set Provider and APIURL.
func DefaultConfig() Config {
	return Config{
		Provider:       "voyage",
		Model:          "voyage-code-2",
		Dimensions:     1536,
		MaxBatchSize:   128,
		Timeout:        30,
		CacheEnabled:   true,
		CacheSize:      10000,
		CacheTTL:       3600,
		CacheCompress:  true,
		MaxRetries:     3,
		RetryBackoffMS: 1000,
	}
}

// ManagerStats holds a point-in-time snapshot of Manager activity counters.
type ManagerStats struct {
	// ProvidersRegistered is the number of registered providers.
	ProvidersRegistered int

	// ProviderNames is the list of registered provider names.
	ProviderNames []string

	// DefaultProvider is the name of the currently active provider.
	DefaultProvider string

	// CacheEnabled is true if the embedding cache is active.
	CacheEnabled bool

	// CacheSize is the current number of entries in the cache.
	CacheSize int

	// CacheHits is the cumulative number of cache hits.
	CacheHits uint64

	// CacheMisses is the cumulative number of cache misses.
	CacheMisses uint64

	// CacheHitRate is the ratio of hits to total lookups; zero if no lookups yet.
	CacheHitRate float64
}
