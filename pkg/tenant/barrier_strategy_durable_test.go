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

package tenant

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestTenantBarrierInitWithStrategy_DurableUnregisteredPath proves that when
// TenantBarrierInitWithStrategy is called for a tenant whose barrier is not
// yet registered, it takes the durable, strategy-honoring registration path
// (RegisterTenantPooledWithStrategy) rather than the old ephemeral
// in-memory fallback. We assert:
//   - a durable per-tenant Pebble was opened (proof the in-memory backend was
//     NOT used), and
//   - the requested strategy ("none") was applied to the tenant barrier.
func TestTenantBarrierInitWithStrategy_DurableUnregisteredPath(t *testing.T) {
	svc, reg := newTestService(t)
	ctx := context.Background()

	const tenantID = "durable-strategy"

	if _, err := svc.CreateTenant(ctx, tenantID, "Durable Strategy", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	// Prefix tenants do not pre-register a barrier; clear any registration
	// so TenantBarrierInitWithStrategy is forced down the unregistered path.
	if err := reg.UnregisterTenant(tenantID); err != nil && !errors.Is(err, seal.ErrTenantNotFound) {
		t.Fatalf("UnregisterTenant: %v", err)
	}

	// The per-tenant Pebble registry must hold no durable backend yet.
	perTenantReg := reg.PerTenantPebbleRegistry()
	if perTenantReg == nil {
		t.Fatal("test precondition: registry has no per-tenant Pebble registry wired")
	}
	if _, err := perTenantReg.Get(tenantID); err == nil {
		t.Fatal("test precondition: tenant unexpectedly already has a durable backend")
	}

	// Initialize with the "none" strategy via the unregistered path.
	if err := svc.TenantBarrierInitWithStrategy(ctx, tenantID, "", 0, 0, seal.StrategyNone); err != nil {
		t.Fatalf("TenantBarrierInitWithStrategy(none): %v", err)
	}

	// A durable per-tenant Pebble must now exist — proof the in-memory
	// fallback was NOT taken.
	storage, err := perTenantReg.Get(tenantID)
	if err != nil {
		t.Fatalf("perTenantReg.Get(%s): %v — expected durable backend, the in-memory fallback was taken", tenantID, err)
	}
	if storage == nil {
		t.Fatal("perTenantReg returned nil storage; expected durable backend")
	}

	// The requested strategy must be the one applied to the tenant barrier.
	tb, err := reg.GetTenant(tenantID)
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if _, ok := tb.Barrier().Strategies()[seal.StrategyNone]; !ok {
		t.Errorf("tenant barrier did not apply the requested StrategyNone")
	}

	st, err := reg.TenantStatus(tenantID)
	if err != nil {
		t.Fatalf("TenantStatus: %v", err)
	}
	if st.Sealed {
		t.Error("expected barrier unsealed after init")
	}
}

// TestTenantBarrierInitWithStrategy_IdempotentRegistrationOnRegistered proves
// that the durable registration step is idempotent: a second invocation on an
// already-registered tenant must NOT re-register the barrier (no
// register-phase BarrierError, and the same durable backend handle is
// reused). Re-initializing an already-initialized barrier is a distinct,
// legitimate error that the registration idempotence guard does not mask.
func TestTenantBarrierInitWithStrategy_IdempotentRegistrationOnRegistered(t *testing.T) {
	svc, reg := newTestService(t)
	ctx := context.Background()

	const tenantID = "idempotent-strategy"

	if _, err := svc.CreateTenant(ctx, tenantID, "Idempotent Strategy", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if err := reg.UnregisterTenant(tenantID); err != nil && !errors.Is(err, seal.ErrTenantNotFound) {
		t.Fatalf("UnregisterTenant: %v", err)
	}

	// First call registers the durable barrier and initializes it.
	if err := svc.TenantBarrierInitWithStrategy(ctx, tenantID, "", 0, 0, seal.StrategyNone); err != nil {
		t.Fatalf("TenantBarrierInitWithStrategy(first): %v", err)
	}

	perTenantReg := reg.PerTenantPebbleRegistry()
	first, err := perTenantReg.Get(tenantID)
	if err != nil {
		t.Fatalf("perTenantReg.Get after first init: %v", err)
	}

	// Second call must NOT take the registration branch. The barrier is
	// already registered, so the durable backend is reused as-is. Re-init
	// of an already-initialized barrier is a separate, expected error; what
	// must NOT happen is a register-phase error or a swapped backend.
	secondErr := svc.TenantBarrierInitWithStrategy(ctx, tenantID, "", 0, 0, seal.StrategyNone)
	var barrierErr *BarrierError
	if errors.As(secondErr, &barrierErr) && barrierErr.Op == "register" {
		t.Fatalf("second call re-registered the barrier (register-phase error): %v", secondErr)
	}

	second, err := perTenantReg.Get(tenantID)
	if err != nil {
		t.Fatalf("perTenantReg.Get after second call: %v", err)
	}
	if first != second {
		t.Error("second call re-registered a new durable backend; expected the existing one to be reused")
	}

	// The tenant remains registered exactly once.
	ids := reg.ListTenants()
	count := 0
	for _, id := range ids {
		if id == tenantID {
			count++
		}
	}
	if count != 1 {
		t.Errorf("tenant registered %d times, want exactly 1", count)
	}
}
