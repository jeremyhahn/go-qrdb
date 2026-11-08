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

//go:build !integration

package node

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

// newAuthEnforcedRESTNode builds a standalone SDK node with the REST
// transport enabled over real mTLS, seeds an admin role/user pair with the
// admin:status permission, and returns the node plus the harness CA and the
// admin client key pair the caller authenticates with. cfg.Auth.Enabled
// defaults to true (config.DefaultConfig), so this exercises the exact
// production posture: wireClientAndServer installs
// pkg/auth/wiring.BuildAuthMiddleware on the multi-protocol server via
// server.WithAuthMiddleware.
func newAuthEnforcedRESTNode(t *testing.T, nodeOpts ...NodeOption) (n *Node, ca *qrdbdharness.CA, adminKP *qrdbdharness.KeyPair) {
	t.Helper()
	certDir := t.TempDir()

	ca, err := qrdbdharness.NewCA(certDir)
	require.NoError(t, err)

	serverKP, err := ca.Issue(certDir, qrdbdharness.WithSubject("node", ""), qrdbdharness.WithServerAuth())
	require.NoError(t, err)
	serverCert, err := serverKP.TLSCertificate()
	require.NoError(t, err)

	adminKP, err = ca.Issue(certDir, qrdbdharness.WithSubject("admin", tenantid.System))
	require.NoError(t, err)

	// VerifyClientCertIfGiven (not RequireAndVerifyClientCert) mirrors
	// qrdbd's own auth-derived default: an unauthenticated request must
	// still complete its TLS handshake and reach the REST auth middleware
	// so the assertion below observes a 401 response body rather than a
	// bare connection failure.
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    ca.Pool,
		ClientAuth:   tls.VerifyClientCertIfGiven,
		MinVersion:   tls.VersionTLS13,
	}

	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	cfg.Protocols.REST = true
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.AdminPort = freeTCPPort(t)

	n, err = NewStandaloneNode(context.Background(), cfg, []server.ServerOption{server.WithTLS(tlsCfg)}, nodeOpts...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })

	svcNode := n.Services()
	require.NotNil(t, svcNode.Role)
	require.NotNil(t, svcNode.User)

	adminRole, err := svcNode.Role.CreateRole(context.Background(), &role.CreateRoleRequest{
		Name:        "auth-mw-test-admin",
		TenantID:    tenantid.System,
		Permissions: []permission.Permission{permission.AdminStatus},
	})
	require.NoError(t, err)

	createdAdmin, err := svcNode.User.CreateUser(context.Background(), &user.CreateUserRequest{WithoutEmailLoginKey: true,
		Username: "admin",
		TenantID: tenantid.System,
		RoleIDs:  []string{adminRole.ID},
	})
	require.NoError(t, err)

	// auth.mtls_binding_policy defaults to "required": a login certificate
	// must be enrolled against the account before it authenticates, the same
	// way production binds the bootstrap admin's certificate during first
	// boot (pkg/firstboot.UserCertBinder).
	adminCertBlock, _ := pem.Decode(adminKP.CertPEM)
	require.NotNil(t, adminCertBlock, "adminKP.CertPEM has no PEM block")
	adminCert, err := x509.ParseCertificate(adminCertBlock.Bytes)
	require.NoError(t, err)
	_, err = svcNode.User.AddCertBinding(context.Background(), createdAdmin.ID, user.ComputeSPKIThumbprint(adminCert), 0)
	require.NoError(t, err)

	return n, ca, adminKP
}

// restStatusRequest issues GET /status against the node's REST listener,
// optionally presenting a client certificate, and returns the response
// status code.
func restStatusRequest(t *testing.T, addr string, ca *qrdbdharness.CA, clientCert *tls.Certificate) int {
	t.Helper()
	tlsCfg := &tls.Config{RootCAs: ca.Pool, MinVersion: tls.VersionTLS13}
	if clientCert != nil {
		tlsCfg.Certificates = []tls.Certificate{*clientCert}
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}}

	resp, err := client.Get("https://" + addr + "/status")
	require.NoError(t, err)
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()
	return resp.StatusCode
}

// TestEmbeddedNode_RESTAuthMiddleware_RejectsUnauthenticated proves the
// security-gap fix end to end over the real REST transport: a request
// presenting NO client certificate against the admin:status-gated /status
// route is rejected (401), where before this fix the SDK node's
// multi-protocol server never received server.WithAuthMiddleware and every
// RBAC-gated route answered 200 with no credential.
func TestEmbeddedNode_RESTAuthMiddleware_RejectsUnauthenticated(t *testing.T) {
	n, ca, _ := newAuthEnforcedRESTNode(t)
	addr := n.Server().RESTAddr()
	require.NotEmpty(t, addr)

	status := restStatusRequest(t, addr, ca, nil)
	require.Equal(t, http.StatusUnauthorized, status,
		"an unauthenticated request to a permission-gated route must be rejected")
}

