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

package seal_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestRegisterTenantPooledWithStrategy_DurableBackend verifies that the
// strategy-honoring pooled registration path uses the SAME durable
// Pebble-pool backend as RegisterTenantPooled — a real on-disk store, not
// an ephemeral in-memory backend. We assert the per-tenant Pebble was
// opened at the resolved volume-pool path.
func TestRegisterTenantPooledWithStrategy_DurableBackend(t *testing.T) {
	reg, roots, perTenantReg := newPooledRegistry(t)
	ctx := context.Background()

	none := seal.NewNoneStrategy()
	tb, err := reg.RegisterTenantPooledWithStrategy(ctx, "alpha", none)
	if err != nil {
		t.Fatalf("RegisterTenantPooledWithStrategy(alpha): %v", err)
	}
	if tb == nil {
		t.Fatal("expected non-nil TenantBarrier")
	}

	// The per-tenant Pebble registry must hold a durable backend for the
	// tenant — proof the in-memory fallback was NOT taken.
	storage, err := perTenantReg.Get("alpha")
	if err != nil {
		t.Fatalf("perTenantReg.Get(alpha): %v", err)
	}
	if storage == nil {
		t.Fatal("perTenantReg returned nil storage for alpha; expected durable backend")
	}

	// The Pebble directory must exist on disk at the picked pool root.
	expected := filepath.Join(roots[0], "alpha")
	assertPebbleAtPath(t, expected)
}

// TestRegisterTenantPooledWithStrategy_AppliesGivenStrategy verifies that
// the tenant barrier seals with the SPECIFIED strategy, distinct from the
// system barrier's posture. The system barrier in newPooledRegistry uses a
// passphrase (SoftwareStrategy) posture; the tenant is registered with the
// "none" strategy, so the two must differ.
func TestRegisterTenantPooledWithStrategy_AppliesGivenStrategy(t *testing.T) {
	reg, _, _ := newPooledRegistry(t)
	ctx := context.Background()

	none := seal.NewNoneStrategy()
	tb, err := reg.RegisterTenantPooledWithStrategy(ctx, "alpha", none)
	if err != nil {
		t.Fatalf("RegisterTenantPooledWithStrategy(alpha): %v", err)
	}

	tenantStrategies := tb.Barrier().Strategies()
	if _, ok := tenantStrategies[seal.StrategyNone]; !ok {
		t.Errorf("tenant barrier missing the specified StrategyNone; have %v", keysOf(tenantStrategies))
	}

	// The system barrier's posture must be the passphrase strategy — proof
	// the tenant did NOT inherit the system posture.
	systemStrategies := reg.SystemBarrier().Strategies()
	if _, ok := systemStrategies[seal.StrategyPassphrase]; !ok {
		t.Fatalf("test precondition: system barrier expected StrategyPassphrase; have %v", keysOf(systemStrategies))
	}
	if _, ok := tenantStrategies[seal.StrategyPassphrase]; ok {
		t.Error("tenant barrier unexpectedly inherited the system's StrategyPassphrase posture")
	}
}

// TestRegisterTenantPooled_InheritsSystemStrategy is the inheritance-default
// counterpart: when no strategy is supplied, the pooled tenant inherits the
// system barrier's posture. This guards the CreateTenant default path.
func TestRegisterTenantPooled_InheritsSystemStrategy(t *testing.T) {
	reg, _, _ := newPooledRegistry(t)
	ctx := context.Background()

	tb, err := reg.RegisterTenantPooled(ctx, "alpha")
	if err != nil {
		t.Fatalf("RegisterTenantPooled(alpha): %v", err)
	}

	tenantStrategies := tb.Barrier().Strategies()
	if _, ok := tenantStrategies[seal.StrategyPassphrase]; !ok {
		t.Errorf("pooled tenant did not inherit system StrategyPassphrase; have %v", keysOf(tenantStrategies))
	}
}

