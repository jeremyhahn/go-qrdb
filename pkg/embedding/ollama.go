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

package embedding

import (
	"context"
	"sync"
	"time"
)

// Default constants for the Ollama provider.
const (
	// DefaultOllamaBaseURL is the standard Ollama server address.
	DefaultOllamaBaseURL = "http://localhost:11434"

	// DefaultOllamaModel is the default embedding model for Ollama.
	DefaultOllamaModel = "nomic-embed-text"

	// DefaultOllamaTimeout is the per-request HTTP timeout for Ollama requests.
	DefaultOllamaTimeout = 120 * time.Second
)

// ollamaModelDimensions maps well-known Ollama embedding model names to their
// vector dimensionality. Models not listed here have their dimensions
// auto-detected from the first successful Embed call.
var ollamaModelDimensions = map[string]int{
	"nomic-embed-text":       768,
	"mxbai-embed-large":      1024,
	"all-minilm":             384,
	"all-minilm-l6-v2":       384,
	"all-minilm-l12-v2":      384,
	"bge-small-en-v1.5":      384,
	"bge-base-en-v1.5":       768,
	"bge-large-en-v1.5":      1024,
	"bge-m3":                 1024,
	"snowflake-arctic-embed": 1024,
	"e5-small-v2":            384,
	"e5-base-v2":             768,
	"e5-large-v2":            1024,
}

// OllamaConfig holds configuration for the Ollama embedding provider.
type OllamaConfig struct {
	// BaseURL is the Ollama server endpoint.
	// Defaults to http://localhost:11434.
	BaseURL string

	// Model is the embedding model name.
	// Defaults to "nomic-embed-text".
	Model string

	// Dimensions is the expected embedding vector size.
	// If zero, the provider auto-detects from known models or the first response.
	Dimensions int

	// Timeout is the per-request HTTP timeout.
	// Defaults to DefaultOllamaTimeout.
	Timeout time.Duration

	// MaxRetries is the maximum number of retry attempts on transient failures.
	MaxRetries int

	// RetryBackoff is the base backoff duration between retries.
	RetryBackoff time.Duration
}

// DefaultOllamaConfig returns an OllamaConfig populated with sensible defaults.
func DefaultOllamaConfig() OllamaConfig {
	return OllamaConfig{
		BaseURL:      DefaultOllamaBaseURL,
		Model:        DefaultOllamaModel,
		Timeout:      DefaultOllamaTimeout,
		MaxRetries:   3,
		RetryBackoff: time.Second,
	}
}

// OllamaProvider implements Provider using the Ollama /v1/embeddings endpoint.
// Ollama exposes an OpenAI-compatible API since v0.1.24, so this provider
// delegates to the shared Client and adds Ollama-specific defaults and
// dimension tracking.
//
// Compile-time assertion that OllamaProvider satisfies Provider.
var _ Provider = (*OllamaProvider)(nil)

// OllamaProvider wraps Client with Ollama-specific configuration and
// dimension tracking.
type OllamaProvider struct {
	client     *Client
	model      string
	dimensions int
	mu         sync.RWMutex
}

// NewOllamaProvider creates a new OllamaProvider from the given OllamaConfig.
func NewOllamaProvider(config OllamaConfig) (*OllamaProvider, error) {
	if config.BaseURL == "" {
		config.BaseURL = DefaultOllamaBaseURL
	}
	if config.Model == "" {
		config.Model = DefaultOllamaModel
	}
	if config.Timeout <= 0 {
		config.Timeout = DefaultOllamaTimeout
	}
	if config.MaxRetries <= 0 {
		config.MaxRetries = 3
	}
	if config.RetryBackoff <= 0 {
		config.RetryBackoff = time.Second
	}

	// Ollama exposes /v1/embeddings; the base URL for Client is the /v1 prefix.
	clientCfg := &ProviderConfig{
		Name:       "ollama",
		BaseURL:    config.BaseURL + "/v1",
		Model:      config.Model,
		Timeout:    config.Timeout,
		MaxRetries: config.MaxRetries,
	}

	client, err := NewClient(clientCfg)
	if err != nil {
		return nil, err
	}

	// Pre-seed dimensions from known models; fall back to auto-detect.
	dimensions := config.Dimensions
	if dimensions == 0 {
		if d, ok := ollamaModelDimensions[config.Model]; ok {
			dimensions = d
		}
	}

	return &OllamaProvider{
		client:     client,
		model:      config.Model,
		dimensions: dimensions,
	}, nil
}

// Embed generates embeddings for the given texts using the configured model.
// If model is empty it falls back to the OllamaConfig.Model.
func (p *OllamaProvider) Embed(ctx context.Context, texts []string, model string) (*EmbeddingResult, error) {
	if model == "" {
		model = p.model
	}

	result, err := p.client.Embed(ctx, texts, model)
	if err != nil {
		return nil, err
	}

	// Auto-detect dimensions on first successful response.
	if len(result.Embeddings) > 0 && len(result.Embeddings[0]) > 0 {
		p.mu.Lock()
		if p.dimensions == 0 {
			p.dimensions = len(result.Embeddings[0])
		}
		p.mu.Unlock()
	}

	return result, nil
}

// Dimensions returns the embedding vector dimensionality.
func (p *OllamaProvider) Dimensions() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.dimensions
}

// Name returns the provider identifier.
func (p *OllamaProvider) Name() string {
	return "ollama"
}

// Models returns the list of models available from the Ollama instance.
func (p *OllamaProvider) Models(ctx context.Context) ([]ModelInfo, error) {
	return p.client.Models(ctx)
}

// Close releases resources held by the underlying HTTP client.
func (p *OllamaProvider) Close() error {
	return p.client.Close()
}
