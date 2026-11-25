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
	"errors"
	"math"
	"net"
	"slices"
	"sync"
	"testing"

	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// groupStubServer records the last request of each group RPC and answers canned replies or err.
type groupStubServer struct {
	pb.UnimplementedQRDBServiceServer
	mu      sync.Mutex
	group   *pb.GroupResponse
	list    *pb.ListGroupsResponse
	members *pb.ListGroupMembersResponse
	err     error
	last    map[string]any
}

func (s *groupStubServer) record(name string, req any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last == nil {
		s.last = map[string]any{}
	}
	s.last[name] = req
	return s.err
}

func (s *groupStubServer) seen(name string) any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last[name]
}

func (s *groupStubServer) CreateGroup(_ context.Context, in *pb.CreateGroupRequest) (*pb.GroupResponse, error) {
	if err := s.record("CreateGroup", in); err != nil {
		return nil, err
	}
	return s.group, nil
}

func (s *groupStubServer) GetGroup(_ context.Context, in *pb.GetGroupRequest) (*pb.GroupResponse, error) {
	if err := s.record("GetGroup", in); err != nil {
		return nil, err
	}
	return s.group, nil
}

func (s *groupStubServer) GetGroupByName(_ context.Context, in *pb.GetGroupByNameRequest) (*pb.GroupResponse, error) {
	if err := s.record("GetGroupByName", in); err != nil {
		return nil, err
	}
	return s.group, nil
}

func (s *groupStubServer) UpdateGroup(_ context.Context, in *pb.UpdateGroupRequest) (*pb.GroupResponse, error) {
	if err := s.record("UpdateGroup", in); err != nil {
		return nil, err
	}
	return s.group, nil
}

func (s *groupStubServer) DeleteGroup(_ context.Context, in *pb.DeleteGroupRequest) (*emptypb.Empty, error) {
	if err := s.record("DeleteGroup", in); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *groupStubServer) ListGroups(_ context.Context, in *pb.ListGroupsRequest) (*pb.ListGroupsResponse, error) {
	if err := s.record("ListGroups", in); err != nil {
		return nil, err
	}
	return s.list, nil
}

func (s *groupStubServer) AddGroupMember(_ context.Context, in *pb.GroupMemberRequest) (*emptypb.Empty, error) {
	if err := s.record("AddGroupMember", in); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *groupStubServer) RemoveGroupMember(_ context.Context, in *pb.GroupMemberRequest) (*emptypb.Empty, error) {
	if err := s.record("RemoveGroupMember", in); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *groupStubServer) ListGroupMembers(_ context.Context, in *pb.ListGroupMembersRequest) (*pb.ListGroupMembersResponse, error) {
	if err := s.record("ListGroupMembers", in); err != nil {
		return nil, err
	}
	return s.members, nil
}

func startGroupStub(t *testing.T, s *groupStubServer) *Transport {
	t.Helper()
	gs := googlegrpc.NewServer()
	pb.RegisterQRDBServiceServer(gs, s)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = gs.Serve(lis) }()
	conn, err := googlegrpc.NewClient(lis.Addr().String(), googlegrpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		gs.Stop()
	})
	return &Transport{config: &transport.Config{Address: lis.Addr().String()}, conn: conn, client: pb.NewQRDBServiceClient(conn)}
}

var stubGroup = &pb.GroupResponse{
	Id: "g1", TenantId: "t1", Name: "ops", Description: "d",
	RoleIds: []string{"r1", "r2"}, CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-02T00:00:00Z",
}

var wantGroup = transport.Group{
	ID: "g1", TenantID: "t1", Name: "ops", Description: "d",
	RoleIDs: []string{"r1", "r2"}, CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-02T00:00:00Z",
}

func sameGroup(got *transport.Group, want transport.Group) bool {
	return got != nil && got.ID == want.ID && got.TenantID == want.TenantID && got.Name == want.Name &&
		got.Description == want.Description && slices.Equal(got.RoleIDs, want.RoleIDs) &&
		got.CreatedAt == want.CreatedAt && got.UpdatedAt == want.UpdatedAt
}

