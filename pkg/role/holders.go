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

package role

import "context"

// HolderRevoker strips a role from every account that holds it. DeleteRole
// calls it after marking the role Deleting and before removing the record, so
// a deleted role never survives as a dead identifier on an account.
//
// The user store implements it (user.NewRoleRevoker); this package names only
// the seam, because accounts reference roles and not the other way round.
type HolderRevoker interface {
	// RevokeRole removes roleID from the RoleIDs and RoleBindings of every
	// account in tenantID that holds it, and reports how many accounts this
	// call changed. It must be idempotent: a retry after a partial failure
	// strips what the first attempt did not reach and leaves alone what it
	// already did.
	RevokeRole(ctx context.Context, tenantID, roleID string) (int, error)
}

// WithHolderRevoker gives the store the revoker DeleteRole strips holders
// through. A store built without one refuses DeleteRole with
// *HolderRevokerRequiredError rather than leave the role on its holders.
func WithHolderRevoker(h HolderRevoker) Option {
	return func(s *kvRoleStore) {
		s.holders = h
	}
}
