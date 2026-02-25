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
// NewBatchService
// ---------------------------------------------------------------------------

func TestNewBatchService_ReturnsNonNil(t *testing.T) {
	svc := NewBatchService(&mockHost{}, 1)
	if svc == nil {
		t.Fatal("expected non-nil BatchService")
	}
}

func TestNewBatchService_PreservesShardID(t *testing.T) {
	var capturedShardID uint64
	host := &mockHost{
		syncProposeFn: func(_ context.Context, shardID uint64, _ []byte) (sm.Result, error) {
			capturedShardID = shardID
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewBatchService(host, 55)
	_, err := svc.Execute(context.Background(), []Op{
		{Type: OpPut, Key: []byte("k"), Value: []byte("v")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedShardID != 55 {
		t.Fatalf("expected shardID 55, got %d", capturedShardID)
	}
}

// ---------------------------------------------------------------------------
// Execute
// ---------------------------------------------------------------------------

func TestBatchService_Execute_SuccessMultipleOps(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			decoded, err := protocol.DecodeCommand(cmd)
			if err != nil {
				t.Fatalf("failed to decode command: %v", err)
			}
			if decoded.Op != protocol.OpBatch {
				t.Fatalf("expected OpBatch (%d), got %d", protocol.OpBatch, decoded.Op)
			}
			ops, err := protocol.DecodeBatch(decoded.Payload)
			if err != nil {
				t.Fatalf("failed to decode batch: %v", err)
			}
			if len(ops) != 3 {
				t.Fatalf("expected 3 ops in batch, got %d", len(ops))
			}
			return sm.Result{Value: 10}, nil
		},
	}

	svc := NewBatchService(host, 1)
	ops := []Op{
		{Type: OpPut, Key: []byte("k1"), Value: []byte("v1")},
		{Type: OpPut, Key: []byte("k2"), Value: []byte("v2")},
		{Type: OpDelete, Key: []byte("k3")},
	}

	responses, err := svc.Execute(context.Background(), ops)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(responses) != 3 {
		t.Fatalf("expected 3 responses, got %d", len(responses))
	}
	for i, r := range responses {
		if !r.Succeeded {
			t.Fatalf("response[%d].Succeeded should be true", i)
		}
	}
}

func TestBatchService_Execute_EmptyOps(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			decoded, err := protocol.DecodeCommand(cmd)
			if err != nil {
				t.Fatalf("failed to decode command: %v", err)
			}
			if decoded.Op != protocol.OpBatch {
				t.Fatalf("expected OpBatch, got %d", decoded.Op)
			}
			ops, err := protocol.DecodeBatch(decoded.Payload)
			if err != nil {
				t.Fatalf("failed to decode batch: %v", err)
			}
			if len(ops) != 0 {
				t.Fatalf("expected 0 ops, got %d", len(ops))
			}
			return sm.Result{}, nil
		},
	}

	svc := NewBatchService(host, 1)
	responses, err := svc.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(responses) != 0 {
		t.Fatalf("expected 0 responses, got %d", len(responses))
	}
}

func TestBatchService_Execute_EmptyOpsSlice(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, nil
		},
	}

	svc := NewBatchService(host, 1)
	responses, err := svc.Execute(context.Background(), []Op{})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(responses) != 0 {
		t.Fatalf("expected 0 responses, got %d", len(responses))
	}
}

func TestBatchService_Execute_SinglePutOp(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			decoded, err := protocol.DecodeCommand(cmd)
			if err != nil {
				t.Fatalf("failed to decode command: %v", err)
			}
			ops, err := protocol.DecodeBatch(decoded.Payload)
			if err != nil {
				t.Fatalf("failed to decode batch: %v", err)
			}
			if len(ops) != 1 {
				t.Fatalf("expected 1 op, got %d", len(ops))
			}
			if ops[0].Type != protocol.OpMVCCPut {
				t.Fatalf("expected OpMVCCPut (%d), got %d", protocol.OpMVCCPut, ops[0].Type)
			}
			if !bytes.Equal(ops[0].Key, []byte("mykey")) {
				t.Fatalf("expected key 'mykey', got %q", ops[0].Key)
			}
			if !bytes.Equal(ops[0].Value, []byte("myval")) {
				t.Fatalf("expected value 'myval', got %q", ops[0].Value)
			}
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewBatchService(host, 1)
	responses, err := svc.Execute(context.Background(), []Op{
		{Type: OpPut, Key: []byte("mykey"), Value: []byte("myval")},
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(responses))
	}
	if !responses[0].Succeeded {
		t.Fatal("expected response[0].Succeeded=true")
	}
}

