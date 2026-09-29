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
// declaration), so two tenants may use the same permission name with
// different scopes without colliding. When no resolver is installed, the
// authorizer consults the built-in process-global scope layer
// (permission.LookupScope) directly. There is no default at the end of
// either chain: a permission with no declared scope is denied.
package rbac

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
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
	"dek": {
		// Rewrap decrypts a ciphertext and re-encrypts it under the tenant's
		// current key: key use, like keys:wrap. dek:encrypt and dek:decrypt
		// are already caught by the resource-independent verbs below.
		"rewrap": {},
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
	//
	// "Safe for authorization" holds for resolving what a role GRANTS. It
	// does not extend to deciding whether an identifier denotes a system
	// role, because the replica can be behind a role that was deleted and
	// recreated — so the two grant-voiding controls treat a stale answer as
	// unable to clear a grant rather than as proof of innocence. See
	// systemRoleIDs and staleMayDenote.
	rolesFallback role.RoleServicer

	// scopes is an optional tenant-aware scope resolver. When non-nil, the
	// authorizer resolves a permission's scope in the identity's own tenant
	// (per-tenant store -> built-in declaration) so that two tenants may use
	// the same permission name with different scopes without colliding. When
	// nil, the authorizer consults only the built-in process-global scope
	// layer via permission.LookupScope. Neither chain ends in a default.
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

// systemRoleIDs returns every identifier that denotes the named system role
// in tenantID: the canonical one plus, on a cluster seeded before system
// role IDs moved off UUIDv5/SHA-1, the legacy identifier still persisted for
// it.
//
// The result is deliberately NOT cached across requests. Both callers use it
// to VOID a grant — the SO deny-list (FIPS 140-3 AS10.03) and the strict-SoD
// Owner strip — and the store does not preserve the invariant that would make
// a cached answer safe: kvRoleStore.CreateRole persists System: false
// regardless of the request, so a seeded SO or Owner role can be deleted and
// recreated under a different identifier, and no role-mutation path
// invalidates an authorizer. Any positive cache therefore buys throughput
// with a window in which a replacement role is not recognised and the control
// silently stops applying. Correctness wins: the identifiers backing a
// grant-voiding control are resolved on the decision that uses them.
//
// The cost is one name-index lookup plus one record read, and only on the
// decisions that consult it. Callers avoid it entirely when the identity is
// already classified by role name or by the canonical identifier, neither of
// which requires the store. Earning a cache back means making system roles
// genuinely immutable first, so the cached invariant is one the store
// guarantees.
//
// The lookup runs on the caller's context, exactly as ResolvePermissions
// does below, so it sees the role store through the same tenant-visibility
// rules that permission resolution will apply moments later. A failure is
// reported rather than swallowed: treating an unresolvable store as "no other
// identifier" would fail open.
//
// The second return value reports whether the answer is authoritative — that
// is, whether it came from the linearizable store rather than the StaleRead
// replica. It matters because the answer is used to CLEAR a grant, and the
// replica can be behind the mutation that matters: after the SO role is
// deleted and recreated under a generated identifier, a replica that has not
// caught up still maps the name to the old one. An identity carrying the new
// identifier would then match nothing in this set and the control would
// silently stop applying. Callers must not treat a stale "not in the set" as
// "does not denote the role" — see staleMayDenote.
func (a *RBACAuthorizer) systemRoleIDs(ctx context.Context, tenantID, roleName string) ([]string, bool, error) {
	ids, err := role.SystemRoleIDs(ctx, a.roles, tenantID, roleName)
	if err == nil {
		return ids, true, nil
	}

	// A resolver that returns neither a role nor an error has broken its
	// contract. That is a defect in the resolver, not the cold-node
	// condition the fallback exists for, so it is terminal: retrying it
	// against the stale-read replica would only mask a broken seam on a
	// path that decides whether to void a grant.
	var invalid *role.InvalidResolverResultError
	if errors.As(err, &invalid) {
		return nil, false, &ResolveError{Err: err}
	}
	if a.rolesFallback == nil {
		return nil, false, &ResolveError{Err: err}
	}
	// Same StaleRead fallback rationale as permission resolution: role data
	// is replicated before servers start, so a stale read keeps a cold or
	// draining node authorizing. Unlike permission resolution, the answer is
	// marked stale so the caller can refuse to clear a grant on it.
	fids, ferr := role.SystemRoleIDs(ctx, a.rolesFallback, tenantID, roleName)
	if ferr != nil {
		// The fallback failed too — including by breaking the same contract.
		// Either way there is no resolved answer, so deny.
		return nil, false, &ResolveError{Err: err}
	}
	return fids, false, nil
}

// staleMayDenote reports whether roleID may denote the named system role,
// asked of the StaleRead replica about an identifier that a stale name lookup
// did not account for.
//
// It exists because a stale name lookup can only be trusted to say what the
// role WAS called, never that an unmatched identifier is innocent. So the
// identifier is resolved the other way round — by ID, to a record with a name
// — and anything the replica cannot rule out is treated as denoting the role:
//
//   - no record for the identifier: this is exactly what a role recreated
//     after the replica's snapshot looks like, so it is assumed to be one.
//   - a record whose name is the system role's: the replica has the recreated
//     role but not yet the name index entry pointing at it, which the earlier
//     lookup would have missed.
//   - a record with some other name: ruled out. The identifier is a generated
//     UUID that names a different role, and identifiers are not reused.
//
// A read failure is not an answer, so it denies rather than clearing.
func (a *RBACAuthorizer) staleMayDenote(ctx context.Context, roleID, roleName string) (bool, error) {
	r, err := a.rolesFallback.GetRole(ctx, roleID)
	if err != nil {
		var notFound *role.RoleNotFoundError
		if errors.As(err, &notFound) {
			return true, nil
		}
		return false, &ResolveError{Err: err}
	}
	if r == nil {
		// Neither a role nor an error: the same broken contract systemRoleIDs
		// refuses to interpret. Deny rather than read it as innocence.
		return false, &ResolveError{Err: &InvalidRoleLookupError{RoleID: roleID}}
	}
	return r.Name == roleName, nil
}

// holdsSystemRole reports whether identity holds the named system role of its
// own tenant, by role name or by any identifier that denotes it.
//
// The name and the canonical identifier are matched first because neither
// needs the store: an identity classified by those is classified with no I/O
// and no staleness whatsoever. The store is consulted only when they do not
// settle it, which is the case where the role may be persisted under another
// identifier — a legacy seeding, or a role recreated under a generated ID.
func (a *RBACAuthorizer) holdsSystemRole(ctx context.Context, identity *auth.Identity, roleName string) (bool, error) {
	return a.roleIDsHoldSystemRole(ctx, identity.Roles, identity.TenantID, roleName)
}

// roleIDsHoldSystemRole reports whether roleIDs — all belonging to tenantID —
// includes the named system role of tenantID, by role name or by any
// identifier that denotes it. It is holdsSystemRole's tenant-parameterized
// core, shared with the bound-role SO check in Authorize: a role id must be
// resolved in the SAME tenant it actually belongs to, whether it came from
// an identity's own Roles (that identity's home tenant) or from a
// RoleBinding's RoleID (the tenant the binding names) — resolving either set
// in the WRONG tenant either hides a genuine system-role grant (an
// unrelated-tenant lookup reports RoleNotFound, read as absence) or fails
// the whole call outright (kvRoleStore.ResolvePermissions/GetRole error on
// an id that belongs to a different tenant's store).
//
// The name and the canonical identifier are matched first because neither
// needs the store: an identity classified by those is classified with no I/O
// and no staleness whatsoever. The store is consulted only when they do not
// settle it, which is the case where the role may be persisted under another
// identifier — a legacy seeding, or a role recreated under a generated ID.
func (a *RBACAuthorizer) roleIDsHoldSystemRole(ctx context.Context, roleIDs []string, tenantID, roleName string) (bool, error) {
	canonical := role.SystemRoleID(tenantID, roleName)
	for _, r := range roleIDs {
		if r == roleName || r == canonical {
			return true, nil
		}
	}
	if len(roleIDs) == 0 {
		return false, nil
	}

	// Resolve under tenantID, not whatever tenant ctx already carries.
	// kvRoleStore's ownership check reads the context tenant, so a mismatch
	// here hides a genuine system-role grant and the lookup comes back
	// RoleNotFound. That reads as genuine absence, leaves the legacy
	// identifier out of the set, and silently switches off the SO deny-list
	// for exactly the privileged caller it exists to constrain.
	ctx = tenant.WithTenantID(ctx, tenantID)

	ids, authoritative, err := a.systemRoleIDs(ctx, tenantID, roleName)
	if err != nil {
		return false, err
	}
	for _, r := range roleIDs {
		if slices.Contains(ids, r) {
			return true, nil
		}
	}
	if authoritative {
		return false, nil
	}

	// The set came from the StaleRead replica, which can be behind the very
	// mutation this control has to see: a role deleted and recreated under a
	// generated identifier. "Not in the stale set" is therefore not a finding
	// of innocence, so each unmatched identifier is checked from the ID side
	// before the grant is cleared.
	for _, r := range roleIDs {
		mayDenote, err := a.staleMayDenote(ctx, r, roleName)
		if err != nil {
			return false, err
		}
		if mayDenote {
			return true, nil
		}
	}
	return false, nil
}

// boundRoleIDs returns the RoleID of every identity.RoleBindings entry whose
// TenantID equals the request's TARGET tenant — tenant.FromContext(ctx), the
// same resolution Authorize's scope rule already reads for this call — so a
// binding grants its role exactly when the request is operating against the
// tenant the binding names, and contributes nothing otherwise (go-qrdb#192
// item 4: "a binding in tenant A grants in A and NOT in B").
//
// It must run against the INCOMING ctx, before any of Authorize's tenant
// rebinds (the home-tenant resolve, the bound-tenant resolve, the Owner
// strip): the target tenant of THIS call and the identity's home tenant are
// frequently different (a system-tenant caller acting on tenant A, or
// exactly the cross-tenant grant a RoleBinding exists to express), and
// rebinding first would silently answer with the wrong tenant.
//
// Every entry this returns shares the SAME tenant (the target), which is
// what lets callers resolve them together in one tenant context rather than
// per binding.
func boundRoleIDs(ctx context.Context, identity *auth.Identity) []string {
	if len(identity.RoleBindings) == 0 {
		return nil
	}
	target := tenant.FromContext(ctx)
	var out []string
	for _, b := range identity.RoleBindings {
		if b.TenantID == target {
			out = append(out, b.RoleID)
		}
	}
	return out
}

// stripOwnerRole removes any entry of roleIDs that denotes tenantID's Owner
// role under strict separation of duties, by name, canonical id, or a
// legacy/generated id staleMayDenote cannot rule out. It is effectiveRoles'
// former inline logic, parameterized so the SAME stripping applies to role
// ids resolved in ANY tenant: an identity's own Roles in its home tenant, or
// a RoleBinding's RoleID in the tenant the binding targets. Resolving the
// strip only in the identity's home tenant — the previous behavior — left a
// CROSS-TENANT bound Owner grant completely unstripped, since the tenant
// that actually issued it was never consulted.
//
// roleIDs is returned unchanged, with no lookup performed, whenever strict
// SoD is off or roleIDs is empty.
func (a *RBACAuthorizer) stripOwnerRole(ctx context.Context, roleIDs []string, tenantID string) ([]string, error) {
	if !a.strictSoD || len(roleIDs) == 0 {
		return roleIDs, nil
	}
	// Same tenant-scoping as roleIDsHoldSystemRole: the Owner role being
	// stripped is TENANTID's, so it must be resolved under that tenant's
	// context or a mismatched request cannot see it and the strip silently
	// stops applying.
	ctx = tenant.WithTenantID(ctx, tenantID)

	ownerIDs, authoritative, err := a.systemRoleIDs(ctx, tenantID, role.RoleNameOwner)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(roleIDs))
	for _, r := range roleIDs {
		if !authoritative && r != role.RoleNameOwner && !slices.Contains(ownerIDs, r) {
			// Stale set: an identifier it does not account for may be an Owner
			// role recreated after the replica's snapshot. Strip it unless the
			// replica can rule it out. See staleMayDenote.
			mayDenote, mayErr := a.staleMayDenote(ctx, r, role.RoleNameOwner)
			if mayErr != nil {
				return nil, mayErr
			}
			if mayDenote {
				continue
			}
		}
		if r == role.RoleNameOwner || slices.Contains(ownerIDs, r) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// effectiveRoles returns the identity's OWN Roles with the Owner convenience
// role removed when strict separation of duties is active (see
// stripOwnerRole). It no longer folds in bound role ids — see
// resolveEffectivePermissions, which resolves an identity's home-tenant
// Roles and any tenant-scoped RoleBindings as TWO SEPARATE permission
// resolutions and unions the results, rather than one combined role-id list.
//
// Combining them into one list and resolving that list in a single tenant
// context was the shape of a real bug: routingRoleService.ResolvePermissions
// resolves every listed role id in ONE tenant (tenant.FromContext(ctx)), and
// kvRoleStore.ResolvePermissions/GetRole return an error — failing the
// ENTIRE call — for any id that does not belong to that tenant's store. A
// combined list containing both the identity's home-tenant role ids and a
// bound role id from a DIFFERENT tenant therefore failed outright whenever
// the two disagreed, denying access a valid binding alone would have
// granted.
func (a *RBACAuthorizer) effectiveRoles(ctx context.Context, identity *auth.Identity) ([]string, error) {
	return a.stripOwnerRole(ctx, identity.Roles, identity.TenantID)
}

// IsSystemTenant reports whether identity is scoped to the structurally
// privileged system tenant. A nil identity is treated as system-scoped:
// the absence of an authenticated identity means the caller is an internal,
// unauthenticated pipeline (platform boot, daemon-internal maintenance),
// which by convention executes with system-tenant privilege — mirroring
// tenant.FromContext's fallback for a context with no bound tenant. A
// non-nil identity delegates to tenantid.IsSystem, the single source of
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
	return tenantid.IsSystem(identity.TenantID)
}

// resolveScope returns the scope governing the given permission for the
// identity's tenant. When a tenant-aware resolver is installed it is
// consulted (fail safe on error); otherwise the built-in process-global
// layer is used. The error is non-nil only when the tenant-aware resolver
// fails — in which case the caller MUST deny.
func (a *RBACAuthorizer) resolveScope(ctx context.Context, identity *auth.Identity, p permission.Permission) (permission.Scope, error) {
	if a.scopes == nil {
		return permission.LookupScope(p)
	}
	return a.scopes.ResolveScope(ctx, identity.TenantID, p)
}

// resolvePermissionSet resolves roleIDs into their union of permissions,
// via the cache when possible, with the same primary/StaleRead-fallback
// pattern Authorize has always used for a single tenant's role ids.
//
// resolveCtx must already be bound to the tenant roleIDs belong to (the
// caller resolves this; ResolvePermissions itself also reads
// tenant.FromContext(resolveCtx) via the routing role service). subject
// and cacheTenantID form the rest of the cache key alongside roleIDs; a
// caller resolving BOUND role ids passes the bound tenant here, which is
// what keeps a bound-role permission set from colliding in the cache with
// the SAME subject's home-tenant set.
func (a *RBACAuthorizer) resolvePermissionSet(resolveCtx context.Context, cacheTenantID, subject string, roleIDs []string) (map[permission.Permission]struct{}, error) {
	if permSet, ok := a.cache.get(cacheTenantID, subject, roleIDs); ok {
		return permSet, nil
	}
	// Bound the primary linearizable resolve when a StaleRead fallback is
	// available, so a SLOW (not just failed) ReadIndex — cold pipeline,
	// draining host, transient consensus latency — fails over within
	// resolveSyncTimeout instead of blocking the whole request until its
	// outer deadline. A healthy ReadIndex answers in microseconds; the
	// only cost of the short budget is a marginally-staler read on a blip,
	// which is safe for replicated role data. Mirrors the auth user-lookup
	// guard and is the companion to the error-triggered fallback below.
	primaryCtx := resolveCtx
	var cancel context.CancelFunc
	if a.rolesFallback != nil {
		primaryCtx, cancel = context.WithTimeout(resolveCtx, resolveSyncTimeout)
	}
	perms, err := a.roles.ResolvePermissions(primaryCtx, roleIDs)
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
			return nil, &ResolveError{Err: err}
		}
		fperms, ferr := a.rolesFallback.ResolvePermissions(resolveCtx, roleIDs)
		if ferr != nil {
			return nil, &ResolveError{Err: err}
		}
		perms = fperms
	}
	a.cache.set(cacheTenantID, subject, roleIDs, perms)
	permSet := make(map[permission.Permission]struct{}, len(perms))
	for _, p := range perms {
		permSet[p] = struct{}{}
	}
	return permSet, nil
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

	// A share-holder certificate is a custody credential, not a login: it
	// reaches only the identity-gated share endpoints, which never come
	// here, and is refused every RBAC permission — even if an account with
	// the same name holds roles.
	if identity.IsShareHolder() {
		return &PermissionDeniedError{
			Resource: resource,
			Action:   action,
			Subject:  identity.Subject,
			Reason:   "share-holder certificates cannot be used to log in",
		}
	}
	// Captured BEFORE any tenant rebind below: boundRoleIDs and the SO/
	// permission resolution below both need the call's ORIGINAL target
	// tenant, and every entry boundRoleIDs returns belongs to it.
	target := tenant.FromContext(ctx)
	bound := boundRoleIDs(ctx, identity)

	// SO deny-list check (FIPS 140-3 AS10.03): must precede any other
	// admission so that a malicious or erroneous grant cannot override it.
	// Checked against BOTH sources of role grants — the identity's own
	// Roles in its home tenant, and any RoleBinding scoped to this call's
	// target tenant — so a bound SO grant plus an unrelated crypto grant is
	// denied exactly as a plain SO role already is. Checking only Roles left
	// a bound SO grant free to combine with another tenant-scoped grant and
	// evade the FIPS denial entirely.
	if isSODenied(resource, action) {
		isSO, err := a.holdsSystemRole(ctx, identity, role.RoleNameSO)
		if err != nil {
			return err
		}
		if !isSO && len(bound) > 0 {
			isSO, err = a.roleIDsHoldSystemRole(ctx, bound, target, role.RoleNameSO)
			if err != nil {
				return err
			}
		}
		if isSO {
			return &PermissionDeniedError{
				Resource: resource,
				Action:   action,
				Subject:  identity.Subject,
				Reason:   "SO role explicitly denied from cryptographic operations (FIPS 140-3 AS10.03)",
			}
		}
	}

	// Resolve the identity's effective permissions. Home-tenant Roles and
	// target-tenant-scoped RoleBindings are resolved as TWO SEPARATE calls,
	// each in the tenant its role ids actually belong to, and their
	// permission sets are unioned — never combined into one role-id list
	// and resolved in a single tenant, which fails outright the moment the
	// two tenants disagree (see effectiveRoles' doc comment).
	homeRoles, err := a.effectiveRoles(ctx, identity)
	if err != nil {
		return err
	}
	homeCtx := tenant.WithTenantID(ctx, identity.TenantID)
	permSet, err := a.resolvePermissionSet(homeCtx, identity.TenantID, identity.Subject, homeRoles)
	if err != nil {
		return err
	}
	if len(bound) > 0 {
		strippedBound, serr := a.stripOwnerRole(ctx, bound, target)
		if serr != nil {
			return serr
		}
		if len(strippedBound) > 0 {
			boundCtx := tenant.WithTenantID(ctx, target)
			boundPerms, berr := a.resolvePermissionSet(boundCtx, target, identity.Subject, strippedBound)
			if berr != nil {
				return berr
			}
			merged := make(map[permission.Permission]struct{}, len(permSet)+len(boundPerms))
			for p := range permSet {
				merged[p] = struct{}{}
			}
			for p := range boundPerms {
				merged[p] = struct{}{}
			}
			permSet = merged
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
	case permission.ScopeSelfStrict:
		// Own tenant only, for EVERY identity. Unlike ScopeSelf the
		// system-tenant rule does not lift this, and unlike ScopeTenantOnly
		// the system tenant is not denied outright: it is confined to its own
		// tenant exactly as every other tenant is.
		if identity.TenantID == targetTenantID {
			return nil
		}
		return &PermissionDeniedError{
			Resource: resource,
			Action:   action,
			Subject:  identity.Subject,
			Reason:   "self-strict permission: identity not in target tenant (the system tenant is not lifted)",
		}
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
