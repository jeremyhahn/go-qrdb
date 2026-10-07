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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cert/certtest"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

// TestKeyBLogin_AuthenticatesOnEveryRemoteTransport proves the dynamic Key B
// trust pool (pkg/clienttrust, wired into sdk/go/node's wireClientAndServer):
// a client certificate issued by THIS node's own Key B CA (cert.CertProvider,
// minted exactly as the `certs generate` / `ca issue` CLI path does) logs in
// successfully on every remote client-facing transport, with no client CA
// seeded into the server's TLS config ahead of time -- the dynamic pool is
// the ONLY thing that makes the handshake verify.
//
// See docs/certificate-authority/architecture.md, "Key B trust on the
// client-facing listeners".
func TestKeyBLogin_AuthenticatesOnEveryRemoteTransport(t *testing.T) {
	for _, tk := range []transportKind{grpcTransport, quicTransport, unixTransport, restTransport, mcpTransport} {
		t.Run(string(tk), func(t *testing.T) {
			n, rootCAs, bundle := bootKeyBLoginCell(t, tk)

			clientCert, err := tls.X509KeyPair(bundle.CertPEM, bundle.KeyPEM)
			if err != nil {
				t.Fatalf("X509KeyPair over the Key-B-issued bundle: %v", err)
			}
			tlsCfg := &tls.Config{
				Certificates: []tls.Certificate{clientCert},
				// RootCAs verifies the SERVER's leaf -- unrelated to the Key B
				// trust pool under test, which governs the server's
				// verification of the CLIENT certificate.
				RootCAs:    rootCAs,
				ServerName: "127.0.0.1",
				MinVersion: tls.VersionTLS13,
			}

			tr := tk.dial(t, n, tlsCfg)
			sdkClient, err := sdk.NewClient(tr)
			if err != nil {
				t.Fatalf("new SDK client: %v", err)
			}
			defer func() { _ = sdkClient.Close() }()
			if err := sdkClient.Connect(boundedCtx(t)); err != nil {
				t.Fatalf("%s connect: %v", tk, err)
			}

			// A real authorized call, not just a successful handshake: the
			// identity the Key-B-issued certificate carries (alice/acme) must
			// also resolve through RBAC to the role granted below.
			if err := sdkClient.KV().Put(boundedCtx(t), "login-proof", []byte("ok")); err != nil {
				t.Fatalf("%s: authorized KV.Put with the Key-B-issued identity: %v", tk, err)
			}
			got, err := sdkClient.KV().Get(boundedCtx(t), "login-proof")
			if err != nil {
				t.Fatalf("%s: authorized KV.Get with the Key-B-issued identity: %v", tk, err)
			}
			if string(got) != "ok" {
				t.Fatalf("%s: got %q, want %q", tk, got, "ok")
			}
		})
	}
}

// bootKeyBLoginCell boots a standalone node over tk whose TLS listener trusts
// NO client CA statically: the only path by which a client certificate can
// ever verify is the dynamic Key B trust pool. It creates tenant acme, a role
// granting kv:put/kv:get, and a non-enrolled user "alice" in acme, then mints
// alice's client certificate through the node's own Key B cert enrolment
// service from a LOCALLY generated CSR (certtest.NewCSR) -- exactly the
// `certs issue` / `ca issue` path, where the server never sees a private key
// -- and returns it alongside the root CA pool a test client needs to verify
// the node's OWN server leaf.
func bootKeyBLoginCell(t *testing.T, tk transportKind) (*node.Node, *x509.CertPool, *certtest.Bundle) {
	t.Helper()

	certDir := t.TempDir()
	ca, err := qrdbdharness.NewCA(certDir)
	if err != nil {
		t.Fatalf("new harness CA (server leaf only): %v", err)
	}
	serverKP, err := ca.Issue(certDir, qrdbdharness.WithSubject("node", ""), qrdbdharness.WithServerAuth())
	if err != nil {
		t.Fatalf("issue server cert: %v", err)
	}
	serverCert, err := serverKP.TLSCertificate()
	if err != nil {
		t.Fatalf("server TLS cert: %v", err)
	}

	// Deliberately NO ClientCAs seeded here: pkg/clienttrust is the only thing
	// that can make a client certificate verify on this listener.
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.VerifyClientCertIfGiven,
		MinVersion:   tls.VersionTLS13,
	}

	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Server.Host = "127.0.0.1"
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "none"
	// This cell is about the Key B dynamic trust pool, not
	// auth.mtls_binding_policy (go-qrdb#517/#548): "alice" is deliberately
	// never enrolled (see below), so under the default "required" her
	// Key-B-issued certificate would be refused as naming no bound
	// account before Key B's own behavior is ever reached. "enrolled" is
	// the documented opt-out for exactly an account the policy was never
	// meant to gate.
	cfg.Auth.MTLSBindingPolicy = "enrolled"

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

	ctx := boundedCtx(t)
	svc := n.Services()
	if _, err := svc.Tenant.CreateTenant(ctx, "acme", "Acme Corp", tenant.IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant(acme): %v", err)
	}
	ctx = tenant.WithTenantID(ctx, "acme")
	r, err := svc.Role.CreateRole(ctx, &role.CreateRoleRequest{
		Name:     "key-b-login",
		TenantID: "acme",
		Permissions: []permission.Permission{
			permission.KVPut, permission.KVGet,
		},
	})
	if err != nil {
		t.Fatalf("create role: %v", err)
	}
	// WithoutEmailLoginKey and no cert-binding enrollment: this account is
	// never "enrolled" (identity-rbac's term), so it authenticates by
	// CN+tenant match alone -- exactly what a freshly `certs generate`-issued
	// certificate relies on.
	if _, err := svc.User.CreateUser(ctx, &user.CreateUserRequest{
		WithoutEmailLoginKey: true,
		Username:             "alice",
		TenantID:             "acme",
		RoleIDs:              []string{r.ID},
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}

	if svc.CertEnrollment == nil {
		t.Fatal("the node has no certificate enrolment service (Key B)")
	}
	keyPEM, csrPEM, err := certtest.NewCSR("alice")
	if err != nil {
		t.Fatalf("certtest.NewCSR: %v", err)
	}
	enrolled, err := svc.CertEnrollment.SignCertificate(ctx, &service.SignCertificateRequest{
		TenantID: "acme", Username: "alice", CSRPEM: csrPEM,
	})
	if err != nil {
		t.Fatalf("SignCertificate (Key B): %v", err)
	}
	bundle := &certtest.Bundle{
		CertPEM:  enrolled.CertPEM,
		KeyPEM:   keyPEM,
		Serial:   enrolled.Serial,
		NotAfter: enrolled.NotAfter,
	}

	return n, ca.Pool, bundle
}
