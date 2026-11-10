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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package quic

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// mockEmbeddingServiceQuic implements service.EmbeddingServicer.
type mockEmbeddingServiceQuic struct {
	embedFn      func(ctx context.Context, req *service.EmbedRequest) (*service.EmbedResponse, error)
	batchEmbedFn func(ctx context.Context, req *service.BatchEmbedRequest) (*service.BatchEmbedResponse, error)
	listModelsFn func(ctx context.Context, req *service.ListEmbeddingModelsRequest) (*service.ListEmbeddingModelsResponse, error)
}

func (m *mockEmbeddingServiceQuic) Embed(ctx context.Context, req *service.EmbedRequest) (*service.EmbedResponse, error) {
	if m.embedFn != nil {
		return m.embedFn(ctx, req)
	}
	return &service.EmbedResponse{
		Embeddings: [][]float32{{0.1, 0.2, 0.3}},
		Model:      req.Model,
		Usage:      &service.UsageInfo{PromptTokens: 4, TotalTokens: 4},
	}, nil
}

func (m *mockEmbeddingServiceQuic) BatchEmbed(ctx context.Context, req *service.BatchEmbedRequest) (*service.BatchEmbedResponse, error) {
	if m.batchEmbedFn != nil {
		return m.batchEmbedFn(ctx, req)
	}
	vecs := make([][]float32, len(req.Texts))
	for i := range vecs {
		vecs[i] = []float32{0.1, 0.2}
	}
	return &service.BatchEmbedResponse{Embeddings: vecs, Model: req.Model}, nil
}

func (m *mockEmbeddingServiceQuic) ListModels(ctx context.Context, req *service.ListEmbeddingModelsRequest) (*service.ListEmbeddingModelsResponse, error) {
	if m.listModelsFn != nil {
		return m.listModelsFn(ctx, req)
	}
	return &service.ListEmbeddingModelsResponse{
		Models: []service.ModelInfo{{Name: "all-minilm-l6-v2", Dimensions: 384, MaxTokens: 256}},
	}, nil
}

func (m *mockEmbeddingServiceQuic) RegisterProvider(_ string, _ service.EmbeddingProvider) error {
	return nil
}
func (m *mockEmbeddingServiceQuic) DeregisterProvider(_ string) error { return nil }
func (m *mockEmbeddingServiceQuic) Close() error                      { return nil }

// assertNotConfigured fails unless err is a *ServiceNotConfiguredError.
func assertNotConfigured(t *testing.T, err error) {
	t.Helper()
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T (%v)", err, err)
	}
}

// assertUnmarshalError fails unless err is an *UnmarshalError.
func assertUnmarshalError(t *testing.T, err error) {
	t.Helper()
	var uErr *UnmarshalError
	if !errors.As(err, &uErr) {
		t.Fatalf("expected *UnmarshalError, got %T (%v)", err, err)
	}
}

func TestHandleEmbed_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.EmbedRequest{Texts: []string{"hello"}})
	_, err := srv.handleEmbed(context.Background(), payload)
	assertNotConfigured(t, err)
}

func TestHandleEmbed_InvalidPayload(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0", WithEmbedding(&mockEmbeddingServiceQuic{}))
	_, err := srv.handleEmbed(context.Background(), []byte{0xff})
	assertUnmarshalError(t, err)
}

func TestHandleEmbed_Success(t *testing.T) {
	var got *service.EmbedRequest
	svc := &mockEmbeddingServiceQuic{
		embedFn: func(_ context.Context, req *service.EmbedRequest) (*service.EmbedResponse, error) {
			got = req
			return &service.EmbedResponse{
				Embeddings: [][]float32{{0.1, 0.2, 0.3}},
				Model:      req.Model,
				Usage:      &service.UsageInfo{PromptTokens: 3, TotalTokens: 5},
				Cached:     true,
			}, nil
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithEmbedding(svc))
	payload, _ := proto.Marshal(&pb.EmbedRequest{Texts: []string{"hello world"}, Model: "all-minilm", Provider: "ollama"})
	resp, err := srv.handleEmbed(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleEmbed failed: %v", err)
	}
	if got == nil || len(got.Texts) != 1 || got.Texts[0] != "hello world" || got.Model != "all-minilm" || got.Provider != "ollama" {
		t.Fatalf("service request = %+v", got)
	}
	var out pb.EmbedResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.GetEmbeddings()) != 1 || len(out.GetEmbeddings()[0].GetValues()) != 3 || out.GetEmbeddings()[0].GetValues()[2] != 0.3 {
		t.Fatalf("embeddings = %v", out.GetEmbeddings())
	}
	if out.GetModel() != "all-minilm" || !out.GetCached() {
		t.Fatalf("model=%q cached=%v", out.GetModel(), out.GetCached())
	}
	if out.GetUsage().GetPromptTokens() != 3 || out.GetUsage().GetTotalTokens() != 5 {
		t.Fatalf("usage = %+v", out.GetUsage())
	}
}

