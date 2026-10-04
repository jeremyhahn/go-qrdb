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
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	pebbleStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// ---------------------------------------------------------------------------
// NewStandaloneNode
// ---------------------------------------------------------------------------

func TestNewStandaloneNode_NilConfig(t *testing.T) {
	_, err := NewStandaloneNode(context.Background(), nil, nil)
	var cfgErr *ConfigError
	require.ErrorAs(t, err, &cfgErr)
	assert.Equal(t, "cfg", cfgErr.Field)
}

func TestNewStandaloneNode_MemoryBackend(t *testing.T) {
	cfg := qrdbconfig.DefaultConfig()
	// In-memory primary storage, but vector storage still requires a disk
	// directory (PebbleDB-only subsystem). Use t.TempDir() so no artifacts
	// escape the test sandbox.
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	// Disable all protocol servers so Start() doesn't try to bind ports.
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false

	n, err := NewStandaloneNode(context.Background(), cfg, nil)
	require.NoError(t, err)
	require.NotNil(t, n)

	assert.NotNil(t, n.Server(), "Server must be non-nil after successful construction")
	assert.NotNil(t, n.Client(), "Client must be non-nil after successful construction")
	assert.NotNil(t, n.Services(), "Services must be non-nil after successful construction")
	assert.NotNil(t, n.KVService(), "KVService must be non-nil")
	assert.NotNil(t, n.AdminService(), "AdminService must be non-nil")

	require.NoError(t, n.Close())
}

func TestNewStandaloneNode_MemoryBackend_WithServerOption(t *testing.T) {
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false

	// Verify a server option (e.g. WithHost) is accepted and doesn't break construction.
	// We pass a nil host option as a no-op; server.WithHost accepts nil safely.
	serverOpts := []server.ServerOption{
		server.WithHost(nil),
	}

	n, err := NewStandaloneNode(context.Background(), cfg, serverOpts)
	require.NoError(t, err)
	require.NotNil(t, n)
	require.NoError(t, n.Close())
}

func TestNewStandaloneNode_ObjstoreBackend(t *testing.T) {
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	// Objstore is a persistent backend (KVEngine, like file). Use the local
	// provider rooted under the node data dir by default.
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendObjstore)}}
	cfg.Storage.Objstore = qrdbconfig.ObjstoreConfig{Provider: "local"}
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false

	n, err := NewStandaloneNode(context.Background(), cfg, nil)
	require.NoError(t, err)
	require.NotNil(t, n)
	assert.NotNil(t, n.Client(), "objstore-backed node must expose a Client")
	assert.NotNil(t, n.KVService(), "objstore-backed node must expose a KV service")
	require.NoError(t, n.Close())
}

func TestNewStandaloneNode_DoesNotConstructSMCreateFunc(t *testing.T) {
	// Standalone mode must NOT call host.StartShard. Verifying this
	// indirectly: if the node builds with no host and no state machine
	// factory, construction must succeed for a memory backend.
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false

	n, err := NewStandaloneNode(context.Background(), cfg, nil)
	require.NoError(t, err)
	// host is nil for standalone — no quicraft.Host created.
	assert.Nil(t, n.host, "standalone node must have nil host (no Raft)")
	require.NoError(t, n.Close())
}

func TestNewStandaloneNode_CloseIsIdempotentAfterPartialBuild(t *testing.T) {
	// Even if construction fails, Close on a partially-built Node must not panic.
	n := &Node{}
	assert.NoError(t, n.Close())
}

// ---------------------------------------------------------------------------
// NewClusteredNode
// ---------------------------------------------------------------------------

func TestNewClusteredNode_NilConfig(t *testing.T) {
	_, err := NewClusteredNode(context.Background(), nil, map[uint64]string{1: "127.0.0.1:63001"}, nil)
	var cfgErr *ConfigError
	require.ErrorAs(t, err, &cfgErr)
}

func TestNewClusteredNode_EmptyMembers(t *testing.T) {
	cfg := qrdbconfig.DefaultConfig()
	_, err := NewClusteredNode(context.Background(), cfg, nil, nil)
	var cfgErr *ConfigError
	require.ErrorAs(t, err, &cfgErr)
	assert.Equal(t, "members", cfgErr.Field)
}

