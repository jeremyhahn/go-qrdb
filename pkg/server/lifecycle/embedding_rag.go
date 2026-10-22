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

package lifecycle

import (
	"context"
	"log/slog"
	"strings"
	"time"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/embedding"
	"github.com/jeremyhahn/go-qrdb/pkg/rag"
	"github.com/jeremyhahn/go-qrdb/pkg/rag/chunker"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// openAIProviderAdapter
// ---------------------------------------------------------------------------

// openAIProviderAdapter adapts embedding.OpenAIProvider to satisfy
// service.EmbeddingProvider. The two interfaces are structurally identical
// except that their return types are distinct named types in different packages
// (embedding.EmbeddingResult vs service.EmbeddingResult, etc.), so a thin
// conversion layer is required.
type openAIProviderAdapter struct {
	p *embedding.OpenAIProvider
}

// Compile-time assertion.
var _ service.EmbeddingProvider = (*openAIProviderAdapter)(nil)

// Embed implements service.EmbeddingProvider.
func (a *openAIProviderAdapter) Embed(ctx context.Context, texts []string, model string) (*service.EmbeddingResult, error) {
	res, err := a.p.Embed(ctx, texts, model)
	if err != nil {
		return nil, err
	}
	return convertEmbeddingResult(res), nil
}

// Models implements service.EmbeddingProvider.
func (a *openAIProviderAdapter) Models(ctx context.Context) ([]service.ModelInfo, error) {
	infos, err := a.p.Models(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]service.ModelInfo, len(infos))
	for i, m := range infos {
		out[i] = service.ModelInfo{
			Name:       m.Name,
			Dimensions: m.Dimensions,
			MaxTokens:  m.MaxTokens,
		}
	}
	return out, nil
}

// Close implements service.EmbeddingProvider.
func (a *openAIProviderAdapter) Close() error {
	return a.p.Close()
}

// convertEmbeddingResult converts embedding.EmbeddingResult to service.EmbeddingResult.
func convertEmbeddingResult(r *embedding.EmbeddingResult) *service.EmbeddingResult {
	if r == nil {
		return nil
	}
	res := &service.EmbeddingResult{
		Embeddings: r.Embeddings,
		Model:      r.Model,
	}
	if r.Usage != nil {
		res.Usage = &service.UsageInfo{
			PromptTokens: r.Usage.PromptTokens,
			TotalTokens:  r.Usage.TotalTokens,
		}
	}
	return res
}

// ---------------------------------------------------------------------------
// RAG ingestor adapter
// ---------------------------------------------------------------------------

// ragIngestorAdapter bridges rag.Ingestor to service.RAGIngestor. The
// rag.Ingestor.Ingest method accepts *rag.IngestRequest while
// service.RAGIngestor.Ingest takes *service.IngestDocumentRequest, requiring a
// field-mapping conversion, in which every chunk setting the request leaves
// unset takes the node's default.
type ragIngestorAdapter struct {
	ing           *rag.Ingestor
	chunkDefaults chunker.Config
}

func newRAGIngestorAdapter(ing *rag.Ingestor, chunkDefaults chunker.Config) *ragIngestorAdapter {
	return &ragIngestorAdapter{ing: ing, chunkDefaults: chunkDefaults}
}

// chunkConfig returns req with every unset field, a zero value on every
// transport, taken from the adapter's defaults.
func (a *ragIngestorAdapter) chunkConfig(req *service.ChunkConfig) *chunker.Config {
	cfg := a.chunkDefaults
	if req == nil {
		return &cfg
	}
	if req.Strategy != "" {
		cfg.Strategy = req.Strategy
	}
	if req.ChunkSize != 0 {
		cfg.ChunkSize = req.ChunkSize
	}
	if req.Overlap != 0 {
		cfg.Overlap = req.Overlap
	}
	if req.MinSize != 0 {
		cfg.MinSize = req.MinSize
	}
	return &cfg
}

// Compile-time assertion.
var _ service.RAGIngestor = (*ragIngestorAdapter)(nil)

