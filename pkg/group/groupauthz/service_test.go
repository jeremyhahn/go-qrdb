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
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
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
	clock   *clocktest.Fake
	svc     *Service
	groups  group.GroupServicer
	racing  *racingGroups
	users   user.UserServicer
	members *user.GroupMembership
	roles   role.RoleServicer
	revoker *flakyRevoker
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
	fake := clocktest.New()
	revoker := &flakyRevoker{MemberRevoker: members}
	groups, err := group.NewStore(kvstore.NewMemoryKVStore(), group.WithRoleLookup(roles), group.WithMemberRevoker(revoker), group.WithClock(fake))
	if err != nil {
		t.Fatalf("group store: %v", err)
	}
	mw := middleware.New(rbac.New(roles), middleware.WithRoleResolver(roles), middleware.WithStrictSeparation(true))
	racing := &racingGroups{GroupServicer: groups}
	return &strictFixture{svc: New(racing, users, roles, mw), groups: groups, racing: racing, users: users, members: members, roles: roles, clock: fake, revoker: revoker}
}

// flakyRevoker revokes through the real membership store and then fails the
// next fail calls, modelling a delete interrupted after a partial revoke.
type flakyRevoker struct {
	group.MemberRevoker
	fail int
}

func (r *flakyRevoker) RevokeGroup(ctx context.Context, tenantID, groupID string) (int, error) {
	n, err := r.MemberRevoker.RevokeGroup(ctx, tenantID, groupID)
	if err == nil && r.fail > 0 {
		r.fail--
		return n, errors.New("revoke interrupted")
	}
	return n, err
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

// TestGroupService_DeleteRetriesAfterAnInterruptedDelete pins that a delete
// interrupted after its claim is retryable: a retry by a caller without the
// role-removal authority is refused, and an authorized retry removes the
// group and every membership naming it.
func TestGroupService_DeleteRetriesAfterAnInterruptedDelete(t *testing.T) {
	f := newStrictFixture(t)
	admin, so := role.SystemRoleID("acme", role.RoleNameAdmin), role.SystemRoleID("acme", role.RoleNameSO)
	deleter := authtest.CustomRole(t, f.roles, "acme", "group-deleter", permission.GroupsRead, permission.GroupsDelete)
	asAdmin, asSO := acmeCaller(0, admin, deleter), acmeCaller(0, admin, so, deleter)
	g, err := f.groups.CreateGroup(asSO, &group.CreateGroupRequest{TenantID: "acme", Name: "signers", RoleIDs: []string{so}})
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
	f.revoker.fail = 1
	if err := f.svc.Delete(asSO, g.ID); err == nil {
		t.Fatal("delete succeeded with an interrupted revoke")
	}
	var refused *middleware.SORoleRequiredError
	if err := f.svc.Delete(asAdmin, g.ID); !errors.As(err, &refused) {
		t.Fatalf("admin retry of an SO group delete: %v, want *SORoleRequiredError", err)
	}
	if err := f.svc.Delete(asSO, g.ID); err != nil {
		t.Fatalf("retry of the interrupted delete: %v", err)
	}
	var notFound *group.GroupNotFoundError
	if _, err := f.groups.GetGroup(asSO, g.ID); !errors.As(err, &notFound) {
		t.Fatalf("get after the retried delete: %v, want *GroupNotFoundError", err)
	}
	if u, err := f.users.GetUser(asSO, signer.ID); err != nil || slices.Contains(u.GroupIDs, g.ID) {
		t.Fatalf("signer after the retried delete = %+v, %v; want no membership", u, err)
	}
}

// TestGroupService_JoinRacingARoleAddIsNotASelfGrant pins the join/role-add
// race: an Admin joining an empty group while adding the built-in User role
// (keys:sign, beyond the Admin) to it must not end holding User. The role
// add racing the join conflicts on the join's member claim, and a role add
// after the join is judged as a self-grant from the caller's current user
// record, though its identity snapshot does not list the group.
func TestGroupService_JoinRacingARoleAddIsNotASelfGrant(t *testing.T) {
	f := newStrictFixture(t)
	admin, so := role.SystemRoleID("acme", role.RoleNameAdmin), role.SystemRoleID("acme", role.RoleNameSO)
	userRole := role.SystemRoleID("acme", role.RoleNameUser)
	keeper := authtest.CustomRole(t, f.roles, "acme", "group-keeper", permission.GroupsRead, permission.GroupsUpdate)
	asSO := acmeCaller(0, admin, so)
	g, err := f.groups.CreateGroup(asSO, &group.CreateGroupRequest{TenantID: "acme", Name: "crypto"})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	self, err := f.users.CreateUser(asSO, &user.CreateUserRequest{WithoutEmailLoginKey: true, Username: "acme-admin", TenantID: "acme"})
	if err != nil {
		t.Fatalf("create admin user: %v", err)
	}
	asAdmin := acmeCaller(self.ID, admin, keeper)
	var conflict *group.GroupConflictError
	raced := false
	f.racing.claimAfter = func() {
		raced = true
		if _, err := f.svc.Update(asAdmin, &group.UpdateGroupRequest{ID: g.ID, RoleIDs: []string{userRole}}); !errors.As(err, &conflict) {
			t.Errorf("role add racing the join: %v, want *GroupConflictError", err)
		}
	}
	if err := f.svc.AddMember(asAdmin, self.ID, g.ID); err != nil {
		t.Fatalf("self-join of the empty group: %v", err)
	}
	if !raced {
		t.Fatal("the role add never raced the join")
	}
	var exceeds *role.GrantExceedsHolderError
	if _, err := f.svc.Update(asAdmin, &group.UpdateGroupRequest{ID: g.ID, RoleIDs: []string{userRole}}); !errors.As(err, &exceeds) {
		t.Fatalf("role add after the join: %v, want *role.GrantExceedsHolderError (a self-grant)", err)
	}
	got, err := f.groups.GetGroup(asSO, g.ID)
	if err != nil || len(got.RoleIDs) != 0 {
		t.Fatalf("group after the race = %+v, %v; want no roles", got, err)
	}
	if u, err := f.users.GetUser(asSO, self.ID); err != nil || !slices.Contains(u.GroupIDs, g.ID) {
		t.Fatalf("admin after the join = %+v, %v; want the membership", u, err)
	}
}

func TestGroupService_JoinAfterALapsedClaimIsUndone(t *testing.T) {
	f := newStrictFixture(t)
	admin, so := role.SystemRoleID("acme", role.RoleNameAdmin), role.SystemRoleID("acme", role.RoleNameSO)
	userRole := role.SystemRoleID("acme", role.RoleNameUser)
	keeper := authtest.CustomRole(t, f.roles, "acme", "group-keeper", permission.GroupsRead, permission.GroupsUpdate)
	asSO := acmeCaller(0, admin, so)
	g, err := f.groups.CreateGroup(asSO, &group.CreateGroupRequest{TenantID: "acme", Name: "crypto"})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	self, err := f.users.CreateUser(asSO, &user.CreateUserRequest{WithoutEmailLoginKey: true, Username: "acme-admin", TenantID: "acme"})
	if err != nil {
		t.Fatalf("create admin user: %v", err)
	}
	asAdmin := acmeCaller(self.ID, admin, keeper)
	raced := false
	f.racing.claimAfter = func() {
		raced = true
		f.clock.Advance(31 * time.Second)
		if _, err := f.svc.Update(asAdmin, &group.UpdateGroupRequest{ID: g.ID, RoleIDs: []string{userRole}}); err != nil {
			t.Errorf("role add over the lapsed claim: %v", err)
		}
	}
	var conflict *group.GroupConflictError
	if err := f.svc.AddMember(asAdmin, self.ID, g.ID); !errors.As(err, &conflict) {
		t.Fatalf("join after its claim lapsed: %v, want *GroupConflictError", err)
	}
	if !raced {
		t.Fatal("the role add never ran after the claim")
	}
	if u, err := f.users.GetUser(asSO, self.ID); err != nil || slices.Contains(u.GroupIDs, g.ID) {
		t.Fatalf("admin after the undone join = %+v, %v; want no membership", u, err)
	}
	_, token, err := f.groups.ClaimGroup(asSO, g.ID, group.ClaimMember, nil)
	if err != nil {
		t.Fatalf("claim after the undone join: %v, want no claim left", err)
	}
	if err := f.groups.ReleaseClaim(asSO, g.ID, token); err != nil {
		t.Fatalf("release: %v", err)
	}
}

func TestGroupService_RemovalAfterALapsedClaimIsUndone(t *testing.T) {
	f := newStrictFixture(t)
	admin, so := role.SystemRoleID("acme", role.RoleNameAdmin), role.SystemRoleID("acme", role.RoleNameSO)
	keeper := authtest.CustomRole(t, f.roles, "acme", "group-keeper", permission.GroupsRead, permission.GroupsUpdate)
	asSO := acmeCaller(0, admin, so)
	g, err := f.groups.CreateGroup(asSO, &group.CreateGroupRequest{TenantID: "acme", Name: "crypto"})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	alice, err := f.users.CreateUser(asSO, &user.CreateUserRequest{WithoutEmailLoginKey: true, Username: "alice", TenantID: "acme"})
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	if _, err := f.members.AddMember(asSO, alice.ID, g.ID); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	asAdmin := acmeCaller(0, admin, keeper)
	raced := false
	f.racing.claimAfter = func() {
		raced = true
		f.clock.Advance(31 * time.Second)
		if _, err := f.svc.Update(asSO, &group.UpdateGroupRequest{ID: g.ID, RoleIDs: []string{so}}); err != nil {
			t.Errorf("SO add over the lapsed claim: %v", err)
		}
	}
	var conflict *group.GroupConflictError
	if err := f.svc.RemoveMember(asAdmin, alice.ID, g.ID); !errors.As(err, &conflict) {
		t.Fatalf("removal after its claim lapsed: %v, want *GroupConflictError", err)
	}
	if !raced {
		t.Fatal("the SO add never ran after the claim")
	}
	if u, err := f.users.GetUser(asSO, alice.ID); err != nil || !slices.Contains(u.GroupIDs, g.ID) {
		t.Fatalf("alice after the undone removal = %+v, %v; want the membership", u, err)
	}
}

// TestGroupService_ClaimReleasedAfterMembershipChange pins that a membership
// change, committed or refused, leaves no claim on the group behind.
func TestGroupService_ClaimReleasedAfterMembershipChange(t *testing.T) {
	f := newStrictFixture(t)
	admin, so := role.SystemRoleID("acme", role.RoleNameAdmin), role.SystemRoleID("acme", role.RoleNameSO)
	keeper := authtest.CustomRole(t, f.roles, "acme", "group-keeper", permission.GroupsRead, permission.GroupsUpdate)
	asAdmin, asSO := acmeCaller(0, admin, keeper), acmeCaller(0, admin, so)
	g, err := f.groups.CreateGroup(asSO, &group.CreateGroupRequest{TenantID: "acme", Name: "eng"})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	signers, err := f.groups.CreateGroup(asSO, &group.CreateGroupRequest{TenantID: "acme", Name: "signers", RoleIDs: []string{so}})
	if err != nil {
		t.Fatalf("create SO group: %v", err)
	}
	alice, err := f.users.CreateUser(asSO, &user.CreateUserRequest{WithoutEmailLoginKey: true, Username: "alice", TenantID: "acme"})
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	update := func(step string, id string) {
		t.Helper()
		desc := step
		if _, err := f.svc.Update(asSO, &group.UpdateGroupRequest{ID: id, Description: &desc}); err != nil {
			t.Fatalf("update after %s: %v (claim left behind)", step, err)
		}
	}
	if err := f.svc.AddMember(asAdmin, alice.ID, g.ID); err != nil {
		t.Fatalf("add alice: %v", err)
	}
	update("add", g.ID)
	if err := f.svc.RemoveMember(asAdmin, alice.ID, g.ID); err != nil {
		t.Fatalf("remove alice: %v", err)
	}
	update("remove", g.ID)
	if err := f.svc.AddMember(asAdmin, alice.ID, signers.ID); err == nil {
		t.Fatal("admin adding a member to the SO group: want refused")
	}
	update("refused add", signers.ID)
	if _, err := f.members.AddMember(asSO, alice.ID, signers.ID); err != nil {
		t.Fatalf("seed alice into the SO group: %v", err)
	}
	if err := f.svc.RemoveMember(asAdmin, alice.ID, signers.ID); err == nil {
		t.Fatal("admin removing a member of the SO group: want refused")
	}
	update("refused remove", signers.ID)
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
