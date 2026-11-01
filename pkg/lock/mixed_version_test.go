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
	"strings"
	"testing"
)

// oldLockValue is the lock record as a binary from before the create-revision
// token wrote it: fencing_token always present.
type oldLockValue struct {
	HolderID     string `json:"holder_id"`
	Reentrant    bool   `json:"reentrant"`
	AcquireCount int32  `json:"acquire_count"`
	AcquiredAt   int64  `json:"acquired_at"`
	LeaseID      int64  `json:"lease_id"`
	TTL          int64  `json:"ttl"`
	FencingToken int64  `json:"fencing_token"`
}

// oldNode is a node running a binary from before the create-revision token,
// over the same replicated store: it reads the token from the record's
// fencing_token field (falling back to the mod revision where it is zero),
// stores it with a second write after an acquire, and preserves it on
// re-entry and decrement. Its logic is that binary's, transcribed.
type oldNode struct{ r *revStore }

func (o oldNode) read(t *testing.T, key string) (oldLockValue, int64, bool) {
	t.Helper()
	cur := o.r.stored(key)
	if cur == nil {
		return oldLockValue{}, 0, false
	}
	var lv oldLockValue
	if err := json.Unmarshal(cur.Value, &lv); err != nil {
		t.Fatalf("old node cannot read %s: %v", cur.Value, err)
	}
	return lv, cur.ModRevision, true
}

