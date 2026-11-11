package credentials

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

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
		name string
		// afterWrite runs once the live write has landed; onRead once the
		// refused install reads the live record back.
		afterWrite, onRead func(*faultBackend)
		settled            bool
	}{
		{name: "settle fails", onRead: func(b *faultBackend) { arm(&b.failCAS, claimKey) }},
		{name: "settle lost", onRead: func(b *faultBackend) { b.casLosses.Store(1); arm(&b.loseCAS, claimKey) }},
		{name: "live record unreadable", afterWrite: func(b *faultBackend) { arm(&b.failList, credentialPrefix+"db") }},
		{name: "settled claim unreadable", onRead: func(b *faultBackend) { arm(&b.failGet, claimKey) }, settled: true},
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
				if tc.afterWrite != nil {
					armed = true
					tc.afterWrite(shared)
				}
			}
			shared.onGet = func(_ context.Context, key string) {
				if !wrote || armed || key != credentialPrefix+"db" {
					return
				}
				armed = true
				tc.onRead(shared)
			}

			svc := newMigratingService(t, legacy)
			require.NoError(t, svc.BindStore(ctx, shared))
			require.True(t, armed)
			disarmAll(shared)
			claim, ok, err := svc.loadClaim(ctx, claimKey)
			require.NoError(t, err)
			require.True(t, ok)
			assert.Equal(t, tc.settled, claim.Complete, "a claim is settled only by its swap")
			assert.Len(t, legacyKeys(t, legacy), 3)

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

// A claim settled to something this node cannot parse, by a writer racing
// its settle, is refused: the copy and the live value are kept.
func TestMigrateLegacy_SettledClaimUnparseable(t *testing.T) {
	ctx := context.Background()
	claimKey := MigrationClaimPrefix + "db"
	legacy := seal.NewMemoryBackend()
	writeLegacy(t, legacy, "db", "v2", "v1", nil)
	shared := &faultBackend{MemoryBackend: seal.NewMemoryBackend()}
	live := newUnboundService(t)
	require.NoError(t, live.BindStore(ctx, shared.MemoryBackend))
	wrote, garbled := false, false
	shared.onCAS = func(ctx context.Context, key string) {
		if !wrote && key == credentialPrefix+"db" {
			wrote = true
			require.NoError(t, live.StoreCredential(ctx, "db", []byte("fresh")))
		}
	}
	shared.onGet = func(_ context.Context, key string) {
		if !wrote || garbled || key != credentialPrefix+"db" {
			return
		}
		garbled = true
		require.NoError(t, shared.MemoryBackend.Put(ctx, claimKey, []byte("garbled")))
	}

	svc := newMigratingService(t, legacy)
	require.NoError(t, svc.BindStore(ctx, shared))
	require.True(t, garbled)
	disarmAll(shared)
	assert.ErrorIs(t, svc.MigrateLegacy(ctx), ErrStoreUnavailable)
	assert.Equal(t, "fresh", retrieveRaw(t, shared.MemoryBackend, credentialPrefix+"db"))
	assert.Len(t, legacyKeys(t, legacy), 3)
}

func retrieveRaw(t *testing.T, st Store, key string) string {
	t.Helper()
	value, err := st.Get(context.Background(), key)
	require.NoError(t, err)
	return string(value)
}

// A derived service whose migration fails is not returned, and the barrier
// is not recorded as migrated: the next derivation migrates it.
func TestForTenant_FailedMigrationIsRetried(t *testing.T) {
	ctx := context.Background()
	legacy := newLegacyBarrier(t)
	writeLegacy(t, legacy, "db", "v2", "v1", nil)
	home := &faultBackend{MemoryBackend: seal.NewMemoryBackend()}
	parent := newUnboundServiceWith(t)
	arm(&home.failList, credentialPrefix+"db")

	derived, err := parent.ForTenant(ctx, home, legacy)
	require.ErrorIs(t, err, errInjectedFault)
	assert.Nil(t, derived)
	disarmAll(home)

	derived, err = parent.ForTenant(ctx, home, legacy)
	require.NoError(t, err)
	assert.Equal(t, "v2", retrieveString(t, derived, "db"))
}

// A barrier migrated without conflict is not migrated again: a later
// derivation does not read the legacy store.
func TestForTenant_MigratedBarrierIsNotReread(t *testing.T) {
	ctx := context.Background()
	legacy := newLegacyBarrier(t)
	writeLegacy(t, legacy, "db", "v2", "v1", nil)
	home := &faultBackend{MemoryBackend: seal.NewMemoryBackend()}
	parent := newUnboundServiceWith(t)
	_, err := parent.ForTenant(ctx, home, legacy)
	require.NoError(t, err)

	arm(&home.failList, credentialPrefix+"db")
	derived, err := parent.ForTenant(ctx, home, legacy)
	require.NoError(t, err, "a migrated barrier is not migrated again")
	disarmAll(home)
	assert.Equal(t, "v2", retrieveString(t, derived, "db"))
}

