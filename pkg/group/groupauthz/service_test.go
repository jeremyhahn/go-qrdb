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

package groupauthz

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/group"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

type fixture struct {
	svc   *Service
	users user.UserServicer
	roles role.RoleServicer
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	roles, err := role.NewStore(kvstore.NewMemoryKVStore())
	if err != nil {
		t.Fatalf("role store: %v", err)
	}
	gen, err := dao.NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatalf("id gen: %v", err)
	}
	users, err := user.NewStore(kvstore.NewMemoryKVStore(), gen)
	if err != nil {
		t.Fatalf("user store: %v", err)
	}
	members, err := user.NewGroupMembership(users)
	if err != nil {
		t.Fatalf("membership: %v", err)
	}
	groups, err := group.NewStore(kvstore.NewMemoryKVStore(), group.WithRoleLookup(roles), group.WithMemberRevoker(members))
	if err != nil {
		t.Fatalf("group store: %v", err)
	}
	svc := New(groups, users, roles, nil)
	if svc == nil {
		t.Fatal("New returned nil with every store configured")
	}
	return &fixture{svc: svc, users: users, roles: roles}
}

func TestGroupService_NewNilWithoutStores(t *testing.T) {
	if New(nil, nil, nil, nil) != nil {
		t.Fatal("New(nil...) != nil")
	}
}

