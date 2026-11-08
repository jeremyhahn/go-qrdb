package seal

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// The tests in this file drive the root-key read-modify-writes other than
// ChangePassword (see change_password_race_test.go): a share redistribution's
// record of its Shamir parameters and a migration's adoption of its target's
// key. The rootKeyWritten seam runs the interleaving operation inline between
// the write and the adoption, so each test is deterministic and needs no
// goroutines or sleeps.

// persistedSealed decodes the root-key blob b's backend holds.
func persistedSealed(t *testing.T, b *StorageBarrier) SealedRootKey {
	t.Helper()
	var sealed SealedRootKey
	require.NoError(t, json.Unmarshal(persistedRootKeyBlob(t, b), &sealed))
	return sealed
}

// requireShamirDiskMatchesLive fails unless the persisted blob records the
// live barrier's epoch state and the given Shamir parameters, and the live
// state records the same parameters. A Shamir barrier holds no root-key
// ciphertext in this blob, so requireDiskMatchesLive does not apply.
func requireShamirDiskMatchesLive(t *testing.T, b *StorageBarrier, threshold, total int) {
	t.Helper()
	disk := persistedSealed(t, b)
	live := b.barrier.SealedRootKeyData()
	require.NotNil(t, live)
	require.Equal(t, disk.Epoch, live.Epoch)
	require.Equal(t, disk.KnownEpochs, live.KnownEpochs)
	require.Equal(t, threshold, disk.ShamirThreshold, "disk lost the recorded threshold")
	require.Equal(t, total, disk.ShamirTotal)
	require.Equal(t, threshold, live.ShamirThreshold, "the live state lost the recorded threshold")
	require.Equal(t, total, live.ShamirTotal)
}

// newUnsealedShamirBarrier returns an initialized 2-of-3 Shamir barrier,
// unsealed by its initialization.
func newUnsealedShamirBarrier(t *testing.T) *StorageBarrier {
	t.Helper()
	b, _, _ := newUninitializedShamirBarrier(t, 2, 3)
	_, err := b.InitializeShamir(t.Context(), "")
	require.NoError(t, err)
	require.False(t, b.IsSealed())
	return b
}

func TestRecordShamirParams_LaterRotateKeepsTheParameters(t *testing.T) {
	ctx := t.Context()
	b := newUnsealedShamirBarrier(t)

	require.NoError(t, b.recordShamirParams(ctx, 3, 5))
	_, err := b.barrier.Rotate(ctx)
	require.NoError(t, err)
	requireShamirDiskMatchesLive(t, b, 3, 5)
}

func TestRecordShamirParams_RotateInsideTheWindowIsRefused(t *testing.T) {
	ctx := t.Context()
	b := newUnsealedShamirBarrier(t)

	var rotateErr error
	b.rootKeyWritten = func() {
		_, rotateErr = b.barrier.Rotate(ctx)
	}
	require.NoError(t, b.recordShamirParams(ctx, 3, 5))
	require.ErrorIs(t, rotateErr, ErrRootKeyWriteInProgress)
	requireShamirDiskMatchesLive(t, b, 3, 5)

	// The refused rotation retries, as go-quicraft's scheduled one does on
	// its next tick, and keeps the recorded parameters.
	b.rootKeyWritten = nil
	_, err := b.barrier.Rotate(ctx)
	require.NoError(t, err)
	requireShamirDiskMatchesLive(t, b, 3, 5)
}

// newAdoptingPassphraseMigration returns a StrategySystemBarrier source
// barrier, which encrypts and so rotates, and a passphrase sibling minted to
// replace it.
func newAdoptingPassphraseMigration(t *testing.T) (src, sib *StorageBarrier) {
	t.Helper()
	ctx := t.Context()
	sys, _ := newNoneBarrier(t)
	t.Cleanup(func() { _ = sys.Close() })
	src, err := NewStorageBarrier(testLogger(), NewMemoryBackend(),
		BarrierConfig{RootKeyPath: "tenant"}, NewSystemBarrierStrategy(sys))
	require.NoError(t, err)
	t.Cleanup(func() { _ = src.Close() })
	require.NoError(t, src.Initialize(ctx, Credentials{}))
	_, err = src.barrier.Rotate(ctx)
	require.NoError(t, err, "the source must rotate for these tests to mean anything")
	sib, err = src.MintKeyedSibling(ctx, ".migrating", NewDeferredSoftwareStrategy(), MustCredentials(ctx, []byte("pw")))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sib.Close() })
	return src, sib
}

// requireAdoptedAndReopens fails unless src's canonical blob is the keyed one
// and it reopens under the migration's passphrase.
func requireAdoptedAndReopens(t *testing.T, src *StorageBarrier) {
	t.Helper()
	ctx := t.Context()
	require.Equal(t, StrategyPassphrase, persistedSealed(t, src).Strategy, "the source's key was written back over the migrated one")
	keyed, err := src.ReopenKeyed(ctx, NewDeferredSoftwareStrategy(), MustCredentials(ctx, []byte("pw")))
	require.NoError(t, err)
	require.False(t, keyed.IsSealed())
}

func TestAdoptMigratedRootKey_LaterSourceRotateIsRefused(t *testing.T) {
	ctx := t.Context()
	src, sib := newAdoptingPassphraseMigration(t)

	require.NoError(t, src.AdoptMigratedRootKey(ctx, sib))
	_, err := src.barrier.Rotate(ctx)
	require.ErrorIs(t, err, ErrRootKeyAdopted)
	requireAdoptedAndReopens(t, src)
}

func TestAdoptMigratedRootKey_SourceRotateInsideTheWindowIsRefused(t *testing.T) {
	ctx := t.Context()
	src, sib := newAdoptingPassphraseMigration(t)

	var rotateErr error
	src.rootKeyWritten = func() {
		_, rotateErr = src.barrier.Rotate(ctx)
	}
	require.NoError(t, src.AdoptMigratedRootKey(ctx, sib))
	require.ErrorIs(t, rotateErr, ErrRootKeyWriteInProgress)
	requireAdoptedAndReopens(t, src)
}

// TestAdoptMigratedRootKey_PersisterCrossingTheAdoptionIsRefused drives a
// source rotation whose persister is entered before the adoption begins and
// takes rootKeyRewrap only after it has finished: the rootKeyPersisting seam
// runs the whole adoption inline at that point. The persister must see the
// adoption it waited behind and leave the migrated key on disk.
func TestAdoptMigratedRootKey_PersisterCrossingTheAdoptionIsRefused(t *testing.T) {
	ctx := t.Context()
	src, sib := newAdoptingPassphraseMigration(t)

	var adoptErr error
	*src.rootKeyPersisting = func() {
		*src.rootKeyPersisting = nil
		adoptErr = src.AdoptMigratedRootKey(ctx, sib)
	}
	_, err := src.barrier.Rotate(ctx)
	require.NoError(t, adoptErr)
	require.ErrorIs(t, err, ErrRootKeyAdopted)
	requireAdoptedAndReopens(t, src)
}
