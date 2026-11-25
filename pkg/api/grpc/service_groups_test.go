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

package grpc

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/group"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

func TestGroupRPCs_LifecycleAndMembership(t *testing.T) {
	ctx := tenant.WithTenantID(context.Background(), "acme")
	gen, err := dao.NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatal(err)
	}
	users, err := user.NewStore(kvstore.NewMemoryKVStore(), gen)
	if err != nil {
		t.Fatal(err)
	}
	members, err := user.NewGroupMembership(users)
	if err != nil {
		t.Fatal(err)
	}
	groups, err := group.NewStore(kvstore.NewMemoryKVStore(), group.WithMemberRevoker(members))
	if err != nil {
		t.Fatal(err)
	}
	s := &qrdbServiceServer{groupSvc: groups, userSvc: users}

	g, err := s.CreateGroup(ctx, &pb.CreateGroupRequest{TenantId: "acme", Name: "eng"})
	if err != nil || g.GetId() == "" {
		t.Fatalf("create = %v, %v", g, err)
	}
	if _, err := s.CreateGroup(ctx, &pb.CreateGroupRequest{TenantId: "acme", Name: "eng"}); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("duplicate: %v, want AlreadyExists", err)
	}
	if _, err := s.GetGroup(ctx, &pb.GetGroupRequest{Id: g.GetId()}); err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, err := s.GetGroupByName(ctx, &pb.GetGroupByNameRequest{TenantId: "acme", Name: "eng"}); err != nil {
		t.Fatalf("get by name: %v", err)
	}
	desc := "engineering"
	if up, err := s.UpdateGroup(ctx, &pb.UpdateGroupRequest{Id: g.GetId(), Description: &desc}); err != nil || up.GetDescription() != desc {
		t.Fatalf("update = %v, %v", up, err)
	}
	if list, err := s.ListGroups(ctx, &pb.ListGroupsRequest{TenantId: "acme"}); err != nil || list.GetTotal() != 1 {
		t.Fatalf("list = %v, %v", list, err)
	}

	alice, err := users.CreateUser(ctx, &user.CreateUserRequest{WithoutEmailLoginKey: true, Username: "alice", TenantID: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddGroupMember(ctx, &pb.GroupMemberRequest{UserId: alice.ID, GroupId: g.GetId()}); err != nil {
		t.Fatalf("add member: %v", err)
	}
	ms, err := s.ListGroupMembers(ctx, &pb.ListGroupMembersRequest{GroupId: g.GetId()})
	if err != nil || len(ms.GetMembers()) != 1 || ms.GetMembers()[0].GetUserId() != alice.ID {
		t.Fatalf("members = %v, %v", ms, err)
	}
	if _, err := s.RemoveGroupMember(ctx, &pb.GroupMemberRequest{UserId: alice.ID, GroupId: g.GetId()}); err != nil {
		t.Fatalf("remove member: %v", err)
	}
	if _, err := s.DeleteGroup(ctx, &pb.DeleteGroupRequest{Id: g.GetId()}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetGroup(ctx, &pb.GetGroupRequest{Id: g.GetId()}); status.Code(err) != codes.NotFound {
		t.Fatalf("get deleted: %v, want NotFound", err)
	}
}

func TestGroupRPCs_NotConfigured(t *testing.T) {
	s := &qrdbServiceServer{}
	if _, err := s.CreateGroup(context.Background(), &pb.CreateGroupRequest{Name: "eng"}); status.Code(err) == codes.OK {
		t.Fatal("CreateGroup without stores succeeded")
	}
}