func TestHandleEmbed_ServiceError(t *testing.T) {
	sentinel := errors.New("embed failed")
	svc := &mockEmbeddingServiceQuic{
		embedFn: func(_ context.Context, _ *service.EmbedRequest) (*service.EmbedResponse, error) {
			return nil, sentinel
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithEmbedding(svc))
	payload, _ := proto.Marshal(&pb.EmbedRequest{Texts: []string{"x"}})
	if _, err := srv.handleEmbed(context.Background(), payload); !errors.Is(err, sentinel) {
		t.Fatalf("expected the service error, got %v", err)
	}
}

func TestHandleBatchEmbed_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.BatchEmbedRequest{Texts: []string{"x"}})
	_, err := srv.handleBatchEmbed(context.Background(), payload)
	assertNotConfigured(t, err)
}

func TestHandleBatchEmbed_InvalidPayload(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0", WithEmbedding(&mockEmbeddingServiceQuic{}))
	_, err := srv.handleBatchEmbed(context.Background(), []byte{0xff})
	assertUnmarshalError(t, err)
}

// TestHandleBatchEmbed_Success also pins the nil-usage conversion: the
// default mock returns no TotalUsage, which must stay nil on the wire.
func TestHandleBatchEmbed_Success(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0", WithEmbedding(&mockEmbeddingServiceQuic{}))
	payload, _ := proto.Marshal(&pb.BatchEmbedRequest{Texts: []string{"a", "b"}, Model: "m"})
	resp, err := srv.handleBatchEmbed(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleBatchEmbed failed: %v", err)
	}
	var out pb.BatchEmbedResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.GetEmbeddings()) != 2 || out.GetEmbeddings()[1].GetValues()[1] != 0.2 {
		t.Fatalf("embeddings = %v", out.GetEmbeddings())
	}
	if out.GetModel() != "m" {
		t.Fatalf("model = %q, want m", out.GetModel())
	}
	if out.GetTotalUsage() != nil {
		t.Fatalf("total usage = %+v, want nil", out.GetTotalUsage())
	}
}

func TestHandleBatchEmbed_ServiceError(t *testing.T) {
	sentinel := errors.New("batch failed")
	svc := &mockEmbeddingServiceQuic{
		batchEmbedFn: func(_ context.Context, _ *service.BatchEmbedRequest) (*service.BatchEmbedResponse, error) {
			return nil, sentinel
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithEmbedding(svc))
	payload, _ := proto.Marshal(&pb.BatchEmbedRequest{Texts: []string{"x"}})
	if _, err := srv.handleBatchEmbed(context.Background(), payload); !errors.Is(err, sentinel) {
		t.Fatalf("expected the service error, got %v", err)
	}
}

func TestHandleListEmbeddingModels_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.ListEmbeddingModelsRequest{})
	_, err := srv.handleListEmbeddingModels(context.Background(), payload)
	assertNotConfigured(t, err)
}

func TestHandleListEmbeddingModels_InvalidPayload(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0", WithEmbedding(&mockEmbeddingServiceQuic{}))
	_, err := srv.handleListEmbeddingModels(context.Background(), []byte{0xff})
	assertUnmarshalError(t, err)
}

func TestHandleListEmbeddingModels_Success(t *testing.T) {
	var gotProvider string
	svc := &mockEmbeddingServiceQuic{
		listModelsFn: func(_ context.Context, req *service.ListEmbeddingModelsRequest) (*service.ListEmbeddingModelsResponse, error) {
			gotProvider = req.Provider
			return &service.ListEmbeddingModelsResponse{
				Models: []service.ModelInfo{{Name: "all-minilm-l6-v2", Dimensions: 384, MaxTokens: 256}},
			}, nil
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithEmbedding(svc))
	payload, _ := proto.Marshal(&pb.ListEmbeddingModelsRequest{Provider: "ollama"})
	resp, err := srv.handleListEmbeddingModels(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleListEmbeddingModels failed: %v", err)
	}
	if gotProvider != "ollama" {
		t.Fatalf("provider = %q, want ollama", gotProvider)
	}
	var out pb.ListEmbeddingModelsResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.GetModels()) != 1 || out.GetModels()[0].GetName() != "all-minilm-l6-v2" || out.GetModels()[0].GetDimensions() != 384 {
		t.Fatalf("models = %v", out.GetModels())
	}
}

func TestHandleListEmbeddingModels_ServiceError(t *testing.T) {
	sentinel := errors.New("list failed")
	svc := &mockEmbeddingServiceQuic{
		listModelsFn: func(_ context.Context, _ *service.ListEmbeddingModelsRequest) (*service.ListEmbeddingModelsResponse, error) {
			return nil, sentinel
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithEmbedding(svc))
	payload, _ := proto.Marshal(&pb.ListEmbeddingModelsRequest{})
	if _, err := srv.handleListEmbeddingModels(context.Background(), payload); !errors.Is(err, sentinel) {
		t.Fatalf("expected the service error, got %v", err)
	}
}

func TestEmbeddingsToProtoQUIC_Empty(t *testing.T) {
	if out := embeddingsToProto(nil); len(out) != 0 {
		t.Fatalf("expected empty, got %d", len(out))
	}
}
