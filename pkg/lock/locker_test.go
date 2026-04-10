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

package lock

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// ---------------------------------------------------------------------------
// Mock implementations
// ---------------------------------------------------------------------------

// mockTransactionService implements TransactionServicer.
type mockTransactionService struct {
	executeFn func(ctx context.Context, compares []Compare, successOps, failureOps []Op) (*TransactionResponse, error)
}

func (m *mockTransactionService) Execute(ctx context.Context, compares []Compare, successOps, failureOps []Op) (*TransactionResponse, error) {
	if m.executeFn != nil {
		return m.executeFn(ctx, compares, successOps, failureOps)
	}
	return &TransactionResponse{Succeeded: true, Revision: 1}, nil
}

// mockLeaseService implements LeaseServicer.
type mockLeaseService struct {
	grantFn     func(ctx context.Context, ttl int64) (*lease.Lease, error)
	revokeFn    func(ctx context.Context, leaseID int64) error
	keepAliveFn func(ctx context.Context, leaseID int64) (*lease.Lease, error)
}

func (m *mockLeaseService) Grant(ctx context.Context, ttl int64) (*lease.Lease, error) {
	if m.grantFn != nil {
		return m.grantFn(ctx, ttl)
	}
	return &lease.Lease{ID: 1, TTL: ttl, GrantedTTL: ttl}, nil
}

func (m *mockLeaseService) Revoke(ctx context.Context, leaseID int64) error {
	if m.revokeFn != nil {
		return m.revokeFn(ctx, leaseID)
	}
	return nil
}

func (m *mockLeaseService) KeepAlive(ctx context.Context, leaseID int64) (*lease.Lease, error) {
	if m.keepAliveFn != nil {
		return m.keepAliveFn(ctx, leaseID)
	}
	return &lease.Lease{ID: leaseID, TTL: 30, GrantedTTL: 30}, nil
}

// mockWatchService implements WatchServicer.
type mockWatchService struct {
	watchFn func(ctx context.Context, key string, opts ...watch.WatchOption) (watch.Watcher, error)
}

func (m *mockWatchService) Watch(ctx context.Context, key string, opts ...watch.WatchOption) (watch.Watcher, error) {
	if m.watchFn != nil {
		return m.watchFn(ctx, key, opts...)
	}
	return &mockWatcher{ch: make(chan watch.WatchResponse)}, nil
}

// mockWatcher implements watch.Watcher.
type mockWatcher struct {
	ch      chan watch.WatchResponse
	closeFn func() error
	err     error
}

func (w *mockWatcher) Events() <-chan watch.WatchResponse { return w.ch }
func (w *mockWatcher) WatchID() int64                     { return 1 }
func (w *mockWatcher) Err() error                         { return w.err }
func (w *mockWatcher) Close() error {
	if w.closeFn != nil {
		return w.closeFn()
	}
	return nil
}

// mockMVCCKVService implements MVCCKVServicer.
type mockMVCCKVService struct {
	getRevisionFn func(ctx context.Context, key string) (*kv.KeyValue, error)
	rangeFn       func(ctx context.Context, start, end []byte, opts ...RangeOption) ([]*kv.KeyValue, int64, error)
}

func (m *mockMVCCKVService) GetRevision(ctx context.Context, key string) (*kv.KeyValue, error) {
	if m.getRevisionFn != nil {
		return m.getRevisionFn(ctx, key)
	}
	return nil, errors.New("key not found")
}

func (m *mockMVCCKVService) Range(ctx context.Context, start, end []byte, opts ...RangeOption) ([]*kv.KeyValue, int64, error) {
	if m.rangeFn != nil {
		return m.rangeFn(ctx, start, end, opts...)
	}
	return nil, 0, nil
}

// ---------------------------------------------------------------------------
// Helper constructors
// ---------------------------------------------------------------------------

func defaultServices() (TransactionServicer, LeaseServicer, WatchServicer, MVCCKVServicer) {
	return &mockTransactionService{}, &mockLeaseService{}, &mockWatchService{}, &mockMVCCKVService{}
}

func mustNewLocker(t *testing.T, tx TransactionServicer, ls LeaseServicer, ws WatchServicer, kvs MVCCKVServicer) *Service {
	t.Helper()
	svc, err := NewLocker(tx, ls, ws, kvs, nil)
	if err != nil {
		t.Fatalf("NewLocker: unexpected error: %v", err)
	}
	return svc
}

func encodeLockValue(t *testing.T, lv lockValue) []byte {
	t.Helper()
	b, err := json.Marshal(lv)
	if err != nil {
		t.Fatalf("encodeLockValue: %v", err)
	}
	return b
}

// ---------------------------------------------------------------------------
// NewLocker
// ---------------------------------------------------------------------------

func TestNewLocker_NilTxService(t *testing.T) {
	_, ls, ws, kvs := defaultServices()
	_, err := NewLocker(nil, ls, ws, kvs, nil)
	if !errors.Is(err, ErrNilTxService) {
		t.Errorf("expected ErrNilTxService, got %v", err)
	}
}

func TestNewLocker_NilLeaseService(t *testing.T) {
	tx, _, ws, kvs := defaultServices()
	_, err := NewLocker(tx, nil, ws, kvs, nil)
	if !errors.Is(err, ErrNilLeaseService) {
		t.Errorf("expected ErrNilLeaseService, got %v", err)
	}
}

func TestNewLocker_NilWatchService(t *testing.T) {
	tx, ls, _, kvs := defaultServices()
	_, err := NewLocker(tx, ls, nil, kvs, nil)
	if !errors.Is(err, ErrNilWatchService) {
		t.Errorf("expected ErrNilWatchService, got %v", err)
	}
}

func TestNewLocker_NilKVService(t *testing.T) {
	tx, ls, ws, _ := defaultServices()
	_, err := NewLocker(tx, ls, ws, nil, nil)
	if !errors.Is(err, ErrNilKVService) {
		t.Errorf("expected ErrNilKVService, got %v", err)
	}
}

func TestNewLocker_AllServicesValid(t *testing.T) {
	tx, ls, ws, kvs := defaultServices()
	svc, err := NewLocker(tx, ls, ws, kvs, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil Service")
	}
}

// ---------------------------------------------------------------------------
// TryLock
// ---------------------------------------------------------------------------

func newDefaultLocker(t *testing.T) *Service {
	t.Helper()
	tx, ls, ws, kvs := defaultServices()
	return mustNewLocker(t, tx, ls, ws, kvs)
}

func TestTryLock_EmptyKey(t *testing.T) {
	svc := newDefaultLocker(t)
	_, err := svc.TryLock(context.Background(), LockOptions{Key: "", TTL: time.Second})
	if !errors.Is(err, ErrEmptyKey) {
		t.Errorf("expected ErrEmptyKey, got %v", err)
	}
}

func TestTryLock_ZeroTTL(t *testing.T) {
	svc := newDefaultLocker(t)
	_, err := svc.TryLock(context.Background(), LockOptions{Key: "k", TTL: 0})
	if !errors.Is(err, ErrInvalidTTL) {
		t.Errorf("expected ErrInvalidTTL, got %v", err)
	}
}

func TestTryLock_NegativeTTL(t *testing.T) {
	svc := newDefaultLocker(t)
	_, err := svc.TryLock(context.Background(), LockOptions{Key: "k", TTL: -time.Second})
	if !errors.Is(err, ErrInvalidTTL) {
		t.Errorf("expected ErrInvalidTTL, got %v", err)
	}
}

