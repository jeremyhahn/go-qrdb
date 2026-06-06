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

// Coverage-uplift tests for the QUIC SDK transport targeting previously-uncovered
// code paths: backup, database barrier, embedding, RAG, rebalance,
// GetUserByCertThumbprint, AddCertBinding, closed-path guards, and helper
// functions quicProtoToBackupInfo / quicCustodianToInfo / protoToRebalancePlan
// / quicEmbeddingsToMatrix / quicEmbeddingUsage.

package quic

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newQuicClosed returns a Transport that is already closed, used to exercise
// the t.closed.Load() guards without starting any server.
func newQuicClosed() *Transport {
	tr := &Transport{config: &transport.Config{}}
	tr.closed.Store(true)
	return tr
}

func assertQuicClosed(t *testing.T, err error) {
	t.Helper()
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

func assertQuicRequestError(t *testing.T, err error) {
	t.Helper()
	var re *transport.RequestError
	if !errors.As(err, &re) {
		t.Fatalf("expected RequestError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Backup — success, closed, rpc-error, unmarshal-error
// ---------------------------------------------------------------------------

func TestE2E_Backup_CreateBackup_Success(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("backup.Create", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.BackupInfo{
			Id: "bk-1", TimestampNs: time.Now().UnixNano(), Size: 4096, ShardCount: 2,
		})
	})
	info, err := tr.CreateBackup(context.Background(), &transport.BackupRequest{
		Destination: "/tmp/bk", ShardIDs: []uint64{1, 2},
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	if info.ID != "bk-1" {
		t.Fatalf("unexpected ID: %q", info.ID)
	}
}

func TestE2E_Backup_CreateBackup_Closed(t *testing.T) {
	_, err := newQuicClosed().CreateBackup(context.Background(), &transport.BackupRequest{})
	assertQuicClosed(t, err)
}

func TestE2E_Backup_CreateBackup_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("backup.Create", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("disk full")
	})
	_, err := tr.CreateBackup(context.Background(), &transport.BackupRequest{Destination: "/x"})
	assertQuicRequestError(t, err)
}

func TestE2E_Backup_CreateBackup_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("backup.Create", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, []byte("not-valid-proto")
	})
	_, err := tr.CreateBackup(context.Background(), &transport.BackupRequest{Destination: "/x"})
	assertQuicRequestError(t, err)
}

func TestE2E_Backup_ListBackups_Success(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("backup.List", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.BackupListResponse{
			Backups: []*pb.BackupInfo{{Id: "bk-1"}, {Id: "bk-2"}},
		})
	})
	list, err := tr.ListBackups(context.Background())
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 backups, got %d", len(list))
	}
}

func TestE2E_Backup_ListBackups_Closed(t *testing.T) {
	_, err := newQuicClosed().ListBackups(context.Background())
	assertQuicClosed(t, err)
}

func TestE2E_Backup_ListBackups_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("backup.List", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("err")
	})
	_, err := tr.ListBackups(context.Background())
	assertQuicRequestError(t, err)
}

func TestE2E_Backup_GetBackup_Success(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("backup.Get", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.BackupInfo{Id: "bk-42", Size: 1024})
	})
	info, err := tr.GetBackup(context.Background(), "bk-42")
	if err != nil {
		t.Fatalf("GetBackup: %v", err)
	}
	if info.ID != "bk-42" {
		t.Fatalf("unexpected ID: %q", info.ID)
	}
}

func TestE2E_Backup_GetBackup_Closed(t *testing.T) {
	_, err := newQuicClosed().GetBackup(context.Background(), "id")
	assertQuicClosed(t, err)
}

func TestE2E_Backup_GetBackup_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("backup.Get", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("not found")
	})
	_, err := tr.GetBackup(context.Background(), "missing")
	assertQuicRequestError(t, err)
}

func TestE2E_Backup_DeleteBackup_Success(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("backup.Delete", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.DeleteBackup(context.Background(), "bk-1"); err != nil {
		t.Fatalf("DeleteBackup: %v", err)
	}
}

func TestE2E_Backup_DeleteBackup_Closed(t *testing.T) {
	err := newQuicClosed().DeleteBackup(context.Background(), "id")
	assertQuicClosed(t, err)
}

func TestE2E_Backup_DeleteBackup_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("backup.Delete", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("access denied")
	})
	if err := tr.DeleteBackup(context.Background(), "bk-1"); err == nil {
		t.Fatal("expected error")
	}
}

