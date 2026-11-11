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
	"fmt"
	"reflect"
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
	rangeFn       func(ctx context.Context, start, end []byte, opts ...RangeOption) ([]*kv.KeyValue, int64, int64, error)
}

func (m *mockMVCCKVService) GetRevision(ctx context.Context, key string) (*kv.KeyValue, error) {
	if m.getRevisionFn != nil {
		return m.getRevisionFn(ctx, key)
	}
	return nil, &keyNotFoundDoubleError{key: key}
}

// keyNotFoundDoubleError mirrors service.KeyNotFoundError, the error the MVCC KV
// service's GetRevision returns for a key it does not hold: an error that
// names the missing key through KeyNotFound. A plain errors.New is NOT a
// stand-in for it — the locker must tell absence apart from a failed read.
type keyNotFoundDoubleError struct {
	key string
}

func (e *keyNotFoundDoubleError) Error() string       { return "service: key not found: " + e.key }
func (e *keyNotFoundDoubleError) KeyNotFound() string { return e.key }

var _ keyNotFoundError = (*keyNotFoundDoubleError)(nil)

// errReadFailed stands for every GetRevision failure that is not absence:
// a timeout, a lost leader, a sealed barrier.
var errReadFailed = errors.New("service: read failed: timeout")

func (m *mockMVCCKVService) Range(ctx context.Context, start, end []byte, opts ...RangeOption) ([]*kv.KeyValue, int64, int64, error) {
	if m.rangeFn != nil {
		return m.rangeFn(ctx, start, end, opts...)
	}
	return nil, 0, 0, nil
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
			return &kv.KeyValue{Key: []byte(lockKey("res")), Value: existingBytes, CreateRevision: 10, ModRevision: 10}, nil
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
			return &kv.KeyValue{Key: []byte(lockKey("res")), Value: existingBytes, CreateRevision: 10, ModRevision: 10}, nil
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
			return &kv.KeyValue{Key: []byte(lockKey("res")), Value: existingBytes, CreateRevision: 5, ModRevision: 5}, nil
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
			return &kv.KeyValue{Key: []byte(lockKey("res")), Value: existingBytes, CreateRevision: 5, ModRevision: 5}, nil
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
			return &kv.KeyValue{Key: []byte(lockKey("res")), Value: existingBytes, CreateRevision: 5, ModRevision: 5}, nil
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
			return &kv.KeyValue{Value: existingBytes, CreateRevision: 5, ModRevision: 5}, nil
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
			return &kv.KeyValue{Value: existingBytes, CreateRevision: 1, ModRevision: 1}, nil
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
			return &kv.KeyValue{Value: lvBytes, CreateRevision: 10, ModRevision: 10}, nil
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
			return &kv.KeyValue{Value: lvBytes, CreateRevision: 10, ModRevision: 10}, nil
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
			return nil, &keyNotFoundDoubleError{key: lockKey("missing")}
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

// TestUnlock_ReadErrorKeepsItsClass pins that a GetRevision failure other
// than absence reaches the caller unchanged rather than as "not found".
func TestUnlock_ReadErrorKeepsItsClass(t *testing.T) {
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return nil, errReadFailed
		},
	}
	svc := mustNewLocker(t, &mockTransactionService{}, &mockLeaseService{}, &mockWatchService{}, kvs)

	err := svc.Unlock(context.Background(), "k", 1)
	if !errors.Is(err, errReadFailed) {
		t.Fatalf("Unlock error = %v, want %v", err, errReadFailed)
	}
	var notFound *LockNotFoundError
	if errors.As(err, &notFound) {
		t.Fatalf("Unlock reported a read failure as LockNotFoundError: %v", err)
	}
}

