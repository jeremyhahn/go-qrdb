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

	quicraftseal "github.com/jeremyhahn/go-quicraft/pkg/seal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChangePassword_Success(t *testing.T) {
	ctx := context.Background()
	oldPassword := "old-password-123"
	newPassword := "new-password-456"

	strategy := testSoftwareStrategy(t)
	cfg := BarrierConfig{RootKeyPath: "test/root-key"}
	b, err := NewStorageBarrier(testLogger(), NewMemoryBackend(), cfg, strategy)
	require.NoError(t, err)

	// Initialize with the old password.
	err = b.Initialize(ctx, MustCredentials([]byte(oldPassword)))
	require.NoError(t, err)

	// Store a value while unsealed so we can verify data survives the change.
	err = b.Put(ctx, "test/data", []byte("secret-payload"))
	require.NoError(t, err)

	// Change the password.
	err = b.ChangePassword(ctx, MustCredentials([]byte(newPassword)))
	require.NoError(t, err)

	// Data written before the password change is still readable.
	val, err := b.Get(ctx, "test/data")
	require.NoError(t, err)
	assert.Equal(t, []byte("secret-payload"), val)

	// Seal the barrier.
	err = b.Seal()
	require.NoError(t, err)
	assert.True(t, b.IsSealed())

	// Unseal with the new password succeeds (the in-memory strategy was
	// swapped to the new one by ChangePassword).
	err = b.Unseal(ctx, MustCredentials([]byte(newPassword)))
	require.NoError(t, err)
	assert.False(t, b.IsSealed())

	// Previously stored data is still accessible after re-unseal.
	val, err = b.Get(ctx, "test/data")
	require.NoError(t, err)
	assert.Equal(t, []byte("secret-payload"), val)
}

func TestChangePassword_OldPasswordFailsOnNewBarrier(t *testing.T) {
	ctx := context.Background()
	oldPassword := "old-password-123"
	newPassword := "new-password-456"

	// Use a shared MemoryBackend so the persisted blob survives across
	// barrier instances, simulating a process restart.
	base := NewMemoryBackend()

	oldStrat, err := NewSoftwareStrategy([]byte(oldPassword))
	require.NoError(t, err)

	cfg := BarrierConfig{RootKeyPath: "test/root-key"}
	b, err := NewStorageBarrier(testLogger(), base, cfg, oldStrat)
	require.NoError(t, err)

	err = b.Initialize(ctx, MustCredentials([]byte(oldPassword)))
	require.NoError(t, err)

	err = b.ChangePassword(ctx, MustCredentials([]byte(newPassword)))
	require.NoError(t, err)

	err = b.Seal()
	require.NoError(t, err)

	// Create a new barrier from the same base backend with a strategy
	// built from the OLD password. This simulates a restart where the
	// operator provides the wrong password.
	oldStrat2, err := NewSoftwareStrategy([]byte(oldPassword))
	require.NoError(t, err)

	b2, err := NewStorageBarrier(testLogger(), base, cfg, oldStrat2)
	require.NoError(t, err)

	err = b2.Unseal(ctx, MustCredentials([]byte(oldPassword)))
	assert.Error(t, err, "unseal with old password strategy should fail after password change")

	// Create a third barrier with the NEW password strategy -- it succeeds.
	newStrat, err := NewSoftwareStrategy([]byte(newPassword))
	require.NoError(t, err)

	b3, err := NewStorageBarrier(testLogger(), base, cfg, newStrat)
	require.NoError(t, err)

	err = b3.Unseal(ctx, MustCredentials([]byte(newPassword)))
	require.NoError(t, err)
	assert.False(t, b3.IsSealed())
}

func TestChangePassword_Sealed(t *testing.T) {
	cfg := BarrierConfig{RootKeyPath: "test/root-key"}
	b, err := NewStorageBarrier(testLogger(), NewMemoryBackend(), cfg, testSoftwareStrategy(t))
	require.NoError(t, err)

	// Barrier is sealed (never initialized) -- should return ErrSealed.
	err = b.ChangePassword(context.Background(), MustCredentials([]byte("new-password")))
	assert.ErrorIs(t, err, ErrSealed)
}

func TestChangePassword_EmptyPassword(t *testing.T) {
	ctx := context.Background()
	cfg := BarrierConfig{RootKeyPath: "test/root-key"}
	b, err := NewStorageBarrier(testLogger(), NewMemoryBackend(), cfg, testSoftwareStrategy(t))
	require.NoError(t, err)

	err = b.Initialize(ctx, MustCredentials([]byte("init-password")))
	require.NoError(t, err)

	// No secret at all — the zero credential — is what an empty passphrase
	// becomes; NewCredentials refuses to seal zero-length material.
	err = b.ChangePassword(ctx, Credentials{})
	assert.ErrorIs(t, err, ErrInvalidPassphrase)
}