func TestE2E_Backup_RestoreBackup_Success(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("backup.Restore", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.BackupRestoreResponse{
			BackupId: "bk-1", ShardsRestored: 3, DurationNs: 1_000_000_000,
		})
	})
	result, err := tr.RestoreBackup(context.Background(), &transport.RestoreRequest{
		BackupID: "bk-1", TargetShards: []uint64{1, 2, 3},
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if result.ShardsRestored != 3 {
		t.Fatalf("unexpected ShardsRestored: %d", result.ShardsRestored)
	}
}

func TestE2E_Backup_RestoreBackup_Closed(t *testing.T) {
	_, err := newQuicClosed().RestoreBackup(context.Background(), &transport.RestoreRequest{})
	assertQuicClosed(t, err)
}

func TestE2E_Backup_RestoreBackup_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("backup.Restore", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("restore failed")
	})
	_, err := tr.RestoreBackup(context.Background(), &transport.RestoreRequest{BackupID: "x"})
	assertQuicRequestError(t, err)
}

func TestE2E_Backup_VerifyBackup_Success(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("backup.Verify", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.BackupVerifyResponse{
			Valid: true, ChecksumMatch: true,
			ExpectedChecksum: "abc", ActualChecksum: "abc",
		})
	})
	result, err := tr.VerifyBackup(context.Background(), "bk-1")
	if err != nil {
		t.Fatalf("VerifyBackup: %v", err)
	}
	if !result.Valid {
		t.Fatal("expected Valid=true")
	}
}

func TestE2E_Backup_VerifyBackup_Closed(t *testing.T) {
	_, err := newQuicClosed().VerifyBackup(context.Background(), "id")
	assertQuicClosed(t, err)
}

func TestE2E_Backup_VerifyBackup_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("backup.Verify", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("checksum mismatch")
	})
	_, err := tr.VerifyBackup(context.Background(), "bk-1")
	assertQuicRequestError(t, err)
}

// quicProtoToBackupInfo nil guard
func TestE2E_Backup_QuicProtoToBackupInfo_Nil(t *testing.T) {
	if got := quicProtoToBackupInfo(nil); got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

// ---------------------------------------------------------------------------
// DatabaseBarrier — success, closed, rpc-error, unmarshal-error
// ---------------------------------------------------------------------------

func TestE2E_DatabaseBarrier_SealDatabase_Success(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("db.Seal", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.SealDatabase(context.Background(), "mydb"); err != nil {
		t.Fatalf("SealDatabase: %v", err)
	}
}

func TestE2E_DatabaseBarrier_SealDatabase_Closed(t *testing.T) {
	assertQuicClosed(t, newQuicClosed().SealDatabase(context.Background(), "mydb"))
}

func TestE2E_DatabaseBarrier_SealDatabase_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("db.Seal", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("forbidden")
	})
	if err := tr.SealDatabase(context.Background(), "mydb"); err == nil {
		t.Fatal("expected error")
	}
}

func TestE2E_DatabaseBarrier_UnsealDatabase_Success(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("db.Unseal", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.UnsealDatabase(context.Background(), "mydb", "secret"); err != nil {
		t.Fatalf("UnsealDatabase: %v", err)
	}
}

func TestE2E_DatabaseBarrier_UnsealDatabase_Closed(t *testing.T) {
	assertQuicClosed(t, newQuicClosed().UnsealDatabase(context.Background(), "mydb", "s"))
}

func TestE2E_DatabaseBarrier_UnsealDatabase_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("db.Unseal", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("bad secret")
	})
	if err := tr.UnsealDatabase(context.Background(), "mydb", "wrong"); err == nil {
		t.Fatal("expected error")
	}
}

func TestE2E_DatabaseBarrier_Status_Success(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("db.BarrierStatus", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.DatabaseBarrierStatusResponse{
			Sealed: false, Strategy: "software", HardwareBacked: false,
			InitializedAtUnix: 1700000000,
		})
	})
	status, err := tr.DatabaseBarrierStatus(context.Background(), "mydb")
	if err != nil {
		t.Fatalf("DatabaseBarrierStatus: %v", err)
	}
	if status.Strategy != "software" {
		t.Fatalf("unexpected strategy: %q", status.Strategy)
	}
}

func TestE2E_DatabaseBarrier_Status_Closed(t *testing.T) {
	_, err := newQuicClosed().DatabaseBarrierStatus(context.Background(), "mydb")
	assertQuicClosed(t, err)
}

