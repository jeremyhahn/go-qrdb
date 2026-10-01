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
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// Identity-set normalization
// ---------------------------------------------------------------------------

func TestNormalizeAuthMethods_FoldsCaseAndDedupes(t *testing.T) {
	got, err := NormalizeAuthMethods([]string{"WebAuthn", " webauthn ", "PIV"})
	if err != nil {
		t.Fatalf("NormalizeAuthMethods: %v", err)
	}
	// A policy list holding both "WebAuthn" and "webauthn" would eventually
	// deny the method it meant to allow, so case IS folded here.
	want := []string{"webauthn", "piv"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestNormalizeAuthMethods_RejectsBlankEntry(t *testing.T) {
	_, err := NormalizeAuthMethods([]string{"webauthn", ""})
	var v *ValidationError
	if !errors.As(err, &v) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if v.Field != "authMethods" {
		t.Errorf("Field: got %q, want %q", v.Field, "authMethods")
	}
}

// ---------------------------------------------------------------------------
// CreateUser
// ---------------------------------------------------------------------------

func TestCreateUser_StoresAuthMethodsAndLifecycle(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	created, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username:    "operator",
		Email:       "operator@example.com",
		AuthMethods: []string{"WebAuthn", "piv"},
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if created.Lifecycle != LifecycleActive || !created.CanAuthenticate() {
		t.Fatalf("lifecycle=%q canAuthenticate=%v, want active/true", created.Lifecycle, created.CanAuthenticate())
	}
	if !reflect.DeepEqual(created.AuthMethods, []string{"webauthn", "piv"}) {
		t.Errorf("AuthMethods: got %v", created.AuthMethods)
	}

	// The normalized values must be what was PERSISTED, not just what was
	// returned: a store that normalizes on the way out but not on the way in
	// disagrees with itself the moment a second reader arrives.
	read, err := svc.GetUser(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if !reflect.DeepEqual(read.AuthMethods, created.AuthMethods) ||
		read.Lifecycle != created.Lifecycle {
		t.Fatalf("persisted record differs: %+v", read)
	}
}

func TestCreateUser_PendingProducesPendingLifecycle(t *testing.T) {
	svc := newTestStore(t)
	created, err := svc.CreateUser(context.Background(), &CreateUserRequest{
		Username: "invitee",
		Email:    "invitee@example.com",
		Pending:  true,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if created.Lifecycle != LifecyclePending {
		t.Errorf("Lifecycle: got %q, want %q", created.Lifecycle, LifecyclePending)
	}
	if created.CanAuthenticate() {
		t.Error("a pending account reported able to authenticate")
	}
}

func TestCreateUser_ExplicitDisabledLifecycle(t *testing.T) {
	svc := newTestStore(t)
	created, err := svc.CreateUser(context.Background(), &CreateUserRequest{
		Username:  "dormant",
		Email:     "dormant@example.com",
		Lifecycle: LifecycleDisabled,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if created.Lifecycle != LifecycleDisabled || created.CanAuthenticate() {
		t.Errorf("lifecycle=%q enabled=%v", created.Lifecycle, created.CanAuthenticate())
	}
}

// A request that cannot produce a valid record must leave the store completely
// untouched — no id claimed, no username or email handle reserved. Otherwise a
// caller that fixes its request and retries is told the address is taken, by
// itself, forever.
func TestCreateUser_RejectedRequestWritesNothing(t *testing.T) {
	kv := newMemKVStore()
	svc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()

	bad := []struct {
		name string
		req  *CreateUserRequest
	}{
		{"non-creatable lifecycle", &CreateUserRequest{Username: "u", Email: "u@example.com", Lifecycle: LifecycleLocked}},
		{"pending conflict", &CreateUserRequest{Username: "u", Email: "u@example.com", Pending: true, Lifecycle: LifecycleActive}},
		{"blank auth method", &CreateUserRequest{Username: "u", Email: "u@example.com", AuthMethods: []string{" "}}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.CreateUser(ctx, tc.req); err == nil {
				t.Fatal("expected the request to be refused")
			}
		})
	}

	written := 0
	kv.data.Range(func(_, _ any) bool { written++; return true })
	if written != 0 {
		t.Fatalf("%d keys were written by requests that all failed validation", written)
	}

	// And the handles are genuinely free afterwards.
	if _, err := svc.CreateUser(ctx, &CreateUserRequest{Username: "u", Email: "u@example.com"}); err != nil {
		t.Fatalf("retry after a corrected request: %v", err)
	}
}

// ---------------------------------------------------------------------------
// UpdateUser
// ---------------------------------------------------------------------------

func TestUpdateUser_ReplacesAndClearsAuthMethods(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()
	u := createTestUser(t, svc, "alice", "")

	updated, err := svc.UpdateUser(ctx, &UpdateUserRequest{
		ID:          u.ID,
		AuthMethods: []string{"PIV"},
	})
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if !reflect.DeepEqual(updated.AuthMethods, []string{"piv"}) {
		t.Errorf("AuthMethods: got %v", updated.AuthMethods)
	}

	// A nil AuthMethods leaves the field alone.
	kept, err := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, Name: "Alice"})
	if err != nil {
		t.Fatalf("UpdateUser(name): %v", err)
	}
	if !reflect.DeepEqual(kept.AuthMethods, []string{"piv"}) {
		t.Errorf("a nil AuthMethods must leave the field alone, got %v", kept.AuthMethods)
	}

	// An empty non-nil slice CLEARS, which is how a restriction is lifted.
	cleared, err := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, AuthMethods: []string{}})
	if err != nil {
		t.Fatalf("UpdateUser(clear): %v", err)
	}
	if len(cleared.AuthMethods) != 0 {
		t.Errorf("AuthMethods after clear: got %v", cleared.AuthMethods)
	}
}

func TestUpdateUser_RejectsBlankIdentitySetEntries(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()
	u := createTestUser(t, svc, "bob", "")

	for _, req := range []*UpdateUserRequest{
		{ID: u.ID, AuthMethods: []string{"ok", ""}},
		{ID: u.ID, AuthMethods: []string{""}},
	} {
		if _, err := svc.UpdateUser(ctx, req); err == nil {
			t.Fatalf("expected a validation error for %+v", req)
		}
	}
	read, err := svc.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if len(read.AuthMethods) != 0 {
		t.Fatalf("a refused update mutated the record: %+v", read)
	}
}

func TestUpdateUser_LifecycleTransitionsAndRefusals(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()
	u := createTestUser(t, svc, "carol", "")

	suspended, err := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, Lifecycle: LifecycleSuspended})
	if err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if suspended.Lifecycle != LifecycleSuspended || suspended.CanAuthenticate() {
		t.Fatalf("lifecycle=%q enabled=%v", suspended.Lifecycle, suspended.CanAuthenticate())
	}

	// suspended -> pending is not a route in the table: an account that has
	// already been activated cannot be un-activated.
	_, err = svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, Lifecycle: LifecyclePending})
	var te *LifecycleTransitionError
	if !errors.As(err, &te) {
		t.Fatalf("expected *LifecycleTransitionError, got %T: %v", err, err)
	}

	// An unknown state is rejected as invalid, not as an unconnected transition.
	_, err = svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, Lifecycle: "quarantined"})
	var invalid *InvalidLifecycleError
	if !errors.As(err, &invalid) {
		t.Fatalf("expected *InvalidLifecycleError, got %T: %v", err, err)
	}

	read, err := svc.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if read.Lifecycle != LifecycleSuspended {
		t.Fatalf("a refused transition was persisted: %q", read.Lifecycle)
	}
}