// TestUnlock_WrappedNotFound pins that absence is recognised through
// wrapping, as errors.As does for every other typed error.
func TestUnlock_WrappedNotFound(t *testing.T) {
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, key string) (*kv.KeyValue, error) {
			return nil, fmt.Errorf("namespaced: %w", &keyNotFoundDoubleError{key: key})
		},
	}
	svc := mustNewLocker(t, &mockTransactionService{}, &mockLeaseService{}, &mockWatchService{}, kvs)

	var notFound *LockNotFoundError
	if err := svc.Unlock(context.Background(), "gone", 1); !errors.As(err, &notFound) || notFound.Key != "gone" {
		t.Fatalf("Unlock error = %v, want LockNotFoundError{Key: gone}", err)
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
			return &kv.KeyValue{Value: lvBytes, CreateRevision: 10, ModRevision: 10}, nil
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

// decrementRace is a store double for Unlock of a reentrant lock on lease 9
// with fencing token 7: each read returns the next of reads (key-not-found
// once they run out), and each decrement CAS succeeds only if the next of
// wins says so.
type decrementRace struct {
	reads        []lockValue
	wins         []bool
	readCalls    int
	decrementCmp []Compare
	decrementOps [][]Op
}

func (r *decrementRace) locker(t *testing.T) *Service {
	t.Helper()
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, compares []Compare, successOps []Op, _ []Op) (*TransactionResponse, error) {
			r.decrementCmp = append(r.decrementCmp, compares...)
			r.decrementOps = append(r.decrementOps, successOps)
			won := r.wins[0]
			r.wins = r.wins[1:]
			return &TransactionResponse{Succeeded: won, Revision: 20}, nil
		},
	}
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, key string) (*kv.KeyValue, error) {
			if r.readCalls == len(r.reads) {
				return nil, &keyNotFoundDoubleError{key: key}
			}
			lv := r.reads[r.readCalls]
			r.readCalls++
			return &kv.KeyValue{Key: []byte(key), Value: encodeLockValue(t, lv), CreateRevision: 7, ModRevision: int64(10 + r.readCalls)}, nil
		},
	}
	return mustNewLocker(t, tx, &mockLeaseService{}, &mockWatchService{}, kvs)
}

func reentrantHeld(count int32) lockValue {
	return lockValue{HolderID: "h", Reentrant: true, AcquireCount: count, LeaseID: 9, TTL: 30}
}

func TestUnlock_DecrementIsCASOnReadRevision(t *testing.T) {
	r := &decrementRace{reads: []lockValue{reentrantHeld(2)}, wins: []bool{true}}
	if err := r.locker(t).Unlock(context.Background(), "k", 7); err != nil {
		t.Fatalf("Unlock error = %v", err)
	}
	if len(r.decrementCmp) != 1 || r.decrementCmp[0].Target != CompareModRevision || r.decrementCmp[0].Result != CompareEqual ||
		r.decrementCmp[0].ModRevision != 11 || string(r.decrementCmp[0].Key) != lockKey("k") {
		t.Fatalf("decrement compares = %+v, want ModRevision == 11 on %q", r.decrementCmp, lockKey("k"))
	}
	var got lockValue
	if err := json.Unmarshal(r.decrementOps[0][0].Value, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.AcquireCount != 1 || r.decrementOps[0][0].LeaseID != 9 {
		t.Fatalf("decrement wrote count %d on lease %d, want count 1 on lease 9", got.AcquireCount, r.decrementOps[0][0].LeaseID)
	}
}

func TestUnlock_ReentryBeforeDecrement_KeepsIt(t *testing.T) {
	// A re-entry lands between the read of count 2 and the decrement: the
	// decrement must count from the re-entry's 3, not write 1 over it.
	r := &decrementRace{reads: []lockValue{reentrantHeld(2), reentrantHeld(3)}, wins: []bool{false, true}}
	if err := r.locker(t).Unlock(context.Background(), "k", 7); err != nil {
		t.Fatalf("Unlock error = %v", err)
	}
	if len(r.decrementCmp) != 2 || r.decrementCmp[1].ModRevision != 12 {
		t.Fatalf("decrement compares = %+v, want a retry on ModRevision 12", r.decrementCmp)
	}
	var got lockValue
	if err := json.Unmarshal(r.decrementOps[1][0].Value, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.AcquireCount != 2 {
		t.Fatalf("decrement wrote count %d, want 2: the re-entry's increment was lost", got.AcquireCount)
	}
}

func TestUnlock_ReleasedBeforeDecrement_NotFound(t *testing.T) {
	// The lease expires between the read and the decrement: the lost CAS
	// must not write the key back, and the re-read finds no lock.
	r := &decrementRace{reads: []lockValue{reentrantHeld(2)}, wins: []bool{false}}
	err := r.locker(t).Unlock(context.Background(), "k", 7)
	var notFound *LockNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("Unlock error = %v, want LockNotFoundError", err)
	}
	if len(r.decrementOps) != 1 || r.readCalls != 1 {
		t.Fatalf("Unlock made %d writes after %d reads, want the one lost CAS", len(r.decrementOps), r.readCalls)
	}
}

func TestUnlock_DecrementRaceEndsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &decrementRace{reads: []lockValue{reentrantHeld(2), reentrantHeld(2)}, wins: []bool{false}}
	svc := r.locker(t)
	tx := svc.txSvc.(*mockTransactionService)
	execute := tx.executeFn
	tx.executeFn = func(ctx context.Context, compares []Compare, successOps, failureOps []Op) (*TransactionResponse, error) {
		defer cancel()
		return execute(ctx, compares, successOps, failureOps)
	}

	if err := svc.Unlock(ctx, "k", 7); !errors.Is(err, context.Canceled) {
		t.Fatalf("Unlock error = %v, want context.Canceled", err)
	}
	if r.readCalls != 1 {
		t.Fatalf("reads = %d, want 1: a canceled Unlock retries nothing", r.readCalls)
	}
}

