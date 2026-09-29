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
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// NewTransactionService
// ---------------------------------------------------------------------------

func TestNewTransactionService_ReturnsNonNil(t *testing.T) {
	svc := NewTransactionService(&mockHost{}, 1)
	if svc == nil {
		t.Fatal("expected non-nil TransactionService")
	}
}

func TestNewTransactionService_PreservesShardID(t *testing.T) {
	var capturedShardID uint64
	host := &mockHost{
		syncProposeFn: func(_ context.Context, shardID uint64, _ []byte) (sm.Result, error) {
			capturedShardID = shardID
			return sm.Result{Value: 1, Data: []byte{1}}, nil
		},
	}

	svc := NewTransactionService(host, 99)
	_, err := svc.Execute(context.Background(), nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedShardID != 99 {
		t.Fatalf("expected shardID 99, got %d", capturedShardID)
	}
}

// ---------------------------------------------------------------------------
// Execute
// ---------------------------------------------------------------------------

func TestTransactionService_Execute_SuccessAllComparesPass(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, shardID uint64, cmd []byte) (sm.Result, error) {
			// Verify the command encodes as a transaction.
			decoded, err := protocol.DecodeCommand(cmd)
			if err != nil {
				t.Fatalf("failed to decode command: %v", err)
			}
			if decoded.Op != protocol.OpTransaction {
				t.Fatalf("expected OpTransaction (%d), got %d", protocol.OpTransaction, decoded.Op)
			}
			return sm.Result{Value: 5, Data: []byte{1}}, nil
		},
	}

	svc := NewTransactionService(host, 42)

	compares := []Compare{
		{Key: []byte("key1"), Target: CompareVersion, Result: CompareEqual, Version: 1},
	}
	successOps := []Op{
		{Type: OpPut, Key: []byte("key1"), Value: []byte("val1")},
		{Type: OpPut, Key: []byte("key2"), Value: []byte("val2")},
	}
	failureOps := []Op{
		{Type: OpDelete, Key: []byte("key1")},
	}

	resp, err := svc.Execute(context.Background(), compares, successOps, failureOps)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !resp.Succeeded {
		t.Fatal("expected Succeeded=true")
	}
	if resp.Revision != 5 {
		t.Fatalf("expected Revision 5, got %d", resp.Revision)
	}
	if len(resp.Responses) != len(successOps) {
		t.Fatalf("expected %d responses, got %d", len(successOps), len(resp.Responses))
	}
	for i, r := range resp.Responses {
		if !r.Succeeded {
			t.Fatalf("response[%d].Succeeded should be true", i)
		}
	}
}

func TestTransactionService_Execute_CompareFails(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 3, Data: []byte{0}}, nil
		},
	}

	svc := NewTransactionService(host, 1)

	compares := []Compare{
		{Key: []byte("k"), Target: CompareVersion, Result: CompareEqual, Version: 5},
	}
	successOps := []Op{
		{Type: OpPut, Key: []byte("k"), Value: []byte("new")},
	}
	failureOps := []Op{
		{Type: OpDelete, Key: []byte("k")},
		{Type: OpPut, Key: []byte("k2"), Value: []byte("fallback")},
	}

	resp, err := svc.Execute(context.Background(), compares, successOps, failureOps)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if resp.Succeeded {
		t.Fatal("expected Succeeded=false")
	}
	if resp.Revision != 3 {
		t.Fatalf("expected Revision 3, got %d", resp.Revision)
	}
	if len(resp.Responses) != len(failureOps) {
		t.Fatalf("expected %d responses (failure branch), got %d", len(failureOps), len(resp.Responses))
	}
	for i, r := range resp.Responses {
		if !r.Succeeded {
			t.Fatalf("response[%d].Succeeded should be true", i)
		}
	}
}

