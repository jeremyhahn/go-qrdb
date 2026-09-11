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
	"fmt"
	"sync"
	"testing"

	quicraftShamir "github.com/jeremyhahn/go-quicraft/pkg/crypto/shamir"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// tenantCaptureDist is a test-only CeremonyDistributor that captures generated
// Shamir shares so tests can later call TenantBarrierUnsealShare.
type tenantCaptureDist struct {
	mu     sync.Mutex
	shares []string
}

func (c *tenantCaptureDist) SealAndDistribute(_ context.Context, secret []byte, threshold, total int) (int, error) {
	raw, err := quicraftShamir.Split(secret, threshold, total)
	if err != nil {
		return 0, fmt.Errorf("tenantCaptureDist: split failed: %w", err)
	}
	c.mu.Lock()
	c.shares = make([]string, len(raw))
	for i, s := range raw {
		c.shares[i] = s.Value
	}
	c.mu.Unlock()
	return len(raw), nil
}

func (c *tenantCaptureDist) RekeyAndDistribute(ctx context.Context, newSecret []byte, newThreshold, newTotal int) (int, error) {
	c.mu.Lock()
	c.shares = nil
	c.mu.Unlock()
	return c.SealAndDistribute(ctx, newSecret, newThreshold, newTotal)
}

func (c *tenantCaptureDist) Shares() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shares == nil {
		return nil
	}
	out := make([]string, len(c.shares))
	copy(out, c.shares)
	return out
}

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
	if _, err := svc.TenantBarrierInitWithStrategy(ctx, tenantID, []byte(""), 0, 0, seal.StrategyNone); err != nil {
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
	if _, err := svc.TenantBarrierInitWithStrategy(ctx, tenantID, []byte(""), 0, 0, seal.StrategyNone); err != nil {
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
	_, secondErr := svc.TenantBarrierInitWithStrategy(ctx, tenantID, []byte(""), 0, 0, seal.StrategyNone)
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

// TestTenantBarrierShamir_InitReturnsShareCountAndUnseals proves the per-tenant
// Shamir vertical at the service layer: init distributes the M-of-N shares to
// enrolled officers (captured here via a test distributor), and the barrier can
// be sealed and then unsealed by submitting a quorum of those shares via
// TenantBarrierUnsealShare.
func TestTenantBarrierShamir_InitReturnsShareCountAndUnseals(t *testing.T) {
	// Wire a capture distributor so the test can retrieve generated shares.
	dist := &tenantCaptureDist{}
	store := seal.NewMemoryBackend()
	reg := newTestBarrierRegistry(t)
	svc, err := NewTenantService(store, reg, testLogger(), WithTenantDistributor(dist))
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	ctx := context.Background()

	const tenantID = "shamir-tenant"
	if _, err := svc.CreateTenant(ctx, tenantID, "Shamir Tenant", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if err := reg.UnregisterTenant(tenantID); err != nil && !errors.Is(err, seal.ErrTenantNotFound) {
		t.Fatalf("UnregisterTenant: %v", err)
	}

	const threshold, total = 2, 3
	res, err := svc.TenantBarrierInitWithStrategy(ctx, tenantID, []byte(""), threshold, total, seal.StrategyShamir)
	if err != nil {
		t.Fatalf("TenantBarrierInitWithStrategy(shamir): %v", err)
	}
	if res == nil || res.ShareCount != total || res.Threshold != threshold || res.TotalShares != total {
		t.Fatalf("unexpected shamir init result: %+v", res)
	}
	if res.Strategy != string(seal.StrategyShamir) {
		t.Fatalf("expected strategy %q, got %q", seal.StrategyShamir, res.Strategy)
	}

	// Retrieve the captured shares from the distributor.
	shares := dist.Shares()
	if len(shares) != total {
		t.Fatalf("distributor captured %d shares, want %d", len(shares), total)
	}

	// Seal the barrier directly via the registry to bypass the service-layer
	// Shamir quorum gate (which is correct behavior; this test only exercises
	// the unseal-share path, not the seal-vote flow).
	if err := reg.SealTenant(tenantID); err != nil {
		t.Fatalf("reg.SealTenant: %v", err)
	}

	// First share is below threshold.
	prog, err := svc.TenantBarrierUnsealShare(ctx, tenantID, shares[0])
	if err != nil {
		t.Fatalf("TenantBarrierUnsealShare(first): %v", err)
	}
	if prog.Complete || !prog.Sealed {
		t.Fatalf("expected incomplete/sealed after one share, got %+v", prog)
	}

	// Second share meets the 2-of-3 threshold and unseals.
	prog, err = svc.TenantBarrierUnsealShare(ctx, tenantID, shares[1])
	if err != nil {
		t.Fatalf("TenantBarrierUnsealShare(second): %v", err)
	}
	if !prog.Complete || prog.Sealed {
		t.Fatalf("expected complete/unsealed after quorum, got %+v", prog)
	}

	st, err := svc.TenantBarrierStatus(ctx, tenantID)
	if err != nil {
		t.Fatalf("TenantBarrierStatus: %v", err)
	}
	if st.Sealed {
		t.Error("expected barrier unsealed after quorum reached")
	}
	if st.ShamirThreshold != threshold || st.ShamirTotal != total {
		t.Errorf("status shamir params = %d/%d, want %d/%d", st.ShamirThreshold, st.ShamirTotal, threshold, total)
	}
}

// TestTenantBarrierUnsealShare_EmptyShareRejected verifies an empty share is
// rejected with ErrEmptyShare.
func TestTenantBarrierUnsealShare_EmptyShareRejected(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	const tenantID = "shamir-empty"
	if _, err := svc.CreateTenant(ctx, tenantID, "Shamir Empty", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	if _, err := svc.TenantBarrierUnsealShare(ctx, tenantID, ""); !errors.Is(err, ErrEmptyShare) {
		t.Fatalf("expected ErrEmptyShare, got %v", err)
	}
}
