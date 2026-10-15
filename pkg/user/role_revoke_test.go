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
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// revokeHookStore is a real memory store whose CompareAndSwap and Get can be
// interposed on, so a test can land a concurrent edit or a failure exactly
// where the revoker reads or commits.
type revokeHookStore struct {
	*kvstore.MemoryKVStore
	beforeCAS func(ctx context.Context, key string)
	afterCAS  func(ctx context.Context, key string)
	getErr    error

	// errAfterCommit, when set, is returned once by the next CompareAndSwap
	// AFTER the swap has been applied: a commit whose outcome the caller
	// cannot know.
	errAfterCommit error
}

func newRevokeHookStore() *revokeHookStore {
	return &revokeHookStore{MemoryKVStore: kvstore.NewMemoryKVStore()}
}

func (s *revokeHookStore) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if s.beforeCAS != nil {
		s.beforeCAS(ctx, key)
	}
	ok, err := s.MemoryKVStore.CompareAndSwap(ctx, key, expected, next)
	if s.afterCAS != nil {
		s.afterCAS(ctx, key)
	}
	if err == nil && s.errAfterCommit != nil {
		err, s.errAfterCommit = s.errAfterCommit, nil
	}
	return ok, err
}

func (s *revokeHookStore) Get(ctx context.Context, key string) ([]byte, error) {
	if s.getErr != nil && strings.HasPrefix(key, keyPrefixPrimary) {
		return nil, s.getErr
	}
	return s.MemoryKVStore.Get(ctx, key)
}

// setRoles rewrites a user's RoleIDs directly in the store, bypassing the
// service: it stands in for an assignment committed by another writer.
func setRoles(t *testing.T, kv kvstore.KVStore, id uint64, roleIDs ...string) {
	t.Helper()
	rewriteUser(t, kv, id, func(u *User) { u.RoleIDs = roleIDs })
}

// rewriteUser applies edit to a user's stored record, bypassing the service.
func rewriteUser(t *testing.T, kv kvstore.KVStore, id uint64, edit func(*User)) {
	t.Helper()
	ctx := context.Background()
	raw, err := kv.Get(ctx, primaryKey(id))
	if err != nil || raw == nil {
		t.Fatalf("Get user %d: %v (present=%v)", id, err, raw != nil)
	}
	u := &User{}
	if err := json.Unmarshal(raw, u); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	edit(u)
	data, err := json.Marshal(u)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := kv.Put(ctx, primaryKey(id), data); err != nil {
		t.Fatalf("Put: %v", err)
	}
}

func newRevokeStore(t *testing.T, kv kvstore.KVStore) *kvUserStore {
	t.Helper()
	svc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return svc.(*kvUserStore)
}

func createHolder(t *testing.T, svc UserServicer, name, tenantID string, roleIDs []string, bindings []RoleBinding) *User {
	t.Helper()
	ctx := context.Background()
	u, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username: name, Email: name + "@example.com", TenantID: tenantID, RoleIDs: roleIDs,
	})
	if err != nil {
		t.Fatalf("CreateUser(%s): %v", name, err)
	}
	if bindings != nil {
		u, err = svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, RoleBindings: bindings})
		if err != nil {
			t.Fatalf("UpdateUser(%s) bindings: %v", name, err)
		}
	}
	return u
}

func mustGetUser(t *testing.T, svc UserServicer, id uint64) *User {
	t.Helper()
	u, err := svc.GetUser(context.Background(), id)
	if err != nil {
		t.Fatalf("GetUser(%d): %v", id, err)
	}
	return u
}

func holdsRole(u *User, roleID string) bool {
	return slices.Contains(u.RoleIDs, roleID) ||
		slices.ContainsFunc(u.RoleBindings, func(b RoleBinding) bool { return b.RoleID == roleID })
}

