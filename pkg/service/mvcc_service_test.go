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
// Mock error types for cross-package error interface matching
// ---------------------------------------------------------------------------

// mockKeyNotFoundError satisfies the keyNotFoundErr interface.
type mockKeyNotFoundError struct {
	key string
}

func (e *mockKeyNotFoundError) Error() string       { return "key not found: " + e.key }
func (e *mockKeyNotFoundError) KeyNotFound() string { return e.key }

// mockRevisionCompactedError satisfies the revisionCompactedErr interface.
type mockRevisionCompactedError struct {
	requested, compacted int64
}

func (e *mockRevisionCompactedError) Error() string { return "revision compacted" }
func (e *mockRevisionCompactedError) RevisionCompacted() (int64, int64) {
	return e.requested, e.compacted
}

// mockRevisionNotFoundError satisfies the revisionNotFoundErr interface.
type mockRevisionNotFoundError struct {
	key      string
	revision int64
}

func (e *mockRevisionNotFoundError) Error() string { return "revision not found" }
func (e *mockRevisionNotFoundError) RevisionNotFound() (string, int64) {
	return e.key, e.revision
}

// ---------------------------------------------------------------------------
// NewMVCCKVService tests
// ---------------------------------------------------------------------------

func TestNewMVCCKVService_ReturnsNonNil(t *testing.T) {
	svc := NewMVCCKVService(&mockHost{}, 1)
	if svc == nil {
		t.Fatal("expected non-nil MVCCKVService")
	}
}

func TestNewMVCCKVService_ImplementsInterface(t *testing.T) {
	var _ MVCCKVServicer = NewMVCCKVService(&mockHost{}, 1) //nolint:staticcheck // QF1011: explicit interface assertion
}

// ---------------------------------------------------------------------------
// Put tests
// ---------------------------------------------------------------------------

func TestMVCCKVService_Put_Success(t *testing.T) {
	var capturedShardID uint64
	var capturedCmd []byte

	host := &mockHost{
		syncProposeFn: func(_ context.Context, shardID uint64, cmd []byte) (sm.Result, error) {
			capturedShardID = shardID
			capturedCmd = make([]byte, len(cmd))
			copy(capturedCmd, cmd)
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewMVCCKVService(host, 42)
	err := svc.Put(context.Background(), "hello", []byte("world"))
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	if capturedShardID != 42 {
		t.Fatalf("expected shardID 42, got %d", capturedShardID)
	}

	expected := protocol.EncodeMVCCPut([]byte("hello"), []byte("world"), 0)
	if !bytes.Equal(capturedCmd, expected) {
		t.Fatalf("command mismatch: expected %v, got %v", expected, capturedCmd)
	}
}

func TestMVCCKVService_Put_NotLeaderError(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, &quicraft.NotLeaderError{
				ShardID:       42,
				LeaderID:      3,
				LeaderAddress: "10.0.0.3:9100",
			}
		},
	}

	svc := NewMVCCKVService(host, 42)
	err := svc.Put(context.Background(), "key", []byte("val"))
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

func TestMVCCKVService_Put_GenericError(t *testing.T) {
	sentinel := errors.New("disk full")
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, sentinel
		},
	}

	svc := NewMVCCKVService(host, 1)
	err := svc.Put(context.Background(), "key", []byte("val"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var pe *ProposeError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProposeError, got %T: %v", err, err)
	}
	if !errors.Is(pe, sentinel) {
		t.Fatal("expected ProposeError to wrap sentinel error")
	}
}

// ---------------------------------------------------------------------------
// Get tests
// ---------------------------------------------------------------------------

func TestMVCCKVService_Get_Success(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, shardID uint64, query interface{}) (interface{}, error) {
			if shardID != 7 {
				t.Fatalf("expected shardID 7, got %d", shardID)
			}
			q, ok := query.(MVCCGetQuery)
			if !ok {
				t.Fatalf("expected MVCCGetQuery, got %T", query)
			}
			if string(q.Key) != "mykey" {
				t.Fatalf("expected key mykey, got %s", string(q.Key))
			}
			return &KeyValue{
				Key:            []byte("mykey"),
				Value:          []byte("myvalue"),
				CreateRevision: 1,
				ModRevision:    5,
				Version:        3,
			}, nil
		},
	}

	svc := NewMVCCKVService(host, 7)
	val, err := svc.Get(context.Background(), "mykey")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if string(val) != "myvalue" {
		t.Fatalf("expected myvalue, got %s", string(val))
	}
}

func TestMVCCKVService_Get_KeyNotFound(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, &mockKeyNotFoundError{key: "missing"}
		},
	}

	svc := NewMVCCKVService(host, 1)
	val, err := svc.Get(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if val != nil {
		t.Fatalf("expected nil val, got %v", val)
	}

	var knf *KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Fatalf("expected *KeyNotFoundError, got %T: %v", err, err)
	}
	if knf.Key != "missing" {
		t.Fatalf("expected key missing, got %s", knf.Key)
	}
}

func TestMVCCKVService_Get_GenericReadError(t *testing.T) {
	sentinel := errors.New("network timeout")
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, sentinel
		},
	}

	svc := NewMVCCKVService(host, 1)
	val, err := svc.Get(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if val != nil {
		t.Fatalf("expected nil val, got %v", val)
	}

	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
	if !errors.Is(re, sentinel) {
		t.Fatal("expected ReadError to wrap sentinel error")
	}
}