// RemoveChunks implements service.RAGIngestor.
func (a *ragIngestorAdapter) RemoveChunks(ctx context.Context, namespace, documentID string) error {
	return a.ing.RemoveChunks(ctx, namespace, documentID)
}

// Ingest implements service.RAGIngestor.
func (a *ragIngestorAdapter) Ingest(ctx context.Context, req *service.IngestDocumentRequest) (*service.RAGIngestResult, error) {
	ragReq := &rag.IngestRequest{
		Document: &rag.Document{
			ID:       req.DocumentID,
			Text:     req.Text,
			Metadata: req.Metadata,
		},
		Namespace:   req.Namespace,
		Model:       req.Model,
		Provider:    req.Provider,
		ChunkConfig: a.chunkConfig(req.ChunkConfig),
	}
	resp, err := a.ing.Ingest(ctx, ragReq)
	if err != nil {
		return nil, err
	}
	return &service.RAGIngestResult{
		DocumentID: resp.DocumentID,
		ChunkCount: resp.ChunkCount,
		Namespace:  resp.Namespace,
	}, nil
}

// ---------------------------------------------------------------------------
// RAG retriever adapter
// ---------------------------------------------------------------------------

// ragRetrieverAdapter bridges rag.Retriever to service.RAGRetriever. A query
// that sets no top-k takes defaultTopK; zero leaves the retriever's own.
type ragRetrieverAdapter struct {
	ret         *rag.Retriever
	defaultTopK int
}

func newRAGRetrieverAdapter(ret *rag.Retriever, defaultTopK int) *ragRetrieverAdapter {
	return &ragRetrieverAdapter{ret: ret, defaultTopK: defaultTopK}
}

// Compile-time assertion.
var _ service.RAGRetriever = (*ragRetrieverAdapter)(nil)

// Retrieve implements service.RAGRetriever.
func (a *ragRetrieverAdapter) Retrieve(ctx context.Context, req *service.RAGQueryRequest) (*service.RAGRetrieveResult, error) {
	topK := req.TopK
	if topK <= 0 {
		topK = a.defaultTopK
	}
	ragReq := &rag.RetrieveRequest{
		Query:     req.Query,
		Namespace: req.Namespace,
		TopK:      topK,
		Model:     req.Model,
		Provider:  req.Provider,
		Filter:    req.Filter,
	}
	resp, err := a.ret.Retrieve(ctx, ragReq)
	if err != nil {
		return nil, err
	}
	results := make([]*service.RAGRetrievalResult, len(resp.Results))
	for i, r := range resp.Results {
		results[i] = &service.RAGRetrievalResult{
			ChunkText:  r.ChunkText,
			Score:      r.Score,
			DocumentID: r.DocumentID,
			ChunkIndex: r.ChunkIndex,
			Metadata:   r.Metadata,
		}
	}
	return &service.RAGRetrieveResult{Results: results}, nil
}

// ---------------------------------------------------------------------------
// buildEmbeddingAndRAG
// ---------------------------------------------------------------------------

