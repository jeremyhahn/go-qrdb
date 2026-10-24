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

package mcp

import (
	"context"
	"encoding/json"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ragTools returns the MCP tool registrations for RAG operations.
func ragTools(ragSvc service.RAGServicer) []serverTool {
	return []serverTool{
		{
			Tool:    toolRAGIngest(),
			Perm:    permission.RAGIngest,
			Handler: handleRAGIngest(ragSvc),
		},
		{
			Tool:    toolRAGQuery(),
			Perm:    permission.RAGQuery,
			Handler: handleRAGQuery(ragSvc),
		},
		{
			Tool:    toolRAGDeleteDocument(),
			Perm:    permission.RAGDeleteDocument,
			Handler: handleRAGDeleteDocument(ragSvc),
		},
		{
			Tool:    toolRAGListDocuments(),
			Perm:    permission.RAGListDocuments,
			Handler: handleRAGListDocuments(ragSvc),
		},
	}
}

// ---------------------------------------------------------------------------
// Tool definitions
// ---------------------------------------------------------------------------

// toolRAGIngest defines the qrdb_rag_ingest tool schema.
func toolRAGIngest() mcp.Tool {
	return mcp.NewTool("qrdb_rag_ingest",
		mcp.WithDescription("Chunk, embed, and ingest a document into the RAG pipeline for later semantic retrieval."),
		mcp.WithString("document_id",
			mcp.Required(),
			mcp.Description("The unique identifier for this document."),
		),
		mcp.WithString("text",
			mcp.Required(),
			mcp.Description("The full text content of the document to ingest."),
		),
		mcp.WithString("namespace",
			mcp.Required(),
			mcp.Description("The vector namespace where embeddings will be stored."),
		),
		mcp.WithString("model",
			mcp.Description("The embedding model to use. Empty uses the provider default."),
		),
		mcp.WithString("provider",
			mcp.Description("The registered embedding provider name. Empty uses the default."),
		),
		mcp.WithString("chunk_strategy",
			mcp.Description("Chunking strategy: fixed, sentence, or paragraph. Default: fixed."),
		),
		mcp.WithNumber("chunk_size",
			mcp.Description("Target chunk size in characters. Default: 512."),
		),
		mcp.WithNumber("chunk_overlap",
			mcp.Description("Overlap characters between adjacent chunks. Default: 50."),
		),
		mcp.WithNumber("chunk_min_size",
			mcp.Description("Minimum chunk size in characters; smaller chunks are merged. Default: the node's configured minimum."),
		),
		mcp.WithString("metadata",
			mcp.Description("Optional JSON object of key-value metadata for the document."),
		),
	)
}

// toolRAGQuery defines the qrdb_rag_query tool schema.
func toolRAGQuery() mcp.Tool {
	return mcp.NewTool("qrdb_rag_query",
		mcp.WithDescription("Embed a natural-language query and retrieve the most semantically similar passages from the RAG pipeline."),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("The natural-language query or question."),
		),
		mcp.WithString("namespace",
			mcp.Required(),
			mcp.Description("The vector namespace to search."),
		),
		mcp.WithNumber("top_k",
			mcp.Description("Maximum number of results to return. Default: 5."),
		),
		mcp.WithString("model",
			mcp.Description("The embedding model to use for the query."),
		),
		mcp.WithString("provider",
			mcp.Description("The registered embedding provider name."),
		),
		mcp.WithString("filter",
			mcp.Description("Optional JSON object of key-value metadata filters."),
		),
	)
}

// toolRAGDeleteDocument defines the qrdb_rag_delete_document tool schema.
func toolRAGDeleteDocument() mcp.Tool {
	return mcp.NewTool("qrdb_rag_delete_document",
		mcp.WithDescription("Remove a document and all its chunk vectors and KV entries from the RAG pipeline."),
		mcp.WithString("document_id",
			mcp.Required(),
			mcp.Description("The unique identifier of the document to remove."),
		),
		mcp.WithString("namespace",
			mcp.Required(),
			mcp.Description("The vector namespace where the document's embeddings are stored."),
		),
	)
}