func TestMVCCKVService_Get_UnexpectedType(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return "string-not-keyvalue", nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	val, err := svc.Get(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if val != nil {
		t.Fatalf("expected nil val, got %v", val)
	}

	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
	var urt *unexpectedResultTypeError
	if !errors.As(re.Unwrap(), &urt) {
		t.Fatalf("expected *unexpectedResultTypeError wrapped, got %T: %v", re.Unwrap(), re.Unwrap())
	}
}

// ---------------------------------------------------------------------------
// Delete tests
// ---------------------------------------------------------------------------

func TestMVCCKVService_Delete_Success(t *testing.T) {
	var capturedCmd []byte

	host := &mockHost{
		syncProposeFn: func(_ context.Context, shardID uint64, cmd []byte) (sm.Result, error) {
			if shardID != 5 {
				t.Fatalf("expected shardID 5, got %d", shardID)
			}
			capturedCmd = make([]byte, len(cmd))
			copy(capturedCmd, cmd)
			return sm.Result{}, nil
		},
	}

	svc := NewMVCCKVService(host, 5)
	err := svc.Delete(context.Background(), "delkey")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	expected := protocol.EncodeMVCCDelete([]byte("delkey"))
	if !bytes.Equal(capturedCmd, expected) {
		t.Fatalf("command mismatch: expected %v, got %v", expected, capturedCmd)
	}
}

func TestMVCCKVService_Delete_NotLeaderError(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, &quicraft.NotLeaderError{
				ShardID:       10,
				LeaderID:      2,
				LeaderAddress: "10.0.0.2:9100",
			}
		},
	}

	svc := NewMVCCKVService(host, 10)
	err := svc.Delete(context.Background(), "key")
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
}

func TestMVCCKVService_Delete_GenericError(t *testing.T) {
	sentinel := errors.New("proposal timeout")
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, sentinel
		},
	}

	svc := NewMVCCKVService(host, 1)
	err := svc.Delete(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var pe *ProposeError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProposeError, got %T: %v", err, err)
	}
	if !errors.Is(pe, sentinel) {
		t.Fatal("expected ProposeError to wrap sentinel error")
	}
}

// ---------------------------------------------------------------------------
// List tests
// ---------------------------------------------------------------------------

func TestMVCCKVService_List_WithPrefix(t *testing.T) {
	var capturedQuery interface{}

	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			capturedQuery = query
			return &MVCCRangeResult{
				KeyValues: []*KeyValue{
					{Key: []byte("user:1")},
					{Key: []byte("user:2")},
					{Key: []byte("user:3")},
				},
				Revision: 10,
			}, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	keys, err := svc.List(context.Background(), "user:")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	if len(keys) != 3 {
		t.Fatalf("expected 3 keys, got %d", len(keys))
	}
	if keys[0] != "user:1" || keys[1] != "user:2" || keys[2] != "user:3" {
		t.Fatalf("unexpected keys: %v", keys)
	}

	rq, ok := capturedQuery.(MVCCRangeQuery)
	if !ok {
		t.Fatalf("expected MVCCRangeQuery, got %T", capturedQuery)
	}
	if string(rq.Start) != "user:" {
		t.Fatalf("expected start user:, got %s", string(rq.Start))
	}
	expectedEnd := prefixEnd([]byte("user:"))
	if !bytes.Equal(rq.End, expectedEnd) {
		t.Fatalf("expected end %v, got %v", expectedEnd, rq.End)
	}
}

func TestMVCCKVService_List_EmptyPrefix(t *testing.T) {
	var capturedQuery interface{}

	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			capturedQuery = query
			return &MVCCRangeResult{
				KeyValues: []*KeyValue{
					{Key: []byte("a")},
					{Key: []byte("z")},
				},
				Revision: 5,
			}, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	keys, err := svc.List(context.Background(), "")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}

	rq, ok := capturedQuery.(MVCCRangeQuery)
	if !ok {
		t.Fatalf("expected MVCCRangeQuery, got %T", capturedQuery)
	}
	if !bytes.Equal(rq.Start, []byte{0x00}) {
		t.Fatalf("expected start [0x00], got %v", rq.Start)
	}
	if !bytes.Equal(rq.End, []byte{0xFF}) {
		t.Fatalf("expected end [0xFF], got %v", rq.End)
	}
}

func TestMVCCKVService_List_ReadError(t *testing.T) {
	sentinel := errors.New("read failure")
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, sentinel
		},
	}

	svc := NewMVCCKVService(host, 1)
	keys, err := svc.List(context.Background(), "pfx")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if keys != nil {
		t.Fatalf("expected nil keys, got %v", keys)
	}

	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestMVCCKVService_List_UnexpectedType(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return "not-a-range-result", nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	keys, err := svc.List(context.Background(), "pfx")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if keys != nil {
		t.Fatalf("expected nil keys, got %v", keys)
	}

	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
	var urt *unexpectedResultTypeError
	if !errors.As(re.Unwrap(), &urt) {
		t.Fatalf("expected *unexpectedResultTypeError wrapped, got %T", re.Unwrap())
	}
}

func TestMVCCKVService_List_EmptyResult(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return &MVCCRangeResult{
				KeyValues: []*KeyValue{},
				Revision:  0,
			}, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	keys, err := svc.List(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("expected 0 keys, got %d", len(keys))
	}
}

// ---------------------------------------------------------------------------
// PutWithLease tests
// ---------------------------------------------------------------------------