// This is the shape go-quicbaas#290 needs: a deletion writes the marker before
// its first mutation, and from that instant nothing — not a concurrent enable,
// not a registration ceremony finishing late — can make the account
// authenticable again.
func TestUpdateUser_DeletingMarkerIsTerminalAndClosesTheAuthWindow(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()
	u := createTestUser(t, svc, "dave", "")

	marked, err := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, Lifecycle: LifecycleDeleting})
	if err != nil {
		t.Fatalf("mark deleting: %v", err)
	}
	if marked.CanAuthenticate() {
		t.Fatal("an account marked deleting reported able to authenticate")
	}

	// Re-asserting the marker is a no-op, so a partially-completed deletion is
	// resumable without special-casing an error.
	if _, err := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, Lifecycle: LifecycleDeleting}); err != nil {
		t.Fatalf("re-marking deleting: %v", err)
	}

	for _, to := range []Lifecycle{LifecycleActive, LifecyclePending, LifecycleSuspended, LifecycleLocked, LifecycleDisabled} {
		_, err := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, Lifecycle: to})
		var te *LifecycleTransitionError
		if !errors.As(err, &te) {
			t.Fatalf("deleting -> %q: expected *LifecycleTransitionError, got %T: %v", to, err, err)
		}
	}

	// The dedicated verbs must honor it too — they are the two callers most
	// likely to race a delete.
	if err := svc.EnableUser(ctx, u.ID); err == nil {
		t.Fatal("EnableUser revived an account marked deleting")
	}
	if err := svc.DisableUser(ctx, u.ID); err == nil {
		t.Fatal("DisableUser landed on an account marked deleting")
	}

	read, err := svc.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if read.Lifecycle != LifecycleDeleting || read.CanAuthenticate() {
		t.Fatalf("lifecycle=%q enabled=%v", read.Lifecycle, read.CanAuthenticate())
	}
}