// buildEmbeddingAndRAG constructs the embedding service (registering one
// OpenAI-compatible provider per config entry) and the RAG service (Ingestor
// and Retriever backed by the embedding service, vectorSvc, and kvSvc).
//
// Returns (nil, nil, nil) when cfg.Providers is empty, leaving embedding and
// RAG disabled for this node.
func buildEmbeddingAndRAG(
	cfg qrdbconfig.EmbeddingConfig,
	ragCfg qrdbconfig.RAGConfig,
	vectorSvc service.VectorServicer,
	kvSvc service.KVServicer,
	log *slog.Logger,
) (service.EmbeddingServicer, service.RAGServicer, error) {
	if len(cfg.Providers) == 0 {
		return nil, nil, nil
	}

	// Build the embedding service with optional LRU cache.
	var embOpts []service.EmbeddingServiceOption
	if cfg.DefaultProvider != "" {
		embOpts = append(embOpts, service.WithDefaultProvider(cfg.DefaultProvider))
	}
	if cfg.CacheCapacity > 0 {
		cache, err := embedding.NewCache(cfg.CacheCapacity)
		if err != nil {
			return nil, nil, &EmbeddingCacheError{Err: err}
		}
		embOpts = append(embOpts, service.WithEmbeddingCache(cache))
	}
	embSvc := service.NewEmbeddingService(embOpts...)

	// Register one OpenAI-compatible provider per config entry.
	for _, p := range cfg.Providers {
		// NewOpenAIProvider internally appends "/v1" to the APIURL it receives.
		// Strip any trailing "/v1" from the config BaseURL to avoid a double-suffix
		// (e.g. "http://localhost:11434/v1" would otherwise become
		// "http://localhost:11434/v1/v1/embeddings").
		apiURL := strings.TrimSuffix(p.BaseURL, "/v1")

		timeout := 30
		if p.Timeout != "" {
			if d, err := time.ParseDuration(p.Timeout); err == nil {
				timeout = int(d.Seconds())
			}
		}

		maxRetries := p.MaxRetries
		if maxRetries <= 0 {
			maxRetries = 3
		}

		provider, err := embedding.NewOpenAIProvider(embedding.Config{
			APIURL:     apiURL,
			APIKey:     p.APIKey,
			Model:      p.Model,
			Timeout:    timeout,
			MaxRetries: maxRetries,
		})
		if err != nil {
			return nil, nil, &EmbeddingProviderError{Name: p.Name, Err: err}
		}

		name := p.Name
		if name == "" {
			name = "default"
		}
		if err := embSvc.RegisterProvider(name, &openAIProviderAdapter{p: provider}); err != nil {
			return nil, nil, &EmbeddingProviderError{Name: name, Err: err}
		}
		log.Info("embedding provider registered", slog.String("name", name))
	}

	// Build the RAG pipeline only when a vector service is available. Its
	// chunk text, document records and vectors are all resolved per call
	// through one RAGBinding -- the node's per-(tenant, database) registry,
	// bound by the server at start -- so they are confined to the caller's
	// database and its seal, ownership, quota and isolation apply.
	var ragSvc service.RAGServicer
	if vectorSvc != nil {
		chunkDefaults, err := ragChunkDefaults(ragCfg)
		if err != nil {
			return nil, nil, err
		}
		binding := service.NewRAGBinding()
		embedAdapter := service.NewEmbedProviderAdapter(embSvc)
		vectorAdapter := service.NewScopedVectorStoreAdapter(binding)
		ragStore := service.NewScopedRAGStore(binding)

		ingestor := rag.NewIngestor(
			embedAdapter,
			vectorAdapter,
			ragStore,
			ragCfg.MaxDocumentSize,
		)
		retriever := rag.NewRetriever(
			embedAdapter,
			vectorAdapter,
			ragStore,
		)
		ragSvc = service.NewRAGService(
			newRAGIngestorAdapter(ingestor, chunkDefaults),
			newRAGRetrieverAdapter(retriever, ragCfg.DefaultTopK),
			ragStore,
		)
		log.Info("RAG service initialized")
	}

	return embSvc, ragSvc, nil
}

// ragChunkDefaults returns the chunk configuration a request's unset fields
// take: each rag key the configuration sets, and the chunker's built-in value
// for each it leaves zero. A set the chunker refuses is a configuration
// error, reported at startup rather than on every ingest.
func ragChunkDefaults(ragCfg qrdbconfig.RAGConfig) (chunker.Config, error) {
	cfg := *chunker.DefaultConfig()
	if ragCfg.DefaultChunkStrategy != "" {
		cfg.Strategy = ragCfg.DefaultChunkStrategy
	}
	if ragCfg.DefaultChunkSize != 0 {
		cfg.ChunkSize = ragCfg.DefaultChunkSize
	}
	if ragCfg.DefaultOverlap != 0 {
		cfg.Overlap = ragCfg.DefaultOverlap
	}
	if _, err := chunker.New(&cfg); err != nil {
		return chunker.Config{}, &RAGChunkDefaultsError{Err: err}
	}
	return cfg, nil
}
