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

// Package wiring is the single, shared constructor for the production auth
// enforcement stack — RBAC authorizer, optional ABAC decorator, mTLS
// authenticator, and the resulting AuthMiddleware — that gates every
// permission-checked route on go-qrdb's unified multi-protocol server
// (REST, gRPC, unix, QUIC, MCP, MySQL, PostgreSQL).
//
// cmd/qrdbd/start.go and sdk/go/node.Node both call BuildAuthMiddleware so
// that a daemon-run node (qrdbd) and a node embedded by a downstream binary
// (go-xkms's xkmsd, go-quicbaas's quicbaasd via NewStandaloneNode /
// NewClusteredNode) enforce authentication and authorization identically.
// Divergent wiring between the two paths is exactly how an embedded node
// previously served every RBAC-gated route unauthenticated: cmd/qrdbd/
// start.go built and installed server.WithAuthMiddleware, sdk/go/node/
// node.go never did.
package wiring

import (
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// AuthorizerConfig configures the layered authorizer construction (RBAC,
// optionally decorated with ABAC) shared by BuildAuthMiddleware and any
// caller that needs the bare auth.Authorizer without a full AuthMiddleware
// (e.g. sdk/go/node's embedded in-process transport).
type AuthorizerConfig struct {
	// Authorizer, when non-nil, is used directly instead of constructing a
	// fresh RBAC[+ABAC] stack from RoleService/ABAC*, and RoleService is
	// then NOT required. This lets a caller that already built the
	// composed authorizer (e.g. sdk/go/node, so its embedded in-process
	// transport and the network-transport AuthMiddleware share the EXACT
	// SAME authorizer instance) reuse it rather than standing up a second,
	// independently-configured one. A live ABAC policy reload on the
	// shared instance then reaches every transport, keeping qrdb the
	// single authorization decision point.
	Authorizer auth.Authorizer

	// RoleService resolves an identity's roles into their granted
	// permissions. Required unless Authorizer is set.
	RoleService role.RoleServicer

	// RoleServiceFallback is an optional StaleRead-backed role store
	// consulted when RoleService fails to resolve permissions with a
	// transient error (Raft ReadIndex not yet warm, host draining). Nil
	// disables the fallback.
	RoleServiceFallback role.RoleServicer

	// StrictSeparation enables strict separation-of-duties posture
	// (rbac.WithStrictSeparation): the Owner convenience role is voided.
	// Callers resolve this as fips140.Enabled() || <config flag>.
	StrictSeparation bool

	// ABACEnabled activates the ABAC policy decorator layered over RBAC.
	// When false and ABACPolicies is empty, the RBAC authorizer is
	// returned undecorated.
	ABACEnabled bool

	// ABACPolicyDir is a directory of YAML ABAC policy files loaded when
	// ABACEnabled is true (or ABACPolicies is non-empty). Load or compile
	// failures abort construction (fail closed).
	ABACPolicyDir string

	// ABACPolicies are caller-supplied, programmatically constructed ABAC
	// policies (e.g. sdk/go/node's WithABACPolicies option). They compose
	// with ABACPolicyDir; either being non-empty activates the decorator.
	ABACPolicies []*abac.Policy
}

// BuildAuthorizer constructs the canonical RBAC[+ABAC] auth.Authorizer:
// rbac.New(cfg.RoleService, ...) optionally decorated via DecorateABAC. When
// cfg.Authorizer is non-nil it is returned directly and no new RBAC/ABAC
// stack is constructed (see AuthorizerConfig.Authorizer). Otherwise
// RoleService is required; its absence returns *MissingServiceError so a
// caller that intends to enforce authorization fails loudly rather than
// silently admitting every request.
func BuildAuthorizer(cfg AuthorizerConfig) (auth.Authorizer, error) {
	if cfg.Authorizer != nil {
		return cfg.Authorizer, nil
	}
	if cfg.RoleService == nil {
		return nil, &MissingServiceError{Service: "role"}
	}

	rbacOpts := []rbac.Option{rbac.WithStrictSeparation(cfg.StrictSeparation)}
	if cfg.RoleServiceFallback != nil {
		rbacOpts = append(rbacOpts, rbac.WithRoleServiceFallback(cfg.RoleServiceFallback))
	}
	rbacAuthz := rbac.New(cfg.RoleService, rbacOpts...)

	return DecorateABAC(rbacAuthz, cfg.ABACEnabled, cfg.ABACPolicyDir, cfg.ABACPolicies)
}

// DecorateABAC wraps inner with the ABAC policy decorator (abac.Decorate)
// when enabled is true or policies is non-empty; otherwise inner is
// returned unchanged (zero overhead, no decoration). policyDir and
// policies compose: both may be supplied together. Any load or compile
// failure is returned directly from pkg/auth/abac — already a typed error
// (e.g. *abac.PolicyDirError, *abac.PolicyParseError) — so callers fail
// closed rather than running with a partially applied policy set.
func DecorateABAC(inner auth.Authorizer, enabled bool, policyDir string, policies []*abac.Policy) (auth.Authorizer, error) {
	if !enabled && len(policies) == 0 {
		return inner, nil
	}
	opts := make([]abac.Option, 0, 2)
	if policyDir != "" {
		opts = append(opts, abac.WithPolicyDir(policyDir))
	}
	if len(policies) > 0 {
		opts = append(opts, abac.WithPolicies(policies))
	}
	return abac.Decorate(inner, opts...)
}

// AuthMiddlewareConfig configures the canonical mTLS-authenticated,
// RBAC[+ABAC]-authorized AuthMiddleware shared by cmd/qrdbd and any
// embedder of an SDK node (sdk/go/node).
type AuthMiddlewareConfig struct {
	// Enabled mirrors the operator's auth.enabled configuration. When
	// false, BuildAuthMiddleware returns (nil, nil): the caller must not
	// install the middleware on any transport, running unauthenticated by
	// explicit operator choice — never as a silent default.
	Enabled bool

	// Authorizer, when non-nil, is forwarded to BuildAuthorizer and used
	// directly instead of constructing a fresh RBAC[+ABAC] stack. See
	// AuthorizerConfig.Authorizer.
	Authorizer auth.Authorizer

	// RoleService and UserService back the RBAC authorizer and the
	// identity-enrichment/enabled-check respectively. UserService is
	// always required when Enabled is true, RoleService even when Authorizer
	// is set: every authenticated request's TenantOwner is decided against
	// it. Either missing requirement returns *MissingServiceError.
	RoleService role.RoleServicer
	UserService user.UserServicer

	// RoleServiceFallback and UserServiceFallback are optional
	// StaleRead-backed stores consulted when the primary linearizable
	// store fails with a transient error. See pkg/auth/middleware and
	// pkg/auth/rbac for the fallback rationale. Nil disables the
	// respective fallback (the SDK node's lifecycle.Node carries a single
	// Role/User service, so it always passes nil here).
	RoleServiceFallback role.RoleServicer
	UserServiceFallback user.UserServicer

	// StrictSeparation enables strict separation-of-duties posture. See
	// AuthorizerConfig.StrictSeparation.
	StrictSeparation bool

	// SPIFFETrustDomain, when non-empty, pins the mTLS authenticator to a
	// SPIFFE trust domain (auth.NewMTLSAuthenticatorWithTrustDomain);
	// empty uses auth.NewMTLSAuthenticator with the legacy Subject-OU
	// tenant fallback.
	SPIFFETrustDomain string

	// CertProvider, when non-nil, turns on per-request certificate-revocation
	// enforcement: the mTLS authenticator consults it on every request and
	// refuses a peer whose serial has been revoked.
	//
	// Nil means revocation is RECORDED AND DISPLAYED BUT NOT ENFORCED — a
	// revoked certificate keeps authenticating until it expires. That is the
	// only posture available to a node with no certificate authority wired,
	// and it is a field rather than an implicit default so the choice is
	// visible at the wiring site.
	CertProvider cert.CertProvider

	// IssuerVerifier, when non-nil, enables the issuer-tenant binding check
	// (auth.WithIssuerVerifier): a verified client certificate's issuing CA
	// must be the cluster CA (Key A, any tenant) or the Key B CA registered
	// for the certificate's OWN claimed tenant.
	//
	// This MUST be the SAME *clienttrust.Pool instance the composer wired
	// onto its listener TLS config (pkg/clienttrust.Pool.Wire) and passed
	// to server.WithTLS -- the pool and the binding are the same object by
	// design (go-qrdb#560 review, B2-2), so Key B is never trusted at the
	// TLS layer without this check also being enforced here and in the
	// on-behalf forwarding path (WithIssuerVerifier on the AuthMiddleware
	// itself, set below). Passing a DIFFERENT Pool, or none at all, is a
	// wiring bug: every CA in the dynamic trust pool would authenticate any
	// tenant it names, unchecked.
	IssuerVerifier auth.IssuerVerifier

	// DelegatedPrincipal names the service principal the node accepts
	// tenant-issued delegations for (auth.WithDelegatedPrincipal): a client
	// certificate a tenant's own login CA signed, naming this principal and
	// the scope it may act with inside that tenant. Empty -- the default --
	// refuses every delegation. A delegation is also refused unless
	// IssuerVerifier, CertProvider (revocation) and AuditLogger are wired:
	// each is a check it cannot be admitted without.
	DelegatedPrincipal string

	// PendingTenantAdmins, when non-nil, confines the trust-on-first-init
	// window of a tenant barrier (middleware.WithPendingTenantAdmins): a
	// dedicated-isolation tenant onboarded with a first admin may have its
	// barrier initialized only with that admin's certificate, and under
	// MTLSBindingPolicy "required" a tenant onboarded with none may not have
	// it initialized by any certificate. qrdb's composers pass the node's
	// lifecycle.Node.PendingAdmins -- the store tenant onboarding writes.
	PendingTenantAdmins middleware.PendingTenantAdmins

	// CertTargetGuard is the certificate target guard users:add_cert_binding
	// is held to, the same one certs:issue and certs:enroll apply
	// (lifecycle.Node.CertTargets). Nil leaves add_cert_binding failing
	// closed.
	CertTargetGuard middleware.CertTargetGuard

	// RevocationCacheTTL is how long a NOT-REVOKED answer is cached on the
	// authentication path.
	//
	// IT IS A SECURITY WINDOW: the maximum time a certificate that has just
	// been revoked continues to authenticate on a node that had already seen
	// it. Zero or less disables caching of that answer, so every request
	// consults replicated state. It has no effect when CertProvider is nil.
	//
	// A REVOKED answer is cached without expiry and is unaffected by this
	// value, because revocation in go-qrdb is a one-way door (pkg/cert).
	RevocationCacheTTL time.Duration

	// ABACEnabled, ABACPolicyDir, and ABACPolicies configure the optional
	// ABAC decorator. See AuthorizerConfig.
	ABACEnabled   bool
	ABACPolicyDir string
	ABACPolicies  []*abac.Policy

	// AuditLogger, when non-nil, records every authorization decision
	// (middleware.WithAuditLogger).
	AuditLogger audit.Logger

	// AuditFailurePolicy selects what the middleware does when AuditLogger
	// cannot record a decision (middleware.WithAuditFailurePolicy).
	//
	// The ZERO VALUE is audit.FailClosed, so an embedder that never sets this
	// field gets the secure posture -- the request is refused rather than
	// granted unrecorded. Resolve an operator's configured string with
	// audit.ParseFailurePolicy at the call site, which fails the boot on an
	// unrecognised value rather than guessing a posture.
	//
	// It is the SAME value the security-officer gate reads
	// (auth.WithSOAuditFailurePolicy, rest.WithAuditFailurePolicy): go-qrdb#445
	// ruled one posture across both assertions, so an embedder that resolves
	// auth.audit_failure_policy once must pass the result to all three.
	AuditFailurePolicy audit.FailurePolicy

	// MTLSBindingPolicy is the fleet-wide mTLS enrolment posture
	// (middleware.WithMTLSBindingPolicy). The ZERO VALUE is
	// auth.MTLSBindingRequired (go-qrdb#548): FedRAMP, HIPAA and PCI DSS 4.0
	// require possession-based authentication of every account, so an
	// embedder that never sets this field gets the compliant posture.
	// Resolve the operator's auth.mtls_binding_policy with
	// auth.ParseMTLSBindingPolicy, which fails the boot on an unrecognised
	// value. Every composer in this module enrols the bootstrap admin's
	// certificate as part of first boot so the default does not lock a fresh
	// node out of itself; firstboot.VerifyAdminCertBinding is the boot-time
	// check that proves it (go-qrdb#517, go-qrdb#548).
	MTLSBindingPolicy auth.MTLSBindingPolicy

	// PeerAttestation, when non-nil, installs the attested-caller gate
	// (middleware.WithPeerAttestationGate, go-qrdb#278): for its gated
	// permissions a request is admitted only when the injected verifier
	// reports the peer on the connection as currently attested. Build it with
	// middleware.NewPeerAttestationGate, which refuses an incomplete policy.
	// Nil -- the default -- gates nothing.
	PeerAttestation *middleware.PeerAttestationGate

	// RateLimitPolicies declares, per permission, the request-throttling
	// Policy go-qrdb#201's centralized rate limiter enforces
	// (middleware.WithRateLimitPolicies). Nil -- the default -- declares no
	// policy for any permission, so BuildAuthMiddleware installs no limiter
	// at all: rate limiting is an explicit per-permission opt-in an embedder
	// takes by constructing a *ratelimit.Registry and calling Register on
	// it, never a default this constructor imposes.
	RateLimitPolicies *ratelimit.Registry

	// RateLimiter overrides the enforcement engine RateLimitPolicies runs
	// against. Nil, with RateLimitPolicies non-nil, constructs the
	// production ratelimit.NewTokenBucketLimiter(); an embedder that already
	// runs one shared limiter across other subsystems may supply it here
	// instead so go-qrdb#201's mechanism reuses that instance rather than
	// starting a second background reaper goroutine and a second bucket set.
	// Ignored when RateLimitPolicies is nil.
	RateLimiter ratelimit.Limiter

	// ExtraAuthenticators places embedder-supplied authenticators in the
	// chain, each at an explicit position relative to the built-in mTLS
	// authenticator: before it, after it, or in its place
	// (auth.AuthenticatorChain gives the exact order). The chain stays one
	// ordered, first-success-wins list on every transport, so the position
	// decides which credential wins for a request that presents more than one.
	// A placement with no valid position fails BuildAuthMiddleware with
	// *auth.InvalidChainPositionError, and replacing mTLS with nothing fails
	// it with *auth.EmptyAuthenticatorChainError. This is the extension seam for credential schemes qrdb deliberately
	// does not implement itself — e.g. go-quicbaas passes its OAuth 2.1
	// bearer-token validator here when embedding qrdb, per the MCP
	// authorization specification. Each authenticator resolves whatever
	// carrier it needs from the request context
	// (auth.HTTPAuthorizationFromContext for the caller's Authorization
	// value, which REST and MCP fill from the header and gRPC and Unix from
	// the "authorization" metadata). One whose tokens carry no observable
	// login method implements auth.UnobservableMethodAuthenticator.
	ExtraAuthenticators []auth.AuthenticatorPlacement

	// WrapAuthorizer, when non-nil, decorates the fully built
	// RBAC[+ABAC] authorizer with embedder-supplied authorization logic.
	// The wrapper receives the inner authorizer and returns the one the
	// middleware will use, so custom logic (OAuth scope checks, external
	// policy engines) can compose over — and defer to — the built-in
	// stack instead of replacing it (use Authorizer to replace outright).
	// Returning nil from the wrapper is a wiring bug and fails closed
	// with *MissingServiceError.
	WrapAuthorizer func(inner auth.Authorizer) auth.Authorizer
}

// revocationChecker returns the auth.RevocationChecker for the given
// certificate provider, or a nil INTERFACE when there is no provider.
//
// The nil-interface return is the point of this function. cert.NewRevocationCache
// returns a typed nil pointer for a nil provider, and a typed nil pointer stored
// in an interface is not a nil interface: the authenticator's `revocation != nil`
// guard would pass and every request would call through a nil receiver. Returning
// the interface's own nil keeps "no provider" and "enforcement off" the same
// statement.
func revocationChecker(provider cert.CertProvider, ttl time.Duration) auth.RevocationChecker {
	cache := cert.NewRevocationCache(provider, ttl)
	if cache == nil {
		return nil
	}
	return cache
}

// BuildAuthMiddleware constructs the production AuthMiddleware: an mTLS
// authenticator, the RBAC[+ABAC] authorizer built via BuildAuthorizer, and
// user-store identity enrichment — the exact stack cmd/qrdbd/start.go
// installs on every transport via server.WithAuthMiddleware.
//
// When cfg.Enabled is false, BuildAuthMiddleware returns (nil, nil): the
// caller must skip installing the middleware, matching qrdbd's own
// --auth-enabled=false posture (auth explicitly disabled by the operator).
// When cfg.Enabled is true, UserService is always REQUIRED, and RoleService
// is required unless cfg.Authorizer is set: their absence returns a
// *MissingServiceError so an embedder (e.g. an SDK node whose service graph
// failed to construct one of them) fails loudly at boot instead of
// silently serving every request unauthenticated.
func BuildAuthMiddleware(cfg AuthMiddlewareConfig) (*middleware.AuthMiddleware, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	// The role store is required even when an embedder supplies its own
	// Authorizer: every authenticated request's TenantOwner is decided
	// against it (role.IsTenantOwner), and an identity whose ownership cannot
	// be decided is never admitted as "not owner".
	if cfg.RoleService == nil {
		return nil, &MissingServiceError{Service: "role"}
	}
	if cfg.UserService == nil {
		return nil, &MissingServiceError{Service: "user"}
	}

	authorizer, err := BuildAuthorizer(AuthorizerConfig{
		Authorizer:          cfg.Authorizer,
		RoleService:         cfg.RoleService,
		RoleServiceFallback: cfg.RoleServiceFallback,
		StrictSeparation:    cfg.StrictSeparation,
		ABACEnabled:         cfg.ABACEnabled,
		ABACPolicyDir:       cfg.ABACPolicyDir,
		ABACPolicies:        cfg.ABACPolicies,
	})
	if err != nil {
		return nil, err
	}

	// mTLS is the ONE first-contact authenticator every transport shares:
	// it authenticates the admin client certificate firstboot issues and
	// binds via Subject CN, the same identifier BindAdminCert's SPKI
	// thumbprint indexes the admin user under (see pkg/firstboot). Pin
	// the operator-configured SPIFFE trust domain when set so the
	// authenticator derives the tenant from the peer certificate's
	// tenant-scoped SPIFFE SAN and rejects foreign trust domains
	// (fail-closed).
	//
	// It is also where certificate revocation is ENFORCED. cert.RevocationCache
	// is nil when no CertProvider is wired, and auth.WithRevocationChecker of a
	// nil checker leaves enforcement off — the same posture as before, reached
	// explicitly.
	revocation := revocationChecker(cfg.CertProvider, cfg.RevocationCacheTTL)
	mtlsOpts := []auth.MTLSOption{auth.WithRevocationChecker(revocation)}
	if cfg.IssuerVerifier != nil {
		mtlsOpts = append(mtlsOpts, auth.WithIssuerVerifier(cfg.IssuerVerifier))
	}
	if cfg.DelegatedPrincipal != "" {
		mtlsOpts = append(mtlsOpts, auth.WithDelegatedPrincipal(cfg.DelegatedPrincipal))
	}
	mtlsAuthenticator := auth.NewMTLSAuthenticator(mtlsOpts...)
	if cfg.SPIFFETrustDomain != "" {
		mtlsAuthenticator = auth.NewMTLSAuthenticatorWithTrustDomain(cfg.SPIFFETrustDomain, mtlsOpts...)
	}
	// The embedder's authenticators take the positions they asked for around
	// mTLS -- or its place -- in the one chain every transport shares.
	chain, err := auth.AuthenticatorChain(mtlsAuthenticator, cfg.ExtraAuthenticators)
	if err != nil {
		return nil, err
	}

	if cfg.WrapAuthorizer != nil {
		authorizer = cfg.WrapAuthorizer(authorizer)
		if authorizer == nil {
			return nil, &MissingServiceError{Service: "wrapped authorizer"}
		}
	}

	opts := make([]middleware.Option, 0, len(chain)+12)
	for _, a := range chain {
		opts = append(opts, middleware.WithAuthenticator(a))
	}
	opts = append(opts,
		middleware.WithUserService(cfg.UserService),
		middleware.WithRoleResolver(cfg.RoleService),
		// The SAME checker on both, sharing one cache: the on-behalf
		// forwarding path never runs the authenticator chain, so it needs the
		// gate wired separately or a revoked cluster node keeps forwarding.
		middleware.WithRevocationChecker(revocation),
		middleware.WithMTLSBindingPolicy(cfg.MTLSBindingPolicy),
		middleware.WithPeerAttestationGate(cfg.PeerAttestation),
	)
	if cfg.IssuerVerifier != nil {
		// The SAME verifier gates the on-behalf forwarding path
		// (authenticateOnBehalf), which bypasses the authenticator chain
		// entirely and so does not inherit the mTLS authenticator's own
		// check above (go-qrdb#560 review, B2-1).
		opts = append(opts, middleware.WithIssuerVerifier(cfg.IssuerVerifier))
	}
	if cfg.DelegatedPrincipal != "" {
		// A forwarded delegated request is verified by the same
		// authenticator a presented delegation is, on this node.
		opts = append(opts, middleware.WithDelegationVerifier(mtlsAuthenticator))
	}
	if cfg.CertTargetGuard != nil {
		opts = append(opts, middleware.WithCertTargetGuard(cfg.CertTargetGuard))
	}
	if cfg.PendingTenantAdmins != nil {
		opts = append(opts, middleware.WithPendingTenantAdmins(cfg.PendingTenantAdmins))
	}
	if cfg.UserServiceFallback != nil {
		opts = append(opts, middleware.WithUserServiceFallback(cfg.UserServiceFallback))
	}
	if cfg.RoleServiceFallback != nil {
		opts = append(opts, middleware.WithRoleResolverFallback(cfg.RoleServiceFallback))
	}
	if cfg.AuditLogger != nil {
		opts = append(opts,
			middleware.WithAuditLogger(cfg.AuditLogger),
			// Applied only alongside a logger: the option's warning names a
			// property that a middleware with no audit logger at all never had
			// to give up, and emitting it there would train operators to
			// ignore it.
			middleware.WithAuditFailurePolicy(cfg.AuditFailurePolicy),
		)
	}
	if cfg.RateLimitPolicies != nil {
		limiter := cfg.RateLimiter
		if limiter == nil {
			limiter = ratelimit.NewTokenBucketLimiter()
		}
		opts = append(opts,
			middleware.WithRateLimiter(limiter),
			middleware.WithRateLimitPolicies(cfg.RateLimitPolicies),
		)
	}

	return middleware.New(authorizer, opts...), nil
}
