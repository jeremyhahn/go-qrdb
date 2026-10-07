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
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cert/certtest"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// remoteTransports are the transports a certificate login crosses. The
// embedded transport has no listener and so no login; its tenant-onboarding
// and certificate verbs are covered by the embedded client's own tests.
var remoteTransports = []transportKind{grpcTransport, quicTransport, unixTransport, restTransport, mcpTransport}

// onboardingCell is one node booted under auth.mtls_binding_policy "required"
// with a system-tenant operator able to create tenants.
type onboardingCell struct {
	tk        transportKind
	n         *node.Node
	serverCAs *x509.CertPool
	// harness is the CA that issued the node's server leaf and the operator's
	// certificate. It is trusted statically for the operator only; no tenant
	// in these tests is onboarded under it.
	harness *qrdbdharness.CA
	dir     string
	// operator is the system-tenant operator's client.
	operator *sdk.Client
}

// bootOnboardingCell boots a standalone node over tk under the "required"
// binding posture: no certificate logs in unless its key is bound to the
// account it names. The operator's own key is bound in-process -- the one
// thing the platform's bootstrap does for its first operator -- and every
// tenant identity after that is created through the transport under test.
func bootOnboardingCell(t *testing.T, tk transportKind) *onboardingCell {
	t.Helper()
	dir := t.TempDir()

	ca, err := qrdbdharness.NewCA(dir)
	if err != nil {
		t.Fatalf("new harness CA: %v", err)
	}
	serverKP, err := ca.Issue(dir, qrdbdharness.WithSubject("node", ""), qrdbdharness.WithServerAuth())
	if err != nil {
		t.Fatalf("issue server cert: %v", err)
	}
	serverCert, err := serverKP.TLSCertificate()
	if err != nil {
		t.Fatalf("server TLS cert: %v", err)
	}
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    ca.Pool,
		ClientAuth:   tls.VerifyClientCertIfGiven,
		MinVersion:   tls.VersionTLS13,
	}

	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Server.Host = "127.0.0.1"
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "none"
	cfg.Auth.MTLSBindingPolicy = "required"
	// A pool for the barrier-isolated tenant's dedicated store.
	cfg.Storage.VolumePools = []qrdbconfig.VolumePoolConfig{{Name: "default", Path: t.TempDir()}}
	cfg.Protocols.REST = tk == restTransport
	cfg.Protocols.GRPC = tk == grpcTransport
	cfg.Protocols.QUIC = tk == quicTransport
	cfg.Protocols.Unix = tk == unixTransport
	cfg.Protocols.MCP = tk == mcpTransport
	switch tk {
	case grpcTransport:
		cfg.Server.GRPCPort = freeTCPPort(t)
	case quicTransport:
		cfg.Server.QUICPort = freeUDPPort(t)
	case restTransport:
		cfg.Server.AdminPort = freeTCPPort(t)
	case unixTransport:
		cfg.Unix.SocketPath = unixSocketPath(t)
	case mcpTransport:
		cfg.MCP.Port = freeTCPPort(t)
	}

	n, err := node.NewStandaloneNode(context.Background(), cfg, []server.ServerOption{server.WithTLS(tlsCfg)})
	if err != nil {
		t.Fatalf("boot %s: %v", tk, err)
	}
	t.Cleanup(func() { _ = n.Close() })

	c := &onboardingCell{tk: tk, n: n, serverCAs: ca.Pool, harness: ca, dir: dir}

	ctx := boundedCtx(t)
	svc := n.Services()
	r, err := svc.Role.CreateRole(ctx, &role.CreateRoleRequest{
		Name:     "onboarding-operator",
		TenantID: tenantid.System,
		Permissions: []permission.Permission{
			permission.TenantsCreate, permission.TenantsCreateCross, permission.TenantsRead,
			permission.VolumePoolCreate,
		},
	})
	if err != nil {
		t.Fatalf("create operator role: %v", err)
	}
	op, err := svc.User.CreateUser(ctx, &user.CreateUserRequest{
		WithoutEmailLoginKey: true,
		Username:             "operator",
		TenantID:             tenantid.System,
		RoleIDs:              []string{r.ID},
	})
	if err != nil {
		t.Fatalf("create operator: %v", err)
	}
	opKP, err := ca.Issue(dir, qrdbdharness.WithSubject("operator", tenantid.System))
	if err != nil {
		t.Fatalf("issue operator cert: %v", err)
	}
	opLeaf := parseLeaf(t, opKP.CertPEM)
	if _, err := svc.User.AddCertBinding(ctx, op.ID, user.ComputeSPKIThumbprint(opLeaf), opLeaf.NotAfter.Unix()); err != nil {
		t.Fatalf("bind operator key: %v", err)
	}
	c.operator = c.mustLogin(t, opKP.CertPEM, opKP.KeyPEM)

	// cfg.Storage.VolumePools only maps "default" for this node to serve;
	// TenantOnboarding routes onto it through the replicated catalog, whose
	// record comes only from an explicit call (docs/volume-pools/architecture.md
	// D2), mirroring createVolumePoolREST in test/integration.
	if _, err := c.operator.VolumePools().CreatePool(ctx, &transport.CreateVolumePoolRequest{Name: "default"}); err != nil {
		t.Fatalf("create volume pool %q: %v", "default", err)
	}
	return c
}

