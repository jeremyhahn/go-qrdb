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

package embedded

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/service"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// helper: build an embedded transport with default required services
// ---------------------------------------------------------------------------

// newExtTransport creates an embedded Transport with the given options.
func newExtTransport(t *testing.T, opts ...Option) *Transport {
	t.Helper()
	tr, err := New(newMockKVService(), &mockAdminService{}, opts...)
	if err != nil {
		t.Fatalf("failed to create transport: %v", err)
	}
	return tr
}

func closedExtTransport(t *testing.T, opts ...Option) *Transport {
	t.Helper()
	tr := newExtTransport(t, opts...)
	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return tr
}

// errSentinel is a generic sentinel error used by mock services to verify
// upstream propagation.
var errSentinel = errors.New("ext: sentinel error")

// ===========================================================================
// CredentialSubmit / Strategy / Delete (0% coverage previously)
// ===========================================================================

func TestEmbeddedCredential_Submit(t *testing.T) {
	var (
		captured  string
		capturedV []byte
	)
	svc := &mockCredentialService{
		submitFn: func(_ context.Context, name string, value []byte) error {
			captured = name
			capturedV = value
			return nil
		},
	}
	tr := newExtTransport(t, WithCredentialService(svc))

	if err := tr.CredentialSubmit(context.Background(), &transport.CredentialSubmitRequest{
		Name:  "db",
		Value: []byte("secret"),
	}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if captured != "db" || string(capturedV) != "secret" {
		t.Errorf("captured (%q, %q)", captured, string(capturedV))
	}
}

func TestEmbeddedCredential_Submit_NilService(t *testing.T) {
	tr := newExtTransport(t)
	err := tr.CredentialSubmit(context.Background(), &transport.CredentialSubmitRequest{})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestEmbeddedCredential_Submit_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithCredentialService(&mockCredentialService{}))
	err := tr.CredentialSubmit(context.Background(), &transport.CredentialSubmitRequest{})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestEmbeddedCredential_Strategy(t *testing.T) {
	svc := &mockCredentialService{
		strategyFn: func() string { return "vault" },
	}
	tr := newExtTransport(t, WithCredentialService(svc))

	resp, err := tr.CredentialStrategy(context.Background())
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if resp.Strategy != "vault" {
		t.Errorf("Strategy = %q, want vault", resp.Strategy)
	}
}

func TestEmbeddedCredential_Strategy_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.CredentialStrategy(context.Background())
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestEmbeddedCredential_Strategy_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithCredentialService(&mockCredentialService{}))
	_, err := tr.CredentialStrategy(context.Background())
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestEmbeddedCredential_Delete(t *testing.T) {
	var captured string
	svc := &mockCredentialService{
		deleteFn: func(_ context.Context, name string) error {
			captured = name
			return nil
		},
	}
	tr := newExtTransport(t, WithCredentialService(svc))

	if err := tr.CredentialDelete(context.Background(), "expired-token"); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if captured != "expired-token" {
		t.Errorf("captured = %q", captured)
	}
}

func TestEmbeddedCredential_Delete_NilService(t *testing.T) {
	tr := newExtTransport(t)
	err := tr.CredentialDelete(context.Background(), "x")
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestEmbeddedCredential_Delete_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithCredentialService(&mockCredentialService{}))
	err := tr.CredentialDelete(context.Background(), "x")
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestEmbeddedCredential_Submit_Dispatch(t *testing.T) {
	svc := &mockCredentialService{}
	tr := newExtTransport(t, WithCredentialService(svc))
	if err := tr.Request(context.Background(), "CredentialSubmit",
		&transport.CredentialSubmitRequest{Name: "k", Value: []byte("v")}, nil); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
}

func TestEmbeddedCredential_Submit_DispatchInvalidReq(t *testing.T) {
	tr := newExtTransport(t, WithCredentialService(&mockCredentialService{}))
	err := tr.Request(context.Background(), "CredentialSubmit", "bad", nil)
	var invErr *InvalidRequestError
	if !errors.As(err, &invErr) {
		t.Fatalf("expected InvalidRequestError, got %T: %v", err, err)
	}
}

func TestEmbeddedCredential_Strategy_Dispatch(t *testing.T) {
	svc := &mockCredentialService{strategyFn: func() string { return "x" }}
	tr := newExtTransport(t, WithCredentialService(svc))
	var resp *transport.CredentialStrategyResponse
	if err := tr.Request(context.Background(), "CredentialStrategy", nil, &resp); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if resp == nil || resp.Strategy != "x" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestEmbeddedCredential_Delete_Dispatch(t *testing.T) {
	tr := newExtTransport(t, WithCredentialService(&mockCredentialService{}))
	if err := tr.Request(context.Background(), "CredentialDelete",
		&transport.CredentialDeleteRequest{Name: "x"}, nil); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
}

func TestEmbeddedCredential_Delete_DispatchInvalidReq(t *testing.T) {
	tr := newExtTransport(t, WithCredentialService(&mockCredentialService{}))
	err := tr.Request(context.Background(), "CredentialDelete", 42, nil)
	var invErr *InvalidRequestError
	if !errors.As(err, &invErr) {
		t.Fatalf("expected InvalidRequestError, got %T: %v", err, err)
	}
}

// ===========================================================================
// Lock transport (was 0%)
// ===========================================================================

type mockLockService struct {
	lockFn    func(ctx context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error)
	tryLockFn func(ctx context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error)
	unlockFn  func(ctx context.Context, key string, token int64) error
	extendFn  func(ctx context.Context, key string, token int64) (*lock.Lock, error)
	statusFn  func(ctx context.Context, key string) (*lock.Lock, error)
	listFn    func(ctx context.Context, prefix string) ([]*lock.Lock, error)
}

func (m *mockLockService) Lock(ctx context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error) {
	if m.lockFn != nil {
		return m.lockFn(ctx, key, holderID, ttl, reentrant)
	}
	return &lock.Lock{Key: key, HolderID: holderID, TTL: int64(ttl.Seconds()), Reentrant: reentrant, FencingToken: 1}, nil
}

func (m *mockLockService) TryLock(ctx context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error) {
	if m.tryLockFn != nil {
		return m.tryLockFn(ctx, key, holderID, ttl, reentrant)
	}
	return &lock.Lock{Key: key, HolderID: holderID, TTL: int64(ttl.Seconds()), FencingToken: 2}, nil
}

func (m *mockLockService) Unlock(ctx context.Context, key string, token int64) error {
	if m.unlockFn != nil {
		return m.unlockFn(ctx, key, token)
	}
	return nil
}

func (m *mockLockService) Extend(ctx context.Context, key string, token int64) (*lock.Lock, error) {
	if m.extendFn != nil {
		return m.extendFn(ctx, key, token)
	}
	return &lock.Lock{Key: key, FencingToken: token}, nil
}

func (m *mockLockService) Status(ctx context.Context, key string) (*lock.Lock, error) {
	if m.statusFn != nil {
		return m.statusFn(ctx, key)
	}
	return nil, nil
}

func (m *mockLockService) List(ctx context.Context, prefix string) ([]*lock.Lock, error) {
	if m.listFn != nil {
		return m.listFn(ctx, prefix)
	}
	return nil, nil
}

func TestEmbeddedLock_Acquire(t *testing.T) {
	now := time.Now().Unix()
	svc := &mockLockService{
		lockFn: func(_ context.Context, key, holderID string, ttl time.Duration, _ bool) (*lock.Lock, error) {
			return &lock.Lock{
				Key:          key,
				HolderID:     holderID,
				FencingToken: 42,
				LeaseID:      99,
				TTL:          int64(ttl.Seconds()),
				AcquiredAt:   now,
			}, nil
		},
	}
	tr := newExtTransport(t, WithLockService(svc))
	info, err := tr.LockAcquire(context.Background(), &transport.LockAcquireRequest{
		Key:      "k",
		HolderID: "h",
		TTL:      30,
	})
	if err != nil {
		t.Fatalf("LockAcquire: %v", err)
	}
	if info.Key != "k" || info.FencingToken != 42 || info.LeaseID != 99 {
		t.Errorf("info = %+v", info)
	}
}

func TestEmbeddedLock_Acquire_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.LockAcquire(context.Background(), &transport.LockAcquireRequest{})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedLock_Acquire_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithLockService(&mockLockService{}))
	_, err := tr.LockAcquire(context.Background(), &transport.LockAcquireRequest{})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedLock_Acquire_Error(t *testing.T) {
	svc := &mockLockService{
		lockFn: func(_ context.Context, _, _ string, _ time.Duration, _ bool) (*lock.Lock, error) {
			return nil, errSentinel
		},
	}
	tr := newExtTransport(t, WithLockService(svc))
	_, err := tr.LockAcquire(context.Background(), &transport.LockAcquireRequest{})
	if !errors.Is(err, errSentinel) {
		t.Errorf("expected errSentinel, got %v", err)
	}
}

