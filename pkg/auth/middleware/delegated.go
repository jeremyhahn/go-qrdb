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
	"crypto/x509"
	"encoding/base64"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/forward"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// Audit metadata keys a delegated request's authorization record carries.
const (
	delegationPrincipalAuditKey = "delegation_principal"
	delegationSerialAuditKey    = "delegation_serial"
	delegationScopeAuditKey     = "delegation_scope"
)

// DelegationVerifier verifies a delegation certificate presented as DER and
// returns the delegated identity. *auth.MTLSAuthenticator implements it
// (VerifyDelegation) with the same checks it applies to a certificate
// presented on the connection.
type DelegationVerifier interface {
	VerifyDelegation(ctx context.Context, der []byte) (*auth.Identity, error)
}

// WithDelegationVerifier sets the verifier the on-behalf forwarding path
// checks a forwarded delegation certificate with: the node's own mTLS
// authenticator, so a forwarded delegation is held to exactly the rules a
// presented one is. Without it, every forwarded delegation is refused.
func WithDelegationVerifier(v DelegationVerifier) Option {
	return func(m *AuthMiddleware) {
		m.delegations = v
	}
}

// authenticateForwardedDelegation rebuilds the original caller of a forwarded
// request that authenticated with a delegation. The forwarding node's word is
// not taken for it: the certificate the assertion carries is verified again
// here, and the result must name the asserted subject and tenant. Every
// refusal but the verifier's own is the opaque on-behalf refusal.
func (m *AuthMiddleware) authenticateForwardedDelegation(ctx context.Context, assertion *forward.CallerAssertion) (context.Context, *auth.Identity, error) {
	if m.delegations == nil || assertion.HolderProfile != "" {
		return ctx, nil, errOnBehalfRejected
	}
	der, err := base64.StdEncoding.DecodeString(assertion.Delegation)
	if err != nil {
		return ctx, nil, errOnBehalfRejected
	}
	identity, err := m.delegations.VerifyDelegation(ctx, der)
	if err != nil {
		return ctx, nil, err
	}
	if identity.Subject != assertion.Subject || identity.TenantID != assertion.TenantID {
		return ctx, nil, errOnBehalfRejected
	}
	identity.Metadata[authSourceMetadataKey] = authSourceOnBehalf
	identity.Metadata["forwarder_nodeid"] = assertion.ForwarderNodeID
	return m.finalizeIdentity(ctx, identity, unknownMethod())
}

// AuthenticateDelegation authenticates an in-process caller presenting a
// tenant-issued delegation certificate, chain[0], and returns a context
// bound to the delegated identity and its tenant, exactly as a request
// presenting it over mTLS would be. The verification is the delegation
// verifier's (WithDelegationVerifier), the one a presented and a forwarded
// delegation both pass: the trust pool, the critical delegation extension,
// the principal, the lifetime, revocation, the tenant binding and the
// issuer being that tenant's own login CA. The identity then passes through
// finalizeIdentity, which grants the delegation's scope intersected with the
// principal's own grants.
//
// The certificates after chain[0] are not consulted: a delegation is valid
// only when its direct issuer is the delegating tenant's login CA, which the
// trust pool holds as a root, so no intermediate can take part in it.
//
// A node that accepts no delegations, an empty chain, and a certificate that
// fails any check are refused with a *auth.DelegationRefusedError (a revoked
// one with auth.ErrCertificateRevoked); nothing falls back to another
// identity.
func (m *AuthMiddleware) AuthenticateDelegation(ctx context.Context, chain []*x509.Certificate) (context.Context, *auth.Identity, error) {
	if m.delegations == nil {
		return ctx, nil, &auth.DelegationRefusedError{Reason: "this node accepts no delegations", Err: auth.ErrForeignDelegationPrincipal}
	}
	if len(chain) == 0 || chain[0] == nil {
		return ctx, nil, &auth.DelegationRefusedError{Reason: "no certificate"}
	}
	identity, err := m.delegations.VerifyDelegation(ctx, chain[0].Raw)
	if err != nil {
		return ctx, nil, err
	}
	return m.finalizeIdentity(ctx, identity, knownMethod(auth.MethodMTLS))
}

// revalidateDelegation verifies identity's delegation certificate again,
// through the same delegation verifier that admitted it, and returns that
// verifier's typed refusal when it no longer holds. It is a no-op for an
// identity that carries no delegation.
//
// A delegation is verified at every authorization decision, not only when
// the identity was built: a context bound by AuthenticateDelegation is held
// by an embedder and reused for as many in-process requests as it likes, so
// a certificate revoked, or past its lifetime, after the context was built
// must stop being honoured on the next request, exactly as a request
// presenting it over mTLS would be refused.
func (m *AuthMiddleware) revalidateDelegation(ctx context.Context, identity *auth.Identity) error {
	d, ok := auth.DelegationOf(identity)
	if !ok {
		return nil
	}
	if m.delegations == nil {
		return &auth.DelegationRefusedError{Reason: "this node accepts no delegations", Err: auth.ErrForeignDelegationPrincipal}
	}
	_, err := m.delegations.VerifyDelegation(ctx, d.Certificate)
	return err
}

// finalizeDelegated admits an identity that authenticated with a verified
// tenant-issued delegation (auth.DelegationOf). It is the one way a service
// principal's subject reaches a request from the network, and only the mTLS
// authenticator can produce the identity: the delegation marker is
// unexported in pkg/auth.
//
// The identity holds no roles, no role bindings, no record attributes and no
// user account -- a service principal has none. Its permissions are the
// delegation's scope intersected with the grants the embedding host declared
// for the principal (permission.RolePermissions), resolved now, on every
// request: the tenant can narrow the principal's authority, never widen it,
// and a grant the host withdraws stops applying at once.
func (m *AuthMiddleware) finalizeDelegated(ctx context.Context, identity *auth.Identity, d auth.Delegation) (context.Context, *auth.Identity, error) {
	identity.TenantID = tenantid.Normalize(identity.TenantID)
	identity.Roles = nil
	identity.RoleBindings = nil
	identity.Attributes = nil
	// TenantOwner has one source on this path too, decideTenantOwner: the
	// verifier's claim is cleared, and an identity with no roles is decided
	// not owner (and refused when no role store is configured, as every
	// other authenticated identity is).
	identity.TenantOwner = false
	if err := m.decideTenantOwner(ctx, identity); err != nil {
		return ctx, nil, err
	}
	identity.ScopePermissions = delegatedPermissions(d)
	ctx = auth.WithIdentity(ctx, identity)
	return tenant.WithTenantID(ctx, identity.TenantID), identity, nil
}

// delegatedPermissions returns the permissions a delegation confers: the
// entries of its scope the principal itself holds.
func delegatedPermissions(d auth.Delegation) []string {
	perms := make([]string, 0, len(d.Scope))
	for _, p := range d.Scope {
		if permission.HasRoleGrant(d.Principal, permission.Permission(p)) {
			perms = append(perms, p)
		}
	}
	return perms
}

// delegationAuditMetadata returns the metadata a delegated identity's
// authorization record carries, and whether identity is delegated. The
// record is keyed by the delegating tenant, so it lands in that tenant's
// trail.
func delegationAuditMetadata(identity *auth.Identity) (map[string]string, bool) {
	d, ok := auth.DelegationOf(identity)
	if !ok {
		return nil, false
	}
	return map[string]string{
		delegationPrincipalAuditKey: d.Principal,
		delegationSerialAuditKey:    d.Serial,
		delegationScopeAuditKey:     strings.Join(d.Scope, " "),
	}, true
}
