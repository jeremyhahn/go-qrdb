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

package service

import (
	"bytes"
	"context"
	"errors"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// ---------------------------------------------------------------------------
// ExecuteEntity - success paths
// ---------------------------------------------------------------------------

func TestBatchService_ExecuteEntity_Success(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			decoded, decErr := protocol.DecodeCommand(cmd)
			if decErr != nil {
				t.Fatalf("failed to decode command: %v", decErr)
			}
			if decoded.Op != protocol.OpBatch {
				t.Fatalf("expected OpBatch (%d), got %d", protocol.OpBatch, decoded.Op)
			}
			ops, decErr := protocol.DecodeBatch(decoded.Payload)
			if decErr != nil {
				t.Fatalf("failed to decode batch: %v", decErr)
			}
			if len(ops) != 2 {
				t.Fatalf("expected 2 ops, got %d", len(ops))
			}
			// Verify first op is a put with entity key "user/u1".
			if ops[0].Type != protocol.OpMVCCPut {
				t.Fatalf("expected OpMVCCPut for op[0], got %d", ops[0].Type)
			}
			if !bytes.Equal(ops[0].Key, []byte("user/u1")) {
				t.Fatalf("expected key 'user/u1', got %q", ops[0].Key)
			}
			if !bytes.Equal(ops[0].Value, []byte(`{"name":"alice"}`)) {
				t.Fatalf("expected value '{\"name\":\"alice\"}', got %q", ops[0].Value)
			}
			// Verify second op is a put with entity key "user/u2".
			if !bytes.Equal(ops[1].Key, []byte("user/u2")) {
				t.Fatalf("expected key 'user/u2', got %q", ops[1].Key)
			}
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewBatchService(host, 1)
	resp, err := svc.ExecuteEntity(context.Background(), &BatchEntityRequest{
		Ops: []BatchEntityOp{
			{Type: "put", EntityType: "user", ID: "u1", Data: []byte(`{"name":"alice"}`)},
			{Type: "put", EntityType: "user", ID: "u2", Data: []byte(`{"name":"bob"}`)},
		},
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if len(resp.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(resp.Results))
	}
	for i, r := range resp.Results {
		if r.Index != i {
			t.Fatalf("expected result[%d].Index=%d, got %d", i, i, r.Index)
		}
		if r.Error != nil {
			t.Fatalf("expected nil error for result[%d], got %v", i, r.Error)
		}
	}
}

func TestBatchService_ExecuteEntity_MixedOps(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			decoded, decErr := protocol.DecodeCommand(cmd)
			if decErr != nil {
				t.Fatalf("failed to decode command: %v", decErr)
			}
			ops, decErr := protocol.DecodeBatch(decoded.Payload)
			if decErr != nil {
				t.Fatalf("failed to decode batch: %v", decErr)
			}
			if len(ops) != 3 {
				t.Fatalf("expected 3 ops, got %d", len(ops))
			}
			if ops[0].Type != protocol.OpMVCCPut {
				t.Fatalf("expected op[0] type OpMVCCPut, got %d", ops[0].Type)
			}
			if ops[1].Type != protocol.OpMVCCDelete {
				t.Fatalf("expected op[1] type OpMVCCDelete, got %d", ops[1].Type)
			}
			if ops[2].Type != protocol.OpMVCCPut {
				t.Fatalf("expected op[2] type OpMVCCPut, got %d", ops[2].Type)
			}
			// Verify entity key construction for delete.
			if !bytes.Equal(ops[1].Key, []byte("config/c1")) {
				t.Fatalf("expected delete key 'config/c1', got %q", ops[1].Key)
			}
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewBatchService(host, 1)
	resp, err := svc.ExecuteEntity(context.Background(), &BatchEntityRequest{
		Ops: []BatchEntityOp{
			{Type: "put", EntityType: "user", ID: "u1", Data: []byte("data1")},
			{Type: "delete", EntityType: "config", ID: "c1"},
			{Type: "put", EntityType: "user", ID: "u2", Data: []byte("data2")},
		},
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(resp.Results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(resp.Results))
	}
}

func TestBatchService_ExecuteEntity_CustomShardID(t *testing.T) {
	var capturedShardID uint64
	host := &mockHost{
		syncProposeFn: func(_ context.Context, shardID uint64, _ []byte) (sm.Result, error) {
			capturedShardID = shardID
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewBatchService(host, 1)
	_, err := svc.ExecuteEntity(context.Background(), &BatchEntityRequest{
		Ops: []BatchEntityOp{
			{Type: "put", EntityType: "user", ID: "u1", Data: []byte("data")},
		},
		ShardID: 42,
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if capturedShardID != 42 {
		t.Fatalf("expected shardID 42, got %d", capturedShardID)
	}
}

func TestBatchService_ExecuteEntity_DefaultShardID(t *testing.T) {
	var capturedShardID uint64
	host := &mockHost{
		syncProposeFn: func(_ context.Context, shardID uint64, _ []byte) (sm.Result, error) {
			capturedShardID = shardID
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewBatchService(host, 7)
	_, err := svc.ExecuteEntity(context.Background(), &BatchEntityRequest{
		Ops: []BatchEntityOp{
			{Type: "put", EntityType: "user", ID: "u1", Data: []byte("data")},
		},
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if capturedShardID != 7 {
		t.Fatalf("expected default shardID 7, got %d", capturedShardID)
	}
}

// ---------------------------------------------------------------------------
// ExecuteEntity - error paths
// ---------------------------------------------------------------------------

func TestBatchService_ExecuteEntity_EmptyOps(t *testing.T) {
	host := &mockHost{}
	svc := NewBatchService(host, 1)

	_, err := svc.ExecuteEntity(context.Background(), &BatchEntityRequest{
		Ops: nil,
	})
	if err == nil {
		t.Fatal("expected error for empty ops, got nil")
	}

	var emptyErr *BatchEntityEmptyOpsError
	if !errors.As(err, &emptyErr) {
		t.Fatalf("expected *BatchEntityEmptyOpsError, got %T: %v", err, err)
	}
}

func TestBatchService_ExecuteEntity_NilRequest(t *testing.T) {
	host := &mockHost{}
	svc := NewBatchService(host, 1)

	_, err := svc.ExecuteEntity(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil request, got nil")
	}

	var emptyErr *BatchEntityEmptyOpsError
	if !errors.As(err, &emptyErr) {
		t.Fatalf("expected *BatchEntityEmptyOpsError, got %T: %v", err, err)
	}
}

func TestBatchService_ExecuteEntity_EmptySliceOps(t *testing.T) {
	host := &mockHost{}
	svc := NewBatchService(host, 1)

	_, err := svc.ExecuteEntity(context.Background(), &BatchEntityRequest{
		Ops: []BatchEntityOp{},
	})
	if err == nil {
		t.Fatal("expected error for empty ops slice, got nil")
	}

	var emptyErr *BatchEntityEmptyOpsError
	if !errors.As(err, &emptyErr) {
		t.Fatalf("expected *BatchEntityEmptyOpsError, got %T: %v", err, err)
	}
}

func TestBatchService_ExecuteEntity_InvalidEntityType(t *testing.T) {
	host := &mockHost{}
	svc := NewBatchService(host, 1)

	_, err := svc.ExecuteEntity(context.Background(), &BatchEntityRequest{
		Ops: []BatchEntityOp{
			{Type: "put", EntityType: "", ID: "u1", Data: []byte("data")},
		},
	})
	if err == nil {
		t.Fatal("expected error for empty entity type, got nil")
	}

	var emptyTypeErr *EntityEmptyTypeError
	if !errors.As(err, &emptyTypeErr) {
		t.Fatalf("expected *EntityEmptyTypeError, got %T: %v", err, err)
	}
}

func TestBatchService_ExecuteEntity_EmptyID(t *testing.T) {
	host := &mockHost{}
	svc := NewBatchService(host, 1)

	_, err := svc.ExecuteEntity(context.Background(), &BatchEntityRequest{
		Ops: []BatchEntityOp{
			{Type: "put", EntityType: "user", ID: "", Data: []byte("data")},
		},
	})
	if err == nil {
		t.Fatal("expected error for empty ID, got nil")
	}

	var emptyIDErr *EntityEmptyIDError
	if !errors.As(err, &emptyIDErr) {
		t.Fatalf("expected *EntityEmptyIDError, got %T: %v", err, err)
	}
	if emptyIDErr.EntityType != "user" {
		t.Fatalf("expected EntityType 'user', got %q", emptyIDErr.EntityType)
	}
}

func TestBatchService_ExecuteEntity_InvalidOpType(t *testing.T) {
	host := &mockHost{}
	svc := NewBatchService(host, 1)

	_, err := svc.ExecuteEntity(context.Background(), &BatchEntityRequest{
		Ops: []BatchEntityOp{
			{Type: "update", EntityType: "user", ID: "u1", Data: []byte("data")},
		},
	})
	if err == nil {
		t.Fatal("expected error for invalid op type, got nil")
	}

	var invalidOpErr *InvalidEntityOpTypeError
	if !errors.As(err, &invalidOpErr) {
		t.Fatalf("expected *InvalidEntityOpTypeError, got %T: %v", err, err)
	}
	if invalidOpErr.OpType != "update" {
		t.Fatalf("expected OpType 'update', got %q", invalidOpErr.OpType)
	}
}

func TestBatchService_ExecuteEntity_ProposeError(t *testing.T) {
	sentinel := errors.New("raft timeout")
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, sentinel
		},
	}

	svc := NewBatchService(host, 1)
	_, err := svc.ExecuteEntity(context.Background(), &BatchEntityRequest{
		Ops: []BatchEntityOp{
			{Type: "put", EntityType: "user", ID: "u1", Data: []byte("data")},
		},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var pe *ProposeError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProposeError, got %T: %v", err, err)
	}
	if !errors.Is(pe.Unwrap(), sentinel) {
		t.Fatalf("expected underlying sentinel error, got %v", pe.Unwrap())
	}
}

func TestBatchService_ExecuteEntity_NotLeaderError(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, &quicraft.NotLeaderError{
				ShardID:       10,
				LeaderID:      2,
				LeaderAddress: "10.0.0.2:9100",
			}
		},
	}

	svc := NewBatchService(host, 10)
	_, err := svc.ExecuteEntity(context.Background(), &BatchEntityRequest{
		Ops: []BatchEntityOp{
			{Type: "put", EntityType: "user", ID: "u1", Data: []byte("data")},
		},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
	if nle.ShardID != 10 {
		t.Fatalf("expected ShardID 10, got %d", nle.ShardID)
	}
	if nle.LeaderID != 2 {
		t.Fatalf("expected LeaderID 2, got %d", nle.LeaderID)
	}
	if nle.LeaderAddress != "10.0.0.2:9100" {
		t.Fatalf("expected LeaderAddress 10.0.0.2:9100, got %s", nle.LeaderAddress)
	}
}

func TestBatchService_ExecuteEntity_SecondOpEmptyType(t *testing.T) {
	host := &mockHost{}
	svc := NewBatchService(host, 1)

	_, err := svc.ExecuteEntity(context.Background(), &BatchEntityRequest{
		Ops: []BatchEntityOp{
			{Type: "put", EntityType: "user", ID: "u1", Data: []byte("data")},
			{Type: "put", EntityType: "", ID: "u2", Data: []byte("data")},
		},
	})
	if err == nil {
		t.Fatal("expected error for empty entity type in second op, got nil")
	}

	var emptyTypeErr *EntityEmptyTypeError
	if !errors.As(err, &emptyTypeErr) {
		t.Fatalf("expected *EntityEmptyTypeError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// entityOpTypeHandlers map
// ---------------------------------------------------------------------------

func TestEntityOpTypeHandlers_ContainsExpectedEntries(t *testing.T) {
	if v, ok := entityOpTypeHandlers["put"]; !ok || v != protocol.OpMVCCPut {
		t.Fatalf("expected 'put' -> OpMVCCPut (%d), got %d (ok=%v)", protocol.OpMVCCPut, v, ok)
	}
	if v, ok := entityOpTypeHandlers["delete"]; !ok || v != protocol.OpMVCCDelete {
		t.Fatalf("expected 'delete' -> OpMVCCDelete (%d), got %d (ok=%v)", protocol.OpMVCCDelete, v, ok)
	}
}

func TestEntityOpTypeHandlers_DoesNotContainInvalid(t *testing.T) {
	for _, invalid := range []string{"update", "get", "range", "PUT", "DELETE", ""} {
		if _, ok := entityOpTypeHandlers[invalid]; ok {
			t.Fatalf("unexpected entry for %q in entityOpTypeHandlers", invalid)
		}
	}
}
