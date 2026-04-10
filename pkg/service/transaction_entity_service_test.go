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

func TestTransactionService_ExecuteEntity_Success(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			decoded, decErr := protocol.DecodeCommand(cmd)
			if decErr != nil {
				t.Fatalf("failed to decode command: %v", decErr)
			}
			if decoded.Op != protocol.OpTransaction {
				t.Fatalf("expected OpTransaction (%d), got %d", protocol.OpTransaction, decoded.Op)
			}
			txn, decErr := protocol.DecodeTransaction(decoded.Payload)
			if decErr != nil {
				t.Fatalf("failed to decode transaction: %v", decErr)
			}
			// Verify compare key is entity key.
			if len(txn.Compares) != 1 {
				t.Fatalf("expected 1 compare, got %d", len(txn.Compares))
			}
			if !bytes.Equal(txn.Compares[0].Key, []byte("user/u1")) {
				t.Fatalf("expected compare key 'user/u1', got %q", txn.Compares[0].Key)
			}
			// Verify success ops.
			if len(txn.SuccessOps) != 1 {
				t.Fatalf("expected 1 success op, got %d", len(txn.SuccessOps))
			}
			if !bytes.Equal(txn.SuccessOps[0].Key, []byte("user/u1")) {
				t.Fatalf("expected success op key 'user/u1', got %q", txn.SuccessOps[0].Key)
			}
			if txn.SuccessOps[0].Type != protocol.OpMVCCPut {
				t.Fatalf("expected success op type OpMVCCPut, got %d", txn.SuccessOps[0].Type)
			}
			// Verify failure ops.
			if len(txn.FailureOps) != 1 {
				t.Fatalf("expected 1 failure op, got %d", len(txn.FailureOps))
			}
			if !bytes.Equal(txn.FailureOps[0].Key, []byte("user/u1")) {
				t.Fatalf("expected failure op key 'user/u1', got %q", txn.FailureOps[0].Key)
			}
			if txn.FailureOps[0].Type != protocol.OpMVCCDelete {
				t.Fatalf("expected failure op type OpMVCCDelete, got %d", txn.FailureOps[0].Type)
			}
			return sm.Result{Value: 5, Data: []byte{1}}, nil
		},
	}

	svc := NewTransactionService(host, 1)
	resp, err := svc.ExecuteEntity(context.Background(), &TransactionEntityRequest{
		Compares: []EntityCompare{
			{EntityType: "user", ID: "u1", Target: CompareVersion, Result: CompareEqual, Value: []byte("1")},
		},
		Success: []TransactionEntityOp{
			{Type: "put", EntityType: "user", ID: "u1", Data: []byte(`{"name":"alice-v2"}`)},
		},
		Failure: []TransactionEntityOp{
			{Type: "delete", EntityType: "user", ID: "u1"},
		},
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !resp.Succeeded {
		t.Fatal("expected Succeeded=true")
	}
	if len(resp.Responses) != 1 {
		t.Fatalf("expected 1 response (success branch), got %d", len(resp.Responses))
	}
}

func TestTransactionService_ExecuteEntity_Failed(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			// Data[0]=0 means compares failed.
			return sm.Result{Value: 3, Data: []byte{0}}, nil
		},
	}

	svc := NewTransactionService(host, 1)
	resp, err := svc.ExecuteEntity(context.Background(), &TransactionEntityRequest{
		Compares: []EntityCompare{
			{EntityType: "user", ID: "u1", Target: CompareVersion, Result: CompareEqual},
		},
		Success: []TransactionEntityOp{
			{Type: "put", EntityType: "user", ID: "u1", Data: []byte("new")},
		},
		Failure: []TransactionEntityOp{
			{Type: "delete", EntityType: "user", ID: "u1"},
			{Type: "put", EntityType: "user", ID: "u2", Data: []byte("fallback")},
		},
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if resp.Succeeded {
		t.Fatal("expected Succeeded=false")
	}
	if len(resp.Responses) != 2 {
		t.Fatalf("expected 2 responses (failure branch), got %d", len(resp.Responses))
	}
}

func TestTransactionService_ExecuteEntity_EmptyCompares(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			decoded, decErr := protocol.DecodeCommand(cmd)
			if decErr != nil {
				t.Fatalf("failed to decode command: %v", decErr)
			}
			txn, decErr := protocol.DecodeTransaction(decoded.Payload)
			if decErr != nil {
				t.Fatalf("failed to decode transaction: %v", decErr)
			}
			if len(txn.Compares) != 0 {
				t.Fatalf("expected 0 compares, got %d", len(txn.Compares))
			}
			if len(txn.SuccessOps) != 1 {
				t.Fatalf("expected 1 success op, got %d", len(txn.SuccessOps))
			}
			return sm.Result{Value: 10, Data: []byte{1}}, nil
		},
	}

	svc := NewTransactionService(host, 1)
	resp, err := svc.ExecuteEntity(context.Background(), &TransactionEntityRequest{
		Success: []TransactionEntityOp{
			{Type: "put", EntityType: "config", ID: "c1", Data: []byte("val")},
		},
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !resp.Succeeded {
		t.Fatal("expected Succeeded=true with empty compares")
	}
	if len(resp.Responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(resp.Responses))
	}
}