func TestEmbeddedLock_TryLock(t *testing.T) {
	tr := newExtTransport(t, WithLockService(&mockLockService{}))
	info, err := tr.LockTryLock(context.Background(), &transport.LockAcquireRequest{
		Key:      "k",
		HolderID: "h",
		TTL:      5,
	})
	if err != nil {
		t.Fatalf("LockTryLock: %v", err)
	}
	if info.Key != "k" || info.FencingToken != 2 {
		t.Errorf("info = %+v", info)
	}
}

func TestEmbeddedLock_TryLock_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.LockTryLock(context.Background(), &transport.LockAcquireRequest{})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedLock_TryLock_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithLockService(&mockLockService{}))
	_, err := tr.LockTryLock(context.Background(), &transport.LockAcquireRequest{})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedLock_TryLock_Error(t *testing.T) {
	svc := &mockLockService{
		tryLockFn: func(_ context.Context, _, _ string, _ time.Duration, _ bool) (*lock.Lock, error) {
			return nil, errSentinel
		},
	}
	tr := newExtTransport(t, WithLockService(svc))
	_, err := tr.LockTryLock(context.Background(), &transport.LockAcquireRequest{})
	if !errors.Is(err, errSentinel) {
		t.Errorf("expected errSentinel, got %v", err)
	}
}

func TestEmbeddedLock_Release(t *testing.T) {
	var capturedKey string
	var capturedToken int64
	svc := &mockLockService{
		unlockFn: func(_ context.Context, key string, token int64) error {
			capturedKey = key
			capturedToken = token
			return nil
		},
	}
	tr := newExtTransport(t, WithLockService(svc))
	if err := tr.LockRelease(context.Background(), &transport.LockReleaseRequest{
		Key:          "abc",
		FencingToken: 7,
	}); err != nil {
		t.Fatalf("LockRelease: %v", err)
	}
	if capturedKey != "abc" || capturedToken != 7 {
		t.Errorf("captured (%q, %d)", capturedKey, capturedToken)
	}
}

func TestEmbeddedLock_Release_NilService(t *testing.T) {
	tr := newExtTransport(t)
	err := tr.LockRelease(context.Background(), &transport.LockReleaseRequest{})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedLock_Release_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithLockService(&mockLockService{}))
	err := tr.LockRelease(context.Background(), &transport.LockReleaseRequest{})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedLock_Extend(t *testing.T) {
	tr := newExtTransport(t, WithLockService(&mockLockService{}))
	info, err := tr.LockExtend(context.Background(), &transport.LockExtendRequest{
		Key:          "k",
		FencingToken: 11,
	})
	if err != nil {
		t.Fatalf("LockExtend: %v", err)
	}
	if info.Key != "k" || info.FencingToken != 11 {
		t.Errorf("info = %+v", info)
	}
}

func TestEmbeddedLock_Extend_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.LockExtend(context.Background(), &transport.LockExtendRequest{})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedLock_Extend_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithLockService(&mockLockService{}))
	_, err := tr.LockExtend(context.Background(), &transport.LockExtendRequest{})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedLock_Extend_Error(t *testing.T) {
	svc := &mockLockService{
		extendFn: func(_ context.Context, _ string, _ int64) (*lock.Lock, error) {
			return nil, errSentinel
		},
	}
	tr := newExtTransport(t, WithLockService(svc))
	_, err := tr.LockExtend(context.Background(), &transport.LockExtendRequest{})
	if !errors.Is(err, errSentinel) {
		t.Errorf("expected errSentinel, got %v", err)
	}
}

func TestEmbeddedLock_Status_Found(t *testing.T) {
	svc := &mockLockService{
		statusFn: func(_ context.Context, key string) (*lock.Lock, error) {
			return &lock.Lock{Key: key, FencingToken: 9, HolderID: "owner"}, nil
		},
	}
	tr := newExtTransport(t, WithLockService(svc))
	info, err := tr.LockStatus(context.Background(), "k")
	if err != nil {
		t.Fatalf("LockStatus: %v", err)
	}
	if info == nil || info.HolderID != "owner" {
		t.Errorf("info = %+v", info)
	}
}

func TestEmbeddedLock_Status_NotFound(t *testing.T) {
	svc := &mockLockService{
		statusFn: func(_ context.Context, _ string) (*lock.Lock, error) {
			return nil, nil
		},
	}
	tr := newExtTransport(t, WithLockService(svc))
	info, err := tr.LockStatus(context.Background(), "k")
	if err != nil {
		t.Fatalf("LockStatus: %v", err)
	}
	if info != nil {
		t.Errorf("expected nil, got %+v", info)
	}
}

func TestEmbeddedLock_Status_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.LockStatus(context.Background(), "k")
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedLock_Status_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithLockService(&mockLockService{}))
	_, err := tr.LockStatus(context.Background(), "k")
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedLock_Status_Error(t *testing.T) {
	svc := &mockLockService{
		statusFn: func(_ context.Context, _ string) (*lock.Lock, error) {
			return nil, errSentinel
		},
	}
	tr := newExtTransport(t, WithLockService(svc))
	_, err := tr.LockStatus(context.Background(), "k")
	if !errors.Is(err, errSentinel) {
		t.Errorf("expected errSentinel, got %v", err)
	}
}

func TestEmbeddedLock_List(t *testing.T) {
	svc := &mockLockService{
		listFn: func(_ context.Context, _ string) ([]*lock.Lock, error) {
			return []*lock.Lock{
				{Key: "a", FencingToken: 1},
				{Key: "b", FencingToken: 2},
			}, nil
		},
	}
	tr := newExtTransport(t, WithLockService(svc))
	resp, err := tr.LockList(context.Background(), "")
	if err != nil {
		t.Fatalf("LockList: %v", err)
	}
	if len(resp.Locks) != 2 {
		t.Errorf("len = %d, want 2", len(resp.Locks))
	}
}

func TestEmbeddedLock_List_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.LockList(context.Background(), "")
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedLock_List_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithLockService(&mockLockService{}))
	_, err := tr.LockList(context.Background(), "")
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedLock_List_Error(t *testing.T) {
	svc := &mockLockService{
		listFn: func(_ context.Context, _ string) ([]*lock.Lock, error) {
			return nil, errSentinel
		},
	}
	tr := newExtTransport(t, WithLockService(svc))
	_, err := tr.LockList(context.Background(), "")
	if !errors.Is(err, errSentinel) {
		t.Errorf("expected errSentinel, got %v", err)
	}
}

// ===========================================================================
// SealStore transport (concrete *seal.SealStore; only nil/closed paths)
// ===========================================================================

func TestEmbeddedSealStore_Put_NilService(t *testing.T) {
	tr := newExtTransport(t)
	err := tr.SealStorePut(context.Background(), &transport.SealStorePutRequest{})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
	if nilErr.Service != "SealStore" {
		t.Errorf("Service = %q", nilErr.Service)
	}
}

