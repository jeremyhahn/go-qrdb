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
	"errors"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/group"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// mapGroupResolver resolves groups from a map; a missing ID is
// *group.GroupNotFoundError, as from the store. It records the tenant the
// lookup ran under.
type mapGroupResolver struct {
	groups  map[string]*group.Group
	tenants []string
}

func (r *mapGroupResolver) GetGroup(ctx context.Context, id string) (*group.Group, error) {
	r.tenants = append(r.tenants, tenant.FromContext(ctx))
	g, ok := r.groups[id]
	if !ok {
		return nil, &group.GroupNotFoundError{ID: id}
	}
	return g, nil
}

func TestAuthenticate_GroupRolesUnionAtBinding(t *testing.T) {
	operator := role.SystemRoleID(claimRolesTenant, role.RoleNameOperator)
	auditor := role.SystemRoleID(claimRolesTenant, role.RoleNameAuditor)
	resolver := &mapGroupResolver{groups: map[string]*group.Group{
		"g-ops":     {ID: "g-ops", TenantID: claimRolesTenant, RoleIDs: []string{operator, auditor}},
		"g-foreign": {ID: "g-foreign", TenantID: "other", RoleIDs: []string{"r-foreign"}},
	}}
	cases := []struct {
		name     string
		groups   []string
		resolver GroupResolver
		want     []string
	}{
		{name: "member gains the group's roles", groups: []string{"g-ops"}, resolver: resolver, want: []string{auditor, operator}},
		{name: "missing group grants nothing", groups: []string{"g-gone"}, resolver: resolver, want: []string{operator}},
		{name: "other tenant's group is ignored", groups: []string{"g-foreign"}, resolver: resolver, want: []string{operator}},
		{name: "no resolver gives record roles only", groups: []string{"g-ops"}, want: []string{operator}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := carolRecord(operator)
			rec.GroupIDs = tc.groups
			opts := []Option{
				WithAuthenticator(&mockAuthenticator{name: "test-authn", identity: claimedAdmin()}),
				WithUserService(&mockUserServicer{user: rec}),
			}
			if tc.resolver != nil {
				opts = append(opts, WithGroupResolver(tc.resolver))
			}
			m := newTestMiddleware(&mockAuthorizer{}, opts...)
			_, id, err := m.authenticate(context.Background())
			if err != nil {
				t.Fatalf("authenticate: %v", err)
			}
			if !slices.Equal(id.Roles, tc.want) {
				t.Fatalf("Roles = %v, want %v", id.Roles, tc.want)
			}
			if !slices.Equal(id.Groups, tc.groups) {
				t.Fatalf("Groups = %v, want %v", id.Groups, tc.groups)
			}
		})
	}
	for _, tid := range resolver.tenants {
		if tid != claimRolesTenant {
			t.Fatalf("group lookup ran under tenant %q, want the record's tenant %q", tid, claimRolesTenant)
		}
	}
}

func TestAuthorizeGroupMembership_Guard(t *testing.T) {
	_, custom, roles := grantRuleFixture()
	member := &userTenantFixture{u: &user.User{ID: 7, TenantID: "acme"}}
	carrying := &group.Group{ID: "g-signers", TenantID: "acme", RoleIDs: []string{custom}}

	t.Run("group carrying a role beyond the caller's holdings", func(t *testing.T) {
		mw := mwWithPerms(permission.GroupsAddMember)
		var exceeds *role.GrantExceedsHolderError
		if err := mw.AuthorizeGroupMembership(selfCtx(), member, roles, 7, carrying); !errors.As(err, &exceeds) {
			t.Fatalf("err = %v, want *role.GrantExceedsHolderError", err)
		}
	})
	t.Run("without groups:add_member", func(t *testing.T) {
		mw := mwWithPerms(permission.KeysSign)
		empty := &group.Group{ID: "g-empty", TenantID: "acme"}
		if err := mw.AuthorizeGroupMembership(selfCtx(), member, roles, 7, empty); err == nil {
			t.Fatal("add member allowed without groups:add_member")
		}
	})
	t.Run("group of another tenant", func(t *testing.T) {
		mw := mwWithPerms(permission.GroupsAddMember)
		foreign := &group.Group{ID: "g-foreign", TenantID: "other"}
		var nf *group.GroupNotFoundError
		if err := mw.AuthorizeGroupMembership(selfCtx(), member, roles, 7, foreign); !errors.As(err, &nf) {
			t.Fatalf("err = %v, want *group.GroupNotFoundError", err)
		}
	})
	t.Run("group with no roles", func(t *testing.T) {
		mw := mwWithPerms(permission.GroupsAddMember)
		empty := &group.Group{ID: "g-empty", TenantID: "acme"}
		if err := mw.AuthorizeGroupMembership(selfCtx(), member, roles, 7, empty); err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
	})
}