func TestRevokeRole_StripsRoleIDsAndBindingsAndKeepsTheRest(t *testing.T) {
	s := newRevokeStore(t, kvstore.NewMemoryKVStore())
	ctx := context.Background()

	a := createHolder(t, s, "alice", "acme", []string{"doomed", "kept"}, nil)
	b := createHolder(t, s, "bob", "acme", []string{"kept"}, []RoleBinding{{RoleID: "doomed", TenantID: "acme"}, {RoleID: "kept", TenantID: "acme"}})
	c := createHolder(t, s, "carol", "acme", []string{"kept"}, nil)

	n, err := s.RevokeRole(ctx, "acme", "doomed")
	if err != nil {
		t.Fatalf("RevokeRole: %v", err)
	}
	if n != 2 {
		t.Fatalf("RevokeRole changed %d accounts, want 2", n)
	}
	for _, u := range []*User{a, b, c} {
		got := mustGetUser(t, s, u.ID)
		if holdsRole(got, "doomed") {
			t.Fatalf("%s still holds the revoked role: %v %v", got.Username, got.RoleIDs, got.RoleBindings)
		}
		if !slices.Contains(got.RoleIDs, "kept") {
			t.Fatalf("%s lost a role that was not revoked: %v", got.Username, got.RoleIDs)
		}
	}
	if got := mustGetUser(t, s, b.ID); !slices.Equal(got.RoleBindings, []RoleBinding{{RoleID: "kept", TenantID: "acme"}}) {
		t.Fatalf("bob's bindings = %v, want only the kept binding", got.RoleBindings)
	}
	if got := mustGetUser(t, s, c.ID); !got.UpdatedAt.Equal(c.UpdatedAt) {
		t.Fatal("an account that did not hold the role was rewritten")
	}
}

func TestRevokeRole_LastRoleLeavesAnEmptySet(t *testing.T) {
	s := newRevokeStore(t, kvstore.NewMemoryKVStore())
	u := createHolder(t, s, "alice", "acme", []string{"doomed"}, nil)

	if _, err := s.RevokeRole(context.Background(), "acme", "doomed"); err != nil {
		t.Fatalf("RevokeRole: %v", err)
	}
	if got := mustGetUser(t, s, u.ID); len(got.RoleIDs) != 0 {
		t.Fatalf("RoleIDs = %v, want empty", got.RoleIDs)
	}
}

func TestRevokeRole_IsIdempotent(t *testing.T) {
	s := newRevokeStore(t, kvstore.NewMemoryKVStore())
	ctx := context.Background()
	createHolder(t, s, "alice", "acme", []string{"doomed"}, nil)

	if n, err := s.RevokeRole(ctx, "acme", "doomed"); err != nil || n != 1 {
		t.Fatalf("first RevokeRole = %d, %v; want 1, nil", n, err)
	}
	if n, err := s.RevokeRole(ctx, "acme", "doomed"); err != nil || n != 0 {
		t.Fatalf("second RevokeRole = %d, %v; want 0, nil", n, err)
	}
}

func TestRevokeRole_TouchesOnlyTheNamedTenant(t *testing.T) {
	s := newRevokeStore(t, kvstore.NewMemoryKVStore())
	other := createHolder(t, s, "olga", "globex", []string{"doomed"}, nil)

	if n, err := s.RevokeRole(context.Background(), "acme", "doomed"); err != nil || n != 0 {
		t.Fatalf("RevokeRole = %d, %v; want 0, nil", n, err)
	}
	if got := mustGetUser(t, s, other.ID); !slices.Contains(got.RoleIDs, "doomed") {
		t.Fatal("an account in another tenant was stripped")
	}
}

func TestRevokeRole_EmptyTenantMeansSystem(t *testing.T) {
	s := newRevokeStore(t, kvstore.NewMemoryKVStore())
	u := createHolder(t, s, "root2", "", []string{"doomed"}, nil)

	if n, err := s.RevokeRole(context.Background(), "", "doomed"); err != nil || n != 1 {
		t.Fatalf("RevokeRole = %d, %v; want 1, nil", n, err)
	}
	if got := mustGetUser(t, s, u.ID); holdsRole(got, "doomed") {
		t.Fatal("system-tenant holder was not stripped")
	}
}

func TestRevokeRole_RefusesAnEmptyRoleID(t *testing.T) {
	s := newRevokeStore(t, kvstore.NewMemoryKVStore())
	_, err := s.RevokeRole(context.Background(), "acme", "")
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("RevokeRole(\"\") = %v, want *ValidationError", err)
	}
}

