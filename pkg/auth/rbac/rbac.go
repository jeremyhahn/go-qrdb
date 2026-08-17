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

// Package rbac implements a role-based access control Authorizer that
// resolves permissions through the role service and caches the result
// for efficient repeated authorization checks.
//
// The authorizer enforces three layered rules in this order:
//
//  1. SO deny-list (FIPS 140-3 AS10.03): an identity holding the SO role is
//     denied every cryptographic operation regardless of any explicit grant —
//     the keys:* crypto verbs (sign, verify, encrypt, decrypt, wrap, unwrap,
//     derive), hardware-token key use (piv:use), and any resource whose action
//     names a cryptographic primitive, which covers permissions contributed by
//     a higher layer that this package cannot enumerate (go-quicbaas's
//     attestation:attest among them).
//  2. Permission resolution: the role service returns the union of all
//     permissions granted by the identity's roles (cached per identity).
//     If the requested permission is not in the union, the request is
//     denied.
//  3. System-tenant rule: a permission is admitted only when the
//     identity's TenantID is consistent with the permission's scope and
//     the request's target tenant. ScopeCross permissions require the
//     identity to be in the system tenant; ScopeSelf permissions admit
//     when the identity's tenant equals the target tenant or the
//     identity is in the system tenant.
//
// The third rule encodes the canonical platform-admin pattern
// "Admin@system" — an Admin role grant carried
// by an identity in the structurally privileged system tenant.
//
// Scope resolution is tenant-aware when a permission.ScopeResolver is
// installed via WithScopeResolver: a caller-defined permission's scope is
// resolved in the identity's own tenant (per-tenant store -> built-in
// default -> ScopeSelf), so two tenants may use the same permission name
// with different scopes without colliding. When no resolver is installed,
// the authorizer falls back to the built-in process-global scope layer
// (permission.LookupScope), preserving the prior behavior.
package rbac

