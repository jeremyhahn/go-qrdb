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
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestNodeMigrationProvider_StoreDirs_ClassifiesPebbleAndFileStores pins that
// the system migration names each existing store directory by the kind of
// store it holds: pebble and vector as Pebble stores, file as a file store,
// and nothing that is absent or not a directory.
func TestNodeMigrationProvider_StoreDirs_ClassifiesPebbleAndFileStores(t *testing.T) {
	dataDir := t.TempDir()
	for _, d := range []string{"pebble", "file"} {
		require.NoError(t, os.Mkdir(filepath.Join(dataDir, d), 0o700))
	}
	// A regular file where the vector store would be is not a store.
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "vector"), nil, 0o600))

	p := &nodeMigrationProvider{node: &Node{retainedStandalone: &pendingStandaloneBoot{dataDir: dataDir}}}
	got, err := p.StoreDirs(context.Background(), service.BarrierScope{Kind: service.ScopeSystem})
	require.NoError(t, err)
	require.Equal(t, service.MigrationStores{
		Pebble: []string{filepath.Join(dataDir, "pebble")},
		File:   []string{filepath.Join(dataDir, "file")},
	}, got)
}

// TestNodeMigrationProvider_StoreDirs_RefusesWithoutAStandaloneSystemScope
// pins the typed refusals: a non-system scope, and a node that retained no
// standalone boot parameters.
func TestNodeMigrationProvider_StoreDirs_RefusesWithoutAStandaloneSystemScope(t *testing.T) {
	ctx := context.Background()
	retained := &Node{retainedStandalone: &pendingStandaloneBoot{dataDir: t.TempDir()}}
	for name, tc := range map[string]struct {
		node  *Node
		scope service.BarrierScope
	}{
		"tenant scope":     {retained, service.BarrierScope{Kind: service.ScopeTenant}},
		"nothing retained": {&Node{}, service.BarrierScope{Kind: service.ScopeSystem}},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := (&nodeMigrationProvider{node: tc.node}).StoreDirs(ctx, tc.scope)
			var notSupported *MigrationNotSupportedError
			require.ErrorAs(t, err, &notSupported)
			require.Empty(t, got.Dirs())
		})
	}
}

// TestNodeMigrationProvider_StoreDirs_WALDirWithoutAnExternalPebbleWAL pins
// the cases storage.wal_dir does not refuse: a wal_dir naming the store's
// own directory, and a node with no Pebble store for it to apply to.
func TestNodeMigrationProvider_StoreDirs_WALDirWithoutAnExternalPebbleWAL(t *testing.T) {
	t.Run("wal_dir is the store directory", func(t *testing.T) {
		dataDir := t.TempDir()
		pebbleDir := filepath.Join(dataDir, "pebble")
		require.NoError(t, os.Mkdir(pebbleDir, 0o700))
		cfg := qrdbconfig.DefaultConfig()
		cfg.Storage.WALDir = pebbleDir + string(filepath.Separator)

		p := &nodeMigrationProvider{node: &Node{retainedStandalone: &pendingStandaloneBoot{dataDir: dataDir, cfg: cfg}}}
		got, err := p.StoreDirs(context.Background(), service.BarrierScope{Kind: service.ScopeSystem})
		require.NoError(t, err)
		require.Equal(t, []string{pebbleDir}, got.Pebble)
	})
	t.Run("no pebble store", func(t *testing.T) {
		dataDir := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(dataDir, "file"), 0o700))
		cfg := qrdbconfig.DefaultConfig()
		cfg.Storage.WALDir = t.TempDir()

		p := &nodeMigrationProvider{node: &Node{retainedStandalone: &pendingStandaloneBoot{dataDir: dataDir, cfg: cfg}}}
		got, err := p.StoreDirs(context.Background(), service.BarrierScope{Kind: service.ScopeSystem})
		require.NoError(t, err)
		require.Equal(t, []string{filepath.Join(dataDir, "file")}, got.File)
	})
}

