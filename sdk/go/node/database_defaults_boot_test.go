//go:build !integration

package node

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// defaultsBootPaths are the four ways the SDK boots a node: standalone and
// clustered, each with the seal enabled (the manager is built by the node)
// and disabled. Each returns a booted node whose config carries d.
var defaultsBootPaths = []struct {
	name      string
	clustered bool
	boot      func(t *testing.T, d qrdbconfig.DatabaseDefaults) *Node
}{
	{"standalone sealed", false, func(t *testing.T, d qrdbconfig.DatabaseDefaults) *Node {
		return bootStandaloneWithDefaults(t, d, true)
	}},
	{"standalone unsealed", false, func(t *testing.T, d qrdbconfig.DatabaseDefaults) *Node {
		return bootStandaloneWithDefaults(t, d, false)
	}},
	{"clustered sealed", true, func(t *testing.T, d qrdbconfig.DatabaseDefaults) *Node {
		return bootClusteredWithDefaults(t, d, true)
	}},
	{"clustered unsealed", true, func(t *testing.T, d qrdbconfig.DatabaseDefaults) *Node {
		return bootClusteredWithDefaults(t, d, false)
	}},
}

func bootStandaloneWithDefaults(t *testing.T, d qrdbconfig.DatabaseDefaults, sealed bool) *Node {
	t.Helper()
	cfg := qrdbconfig.DefaultConfig()
	cfg.Auth.Enabled = qrdbconfig.BoolPtr(false) // not an authorization test
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendPebble)}}
	cfg.DatabaseDefaults = d
	cfg.Seal.Enabled = sealed
	if sealed {
		cfg.Seal.Strategy = "none"
	}
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false
	n, err := NewStandaloneNode(context.Background(), cfg, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	return n
}

func bootClusteredWithDefaults(t *testing.T, d qrdbconfig.DatabaseDefaults, sealed bool) *Node {
	t.Helper()
	cfg := newRaftIsolationConfig(t)
	cfg.Auth.Enabled = qrdbconfig.BoolPtr(false) // not an authorization test
	cfg.DatabaseDefaults = d
	cfg.Seal.Enabled = sealed
	n, err := NewClusteredNode(context.Background(), cfg, raftMembers(), nil, WithNoSyncWrites())
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	return n
}

// TestBootPaths_DatabaseDefaultsResolveAnOmittedBackend pins that every SDK
// boot path hands the node's database_defaults to the database manager, under
// the node's actual run mode, as the daemon does: a create naming no backend
// records the configured default backend.
func TestBootPaths_DatabaseDefaultsResolveAnOmittedBackend(t *testing.T) {
	for _, p := range defaultsBootPaths {
		t.Run(p.name, func(t *testing.T) {
			n := p.boot(t, qrdbconfig.DatabaseDefaults{Backend: string(database.BackendFile)})
			svc := n.svcNode.Load()
			require.NotNil(t, svc)
			info, err := svc.Database.Create(database.DatabaseConfig{Name: "dflt"})
			require.NoError(t, err)
			require.Equal(t, database.BackendFile, info.Backend,
				"a create with no backend records database_defaults.backend")
		})
	}
}

// TestBootPaths_RunModeResolvesAnOmittedBackend pins that, with no
// database_defaults, each boot path records the backend of its actual run
// mode: raft on a clustered node, pebble on a standalone one.
func TestBootPaths_RunModeResolvesAnOmittedBackend(t *testing.T) {
	for _, p := range defaultsBootPaths {
		t.Run(p.name, func(t *testing.T) {
			n := p.boot(t, qrdbconfig.DatabaseDefaults{})
			svc := n.svcNode.Load()
			require.NotNil(t, svc)
			info, err := svc.Database.Create(database.DatabaseConfig{Name: "dflt"})
			require.NoError(t, err)
			want := database.BackendPebble
			if p.clustered {
				want = database.BackendRaft
			}
			require.Equal(t, want, info.Backend, "a create with no backend records the run mode's backend")
		})
	}
}