// TestRegisterTenantPooledShamir_InitUnsealRoundTrip exercises the full
// per-tenant Shamir vertical: register a Shamir tenant barrier, wire a
// CaptureDistributor, initialize (shares sealed to distributor; none returned
// in result), seal via quorum vote, then unseal by submitting a quorum of
// shares one at a time. The barrier must report incomplete progress until the
// threshold is met, then unseal.
func TestRegisterTenantPooledShamir_InitUnsealRoundTrip(t *testing.T) {
	reg, _, _ := newPooledRegistry(t)
	ctx := context.Background()

	const threshold, total = 2, 3
	tb, err := reg.RegisterTenantPooledShamir(ctx, "alpha", threshold, total, 0)
	if err != nil {
		t.Fatalf("RegisterTenantPooledShamir(alpha): %v", err)
	}
	if _, ok := tb.Barrier().Strategies()[seal.StrategyShamir]; !ok {
		t.Fatalf("tenant barrier missing StrategyShamir; have %v", keysOf(tb.Barrier().Strategies()))
	}

	// Wire a CaptureDistributor so InitializeTenantShamir can seal shares.
	dist := seal.NewCaptureDistributor()
	tb.Barrier().SetCeremonyDistributor(dist)

	res, err := reg.InitializeTenantShamir(ctx, "alpha", seal.Credentials{})
	if err != nil {
		t.Fatalf("InitializeTenantShamir(alpha): %v", err)
	}
	if res.Threshold != threshold || res.TotalShares != total {
		t.Fatalf("unexpected init result: threshold=%d total=%d", res.Threshold, res.TotalShares)
	}
	if res.ShareCount != total {
		t.Fatalf("unexpected ShareCount: got %d, want %d", res.ShareCount, total)
	}
	shares := dist.Shares()
	if len(shares) != total {
		t.Fatalf("expected %d captured shares, got %d", total, len(shares))
	}

	// Initialization leaves the barrier unsealed; seal it so the quorum unseal
	// path is exercised from a sealed state.
	if err := reg.SealTenant("alpha"); err != nil {
		t.Fatalf("SealTenant(alpha): %v", err)
	}
	if st, _ := reg.TenantStatus("alpha"); st == nil || !st.Sealed {
		t.Fatal("expected tenant barrier to be sealed after SealTenant")
	}

	// First share: below threshold, must report incomplete and stay sealed.
	prog, err := reg.UnsealTenantWithShare(ctx, "alpha", shares[0])
	if err != nil {
		t.Fatalf("UnsealTenantWithShare(first): %v", err)
	}
	if prog.Complete {
		t.Fatal("quorum reported complete after a single share")
	}

	// Second share meets the 2-of-3 threshold and unseals the barrier.
	prog, err = reg.UnsealTenantWithShare(ctx, "alpha", shares[1])
	if err != nil {
		t.Fatalf("UnsealTenantWithShare(second): %v", err)
	}
	if !prog.Complete {
		t.Fatal("quorum not complete after reaching the threshold")
	}
	if st, _ := reg.TenantStatus("alpha"); st == nil || st.Sealed {
		t.Fatal("expected tenant barrier to be unsealed after quorum reached")
	}
}

// TestRegisterTenantPooledShamir_InvalidThreshold verifies that an invalid
// threshold/total pair is rejected at registration rather than silently
// accepted.
func TestRegisterTenantPooledShamir_InvalidThreshold(t *testing.T) {
	reg, _, _ := newPooledRegistry(t)
	ctx := context.Background()

	if _, err := reg.RegisterTenantPooledShamir(ctx, "alpha", 0, 0, 0); err == nil {
		t.Fatal("expected error for invalid 0-of-0 Shamir threshold")
	}
}

// TestRegisterTenantPooledWithStrategy_NoPoolConfigured verifies the error
// path: without a configured volume pool, the durable flow surfaces
// ErrPoolNotConfigured rather than silently falling back to memory.
func TestRegisterTenantPooledWithStrategy_NoPoolConfigured(t *testing.T) {
	system := newPoolSystemBarrier(t)
	reg, err := seal.NewBarrierRegistry(poolTestLogger(), system)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}

	_, err = reg.RegisterTenantPooledWithStrategy(context.Background(), "alpha", seal.NewNoneStrategy())
	if !errors.Is(err, seal.ErrPoolNotConfigured) {
		t.Errorf("RegisterTenantPooledWithStrategy without pool: got %v, want ErrPoolNotConfigured", err)
	}
}

// TestRegisterTenantPooledSystemManaged_RegistrationSucceeds verifies that
// RegisterTenantPooledSystemManaged opens a pooled per-tenant Pebble and
// registers the tenant with a SystemBarrierStrategy.
func TestRegisterTenantPooledSystemManaged_RegistrationSucceeds(t *testing.T) {
	ctx := context.Background()
	reg, _, _ := newPooledRegistry(t)

	// Unseal the system barrier so the SystemBarrierStrategy is functional.
	if err := reg.SystemBarrier().Initialize(ctx, seal.Credentials{Secret: []byte("pool-test-pass")}); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	tb, err := reg.RegisterTenantPooledSystemManaged(ctx, "sys-mgd-tenant")
	if err != nil {
		t.Fatalf("RegisterTenantPooledSystemManaged: %v", err)
	}
	if tb == nil {
		t.Fatal("expected non-nil TenantBarrier")
	}

	// The registered barrier should be retrievable.
	if got, gerr := reg.GetTenant("sys-mgd-tenant"); gerr != nil || got == nil {
		t.Fatalf("GetTenant after RegisterTenantPooledSystemManaged: %v", gerr)
	}
}

// TestRegisterTenantPooledSystemManaged_NoPool verifies that the function
// propagates ErrPoolNotConfigured when no volume pool is attached.
func TestRegisterTenantPooledSystemManaged_NoPool(t *testing.T) {
	system := newPoolSystemBarrier(t)
	reg, err := seal.NewBarrierRegistry(poolTestLogger(), system)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}

	_, err = reg.RegisterTenantPooledSystemManaged(context.Background(), "sys-mgd-nopool")
	if !errors.Is(err, seal.ErrPoolNotConfigured) {
		t.Errorf("RegisterTenantPooledSystemManaged without pool: got %v, want ErrPoolNotConfigured", err)
	}
}

// keysOf returns the StrategyID keys of a strategies map for diagnostics.
func keysOf(m map[seal.StrategyID]seal.SealingStrategy) []seal.StrategyID {
	ids := make([]seal.StrategyID, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	return ids
}
