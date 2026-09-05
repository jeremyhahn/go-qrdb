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
	"errors"
	"strings"
	"testing"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	mcptransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/mcp"
)

// The user-not-found conformance matrix (go-qrdb#289).
//
// One condition — "this user does not exist" — reached six ways, and before
// #289 it produced four different Go types. Measured on dev/main across 11
// methods and 6 transports:
//
//	embedded  *user.UserNotFoundError
//	REST      *transport.UserNotFoundError on reads, a bare 404 on writes
//	gRPC      *transport.KeyNotFoundError
//	unix      *transport.KeyNotFoundError
//	QUIC      *transport.RequestError
//	MCP       *transport.KeyNotFoundError, by substring-matching "not found"
//
// errors.As against transport.UserNotFoundError matched 4 of 66 cells, so a
// consumer's absence check worked over REST and silently failed everywhere
// else. This suite is what makes that unrepeatable: it asserts the exact
// classification of EVERY cell, including the two transports #289 did not fix.
//
// Pinning the unfixed cells is the point, not an oversight. A suite covering
// only the four repaired transports would read as "the SDK is uniform" while
// two thirds of a consumer's deployments behaved differently — a worse state
// than the measured inconsistency, because it would be invisible. When the
// QUIC wire status and the MCP not-found code land, the quic and mcp rows flip
// to typed and this file is where that is stated.

// notFoundClass is what a cell is required to do with a missing user.
type notFoundClass int

const (
	// typedNotFound: errors.As resolves *transport.UserNotFoundError, which is
	// the predicate the SDK promises and sdk.UserNotFoundError aliases.
	typedNotFound notFoundClass = iota

	// opaqueNotFound: the transport reports a failure that a consumer CANNOT
	// classify as absence without reading the message text. This is a recorded
	// gap, never an acceptable resting state — see the per-cell reasons below.
	opaqueNotFound
)

func (c notFoundClass) String() string {
	if c == typedNotFound {
		return "typed"
	}
	return "opaque"
}

// userNotFoundTransports is the transport axis. It is every transport the SDK
// ships, including the two that cannot yet carry the type — enumerating them
// is what keeps the gap visible.
var userNotFoundTransports = []transportKind{
	embeddedTransport,
	restTransport,
	grpcTransport,
	unixTransport,
	quicTransport,
	mcpTransport,
}

// wantClass says what each transport must do with a missing user on the
// single-subject user methods.
//
// A single-subject method is one whose not-found can mean exactly one thing.
// That is what makes the mapping honest: the client reconstitutes the error
// from the id or key IT asked for, and never from the server's message.
func wantClass(tk transportKind) notFoundClass {
	switch tk {
	case quicTransport:
		// pkg/api/quic/wire carries no not-found status at all — OK, Error,
		// Unimplemented, Unauthenticated, PermissionDenied, Unavailable — so a
		// missing user arrives as the generic Error and is indistinguishable
		// from any other server-side failure. Carrying it typed needs a new
		// wire status, which is protocol design that outlives #289 and which
		// go-qrdb#169 needs identically. Escalated, not invented here.
		return opaqueNotFound
	case mcpTransport:
		// go-qrdb has no JSON-RPC error-code mapping (conventions/errors.md
		// §5), so a tool error reaches the client as a message string. The MCP
		// transport currently classifies four of these by
		// strings.Contains(msg, "not found"), which is why they are typed
		// today — but that is the fail-open this issue exists to remove, not a
		// working implementation, so the cell is recorded as opaque and the
		// negative test below pins that the substring must not be trusted.
		return opaqueNotFound
	default:
		return typedNotFound
	}
}

// userNotFoundCall is one method exercised against a user that does not exist.
type userNotFoundCall struct {
	name string

	// wantID is the numeric id the typed error must carry, or 0 when the
	// lookup was by a non-numeric key and Lookup carries the descriptor.
	wantID uint64

	call func(ctx context.Context, c userClients) error
}

// missingUserID is an id no cell ever creates.
const missingUserID uint64 = 999999