func TestEmbeddedSealStore_Put_Closed(t *testing.T) {
	tr := closedExtTransport(t)
	err := tr.SealStorePut(context.Background(), &transport.SealStorePutRequest{})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedSealStore_Get_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.SealStoreGet(context.Background(), &transport.SealStoreGetRequest{})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedSealStore_Get_Closed(t *testing.T) {
	tr := closedExtTransport(t)
	_, err := tr.SealStoreGet(context.Background(), &transport.SealStoreGetRequest{})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedSealStore_Delete_NilService(t *testing.T) {
	tr := newExtTransport(t)
	err := tr.SealStoreDelete(context.Background(), &transport.SealStoreDeleteRequest{})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedSealStore_Delete_Closed(t *testing.T) {
	tr := closedExtTransport(t)
	err := tr.SealStoreDelete(context.Background(), &transport.SealStoreDeleteRequest{})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedSealStore_List_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.SealStoreList(context.Background())
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedSealStore_List_Closed(t *testing.T) {
	tr := closedExtTransport(t)
	_, err := tr.SealStoreList(context.Background())
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedSealStore_Reseal_NilService(t *testing.T) {
	tr := newExtTransport(t)
	err := tr.SealStoreReseal(context.Background(), &transport.SealStoreResealRequest{})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedSealStore_Reseal_Closed(t *testing.T) {
	tr := closedExtTransport(t)
	err := tr.SealStoreReseal(context.Background(), &transport.SealStoreResealRequest{})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedSealStore_Status_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.SealStoreStatus(context.Background())
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedSealStore_Status_Closed(t *testing.T) {
	tr := closedExtTransport(t)
	_, err := tr.SealStoreStatus(context.Background())
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedSealStore_WithOptionAssigns(t *testing.T) {
	// We can not construct a real *seal.SealStore without heavy
	// dependencies. Verify the option is invocable with nil and the
	// pointer is stored. Closed/nil paths above already cover nil.
	opt := WithSealStore(nil)
	tr := newExtTransport(t, opt)
	if tr.sealStoreSvc != nil {
		t.Errorf("sealStoreSvc = %v, want nil", tr.sealStoreSvc)
	}
}

// ===========================================================================
// CustodianGroup transport (was 0%)
// ===========================================================================

type mockCustodianGroupService struct {
	deleteGroupFn  func(ctx context.Context, id string) error
	removeMemberFn func(ctx context.Context, groupID, userID string) error
	distributeFn   func(ctx context.Context, groupID string) (int, error)
}

func (m *mockCustodianGroupService) DeleteGroup(ctx context.Context, id string) error {
	if m.deleteGroupFn != nil {
		return m.deleteGroupFn(ctx, id)
	}
	return nil
}

func (m *mockCustodianGroupService) RemoveMember(ctx context.Context, groupID, userID string) error {
	if m.removeMemberFn != nil {
		return m.removeMemberFn(ctx, groupID, userID)
	}
	return nil
}

func (m *mockCustodianGroupService) DistributeShares(ctx context.Context, groupID string) (int, error) {
	if m.distributeFn != nil {
		return m.distributeFn(ctx, groupID)
	}
	return 0, nil
}

// ===========================================================================
// Tenant transport (was 0%)
// ===========================================================================

type mockTenantService struct {
	tenant.TenantServicer
	createFn        func(ctx context.Context, id, name string, iso tenant.IsolationType) (*tenant.TenantInfo, error)
	getFn           func(ctx context.Context, id string) (*tenant.TenantInfo, error)
	listFn          func(ctx context.Context) ([]tenant.TenantInfo, error)
	deleteFn        func(ctx context.Context, id string) error
	updateFn        func(ctx context.Context, id string, patch tenant.TenantPatch) (*tenant.TenantInfo, error)
	updateQuotasFn  func(ctx context.Context, id string, q *quota.Quotas) (*tenant.TenantInfo, error)
	barrierInitFn   func(ctx context.Context, id string, passphrase []byte, threshold, shares int) error
	barrierUnsealFn func(ctx context.Context, id string, key []byte) error
	barrierSealFn   func(ctx context.Context, id string) error
	barrierStatusFn func(ctx context.Context, id string) (*tenant.TenantBarrierStatus, error)
}

func (m *mockTenantService) CreateTenant(ctx context.Context, id, name string, iso tenant.IsolationType, _ ...tenant.CreateTenantOption) (*tenant.TenantInfo, error) {
	if m.createFn != nil {
		return m.createFn(ctx, id, name, iso)
	}
	return &tenant.TenantInfo{ID: id, Name: name, IsolationType: iso, CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(2, 0)}, nil
}

func (m *mockTenantService) GetTenant(ctx context.Context, id string) (*tenant.TenantInfo, error) {
	if m.getFn != nil {
		return m.getFn(ctx, id)
	}
	return &tenant.TenantInfo{ID: id, Name: "t-" + id}, nil
}

func (m *mockTenantService) ListDeletedTenants(_ context.Context, _ string, _ int) (*tenant.DeletedTenantsPage, error) {
	return &tenant.DeletedTenantsPage{}, nil
}

func (m *mockTenantService) ReconcileDeletedTenants(_ context.Context) (*tenant.ReconcileReport, error) {
	return &tenant.ReconcileReport{}, nil
}

func (m *mockTenantService) ListTenants(ctx context.Context) ([]tenant.TenantInfo, error) {
	if m.listFn != nil {
		return m.listFn(ctx)
	}
	return nil, nil
}

func (m *mockTenantService) DeleteTenant(ctx context.Context, id string) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, id)
	}
	return nil
}

func (m *mockTenantService) UpdateTenant(ctx context.Context, id string, patch tenant.TenantPatch) (*tenant.TenantInfo, error) {
	if m.updateFn != nil {
		return m.updateFn(ctx, id, patch)
	}
	return &tenant.TenantInfo{ID: id, Name: patch.Name, IsolationType: patch.IsolationType}, nil
}

func (m *mockTenantService) UpdateQuotas(ctx context.Context, id string, q *quota.Quotas) (*tenant.TenantInfo, error) {
	if m.updateQuotasFn != nil {
		return m.updateQuotasFn(ctx, id, q)
	}
	return &tenant.TenantInfo{ID: id, Quotas: q}, nil
}

func (m *mockTenantService) UpdateEntitlements(ctx context.Context, id string, e *quota.Entitlements) (*tenant.TenantInfo, error) {
	return &tenant.TenantInfo{ID: id, Entitlements: e}, nil
}

func (m *mockTenantService) TenantBarrierInit(ctx context.Context, id string, passphrase []byte, threshold, shares int) (*tenant.TenantBarrierInitResult, error) {
	if m.barrierInitFn != nil {
		if err := m.barrierInitFn(ctx, id, passphrase, threshold, shares); err != nil {
			return nil, err
		}
	}
	return &tenant.TenantBarrierInitResult{TenantID: id}, nil
}

func (m *mockTenantService) TenantBarrierInitWithStrategy(ctx context.Context, id string, passphrase []byte, threshold, shares int, _ seal.StrategyID) (*tenant.TenantBarrierInitResult, error) {
	return m.TenantBarrierInit(ctx, id, passphrase, threshold, shares)
}

func (m *mockTenantService) TenantBarrierUnseal(ctx context.Context, id string, key []byte) error {
	if m.barrierUnsealFn != nil {
		return m.barrierUnsealFn(ctx, id, key)
	}
	return nil
}

func (m *mockTenantService) TenantBarrierSeal(ctx context.Context, id string) error {
	if m.barrierSealFn != nil {
		return m.barrierSealFn(ctx, id)
	}
	return nil
}

func (m *mockTenantService) TenantBarrierStatus(ctx context.Context, id string) (*tenant.TenantBarrierStatus, error) {
	if m.barrierStatusFn != nil {
		return m.barrierStatusFn(ctx, id)
	}
	return &tenant.TenantBarrierStatus{TenantID: id, Sealed: true, Initialized: true, Strategy: "passphrase"}, nil
}
func (m *mockTenantService) IncrementQuotaUsage(_ context.Context, _ string, _ quota.Resource, _ int64) (*tenant.TenantInfo, error) {
	return nil, nil
}
func (m *mockTenantService) DecrementQuotaUsage(_ context.Context, _ string, _ quota.Resource, _ int64) (*tenant.TenantInfo, error) {
	return nil, nil
}
func (m *mockTenantService) GetQuotaUsage(_ context.Context, _ string, _ quota.Resource) (int64, error) {
	return 0, nil
}

func (m *mockTenantService) TenantBarrierOfficerRegister(_ context.Context, _ string, _ *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error) {
	return &ceremony.RegisterOfficerResult{}, nil
}
func (m *mockTenantService) TenantBarrierOfficerList(_ context.Context, _ string) ([]*ceremony.OfficerStatus, error) {
	return nil, nil
}
func (m *mockTenantService) TenantBarrierShareClaimBegin(_ context.Context, _, _, _ string) (*ceremony.ChallengeResponse, error) {
	return &ceremony.ChallengeResponse{}, nil
}
func (m *mockTenantService) TenantBarrierShareClaimComplete(_ context.Context, _, _, _ string, _ []byte, _ string) (*ceremony.ShareClaimResult, error) {
	return &ceremony.ShareClaimResult{}, nil
}

func TestEmbeddedTenant_Create(t *testing.T) {
	var capturedID, capturedName string
	var capturedIso tenant.IsolationType
	svc := &mockTenantService{
		createFn: func(_ context.Context, id, name string, iso tenant.IsolationType) (*tenant.TenantInfo, error) {
			capturedID = id
			capturedName = name
			capturedIso = iso
			return &tenant.TenantInfo{
				ID:            id,
				Name:          name,
				IsolationType: iso,
				Quotas:        &quota.Quotas{MaxDatabases: 5},
				CreatedAt:     time.Unix(100, 0),
				UpdatedAt:     time.Unix(200, 0),
			}, nil
		},
	}
	tr := newExtTransport(t, WithTenantService(svc))
	resp, err := tr.CreateTenant(context.Background(), &transport.CreateTenantRequest{
		ID:            "t1",
		Name:          "tenant 1",
		IsolationType: "barrier",
	})
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if capturedID != "t1" || capturedName != "tenant 1" || capturedIso != "barrier" {
		t.Errorf("captured (%q, %q, %q)", capturedID, capturedName, capturedIso)
	}
	if resp.Tenant.ID != "t1" {
		t.Errorf("ID = %q", resp.Tenant.ID)
	}
	if resp.Tenant.Quotas == nil || resp.Tenant.Quotas.MaxDatabases != 5 {
		t.Errorf("Quotas = %+v", resp.Tenant.Quotas)
	}
}

func TestEmbeddedTenant_Create_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.CreateTenant(context.Background(), &transport.CreateTenantRequest{})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedTenant_Create_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithTenantService(&mockTenantService{}))
	_, err := tr.CreateTenant(context.Background(), &transport.CreateTenantRequest{})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedTenant_Create_Error(t *testing.T) {
	svc := &mockTenantService{
		createFn: func(context.Context, string, string, tenant.IsolationType) (*tenant.TenantInfo, error) {
			return nil, errSentinel
		},
	}
	tr := newExtTransport(t, WithTenantService(svc))
	_, err := tr.CreateTenant(context.Background(), &transport.CreateTenantRequest{})
	if !errors.Is(err, errSentinel) {
		t.Errorf("expected errSentinel, got %v", err)
	}
}

func TestEmbeddedTenant_Get(t *testing.T) {
	tr := newExtTransport(t, WithTenantService(&mockTenantService{}))
	resp, err := tr.GetTenant(context.Background(), "t1")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if resp.Tenant.ID != "t1" {
		t.Errorf("ID = %q", resp.Tenant.ID)
	}
}

func TestEmbeddedTenant_Get_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.GetTenant(context.Background(), "t1")
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedTenant_Get_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithTenantService(&mockTenantService{}))
	_, err := tr.GetTenant(context.Background(), "t1")
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedTenant_Get_Error(t *testing.T) {
	svc := &mockTenantService{
		getFn: func(context.Context, string) (*tenant.TenantInfo, error) {
			return nil, errSentinel
		},
	}
	tr := newExtTransport(t, WithTenantService(svc))
	_, err := tr.GetTenant(context.Background(), "t1")
	if !errors.Is(err, errSentinel) {
		t.Errorf("expected errSentinel, got %v", err)
	}
}

func TestEmbeddedTenant_List(t *testing.T) {
	svc := &mockTenantService{
		listFn: func(context.Context) ([]tenant.TenantInfo, error) {
			return []tenant.TenantInfo{
				{ID: "a"},
				{ID: "b", Quotas: &quota.Quotas{MaxUsers: 10}},
			}, nil
		},
	}
	tr := newExtTransport(t, WithTenantService(svc))
	resp, err := tr.ListTenants(context.Background())
	if err != nil {
		t.Fatalf("ListTenants: %v", err)
	}
	if len(resp.Tenants) != 2 {
		t.Errorf("len = %d", len(resp.Tenants))
	}
	if resp.Tenants[1].Quotas == nil || resp.Tenants[1].Quotas.MaxUsers != 10 {
		t.Errorf("Quotas = %+v", resp.Tenants[1].Quotas)
	}
}

func TestEmbeddedTenant_List_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.ListTenants(context.Background())
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedTenant_List_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithTenantService(&mockTenantService{}))
	_, err := tr.ListTenants(context.Background())
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedTenant_List_Error(t *testing.T) {
	svc := &mockTenantService{
		listFn: func(context.Context) ([]tenant.TenantInfo, error) {
			return nil, errSentinel
		},
	}
	tr := newExtTransport(t, WithTenantService(svc))
	_, err := tr.ListTenants(context.Background())
	if !errors.Is(err, errSentinel) {
		t.Errorf("expected errSentinel, got %v", err)
	}
}

func TestEmbeddedTenant_Update(t *testing.T) {
	tr := newExtTransport(t, WithTenantService(&mockTenantService{}))
	resp, err := tr.UpdateTenant(context.Background(), &transport.UpdateTenantRequest{
		TenantID:      "t1",
		Name:          "updated",
		IsolationType: "prefix",
	})
	if err != nil {
		t.Fatalf("UpdateTenant: %v", err)
	}
	if resp.Tenant.Name != "updated" {
		t.Errorf("Name = %q", resp.Tenant.Name)
	}
}

func TestEmbeddedTenant_Update_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.UpdateTenant(context.Background(), &transport.UpdateTenantRequest{})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedTenant_Update_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithTenantService(&mockTenantService{}))
	_, err := tr.UpdateTenant(context.Background(), &transport.UpdateTenantRequest{})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedTenant_Update_Error(t *testing.T) {
	svc := &mockTenantService{
		updateFn: func(context.Context, string, tenant.TenantPatch) (*tenant.TenantInfo, error) {
			return nil, errSentinel
		},
	}
	tr := newExtTransport(t, WithTenantService(svc))
	_, err := tr.UpdateTenant(context.Background(), &transport.UpdateTenantRequest{})
	if !errors.Is(err, errSentinel) {
		t.Errorf("expected errSentinel, got %v", err)
	}
}

func TestEmbeddedTenant_UpdateQuotas(t *testing.T) {
	tr := newExtTransport(t, WithTenantService(&mockTenantService{}))
	resp, err := tr.UpdateTenantQuotas(context.Background(), &transport.UpdateTenantQuotasRequest{
		TenantID: "t1",
		Quotas: transport.TenantQuotas{
			MaxDatabases:    3,
			MaxRaftGroups:   4,
			MaxCollections:  5,
			MaxKeys:         6,
			MaxStorageBytes: 7,
			MaxUsers:        8,
			MaxRoles:        9,
			MaxCertificates: 10,
			MaxBackups:      11,
			MaxVectorDims:   12,
			MaxTSMetrics:    13,
		},
	})
	if err != nil {
		t.Fatalf("UpdateTenantQuotas: %v", err)
	}
	if resp.Tenant.Quotas == nil || resp.Tenant.Quotas.MaxDatabases != 3 {
		t.Errorf("Quotas = %+v", resp.Tenant.Quotas)
	}
}

func TestEmbeddedTenant_UpdateQuotas_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.UpdateTenantQuotas(context.Background(), &transport.UpdateTenantQuotasRequest{})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedTenant_UpdateQuotas_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithTenantService(&mockTenantService{}))
	_, err := tr.UpdateTenantQuotas(context.Background(), &transport.UpdateTenantQuotasRequest{})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedTenant_UpdateQuotas_Error(t *testing.T) {
	svc := &mockTenantService{
		updateQuotasFn: func(context.Context, string, *quota.Quotas) (*tenant.TenantInfo, error) {
			return nil, errSentinel
		},
	}
	tr := newExtTransport(t, WithTenantService(svc))
	_, err := tr.UpdateTenantQuotas(context.Background(), &transport.UpdateTenantQuotasRequest{})
	if !errors.Is(err, errSentinel) {
		t.Errorf("expected errSentinel, got %v", err)
	}
}