func TestE2E_DatabaseBarrier_Status_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("db.BarrierStatus", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("unavailable")
	})
	_, err := tr.DatabaseBarrierStatus(context.Background(), "mydb")
	assertQuicRequestError(t, err)
}

func TestE2E_DatabaseBarrier_Status_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("db.BarrierStatus", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, []byte("not-proto")
	})
	_, err := tr.DatabaseBarrierStatus(context.Background(), "mydb")
	assertQuicRequestError(t, err)
}

// ---------------------------------------------------------------------------
// Embedding — success, closed, rpc-error, unmarshal-error, helpers
// ---------------------------------------------------------------------------

func TestE2E_Embedding_Embed_Success(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("embedding.Embed", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.EmbedResponse{
			Embeddings: []*pb.EmbeddingVector{{Values: []float32{0.1, 0.2}}},
			Model:      "ada-002",
			Usage:      &pb.EmbeddingUsage{PromptTokens: 5, TotalTokens: 5},
		})
	})
	resp, err := tr.Embed(context.Background(), &transport.EmbedRequest{
		Texts: []string{"hello"}, Model: "ada-002",
	})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(resp.Embeddings) != 1 {
		t.Fatalf("expected 1 embedding, got %d", len(resp.Embeddings))
	}
}

func TestE2E_Embedding_Embed_Closed(t *testing.T) {
	_, err := newQuicClosed().Embed(context.Background(), &transport.EmbedRequest{})
	assertQuicClosed(t, err)
}

func TestE2E_Embedding_Embed_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("embedding.Embed", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("model unavailable")
	})
	_, err := tr.Embed(context.Background(), &transport.EmbedRequest{Texts: []string{"x"}})
	assertQuicRequestError(t, err)
}

func TestE2E_Embedding_Embed_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("embedding.Embed", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, []byte("bad-proto")
	})
	_, err := tr.Embed(context.Background(), &transport.EmbedRequest{Texts: []string{"x"}})
	assertQuicRequestError(t, err)
}

func TestE2E_Embedding_BatchEmbed_Success(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("embedding.BatchEmbed", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.BatchEmbedResponse{
			Embeddings: []*pb.EmbeddingVector{{Values: []float32{0.3}}},
			Model:      "ada-002",
			TotalUsage: &pb.EmbeddingUsage{TotalTokens: 10},
		})
	})
	resp, err := tr.BatchEmbed(context.Background(), &transport.BatchEmbedRequest{
		Texts: []string{"a", "b"}, Model: "ada-002",
	})
	if err != nil {
		t.Fatalf("BatchEmbed: %v", err)
	}
	if len(resp.Embeddings) != 1 {
		t.Fatalf("expected 1, got %d", len(resp.Embeddings))
	}
}

func TestE2E_Embedding_BatchEmbed_Closed(t *testing.T) {
	_, err := newQuicClosed().BatchEmbed(context.Background(), &transport.BatchEmbedRequest{})
	assertQuicClosed(t, err)
}

func TestE2E_Embedding_BatchEmbed_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("embedding.BatchEmbed", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("batch too large")
	})
	_, err := tr.BatchEmbed(context.Background(), &transport.BatchEmbedRequest{Texts: []string{"x"}})
	assertQuicRequestError(t, err)
}

func TestE2E_Embedding_ListModels_Success(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("embedding.ListModels", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.ListEmbeddingModelsResponse{
			Models: []*pb.EmbeddingModelInfo{
				{Name: "text-embedding-ada-002", Dimensions: 1536, MaxTokens: 8192},
			},
		})
	})
	resp, err := tr.ListEmbeddingModels(context.Background(), "openai")
	if err != nil {
		t.Fatalf("ListEmbeddingModels: %v", err)
	}
	if len(resp.Models) == 0 {
		t.Fatal("expected models")
	}
}

func TestE2E_Embedding_ListModels_Closed(t *testing.T) {
	_, err := newQuicClosed().ListEmbeddingModels(context.Background(), "p")
	assertQuicClosed(t, err)
}

func TestE2E_Embedding_ListModels_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("embedding.ListModels", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("offline")
	})
	_, err := tr.ListEmbeddingModels(context.Background(), "openai")
	assertQuicRequestError(t, err)
}

// helper unit tests
func TestE2E_Embedding_QuicEmbeddingUsage_Nil(t *testing.T) {
	if got := quicEmbeddingUsage(nil); got != nil {
		t.Fatalf("expected nil UsageInfo for nil input, got %+v", got)
	}
}

