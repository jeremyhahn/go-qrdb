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

package node

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// testhelpers_test.go holds test helpers shared across build-tag boundaries
// inside this package.
//
// It carries NO build constraint on purpose. sealedStandaloneConfig used to
// live in node_test.go and newSharedTenantNode in dao_tenant_index_test.go,
// both `!integration`, while sealedwindow_test.go and zz_iam_db_test.go consume
// them under no constraint at all -- so under BUILD_TAGS+integration the helpers
// vanished and this package stopped typechecking (#203; the same shape #178
// fixed in pkg/statemachine). `make vet-tags` did not see it either, because it
// swept only the root module until #221. A test helper's constraint must be a
// SUPERSET of every caller's; the union of these callers is unconstrained, so
// this file is too.
//
// newTenantIsolationConfig comes along because newSharedTenantNode calls it:
// hoisting a helper that still reaches into a constrained one would just move
// the same defect one call deep.

// sealedStandaloneConfig returns a config with seal enabled, passphrase
// strategy, and the supplied dataDir as the pebble backend root.
func sealedStandaloneConfig(t *testing.T, dataDir string) *qrdbconfig.Config {
	t.Helper()
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = dataDir
	cfg.Databases = []qrdbconfig.DatabaseConfig{
		{Name: "default", Backend: string(database.BackendPebble)},
	}
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "passphrase"
	cfg.Seal.RootKeyPath = "root-key"
	cfg.Seal.CredentialStrategy = "barrier"
	// Disable all network-bound protocol servers so Start() doesn't need ports.
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false
	return cfg
}

// newSharedTenantNode boots a standalone node and creates two SHARED-PREFIX
// tenants, which is the default isolation and the consumer's primary path:
// per the tenancy architecture a tenant is not isolated unless it asks to be,
// and a prefix tenant lives in the system store separated by keyspace.
func newSharedTenantNode(ctx context.Context, t *testing.T) *Node {
	t.Helper()
	cfg := newTenantIsolationConfig(t)
	n, err := NewStandaloneNode(ctx, cfg, nil, WithTenantIsolation())
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	require.False(t, n.IsSealed(), "none-strategy node must boot unsealed")

	for _, id := range []string{"acme", "globex"} {
		_, err := n.Services().Tenant.CreateTenant(ctx, id, id, tenant.IsolationPrefix)
		require.NoErrorf(t, err, "CreateTenant(%s, IsolationPrefix)", id)
	}
	return n
}

// newTenantIsolationConfig builds a minimal standalone-node Config with a
// none-strategy system barrier, a single-path volume pool, and pebble storage.
// All protocol listeners are disabled so the node binds no ports.
func newTenantIsolationConfig(t *testing.T) *qrdbconfig.Config {
	t.Helper()
	dataDir := t.TempDir()
	poolDir := t.TempDir()
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = dataDir
	cfg.Databases = []qrdbconfig.DatabaseConfig{
		{Name: "default", Backend: string(database.BackendPebble)},
	}
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "none"
	cfg.Storage.VolumePools = []qrdbconfig.VolumePoolConfig{
		{Name: "default", Paths: []string{poolDir}},
	}
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false
	return cfg
}

// volumeEntitlements returns the entitlement set that permits creating
// IsolationVolume tenants with system-managed auto-unseal barriers. These are
// used only in tests; production operators configure entitlements via the
// operator config.
func volumeEntitlements() *quota.Entitlements {
	return &quota.Entitlements{
		AllowedIsolationModes:     []string{quota.IsolationModeVolume},
		AllowVolumeStorage:        true,
		AllowSystemManagedBarrier: true,
	}
}
