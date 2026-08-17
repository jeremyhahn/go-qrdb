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
	m.auditDecision(ctx, caller, permission.TenantBarrierInit, nil)
	return nil
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

	// Never for the system tenant — see AuthorizeTenantBarrierInit.
	if tenant.IsSystemTenant(tenantID) {
		return rbacErr
	}
	caller := auth.IdentityFromContext(ctx)
	if caller == nil || caller.TenantID != tenantID || !IdentityIAMSealed(caller) {
		return rbacErr
	}
	// The RBAC denial above was already audited; record the final decision
	// too so the audit trail shows the request was ultimately allowed.
	m.auditDecision(ctx, caller, perm, nil)
	return nil
}
