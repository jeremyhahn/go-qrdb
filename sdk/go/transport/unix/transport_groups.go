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

package unix

import (
	"context"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

var _ transport.GroupClient = (*Transport)(nil)

// CreateGroup creates an IAM group.
func (t *Transport) CreateGroup(ctx context.Context, req *transport.CreateGroupRequest) (*transport.Group, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.CreateGroup(ctx, &pb.CreateGroupRequest{
		TenantId:    req.TenantID,
		Name:        req.Name,
		Description: req.Description,
		RoleIds:     req.RoleIDs,
	})
	if err != nil {
		return nil, mapGRPCError("CreateGroup", err)
	}
	return protoToGroup(resp), nil
}

// GetGroup retrieves a group by ID.
func (t *Transport) GetGroup(ctx context.Context, id string) (*transport.Group, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.GetGroup(ctx, &pb.GetGroupRequest{Id: id})
	if err != nil {
		return nil, mapGRPCError("GetGroup", err)
	}
	return protoToGroup(resp), nil
}

// GetGroupByName retrieves a group by its tenant-scoped name.
func (t *Transport) GetGroupByName(ctx context.Context, req *transport.GetGroupByNameRequest) (*transport.Group, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.GetGroupByName(ctx, &pb.GetGroupByNameRequest{TenantId: req.TenantID, Name: req.Name})
	if err != nil {
		return nil, mapGRPCError("GetGroupByName", err)
	}
	return protoToGroup(resp), nil
}

// UpdateGroup changes a group.
func (t *Transport) UpdateGroup(ctx context.Context, req *transport.UpdateGroupRequest) (*transport.Group, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	in := &pb.UpdateGroupRequest{Id: req.ID, Name: req.Name, Description: req.Description}
	if req.RoleIDs != nil {
		in.RoleIds = *req.RoleIDs
		in.SetRoleIds = true
	}
	resp, err := t.client.UpdateGroup(ctx, in)
	if err != nil {
		return nil, mapGRPCError("UpdateGroup", err)
	}
	return protoToGroup(resp), nil
}

// DeleteGroup removes a group.
func (t *Transport) DeleteGroup(ctx context.Context, id string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if _, err := t.client.DeleteGroup(ctx, &pb.DeleteGroupRequest{Id: id}); err != nil {
		return mapGRPCError("DeleteGroup", err)
	}
	return nil
}

// ListGroups returns a page of groups.
func (t *Transport) ListGroups(ctx context.Context, req *transport.ListGroupsRequest) (*transport.ListGroupsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.ListGroups(ctx, &pb.ListGroupsRequest{
		TenantId: req.TenantID,
		Page:     int32(req.Page),
		PageSize: int32(req.PageSize),
	})
	if err != nil {
		return nil, mapGRPCError("ListGroups", err)
	}
	groups := make([]*transport.Group, len(resp.GetGroups()))
	for i, g := range resp.GetGroups() {
		groups[i] = protoToGroup(g)
	}
	return &transport.ListGroupsResponse{Groups: groups, Total: int(resp.GetTotal()), HasMore: resp.GetHasMore()}, nil
}

// AddGroupMember adds a user to a group.
func (t *Transport) AddGroupMember(ctx context.Context, userID uint64, groupID string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if _, err := t.client.AddGroupMember(ctx, &pb.GroupMemberRequest{UserId: userID, GroupId: groupID}); err != nil {
		return mapGRPCError("AddGroupMember", err)
	}
	return nil
}

// RemoveGroupMember removes a user from a group.
func (t *Transport) RemoveGroupMember(ctx context.Context, userID uint64, groupID string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if _, err := t.client.RemoveGroupMember(ctx, &pb.GroupMemberRequest{UserId: userID, GroupId: groupID}); err != nil {
		return mapGRPCError("RemoveGroupMember", err)
	}
	return nil
}

// ListGroupMembers lists a group's members.
func (t *Transport) ListGroupMembers(ctx context.Context, groupID string) (*transport.ListGroupMembersResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.ListGroupMembers(ctx, &pb.ListGroupMembersRequest{GroupId: groupID})
	if err != nil {
		return nil, mapGRPCError("ListGroupMembers", err)
	}
	out := &transport.ListGroupMembersResponse{Members: make([]transport.GroupMember, len(resp.GetMembers()))}
	for i, m := range resp.GetMembers() {
		out.Members[i] = transport.GroupMember{UserID: m.GetUserId(), Username: m.GetUsername(), TenantID: m.GetTenantId()}
	}
	return out, nil
}

// protoToGroup converts the wire group to the SDK type.
func protoToGroup(g *pb.GroupResponse) *transport.Group {
	return &transport.Group{
		ID:          g.GetId(),
		TenantID:    g.GetTenantId(),
		Name:        g.GetName(),
		Description: g.GetDescription(),
		RoleIDs:     g.GetRoleIds(),
		CreatedAt:   g.GetCreatedAt(),
		UpdatedAt:   g.GetUpdatedAt(),
	}
}
