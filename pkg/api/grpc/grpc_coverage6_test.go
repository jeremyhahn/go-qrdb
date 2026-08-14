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
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/custodian"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// mockBackupService
// ---------------------------------------------------------------------------

type mockBackupService struct {
	createFn  func(ctx context.Context, opts service.BackupOptions) (*service.BackupInfo, error)
	listFn    func(ctx context.Context) ([]service.BackupInfo, error)
	getFn     func(ctx context.Context, id string) (*service.BackupInfo, error)
	deleteFn  func(ctx context.Context, id string) error
	restoreFn func(ctx context.Context, opts service.RestoreOptions) (*service.RestoreResult, error)
	verifyFn  func(ctx context.Context, id string) (*service.VerifyResult, error)
}

func (m *mockBackupService) CreateBackup(ctx context.Context, opts service.BackupOptions) (*service.BackupInfo, error) {
	if m.createFn != nil {
		return m.createFn(ctx, opts)
	}
	return &service.BackupInfo{
		ID:          "backup-001",
		Timestamp:   time.Now(),
		ShardCount:  2,
		Destination: opts.Destination,
	}, nil
}

func (m *mockBackupService) ListBackups(ctx context.Context) ([]service.BackupInfo, error) {
	if m.listFn != nil {
		return m.listFn(ctx)
	}
	return []service.BackupInfo{{ID: "backup-001", Timestamp: time.Now()}}, nil
}

func (m *mockBackupService) GetBackup(ctx context.Context, id string) (*service.BackupInfo, error) {
	if m.getFn != nil {
		return m.getFn(ctx, id)
	}
	return &service.BackupInfo{ID: id, Timestamp: time.Now()}, nil
}

func (m *mockBackupService) DeleteBackup(ctx context.Context, id string) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, id)
	}
	return nil
}

func (m *mockBackupService) RestoreBackup(ctx context.Context, opts service.RestoreOptions) (*service.RestoreResult, error) {
	if m.restoreFn != nil {
		return m.restoreFn(ctx, opts)
	}
	return &service.RestoreResult{
		BackupID:       opts.BackupID,
		ShardsRestored: 2,
		Duration:       time.Second,
	}, nil
}

func (m *mockBackupService) VerifyBackup(ctx context.Context, id string) (*service.VerifyResult, error) {
	if m.verifyFn != nil {
		return m.verifyFn(ctx, id)
	}
	return &service.VerifyResult{
		Valid:            true,
		ChecksumMatch:    true,
		ExpectedChecksum: "abc123",
		ActualChecksum:   "abc123",
	}, nil
}

// ---------------------------------------------------------------------------
// mockRebalanceService
// ---------------------------------------------------------------------------

type mockRebalanceService struct {
	statsFn   func(ctx context.Context, shardID uint64) (*service.ShardStats, error)
	planFn    func(ctx context.Context, opts *service.RebalancePlanOptions) (*service.RebalancePlanResult, error)
	executeFn func(ctx context.Context, plan *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error)
	reportFn  func(ctx context.Context) (*service.ImbalanceReportResult, error)
}

func (m *mockRebalanceService) GetShardStats(ctx context.Context, shardID uint64) (*service.ShardStats, error) {
	if m.statsFn != nil {
		return m.statsFn(ctx, shardID)
	}
	return &service.ShardStats{ShardID: shardID, KeyCount: 100, ByteCount: 4096, LeaderID: 1, ReplicaCount: 3}, nil
}

func (m *mockRebalanceService) CreateRebalancePlan(ctx context.Context, opts *service.RebalancePlanOptions) (*service.RebalancePlanResult, error) {
	if m.planFn != nil {
		return m.planFn(ctx, opts)
	}
	return &service.RebalancePlanResult{
		Moves: []service.RebalanceMovePlan{
			{ShardID: 1, FromNode: "node-a", ToNode: "node-b", EstimatedBytes: 1024},
		},
		EstimatedDuration: time.Second * 5,
		CurrentImbalance:  0.3,
	}, nil
}

func (m *mockRebalanceService) ExecuteRebalance(ctx context.Context, plan *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error) {
	if m.executeFn != nil {
		return m.executeFn(ctx, plan)
	}
	return &service.RebalanceExecutionResult{
		MovesCompleted: len(plan.Moves),
		MovesFailed:    0,
		Duration:       time.Second * 3,
		FinalImbalance: 0.05,
	}, nil
}

func (m *mockRebalanceService) GetImbalanceReport(ctx context.Context) (*service.ImbalanceReportResult, error) {
	if m.reportFn != nil {
		return m.reportFn(ctx)
	}
	return &service.ImbalanceReportResult{
		Nodes: []service.NodeLoadInfo{
			{NodeID: "n1", Address: "127.0.0.1:7001", ShardCount: 3, TotalKeys: 300, TotalBytes: 12288, LeaderCount: 2},
		},
		ImbalanceScore: 0.2,
		Hotspots:       []string{},
	}, nil
}

// ---------------------------------------------------------------------------
// mockRAGService
// ---------------------------------------------------------------------------

type mockRAGService struct {
	ingestFn func(ctx context.Context, req *service.IngestDocumentRequest) (*service.IngestDocumentResponse, error)
	queryFn  func(ctx context.Context, req *service.RAGQueryRequest) (*service.RAGQueryResponse, error)
	deleteFn func(ctx context.Context, req *service.DeleteRAGDocumentRequest) (*service.DeleteRAGDocumentResponse, error)
	listFn   func(ctx context.Context, req *service.ListRAGDocumentsRequest) (*service.ListRAGDocumentsResponse, error)
	closeErr error
}

func (m *mockRAGService) IngestDocument(ctx context.Context, req *service.IngestDocumentRequest) (*service.IngestDocumentResponse, error) {
	if m.ingestFn != nil {
		return m.ingestFn(ctx, req)
	}
	return &service.IngestDocumentResponse{DocumentID: req.DocumentID, ChunkCount: 3, Namespace: req.Namespace}, nil
}

func (m *mockRAGService) Query(ctx context.Context, req *service.RAGQueryRequest) (*service.RAGQueryResponse, error) {
	if m.queryFn != nil {
		return m.queryFn(ctx, req)
	}
	return &service.RAGQueryResponse{
		Results: []*service.RAGQueryResult{
			{ChunkText: "chunk text", Score: 0.9, DocumentID: "doc1", ChunkIndex: 0},
		},
	}, nil
}

