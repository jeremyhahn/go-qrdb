// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Barrier-mode entitlement tests for the embedded KV tenant service.
//
// CheckBarrierMode decides which sealing strategy a tenant may put its data
// under. It gates two separate things through one entry point -- the shared
// system barrier (by quota) and dedicated modes (by entitlement) -- so each
// has to be exercised on its own, or a permissive answer on one path would be
// masked by a correct answer on the other.

package tenant

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// newKVBarrierService builds the embedded KV tenant service.
func newKVBarrierService(t *testing.T) TenantServicer {
	t.Helper()
	svc, err := NewKVTenantService(newMemKVStore(), slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}
	return svc
}

// TestCheckBarrierModeExemptsTheSystemTenant covers the first guard. The
// system tenant owns the barrier machinery itself, so gating it on its own
// entitlements would be circular.
func TestCheckBarrierModeExemptsTheSystemTenant(t *testing.T) {
	svc := newKVBarrierService(t)
	checker, ok := svc.(interface {
		CheckBarrierMode(context.Context, string, seal.StrategyID) error
	})
	if !ok {
		t.Fatal("kvTenantService does not expose CheckBarrierMode")
	}

	for _, mode := range []seal.StrategyID{
		seal.StrategySystemBarrier,
		seal.StrategyPassphrase,
		seal.StrategyShamir,
	} {
		if err := checker.CheckBarrierMode(context.Background(), SystemTenantID, mode); err != nil {
			t.Errorf("system tenant refused mode %q: %v", mode, err)
		}
	}
}

// TestCheckBarrierModeGatesTheSystemBarrierOnQuota covers the
// StrategySystemBarrier branch. The default quota of zero makes the shared
// barrier unavailable, which is the fail-closed default: a tenant that slipped
// onto the system barrier would put its data under a key it does not control.
func TestCheckBarrierModeGatesTheSystemBarrierOnQuota(t *testing.T) {
	ctx := context.Background()
	svc := newKVBarrierService(t)
	checker := svc.(interface {
		CheckBarrierMode(context.Context, string, seal.StrategyID) error
	})

	if _, err := svc.CreateTenant(ctx, "acme", "Acme", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// Default quota (0) => unavailable.
	err := checker.CheckBarrierMode(ctx, "acme", seal.StrategySystemBarrier)
	if err == nil {
		t.Fatal("the system barrier must be refused at the default quota")
	}
	var entErr *quota.EntitlementError
	if !errors.As(err, &entErr) {
		t.Fatalf("error = %v (%T), want *quota.EntitlementError", err, err)
	}
	if entErr.TenantID != "acme" {
		t.Errorf("TenantID = %q, want %q", entErr.TenantID, "acme")
	}
}

// TestCheckBarrierModeAllowsTheSystemBarrierWhenQuotaIsGranted is the other
// half: a non-zero quota makes the option available.
func TestCheckBarrierModeAllowsTheSystemBarrierWhenQuotaIsGranted(t *testing.T) {
	ctx := context.Background()
	svc := newKVBarrierService(t)
	checker := svc.(interface {
		CheckBarrierMode(context.Context, string, seal.StrategyID) error
	})

	if _, err := svc.CreateTenant(ctx, "acme", "Acme", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.UpdateQuotas(ctx, "acme", &quota.Quotas{SystemBarrierQuota: -1}); err != nil {
		t.Fatalf("UpdateQuotas: %v", err)
	}

	if err := checker.CheckBarrierMode(ctx, "acme", seal.StrategySystemBarrier); err != nil {
		t.Fatalf("an unlimited system-barrier quota must permit the mode: %v", err)
	}
}

// TestCheckBarrierModeRefusesAnUnknownTenant covers the lookup-failure branch
// on both paths. An unknown tenant must be a refusal, never a default-allow.
func TestCheckBarrierModeRefusesAnUnknownTenant(t *testing.T) {
	ctx := context.Background()
	svc := newKVBarrierService(t)
	checker := svc.(interface {
		CheckBarrierMode(context.Context, string, seal.StrategyID) error
	})

	for _, mode := range []seal.StrategyID{seal.StrategySystemBarrier, seal.StrategyPassphrase} {
		if err := checker.CheckBarrierMode(ctx, "ghost", mode); err == nil {
			t.Errorf("mode %q on an unknown tenant must fail", mode)
		}
	}
}

// TestCheckBarrierModeGatesDedicatedModesOnEntitlements covers the non-system
// branch, which routes through the tenant's entitlements rather than its
// quotas.
func TestCheckBarrierModeGatesDedicatedModesOnEntitlements(t *testing.T) {
	ctx := context.Background()
	svc := newKVBarrierService(t)
	checker := svc.(interface {
		CheckBarrierMode(context.Context, string, seal.StrategyID) error
	})

	if _, err := svc.CreateTenant(ctx, "acme", "Acme", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.UpdateEntitlements(ctx, "acme", &quota.Entitlements{
		AllowedBarrierModes: []string{string(seal.StrategyPassphrase)},
	}); err != nil {
		t.Fatalf("UpdateEntitlements: %v", err)
	}

	if err := checker.CheckBarrierMode(ctx, "acme", seal.StrategyPassphrase); err != nil {
		t.Fatalf("an explicitly allowed mode was refused: %v", err)
	}

	err := checker.CheckBarrierMode(ctx, "acme", seal.StrategyShamir)
	if err == nil {
		t.Fatal("a mode outside AllowedBarrierModes must be refused")
	}
	var entErr *quota.EntitlementError
	if !errors.As(err, &entErr) {
		t.Fatalf("error = %v (%T), want *quota.EntitlementError", err, err)
	}
	if entErr.Feature != "barrier_mode:"+string(seal.StrategyShamir) {
		t.Errorf("Feature = %q, want the refused mode", entErr.Feature)
	}
}