// A login is recorded after the ceremony completes, so two overlapping logins
// can be written out of order. The stored value is a high-water mark: an
// account whose last-seen time moves backwards misleads exactly the readers it
// exists for.
func TestUpdateUser_LastAuthOnlyMovesForward(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()
	u := createTestUser(t, svc, "erin", "")
	if u.LastAuth != nil {
		t.Fatalf("a fresh account carries a LastAuth: %v", u.LastAuth)
	}

	later := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	earlier := later.Add(-time.Hour)

	updated, err := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, LastAuth: &later})
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if updated.LastAuth == nil || !updated.LastAuth.Equal(later) {
		t.Fatalf("LastAuth: got %v, want %v", updated.LastAuth, later)
	}

	stale, err := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, LastAuth: &earlier})
	if err != nil {
		t.Fatalf("UpdateUser(stale): %v", err)
	}
	if !stale.LastAuth.Equal(later) {
		t.Fatalf("LastAuth moved backwards to %v", stale.LastAuth)
	}

	// A non-UTC input is stored in UTC so every reader compares like with like.
	zone := time.FixedZone("UTC+5", 5*3600)
	newest := later.Add(time.Hour).In(zone)
	bumped, err := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, LastAuth: &newest})
	if err != nil {
		t.Fatalf("UpdateUser(newest): %v", err)
	}
	if bumped.LastAuth.Location() != time.UTC {
		t.Errorf("LastAuth location: got %v, want UTC", bumped.LastAuth.Location())
	}
	if !bumped.LastAuth.Equal(newest) {
		t.Errorf("LastAuth: got %v, want %v", bumped.LastAuth, newest)
	}
}

// ---------------------------------------------------------------------------
// EnableUser / DisableUser as lifecycle transitions
// ---------------------------------------------------------------------------

func TestEnableDisableUser_AreLifecycleTransitions(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()
	u := createTestUser(t, svc, "frank", "")

	if err := svc.DisableUser(ctx, u.ID); err != nil {
		t.Fatalf("DisableUser: %v", err)
	}
	read, err := svc.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if read.Lifecycle != LifecycleDisabled || read.CanAuthenticate() {
		t.Fatalf("lifecycle=%q enabled=%v", read.Lifecycle, read.CanAuthenticate())
	}

	if err := svc.EnableUser(ctx, u.ID); err != nil {
		t.Fatalf("EnableUser: %v", err)
	}
	read, err = svc.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if read.Lifecycle != LifecycleActive || !read.CanAuthenticate() {
		t.Fatalf("lifecycle=%q enabled=%v", read.Lifecycle, read.CanAuthenticate())
	}

	// Repeating either verb is a no-op, not an error: callers retry.
	if err := svc.EnableUser(ctx, u.ID); err != nil {
		t.Fatalf("EnableUser (repeat): %v", err)
	}
}

