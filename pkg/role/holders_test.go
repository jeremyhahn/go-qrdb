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

package role

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// The user store's revoker is the HolderRevoker every node composes.
var _ HolderRevoker = (*user.RoleRevoker)(nil)

// noHolders is a HolderRevoker for stores whose tests hold no accounts.
type noHolders struct{}

func (noHolders) RevokeRole(context.Context, string, string) (int, error) { return 0, nil }

// failingHolders is a HolderRevoker that fails every call.
type failingHolders struct{ err error }

func (f failingHolders) RevokeRole(context.Context, string, string) (int, error) { return 0, f.err }

// casFaultStore is a real memory store whose CompareAndSwap starts failing
// after a set number of successful commits, so a delete can be interrupted
// partway through stripping its holders.
type casFaultStore struct {
	*kvstore.MemoryKVStore
	failAfter atomic.Int64 // < 0 disarms
	swaps     atomic.Int64
}

func newCASFaultStore() *casFaultStore {
	s := &casFaultStore{MemoryKVStore: kvstore.NewMemoryKVStore()}
	s.failAfter.Store(-1)
	return s
}

func (s *casFaultStore) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if limit := s.failAfter.Load(); limit >= 0 && s.swaps.Load() >= limit {
		return false, errors.New("injected commit failure")
	}
	ok, err := s.MemoryKVStore.CompareAndSwap(ctx, key, expected, next)
	if ok && err == nil {
		s.swaps.Add(1)
	}
	return ok, err
}

// newCascadeStores builds a role store and a user store over ONE kv store,
// composed the way a node composes them.
func newCascadeStores(t *testing.T, kv kvstore.KVStore) (RoleServicer, user.UserServicer) {
	t.Helper()
	gen, err := dao.NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatalf("NewSnowflakeGenerator: %v", err)
	}
	users, err := user.NewStore(kv, gen)
	if err != nil {
		t.Fatalf("user.NewStore: %v", err)
	}
	holders, err := user.NewRoleRevoker(users)
	if err != nil {
		t.Fatalf("NewRoleRevoker: %v", err)
	}
	roles, err := NewStore(kv, WithHolderRevoker(holders))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return roles, users
}

// seedHolders creates n accounts in tenantID holding every role in roleIDs.
func seedHolders(t *testing.T, users user.UserServicer, tenantID string, n int, roleIDs ...string) {
	t.Helper()
	for i := range n {
		name := "holder" + strconv.Itoa(i)
		if _, err := users.CreateUser(context.Background(), &user.CreateUserRequest{
			Username: name, Email: name + "@example.com", TenantID: tenantID, RoleIDs: roleIDs,
		}); err != nil {
			t.Fatalf("CreateUser(%s): %v", name, err)
		}
	}
}

// assertNoHolder pages through every account in tenantID and fails if any
// still names roleID, or if any lost keptID. It returns how many it saw.
func assertNoHolder(t *testing.T, users user.UserServicer, tenantID, roleID, keptID string) int {
	t.Helper()
	seen := 0
	for page := 1; ; page++ {
		resp, err := users.ListUsers(context.Background(), &user.ListUsersRequest{TenantID: tenantID, Page: page})
		if err != nil {
			t.Fatalf("ListUsers page %d: %v", page, err)
		}
		for _, u := range resp.Users {
			seen++
			if slices.Contains(u.RoleIDs, roleID) {
				t.Fatalf("user %s still holds deleted role %s: %v", u.Username, roleID, u.RoleIDs)
			}
			if keptID != "" && !slices.Contains(u.RoleIDs, keptID) {
				t.Fatalf("user %s lost role %s, which was not deleted: %v", u.Username, keptID, u.RoleIDs)
			}
		}
		if !resp.HasMore {
			return seen
		}
	}
}

func createPermRole(t *testing.T, svc RoleServicer, name, tenantID string, p permission.Permission) *Role {
	t.Helper()
	r, err := svc.CreateRole(context.Background(), &CreateRoleRequest{
		Name: name, TenantID: tenantID, Permissions: []permission.Permission{p},
	})
	if err != nil {
		t.Fatalf("CreateRole(%s): %v", name, err)
	}
	return r
}

// holderCount is more than one ListUsers page, so a strip that only reached
// the first page would leave holders behind.
const holderCount = defaultPageSize + 30

