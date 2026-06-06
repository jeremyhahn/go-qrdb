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

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// embeddingTools returns the MCP tool registrations for embedding operations.
func embeddingTools(embeddingSvc service.EmbeddingServicer) []mcpserver.ServerTool {
	return []mcpserver.ServerTool{
		{
			Tool:    toolEmbedText(),
			Handler: handleEmbedText(embeddingSvc),
		},
		{
			Tool:    toolBatchEmbed(),
			Handler: handleBatchEmbed(embeddingSvc),
		},
		{
			Tool:    toolListEmbeddingModels(),
			Handler: handleListEmbeddingModels(embeddingSvc),
		},
	}
}

// ---------------------------------------------------------------------------
// Embed text
// ---------------------------------------------------------------------------

// toolEmbedText defines the qrdb_embed_text tool schema.
func toolEmbedText() mcp.Tool {
	return mcp.NewTool("qrdb_embed_text",
		mcp.WithDescription("Generate vector embeddings for one or more texts using the configured embedding provider."),
		mcp.WithArray("texts",
			mcp.Required(),
			mcp.Description("The list of text strings to embed. Must contain at least one element."),
		),
		mcp.WithString("model",
			mcp.Description("The embedding model to use. Leave empty to use the provider's default model."),
		),
		mcp.WithString("provider",
			mcp.Description("The name of the registered embedding provider to use. Leave empty to use the default provider."),
		),
	)
}

// handleEmbedText returns a tool handler that delegates to embeddingSvc.Embed.
func handleEmbedText(svc service.EmbeddingServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		rawTexts := mcp.ParseArgument(req, "texts", nil)
		if rawTexts == nil {
			return mcp.NewToolResultError("texts is required"), nil
		}

		textsSlice, ok := rawTexts.([]interface{})
		if !ok || len(textsSlice) == 0 {
			return mcp.NewToolResultError("texts must be a non-empty array of strings"), nil
		}

		texts := make([]string, 0, len(textsSlice))
		for _, v := range textsSlice {
			s, ok := v.(string)
			if !ok {
				return mcp.NewToolResultError("each element of texts must be a string"), nil
			}
			texts = append(texts, s)
		}

		model := mcp.ParseString(req, "model", "")
		provider := mcp.ParseString(req, "provider", "")

		resp, err := svc.Embed(ctx, &service.EmbedRequest{
			Texts:    texts,
			Model:    model,
			Provider: provider,
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return marshalToolResult("qrdb_embed_text", resp)
	}
}

// ---------------------------------------------------------------------------
// Batch embed
// ---------------------------------------------------------------------------

// toolBatchEmbed defines the qrdb_batch_embed tool schema.
func toolBatchEmbed() mcp.Tool {
	return mcp.NewTool("qrdb_batch_embed",
		mcp.WithDescription("Generate vector embeddings for a large list of texts, automatically splitting into sub-batches for the provider."),
		mcp.WithArray("texts",
			mcp.Required(),
			mcp.Description("The complete list of text strings to embed."),
		),
		mcp.WithString("model",
			mcp.Description("The embedding model to use for all batches."),
		),
		mcp.WithString("provider",
			mcp.Description("The name of the registered embedding provider to use."),
		),
		mcp.WithNumber("batch_size",
			mcp.Description("Maximum number of texts per provider call. Zero uses the service default (256)."),
		),
	)
}

// handleBatchEmbed returns a tool handler that delegates to embeddingSvc.BatchEmbed.
func handleBatchEmbed(svc service.EmbeddingServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		rawTexts := mcp.ParseArgument(req, "texts", nil)
		if rawTexts == nil {
			return mcp.NewToolResultError("texts is required"), nil
		}

		textsSlice, ok := rawTexts.([]interface{})
		if !ok || len(textsSlice) == 0 {
			return mcp.NewToolResultError("texts must be a non-empty array of strings"), nil
		}

		texts := make([]string, 0, len(textsSlice))
		for _, v := range textsSlice {
			s, ok := v.(string)
			if !ok {
				return mcp.NewToolResultError("each element of texts must be a string"), nil
			}
			texts = append(texts, s)
		}

		model := mcp.ParseString(req, "model", "")
		provider := mcp.ParseString(req, "provider", "")
		batchSize := int(mcp.ParseInt(req, "batch_size", 0))

		resp, err := svc.BatchEmbed(ctx, &service.BatchEmbedRequest{
			Texts:     texts,
			Model:     model,
			Provider:  provider,
			BatchSize: batchSize,
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return marshalToolResult("qrdb_batch_embed", resp)
	}
}

// ---------------------------------------------------------------------------
// List embedding models
// ---------------------------------------------------------------------------

// toolListEmbeddingModels defines the qrdb_list_embedding_models tool schema.
func toolListEmbeddingModels() mcp.Tool {
	return mcp.NewTool("qrdb_list_embedding_models",
		mcp.WithDescription("List the embedding models available from a registered provider."),
		mcp.WithString("provider",
			mcp.Description("The name of the registered embedding provider to query. Leave empty to query the default provider."),
		),
	)
}

// handleListEmbeddingModels returns a tool handler that delegates to
// embeddingSvc.ListModels.
func handleListEmbeddingModels(svc service.EmbeddingServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		provider := mcp.ParseString(req, "provider", "")

		resp, err := svc.ListModels(ctx, &service.ListEmbeddingModelsRequest{
			Provider: provider,
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return marshalToolResult("qrdb_list_embedding_models", resp)
	}
}
