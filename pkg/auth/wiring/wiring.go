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
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
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
	// always required when Enabled is true; RoleService is required
	// unless Authorizer is set. Either missing requirement returns
	// *MissingServiceError.
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

	// ABACEnabled, ABACPolicyDir, and ABACPolicies configure the optional
	// ABAC decorator. See AuthorizerConfig.
	ABACEnabled   bool
	ABACPolicyDir string
	ABACPolicies  []*abac.Policy

	// AuditLogger, when non-nil, records every authorization decision
	// (middleware.WithAuditLogger).
	AuditLogger audit.Logger

	// ExtraAuthenticators appends embedder-supplied authenticators to the
	// chain AFTER the built-in mTLS authenticator. The chain is
	// first-success-wins, so mTLS peers keep their existing behavior and a
	// request without a client certificate falls through to the extras.
	// This is the extension seam for credential schemes qrdb deliberately
	// does not implement itself — e.g. go-quicbaas passes its OAuth 2.1
	// bearer-token validator here when embedding qrdb, per the MCP
	// authorization specification. Each authenticator resolves whatever
	// carrier it needs from the request context
	// (auth.HTTPAuthorizationFromContext for the raw Authorization header
	// on the HTTP transports, gRPC metadata on gRPC).
	ExtraAuthenticators []auth.Authenticator

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
	if cfg.Authorizer == nil && cfg.RoleService == nil {
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
	mtlsAuthenticator := auth.NewMTLSAuthenticator()
	if cfg.SPIFFETrustDomain != "" {
		mtlsAuthenticator = auth.NewMTLSAuthenticatorWithTrustDomain(cfg.SPIFFETrustDomain)
	}

	if cfg.WrapAuthorizer != nil {
		authorizer = cfg.WrapAuthorizer(authorizer)
		if authorizer == nil {
			return nil, &MissingServiceError{Service: "wrapped authorizer"}
		}
	}

	opts := []middleware.Option{
		middleware.WithAuthenticator(mtlsAuthenticator),
		middleware.WithUserService(cfg.UserService),
	}
	for _, a := range cfg.ExtraAuthenticators {
		if a == nil {
			continue
		}
		opts = append(opts, middleware.WithAuthenticator(a))
	}
	if cfg.UserServiceFallback != nil {
		opts = append(opts, middleware.WithUserServiceFallback(cfg.UserServiceFallback))
	}
	if cfg.AuditLogger != nil {
		opts = append(opts, middleware.WithAuditLogger(cfg.AuditLogger))
	}

	return middleware.New(authorizer, opts...), nil
}