func TestTransactionService_Execute_EmptyCompares(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			decoded, err := protocol.DecodeCommand(cmd)
			if err != nil {
				t.Fatalf("failed to decode command: %v", err)
			}
			if decoded.Op != protocol.OpTransaction {
				t.Fatalf("expected OpTransaction, got %d", decoded.Op)
			}
			// Decode the transaction payload to verify zero compares.
			txn, err := protocol.DecodeTransaction(decoded.Payload)
			if err != nil {
				t.Fatalf("failed to decode transaction: %v", err)
			}
			if len(txn.Compares) != 0 {
				t.Fatalf("expected 0 compares, got %d", len(txn.Compares))
			}
			return sm.Result{Value: 10, Data: []byte{1}}, nil
		},
	}

	svc := NewTransactionService(host, 1)
	successOps := []Op{
		{Type: OpPut, Key: []byte("a"), Value: []byte("b")},
	}

	resp, err := svc.Execute(context.Background(), nil, successOps, nil)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !resp.Succeeded {
		t.Fatal("expected Succeeded=true with empty compares")
	}
	if resp.Revision != 10 {
		t.Fatalf("expected Revision 10, got %d", resp.Revision)
	}
	if len(resp.Responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(resp.Responses))
	}
}

func TestTransactionService_Execute_EmptyOpsInBothBranches(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 7, Data: []byte{1}}, nil
		},
	}

	svc := NewTransactionService(host, 1)
	compares := []Compare{
		{Key: []byte("k"), Target: CompareVersion, Result: CompareEqual, Version: 1},
	}

	resp, err := svc.Execute(context.Background(), compares, nil, nil)
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

func TestTransactionService_Execute_EmptyOpsFailureBranch(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 4, Data: []byte{0}}, nil
		},
	}

	svc := NewTransactionService(host, 1)
	compares := []Compare{
		{Key: []byte("k"), Target: CompareVersion, Result: CompareEqual, Version: 1},
	}

	resp, err := svc.Execute(context.Background(), compares, []Op{{Type: OpPut, Key: []byte("a"), Value: []byte("b")}}, nil)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if resp.Succeeded {
		t.Fatal("expected Succeeded=false")
	}
	if len(resp.Responses) != 0 {
		t.Fatalf("expected 0 responses for empty failure branch, got %d", len(resp.Responses))
	}
}

func TestTransactionService_Execute_NotLeaderError(t *testing.T) {
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
	_, err := svc.Execute(context.Background(), nil, nil, nil)
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
	if nle.LeaderAddress != "10.0.0.3:9100" {
		t.Fatalf("expected LeaderAddress 10.0.0.3:9100, got %s", nle.LeaderAddress)
	}
}

func TestTransactionService_Execute_ProposeError(t *testing.T) {
	sentinel := errors.New("disk full")
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, sentinel
		},
	}

	svc := NewTransactionService(host, 1)
	_, err := svc.Execute(context.Background(), nil, nil, nil)
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

func TestTransactionService_Execute_MultipleComparesAndOps(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			decoded, err := protocol.DecodeCommand(cmd)
			if err != nil {
				t.Fatalf("failed to decode command: %v", err)
			}
			txn, err := protocol.DecodeTransaction(decoded.Payload)
			if err != nil {
				t.Fatalf("failed to decode transaction: %v", err)
			}
			if len(txn.Compares) != 3 {
				t.Fatalf("expected 3 compares, got %d", len(txn.Compares))
			}
			if len(txn.SuccessOps) != 4 {
				t.Fatalf("expected 4 success ops, got %d", len(txn.SuccessOps))
			}
			if len(txn.FailureOps) != 2 {
				t.Fatalf("expected 2 failure ops, got %d", len(txn.FailureOps))
			}
			return sm.Result{Value: 15, Data: []byte{1}}, nil
		},
	}

	svc := NewTransactionService(host, 5)

	compares := []Compare{
		{Key: []byte("k1"), Target: CompareVersion, Result: CompareEqual, Version: 1},
		{Key: []byte("k2"), Target: CompareCreateRevision, Result: CompareGreater, CreateRevision: 10},
		{Key: []byte("k3"), Target: CompareValue, Result: CompareNotEqual, Value: []byte("old")},
	}
	successOps := []Op{
		{Type: OpPut, Key: []byte("k1"), Value: []byte("v1")},
		{Type: OpPut, Key: []byte("k2"), Value: []byte("v2")},
		{Type: OpDelete, Key: []byte("k3")},
		{Type: OpPut, Key: []byte("k4"), Value: []byte("v4"), LeaseID: 100},
	}
	failureOps := []Op{
		{Type: OpDelete, Key: []byte("k1")},
		{Type: OpDelete, Key: []byte("k2")},
	}

	resp, err := svc.Execute(context.Background(), compares, successOps, failureOps)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !resp.Succeeded {
		t.Fatal("expected Succeeded=true")
	}
	if resp.Revision != 15 {
		t.Fatalf("expected Revision 15, got %d", resp.Revision)
	}
	if len(resp.Responses) != 4 {
		t.Fatalf("expected 4 responses (success branch), got %d", len(resp.Responses))
	}
}

