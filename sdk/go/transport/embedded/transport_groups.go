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
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/group"
	"github.com/jeremyhahn/go-qrdb/pkg/group/groupauthz"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

var _ transport.GroupClient = (*Transport)(nil)

// groups admits the call and returns the shared group authorization path the
// network transports use, over this transport's stores and auth middleware.
func (t *Transport) groups(ctx context.Context, method string) (context.Context, *groupauthz.Service, error) {
	ctx, err := t.admit(ctx, method)
	if err != nil {
		return ctx, nil, err
	}
	svc := groupauthz.New(t.groupSvc, t.userSvc, t.roleSvc, t.authMW)
	if svc == nil {
		return ctx, nil, &transport.NotImplementedError{Method: method}
	}
	return ctx, svc, nil
}

// CreateGroup creates an IAM group.
func (t *Transport) CreateGroup(ctx context.Context, req *transport.CreateGroupRequest) (*transport.Group, error) {
	ctx, svc, err := t.groups(ctx, "CreateGroup")
	if err != nil {
		return nil, err
	}
	g, err := svc.Create(ctx, &group.CreateGroupRequest{
		TenantID: req.TenantID, Name: req.Name, Description: req.Description, RoleIDs: req.RoleIDs,
	})
	if err != nil {
		return nil, err
	}
	return groupToTransport(g), nil
}

// GetGroup retrieves a group by ID.
func (t *Transport) GetGroup(ctx context.Context, id string) (*transport.Group, error) {
	ctx, svc, err := t.groups(ctx, "GetGroup")
	if err != nil {
		return nil, err
	}
	g, err := svc.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return groupToTransport(g), nil
}

// GetGroupByName retrieves a group by its tenant-scoped name.
func (t *Transport) GetGroupByName(ctx context.Context, req *transport.GetGroupByNameRequest) (*transport.Group, error) {
	ctx, svc, err := t.groups(ctx, "GetGroupByName")
	if err != nil {
		return nil, err
	}
	g, err := svc.GetByName(ctx, req.TenantID, req.Name)
	if err != nil {
		return nil, err
	}
	return groupToTransport(g), nil
}

// UpdateGroup changes a group.
func (t *Transport) UpdateGroup(ctx context.Context, req *transport.UpdateGroupRequest) (*transport.Group, error) {
	ctx, svc, err := t.groups(ctx, "UpdateGroup")
	if err != nil {
		return nil, err
	}
	up := &group.UpdateGroupRequest{ID: req.ID, Name: req.Name, Description: req.Description}
	if req.RoleIDs != nil {
		up.RoleIDs = append([]string{}, (*req.RoleIDs)...)
	}
	g, err := svc.Update(ctx, up)
	if err != nil {
		return nil, err
	}
	return groupToTransport(g), nil
}

// DeleteGroup removes a group from every member, then the group.
func (t *Transport) DeleteGroup(ctx context.Context, id string) error {
	ctx, svc, err := t.groups(ctx, "DeleteGroup")
	if err != nil {
		return err
	}
	return svc.Delete(ctx, id)
}

// ListGroups returns a page of groups.
func (t *Transport) ListGroups(ctx context.Context, req *transport.ListGroupsRequest) (*transport.ListGroupsResponse, error) {
	ctx, svc, err := t.groups(ctx, "ListGroups")
	if err != nil {
		return nil, err
	}
	resp, err := svc.List(ctx, &group.ListGroupsRequest{TenantID: req.TenantID, Page: req.Page, PageSize: req.PageSize})
	if err != nil {
		return nil, err
	}
	out := &transport.ListGroupsResponse{Groups: make([]*transport.Group, len(resp.Groups)), Total: resp.Total, HasMore: resp.HasMore}
	for i, g := range resp.Groups {
		out.Groups[i] = groupToTransport(g)
	}
	return out, nil
}

// AddGroupMember adds a user to a group of the user's own tenant.
func (t *Transport) AddGroupMember(ctx context.Context, userID uint64, groupID string) error {
	ctx, svc, err := t.groups(ctx, "AddGroupMember")
	if err != nil {
		return err
	}
	return svc.AddMember(ctx, userID, groupID)
}

// RemoveGroupMember removes a user from a group.
func (t *Transport) RemoveGroupMember(ctx context.Context, userID uint64, groupID string) error {
	ctx, svc, err := t.groups(ctx, "RemoveGroupMember")
	if err != nil {
		return err
	}
	return svc.RemoveMember(ctx, userID, groupID)
}

// ListGroupMembers lists a group's members ordered by user ID.
func (t *Transport) ListGroupMembers(ctx context.Context, groupID string) (*transport.ListGroupMembersResponse, error) {
	ctx, svc, err := t.groups(ctx, "ListGroupMembers")
	if err != nil {
		return nil, err
	}
	members, err := svc.ListMembers(ctx, groupID)
	if err != nil {
		return nil, err
	}
	out := &transport.ListGroupMembersResponse{Members: make([]transport.GroupMember, len(members))}
	for i, m := range members {
		out.Members[i] = transport.GroupMember{UserID: m.UserID, Username: m.Username, TenantID: m.TenantID}
	}
	return out, nil
}

// groupToTransport converts a group record to the SDK type, with the
// timestamps formatted as the network transports send them.
func groupToTransport(g *group.Group) *transport.Group {
	roles := g.RoleIDs
	if roles == nil {
		roles = []string{}
	}
	return &transport.Group{
		ID:          g.ID,
		TenantID:    g.TenantID,
		Name:        g.Name,
		Description: g.Description,
		RoleIDs:     roles,
		CreatedAt:   g.CreatedAt.Format(time.RFC3339),
		UpdatedAt:   g.UpdatedAt.Format(time.RFC3339),
	}
}