func TestE2E_Embedding_QuicEmbeddingsToMatrix(t *testing.T) {
	vecs := []*pb.EmbeddingVector{{Values: []float32{1.0, 2.0}}, {Values: []float32{3.0}}}
	m := quicEmbeddingsToMatrix(vecs)
	if len(m) != 2 || len(m[0]) != 2 {
		t.Fatalf("unexpected matrix: %v", m)
	}
}

// ---------------------------------------------------------------------------
// RAG — success, closed, rpc-error, unmarshal-error, helper
// ---------------------------------------------------------------------------

func TestE2E_RAG_IngestDocument_Success(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("rag.IngestDocument", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.RAGIngestDocumentResponse{
			DocumentId: "doc-1", ChunkCount: 3, Namespace: "ns1",
		})
	})
	resp, err := tr.RAGIngestDocument(context.Background(), &transport.RAGIngestRequest{
		DocumentID: "doc-1", Text: "hello world", Namespace: "ns1",
		ChunkStrategy: "sentence", ChunkSize: 512,
	})
	if err != nil {
		t.Fatalf("RAGIngestDocument: %v", err)
	}
	if resp.DocumentID != "doc-1" {
		t.Fatalf("unexpected DocumentID: %q", resp.DocumentID)
	}
}

func TestE2E_RAG_IngestDocument_Closed(t *testing.T) {
	_, err := newQuicClosed().RAGIngestDocument(context.Background(), &transport.RAGIngestRequest{})
	assertQuicClosed(t, err)
}

func TestE2E_RAG_IngestDocument_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("rag.IngestDocument", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("ingest failed")
	})
	_, err := tr.RAGIngestDocument(context.Background(), &transport.RAGIngestRequest{DocumentID: "d1"})
	assertQuicRequestError(t, err)
}

func TestE2E_RAG_Query_Success(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("rag.Query", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.RAGQueryResponse{
			Results: []*pb.RAGQueryResult{
				{ChunkText: "relevant chunk", Score: 0.9, DocumentId: "doc-1"},
			},
		})
	})
	resp, err := tr.RAGQuery(context.Background(), &transport.RAGQueryRequest{
		Query: "what is QRDB", Namespace: "ns1", TopK: 5,
	})
	if err != nil {
		t.Fatalf("RAGQuery: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("expected results")
	}
}

func TestE2E_RAG_Query_Closed(t *testing.T) {
	_, err := newQuicClosed().RAGQuery(context.Background(), &transport.RAGQueryRequest{})
	assertQuicClosed(t, err)
}

func TestE2E_RAG_Query_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("rag.Query", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("query failed")
	})
	_, err := tr.RAGQuery(context.Background(), &transport.RAGQueryRequest{Query: "x"})
	assertQuicRequestError(t, err)
}

func TestE2E_RAG_DeleteDocument_Success(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("rag.DeleteDocument", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.RAGDeleteDocumentResponse{
			DocumentId: "doc-1", ChunksRemoved: 3,
		})
	})
	resp, err := tr.RAGDeleteDocument(context.Background(), &transport.RAGDeleteDocumentRequest{
		DocumentID: "doc-1", Namespace: "ns1",
	})
	if err != nil {
		t.Fatalf("RAGDeleteDocument: %v", err)
	}
	if resp.DocumentID != "doc-1" {
		t.Fatalf("unexpected DocumentID: %q", resp.DocumentID)
	}
}

func TestE2E_RAG_DeleteDocument_Closed(t *testing.T) {
	_, err := newQuicClosed().RAGDeleteDocument(context.Background(), &transport.RAGDeleteDocumentRequest{})
	assertQuicClosed(t, err)
}

func TestE2E_RAG_DeleteDocument_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("rag.DeleteDocument", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("delete failed")
	})
	_, err := tr.RAGDeleteDocument(context.Background(), &transport.RAGDeleteDocumentRequest{DocumentID: "d1"})
	assertQuicRequestError(t, err)
}

func TestE2E_RAG_ListDocuments_Success(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("rag.ListDocuments", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.RAGListDocumentsResponse{
			Documents: []*pb.RAGDocumentInfo{{Id: "doc-1", Namespace: "ns1", ChunkCount: 3}},
			Total:     1,
		})
	})
	resp, err := tr.RAGListDocuments(context.Background(), &transport.RAGListDocumentsRequest{Namespace: "ns1"})
	if err != nil {
		t.Fatalf("RAGListDocuments: %v", err)
	}
	if len(resp.Documents) == 0 {
		t.Fatal("expected documents")
	}
}