// login dials the cell's transport presenting certPEM/keyPEM and connects.
// The returned client is closed at cleanup.
func (c *onboardingCell) login(t *testing.T, certPEM, keyPEM []byte) (*sdk.Client, error) {
	t.Helper()
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}
	tr := c.tk.dial(t, c.n, &tls.Config{
		Certificates: []tls.Certificate{pair},
		RootCAs:      c.serverCAs,
		ServerName:   "127.0.0.1",
		MinVersion:   tls.VersionTLS13,
	})
	cl, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("new SDK client: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	return cl, cl.Connect(boundedCtx(t))
}

func (c *onboardingCell) mustLogin(t *testing.T, certPEM, keyPEM []byte) *sdk.Client {
	t.Helper()
	cl, err := c.login(t, certPEM, keyPEM)
	if err != nil {
		t.Fatalf("%s connect: %v", c.tk, err)
	}
	return cl
}

// assertAdminOf proves cl is logged in as an admin of tenantID: listing the
// tenant's trust anchors needs both an authenticated identity in that tenant
// and the tenant Admin role's trust-anchors:list.
func assertAdminOf(t *testing.T, tk transportKind, cl *sdk.Client, tenantID string) {
	t.Helper()
	if _, err := cl.Tenants().ListTrustAnchors(boundedCtx(t), tenantID); err != nil {
		t.Fatalf("%s: tenant %s's admin could not list its trust anchors: %v", tk, tenantID, err)
	}
}

// assertLoginRefused proves certPEM/keyPEM cannot act on the node: the
// refusal may come at the handshake, at connect, or at the first call
// depending on the transport, and it must come at one of them.
func (c *onboardingCell) assertLoginRefused(t *testing.T, certPEM, keyPEM []byte, tenantID, why string) {
	t.Helper()
	cl, err := c.login(t, certPEM, keyPEM)
	if err != nil {
		return
	}
	if _, err := cl.Tenants().ListTrustAnchors(boundedCtx(t), tenantID); err == nil {
		t.Fatalf("%s: %s, yet the certificate authenticated and acted on tenant %s", c.tk, why, tenantID)
	}
}

func parseLeaf(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("no PEM block in certificate")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	return leaf
}

