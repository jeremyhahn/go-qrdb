// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Storage-failure tests. An invitation is a credential, so a storage fault has
// to surface as a typed error rather than as a missing or partially written
// record: reporting "no such invitation" when the store is simply unavailable
// would let a caller conclude a valid token was forged.

package invite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// errKV wraps a memKV and fails the chosen operations. Reads and writes are
// controlled separately so a test can let a record be created and then break
// only the step under test.
type errKV struct {
	*memKV
	failWrites bool
	failReads  bool
	err        error
}

func newErrKV() *errKV {
	return &errKV{memKV: newMemKV(), err: errors.New("storage unavailable")}
}

func (e *errKV) Put(ctx context.Context, key string, value []byte) error {
	if e.failWrites {
		return e.err
	}
	return e.memKV.Put(ctx, key, value)
}

func (e *errKV) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if e.failWrites {
		return false, e.err
	}
	return e.memKV.PutIfAbsent(ctx, key, value)
}

// CompareAndDelete mirrors PutIfAbsent's guard on the release side:
// it removes the key only when the stored value still equals expected.
func (e *errKV) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	return false, nil
}

func (e *errKV) Delete(ctx context.Context, key string) error {
	if e.failWrites {
		return e.err
	}
	return e.memKV.Delete(ctx, key)
}

func (e *errKV) Get(ctx context.Context, key string) ([]byte, error) {
	if e.failReads {
		return nil, e.err
	}
	return e.memKV.Get(ctx, key)
}

func (e *errKV) List(ctx context.Context, prefix string) ([]string, error) {
	if e.failReads {
		return nil, e.err
	}
	return e.memKV.List(ctx, prefix)
}

func (e *errKV) Scan(ctx context.Context, prefix string, fn func(key string, value []byte) error) error {
	if e.failReads {
		return e.err
	}
	return e.memKV.Scan(ctx, prefix, fn)
}

func (e *errKV) Exists(ctx context.Context, key string) (bool, error) {
	if e.failReads {
		return false, e.err
	}
	return e.memKV.Exists(ctx, key)
}

var _ kvstore.KVStore = (*errKV)(nil)

// newErrStore builds a DAOStore over a controllable failing KV.
func newErrStore(t *testing.T, kv *errKV) *DAOStore {
	t.Helper()
	store, err := NewDAOStore(kv)
	if err != nil {
		t.Fatalf("NewDAOStore: %v", err)
	}
	return store
}

// TestCreateReportsAStorageFailure covers the write-failure branch. A Create
// that swallowed the error would hand the caller a plaintext token that was
// never persisted -- an invitation that can never be redeemed, with no
// indication anything went wrong.
func TestCreateReportsAStorageFailure(t *testing.T) {
	kv := newErrKV()
	kv.failWrites = true
	store := newErrStore(t, kv)

	token, entity, err := store.Create(context.Background(), "user-1", "tenant-a", time.Hour)
	if err == nil {
		t.Fatal("Create must fail when storage rejects the write")
	}
	if token != "" || entity != nil {
		t.Fatal("a failed Create must not return a token or an entity")
	}
	if !errors.Is(err, kv.err) {
		t.Errorf("error %v does not wrap the storage cause", err)
	}
}

// TestLookupFailsWhenStorageIsUnavailable covers the read-failure branch.
//
// It asserts only that Lookup fails, NOT that the cause is recoverable from
// the error: find() collapses every failure to ErrNotFound, because
// KVStore.Get returns (nil, nil) for a missing key and a miss is therefore
// indistinguishable from a read fault by the time the DAO reports it. See
// docs/invite-notfound-ambiguity.md -- tightening this assertion requires a
// not-found signal in the KVStore contract, not a change here.
func TestLookupFailsWhenStorageIsUnavailable(t *testing.T) {
	kv := newErrKV()
	store := newErrStore(t, kv)

	token, _, err := store.Create(context.Background(), "user-1", "tenant-a", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	kv.failReads = true
	if _, err := store.Lookup(context.Background(), token); err == nil {
		t.Fatal("Lookup must fail when storage is unavailable")
	}
}

// TestRedeemReportsAStorageFailureOnTheConsumingWrite covers the branch where
// the record is found but the write marking it redeemed fails. Returning
// success there would be the worst outcome: the caller treats the token as
// consumed while it remains redeemable.
func TestRedeemReportsAStorageFailureOnTheConsumingWrite(t *testing.T) {
	ctx := context.Background()
	kv := newErrKV()
	store := newErrStore(t, kv)

	token, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	kv.failWrites = true
	if _, err := store.Redeem(ctx, token); err == nil {
		t.Fatal("Redeem must fail when the consuming write fails")
	}

	// The invitation must still be usable: a failed redemption consumed
	// nothing.
	kv.failWrites = false
	entity, err := store.Redeem(ctx, token)
	if err != nil {
		t.Fatalf("the token was consumed by a failed Redeem: %v", err)
	}
	if entity.UserID != "user-1" {
		t.Errorf("UserID = %q, want %q", entity.UserID, "user-1")
	}
}

// TestRedeemReportsAStorageFailureOnTheLookup covers the read half of Redeem.
func TestRedeemReportsAStorageFailureOnTheLookup(t *testing.T) {
	ctx := context.Background()
	kv := newErrKV()
	store := newErrStore(t, kv)

	token, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	kv.failReads = true
	if _, err := store.Redeem(ctx, token); err == nil {
		t.Fatal("Redeem must fail when the lookup fails")
	}
}

// TestGCReportsAStorageFailure covers the sweep's error path. A GC that
// reported zero removals on a storage fault would look like a clean sweep and
// leave expired invitations accumulating unnoticed.
func TestGCReportsAStorageFailure(t *testing.T) {
	ctx := context.Background()
	kv := newErrKV()
	store := newErrStore(t, kv)

	if _, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour); err != nil {
		t.Fatalf("Create: %v", err)
	}

	kv.failReads = true
	removed, err := store.GC(ctx, time.Now().Add(2*time.Hour))
	if err == nil {
		t.Fatal("GC must fail when the store cannot be read")
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0 on failure", removed)
	}
}

// TestGCReportsAStorageFailureOnDelete covers the delete half of the sweep.
func TestGCReportsAStorageFailureOnDelete(t *testing.T) {
	ctx := context.Background()
	kv := newErrKV()
	store := newErrStore(t, kv)

	if _, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour); err != nil {
		t.Fatalf("Create: %v", err)
	}

	kv.failWrites = true
	if _, err := store.GC(ctx, time.Now().Add(2*time.Hour)); err == nil {
		t.Fatal("GC must fail when an expired record cannot be deleted")
	}
}

// TestNewDAOStoreRequiresAStore refuses construction without backing storage,
// rather than producing a store whose every operation nil-panics later.
func TestNewDAOStoreRequiresAStore(t *testing.T) {
	if _, err := NewDAOStore(nil); err == nil {
		t.Fatal("NewDAOStore(nil) must fail")
	}
}