func TestChangePassword_NoSoftwareStrategy(t *testing.T) {
	ctx := context.Background()

	// Use the mock hardware strategy which registers under "mock-hardware".
	// Set PreferenceOrder so bestStrategy finds it during Initialize, but
	// ChangePassword will fail because no StrategyPassphrase is registered.
	hwStrat := &mockHardwareStrategy{}
	cfg := BarrierConfig{
		RootKeyPath:     "test/root-key",
		PreferenceOrder: []StrategyID{"mock-hardware"},
	}
	b, err := NewStorageBarrier(testLogger(), NewMemoryBackend(), cfg, hwStrat)
	require.NoError(t, err)

	err = b.Initialize(ctx, MustCredentials([]byte("hw-password")))
	require.NoError(t, err)

	err = b.ChangePassword(ctx, MustCredentials([]byte("new-password")))
	assert.ErrorIs(t, err, ErrStrategyNotFound)
}

func TestChangePassword_AuditEvent(t *testing.T) {
	ctx := context.Background()
	audit := &mockAuditLogger{}
	cfg := BarrierConfig{
		RootKeyPath: "test/root-key",
		AuditLogger: audit,
	}
	b, err := NewStorageBarrier(testLogger(), NewMemoryBackend(), cfg, testSoftwareStrategy(t))
	require.NoError(t, err)

	err = b.Initialize(ctx, MustCredentials([]byte("password")))
	require.NoError(t, err)

	err = b.ChangePassword(ctx, MustCredentials([]byte("new-password")))
	require.NoError(t, err)

	// Find the change_password audit event.
	events := audit.Events()
	var found bool
	for _, e := range events {
		if e.Action == "change_password" {
			found = true
			assert.Equal(t, "allow", e.Outcome)
			assert.Equal(t, "test/root-key", e.ResourceID)
			break
		}
	}
	assert.True(t, found, "expected a change_password audit event")
}

func TestChangePassword_PersistNewBlob(t *testing.T) {
	ctx := context.Background()
	base := NewMemoryBackend()
	cfg := BarrierConfig{RootKeyPath: "test/root-key"}
	b, err := NewStorageBarrier(testLogger(), base, cfg, testSoftwareStrategy(t))
	require.NoError(t, err)

	err = b.Initialize(ctx, MustCredentials([]byte("password-a")))
	require.NoError(t, err)

	// Capture the sealed blob before the change.
	blobBefore, err := base.Get(ctx, "test/root-key")
	require.NoError(t, err)

	err = b.ChangePassword(ctx, MustCredentials([]byte("password-b")))
	require.NoError(t, err)

	// The sealed blob must have changed in the base backend.
	blobAfter, err := base.Get(ctx, "test/root-key")
	require.NoError(t, err)
	assert.NotEqual(t, blobBefore, blobAfter,
		"sealed root key blob should differ after password change")
}

// ---------------------------------------------------------------------------
// The KDF work factor survives a password change (#156)
//
// A sealed root-key blob records the KDF VERSION byte and never the params, so
// a blob sealed at a non-default factor can only be unsealed by a strategy
// carrying that same factor. ChangePassword used to build its replacement with
// the no-argument deferred constructor, which reset a hardened deployment to
// the library defaults on every password change — and did so in the weaker
// direction, on the credential that wraps the root key.
//
// The fixture MUST use non-default parameters: default-in/default-out passes
// either way and proves nothing. The two legs pick the KDF the way the
// deployment would — PBKDF2 under FIPS, Argon2id otherwise — so the FIPS leg
// never attempts the Argon2id constructor, which is refused there by design.
// ---------------------------------------------------------------------------

// tunedPassphraseStrategy returns a bound software strategy at a deliberately
// non-default work factor for the running FIPS policy, plus the version and
// params it was built with. Under runtime FIPS (GODEBUG=fips140=only) the
// Argon2id constructor is never attempted; in a -tags fips BUILD, which has no
// Argon2id deriver at all, the constructor's own ErrKDFUnsupportedVersion
// selects the PBKDF2 fixture — the test is about preserving whichever factor
// the build can derive with, not about which one that is.
func tunedPassphraseStrategy(t *testing.T, passphrase string) (*SoftwareStrategy, KDFVersion, KDFParams) {
	t.Helper()
	if !FIPSEnabled() {
		// Above every Argon2id floor (2 / 32 MiB / 1) and NOT the 4 / 128 MiB
		// / 4 default. Small enough to keep the test fast.
		s, err := NewSoftwareStrategyWithParams([]byte(passphrase), 3, 48*1024, 2)
		if err == nil {
			require.NotEqual(t, quicraftseal.DefaultKDFParams(), s.KDFParams(),
				"fixture must not coincide with the default; the test would prove nothing")
			return s, KDFArgon2id, s.KDFParams()
		}
		require.ErrorIs(t, err, quicraftseal.ErrKDFUnsupportedVersion,
			"only a build without the Argon2id deriver may decline the Argon2id fixture")
	}
	// Above the 100,000 floor and NOT the 600,000 default.
	const iterations = 250_000
	s, err := NewSoftwareStrategyFIPS([]byte(passphrase), iterations)
	require.NoError(t, err)
	require.NotEqual(t, quicraftseal.DefaultFIPSKDFParams().Iterations, s.KDFParams().Iterations,
		"fixture must not coincide with the default; the test would prove nothing")
	return s, KDFPbkdf2SHA256, s.KDFParams()
}