// onboardWithCSR creates tenantID through the operator with alice as its
// first admin, enrolled from a CSR whose key never leaves this test.
func (c *onboardingCell) onboardWithCSR(t *testing.T, tenantID, admin string) (certPEM, keyPEM []byte) {
	t.Helper()
	keyPEM, csrPEM, err := certtest.NewCSR(admin)
	if err != nil {
		t.Fatalf("new CSR: %v", err)
	}
	resp, err := c.operator.Tenants().CreateTenant(boundedCtx(t), &transport.CreateTenantRequest{
		ID:            tenantID,
		Name:          tenantID,
		AdminUsername: admin,
		AdminCSRPEM:   string(csrPEM),
	})
	if err != nil {
		t.Fatalf("%s: create tenant %s with an admin CSR: %v", c.tk, tenantID, err)
	}
	if resp.Tenant.ID != tenantID {
		t.Fatalf("%s: created tenant %q, want %q", c.tk, resp.Tenant.ID, tenantID)
	}
	if resp.Admin.Username != admin || resp.Admin.CertPEM == "" || resp.Admin.Thumbprint == "" {
		t.Fatalf("%s: admin credential incomplete: %+v", c.tk, resp.Admin)
	}
	if resp.Admin.AwaitsBarrierInit {
		t.Fatalf("%s: a prefix tenant's admin must be created at once, not await a barrier", c.tk)
	}
	leaf := parseLeaf(t, []byte(resp.Admin.CertPEM))
	if got := user.ComputeSPKIThumbprint(leaf); got != resp.Admin.Thumbprint {
		t.Fatalf("%s: returned thumbprint %s is not the issued certificate's key (%s)", c.tk, resp.Admin.Thumbprint, got)
	}
	if _, err := tls.X509KeyPair([]byte(resp.Admin.CertPEM), keyPEM); err != nil {
		t.Fatalf("%s: the issued certificate does not carry the CSR's key: %v", c.tk, err)
	}
	return []byte(resp.Admin.CertPEM), keyPEM
}

// TestTenantOnboarding_CSRAdminLogsInUnderRequired is the release blocker this
// lane exists for: under "required" no tenant could ever log in, because the
// only way to mint a tenant identity was server-side key generation with no
// binding. A tenant created with its admin's CSR returns a Key B certificate
// already bound to the admin, and that admin logs in on every transport.
//
// Control: a Key B certificate for the same account minted WITHOUT a binding
// is refused in the same cell, so the green answer is the binding at work and
// not a cell in which "required" is not enforced.
func TestTenantOnboarding_CSRAdminLogsInUnderRequired(t *testing.T) {
	for _, tk := range remoteTransports {
		t.Run(string(tk), func(t *testing.T) {
			c := bootOnboardingCell(t, tk)
			certPEM, keyPEM := c.onboardWithCSR(t, "acme", "alice")

			alice := c.mustLogin(t, certPEM, keyPEM)
			assertAdminOf(t, tk, alice, "acme")

			unbound, err := certtest.Generate(boundedCtx(t), c.n.Services().CertProv, &certtest.Request{Username: "alice", TenantID: "acme"})
			if err != nil {
				t.Fatalf("mint an unbound Key B certificate: %v", err)
			}
			c.assertLoginRefused(t, unbound.CertPEM, unbound.KeyPEM, "acme", "the certificate's key is bound to no account under \"required\"")
		})
	}
}

// TestTenantOnboarding_AdminIssuesForExistingAccountOnly proves the CSR-sign
// verb on every transport: the onboarded admin issues a bound certificate for
// an existing account of its tenant, which logs in at once, and is refused a
// certificate for an account that does not exist.
func TestTenantOnboarding_AdminIssuesForExistingAccountOnly(t *testing.T) {
	for _, tk := range remoteTransports {
		t.Run(string(tk), func(t *testing.T) {
			c := bootOnboardingCell(t, tk)
			certPEM, keyPEM := c.onboardWithCSR(t, "acme", "alice")
			alice := c.mustLogin(t, certPEM, keyPEM)

			ctx := tenant.WithTenantID(boundedCtx(t), "acme")
			svc := c.n.Services()
			r, err := svc.Role.CreateRole(ctx, &role.CreateRoleRequest{
				Name: "kv", TenantID: "acme",
				Permissions: []permission.Permission{permission.KVPut, permission.KVGet},
			})
			if err != nil {
				t.Fatalf("create role: %v", err)
			}
			if _, err := svc.User.CreateUser(ctx, &user.CreateUserRequest{
				WithoutEmailLoginKey: true, Username: "bob", TenantID: "acme", RoleIDs: []string{r.ID},
			}); err != nil {
				t.Fatalf("create bob: %v", err)
			}

			bobKey, bobCSR, err := certtest.NewCSR("bob")
			if err != nil {
				t.Fatalf("bob's CSR: %v", err)
			}
			issued, err := alice.Certs().IssueCert(ctx, &transport.IssueCertRequest{Username: "bob", CSRPEM: string(bobCSR)})
			if err != nil {
				t.Fatalf("%s: issue bob's certificate: %v", tk, err)
			}
			if issued.Username != "bob" || issued.TenantID != "acme" || issued.Thumbprint == "" || issued.NotAfter.IsZero() {
				t.Fatalf("%s: enrolled certificate incomplete: %+v", tk, issued)
			}
			bob := c.mustLogin(t, []byte(issued.CertPEM), bobKey)
			if err := bob.KV().Put(boundedCtx(t), "onboarded", []byte("ok")); err != nil {
				t.Fatalf("%s: bob's authorized write with the issued certificate: %v", tk, err)
			}

			_, ghostCSR, err := certtest.NewCSR("ghost")
			if err != nil {
				t.Fatalf("ghost CSR: %v", err)
			}
			if got, err := alice.Certs().IssueCert(boundedCtx(t), &transport.IssueCertRequest{Username: "ghost", CSRPEM: string(ghostCSR)}); err == nil {
				t.Fatalf("%s: a certificate was issued for an account that does not exist: %+v", tk, got)
			}
		})
	}
}

