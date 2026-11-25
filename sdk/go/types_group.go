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

package sdk

import (
	"github.com/jeremyhahn/go-qrdb/pkg/group"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// IAM groups (go-qrdb#1651). A group is a named set of roles of one tenant;
// every member holds the group's roles. Client.Groups returns the client.

// GroupClient is the IAM group management surface.
type GroupClient = transport.GroupClient

// Group is an IAM group record as the transports return it.
type Group = transport.Group

// CreateGroupRequest creates a group.
type CreateGroupRequest = transport.CreateGroupRequest

// GetGroupByNameRequest looks a group up by its tenant-scoped name.
type GetGroupByNameRequest = transport.GetGroupByNameRequest

// UpdateGroupRequest changes a group's name, description or role set.
type UpdateGroupRequest = transport.UpdateGroupRequest

// ListGroupsRequest pages through a tenant's groups.
type ListGroupsRequest = transport.ListGroupsRequest

// ListGroupsResponse is one page of groups.
type ListGroupsResponse = transport.ListGroupsResponse

// GroupMember is one member of a group.
type GroupMember = transport.GroupMember

// ListGroupMembersResponse lists the members of a group.
type ListGroupMembersResponse = transport.ListGroupMembersResponse

// GroupNotFoundError is returned when a group does not exist in the
// caller's view (absent, or of another tenant).
type GroupNotFoundError = group.GroupNotFoundError

// GroupNameTakenError is returned when a tenant already has a group of
// that name.
type GroupNameTakenError = group.GroupNameTakenError

// GroupRoleNotInTenantError is returned when a group would carry a role
// that is not a role of the group's tenant.
type GroupRoleNotInTenantError = group.RoleNotInTenantError

// GroupValidationError is returned for a malformed group request.
type GroupValidationError = group.ValidationError
