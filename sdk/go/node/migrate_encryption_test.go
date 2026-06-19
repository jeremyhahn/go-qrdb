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
	"testing"

	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestNode_EnableEncryption_SystemNoneToPassphrase boots a real standalone node
// with a none-strategy system barrier (pebble backend), writes data, then
// enables encryption in process (online, no restart) and verifies every record
// survives and is readable through the rebuilt, now-keyed data plane.
func TestNode_EnableEncryption_SystemNoneToPassphrase(t *testing.T) {
	ctx := context.Background()

	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendPebble)}}
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "none"
	// No protocol servers: drive the KV service in process.
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false

	n, err := NewStandaloneNode(ctx, cfg, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })

	require.False(t, n.IsSealed(), "none-strategy node boots unsealed")

	// --- Write data through the none (unencrypted) barrier. ----------------
	kv := n.KVService()
	require.NotNil(t, kv)
	want := map[string]string{}
	for i := 0; i < 100; i++ {
		k := fmt.Sprintf("key/%03d", i)
		v := fmt.Sprintf("value-%d", i)
		require.NoError(t, kv.Put(ctx, k, []byte(v)))
		want[k] = v
	}

	// --- Enable encryption in process (none -> passphrase). ----------------
	opts := seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase,
		Mechanism:      seal.MechanismSwap,
		Availability:   seal.AvailabilityQuiesced,
		Credentials:    seal.Credentials{Secret: []byte("enable-enc-pass")},
	}
	require.NoError(t, n.EnableEncryption(ctx, service.BarrierScope{Kind: service.ScopeSystem}, opts))

	// --- The data plane is rebuilt and serving again under the keyed barrier.
	kv2 := n.KVService()
	require.NotNil(t, kv2, "KV service must be available after the in-process recycle")
	for k, v := range want {
		got, gerr := kv2.Get(ctx, k)
		require.NoErrorf(t, gerr, "Get(%q) after migration", k)
		require.Equalf(t, v, string(got), "value for %q after migration", k)
	}

	// --- A new write succeeds post-migration (the node is fully operational).
	require.NoError(t, kv2.Put(ctx, "post/migration", []byte("ok")))
	got, gerr := kv2.Get(ctx, "post/migration")
	require.NoError(t, gerr)
	require.Equal(t, "ok", string(got))
}

// TestNode_EnableEncryption_InPlaceMechanism repeats the migration with the
// in-place (raw-file recodec) mechanism.
func TestNode_EnableEncryption_InPlaceMechanism(t *testing.T) {
	ctx := context.Background()
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendPebble)}}
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "none"
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false

	n, err := NewStandaloneNode(ctx, cfg, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })

	kv := n.KVService()
	want := map[string]string{}
	for i := 0; i < 40; i++ {
		k := fmt.Sprintf("k/%02d", i)
		v := fmt.Sprintf("v-%d", i)
		require.NoError(t, kv.Put(ctx, k, []byte(v)))
		want[k] = v
	}

	opts := seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase,
		Mechanism:      seal.MechanismInPlace,
		Availability:   seal.AvailabilityQuiesced,
		Credentials:    seal.Credentials{Secret: []byte("inplace-pass")},
	}
	require.NoError(t, n.EnableEncryption(ctx, service.BarrierScope{Kind: service.ScopeSystem}, opts))

	kv2 := n.KVService()
	for k, v := range want {
		got, gerr := kv2.Get(ctx, k)
		require.NoError(t, gerr)
		require.Equal(t, v, string(got))
	}
}
