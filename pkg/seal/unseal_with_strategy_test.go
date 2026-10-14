package seal

import (
	"context"
	"errors"
	"testing"
)

// unsealWithStrategyFixture initializes a barrier over backend with a
// tenant-chosen software strategy, writes one value, and returns a fresh,
// sealed barrier over the same backend whose shell carries a same-ID software
// strategy keyed differently -- the shape a node restart produces when the
// shell is registered with the node's default strategy set.
func unsealWithStrategyFixture(t *testing.T, chosen SealingStrategy) *StorageBarrier {
	t.Helper()
	ctx := context.Background()
	backend := NewMemoryBackend()
	cfg := BarrierConfig{RootKeyPath: "tenant/t1/root-key"}

	first, err := NewStorageBarrier(testLogger(), backend, cfg, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := first.InitializeWithStrategy(ctx, chosen, Credentials{}); err != nil {
		t.Fatalf("InitializeWithStrategy: %v", err)
	}
	if err := first.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := first.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	restarted, err := NewStorageBarrier(testLogger(), backend, cfg, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewStorageBarrier(restart): %v", err)
	}
	return restarted
}

// tenantSoftwareStrategy returns a software strategy keyed differently from
// testSoftwareStrategy but registered under the same strategy ID.
func tenantSoftwareStrategy(t *testing.T) *SoftwareStrategy {
	t.Helper()
	s, err := NewSoftwareStrategy(t.Context(), []byte("tenant-provider-password"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	return s
}

// TestUnsealWithStrategy_UsesExactlyTheGivenStrategy proves the restarted
// barrier unseals with the caller's strategy and serves the data written
// before the restart, while a plain Unseal -- which resolves the shell's
// same-ID strategy -- cannot.
func TestUnsealWithStrategy_UsesExactlyTheGivenStrategy(t *testing.T) {
	ctx := context.Background()
	chosen := tenantSoftwareStrategy(t)
	b := unsealWithStrategyFixture(t, chosen)

	if err := b.Unseal(ctx, Credentials{}); err == nil {
		t.Fatal("Unseal with the shell's same-ID strategy succeeded; it must not open a root key another strategy sealed")
	}
	if !b.IsSealed() {
		t.Fatal("barrier unsealed through the shell's same-ID strategy")
	}

	if err := b.UnsealWithStrategy(ctx, chosen, Credentials{}); err != nil {
		t.Fatalf("UnsealWithStrategy: %v", err)
	}
	got, err := b.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "v" {
		t.Fatalf("Get = %q, want %q", got, "v")
	}
	if b.ActiveStrategy() != chosen.ID() {
		t.Fatalf("ActiveStrategy = %q, want %q", b.ActiveStrategy(), chosen.ID())
	}
}

// TestUnsealWithStrategy_ReplacesShellRegistration verifies a successful
// UnsealWithStrategy leaves the given strategy registered, so a later plain
// Unseal after a seal resolves it rather than the shell's.
func TestUnsealWithStrategy_ReplacesShellRegistration(t *testing.T) {
	ctx := context.Background()
	chosen := tenantSoftwareStrategy(t)
	b := unsealWithStrategyFixture(t, chosen)

	if err := b.UnsealWithStrategy(ctx, chosen, Credentials{}); err != nil {
		t.Fatalf("UnsealWithStrategy: %v", err)
	}
	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if err := b.Unseal(ctx, Credentials{}); err != nil {
		t.Fatalf("Unseal after UnsealWithStrategy: %v", err)
	}
}

// TestUnsealWithStrategy_RefusesStrategyMismatch verifies a strategy whose ID
// is not the persisted root key's is refused with a typed error matching
// ErrStrategyMismatch, and that the barrier stays sealed.
func TestUnsealWithStrategy_RefusesStrategyMismatch(t *testing.T) {
	ctx := context.Background()
	b := unsealWithStrategyFixture(t, tenantSoftwareStrategy(t))

	err := b.UnsealWithStrategy(ctx, NewNoneStrategy(), Credentials{})
	var mismatch *UnsealStrategyMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("UnsealWithStrategy(none) = %v, want *UnsealStrategyMismatchError", err)
	}
	if mismatch.Requested != StrategyNone || mismatch.Sealed == StrategyNone {
		t.Fatalf("mismatch = %+v, want requested %q against the sealed strategy", mismatch, StrategyNone)
	}
	if !errors.Is(err, ErrStrategyMismatch) {
		t.Fatalf("UnsealWithStrategy(none) = %v, want errors.Is ErrStrategyMismatch", err)
	}
	if mismatch.Error() == "" {
		t.Fatal("empty error message")
	}
	if !b.IsSealed() {
		t.Fatal("barrier unsealed after a strategy mismatch")
	}
}

// TestUnsealWithStrategy_NilStrategyFailsClosed verifies a nil strategy is
// refused rather than falling back to the barrier's registrations.
func TestUnsealWithStrategy_NilStrategyFailsClosed(t *testing.T) {
	b := unsealWithStrategyFixture(t, tenantSoftwareStrategy(t))
	if err := b.UnsealWithStrategy(context.Background(), nil, Credentials{}); !errors.Is(err, ErrNoStrategySpecified) {
		t.Fatalf("UnsealWithStrategy(nil) = %v, want ErrNoStrategySpecified", err)
	}
	if !b.IsSealed() {
		t.Fatal("barrier unsealed with a nil strategy")
	}
}

// TestUnsealWithStrategy_Uninitialized verifies an uninitialized barrier
// reports ErrNotInitialized.
func TestUnsealWithStrategy_Uninitialized(t *testing.T) {
	b, err := NewStorageBarrier(testLogger(), NewMemoryBackend(), BarrierConfig{
		RootKeyPath: "tenant/t1/root-key",
	}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := b.UnsealWithStrategy(context.Background(), NewNoneStrategy(), Credentials{}); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("UnsealWithStrategy = %v, want ErrNotInitialized", err)
	}
}

// TestUnsealTenantWithStrategy_RoundTrip verifies the registry-level wrapper
// unseals a reattached tenant shell with the given strategy, refuses a
// mismatch, and reports an unregistered tenant.
func TestUnsealTenantWithStrategy_RoundTrip(t *testing.T) {
	ctx := context.Background()
	system, err := NewStorageBarrier(testLogger(), NewMemoryBackend(), BarrierConfig{
		RootKeyPath: "system/root-key",
	}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewStorageBarrier(system): %v", err)
	}
	if err := system.Initialize(ctx, MustCredentials(context.Background(), []byte("test-master-password"))); err != nil {
		t.Fatalf("system Initialize: %v", err)
	}
	chosen := tenantSoftwareStrategy(t)
	backend := NewMemoryBackend()
	cfg := BarrierConfig{RootKeyPath: "tenant/t1/root-key"}

	first, err := NewBarrierRegistry(testLogger(), system)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	if _, err := first.RegisterTenant(ctx, "t1", backend, cfg, testSoftwareStrategy(t)); err != nil {
		t.Fatalf("RegisterTenant: %v", err)
	}
	if err := first.InitializeTenantWithStrategy(ctx, "t1", chosen, Credentials{}); err != nil {
		t.Fatalf("InitializeTenantWithStrategy: %v", err)
	}

	restarted, err := NewBarrierRegistry(testLogger(), system)
	if err != nil {
		t.Fatalf("NewBarrierRegistry(restart): %v", err)
	}
	if _, err := restarted.RegisterTenant(ctx, "t1", backend, cfg, testSoftwareStrategy(t)); err != nil {
		t.Fatalf("RegisterTenant(restart): %v", err)
	}
	if err := restarted.UnsealTenantWithStrategy(ctx, "t1", NewNoneStrategy(), Credentials{}); !errors.Is(err, ErrStrategyMismatch) {
		t.Fatalf("UnsealTenantWithStrategy(none) = %v, want ErrStrategyMismatch", err)
	}
	if err := restarted.UnsealTenantWithStrategy(ctx, "t1", chosen, Credentials{}); err != nil {
		t.Fatalf("UnsealTenantWithStrategy: %v", err)
	}
	tb, err := restarted.GetTenant("t1")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if tb.IsSealed() {
		t.Fatal("tenant barrier still sealed after UnsealTenantWithStrategy")
	}
	if err := restarted.UnsealTenantWithStrategy(ctx, "missing", chosen, Credentials{}); err == nil {
		t.Fatal("UnsealTenantWithStrategy on an unregistered tenant succeeded")
	}
}
