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

package conformance

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cert/certtest"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Cross-tenant certificate confinement under IssueCert/EnrollCert
// (go-qrdb tenant-onboarding refactor).
//
// Server keygen (GenerateCert, which took an explicit TenantID) is retired.
// IssueCert and EnrollCert both bind to an EXISTING account, which the
// service looks up by (CALLER'S OWN TENANT, Username) -- the tenant comes
// from the caller's authenticated context (tenant.Confine), never from a
// request field, so there is no parameter left to "ask for another
// tenant's" the way the old test proved GenerateCert's TenantID was
// refused.
//
// What replaces it: naming an account that is NOT in the caller's own
// tenant -- whether because it belongs to a DIFFERENT tenant or because it
// does not exist anywhere -- must be refused. Both collapse to the exact
// same server-side classification (*user.UserNotFoundError, go-qrdb#343):
// a caller cannot distinguish "exists elsewhere" from "exists nowhere" by
// the shape of the refusal, which is itself the confinement property this
// file proves.
//
// The embedded cell is included: sdk/go/node wires a real CertEnrollment
// service into the in-process client (unlike the old CertProv-only wiring
// TestCerts_SystemAdminCannotActOnAnotherTenant's comment described), so
// embedded is on the same axis as every remote transport here.

// certAccountRefusals is what IssueCert/EnrollCert must answer, on each
// transport, when the named account does not resolve within the caller's
// own tenant. Every transport is listed, so a new one declares its
// classification instead of inheriting a default (same discipline as
// crossTenantRefusals and wantClassByTransport).
var certAccountRefusals = map[transportKind]func(error) bool{
	embeddedTransport: isUserNotFound,
	restTransport:     isUserNotFound,
	grpcTransport:     isUserNotFound,
	unixTransport:     isUserNotFound,
	quicTransport:     isUserNotFound,
	mcpTransport:      isUserNotFound,
}

// isUserNotFound reports whether err is the SDK's typed account refusal.
// Every transport answers it the same way: the only subject either verb can
// fail to find is the account it names, so each SDK transport types its
// not-found as *transport.UserNotFoundError rather than the generic
// key-not-found a NotFound means elsewhere.
func isUserNotFound(err error) bool {
	var unf *transport.UserNotFoundError
	return errors.As(err, &unf)
}

// assertAccountRefused checks err is tk's refusal for an account that does
// not resolve in the caller's own tenant.
func assertAccountRefused(t *testing.T, tk transportKind, verb string, err error) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: %s: the request succeeded", tk, verb)
		return
	}
	refused, ok := certAccountRefusals[tk]
	if !ok {
		t.Fatalf("%s declares no account-not-found refusal for certificate enrolment", tk)
	}
	if !refused(err) {
		t.Errorf("%s: %s: %T %v is not %s's not-found refusal", tk, verb, err, err, tk)
	}
}

// seedCertAccount creates username in tenantID directly through the node's
// own user service, the way an operator would have provisioned the account
// before a holder ever asks for a certificate.
func seedCertAccount(t *testing.T, n *node.Node, tenantID, username string) {
	t.Helper()
	if _, err := n.Services().User.CreateUser(boundedCtx(t), &user.CreateUserRequest{
		WithoutEmailLoginKey: true,
		Username:             username,
		TenantID:             tenantID,
	}); err != nil {
		t.Fatalf("seed account %s/%s: %v", tenantID, username, err)
	}
}

// TestIssueCert_ConfinedToCallersOwnTenant: acme's Admin can IssueCert for an
// account that exists in acme, but is refused for one that exists only in
// globex, and refused identically for one that exists nowhere at all.
func TestIssueCert_ConfinedToCallersOwnTenant(t *testing.T) {
	for _, tk := range userNotFoundTransports {
		t.Run(string(tk), func(t *testing.T) {
			c := bootConfinedCell(t, tk, confineAcme)
			certs := c.client.Certs()
			if certs == nil {
				t.Fatalf("%s: no cert client", tk)
			}
			seedCertAccount(t, c.node, confineAcme, "alice")
			seedCertAccount(t, c.node, confineGlobex, "dave")

			_, csrPEM, err := certtest.NewCSR("alice")
			if err != nil {
				t.Fatalf("certtest.NewCSR: %v", err)
			}
			own, err := certs.IssueCert(c.ctx, &transport.IssueCertRequest{
				Username: "alice", CSRPEM: string(csrPEM),
			})
			if err != nil {
				t.Fatalf("%s: IssueCert for the caller's own tenant's account: %v", tk, err)
			}
			if own.Thumbprint == "" {
				t.Fatalf("%s: own-tenant certificate carries no thumbprint", tk)
			}

			_, otherCSR, err := certtest.NewCSR("dave")
			if err != nil {
				t.Fatalf("certtest.NewCSR: %v", err)
			}
			_, err = certs.IssueCert(c.ctx, &transport.IssueCertRequest{
				Username: "dave", CSRPEM: string(otherCSR),
			})
			assertAccountRefused(t, tk, "IssueCert for globex's account", err)

			_, nobodyCSR, err := certtest.NewCSR("nobody")
			if err != nil {
				t.Fatalf("certtest.NewCSR: %v", err)
			}
			_, err = certs.IssueCert(c.ctx, &transport.IssueCertRequest{
				Username: "nobody-at-all", CSRPEM: string(nobodyCSR),
			})
			assertAccountRefused(t, tk, "IssueCert for a nonexistent account", err)
		})
	}
}

// TestEnrollCert_ConfinedToCallersOwnTenant: the same confinement for
// EnrollCert's bring-your-own path. The offered certificate is deliberately
// not a valid chain: the account lookup -- the refusal under test -- happens
// BEFORE chain verification (pkg/service.CertEnrollmentService), so a
// malformed bring-your-own certificate never masks it.
func TestEnrollCert_ConfinedToCallersOwnTenant(t *testing.T) {
	for _, tk := range userNotFoundTransports {
		t.Run(string(tk), func(t *testing.T) {
			c := bootConfinedCell(t, tk, confineAcme)
			certs := c.client.Certs()
			if certs == nil {
				t.Fatalf("%s: no cert client", tk)
			}
			seedCertAccount(t, c.node, confineGlobex, "dave")

			_, err := certs.EnrollCert(c.ctx, &transport.EnrollCertRequest{
				Username: "dave", CertPEM: "not-a-real-certificate",
			})
			assertAccountRefused(t, tk, "EnrollCert for globex's account", err)

			_, err = certs.EnrollCert(c.ctx, &transport.EnrollCertRequest{
				Username: "nobody-at-all", CertPEM: "not-a-real-certificate",
			})
			assertAccountRefused(t, tk, "EnrollCert for a nonexistent account", err)
		})
	}
}