// TestTenantOnboarding_BYOCAdminUnderRegisteredAnchor proves bring-your-own
// certificate onboarding: a tenant created with its admin's certificate from
// the tenant's own CA, registered as the tenant's trust anchor in the same
// call, is logged in on every transport -- the node trusts that CA for this
// tenant only because the tenant registered it.
func TestTenantOnboarding_BYOCAdminUnderRegisteredAnchor(t *testing.T) {
	for _, tk := range remoteTransports {
		t.Run(string(tk), func(t *testing.T) {
			c := bootOnboardingCell(t, tk)
			ext, err := qrdbdharness.NewCA(t.TempDir(), qrdbdharness.WithCACommonName("Globex Issuing CA"))
			if err != nil {
				t.Fatalf("external CA: %v", err)
			}
			adminKP, err := ext.Issue(t.TempDir(), qrdbdharness.WithSubject("gina", "globex"))
			if err != nil {
				t.Fatalf("issue admin cert: %v", err)
			}

			// Control first: before the tenant registers its CA, nothing on
			// the node trusts it.
			c.assertLoginRefused(t, adminKP.CertPEM, adminKP.KeyPEM, "globex", "no tenant has registered the external CA")

			resp, err := c.operator.Tenants().CreateTenant(boundedCtx(t), &transport.CreateTenantRequest{
				ID: "globex", Name: "Globex",
				AdminUsername:   "gina",
				AdminCertPEM:    string(adminKP.CertPEM),
				TrustAnchorsPEM: string(ext.CertPEM),
			})
			if err != nil {
				t.Fatalf("%s: create tenant with a BYO admin certificate: %v", tk, err)
			}
			if resp.Admin.Thumbprint != user.ComputeSPKIThumbprint(parseLeaf(t, adminKP.CertPEM)) {
				t.Fatalf("%s: the bound key is not the presented certificate's", tk)
			}

			gina := c.mustLogin(t, adminKP.CertPEM, adminKP.KeyPEM)
			assertAdminOf(t, tk, gina, "globex")
			anchors, err := gina.Tenants().ListTrustAnchors(boundedCtx(t), "globex")
			if err != nil || len(anchors.Anchors) != 1 {
				t.Fatalf("%s: globex's anchors = %+v, %v; want the one registered", tk, anchors, err)
			}
		})
	}
}

