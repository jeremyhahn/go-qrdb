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

package quic

import (
	"context"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// embeddingsToProto converts a [][]float32 matrix to repeated EmbeddingVector.
func embeddingsToProto(vectors [][]float32) []*pb.EmbeddingVector {
	out := make([]*pb.EmbeddingVector, len(vectors))
	for i, v := range vectors {
		out[i] = &pb.EmbeddingVector{Values: v}
	}
	return out
}

// embeddingUsageToProto converts a service UsageInfo to its protobuf form.
func embeddingUsageToProto(u *service.UsageInfo) *pb.EmbeddingUsage {
	if u == nil {
		return nil
	}
	return &pb.EmbeddingUsage{
		PromptTokens: int32(u.PromptTokens),
		TotalTokens:  int32(u.TotalTokens),
	}
}

// handleEmbed generates embedding vectors for one or more texts.
func (s *Server) handleEmbed(ctx context.Context, payload []byte) ([]byte, error) {
	if s.embeddingSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "embedding"}
	}

	var req pb.EmbedRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	resp, err := s.embeddingSvc.Embed(ctx, &service.EmbedRequest{
		Texts:    req.GetTexts(),
		Model:    req.GetModel(),
		Provider: req.GetProvider(),
	})
	if err != nil {
		return nil, err
	}

	out, err := proto.Marshal(&pb.EmbedResponse{
		Embeddings: embeddingsToProto(resp.Embeddings),
		Model:      resp.Model,
		Usage:      embeddingUsageToProto(resp.Usage),
		Cached:     resp.Cached,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleBatchEmbed embeds a large set of texts in provider sub-batches.
func (s *Server) handleBatchEmbed(ctx context.Context, payload []byte) ([]byte, error) {
	if s.embeddingSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "embedding"}
	}

	var req pb.BatchEmbedRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	resp, err := s.embeddingSvc.BatchEmbed(ctx, &service.BatchEmbedRequest{
		Texts:     req.GetTexts(),
		Model:     req.GetModel(),
		Provider:  req.GetProvider(),
		BatchSize: int(req.GetBatchSize()),
	})
	if err != nil {
		return nil, err
	}

	out, err := proto.Marshal(&pb.BatchEmbedResponse{
		Embeddings: embeddingsToProto(resp.Embeddings),
		Model:      resp.Model,
		TotalUsage: embeddingUsageToProto(resp.TotalUsage),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleListEmbeddingModels returns the embedding models available from a provider.
func (s *Server) handleListEmbeddingModels(ctx context.Context, payload []byte) ([]byte, error) {
	if s.embeddingSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "embedding"}
	}

	var req pb.ListEmbeddingModelsRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	resp, err := s.embeddingSvc.ListModels(ctx, &service.ListEmbeddingModelsRequest{
		Provider: req.GetProvider(),
	})
	if err != nil {
		return nil, err
	}

	models := make([]*pb.EmbeddingModelInfo, len(resp.Models))
	for i := range resp.Models {
		models[i] = &pb.EmbeddingModelInfo{
			Name:       resp.Models[i].Name,
			Dimensions: int32(resp.Models[i].Dimensions),
			MaxTokens:  int32(resp.Models[i].MaxTokens),
		}
	}
	out, err := proto.Marshal(&pb.ListEmbeddingModelsResponse{Models: models})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}