func TestMVCCKVService_PutWithLease_Success(t *testing.T) {
	var capturedCmd []byte
	var readCallCount int

	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			capturedCmd = make([]byte, len(cmd))
			copy(capturedCmd, cmd)
			return sm.Result{Value: 42}, nil // revision 42
		},
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			readCallCount++
			q, ok := query.(MVCCGetAtRevisionQuery)
			if !ok {
				t.Fatalf("expected MVCCGetAtRevisionQuery, got %T", query)
			}
			if string(q.Key) != "leasekey" {
				t.Fatalf("expected key leasekey, got %s", string(q.Key))
			}
			if q.Revision != 42 {
				t.Fatalf("expected revision 42, got %d", q.Revision)
			}
			return &KeyValue{
				Key:            []byte("leasekey"),
				Value:          []byte("leaseval"),
				CreateRevision: 42,
				ModRevision:    42,
				Version:        1,
				LeaseID:        100,
			}, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	kv, err := svc.PutWithLease(context.Background(), "leasekey", []byte("leaseval"), 100)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if kv == nil {
		t.Fatal("expected non-nil KeyValue")
	}
	if string(kv.Key) != "leasekey" {
		t.Fatalf("expected key leasekey, got %s", string(kv.Key))
	}
	if kv.LeaseID != 100 {
		t.Fatalf("expected leaseID 100, got %d", kv.LeaseID)
	}
	if readCallCount != 1 {
		t.Fatalf("expected 1 read call, got %d", readCallCount)
	}

	// Verify protocol encoding includes leaseID.
	expected := protocol.EncodeMVCCPut([]byte("leasekey"), []byte("leaseval"), 100)
	if !bytes.Equal(capturedCmd, expected) {
		t.Fatalf("command mismatch: expected %v, got %v", expected, capturedCmd)
	}
}

func TestMVCCKVService_PutWithLease_ProposeError(t *testing.T) {
	sentinel := errors.New("raft closed")
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, sentinel
		},
	}

	svc := NewMVCCKVService(host, 1)
	kv, err := svc.PutWithLease(context.Background(), "key", []byte("val"), 1)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if kv != nil {
		t.Fatalf("expected nil kv, got %v", kv)
	}

	var pe *ProposeError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProposeError, got %T: %v", err, err)
	}
}

func TestMVCCKVService_PutWithLease_ReadError(t *testing.T) {
	sentinel := errors.New("read failed")
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 10}, nil
		},
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, sentinel
		},
	}

	svc := NewMVCCKVService(host, 1)
	kv, err := svc.PutWithLease(context.Background(), "key", []byte("val"), 1)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if kv != nil {
		t.Fatalf("expected nil kv, got %v", kv)
	}

	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestMVCCKVService_PutWithLease_UnexpectedType(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 10}, nil
		},
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return int64(99), nil // wrong type; should be *KeyValue
		},
	}

	svc := NewMVCCKVService(host, 1)
	kv, err := svc.PutWithLease(context.Background(), "key", []byte("val"), 1)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if kv != nil {
		t.Fatalf("expected nil kv, got %v", kv)
	}

	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
	var urt *unexpectedResultTypeError
	if !errors.As(re.Unwrap(), &urt) {
		t.Fatalf("expected *unexpectedResultTypeError wrapped, got %T", re.Unwrap())
	}
}

func TestMVCCKVService_PutWithLease_NotLeaderError(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, &quicraft.NotLeaderError{
				ShardID:       1,
				LeaderID:      5,
				LeaderAddress: "10.0.0.5:9100",
			}
		},
	}

	svc := NewMVCCKVService(host, 1)
	kv, err := svc.PutWithLease(context.Background(), "key", []byte("val"), 1)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if kv != nil {
		t.Fatalf("expected nil kv, got %v", kv)
	}

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
	if nle.LeaderID != 5 {
		t.Fatalf("expected LeaderID 5, got %d", nle.LeaderID)
	}
}

// ---------------------------------------------------------------------------
// GetRevision tests
// ---------------------------------------------------------------------------

func TestMVCCKVService_GetRevision_Success(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, shardID uint64, query interface{}) (interface{}, error) {
			if shardID != 3 {
				t.Fatalf("expected shardID 3, got %d", shardID)
			}
			q, ok := query.(MVCCGetQuery)
			if !ok {
				t.Fatalf("expected MVCCGetQuery, got %T", query)
			}
			if string(q.Key) != "revkey" {
				t.Fatalf("expected key revkey, got %s", string(q.Key))
			}
			return &KeyValue{
				Key:            []byte("revkey"),
				Value:          []byte("revval"),
				CreateRevision: 5,
				ModRevision:    12,
				Version:        7,
			}, nil
		},
	}

	svc := NewMVCCKVService(host, 3)
	kv, err := svc.GetRevision(context.Background(), "revkey")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if string(kv.Key) != "revkey" {
		t.Fatalf("expected key revkey, got %s", string(kv.Key))
	}
	if kv.ModRevision != 12 {
		t.Fatalf("expected ModRevision 12, got %d", kv.ModRevision)
	}
	if kv.Version != 7 {
		t.Fatalf("expected Version 7, got %d", kv.Version)
	}
}

