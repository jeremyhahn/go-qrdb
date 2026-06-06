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

package mcp

import (
	"context"
	"encoding/json"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.EmbeddingClient = (*Transport)(nil)

// mcpUsageInfo mirrors the JSON emitted by the embedding tools, which marshal
// the service-layer UsageInfo directly (PascalCase, no json tags).
type mcpUsageInfo struct {
	PromptTokens int
	TotalTokens  int
}

func (u *mcpUsageInfo) toTransport() *transport.UsageInfo {
	if u == nil {
		return nil
	}
	return &transport.UsageInfo{PromptTokens: u.PromptTokens, TotalTokens: u.TotalTokens}
}

// Embed generates embedding vectors via the qrdb_embed_text tool.
func (t *Transport) Embed(ctx context.Context, req *transport.EmbedRequest) (*transport.EmbedResponse, error) {
	args := map[string]any{"texts": req.Texts}
	if req.Model != "" {
		args["model"] = req.Model
	}
	if req.Provider != "" {
		args["provider"] = req.Provider
	}
	text, err := t.callTool(ctx, "qrdb_embed_text", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "Embed", Err: err}
	}
	var wire struct {
		Embeddings [][]float32
		Model      string
		Usage      *mcpUsageInfo
		Cached     bool
	}
	if jsonErr := json.Unmarshal([]byte(text), &wire); jsonErr != nil {
		return nil, &transport.RequestError{Method: "Embed", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return &transport.EmbedResponse{
		Embeddings: wire.Embeddings,
		Model:      wire.Model,
		Cached:     wire.Cached,
		Usage:      wire.Usage.toTransport(),
	}, nil
}

// BatchEmbed embeds a large set of texts via the qrdb_batch_embed tool.
func (t *Transport) BatchEmbed(ctx context.Context, req *transport.BatchEmbedRequest) (*transport.BatchEmbedResponse, error) {
	args := map[string]any{"texts": req.Texts}
	if req.Model != "" {
		args["model"] = req.Model
	}
	if req.Provider != "" {
		args["provider"] = req.Provider
	}
	if req.BatchSize != 0 {
		args["batch_size"] = req.BatchSize
	}
	text, err := t.callTool(ctx, "qrdb_batch_embed", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "BatchEmbed", Err: err}
	}
	var wire struct {
		Embeddings [][]float32
		Model      string
		TotalUsage *mcpUsageInfo
	}
	if jsonErr := json.Unmarshal([]byte(text), &wire); jsonErr != nil {
		return nil, &transport.RequestError{Method: "BatchEmbed", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return &transport.BatchEmbedResponse{
		Embeddings: wire.Embeddings,
		Model:      wire.Model,
		Usage:      wire.TotalUsage.toTransport(),
	}, nil
}

// ListEmbeddingModels returns a provider's models via the qrdb_list_embedding_models tool.
func (t *Transport) ListEmbeddingModels(ctx context.Context, provider string) (*transport.ListEmbeddingModelsResponse, error) {
	args := map[string]any{}
	if provider != "" {
		args["provider"] = provider
	}
	text, err := t.callTool(ctx, "qrdb_list_embedding_models", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListEmbeddingModels", Err: err}
	}
	var wire struct {
		Models []struct {
			Name       string
			Dimensions int
			MaxTokens  int
		}
	}
	if jsonErr := json.Unmarshal([]byte(text), &wire); jsonErr != nil {
		return nil, &transport.RequestError{Method: "ListEmbeddingModels", Err: &jsonDecodeError{Err: jsonErr}}
	}
	models := make([]transport.EmbeddingModelInfo, len(wire.Models))
	for i := range wire.Models {
		models[i] = transport.EmbeddingModelInfo{
			Name:       wire.Models[i].Name,
			Dimensions: wire.Models[i].Dimensions,
			MaxTokens:  wire.Models[i].MaxTokens,
		}
	}
	return &transport.ListEmbeddingModelsResponse{Models: models}, nil
}
