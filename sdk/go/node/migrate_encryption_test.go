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
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	quicraftshamir "github.com/jeremyhahn/go-quicraft/pkg/crypto/shamir"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// nodeCaptureDist is a test-only seal.CeremonyDistributor that captures the
// Shamir shares generated during a direct-mode migration ceremony so the test
// can later reconstruct quorum and drive UnsealWithShare, without standing up
// the full external-officer PKI ceremony. Mirrors
// pkg/server/tenant_migrate_test.go's serverCaptureDist.
type nodeCaptureDist struct {
	mu     sync.Mutex
	shares []string
}

func (c *nodeCaptureDist) SealAndDistribute(_ context.Context, secret []byte, threshold, total int) (int, error) {
	raw, err := quicraftshamir.Split(secret, threshold, total)
	if err != nil {
		return 0, err
	}
	c.mu.Lock()
	c.shares = make([]string, len(raw))
	for i, s := range raw {
		c.shares[i] = s.Value
	}
	c.mu.Unlock()
	return len(raw), nil
}

func (c *nodeCaptureDist) RekeyAndDistribute(ctx context.Context, newSecret []byte, newThreshold, newTotal int) (int, error) {
	c.mu.Lock()
	c.shares = nil
	c.mu.Unlock()
	return c.SealAndDistribute(ctx, newSecret, newThreshold, newTotal)
}

func (c *nodeCaptureDist) Shares() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.shares...)
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

// TestNode_EnableEncryption_SystemNoneToShamir proves the embedded node's
// system-scope Shamir migration produces a GENUINE M-of-N Shamir barrier —
// not a silently mislabeled passphrase barrier (the Codex #7 security
// downgrade: the embedded path used to map StrategyShamir to
// NewDeferredSoftwareStrategy + MintKeyedSibling instead of the real
// MintKeyedShamirSibling ceremony). It proves the full lifecycle: the
// migration completes, real shares are distributed to the (fake, test-only)
// CeremonyDistributor, the system barrier reports strategy=shamir with the
// correct threshold/total and comes back SEALED (Shamir never auto-unseals),
// the data plane correctly stays torn down until a Security Officer quorum
// unseals it, and submitting that quorum completes the boot with zero data
// loss.
func TestNode_EnableEncryption_SystemNoneToShamir(t *testing.T) {
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

	// Wire a fake CeremonyDistributor directly on the live system barrier
	// (white-box: this test lives in package node) so the migration's real
	// M-of-N split + distribution can be captured and later replayed as a
	// genuine quorum, without standing up the full external-officer PKI
	// ceremony. This is the same operational precondition a fresh
	// BarrierShamirInitialize has: SetCeremonyDistributor before the
	// migration, exactly mirroring pkg/server/tenant_migrate_test.go's
	// TestTenantHostRegistry_MigrateEncryption_NoneToShamir.
	dist := &nodeCaptureDist{}
	n.barrier.SetCeremonyDistributor(dist)

	// --- Write data through the none (unencrypted) barrier. ----------------
	kv := n.KVService()
	require.NotNil(t, kv)
	want := map[string]string{}
	for i := 0; i < 25; i++ {
		k := fmt.Sprintf("shamir/%03d", i)
		v := fmt.Sprintf("value-%d", i)
		require.NoError(t, kv.Put(ctx, k, []byte(v)))
		want[k] = v
	}

	// --- Enable encryption in process (none -> shamir). ---------------------
	opts := seal.MigrationOptions{
		TargetStrategy:  seal.StrategyShamir,
		Mechanism:       seal.MechanismSwap,
		Availability:    seal.AvailabilityQuiesced,
		ShamirThreshold: 2,
		ShamirTotal:     3,
	}
	require.NoError(t, n.EnableEncryption(ctx, service.BarrierScope{Kind: service.ScopeSystem}, opts))

	// A genuine M-of-N ceremony ran: 3 real shares were distributed (not a
	// mislabeled passphrase strategy with zero officer shares).
	require.Len(t, dist.Shares(), 3, "migration must distribute real Shamir shares to enrolled officers")

	// The system barrier is now genuinely Shamir — correct label, correct
	// M/N — and SEALED (Shamir never auto-unseals from disk, migration or
	// not).
	require.True(t, n.IsSealed(), "post-migration shamir system barrier must be sealed pending Security Officer quorum")
	status := n.barrier.Status()
	require.Equal(t, seal.StrategyShamir, status.Strategy, "must not be silently downgraded to passphrase")
	require.Equal(t, 2, status.ShamirThreshold)
	require.Equal(t, 3, status.ShamirTotal)

	// The data plane correctly stays torn down: no KV service until unsealed.
	require.Nil(t, n.KVService(), "data plane must not be rebuilt while the migrated shamir barrier is sealed")

	// Submitting a quorum (2-of-3) of the REAL distributed shares unseals the
	// migrated barrier — the "shares are usable" proof.
	shares := dist.Shares()
	_, uerr := n.barrier.UnsealWithShare(ctx, shares[0])
	require.NoError(t, uerr)
	progress, uerr := n.barrier.UnsealWithShare(ctx, shares[1])
	require.NoError(t, uerr)
	require.True(t, progress.Complete)
	require.False(t, n.barrier.IsSealed())

	// Completing the boot (mirrors the ceremony API driving Node.Unseal once
	// quorum is reached externally — see its doc comment on the "already
	// unsealed externally" fallthrough) rebuilds the data plane, and every
	// datum written before the migration survives with zero loss.
	require.NoError(t, n.Unseal(ctx, seal.Credentials{}))
	require.False(t, n.IsSealed())

	kv2 := n.KVService()
	require.NotNil(t, kv2, "KV service must be available after the SO quorum completes the boot")
	for k, v := range want {
		got, gerr := kv2.Get(ctx, k)
		require.NoErrorf(t, gerr, "Get(%q) after shamir migration+unseal", k)
		require.Equalf(t, v, string(got), "value for %q after shamir migration+unseal (DATA LOSS)", k)
	}

	// A new write succeeds post-migration (the node is fully operational).
	require.NoError(t, kv2.Put(ctx, "post/shamir-migration", []byte("ok")))
	got, gerr := kv2.Get(ctx, "post/shamir-migration")
	require.NoError(t, gerr)
	require.Equal(t, "ok", string(got))
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
	require.Equal(t, seal.StrategyNone, n.barrier.ActiveStrategy())
}
