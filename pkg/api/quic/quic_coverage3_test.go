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
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// mockEmbeddingServiceQuic implements service.EmbeddingServicer
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// mockRAGServiceQuic implements service.RAGServicer
// ---------------------------------------------------------------------------

type mockRAGServiceQuic struct {
	ingestFn func(ctx context.Context, req *service.IngestDocumentRequest) (*service.IngestDocumentResponse, error)
	queryFn  func(ctx context.Context, req *service.RAGQueryRequest) (*service.RAGQueryResponse, error)
	deleteFn func(ctx context.Context, req *service.DeleteRAGDocumentRequest) (*service.DeleteRAGDocumentResponse, error)
	listFn   func(ctx context.Context, req *service.ListRAGDocumentsRequest) (*service.ListRAGDocumentsResponse, error)
}

func (m *mockRAGServiceQuic) IngestDocument(ctx context.Context, req *service.IngestDocumentRequest) (*service.IngestDocumentResponse, error) {
	if m.ingestFn != nil {
		return m.ingestFn(ctx, req)
	}
	return &service.IngestDocumentResponse{DocumentID: req.DocumentID, ChunkCount: 3, Namespace: req.Namespace}, nil
}

func (m *mockRAGServiceQuic) Query(ctx context.Context, req *service.RAGQueryRequest) (*service.RAGQueryResponse, error) {
	if m.queryFn != nil {
		return m.queryFn(ctx, req)
	}
	return &service.RAGQueryResponse{
		Results: []*service.RAGQueryResult{
			{ChunkText: "chunk text", Score: 0.9, DocumentID: "doc1"},
		},
	}, nil
}

func (m *mockRAGServiceQuic) DeleteDocument(ctx context.Context, req *service.DeleteRAGDocumentRequest) (*service.DeleteRAGDocumentResponse, error) {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, req)
	}
	return &service.DeleteRAGDocumentResponse{DocumentID: req.DocumentID, ChunksRemoved: 3}, nil
}

func (m *mockRAGServiceQuic) ListDocuments(ctx context.Context, req *service.ListRAGDocumentsRequest) (*service.ListRAGDocumentsResponse, error) {
	if m.listFn != nil {
		return m.listFn(ctx, req)
	}
	return &service.ListRAGDocumentsResponse{
		Documents: []*service.RAGDocumentInfo{{ID: "doc1", Namespace: req.Namespace, ChunkCount: 3}},
		Total:     1,
	}, nil
}

func (m *mockRAGServiceQuic) Close() error { return nil }

// ---------------------------------------------------------------------------
// mockRebalanceServiceQuic implements service.RebalanceServicer
// ---------------------------------------------------------------------------

type mockRebalanceServiceQuic struct {
	statsFn   func(ctx context.Context, shardID uint64) (*service.ShardStats, error)
	planFn    func(ctx context.Context, opts *service.RebalancePlanOptions) (*service.RebalancePlanResult, error)
	executeFn func(ctx context.Context, plan *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error)
	reportFn  func(ctx context.Context) (*service.ImbalanceReportResult, error)
}

func (m *mockRebalanceServiceQuic) GetShardStats(ctx context.Context, shardID uint64) (*service.ShardStats, error) {
	if m.statsFn != nil {
		return m.statsFn(ctx, shardID)
	}
	return &service.ShardStats{ShardID: shardID, KeyCount: 100, ByteCount: 4096, LeaderID: 1, ReplicaCount: 3}, nil
}

func (m *mockRebalanceServiceQuic) CreateRebalancePlan(ctx context.Context, opts *service.RebalancePlanOptions) (*service.RebalancePlanResult, error) {
	if m.planFn != nil {
		return m.planFn(ctx, opts)
	}
	return &service.RebalancePlanResult{
		Moves: []service.RebalanceMovePlan{{ShardID: 1, FromNode: "a", ToNode: "b", EstimatedBytes: 512}},
	}, nil
}

func (m *mockRebalanceServiceQuic) ExecuteRebalance(ctx context.Context, plan *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error) {
	if m.executeFn != nil {
		return m.executeFn(ctx, plan)
	}
	return &service.RebalanceExecutionResult{MovesCompleted: len(plan.Moves), MovesFailed: 0}, nil
}

