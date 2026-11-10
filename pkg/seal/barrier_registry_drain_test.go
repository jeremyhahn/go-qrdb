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

package seal

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal/quiesce"
)

// newDrainRegistry builds a registry over an initialized system barrier
// configured with the given seal drain bound.
func newDrainRegistry(t *testing.T, drain time.Duration) *BarrierRegistry {
	t.Helper()
	system, err := NewStorageBarrier(testLogger(), NewMemoryBackend(), BarrierConfig{
		RootKeyPath:      "system/root-key",
		SealDrainTimeout: drain,
	}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewStorageBarrier (system): %v", err)
	}
	reg, err := NewBarrierRegistry(testLogger(), system)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	if err := system.Initialize(context.Background(), Credentials{}); err != nil {
		t.Fatalf("system Initialize: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	return reg
}

func TestTenantBarrierConfig_InheritsTheSystemDrainTimeout(t *testing.T) {
	reg := newDrainRegistry(t, 250*time.Millisecond)
	tb, err := reg.RegisterTenantDefault("t1")
	if err != nil {
		t.Fatalf("RegisterTenantDefault: %v", err)
	}
	if got := tb.Barrier().config.sealDrainTimeout(); got != 250*time.Millisecond {
		t.Fatalf("tenant drain timeout = %v; want the system's 250ms", got)
	}
}

func TestTenantBarrierConfig_DefaultDrainTimeoutStays(t *testing.T) {
	reg := newTestRegistry(t)
	t.Cleanup(func() { _ = reg.Close() })
	tb, err := reg.RegisterTenantDefault("t1")
	if err != nil {
		t.Fatalf("RegisterTenantDefault: %v", err)
	}
	if got := tb.Barrier().config.sealDrainTimeout(); got != DefaultSealDrainTimeout {
		t.Fatalf("tenant drain timeout = %v; want the default %v", got, DefaultSealDrainTimeout)
	}
}

// TestSealAll_DrainTimeoutTenantIsUnsealedByTheNodeUnseal proves a
// system-managed tenant whose drain runs out during a node seal, and so is
// sealed with an error, is still the node seal's to bring back.
func TestSealAll_DrainTimeoutTenantIsUnsealedByTheNodeUnseal(t *testing.T) {
	ctx := context.Background()
	reg := newDrainRegistry(t, time.Millisecond)
	if _, err := reg.RegisterTenant(ctx, "auto", NewMemoryBackend(), reg.tenantBarrierConfig("auto"), reg.systemBarrierStrategy()); err != nil {
		t.Fatalf("register auto: %v", err)
	}
	if err := reg.InitializeTenant(ctx, "auto", Credentials{}); err != nil {
		t.Fatalf("initialize auto: %v", err)
	}
	tb, err := reg.GetTenant("auto")
	if err != nil {
		t.Fatal(err)
	}
	lease := tb.Barrier().Quiesce().Register("stuck-store")
	if err := lease.Acquire(); err != nil {
		t.Fatal(err)
	}

	err = reg.SealAll()
	var timeout *quiesce.DrainTimeoutError
	if !errors.As(err, &timeout) || !slices.Equal(timeout.Stores, []string{"stuck-store"}) {
		t.Fatalf("SealAll = %v; want a DrainTimeoutError naming stuck-store", err)
	}
	if !tb.IsSealed() {
		t.Fatal("a drain timeout vetoed the tenant's seal")
	}
	if _, ok := reg.PendingTenantUnseals()["auto"]; !ok {
		t.Fatalf("PendingTenantUnseals = %v; want auto", reg.PendingTenantUnseals())
	}

	lease.Release()
	if err := reg.SystemBarrier().Unseal(ctx, Credentials{}); err != nil {
		t.Fatalf("system Unseal: %v", err)
	}
	if tb.IsSealed() {
		t.Fatal("the node unseal left the drain-timeout tenant sealed")
	}
}
