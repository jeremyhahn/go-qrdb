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

package user

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// groupMembershipStore is the store-side surface of IAM group membership
// (pkg/group). Membership lives on the user record (User.GroupIDs), so every
// change commits against the exact bytes it read, as role revocation does
// (role_revoke.go): only the store can do that.
type groupMembershipStore interface {
	SetGroupMembership(ctx context.Context, userID uint64, groupID string, member bool) (bool, error)
	changeGroupMembership(ctx context.Context, userID uint64, groupID string, op membershipOp) (bool, error)
	RevokeGroup(ctx context.Context, tenantID, groupID string) (int, error)
	GroupMembers(ctx context.Context, tenantID, groupID string) ([]*User, error)
}

// GroupMembership adds and removes IAM group members on the user records and
// finds a group's members. Build it with NewGroupMembership over a store from
// NewStore or NewRoutingService.
type GroupMembership struct {
	s groupMembershipStore
}

// NewGroupMembership wraps svc. Only the stores this package builds can commit
// a membership change against the record they read; any other UserServicer is
// refused with *GroupMembershipUnsupportedError.
func NewGroupMembership(svc UserServicer) (*GroupMembership, error) {
	s, ok := svc.(groupMembershipStore)
	if !ok {
		return nil, &GroupMembershipUnsupportedError{}
	}
	return &GroupMembership{s: s}, nil
}

// AddMember adds groupID to the user's GroupIDs. It reports whether the record
// changed (false when the user was already a member).
func (g *GroupMembership) AddMember(ctx context.Context, userID uint64, groupID string) (bool, error) {
	return g.s.SetGroupMembership(ctx, userID, groupID, true)
}

// RemoveMember removes groupID from the user's GroupIDs. It reports whether the
// record changed (false when the user was not a member).
func (g *GroupMembership) RemoveMember(ctx context.Context, userID uint64, groupID string) (bool, error) {
	return g.s.SetGroupMembership(ctx, userID, groupID, false)
}

// AddPendingMember adds groupID to the user's PendingGroupIDs, a join that
// authentication ignores until PromoteMember. It reports whether the record
// changed (false when the user is already a member or already pending).
func (g *GroupMembership) AddPendingMember(ctx context.Context, userID uint64, groupID string) (bool, error) {
	return g.s.changeGroupMembership(ctx, userID, groupID, opAddPending)
}

// PromoteMember moves groupID from the user's PendingGroupIDs to GroupIDs. It
// reports whether the record changed (false when no join was pending).
func (g *GroupMembership) PromoteMember(ctx context.Context, userID uint64, groupID string) (bool, error) {
	return g.s.changeGroupMembership(ctx, userID, groupID, opPromote)
}

// DropPendingMember removes groupID from the user's PendingGroupIDs. It
// reports whether the record changed (false when no join was pending).
func (g *GroupMembership) DropPendingMember(ctx context.Context, userID uint64, groupID string) (bool, error) {
	return g.s.changeGroupMembership(ctx, userID, groupID, opDropPending)
}

// RevokeGroup removes groupID from the GroupIDs and PendingGroupIDs of every
// account in tenantID and reports how many accounts it changed. DeleteGroup
// calls it before the group record goes.
func (g *GroupMembership) RevokeGroup(ctx context.Context, tenantID, groupID string) (int, error) {
	return g.s.RevokeGroup(ctx, tenantID, groupID)
}

// Members returns the accounts of tenantID whose GroupIDs or PendingGroupIDs
// name groupID, in ascending ID order.
func (g *GroupMembership) Members(ctx context.Context, tenantID, groupID string) ([]*User, error) {
	return g.s.GroupMembers(ctx, tenantID, groupID)
}

// SetGroupMembership routes to the store that holds the user's tenant, found
// the way GetUser finds it.
func (r *routingUserService) SetGroupMembership(ctx context.Context, userID uint64, groupID string, member bool) (bool, error) {
	return r.changeGroupMembership(ctx, userID, groupID, memberOp(member))
}

