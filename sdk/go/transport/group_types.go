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

package transport

// Group is an IAM group: a named set of roles of one tenant. Every member of
// the group holds the group's roles in addition to its own.
type Group struct {
	ID          string   `json:"id"`
	TenantID    string   `json:"tenantID"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	RoleIDs     []string `json:"roleIDs"`
	CreatedAt   string   `json:"createdAt"`
	UpdatedAt   string   `json:"updatedAt"`
}

// CreateGroupRequest creates a group. An empty TenantID means the caller's
// tenant.
type CreateGroupRequest struct {
	TenantID    string   `json:"tenantID,omitempty"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	RoleIDs     []string `json:"roleIDs,omitempty"`
}

// GetGroupByNameRequest looks a group up by its tenant-scoped name.
type GetGroupByNameRequest struct {
	TenantID string `json:"tenantID,omitempty"`
	Name     string `json:"name"`
}

// UpdateGroupRequest changes a group. An empty Name keeps the name, a nil
// Description keeps the description and a nil RoleIDs keeps the role set; a
// non-nil empty RoleIDs clears it.
type UpdateGroupRequest struct {
	ID          string    `json:"id"`
	Name        string    `json:"name,omitempty"`
	Description *string   `json:"description,omitempty"`
	RoleIDs     *[]string `json:"roleIDs,omitempty"`
}

// ListGroupsRequest pages through a tenant's groups. An empty TenantID means
// the caller's tenant.
type ListGroupsRequest struct {
	TenantID string `json:"tenantID,omitempty"`
	Page     int    `json:"page,omitempty"`
	PageSize int    `json:"pageSize,omitempty"`
}

// ListGroupsResponse is one page of groups.
type ListGroupsResponse struct {
	Groups  []*Group `json:"groups"`
	Total   int      `json:"total"`
	HasMore bool     `json:"hasMore"`
}

// GroupMemberRequest names a user and a group for a membership change.
type GroupMemberRequest struct {
	UserID  uint64 `json:"userID,string"`
	GroupID string `json:"groupID"`
}

// GroupMember is one member of a group.
type GroupMember struct {
	UserID   uint64 `json:"userID,string"`
	Username string `json:"username"`
	TenantID string `json:"tenantID"`
}

// ListGroupMembersResponse lists the members of a group ordered by user ID.
type ListGroupMembersResponse struct {
	Members []GroupMember `json:"members"`
}
