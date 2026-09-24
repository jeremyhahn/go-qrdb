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

// Package middleware provides authentication and authorization middleware
// for gRPC interceptors and REST HTTP handlers. It chains pluggable
// authenticators, enriches identities from the user store, and enforces
// permission-based authorization using map-based dispatch.
package middleware

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// AuthMiddleware chains authenticators and an authorizer to enforce
// access control on gRPC and REST endpoints. It optionally enriches
// identities from the user store and logs authorization decisions to
// an audit logger.
type AuthMiddleware struct {
	authenticators []auth.Authenticator
	authorizer     auth.Authorizer
	users          user.UserServicer
	usersFallback  user.UserServicer
	audit          audit.Logger

	// roles resolves a RoleTarget to the tenant the role belongs to (see
	// ResolveTarget). Optional: without it a RoleTarget is the caller's own
	// tenant.
	roles role.RoleServicer

	// auditFailure is what authorize() does when m.audit cannot record a
	// decision. Its ZERO VALUE is audit.FailClosed, so an AuthMiddleware built
	// without WithAuditFailurePolicy refuses rather than grants -- see
	// audit.FailClosed for why the security posture is the zero value.
	//
	// It is the SAME value the security-officer gate reads (go-qrdb#445): one
	// operator lever, auth.audit_failure_policy, governs both assertions.
	auditFailure audit.FailurePolicy

	// peerAttestation, when non-nil, is the attested-caller gate
	// (go-qrdb#278): for its gated permissions, a request RBAC granted is
	// admitted only when the peer's measured state is currently acceptable.
	peerAttestation *PeerAttestationGate

	// bindingPolicy is the fleet-wide mTLS enrolment posture
	// (auth.mtls_binding_policy, go-qrdb#517). Its zero value is
	// auth.MTLSBindingEnrolled -- the behaviour before the setting existed.
	bindingPolicy auth.MTLSBindingPolicy

	// revocation gates the ON-BEHALF forwarding path, which bypasses the
	// authenticator chain entirely and therefore does not inherit the mTLS
	// authenticator's own revocation check. Nil means revocation is not
	// enforced, matching the authenticator.
	revocation auth.RevocationChecker

	// limiter and limits together are go-qrdb#201's centralized rate-limit
	// enforcement: limiter is the engine (see ratelimit.Limiter), limits is
	// the single-source per-permission Policy declaration (see
	// ratelimit.Registry). Either being nil disables rate limiting entirely
	// -- the pre-#201 posture -- so an embedder that never opts in pays one
	// nil check per authorize() call and nothing else.
	limiter ratelimit.Limiter
	limits  *ratelimit.Registry

	// publicGRPCMethods holds the gRPC full method names ("/package.Service/
	// Method") that bypass both authentication and authorization, declared by
	// ServiceModules that implement apimodule.GRPCPublicMethodLister via
	// RegisterPublicGRPCMethods. It is a *map behind an atomic.Pointer so
	// registration (server startup, and possibly re-registration from a
	// second transport sharing this same middleware instance) replaces the
	// whole set in one atomic store, and the interceptors' per-request lookup
	// stays lock-free.
	publicGRPCMethods atomic.Pointer[map[string]struct{}]

	// grpcTargets maps a gRPC full method name to how its request names the
	// tenant it acts on. See RegisterGRPCTargets.
	grpcTargets atomic.Pointer[map[string]GRPCTargetFunc]
}

// authLookupSyncTimeout bounds the per-request linearizable user lookup. A
// healthy ReadIndex answers far faster; on a transient consensus slowdown the
// lookup fails over to the StaleRead fallback within this budget rather than
// stalling the request. Kept sub-second so a brief blip cannot cascade into
// multi-second per-request latency.
const authLookupSyncTimeout = 500 * time.Millisecond

// Option configures an AuthMiddleware.
type Option func(*AuthMiddleware)

