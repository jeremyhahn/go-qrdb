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

package main

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// RAGTransportUnsupportedError is returned when the active transport does
// not implement transport.RAGClient.
type RAGTransportUnsupportedError struct{}

// Error returns a human-readable description.
func (e *RAGTransportUnsupportedError) Error() string {
	return "rag: active transport does not implement the RAG service; use a network transport (rest, grpc, quic, mcp, unix)"
}

// ragClient resolves a RAGClient from c. Returns
// RAGTransportUnsupportedError if c is nil or its active transport does
// not satisfy transport.RAGClient, mirroring the userAuthFactorClient
// nil-guard pattern so the CLI never panics on an interface conversion.
func ragClient(c *sdk.Client) (transport.RAGClient, error) {
	if c == nil {
		return nil, &RAGTransportUnsupportedError{}
	}
	rc := c.RAG()
	if rc == nil {
		return nil, &RAGTransportUnsupportedError{}
	}
	return rc, nil
}

// newRAGCmd creates the "rag" command group with retrieval-augmented-generation
// subcommands. Only present under the qrdb_vector build tag.
func newRAGCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rag",
		Short: "Retrieval-augmented generation operations",
		Long:  "Ingest documents, run semantic queries, and manage the RAG document store.",
	}
	cmd.AddCommand(
		newRAGIngestCmd(),
		newRAGQueryCmd(),
		newRAGDeleteCmd(),
		newRAGListCmd(),
	)
	return cmd
}

// newRAGIngestCmd creates the "rag ingest" subcommand.
func newRAGIngestCmd() *cobra.Command {
	var (
		documentID    string
		text          string
		namespace     string
		model         string
		provider      string
		chunkStrategy string
		chunkSize     int
		chunkOverlap  int
		chunkMinSize  int
	)
	cmd := &cobra.Command{
		Use:   "ingest",
		Short: "Chunk, embed, and store a document",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rc, err := ragClient(client)
			if err != nil {
				return err
			}
			resp, err := rc.RAGIngestDocument(cmd.Context(), &transport.RAGIngestRequest{
				DocumentID:    documentID,
				Text:          text,
				Namespace:     namespace,
				Model:         model,
				Provider:      provider,
				ChunkStrategy: chunkStrategy,
				ChunkSize:     chunkSize,
				ChunkOverlap:  chunkOverlap,
				ChunkMinSize:  chunkMinSize,
			})
			if err != nil {
				return err
			}
			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				_, _ = fmt.Fprintf(outputWriter, "ingested document %s into namespace %s as %d chunk(s)\n",
					resp.DocumentID, resp.Namespace, resp.ChunkCount)
				return nil
			}
		},
	}
	cmd.Flags().StringVar(&documentID, "id", "", "Unique document identifier")
	_ = cmd.MarkFlagRequired("id")
	cmd.Flags().StringVar(&text, "text", "", "Document content to ingest")
	_ = cmd.MarkFlagRequired("text")
	cmd.Flags().StringVar(&namespace, "namespace", "", "Vector namespace to store embeddings in")
	_ = cmd.MarkFlagRequired("namespace")
	cmd.Flags().StringVar(&model, "model", "", "Embedding model (empty uses the provider default)")
	cmd.Flags().StringVar(&provider, "provider", "", "Registered embedding provider name")
	cmd.Flags().StringVar(&chunkStrategy, "chunk-strategy", "", "Chunking strategy: fixed, sentence, paragraph")
	cmd.Flags().IntVar(&chunkSize, "chunk-size", 0, "Target chunk size in characters")
	cmd.Flags().IntVar(&chunkOverlap, "chunk-overlap", 0, "Overlapping characters between adjacent chunks")
	cmd.Flags().IntVar(&chunkMinSize, "chunk-min-size", 0, "Minimum chunk size; smaller chunks are merged")
	return cmd
}

