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
	"errors"
	"strconv"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// WithIdentityIndex records every user id this store creates in the
// system-plane tenant.IdentityIndex, and removes it again on delete.
//
// The index is what lets a caller holding only a globally unique user id be
// routed to the tenant store that actually holds the record — see
// routingUserService. It is an Option rather than a required dependency
// because a single-tenant embedding has nothing to route: there, the
// caller's tenant is always the record's tenant.
//
// The index is maintained INSIDE CreateUser and DeleteUser, alongside the
// tenant and username indexes, deliberately. An entry kept in step by a
// sibling caller — the router, a service layer, a bootstrap path — drifts
// from the record it describes the first time some other path writes a
// user, and every occurrence of that class in this repository began exactly
// that way. One function writes the record and its entry, or the two can
// disagree.
func WithIdentityIndex(index tenant.IdentityIndex) Option {
	return func(s *kvUserStore) { s.identity = index }
}

// identityID renders a user id as the identity index's key segment. User
// ids are decimal here rather than the fixed-width big-endian encoding the
// user store's own keys use: the identity index is not scanned in id
// order, and the CLI and every transport name a user by its decimal id.
func identityID(id uint64) string {
	return strconv.FormatUint(id, 10)
}

// recordIdentity places a user id in its tenant. It is a no-op when no
// index is configured.
// recordIdentity claims an id for its tenant and returns the claim TOKEN. It
// is a no-op returning an empty token when no index is configured.
//
// The token identifies THIS claim, not merely its tenant, so a later release
// cannot delete a claim some other generation created. The caller keeps it
// for the duration of the operation.
func (s *kvUserStore) recordIdentity(ctx context.Context, id uint64, tenantID string) (string, error) {
	if s.identity == nil {
		return "", nil
	}
	return s.identity.Record(ctx, tenant.IdentityUser, identityID(id), tenantID)
}

// forgetIdentity releases the claim this tenant holds on an id.
//
// It reads the claim and releases it on THAT claim's token, so a release can
// never delete a claim a concurrent recreate established: the guard refuses on
// a token mismatch. A refusal is not this caller's failure — the claim it was
// releasing is already gone or already replaced — so a stale mismatch is
// reported as done. Anything else propagates.
func (s *kvUserStore) forgetIdentity(ctx context.Context, id uint64, tenantID string) error {
	if s.identity == nil {
		return nil
	}
	claim, held, err := s.identity.Lookup(ctx, tenant.IdentityUser, identityID(id))
	if err != nil {
		return err
	}
	if !held {
		return nil
	}
	if tenant.IsSystemTenant(tenantID) {
		tenantID = tenant.SystemTenantID
	}
	if claim.Tenant != tenantID {
		return &tenant.IdentityClaimConflictError{Kind: tenant.IdentityUser, ID: identityID(id)}
	}
	if ferr := s.identity.Forget(ctx, tenant.IdentityUser, identityID(id), claim.Token); ferr != nil {
		var stale *tenant.IdentityClaimStaleError
		if errors.As(ferr, &stale) {
			return nil
		}
		return ferr
	}
	return nil
}

// rollbackTimeout bounds compensation after a failed create. See the role
// store's equivalent: short, because the work is a few deletes and a claim
// release against a store the caller was just talking to.
const rollbackTimeout = 10 * time.Second

// rollbackFailedCreate undoes a create that failed partway: the keys it
// actually wrote, and then the claim. See the equivalent in pkg/role for the
// three properties this shape exists to satisfy — only keys THIS call wrote,
// secondary indexes before the primary, and the claim released only when the
// record cleanup fully succeeded.
//
// A create failing on its second write otherwise leaves an orphan primary
// record that no retry and no delete can reach, because the username index
// the retry keys on was never written.
func (s *kvUserStore) rollbackFailedCreate(ctx context.Context, written []string, id uint64, tenantID string, claimToken string) {
	// Detached from the caller's cancellation: the failure being compensated
	// is often a timeout, and cleanup on that same context fails at every
	// step, leaving exactly the claim this exists to release.
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancel()

	clean := true
	for _, key := range written {
		if err := s.store.Delete(cleanupCtx, key); err != nil {
			clean = false
			s.emitAudit(cleanupCtx, "create_rollback_incomplete", id, map[string]any{
				"tenantID": tenantID,
				"key":      key,
				"error":    err.Error(),
			})
		}
	}
	if !clean {
		// Leftovers remain in this tenant. Keeping the claim keeps the id
		// pointed where they are rather than freeing it for another tenant.
		return
	}
	s.releaseClaimAfterFailedCreate(cleanupCtx, id, claimToken)
}

// releaseClaimAfterFailedCreate rolls back the claim a create took before its
// record writes failed, releasing THE CLAIM THAT CREATE WON rather than
// whatever holds the id now. Best effort by necessity: the create is already
// failing, and the error the caller needs is the one that failed the write.
//
// A stale token means the claim is already gone or already replaced, so there
// is nothing of ours to release. Any other failure leaves a claim with no
// record, which blocks that id until an operator clears it, so it goes to the
// audit log where it can be found rather than being dropped.
func (s *kvUserStore) releaseClaimAfterFailedCreate(ctx context.Context, id uint64, claimToken string) {
	if s.identity == nil || claimToken == "" {
		return
	}
	err := s.identity.Forget(ctx, tenant.IdentityUser, identityID(id), claimToken)
	if err == nil {
		return
	}
	var stale *tenant.IdentityClaimStaleError
	if errors.As(err, &stale) {
		return
	}
	s.emitAudit(ctx, "create_claim_leaked", id, map[string]any{
		"error": err.Error(),
	})
}
