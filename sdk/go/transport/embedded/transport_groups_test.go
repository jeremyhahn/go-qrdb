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

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/group"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestGroupClient_EmbeddedLifecycleAndMembership(t *testing.T) {
	ctx := context.Background()
	gen, err := dao.NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatal(err)
	}
	users, err := user.NewStore(kvstore.NewMemoryKVStore(), gen)
	if err != nil {
		t.Fatal(err)
	}
	roles, err := role.NewStore(kvstore.NewMemoryKVStore())
	if err != nil {
		t.Fatal(err)
	}
	members, err := user.NewGroupMembership(users)
	if err != nil {
		t.Fatal(err)
	}
	groups, err := group.NewStore(kvstore.NewMemoryKVStore(), group.WithRoleLookup(roles), group.WithMemberRevoker(members))
	if err != nil {
		t.Fatal(err)
	}
	tr, err := New(newMockKVService(), &mockAdminService{},
		WithUserService(users), WithRoleService(roles), WithGroupService(groups))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	g, err := tr.CreateGroup(ctx, &transport.CreateGroupRequest{Name: "eng"})
	if err != nil || g.ID == "" || g.Name != "eng" {
		t.Fatalf("create = %+v, %v", g, err)
	}
	var taken *group.GroupNameTakenError
	if _, err := tr.CreateGroup(ctx, &transport.CreateGroupRequest{Name: "eng"}); !errors.As(err, &taken) {
		t.Fatalf("duplicate: %v, want *GroupNameTakenError", err)
	}
	if got, err := tr.GetGroupByName(ctx, &transport.GetGroupByNameRequest{Name: "eng"}); err != nil || got.ID != g.ID {
		t.Fatalf("get by name = %+v, %v", got, err)
	}
	desc := "engineering"
	if up, err := tr.UpdateGroup(ctx, &transport.UpdateGroupRequest{ID: g.ID, Description: &desc}); err != nil || up.Description != desc {
		t.Fatalf("update = %+v, %v", up, err)
	}
	if list, err := tr.ListGroups(ctx, &transport.ListGroupsRequest{}); err != nil || list.Total != 1 {
		t.Fatalf("list = %+v, %v", list, err)
	}

	alice, err := users.CreateUser(ctx, &user.CreateUserRequest{WithoutEmailLoginKey: true, Username: "alice", TenantID: g.TenantID})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.AddGroupMember(ctx, alice.ID, g.ID); err != nil {
		t.Fatalf("add member: %v", err)
	}
	ms, err := tr.ListGroupMembers(ctx, g.ID)
	if err != nil || len(ms.Members) != 1 || ms.Members[0].UserID != alice.ID {
		t.Fatalf("members = %+v, %v", ms, err)
	}
	if err := tr.RemoveGroupMember(ctx, alice.ID, g.ID); err != nil {
		t.Fatalf("remove member: %v", err)
	}
	if err := tr.DeleteGroup(ctx, g.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	var nf *group.GroupNotFoundError
	if _, err := tr.GetGroup(ctx, g.ID); !errors.As(err, &nf) {
		t.Fatalf("get deleted: %v, want *GroupNotFoundError", err)
	}
}

func TestGroupClient_EmbeddedNotConfigured(t *testing.T) {
	tr, err := New(newMockKVService(), &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var ni *transport.NotImplementedError
	if _, err := tr.CreateGroup(context.Background(), &transport.CreateGroupRequest{Name: "eng"}); !errors.As(err, &ni) {
		t.Fatalf("create without stores: %v, want *NotImplementedError", err)
	}
}
