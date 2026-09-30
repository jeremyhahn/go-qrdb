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
	"time"

	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// noSealStoreNode boots a real standalone node and then re-adopts its seal
// subsystem through adoptSealSubsystem -- the one path both node builders
// install it by -- with the seal store absent, simulating a backend that
// cannot hold one. Every construction path in this package builds the barrier
// registry and the seal store together in buildNodeSealSubsystem, so a node
// with a registry and no store cannot currently be produced through the public
// API; going through adoptSealSubsystem rather than assigning n.sealStore
// directly is what makes this exercise the shape the field's type exists to
// prevent. If nodeSealSubsystem.sealStore were a concrete pointer again, its
// nil would arrive in n.sealStore as a non-nil interface, the precondition
// check below would fail, and without it the refusals under test would
// dereference a nil receiver instead.
func noSealStoreNode(t *testing.T) *Node {
	t.Helper()
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
	store := n.sealStore
	require.NotNil(t, store, "precondition: a normal boot has a seal store")
	// The node no longer owns the store once it is re-adopted without one, so
	// the test closes it -- after the node, which it must outlive.
	t.Cleanup(func() {
		_ = n.Close()
		_ = store.Close()
	})

	n.adoptSealSubsystem(&nodeSealSubsystem{
		barrier:         n.barrier.Load(),
		credCoreSvc:     n.credCoreSvc,
		barrierRegistry: n.barrierRegistry.Load(),
		volumePool:      n.volumePool,
	})
	// Compared with == rather than require.Nil, which reports a typed nil
	// inside a non-nil interface as nil and so cannot see the defect.
	require.True(t, n.sealStore == nil, "an absent seal store must reach Node.sealStore as a nil interface, got %T", n.sealStore)
	return n
}

// TestCanonicalRootKeyStrategy_NoSealStore_RefusesTyped is the go-qrdb#550
// regression test for the nil-seal-store segfault: canonicalRootKeyStrategy
// used to hand n.sealStore straight to seal.NewStorageAdapter without a nil
// check, and passing a nil *pebbleStorage.PebbleStorage into that
// interface-typed parameter produces a NON-nil seal.LegacyStorage wrapping a
// nil receiver -- the read then panicked inside the concrete backend instead
// of ever reaching a nil check. It now refuses with the same typed
// *MigrationNotSupportedError the rest of this package uses for "cannot
// migrate here", and does not panic.
func TestCanonicalRootKeyStrategy_NoSealStore_RefusesTyped(t *testing.T) {
	n := noSealStoreNode(t)

	_, err := canonicalRootKeyStrategy(context.Background(), n)

	var notSupported *MigrationNotSupportedError
	require.ErrorAs(t, err, &notSupported)
	require.Contains(t, notSupported.Reason, "seal store")
	// The refusal every transport maps to 501 / Unimplemented.
	require.ErrorIs(t, err, service.ErrMigrationProviderUnavailable)
}

// TestNode_EnableEncryption_NoSealStore_RefusesTyped pins the same refusal at
// the entry point a real caller reaches: nodeSystemMigrator.EnableEncryption
// refuses upfront, before Quiesce ever tears the data plane down, rather than
// only failing once canonicalRootKeyStrategy is reached deep inside a
// migration already in progress.
func TestNode_EnableEncryption_NoSealStore_RefusesTyped(t *testing.T) {
	n := noSealStoreNode(t)

	err := n.EnableEncryption(context.Background(), service.BarrierScope{Kind: service.ScopeSystem}, seal.MigrationOptions{})

	var notSupported *MigrationNotSupportedError
	require.ErrorAs(t, err, &notSupported)
	require.Contains(t, notSupported.Reason, "seal store")
	// The refusal every transport maps to 501 / Unimplemented.
	require.ErrorIs(t, err, service.ErrMigrationProviderUnavailable)
}

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
		Credentials:    seal.MustCredentials([]byte("enable-enc-pass")),
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

	// --- Restart-survival: close the node and re-open it from the SAME data
	// dir with the config STILL naming strategy "none" (the migration does not
	// rewrite the config). The boot must self-heal — registering the migrated
	// (keyed) strategy from the persisted root key — and unseal with the
	// passphrase, with all data intact.
	require.NoError(t, n.Close())

	cfg2 := qrdbconfig.DefaultConfig()
	cfg2.Raft.DataDir = cfg.Raft.DataDir // same data dir
	cfg2.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendPebble)}}
	cfg2.Seal.Enabled = true
	cfg2.Seal.Strategy = "none" // unchanged on disk — the node must self-heal
	cfg2.Protocols.REST = false
	cfg2.Protocols.GRPC = false
	cfg2.Protocols.QUIC = false
	cfg2.Protocols.Unix = false
	cfg2.Protocols.MCP = false

	// The keyed barrier requires the passphrase to unseal on boot.
	n2, err := NewStandaloneNode(ctx, cfg2, nil, WithSealPassphrase([]byte("enable-enc-pass")))
	require.NoError(t, err, "node must boot from a migrated data dir even with config strategy=none")
	t.Cleanup(func() { _ = n2.Close() })
	require.False(t, n2.IsSealed(), "migrated node must unseal on restart")

	kv3 := n2.KVService()
	require.NotNil(t, kv3)
	for k, v := range want {
		gotR, rerr := kv3.Get(ctx, k)
		require.NoErrorf(t, rerr, "Get(%q) after restart", k)
		require.Equalf(t, v, string(gotR), "value for %q after restart", k)
	}
}