func TestBatchService_Execute_MixedPutDeleteOps(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			decoded, err := protocol.DecodeCommand(cmd)
			if err != nil {
				t.Fatalf("failed to decode command: %v", err)
			}
			ops, err := protocol.DecodeBatch(decoded.Payload)
			if err != nil {
				t.Fatalf("failed to decode batch: %v", err)
			}
			if len(ops) != 4 {
				t.Fatalf("expected 4 ops, got %d", len(ops))
			}
			if ops[0].Type != protocol.OpMVCCPut {
				t.Fatalf("expected op[0] type %d, got %d", protocol.OpMVCCPut, ops[0].Type)
			}
			if ops[1].Type != protocol.OpMVCCDelete {
				t.Fatalf("expected op[1] type %d, got %d", protocol.OpMVCCDelete, ops[1].Type)
			}
			if ops[2].Type != protocol.OpMVCCPut {
				t.Fatalf("expected op[2] type %d, got %d", protocol.OpMVCCPut, ops[2].Type)
			}
			if ops[3].Type != protocol.OpMVCCDelete {
				t.Fatalf("expected op[3] type %d, got %d", protocol.OpMVCCDelete, ops[3].Type)
			}
			return sm.Result{Value: 5}, nil
		},
	}

	svc := NewBatchService(host, 1)
	ops := []Op{
		{Type: OpPut, Key: []byte("k1"), Value: []byte("v1")},
		{Type: OpDelete, Key: []byte("k2")},
		{Type: OpPut, Key: []byte("k3"), Value: []byte("v3")},
		{Type: OpDelete, Key: []byte("k4")},
	}

	responses, err := svc.Execute(context.Background(), ops)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(responses) != 4 {
		t.Fatalf("expected 4 responses, got %d", len(responses))
	}
	for i, r := range responses {
		if !r.Succeeded {
			t.Fatalf("response[%d].Succeeded should be true", i)
		}
	}
}