func TestTryLock_AutoGeneratesHolderID(t *testing.T) {
	var capturedOps []Op
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, successOps, _ []Op) (*TransactionResponse, error) {
			capturedOps = successOps
			return &TransactionResponse{Succeeded: true, Revision: 5}, nil
		},
	}
	svc := mustNewLocker(t, tx, &mockLeaseService{}, &mockWatchService{}, &mockMVCCKVService{})

	lock, err := svc.TryLock(context.Background(), LockOptions{Key: "res", HolderID: "", TTL: 10 * time.Second})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lock.HolderID == "" {
		t.Error("expected auto-generated HolderID, got empty string")
	}

	// The holder ID stored in the KV value must match the returned lock.
	if len(capturedOps) == 0 {
		t.Fatal("no success ops captured")
	}
	var lv lockValue
	if err := json.Unmarshal(capturedOps[0].Value, &lv); err != nil {
		t.Fatalf("failed to unmarshal lock value: %v", err)
	}
	if lv.HolderID != lock.HolderID {
		t.Errorf("stored HolderID %q != returned HolderID %q", lv.HolderID, lock.HolderID)
	}
}

func TestTryLock_Success(t *testing.T) {
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			return &TransactionResponse{Succeeded: true, Revision: 42}, nil
		},
	}
	ls := &mockLeaseService{
		grantFn: func(_ context.Context, ttl int64) (*lease.Lease, error) {
			return &lease.Lease{ID: 7, TTL: ttl, GrantedTTL: ttl}, nil
		},
	}
	svc := mustNewLocker(t, tx, ls, &mockWatchService{}, &mockMVCCKVService{})

	lock, err := svc.TryLock(context.Background(), LockOptions{Key: "mykey", HolderID: "h1", TTL: 30 * time.Second})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lock.Key != "mykey" {
		t.Errorf("Lock.Key = %q, want %q", lock.Key, "mykey")
	}
	if lock.HolderID != "h1" {
		t.Errorf("Lock.HolderID = %q, want %q", lock.HolderID, "h1")
	}
	if lock.LeaseID != 7 {
		t.Errorf("Lock.LeaseID = %d, want 7", lock.LeaseID)
	}
	if lock.AcquireCount != 1 {
		t.Errorf("Lock.AcquireCount = %d, want 1", lock.AcquireCount)
	}
}

func TestTryLock_FencingToken_IsRevision(t *testing.T) {
	const wantRevision int64 = 999
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			return &TransactionResponse{Succeeded: true, Revision: wantRevision}, nil
		},
	}
	svc := mustNewLocker(t, tx, &mockLeaseService{}, &mockWatchService{}, &mockMVCCKVService{})

	lock, err := svc.TryLock(context.Background(), LockOptions{Key: "k", HolderID: "h", TTL: time.Second})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lock.FencingToken != wantRevision {
		t.Errorf("FencingToken = %d, want %d", lock.FencingToken, wantRevision)
	}
}

func TestTryLock_KeyHeld_DifferentHolder(t *testing.T) {
	existingLV := lockValue{HolderID: "other-holder", Reentrant: false, AcquireCount: 1, LeaseID: 5, TTL: 30}
	existingBytes := encodeLockValue(t, existingLV)

	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			return &TransactionResponse{Succeeded: false, Revision: 10}, nil
		},
	}
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Key: []byte(lockKey("res")), Value: existingBytes, ModRevision: 10}, nil
		},
	}
	svc := mustNewLocker(t, tx, ls(), &mockWatchService{}, kvs)

	_, err := svc.TryLock(context.Background(), LockOptions{Key: "res", HolderID: "my-holder", TTL: time.Second})
	var held *LockHeldError
	if !errors.As(err, &held) {
		t.Fatalf("expected LockHeldError, got %v", err)
	}
	if held.HolderID != "other-holder" {
		t.Errorf("LockHeldError.HolderID = %q, want %q", held.HolderID, "other-holder")
	}
	if held.Key != "res" {
		t.Errorf("LockHeldError.Key = %q, want %q", held.Key, "res")
	}
}

func TestTryLock_ReentrantSameHolder_IncrementsCount(t *testing.T) {
	existingLV := lockValue{HolderID: "same-holder", Reentrant: true, AcquireCount: 1, LeaseID: 5, TTL: 30}
	existingBytes := encodeLockValue(t, existingLV)

	callCount := 0
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			callCount++
			if callCount == 1 {
				// First call: CAS fails -- key exists.
				return &TransactionResponse{Succeeded: false, Revision: 10}, nil
			}
			// Second call: unconditional put for increment.
			return &TransactionResponse{Succeeded: true, Revision: 11}, nil
		},
	}
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Key: []byte(lockKey("res")), Value: existingBytes, ModRevision: 10}, nil
		},
	}
	svc := mustNewLocker(t, tx, &mockLeaseService{}, &mockWatchService{}, kvs)

	lock, err := svc.TryLock(context.Background(), LockOptions{Key: "res", HolderID: "same-holder", TTL: time.Second, Reentrant: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lock.AcquireCount != 2 {
		t.Errorf("AcquireCount = %d, want 2", lock.AcquireCount)
	}
}

func TestTryLock_NonReentrantSameHolder_ReturnsHeld(t *testing.T) {
	existingLV := lockValue{HolderID: "same-holder", Reentrant: false, AcquireCount: 1, LeaseID: 5, TTL: 30}
	existingBytes := encodeLockValue(t, existingLV)

	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			return &TransactionResponse{Succeeded: false}, nil
		},
	}
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Key: []byte(lockKey("res")), Value: existingBytes, ModRevision: 5}, nil
		},
	}
	svc := mustNewLocker(t, tx, &mockLeaseService{}, &mockWatchService{}, kvs)

	_, err := svc.TryLock(context.Background(), LockOptions{Key: "res", HolderID: "same-holder", TTL: time.Second, Reentrant: false})
	var held *LockHeldError
	if !errors.As(err, &held) {
		t.Fatalf("expected LockHeldError for non-reentrant same holder, got %v", err)
	}
}

func TestTryLock_ReentrantCallerButNonReentrantLock_ReturnsHeld(t *testing.T) {
	// The existing lock was acquired as non-reentrant. Even when the same
	// holder requests Reentrant: true, the lock must refuse re-entry because
	// the original acquisition did not enable reentrancy.
	existingLV := lockValue{HolderID: "same-holder", Reentrant: false, AcquireCount: 1, LeaseID: 5, TTL: 30}
	existingBytes := encodeLockValue(t, existingLV)

	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			return &TransactionResponse{Succeeded: false}, nil
		},
	}
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Key: []byte(lockKey("res")), Value: existingBytes, ModRevision: 5}, nil
		},
	}
	svc := mustNewLocker(t, tx, &mockLeaseService{}, &mockWatchService{}, kvs)

	_, err := svc.TryLock(context.Background(), LockOptions{Key: "res", HolderID: "same-holder", TTL: time.Second, Reentrant: true})
	var held *LockHeldError
	if !errors.As(err, &held) {
		t.Fatalf("expected LockHeldError when existing lock is non-reentrant, got %v", err)
	}
	if held.HolderID != "same-holder" {
		t.Errorf("LockHeldError.HolderID = %q, want %q", held.HolderID, "same-holder")
	}
}

func TestTryLock_NonReentrantCallerButReentrantLock_ReturnsHeld(t *testing.T) {
	// The existing lock was acquired as reentrant but the caller does not
	// request reentrancy. Re-entry must be refused.
	existingLV := lockValue{HolderID: "same-holder", Reentrant: true, AcquireCount: 1, LeaseID: 5, TTL: 30}
	existingBytes := encodeLockValue(t, existingLV)

	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			return &TransactionResponse{Succeeded: false}, nil
		},
	}
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Key: []byte(lockKey("res")), Value: existingBytes, ModRevision: 5}, nil
		},
	}
	svc := mustNewLocker(t, tx, &mockLeaseService{}, &mockWatchService{}, kvs)

	_, err := svc.TryLock(context.Background(), LockOptions{Key: "res", HolderID: "same-holder", TTL: time.Second, Reentrant: false})
	var held *LockHeldError
	if !errors.As(err, &held) {
		t.Fatalf("expected LockHeldError when caller does not request reentrancy, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Lock (blocking)
// ---------------------------------------------------------------------------

func TestLock_ImmediateSuccess(t *testing.T) {
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			return &TransactionResponse{Succeeded: true, Revision: 3}, nil
		},
	}
	svc := mustNewLocker(t, tx, &mockLeaseService{}, &mockWatchService{}, &mockMVCCKVService{})

	lock, err := svc.Lock(context.Background(), LockOptions{Key: "k", HolderID: "h", TTL: time.Second})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lock.FencingToken != 3 {
		t.Errorf("FencingToken = %d, want 3", lock.FencingToken)
	}
}