import (
	"context"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

const (
	// defaultCacheTTL is the default time-to-live for cached permission sets.
	defaultCacheTTL = 30 * time.Second

	// resolveSyncTimeout bounds the primary linearizable permission resolve
	// when a StaleRead fallback is configured, so a transient ReadIndex
	// slowdown fails over in well under a second rather than blocking the
	// request. A healthy ReadIndex answers far faster.
	resolveSyncTimeout = 500 * time.Millisecond
)

// soDeniedActions is the set of resource-specific cryptographic actions
// explicitly denied to the SO role regardless of any permission grants (FIPS
// 140-3 AS10.03). It names the operations whose action verb alone does not
// identify them as cryptographic use: "verify" is ambiguous across families
// (backup:verify and objstore:verify are integrity checks over stored bytes)
// and "use" says nothing on its own, so both are pinned to their resource here.
//
// Defense-in-depth: even if an SO role is erroneously granted these
// permissions — by a hand-authored custom role named "so", or by a role store
// written by an earlier build — the authorizer rejects the request at this
// layer. This table is deliberately INDEPENDENT of the registration-time guard
// in pkg/permission (it classifies the requested resource:action itself rather
// than consulting the duty registry) so that a defect in one mechanism does not
// disable the other. TestSODenyList_AgreesWithPermissionDutyRegistry proves the
// two agree.
var soDeniedActions = map[string]map[string]struct{}{
	"keys": {
		"sign":    {},
		"encrypt": {},
		"decrypt": {},
		"verify":  {},
		"wrap":    {},
		"unwrap":  {},
		"derive":  {},
	},
	"piv": {
		// "use a PIV slot key for cryptographic operations" — keys:sign
		// reached through a smart card. The SO administers PIV slots
		// (piv:read / piv:write / piv:delete) but never uses their keys.
		"use": {},
	},
}

// soDeniedCryptoVerbs is the resource-independent half of the deny-list: action
// verbs that name a cryptographic primitive performed with key material in any
// resource family. It is what makes this layer complete for permissions this
// package cannot enumerate — a higher layer (go-quicbaas, go-xkms) contributes
// its own resources to the shared RBAC vocabulary, and go-quicbaas's
// attestation:attest (extend a PCR, return a signed TPM quote) must be denied
// to the SO without qrdb knowing the resource exists.
//
// The set mirrors permission.cryptoPrimitiveActions and excludes the same
// ambiguous verbs, for the same reasons: "verify" (integrity checks),
// "seal"/"unseal" (the SO's own barrier lifecycle) and "generate" (issuance and
// credential minting) are NOT cryptographic use.
var soDeniedCryptoVerbs = map[string]struct{}{
	"sign":    {},
	"encrypt": {},
	"decrypt": {},
	"wrap":    {},
	"unwrap":  {},
	"derive":  {},
	"kdf":     {},
	"mac":     {},
	"hmac":    {},
	"attest":  {},
	"quote":   {},
}

// isSODenied reports whether the given resource+action combination is a
// cryptographic operation denied to the SO role per FIPS 140-3 AS10.03. Both
// halves are O(1) map lookups: the resource-independent verb set first, then the
// resource-specific table.
func isSODenied(resource, action string) bool {
	if _, denied := soDeniedCryptoVerbs[action]; denied {
		return true
	}
	if actions, ok := soDeniedActions[resource]; ok {
		_, denied := actions[action]
		return denied
	}
	return false
}

// permissionGranted reports whether the resolved permission set admits the
// requested resource:action, honoring segment wildcards. A granted permission
// matches when it is the exact "resource:action", a resource wildcard
// "resource:*", an action wildcard "*:action", or the full wildcard "*:*".
// Only whole-segment "*" wildcards are recognized (not glob prefixes), so the
// check is a bounded O(1) lookup against the set rather than an iteration. A
// role that grants "keys:*" therefore satisfies a concrete "keys:sign" check
// while the built-in roles continue to use concrete permissions.
func permissionGranted(permSet map[permission.Permission]struct{}, resource, action string) bool {
	candidates := [...]permission.Permission{
		permission.Permission(resource + ":" + action),
		permission.Permission(resource + ":*"),
		permission.Permission("*:" + action),
		permission.Permission("*:*"),
	}
	for _, c := range candidates {
		if _, ok := permSet[c]; ok {
			return true
		}
	}
	return false
}

// RBACAuthorizer implements auth.Authorizer using role-based access control.
// Permissions are resolved through the role service and cached per identity
// with a configurable TTL.
type RBACAuthorizer struct {
	roles role.RoleServicer
	cache *permissionCache

	// strictSoD enforces strict separation of duties. When true, the Owner
	// convenience role (role.RoleNameOwner) is treated as inert: its grants
	// are stripped from every identity before permission resolution. This is
	// defense-in-depth — even if a role store was bootstrapped under the
	// standard posture and assigned Owner to the admin, a strict (FIPS 140-3
	// / auth.separation_of_duties) authorizer never honors it. The six
	// canonical roles are unaffected.
	strictSoD bool

	// rolesFallback is an optional StaleRead-backed role store consulted
	// when the primary (linearizable SyncRead) role store fails to resolve
	// permissions with a transient error — e.g. the Raft ReadIndex pipeline
	// is not yet warm, or the host is draining and rejects linearizable
	// reads. Role data is replicated before servers start, so a stale read
	// is safe for authorization. This mirrors the user-store fallback in
	// pkg/auth/middleware and keeps control-plane operations (such as Drain)
	// authorizable on a draining node. Nil disables the fallback.
	rolesFallback role.RoleServicer

	// scopes is an optional tenant-aware scope resolver. When non-nil, the
	// authorizer resolves a permission's scope in the identity's own tenant
	// (per-tenant store -> built-in default -> ScopeSelf) so that two
	// tenants may use the same permission name with different scopes without
	// colliding. When nil, the authorizer consults only the built-in
	// process-global scope layer via permission.LookupScope (back-compat).
	//
	// SECURITY: when scopes is set and ResolveScope returns an error — most
	// importantly tenant.ErrTenantSealed for a sealed barrier tenant — the
	// request is denied (fail safe). The scope is never defaulted to an
	// admitting value on error.
	scopes permission.ScopeResolver
}

// Option configures an RBACAuthorizer.
type Option func(*RBACAuthorizer)

// WithCacheTTL sets the time-to-live for cached permission entries.
// The default is 30 seconds.
func WithCacheTTL(ttl time.Duration) Option {
	return func(a *RBACAuthorizer) {
		a.cache = newPermissionCache(ttl)
	}
}

// WithStrictSeparation enables (true) or disables (false) strict separation
// of duties. Default is false (standard posture, Owner role honored). The
// daemon sets this to crypto/fips140.Enabled() || config separation flag.
func WithStrictSeparation(strict bool) Option {
	return func(a *RBACAuthorizer) {
		a.strictSoD = strict
	}
}

// WithRoleServiceFallback installs a StaleRead-backed role store used to
// resolve permissions when the primary linearizable read fails with a
// transient error (ReadIndex not warm, host draining). See the rolesFallback
// field for the safety rationale.
func WithRoleServiceFallback(fallback role.RoleServicer) Option {
	return func(a *RBACAuthorizer) {
		a.rolesFallback = fallback
	}
}

// WithScopeResolver installs a tenant-aware permission.ScopeResolver
// (typically a *permission.TenantScopeStore). When set, Authorize resolves
// each permission's scope in the identity's own tenant rather than from the
// process-global built-in layer, so caller-defined per-tenant permissions
// cannot collide on a shared name. A scope-resolution error fails safe
// (the request is denied). Passing nil leaves the back-compat behavior in
// place.
func WithScopeResolver(resolver permission.ScopeResolver) Option {
	return func(a *RBACAuthorizer) {
		a.scopes = resolver
	}
}

// New creates a new RBACAuthorizer backed by the given role service.
func New(roles role.RoleServicer, opts ...Option) *RBACAuthorizer {
	a := &RBACAuthorizer{
		roles: roles,
		cache: newPermissionCache(defaultCacheTTL),
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// effectiveRoles returns the identity's roles with the Owner convenience role
// removed when strict separation of duties is active. Owner may be referenced
// either by its name (role.RoleNameOwner) or by its deterministic per-tenant
// ID (role.SystemRoleID(tenantID, RoleNameOwner)); both forms are stripped.
// In the standard posture the roles are returned unchanged.
func (a *RBACAuthorizer) effectiveRoles(identity *auth.Identity) []string {
	if !a.strictSoD {
		return identity.Roles
	}
	ownerID := role.SystemRoleID(identity.TenantID, role.RoleNameOwner)
	out := make([]string, 0, len(identity.Roles))
	for _, r := range identity.Roles {
		if r == role.RoleNameOwner || r == ownerID {
			continue
		}
		out = append(out, r)
	}
	return out
}

// IsSystemTenant reports whether identity is scoped to the structurally
// privileged system tenant. A nil identity is treated as system-scoped:
// the absence of an authenticated identity means the caller is an internal,
// unauthenticated pipeline (platform boot, daemon-internal maintenance),
// which by convention executes with system-tenant privilege — mirroring
// tenant.FromContext's fallback for a context with no bound tenant. A
// non-nil identity delegates to tenant.IsSystemTenant, the single source of
// truth for the string-level comparison; this function does not re-derive
// that comparison itself.
//
// It lives in this package (rather than pkg/auth, where Identity is
// defined) because pkg/auth cannot import pkg/tenant without closing an
// import cycle through pkg/ceremony; pkg/auth/rbac already depends on both
// and is where the system-tenant rule this predicate encodes is enforced.
func IsSystemTenant(identity *auth.Identity) bool {
	if identity == nil {
		return true
	}
	return tenant.IsSystemTenant(identity.TenantID)
}

// resolveScope returns the scope governing the given permission for the
// identity's tenant. When a tenant-aware resolver is installed it is
// consulted (fail safe on error); otherwise the built-in process-global
// layer is used. The error is non-nil only when the tenant-aware resolver
// fails — in which case the caller MUST deny.
func (a *RBACAuthorizer) resolveScope(ctx context.Context, identity *auth.Identity, p permission.Permission) (permission.Scope, error) {
	if a.scopes == nil {
		return permission.LookupScope(p), nil
	}
	return a.scopes.ResolveScope(ctx, identity.TenantID, p)
}

// Authorize checks whether the given identity has permission to perform
// the specified action on the resource. It returns nil if the action is
// allowed, auth.ErrUnauthenticated if the identity is nil, or a
// PermissionDeniedError (wrapping auth.ErrUnauthorized) if denied.
//
// The target tenant is derived from the request context via
// tenant.FromContext(ctx). When no tenant is bound to the context, the
// request is treated as targeting the system tenant — daemon-internal
// maintenance flows execute against system by convention.
//
// Authorization decisions follow the layered rules described in the
// package doc:
//   - SO deny-list (FIPS 140-3 AS10.03) takes precedence over every other
//     check, including identities holding multiple roles.
//   - Permission resolution: the requested resource:action permission must
//     be in the union of permissions granted by the identity's roles.
//   - System-tenant rule: ScopeCross permissions require
//     identity.TenantID == SystemTenantID; ScopeSelf permissions require
//     identity.TenantID == targetTenantID OR
//     identity.TenantID == SystemTenantID. The system tenant grants
//     cross-tenant scope to whatever roles its members hold. The scope is
//     resolved in the identity's own tenant when a tenant-aware resolver is
//     installed (see WithScopeResolver); a resolver error fails safe.
func (a *RBACAuthorizer) Authorize(ctx context.Context, identity *auth.Identity, resource, action string) error {
	if identity == nil {
		return auth.ErrUnauthenticated
	}

	// SO deny-list check (FIPS 140-3 AS10.03): must precede any other
	// admission so that a malicious or erroneous grant cannot override it.
	if isSODenied(resource, action) {
		soRoleID := role.SystemRoleID(identity.TenantID, role.RoleNameSO)
		for _, r := range identity.Roles {
			if r == role.RoleNameSO || r == soRoleID {
				return &PermissionDeniedError{
					Resource: resource,
					Action:   action,
					Subject:  identity.Subject,
					Reason:   "SO role explicitly denied from cryptographic operations (FIPS 140-3 AS10.03)",
				}
			}
		}
	}

	// Resolve the identity's effective permissions, with caching. The
	// cache key includes the effective role set (see permissionCache), so
	// a change in the identity's enriched roles is never masked by a
	// stale entry.
	effRoles := a.effectiveRoles(identity)
	permSet, ok := a.cache.get(identity.TenantID, identity.Subject, effRoles)
	if !ok {
		// Bound the primary linearizable resolve when a StaleRead fallback is
		// available, so a SLOW (not just failed) ReadIndex — cold pipeline,
		// draining host, transient consensus latency — fails over within
		// resolveSyncTimeout instead of blocking the whole request until its
		// outer deadline. A healthy ReadIndex answers in microseconds; the
		// only cost of the short budget is a marginally-staler read on a blip,
		// which is safe for replicated role data. Mirrors the auth user-lookup
		// guard and is the companion to the error-triggered fallback below.
		primaryCtx := ctx
		var cancel context.CancelFunc
		if a.rolesFallback != nil {
			primaryCtx, cancel = context.WithTimeout(ctx, resolveSyncTimeout)
		}
		perms, err := a.roles.ResolvePermissions(primaryCtx, effRoles)
		if cancel != nil {
			cancel()
		}
		if err != nil {
			// Primary (linearizable) resolution failed or timed out. Retry
			// against the StaleRead-backed fallback when available — this keeps
			// authorization working when the ReadIndex pipeline is not yet
			// warm or the host is draining (which rejects linearizable
			// reads). Role data is replicated before servers start, so the
			// stale read is safe. Mirrors the user-store fallback.
			if a.rolesFallback == nil {
				return &ResolveError{Err: err}
			}
			fperms, ferr := a.rolesFallback.ResolvePermissions(ctx, effRoles)
			if ferr != nil {
				return &ResolveError{Err: err}
			}
			perms = fperms
		}
		a.cache.set(identity.TenantID, identity.Subject, effRoles, perms)
		permSet = make(map[permission.Permission]struct{}, len(perms))
		for _, p := range perms {
			permSet[p] = struct{}{}
		}
	}

	// Union in the identity's pre-resolved scope permissions (platform
	// tokens whose subject has no user record — see
	// auth.Identity.ScopePermissions). Additive only: they can grant, never
	// revoke, and the SO deny-list above already had its say. The set was
	// stamped at token issuance by the same permission vocabulary, so no
	// re-mapping is needed.
	if len(identity.ScopePermissions) > 0 {
		merged := make(map[permission.Permission]struct{}, len(permSet)+len(identity.ScopePermissions))
		for p := range permSet {
			merged[p] = struct{}{}
		}
		for _, p := range identity.ScopePermissions {
			merged[permission.Permission(p)] = struct{}{}
		}
		permSet = merged
	}

	required := permission.Permission(resource + ":" + action)
	if !permissionGranted(permSet, resource, action) {
		return &PermissionDeniedError{
			Resource: resource,
			Action:   action,
			Subject:  identity.Subject,
		}
	}

	// System-tenant rule: gate the permission by scope against the
	// identity's tenant and the request's target tenant. The scope is
	// resolved in the identity's own tenant when a tenant-aware resolver is
	// installed; a resolver error (e.g. a sealed barrier tenant) fails safe.
	targetTenantID := tenant.FromContext(ctx)
	inSystem := IsSystemTenant(identity)
	scope, scopeErr := a.resolveScope(ctx, identity, required)
	if scopeErr != nil {
		return &PermissionDeniedError{
			Resource: resource,
			Action:   action,
			Subject:  identity.Subject,
			Reason:   "scope resolution failed: " + scopeErr.Error(),
		}
	}
	switch scope {
	case permission.ScopeCross:
		if !inSystem {
			return &PermissionDeniedError{
				Resource: resource,
				Action:   action,
				Subject:  identity.Subject,
				Reason:   "cross-scope permission requires system tenant",
			}
		}
		return nil
	case permission.ScopeSelf:
		if inSystem || identity.TenantID == targetTenantID {
			return nil
		}
		return &PermissionDeniedError{
			Resource: resource,
			Action:   action,
			Subject:  identity.Subject,
			Reason:   "self-scope permission: identity not in target tenant",
		}
	case permission.ScopeTenantOnly:
		// Strictly tenant-local: the system tenant is denied (the platform
		// operator must not hold tenant key material), and the identity's
		// tenant must equal the request's target tenant (no cross-tenant).
		// Unlike ScopeSelf, the system-tenant rule does NOT lift this.
		if inSystem {
			return &PermissionDeniedError{
				Resource: resource,
				Action:   action,
				Subject:  identity.Subject,
				Reason:   "tenant-only permission denied to system tenant",
			}
		}
		if identity.TenantID != targetTenantID {
			return &PermissionDeniedError{
				Resource: resource,
				Action:   action,
				Subject:  identity.Subject,
				Reason:   "tenant-only permission: identity not in target tenant",
			}
		}
		return nil
	default:
		return &PermissionDeniedError{
			Resource: resource,
			Action:   action,
			Subject:  identity.Subject,
			Reason:   "unknown permission scope",
		}
	}
}

// Invalidate removes the cached permissions for a specific identity,
// forcing a fresh resolution on the next Authorize call.
func (a *RBACAuthorizer) Invalidate(tenantID, subject string) {
	a.cache.invalidate(tenantID, subject)
}

// InvalidateAll clears all cached permission entries.
func (a *RBACAuthorizer) InvalidateAll() {
	a.cache.invalidateAll()
}