func TestEmbeddedTenant_Delete(t *testing.T) {
	var captured string
	svc := &mockTenantService{
		deleteFn: func(_ context.Context, id string) error {
			captured = id
			return nil
		},
	}
	tr := newExtTransport(t, WithTenantService(svc))
	if err := tr.DeleteTenant(context.Background(), "t1"); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}
	if captured != "t1" {
		t.Errorf("captured = %q", captured)
	}
}

func TestEmbeddedTenant_Delete_NilService(t *testing.T) {
	tr := newExtTransport(t)
	err := tr.DeleteTenant(context.Background(), "t1")
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedTenant_Delete_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithTenantService(&mockTenantService{}))
	err := tr.DeleteTenant(context.Background(), "t1")
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedTenant_BarrierInit(t *testing.T) {
	var capturedID string
	var capturedT, capturedS int
	svc := &mockTenantService{
		barrierInitFn: func(_ context.Context, id string, _ []byte, threshold, shares int) error {
			capturedID = id
			capturedT = threshold
			capturedS = shares
			return nil
		},
	}
	tr := newExtTransport(t, WithTenantService(svc))
	if _, err := tr.TenantBarrierInit(context.Background(), &transport.TenantBarrierInitRequest{
		TenantID:  "t1",
		Threshold: 3,
		Shares:    5,
	}); err != nil {
		t.Fatalf("TenantBarrierInit: %v", err)
	}
	if capturedID != "t1" || capturedT != 3 || capturedS != 5 {
		t.Errorf("captured (%q, %d, %d)", capturedID, capturedT, capturedS)
	}
}

func TestEmbeddedTenant_BarrierInit_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.TenantBarrierInit(context.Background(), &transport.TenantBarrierInitRequest{})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedTenant_BarrierInit_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithTenantService(&mockTenantService{}))
	_, err := tr.TenantBarrierInit(context.Background(), &transport.TenantBarrierInitRequest{})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedTenant_BarrierUnseal_Key(t *testing.T) {
	var capturedKey []byte
	svc := &mockTenantService{
		barrierUnsealFn: func(_ context.Context, _ string, key []byte) error {
			capturedKey = key
			return nil
		},
	}
	tr := newExtTransport(t, WithTenantService(svc))
	if err := tr.TenantBarrierUnseal(context.Background(), &transport.TenantBarrierUnsealRequest{
		TenantID: "t1",
		Key:      []byte("master-key"),
	}); err != nil {
		t.Fatalf("TenantBarrierUnseal: %v", err)
	}
	if string(capturedKey) != "master-key" {
		t.Errorf("captured = %q", string(capturedKey))
	}
}