// singleSubjectCalls are the methods whose not-found can only mean the user.
//
// AssignRole and UnassignRole are deliberately absent and are covered by
// TestUserNotFound_AmbiguousMethods instead: measured on every remote
// transport, a missing user and a missing role produce the SAME status and
// differ only in message text, so typing them at the client would reintroduce
// exactly the substring classification this suite exists to forbid.
var singleSubjectCalls = []userNotFoundCall{
	{"GetUser", missingUserID, func(ctx context.Context, c userClients) error {
		_, err := c.users.GetUser(ctx, missingUserID)
		return err
	}},
	{"GetUserByUsername", 0, func(ctx context.Context, c userClients) error {
		_, err := c.users.GetUserByUsername(ctx, tenant.SystemTenantID, "nope-nobody")
		return err
	}},
	{"GetUserByEmail", 0, func(ctx context.Context, c userClients) error {
		_, err := c.users.GetUserByEmail(ctx, tenant.SystemTenantID, "nobody@example.com")
		return err
	}},
	{"GetUserByCertThumbprint", 0, func(ctx context.Context, c userClients) error {
		_, err := c.users.GetUserByCertThumbprint(ctx, "deadbeef")
		return err
	}},
	{"AddCertBinding", missingUserID, func(ctx context.Context, c userClients) error {
		// A syntactically valid SPKI thumbprint: an invalid one is rejected by
		// validation BEFORE the user lookup, and the cell would then assert
		// nothing about not-found at all.
		_, err := c.users.AddCertBinding(ctx, missingUserID, validThumbprint, 0)
		return err
	}},
	{"UpdateUser", missingUserID, func(ctx context.Context, c userClients) error {
		_, err := c.users.UpdateUser(ctx, &transport.UpdateUserRequest{ID: missingUserID})
		return err
	}},
	{"EnableUser", missingUserID, func(ctx context.Context, c userClients) error {
		return c.users.EnableUser(ctx, missingUserID)
	}},
	{"DisableUser", missingUserID, func(ctx context.Context, c userClients) error {
		return c.users.DisableUser(ctx, missingUserID)
	}},
	{"DeleteUser", missingUserID, func(ctx context.Context, c userClients) error {
		return c.users.DeleteUser(ctx, missingUserID)
	}},
}

// validThumbprint is a well-formed base64url SHA-256 SPKI thumbprint bound to
// nothing.
const validThumbprint = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

// TestUserNotFound_Matrix walks every transport against every single-subject
// user method and asserts the classification each one is required to produce.
func TestUserNotFound_Matrix(t *testing.T) {
	for _, tk := range userNotFoundTransports {
		want := wantClass(tk)
		t.Run(string(tk), func(t *testing.T) {
			c := bootUserCell(t, tk)
			for _, call := range singleSubjectCalls {
				t.Run(call.name, func(t *testing.T) {
					err := call.call(boundedCtx(t), c)
					if err == nil {
						t.Fatalf("%s/%s: a lookup of a user that does not exist returned no error", tk, call.name)
					}

					var unf *transport.UserNotFoundError
					got := opaqueNotFound
					if errors.As(err, &unf) {
						got = typedNotFound
					}
					if got != want {
						t.Fatalf("%s/%s: classified %s, want %s (err %T: %v)",
							tk, call.name, got, want, err, err)
					}
					if want != typedNotFound {
						return
					}

					// The typed error must also carry WHICH user was missing.
					// A type that matches but names nothing pushes the caller
					// straight back to the message text.
					if call.wantID != 0 {
						if unf.ID != call.wantID {
							t.Errorf("%s/%s: ID = %d, want %d", tk, call.name, unf.ID, call.wantID)
						}
						return
					}
					if unf.Lookup == "" {
						t.Errorf("%s/%s: a non-id lookup left Lookup empty, so the error names no key",
							tk, call.name)
					}
				})
			}
		})
	}
}