func TestGRPCGroups_RequestsAndConversions(t *testing.T) {
	s := &groupStubServer{
		group: stubGroup,
		list:  &pb.ListGroupsResponse{Groups: []*pb.GroupResponse{stubGroup}, Total: 3, HasMore: true},
		members: &pb.ListGroupMembersResponse{Members: []*pb.GroupMember{
			{UserId: math.MaxUint64, Username: "bob", TenantId: "t1"},
		}},
	}
	tr := startGroupStub(t, s)
	ctx := context.Background()

	g, err := tr.CreateGroup(ctx, &transport.CreateGroupRequest{TenantID: "t1", Name: "ops", Description: "d", RoleIDs: []string{"r1", "r2"}})
	if err != nil || !sameGroup(g, wantGroup) {
		t.Fatalf("CreateGroup = %+v, %v", g, err)
	}
	if c := s.seen("CreateGroup").(*pb.CreateGroupRequest); c.GetTenantId() != "t1" || c.GetName() != "ops" || c.GetDescription() != "d" || !slices.Equal(c.GetRoleIds(), []string{"r1", "r2"}) {
		t.Fatalf("CreateGroup request = %v", c)
	}

	if g, err := tr.GetGroup(ctx, "g1"); err != nil || !sameGroup(g, wantGroup) {
		t.Fatalf("GetGroup = %+v, %v", g, err)
	}
	if r := s.seen("GetGroup").(*pb.GetGroupRequest); r.GetId() != "g1" {
		t.Fatalf("GetGroup request = %v", r)
	}

	if g, err := tr.GetGroupByName(ctx, &transport.GetGroupByNameRequest{TenantID: "t1", Name: "ops"}); err != nil || !sameGroup(g, wantGroup) {
		t.Fatalf("GetGroupByName = %+v, %v", g, err)
	}
	if r := s.seen("GetGroupByName").(*pb.GetGroupByNameRequest); r.GetTenantId() != "t1" || r.GetName() != "ops" {
		t.Fatalf("GetGroupByName request = %v", r)
	}

	if err := tr.DeleteGroup(ctx, "g1"); err != nil {
		t.Fatalf("DeleteGroup = %v", err)
	}
	if r := s.seen("DeleteGroup").(*pb.DeleteGroupRequest); r.GetId() != "g1" {
		t.Fatalf("DeleteGroup request = %v", r)
	}

	list, err := tr.ListGroups(ctx, &transport.ListGroupsRequest{TenantID: "t1", Page: 2, PageSize: 5})
	if err != nil || len(list.Groups) != 1 || !sameGroup(list.Groups[0], wantGroup) || list.Total != 3 || !list.HasMore {
		t.Fatalf("ListGroups = %+v, %v", list, err)
	}
	if r := s.seen("ListGroups").(*pb.ListGroupsRequest); r.GetTenantId() != "t1" || r.GetPage() != 2 || r.GetPageSize() != 5 {
		t.Fatalf("ListGroups request = %v", r)
	}

	if err := tr.AddGroupMember(ctx, math.MaxUint64, "g1"); err != nil {
		t.Fatalf("AddGroupMember = %v", err)
	}
	if r := s.seen("AddGroupMember").(*pb.GroupMemberRequest); r.GetUserId() != math.MaxUint64 || r.GetGroupId() != "g1" {
		t.Fatalf("AddGroupMember request = %v", r)
	}
	if err := tr.RemoveGroupMember(ctx, 7, "g2"); err != nil {
		t.Fatalf("RemoveGroupMember = %v", err)
	}
	if r := s.seen("RemoveGroupMember").(*pb.GroupMemberRequest); r.GetUserId() != 7 || r.GetGroupId() != "g2" {
		t.Fatalf("RemoveGroupMember request = %v", r)
	}

	members, err := tr.ListGroupMembers(ctx, "g1")
	if err != nil || len(members.Members) != 1 || members.Members[0] != (transport.GroupMember{UserID: math.MaxUint64, Username: "bob", TenantID: "t1"}) {
		t.Fatalf("ListGroupMembers = %+v, %v", members, err)
	}
	if r := s.seen("ListGroupMembers").(*pb.ListGroupMembersRequest); r.GetGroupId() != "g1" {
		t.Fatalf("ListGroupMembers request = %v", r)
	}
}

func TestGRPCGroups_UpdateRoleSetFlag(t *testing.T) {
	s := &groupStubServer{group: stubGroup}
	tr := startGroupStub(t, s)
	desc := "new"
	for _, tc := range []struct {
		name    string
		roleIDs *[]string
		set     bool
		ids     []string
	}{
		{"nil keeps", nil, false, nil},
		{"empty clears", &[]string{}, true, nil},
		{"one replaces", &[]string{"r1"}, true, []string{"r1"}},
	} {
		g, err := tr.UpdateGroup(context.Background(), &transport.UpdateGroupRequest{ID: "g1", Name: "n", Description: &desc, RoleIDs: tc.roleIDs})
		if err != nil || !sameGroup(g, wantGroup) {
			t.Fatalf("%s: UpdateGroup = %+v, %v", tc.name, g, err)
		}
		r := s.seen("UpdateGroup").(*pb.UpdateGroupRequest)
		if r.GetId() != "g1" || r.GetName() != "n" || r.Description == nil || r.GetDescription() != "new" {
			t.Fatalf("%s: request = %v", tc.name, r)
		}
		if r.GetSetRoleIds() != tc.set || !slices.Equal(r.GetRoleIds(), tc.ids) {
			t.Fatalf("%s: set_role_ids=%v role_ids=%v, want %v %v", tc.name, r.GetSetRoleIds(), r.GetRoleIds(), tc.set, tc.ids)
		}
	}
	if _, err := tr.UpdateGroup(context.Background(), &transport.UpdateGroupRequest{ID: "g1"}); err != nil {
		t.Fatalf("UpdateGroup = %v", err)
	}
	if r := s.seen("UpdateGroup").(*pb.UpdateGroupRequest); r.Description != nil {
		t.Fatalf("nil Description sent %q", r.GetDescription())
	}
}

