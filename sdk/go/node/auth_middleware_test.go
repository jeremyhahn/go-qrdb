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
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

// freeTCPPort reserves a port for the REST server through the shared harness
// allocator, which certifies it on TCP and on UDP (the HTTP/3 sibling binds
// the same number) and locks it against every other test process. AdminPort
// left at its zero value would be forced to the fixed default 63001 by
// buildNodeServerConfig, risking collisions between parallel test binaries.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	port, err := qrdbdharness.ReserveTCPPort()
	require.NoError(t, err)
	return port
}

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

	_, err = svcNode.User.CreateUser(context.Background(), &user.CreateUserRequest{WithoutEmailLoginKey: true,
		Username: "admin",
		TenantID: tenantid.System,
		RoleIDs:  []string{adminRole.ID},
	})
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
