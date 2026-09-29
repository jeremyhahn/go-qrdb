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
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// createInTenant creates a user in tenantID and returns it.
func createInTenant(t *testing.T, svc UserServicer, tenantID, username string) *User {
	t.Helper()
	u, err := svc.CreateUser(context.Background(), &CreateUserRequest{
		Username: username,
		Email:    username + "@example.com",
		TenantID: tenantID,
	})
	if err != nil {
		t.Fatalf("CreateUser(%s/%s): %v", tenantID, username, err)
	}
	return u
}

// A binding grants only when the request targets the tenant it names, and a
// non-system identity is refused every request outside its own tenant before
// a binding is consulted. A binding naming another tenant on such a user can
// therefore never grant anything: it is refused at write, not stored dead.
func TestUpdateUser_CrossTenantBindingOnTenantUserRefused(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()
	u := createInTenant(t, svc, "acme", "alice")

	_, err := svc.UpdateUser(ctx, &UpdateUserRequest{
		ID:           u.ID,
		RoleBindings: []RoleBinding{{RoleID: "admin", TenantID: "globex"}},
	})
	var be *RoleBindingTenantError
	if !errors.As(err, &be) {
		t.Fatalf("expected *RoleBindingTenantError, got %T: %v", err, err)
	}
	if be.UserTenantID != "acme" || be.BindingTenantID != "globex" {
		t.Errorf("error fields: got user=%q binding=%q", be.UserTenantID, be.BindingTenantID)
	}

	got, gerr := svc.GetUser(ctx, u.ID)
	if gerr != nil {
		t.Fatalf("GetUser: %v", gerr)
	}
	if len(got.RoleBindings) != 0 {
		t.Errorf("refused write was stored: %+v", got.RoleBindings)
	}
}

// The system tenant (the empty binding tenant normalizes onto it too) is not
// the tenant-A user's own tenant, so it is equally dead.
func TestUpdateUser_SystemBindingOnTenantUserRefused(t *testing.T) {
	svc := newTestStore(t)
	u := createInTenant(t, svc, "acme", "alice")

	for _, bt := range []string{"", tenantid.System} {
		_, err := svc.UpdateUser(context.Background(), &UpdateUserRequest{
			ID:           u.ID,
			RoleBindings: []RoleBinding{{RoleID: "admin", TenantID: bt}},
		})
		var be *RoleBindingTenantError
		if !errors.As(err, &be) {
			t.Fatalf("binding tenant %q: expected *RoleBindingTenantError, got %T: %v", bt, err, err)
		}
		if be.BindingTenantID != tenantid.System {
			t.Errorf("binding tenant %q: error names %q, want the normalized %q", bt, be.BindingTenantID, tenantid.System)
		}
	}
}

// A binding in the user's own tenant is redundant with RoleIDs but live, so
// it is accepted.
func TestUpdateUser_OwnTenantBindingOnTenantUserAccepted(t *testing.T) {
	svc := newTestStore(t)
	u := createInTenant(t, svc, "acme", "alice")

	got, err := svc.UpdateUser(context.Background(), &UpdateUserRequest{
		ID:           u.ID,
		RoleBindings: []RoleBinding{{RoleID: "operator", TenantID: "acme"}},
	})
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	want := []RoleBinding{{RoleID: "operator", TenantID: "acme"}}
	if !reflect.DeepEqual(got.RoleBindings, want) {
		t.Errorf("RoleBindings: got %+v, want %+v", got.RoleBindings, want)
	}
}

// A system-tenant user may act in any tenant, so a binding naming another
// tenant is exactly the cross-tenant grant bindings exist to express.
func TestUpdateUser_CrossTenantBindingOnSystemUserAccepted(t *testing.T) {
	svc := newTestStore(t)
	u := createInTenant(t, svc, tenantid.System, "ops")

	got, err := svc.UpdateUser(context.Background(), &UpdateUserRequest{
		ID:           u.ID,
		RoleBindings: []RoleBinding{{RoleID: "admin", TenantID: "acme"}, {RoleID: "admin", TenantID: "globex"}},
	})
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if len(got.RoleBindings) != 2 {
		t.Errorf("RoleBindings: got %+v, want both", got.RoleBindings)
	}
}

// The read side compares a binding's TenantID with the normalized target
// tenant, so a binding stored as "" would never match anything. It is
// normalized on write, and the caller's request slice is not mutated.
func TestUpdateUser_BindingTenantNormalizedOnWrite(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()
	u := createInTenant(t, svc, tenantid.System, "ops")

	req := &UpdateUserRequest{
		ID:           u.ID,
		RoleBindings: []RoleBinding{{RoleID: "admin", TenantID: ""}, {RoleID: "admin", TenantID: "acme"}},
	}
	if _, err := svc.UpdateUser(ctx, req); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	got, err := svc.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	want := []RoleBinding{{RoleID: "admin", TenantID: tenantid.System}, {RoleID: "admin", TenantID: "acme"}}
	if !reflect.DeepEqual(got.RoleBindings, want) {
		t.Errorf("stored RoleBindings: got %+v, want %+v", got.RoleBindings, want)
	}
	if req.RoleBindings[0].TenantID != "" {
		t.Errorf("request slice mutated: %+v", req.RoleBindings)
	}
}

func TestRoleBindingTenantError_Message(t *testing.T) {
	e := &RoleBindingTenantError{UserID: 7, UserTenantID: "acme", BindingTenantID: "globex"}
	want := "user: role binding in tenant globex can never apply to user 7 of tenant acme: only a system-tenant user may hold a binding outside its own tenant"
	if e.Error() != want {
		t.Errorf("Error():\n got %q\nwant %q", e.Error(), want)
	}
}
