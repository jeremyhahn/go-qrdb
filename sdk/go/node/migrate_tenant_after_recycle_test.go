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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestNode_EnableEncryption_TenantScope_AfterSystemRecycle is the go-qrdb#383
// regression test. TenantHostRegistry is built once, on the first boot, and is
// deliberately kept running through a system-scope encryption migration's
// in-place rebuild of the service graph. Before the fix it held the FIRST
// boot's *tenant.Service for its DataRoot lookup, so every tenant-scope
// migration after ANY completed system recycle failed reading through a
// stopped backend: "mvcc: get meta failed: storage is closed".
//
// A plain passphrase target is used on purpose: it rebuilds the data plane
// fully and immediately, which is the common case, and it shows the defect has
// nothing to do with a sealed Shamir window.
func TestNode_EnableEncryption_TenantScope_AfterSystemRecycle(t *testing.T) {
	ctx := context.Background()
	cfg := newTenantIsolationConfig(t)

	n, err := NewStandaloneNode(ctx, cfg, nil,
		WithTenantIsolation(),
		WithDefaultTenantEntitlements(volumeEntitlements()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	require.NotNil(t, n.tenantHosts, "tenantHosts must be wired when WithTenantIsolation() is set")

	createDefaultPool(t, n)
	_, err = n.Services().Tenant.CreateTenant(ctx, "acme", "Acme Corp",
		tenant.IsolationVolume, tenant.WithBarrierAutoUnseal(true))
	require.NoError(t, err)
	svc, err := n.tenantHosts.Services(ctx, "acme")
	require.NoError(t, err)
	require.NoError(t, svc.KV.Put(ctx, "k", []byte("v")))

	firstTenantSvc := n.Services().Tenant

	// A full system-scope recycle: the service graph, including the tenant
	// service the registry was built against, is stopped and rebuilt.
	require.NoError(t, n.EnableEncryption(ctx, service.BarrierScope{Kind: service.ScopeSystem}, seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase,
		Mechanism:      seal.MechanismSwap,
		Availability:   seal.AvailabilityQuiesced,
		Credentials:    seal.MustCredentials(context.Background(), []byte("system-pass")),
	}))
	require.False(t, n.IsSealed())
	require.NotSame(t, firstTenantSvc, n.Services().Tenant,
		"the recycle must have replaced the tenant service; otherwise this test does not exercise go-qrdb#383")

	// The rebuilt tenant service must carry the runtime closer too, or its
	// deleted-tenant reconcile would remove a retired tenant's directories
	// from under a host the node is still running (go-qrdb#469).
	rebuilt, ok := n.Services().Tenant.(*tenant.TenantService)
	require.True(t, ok, "a seal-enabled node runs *tenant.TenantService")
	require.NotNil(t, rebuilt.TenantRuntimeCloser(),
		"the system recycle rebuilt the tenant service without re-wiring its runtime closer")

	// The tenant-scope migration after the recycle must reach the LIVE tenant
	// service for the tenant's DataRoot.
	err = n.EnableEncryption(ctx, service.BarrierScope{Kind: service.ScopeTenant, TenantID: "acme"}, seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase,
		Mechanism:      seal.MechanismSwap,
		Availability:   seal.AvailabilityQuiesced,
		Credentials:    seal.MustCredentials(context.Background(), []byte("acme-pass")),
	})
	if err != nil {
		require.Falsef(t, strings.Contains(err.Error(), "storage is closed"),
			"tenant-scope migration read through the stopped pre-recycle tenant service: %v", err)
	}
	require.NoError(t, err, "tenant-scope migration after a completed system recycle")

	// And the tenant's data survives under its newly keyed barrier.
	svc2, err := n.tenantHosts.Services(ctx, "acme")
	require.NoError(t, err)
	got, err := svc2.KV.Get(ctx, "k")
	require.NoError(t, err)
	require.Equal(t, "v", string(got))
}
