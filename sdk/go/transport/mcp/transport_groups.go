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

package mcp

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

var _ transport.GroupClient = (*Transport)(nil)

// callGroupTool runs one qrdb_group_* tool and decodes its JSON text into out
// (skipped when out is nil). The server's group JSON carries the SDK type's
// field names.
func (t *Transport) callGroupTool(ctx context.Context, method, tool string, args map[string]any, out any) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	text, err := t.callTool(ctx, tool, args)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if jsonErr := json.Unmarshal([]byte(text), out); jsonErr != nil {
		return &transport.RequestError{Method: method, Err: &jsonDecodeError{Err: jsonErr}}
	}
	return nil
}

// CreateGroup creates an IAM group (qrdb_group_create).
func (t *Transport) CreateGroup(ctx context.Context, req *transport.CreateGroupRequest) (*transport.Group, error) {
	args := map[string]any{"name": req.Name}
	if req.TenantID != "" {
		args["tenant_id"] = req.TenantID
	}
	if req.Description != "" {
		args["description"] = req.Description
	}
	if len(req.RoleIDs) > 0 {
		args["role_ids"] = strings.Join(req.RoleIDs, ",")
	}
	var g transport.Group
	if err := t.callGroupTool(ctx, "CreateGroup", "qrdb_group_create", args, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// GetGroup retrieves a group by ID (qrdb_group_get).
func (t *Transport) GetGroup(ctx context.Context, id string) (*transport.Group, error) {
	var g transport.Group
	if err := t.callGroupTool(ctx, "GetGroup", "qrdb_group_get", map[string]any{"id": id}, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// GetGroupByName retrieves a group by name (qrdb_group_get_by_name).
func (t *Transport) GetGroupByName(ctx context.Context, req *transport.GetGroupByNameRequest) (*transport.Group, error) {
	args := map[string]any{"name": req.Name}
	if req.TenantID != "" {
		args["tenant_id"] = req.TenantID
	}
	var g transport.Group
	if err := t.callGroupTool(ctx, "GetGroupByName", "qrdb_group_get_by_name", args, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// UpdateGroup changes a group (qrdb_group_update).
func (t *Transport) UpdateGroup(ctx context.Context, req *transport.UpdateGroupRequest) (*transport.Group, error) {
	args := map[string]any{"id": req.ID}
	if req.Name != "" {
		args["name"] = req.Name
	}
	if req.Description != nil {
		args["description"] = *req.Description
	}
	if req.RoleIDs != nil {
		args["role_ids"] = strings.Join(*req.RoleIDs, ",")
	}
	var g transport.Group
	if err := t.callGroupTool(ctx, "UpdateGroup", "qrdb_group_update", args, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// DeleteGroup removes a group (qrdb_group_delete).
func (t *Transport) DeleteGroup(ctx context.Context, id string) error {
	return t.callGroupTool(ctx, "DeleteGroup", "qrdb_group_delete", map[string]any{"id": id}, nil)
}

// ListGroups returns a page of groups (qrdb_group_list).
func (t *Transport) ListGroups(ctx context.Context, req *transport.ListGroupsRequest) (*transport.ListGroupsResponse, error) {
	args := map[string]any{}
	if req.TenantID != "" {
		args["tenant_id"] = req.TenantID
	}
	if req.Page > 0 {
		args["page"] = req.Page
	}
	if req.PageSize > 0 {
		args["page_size"] = req.PageSize
	}
	var out transport.ListGroupsResponse
	if err := t.callGroupTool(ctx, "ListGroups", "qrdb_group_list", args, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AddGroupMember adds a user to a group (qrdb_group_add_member).
func (t *Transport) AddGroupMember(ctx context.Context, userID uint64, groupID string) error {
	return t.callGroupTool(ctx, "AddGroupMember", "qrdb_group_add_member", map[string]any{
		"user_id":  strconv.FormatUint(userID, 10),
		"group_id": groupID,
	}, nil)
}

// RemoveGroupMember removes a user from a group (qrdb_group_remove_member).
func (t *Transport) RemoveGroupMember(ctx context.Context, userID uint64, groupID string) error {
	return t.callGroupTool(ctx, "RemoveGroupMember", "qrdb_group_remove_member", map[string]any{
		"user_id":  strconv.FormatUint(userID, 10),
		"group_id": groupID,
	}, nil)
}

// ListGroupMembers lists a group's members (qrdb_group_list_members).
func (t *Transport) ListGroupMembers(ctx context.Context, groupID string) (*transport.ListGroupMembersResponse, error) {
	var out transport.ListGroupMembersResponse
	if err := t.callGroupTool(ctx, "ListGroupMembers", "qrdb_group_list_members", map[string]any{"group_id": groupID}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
