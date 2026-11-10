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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package quic

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// mockLockService implements service.LockServicer for QUIC tests.
// ---------------------------------------------------------------------------

type mockLockService struct {
	lockFn    func(ctx context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error)
	tryLockFn func(ctx context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error)
	unlockFn  func(ctx context.Context, key string, fencingToken int64) error
	extendFn  func(ctx context.Context, key string, fencingToken int64) (*lock.Lock, error)
	statusFn  func(ctx context.Context, key string) (*lock.Lock, error)
	listFn    func(ctx context.Context, prefix string) ([]*lock.Lock, error)
}

var _ service.LockServicer = (*mockLockService)(nil)

func newMockLockService() *mockLockService { return &mockLockService{} }

func (m *mockLockService) Lock(ctx context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error) {
	if m.lockFn != nil {
		return m.lockFn(ctx, key, holderID, ttl, reentrant)
	}
	return &lock.Lock{Key: key, HolderID: holderID, FencingToken: 42, TTL: int64(ttl.Seconds())}, nil
}

func (m *mockLockService) TryLock(ctx context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error) {
	if m.tryLockFn != nil {
		return m.tryLockFn(ctx, key, holderID, ttl, reentrant)
	}
	return &lock.Lock{Key: key, HolderID: holderID, FencingToken: 99}, nil
}

func (m *mockLockService) Unlock(ctx context.Context, key string, fencingToken int64) error {
	if m.unlockFn != nil {
		return m.unlockFn(ctx, key, fencingToken)
	}
	return nil
}

func (m *mockLockService) Extend(ctx context.Context, key string, fencingToken int64) (*lock.Lock, error) {
	if m.extendFn != nil {
		return m.extendFn(ctx, key, fencingToken)
	}
	return &lock.Lock{Key: key, FencingToken: fencingToken}, nil
}

func (m *mockLockService) Status(ctx context.Context, key string) (*lock.Lock, error) {
	if m.statusFn != nil {
		return m.statusFn(ctx, key)
	}
	return &lock.Lock{Key: key, HolderID: "holder-1", FencingToken: 7}, nil
}

func (m *mockLockService) List(ctx context.Context, prefix string) ([]*lock.Lock, error) {
	if m.listFn != nil {
		return m.listFn(ctx, prefix)
	}
	return []*lock.Lock{
		{Key: prefix + "a", HolderID: "h1", FencingToken: 1},
		{Key: prefix + "b", HolderID: "h2", FencingToken: 2},
	}, nil
}

// ---------------------------------------------------------------------------
// lockToProto
// ---------------------------------------------------------------------------

func TestLockToProto_Nil(t *testing.T) {
	if got := lockToProto(nil); got != nil {
		t.Errorf("expected nil for nil input, got %v", got)
	}
}

func TestLockToProto_Fields(t *testing.T) {
	l := &lock.Lock{
		Key:          "mykey",
		FencingToken: 42,
		LeaseID:      7,
		HolderID:     "node-1",
		Reentrant:    true,
		AcquireCount: 3,
		TTL:          30,
		AcquiredAt:   1234567890,
	}
	got := lockToProto(l)
	if got == nil {
		t.Fatal("expected non-nil LockInfo")
	}
	if got.GetKey() != "mykey" {
		t.Errorf("Key: expected 'mykey', got %q", got.GetKey())
	}
	if got.GetFencingToken() != 42 {
		t.Errorf("FencingToken: expected 42, got %d", got.GetFencingToken())
	}
	if got.GetHolderId() != "node-1" {
		t.Errorf("HolderID: expected 'node-1', got %q", got.GetHolderId())
	}
	if !got.GetReentrant() {
		t.Error("expected Reentrant=true")
	}
	if got.GetAcquireCount() != 3 {
		t.Errorf("AcquireCount: expected 3, got %d", got.GetAcquireCount())
	}
}

// ---------------------------------------------------------------------------
// handleLockAcquire
// ---------------------------------------------------------------------------