func TestLock_BlocksAndAcquiresOnRelease(t *testing.T) {
	existingLV := lockValue{HolderID: "other", Reentrant: false, AcquireCount: 1, LeaseID: 5, TTL: 30}
	existingBytes := encodeLockValue(t, existingLV)

	// Track how many times Execute is called.
	callCount := 0
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			callCount++
			if callCount == 1 {
				return &TransactionResponse{Succeeded: false}, nil
			}
			// Second attempt (after delete event) succeeds.
			return &TransactionResponse{Succeeded: true, Revision: 20}, nil
		},
	}
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Value: existingBytes, ModRevision: 5}, nil
		},
	}

	// Deliver a delete event via the watcher.
	watchCh := make(chan watch.WatchResponse, 1)
	watchCh <- watch.WatchResponse{
		Events: []*watch.Event{
			{Type: watch.EventDelete, Kv: &kv.KeyValue{Key: []byte(lockKey("k"))}},
		},
	}
	ws := &mockWatchService{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return &mockWatcher{ch: watchCh}, nil
		},
	}
	svc := mustNewLocker(t, tx, &mockLeaseService{}, ws, kvs)

	lock, err := svc.Lock(context.Background(), LockOptions{Key: "k", HolderID: "me", TTL: time.Second})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lock.FencingToken != 20 {
		t.Errorf("FencingToken = %d, want 20", lock.FencingToken)
	}
}

func TestLock_ContextCancelled(t *testing.T) {
	existingLV := lockValue{HolderID: "other", AcquireCount: 1}
	existingBytes := encodeLockValue(t, existingLV)

	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			return &TransactionResponse{Succeeded: false}, nil
		},
	}
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Value: existingBytes, ModRevision: 1}, nil
		},
	}

	// Watcher that never delivers events.
	ws := &mockWatchService{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return &mockWatcher{ch: make(chan watch.WatchResponse)}, nil
		},
	}
	svc := mustNewLocker(t, tx, &mockLeaseService{}, ws, kvs)

	ctx, cancel := context.WithCancel(context.Background())

	// Use a channel to coordinate cancellation instead of time.Sleep.
	ready := make(chan struct{})
	go func() {
		close(ready)
		cancel()
	}()
	<-ready

	_, err := svc.Lock(ctx, LockOptions{Key: "k", HolderID: "me", TTL: time.Second})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Unlock
// ---------------------------------------------------------------------------

func TestUnlock_Success(t *testing.T) {
	lv := lockValue{HolderID: "h1", LeaseID: 9, TTL: 30, AcquireCount: 1}
	lvBytes := encodeLockValue(t, lv)

	var revokedID int64
	ls := &mockLeaseService{
		revokeFn: func(_ context.Context, leaseID int64) error {
			revokedID = leaseID
			return nil
		},
	}

	deleteCalled := false
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, successOps, _ []Op) (*TransactionResponse, error) {
			if len(successOps) > 0 && successOps[0].Type == OpDelete {
				deleteCalled = true
			}
			return &TransactionResponse{Succeeded: true, Revision: 15}, nil
		},
	}
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Value: lvBytes, ModRevision: 10}, nil
		},
	}
	svc := mustNewLocker(t, tx, ls, &mockWatchService{}, kvs)

	err := svc.Unlock(context.Background(), "res", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !deleteCalled {
		t.Error("expected delete op to be executed")
	}
	if revokedID != 9 {
		t.Errorf("revoked leaseID = %d, want 9", revokedID)
	}
}

func TestUnlock_StaleToken(t *testing.T) {
	lv := lockValue{HolderID: "h1", LeaseID: 9, TTL: 30, AcquireCount: 1}
	lvBytes := encodeLockValue(t, lv)

	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Value: lvBytes, ModRevision: 10}, nil
		},
	}
	svc := mustNewLocker(t, &mockTransactionService{}, &mockLeaseService{}, &mockWatchService{}, kvs)

	err := svc.Unlock(context.Background(), "res", 99 /* wrong token */)
	var notHolder *NotLockHolderError
	if !errors.As(err, &notHolder) {
		t.Fatalf("expected NotLockHolderError, got %v", err)
	}
	if notHolder.Key != "res" {
		t.Errorf("Key = %q, want %q", notHolder.Key, "res")
	}
}

func TestUnlock_NotFound(t *testing.T) {
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return nil, errors.New("key not found: " + lockKey("missing"))
		},
	}
	svc := mustNewLocker(t, &mockTransactionService{}, &mockLeaseService{}, &mockWatchService{}, kvs)

	err := svc.Unlock(context.Background(), "missing", 1)
	var notFound *LockNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected LockNotFoundError, got %v", err)
	}
	if notFound.Key != "missing" {
		t.Errorf("Key = %q, want %q", notFound.Key, "missing")
	}
}

func TestUnlock_ReentrantDecrement(t *testing.T) {
	lv := lockValue{HolderID: "h1", Reentrant: true, AcquireCount: 2, LeaseID: 9, TTL: 30}
	lvBytes := encodeLockValue(t, lv)

	var storedValue []byte
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, successOps, _ []Op) (*TransactionResponse, error) {
			if len(successOps) > 0 {
				storedValue = successOps[0].Value
			}
			return &TransactionResponse{Succeeded: true, Revision: 11}, nil
		},
	}
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Value: lvBytes, ModRevision: 10}, nil
		},
	}
	svc := mustNewLocker(t, tx, &mockLeaseService{}, &mockWatchService{}, kvs)

	err := svc.Unlock(context.Background(), "res", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if storedValue == nil {
		t.Fatal("expected updated value to be stored")
	}
	var updated lockValue
	if err := json.Unmarshal(storedValue, &updated); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if updated.AcquireCount != 1 {
		t.Errorf("AcquireCount after decrement = %d, want 1", updated.AcquireCount)
	}
}

// ---------------------------------------------------------------------------
// Extend
// ---------------------------------------------------------------------------

func TestExtend_Success(t *testing.T) {
	lv := lockValue{HolderID: "h1", LeaseID: 7, TTL: 30, AcquireCount: 1, FencingToken: 5}
	lvBytes := encodeLockValue(t, lv)

	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Value: lvBytes, ModRevision: 5}, nil
		},
	}
	leaseService := &mockLeaseService{
		keepAliveFn: func(_ context.Context, leaseID int64) (*lease.Lease, error) {
			return &lease.Lease{ID: leaseID, TTL: 30, GrantedTTL: 30}, nil
		},
	}
	svc := mustNewLocker(t, &mockTransactionService{}, leaseService, &mockWatchService{}, kvs)

	extended, err := svc.Extend(context.Background(), "res", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if extended.HolderID != "h1" {
		t.Errorf("HolderID = %q, want %q", extended.HolderID, "h1")
	}
	if extended.FencingToken != 5 {
		t.Errorf("FencingToken = %d, want 5", extended.FencingToken)
	}
}

func TestExtend_StaleToken(t *testing.T) {
	lv := lockValue{HolderID: "h1", LeaseID: 7, TTL: 30, AcquireCount: 1}
	lvBytes := encodeLockValue(t, lv)

	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Value: lvBytes, ModRevision: 5}, nil
		},
	}
	svc := mustNewLocker(t, &mockTransactionService{}, &mockLeaseService{}, &mockWatchService{}, kvs)

	_, err := svc.Extend(context.Background(), "res", 99 /* wrong token */)
	var notHolder *NotLockHolderError
	if !errors.As(err, &notHolder) {
		t.Fatalf("expected NotLockHolderError, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Status
// ---------------------------------------------------------------------------

func TestStatus_Locked(t *testing.T) {
	lv := lockValue{HolderID: "h1", LeaseID: 3, TTL: 60, AcquireCount: 1, AcquiredAt: 12345, FencingToken: 7}
	lvBytes := encodeLockValue(t, lv)

	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Value: lvBytes, ModRevision: 7}, nil
		},
	}
	svc := mustNewLocker(t, &mockTransactionService{}, &mockLeaseService{}, &mockWatchService{}, kvs)

	lock, err := svc.Status(context.Background(), "mykey")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lock == nil {
		t.Fatal("expected non-nil Lock")
	}
	if lock.HolderID != "h1" {
		t.Errorf("HolderID = %q, want %q", lock.HolderID, "h1")
	}
	if lock.FencingToken != 7 {
		t.Errorf("FencingToken = %d, want 7", lock.FencingToken)
	}
	if lock.AcquiredAt != 12345 {
		t.Errorf("AcquiredAt = %d, want 12345", lock.AcquiredAt)
	}
}

