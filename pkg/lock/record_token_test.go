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
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
)

// transientError is a failure the transport reports when it cannot say
// whether a write was applied, or the leader moved under it.
type transientError struct{}

func (transientError) Error() string { return "lock test: transient" }

// finalError is a failure that proves the write can never land.
type finalError struct{}

func (finalError) Error() string { return "lock test: final" }

func isTransient(err error) bool { return errors.As(err, new(transientError)) }

// flakyStore serves the acquire, then fails the next fails transactions
// with err before it serves the rest. A failure with applied set commits the
// write first, as a proposal whose response was lost; one without refuses it.
// onFail, if set, runs at each failure.
type flakyStore struct {
	*revStore
	err     error
	fails   int
	applied bool
	onFail  func()
	calls   int
}

func (f *flakyStore) Execute(ctx context.Context, compares []Compare, successOps, failureOps []Op) (*TransactionResponse, error) {
	f.calls++
	if f.calls == 1 || f.calls-1 > f.fails {
		return f.revStore.Execute(ctx, compares, successOps, failureOps)
	}
	if f.onFail != nil {
		f.onFail()
	}
	if f.applied {
		if _, err := f.revStore.Execute(ctx, compares, successOps, failureOps); err != nil {
			return nil, err
		}
	}
	return nil, f.err
}

