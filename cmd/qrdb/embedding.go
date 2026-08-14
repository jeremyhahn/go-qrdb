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

// EmbeddingTransportUnsupportedError is returned when the active transport
// does not implement transport.EmbeddingClient.
type EmbeddingTransportUnsupportedError struct{}

// Error returns a human-readable description.
func (e *EmbeddingTransportUnsupportedError) Error() string {
	return "embedding: active transport does not implement the embedding service; use a network transport (rest, grpc, quic, mcp, unix)"
}

// embeddingClient resolves an EmbeddingClient from c. Returns
// EmbeddingTransportUnsupportedError if c is nil or its active transport
// does not satisfy transport.EmbeddingClient, mirroring the
// userAuthFactorClient nil-guard pattern so the CLI never panics on an
// interface conversion.
func embeddingClient(c *sdk.Client) (transport.EmbeddingClient, error) {
	if c == nil {
		return nil, &EmbeddingTransportUnsupportedError{}
	}
	ec := c.Embedding()
	if ec == nil {
		return nil, &EmbeddingTransportUnsupportedError{}
	}
	return ec, nil
}

// newEmbeddingCmd creates the "embedding" command group with text-embedding
// subcommands.
func newEmbeddingCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "embedding",
		Short: "Text embedding operations",
		Long:  "Generate vector embeddings for text and list available embedding models.",
	}
	cmd.AddCommand(
		newEmbeddingEmbedCmd(),
		newEmbeddingBatchCmd(),
		newEmbeddingModelsCmd(),
	)
	return cmd
}

// newEmbeddingEmbedCmd creates the "embedding embed" subcommand.
func newEmbeddingEmbedCmd() *cobra.Command {
	var (
		model    string
		provider string
	)
	cmd := &cobra.Command{
		Use:   "embed <text> [text...]",
		Short: "Generate embeddings for one or more texts",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ec, err := embeddingClient(client)
			if err != nil {
				return err
			}
			resp, err := ec.Embed(cmd.Context(), &transport.EmbedRequest{
				Texts:    args,
				Model:    model,
				Provider: provider,
			})
			if err != nil {
				return err
			}
			return printEmbedResponse(resp.Embeddings, resp.Model, resp.Cached)
		},
	}
	cmd.Flags().StringVar(&model, "model", "", "Embedding model to use (empty uses the provider default)")
	cmd.Flags().StringVar(&provider, "provider", "", "Registered embedding provider name (empty uses the default)")
	return cmd
}

// newEmbeddingBatchCmd creates the "embedding batch-embed" subcommand.
func newEmbeddingBatchCmd() *cobra.Command {
	var (
		model     string
		provider  string
		batchSize int
	)
	cmd := &cobra.Command{
		Use:   "batch-embed <text> [text...]",
		Short: "Generate embeddings for a large list of texts in sub-batches",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ec, err := embeddingClient(client)
			if err != nil {
				return err
			}
			resp, err := ec.BatchEmbed(cmd.Context(), &transport.BatchEmbedRequest{
				Texts:     args,
				Model:     model,
				Provider:  provider,
				BatchSize: batchSize,
			})
			if err != nil {
				return err
			}
			return printEmbedResponse(resp.Embeddings, resp.Model, false)
		},
	}
	cmd.Flags().StringVar(&model, "model", "", "Embedding model to use for all batches")
	cmd.Flags().StringVar(&provider, "provider", "", "Registered embedding provider name")
	cmd.Flags().IntVar(&batchSize, "batch-size", 0, "Maximum texts per provider call (0 uses the service default)")
	return cmd
}

// newEmbeddingModelsCmd creates the "embedding models" subcommand.
func newEmbeddingModelsCmd() *cobra.Command {
	var provider string
	cmd := &cobra.Command{
		Use:   "models",
		Short: "List the embedding models available from a provider",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ec, err := embeddingClient(client)
			if err != nil {
				return err
			}
			resp, err := ec.ListEmbeddingModels(cmd.Context(), provider)
			if err != nil {
				return err
			}
			switch outputFormat {
			case "json":
				return printJSON(resp.Models)
			case "yaml":
				return printYAML(resp.Models)
			default:
				headers := []string{"NAME", "DIMENSIONS", "MAX_TOKENS"}
				rows := make([][]string, len(resp.Models))
				for i, m := range resp.Models {
					rows[i] = []string{m.Name, strconv.Itoa(m.Dimensions), strconv.Itoa(m.MaxTokens)}
				}
				printTable(headers, rows)
				return nil
			}
		},
	}
	cmd.Flags().StringVar(&provider, "provider", "", "Registered embedding provider to query (empty uses the default)")
	return cmd
}

// printEmbedResponse renders an embedding response. The full vectors are only
// emitted in json/yaml mode; the table view shows a summary because raw
// vectors are large and rarely useful on a terminal.
func printEmbedResponse(embeddings [][]float32, model string, cached bool) error {
	switch outputFormat {
	case "json":
		return printJSON(map[string]any{"embeddings": embeddings, "model": model, "cached": cached})
	case "yaml":
		return printYAML(map[string]any{"embeddings": embeddings, "model": model, "cached": cached})
	default:
		dims := 0
		if len(embeddings) > 0 {
			dims = len(embeddings[0])
		}
		_, _ = fmt.Fprintf(outputWriter, "model:  %s\ncount:  %d\ndims:   %d\ncached: %t\n",
			model, len(embeddings), dims, cached)
		return nil
	}
}