func (m *mockRAGService) DeleteDocument(ctx context.Context, req *service.DeleteRAGDocumentRequest) (*service.DeleteRAGDocumentResponse, error) {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, req)
	}
	return &service.DeleteRAGDocumentResponse{DocumentID: req.DocumentID, ChunksRemoved: 3}, nil
}

func (m *mockRAGService) ListDocuments(ctx context.Context, req *service.ListRAGDocumentsRequest) (*service.ListRAGDocumentsResponse, error) {
	if m.listFn != nil {
		return m.listFn(ctx, req)
	}
	return &service.ListRAGDocumentsResponse{
		Documents: []*service.RAGDocumentInfo{{ID: "doc1", Namespace: req.Namespace, ChunkCount: 3}},
		Total:     1,
	}, nil
}

func (m *mockRAGService) Close() error { return m.closeErr }

// ---------------------------------------------------------------------------
// mockCeremonyServiceWithCustodian extends mockCeremonyService to return
// real values for custodian-ceremony operations.
// ---------------------------------------------------------------------------

type mockCeremonyServiceWithCustodian struct {
	mockCeremonyService
	beginFn    func(ctx context.Context, id string) (*service.CeremonyCustodianChallenge, error)
	completeFn func(ctx context.Context, id, challengeID string, factorResponse []byte) (*custodian.Custodian, error)
	submitFn   func(ctx context.Context, id, challengeID string, factorResponse, shareBytes []byte) (*service.CeremonyUnsealShareResult, error)
}

func (m *mockCeremonyServiceWithCustodian) CeremonyClaimShareBegin(ctx context.Context, id string) (*service.CeremonyCustodianChallenge, error) {
	if m.beginFn != nil {
		return m.beginFn(ctx, id)
	}
	return &service.CeremonyCustodianChallenge{
		CustodianID: id,
		Nonce:       []byte("nonce-bytes"),
		ExpiresAt:   time.Now().Add(15 * time.Minute),
	}, nil
}

func (m *mockCeremonyServiceWithCustodian) CeremonyClaimShareComplete(ctx context.Context, id, challengeID string, factorResponse []byte) (*custodian.Custodian, error) {
	if m.completeFn != nil {
		return m.completeFn(ctx, id, challengeID, factorResponse)
	}
	now := time.Now()
	return &custodian.Custodian{
		ID:           id,
		Username:     "alice",
		TenantID:     "tenant-1",
		Method:       "software",
		EnrollmentID: "enroll-1",
		ShareIndex:   1,
		EnrolledAt:   now,
		ShareClaimed: true,
	}, nil
}

func (m *mockCeremonyServiceWithCustodian) CeremonySubmitUnsealShare(ctx context.Context, id, challengeID string, factorResponse, shareBytes []byte) (*service.CeremonyUnsealShareResult, error) {
	if m.submitFn != nil {
		return m.submitFn(ctx, id, challengeID, factorResponse, shareBytes)
	}
	return &service.CeremonyUnsealShareResult{
		Submitted: 1,
		Required:  2,
		Unsealed:  false,
	}, nil
}

// ---------------------------------------------------------------------------
// RegisterService options for backup/rebalance/RAG (0% covered)
// ---------------------------------------------------------------------------

func TestWithRegisterBackupService(t *testing.T) {
	svc := &mockBackupService{}
	grpcSrv := newGRPCServer()
	RegisterService(grpcSrv, newMockKVService(), newMockAdminService(),
		WithRegisterBackupService(svc),
	)
	grpcSrv.Stop()
}

func TestWithRegisterRebalanceService(t *testing.T) {
	svc := &mockRebalanceService{}
	grpcSrv := newGRPCServer()
	RegisterService(grpcSrv, newMockKVService(), newMockAdminService(),
		WithRegisterRebalanceService(svc),
	)
	grpcSrv.Stop()
}

func TestWithRegisterRAGService(t *testing.T) {
	svc := &mockRAGService{}
	grpcSrv := newGRPCServer()
	RegisterService(grpcSrv, newMockKVService(), newMockAdminService(),
		WithRegisterRAGService(svc),
	)
	grpcSrv.Stop()
}

// ---------------------------------------------------------------------------
// Server option tests (0% covered)
// ---------------------------------------------------------------------------

func TestWithRAGService_ServerOption(t *testing.T) {
	svc := &mockRAGService{}
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithRAGService(svc))
	if srv.ragSvc == nil {
		t.Fatal("expected ragSvc to be set")
	}
}

func TestWithAuditLogger_ServerOption(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithAuditLogger(nil))
	if srv.auditLogger != nil {
		t.Fatal("expected auditLogger to be nil when nil is passed")
	}
}

func TestWithBackupService_ServerOption(t *testing.T) {
	svc := &mockBackupService{}
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithBackupService(svc))
	if srv.backupSvc == nil {
		t.Fatal("expected backupSvc to be set")
	}
}

func TestWithRebalanceService_ServerOption(t *testing.T) {
	svc := &mockRebalanceService{}
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithRebalanceService(svc))
	if srv.rebalanceSvc == nil {
		t.Fatal("expected rebalanceSvc to be set")
	}
}

// ---------------------------------------------------------------------------
// BackupCreate
// ---------------------------------------------------------------------------

func TestGRPC_BackupCreate_Success(t *testing.T) {
	svc := &mockBackupService{}
	client, cleanup := startTestServerFull(t, WithBackupService(svc))
	defer cleanup()

	resp, err := client.BackupCreate(context.Background(), &pb.BackupCreateRequest{
		Destination: "/tmp/backup",
		Compression: "gzip",
	})
	if err != nil {
		t.Fatalf("BackupCreate failed: %v", err)
	}
	if resp.GetId() != "backup-001" {
		t.Fatalf("expected id 'backup-001', got %q", resp.GetId())
	}
}