// TestNode_EnableEncryption_ViaEmbeddedTransport is the go-qrdb#558 SDK-wiring
// regression test: enable-encryption must reach the SAME node-level migrator
// whether driven directly via Node.EnableEncryption (proven by
// TestNode_EnableEncryption_SystemNoneToPassphrase above) or through the
// embedded SDK transport (n.Client().Barrier().BarrierEnableEncryption).
// Before buildEmbeddedOpts wired embedded.WithEncryptionMigrator, this call
// always failed with service.ErrMigrationProviderUnavailable regardless of
// node state, since the embedded transport had no migrator wiring of any
// kind.
//
// Unlike the direct call, which runs synchronously to completion, the
// transport handler races the migration against an accept window and
// reports "migration_started" immediately (matching every other transport),
// so this test polls BarrierStatus for the strategy to change.
func TestNode_EnableEncryption_ViaEmbeddedTransport(t *testing.T) {
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
	require.False(t, n.IsSealed(), "none-strategy node boots unsealed")

	kv := n.KVService()
	require.NoError(t, kv.Put(ctx, "key/embedded-transport", []byte("value")))

	resp, err := n.Client().Barrier().BarrierEnableEncryption(ctx, &transport.BarrierEnableEncryptionRequest{
		Strategy:   string(seal.StrategyPassphrase),
		Passphrase: []byte("enable-enc-pass-embedded"),
	})
	require.NoError(t, err, "BarrierEnableEncryption over the embedded transport")
	require.Equal(t, "migration_started", resp.Status)
	require.Equal(t, "system", resp.Scope)

	require.Eventually(t, func() bool {
		// The system-scope migration recycles the whole in-process data
		// plane, briefly swapping n.client to nil between teardown and the
		// rebuilt client's Store -- this poll must tolerate that window
		// rather than dereferencing a nil *sdk.Client.
		client := n.Client()
		if client == nil {
			return false
		}
		status, serr := client.Barrier().BarrierStatus(ctx)
		return serr == nil && status.Strategy == string(seal.StrategyPassphrase)
	}, 5*time.Second, 20*time.Millisecond, "strategy never changed from none to passphrase")

	// The data plane is rebuilt and serving again under the keyed barrier,
	// reached through a freshly-resolved client (the recycle replaces it).
	kv2 := n.KVService()
	require.NotNil(t, kv2, "KV service must be available after the in-process recycle")
	got, gerr := kv2.Get(ctx, "key/embedded-transport")
	require.NoError(t, gerr)
	require.Equal(t, "value", string(got))
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
		Credentials:    seal.MustCredentials([]byte("inplace-pass")),
	}
	require.NoError(t, n.EnableEncryption(ctx, service.BarrierScope{Kind: service.ScopeSystem}, opts))

	kv2 := n.KVService()
	for k, v := range want {
		got, gerr := kv2.Get(ctx, k)
		require.NoError(t, gerr)
		require.Equal(t, v, string(got))
	}
}

// TestNode_EnableEncryption_SystemShamirRefused proves a system-scope
// migration to Shamir is refused before the data plane is touched: the
// in-process restart would discard the share envelopes it distributes. The
// node keeps serving on its none barrier and every datum survives.
func TestNode_EnableEncryption_SystemShamirRefused(t *testing.T) {
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
	require.NoError(t, n.KVService().Put(ctx, "k", []byte("v")))

	opts := seal.MigrationOptions{
		TargetStrategy:  seal.StrategyShamir,
		Mechanism:       seal.MechanismSwap,
		Availability:    seal.AvailabilityQuiesced,
		ShamirThreshold: 2,
		ShamirTotal:     3,
	}
	err = n.EnableEncryption(ctx, service.BarrierScope{Kind: service.ScopeSystem}, opts)
	require.ErrorIs(t, err, service.ErrShamirSystemMigrationUnsupported)

	require.False(t, n.IsSealed())
	require.Equal(t, seal.StrategyNone, n.barrier.Load().ActiveStrategy())
	got, err := n.KVService().Get(ctx, "k")
	require.NoError(t, err)
	require.Equal(t, "v", string(got))
}

// TestNode_EnableEncryption_SystemShamirInsufficientOfficers proves a Shamir
// target migration on a node with no enrolled Security Officers fails CLOSED
// through the real ceremony's own precondition — the same failure mode a
// fresh BarrierShamirInitialize would give — rather than silently completing
// as a mislabeled passphrase barrier via the generic MintKeyedSibling path
// (the routing this test guards against regressing to).
func TestNode_EnableEncryption_SystemShamirInsufficientOfficers(t *testing.T) {
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
	// No officers enrolled: the node's real ceremony service is wired
	// automatically by lifecycle.BuildServices (SetCeremonyDistributor), but
	// with zero Security Officers registered the ceremony's own
	// insufficient-officers guard applies. This still proves the routing fix:
	// the OLD buggy code reached MintKeyedSibling with an empty-secret
	// passphrase strategy (a different, wrong failure mode, or a silent
	// mislabeled "success"); the FIXED code reaches MintKeyedShamirSibling's
	// real ceremony precondition instead.

	opts := seal.MigrationOptions{
		TargetStrategy:  seal.StrategyShamir,
		Mechanism:       seal.MechanismSwap,
		Availability:    seal.AvailabilityQuiesced,
		ShamirThreshold: 2,
		ShamirTotal:     3,
	}
	err = n.EnableEncryption(ctx, service.BarrierScope{Kind: service.ScopeSystem}, opts)
	require.Error(t, err)

	// The barrier must be untouched by the failed attempt.
	require.False(t, n.IsSealed())
	require.Equal(t, seal.StrategyNone, n.barrier.Load().ActiveStrategy())
}