// ---------------------------------------------------------------------------
// Extend
// ---------------------------------------------------------------------------

func TestExtend_Success(t *testing.T) {
	lv := lockValue{HolderID: "h1", LeaseID: 7, TTL: 30, AcquireCount: 1}
	lvBytes := encodeLockValue(t, lv)

	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Value: lvBytes, CreateRevision: 5, ModRevision: 5}, nil
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
			return &kv.KeyValue{Value: lvBytes, CreateRevision: 5, ModRevision: 5}, nil
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
	lv := lockValue{HolderID: "h1", LeaseID: 3, TTL: 60, AcquireCount: 1, AcquiredAt: 12345}
	lvBytes := encodeLockValue(t, lv)

	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Value: lvBytes, CreateRevision: 7, ModRevision: 7}, nil
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
			return nil, &keyNotFoundDoubleError{key: lockKey("k")}
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

// TestStatus_ReadErrorKeepsItsClass pins that a GetRevision failure other
// than absence is returned, not reported as "not locked".
func TestStatus_ReadErrorKeepsItsClass(t *testing.T) {
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return nil, errReadFailed
		},
	}
	svc := mustNewLocker(t, &mockTransactionService{}, &mockLeaseService{}, &mockWatchService{}, kvs)

	lk, err := svc.Status(context.Background(), "k")
	if lk != nil {
		t.Fatalf("Status returned a lock on a failed read: %+v", lk)
	}
	if !errors.Is(err, errReadFailed) {
		t.Fatalf("Status error = %v, want %v", err, errReadFailed)
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
		rangeFn: func(_ context.Context, _, _ []byte, _ ...RangeOption) ([]*kv.KeyValue, int64, int64, error) {
			return []*kv.KeyValue{
				{Key: []byte(lockKeyPrefix + "res1"), Value: b1, CreateRevision: 10, ModRevision: 10},
				{Key: []byte(lockKeyPrefix + "res2"), Value: b2, CreateRevision: 20, ModRevision: 20},
			}, 2, 20, nil
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
		rangeFn: func(_ context.Context, start, end []byte, _ ...RangeOption) ([]*kv.KeyValue, int64, int64, error) {
			capturedStart = start
			capturedEnd = end
			return []*kv.KeyValue{
				{Key: []byte(lockKeyPrefix + "db/table"), Value: b, CreateRevision: 5, ModRevision: 5},
			}, 1, 5, nil
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
		rangeFn: func(_ context.Context, _, _ []byte, _ ...RangeOption) ([]*kv.KeyValue, int64, int64, error) {
			return nil, 0, 0, nil
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

// TestTryLock_TransactionError_MaybeApplied_LockActuallyCommitted covers the
// outcome-unknown case go-quicraft 0.18 widened: Execute fails, but the PUT
// it proposed actually committed under the lease this attempt just granted.
// TryLock must recover the acquired lock from the re-read rather than
// revoking the lease that backs it and reporting failure (go-qrdb#601).
func TestTryLock_TransactionError_MaybeApplied_LockActuallyCommitted(t *testing.T) {
	txErr := errors.New("tx error: outcome unknown")
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			return nil, txErr
		},
	}
	revoked := false
	ls := &mockLeaseService{
		revokeFn: func(_ context.Context, _ int64) error {
			revoked = true
			return nil
		},
	}
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{
				Value:          encodeLockValue(t, lockValue{HolderID: "h", LeaseID: 1, TTL: 1, AcquiredAt: time.Now().Unix()}),
				LeaseID:        1, // the lease mockLeaseService.Grant assigns by default
				CreateRevision: 42,
				ModRevision:    42,
			}, nil
		},
	}
	svc := mustNewLocker(t, tx, ls, &mockWatchService{}, kvs)

	lock, err := svc.TryLock(context.Background(), LockOptions{Key: "k", HolderID: "h", TTL: time.Second})
	if err != nil {
		t.Fatalf("expected TryLock to recover the maybe-applied acquire, got error: %v", err)
	}
	if lock == nil || lock.FencingToken != 42 || lock.LeaseID != 1 {
		t.Fatalf("expected recovered lock with fencing token 42 and lease 1, got %+v", lock)
	}
	if revoked {
		t.Fatal("must not revoke a lease that the maybe-applied transaction actually bound")
	}
}