func flakyLocker(t *testing.T, f *flakyStore, fake *clocktest.Fake) *Service {
	t.Helper()
	l, err := NewLocker(f, f.revStore, &mockWatchService{}, f.revStore, nil,
		WithTokenFieldRetired(notRetired), WithRetryable(isTransient), WithClock(fake))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// tryLockThroughBackoffs runs TryLock while advancing the fake clock past
// each of backoffs waits recordToken arms, and returns TryLock's result.
func tryLockThroughBackoffs(t *testing.T, l *Service, fake *clocktest.Fake, backoffs int) (*Lock, error) {
	t.Helper()
	type result struct {
		lock *Lock
		err  error
	}
	done := make(chan result, 1)
	go func() {
		got, err := l.TryLock(context.Background(), LockOptions{Key: "k", HolderID: "h", TTL: 30 * time.Second})
		done <- result{got, err}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for range backoffs {
		if err := fake.BlockUntil(ctx, 1); err != nil {
			t.Fatalf("recordToken armed no backoff: %v", err)
		}
		fake.Advance(tokenStoreBackoff)
	}
	select {
	case r := <-done:
		return r.lock, r.err
	case <-ctx.Done():
		t.Fatal("TryLock did not return")
		return nil, nil
	}
}

// A store-back refused once by a transient failure lands on the retry.
func TestRecordToken_TransientFailureThenLands(t *testing.T) {
	fake := clocktest.New()
	f := &flakyStore{revStore: newRevStore(), err: transientError{}, fails: 1}
	got, err := tryLockThroughBackoffs(t, flakyLocker(t, f, fake), fake, 1)
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	if f.calls != 3 {
		t.Fatalf("transactions = %d, want the acquire and two store-backs", f.calls)
	}
	if token, ok := storedTokenField(t, f.revStore, "k"); !ok || token != got.FencingToken {
		t.Fatalf("stored fencing_token = %d (present %v), want %d", token, ok, got.FencingToken)
	}
}

// A store-back that committed but lost its response is retried; the retry's
// compare loses to the committed write and writes nothing, and the token
// stays stored.
func TestRecordToken_AppliedFailureRetryWritesNothing(t *testing.T) {
	fake := clocktest.New()
	f := &flakyStore{revStore: newRevStore(), err: transientError{}, fails: 1, applied: true}
	got, err := tryLockThroughBackoffs(t, flakyLocker(t, f, fake), fake, 1)
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	stored := f.stored("k")
	if f.calls != 3 || stored.Version != 2 {
		t.Fatalf("transactions = %d leaving version %d, want three and one store-back applied", f.calls, stored.Version)
	}
	if token, ok := storedTokenField(t, f.revStore, "k"); !ok || token != got.FencingToken {
		t.Fatalf("stored fencing_token = %d (present %v), want %d", token, ok, got.FencingToken)
	}
}

// Retries stop at maxTokenStoreAttempts; the lock stays held without the
// field.
func TestRecordToken_RetriesExhausted(t *testing.T) {
	fake := clocktest.New()
	f := &flakyStore{revStore: newRevStore(), err: transientError{}, fails: maxTokenStoreAttempts + 1}
	got, err := tryLockThroughBackoffs(t, flakyLocker(t, f, fake), fake, maxTokenStoreAttempts-1)
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	if f.calls != 1+maxTokenStoreAttempts {
		t.Fatalf("transactions = %d, want the acquire and %d store-backs", f.calls, maxTokenStoreAttempts)
	}
	if stored := f.stored("k"); stored == nil || stored.CreateRevision != got.FencingToken {
		t.Fatalf("lock = %+v, want it held at token %d", stored, got.FencingToken)
	}
	if _, ok := storedTokenField(t, f.revStore, "k"); ok {
		t.Fatal("fencing_token stored though every store-back failed")
	}
}

// A failure the classifier calls final is not retried.
func TestRecordToken_FinalFailureNotRetried(t *testing.T) {
	fake := clocktest.New()
	f := &flakyStore{revStore: newRevStore(), err: finalError{}, fails: maxTokenStoreAttempts}
	if _, err := tryLockThroughBackoffs(t, flakyLocker(t, f, fake), fake, 0); err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	if f.calls != 2 {
		t.Fatalf("transactions = %d, want the acquire and one store-back", f.calls)
	}
}

// A store-back whose compare lost because the record moved is not retried,
// and leaves the moved record alone.
func TestRecordToken_MovedRevisionNotRetried(t *testing.T) {
	fake := clocktest.New()
	f := &flakyStore{revStore: newRevStore()}
	moved := []byte(`{"holder_id":"h","acquire_count":2}`)
	f.beforeExecute = func(n int) {
		if n != 2 {
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.revision++
		cur := f.keys[lockKey("k")]
		f.put(Op{Type: OpPut, Key: cur.Key, Value: moved, LeaseID: cur.LeaseID}, f.revision)
	}
	if _, err := tryLockThroughBackoffs(t, flakyLocker(t, f, fake), fake, 0); err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	if f.calls != 2 {
		t.Fatalf("transactions = %d, want the acquire and one store-back", f.calls)
	}
	if got := string(f.stored("k").Value); got != string(moved) {
		t.Fatalf("record = %s, want the moved record %s", got, moved)
	}
}

// A context that ends stops the retries without waiting out the backoff.
func TestRecordToken_CancelledContextStopsRetries(t *testing.T) {
	fake := clocktest.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &flakyStore{revStore: newRevStore(), err: transientError{}, fails: maxTokenStoreAttempts, onFail: cancel}
	l := flakyLocker(t, f, fake)
	got, err := l.TryLock(ctx, LockOptions{Key: "k", HolderID: "h", TTL: 30 * time.Second})
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	if f.calls != 2 {
		t.Fatalf("transactions = %d, want the acquire and one store-back", f.calls)
	}
	if stored := f.stored("k"); stored == nil || stored.CreateRevision != got.FencingToken {
		t.Fatalf("lock = %+v, want it held at token %d", stored, got.FencingToken)
	}
}

// Without WithRetryable no failure is retried.
func TestRecordToken_NoClassifierNoRetry(t *testing.T) {
	r := newRevStore()
	f := &flakyStore{revStore: r, err: transientError{}, fails: maxTokenStoreAttempts}
	l, err := NewLocker(f, r, &mockWatchService{}, r, nil, WithTokenFieldRetired(notRetired))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.TryLock(context.Background(), LockOptions{Key: "k", HolderID: "h", TTL: 30 * time.Second}); err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	if f.calls != 2 {
		t.Fatalf("transactions = %d, want the acquire and one store-back", f.calls)
	}
}

func TestRetryable(t *testing.T) {
	s := &Service{}
	if s.retryable(transientError{}) {
		t.Fatal("retryable without a classifier = true")
	}
	WithRetryable(isTransient)(s)
	if !s.retryable(transientError{}) || s.retryable(finalError{}) {
		t.Fatal("retryable does not follow the classifier")
	}
}

func TestBackoff(t *testing.T) {
	fake := clocktest.New()
	s := &Service{clock: fake}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if s.backoff(ctx) {
		t.Fatal("backoff on an ended context = true")
	}

	done := make(chan bool, 1)
	go func() { done <- s.backoff(context.Background()) }()
	wait, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	if err := fake.BlockUntil(wait, 1); err != nil {
		t.Fatalf("backoff armed no timer: %v", err)
	}
	fake.Advance(tokenStoreBackoff - time.Nanosecond)
	select {
	case <-done:
		t.Fatal("backoff returned before tokenStoreBackoff elapsed")
	default:
	}
	fake.Advance(time.Nanosecond)
	if !<-done {
		t.Fatal("backoff after the wait = false")
	}
}