func TestMVCCKVService_GetRevision_KeyNotFound(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, &mockKeyNotFoundError{key: "gone"}
		},
	}

	svc := NewMVCCKVService(host, 1)
	kv, err := svc.GetRevision(context.Background(), "gone")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if kv != nil {
		t.Fatalf("expected nil kv, got %v", kv)
	}

	var knf *KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Fatalf("expected *KeyNotFoundError, got %T: %v", err, err)
	}
	if knf.Key != "gone" {
		t.Fatalf("expected key gone, got %s", knf.Key)
	}
}

func TestMVCCKVService_GetRevision_GenericReadError(t *testing.T) {
	sentinel := errors.New("io error")
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, sentinel
		},
	}

	svc := NewMVCCKVService(host, 1)
	kv, err := svc.GetRevision(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if kv != nil {
		t.Fatalf("expected nil kv, got %v", kv)
	}

	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestMVCCKVService_GetRevision_UnexpectedType(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return []byte("raw-bytes"), nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	kv, err := svc.GetRevision(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if kv != nil {
		t.Fatalf("expected nil kv, got %v", kv)
	}

	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
	var urt *unexpectedResultTypeError
	if !errors.As(re.Unwrap(), &urt) {
		t.Fatalf("expected *unexpectedResultTypeError wrapped, got %T", re.Unwrap())
	}
}

// ---------------------------------------------------------------------------
// GetAtRevision tests
// ---------------------------------------------------------------------------

func TestMVCCKVService_GetAtRevision_Success(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			q, ok := query.(MVCCGetAtRevisionQuery)
			if !ok {
				t.Fatalf("expected MVCCGetAtRevisionQuery, got %T", query)
			}
			if string(q.Key) != "histkey" {
				t.Fatalf("expected key histkey, got %s", string(q.Key))
			}
			if q.Revision != 15 {
				t.Fatalf("expected revision 15, got %d", q.Revision)
			}
			return &KeyValue{
				Key:            []byte("histkey"),
				Value:          []byte("old-value"),
				CreateRevision: 3,
				ModRevision:    15,
				Version:        5,
			}, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	kv, err := svc.GetAtRevision(context.Background(), "histkey", 15)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if string(kv.Value) != "old-value" {
		t.Fatalf("expected old-value, got %s", string(kv.Value))
	}
	if kv.ModRevision != 15 {
		t.Fatalf("expected ModRevision 15, got %d", kv.ModRevision)
	}
}

func TestMVCCKVService_GetAtRevision_RevisionCompacted(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, &mockRevisionCompactedError{requested: 5, compacted: 10}
		},
	}

	svc := NewMVCCKVService(host, 1)
	kv, err := svc.GetAtRevision(context.Background(), "key", 5)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if kv != nil {
		t.Fatalf("expected nil kv, got %v", kv)
	}

	var rce *RevisionCompactedError
	if !errors.As(err, &rce) {
		t.Fatalf("expected *RevisionCompactedError, got %T: %v", err, err)
	}
	if rce.Requested != 5 {
		t.Fatalf("expected Requested 5, got %d", rce.Requested)
	}
	if rce.Compacted != 10 {
		t.Fatalf("expected Compacted 10, got %d", rce.Compacted)
	}
}

func TestMVCCKVService_GetAtRevision_RevisionNotFound(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, &mockRevisionNotFoundError{key: "mykey", revision: 999}
		},
	}

	svc := NewMVCCKVService(host, 1)
	kv, err := svc.GetAtRevision(context.Background(), "mykey", 999)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if kv != nil {
		t.Fatalf("expected nil kv, got %v", kv)
	}

	var rnf *RevisionNotFoundError
	if !errors.As(err, &rnf) {
		t.Fatalf("expected *RevisionNotFoundError, got %T: %v", err, err)
	}
	if rnf.Revision != 999 {
		t.Fatalf("expected Revision 999, got %d", rnf.Revision)
	}
}

func TestMVCCKVService_GetAtRevision_KeyNotFound(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, &mockKeyNotFoundError{key: "nokey"}
		},
	}

	svc := NewMVCCKVService(host, 1)
	kv, err := svc.GetAtRevision(context.Background(), "nokey", 10)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if kv != nil {
		t.Fatalf("expected nil kv, got %v", kv)
	}

	var knf *KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Fatalf("expected *KeyNotFoundError, got %T: %v", err, err)
	}
	if knf.Key != "nokey" {
		t.Fatalf("expected key nokey, got %s", knf.Key)
	}
}

func TestMVCCKVService_GetAtRevision_GenericReadError(t *testing.T) {
	sentinel := errors.New("storage corrupt")
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, sentinel
		},
	}

	svc := NewMVCCKVService(host, 1)
	kv, err := svc.GetAtRevision(context.Background(), "key", 1)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if kv != nil {
		t.Fatalf("expected nil kv, got %v", kv)
	}

	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestMVCCKVService_GetAtRevision_MVCCBackendRequired(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, &fakeUnsupportedQueryTypeError{unsupported: true}
		},
	}

	svc := NewMVCCKVService(host, 1)
	kv, err := svc.GetAtRevision(context.Background(), "key", 1)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if kv != nil {
		t.Fatalf("expected nil kv, got %v", kv)
	}

	var mbr *MVCCBackendRequiredError
	if !errors.As(err, &mbr) {
		t.Fatalf("expected *MVCCBackendRequiredError, got %T: %v", err, err)
	}
}

