// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"testing"
)

// newUnsealedSystemBarrier builds an initialized + unsealed StorageBarrier
// suitable as the unseal authority for SystemBarrierStrategy tests.
func newUnsealedSystemBarrier(t *testing.T) *StorageBarrier {
	t.Helper()
	strategy, err := NewSoftwareStrategy(context.Background(), []byte("system-master-pass"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	b, err := NewStorageBarrier(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		NewMemoryBackend(),
		BarrierConfig{RootKeyPath: "system/root-key"},
		strategy,
	)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := b.Initialize(context.Background(), Credentials{}); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}
	return b
}

// TestSystemBarrierStrategy_SealUnsealRoundTrip verifies that a root key
// wrapped under the system barrier round-trips back to the identical bytes,
// composing only the system barrier's existing AEAD (no new crypto).
func TestSystemBarrierStrategy_SealUnsealRoundTrip(t *testing.T) {
	system := newUnsealedSystemBarrier(t)
	strategy := NewSystemBarrierStrategy(system)
	ctx := context.Background()

	if strategy.ID() != StrategySystemBarrier {
		t.Fatalf("ID = %q, want %q", strategy.ID(), StrategySystemBarrier)
	}
	if !strategy.Available() {
		t.Fatal("strategy should be Available with an unsealed system barrier")
	}
	if strategy.HardwareBacked() {
		t.Error("system-barrier strategy must not report HardwareBacked")
	}

	rootKey := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, rootKey); err != nil {
		t.Fatalf("rand: %v", err)
	}

	sealed, err := strategy.SealRootKey(ctx, rootKey, Credentials{})
	if err != nil {
		t.Fatalf("SealRootKey: %v", err)
	}
	if sealed.Strategy != StrategySystemBarrier {
		t.Errorf("sealed.Strategy = %q, want %q", sealed.Strategy, StrategySystemBarrier)
	}
	if bytes.Equal(sealed.Ciphertext, rootKey) {
		t.Error("ciphertext must not equal the plaintext root key")
	}

	got, err := strategy.UnsealRootKey(ctx, sealed, Credentials{})
	if err != nil {
		t.Fatalf("UnsealRootKey: %v", err)
	}
	if !bytes.Equal(got, rootKey) {
		t.Fatal("unwrapped root key does not match the original")
	}
}

// TestSystemBarrierStrategy_Unavailable verifies the strategy fails closed when
// the system barrier is nil or sealed: it is unavailable and seal/unseal return
// ErrSystemBarrierUnavailable.
func TestSystemBarrierStrategy_Unavailable(t *testing.T) {
	ctx := context.Background()

	nilStrat := NewSystemBarrierStrategy(nil)
	if nilStrat.Available() {
		t.Error("nil system barrier must not be Available")
	}
	if _, err := nilStrat.SealRootKey(ctx, make([]byte, 32), Credentials{}); !errors.Is(err, ErrSystemBarrierUnavailable) {
		t.Errorf("SealRootKey err = %v, want ErrSystemBarrierUnavailable", err)
	}

	system := newUnsealedSystemBarrier(t)
	_ = system.Seal()
	sealedStrat := NewSystemBarrierStrategy(system)
	if sealedStrat.Available() {
		t.Error("sealed system barrier must not be Available")
	}
	if _, err := sealedStrat.UnsealRootKey(ctx, &SealedRootKey{Strategy: StrategySystemBarrier}, Credentials{}); !errors.Is(err, ErrSystemBarrierUnavailable) {
		t.Errorf("UnsealRootKey err = %v, want ErrSystemBarrierUnavailable", err)
	}
}