// changeGroupMembership routes to the store that holds the user's tenant.
func (r *routingUserService) changeGroupMembership(ctx context.Context, userID uint64, groupID string, op membershipOp) (bool, error) {
	target, err := r.targetTenant(ctx, userID, "group membership")
	if err != nil {
		return false, err
	}
	svc, err := r.storeFor(ctx, target)
	if err != nil {
		return false, err
	}
	s, ok := svc.(groupMembershipStore)
	if !ok {
		return false, &GroupMembershipUnsupportedError{}
	}
	return s.changeGroupMembership(ctx, userID, groupID, op)
}

// RevokeGroup routes to the store that holds tenantID's accounts. A group's
// members always live in the group's own tenant: AddMember refuses a user of
// another tenant.
func (r *routingUserService) RevokeGroup(ctx context.Context, tenantID, groupID string) (int, error) {
	svc, err := r.storeFor(ctx, tenantid.Normalize(tenantID))
	if err != nil {
		return 0, err
	}
	s, ok := svc.(groupMembershipStore)
	if !ok {
		return 0, &GroupMembershipUnsupportedError{}
	}
	return s.RevokeGroup(ctx, tenantID, groupID)
}

// GroupMembers routes to the store that holds tenantID's accounts.
func (r *routingUserService) GroupMembers(ctx context.Context, tenantID, groupID string) ([]*User, error) {
	svc, err := r.storeFor(ctx, tenantid.Normalize(tenantID))
	if err != nil {
		return nil, err
	}
	s, ok := svc.(groupMembershipStore)
	if !ok {
		return nil, &GroupMembershipUnsupportedError{}
	}
	return s.GroupMembers(ctx, tenantID, groupID)
}

// SetGroupMembership adds (member true) or removes groupID on the user's
// record, committing against the bytes it read and retrying a lost race.
func (s *kvUserStore) SetGroupMembership(ctx context.Context, userID uint64, groupID string, member bool) (bool, error) {
	return s.changeGroupMembership(ctx, userID, groupID, memberOp(member))
}

// changeGroupMembership applies op for groupID to the user's record,
// committing against the bytes it read and retrying a lost race.
func (s *kvUserStore) changeGroupMembership(ctx context.Context, userID uint64, groupID string, op membershipOp) (bool, error) {
	if groupID == "" {
		return false, &ValidationError{Field: "group_id", Reason: "must not be empty"}
	}
	if userID == 0 {
		return false, &ValidationError{Field: "user_id", Reason: "must not be zero"}
	}
	var lastErr error
	for range maxRevokeAttempts {
		base, err := s.store.Get(ctx, primaryKey(userID))
		if err != nil {
			return false, &StoreError{Op: "group membership", Detail: "primary lookup", Err: err}
		}
		if base == nil {
			return false, &UserNotFoundError{ID: userID}
		}
		req, err := membershipRequest(userID, base, groupID, op)
		if err != nil {
			return false, err
		}
		if req == nil {
			return false, nil
		}
		_, lastErr = s.updateUserAt(ctx, req, base)
		var conflict *ConcurrentUpdateError
		if lastErr == nil {
			return true, nil
		}
		if !errors.As(lastErr, &conflict) {
			return false, lastErr
		}
	}
	return false, lastErr
}

// RevokeGroup removes groupID from the GroupIDs and PendingGroupIDs of every
// account in tenantID.
func (s *kvUserStore) RevokeGroup(ctx context.Context, tenantID, groupID string) (int, error) {
	if groupID == "" {
		return 0, &ValidationError{Field: "group_id", Reason: "must not be empty"}
	}
	prefix := keyPrefixTenant + tenantid.Normalize(tenantID) + "/"
	ids, err := s.tenantUserIDs(ctx, prefix)
	if err != nil {
		return 0, err
	}
	changed := 0
	for _, id := range ids {
		ok, err := s.changeGroupMembership(ctx, id, groupID, opRevoke)
		var nf *UserNotFoundError
		if errors.As(err, &nf) {
			continue
		}
		if err != nil {
			return changed, err
		}
		if ok {
			changed++
		}
	}
	return changed, nil
}