func (m *mockRebalanceServiceQuic) GetImbalanceReport(ctx context.Context) (*service.ImbalanceReportResult, error) {
	if m.reportFn != nil {
		return m.reportFn(ctx)
	}
	return &service.ImbalanceReportResult{
		Nodes:          []service.NodeLoadInfo{{NodeID: "n1", ShardCount: 3}},
		ImbalanceScore: 0.2,
	}, nil
}

// ---------------------------------------------------------------------------
// mockDatabaseServiceQuic implements service.DatabaseServicer
// ---------------------------------------------------------------------------

type mockDatabaseServiceQuic struct {
	sealFn          func(ctx context.Context, name string) error
	unsealFn        func(ctx context.Context, name string, creds seal.Credentials) error
	barrierStatusFn func(name string) (*seal.BarrierStatus, error)
}

func (m *mockDatabaseServiceQuic) Create(_ database.DatabaseConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{}, nil
}
func (m *mockDatabaseServiceQuic) Get(_ string) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{}, nil
}
func (m *mockDatabaseServiceQuic) List() ([]database.DatabaseInfo, error) {
	return []database.DatabaseInfo{}, nil
}
func (m *mockDatabaseServiceQuic) Drop(_ context.Context, _ string) error { return nil }
func (m *mockDatabaseServiceQuic) UpdateProtocol(_ string, _ database.ProtocolConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{}, nil
}
func (m *mockDatabaseServiceQuic) UpdateEtcd(_ string, _ *database.EtcdConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{}, nil
}
func (m *mockDatabaseServiceQuic) UpdateRaft(_ string, _ *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{}, nil
}
func (m *mockDatabaseServiceQuic) UpdateBarrier(_ string, _ *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{}, nil
}
func (m *mockDatabaseServiceQuic) UpdateConnection(_ string, _ *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{}, nil
}
func (m *mockDatabaseServiceQuic) SealDatabase(ctx context.Context, name string) error {
	if m.sealFn != nil {
		return m.sealFn(ctx, name)
	}
	return nil
}
func (m *mockDatabaseServiceQuic) UnsealDatabase(ctx context.Context, name string, creds seal.Credentials) error {
	if m.unsealFn != nil {
		return m.unsealFn(ctx, name, creds)
	}
	return nil
}
func (m *mockDatabaseServiceQuic) DatabaseBarrierStatus(_ context.Context, name string) (*seal.BarrierStatus, error) {
	if m.barrierStatusFn != nil {
		return m.barrierStatusFn(name)
	}
	return &seal.BarrierStatus{Sealed: false, Strategy: "software"}, nil
}
func (m *mockDatabaseServiceQuic) CreateForTenant(ctx context.Context, cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
	return m.Create(cfg)
}
func (m *mockDatabaseServiceQuic) GetForTenant(ctx context.Context, name string) (*database.DatabaseInfo, error) {
	return m.Get(name)
}
func (m *mockDatabaseServiceQuic) ListForTenant(ctx context.Context) ([]database.DatabaseInfo, error) {
	return m.List()
}
func (m *mockDatabaseServiceQuic) DropForTenant(ctx context.Context, name string) error {
	return m.Drop(context.Background(), name)
}
func (m *mockDatabaseServiceQuic) UpdateProtocolForTenant(ctx context.Context, name string, protocol database.ProtocolConfig) (*database.DatabaseInfo, error) {
	return m.UpdateProtocol(name, protocol)
}
func (m *mockDatabaseServiceQuic) UpdateEtcdForTenant(ctx context.Context, name string, etcd *database.EtcdConfig) (*database.DatabaseInfo, error) {
	return m.UpdateEtcd(name, etcd)
}
func (m *mockDatabaseServiceQuic) UpdateRaftForTenant(ctx context.Context, name string, raft *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	return m.UpdateRaft(name, raft)
}
func (m *mockDatabaseServiceQuic) UpdateBarrierForTenant(ctx context.Context, name string, barrier *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	return m.UpdateBarrier(name, barrier)
}
func (m *mockDatabaseServiceQuic) UpdateConnectionForTenant(ctx context.Context, name string, conn *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	return m.UpdateConnection(name, conn)
}

func (m *mockDatabaseServiceQuic) UpdateSQL(name string, sqlCfg *database.SQLConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: name, SQL: sqlCfg}, nil
}

