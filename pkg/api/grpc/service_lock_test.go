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

package grpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// mockLockService implements service.LockServicer for testing.
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
	if got.GetLeaseId() != 7 {
		t.Errorf("LeaseID: expected 7, got %d", got.GetLeaseId())
	}
	if got.GetHolderId() != "node-1" {
		t.Errorf("HolderID: expected 'node-1', got %q", got.GetHolderId())
	}
	if !got.GetReentrant() {
		t.Error("Reentrant: expected true")
	}
	if got.GetAcquireCount() != 3 {
		t.Errorf("AcquireCount: expected 3, got %d", got.GetAcquireCount())
	}
	if got.GetTtl() != 30 {
		t.Errorf("TTL: expected 30, got %d", got.GetTtl())
	}
	if got.GetAcquiredAt() != 1234567890 {
		t.Errorf("AcquiredAt: expected 1234567890, got %d", got.GetAcquiredAt())
	}
}

// ---------------------------------------------------------------------------
// LockAcquire
// ---------------------------------------------------------------------------

func TestGRPC_LockAcquire_NilService(t *testing.T) {
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_, err := client.LockAcquire(context.Background(), &pb.LockAcquireRequest{
		Key:      "mykey",
		HolderId: "node-1",
		Ttl:      10,
	})
	if err == nil {
		t.Fatal("expected error for nil lock service")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_LockAcquire_Success(t *testing.T) {
	lockSvc := newMockLockService()
	lockSvc.lockFn = func(_ context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error) {
		return &lock.Lock{Key: key, HolderID: holderID, FencingToken: 55, TTL: int64(ttl.Seconds())}, nil
	}
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithLock(lockSvc))
	defer cleanup()

	resp, err := client.LockAcquire(context.Background(), &pb.LockAcquireRequest{
		Key:       "resource/db",
		HolderId:  "worker-1",
		Ttl:       30,
		Reentrant: false,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetLock() == nil {
		t.Fatal("expected non-nil lock in response")
	}
	if resp.GetLock().GetKey() != "resource/db" {
		t.Errorf("expected key 'resource/db', got %q", resp.GetLock().GetKey())
	}
	if resp.GetLock().GetFencingToken() != 55 {
		t.Errorf("expected fencing_token 55, got %d", resp.GetLock().GetFencingToken())
	}
}

func TestGRPC_LockAcquire_ServiceError(t *testing.T) {
	lockSvc := newMockLockService()
	lockSvc.lockFn = func(_ context.Context, _ string, _ string, _ time.Duration, _ bool) (*lock.Lock, error) {
		return nil, errors.New("lock store error")
	}
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithLock(lockSvc))
	defer cleanup()

	_, err := client.LockAcquire(context.Background(), &pb.LockAcquireRequest{Key: "k", HolderId: "h", Ttl: 5})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// LockTryLock
// ---------------------------------------------------------------------------

func TestGRPC_LockTryLock_NilService(t *testing.T) {
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_, err := client.LockTryLock(context.Background(), &pb.LockTryLockRequest{Key: "k", HolderId: "h", Ttl: 5})
	if err == nil {
		t.Fatal("expected error for nil lock service")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_LockTryLock_Success(t *testing.T) {
	lockSvc := newMockLockService()
	lockSvc.tryLockFn = func(_ context.Context, key, holderID string, _ time.Duration, _ bool) (*lock.Lock, error) {
		return &lock.Lock{Key: key, HolderID: holderID, FencingToken: 77}, nil
	}
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithLock(lockSvc))
	defer cleanup()

	resp, err := client.LockTryLock(context.Background(), &pb.LockTryLockRequest{Key: "k2", HolderId: "w2", Ttl: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetLock().GetFencingToken() != 77 {
		t.Errorf("expected fencing_token 77, got %d", resp.GetLock().GetFencingToken())
	}
}

func TestGRPC_LockTryLock_ServiceError(t *testing.T) {
	lockSvc := newMockLockService()
	lockSvc.tryLockFn = func(_ context.Context, _ string, _ string, _ time.Duration, _ bool) (*lock.Lock, error) {
		return nil, errors.New("lock held")
	}
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithLock(lockSvc))
	defer cleanup()

	_, err := client.LockTryLock(context.Background(), &pb.LockTryLockRequest{Key: "k", HolderId: "h", Ttl: 5})
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// LockRelease
// ---------------------------------------------------------------------------

func TestGRPC_LockRelease_NilService(t *testing.T) {
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_, err := client.LockRelease(context.Background(), &pb.LockReleaseRequest{Key: "k", FencingToken: 1})
	if err == nil {
		t.Fatal("expected error for nil lock service")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_LockRelease_Success(t *testing.T) {
	called := false
	lockSvc := newMockLockService()
	lockSvc.unlockFn = func(_ context.Context, key string, fencingToken int64) error {
		called = true
		if key != "mykey" || fencingToken != 42 {
			return errors.New("wrong args")
		}
		return nil
	}
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithLock(lockSvc))
	defer cleanup()

	_, err := client.LockRelease(context.Background(), &pb.LockReleaseRequest{Key: "mykey", FencingToken: 42})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("expected Unlock to be called")
	}
}

func TestGRPC_LockRelease_ServiceError(t *testing.T) {
	lockSvc := newMockLockService()
	lockSvc.unlockFn = func(_ context.Context, _ string, _ int64) error {
		return errors.New("stale fencing token")
	}
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithLock(lockSvc))
	defer cleanup()

	_, err := client.LockRelease(context.Background(), &pb.LockReleaseRequest{Key: "k", FencingToken: 1})
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// LockExtend
// ---------------------------------------------------------------------------

func TestGRPC_LockExtend_NilService(t *testing.T) {
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_, err := client.LockExtend(context.Background(), &pb.LockExtendRequest{Key: "k", FencingToken: 1})
	if err == nil {
		t.Fatal("expected error for nil lock service")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_LockExtend_Success(t *testing.T) {
	lockSvc := newMockLockService()
	lockSvc.extendFn = func(_ context.Context, key string, fencingToken int64) (*lock.Lock, error) {
		return &lock.Lock{Key: key, FencingToken: fencingToken, HolderID: "holder"}, nil
	}
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithLock(lockSvc))
	defer cleanup()

	resp, err := client.LockExtend(context.Background(), &pb.LockExtendRequest{Key: "resource", FencingToken: 33})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetLock().GetFencingToken() != 33 {
		t.Errorf("expected fencing_token 33, got %d", resp.GetLock().GetFencingToken())
	}
}

func TestGRPC_LockExtend_ServiceError(t *testing.T) {
	lockSvc := newMockLockService()
	lockSvc.extendFn = func(_ context.Context, _ string, _ int64) (*lock.Lock, error) {
		return nil, errors.New("lock expired")
	}
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithLock(lockSvc))
	defer cleanup()

	_, err := client.LockExtend(context.Background(), &pb.LockExtendRequest{Key: "k", FencingToken: 1})
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// LockStatus
// ---------------------------------------------------------------------------

func TestGRPC_LockStatus_NilService(t *testing.T) {
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_, err := client.LockStatus(context.Background(), &pb.LockStatusRequest{Key: "k"})
	if err == nil {
		t.Fatal("expected error for nil lock service")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_LockStatus_Success(t *testing.T) {
	lockSvc := newMockLockService()
	lockSvc.statusFn = func(_ context.Context, key string) (*lock.Lock, error) {
		return &lock.Lock{Key: key, HolderID: "node-3", FencingToken: 11}, nil
	}
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithLock(lockSvc))
	defer cleanup()

	resp, err := client.LockStatus(context.Background(), &pb.LockStatusRequest{Key: "res"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetLock().GetKey() != "res" {
		t.Errorf("expected key 'res', got %q", resp.GetLock().GetKey())
	}
	if resp.GetLock().GetHolderId() != "node-3" {
		t.Errorf("expected holder 'node-3', got %q", resp.GetLock().GetHolderId())
	}
}

func TestGRPC_LockStatus_ServiceError(t *testing.T) {
	lockSvc := newMockLockService()
	lockSvc.statusFn = func(_ context.Context, _ string) (*lock.Lock, error) {
		return nil, errors.New("not found")
	}
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithLock(lockSvc))
	defer cleanup()

	_, err := client.LockStatus(context.Background(), &pb.LockStatusRequest{Key: "k"})
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// LockList
// ---------------------------------------------------------------------------

func TestGRPC_LockList_NilService(t *testing.T) {
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_, err := client.LockList(context.Background(), &pb.LockListRequest{})
	if err == nil {
		t.Fatal("expected error for nil lock service")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_LockList_Success(t *testing.T) {
	lockSvc := newMockLockService()
	lockSvc.listFn = func(_ context.Context, prefix string) ([]*lock.Lock, error) {
		return []*lock.Lock{
			{Key: prefix + "x", HolderID: "h1", FencingToken: 1},
			{Key: prefix + "y", HolderID: "h2", FencingToken: 2},
		}, nil
	}
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithLock(lockSvc))
	defer cleanup()

	resp, err := client.LockList(context.Background(), &pb.LockListRequest{Prefix: "res/"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetLocks()) != 2 {
		t.Fatalf("expected 2 locks, got %d", len(resp.GetLocks()))
	}
}

func TestGRPC_LockList_ServiceError(t *testing.T) {
	lockSvc := newMockLockService()
	lockSvc.listFn = func(_ context.Context, _ string) ([]*lock.Lock, error) {
		return nil, errors.New("store error")
	}
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithLock(lockSvc))
	defer cleanup()

	_, err := client.LockList(context.Background(), &pb.LockListRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// register.go option coverage: WithRegisterLockService, WithRegisterTenantService
// ---------------------------------------------------------------------------

func TestRegisterService_WithLockService(t *testing.T) {
	rawSrv := grpc.NewServer()
	defer rawSrv.Stop()
	lockSvc := newMockLockService()
	RegisterService(rawSrv, newMockKVService(), newMockAdminService(),
		WithRegisterLockService(lockSvc))
}

func TestRegisterService_WithTenantService(t *testing.T) {
	rawSrv := grpc.NewServer()
	defer rawSrv.Stop()
	tenantSvc := newMockTenantService()
	RegisterService(rawSrv, newMockKVService(), newMockAdminService(),
		WithRegisterTenantService(tenantSvc))
}

// ---------------------------------------------------------------------------
// server.go option coverage: WithLock, WithAuthFactorService, WithKVService, WithAdminService
// ---------------------------------------------------------------------------

func TestServerOption_WithLock(t *testing.T) {
	lockSvc := newMockLockService()
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
		WithLock(lockSvc))
	if srv.lockSvc == nil {
		t.Error("expected lockSvc to be set")
	}
}

func TestServerOption_WithAuthFactorService(t *testing.T) {
	authSvc := newMockAuthFactorService()
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
		WithAuthFactorService(authSvc))
	if srv.authFactorSvc == nil {
		t.Error("expected authFactorSvc to be set")
	}
}

func TestServerOption_WithKVService(t *testing.T) {
	kvSvc := newMockKVService()
	srv := NewServer(nil, newMockAdminService(), "127.0.0.1:0",
		WithKVService(kvSvc))
	if srv.kvSvc != kvSvc {
		t.Error("expected kvSvc to be set via option")
	}
}

func TestServerOption_WithAdminService(t *testing.T) {
	adminSvc := newMockAdminService()
	srv := NewServer(newMockKVService(), nil, "127.0.0.1:0",
		WithAdminService(adminSvc))
	if srv.adminSvc != adminSvc {
		t.Error("expected adminSvc to be set via option")
	}
}
