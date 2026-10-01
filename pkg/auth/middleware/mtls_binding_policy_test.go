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
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// These tests pin auth.mtls_binding_policy (go-qrdb#517) against the REAL
// mTLS authenticator and the REAL kvstore-backed user store, exactly as the
// enrolment gate's own tests do: the posture is a statement about what the
// production authentication path admits, so a double of either would assert
// the double.

// policyMiddleware wires the real authenticator over svc under policy.
func policyMiddleware(svc user.UserServicer, policy auth.MTLSBindingPolicy) *AuthMiddleware {
	return New(&mockAuthorizer{},
		WithAuthenticator(auth.NewMTLSAuthenticator()),
		WithUserService(svc),
		WithMTLSBindingPolicy(policy),
	)
}

// TestMTLSBindingPolicy_UnboundAccount_AdmittedUnderEnrolledRefusedUnderRequired
// is the issue's first verification item, both directions on ONE account and
// ONE certificate, so the policy is the only difference.
func TestMTLSBindingPolicy_UnboundAccount_AdmittedUnderEnrolledRefusedUnderRequired(t *testing.T) {
	svc := newRealUserStore(t)
	createUser(t, svc, "acme", "bob", []string{"operator"})
	cert := mintCert(t, "bob", "acme", nil, "")

	_, identity, err := policyMiddleware(svc, auth.MTLSBindingEnrolled).authenticate(mtlsContext(cert))
	if err != nil {
		t.Fatalf("enrolled posture refused an unbound account: %v", err)
	}
	if identity.Subject != "bob" || len(identity.Roles) != 1 {
		t.Fatalf("identity = %+v, want bob with the store's role", identity)
	}

	_, identity, err = policyMiddleware(svc, auth.MTLSBindingRequired).authenticate(mtlsContext(cert))
	var req *CertBindingRequiredError
	if !errors.As(err, &req) {
		t.Fatalf("required posture = (%+v, %v), want *CertBindingRequiredError", identity, err)
	}
	if req.Username != "bob" || req.TenantID != "acme" || !req.AccountResolved {
		t.Fatalf("error = %+v, want bob@acme with AccountResolved", req)
	}
	if req.PresentedThumbprint != user.ComputeSPKIThumbprint(cert.cert) {
		t.Fatalf("PresentedThumbprint = %q, want the refused certificate's SPKI thumbprint", req.PresentedThumbprint)
	}
	if got := ClassifyAuthError(err); got != AuthFailureUnauthenticated {
		t.Fatalf("ClassifyAuthError = %v, want AuthFailureUnauthenticated", got)
	}
	for _, want := range []string{`"required"`, "users:add_cert_binding", `"enrolled"`, req.PresentedThumbprint} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not name %q; an operator cannot act on it", err.Error(), want)
		}
	}
}

// TestMTLSBindingPolicy_Required_EnrollingThePresentedKeyAdmits is the
// migration path the refusal names: enrol the thumbprint it reported and the
// same certificate is admitted.
func TestMTLSBindingPolicy_Required_EnrollingThePresentedKeyAdmits(t *testing.T) {
	svc := newRealUserStore(t)
	bob := createUser(t, svc, "acme", "bob", nil)
	cert := mintCert(t, "bob", "acme", nil, "")
	m := policyMiddleware(svc, auth.MTLSBindingRequired)

	if _, _, err := m.authenticate(mtlsContext(cert)); err == nil {
		t.Fatal("precondition: unbound account admitted under required")
	}
	enroll(t, svc, bob.ID, cert)
	if _, _, err := m.authenticate(mtlsContext(cert)); err != nil {
		t.Fatalf("enrolled certificate refused under required: %v", err)
	}
	// A different key naming the same account is still the enrolled gate's
	// refusal, not the posture's.
	_, _, err := m.authenticate(mtlsContext(mintCert(t, "bob", "acme", nil, "")))
	var notBound *CertBindingNotPermittedError
	if !errors.As(err, &notBound) {
		t.Fatalf("foreign key under required = %v, want *CertBindingNotPermittedError", err)
	}
}

// TestMTLSBindingPolicy_UnknownSubject covers a certificate that names no
// account: admitted unenriched under enrolled (as before), refused under
// required, because no enrolment can exist for it.
func TestMTLSBindingPolicy_UnknownSubject(t *testing.T) {
	svc := newRealUserStore(t)
	cert := mintCert(t, "ghost", "acme", nil, "")

	_, identity, err := policyMiddleware(svc, auth.MTLSBindingEnrolled).authenticate(mtlsContext(cert))
	if err != nil || len(identity.Roles) != 0 {
		t.Fatalf("enrolled posture = (%+v, %v), want an unenriched identity", identity, err)
	}

	_, _, err = policyMiddleware(svc, auth.MTLSBindingRequired).authenticate(mtlsContext(cert))
	var req *CertBindingRequiredError
	if !errors.As(err, &req) || req.AccountResolved || req.Username != "ghost" {
		t.Fatalf("required posture = %v, want *CertBindingRequiredError for an unresolved ghost@acme", err)
	}
	if !strings.Contains(err.Error(), "names no account") {
		t.Fatalf("error %q does not say the account does not exist", err.Error())
	}
}

