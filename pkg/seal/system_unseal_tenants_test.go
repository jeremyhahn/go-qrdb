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

package seal

import (
	"context"
	"testing"
)

// registryWithBothTenantKinds returns an unsealed registry holding one
// system-managed (auto-unseal) tenant and one self-managed tenant, both
// initialized and unsealed.
func registryWithBothTenantKinds(t *testing.T) *BarrierRegistry {
	t.Helper()
	ctx := context.Background()
	reg := newTestRegistry(t)
	if err := reg.SystemBarrier().Initialize(ctx, Credentials{}); err != nil {
		t.Fatalf("system Initialize: %v", err)
	}
	if _, err := reg.RegisterTenant(ctx, "auto", NewMemoryBackend(), tenantBarrierConfig("auto"), reg.systemBarrierStrategy()); err != nil {
		t.Fatalf("register auto: %v", err)
	}
	if err := reg.InitializeTenant(ctx, "auto", Credentials{}); err != nil {
		t.Fatalf("initialize auto: %v", err)
	}
	if _, err := reg.RegisterTenantDefault("self"); err != nil {
		t.Fatalf("register self: %v", err)
	}
	if err := reg.InitializeTenant(ctx, "self", Credentials{}); err != nil {
		t.Fatalf("initialize self: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	return reg
}

func tenantSealed(t *testing.T, reg *BarrierRegistry, id string) bool {
	t.Helper()
	tb, err := reg.GetTenant(id)
	if err != nil {
		t.Fatalf("GetTenant(%s): %v", id, err)
	}
	return tb.IsSealed()
}

// TestSystemUnseal_BringsBackSystemManagedTenantsOnly pins the unseal order of
// a whole-node seal: the system barrier unseals first, the system-managed
// (auto-unseal) tenants -- whose keys the system barrier wraps -- come back
// with it, and a self-managed tenant stays sealed until its own admin
// unseals it.
func TestSystemUnseal_BringsBackSystemManagedTenantsOnly(t *testing.T) {
	ctx := context.Background()
	reg := registryWithBothTenantKinds(t)
	if err := reg.SealAll(); err != nil {
		t.Fatalf("SealAll: %v", err)
	}
	if !tenantSealed(t, reg, "auto") || !tenantSealed(t, reg, "self") || !reg.SystemBarrier().IsSealed() {
		t.Fatal("precondition: SealAll left a barrier unsealed")
	}
	if err := reg.SystemBarrier().Unseal(ctx, Credentials{}); err != nil {
		t.Fatalf("system Unseal: %v", err)
	}
	if tenantSealed(t, reg, "auto") {
		t.Fatal("a system-managed tenant stayed sealed after the system barrier unsealed")
	}
	if !tenantSealed(t, reg, "self") {
		t.Fatal("a self-managed tenant was unsealed by the system barrier's unseal")
	}
}

// TestOnUnsealed_RunsOnEveryUnseal: an observer runs on each transition to
// unsealed, with the unsealing operation's context, and not on a seal.
func TestOnUnsealed_RunsOnEveryUnseal(t *testing.T) {
	ctx := context.WithValue(context.Background(), onUnsealedKey{}, "op")
	b := newSystemBarrier(t)
	var calls []any
	b.OnUnsealed(func(c context.Context) { calls = append(calls, c.Value(onUnsealedKey{})) })
	if err := b.Initialize(ctx, Credentials{}); err != nil {
		t.Fatal(err)
	}
	if err := b.Seal(); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("observer calls after initialize and seal = %d, want 1", len(calls))
	}
	if err := b.Unseal(ctx, Credentials{}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[1] != "op" {
		t.Fatalf("observer calls = %v, want two with the unseal's context", calls)
	}
}

type onUnsealedKey struct{}

// TestSystemUnseal_LeavesALocallySealedTenantSealed: a system-managed tenant
// its own admin sealed before the node seal was not moved by the node seal,
// so the node unseal does not unseal it.
func TestSystemUnseal_LeavesALocallySealedTenantSealed(t *testing.T) {
	ctx := context.Background()
	reg := registryWithBothTenantKinds(t)
	if err := reg.SealTenant("auto"); err != nil {
		t.Fatal(err)
	}
	if err := reg.SealAll(); err != nil {
		t.Fatal(err)
	}
	if err := reg.SystemBarrier().Unseal(ctx, Credentials{}); err != nil {
		t.Fatal(err)
	}
	if !tenantSealed(t, reg, "auto") {
		t.Fatal("a tenant its admin sealed before the node seal was unsealed by the node unseal")
	}
}

// TestSystemUnseal_LeavesATenantSealedSinceTheNodeSealSealed: a tenant the
// node seal moved, but that was sealed explicitly afterwards -- its own
// admin, or its cluster record applied while the node was sealed -- is no
// longer the node seal's to undo.
func TestSystemUnseal_LeavesATenantSealedSinceTheNodeSealSealed(t *testing.T) {
	ctx := context.Background()
	reg := registryWithBothTenantKinds(t)
	if err := reg.SealAll(); err != nil {
		t.Fatal(err)
	}
	if err := reg.SealTenant("auto"); err != nil {
		t.Fatal(err)
	}
	if err := reg.SystemBarrier().Unseal(ctx, Credentials{}); err != nil {
		t.Fatal(err)
	}
	if !tenantSealed(t, reg, "auto") {
		t.Fatal("a tenant sealed explicitly while the node was sealed was unsealed by the node unseal")
	}
}

// TestSystemUnseal_HonoursTheTenantUnsealGate: a tenant the gate refuses --
// its cluster record is sealed, or cannot be read -- stays sealed, and is no
// longer pending.
func TestSystemUnseal_HonoursTheTenantUnsealGate(t *testing.T) {
	ctx := context.Background()
	reg := registryWithBothTenantKinds(t)
	reg.Attach(WithTenantUnsealGate(func(id string) bool { return id == "auto" }))
	if err := reg.SealAll(); err != nil {
		t.Fatal(err)
	}
	if err := reg.SystemBarrier().Unseal(ctx, Credentials{}); err != nil {
		t.Fatal(err)
	}
	if !tenantSealed(t, reg, "auto") {
		t.Fatal("a tenant whose record refuses it was unsealed by the node unseal")
	}
	if len(reg.PendingTenantUnseals()) != 0 {
		t.Fatalf("pending after the gate refused: %v", reg.PendingTenantUnseals())
	}
}