func TestTransactionService_ExecuteEntity_EmptyOpsInBothBranches(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 7, Data: []byte{1}}, nil
		},
	}

	svc := NewTransactionService(host, 1)
	resp, err := svc.ExecuteEntity(context.Background(), &TransactionEntityRequest{
		Compares: []EntityCompare{
			{EntityType: "user", ID: "u1", Target: CompareVersion, Result: CompareEqual},
		},
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !resp.Succeeded {
		t.Fatal("expected Succeeded=true")
	}
	if len(resp.Responses) != 0 {
		t.Fatalf("expected 0 responses, got %d", len(resp.Responses))
	}
}

func TestTransactionService_ExecuteEntity_CustomShardID(t *testing.T) {
	var capturedShardID uint64
	host := &mockHost{
		syncProposeFn: func(_ context.Context, shardID uint64, _ []byte) (sm.Result, error) {
			capturedShardID = shardID
			return sm.Result{Value: 1, Data: []byte{1}}, nil
		},
	}

	svc := NewTransactionService(host, 1)
	_, err := svc.ExecuteEntity(context.Background(), &TransactionEntityRequest{
		Success: []TransactionEntityOp{
			{Type: "put", EntityType: "user", ID: "u1", Data: []byte("data")},
		},
		ShardID: 99,
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if capturedShardID != 99 {
		t.Fatalf("expected shardID 99, got %d", capturedShardID)
	}
}

// ---------------------------------------------------------------------------
// ExecuteEntity - error paths
// ---------------------------------------------------------------------------

func TestTransactionService_ExecuteEntity_NilRequest(t *testing.T) {
	host := &mockHost{}
	svc := NewTransactionService(host, 1)

	_, err := svc.ExecuteEntity(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil request, got nil")
	}

	var emptyTypeErr *EntityEmptyTypeError
	if !errors.As(err, &emptyTypeErr) {
		t.Fatalf("expected *EntityEmptyTypeError, got %T: %v", err, err)
	}
}

func TestTransactionService_ExecuteEntity_CompareEmptyEntityType(t *testing.T) {
	host := &mockHost{}
	svc := NewTransactionService(host, 1)

	_, err := svc.ExecuteEntity(context.Background(), &TransactionEntityRequest{
		Compares: []EntityCompare{
			{EntityType: "", ID: "u1", Target: CompareVersion, Result: CompareEqual},
		},
	})
	if err == nil {
		t.Fatal("expected error for empty compare entity type, got nil")
	}

	var emptyTypeErr *EntityEmptyTypeError
	if !errors.As(err, &emptyTypeErr) {
		t.Fatalf("expected *EntityEmptyTypeError, got %T: %v", err, err)
	}
}

func TestTransactionService_ExecuteEntity_CompareEmptyID(t *testing.T) {
	host := &mockHost{}
	svc := NewTransactionService(host, 1)

	_, err := svc.ExecuteEntity(context.Background(), &TransactionEntityRequest{
		Compares: []EntityCompare{
			{EntityType: "user", ID: "", Target: CompareVersion, Result: CompareEqual},
		},
	})
	if err == nil {
		t.Fatal("expected error for empty compare ID, got nil")
	}

	var emptyIDErr *EntityEmptyIDError
	if !errors.As(err, &emptyIDErr) {
		t.Fatalf("expected *EntityEmptyIDError, got %T: %v", err, err)
	}
	if emptyIDErr.EntityType != "user" {
		t.Fatalf("expected EntityType 'user', got %q", emptyIDErr.EntityType)
	}
}

func TestTransactionService_ExecuteEntity_SuccessOpEmptyType(t *testing.T) {
	host := &mockHost{}
	svc := NewTransactionService(host, 1)

	_, err := svc.ExecuteEntity(context.Background(), &TransactionEntityRequest{
		Success: []TransactionEntityOp{
			{Type: "put", EntityType: "", ID: "u1", Data: []byte("data")},
		},
	})
	if err == nil {
		t.Fatal("expected error for empty success op entity type, got nil")
	}

	var emptyTypeErr *EntityEmptyTypeError
	if !errors.As(err, &emptyTypeErr) {
		t.Fatalf("expected *EntityEmptyTypeError, got %T: %v", err, err)
	}
}

func TestTransactionService_ExecuteEntity_SuccessOpEmptyID(t *testing.T) {
	host := &mockHost{}
	svc := NewTransactionService(host, 1)

	_, err := svc.ExecuteEntity(context.Background(), &TransactionEntityRequest{
		Success: []TransactionEntityOp{
			{Type: "put", EntityType: "user", ID: "", Data: []byte("data")},
		},
	})
	if err == nil {
		t.Fatal("expected error for empty success op ID, got nil")
	}

	var emptyIDErr *EntityEmptyIDError
	if !errors.As(err, &emptyIDErr) {
		t.Fatalf("expected *EntityEmptyIDError, got %T: %v", err, err)
	}
}

func TestTransactionService_ExecuteEntity_FailureOpInvalidType(t *testing.T) {
	host := &mockHost{}
	svc := NewTransactionService(host, 1)

	_, err := svc.ExecuteEntity(context.Background(), &TransactionEntityRequest{
		Failure: []TransactionEntityOp{
			{Type: "merge", EntityType: "user", ID: "u1", Data: []byte("data")},
		},
	})
	if err == nil {
		t.Fatal("expected error for invalid failure op type, got nil")
	}

	var invalidOpErr *InvalidEntityOpTypeError
	if !errors.As(err, &invalidOpErr) {
		t.Fatalf("expected *InvalidEntityOpTypeError, got %T: %v", err, err)
	}
	if invalidOpErr.OpType != "merge" {
		t.Fatalf("expected OpType 'merge', got %q", invalidOpErr.OpType)
	}
}

func TestTransactionService_ExecuteEntity_ProposeError(t *testing.T) {
	sentinel := errors.New("disk full")
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, sentinel
		},
	}

	svc := NewTransactionService(host, 1)
	_, err := svc.ExecuteEntity(context.Background(), &TransactionEntityRequest{
		Success: []TransactionEntityOp{
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

func TestTransactionService_ExecuteEntity_NotLeaderError(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, &quicraft.NotLeaderError{
				ShardID:       42,
				LeaderID:      3,
				LeaderAddress: "10.0.0.3:9100",
			}
		},
	}

	svc := NewTransactionService(host, 42)
	_, err := svc.ExecuteEntity(context.Background(), &TransactionEntityRequest{
		Success: []TransactionEntityOp{
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
	if nle.ShardID != 42 {
		t.Fatalf("expected ShardID 42, got %d", nle.ShardID)
	}
	if nle.LeaderID != 3 {
		t.Fatalf("expected LeaderID 3, got %d", nle.LeaderID)
	}
}

// ---------------------------------------------------------------------------
// encodeEntityOps
// ---------------------------------------------------------------------------

func TestEncodeEntityOps_EncodesCorrectly(t *testing.T) {
	ops := []TransactionEntityOp{
		{Type: "put", EntityType: "user", ID: "u1", Data: []byte("val1")},
		{Type: "delete", EntityType: "config", ID: "c1"},
	}

	encoded, err := encodeEntityOps(ops)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(encoded) != 2 {
		t.Fatalf("expected 2 encoded ops, got %d", len(encoded))
	}

	// Decode first op and verify.
	op0, _, decErr := protocol.DecodeOp(encoded[0])
	if decErr != nil {
		t.Fatalf("failed to decode op[0]: %v", decErr)
	}
	if op0.Type != protocol.OpMVCCPut {
		t.Fatalf("expected op[0] type OpMVCCPut, got %d", op0.Type)
	}
	if !bytes.Equal(op0.Key, []byte("user/u1")) {
		t.Fatalf("expected op[0] key 'user/u1', got %q", op0.Key)
	}
	if !bytes.Equal(op0.Value, []byte("val1")) {
		t.Fatalf("expected op[0] value 'val1', got %q", op0.Value)
	}

	// Decode second op and verify.
	op1, _, decErr := protocol.DecodeOp(encoded[1])
	if decErr != nil {
		t.Fatalf("failed to decode op[1]: %v", decErr)
	}
	if op1.Type != protocol.OpMVCCDelete {
		t.Fatalf("expected op[1] type OpMVCCDelete, got %d", op1.Type)
	}
	if !bytes.Equal(op1.Key, []byte("config/c1")) {
		t.Fatalf("expected op[1] key 'config/c1', got %q", op1.Key)
	}
}

func TestEncodeEntityOps_EmptySlice(t *testing.T) {
	encoded, err := encodeEntityOps(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(encoded) != 0 {
		t.Fatalf("expected 0 encoded ops, got %d", len(encoded))
	}
}

func TestEncodeEntityOps_EmptyEntityType(t *testing.T) {
	_, err := encodeEntityOps([]TransactionEntityOp{
		{Type: "put", EntityType: "", ID: "u1", Data: []byte("data")},
	})
	if err == nil {
		t.Fatal("expected error for empty entity type, got nil")
	}

	var emptyTypeErr *EntityEmptyTypeError
	if !errors.As(err, &emptyTypeErr) {
		t.Fatalf("expected *EntityEmptyTypeError, got %T: %v", err, err)
	}
}

func TestEncodeEntityOps_EmptyID(t *testing.T) {
	_, err := encodeEntityOps([]TransactionEntityOp{
		{Type: "put", EntityType: "user", ID: "", Data: []byte("data")},
	})
	if err == nil {
		t.Fatal("expected error for empty ID, got nil")
	}

	var emptyIDErr *EntityEmptyIDError
	if !errors.As(err, &emptyIDErr) {
		t.Fatalf("expected *EntityEmptyIDError, got %T: %v", err, err)
	}
}

func TestEncodeEntityOps_InvalidOpType(t *testing.T) {
	_, err := encodeEntityOps([]TransactionEntityOp{
		{Type: "patch", EntityType: "user", ID: "u1", Data: []byte("data")},
	})
	if err == nil {
		t.Fatal("expected error for invalid op type, got nil")
	}

	var invalidOpErr *InvalidEntityOpTypeError
	if !errors.As(err, &invalidOpErr) {
		t.Fatalf("expected *InvalidEntityOpTypeError, got %T: %v", err, err)
	}
}
