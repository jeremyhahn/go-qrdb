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

// Package groupauthz is the one authorization path every transport's IAM
// group operations go through: the target tenant is checked for the groups
// verb, and a role reaching a principal through a group passes the same
// escalation guard as a direct role assignment.
package groupauthz

import (
	"context"
	"errors"
	"slices"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/group"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// Service runs IAM group operations under the auth middleware's checks.
// A nil Auth skips authorization (embedded mode), never the tenant match
// between a member and its group.
type Service struct {
	Groups  group.GroupServicer
	Users   user.UserServicer
	Roles   role.RoleServicer
	Members *user.GroupMembership
	Auth    *middleware.AuthMiddleware
}

// New returns a Service, or nil when groups or users are not configured.
func New(groups group.GroupServicer, users user.UserServicer, roles role.RoleServicer, auth *middleware.AuthMiddleware) *Service {
	if groups == nil || users == nil {
		return nil
	}
	members, err := user.NewGroupMembership(users)
	if err != nil {
		return nil
	}
	return &Service{Groups: groups, Users: users, Roles: roles, Members: members, Auth: auth}
}

// Member is one member of a group.
type Member struct {
	UserID   uint64 `json:"userID,string"`
	Username string `json:"username"`
	TenantID string `json:"tenantID"`
}

// Create creates a group in the request's tenant (the caller's when empty).
func (s *Service) Create(ctx context.Context, req *group.CreateGroupRequest) (*group.Group, error) {
	target, err := tenant.EffectiveTenant(ctx, req.TenantID, "create group")
	if err != nil {
		return nil, err
	}
	if err := s.authorizeTenant(ctx, target, permission.GroupsCreate); err != nil {
		return nil, err
	}
	if err := s.Auth.AuthorizeGroupRoles(ctx, s.Users, s.Roles, target, "", req.RoleIDs); err != nil {
		return nil, err
	}
	next := *req
	next.TenantID = target
	return s.Groups.CreateGroup(ctx, &next)
}

// Get returns a group by ID.
func (s *Service) Get(ctx context.Context, id string) (*group.Group, error) {
	return s.read(ctx, id, permission.GroupsRead)
}

// GetByName returns a group by its name in tenantID (the caller's when empty).
func (s *Service) GetByName(ctx context.Context, tenantID, name string) (*group.Group, error) {
	target, err := tenant.EffectiveTenant(ctx, tenantID, "get group")
	if err != nil {
		return nil, err
	}
	if err := s.authorizeTenant(ctx, target, permission.GroupsRead); err != nil {
		return nil, err
	}
	return s.Groups.GetGroupByName(ctx, target, name)
}

// Update changes a group. Roles the update adds pass the escalation guard.
func (s *Service) Update(ctx context.Context, req *group.UpdateGroupRequest) (*group.Group, error) {
	if _, err := s.read(ctx, req.ID, permission.GroupsUpdate); err != nil {
		return nil, err
	}
	// The role guards are judged against the record the store commits over
	// (compare-and-swap), not against this read: a concurrent role change
	// between the two fails the update with *group.GroupConflictError
	// instead of being overwritten unchecked.
	next := *req
	callerGuard := req.Guard
	next.Guard = func(prior, upd *group.Group) error {
		if callerGuard != nil {
			if err := callerGuard(prior, upd); err != nil {
				return err
			}
		}
		var added, removed []string
		for _, rid := range upd.RoleIDs {
			if !slices.Contains(prior.RoleIDs, rid) {
				added = append(added, rid)
			}
		}
		for _, rid := range prior.RoleIDs {
			if !slices.Contains(upd.RoleIDs, rid) {
				removed = append(removed, rid)
			}
		}
		if err := s.Auth.AuthorizeGroupRoleRemoval(ctx, prior, removed); err != nil {
			return err
		}
		return s.Auth.AuthorizeGroupRoles(ctx, s.Users, s.Roles, prior.TenantID, prior.ID, added)
	}
	return s.Groups.UpdateGroup(ctx, &next)
}

// Delete removes a group from every member, then the group. The caller is
// judged on exactly the record the delete removes: the role-removal check
// runs inside ClaimGroup, whose delete claim freezes that record, so a role
// added after the check (an SO adding SO) fails the claim instead of being
// revoked from every member unchecked.
func (s *Service) Delete(ctx context.Context, id string) error {
	if _, err := s.read(ctx, id, permission.GroupsDelete); err != nil {
		return err
	}
	g, _, err := s.Groups.ClaimGroup(ctx, id, group.ClaimDelete, func(c *group.Group) error {
		return s.Auth.AuthorizeGroupRoleRemoval(ctx, c, c.RoleIDs)
	})
	if err != nil {
		return err
	}
	return s.Groups.DeleteGroup(ctx, g.ID)
}

// List returns a page of the tenant's groups (the caller's when empty).
func (s *Service) List(ctx context.Context, req *group.ListGroupsRequest) (*group.ListGroupsResponse, error) {
	target, err := tenant.EffectiveTenant(ctx, req.TenantID, "list groups")
	if err != nil {
		return nil, err
	}
	if err := s.authorizeTenant(ctx, target, permission.GroupsList); err != nil {
		return nil, err
	}
	next := *req
	next.TenantID = target
	return s.Groups.ListGroups(ctx, &next)
}

// AddMember adds a user to a group of the user's own tenant. The caller must
// be able to assign every role the group carries to that user. The check
// runs inside a member claim on the group record, held until the membership
// commits: the group's role set cannot change between the check and the
// join (a concurrent update fails with *group.GroupConflictError).
// The join is written as pending, which authentication ignores, and promoted
// to a membership only once the claim is released; a join whose claim lapsed
// is dropped and reported as a conflict.
func (s *Service) AddMember(ctx context.Context, userID uint64, groupID string) error {
	g, token, err := s.Groups.ClaimGroup(ctx, groupID, group.ClaimMember, func(c *group.Group) error {
		if err := s.Auth.AuthorizeGroupMembership(ctx, s.Users, s.Roles, userID, c); err != nil {
			return err
		}
		return s.sameTenant(ctx, userID, c)
	})
	if err != nil {
		return err
	}
	changed, err := s.Members.AddPendingMember(ctx, userID, g.ID)
	ctx = context.WithoutCancel(ctx)
	rerr := s.Groups.ReleaseClaim(ctx, g.ID, token)
	var lapsed *group.ClaimLapsedError
	isLapse := errors.As(rerr, &lapsed)
	if err != nil {
		if rerr != nil && !isLapse {
			return errors.Join(err, rerr)
		}
		return err
	}
	if rerr != nil {
		dropped, derr := s.Members.DropPendingMember(ctx, userID, g.ID)
		if !isLapse {
			return errors.Join(rerr, derr)
		}
		if !changed && !dropped && derr == nil {
			return nil
		}
		return errors.Join(&group.GroupConflictError{ID: g.ID}, derr)
	}
	_, err = s.Members.PromoteMember(ctx, userID, g.ID)
	return err
}

// RemoveMember removes a user from a group. Like AddMember, the removal
// check is judged on a member-claimed record, so a role added concurrently
// (an SO adding SO) cannot be stripped from the member unchecked.
// A removal whose claim lapsed before it committed is undone through AddMember,
// which re-checks the caller against the group's current roles, and reported
// as a conflict; when that re-add is refused the member stays removed and the
// refusal is joined to the conflict.
func (s *Service) RemoveMember(ctx context.Context, userID uint64, groupID string) error {
	g, token, err := s.Groups.ClaimGroup(ctx, groupID, group.ClaimMember, func(c *group.Group) error {
		if err := s.Auth.AuthorizeGroupMemberRemoval(ctx, s.Users, userID, c); err != nil {
			return err
		}
		return s.sameTenant(ctx, userID, c)
	})
	if err != nil {
		return err
	}
	changed, err := s.Members.RemoveMember(ctx, userID, g.ID)
	return s.finishMember(ctx, g.ID, token, changed, err, func(ctx context.Context) error {
		return s.AddMember(ctx, userID, g.ID)
	})
}

// finishMember releases the member claim of a removal once the change it
// guarded has committed (or failed), even when ctx is already cancelled. When
// the claim lapsed before the removal committed, undo restores the membership
// through the authorized add path, which re-checks the caller, and the result
// is *group.GroupConflictError joined with any undo error; an unreleased claim
// lapses after its lease.
func (s *Service) finishMember(ctx context.Context, id, token string, changed bool, err error, undo func(context.Context) error) error {
	ctx = context.WithoutCancel(ctx)
	rerr := s.Groups.ReleaseClaim(ctx, id, token)
	var lapsed *group.ClaimLapsedError
	isLapse := errors.As(rerr, &lapsed)
	if err != nil {
		if rerr != nil && !isLapse {
			return errors.Join(err, rerr)
		}
		return err
	}
	if !isLapse {
		return rerr
	}
	if !changed {
		return nil
	}
	return errors.Join(&group.GroupConflictError{ID: id}, undo(ctx))
}

// ListMembers returns the members of a group ordered by user ID.
func (s *Service) ListMembers(ctx context.Context, groupID string) ([]Member, error) {
	g, err := s.read(ctx, groupID, permission.GroupsRead)
	if err != nil {
		return nil, err
	}
	users, err := s.Members.Members(ctx, g.TenantID, g.ID)
	if err != nil {
		return nil, err
	}
	out := make([]Member, 0, len(users))
	for _, u := range users {
		out = append(out, Member{UserID: u.ID, Username: u.Username, TenantID: tenantid.Normalize(u.TenantID)})
	}
	return out, nil
}

func (s *Service) read(ctx context.Context, id string, perm permission.Permission) (*group.Group, error) {
	g, err := s.Groups.GetGroup(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.authorizeTenant(ctx, g.TenantID, perm); err != nil {
		return nil, err
	}
	return g, nil
}

func (s *Service) authorizeTenant(ctx context.Context, target string, perm permission.Permission) error {
	if s.Auth == nil {
		return nil
	}
	return s.Auth.AuthorizeTenant(ctx, target, perm)
}

// sameTenant refuses a member of another tenant than the group's, reported
// as the group not found.
func (s *Service) sameTenant(ctx context.Context, userID uint64, g *group.Group) error {
	u, err := s.Users.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	if tenantid.Normalize(u.TenantID) != g.TenantID {
		return &group.GroupNotFoundError{ID: g.ID}
	}
	return nil
}
