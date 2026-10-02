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

//go:build !integration

package etcd

import (
	"context"
	"errors"
	"testing"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Mock MVCCKVServicer
// ---------------------------------------------------------------------------

type mockMVCCKVServicer struct {
	service.MVCCKVServicer

	getRevisionFn     func(ctx context.Context, key string) (*kv.KeyValue, error)
	putWithLeaseFn    func(ctx context.Context, key string, value []byte, leaseID int64) (*kv.KeyValue, error)
	deleteFn          func(ctx context.Context, key string) error
	rangeFn           func(ctx context.Context, start, end []byte, opts ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error)
	compactFn         func(ctx context.Context, revision int64) error
	currentRevisionFn func(ctx context.Context) (int64, error)
}

func (m *mockMVCCKVServicer) GetRevision(ctx context.Context, key string) (*kv.KeyValue, error) {
	if m.getRevisionFn != nil {
		return m.getRevisionFn(ctx, key)
	}
	return nil, errors.New("key not found")
}

func (m *mockMVCCKVServicer) PutWithLease(ctx context.Context, key string, value []byte, leaseID int64) (*kv.KeyValue, error) {
	if m.putWithLeaseFn != nil {
		return m.putWithLeaseFn(ctx, key, value, leaseID)
	}
	return nil, errors.New("not implemented")
}

func (m *mockMVCCKVServicer) Delete(ctx context.Context, key string) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, key)
	}
	return nil
}

func (m *mockMVCCKVServicer) Range(ctx context.Context, start, end []byte, opts ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
	if m.rangeFn != nil {
		return m.rangeFn(ctx, start, end, opts...)
	}
	return nil, 0, 0, nil
}

func (m *mockMVCCKVServicer) Compact(ctx context.Context, revision int64) error {
	if m.compactFn != nil {
		return m.compactFn(ctx, revision)
	}
	return nil
}

func (m *mockMVCCKVServicer) CurrentRevision(ctx context.Context) (int64, error) {
	if m.currentRevisionFn != nil {
		return m.currentRevisionFn(ctx)
	}
	return 1, nil
}

// ---------------------------------------------------------------------------
// Mock TransactionServicer
// ---------------------------------------------------------------------------

type mockTransactionServicer struct {
	service.TransactionServicer

	executeFn func(ctx context.Context, compares []service.Compare, successOps, failureOps []service.Op) (*service.TransactionResponse, error)
}

func (m *mockTransactionServicer) Execute(ctx context.Context, compares []service.Compare, successOps, failureOps []service.Op) (*service.TransactionResponse, error) {
	if m.executeFn != nil {
		return m.executeFn(ctx, compares, successOps, failureOps)
	}
	return nil, errors.New("not implemented")
}

// ---------------------------------------------------------------------------
// Helper: create a kvServer with defaults
// ---------------------------------------------------------------------------

func newTestKVServer(mvcc *mockMVCCKVServicer, tx service.TransactionServicer) *kvServer {
	return &kvServer{
		mvccSvc:   mvcc,
		txSvc:     tx,
		clusterID: 100,
		memberID:  200,
	}
}

// ---------------------------------------------------------------------------
// Range tests
// ---------------------------------------------------------------------------

func TestRange_SingleKeyGetSuccess(t *testing.T) {
	t.Parallel()

	mvcc := &mockMVCCKVServicer{
		currentRevisionFn: func(_ context.Context) (int64, error) {
			return 5, nil
		},
		getRevisionFn: func(_ context.Context, key string) (*kv.KeyValue, error) {
			if key != "foo" {
				t.Errorf("unexpected key: %s", key)
			}
			return &kv.KeyValue{
				Key:            []byte("foo"),
				Value:          []byte("bar"),
				CreateRevision: 2,
				ModRevision:    5,
				Version:        3,
			}, nil
		},
	}

	srv := newTestKVServer(mvcc, nil)
	resp, err := srv.Range(context.Background(), &pb.RangeRequest{
		Key: []byte("foo"),
	})
	if err != nil {
		t.Fatalf("Range() returned error: %v", err)
	}
	if resp.Count != 1 {
		t.Errorf("Count = %d, want 1", resp.Count)
	}
	if len(resp.Kvs) != 1 {
		t.Fatalf("len(Kvs) = %d, want 1", len(resp.Kvs))
	}
	if string(resp.Kvs[0].Key) != "foo" {
		t.Errorf("Key = %q, want %q", resp.Kvs[0].Key, "foo")
	}
	if string(resp.Kvs[0].Value) != "bar" {
		t.Errorf("Value = %q, want %q", resp.Kvs[0].Value, "bar")
	}
	if resp.Header.ClusterId != 100 {
		t.Errorf("ClusterId = %d, want 100", resp.Header.ClusterId)
	}
	if resp.Header.MemberId != 200 {
		t.Errorf("MemberId = %d, want 200", resp.Header.MemberId)
	}
	if resp.Header.Revision != 5 {
		t.Errorf("Revision = %d, want 5", resp.Header.Revision)
	}
}

func TestRange_SingleKeyNotFound(t *testing.T) {
	t.Parallel()

	mvcc := &mockMVCCKVServicer{
		currentRevisionFn: func(_ context.Context) (int64, error) {
			return 3, nil
		},
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return nil, errors.New("key not found")
		},
	}

	srv := newTestKVServer(mvcc, nil)
	resp, err := srv.Range(context.Background(), &pb.RangeRequest{
		Key: []byte("missing"),
	})
	if err != nil {
		t.Fatalf("Range() returned error: %v, want nil (etcd semantics)", err)
	}
	if resp.Count != 0 {
		t.Errorf("Count = %d, want 0", resp.Count)
	}
	if len(resp.Kvs) != 0 {
		t.Errorf("len(Kvs) = %d, want 0", len(resp.Kvs))
	}
}

func TestRange_EmptyKeyReturnsInvalidArgument(t *testing.T) {
	t.Parallel()

	srv := newTestKVServer(&mockMVCCKVServicer{}, nil)
	_, err := srv.Range(context.Background(), &pb.RangeRequest{})
	if err == nil {
		t.Fatal("Range() with empty key should return error")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Errorf("code = %v, want %v", st.Code(), codes.InvalidArgument)
	}
}