// TestTryLock_TransactionError_AmbiguousReadFinds_DifferentLease covers the
// re-read observing a DIFFERENT lease than the one this attempt granted
// (someone else's lock, or a lease ID coincidence): the original ambiguous
// error must still be reported, and the lease revoked, exactly as for any
// other proven-not-applied propose failure.
func TestTryLock_TransactionError_AmbiguousReadFinds_DifferentLease(t *testing.T) {
	txErr := errors.New("tx error: outcome unknown")
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			return nil, txErr
		},
	}
	revoked := false
	ls := &mockLeaseService{
		revokeFn: func(_ context.Context, _ int64) error {
			revoked = true
			return nil
		},
	}
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{
				Value:       encodeLockValue(t, lockValue{HolderID: "other", LeaseID: 999, TTL: 1, AcquiredAt: time.Now().Unix()}),
				LeaseID:     999,
				ModRevision: 7,
			}, nil
		},
	}
	svc := mustNewLocker(t, tx, ls, &mockWatchService{}, kvs)

	_, err := svc.TryLock(context.Background(), LockOptions{Key: "k", HolderID: "h", TTL: time.Second})
	if !errors.Is(err, txErr) {
		t.Fatalf("expected the original ambiguous error, got: %v", err)
	}
	if !revoked {
		t.Fatal("expected the lease to be revoked when the re-read shows a different owner")
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

// TestTryLock_ReleasedBeforeReread_Acquires pins that a lock released between
// TryLock's failed CAS and its read of the holder is taken on a second CAS
// with the lease already granted, rather than surfacing the read's
// key-not-found error.
func TestTryLock_ReleasedBeforeReread_Acquires(t *testing.T) {
	casCalls := 0
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, compares []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			if compares[0].Target == CompareModRevision {
				return &TransactionResponse{Succeeded: true}, nil // fencing-token update
			}
			casCalls++
			return &TransactionResponse{Succeeded: casCalls > 1, Revision: 12}, nil
		},
	}
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, key string) (*kv.KeyValue, error) {
			return nil, &keyNotFoundDoubleError{key: key}
		},
	}
	revoked := false
	leases := &mockLeaseService{revokeFn: func(context.Context, int64) error { revoked = true; return nil }}
	svc := mustNewLocker(t, tx, leases, &mockWatchService{}, kvs)

	got, err := svc.TryLock(context.Background(), LockOptions{Key: "k", HolderID: "h", TTL: time.Second})
	if err != nil {
		t.Fatalf("TryLock error = %v, want the lock", err)
	}
	if casCalls != 2 || got.FencingToken != 12 || got.LeaseID != 1 || got.HolderID != "h" {
		t.Fatalf("TryLock = %+v after %d CAS attempts, want token 12 on lease 1 after 2", got, casCalls)
	}
	if revoked {
		t.Fatal("TryLock revoked the lease the acquired lock is bound to")
	}
}

// TestTryLock_ReleasedThenRetaken_ReportsHeld pins that when the retried CAS
// loses to a caller that took the released lock first, TryLock reports that
// caller as the holder and revokes its own lease.
func TestTryLock_ReleasedThenRetaken_ReportsHeld(t *testing.T) {
	casCalls := 0
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			casCalls++
			return &TransactionResponse{Succeeded: false}, nil
		},
	}
	reads := 0
	other := encodeLockValue(t, lockValue{HolderID: "other", AcquireCount: 1, LeaseID: 5, TTL: 30})
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, key string) (*kv.KeyValue, error) {
			reads++
			if reads == 1 {
				return nil, &keyNotFoundDoubleError{key: key}
			}
			return &kv.KeyValue{Key: []byte(key), Value: other, CreateRevision: 20, ModRevision: 20}, nil
		},
	}
	var revoked []int64
	leases := &mockLeaseService{revokeFn: func(_ context.Context, id int64) error { revoked = append(revoked, id); return nil }}
	svc := mustNewLocker(t, tx, leases, &mockWatchService{}, kvs)

	_, err := svc.TryLock(context.Background(), LockOptions{Key: "k", HolderID: "h", TTL: time.Second})
	held, ok := errors.AsType[*LockHeldError](err)
	if !ok || *held != (LockHeldError{Key: "k", HolderID: "other"}) {
		t.Fatalf("TryLock error = %v, want LockHeldError{k, other}", err)
	}
	if casCalls != 2 || !reflect.DeepEqual(revoked, []int64{1}) {
		t.Fatalf("%d CAS attempts, revoked %v; want 2 attempts and lease 1 revoked", casCalls, revoked)
	}
}