func TestTransactionService_Execute_EmptyDataFieldReturnsFalse(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 2, Data: nil}, nil
		},
	}

	svc := NewTransactionService(host, 1)
	resp, err := svc.Execute(context.Background(), nil,
		[]Op{{Type: OpPut, Key: []byte("k"), Value: []byte("v")}},
		[]Op{{Type: OpDelete, Key: []byte("k")}},
	)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if resp.Succeeded {
		t.Fatal("expected Succeeded=false when Data is empty")
	}
	if resp.Revision != 2 {
		t.Fatalf("expected Revision 2, got %d", resp.Revision)
	}
	// Empty data means not succeeded, so failure ops responses.
	if len(resp.Responses) != 1 {
		t.Fatalf("expected 1 response (failure branch), got %d", len(resp.Responses))
	}
}

func TestTransactionService_Execute_DataFieldZeroByte(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 8, Data: []byte{0}}, nil
		},
	}

	svc := NewTransactionService(host, 1)
	resp, err := svc.Execute(context.Background(), nil, nil, nil)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if resp.Succeeded {
		t.Fatal("expected Succeeded=false when Data[0]==0")
	}
	if resp.Revision != 8 {
		t.Fatalf("expected Revision 8, got %d", resp.Revision)
	}
}

func TestTransactionService_Execute_EncodesComparesCorrectly(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			decoded, err := protocol.DecodeCommand(cmd)
			if err != nil {
				t.Fatalf("failed to decode command: %v", err)
			}
			txn, err := protocol.DecodeTransaction(decoded.Payload)
			if err != nil {
				t.Fatalf("failed to decode transaction: %v", err)
			}
			if len(txn.Compares) != 1 {
				t.Fatalf("expected 1 compare, got %d", len(txn.Compares))
			}
			c := txn.Compares[0]
			if !bytes.Equal(c.Key, []byte("testkey")) {
				t.Fatalf("expected key 'testkey', got %q", c.Key)
			}
			if c.Target != byte(CompareModRevision) {
				t.Fatalf("expected target %d, got %d", CompareModRevision, c.Target)
			}
			if c.Result != byte(CompareLess) {
				t.Fatalf("expected result %d, got %d", CompareLess, c.Result)
			}
			if !bytes.Equal(c.Value, []byte("cmpval")) {
				t.Fatalf("expected value 'cmpval', got %q", c.Value)
			}
			if c.Version != 7 {
				t.Fatalf("expected version 7, got %d", c.Version)
			}
			if c.CreateRevision != 100 {
				t.Fatalf("expected createRevision 100, got %d", c.CreateRevision)
			}
			if c.ModRevision != 200 {
				t.Fatalf("expected modRevision 200, got %d", c.ModRevision)
			}
			return sm.Result{Value: 1, Data: []byte{1}}, nil
		},
	}

	svc := NewTransactionService(host, 1)
	compares := []Compare{
		{
			Key:            []byte("testkey"),
			Target:         CompareModRevision,
			Result:         CompareLess,
			Value:          []byte("cmpval"),
			Version:        7,
			CreateRevision: 100,
			ModRevision:    200,
		},
	}
	_, err := svc.Execute(context.Background(), compares, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTransactionService_Execute_EncodesOpsCorrectly(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			decoded, err := protocol.DecodeCommand(cmd)
			if err != nil {
				t.Fatalf("failed to decode command: %v", err)
			}
			txn, err := protocol.DecodeTransaction(decoded.Payload)
			if err != nil {
				t.Fatalf("failed to decode transaction: %v", err)
			}
			// Verify success ops encoding.
			if len(txn.SuccessOps) != 2 {
				t.Fatalf("expected 2 success ops, got %d", len(txn.SuccessOps))
			}
			if txn.SuccessOps[0].Type != protocol.OpMVCCPut {
				t.Fatalf("expected first success op type %d, got %d", protocol.OpMVCCPut, txn.SuccessOps[0].Type)
			}
			if !bytes.Equal(txn.SuccessOps[0].Key, []byte("k1")) {
				t.Fatalf("expected first op key 'k1', got %q", txn.SuccessOps[0].Key)
			}
			if !bytes.Equal(txn.SuccessOps[0].Value, []byte("v1")) {
				t.Fatalf("expected first op value 'v1', got %q", txn.SuccessOps[0].Value)
			}
			if txn.SuccessOps[1].Type != protocol.OpMVCCDelete {
				t.Fatalf("expected second success op type %d, got %d", protocol.OpMVCCDelete, txn.SuccessOps[1].Type)
			}
			if !bytes.Equal(txn.SuccessOps[1].Key, []byte("k2")) {
				t.Fatalf("expected second op key 'k2', got %q", txn.SuccessOps[1].Key)
			}
			// Verify failure ops encoding.
			if len(txn.FailureOps) != 1 {
				t.Fatalf("expected 1 failure op, got %d", len(txn.FailureOps))
			}
			if txn.FailureOps[0].Type != protocol.OpMVCCPut {
				t.Fatalf("expected failure op type %d, got %d", protocol.OpMVCCPut, txn.FailureOps[0].Type)
			}
			return sm.Result{Value: 1, Data: []byte{1}}, nil
		},
	}

	svc := NewTransactionService(host, 1)
	successOps := []Op{
		{Type: OpPut, Key: []byte("k1"), Value: []byte("v1")},
		{Type: OpDelete, Key: []byte("k2")},
	}
	failureOps := []Op{
		{Type: OpPut, Key: []byte("fallback"), Value: []byte("val")},
	}
	_, err := svc.Execute(context.Background(), nil, successOps, failureOps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTransactionService_Execute_LargeRevisionValue(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 1<<63 - 1, Data: []byte{1}}, nil
		},
	}

	svc := NewTransactionService(host, 1)
	resp, err := svc.Execute(context.Background(), nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := int64(1<<63 - 1)
	if resp.Revision != expected {
		t.Fatalf("expected Revision %d, got %d", expected, resp.Revision)
	}
}