// TestBootPaths_SealDisabledClusteredProvisioningResolvesDefaults pins that
// the seal-disabled clustered boot hands both provisioning authorization
// wrappers (embedded and network) the manager its database service uses: a
// caller holding databases:create but not databases:create_raft that omits
// its backend under a raft database_defaults is refused, on the embedded
// transport and over REST, and nothing reaches the catalog.
func TestBootPaths_SealDisabledClusteredProvisioningResolvesDefaults(t *testing.T) {
	certDir := t.TempDir()
	ca, err := qrdbdharness.NewCA(certDir)
	require.NoError(t, err)
	serverKP, err := ca.Issue(certDir, qrdbdharness.WithSubject("node", ""), qrdbdharness.WithServerAuth())
	require.NoError(t, err)
	serverCert, err := serverKP.TLSCertificate()
	require.NoError(t, err)
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    ca.Pool,
		ClientAuth:   tls.VerifyClientCertIfGiven,
		MinVersion:   tls.VersionTLS13,
	}

	cfg := newRaftIsolationConfig(t)
	cfg.Auth.Enabled = qrdbconfig.BoolPtr(true)
	cfg.Seal.Enabled = false
	cfg.DatabaseDefaults = qrdbconfig.DatabaseDefaults{Backend: string(database.BackendRaft)}
	cfg.Server.Host = "127.0.0.1"
	cfg.Protocols.REST = true
	cfg.Server.AdminPort = freeTCPPort(t)
	ctx := context.Background()
	n, err := NewClusteredNode(ctx, cfg, raftMembers(),
		[]server.ServerOption{server.WithTLS(tlsCfg), server.WithCACertPool(ca.Pool)},
		WithNoSyncWrites(), WithAuthenticators(sdk.ChainBeforeMTLS, hostTokenAuthenticator{}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })

	createRole, err := n.Services().Role.CreateRole(ctx, &role.CreateRoleRequest{
		Name:        "db-create-only",
		TenantID:    tenantid.System,
		Permissions: []permission.Permission{permission.DatabasesCreate},
	})
	require.NoError(t, err)
	_, err = n.Services().User.CreateUser(ctx, &user.CreateUserRequest{
		WithoutEmailLoginKey: true,
		Username:             hostBearerSubject,
		TenantID:             tenantid.System,
		RoleIDs:              []string{createRole.ID},
		AuthMethods:          []string{auth.MethodWebAuthn},
	})
	require.NoError(t, err)

	t.Run("embedded", func(t *testing.T) {
		caller := auth.WithIdentity(ctx, &auth.Identity{
			Subject:  hostBearerSubject,
			TenantID: tenantid.System,
			Roles:    []string{createRole.ID},
		})
		_, err := n.Client().Database().CreateDatabase(caller, transport.CreateDatabaseRequest{Name: "embdflt"})
		var denied *service.ProvisioningDeniedError
		require.Truef(t, errors.As(err, &denied), "create omitting backend under a raft default = %v, want *service.ProvisioningDeniedError", err)
		require.Equal(t, permission.DatabasesCreateRaft, denied.Permission)
		_, getErr := n.dbManager.Get("embdflt")
		require.Error(t, getErr, "a refused create reached the catalog")
	})

	t.Run("rest", func(t *testing.T) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+n.Server().RESTAddr()+"/v1/databases",
			strings.NewReader(`{"name":"restdflt"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", hostBearerToken)
		resp, err := bearerHTTPClient(ca, bearer(hostBearerToken)).Do(req)
		require.NoError(t, err)
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		require.Equalf(t, http.StatusForbidden, resp.StatusCode, "create omitting backend under a raft default: body %s", body)
		_, getErr := n.dbManager.Get("restdflt")
		require.Error(t, getErr, "a refused create reached the catalog")
	})
}