// TestTryLock_ReleaseRaceEndsWithContext pins that the retry after a release
// race stops when the caller's context does: TryLock returns the context's
// error and revokes its lease, never the read's key-not-found error.
func TestTryLock_ReleaseRaceEndsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			return &TransactionResponse{Succeeded: false}, nil
		},
	}
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, key string) (*kv.KeyValue, error) {
			cancel()
			return nil, &keyNotFoundDoubleError{key: key}
		},
	}
	revoked := false
	leases := &mockLeaseService{revokeFn: func(context.Context, int64) error { revoked = true; return nil }}
	svc := mustNewLocker(t, tx, leases, &mockWatchService{}, kvs)

	_, err := svc.TryLock(ctx, LockOptions{Key: "k", HolderID: "h", TTL: time.Second})
	if !errors.Is(err, context.Canceled) || isKeyNotFound(err) {
		t.Fatalf("TryLock error = %v, want context.Canceled", err)
	}
	if !revoked {
		t.Fatal("TryLock kept the lease of a lock it did not acquire")
	}
}

// reentryRace is a store double for a reentrant TryLock whose first acquire
// CAS finds the lock held by the same reentrant holder at revision 10 on
// lease 5. reentryWins decides the outcome of the re-entry's CAS; every later
// acquire CAS succeeds at revision 12.
type reentryRace struct {
	reentryWins   bool
	acquireCalls  int
	reentryCmps   []Compare
	reentryOps    []Op
	revokedLeases []int64
}

func (r *reentryRace) locker(t *testing.T) *Service {
	t.Helper()
	existing := encodeLockValue(t, lockValue{HolderID: "h", Reentrant: true, AcquireCount: 1, LeaseID: 5, TTL: 30})
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, compares []Compare, successOps []Op, _ []Op) (*TransactionResponse, error) {
			if compares[0].Target == CompareModRevision {
				r.reentryCmps, r.reentryOps = compares, successOps
				return &TransactionResponse{Succeeded: r.reentryWins, Revision: 11}, nil
			}
			r.acquireCalls++
			return &TransactionResponse{Succeeded: r.acquireCalls > 1, Revision: 12}, nil
		},
	}
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, key string) (*kv.KeyValue, error) {
			// Created at 9 and re-entered once since, at 10.
			return &kv.KeyValue{Key: []byte(key), Value: existing, CreateRevision: 9, ModRevision: 10}, nil
		},
	}
	leases := &mockLeaseService{revokeFn: func(_ context.Context, id int64) error {
		r.revokedLeases = append(r.revokedLeases, id)
		return nil
	}}
	return mustNewLocker(t, tx, leases, &mockWatchService{}, kvs)
}

func TestTryLock_ReentryIsCASOnReadRevision(t *testing.T) {
	r := &reentryRace{reentryWins: true}
	got, err := r.locker(t).TryLock(context.Background(), LockOptions{Key: "k", HolderID: "h", TTL: time.Second, Reentrant: true})
	if err != nil {
		t.Fatalf("TryLock error = %v, want the re-entered lock", err)
	}
	if len(r.reentryCmps) != 1 || r.reentryCmps[0].Result != CompareEqual || r.reentryCmps[0].ModRevision != 10 ||
		string(r.reentryCmps[0].Key) != lockKey("k") {
		t.Fatalf("re-entry compares = %+v, want ModRevision == 10 on %q", r.reentryCmps, lockKey("k"))
	}
	if len(r.reentryOps) != 1 || r.reentryOps[0].LeaseID != 5 {
		t.Fatalf("re-entry ops = %+v, want one put on the existing lease 5", r.reentryOps)
	}
	if got.AcquireCount != 2 || got.FencingToken != 9 || got.LeaseID != 5 {
		t.Fatalf("TryLock = %+v, want count 2, token 9 on lease 5", got)
	}
	if len(r.revokedLeases) != 1 || r.revokedLeases[0] == 5 {
		t.Fatalf("revoked leases = %v, want only the newly granted one", r.revokedLeases)
	}
}

