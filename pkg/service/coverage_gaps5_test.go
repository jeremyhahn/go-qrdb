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

// coverage_gaps5_test.go covers remaining 0% items: MVCC stale/mode/consistency
// methods, error types, index service, and lock service bootstrap.
package service

import (
	"context"
	"errors"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// errors.go – ImportArchiveError, BarrierNotConfiguredError,
//              BarrierManagerNotConfiguredError (0%)
// ---------------------------------------------------------------------------

func TestImportArchiveError_ErrorAndUnwrap(t *testing.T) {
	cause := errors.New("zip: not a valid archive")
	e := &ImportArchiveError{Err: cause}
	want := "service: import archive extraction failed: zip: not a valid archive"
	if e.Error() != want {
		t.Fatalf("got %q, want %q", e.Error(), want)
	}
	if !errors.Is(e, cause) {
		t.Fatal("errors.Is should match cause through Unwrap")
	}
}

func TestBarrierManagerNotConfiguredError_Error(t *testing.T) {
	e := &BarrierManagerNotConfiguredError{}
	want := "service: database barrier manager not configured"
	if e.Error() != want {
		t.Fatalf("got %q, want %q", e.Error(), want)
	}
}

// ---------------------------------------------------------------------------
// mvcc_service.go – GetStale, ListStale, ScanStale (all 0%)
// ---------------------------------------------------------------------------

func TestMVCCKVService_GetStale_Error(t *testing.T) {
	host := &mockHost{
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, &quicraft.NotLeaderError{ShardID: 1}
		},
	}
	svc := NewMVCCKVService(host, 1)
	_, err := svc.GetStale(context.Background(), "k")
	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
}

func TestMVCCKVService_ListStale_WithPrefix(t *testing.T) {
	rr := &MVCCRangeResult{
		KeyValues: []*KeyValue{
			{Key: []byte("user:alice"), Value: []byte("1")},
		},
	}
	host := &mockHost{
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return rr, nil
		},
	}
	svc := NewMVCCKVService(host, 1)
	keys, err := svc.ListStale(context.Background(), "user:")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(keys))
	}
}

func TestMVCCKVService_ListStale_Error(t *testing.T) {
	host := &mockHost{
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, errors.New("stale read failed")
		},
	}
	svc := NewMVCCKVService(host, 1)
	_, err := svc.ListStale(context.Background(), "")
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestMVCCKVService_ScanStale_Error(t *testing.T) {
	host := &mockHost{
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, errors.New("stale failure")
		},
	}
	svc := NewMVCCKVService(host, 1)
	_, err := svc.ScanStale(context.Background(), "pfx")
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// mvcc_service.go – PutWithMode, DeleteWithMode (0%)
// ---------------------------------------------------------------------------

func TestMVCCKVService_DeleteWithMode_Error(t *testing.T) {
	sentinel := errors.New("IO error")
	host := &mockHost{
		proposeWithModeFn: func(_ context.Context, _ uint64, _ []byte, _ writemode.Mode) (sm.Result, error) {
			return sm.Result{}, sentinel
		},
	}
	svc := NewMVCCKVService(host, 1)
	err := svc.DeleteWithMode(context.Background(), "k", writemode.WALDurable)
	var pe *ProposeError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProposeError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// mvcc_service.go – GetWithConsistency, ListWithConsistency,
//                   ScanWithConsistency, ExistsWithConsistency (0%)
// ---------------------------------------------------------------------------

func TestMVCCKVService_GetWithConsistency_Error(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return nil, &quicraft.NotLeaderError{ShardID: 1}
		},
	}
	svc := NewMVCCKVService(host, 1)
	_, err := svc.GetWithConsistency(context.Background(), "k", consistency.ReadOptions{})
	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
}

func TestMVCCKVService_ExistsWithConsistency_Exists(t *testing.T) {
	kv := &KeyValue{Key: []byte("k"), Value: []byte("v")}
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return kv, nil
		},
	}
	svc := NewMVCCKVService(host, 1)
	exists, err := svc.ExistsWithConsistency(context.Background(), "k", consistency.ReadOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Fatal("expected exists=true")
	}
}

func TestMVCCKVService_ExistsWithConsistency_NoErrorIsTrue(t *testing.T) {
	// MVCCKVService.ExistsWithConsistency returns true whenever ReadWithConsistency
	// succeeds (no error), regardless of the result value.
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return nil, nil
		},
	}
	svc := NewMVCCKVService(host, 1)
	exists, err := svc.ExistsWithConsistency(context.Background(), "k", consistency.ReadOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Fatal("expected exists=true when read succeeds")
	}
}

func TestMVCCKVService_ExistsWithConsistency_KeyNotFound(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return nil, &mockKeyNotFoundError{key: "k"}
		},
	}
	svc := NewMVCCKVService(host, 1)
	exists, err := svc.ExistsWithConsistency(context.Background(), "k", consistency.ReadOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Fatal("expected exists=false for key-not-found")
	}
}

// ---------------------------------------------------------------------------
// mvcc_service.go – PutWithLeaseAndMode (0%)
// ---------------------------------------------------------------------------

func TestMVCCKVService_PutWithLeaseAndMode_Error(t *testing.T) {
	host := &mockHost{
		proposeWithModeFn: func(_ context.Context, _ uint64, _ []byte, _ writemode.Mode) (sm.Result, error) {
			return sm.Result{}, &quicraft.NotLeaderError{ShardID: 1}
		},
	}
	svc := NewMVCCKVService(host, 1)
	_, err := svc.PutWithLeaseAndMode(context.Background(), "k", []byte("v"), int64(0), writemode.Sync)
	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// mvcc_service.go – GetRevisionWithConsistency, GetAtRevisionWithConsistency,
//                   RangeWithConsistency (0%)
// ---------------------------------------------------------------------------