func TestEnableUser_ActivatesAPendingAccount(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()
	created, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "invitee2",
		Email:    "invitee2@example.com",
		Pending:  true,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := svc.EnableUser(ctx, created.ID); err != nil {
		t.Fatalf("EnableUser: %v", err)
	}
	read, err := svc.GetUser(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if read.Lifecycle != LifecycleActive || !read.CanAuthenticate() {
		t.Fatalf("lifecycle=%q", read.Lifecycle)
	}
}

// EnableUser must not resurrect a suspended account by way of a state the
// table does not connect — suspended -> active IS connected, so this asserts
// the reinstatement path works rather than that it is blocked.
func TestEnableUser_ReinstatesASuspendedAccount(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()
	u := createTestUser(t, svc, "grace", "")
	if _, err := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, Lifecycle: LifecycleSuspended}); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if err := svc.EnableUser(ctx, u.ID); err != nil {
		t.Fatalf("EnableUser: %v", err)
	}
	read, err := svc.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if read.Lifecycle != LifecycleActive {
		t.Fatalf("lifecycle: got %q, want active", read.Lifecycle)
	}
}

// ---------------------------------------------------------------------------
// Records that carry no lifecycle state fail closed
// ---------------------------------------------------------------------------

// A record with no lifecycle state — including one written with the removed
// "enabled" key — reads back with NO state, cannot authenticate, and cannot be
// transitioned. There is no decode-time fallback: Lifecycle is the only
// account-state field, and its absence is refused, never guessed.
func TestGetUser_RecordWithoutLifecycleFailsClosed(t *testing.T) {
	kv := newMemKVStore()
	svc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()

	data, merr := json.Marshal(map[string]any{
		"id":        uint64(4242),
		"username":  "stateless",
		"tenantID":  tenant.SystemTenantID,
		"roleIDs":   []string{"viewer"},
		"enabled":   true,
		"createdAt": time.Now().UTC(),
		"updatedAt": time.Now().UTC(),
	})
	if merr != nil {
		t.Fatalf("marshal: %v", merr)
	}
	if perr := kv.Put(ctx, primaryKey(4242), data); perr != nil {
		t.Fatalf("Put: %v", perr)
	}

	read, err := svc.GetUser(ctx, 4242)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if read.Lifecycle != "" {
		t.Fatalf("Lifecycle: got %q, want empty (no state is derived)", read.Lifecycle)
	}
	if read.CanAuthenticate() {
		t.Fatal("a record with no lifecycle state may authenticate")
	}

	// Nor can it be talked into a state: the transition table has no row for
	// an absent state, so EnableUser is refused and the record is unchanged.
	err = svc.EnableUser(ctx, 4242)
	var te *LifecycleTransitionError
	if !errors.As(err, &te) {
		t.Fatalf("EnableUser: expected *LifecycleTransitionError, got %T: %v", err, err)
	}
	raw, gerr := kv.Get(ctx, primaryKey(4242))
	if gerr != nil {
		t.Fatalf("Get: %v", gerr)
	}
	if string(raw) != string(data) {
		t.Fatalf("a refused transition rewrote the record: %s", raw)
	}
}

