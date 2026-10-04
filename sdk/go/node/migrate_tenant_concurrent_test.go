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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestNode_EnableEncryption_TenantScope_DuringSystemShamirSealedWindow is the
// go-qrdb#222 regression test: a tenant-scope migration's entitlement check
// issued while a system-scope Shamir migration is sealed pending a Security
// Officer quorum must reach the LIVE tenant service, not a handle onto the
// STOPPED pre-recycle one.
//
// This is the worst-case window the issue names: nodeMigrationProvider.Reopen
// never reaches the data-plane rebuild for a committed Shamir target -- it
// stores a seal-window-only svcNode (lifecycle.BuildSealServicesOnly) and
// returns sealed -- so the window lasts until an operator supplies quorum,
// which can be arbitrarily long. TenantHostRegistry (n.tenantHosts) is wired
// to survive that window by design (see Node.retractDataPlaneServices).
//
// MEASURED, not assumed (see the commit message for the before/after run):
// the seal-window Tenant service BuildSealServicesOnly constructs is backed
// directly by the barrier under migration (infra.SealBarrier), so a
// dedicated-mode CheckBarrierMode call against it is ALSO refused while that
// barrier is sealed -- seal.ErrSealed, a well-defined domain error. That is a
// separate, pre-existing architectural fact about the seal-window tenant
// service, not something go-qrdb#222's scope (sdk/go/node,
// server.NewScopedEncryptionMigrator) touches or can fix by itself, and this
// test does not claim otherwise.
//
// What this test DOES prove, by running the same scenario against both the
// pre-fix capture (git-inspectable via the nodeTenantResolver diff) and the
// fix: pre-fix, the error is an infrastructure artifact of talking to a
// CLOSED backend ("storage is closed" -- go's mvcc layer surfacing the
// stopped pre-recycle raft/KV host); post-fix, the error is the LIVE
// service's own typed seal.ErrSealed, reflecting the barrier's actual current
// state, AND the router itself is never refused with
// MigrationNotSupportedError -- proof encMigrator stayed published and its
// tenant leg reached the CURRENT tenant service rather than the stopped one.
// That distinction -- stopped backend vs. live service correctly reporting
// itself -- is exactly the defect in the issue title ("holds a stopped Tenant
// service"). See the NOTE inside the test for why it stops there rather than
// retrying the migration after the quorum completes.
func TestNode_EnableEncryption_TenantScope_DuringSystemShamirSealedWindow(t *testing.T) {
	ctx := context.Background()
	cfg := newTenantIsolationConfig(t)

	n, err := NewStandaloneNode(ctx, cfg, nil,
		WithTenantIsolation(),
		WithDefaultTenantEntitlements(volumeEntitlements()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	require.False(t, n.IsSealed(), "none-strategy node must boot unsealed")
	require.NotNil(t, n.tenantHosts, "tenantHosts must be wired when WithTenantIsolation() is set")

	// A dedicated IsolationVolume tenant on its OWN none-strategy barrier,
	// entirely independent of the system barrier this test migrates.
	_, err = n.Services().Tenant.CreateTenant(ctx, "acme", "Acme Corp",
		tenant.IsolationVolume, tenant.WithBarrierAutoUnseal(true))
	require.NoError(t, err, "CreateTenant(IsolationVolume) must succeed with volume entitlements")

	svc, err := n.tenantHosts.Services(ctx, "acme")
	require.NoError(t, err, "Services(acme) must resolve the per-tenant Host")
	require.NoError(t, svc.KV.Put(ctx, "k", []byte("v")))

	// Drive the SYSTEM barrier through a genuine 2-of-3 Shamir migration.
	// nodeMigrationProvider.Reopen's Shamir-committed branch returns sealed,
	// pending Security Officer quorum -- the worst-case window under test.
	custody := sealtest.NewCustody("h1", "h2", "h3")
	custody.Install(n.barrier.Load())
	sysOpts := seal.MigrationOptions{
		TargetStrategy:  seal.StrategyShamir,
		Mechanism:       seal.MechanismSwap,
		Availability:    seal.AvailabilityQuiesced,
		ShamirThreshold: 2,
		ShamirTotal:     3,
	}
	require.NoError(t, n.EnableEncryption(ctx, service.BarrierScope{Kind: service.ScopeSystem}, sysOpts))
	require.True(t, n.IsSealed(), "post-migration shamir system barrier must be sealed pending SO quorum")
	require.Nil(t, n.KVService(), "system data plane must stay torn down while sealed")

	// --- THE WINDOW: issue a tenant-scope migration now, while the system
	// barrier is sealed and n.svcNode is the seal-window-only service graph
	// BuildSealServicesOnly built. ---
	tenOpts := seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase,
		Mechanism:      seal.MechanismSwap,
		Availability:   seal.AvailabilityQuiesced,
		Credentials:    seal.MustCredentials(context.Background(), []byte("acme-pass")),
	}
	err = n.EnableEncryption(ctx,
		service.BarrierScope{Kind: service.ScopeTenant, TenantID: "acme"}, tenOpts)

	// The router itself is never refused: no MigrationNotSupportedError, which
	// is what the DECLINED "nil encMigrator in teardownDataPlane" fix would
	// have produced for the entire window (see retractDataPlaneServices).
	var notSupported *MigrationNotSupportedError
	require.False(t, errors.As(err, &notSupported),
		"router must not be refused as \"not yet fully booted\" -- encMigrator is deliberately kept published across this window")

	// The failure, if any, must be the LIVE tenant service's own typed
	// seal.ErrSealed -- proof CheckBarrierMode reached the CURRENT
	// (seal-window) tenant service and it correctly reported its own state --
	// never an infrastructure artifact of a CLOSED backend ("storage is
	// closed" / "mvcc" / raft-host-closed wording), which is what the
	// pre-fix captured, stopped *tenant.Service produced for this exact
	// scenario (see the doc comment above).
	require.Error(t, err, "system barrier is genuinely sealed pending quorum; the dedicated-mode entitlement check cannot read through it")
	require.Truef(t, errors.Is(err, seal.ErrSealed), "want seal.ErrSealed from the LIVE tenant service, got %v", err)
	require.Falsef(t, strings.Contains(err.Error(), "storage is closed"),
		"must not be the stopped pre-recycle backend's error, got %v", err)

	// The tenant's data is untouched by the (refused) migration attempt: the
	// per-tenant Host, wired once at boot and never captured/recycled by the
	// system-scope migration, is still live and unaffected.
	svc2, err := n.tenantHosts.Services(ctx, "acme")
	require.NoError(t, err, "Services(acme) must resolve the per-tenant Host, unaffected by the system recycle")
	got, gerr := svc2.KV.Get(ctx, "k")
	require.NoError(t, gerr)
	require.Equal(t, "v", string(got))

	// --- Complete the system-scope Shamir quorum so the node finishes
	// booting cleanly (avoids racing a half-open host in t.Cleanup's Close).
	//
	// NOTE: this test deliberately stops here rather than retrying the
	// tenant-scope migration post-reboot. Doing so surfaces a SEPARATE,
	// pre-existing staleness bug: BuildTenantInfra (called from
	// completeStandaloneBoot, guarded by `n.tenantHosts == nil` so it runs
	// only on the very first boot) wires TenantHostRegistry's own
	// tenantInfoSource from THAT FIRST svcNode.Tenant and never re-wires it on
	// a later system-scope rebuild, so TenantHostRegistry.MigrateEncryption's
	// dataRootOf lookup fails with the same "storage is closed" symptom this
	// issue's fix eliminates from scopedEncryptionMigrator -- confirmed by
	// running that call here and observing the identical failure signature.
	// That defect is in BuildTenantInfra/TenantHostRegistry, not in
	// server.NewScopedEncryptionMigrator, so it is out of go-qrdb#222's
	// stated scope; tracked separately as go-qrdb#383 rather than folded in
	// here.
	// The reopened barrier is re-composed with the node's own ceremony
	// service as its custody; put the test's custody back so its holders can
	// answer.
	custody.Install(n.barrier.Load())
	progress, uerr := custody.Quorum(ctx, n.barrier.Load(), seal.DomainUnseal, seal.OpUnseal, "h1", "h2")
	require.NoError(t, uerr)
	require.True(t, progress.Complete)
	require.NoError(t, n.Unseal(ctx, seal.Credentials{}))
	require.False(t, n.IsSealed())
	require.NotNil(t, n.KVService(), "system data plane must be rebuilt after quorum completes the boot")
}