func TestRange_WithLimitOption(t *testing.T) {
	t.Parallel()

	var capturedOpts []service.RangeOption
	mvcc := &mockMVCCKVServicer{
		currentRevisionFn: func(_ context.Context) (int64, error) {
			return 10, nil
		},
		rangeFn: func(_ context.Context, start, end []byte, opts ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
			capturedOpts = opts
			return []*kv.KeyValue{
				{Key: []byte("a"), Value: []byte("1"), ModRevision: 8, Version: 1},
			}, 3, 8, nil
		},
	}

	srv := newTestKVServer(mvcc, nil)
	resp, err := srv.Range(context.Background(), &pb.RangeRequest{
		Key:      []byte("a"),
		RangeEnd: []byte("z"),
		Limit:    1,
	})
	if err != nil {
		t.Fatalf("Range() returned error: %v", err)
	}
	if resp.Count != 3 {
		t.Errorf("Count = %d, want 3", resp.Count)
	}
	if len(resp.Kvs) != 1 {
		t.Errorf("len(Kvs) = %d, want 1", len(resp.Kvs))
	}

	// Verify the limit option was passed through.
	ro := service.ApplyRangeOptions(capturedOpts)
	if ro.Limit != 1 {
		t.Errorf("Limit option = %d, want 1", ro.Limit)
	}
}

func TestRange_AllKeysRange(t *testing.T) {
	t.Parallel()

	var capturedStart, capturedEnd []byte
	mvcc := &mockMVCCKVServicer{
		currentRevisionFn: func(_ context.Context) (int64, error) {
			return 7, nil
		},
		rangeFn: func(_ context.Context, start, end []byte, _ ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
			capturedStart = start
			capturedEnd = end
			return []*kv.KeyValue{
				{Key: []byte("a"), Value: []byte("1"), ModRevision: 3, Version: 1},
				{Key: []byte("b"), Value: []byte("2"), ModRevision: 5, Version: 1},
			}, 2, 5, nil
		},
	}

	srv := newTestKVServer(mvcc, nil)
	resp, err := srv.Range(context.Background(), &pb.RangeRequest{
		Key:      []byte{0},
		RangeEnd: []byte{0},
	})
	if err != nil {
		t.Fatalf("Range() returned error: %v", err)
	}
	if resp.Count != 2 {
		t.Errorf("Count = %d, want 2", resp.Count)
	}

	// Verify all-keys range expands start/end correctly.
	if len(capturedStart) != 1 || capturedStart[0] != 0 {
		t.Errorf("start = %v, want [0]", capturedStart)
	}
	expectedEnd := []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	if len(capturedEnd) != len(expectedEnd) {
		t.Errorf("end length = %d, want %d", len(capturedEnd), len(expectedEnd))
	}
	for i, b := range capturedEnd {
		if b != expectedEnd[i] {
			t.Errorf("end[%d] = %x, want %x", i, b, expectedEnd[i])
		}
	}
}

func TestRange_RangeError(t *testing.T) {
	t.Parallel()

	mvcc := &mockMVCCKVServicer{
		currentRevisionFn: func(_ context.Context) (int64, error) {
			return 1, nil
		},
		rangeFn: func(_ context.Context, _, _ []byte, _ ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
			return nil, 0, 0, errors.New("storage failure")
		},
	}

	srv := newTestKVServer(mvcc, nil)
	_, err := srv.Range(context.Background(), &pb.RangeRequest{
		Key:      []byte("a"),
		RangeEnd: []byte("z"),
	})
	if err == nil {
		t.Fatal("Range() should return error on storage failure")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.Internal {
		t.Errorf("code = %v, want %v", st.Code(), codes.Internal)
	}
}

func TestRange_WithRevisionAndCountOnlyAndKeysOnly(t *testing.T) {
	t.Parallel()

	var capturedOpts []service.RangeOption
	mvcc := &mockMVCCKVServicer{
		currentRevisionFn: func(_ context.Context) (int64, error) {
			return 20, nil
		},
		rangeFn: func(_ context.Context, _, _ []byte, opts ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
			capturedOpts = opts
			return nil, 5, 20, nil
		},
	}

	srv := newTestKVServer(mvcc, nil)
	resp, err := srv.Range(context.Background(), &pb.RangeRequest{
		Key:       []byte("prefix"),
		RangeEnd:  []byte("prefiy"),
		Revision:  15,
		CountOnly: true,
		KeysOnly:  true,
	})
	if err != nil {
		t.Fatalf("Range() returned error: %v", err)
	}
	if resp.Count != 5 {
		t.Errorf("Count = %d, want 5", resp.Count)
	}

	ro := service.ApplyRangeOptions(capturedOpts)
	if ro.Revision != 15 {
		t.Errorf("Revision option = %d, want 15", ro.Revision)
	}
	if !ro.CountOnly {
		t.Error("CountOnly option = false, want true")
	}
	if !ro.KeysOnly {
		t.Error("KeysOnly option = false, want true")
	}
}

// ---------------------------------------------------------------------------
// Put tests
// ---------------------------------------------------------------------------

func TestPut_BasicSuccess(t *testing.T) {
	t.Parallel()

	mvcc := &mockMVCCKVServicer{
		putWithLeaseFn: func(_ context.Context, key string, value []byte, leaseID int64) (*kv.KeyValue, error) {
			if key != "mykey" {
				t.Errorf("key = %q, want %q", key, "mykey")
			}
			if string(value) != "myval" {
				t.Errorf("value = %q, want %q", value, "myval")
			}
			if leaseID != 0 {
				t.Errorf("leaseID = %d, want 0", leaseID)
			}
			return &kv.KeyValue{
				Key:            []byte("mykey"),
				Value:          []byte("myval"),
				CreateRevision: 10,
				ModRevision:    10,
				Version:        1,
			}, nil
		},
	}

	srv := newTestKVServer(mvcc, nil)
	resp, err := srv.Put(context.Background(), &pb.PutRequest{
		Key:   []byte("mykey"),
		Value: []byte("myval"),
	})
	if err != nil {
		t.Fatalf("Put() returned error: %v", err)
	}
	if resp.Header.Revision != 10 {
		t.Errorf("Revision = %d, want 10", resp.Header.Revision)
	}
	if resp.PrevKv != nil {
		t.Error("PrevKv should be nil when PrevKv flag is not set")
	}
}

func TestPut_WithPrevKV(t *testing.T) {
	t.Parallel()

	mvcc := &mockMVCCKVServicer{
		getRevisionFn: func(_ context.Context, key string) (*kv.KeyValue, error) {
			return &kv.KeyValue{
				Key:            []byte(key),
				Value:          []byte("old-value"),
				CreateRevision: 5,
				ModRevision:    7,
				Version:        2,
			}, nil
		},
		putWithLeaseFn: func(_ context.Context, _ string, _ []byte, _ int64) (*kv.KeyValue, error) {
			return &kv.KeyValue{
				Key:            []byte("mykey"),
				Value:          []byte("new-value"),
				CreateRevision: 5,
				ModRevision:    8,
				Version:        3,
			}, nil
		},
	}

	srv := newTestKVServer(mvcc, nil)
	resp, err := srv.Put(context.Background(), &pb.PutRequest{
		Key:    []byte("mykey"),
		Value:  []byte("new-value"),
		PrevKv: true,
	})
	if err != nil {
		t.Fatalf("Put() returned error: %v", err)
	}
	if resp.PrevKv == nil {
		t.Fatal("PrevKv should not be nil")
	}
	if string(resp.PrevKv.Value) != "old-value" {
		t.Errorf("PrevKv.Value = %q, want %q", resp.PrevKv.Value, "old-value")
	}
	if resp.PrevKv.Version != 2 {
		t.Errorf("PrevKv.Version = %d, want 2", resp.PrevKv.Version)
	}
}

func TestPut_WithPrevKVNotFound(t *testing.T) {
	t.Parallel()

	mvcc := &mockMVCCKVServicer{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return nil, errors.New("key not found")
		},
		putWithLeaseFn: func(_ context.Context, _ string, _ []byte, _ int64) (*kv.KeyValue, error) {
			return &kv.KeyValue{
				Key:         []byte("newkey"),
				Value:       []byte("val"),
				ModRevision: 10,
				Version:     1,
			}, nil
		},
	}

	srv := newTestKVServer(mvcc, nil)
	resp, err := srv.Put(context.Background(), &pb.PutRequest{
		Key:    []byte("newkey"),
		Value:  []byte("val"),
		PrevKv: true,
	})
	if err != nil {
		t.Fatalf("Put() returned error: %v", err)
	}
	if resp.PrevKv != nil {
		t.Error("PrevKv should be nil when key did not previously exist")
	}
}

