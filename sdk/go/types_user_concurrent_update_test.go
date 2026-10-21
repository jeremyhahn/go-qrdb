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

package sdk

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// racingKVStore loses one update of a user to a competing one, with no
// sleeps. It EMBEDS the concrete memory store rather than wrapping the
// KVStore interface, so CompareAndSwap stays in its method set and the user
// store's conditional commit runs against the real implementation.
//
// Once armed, the first Get of the watched primary record reads the bytes,
// runs the competing update to completion, and only then returns the bytes
// it read before it: the outer update decodes a version that is no longer
// committed, so its swap loses.
type racingKVStore struct {
	*MemoryKVStore
	key     string
	armed   atomic.Bool
	compete func(ctx context.Context) error
	err     error

	// releaseHandle, when set, fails every CompareAndDelete of a key ending
	// in it: a lost update cannot release the handle it claimed, so its
	// rollback is incomplete.
	releaseHandle string
}

// CompareAndDelete fails with errReleaseFault for the releaseHandle key.
func (s *racingKVStore) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if s.releaseHandle != "" && strings.HasSuffix(key, "/"+s.releaseHandle) {
		return false, errReleaseFault
	}
	return s.MemoryKVStore.CompareAndDelete(ctx, key, expected)
}

// Get returns the record as it was before the competing update committed.
func (s *racingKVStore) Get(ctx context.Context, key string) ([]byte, error) {
	if key != s.key || !s.armed.CompareAndSwap(true, false) {
		return s.MemoryKVStore.Get(ctx, key)
	}
	before, err := s.MemoryKVStore.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	s.err = s.compete(ctx)
	return before, nil
}