func TestMVCCKVService_GetAtRevision_UnexpectedType(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return int64(42), nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	kv, err := svc.GetAtRevision(context.Background(), "key", 1)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if kv != nil {
		t.Fatalf("expected nil kv, got %v", kv)
	}

	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
	var urt *unexpectedResultTypeError
	if !errors.As(re.Unwrap(), &urt) {
		t.Fatalf("expected *unexpectedResultTypeError wrapped, got %T", re.Unwrap())
	}
}

// ---------------------------------------------------------------------------
// Range tests
// ---------------------------------------------------------------------------

func TestMVCCKVService_Range_Success(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			q, ok := query.(MVCCRangeQuery)
			if !ok {
				t.Fatalf("expected MVCCRangeQuery, got %T", query)
			}
			if string(q.Start) != "a" {
				t.Fatalf("expected start a, got %s", string(q.Start))
			}
			if string(q.End) != "z" {
				t.Fatalf("expected end z, got %s", string(q.End))
			}
			return &MVCCRangeResult{
				KeyValues: []*KeyValue{
					{Key: []byte("b"), Value: []byte("bval")},
					{Key: []byte("c"), Value: []byte("cval")},
				},
				// Count and Revision are deliberately different values so
				// this test cannot pass by coincidence if Range regresses
				// to returning the revision in the count's place.
				Count:    2,
				Revision: 50,
			}, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	kvs, count, revision, err := svc.Range(context.Background(), []byte("a"), []byte("z"))
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(kvs) != 2 {
		t.Fatalf("expected 2 kvs, got %d", len(kvs))
	}
	// Range's second return value is documented as the match count, not the
	// MVCC revision (see MVCCKVServicer.Range's doc comment).
	if count != 2 {
		t.Fatalf("expected count 2, got %d", count)
	}
	// The third return value is the MVCC revision the read was performed at,
	// which must be threaded through distinctly from the count.
	if revision != 50 {
		t.Fatalf("expected revision 50, got %d", revision)
	}
	if string(kvs[0].Key) != "b" {
		t.Fatalf("expected first key b, got %s", string(kvs[0].Key))
	}
}

func TestMVCCKVService_Range_WithOptions(t *testing.T) {
	var capturedQuery interface{}

	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			capturedQuery = query
			return &MVCCRangeResult{
				KeyValues: []*KeyValue{},
				Count:     0,
				Revision:  20,
			}, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	kvs, count, revision, err := svc.Range(context.Background(), []byte("x"), []byte("y"), WithLimit(10), WithKeysOnly())
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(kvs) != 0 {
		t.Fatalf("expected 0 kvs, got %d", len(kvs))
	}
	if count != 0 {
		t.Fatalf("expected count 0, got %d", count)
	}
	if revision != 20 {
		t.Fatalf("expected revision 20, got %d", revision)
	}

	rq, ok := capturedQuery.(MVCCRangeQuery)
	if !ok {
		t.Fatalf("expected MVCCRangeQuery, got %T", capturedQuery)
	}
	if rq.Limit != 10 {
		t.Fatalf("expected Limit=10, got %d", rq.Limit)
	}
	if !rq.KeysOnly {
		t.Fatal("expected KeysOnly=true")
	}
}

// TestMVCCKVService_Range_ThreadsSortOptions verifies WithSort's SortOrder
// and SortTarget are copied onto the MVCCRangeQuery built by Range,
// reproducing (as a regression guard) the bug where these fields were
// silently dropped before ever reaching the query.
func TestMVCCKVService_Range_ThreadsSortOptions(t *testing.T) {
	var capturedQuery interface{}

	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			capturedQuery = query
			return &MVCCRangeResult{KeyValues: []*KeyValue{}}, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	_, _, _, err := svc.Range(context.Background(), []byte("a"), []byte("z"), WithSort(SortDescend, SortByModRevision))
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	rq, ok := capturedQuery.(MVCCRangeQuery)
	if !ok {
		t.Fatalf("expected MVCCRangeQuery, got %T", capturedQuery)
	}
	if rq.SortOrder != SortDescend {
		t.Fatalf("expected SortOrder=SortDescend, got %v", rq.SortOrder)
	}
	if rq.SortTarget != SortByModRevision {
		t.Fatalf("expected SortTarget=SortByModRevision, got %v", rq.SortTarget)
	}
}