// ---------------------------------------------------------------------------
// clusteredCreateFunc — Raft state-machine selection
// ---------------------------------------------------------------------------

func TestClusteredCreateFunc_DefaultPebble(t *testing.T) {
	cfg := qrdbconfig.DefaultConfig() // SMType defaults to "pebble"
	fn, err := clusteredCreateFunc(cfg, nil, nil, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, fn)
}

func TestClusteredCreateFunc_EmptySMTypeDefaultsPebble(t *testing.T) {
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.SMType = ""
	fn, err := clusteredCreateFunc(cfg, nil, nil, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, fn)
}

func TestClusteredCreateFunc_MemoryUnsealed(t *testing.T) {
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.SMType = "memory"
	fn, err := clusteredCreateFunc(cfg, nil, nil, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, fn)
}

// TestClusteredCreateFunc_UnderTheSealBarrier pins what a clustered node may
// run while the seal barrier is enabled — the same rule the daemon applies:
// state machines that persist nothing (memory-class, or disk/fast over memory
// storage) and disk/fast over pebble storage, which is opened on the
// barrier's encfs, build; disk/fast over file storage, which would sit on disk
// in plaintext, is refused as a raft.sm_storage ConfigError.
func TestClusteredCreateFunc_UnderTheSealBarrier(t *testing.T) {
	sealed := atrest.New(sealtest.UnsealedBarrier(t))
	for _, tc := range []struct {
		smType, storage string
		refused         bool
	}{
		{"memory", "", false},
		{"highperf", "", false},
		{"disk", "pebble", false},
		{"fast", "memory", false},
		{"disk", "file", true},
		{"fast", "file", true},
	} {
		cfg := qrdbconfig.DefaultConfig()
		cfg.Raft.DataDir = t.TempDir()
		cfg.Raft.SMType = tc.smType
		cfg.Raft.SMStorage = tc.storage
		_, err := clusteredCreateFunc(cfg, nil, nil, sealed, nil)
		if !tc.refused {
			require.NoError(t, err, "%s over %q under the barrier", tc.smType, tc.storage)
			continue
		}
		var cfgErr *ConfigError
		require.ErrorAs(t, err, &cfgErr, "%s over %q under the barrier", tc.smType, tc.storage)
		assert.Equal(t, "raft.sm_storage", cfgErr.Field)
	}
}

func TestClusteredCreateFunc_UnknownSMTypeErrors(t *testing.T) {
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.SMType = "does-not-exist"
	_, err := clusteredCreateFunc(cfg, nil, nil, nil, nil)
	require.Error(t, err)
	var cfgErr *ConfigError
	require.ErrorAs(t, err, &cfgErr)
	assert.Equal(t, "raft.sm_type", cfgErr.Field)
}

// TestClusteredCreateFunc_HonoursSMStorage pins go-qrdb#136's plumbing: a
// storage-generic sm_type composes the storage raft.sm_storage selects, in the
// per-replica directory under raft.data_dir — as the daemon does — rather than
// the SDK ignoring the setting.
func TestClusteredCreateFunc_HonoursSMStorage(t *testing.T) {
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Raft.SMType = "disk"
	cfg.Raft.SMStorage = "file"
	fn, err := clusteredCreateFunc(cfg, nil, nil, nil, nil)
	require.NoError(t, err)

	machine := fn(3, 4)
	disk, ok := machine.(interface {
		Open(context.Context, string, <-chan struct{}) (uint64, error)
		Close(context.Context) error
	})
	require.True(t, ok, "sm_type disk must build a disk state machine, got %T", machine)
	t.Cleanup(func() { _ = disk.Close(context.Background()) })

	info, err := os.Stat(filepath.Join(cfg.Raft.DataDir, "sm", "shard-3-replica-4"))
	require.NoError(t, err, "sm_storage file must root the replica under raft.data_dir")
	assert.True(t, info.IsDir())
}

