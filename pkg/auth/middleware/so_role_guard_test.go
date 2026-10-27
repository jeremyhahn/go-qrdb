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

package middleware

import (
	"context"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// racedUserStore is a user store whose UpdateUser first lets a concurrent
// write land (beforeUpdate), then judges the request's Guard against the
// record it replaces and writes, as the user store does against the base of
// its conditional commit.
type racedUserStore struct {
	user.UserServicer
	rec          user.User
	beforeUpdate func(*user.User)
}

func (s *racedUserStore) GetUser(_ context.Context, _ uint64) (*user.User, error) {
	cp := s.rec
	return &cp, nil
}

func (s *racedUserStore) UpdateUser(_ context.Context, req *user.UpdateUserRequest) (*user.User, error) {
	if s.beforeUpdate != nil {
		s.beforeUpdate(&s.rec)
		s.beforeUpdate = nil
	}
	prior, next := s.rec, s.rec
	if req.RoleIDs != nil {
		next.RoleIDs = req.RoleIDs
	}
	if req.RoleBindings != nil {
		next.RoleBindings = req.RoleBindings
	}
	if req.Guard != nil {
		if err := req.Guard(&prior, &next); err != nil {
			return nil, err
		}
	}
	s.rec = next
	cp := next
	return &cp, nil
}

// TestSORoleGuard_AdminWriteOverlappingAnSOGrant is the interleaving the gate
// alone lets through: an Admin's role replacement is authorized against a
// record without the SO role, an SO grants it before the write, and the write
// would remove it. The guard judges the record the write replaces and refuses.
func TestSORoleGuard_AdminWriteOverlappingAnSOGrant(t *testing.T) {
	f := newSORoleFixture(t)
	admin := f.caller(f.admin)
	store := &racedUserStore{rec: user.User{ID: 7, TenantID: "acme", RoleIDs: []string{f.userRole}}}

	roleIDs := []string{f.userRole}
	if err := f.mw.AuthorizeRoleAssignmentWrite(admin, store, f.roles, 7, roleIDs, nil); err != nil {
		t.Fatalf("gate: %v", err)
	}
	store.beforeUpdate = func(u *user.User) { u.RoleIDs = append(slices.Clone(u.RoleIDs), f.so) }
	_, err := store.UpdateUser(admin, &user.UpdateUserRequest{ID: 7, RoleIDs: roleIDs, Guard: f.mw.SORoleGuard(admin)})
	wantErrorAs[*SORoleRequiredError](t, err)
	if !slices.Contains(store.rec.RoleIDs, f.so) {
		t.Fatalf("the SO grant was removed: roles = %v", store.rec.RoleIDs)
	}
}

// TestSORoleGuard_AdminUnassignOverlappingAnSOGrant: an Admin's unassignment
// of the SO role from an account that did not hold it passes the gate as a
// no-op; once an SO grants it, the write that follows would revoke it.
func TestSORoleGuard_AdminUnassignOverlappingAnSOGrant(t *testing.T) {
	f := newSORoleFixture(t)
	admin := f.caller(f.admin)
	store := &racedUserStore{rec: user.User{ID: 7, TenantID: "acme"}}

	if err := f.mw.AuthorizeRoleUnassignment(admin, store, 7, f.so); err != nil {
		t.Fatalf("gate: %v", err)
	}
	store.beforeUpdate = func(u *user.User) { u.RoleIDs = []string{f.so} }
	_, err := store.UpdateUser(admin, &user.UpdateUserRequest{ID: 7, RoleIDs: []string{}, Guard: f.mw.SORoleGuard(admin)})
	wantErrorAs[*SORoleRequiredError](t, err)
	if !slices.Contains(store.rec.RoleIDs, f.so) {
		t.Fatalf("the SO grant was removed: roles = %v", store.rec.RoleIDs)
	}
}

// TestSORoleGuard_Transitions judges the guard directly on each kind of
// transition.
func TestSORoleGuard_Transitions(t *testing.T) {
	f := newSORoleFixture(t)
	acme := func(roleIDs ...string) *user.User { return &user.User{ID: 7, TenantID: "acme", RoleIDs: roleIDs} }
	tests := []struct {
		name        string
		ctx         context.Context
		prior, next *user.User
		check       func(t *testing.T, err error)
	}{
		{"admin grants SO", f.caller(f.admin), acme(), acme(f.so), wantErrorAs[*SORoleRequiredError]},
		{"admin revokes SO", f.caller(f.admin), acme(f.so), acme(), wantErrorAs[*SORoleRequiredError]},
		{"admin changes another role of an SO", f.caller(f.admin), acme(f.so), acme(f.so, f.userRole), wantNoError},
		{"admin leaves roles alone", f.caller(f.admin), acme(f.so), acme(f.so), wantNoError},
		{"SO grants SO", f.caller(f.so), acme(), acme(f.so), wantNoError},
		{"SO revokes SO", f.caller(f.so), acme(f.so), acme(), wantNoError},
		{"system SO grants SO", f.caller(f.sysSO), acme(), acme(f.so), wantErrorAs[*SORoleRequiredError]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, f.mw.SORoleGuard(tt.ctx)(tt.prior, tt.next))
		})
	}
}

