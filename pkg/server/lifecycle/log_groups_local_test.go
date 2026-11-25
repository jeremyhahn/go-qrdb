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

package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// registerHomeTenant registers tenantID's barrier, over its own memory
// backend, in reg.
func registerHomeTenant(t *testing.T, reg *seal.BarrierRegistry, tenantID string) *seal.TenantBarrier {
	t.Helper()
	tb, err := reg.RegisterTenant(context.Background(), tenantID, seal.NewMemoryBackend(), seal.BarrierConfig{}, seal.NewNoneStrategy())
	if err != nil {
		t.Fatalf("RegisterTenant: %v", err)
	}
	return tb
}

func TestAttachLocalTenantHomes_AttachesTheStandaloneLogStore(t *testing.T) {
	ctx := context.Background()
	reg := newHomeRegistry(t)
	tb := registerHomeTenant(t, reg, "vault")
	n := &Node{StoreResolver: &homeRouterResolver{}, logGroups: &logGroups{host: &groupHost{}}}
	var unavailable *service.BarrierStoreUnavailableError
	if _, _, err := n.logGroups.TenantLogStore(ctx, "vault"); !errors.As(err, &unavailable) || unavailable.TenantID != "vault" {
		t.Fatalf("TenantLogStore before the attach = %v, want BarrierStoreUnavailableError", err)
	}
	if !AttachLocalTenantHomes(n, reg, nil) {
		t.Fatal("AttachLocalTenantHomes = false, want true")
	}
	barrier, kv, err := n.logGroups.TenantLogStore(ctx, "vault")
	if err != nil || kv == nil {
		t.Fatalf("TenantLogStore = (%v, %v, %v), want the tenant barrier's store", barrier, kv, err)
	}
	if got, ok := barrier.(*seal.TenantBarrier); !ok || got != tb {
		t.Fatalf("TenantLogStore barrier = %v, want the registry's barrier for vault", barrier)
	}
	if _, _, err := n.logGroups.TenantLogStore(ctx, "ghost"); err == nil {
		t.Fatal("TenantLogStore for a tenant with no barrier here must fail")
	}

	// Home groups win: once they are attached the standalone store is not
	// served.
	n.logGroups.attach(&fakeGroupServices{})
	if _, _, err := n.logGroups.TenantLogStore(ctx, "vault"); !errors.As(err, &unavailable) {
		t.Fatalf("TenantLogStore with home groups = %v, want BarrierStoreUnavailableError", err)
	}
}

// refusingHomeGate is a tenant.HomeAdmission that refuses every operation,
// naming the tenant it was asked to admit.
type refusingHomeGate struct{ asked []string }

var errHomeGateFenced = errors.New("home gate fenced")

func (g *refusingHomeGate) AdmitHome(tenantID string) (func(), error) {
	g.asked = append(g.asked, tenantID)
	return nil, errHomeGateFenced
}

// TestAttachLocalTenantHomes_LogStoreAdmitsThroughTheHomeGate pins that the
// standalone log store admits its operations through the home-store gate
// AttachLocalTenantHomes is given, and admits every one without a gate.
func TestAttachLocalTenantHomes_LogStoreAdmitsThroughTheHomeGate(t *testing.T) {
	reg := newHomeRegistry(t)
	registerHomeTenant(t, reg, "vault")
	n := &Node{StoreResolver: &homeRouterResolver{}, logGroups: &logGroups{host: &groupHost{}}}
	release, err := n.logGroups.AdmitTenantLog("vault")
	if err != nil {
		t.Fatalf("AdmitTenantLog without a gate = %v, want admitted", err)
	}
	release()
	gate := &refusingHomeGate{}
	if !AttachLocalTenantHomes(n, reg, gate) {
		t.Fatal("AttachLocalTenantHomes = false, want true")
	}
	if _, err := n.logGroups.AdmitTenantLog("vault"); !errors.Is(err, errHomeGateFenced) {
		t.Fatalf("AdmitTenantLog through a fenced gate = %v, want the gate's error", err)
	}
	if len(gate.asked) != 1 || gate.asked[0] != "vault" {
		t.Fatalf("gate asked for %v, want [vault]", gate.asked)
	}
}

func TestAttachLocalTenantHomes_RefusedAttachesNoLogStore(t *testing.T) {
	reg := newHomeRegistry(t)
	registerHomeTenant(t, reg, "vault")
	n := &Node{StoreResolver: plainResolver{}, logGroups: &logGroups{host: &groupHost{}}}
	if AttachLocalTenantHomes(n, reg, nil) {
		t.Fatal("AttachLocalTenantHomes = true, want false")
	}
	var unavailable *service.BarrierStoreUnavailableError
	if _, _, err := n.logGroups.TenantLogStore(context.Background(), "vault"); !errors.As(err, &unavailable) {
		t.Fatalf("TenantLogStore after a refused attach = %v, want BarrierStoreUnavailableError", err)
	}
}
