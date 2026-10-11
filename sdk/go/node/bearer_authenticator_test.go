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

//go:build !integration

package node

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

// The token the host's bearer authenticator accepts, and the account it
// names. The account's permitted-method policy is WebAuthn only: the host
// minted the token after a WebAuthn ceremony, so the token carries no method
// the chain could compare, and the authenticator declares that.
const (
	hostBearerToken   = "Bearer host-issued-token"
	hostBearerSubject = "bearer-admin"
)

// hostTokenAuthenticator is an embedder's bearer validator in the shape
// go-quicbaas and go-xkms register, written against the SDK alone: it reads
// the credential carrier every transport fills (sdk.HTTPAuthorizationFromContext),
// declines with sdk.ErrUnauthenticated, and declares its method unobservable
// through sdk.UnobservableMethodAuthenticator. No pkg/auth import is needed.
type hostTokenAuthenticator struct{}

var _ sdk.UnobservableMethodAuthenticator = hostTokenAuthenticator{}

func (hostTokenAuthenticator) Authenticate(ctx context.Context) (*sdk.Identity, error) {
	if sdk.HTTPAuthorizationFromContext(ctx) != hostBearerToken {
		return nil, sdk.ErrUnauthenticated
	}
	return &sdk.Identity{Subject: hostBearerSubject, TenantID: sdk.SystemTenantID}, nil
}

func (hostTokenAuthenticator) Name() string { return "host-bearer" }

func (hostTokenAuthenticator) MethodUnobservable() bool { return true }

// callerCredential is what a test caller presents: an Authorization value, a
// client certificate, both or neither.
type callerCredential struct {
	authorization string
	cert          *tls.Certificate
}