// newRacingServicer builds the production user service over a racingKVStore
// and creates the one user the race is run on.
func newRacingServicer(t *testing.T) (UserServicer, *racingKVStore, uint64) {
	t.Helper()
	idgen, err := dao.NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatalf("NewSnowflakeGenerator: %v", err)
	}
	store := &racingKVStore{MemoryKVStore: NewMemoryKVStore()}
	svc, err := user.NewStore(store, idgen)
	if err != nil {
		t.Fatalf("user.NewStore: %v", err)
	}
	u, err := svc.CreateUser(context.Background(), &user.CreateUserRequest{
		Username: "racer", Email: "racer@acme.example", TenantID: "acme", Name: "Original",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	store.key = user.PrimaryRecordKey(u.ID)
	store.compete = func(ctx context.Context) error {
		_, err := svc.UpdateUser(ctx, &user.UpdateUserRequest{ID: u.ID, Name: "Winner"})
		return err
	}
	store.armed.Store(true)
	return svc, store, u.ID
}

// TestConcurrentUpdateError_RealRaceIsRetryableConflict is the go-qrdb#938
// contract on a real race: an update that loses to a concurrent one is
// classified by the SDK's own exports alone -- errors.As to
// sdk.ConcurrentUpdateError, IsErrorCode(err, ErrConflict) and IsRetryable --
// and the winner's write stands.
func TestConcurrentUpdateError_RealRaceIsRetryableConflict(t *testing.T) {
	svc, store, id := newRacingServicer(t)
	ctx := context.Background()

	_, err := svc.UpdateUser(ctx, &UpdateUserRequest{ID: id, Name: "Loser"})
	if store.err != nil {
		t.Fatalf("competing UpdateUser: %v", store.err)
	}
	if store.armed.Load() {
		t.Fatal("the race never ran: the watched record was not read")
	}
	if err == nil {
		t.Fatal("UpdateUser that lost the race returned nil")
	}
	var lost *ConcurrentUpdateError
	if !errors.As(err, &lost) {
		t.Fatalf("errors.As(err, *sdk.ConcurrentUpdateError) = false for %T: %v", err, err)
	}
	if lost.ID != id {
		t.Errorf("ConcurrentUpdateError.ID = %d, want %d", lost.ID, id)
	}
	if !IsErrorCode(err, ErrConflict) {
		t.Errorf("IsErrorCode(err, ErrConflict) = false for %v", err)
	}
	if !IsRetryable(err) {
		t.Errorf("IsRetryable(err) = false for %v", err)
	}

	got, gerr := svc.GetUser(ctx, id)
	if gerr != nil {
		t.Fatalf("GetUser: %v", gerr)
	}
	if got.Name != "Winner" {
		t.Errorf("Name = %q after the race, want the winner's %q", got.Name, "Winner")
	}
}

// TestConcurrentUpdateError_RetryAfterRaceSucceeds pins what "retryable"
// promises: the same request, re-sent once the race is over, commits.
func TestConcurrentUpdateError_RetryAfterRaceSucceeds(t *testing.T) {
	svc, _, id := newRacingServicer(t)
	ctx := context.Background()

	req := &UpdateUserRequest{ID: id, Name: "Retried"}
	if _, err := svc.UpdateUser(ctx, req); !IsRetryable(err) {
		t.Fatalf("first UpdateUser: want a retryable conflict, got %v", err)
	}
	u, err := svc.UpdateUser(ctx, req)
	if err != nil {
		t.Fatalf("retried UpdateUser: %v", err)
	}
	if u.Name != "Retried" {
		t.Errorf("Name = %q after the retry, want %q", u.Name, "Retried")
	}
}

// errReleaseFault is the injected failure of a lost update's handle release.
var errReleaseFault = errors.New("injected handle release fault")

// newFailedRollbackServicer arms a race whose loser claims a new email and
// cannot release the claim, so the store returns an IndexCompensationError
// wrapping the ConcurrentUpdateError.
func newFailedRollbackServicer(t *testing.T) (UserServicer, *UpdateUserRequest) {
	t.Helper()
	svc, store, id := newRacingServicer(t)
	store.releaseHandle = "moved@acme.example"
	return svc, &UpdateUserRequest{ID: id, Email: store.releaseHandle}
}

// A lost race whose rollback failed is DataLoss, not the retryable conflict
// it wraps: the compensation failure dominates its cause for every in-process
// check, or a retry could succeed over an inconsistent index (go-qrdb#938).
func TestConcurrentUpdateError_FailedRollbackIsNotRetryable(t *testing.T) {
	svc, req := newFailedRollbackServicer(t)
	_, err := svc.UpdateUser(context.Background(), req)
	var comp *user.IndexCompensationError
	if !errors.As(err, &comp) {
		t.Fatalf("want *user.IndexCompensationError, got %T: %v", err, err)
	}

	// The cause stays matchable through Unwrap ...
	var lost *ConcurrentUpdateError
	if !errors.As(err, &lost) {
		t.Fatalf("errors.As(err, *sdk.ConcurrentUpdateError) = false for %v", err)
	}
	// ... but it no longer decides the classification.
	if IsRetryable(err) {
		t.Errorf("IsRetryable(err) = true for a failed rollback: %v", err)
	}
	if IsErrorCode(err, ErrConflict) {
		t.Errorf("IsErrorCode(err, ErrConflict) = true for a failed rollback: %v", err)
	}
	if !IsErrorCode(err, ErrDataLoss) {
		t.Errorf("IsErrorCode(err, ErrDataLoss) = false for a failed rollback: %v", err)
	}
}

// RetryDo's default classifier makes exactly one attempt at an update whose
// rollback failed.
func TestConcurrentUpdateError_FailedRollbackIsNotRetriedByRetryDo(t *testing.T) {
	svc, req := newFailedRollbackServicer(t)
	attempts := 0
	err := RetryDo(context.Background(), &RetryConfig{MaxAttempts: 3, Backoff: &ConstantBackoff{}},
		func(ctx context.Context) error {
			attempts++
			_, err := svc.UpdateUser(ctx, req)
			return err
		})
	if attempts != 1 {
		t.Fatalf("RetryDo made %d attempts at a failed rollback, want 1 (err %v)", attempts, err)
	}
	var comp *user.IndexCompensationError
	if !errors.As(err, &comp) {
		t.Fatalf("RetryDo returned %T (%v), want the *user.IndexCompensationError", err, err)
	}
}
