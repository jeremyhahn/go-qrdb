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

package lifecycle_test

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// TestBuildServices_TenantServiceHasTheNodeKeyLogProbe: the node-key log
// probe is wired where the tenant service and the database manager are
// built, so every composer's tenant service can verify a strict
// tightening before the first floor write. A raft tenant with no
// databases, its home store attached, tightens to strict; without the
// probe the tightening fails closed with ErrNodeKeyLogsUnverifiable.
func TestBuildServices_TenantServiceHasTheNodeKeyLogProbe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	host := newLocalHost()
	defer func() { _ = host.Close() }()
	hub := watch.NewHub()
	defer hub.Close()
	barrier, _, credSvc := newTestBarrier(t)
	registry := newPooledRegistry(t, barrier)

	node, err := lifecycle.BuildServices(ctx, lifecycle.Infra{
		Host:              host,
		WatchHub:          hub,
		DataDir:           t.TempDir(),
		SealBarrier:       barrier,
		BarrierRegistry:   registry,
		CredentialCoreSvc: credSvc,
		DefaultTenantEntitlements: &quota.Entitlements{
			AllowedIsolationModes: []string{quota.IsolationModePrefix, quota.IsolationModeRaft},
			AllowDedicatedRaft:    true,
			AllowedVolumePools:    []string{"default"},
		},
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(ctx) }()
	if !lifecycle.AttachLocalTenantHomes(node, registry, nil) {
		t.Fatal("AttachLocalTenantHomes = false, want the tenant home stores attached")
	}

	if _, err := node.Tenant.CreateTenant(ctx, "acme", "acme", tenant.IsolationRaft); err != nil {
		t.Fatalf("CreateTenant(acme): %v", err)
	}
	if err := registry.InitializeTenant(ctx, "acme", seal.Credentials{}); err != nil {
		t.Fatalf("InitializeTenant(acme): %v", err)
	}
	if _, err := node.Tenant.UpdateTenant(ctx, "acme", tenant.TenantPatch{IsolationPolicy: tenant.TenantIsolationPolicyStrict}); err != nil {
		t.Fatalf("UpdateTenant(strict) of a raft tenant with no databases = %v; want the node-key log probe wired", err)
	}
	info, err := node.Tenant.GetTenant(ctx, "acme")
	if err != nil || info == nil || info.IsolationPolicy != tenant.TenantIsolationPolicyStrict {
		t.Fatalf("tenant after the tightening = (%+v, %v); want strict", info, err)
	}
}
