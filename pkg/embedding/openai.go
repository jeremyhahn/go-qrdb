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

// Default constants for OpenAI-compatible providers.
const (
	// DefaultOpenAIBaseURL is the canonical OpenAI API base URL.
	DefaultOpenAIBaseURL = "https://api.openai.com"

	// DefaultOpenAITimeout is the default per-request HTTP timeout.
	DefaultOpenAITimeout = 30 * time.Second
)

// openaiModelDimensions maps well-known OpenAI model names to their
// vector dimensionality.
var openaiModelDimensions = map[string]int{
	"text-embedding-3-small":                  1536,
	"text-embedding-3-large":                  3072,
	"text-embedding-ada-002":                  1536,
	"nomic-embed-text":                        768,
	"mxbai-embed-large":                       1024,
	"all-minilm":                              384,
	"all-minilm-l6-v2":                        384,
	"all-minilm-l12-v2":                       384,
	"bge-small-en-v1.5":                       384,
	"bge-base-en-v1.5":                        768,
	"bge-large-en-v1.5":                       1024,
	"nomic-ai/nomic-embed-text-v1.5-GGUF":     768,
}

// OpenAIProvider implements Provider for OpenAI-compatible embedding APIs.
// It wraps Client with OpenAI-specific defaults and dimension tracking.
// This provider also works with any service that implements the /v1/embeddings
// endpoint (Ollama v1 API, vLLM, etc.).
//
// Compile-time assertion that OpenAIProvider satisfies Provider.
var _ Provider = (*OpenAIProvider)(nil)

// OpenAIProvider wraps Client with OpenAI-specific configuration and
// dimension tracking.
type OpenAIProvider struct {
	client     *Client
	model      string
	dimensions int
	mu         sync.RWMutex
}

// NewOpenAIProvider creates a new OpenAIProvider from the given Config.
// If APIURL is empty it defaults to DefaultOpenAIBaseURL.
func NewOpenAIProvider(config Config) (*OpenAIProvider, error) {
	apiURL := config.APIURL
	if apiURL == "" {
		apiURL = DefaultOpenAIBaseURL
	}

	model := config.Model
	if model == "" {
		model = "text-embedding-3-small"
	}

	timeout := time.Duration(config.Timeout) * time.Second
	if timeout <= 0 {
		timeout = DefaultOpenAITimeout
	}

	maxRetries := config.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 3
	}

	clientCfg := &ProviderConfig{
		Name:       "openai",
		BaseURL:    apiURL + "/v1",
		APIKey:     config.APIKey,
		Model:      model,
		Timeout:    timeout,
		MaxRetries: maxRetries,
	}

	client, err := NewClient(clientCfg)
	if err != nil {
		return nil, err
	}

	dimensions := config.Dimensions
	if dimensions == 0 {
		if d, ok := openaiModelDimensions[model]; ok {
			dimensions = d
		}
	}

	return &OpenAIProvider{
		client:     client,
		model:      model,
		dimensions: dimensions,
	}, nil
}

// Embed generates embeddings for the given texts using the configured model.
func (p *OpenAIProvider) Embed(ctx context.Context, texts []string, model string) (*EmbeddingResult, error) {
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
func (p *OpenAIProvider) Dimensions() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.dimensions
}

// Name returns the provider identifier.
func (p *OpenAIProvider) Name() string {
	return "openai"
}

// Models returns the list of models available from the OpenAI-compatible endpoint.
func (p *OpenAIProvider) Models(ctx context.Context) ([]ModelInfo, error) {
	return p.client.Models(ctx)
}

// Close releases resources held by the underlying HTTP client.
func (p *OpenAIProvider) Close() error {
	return p.client.Close()
}
