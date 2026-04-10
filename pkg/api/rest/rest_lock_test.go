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

package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Mock: LockServicer
// ---------------------------------------------------------------------------

// mockLockService implements service.LockServicer for REST handler testing.
// Each method delegates to an overridable function field.
type mockLockService struct {
	lockFn    func(ctx context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error)
	tryLockFn func(ctx context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error)
	unlockFn  func(ctx context.Context, key string, fencingToken int64) error
	extendFn  func(ctx context.Context, key string, fencingToken int64) (*lock.Lock, error)
	statusFn  func(ctx context.Context, key string) (*lock.Lock, error)
	listFn    func(ctx context.Context, prefix string) ([]*lock.Lock, error)
}

// Compile-time interface check.
var _ service.LockServicer = (*mockLockService)(nil)

func (m *mockLockService) Lock(ctx context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error) {
	if m.lockFn != nil {
		return m.lockFn(ctx, key, holderID, ttl, reentrant)
	}
	return &lock.Lock{
		Key:          key,
		FencingToken: 1,
		LeaseID:      100,
		HolderID:     holderID,
		Reentrant:    reentrant,
		AcquireCount: 1,
		TTL:          int64(ttl.Seconds()),
		AcquiredAt:   time.Now().Unix(),
	}, nil
}

func (m *mockLockService) TryLock(ctx context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error) {
	if m.tryLockFn != nil {
		return m.tryLockFn(ctx, key, holderID, ttl, reentrant)
	}
	return &lock.Lock{
		Key:          key,
		FencingToken: 2,
		LeaseID:      101,
		HolderID:     holderID,
		Reentrant:    reentrant,
		AcquireCount: 1,
		TTL:          int64(ttl.Seconds()),
		AcquiredAt:   time.Now().Unix(),
	}, nil
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
	return &lock.Lock{
		Key:          key,
		FencingToken: fencingToken,
		LeaseID:      100,
		HolderID:     "holder-1",
		AcquireCount: 1,
		TTL:          30,
		AcquiredAt:   time.Now().Unix(),
	}, nil
}

func (m *mockLockService) Status(ctx context.Context, key string) (*lock.Lock, error) {
	if m.statusFn != nil {
		return m.statusFn(ctx, key)
	}
	return &lock.Lock{
		Key:          key,
		FencingToken: 1,
		LeaseID:      100,
		HolderID:     "holder-1",
		AcquireCount: 1,
		TTL:          30,
		AcquiredAt:   time.Now().Unix(),
	}, nil
}

func (m *mockLockService) List(ctx context.Context, prefix string) ([]*lock.Lock, error) {
	if m.listFn != nil {
		return m.listFn(ctx, prefix)
	}
	return []*lock.Lock{
		{
			Key:          "resource-1",
			FencingToken: 1,
			LeaseID:      100,
			HolderID:     "holder-1",
			AcquireCount: 1,
			TTL:          30,
			AcquiredAt:   time.Now().Unix(),
		},
	}, nil
}

// ---------------------------------------------------------------------------
// Helper: construct server with lock service
// ---------------------------------------------------------------------------

func newLockTestServer(t *testing.T, lockSvc service.LockServicer) *Server {
	t.Helper()
	opts := []ServerOption{}
	if lockSvc != nil {
		opts = append(opts, WithLock(lockSvc))
	}
	return NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", opts...)
}

// ---------------------------------------------------------------------------
// NilService: all endpoints return 501 when lockSvc is nil
// ---------------------------------------------------------------------------

func TestLockHandlers_NilService(t *testing.T) {
	srv := &Server{lockSvc: nil}

	handlers := []struct {
		name    string
		method  string
		path    string
		body    string
		handler func(http.ResponseWriter, *http.Request)
	}{
		{"Acquire", http.MethodPost, "/lock/acquire", `{"key":"k","holder_id":"h","ttl":30}`, srv.handleLockAcquire},
		{"TryLock", http.MethodPost, "/lock/trylock", `{"key":"k","holder_id":"h","ttl":30}`, srv.handleLockTryLock},
		{"Release", http.MethodPost, "/lock/release", `{"key":"k","fencing_token":1}`, srv.handleLockRelease},
		{"Extend", http.MethodPost, "/lock/extend", `{"key":"k","fencing_token":1}`, srv.handleLockExtend},
		{"Status", http.MethodGet, "/lock/status?key=k", "", srv.handleLockStatus},
		{"List", http.MethodGet, "/lock/list", "", srv.handleLockList},
	}

	for _, tc := range handlers {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			tc.handler(rec, req)
			assertStatus(t, rec, http.StatusNotImplemented)
		})
	}
}