func (m *mockDatabaseServiceQuic) UpdateSQLForTenant(_ context.Context, name string, sqlCfg *database.SQLConfig) (*database.DatabaseInfo, error) {
	return m.UpdateSQL(name, sqlCfg)
}
func (m *mockDatabaseServiceQuic) ScaleForTenant(_ context.Context, name string, targetReplicas int) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: name, Replicas: targetReplicas}, nil
}

// ---------------------------------------------------------------------------
// handleEmbed tests
// ---------------------------------------------------------------------------

func TestHandleEmbed_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.EmbedRequest{Texts: []string{"hello"}})
	_, err := srv.handleEmbed(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleEmbed_InvalidPayload(t *testing.T) {
	svc := &mockEmbeddingServiceQuic{}
	srv := NewServer(nil, nil, "localhost:0", WithEmbedding(svc))
	_, err := srv.handleEmbed(context.Background(), []byte{0xff})
	if err == nil {
		t.Fatal("expected UnmarshalError")
	}
}

func TestHandleEmbed_Success(t *testing.T) {
	svc := &mockEmbeddingServiceQuic{}
	srv := NewServer(nil, nil, "localhost:0", WithEmbedding(svc))
	payload, _ := proto.Marshal(&pb.EmbedRequest{Texts: []string{"hello world"}, Model: "all-minilm"})
	resp, err := srv.handleEmbed(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleEmbed failed: %v", err)
	}
	var out pb.EmbedResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.GetEmbeddings()) != 1 {
		t.Fatalf("expected 1 embedding, got %d", len(out.GetEmbeddings()))
	}
}

