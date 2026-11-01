package node

import (
	"context"
	"crypto/x509"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	grpctransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/grpc"
	mcptransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/mcp"
	quictransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/quic"
	resttransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/rest"
	unixtransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/unix"
)

// sealStatusCertSubject is the certificate subject of the caller the
// seal-disabled barrier tests enrol with the seal:status permission.
const sealStatusCertSubject = "seal-status-client"

// barrierStatusClient is the slice of an SDK transport these tests drive.
type barrierStatusClient interface {
	Connect(ctx context.Context) error
	Close() error
	BarrierStatus(ctx context.Context) (*transport.BarrierStatusResponse, error)
}

// sealDisabledTransport configures one listener on a seal-disabled node and
// dials it with the SDK transport for that protocol.
type sealDisabledTransport struct {
	name      string
	configure func(t *testing.T, cfg *qrdbconfig.Config)
	dial      func(n *Node, opts ...transport.Option) (barrierStatusClient, error)
}

func sealDisabledTransports() []sealDisabledTransport {
	return []sealDisabledTransport{
		{
			name: "rest",
			configure: func(t *testing.T, cfg *qrdbconfig.Config) {
				cfg.Protocols.REST = true
				cfg.Server.AdminPort = freeTCPPort(t)
			},
			dial: func(n *Node, opts ...transport.Option) (barrierStatusClient, error) {
				return resttransport.New(append(opts, transport.WithAddress("https://"+n.Server().RESTAddr()))...)
			},
		},
		{
			name: "grpc",
			configure: func(t *testing.T, cfg *qrdbconfig.Config) {
				cfg.Protocols.GRPC = true
				cfg.Server.GRPCPort = freeTCPPort(t)
			},
			dial: func(n *Node, opts ...transport.Option) (barrierStatusClient, error) {
				return grpctransport.New(append(opts, transport.WithAddress(n.Server().GRPCAddr()))...)
			},
		},
		{
			name: "quic",
			configure: func(t *testing.T, cfg *qrdbconfig.Config) {
				cfg.Protocols.QUIC = true
				cfg.Server.QUICPort = freeUDPPort(t)
			},
			dial: func(n *Node, opts ...transport.Option) (barrierStatusClient, error) {
				return quictransport.New(append(opts, transport.WithAddress(n.Server().QUICAddr()))...)
			},
		},
		{
			name: "unix",
			configure: func(t *testing.T, cfg *qrdbconfig.Config) {
				cfg.Protocols.Unix = true
				cfg.Unix.SocketPath = shortSocketPath(t)
			},
			dial: func(n *Node, opts ...transport.Option) (barrierStatusClient, error) {
				return unixtransport.New(append(opts, transport.WithAddress(n.Server().UnixAddr()))...)
			},
		},
		{
			name: "mcp",
			configure: func(t *testing.T, cfg *qrdbconfig.Config) {
				cfg.Protocols.MCP = true
				cfg.MCP.Port = freeTCPPort(t)
			},
			dial: func(n *Node, opts ...transport.Option) (barrierStatusClient, error) {
				return mcptransport.New(append(opts, transport.WithAddress("https://"+n.Server().MCPAddr()+"/mcp"))...)
			},
		},
	}
}

// enrolSealStatusClient enrols a certificate caller holding seal:status, the
// permission every transport's BarrierStatus route requires.
func enrolSealStatusClient(t *testing.T, n *Node, ca *qrdbdharness.CA) callerCredential {
	t.Helper()
	kp, err := ca.Issue(t.TempDir(), qrdbdharness.WithSubject(sealStatusCertSubject, tenantid.System))
	require.NoError(t, err)
	cert, err := kp.TLSCertificate()
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	require.NoError(t, err)

	ctx := context.Background()
	statusRole, err := n.Services().Role.CreateRole(ctx, &role.CreateRoleRequest{
		Name:        "seal-status",
		TenantID:    tenantid.System,
		Permissions: []permission.Permission{permission.AdminStatus, permission.SealStatus},
	})
	require.NoError(t, err)
	u, err := n.Services().User.CreateUser(ctx, &user.CreateUserRequest{
		WithoutEmailLoginKey: true,
		Username:             sealStatusCertSubject,
		TenantID:             tenantid.System,
		RoleIDs:              []string{statusRole.ID},
	})
	require.NoError(t, err)
	_, err = n.Services().User.AddCertBinding(ctx, u.ID, user.ComputeSPKIThumbprint(leaf), 0)
	require.NoError(t, err)
	return callerCredential{cert: &cert}
}