// TestSORoleGuard_NilMiddlewareHasNoGuard: a composition without RBAC
// attaches no guard.
func TestSORoleGuard_NilMiddlewareHasNoGuard(t *testing.T) {
	var m *AuthMiddleware
	if g := m.SORoleGuard(context.Background()); g != nil {
		t.Fatal("a nil middleware returned a guard")
	}
}

// boundCaller is an acme caller holding roleIDs and the given bindings, the
// way authentication carries them: bindings apart from Roles.
func boundCaller(roleIDs []string, bindings ...auth.RoleBinding) context.Context {
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{
		Subject: "acme-caller", TenantID: "acme", Roles: roleIDs, RoleBindings: bindings,
	})
	return tenant.WithTenantID(ctx, "acme")
}

// TestSORoleChange_CallerBindings: a caller holding the SO role only through
// a binding scoped to the target tenant grants and revokes it; a binding
// scoped to another tenant counts for nothing, and the Admin stays refused.
func TestSORoleChange_CallerBindings(t *testing.T) {
	f := newSORoleFixture(t)
	acmeUser := func(roleIDs ...string) *userTenantFixture {
		return &userTenantFixture{u: &user.User{ID: 7, TenantID: "acme", RoleIDs: roleIDs}}
	}
	boundSO := boundCaller(nil, auth.RoleBinding{RoleID: f.so, TenantID: "acme"})
	foreignSO := role.SystemRoleID("globex", role.RoleNameSO)
	foreignBound := boundCaller([]string{f.admin},
		auth.RoleBinding{RoleID: f.so, TenantID: "globex"},
		auth.RoleBinding{RoleID: foreignSO, TenantID: "globex"})
	admin := boundCaller([]string{f.admin}, auth.RoleBinding{RoleID: f.userRole, TenantID: "acme"})

	tests := []struct {
		name  string
		call  func() error
		check func(t *testing.T, err error)
	}{
		{"bound SO assigns SO", func() error { return f.mw.AuthorizeRoleAssignment(boundSO, acmeUser(), f.roles, 7, f.so) }, wantNoError},
		{"bound SO unassigns SO", func() error { return f.mw.AuthorizeRoleUnassignment(boundSO, acmeUser(f.so), 7, f.so) }, wantNoError},
		{"bound SO replaces role_ids granting SO", func() error {
			return f.mw.AuthorizeRoleAssignmentWrite(boundSO, acmeUser(), f.roles, 7, []string{f.so}, nil)
		}, wantNoError},
		{"bound SO guard revokes SO", func() error {
			return f.mw.SORoleGuard(boundSO)(acmeUser(f.so).u, acmeUser().u)
		}, wantNoError},
		{"foreign-bound SO assigns SO", func() error {
			return f.mw.AuthorizeRoleAssignment(foreignBound, acmeUser(), f.roles, 7, f.so)
		}, wantErrorAs[*SORoleRequiredError]},
		{"foreign-bound SO unassigns SO", func() error {
			return f.mw.AuthorizeRoleUnassignment(foreignBound, acmeUser(f.so), 7, f.so)
		}, wantErrorAs[*SORoleRequiredError]},
		{"admin with a bound role assigns SO", func() error { return f.mw.AuthorizeRoleAssignment(admin, acmeUser(), f.roles, 7, f.so) }, wantErrorAs[*SORoleRequiredError]},
		{"admin with a bound role guard revokes SO", func() error {
			return f.mw.SORoleGuard(admin)(acmeUser(f.so).u, acmeUser().u)
		}, wantErrorAs[*SORoleRequiredError]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, tt.call())
		})
	}
}

func TestHeldRoleIDs(t *testing.T) {
	bindings := []user.RoleBinding{{RoleID: "b-acme", TenantID: "acme"}, {RoleID: "b-globex", TenantID: "globex"}}
	tests := []struct {
		name     string
		target   string
		roleIDs  []string
		bindings []user.RoleBinding
		want     []string
	}{
		{"roles and target-scoped bindings", "acme", []string{"r"}, bindings, []string{"r", "b-acme"}},
		{"another target", "globex", nil, bindings, []string{"b-globex"}},
		{"no bindings", "acme", []string{"r"}, nil, []string{"r"}},
		{"nothing", "acme", nil, nil, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := heldRoleIDs(tt.target, tt.roleIDs, tt.bindings); !slices.Equal(got, tt.want) {
				t.Fatalf("heldRoleIDs = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCallerRoleIDs(t *testing.T) {
	identity := &auth.Identity{
		Roles:        []string{"r"},
		RoleBindings: []auth.RoleBinding{{RoleID: "b-acme", TenantID: "acme"}, {RoleID: "b-globex", TenantID: "globex"}},
	}
	if got, want := callerRoleIDs(identity, "acme"), []string{"r", "b-acme"}; !slices.Equal(got, want) {
		t.Fatalf("callerRoleIDs(acme) = %v, want %v", got, want)
	}
	if got, want := callerRoleIDs(&auth.Identity{Roles: []string{"r"}}, "acme"), []string{"r"}; !slices.Equal(got, want) {
		t.Fatalf("callerRoleIDs without bindings = %v, want %v", got, want)
	}
}
