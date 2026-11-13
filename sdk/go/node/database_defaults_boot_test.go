package node

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
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