// requireProtectionNone asserts the status of a seal-disabled node's barrier:
// protection none, initialized, unsealed.
func requireProtectionNone(t *testing.T, st *transport.BarrierStatusResponse) {
	t.Helper()
	require.NotNil(t, st)
	require.False(t, st.Sealed, "a seal-disabled node reported its barrier sealed")
	require.True(t, st.Initialized, "barrier protection none is a configured barrier")
	require.Equal(t, string(seal.StrategyNone), st.Strategy)
	require.False(t, st.HardwareBacked)
}

// TestStandaloneNode_SealDisabledServesBarrierStatusOnEveryTransport is
// go-qrdb#1285: a seal-disabled node registered no barrier service, so every
// listener answered BarrierStatus with "barrier service is not configured".
// Protection none is a valid barrier, served on every transport.
func TestStandaloneNode_SealDisabledServesBarrierStatusOnEveryTransport(t *testing.T) {
	for _, tt := range sealDisabledTransports() {
		t.Run(tt.name, func(t *testing.T) {
			n, ca := newBearerNode(t, func(t *testing.T, cfg *qrdbconfig.Config) {
				cfg.Seal.Enabled = false
				tt.configure(t, cfg)
			})
			cred := enrolSealStatusClient(t, n, ca)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()

			tr, err := tt.dial(n, transport.WithTLSConfig(cred.clientTLS(ca)))
			require.NoError(t, err)
			defer func() { _ = tr.Close() }()
			require.NoError(t, tr.Connect(ctx))

			st, err := tr.BarrierStatus(ctx)
			require.NoError(t, err, "BarrierStatus over %s on a seal-disabled node", tt.name)
			requireProtectionNone(t, st)
		})
	}
}

// TestStandaloneNode_SealDisabledEmbeddedBarrier covers the in-process
// transport, and the verbs that would change a barrier that guards nothing:
// each is refused, and the barrier stays unsealed.
func TestStandaloneNode_SealDisabledEmbeddedBarrier(t *testing.T) {
	// The embedded caller is not what this test is about; RBAC is off so it
	// reaches the barrier service.
	n, _ := newBearerNode(t, func(_ *testing.T, cfg *qrdbconfig.Config) {
		cfg.Seal.Enabled = false
		cfg.Auth.Enabled = qrdbconfig.BoolPtr(false)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	st, err := n.Client().Barrier().BarrierStatus(ctx)
	require.NoError(t, err)
	requireProtectionNone(t, st)

	require.Error(t, n.Client().Barrier().BarrierSeal(ctx), "a barrier that guards nothing must not report a seal")
	st, err = n.Client().Barrier().BarrierStatus(ctx)
	require.NoError(t, err)
	requireProtectionNone(t, st)
}

// TestClusteredNode_SealDisabledServesBarrierStatus is the clustered builder's
// half of go-qrdb#1285.
func TestClusteredNode_SealDisabledServesBarrierStatus(t *testing.T) {
	ctx := context.Background()
	cfg := newRaftIsolationConfig(t)
	cfg.Auth.Enabled = qrdbconfig.BoolPtr(false)
	cfg.Seal.Enabled = false
	n, err := NewClusteredNode(ctx, cfg, raftMembers(), nil, WithNoSyncWrites())
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	awaitWritable(ctx, t, n)

	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	st, err := n.Client().Barrier().BarrierStatus(callCtx)
	require.NoError(t, err)
	requireProtectionNone(t, st)
}
