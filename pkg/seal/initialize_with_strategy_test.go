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
	"testing"
)

// TestInitializeWithStrategy_HonorsExplicitStrategy pins the seam that
// honors a tenant admin's explicit strategy choice: a barrier constructed
// with ONLY a passphrase strategy (the pre-registered shell shape) is
// initialized with the none strategy and empty credentials — exactly the
// combination the silent best-strategy fallback used to fail with "empty
// credentials for deferred-passphrase strategy".
func TestInitializeWithStrategy_HonorsExplicitStrategy(t *testing.T) {
	ctx := context.Background()
	b, err := NewStorageBarrier(testLogger(), NewMemoryBackend(), BarrierConfig{
		RootKeyPath: "test/root-key",
	}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}

	if err := b.InitializeWithStrategy(ctx, NewNoneStrategy(), Credentials{}); err != nil {
		t.Fatalf("InitializeWithStrategy(none): %v", err)
	}
	if b.IsSealed() {
		t.Fatal("barrier should be unsealed after initialization")
	}
	if got := b.ActiveStrategy(); got != StrategyNone {
		t.Fatalf("ActiveStrategy = %q, want %q", got, StrategyNone)
	}
}

// TestInitializeWithStrategy_RefusesInitialized verifies the
// already-initialized guard matches Initialize's.
func TestInitializeWithStrategy_RefusesInitialized(t *testing.T) {
	ctx := context.Background()
	b, err := NewStorageBarrier(testLogger(), NewMemoryBackend(), BarrierConfig{
		RootKeyPath: "test/root-key",
	}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := b.InitializeWithStrategy(ctx, NewNoneStrategy(), Credentials{}); err != nil {
		t.Fatalf("first InitializeWithStrategy: %v", err)
	}
	err = b.InitializeWithStrategy(ctx, NewNoneStrategy(), Credentials{})
	if !errors.Is(err, ErrAlreadyInitialized) {
		t.Fatalf("second InitializeWithStrategy = %v, want ErrAlreadyInitialized", err)
	}
}

// TestInitializeWithStrategy_NilStrategyFailsClosed verifies a nil strategy
// is refused rather than dereferenced.
func TestInitializeWithStrategy_NilStrategyFailsClosed(t *testing.T) {
	b, err := NewStorageBarrier(testLogger(), NewMemoryBackend(), BarrierConfig{
		RootKeyPath: "test/root-key",
	}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := b.InitializeWithStrategy(context.Background(), nil, Credentials{}); err == nil {
		t.Fatal("nil strategy must be refused")
	}
}

// TestInitializeTenantWithStrategy_RoundTrip verifies the registry-level
// wrapper initializes a registered tenant barrier with the exact strategy
// and that the tenant barrier serves data afterward.
func TestInitializeTenantWithStrategy_RoundTrip(t *testing.T) {
	ctx := context.Background()
	system, err := NewStorageBarrier(testLogger(), NewMemoryBackend(), BarrierConfig{
		RootKeyPath: "system/root-key",
	}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewStorageBarrier(system): %v", err)
	}
	if err := system.Initialize(ctx, MustCredentials([]byte("test-master-password"))); err != nil {
		t.Fatalf("system Initialize: %v", err)
	}
	reg, err := NewBarrierRegistry(testLogger(), system)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	if _, err := reg.RegisterTenant(ctx, "t1", NewMemoryBackend(), BarrierConfig{
		RootKeyPath: "tenant/t1/root-key",
	}, testSoftwareStrategy(t)); err != nil {
		t.Fatalf("RegisterTenant: %v", err)
	}

	if err := reg.InitializeTenantWithStrategy(ctx, "t1", NewNoneStrategy(), Credentials{}); err != nil {
		t.Fatalf("InitializeTenantWithStrategy: %v", err)
	}
	tb, err := reg.GetTenant("t1")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if tb.IsSealed() {
		t.Fatal("tenant barrier should be unsealed")
	}
	if got := tb.Barrier().ActiveStrategy(); got != StrategyNone {
		t.Fatalf("ActiveStrategy = %q, want %q", got, StrategyNone)
	}
}

// TestInitializeTenantWithStrategy_UnknownTenant verifies the wrapper fails
// for an unregistered tenant.
func TestInitializeTenantWithStrategy_UnknownTenant(t *testing.T) {
	system, err := NewStorageBarrier(testLogger(), NewMemoryBackend(), BarrierConfig{
		RootKeyPath: "system/root-key",
	}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewStorageBarrier(system): %v", err)
	}
	reg, err := NewBarrierRegistry(testLogger(), system)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	if err := reg.InitializeTenantWithStrategy(context.Background(), "ghost", NewNoneStrategy(), Credentials{}); err == nil {
		t.Fatal("unregistered tenant must be refused")
	}
}