// ---------------------------------------------------------------------------
// Acquire: POST /lock/acquire
// ---------------------------------------------------------------------------

func TestLockHandlers_Acquire_Success(t *testing.T) {
	svc := &mockLockService{}
	srv := newLockTestServer(t, svc)

	body := `{"key":"resource-1","holder_id":"node-abc","ttl":60,"reentrant":false}`
	req := httptest.NewRequest(http.MethodPost, "/lock/acquire", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleLockAcquire(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp lockResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Key != "resource-1" {
		t.Errorf("key = %q, want %q", resp.Key, "resource-1")
	}
	if resp.HolderID != "node-abc" {
		t.Errorf("holder_id = %q, want %q", resp.HolderID, "node-abc")
	}
}

func TestLockHandlers_Acquire_InvalidBody(t *testing.T) {
	svc := &mockLockService{}
	srv := newLockTestServer(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/lock/acquire", strings.NewReader("{invalid"))
	rec := httptest.NewRecorder()
	srv.handleLockAcquire(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestLockHandlers_Acquire_ServiceError(t *testing.T) {
	svc := &mockLockService{
		lockFn: func(_ context.Context, key, holderID string, _ time.Duration, _ bool) (*lock.Lock, error) {
			return nil, &lock.LockHeldError{Key: key, HolderID: "other-holder"}
		},
	}
	srv := newLockTestServer(t, svc)

	body := `{"key":"resource-1","holder_id":"node-abc","ttl":30}`
	req := httptest.NewRequest(http.MethodPost, "/lock/acquire", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleLockAcquire(rec, req)

	assertStatus(t, rec, http.StatusConflict)
}

// ---------------------------------------------------------------------------
// TryLock: POST /lock/trylock
// ---------------------------------------------------------------------------

func TestLockHandlers_TryLock_Success(t *testing.T) {
	svc := &mockLockService{}
	srv := newLockTestServer(t, svc)

	body := `{"key":"resource-2","holder_id":"node-xyz","ttl":30}`
	req := httptest.NewRequest(http.MethodPost, "/lock/trylock", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleLockTryLock(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp lockResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Key != "resource-2" {
		t.Errorf("key = %q, want %q", resp.Key, "resource-2")
	}
}

func TestLockHandlers_TryLock_Held(t *testing.T) {
	svc := &mockLockService{
		tryLockFn: func(_ context.Context, key, _ string, _ time.Duration, _ bool) (*lock.Lock, error) {
			return nil, &lock.LockHeldError{Key: key, HolderID: "other"}
		},
	}
	srv := newLockTestServer(t, svc)

	body := `{"key":"resource-2","holder_id":"node-xyz","ttl":30}`
	req := httptest.NewRequest(http.MethodPost, "/lock/trylock", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleLockTryLock(rec, req)

	assertStatus(t, rec, http.StatusConflict)
}

func TestLockHandlers_TryLock_InvalidBody(t *testing.T) {
	svc := &mockLockService{}
	srv := newLockTestServer(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/lock/trylock", strings.NewReader("not-json"))
	rec := httptest.NewRecorder()
	srv.handleLockTryLock(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

// ---------------------------------------------------------------------------
// Release: POST /lock/release
// ---------------------------------------------------------------------------

func TestLockHandlers_Release_Success(t *testing.T) {
	svc := &mockLockService{}
	srv := newLockTestServer(t, svc)

	body := `{"key":"resource-1","fencing_token":42}`
	req := httptest.NewRequest(http.MethodPost, "/lock/release", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleLockRelease(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp map[string]string
	decodeJSON(t, rec, &resp)
	if resp["status"] != "released" {
		t.Errorf("status = %q, want %q", resp["status"], "released")
	}
}

func TestLockHandlers_Release_WrongHolder(t *testing.T) {
	svc := &mockLockService{
		unlockFn: func(_ context.Context, key string, token int64) error {
			return &lock.NotLockHolderError{Key: key, ExpectedToken: token, ActualToken: 99}
		},
	}
	srv := newLockTestServer(t, svc)

	body := `{"key":"resource-1","fencing_token":42}`
	req := httptest.NewRequest(http.MethodPost, "/lock/release", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleLockRelease(rec, req)

	assertStatus(t, rec, http.StatusForbidden)
}

func TestLockHandlers_Release_InvalidBody(t *testing.T) {
	svc := &mockLockService{}
	srv := newLockTestServer(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/lock/release", strings.NewReader("{bad"))
	rec := httptest.NewRecorder()
	srv.handleLockRelease(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

// ---------------------------------------------------------------------------
// Extend: POST /lock/extend
// ---------------------------------------------------------------------------

func TestLockHandlers_Extend_Success(t *testing.T) {
	svc := &mockLockService{}
	srv := newLockTestServer(t, svc)

	body := `{"key":"resource-1","fencing_token":42}`
	req := httptest.NewRequest(http.MethodPost, "/lock/extend", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleLockExtend(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp lockResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Key != "resource-1" {
		t.Errorf("key = %q, want %q", resp.Key, "resource-1")
	}
}

func TestLockHandlers_Extend_NotHolder(t *testing.T) {
	svc := &mockLockService{
		extendFn: func(_ context.Context, key string, token int64) (*lock.Lock, error) {
			return nil, &lock.NotLockHolderError{Key: key, ExpectedToken: token, ActualToken: 1}
		},
	}
	srv := newLockTestServer(t, svc)

	body := `{"key":"resource-1","fencing_token":99}`
	req := httptest.NewRequest(http.MethodPost, "/lock/extend", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleLockExtend(rec, req)

	assertStatus(t, rec, http.StatusForbidden)
}

func TestLockHandlers_Extend_InvalidBody(t *testing.T) {
	svc := &mockLockService{}
	srv := newLockTestServer(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/lock/extend", strings.NewReader("{bad"))
	rec := httptest.NewRecorder()
	srv.handleLockExtend(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

// ---------------------------------------------------------------------------
// Status: GET /lock/status?key=...
// ---------------------------------------------------------------------------

func TestLockHandlers_Status_Success(t *testing.T) {
	svc := &mockLockService{}
	srv := newLockTestServer(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/lock/status?key=resource-1", nil)
	rec := httptest.NewRecorder()
	srv.handleLockStatus(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp lockResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Key != "resource-1" {
		t.Errorf("key = %q, want %q", resp.Key, "resource-1")
	}
}

func TestLockHandlers_Status_NotFound(t *testing.T) {
	svc := &mockLockService{
		statusFn: func(_ context.Context, _ string) (*lock.Lock, error) {
			return nil, nil
		},
	}
	srv := newLockTestServer(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/lock/status?key=missing", nil)
	rec := httptest.NewRecorder()
	srv.handleLockStatus(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestLockHandlers_Status_MissingKey(t *testing.T) {
	svc := &mockLockService{}
	srv := newLockTestServer(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/lock/status", nil)
	rec := httptest.NewRecorder()
	srv.handleLockStatus(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

// ---------------------------------------------------------------------------
// List: GET /lock/list?prefix=...
// ---------------------------------------------------------------------------

func TestLockHandlers_List_Success(t *testing.T) {
	svc := &mockLockService{}
	srv := newLockTestServer(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/lock/list", nil)
	rec := httptest.NewRecorder()
	srv.handleLockList(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp locksResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Locks) != 1 {
		t.Errorf("len(locks) = %d, want 1", len(resp.Locks))
	}
}

func TestLockHandlers_List_WithPrefix(t *testing.T) {
	var capturedPrefix string
	svc := &mockLockService{
		listFn: func(_ context.Context, prefix string) ([]*lock.Lock, error) {
			capturedPrefix = prefix
			return nil, nil
		},
	}
	srv := newLockTestServer(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/lock/list?prefix=resource", nil)
	rec := httptest.NewRecorder()
	srv.handleLockList(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if capturedPrefix != "resource" {
		t.Errorf("prefix = %q, want %q", capturedPrefix, "resource")
	}
}

func TestLockHandlers_List_Empty(t *testing.T) {
	svc := &mockLockService{
		listFn: func(_ context.Context, _ string) ([]*lock.Lock, error) {
			return nil, nil
		},
	}
	srv := newLockTestServer(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/lock/list", nil)
	rec := httptest.NewRecorder()
	srv.handleLockList(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp locksResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Locks) != 0 {
		t.Errorf("len(locks) = %d, want 0", len(resp.Locks))
	}
}