// A service built without a logger cannot derive one.
func TestForTenant_ParentWithoutLogger(t *testing.T) {
	derived, err := (&CredentialService{}).ForTenant(context.Background(), newHomeStore(t), newLegacyBarrier(t))
	assert.ErrorIs(t, err, ErrNilLogger)
	assert.Nil(t, derived)
}

// A legacy key is a record of a credential only when its suffix is a
// version number or a usable metadata record of a held name that is not
// itself a record. Every other key is a credential of its own and migrates
// under its full name, beside a live credential whose name extends one.
func TestMigrateLegacy_OddRecordNamesMigrateAsCredentials(t *testing.T) {
	ctx := context.Background()
	legacy := seal.NewMemoryBackend()
	writeLegacy(t, legacy, "db", "v2", "v1", nil)
	own := map[string]string{
		"db#vx":       "not a version",      // suffix is not a number
		"orphan#meta": "no orphan value",    // no value for the base name
		"db#v1#meta":  "record of a record", // base is a version record
		"e":           "plain",              // its metadata is empty
		"e#meta":      "{}",
	}
	for name, value := range own {
		require.NoError(t, legacy.Put(ctx, legacyCredentialPrefix+name, []byte(value)))
	}
	shared := seal.NewMemoryBackend()
	live := newUnboundService(t)
	require.NoError(t, live.BindStore(ctx, shared))
	require.NoError(t, live.StoreCredential(ctx, "db2", []byte("live")))

	svc := newMigratingService(t, legacy)
	require.NoError(t, svc.BindStore(ctx, shared))
	conflicts, err := svc.ListMigrationConflicts(ctx)
	require.NoError(t, err)
	assert.Empty(t, conflicts)
	assert.Empty(t, legacyKeys(t, legacy), "every copy is migrated")
	assert.Equal(t, "v2", retrieveString(t, svc, "db"))
	assert.Equal(t, "v1", versionString(t, svc, "db", 1))
	assert.Equal(t, "live", retrieveString(t, svc, "db2"))
	for name, value := range own {
		assert.Equal(t, value, retrieveString(t, svc, name), name)
	}
}

// twoConflictNode returns a node whose copies of "db" and "db2" both
// conflict with the replicated values another node migrated, with its
// legacy and replicated stores open to faults.
func twoConflictNode(t *testing.T) (svc *CredentialService, legacy, shared *faultBackend) {
	t.Helper()
	ctx := context.Background()
	legacyA := seal.NewMemoryBackend()
	legacy = &faultBackend{MemoryBackend: seal.NewMemoryBackend()}
	shared = &faultBackend{MemoryBackend: seal.NewMemoryBackend()}
	for _, name := range []string{"db", "db2"} {
		writeLegacy(t, legacyA, name, "v2", "v1", nil)
		writeLegacy(t, legacy.MemoryBackend, name, "kept", "kept-old", nil)
	}
	require.NoError(t, newMigratingService(t, legacyA).BindStore(ctx, shared))
	svc = newMigratingService(t, legacy)
	require.NoError(t, svc.BindStore(ctx, shared))
	requireConflicts(t, svc, "db", "db2")
	return svc, legacy, shared
}

func requireConflicts(t *testing.T, svc *CredentialService, names ...string) {
	t.Helper()
	conflicts, err := svc.ListMigrationConflicts(context.Background())
	require.NoError(t, err)
	got := make([]string, 0, len(conflicts))
	for _, c := range conflicts {
		got = append(got, c.Name)
	}
	require.Equal(t, names, got)
}

// Resolving one conflict touches only that name's copy: the copy and the
// migration note of a name it prefixes are kept, and so is its conflict.
func TestResolveMigrationConflict_LeavesOtherNames(t *testing.T) {
	ctx := context.Background()
	svc, legacy, _ := twoConflictNode(t)
	note := legacyMigrationNotePrefix + "db2"
	require.NoError(t, legacy.MemoryBackend.Put(ctx, note, []byte("{}")))

	require.NoError(t, svc.ResolveMigrationConflict(ctx, "db", MigrationDiscard))
	requireConflicts(t, svc, "db2")
	assert.Equal(t, []string{
		note,
		legacyCredentialPrefix + "db2",
		legacyCredentialPrefix + "db2" + metaSuffix,
		legacyCredentialPrefix + "db2" + versionPrefix + "1",
	}, legacyKeys(t, legacy.MemoryBackend))
	assert.Equal(t, "v2", retrieveString(t, svc, "db"))
}

// A resolve whose copy has gone since the migration reports the conflict
// as not found and drops it.
func TestResolveMigrationConflict_CopyGone(t *testing.T) {
	ctx := context.Background()
	svc, legacy, _ := twoConflictNode(t)
	for _, suffix := range []string{"", metaSuffix, versionPrefix + "1"} {
		require.NoError(t, legacy.MemoryBackend.Delete(ctx, legacyCredentialPrefix+"db"+suffix))
	}

	err := svc.ResolveMigrationConflict(ctx, "db", MigrationImport)
	require.ErrorIs(t, err, ErrMigrationConflictNotFound)
	requireConflicts(t, svc, "db2")
	assert.Equal(t, "v2", retrieveString(t, svc, "db"))
}

