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
	"context"
	"errors"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
)

// ---------------------------------------------------------------------------
// NewIndexService
// ---------------------------------------------------------------------------

func TestIndexService_NewIndexService_ReturnsNonNil(t *testing.T) {
	svc := NewIndexService(&mockHost{}, 1)
	if svc == nil {
		t.Fatal("expected non-nil IndexService")
	}
}

func TestIndexService_NewIndexService_PreservesShardID(t *testing.T) {
	var capturedShardID uint64
	host := &mockHost{
		syncProposeFn: func(_ context.Context, shardID uint64, _ []byte) (sm.Result, error) {
			capturedShardID = shardID
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewIndexService(host, 42)
	err := svc.CreateIndex(context.Background(), "test_idx", "field1", IndexConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedShardID != 42 {
		t.Fatalf("expected shardID 42, got %d", capturedShardID)
	}
}

// ---------------------------------------------------------------------------
// CreateIndex
// ---------------------------------------------------------------------------

func TestIndexService_CreateIndex(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewIndexService(host, 1)
	err := svc.CreateIndex(context.Background(), "my_idx", "field1", IndexConfig{
		Unique: true,
		Sparse: false,
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestIndexService_CreateIndex_EncodesCommand(t *testing.T) {
	var capturedCmd []byte
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			capturedCmd = make([]byte, len(cmd))
			copy(capturedCmd, cmd)
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewIndexService(host, 1)
	err := svc.CreateIndex(context.Background(), "idx_enc", "field_enc", IndexConfig{
		Unique: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(capturedCmd) == 0 {
		t.Fatal("expected non-empty command to be proposed")
	}
	// First byte should be the OpIndexCreate op code (50).
	if capturedCmd[0] != 50 {
		t.Fatalf("expected op code 50, got %d", capturedCmd[0])
	}
}

// ---------------------------------------------------------------------------
// CreateIndex: NotLeader
// ---------------------------------------------------------------------------

func TestIndexService_CreateIndex_NotLeader(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, &quicraft.NotLeaderError{
				ShardID:       5,
				LeaderID:      3,
				LeaderAddress: "10.0.0.3:9100",
			}
		},
	}

	svc := NewIndexService(host, 5)
	err := svc.CreateIndex(context.Background(), "idx", "field", IndexConfig{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
	if nle.ShardID != 5 {
		t.Fatalf("expected ShardID 5, got %d", nle.ShardID)
	}
	if nle.LeaderID != 3 {
		t.Fatalf("expected LeaderID 3, got %d", nle.LeaderID)
	}
	if nle.LeaderAddress != "10.0.0.3:9100" {
		t.Fatalf("expected LeaderAddress 10.0.0.3:9100, got %s", nle.LeaderAddress)
	}
}

func TestIndexService_CreateIndex_ProposeError(t *testing.T) {
	sentinel := errors.New("raft timeout")
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, sentinel
		},
	}

	svc := NewIndexService(host, 1)
	err := svc.CreateIndex(context.Background(), "idx", "field", IndexConfig{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var pe *ProposeError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProposeError, got %T: %v", err, err)
	}
	if !errors.Is(pe.Unwrap(), sentinel) {
		t.Fatalf("expected underlying sentinel, got %v", pe.Unwrap())
	}
}

// ---------------------------------------------------------------------------
// DropIndex
// ---------------------------------------------------------------------------

func TestIndexService_DropIndex(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewIndexService(host, 1)
	err := svc.DropIndex(context.Background(), "my_idx")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestIndexService_DropIndex_EncodesCommand(t *testing.T) {
	var capturedCmd []byte
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			capturedCmd = make([]byte, len(cmd))
			copy(capturedCmd, cmd)
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewIndexService(host, 1)
	err := svc.DropIndex(context.Background(), "drop_idx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(capturedCmd) == 0 {
		t.Fatal("expected non-empty command")
	}
	// First byte should be OpIndexDrop (51).
	if capturedCmd[0] != 51 {
		t.Fatalf("expected op code 51, got %d", capturedCmd[0])
	}
}

// ---------------------------------------------------------------------------
// DropIndex: ProposeError
// ---------------------------------------------------------------------------

func TestIndexService_DropIndex_ProposeError(t *testing.T) {
	sentinel := errors.New("proposal dropped")
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, sentinel
		},
	}

	svc := NewIndexService(host, 1)
	err := svc.DropIndex(context.Background(), "idx")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var pe *ProposeError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProposeError, got %T: %v", err, err)
	}
	if !errors.Is(pe.Unwrap(), sentinel) {
		t.Fatalf("expected underlying sentinel, got %v", pe.Unwrap())
	}
}

func TestIndexService_DropIndex_NotLeader(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, &quicraft.NotLeaderError{
				ShardID:       7,
				LeaderID:      2,
				LeaderAddress: "10.0.0.2:9100",
			}
		},
	}

	svc := NewIndexService(host, 7)
	err := svc.DropIndex(context.Background(), "idx")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
	if nle.ShardID != 7 {
		t.Fatalf("expected ShardID 7, got %d", nle.ShardID)
	}
}

// ---------------------------------------------------------------------------
// Query
// ---------------------------------------------------------------------------

func TestIndexService_Query(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			q, ok := query.(IndexQueryRequest)
			if !ok {
				t.Fatalf("expected IndexQueryRequest, got %T", query)
			}
			if q.IndexName != "my_idx" {
				t.Fatalf("expected index name my_idx, got %s", q.IndexName)
			}
			if string(q.Value) != "alice" {
				t.Fatalf("expected value alice, got %s", string(q.Value))
			}
			return [][]byte{[]byte("100"), []byte("200"), []byte("300")}, nil
		},
	}

	svc := NewIndexService(host, 1)
	ids, err := svc.Query(context.Background(), "my_idx", []byte("alice"))
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("expected 3 IDs, got %d", len(ids))
	}
	if string(ids[0]) != "100" || string(ids[1]) != "200" || string(ids[2]) != "300" {
		t.Fatalf("unexpected keys: %v", ids)
	}
}

