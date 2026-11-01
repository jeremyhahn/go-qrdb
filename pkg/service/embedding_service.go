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

// Package service provides the embedding service interface and its production
// implementation, which sits between the API layer and the embedding providers.
package service

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
)

// defaultBatchSize is the maximum number of texts sent to a provider in a
// single call when no explicit BatchSize is set on the request.
const defaultBatchSize = 256

// EmbeddingServicer generates vector embeddings through registered providers.
// All methods are safe for concurrent use.
type EmbeddingServicer interface {
	// Embed generates embeddings for the texts in req.
	// If req.Provider is empty the default provider is used.
	// Results are served from the in-memory cache when available.
	Embed(ctx context.Context, req *EmbedRequest) (*EmbedResponse, error)

	// BatchEmbed splits req.Texts into sub-batches and calls the provider
	// for each batch, aggregating the results in input order.
	BatchEmbed(ctx context.Context, req *BatchEmbedRequest) (*BatchEmbedResponse, error)

	// ListModels returns the embedding models available from a provider.
	// If req.Provider is empty the default provider is queried.
	ListModels(ctx context.Context, req *ListEmbeddingModelsRequest) (*ListEmbeddingModelsResponse, error)

	// RegisterProvider adds a provider under the given name.
	// It returns EmbeddingProviderRegistrationError if the name is already taken.
	RegisterProvider(name string, provider EmbeddingProvider) error

	// DeregisterProvider removes the provider with the given name.
	// It returns EmbeddingProviderNotFoundError if the name does not exist.
	DeregisterProvider(name string) error

	// Close releases all provider resources. Subsequent calls are no-ops.
	Close() error
}

// EmbeddingServiceOption is a functional option for NewEmbeddingService.
type EmbeddingServiceOption func(*embeddingService)

// WithDefaultProvider sets the name of the provider used when a request does
// not specify one explicitly.
func WithDefaultProvider(name string) EmbeddingServiceOption {
	return func(s *embeddingService) {
		s.defaultProvider = name
	}
}

// WithEmbeddingCache attaches a cache to the service.
// Embeddings retrieved from the provider are stored in the cache and
// subsequent identical requests are served without a network call.
func WithEmbeddingCache(cache EmbeddingCacher) EmbeddingServiceOption {
	return func(s *embeddingService) {
		s.cache = cache
	}
}

// embeddingService is the production implementation of EmbeddingServicer.
type embeddingService struct {
	mu              sync.RWMutex
	providers       map[string]EmbeddingProvider
	defaultProvider string
	cache           EmbeddingCacher
	closed          atomic.Bool
}

// Compile-time assertion that embeddingService implements EmbeddingServicer.
var _ EmbeddingServicer = (*embeddingService)(nil)