func TestGroupService_LifecycleAndMembership(t *testing.T) {
	ctx := tenant.WithTenantID(context.Background(), "acme")
	f := newFixture(t)
	r, err := f.roles.CreateRole(ctx, &role.CreateRoleRequest{Name: "readers", TenantID: "acme", Permissions: []permission.Permission{permission.RolesRead}})
	if err != nil {
		t.Fatalf("create role: %v", err)
	}
	g, err := f.svc.Create(ctx, &group.CreateGroupRequest{TenantID: "acme", Name: "eng", RoleIDs: []string{r.ID}})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if g.TenantID != "acme" || !slices.Equal(g.RoleIDs, []string{r.ID}) {
		t.Fatalf("group = %+v", g)
	}
	var taken *group.GroupNameTakenError
	if _, err := f.svc.Create(ctx, &group.CreateGroupRequest{TenantID: "acme", Name: "eng"}); !errors.As(err, &taken) {
		t.Fatalf("duplicate name: %v, want *GroupNameTakenError", err)
	}
	if got, err := f.svc.GetByName(ctx, "acme", "eng"); err != nil || got.ID != g.ID {
		t.Fatalf("get by name = %+v, %v", got, err)
	}
	list, err := f.svc.List(ctx, &group.ListGroupsRequest{TenantID: "acme"})
	if err != nil || list.Total != 1 {
		t.Fatalf("list = %+v, %v", list, err)
	}

	alice, err := f.users.CreateUser(ctx, &user.CreateUserRequest{WithoutEmailLoginKey: true, Username: "alice", TenantID: "acme"})
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	eve, err := f.users.CreateUser(ctx, &user.CreateUserRequest{WithoutEmailLoginKey: true, Username: "eve", TenantID: "other"})
	if err != nil {
		t.Fatalf("create eve: %v", err)
	}
	if err := f.svc.AddMember(ctx, alice.ID, g.ID); err != nil {
		t.Fatalf("add alice: %v", err)
	}
	var nf *group.GroupNotFoundError
	if err := f.svc.AddMember(ctx, eve.ID, g.ID); !errors.As(err, &nf) {
		t.Fatalf("add other-tenant user: %v, want *GroupNotFoundError", err)
	}
	ms, err := f.svc.ListMembers(ctx, g.ID)
	if err != nil || len(ms) != 1 || ms[0].UserID != alice.ID || ms[0].TenantID != "acme" {
		t.Fatalf("members = %+v, %v", ms, err)
	}

	desc := "engineering"
	up, err := f.svc.Update(ctx, &group.UpdateGroupRequest{ID: g.ID, Description: &desc, RoleIDs: []string{}})
	if err != nil || up.Description != desc || len(up.RoleIDs) != 0 {
		t.Fatalf("update = %+v, %v", up, err)
	}

	if err := f.svc.RemoveMember(ctx, alice.ID, g.ID); err != nil {
		t.Fatalf("remove alice: %v", err)
	}
	if err := f.svc.AddMember(ctx, alice.ID, g.ID); err != nil {
		t.Fatalf("re-add alice: %v", err)
	}
	if err := f.svc.Delete(ctx, g.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	u, err := f.users.GetUser(ctx, alice.ID)
	if err != nil || len(u.GroupIDs) != 0 {
		t.Fatalf("alice after delete: GroupIDs=%v err=%v", u.GroupIDs, err)
	}
	if _, err := f.svc.Get(ctx, g.ID); !errors.As(err, &nf) {
		t.Fatalf("get deleted: %v, want *GroupNotFoundError", err)
	}
}

// Dropping a role from a group, or deleting the group, unassigns the role
// from every member: under strict separation a non-SO caller may not remove
// the so role that way, and the SO may.
func TestGroupService_RoleRemovalSOGate(t *testing.T) {
	roles := authtest.BuiltInRoles(t, "acme")
	gen, err := dao.NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatalf("id gen: %v", err)
	}
	users, err := user.NewStore(kvstore.NewMemoryKVStore(), gen)
	if err != nil {
		t.Fatalf("user store: %v", err)
	}
	members, err := user.NewGroupMembership(users)
	if err != nil {
		t.Fatalf("membership: %v", err)
	}
	groups, err := group.NewStore(kvstore.NewMemoryKVStore(), group.WithRoleLookup(roles), group.WithMemberRevoker(members))
	if err != nil {
		t.Fatalf("group store: %v", err)
	}
	mw := middleware.New(rbac.New(roles), middleware.WithRoleResolver(roles), middleware.WithStrictSeparation(true))
	svc := New(groups, users, roles, mw)
	admin, so := role.SystemRoleID("acme", role.RoleNameAdmin), role.SystemRoleID("acme", role.RoleNameSO)
	caller := func(roleIDs ...string) context.Context {
		ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "acme-caller", TenantID: "acme", Roles: roleIDs})
		return tenant.WithTenantID(ctx, "acme")
	}
	// groups:delete is SO-exclusive among the built-in roles; a tenant role
	// carrying it puts a non-SO caller in front of the role-change gate.
	keeper := authtest.CustomRole(t, roles, "acme", "group-keeper", permission.GroupsRead, permission.GroupsUpdate, permission.GroupsDelete)
	asAdmin, asSO := caller(admin, keeper), caller(admin, so)
	g, err := groups.CreateGroup(asSO, &group.CreateGroupRequest{TenantID: "acme", Name: "signers", RoleIDs: []string{so}})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}

	var refused *middleware.SORoleRequiredError
	if _, err := svc.Update(asAdmin, &group.UpdateGroupRequest{ID: g.ID, RoleIDs: []string{}}); !errors.As(err, &refused) {
		t.Fatalf("admin drops so from the group: %v, want *SORoleRequiredError", err)
	}
	if err := svc.Delete(asAdmin, g.ID); !errors.As(err, &refused) {
		t.Fatalf("admin deletes a group carrying so: %v, want *SORoleRequiredError", err)
	}
	if got, err := svc.Get(asSO, g.ID); err != nil || !slices.Equal(got.RoleIDs, []string{so}) {
		t.Fatalf("group after refusals = %+v, %v", got, err)
	}
	desc := "kept"
	if _, err := svc.Update(asAdmin, &group.UpdateGroupRequest{ID: g.ID, Description: &desc}); err != nil {
		t.Fatalf("admin update leaving roles: %v", err)
	}
	if _, err := svc.Update(asSO, &group.UpdateGroupRequest{ID: g.ID, RoleIDs: []string{}}); err != nil {
		t.Fatalf("SO drops so from the group: %v", err)
	}
	if err := svc.Delete(asAdmin, g.ID); err != nil {
		t.Fatalf("admin deletes a group with no so: %v", err)
	}
}