func TestEmbeddedTenant_BarrierUnseal_NilService(t *testing.T) {
	tr := newExtTransport(t)
	err := tr.TenantBarrierUnseal(context.Background(), &transport.TenantBarrierUnsealRequest{})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedTenant_BarrierUnseal_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithTenantService(&mockTenantService{}))
	err := tr.TenantBarrierUnseal(context.Background(), &transport.TenantBarrierUnsealRequest{})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedTenant_BarrierSeal(t *testing.T) {
	var captured string
	svc := &mockTenantService{
		barrierSealFn: func(_ context.Context, id string) error {
			captured = id
			return nil
		},
	}
	tr := newExtTransport(t, WithTenantService(svc))
	if err := tr.TenantBarrierSeal(context.Background(), &transport.TenantBarrierSealRequest{TenantID: "t1"}); err != nil {
		t.Fatalf("TenantBarrierSeal: %v", err)
	}
	if captured != "t1" {
		t.Errorf("captured = %q", captured)
	}
}

func TestEmbeddedTenant_BarrierSeal_NilService(t *testing.T) {
	tr := newExtTransport(t)
	err := tr.TenantBarrierSeal(context.Background(), &transport.TenantBarrierSealRequest{})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedTenant_BarrierSeal_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithTenantService(&mockTenantService{}))
	err := tr.TenantBarrierSeal(context.Background(), &transport.TenantBarrierSealRequest{})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedTenant_BarrierSeal_NilRequest(t *testing.T) {
	tr := newExtTransport(t, WithTenantService(&mockTenantService{}))
	err := tr.TenantBarrierSeal(context.Background(), nil)
	var nilReq *NilRequestError
	if !errors.As(err, &nilReq) {
		t.Fatalf("expected NilRequestError, got %T", err)
	}
}

func TestEmbeddedTenant_BarrierStatus(t *testing.T) {
	tr := newExtTransport(t, WithTenantService(&mockTenantService{}))
	resp, err := tr.TenantBarrierStatus(context.Background(), &transport.TenantBarrierStatusRequest{TenantID: "t1"})
	if err != nil {
		t.Fatalf("TenantBarrierStatus: %v", err)
	}
	if resp.TenantID != "t1" || !resp.Sealed || !resp.Initialized {
		t.Errorf("resp = %+v", resp)
	}
}

func TestEmbeddedTenant_BarrierStatus_ShamirFields(t *testing.T) {
	svc := &mockTenantService{
		barrierStatusFn: func(_ context.Context, id string) (*tenant.TenantBarrierStatus, error) {
			return &tenant.TenantBarrierStatus{
				TenantID:        id,
				Sealed:          true,
				Initialized:     true,
				Strategy:        "shamir",
				HardwareBacked:  true,
				ShamirThreshold: 2,
				ShamirTotal:     5,
			}, nil
		},
	}
	tr := newExtTransport(t, WithTenantService(svc))
	resp, err := tr.TenantBarrierStatus(context.Background(), &transport.TenantBarrierStatusRequest{TenantID: "t-shamir"})
	if err != nil {
		t.Fatalf("TenantBarrierStatus: %v", err)
	}
	if resp.TenantID != "t-shamir" || !resp.Sealed || !resp.Initialized {
		t.Errorf("base fields: resp = %+v", resp)
	}
	if resp.Strategy != "shamir" || !resp.HardwareBacked {
		t.Errorf("strategy mapping: resp = %+v", resp)
	}
	if resp.ShamirThreshold != 2 || resp.ShamirTotal != 5 {
		t.Errorf("shamir M/N mapping: got %d/%d, want 2/5", resp.ShamirThreshold, resp.ShamirTotal)
	}
}

func TestEmbeddedTenant_BarrierStatus_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.TenantBarrierStatus(context.Background(), &transport.TenantBarrierStatusRequest{})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedTenant_BarrierStatus_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithTenantService(&mockTenantService{}))
	_, err := tr.TenantBarrierStatus(context.Background(), &transport.TenantBarrierStatusRequest{})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedTenant_BarrierStatus_NilRequest(t *testing.T) {
	tr := newExtTransport(t, WithTenantService(&mockTenantService{}))
	_, err := tr.TenantBarrierStatus(context.Background(), nil)
	var nilReq *NilRequestError
	if !errors.As(err, &nilReq) {
		t.Fatalf("expected NilRequestError, got %T", err)
	}
}

func TestEmbeddedTenant_BarrierStatus_Error(t *testing.T) {
	svc := &mockTenantService{
		barrierStatusFn: func(context.Context, string) (*tenant.TenantBarrierStatus, error) {
			return nil, errSentinel
		},
	}
	tr := newExtTransport(t, WithTenantService(svc))
	_, err := tr.TenantBarrierStatus(context.Background(), &transport.TenantBarrierStatusRequest{})
	if !errors.Is(err, errSentinel) {
		t.Errorf("expected errSentinel, got %v", err)
	}
}

// ===========================================================================
// Database transport (was 0%)
// ===========================================================================

type mockDatabaseService struct {
	createFn           func(cfg database.DatabaseConfig) (*database.DatabaseInfo, error)
	getFn              func(name string) (*database.DatabaseInfo, error)
	listFn             func() ([]database.DatabaseInfo, error)
	dropFn             func(name string) error
	updateProtocolFn   func(name string, p database.ProtocolConfig) (*database.DatabaseInfo, error)
	updateEtcdFn       func(name string, e *database.EtcdConfig) (*database.DatabaseInfo, error)
	updateRaftFn       func(name string, r *database.DatabaseRaftConfig) (*database.DatabaseInfo, error)
	updateBarrierFn    func(name string, b *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error)
	updateConnectionFn func(name string, c *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error)
}

func (m *mockDatabaseService) Create(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
	if m.createFn != nil {
		return m.createFn(cfg)
	}
	return &database.DatabaseInfo{Name: cfg.Name, Engine: cfg.Engine, Backend: cfg.Backend}, nil
}

func (m *mockDatabaseService) Get(name string) (*database.DatabaseInfo, error) {
	if m.getFn != nil {
		return m.getFn(name)
	}
	return &database.DatabaseInfo{Name: name}, nil
}

func (m *mockDatabaseService) List() ([]database.DatabaseInfo, error) {
	if m.listFn != nil {
		return m.listFn()
	}
	return nil, nil
}

func (m *mockDatabaseService) Drop(_ context.Context, name string) error {
	if m.dropFn != nil {
		return m.dropFn(name)
	}
	return nil
}

func (m *mockDatabaseService) UpdateProtocol(name string, p database.ProtocolConfig) (*database.DatabaseInfo, error) {
	if m.updateProtocolFn != nil {
		return m.updateProtocolFn(name, p)
	}
	return &database.DatabaseInfo{Name: name, Protocol: p}, nil
}

func (m *mockDatabaseService) UpdateEtcd(name string, e *database.EtcdConfig) (*database.DatabaseInfo, error) {
	if m.updateEtcdFn != nil {
		return m.updateEtcdFn(name, e)
	}
	return &database.DatabaseInfo{Name: name, Protocol: database.ProtocolConfig{Etcd: e}}, nil
}

func (m *mockDatabaseService) UpdateRaft(name string, r *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	if m.updateRaftFn != nil {
		return m.updateRaftFn(name, r)
	}
	return &database.DatabaseInfo{Name: name, Raft: r}, nil
}

func (m *mockDatabaseService) UpdateBarrier(name string, b *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	if m.updateBarrierFn != nil {
		return m.updateBarrierFn(name, b)
	}
	return &database.DatabaseInfo{Name: name, Barrier: b}, nil
}

func (m *mockDatabaseService) UpdateConnection(name string, c *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	if m.updateConnectionFn != nil {
		return m.updateConnectionFn(name, c)
	}
	return &database.DatabaseInfo{Name: name, Connection: c}, nil
}

func (m *mockDatabaseService) SealDatabase(_ context.Context, _ string) error { return nil }
func (m *mockDatabaseService) UnsealDatabase(_ context.Context, _ string, _ seal.Credentials) error {
	return nil
}

// DatabaseBarrierStatus adapts databaseBarrierStatusSeal to the service
// interface, with no cluster seal state.
func (m *mockDatabaseService) DatabaseBarrierStatus(ctx context.Context, name string) (*service.DatabaseBarrierStatusInfo, error) {
	st, err := m.databaseBarrierStatusSeal(ctx, name)
	if st == nil {
		return nil, err
	}
	return &service.DatabaseBarrierStatusInfo{BarrierStatus: st}, err
}

func (m *mockDatabaseService) databaseBarrierStatusSeal(_ context.Context, _ string) (*seal.BarrierStatus, error) {
	return nil, nil
}