func TestE2E_RAG_ListDocuments_Closed(t *testing.T) {
	_, err := newQuicClosed().RAGListDocuments(context.Background(), &transport.RAGListDocumentsRequest{})
	assertQuicClosed(t, err)
}

func TestE2E_RAG_ListDocuments_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("rag.ListDocuments", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("list failed")
	})
	_, err := tr.RAGListDocuments(context.Background(), &transport.RAGListDocumentsRequest{Namespace: "ns1"})
	assertQuicRequestError(t, err)
}

// quicRAGChunkConfig helpers
func TestE2E_RAG_QuicRAGChunkConfig_Empty(t *testing.T) {
	if got := quicRAGChunkConfig(&transport.RAGIngestRequest{}); got != nil {
		t.Fatal("expected nil for empty chunk config")
	}
}

func TestE2E_RAG_QuicRAGChunkConfig_NonEmpty(t *testing.T) {
	got := quicRAGChunkConfig(&transport.RAGIngestRequest{ChunkSize: 512})
	if got == nil {
		t.Fatal("expected non-nil chunk config")
	}
}

// ---------------------------------------------------------------------------
// Rebalance — success, closed, rpc-error, unmarshal-error, helpers
// ---------------------------------------------------------------------------

func TestE2E_Rebalance_GetShardStats_Success(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("rebalance.GetShardStats", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.ShardStats{
			ShardId: 1, KeyCount: 100, ByteCount: 2048, LeaderId: 1, ReplicaCount: 3,
		})
	})
	stats, err := tr.GetShardStats(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetShardStats: %v", err)
	}
	if stats.KeyCount != 100 {
		t.Fatalf("unexpected KeyCount: %d", stats.KeyCount)
	}
}

func TestE2E_Rebalance_GetShardStats_Closed(t *testing.T) {
	_, err := newQuicClosed().GetShardStats(context.Background(), 1)
	assertQuicClosed(t, err)
}

func TestE2E_Rebalance_GetShardStats_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("rebalance.GetShardStats", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("shard not found")
	})
	_, err := tr.GetShardStats(context.Background(), 999)
	assertQuicRequestError(t, err)
}

func TestE2E_Rebalance_CreateRebalancePlan_Success(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("rebalance.CreatePlan", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.RebalancePlan{
			Moves: []*pb.RebalanceMove{
				{ShardId: 1, FromNode: "n1", ToNode: "n2", EstimatedBytes: 512},
			},
			EstimatedDurationNs: int64(time.Second),
			CurrentImbalance:    0.35,
		})
	})
	plan, err := tr.CreateRebalancePlan(context.Background(), &transport.RebalancePlanRequest{MaxMoves: 5})
	if err != nil {
		t.Fatalf("CreateRebalancePlan: %v", err)
	}
	if len(plan.Moves) == 0 {
		t.Fatal("expected moves")
	}
}

func TestE2E_Rebalance_CreateRebalancePlan_Closed(t *testing.T) {
	_, err := newQuicClosed().CreateRebalancePlan(context.Background(), &transport.RebalancePlanRequest{})
	assertQuicClosed(t, err)
}

func TestE2E_Rebalance_CreateRebalancePlan_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("rebalance.CreatePlan", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("cluster unhealthy")
	})
	_, err := tr.CreateRebalancePlan(context.Background(), &transport.RebalancePlanRequest{})
	assertQuicRequestError(t, err)
}

func TestE2E_Rebalance_ExecuteRebalance_Success(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("rebalance.Execute", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.RebalanceExecutionResponse{
			MovesCompleted: 2, MovesFailed: 0, DurationNs: 500_000_000, FinalImbalance: 0.05,
		})
	})
	result, err := tr.ExecuteRebalance(context.Background(), &transport.RebalancePlan{
		Moves: []transport.RebalanceMove{{ShardID: 1, FromNode: "n1", ToNode: "n2"}},
	})
	if err != nil {
		t.Fatalf("ExecuteRebalance: %v", err)
	}
	if result.MovesCompleted != 2 {
		t.Fatalf("unexpected MovesCompleted: %d", result.MovesCompleted)
	}
}

