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

package user

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// UserNotFoundError
// ---------------------------------------------------------------------------

func TestUserNotFoundError_Message(t *testing.T) {
	err := &UserNotFoundError{ID: 12345}
	want := "user: not found: 12345"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestUserNotFoundError_LookupMessage(t *testing.T) {
	err := &UserNotFoundError{Lookup: "acme/alice"}
	want := "user: not found: acme/alice"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestUserNotFoundError_As(t *testing.T) {
	var err error = &UserNotFoundError{ID: 42}
	var target *UserNotFoundError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *UserNotFoundError")
	}
	if target.ID != 42 {
		t.Fatalf("ID: got %d, want %d", target.ID, 42)
	}
}

// ---------------------------------------------------------------------------
// UsernameExistsError
// ---------------------------------------------------------------------------

func TestUsernameExistsError_Message(t *testing.T) {
	err := &UsernameExistsError{Username: "alice", TenantID: "acme"}
	want := "user: username already exists: alice in tenant acme"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestUsernameExistsError_As(t *testing.T) {
	var err error = &UsernameExistsError{Username: "bob", TenantID: "system"}
	var target *UsernameExistsError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *UsernameExistsError")
	}
	if target.Username != "bob" {
		t.Fatalf("Username: got %q, want %q", target.Username, "bob")
	}
}

// ---------------------------------------------------------------------------
// UserDisabledError
// ---------------------------------------------------------------------------

func TestUserDisabledError_Message(t *testing.T) {
	err := &UserDisabledError{ID: 1}
	want := "user: disabled: 1"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestUserDisabledError_As(t *testing.T) {
	var err error = &UserDisabledError{ID: 2}
	var target *UserDisabledError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *UserDisabledError")
	}
	if target.ID != 2 {
		t.Fatalf("ID: got %d, want %d", target.ID, 2)
	}
}

// ---------------------------------------------------------------------------
// SystemUserImmutableError
// ---------------------------------------------------------------------------

func TestSystemUserImmutableError_Message(t *testing.T) {
	err := &SystemUserImmutableError{ID: 7}
	want := "user: system user cannot be deleted: 7"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestSystemUserImmutableError_As(t *testing.T) {
	var err error = &SystemUserImmutableError{ID: 8}
	var target *SystemUserImmutableError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *SystemUserImmutableError")
	}
	if target.ID != 8 {
		t.Fatalf("ID: got %d, want %d", target.ID, 8)
	}
}

// ---------------------------------------------------------------------------
// StoreError
// ---------------------------------------------------------------------------

func TestStoreError_MessageFull(t *testing.T) {
	err := &StoreError{Op: "create", Detail: "primary key", Err: io.EOF}
	want := "user: store create failed: primary key: EOF"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestStoreError_MessageNoDetail(t *testing.T) {
	err := &StoreError{Op: "get", Err: io.ErrUnexpectedEOF}
	want := "user: store get failed: unexpected EOF"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestStoreError_MessageNoErr(t *testing.T) {
	err := &StoreError{Op: "new", Detail: "kvstore must not be nil"}
	want := "user: store new failed: kvstore must not be nil"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestStoreError_Unwrap(t *testing.T) {
	inner := io.ErrClosedPipe
	err := &StoreError{Op: "put", Err: inner}
	if !errors.Is(err, inner) {
		t.Fatal("Unwrap should allow errors.Is to match inner error")
	}
}

func TestStoreError_UnwrapNil(t *testing.T) {
	err := &StoreError{Op: "put"}
	if err.Unwrap() != nil {
		t.Fatal("Unwrap should return nil when Err is nil")
	}
}

func TestStoreError_As(t *testing.T) {
	var err error = &StoreError{Op: "delete", Detail: "index", Err: io.EOF}
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *StoreError")
	}
	if target.Op != "delete" {
		t.Fatalf("Op: got %q, want %q", target.Op, "delete")
	}
}

// ---------------------------------------------------------------------------
// ValidationError
// ---------------------------------------------------------------------------

func TestValidationError_Message(t *testing.T) {
	err := &ValidationError{Field: "username", Reason: "must not be empty"}
	want := "user: validation failed: username: must not be empty"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestValidationError_As(t *testing.T) {
	var err error = &ValidationError{Field: "id", Reason: "required"}
	var target *ValidationError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *ValidationError")
	}
	if target.Field != "id" {
		t.Fatalf("Field: got %q, want %q", target.Field, "id")
	}
}

// TestUserNotFoundError_IsTheSDKTransportType pins go-qrdb#343: the service's
// not-found IS the SDK's, so a servicer consumer that imports only sdk/go
// classifies it with the exported type. The compile-time assignment is the
// contract; the errors.As through a real store is the proof it holds for
// what the store actually returns.
func TestUserNotFoundError_IsTheSDKTransportType(t *testing.T) {
	var _ *transport.UserNotFoundError = (*UserNotFoundError)(nil)

	svc := newTestStore(t)
	_, err := svc.GetUserByEmail(context.Background(), "acme", "nobody@acme.example")
	if err == nil {
		t.Fatal("GetUserByEmail on a missing user returned nil")
	}
	var viaTransport *transport.UserNotFoundError
	if !errors.As(err, &viaTransport) {
		t.Fatalf("errors.As(err, *transport.UserNotFoundError) = false for %T: %v", err, err)
	}
	if viaTransport.Lookup != "acme/nobody@acme.example" {
		t.Errorf("Lookup = %q", viaTransport.Lookup)
	}
}

// ---------------------------------------------------------------------------
// EmailExistsError
// ---------------------------------------------------------------------------

func TestEmailExistsError_Message(t *testing.T) {
	err := &EmailExistsError{Email: "a@example.com", TenantID: "acme"}
	want := "user: email already exists: a@example.com in tenant acme"
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// SystemTenantRequiredError
// ---------------------------------------------------------------------------

func TestSystemTenantRequiredError_Message(t *testing.T) {
	err := &SystemTenantRequiredError{TenantID: "acme"}
	want := "user: system flag requires the system tenant, got: acme"
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// SystemUserAlreadyExistsError
// ---------------------------------------------------------------------------

func TestSystemUserAlreadyExistsError_MessageWithID(t *testing.T) {
	err := &SystemUserAlreadyExistsError{ExistingID: 7}
	want := "user: a system user already exists: 7"
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

func TestSystemUserAlreadyExistsError_MessageUnresolved(t *testing.T) {
	err := &SystemUserAlreadyExistsError{}
	want := "user: a system user already exists"
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// CertBindingNotFoundError
// ---------------------------------------------------------------------------

func TestCertBindingNotFoundError_Message(t *testing.T) {
	err := &CertBindingNotFoundError{UserID: 3, Thumbprint: "AAAA"}
	want := "user: cert thumbprint AAAA is not bound to user 3"
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// IndexCompensationError
// ---------------------------------------------------------------------------

func TestIndexCompensationError_Message(t *testing.T) {
	cause := errors.New("write failed")
	err := &IndexCompensationError{
		Op:       "update",
		UserID:   5,
		Cause:    cause,
		Failures: []error{errors.New("f1"), errors.New("f2")},
	}
	want := "user: update failed for id 5 and index rollback was incomplete (2 cleanup failure(s)): write failed"
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, cause) {
		t.Fatal("errors.Is(err, cause) = false, want true via Unwrap")
	}
}

// ---------------------------------------------------------------------------
// HandleReclaimedError
// ---------------------------------------------------------------------------

func TestHandleReclaimedError_Message(t *testing.T) {
	err := &HandleReclaimedError{Key: "username/bob"}
	want := "user: handle username/bob was reclaimed before rollback could restore it"
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// ConcurrentUpdateError
// ---------------------------------------------------------------------------

func TestConcurrentUpdateError_Message(t *testing.T) {
	err := &ConcurrentUpdateError{ID: 9}
	want := "user: concurrent update to user 9; re-read and retry"
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// CommitOutcomeUnknownError
// ---------------------------------------------------------------------------

func TestCommitOutcomeUnknownError_MessageWithErr(t *testing.T) {
	inner := errors.New("timeout")
	err := &CommitOutcomeUnknownError{ID: 4, Err: inner}
	want := "user: commit outcome unknown for user 4; re-read and reconcile before retrying: timeout"
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, inner) {
		t.Fatal("errors.Is(err, inner) = false, want true via Unwrap")
	}
}

func TestCommitOutcomeUnknownError_MessageNoErr(t *testing.T) {
	err := &CommitOutcomeUnknownError{ID: 4}
	want := "user: commit outcome unknown for user 4; re-read and reconcile before retrying"
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if err.Unwrap() != nil {
		t.Fatal("Unwrap() with no Err should return nil")
	}
}

// ---------------------------------------------------------------------------
// IDEncodingError
// ---------------------------------------------------------------------------

func TestIDEncodingError_Message(t *testing.T) {
	err := &IDEncodingError{Length: 3}
	want := "user: malformed id index value: expected 8 bytes, got 3"
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// HandleContendedError
// ---------------------------------------------------------------------------

func TestHandleContendedError_Message(t *testing.T) {
	err := &HandleContendedError{Key: "user/idx/k", Attempts: 8}
	want := "user: handle user/idx/k changed hands on each of 8 claim attempts; retry"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

// TestClaimOrHolder_SustainedChurnIsContended: every claim finds the key held
// and every read finds it released again. The claim neither spins nor reports
// a holder it never saw; it gives up with the typed contention error.
func TestClaimOrHolder_SustainedChurnIsContended(t *testing.T) {
	kv := newMemKVStore()
	s := &kvUserStore{store: kv}
	ctx := context.Background()
	const key = "handle/churning"
	if err := kv.Put(ctx, key, encodeID(9)); err != nil {
		t.Fatal(err)
	}
	claims := 0
	kv.afterPutIfAbsent = func(k string, stored bool) {
		if k == key && !stored {
			claims++
			kv.data.Delete(key) // the holder releases before the read
		}
	}
	kv.afterGet = func(k string) {
		if k == key {
			kv.data.Store(key, encodeID(9)) // and re-takes it before the next claim
		}
	}
	holder, err := s.claimOrHolder(ctx, key, encodeID(1))
	var contended *HandleContendedError
	if !errors.As(err, &contended) || holder != nil {
		t.Fatalf("claimOrHolder = %v, %v; want *HandleContendedError", holder, err)
	}
	if contended.Attempts != handleClaimAttempts || claims != handleClaimAttempts {
		t.Fatalf("attempts = %d, claims = %d; want %d each", contended.Attempts, claims, handleClaimAttempts)
	}
}

// TestClaimOrHolder_StoreErrorsAreNotConflicts: a store that cannot claim, or
// cannot read the holder of a key it found held, reports that failure and
// never a holder -- for the claim and for a rollback's restore alike.
func TestClaimOrHolder_StoreErrorsAreNotConflicts(t *testing.T) {
	ctx := context.Background()
	claimFails := errors.New("claim failed")
	readFails := errors.New("read failed")
	for name, kv := range map[string]*errKVStore{
		"claim": {putErr: claimFails},
		"read":  {getErr: readFails},
	} {
		t.Run(name, func(t *testing.T) {
			want := map[string]error{"claim": claimFails, "read": readFails}[name]
			s := &kvUserStore{store: kv}
			if holder, err := s.claimOrHolder(ctx, "k", encodeID(1)); !errors.Is(err, want) || holder != nil {
				t.Fatalf("claimOrHolder = %v, %v; want %v", holder, err, want)
			}
			if err := s.restoreHandle(ctx, "k", encodeID(1)); !errors.Is(err, want) {
				t.Fatalf("restoreHandle = %v, want %v", err, want)
			}
		})
	}
}

// TestHeldByOther covers the three holder readings: none (claimed), self,
// another user, and an undecodable value, which is an error rather than a
// conflict.
func TestHeldByOther(t *testing.T) {
	if _, other, err := heldByOther(nil, 1); other || err != nil {
		t.Fatalf("nil holder: other=%v err=%v", other, err)
	}
	if _, other, err := heldByOther(encodeID(1), 1); other || err != nil {
		t.Fatalf("self holder: other=%v err=%v", other, err)
	}
	if owner, other, err := heldByOther(encodeID(2), 1); !other || owner != 2 || err != nil {
		t.Fatalf("other holder: owner=%d other=%v err=%v", owner, other, err)
	}
	if _, other, err := heldByOther([]byte("not-an-id"), 1); other || err == nil {
		t.Fatalf("undecodable holder: other=%v err=%v; want an error", other, err)
	}
}