// Stubbed signatures for DatabaseServicer interface conformance.
func (m *mockDatabaseService) CreateForTenant(_ context.Context, cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
	return m.Create(cfg)
}
func (m *mockDatabaseService) GetForTenant(_ context.Context, name string) (*database.DatabaseInfo, error) {
	return m.Get(name)
}
func (m *mockDatabaseService) ListForTenant(_ context.Context) ([]database.DatabaseInfo, error) {
	return m.List()
}
func (m *mockDatabaseService) DropForTenant(_ context.Context, name string) error {
	return m.Drop(context.Background(), name)
}
func (m *mockDatabaseService) UpdateProtocolForTenant(_ context.Context, name string, p database.ProtocolConfig) (*database.DatabaseInfo, error) {
	return m.UpdateProtocol(name, p)
}
func (m *mockDatabaseService) UpdateEtcdForTenant(_ context.Context, name string, e *database.EtcdConfig) (*database.DatabaseInfo, error) {
	return m.UpdateEtcd(name, e)
}
func (m *mockDatabaseService) UpdateRaftForTenant(_ context.Context, name string, r *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	return m.UpdateRaft(name, r)
}
func (m *mockDatabaseService) UpdateBarrierForTenant(_ context.Context, name string, b *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	return m.UpdateBarrier(name, b)
}
func (m *mockDatabaseService) UpdateConnectionForTenant(_ context.Context, name string, c *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	return m.UpdateConnection(name, c)
}

func (m *mockDatabaseService) UpdateSQL(name string, sqlCfg *database.SQLConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: name, SQL: sqlCfg}, nil
}

func (m *mockDatabaseService) UpdateSQLForTenant(_ context.Context, name string, sqlCfg *database.SQLConfig) (*database.DatabaseInfo, error) {
	return m.UpdateSQL(name, sqlCfg)
}

func (m *mockDatabaseService) UpdateCompactionForTenant(_ context.Context, name string, policy *compaction.Policy) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: name, Compaction: policy}, nil
}
func (m *mockDatabaseService) ScaleForTenant(_ context.Context, name string, targetReplicas int) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: name, Replicas: targetReplicas}, nil
}

func TestEmbeddedDatabase_Create(t *testing.T) {
	var capturedCfg database.DatabaseConfig
	svc := &mockDatabaseService{
		createFn: func(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
			capturedCfg = cfg
			return &database.DatabaseInfo{
				Name:    cfg.Name,
				Engine:  cfg.Engine,
				Backend: cfg.Backend,
				ShardID: 9,
				Mode:    cfg.Mode,
				Protocol: database.ProtocolConfig{
					MySQL:      cfg.Protocol.MySQL,
					PostgreSQL: cfg.Protocol.PostgreSQL,
					Etcd:       &database.EtcdConfig{Enabled: true, ListenAddr: "127.0.0.1:2379"},
				},
				Raft: &database.DatabaseRaftConfig{
					ClusterMode: "multi",
					ShardID:     1,
					ReplicaID:   2,
					RaftAddress: "127.0.0.1:63001",
					Peers:       []string{"p1", "p2"},
				},
				Barrier: &database.DatabaseBarrierConfig{
					Enabled:        true,
					Mode:           "shamir",
					SharesRequired: 3,
					SharesTotal:    5,
				},
				Connection: &database.DatabaseConnectionConfig{
					Transport: "grpc",
					Address:   "127.0.0.1:5000",
					CAFile:    "/ca",
					CertFile:  "/cert",
					KeyFile:   "/key",
				},
			}, nil
		},
	}
	tr := newExtTransport(t, WithDatabaseService(svc))
	resp, err := tr.CreateDatabase(context.Background(), transport.CreateDatabaseRequest{
		Name:    "db1",
		Engine:  "kv",
		Backend: "pebble",
		Mode:    "raft",
		Protocol: transport.ProtocolConfig{
			MySQL:      true,
			PostgreSQL: false,
		},
		Raft: &transport.DatabaseRaftConfig{
			ClusterMode: "multi",
			ReplicaID:   2,
			RaftAddress: "127.0.0.1:63001",
			Peers:       []string{"p1", "p2"},
		},
		Barrier: &transport.DatabaseBarrierConfig{
			Enabled:        true,
			Mode:           "shamir",
			SharesRequired: 3,
			SharesTotal:    5,
		},
		Connection: &transport.DatabaseConnectionConfig{
			Transport: "grpc",
			Address:   "127.0.0.1:5000",
			CAFile:    "/ca",
			CertFile:  "/cert",
			KeyFile:   "/key",
		},
	})
	if err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if capturedCfg.Name != "db1" || string(capturedCfg.Engine) != "kv" {
		t.Errorf("captured = %+v", capturedCfg)
	}
	if resp.Name != "db1" || resp.ShardID != 9 {
		t.Errorf("resp = %+v", resp)
	}
	if resp.Protocol.Etcd == nil || !resp.Protocol.Etcd.Enabled {
		t.Errorf("Etcd = %+v", resp.Protocol.Etcd)
	}
	if resp.Raft == nil || resp.Raft.ReplicaID != 2 {
		t.Errorf("Raft = %+v", resp.Raft)
	}
	if resp.Barrier == nil || !resp.Barrier.Enabled {
		t.Errorf("Barrier = %+v", resp.Barrier)
	}
	if resp.Connection == nil || resp.Connection.Address != "127.0.0.1:5000" {
		t.Errorf("Connection = %+v", resp.Connection)
	}
}

func TestEmbeddedDatabase_Create_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.CreateDatabase(context.Background(), transport.CreateDatabaseRequest{})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedDatabase_Create_Error(t *testing.T) {
	svc := &mockDatabaseService{
		createFn: func(database.DatabaseConfig) (*database.DatabaseInfo, error) {
			return nil, errSentinel
		},
	}
	tr := newExtTransport(t, WithDatabaseService(svc))
	_, err := tr.CreateDatabase(context.Background(), transport.CreateDatabaseRequest{})
	if !errors.Is(err, errSentinel) {
		t.Errorf("expected errSentinel, got %v", err)
	}
}

func TestEmbeddedDatabase_Get(t *testing.T) {
	tr := newExtTransport(t, WithDatabaseService(&mockDatabaseService{}))
	resp, err := tr.GetDatabase(context.Background(), "db1")
	if err != nil {
		t.Fatalf("GetDatabase: %v", err)
	}
	if resp.Name != "db1" {
		t.Errorf("Name = %q", resp.Name)
	}
}

func TestEmbeddedDatabase_Get_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.GetDatabase(context.Background(), "db1")
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedDatabase_Get_Error(t *testing.T) {
	svc := &mockDatabaseService{
		getFn: func(string) (*database.DatabaseInfo, error) {
			return nil, errSentinel
		},
	}
	tr := newExtTransport(t, WithDatabaseService(svc))
	_, err := tr.GetDatabase(context.Background(), "x")
	if !errors.Is(err, errSentinel) {
		t.Errorf("expected errSentinel, got %v", err)
	}
}

func TestEmbeddedDatabase_List(t *testing.T) {
	svc := &mockDatabaseService{
		listFn: func() ([]database.DatabaseInfo, error) {
			return []database.DatabaseInfo{
				{Name: "a"},
				{Name: "b"},
			}, nil
		},
	}
	tr := newExtTransport(t, WithDatabaseService(svc))
	infos, err := tr.ListDatabases(context.Background())
	if err != nil {
		t.Fatalf("ListDatabases: %v", err)
	}
	if len(infos) != 2 {
		t.Errorf("len = %d", len(infos))
	}
}

func TestEmbeddedDatabase_List_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.ListDatabases(context.Background())
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedDatabase_List_Error(t *testing.T) {
	svc := &mockDatabaseService{
		listFn: func() ([]database.DatabaseInfo, error) {
			return nil, errSentinel
		},
	}
	tr := newExtTransport(t, WithDatabaseService(svc))
	_, err := tr.ListDatabases(context.Background())
	if !errors.Is(err, errSentinel) {
		t.Errorf("expected errSentinel, got %v", err)
	}
}

func TestEmbeddedDatabase_Drop(t *testing.T) {
	var captured string
	svc := &mockDatabaseService{
		dropFn: func(name string) error {
			captured = name
			return nil
		},
	}
	tr := newExtTransport(t, WithDatabaseService(svc))
	if err := tr.DropDatabase(context.Background(), "db1"); err != nil {
		t.Fatalf("DropDatabase: %v", err)
	}
	if captured != "db1" {
		t.Errorf("captured = %q", captured)
	}
}