// racingGroups commits race once, right before the first UpdateGroup it
// forwards: a writer that lands between Service.Update's permission read
// and the store's commit.
type racingGroups struct {
	group.GroupServicer
	race       func()
	claimRace  func()
	claimAfter func()
}

func (r *racingGroups) UpdateGroup(ctx context.Context, req *group.UpdateGroupRequest) (*group.Group, error) {
	if r.race != nil {
		race := r.race
		r.race = nil
		race()
	}
	return r.GroupServicer.UpdateGroup(ctx, req)
}

// ClaimGroup commits claimRace once right before the first ClaimGroup it
// forwards (a writer landing between the permission read and the claim),
// and claimAfter once right after a claim is taken (a writer landing
// between the claim and the operation it guards).
func (r *racingGroups) ClaimGroup(ctx context.Context, id, op string, guard func(*group.Group) error) (*group.Group, string, error) {
	if r.claimRace != nil {
		race := r.claimRace
		r.claimRace = nil
		race()
	}
	g, token, err := r.GroupServicer.ClaimGroup(ctx, id, op, guard)
	if err == nil && r.claimAfter != nil {
		after := r.claimAfter
		r.claimAfter = nil
		after()
	}
	return g, token, err
}

// strictFixture is a groupauthz Service over acme's built-in roles with
// strict separation of duties, whose group store races through racing.
type strictFixture struct {
	svc     *Service
	groups  group.GroupServicer
	racing  *racingGroups
	users   user.UserServicer
	members *user.GroupMembership
	roles   role.RoleServicer
}

func newStrictFixture(t *testing.T) *strictFixture {
	t.Helper()
	roles := authtest.BuiltInRoles(t, "acme")
	gen, err := dao.NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatalf("id gen: %v", err)
	}
	users, err := user.NewStore(kvstore.NewMemoryKVStore(), gen)
	if err != nil {
		t.Fatalf("user store: %v", err)
	}
	members, err := user.NewGroupMembership(users)
	if err != nil {
		t.Fatalf("membership: %v", err)
	}
	groups, err := group.NewStore(kvstore.NewMemoryKVStore(), group.WithRoleLookup(roles), group.WithMemberRevoker(members))
	if err != nil {
		t.Fatalf("group store: %v", err)
	}
	mw := middleware.New(rbac.New(roles), middleware.WithRoleResolver(roles), middleware.WithStrictSeparation(true))
	racing := &racingGroups{GroupServicer: groups}
	return &strictFixture{svc: New(racing, users, roles, mw), groups: groups, racing: racing, users: users, members: members, roles: roles}
}

// acmeCaller returns an acme request context for an identity with the
// given user ID and roles (no group snapshot).
func acmeCaller(userID uint64, roleIDs ...string) context.Context {
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "acme-caller", TenantID: "acme", UserID: userID, Roles: roleIDs})
	return tenant.WithTenantID(ctx, "acme")
}