func TestTransactionService_Execute_OpWithLeaseAndRangeEnd(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			decoded, err := protocol.DecodeCommand(cmd)
			if err != nil {
				t.Fatalf("failed to decode command: %v", err)
			}
			txn, err := protocol.DecodeTransaction(decoded.Payload)
			if err != nil {
				t.Fatalf("failed to decode transaction: %v", err)
			}
			if len(txn.SuccessOps) != 1 {
				t.Fatalf("expected 1 success op, got %d", len(txn.SuccessOps))
			}
			op := txn.SuccessOps[0]
			if op.LeaseID != 42 {
				t.Fatalf("expected leaseID 42, got %d", op.LeaseID)
			}
			if !bytes.Equal(op.RangeEnd, []byte("zz")) {
				t.Fatalf("expected rangeEnd 'zz', got %q", op.RangeEnd)
			}
			return sm.Result{Value: 1, Data: []byte{1}}, nil
		},
	}

	svc := NewTransactionService(host, 1)
	ops := []Op{
		{Type: OpPut, Key: []byte("a"), Value: []byte("b"), RangeEnd: []byte("zz"), LeaseID: 42},
	}
	_, err := svc.Execute(context.Background(), nil, ops, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Execute / ExecuteWithMode: fail-closed rejection of read ops in branches
// ---------------------------------------------------------------------------

// TestTransactionService_Execute_RejectsOpGetInSuccessOps verifies that a Get
// op in the success branch is rejected with a typed error and never reaches
// Raft (it must not be silently coerced into an empty-value Put).
func TestTransactionService_Execute_RejectsOpGetInSuccessOps(t *testing.T) {
	proposed := false
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			proposed = true
			return sm.Result{Value: 1, Data: []byte{1}}, nil
		},
	}

	svc := NewTransactionService(host, 1)
	successOps := []Op{
		{Type: OpGet, Key: []byte("k")},
	}

	resp, err := svc.Execute(context.Background(), nil, successOps, nil)
	if resp != nil {
		t.Fatalf("expected nil response on rejection, got %+v", resp)
	}
	var utoe *UnsupportedTransactionOpError
	if !errors.As(err, &utoe) {
		t.Fatalf("expected *UnsupportedTransactionOpError, got %T: %v", err, err)
	}
	if utoe.OpType != OpGet {
		t.Fatalf("expected OpType OpGet in error, got %d", utoe.OpType)
	}
	if proposed {
		t.Fatal("SyncPropose must not be called when a branch op is rejected")
	}
}