// A refused request must be a guaranteed no-op, not a compensated one. An
// update that would BOTH move the username handle and carry an invalid field
// has to be refused before the index is touched — compensation is itself a
// write that can fail, and trading a certain no-op for a probable undo is a bad
// trade when the check costs nothing.
func TestUpdateUser_RefusedRequestNeverTouchesTheIndex(t *testing.T) {
	kv := newMemKVStore()
	svc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	u, err := svc.CreateUser(ctx, &CreateUserRequest{Username: "heidi", Email: "heidi@example.com"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	bad := []*UpdateUserRequest{
		{ID: u.ID, Username: "heidi-renamed", AuthMethods: []string{" "}},
		{ID: u.ID, Username: "heidi-renamed", Email: "renamed@example.com", Lifecycle: "quarantined"},
	}
	for _, req := range bad {
		if _, uerr := svc.UpdateUser(ctx, req); uerr == nil {
			t.Fatalf("expected the update to be refused: %+v", req)
		}
	}

	// The old handles still resolve and the new ones were never reserved.
	if _, gerr := svc.GetUserByUsername(ctx, tenant.SystemTenantID, "heidi"); gerr != nil {
		t.Fatalf("the original username stopped resolving: %v", gerr)
	}
	if _, gerr := svc.GetUserByEmail(ctx, tenant.SystemTenantID, "heidi@example.com"); gerr != nil {
		t.Fatalf("the original login key stopped resolving: %v", gerr)
	}
	if raw, gerr := kv.Get(ctx, usernameIndexKey(tenant.SystemTenantID, "heidi-renamed")); gerr != nil || raw != nil {
		t.Fatalf("the refused username was reserved anyway (raw=%v err=%v)", raw, gerr)
	}
	if raw, gerr := kv.Get(ctx, emailIndexKey(tenant.SystemTenantID, "renamed@example.com")); gerr != nil || raw != nil {
		t.Fatalf("the refused login key was reserved anyway (raw=%v err=%v)", raw, gerr)
	}

	// And a corrected request still lands, so nothing was left half-claimed.
	if _, uerr := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, Username: "heidi-renamed"}); uerr != nil {
		t.Fatalf("the corrected update was refused: %v", uerr)
	}
}

// A LastAuth-only request must SURVIVE losing a race to another writer, because
// its merge is commutative — max(stored, requested) — and there is nothing
// stale for the caller to reconcile. Sending it through the ordinary update path
// made a login's timestamp lost whenever an administrator happened to be editing
// the same account, and lost silently below the true high-water mark, because
// the loser computed its max against a base the winner had already replaced.
//
// The race is driven deterministically: the store double commits one interfering
// write during the first CompareAndSwap, so the first attempt provably loses.
func TestUpdateUser_LastAuthOnlyRetriesAndKeepsTheHighWaterMark(t *testing.T) {
	kv := newMemKVStore()
	svc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	u, err := svc.CreateUser(ctx, &CreateUserRequest{Username: "ivan", Email: "ivan@example.com"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Another writer commits a display-name change, invalidating the base the
	// login read. Simulated by mutating the record out from under the merge on
	// its first CompareAndSwap.
	kv.beforeCAS = func(key string) {
		if key != primaryKey(u.ID) {
			return
		}
		kv.beforeCAS = nil // interfere exactly once
		raw, _ := kv.Get(ctx, key)
		var other User
		if uerr := json.Unmarshal(raw, &other); uerr != nil {
			t.Errorf("interfering write: unmarshal: %v", uerr)
			return
		}
		other.Name = "Ivan the Concurrent"
		data, merr := json.Marshal(&other)
		if merr != nil {
			t.Errorf("interfering write: marshal: %v", merr)
			return
		}
		if perr := kv.Put(ctx, key, data); perr != nil {
			t.Errorf("interfering write: put: %v", perr)
		}
	}

	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	got, err := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, LastAuth: &at})
	if err != nil {
		t.Fatalf("a LastAuth-only update must retry past a lost race, got: %v", err)
	}
	if got.LastAuth == nil || !got.LastAuth.Equal(at) {
		t.Fatalf("LastAuth: got %v, want %v", got.LastAuth, at)
	}
	// The interfering writer's change survived: the merge rebuilt on top of it
	// rather than reverting it.
	if got.Name != "Ivan the Concurrent" {
		t.Fatalf("the concurrent writer's change was reverted: Name=%q", got.Name)
	}
}