func TestHandleLockAcquire_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.LockAcquireRequest{Key: "k", HolderId: "h", Ttl: 5})

	_, err := srv.handleLockAcquire(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleLockAcquire_Success(t *testing.T) {
	lockSvc := newMockLockService()
	lockSvc.lockFn = func(_ context.Context, key, holderID string, ttl time.Duration, _ bool) (*lock.Lock, error) {
		return &lock.Lock{Key: key, HolderID: holderID, FencingToken: 55, TTL: int64(ttl.Seconds())}, nil
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLock(lockSvc))

	req := &pb.LockAcquireRequest{Key: "resource/db", HolderId: "worker-1", Ttl: 30}
	payload, _ := proto.Marshal(req)

	resp, err := srv.handleLockAcquire(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var acquireResp pb.LockAcquireResponse
	if err := proto.Unmarshal(resp, &acquireResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if acquireResp.GetLock().GetKey() != "resource/db" {
		t.Errorf("expected key 'resource/db', got %q", acquireResp.GetLock().GetKey())
	}
	if acquireResp.GetLock().GetFencingToken() != 55 {
		t.Errorf("expected fencing_token 55, got %d", acquireResp.GetLock().GetFencingToken())
	}
}

func TestHandleLockAcquire_ServiceError(t *testing.T) {
	lockSvc := newMockLockService()
	lockSvc.lockFn = func(_ context.Context, _ string, _ string, _ time.Duration, _ bool) (*lock.Lock, error) {
		return nil, errors.New("lock error")
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLock(lockSvc))
	payload, _ := proto.Marshal(&pb.LockAcquireRequest{Key: "k", HolderId: "h", Ttl: 5})

	_, err := srv.handleLockAcquire(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleLockAcquire_UnmarshalError(t *testing.T) {
	lockSvc := newMockLockService()
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLock(lockSvc))

	_, err := srv.handleLockAcquire(context.Background(), []byte{0xff, 0xfe, 0xfd})
	if err == nil {
		t.Fatal("expected error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleLockTryLock
// ---------------------------------------------------------------------------

func TestHandleLockTryLock_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.LockTryLockRequest{Key: "k", HolderId: "h", Ttl: 5})

	_, err := srv.handleLockTryLock(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleLockTryLock_Success(t *testing.T) {
	lockSvc := newMockLockService()
	lockSvc.tryLockFn = func(_ context.Context, key, holderID string, _ time.Duration, _ bool) (*lock.Lock, error) {
		return &lock.Lock{Key: key, HolderID: holderID, FencingToken: 77}, nil
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLock(lockSvc))
	payload, _ := proto.Marshal(&pb.LockTryLockRequest{Key: "k2", HolderId: "w2", Ttl: 10})

	resp, err := srv.handleLockTryLock(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var tryResp pb.LockTryLockResponse
	if err := proto.Unmarshal(resp, &tryResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if tryResp.GetLock().GetFencingToken() != 77 {
		t.Errorf("expected fencing_token 77, got %d", tryResp.GetLock().GetFencingToken())
	}
}

func TestHandleLockTryLock_ServiceError(t *testing.T) {
	lockSvc := newMockLockService()
	lockSvc.tryLockFn = func(_ context.Context, _ string, _ string, _ time.Duration, _ bool) (*lock.Lock, error) {
		return nil, errors.New("lock held")
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLock(lockSvc))
	payload, _ := proto.Marshal(&pb.LockTryLockRequest{Key: "k", HolderId: "h", Ttl: 5})

	_, err := srv.handleLockTryLock(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleLockTryLock_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLock(&mockLockSvc{}))
	_, err := srv.handleLockTryLock(context.Background(), []byte{0xff})
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleLockRelease
// ---------------------------------------------------------------------------

func TestHandleLockRelease_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.LockReleaseRequest{Key: "k", FencingToken: 1})

	_, err := srv.handleLockRelease(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleLockRelease_Success(t *testing.T) {
	called := false
	lockSvc := newMockLockService()
	lockSvc.unlockFn = func(_ context.Context, key string, fencingToken int64) error {
		called = true
		if key != "mykey" || fencingToken != 42 {
			return errors.New("wrong args")
		}
		return nil
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLock(lockSvc))
	payload, _ := proto.Marshal(&pb.LockReleaseRequest{Key: "mykey", FencingToken: 42})

	result, err := srv.handleLockRelease(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("expected Unlock to be called")
	}
	if len(result) != 0 {
		t.Errorf("expected empty response, got %v", result)
	}
}

func TestHandleLockRelease_ServiceError(t *testing.T) {
	lockSvc := newMockLockService()
	lockSvc.unlockFn = func(_ context.Context, _ string, _ int64) error {
		return errors.New("stale fencing token")
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLock(lockSvc))
	payload, _ := proto.Marshal(&pb.LockReleaseRequest{Key: "k", FencingToken: 1})

	_, err := srv.handleLockRelease(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// handleLockExtend
// ---------------------------------------------------------------------------

func TestHandleLockExtend_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.LockExtendRequest{Key: "k", FencingToken: 1})

	_, err := srv.handleLockExtend(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleLockExtend_Success(t *testing.T) {
	lockSvc := newMockLockService()
	lockSvc.extendFn = func(_ context.Context, key string, fencingToken int64) (*lock.Lock, error) {
		return &lock.Lock{Key: key, FencingToken: fencingToken, HolderID: "holder"}, nil
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLock(lockSvc))
	payload, _ := proto.Marshal(&pb.LockExtendRequest{Key: "resource", FencingToken: 33})

	resp, err := srv.handleLockExtend(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var extResp pb.LockExtendResponse
	if err := proto.Unmarshal(resp, &extResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if extResp.GetLock().GetFencingToken() != 33 {
		t.Errorf("expected fencing_token 33, got %d", extResp.GetLock().GetFencingToken())
	}
}

func TestHandleLockExtend_ServiceError(t *testing.T) {
	lockSvc := newMockLockService()
	lockSvc.extendFn = func(_ context.Context, _ string, _ int64) (*lock.Lock, error) {
		return nil, errors.New("lock expired")
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLock(lockSvc))
	payload, _ := proto.Marshal(&pb.LockExtendRequest{Key: "k", FencingToken: 1})

	_, err := srv.handleLockExtend(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// handleLockStatus
// ---------------------------------------------------------------------------

func TestHandleLockStatus_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.LockStatusRequest{Key: "k"})

	_, err := srv.handleLockStatus(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleLockStatus_Success(t *testing.T) {
	lockSvc := newMockLockService()
	lockSvc.statusFn = func(_ context.Context, key string) (*lock.Lock, error) {
		return &lock.Lock{Key: key, HolderID: "node-3", FencingToken: 11}, nil
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLock(lockSvc))
	payload, _ := proto.Marshal(&pb.LockStatusRequest{Key: "res"})

	resp, err := srv.handleLockStatus(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var statusResp pb.LockStatusResponse
	if err := proto.Unmarshal(resp, &statusResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if statusResp.GetLock().GetHolderId() != "node-3" {
		t.Errorf("expected holder 'node-3', got %q", statusResp.GetLock().GetHolderId())
	}
}

func TestHandleLockStatus_ServiceError(t *testing.T) {
	lockSvc := newMockLockService()
	lockSvc.statusFn = func(_ context.Context, _ string) (*lock.Lock, error) {
		return nil, errors.New("not found")
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLock(lockSvc))
	payload, _ := proto.Marshal(&pb.LockStatusRequest{Key: "k"})

	_, err := srv.handleLockStatus(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// handleLockList
// ---------------------------------------------------------------------------

func TestHandleLockList_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.LockListRequest{})

	_, err := srv.handleLockList(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleLockList_Success(t *testing.T) {
	lockSvc := newMockLockService()
	lockSvc.listFn = func(_ context.Context, prefix string) ([]*lock.Lock, error) {
		return []*lock.Lock{
			{Key: prefix + "x", HolderID: "h1", FencingToken: 1},
			{Key: prefix + "y", HolderID: "h2", FencingToken: 2},
		}, nil
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLock(lockSvc))
	payload, _ := proto.Marshal(&pb.LockListRequest{Prefix: "res/"})

	resp, err := srv.handleLockList(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var listResp pb.LockListResponse
	if err := proto.Unmarshal(resp, &listResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(listResp.GetLocks()) != 2 {
		t.Fatalf("expected 2 locks, got %d", len(listResp.GetLocks()))
	}
}

func TestHandleLockList_ServiceError(t *testing.T) {
	lockSvc := newMockLockService()
	lockSvc.listFn = func(_ context.Context, _ string) ([]*lock.Lock, error) {
		return nil, errors.New("store error")
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLock(lockSvc))
	payload, _ := proto.Marshal(&pb.LockListRequest{})

	_, err := srv.handleLockList(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleLockList_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLock(&mockLockSvc{}))
	_, err := srv.handleLockList(context.Background(), []byte{0xff, 0xfe})
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// WithLock option
// ---------------------------------------------------------------------------

func TestWithLock_SetsField(t *testing.T) {
	lockSvc := newMockLockService()
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLock(lockSvc))
	if srv.lockSvc == nil {
		t.Error("expected lockSvc to be set")
	}
}