func TestStatus_NotLocked(t *testing.T) {
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return nil, errors.New("key not found: k")
		},
	}
	svc := mustNewLocker(t, &mockTransactionService{}, &mockLeaseService{}, &mockWatchService{}, kvs)

	lock, err := svc.Status(context.Background(), "k")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lock != nil {
		t.Errorf("expected nil Lock for unlocked key, got %+v", lock)
	}
}

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

func TestList_MultipleLocks(t *testing.T) {
	lv1 := lockValue{HolderID: "h1", LeaseID: 1, TTL: 30, AcquireCount: 1}
	lv2 := lockValue{HolderID: "h2", LeaseID: 2, TTL: 60, AcquireCount: 1}
	b1 := encodeLockValue(t, lv1)
	b2 := encodeLockValue(t, lv2)

	kvs := &mockMVCCKVService{
		rangeFn: func(_ context.Context, _, _ []byte, _ ...RangeOption) ([]*kv.KeyValue, int64, error) {
			return []*kv.KeyValue{
				{Key: []byte(lockKeyPrefix + "res1"), Value: b1, ModRevision: 10},
				{Key: []byte(lockKeyPrefix + "res2"), Value: b2, ModRevision: 20},
			}, 2, nil
		},
	}
	svc := mustNewLocker(t, &mockTransactionService{}, &mockLeaseService{}, &mockWatchService{}, kvs)

	locks, err := svc.List(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(locks) != 2 {
		t.Fatalf("expected 2 locks, got %d", len(locks))
	}
}

func TestList_PrefixFilter(t *testing.T) {
	lv := lockValue{HolderID: "h1", LeaseID: 1, TTL: 30, AcquireCount: 1}
	b := encodeLockValue(t, lv)

	var capturedStart, capturedEnd []byte
	kvs := &mockMVCCKVService{
		rangeFn: func(_ context.Context, start, end []byte, _ ...RangeOption) ([]*kv.KeyValue, int64, error) {
			capturedStart = start
			capturedEnd = end
			return []*kv.KeyValue{
				{Key: []byte(lockKeyPrefix + "db/table"), Value: b, ModRevision: 5},
			}, 1, nil
		},
	}
	svc := mustNewLocker(t, &mockTransactionService{}, &mockLeaseService{}, &mockWatchService{}, kvs)

	locks, err := svc.List(context.Background(), "db/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(locks) != 1 {
		t.Fatalf("expected 1 lock, got %d", len(locks))
	}
	// The start key must include the internal prefix plus the user prefix.
	wantStart := lockKeyPrefix + "db/"
	if string(capturedStart) != wantStart {
		t.Errorf("range start = %q, want %q", capturedStart, wantStart)
	}
	_ = capturedEnd // end is computed by prefixEnd; just verify it is non-nil.
	if capturedEnd == nil {
		t.Error("range end should be non-nil for non-0xFF prefix")
	}
	if locks[0].Key != "db/table" {
		t.Errorf("Lock.Key = %q, want %q", locks[0].Key, "db/table")
	}
}

func TestList_EmptyResult(t *testing.T) {
	kvs := &mockMVCCKVService{
		rangeFn: func(_ context.Context, _, _ []byte, _ ...RangeOption) ([]*kv.KeyValue, int64, error) {
			return nil, 0, nil
		},
	}
	svc := mustNewLocker(t, &mockTransactionService{}, &mockLeaseService{}, &mockWatchService{}, kvs)

	locks, err := svc.List(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(locks) != 0 {
		t.Errorf("expected empty slice, got %d locks", len(locks))
	}
}

// ---------------------------------------------------------------------------
// Additional branch coverage
// ---------------------------------------------------------------------------

func TestTryLock_LeaseGrantError(t *testing.T) {
	grantErr := errors.New("grant failed")
	leaseService := &mockLeaseService{
		grantFn: func(_ context.Context, _ int64) (*lease.Lease, error) {
			return nil, grantErr
		},
	}
	svc := mustNewLocker(t, &mockTransactionService{}, leaseService, &mockWatchService{}, &mockMVCCKVService{})
	_, err := svc.TryLock(context.Background(), LockOptions{Key: "k", HolderID: "h", TTL: time.Second})
	if err == nil {
		t.Fatal("expected error from Grant, got nil")
	}
}

func TestTryLock_TransactionError(t *testing.T) {
	txErr := errors.New("tx error")
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			return nil, txErr
		},
	}
	svc := mustNewLocker(t, tx, &mockLeaseService{}, &mockWatchService{}, &mockMVCCKVService{})
	_, err := svc.TryLock(context.Background(), LockOptions{Key: "k", HolderID: "h", TTL: time.Second})
	if err == nil {
		t.Fatal("expected error from Execute, got nil")
	}
}

func TestTryLock_CASFails_GetRevisionError(t *testing.T) {
	getErr := errors.New("read error")
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			return &TransactionResponse{Succeeded: false}, nil
		},
	}
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return nil, getErr
		},
	}
	svc := mustNewLocker(t, tx, &mockLeaseService{}, &mockWatchService{}, kvs)
	_, err := svc.TryLock(context.Background(), LockOptions{Key: "k", HolderID: "h", TTL: time.Second})
	if err == nil {
		t.Fatal("expected error from GetRevision, got nil")
	}
}

func TestTryLock_ReentrantUpdate_ExecuteError(t *testing.T) {
	existingLV := lockValue{HolderID: "h1", Reentrant: true, AcquireCount: 1, LeaseID: 5, TTL: 30}
	existingBytes := encodeLockValue(t, existingLV)

	callCount := 0
	updateErr := errors.New("update failed")
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			callCount++
			if callCount == 1 {
				return &TransactionResponse{Succeeded: false}, nil
			}
			return nil, updateErr
		},
	}
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Value: existingBytes, ModRevision: 10}, nil
		},
	}
	svc := mustNewLocker(t, tx, &mockLeaseService{}, &mockWatchService{}, kvs)
	_, err := svc.TryLock(context.Background(), LockOptions{Key: "k", HolderID: "h1", TTL: time.Second, Reentrant: true})
	if err == nil {
		t.Fatal("expected error from reentrant update Execute, got nil")
	}
}

func TestLock_NonLockHeldError_Propagates(t *testing.T) {
	otherErr := errors.New("some other error")
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			return nil, otherErr
		},
	}
	svc := mustNewLocker(t, tx, &mockLeaseService{}, &mockWatchService{}, &mockMVCCKVService{})
	_, err := svc.Lock(context.Background(), LockOptions{Key: "k", HolderID: "h", TTL: time.Second})
	if err == nil {
		t.Fatal("expected error to be propagated")
	}
}

func TestLock_WatcherClosedRetryFails(t *testing.T) {
	existingLV := lockValue{HolderID: "other", AcquireCount: 1}
	existingBytes := encodeLockValue(t, existingLV)

	callCount := 0
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			callCount++
			if callCount == 1 {
				return &TransactionResponse{Succeeded: false}, nil
			}
			// Retry after watcher closed also fails.
			return nil, errors.New("retry error")
		},
	}
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Value: existingBytes, ModRevision: 1}, nil
		},
	}

	// Watcher that is closed immediately (channel already closed).
	closedCh := make(chan watch.WatchResponse)
	close(closedCh)
	ws := &mockWatchService{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return &mockWatcher{ch: closedCh}, nil
		},
	}
	svc := mustNewLocker(t, tx, &mockLeaseService{}, ws, kvs)

	_, err := svc.Lock(context.Background(), LockOptions{Key: "k", HolderID: "me", TTL: time.Second})
	if err == nil {
		t.Fatal("expected error after watcher closed and retry fails")
	}
}