// NewEmbeddingService constructs an EmbeddingServicer with the supplied options.
// Providers can be registered either before or after construction via
// RegisterProvider.
func NewEmbeddingService(opts ...EmbeddingServiceOption) EmbeddingServicer {
	s := &embeddingService{
		providers: make(map[string]EmbeddingProvider),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Embed generates embeddings for the texts in req. The cache is checked first;
// texts that are not cached are forwarded to the provider and the results are
// written back to the cache before returning.
func (s *embeddingService) Embed(ctx context.Context, req *EmbedRequest) (*EmbedResponse, error) {
	if s.closed.Load() {
		return nil, &EmbeddingServiceClosedError{}
	}

	provider, err := s.resolveProvider(req.Provider)
	if err != nil {
		return nil, err
	}

	// Resolve the effective model once: the lookup and the store use the
	// same key, so a request naming the default model and one naming none
	// share one cache entry.
	model := req.Model
	if model == "" {
		model = provider.DefaultModel()
	}

	// Fast path: all texts are in the cache.
	if s.cache != nil && len(req.Texts) > 0 {
		if cached, ok := s.tryCache(model, req.Texts); ok {
			return &EmbedResponse{
				Embeddings: cached,
				Model:      model,
				Cached:     true,
			}, nil
		}
	}

	result, err := provider.Embed(ctx, req.Texts, model)
	if err != nil {
		return nil, err
	}

	// Populate the cache with the fresh embeddings.
	if s.cache != nil {
		for i, text := range req.Texts {
			if i < len(result.Embeddings) {
				s.cache.Put(model, text, result.Embeddings[i])
			}
		}
	}

	return &EmbedResponse{
		Embeddings: result.Embeddings,
		Model:      result.Model,
		Usage:      result.Usage,
		Cached:     false,
	}, nil
}

// BatchEmbed splits req.Texts into sub-batches of up to req.BatchSize texts
// (defaulting to defaultBatchSize) and calls the provider for each batch.
// Results are assembled in the same order as the input slice.
func (s *embeddingService) BatchEmbed(ctx context.Context, req *BatchEmbedRequest) (*BatchEmbedResponse, error) {
	if s.closed.Load() {
		return nil, &EmbeddingServiceClosedError{}
	}

	batchSize := req.BatchSize
	if batchSize <= 0 {
		batchSize = defaultBatchSize
	}

	allEmbeddings := make([][]float32, 0, len(req.Texts))
	total := &UsageInfo{}
	var model string

	for start := 0; start < len(req.Texts); start += batchSize {
		end := start + batchSize
		if end > len(req.Texts) {
			end = len(req.Texts)
		}

		resp, err := s.Embed(ctx, &EmbedRequest{
			Texts:    req.Texts[start:end],
			Model:    req.Model,
			Provider: req.Provider,
		})
		if err != nil {
			return nil, err
		}

		allEmbeddings = append(allEmbeddings, resp.Embeddings...)
		if resp.Model != "" {
			model = resp.Model
		}
		if resp.Usage != nil {
			total.PromptTokens += resp.Usage.PromptTokens
			total.TotalTokens += resp.Usage.TotalTokens
		}
	}

	return &BatchEmbedResponse{
		Embeddings: allEmbeddings,
		Model:      model,
		TotalUsage: total,
	}, nil
}

// ListModels returns the embedding models available from the named provider.
// If req.Provider is empty the default provider is queried.
func (s *embeddingService) ListModels(ctx context.Context, req *ListEmbeddingModelsRequest) (*ListEmbeddingModelsResponse, error) {
	if s.closed.Load() {
		return nil, &EmbeddingServiceClosedError{}
	}

	provider, err := s.resolveProvider(req.Provider)
	if err != nil {
		return nil, err
	}

	models, err := provider.Models(ctx)
	if err != nil {
		return nil, err
	}

	return &ListEmbeddingModelsResponse{Models: models}, nil
}

// RegisterProvider adds provider under name to the service registry.
// It returns EmbeddingProviderRegistrationError if name is already taken.
func (s *embeddingService) RegisterProvider(name string, provider EmbeddingProvider) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.providers[name]; exists {
		return &EmbeddingProviderRegistrationError{
			Provider: name,
			Cause:    &EmbeddingProviderAlreadyRegisteredError{Name: name},
		}
	}
	s.providers[name] = provider
	return nil
}

// DeregisterProvider removes the provider with the given name.
// It returns EmbeddingProviderNotFoundError if name does not exist.
func (s *embeddingService) DeregisterProvider(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.providers[name]; !exists {
		return &EmbeddingProviderNotFoundError{Provider: name}
	}
	delete(s.providers, name)
	return nil
}

// Close closes all registered providers and marks the service as closed.
// Subsequent method calls return EmbeddingServiceClosedError.
func (s *embeddingService) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var errs []error
	for _, p := range s.providers {
		if err := p.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return &EmbeddingCloseError{Errors: errs}
	}
	return nil
}

// resolveProvider returns the provider for the given name, falling back to the
// default provider when name is empty.
func (s *embeddingService) resolveProvider(name string) (EmbeddingProvider, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := name
	if key == "" {
		key = s.defaultProvider
	}

	provider, ok := s.providers[key]
	if !ok {
		return nil, &EmbeddingProviderNotFoundError{Provider: key}
	}
	return provider, nil
}

// tryCache attempts to retrieve all embeddings from the cache. It returns the
// complete slice of embeddings and true only when every text is cached;
// otherwise it returns nil and false.
func (s *embeddingService) tryCache(model string, texts []string) ([][]float32, bool) {
	embeddings := make([][]float32, len(texts))
	for i, text := range texts {
		vec, ok := s.cache.Get(model, text)
		if !ok {
			return nil, false
		}
		embeddings[i] = vec
	}
	return embeddings, true
}

// EmbeddingProviderAlreadyRegisteredError is returned when attempting to
// register a provider under a name that is already in use.
type EmbeddingProviderAlreadyRegisteredError struct {
	// Name is the duplicate provider name.
	Name string
}

// Error implements the error interface.
func (e *EmbeddingProviderAlreadyRegisteredError) Error() string {
	return "service: embedding provider already registered: " + e.Name
}

// EmbeddingCloseError is returned when one or more errors occur during
// provider cleanup.
type EmbeddingCloseError struct {
	// Errors is the list of errors that occurred while closing resources.
	Errors []error
}

// Error implements the error interface.
func (e *EmbeddingCloseError) Error() string {
	msgs := make([]string, len(e.Errors))
	for i, err := range e.Errors {
		msgs[i] = err.Error()
	}
	return "service: embedding close errors: [" + strings.Join(msgs, "; ") + "]"
}

// Unwrap returns the list of underlying errors to support errors.Join semantics.
func (e *EmbeddingCloseError) Unwrap() []error {
	return e.Errors
}
