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

package grpc

import (
	"context"
	"errors"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Embedding handler tests (service_embedding.go – 0% covered)
// ---------------------------------------------------------------------------

// embeddingMockService is a controllable implementation of
// service.EmbeddingServicer for gRPC embedding handler tests.
type embeddingMockService struct {
	embedFn      func(ctx context.Context, req *service.EmbedRequest) (*service.EmbedResponse, error)
	batchEmbedFn func(ctx context.Context, req *service.BatchEmbedRequest) (*service.BatchEmbedResponse, error)
	listModelsFn func(ctx context.Context, req *service.ListEmbeddingModelsRequest) (*service.ListEmbeddingModelsResponse, error)
	registerFn   func(name string, p service.EmbeddingProvider) error
	deregisterFn func(name string) error
	closeErr     error
}

func (m *embeddingMockService) Embed(ctx context.Context, req *service.EmbedRequest) (*service.EmbedResponse, error) {
	if m.embedFn != nil {
		return m.embedFn(ctx, req)
	}
	return &service.EmbedResponse{
		Embeddings: [][]float32{{0.1, 0.2, 0.3}},
		Model:      req.Model,
		Usage:      &service.UsageInfo{PromptTokens: 4, TotalTokens: 4},
		Cached:     false,
	}, nil
}

func (m *embeddingMockService) BatchEmbed(ctx context.Context, req *service.BatchEmbedRequest) (*service.BatchEmbedResponse, error) {
	if m.batchEmbedFn != nil {
		return m.batchEmbedFn(ctx, req)
	}
	vectors := make([][]float32, len(req.Texts))
	for i := range vectors {
		vectors[i] = []float32{0.1, 0.2}
	}
	return &service.BatchEmbedResponse{
		Embeddings: vectors,
		Model:      req.Model,
		TotalUsage: &service.UsageInfo{PromptTokens: len(req.Texts) * 2, TotalTokens: len(req.Texts) * 2},
	}, nil
}

func (m *embeddingMockService) ListModels(ctx context.Context, req *service.ListEmbeddingModelsRequest) (*service.ListEmbeddingModelsResponse, error) {
	if m.listModelsFn != nil {
		return m.listModelsFn(ctx, req)
	}
	return &service.ListEmbeddingModelsResponse{
		Models: []service.ModelInfo{
			{Name: "all-minilm-l6-v2", Dimensions: 384, MaxTokens: 256},
		},
	}, nil
}

func (m *embeddingMockService) RegisterProvider(name string, p service.EmbeddingProvider) error {
	if m.registerFn != nil {
		return m.registerFn(name, p)
	}
	return nil
}

func (m *embeddingMockService) DeregisterProvider(name string) error {
	if m.deregisterFn != nil {
		return m.deregisterFn(name)
	}
	return nil
}

func (m *embeddingMockService) Close() error { return m.closeErr }

// ---------------------------------------------------------------------------
// Embed
// ---------------------------------------------------------------------------

func TestGRPC_Embed_Success(t *testing.T) {
	svc := &embeddingMockService{}
	client, cleanup := startTestServerFull(t, WithEmbedding(svc))
	defer cleanup()

	resp, err := client.Embed(context.Background(), &pb.EmbedRequest{
		Texts: []string{"hello world"},
		Model: "all-minilm",
	})
	if err != nil {
		t.Fatalf("Embed failed: %v", err)
	}
	if len(resp.GetEmbeddings()) != 1 {
		t.Fatalf("expected 1 embedding, got %d", len(resp.GetEmbeddings()))
	}
	if resp.GetModel() != "all-minilm" {
		t.Fatalf("expected model 'all-minilm', got %q", resp.GetModel())
	}
}

func TestGRPC_Embed_WithUsage(t *testing.T) {
	svc := &embeddingMockService{
		embedFn: func(_ context.Context, _ *service.EmbedRequest) (*service.EmbedResponse, error) {
			return &service.EmbedResponse{
				Embeddings: [][]float32{{0.1, 0.2}},
				Model:      "model-x",
				Usage:      &service.UsageInfo{PromptTokens: 10, TotalTokens: 10},
				Cached:     true,
			}, nil
		},
	}
	client, cleanup := startTestServerFull(t, WithEmbedding(svc))
	defer cleanup()

	resp, err := client.Embed(context.Background(), &pb.EmbedRequest{Texts: []string{"test"}})
	if err != nil {
		t.Fatalf("Embed failed: %v", err)
	}
	if resp.GetUsage().GetPromptTokens() != 10 {
		t.Errorf("expected prompt_tokens=10, got %d", resp.GetUsage().GetPromptTokens())
	}
	if !resp.GetCached() {
		t.Error("expected cached=true")
	}
}

func TestGRPC_Embed_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.Embed(context.Background(), &pb.EmbedRequest{Texts: []string{"x"}})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_Embed_Error(t *testing.T) {
	svc := &embeddingMockService{
		embedFn: func(_ context.Context, _ *service.EmbedRequest) (*service.EmbedResponse, error) {
			return nil, errors.New("embed failed")
		},
	}
	client, cleanup := startTestServerFull(t, WithEmbedding(svc))
	defer cleanup()

	_, err := client.Embed(context.Background(), &pb.EmbedRequest{Texts: []string{"x"}})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// BatchEmbed
// ---------------------------------------------------------------------------

func TestGRPC_BatchEmbed_Success(t *testing.T) {
	svc := &embeddingMockService{}
	client, cleanup := startTestServerFull(t, WithEmbedding(svc))
	defer cleanup()

	resp, err := client.BatchEmbed(context.Background(), &pb.BatchEmbedRequest{
		Texts:     []string{"text1", "text2"},
		Model:     "all-minilm",
		BatchSize: 32,
	})
	if err != nil {
		t.Fatalf("BatchEmbed failed: %v", err)
	}
	if len(resp.GetEmbeddings()) != 2 {
		t.Fatalf("expected 2 embeddings, got %d", len(resp.GetEmbeddings()))
	}
}

func TestGRPC_BatchEmbed_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.BatchEmbed(context.Background(), &pb.BatchEmbedRequest{Texts: []string{"x"}})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_BatchEmbed_Error(t *testing.T) {
	svc := &embeddingMockService{
		batchEmbedFn: func(_ context.Context, _ *service.BatchEmbedRequest) (*service.BatchEmbedResponse, error) {
			return nil, errors.New("batch embed failed")
		},
	}
	client, cleanup := startTestServerFull(t, WithEmbedding(svc))
	defer cleanup()

	_, err := client.BatchEmbed(context.Background(), &pb.BatchEmbedRequest{Texts: []string{"x"}})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// ListEmbeddingModels
// ---------------------------------------------------------------------------

func TestGRPC_ListEmbeddingModels_Success(t *testing.T) {
	svc := &embeddingMockService{}
	client, cleanup := startTestServerFull(t, WithEmbedding(svc))
	defer cleanup()

	resp, err := client.ListEmbeddingModels(context.Background(), &pb.ListEmbeddingModelsRequest{Provider: "ollama"})
	if err != nil {
		t.Fatalf("ListEmbeddingModels failed: %v", err)
	}
	if len(resp.GetModels()) != 1 {
		t.Fatalf("expected 1 model, got %d", len(resp.GetModels()))
	}
	if resp.GetModels()[0].GetName() != "all-minilm-l6-v2" {
		t.Fatalf("expected model name 'all-minilm-l6-v2', got %q", resp.GetModels()[0].GetName())
	}
}

func TestGRPC_ListEmbeddingModels_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.ListEmbeddingModels(context.Background(), &pb.ListEmbeddingModelsRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_ListEmbeddingModels_Error(t *testing.T) {
	svc := &embeddingMockService{
		listModelsFn: func(_ context.Context, _ *service.ListEmbeddingModelsRequest) (*service.ListEmbeddingModelsResponse, error) {
			return nil, errors.New("list models failed")
		},
	}
	client, cleanup := startTestServerFull(t, WithEmbedding(svc))
	defer cleanup()

	_, err := client.ListEmbeddingModels(context.Background(), &pb.ListEmbeddingModelsRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// embeddingsToProto / embeddingUsageToProto (direct unit tests)
// ---------------------------------------------------------------------------

func TestEmbeddingsToProto_Empty(t *testing.T) {
	out := embeddingsToProto(nil)
	if len(out) != 0 {
		t.Fatalf("expected empty slice, got %d", len(out))
	}
}

func TestEmbeddingsToProto_NonEmpty(t *testing.T) {
	vecs := [][]float32{{0.1, 0.2}, {0.3, 0.4}}
	out := embeddingsToProto(vecs)
	if len(out) != 2 {
		t.Fatalf("expected 2, got %d", len(out))
	}
	if out[0].Values[1] != 0.2 {
		t.Errorf("unexpected value: %f", out[0].Values[1])
	}
}

func TestEmbeddingUsageToProto_Nil(t *testing.T) {
	if got := embeddingUsageToProto(nil); got != nil {
		t.Fatalf("expected nil for nil input, got %v", got)
	}
}

func TestEmbeddingUsageToProto_NonNil(t *testing.T) {
	u := &service.UsageInfo{PromptTokens: 5, TotalTokens: 7}
	out := embeddingUsageToProto(u)
	if out.GetPromptTokens() != 5 {
		t.Errorf("expected prompt_tokens=5, got %d", out.GetPromptTokens())
	}
	if out.GetTotalTokens() != 7 {
		t.Errorf("expected total_tokens=7, got %d", out.GetTotalTokens())
	}
}

// ---------------------------------------------------------------------------
// AddCertBinding (service_auth.go – 0% covered)
// ---------------------------------------------------------------------------

func TestGRPC_AddCertBinding_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.AddCertBinding(context.Background(), &pb.AddCertBindingRequest{
		UserId:       42,
		Thumbprint:   "sha256:abc123",
		NotAfterUnix: 1800000000,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// EnrollFactorUnary / ChallengeFactorUnary / VerifyFactorUnary /
// ListFactorsUnary / RevokeFactorUnary (authfactor_server.go – 0% covered)
//
// These are client-side SDK helpers. We test them via an in-process server
// that has RegisterAuthFactorService wired, verifying the full request/response
// round-trip for the happy path and the connection-error path.
// ---------------------------------------------------------------------------

// startAuthFactorServer starts an in-process gRPC server with the auth factor
// service registered and returns a connected ClientConn + cleanup.
func startAuthFactorServer(t *testing.T, svc *mockAuthFactorService) (*grpc.ClientConn, func()) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	srv := grpc.NewServer()
	RegisterAuthFactorService(srv, svc)
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		srv.Stop()
		t.Fatalf("grpc.NewClient: %v", err)
	}
	return conn, func() {
		_ = conn.Close()
		srv.Stop()
	}
}

func TestEnrollFactorUnary_Success(t *testing.T) {
	svc := newMockAuthFactorService()
	conn, cleanup := startAuthFactorServer(t, svc)
	defer cleanup()

	resp, err := EnrollFactorUnary(context.Background(), conn, &grpcEnrollFactorRequest{
		FactorName: "pkcs11",
		Subject:    "alice",
		TenantID:   "tenant-1",
	})
	if err != nil {
		t.Fatalf("EnrollFactorUnary failed: %v", err)
	}
	if resp.EnrollmentID != "enroll-1" {
		t.Fatalf("expected enrollment_id 'enroll-1', got %q", resp.EnrollmentID)
	}
}

func TestEnrollFactorUnary_ConnectionError(t *testing.T) {
	// Use a closed connection to trigger the conn.Invoke error path.
	conn, err := grpc.NewClient("127.0.0.1:1",
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 0) // immediate deadline
	cancel()
	_, err = EnrollFactorUnary(ctx, conn, &grpcEnrollFactorRequest{FactorName: "piv", Subject: "bob"})
	if err == nil {
		t.Fatal("expected error from dead connection, got nil")
	}
}

func TestChallengeFactorUnary_Success(t *testing.T) {
	svc := newMockAuthFactorService()
	conn, cleanup := startAuthFactorServer(t, svc)
	defer cleanup()

	resp, err := ChallengeFactorUnary(context.Background(), conn, &grpcChallengeFactorRequest{
		EnrollmentID: "enroll-1",
	})
	if err != nil {
		t.Fatalf("ChallengeFactorUnary failed: %v", err)
	}
	if resp.ChallengeID != "challenge-1" {
		t.Fatalf("expected challenge_id 'challenge-1', got %q", resp.ChallengeID)
	}
}

func TestChallengeFactorUnary_ConnectionError(t *testing.T) {
	conn, err := grpc.NewClient("127.0.0.1:1",
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 0)
	cancel()
	_, err = ChallengeFactorUnary(ctx, conn, &grpcChallengeFactorRequest{EnrollmentID: "x"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestVerifyFactorUnary_Success(t *testing.T) {
	svc := newMockAuthFactorService()
	conn, cleanup := startAuthFactorServer(t, svc)
	defer cleanup()

	err := VerifyFactorUnary(context.Background(), conn, &grpcVerifyFactorRequest{
		ChallengeID: "challenge-1",
		Value:       []byte("sig"),
	})
	if err != nil {
		t.Fatalf("VerifyFactorUnary failed: %v", err)
	}
}

func TestVerifyFactorUnary_ConnectionError(t *testing.T) {
	conn, err := grpc.NewClient("127.0.0.1:1",
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 0)
	cancel()
	err = VerifyFactorUnary(ctx, conn, &grpcVerifyFactorRequest{ChallengeID: "c"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestListFactorsUnary_Success(t *testing.T) {
	svc := newMockAuthFactorService()
	conn, cleanup := startAuthFactorServer(t, svc)
	defer cleanup()

	resp, err := ListFactorsUnary(context.Background(), conn, &grpcListFactorsRequest{Subject: "alice"})
	if err != nil {
		t.Fatalf("ListFactorsUnary failed: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("expected total=1, got %d", resp.Total)
	}
}

func TestListFactorsUnary_ConnectionError(t *testing.T) {
	conn, err := grpc.NewClient("127.0.0.1:1",
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 0)
	cancel()
	_, err = ListFactorsUnary(ctx, conn, &grpcListFactorsRequest{Subject: "x"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestRevokeFactorUnary_Success(t *testing.T) {
	svc := newMockAuthFactorService()
	conn, cleanup := startAuthFactorServer(t, svc)
	defer cleanup()

	err := RevokeFactorUnary(context.Background(), conn, &grpcRevokeFactorRequest{EnrollmentID: "enroll-1"})
	if err != nil {
		t.Fatalf("RevokeFactorUnary failed: %v", err)
	}
}

func TestRevokeFactorUnary_ConnectionError(t *testing.T) {
	conn, err := grpc.NewClient("127.0.0.1:1",
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 0)
	cancel()
	err = RevokeFactorUnary(ctx, conn, &grpcRevokeFactorRequest{EnrollmentID: "e"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// RegisterAuthFactorService with nil (0% covered)
// ---------------------------------------------------------------------------

func TestRegisterAuthFactorService_NilService(t *testing.T) {
	srv := grpc.NewServer()
	// Must not panic; nil means the service is not registered.
	RegisterAuthFactorService(srv, nil)
	srv.Stop()
}

func TestRegisterAuthFactorService_NonNil(t *testing.T) {
	srv := grpc.NewServer()
	RegisterAuthFactorService(srv, newMockAuthFactorService())
	srv.Stop()
}