func TestLock_WatcherClosedRetrySucceeds(t *testing.T) {
	existingLV := lockValue{HolderID: "other", AcquireCount: 1}
	existingBytes := encodeLockValue(t, existingLV)

	callCount := 0
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			callCount++
			if callCount == 1 {
				return &TransactionResponse{Succeeded: false}, nil
			}
			return &TransactionResponse{Succeeded: true, Revision: 55}, nil
		},
	}
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Value: existingBytes, ModRevision: 1}, nil
		},
	}

	closedCh := make(chan watch.WatchResponse)
	close(closedCh)
	ws := &mockWatchService{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return &mockWatcher{ch: closedCh}, nil
		},
	}
	svc := mustNewLocker(t, tx, &mockLeaseService{}, ws, kvs)

	lock, err := svc.Lock(context.Background(), LockOptions{Key: "k", HolderID: "me", TTL: time.Second})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lock.FencingToken != 55 {
		t.Errorf("FencingToken = %d, want 55", lock.FencingToken)
	}
}

func TestLock_WatchError_Propagates(t *testing.T) {
	existingLV := lockValue{HolderID: "other", AcquireCount: 1}
	existingBytes := encodeLockValue(t, existingLV)

	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			return &TransactionResponse{Succeeded: false}, nil
		},
	}
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Value: existingBytes, ModRevision: 1}, nil
		},
	}
	watchErr := errors.New("watch failed")
	ws := &mockWatchService{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return nil, watchErr
		},
	}
	svc := mustNewLocker(t, tx, &mockLeaseService{}, ws, kvs)

	_, err := svc.Lock(context.Background(), LockOptions{Key: "k", HolderID: "me", TTL: time.Second})
	if !errors.Is(err, watchErr) {
		t.Errorf("expected watchErr, got %v", err)
	}
}

func TestUnlock_DeleteCASFails_ReturnsNotHolder(t *testing.T) {
	lv := lockValue{HolderID: "h1", LeaseID: 9, TTL: 30, AcquireCount: 1}
	lvBytes := encodeLockValue(t, lv)

	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			// CAS delete fails (another holder changed the key).
			// The new revision is 25 (different from the caller's token of 10).
			return &TransactionResponse{Succeeded: false, Revision: 25}, nil
		},
	}
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Value: lvBytes, ModRevision: 10}, nil
		},
	}
	svc := mustNewLocker(t, tx, &mockLeaseService{}, &mockWatchService{}, kvs)

	err := svc.Unlock(context.Background(), "res", 10)
	var notHolder *NotLockHolderError
	if !errors.As(err, &notHolder) {
		t.Fatalf("expected NotLockHolderError when CAS delete fails, got %v", err)
	}
	// After the bug fix, ActualToken should reflect the new revision (25),
	// not the stale value that matched fencingToken.
	if notHolder.ActualToken != 25 {
		t.Errorf("ActualToken = %d, want 25 (from resp.Revision)", notHolder.ActualToken)
	}
	if notHolder.ExpectedToken != 10 {
		t.Errorf("ExpectedToken = %d, want 10", notHolder.ExpectedToken)
	}
}

func TestExtend_NotFound(t *testing.T) {
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return nil, errors.New("key not found: " + lockKey("k"))
		},
	}
	svc := mustNewLocker(t, &mockTransactionService{}, &mockLeaseService{}, &mockWatchService{}, kvs)

	_, err := svc.Extend(context.Background(), "k", 1)
	var notFound *LockNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected LockNotFoundError, got %v", err)
	}
}

func TestExtend_KeepAliveError(t *testing.T) {
	lv := lockValue{HolderID: "h1", LeaseID: 7, TTL: 30, AcquireCount: 1}
	lvBytes := encodeLockValue(t, lv)

	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Value: lvBytes, ModRevision: 5}, nil
		},
	}
	kaErr := errors.New("keepalive failed")
	leaseService := &mockLeaseService{
		keepAliveFn: func(_ context.Context, _ int64) (*lease.Lease, error) {
			return nil, kaErr
		},
	}
	svc := mustNewLocker(t, &mockTransactionService{}, leaseService, &mockWatchService{}, kvs)

	_, err := svc.Extend(context.Background(), "res", 5)
	if !errors.Is(err, kaErr) {
		t.Errorf("expected keepalive error, got %v", err)
	}
}

func TestList_RangeError(t *testing.T) {
	rangeErr := errors.New("range failed")
	kvs := &mockMVCCKVService{
		rangeFn: func(_ context.Context, _, _ []byte, _ ...RangeOption) ([]*kv.KeyValue, int64, error) {
			return nil, 0, rangeErr
		},
	}
	svc := mustNewLocker(t, &mockTransactionService{}, &mockLeaseService{}, &mockWatchService{}, kvs)

	_, err := svc.List(context.Background(), "")
	if !errors.Is(err, rangeErr) {
		t.Errorf("expected rangeErr, got %v", err)
	}
}

func TestList_MalformedEntrySkipped(t *testing.T) {
	kvs := &mockMVCCKVService{
		rangeFn: func(_ context.Context, _, _ []byte, _ ...RangeOption) ([]*kv.KeyValue, int64, error) {
			return []*kv.KeyValue{
				{Key: []byte(lockKeyPrefix + "bad"), Value: []byte("not-json"), ModRevision: 1},
			}, 1, nil
		},
	}
	svc := mustNewLocker(t, &mockTransactionService{}, &mockLeaseService{}, &mockWatchService{}, kvs)

	locks, err := svc.List(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The malformed entry should be silently skipped.
	if len(locks) != 0 {
		t.Errorf("expected empty slice (malformed entry skipped), got %d", len(locks))
	}
}

func TestLock_DeleteEventThenHeldAgain(t *testing.T) {
	// Cover the "continue loop" branch when after a delete event TryLock returns LockHeld again.
	existingLV := lockValue{HolderID: "other", AcquireCount: 1}
	existingBytes := encodeLockValue(t, existingLV)

	callCount := 0
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			callCount++
			switch callCount {
			case 1:
				// Initial TryLock: CAS fails.
				return &TransactionResponse{Succeeded: false}, nil
			case 2:
				// After delete event: CAS still fails (raced).
				return &TransactionResponse{Succeeded: false}, nil
			default:
				// Third attempt: succeed.
				return &TransactionResponse{Succeeded: true, Revision: 77}, nil
			}
		},
	}
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Value: existingBytes, ModRevision: 1}, nil
		},
	}

	// Deliver two delete events to trigger both the "still held" and "success" paths.
	watchCh := make(chan watch.WatchResponse, 2)
	deleteEvt := watch.WatchResponse{
		Events: []*watch.Event{
			{Type: watch.EventDelete, Kv: &kv.KeyValue{Key: []byte(lockKey("k"))}},
		},
	}
	watchCh <- deleteEvt
	watchCh <- deleteEvt

	ws := &mockWatchService{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return &mockWatcher{ch: watchCh}, nil
		},
	}
	svc := mustNewLocker(t, tx, &mockLeaseService{}, ws, kvs)

	lock, err := svc.Lock(context.Background(), LockOptions{Key: "k", HolderID: "me", TTL: time.Second})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lock.FencingToken != 77 {
		t.Errorf("FencingToken = %d, want 77", lock.FencingToken)
	}
}

