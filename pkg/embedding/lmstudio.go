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
	"time"
)

// Default constants for the LMStudio provider.
const (
	// DefaultLMStudioBaseURL is the standard LMStudio server address.
	DefaultLMStudioBaseURL = "http://localhost:1234"

	// DefaultLMStudioModel is the default embedding model for LMStudio.
	DefaultLMStudioModel = "nomic-ai/nomic-embed-text-v1.5-GGUF"

	// DefaultLMStudioTimeout is the default per-request HTTP timeout.
	DefaultLMStudioTimeout = 60 * time.Second
)

// lmstudioModelDimensions maps well-known LMStudio GGUF model names to their
// vector dimensionality.
var lmstudioModelDimensions = map[string]int{
	"nomic-ai/nomic-embed-text-v1.5-GGUF":          768,
	"nomic-embed-text-v1.5-GGUF":                   768,
	"BAAI/bge-small-en-v1.5-GGUF":                  384,
	"BAAI/bge-base-en-v1.5-GGUF":                   768,
	"BAAI/bge-large-en-v1.5-GGUF":                  1024,
	"sentence-transformers/all-MiniLM-L6-v2-GGUF":  384,
	"sentence-transformers/all-MiniLM-L12-v2-GGUF": 384,
	"Xenova/all-MiniLM-L6-v2-onnx":                 384,
	"mixedbread-ai/mxbai-embed-large-v1-GGUF":      1024,
	"intfloat/e5-small-v2-GGUF":                    384,
	"intfloat/e5-base-v2-GGUF":                     768,
	"intfloat/e5-large-v2-GGUF":                    1024,
	"Snowflake/snowflake-arctic-embed-m-v1.5-GGUF": 768,
	"Snowflake/snowflake-arctic-embed-l-v2.0-GGUF": 1024,
}

// LMStudioConfig holds configuration for the LMStudio embedding provider.
// LMStudio exposes an OpenAI-compatible API, so most settings mirror
// OpenAI configuration.
type LMStudioConfig struct {
	// BaseURL is the LMStudio server endpoint.
	// Defaults to http://localhost:1234.
	BaseURL string

	// Model is the embedding model name as loaded in LMStudio.
	// Defaults to "nomic-ai/nomic-embed-text-v1.5-GGUF".
	Model string

	// Dimensions is the expected embedding vector size.
	// If zero, the provider looks up the model in the known-models table.
	Dimensions int

	// Timeout is the per-request HTTP timeout in seconds.
	Timeout int

	// MaxRetries is the maximum number of retry attempts on transient failures.
	MaxRetries int

	// RetryBackoffMS is the base backoff duration in milliseconds.
	RetryBackoffMS int
}

// DefaultLMStudioConfig returns an LMStudioConfig populated with sensible defaults.
func DefaultLMStudioConfig() LMStudioConfig {
	return LMStudioConfig{
		BaseURL:        DefaultLMStudioBaseURL,
		Model:          DefaultLMStudioModel,
		Timeout:        int(DefaultLMStudioTimeout.Seconds()),
		MaxRetries:     3,
		RetryBackoffMS: 1000,
	}
}

// LMStudioProvider implements Provider for LMStudio.
// LMStudio exposes an OpenAI-compatible API, so this is a thin wrapper
// around OpenAIProvider with LMStudio-specific defaults and naming.
//
// Compile-time assertion that LMStudioProvider satisfies Provider.
var _ Provider = (*LMStudioProvider)(nil)

// LMStudioProvider delegates to OpenAIProvider with LMStudio-specific defaults.
type LMStudioProvider struct {
	inner *OpenAIProvider
}

// NewLMStudioProvider creates a new LMStudioProvider from the given LMStudioConfig.
func NewLMStudioProvider(config LMStudioConfig) (*LMStudioProvider, error) {
	if config.BaseURL == "" {
		config.BaseURL = DefaultLMStudioBaseURL
	}
	if config.Model == "" {
		config.Model = DefaultLMStudioModel
	}
	if config.Timeout <= 0 {
		config.Timeout = int(DefaultLMStudioTimeout.Seconds())
	}
	if config.MaxRetries <= 0 {
		config.MaxRetries = 3
	}
	if config.RetryBackoffMS <= 0 {
		config.RetryBackoffMS = 1000
	}

	// Look up dimensions from known models if not explicitly set.
	dimensions := config.Dimensions
	if dimensions == 0 {
		if d, ok := lmstudioModelDimensions[config.Model]; ok {
			dimensions = d
		}
	}

	// LMStudio does not require an API key.
	openaiCfg := Config{
		APIURL:         config.BaseURL,
		APIKey:         "",
		Model:          config.Model,
		Dimensions:     dimensions,
		Timeout:        config.Timeout,
		MaxRetries:     config.MaxRetries,
		RetryBackoffMS: config.RetryBackoffMS,
	}

	inner, err := NewOpenAIProvider(openaiCfg)
	if err != nil {
		return nil, err
	}

	return &LMStudioProvider{inner: inner}, nil
}

// Embed generates embeddings for the given texts using the configured model.
func (p *LMStudioProvider) Embed(ctx context.Context, texts []string, model string) (*EmbeddingResult, error) {
	return p.inner.Embed(ctx, texts, model)
}

// Dimensions returns the embedding vector dimensionality.
func (p *LMStudioProvider) Dimensions() int {
	return p.inner.Dimensions()
}

// Name returns the provider identifier.
func (p *LMStudioProvider) Name() string {
	return "lmstudio"
}

// Models returns the list of models available from the LMStudio instance.
func (p *LMStudioProvider) Models(ctx context.Context) ([]ModelInfo, error) {
	return p.inner.Models(ctx)
}

// Close releases resources held by the underlying HTTP client.
func (p *LMStudioProvider) Close() error {
	return p.inner.Close()
}