// The merge writes NOTHING when the mark is already at or past the request, so
// a duplicate or delayed login report costs one read and cannot lose a race at
// all.
func TestUpdateUser_LastAuthOnlyBelowTheMarkWritesNothing(t *testing.T) {
	kv := newMemKVStore()
	svc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	u, err := svc.CreateUser(ctx, &CreateUserRequest{Username: "judy", Email: "judy@example.com"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	later := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	if _, uerr := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, LastAuth: &later}); uerr != nil {
		t.Fatalf("UpdateUser: %v", uerr)
	}
	before, gerr := kv.Get(ctx, primaryKey(u.ID))
	if gerr != nil {
		t.Fatalf("Get: %v", gerr)
	}

	earlier := later.Add(-time.Hour)
	got, uerr := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, LastAuth: &earlier})
	if uerr != nil {
		t.Fatalf("UpdateUser(stale): %v", uerr)
	}
	if !got.LastAuth.Equal(later) {
		t.Fatalf("LastAuth moved backwards to %v", got.LastAuth)
	}
	after, gerr := kv.Get(ctx, primaryKey(u.ID))
	if gerr != nil {
		t.Fatalf("Get: %v", gerr)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("a below-the-mark report rewrote the record; it must write nothing")
	}
}

// A request that carries LastAuth ALONGSIDE another field is an ordinary
// update, with ordinary lost-update semantics — the merge path must not swallow
// the other field, and must not retry a replacement.
func TestUpdateUser_LastAuthWithAnotherFieldTakesTheOrdinaryPath(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()
	u := createTestUser(t, svc, "karl", "")

	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	got, err := svc.UpdateUser(ctx, &UpdateUserRequest{
		ID:       u.ID,
		Name:     "Karl Renamed",
		LastAuth: &at,
	})
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if got.Name != "Karl Renamed" {
		t.Fatalf("Name: got %q; the fast path swallowed a field it must not handle", got.Name)
	}
	if got.LastAuth == nil || !got.LastAuth.Equal(at) {
		t.Fatalf("LastAuth: got %v, want %v", got.LastAuth, at)
	}
}

// lastAuthOnly is an exhaustive field list, and a field added to
// UpdateUserRequest without being named there would be silently DROPPED by the
// fast path. This pins every field that must disqualify it.
func TestLastAuthOnly_AnyOtherFieldDisqualifies(t *testing.T) {
	at := time.Now().UTC()
	base := func() *UpdateUserRequest { return &UpdateUserRequest{ID: 1, LastAuth: &at} }

	if !lastAuthOnly(base()) {
		t.Fatal("a LastAuth-only request was not recognized")
	}
	if lastAuthOnly(&UpdateUserRequest{ID: 1}) {
		t.Fatal("a request with no LastAuth was recognized")
	}

	disqualifying := map[string]func(*UpdateUserRequest){
		"Username":     func(r *UpdateUserRequest) { r.Username = "x" },
		"Email":        func(r *UpdateUserRequest) { r.Email = "x@example.com" },
		"Name":         func(r *UpdateUserRequest) { r.Name = "x" },
		"Lifecycle":    func(r *UpdateUserRequest) { r.Lifecycle = LifecycleActive },
		"RoleIDs":      func(r *UpdateUserRequest) { r.RoleIDs = []string{} },
		"RoleBindings": func(r *UpdateUserRequest) { r.RoleBindings = []RoleBinding{} },
		"Attributes":   func(r *UpdateUserRequest) { r.Attributes = map[string]string{} },
		"CertBindings": func(r *UpdateUserRequest) { r.CertBindings = []CertBinding{} },
		"Factors":      func(r *UpdateUserRequest) { r.Factors = []FactorEnrollment{} },
		"AuthMethods":  func(r *UpdateUserRequest) { r.AuthMethods = []string{} },
	}
	for name, set := range disqualifying {
		req := base()
		set(req)
		if lastAuthOnly(req) {
			t.Errorf("a request also setting %s took the LastAuth fast path, which would drop it", name)
		}
	}

	// Every mutable field of UpdateUserRequest must appear above. ID is the
	// target, not a mutation, and LastAuth is the field the path exists for.
	const idAndLastAuth = 2
	if got := reflect.TypeOf(UpdateUserRequest{}).NumField(); got != len(disqualifying)+idAndLastAuth {
		t.Fatalf("UpdateUserRequest has %d fields but only %d are pinned here; a new field must "+
			"either disqualify the fast path or be handled by mergeLastAuth", got, len(disqualifying)+idAndLastAuth)
	}
}