func TestLock_DeleteEvent_TryLockNonHeldError(t *testing.T) {
	// Cover: after delete event, TryLock returns an error that is not LockHeldError.
	existingLV := lockValue{HolderID: "other", AcquireCount: 1}
	existingBytes := encodeLockValue(t, existingLV)

	callCount := 0
	fatalErr := errors.New("fatal error")
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			callCount++
			if callCount == 1 {
				return &TransactionResponse{Succeeded: false}, nil
			}
			return nil, fatalErr
		},
	}
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Value: existingBytes, ModRevision: 1}, nil
		},
	}

	watchCh := make(chan watch.WatchResponse, 1)
	watchCh <- watch.WatchResponse{
		Events: []*watch.Event{
			{Type: watch.EventDelete, Kv: &kv.KeyValue{Key: []byte(lockKey("k"))}},
		},
	}
	ws := &mockWatchService{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return &mockWatcher{ch: watchCh}, nil
		},
	}
	svc := mustNewLocker(t, tx, &mockLeaseService{}, ws, kvs)

	_, err := svc.Lock(context.Background(), LockOptions{Key: "k", HolderID: "me", TTL: time.Second})
	if err == nil {
		t.Fatal("expected error to be propagated")
	}
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

func TestTryLock_SubSecondTTL_ClampsToOne(t *testing.T) {
	var grantedTTL int64
	leaseService := &mockLeaseService{
		grantFn: func(_ context.Context, ttl int64) (*lease.Lease, error) {
			grantedTTL = ttl
			return &lease.Lease{ID: 1, TTL: ttl, GrantedTTL: ttl}, nil
		},
	}
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			return &TransactionResponse{Succeeded: true, Revision: 1}, nil
		},
	}
	svc := mustNewLocker(t, tx, leaseService, &mockWatchService{}, &mockMVCCKVService{})

	// 500ms < 1s, so ttlSecs is 0 and should be clamped to 1.
	_, err := svc.TryLock(context.Background(), LockOptions{Key: "k", HolderID: "h", TTL: 500 * time.Millisecond})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if grantedTTL != 1 {
		t.Errorf("grantedTTL = %d, want 1 (clamped from sub-second)", grantedTTL)
	}
}

func TestTryLock_JSONMarshalAfterCASFail_InvalidJSON(t *testing.T) {
	// GetRevision returns bytes that are not valid JSON to cover unmarshal error branch.
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			return &TransactionResponse{Succeeded: false}, nil
		},
	}
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Value: []byte("not-json"), ModRevision: 1}, nil
		},
	}
	svc := mustNewLocker(t, tx, &mockLeaseService{}, &mockWatchService{}, kvs)

	_, err := svc.TryLock(context.Background(), LockOptions{Key: "k", HolderID: "h", TTL: time.Second})
	if err == nil {
		t.Fatal("expected JSON unmarshal error, got nil")
	}
}

func TestUnlock_GetRevisionReturnsNilKV(t *testing.T) {
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return nil, nil
		},
	}
	svc := mustNewLocker(t, &mockTransactionService{}, &mockLeaseService{}, &mockWatchService{}, kvs)

	err := svc.Unlock(context.Background(), "k", 1)
	var notFound *LockNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected LockNotFoundError for nil KV, got %v", err)
	}
}

func TestUnlock_InvalidJSON(t *testing.T) {
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Value: []byte("bad-json"), ModRevision: 5}, nil
		},
	}
	svc := mustNewLocker(t, &mockTransactionService{}, &mockLeaseService{}, &mockWatchService{}, kvs)

	err := svc.Unlock(context.Background(), "k", 5)
	if err == nil {
		t.Fatal("expected JSON unmarshal error, got nil")
	}
}

func TestUnlock_ReentrantDecrement_ExecuteError(t *testing.T) {
	lv := lockValue{HolderID: "h1", Reentrant: true, AcquireCount: 2, LeaseID: 9, TTL: 30}
	lvBytes := encodeLockValue(t, lv)

	updateErr := errors.New("update failed")
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			return nil, updateErr
		},
	}
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Value: lvBytes, ModRevision: 10}, nil
		},
	}
	svc := mustNewLocker(t, tx, &mockLeaseService{}, &mockWatchService{}, kvs)

	err := svc.Unlock(context.Background(), "k", 10)
	if err == nil {
		t.Fatal("expected update error from reentrant decrement, got nil")
	}
}

func TestExtend_GetRevisionReturnsNilKV(t *testing.T) {
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return nil, nil
		},
	}
	svc := mustNewLocker(t, &mockTransactionService{}, &mockLeaseService{}, &mockWatchService{}, kvs)

	_, err := svc.Extend(context.Background(), "k", 1)
	var notFound *LockNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected LockNotFoundError for nil KV, got %v", err)
	}
}

func TestExtend_InvalidJSON(t *testing.T) {
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Value: []byte("bad-json"), ModRevision: 5}, nil
		},
	}
	svc := mustNewLocker(t, &mockTransactionService{}, &mockLeaseService{}, &mockWatchService{}, kvs)

	_, err := svc.Extend(context.Background(), "k", 5)
	if err == nil {
		t.Fatal("expected JSON unmarshal error, got nil")
	}
}

func TestStatus_InvalidJSON(t *testing.T) {
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Value: []byte("bad-json"), ModRevision: 3}, nil
		},
	}
	svc := mustNewLocker(t, &mockTransactionService{}, &mockLeaseService{}, &mockWatchService{}, kvs)

	_, err := svc.Status(context.Background(), "k")
	if err == nil {
		t.Fatal("expected JSON unmarshal error, got nil")
	}
}

func TestStatus_GetRevisionReturnsNilKV(t *testing.T) {
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return nil, nil
		},
	}
	svc := mustNewLocker(t, &mockTransactionService{}, &mockLeaseService{}, &mockWatchService{}, kvs)

	lock, err := svc.Status(context.Background(), "k")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lock != nil {
		t.Errorf("expected nil lock for nil KV, got %+v", lock)
	}
}

func TestLock_NilEventSkipped(t *testing.T) {
	// Cover the evt == nil guard inside the watch loop.
	existingLV := lockValue{HolderID: "other", AcquireCount: 1}
	existingBytes := encodeLockValue(t, existingLV)

	callCount := 0
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			callCount++
			if callCount == 1 {
				return &TransactionResponse{Succeeded: false}, nil
			}
			return &TransactionResponse{Succeeded: true, Revision: 33}, nil
		},
	}
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Value: existingBytes, ModRevision: 1}, nil
		},
	}

	watchCh := make(chan watch.WatchResponse, 2)
	// First response has a nil event to exercise the nil guard.
	watchCh <- watch.WatchResponse{Events: []*watch.Event{nil}}
	// Second response has a real delete to acquire the lock.
	watchCh <- watch.WatchResponse{
		Events: []*watch.Event{
			{Type: watch.EventDelete, Kv: &kv.KeyValue{Key: []byte(lockKey("k"))}},
		},
	}
	ws := &mockWatchService{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return &mockWatcher{ch: watchCh}, nil
		},
	}
	svc := mustNewLocker(t, tx, &mockLeaseService{}, ws, kvs)

	lock, err := svc.Lock(context.Background(), LockOptions{Key: "k", HolderID: "me", TTL: time.Second})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lock.FencingToken != 33 {
		t.Errorf("FencingToken = %d, want 33", lock.FencingToken)
	}
}

func TestLockKey_AddsPrefixCorrectly(t *testing.T) {
	got := lockKey("myresource")
	want := lockKeyPrefix + "myresource"
	if got != want {
		t.Errorf("lockKey = %q, want %q", got, want)
	}
}

func TestUserKey_StripsPrefix(t *testing.T) {
	got := userKey(lockKeyPrefix + "myresource")
	want := "myresource"
	if got != want {
		t.Errorf("userKey = %q, want %q", got, want)
	}
}

func TestUserKey_ShortKey_ReturnsOriginal(t *testing.T) {
	short := "ab"
	got := userKey(short)
	if got != short {
		t.Errorf("userKey(%q) = %q, want %q", short, got, short)
	}
}

// ---------------------------------------------------------------------------
// Concurrent safety
// ---------------------------------------------------------------------------