func TestChangePassword_PreservesKDFVersionAndParams(t *testing.T) {
	ctx := context.Background()
	const oldPassword, newPassword = "old-password-123", "new-password-456"

	oldStrat, wantVersion, wantParams := tunedPassphraseStrategy(t, oldPassword)
	cfg := BarrierConfig{RootKeyPath: "test/root-key"}
	b, err := NewStorageBarrier(testLogger(), NewMemoryBackend(), cfg, oldStrat)
	require.NoError(t, err)
	require.NoError(t, b.Initialize(ctx, MustCredentials([]byte(oldPassword))))

	require.NoError(t, b.ChangePassword(ctx, MustCredentials([]byte(newPassword))))

	// The replacement strategy reports the factor it was built from — the
	// exported accessors, not the constructor call.
	replaced, ok := b.strategyByID(StrategyPassphrase)
	require.True(t, ok, "passphrase strategy must still be registered")
	software, ok := replaced.(*SoftwareStrategy)
	require.True(t, ok, "replacement must be a SoftwareStrategy, got %T", replaced)
	assert.Equal(t, wantVersion, software.KDFVersion(), "KDF version changed across the password change")
	assert.Equal(t, wantParams, software.KDFParams(), "KDF work factor changed across the password change")
	assert.NotSame(t, oldStrat, software, "the strategy must have been replaced, not left in place")
}

// TestChangePassword_TunedBlobUnsealsOnRestart is the behavioural half: after
// a password change the persisted blob must be unsealable by a fresh strategy
// carrying the ORIGINAL work factor — which is what a restarted deployment
// constructs from its configuration — and must NOT be unsealable by one at the
// library defaults, because that is the blob the old code wrote.
func TestChangePassword_TunedBlobUnsealsOnRestart(t *testing.T) {
	ctx := context.Background()
	const oldPassword, newPassword = "old-password-123", "new-password-456"
	base := NewMemoryBackend()
	cfg := BarrierConfig{RootKeyPath: "test/root-key"}

	oldStrat, _, _ := tunedPassphraseStrategy(t, oldPassword)
	b, err := NewStorageBarrier(testLogger(), base, cfg, oldStrat)
	require.NoError(t, err)
	require.NoError(t, b.Initialize(ctx, MustCredentials([]byte(oldPassword))))
	require.NoError(t, b.ChangePassword(ctx, MustCredentials([]byte(newPassword))))
	require.NoError(t, b.Seal())

	// Restart with the deployment's configured (tuned) factor and the new
	// password: unseals.
	tuned, _, _ := tunedPassphraseStrategy(t, newPassword)
	b2, err := NewStorageBarrier(testLogger(), base, cfg, tuned)
	require.NoError(t, err)
	require.NoError(t, b2.Unseal(ctx, MustCredentials([]byte(newPassword))))
	assert.False(t, b2.IsSealed())

	// Restart at the library DEFAULTS with the new password: refused. This is
	// the assertion that fails against the old code, which wrote a blob only
	// a default-factor strategy could open.
	defaults, err := NewSoftwareStrategy([]byte(newPassword))
	require.NoError(t, err)
	b3, err := NewStorageBarrier(testLogger(), base, cfg, defaults)
	require.NoError(t, err)
	assert.Error(t, b3.Unseal(ctx, MustCredentials([]byte(newPassword))),
		"a default-factor strategy must not open a blob sealed at the tuned factor")
}

// TestChangePassword_NonSoftwarePassphraseStrategyIsRefused pins the refusal:
// a strategy of another type registered under StrategyPassphrase has no KDF
// parameters to preserve, and re-sealing under defaults instead would be the
// silent reset this fix removes.
func TestChangePassword_NonSoftwarePassphraseStrategyIsRefused(t *testing.T) {
	ctx := context.Background()
	cfg := BarrierConfig{RootKeyPath: "test/root-key"}
	b, err := NewStorageBarrier(testLogger(), NewMemoryBackend(), cfg, testSoftwareStrategy(t))
	require.NoError(t, err)
	require.NoError(t, b.Initialize(ctx, MustCredentials([]byte("init-password"))))

	// Swap the registered passphrase strategy for a non-software one.
	b.strategiesMu.Lock()
	b.strategies[StrategyPassphrase] = &passphraseIDNoneStrategy{SealingStrategy: NewNoneStrategy()}
	b.strategiesMu.Unlock()

	err = b.ChangePassword(ctx, MustCredentials([]byte("new-password")))
	assert.ErrorIs(t, err, ErrPassphraseStrategyNotSoftware)
}

// passphraseIDNoneStrategy is a none strategy that answers StrategyPassphrase
// for its ID — the composition error the refusal exists for.
type passphraseIDNoneStrategy struct{ SealingStrategy }

func (passphraseIDNoneStrategy) ID() StrategyID { return StrategyPassphrase }