// TestTransactionService_Execute_RejectsOpRangeInFailureOps verifies that an
// OpRange in the failure branch is rejected the same way as OpGet, and that
// the key targeted by an earlier, valid success op is not touched by the
// rejected proposal (nothing is ever proposed).
func TestTransactionService_Execute_RejectsOpRangeInFailureOps(t *testing.T) {
	proposed := false
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			proposed = true
			return sm.Result{Value: 1, Data: []byte{0}}, nil
		},
	}

	svc := NewTransactionService(host, 1)
	failureOps := []Op{
		{Type: OpRange, Key: []byte("a"), RangeEnd: []byte("z")},
	}

	_, err := svc.Execute(context.Background(), nil, nil, failureOps)
	var utoe *UnsupportedTransactionOpError
	if !errors.As(err, &utoe) {
		t.Fatalf("expected *UnsupportedTransactionOpError, got %T: %v", err, err)
	}
	if utoe.OpType != OpRange {
		t.Fatalf("expected OpType OpRange in error, got %d", utoe.OpType)
	}
	if proposed {
		t.Fatal("SyncPropose must not be called when a branch op is rejected")
	}
}

// TestTransactionService_ExecuteWithMode_RejectsOpGet mirrors the Execute
// rejection test for the ExecuteWithMode path.
func TestTransactionService_ExecuteWithMode_RejectsOpGet(t *testing.T) {
	proposed := false
	host := &mockHost{
		proposeWithModeFn: func(_ context.Context, _ uint64, _ []byte, _ writemode.Mode) (sm.Result, error) {
			proposed = true
			return sm.Result{Value: 1, Data: []byte{1}}, nil
		},
	}

	svc := NewTransactionService(host, 1)
	successOps := []Op{
		{Type: OpGet, Key: []byte("k")},
	}

	_, err := svc.ExecuteWithMode(context.Background(), nil, successOps, nil, writemode.WALDurable)
	var utoe *UnsupportedTransactionOpError
	if !errors.As(err, &utoe) {
		t.Fatalf("expected *UnsupportedTransactionOpError, got %T: %v", err, err)
	}
	if proposed {
		t.Fatal("ProposeWithMode must not be called when a branch op is rejected")
	}
}