func TestPut_EmptyKeyReturnsInvalidArgument(t *testing.T) {
	t.Parallel()

	srv := newTestKVServer(&mockMVCCKVServicer{}, nil)
	_, err := srv.Put(context.Background(), &pb.PutRequest{
		Value: []byte("val"),
	})
	if err == nil {
		t.Fatal("Put() with empty key should return error")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Errorf("code = %v, want %v", st.Code(), codes.InvalidArgument)
	}
}

func TestPut_WithLease(t *testing.T) {
	t.Parallel()

	var capturedLeaseID int64
	mvcc := &mockMVCCKVServicer{
		putWithLeaseFn: func(_ context.Context, _ string, _ []byte, leaseID int64) (*kv.KeyValue, error) {
			capturedLeaseID = leaseID
			return &kv.KeyValue{
				Key:         []byte("leased"),
				Value:       []byte("val"),
				ModRevision: 15,
				Version:     1,
				LeaseID:     leaseID,
			}, nil
		},
	}

	srv := newTestKVServer(mvcc, nil)
	_, err := srv.Put(context.Background(), &pb.PutRequest{
		Key:   []byte("leased"),
		Value: []byte("val"),
		Lease: 42,
	})
	if err != nil {
		t.Fatalf("Put() returned error: %v", err)
	}
	if capturedLeaseID != 42 {
		t.Errorf("leaseID = %d, want 42", capturedLeaseID)
	}
}

func TestPut_ServiceError(t *testing.T) {
	t.Parallel()

	mvcc := &mockMVCCKVServicer{
		putWithLeaseFn: func(_ context.Context, _ string, _ []byte, _ int64) (*kv.KeyValue, error) {
			return nil, errors.New("disk full")
		},
	}

	srv := newTestKVServer(mvcc, nil)
	_, err := srv.Put(context.Background(), &pb.PutRequest{
		Key:   []byte("key"),
		Value: []byte("val"),
	})
	if err == nil {
		t.Fatal("Put() should return error on service failure")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.Internal {
		t.Errorf("code = %v, want %v", st.Code(), codes.Internal)
	}
}

// ---------------------------------------------------------------------------
// DeleteRange tests
// ---------------------------------------------------------------------------

func TestDeleteRange_SingleKeySuccess(t *testing.T) {
	t.Parallel()

	var deletedKey string
	mvcc := &mockMVCCKVServicer{
		deleteFn: func(_ context.Context, key string) error {
			deletedKey = key
			return nil
		},
		currentRevisionFn: func(_ context.Context) (int64, error) {
			return 12, nil
		},
	}

	srv := newTestKVServer(mvcc, nil)
	resp, err := srv.DeleteRange(context.Background(), &pb.DeleteRangeRequest{
		Key: []byte("delme"),
	})
	if err != nil {
		t.Fatalf("DeleteRange() returned error: %v", err)
	}
	if deletedKey != "delme" {
		t.Errorf("deleted key = %q, want %q", deletedKey, "delme")
	}
	if resp.Deleted != 1 {
		t.Errorf("Deleted = %d, want 1", resp.Deleted)
	}
	if resp.Header.Revision != 12 {
		t.Errorf("Revision = %d, want 12", resp.Header.Revision)
	}
}

func TestDeleteRange_SingleKeyWithPrevKV(t *testing.T) {
	t.Parallel()

	mvcc := &mockMVCCKVServicer{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{
				Key:            []byte("delme"),
				Value:          []byte("old"),
				CreateRevision: 2,
				ModRevision:    5,
				Version:        3,
			}, nil
		},
		deleteFn: func(_ context.Context, _ string) error {
			return nil
		},
		currentRevisionFn: func(_ context.Context) (int64, error) {
			return 6, nil
		},
	}

	srv := newTestKVServer(mvcc, nil)
	resp, err := srv.DeleteRange(context.Background(), &pb.DeleteRangeRequest{
		Key:    []byte("delme"),
		PrevKv: true,
	})
	if err != nil {
		t.Fatalf("DeleteRange() returned error: %v", err)
	}
	if resp.Deleted != 1 {
		t.Errorf("Deleted = %d, want 1", resp.Deleted)
	}
	if len(resp.PrevKvs) != 1 {
		t.Fatalf("len(PrevKvs) = %d, want 1", len(resp.PrevKvs))
	}
	if string(resp.PrevKvs[0].Value) != "old" {
		t.Errorf("PrevKvs[0].Value = %q, want %q", resp.PrevKvs[0].Value, "old")
	}
}

