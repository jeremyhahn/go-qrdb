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

// Round-trip tests for the Embedding surface (transport.EmbeddingClient),
// closing the unix-vs-grpc SDK transport parity gap.

package unix

import (
	"context"
	"testing"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// mockEmbeddingSvc
// ---------------------------------------------------------------------------

type mockEmbeddingSvc struct {
	embedFn      func(ctx context.Context, req *service.EmbedRequest) (*service.EmbedResponse, error)
	batchFn      func(ctx context.Context, req *service.BatchEmbedRequest) (*service.BatchEmbedResponse, error)
	listModelsFn func(ctx context.Context, req *service.ListEmbeddingModelsRequest) (*service.ListEmbeddingModelsResponse, error)
}

var _ service.EmbeddingServicer = (*mockEmbeddingSvc)(nil)

func (m *mockEmbeddingSvc) Embed(ctx context.Context, req *service.EmbedRequest) (*service.EmbedResponse, error) {
	if m.embedFn != nil {
		return m.embedFn(ctx, req)
	}
	return &service.EmbedResponse{
		Embeddings: [][]float32{{0.1, 0.2, 0.3}},
		Model:      "text-embedding-3-small",
		Usage:      &service.UsageInfo{PromptTokens: 3, TotalTokens: 3},
	}, nil
}

func (m *mockEmbeddingSvc) BatchEmbed(ctx context.Context, req *service.BatchEmbedRequest) (*service.BatchEmbedResponse, error) {
	if m.batchFn != nil {
		return m.batchFn(ctx, req)
	}
	return &service.BatchEmbedResponse{
		Embeddings: [][]float32{{0.1, 0.2}, {0.3, 0.4}},
		Model:      "text-embedding-3-small",
		TotalUsage: &service.UsageInfo{PromptTokens: 6, TotalTokens: 6},
	}, nil
}

func (m *mockEmbeddingSvc) ListModels(ctx context.Context, req *service.ListEmbeddingModelsRequest) (*service.ListEmbeddingModelsResponse, error) {
	if m.listModelsFn != nil {
		return m.listModelsFn(ctx, req)
	}
	return &service.ListEmbeddingModelsResponse{
		Models: []service.ModelInfo{{Name: "text-embedding-3-small", Dimensions: 1536, MaxTokens: 8191}},
	}, nil
}

func (m *mockEmbeddingSvc) RegisterProvider(_ string, _ service.EmbeddingProvider) error { return nil }
func (m *mockEmbeddingSvc) DeregisterProvider(_ string) error                            { return nil }
func (m *mockEmbeddingSvc) Close() error                                                 { return nil }

// ---------------------------------------------------------------------------
// Embed
// ---------------------------------------------------------------------------

func TestUnixRT_Embed(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithEmbedding(&mockEmbeddingSvc{}))
	defer cleanup()

	resp, err := tr.Embed(context.Background(), &transport.EmbedRequest{Texts: []string{"hello"}})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(resp.Embeddings) != 1 || resp.Model != "text-embedding-3-small" {
		t.Errorf("got %+v, want 1 embedding for text-embedding-3-small", resp)
	}
}

func TestUnixRT_Embed_Error(t *testing.T) {
	svc := &mockEmbeddingSvc{embedFn: func(_ context.Context, _ *service.EmbedRequest) (*service.EmbedResponse, error) {
		return nil, errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithEmbedding(svc))
	defer cleanup()

	if _, err := tr.Embed(context.Background(), &transport.EmbedRequest{Texts: []string{"hello"}}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// BatchEmbed
// ---------------------------------------------------------------------------

func TestUnixRT_BatchEmbed(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithEmbedding(&mockEmbeddingSvc{}))
	defer cleanup()

	resp, err := tr.BatchEmbed(context.Background(), &transport.BatchEmbedRequest{Texts: []string{"a", "b"}})
	if err != nil {
		t.Fatalf("BatchEmbed: %v", err)
	}
	if len(resp.Embeddings) != 2 {
		t.Errorf("got %+v, want 2 embeddings", resp)
	}
}

func TestUnixRT_BatchEmbed_Error(t *testing.T) {
	svc := &mockEmbeddingSvc{batchFn: func(_ context.Context, _ *service.BatchEmbedRequest) (*service.BatchEmbedResponse, error) {
		return nil, errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithEmbedding(svc))
	defer cleanup()

	if _, err := tr.BatchEmbed(context.Background(), &transport.BatchEmbedRequest{Texts: []string{"a"}}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// ListEmbeddingModels
// ---------------------------------------------------------------------------

func TestUnixRT_ListEmbeddingModels(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithEmbedding(&mockEmbeddingSvc{}))
	defer cleanup()

	resp, err := tr.ListEmbeddingModels(context.Background(), "")
	if err != nil {
		t.Fatalf("ListEmbeddingModels: %v", err)
	}
	if len(resp.Models) != 1 || resp.Models[0].Name != "text-embedding-3-small" {
		t.Errorf("got %+v, want 1 model text-embedding-3-small", resp)
	}
}

func TestUnixRT_ListEmbeddingModels_Error(t *testing.T) {
	svc := &mockEmbeddingSvc{listModelsFn: func(_ context.Context, _ *service.ListEmbeddingModelsRequest) (*service.ListEmbeddingModelsResponse, error) {
		return nil, errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithEmbedding(svc))
	defer cleanup()

	if _, err := tr.ListEmbeddingModels(context.Background(), "openai"); err == nil {
		t.Fatal("expected error")
	}
}
