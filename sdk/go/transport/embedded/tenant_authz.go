// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package embedded

import (
	"context"

	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// This file is the embedded transport's half of the tenant-administration
// authorization surface (sdk/go/transport/embedded/tenant.go and
// tenant_seal.go): the probe that found the embedded transport performing no
// tenancy or permission check on GetTenant, ListTenants, UpdateTenant,
// DeleteTenant, ReconcileDeletedTenants or the TenantBarrier* operations.
//
// Every helper below delegates to the SAME *authmw.AuthMiddleware decision
// the network transports enforce (pkg/auth/middleware), resolved against the
// SAME pkg/permission operation table -- there is no second, embedded-only
// permission table. AuthorizeTarget is the method pkg/auth/middleware/target.go
// documents as "the embedded transport authorizes its typed calls through
// this": the embedded transport has no router to run an entry-level check
// ahead of a handler, so each helper here carries the FULL decision (rate
// limit charge and audit record) that a network transport splits between its
// route middleware and a handler-level re-check.
//
// A nil t.authMW means no AuthMiddleware was installed -- the node runs with
// RBAC disabled, matching --auth-enabled=false on every network transport --
// and every helper here is a deliberate no-op in that case, exactly as
// AuthMiddleware-gated role-assignment writes already are (rbac.go,
// transport_auth.go).

// authorizeTenantSelf authorizes perm against the caller's own (ambient)
// tenant: the check for an operation that names no target tenant of its own
// (CreateTenant, ListTenants' entry check, ListDeletedTenants,
// ReconcileDeletedTenants). For a ScopeCross permission -- every one of
// those four -- the target is irrelevant to the decision; the RBAC
// authorizer admits only a system-tenant identity regardless of what tenant
// context names. For a ScopeSelf permission it is the ordinary "act on
// yourself" check.
func (t *Transport) authorizeTenantSelf(ctx context.Context, perm permission.Permission) error {
	if t.authMW == nil {
		return nil
	}
	return t.authMW.AuthorizeTarget(ctx, perm, authmw.SelfTarget())
}

// authorizeTenantTarget authorizes perm against the tenant the request
// NAMES, not the caller's ambient tenant: the re-check every tenant-record
// operation (get, update, delete, quotas, entitlements) and every
// ScopeTenantOnly barrier operation needs, because a tenant-local Admin's
// ScopeSelf grant is otherwise checked against itself and would admit it on
// ANY tenant id the request carries -- exactly the probe scenario (a caller
// bound to tenant "acme" deleting "globex" or "system").
func (t *Transport) authorizeTenantTarget(ctx context.Context, tenantID string, perm permission.Permission) error {
	if t.authMW == nil {
		return nil
	}
	return t.authMW.AuthorizeTarget(ctx, perm, authmw.TenantTarget(tenantID))
}

// authorizeTenantUpdate authorizes a tenant PATCH the same way
// AuthMiddleware.AuthorizeTenantUpdate does on every network transport:
// tenants:update against the named tenant, plus tenants:set_isolation (a
// platform-operator permission) when the patch also changes the isolation
// tier.
func (t *Transport) authorizeTenantUpdate(ctx context.Context, tenantID string, patch tenant.TenantPatch) error {
	if t.authMW == nil {
		return nil
	}
	return t.authMW.AuthorizeTenantUpdate(ctx, tenantID, patch)
}

// authorizeTenantBarrierInit authorizes a tenant barrier initialization
// through the shared trust-on-first-init bootstrap rule
// (AuthMiddleware.AuthorizeTenantBarrierInit): the full tenant_barrier:init
// RBAC check, with the one-time fallback that admits the tenant's own
// identity before any role granting that permission could structurally
// exist.
func (t *Transport) authorizeTenantBarrierInit(ctx context.Context, tenantID string) error {
	if t.authMW == nil {
		return nil
	}
	return t.authMW.AuthorizeTenantBarrierInit(ctx, tenantID, t.tenantSvc)
}

// authorizeTenantBarrierSealedOp authorizes a tenant barrier operation that
// must stay reachable while the tenant's own IAM is unreachable (status,
// unseal) through the shared sealed-window rule
// (AuthMiddleware.AuthorizeTenantBarrierSealedOp).
func (t *Transport) authorizeTenantBarrierSealedOp(ctx context.Context, tenantID string, perm permission.Permission) error {
	if t.authMW == nil {
		return nil
	}
	return t.authMW.AuthorizeTenantBarrierSealedOp(ctx, tenantID, perm)
}

// filterTenantList narrows tenants to what the caller may enumerate
// (AuthMiddleware.FilterTenantList): every tenant for a caller holding
// tenants:list:cross, and only the caller's own tenant for anyone else. A nil
// t.authMW (RBAC disabled) returns tenants unfiltered, matching every other
// helper in this file.
func (t *Transport) filterTenantList(ctx context.Context, tenants []tenant.TenantInfo) ([]tenant.TenantInfo, error) {
	if t.authMW == nil {
		return tenants, nil
	}
	return t.authMW.FilterTenantList(ctx, tenants)
}

// TenantBarrierQuorumBegin and TenantBarrierQuorumSubmit carry no RBAC
// permission of their own -- a Security Officer or custodian authenticates
// through the quorum factor it presents (a certificate, a PIN), not an RBAC
// role, exactly as the per-tenant share-claim handshake does. They are
// IdentityGated on every network transport and "public redeem" on this one:
// what they must still do, which they did not do at all before this file,
// is mark ctx with the SAME claimContext provenance the share-claim
// handshake marks it with, so the seal/ceremony layer underneath sees a
// presented certificate or the in-process marker exactly as it would for a
// claim -- see tenant.go's calls to claimContext for TenantBarrierQuorumBegin
// and TenantBarrierQuorumSubmit.