// GroupMembers scans tenantID's accounts for those whose GroupIDs or
// PendingGroupIDs name groupID.
func (s *kvUserStore) GroupMembers(ctx context.Context, tenantID, groupID string) ([]*User, error) {
	if groupID == "" {
		return nil, &ValidationError{Field: "group_id", Reason: "must not be empty"}
	}
	prefix := keyPrefixTenant + tenantid.Normalize(tenantID) + "/"
	ids, err := s.tenantUserIDs(ctx, prefix)
	if err != nil {
		return nil, err
	}
	slices.Sort(ids)
	members := []*User{}
	for _, id := range ids {
		data, err := s.store.Get(ctx, primaryKey(id))
		if err != nil {
			return nil, &StoreError{Op: "group members", Detail: "primary lookup", Err: err}
		}
		if data == nil {
			continue
		}
		u := &User{}
		if err := json.Unmarshal(data, u); err != nil {
			return nil, &StoreError{Op: "group members", Detail: "unmarshal", Err: err}
		}
		if slices.Contains(u.GroupIDs, groupID) || slices.Contains(u.PendingGroupIDs, groupID) {
			members = append(members, u)
		}
	}
	return members, nil
}

// membershipRequest builds the update that applies op for groupID, or nil
// when the record already has the wanted membership or is terminal.
func membershipRequest(id uint64, base []byte, groupID string, op membershipOp) (*UpdateUserRequest, error) {
	u := &User{}
	if err := json.Unmarshal(base, u); err != nil {
		return nil, &StoreError{Op: "group membership", Detail: "unmarshal", Err: err}
	}
	if u.Lifecycle.Terminal() {
		return nil, nil
	}
	has := slices.Contains(u.GroupIDs, groupID)
	pending := slices.Contains(u.PendingGroupIDs, groupID)
	req := &UpdateUserRequest{ID: id}
	switch op {
	case opAdd:
		if has {
			return nil, nil
		}
		req.GroupIDs = withGroup(u.GroupIDs, groupID)
	case opRemove:
		if !has {
			return nil, nil
		}
		req.GroupIDs = withoutGroup(u.GroupIDs, groupID)
	case opAddPending:
		if has || pending {
			return nil, nil
		}
		req.pendingGroupIDs = withGroup(u.PendingGroupIDs, groupID)
	case opPromote:
		if !pending {
			return nil, nil
		}
		req.pendingGroupIDs = withoutGroup(u.PendingGroupIDs, groupID)
		if !has {
			req.GroupIDs = withGroup(u.GroupIDs, groupID)
		}
	case opDropPending:
		if !pending {
			return nil, nil
		}
		req.pendingGroupIDs = withoutGroup(u.PendingGroupIDs, groupID)
	case opRevoke:
		if !has && !pending {
			return nil, nil
		}
		if has {
			req.GroupIDs = withoutGroup(u.GroupIDs, groupID)
		}
		if pending {
			req.pendingGroupIDs = withoutGroup(u.PendingGroupIDs, groupID)
		}
	default:
		return nil, &ValidationError{Field: "op", Reason: "unknown group membership change"}
	}
	return req, nil
}

// membershipOp names a change membershipRequest makes to a user's groups.
type membershipOp int

const (
	opAdd membershipOp = iota + 1
	opRemove
	opAddPending
	opPromote
	opDropPending
	opRevoke
)

// memberOp is opAdd when member is true and opRemove otherwise.
func memberOp(member bool) membershipOp {
	if member {
		return opAdd
	}
	return opRemove
}

// withGroup returns a sorted copy of ids with groupID added.
func withGroup(ids []string, groupID string) []string {
	next := append(slices.Clone(ids), groupID)
	slices.Sort(next)
	return next
}

// withoutGroup returns a copy of ids without groupID, non-nil so it clears.
func withoutGroup(ids []string, groupID string) []string {
	next := slices.DeleteFunc(slices.Clone(ids), func(g string) bool { return g == groupID })
	if next == nil {
		next = []string{}
	}
	return next
}