func TestRevokeRole_SkipsAnAccountBeingDeleted(t *testing.T) {
	kv := kvstore.NewMemoryKVStore()
	s := newRevokeStore(t, kv)
	ctx := context.Background()
	u := createHolder(t, s, "alice", "acme", []string{"doomed"}, nil)
	rewriteUser(t, kv, u.ID, func(u *User) { u.Lifecycle = LifecycleDeleting })

	if n, err := s.RevokeRole(ctx, "acme", "doomed"); err != nil || n != 0 {
		t.Fatalf("RevokeRole = %d, %v; want 0, nil", n, err)
	}
}

// A role assigned by another writer between the revoker's read and its
// commit must survive: the revoker re-reads and strips only its own role.
func TestRevokeRole_AConcurrentAssignmentIsNotLost(t *testing.T) {
	kv := newRevokeHookStore()
	s := newRevokeStore(t, kv)
	u := createHolder(t, s, "alice", "acme", []string{"doomed"}, nil)

	fired := false
	kv.beforeCAS = func(_ context.Context, key string) {
		if fired || key != primaryKey(u.ID) {
			return
		}
		fired = true
		setRoles(t, kv.MemoryKVStore, u.ID, "doomed", "late")
	}

	if n, err := s.RevokeRole(context.Background(), "acme", "doomed"); err != nil || n != 1 {
		t.Fatalf("RevokeRole = %d, %v; want 1, nil", n, err)
	}
	if !fired {
		t.Fatal("the concurrent assignment was never injected")
	}
	if got := mustGetUser(t, s, u.ID); !slices.Equal(got.RoleIDs, []string{"late"}) {
		t.Fatalf("RoleIDs = %v, want [late]", got.RoleIDs)
	}
}

// A commit that errors after it landed is the store's success: updateUserAt
// reads the record back, finds its own write, and the account counts.
func TestRevokeRole_AnAmbiguousCommitThatLandedCounts(t *testing.T) {
	kv := newRevokeHookStore()
	s := newRevokeStore(t, kv)
	u := createHolder(t, s, "alice", "acme", []string{"doomed", "kept"}, nil)

	kv.errAfterCommit = errors.New("transport reset after commit")

	n, err := s.RevokeRole(context.Background(), "acme", "doomed")
	if err != nil || n != 1 {
		t.Fatalf("RevokeRole = %d, %v; want 1, nil", n, err)
	}
	if got := mustGetUser(t, s, u.ID); !slices.Equal(got.RoleIDs, []string{"kept"}) {
		t.Fatalf("RoleIDs = %v, want [kept]", got.RoleIDs)
	}
}

func TestRevokeRole_ReportsAStoreFailure(t *testing.T) {
	kv := newRevokeHookStore()
	s := newRevokeStore(t, kv)
	createHolder(t, s, "alice", "acme", []string{"doomed"}, nil)
	kv.getErr = errors.New("disk gone")

	_, err := s.RevokeRole(context.Background(), "acme", "doomed")
	var se *StoreError
	if !errors.As(err, &se) || se.Op != "revoke role" {
		t.Fatalf("RevokeRole = %v, want a revoke-role *StoreError", err)
	}
}