// TestMTLSBindingPolicy_NoUserStore: with no user store no account resolves,
// so required refuses every certificate login and enrolled admits as before.
func TestMTLSBindingPolicy_NoUserStore(t *testing.T) {
	cert := mintCert(t, "bob", "acme", nil, "")
	build := func(p auth.MTLSBindingPolicy) *AuthMiddleware {
		return New(&mockAuthorizer{}, WithAuthenticator(auth.NewMTLSAuthenticator()), WithMTLSBindingPolicy(p))
	}
	if _, _, err := build(auth.MTLSBindingEnrolled).authenticate(mtlsContext(cert)); err != nil {
		t.Fatalf("enrolled posture with no user store refused: %v", err)
	}
	var req *CertBindingRequiredError
	if _, _, err := build(auth.MTLSBindingRequired).authenticate(mtlsContext(cert)); !errors.As(err, &req) {
		t.Fatalf("required posture with no user store = %v, want *CertBindingRequiredError", err)
	}
}

// TestMTLSBindingPolicy_Required_UnobservableMethodIsNotGated pins the
// exclusion the issue asked to be decided: a forwarded on-behalf assertion and
// a module's session-token plane present no certificate of the subject's, so
// the posture cannot be evaluated against them here and does not refuse them.
func TestMTLSBindingPolicy_Required_UnobservableMethodIsNotGated(t *testing.T) {
	svc := newRealUserStore(t)
	createUser(t, svc, "acme", "bob", nil)
	nodeCert := mintCert(t, "node-1", "system", nil, "")
	m := policyMiddleware(svc, auth.MTLSBindingRequired)

	for name, identity := range map[string]*auth.Identity{
		"resolved account":   {Subject: "bob", TenantID: "acme"},
		"unresolved account": {Subject: "ghost", TenantID: "acme"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := m.finalizeIdentity(mtlsContext(nodeCert), identity, unknownMethod()); err != nil {
				t.Fatalf("unobservable method refused under required: %v", err)
			}
		})
	}
	// And the same identity through an OBSERVED certificate method is gated.
	if _, _, err := m.finalizeIdentity(mtlsContext(nodeCert), &auth.Identity{Subject: "bob", TenantID: "acme"}, knownMethod(auth.MethodMTLS)); err == nil {
		t.Fatal("observed mTLS method admitted an unbound account under required")
	}
}

// TestMTLSBindingPolicy_Required_NonCertMethodIsNotGated: the posture is about
// certificate logins. An account with no binding that authenticates by some
// other method the chain observes is the permitted-method policy's business.
func TestMTLSBindingPolicy_Required_NonCertMethodIsNotGated(t *testing.T) {
	svc := newRealUserStore(t)
	createUser(t, svc, "acme", "bob", nil)
	m := policyMiddleware(svc, auth.MTLSBindingRequired)
	if _, _, err := m.finalizeIdentity(mtlsContext(mintCert(t, "bob", "acme", nil, "")), &auth.Identity{Subject: "bob", TenantID: "acme"}, knownMethod("bearer")); err != nil {
		t.Fatalf("non-certificate method refused under required: %v", err)
	}
}

func TestMTLSBindingPolicy_Accessor(t *testing.T) {
	var nilMW *AuthMiddleware
	if got := nilMW.MTLSBindingPolicy(); got != auth.MTLSBindingEnrolled {
		t.Fatalf("nil middleware policy = %v, want enrolled", got)
	}
	if got := New(&mockAuthorizer{}).MTLSBindingPolicy(); got != auth.MTLSBindingEnrolled {
		t.Fatalf("default policy = %v, want enrolled", got)
	}
	if got := New(&mockAuthorizer{}, WithMTLSBindingPolicy(auth.MTLSBindingRequired)).MTLSBindingPolicy(); got != auth.MTLSBindingRequired {
		t.Fatalf("configured policy = %v, want required", got)
	}
}

func TestCertBindingRequiredError_NoPeerCertificate(t *testing.T) {
	msg := (&CertBindingRequiredError{Username: "bob", TenantID: "acme", AccountResolved: true}).Error()
	if !strings.Contains(msg, "<no peer certificate>") || !strings.Contains(msg, "has no enrolled certificate binding") {
		t.Fatalf("message = %q", msg)
	}
}
