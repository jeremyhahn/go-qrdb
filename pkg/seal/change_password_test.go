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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChangePassword_Success(t *testing.T) {
	ctx := context.Background()
	oldPassword := "old-password-123"
	newPassword := "new-password-456"

	strat := testSoftwareStrategy(t)
	cfg := BarrierConfig{RootKeyPath: "test/root-key"}
	b, err := NewStorageBarrier(testLogger(), NewMemoryBackend(), cfg, strat)
	require.NoError(t, err)

	// Initialize with the old password.
	err = b.Initialize(ctx, Credentials{Secret: oldPassword})
	require.NoError(t, err)

	// Store a value while unsealed so we can verify data survives the change.
	err = b.Put(ctx, "test/data", []byte("secret-payload"))
	require.NoError(t, err)

	// Change the password.
	err = b.ChangePassword(ctx, Credentials{Secret: newPassword})
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
	err = b.Unseal(ctx, Credentials{Secret: newPassword})
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

	err = b.Initialize(ctx, Credentials{Secret: oldPassword})
	require.NoError(t, err)

	err = b.ChangePassword(ctx, Credentials{Secret: newPassword})
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

	err = b2.Unseal(ctx, Credentials{Secret: oldPassword})
	assert.Error(t, err, "unseal with old password strategy should fail after password change")

	// Create a third barrier with the NEW password strategy -- it succeeds.
	newStrat, err := NewSoftwareStrategy([]byte(newPassword))
	require.NoError(t, err)

	b3, err := NewStorageBarrier(testLogger(), base, cfg, newStrat)
	require.NoError(t, err)

	err = b3.Unseal(ctx, Credentials{Secret: newPassword})
	require.NoError(t, err)
	assert.False(t, b3.IsSealed())
}

func TestChangePassword_Sealed(t *testing.T) {
	cfg := BarrierConfig{RootKeyPath: "test/root-key"}
	b, err := NewStorageBarrier(testLogger(), NewMemoryBackend(), cfg, testSoftwareStrategy(t))
	require.NoError(t, err)

	// Barrier is sealed (never initialized) -- should return ErrSealed.
	err = b.ChangePassword(context.Background(), Credentials{Secret: "new-password"})
	assert.ErrorIs(t, err, ErrSealed)
}

func TestChangePassword_EmptyPassword(t *testing.T) {
	ctx := context.Background()
	cfg := BarrierConfig{RootKeyPath: "test/root-key"}
	b, err := NewStorageBarrier(testLogger(), NewMemoryBackend(), cfg, testSoftwareStrategy(t))
	require.NoError(t, err)

	err = b.Initialize(ctx, Credentials{Secret: "init-password"})
	require.NoError(t, err)

	err = b.ChangePassword(ctx, Credentials{Secret: ""})
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

	err = b.Initialize(ctx, Credentials{Secret: "hw-password"})
	require.NoError(t, err)

	err = b.ChangePassword(ctx, Credentials{Secret: "new-password"})
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

	err = b.Initialize(ctx, Credentials{Secret: "password"})
	require.NoError(t, err)

	err = b.ChangePassword(ctx, Credentials{Secret: "new-password"})
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

	err = b.Initialize(ctx, Credentials{Secret: "password-a"})
	require.NoError(t, err)

	// Capture the sealed blob before the change.
	blobBefore, err := base.Get(ctx, "test/root-key")
	require.NoError(t, err)

	err = b.ChangePassword(ctx, Credentials{Secret: "password-b"})
	require.NoError(t, err)

	// The sealed blob must have changed in the base backend.
	blobAfter, err := base.Get(ctx, "test/root-key")
	require.NoError(t, err)
	assert.NotEqual(t, blobBefore, blobAfter,
		"sealed root key blob should differ after password change")
}