func TestGRPCGroups_ErrorsMapped(t *testing.T) {
	ctx := context.Background()
	aborted := startGroupStub(t, &groupStubServer{err: status.Error(codes.Aborted, "group g1 changed")})
	var reqErr *transport.RequestError
	if err := aborted.AddGroupMember(ctx, 1, "g1"); !errors.As(err, &reqErr) || reqErr.StatusCode != int(codes.Aborted) || reqErr.Method != "AddGroupMember" {
		t.Fatalf("AddGroupMember Aborted = %v, want a RequestError with code Aborted", err)
	}
	if _, err := aborted.UpdateGroup(ctx, &transport.UpdateGroupRequest{ID: "g1"}); !errors.As(err, &reqErr) || reqErr.StatusCode != int(codes.Aborted) {
		t.Fatalf("UpdateGroup Aborted = %v, want a RequestError with code Aborted", err)
	}

	missing := startGroupStub(t, &groupStubServer{err: status.Error(codes.NotFound, "group g9")})
	var nf *transport.KeyNotFoundError
	if _, err := missing.GetGroup(ctx, "g9"); !errors.As(err, &nf) {
		t.Fatalf("GetGroup NotFound = %T %v, want KeyNotFoundError", err, err)
	}
	if err := missing.DeleteGroup(ctx, "g9"); !errors.As(err, &nf) {
		t.Fatalf("DeleteGroup NotFound = %T %v, want KeyNotFoundError", err, err)
	}
	if _, err := missing.ListGroupMembers(ctx, "g9"); !errors.As(err, &nf) {
		t.Fatalf("ListGroupMembers NotFound = %T %v, want KeyNotFoundError", err, err)
	}
	if _, err := missing.ListGroups(ctx, &transport.ListGroupsRequest{}); !errors.As(err, &nf) {
		t.Fatalf("ListGroups NotFound = %T %v, want KeyNotFoundError", err, err)
	}
	if _, err := missing.CreateGroup(ctx, &transport.CreateGroupRequest{Name: "n"}); err == nil {
		t.Fatal("CreateGroup error = nil")
	}
	if _, err := missing.GetGroupByName(ctx, &transport.GetGroupByNameRequest{Name: "n"}); err == nil {
		t.Fatal("GetGroupByName error = nil")
	}
	if err := missing.RemoveGroupMember(ctx, 1, "g9"); err == nil {
		t.Fatal("RemoveGroupMember error = nil")
	}
}

func TestGRPCGroups_Closed(t *testing.T) {
	tr := startGroupStub(t, &groupStubServer{})
	_ = tr.Close()
	ctx := context.Background()
	var ce *transport.ClosedError
	check := func(name string, err error) {
		t.Helper()
		if !errors.As(err, &ce) {
			t.Fatalf("%s = %T, want ClosedError", name, err)
		}
	}
	_, err := tr.CreateGroup(ctx, &transport.CreateGroupRequest{Name: "n"})
	check("CreateGroup", err)
	_, err = tr.GetGroup(ctx, "g1")
	check("GetGroup", err)
	_, err = tr.GetGroupByName(ctx, &transport.GetGroupByNameRequest{Name: "n"})
	check("GetGroupByName", err)
	_, err = tr.UpdateGroup(ctx, &transport.UpdateGroupRequest{ID: "g1"})
	check("UpdateGroup", err)
	check("DeleteGroup", tr.DeleteGroup(ctx, "g1"))
	_, err = tr.ListGroups(ctx, &transport.ListGroupsRequest{})
	check("ListGroups", err)
	check("AddGroupMember", tr.AddGroupMember(ctx, 1, "g1"))
	check("RemoveGroupMember", tr.RemoveGroupMember(ctx, 1, "g1"))
	_, err = tr.ListGroupMembers(ctx, "g1")
	check("ListGroupMembers", err)
}
