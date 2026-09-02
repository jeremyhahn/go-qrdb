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

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// mock_embedding_service_test.go holds mockEmbeddingService, the package's
// shared service.EmbeddingServicer double.
//
// It carries NO build constraint on purpose. The double used to live in
// tools_embedding_test.go, which is `!integration`, while
// tool_mapping_guard_test.go consumes it under `codec_json && raft` -- a
// constraint that does not imply `!integration`. Under BUILD_TAGS+integration
// the type vanished and the package stopped typechecking (#203, the shape #178
// first hit). A test helper's constraint must be a SUPERSET of every caller's;
// the union here is unconstrained, so this file is too.

// The compile-time check belongs with the double: it is what turns "a method
// slipped behind a build constraint" into a failure HERE rather than at some
// construction site in another file (#203).
var _ service.EmbeddingServicer = (*mockEmbeddingService)(nil)

type mockEmbeddingService struct {
	embedFn      func(ctx context.Context, req *service.EmbedRequest) (*service.EmbedResponse, error)
	batchEmbedFn func(ctx context.Context, req *service.BatchEmbedRequest) (*service.BatchEmbedResponse, error)
	listModelsFn func(ctx context.Context, req *service.ListEmbeddingModelsRequest) (*service.ListEmbeddingModelsResponse, error)
}

func (m *mockEmbeddingService) Embed(ctx context.Context, req *service.EmbedRequest) (*service.EmbedResponse, error) {
	if m.embedFn != nil {
		return m.embedFn(ctx, req)
	}
	return &service.EmbedResponse{
		Embeddings: [][]float32{{0.1, 0.2, 0.3}},
		Model:      "text-embedding-3-small",
		Cached:     false,
	}, nil
}

func (m *mockEmbeddingService) BatchEmbed(ctx context.Context, req *service.BatchEmbedRequest) (*service.BatchEmbedResponse, error) {
	if m.batchEmbedFn != nil {
		return m.batchEmbedFn(ctx, req)
	}
	vecs := make([][]float32, len(req.Texts))
	for i := range req.Texts {
		vecs[i] = []float32{float32(i) * 0.1}
	}
	return &service.BatchEmbedResponse{
		Embeddings: vecs,
		Model:      "text-embedding-3-small",
	}, nil
}

func (m *mockEmbeddingService) ListModels(ctx context.Context, req *service.ListEmbeddingModelsRequest) (*service.ListEmbeddingModelsResponse, error) {
	if m.listModelsFn != nil {
		return m.listModelsFn(ctx, req)
	}
	return &service.ListEmbeddingModelsResponse{
		Models: []service.ModelInfo{
			{Name: "text-embedding-3-small", Dimensions: 1536, MaxTokens: 8191},
		},
	}, nil
}

func (m *mockEmbeddingService) RegisterProvider(name string, provider service.EmbeddingProvider) error {
	return nil
}

func (m *mockEmbeddingService) DeregisterProvider(name string) error {
	return nil
}

func (m *mockEmbeddingService) Close() error {
	return nil
}