func TestGRPC_BackupCreate_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.BackupCreate(context.Background(), &pb.BackupCreateRequest{Destination: "/tmp"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_BackupCreate_Error(t *testing.T) {
	svc := &mockBackupService{
		createFn: func(_ context.Context, _ service.BackupOptions) (*service.BackupInfo, error) {
			return nil, &service.ProposeError{Err: errors.New("backup failed")}
		},
	}
	client, cleanup := startTestServerFull(t, WithBackupService(svc))
	defer cleanup()

	_, err := client.BackupCreate(context.Background(), &pb.BackupCreateRequest{Destination: "/tmp"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// BackupList
// ---------------------------------------------------------------------------

func TestGRPC_BackupList_Success(t *testing.T) {
	svc := &mockBackupService{}
	client, cleanup := startTestServerFull(t, WithBackupService(svc))
	defer cleanup()

	resp, err := client.BackupList(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("BackupList failed: %v", err)
	}
	if len(resp.GetBackups()) != 1 {
		t.Fatalf("expected 1 backup, got %d", len(resp.GetBackups()))
	}
}

func TestGRPC_BackupList_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.BackupList(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_BackupList_Error(t *testing.T) {
	svc := &mockBackupService{
		listFn: func(_ context.Context) ([]service.BackupInfo, error) {
			return nil, errors.New("list failed")
		},
	}
	client, cleanup := startTestServerFull(t, WithBackupService(svc))
	defer cleanup()

	_, err := client.BackupList(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// BackupGet
// ---------------------------------------------------------------------------

func TestGRPC_BackupGet_Success(t *testing.T) {
	svc := &mockBackupService{}
	client, cleanup := startTestServerFull(t, WithBackupService(svc))
	defer cleanup()

	resp, err := client.BackupGet(context.Background(), &pb.BackupGetRequest{Id: "backup-001"})
	if err != nil {
		t.Fatalf("BackupGet failed: %v", err)
	}
	if resp.GetId() != "backup-001" {
		t.Fatalf("expected id 'backup-001', got %q", resp.GetId())
	}
}

func TestGRPC_BackupGet_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.BackupGet(context.Background(), &pb.BackupGetRequest{Id: "x"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_BackupGet_Error(t *testing.T) {
	svc := &mockBackupService{
		getFn: func(_ context.Context, _ string) (*service.BackupInfo, error) {
			return nil, &service.KeyNotFoundError{Key: "backup-999"}
		},
	}
	client, cleanup := startTestServerFull(t, WithBackupService(svc))
	defer cleanup()

	_, err := client.BackupGet(context.Background(), &pb.BackupGetRequest{Id: "backup-999"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// BackupDelete
// ---------------------------------------------------------------------------

func TestGRPC_BackupDelete_Success(t *testing.T) {
	svc := &mockBackupService{}
	client, cleanup := startTestServerFull(t, WithBackupService(svc))
	defer cleanup()

	_, err := client.BackupDelete(context.Background(), &pb.BackupDeleteRequest{Id: "backup-001"})
	if err != nil {
		t.Fatalf("BackupDelete failed: %v", err)
	}
}

func TestGRPC_BackupDelete_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.BackupDelete(context.Background(), &pb.BackupDeleteRequest{Id: "x"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_BackupDelete_Error(t *testing.T) {
	svc := &mockBackupService{
		deleteFn: func(_ context.Context, _ string) error {
			return errors.New("delete failed")
		},
	}
	client, cleanup := startTestServerFull(t, WithBackupService(svc))
	defer cleanup()

	_, err := client.BackupDelete(context.Background(), &pb.BackupDeleteRequest{Id: "backup-001"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// BackupRestore
// ---------------------------------------------------------------------------

func TestGRPC_BackupRestore_Success(t *testing.T) {
	svc := &mockBackupService{}
	client, cleanup := startTestServerFull(t, WithBackupService(svc))
	defer cleanup()

	resp, err := client.BackupRestore(context.Background(), &pb.BackupRestoreRequest{
		BackupId:      "backup-001",
		TargetShards:  []uint64{1, 2},
		ValidateFirst: true,
	})
	if err != nil {
		t.Fatalf("BackupRestore failed: %v", err)
	}
	if resp.GetBackupId() != "backup-001" {
		t.Fatalf("expected backup_id 'backup-001', got %q", resp.GetBackupId())
	}
	if resp.GetShardsRestored() != 2 {
		t.Fatalf("expected 2 shards restored, got %d", resp.GetShardsRestored())
	}
}

func TestGRPC_BackupRestore_WithShardErrors(t *testing.T) {
	svc := &mockBackupService{
		restoreFn: func(_ context.Context, _ service.RestoreOptions) (*service.RestoreResult, error) {
			return &service.RestoreResult{
				BackupID:       "backup-001",
				ShardsRestored: 1,
				Errors: []service.ShardError{
					{ShardID: 2, Message: "shard unavailable"},
				},
			}, nil
		},
	}
	client, cleanup := startTestServerFull(t, WithBackupService(svc))
	defer cleanup()

	resp, err := client.BackupRestore(context.Background(), &pb.BackupRestoreRequest{BackupId: "backup-001"})
	if err != nil {
		t.Fatalf("BackupRestore failed: %v", err)
	}
	if len(resp.GetErrors()) != 1 {
		t.Fatalf("expected 1 shard error, got %d", len(resp.GetErrors()))
	}
	if resp.GetErrors()[0].GetShardId() != 2 {
		t.Fatalf("expected shard_id=2, got %d", resp.GetErrors()[0].GetShardId())
	}
}

func TestGRPC_BackupRestore_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.BackupRestore(context.Background(), &pb.BackupRestoreRequest{BackupId: "x"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_BackupRestore_Error(t *testing.T) {
	svc := &mockBackupService{
		restoreFn: func(_ context.Context, _ service.RestoreOptions) (*service.RestoreResult, error) {
			return nil, errors.New("restore failed")
		},
	}
	client, cleanup := startTestServerFull(t, WithBackupService(svc))
	defer cleanup()

	_, err := client.BackupRestore(context.Background(), &pb.BackupRestoreRequest{BackupId: "b"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// BackupVerify
// ---------------------------------------------------------------------------

func TestGRPC_BackupVerify_Success(t *testing.T) {
	svc := &mockBackupService{}
	client, cleanup := startTestServerFull(t, WithBackupService(svc))
	defer cleanup()

	resp, err := client.BackupVerify(context.Background(), &pb.BackupVerifyRequest{Id: "backup-001"})
	if err != nil {
		t.Fatalf("BackupVerify failed: %v", err)
	}
	if !resp.GetValid() {
		t.Fatal("expected valid=true")
	}
	if !resp.GetChecksumMatch() {
		t.Fatal("expected checksum_match=true")
	}
}

func TestGRPC_BackupVerify_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.BackupVerify(context.Background(), &pb.BackupVerifyRequest{Id: "x"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_BackupVerify_Error(t *testing.T) {
	svc := &mockBackupService{
		verifyFn: func(_ context.Context, _ string) (*service.VerifyResult, error) {
			return nil, errors.New("verify failed")
		},
	}
	client, cleanup := startTestServerFull(t, WithBackupService(svc))
	defer cleanup()

	_, err := client.BackupVerify(context.Background(), &pb.BackupVerifyRequest{Id: "backup-001"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// backupInfoToProto (direct unit test)
// ---------------------------------------------------------------------------

func TestBackupInfoToProto_Nil(t *testing.T) {
	if got := backupInfoToProto(nil); got != nil {
		t.Fatalf("expected nil for nil input, got %v", got)
	}
}

func TestBackupInfoToProto_Full(t *testing.T) {
	ts := time.Unix(1700000000, 0)
	b := &service.BackupInfo{
		ID:           "b1",
		Timestamp:    ts,
		Size:         4096,
		ShardCount:   3,
		Status:       service.BackupStatusComplete,
		Checksum:     "abc",
		Destination:  "/tmp",
		Compression:  "gzip",
		Encryption:   "aes256",
		Metadata:     map[string]string{"env": "prod"},
		Duration:     2 * time.Second,
		ErrorMessage: "",
	}
	pb := backupInfoToProto(b)
	if pb.GetId() != "b1" {
		t.Errorf("expected id 'b1', got %q", pb.GetId())
	}
	if pb.GetTimestampNs() != ts.UnixNano() {
		t.Errorf("unexpected timestamp_ns")
	}
	if pb.GetShardCount() != 3 {
		t.Errorf("expected shard_count=3, got %d", pb.GetShardCount())
	}
}

// ---------------------------------------------------------------------------
// shardErrorToProto (direct unit test)
// ---------------------------------------------------------------------------

func TestShardErrorToProto(t *testing.T) {
	se := service.ShardError{ShardID: 5, Message: "shard failed"}
	pb := shardErrorToProto(se)
	if pb.GetShardId() != 5 {
		t.Errorf("expected shard_id=5, got %d", pb.GetShardId())
	}
	if pb.GetMessage() != "shard failed" {
		t.Errorf("expected message 'shard failed', got %q", pb.GetMessage())
	}
}

// ---------------------------------------------------------------------------
// RebalanceGetShardStats
// ---------------------------------------------------------------------------

func TestGRPC_RebalanceGetShardStats_Success(t *testing.T) {
	svc := &mockRebalanceService{}
	client, cleanup := startTestServerFull(t, WithRebalanceService(svc))
	defer cleanup()

	resp, err := client.RebalanceGetShardStats(context.Background(), &pb.RebalanceShardStatsRequest{ShardId: 42})
	if err != nil {
		t.Fatalf("RebalanceGetShardStats failed: %v", err)
	}
	if resp.GetShardId() != 42 {
		t.Fatalf("expected shard_id=42, got %d", resp.GetShardId())
	}
	if resp.GetKeyCount() != 100 {
		t.Fatalf("expected key_count=100, got %d", resp.GetKeyCount())
	}
}

func TestGRPC_RebalanceGetShardStats_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.RebalanceGetShardStats(context.Background(), &pb.RebalanceShardStatsRequest{ShardId: 1})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_RebalanceGetShardStats_Error(t *testing.T) {
	svc := &mockRebalanceService{
		statsFn: func(_ context.Context, _ uint64) (*service.ShardStats, error) {
			return nil, errors.New("stats failed")
		},
	}
	client, cleanup := startTestServerFull(t, WithRebalanceService(svc))
	defer cleanup()

	_, err := client.RebalanceGetShardStats(context.Background(), &pb.RebalanceShardStatsRequest{ShardId: 1})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// RebalanceCreatePlan
// ---------------------------------------------------------------------------

func TestGRPC_RebalanceCreatePlan_Success(t *testing.T) {
	svc := &mockRebalanceService{}
	client, cleanup := startTestServerFull(t, WithRebalanceService(svc))
	defer cleanup()

	resp, err := client.RebalanceCreatePlan(context.Background(), &pb.RebalancePlanRequest{
		MaxMoves:      10,
		DryRun:        true,
		TargetBalance: 0.1,
	})
	if err != nil {
		t.Fatalf("RebalanceCreatePlan failed: %v", err)
	}
	if len(resp.GetMoves()) != 1 {
		t.Fatalf("expected 1 move, got %d", len(resp.GetMoves()))
	}
	if resp.GetMoves()[0].GetFromNode() != "node-a" {
		t.Fatalf("expected from_node 'node-a', got %q", resp.GetMoves()[0].GetFromNode())
	}
}

func TestGRPC_RebalanceCreatePlan_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.RebalanceCreatePlan(context.Background(), &pb.RebalancePlanRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_RebalanceCreatePlan_Error(t *testing.T) {
	svc := &mockRebalanceService{
		planFn: func(_ context.Context, _ *service.RebalancePlanOptions) (*service.RebalancePlanResult, error) {
			return nil, errors.New("plan failed")
		},
	}
	client, cleanup := startTestServerFull(t, WithRebalanceService(svc))
	defer cleanup()

	_, err := client.RebalanceCreatePlan(context.Background(), &pb.RebalancePlanRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// RebalanceExecute
// ---------------------------------------------------------------------------

func TestGRPC_RebalanceExecute_Success(t *testing.T) {
	svc := &mockRebalanceService{}
	client, cleanup := startTestServerFull(t, WithRebalanceService(svc))
	defer cleanup()

	resp, err := client.RebalanceExecute(context.Background(), &pb.RebalancePlan{
		Moves: []*pb.RebalanceMove{
			{ShardId: 1, FromNode: "a", ToNode: "b", EstimatedBytes: 512},
		},
		CurrentImbalance: 0.4,
	})
	if err != nil {
		t.Fatalf("RebalanceExecute failed: %v", err)
	}
	if resp.GetMovesCompleted() != 1 {
		t.Fatalf("expected moves_completed=1, got %d", resp.GetMovesCompleted())
	}
}

func TestGRPC_RebalanceExecute_NilPlan(t *testing.T) {
	svc := &mockRebalanceService{}
	client, cleanup := startTestServerFull(t, WithRebalanceService(svc))
	defer cleanup()

	// nil plan is treated as an empty plan (protoToRebalancePlan handles nil)
	resp, err := client.RebalanceExecute(context.Background(), &pb.RebalancePlan{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetMovesCompleted() != 0 {
		t.Fatalf("expected 0 moves, got %d", resp.GetMovesCompleted())
	}
}

func TestGRPC_RebalanceExecute_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.RebalanceExecute(context.Background(), &pb.RebalancePlan{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_RebalanceExecute_Error(t *testing.T) {
	svc := &mockRebalanceService{
		executeFn: func(_ context.Context, _ *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error) {
			return nil, errors.New("execute failed")
		},
	}
	client, cleanup := startTestServerFull(t, WithRebalanceService(svc))
	defer cleanup()

	_, err := client.RebalanceExecute(context.Background(), &pb.RebalancePlan{Moves: []*pb.RebalanceMove{{ShardId: 1}}})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// RebalanceGetImbalanceReport
// ---------------------------------------------------------------------------

func TestGRPC_RebalanceGetImbalanceReport_Success(t *testing.T) {
	svc := &mockRebalanceService{}
	client, cleanup := startTestServerFull(t, WithRebalanceService(svc))
	defer cleanup()

	resp, err := client.RebalanceGetImbalanceReport(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("RebalanceGetImbalanceReport failed: %v", err)
	}
	if len(resp.GetNodes()) != 1 {
		t.Fatalf("expected 1 node, got %d", len(resp.GetNodes()))
	}
}

func TestGRPC_RebalanceGetImbalanceReport_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.RebalanceGetImbalanceReport(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_RebalanceGetImbalanceReport_Error(t *testing.T) {
	svc := &mockRebalanceService{
		reportFn: func(_ context.Context) (*service.ImbalanceReportResult, error) {
			return nil, errors.New("report failed")
		},
	}
	client, cleanup := startTestServerFull(t, WithRebalanceService(svc))
	defer cleanup()

	_, err := client.RebalanceGetImbalanceReport(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// rebalancePlanToProto / protoToRebalancePlan (direct unit tests)
// ---------------------------------------------------------------------------

func TestRebalancePlanToProto_Nil(t *testing.T) {
	if got := rebalancePlanToProto(nil); got != nil {
		t.Fatalf("expected nil for nil input, got %v", got)
	}
}

func TestRebalancePlanToProto_WithMoves(t *testing.T) {
	plan := &service.RebalancePlanResult{
		Moves: []service.RebalanceMovePlan{
			{ShardID: 7, FromNode: "n1", ToNode: "n2", EstimatedBytes: 2048},
		},
		EstimatedDuration: 3 * time.Second,
		CurrentImbalance:  0.35,
	}
	pb := rebalancePlanToProto(plan)
	if len(pb.GetMoves()) != 1 {
		t.Fatalf("expected 1 move, got %d", len(pb.GetMoves()))
	}
	if pb.GetMoves()[0].GetShardId() != 7 {
		t.Errorf("expected shard_id=7, got %d", pb.GetMoves()[0].GetShardId())
	}
}

func TestProtoToRebalancePlan_Nil(t *testing.T) {
	if got := protoToRebalancePlan(nil); got != nil {
		t.Fatalf("expected nil for nil input, got %v", got)
	}
}

func TestProtoToRebalancePlan_WithMoves(t *testing.T) {
	proto := &pb.RebalancePlan{
		Moves: []*pb.RebalanceMove{
			{ShardId: 3, FromNode: "a", ToNode: "b", EstimatedBytes: 512},
		},
		EstimatedDurationNs: int64(5 * time.Second),
		CurrentImbalance:    0.25,
	}
	plan := protoToRebalancePlan(proto)
	if len(plan.Moves) != 1 {
		t.Fatalf("expected 1 move, got %d", len(plan.Moves))
	}
	if plan.Moves[0].ShardID != 3 {
		t.Errorf("expected ShardID=3, got %d", plan.Moves[0].ShardID)
	}
}

// ---------------------------------------------------------------------------
// RAGIngestDocument
// ---------------------------------------------------------------------------

func TestGRPC_RAGIngestDocument_Success(t *testing.T) {
	svc := &mockRAGService{}
	client, cleanup := startTestServerFull(t, WithRAGService(svc))
	defer cleanup()

	resp, err := client.RAGIngestDocument(context.Background(), &pb.RAGIngestDocumentRequest{
		DocumentId: "doc1",
		Text:       "hello world",
		Namespace:  "ns1",
		Model:      "all-minilm",
	})
	if err != nil {
		t.Fatalf("RAGIngestDocument failed: %v", err)
	}
	if resp.GetDocumentId() != "doc1" {
		t.Fatalf("expected doc_id 'doc1', got %q", resp.GetDocumentId())
	}
	if resp.GetChunkCount() != 3 {
		t.Fatalf("expected chunk_count=3, got %d", resp.GetChunkCount())
	}
}

func TestGRPC_RAGIngestDocument_WithChunkConfig(t *testing.T) {
	var capturedReq *service.IngestDocumentRequest
	svc := &mockRAGService{
		ingestFn: func(_ context.Context, req *service.IngestDocumentRequest) (*service.IngestDocumentResponse, error) {
			capturedReq = req
			return &service.IngestDocumentResponse{DocumentID: req.DocumentID, ChunkCount: 1, Namespace: req.Namespace}, nil
		},
	}
	client, cleanup := startTestServerFull(t, WithRAGService(svc))
	defer cleanup()

	_, err := client.RAGIngestDocument(context.Background(), &pb.RAGIngestDocumentRequest{
		DocumentId: "d1",
		Text:       "text",
		ChunkConfig: &pb.ChunkConfig{
			Strategy:  "sentence",
			ChunkSize: 512,
			Overlap:   64,
			MinSize:   128,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedReq.ChunkConfig == nil {
		t.Fatal("expected ChunkConfig to be set")
	}
	if capturedReq.ChunkConfig.Strategy != "sentence" {
		t.Errorf("expected strategy 'sentence', got %q", capturedReq.ChunkConfig.Strategy)
	}
}

func TestGRPC_RAGIngestDocument_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.RAGIngestDocument(context.Background(), &pb.RAGIngestDocumentRequest{DocumentId: "d"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_RAGIngestDocument_Error(t *testing.T) {
	svc := &mockRAGService{
		ingestFn: func(_ context.Context, _ *service.IngestDocumentRequest) (*service.IngestDocumentResponse, error) {
			return nil, errors.New("ingest failed")
		},
	}
	client, cleanup := startTestServerFull(t, WithRAGService(svc))
	defer cleanup()

	_, err := client.RAGIngestDocument(context.Background(), &pb.RAGIngestDocumentRequest{DocumentId: "d"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// protoToChunkConfig (direct unit test)
// ---------------------------------------------------------------------------

func TestProtoToChunkConfig_Nil(t *testing.T) {
	if got := protoToChunkConfig(nil); got != nil {
		t.Fatalf("expected nil for nil input, got %v", got)
	}
}

func TestProtoToChunkConfig_Full(t *testing.T) {
	cfg := protoToChunkConfig(&pb.ChunkConfig{
		Strategy:  "fixed",
		ChunkSize: 256,
		Overlap:   32,
		MinSize:   64,
	})
	if cfg.Strategy != "fixed" {
		t.Errorf("expected strategy 'fixed', got %q", cfg.Strategy)
	}
	if cfg.ChunkSize != 256 {
		t.Errorf("expected chunk_size=256, got %d", cfg.ChunkSize)
	}
}

// ---------------------------------------------------------------------------
// RAGQuery
// ---------------------------------------------------------------------------

func TestGRPC_RAGQuery_Success(t *testing.T) {
	svc := &mockRAGService{}
	client, cleanup := startTestServerFull(t, WithRAGService(svc))
	defer cleanup()

	resp, err := client.RAGQuery(context.Background(), &pb.RAGQueryRequest{
		Query:     "what is qrdb",
		Namespace: "ns1",
		TopK:      5,
		Model:     "all-minilm",
	})
	if err != nil {
		t.Fatalf("RAGQuery failed: %v", err)
	}
	if len(resp.GetResults()) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.GetResults()))
	}
	if resp.GetResults()[0].GetScore() != 0.9 {
		t.Errorf("expected score=0.9, got %f", resp.GetResults()[0].GetScore())
	}
}

func TestGRPC_RAGQuery_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.RAGQuery(context.Background(), &pb.RAGQueryRequest{Query: "q"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_RAGQuery_Error(t *testing.T) {
	svc := &mockRAGService{
		queryFn: func(_ context.Context, _ *service.RAGQueryRequest) (*service.RAGQueryResponse, error) {
			return nil, errors.New("query failed")
		},
	}
	client, cleanup := startTestServerFull(t, WithRAGService(svc))
	defer cleanup()

	_, err := client.RAGQuery(context.Background(), &pb.RAGQueryRequest{Query: "q"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// RAGDeleteDocument
// ---------------------------------------------------------------------------

func TestGRPC_RAGDeleteDocument_Success(t *testing.T) {
	svc := &mockRAGService{}
	client, cleanup := startTestServerFull(t, WithRAGService(svc))
	defer cleanup()

	resp, err := client.RAGDeleteDocument(context.Background(), &pb.RAGDeleteDocumentRequest{
		DocumentId: "doc1",
		Namespace:  "ns1",
	})
	if err != nil {
		t.Fatalf("RAGDeleteDocument failed: %v", err)
	}
	if resp.GetDocumentId() != "doc1" {
		t.Fatalf("expected doc_id 'doc1', got %q", resp.GetDocumentId())
	}
	if resp.GetChunksRemoved() != 3 {
		t.Fatalf("expected chunks_removed=3, got %d", resp.GetChunksRemoved())
	}
}

func TestGRPC_RAGDeleteDocument_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.RAGDeleteDocument(context.Background(), &pb.RAGDeleteDocumentRequest{DocumentId: "d"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_RAGDeleteDocument_Error(t *testing.T) {
	svc := &mockRAGService{
		deleteFn: func(_ context.Context, _ *service.DeleteRAGDocumentRequest) (*service.DeleteRAGDocumentResponse, error) {
			return nil, errors.New("delete failed")
		},
	}
	client, cleanup := startTestServerFull(t, WithRAGService(svc))
	defer cleanup()

	_, err := client.RAGDeleteDocument(context.Background(), &pb.RAGDeleteDocumentRequest{DocumentId: "d"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// RAGListDocuments
// ---------------------------------------------------------------------------

func TestGRPC_RAGListDocuments_Success(t *testing.T) {
	svc := &mockRAGService{}
	client, cleanup := startTestServerFull(t, WithRAGService(svc))
	defer cleanup()

	resp, err := client.RAGListDocuments(context.Background(), &pb.RAGListDocumentsRequest{
		Namespace: "ns1",
		Limit:     10,
		Offset:    0,
	})
	if err != nil {
		t.Fatalf("RAGListDocuments failed: %v", err)
	}
	if len(resp.GetDocuments()) != 1 {
		t.Fatalf("expected 1 document, got %d", len(resp.GetDocuments()))
	}
	if resp.GetTotal() != 1 {
		t.Fatalf("expected total=1, got %d", resp.GetTotal())
	}
}

func TestGRPC_RAGListDocuments_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.RAGListDocuments(context.Background(), &pb.RAGListDocumentsRequest{Namespace: "ns"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_RAGListDocuments_Error(t *testing.T) {
	svc := &mockRAGService{
		listFn: func(_ context.Context, _ *service.ListRAGDocumentsRequest) (*service.ListRAGDocumentsResponse, error) {
			return nil, errors.New("list failed")
		},
	}
	client, cleanup := startTestServerFull(t, WithRAGService(svc))
	defer cleanup()

	_, err := client.RAGListDocuments(context.Background(), &pb.RAGListDocumentsRequest{Namespace: "ns"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// CredentialStore / CredentialRetrieve (0% covered in grpc package)
// ---------------------------------------------------------------------------

func TestCredentialStore_Success_Proto(t *testing.T) {
	var capturedName string
	var capturedValue []byte
	svc := &fakeCredentialServicerProto{
		storeFn: func(_ context.Context, name string, value []byte) error {
			capturedName = name
			capturedValue = value
			return nil
		},
	}
	s := &qrdbServiceServer{credentialSvc: svc}

	_, err := s.CredentialStore(context.Background(), &pb.CredentialStoreRequest{Name: "db-pass", Value: []byte("s3cret")})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedName != "db-pass" {
		t.Errorf("expected name 'db-pass', got %q", capturedName)
	}
	if string(capturedValue) != "s3cret" {
		t.Errorf("expected value 's3cret', got %q", capturedValue)
	}
}

func TestCredentialStore_NilService_Proto(t *testing.T) {
	s := &qrdbServiceServer{}
	_, err := s.CredentialStore(context.Background(), &pb.CredentialStoreRequest{Name: "n"})
	if codeOf(err) != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", codeOf(err))
	}
}

func TestCredentialStore_Error_Proto(t *testing.T) {
	svc := &fakeCredentialServicerProto{
		storeFn: func(_ context.Context, _ string, _ []byte) error {
			return errors.New("store failed")
		},
	}
	s := &qrdbServiceServer{credentialSvc: svc}
	_, err := s.CredentialStore(context.Background(), &pb.CredentialStoreRequest{Name: "n", Value: []byte("v")})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCredentialRetrieve_Success_Proto(t *testing.T) {
	svc := &fakeCredentialServicerProto{
		retrieveFn: func(_ context.Context, name string) ([]byte, error) {
			if name != "db-pass" {
				return nil, errors.New("not found")
			}
			return []byte("s3cret"), nil
		},
	}
	s := &qrdbServiceServer{credentialSvc: svc}

	resp, err := s.CredentialRetrieve(context.Background(), &pb.CredentialRetrieveRequest{Name: "db-pass"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(resp.GetValue()) != "s3cret" {
		t.Errorf("expected value 's3cret', got %q", resp.GetValue())
	}
}

func TestCredentialRetrieve_NilService_Proto(t *testing.T) {
	s := &qrdbServiceServer{}
	_, err := s.CredentialRetrieve(context.Background(), &pb.CredentialRetrieveRequest{Name: "n"})
	if codeOf(err) != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", codeOf(err))
	}
}

func TestCredentialRetrieve_Error_Proto(t *testing.T) {
	svc := &fakeCredentialServicerProto{
		retrieveFn: func(_ context.Context, _ string) ([]byte, error) {
			return nil, errors.New("retrieve failed")
		},
	}
	s := &qrdbServiceServer{credentialSvc: svc}
	_, err := s.CredentialRetrieve(context.Background(), &pb.CredentialRetrieveRequest{Name: "n"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// DatabaseSeal / DatabaseUnseal / DatabaseBarrierStatus (0% covered)
// ---------------------------------------------------------------------------

func TestGRPC_DatabaseSeal_Success(t *testing.T) {
	svc := newMockDatabaseService()
	client, cleanup := startTestServerFull(t, WithDatabaseService(svc))
	defer cleanup()

	_, err := client.DatabaseSeal(context.Background(), &pb.DatabaseSealRequest{Name: "testdb"})
	if err != nil {
		t.Fatalf("DatabaseSeal failed: %v", err)
	}
}

func TestGRPC_DatabaseSeal_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.DatabaseSeal(context.Background(), &pb.DatabaseSealRequest{Name: "testdb"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_DatabaseSeal_Error(t *testing.T) {
	svc := newMockDatabaseService()
	svc.dropErr = errors.New("seal failed") // Use dropErr as a proxy; we need SealDatabase to error.
	// Inline mock override:
	type sealableDB struct {
		*mockDatabaseService
	}
	_ = sealableDB{}

	// Use a direct server call to inject error
	server := &qrdbServiceServer{databaseSvc: &sealErrorDatabaseService{}}
	_, err := server.DatabaseSeal(context.Background(), &pb.DatabaseSealRequest{Name: "testdb"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

type sealErrorDatabaseService struct {
	mockDatabaseService
}

func (s *sealErrorDatabaseService) SealDatabase(_ context.Context, _ string) error {
	return errors.New("seal failed")
}

func TestGRPC_DatabaseUnseal_Success(t *testing.T) {
	svc := newMockDatabaseService()
	client, cleanup := startTestServerFull(t, WithDatabaseService(svc))
	defer cleanup()

	_, err := client.DatabaseUnseal(context.Background(), &pb.DatabaseUnsealRequest{Name: "testdb", Secret: "mysecret"})
	if err != nil {
		t.Fatalf("DatabaseUnseal failed: %v", err)
	}
}

func TestGRPC_DatabaseUnseal_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.DatabaseUnseal(context.Background(), &pb.DatabaseUnsealRequest{Name: "testdb"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_DatabaseUnseal_Error(t *testing.T) {
	server := &qrdbServiceServer{databaseSvc: &unsealErrorDatabaseService{}}
	_, err := server.DatabaseUnseal(context.Background(), &pb.DatabaseUnsealRequest{Name: "testdb", Secret: "bad"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

type unsealErrorDatabaseService struct {
	mockDatabaseService
}

func (s *unsealErrorDatabaseService) UnsealDatabase(_ context.Context, _ string, _ seal.Credentials) error {
	return errors.New("unseal failed")
}

func TestGRPC_DatabaseBarrierStatus_Success(t *testing.T) {
	svc := newMockDatabaseService()
	client, cleanup := startTestServerFull(t, WithDatabaseService(svc))
	defer cleanup()

	resp, err := client.DatabaseBarrierStatus(context.Background(), &pb.DatabaseBarrierStatusRequest{Name: "testdb"})
	if err != nil {
		t.Fatalf("DatabaseBarrierStatus failed: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestGRPC_DatabaseBarrierStatus_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.DatabaseBarrierStatus(context.Background(), &pb.DatabaseBarrierStatusRequest{Name: "testdb"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_DatabaseBarrierStatus_NilBarrierStatus(t *testing.T) {
	// databaseBarrierStatusToProto should handle nil gracefully.
	resp := databaseBarrierStatusToProto(nil)
	if resp == nil {
		t.Fatal("expected non-nil response for nil input")
	}
	if resp.GetSealed() {
		t.Fatal("expected sealed=false for nil input")
	}
}

// ---------------------------------------------------------------------------
// CeremonyClaimShareBegin / CeremonyClaimShareComplete / CeremonySubmitUnsealShare
// (0% covered in the custodian file)
// ---------------------------------------------------------------------------

func TestGRPC_CeremonyClaimShareBegin_Success(t *testing.T) {
	svc := &mockCeremonyServiceWithCustodian{}
	client, cleanup := startTestServerFull(t, WithCeremonyService(svc))
	defer cleanup()

	resp, err := client.CeremonyClaimShareBegin(context.Background(), &pb.CeremonyClaimShareBeginRequest{
		CustodianId: "custodian-1",
	})
	if err != nil {
		t.Fatalf("CeremonyClaimShareBegin failed: %v", err)
	}
	if resp.GetCustodianId() != "custodian-1" {
		t.Fatalf("expected custodian_id 'custodian-1', got %q", resp.GetCustodianId())
	}
}

func TestGRPC_CeremonyClaimShareBegin_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.CeremonyClaimShareBegin(context.Background(), &pb.CeremonyClaimShareBeginRequest{CustodianId: "c"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_CeremonyClaimShareBegin_Error(t *testing.T) {
	svc := &mockCeremonyServiceWithCustodian{
		beginFn: func(_ context.Context, _ string) (*service.CeremonyCustodianChallenge, error) {
			return nil, errors.New("begin failed")
		},
	}
	client, cleanup := startTestServerFull(t, WithCeremonyService(svc))
	defer cleanup()

	_, err := client.CeremonyClaimShareBegin(context.Background(), &pb.CeremonyClaimShareBeginRequest{CustodianId: "c"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_CeremonyClaimShareComplete_Success(t *testing.T) {
	svc := &mockCeremonyServiceWithCustodian{}
	client, cleanup := startTestServerFull(t, WithCeremonyService(svc))
	defer cleanup()

	resp, err := client.CeremonyClaimShareComplete(context.Background(), &pb.CeremonyClaimShareCompleteRequest{
		CustodianId:    "custodian-1",
		ChallengeId:    "challenge-abc",
		FactorResponse: []byte("response"),
	})
	if err != nil {
		t.Fatalf("CeremonyClaimShareComplete failed: %v", err)
	}
	if resp.GetUsername() != "alice" {
		t.Fatalf("expected username 'alice', got %q", resp.GetUsername())
	}
}

func TestGRPC_CeremonyClaimShareComplete_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.CeremonyClaimShareComplete(context.Background(), &pb.CeremonyClaimShareCompleteRequest{CustodianId: "c"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_CeremonyClaimShareComplete_Error(t *testing.T) {
	svc := &mockCeremonyServiceWithCustodian{
		completeFn: func(_ context.Context, _, _ string, _ []byte) (*custodian.Custodian, error) {
			return nil, errors.New("complete failed")
		},
	}
	client, cleanup := startTestServerFull(t, WithCeremonyService(svc))
	defer cleanup()

	_, err := client.CeremonyClaimShareComplete(context.Background(), &pb.CeremonyClaimShareCompleteRequest{CustodianId: "c"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_CeremonySubmitUnsealShare_Success(t *testing.T) {
	svc := &mockCeremonyServiceWithCustodian{}
	client, cleanup := startTestServerFull(t, WithCeremonyService(svc))
	defer cleanup()

	resp, err := client.CeremonySubmitUnsealShare(context.Background(), &pb.CeremonySubmitUnsealShareRequest{
		CustodianId:    "custodian-1",
		ChallengeId:    "challenge-abc",
		FactorResponse: []byte("response"),
		ShareBytes:     []byte("shamir-share"),
	})
	if err != nil {
		t.Fatalf("CeremonySubmitUnsealShare failed: %v", err)
	}
	if resp.GetSubmitted() != 1 {
		t.Fatalf("expected submitted=1, got %d", resp.GetSubmitted())
	}
	if resp.GetRequired() != 2 {
		t.Fatalf("expected required=2, got %d", resp.GetRequired())
	}
	if resp.GetUnsealed() {
		t.Fatal("expected unsealed=false")
	}
}

func TestGRPC_CeremonySubmitUnsealShare_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.CeremonySubmitUnsealShare(context.Background(), &pb.CeremonySubmitUnsealShareRequest{CustodianId: "c"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_CeremonySubmitUnsealShare_Error(t *testing.T) {
	svc := &mockCeremonyServiceWithCustodian{
		submitFn: func(_ context.Context, _, _ string, _, _ []byte) (*service.CeremonyUnsealShareResult, error) {
			return nil, errors.New("submit failed")
		},
	}
	client, cleanup := startTestServerFull(t, WithCeremonyService(svc))
	defer cleanup()

	_, err := client.CeremonySubmitUnsealShare(context.Background(), &pb.CeremonySubmitUnsealShareRequest{CustodianId: "c"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// custodianToProto (direct unit test for grpc package version)
// ---------------------------------------------------------------------------

func TestCustodianToProto_Nil(t *testing.T) {
	resp := custodianToProto(nil)
	if resp == nil {
		t.Fatal("expected non-nil response for nil input")
	}
}

func TestCustodianToProto_WithRevokedAt(t *testing.T) {
	revokedAt := time.Unix(1700000000, 0)
	claimedAt := time.Unix(1700001000, 0)
	c := &custodian.Custodian{
		ID:             "c1",
		Username:       "bob",
		TenantID:       "t1",
		Method:         "software",
		EnrollmentID:   "enroll-1",
		ShareIndex:     2,
		EnrolledAt:     time.Unix(1699999000, 0),
		ShareClaimed:   true,
		RevokedAt:      &revokedAt,
		ShareClaimedAt: &claimedAt,
	}
	resp := custodianToProto(c)
	if resp.GetId() != "c1" {
		t.Errorf("expected id 'c1', got %q", resp.GetId())
	}
	if resp.GetRevokedAtUnix() != revokedAt.Unix() {
		t.Errorf("expected revoked_at_unix=%d, got %d", revokedAt.Unix(), resp.GetRevokedAtUnix())
	}
	if resp.GetShareClaimedAtUnix() != claimedAt.Unix() {
		t.Errorf("expected share_claimed_at_unix=%d, got %d", claimedAt.Unix(), resp.GetShareClaimedAtUnix())
	}
}

// ---------------------------------------------------------------------------
// CeremonyEnrollExternalOfficer (0% in service_ceremony.go)
// ---------------------------------------------------------------------------

func TestGRPC_CeremonyEnrollExternalOfficer_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.CeremonyEnrollExternalOfficer(context.Background(), &pb.CeremonyEnrollExternalOfficerRequest{
		Username: "external-officer",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}
