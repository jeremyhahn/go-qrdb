// Copyright (c) 2025 Jeremy Hahn
// SPDX-License-Identifier: AGPL-3.0-only

package node

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// TestNode_MoveAfterStandaloneEncryptionMigration: a standalone system
// encryption migration rebuilds the data plane, catalog and tenant service.
// The tenant move worker of the old plane is stopped and joined, and a new
// one is wired over the rebuilt catalog, so a move admitted after the
// migration is admitted (not MoveNotImplementedError) and runs to its end.
func TestNode_MoveAfterStandaloneEncryptionMigration(t *testing.T) {
	ctx := context.Background()
	cfg := newTenantIsolationConfig(t)
	cfg.Storage.VolumePools = append(cfg.Storage.VolumePools,
		qrdbconfig.VolumePoolConfig{Name: "bulk", Path: t.TempDir()})
	ents := volumeEntitlements()
	ents.AllowedVolumePools = []string{"default", "bulk"}

	n, err := NewStandaloneNode(ctx, cfg, nil, WithTenantIsolation(), WithDefaultTenantEntitlements(ents))
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })

	createDefaultPool(t, n)
	svc := n.Services()
	catalog, err := volume.NewCatalog(lifecycle.NewVolumeCatalogKVServicer(svc.KV), svc.Transaction, n.volumeRegistry)
	require.NoError(t, err)
	_, err = catalog.CreatePool(ctx, volume.CreatePoolRequest{Name: "bulk"})
	require.NoError(t, err)

	_, err = svc.Tenant.CreateTenant(ctx, "acme", "Acme Corp",
		tenant.IsolationVolume, tenant.WithBarrierAutoUnseal(true))
	require.NoError(t, err)
	hosted, err := n.tenantHosts.Services(ctx, "acme")
	require.NoError(t, err)
	require.NoError(t, hosted.KV.Put(ctx, "k", []byte("v")))

	firstWorker := n.stopMoveWorker
	require.NotNil(t, firstWorker, "precondition: the boot wired the tenant move worker")
	firstCatalog := n.volumeCatalog

	require.NoError(t, n.EnableEncryption(ctx, service.BarrierScope{Kind: service.ScopeSystem}, seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase,
		Mechanism:      seal.MechanismSwap,
		Availability:   seal.AvailabilityQuiesced,
		Credentials:    seal.MustCredentials(context.Background(), []byte("system-pass")),
	}))
	require.False(t, n.IsSealed())
	require.NotSame(t, firstCatalog, n.volumeCatalog, "the recycle must rebuild the catalog")
	require.NotNil(t, n.stopMoveWorker, "the rebuild must wire a new tenant move worker")

	_, err = n.volumeCatalog.MoveTenant(ctx, volume.MoveTenantRequest{TenantID: "acme", ToPool: "bulk"})
	var notImpl *volume.MoveNotImplementedError
	require.False(t, errors.As(err, &notImpl), "a move after the migration found no admission: %v", err)
	require.NoError(t, err)
	// The move's run to completion after the recycle is still open: its copy
	// step fails reading the tenant posture through the barrier registry
	// ("decryption failed") after the system re-key (next slice).
}