func TestHandleEmbed_ServiceError(t *testing.T) {
	svc := &mockEmbeddingServiceQuic{
		embedFn: func(_ context.Context, _ *service.EmbedRequest) (*service.EmbedResponse, error) {
			return nil, errors.New("embed failed")
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithEmbedding(svc))
	payload, _ := proto.Marshal(&pb.EmbedRequest{Texts: []string{"x"}})
	_, err := srv.handleEmbed(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// handleBatchEmbed tests
// ---------------------------------------------------------------------------

func TestHandleBatchEmbed_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.BatchEmbedRequest{Texts: []string{"x"}})
	_, err := srv.handleBatchEmbed(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
}

func TestHandleBatchEmbed_InvalidPayload(t *testing.T) {
	svc := &mockEmbeddingServiceQuic{}
	srv := NewServer(nil, nil, "localhost:0", WithEmbedding(svc))
	_, err := srv.handleBatchEmbed(context.Background(), []byte{0xff})
	if err == nil {
		t.Fatal("expected UnmarshalError")
	}
}

func TestHandleBatchEmbed_Success(t *testing.T) {
	svc := &mockEmbeddingServiceQuic{}
	srv := NewServer(nil, nil, "localhost:0", WithEmbedding(svc))
	payload, _ := proto.Marshal(&pb.BatchEmbedRequest{Texts: []string{"a", "b"}, Model: "m"})
	resp, err := srv.handleBatchEmbed(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleBatchEmbed failed: %v", err)
	}
	var out pb.BatchEmbedResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.GetEmbeddings()) != 2 {
		t.Fatalf("expected 2 embeddings, got %d", len(out.GetEmbeddings()))
	}
}

func TestHandleBatchEmbed_ServiceError(t *testing.T) {
	svc := &mockEmbeddingServiceQuic{
		batchEmbedFn: func(_ context.Context, _ *service.BatchEmbedRequest) (*service.BatchEmbedResponse, error) {
			return nil, errors.New("batch failed")
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithEmbedding(svc))
	payload, _ := proto.Marshal(&pb.BatchEmbedRequest{Texts: []string{"x"}})
	_, err := srv.handleBatchEmbed(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// handleListEmbeddingModels tests
// ---------------------------------------------------------------------------

func TestHandleListEmbeddingModels_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.ListEmbeddingModelsRequest{})
	_, err := srv.handleListEmbeddingModels(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
}

func TestHandleListEmbeddingModels_InvalidPayload(t *testing.T) {
	svc := &mockEmbeddingServiceQuic{}
	srv := NewServer(nil, nil, "localhost:0", WithEmbedding(svc))
	_, err := srv.handleListEmbeddingModels(context.Background(), []byte{0xff})
	if err == nil {
		t.Fatal("expected UnmarshalError")
	}
}

func TestHandleListEmbeddingModels_Success(t *testing.T) {
	svc := &mockEmbeddingServiceQuic{}
	srv := NewServer(nil, nil, "localhost:0", WithEmbedding(svc))
	payload, _ := proto.Marshal(&pb.ListEmbeddingModelsRequest{Provider: "ollama"})
	resp, err := srv.handleListEmbeddingModels(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleListEmbeddingModels failed: %v", err)
	}
	var out pb.ListEmbeddingModelsResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.GetModels()) != 1 {
		t.Fatalf("expected 1 model, got %d", len(out.GetModels()))
	}
}

func TestHandleListEmbeddingModels_ServiceError(t *testing.T) {
	svc := &mockEmbeddingServiceQuic{
		listModelsFn: func(_ context.Context, _ *service.ListEmbeddingModelsRequest) (*service.ListEmbeddingModelsResponse, error) {
			return nil, errors.New("list failed")
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithEmbedding(svc))
	payload, _ := proto.Marshal(&pb.ListEmbeddingModelsRequest{})
	_, err := srv.handleListEmbeddingModels(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// embeddingsToProto / embeddingUsageToProto (direct unit tests for QUIC pkg)
// ---------------------------------------------------------------------------

func TestEmbeddingsToProtoQUIC_Empty(t *testing.T) {
	out := embeddingsToProto(nil)
	if len(out) != 0 {
		t.Fatalf("expected empty, got %d", len(out))
	}
}

func TestEmbeddingsToProtoQUIC_Values(t *testing.T) {
	vecs := [][]float32{{0.1, 0.2}, {0.3, 0.4}}
	out := embeddingsToProto(vecs)
	if len(out) != 2 {
		t.Fatalf("expected 2, got %d", len(out))
	}
}

func TestEmbeddingUsageToProtoQUIC_Nil(t *testing.T) {
	if got := embeddingUsageToProto(nil); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestEmbeddingUsageToProtoQUIC_Values(t *testing.T) {
	u := &service.UsageInfo{PromptTokens: 3, TotalTokens: 5}
	out := embeddingUsageToProto(u)
	if out.GetPromptTokens() != 3 || out.GetTotalTokens() != 5 {
		t.Errorf("unexpected values: %+v", out)
	}
}

// ---------------------------------------------------------------------------
// RAG handler tests
// ---------------------------------------------------------------------------

func TestProtoToChunkConfigQUIC_Nil(t *testing.T) {
	if got := protoToChunkConfig(nil); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestProtoToChunkConfigQUIC_Full(t *testing.T) {
	cfg := protoToChunkConfig(&pb.ChunkConfig{
		Strategy:  "sentence",
		ChunkSize: 512,
		Overlap:   64,
		MinSize:   128,
	})
	if cfg.Strategy != "sentence" {
		t.Errorf("expected strategy 'sentence', got %q", cfg.Strategy)
	}
}

func TestHandleRAGIngestDocument_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.RAGIngestDocumentRequest{DocumentId: "d"})
	_, err := srv.handleRAGIngestDocument(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleRAGIngestDocument_InvalidPayload(t *testing.T) {
	svc := &mockRAGServiceQuic{}
	srv := NewServer(nil, nil, "localhost:0", WithRAGService(svc))
	_, err := srv.handleRAGIngestDocument(context.Background(), []byte{0xff})
	if err == nil {
		t.Fatal("expected UnmarshalError")
	}
}

func TestHandleRAGIngestDocument_Success(t *testing.T) {
	svc := &mockRAGServiceQuic{}
	srv := NewServer(nil, nil, "localhost:0", WithRAGService(svc))
	payload, _ := proto.Marshal(&pb.RAGIngestDocumentRequest{
		DocumentId: "doc1",
		Text:       "hello world",
		Namespace:  "ns1",
	})
	resp, err := srv.handleRAGIngestDocument(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleRAGIngestDocument failed: %v", err)
	}
	var out pb.RAGIngestDocumentResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.GetDocumentId() != "doc1" {
		t.Fatalf("expected doc_id 'doc1', got %q", out.GetDocumentId())
	}
}

func TestHandleRAGIngestDocument_WithChunkConfig(t *testing.T) {
	var captured *service.IngestDocumentRequest
	svc := &mockRAGServiceQuic{
		ingestFn: func(_ context.Context, req *service.IngestDocumentRequest) (*service.IngestDocumentResponse, error) {
			captured = req
			return &service.IngestDocumentResponse{DocumentID: req.DocumentID, ChunkCount: 1}, nil
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithRAGService(svc))
	payload, _ := proto.Marshal(&pb.RAGIngestDocumentRequest{
		DocumentId: "d1",
		Text:       "text",
		ChunkConfig: &pb.ChunkConfig{
			Strategy:  "fixed",
			ChunkSize: 256,
		},
	})
	_, err := srv.handleRAGIngestDocument(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured.ChunkConfig == nil {
		t.Fatal("expected ChunkConfig to be set")
	}
}

func TestHandleRAGIngestDocument_ServiceError(t *testing.T) {
	svc := &mockRAGServiceQuic{
		ingestFn: func(_ context.Context, _ *service.IngestDocumentRequest) (*service.IngestDocumentResponse, error) {
			return nil, errors.New("ingest failed")
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithRAGService(svc))
	payload, _ := proto.Marshal(&pb.RAGIngestDocumentRequest{DocumentId: "d"})
	_, err := srv.handleRAGIngestDocument(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestHandleRAGQuery_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.RAGQueryRequest{Query: "q"})
	_, err := srv.handleRAGQuery(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
}

func TestHandleRAGQuery_Success(t *testing.T) {
	svc := &mockRAGServiceQuic{}
	srv := NewServer(nil, nil, "localhost:0", WithRAGService(svc))
	payload, _ := proto.Marshal(&pb.RAGQueryRequest{Query: "what is qrdb", Namespace: "ns1", TopK: 5})
	resp, err := srv.handleRAGQuery(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleRAGQuery failed: %v", err)
	}
	var out pb.RAGQueryResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.GetResults()) != 1 {
		t.Fatalf("expected 1 result, got %d", len(out.GetResults()))
	}
}

func TestHandleRAGQuery_ServiceError(t *testing.T) {
	svc := &mockRAGServiceQuic{
		queryFn: func(_ context.Context, _ *service.RAGQueryRequest) (*service.RAGQueryResponse, error) {
			return nil, errors.New("query failed")
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithRAGService(svc))
	payload, _ := proto.Marshal(&pb.RAGQueryRequest{Query: "q"})
	_, err := srv.handleRAGQuery(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestHandleRAGDeleteDocument_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.RAGDeleteDocumentRequest{DocumentId: "d"})
	_, err := srv.handleRAGDeleteDocument(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
}

func TestHandleRAGDeleteDocument_Success(t *testing.T) {
	svc := &mockRAGServiceQuic{}
	srv := NewServer(nil, nil, "localhost:0", WithRAGService(svc))
	payload, _ := proto.Marshal(&pb.RAGDeleteDocumentRequest{DocumentId: "doc1", Namespace: "ns1"})
	resp, err := srv.handleRAGDeleteDocument(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleRAGDeleteDocument failed: %v", err)
	}
	var out pb.RAGDeleteDocumentResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.GetDocumentId() != "doc1" {
		t.Fatalf("expected doc_id 'doc1', got %q", out.GetDocumentId())
	}
}

func TestHandleRAGDeleteDocument_ServiceError(t *testing.T) {
	svc := &mockRAGServiceQuic{
		deleteFn: func(_ context.Context, _ *service.DeleteRAGDocumentRequest) (*service.DeleteRAGDocumentResponse, error) {
			return nil, errors.New("delete failed")
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithRAGService(svc))
	payload, _ := proto.Marshal(&pb.RAGDeleteDocumentRequest{DocumentId: "d"})
	_, err := srv.handleRAGDeleteDocument(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestHandleRAGListDocuments_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.RAGListDocumentsRequest{Namespace: "ns"})
	_, err := srv.handleRAGListDocuments(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
}

func TestHandleRAGListDocuments_Success(t *testing.T) {
	svc := &mockRAGServiceQuic{}
	srv := NewServer(nil, nil, "localhost:0", WithRAGService(svc))
	payload, _ := proto.Marshal(&pb.RAGListDocumentsRequest{Namespace: "ns1", Limit: 10})
	resp, err := srv.handleRAGListDocuments(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleRAGListDocuments failed: %v", err)
	}
	var out pb.RAGListDocumentsResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.GetDocuments()) != 1 {
		t.Fatalf("expected 1 document, got %d", len(out.GetDocuments()))
	}
}

func TestHandleRAGListDocuments_ServiceError(t *testing.T) {
	svc := &mockRAGServiceQuic{
		listFn: func(_ context.Context, _ *service.ListRAGDocumentsRequest) (*service.ListRAGDocumentsResponse, error) {
			return nil, errors.New("list failed")
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithRAGService(svc))
	payload, _ := proto.Marshal(&pb.RAGListDocumentsRequest{Namespace: "ns"})
	_, err := srv.handleRAGListDocuments(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// Rebalance handler tests
// ---------------------------------------------------------------------------

func TestRebalancePlanToProtoQUIC_Nil(t *testing.T) {
	if got := rebalancePlanToProto(nil); got != nil {
		t.Fatalf("expected nil for nil input, got %v", got)
	}
}

func TestRebalancePlanToProtoQUIC_WithMoves(t *testing.T) {
	plan := &service.RebalancePlanResult{
		Moves: []service.RebalanceMovePlan{
			{ShardID: 1, FromNode: "a", ToNode: "b", EstimatedBytes: 1024},
		},
		EstimatedDuration: 5 * time.Second,
		CurrentImbalance:  0.3,
	}
	out := rebalancePlanToProto(plan)
	if len(out.GetMoves()) != 1 {
		t.Fatalf("expected 1 move, got %d", len(out.GetMoves()))
	}
	if out.GetMoves()[0].GetShardId() != 1 {
		t.Errorf("expected shard_id=1, got %d", out.GetMoves()[0].GetShardId())
	}
}

func TestProtoToRebalancePlanQUIC_Nil(t *testing.T) {
	if got := protoToRebalancePlan(nil); got != nil {
		t.Fatalf("expected nil for nil input, got %v", got)
	}
}

func TestProtoToRebalancePlanQUIC_WithMoves(t *testing.T) {
	plan := &pb.RebalancePlan{
		Moves: []*pb.RebalanceMove{
			{ShardId: 3, FromNode: "x", ToNode: "y", EstimatedBytes: 512},
		},
		CurrentImbalance: 0.25,
	}
	out := protoToRebalancePlan(plan)
	if len(out.Moves) != 1 {
		t.Fatalf("expected 1 move, got %d", len(out.Moves))
	}
	if out.Moves[0].ShardID != 3 {
		t.Errorf("expected ShardID=3, got %d", out.Moves[0].ShardID)
	}
}

func TestHandleRebalanceGetShardStats_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.RebalanceShardStatsRequest{ShardId: 1})
	_, err := srv.handleRebalanceGetShardStats(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleRebalanceGetShardStats_InvalidPayload(t *testing.T) {
	svc := &mockRebalanceServiceQuic{}
	srv := NewServer(nil, nil, "localhost:0", WithRebalanceService(svc))
	_, err := srv.handleRebalanceGetShardStats(context.Background(), []byte{0xff})
	if err == nil {
		t.Fatal("expected UnmarshalError")
	}
}

func TestHandleRebalanceGetShardStats_Success(t *testing.T) {
	svc := &mockRebalanceServiceQuic{}
	srv := NewServer(nil, nil, "localhost:0", WithRebalanceService(svc))
	payload, _ := proto.Marshal(&pb.RebalanceShardStatsRequest{ShardId: 42})
	resp, err := srv.handleRebalanceGetShardStats(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleRebalanceGetShardStats failed: %v", err)
	}
	var out pb.ShardStats
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.GetShardId() != 42 {
		t.Fatalf("expected shard_id=42, got %d", out.GetShardId())
	}
}

func TestHandleRebalanceGetShardStats_ServiceError(t *testing.T) {
	svc := &mockRebalanceServiceQuic{
		statsFn: func(_ context.Context, _ uint64) (*service.ShardStats, error) {
			return nil, errors.New("stats failed")
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithRebalanceService(svc))
	payload, _ := proto.Marshal(&pb.RebalanceShardStatsRequest{ShardId: 1})
	_, err := srv.handleRebalanceGetShardStats(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestHandleRebalanceCreatePlan_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.RebalancePlanRequest{})
	_, err := srv.handleRebalanceCreatePlan(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
}

func TestHandleRebalanceCreatePlan_Success(t *testing.T) {
	svc := &mockRebalanceServiceQuic{}
	srv := NewServer(nil, nil, "localhost:0", WithRebalanceService(svc))
	payload, _ := proto.Marshal(&pb.RebalancePlanRequest{MaxMoves: 10, DryRun: true})
	resp, err := srv.handleRebalanceCreatePlan(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleRebalanceCreatePlan failed: %v", err)
	}
	var out pb.RebalancePlan
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.GetMoves()) != 1 {
		t.Fatalf("expected 1 move, got %d", len(out.GetMoves()))
	}
}

func TestHandleRebalanceCreatePlan_ServiceError(t *testing.T) {
	svc := &mockRebalanceServiceQuic{
		planFn: func(_ context.Context, _ *service.RebalancePlanOptions) (*service.RebalancePlanResult, error) {
			return nil, errors.New("plan failed")
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithRebalanceService(svc))
	payload, _ := proto.Marshal(&pb.RebalancePlanRequest{})
	_, err := srv.handleRebalanceCreatePlan(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestHandleRebalanceExecute_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.RebalancePlan{})
	_, err := srv.handleRebalanceExecute(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
}

func TestHandleRebalanceExecute_Success(t *testing.T) {
	svc := &mockRebalanceServiceQuic{}
	srv := NewServer(nil, nil, "localhost:0", WithRebalanceService(svc))
	payload, _ := proto.Marshal(&pb.RebalancePlan{
		Moves: []*pb.RebalanceMove{{ShardId: 1, FromNode: "a", ToNode: "b"}},
	})
	resp, err := srv.handleRebalanceExecute(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleRebalanceExecute failed: %v", err)
	}
	var out pb.RebalanceExecutionResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.GetMovesCompleted() != 1 {
		t.Fatalf("expected moves_completed=1, got %d", out.GetMovesCompleted())
	}
}

func TestHandleRebalanceExecute_ServiceError(t *testing.T) {
	svc := &mockRebalanceServiceQuic{
		executeFn: func(_ context.Context, _ *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error) {
			return nil, errors.New("execute failed")
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithRebalanceService(svc))
	payload, _ := proto.Marshal(&pb.RebalancePlan{Moves: []*pb.RebalanceMove{{ShardId: 1}}})
	_, err := srv.handleRebalanceExecute(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestHandleRebalanceGetImbalanceReport_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	_, err := srv.handleRebalanceGetImbalanceReport(context.Background(), nil)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
}

func TestHandleRebalanceGetImbalanceReport_Success(t *testing.T) {
	svc := &mockRebalanceServiceQuic{}
	srv := NewServer(nil, nil, "localhost:0", WithRebalanceService(svc))
	resp, err := srv.handleRebalanceGetImbalanceReport(context.Background(), nil)
	if err != nil {
		t.Fatalf("handleRebalanceGetImbalanceReport failed: %v", err)
	}
	var out pb.ImbalanceReport
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.GetNodes()) != 1 {
		t.Fatalf("expected 1 node, got %d", len(out.GetNodes()))
	}
}

func TestHandleRebalanceGetImbalanceReport_ServiceError(t *testing.T) {
	svc := &mockRebalanceServiceQuic{
		reportFn: func(_ context.Context) (*service.ImbalanceReportResult, error) {
			return nil, errors.New("report failed")
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithRebalanceService(svc))
	_, err := srv.handleRebalanceGetImbalanceReport(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// Database barrier handler tests (handlers_database_barrier.go – 0% covered)
// ---------------------------------------------------------------------------

func TestHandleDatabaseSeal_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.DatabaseSealRequest{Name: "testdb"})
	_, err := srv.handleDatabaseSeal(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleDatabaseSeal_InvalidPayload(t *testing.T) {
	svc := &mockDatabaseServiceQuic{}
	srv := NewServer(nil, nil, "localhost:0", WithDatabaseService(svc))
	_, err := srv.handleDatabaseSeal(context.Background(), []byte{0xff})
	if err == nil {
		t.Fatal("expected UnmarshalError")
	}
}

func TestHandleDatabaseSeal_Success(t *testing.T) {
	svc := &mockDatabaseServiceQuic{}
	srv := NewServer(nil, nil, "localhost:0", WithDatabaseService(svc))
	payload, _ := proto.Marshal(&pb.DatabaseSealRequest{Name: "testdb"})
	_, err := srv.handleDatabaseSeal(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleDatabaseSeal failed: %v", err)
	}
}

func TestHandleDatabaseSeal_ServiceError(t *testing.T) {
	svc := &mockDatabaseServiceQuic{
		sealFn: func(_ context.Context, _ string) error {
			return errors.New("seal failed")
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithDatabaseService(svc))
	payload, _ := proto.Marshal(&pb.DatabaseSealRequest{Name: "testdb"})
	_, err := srv.handleDatabaseSeal(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestHandleDatabaseUnseal_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.DatabaseUnsealRequest{Name: "testdb"})
	_, err := srv.handleDatabaseUnseal(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
}

func TestHandleDatabaseUnseal_InvalidPayload(t *testing.T) {
	svc := &mockDatabaseServiceQuic{}
	srv := NewServer(nil, nil, "localhost:0", WithDatabaseService(svc))
	_, err := srv.handleDatabaseUnseal(context.Background(), []byte{0xff})
	if err == nil {
		t.Fatal("expected UnmarshalError")
	}
}

func TestHandleDatabaseUnseal_Success(t *testing.T) {
	svc := &mockDatabaseServiceQuic{}
	srv := NewServer(nil, nil, "localhost:0", WithDatabaseService(svc))
	payload, _ := proto.Marshal(&pb.DatabaseUnsealRequest{Name: "testdb", Secret: "mysecret"})
	_, err := srv.handleDatabaseUnseal(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleDatabaseUnseal failed: %v", err)
	}
}

func TestHandleDatabaseUnseal_ServiceError(t *testing.T) {
	svc := &mockDatabaseServiceQuic{
		unsealFn: func(_ context.Context, _ string, _ seal.Credentials) error {
			return errors.New("unseal failed")
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithDatabaseService(svc))
	payload, _ := proto.Marshal(&pb.DatabaseUnsealRequest{Name: "testdb", Secret: "bad"})
	_, err := srv.handleDatabaseUnseal(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestHandleDatabaseBarrierStatus_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.DatabaseBarrierStatusRequest{Name: "testdb"})
	_, err := srv.handleDatabaseBarrierStatus(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
}

func TestHandleDatabaseBarrierStatus_InvalidPayload(t *testing.T) {
	svc := &mockDatabaseServiceQuic{}
	srv := NewServer(nil, nil, "localhost:0", WithDatabaseService(svc))
	_, err := srv.handleDatabaseBarrierStatus(context.Background(), []byte{0xff})
	if err == nil {
		t.Fatal("expected UnmarshalError")
	}
}

func TestHandleDatabaseBarrierStatus_Success(t *testing.T) {
	svc := &mockDatabaseServiceQuic{}
	srv := NewServer(nil, nil, "localhost:0", WithDatabaseService(svc))
	payload, _ := proto.Marshal(&pb.DatabaseBarrierStatusRequest{Name: "testdb"})
	resp, err := srv.handleDatabaseBarrierStatus(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleDatabaseBarrierStatus failed: %v", err)
	}
	var out pb.DatabaseBarrierStatusResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Strategy is "software" from the mock
	if out.GetStrategy() != "software" {
		t.Fatalf("expected strategy 'software', got %q", out.GetStrategy())
	}
}

func TestHandleDatabaseBarrierStatus_ServiceError(t *testing.T) {
	svc := &mockDatabaseServiceQuic{
		barrierStatusFn: func(_ string) (*seal.BarrierStatus, error) {
			return nil, errors.New("status failed")
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithDatabaseService(svc))
	payload, _ := proto.Marshal(&pb.DatabaseBarrierStatusRequest{Name: "testdb"})
	_, err := srv.handleDatabaseBarrierStatus(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