// Removing roles from a group, or deleting it, unassigns those roles from
// every member: under strict separation only the SO may remove so that way.
func TestAuthorizeGroupRoleRemoval_SOGate(t *testing.T) {
	f := newSORoleFixture(t)
	carrying := &group.Group{ID: "g-so", TenantID: "acme", RoleIDs: []string{f.so, f.userRole}}
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		removed []string
		check   func(t *testing.T, err error)
	}{
		{"admin removes so", f.caller(f.admin), []string{f.so}, wantErrorAs[*SORoleRequiredError]},
		{"admin deletes a group carrying so", f.caller(f.admin), carrying.RoleIDs, wantErrorAs[*SORoleRequiredError]},
		{"SO removes so", f.caller(f.so), []string{f.so}, wantNoError},
		{"admin removes a non-so role", f.caller(f.admin), []string{f.userRole}, wantNoError},
		{"nothing removed", f.caller(f.admin), nil, wantNoError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.check(t, f.mw.AuthorizeGroupRoleRemoval(tc.ctx, carrying, tc.removed))
		})
	}
}

func TestAuthorizeGroupRoles_Guard(t *testing.T) {
	builtIn, custom, roles := grantRuleFixture()
	mw := mwWithPerms(permission.GroupsUpdate, permission.KeysSign)
	var exceeds *role.GrantExceedsHolderError
	mwNarrow := mwWithPerms(permission.GroupsUpdate)
	if err := mwNarrow.AuthorizeGroupRoles(selfCtx(), nil, roles, "acme", "", []string{custom}); !errors.As(err, &exceeds) {
		t.Fatalf("role beyond holdings on a group: err = %v, want *role.GrantExceedsHolderError", err)
	}
	if err := mw.AuthorizeGroupRoles(selfCtx(), nil, roles, "acme", "", []string{builtIn}); err != nil {
		t.Fatalf("built-in role on a group: err = %v, want nil", err)
	}
	if err := mw.AuthorizeGroupRoles(selfCtx(), nil, roles, "acme", "", []string{"r-missing"}); err == nil {
		t.Fatal("unknown role on a group allowed")
	}
}

// errGroupResolver fails every lookup with err.
type errGroupResolver struct{ err error }

func (r errGroupResolver) GetGroup(context.Context, string) (*group.Group, error) {
	return nil, r.err
}

// TestAuthenticate_GroupLookupFailsClosed pins that a group the store could
// not read fails authentication instead of silently dropping the group's
// roles (and with them a strict-separation deny-list), while a group that
// does not exist still contributes nothing.
func TestAuthenticate_GroupLookupFailsClosed(t *testing.T) {
	operator := role.SystemRoleID(claimRolesTenant, role.RoleNameOperator)
	authn := func(r GroupResolver) *AuthMiddleware {
		rec := carolRecord(operator)
		rec.GroupIDs = []string{"g-so"}
		return newTestMiddleware(&mockAuthorizer{},
			WithAuthenticator(&mockAuthenticator{name: "test-authn", identity: claimedAdmin()}),
			WithUserService(&mockUserServicer{user: rec}),
			WithGroupResolver(r))
	}
	t.Run("store error", func(t *testing.T) {
		_, _, err := authn(errGroupResolver{err: errors.New("raft: no leader")}).authenticate(context.Background())
		var su *StoreUnavailableError
		if !errors.As(err, &su) {
			t.Fatalf("err = %v, want *StoreUnavailableError", err)
		}
	})
	t.Run("group not found", func(t *testing.T) {
		_, id, err := authn(errGroupResolver{err: &group.GroupNotFoundError{ID: "g-so"}}).authenticate(context.Background())
		if err != nil {
			t.Fatalf("authenticate: %v", err)
		}
		if !slices.Equal(id.Roles, []string{operator}) {
			t.Fatalf("Roles = %v, want the direct roles %v", id.Roles, []string{operator})
		}
	})
}

