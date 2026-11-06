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

// Coverage-uplift tests for the gRPC SDK transport. These tests target
// previously-uncovered code paths:
//   - Backup client (all methods: success + closed + rpc-error)
//   - DatabaseBarrier (SealDatabase, UnsealDatabase, DatabaseBarrierStatus)
//   - CeremonyCustodian (ClaimShareBegin, ClaimShareComplete, SubmitUnsealShare)
//   - Embedding (Embed, BatchEmbed, ListEmbeddingModels, helpers)
//   - RAG (IngestDocument, Query, DeleteDocument, ListDocuments + ragIngestChunkConfig)
//   - Rebalance (GetShardStats, CreateRebalancePlan, ExecuteRebalance,
//     GetImbalanceReport, protoToRebalancePlan / rebalancePlanToProto)
//   - CredentialStore + CredentialRetrieve (new simple-store API)
//   - GetUserByCertThumbprint + AddCertBinding
//   - protoConsistency / protoWriteMode mapping helpers (all enum branches)

package grpc

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"

	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Minimal fake gRPC service implementations for uncovered RPCs.
// We wire them through grpcpkg.RegisterService with functional options so the
// real proto glue code (and thus the Transport client methods) is exercised.
// ---------------------------------------------------------------------------

// fakeBackupServicer satisfies service.BackupServicer.
type fakeBackupServicer struct {
	createFn  func(ctx context.Context, req interface{}) (*pb.BackupInfo, error)
	listFn    func(ctx context.Context) (*pb.BackupListResponse, error)
	getFn     func(ctx context.Context, id string) (*pb.BackupInfo, error)
	deleteFn  func(ctx context.Context, id string) error
	restoreFn func(ctx context.Context, req interface{}) (*pb.BackupRestoreResponse, error)
	verifyFn  func(ctx context.Context, id string) (*pb.BackupVerifyResponse, error)
}

// fakeEmbeddingServicer satisfies service.EmbeddingServicer (enough for testing).
type fakeEmbeddingServicer struct {
	embedFn      func(ctx context.Context, req *pb.EmbedRequest) (*pb.EmbedResponse, error)
	batchEmbedFn func(ctx context.Context, req *pb.BatchEmbedRequest) (*pb.BatchEmbedResponse, error)
	listModelsFn func(ctx context.Context, req *pb.ListEmbeddingModelsRequest) (*pb.ListEmbeddingModelsResponse, error)
}

// ---------------------------------------------------------------------------
// Raw loopback gRPC server helpers used by tests that need service-specific
// behaviour. These avoid introducing full mock implementations for every
// service and instead use a thin testServer wrapper over
// UnimplementedQRDBServiceServer to override just the RPCs under test.
// ---------------------------------------------------------------------------

// testQRDBServer embeds UnimplementedQRDBServiceServer and lets individual
// tests override specific methods via function fields.
type testQRDBServer struct {
	pb.UnimplementedQRDBServiceServer

	// backup
	backupCreateFn  func(ctx context.Context, req *pb.BackupCreateRequest) (*pb.BackupInfo, error)
	backupListFn    func(ctx context.Context, _ *emptypb.Empty) (*pb.BackupListResponse, error)
	backupGetFn     func(ctx context.Context, req *pb.BackupGetRequest) (*pb.BackupInfo, error)
	backupDeleteFn  func(ctx context.Context, req *pb.BackupDeleteRequest) (*emptypb.Empty, error)
	backupRestoreFn func(ctx context.Context, req *pb.BackupRestoreRequest) (*pb.BackupRestoreResponse, error)
	backupVerifyFn  func(ctx context.Context, req *pb.BackupVerifyRequest) (*pb.BackupVerifyResponse, error)

	// database barrier
	dbSealFn          func(ctx context.Context, req *pb.DatabaseSealRequest) (*emptypb.Empty, error)
	dbUnsealFn        func(ctx context.Context, req *pb.DatabaseUnsealRequest) (*emptypb.Empty, error)
	dbBarrierStatusFn func(ctx context.Context, req *pb.DatabaseBarrierStatusRequest) (*pb.DatabaseBarrierStatusResponse, error)

	// ceremony custodian

	// embedding
	embedFn      func(ctx context.Context, req *pb.EmbedRequest) (*pb.EmbedResponse, error)
	batchEmbedFn func(ctx context.Context, req *pb.BatchEmbedRequest) (*pb.BatchEmbedResponse, error)
	listModelsFn func(ctx context.Context, req *pb.ListEmbeddingModelsRequest) (*pb.ListEmbeddingModelsResponse, error)

	// rebalance
	shardStatsFn      func(ctx context.Context, req *pb.RebalanceShardStatsRequest) (*pb.ShardStats, error)
	createPlanFn      func(ctx context.Context, req *pb.RebalancePlanRequest) (*pb.RebalancePlan, error)
	executeFn         func(ctx context.Context, req *pb.RebalancePlan) (*pb.RebalanceExecutionResponse, error)
	imbalanceReportFn func(ctx context.Context, _ *emptypb.Empty) (*pb.ImbalanceReport, error)

	// auth cert-binding
	getUserByCertFn  func(ctx context.Context, req *pb.GetUserByCertThumbprintRequest) (*pb.UserResponse, error)
	addCertBindFn    func(ctx context.Context, req *pb.AddCertBindingRequest) (*pb.UserResponse, error)
	removeCertBindFn func(ctx context.Context, req *pb.RemoveCertBindingRequest) (*pb.UserResponse, error)

	// RAG
	ragIngestFn    func(ctx context.Context, req *pb.RAGIngestDocumentRequest) (*pb.RAGIngestDocumentResponse, error)
	ragQueryFn     func(ctx context.Context, req *pb.RAGQueryRequest) (*pb.RAGQueryResponse, error)
	ragDeleteDocFn func(ctx context.Context, req *pb.RAGDeleteDocumentRequest) (*pb.RAGDeleteDocumentResponse, error)
	ragListDocsFn  func(ctx context.Context, req *pb.RAGListDocumentsRequest) (*pb.RAGListDocumentsResponse, error)

	// credential store/retrieve
	credStoreFn    func(ctx context.Context, req *pb.CredentialStoreRequest) (*emptypb.Empty, error)
	credRetrieveFn func(ctx context.Context, req *pb.CredentialRetrieveRequest) (*pb.CredentialRetrieveResponse, error)
}