// TestTenantOnboarding_AnchorCannotVouchAcrossTenants proves a trust anchor is
// scoped to the tenant that registered it. Globex registers its CA; a
// certificate from that CA naming an account of acme is refused both ways it
// could reach acme: acme's admin cannot enroll it (acme has no such anchor),
// and even a binding forced into acme's account in-process does not make it
// log in -- the issuer-tenant check refuses a CA that is not acme's.
func TestTenantOnboarding_AnchorCannotVouchAcrossTenants(t *testing.T) {
	for _, tk := range remoteTransports {
		t.Run(string(tk), func(t *testing.T) {
			c := bootOnboardingCell(t, tk)
			aliceCert, aliceKey := c.onboardWithCSR(t, "acme", "alice")
			alice := c.mustLogin(t, aliceCert, aliceKey)

			ext, err := qrdbdharness.NewCA(t.TempDir(), qrdbdharness.WithCACommonName("Globex Issuing CA"))
			if err != nil {
				t.Fatalf("external CA: %v", err)
			}
			ginaKP, err := ext.Issue(t.TempDir(), qrdbdharness.WithSubject("gina", "globex"))
			if err != nil {
				t.Fatalf("issue gina: %v", err)
			}
			if _, err := c.operator.Tenants().CreateTenant(boundedCtx(t), &transport.CreateTenantRequest{
				ID: "globex", Name: "Globex", AdminUsername: "gina",
				AdminCertPEM: string(ginaKP.CertPEM), TrustAnchorsPEM: string(ext.CertPEM),
			}); err != nil {
				t.Fatalf("%s: create globex: %v", tk, err)
			}

			ctx := tenant.WithTenantID(boundedCtx(t), "acme")
			// mallory holds trust-anchors:list in acme, so the refusal below
			// can only be authentication: an authenticated mallory would be
			// allowed the very call assertLoginRefused makes.
			listRole, err := c.n.Services().Role.CreateRole(ctx, &role.CreateRoleRequest{
				Name: "anchor-reader", TenantID: "acme",
				Permissions: []permission.Permission{permission.TrustAnchorsList},
			})
			if err != nil {
				t.Fatalf("create role: %v", err)
			}
			mallory, err := c.n.Services().User.CreateUser(ctx, &user.CreateUserRequest{
				WithoutEmailLoginKey: true, Username: "mallory", TenantID: "acme", RoleIDs: []string{listRole.ID},
			})
			if err != nil {
				t.Fatalf("create mallory: %v", err)
			}
			forged, err := ext.Issue(t.TempDir(), qrdbdharness.WithSubject("mallory", "acme"))
			if err != nil {
				t.Fatalf("issue cross-tenant cert: %v", err)
			}

			if got, err := alice.Certs().EnrollCert(ctx, &transport.EnrollCertRequest{Username: "mallory", CertPEM: string(forged.CertPEM)}); err == nil {
				t.Fatalf("%s: acme enrolled a certificate only globex's anchor vouches for: %+v", tk, got)
			}

			leaf := parseLeaf(t, forged.CertPEM)
			if _, err := c.n.Services().User.AddCertBinding(ctx, mallory.ID, user.ComputeSPKIThumbprint(leaf), leaf.NotAfter.Unix()); err != nil {
				t.Fatalf("force mallory's binding: %v", err)
			}
			c.assertLoginRefused(t, forged.CertPEM, forged.KeyPEM, "acme", "the only anchor vouching for the certificate is globex's, and it names acme")

			// Positive control: mallory's own acme-issued certificate makes
			// that same call, so the refusal above is the anchor scoping.
			malloryKey, malloryCSR, err := certtest.NewCSR("mallory")
			if err != nil {
				t.Fatalf("mallory CSR: %v", err)
			}
			issued, err := alice.Certs().IssueCert(boundedCtx(t), &transport.IssueCertRequest{Username: "mallory", CSRPEM: string(malloryCSR)})
			if err != nil {
				t.Fatalf("%s: issue mallory's acme certificate: %v", tk, err)
			}
			assertAdminOf(t, tk, c.mustLogin(t, []byte(issued.CertPEM), malloryKey), "acme")
		})
	}
}

