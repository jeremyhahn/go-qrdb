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

package embedded

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.EmbeddingClient = (*Transport)(nil)

// WithEmbeddingService injects an EmbeddingServicer into the transport.
func WithEmbeddingService(svc service.EmbeddingServicer) Option {
	return func(t *Transport) {
		t.embeddingSvc = svc
	}
}

// Embed generates embedding vectors by delegating to the injected service.
func (t *Transport) Embed(ctx context.Context, req *transport.EmbedRequest) (*transport.EmbedResponse, error) {
	if err := t.gate("Embed"); err != nil {
		return nil, err
	}
	if t.embeddingSvc == nil {
		return nil, &NilServiceError{Service: "EmbeddingServicer"}
	}
	resp, err := t.embeddingSvc.Embed(ctx, &service.EmbedRequest{
		Texts:    req.Texts,
		Model:    req.Model,
		Provider: req.Provider,
	})
	if err != nil {
		return nil, err
	}
	return &transport.EmbedResponse{
		Embeddings: resp.Embeddings,
		Model:      resp.Model,
		Cached:     resp.Cached,
		Usage:      toTransportUsage(resp.Usage),
	}, nil
}

// BatchEmbed embeds a large set of texts by delegating to the injected service.
func (t *Transport) BatchEmbed(ctx context.Context, req *transport.BatchEmbedRequest) (*transport.BatchEmbedResponse, error) {
	if err := t.gate("BatchEmbed"); err != nil {
		return nil, err
	}
	if t.embeddingSvc == nil {
		return nil, &NilServiceError{Service: "EmbeddingServicer"}
	}
	resp, err := t.embeddingSvc.BatchEmbed(ctx, &service.BatchEmbedRequest{
		Texts:     req.Texts,
		Model:     req.Model,
		Provider:  req.Provider,
		BatchSize: req.BatchSize,
	})
	if err != nil {
		return nil, err
	}
	return &transport.BatchEmbedResponse{
		Embeddings: resp.Embeddings,
		Model:      resp.Model,
		Usage:      toTransportUsage(resp.TotalUsage),
	}, nil
}

// ListEmbeddingModels returns a provider's models by delegating to the service.
func (t *Transport) ListEmbeddingModels(ctx context.Context, provider string) (*transport.ListEmbeddingModelsResponse, error) {
	if err := t.gate("ListEmbeddingModels"); err != nil {
		return nil, err
	}
	if t.embeddingSvc == nil {
		return nil, &NilServiceError{Service: "EmbeddingServicer"}
	}
	resp, err := t.embeddingSvc.ListModels(ctx, &service.ListEmbeddingModelsRequest{Provider: provider})
	if err != nil {
		return nil, err
	}
	models := make([]transport.EmbeddingModelInfo, len(resp.Models))
	for i := range resp.Models {
		models[i] = transport.EmbeddingModelInfo{
			Name:       resp.Models[i].Name,
			Dimensions: resp.Models[i].Dimensions,
			MaxTokens:  resp.Models[i].MaxTokens,
		}
	}
	return &transport.ListEmbeddingModelsResponse{Models: models}, nil
}

// toTransportUsage converts a service UsageInfo to its transport form.
func toTransportUsage(u *service.UsageInfo) *transport.UsageInfo {
	if u == nil {
		return nil
	}
	return &transport.UsageInfo{
		PromptTokens: u.PromptTokens,
		TotalTokens:  u.TotalTokens,
	}
}
