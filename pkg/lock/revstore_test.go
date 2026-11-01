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
	"bytes"
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
)

// revStore is an in-memory store double faithful to the MVCC revision model
// the lock service runs on: every committed write raises one store-global
// revision; a put that creates a key sets its create and mod revisions to
// that revision and its version to 1; a put on an existing key keeps the
// create revision and raises the mod revision and version; a delete removes
// the key, so the next put creates it afresh at a new create revision.
// Revoking a lease deletes the keys bound to it, as an expiry does.
//
// beforeExecute, when set, runs before every transaction with its 1-based
// sequence number and outside the store's lock, so a test can land another
// caller's write between a read and the CAS that follows it.
type revStore struct {
	mu            sync.Mutex
	revision      int64
	nextLease     int64
	keys          map[string]*kv.KeyValue
	executes      int
	beforeExecute func(n int)
}

var (
	_ TransactionServicer = (*revStore)(nil)
	_ LeaseServicer       = (*revStore)(nil)
	_ MVCCKVServicer      = (*revStore)(nil)
)

func newRevStore() *revStore {
	return &revStore{keys: make(map[string]*kv.KeyValue)}
}

func (r *revStore) locker(t *testing.T) *Service {
	t.Helper()
	return mustNewLocker(t, r, r, &mockWatchService{}, r)
}

func (r *revStore) Execute(_ context.Context, compares []Compare, successOps, failureOps []Op) (*TransactionResponse, error) {
	r.mu.Lock()
	r.executes++
	n := r.executes
	hook := r.beforeExecute
	r.mu.Unlock()
	if hook != nil {
		hook(n)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	succeeded := true
	for _, c := range compares {
		if !r.holds(c) {
			succeeded = false
			break
		}
	}
	ops := successOps
	if !succeeded {
		ops = failureOps
	}
	written := false
	for _, op := range ops {
		switch op.Type {
		case OpPut:
			if !written {
				r.revision++
				written = true
			}
			r.put(op, r.revision)
		case OpDelete:
			if _, ok := r.keys[string(op.Key)]; ok {
				if !written {
					r.revision++
					written = true
				}
				delete(r.keys, string(op.Key))
			}
		}
	}
	return &TransactionResponse{Succeeded: succeeded, Revision: r.revision}, nil
}

func (r *revStore) holds(c Compare) bool {
	var got, want int64
	cur := r.keys[string(c.Key)]
	switch c.Target {
	case CompareVersion:
		want = c.Version
		if cur != nil {
			got = cur.Version
		}
	case CompareCreateRevision:
		want = c.CreateRevision
		if cur != nil {
			got = cur.CreateRevision
		}
	case CompareModRevision:
		want = c.ModRevision
		if cur != nil {
			got = cur.ModRevision
		}
	default:
		return false
	}
	switch c.Result {
	case CompareEqual:
		return got == want
	case CompareNotEqual:
		return got != want
	case CompareGreater:
		return got > want
	case CompareLess:
		return got < want
	}
	return false
}

func (r *revStore) put(op Op, revision int64) {
	cur, ok := r.keys[string(op.Key)]
	if !ok {
		r.keys[string(op.Key)] = &kv.KeyValue{
			Key:            bytes.Clone(op.Key),
			Value:          bytes.Clone(op.Value),
			CreateRevision: revision,
			ModRevision:    revision,
			Version:        1,
			LeaseID:        op.LeaseID,
		}
		return
	}
	cur.Value = bytes.Clone(op.Value)
	cur.ModRevision = revision
	cur.Version++
	cur.LeaseID = op.LeaseID
}

func (r *revStore) Grant(_ context.Context, ttl int64) (*lease.Lease, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextLease++
	return &lease.Lease{ID: r.nextLease, TTL: ttl, GrantedTTL: ttl}, nil
}

// Revoke deletes every key bound to leaseID in one write, as an expiry does.
func (r *revStore) Revoke(_ context.Context, leaseID int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	written := false
	for k, v := range r.keys {
		if v.LeaseID != leaseID {
			continue
		}
		if !written {
			r.revision++
			written = true
		}
		delete(r.keys, k)
	}
	return nil
}

func (r *revStore) KeepAlive(_ context.Context, leaseID int64) (*lease.Lease, error) {
	return &lease.Lease{ID: leaseID, TTL: 30, GrantedTTL: 30}, nil
}

func (r *revStore) GetRevision(_ context.Context, key string) (*kv.KeyValue, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur, ok := r.keys[key]
	if !ok {
		return nil, &keyNotFoundDoubleError{key: key}
	}
	out := *cur
	return &out, nil
}

func (r *revStore) Range(_ context.Context, start, end []byte, _ ...RangeOption) ([]*kv.KeyValue, int64, int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*kv.KeyValue
	for k, v := range r.keys {
		if bytes.Compare([]byte(k), start) >= 0 && (end == nil || bytes.Compare([]byte(k), end) < 0) {
			cp := *v
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i].Key, out[j].Key) < 0 })
	return out, int64(len(out)), r.revision, nil
}