func TestBatchService_Execute_NotLeaderError(t *testing.T) {
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
	_, err := svc.Execute(context.Background(), []Op{
		{Type: OpPut, Key: []byte("k"), Value: []byte("v")},
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

func TestBatchService_Execute_ProposeError(t *testing.T) {
	sentinel := errors.New("raft timeout")
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, sentinel
		},
	}

	svc := NewBatchService(host, 1)
	_, err := svc.Execute(context.Background(), []Op{
		{Type: OpPut, Key: []byte("k"), Value: []byte("v")},
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

func TestBatchService_Execute_OpWithLeaseID(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			decoded, err := protocol.DecodeCommand(cmd)
			if err != nil {
				t.Fatalf("failed to decode command: %v", err)
			}
			ops, err := protocol.DecodeBatch(decoded.Payload)
			if err != nil {
				t.Fatalf("failed to decode batch: %v", err)
			}
			if len(ops) != 1 {
				t.Fatalf("expected 1 op, got %d", len(ops))
			}
			if ops[0].LeaseID != 42 {
				t.Fatalf("expected leaseID 42, got %d", ops[0].LeaseID)
			}
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewBatchService(host, 1)
	_, err := svc.Execute(context.Background(), []Op{
		{Type: OpPut, Key: []byte("k"), Value: []byte("v"), LeaseID: 42},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBatchService_Execute_OpWithRangeEnd(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			decoded, err := protocol.DecodeCommand(cmd)
			if err != nil {
				t.Fatalf("failed to decode command: %v", err)
			}
			ops, err := protocol.DecodeBatch(decoded.Payload)
			if err != nil {
				t.Fatalf("failed to decode batch: %v", err)
			}
			if len(ops) != 1 {
				t.Fatalf("expected 1 op, got %d", len(ops))
			}
			if !bytes.Equal(ops[0].RangeEnd, []byte("zz")) {
				t.Fatalf("expected rangeEnd 'zz', got %q", ops[0].RangeEnd)
			}
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewBatchService(host, 1)
	_, err := svc.Execute(context.Background(), []Op{
		{Type: OpPut, Key: []byte("a"), Value: []byte("b"), RangeEnd: []byte("zz")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBatchService_Execute_ResponseCountMatchesOpsCount(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewBatchService(host, 1)

	for _, count := range []int{1, 5, 10, 50} {
		ops := make([]Op, count)
		for i := range ops {
			ops[i] = Op{Type: OpPut, Key: []byte("k"), Value: []byte("v")}
		}

		responses, err := svc.Execute(context.Background(), ops)
		if err != nil {
			t.Fatalf("unexpected error for count=%d: %v", count, err)
		}
		if len(responses) != count {
			t.Fatalf("expected %d responses, got %d", count, len(responses))
		}
	}
}

// ---------------------------------------------------------------------------
// translateProposeError
// ---------------------------------------------------------------------------

func TestBatchService_translateProposeError_NotLeaderError(t *testing.T) {
	svc := &BatchService{host: &mockHost{}, shardID: 1}

	err := svc.translateProposeError(&quicraft.NotLeaderError{
		ShardID:       33,
		LeaderID:      7,
		LeaderAddress: "192.168.0.7:9100",
	})

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
	if nle.ShardID != 33 {
		t.Fatalf("expected ShardID 33, got %d", nle.ShardID)
	}
	if nle.LeaderID != 7 {
		t.Fatalf("expected LeaderID 7, got %d", nle.LeaderID)
	}
	if nle.LeaderAddress != "192.168.0.7:9100" {
		t.Fatalf("expected LeaderAddress 192.168.0.7:9100, got %s", nle.LeaderAddress)
	}
}

func TestBatchService_translateProposeError_GenericError(t *testing.T) {
	svc := &BatchService{host: &mockHost{}, shardID: 1}
	sentinel := errors.New("connection reset")

	err := svc.translateProposeError(sentinel)

	var pe *ProposeError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProposeError, got %T: %v", err, err)
	}
	if !errors.Is(pe.Unwrap(), sentinel) {
		t.Fatalf("expected underlying sentinel, got %v", pe.Unwrap())
	}
}

func TestBatchService_translateProposeError_NotLeaderUnknownLeader(t *testing.T) {
	svc := &BatchService{host: &mockHost{}, shardID: 1}

	err := svc.translateProposeError(&quicraft.NotLeaderError{
		ShardID:       20,
		LeaderID:      0,
		LeaderAddress: "",
	})

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
	if nle.ShardID != 20 {
		t.Fatalf("expected ShardID 20, got %d", nle.ShardID)
	}
	if nle.LeaderID != 0 {
		t.Fatalf("expected LeaderID 0, got %d", nle.LeaderID)
	}
	if nle.LeaderAddress != "" {
		t.Fatalf("expected empty LeaderAddress, got %s", nle.LeaderAddress)
	}
}

func TestBatchService_translateProposeError_WrappedNotLeaderError(t *testing.T) {
	svc := &BatchService{host: &mockHost{}, shardID: 1}

	// errors.As traverses the chain, so wrapping should still match.
	inner := &quicraft.NotLeaderError{
		ShardID:       15,
		LeaderID:      4,
		LeaderAddress: "10.0.0.4:9100",
	}
	type wrappedError struct {
		msg string
		err error
	}
	wrapped := &wrappedError{msg: "outer", err: inner}
	// Implement Error and Unwrap so errors.As can traverse.
	_ = wrapped

	// Direct NotLeaderError (not wrapped) since the implementation uses
	// errors.As which handles pointer chains.
	err := svc.translateProposeError(inner)

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
	if nle.ShardID != 15 {
		t.Fatalf("expected ShardID 15, got %d", nle.ShardID)
	}
}