// TestTransactionService_Execute_RejectsUnknownOpType verifies that an
// OpType with no meaning at all (not one of the four defined constants)
// also fails closed rather than silently becoming a write.
func TestTransactionService_Execute_RejectsUnknownOpType(t *testing.T) {
	proposed := false
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			proposed = true
			return sm.Result{Value: 1, Data: []byte{1}}, nil
		},
	}

	svc := NewTransactionService(host, 1)
	successOps := []Op{
		{Type: OpType(200), Key: []byte("k"), Value: []byte("v")},
	}

	_, err := svc.Execute(context.Background(), nil, successOps, nil)
	var utoe *UnsupportedTransactionOpError
	if !errors.As(err, &utoe) {
		t.Fatalf("expected *UnsupportedTransactionOpError, got %T: %v", err, err)
	}
	if proposed {
		t.Fatal("SyncPropose must not be called when a branch op is rejected")
	}
}

// ---------------------------------------------------------------------------
// translateProposeError
// ---------------------------------------------------------------------------

func TestTransactionService_translateProposeError_NotLeaderError(t *testing.T) {

	err := translateProposeError(&quicraft.NotLeaderError{
		ShardID:       77,
		LeaderID:      5,
		LeaderAddress: "192.168.1.5:9100",
	})

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
	if nle.ShardID != 77 {
		t.Fatalf("expected ShardID 77, got %d", nle.ShardID)
	}
	if nle.LeaderID != 5 {
		t.Fatalf("expected LeaderID 5, got %d", nle.LeaderID)
	}
	if nle.LeaderAddress != "192.168.1.5:9100" {
		t.Fatalf("expected LeaderAddress 192.168.1.5:9100, got %s", nle.LeaderAddress)
	}
}

func TestTransactionService_translateProposeError_GenericError(t *testing.T) {
	sentinel := errors.New("timeout")

	err := translateProposeError(sentinel)

	var pe *ProposeError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProposeError, got %T: %v", err, err)
	}
	if !errors.Is(pe.Unwrap(), sentinel) {
		t.Fatalf("expected underlying sentinel, got %v", pe.Unwrap())
	}
}

func TestTransactionService_translateProposeError_NotLeaderUnknownLeader(t *testing.T) {

	err := translateProposeError(&quicraft.NotLeaderError{
		ShardID:       10,
		LeaderID:      0,
		LeaderAddress: "",
	})

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
	if nle.LeaderID != 0 {
		t.Fatalf("expected LeaderID 0, got %d", nle.LeaderID)
	}
	if nle.LeaderAddress != "" {
		t.Fatalf("expected empty LeaderAddress, got %s", nle.LeaderAddress)
	}
}

// ---------------------------------------------------------------------------
// encodeServiceOp
// ---------------------------------------------------------------------------

func TestEncodeServiceOp_OpPut(t *testing.T) {
	op := Op{
		Type:  OpPut,
		Key:   []byte("mykey"),
		Value: []byte("myval"),
	}

	encoded, err := encodeServiceOp(op)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("mykey"), []byte("myval"), nil, 0))
	if !bytes.Equal(encoded, expected) {
		t.Fatalf("OpPut encoding mismatch:\n  got:  %v\n  want: %v", encoded, expected)
	}
}

func TestEncodeServiceOp_OpDelete(t *testing.T) {
	op := Op{
		Type: OpDelete,
		Key:  []byte("delkey"),
	}

	encoded, err := encodeServiceOp(op)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := mustEncode(protocol.EncodeOp(protocol.OpMVCCDelete, []byte("delkey"), nil, nil, 0))
	if !bytes.Equal(encoded, expected) {
		t.Fatalf("OpDelete encoding mismatch:\n  got:  %v\n  want: %v", encoded, expected)
	}
}

func TestEncodeServiceOp_UnknownOpType(t *testing.T) {
	op := Op{
		Type:  OpType(255),
		Key:   []byte("k"),
		Value: []byte("v"),
	}

	encoded, err := encodeServiceOp(op)
	if encoded != nil {
		t.Fatalf("expected nil encoding on error, got %v", encoded)
	}
	var utoe *UnsupportedTransactionOpError
	if !errors.As(err, &utoe) {
		t.Fatalf("expected *UnsupportedTransactionOpError, got %T: %v", err, err)
	}
	if utoe.OpType != OpType(255) {
		t.Fatalf("expected OpType 255 in error, got %d", utoe.OpType)
	}
}

