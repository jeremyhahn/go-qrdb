// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

//go:build !integration

package node

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// TestNode_EnableEncryption_ReencryptsMaterializedDatabases: a system
// database the binding factory materializes in a store of its own (a file
// collection store and a Pebble store) is encrypted by the node barrier, so
// EnableEncryption re-encrypts its root with the node's base store. After a
// restart under the new barrier both databases open and read back what was
// written before the migration.
func TestNode_EnableEncryption_ReencryptsMaterializedDatabases(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	const passphrase = "materialized-enable-enc-pass"
	newConfig := func() *qrdbconfig.Config {
		cfg := qrdbconfig.DefaultConfig()
		cfg.Auth.Enabled = qrdbconfig.BoolPtr(false) // not an authorization test; see testhelpers_test.go
		cfg.Raft.DataDir = dataDir
		cfg.Seal.Enabled = true
		cfg.Seal.Strategy = "none"
		cfg.Protocols.REST = false
		cfg.Protocols.GRPC = false
		cfg.Protocols.QUIC = false
		cfg.Protocols.Unix = false
		cfg.Protocols.MCP = false
		return cfg
	}
	databases := map[string]database.BackendType{
		"docs":   database.BackendFile,
		"events": database.BackendPebble,
	}

	n, err := NewStandaloneNode(ctx, newConfig(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	sysCtx := tenant.WithTenantID(ctx, tenantid.System)
	for name, backend := range databases {
		_, err := n.Services().Database.CreateForTenant(sysCtx, database.DatabaseConfig{Name: name, Backend: backend})
		require.NoErrorf(t, err, "create %s", name)
		require.NoErrorf(t, materializedTarget(t, ctx, n, name).put(ctx, "k", []byte(name+"-value")), "put into %s", name)
	}

	require.NoError(t, n.EnableEncryption(ctx, service.BarrierScope{Kind: service.ScopeSystem}, seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase,
		Mechanism:      seal.MechanismSwap,
		Availability:   seal.AvailabilityQuiesced,
		Credentials:    seal.MustCredentials(ctx, []byte(passphrase)),
	}))
	require.NoError(t, n.Close())

	n2, err := NewStandaloneNode(ctx, newConfig(), nil, WithSealPassphrase([]byte(passphrase)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = n2.Close() })
	require.False(t, n2.IsSealed())
	for name := range databases {
		got, err := materializedTarget(t, ctx, n2, name).mvcc.Get(ctx, "k")
		require.NoErrorf(t, err, "read %s after the migration and restart", name)
		require.Equalf(t, name+"-value", string(got), "database %s", name)
	}
}