func TestDeleteRange_EmptyKeyReturnsInvalidArgument(t *testing.T) {
	t.Parallel()

	srv := newTestKVServer(&mockMVCCKVServicer{}, nil)
	_, err := srv.DeleteRange(context.Background(), &pb.DeleteRangeRequest{})
	if err == nil {
		t.Fatal("DeleteRange() with empty key should return error")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Errorf("code = %v, want %v", st.Code(), codes.InvalidArgument)
	}
}

func TestDeleteRange_RangeDeleteWithPrevKV(t *testing.T) {
	t.Parallel()

	deletedKeys := make(map[string]bool)
	mvcc := &mockMVCCKVServicer{
		rangeFn: func(_ context.Context, _, _ []byte, _ ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
			return []*kv.KeyValue{
				{Key: []byte("a"), Value: []byte("1"), ModRevision: 3, Version: 1},
				{Key: []byte("b"), Value: []byte("2"), ModRevision: 4, Version: 1},
				{Key: []byte("c"), Value: []byte("3"), ModRevision: 5, Version: 1},
			}, 3, 5, nil
		},
		deleteFn: func(_ context.Context, key string) error {
			deletedKeys[key] = true
			return nil
		},
		currentRevisionFn: func(_ context.Context) (int64, error) {
			return 8, nil
		},
	}

	srv := newTestKVServer(mvcc, nil)
	resp, err := srv.DeleteRange(context.Background(), &pb.DeleteRangeRequest{
		Key:      []byte("a"),
		RangeEnd: []byte("d"),
		PrevKv:   true,
	})
	if err != nil {
		t.Fatalf("DeleteRange() returned error: %v", err)
	}
	if resp.Deleted != 3 {
		t.Errorf("Deleted = %d, want 3", resp.Deleted)
	}
	if len(resp.PrevKvs) != 3 {
		t.Fatalf("len(PrevKvs) = %d, want 3", len(resp.PrevKvs))
	}
	for _, k := range []string{"a", "b", "c"} {
		if !deletedKeys[k] {
			t.Errorf("key %q was not deleted", k)
		}
	}
}

func TestDeleteRange_RangeDeleteWithoutPrevKV(t *testing.T) {
	t.Parallel()

	mvcc := &mockMVCCKVServicer{
		rangeFn: func(_ context.Context, _, _ []byte, _ ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
			return []*kv.KeyValue{
				{Key: []byte("x"), Value: []byte("1")},
			}, 1, 9, nil
		},
		deleteFn: func(_ context.Context, _ string) error {
			return nil
		},
		currentRevisionFn: func(_ context.Context) (int64, error) {
			return 9, nil
		},
	}

	srv := newTestKVServer(mvcc, nil)
	resp, err := srv.DeleteRange(context.Background(), &pb.DeleteRangeRequest{
		Key:      []byte("x"),
		RangeEnd: []byte("z"),
	})
	if err != nil {
		t.Fatalf("DeleteRange() returned error: %v", err)
	}
	if resp.Deleted != 1 {
		t.Errorf("Deleted = %d, want 1", resp.Deleted)
	}
	if len(resp.PrevKvs) != 0 {
		t.Errorf("len(PrevKvs) = %d, want 0", len(resp.PrevKvs))
	}
}

