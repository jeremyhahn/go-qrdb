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
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"strings"
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

	err := translateProposeError(&quicraft.NotLeaderError{
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
	sentinel := errors.New("connection reset")

	err := translateProposeError(sentinel)

	var pe *ProposeError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProposeError, got %T: %v", err, err)
	}
	if !errors.Is(pe.Unwrap(), sentinel) {
		t.Fatalf("expected underlying sentinel, got %v", pe.Unwrap())
	}
}

func TestBatchService_translateProposeError_NotLeaderUnknownLeader(t *testing.T) {

	err := translateProposeError(&quicraft.NotLeaderError{
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
	err := translateProposeError(inner)

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
	if nle.ShardID != 15 {
		t.Fatalf("expected ShardID 15, got %d", nle.ShardID)
	}
}

// ---------------------------------------------------------------------------
// Import - Options Validation
// ---------------------------------------------------------------------------

func TestBatchService_Import_InvalidFormat(t *testing.T) {
	svc := NewBatchService(&mockHost{}, 1)

	_, err := svc.Import(context.Background(), strings.NewReader(""), ImportOptions{
		Format:    "xml",
		KeyField:  "id",
		ValueMode: "row",
	})

	var ife *ImportInvalidFormatError
	if !errors.As(err, &ife) {
		t.Fatalf("expected *ImportInvalidFormatError, got %T: %v", err, err)
	}
	if ife.Format != "xml" {
		t.Fatalf("expected format 'xml', got %q", ife.Format)
	}
}

func TestBatchService_Import_MissingKeyField(t *testing.T) {
	svc := NewBatchService(&mockHost{}, 1)

	_, err := svc.Import(context.Background(), strings.NewReader(""), ImportOptions{
		Format:    "csv",
		KeyField:  "",
		ValueMode: "row",
	})

	var mkf *ImportMissingKeyFieldError
	if !errors.As(err, &mkf) {
		t.Fatalf("expected *ImportMissingKeyFieldError, got %T: %v", err, err)
	}
}

func TestBatchService_Import_InvalidValueMode(t *testing.T) {
	svc := NewBatchService(&mockHost{}, 1)

	_, err := svc.Import(context.Background(), strings.NewReader(""), ImportOptions{
		Format:    "csv",
		KeyField:  "id",
		ValueMode: "invalid",
	})

	var ivm *ImportInvalidValueModeError
	if !errors.As(err, &ivm) {
		t.Fatalf("expected *ImportInvalidValueModeError, got %T: %v", err, err)
	}
}

func TestBatchService_Import_MissingValueFieldWhenFieldMode(t *testing.T) {
	svc := NewBatchService(&mockHost{}, 1)

	_, err := svc.Import(context.Background(), strings.NewReader(""), ImportOptions{
		Format:     "csv",
		KeyField:   "id",
		ValueMode:  "field",
		ValueField: "",
	})

	var mvf *ImportMissingValueFieldError
	if !errors.As(err, &mvf) {
		t.Fatalf("expected *ImportMissingValueFieldError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Import - CSV Parsing
// ---------------------------------------------------------------------------

func TestBatchService_Import_CSV_Success(t *testing.T) {
	var capturedCmd []byte
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			capturedCmd = cmd
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewBatchService(host, 1)

	csvData := "id,name,value\nkey1,Alice,100\nkey2,Bob,200\n"

	result, err := svc.Import(context.Background(), strings.NewReader(csvData), ImportOptions{
		Format:     "csv",
		KeyField:   "id",
		ValueMode:  "field",
		ValueField: "value",
		SkipHeader: true,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 2 {
		t.Fatalf("expected Total=2, got %d", result.Total)
	}
	if result.Succeeded != 2 {
		t.Fatalf("expected Succeeded=2, got %d", result.Succeeded)
	}
	if result.Failed != 0 {
		t.Fatalf("expected Failed=0, got %d", result.Failed)
	}

	// Verify batch was proposed.
	if capturedCmd == nil {
		t.Fatal("expected batch command to be proposed")
	}
	decoded, err := protocol.DecodeCommand(capturedCmd)
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
	if len(ops) != 2 {
		t.Fatalf("expected 2 ops, got %d", len(ops))
	}
}

func TestBatchService_Import_CSV_RowMode(t *testing.T) {
	var capturedOps []protocol.DecodedOp
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			decoded, _ := protocol.DecodeCommand(cmd)
			capturedOps, _ = protocol.DecodeBatch(decoded.Payload)
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewBatchService(host, 1)

	csvData := "id,name,value\nkey1,Alice,100\n"

	result, err := svc.Import(context.Background(), strings.NewReader(csvData), ImportOptions{
		Format:     "csv",
		KeyField:   "id",
		ValueMode:  "row",
		SkipHeader: true,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("expected Total=1, got %d", result.Total)
	}

	// Verify value is JSON-serialized row.
	if len(capturedOps) != 1 {
		t.Fatalf("expected 1 op, got %d", len(capturedOps))
	}
	if !bytes.Equal(capturedOps[0].Key, []byte("key1")) {
		t.Fatalf("expected key 'key1', got %q", capturedOps[0].Key)
	}
	// Value should be JSON containing all fields.
	if !bytes.Contains(capturedOps[0].Value, []byte("Alice")) {
		t.Fatalf("expected value to contain 'Alice', got %q", capturedOps[0].Value)
	}
}

func TestBatchService_Import_CSV_NoSkipHeader(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewBatchService(host, 1)

	// When skipHeader is false, the header row is treated as data.
	csvData := "id,name,value\nkey1,Alice,100\n"

	result, err := svc.Import(context.Background(), strings.NewReader(csvData), ImportOptions{
		Format:     "csv",
		KeyField:   "id",
		ValueMode:  "field",
		ValueField: "value",
		SkipHeader: false,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Both header and data row are imported.
	if result.Total != 2 {
		t.Fatalf("expected Total=2, got %d", result.Total)
	}
}

func TestBatchService_Import_CSV_EmptyFile(t *testing.T) {
	host := &mockHost{}

	svc := NewBatchService(host, 1)

	result, err := svc.Import(context.Background(), strings.NewReader(""), ImportOptions{
		Format:     "csv",
		KeyField:   "id",
		ValueMode:  "row",
		SkipHeader: true,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 0 {
		t.Fatalf("expected Total=0, got %d", result.Total)
	}
}

func TestBatchService_Import_CSV_MissingKeyField(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewBatchService(host, 1)

	csvData := "name,value\nAlice,100\n"

	result, err := svc.Import(context.Background(), strings.NewReader(csvData), ImportOptions{
		Format:     "csv",
		KeyField:   "id", // Not in CSV
		ValueMode:  "row",
		SkipHeader: true,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("expected Total=1, got %d", result.Total)
	}
	if result.Failed != 1 {
		t.Fatalf("expected Failed=1, got %d", result.Failed)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(result.Errors))
	}
	if !strings.Contains(result.Errors[0].Message, "missing key field") {
		t.Fatalf("expected error about missing key field, got %q", result.Errors[0].Message)
	}
}

// ---------------------------------------------------------------------------
// Import - JSON Parsing
// ---------------------------------------------------------------------------

func TestBatchService_Import_JSON_Array(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewBatchService(host, 1)

	jsonData := `[{"id":"key1","value":"v1"},{"id":"key2","value":"v2"}]`

	result, err := svc.Import(context.Background(), strings.NewReader(jsonData), ImportOptions{
		Format:     "json",
		KeyField:   "id",
		ValueMode:  "field",
		ValueField: "value",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 2 {
		t.Fatalf("expected Total=2, got %d", result.Total)
	}
	if result.Succeeded != 2 {
		t.Fatalf("expected Succeeded=2, got %d", result.Succeeded)
	}
}

func TestBatchService_Import_JSON_SingleObject(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewBatchService(host, 1)

	jsonData := `{"id":"key1","value":"v1"}`

	result, err := svc.Import(context.Background(), strings.NewReader(jsonData), ImportOptions{
		Format:     "json",
		KeyField:   "id",
		ValueMode:  "field",
		ValueField: "value",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("expected Total=1, got %d", result.Total)
	}
}

func TestBatchService_Import_JSON_NDJSON(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewBatchService(host, 1)

	ndjsonData := `{"id":"key1","value":"v1"}
{"id":"key2","value":"v2"}
{"id":"key3","value":"v3"}`

	result, err := svc.Import(context.Background(), strings.NewReader(ndjsonData), ImportOptions{
		Format:     "json",
		KeyField:   "id",
		ValueMode:  "field",
		ValueField: "value",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 3 {
		t.Fatalf("expected Total=3, got %d", result.Total)
	}
}

func TestBatchService_Import_JSON_Empty(t *testing.T) {
	svc := NewBatchService(&mockHost{}, 1)

	result, err := svc.Import(context.Background(), strings.NewReader(""), ImportOptions{
		Format:    "json",
		KeyField:  "id",
		ValueMode: "row",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 0 {
		t.Fatalf("expected Total=0, got %d", result.Total)
	}
}

func TestBatchService_Import_JSON_InvalidFormat(t *testing.T) {
	svc := NewBatchService(&mockHost{}, 1)

	_, err := svc.Import(context.Background(), strings.NewReader("not json"), ImportOptions{
		Format:    "json",
		KeyField:  "id",
		ValueMode: "row",
	})

	var ipe *ImportParseError
	if !errors.As(err, &ipe) {
		t.Fatalf("expected *ImportParseError, got %T: %v", err, err)
	}
}

func TestBatchService_Import_JSON_NonStringValue(t *testing.T) {
	var capturedOps []protocol.DecodedOp
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			decoded, _ := protocol.DecodeCommand(cmd)
			capturedOps, _ = protocol.DecodeBatch(decoded.Payload)
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewBatchService(host, 1)

	// JSON with non-string values (number, boolean, null).
	jsonData := `{"id":"key1","count":42,"active":true,"data":null}`

	result, err := svc.Import(context.Background(), strings.NewReader(jsonData), ImportOptions{
		Format:    "json",
		KeyField:  "id",
		ValueMode: "row",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("expected Total=1, got %d", result.Total)
	}

	// Verify the row was serialized as JSON.
	if len(capturedOps) != 1 {
		t.Fatalf("expected 1 op, got %d", len(capturedOps))
	}
	// Value should contain the serialized number.
	if !bytes.Contains(capturedOps[0].Value, []byte("42")) {
		t.Fatalf("expected value to contain '42', got %q", capturedOps[0].Value)
	}
}

// ---------------------------------------------------------------------------
// Import - Archive Parsing
// ---------------------------------------------------------------------------

func TestBatchService_Import_Archive_Zip(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewBatchService(host, 1)

	// Create a zip archive with CSV content.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	fw, _ := zw.Create("data.csv")
	_, _ = fw.Write([]byte("id,value\nkey1,v1\nkey2,v2\n"))
	_ = zw.Close()

	result, err := svc.Import(context.Background(), bytes.NewReader(buf.Bytes()), ImportOptions{
		Format:     "archive",
		KeyField:   "id",
		ValueMode:  "field",
		ValueField: "value",
		SkipHeader: true,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 2 {
		t.Fatalf("expected Total=2, got %d", result.Total)
	}
}

func TestBatchService_Import_Archive_TarGz(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewBatchService(host, 1)

	// Create a tar.gz archive with CSV content.
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	csvContent := []byte("id,value\nkey1,v1\n")
	hdr := &tar.Header{
		Name: "data.csv",
		Mode: 0600,
		Size: int64(len(csvContent)),
	}
	_ = tw.WriteHeader(hdr)
	_, _ = tw.Write(csvContent)
	_ = tw.Close()
	_ = gw.Close()

	result, err := svc.Import(context.Background(), bytes.NewReader(buf.Bytes()), ImportOptions{
		Format:     "archive",
		KeyField:   "id",
		ValueMode:  "field",
		ValueField: "value",
		SkipHeader: true,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("expected Total=1, got %d", result.Total)
	}
}

func TestBatchService_Import_Archive_ZipWithJSON(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewBatchService(host, 1)

	// Create a zip archive with JSON content.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	fw, _ := zw.Create("data.json")
	_, _ = fw.Write([]byte(`[{"id":"key1","value":"v1"}]`))
	_ = zw.Close()

	result, err := svc.Import(context.Background(), bytes.NewReader(buf.Bytes()), ImportOptions{
		Format:     "archive",
		KeyField:   "id",
		ValueMode:  "field",
		ValueField: "value",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("expected Total=1, got %d", result.Total)
	}
}

func TestBatchService_Import_Archive_InvalidFormat(t *testing.T) {
	svc := NewBatchService(&mockHost{}, 1)

	_, err := svc.Import(context.Background(), strings.NewReader("not an archive"), ImportOptions{
		Format:    "archive",
		KeyField:  "id",
		ValueMode: "row",
	})

	var iae *ImportArchiveError
	if !errors.As(err, &iae) {
		t.Fatalf("expected *ImportArchiveError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Import - Transactional Mode
// ---------------------------------------------------------------------------

func TestBatchService_Import_Transactional_Success(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewBatchService(host, 1)

	csvData := "id,value\nkey1,v1\nkey2,v2\n"

	result, err := svc.Import(context.Background(), strings.NewReader(csvData), ImportOptions{
		Format:        "csv",
		KeyField:      "id",
		ValueMode:     "field",
		ValueField:    "value",
		SkipHeader:    true,
		Transactional: true,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Succeeded != 2 {
		t.Fatalf("expected Succeeded=2, got %d", result.Succeeded)
	}
}

func TestBatchService_Import_Transactional_AbortOnMissingKey(t *testing.T) {
	svc := NewBatchService(&mockHost{}, 1)

	csvData := "name,value\nAlice,v1\n"

	_, err := svc.Import(context.Background(), strings.NewReader(csvData), ImportOptions{
		Format:        "csv",
		KeyField:      "id", // Missing from CSV.
		ValueMode:     "row",
		SkipHeader:    true,
		Transactional: true,
	})

	var ita *ImportTransactionAbortedError
	if !errors.As(err, &ita) {
		t.Fatalf("expected *ImportTransactionAbortedError, got %T: %v", err, err)
	}
	if !strings.Contains(ita.Message, "missing key field") {
		t.Fatalf("expected message about missing key field, got %q", ita.Message)
	}
}

func TestBatchService_Import_Transactional_AbortOnMissingValueField(t *testing.T) {
	svc := NewBatchService(&mockHost{}, 1)

	csvData := "id,name\nkey1,Alice\n"

	_, err := svc.Import(context.Background(), strings.NewReader(csvData), ImportOptions{
		Format:        "csv",
		KeyField:      "id",
		ValueMode:     "field",
		ValueField:    "value", // Missing from CSV.
		SkipHeader:    true,
		Transactional: true,
	})

	var ita *ImportTransactionAbortedError
	if !errors.As(err, &ita) {
		t.Fatalf("expected *ImportTransactionAbortedError, got %T: %v", err, err)
	}
	if !strings.Contains(ita.Message, "missing value field") {
		t.Fatalf("expected message about missing value field, got %q", ita.Message)
	}
}

func TestBatchService_Import_Transactional_ProposeError(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, errors.New("raft timeout")
		},
	}

	svc := NewBatchService(host, 1)

	csvData := "id,value\nkey1,v1\n"

	_, err := svc.Import(context.Background(), strings.NewReader(csvData), ImportOptions{
		Format:        "csv",
		KeyField:      "id",
		ValueMode:     "field",
		ValueField:    "value",
		SkipHeader:    true,
		Transactional: true,
	})

	var pe *ProposeError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProposeError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Import - Non-Transactional Error Handling
// ---------------------------------------------------------------------------

func TestBatchService_Import_NonTransactional_ContinuesOnMissingKey(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewBatchService(host, 1)

	// First row missing 'id', second row has it.
	csvData := "id,name,value\n,Alice,v1\nkey2,Bob,v2\n"

	result, err := svc.Import(context.Background(), strings.NewReader(csvData), ImportOptions{
		Format:        "csv",
		KeyField:      "id",
		ValueMode:     "field",
		ValueField:    "value",
		SkipHeader:    true,
		Transactional: false,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 2 {
		t.Fatalf("expected Total=2, got %d", result.Total)
	}
	if result.Succeeded != 1 {
		t.Fatalf("expected Succeeded=1, got %d", result.Succeeded)
	}
	if result.Failed != 1 {
		t.Fatalf("expected Failed=1, got %d", result.Failed)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(result.Errors))
	}
}

func TestBatchService_Import_NonTransactional_ProposeError(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, errors.New("raft timeout")
		},
	}

	svc := NewBatchService(host, 1)

	csvData := "id,value\nkey1,v1\nkey2,v2\n"

	result, err := svc.Import(context.Background(), strings.NewReader(csvData), ImportOptions{
		Format:        "csv",
		KeyField:      "id",
		ValueMode:     "field",
		ValueField:    "value",
		SkipHeader:    true,
		Transactional: false,
	})

	// Should not return error, but record failures.
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Succeeded != 0 {
		t.Fatalf("expected Succeeded=0, got %d", result.Succeeded)
	}
	if result.Failed != 2 {
		t.Fatalf("expected Failed=2, got %d", result.Failed)
	}
}

// ---------------------------------------------------------------------------
// Import - Edge Cases
// ---------------------------------------------------------------------------

func TestBatchService_Import_EmptyRecords(t *testing.T) {
	svc := NewBatchService(&mockHost{}, 1)

	// CSV with only header.
	csvData := "id,value\n"

	result, err := svc.Import(context.Background(), strings.NewReader(csvData), ImportOptions{
		Format:     "csv",
		KeyField:   "id",
		ValueMode:  "row",
		SkipHeader: true,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 0 {
		t.Fatalf("expected Total=0, got %d", result.Total)
	}
}

func TestBatchService_Import_LargeFile(t *testing.T) {
	var proposeCalls int
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			proposeCalls++
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewBatchService(host, 1)

	// Generate CSV with 100 rows.
	var sb strings.Builder
	sb.WriteString("id,value\n")
	for i := 0; i < 100; i++ {
		sb.WriteString("key")
		sb.WriteString(strings.Repeat("x", i))
		sb.WriteString(",value")
		sb.WriteString(strings.Repeat("y", i))
		sb.WriteString("\n")
	}

	result, err := svc.Import(context.Background(), strings.NewReader(sb.String()), ImportOptions{
		Format:     "csv",
		KeyField:   "id",
		ValueMode:  "field",
		ValueField: "value",
		SkipHeader: true,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 100 {
		t.Fatalf("expected Total=100, got %d", result.Total)
	}
	if result.Succeeded != 100 {
		t.Fatalf("expected Succeeded=100, got %d", result.Succeeded)
	}
	// Should be a single batch proposal.
	if proposeCalls != 1 {
		t.Fatalf("expected 1 propose call, got %d", proposeCalls)
	}
}

// ---------------------------------------------------------------------------
// Import - Format Auto-Detection
// ---------------------------------------------------------------------------

func TestBatchService_Import_Auto_DetectsJSONByFilename(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewBatchService(host, 1)

	jsonData := `[{"id":"k1","value":"v1"},{"id":"k2","value":"v2"}]`

	result, err := svc.Import(context.Background(), strings.NewReader(jsonData), ImportOptions{
		Format:     "auto",
		Filename:   "records.json",
		KeyField:   "id",
		ValueMode:  "field",
		ValueField: "value",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 2 {
		t.Fatalf("expected Total=2, got %d", result.Total)
	}
	if result.Succeeded != 2 {
		t.Fatalf("expected Succeeded=2, got %d", result.Succeeded)
	}
}

func TestBatchService_Import_Auto_DetectsCSVByFilename(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewBatchService(host, 1)

	csvData := "id,value\nk1,v1\nk2,v2\n"

	result, err := svc.Import(context.Background(), strings.NewReader(csvData), ImportOptions{
		Format:     "auto",
		Filename:   "data.csv",
		KeyField:   "id",
		ValueMode:  "field",
		ValueField: "value",
		SkipHeader: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 2 {
		t.Fatalf("expected Total=2, got %d", result.Total)
	}
	if result.Succeeded != 2 {
		t.Fatalf("expected Succeeded=2, got %d", result.Succeeded)
	}
}

func TestBatchService_Import_Auto_EmptyFormatDetectsJSONByContent(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewBatchService(host, 1)

	jsonData := `{"id":"k1","value":"hello"}`

	// Empty Format and Filename: must probe content.
	result, err := svc.Import(context.Background(), strings.NewReader(jsonData), ImportOptions{
		Format:     "",
		Filename:   "",
		KeyField:   "id",
		ValueMode:  "field",
		ValueField: "value",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("expected Total=1, got %d", result.Total)
	}
}

func TestBatchService_Import_Auto_UnknownFormatReturnsParseError(t *testing.T) {
	svc := NewBatchService(&mockHost{}, 1)

	// Binary data that cannot be detected.
	data := []byte{0xFF, 0xFE, 0xFD, 0xFC, 0xFB, 0xFA, 0xF9, 0xF8}

	_, err := svc.Import(context.Background(), bytes.NewReader(data), ImportOptions{
		Format:    "auto",
		Filename:  "unknown.bin",
		KeyField:  "id",
		ValueMode: "row",
	})
	if err == nil {
		t.Fatal("expected error for undetectable format, got nil")
	}
	var ipe *ImportParseError
	if !errors.As(err, &ipe) {
		t.Fatalf("expected *ImportParseError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Import - YAML Parsing
// ---------------------------------------------------------------------------

func TestBatchService_Import_YAML_Sequence(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewBatchService(host, 1)

	yamlData := "- id: k1\n  value: v1\n- id: k2\n  value: v2\n"

	result, err := svc.Import(context.Background(), strings.NewReader(yamlData), ImportOptions{
		Format:     "yaml",
		KeyField:   "id",
		ValueMode:  "field",
		ValueField: "value",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 2 {
		t.Fatalf("expected Total=2, got %d", result.Total)
	}
	if result.Succeeded != 2 {
		t.Fatalf("expected Succeeded=2, got %d", result.Succeeded)
	}
}

func TestBatchService_Import_YAML_InvalidData(t *testing.T) {
	svc := NewBatchService(&mockHost{}, 1)

	// YAML that cannot be unmarshalled into a map or sequence of maps
	// must return an ImportParseError.
	yamlData := ": invalid: yaml: ]["

	_, err := svc.Import(context.Background(), strings.NewReader(yamlData), ImportOptions{
		Format:    "yaml",
		KeyField:  "id",
		ValueMode: "row",
	})

	var ipe *ImportParseError
	if !errors.As(err, &ipe) {
		t.Fatalf("expected *ImportParseError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Import - NDJSON Parsing
// ---------------------------------------------------------------------------

func TestBatchService_Import_NDJSON_MultipleObjects(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewBatchService(host, 1)

	ndjsonData := `{"id":"k1","value":"v1"}
{"id":"k2","value":"v2"}
{"id":"k3","value":"v3"}`

	result, err := svc.Import(context.Background(), strings.NewReader(ndjsonData), ImportOptions{
		Format:     "ndjson",
		KeyField:   "id",
		ValueMode:  "field",
		ValueField: "value",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 3 {
		t.Fatalf("expected Total=3, got %d", result.Total)
	}
	if result.Succeeded != 3 {
		t.Fatalf("expected Succeeded=3, got %d", result.Succeeded)
	}
}

func TestBatchService_Import_NDJSON_InvalidJSON(t *testing.T) {
	svc := NewBatchService(&mockHost{}, 1)

	ndjsonData := `{"id":"k1"}
not-valid-json`

	_, err := svc.Import(context.Background(), strings.NewReader(ndjsonData), ImportOptions{
		Format:    "ndjson",
		KeyField:  "id",
		ValueMode: "row",
	})

	var ipe *ImportParseError
	if !errors.As(err, &ipe) {
		t.Fatalf("expected *ImportParseError, got %T: %v", err, err)
	}
}
