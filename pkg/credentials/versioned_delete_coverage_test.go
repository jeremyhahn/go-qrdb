package credentials

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// keyExists reports whether key is present in svc's store.
func keyExists(t *testing.T, svc *CredentialService, key string) bool {
	t.Helper()
	_, err := svc.store.Get(context.Background(), key)
	if errors.Is(err, seal.ErrSecretNotFound) {
		return false
	}
	require.NoError(t, err)
	return true
}

// loseEveryCAS makes every conditional write naming a key ending in suffix
// report a lost race, so a retry loop over it runs out.
func loseEveryCAS(b *faultBackend, suffix string) {
	b.casLosses.Store(math.MaxInt32)
	arm(&b.loseCAS, suffix)
}

func TestDeleteBarrierHead_Faults(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		fault func(b *faultBackend)
		op    string
		cause error
	}{
		{name: "read meta", fault: func(b *faultBackend) { arm(&b.failGet, metaKey("k")) }, op: "read meta", cause: errInjectedFault},
		{name: "read current", fault: func(b *faultBackend) { arm(&b.failGet, credentialPrefix+"k") }, op: "read current", cause: errInjectedFault},
		{name: "contended", fault: func(b *faultBackend) { loseEveryCAS(b, metaKey("k")) }, op: "delete current", cause: ErrRotationMetaContended},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, backend := newFaultBarrierService(t)
			seedGraceVersion(t, svc, "k")
			tc.fault(backend)

			err := svc.deleteBarrierHead(ctx, "k")
			requireOperationError(t, err, tc.op)
			assert.ErrorIs(t, err, tc.cause)

			// The value and its metadata go together or not at all.
			backend.failGet.Store(nil)
			assert.True(t, keyExists(t, svc, metaKey("k")))
			assert.True(t, keyExists(t, svc, credentialPrefix+"k"))
		})
	}
}

func TestDeleteBarrierHead_EmptyValueDeleted(t *testing.T) {
	ctx := context.Background()
	svc, _ := newFaultBarrierService(t)
	require.NoError(t, svc.store.Put(ctx, credentialPrefix+"e", []byte{}))

	require.NoError(t, svc.deleteBarrierHead(ctx, "e"))
	assert.False(t, keyExists(t, svc, credentialPrefix+"e"))
}

func TestDeleteBarrierStaged_ListFailureAndForeignKeys(t *testing.T) {
	ctx := context.Background()
	prefix := StagedKeyPrefix + encodedName("k") + "/"

	t.Run("list fails", func(t *testing.T) {
		svc, backend := newFaultBarrierService(t)
		arm(&backend.failList, prefix)
		err := svc.deleteBarrierStaged(ctx, "k", math.MaxUint64)
		requireOperationError(t, err, "list staged rotations")
		assert.ErrorIs(t, err, errInjectedFault)
	})

	t.Run("only staged versions up to the bound go", func(t *testing.T) {
		svc, _ := newFaultBarrierService(t)
		for _, k := range []string{stagedKey("k", 1), stagedKey("k", 9), prefix + "x", prefix + "01"} {
			require.NoError(t, svc.store.Put(ctx, k, []byte("staged")))
		}
		require.NoError(t, svc.deleteBarrierStaged(ctx, "k", 5))
		assert.False(t, keyExists(t, svc, stagedKey("k", 1)))
		assert.True(t, keyExists(t, svc, stagedKey("k", 9)))
		assert.True(t, keyExists(t, svc, prefix+"x"))
		assert.True(t, keyExists(t, svc, prefix+"01"))
	})
}

func TestAbandonScheduled_LeavesValueOnFault(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		fault func(b *faultBackend)
	}{
		{name: "read meta", fault: func(b *faultBackend) { arm(&b.failGet, metaKey("k")) }},
		{name: "contended", fault: func(b *faultBackend) { loseEveryCAS(b, stagedKey("k", 7)) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, backend := newFaultBarrierService(t)
			seedGraceVersion(t, svc, "k")
			require.NoError(t, svc.store.Put(ctx, stagedKey("k", 7), []byte("staged")))
			tc.fault(backend)

			svc.abandonScheduled(ctx, "k", 7)

			backend.failGet.Store(nil)
			assert.True(t, keyExists(t, svc, stagedKey("k", 7)))
		})
	}
}

func TestReapBarrierVersions_Contended(t *testing.T) {
	ctx := context.Background()
	svc, backend := newFaultBarrierService(t)
	seedGraceVersion(t, svc, "k")
	loseEveryCAS(backend, metaKey("k"))

	n, err := svc.reapBarrierVersions(ctx, "k", func(m *credentialMeta) ([]credentialMetaEntry, bool) {
		return m.History, true
	})
	assert.Zero(t, n)
	requireOperationError(t, err, "write meta")
	assert.ErrorIs(t, err, ErrRotationMetaContended)
	assert.True(t, keyExists(t, svc, versionKey("k", 1)))
}