// newRAGQueryCmd creates the "rag query" subcommand.
func newRAGQueryCmd() *cobra.Command {
	var (
		namespace string
		topK      int
		model     string
		provider  string
	)
	cmd := &cobra.Command{
		Use:   "query <query>",
		Short: "Retrieve the most semantically similar passages",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := ragClient(client)
			if err != nil {
				return err
			}
			resp, err := rc.RAGQuery(cmd.Context(), &transport.RAGQueryRequest{
				Query:     args[0],
				Namespace: namespace,
				TopK:      topK,
				Model:     model,
				Provider:  provider,
			})
			if err != nil {
				return err
			}
			switch outputFormat {
			case "json":
				return printJSON(resp.Results)
			case "yaml":
				return printYAML(resp.Results)
			default:
				headers := []string{"SCORE", "DOCUMENT", "CHUNK", "TEXT"}
				rows := make([][]string, len(resp.Results))
				for i, r := range resp.Results {
					rows[i] = []string{
						strconv.FormatFloat(float64(r.Score), 'f', 4, 32),
						r.DocumentID,
						strconv.Itoa(r.ChunkIndex),
						truncate(r.ChunkText, 60),
					}
				}
				printTable(headers, rows)
				return nil
			}
		},
	}
	cmd.Flags().StringVar(&namespace, "namespace", "", "Vector namespace to search")
	_ = cmd.MarkFlagRequired("namespace")
	cmd.Flags().IntVar(&topK, "top-k", 0, "Maximum number of results (0 uses the default)")
	cmd.Flags().StringVar(&model, "model", "", "Embedding model for the query")
	cmd.Flags().StringVar(&provider, "provider", "", "Registered embedding provider name")
	return cmd
}

// newRAGDeleteCmd creates the "rag delete" subcommand.
func newRAGDeleteCmd() *cobra.Command {
	var (
		documentID string
		namespace  string
	)
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Remove a document and all of its chunk vectors",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rc, err := ragClient(client)
			if err != nil {
				return err
			}
			resp, err := rc.RAGDeleteDocument(cmd.Context(), &transport.RAGDeleteDocumentRequest{
				DocumentID: documentID,
				Namespace:  namespace,
			})
			if err != nil {
				return err
			}
			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				_, _ = fmt.Fprintf(outputWriter, "deleted document %s: removed %d chunk(s)\n",
					resp.DocumentID, resp.ChunksRemoved)
				return nil
			}
		},
	}
	cmd.Flags().StringVar(&documentID, "id", "", "Document identifier to remove")
	_ = cmd.MarkFlagRequired("id")
	cmd.Flags().StringVar(&namespace, "namespace", "", "Vector namespace the document is stored in")
	_ = cmd.MarkFlagRequired("namespace")
	return cmd
}

// newRAGListCmd creates the "rag list" subcommand.
func newRAGListCmd() *cobra.Command {
	var (
		namespace string
		limit     int
		offset    int
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List ingested documents in a namespace",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rc, err := ragClient(client)
			if err != nil {
				return err
			}
			resp, err := rc.RAGListDocuments(cmd.Context(), &transport.RAGListDocumentsRequest{
				Namespace: namespace,
				Limit:     limit,
				Offset:    offset,
			})
			if err != nil {
				return err
			}
			switch outputFormat {
			case "json":
				return printJSON(resp.Documents)
			case "yaml":
				return printYAML(resp.Documents)
			default:
				headers := []string{"ID", "NAMESPACE", "CHUNKS"}
				rows := make([][]string, len(resp.Documents))
				for i, d := range resp.Documents {
					rows[i] = []string{d.ID, d.Namespace, strconv.Itoa(d.ChunkCount)}
				}
				printTable(headers, rows)
				_, _ = fmt.Fprintf(outputWriter, "\nTotal: %d\n", resp.Total)
				return nil
			}
		},
	}
	cmd.Flags().StringVar(&namespace, "namespace", "", "Vector namespace to list")
	cmd.Flags().IntVar(&limit, "limit", 0, "Maximum number of documents (0 means no limit)")
	cmd.Flags().IntVar(&offset, "offset", 0, "Zero-based pagination offset")
	return cmd
}

// truncate shortens s to at most n runes, appending an ellipsis when cut.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