// toolRAGListDocuments defines the qrdb_rag_list_documents tool schema.
func toolRAGListDocuments() mcp.Tool {
	return mcp.NewTool("qrdb_rag_list_documents",
		mcp.WithDescription("List metadata about ingested documents in a RAG namespace with optional pagination."),
		mcp.WithString("namespace",
			mcp.Required(),
			mcp.Description("The vector namespace to list documents from."),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of documents to return. Zero means no limit."),
		),
		mcp.WithNumber("offset",
			mcp.Description("Pagination offset (zero-based)."),
		),
	)
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// handleRAGIngest returns a tool handler that delegates to ragSvc.IngestDocument.
func handleRAGIngest(svc service.RAGServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		docID := mcp.ParseString(req, "document_id", "")
		text := mcp.ParseString(req, "text", "")
		namespace := mcp.ParseString(req, "namespace", "")
		model := mcp.ParseString(req, "model", "")
		provider := mcp.ParseString(req, "provider", "")
		chunkStrategy := mcp.ParseString(req, "chunk_strategy", "")
		chunkSize := mcp.ParseInt(req, "chunk_size", 0)
		chunkOverlap := mcp.ParseInt(req, "chunk_overlap", 0)
		chunkMinSize := mcp.ParseInt(req, "chunk_min_size", 0)

		var metadata map[string]string
		if raw := mcp.ParseString(req, "metadata", ""); raw != "" {
			_ = json.Unmarshal([]byte(raw), &metadata)
		}

		// An omitted chunk field stays zero; the node's default fills it.
		var chunkCfg *service.ChunkConfig
		if chunkStrategy != "" || chunkSize != 0 || chunkOverlap != 0 || chunkMinSize != 0 {
			chunkCfg = &service.ChunkConfig{
				Strategy:  chunkStrategy,
				ChunkSize: chunkSize,
				Overlap:   chunkOverlap,
				MinSize:   chunkMinSize,
			}
		}

		resp, err := svc.IngestDocument(ctx, &service.IngestDocumentRequest{
			DocumentID:  docID,
			Text:        text,
			Namespace:   namespace,
			Model:       model,
			Provider:    provider,
			Metadata:    metadata,
			ChunkConfig: chunkCfg,
		})
		if err != nil {
			return nil, err
		}

		out, err := json.Marshal(resp)
		if err != nil {
			return nil, err
		}
		return mcp.NewToolResultText(string(out)), nil
	}
}

// handleRAGQuery returns a tool handler that delegates to ragSvc.Query.
func handleRAGQuery(svc service.RAGServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query := mcp.ParseString(req, "query", "")
		namespace := mcp.ParseString(req, "namespace", "")
		model := mcp.ParseString(req, "model", "")
		provider := mcp.ParseString(req, "provider", "")
		topK := mcp.ParseInt(req, "top_k", 0)

		var filter map[string]string
		if raw := mcp.ParseString(req, "filter", ""); raw != "" {
			_ = json.Unmarshal([]byte(raw), &filter)
		}

		resp, err := svc.Query(ctx, &service.RAGQueryRequest{
			Query:     query,
			Namespace: namespace,
			TopK:      topK,
			Model:     model,
			Provider:  provider,
			Filter:    filter,
		})
		if err != nil {
			return nil, err
		}

		out, err := json.Marshal(resp)
		if err != nil {
			return nil, err
		}
		return mcp.NewToolResultText(string(out)), nil
	}
}

// handleRAGDeleteDocument returns a tool handler that delegates to
// ragSvc.DeleteDocument.
func handleRAGDeleteDocument(svc service.RAGServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		docID := mcp.ParseString(req, "document_id", "")
		namespace := mcp.ParseString(req, "namespace", "")

		resp, err := svc.DeleteDocument(ctx, &service.DeleteRAGDocumentRequest{
			DocumentID: docID,
			Namespace:  namespace,
		})
		if err != nil {
			return nil, err
		}

		out, err := json.Marshal(resp)
		if err != nil {
			return nil, err
		}
		return mcp.NewToolResultText(string(out)), nil
	}
}

// handleRAGListDocuments returns a tool handler that delegates to
// ragSvc.ListDocuments.
func handleRAGListDocuments(svc service.RAGServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		namespace := mcp.ParseString(req, "namespace", "")
		limit := mcp.ParseInt(req, "limit", 0)
		offset := mcp.ParseInt(req, "offset", 0)

		resp, err := svc.ListDocuments(ctx, &service.ListRAGDocumentsRequest{
			Namespace: namespace,
			Limit:     limit,
			Offset:    offset,
		})
		if err != nil {
			return nil, err
		}

		out, err := json.Marshal(resp)
		if err != nil {
			return nil, err
		}
		return mcp.NewToolResultText(string(out)), nil
	}
}