// TestClusteredCreateFunc_RefusesInapplicableSMStorage: a non-pebble
// sm_storage for a state machine that carries its own storage is refused,
// never silently ignored.
func TestClusteredCreateFunc_RefusesInapplicableSMStorage(t *testing.T) {
	for _, smType := range []string{"", "mvcc", "memory", "highperf"} {
		cfg := qrdbconfig.DefaultConfig()
		cfg.Raft.DataDir = t.TempDir()
		cfg.Raft.SMType = smType
		cfg.Raft.SMStorage = "file"
		_, err := clusteredCreateFunc(cfg, nil, nil, nil, nil)
		var cfgErr *ConfigError
		require.ErrorAs(t, err, &cfgErr, "sm_type %q over sm_storage file", smType)
	}
}

// TestClusteredProfile_ReportsTheRealSMType pins the capability report: a node
// on a registry state machine reports transactions and batches, and not the
// watch / lease / MVCC / time-series families only the default MVCC store
// serves; the default reports those.
func TestClusteredProfile_ReportsTheRealSMType(t *testing.T) {
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.SMType = "memory"
	p, err := clusteredProfile(cfg)
	require.NoError(t, err)
	assert.True(t, p.KV && p.Batch && p.Transaction && p.Lease, "memory serves KV, batches, transactions and leases")
	assert.False(t, p.MVCC || p.Watch || p.Lock || p.TimeSeries,
		"memory must not report families it does not serve: %+v", p)

	cfg.Raft.SMType = ""
	p, err = clusteredProfile(cfg)
	require.NoError(t, err)
	assert.True(t, p.MVCC && p.Watch && p.Lease && p.Transaction, "the default MVCC store serves them: %+v", p)
}

// ---------------------------------------------------------------------------
// resolveBackend
// ---------------------------------------------------------------------------

func TestResolveBackend_EmptyDataDir_Errors(t *testing.T) {
	// No data dir and no explicit backend defaults to PebbleDB, which requires
	// a data directory — this is a misconfiguration, NOT a silent fallback to
	// an in-memory store.
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = ""
	cfg.Databases = nil
	_, _, err := resolveBackend(cfg)
	require.Error(t, err)
	var cfgErr *ConfigError
	require.ErrorAs(t, err, &cfgErr)
	assert.Equal(t, "raft.data_dir", cfgErr.Field)
}

func TestResolveBackend_EmptyDataDir_ExplicitMemory(t *testing.T) {
	// An in-memory store is permitted with no data dir only when explicitly
	// requested.
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = ""
	cfg.Databases = []qrdbconfig.DatabaseConfig{
		{Name: "default", Backend: string(database.BackendMemory)},
	}
	bt, dir, err := resolveBackend(cfg)
	require.NoError(t, err)
	assert.Equal(t, database.BackendMemory, bt)
	assert.Equal(t, "", dir)
}

func TestResolveBackend_PebbleDefault(t *testing.T) {
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = "/tmp/test-data"
	cfg.Databases = nil
	bt, dir, err := resolveBackend(cfg)
	require.NoError(t, err)
	assert.Equal(t, database.BackendPebble, bt)
	assert.Equal(t, "/tmp/test-data", dir)
}

func TestResolveBackend_DatabaseConfigOverride(t *testing.T) {
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = "/tmp/test-data"
	cfg.Databases = []qrdbconfig.DatabaseConfig{
		{Name: "default", Backend: string(database.BackendMemory)},
	}
	bt, dir, err := resolveBackend(cfg)
	require.NoError(t, err)
	assert.Equal(t, database.BackendMemory, bt)
	assert.Equal(t, "/tmp/test-data", dir)
}

// ---------------------------------------------------------------------------
// buildNodeServerConfig
// ---------------------------------------------------------------------------

func TestBuildNodeServerConfig_Defaults(t *testing.T) {
	cfg := &qrdbconfig.Config{}
	out := buildNodeServerConfig(cfg)
	assert.Equal(t, 63001, out.Server.AdminPort)
	assert.Equal(t, 63002, out.Server.GRPCPort)
	assert.Equal(t, "127.0.0.1", out.Server.Host)
}

