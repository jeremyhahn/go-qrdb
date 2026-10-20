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
	"io"
	"log/slog"
	"testing"
)

// openPinTestBarrier opens the barrier persisted in backend the way a daemon
// does at boot: a fresh StorageBarrier over the same store, initialized on
// first use and unsealed on every later one. A fresh instance is the point,
// because it carries none of the previous instance's in-memory pins, exactly
// as a restarted process does not.
func openPinTestBarrier(t *testing.T, backend StorageBackend, rootKeyPath string, strategy SealingStrategy) *StorageBarrier {
	t.Helper()
	ctx := context.Background()
	b, err := NewStorageBarrier(slog.New(slog.NewTextHandler(io.Discard, nil)),
		backend, BarrierConfig{RootKeyPath: rootKeyPath}, strategy)
	if err != nil {
		t.Fatalf("NewStorageBarrier(%s): %v", rootKeyPath, err)
	}
	exists, err := backend.Exists(ctx, rootKeyPath)
	if err != nil {
		t.Fatalf("Exists(%s): %v", rootKeyPath, err)
	}
	if !exists {
		if err := b.Initialize(ctx, Credentials{}); err != nil {
			t.Fatalf("Initialize(%s): %v", rootKeyPath, err)
		}
		return b
	}
	if err := b.Unseal(ctx, Credentials{}); err != nil {
		t.Fatalf("Unseal(%s): %v", rootKeyPath, err)
	}
	return b
}

