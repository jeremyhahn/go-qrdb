//go:build qrdb_vector

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

package sdk

import (
	"github.com/jeremyhahn/go-qrdb/pkg/embedding"
)

// ---------------------------------------------------------------------------
// Embedding provider interface and core types
// ---------------------------------------------------------------------------

// EmbeddingProvider generates vector embeddings from text.
// Any service that exposes an embeddings API (OpenAI-compatible, Voyage AI,
// Ollama, LMStudio, etc.) can be wrapped by an EmbeddingProvider implementation.
type EmbeddingProvider = embedding.Provider

// EmbeddingResult holds the embeddings returned by a provider for a batch of texts.
type EmbeddingResult = embedding.EmbeddingResult

// EmbeddingUsageInfo reports token usage for an embedding request.
type EmbeddingUsageInfo = embedding.UsageInfo

// EmbeddingModelInfo describes an embedding model available from a provider.
type EmbeddingModelInfo = embedding.ModelInfo

// EmbeddingProviderConfig holds configuration for a low-level embedding provider client.
type EmbeddingProviderConfig = embedding.ProviderConfig

// DefaultEmbeddingProviderConfig returns a ProviderConfig populated with sensible defaults.
var DefaultEmbeddingProviderConfig = embedding.DefaultProviderConfig

// ---------------------------------------------------------------------------
// Manager
// ---------------------------------------------------------------------------

// EmbeddingManagerInterface defines the contract that EmbeddingManager satisfies.
// Callers that need to substitute a mock should depend on this interface.
type EmbeddingManagerInterface = embedding.ManagerInterface

// EmbeddingManager manages multiple embedding providers with LRU caching.
type EmbeddingManager = embedding.Manager

// NewEmbeddingManager creates a new EmbeddingManager from the given EmbeddingConfig.
var NewEmbeddingManager = embedding.NewManager

// ---------------------------------------------------------------------------
// Manager configuration and statistics
// ---------------------------------------------------------------------------

// EmbeddingConfig holds the full configuration for the embedding subsystem,
// including provider selection, model settings, caching, and retry behaviour.
type EmbeddingConfig = embedding.Config

// DefaultEmbeddingConfig returns an EmbeddingConfig populated with sensible defaults.
var DefaultEmbeddingConfig = embedding.DefaultConfig

// EmbeddingManagerStats holds a point-in-time snapshot of EmbeddingManager
// activity counters.
type EmbeddingManagerStats = embedding.ManagerStats

// ---------------------------------------------------------------------------
// OpenAI provider
// ---------------------------------------------------------------------------

// OpenAIEmbeddingProvider implements EmbeddingProvider for OpenAI-compatible APIs.
type OpenAIEmbeddingProvider = embedding.OpenAIProvider

// NewOpenAIEmbeddingProvider creates a new OpenAI embedding provider.
// config.Provider must be "openai" and config.APIKey must be non-empty.
var NewOpenAIEmbeddingProvider = embedding.NewOpenAIProvider

// OpenAI embedding model name constants.
const (
	EmbeddingModelTextEmbedding3Small = "text-embedding-3-small"
	EmbeddingModelTextEmbedding3Large = "text-embedding-3-large"
	EmbeddingModelTextEmbeddingAda002 = "text-embedding-ada-002"
)

// ---------------------------------------------------------------------------
// Ollama provider
// ---------------------------------------------------------------------------

// OllamaEmbeddingConfig holds configuration for the Ollama embedding provider.
type OllamaEmbeddingConfig = embedding.OllamaConfig

// DefaultOllamaEmbeddingConfig returns the default Ollama embedding configuration.
var DefaultOllamaEmbeddingConfig = embedding.DefaultOllamaConfig

// OllamaEmbeddingProvider implements EmbeddingProvider for a local Ollama instance.
type OllamaEmbeddingProvider = embedding.OllamaProvider

// NewOllamaEmbeddingProvider creates a new Ollama embedding provider.
var NewOllamaEmbeddingProvider = embedding.NewOllamaProvider

// Ollama embedding default constants.
const (
	DefaultOllamaEmbeddingBaseURL = embedding.DefaultOllamaBaseURL
	DefaultOllamaEmbeddingModel   = embedding.DefaultOllamaModel
)

// ---------------------------------------------------------------------------
// LMStudio provider
// ---------------------------------------------------------------------------