func TestBuildNodeServerConfig_PreservesExistingPorts(t *testing.T) {
	cfg := &qrdbconfig.Config{}
	cfg.Server.AdminPort = 9000
	cfg.Server.GRPCPort = 9001
	cfg.Server.Host = "0.0.0.0"
	out := buildNodeServerConfig(cfg)
	assert.Equal(t, 9000, out.Server.AdminPort)
	assert.Equal(t, 9001, out.Server.GRPCPort)
	assert.Equal(t, "0.0.0.0", out.Server.Host)
}

// ---------------------------------------------------------------------------
// Node.Close
// ---------------------------------------------------------------------------

func TestNode_Close_NilFields(t *testing.T) {
	n := &Node{}
	assert.NoError(t, n.Close(), "Close on empty Node must not error")
}

func TestNode_Close_NilSvcNode(t *testing.T) {
	// The data-plane handles are published through atomic.Pointer, whose zero
	// value already reads nil, so an un-booted Node needs no explicit nils.
	n := &Node{}
	assert.Nil(t, n.Services())
	assert.Nil(t, n.Client())
	assert.Nil(t, n.Server())
	assert.NoError(t, n.Close())
}

// ---------------------------------------------------------------------------
// NodeOption
// ---------------------------------------------------------------------------

func TestWithSealPassphrase(t *testing.T) {
	o := &nodeOptions{}
	pass := []byte("secret")
	WithSealPassphrase(pass)(o)
	assert.Equal(t, pass, o.sealPassphrase)
}

func TestWithLogger(t *testing.T) {
	o := &nodeOptions{}
	// nil logger is accepted without panic (node construction uses slog.Default fallback)
	WithLogger(nil)(o)
	assert.Nil(t, o.logger)
}

func TestWithRaftMTLS_NilIsAccepted(t *testing.T) {
	o := &nodeOptions{}
	WithRaftMTLS(nil)(o)
	assert.Nil(t, o.raftMTLS)
}

func TestWithRaftMTLS_SetsConfig(t *testing.T) {
	mtls := &quicraftconfig.MTLSConfig{
		CACert: []byte("ca-pem"),
		Cert:   []byte("cert-pem"),
		Key:    []byte("key-pem"),
	}
	o := &nodeOptions{}
	WithRaftMTLS(mtls)(o)
	require.NotNil(t, o.raftMTLS)
	assert.Equal(t, []byte("ca-pem"), o.raftMTLS.CACert)
	assert.Equal(t, []byte("cert-pem"), o.raftMTLS.Cert)
	assert.Equal(t, []byte("key-pem"), o.raftMTLS.Key)
}

// ---------------------------------------------------------------------------
// IsSealed / Unseal — passphrase + software strategy
// ---------------------------------------------------------------------------

// TestNewStandaloneNode_SealEnabled_WithPassphrase verifies that a standalone
// node configured with passphrase sealing and WithSealPassphrase boots fully
// (IsSealed() == false, Server/Client non-nil).
func TestNewStandaloneNode_SealEnabled_WithPassphrase(t *testing.T) {
	dataDir := t.TempDir()
	cfg := sealedStandaloneConfig(t, dataDir)

	n, err := NewStandaloneNode(
		context.Background(), cfg, nil,
		WithSealPassphrase([]byte("test-secret")),
	)
	require.NoError(t, err)
	require.NotNil(t, n)

	assert.False(t, n.IsSealed(), "node must be unsealed after WithSealPassphrase")
	assert.NotNil(t, n.Server(), "server must be non-nil after full boot")
	assert.NotNil(t, n.Client(), "client must be non-nil after full boot")
	assert.NotNil(t, n.Services(), "services must be non-nil after full boot")

	require.NoError(t, n.Close())
}