// TestEmbeddedNode_RESTAuthMiddleware_AcceptsAuthenticatedAdmin proves the
// accept side: a request presenting the seeded admin's mTLS client
// certificate (CN=admin, OU=system) is authenticated, resolves the admin
// role's admin:status grant through the SAME RBAC authorizer
// cmd/qrdbd/start.go builds (pkg/auth/wiring.BuildAuthMiddleware), and is
// admitted.
func TestEmbeddedNode_RESTAuthMiddleware_AcceptsAuthenticatedAdmin(t *testing.T) {
	n, ca, adminKP := newAuthEnforcedRESTNode(t)
	addr := n.Server().RESTAddr()
	require.NotEmpty(t, addr)

	clientCert, err := adminKP.TLSCertificate()
	require.NoError(t, err)

	status := restStatusRequest(t, addr, ca, &clientCert)
	require.Equal(t, http.StatusOK, status,
		"an authenticated admin request to a role-granted route must be admitted")
}

// TestEmbeddedNode_RESTAuthMiddleware_Disabled verifies that cfg.Auth.Enabled
// = false runs the multi-protocol server with no auth middleware installed
// (the operator's explicit choice), matching qrdbd's own posture rather than
// enforcing a policy the config disabled.
func TestEmbeddedNode_RESTAuthMiddleware_Disabled(t *testing.T) {
	certDir := t.TempDir()
	ca, err := qrdbdharness.NewCA(certDir)
	require.NoError(t, err)
	serverKP, err := ca.Issue(certDir, qrdbdharness.WithSubject("node", ""), qrdbdharness.WithServerAuth())
	require.NoError(t, err)
	serverCert, err := serverKP.TLSCertificate()
	require.NoError(t, err)

	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.NoClientCert,
		MinVersion:   tls.VersionTLS13,
	}

	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	cfg.Protocols.REST = true
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.AdminPort = freeTCPPort(t)
	cfg.Auth = qrdbconfig.AuthConfig{Enabled: qrdbconfig.BoolPtr(false)}

	n, err := NewStandaloneNode(context.Background(), cfg, []server.ServerOption{server.WithTLS(tlsCfg)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })

	addr := n.Server().RESTAddr()
	require.NotEmpty(t, addr)

	status := restStatusRequest(t, addr, ca, nil)
	require.Equal(t, http.StatusOK, status,
		"auth.enabled=false must run the route unauthenticated by explicit configuration")
}

// TestEmbeddedNode_EmbeddedAuthMiddleware_WiredWheneverAuthIsEnabled is the
// embedded-transport sibling of the three REST tests above: node.go hands
// the embedded transport the SAME *middleware.AuthMiddleware instance
// pkg/auth/wiring.BuildAuthMiddleware produced for the network transports
// (embedded.WithAuthMiddleware), conditioned only on authMW != nil --
// which BuildAuthMiddleware returns exactly when cfg.Auth.Enabled is true.
// So whenever auth is enabled, the embedded transport must never have a
// nil AuthMiddleware, and a caller with no identity bound to ctx must be
// refused exactly as an unauthenticated REST request is.
func TestEmbeddedNode_EmbeddedAuthMiddleware_WiredWheneverAuthIsEnabled(t *testing.T) {
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false
	// cfg.Auth.Enabled defaults to true (qrdbconfig.DefaultConfig) -- the
	// production posture this test exercises.

	n, err := NewStandaloneNode(context.Background(), cfg, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })

	svcNode := n.Services()
	require.NotNil(t, svcNode.Role)
	require.NotNil(t, svcNode.User)

	adminRole, err := svcNode.Role.CreateRole(context.Background(), &role.CreateRoleRequest{
		Name:        "embedded-auth-mw-test-admin",
		TenantID:    tenantid.System,
		Permissions: []permission.Permission{permission.AdminStatus},
	})
	require.NoError(t, err)
	_, err = svcNode.User.CreateUser(context.Background(), &user.CreateUserRequest{
		WithoutEmailLoginKey: true,
		Username:             "admin",
		TenantID:             tenantid.System,
		RoleIDs:              []string{adminRole.ID},
	})
	require.NoError(t, err)

	client := n.Client()
	require.NotNil(t, client)

	// No identity bound to ctx: a caller the embedded transport cannot
	// identify at all. Before the embedded-authorization fix this answered
	// 200 unconditionally (the gap go-qrdb's security probe found).
	_, err = client.Admin().Status(context.Background())
	require.Error(t, err, "an unidentified embedded caller must be refused a permission-gated operation")
	require.ErrorIs(t, err, auth.ErrUnauthenticated)

	// The seeded admin's identity, bound the way every embedded caller
	// binds one (auth.WithIdentity): admitted.
	adminCtx := auth.WithIdentity(context.Background(), &auth.Identity{
		Subject:  "admin",
		TenantID: tenantid.System,
		Roles:    []string{adminRole.ID},
	})
	_, err = client.Admin().Status(adminCtx)
	require.NoError(t, err, "the seeded admin identity must be admitted")
}