func TestMVCCKVService_Range_ReadError(t *testing.T) {
	sentinel := errors.New("range failure")
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, sentinel
		},
	}

	svc := NewMVCCKVService(host, 1)
	kvs, count, rev, err := svc.Range(context.Background(), []byte("a"), []byte("z"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if kvs != nil {
		t.Fatalf("expected nil kvs, got %v", kvs)
	}
	if count != 0 {
		t.Fatalf("expected count 0, got %d", count)
	}
	if rev != 0 {
		t.Fatalf("expected revision 0, got %d", rev)
	}

	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestMVCCKVService_Range_UnexpectedType(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return "wrong-type", nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	kvs, count, rev, err := svc.Range(context.Background(), []byte("a"), []byte("z"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if kvs != nil {
		t.Fatalf("expected nil kvs, got %v", kvs)
	}
	if count != 0 {
		t.Fatalf("expected count 0, got %d", count)
	}
	if rev != 0 {
		t.Fatalf("expected revision 0, got %d", rev)
	}

	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
	var urt *unexpectedResultTypeError
	if !errors.As(re.Unwrap(), &urt) {
		t.Fatalf("expected *unexpectedResultTypeError wrapped, got %T", re.Unwrap())
	}
}

// ---------------------------------------------------------------------------
// Compact tests
// ---------------------------------------------------------------------------

func TestMVCCKVService_Compact_Success(t *testing.T) {
	var capturedCmd []byte

	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			capturedCmd = make([]byte, len(cmd))
			copy(capturedCmd, cmd)
			return sm.Result{}, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	err := svc.Compact(context.Background(), 100)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	expected := protocol.EncodeMVCCCompact(100)
	if !bytes.Equal(capturedCmd, expected) {
		t.Fatalf("command mismatch: expected %v, got %v", expected, capturedCmd)
	}
}

func TestMVCCKVService_Compact_NotLeaderError(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, &quicraft.NotLeaderError{
				ShardID:       1,
				LeaderID:      9,
				LeaderAddress: "10.0.0.9:9100",
			}
		},
	}

	svc := NewMVCCKVService(host, 1)
	err := svc.Compact(context.Background(), 50)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
	if nle.LeaderID != 9 {
		t.Fatalf("expected LeaderID 9, got %d", nle.LeaderID)
	}
}

func TestMVCCKVService_Compact_GenericError(t *testing.T) {
	sentinel := errors.New("compact failed")
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, sentinel
		},
	}

	svc := NewMVCCKVService(host, 1)
	err := svc.Compact(context.Background(), 50)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var pe *ProposeError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProposeError, got %T: %v", err, err)
	}
	if !errors.Is(pe, sentinel) {
		t.Fatal("expected ProposeError to wrap sentinel error")
	}
}

// ---------------------------------------------------------------------------
// CurrentRevision tests
// ---------------------------------------------------------------------------

func TestMVCCKVService_CurrentRevision_Success(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, shardID uint64, query interface{}) (interface{}, error) {
			if shardID != 2 {
				t.Fatalf("expected shardID 2, got %d", shardID)
			}
			_, ok := query.(MVCCRevisionQuery)
			if !ok {
				t.Fatalf("expected MVCCRevisionQuery, got %T", query)
			}
			return int64(77), nil
		},
	}

	svc := NewMVCCKVService(host, 2)
	rev, err := svc.CurrentRevision(context.Background())
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if rev != 77 {
		t.Fatalf("expected revision 77, got %d", rev)
	}
}

func TestMVCCKVService_CurrentRevision_ReadError(t *testing.T) {
	sentinel := errors.New("cannot read revision")
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, sentinel
		},
	}

	svc := NewMVCCKVService(host, 1)
	rev, err := svc.CurrentRevision(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if rev != 0 {
		t.Fatalf("expected revision 0, got %d", rev)
	}

	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestMVCCKVService_CurrentRevision_UnexpectedType(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return "not-int64", nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	rev, err := svc.CurrentRevision(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if rev != 0 {
		t.Fatalf("expected revision 0, got %d", rev)
	}

	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
	var urt *unexpectedResultTypeError
	if !errors.As(re.Unwrap(), &urt) {
		t.Fatalf("expected *unexpectedResultTypeError wrapped, got %T", re.Unwrap())
	}
}

// ---------------------------------------------------------------------------
// translateProposeError tests
// ---------------------------------------------------------------------------

func TestMVCCKVService_translateProposeError_NotLeader(t *testing.T) {
	svc := &MVCCKVService{host: &mockHost{}, shardID: 1}
	err := svc.translateProposeError(&quicraft.NotLeaderError{
		ShardID:       42,
		LeaderID:      7,
		LeaderAddress: "10.0.0.7:9100",
	})

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
	if nle.ShardID != 42 {
		t.Fatalf("expected ShardID 42, got %d", nle.ShardID)
	}
	if nle.LeaderID != 7 {
		t.Fatalf("expected LeaderID 7, got %d", nle.LeaderID)
	}
	if nle.LeaderAddress != "10.0.0.7:9100" {
		t.Fatalf("expected LeaderAddress 10.0.0.7:9100, got %s", nle.LeaderAddress)
	}
}

func TestMVCCKVService_translateProposeError_Generic(t *testing.T) {
	svc := &MVCCKVService{host: &mockHost{}, shardID: 1}
	sentinel := errors.New("some propose error")
	err := svc.translateProposeError(sentinel)

	var pe *ProposeError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProposeError, got %T: %v", err, err)
	}
	if !errors.Is(pe, sentinel) {
		t.Fatal("expected ProposeError to wrap sentinel error")
	}
}

// ---------------------------------------------------------------------------
// translateReadError tests
// ---------------------------------------------------------------------------

func TestMVCCKVService_translateReadError_KeyNotFound(t *testing.T) {
	svc := &MVCCKVService{host: &mockHost{}, shardID: 1}
	err := svc.translateReadError(&mockKeyNotFoundError{key: "testkey"})

	var knf *KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Fatalf("expected *KeyNotFoundError, got %T: %v", err, err)
	}
	if knf.Key != "testkey" {
		t.Fatalf("expected key testkey, got %s", knf.Key)
	}
}

func TestMVCCKVService_translateReadError_RevisionCompacted(t *testing.T) {
	svc := &MVCCKVService{host: &mockHost{}, shardID: 1}
	err := svc.translateReadError(&mockRevisionCompactedError{requested: 3, compacted: 8})

	var rce *RevisionCompactedError
	if !errors.As(err, &rce) {
		t.Fatalf("expected *RevisionCompactedError, got %T: %v", err, err)
	}
	if rce.Requested != 3 {
		t.Fatalf("expected Requested 3, got %d", rce.Requested)
	}
	if rce.Compacted != 8 {
		t.Fatalf("expected Compacted 8, got %d", rce.Compacted)
	}
}

