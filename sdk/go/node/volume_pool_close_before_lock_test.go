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
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// testLockableVolume wraps a real "dir"-provider Volume (so Root/Usage/
// Health/Close, and the directory IsolationVolume tenants actually write
// into, are all real) and adds an instrumented volume.Lockable on top --
// this package ships no real Lockable provider (layer ownership: a real
// one, e.g. go-xkms's luks2, is a hardware-composition concern), so this is
// the stand-in every test exercising decision D6's lock/unlock ordering
// through a real Node boot must supply itself.
type testLockableVolume struct {
	volume.Volume

	mu          sync.Mutex
	unlockCalls int
	lockCalls   int
}

func (v *testLockableVolume) Unlock(context.Context, volume.VolumeKey) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.unlockCalls++
	return nil
}

func (v *testLockableVolume) Lock(context.Context) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.lockCalls++
	return nil
}

func (v *testLockableVolume) counts() (unlocks, locks int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.unlockCalls, v.lockCalls
}

// testLockableProvider opens the real "dir" provider underneath and wraps
// its Volume in testLockableVolume, publishing the wrapped Volume on vol so
// the test can inspect it once the node has booted (Open runs exactly once,
// synchronously, during NewStandaloneNode).
type testLockableProvider struct {
	inner volume.Provider
	vol   *testLockableVolume
}

func newTestLockableProvider() *testLockableProvider {
	return &testLockableProvider{inner: volume.NewDirProvider()}
}

func (p *testLockableProvider) Name() string { return "testlockable" }

func (p *testLockableProvider) Open(ctx context.Context, spec volume.Spec) (volume.Volume, error) {
	inner, err := p.inner.Open(ctx, spec)
	if err != nil {
		return nil, err
	}
	v := &testLockableVolume{Volume: inner}
	p.vol = v
	return v, nil
}

// TestNode_VolumePoolSeal_ClosesTenantStoresBeforeLockingVolume is the
// integration proof for security review item 4(a): sealing a real Node
// with a real IsolationVolume tenant open on a Lockable pool actually
// closes that tenant's live per-tenant Host (the real composer wiring --
// server.CloseTenantStoresForPool through the TenantHostRegistryHolder,
// not a stand-in) before the pool's volume is locked, end to end through
// the public Node API and the real StorageBarrier.Seal path -- not a
// pkg/seal-level fake closeStores function (see
// TestVolumeLifecycle_WithCloseStoresFunc_RunsBeforeLock there for the
// generic ordering guarantee this test wires a real composer into).
func TestNode_VolumePoolSeal_ClosesTenantStoresBeforeLockingVolume(t *testing.T) {
	ctx := context.Background()
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
		{Name: "default", Path: poolDir, Provider: "testlockable"},
	}
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false

	provider := newTestLockableProvider()
	n, err := NewStandaloneNode(ctx, cfg, nil,
		WithTenantIsolation(),
		WithDefaultTenantEntitlements(volumeEntitlements()),
		WithVolumeProvider(provider))
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	require.False(t, n.IsSealed(), "none-strategy node must boot unsealed")
	require.NotNil(t, provider.vol, "Open must have run against the configured pool during boot")

	// Boot's own unlockAll already ran against the tracked Lockable volume:
	// a none-strategy barrier auto-unseals during NewStandaloneNode.
	unlocks, locks := provider.vol.counts()
	require.Equal(t, 1, unlocks, "boot must unlock the tracked Lockable volume exactly once")
	require.Zero(t, locks, "the volume must not be locked before Seal")

	// The catalog pool's provider must match this node's local mapping
	// ("testlockable"), or attachVolumeCatalog's provider-consistency
	// check (security review finding 7) refuses to serve it.
	svc := n.Services()
	require.NotNil(t, svc)
	catalog, err := volume.NewCatalog(lifecycle.NewVolumeCatalogKVServicer(svc.KV), svc.Transaction, n.volumeRegistry)
	require.NoError(t, err)
	_, err = catalog.CreatePool(ctx, volume.CreatePoolRequest{Name: "default", Provider: "testlockable"})
	require.NoError(t, err)

	_, err = n.Services().Tenant.CreateTenant(ctx, "acme", "Acme Corp",
		tenant.IsolationVolume, tenant.WithBarrierAutoUnseal(true))
	require.NoError(t, err, "CreateTenant(IsolationVolume) must succeed with volume entitlements")

	hostSvc, err := n.tenantHosts.Services(ctx, "acme")
	require.NoError(t, err, "Services(acme) must resolve the per-tenant Host")
	require.NoError(t, hostSvc.KV.Put(ctx, "k", []byte("v")))
	require.True(t, n.tenantHosts.Started("acme"), "the tenant's per-tenant Host must be live before Seal")

	require.NoError(t, n.BarrierRegistry().SystemBarrier().Seal())

	_, locks = provider.vol.counts()
	require.Equal(t, 1, locks, "Seal must lock the real Lockable volume")
	require.False(t, n.tenantHosts.Started("acme"),
		"Seal must close the tenant's per-tenant Host through the real composer wiring before locking the volume")
}