// openSystemPinTestBarrier opens the system barrier persisted in backend.
func openSystemPinTestBarrier(t *testing.T, backend StorageBackend) *StorageBarrier {
	t.Helper()
	strategy, err := NewSoftwareStrategy(context.Background(), []byte("system-master-pass"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	return openPinTestBarrier(t, backend, "system/root-key", strategy)
}

// restartAndPurge stands in for one node restart followed by a WAL snapshot:
// it seals the running barrier, opens the store afresh (which rotates the
// epoch), and purges every epoch below the new current one, which is the
// highest floor go-quicraft's snapshot pool can ever pass to
// PurgeEpochsBefore. It returns the reopened barrier and how many epochs the
// purge dropped.
func restartAndPurge(t *testing.T, running *StorageBarrier, backend StorageBackend) (*StorageBarrier, int) {
	t.Helper()
	before := running.EpochBarrier().CurrentEpoch()
	_ = running.Seal()
	reopened := openSystemPinTestBarrier(t, backend)
	current := reopened.EpochBarrier().CurrentEpoch()
	if current <= before {
		t.Fatalf("reopen did not rotate: epoch %d -> %d; the purge below would test nothing", before, current)
	}
	purged, err := reopened.EpochBarrier().PurgeEpochsBefore(context.Background(), current)
	if err != nil {
		t.Fatalf("PurgeEpochsBefore(%d): %v", current, err)
	}
	return reopened, purged
}

// TestEpochPins_SystemWrappedTenantRootKeySurvivesPurge is the regression
// for the strict-home restart flake. An isolated tenant's root key is
// wrapped by the system barrier at the epoch current when the tenant was
// created. Every restart rotates the system epoch, and a WAL snapshot then
// purges the epochs below the floor. Before the pins, that purge dropped the
// wrapping epoch's DEK, and the next restart's tenant unseal failed with
// "decryption failed". The tenant barrier stayed sealed and its home group
// never started on that node.
func TestEpochPins_SystemWrappedTenantRootKeySurvivesPurge(t *testing.T) {
	ctx := context.Background()
	systemStore, tenantStore := NewMemoryBackend(), NewMemoryBackend()

	system := openSystemPinTestBarrier(t, systemStore)
	tenant := openPinTestBarrier(t, tenantStore, "tenant/acme/root-key", NewSystemBarrierStrategy(system))
	if err := tenant.Put(ctx, "acme/probe", []byte("tenant-record")); err != nil {
		t.Fatalf("tenant Put: %v", err)
	}
	_ = tenant.Seal()

	for restart := 1; restart <= 3; restart++ {
		var purged int
		system, purged = restartAndPurge(t, system, systemStore)
		if purged != 0 {
			t.Fatalf("restart %d: purge dropped %d system epochs; the tenant root key is "+
				"wrapped under one of them", restart, purged)
		}
	}

	_ = system.Seal()
	system = openSystemPinTestBarrier(t, systemStore)
	tenant = openPinTestBarrier(t, tenantStore, "tenant/acme/root-key", NewSystemBarrierStrategy(system))
	got, err := tenant.Get(ctx, "acme/probe")
	if err != nil {
		t.Fatalf("tenant Get after purged restarts: %v", err)
	}
	if !bytes.Equal(got, []byte("tenant-record")) {
		t.Fatalf("tenant record = %q, want %q", got, "tenant-record")
	}
}

// TestEpochPins_StoredValueSurvivesPurge covers the general case: any value a
// StorageBarrier stores is long-lived ciphertext under the epoch it was
// written in, and must stay readable across restarts and purges of the WAL
// barrier that shares its keyring.
func TestEpochPins_StoredValueSurvivesPurge(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryBackend()

	b := openSystemPinTestBarrier(t, store)
	if err := b.Put(ctx, "cfg/value", []byte("written-at-first-epoch")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	for restart := 1; restart <= 2; restart++ {
		var purged int
		b, purged = restartAndPurge(t, b, store)
		if purged != 0 {
			t.Fatalf("restart %d: purge dropped %d epochs under a stored value", restart, purged)
		}
	}
	got, err := b.Get(ctx, "cfg/value")
	if err != nil {
		t.Fatalf("Get after purged restarts: %v", err)
	}
	if !bytes.Equal(got, []byte("written-at-first-epoch")) {
		t.Fatalf("value = %q, want %q", got, "written-at-first-epoch")
	}
}

// TestEpochPins_UnwrittenEpochIsStillPurged is the control that gives the two
// tests above their meaning: the purge they run is live, and it retires an
// epoch nothing was written under. If this failed, "purged 0" above would
// prove nothing.
func TestEpochPins_UnwrittenEpochIsStillPurged(t *testing.T) {
	ctx := context.Background()
	b := openSystemPinTestBarrier(t, NewMemoryBackend())
	first := b.EpochBarrier().CurrentEpoch()

	next, err := b.EpochBarrier().Rotate(ctx)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	purged, err := b.EpochBarrier().PurgeEpochsBefore(ctx, next)
	if err != nil {
		t.Fatalf("PurgeEpochsBefore(%d): %v", next, err)
	}
	if purged != 1 {
		t.Fatalf("purged %d epochs, want 1: epoch %d held no ciphertext and no pin", purged, first)
	}
}

// TestEpochPins_OnePinPerEpoch checks that many writes under one epoch hold
// exactly one pin on it, not one per write. Releasing that single pin by hand
// must make the epoch purgeable. If each write had kept its own pin, the
// purge would still find the epoch held.
func TestEpochPins_OnePinPerEpoch(t *testing.T) {
	ctx := context.Background()
	b := openSystemPinTestBarrier(t, NewMemoryBackend())
	first := b.EpochBarrier().CurrentEpoch()

	for _, key := range []string{"a", "b", "c"} {
		if err := b.Put(ctx, key, []byte(key)); err != nil {
			t.Fatalf("Put(%s): %v", key, err)
		}
	}
	if _, err := b.Encrypt([]byte("d")); err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	next, err := b.EpochBarrier().Rotate(ctx)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if purged, err := b.EpochBarrier().PurgeEpochsBefore(ctx, next); err != nil || purged != 0 {
		t.Fatalf("PurgeEpochsBefore with the pin held = (%d, %v), want (0, nil)", purged, err)
	}

	b.EpochBarrier().DeregisterInFlightEpoch(first)
	purged, err := b.EpochBarrier().PurgeEpochsBefore(ctx, next)
	if err != nil {
		t.Fatalf("PurgeEpochsBefore after release: %v", err)
	}
	if purged != 1 {
		t.Fatalf("purged %d epochs after releasing one pin, want 1: writes left extra pins on epoch %d",
			purged, first)
	}
}