func TestBarrierCurrentReadFailures(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		call func(svc *CredentialService) error
	}{
		{name: "list versions", call: func(svc *CredentialService) error {
			_, err := svc.listVersionsBarrier(ctx, "k")
			return err
		}},
		{name: "rotation meta", call: func(svc *CredentialService) error {
			_, err := svc.rotationMeta(ctx, "k")
			return err
		}},
		{name: "policy write", call: func(svc *CredentialService) error {
			_, err := svc.tryPolicyMetaWrite(ctx, "k", metaKey("k"), true, func(*credentialMeta) {})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, backend := newFaultBarrierService(t)
			seedGraceVersion(t, svc, "k")
			arm(&backend.failGet, credentialPrefix+"k")

			err := tc.call(svc)
			requireOperationError(t, err, "read current")
			assert.ErrorIs(t, err, errInjectedFault)
		})
	}
}

func TestTryPolicyMetaWrite_MetaReadFailure(t *testing.T) {
	ctx := context.Background()
	svc, backend := newFaultBarrierService(t)
	seedGraceVersion(t, svc, "k")
	arm(&backend.failGet, metaKey("k"))

	applied, err := svc.tryPolicyMetaWrite(ctx, "k", metaKey("k"), false, func(*credentialMeta) {
		t.Fatal("fn must not run over metadata that could not be read")
	})
	assert.False(t, applied)
	requireOperationError(t, err, "read meta")
	assert.ErrorIs(t, err, errInjectedFault)
}

func TestTryPolicyMetaWrite_EmptyCurrentValue(t *testing.T) {
	ctx := context.Background()
	svc, _ := newFaultBarrierService(t)
	require.NoError(t, svc.store.Put(ctx, credentialPrefix+"e", []byte{}))

	applied, err := svc.tryPolicyMetaWrite(ctx, "e", metaKey("e"), false, func(m *credentialMeta) {
		m.RotationOp = "op"
	})
	require.NoError(t, err)
	assert.True(t, applied)
	meta, err := svc.loadBarrierMeta(ctx, "e")
	require.NoError(t, err)
	assert.Equal(t, "op", meta.RotationOp)
}

func TestReapSpentStaged_Faults(t *testing.T) {
	ctx := context.Background()
	prefix := StagedKeyPrefix + encodedName("k") + "/"
	pending := func(t *testing.T, svc *CredentialService) {
		t.Helper()
		require.NoError(t, svc.updateBarrierMeta(ctx, "k", "mark cleanup", func(m *credentialMeta) bool {
			m.PendingCleanup = "claim"
			return true
		}))
		require.NoError(t, svc.store.Put(ctx, stagedKey("k", 1), []byte("staged")))
	}
	for _, tc := range []struct {
		name  string
		fault func(b *faultBackend)
		op    string
		cause error
	}{
		{name: "read meta", fault: func(b *faultBackend) { arm(&b.failGet, metaKey("k")) }, op: "read meta", cause: errInjectedFault},
		{name: "list", fault: func(b *faultBackend) { arm(&b.failList, prefix) }, op: "list staged rotations", cause: errInjectedFault},
		{name: "contended", fault: func(b *faultBackend) { loseEveryCAS(b, stagedKey("k", 1)) }, op: "delete staged rotation", cause: ErrRotationMetaContended},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, backend := newFaultBarrierService(t)
			seedGraceVersion(t, svc, "k")
			pending(t, svc)
			tc.fault(backend)

			err := svc.reapSpentStaged(ctx, "k", "claim")
			requireOperationError(t, err, tc.op)
			assert.ErrorIs(t, err, tc.cause)

			backend.failGet.Store(nil)
			assert.True(t, keyExists(t, svc, stagedKey("k", 1)))
		})
	}
}

func TestUpdateBarrierMeta_AbsentAndContended(t *testing.T) {
	ctx := context.Background()

	t.Run("absent record is created", func(t *testing.T) {
		svc, _ := newFaultBarrierService(t)
		require.NoError(t, svc.updateBarrierMeta(ctx, "fresh", "set op", func(m *credentialMeta) bool {
			m.RotationOp = "op"
			return true
		}))
		meta, err := svc.loadBarrierMeta(ctx, "fresh")
		require.NoError(t, err)
		assert.Equal(t, "op", meta.RotationOp)
	})

	t.Run("contended", func(t *testing.T) {
		svc, backend := newFaultBarrierService(t)
		seedGraceVersion(t, svc, "k")
		loseEveryCAS(backend, metaKey("k"))

		err := svc.updateBarrierMeta(ctx, "k", "set op", func(m *credentialMeta) bool {
			m.RotationOp = "op"
			return true
		})
		requireOperationError(t, err, "set op")
		assert.ErrorIs(t, err, ErrRotationMetaContended)
	})
}