func TestTryLock_ReleasedBeforeReentry_Acquires(t *testing.T) {
	r := &reentryRace{reentryWins: false}
	got, err := r.locker(t).TryLock(context.Background(), LockOptions{Key: "k", HolderID: "h", TTL: time.Second, Reentrant: true})
	if err != nil {
		t.Fatalf("TryLock error = %v, want the freshly acquired lock", err)
	}
	if r.reentryCmps == nil {
		t.Fatal("TryLock never attempted the re-entry")
	}
	if r.acquireCalls != 2 || got.FencingToken != 12 || got.LeaseID != 1 || got.AcquireCount != 1 {
		t.Fatalf("TryLock = %+v after %d acquire CAS, want a new lock (token 12, lease 1, count 1) after 2", got, r.acquireCalls)
	}
	if len(r.revokedLeases) != 0 {
		t.Fatalf("revoked leases = %v, want none: the acquired lock is bound to the new lease", r.revokedLeases)
	}
}

func TestTryLock_ReentryRaceEndsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &reentryRace{reentryWins: false}
	svc := r.locker(t)
	tx := svc.txSvc.(*mockTransactionService)
	execute := tx.executeFn
	tx.executeFn = func(ctx context.Context, compares []Compare, successOps, failureOps []Op) (*TransactionResponse, error) {
		resp, err := execute(ctx, compares, successOps, failureOps)
		if len(compares) == 1 && compares[0].Target == CompareModRevision {
			cancel()
		}
		return resp, err
	}

	_, err := svc.TryLock(ctx, LockOptions{Key: "k", HolderID: "h", TTL: time.Second, Reentrant: true})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("TryLock error = %v, want context.Canceled", err)
	}
	if r.acquireCalls != 1 {
		t.Fatalf("acquire CAS attempts = %d, want 1: a canceled TryLock retries nothing", r.acquireCalls)
	}
	if len(r.revokedLeases) != 1 || r.revokedLeases[0] != 1 {
		t.Fatalf("revoked leases = %v, want the newly granted lease 1", r.revokedLeases)
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
			return &kv.KeyValue{Value: existingBytes, CreateRevision: 10, ModRevision: 10}, nil
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
			return &kv.KeyValue{Value: existingBytes, CreateRevision: 1, ModRevision: 1}, nil
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
			return &kv.KeyValue{Value: existingBytes, CreateRevision: 1, ModRevision: 1}, nil
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
			return &kv.KeyValue{Value: existingBytes, CreateRevision: 1, ModRevision: 1}, nil
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

func TestUnlock_DeleteCASAlwaysFails_ReturnsContended(t *testing.T) {
	lv := lockValue{HolderID: "h1", LeaseID: 9, TTL: 30, AcquireCount: 1}
	lvBytes := encodeLockValue(t, lv)

	var executes, reads, revokes int
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			// Every CAS delete loses to a concurrent write to the key.
			executes++
			return &TransactionResponse{Succeeded: false, Revision: int64(20 + executes)}, nil
		},
	}
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			reads++
			return &kv.KeyValue{Value: lvBytes, CreateRevision: 10, ModRevision: int64(10 + reads)}, nil
		},
	}
	ls := &mockLeaseService{
		revokeFn: func(_ context.Context, _ int64) error {
			revokes++
			return nil
		},
	}
	svc := mustNewLocker(t, tx, ls, &mockWatchService{}, kvs)

	err := svc.Unlock(context.Background(), "res", 10)
	if !errors.Is(err, ErrUnlockContended) {
		t.Fatalf("Unlock = %v, want ErrUnlockContended once every CAS delete fails", err)
	}
	if executes != maxUnlockAttempts || reads != maxUnlockAttempts {
		t.Errorf("executes = %d, reads = %d, want %d of each", executes, reads, maxUnlockAttempts)
	}
	if revokes != 0 {
		t.Errorf("revokes = %d, want 0: a lock that was not deleted keeps its lease", revokes)
	}
}

