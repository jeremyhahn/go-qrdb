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

import (
	"context"
	"errors"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// WithIdentityIndex records every role id this store creates in the
// system-plane tenant.IdentityIndex, and removes it again on delete.
//
// The index is what lets a caller holding only a globally unique role id be
// routed to the tenant store that actually holds the record — see
// routingRoleService. It is an Option rather than a required dependency
// because a single-tenant embedding has nothing to route.
//
// As in pkg/user, the index is maintained INSIDE CreateRole and DeleteRole
// alongside the tenant and name indexes: one function writes the record and
// its entry, so the two cannot disagree.
// rollbackTimeout bounds compensation after a failed create. It is short
// because the work is three deletes and a claim release against a store the
// caller was just talking to, and unbounded cleanup on a detached context
// would outlive the request that needed it.
const rollbackTimeout = 10 * time.Second

// reconcileTimeout bounds reconcileAmbiguousCreate's read-back after a
// PutAllIfAllUnchanged call whose outcome is unknown. Same reasoning as
// rollbackTimeout: the read-back deliberately outlives the request's own
// context (a done ctx is the common reason the outcome went unknown in the
// first place), so it needs a deadline of its own. It is also the default
// RETRY window: cmd/qrdbd's boot-time stores read via StaleRead (local
// applied state, not linearizable ReadIndex), so a proposal that committed
// but lost its response may not be locally visible for a brief window while
// apply catches up — see reconcileAmbiguousCreate's doc.
const reconcileTimeout = 5 * time.Second

// defaultReconcilePollInterval is the default spacing between
// reconcileAmbiguousCreate's retries. Short relative to reconcileTimeout: a
// real local-apply catch-up is expected to resolve in single-digit
// milliseconds, so this trades a few wasted reads for not adding meaningful
// latency to the (rare) ambiguous-outcome path.
const defaultReconcilePollInterval = 10 * time.Millisecond

func WithIdentityIndex(index tenant.IdentityIndex) Option {
	return func(s *kvRoleStore) { s.identity = index }
}

// WithReconcilePolling overrides reconcileAmbiguousCreate's poll interval
// and total retry window (both default from defaultReconcilePollInterval and
// reconcileTimeout when left zero). Exists for tests that deliberately model
// a write that will NEVER appear (a genuinely failed proposal, not a
// StaleRead visibility lag) and would otherwise have to wait out the full
// production window to observe the failure path. Production callers have no
// reason to set this.
func WithReconcilePolling(interval, window time.Duration) Option {
	return func(s *kvRoleStore) {
		s.reconcilePollInterval = interval
		s.reconcileWindow = window
	}
}

// WithAtomicIdentity asserts that the store this option is applied to and
// the store the configured tenant.IdentityIndex resolves to are the EXACT
// SAME physical store, and switches CreateRole to commit the identity
// claim and its remaining writes as one guarded batch (see createRoleAtomic)
// instead of the default claim-then-write sequence a Raft leadership change
// can interrupt between any two steps of (go-qrdb#555).
//
// This is an assertion, not something NewStore can verify on its own: an
// IdentityIndex built with tenant.NewIdentityIndex is confined to a store
// fixed at ITS OWN construction, which this package cannot compare against
// the store passed here for identity. Two composers are positioned to know
// the assertion holds and set this option themselves:
//
//   - A boot-time composer wiring firstboot's system-tenant stores from one
//     shared, namespaced KVStore.
//   - routingRoleService, but ONLY for the SYSTEM tenant's own resolved
//     store: tenant.NewResolvedIdentityIndex always resolves its backing
//     store via resolver.Resolve(ctx, tenantid.System)
//     (go-qrdb#555 review, round 3), which is the EXACT SAME call — and,
//     through the resolver's own namespace cache, the exact same physical
//     object — that resolving the system tenant for an ordinary role
//     operation produces. It is NOT set for any other tenant: a
//     barrier-isolated or prefix tenant's own resolved store is a DIFFERENT
//     physical object than the system tenant's, and folding a claim
//     prepared against the wrong store into a batch addressed to this one
//     would silently misroute the claim through the wrong namespace
//     confinement instead of raising an error.
//
// No other caller should set this. NewStore has no way to detect the misuse
// of setting it over a mismatched store, which is why the precondition is
// documented rather than checked.
//
// NewStore refuses construction unless WithIdentityIndex is also set and
// the store implements kvstore.MultiGuardedBatchKVStore — see NewStore's doc.
func WithAtomicIdentity() Option {
	return func(s *kvRoleStore) { s.atomicIdentity = true }
}

// recordIdentity places a role id in its tenant. It is a no-op when no
// index is configured.
// recordIdentity claims an id for its tenant and returns the claim TOKEN. It
// is a no-op returning an empty token when no index is configured.
//
// The token identifies THIS claim, not merely its tenant, so a later release
// cannot delete a claim some other generation created. The caller keeps it
// for the duration of the operation.
func (s *kvRoleStore) recordIdentity(ctx context.Context, id string, tenantID string) (string, error) {
	if s.identity == nil {
		return "", nil
	}
	return s.identity.Record(ctx, tenant.IdentityRole, id, tenantID)
}

// forgetIdentity releases the claim this tenant holds on an id.
//
// It reads the claim and releases it on THAT claim's token, so a release can
// never delete a claim a concurrent recreate established: the guard refuses on
// a token mismatch. A refusal is not this caller's failure — the claim it was
// releasing is already gone or already replaced — so a stale mismatch is
// reported as done. Anything else propagates.
func (s *kvRoleStore) forgetIdentity(ctx context.Context, id string, tenantID string) error {
	if s.identity == nil {
		return nil
	}
	claim, held, err := s.identity.Lookup(ctx, tenant.IdentityRole, id)
	if err != nil {
		return err
	}
	if !held {
		return nil
	}
	if tenantid.IsSystem(tenantID) {
		tenantID = tenantid.System
	}
	if claim.Tenant != tenantID {
		return &tenant.IdentityClaimConflictError{Kind: tenant.IdentityRole, ID: id}
	}
	if ferr := s.identity.Forget(ctx, tenant.IdentityRole, id, claim.Token); ferr != nil {
		var stale *tenant.IdentityClaimStaleError
		if errors.As(ferr, &stale) {
			return nil
		}
		return ferr
	}
	return nil
}

// rollbackFailedCreate undoes a create that failed partway: the keys it
// actually wrote, and then the claim.
//
// Removing the record writes matters as much as releasing the claim. A create
// that failed on its SECOND write left the primary record behind, and the
// retry — reaching the per-tenant duplicate-id check before the claim — was
// refused by its own orphan. Releasing the claim alone therefore did not make
// a caller-supplied id reusable; it only moved which check refused it.
//
// Three properties, each of which a simpler version got wrong:
//
//   - ONLY KEYS THIS CALL WROTE. Deleting the full possible key set looks
//     safe because every key was verified free before the first write, and is
//     not: the NAME key is shared by value, so two concurrent creates of the
//     same name in one tenant can both pass the uniqueness check, and the one
//     that fails would delete the winner's index entry. The id-bearing keys
//     cannot collide that way, but tracking what was written costs nothing
//     and removes the reasoning entirely.
//   - SECONDARY INDEXES BEFORE THE PRIMARY, for the reason DeleteRole
//     removes it last: while the primary exists the record is still findable,
//     so a later delete can finish the cleanup. Removing it first and then
//     failing strands whatever is left.
//   - THE CLAIM ONLY IF THE CLEANUP FULLY SUCCEEDED. Releasing it over a
//     partial record would make the id claimable while that record exists —
//     the same window DeleteRole refuses to open.
func (s *kvRoleStore) rollbackFailedCreate(ctx context.Context, written []string, id, tenantID string, claimToken string) {
	// Compensation runs on a context detached from the caller's cancellation.
	// The failure being compensated is often a timeout or a cancelled request,
	// and cleanup issued on that same context fails immediately at every step
	// — leaving exactly the claim this exists to release. Values are
	// preserved, so routing and audit still see what they need.
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancel()

	clean := true
	for _, key := range written {
		if err := s.store.Delete(cleanupCtx, key); err != nil {
			clean = false
			s.emitAudit(cleanupCtx, "create_rollback_incomplete", "role:"+id, map[string]any{
				"tenantID": tenantID,
				"key":      key,
				"error":    err.Error(),
			})
		}
	}
	if !clean {
		// A partial record remains. Keeping the claim keeps the id pointed at
		// this tenant, which is where the leftovers are, rather than freeing
		// it for another tenant to take while they sit there.
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
func (s *kvRoleStore) releaseClaimAfterFailedCreate(ctx context.Context, id string, claimToken string) {
	if s.identity == nil || claimToken == "" {
		return
	}
	err := s.identity.Forget(ctx, tenant.IdentityRole, id, claimToken)
	if err == nil {
		return
	}
	var stale *tenant.IdentityClaimStaleError
	if errors.As(err, &stale) {
		return
	}
	s.emitAudit(ctx, "create_claim_leaked", "role:"+id, map[string]any{
		"error": err.Error(),
	})
}
