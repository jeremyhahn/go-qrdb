//go:build integration

package integration_test

import (
	"context"
	"crypto/tls"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

const (
	// cliBearerToken is the host-issued token the CLI reads from its
	// --bearer-token-file; cliForgedBearerToken is one the host never issued.
	cliBearerToken       = "cli-host-issued-token"
	cliForgedBearerToken = "cli-forged-token"
	cliBearerSubject     = "cli-bearer-operator"
)

// cliHostTokenAuthenticator is an embedder's bearer validator in the shape
// go-quicbaas and go-xkms register: it reads the Authorization value every
// transport fills, declines with sdk.ErrUnauthenticated, and declares its
// method unobservable.
type cliHostTokenAuthenticator struct{}

var _ sdk.UnobservableMethodAuthenticator = cliHostTokenAuthenticator{}

func (cliHostTokenAuthenticator) Authenticate(ctx context.Context) (*sdk.Identity, error) {
	if sdk.HTTPAuthorizationFromContext(ctx) != "Bearer "+cliBearerToken {
		return nil, sdk.ErrUnauthenticated
	}
	return &sdk.Identity{Subject: cliBearerSubject, TenantID: sdk.SystemTenantID}, nil
}

func (cliHostTokenAuthenticator) Name() string { return "cli-host-bearer" }

func (cliHostTokenAuthenticator) MethodUnobservable() bool { return true }

// startBearerCLINode starts an in-process node serving all five protocols
// over TLS that admits a certificate-less caller to the handshake
// (VerifyClientCertIfGiven) and authenticates it by the host bearer
// authenticator, ahead of mTLS. The bearer's subject is a system-tenant
// account holding admin:status that can log in by WebAuthn only, so the
// bearer is its only way in from the CLI.
func startBearerCLINode(t *testing.T) *multiProtoNode {
	t.Helper()
	ca := generateTestCA(t)
	serverKP := ca.issue(t, t.TempDir(), qrdbdharness.WithSubject("node", ""), qrdbdharness.WithServerAuth())
	serverCert, err := serverKP.TLSCertificate()
	if err != nil {
		t.Fatalf("server TLS certificate: %v", err)
	}

	sockDir, err := os.MkdirTemp("", "qrdb-bearer-")
	if err != nil {
		t.Fatalf("socket dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	unixSocket := filepath.Join(sockDir, "qrdbd.sock")

	tcp := allocateTCPPorts(t, 3)
	quic := allocatePorts(t, 1)

	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.AdminPort, cfg.Server.GRPCPort, cfg.MCP.Port = tcp[0], tcp[1], tcp[2]
	cfg.Server.QUICPort = quic[0]
	cfg.Unix.SocketPath = unixSocket
	cfg.Protocols.REST, cfg.Protocols.GRPC, cfg.Protocols.QUIC = true, true, true
	cfg.Protocols.MCP, cfg.Protocols.Unix = true, true
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}

	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    ca.Pool,
		ClientAuth:   tls.VerifyClientCertIfGiven,
		MinVersion:   tls.VersionTLS13,
	}
	n, err := node.NewStandaloneNode(context.Background(), cfg,
		[]server.ServerOption{server.WithTLS(tlsCfg), server.WithCACertPool(ca.Pool)},
		node.WithAuthenticators(sdk.ChainBeforeMTLS, cliHostTokenAuthenticator{}),
	)
	if err != nil {
		t.Fatalf("NewStandaloneNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })

	ctx := context.Background()
	statusRole, err := n.Services().Role.CreateRole(ctx, &role.CreateRoleRequest{
		Name:        "cli-bearer-status",
		TenantID:    tenantid.System,
		Permissions: []permission.Permission{permission.AdminStatus},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	if _, err := n.Services().User.CreateUser(ctx, &user.CreateUserRequest{
		WithoutEmailLoginKey: true,
		Username:             cliBearerSubject,
		TenantID:             tenantid.System,
		RoleIDs:              []string{statusRole.ID},
		AuthMethods:          []string{auth.MethodWebAuthn},
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	return &multiProtoNode{
		cli:        buildCLIBinary(t),
		ca:         ca,
		unixSocket: unixSocket,
		restPort:   tcp[0],
		grpcPort:   tcp[1],
		mcpPort:    tcp[2],
		quicPort:   quic[0],
	}
}

// writeCLITokenFile writes token, newline-terminated as an operator's
// editor or a projected token would leave it, to a fresh file.
func writeCLITokenFile(t *testing.T, token string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	return path
}

// TestBearerToken_CLI_PerProtocol drives `qrdb admin status` with no client
// certificate on every transport: --bearer-token-file holding the host's
// token is admitted over rest, grpc, mcp and unix; no flag and a forged token
// are refused as unauthenticated; quic refuses the token source before any
// request. No refused output ever carries the token.
func TestBearerToken_CLI_PerProtocol(t *testing.T) {
	n := startBearerCLINode(t)
	valid := writeCLITokenFile(t, cliBearerToken)
	forged := writeCLITokenFile(t, cliForgedBearerToken)

	run := func(t *testing.T, proto multiProtocol, extra ...string) (string, error) {
		t.Helper()
		args := []string{"--server", n.serverURLFor(proto), "--ca-file", n.ca.CertPath, "--timeout", "20s"}
		args = append(args, extra...)
		args = append(args, "admin", "status")
		return runCLI(t, n.cli, args...)
	}
	assertRefused := func(t *testing.T, out string, err error, want ...string) {
		t.Helper()
		if err == nil {
			t.Fatalf("CLI succeeded; want refusal. output:\n%s", out)
		}
		if strings.Contains(out, cliBearerToken) || strings.Contains(out, cliForgedBearerToken) {
			t.Fatalf("refused output carries a token:\n%s", out)
		}
		lower := strings.ToLower(out)
		for _, w := range want {
			if strings.Contains(lower, w) {
				return
			}
		}
		t.Fatalf("refusal does not name any of %q:\n%s", want, out)
	}

	for _, proto := range multiProtocols {
		t.Run(proto.name, func(t *testing.T) {
			if proto.name == "quic" {
				out, err := run(t, proto, "--bearer-token-file", valid)
				assertRefused(t, out, err, "tokensource")
				return
			}

			out, err := run(t, proto, "--bearer-token-file", valid)
			if err != nil {
				t.Fatalf("admin status with a valid bearer token file: %v\n%s", err, out)
			}

			out, err = run(t, proto)
			assertRefused(t, out, err, "unauthenticated", "401")

			out, err = run(t, proto, "--bearer-token-file", forged)
			assertRefused(t, out, err, "unauthenticated", "401")
		})
	}
}
