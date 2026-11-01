package seal

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// The tests in this file drive the window between ChangePassword's guarded
// write of the root-key blob and its adoption of the new state into the live
// barrier (#631). The rootKeyWritten seam runs the interleaving
// operation inline at exactly that point, so each test is deterministic and
// needs no goroutines or sleeps.

// requireDiskMatchesLive fails unless the persisted blob wraps the root key in
// the ciphertext the live barrier holds, at the same epoch state. A mismatch
// is what lets go-quicraft's Rotate, which persists a clone of the live
// state, put a revoked wrapper back on disk.
func requireDiskMatchesLive(t *testing.T, b *StorageBarrier) {
	t.Helper()
	var disk SealedRootKey
	require.NoError(t, json.Unmarshal(persistedRootKeyBlob(t, b), &disk))
	live := b.barrier.SealedRootKeyData()
	require.NotNil(t, live)
	require.Equal(t, disk.Ciphertext, live.Ciphertext, "disk and live barrier wrap the root key differently")
	require.Equal(t, disk.Epoch, live.Epoch)
	require.Equal(t, disk.KnownEpochs, live.KnownEpochs)
}

// requireOpensUnder seals b and unseals it under passphrase.
func requireOpensUnder(t *testing.T, b *StorageBarrier, passphrase string) {
	t.Helper()
	require.NoError(t, b.Seal())
	require.NoError(t, b.Unseal(t.Context(), MustCredentials(t.Context(), []byte(passphrase))))
}

func TestChangePassword_SecondChangeInsideTheWindowIsRefused(t *testing.T) {
	ctx := t.Context()
	b := newDeferredPassphraseBarrier(t, NewMemoryBackend(), "p0")

	var second error
	b.rootKeyWritten = func() {
		b.rootKeyWritten = nil
		second = b.ChangePassword(ctx, MustCredentials(ctx, []byte("p1")), MustCredentials(ctx, []byte("p2")))
	}
	require.NoError(t, b.ChangePassword(ctx, MustCredentials(ctx, []byte("p0")), MustCredentials(ctx, []byte("p1"))))
	require.ErrorIs(t, second, ErrRootKeyWriteInProgress)
	requireDiskMatchesLive(t, b)

	// A rotation persists a clone of the live state; it must keep p1.
	_, err := b.barrier.Rotate(ctx)
	require.NoError(t, err)
	requireDiskMatchesLive(t, b)
	requireOpensUnder(t, b, "p1")
}

func TestChangePassword_RotateInsideTheWindowIsRefused(t *testing.T) {
	ctx := t.Context()
	b := newDeferredPassphraseBarrier(t, NewMemoryBackend(), "p0")

	var rotateErr error
	b.rootKeyWritten = func() {
		_, rotateErr = b.barrier.Rotate(ctx)
	}
	require.NoError(t, b.ChangePassword(ctx, MustCredentials(ctx, []byte("p0")), MustCredentials(ctx, []byte("p1"))))
	require.ErrorIs(t, rotateErr, ErrRootKeyWriteInProgress)
	requireDiskMatchesLive(t, b)

	b.rootKeyWritten = nil
	_, err := b.barrier.Rotate(ctx)
	require.NoError(t, err)
	requireDiskMatchesLive(t, b)
	requireOpensUnder(t, b, "p1")
}

func TestChangePassword_PurgeInsideTheWindowIsRefused(t *testing.T) {
	ctx := t.Context()
	b := newDeferredPassphraseBarrier(t, NewMemoryBackend(), "p0")
	epoch, err := b.barrier.Rotate(ctx)
	require.NoError(t, err)

	var purgeErr error
	b.rootKeyWritten = func() {
		_, purgeErr = b.barrier.PurgeEpochsBefore(ctx, epoch)
	}
	require.NoError(t, b.ChangePassword(ctx, MustCredentials(ctx, []byte("p0")), MustCredentials(ctx, []byte("p1"))))
	require.ErrorIs(t, purgeErr, ErrRootKeyWriteInProgress)
	requireDiskMatchesLive(t, b)

	b.rootKeyWritten = nil
	purged, err := b.barrier.PurgeEpochsBefore(ctx, epoch)
	require.NoError(t, err)
	require.Positive(t, purged)
	requireDiskMatchesLive(t, b)
	requireOpensUnder(t, b, "p1")
}