// TestUserNotFound_AmbiguousMethods pins that AssignRole and UnassignRole are
// NOT typed, on any transport, and says why.
//
// This is not a lowered bar. Measured on every remote transport, assigning a
// role fails with the identical status whether the USER or the ROLE is
// missing — 404 over REST, codes.NotFound over gRPC and unix, wire status 1
// over QUIC, a tool error over MCP — and the two are separable only by reading
// the message. A client that mapped that status to UserNotFoundError would
// report a missing ROLE as a missing USER, which is the same class of defect
// as the substring matching, arrived at from the other direction.
//
// The information exists at the server: embedded returns *user.UserNotFoundError
// and *role.RoleNotFoundError correctly. It is the wire that loses it, which is
// why the fix belongs with the QUIC/MCP protocol work rather than here.
func TestUserNotFound_AmbiguousMethods(t *testing.T) {
	for _, tk := range userNotFoundTransports {
		t.Run(string(tk), func(t *testing.T) {
			c := bootUserCell(t, tk)
			ctx := boundedCtx(t)

			for _, tc := range []struct {
				name string
				err  error
			}{
				{"AssignRole", c.roles.AssignRole(ctx, missingUserID, c.roleID)},
				{"UnassignRole", c.roles.UnassignRole(ctx, missingUserID, c.roleID)},
			} {
				if tc.err == nil {
					t.Fatalf("%s/%s: a missing user returned no error", tk, tc.name)
				}
				var unf *transport.UserNotFoundError
				if errors.As(tc.err, &unf) {
					t.Fatalf("%s/%s: classified as a typed user-not-found. If that is now "+
						"correct, the server must distinguish a missing user from a missing "+
						"role on the wire — see this test's comment before relaxing it",
						tk, tc.name)
				}
			}
		})
	}
}

