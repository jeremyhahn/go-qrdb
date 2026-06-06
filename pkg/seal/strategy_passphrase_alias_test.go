// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"context"
	"testing"
)

// TestStrategyPassphrase_ConstantValue pins the canonical strategy identifier
// so a future refactor cannot silently change the operator-facing name or the
// on-disk / AAD value.
func TestStrategyPassphrase_ConstantValue(t *testing.T) {
	if StrategyPassphrase != "passphrase" {
		t.Fatalf("StrategyPassphrase = %q, want %q", StrategyPassphrase, "passphrase")
	}
}

// TestIsPassphraseStrategy verifies the passphrase strategy id (and nothing
// else) is recognized.
func TestIsPassphraseStrategy(t *testing.T) {
	cases := []struct {
		id   StrategyID
		want bool
	}{
		{StrategyPassphrase, true},
		{StrategyShamir, false},
		{StrategyNone, false},
		{StrategyID("tpm2"), false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsPassphraseStrategy(c.id); got != c.want {
			t.Errorf("IsPassphraseStrategy(%q) = %v, want %v", c.id, got, c.want)
		}
	}
}

// TestStrategyRegistry_PassphraseResolves proves the default registry resolves
// the canonical "passphrase" name to a deferred software strategy whose
// persisted ID is StrategyPassphrase.
func TestStrategyRegistry_PassphraseResolves(t *testing.T) {
	r := NewDefaultStrategyRegistry()

	factory, err := r.LookupFactory(StrategyPassphrase)
	if err != nil {
		t.Fatalf("LookupFactory(%q): %v", StrategyPassphrase, err)
	}
	strat, err := factory()
	if err != nil {
		t.Fatalf("factory(%q): %v", StrategyPassphrase, err)
	}
	defer func() { _ = strat.Close() }()
	if id := strat.ID(); id != StrategyPassphrase {
		t.Errorf("strategy resolved via %q has ID %q, want %q",
			StrategyPassphrase, id, StrategyPassphrase)
	}
}

// TestBarrier_PassphraseRoundTrip initializes and seals a barrier using the
// passphrase strategy, then unseals it from a fresh barrier over the same
// backend with the same passphrase. A wrong passphrase must still fail.
func TestBarrier_PassphraseRoundTrip(t *testing.T) {
	ctx := context.Background()
	registry := NewDefaultStrategyRegistry()
	backend := NewMemoryBackend()
	creds := Credentials{Secret: "round-trip-passphrase"}
	cfg := BarrierConfig{RootKeyPath: "passphrase/root-key"}

	sealStrat := buildRegisteredStrategy(t, registry, StrategyPassphrase)
	sealer, err := NewStorageBarrier(testLogger(), backend, cfg, sealStrat)
	if err != nil {
		t.Fatalf("NewStorageBarrier(seal): %v", err)
	}
	if err := sealer.Initialize(ctx, creds); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if id := sealer.ActiveStrategy(); id != StrategyPassphrase {
		t.Fatalf("ActiveStrategy after seal = %q, want %q", id, StrategyPassphrase)
	}
	if err := sealer.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	unsealStrat := buildRegisteredStrategy(t, registry, StrategyPassphrase)
	unsealer, err := NewStorageBarrier(testLogger(), backend, cfg, unsealStrat)
	if err != nil {
		t.Fatalf("NewStorageBarrier(unseal): %v", err)
	}
	if err := unsealer.Unseal(ctx, creds); err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	if unsealer.IsSealed() {
		t.Fatal("barrier still sealed after Unseal")
	}

	// A wrong passphrase must still fail.
	other := NewMemoryBackend()
	wrongStrat := buildRegisteredStrategy(t, registry, StrategyPassphrase)
	guard, err := NewStorageBarrier(testLogger(), other, cfg, wrongStrat)
	if err != nil {
		t.Fatalf("NewStorageBarrier(guard): %v", err)
	}
	if err := guard.Initialize(ctx, creds); err != nil {
		t.Fatalf("Initialize(guard): %v", err)
	}
	if err := guard.Seal(); err != nil {
		t.Fatalf("Seal(guard): %v", err)
	}
	if err := guard.Unseal(ctx, Credentials{Secret: "wrong-passphrase"}); err == nil {
		t.Fatal("Unseal with wrong passphrase: expected error, got nil")
	}
}

// buildRegisteredStrategy resolves name through the registry and constructs the
// strategy, failing the test on any error.
func buildRegisteredStrategy(t *testing.T, r StrategyRegistry, name StrategyID) SealingStrategy {
	t.Helper()
	factory, err := r.LookupFactory(name)
	if err != nil {
		t.Fatalf("LookupFactory(%q): %v", name, err)
	}
	strat, err := factory()
	if err != nil {
		t.Fatalf("factory(%q): %v", name, err)
	}
	return strat
}
