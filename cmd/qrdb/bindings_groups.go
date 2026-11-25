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

package main

import (
	"context"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// GroupBinding exposes IAM group operations (the connected client's
// GroupClient) to the desktop frontend. Request and response types are the
// transport group types, whose JSON keys are camelCase (tenantID, roleIDs);
// a user id arrives from the frontend as a decimal string.
type GroupBinding struct {
	logger *sdk.ConcreteLogger
	conn   *ConnectionBinding
	// client resolves the GroupClient of the current connection; tests
	// replace it with a stub.
	client func() (transport.GroupClient, error)
}

// NewGroupBinding creates a GroupBinding over the shared connection.
func NewGroupBinding(log *sdk.ConcreteLogger, conn *ConnectionBinding) *GroupBinding {
	b := &GroupBinding{logger: log, conn: conn}
	b.client = b.connectedGroups
	return b
}

// connectedGroups returns the GroupClient of the connected client.
func (b *GroupBinding) connectedGroups() (transport.GroupClient, error) {
	cl, err := b.conn.loadClient()
	if err != nil {
		return nil, err
	}
	return cl.Groups(), nil
}

// groups resolves the GroupClient, logging a failure under op.
func (b *GroupBinding) groups(op string) (transport.GroupClient, error) {
	b.logger.Debug(op + " called")
	gc, err := b.client()
	if err != nil {
		b.logger.Error(op+" failed", "error", err)
		return nil, err
	}
	return gc, nil
}

// done logs the outcome of op and returns err unchanged.
func (b *GroupBinding) done(op string, err error) error {
	if err != nil {
		b.logger.Error(op+" failed", "error", err)
		return err
	}
	b.logger.Debug(op + " completed")
	return nil
}

// Create creates a group in the request's tenant.
func (b *GroupBinding) Create(req *transport.CreateGroupRequest) (*transport.Group, error) {
	gc, err := b.groups("Create")
	if err != nil {
		return nil, err
	}
	g, err := gc.CreateGroup(context.Background(), req)
	return g, b.done("Create", err)
}

// Get retrieves a group by its ID.
func (b *GroupBinding) Get(id string) (*transport.Group, error) {
	gc, err := b.groups("Get")
	if err != nil {
		return nil, err
	}
	g, err := gc.GetGroup(context.Background(), id)
	return g, b.done("Get", err)
}

// GetByName retrieves a group by its tenant-scoped name.
func (b *GroupBinding) GetByName(req *transport.GetGroupByNameRequest) (*transport.Group, error) {
	gc, err := b.groups("GetByName")
	if err != nil {
		return nil, err
	}
	g, err := gc.GetGroupByName(context.Background(), req)
	return g, b.done("GetByName", err)
}

// Update changes a group's name, description or role set.
func (b *GroupBinding) Update(req *transport.UpdateGroupRequest) (*transport.Group, error) {
	gc, err := b.groups("Update")
	if err != nil {
		return nil, err
	}
	g, err := gc.UpdateGroup(context.Background(), req)
	return g, b.done("Update", err)
}

// Delete removes the group from every member, then the group.
func (b *GroupBinding) Delete(id string) error {
	gc, err := b.groups("Delete")
	if err != nil {
		return err
	}
	return b.done("Delete", gc.DeleteGroup(context.Background(), id))
}

// List returns a page of the tenant's groups.
func (b *GroupBinding) List(req *transport.ListGroupsRequest) (*transport.ListGroupsResponse, error) {
	gc, err := b.groups("List")
	if err != nil {
		return nil, err
	}
	resp, err := gc.ListGroups(context.Background(), req)
	return resp, b.done("List", err)
}

// AddMember adds the user (a decimal user id) to the group. A user id that
// is not a decimal uint64 is refused with *InvalidUserIDError before the
// client is called.
func (b *GroupBinding) AddMember(userID, groupID string) error {
	gc, err := b.groups("AddMember")
	if err != nil {
		return err
	}
	uid, err := parseUserID(userID)
	if err != nil {
		return b.done("AddMember", err)
	}
	return b.done("AddMember", gc.AddGroupMember(context.Background(), uid, groupID))
}

// RemoveMember removes the user (a decimal user id) from the group, refusing
// a malformed user id as AddMember does.
func (b *GroupBinding) RemoveMember(userID, groupID string) error {
	gc, err := b.groups("RemoveMember")
	if err != nil {
		return err
	}
	uid, err := parseUserID(userID)
	if err != nil {
		return b.done("RemoveMember", err)
	}
	return b.done("RemoveMember", gc.RemoveGroupMember(context.Background(), uid, groupID))
}

// Members lists the members of the group ordered by user ID.
func (b *GroupBinding) Members(groupID string) (*transport.ListGroupMembersResponse, error) {
	gc, err := b.groups("Members")
	if err != nil {
		return nil, err
	}
	resp, err := gc.ListGroupMembers(context.Background(), groupID)
	return resp, b.done("Members", err)
}