func (o oldNode) put(t *testing.T, key string, lv oldLockValue, leaseID int64) {
	t.Helper()
	value, err := json.Marshal(lv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.r.Execute(context.Background(), nil, []Op{{Type: OpPut, Key: []byte(lockKey(key)), Value: value, LeaseID: leaseID}}, nil); err != nil {
		t.Fatal(err)
	}
}

func (o oldNode) tryLock(t *testing.T, opts LockOptions) (int64, error) {
	t.Helper()
	ctx := context.Background()
	lease, _ := o.r.Grant(ctx, 30)
	lv := oldLockValue{HolderID: opts.HolderID, Reentrant: opts.Reentrant, AcquireCount: 1, LeaseID: lease.ID, TTL: 30}
	value, _ := json.Marshal(lv)
	resp, _ := o.r.Execute(ctx,
		[]Compare{{Key: []byte(lockKey(opts.Key)), Target: CompareVersion, Result: CompareEqual}},
		[]Op{{Type: OpPut, Key: []byte(lockKey(opts.Key)), Value: value, LeaseID: lease.ID}}, nil)
	if resp.Succeeded {
		lv.FencingToken = resp.Revision
		o.put(t, opts.Key, lv, lease.ID)
		return resp.Revision, nil
	}
	_ = o.r.Revoke(ctx, lease.ID)
	existing, _, _ := o.read(t, opts.Key)
	if existing.HolderID != opts.HolderID || !opts.Reentrant || !existing.Reentrant {
		return 0, &LockHeldError{Key: opts.Key, HolderID: existing.HolderID}
	}
	existing.AcquireCount++
	o.put(t, opts.Key, existing, existing.LeaseID)
	return existing.FencingToken, nil
}

func (o oldNode) token(t *testing.T, key string) int64 {
	t.Helper()
	lv, modRevision, ok := o.read(t, key)
	if !ok {
		t.Fatalf("old node finds no lock %q", key)
	}
	if lv.FencingToken == 0 {
		return modRevision
	}
	return lv.FencingToken
}

// status is what the old Status and List report as the token.
func (o oldNode) status(t *testing.T, key string) int64 {
	t.Helper()
	lv, _, ok := o.read(t, key)
	if !ok {
		t.Fatalf("old node finds no lock %q", key)
	}
	return lv.FencingToken
}

func (o oldNode) extend(t *testing.T, key string, token int64) error {
	t.Helper()
	if got := o.token(t, key); got != token {
		return &NotLockHolderError{Key: key, ExpectedToken: token, ActualToken: got}
	}
	return nil
}

func (o oldNode) unlock(t *testing.T, key string, token int64) error {
	t.Helper()
	if got := o.token(t, key); got != token {
		return &NotLockHolderError{Key: key, ExpectedToken: token, ActualToken: got}
	}
	lv, modRevision, _ := o.read(t, key)
	if lv.Reentrant && lv.AcquireCount > 1 {
		lv.AcquireCount--
		o.put(t, key, lv, lv.LeaseID)
		return nil
	}
	ctx := context.Background()
	resp, _ := o.r.Execute(ctx,
		[]Compare{{Key: []byte(lockKey(key)), Target: CompareModRevision, Result: CompareEqual, ModRevision: modRevision}},
		[]Op{{Type: OpDelete, Key: []byte(lockKey(key))}}, nil)
	if !resp.Succeeded {
		return &NotLockHolderError{Key: key, ExpectedToken: token}
	}
	return o.r.Revoke(ctx, lv.LeaseID)
}

// mixedLocker is this binary's locker on a group where an older node may
// still read the stored token.
func mixedLocker(t *testing.T, r *revStore, retired func() bool) *Service {
	t.Helper()
	l, err := NewLocker(r, r, &mockWatchService{}, r, nil, WithTokenFieldRetired(retired))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func notRetired() bool { return false }

func storedTokenField(t *testing.T, r *revStore, key string) (int64, bool) {
	t.Helper()
	cur := r.stored(key)
	if cur == nil {
		t.Fatalf("no lock %q stored", key)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(cur.Value, &fields); err != nil {
		t.Fatal(err)
	}
	raw, ok := fields["fencing_token"]
	if !ok {
		return 0, false
	}
	var token int64
	if err := json.Unmarshal(raw, &token); err != nil {
		t.Fatal(err)
	}
	return token, true
}

// A lock this binary acquires before activation is read by an older node
// with the token its holder was given: Status, List, Extend and Unlock.
func TestMixedVersion_NewAcquire_OldNodeOperates(t *testing.T) {
	ctx := context.Background()
	for _, reentrant := range []bool{false, true} {
		r := newRevStore()
		l := mixedLocker(t, r, notRetired)
		got, err := l.TryLock(ctx, LockOptions{Key: "k", HolderID: "h", TTL: 30e9, Reentrant: reentrant})
		if err != nil {
			t.Fatalf("TryLock: %v", err)
		}
		if token, ok := storedTokenField(t, r, "k"); !ok || token != got.FencingToken {
			t.Fatalf("stored fencing_token = %d (present %v), want %d", token, ok, got.FencingToken)
		}
		old := oldNode{r}
		if s := old.status(t, "k"); s != got.FencingToken {
			t.Fatalf("old Status/List token = %d, want %d", s, got.FencingToken)
		}
		if err := old.extend(t, "k", got.FencingToken); err != nil {
			t.Fatalf("old Extend: %v", err)
		}
		if err := old.extend(t, "k", got.FencingToken+1); err == nil {
			t.Fatal("old Extend accepted a wrong token")
		}
		if err := old.unlock(t, "k", got.FencingToken); err != nil {
			t.Fatalf("old Unlock: %v", err)
		}
		if r.stored("k") != nil {
			t.Fatal("old Unlock left the lock held")
		}
	}
}

// Re-entries and decrements through this binary keep the stored token, so
// an older node's re-entry returns it and its Unlock accepts it.
func TestMixedVersion_NewReentryAndDecrement_KeepTheStoredToken(t *testing.T) {
	ctx := context.Background()
	r := newRevStore()
	l := mixedLocker(t, r, notRetired)
	old := oldNode{r}
	first, err := l.TryLock(ctx, reentrantOpts("k"))
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	if _, err := l.TryLock(ctx, reentrantOpts("k")); err != nil {
		t.Fatalf("re-entry: %v", err)
	}
	oldToken, err := old.tryLock(t, reentrantOpts("k"))
	if err != nil || oldToken != first.FencingToken {
		t.Fatalf("old re-entry = %d, %v; want token %d", oldToken, err, first.FencingToken)
	}
	if err := l.Unlock(ctx, "k", first.FencingToken); err != nil {
		t.Fatalf("Unlock (decrement): %v", err)
	}
	if token, ok := storedTokenField(t, r, "k"); !ok || token != first.FencingToken {
		t.Fatalf("stored fencing_token after decrement = %d (present %v), want %d", token, ok, first.FencingToken)
	}
	for range 2 {
		if err := old.unlock(t, "k", first.FencingToken); err != nil {
			t.Fatalf("old Unlock: %v", err)
		}
	}
	if r.stored("k") != nil {
		t.Fatal("lock still held after every acquisition was released")
	}
}

// A lock an older node acquires is served by this binary under the same
// token: acquire, re-entry, Status, List, Extend and Unlock.
func TestMixedVersion_OldAcquire_NewNodeOperates(t *testing.T) {
	ctx := context.Background()
	r := newRevStore()
	l := mixedLocker(t, r, notRetired)
	token, err := oldNode{r}.tryLock(t, reentrantOpts("k"))
	if err != nil {
		t.Fatalf("old TryLock: %v", err)
	}
	if _, err := l.TryLock(ctx, LockOptions{Key: "k", HolderID: "other", TTL: 30e9}); !errors.As(err, new(*LockHeldError)) {
		t.Fatalf("TryLock by another holder = %v, want LockHeldError", err)
	}
	again, err := l.TryLock(ctx, reentrantOpts("k"))
	if err != nil || again.FencingToken != token {
		t.Fatalf("re-entry = %+v, %v; want token %d", again, err, token)
	}
	status, err := l.Status(ctx, "k")
	if err != nil || status.FencingToken != token {
		t.Fatalf("Status = %+v, %v; want token %d", status, err, token)
	}
	locks, err := l.List(ctx, "")
	if err != nil || len(locks) != 1 || locks[0].FencingToken != token {
		t.Fatalf("List = %+v, %v; want token %d", locks, err, token)
	}
	if _, err := l.Extend(ctx, "k", token); err != nil {
		t.Fatalf("Extend: %v", err)
	}
	for range 2 {
		if err := l.Unlock(ctx, "k", token); err != nil {
			t.Fatalf("Unlock: %v", err)
		}
	}
	if r.stored("k") != nil {
		t.Fatal("lock still held after every acquisition was released")
	}
}

// Once every node reads the create revision, the field is no longer written:
// the acquire is one write, and the next re-entry drops a stored field.
func TestMixedVersion_RetiredDropsTheField(t *testing.T) {
	ctx := context.Background()
	r := newRevStore()
	retired := false
	l := mixedLocker(t, r, func() bool { return retired })
	first, err := l.TryLock(ctx, reentrantOpts("k"))
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	if _, ok := storedTokenField(t, r, "k"); !ok {
		t.Fatal("no fencing_token stored before activation")
	}
	retired = true
	again, err := l.TryLock(ctx, reentrantOpts("k"))
	if err != nil || again.FencingToken != first.FencingToken {
		t.Fatalf("re-entry = %+v, %v; want token %d", again, err, first.FencingToken)
	}
	if _, ok := storedTokenField(t, r, "k"); ok {
		t.Fatal("re-entry after activation kept the fencing_token field")
	}

	r2 := newRevStore()
	if _, err := mixedLocker(t, r2, func() bool { return true }).TryLock(ctx, reentrantOpts("k")); err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	if r2.executes != 1 || strings.Contains(string(r2.stored("k").Value), "fencing_token") {
		t.Fatalf("activated acquire made %d writes leaving %s, want one write and no field", r2.executes, r2.stored("k").Value)
	}
}

// The write that stores the token is a compare-and-swap on the acquire's own
// revision: a release that lands first leaves it nothing to write.
func TestMixedVersion_RecordTokenNeverResurrectsAReleasedLock(t *testing.T) {
	r := newRevStore()
	l := mixedLocker(t, r, notRetired)
	r.beforeExecute = func(n int) {
		if n == 2 {
			_ = r.Revoke(context.Background(), 1)
		}
	}
	if _, err := l.TryLock(context.Background(), LockOptions{Key: "k", HolderID: "h", TTL: 30e9}); err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	if r.executes != 2 {
		t.Fatalf("executes = %d, want the acquire and the token write", r.executes)
	}
	if cur := r.stored("k"); cur != nil {
		t.Fatalf("released lock written back: %s", cur.Value)
	}
}

// ambiguousStore commits its first transaction and then reports an error, as
// a proposal committed whose response was lost.
type ambiguousStore struct{ *revStore }

func (a ambiguousStore) Execute(ctx context.Context, compares []Compare, successOps, failureOps []Op) (*TransactionResponse, error) {
	resp, err := a.revStore.Execute(ctx, compares, successOps, failureOps)
	if a.executes == 1 {
		return nil, errors.New("response lost")
	}
	return resp, err
}

// An acquire whose commit was ambiguous stores the token too.
func TestMixedVersion_AmbiguousAcquireStoresTheToken(t *testing.T) {
	r := newRevStore()
	a := ambiguousStore{r}
	l, err := NewLocker(a, r, &mockWatchService{}, r, nil, WithTokenFieldRetired(notRetired))
	if err != nil {
		t.Fatal(err)
	}
	got, err := l.TryLock(context.Background(), LockOptions{Key: "k", HolderID: "h", TTL: 30e9})
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	if token, ok := storedTokenField(t, r, "k"); !ok || token != got.FencingToken || token != r.stored("k").CreateRevision {
		t.Fatalf("stored fencing_token = %d (present %v), want the create revision %d", token, ok, got.FencingToken)
	}
}
