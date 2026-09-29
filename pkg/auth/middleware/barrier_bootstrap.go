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

package middleware

import (
	"context"
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// This file is the single home of the tenant-barrier BOOTSTRAP authorization
// rules — the two windows in which a dedicated-isolation tenant's own
// authenticated identity substitutes for an RBAC grant that structurally
// cannot exist yet. Every transport (REST, gRPC, gRPC-over-UDS, QUIC, MCP)
// authorizes these operations through the two methods below so the rules
// cannot drift per transport; the corresponding routes/methods/tools are
// registered as permission.IdentityGated and the RBAC check runs here, in
// the handler path, where the fallback can be evaluated.
//
// Window 1 — trust-on-first-init (AuthorizeTenantBarrierInit): a
// self-managed tenant's IAM records are encrypted BEHIND the barrier being
// initialized, so before the first successful init no role able to satisfy
// tenant_barrier:init can exist. The window closes at initialization and
// never reopens.
//
// Window 2 — the sealed window (AuthorizeTenantBarrierSealedOp): on a node
// where the tenant's barrier is sealed (or its key not yet delivered), IAM
// cannot be decrypted, so no caller can hold tenant_barrier:status/unseal —
// yet those are precisely the operations that end the state. The fallback
// is accepted only when the auth middleware itself attested, at
// authentication time, that the tenant's IAM was unreachable (the sealed
// marker stamped by finalizeIdentity); once the barrier is unsealed the
// marker is never stamped and RBAC is the only path.

// TenantBarrierStatusReader reports a tenant barrier's initialization state.
// It is satisfied by tenant.TenantServicer; the narrow interface keeps the
// middleware's dependency to the one call the trust-on-first-init rule needs.
type TenantBarrierStatusReader interface {
	TenantBarrierStatus(ctx context.Context, tenantID string) (*tenant.TenantBarrierStatus, error)
}

// AuthorizeTenantBarrierInit authorizes a tenant barrier initialization.
//
// An INITIALIZED barrier demands the full tenant_barrier:init RBAC check,
// exactly like every other barrier operation. A barrier that has NEVER been
// initialized additionally accepts the tenant's own authenticated identity
// without a role — trust-on-first-init. Possession of a cluster-CA-issued
// mTLS certificate carrying the tenant's identity is the same credential the
// per-tenant officer-claim endpoints (permission.IdentityGated) already
// accept during their own bootstrap handshakes. Re-initialization of an
// initialized barrier requires the RBAC grant (and is refused as
// already-initialized by the service regardless).
func (m *AuthMiddleware) AuthorizeTenantBarrierInit(ctx context.Context, tenantID string, status TenantBarrierStatusReader) error {
	rbacErr := m.AuthorizeTenant(ctx, tenantID, permission.TenantBarrierInit)
	if rbacErr == nil {
		return nil
	}

	// AN AUDIT FAILURE IS NOT AN RBAC VERDICT, so it must not open the
	// bootstrap window.
	//
	// AuthorizeTenant can now return *audit.GateError instead of a decision
	// (go-qrdb#335), and every test below treats a non-nil rbacErr as "RBAC
	// said no" -- so the fallback ran on a request RBAC had never actually
	// refused. It cost a TenantBarrierStatus call, then a SECOND
	// auditDecision: another write to the sink that had just failed, which is
	// the retry this feature forbids, and a second
	// qrdb_auth_audit_write_failures_total{outcome="denied"} increment for ONE
	// decision. That undercuts the counter's stated meaning -- a non-zero rate
	// is meant to be the exact size of the gap in the trail, not a multiple of
	// it.
	//
	// Returning immediately is also the right ANSWER: the caller is refused
	// either way, and *audit.GateError carries "retry", which the RBAC denial it
	// would otherwise be flattened into does not.
	//
	// NARROWLY the AuthFailureAuditUnavailable class, not AuthFailureUnavailable.
	// The sealed-tenant signal (*StoreUnavailableError wrapping
	// tenant.ErrTenantSealed) is exactly what these fallbacks EXIST to act on
	// -- a tenant whose IAM is encrypted behind the barrier it is trying to
	// unseal -- and go-qrdb#460 split it into its own class precisely so this
	// check cannot drift back onto the wide one that used to contain both.
	if isAuditFailure(rbacErr) {
		return rbacErr
	}

	// A RATE-LIMIT REFUSAL IS NOT AN RBAC VERDICT EITHER. The limiter runs
	// before RBAC is consulted, so a *RateLimitedError says nothing about
	// whether the caller holds the grant -- and treating it as a denial let
	// the fallback below issue the grant anyway, which made the policy on
	// this permission unenforceable for exactly the callers the window
	// admits. The limiter's answer is final.
	if isRateLimited(rbacErr) {
		return rbacErr
	}

	// The bootstrap windows exist for PER-TENANT barriers only: the system
	// tenant has no tenant barrier, and the tenant-scoped RBAC rule
	// (ScopeTenantOnly) denies it deliberately — the fallback must never
	// reopen that door.
	if tenant.IsSystemTenant(tenantID) {
		return rbacErr
	}
	caller := auth.IdentityFromContext(ctx)
	if caller == nil || caller.TenantID != tenantID || status == nil {
		return rbacErr
	}
	st, err := status.TenantBarrierStatus(ctx, tenantID)
	if err != nil || st == nil || st.Initialized {
		return rbacErr
	}
	// The RBAC denial above was already audited; record the final decision
	// too so the audit trail shows the request was ultimately allowed.
	//
	// PROPAGATED, not discarded: this line is the bootstrap window issuing a
	// GRANT that RBAC refused, which is the single most consequential
	// authorization decision on this surface. If it cannot be recorded, the
	// configured audit failure policy decides, exactly as it does for every
	// other grant (see auditDecision).
	return m.auditDecision(ctx, caller, permission.TenantBarrierInit, nil)
}

// AuthorizeTenantBarrierSealedOp authorizes the tenant barrier operations
// that must remain reachable while the tenant's IAM itself is unreachable:
// status and unseal (perm is TenantBarrierStatus or TenantBarrierUnseal).
// The full RBAC check runs first and wins whenever it can be evaluated. The
// fallback accepts the tenant's OWN authenticated identity only when the
// middleware stamped it as authenticated during a tenant-IAM outage
// (IdentityIAMSealed): the tenant's roles are encrypted behind the sealed
// (or not-yet-delivered) barrier, so no caller could hold the RBAC grant.
func (m *AuthMiddleware) AuthorizeTenantBarrierSealedOp(ctx context.Context, tenantID string, perm permission.Permission) error {
	rbacErr := m.AuthorizeTenant(ctx, tenantID, perm)
	if rbacErr == nil {
		return nil
	}

	// Not an RBAC verdict, so not a reason to open the sealed window — see
	// AuthorizeTenantBarrierInit for why this precedes every other check, and
	// why it is AuthFailureAuditUnavailable specifically rather than
	// AuthFailureUnavailable (which carries the sealed-tenant signal this
	// fallback acts on).
	if isAuditFailure(rbacErr) {
		return rbacErr
	}

	// Nor is a rate-limit refusal: the sealed window must not grant past the
	// limiter -- see AuthorizeTenantBarrierInit.
	if isRateLimited(rbacErr) {
		return rbacErr
	}

	// Never for the system tenant — see AuthorizeTenantBarrierInit.
	if tenant.IsSystemTenant(tenantID) {
		return rbacErr
	}
	caller := auth.IdentityFromContext(ctx)
	if caller == nil || caller.TenantID != tenantID || !IdentityIAMSealed(caller) {
		return rbacErr
	}
	// The RBAC denial above was already audited; record the final decision
	// too so the audit trail shows the request was ultimately allowed. It is
	// propagated for the same reason as in AuthorizeTenantBarrierInit: this is
	// a grant RBAC refused.
	return m.auditDecision(ctx, caller, perm, nil)
}

// isAuditFailure reports whether err is the middleware refusing because it
// could not RECORD a decision, as opposed to a decision it actually made.
//
// The distinction matters wherever a non-nil error is read as "RBAC said no".
// Both bootstrap fallbacks did exactly that, and an *audit.GateError sent them
// down a path meant for a genuine denial. so_middleware.go's SO-PIN gate has
// the identical hazard and uses this same predicate for the same reason.
//
// THE NARROW AuthFailureAuditUnavailable CLASS, never the wider
// AuthFailureUnavailable it used to be part of. The sealed-tenant signal
// (*StoreUnavailableError wrapping tenant.ErrTenantSealed) is
// AuthFailureUnavailable, not this class, and it is the exact signal these
// fallbacks exist to act on. Before go-qrdb#460 split the class, this function
// was the only thing standing between a future caller and testing the wide
// class directly -- a single, easy, silent regression. Now the type itself
// enforces the distinction: ClassifyAuthError cannot return
// AuthFailureAuditUnavailable for a *StoreUnavailableError, so this function
// (or any other code that tests the class directly) is correct by
// construction rather than by vigilance.
func isAuditFailure(err error) bool {
	return ClassifyAuthError(err) == AuthFailureAuditUnavailable
}

// isRateLimited reports whether err is the middleware's rate limiter refusing
// the request. Like an audit failure it is decided before, and independently
// of, RBAC, so neither bootstrap fallback may read it as a denial it can
// override.
func isRateLimited(err error) bool {
	var rl *RateLimitedError
	return errors.As(err, &rl)
}