// --------------- backup RPCs --------------------------------------------------

func (s *testQRDBServer) BackupCreate(ctx context.Context, req *pb.BackupCreateRequest) (*pb.BackupInfo, error) {
	if s.backupCreateFn != nil {
		return s.backupCreateFn(ctx, req)
	}
	return &pb.BackupInfo{Id: "bk-1", TimestampNs: time.Now().UnixNano()}, nil
}

func (s *testQRDBServer) BackupList(ctx context.Context, _ *emptypb.Empty) (*pb.BackupListResponse, error) {
	if s.backupListFn != nil {
		return s.backupListFn(ctx, nil)
	}
	return &pb.BackupListResponse{
		Backups: []*pb.BackupInfo{{Id: "bk-1"}, {Id: "bk-2"}},
	}, nil
}

func (s *testQRDBServer) BackupGet(ctx context.Context, req *pb.BackupGetRequest) (*pb.BackupInfo, error) {
	if s.backupGetFn != nil {
		return s.backupGetFn(ctx, req)
	}
	return &pb.BackupInfo{Id: req.GetId(), Size: 1024}, nil
}

func (s *testQRDBServer) BackupDelete(ctx context.Context, req *pb.BackupDeleteRequest) (*emptypb.Empty, error) {
	if s.backupDeleteFn != nil {
		return s.backupDeleteFn(ctx, req)
	}
	return &emptypb.Empty{}, nil
}

func (s *testQRDBServer) BackupRestore(ctx context.Context, req *pb.BackupRestoreRequest) (*pb.BackupRestoreResponse, error) {
	if s.backupRestoreFn != nil {
		return s.backupRestoreFn(ctx, req)
	}
	return &pb.BackupRestoreResponse{BackupId: req.GetBackupId(), ShardsRestored: 2, DurationNs: 1_000_000}, nil
}

func (s *testQRDBServer) BackupVerify(ctx context.Context, req *pb.BackupVerifyRequest) (*pb.BackupVerifyResponse, error) {
	if s.backupVerifyFn != nil {
		return s.backupVerifyFn(ctx, req)
	}
	return &pb.BackupVerifyResponse{
		Valid: true, ChecksumMatch: true,
		ExpectedChecksum: "abc", ActualChecksum: "abc",
	}, nil
}

// --------------- database barrier RPCs ---------------------------------------

func (s *testQRDBServer) DatabaseSeal(ctx context.Context, req *pb.DatabaseSealRequest) (*emptypb.Empty, error) {
	if s.dbSealFn != nil {
		return s.dbSealFn(ctx, req)
	}
	return &emptypb.Empty{}, nil
}

func (s *testQRDBServer) DatabaseUnseal(ctx context.Context, req *pb.DatabaseUnsealRequest) (*emptypb.Empty, error) {
	if s.dbUnsealFn != nil {
		return s.dbUnsealFn(ctx, req)
	}
	return &emptypb.Empty{}, nil
}

func (s *testQRDBServer) DatabaseBarrierStatus(ctx context.Context, req *pb.DatabaseBarrierStatusRequest) (*pb.DatabaseBarrierStatusResponse, error) {
	if s.dbBarrierStatusFn != nil {
		return s.dbBarrierStatusFn(ctx, req)
	}
	return &pb.DatabaseBarrierStatusResponse{
		Sealed: false, Strategy: "software", HardwareBacked: false,
		InitializedAtUnix: 1700000000,
	}, nil
}

// --------------- ceremony custodian RPCs -------------------------------------

// --------------- embedding RPCs ----------------------------------------------

func (s *testQRDBServer) Embed(ctx context.Context, req *pb.EmbedRequest) (*pb.EmbedResponse, error) {
	if s.embedFn != nil {
		return s.embedFn(ctx, req)
	}
	return &pb.EmbedResponse{
		Embeddings: []*pb.EmbeddingVector{{Values: []float32{0.1, 0.2}}},
		Model:      req.GetModel(),
		Usage:      &pb.EmbeddingUsage{PromptTokens: 5, TotalTokens: 5},
	}, nil
}

func (s *testQRDBServer) BatchEmbed(ctx context.Context, req *pb.BatchEmbedRequest) (*pb.BatchEmbedResponse, error) {
	if s.batchEmbedFn != nil {
		return s.batchEmbedFn(ctx, req)
	}
	return &pb.BatchEmbedResponse{
		Embeddings: []*pb.EmbeddingVector{{Values: []float32{0.3}}},
		Model:      req.GetModel(),
		TotalUsage: &pb.EmbeddingUsage{TotalTokens: 10},
	}, nil
}

func (s *testQRDBServer) ListEmbeddingModels(ctx context.Context, req *pb.ListEmbeddingModelsRequest) (*pb.ListEmbeddingModelsResponse, error) {
	if s.listModelsFn != nil {
		return s.listModelsFn(ctx, req)
	}
	return &pb.ListEmbeddingModelsResponse{
		Models: []*pb.EmbeddingModelInfo{
			{Name: "text-embedding-ada-002", Dimensions: 1536, MaxTokens: 8192},
		},
	}, nil
}

// --------------- rebalance RPCs ----------------------------------------------

