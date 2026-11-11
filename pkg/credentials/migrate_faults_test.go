package credentials

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// disarmAll clears every fault and hook a faultBackend holds.
func disarmAll(b *faultBackend) {
	for _, p := range []*atomic.Pointer[string]{&b.failGet, &b.failPut, &b.failDelete, &b.failList, &b.failCAS, &b.loseCAS} {
		p.Store(nil)
	}
	b.onGet, b.onCAS, b.onPut, b.onDelete = nil, nil, nil, nil
}

// A migration that fails on the replicated store leaves the legacy copy
// untouched, reports the storage cause, and completes once the store
// answers again.
func TestMigrateLegacy_ReplicatedStoreFaultsAreRetried(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		live  bool
		fault func(*faultBackend)
	}{
		{name: "list replicated value", fault: func(b *faultBackend) { arm(&b.failList, credentialPrefix+"db") }},
		{name: "list replicated versions", fault: func(b *faultBackend) { arm(&b.failList, VersionKeyPrefix+encodedName("db")+"/") }},
		{name: "write claim", fault: func(b *faultBackend) { arm(&b.failCAS, MigrationClaimPrefix+"db") }},
		{name: "read live value", live: true, fault: func(b *faultBackend) { arm(&b.failGet, credentialPrefix+"db") }},
		{name: "read live version", live: true, fault: func(b *faultBackend) { arm(&b.failGet, versionKey("db", 1)) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			legacy := seal.NewMemoryBackend()
			shared := &faultBackend{MemoryBackend: seal.NewMemoryBackend()}
			writeLegacy(t, legacy, "db", "v2", "v1", testPolicy)
			if tc.live {
				live := newUnboundService(t)
				require.NoError(t, live.BindStore(ctx, shared.MemoryBackend))
				seedGraceVersion(t, live, "db")
			}
			tc.fault(shared)

			svc := newMigratingService(t, legacy)
			require.NoError(t, svc.BindStore(ctx, shared), "a failed migration leaves the store bound")
			require.ErrorIs(t, svc.MigrateLegacy(ctx), errInjectedFault)
			assert.Len(t, legacyKeys(t, legacy), 3, "a failed migration keeps the legacy copy")

			disarmAll(shared)
			require.NoError(t, svc.MigrateLegacy(ctx))
			conflicts, err := svc.ListMigrationConflicts(ctx)
			require.NoError(t, err)
			if tc.live {
				require.Len(t, conflicts, 1, "the copy differs from the live record")
				assert.Equal(t, "v2", retrieveString(t, svc, "db"), "the live record is kept")
				assert.Len(t, legacyKeys(t, legacy), 3)
				return
			}
			assert.Empty(t, conflicts)
			assert.Equal(t, "v2", retrieveString(t, svc, "db"))
			assert.Equal(t, "v1", versionString(t, svc, "db", 1))
			assert.Empty(t, legacyKeys(t, legacy))
		})
	}
}

// A migration that cannot read or clean up the legacy store reports the
// storage cause and finishes on the next run, with the copy migrated once.
func TestMigrateLegacy_LegacyStoreFaultsAreRetried(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		noteLeft bool
		fault    func(*faultBackend)
	}{
		{name: "list notes", fault: func(b *faultBackend) { arm(&b.failList, legacyMigrationNotePrefix) }},
		{name: "read a version record", fault: func(b *faultBackend) { arm(&b.failGet, legacyCredentialPrefix+"db"+versionPrefix+"1") }},
		{name: "delete a version record", fault: func(b *faultBackend) { arm(&b.failDelete, legacyCredentialPrefix+"db"+versionPrefix+"1") }},
		{name: "delete the note", noteLeft: true, fault: func(b *faultBackend) { arm(&b.failDelete, legacyMigrationNotePrefix+"db") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			legacy := &faultBackend{MemoryBackend: seal.NewMemoryBackend()}
			writeLegacy(t, legacy.MemoryBackend, "db", "v2", "v1", testPolicy)
			tc.fault(legacy)

			svc := newMigratingService(t, legacy)
			require.NoError(t, svc.BindStore(ctx, seal.NewMemoryBackend()))
			require.ErrorIs(t, svc.MigrateLegacy(ctx), errInjectedFault)
			if tc.noteLeft {
				assert.Equal(t, []string{legacyMigrationNotePrefix + "db"}, legacyKeys(t, legacy.MemoryBackend),
					"only the note that marks the copy settled is left")
			} else {
				value, err := legacy.MemoryBackend.Get(ctx, legacyCredentialPrefix+"db")
				require.NoError(t, err, "the legacy value is the last record deleted")
				assert.Equal(t, "v2", string(value))
			}

			disarmAll(legacy)
			require.NoError(t, svc.MigrateLegacy(ctx))
			assert.Empty(t, legacyKeys(t, legacy))
			assert.Equal(t, "v2", retrieveString(t, svc, "db"))
			assert.Equal(t, "v1", versionString(t, svc, "db", 1))
			meta, err := svc.loadBarrierMeta(ctx, "db")
			require.NoError(t, err)
			assert.Equal(t, uint64(2), meta.CurrentVersion, "the copy is migrated once")
		})
	}
}