func TestUnlock_DeleteCASFails_RetriesAndReleases(t *testing.T) {
	lv := lockValue{HolderID: "h1", LeaseID: 9, TTL: 30, AcquireCount: 1}
	lvBytes := encodeLockValue(t, lv)

	var executes int
	var deleteRevisions []int64
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, compares []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			executes++
			deleteRevisions = append(deleteRevisions, compares[0].ModRevision)
			// The first CAS delete loses to a concurrent write; the second
			// is made against the re-read revision and wins.
			return &TransactionResponse{Succeeded: executes > 1, Revision: 30}, nil
		},
	}
	var reads int
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			reads++
			return &kv.KeyValue{Value: lvBytes, CreateRevision: 10, ModRevision: int64(10 + reads)}, nil
		},
	}
	var revoked []int64
	ls := &mockLeaseService{
		revokeFn: func(_ context.Context, id int64) error {
			revoked = append(revoked, id)
			return nil
		},
	}
	svc := mustNewLocker(t, tx, ls, &mockWatchService{}, kvs)

	if err := svc.Unlock(context.Background(), "res", 10); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if len(deleteRevisions) != 2 || deleteRevisions[0] != 11 || deleteRevisions[1] != 12 {
		t.Errorf("CAS deletes on ModRevision %v, want [11 12]: the retry re-reads", deleteRevisions)
	}
	if len(revoked) != 1 || revoked[0] != 9 {
		t.Errorf("revoked %v, want [9] once, after the delete that won", revoked)
	}
}

func TestUnlock_DeleteCASFails_ReacquiredByAnother_ReturnsNotHolder(t *testing.T) {
	lv := lockValue{HolderID: "h1", LeaseID: 9, TTL: 30, AcquireCount: 1}
	lvBytes := encodeLockValue(t, lv)

	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			return &TransactionResponse{Succeeded: false, Revision: 25}, nil
		},
	}
	var reads int
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			reads++
			if reads == 1 {
				return &kv.KeyValue{Value: lvBytes, CreateRevision: 10, ModRevision: 10}, nil
			}
			// Between the read and the delete the lock expired and was
			// re-acquired: the key was created afresh at 25.
			return &kv.KeyValue{Value: lvBytes, CreateRevision: 25, ModRevision: 25}, nil
		},
	}
	svc := mustNewLocker(t, tx, &mockLeaseService{}, &mockWatchService{}, kvs)

	err := svc.Unlock(context.Background(), "res", 10)
	var notHolder *NotLockHolderError
	if !errors.As(err, &notHolder) {
		t.Fatalf("Unlock = %v, want NotLockHolderError after a re-acquisition", err)
	}
	if notHolder.ExpectedToken != 10 || notHolder.ActualToken != 25 {
		t.Errorf("tokens = (%d, %d), want (10, 25)", notHolder.ExpectedToken, notHolder.ActualToken)
	}
}

func TestUnlock_ContextCancelledAfterLostDelete(t *testing.T) {
	lv := lockValue{HolderID: "h1", LeaseID: 9, TTL: 30, AcquireCount: 1}
	lvBytes := encodeLockValue(t, lv)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tx := &mockTransactionService{
		executeFn: func(_ context.Context, _ []Compare, _ []Op, _ []Op) (*TransactionResponse, error) {
			cancel()
			return &TransactionResponse{Succeeded: false, Revision: 25}, nil
		},
	}
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Value: lvBytes, CreateRevision: 10, ModRevision: 10}, nil
		},
	}
	svc := mustNewLocker(t, tx, &mockLeaseService{}, &mockWatchService{}, kvs)

	if err := svc.Unlock(ctx, "res", 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("Unlock = %v, want context.Canceled", err)
	}
}

func TestExtend_NotFound(t *testing.T) {
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return nil, &keyNotFoundDoubleError{key: lockKey("k")}
		},
	}
	svc := mustNewLocker(t, &mockTransactionService{}, &mockLeaseService{}, &mockWatchService{}, kvs)

	_, err := svc.Extend(context.Background(), "k", 1)
	var notFound *LockNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected LockNotFoundError, got %v", err)
	}
}