// TestNode_EnableEncryption_ExternalPebbleWALRefusedAndIntact drives the
// combination end to end: a Pebble node with storage.wal_dir outside its
// data directory acknowledges writes, the system migration is refused
// before it touches anything, and every write is still served, then served
// again after a restart under the unchanged none barrier.
func TestNode_EnableEncryption_ExternalPebbleWALRefusedAndIntact(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	walDir := t.TempDir()
	newConfig := func() *qrdbconfig.Config {
		cfg := qrdbconfig.DefaultConfig()
		cfg.Auth.Enabled = qrdbconfig.BoolPtr(false) // not an authorization test; see testhelpers_test.go
		cfg.Raft.DataDir = dataDir
		cfg.Storage.WALDir = walDir
		cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendPebble)}}
		cfg.Seal.Enabled = true
		cfg.Seal.Strategy = "none"
		cfg.Protocols.REST = false
		cfg.Protocols.GRPC = false
		cfg.Protocols.QUIC = false
		cfg.Protocols.Unix = false
		cfg.Protocols.MCP = false
		return cfg
	}

	n, err := NewStandaloneNode(ctx, newConfig(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })

	want := map[string]string{}
	for i := 0; i < 20; i++ {
		k, v := fmt.Sprintf("key/%02d", i), fmt.Sprintf("value-%d", i)
		require.NoError(t, n.KVService().Put(ctx, k, []byte(v)))
		want[k] = v
	}

	err = n.EnableEncryption(ctx, service.BarrierScope{Kind: service.ScopeSystem}, seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase,
		Mechanism:      seal.MechanismSwap,
		Availability:   seal.AvailabilityQuiesced,
		Credentials:    seal.MustCredentials(ctx, []byte("external-wal-pass")),
	})
	var notSupported *MigrationNotSupportedError
	require.ErrorAs(t, err, &notSupported)
	for k, v := range want {
		got, gerr := n.KVService().Get(ctx, k)
		require.NoErrorf(t, gerr, "Get(%q) after the refused migration", k)
		require.Equal(t, v, string(got))
	}
	require.NoError(t, n.Close())

	n2, err := NewStandaloneNode(ctx, newConfig(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n2.Close() })
	for k, v := range want {
		got, gerr := n2.KVService().Get(ctx, k)
		require.NoErrorf(t, gerr, "Get(%q) after restart", k)
		require.Equal(t, v, string(got))
	}
}

// TestNode_EnableEncryption_SystemNoneToPassphrase_FileBackend is the file
// backend's acceptance for a system encryption migration: a node whose store
// is storage.backend: file moves from strategy none to a passphrase, serves
// every record under the keyed barrier, and reads them back after a restart
// that unseals with the passphrase. The file store is re-encrypted record by
// record under the new key; one left under the old key would be unreadable.
func TestNode_EnableEncryption_SystemNoneToPassphrase_FileBackend(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	newConfig := func() *qrdbconfig.Config {
		cfg := qrdbconfig.DefaultConfig()
		cfg.Auth.Enabled = qrdbconfig.BoolPtr(false) // not an authorization test; see testhelpers_test.go
		cfg.Raft.DataDir = dataDir
		cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendFile)}}
		cfg.Seal.Enabled = true
		cfg.Seal.Strategy = "none"
		cfg.Protocols.REST = false
		cfg.Protocols.GRPC = false
		cfg.Protocols.QUIC = false
		cfg.Protocols.Unix = false
		cfg.Protocols.MCP = false
		return cfg
	}

	n, err := NewStandaloneNode(ctx, newConfig(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })

	want := map[string]string{}
	for i := 0; i < 20; i++ {
		k, v := fmt.Sprintf("key/%02d", i), fmt.Sprintf("value-%d", i)
		require.NoError(t, n.KVService().Put(ctx, k, []byte(v)))
		want[k] = v
	}

	require.NoError(t, n.EnableEncryption(ctx, service.BarrierScope{Kind: service.ScopeSystem}, seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase,
		Mechanism:      seal.MechanismSwap,
		Availability:   seal.AvailabilityQuiesced,
		Credentials:    seal.MustCredentials(ctx, []byte("file-enable-enc-pass")),
	}))
	for k, v := range want {
		got, gerr := n.KVService().Get(ctx, k)
		require.NoErrorf(t, gerr, "Get(%q) after migration", k)
		require.Equal(t, v, string(got))
	}
	require.NoError(t, n.Close())

	n2, err := NewStandaloneNode(ctx, newConfig(), nil, WithSealPassphrase([]byte("file-enable-enc-pass")))
	require.NoError(t, err)
	t.Cleanup(func() { _ = n2.Close() })
	require.False(t, n2.IsSealed())
	for k, v := range want {
		got, gerr := n2.KVService().Get(ctx, k)
		require.NoErrorf(t, gerr, "Get(%q) after restart", k)
		require.Equal(t, v, string(got))
	}
}