func TestRevokeRole_ReportsACorruptTenantIndex(t *testing.T) {
	kv := kvstore.NewMemoryKVStore()
	s := newRevokeStore(t, kv)
	if err := kv.Put(context.Background(), keyPrefixTenant+"acme/x", []byte("not-an-id")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	_, err := s.RevokeRole(context.Background(), "acme", "doomed")
	var se *StoreError
	if !errors.As(err, &se) || se.Detail != "decode tenant index value" {
		t.Fatalf("RevokeRole = %v, want a decode *StoreError", err)
	}
}

// An account that keeps losing the race reports the race rather than looping.
func TestRevokeRole_GivesUpOnAnAccountThatNeverSettles(t *testing.T) {
	kv := newRevokeHookStore()
	s := newRevokeStore(t, kv)
	u := createHolder(t, s, "alice", "acme", []string{"doomed"}, nil)

	n := 0
	kv.beforeCAS = func(_ context.Context, key string) {
		if key == primaryKey(u.ID) {
			n++
			setRoles(t, kv.MemoryKVStore, u.ID, "doomed", "edit-"+strconv.Itoa(n))
		}
	}

	_, err := s.RevokeRole(context.Background(), "acme", "doomed")
	var ce *ConcurrentUpdateError
	if !errors.As(err, &ce) {
		t.Fatalf("RevokeRole = %v, want *ConcurrentUpdateError", err)
	}
	if n != maxRevokeAttempts {
		t.Fatalf("CAS attempts = %d, want %d", n, maxRevokeAttempts)
	}
}

// A role re-granted after every pass ends the sweep with a typed error after
// the pass budget, never an endless loop.
func TestRevokeRole_StopsAfterThePassBudget(t *testing.T) {
	kv := newRevokeHookStore()
	s := newRevokeStore(t, kv)
	u := createHolder(t, s, "alice", "acme", []string{"doomed"}, nil)

	kv.afterCAS = func(_ context.Context, key string) {
		if key == primaryKey(u.ID) {
			setRoles(t, kv.MemoryKVStore, u.ID, "doomed")
		}
	}

	n, err := s.RevokeRole(context.Background(), "acme", "doomed")
	var ie *RoleRevocationIncompleteError
	if !errors.As(err, &ie) {
		t.Fatalf("RevokeRole = %v, want *RoleRevocationIncompleteError", err)
	}
	if ie.RoleID != "doomed" || ie.Passes != maxRevokePasses || n != maxRevokePasses {
		t.Fatalf("error = %+v after %d changes", ie, n)
	}
}

func TestNewRoleRevoker_AcceptsTheStore(t *testing.T) {
	s := newRevokeStore(t, kvstore.NewMemoryKVStore())
	createHolder(t, s, "alice", "acme", []string{"doomed"}, nil)

	rv, err := NewRoleRevoker(s)
	if err != nil {
		t.Fatalf("NewRoleRevoker: %v", err)
	}
	if n, err := rv.RevokeRole(context.Background(), "acme", "doomed"); err != nil || n != 1 {
		t.Fatalf("RevokeRole = %d, %v; want 1, nil", n, err)
	}
}

// foreignUserService is a UserServicer from outside this package.
type foreignUserService struct{ UserServicer }

func TestNewRoleRevoker_RefusesAForeignService(t *testing.T) {
	_, err := NewRoleRevoker(foreignUserService{})
	var ue *RoleRevokerUnsupportedError
	if !errors.As(err, &ue) {
		t.Fatalf("NewRoleRevoker = %v, want *RoleRevokerUnsupportedError", err)
	}
	if !strings.Contains(ue.Error(), "cannot revoke roles") {
		t.Fatalf("Error() = %q", ue.Error())
	}
}

func TestRoutingRevokeRole_StripsInTheTenantsOwnStore(t *testing.T) {
	acme := kvstore.NewMemoryKVStore()
	resolver := &fakeResolver{shared: kvstore.NewMemoryKVStore(), tenants: map[string]kvstore.KVStore{"acme": acme}}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	ctx := tenant.WithTenantID(context.Background(), "acme")
	u, err := svc.CreateUser(ctx, &CreateUserRequest{Username: "alice", Email: "alice@example.com", TenantID: "acme", RoleIDs: []string{"doomed", "kept"}})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	rv, err := NewRoleRevoker(svc)
	if err != nil {
		t.Fatalf("NewRoleRevoker: %v", err)
	}
	if n, err := rv.RevokeRole(context.Background(), "acme", "doomed"); err != nil || n != 1 {
		t.Fatalf("RevokeRole = %d, %v; want 1, nil", n, err)
	}
	got, err := svc.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if !slices.Equal(got.RoleIDs, []string{"kept"}) {
		t.Fatalf("RoleIDs = %v, want [kept]", got.RoleIDs)
	}
}

func TestRoutingRevokeRole_ReportsAnUnresolvableTenant(t *testing.T) {
	resolver := &fakeResolver{shared: kvstore.NewMemoryKVStore(), sealed: map[string]bool{"acme": true}}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	rv, err := NewRoleRevoker(svc)
	if err != nil {
		t.Fatalf("NewRoleRevoker: %v", err)
	}
	if _, err := rv.RevokeRole(context.Background(), "acme", "doomed"); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("RevokeRole = %v, want ErrTenantSealed", err)
	}
}

func TestRoleRevocationIncompleteError_Error(t *testing.T) {
	err := &RoleRevocationIncompleteError{RoleID: "doomed", Passes: 3}
	for _, want := range []string{"doomed", "3", "retry"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Error() = %q, missing %q", err.Error(), want)
		}
	}
}