func TestIndexService_Query_EmptyResult(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return [][]byte{}, nil
		},
	}

	svc := NewIndexService(host, 1)
	ids, err := svc.Query(context.Background(), "idx", []byte("nobody"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("expected 0 IDs, got %d", len(ids))
	}
}

// ---------------------------------------------------------------------------
// Query: NotFound / ReadError
// ---------------------------------------------------------------------------

func TestIndexService_Query_NotFound(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, &quicraft.NotLeaderError{
				ShardID:       3,
				LeaderID:      5,
				LeaderAddress: "10.0.0.5:9100",
			}
		},
	}

	svc := NewIndexService(host, 3)
	_, err := svc.Query(context.Background(), "idx", []byte("val"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
	if nle.ShardID != 3 {
		t.Fatalf("expected ShardID 3, got %d", nle.ShardID)
	}
}

func TestIndexService_Query_ReadError(t *testing.T) {
	sentinel := errors.New("state machine error")
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, sentinel
		},
	}

	svc := NewIndexService(host, 1)
	_, err := svc.Query(context.Background(), "idx", []byte("val"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
	if !errors.Is(re.Unwrap(), sentinel) {
		t.Fatalf("expected underlying sentinel, got %v", re.Unwrap())
	}
}

// ---------------------------------------------------------------------------
// Scan
// ---------------------------------------------------------------------------

func TestIndexService_Scan(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			q, ok := query.(IndexScanRequest)
			if !ok {
				t.Fatalf("expected IndexScanRequest, got %T", query)
			}
			if q.IndexName != "scan_idx" {
				t.Fatalf("expected index name scan_idx, got %s", q.IndexName)
			}
			if string(q.Start) != "a" {
				t.Fatalf("expected start 'a', got %s", string(q.Start))
			}
			if string(q.End) != "z" {
				t.Fatalf("expected end 'z', got %s", string(q.End))
			}
			return map[string][][]byte{
				"apple":  {[]byte("1"), []byte("2")},
				"banana": {[]byte("3")},
			}, nil
		},
	}

	svc := NewIndexService(host, 1)
	result, err := svc.Scan(context.Background(), "scan_idx", []byte("a"), []byte("z"))
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(result))
	}
	if len(result["apple"]) != 2 {
		t.Fatalf("expected 2 IDs for apple, got %d", len(result["apple"]))
	}
	if len(result["banana"]) != 1 {
		t.Fatalf("expected 1 ID for banana, got %d", len(result["banana"]))
	}
}