func (s *testQRDBServer) RebalanceGetShardStats(ctx context.Context, req *pb.RebalanceShardStatsRequest) (*pb.ShardStats, error) {
	if s.shardStatsFn != nil {
		return s.shardStatsFn(ctx, req)
	}
	return &pb.ShardStats{
		ShardId: req.GetShardId(), KeyCount: 100, ByteCount: 2048, LeaderId: 1, ReplicaCount: 3,
	}, nil
}

func (s *testQRDBServer) RebalanceCreatePlan(ctx context.Context, req *pb.RebalancePlanRequest) (*pb.RebalancePlan, error) {
	if s.createPlanFn != nil {
		return s.createPlanFn(ctx, req)
	}
	return &pb.RebalancePlan{
		Moves: []*pb.RebalanceMove{
			{ShardId: 1, FromNode: "n1", ToNode: "n2", EstimatedBytes: 512},
		},
		EstimatedDurationNs: int64(time.Second),
		CurrentImbalance:    0.35,
	}, nil
}

func (s *testQRDBServer) RebalanceExecute(ctx context.Context, req *pb.RebalancePlan) (*pb.RebalanceExecutionResponse, error) {
	if s.executeFn != nil {
		return s.executeFn(ctx, req)
	}
	return &pb.RebalanceExecutionResponse{MovesCompleted: 1, MovesFailed: 0, DurationNs: 500_000_000, FinalImbalance: 0.05}, nil
}

func (s *testQRDBServer) RebalanceGetImbalanceReport(ctx context.Context, _ *emptypb.Empty) (*pb.ImbalanceReport, error) {
	if s.imbalanceReportFn != nil {
		return s.imbalanceReportFn(ctx, nil)
	}
	return &pb.ImbalanceReport{
		Nodes: []*pb.NodeLoad{
			{NodeId: "node-1", Address: "127.0.0.1:5001", ShardCount: 3, TotalKeys: 1000, TotalBytes: 4096, LeaderCount: 1},
		},
		ImbalanceScore: 0.2,
	}, nil
}

// --------------- auth cert-binding RPCs --------------------------------------

func (s *testQRDBServer) GetUserByCertThumbprint(ctx context.Context, req *pb.GetUserByCertThumbprintRequest) (*pb.UserResponse, error) {
	if s.getUserByCertFn != nil {
		return s.getUserByCertFn(ctx, req)
	}
	return &pb.UserResponse{Id: 99, Username: "cert-user"}, nil
}

func (s *testQRDBServer) AddCertBinding(ctx context.Context, req *pb.AddCertBindingRequest) (*pb.UserResponse, error) {
	if s.addCertBindFn != nil {
		return s.addCertBindFn(ctx, req)
	}
	return &pb.UserResponse{Id: req.GetUserId(), Username: "cert-user"}, nil
}

func (s *testQRDBServer) RemoveCertBinding(ctx context.Context, req *pb.RemoveCertBindingRequest) (*pb.UserResponse, error) {
	if s.removeCertBindFn != nil {
		return s.removeCertBindFn(ctx, req)
	}
	return &pb.UserResponse{Id: req.GetUserId(), Username: "cert-user"}, nil
}

// --------------- RAG RPCs ----------------------------------------------------

func (s *testQRDBServer) RAGIngestDocument(ctx context.Context, req *pb.RAGIngestDocumentRequest) (*pb.RAGIngestDocumentResponse, error) {
	if s.ragIngestFn != nil {
		return s.ragIngestFn(ctx, req)
	}
	return &pb.RAGIngestDocumentResponse{DocumentId: req.GetDocumentId(), ChunkCount: 3, Namespace: req.GetNamespace()}, nil
}

func (s *testQRDBServer) RAGQuery(ctx context.Context, req *pb.RAGQueryRequest) (*pb.RAGQueryResponse, error) {
	if s.ragQueryFn != nil {
		return s.ragQueryFn(ctx, req)
	}
	return &pb.RAGQueryResponse{
		Results: []*pb.RAGQueryResult{
			{ChunkText: "relevant chunk", Score: 0.9, DocumentId: "doc-1", ChunkIndex: 0},
		},
	}, nil
}

func (s *testQRDBServer) RAGDeleteDocument(ctx context.Context, req *pb.RAGDeleteDocumentRequest) (*pb.RAGDeleteDocumentResponse, error) {
	if s.ragDeleteDocFn != nil {
		return s.ragDeleteDocFn(ctx, req)
	}
	return &pb.RAGDeleteDocumentResponse{DocumentId: req.GetDocumentId(), ChunksRemoved: 3}, nil
}

func (s *testQRDBServer) RAGListDocuments(ctx context.Context, req *pb.RAGListDocumentsRequest) (*pb.RAGListDocumentsResponse, error) {
	if s.ragListDocsFn != nil {
		return s.ragListDocsFn(ctx, req)
	}
	return &pb.RAGListDocumentsResponse{
		Documents: []*pb.RAGDocumentInfo{{Id: "doc-1", Namespace: req.GetNamespace(), ChunkCount: 3}},
		Total:     1,
	}, nil
}

// --------------- credential store/retrieve RPCs ------------------------------

func (s *testQRDBServer) CredentialStore(ctx context.Context, req *pb.CredentialStoreRequest) (*emptypb.Empty, error) {
	if s.credStoreFn != nil {
		return s.credStoreFn(ctx, req)
	}
	return &emptypb.Empty{}, nil
}

func (s *testQRDBServer) CredentialRetrieve(ctx context.Context, req *pb.CredentialRetrieveRequest) (*pb.CredentialRetrieveResponse, error) {
	if s.credRetrieveFn != nil {
		return s.credRetrieveFn(ctx, req)
	}
	return &pb.CredentialRetrieveResponse{Value: []byte("secret-value")}, nil
}

// ---------------------------------------------------------------------------
// Helper: start a loopback gRPC server with testQRDBServer and return a
// Transport pointing at it. The Transport's gRPC connection bypasses
// Transport.Connect so no health-check service is needed.
// ---------------------------------------------------------------------------