func TestConcurrent_TryLock_Safety(t *testing.T) {
	// Verify that concurrent TryLock calls do not panic or race.
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			return &TransactionResponse{Succeeded: true, Revision: 1}, nil
		},
	}
	svc := mustNewLocker(t, tx, &mockLeaseService{}, &mockWatchService{}, &mockMVCCKVService{})

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)
	errs := make(chan error, goroutines)

	for i := range goroutines {
		go func(idx int) {
			defer wg.Done()
			_, err := svc.TryLock(context.Background(), LockOptions{
				Key:      "concurrent-key",
				HolderID: "holder",
				TTL:      5 * time.Second,
			})
			if err != nil {
				errs <- err
			}
		}(i)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent TryLock error: %v", err)
	}
}

func TestConcurrent_Unlock_Safety(t *testing.T) {
	// Verify that concurrent Unlock calls do not panic or race.
	lv := lockValue{HolderID: "h1", LeaseID: 9, TTL: 30, AcquireCount: 1}
	lvBytes := encodeLockValue(t, lv)

	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			return &TransactionResponse{Succeeded: true, Revision: 15}, nil
		},
	}
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Value: lvBytes, ModRevision: 10}, nil
		},
	}
	svc := mustNewLocker(t, tx, &mockLeaseService{}, &mockWatchService{}, kvs)

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()
			// Some will succeed, some may get NotLockHolderError -- either is fine.
			_ = svc.Unlock(context.Background(), "res", 10)
		}()
	}

	wg.Wait()
}

// ---------------------------------------------------------------------------
// Unlock lease revoke error propagation
// ---------------------------------------------------------------------------

func TestUnlock_Success_LeaseRevokeError(t *testing.T) {
	lv := lockValue{HolderID: "h1", LeaseID: 9, TTL: 30, AcquireCount: 1}
	lvBytes := encodeLockValue(t, lv)

	revokeErr := errors.New("revoke failed")
	leaseService := &mockLeaseService{
		revokeFn: func(_ context.Context, _ int64) error {
			return revokeErr
		},
	}
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			return &TransactionResponse{Succeeded: true, Revision: 15}, nil
		},
	}
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Value: lvBytes, ModRevision: 10}, nil
		},
	}
	svc := mustNewLocker(t, tx, leaseService, &mockWatchService{}, kvs)

	err := svc.Unlock(context.Background(), "res", 10)
	if !errors.Is(err, revokeErr) {
		t.Errorf("expected revoke error propagated, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Unlock execute error propagation
// ---------------------------------------------------------------------------

func TestUnlock_ExecuteError(t *testing.T) {
	lv := lockValue{HolderID: "h1", LeaseID: 9, TTL: 30, AcquireCount: 1}
	lvBytes := encodeLockValue(t, lv)

	txErr := errors.New("tx execute failed")
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			return nil, txErr
		},
	}
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Value: lvBytes, ModRevision: 10}, nil
		},
	}
	svc := mustNewLocker(t, tx, &mockLeaseService{}, &mockWatchService{}, kvs)

	err := svc.Unlock(context.Background(), "res", 10)
	if !errors.Is(err, txErr) {
		t.Errorf("expected tx error propagated, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Extend preserves all fields
// ---------------------------------------------------------------------------

func TestExtend_PreservesAllFields(t *testing.T) {
	lv := lockValue{HolderID: "h1", Reentrant: true, LeaseID: 7, TTL: 30, AcquireCount: 3, AcquiredAt: 99999, FencingToken: 42}
	lvBytes := encodeLockValue(t, lv)

	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Value: lvBytes, ModRevision: 42}, nil
		},
	}
	leaseService := &mockLeaseService{
		keepAliveFn: func(_ context.Context, leaseID int64) (*lease.Lease, error) {
			return &lease.Lease{ID: leaseID, TTL: 60, GrantedTTL: 60}, nil
		},
	}
	svc := mustNewLocker(t, &mockTransactionService{}, leaseService, &mockWatchService{}, kvs)

	extended, err := svc.Extend(context.Background(), "mykey", 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if extended.Key != "mykey" {
		t.Errorf("Key = %q, want %q", extended.Key, "mykey")
	}
	if extended.FencingToken != 42 {
		t.Errorf("FencingToken = %d, want 42", extended.FencingToken)
	}
	if extended.LeaseID != 7 {
		t.Errorf("LeaseID = %d, want 7", extended.LeaseID)
	}
	if extended.HolderID != "h1" {
		t.Errorf("HolderID = %q, want %q", extended.HolderID, "h1")
	}
	if !extended.Reentrant {
		t.Error("Reentrant should be true")
	}
	if extended.AcquireCount != 3 {
		t.Errorf("AcquireCount = %d, want 3", extended.AcquireCount)
	}
	// TTL should be the refreshed value.
	if extended.TTL != 60 {
		t.Errorf("TTL = %d, want 60 (refreshed)", extended.TTL)
	}
	if extended.AcquiredAt != 99999 {
		t.Errorf("AcquiredAt = %d, want 99999", extended.AcquiredAt)
	}
}

// ---------------------------------------------------------------------------
// List preserves all fields
// ---------------------------------------------------------------------------

func TestList_PreservesAllFields(t *testing.T) {
	lv := lockValue{HolderID: "holder-x", Reentrant: true, LeaseID: 42, TTL: 120, AcquireCount: 5, AcquiredAt: 11111, FencingToken: 88}
	b := encodeLockValue(t, lv)

	kvs := &mockMVCCKVService{
		rangeFn: func(_ context.Context, _, _ []byte, _ ...RangeOption) ([]*kv.KeyValue, int64, error) {
			return []*kv.KeyValue{
				{Key: []byte(lockKeyPrefix + "myres"), Value: b, ModRevision: 88},
			}, 1, nil
		},
	}
	svc := mustNewLocker(t, &mockTransactionService{}, &mockLeaseService{}, &mockWatchService{}, kvs)

	locks, err := svc.List(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(locks) != 1 {
		t.Fatalf("expected 1 lock, got %d", len(locks))
	}
	l := locks[0]
	if l.Key != "myres" {
		t.Errorf("Key = %q, want %q", l.Key, "myres")
	}
	if l.FencingToken != 88 {
		t.Errorf("FencingToken = %d, want 88", l.FencingToken)
	}
	if l.HolderID != "holder-x" {
		t.Errorf("HolderID = %q, want %q", l.HolderID, "holder-x")
	}
	if !l.Reentrant {
		t.Error("Reentrant should be true")
	}
	if l.AcquireCount != 5 {
		t.Errorf("AcquireCount = %d, want 5", l.AcquireCount)
	}
	if l.TTL != 120 {
		t.Errorf("TTL = %d, want 120", l.TTL)
	}
	if l.AcquiredAt != 11111 {
		t.Errorf("AcquiredAt = %d, want 11111", l.AcquiredAt)
	}
}

// ---------------------------------------------------------------------------
// TryLock verifies CAS compare fields
// ---------------------------------------------------------------------------

func TestTryLock_CASCompare_UsesVersionZero(t *testing.T) {
	var capturedCompares []Compare
	var captured bool
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, compares []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			if !captured {
				capturedCompares = compares
				captured = true
			}
			return &TransactionResponse{Succeeded: true, Revision: 1}, nil
		},
	}
	svc := mustNewLocker(t, tx, &mockLeaseService{}, &mockWatchService{}, &mockMVCCKVService{})

	_, err := svc.TryLock(context.Background(), LockOptions{Key: "cas-test", HolderID: "h", TTL: time.Second})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(capturedCompares) != 1 {
		t.Fatalf("expected 1 compare, got %d", len(capturedCompares))
	}
	cmp := capturedCompares[0]
	if cmp.Target != CompareVersion {
		t.Errorf("compare Target = %v, want CompareVersion", cmp.Target)
	}
	if cmp.Result != CompareEqual {
		t.Errorf("compare Result = %v, want CompareEqual", cmp.Result)
	}
	if cmp.Version != 0 {
		t.Errorf("compare Version = %d, want 0", cmp.Version)
	}
	wantKey := lockKey("cas-test")
	if string(cmp.Key) != wantKey {
		t.Errorf("compare Key = %q, want %q", cmp.Key, wantKey)
	}
}

