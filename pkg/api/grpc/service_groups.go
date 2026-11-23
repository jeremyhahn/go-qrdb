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
	"time"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/group"
	"github.com/jeremyhahn/go-qrdb/pkg/group/groupauthz"
	"google.golang.org/protobuf/types/known/emptypb"
)

// groups returns the IAM group service, or a not-configured error.
func (s *qrdbServiceServer) groups() (*groupauthz.Service, error) {
	svc := groupauthz.New(s.groupSvc, s.userSvc, s.roleSvc, s.authMW)
	if svc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "group"})
	}
	return svc, nil
}

// CreateGroup creates an IAM group.
func (s *qrdbServiceServer) CreateGroup(ctx context.Context, req *pb.CreateGroupRequest) (*pb.GroupResponse, error) {
	svc, err := s.groups()
	if err != nil {
		return nil, err
	}
	g, err := svc.Create(ctx, &group.CreateGroupRequest{
		TenantID:    req.GetTenantId(),
		Name:        req.GetName(),
		Description: req.GetDescription(),
		RoleIDs:     req.GetRoleIds(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	return groupToProto(g), nil
}

// GetGroup retrieves a group by ID.
func (s *qrdbServiceServer) GetGroup(ctx context.Context, req *pb.GetGroupRequest) (*pb.GroupResponse, error) {
	svc, err := s.groups()
	if err != nil {
		return nil, err
	}
	g, err := svc.Get(ctx, req.GetId())
	if err != nil {
		return nil, mapServiceError(err)
	}
	return groupToProto(g), nil
}

// GetGroupByName retrieves a group by tenant-scoped name.
func (s *qrdbServiceServer) GetGroupByName(ctx context.Context, req *pb.GetGroupByNameRequest) (*pb.GroupResponse, error) {
	svc, err := s.groups()
	if err != nil {
		return nil, err
	}
	g, err := svc.GetByName(ctx, req.GetTenantId(), req.GetName())
	if err != nil {
		return nil, mapServiceError(err)
	}
	return groupToProto(g), nil
}

// UpdateGroup changes a group.
func (s *qrdbServiceServer) UpdateGroup(ctx context.Context, req *pb.UpdateGroupRequest) (*pb.GroupResponse, error) {
	svc, err := s.groups()
	if err != nil {
		return nil, err
	}
	up := &group.UpdateGroupRequest{ID: req.GetId(), Name: req.GetName()}
	if req.Description != nil {
		d := req.GetDescription()
		up.Description = &d
	}
	if req.GetSetRoleIds() {
		up.RoleIDs = append([]string{}, req.GetRoleIds()...)
	}
	g, err := svc.Update(ctx, up)
	if err != nil {
		return nil, mapServiceError(err)
	}
	return groupToProto(g), nil
}

// DeleteGroup removes a group from every member, then the group.
func (s *qrdbServiceServer) DeleteGroup(ctx context.Context, req *pb.DeleteGroupRequest) (*emptypb.Empty, error) {
	svc, err := s.groups()
	if err != nil {
		return nil, err
	}
	if err := svc.Delete(ctx, req.GetId()); err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}

// ListGroups returns a page of a tenant's groups.
func (s *qrdbServiceServer) ListGroups(ctx context.Context, req *pb.ListGroupsRequest) (*pb.ListGroupsResponse, error) {
	svc, err := s.groups()
	if err != nil {
		return nil, err
	}
	resp, err := svc.List(ctx, &group.ListGroupsRequest{
		TenantID: req.GetTenantId(),
		Page:     int(req.GetPage()),
		PageSize: int(req.GetPageSize()),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	out := make([]*pb.GroupResponse, len(resp.Groups))
	for i, g := range resp.Groups {
		out[i] = groupToProto(g)
	}
	return &pb.ListGroupsResponse{Groups: out, Total: int32(resp.Total), HasMore: resp.HasMore}, nil
}

// AddGroupMember adds a user to a group.
func (s *qrdbServiceServer) AddGroupMember(ctx context.Context, req *pb.GroupMemberRequest) (*emptypb.Empty, error) {
	svc, err := s.groups()
	if err != nil {
		return nil, err
	}
	if err := svc.AddMember(ctx, req.GetUserId(), req.GetGroupId()); err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}

// RemoveGroupMember removes a user from a group.
func (s *qrdbServiceServer) RemoveGroupMember(ctx context.Context, req *pb.GroupMemberRequest) (*emptypb.Empty, error) {
	svc, err := s.groups()
	if err != nil {
		return nil, err
	}
	if err := svc.RemoveMember(ctx, req.GetUserId(), req.GetGroupId()); err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}

// ListGroupMembers returns the members of a group.
func (s *qrdbServiceServer) ListGroupMembers(ctx context.Context, req *pb.ListGroupMembersRequest) (*pb.ListGroupMembersResponse, error) {
	svc, err := s.groups()
	if err != nil {
		return nil, err
	}
	members, err := svc.ListMembers(ctx, req.GetGroupId())
	if err != nil {
		return nil, mapServiceError(err)
	}
	out := make([]*pb.GroupMember, len(members))
	for i, m := range members {
		out[i] = &pb.GroupMember{UserId: m.UserID, Username: m.Username, TenantId: m.TenantID}
	}
	return &pb.ListGroupMembersResponse{Members: out}, nil
}

func groupToProto(g *group.Group) *pb.GroupResponse {
	if g == nil {
		return nil
	}
	return &pb.GroupResponse{
		Id:          g.ID,
		TenantId:    g.TenantID,
		Name:        g.Name,
		Description: g.Description,
		RoleIds:     g.RoleIDs,
		CreatedAt:   g.CreatedAt.Format(time.RFC3339),
		UpdatedAt:   g.UpdatedAt.Format(time.RFC3339),
	}
}
