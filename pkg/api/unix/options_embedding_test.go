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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Stub EmbeddingServicer
// ---------------------------------------------------------------------------

type stubEmbeddingSvc struct{}

var _ service.EmbeddingServicer = (*stubEmbeddingSvc)(nil)

func (s *stubEmbeddingSvc) Embed(_ context.Context, _ *service.EmbedRequest) (*service.EmbedResponse, error) {
	return nil, nil
}
func (s *stubEmbeddingSvc) BatchEmbed(_ context.Context, _ *service.BatchEmbedRequest) (*service.BatchEmbedResponse, error) {
	return nil, nil
}
func (s *stubEmbeddingSvc) ListModels(_ context.Context, _ *service.ListEmbeddingModelsRequest) (*service.ListEmbeddingModelsResponse, error) {
	return nil, nil
}
func (s *stubEmbeddingSvc) RegisterProvider(_ string, _ service.EmbeddingProvider) error {
	return nil
}
func (s *stubEmbeddingSvc) DeregisterProvider(_ string) error { return nil }
func (s *stubEmbeddingSvc) Close() error                      { return nil }

// ---------------------------------------------------------------------------
// Stub RAGServicer
// ---------------------------------------------------------------------------

type stubRAGSvc struct{}

var _ service.RAGServicer = (*stubRAGSvc)(nil)

func (s *stubRAGSvc) IngestDocument(_ context.Context, _ *service.IngestDocumentRequest) (*service.IngestDocumentResponse, error) {
	return nil, nil
}
func (s *stubRAGSvc) Query(_ context.Context, _ *service.RAGQueryRequest) (*service.RAGQueryResponse, error) {
	return nil, nil
}
func (s *stubRAGSvc) DeleteDocument(_ context.Context, _ *service.DeleteRAGDocumentRequest) (*service.DeleteRAGDocumentResponse, error) {
	return nil, nil
}
func (s *stubRAGSvc) ListDocuments(_ context.Context, _ *service.ListRAGDocumentsRequest) (*service.ListRAGDocumentsResponse, error) {
	return nil, nil
}
func (s *stubRAGSvc) Close() error { return nil }

// ---------------------------------------------------------------------------
// Stub BackupServicer
// ---------------------------------------------------------------------------

type stubBackupSvcUnix struct{}

var _ service.BackupServicer = (*stubBackupSvcUnix)(nil)

func (s *stubBackupSvcUnix) CreateBackup(_ context.Context, _ service.BackupOptions) (*service.BackupInfo, error) {
	return nil, nil
}
func (s *stubBackupSvcUnix) ListBackups(_ context.Context) ([]service.BackupInfo, error) {
	return nil, nil
}
func (s *stubBackupSvcUnix) GetBackup(_ context.Context, _ string) (*service.BackupInfo, error) {
	return nil, nil
}
func (s *stubBackupSvcUnix) DeleteBackup(_ context.Context, _ string) error { return nil }
func (s *stubBackupSvcUnix) RestoreBackup(_ context.Context, _ service.RestoreOptions) (*service.RestoreResult, error) {
	return nil, nil
}
func (s *stubBackupSvcUnix) VerifyBackup(_ context.Context, _ string) (*service.VerifyResult, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// Stub RebalanceServicer
// ---------------------------------------------------------------------------

type stubRebalanceSvcUnix struct{}

var _ service.RebalanceServicer = (*stubRebalanceSvcUnix)(nil)

func (s *stubRebalanceSvcUnix) GetShardStats(_ context.Context, _ uint64) (*service.ShardStats, error) {
	return nil, nil
}
func (s *stubRebalanceSvcUnix) CreateRebalancePlan(_ context.Context, _ *service.RebalancePlanOptions) (*service.RebalancePlanResult, error) {
	return nil, nil
}
func (s *stubRebalanceSvcUnix) ExecuteRebalance(_ context.Context, _ *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error) {
	return nil, nil
}
func (s *stubRebalanceSvcUnix) GetImbalanceReport(_ context.Context) (*service.ImbalanceReportResult, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// WithEmbedding option
// ---------------------------------------------------------------------------

func TestWithEmbedding_Applied(t *testing.T) {
	embSvc := &stubEmbeddingSvc{}
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-emb.sock",
		WithEmbedding(embSvc),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.embeddingSvc == nil {
		t.Fatal("embeddingSvc not set")
	}
}

func TestWithEmbedding_Nil(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-emb-nil.sock",
		WithEmbedding(nil),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.embeddingSvc != nil {
		t.Fatal("expected nil embeddingSvc")
	}
}

// ---------------------------------------------------------------------------
// WithRAGService option
// ---------------------------------------------------------------------------

func TestWithRAGService_Applied(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-rag.sock",
		WithRAGService(&stubRAGSvc{}),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.ragSvc == nil {
		t.Fatal("ragSvc not set")
	}
}

func TestWithRAGService_Nil(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-rag-nil.sock",
		WithRAGService(nil),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.ragSvc != nil {
		t.Fatal("expected nil ragSvc")
	}
}

// ---------------------------------------------------------------------------
// WithBackupService option
// ---------------------------------------------------------------------------

func TestWithBackupService_Applied(t *testing.T) {
	bkpSvc := &stubBackupSvcUnix{}
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-bkp.sock",
		WithBackupService(bkpSvc),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.backupSvc != bkpSvc {
		t.Fatal("backupSvc not set")
	}
}

func TestWithBackupService_Nil(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-bkp-nil.sock",
		WithBackupService(nil),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.backupSvc != nil {
		t.Fatal("expected nil backupSvc")
	}
}

// ---------------------------------------------------------------------------
// WithRebalanceService option
// ---------------------------------------------------------------------------

func TestWithRebalanceService_Applied(t *testing.T) {
	rbSvc := &stubRebalanceSvcUnix{}
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-rb.sock",
		WithRebalanceService(rbSvc),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.rebalanceSvc != rbSvc {
		t.Fatal("rebalanceSvc not set")
	}
}

func TestWithRebalanceService_Nil(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-rb-nil.sock",
		WithRebalanceService(nil),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.rebalanceSvc != nil {
		t.Fatal("expected nil rebalanceSvc")
	}
}

// ---------------------------------------------------------------------------
// Start with embedding, RAG, backup, rebalance exercises all non-nil branches
// ---------------------------------------------------------------------------

func TestStart_WithEmbeddingAndRAGAndBackupAndRebalance(t *testing.T) {
	socketPath := tempSocketPath(t)
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath,
		WithEmbedding(&stubEmbeddingSvc{}),
		WithRAGService(&stubRAGSvc{}),
		WithBackupService(&stubBackupSvcUnix{}),
		WithRebalanceService(&stubRebalanceSvcUnix{}),
		WithTLS(selfSignedTLSConfig(t)))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	if srv.Addr() != socketPath {
		t.Errorf("Addr = %q, want %q", srv.Addr(), socketPath)
	}
}