// TestUserNotFound_MissingRoleIsNotAMissingUser is the negative that matters.
//
// It assigns a REAL user a role that does not exist. The server's message
// contains "not found", the status is the same one a missing user produces,
// and the correct answer is still "this is not a user-not-found". Any client
// that reintroduces substring classification on the user path — the shape
// go-qrdb#286 was asked to stop using, and the shape the MCP transport still
// carries at transport.go's isNotFoundError — fails here.
//
// This is the assertion that makes the whole file worth running: the matrix
// above proves the predicate FIRES when it should, and only this proves it
// does not fire when it should not.
func TestUserNotFound_MissingRoleIsNotAMissingUser(t *testing.T) {
	for _, tk := range userNotFoundTransports {
		t.Run(string(tk), func(t *testing.T) {
			c := bootUserCell(t, tk)

			err := c.roles.AssignRole(boundedCtx(t), c.realUserID, "no-such-role")
			if err == nil {
				t.Fatalf("%s: assigning a role that does not exist succeeded", tk)
			}
			if !strings.Contains(strings.ToLower(err.Error()), "not found") {
				t.Fatalf("%s: this cell only tests what it is meant to when the message "+
					"contains \"not found\"; got %q", tk, err)
			}

			var unf *transport.UserNotFoundError
			if errors.As(err, &unf) {
				t.Fatalf("%s: a MISSING ROLE was classified as a missing USER (%v). "+
					"A not-found predicate that keys on message text reports absence of "+
					"the wrong subject — that is fail-open classification, not a fix", tk, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Bring-up
// ---------------------------------------------------------------------------

// userClients is the identity surface under test, however it was reached.
type userClients struct {
	users transport.UserClient
	roles transport.RoleClient

	// roleID names a role that EXISTS, so an AssignRole against a missing user
	// fails for the user rather than for the role.
	roleID string

	// realUserID names a user that EXISTS, for the inverse case.
	realUserID uint64
}

// bootUserCell brings up a standalone node carrying exactly one listener and
// returns clients reaching it over the given transport.
//
// Standalone/memory throughout: the axis under test is the TRANSPORT, and a
// missing user is missing identically on every backend and in every deployment
// mode. Booting the engine and mode axes here would multiply the runtime
// without varying anything the assertions read.
func bootUserCell(t *testing.T, tk transportKind) userClients {
	t.Helper()

	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Server.Host = "127.0.0.1"
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "none"

	// Exactly one listener per cell, as in the guarded matrix: a cell tests one
	// transport and starting the others spends ports on paths it never calls.
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

	var serverOpts []server.ServerOption
	var ca *qrdbdharness.CA
	var clientKP *qrdbdharness.KeyPair
	if tk.remote() {
		ca, clientKP, serverOpts = remoteServerTLS(t)
	}

	n, err := node.NewStandaloneNode(context.Background(), cfg, serverOpts)
	if err != nil {
		t.Fatalf("boot %s: %v", tk, err)
	}
	t.Cleanup(func() { _ = n.Close() })

	roleID, realUserID := seedIdentityFixtures(t, n)

	if !tk.remote() {
		return userClients{
			users:      n.Client().Users(),
			roles:      n.Client().Roles(),
			roleID:     roleID,
			realUserID: realUserID,
		}
	}

	clientCert, err := clientKP.TLSCertificate()
	if err != nil {
		t.Fatalf("client TLS cert: %v", err)
	}
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      ca.Pool,
		ServerName:   "127.0.0.1",
		MinVersion:   tls.VersionTLS13,
	}

	sdkClient, err := sdk.NewClient(dialUser(t, tk, n, tlsCfg))
	if err != nil {
		t.Fatalf("new SDK client: %v", err)
	}
	t.Cleanup(func() { _ = sdkClient.Close() })
	if err := sdkClient.Connect(boundedCtx(t)); err != nil {
		t.Fatalf("%s connect: %v", tk, err)
	}

	return userClients{
		users:      sdkClient.Users(),
		roles:      sdkClient.Roles(),
		roleID:     roleID,
		realUserID: realUserID,
	}
}

// dialUser builds the SDK transport for this cell. It defers to the guarded
// matrix's dialer for the five transports that suite already covers, and
// carries only the MCP case itself — MCP is reached over Streamable HTTP at
// /mcp rather than through a bare host:port, so it does not fit that switch.
func dialUser(t *testing.T, tk transportKind, n *node.Node, tlsCfg *tls.Config) transport.Transport {
	t.Helper()
	if tk != mcpTransport {
		return tk.dial(t, n, tlsCfg)
	}

	addr := n.Server().MCPAddr()
	if addr == "" {
		t.Fatal("the node reports no MCP address; the listener did not bind")
	}
	tr, err := mcptransport.New(
		transport.WithAddress("https://"+addr+"/mcp"),
		transport.WithTLSConfig(tlsCfg),
	)
	if err != nil {
		t.Fatalf("new mcp transport: %v", err)
	}
	return tr
}

// seedIdentityFixtures creates the role and the user the negative cases need,
// and grants the acting certificate the identity permissions the matrix
// exercises.
//
// The grant is deliberately complete for users and roles: a permission the
// cell lacks turns a not-found cell into a permission-denied cell, and the
// assertion would then pass or fail for a reason unrelated to absence. That
// masking is real — the first run of this measurement reported
// GetUserByCertThumbprint and DeleteUser as generic errors on four transports
// purely because the seeded role could not call them.
func seedIdentityFixtures(t *testing.T, n *node.Node) (roleID string, realUserID uint64) {
	t.Helper()
	svc := n.Services()
	ctx := boundedCtx(t)

	r, err := svc.Role.CreateRole(ctx, &role.CreateRoleRequest{
		Name:     "notfound-matrix",
		TenantID: tenant.SystemTenantID,
		Permissions: []permission.Permission{
			permission.UsersCreate, permission.UsersRead, permission.UsersUpdate,
			permission.UsersDelete, permission.UsersList,
			permission.UsersEnable, permission.UsersDisable,
			permission.UsersGetByCertThumbprint, permission.UsersAddCertBinding,
			permission.RolesCreate, permission.RolesRead, permission.RolesList,
			permission.RolesAssign, permission.RolesUnassign,
			permission.AdminHealth, permission.AdminStatus,
		},
	})
	if err != nil {
		t.Fatalf("create role: %v", err)
	}

	// Username "matrix" is the subject the harness CA issues the client
	// certificate to, so this is the identity a remote cell authenticates as.
	u, err := svc.User.CreateUser(ctx, &user.CreateUserRequest{
		WithoutEmailLoginKey: true,
		Username:             "matrix",
		TenantID:             tenant.SystemTenantID,
		RoleIDs:              []string{r.ID},
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return r.ID, u.ID
}
