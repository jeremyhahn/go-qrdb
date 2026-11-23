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
