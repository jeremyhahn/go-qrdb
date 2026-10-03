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
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// certBoundMethods names the authentication methods whose credential IS the
// X.509 client certificate on the connection, and for which the presented
// certificate can therefore be compared against the account's enrolment.
//
// A map, rather than an equality test against auth.MethodMTLS, because the set
// is the thing being stated: a second certificate-presenting authenticator
// added to the chain must be added HERE to be gated, and a reader asking "what
// does this check apply to" gets an answer rather than a chain of conditions.
// The values are CANONICAL method names (auth.CanonicalAuthMethod), the same
// form a user's permitted-method policy is compared in.
var certBoundMethods = map[string]struct{}{
	auth.MethodMTLS: {},
}

// isCertBoundMethod reports whether the method that authenticated this request
// presented an X.509 client certificate this node can compare against an
// enrolment.
//
// An UNOBSERVABLE method is never cert-bound. That is the load-bearing half:
// a forwarded on-behalf assertion and a capability module's own session-token
// plane both reach finalizeIdentity with unknownMethod(), and on both the
// certificate on the connection belongs to somebody other than the subject —
// the forwarding cluster node, or whoever terminated the module's TLS.
// Comparing an enrolment against that certificate would refuse every forwarded
// write to an enrolled user. The layer that ran the ceremony is the one that
// knows what credential the subject presented, exactly as for the
// permitted-method policy.
//
// The name is canonicalized with the SAME function the permitted-method check
// compares with, so the two cannot drift into disagreeing about what "mtls" is
// spelled like.
func isCertBoundMethod(method authMethod) bool {
	if !method.known {
		return false
	}
	_, ok := certBoundMethods[auth.CanonicalAuthMethod(method.name)]
	return ok
}

// checkCertBinding enforces that a certificate-authenticated request presents a
// certificate the named account actually enrolled.
//
// Without it, an mTLS identity's entire security rests on the CA never issuing
// a certificate whose Subject CN and OU name someone else: MTLSAuthenticator
// derives the subject from the CN and the tenant from the SPIFFE tenant SAN or
// the first Subject OU, finalizeIdentity looks THAT up in the user store, and
// nothing ever asked whether the presenting party holds the account's key. Any
// unguarded issuance path is then a full authentication bypass rather than a
// misfiled certificate. This is the layer that contains one.
//
// It permits the request in three cases, all statements rather than
// oversights:
//
//   - The account has NO cert bindings AND the fleet's posture is
//     auth.MTLSBindingEnrolled (an explicit opt-out since go-qrdb#548; the
//     default is auth.MTLSBindingRequired). An empty binding set then states
//     no restriction — the same statement user.User.AuthMethods makes with an
//     empty policy, and for the same reason: enrolment is an operator act that
//     predates this gate in every existing deployment, and enforcing an empty
//     set as "nothing authenticates" would lock out every account nobody has
//     enrolled yet, including the first-boot admin on a node whose bootstrap
//     wired no CertBinder. The consequence is stated rather than hidden: an
//     unenrolled account is exactly as impersonable as it was before, and
//     enrolling it — or selecting auth.MTLSBindingRequired once everyone is
//     enrolled — is what closes it. Under MTLSBindingRequired the empty set is
//     refused with *CertBindingRequiredError (go-qrdb#517).
//   - The method is not CERT-BOUND. See isCertBoundMethod.
//   - The presented certificate matches a currently-valid binding. Matching is
//     user.User.MatchesCert, which is the single mTLS-auth predicate on the
//     user model — over the SubjectPublicKeyInfo, so a renewal for the same
//     key keeps working, and expiry-checked, so a decommissioned binding
//     cannot be replayed.
//
// Everything else FAILS CLOSED, including a cert-bound method whose peer
// certificate cannot be read back: the account has enrolled certificates and
// this request has produced none to compare, which is not an admission.
//
// The certificate is re-read from the connection state in ctx rather than
// carried on the identity. The identity's Metadata is a map the fallback and
// on-behalf paths construct themselves, so a thumbprint carried there would be
// a claim; the TLS state is the fact the authenticator itself read.
func checkCertBinding(ctx context.Context, u *user.User, method authMethod, policy auth.MTLSBindingPolicy) error {
	if u == nil || !isCertBoundMethod(method) {
		return nil
	}
	if len(u.CertBindings) == 0 {
		if policy != auth.MTLSBindingRequired {
			return nil
		}
		return &CertBindingRequiredError{
			Username:            u.Username,
			TenantID:            u.TenantID,
			AccountResolved:     true,
			PresentedThumbprint: presentedThumbprint(ctx),
		}
	}
	cert, err := verifiedPeerCertificate(auth.TLSStateFromContext(ctx))
	if err != nil || cert == nil {
		return &CertBindingNotPermittedError{Username: u.Username, TenantID: u.TenantID}
	}
	if u.MatchesCert(cert) {
		return nil
	}
	return &CertBindingNotPermittedError{
		Username:            u.Username,
		TenantID:            u.TenantID,
		PresentedThumbprint: user.ComputeSPKIThumbprint(cert),
	}
}

// checkUnresolvedCertLogin applies the binding posture to a certificate login
// whose subject resolves to NO account — the user store answered not-found, or
// no user store is wired at all.
//
// Under auth.MTLSBindingEnrolled it admits, as before: the identity proceeds
// unenriched with no roles and RBAC default-denies whatever it asks for.
// Under auth.MTLSBindingRequired it refuses, because "every certificate login
// presents a certificate enrolled on the account it names" cannot hold for a
// login that names no account; admitting it would leave the one identity that
// by definition has no enrolment as the posture's exception.
//
// The unobservable-method paths are excluded for the reason isCertBoundMethod
// gives: the certificate on those connections is not the subject's.
func checkUnresolvedCertLogin(ctx context.Context, identity *auth.Identity, method authMethod, policy auth.MTLSBindingPolicy) error {
	if policy != auth.MTLSBindingRequired || identity == nil || !isCertBoundMethod(method) {
		return nil
	}
	return &CertBindingRequiredError{
		Username:            identity.Subject,
		TenantID:            identity.TenantID,
		PresentedThumbprint: presentedThumbprint(ctx),
	}
}

// presentedThumbprint is the SPKI thumbprint of the verified peer certificate
// in ctx, or "" when none can be read. It is carried on a refusal because it is
// the exact value an operator enrols to turn that refusal into an admission.
func presentedThumbprint(ctx context.Context) string {
	cert, err := verifiedPeerCertificate(auth.TLSStateFromContext(ctx))
	if err != nil || cert == nil {
		return ""
	}
	return user.ComputeSPKIThumbprint(cert)
}