func TestDeleteRole_StripsTheRoleFromEveryHolder(t *testing.T) {
	roles, users := newCascadeStores(t, kvstore.NewMemoryKVStore())
	ctx := context.Background()
	doomed := createPermRole(t, roles, "doomed", "acme", permission.KVPut)
	kept := createPermRole(t, roles, "kept", "acme", permission.KVGet)
	seedHolders(t, users, "acme", holderCount, doomed.ID, kept.ID)

	if err := roles.DeleteRole(ctx, doomed.ID); err != nil {
		t.Fatalf("DeleteRole: %v", err)
	}

	if seen := assertNoHolder(t, users, "acme", doomed.ID, kept.ID); seen != holderCount {
		t.Fatalf("saw %d accounts, want %d", seen, holderCount)
	}
	var nf *RoleNotFoundError
	if _, err := roles.GetRole(ctx, doomed.ID); !errors.As(err, &nf) {
		t.Fatalf("GetRole after delete = %v, want *RoleNotFoundError", err)
	}
}

// A delete interrupted partway leaves the role marked Deleting — granting
// nothing and assignable to no one — and a plain retry finishes it.
func TestDeleteRole_AnInterruptedStripIsFinishedByRetry(t *testing.T) {
	kv := newCASFaultStore()
	roles, users := newCascadeStores(t, kv)
	ctx := context.Background()
	doomed := createPermRole(t, roles, "doomed", "acme", permission.KVPut)
	kept := createPermRole(t, roles, "kept", "acme", permission.KVGet)
	seedHolders(t, users, "acme", holderCount, doomed.ID, kept.ID)

	kv.failAfter.Store(kv.swaps.Load() + holderCount/2)
	err := roles.DeleteRole(ctx, doomed.ID)
	var hre *HolderRevocationError
	if !errors.As(err, &hre) {
		t.Fatalf("interrupted DeleteRole = %v, want *HolderRevocationError", err)
	}
	if hre.ID != doomed.ID || hre.TenantID != "acme" {
		t.Fatalf("HolderRevocationError = %+v", hre)
	}

	// Mid-delete: the role is still readable, marked, and inert.
	mid, err := roles.GetRole(ctx, doomed.ID)
	if err != nil {
		t.Fatalf("GetRole mid-delete: %v", err)
	}
	if !mid.Deleting {
		t.Fatal("an interrupted delete left the role unmarked")
	}
	perms, err := roles.ResolvePermissions(ctx, []string{doomed.ID, kept.ID})
	if err != nil {
		t.Fatalf("ResolvePermissions: %v", err)
	}
	if !slices.Equal(perms, []permission.Permission{permission.KVGet}) {
		t.Fatalf("ResolvePermissions = %v, want only the kept role's grant", perms)
	}
	var rde *RoleDeletingError
	if err := ValidateAssignable(ctx, roles, doomed.ID, "acme"); !errors.As(err, &rde) {
		t.Fatalf("ValidateAssignable mid-delete = %v, want *RoleDeletingError", err)
	}

	kv.failAfter.Store(-1)
	if err := roles.DeleteRole(ctx, doomed.ID); err != nil {
		t.Fatalf("retried DeleteRole: %v", err)
	}
	if seen := assertNoHolder(t, users, "acme", doomed.ID, kept.ID); seen != holderCount {
		t.Fatalf("saw %d accounts, want %d", seen, holderCount)
	}
	var nf *RoleNotFoundError
	if _, err := roles.GetRole(ctx, doomed.ID); !errors.As(err, &nf) {
		t.Fatalf("GetRole after retry = %v, want *RoleNotFoundError", err)
	}
}

func TestDeleteRole_WithoutAHolderRevokerIsRefused(t *testing.T) {
	svc, err := NewStore(newMemKVStore())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	r := createTestRole(t, svc, "editor", "acme")

	err = svc.DeleteRole(context.Background(), r.ID)
	var hrr *HolderRevokerRequiredError
	if !errors.As(err, &hrr) || hrr.ID != r.ID {
		t.Fatalf("DeleteRole = %v, want *HolderRevokerRequiredError", err)
	}
	got, err := svc.GetRole(context.Background(), r.ID)
	if err != nil {
		t.Fatalf("GetRole: %v", err)
	}
	if got.Deleting {
		t.Fatal("a refused delete marked the role")
	}
}

func TestDeleteRole_ARevokerFailureKeepsTheRoleMarked(t *testing.T) {
	sentinel := errors.New("users unreachable")
	svc, err := NewStore(newMemKVStore(), WithHolderRevoker(failingHolders{err: sentinel}))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	r := createTestRole(t, svc, "editor", "acme")

	err = svc.DeleteRole(context.Background(), r.ID)
	if !errors.Is(err, sentinel) {
		t.Fatalf("DeleteRole = %v, want it to wrap the revoker's failure", err)
	}
	got, gerr := svc.GetRole(context.Background(), r.ID)
	if gerr != nil {
		t.Fatalf("GetRole: %v", gerr)
	}
	if !got.Deleting {
		t.Fatal("the role was not marked Deleting before its holders were stripped")
	}
}