// A delete CLAIMS the record by marking it deleting before it removes anything.
// Until that mark lands the account is fully able to authenticate and fully
// writable, so the cascade would be sweeping a snapshot another writer can still
// invalidate — and a credential bound in that window outlives the sweep.
func TestDeleteUser_MarksDeletingBeforeTheCascade(t *testing.T) {
	kv := newMemKVStore()
	svc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	u, err := svc.CreateUser(ctx, &CreateUserRequest{Username: "leo", Email: "leo@example.com"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Observe the record at the instant the cascade's first index removal runs.
	var atFirstRemoval *User
	kv.beforeCAS = nil
	kv.onDelete = func(key string) {
		if atFirstRemoval != nil {
			return
		}
		raw, _ := kv.Get(ctx, primaryKey(u.ID))
		if raw == nil {
			return
		}
		var snap User
		if uerr := json.Unmarshal(raw, &snap); uerr != nil {
			t.Errorf("snapshot unmarshal: %v", uerr)
			return
		}
		atFirstRemoval = &snap
	}

	if derr := svc.DeleteUser(ctx, u.ID); derr != nil {
		t.Fatalf("DeleteUser: %v", derr)
	}
	if atFirstRemoval == nil {
		t.Fatal("no index removal was observed; the test cannot say what it claims")
	}
	if atFirstRemoval.Lifecycle != LifecycleDeleting {
		t.Fatalf("lifecycle at the first removal was %q, want %q — the cascade ran before the record was claimed",
			atFirstRemoval.Lifecycle, LifecycleDeleting)
	}
	if atFirstRemoval.CanAuthenticate() {
		t.Fatal("the account was still able to authenticate while its indexes were being removed")
	}

	if _, gerr := svc.GetUser(ctx, u.ID); gerr == nil {
		t.Fatal("the user still exists after DeleteUser")
	}
}

// A retried delete must not be refused by the marker its own first attempt
// wrote. Re-asserting a terminal state is a no-op, so the retry proceeds.
func TestDeleteUser_RetryAfterAMarkedRecordSucceeds(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()
	u := createTestUser(t, svc, "mia", "")

	if _, uerr := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, Lifecycle: LifecycleDeleting}); uerr != nil {
		t.Fatalf("mark deleting: %v", uerr)
	}
	if derr := svc.DeleteUser(ctx, u.ID); derr != nil {
		t.Fatalf("DeleteUser on an already-marked record: %v", derr)
	}
	if _, gerr := svc.GetUser(ctx, u.ID); gerr == nil {
		t.Fatal("the user still exists after DeleteUser")
	}
}

// A record whose deletion has begun accepts NO edits. Refusing only the
// lifecycle transition left every other field writable, so a caller could still
// rename a deleting account or bind a certificate to it — and a credential bound
// after the deletion sweep is exactly the orphan the marker exists to prevent.
func TestUpdateUser_TerminalRecordRefusesEveryEdit(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()
	u := createTestUser(t, svc, "nina", "")
	if _, uerr := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, Lifecycle: LifecycleDeleting}); uerr != nil {
		t.Fatalf("mark deleting: %v", uerr)
	}

	at := time.Now().UTC()
	edits := map[string]*UpdateUserRequest{
		"username":     {ID: u.ID, Username: "renamed"},
		"email":        {ID: u.ID, Email: "renamed@example.com"},
		"name":         {ID: u.ID, Name: "Renamed"},
		"roleIDs":      {ID: u.ID, RoleIDs: []string{"admin"}},
		"authMethods":  {ID: u.ID, AuthMethods: []string{"piv"}},
		"attributes":   {ID: u.ID, Attributes: map[string]string{"a": "b"}},
		"certBindings": {ID: u.ID, CertBindings: []CertBinding{}},
		"factors":      {ID: u.ID, Factors: []FactorEnrollment{}},
		"lastAuth":     {ID: u.ID, LastAuth: &at},
	}
	for name, req := range edits {
		t.Run(name, func(t *testing.T) {
			_, err := svc.UpdateUser(ctx, req)
			var te *LifecycleTransitionError
			if !errors.As(err, &te) {
				t.Fatalf("expected *LifecycleTransitionError, got %T: %v", err, err)
			}
		})
	}

	// Re-asserting the state is still permitted: that is how a partially
	// completed deletion is resumed.
	if _, uerr := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, Lifecycle: LifecycleDeleting}); uerr != nil {
		t.Fatalf("re-asserting deleting must stay permitted, got: %v", uerr)
	}

	read, gerr := svc.GetUser(ctx, u.ID)
	if gerr != nil {
		t.Fatalf("GetUser: %v", gerr)
	}
	if read.Username != "nina" || read.Lifecycle != LifecycleDeleting {
		t.Fatalf("a refused edit landed anyway: %+v", read)
	}
}