// LMStudioEmbeddingConfig holds configuration for the LMStudio embedding provider.
type LMStudioEmbeddingConfig = embedding.LMStudioConfig

// DefaultLMStudioEmbeddingConfig returns the default LMStudio embedding configuration.
var DefaultLMStudioEmbeddingConfig = embedding.DefaultLMStudioConfig

// LMStudioEmbeddingProvider implements EmbeddingProvider for a local LMStudio instance.
type LMStudioEmbeddingProvider = embedding.LMStudioProvider

// NewLMStudioEmbeddingProvider creates a new LMStudio embedding provider.
var NewLMStudioEmbeddingProvider = embedding.NewLMStudioProvider

// LMStudio embedding default constants.
const (
	DefaultLMStudioEmbeddingBaseURL = embedding.DefaultLMStudioBaseURL
	DefaultLMStudioEmbeddingModel   = embedding.DefaultLMStudioModel
)

// ---------------------------------------------------------------------------
// Voyage AI provider
// ---------------------------------------------------------------------------

// VoyageEmbeddingProvider implements EmbeddingProvider for the Voyage AI API.
type VoyageEmbeddingProvider = embedding.VoyageProvider

// NewVoyageEmbeddingProvider creates a new Voyage AI embedding provider.
// config.APIKey must be non-empty.
var NewVoyageEmbeddingProvider = embedding.NewVoyageProvider

// Voyage AI embedding default constants.
const (
	DefaultVoyageEmbeddingAPIURL = embedding.DefaultVoyageAPIURL
	DefaultVoyageEmbeddingModel  = embedding.DefaultVoyageModel
)

// ---------------------------------------------------------------------------
// Sentinel errors
// ---------------------------------------------------------------------------

// Embedding configuration validation sentinel errors.
var (
	ErrEmbeddingProviderRequired    = embedding.ErrProviderRequired
	ErrEmbeddingProviderInvalid     = embedding.ErrProviderInvalid
	ErrEmbeddingAPIURLRequired      = embedding.ErrAPIURLRequired
	ErrEmbeddingAPIKeyRequired      = embedding.ErrAPIKeyRequired
	ErrEmbeddingModelRequired       = embedding.ErrModelRequired
	ErrEmbeddingDimensionsPositive  = embedding.ErrDimensionsPositive
	ErrEmbeddingMaxBatchSizePositive = embedding.ErrMaxBatchSizePositive
	ErrEmbeddingTimeoutPositive     = embedding.ErrTimeoutPositive
)

// ---------------------------------------------------------------------------
// Error types
// ---------------------------------------------------------------------------

// EmbeddingInvalidInputError is returned when the caller passes empty input.
type EmbeddingInvalidInputError = embedding.InvalidInputError

// EmbeddingEmptyResponseError is returned when the provider returns no embeddings.
type EmbeddingEmptyResponseError = embedding.EmptyResponseError

// EmbeddingRetryExhaustedError is returned when all retry attempts are exhausted.
type EmbeddingRetryExhaustedError = embedding.RetryExhaustedError

// EmbeddingProviderUnavailableError is returned when the provider is unreachable.
type EmbeddingProviderUnavailableError = embedding.ProviderUnavailableError

// EmbeddingHTTPError is returned when the provider responds with a non-2xx status.
type EmbeddingHTTPError = embedding.HTTPError

// EmbeddingDimensionMismatchError is returned when dimensions do not match expectations.
type EmbeddingDimensionMismatchError = embedding.DimensionMismatchError

// EmbeddingProviderNotFoundError is returned when a named provider is not registered.
type EmbeddingProviderNotFoundError = embedding.ProviderNotFoundError

// EmbeddingProviderAlreadyRegisteredError is returned when a provider name is already in use.
type EmbeddingProviderAlreadyRegisteredError = embedding.ProviderAlreadyRegisteredError

// EmbeddingModelNotFoundError is returned when a provider does not recognise a model name.
type EmbeddingModelNotFoundError = embedding.ModelNotFoundError

// EmbeddingCacheCapacityError is returned when an invalid cache capacity is provided.
type EmbeddingCacheCapacityError = embedding.CacheCapacityError

// EmbeddingCloseError is returned when errors occur during provider cleanup.
type EmbeddingCloseError = embedding.CloseError