// A resolve that cannot read the copy, or cannot read the replicated
// metadata it imports over, fails with the cause and keeps the conflict,
// the copy and the replicated value; once the fault clears it resolves.
func TestResolveMigrationConflict_ReadFaults(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fault func(legacy, shared *faultBackend)
	}{
		{name: "copy unreadable", fault: func(l, _ *faultBackend) { arm(&l.failList, legacyCredentialPrefix+"db") }},
		{name: "replicated metadata unreadable", fault: func(_, s *faultBackend) { arm(&s.failGet, metaKey("db")) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			svc, legacy, shared := twoConflictNode(t)
			tc.fault(legacy, shared)

			err := svc.ResolveMigrationConflict(ctx, "db", MigrationImport)
			require.ErrorIs(t, err, errInjectedFault)
			disarmAll(legacy)
			disarmAll(shared)
			requireConflicts(t, svc, "db", "db2")
			assert.Len(t, legacyKeys(t, legacy.MemoryBackend), 6)
			assert.Equal(t, "v2", retrieveString(t, svc, "db"))

			require.NoError(t, svc.ResolveMigrationConflict(ctx, "db", MigrationImport))
			requireConflicts(t, svc, "db2")
			assert.Equal(t, "kept", retrieveString(t, svc, "db"))
		})
	}
}

// While the legacy copy cannot be migrated, every verb is refused with the
// migration's failure, so none reads or writes a store that may still lack
// the legacy records; once the legacy store answers, each verb works.
func TestMigrateLegacy_FailingMigrationRefusesEveryVerb(t *testing.T) {
	ctx := context.Background()
	legacy := &faultBackend{MemoryBackend: seal.NewMemoryBackend()}
	writeLegacy(t, legacy.MemoryBackend, "db", "v2", "v1", nil)
	arm(&legacy.failList, legacyCredentialPrefix)
	svc := newMigratingService(t, legacy)
	require.NoError(t, svc.BindStore(ctx, seal.NewMemoryBackend()))

	verbs := map[string]func() error{
		"list": func() error { _, err := svc.ListCredentials(ctx); return err },
		"rotate": func() error {
			_, err := svc.RotateCredential(ctx, "db", RotateOptions{NewValue: []byte("v3")})
			return err
		},
		"version":      func() error { _, err := svc.RetrieveCredentialVersionBuffer(ctx, "db", 1); return err },
		"versions":     func() error { _, err := svc.ListCredentialVersions(ctx, "db"); return err },
		"gc":           func() error { _, err := svc.GCExpiredVersions(ctx, "db"); return err },
		"store":        func() error { return svc.StoreCredential(ctx, "db", []byte("v3")) },
		"delete":       func() error { return svc.DeleteCredential(ctx, "db") },
		"set policy":   func() error { return svc.SetRotationPolicy(ctx, "db", *testPolicy) },
		"policy":       func() error { _, err := svc.RotationPolicy(ctx, "db"); return err },
		"nextRotation": func() error { _, err := svc.NextRotation(ctx, "db"); return err },
	}
	for name, verb := range verbs {
		err := verb()
		requireOperationError(t, err, "migrate")
		assert.ErrorIs(t, err, errInjectedFault, name)
	}
	// The conflict verbs are the migration's own surface and report its
	// failure as it is.
	conflicts, err := svc.ListMigrationConflicts(ctx)
	assert.ErrorIs(t, err, errInjectedFault)
	assert.Nil(t, conflicts)
	assert.ErrorIs(t, svc.ResolveMigrationConflict(ctx, "db", MigrationImport), errInjectedFault)
	assert.Len(t, legacyKeys(t, legacy.MemoryBackend), 3, "the legacy copy is kept")

	disarmAll(legacy)
	names, err := svc.ListCredentials(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"db"}, names)
	assert.Equal(t, "v1", versionString(t, svc, "db", 1))
	require.NoError(t, svc.SetRotationPolicy(ctx, "db", *testPolicy))
	got, err := svc.RotationPolicy(ctx, "db")
	require.NoError(t, err)
	assert.Equal(t, *testPolicy, got)
}

// A migration retried under a caller's context that has ended reports the
// context's error beside the storage failure it met.
func TestEnsureMigrated_EndedContextJoinedToFailure(t *testing.T) {
	legacy := &faultBackend{MemoryBackend: seal.NewMemoryBackend()}
	writeLegacy(t, legacy.MemoryBackend, "db", "v2", "v1", nil)
	arm(&legacy.failList, legacyCredentialPrefix)
	svc := newMigratingService(t, legacy)
	require.NoError(t, svc.BindStore(context.Background(), seal.NewMemoryBackend()))
	svc.rotationAttemptTimeout = time.Minute

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := svc.ListCredentials(ctx)
	requireOperationError(t, err, "migrate")
	assert.ErrorIs(t, err, context.Canceled)
	assert.ErrorIs(t, err, errInjectedFault)
}