func TestEncodeServiceOp_OpGetIsRejected(t *testing.T) {
	// OpGet is a read: it must never be silently coerced into a write.
	op := Op{
		Type:  OpGet,
		Key:   []byte("getkey"),
		Value: []byte("getval"),
	}

	encoded, err := encodeServiceOp(op)
	if encoded != nil {
		t.Fatalf("expected nil encoding on error, got %v", encoded)
	}
	var utoe *UnsupportedTransactionOpError
	if !errors.As(err, &utoe) {
		t.Fatalf("expected *UnsupportedTransactionOpError, got %T: %v", err, err)
	}
	if utoe.OpType != OpGet {
		t.Fatalf("expected OpType OpGet in error, got %d", utoe.OpType)
	}
}

func TestEncodeServiceOp_OpRangeIsRejected(t *testing.T) {
	op := Op{
		Type:     OpRange,
		Key:      []byte("a"),
		RangeEnd: []byte("z"),
	}

	encoded, err := encodeServiceOp(op)
	if encoded != nil {
		t.Fatalf("expected nil encoding on error, got %v", encoded)
	}
	var utoe *UnsupportedTransactionOpError
	if !errors.As(err, &utoe) {
		t.Fatalf("expected *UnsupportedTransactionOpError, got %T: %v", err, err)
	}
	if utoe.OpType != OpRange {
		t.Fatalf("expected OpType OpRange in error, got %d", utoe.OpType)
	}
}

func TestEncodeServiceOp_WithLeaseAndRangeEnd(t *testing.T) {
	op := Op{
		Type:     OpPut,
		Key:      []byte("k"),
		Value:    []byte("v"),
		RangeEnd: []byte("z"),
		LeaseID:  999,
	}

	encoded, err := encodeServiceOp(op)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("k"), []byte("v"), []byte("z"), 999))
	if !bytes.Equal(encoded, expected) {
		t.Fatalf("OpPut with lease/rangeEnd encoding mismatch:\n  got:  %v\n  want: %v", encoded, expected)
	}
}

// TestEncodeServiceOp_EmptyKeyRefusedEmptyValueEncoded: an empty KEY is refused
// (no state machine can store one — go-qrdb#136 review), while an empty VALUE
// is an ordinary value and still encodes.
func TestEncodeServiceOp_EmptyKeyRefusedEmptyValueEncoded(t *testing.T) {
	_, err := encodeServiceOp(Op{Type: OpPut})
	var ik *InvalidKeyError
	if !errors.As(err, &ik) {
		t.Fatalf("empty key: err = %v; want InvalidKeyError", err)
	}

	encoded, err := encodeServiceOp(Op{Type: OpPut, Key: []byte("k")})
	if err != nil {
		t.Fatalf("empty value: unexpected error: %v", err)
	}
	expected := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("k"), nil, nil, 0))
	if !bytes.Equal(encoded, expected) {
		t.Fatalf("empty value encoding mismatch:\n  got:  %v\n  want: %v", encoded, expected)
	}
}

// ---------------------------------------------------------------------------
// opTypeMap
// ---------------------------------------------------------------------------

func TestOpTypeMap_ContainsExpectedEntries(t *testing.T) {
	if v, ok := opTypeMap[OpPut]; !ok || v != protocol.OpMVCCPut {
		t.Fatalf("expected OpPut -> OpMVCCPut (%d), got %d (ok=%v)", protocol.OpMVCCPut, v, ok)
	}
	if v, ok := opTypeMap[OpDelete]; !ok || v != protocol.OpMVCCDelete {
		t.Fatalf("expected OpDelete -> OpMVCCDelete (%d), got %d (ok=%v)", protocol.OpMVCCDelete, v, ok)
	}
}

func TestOpTypeMap_DoesNotContainUnmappedTypes(t *testing.T) {
	if _, ok := opTypeMap[OpGet]; ok {
		t.Fatal("OpGet should not be in opTypeMap")
	}
	if _, ok := opTypeMap[OpRange]; ok {
		t.Fatal("OpRange should not be in opTypeMap")
	}
}