func TestEmbeddedDatabase_Drop_NilService(t *testing.T) {
	tr := newExtTransport(t)
	err := tr.DropDatabase(context.Background(), "x")
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedDatabase_UpdateProtocol(t *testing.T) {
	tr := newExtTransport(t, WithDatabaseService(&mockDatabaseService{}))
	resp, err := tr.UpdateDatabaseProtocol(context.Background(), transport.UpdateDatabaseProtocolRequest{
		Name:       "db1",
		MySQL:      true,
		PostgreSQL: true,
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseProtocol: %v", err)
	}
	if !resp.Protocol.MySQL || !resp.Protocol.PostgreSQL {
		t.Errorf("Protocol = %+v", resp.Protocol)
	}
}

func TestEmbeddedDatabase_UpdateProtocol_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.UpdateDatabaseProtocol(context.Background(), transport.UpdateDatabaseProtocolRequest{})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedDatabase_UpdateProtocol_Error(t *testing.T) {
	svc := &mockDatabaseService{
		updateProtocolFn: func(string, database.ProtocolConfig) (*database.DatabaseInfo, error) {
			return nil, errSentinel
		},
	}
	tr := newExtTransport(t, WithDatabaseService(svc))
	_, err := tr.UpdateDatabaseProtocol(context.Background(), transport.UpdateDatabaseProtocolRequest{})
	if !errors.Is(err, errSentinel) {
		t.Errorf("expected errSentinel, got %v", err)
	}
}

func TestEmbeddedDatabase_UpdateEtcd(t *testing.T) {
	tr := newExtTransport(t, WithDatabaseService(&mockDatabaseService{}))
	resp, err := tr.UpdateDatabaseEtcd(context.Background(), transport.UpdateDatabaseEtcdRequest{
		Name:                "db1",
		Enabled:             true,
		ListenAddr:          "0.0.0.0:2379",
		AdvertiseClientURLs: []string{"http://localhost:2379"},
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseEtcd: %v", err)
	}
	if resp.Protocol.Etcd == nil || !resp.Protocol.Etcd.Enabled {
		t.Errorf("Etcd = %+v", resp.Protocol.Etcd)
	}
}

func TestEmbeddedDatabase_UpdateEtcd_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.UpdateDatabaseEtcd(context.Background(), transport.UpdateDatabaseEtcdRequest{})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedDatabase_UpdateEtcd_Error(t *testing.T) {
	svc := &mockDatabaseService{
		updateEtcdFn: func(string, *database.EtcdConfig) (*database.DatabaseInfo, error) {
			return nil, errSentinel
		},
	}
	tr := newExtTransport(t, WithDatabaseService(svc))
	_, err := tr.UpdateDatabaseEtcd(context.Background(), transport.UpdateDatabaseEtcdRequest{})
	if !errors.Is(err, errSentinel) {
		t.Errorf("expected errSentinel, got %v", err)
	}
}

func TestEmbeddedDatabase_UpdateRaft(t *testing.T) {
	tr := newExtTransport(t, WithDatabaseService(&mockDatabaseService{}))
	resp, err := tr.UpdateDatabaseRaft(context.Background(), transport.UpdateDatabaseRaftRequest{
		Name: "db1",
		Raft: &transport.DatabaseRaftConfig{
			ClusterMode: "single",
		},
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseRaft: %v", err)
	}
	if resp.Raft == nil || resp.Raft.ClusterMode != "single" {
		t.Errorf("Raft = %+v", resp.Raft)
	}
}

func TestEmbeddedDatabase_UpdateRaft_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.UpdateDatabaseRaft(context.Background(), transport.UpdateDatabaseRaftRequest{})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedDatabase_UpdateRaft_Error(t *testing.T) {
	svc := &mockDatabaseService{
		updateRaftFn: func(string, *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
			return nil, errSentinel
		},
	}
	tr := newExtTransport(t, WithDatabaseService(svc))
	_, err := tr.UpdateDatabaseRaft(context.Background(), transport.UpdateDatabaseRaftRequest{})
	if !errors.Is(err, errSentinel) {
		t.Errorf("expected errSentinel, got %v", err)
	}
}

func TestEmbeddedDatabase_UpdateBarrier(t *testing.T) {
	tr := newExtTransport(t, WithDatabaseService(&mockDatabaseService{}))
	resp, err := tr.UpdateDatabaseBarrier(context.Background(), transport.UpdateDatabaseBarrierRequest{
		Name: "db1",
		Barrier: &transport.DatabaseBarrierConfig{
			Enabled: true,
			Mode:    "shamir",
		},
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseBarrier: %v", err)
	}
	if resp.Barrier == nil || resp.Barrier.Mode != "shamir" {
		t.Errorf("Barrier = %+v", resp.Barrier)
	}
}

func TestEmbeddedDatabase_UpdateBarrier_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.UpdateDatabaseBarrier(context.Background(), transport.UpdateDatabaseBarrierRequest{})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedDatabase_UpdateBarrier_Error(t *testing.T) {
	svc := &mockDatabaseService{
		updateBarrierFn: func(string, *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
			return nil, errSentinel
		},
	}
	tr := newExtTransport(t, WithDatabaseService(svc))
	_, err := tr.UpdateDatabaseBarrier(context.Background(), transport.UpdateDatabaseBarrierRequest{})
	if !errors.Is(err, errSentinel) {
		t.Errorf("expected errSentinel, got %v", err)
	}
}

func TestEmbeddedDatabase_UpdateConnection(t *testing.T) {
	tr := newExtTransport(t, WithDatabaseService(&mockDatabaseService{}))
	resp, err := tr.UpdateDatabaseConnection(context.Background(), transport.UpdateDatabaseConnectionRequest{
		Name: "db1",
		Connection: &transport.DatabaseConnectionConfig{
			Transport: "grpc",
			Address:   "127.0.0.1:5000",
		},
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseConnection: %v", err)
	}
	if resp.Connection == nil || resp.Connection.Transport != "grpc" {
		t.Errorf("Connection = %+v", resp.Connection)
	}
}

func TestEmbeddedDatabase_UpdateConnection_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.UpdateDatabaseConnection(context.Background(), transport.UpdateDatabaseConnectionRequest{})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedDatabase_UpdateConnection_Error(t *testing.T) {
	svc := &mockDatabaseService{
		updateConnectionFn: func(string, *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
			return nil, errSentinel
		},
	}
	tr := newExtTransport(t, WithDatabaseService(svc))
	_, err := tr.UpdateDatabaseConnection(context.Background(), transport.UpdateDatabaseConnectionRequest{})
	if !errors.Is(err, errSentinel) {
		t.Errorf("expected errSentinel, got %v", err)
	}
}

// ===========================================================================
// Index transport (was 0%)
// ===========================================================================

type mockIndexService struct {
	createFn func(ctx context.Context, name, field string, cfg index.IndexConfig) error
	dropFn   func(ctx context.Context, name string) error
	queryFn  func(ctx context.Context, name string, value []byte) ([][]byte, error)
	scanFn   func(ctx context.Context, name string, start, end []byte) (map[string][][]byte, error)
	listFn   func(ctx context.Context) ([]string, error)
}

func (m *mockIndexService) CreateIndex(ctx context.Context, name, field string, cfg index.IndexConfig) error {
	if m.createFn != nil {
		return m.createFn(ctx, name, field, cfg)
	}
	return nil
}
func (m *mockIndexService) DropIndex(ctx context.Context, name string) error {
	if m.dropFn != nil {
		return m.dropFn(ctx, name)
	}
	return nil
}

// DropOwnedIndex answers as DropIndex does: this double records no
// ownership, and no test that uses it exercises an owner-checked drop.
func (m *mockIndexService) DropOwnedIndex(ctx context.Context, name, _ string) error {
	return m.DropIndex(ctx, name)
}
func (m *mockIndexService) Query(ctx context.Context, name string, value []byte) ([][]byte, error) {
	if m.queryFn != nil {
		return m.queryFn(ctx, name, value)
	}
	return nil, nil
}
func (m *mockIndexService) Scan(ctx context.Context, name string, start, end []byte) (map[string][][]byte, error) {
	if m.scanFn != nil {
		return m.scanFn(ctx, name, start, end)
	}
	return nil, nil
}
func (m *mockIndexService) ListIndexes(ctx context.Context) ([]string, error) {
	if m.listFn != nil {
		return m.listFn(ctx)
	}
	return nil, nil
}

func TestEmbeddedIndex_Create_String(t *testing.T) {
	var capturedCfg index.IndexConfig
	svc := &mockIndexService{
		createFn: func(_ context.Context, _, _ string, cfg index.IndexConfig) error {
			capturedCfg = cfg
			return nil
		},
	}
	tr := newExtTransport(t, WithIndexService(svc))
	if err := tr.IndexCreate(context.Background(), "idx", "field", transport.IndexConfig{Unique: true}); err != nil {
		t.Fatalf("IndexCreate: %v", err)
	}
	if capturedCfg.ValueType != index.IndexValueTypeString || !capturedCfg.Unique {
		t.Errorf("captured = %+v", capturedCfg)
	}
}

func TestEmbeddedIndex_Create_Numeric(t *testing.T) {
	var capturedCfg index.IndexConfig
	svc := &mockIndexService{
		createFn: func(_ context.Context, _, _ string, cfg index.IndexConfig) error {
			capturedCfg = cfg
			return nil
		},
	}
	tr := newExtTransport(t, WithIndexService(svc))
	if err := tr.IndexCreate(context.Background(), "idx", "f", transport.IndexConfig{Numeric: true, Sparse: true}); err != nil {
		t.Fatalf("IndexCreate: %v", err)
	}
	if capturedCfg.ValueType != index.IndexValueTypeNumeric || !capturedCfg.Sparse {
		t.Errorf("captured = %+v", capturedCfg)
	}
}

func TestEmbeddedIndex_Create_NilService(t *testing.T) {
	tr := newExtTransport(t)
	err := tr.IndexCreate(context.Background(), "n", "f", transport.IndexConfig{})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedIndex_Create_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithIndexService(&mockIndexService{}))
	err := tr.IndexCreate(context.Background(), "n", "f", transport.IndexConfig{})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedIndex_Drop(t *testing.T) {
	var captured string
	svc := &mockIndexService{
		dropFn: func(_ context.Context, name string) error {
			captured = name
			return nil
		},
	}
	tr := newExtTransport(t, WithIndexService(svc))
	if err := tr.IndexDrop(context.Background(), "idx"); err != nil {
		t.Fatalf("IndexDrop: %v", err)
	}
	if captured != "idx" {
		t.Errorf("captured = %q", captured)
	}
}

func TestEmbeddedIndex_Drop_NilService(t *testing.T) {
	tr := newExtTransport(t)
	err := tr.IndexDrop(context.Background(), "n")
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedIndex_Drop_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithIndexService(&mockIndexService{}))
	err := tr.IndexDrop(context.Background(), "n")
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedIndex_Query(t *testing.T) {
	svc := &mockIndexService{
		queryFn: func(_ context.Context, name string, value []byte) ([][]byte, error) {
			if name != "idx" || string(value) != "alice" {
				t.Errorf("captured (%q, %q)", name, string(value))
			}
			return [][]byte{[]byte("k1"), []byte("k2")}, nil
		},
	}
	tr := newExtTransport(t, WithIndexService(svc))
	resp, err := tr.IndexQuery(context.Background(), "idx", "alice")
	if err != nil {
		t.Fatalf("IndexQuery: %v", err)
	}
	if len(resp.Keys) != 2 || resp.Keys[0] != "k1" {
		t.Errorf("Keys = %+v", resp.Keys)
	}
}

func TestEmbeddedIndex_Query_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.IndexQuery(context.Background(), "n", "v")
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedIndex_Query_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithIndexService(&mockIndexService{}))
	_, err := tr.IndexQuery(context.Background(), "n", "v")
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedIndex_Query_Error(t *testing.T) {
	svc := &mockIndexService{
		queryFn: func(context.Context, string, []byte) ([][]byte, error) {
			return nil, errSentinel
		},
	}
	tr := newExtTransport(t, WithIndexService(svc))
	_, err := tr.IndexQuery(context.Background(), "n", "v")
	if !errors.Is(err, errSentinel) {
		t.Errorf("expected errSentinel, got %v", err)
	}
}

func TestEmbeddedIndex_Scan(t *testing.T) {
	svc := &mockIndexService{
		scanFn: func(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
			return map[string][][]byte{
				"bbb": {[]byte("k2")},
				"aaa": {[]byte("k1")},
			}, nil
		},
	}
	tr := newExtTransport(t, WithIndexService(svc))
	resp, err := tr.IndexScan(context.Background(), "idx", spIdx("a"), spIdx("c"))
	if err != nil {
		t.Fatalf("IndexScan: %v", err)
	}
	if len(resp.Entries) != 2 {
		t.Errorf("len = %d", len(resp.Entries))
	}
	// Verify sorted output.
	if resp.Entries[0].Value != "aaa" || resp.Entries[1].Value != "bbb" {
		t.Errorf("Entries = %+v", resp.Entries)
	}
}

func TestEmbeddedIndex_Scan_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.IndexScan(context.Background(), "n", spIdx("a"), spIdx("z"))
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedIndex_Scan_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithIndexService(&mockIndexService{}))
	_, err := tr.IndexScan(context.Background(), "n", spIdx("a"), spIdx("z"))
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedIndex_Scan_Error(t *testing.T) {
	svc := &mockIndexService{
		scanFn: func(context.Context, string, []byte, []byte) (map[string][][]byte, error) {
			return nil, errSentinel
		},
	}
	tr := newExtTransport(t, WithIndexService(svc))
	_, err := tr.IndexScan(context.Background(), "n", spIdx("a"), spIdx("z"))
	if !errors.Is(err, errSentinel) {
		t.Errorf("expected errSentinel, got %v", err)
	}
}

func TestEmbeddedIndex_List(t *testing.T) {
	svc := &mockIndexService{
		listFn: func(context.Context) ([]string, error) {
			return []string{"i1", "i2"}, nil
		},
	}
	tr := newExtTransport(t, WithIndexService(svc))
	resp, err := tr.IndexList(context.Background())
	if err != nil {
		t.Fatalf("IndexList: %v", err)
	}
	if len(resp.Names) != 2 {
		t.Errorf("len = %d", len(resp.Names))
	}
}

func TestEmbeddedIndex_List_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.IndexList(context.Background())
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedIndex_List_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithIndexService(&mockIndexService{}))
	_, err := tr.IndexList(context.Background())
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedIndex_List_Error(t *testing.T) {
	svc := &mockIndexService{
		listFn: func(context.Context) ([]string, error) {
			return nil, errSentinel
		},
	}
	tr := newExtTransport(t, WithIndexService(svc))
	_, err := tr.IndexList(context.Background())
	if !errors.Is(err, errSentinel) {
		t.Errorf("expected errSentinel, got %v", err)
	}
}

// Dispatch tests for Index methods.
func TestEmbeddedIndex_Dispatch_Create(t *testing.T) {
	tr := newExtTransport(t, WithIndexService(&mockIndexService{}))
	if err := tr.Request(context.Background(), "IndexCreate",
		&indexCreateRequest{Name: "i", FieldName: "f"}, nil); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
}

func TestEmbeddedIndex_Dispatch_Create_BadReq(t *testing.T) {
	tr := newExtTransport(t, WithIndexService(&mockIndexService{}))
	err := tr.Request(context.Background(), "IndexCreate", "bad", nil)
	var invErr *InvalidRequestError
	if !errors.As(err, &invErr) {
		t.Fatalf("expected InvalidRequestError, got %T", err)
	}
}

func TestEmbeddedIndex_Dispatch_Drop(t *testing.T) {
	tr := newExtTransport(t, WithIndexService(&mockIndexService{}))
	if err := tr.Request(context.Background(), "IndexDrop", &indexDropRequest{Name: "i"}, nil); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
}

func TestEmbeddedIndex_Dispatch_Drop_BadReq(t *testing.T) {
	tr := newExtTransport(t, WithIndexService(&mockIndexService{}))
	err := tr.Request(context.Background(), "IndexDrop", 1, nil)
	var invErr *InvalidRequestError
	if !errors.As(err, &invErr) {
		t.Fatalf("expected InvalidRequestError, got %T", err)
	}
}

func TestEmbeddedIndex_Dispatch_Query(t *testing.T) {
	tr := newExtTransport(t, WithIndexService(&mockIndexService{}))
	var resp *transport.IndexQueryResponse
	if err := tr.Request(context.Background(), "IndexQuery",
		&indexQueryRequest{IndexName: "i", Value: "v"}, &resp); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
}

func TestEmbeddedIndex_Dispatch_Query_BadReq(t *testing.T) {
	tr := newExtTransport(t, WithIndexService(&mockIndexService{}))
	err := tr.Request(context.Background(), "IndexQuery", 1, nil)
	var invErr *InvalidRequestError
	if !errors.As(err, &invErr) {
		t.Fatalf("expected InvalidRequestError, got %T", err)
	}
}

func TestEmbeddedIndex_Dispatch_Scan(t *testing.T) {
	tr := newExtTransport(t, WithIndexService(&mockIndexService{}))
	var resp *transport.IndexScanResponse
	if err := tr.Request(context.Background(), "IndexScan",
		&indexScanRequest{IndexName: "i", Start: spIdx("a"), End: spIdx("z")}, &resp); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
}

func TestEmbeddedIndex_Dispatch_Scan_BadReq(t *testing.T) {
	tr := newExtTransport(t, WithIndexService(&mockIndexService{}))
	err := tr.Request(context.Background(), "IndexScan", 1, nil)
	var invErr *InvalidRequestError
	if !errors.As(err, &invErr) {
		t.Fatalf("expected InvalidRequestError, got %T", err)
	}
}

func TestEmbeddedIndex_Dispatch_List(t *testing.T) {
	tr := newExtTransport(t, WithIndexService(&mockIndexService{}))
	var resp *transport.IndexListResponse
	if err := tr.Request(context.Background(), "IndexList", nil, &resp); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
}

func (m *mockTenantService) CheckBarrierMode(_ context.Context, _ string, _ seal.StrategyID) error {
	return nil
}

func spIdx(s string) *string { return &s }