// stored returns the lock key's current record, or nil when it is absent.
func (r *revStore) stored(key string) *kv.KeyValue {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur, ok := r.keys[lockKey(key)]
	if !ok {
		return nil
	}
	out := *cur
	return &out
}

func reentrantOpts(key string) LockOptions {
	return LockOptions{Key: key, HolderID: "h", TTL: 30 * time.Second, Reentrant: true}
}

func TestTryLock_AcquireIsTheOnlyWrite(t *testing.T) {
	r := newRevStore()
	got, err := r.locker(t).TryLock(context.Background(), LockOptions{Key: "k", HolderID: "h", TTL: time.Second})
	if err != nil {
		t.Fatalf("TryLock error = %v", err)
	}
	stored := r.stored("k")
	if r.executes != 1 || stored == nil || stored.Version != 1 {
		t.Fatalf("TryLock made %d transactions leaving %+v, want the acquire alone at version 1", r.executes, stored)
	}
	if got.FencingToken != stored.CreateRevision {
		t.Fatalf("FencingToken = %d, want the key's create revision %d", got.FencingToken, stored.CreateRevision)
	}
	if strings.Contains(string(stored.Value), "fencing_token") {
		t.Fatalf("stored value %s carries a token; the token is the create revision", stored.Value)
	}
}

func TestTryLock_ReentryKeepsCreateRevision(t *testing.T) {
	r := newRevStore()
	l := r.locker(t)
	first, err := l.TryLock(context.Background(), reentrantOpts("k"))
	if err != nil {
		t.Fatalf("TryLock error = %v", err)
	}
	for want := int32(2); want <= 3; want++ {
		again, err := l.TryLock(context.Background(), reentrantOpts("k"))
		if err != nil {
			t.Fatalf("re-entry error = %v", err)
		}
		if again.FencingToken != first.FencingToken || again.AcquireCount != want {
			t.Fatalf("re-entry = %+v, want token %d at count %d", again, first.FencingToken, want)
		}
	}
	stored := r.stored("k")
	if stored.ModRevision == first.FencingToken {
		t.Fatalf("mod revision %d did not move; the test would not tell the two revisions apart", stored.ModRevision)
	}
	status, err := l.Status(context.Background(), "k")
	if err != nil || status.FencingToken != first.FencingToken {
		t.Fatalf("Status = %+v, %v; want token %d", status, err, first.FencingToken)
	}
	locks, err := l.List(context.Background(), "")
	if err != nil || len(locks) != 1 || locks[0].FencingToken != first.FencingToken {
		t.Fatalf("List = %+v, %v; want one lock with token %d", locks, err, first.FencingToken)
	}
}

