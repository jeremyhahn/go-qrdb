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

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// mockLockService implements service.LockServicer for MCP tests.
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
	}, nil
}

// ---------------------------------------------------------------------------
// handleLockAcquire
// ---------------------------------------------------------------------------

func TestHandleLockAcquire_MissingKey(t *testing.T) {
	svc := newMockLockService()
	handler := handleLockAcquire(svc)

	req := buildCallToolRequest(t, "qrdb_lock_acquire", map[string]any{
		"holder_id": "worker-1",
		"ttl":       float64(10),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing key")
	}
}

func TestHandleLockAcquire_MissingHolderID(t *testing.T) {
	svc := newMockLockService()
	handler := handleLockAcquire(svc)

	req := buildCallToolRequest(t, "qrdb_lock_acquire", map[string]any{
		"key": "mykey",
		"ttl": float64(10),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing holder_id")
	}
}

func TestHandleLockAcquire_MissingTTL(t *testing.T) {
	svc := newMockLockService()
	handler := handleLockAcquire(svc)

	req := buildCallToolRequest(t, "qrdb_lock_acquire", map[string]any{
		"key":       "mykey",
		"holder_id": "worker-1",
		"ttl":       float64(0),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for ttl=0")
	}
}

func TestHandleLockAcquire_Success(t *testing.T) {
	svc := newMockLockService()
	svc.lockFn = func(_ context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error) {
		return &lock.Lock{Key: key, HolderID: holderID, FencingToken: 55, TTL: int64(ttl.Seconds()), Reentrant: reentrant}, nil
	}
	handler := handleLockAcquire(svc)

	req := buildCallToolRequest(t, "qrdb_lock_acquire", map[string]any{
		"key":       "resource/db",
		"holder_id": "worker-1",
		"ttl":       float64(30),
		"reentrant": true,
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected IsError: %v", result.Content)
	}
	text := resultText(t, result)
	var l lock.Lock
	if err := json.Unmarshal([]byte(text), &l); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if l.Key != "resource/db" {
		t.Errorf("expected key 'resource/db', got %q", l.Key)
	}
	if l.FencingToken != 55 {
		t.Errorf("expected fencing_token 55, got %d", l.FencingToken)
	}
}

func TestHandleLockAcquire_ServiceError(t *testing.T) {
	svc := newMockLockService()
	svc.lockFn = func(_ context.Context, _, _ string, _ time.Duration, _ bool) (*lock.Lock, error) {
		return nil, errors.New("lock error")
	}
	handler := handleLockAcquire(svc)

	req := buildCallToolRequest(t, "qrdb_lock_acquire", map[string]any{
		"key":       "k",
		"holder_id": "h",
		"ttl":       float64(5),
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected ToolError")
	}
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("expected *ToolError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleLockTryLock
// ---------------------------------------------------------------------------

func TestHandleLockTryLock_MissingKey(t *testing.T) {
	svc := newMockLockService()
	handler := handleLockTryLock(svc)

	req := buildCallToolRequest(t, "qrdb_lock_trylock", map[string]any{
		"holder_id": "h",
		"ttl":       float64(5),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing key")
	}
}

func TestHandleLockTryLock_MissingHolderID(t *testing.T) {
	svc := newMockLockService()
	handler := handleLockTryLock(svc)

	req := buildCallToolRequest(t, "qrdb_lock_trylock", map[string]any{
		"key": "k",
		"ttl": float64(5),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing holder_id")
	}
}

func TestHandleLockTryLock_ZeroTTL(t *testing.T) {
	svc := newMockLockService()
	handler := handleLockTryLock(svc)

	req := buildCallToolRequest(t, "qrdb_lock_trylock", map[string]any{
		"key":       "k",
		"holder_id": "h",
		"ttl":       float64(0),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for zero ttl")
	}
}

func TestHandleLockExtend_MissingFencingToken(t *testing.T) {
	svc := newMockLockService()
	handler := handleLockExtend(svc)

	req := buildCallToolRequest(t, "qrdb_lock_extend", map[string]any{
		"key": "k",
		"ttl": float64(5),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing fencing_token")
	}
}

func TestHandleLockTryLock_Success(t *testing.T) {
	svc := newMockLockService()
	svc.tryLockFn = func(_ context.Context, key, holderID string, _ time.Duration, _ bool) (*lock.Lock, error) {
		return &lock.Lock{Key: key, HolderID: holderID, FencingToken: 77}, nil
	}
	handler := handleLockTryLock(svc)

	req := buildCallToolRequest(t, "qrdb_lock_trylock", map[string]any{
		"key":       "k2",
		"holder_id": "w2",
		"ttl":       float64(10),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected IsError: %v", result.Content)
	}
	text := resultText(t, result)
	var l lock.Lock
	if err := json.Unmarshal([]byte(text), &l); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if l.FencingToken != 77 {
		t.Errorf("expected fencing_token 77, got %d", l.FencingToken)
	}
}

func TestHandleLockTryLock_ServiceError(t *testing.T) {
	svc := newMockLockService()
	svc.tryLockFn = func(_ context.Context, _, _ string, _ time.Duration, _ bool) (*lock.Lock, error) {
		return nil, errors.New("lock held")
	}
	handler := handleLockTryLock(svc)

	req := buildCallToolRequest(t, "qrdb_lock_trylock", map[string]any{
		"key":       "k",
		"holder_id": "h",
		"ttl":       float64(5),
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected ToolError")
	}
}

// ---------------------------------------------------------------------------
// handleLockRelease
// ---------------------------------------------------------------------------

func TestHandleLockRelease_MissingKey(t *testing.T) {
	svc := newMockLockService()
	handler := handleLockRelease(svc)

	req := buildCallToolRequest(t, "qrdb_lock_release", map[string]any{
		"fencing_token": float64(42),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing key")
	}
}

func TestHandleLockRelease_MissingFencingToken(t *testing.T) {
	svc := newMockLockService()
	handler := handleLockRelease(svc)

	req := buildCallToolRequest(t, "qrdb_lock_release", map[string]any{
		"key":           "k",
		"fencing_token": float64(0),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing fencing_token")
	}
}

func TestHandleLockRelease_Success(t *testing.T) {
	called := false
	svc := newMockLockService()
	svc.unlockFn = func(_ context.Context, key string, fencingToken int64) error {
		called = true
		if key != "mykey" || fencingToken != 42 {
			return errors.New("wrong args")
		}
		return nil
	}
	handler := handleLockRelease(svc)

	req := buildCallToolRequest(t, "qrdb_lock_release", map[string]any{
		"key":           "mykey",
		"fencing_token": float64(42),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected IsError: %v", result.Content)
	}
	if !called {
		t.Error("expected Unlock to be called")
	}
}

func TestHandleLockRelease_ServiceError(t *testing.T) {
	svc := newMockLockService()
	svc.unlockFn = func(_ context.Context, _ string, _ int64) error {
		return errors.New("stale fencing token")
	}
	handler := handleLockRelease(svc)

	req := buildCallToolRequest(t, "qrdb_lock_release", map[string]any{
		"key":           "k",
		"fencing_token": float64(1),
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected ToolError")
	}
}

// ---------------------------------------------------------------------------
// handleLockExtend
// ---------------------------------------------------------------------------

func TestHandleLockExtend_MissingKey(t *testing.T) {
	svc := newMockLockService()
	handler := handleLockExtend(svc)

	req := buildCallToolRequest(t, "qrdb_lock_extend", map[string]any{
		"fencing_token": float64(1),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing key")
	}
}

func TestHandleLockExtend_Success(t *testing.T) {
	svc := newMockLockService()
	svc.extendFn = func(_ context.Context, key string, fencingToken int64) (*lock.Lock, error) {
		return &lock.Lock{Key: key, FencingToken: fencingToken, HolderID: "holder"}, nil
	}
	handler := handleLockExtend(svc)

	req := buildCallToolRequest(t, "qrdb_lock_extend", map[string]any{
		"key":           "resource",
		"fencing_token": float64(33),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected IsError: %v", result.Content)
	}
	text := resultText(t, result)
	var l lock.Lock
	if err := json.Unmarshal([]byte(text), &l); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if l.FencingToken != 33 {
		t.Errorf("expected fencing_token 33, got %d", l.FencingToken)
	}
}

func TestHandleLockExtend_ServiceError(t *testing.T) {
	svc := newMockLockService()
	svc.extendFn = func(_ context.Context, _ string, _ int64) (*lock.Lock, error) {
		return nil, errors.New("lock expired")
	}
	handler := handleLockExtend(svc)

	req := buildCallToolRequest(t, "qrdb_lock_extend", map[string]any{
		"key":           "k",
		"fencing_token": float64(1),
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected ToolError")
	}
}

// ---------------------------------------------------------------------------
// handleLockStatus
// ---------------------------------------------------------------------------

func TestHandleLockStatus_MissingKey(t *testing.T) {
	svc := newMockLockService()
	handler := handleLockStatus(svc)

	req := buildCallToolRequest(t, "qrdb_lock_status", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing key")
	}
}

func TestHandleLockStatus_Success(t *testing.T) {
	svc := newMockLockService()
	svc.statusFn = func(_ context.Context, key string) (*lock.Lock, error) {
		return &lock.Lock{Key: key, HolderID: "node-3", FencingToken: 11}, nil
	}
	handler := handleLockStatus(svc)

	req := buildCallToolRequest(t, "qrdb_lock_status", map[string]any{"key": "res"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected IsError: %v", result.Content)
	}
	text := resultText(t, result)
	var l lock.Lock
	if err := json.Unmarshal([]byte(text), &l); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if l.HolderID != "node-3" {
		t.Errorf("expected holder 'node-3', got %q", l.HolderID)
	}
}

func TestHandleLockStatus_ServiceError(t *testing.T) {
	svc := newMockLockService()
	svc.statusFn = func(_ context.Context, _ string) (*lock.Lock, error) {
		return nil, errors.New("not found")
	}
	handler := handleLockStatus(svc)

	req := buildCallToolRequest(t, "qrdb_lock_status", map[string]any{"key": "k"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected ToolError")
	}
}

// ---------------------------------------------------------------------------
// handleLockList
// ---------------------------------------------------------------------------

func TestHandleLockList_Success(t *testing.T) {
	svc := newMockLockService()
	svc.listFn = func(_ context.Context, prefix string) ([]*lock.Lock, error) {
		return []*lock.Lock{
			{Key: prefix + "x", HolderID: "h1", FencingToken: 1},
			{Key: prefix + "y", HolderID: "h2", FencingToken: 2},
		}, nil
	}
	handler := handleLockList(svc)

	req := buildCallToolRequest(t, "qrdb_lock_list", map[string]any{"prefix": "res/"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected IsError: %v", result.Content)
	}
	text := resultText(t, result)
	var locks []*lock.Lock
	if err := json.Unmarshal([]byte(text), &locks); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(locks) != 2 {
		t.Fatalf("expected 2 locks, got %d", len(locks))
	}
}

func TestHandleLockList_Empty(t *testing.T) {
	svc := newMockLockService()
	svc.listFn = func(_ context.Context, _ string) ([]*lock.Lock, error) {
		return []*lock.Lock{}, nil
	}
	handler := handleLockList(svc)

	req := buildCallToolRequest(t, "qrdb_lock_list", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected IsError: %v", result.Content)
	}
}

func TestHandleLockList_ServiceError(t *testing.T) {
	svc := newMockLockService()
	svc.listFn = func(_ context.Context, _ string) ([]*lock.Lock, error) {
		return nil, errors.New("store error")
	}
	handler := handleLockList(svc)

	req := buildCallToolRequest(t, "qrdb_lock_list", map[string]any{})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected ToolError")
	}
}

// ---------------------------------------------------------------------------
// WithLock option
// ---------------------------------------------------------------------------

func TestWithLock_ToolsRegistered(t *testing.T) {
	svc := newMockLockService()
	s := NewServer(newMockKVService(), newMockAdminService(), ":0",
		WithLock(svc))

	if s.lockSvc == nil {
		t.Fatal("expected lockSvc to be set on server")
	}

	tools := s.MCPServer().ListTools()
	expected := []string{
		"qrdb_lock_acquire",
		"qrdb_lock_trylock",
		"qrdb_lock_release",
		"qrdb_lock_extend",
		"qrdb_lock_status",
		"qrdb_lock_list",
	}
	for _, name := range expected {
		if _, ok := tools[name]; !ok {
			t.Errorf("tool %s not registered", name)
		}
	}
}