// bearer is a credential carrying only an Authorization value.
func bearer(authorization string) callerCredential {
	return callerCredential{authorization: authorization}
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

// bearerTransport is one transport's way of making an admin:status call with
// a caller credential. It returns nil when the call was admitted, and the
// transport's refusal otherwise.
type bearerTransport struct {
	name      string
	configure func(t *testing.T, cfg *qrdbconfig.Config)
	call      func(ctx context.Context, n *Node, ca *qrdbdharness.CA, cred callerCredential) error
	// unauthenticated reports whether err is the transport's
	// "unauthenticated" answer, as opposed to any other failure.
	unauthenticated func(err error) bool
}

// TestWithAuthenticators_BearerAuthenticatesOnEveryNetworkTransport: an
// SDK-only authenticator registered with WithAuthenticators reads the caller's
// Authorization value on REST, gRPC, Unix and MCP alike, so a bearer-only
// caller (no client certificate) is admitted on each, and a missing or wrong
// token is refused as unauthenticated on each. The account's method policy
// excludes the bearer, so admission also proves the authenticator's
// unobservable declaration is honoured on every transport.
//
// QUIC is not in the table: its request frame carries no credential field, so
// a bearer has nowhere to travel on that transport.
//
// It holds in every chain position: a bearer-only caller presents nothing mTLS
// could accept, so the bearer decides wherever it sits.
func TestWithAuthenticators_BearerAuthenticatesOnEveryNetworkTransport(t *testing.T) {
	for _, pos := range []sdk.ChainPosition{sdk.ChainBeforeMTLS, sdk.ChainAfterMTLS, sdk.ChainReplaceMTLS} {
		for _, bt := range bearerTransports() {
			t.Run(pos.String()+"/"+bt.name, func(t *testing.T) {
				n, ca := newBearerNode(t, bt.configure, WithAuthenticators(pos, hostTokenAuthenticator{}))
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()

				require.NoError(t, bt.call(ctx, n, ca, bearer(hostBearerToken)), "a valid bearer was refused")

				for _, refused := range []struct{ name, authorization string }{
					{"no credential", ""},
					{"wrong token", "Bearer forged"},
					{"right token, wrong scheme", strings.Replace(hostBearerToken, "Bearer", "Basic", 1)},
				} {
					err := bt.call(ctx, n, ca, bearer(refused.authorization))
					require.Error(t, err, "%s was admitted", refused.name)
					require.True(t, bt.unauthenticated(err), "%s: refusal is not unauthenticated: %v", refused.name, err)
				}
			})
		}
	}
}

func bearerTransports() []bearerTransport {
	grpcUnauthenticated := func(err error) bool { return status.Code(err) == codes.Unauthenticated }
	grpcStatus := func(target string) func(context.Context, *Node, *qrdbdharness.CA, callerCredential) error {
		return func(ctx context.Context, n *Node, ca *qrdbdharness.CA, cred callerCredential) error {
			addr := n.Server().GRPCAddr()
			if target == "unix" {
				addr = "unix://" + n.Server().UnixAddr()
			}
			creds := credentials.NewTLS(cred.clientTLS(ca))
			conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
			if err != nil {
				return err
			}
			defer func() { _ = conn.Close() }()
			if cred.authorization != "" {
				ctx = metadata.AppendToOutgoingContext(ctx, "authorization", cred.authorization)
			}
			_, err = pb.NewQRDBServiceClient(conn).Status(ctx, &emptypb.Empty{})
			return err
		}
	}
	return []bearerTransport{
		{
			name: "rest",
			configure: func(t *testing.T, cfg *qrdbconfig.Config) {
				cfg.Protocols.REST = true
				cfg.Server.AdminPort = freeTCPPort(t)
			},
			call: func(ctx context.Context, n *Node, ca *qrdbdharness.CA, cred callerCredential) error {
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+n.Server().RESTAddr()+"/status", nil)
				if err != nil {
					return err
				}
				if cred.authorization != "" {
					req.Header.Set("Authorization", cred.authorization)
				}
				resp, err := bearerHTTPClient(ca, cred).Do(req)
				if err != nil {
					return err
				}
				defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()
				if resp.StatusCode != http.StatusOK {
					return &restStatusError{code: resp.StatusCode}
				}
				return nil
			},
			unauthenticated: func(err error) bool {
				se, ok := err.(*restStatusError)
				return ok && se.code == http.StatusUnauthorized
			},
		},
		{
			name: "grpc",
			configure: func(t *testing.T, cfg *qrdbconfig.Config) {
				cfg.Protocols.GRPC = true
				cfg.Server.GRPCPort = freeTCPPort(t)
			},
			call:            grpcStatus("grpc"),
			unauthenticated: grpcUnauthenticated,
		},
		{
			name: "unix",
			configure: func(t *testing.T, cfg *qrdbconfig.Config) {
				cfg.Protocols.Unix = true
				cfg.Unix.SocketPath = shortSocketPath(t)
			},
			call:            grpcStatus("unix"),
			unauthenticated: grpcUnauthenticated,
		},
		{
			name: "mcp",
			configure: func(t *testing.T, cfg *qrdbconfig.Config) {
				cfg.Protocols.MCP = true
				cfg.MCP.Port = freeTCPPort(t)
			},
			call: func(ctx context.Context, n *Node, ca *qrdbdharness.CA, cred callerCredential) error {
				opts := []mcptransport.StreamableHTTPCOption{mcptransport.WithHTTPBasicClient(bearerHTTPClient(ca, cred))}
				if cred.authorization != "" {
					opts = append(opts, mcptransport.WithHTTPHeaders(map[string]string{"Authorization": cred.authorization}))
				}
				c, err := client.NewStreamableHttpClient("https://"+n.Server().MCPAddr()+"/mcp", opts...)
				if err != nil {
					return err
				}
				defer func() { _ = c.Close() }()
				if err := c.Start(ctx); err != nil {
					return err
				}
				init := mcp.InitializeRequest{}
				init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
				init.Params.ClientInfo = mcp.Implementation{Name: "bearer-test", Version: "1"}
				if _, err := c.Initialize(ctx, init); err != nil {
					return err
				}
				call := mcp.CallToolRequest{}
				call.Params.Name = "qrdb_status"
				res, err := c.CallTool(ctx, call)
				if err != nil {
					return err
				}
				if res.IsError {
					return &mcpToolError{result: res}
				}
				return nil
			},
			unauthenticated: func(err error) bool {
				msg := strings.ToLower(err.Error())
				return strings.Contains(msg, "401") || strings.Contains(msg, "unauthorized") || strings.Contains(msg, "unauthenticated")
			},
		},
	}
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

// bearerHTTPClient trusts the node's CA and presents cred's client
// certificate, if any.
func bearerHTTPClient(ca *qrdbdharness.CA, cred callerCredential) *http.Client {
	return &http.Client{Transport: &http.Transport{TLSClientConfig: cred.clientTLS(ca)}}
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

type restStatusError struct{ code int }

func (e *restStatusError) Error() string { return "rest: status " + http.StatusText(e.code) }

type mcpToolError struct{ result *mcp.CallToolResult }

func (e *mcpToolError) Error() string {
	for _, c := range e.result.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			return "mcp tool error: " + tc.Text
		}
	}
	return "mcp tool error"
}
