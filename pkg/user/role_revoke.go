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

const (
	// maxRevokePasses bounds how many times RevokeRole sweeps a tenant. A pass
	// that changes nothing ends the sweep; another pass is needed only when an
	// assignment validated before the role was marked Deleting committed while
	// the previous pass was running.
	maxRevokePasses = 8

	// maxRevokeAttempts bounds the compare-and-swap retries on one account
	// that keeps losing races to concurrent edits.
	maxRevokeAttempts = 8
)

// roleRevoker is the store-level revocation both kvUserStore and
// routingUserService implement. It is unexported so that UserServicer, and
// every double of it, stays unchanged; NewRoleRevoker is the way in.
type roleRevoker interface {
	RevokeRole(ctx context.Context, tenantID, roleID string) (int, error)
}

var (
	_ roleRevoker = (*kvUserStore)(nil)
	_ roleRevoker = (*routingUserService)(nil)
)

// RoleRevoker strips a role from every account that holds it. It satisfies
// role.HolderRevoker, which is how the role store's DeleteRole reaches the
// accounts without importing this package.
type RoleRevoker struct {
	r roleRevoker
}

// NewRoleRevoker returns the RoleRevoker for svc, which must be a store built
// by NewStore or NewRoutingService. Revocation has to commit against the exact
// bytes it read — an edit through UpdateUser re-reads the record and would
// overwrite a role assigned in between — so only the store itself can do it,
// and any other UserServicer is refused with *RoleRevokerUnsupportedError.
func NewRoleRevoker(svc UserServicer) (*RoleRevoker, error) {
	r, ok := svc.(roleRevoker)
	if !ok {
		return nil, &RoleRevokerUnsupportedError{}
	}
	return &RoleRevoker{r: r}, nil
}

// RevokeRole removes roleID from every account in tenantID that holds it and
// reports how many accounts it changed.
func (v *RoleRevoker) RevokeRole(ctx context.Context, tenantID, roleID string) (int, error) {
	return v.r.RevokeRole(ctx, tenantID, roleID)
}

// RevokeRole routes the revocation to the store that holds tenantID's
// accounts. A role's holders always live in the role's own tenant: role
// assignment refuses a role from another tenant, and NormalizeRoleBindings
// refuses a binding outside the account's tenant.
func (r *routingUserService) RevokeRole(ctx context.Context, tenantID, roleID string) (int, error) {
	svc, err := r.storeFor(ctx, tenantid.Normalize(tenantID))
	if err != nil {
		return 0, err
	}
	rv, ok := svc.(roleRevoker)
	if !ok {
		return 0, &RoleRevokerUnsupportedError{}
	}
	return rv.RevokeRole(ctx, tenantID, roleID)
}

// RevokeRole removes roleID from the RoleIDs and RoleBindings of every account
// in tenantID and reports how many accounts it changed.
//
// It sweeps the tenant index until a pass changes nothing, so an account
// created, edited or deleted while it runs is either stripped or was never a
// holder. Each account is rewritten with a compare-and-swap against the bytes
// just read, so a concurrent edit is never overwritten — the strip is re-read
// and re-applied instead. An account being deleted (a terminal lifecycle) is
// left alone: it refuses every edit and its record is on its way out.
//
// It is idempotent. A retry after a failure finds the accounts already
// stripped unchanged and carries on with the rest.
func (s *kvUserStore) RevokeRole(ctx context.Context, tenantID, roleID string) (int, error) {
	if roleID == "" {
		return 0, &ValidationError{Field: "role_id", Reason: "must not be empty"}
	}
	prefix := keyPrefixTenant + tenantid.Normalize(tenantID) + "/"

	total := 0
	for range maxRevokePasses {
		ids, err := s.tenantUserIDs(ctx, prefix)
		if err != nil {
			return total, err
		}
		changed := 0
		for _, id := range ids {
			ok, err := s.revokeRoleFrom(ctx, id, roleID)
			if err != nil {
				return total + changed, err
			}
			if ok {
				changed++
			}
		}
		total += changed
		if changed == 0 {
			return total, nil
		}
	}
	return total, &RoleRevocationIncompleteError{RoleID: roleID, Passes: maxRevokePasses}
}

// tenantUserIDs lists the account ids under one tenant-index prefix.
func (s *kvUserStore) tenantUserIDs(ctx context.Context, prefix string) ([]uint64, error) {
	var ids []uint64
	var decErr error
	err := s.store.Scan(ctx, prefix, func(_ string, value []byte) error {
		id, err := decodeID(value)
		if err != nil {
			decErr = err
			return err
		}
		ids = append(ids, id)
		return nil
	})
	if decErr != nil {
		return nil, &StoreError{Op: "revoke role", Detail: "decode tenant index value", Err: decErr}
	}
	if err != nil {
		return nil, &StoreError{Op: "revoke role", Detail: "scan tenant index", Err: err}
	}
	return ids, nil
}

// revokeRoleFrom strips roleID from one account and reports whether this call
// changed it. A lost race is retried against a fresh read; an ambiguous commit
// is re-read too, and finding the role already gone then means it landed. An
// account still contended after maxRevokeAttempts reports the last failure.
func (s *kvUserStore) revokeRoleFrom(ctx context.Context, id uint64, roleID string) (bool, error) {
	ambiguous := false
	var lastErr error
	for range maxRevokeAttempts {
		base, err := s.store.Get(ctx, primaryKey(id))
		if err != nil {
			return false, &StoreError{Op: "revoke role", Detail: "primary lookup", Err: err}
		}
		if base == nil {
			return false, nil
		}
		req, err := revokeRequest(id, base, roleID)
		if err != nil {
			return false, err
		}
		if req == nil {
			return ambiguous, nil
		}
		_, lastErr = s.updateUserAt(ctx, req, base)
		var conflict *ConcurrentUpdateError
		var unknown *CommitOutcomeUnknownError
		switch {
		case lastErr == nil:
			return true, nil
		case errors.As(lastErr, &unknown):
			ambiguous = true
		case !errors.As(lastErr, &conflict):
			return false, lastErr
		}
	}
	return false, lastErr
}

// revokeRequest builds the update that strips roleID from the account encoded
// in base, or returns nil when there is nothing to strip.
func revokeRequest(id uint64, base []byte, roleID string) (*UpdateUserRequest, error) {
	u := &User{}
	if err := json.Unmarshal(base, u); err != nil {
		return nil, &StoreError{Op: "revoke role", Detail: "unmarshal", Err: err}
	}
	if u.Lifecycle.Terminal() {
		return nil, nil
	}
	req := &UpdateUserRequest{ID: id}
	if slices.Contains(u.RoleIDs, roleID) {
		req.RoleIDs = slices.DeleteFunc(slices.Clone(u.RoleIDs), func(r string) bool { return r == roleID })
	}
	if slices.ContainsFunc(u.RoleBindings, func(b RoleBinding) bool { return b.RoleID == roleID }) {
		req.RoleBindings = slices.DeleteFunc(slices.Clone(u.RoleBindings), func(b RoleBinding) bool { return b.RoleID == roleID })
	}
	if req.RoleIDs == nil && req.RoleBindings == nil {
		return nil, nil
	}
	return req, nil
}