func TestMVCCKVService_translateReadError_RevisionNotFound(t *testing.T) {
	svc := &MVCCKVService{host: &mockHost{}, shardID: 1}
	err := svc.translateReadError(&mockRevisionNotFoundError{key: "rkey", revision: 123})

	var rnf *RevisionNotFoundError
	if !errors.As(err, &rnf) {
		t.Fatalf("expected *RevisionNotFoundError, got %T: %v", err, err)
	}
	if rnf.Revision != 123 {
		t.Fatalf("expected Revision 123, got %d", rnf.Revision)
	}
}

func TestMVCCKVService_translateReadError_NotLeader(t *testing.T) {
	svc := &MVCCKVService{host: &mockHost{}, shardID: 1}
	err := svc.translateReadError(&quicraft.NotLeaderError{
		ShardID:       10,
		LeaderID:      2,
		LeaderAddress: "10.0.0.2:9100",
	})

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

func TestMVCCKVService_translateReadError_Generic(t *testing.T) {
	svc := &MVCCKVService{host: &mockHost{}, shardID: 1}
	sentinel := errors.New("some read error")
	err := svc.translateReadError(sentinel)

	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
	if !errors.Is(re, sentinel) {
		t.Fatal("expected ReadError to wrap sentinel error")
	}
}

// ---------------------------------------------------------------------------
// prefixEnd tests
// ---------------------------------------------------------------------------

func TestPrefixEnd_NormalPrefix(t *testing.T) {
	result := prefixEnd([]byte("abc"))
	expected := []byte("abd")
	if !bytes.Equal(result, expected) {
		t.Fatalf("expected %v, got %v", expected, result)
	}
}

func TestPrefixEnd_SingleByte(t *testing.T) {
	result := prefixEnd([]byte{0x01})
	expected := []byte{0x02}
	if !bytes.Equal(result, expected) {
		t.Fatalf("expected %v, got %v", expected, result)
	}
}

func TestPrefixEnd_TrailingFF(t *testing.T) {
	result := prefixEnd([]byte{0x01, 0xFF})
	expected := []byte{0x02}
	if !bytes.Equal(result, expected) {
		t.Fatalf("expected %v, got %v", expected, result)
	}
}

func TestPrefixEnd_AllFF(t *testing.T) {
	result := prefixEnd([]byte{0xFF, 0xFF})
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
}

func TestPrefixEnd_Empty(t *testing.T) {
	result := prefixEnd([]byte{})
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
}

func TestPrefixEnd_MultiByteMiddleFF(t *testing.T) {
	// "a" + 0xFF + 0xFF should produce "b"
	result := prefixEnd([]byte{0x61, 0xFF, 0xFF})
	expected := []byte{0x62}
	if !bytes.Equal(result, expected) {
		t.Fatalf("expected %v, got %v", expected, result)
	}
}

func TestPrefixEnd_SingleNonFF(t *testing.T) {
	result := prefixEnd([]byte{0xFE})
	expected := []byte{0xFF}
	if !bytes.Equal(result, expected) {
		t.Fatalf("expected %v, got %v", expected, result)
	}
}

// ---------------------------------------------------------------------------
// Edge case / additional coverage tests
// ---------------------------------------------------------------------------

func TestMVCCKVService_Get_NotLeaderError(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, &quicraft.NotLeaderError{
				ShardID:       1,
				LeaderID:      4,
				LeaderAddress: "10.0.0.4:9100",
			}
		},
	}

	svc := NewMVCCKVService(host, 1)
	val, err := svc.Get(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if val != nil {
		t.Fatalf("expected nil val, got %v", val)
	}

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
	if nle.LeaderID != 4 {
		t.Fatalf("expected LeaderID 4, got %d", nle.LeaderID)
	}
}

func TestMVCCKVService_List_NotLeaderError(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, &quicraft.NotLeaderError{
				ShardID:       1,
				LeaderID:      6,
				LeaderAddress: "10.0.0.6:9100",
			}
		},
	}

	svc := NewMVCCKVService(host, 1)
	keys, err := svc.List(context.Background(), "pfx")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if keys != nil {
		t.Fatalf("expected nil keys, got %v", keys)
	}

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
}

func TestMVCCKVService_Range_NotLeaderError(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, &quicraft.NotLeaderError{
				ShardID:       1,
				LeaderID:      8,
				LeaderAddress: "10.0.0.8:9100",
			}
		},
	}

	svc := NewMVCCKVService(host, 1)
	kvs, count, rev, err := svc.Range(context.Background(), []byte("a"), []byte("z"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if kvs != nil {
		t.Fatalf("expected nil kvs, got %v", kvs)
	}
	if count != 0 {
		t.Fatalf("expected count 0, got %d", count)
	}
	if rev != 0 {
		t.Fatalf("expected revision 0, got %d", rev)
	}

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
}

func TestMVCCKVService_CurrentRevision_NotLeaderError(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, &quicraft.NotLeaderError{
				ShardID:       1,
				LeaderID:      11,
				LeaderAddress: "10.0.0.11:9100",
			}
		},
	}

	svc := NewMVCCKVService(host, 1)
	rev, err := svc.CurrentRevision(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if rev != 0 {
		t.Fatalf("expected revision 0, got %d", rev)
	}

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
}