func TestUnlock_ChecksCreateRevision(t *testing.T) {
	r := newRevStore()
	l := r.locker(t)
	first, err := l.TryLock(context.Background(), reentrantOpts("k"))
	if err != nil {
		t.Fatalf("TryLock error = %v", err)
	}
	if _, err := l.TryLock(context.Background(), reentrantOpts("k")); err != nil {
		t.Fatalf("re-entry error = %v", err)
	}
	modRevision := r.stored("k").ModRevision

	// The mod revision is not the token: Unlock refuses it and names the
	// create revision.
	err = l.Unlock(context.Background(), "k", modRevision)
	var notHolder *NotLockHolderError
	if !errors.As(err, &notHolder) || notHolder.ActualToken != first.FencingToken || notHolder.ExpectedToken != modRevision {
		t.Fatalf("Unlock(mod revision) = %v, want NotLockHolderError naming token %d", err, first.FencingToken)
	}

	if err := l.Unlock(context.Background(), "k", first.FencingToken); err != nil {
		t.Fatalf("first Unlock error = %v", err)
	}
	if stored := r.stored("k"); stored == nil || stored.CreateRevision != first.FencingToken {
		t.Fatalf("after the decrement the key is %+v, want it held at create revision %d", stored, first.FencingToken)
	}
	if err := l.Unlock(context.Background(), "k", first.FencingToken); err != nil {
		t.Fatalf("second Unlock error = %v", err)
	}
	if stored := r.stored("k"); stored != nil {
		t.Fatalf("after the last Unlock the key is %+v, want it released", stored)
	}
}

func TestExtend_ChecksCreateRevision(t *testing.T) {
	r := newRevStore()
	l := r.locker(t)
	first, err := l.TryLock(context.Background(), reentrantOpts("k"))
	if err != nil {
		t.Fatalf("TryLock error = %v", err)
	}
	if _, err := l.TryLock(context.Background(), reentrantOpts("k")); err != nil {
		t.Fatalf("re-entry error = %v", err)
	}

	_, err = l.Extend(context.Background(), "k", r.stored("k").ModRevision)
	var notHolder *NotLockHolderError
	if !errors.As(err, &notHolder) || notHolder.ActualToken != first.FencingToken {
		t.Fatalf("Extend(mod revision) = %v, want NotLockHolderError naming token %d", err, first.FencingToken)
	}

	extended, err := l.Extend(context.Background(), "k", first.FencingToken)
	if err != nil {
		t.Fatalf("Extend error = %v", err)
	}
	if extended.FencingToken != first.FencingToken || extended.AcquireCount != 2 {
		t.Fatalf("Extend = %+v, want token %d at count 2", extended, first.FencingToken)
	}
}

func TestTryLock_ReacquireGetsNewToken(t *testing.T) {
	r := newRevStore()
	l := r.locker(t)
	first, err := l.TryLock(context.Background(), reentrantOpts("k"))
	if err != nil {
		t.Fatalf("TryLock error = %v", err)
	}
	if err := l.Unlock(context.Background(), "k", first.FencingToken); err != nil {
		t.Fatalf("Unlock error = %v", err)
	}
	second, err := l.TryLock(context.Background(), reentrantOpts("k"))
	if err != nil {
		t.Fatalf("re-acquire error = %v", err)
	}
	if second.FencingToken <= first.FencingToken {
		t.Fatalf("re-acquired token %d, want one above the released token %d", second.FencingToken, first.FencingToken)
	}

	// The released holder's token is stale on every operation.
	var notHolder *NotLockHolderError
	if err := l.Unlock(context.Background(), "k", first.FencingToken); !errors.As(err, &notHolder) {
		t.Fatalf("Unlock(stale token) = %v, want NotLockHolderError", err)
	}
	if _, err := l.Extend(context.Background(), "k", first.FencingToken); !errors.As(err, &notHolder) {
		t.Fatalf("Extend(stale token) = %v, want NotLockHolderError", err)
	}
	if stored := r.stored("k"); stored == nil || stored.CreateRevision != second.FencingToken {
		t.Fatalf("stale calls left the key %+v, want it held at %d", stored, second.FencingToken)
	}
}