// ---------------------------------------------------------------------------
// TryLock stores correct lease ID in put op
// ---------------------------------------------------------------------------

func TestTryLock_PutOp_HasLeaseID(t *testing.T) {
	var capturedOps []Op
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, successOps, _ []Op) (*TransactionResponse, error) {
			capturedOps = successOps
			return &TransactionResponse{Succeeded: true, Revision: 1}, nil
		},
	}
	leaseService := &mockLeaseService{
		grantFn: func(_ context.Context, ttl int64) (*lease.Lease, error) {
			return &lease.Lease{ID: 42, TTL: ttl, GrantedTTL: ttl}, nil
		},
	}
	svc := mustNewLocker(t, tx, leaseService, &mockWatchService{}, &mockMVCCKVService{})

	_, err := svc.TryLock(context.Background(), LockOptions{Key: "k", HolderID: "h", TTL: 5 * time.Second})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(capturedOps) != 1 {
		t.Fatalf("expected 1 success op, got %d", len(capturedOps))
	}
	if capturedOps[0].LeaseID != 42 {
		t.Errorf("put op LeaseID = %d, want 42", capturedOps[0].LeaseID)
	}
}

// ls returns a default mock lease service (convenience helper).
func ls() *mockLeaseService {
	return &mockLeaseService{}
}

// ---------------------------------------------------------------------------
// Lock watches the correct internal key
// ---------------------------------------------------------------------------

func TestLock_WatchesInternalKey(t *testing.T) {
	existingLV := lockValue{HolderID: "other", AcquireCount: 1}
	existingBytes := encodeLockValue(t, existingLV)

	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			return &TransactionResponse{Succeeded: false}, nil
		},
	}
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Value: existingBytes, ModRevision: 1}, nil
		},
	}

	var watchedKey string
	ws := &mockWatchService{
		watchFn: func(_ context.Context, key string, _ ...watch.WatchOption) (watch.Watcher, error) {
			watchedKey = key
			// Return a closed channel so Lock falls through to retry.
			ch := make(chan watch.WatchResponse)
			close(ch)
			return &mockWatcher{ch: ch}, nil
		},
	}

	callCount := 0
	tx.executeFn = func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
		callCount++
		if callCount == 1 {
			return &TransactionResponse{Succeeded: false}, nil
		}
		return &TransactionResponse{Succeeded: true, Revision: 1}, nil
	}

	svc := mustNewLocker(t, tx, &mockLeaseService{}, ws, kvs)

	_, err := svc.Lock(context.Background(), LockOptions{Key: "user-key", HolderID: "me", TTL: time.Second})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantKey := lockKey("user-key")
	if watchedKey != wantKey {
		t.Errorf("watched key = %q, want %q", watchedKey, wantKey)
	}
}

// ---------------------------------------------------------------------------
// Unlock CAS compare uses correct fields
// ---------------------------------------------------------------------------

func TestUnlock_CASCompare_UsesModRevision(t *testing.T) {
	lv := lockValue{HolderID: "h1", LeaseID: 9, TTL: 30, AcquireCount: 1}
	lvBytes := encodeLockValue(t, lv)

	var capturedCompares []Compare
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, compares []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			capturedCompares = compares
			return &TransactionResponse{Succeeded: true, Revision: 15}, nil
		},
	}
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Value: lvBytes, ModRevision: 10}, nil
		},
	}
	svc := mustNewLocker(t, tx, &mockLeaseService{}, &mockWatchService{}, kvs)

	err := svc.Unlock(context.Background(), "res", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(capturedCompares) != 1 {
		t.Fatalf("expected 1 compare, got %d", len(capturedCompares))
	}
	cmp := capturedCompares[0]
	if cmp.Target != CompareModRevision {
		t.Errorf("compare Target = %v, want CompareModRevision", cmp.Target)
	}
	if cmp.ModRevision != 10 {
		t.Errorf("compare ModRevision = %d, want 10", cmp.ModRevision)
	}
}

// ---------------------------------------------------------------------------
// Status returns correct key from user-provided key
// ---------------------------------------------------------------------------

func TestStatus_ReturnsUserKey(t *testing.T) {
	lv := lockValue{HolderID: "h1", LeaseID: 1, TTL: 30, AcquireCount: 1}
	lvBytes := encodeLockValue(t, lv)

	var queriedKey string
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, key string) (*kv.KeyValue, error) {
			queriedKey = key
			return &kv.KeyValue{Value: lvBytes, ModRevision: 5}, nil
		},
	}
	svc := mustNewLocker(t, &mockTransactionService{}, &mockLeaseService{}, &mockWatchService{}, kvs)

	lock, err := svc.Status(context.Background(), "my-user-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Status should query using the internal prefix.
	wantInternal := lockKey("my-user-key")
	if queriedKey != wantInternal {
		t.Errorf("queried key = %q, want %q", queriedKey, wantInternal)
	}
	// The returned lock key should be the user-facing key.
	if lock.Key != "my-user-key" {
		t.Errorf("Lock.Key = %q, want %q", lock.Key, "my-user-key")
	}
}

// ---------------------------------------------------------------------------
// prefixEnd edge cases
// ---------------------------------------------------------------------------

func TestPrefixEnd_EmptySlice_ReturnsNil(t *testing.T) {
	result := prefixEnd([]byte{})
	if result != nil {
		t.Errorf("expected nil for empty prefix, got %v", result)
	}
}

func TestPrefixEnd_SingleByte(t *testing.T) {
	result := prefixEnd([]byte{0x41})
	if len(result) != 1 || result[0] != 0x42 {
		t.Errorf("prefixEnd([0x41]) = %v, want [0x42]", result)
	}
}

func TestPrefixEnd_TrailingFF(t *testing.T) {
	// [0x41, 0xFF] should increment the first byte and truncate.
	result := prefixEnd([]byte{0x41, 0xFF})
	if len(result) != 1 || result[0] != 0x42 {
		t.Errorf("prefixEnd([0x41, 0xFF]) = %v, want [0x42]", result)
	}
}

// ---------------------------------------------------------------------------
// Lock non-delete watch events are ignored
// ---------------------------------------------------------------------------

func TestLock_NonDeleteEvent_Ignored(t *testing.T) {
	existingLV := lockValue{HolderID: "other", AcquireCount: 1}
	existingBytes := encodeLockValue(t, existingLV)

	callCount := 0
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			callCount++
			if callCount == 1 {
				return &TransactionResponse{Succeeded: false}, nil
			}
			return &TransactionResponse{Succeeded: true, Revision: 10}, nil
		},
	}
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Value: existingBytes, ModRevision: 1}, nil
		},
	}

	watchCh := make(chan watch.WatchResponse, 2)
	// First event is a put (should be ignored).
	watchCh <- watch.WatchResponse{
		Events: []*watch.Event{
			{Type: watch.EventPut, Kv: &kv.KeyValue{Key: []byte(lockKey("k"))}},
		},
	}
	// Second event is a delete (should trigger retry).
	watchCh <- watch.WatchResponse{
		Events: []*watch.Event{
			{Type: watch.EventDelete, Kv: &kv.KeyValue{Key: []byte(lockKey("k"))}},
		},
	}

	ws := &mockWatchService{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return &mockWatcher{ch: watchCh}, nil
		},
	}
	svc := mustNewLocker(t, tx, &mockLeaseService{}, ws, kvs)

	lock, err := svc.Lock(context.Background(), LockOptions{Key: "k", HolderID: "me", TTL: time.Second})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// TryLock generates 2 CAS calls (initial + after delete event) plus
	// 1 fencing-token persistence PUT per successful CAS. The put watch
	// event must not have triggered a retry.
	if callCount != 3 {
		t.Errorf("Execute callCount = %d, want 3 (2 CAS + 1 fencing-token persist)", callCount)
	}
	if lock.FencingToken != 10 {
		t.Errorf("FencingToken = %d, want 10", lock.FencingToken)
	}
}