func TestDeleteRole_MarkWriteFailureLeavesTheRoleIntact(t *testing.T) {
	fkv := &faultKVStore{inner: newMemKVStore()}
	svc, err := NewStore(fkv, WithHolderRevoker(noHolders{}))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	r := createTestRole(t, svc, "editor", "acme")

	fkv.putErr = errors.New("disk full")
	fkv.putFailAt = fkv.putCount + 1
	err = svc.DeleteRole(context.Background(), r.ID)
	var se *StoreError
	if !errors.As(err, &se) || se.Detail != "mark deleting" {
		t.Fatalf("DeleteRole = %v, want a mark-deleting *StoreError", err)
	}
}

func TestResolvePermissions_SkipsARoleBeingDeleted(t *testing.T) {
	svc, err := NewStore(newMemKVStore(), WithHolderRevoker(failingHolders{err: errors.New("stop")}))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	r := createPermRole(t, svc, "doomed", "acme", permission.KVPut)
	_ = svc.DeleteRole(ctx, r.ID)

	perms, err := svc.ResolvePermissions(ctx, []string{r.ID})
	if err != nil {
		t.Fatalf("ResolvePermissions: %v", err)
	}
	if len(perms) != 0 {
		t.Fatalf("a role being deleted still granted %v", perms)
	}
}

func TestValidateAssignable_RefusesARoleBeingDeleted(t *testing.T) {
	svc, err := NewStore(newMemKVStore(), WithHolderRevoker(failingHolders{err: errors.New("stop")}))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	r := createTestRole(t, svc, "doomed", "acme")
	if err := ValidateAssignable(ctx, svc, r.ID, "acme"); err != nil {
		t.Fatalf("ValidateAssignable before delete: %v", err)
	}
	_ = svc.DeleteRole(ctx, r.ID)

	var rde *RoleDeletingError
	if err := ValidateAssignable(ctx, svc, r.ID, "acme"); !errors.As(err, &rde) || rde.ID != r.ID {
		t.Fatalf("ValidateAssignable = %v, want *RoleDeletingError", err)
	}
}

func TestUpdateRole_RefusesARoleBeingDeleted(t *testing.T) {
	svc, err := NewStore(newMemKVStore(), WithHolderRevoker(failingHolders{err: errors.New("stop")}))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	r := createPermRole(t, svc, "doomed", "acme", permission.KVGet)
	_ = svc.DeleteRole(ctx, r.ID)

	var rde *RoleDeletingError
	_, err = svc.UpdateRole(ctx, &UpdateRoleRequest{ID: r.ID, Permissions: []permission.Permission{permission.KVPut}})
	if !errors.As(err, &rde) || rde.ID != r.ID || rde.Op != "updated" {
		t.Fatalf("UpdateRole = %v, want *RoleDeletingError{Op: updated}", err)
	}
	got, err := svc.GetRole(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRole: %v", err)
	}
	if !got.Deleting || len(got.Permissions) != 1 || got.Permissions[0] != permission.KVGet {
		t.Fatalf("refused UpdateRole changed the role: %+v", got)
	}
}

func TestUpdateRole_EditsARoleNotBeingDeleted(t *testing.T) {
	svc, err := NewStore(newMemKVStore(), WithHolderRevoker(noHolders{}))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	r := createPermRole(t, svc, "live", "acme", permission.KVGet)
	got, err := svc.UpdateRole(context.Background(), &UpdateRoleRequest{ID: r.ID, Permissions: []permission.Permission{permission.KVPut}})
	if err != nil {
		t.Fatalf("UpdateRole: %v", err)
	}
	if got.Deleting || len(got.Permissions) != 1 || got.Permissions[0] != permission.KVPut {
		t.Fatalf("UpdateRole = %+v", got)
	}
}

func TestHolderErrors_Messages(t *testing.T) {
	inner := errors.New("boom")
	cases := []struct {
		err  error
		want []string
	}{
		{&RoleDeletingError{ID: "r1", Op: "updated"}, []string{"r1", "being deleted", "updated"}},
		{&HolderRevokerRequiredError{ID: "r2"}, []string{"r2", "holder revoker"}},
		{&HolderRevocationError{ID: "r3", TenantID: "acme", Err: inner}, []string{"r3", "acme", "retry", "boom"}},
	}
	for _, c := range cases {
		for _, w := range c.want {
			if !strings.Contains(c.err.Error(), w) {
				t.Fatalf("%T.Error() = %q, missing %q", c.err, c.err.Error(), w)
			}
		}
	}
	if !errors.Is(&HolderRevocationError{Err: inner}, inner) {
		t.Fatal("HolderRevocationError does not unwrap to its cause")
	}
}