// TestAuthorizeGroupRoles_SelfGrantThroughGroup pins the three-step
// escalation: an Admin creates an empty group, joins it, then puts the
// built-in User role (keys:sign) on it. Because the caller is a member, the
// update is judged as self-assignment and refused exactly as direct
// self-assignment of the same role is.
func TestAuthorizeGroupRoles_SelfGrantThroughGroup(t *testing.T) {
	builtIn, _, roles := grantRuleFixture()
	mw := mwWithPerms(permission.GroupsUpdate)
	id := &auth.Identity{Subject: auth.UserSubject(1), UserID: 1, TenantID: "acme", Roles: []string{"admin"}, Groups: []string{"g-mine"}}
	ctx := tenant.WithTenantID(auth.WithIdentity(context.Background(), id), "acme")

	direct := role.ValidateSelfAssignable(ctx, roles, mw.Authorizer(), builtIn, "acme")
	var exceeds *role.GrantExceedsHolderError
	if !errors.As(direct, &exceeds) {
		t.Fatalf("direct self-assignment: err = %v, want *role.GrantExceedsHolderError", direct)
	}
	err := mw.AuthorizeGroupRoles(ctx, nil, roles, "acme", "g-mine", []string{builtIn})
	if !errors.As(err, &exceeds) || err.Error() != direct.Error() {
		t.Fatalf("built-in role on the caller's own group: err = %v, want %v", err, direct)
	}
	if err := mw.AuthorizeGroupRoles(ctx, nil, roles, "acme", "g-other", []string{builtIn}); err != nil {
		t.Fatalf("built-in role on a group the caller is not in: err = %v, want nil", err)
	}
}

// recordUsers is a user service whose GetUser returns fixed records; every
// other method is unused by AuthorizeGroupRoles.
type recordUsers struct {
	user.UserServicer
	records map[uint64]*user.User
	err     error
}

func (r *recordUsers) GetUser(_ context.Context, id uint64) (*user.User, error) {
	if r.err != nil {
		return nil, r.err
	}
	u, ok := r.records[id]
	if !ok {
		return nil, &user.UserNotFoundError{ID: id}
	}
	return u, nil
}

// TestAuthorizeGroupRoles_SelfGrantJudgedFromTheCurrentRecord pins that a
// membership committed after authentication counts: the identity snapshot
// does not list the group, but the caller's current user record does, so a
// role put on the group is judged as a self-assignment and refused exactly
// as for a snapshot member. A lookup failure other than not-found fails
// closed.
func TestAuthorizeGroupRoles_SelfGrantJudgedFromTheCurrentRecord(t *testing.T) {
	builtIn, _, roles := grantRuleFixture()
	mw := mwWithPerms(permission.GroupsUpdate)
	id := &auth.Identity{Subject: auth.UserSubject(1), UserID: 1, TenantID: "acme", Roles: []string{"admin"}}
	ctx := tenant.WithTenantID(auth.WithIdentity(context.Background(), id), "acme")
	users := &recordUsers{records: map[uint64]*user.User{1: {ID: 1, TenantID: "acme", GroupIDs: []string{"g-mine"}}}}

	direct := role.ValidateSelfAssignable(ctx, roles, mw.Authorizer(), builtIn, "acme")
	var exceeds *role.GrantExceedsHolderError
	if !errors.As(direct, &exceeds) {
		t.Fatalf("direct self-assignment: err = %v, want *role.GrantExceedsHolderError", direct)
	}
	err := mw.AuthorizeGroupRoles(ctx, users, roles, "acme", "g-mine", []string{builtIn})
	if !errors.As(err, &exceeds) || err.Error() != direct.Error() {
		t.Fatalf("built-in role on a group the current record lists: err = %v, want %v", err, direct)
	}
	if err := mw.AuthorizeGroupRoles(ctx, users, roles, "acme", "g-other", []string{builtIn}); err != nil {
		t.Fatalf("built-in role on a group the caller is not in: err = %v, want nil", err)
	}
	gone := &recordUsers{records: map[uint64]*user.User{}}
	if err := mw.AuthorizeGroupRoles(ctx, gone, roles, "acme", "g-mine", []string{builtIn}); err != nil {
		t.Fatalf("caller without a user record: err = %v, want nil", err)
	}
	broken := errors.New("store down")
	if err := mw.AuthorizeGroupRoles(ctx, &recordUsers{err: broken}, roles, "acme", "g-mine", []string{builtIn}); !errors.Is(err, broken) {
		t.Fatalf("user lookup failure: err = %v, want it returned", err)
	}
}
