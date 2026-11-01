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
	"errors"
	"strings"
	"testing"
	"time"
)

// TestChangePassword_SwapErrorIsReturnedAndWritesNothing: a storage error
// from the guarded swap reaches the caller as itself, not as a conflict,
// and the persisted root-key blob is unchanged.
func TestChangePassword_SwapErrorIsReturnedAndWritesNothing(t *testing.T) {
	fb := newFailingBackend()
	fb.casErr = errStorageInject
	cfg := BarrierConfig{RootKeyPath: "test/root"}
	b, err := NewStorageBarrier(testLogger(), fb, cfg, testSoftwareStrategy(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := b.Initialize(ctx, MustCredentials(ctx, []byte("old-pw"))); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	before, err := fb.inner.Get(ctx, cfg.RootKeyPath)
	if err != nil {
		t.Fatal(err)
	}

	err = b.ChangePassword(ctx, MustCredentials(ctx, []byte(testMasterPassword)), MustCredentials(ctx, []byte("new-pw")))
	if !errors.Is(err, errStorageInject) {
		t.Fatalf("ChangePassword over a failing swap = %v; want the storage error", err)
	}
	after, err := fb.inner.Get(ctx, cfg.RootKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("a failed swap changed the persisted root-key blob")
	}
}

// TestOpenedToLiveKey: the current credential is accepted only when the
// blob opens to the very key the barrier holds; a blob that opens to any
// other key, or does not open, is a credential mismatch.
func TestOpenedToLiveKey(t *testing.T) {
	ctx := context.Background()
	strategy := NewDeferredSoftwareStrategy()
	t.Cleanup(func() { _ = strategy.Close() })
	live := bytes.Repeat([]byte{0x11}, 32)
	sealed, err := strategy.SealRootKey(ctx, live, MustCredentials(ctx, []byte("pw")))
	if err != nil {
		t.Fatalf("SealRootKey: %v", err)
	}

	if err := openedToLiveKey(ctx, strategy, sealed, MustCredentials(ctx, []byte("pw")), live); err != nil {
		t.Fatalf("the right credential over the live key = %v; want nil", err)
	}
	other := bytes.Repeat([]byte{0x22}, 32)
	if err := openedToLiveKey(ctx, strategy, sealed, MustCredentials(ctx, []byte("pw")), other); !errors.Is(err, ErrCredentialMismatch) {
		t.Fatalf("a blob opening to another key = %v; want ErrCredentialMismatch", err)
	}
	if err := openedToLiveKey(ctx, strategy, sealed, MustCredentials(ctx, []byte("wrong")), live); !errors.Is(err, ErrCredentialMismatch) {
		t.Fatalf("a wrong credential = %v; want ErrCredentialMismatch", err)
	}
}

// TestRestoreInitializedAt_UnreadableMetadataStillSignalsInitialized: an
// unseal that cannot read the persisted initialization time records the
// present instead, so the unsealed barrier still reports initialized.
func TestRestoreInitializedAt_UnreadableMetadataStillSignalsInitialized(t *testing.T) {
	fb := newFailingBackend()
	b, err := NewStorageBarrier(testLogger(), fb, BarrierConfig{RootKeyPath: "test/root"}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := b.Initialize(ctx, MustCredentials(ctx, []byte("pw"))); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	b.initializedAt.Store(time.Time{})
	fb.getErr = errStorageInject

	before := time.Now().UTC()
	b.restoreInitializedAt(ctx)
	got, ok := b.initializedAt.Load().(time.Time)
	if !ok || got.Before(before) {
		t.Fatalf("initializedAt = %v; want a time no earlier than %v", got, before)
	}
}

// TestDataRootErrors_CarryTheCause: both data-root refusals name the path
// and append the underlying error when there is one.
func TestDataRootErrors_CarryTheCause(t *testing.T) {
	cause := errors.New("stat failed")
	cases := []struct {
		name string
		err  error
		want []string
	}{
		{"mount missing", &DataRootMountMissingError{TenantID: "acme", BarrierPath: "/data/acme", Err: cause},
			[]string{"tenant acme barrier store /data/acme", ": stat failed"}},
		{"mount missing, no tenant", &DataRootMountMissingError{BarrierPath: "/data/x"},
			[]string{"qrdb/seal: tenant barrier store /data/x"}},
		{"outside local roots", &DataRootOutsideLocalRootsError{DataRoot: "/elsewhere", Err: cause},
			[]string{"refusing to remove /elsewhere", ": stat failed"}},
		{"outside local roots, no cause", &DataRootOutsideLocalRootsError{DataRoot: "/elsewhere"},
			[]string{"volume-pool paths"}},
	}
	for _, tc := range cases {
		msg := tc.err.Error()
		for _, w := range tc.want {
			if !strings.Contains(msg, w) {
				t.Errorf("%s: %q lacks %q", tc.name, msg, w)
			}
		}
	}
}