// TestGroupService_DeleteJudgedAgainstClaimedRecord pins that a delete is
// judged on the record it removes: an Admin with groups:delete deleting a
// role-less group while an SO adds the SO role is refused (or conflicts),
// and the group, its SO role and its member's membership survive.
func TestGroupService_DeleteJudgedAgainstClaimedRecord(t *testing.T) {
	f := newStrictFixture(t)
	admin, so := role.SystemRoleID("acme", role.RoleNameAdmin), role.SystemRoleID("acme", role.RoleNameSO)
	deleter := authtest.CustomRole(t, f.roles, "acme", "group-deleter", permission.GroupsRead, permission.GroupsDelete)
	asAdmin, asSO := acmeCaller(0, admin, deleter), acmeCaller(0, admin, so)
	g, err := f.groups.CreateGroup(asSO, &group.CreateGroupRequest{TenantID: "acme", Name: "signers"})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	signer, err := f.users.CreateUser(asSO, &user.CreateUserRequest{WithoutEmailLoginKey: true, Username: "signer", TenantID: "acme"})
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	if _, err := f.members.AddMember(asSO, signer.ID, g.ID); err != nil {
		t.Fatalf("add signer: %v", err)
	}
	f.racing.claimRace = func() {
		if _, err := f.groups.UpdateGroup(asSO, &group.UpdateGroupRequest{ID: g.ID, RoleIDs: []string{so}}); err != nil {
			t.Fatalf("racing SO add: %v", err)
		}
	}
	var refused *middleware.SORoleRequiredError
	var conflict *group.GroupConflictError
	if err := f.svc.Delete(asAdmin, g.ID); !errors.As(err, &refused) && !errors.As(err, &conflict) {
		t.Fatalf("admin delete racing an SO add: %v, want *SORoleRequiredError or *GroupConflictError", err)
	}
	if got, err := f.groups.GetGroup(asSO, g.ID); err != nil || !slices.Equal(got.RoleIDs, []string{so}) {
		t.Fatalf("group after the race = %+v, %v; want it kept with SO", got, err)
	}
	if u, err := f.users.GetUser(asSO, signer.ID); err != nil || !slices.Contains(u.GroupIDs, g.ID) {
		t.Fatalf("signer after the race = %+v, %v; want the membership kept", u, err)
	}
	// The refused delete left no claim behind: the SO can still update.
	if _, err := f.groups.UpdateGroup(asSO, &group.UpdateGroupRequest{ID: g.ID, RoleIDs: []string{so}}); err != nil {
		t.Fatalf("update after the refused delete: %v", err)
	}
}

// TestGroupService_UpdateJudgedAgainstCommittedRecord pins that the role
// guards judge the record the update commits over: an Admin's update that
// clears the role set, racing an SO adding the SO role, is refused (the
// removal now includes SO) and never strips SO unchecked.
func TestGroupService_UpdateJudgedAgainstCommittedRecord(t *testing.T) {
	roles := authtest.BuiltInRoles(t, "acme")
	gen, err := dao.NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatalf("id gen: %v", err)
	}
	users, err := user.NewStore(kvstore.NewMemoryKVStore(), gen)
	if err != nil {
		t.Fatalf("user store: %v", err)
	}
	members, err := user.NewGroupMembership(users)
	if err != nil {
		t.Fatalf("membership: %v", err)
	}
	groups, err := group.NewStore(kvstore.NewMemoryKVStore(), group.WithRoleLookup(roles), group.WithMemberRevoker(members))
	if err != nil {
		t.Fatalf("group store: %v", err)
	}
	mw := middleware.New(rbac.New(roles), middleware.WithRoleResolver(roles), middleware.WithStrictSeparation(true))
	racing := &racingGroups{GroupServicer: groups}
	svc := New(racing, users, roles, mw)
	admin, so := role.SystemRoleID("acme", role.RoleNameAdmin), role.SystemRoleID("acme", role.RoleNameSO)
	caller := func(roleIDs ...string) context.Context {
		ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "acme-caller", TenantID: "acme", Roles: roleIDs})
		return tenant.WithTenantID(ctx, "acme")
	}
	keeper := authtest.CustomRole(t, roles, "acme", "group-keeper", permission.GroupsRead, permission.GroupsUpdate)
	asAdmin, asSO := caller(admin, keeper), caller(admin, so)
	g, err := groups.CreateGroup(asSO, &group.CreateGroupRequest{TenantID: "acme", Name: "signers"})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	racing.race = func() {
		if _, err := groups.UpdateGroup(asSO, &group.UpdateGroupRequest{ID: g.ID, RoleIDs: []string{so}}); err != nil {
			t.Fatalf("racing SO add: %v", err)
		}
	}
	var refused *middleware.SORoleRequiredError
	var conflict *group.GroupConflictError
	_, err = svc.Update(asAdmin, &group.UpdateGroupRequest{ID: g.ID, RoleIDs: []string{}})
	if !errors.As(err, &refused) && !errors.As(err, &conflict) {
		t.Fatalf("admin clear racing an SO add: %v, want *SORoleRequiredError or *GroupConflictError", err)
	}
	if got, err := groups.GetGroup(asSO, g.ID); err != nil || !slices.Equal(got.RoleIDs, []string{so}) {
		t.Fatalf("group after the race = %+v, %v; want SO kept", got, err)
	}
}
