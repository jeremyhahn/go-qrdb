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

package quic

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// MVCC
// ---------------------------------------------------------------------------

func TestE2E_PutWithLease_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("mvcc.PutWithLease", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.PutWithLeaseResponse{
			Kv: &pb.KeyValue{Key: []byte("k"), Value: []byte("v"), Version: 1, LeaseId: 99},
		})
	})
	kv, err := tr.PutWithLease(context.Background(), "k", []byte("v"), 99)
	if err != nil {
		t.Fatalf("PutWithLease: %v", err)
	}
	if kv.LeaseID != 99 {
		t.Fatalf("LeaseID = %d, want 99", kv.LeaseID)
	}
}

func TestE2E_RestoreToRevision_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("mvcc.RestoreToRevision", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.RestoreRevisionResponse{
			Revision: 5,
			Changes: []*pb.RestoreKeyChange{
				{Key: []byte("a"), Action: pb.RestoreAction_RESTORE_ACTION_REVERT, FromRevision: 9, ToRevision: 5, ValueBytes: 3},
			},
			Applied: 1,
		})
	})
	result, err := tr.RestoreToRevision(context.Background(), &transport.RestoreRevisionRequest{
		Revision: 5,
		Prefix:   []byte("prefix"),
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	if result.Revision != 5 || result.Applied != 1 {
		t.Fatalf("RestoreToRevision result = %+v", result)
	}
	if len(result.Changes) != 1 || string(result.Changes[0].Key) != "a" {
		t.Fatalf("unexpected changes: %+v", result.Changes)
	}
}

// TestE2E_RestoreToRevision_BinaryRoundTrip proves an arbitrary byte-valued
// prefix and key round-trip byte-for-byte through the QUIC wire protocol's
// proto `bytes` encoding, in both directions.
func TestE2E_RestoreToRevision_BinaryRoundTrip(t *testing.T) {
	binaryPrefix := []byte{0x00, 0xff, 0xfe, 'p'}
	binaryRespKey := []byte{0x00, 0xff, 0xfe, 'k'}

	srv, tr := startE2EServer(t)
	srv.SetResponder("mvcc.RestoreToRevision", func(payload []byte) (uint16, []byte) {
		var req pb.RestoreRevisionRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			t.Fatalf("server failed to unmarshal request: %v", err)
		}
		if string(req.GetPrefix()) != string(binaryPrefix) {
			t.Fatalf("server received prefix %v, want %v", req.GetPrefix(), binaryPrefix)
		}
		return wire.StatusOK, mustMarshal(&pb.RestoreRevisionResponse{
			Revision: 5,
			Changes: []*pb.RestoreKeyChange{
				{Key: binaryRespKey, Action: pb.RestoreAction_RESTORE_ACTION_DELETE},
			},
		})
	})

	result, err := tr.RestoreToRevision(context.Background(), &transport.RestoreRevisionRequest{
		Revision: 5,
		Prefix:   binaryPrefix,
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	if len(result.Changes) != 1 || string(result.Changes[0].Key) != string(binaryRespKey) {
		t.Fatalf("expected round-tripped binary response key %v, got %+v", binaryRespKey, result.Changes)
	}
}

// ---------------------------------------------------------------------------
// Batch
// ---------------------------------------------------------------------------

func TestE2E_ExecuteBatch_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("batch.Batch", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.BatchResponse{
			Responses: []*pb.OpResponse{
				{Succeeded: true, Count: 1},
			},
		})
	})
	ops := []transport.Op{
		{Type: transport.OpPut, Key: []byte("k"), Value: []byte("v")},
		{Type: transport.OpDelete, Key: []byte("k2")},
	}
	resp, err := tr.ExecuteBatch(context.Background(), ops)
	if err != nil {
		t.Fatalf("ExecuteBatch: %v", err)
	}
	if len(resp.Responses) != 1 {
		t.Fatalf("Responses = %+v", resp)
	}
}

func TestE2E_ImportFile_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)

	var staged []byte
	srv.SetResponder("batch.ImportBegin", func(req []byte) (uint16, []byte) {
		var pbReq pb.ImportBeginRequest
		if err := proto.Unmarshal(req, &pbReq); err != nil {
			t.Fatalf("unmarshal request: %v", err)
		}
		if pbReq.GetFormat() != "csv" || pbReq.GetKeyField() != "id" {
			t.Errorf("request = %+v", &pbReq)
		}
		return wire.StatusOK, mustMarshal(&pb.ImportBeginResponse{SessionId: "sess-1"})
	})
	srv.SetResponder("batch.ImportAppend", func(req []byte) (uint16, []byte) {
		var pbReq pb.ImportAppendRequest
		if err := proto.Unmarshal(req, &pbReq); err != nil {
			t.Fatalf("unmarshal request: %v", err)
		}
		if pbReq.GetSessionId() != "sess-1" {
			t.Errorf("session id = %q", pbReq.GetSessionId())
		}
		staged = append(staged, pbReq.GetChunk()...)
		return wire.StatusOK, mustMarshal(&pb.ImportAppendResponse{})
	})
	srv.SetResponder("batch.ImportCommit", func(req []byte) (uint16, []byte) {
		var pbReq pb.ImportCommitRequest
		if err := proto.Unmarshal(req, &pbReq); err != nil {
			t.Fatalf("unmarshal request: %v", err)
		}
		if pbReq.GetSessionId() != "sess-1" {
			t.Errorf("session id = %q", pbReq.GetSessionId())
		}
		if string(staged) != "id,val\n1,a\n" {
			t.Errorf("staged = %q", staged)
		}
		return wire.StatusOK, mustMarshal(&pb.ImportCommitResponse{
			Total:     1,
			Succeeded: 1,
			Errors: []*pb.BatchImportError{
				{Row: 2, Key: "1", Message: "duplicate"},
			},
		})
	})

	result, err := tr.ImportFile(context.Background(), "data.csv",
		strings.NewReader("id,val\n1,a\n"),
		transport.ImportOptions{Format: "csv", KeyField: "id", ValueMode: "row"})
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if result.Total != 1 || result.Succeeded != 1 {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Errors) != 1 || result.Errors[0].Message != "duplicate" {
		t.Fatalf("errors = %+v", result.Errors)
	}
}

func TestE2E_ImportFile_ServerError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("batch.ImportBegin", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.ImportBeginResponse{SessionId: "sess-1"})
	})
	srv.SetResponder("batch.ImportAppend", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("import failed")
	})

	_, err := tr.ImportFile(context.Background(), "data.csv", strings.NewReader("x"), transport.ImportOptions{Format: "csv"})
	if err == nil {
		t.Fatal("expected error from server")
	}
}

// ---------------------------------------------------------------------------
// Lease
// ---------------------------------------------------------------------------

func TestE2E_LeaseTimeToLive_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("lease.TimeToLive", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.LeaseTimeToLiveResponse{
			Lease: &pb.Lease{Id: 1, Ttl: 60, Keys: [][]byte{[]byte("k")}},
		})
	})
	info, err := tr.LeaseTimeToLive(context.Background(), 1)
	if err != nil {
		t.Fatalf("LeaseTimeToLive: %v", err)
	}
	if len(info.Keys) != 1 {
		t.Fatalf("Keys = %v", info.Keys)
	}
}
