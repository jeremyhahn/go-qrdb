package credentials

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readClaim returns the raw migration claim stored under name.
func readClaim(t *testing.T, svc *CredentialService, name string) []byte {
	t.Helper()
	raw, err := svc.store.Get(context.Background(), MigrationClaimPrefix+name)
	require.NoError(t, err)
	return raw
}

// requireTombstone asserts the claim under name is a settled deletion
// tombstone and returns its raw bytes.
func requireTombstone(t *testing.T, svc *CredentialService, name string) []byte {
	t.Helper()
	raw := readClaim(t, svc, name)
	var claim migrationClaim
	require.NoError(t, json.Unmarshal(raw, &claim))
	assert.True(t, claim.Complete, "the tombstone is settled")
	assert.True(t, claim.Deleted, "the claim is a deletion tombstone")
	return raw
}

func TestDeleteCredential_DeletionFenceBranches(t *testing.T) {
	t.Run("claim unreadable", func(t *testing.T) {
		ctx := context.Background()
		svc, backend := newFaultBarrierService(t)
		seedGraceVersion(t, svc, "k")
		arm(&backend.failGet, MigrationClaimPrefix+"k")

		err := svc.DeleteCredential(ctx, "k")
		requireOperationError(t, err, "read migration claim")
		assert.ErrorIs(t, err, errInjectedFault)

		backend.failGet.Store(nil)
		assert.True(t, keyExists(t, svc, credentialPrefix+"k"), "the value is kept")
		assert.True(t, keyExists(t, svc, metaKey("k")), "the metadata is kept")
		assert.True(t, keyExists(t, svc, versionKey("k", 1)), "the retained version is kept")
		assert.Equal(t, "v2", retrieveString(t, svc, "k"))
	})

	t.Run("claim undecodable", func(t *testing.T) {
		ctx := context.Background()
		svc, _ := newFaultBarrierService(t)
		seedGraceVersion(t, svc, "k")
		garbage := []byte("not a claim")
		require.NoError(t, svc.store.Put(ctx, MigrationClaimPrefix+"k", garbage))

		require.NoError(t, svc.DeleteCredential(ctx, "k"))
		assert.False(t, keyExists(t, svc, credentialPrefix+"k"), "the value is deleted")
		assert.False(t, keyExists(t, svc, metaKey("k")), "the metadata is deleted")
		assert.False(t, keyExists(t, svc, versionKey("k", 1)), "the retained version is deleted")
		assert.Equal(t, garbage, readClaim(t, svc, "k"), "an undecodable claim is left unchanged")
	})

	t.Run("already a tombstone", func(t *testing.T) {
		ctx := context.Background()
		svc, _ := newFaultBarrierService(t)
		require.NoError(t, svc.StoreCredential(ctx, "k", []byte("v1")))
		require.NoError(t, svc.DeleteCredential(ctx, "k"))
		first := requireTombstone(t, svc, "k")

		require.NoError(t, svc.StoreCredential(ctx, "k", []byte("v2")))
		require.NoError(t, svc.DeleteCredential(ctx, "k"))
		assert.False(t, keyExists(t, svc, credentialPrefix+"k"), "the second value is deleted")
		assert.Equal(t, first, requireTombstone(t, svc, "k"), "the tombstone is not rewritten")

		// Nothing is left under the name but the tombstone: the delete
		// writes nothing and reports success.
		require.NoError(t, svc.DeleteCredential(ctx, "k"))
		assert.Equal(t, first, requireTombstone(t, svc, "k"), "a delete of an absent name leaves the tombstone")
	})
}