func TestMVCCKVService_GetRevision_NotLeaderError(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, &quicraft.NotLeaderError{
				ShardID:       1,
				LeaderID:      12,
				LeaderAddress: "10.0.0.12:9100",
			}
		},
	}

	svc := NewMVCCKVService(host, 1)
	kv, err := svc.GetRevision(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if kv != nil {
		t.Fatalf("expected nil kv, got %v", kv)
	}

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
}

func TestMVCCKVService_GetAtRevision_NotLeaderError(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, &quicraft.NotLeaderError{
				ShardID:       1,
				LeaderID:      13,
				LeaderAddress: "10.0.0.13:9100",
			}
		},
	}

	svc := NewMVCCKVService(host, 1)
	kv, err := svc.GetAtRevision(context.Background(), "key", 5)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if kv != nil {
		t.Fatalf("expected nil kv, got %v", kv)
	}

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
}

func TestMVCCKVService_Put_EmptyKeyAndValue(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			expected := protocol.EncodeMVCCPut([]byte(""), []byte(""), 0)
			if !bytes.Equal(cmd, expected) {
				t.Fatalf("command mismatch for empty key/value")
			}
			return sm.Result{}, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	err := svc.Put(context.Background(), "", []byte(""))
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestMVCCKVService_Delete_EmptyKey(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			expected := protocol.EncodeMVCCDelete([]byte(""))
			if !bytes.Equal(cmd, expected) {
				t.Fatalf("command mismatch for empty key delete")
			}
			return sm.Result{}, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	err := svc.Delete(context.Background(), "")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestMVCCKVService_Compact_ZeroRevision(t *testing.T) {
	var capturedCmd []byte

	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			capturedCmd = make([]byte, len(cmd))
			copy(capturedCmd, cmd)
			return sm.Result{}, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	err := svc.Compact(context.Background(), 0)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	expected := protocol.EncodeMVCCCompact(0)
	if !bytes.Equal(capturedCmd, expected) {
		t.Fatalf("command mismatch: expected %v, got %v", expected, capturedCmd)
	}
}

func TestMVCCKVService_Get_NilResult(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	val, err := svc.Get(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error for nil result, got nil")
	}
	if val != nil {
		t.Fatalf("expected nil val, got %v", val)
	}

	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestMVCCKVService_CurrentRevision_ZeroValue(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return int64(0), nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	rev, err := svc.CurrentRevision(context.Background())
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if rev != 0 {
		t.Fatalf("expected revision 0, got %d", rev)
	}
}

func TestMVCCKVService_Put_LargeValue(t *testing.T) {
	largeVal := make([]byte, 64*1024) // 64KB
	for i := range largeVal {
		largeVal[i] = byte(i % 256)
	}

	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			expected := protocol.EncodeMVCCPut([]byte("bigkey"), largeVal, 0)
			if !bytes.Equal(cmd, expected) {
				t.Fatalf("command mismatch for large value")
			}
			return sm.Result{}, nil
		},
	}

	svc := NewMVCCKVService(host, 1)
	err := svc.Put(context.Background(), "bigkey", largeVal)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Mock error interface compliance tests
// ---------------------------------------------------------------------------

func TestMockKeyNotFoundErr_ImplementsInterface(t *testing.T) {
	var e keyNotFoundErr = &mockKeyNotFoundError{key: "k"}
	if e.KeyNotFound() != "k" {
		t.Fatalf("expected k, got %s", e.KeyNotFound())
	}
	if e.Error() != "key not found: k" {
		t.Fatalf("unexpected error string: %s", e.Error())
	}
}

func TestMockRevisionCompactedErr_ImplementsInterface(t *testing.T) {
	var e revisionCompactedErr = &mockRevisionCompactedError{requested: 1, compacted: 5}
	req, comp := e.RevisionCompacted()
	if req != 1 || comp != 5 {
		t.Fatalf("expected (1, 5), got (%d, %d)", req, comp)
	}
}

func TestMockRevisionNotFoundErr_ImplementsInterface(t *testing.T) {
	var e revisionNotFoundErr = &mockRevisionNotFoundError{key: "rk", revision: 77}
	key, rev := e.RevisionNotFound()
	if key != "rk" || rev != 77 {
		t.Fatalf("expected (rk, 77), got (%s, %d)", key, rev)
	}
}

// ---------------------------------------------------------------------------
// MVCCKVService.Scan
// ---------------------------------------------------------------------------

func TestMVCCKVService_Scan(t *testing.T) {
	expected := map[string][]byte{
		"app/key1": []byte("v1"),
		"app/key2": []byte("v2"),
	}
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return expected, nil
		},
	}
	svc := NewMVCCKVService(host, 1)
	result, err := svc.Scan(context.Background(), "app/")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(result))
	}
}

func TestMVCCKVService_Scan_NilResult(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, nil
		},
	}
	svc := NewMVCCKVService(host, 1)
	result, err := svc.Scan(context.Background(), "empty/")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil result, got %v", result)
	}
}

func TestMVCCKVService_Scan_ReadError(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, errors.New("read failed")
		},
	}
	svc := NewMVCCKVService(host, 1)
	_, err := svc.Scan(context.Background(), "prefix/")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestMVCCKVService_Scan_UnexpectedType(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return "not a map", nil
		},
	}
	svc := NewMVCCKVService(host, 1)
	_, err := svc.Scan(context.Background(), "prefix/")
	if err == nil {
		t.Fatal("expected error")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T", err)
	}
}