func TestIndexService_Scan_EmptyResult(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return map[string][][]byte{}, nil
		},
	}

	svc := NewIndexService(host, 1)
	result, err := svc.Scan(context.Background(), "idx", []byte("a"), []byte("b"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(result))
	}
}

// ---------------------------------------------------------------------------
// Scan: ReadError
// ---------------------------------------------------------------------------

func TestIndexService_Scan_ReadError(t *testing.T) {
	sentinel := errors.New("scan failed")
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, sentinel
		},
	}

	svc := NewIndexService(host, 1)
	_, err := svc.Scan(context.Background(), "idx", []byte("a"), []byte("z"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
	if !errors.Is(re.Unwrap(), sentinel) {
		t.Fatalf("expected underlying sentinel, got %v", re.Unwrap())
	}
}

func TestIndexService_Scan_NotLeader(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, &quicraft.NotLeaderError{
				ShardID:       9,
				LeaderID:      4,
				LeaderAddress: "10.0.0.4:9100",
			}
		},
	}

	svc := NewIndexService(host, 9)
	_, err := svc.Scan(context.Background(), "idx", []byte("a"), []byte("z"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
	if nle.ShardID != 9 {
		t.Fatalf("expected ShardID 9, got %d", nle.ShardID)
	}
}

// ---------------------------------------------------------------------------
// translateProposeError
// ---------------------------------------------------------------------------

func TestIndexService_translateProposeError_NotLeader(t *testing.T) {

	err := translateProposeError(&quicraft.NotLeaderError{
		ShardID:       10,
		LeaderID:      2,
		LeaderAddress: "192.168.0.2:9100",
	})

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
	if nle.ShardID != 10 {
		t.Fatalf("expected ShardID 10, got %d", nle.ShardID)
	}
}

func TestIndexService_translateProposeError_GenericError(t *testing.T) {
	sentinel := errors.New("generic failure")

	err := translateProposeError(sentinel)

	var pe *ProposeError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProposeError, got %T: %v", err, err)
	}
	if !errors.Is(pe.Unwrap(), sentinel) {
		t.Fatalf("expected underlying sentinel, got %v", pe.Unwrap())
	}
}

// ---------------------------------------------------------------------------
// translateReadError
// ---------------------------------------------------------------------------

func TestIndexService_translateReadError_NotLeader(t *testing.T) {
	svc := &IndexService{host: &mockHost{}, shardID: 1}

	err := svc.translateReadError(&quicraft.NotLeaderError{
		ShardID:       12,
		LeaderID:      6,
		LeaderAddress: "192.168.0.6:9100",
	})

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
	if nle.ShardID != 12 {
		t.Fatalf("expected ShardID 12, got %d", nle.ShardID)
	}
}

func TestIndexService_translateReadError_GenericError(t *testing.T) {
	svc := &IndexService{host: &mockHost{}, shardID: 1}
	sentinel := errors.New("read failure")

	err := svc.translateReadError(sentinel)

	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
	if !errors.Is(re.Unwrap(), sentinel) {
		t.Fatalf("expected underlying sentinel, got %v", re.Unwrap())
	}
}

// ---------------------------------------------------------------------------
// Query: UnexpectedResultType
// ---------------------------------------------------------------------------

func TestIndexService_Query_UnexpectedResultType(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return "not-a-slice", nil
		},
	}

	svc := NewIndexService(host, 1)
	_, err := svc.Query(context.Background(), "idx", []byte("val"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var urte *unexpectedResultTypeError
	if !errors.As(err, &urte) {
		t.Fatalf("expected *unexpectedResultTypeError, got %T: %v", err, err)
	}
}

func TestIndexService_Scan_UnexpectedResultType(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return "not-a-map", nil
		},
	}

	svc := NewIndexService(host, 1)
	_, err := svc.Scan(context.Background(), "idx", []byte("a"), []byte("z"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var urte *unexpectedResultTypeError
	if !errors.As(err, &urte) {
		t.Fatalf("expected *unexpectedResultTypeError, got %T: %v", err, err)
	}
}