// TestExtend_ReadErrorKeepsItsClass pins that a GetRevision failure other
// than absence reaches the caller unchanged rather than as "not found".
func TestExtend_ReadErrorKeepsItsClass(t *testing.T) {
	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return nil, errReadFailed
		},
	}
	svc := mustNewLocker(t, &mockTransactionService{}, &mockLeaseService{}, &mockWatchService{}, kvs)

	lk, err := svc.Extend(context.Background(), "k", 1)
	if lk != nil {
		t.Fatalf("Extend returned a lock on a failed read: %+v", lk)
	}
	if !errors.Is(err, errReadFailed) {
		t.Fatalf("Extend error = %v, want %v", err, errReadFailed)
	}
	var notFound *LockNotFoundError
	if errors.As(err, &notFound) {
		t.Fatalf("Extend reported a read failure as LockNotFoundError: %v", err)
	}
}

func TestExtend_KeepAliveError(t *testing.T) {
	lv := lockValue{HolderID: "h1", LeaseID: 7, TTL: 30, AcquireCount: 1}
	lvBytes := encodeLockValue(t, lv)

	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Value: lvBytes, CreateRevision: 5, ModRevision: 5}, nil
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
		rangeFn: func(_ context.Context, _, _ []byte, _ ...RangeOption) ([]*kv.KeyValue, int64, int64, error) {
			return nil, 0, 0, rangeErr
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
		rangeFn: func(_ context.Context, _, _ []byte, _ ...RangeOption) ([]*kv.KeyValue, int64, int64, error) {
			return []*kv.KeyValue{
				{Key: []byte(lockKeyPrefix + "bad"), Value: []byte("not-json"), CreateRevision: 1, ModRevision: 1},
			}, 1, 1, nil
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
			return &kv.KeyValue{Value: existingBytes, CreateRevision: 1, ModRevision: 1}, nil
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
			return &kv.KeyValue{Value: existingBytes, CreateRevision: 1, ModRevision: 1}, nil
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
			return &kv.KeyValue{Value: []byte("not-json"), CreateRevision: 1, ModRevision: 1}, nil
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
			return &kv.KeyValue{Value: []byte("bad-json"), CreateRevision: 5, ModRevision: 5}, nil
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
			return &kv.KeyValue{Value: lvBytes, CreateRevision: 10, ModRevision: 10}, nil
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
			return &kv.KeyValue{Value: []byte("bad-json"), CreateRevision: 5, ModRevision: 5}, nil
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
			return &kv.KeyValue{Value: []byte("bad-json"), CreateRevision: 3, ModRevision: 3}, nil
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
			return &kv.KeyValue{Value: existingBytes, CreateRevision: 1, ModRevision: 1}, nil
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
			return &kv.KeyValue{Value: lvBytes, CreateRevision: 10, ModRevision: 10}, nil
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
			return &kv.KeyValue{Value: lvBytes, CreateRevision: 10, ModRevision: 10}, nil
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
			return &kv.KeyValue{Value: lvBytes, CreateRevision: 10, ModRevision: 10}, nil
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
	lv := lockValue{HolderID: "h1", Reentrant: true, LeaseID: 7, TTL: 30, AcquireCount: 3, AcquiredAt: 99999}
	lvBytes := encodeLockValue(t, lv)

	kvs := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return &kv.KeyValue{Value: lvBytes, CreateRevision: 42, ModRevision: 42}, nil
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
	lv := lockValue{HolderID: "holder-x", Reentrant: true, LeaseID: 42, TTL: 120, AcquireCount: 5, AcquiredAt: 11111}
	b := encodeLockValue(t, lv)

	kvs := &mockMVCCKVService{
		rangeFn: func(_ context.Context, _, _ []byte, _ ...RangeOption) ([]*kv.KeyValue, int64, int64, error) {
			return []*kv.KeyValue{
				{Key: []byte(lockKeyPrefix + "myres"), Value: b, CreateRevision: 88, ModRevision: 88},
			}, 1, 88, nil
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
			return &kv.KeyValue{Value: existingBytes, CreateRevision: 1, ModRevision: 1}, nil
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
			return &kv.KeyValue{Value: lvBytes, CreateRevision: 10, ModRevision: 10}, nil
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
			return &kv.KeyValue{Value: lvBytes, CreateRevision: 5, ModRevision: 5}, nil
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
			return &kv.KeyValue{Value: existingBytes, CreateRevision: 1, ModRevision: 1}, nil
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
	// TryLock makes one CAS before the watch and one after the delete
	// event: the put watch event must not have triggered a retry.
	if callCount != 2 {
		t.Errorf("Execute callCount = %d, want 2 (one CAS each side of the delete event)", callCount)
	}
	if lock.FencingToken != 10 {
		t.Errorf("FencingToken = %d, want 10", lock.FencingToken)
	}
}