// TestSystemBarrierStrategy_Close verifies the no-op Close returns nil even with
// a nil backing barrier (the strategy holds no resources of its own).
func TestSystemBarrierStrategy_Close(t *testing.T) {
	t.Parallel()
	if err := NewSystemBarrierStrategy(nil).Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestSystemBarrierStrategy_AutoUnsealTenantBarrier proves the end-to-end
// system-managed mode: a tenant barrier is initialized credential-free, stores
// data, is sealed, and is then re-opened and unsealed credential-free (the
// system barrier is the unseal authority), with the data still decryptable.
// TestSystemBarrierStrategy_UnsealRootKey_NilSealed verifies that
// UnsealRootKey returns ErrCorruptRootKey when passed a nil SealedRootKey.
func TestSystemBarrierStrategy_UnsealRootKey_NilSealed(t *testing.T) {
	system := newUnsealedSystemBarrier(t)
	strategy := NewSystemBarrierStrategy(system)

	_, err := strategy.UnsealRootKey(context.Background(), nil, Credentials{})
	if err == nil {
		t.Fatal("expected error for nil SealedRootKey, got nil")
	}
	if !errors.Is(err, ErrCorruptRootKey) {
		t.Errorf("error = %v, want ErrCorruptRootKey", err)
	}
}

// TestSystemBarrierStrategy_UnsealRootKey_WrongStrategy verifies that
// UnsealRootKey returns ErrStrategyMismatch when the SealedRootKey was
// sealed by a different strategy.
func TestSystemBarrierStrategy_UnsealRootKey_WrongStrategy(t *testing.T) {
	system := newUnsealedSystemBarrier(t)
	strategy := NewSystemBarrierStrategy(system)

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	// Seal with the software strategy, which produces a StrategyPassphrase sealed key.
	sw, err := NewSoftwareStrategy(context.Background(), []byte("passphrase"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	sealed, err := sw.SealRootKey(context.Background(), key, Credentials{})
	if err != nil {
		t.Fatalf("SealRootKey: %v", err)
	}

	// Attempting to unseal with SystemBarrierStrategy must fail.
	_, err = strategy.UnsealRootKey(context.Background(), sealed, Credentials{})
	if err == nil {
		t.Fatal("expected ErrStrategyMismatch for wrong-strategy SealedRootKey, got nil")
	}
	if !errors.Is(err, ErrStrategyMismatch) {
		t.Errorf("error = %v, want ErrStrategyMismatch", err)
	}
}

func TestSystemBarrierStrategy_AutoUnsealTenantBarrier(t *testing.T) {
	system := newUnsealedSystemBarrier(t)
	ctx := context.Background()
	backend := NewMemoryBackend()
	cfg := BarrierConfig{RootKeyPath: "tenant/auto/root-key"}

	tenantBar, err := NewStorageBarrier(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		backend, cfg, NewSystemBarrierStrategy(system),
	)
	if err != nil {
		t.Fatalf("NewStorageBarrier (tenant): %v", err)
	}
	// Credential-free init — the live blocker. The deferred passphrase strategy
	// would reject empty credentials here; the system-barrier strategy does not.
	if err := tenantBar.Initialize(ctx, Credentials{}); err != nil {
		t.Fatalf("tenant Initialize (credential-free): %v", err)
	}
	if err := tenantBar.Put(ctx, "k1", []byte("secret-value")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	_ = tenantBar.Seal()

	// Simulate a restart: a fresh StorageBarrier over the SAME backend with a
	// fresh SystemBarrierStrategy bound to the (still unsealed) system barrier.
	reopened, err := NewStorageBarrier(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		backend, cfg, NewSystemBarrierStrategy(system),
	)
	if err != nil {
		t.Fatalf("NewStorageBarrier (reopen): %v", err)
	}
	if err := reopened.Unseal(ctx, Credentials{}); err != nil {
		t.Fatalf("credential-free Unseal after restart: %v", err)
	}
	got, err := reopened.Get(ctx, "k1")
	if err != nil {
		t.Fatalf("Get after re-attach: %v", err)
	}
	if string(got) != "secret-value" {
		t.Fatalf("Get = %q, want %q", got, "secret-value")
	}
}
