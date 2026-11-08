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

// The helpers in this file are shared by test files under different build
// constraints, so the file carries none: its constraint is the union of every
// caller's (AGENTS.md, "Build constraints on test files").

package node

import (
	"context"
	"crypto/tls"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

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

// hostBearerSubject is the account the host bearer names, seeded by
// newBearerNode.
const hostBearerSubject = "bearer-admin"

// callerCredential is what a test caller presents: an Authorization value, a
// client certificate, both or neither.
type callerCredential struct {
	authorization string
	cert          *tls.Certificate
}

// clientTLS is the client side of a call to the node: it trusts the node's
// CA and presents cred's certificate, if any.
func (cred callerCredential) clientTLS(ca *qrdbdharness.CA) *tls.Config {
	cfg := &tls.Config{RootCAs: ca.Pool, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS13}
	if cred.cert != nil {
		cfg.Certificates = []tls.Certificate{*cred.cert}
	}
	return cfg
}

// newBearerNode boots a standalone node serving one transport with opts (the
// authenticators under test), and the account the host bearer names seeded
// with admin:status and a WebAuthn-only method policy.
func newBearerNode(t *testing.T, configure func(*testing.T, *qrdbconfig.Config), opts ...NodeOption) (*Node, *qrdbdharness.CA) {
	t.Helper()
	certDir := t.TempDir()
	ca, err := qrdbdharness.NewCA(certDir)
	require.NoError(t, err)
	serverKP, err := ca.Issue(certDir, qrdbdharness.WithSubject("node", ""), qrdbdharness.WithServerAuth())
	require.NoError(t, err)
	serverCert, err := serverKP.TLSCertificate()
	require.NoError(t, err)

	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	cfg.Server.Host = "127.0.0.1"
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false
	configure(t, cfg)

	// VerifyClientCertIfGiven: a bearer-only caller presents no certificate
	// and must still complete the handshake and reach the auth middleware.
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    ca.Pool,
		ClientAuth:   tls.VerifyClientCertIfGiven,
		MinVersion:   tls.VersionTLS13,
	}
	n, err := NewStandaloneNode(context.Background(), cfg,
		[]server.ServerOption{server.WithTLS(tlsCfg), server.WithCACertPool(ca.Pool)}, opts...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })

	ctx := context.Background()
	statusRole, err := n.Services().Role.CreateRole(ctx, &role.CreateRoleRequest{
		Name:        "bearer-status",
		TenantID:    tenantid.System,
		Permissions: []permission.Permission{permission.AdminStatus},
	})
	require.NoError(t, err)
	_, err = n.Services().User.CreateUser(ctx, &user.CreateUserRequest{
		WithoutEmailLoginKey: true,
		Username:             hostBearerSubject,
		TenantID:             tenantid.System,
		RoleIDs:              []string{statusRole.ID},
		AuthMethods:          []string{auth.MethodWebAuthn},
	})
	require.NoError(t, err)
	return n, ca
}

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

// freeUDPPort returns a UDP port free at the time of the call, for the QUIC
// listener.
func freeUDPPort(t *testing.T) int {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	port := pc.LocalAddr().(*net.UDPAddr).Port
	require.NoError(t, pc.Close())
	return port
}

// shortSocketPath returns a unix socket path short enough for sun_path,
// which t.TempDir under a long test name can exceed.
func shortSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "qb")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "s.sock")
}

// awaitWritable waits for the single member to elect itself and accept a
// write, bounded so a node that never becomes writable fails the test.
func awaitWritable(ctx context.Context, t *testing.T, n *Node) {
	t.Helper()
	deadline := time.After(15 * time.Second)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		err := n.Client().KV().Put(ctx, "embedded/ready", []byte("1"))
		if err == nil {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("the clustered node never accepted a write: %v", err)
		case <-tick.C:
		}
	}
}
