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

package group

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/role"
)

// GroupServicer defines the operations on IAM group records. Membership is
// recorded on the user record; see pkg/user GroupMembership.
type GroupServicer interface {
	// CreateGroup persists a new group. The name must be unique within the
	// tenant (*GroupNameTakenError otherwise) and every role must be a role
	// of the tenant (*RoleNotInTenantError otherwise).
	CreateGroup(ctx context.Context, req *CreateGroupRequest) (*Group, error)

	// GetGroup returns the group with the given ID (*GroupNotFoundError when
	// absent or of another tenant).
	GetGroup(ctx context.Context, id string) (*Group, error)

	// GetGroupByName returns the group of tenantID with the given name.
	GetGroupByName(ctx context.Context, tenantID, name string) (*Group, error)

	// UpdateGroup changes the name, description or role set of a group.
	UpdateGroup(ctx context.Context, req *UpdateGroupRequest) (*Group, error)

	// DeleteGroup removes the group from every member, then the record. It
	// deletes the record a prior ClaimGroup(ClaimDelete) froze, or claims it
	// itself; a live member claim fails it with *GroupConflictError.
	DeleteGroup(ctx context.Context, id string) error

	// ClaimGroup runs guard on exactly the stored record of group id and,
	// when it passes, marks that record claimed for op (ClaimDelete or
	// ClaimMember) by compare-and-swap, so the record cannot change until
	// the claim is released (or the group deleted). A live claim already on
	// the record, or a record changed after the read, fails with
	// *GroupConflictError; a guard error is returned with nothing written.
	// It returns the judged group and the claim's token.
	ClaimGroup(ctx context.Context, id, op string, guard func(*Group) error) (*Group, string, error)

	// ReleaseClaim clears the claim token holds on group id; a no-op when
	// the claim is no longer on the record or the group is gone.
	ReleaseClaim(ctx context.Context, id, token string) error

	// ListGroups returns a page of the tenant's groups ordered by ID.
	ListGroups(ctx context.Context, req *ListGroupsRequest) (*ListGroupsResponse, error)
}

// RoleLookup resolves a role by ID. role.RoleServicer satisfies it.
type RoleLookup interface {
	GetRole(ctx context.Context, id string) (*role.Role, error)
}

// MemberRevoker removes a group from every member's record. pkg/user
// GroupMembership satisfies it; DeleteGroup calls it before the record goes,
// so an interrupted delete is finished by a plain retry.
type MemberRevoker interface {
	RevokeGroup(ctx context.Context, tenantID, groupID string) (int, error)
}