// TestNewStandaloneNode_SealEnabled_NoPassphrase_ReturnsSealedNode verifies
// that a node with passphrase sealing but no WithSealPassphrase is returned
// in the sealed state: IsSealed() == true, Server/Client nil, but
// Services().Barrier is non-nil.
func TestNewStandaloneNode_SealEnabled_NoPassphrase_ReturnsSealedNode(t *testing.T) {
	dataDir := t.TempDir()
	cfg := sealedStandaloneConfig(t, dataDir)

	n, err := NewStandaloneNode(context.Background(), cfg, nil)
	require.NoError(t, err)
	require.NotNil(t, n)

	assert.True(t, n.IsSealed(), "node must be in sealed state when no passphrase is supplied")
	assert.Nil(t, n.Server(), "server must be nil in sealed state")
	assert.Nil(t, n.Client(), "client must be nil in sealed state")
	require.NotNil(t, n.Services(), "services must not be nil even in sealed state")
	assert.NotNil(t, n.Services().Barrier, "barrier service must be reachable in sealed state")
	assert.NotNil(t, n.Services().Ceremony, "ceremony service must be reachable in sealed state")

	require.NoError(t, n.Close())
}

// TestNode_Unseal_CompletesBootAndServesRequests verifies that calling
// Unseal on a sealed node unseals the barrier and completes BuildServices +
// server wiring so the node becomes fully operational.
func TestNode_Unseal_CompletesBootAndServesRequests(t *testing.T) {
	dataDir := t.TempDir()
	cfg := sealedStandaloneConfig(t, dataDir)

	// First boot: supply passphrase to initialize + unseal the barrier.
	n1, err := NewStandaloneNode(
		context.Background(), cfg, nil,
		WithSealPassphrase([]byte("my-passphrase")),
	)
	require.NoError(t, err)
	require.NotNil(t, n1)
	require.False(t, n1.IsSealed())
	require.NoError(t, n1.Close())

	// Second boot: no passphrase → sealed node.
	n2, err := NewStandaloneNode(context.Background(), cfg, nil)
	require.NoError(t, err)
	require.NotNil(t, n2)
	require.True(t, n2.IsSealed())
	assert.Nil(t, n2.Server())
	assert.Nil(t, n2.Client())

	// Unseal with the correct passphrase → full boot.
	creds := seal.MustCredentials([]byte("my-passphrase"))
	require.NoError(t, n2.Unseal(context.Background(), creds))

	assert.False(t, n2.IsSealed(), "must be unsealed after Unseal()")
	assert.NotNil(t, n2.Server(), "server must be wired after Unseal()")
	assert.NotNil(t, n2.Client(), "client must be wired after Unseal()")
	assert.NotNil(t, n2.KVService(), "KVService must be non-nil after Unseal()")

	require.NoError(t, n2.Close())
}

// TestNode_Unseal_WrongPassphrase_ReturnsError verifies that Unseal with an
// incorrect passphrase returns a typed UnsealError and leaves the node sealed.
func TestNode_Unseal_WrongPassphrase_ReturnsError(t *testing.T) {
	dataDir := t.TempDir()
	cfg := sealedStandaloneConfig(t, dataDir)

	// Initialize the barrier on first boot.
	n1, err := NewStandaloneNode(
		context.Background(), cfg, nil,
		WithSealPassphrase([]byte("correct-passphrase")),
	)
	require.NoError(t, err)
	require.NoError(t, n1.Close())

	// Second boot: sealed node.
	n2, err := NewStandaloneNode(context.Background(), cfg, nil)
	require.NoError(t, err)
	require.True(t, n2.IsSealed())

	// Wrong passphrase must return an UnsealError.
	err = n2.Unseal(context.Background(), seal.MustCredentials([]byte("wrong-passphrase")))
	var unsealErr *UnsealError
	require.True(t, errors.As(err, &unsealErr), "expected UnsealError, got %T: %v", err, err)
	assert.True(t, n2.IsSealed(), "node must remain sealed after failed Unseal")

	require.NoError(t, n2.Close())
}

