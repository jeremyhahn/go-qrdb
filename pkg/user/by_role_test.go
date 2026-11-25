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
	"testing"
)

// byRoleLister serves ListUsers from a fixed slice in pages of PageSize;
// every other UserServicer method is the nil embedded interface.
type byRoleLister struct {
	UserServicer
	users []*User
	err   error
	// failAfter is the number of pages served before err is returned.
	failAfter int
	calls     int
}

func (l *byRoleLister) ListUsers(_ context.Context, req *ListUsersRequest) (*ListUsersResponse, error) {
	l.calls++
	if l.err != nil && l.calls > l.failAfter {
		return nil, l.err
	}
	start := (req.Page - 1) * req.PageSize
	if start >= len(l.users) {
		return &ListUsersResponse{Total: len(l.users)}, nil
	}
	end := min(start+req.PageSize, len(l.users))
	return &ListUsersResponse{Users: l.users[start:end], Total: len(l.users), HasMore: end < len(l.users)}, nil
}

func TestHeldRoleIDs(t *testing.T) {
	u := &User{TenantID: "system", RoleIDs: []string{"admin"}, RoleBindings: []RoleBinding{
		{RoleID: "so", TenantID: "system"},
		{RoleID: "other", TenantID: "acme"},
	}}
	got := HeldRoleIDs(u, "")
	if len(got) != 2 || got[0] != "admin" || got[1] != "so" {
		t.Fatalf("HeldRoleIDs(system) = %v", got)
	}
	if got := HeldRoleIDs(u, "acme"); len(got) != 1 || got[0] != "other" {
		t.Fatalf("HeldRoleIDs(acme) = %v", got)
	}
	if HeldRoleIDs(nil, "system") != nil {
		t.Fatal("HeldRoleIDs(nil) must be nil")
	}
}

func TestUsersHoldingRole_PagesAndFilters(t *testing.T) {
	var users []*User
	for i := range 250 {
		u := &User{ID: uint64(i + 1), TenantID: "system"}
		if i%100 == 7 {
			u.RoleIDs = []string{"so"}
		}
		users = append(users, u)
	}
	users[249].RoleBindings = []RoleBinding{{RoleID: "so", TenantID: "system"}}
	l := &byRoleLister{users: users}
	got, err := UsersHoldingRole(context.Background(), l, "system", "so")
	if err != nil {
		t.Fatal(err)
	}
	if l.calls != 3 {
		t.Fatalf("ListUsers calls = %d, want 3", l.calls)
	}
	var ids []uint64
	for _, u := range got {
		ids = append(ids, u.ID)
	}
	want := []uint64{8, 108, 208, 250}
	if len(ids) != len(want) {
		t.Fatalf("holders = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("holders = %v, want %v", ids, want)
		}
	}
}

func TestUsersHoldingRole_Errors(t *testing.T) {
	ctx := context.Background()
	var v *ValidationError
	if _, err := UsersHoldingRole(ctx, nil, "system", "so"); !errors.As(err, &v) {
		t.Fatalf("nil servicer: %v", err)
	}
	if _, err := UsersHoldingRole(ctx, &byRoleLister{}, "system", ""); !errors.As(err, &v) {
		t.Fatalf("empty role: %v", err)
	}
	fault := errors.New("store fault")
	if _, err := UsersHoldingRole(ctx, &byRoleLister{err: fault}, "system", "so"); !errors.Is(err, fault) {
		t.Fatalf("fault: %v", err)
	}
	// A fault on a later page aborts the walk: the holders already found
	// are not returned as the whole answer.
	var users []*User
	for i := range 250 {
		users = append(users, &User{ID: uint64(i + 1), TenantID: "system", RoleIDs: []string{"so"}})
	}
	l := &byRoleLister{users: users, err: fault, failAfter: 1}
	got, err := UsersHoldingRole(ctx, l, "system", "so")
	if !errors.Is(err, fault) || got != nil {
		t.Fatalf("second-page fault: holders %d, err %v; want no holders and the fault", len(got), err)
	}
	if l.calls != 2 {
		t.Fatalf("ListUsers calls = %d, want 2", l.calls)
	}
}
