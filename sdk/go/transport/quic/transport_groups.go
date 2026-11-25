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

package quic

import (
	"context"
	"encoding/json"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

var _ transport.GroupClient = (*Transport)(nil)

// groupIDWireRequest names a group by ID (group.Get, group.Delete).
type groupIDWireRequest struct {
	ID string `json:"id"`
}

// callGroup sends one group.* operation. The SDK group types carry the
// server's wire field names, so req and out are encoded as they are.
func (t *Transport) callGroup(ctx context.Context, method, op string, req, out any) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return &transport.RequestError{Method: method, Err: &MarshalError{Err: err}}
	}
	data, err := t.call(ctx, op, payload)
	if err != nil {
		return &transport.RequestError{Method: method, Err: err}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return &transport.RequestError{Method: method, Err: &UnmarshalError{Err: err}}
	}
	return nil
}

// CreateGroup creates an IAM group (group.Create).
func (t *Transport) CreateGroup(ctx context.Context, req *transport.CreateGroupRequest) (*transport.Group, error) {
	var g transport.Group
	if err := t.callGroup(ctx, "CreateGroup", "group.Create", req, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// GetGroup retrieves a group by ID (group.Get).
func (t *Transport) GetGroup(ctx context.Context, id string) (*transport.Group, error) {
	var g transport.Group
	if err := t.callGroup(ctx, "GetGroup", "group.Get", groupIDWireRequest{ID: id}, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// GetGroupByName retrieves a group by name (group.GetByName).
func (t *Transport) GetGroupByName(ctx context.Context, req *transport.GetGroupByNameRequest) (*transport.Group, error) {
	var g transport.Group
	if err := t.callGroup(ctx, "GetGroupByName", "group.GetByName", req, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// UpdateGroup changes a group (group.Update).
func (t *Transport) UpdateGroup(ctx context.Context, req *transport.UpdateGroupRequest) (*transport.Group, error) {
	var g transport.Group
	if err := t.callGroup(ctx, "UpdateGroup", "group.Update", req, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// DeleteGroup removes a group (group.Delete).
func (t *Transport) DeleteGroup(ctx context.Context, id string) error {
	return t.callGroup(ctx, "DeleteGroup", "group.Delete", groupIDWireRequest{ID: id}, nil)
}

// ListGroups returns a page of groups (group.List).
func (t *Transport) ListGroups(ctx context.Context, req *transport.ListGroupsRequest) (*transport.ListGroupsResponse, error) {
	var out transport.ListGroupsResponse
	if err := t.callGroup(ctx, "ListGroups", "group.List", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AddGroupMember adds a user to a group (group.AddMember).
func (t *Transport) AddGroupMember(ctx context.Context, userID uint64, groupID string) error {
	return t.callGroup(ctx, "AddGroupMember", "group.AddMember", transport.GroupMemberRequest{UserID: userID, GroupID: groupID}, nil)
}

// RemoveGroupMember removes a user from a group (group.RemoveMember).
func (t *Transport) RemoveGroupMember(ctx context.Context, userID uint64, groupID string) error {
	return t.callGroup(ctx, "RemoveGroupMember", "group.RemoveMember", transport.GroupMemberRequest{UserID: userID, GroupID: groupID}, nil)
}

// ListGroupMembers lists a group's members (group.ListMembers).
func (t *Transport) ListGroupMembers(ctx context.Context, groupID string) (*transport.ListGroupMembersResponse, error) {
	var out transport.ListGroupMembersResponse
	if err := t.callGroup(ctx, "ListGroupMembers", "group.ListMembers", transport.GroupMemberRequest{GroupID: groupID}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