// TestNode_Unseal_Idempotent verifies that calling Unseal on a node that is
// already unsealed is a no-op and returns nil.
func TestNode_Unseal_Idempotent(t *testing.T) {
	dataDir := t.TempDir()
	cfg := sealedStandaloneConfig(t, dataDir)

	n, err := NewStandaloneNode(
		context.Background(), cfg, nil,
		WithSealPassphrase([]byte("idempotent-test")),
	)
	require.NoError(t, err)
	require.False(t, n.IsSealed())

	// Second Unseal on an already-unsealed node must not error.
	assert.NoError(t, n.Unseal(context.Background(), seal.MustCredentials([]byte("idempotent-test"))))
	assert.False(t, n.IsSealed())
	require.NoError(t, n.Close())
}

// TestNode_Unseal_NoSeal_IsNoop verifies that calling Unseal on a node with
// seal disabled is a no-op.
func TestNode_Unseal_NoSeal_IsNoop(t *testing.T) {
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	// In-memory primary storage, but vector storage still requires a disk
	// directory. t.TempDir() above satisfies that requirement.
	cfg.Databases = []qrdbconfig.DatabaseConfig{
		{Name: "default", Backend: string(database.BackendMemory)},
	}
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false

	n, err := NewStandaloneNode(context.Background(), cfg, nil)
	require.NoError(t, err)
	require.False(t, n.IsSealed())
	assert.NoError(t, n.Unseal(context.Background(), seal.Credentials{}))
	assert.False(t, n.IsSealed())
	require.NoError(t, n.Close())
}

// TestIsSealed_NilBarrier verifies IsSealed returns false when no barrier is set.
func TestIsSealed_NilBarrier(t *testing.T) {
	n := &Node{}
	assert.False(t, n.IsSealed())
}

// ---------------------------------------------------------------------------
// BarrierRegistry
// ---------------------------------------------------------------------------

// TestBarrierRegistry_NilWhenSealDisabled verifies that a node built without
// cfg.Seal.Enabled exposes a nil BarrierRegistry (no seal subsystem was built).
func TestBarrierRegistry_NilWhenSealDisabled(t *testing.T) {
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{
		{Name: "default", Backend: string(database.BackendMemory)},
	}
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false

	n, err := NewStandaloneNode(context.Background(), cfg, nil)
	require.NoError(t, err)
	require.NotNil(t, n)

	assert.Nil(t, n.BarrierRegistry(),
		"BarrierRegistry must be nil when seal is disabled")

	require.NoError(t, n.Close())
}

// TestBarrierRegistry_NonNilAfterSealEnabled verifies that a seal-enabled node
// exposes a non-nil BarrierRegistry once fully booted. The registry is built
// during the seal subsystem phase (before BuildServices) and is the object
// embedders use to construct tenant-scoped credential resolvers via the
// sdk/go/secret subpackage's NewTenantScopedCredentialRetrieve.
func TestBarrierRegistry_NonNilAfterSealEnabled(t *testing.T) {
	dataDir := t.TempDir()
	cfg := sealedStandaloneConfig(t, dataDir)

	n, err := NewStandaloneNode(
		context.Background(), cfg, nil,
		WithSealPassphrase([]byte("registry-test-secret")),
	)
	require.NoError(t, err)
	require.NotNil(t, n)
	require.False(t, n.IsSealed(), "node must be unsealed after WithSealPassphrase")

	assert.NotNil(t, n.BarrierRegistry(),
		"BarrierRegistry must be non-nil for a seal-enabled, unsealed node")

	require.NoError(t, n.Close())
}

// ---------------------------------------------------------------------------
// attemptAutoUnseal
// ---------------------------------------------------------------------------

// newTestBarrier creates a minimal sealed StorageBarrier backed by an
// in-memory PebbleDB instance in a temp directory for use in unit tests.
func newTestBarrier(t *testing.T) (*seal.StorageBarrier, func()) {
	t.Helper()
	dir := t.TempDir()
	store, err := pebbleStorage.New(pebbleStorage.DefaultConfig(dir))
	require.NoError(t, err)

	backend := seal.NewStorageAdapter(store)
	barrier, err := seal.NewStorageBarrier(
		slog.Default(), backend,
		seal.BarrierConfig{
			RootKeyPath:     "root-key",
			PreferenceOrder: []seal.StrategyID{seal.StrategyPassphrase},
		},
		seal.NewDeferredSoftwareStrategy(),
	)
	require.NoError(t, err)
	return barrier, func() { _ = store.Close() }
}

