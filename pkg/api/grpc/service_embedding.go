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

package grpc

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Embedding conversion helpers
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Embedding handlers
// ---------------------------------------------------------------------------

// Embed generates embedding vectors for one or more texts.
func (s *qrdbServiceServer) Embed(ctx context.Context, req *pb.EmbedRequest) (*pb.EmbedResponse, error) {
	if s.embeddingSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "embedding"}).Error())
	}
	resp, err := s.embeddingSvc.Embed(ctx, &service.EmbedRequest{
		Texts:    req.GetTexts(),
		Model:    req.GetModel(),
		Provider: req.GetProvider(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.EmbedResponse{
		Embeddings: embeddingsToProto(resp.Embeddings),
		Model:      resp.Model,
		Usage:      embeddingUsageToProto(resp.Usage),
		Cached:     resp.Cached,
	}, nil
}

// BatchEmbed embeds a large set of texts in provider sub-batches.
func (s *qrdbServiceServer) BatchEmbed(ctx context.Context, req *pb.BatchEmbedRequest) (*pb.BatchEmbedResponse, error) {
	if s.embeddingSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "embedding"}).Error())
	}
	resp, err := s.embeddingSvc.BatchEmbed(ctx, &service.BatchEmbedRequest{
		Texts:     req.GetTexts(),
		Model:     req.GetModel(),
		Provider:  req.GetProvider(),
		BatchSize: int(req.GetBatchSize()),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.BatchEmbedResponse{
		Embeddings: embeddingsToProto(resp.Embeddings),
		Model:      resp.Model,
		TotalUsage: embeddingUsageToProto(resp.TotalUsage),
	}, nil
}

// ListEmbeddingModels returns the embedding models available from a provider.
func (s *qrdbServiceServer) ListEmbeddingModels(ctx context.Context, req *pb.ListEmbeddingModelsRequest) (*pb.ListEmbeddingModelsResponse, error) {
	if s.embeddingSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "embedding"}).Error())
	}
	resp, err := s.embeddingSvc.ListModels(ctx, &service.ListEmbeddingModelsRequest{
		Provider: req.GetProvider(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	models := make([]*pb.EmbeddingModelInfo, len(resp.Models))
	for i := range resp.Models {
		models[i] = &pb.EmbeddingModelInfo{
			Name:       resp.Models[i].Name,
			Dimensions: int32(resp.Models[i].Dimensions),
			MaxTokens:  int32(resp.Models[i].MaxTokens),
		}
	}
	return &pb.ListEmbeddingModelsResponse{Models: models}, nil
}