func TestUnlock_ReentryBeforeDelete_Decrements(t *testing.T) {
	r := newRevStore()
	l := r.locker(t)
	held, err := l.TryLock(context.Background(), reentrantOpts("k"))
	if err != nil {
		t.Fatalf("TryLock error = %v", err)
	}

	// Unlock reads a count of one and deletes; the holder re-enters between
	// the two, so the delete must lose its CAS and the retry decrement.
	fired := false
	r.beforeExecute = func(int) {
		if fired {
			return
		}
		fired = true
		if _, err := l.TryLock(context.Background(), reentrantOpts("k")); err != nil {
			t.Errorf("re-entry error = %v", err)
		}
	}
	if err := l.Unlock(context.Background(), "k", held.FencingToken); err != nil {
		t.Fatalf("Unlock error = %v", err)
	}
	r.beforeExecute = nil

	status, err := l.Status(context.Background(), "k")
	if err != nil {
		t.Fatalf("Status error = %v: the re-entered lock was deleted", err)
	}
	if status.AcquireCount != 1 || status.FencingToken != held.FencingToken {
		t.Fatalf("lock = (count %d, token %d), want (1, %d)", status.AcquireCount, status.FencingToken, held.FencingToken)
	}
	if err := l.Unlock(context.Background(), "k", held.FencingToken); err != nil {
		t.Fatalf("final Unlock error = %v", err)
	}
	if stored := r.stored("k"); stored != nil {
		t.Fatalf("final Unlock left the key %+v", stored)
	}
}

func TestUnlock_ReleaseAndReacquireBeforeDelete_RefusesStaleToken(t *testing.T) {
	r := newRevStore()
	l := r.locker(t)
	held, err := l.TryLock(context.Background(), reentrantOpts("k"))
	if err != nil {
		t.Fatalf("TryLock error = %v", err)
	}

	// Between Unlock's read and its delete the lock is released and taken
	// by another holder: the delete loses and the re-read finds a new token.
	fired := false
	var other *Lock
	r.beforeExecute = func(int) {
		if fired {
			return
		}
		fired = true
		r.mu.Lock()
		delete(r.keys, lockKey("k"))
		r.revision++
		r.mu.Unlock()
		opts := reentrantOpts("k")
		opts.HolderID = "other"
		if other, err = l.TryLock(context.Background(), opts); err != nil {
			t.Errorf("other holder's TryLock error = %v", err)
		}
	}
	var notHolder *NotLockHolderError
	if err := l.Unlock(context.Background(), "k", held.FencingToken); !errors.As(err, &notHolder) {
		t.Fatalf("Unlock = %v, want NotLockHolderError", err)
	}
	r.beforeExecute = nil
	if other == nil || notHolder.ActualToken != other.FencingToken {
		t.Fatalf("ActualToken = %d, want the other holder's token %+v", notHolder.ActualToken, other)
	}
	if stored := r.stored("k"); stored == nil || stored.CreateRevision != other.FencingToken {
		t.Fatalf("stale Unlock disturbed the other holder's lock: %+v", stored)
	}
}

func TestTryLock_ReacquireAfterExpiryGetsNewToken(t *testing.T) {
	// The same holder re-enters after its lease expired: the key is created
	// afresh, so the call is a new acquisition with a new token, not a
	// re-entry carrying the expired one.
	r := newRevStore()
	l := r.locker(t)
	first, err := l.TryLock(context.Background(), reentrantOpts("k"))
	if err != nil {
		t.Fatalf("TryLock error = %v", err)
	}
	if err := r.Revoke(context.Background(), first.LeaseID); err != nil {
		t.Fatalf("Revoke error = %v", err)
	}
	second, err := l.TryLock(context.Background(), reentrantOpts("k"))
	if err != nil {
		t.Fatalf("re-acquire error = %v", err)
	}
	if second.FencingToken <= first.FencingToken || second.AcquireCount != 1 {
		t.Fatalf("re-acquire = %+v, want a new token above %d at count 1", second, first.FencingToken)
	}
}