// AddCertBinding is the enrollment seam, so it is the single most consequential
// write to leave open on a deleting account: a certificate bound after the
// deletion cascade swept the index is a live credential for an account on its
// way out.
func TestAddCertBinding_RefusedOnATerminalRecord(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()
	u := createTestUser(t, svc, "omar", "")
	if _, uerr := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, Lifecycle: LifecycleDeleting}); uerr != nil {
		t.Fatalf("mark deleting: %v", uerr)
	}

	key, kerr := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if kerr != nil {
		t.Fatalf("GenerateKey: %v", kerr)
	}
	thumbprint := ComputeSPKIThumbprint(makeCertForKey(t, key, "omar", 1))
	_, err := svc.AddCertBinding(ctx, u.ID, thumbprint, 0)
	var te *LifecycleTransitionError
	if !errors.As(err, &te) {
		t.Fatalf("expected *LifecycleTransitionError, got %T: %v", err, err)
	}

	if _, rerr := svc.UserByCertThumbprint(ctx, thumbprint); rerr == nil {
		t.Fatal("the refused binding still resolves — an index entry was written")
	}
}

// onlyReassertsLifecycle is an exhaustive field list, exactly like
// lastAuthOnly: a field added to UpdateUserRequest without being named there
// becomes writable on an account whose deletion has already begun.
func TestOnlyReassertsLifecycle_AnyOtherFieldDisqualifies(t *testing.T) {
	at := time.Now().UTC()
	base := func() *UpdateUserRequest {
		return &UpdateUserRequest{ID: 1, Lifecycle: LifecycleDeleting}
	}
	if !onlyReassertsLifecycle(base()) {
		t.Fatal("a lifecycle-only request was not recognized")
	}
	if onlyReassertsLifecycle(&UpdateUserRequest{ID: 1}) {
		t.Fatal("a request with no lifecycle was recognized")
	}

	disqualifying := map[string]func(*UpdateUserRequest){
		"Username":     func(r *UpdateUserRequest) { r.Username = "x" },
		"Email":        func(r *UpdateUserRequest) { r.Email = "x@example.com" },
		"Name":         func(r *UpdateUserRequest) { r.Name = "x" },
		"LastAuth":     func(r *UpdateUserRequest) { r.LastAuth = &at },
		"RoleIDs":      func(r *UpdateUserRequest) { r.RoleIDs = []string{} },
		"RoleBindings": func(r *UpdateUserRequest) { r.RoleBindings = []RoleBinding{} },
		"Attributes":   func(r *UpdateUserRequest) { r.Attributes = map[string]string{} },
		"CertBindings": func(r *UpdateUserRequest) { r.CertBindings = []CertBinding{} },
		"Factors":      func(r *UpdateUserRequest) { r.Factors = []FactorEnrollment{} },
		"AuthMethods":  func(r *UpdateUserRequest) { r.AuthMethods = []string{} },
	}
	for name, set := range disqualifying {
		req := base()
		set(req)
		if onlyReassertsLifecycle(req) {
			t.Errorf("a request also setting %s was treated as a bare re-assertion, which "+
				"would make it writable on a deleting account", name)
		}
	}

	const idAndLifecycle = 2
	if got := reflect.TypeOf(UpdateUserRequest{}).NumField(); got != len(disqualifying)+idAndLifecycle {
		t.Fatalf("UpdateUserRequest has %d fields but only %d are pinned here", got, len(disqualifying)+idAndLifecycle)
	}
}