func TestDeleteRange_SingleKeyDeleteError(t *testing.T) {
	t.Parallel()

	mvcc := &mockMVCCKVServicer{
		deleteFn: func(_ context.Context, _ string) error {
			return errors.New("storage error")
		},
	}

	srv := newTestKVServer(mvcc, nil)
	_, err := srv.DeleteRange(context.Background(), &pb.DeleteRangeRequest{
		Key: []byte("fail"),
	})
	if err == nil {
		t.Fatal("DeleteRange() should return error on delete failure")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.Internal {
		t.Errorf("code = %v, want %v", st.Code(), codes.Internal)
	}
}

func TestDeleteRange_RangeScanError(t *testing.T) {
	t.Parallel()

	mvcc := &mockMVCCKVServicer{
		rangeFn: func(_ context.Context, _, _ []byte, _ ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
			return nil, 0, 0, errors.New("scan failed")
		},
	}

	srv := newTestKVServer(mvcc, nil)
	_, err := srv.DeleteRange(context.Background(), &pb.DeleteRangeRequest{
		Key:      []byte("a"),
		RangeEnd: []byte("z"),
	})
	if err == nil {
		t.Fatal("DeleteRange() should return error on range scan failure")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.Internal {
		t.Errorf("code = %v, want %v", st.Code(), codes.Internal)
	}
}

func TestDeleteRange_RangeDeletePerKeyError(t *testing.T) {
	t.Parallel()

	mvcc := &mockMVCCKVServicer{
		rangeFn: func(_ context.Context, _, _ []byte, _ ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
			return []*kv.KeyValue{
				{Key: []byte("a"), Value: []byte("1")},
			}, 1, 1, nil
		},
		deleteFn: func(_ context.Context, _ string) error {
			return errors.New("delete failed")
		},
		currentRevisionFn: func(_ context.Context) (int64, error) {
			return 1, nil
		},
	}

	srv := newTestKVServer(mvcc, nil)
	_, err := srv.DeleteRange(context.Background(), &pb.DeleteRangeRequest{
		Key:      []byte("a"),
		RangeEnd: []byte("z"),
	})
	if err == nil {
		t.Fatal("DeleteRange() should return error on per-key delete failure")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.Internal {
		t.Errorf("code = %v, want %v", st.Code(), codes.Internal)
	}
}

// ---------------------------------------------------------------------------
// Txn tests
// ---------------------------------------------------------------------------

func TestTxn_CompareSucceedsRunsSuccessOps(t *testing.T) {
	t.Parallel()

	mvcc := &mockMVCCKVServicer{
		getRevisionFn: func(_ context.Context, key string) (*kv.KeyValue, error) {
			if key == "counter" {
				return &kv.KeyValue{
					Key:            []byte("counter"),
					Value:          []byte("10"),
					CreateRevision: 1,
					ModRevision:    5,
					Version:        5,
				}, nil
			}
			return nil, errors.New("not found")
		},
		putWithLeaseFn: func(_ context.Context, _ string, _ []byte, _ int64) (*kv.KeyValue, error) {
			return &kv.KeyValue{
				Key:         []byte("counter"),
				Value:       []byte("11"),
				ModRevision: 6,
				Version:     6,
			}, nil
		},
		currentRevisionFn: func(_ context.Context) (int64, error) {
			return 6, nil
		},
	}

	txSvc := &mockTransactionServicer{}
	srv := newTestKVServer(mvcc, txSvc)

	resp, err := srv.Txn(context.Background(), &pb.TxnRequest{
		Compare: []*pb.Compare{
			{
				Key:    []byte("counter"),
				Target: pb.Compare_VERSION,
				Result: pb.Compare_EQUAL,
				TargetUnion: &pb.Compare_Version{
					Version: 5,
				},
			},
		},
		Success: []*pb.RequestOp{
			{Request: &pb.RequestOp_RequestPut{
				RequestPut: &pb.PutRequest{
					Key:   []byte("counter"),
					Value: []byte("11"),
				},
			}},
		},
		Failure: []*pb.RequestOp{
			{Request: &pb.RequestOp_RequestRange{
				RequestRange: &pb.RangeRequest{
					Key: []byte("counter"),
				},
			}},
		},
	})
	if err != nil {
		t.Fatalf("Txn() returned error: %v", err)
	}
	if !resp.Succeeded {
		t.Error("Succeeded = false, want true")
	}
	if len(resp.Responses) != 1 {
		t.Fatalf("len(Responses) = %d, want 1", len(resp.Responses))
	}
	putResp := resp.Responses[0].GetResponsePut()
	if putResp == nil {
		t.Fatal("expected ResponsePut, got nil")
	}
}

func TestTxn_CompareFailsRunsFailureOps(t *testing.T) {
	t.Parallel()

	mvcc := &mockMVCCKVServicer{
		getRevisionFn: func(_ context.Context, key string) (*kv.KeyValue, error) {
			return &kv.KeyValue{
				Key:            []byte(key),
				Value:          []byte("val"),
				CreateRevision: 1,
				ModRevision:    3,
				Version:        3,
			}, nil
		},
		currentRevisionFn: func(_ context.Context) (int64, error) {
			return 3, nil
		},
	}

	txSvc := &mockTransactionServicer{}
	srv := newTestKVServer(mvcc, txSvc)

	resp, err := srv.Txn(context.Background(), &pb.TxnRequest{
		Compare: []*pb.Compare{
			{
				Key:    []byte("key1"),
				Target: pb.Compare_VERSION,
				Result: pb.Compare_EQUAL,
				TargetUnion: &pb.Compare_Version{
					Version: 99, // Will not match version 3.
				},
			},
		},
		Success: []*pb.RequestOp{
			{Request: &pb.RequestOp_RequestPut{
				RequestPut: &pb.PutRequest{
					Key:   []byte("key1"),
					Value: []byte("should-not-run"),
				},
			}},
		},
		Failure: []*pb.RequestOp{
			{Request: &pb.RequestOp_RequestRange{
				RequestRange: &pb.RangeRequest{
					Key: []byte("key1"),
				},
			}},
		},
	})
	if err != nil {
		t.Fatalf("Txn() returned error: %v", err)
	}
	if resp.Succeeded {
		t.Error("Succeeded = true, want false")
	}
	if len(resp.Responses) != 1 {
		t.Fatalf("len(Responses) = %d, want 1", len(resp.Responses))
	}
	rangeResp := resp.Responses[0].GetResponseRange()
	if rangeResp == nil {
		t.Fatal("expected ResponseRange, got nil")
	}
}

func TestTxn_NilTxSvcReturnsUnimplemented(t *testing.T) {
	t.Parallel()

	srv := newTestKVServer(&mockMVCCKVServicer{}, nil)
	_, err := srv.Txn(context.Background(), &pb.TxnRequest{})
	if err == nil {
		t.Fatal("Txn() with nil txSvc should return error")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.Unimplemented {
		t.Errorf("code = %v, want %v", st.Code(), codes.Unimplemented)
	}
}

func TestTxn_CompareKeyNotFoundFailsComparison(t *testing.T) {
	t.Parallel()

	mvcc := &mockMVCCKVServicer{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return nil, errors.New("key not found")
		},
		currentRevisionFn: func(_ context.Context) (int64, error) {
			return 1, nil
		},
	}

	txSvc := &mockTransactionServicer{}
	srv := newTestKVServer(mvcc, txSvc)

	resp, err := srv.Txn(context.Background(), &pb.TxnRequest{
		Compare: []*pb.Compare{
			{
				Key:    []byte("missing"),
				Target: pb.Compare_VERSION,
				Result: pb.Compare_EQUAL,
				TargetUnion: &pb.Compare_Version{
					Version: 0,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Txn() returned error: %v", err)
	}
	if resp.Succeeded {
		t.Error("Succeeded = true, want false (key not found should fail comparison)")
	}
}

func TestTxn_NoComparesSucceeds(t *testing.T) {
	t.Parallel()

	mvcc := &mockMVCCKVServicer{
		currentRevisionFn: func(_ context.Context) (int64, error) {
			return 1, nil
		},
	}

	txSvc := &mockTransactionServicer{}
	srv := newTestKVServer(mvcc, txSvc)

	resp, err := srv.Txn(context.Background(), &pb.TxnRequest{
		Compare: nil,
		Success: nil,
		Failure: nil,
	})
	if err != nil {
		t.Fatalf("Txn() returned error: %v", err)
	}
	if !resp.Succeeded {
		t.Error("Succeeded = false, want true (no compares should succeed)")
	}
}

func TestTxn_ExecuteOpDeleteRange(t *testing.T) {
	t.Parallel()

	mvcc := &mockMVCCKVServicer{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{
				Key:     []byte("k"),
				Value:   []byte("v"),
				Version: 1,
			}, nil
		},
		deleteFn: func(_ context.Context, _ string) error {
			return nil
		},
		currentRevisionFn: func(_ context.Context) (int64, error) {
			return 5, nil
		},
	}

	txSvc := &mockTransactionServicer{}
	srv := newTestKVServer(mvcc, txSvc)

	resp, err := srv.Txn(context.Background(), &pb.TxnRequest{
		Compare: []*pb.Compare{
			{
				Key:    []byte("k"),
				Target: pb.Compare_VERSION,
				Result: pb.Compare_EQUAL,
				TargetUnion: &pb.Compare_Version{
					Version: 1,
				},
			},
		},
		Success: []*pb.RequestOp{
			{Request: &pb.RequestOp_RequestDeleteRange{
				RequestDeleteRange: &pb.DeleteRangeRequest{
					Key: []byte("k"),
				},
			}},
		},
	})
	if err != nil {
		t.Fatalf("Txn() returned error: %v", err)
	}
	if !resp.Succeeded {
		t.Error("Succeeded = false, want true")
	}
	if len(resp.Responses) != 1 {
		t.Fatalf("len(Responses) = %d, want 1", len(resp.Responses))
	}
	delResp := resp.Responses[0].GetResponseDeleteRange()
	if delResp == nil {
		t.Fatal("expected ResponseDeleteRange, got nil")
	}
}

func TestTxn_ExecuteOpUnknownType(t *testing.T) {
	t.Parallel()

	mvcc := &mockMVCCKVServicer{
		currentRevisionFn: func(_ context.Context) (int64, error) {
			return 1, nil
		},
	}

	txSvc := &mockTransactionServicer{}
	srv := newTestKVServer(mvcc, txSvc)

	resp, err := srv.Txn(context.Background(), &pb.TxnRequest{
		Compare: nil,
		Success: []*pb.RequestOp{
			{Request: nil}, // unknown/nil type
		},
	})
	if resp != nil {
		t.Errorf("response should be nil for unknown op type, got %v", resp)
	}
	if err == nil {
		t.Fatal("Txn() should return error for unknown op type")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.Unimplemented {
		t.Errorf("code = %v, want %v", st.Code(), codes.Unimplemented)
	}
}

// ---------------------------------------------------------------------------
// Compact tests
// ---------------------------------------------------------------------------

func TestCompact_Success(t *testing.T) {
	t.Parallel()

	var compactedRev int64
	mvcc := &mockMVCCKVServicer{
		compactFn: func(_ context.Context, revision int64) error {
			compactedRev = revision
			return nil
		},
	}

	srv := newTestKVServer(mvcc, nil)
	resp, err := srv.Compact(context.Background(), &pb.CompactionRequest{
		Revision: 42,
	})
	if err != nil {
		t.Fatalf("Compact() returned error: %v", err)
	}
	if compactedRev != 42 {
		t.Errorf("compacted revision = %d, want 42", compactedRev)
	}
	if resp.Header.Revision != 42 {
		t.Errorf("Header.Revision = %d, want 42", resp.Header.Revision)
	}
	if resp.Header.ClusterId != 100 {
		t.Errorf("ClusterId = %d, want 100", resp.Header.ClusterId)
	}
	if resp.Header.MemberId != 200 {
		t.Errorf("MemberId = %d, want 200", resp.Header.MemberId)
	}
}

func TestCompact_ErrorPropagation(t *testing.T) {
	t.Parallel()

	mvcc := &mockMVCCKVServicer{
		compactFn: func(_ context.Context, _ int64) error {
			return errors.New("compaction in progress")
		},
	}

	srv := newTestKVServer(mvcc, nil)
	_, err := srv.Compact(context.Background(), &pb.CompactionRequest{
		Revision: 10,
	})
	if err == nil {
		t.Fatal("Compact() should return error on service failure")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.Internal {
		t.Errorf("code = %v, want %v", st.Code(), codes.Internal)
	}
}

// TestCompact_AnswersWithEtcdsOwnRevisionErrors: kube-apiserver's compactor
// and watch cache read etcd's compacted and future-revision errors; the
// listener answers with exactly those, not an Internal status.
func TestCompact_AnswersWithEtcdsOwnRevisionErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want error
	}{
		{"already compacted", &service.RevisionCompactedError{Requested: 5, Compacted: 9}, rpctypes.ErrGRPCCompacted},
		{"future revision", &service.CompactRevisionFutureError{Requested: 50, Current: 9}, rpctypes.ErrGRPCFutureRev},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := newTestKVServer(&mockMVCCKVServicer{
				compactFn: func(context.Context, int64) error { return tt.err },
			}, nil)
			_, err := srv.Compact(context.Background(), &pb.CompactionRequest{Revision: 5})
			if !errors.Is(err, tt.want) {
				t.Fatalf("Compact = %v, want %v", err, tt.want)
			}
		})
	}
}

// TestRange_CompactedRevisionIsEtcdsCompactedError: a historical range below
// the database's floor answers rpctypes.ErrGRPCCompacted, the error an etcd
// client relists on.
func TestRange_CompactedRevisionIsEtcdsCompactedError(t *testing.T) {
	t.Parallel()

	srv := newTestKVServer(&mockMVCCKVServicer{
		rangeFn: func(context.Context, []byte, []byte, ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
			return nil, 0, 0, &service.RevisionCompactedError{Requested: 2, Compacted: 9}
		},
	}, nil)
	_, err := srv.Range(context.Background(), &pb.RangeRequest{Key: []byte("a"), RangeEnd: []byte("z"), Revision: 2})
	if !errors.Is(err, rpctypes.ErrGRPCCompacted) {
		t.Fatalf("Range = %v, want rpctypes.ErrGRPCCompacted", err)
	}
}

func TestCompact_ZeroRevision(t *testing.T) {
	t.Parallel()

	var capturedRev int64
	mvcc := &mockMVCCKVServicer{
		compactFn: func(_ context.Context, revision int64) error {
			capturedRev = revision
			return nil
		},
	}

	srv := newTestKVServer(mvcc, nil)
	_, err := srv.Compact(context.Background(), &pb.CompactionRequest{
		Revision: 0,
	})
	if err != nil {
		t.Fatalf("Compact() returned error: %v", err)
	}
	if capturedRev != 0 {
		t.Errorf("compacted revision = %d, want 0", capturedRev)
	}
}

// ---------------------------------------------------------------------------
// evaluateCompare tests
// ---------------------------------------------------------------------------

func TestEvaluateCompare_VersionEqual(t *testing.T) {
	t.Parallel()

	kval := &kv.KeyValue{Version: 5}
	cmp := &pb.Compare{
		Target:      pb.Compare_VERSION,
		Result:      pb.Compare_EQUAL,
		TargetUnion: &pb.Compare_Version{Version: 5},
	}
	if !evaluateCompare(cmp, kval) {
		t.Error("evaluateCompare() = false, want true (version 5 == 5)")
	}
}

func TestEvaluateCompare_VersionNotEqual(t *testing.T) {
	t.Parallel()

	kval := &kv.KeyValue{Version: 5}
	cmp := &pb.Compare{
		Target:      pb.Compare_VERSION,
		Result:      pb.Compare_EQUAL,
		TargetUnion: &pb.Compare_Version{Version: 3},
	}
	if evaluateCompare(cmp, kval) {
		t.Error("evaluateCompare() = true, want false (version 5 != 3)")
	}
}

func TestEvaluateCompare_VersionGreater(t *testing.T) {
	t.Parallel()

	kval := &kv.KeyValue{Version: 10}
	cmp := &pb.Compare{
		Target:      pb.Compare_VERSION,
		Result:      pb.Compare_GREATER,
		TargetUnion: &pb.Compare_Version{Version: 5},
	}
	if !evaluateCompare(cmp, kval) {
		t.Error("evaluateCompare() = false, want true (version 10 > 5)")
	}
}

func TestEvaluateCompare_VersionLess(t *testing.T) {
	t.Parallel()

	kval := &kv.KeyValue{Version: 2}
	cmp := &pb.Compare{
		Target:      pb.Compare_VERSION,
		Result:      pb.Compare_LESS,
		TargetUnion: &pb.Compare_Version{Version: 5},
	}
	if !evaluateCompare(cmp, kval) {
		t.Error("evaluateCompare() = false, want true (version 2 < 5)")
	}
}

func TestEvaluateCompare_VersionNotEqualResult(t *testing.T) {
	t.Parallel()

	kval := &kv.KeyValue{Version: 3}
	cmp := &pb.Compare{
		Target:      pb.Compare_VERSION,
		Result:      pb.Compare_NOT_EQUAL,
		TargetUnion: &pb.Compare_Version{Version: 5},
	}
	if !evaluateCompare(cmp, kval) {
		t.Error("evaluateCompare() = false, want true (version 3 != 5)")
	}
}

func TestEvaluateCompare_CreateRevision(t *testing.T) {
	t.Parallel()

	kval := &kv.KeyValue{CreateRevision: 7}
	cmp := &pb.Compare{
		Target:      pb.Compare_CREATE,
		Result:      pb.Compare_EQUAL,
		TargetUnion: &pb.Compare_CreateRevision{CreateRevision: 7},
	}
	if !evaluateCompare(cmp, kval) {
		t.Error("evaluateCompare() = false, want true (createRev 7 == 7)")
	}
}

func TestEvaluateCompare_CreateRevisionGreater(t *testing.T) {
	t.Parallel()

	kval := &kv.KeyValue{CreateRevision: 10}
	cmp := &pb.Compare{
		Target:      pb.Compare_CREATE,
		Result:      pb.Compare_GREATER,
		TargetUnion: &pb.Compare_CreateRevision{CreateRevision: 5},
	}
	if !evaluateCompare(cmp, kval) {
		t.Error("evaluateCompare() = false, want true (createRev 10 > 5)")
	}
}

func TestEvaluateCompare_ModRevision(t *testing.T) {
	t.Parallel()

	kval := &kv.KeyValue{ModRevision: 15}
	cmp := &pb.Compare{
		Target:      pb.Compare_MOD,
		Result:      pb.Compare_EQUAL,
		TargetUnion: &pb.Compare_ModRevision{ModRevision: 15},
	}
	if !evaluateCompare(cmp, kval) {
		t.Error("evaluateCompare() = false, want true (modRev 15 == 15)")
	}
}

func TestEvaluateCompare_ModRevisionLess(t *testing.T) {
	t.Parallel()

	kval := &kv.KeyValue{ModRevision: 3}
	cmp := &pb.Compare{
		Target:      pb.Compare_MOD,
		Result:      pb.Compare_LESS,
		TargetUnion: &pb.Compare_ModRevision{ModRevision: 10},
	}
	if !evaluateCompare(cmp, kval) {
		t.Error("evaluateCompare() = false, want true (modRev 3 < 10)")
	}
}

func TestEvaluateCompare_ValueEqual(t *testing.T) {
	t.Parallel()

	kval := &kv.KeyValue{Value: []byte("hello")}
	cmp := &pb.Compare{
		Target:      pb.Compare_VALUE,
		Result:      pb.Compare_EQUAL,
		TargetUnion: &pb.Compare_Value{Value: []byte("hello")},
	}
	if !evaluateCompare(cmp, kval) {
		t.Error("evaluateCompare() = false, want true (value equal)")
	}
}

func TestEvaluateCompare_ValueGreater(t *testing.T) {
	t.Parallel()

	kval := &kv.KeyValue{Value: []byte("b")}
	cmp := &pb.Compare{
		Target:      pb.Compare_VALUE,
		Result:      pb.Compare_GREATER,
		TargetUnion: &pb.Compare_Value{Value: []byte("a")},
	}
	if !evaluateCompare(cmp, kval) {
		t.Error("evaluateCompare() = false, want true (value 'b' > 'a')")
	}
}

func TestEvaluateCompare_ValueLess(t *testing.T) {
	t.Parallel()

	kval := &kv.KeyValue{Value: []byte("a")}
	cmp := &pb.Compare{
		Target:      pb.Compare_VALUE,
		Result:      pb.Compare_LESS,
		TargetUnion: &pb.Compare_Value{Value: []byte("b")},
	}
	if !evaluateCompare(cmp, kval) {
		t.Error("evaluateCompare() = false, want true (value 'a' < 'b')")
	}
}

func TestEvaluateCompare_ValueNotEqual(t *testing.T) {
	t.Parallel()

	kval := &kv.KeyValue{Value: []byte("hello")}
	cmp := &pb.Compare{
		Target:      pb.Compare_VALUE,
		Result:      pb.Compare_NOT_EQUAL,
		TargetUnion: &pb.Compare_Value{Value: []byte("world")},
	}
	if !evaluateCompare(cmp, kval) {
		t.Error("evaluateCompare() = false, want true (value 'hello' != 'world')")
	}
}

func TestEvaluateCompare_UnknownTarget(t *testing.T) {
	t.Parallel()

	kval := &kv.KeyValue{Version: 1}
	cmp := &pb.Compare{
		Target: pb.Compare_CompareTarget(99),
		Result: pb.Compare_EQUAL,
	}
	if evaluateCompare(cmp, kval) {
		t.Error("evaluateCompare() = true, want false (unknown target)")
	}
}

func TestEvaluateCompare_UnknownResult(t *testing.T) {
	t.Parallel()

	kval := &kv.KeyValue{Version: 1}
	cmp := &pb.Compare{
		Target:      pb.Compare_VERSION,
		Result:      pb.Compare_CompareResult(99),
		TargetUnion: &pb.Compare_Version{Version: 1},
	}
	if evaluateCompare(cmp, kval) {
		t.Error("evaluateCompare() = true, want false (unknown result)")
	}
}

// ---------------------------------------------------------------------------
// compareInt64 tests
// ---------------------------------------------------------------------------

func TestCompareInt64_Less(t *testing.T) {
	t.Parallel()

	result := compareInt64(3, 5)
	if result != -1 {
		t.Errorf("compareInt64(3, 5) = %d, want -1", result)
	}
}

func TestCompareInt64_Equal(t *testing.T) {
	t.Parallel()

	result := compareInt64(7, 7)
	if result != 0 {
		t.Errorf("compareInt64(7, 7) = %d, want 0", result)
	}
}

func TestCompareInt64_Greater(t *testing.T) {
	t.Parallel()

	result := compareInt64(10, 3)
	if result != 1 {
		t.Errorf("compareInt64(10, 3) = %d, want 1", result)
	}
}

func TestCompareInt64_NegativeValues(t *testing.T) {
	t.Parallel()

	result := compareInt64(-5, -3)
	if result != -1 {
		t.Errorf("compareInt64(-5, -3) = %d, want -1", result)
	}
}

func TestCompareInt64_ZeroEqual(t *testing.T) {
	t.Parallel()

	result := compareInt64(0, 0)
	if result != 0 {
		t.Errorf("compareInt64(0, 0) = %d, want 0", result)
	}
}

func TestCompareInt64_MixedSign(t *testing.T) {
	t.Parallel()

	result := compareInt64(-1, 1)
	if result != -1 {
		t.Errorf("compareInt64(-1, 1) = %d, want -1", result)
	}
}

// ---------------------------------------------------------------------------
// executeOp tests (exercised through Txn, plus direct edge cases)
// ---------------------------------------------------------------------------

func TestTxn_ExecuteOpRangeError(t *testing.T) {
	t.Parallel()

	mvcc := &mockMVCCKVServicer{
		currentRevisionFn: func(_ context.Context) (int64, error) {
			return 1, nil
		},
	}

	txSvc := &mockTransactionServicer{}
	srv := newTestKVServer(mvcc, txSvc)

	// Success op is a Range with empty key, which should trigger InvalidArgument.
	_, err := srv.Txn(context.Background(), &pb.TxnRequest{
		Compare: nil,
		Success: []*pb.RequestOp{
			{Request: &pb.RequestOp_RequestRange{
				RequestRange: &pb.RangeRequest{
					Key: nil, // empty key
				},
			}},
		},
	})
	if err == nil {
		t.Fatal("Txn() should propagate op error")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Errorf("code = %v, want %v", st.Code(), codes.InvalidArgument)
	}
}

func TestTxn_ExecuteOpPutError(t *testing.T) {
	t.Parallel()

	mvcc := &mockMVCCKVServicer{
		putWithLeaseFn: func(_ context.Context, _ string, _ []byte, _ int64) (*kv.KeyValue, error) {
			return nil, errors.New("put failed")
		},
		currentRevisionFn: func(_ context.Context) (int64, error) {
			return 1, nil
		},
	}

	txSvc := &mockTransactionServicer{}
	srv := newTestKVServer(mvcc, txSvc)

	_, err := srv.Txn(context.Background(), &pb.TxnRequest{
		Compare: nil,
		Success: []*pb.RequestOp{
			{Request: &pb.RequestOp_RequestPut{
				RequestPut: &pb.PutRequest{
					Key:   []byte("key"),
					Value: []byte("val"),
				},
			}},
		},
	})
	if err == nil {
		t.Fatal("Txn() should propagate put error")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.Internal {
		t.Errorf("code = %v, want %v", st.Code(), codes.Internal)
	}
}

func TestTxn_ExecuteOpDeleteRangeError(t *testing.T) {
	t.Parallel()

	mvcc := &mockMVCCKVServicer{
		deleteFn: func(_ context.Context, _ string) error {
			return errors.New("delete failed")
		},
		currentRevisionFn: func(_ context.Context) (int64, error) {
			return 1, nil
		},
	}

	txSvc := &mockTransactionServicer{}
	srv := newTestKVServer(mvcc, txSvc)

	_, err := srv.Txn(context.Background(), &pb.TxnRequest{
		Compare: nil,
		Success: []*pb.RequestOp{
			{Request: &pb.RequestOp_RequestDeleteRange{
				RequestDeleteRange: &pb.DeleteRangeRequest{
					Key: []byte("key"),
				},
			}},
		},
	})
	if err == nil {
		t.Fatal("Txn() should propagate delete error")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.Internal {
		t.Errorf("code = %v, want %v", st.Code(), codes.Internal)
	}
}

func TestTxn_MultipleCompareSecondFails(t *testing.T) {
	t.Parallel()

	callCount := 0
	mvcc := &mockMVCCKVServicer{
		getRevisionFn: func(_ context.Context, key string) (*kv.KeyValue, error) {
			callCount++
			if key == "k1" {
				return &kv.KeyValue{
					Key:     []byte("k1"),
					Version: 5,
				}, nil
			}
			return &kv.KeyValue{
				Key:     []byte("k2"),
				Version: 3, // Does not match expected 10.
			}, nil
		},
		currentRevisionFn: func(_ context.Context) (int64, error) {
			return 1, nil
		},
	}

	txSvc := &mockTransactionServicer{}
	srv := newTestKVServer(mvcc, txSvc)

	resp, err := srv.Txn(context.Background(), &pb.TxnRequest{
		Compare: []*pb.Compare{
			{
				Key:         []byte("k1"),
				Target:      pb.Compare_VERSION,
				Result:      pb.Compare_EQUAL,
				TargetUnion: &pb.Compare_Version{Version: 5},
			},
			{
				Key:         []byte("k2"),
				Target:      pb.Compare_VERSION,
				Result:      pb.Compare_EQUAL,
				TargetUnion: &pb.Compare_Version{Version: 10},
			},
		},
	})
	if err != nil {
		t.Fatalf("Txn() returned error: %v", err)
	}
	if resp.Succeeded {
		t.Error("Succeeded = true, want false (second compare should fail)")
	}
}

// RestoreToRevision satisfies service.MVCCKVServicer.
func (m *mockMVCCKVServicer) RestoreToRevision(context.Context, *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
	return nil, nil
}
