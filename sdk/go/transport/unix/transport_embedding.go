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

package unix

import (
	"context"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.EmbeddingClient = (*Transport)(nil)

// protoEmbeddingsToMatrix converts repeated EmbeddingVector to a [][]float32.
func protoEmbeddingsToMatrix(vectors []*pb.EmbeddingVector) [][]float32 {
	out := make([][]float32, len(vectors))
	for i, v := range vectors {
		out[i] = v.GetValues()
	}
	return out
}

// protoEmbeddingUsage converts a proto EmbeddingUsage to its transport form.
func protoEmbeddingUsage(u *pb.EmbeddingUsage) *transport.UsageInfo {
	if u == nil {
		return nil
	}
	return &transport.UsageInfo{
		PromptTokens: int(u.GetPromptTokens()),
		TotalTokens:  int(u.GetTotalTokens()),
	}
}

// Embed generates embedding vectors via the Embed RPC over the Unix domain
// socket.
func (t *Transport) Embed(ctx context.Context, req *transport.EmbedRequest) (*transport.EmbedResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.Embed(ctx, &pb.EmbedRequest{
		Texts:    req.Texts,
		Model:    req.Model,
		Provider: req.Provider,
	})
	if err != nil {
		return nil, mapGRPCError("Embed", err)
	}
	return &transport.EmbedResponse{
		Embeddings: protoEmbeddingsToMatrix(resp.GetEmbeddings()),
		Model:      resp.GetModel(),
		Cached:     resp.GetCached(),
		Usage:      protoEmbeddingUsage(resp.GetUsage()),
	}, nil
}

// BatchEmbed embeds a large set of texts via the BatchEmbed RPC over the
// Unix domain socket.
func (t *Transport) BatchEmbed(ctx context.Context, req *transport.BatchEmbedRequest) (*transport.BatchEmbedResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.BatchEmbed(ctx, &pb.BatchEmbedRequest{
		Texts:     req.Texts,
		Model:     req.Model,
		Provider:  req.Provider,
		BatchSize: int32(req.BatchSize),
	})
	if err != nil {
		return nil, mapGRPCError("BatchEmbed", err)
	}
	return &transport.BatchEmbedResponse{
		Embeddings: protoEmbeddingsToMatrix(resp.GetEmbeddings()),
		Model:      resp.GetModel(),
		Usage:      protoEmbeddingUsage(resp.GetTotalUsage()),
	}, nil
}

// ListEmbeddingModels returns a provider's models via the
// ListEmbeddingModels RPC over the Unix domain socket.
func (t *Transport) ListEmbeddingModels(ctx context.Context, provider string) (*transport.ListEmbeddingModelsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.ListEmbeddingModels(ctx, &pb.ListEmbeddingModelsRequest{Provider: provider})
	if err != nil {
		return nil, mapGRPCError("ListEmbeddingModels", err)
	}
	models := make([]transport.EmbeddingModelInfo, len(resp.GetModels()))
	for i, m := range resp.GetModels() {
		models[i] = transport.EmbeddingModelInfo{
			Name:       m.GetName(),
			Dimensions: int(m.GetDimensions()),
			MaxTokens:  int(m.GetMaxTokens()),
		}
	}
	return &transport.ListEmbeddingModelsResponse{Models: models}, nil
}
