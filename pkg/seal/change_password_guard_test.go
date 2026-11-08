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
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// levelCapture is a slog.Handler recording every record at or above Error.
type levelCapture struct {
	mu     sync.Mutex
	errors []string
}

func (c *levelCapture) Enabled(context.Context, slog.Level) bool { return true }

func (c *levelCapture) Handle(_ context.Context, r slog.Record) error {
	if r.Level >= slog.LevelError {
		c.mu.Lock()
		c.errors = append(c.errors, r.Message)
		c.mu.Unlock()
	}
	return nil
}

func (c *levelCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *levelCapture) WithGroup(string) slog.Handler      { return c }

func (c *levelCapture) errorRecords() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.errors...)
}

// captureDefaultLog routes the process-wide slog default, which go-quicraft's
// barrier logs its load-generation fence through, into a capture for the
// test's lifetime. Not parallel-safe, so the tests using it are not parallel.
func captureDefaultLog(t *testing.T) *levelCapture {
	t.Helper()
	c := &levelCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(c))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return c
}

func newDeferredPassphraseBarrier(t *testing.T, base StorageBackend, passphrase string) *StorageBarrier {
	t.Helper()
	cfg := BarrierConfig{RootKeyPath: "test/root-key"}
	b, err := NewStorageBarrier(testLogger(), base, cfg, cheapDeferredStrategy(t))
	require.NoError(t, err)
	require.NoError(t, b.Initialize(t.Context(), MustCredentials(t.Context(), []byte(passphrase))))
	return b
}

// cheapDeferredStrategy is a credential-checking strategy at the cheap test
// work factor of cheapSoftwareStrategy.
func cheapDeferredStrategy(t *testing.T) *SoftwareStrategy {
	t.Helper()
	bound := cheapSoftwareStrategy(t, "unused")
	s, err := NewDeferredSoftwareStrategyWithParams(bound.KDFVersion(), bound.KDFParams())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func persistedRootKeyBlob(t *testing.T, b *StorageBarrier) []byte {
	t.Helper()
	blob, err := b.base.Get(t.Context(), b.config.RootKeyPath)
	require.NoError(t, err)
	return blob
}

// TestChangePassword_WrongCurrentIsRefusedAndWritesNothing pins #631: holding
// an unsealed barrier is not knowing its passphrase.
func TestChangePassword_WrongCurrentIsRefusedAndWritesNothing(t *testing.T) {
	ctx := t.Context()
	b := newDeferredPassphraseBarrier(t, NewMemoryBackend(), "current-passphrase")
	before := persistedRootKeyBlob(t, b)

	err := b.ChangePassword(ctx, MustCredentials(ctx, []byte("not-the-passphrase")), MustCredentials(ctx, []byte("next-passphrase")))
	require.ErrorIs(t, err, ErrCredentialMismatch)
	require.True(t, bytes.Equal(before, persistedRootKeyBlob(t, b)), "a refused change wrote the blob")

	// The barrier still opens under the passphrase it had.
	require.NoError(t, b.Seal())
	require.NoError(t, b.Unseal(ctx, MustCredentials(ctx, []byte("current-passphrase"))))
}

// TestChangePassword_CarriesLoadGenerationAndLogsNoError is the #631
// reproduction: the re-wrapped blob dropped the load generation, so the next
// load of it logged "load generation went BACKWARDS" at error level.
func TestChangePassword_CarriesLoadGenerationAndLogsNoError(t *testing.T) {
	ctx := t.Context()
	logs := captureDefaultLog(t)
	b := newDeferredPassphraseBarrier(t, NewMemoryBackend(), "current-passphrase")
	claimed := b.barrier.LoadGeneration()
	require.NotZero(t, claimed, "the fence under test needs a claimed generation")

	require.NoError(t, b.ChangePassword(ctx, MustCredentials(ctx, []byte("current-passphrase")), MustCredentials(ctx, []byte("next-passphrase"))))

	var sealed SealedRootKey
	require.NoError(t, json.Unmarshal(persistedRootKeyBlob(t, b), &sealed))
	require.Equal(t, claimed, sealed.LoadGeneration)

	require.NoError(t, b.Seal())
	require.Error(t, b.Unseal(ctx, MustCredentials(ctx, []byte("current-passphrase"))), "the old passphrase still opens the barrier")
	require.NoError(t, b.Unseal(ctx, MustCredentials(ctx, []byte("next-passphrase"))))
	require.Empty(t, logs.errorRecords())
}

// TestChangePassword_RefusesABaseWithoutAGuardedSwap pins that the write is
// never degraded to an unguarded Put.
func TestChangePassword_RefusesABaseWithoutAGuardedSwap(t *testing.T) {
	ctx := t.Context()
	b := newDeferredPassphraseBarrier(t, NewMemoryBackend(), "current-passphrase")
	before := persistedRootKeyBlob(t, b)
	b.base = unfencedMemory{b.base}

	err := b.ChangePassword(ctx, MustCredentials(ctx, []byte("current-passphrase")), MustCredentials(ctx, []byte("next-passphrase")))
	require.True(t, errors.Is(err, ErrGuardedSwapUnsupported), "got %v", err)
	require.True(t, bytes.Equal(before, persistedRootKeyBlob(t, b)))
}

// TestChangePassword_RefusesAnUninitializedOrForeignBlob covers the reads
// ahead of the check.
func TestChangePassword_RefusesAnUninitializedOrForeignBlob(t *testing.T) {
	ctx := t.Context()
	current := MustCredentials(ctx, []byte("current-passphrase"))
	next := MustCredentials(ctx, []byte("next-passphrase"))

	t.Run("blob deleted", func(t *testing.T) {
		b := newDeferredPassphraseBarrier(t, NewMemoryBackend(), "current-passphrase")
		require.NoError(t, b.base.Delete(ctx, b.config.RootKeyPath))
		require.ErrorIs(t, b.ChangePassword(ctx, current, next), ErrNotInitialized)
	})
	t.Run("blob corrupt", func(t *testing.T) {
		b := newDeferredPassphraseBarrier(t, NewMemoryBackend(), "current-passphrase")
		require.NoError(t, b.base.Put(ctx, b.config.RootKeyPath, []byte("{")))
		require.ErrorIs(t, b.ChangePassword(ctx, current, next), ErrCorruptRootKey)
	})
	t.Run("blob under another strategy", func(t *testing.T) {
		b := newDeferredPassphraseBarrier(t, NewMemoryBackend(), "current-passphrase")
		var sealed SealedRootKey
		require.NoError(t, json.Unmarshal(persistedRootKeyBlob(t, b), &sealed))
		sealed.Strategy = StrategyShamir
		blob, err := json.Marshal(&sealed)
		require.NoError(t, err)
		require.NoError(t, b.base.Put(ctx, b.config.RootKeyPath, blob))
		require.ErrorIs(t, b.ChangePassword(ctx, current, next), ErrStrategyMismatch)
	})
	t.Run("no current", func(t *testing.T) {
		b := newDeferredPassphraseBarrier(t, NewMemoryBackend(), "current-passphrase")
		require.ErrorIs(t, b.ChangePassword(ctx, Credentials{}, next), ErrInvalidPassphrase)
	})
}