// TestTenantOnboarding_RefusedCreateLeavesNoTenant proves the create is one
// unit on every transport: a request whose admin credential is refused leaves
// no tenant behind and does not retire the ID, so the corrected request
// succeeds under the same ID.
func TestTenantOnboarding_RefusedCreateLeavesNoTenant(t *testing.T) {
	for _, tk := range remoteTransports {
		t.Run(string(tk), func(t *testing.T) {
			c := bootOnboardingCell(t, tk)
			ext, err := qrdbdharness.NewCA(t.TempDir(), qrdbdharness.WithCACommonName("Unregistered CA"))
			if err != nil {
				t.Fatalf("external CA: %v", err)
			}
			kp, err := ext.Issue(t.TempDir(), qrdbdharness.WithSubject("alice", "acme"))
			if err != nil {
				t.Fatalf("issue: %v", err)
			}
			// A BYO certificate with no anchor that vouches for it.
			if _, err := c.operator.Tenants().CreateTenant(boundedCtx(t), &transport.CreateTenantRequest{
				ID: "acme", Name: "acme", AdminUsername: "alice", AdminCertPEM: string(kp.CertPEM),
			}); err == nil {
				t.Fatalf("%s: a tenant was created with an admin certificate nothing vouches for", tk)
			}
			if _, err := c.n.Services().Tenant.GetTenant(boundedCtx(t), "acme"); err == nil {
				t.Fatalf("%s: the refused create left tenant acme behind", tk)
			}
			certPEM, keyPEM := c.onboardWithCSR(t, "acme", "alice")
			assertAdminOf(t, tk, c.mustLogin(t, certPEM, keyPEM), "acme")
		})
	}
}

// TestTenantOnboarding_DedicatedTenantAdminInitializesItsBarrier proves the
// onboarding of a tenant whose identity records live behind its own barrier:
// at create the admin's certificate is issued and recorded, the account
// cannot exist yet, and the barrier's trust-on-first-init window admits ONLY
// that certificate. Once the onboarded admin initializes the barrier, its
// account exists and it logs in as the tenant's admin -- under "required", on
// every transport.
//
// Control: another certificate naming the same tenant, from a CA the node
// trusts, is refused the first init.
func TestTenantOnboarding_DedicatedTenantAdminInitializesItsBarrier(t *testing.T) {
	for _, tk := range remoteTransports {
		t.Run(string(tk), func(t *testing.T) {
			c := bootOnboardingCell(t, tk)
			keyPEM, csrPEM, err := certtest.NewCSR("vera")
			if err != nil {
				t.Fatalf("new CSR: %v", err)
			}
			resp, err := c.operator.Tenants().CreateTenant(boundedCtx(t), &transport.CreateTenantRequest{
				ID: "vault", Name: "Vault",
				IsolationType: transport.TenantIsolationBarrier,
				AdminUsername: "vera",
				AdminCSRPEM:   string(csrPEM),
			})
			if err != nil {
				t.Fatalf("%s: create a barrier-isolated tenant with an admin CSR: %v", tk, err)
			}
			if !resp.Admin.AwaitsBarrierInit || resp.Admin.CertPEM == "" {
				t.Fatalf("%s: a barrier-isolated tenant's admin must be issued now and created at barrier init: %+v", tk, resp.Admin)
			}

			init := func(cl *sdk.Client) error {
				_, err := cl.Tenants().TenantBarrierInit(boundedCtx(t), &transport.TenantBarrierInitRequest{
					TenantID: "vault", Strategy: "passphrase", Passphrase: []byte("correct horse battery staple"),
				})
				return err
			}

			eveKP, err := c.harness.Issue(t.TempDir(), qrdbdharness.WithSubject("eve", "vault"))
			if err != nil {
				t.Fatalf("issue eve: %v", err)
			}
			// eve's certificate is trusted at the TLS layer and the tenant's
			// identity records are sealed, so she reaches the barrier's
			// first-init window; the window itself must refuse her, and say
			// why -- a refusal for any other reason would not prove the gate.
			eve := c.mustLogin(t, eveKP.CertPEM, eveKP.KeyPEM)
			if err := init(eve); err == nil {
				t.Fatalf("%s: a certificate other than the onboarded admin's initialized the tenant's barrier", tk)
			} else if !strings.Contains(err.Error(), "may be initialized only with the certificate its first admin") {
				t.Fatalf("%s: eve was refused, but not by the first-init gate: %v", tk, err)
			}

			vera := c.mustLogin(t, []byte(resp.Admin.CertPEM), keyPEM)
			if err := init(vera); err != nil {
				t.Fatalf("%s: the onboarded admin could not initialize its tenant's barrier: %v", tk, err)
			}
			// The account is created when the barrier becomes usable; a
			// fresh connection authenticates against it.
			assertAdminOf(t, tk, c.mustLogin(t, []byte(resp.Admin.CertPEM), keyPEM), "vault")
		})
	}
}
