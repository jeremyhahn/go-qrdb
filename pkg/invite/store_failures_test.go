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
//
// find() and checkUsable() tell a genuine miss (dao.IsNotFound) from every
// other failure and report the latter as *StoreError, so these tests assert
// the CAUSE is recoverable from the error, not merely that the call failed.

package invite

import (
	"context"
	"errors"
	"strings"
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
	// failReadsPrefix, when non-empty, narrows failReads to keys under this
	// prefix only -- everything else reads through. This lets a test break
	// JUST the redemption-marker check (keys under "invite_redemptions/")
	// while the invitation lookup that precedes it still succeeds, which is
	// the only way to reach checkUsable's own fault branch: find() fails
	// first on a blanket read failure, before checkUsable ever runs.
	failReadsPrefix string
	err             error
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

func (e *errKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return false, nil
}

func (e *errKV) Delete(ctx context.Context, key string) error {
	if e.failWrites {
		return e.err
	}
	return e.memKV.Delete(ctx, key)
}

func (e *errKV) Get(ctx context.Context, key string) ([]byte, error) {
	if e.failReads && (e.failReadsPrefix == "" || strings.HasPrefix(key, e.failReadsPrefix)) {
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
// The cause IS recoverable from the error: find() uses dao.IsNotFound to
// tell a genuine miss from a storage fault (go-qrdb#58/#65 already made
// dao.Get report the two differently; find() used to discard that and
// collapse both to ErrNotFound regardless -- see
// docs/identity-rbac/engineering.md's invitation section). A VALID token
// presented during an outage must not be told "forged".
func TestLookupFailsWhenStorageIsUnavailable(t *testing.T) {
	kv := newErrKV()
	store := newErrStore(t, kv)

	token, _, err := store.Create(context.Background(), "user-1", "tenant-a", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	kv.failReads = true
	_, err = store.Lookup(context.Background(), token)
	if err == nil {
		t.Fatal("Lookup must fail when storage is unavailable")
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatalf("Lookup on a VALID token during an outage = %v; must not be ErrNotFound", err)
	}
	var storeErr *StoreError
	if !errors.As(err, &storeErr) {
		t.Fatalf("error = %T (%v), want *StoreError", err, err)
	}
	if !errors.Is(storeErr, kv.err) {
		t.Errorf("StoreError %v does not wrap the storage cause %v", storeErr, kv.err)
	}
}

// TestLookupFailsWhenTheRedemptionCheckCannotBeConfirmed covers checkUsable's
// own fault branch: the invitation itself reads fine, but the redemption
// marker that decides whether it is already spent cannot be read. Treating
// that as "no marker, so not yet redeemed" would let an outage make a spent
// token look usable; this must fail instead of answering either way.
func TestLookupFailsWhenTheRedemptionCheckCannotBeConfirmed(t *testing.T) {
	kv := newErrKV()
	store := newErrStore(t, kv)

	token, _, err := store.Create(context.Background(), "user-1", "tenant-a", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	kv.failReads = true
	kv.failReadsPrefix = redemptionEntityType + "/"
	_, err = store.Lookup(context.Background(), token)
	if err == nil {
		t.Fatal("Lookup must fail when the redemption marker cannot be read")
	}
	if errors.Is(err, ErrAlreadyRedeemed) {
		t.Fatalf("Lookup = %v; an unconfirmed marker must not answer ErrAlreadyRedeemed", err)
	}
	var storeErr *StoreError
	if !errors.As(err, &storeErr) {
		t.Fatalf("error = %T (%v), want *StoreError", err, err)
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
// Same requirement as Lookup's: a storage fault must not be reported as the
// token being unknown.
func TestRedeemReportsAStorageFailureOnTheLookup(t *testing.T) {
	ctx := context.Background()
	kv := newErrKV()
	store := newErrStore(t, kv)

	token, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	kv.failReads = true
	_, err = store.Redeem(ctx, token)
	if err == nil {
		t.Fatal("Redeem must fail when the lookup fails")
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatalf("Redeem on a VALID token during an outage = %v; must not be ErrNotFound", err)
	}
	var storeErr *StoreError
	if !errors.As(err, &storeErr) {
		t.Fatalf("error = %T (%v), want *StoreError", err, err)
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