// Another node claims the name between this node's read of the claim and
// its own claim. An identical copy is then settled by the other node's
// claim; a claim that cannot be read is refused, and the copy kept.
func TestMigrateLegacy_ClaimLostToAnotherWriter(t *testing.T) {
	ctx := context.Background()
	claimKey := MigrationClaimPrefix + "db"

	t.Run("identical copy", func(t *testing.T) {
		legacyA, legacyB := seal.NewMemoryBackend(), seal.NewMemoryBackend()
		writeLegacy(t, legacyA, "db", "v2", "v1", testPolicy)
		writeLegacy(t, legacyB, "db", "v2", "v1", testPolicy)
		shared := &faultBackend{MemoryBackend: seal.NewMemoryBackend()}
		fired := false
		shared.onGet = func(_ context.Context, key string) {
			if fired || key != claimKey {
				return
			}
			fired = true
			require.NoError(t, newMigratingService(t, legacyA).BindStore(ctx, shared.MemoryBackend))
		}

		svc := newMigratingService(t, legacyB)
		require.NoError(t, svc.BindStore(ctx, shared))
		require.True(t, fired)
		conflicts, err := svc.ListMigrationConflicts(ctx)
		require.NoError(t, err)
		assert.Empty(t, conflicts)
		assert.Empty(t, legacyKeys(t, legacyB), "the other node's claim settles this copy")
		assert.Equal(t, "v2", retrieveString(t, svc, "db"))
	})

	for _, tc := range []struct {
		name  string
		claim []byte
	}{
		{name: "unparseable claim", claim: []byte("not json")},
		{name: "empty claim", claim: []byte{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			legacy := seal.NewMemoryBackend()
			writeLegacy(t, legacy, "db", "v2", "v1", nil)
			shared := &faultBackend{MemoryBackend: seal.NewMemoryBackend()}
			fired := false
			shared.onGet = func(_ context.Context, key string) {
				if fired || key != claimKey {
					return
				}
				fired = true
				require.NoError(t, shared.MemoryBackend.Put(ctx, claimKey, tc.claim))
			}

			svc := newMigratingService(t, legacy)
			require.NoError(t, svc.BindStore(ctx, shared))
			require.True(t, fired)
			assert.ErrorIs(t, svc.MigrateLegacy(ctx), ErrStoreUnavailable)
			assert.Len(t, legacyKeys(t, legacy), 3, "the copy is kept")
			_, err := shared.MemoryBackend.Get(ctx, credentialPrefix+"db")
			assert.ErrorIs(t, err, seal.ErrSecretNotFound, "nothing is installed under a claim not read")
		})
	}
}

// A live write lands between this node's claim and its install, so the
// install is refused; settling the claim to the live record then fails or
// loses. The claim stays pending, the live value and the legacy copy are
// kept, and the next run reports the conflict.
func TestMigrateLegacy_SettleClaimFaults(t *testing.T) {
	ctx := context.Background()
	claimKey := MigrationClaimPrefix + "db"
	for _, tc := range []struct {
		name  string
		fault func(*faultBackend)
	}{
		{name: "settle fails", fault: func(b *faultBackend) { arm(&b.failCAS, claimKey) }},
		{name: "settle lost", fault: func(b *faultBackend) { b.casLosses.Store(1); arm(&b.loseCAS, claimKey) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			legacy := seal.NewMemoryBackend()
			writeLegacy(t, legacy, "db", "v2", "v1", testPolicy)
			shared := &faultBackend{MemoryBackend: seal.NewMemoryBackend()}
			live := newUnboundService(t)
			require.NoError(t, live.BindStore(ctx, shared.MemoryBackend))
			wrote, armed := false, false
			shared.onCAS = func(ctx context.Context, key string) {
				if wrote || key != credentialPrefix+"db" {
					return
				}
				wrote = true
				require.NoError(t, live.StoreCredential(ctx, "db", []byte("fresh")))
			}
			shared.onGet = func(_ context.Context, key string) {
				if !wrote || armed || key != credentialPrefix+"db" {
					return
				}
				armed = true
				tc.fault(shared)
			}

			svc := newMigratingService(t, legacy)
			require.NoError(t, svc.BindStore(ctx, shared))
			require.True(t, armed)
			claim, ok, err := svc.loadClaim(ctx, claimKey)
			require.NoError(t, err)
			require.True(t, ok)
			assert.False(t, claim.Complete, "the claim stays pending")
			assert.Len(t, legacyKeys(t, legacy), 3)

			disarmAll(shared)
			require.NoError(t, svc.MigrateLegacy(ctx))
			conflicts, err := svc.ListMigrationConflicts(ctx)
			require.NoError(t, err)
			require.Len(t, conflicts, 1)
			assert.True(t, conflicts[0].Value)
			assert.Equal(t, "fresh", retrieveString(t, svc, "db"), "the live value is kept")
			assert.Len(t, legacyKeys(t, legacy), 3)
		})
	}
}
