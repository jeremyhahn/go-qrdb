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

package rest

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

var _ transport.GroupClient = (*Transport)(nil)

// CreateGroup creates an IAM group via POST /groups.
func (t *Transport) CreateGroup(ctx context.Context, req *transport.CreateGroupRequest) (*transport.Group, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var g transport.Group
	if err := t.postJSONDecode(ctx, "CreateGroup", "/groups", req, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// GetGroup retrieves a group via GET /groups/{id}.
func (t *Transport) GetGroup(ctx context.Context, id string) (*transport.Group, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var g transport.Group
	if err := t.getJSONDecode(ctx, "GetGroup", "/groups/"+url.PathEscape(id), &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// GetGroupByName retrieves a group via GET /groups/by-name.
func (t *Transport) GetGroupByName(ctx context.Context, req *transport.GetGroupByNameRequest) (*transport.Group, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	params := url.Values{}
	params.Set("name", req.Name)
	if req.TenantID != "" {
		params.Set("tenantID", req.TenantID)
	}
	var g transport.Group
	if err := t.getJSONDecode(ctx, "GetGroupByName", "/groups/by-name?"+params.Encode(), &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// UpdateGroup changes a group via PUT /groups/{id}.
func (t *Transport) UpdateGroup(ctx context.Context, req *transport.UpdateGroupRequest) (*transport.Group, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var g transport.Group
	if err := t.putJSONDecode(ctx, "UpdateGroup", "/groups/"+url.PathEscape(req.ID), req, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// DeleteGroup removes a group via DELETE /groups/{id}.
func (t *Transport) DeleteGroup(ctx context.Context, id string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, t.baseURL+"/groups/"+url.PathEscape(id), nil)
	if err != nil {
		return &transport.RequestError{Method: "DeleteGroup", Err: err}
	}
	resp, err := t.client.Do(httpReq)
	if err != nil {
		return &transport.RequestError{Method: "DeleteGroup", Err: err}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		return nil
	}
	return t.decodeErrorResponse("DeleteGroup", resp)
}

// ListGroups returns a page of groups via GET /groups/list.
func (t *Transport) ListGroups(ctx context.Context, req *transport.ListGroupsRequest) (*transport.ListGroupsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	params := url.Values{}
	if req.TenantID != "" {
		params.Set("tenantID", req.TenantID)
	}
	if req.Page > 0 {
		params.Set("page", strconv.Itoa(req.Page))
	}
	if req.PageSize > 0 {
		params.Set("pageSize", strconv.Itoa(req.PageSize))
	}
	path := "/groups/list"
	if encoded := params.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var out transport.ListGroupsResponse
	if err := t.getJSONDecode(ctx, "ListGroups", path, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AddGroupMember adds a user to a group via POST /groups/members/add.
func (t *Transport) AddGroupMember(ctx context.Context, userID uint64, groupID string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	return t.postJSONDecode(ctx, "AddGroupMember", "/groups/members/add", transport.GroupMemberRequest{UserID: userID, GroupID: groupID}, nil)
}

// RemoveGroupMember removes a user from a group via POST /groups/members/remove.
func (t *Transport) RemoveGroupMember(ctx context.Context, userID uint64, groupID string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	return t.postJSONDecode(ctx, "RemoveGroupMember", "/groups/members/remove", transport.GroupMemberRequest{UserID: userID, GroupID: groupID}, nil)
}

// ListGroupMembers lists a group's members via GET /groups/members.
func (t *Transport) ListGroupMembers(ctx context.Context, groupID string) (*transport.ListGroupMembersResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var out transport.ListGroupMembersResponse
	if err := t.getJSONDecode(ctx, "ListGroupMembers", "/groups/members?groupID="+url.QueryEscape(groupID), &out); err != nil {
		return nil, err
	}
	return &out, nil
}
