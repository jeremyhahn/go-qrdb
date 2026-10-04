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

	"github.com/stretchr/testify/require"
)

// A password change must leave the barrier still CHECKING passwords.
//
// go-qrdb#157: ChangePassword used to install a bound SoftwareStrategy, and a
// bound strategy ignores the credentials passed to UnsealRootKey — go-quicraft
// says so directly: "When deferPassphrase is false (default), the ctx and
// creds parameters are unused". So from the first password change onward the
// barrier accepted ANY passphrase. Measured before the fix:
//
//	BEFORE ChangePassword — wrong passphrase: decryption failed   (correct)
//	AFTER  ChangePassword — wrong passphrase: <nil>               (bypass)
//	AFTER  ChangePassword — OLD passphrase:   <nil>               (bypass)
//
// The barrier is built the way production builds it —
// pkg/database/barrier_manager.go registers NewDeferredSoftwareStrategy — because
// that is the shape the defect appeared in. A test written on a bound strategy
// would pass before and after and prove nothing.
//
// All three post-change outcomes are pinned separately. "New password works" is
// the weakest of them: it passes even in the fully-bypassed state, where
// everything works. The two that carry the security claim are that the WRONG
// passphrase and the OLD passphrase are both refused — an old credential
// surviving a rotation is its own failure, distinct from accepting an arbitrary
// one.
func TestChangePassword_KeepsCheckingCredentials(t *testing.T) {
	const (
		original = "original-password"
		updated  = "updated-password"
		wrong    = "not-the-password"
	)

	ctx := context.Background()
	cfg := BarrierConfig{RootKeyPath: "test/root-key"}
	b, err := NewStorageBarrier(testLogger(), NewMemoryBackend(), cfg, NewDeferredSoftwareStrategy())
	require.NoError(t, err)
	require.NoError(t, b.Initialize(ctx, MustCredentials(context.Background(), []byte(original))))

	// Baseline, asserted here rather than assumed: credential checking works
	// BEFORE the password change. Without this, a future regression that broke
	// checking at Initialize would leave the post-change assertions passing for
	// the wrong reason.
	require.NoError(t, b.Seal())
	require.Error(t, b.Unseal(ctx, MustCredentials(context.Background(), []byte(wrong))),
		"baseline: a wrong passphrase must be refused before any password change")
	require.NoError(t, b.Unseal(ctx, MustCredentials(context.Background(), []byte(original))),
		"baseline: the original passphrase must be accepted")

	require.NoError(t, b.ChangePassword(ctx, MustCredentials(context.Background(), []byte(updated))))

	// (1) A wrong passphrase must still be refused. This is the bypass.
	require.NoError(t, b.Seal())
	require.Error(t, b.Unseal(ctx, MustCredentials(context.Background(), []byte(wrong))),
		"go-qrdb#157: a WRONG passphrase unsealed the barrier after ChangePassword")

	// (2) The OLD passphrase must be refused — a rotated credential that still
	// works has not been rotated.
	require.NoError(t, b.Seal())
	require.Error(t, b.Unseal(ctx, MustCredentials(context.Background(), []byte(original))),
		"go-qrdb#157: the OLD passphrase still unsealed the barrier after ChangePassword")

	// (3) And the new passphrase must actually work, or the fix has traded a
	// bypass for a lockout.
	require.NoError(t, b.Seal())
	require.NoError(t, b.Unseal(ctx, MustCredentials(context.Background(), []byte(updated))),
		"the new passphrase must unseal the barrier after ChangePassword")
}

// The blob written by ChangePassword survives a restart.
//
// The in-process assertions above all run against the barrier instance that
// performed the change, which still holds live state. This one throws that away
// and re-opens the persisted blob with a FRESH barrier and a fresh deferred
// strategy — the path a daemon takes after a restart. It is what proves the
// re-seal actually rewrapped the root key under the new passphrase rather than
// leaving something only the original instance could open.
func TestChangePassword_ReSealedBlobUnsealsAfterRestart(t *testing.T) {
	const (
		original = "original-password"
		updated  = "updated-password"
		wrong    = "not-the-password"
	)

	ctx := context.Background()
	cfg := BarrierConfig{RootKeyPath: "test/root-key"}

	// One backend, shared across both barriers: the restart keeps the disk.
	backend := NewMemoryBackend()

	first, err := NewStorageBarrier(testLogger(), backend, cfg, NewDeferredSoftwareStrategy())
	require.NoError(t, err)
	require.NoError(t, first.Initialize(ctx, MustCredentials(context.Background(), []byte(original))))
	require.NoError(t, first.ChangePassword(ctx, MustCredentials(context.Background(), []byte(updated))))
	require.NoError(t, first.Seal())

	// "Restart": a brand-new barrier over the same persisted state.
	second, err := NewStorageBarrier(testLogger(), backend, cfg, NewDeferredSoftwareStrategy())
	require.NoError(t, err)

	require.Error(t, second.Unseal(ctx, MustCredentials(context.Background(), []byte(wrong))),
		"a wrong passphrase must be refused after a restart")
	require.Error(t, second.Unseal(ctx, MustCredentials(context.Background(), []byte(original))),
		"the pre-change passphrase must be refused after a restart")
	require.NoError(t, second.Unseal(ctx, MustCredentials(context.Background(), []byte(updated))),
		"the new passphrase must unseal the persisted blob after a restart")
}