// New creates a new AuthMiddleware with the given authorizer and options.
// The authorizer is required; authenticators are added via
// WithAuthenticator options.
func New(authorizer auth.Authorizer, opts ...Option) *AuthMiddleware {
	m := &AuthMiddleware{
		authorizer: authorizer,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// WithAuthenticator appends an authenticator to the chain. Authenticators
// are tried in the order they are added; the first to succeed provides
// the identity.
func WithAuthenticator(a auth.Authenticator) Option {
	return func(m *AuthMiddleware) {
		m.authenticators = append(m.authenticators, a)
	}
}

// WithUserService configures user-store enrichment. When set, the
// middleware looks up the authenticated subject in the user store to
// enrich the identity with persistent role assignments and to enforce
// the user-enabled check.
func WithUserService(u user.UserServicer) Option {
	return func(m *AuthMiddleware) {
		m.users = u
	}
}

// WithUserServiceFallback configures a fallback user-store for when the
// primary store is unavailable (e.g. Raft ReadIndex not yet operational
// after cluster restart). The fallback typically uses StaleRead which
// reads from the local state machine directly. This is safe because
// bootstrap ensures user/role data is replicated before servers start.
func WithUserServiceFallback(u user.UserServicer) Option {
	return func(m *AuthMiddleware) {
		m.usersFallback = u
	}
}

// WithMTLSBindingPolicy selects the fleet-wide mTLS enrolment posture
// (auth.mtls_binding_policy). Under auth.MTLSBindingRequired a certificate
// login is admitted only by presenting a certificate enrolled on the account it
// names; an account with no bindings, and a certificate naming no account, are
// refused with *CertBindingRequiredError. The default, auth.MTLSBindingEnrolled,
// enforces a binding only on accounts that have one.
//
// Selecting "required" is a lockout risk. The composer that selects it must
// also refuse to boot when the bootstrap admin could not log in under it —
// firstboot.VerifyAdminCertBinding is that check.
func WithMTLSBindingPolicy(p auth.MTLSBindingPolicy) Option {
	return func(m *AuthMiddleware) {
		m.bindingPolicy = p
	}
}

// MTLSBindingPolicy returns the enrolment posture this middleware enforces.
// A nil middleware (auth disabled) enforces none and answers the default.
func (m *AuthMiddleware) MTLSBindingPolicy() auth.MTLSBindingPolicy {
	if m == nil {
		return auth.MTLSBindingEnrolled
	}
	return m.bindingPolicy
}

// WithRevocationChecker gates the on-behalf forwarding path on certificate
// revocation.
//
// It is a SEPARATE wiring point from auth.WithRevocationChecker on purpose,
// and production wiring passes the SAME checker to both: a forwarded
// assertion is verified against the forwarding node's peer certificate
// without ever running the authenticator chain, so a revoked cluster node
// would otherwise keep forwarding writes on behalf of any user it names.
func WithRevocationChecker(rc auth.RevocationChecker) Option {
	return func(m *AuthMiddleware) {
		m.revocation = rc
	}
}

// RevocationChecker returns the checker this middleware enforces revocation
// with, or nil when revocation is not enforced.
//
// It exists so a certificate-authorization site OUTSIDE the middleware can
// enforce the same policy against the same state. The cluster key plane
// (pkg/clusterkeys) is one: its routes are on the public allowlist by design,
// so they never reach this middleware, yet they authorize a peer certificate
// and must refuse a revoked one. Handing that site this checker rather than a
// second one built from the same provider means both share one bounded-TTL
// cache, so the window in which a freshly revoked certificate still passes is
// one number for the node instead of one per plane.
//
// Nil middleware answers nil: --auth-enabled=false wires no middleware at all,
// and the whole node then enforces no revocation, which is the posture the
// operator selected.
func (m *AuthMiddleware) RevocationChecker() auth.RevocationChecker {
	if m == nil {
		return nil
	}
	return m.revocation
}

// WithAuditLogger configures audit logging for authorization decisions.
// When set, every authorize call is recorded with its outcome, and a failure
// to record one is acted on per WithAuditFailurePolicy.
func WithAuditLogger(a audit.Logger) Option {
	return func(m *AuthMiddleware) {
		m.audit = a
	}
}

// authenticate runs the authenticator chain and returns an enriched
// identity stored in the returned context. It tries each authenticator
// in order; the first to succeed provides the base identity.
//
// When a UserServicer is configured, the identity is enriched with the
// user's persistent RoleIDs. If the user is disabled, a
// UserDisabledError is returned. If the user is not found in the store,
// the base identity is used without enrichment.
func (m *AuthMiddleware) authenticate(ctx context.Context) (context.Context, *auth.Identity, error) {
	if len(m.authenticators) == 0 {
		return ctx, nil, auth.ErrUnauthenticated
	}

	var lastErr error
	var identity *auth.Identity
	// The NAME of the authenticator that succeeded. It was previously discarded
	// the moment the loop broke, which is why the account's permitted-method
	// policy had nothing to compare against.
	var method authMethod

	for _, authn := range m.authenticators {
		id, err := authn.Authenticate(ctx)
		if err != nil {
			lastErr = err
			continue
		}
		identity = id
		// KNOWN, whatever the name turns out to be — including blank. This
		// path observed the method, so it must never be mistaken for one of
		// the paths that could not: an authenticator whose Name() returns ""
		// would otherwise take the unknown-method bypass and walk straight
		// past a non-empty policy (Codex, PR #198).
		method = knownMethod(authn.Name())
		break
	}

	if identity == nil {
		// All authenticators failed. If the last authenticator returned
		// an AuthError (e.g., ErrUnauthenticated), return it directly
		// to preserve the original error type.
		var authErr *auth.AuthError
		if errors.As(lastErr, &authErr) {
			return ctx, nil, lastErr
		}
		return ctx, nil, &AuthChainError{Err: lastErr}
	}

	return m.finalizeIdentity(ctx, identity, method)
}

// tenantIAMSealedMetadataKey marks an identity whose tenant's IAM store was
// unreachable at authentication time because the tenant barrier is sealed on
// this node. See finalizeIdentity (where it is stamped) and authorize (where
// every non-IdentityGated permission converts it into a retryable
// unavailable error).
const tenantIAMSealedMetadataKey = "qrdb_tenant_iam_sealed"

// IdentityIAMSealed reports whether identity was authenticated while its
// tenant's IAM store was unreachable (tenant barrier sealed, not yet
// initialized, or its key not yet delivered to this node). Handlers behind
// permission.IdentityGated routes use this to distinguish "RBAC denied this
// caller" from "RBAC could not have been evaluated at all": the tenant
// barrier status and unseal endpoints accept the tenant's own identity
// exactly in this window, because a role able to satisfy their RBAC check
// is encrypted behind the very barrier being unsealed.
func IdentityIAMSealed(identity *auth.Identity) bool {
	return identity != nil && identity.Metadata[tenantIAMSealedMetadataKey] == "true"
}

// finalizeIdentity enriches a base identity with its persistent role
// assignments from the user store, enforces the user-enabled check, the
// account's mTLS certificate enrolment (checkCertBinding) and its
// permitted-method policy (checkAuthMethod), then binds the identity and its
// tenant into the returned context. The base identity's
// Roles are overwritten with the store's RoleIDs: roles are ALWAYS resolved
// from the authorization store, never trusted from the identity's origin (a
// certificate, a token, or -- critically -- a forwarded on-behalf assertion).
//
// When no user store is configured the base identity is used as-is. A missing
// user proceeds without enrichment (empty roles -> default-deny at authorize);
// a transient store failure returns StoreUnavailableError so clients retry
// rather than being denied with stale/empty roles.
func (m *AuthMiddleware) finalizeIdentity(ctx context.Context, identity *auth.Identity, method authMethod) (context.Context, *auth.Identity, error) {
	// Fold an absent tenant onto the system tenant HERE, on the identity
	// itself, before anything reads it. This function is the single funnel
	// every authenticated identity passes through on every transport, and it
	// is the "auth/middleware identity normalization" that
	// tenant.IsSystemTenant names as the reason its empty-string branch is
	// safe.
	//
	// Until #188 it normalized only the CONTEXT tenant (below), leaving
	// identity.TenantID raw — and rbac.IsSystemTenant reads the raw field,
	// not the context. An identity that authenticated with no tenant binding
	// (pkg/auth/mtls.go: a verified client certificate carrying neither a
	// SPIFFE tenant SAN nor a Subject OU yields TenantID "" with no error;
	// pkg/auth/middleware/onbehalf.go: a forwarded assertion re-carries
	// whatever tenant the origin node had, empty included) therefore reached
	// the authorization predicate unnormalized. The precondition was stated
	// in a doc comment and enforced by nothing.
	//
	// This changes no authorization OUTCOME — IsSystemTenant("") and
	// IsSystemTenant(SystemTenantID) are both true, and every store resolver
	// already maps "" to the system keyspace — which is exactly why it is a
	// defect fix and not a policy change. What it changes is that the mapping
	// "no tenant -> system" now happens once, visibly, at the boundary, and
	// the value that flows into RBAC, into the permission cache key, into the
	// scope resolver and into the audit log is the tenant that was actually
	// used. Making an absent tenant fail CLOSED instead is a separate
	// decision; it is not this one, and it does not belong in a fold.
	identity.TenantID = tenant.Normalize(identity.TenantID)

	if m.users != nil {
		// RoleBindings and Attributes are user-record state, and with a user
		// store configured the store is their ONLY source. Clear whatever the
		// authenticator carried in BEFORE the lookup, so every path below --
		// found, not found, tenant sealed -- starts from none: a binding
		// withdrawn from the record, or never on it, cannot survive on an
		// identity issued earlier or forwarded from elsewhere.
		identity.RoleBindings = nil
		identity.Attributes = nil

		u, err := m.lookupUser(ctx, identity.TenantID, identity.Subject)
		if err != nil {
			// A dedicated-isolation tenant whose barrier is sealed (or not
			// yet initialized) cannot resolve ANY user: its IAM is encrypted
			// under the tenant DEK and the store resolver fails closed with
			// ErrTenantSealed. That is the tenant's sealed-window state, not
			// a transient outage — the identity proceeds UNENRICHED (empty
			// roles), so RBAC denies everything except the IdentityGated
			// bootstrap endpoints (tenant barrier init, officer claims),
			// which is exactly the surface a sealed tenant may reach.
			if errors.Is(err, tenant.ErrTenantSealed) {
				if identity.Metadata == nil {
					identity.Metadata = make(map[string]string)
				}
				identity.Metadata[tenantIAMSealedMetadataKey] = "true"
				ctx = auth.WithIdentity(ctx, identity)
				return tenant.WithTenantID(ctx, identity.TenantID), identity, nil
			}
			// If user not found, proceed with base identity (no enrichment) --
			// unless the binding posture is "required", under which a
			// certificate naming no account is refused.
			var unf *user.UserNotFoundError
			if errors.As(err, &unf) {
				if rerr := checkUnresolvedCertLogin(ctx, identity, method, m.bindingPolicy); rerr != nil {
					return ctx, nil, rerr
				}
			} else {
				// Transient store error (Raft not ready, timeout, etc).
				// Return 503/UNAVAILABLE so clients retry rather than
				// proceeding without roles (which would cause false 403).
				slog.Warn("auth/middleware: user store unavailable",
					"subject", identity.Subject,
					"tenant", identity.TenantID,
					"error", err,
				)
				return ctx, nil, &StoreUnavailableError{Err: err}
			}
		} else {
			// User found; check that its lifecycle state permits a login.
			// CanAuthenticate fails closed on an absent or unrecognized state.
			if !u.CanAuthenticate() {
				return ctx, nil, &UserDisabledError{
					Username:  u.Username,
					TenantID:  u.TenantID,
					Lifecycle: string(u.Lifecycle),
				}
			}
			// Enforce the account's mTLS certificate enrolment BEFORE its
			// permitted-method policy, because the two answer different
			// questions and this one comes first: "is this credential
			// yours?" precedes "may you come in this way?". A certificate
			// that names an account it is not bound to has not identified
			// anyone, so it must not be told which methods that account is
			// permitted -- which is also why the two errors classify
			// differently (401 versus 403).
			//
			// This is the layer that contains an unguarded certificate
			// issuance path. Without it the Subject CN and OU ARE the
			// authentication claim, and any path that mints a certificate
			// naming someone else mints their identity. See checkCertBinding
			// for what it does and does not enforce -- in particular that an
			// account with no enrolled bindings authenticates exactly as
			// before.
			if cerr := checkCertBinding(ctx, u, method, m.bindingPolicy); cerr != nil {
				return ctx, nil, cerr
			}

			// Enforce the account's permitted-method policy.
			//
			// User.AuthMethods says what an account MAY present. Storing it
			// without checking it here would make method withdrawal fail open:
			// a user restricted to webauthn could still walk in through the
			// mTLS authenticator, because the credential exists and nothing
			// compared it against the policy (Codex peer review, PR #198).
			//
			// Two deliberate limits, both stated rather than assumed:
			//   - An EMPTY policy states no restriction, so every account that
			//     predates the field is unaffected. That is what makes turning
			//     this on safe rather than a fleet-wide lockout.
			//   - It can only be enforced where the method is KNOWN — the
			//     authenticator chain qrdb owns. A forwarded on-behalf
			//     assertion and a capability module's session token carry no
			//     method qrdb can observe, so they pass unknownMethod() and
			//     qrdb does not pretend to check them. The layer that ran that
			//     ceremony is the one that must enforce the policy there.
			//
			// The distinction is carried by a FLAG, not by an empty string. An
			// authenticator that names itself "" is observed, not unobservable,
			// and conflating the two let it bypass the policy entirely.
			if perr := checkAuthMethod(u, method); perr != nil {
				return ctx, nil, perr
			}

			// Enrich identity with persistent role assignments. Roles,
			// RoleBindings and Attributes are overwritten from the store's
			// current state, never merged, so a withdrawal on the record
			// takes effect here.
			identity.Roles = u.RoleIDs
			identity.RoleBindings = convertRoleBindings(u.RoleBindings)

			// Attributes stays separate from Metadata (see
			// auth.Identity.Attributes): Metadata is authentication-derived,
			// Attributes is record state an administrator holding
			// roles:assign writes. Cloned so the identity never aliases the
			// record's map.
			identity.Attributes = maps.Clone(u.Attributes)
		}
	} else if rerr := checkUnresolvedCertLogin(ctx, identity, method, m.bindingPolicy); rerr != nil {
		// No user store: no account can be resolved, so under the
		// "required" posture no certificate login can show an enrolment.
		return ctx, nil, rerr
	}

	ctx = auth.WithIdentity(ctx, identity)

	// Bind the canonical tenant context from the authenticated identity so
	// that tenant.FromContext(ctx) returns the authenticated tenant for every
	// downstream service on every transport. The fold happened once at the
	// top of this function, so context and identity now agree by
	// construction rather than by two separate copies of the same rule.
	ctx = tenant.WithTenantID(ctx, identity.TenantID)

	return ctx, identity, nil
}

// convertRoleBindings copies a user's RoleBindings into their auth.RoleBinding
// mirror, field by field rather than through a shared type: pkg/auth cannot
// import pkg/user (pkg/user already imports pkg/auth), so auth.RoleBinding is
// this package's own copy of the same two fields.
//
// A nil or empty input returns nil rather than an empty non-nil slice, so
// finalizeIdentity's unconditional assignment (identity.RoleBindings =
// convertRoleBindings(u.RoleBindings)) correctly CLEARS the field when the
// store reports no bindings, rather than requiring a caller to special-case
// emptiness itself.
func convertRoleBindings(bindings []user.RoleBinding) []auth.RoleBinding {
	if len(bindings) == 0 {
		return nil
	}
	out := make([]auth.RoleBinding, len(bindings))
	for i, b := range bindings {
		out[i] = auth.RoleBinding{RoleID: b.RoleID, TenantID: b.TenantID}
	}
	return out
}

// lookupUser looks up a user by username, first trying the primary user
// store (SyncRead for linearizable reads), then falling back to the
// optional fallback store (StaleRead from local state machine) when the
// primary fails with a transient error. The fallback is safe because
// bootstrap ensures user/role data is replicated before servers start.
func (m *AuthMiddleware) lookupUser(ctx context.Context, tenantID, subject string) (*user.User, error) {
	// Bound the linearizable (SyncRead) lookup tightly so a transient
	// ReadIndex slowdown fails over to the StaleRead fallback in well under a
	// second instead of stalling every authenticated request for seconds. A
	// healthy ReadIndex answers in microseconds; the only cost of a short
	// budget is a marginally-staler read on a blip, which is safe for
	// auth (user/role data is replicated before servers start and changes
	// rarely). This is the key guard against a brief consensus blip
	// cascading into per-request latency that trips client/connect timeouts.
	lookupCtx, cancel := context.WithTimeout(ctx, authLookupSyncTimeout)
	u, err := m.users.GetUserByUsername(lookupCtx, tenantID, subject)
	cancel()
	if err == nil {
		return u, nil
	}

	// UserNotFoundError is authoritative — don't fall back.
	var unf *user.UserNotFoundError
	if errors.As(err, &unf) {
		return nil, err
	}

	// A sealed tenant barrier is authoritative too: the tenant's IAM is
	// encrypted behind it, so the fallback store CANNOT hold the record —
	// letting it answer would mask the sealed window as a definitive
	// UserNotFound, stripping the sealed marker finalizeIdentity stamps and
	// with it both the retryable 503 and the IdentityGated barrier
	// bootstrap fallback that end the window.
	if errors.Is(err, tenant.ErrTenantSealed) {
		return nil, err
	}

	// Primary failed with a transient error; try fallback.
	if m.usersFallback != nil {
		fallbackCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		u, ferr := m.usersFallback.GetUserByUsername(fallbackCtx, tenantID, subject)
		cancel()
		if ferr == nil {
			slog.Debug("auth/middleware: used fallback user store",
				"subject", subject, "tenant", tenantID)
			return u, nil
		}
		// If fallback also returns UserNotFoundError, use that.
		if errors.As(ferr, &unf) {
			return nil, ferr
		}
	}

	// Both primary and fallback failed (or no fallback configured).
	return nil, err
}

// authorize checks whether the identity is permitted to perform the
// action on the resource, and optionally records the decision in the
// audit log.
//
// The permission.IdentityGated sentinel short-circuits the RBAC check:
// possession of an authenticated identity is the only middleware
// requirement, and the binding authorization (officer/custodian registry
// lookup, certificate-thumbprint match, nonce-signature or factor
// verification) is enforced at the handler layer. Security Officers and
// custodians are enrolled in the ceremony registry — not the RBAC user
// store, which does not yet exist during sealed-window registration — so a
// role-permission check can never authorize them. The decision is still
// audited.
func (m *AuthMiddleware) authorize(ctx context.Context, identity *auth.Identity, perm permission.Permission) error {
	// Rate limiting runs FIRST, ahead of every other branch below, including
	// IdentityGated and the tenant-IAM-sealed allowance: a caller within its
	// quota reaches exactly the same authorization outcome as before #201,
	// and a caller over it is refused before the RBAC/ABAC authorizer or the
	// audit logger's user-store-adjacent lookups do any further work on its
	// behalf. It is a no-op whenever rate limiting is not configured (see
	// checkRateLimit) or perm carries no declared Policy -- a Policy is an
	// explicit per-permission opt-in, not a default, so admin:health and
	// every other permission behave exactly as before #201 until an owner
	// declares one.
	if identity != nil {
		if err := m.checkRateLimit(identity, perm); err != nil {
			if auditErr := m.auditDecision(ctx, identity, perm, err); auditErr != nil {
				return auditErr
			}
			return err
		}
	}

	err := m.decide(ctx, identity, perm)
	if errors.Is(err, errUnauditedUnauthenticated) {
		return auth.ErrUnauthenticated
	}
	if err == nil {
		// The attested-caller gate runs only on a request the decision
		// GRANTED -- through RBAC, the IdentityGated sentinel or the sealed
		// tenant's admin:health allowance: it is a second condition on
		// admission, never a substitute for the first, and a request already
		// refused costs no verification. It is not part of decide because it
		// has side effects on a refusal (its own log line and audit record),
		// and it is a property of the caller's connection, not of the tenant a
		// request names, so AuthorizeTenantTarget's re-check does not repeat it.
		err = m.checkPeerAttestation(ctx, identity, perm)
	}
	if auditErr := m.auditDecision(ctx, identity, perm, err); auditErr != nil {
		return auditErr
	}
	return err
}

// errUnauditedUnauthenticated marks decide's one outcome authorize() does not
// record: an IdentityGated request with no identity at all, refused before
// there is anything to attribute (the long-standing behaviour of that branch).
var errUnauditedUnauthenticated = errors.New("auth/middleware: unaudited unauthenticated")

// decide is the authorization decision itself -- the IdentityGated sentinel,
// the sealed-tenant-IAM answer, and the RBAC/ABAC authorizer -- with NO side
// effects: it charges no rate-limit token and writes no audit record.
// authorize() wraps it with both; AuthorizeTenantTarget wraps it with
// neither on a grant.
//
// The permission.IdentityGated sentinel short-circuits the RBAC check:
// possession of an authenticated identity is the only middleware
// requirement, and the binding authorization (officer/custodian registry
// lookup, certificate-thumbprint match, nonce-signature or factor
// verification) is enforced at the handler layer.
func (m *AuthMiddleware) decide(ctx context.Context, identity *auth.Identity, perm permission.Permission) error {
	if perm == permission.IdentityGated {
		if identity == nil {
			return errUnauditedUnauthenticated
		}
		return nil
	}

	// An identity whose tenant IAM was unreachable because the tenant's
	// barrier is sealed on THIS node (see finalizeIdentity) can reach only
	// the IdentityGated bootstrap surface above, plus admin:health. Every
	// other permission is answered UNAVAILABLE, not denied: the sealed
	// state is transient on a follower awaiting tenant-key delivery (and
	// operator-resolvable everywhere else), so clients and polling
	// harnesses must see a retryable 503/UNAVAILABLE rather than a
	// definitive 403 computed from an empty role set the store never
	// actually served.
	//
	// admin:health is admitted for the same liveness rationale the SYSTEM
	// sealed-window allowlist (pkg/api/sealedwindow) admits Health: the
	// gRPC/QUIC/Unix SDK transports perform an authenticated Health RPC at
	// Connect time, so without it a sealed tenant's identity cannot even
	// OPEN the connection it needs to reach the IdentityGated barrier
	// status/unseal endpoints that end the sealed window. Health reports
	// node liveness only — no tenant data crosses the seal boundary.
	if identity != nil && identity.Metadata[tenantIAMSealedMetadataKey] == "true" {
		if perm == permission.AdminHealth {
			return nil
		}
		return &StoreUnavailableError{Err: tenant.ErrTenantSealed}
	}

	return m.authorizer.Authorize(ctx, identity, permission.Resource(perm), permission.Action(perm))
}

// AuthorizeTenantTarget is the handler-level check for an operation whose
// request names the tenant it reads or changes -- a tenant's own record (get,
// update, quotas, entitlements, delete) -- or, against the system tenant, the
// platform-wide tenant operations (AuthorizeTenantCreate, FilterTenantList).
//
// The transport's entry check (the gRPC interceptor, the REST route
// middleware, the QUIC dispatch check, the MCP tool check) authorized the
// route's permission against the caller's OWN tenant, which says nothing about
// the tenant the request names: a tenant-local Admin passed it against itself
// and could then read, change or delete ANY tenant. This re-runs the decision
// against the named tenant, so ScopeSelf admits a tenant acting on itself and
// the system-tenant rule admits the platform operator, and nothing else.
//
// PRECONDITION: the entry check has already run for this request, which
// charged its rate-limit token and recorded its grant. This re-check therefore
// charges no token and records no grant -- doing either again would bill one
// request twice and write two records for it. A REFUSAL is recorded, because
// it is new information the entry check's grant did not carry, under the same
// audit-failure policy authorize() applies.
//
// Returns auth.ErrUnauthenticated when no identity is present in ctx.
func (m *AuthMiddleware) AuthorizeTenantTarget(ctx context.Context, tenantID string, perm permission.Permission) error {
	identity := auth.IdentityFromContext(ctx)
	if identity == nil {
		return auth.ErrUnauthenticated
	}
	ctx = tenant.WithTenantID(ctx, tenantID)
	err := m.decide(ctx, identity, perm)
	if err == nil {
		return nil
	}
	if auditErr := m.auditDecision(ctx, identity, perm, err); auditErr != nil {
		return auditErr
	}
	return err
}

// auditDecision records an authorization decision in the audit log when a
// logger is configured. A nil err is recorded as success, non-nil as denied.
//
// It returns a non-nil *audit.GateError -- wrapping whatever the sink
// reported, typically an *audit.WriteError -- when the record could NOT be
// written and the configured policy is audit.FailClosed. authorize() returns
// that error instead of the decision it was about to return, so the request is
// refused. Under audit.FailOpen it returns nil and the decision stands.
//
// The refusal is UNIFORM: it does not matter whether the decision being
// recorded was a grant, a denial, a rate-limit refusal or a sealed-tenant
// 503. "We answer only what we could record" is a property an auditor can
// state and a reader can check; "we answer only what we could record, except
// when we were going to refuse anyway" is four rules that will drift. The
// dangerous case is the unrecorded grant, and carving out the others buys
// nothing -- every one of them refuses the caller either way.
//
// THE AUDIT OF THE AUDIT. This refusal cannot itself be audited: the only
// sink is the one that just failed, and writing to it again would be a retry
// this feature explicitly forbids, on a sink that has already said no. So the
// event is recorded on two planes that do not depend on the audit plane --
// the qrdb_auth_audit_write_failures_total counter and an slog.Error on the
// operational log stream. During an audit outage the trail therefore has a
// GAP BY CONSTRUCTION; what the counter guarantees is that the gap is
// visible and measurable rather than silent, and the hash chain guarantees
// the gap cannot be back-filled with forged records.
func (m *AuthMiddleware) auditDecision(ctx context.Context, identity *auth.Identity, perm permission.Permission, err error) error {
	if m.audit == nil {
		return nil
	}

	// A NIL IDENTITY IS RECORDED, NOT DEREFERENCED AND NOT SKIPPED.
	//
	// authorize() admits a nil identity -- it guards on it three separate
	// times (the rate-limit check, the IdentityGated branch, the
	// tenant-IAM-sealed branch) -- and then reaches here, where reading
	// identity.Subject would panic. Every transport that calls authorize()
	// today refuses a nil identity before doing so (grpc.go:77, grpc.go:212,
	// rest.go:222, quic.go:44, mcp.go:38, AuthorizeTenant), so the panic is
	// not live on any path a request can take. It is guarded anyway: a
	// function that defends against nil three times and then dereferences is
	// one edit away from being wrong, and go-qrdb#335 is the change that makes
	// a wired audit logger the universal case rather than the configured one.
	//
	// SKIPPING the record was the other option and is the wrong one here. An
	// unattributable authorization decision is more interesting to an auditor,
	// not less, and silently not recording it is the exact failure mode this
	// issue exists to remove. anonymousActor is a sentinel rather than an
	// empty string so the record cannot be confused with an identity that
	// simply carried no subject.
	subject, tenantID := anonymousActor, ""
	if identity != nil {
		subject, tenantID = identity.Subject, identity.TenantID
	}

	result := audit.ResultSuccess
	if err != nil {
		result = audit.ResultDenied
	}

	entry := &audit.Entry{
		Timestamp: time.Now().UTC(),
		Subject:   subject,
		TenantID:  tenantID,
		Action:    "auth.authorize",
		Resource:  perm.String(),
		Result:    result,
	}

	auditErr := m.audit.Log(ctx, entry)
	if auditErr == nil {
		return nil
	}

	auditWriteFailures().With(auditFailureOutcomes[m.auditFailure]).Inc()

	slog.Error("auth/middleware: audit log failed",
		"error", auditErr,
		"subject", subject,
		"tenant", tenantID,
		"permission", perm.String(),
		"policy", m.auditFailure.String(),
	)

	if m.auditFailure == audit.FailOpen {
		return nil
	}
	return &audit.GateError{Component: authorizeComponent, Err: auditErr}
}

// anonymousActor is the Subject recorded for an authorization decision that
// reached the audit log with no identity attached.
//
// It is a sentinel, not the empty string: an empty Subject already occurs
// legitimately (a first-boot bootstrap write, a certificate with no CN), and
// an auditor must be able to tell "no identity was present" from "the identity
// present had no subject". The angle brackets cannot appear in a certificate
// CN, a SPIFFE SAN or a username, so it cannot be forged by a caller naming
// itself this.
const anonymousActor = "<anonymous>"

// AuthorizeTenant authorizes the authenticated identity in ctx to perform a
// tenant-scoped operation against an explicit target tenant. It is intended
// for handlers where the true target tenant is carried in the request body
// or URL (for example the per-tenant barrier {id}) rather than implied by
// the identity's own tenant.
//
// The identity is read from the context (it must have been authenticated by
// the surrounding middleware). The target tenant is injected into the
// context so the RBAC authorizer's scope rule evaluates against the request
// target, not the ambient tenant. For a ScopeTenantOnly permission this
// denies the system tenant and any cross-tenant access; the decision is
// audited like any other authorize call.
//
// Returns auth.ErrUnauthenticated when no identity is present in ctx.
func (m *AuthMiddleware) AuthorizeTenant(ctx context.Context, targetTenantID string, perm permission.Permission) error {
	identity := auth.IdentityFromContext(ctx)
	if identity == nil {
		return auth.ErrUnauthenticated
	}
	ctx = tenant.WithTenantID(ctx, targetTenantID)
	return m.authorize(ctx, identity, perm)
}

// authMethod carries HOW a request authenticated, and — separately — whether
// that is something the caller could observe at all.
//
// The two facts must not share a representation. Encoding "unobservable" as an
// empty name let an authenticator that names itself "" be treated as
// unobservable and skip the policy check entirely, which is precisely the
// bypass the policy exists to prevent (Codex, PR #198). A flag cannot be
// produced by accident: only unknownMethod() sets it, and only the two paths
// that genuinely cannot see a method call it.
type authMethod struct {
	name  string
	known bool
}

// knownMethod records a method the caller observed, whatever it is named.
func knownMethod(name string) authMethod { return authMethod{name: name, known: true} }

// unknownMethod records that the caller could not observe a method at all — a
// forwarded on-behalf assertion, or a capability module's own session-token
// plane. It is not "no method"; it is "not ours to know".
func unknownMethod() authMethod { return authMethod{} }

// checkAuthMethod enforces a user's permitted-authentication-method policy
// against the method that actually authenticated this request.
//
// It permits the request in two cases, both statements rather than oversights:
//
//   - The policy is EMPTY. User.AuthMethods empty means qrdb states no
//     restriction, so every account written before the field existed keeps
//     working. Enforcing an empty list as "nothing is allowed" would lock out
//     the entire fleet the moment this shipped.
//   - The method is UNOBSERVABLE. Only the authenticator chain qrdb owns can
//     name the method; a forwarded on-behalf assertion and a capability
//     module's session token cannot, and refusing them on a policy qrdb could
//     not evaluate would deny a request on no evidence.
//
// Everything else FAILS CLOSED, including an observed method with a blank or
// unnormalizable name: it cannot match any stored policy entry, and this is an
// authentication gate.
//
// Comparison uses the SAME normalization the store applied when the policy was
// written, so a policy stored as "webauthn" matches an authenticator named
// "WebAuthn". Normalizing only one side is how a policy silently stops matching
// the thing it was written to allow.
func checkAuthMethod(u *user.User, method authMethod) error {
	if u == nil || len(u.AuthMethods) == 0 || !method.known {
		return nil
	}
	refuse := func(name string) error {
		return &AuthMethodNotPermittedError{
			Username:  u.Username,
			TenantID:  u.TenantID,
			Method:    name,
			Permitted: u.AuthMethods,
		}
	}
	normalized, err := user.NormalizeAuthMethods([]string{method.name})
	if err != nil || len(normalized) == 0 {
		// An observed method whose name does not survive normalization — blank,
		// whitespace — matches nothing. A misconfigured authenticator must not
		// become a way past the policy.
		return refuse(method.name)
	}
	for _, permitted := range u.AuthMethods {
		if permitted == normalized[0] {
			return nil
		}
	}
	return refuse(normalized[0])
}
