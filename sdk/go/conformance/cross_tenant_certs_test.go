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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestCerts_SystemAdminCannotActOnAnotherTenant: the system tenant's Admin
// can neither mint a client certificate for acme's user, nor list acme's
// certificates, nor revoke one of them. It can still mint for its own tenant.
//
// The embedded cell is absent because sdk/go/node does not wire a certificate
// provider into the in-process client; the embedded transport's confinement is
// proven in sdk/go/transport/embedded (TestCertConfinement_*).
func TestCerts_SystemAdminCannotActOnAnotherTenant(t *testing.T) {
	for _, tk := range userNotFoundTransports {
		if !tk.remote() {
			continue
		}
		t.Run(string(tk), func(t *testing.T) {
			c := bootConfinedCell(t, tk, tenant.SystemTenantID)
			certs := c.client.Certs()
			if certs == nil {
				t.Fatalf("%s: no cert client", tk)
			}

			own, err := certs.GenerateCert(c.ctx, &transport.GenerateCertRequest{
				Username: "operator", TenantID: tenant.SystemTenantID,
			})
			if err != nil {
				t.Fatalf("%s: GenerateCert for the caller's own tenant: %v", tk, err)
			}
			if own.Serial == "" {
				t.Fatalf("%s: own-tenant certificate carries no serial", tk)
			}

			_, err = certs.GenerateCert(c.ctx, &transport.GenerateCertRequest{
				Username: "alice", TenantID: confineAcme,
			})
			assertCrossTenantRefused(t, tk, "GenerateCert for acme's user", err)

			_, err = certs.ListCerts(c.ctx, confineAcme)
			assertCrossTenantRefused(t, tk, "ListCerts for acme", err)

			acmeSerial := issueThroughNode(t, c.node, "bob", confineAcme)
			_, err = certs.RevokeCert(c.ctx, acmeSerial)
			assertCrossTenantRefused(t, tk, "RevokeCert of acme's certificate", err)
		})
	}
}

// TestCerts_TenantAdminCannotActOnAnotherTenant: acme's Admin mints for acme
// and is refused globex.
func TestCerts_TenantAdminCannotActOnAnotherTenant(t *testing.T) {
	for _, tk := range userNotFoundTransports {
		if !tk.remote() {
			continue
		}
		t.Run(string(tk), func(t *testing.T) {
			c := bootConfinedCell(t, tk, confineAcme)
			certs := c.client.Certs()
			if certs == nil {
				t.Fatalf("%s: no cert client", tk)
			}

			if _, err := certs.GenerateCert(c.ctx, &transport.GenerateCertRequest{
				Username: "alice", TenantID: confineAcme,
			}); err != nil {
				t.Fatalf("%s: GenerateCert for the caller's own tenant: %v", tk, err)
			}

			_, err := certs.GenerateCert(c.ctx, &transport.GenerateCertRequest{
				Username: "carol", TenantID: confineGlobex,
			})
			assertCrossTenantRefused(t, tk, "GenerateCert for globex's user", err)

			globexSerial := issueThroughNode(t, c.node, "dave", confineGlobex)
			_, err = certs.RevokeCert(c.ctx, globexSerial)
			assertCrossTenantRefused(t, tk, "RevokeCert of globex's certificate", err)
		})
	}
}

// issueThroughNode mints a certificate through the node's own provider, the
// way a tenant's own Admin would have, and returns its serial.
func issueThroughNode(t *testing.T, n *node.Node, username, tenantID string) string {
	t.Helper()
	prov := n.Services().CertProv
	if prov == nil {
		t.Fatal("the node has no certificate provider")
	}
	bundle, err := prov.GenerateClientCert(boundedCtx(t), &cert.GenerateRequest{Username: username, TenantID: tenantID})
	if err != nil {
		t.Fatalf("issue %s/%s: %v", tenantID, username, err)
	}
	return bundle.Serial
}