// TestAttemptAutoUnseal_StrategyNone verifies that StrategyNone always
// auto-unseals the barrier (no credentials required).
func TestAttemptAutoUnseal_StrategyNone(t *testing.T) {
	dir := t.TempDir()
	store, err := pebbleStorage.New(pebbleStorage.DefaultConfig(dir))
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	backend := seal.NewStorageAdapter(store)
	barrier, err := seal.NewStorageBarrier(
		slog.Default(), backend,
		seal.BarrierConfig{
			RootKeyPath:     "root-key",
			PreferenceOrder: []seal.StrategyID{seal.StrategyNone},
		},
		seal.NewNoneStrategy(),
	)
	require.NoError(t, err)

	sealCfg := qrdbconfig.SealConfig{Strategy: string(seal.StrategyNone)}
	result := attemptAutoUnseal(context.Background(), barrier, sealCfg, nil, slog.Default())
	assert.True(t, result, "StrategyNone must auto-unseal")
	assert.False(t, barrier.IsSealed())
}

// TestAttemptAutoUnseal_PassphraseWithCreds verifies that the passphrase
// strategy auto-unseals when a passphrase is supplied.
func TestAttemptAutoUnseal_PassphraseWithCreds(t *testing.T) {
	barrier, cleanup := newTestBarrier(t)
	defer cleanup()

	sealCfg := qrdbconfig.SealConfig{Strategy: string(seal.StrategyPassphrase)}
	result := attemptAutoUnseal(context.Background(), barrier, sealCfg, []byte("secret"), slog.Default())
	assert.True(t, result, "passphrase strategy with creds must auto-unseal")
	assert.False(t, barrier.IsSealed())
}

// TestAttemptAutoUnseal_PassphraseWithoutCreds verifies that the passphrase
// strategy does NOT auto-unseal when no passphrase is supplied.
func TestAttemptAutoUnseal_PassphraseWithoutCreds(t *testing.T) {
	barrier, cleanup := newTestBarrier(t)
	defer cleanup()

	sealCfg := qrdbconfig.SealConfig{Strategy: string(seal.StrategyPassphrase)}
	result := attemptAutoUnseal(context.Background(), barrier, sealCfg, nil, slog.Default())
	assert.False(t, result, "passphrase strategy without creds must not auto-unseal")
	assert.True(t, barrier.IsSealed())
}

// TestAttemptAutoUnseal_ShamirNeverAutoUnseals verifies that the Shamir
// strategy never auto-unseals (always requires operator quorum).
func TestAttemptAutoUnseal_ShamirNeverAutoUnseals(t *testing.T) {
	dir := t.TempDir()
	store, err := pebbleStorage.New(pebbleStorage.DefaultConfig(dir))
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	backend := seal.NewStorageAdapter(store)
	shamirStrat, err := seal.NewShamirStrategy(2, 3)
	require.NoError(t, err)
	barrier, err := seal.NewStorageBarrier(
		slog.Default(), backend,
		seal.BarrierConfig{
			RootKeyPath:     "root-key",
			PreferenceOrder: []seal.StrategyID{seal.StrategyShamir},
			Shamir:          &seal.ShamirConfig{Threshold: 2, TotalShares: 3},
		},
		shamirStrat,
	)
	require.NoError(t, err)

	sealCfg := qrdbconfig.SealConfig{Strategy: string(seal.StrategyShamir)}
	result := attemptAutoUnseal(context.Background(), barrier, sealCfg, nil, slog.Default())
	assert.False(t, result, "Shamir must never auto-unseal")
	assert.True(t, barrier.IsSealed())
}