func TestE2E_Rebalance_ExecuteRebalance_NilPlan(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("rebalance.Execute", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.RebalanceExecutionResponse{MovesCompleted: 0})
	})
	// nil plan: quicRebalancePlanToProto handles nil → no panic
	_, err := tr.ExecuteRebalance(context.Background(), nil)
	_ = err // result doesn't matter, just no panic
}

func TestE2E_Rebalance_ExecuteRebalance_Closed(t *testing.T) {
	_, err := newQuicClosed().ExecuteRebalance(context.Background(), nil)
	assertQuicClosed(t, err)
}

func TestE2E_Rebalance_ExecuteRebalance_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("rebalance.Execute", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("rebalance failed")
	})
	_, err := tr.ExecuteRebalance(context.Background(), &transport.RebalancePlan{})
	assertQuicRequestError(t, err)
}

func TestE2E_Rebalance_GetImbalanceReport_Success(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("rebalance.GetImbalanceReport", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.ImbalanceReport{
			Nodes: []*pb.NodeLoad{
				{NodeId: "node-1", Address: "127.0.0.1:5001", ShardCount: 3, TotalKeys: 1000},
			},
			ImbalanceScore: 0.2,
		})
	})
	report, err := tr.GetImbalanceReport(context.Background())
	if err != nil {
		t.Fatalf("GetImbalanceReport: %v", err)
	}
	if len(report.Nodes) == 0 {
		t.Fatal("expected nodes")
	}
}

func TestE2E_Rebalance_GetImbalanceReport_Closed(t *testing.T) {
	_, err := newQuicClosed().GetImbalanceReport(context.Background())
	assertQuicClosed(t, err)
}

func TestE2E_Rebalance_GetImbalanceReport_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("rebalance.GetImbalanceReport", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("unavailable")
	})
	_, err := tr.GetImbalanceReport(context.Background())
	assertQuicRequestError(t, err)
}

// helper nil-guard tests
func TestE2E_Rebalance_QuicProtoToRebalancePlan_Nil(t *testing.T) {
	if got := quicProtoToRebalancePlan(nil); got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestE2E_Rebalance_QuicRebalancePlanToProto_Nil(t *testing.T) {
	got := quicRebalancePlanToProto(nil)
	if got == nil {
		t.Fatal("expected non-nil proto for nil plan")
	}
}

// ---------------------------------------------------------------------------
// GetUserByCertThumbprint / AddCertBinding
// ---------------------------------------------------------------------------

// mustMarshalJSON marshals v to JSON bytes, panicking on error.
func mustMarshalJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func TestE2E_Auth_GetUserByCertThumbprint_Success(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("user.GetByCertThumbprint", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshalJSON(map[string]any{
			"id": uint64(99), "username": "cert-user", "enabled": true,
		})
	})
	user, err := tr.GetUserByCertThumbprint(context.Background(), "abc:def:123")
	if err != nil {
		t.Fatalf("GetUserByCertThumbprint: %v", err)
	}
	if user == nil || user.ID != 99 {
		t.Fatalf("unexpected user: %+v", user)
	}
}

func TestE2E_Auth_GetUserByCertThumbprint_Closed(t *testing.T) {
	_, err := newQuicClosed().GetUserByCertThumbprint(context.Background(), "tp")
	assertQuicClosed(t, err)
}

func TestE2E_Auth_GetUserByCertThumbprint_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("user.GetByCertThumbprint", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("not found")
	})
	_, err := tr.GetUserByCertThumbprint(context.Background(), "tp")
	assertQuicRequestError(t, err)
}

func TestE2E_Auth_AddCertBinding_Success(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("user.AddCertBinding", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshalJSON(map[string]any{
			"id": uint64(42), "username": "cert-user", "enabled": true,
		})
	})
	user, err := tr.AddCertBinding(context.Background(), 42, "fp:12:34", time.Now().Add(365*24*time.Hour).Unix())
	if err != nil {
		t.Fatalf("AddCertBinding: %v", err)
	}
	if user == nil || user.ID != 42 {
		t.Fatalf("unexpected user: %+v", user)
	}
}

func TestE2E_Auth_AddCertBinding_Closed(t *testing.T) {
	_, err := newQuicClosed().AddCertBinding(context.Background(), 1, "fp", 0)
	assertQuicClosed(t, err)
}

func TestE2E_Auth_AddCertBinding_RPCError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("user.AddCertBinding", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("user not found")
	})
	_, err := tr.AddCertBinding(context.Background(), 999, "fp", 0)
	assertQuicRequestError(t, err)
}
