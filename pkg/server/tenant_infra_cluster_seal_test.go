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

package server

import (
	"context"
	"testing"

	qrdbpool "github.com/jeremyhahn/go-qrdb/pkg/storage/pool"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// restartedAutoUnsealVolumeTenant creates an auto-unseal volume tenant, then
// simulates a restart: the returned registry is fresh and knows the tenant
// only from its persisted root record, exactly what BuildTenantInfra sees at
// boot.
func restartedAutoUnsealVolumeTenant(t *testing.T, id string) (*freshRegistryFixture, TenantInfraConfig) {
	t.Helper()
	fx, _, perTenantReg1 := newFreshRegistryFixture(t, &quota.Entitlements{
		AllowedIsolationModes:     []string{quota.IsolationModePrefix, quota.IsolationModeVolume},
		AllowVolumeStorage:        true,
		AllowSystemManagedBarrier: true,
	})
	ctx := context.Background()
	if _, err := fx.svc.CreateTenant(ctx, id, id, tenant.IsolationVolume, tenant.WithBarrierAutoUnseal(true)); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if err := perTenantReg1.CloseAll(ctx); err != nil {
		t.Fatalf("CloseAll: %v", err)
	}
	reg2 := fx.rebuildRegistry(t)
	return fx, TenantInfraConfig{
		Ctx:          ctx,
		BarrierReg:   reg2,
		TenantSvc:    fx.svc,
		TenantLookup: fx.svc,
		VolumePool:   &qrdbpool.VolumePoolSubsystem{},
		Logger:       reattachTestLogger(),
	}
}

// TestBuildTenantInfra_UnderClusterSealingStartsNoTenantBeforeTheRecordIsRead
// pins boot ordering under cluster sealing: BuildTenantInfra runs before the
// composer can install the seal monitor -- the monitor seals through the
// registries BuildTenantInfra returns -- so the eager re-attach of an
// auto-unseal tenant would start a tenant whose record says sealed before this
// node has read it. With a cluster sealer wired, nothing starts at boot; the
// gated reconciler and first access, which both consult the record, start it.
func TestBuildTenantInfra_UnderClusterSealingStartsNoTenantBeforeTheRecordIsRead(t *testing.T) {
	_, cfg := restartedAutoUnsealVolumeTenant(t, "vsealed")
	cfg.ClusterSealer = clusterseal.NewDeferred()
	ti, err := BuildTenantInfra(cfg)
	if err != nil {
		t.Fatalf("BuildTenantInfra: %v", err)
	}
	t.Cleanup(func() { _ = ti.Hosts.CloseAll() })
	if ti.Hosts.Started("vsealed") {
		t.Fatal("an auto-unseal tenant's Host started at boot under cluster sealing, before this node read its seal record")
	}
	if _, err := cfg.BarrierReg.GetTenant("vsealed"); err != nil {
		t.Fatalf("the tenant's barrier was not restored, so nothing can start it later: %v", err)
	}
}

// TestBuildTenantInfra_WithoutClusterSealingReattachesEagerly is the control:
// a node with no cluster sealer keeps the eager re-attach that makes an
// auto-unseal tenant's data reachable as soon as the node serves.
func TestBuildTenantInfra_WithoutClusterSealingReattachesEagerly(t *testing.T) {
	_, cfg := restartedAutoUnsealVolumeTenant(t, "veager")
	ti, err := BuildTenantInfra(cfg)
	if err != nil {
		t.Fatalf("BuildTenantInfra: %v", err)
	}
	t.Cleanup(func() { _ = ti.Hosts.CloseAll() })
	if !ti.Hosts.Started("veager") {
		t.Fatal("an auto-unseal tenant's Host did not start at boot without cluster sealing")
	}
}