// TestAttemptAutoUnseal_SubsequentBoot_PassphraseAlreadyInitialized verifies
// that attemptAutoUnseal correctly handles the ErrAlreadyInitialized path
// (subsequent boots after the barrier has been initialized).
func TestAttemptAutoUnseal_SubsequentBoot_PassphraseAlreadyInitialized(t *testing.T) {
	barrier, cleanup := newTestBarrier(t)
	defer cleanup()

	// First call: initializes + unseals.
	sealCfg := qrdbconfig.SealConfig{Strategy: string(seal.StrategyPassphrase)}
	result1 := attemptAutoUnseal(context.Background(), barrier, sealCfg, []byte("secret"), slog.Default())
	require.True(t, result1)
	require.False(t, barrier.IsSealed())

	// Re-seal to simulate a restart.
	_ = barrier.Seal()

	// Second call: already initialized → unseal path.
	result2 := attemptAutoUnseal(context.Background(), barrier, sealCfg, []byte("secret"), slog.Default())
	assert.True(t, result2, "must unseal on subsequent boot")
	assert.False(t, barrier.IsSealed())
}

// ---------------------------------------------------------------------------
// buildNodeSealSubsystem
// ---------------------------------------------------------------------------

// TestBuildNodeSealSubsystem_CreatesBarrierAndRegistry verifies that
// buildNodeSealSubsystem returns a fully populated subsystem struct.
func TestBuildNodeSealSubsystem_CreatesBarrierAndRegistry(t *testing.T) {
	dataDir := t.TempDir()
	cfg := qrdbconfig.DefaultConfig()
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "passphrase"
	cfg.Seal.RootKeyPath = "root-key"
	cfg.Seal.CredentialStrategy = "barrier"

	sub, err := buildNodeSealSubsystem(context.Background(), cfg, dataDir, slog.Default(), nil, false, nil)
	require.NoError(t, err)
	require.NotNil(t, sub)
	assert.NotNil(t, sub.barrier)
	assert.NotNil(t, sub.credCoreSvc)
	assert.NotNil(t, sub.barrierRegistry)
	assert.NotNil(t, sub.sealStore)
	assert.NotEmpty(t, sub.sealDataDir)

	require.NoError(t, sub.close())
}

// TestBuildNodeSealSubsystem_StrategyNone creates a StrategyNone subsystem.
func TestBuildNodeSealSubsystem_StrategyNone(t *testing.T) {
	dataDir := t.TempDir()
	cfg := qrdbconfig.DefaultConfig()
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = string(seal.StrategyNone)
	cfg.Seal.RootKeyPath = "root-key"

	sub, err := buildNodeSealSubsystem(context.Background(), cfg, dataDir, slog.Default(), nil, false, nil)
	require.NoError(t, err)
	require.NotNil(t, sub)
	require.NoError(t, sub.close())
}

// TestBuildNodeSealSubsystem_InvalidShamirConfig verifies that an invalid
// Shamir threshold returns an error (not a panic).
func TestBuildNodeSealSubsystem_InvalidShamirConfig(t *testing.T) {
	dataDir := t.TempDir()
	cfg := qrdbconfig.DefaultConfig()
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "shamir"
	cfg.Seal.ShamirThreshold = 5 // threshold > total is invalid
	cfg.Seal.ShamirTotal = 3

	_, err := buildNodeSealSubsystem(context.Background(), cfg, dataDir, nil, nil, false, nil)
	require.Error(t, err, "invalid Shamir params must return an error")
	var sce *StorageCreateError
	require.True(t, errors.As(err, &sce))
	assert.Equal(t, "seal-shamir", sce.Backend)
}

// ---------------------------------------------------------------------------
// UnsealError
// ---------------------------------------------------------------------------

// TestUnsealError_ErrorString verifies the UnsealError message.
func TestUnsealError_ErrorString(t *testing.T) {
	e := &UnsealError{Err: seal.ErrInvalidCredentials}
	assert.Contains(t, e.Error(), "failed to unseal barrier")
	assert.ErrorIs(t, e, seal.ErrInvalidCredentials)
}

// TestUnsealError_Unwrap verifies Unwrap returns the wrapped error.
func TestUnsealError_Unwrap(t *testing.T) {
	inner := seal.ErrInvalidCredentials
	e := &UnsealError{Err: inner}
	assert.Equal(t, inner, e.Unwrap())
}