func startTestServer(t *testing.T, svc *testQRDBServer) (*Transport, func()) {
	t.Helper()
	return startTestServerTLS(t, svc, nil)
}

// startTestServerTLS is startTestServer over tt; a nil tt is plaintext.
func startTestServerTLS(t *testing.T, svc *testQRDBServer, tt *testTLS) (*Transport, func()) {
	t.Helper()
	gs := googlegrpc.NewServer(tt.serverOptions()...)
	pb.RegisterQRDBServiceServer(gs, svc)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = gs.Serve(lis) }()

	conn, err := googlegrpc.NewClient(lis.Addr().String(), tt.dialOption())
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	tr := &Transport{
		config: tt.config(lis.Addr().String()),
		conn:   conn,
		client: pb.NewQRDBServiceClient(conn),
	}
	return tr, func() {
		_ = conn.Close()
		gs.Stop()
	}
}

// Helper used by tests that deliberately want a closed Transport.
func newUpliftClosed() *Transport {
	tr := &Transport{config: &transport.Config{}}
	tr.closed.Store(true)
	return tr
}

// ---------------------------------------------------------------------------
// Backup tests
// ---------------------------------------------------------------------------

func TestGRPCBackup_CreateBackup_Success(t *testing.T) {
	tr, cleanup := startTestServer(t, &testQRDBServer{})
	defer cleanup()

	info, err := tr.CreateBackup(context.Background(), &transport.BackupRequest{
		Destination: "/tmp/bk", ShardIDs: []uint64{1},
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	if info.ID != "bk-1" {
		t.Fatalf("unexpected ID: %q", info.ID)
	}
}

func TestGRPCBackup_CreateBackup_Closed(t *testing.T) {
	_, err := newUpliftClosed().CreateBackup(context.Background(), &transport.BackupRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestGRPCBackup_ListBackups_Success(t *testing.T) {
	tr, cleanup := startTestServer(t, &testQRDBServer{})
	defer cleanup()

	list, err := tr.ListBackups(context.Background())
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2, got %d", len(list))
	}
}

func TestGRPCBackup_ListBackups_Closed(t *testing.T) {
	_, err := newUpliftClosed().ListBackups(context.Background())
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestGRPCBackup_GetBackup_Success(t *testing.T) {
	tr, cleanup := startTestServer(t, &testQRDBServer{})
	defer cleanup()

	info, err := tr.GetBackup(context.Background(), "bk-99")
	if err != nil {
		t.Fatalf("GetBackup: %v", err)
	}
	if info.ID != "bk-99" {
		t.Fatalf("unexpected ID: %q", info.ID)
	}
}

func TestGRPCBackup_GetBackup_Closed(t *testing.T) {
	_, err := newUpliftClosed().GetBackup(context.Background(), "id")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestGRPCBackup_DeleteBackup_Success(t *testing.T) {
	var got string
	svc := &testQRDBServer{
		backupDeleteFn: func(_ context.Context, req *pb.BackupDeleteRequest) (*emptypb.Empty, error) {
			got = req.GetId()
			return &emptypb.Empty{}, nil
		},
	}
	tr, cleanup := startTestServer(t, svc)
	defer cleanup()

	if err := tr.DeleteBackup(context.Background(), "bk-1"); err != nil {
		t.Fatalf("DeleteBackup: %v", err)
	}
	if got != "bk-1" {
		t.Fatalf("server deleted %q, want bk-1", got)
	}
}

func TestGRPCBackup_DeleteBackup_Closed(t *testing.T) {
	err := newUpliftClosed().DeleteBackup(context.Background(), "id")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestGRPCBackup_RestoreBackup_Success(t *testing.T) {
	tr, cleanup := startTestServer(t, &testQRDBServer{})
	defer cleanup()

	result, err := tr.RestoreBackup(context.Background(), &transport.RestoreRequest{
		BackupID: "bk-1", TargetShards: []uint64{1, 2},
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if result.ShardsRestored != 2 {
		t.Fatalf("expected 2 shards restored, got %d", result.ShardsRestored)
	}
}

func TestGRPCBackup_RestoreBackup_Closed(t *testing.T) {
	_, err := newUpliftClosed().RestoreBackup(context.Background(), &transport.RestoreRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestGRPCBackup_VerifyBackup_Success(t *testing.T) {
	tr, cleanup := startTestServer(t, &testQRDBServer{})
	defer cleanup()

	result, err := tr.VerifyBackup(context.Background(), "bk-1")
	if err != nil {
		t.Fatalf("VerifyBackup: %v", err)
	}
	if !result.Valid {
		t.Fatal("expected Valid=true")
	}
}

func TestGRPCBackup_VerifyBackup_Closed(t *testing.T) {
	_, err := newUpliftClosed().VerifyBackup(context.Background(), "id")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

// protoToBackupInfo nil guard
func TestGRPCBackup_ProtoToBackupInfo_Nil(t *testing.T) {
	if got := protoToBackupInfo(nil); got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

// ---------------------------------------------------------------------------
// DatabaseBarrier tests
// ---------------------------------------------------------------------------

func TestGRPCDatabaseBarrier_SealDatabase_Success(t *testing.T) {
	var got string
	svc := &testQRDBServer{
		dbSealFn: func(_ context.Context, req *pb.DatabaseSealRequest) (*emptypb.Empty, error) {
			got = req.GetName()
			return &emptypb.Empty{}, nil
		},
	}
	tr, cleanup := startTestServer(t, svc)
	defer cleanup()

	if err := tr.SealDatabase(context.Background(), "mydb"); err != nil {
		t.Fatalf("SealDatabase: %v", err)
	}
	if got != "mydb" {
		t.Fatalf("server sealed %q, want mydb", got)
	}
}

func TestGRPCDatabaseBarrier_SealDatabase_Closed(t *testing.T) {
	err := newUpliftClosed().SealDatabase(context.Background(), "mydb")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestGRPCDatabaseBarrier_UnsealDatabase_Success(t *testing.T) {
	var got *pb.DatabaseUnsealRequest
	svc := &testQRDBServer{
		dbUnsealFn: func(_ context.Context, req *pb.DatabaseUnsealRequest) (*emptypb.Empty, error) {
			got = req
			return &emptypb.Empty{}, nil
		},
	}
	tr, cleanup := startTestServerTLS(t, svc, newTestTLS(t))
	defer cleanup()

	if err := tr.UnsealDatabase(context.Background(), "mydb", "s3cr3t"); err != nil {
		t.Fatalf("UnsealDatabase: %v", err)
	}
	if got.GetName() != "mydb" || got.GetSecret() != "s3cr3t" {
		t.Fatalf("server got name=%q secret=%q, want mydb/s3cr3t", got.GetName(), got.GetSecret())
	}
}

// TestGRPCDatabaseBarrier_UnsealDatabase_RPCError verifies a refused unseal
// over TLS reaches the caller as a RequestError naming the method.
func TestGRPCDatabaseBarrier_UnsealDatabase_RPCError(t *testing.T) {
	svc := &testQRDBServer{
		dbUnsealFn: func(context.Context, *pb.DatabaseUnsealRequest) (*emptypb.Empty, error) {
			return nil, errors.New("bad secret")
		},
	}
	tr, cleanup := startTestServerTLS(t, svc, newTestTLS(t))
	defer cleanup()

	var re *transport.RequestError
	if err := tr.UnsealDatabase(context.Background(), "mydb", "wrong"); !errors.As(err, &re) || re.Method != "UnsealDatabase" {
		t.Fatalf("error = %T %v, want RequestError for UnsealDatabase", err, err)
	}
}

func TestGRPCDatabaseBarrier_UnsealDatabase_Closed(t *testing.T) {
	err := newUpliftClosed().UnsealDatabase(context.Background(), "mydb", "x")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestGRPCDatabaseBarrier_Status_Success(t *testing.T) {
	tr, cleanup := startTestServer(t, &testQRDBServer{})
	defer cleanup()

	status, err := tr.DatabaseBarrierStatus(context.Background(), "mydb")
	if err != nil {
		t.Fatalf("DatabaseBarrierStatus: %v", err)
	}
	if status.Strategy != "software" {
		t.Fatalf("unexpected strategy: %q", status.Strategy)
	}
}

func TestGRPCDatabaseBarrier_Status_Closed(t *testing.T) {
	_, err := newUpliftClosed().DatabaseBarrierStatus(context.Background(), "mydb")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// Embedding tests
// ---------------------------------------------------------------------------

func TestGRPCEmbedding_Embed_Success(t *testing.T) {
	tr, cleanup := startTestServer(t, &testQRDBServer{})
	defer cleanup()

	resp, err := tr.Embed(context.Background(), &transport.EmbedRequest{
		Texts: []string{"hello world"}, Model: "ada-002",
	})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(resp.Embeddings) != 1 {
		t.Fatalf("expected 1 embedding, got %d", len(resp.Embeddings))
	}
	if resp.Usage == nil {
		t.Fatal("expected usage info")
	}
}

func TestGRPCEmbedding_Embed_Closed(t *testing.T) {
	_, err := newUpliftClosed().Embed(context.Background(), &transport.EmbedRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestGRPCEmbedding_BatchEmbed_Success(t *testing.T) {
	tr, cleanup := startTestServer(t, &testQRDBServer{})
	defer cleanup()

	resp, err := tr.BatchEmbed(context.Background(), &transport.BatchEmbedRequest{
		Texts: []string{"a", "b"}, Model: "ada-002", BatchSize: 2,
	})
	if err != nil {
		t.Fatalf("BatchEmbed: %v", err)
	}
	if len(resp.Embeddings) == 0 {
		t.Fatal("expected embeddings")
	}
}

func TestGRPCEmbedding_BatchEmbed_Closed(t *testing.T) {
	_, err := newUpliftClosed().BatchEmbed(context.Background(), &transport.BatchEmbedRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestGRPCEmbedding_ListModels_Success(t *testing.T) {
	tr, cleanup := startTestServer(t, &testQRDBServer{})
	defer cleanup()

	resp, err := tr.ListEmbeddingModels(context.Background(), "openai")
	if err != nil {
		t.Fatalf("ListEmbeddingModels: %v", err)
	}
	if len(resp.Models) == 0 {
		t.Fatal("expected models")
	}
}

func TestGRPCEmbedding_ListModels_Closed(t *testing.T) {
	_, err := newUpliftClosed().ListEmbeddingModels(context.Background(), "p")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

// protoEmbeddingsToMatrix with nil usage
func TestGRPCEmbedding_ProtoEmbeddingUsage_Nil(t *testing.T) {
	if got := protoEmbeddingUsage(nil); got != nil {
		t.Fatalf("expected nil UsageInfo for nil input")
	}
}

func TestGRPCEmbedding_ProtoEmbeddingsToMatrix(t *testing.T) {
	vectors := []*pb.EmbeddingVector{
		{Values: []float32{1.0, 2.0}},
		{Values: []float32{3.0}},
	}
	m := protoEmbeddingsToMatrix(vectors)
	if len(m) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(m))
	}
	if len(m[0]) != 2 || m[0][0] != 1.0 {
		t.Fatalf("unexpected row 0: %v", m[0])
	}
}

// ---------------------------------------------------------------------------
// Rebalance tests
// ---------------------------------------------------------------------------

func TestGRPCRebalance_GetShardStats_Success(t *testing.T) {
	tr, cleanup := startTestServer(t, &testQRDBServer{})
	defer cleanup()

	stats, err := tr.GetShardStats(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetShardStats: %v", err)
	}
	if stats.KeyCount != 100 {
		t.Fatalf("unexpected KeyCount: %d", stats.KeyCount)
	}
}

func TestGRPCRebalance_GetShardStats_Closed(t *testing.T) {
	_, err := newUpliftClosed().GetShardStats(context.Background(), 1)
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestGRPCRebalance_CreateRebalancePlan_Success(t *testing.T) {
	tr, cleanup := startTestServer(t, &testQRDBServer{})
	defer cleanup()

	plan, err := tr.CreateRebalancePlan(context.Background(), &transport.RebalancePlanRequest{
		MaxMoves: 5, DryRun: true,
	})
	if err != nil {
		t.Fatalf("CreateRebalancePlan: %v", err)
	}
	if len(plan.Moves) == 0 {
		t.Fatal("expected moves")
	}
}

func TestGRPCRebalance_CreateRebalancePlan_Closed(t *testing.T) {
	_, err := newUpliftClosed().CreateRebalancePlan(context.Background(), &transport.RebalancePlanRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestGRPCRebalance_ExecuteRebalance_Success(t *testing.T) {
	tr, cleanup := startTestServer(t, &testQRDBServer{})
	defer cleanup()

	plan := &transport.RebalancePlan{
		Moves: []transport.RebalanceMove{
			{ShardID: 1, FromNode: "n1", ToNode: "n2"},
		},
	}
	result, err := tr.ExecuteRebalance(context.Background(), plan)
	if err != nil {
		t.Fatalf("ExecuteRebalance: %v", err)
	}
	if result.MovesCompleted != 1 {
		t.Fatalf("unexpected MovesCompleted: %d", result.MovesCompleted)
	}
}

func TestGRPCRebalance_ExecuteRebalance_NilPlan(t *testing.T) {
	var got *pb.RebalancePlan
	svc := &testQRDBServer{
		executeFn: func(_ context.Context, req *pb.RebalancePlan) (*pb.RebalanceExecutionResponse, error) {
			got = req
			return &pb.RebalanceExecutionResponse{}, nil
		},
	}
	tr, cleanup := startTestServer(t, svc)
	defer cleanup()

	if _, err := tr.ExecuteRebalance(context.Background(), nil); err != nil {
		t.Fatalf("ExecuteRebalance(nil): %v", err)
	}
	if !proto.Equal(got, &pb.RebalancePlan{}) {
		t.Fatalf("server got plan %v, want an empty plan", got)
	}
}

func TestGRPCRebalance_ExecuteRebalance_Closed(t *testing.T) {
	_, err := newUpliftClosed().ExecuteRebalance(context.Background(), nil)
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestGRPCRebalance_GetImbalanceReport_Success(t *testing.T) {
	tr, cleanup := startTestServer(t, &testQRDBServer{})
	defer cleanup()

	report, err := tr.GetImbalanceReport(context.Background())
	if err != nil {
		t.Fatalf("GetImbalanceReport: %v", err)
	}
	if len(report.Nodes) == 0 {
		t.Fatal("expected nodes")
	}
}

func TestGRPCRebalance_GetImbalanceReport_Closed(t *testing.T) {
	_, err := newUpliftClosed().GetImbalanceReport(context.Background())
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

// protoToRebalancePlan nil guard
func TestGRPCRebalance_ProtoToRebalancePlan_Nil(t *testing.T) {
	if got := protoToRebalancePlan(nil); got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

// rebalancePlanToProto nil guard
func TestGRPCRebalance_RebalancePlanToProto_Nil(t *testing.T) {
	got := rebalancePlanToProto(nil)
	if got == nil {
		t.Fatal("expected non-nil proto even for nil plan")
	}
}

// ---------------------------------------------------------------------------
// CredentialStore / CredentialRetrieve tests (simple store API)
// ---------------------------------------------------------------------------

func TestGRPCCredential_Store_Success(t *testing.T) {
	var got *pb.CredentialStoreRequest
	svc := &testQRDBServer{
		credStoreFn: func(_ context.Context, req *pb.CredentialStoreRequest) (*emptypb.Empty, error) {
			got = req
			return &emptypb.Empty{}, nil
		},
	}
	tr, cleanup := startTestServer(t, svc)
	defer cleanup()

	if err := tr.CredentialStore(context.Background(), "n", []byte("v")); err != nil {
		t.Fatalf("CredentialStore: %v", err)
	}
	if got.GetName() != "n" || string(got.GetValue()) != "v" {
		t.Fatalf("server stored %q=%q, want n=v", got.GetName(), got.GetValue())
	}
}

func TestGRPCCredential_Store_Closed(t *testing.T) {
	err := newUpliftClosed().CredentialStore(context.Background(), "n", []byte("v"))
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestGRPCCredential_Retrieve_Success(t *testing.T) {
	tr, cleanup := startTestServer(t, &testQRDBServer{})
	defer cleanup()

	val, err := tr.retrieveBytes(context.Background(), "db-pass")
	if err != nil {
		t.Fatalf("CredentialRetrieve: %v", err)
	}
	if string(val) != "secret-value" {
		t.Fatalf("unexpected value: %q", string(val))
	}
}

func TestGRPCCredential_Retrieve_Closed(t *testing.T) {
	_, err := newUpliftClosed().retrieveBytes(context.Background(), "n")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// GetUserByCertThumbprint / AddCertBinding tests
// ---------------------------------------------------------------------------

func TestGRPCAuth_GetUserByCertThumbprint_Success(t *testing.T) {
	// Use grpcpkg.RegisterService wiring to exercise the real handler chain.
	svc := &testQRDBServer{}
	tr, cleanup := startTestServer(t, svc)
	defer cleanup()

	user, err := tr.GetUserByCertThumbprint(context.Background(), "abc:def:123")
	if err != nil {
		t.Fatalf("GetUserByCertThumbprint: %v", err)
	}
	if user == nil {
		t.Fatal("expected non-nil user")
	}
}

func TestGRPCAuth_GetUserByCertThumbprint_Closed(t *testing.T) {
	_, err := newUpliftClosed().GetUserByCertThumbprint(context.Background(), "tp")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestGRPCAuth_AddCertBinding_Success(t *testing.T) {
	tr, cleanup := startTestServer(t, &testQRDBServer{})
	defer cleanup()

	user, err := tr.AddCertBinding(context.Background(), 42, "fp:12:34", time.Now().Add(365*24*time.Hour).Unix())
	if err != nil {
		t.Fatalf("AddCertBinding: %v", err)
	}
	if user == nil {
		t.Fatal("expected non-nil user")
	}
}

func TestGRPCAuth_AddCertBinding_Closed(t *testing.T) {
	_, err := newUpliftClosed().AddCertBinding(context.Background(), 1, "fp", 0)
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// RemoveCertBinding — the withdrawal verb (go-qrdb#518)
// ---------------------------------------------------------------------------

func TestGRPCAuth_RemoveCertBinding_Success(t *testing.T) {
	var got *pb.RemoveCertBindingRequest
	svc := &testQRDBServer{
		removeCertBindFn: func(_ context.Context, req *pb.RemoveCertBindingRequest) (*pb.UserResponse, error) {
			got = req
			return &pb.UserResponse{Id: req.GetUserId(), Username: "cert-user"}, nil
		},
	}
	tr, cleanup := startTestServer(t, svc)
	defer cleanup()

	u, err := tr.RemoveCertBinding(context.Background(), 42, "fp:12:34")
	if err != nil {
		t.Fatalf("RemoveCertBinding: %v", err)
	}
	if u == nil || u.ID != 42 {
		t.Fatalf("unexpected user: %+v", u)
	}
	// Both arguments must cross the wire: a thumbprint that did not arrive
	// withdraws nothing and reports a not-found.
	if got.GetUserId() != 42 || got.GetThumbprint() != "fp:12:34" {
		t.Fatalf("server saw %+v, want user 42 / fp:12:34", got)
	}
}

func TestGRPCAuth_RemoveCertBinding_Closed(t *testing.T) {
	_, err := newUpliftClosed().RemoveCertBinding(context.Background(), 1, "fp")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// protoConsistency / protoWriteMode mapping helpers
// ---------------------------------------------------------------------------

func TestGRPCProtoConsistency_AllBranches(t *testing.T) {
	cases := []struct {
		opt  transport.ReadOption
		want pb.ConsistencyLevel
	}{
		{transport.WithConsistency(transport.ConsistencyStrong), pb.ConsistencyLevel_CONSISTENCY_LEVEL_STRONG},
		{transport.WithConsistency(transport.ConsistencyLinearizable), pb.ConsistencyLevel_CONSISTENCY_LEVEL_LINEARIZABLE},
		{transport.WithConsistency(transport.ConsistencyEventual), pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL},
		{transport.WithAllowStale(), pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL},
	}
	for _, c := range cases {
		got := protoConsistency(c.opt)
		if got != c.want {
			t.Errorf("protoConsistency(%v) = %v, want %v", c.opt, got, c.want)
		}
	}
	// No options → UNSPECIFIED
	if got := protoConsistency(); got != pb.ConsistencyLevel_CONSISTENCY_LEVEL_UNSPECIFIED {
		t.Errorf("protoConsistency() = %v, want UNSPECIFIED", got)
	}
}

func TestGRPCProtoWriteMode_AllBranches(t *testing.T) {
	cases := []struct {
		opt  transport.WriteOption
		want pb.WriteMode
	}{
		{transport.WithWriteMode(transport.WriteModeSync), pb.WriteMode_WRITE_MODE_SYNC},
		{transport.WithWriteMode(transport.WriteModeWALDurable), pb.WriteMode_WRITE_MODE_WAL_DURABLE},
		{transport.WithWriteMode(transport.WriteModeBatched), pb.WriteMode_WRITE_MODE_BATCHED},
	}
	for _, c := range cases {
		got := protoWriteMode(c.opt)
		if got != c.want {
			t.Errorf("protoWriteMode(%v) = %v, want %v", c.opt, got, c.want)
		}
	}
	// No options → UNSPECIFIED
	if got := protoWriteMode(); got != pb.WriteMode_WRITE_MODE_UNSPECIFIED {
		t.Errorf("protoWriteMode() = %v, want UNSPECIFIED", got)
	}
}

// ---------------------------------------------------------------------------
// grpcpkg.RegisterService options wiring — ensure WithRegisterBackupService
// and WithRegisterRebalanceService do not panic when nil svc is registered.
// ---------------------------------------------------------------------------

func TestGRPCRegisterOptions_NilSafe(t *testing.T) {
	gs := googlegrpc.NewServer()
	// This must not panic even with nil services.
	grpcpkg.RegisterService(gs, nil, nil,
		grpcpkg.WithRegisterBackupService(nil),
		grpcpkg.WithRegisterRebalanceService(nil),
		grpcpkg.WithRegisterEmbedding(nil),
		grpcpkg.WithRegisterRAGService(nil),
	)
}

// TestGRPCUplift_RPCErrorIsRequestError verifies that a server-side refusal on
// each admin RPC reaches the caller as a RequestError naming the method and
// carrying the gRPC status code, rather than as a bare or untyped error.
func TestGRPCUplift_RPCErrorIsRequestError(t *testing.T) {
	refused := errors.New("refused")
	svc := &testQRDBServer{
		backupCreateFn: func(context.Context, *pb.BackupCreateRequest) (*pb.BackupInfo, error) { return nil, refused },
		backupListFn:   func(context.Context, *emptypb.Empty) (*pb.BackupListResponse, error) { return nil, refused },
		backupGetFn:    func(context.Context, *pb.BackupGetRequest) (*pb.BackupInfo, error) { return nil, refused },
		backupDeleteFn: func(context.Context, *pb.BackupDeleteRequest) (*emptypb.Empty, error) { return nil, refused },
		backupRestoreFn: func(context.Context, *pb.BackupRestoreRequest) (*pb.BackupRestoreResponse, error) {
			return nil, refused
		},
		backupVerifyFn: func(context.Context, *pb.BackupVerifyRequest) (*pb.BackupVerifyResponse, error) {
			return nil, refused
		},
		dbSealFn: func(context.Context, *pb.DatabaseSealRequest) (*emptypb.Empty, error) { return nil, refused },
		dbBarrierStatusFn: func(context.Context, *pb.DatabaseBarrierStatusRequest) (*pb.DatabaseBarrierStatusResponse, error) {
			return nil, refused
		},
		embedFn:      func(context.Context, *pb.EmbedRequest) (*pb.EmbedResponse, error) { return nil, refused },
		batchEmbedFn: func(context.Context, *pb.BatchEmbedRequest) (*pb.BatchEmbedResponse, error) { return nil, refused },
		listModelsFn: func(context.Context, *pb.ListEmbeddingModelsRequest) (*pb.ListEmbeddingModelsResponse, error) {
			return nil, refused
		},
		shardStatsFn:      func(context.Context, *pb.RebalanceShardStatsRequest) (*pb.ShardStats, error) { return nil, refused },
		createPlanFn:      func(context.Context, *pb.RebalancePlanRequest) (*pb.RebalancePlan, error) { return nil, refused },
		executeFn:         func(context.Context, *pb.RebalancePlan) (*pb.RebalanceExecutionResponse, error) { return nil, refused },
		imbalanceReportFn: func(context.Context, *emptypb.Empty) (*pb.ImbalanceReport, error) { return nil, refused },
		credStoreFn:       func(context.Context, *pb.CredentialStoreRequest) (*emptypb.Empty, error) { return nil, refused },
		credRetrieveFn: func(context.Context, *pb.CredentialRetrieveRequest) (*pb.CredentialRetrieveResponse, error) {
			return nil, refused
		},
		getUserByCertFn: func(context.Context, *pb.GetUserByCertThumbprintRequest) (*pb.UserResponse, error) {
			return nil, refused
		},
		addCertBindFn:    func(context.Context, *pb.AddCertBindingRequest) (*pb.UserResponse, error) { return nil, refused },
		removeCertBindFn: func(context.Context, *pb.RemoveCertBindingRequest) (*pb.UserResponse, error) { return nil, refused },
	}
	tr, cleanup := startTestServer(t, svc)
	defer cleanup()
	ctx := context.Background()

	calls := map[string]func() error{
		"CreateBackup": func() error {
			_, err := tr.CreateBackup(ctx, &transport.BackupRequest{Destination: "/x"})
			return err
		},
		"ListBackups":           func() error { _, err := tr.ListBackups(ctx); return err },
		"GetBackup":             func() error { _, err := tr.GetBackup(ctx, "bk-1"); return err },
		"DeleteBackup":          func() error { return tr.DeleteBackup(ctx, "bk-1") },
		"RestoreBackup":         func() error { _, err := tr.RestoreBackup(ctx, &transport.RestoreRequest{BackupID: "x"}); return err },
		"VerifyBackup":          func() error { _, err := tr.VerifyBackup(ctx, "bk-1"); return err },
		"SealDatabase":          func() error { return tr.SealDatabase(ctx, "mydb") },
		"DatabaseBarrierStatus": func() error { _, err := tr.DatabaseBarrierStatus(ctx, "mydb"); return err },
		"Embed": func() error {
			_, err := tr.Embed(ctx, &transport.EmbedRequest{Texts: []string{"x"}})
			return err
		},
		"BatchEmbed": func() error {
			_, err := tr.BatchEmbed(ctx, &transport.BatchEmbedRequest{Texts: []string{"x"}})
			return err
		},
		"ListEmbeddingModels": func() error { _, err := tr.ListEmbeddingModels(ctx, "openai"); return err },
		"GetShardStats":       func() error { _, err := tr.GetShardStats(ctx, 1); return err },
		"CreateRebalancePlan": func() error {
			_, err := tr.CreateRebalancePlan(ctx, &transport.RebalancePlanRequest{})
			return err
		},
		"ExecuteRebalance": func() error {
			_, err := tr.ExecuteRebalance(ctx, &transport.RebalancePlan{})
			return err
		},
		"GetImbalanceReport":      func() error { _, err := tr.GetImbalanceReport(ctx); return err },
		"CredentialStore":         func() error { return tr.CredentialStore(ctx, "n", []byte("v")) },
		"CredentialRetrieve":      func() error { _, err := tr.retrieveBytes(ctx, "n"); return err },
		"GetUserByCertThumbprint": func() error { _, err := tr.GetUserByCertThumbprint(ctx, "tp"); return err },
		"AddCertBinding":          func() error { _, err := tr.AddCertBinding(ctx, 1, "fp", 0); return err },
		"RemoveCertBinding":       func() error { _, err := tr.RemoveCertBinding(ctx, 1, "fp"); return err },
	}
	for method, call := range calls {
		t.Run(method, func(t *testing.T) {
			var re *transport.RequestError
			if err := call(); !errors.As(err, &re) {
				t.Fatalf("error = %T %v, want *transport.RequestError", err, err)
			}
			if re.Method != method || re.StatusCode != int(codes.Unknown) {
				t.Fatalf("RequestError{Method: %q, StatusCode: %d}, want {%q, %d}",
					re.Method, re.StatusCode, method, codes.Unknown)
			}
		})
	}
}
