package credentials

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// BindStoreFrom moves the replicated layout of the earlier store into the
// new one, on a service whose node-local migration already ran; a move
// the source refuses at bind time finishes before the next operation, and
// a record the new store already holds is kept.
func TestBindStoreFrom_MovesTheEarlierStoreLayout(t *testing.T) {
	ctx := context.Background()
	first, second := seal.NewMemoryBackend(), seal.NewMemoryBackend()
	svc := newUnboundService(t)
	require.NoError(t, svc.BindStore(ctx, first))
	require.NoError(t, svc.StoreCredential(ctx, "db", []byte("v1")))
	_, err := svc.RotateCredential(ctx, "db", RotateOptions{NewValue: []byte("v2"), GracePeriod: time.Hour})
	require.NoError(t, err)
	policy := RotationPolicy{Enabled: true, Interval: time.Hour, RetainVersions: 2}
	require.NoError(t, svc.SetRotationPolicy(ctx, "db", policy))
	require.NoError(t, svc.BindStore(ctx, first))
	require.NoError(t, second.Put(ctx, credentialPrefix+"kept", []byte("cluster")))
	require.NoError(t, first.Put(ctx, credentialPrefix+"kept", []byte("node")))

	from := &faultyStore{Store: first, op: "list", key: credentialPrefix}
	require.NoError(t, svc.BindStoreFrom(ctx, second, from), "a refused move does not fail the bind")
	assert.True(t, from.fired)

	assert.Equal(t, "v2", retrieveString(t, svc, "db"))
	assert.Equal(t, "v1", versionString(t, svc, "db", 1))
	got, err := svc.RotationPolicy(ctx, "db")
	require.NoError(t, err)
	assert.Equal(t, policy, got)
	kept, err := second.Get(ctx, credentialPrefix+"kept")
	require.NoError(t, err)
	assert.Equal(t, "cluster", string(kept), "the new store's record is kept")
	for _, prefix := range layoutPrefixes {
		keys, err := first.List(ctx, prefix)
		require.NoError(t, err)
		if prefix == credentialPrefix {
			assert.Equal(t, []string{credentialPrefix + "kept"}, keys,
				"a conflicting name stays in the earlier store")
			continue
		}
		assert.Empty(t, keys, "moved records leave the earlier store")
	}
}

// A plain BindStore after BindStoreFrom drops the pending move: it was
// into the store the new binding replaces.
func TestBindStore_DropsAPendingMove(t *testing.T) {
	ctx := context.Background()
	first, second := seal.NewMemoryBackend(), seal.NewMemoryBackend()
	require.NoError(t, first.Put(ctx, credentialPrefix+"db", []byte("v")))
	svc := newUnboundService(t)
	from := &faultyStore{Store: first, op: "list", key: credentialPrefix}
	require.NoError(t, svc.BindStoreFrom(ctx, second, from))
	require.NoError(t, svc.BindStore(ctx, first))
	require.NoError(t, svc.ensureLayoutMoved(ctx))
	value, err := first.Get(ctx, credentialPrefix+"db")
	require.NoError(t, err)
	assert.Equal(t, "v", string(value))
}

func TestBindStoreFrom_NilStoresRefused(t *testing.T) {
	svc := newUnboundService(t)
	var nilBarrier *seal.StorageBarrier
	assert.ErrorIs(t, svc.BindStoreFrom(context.Background(), seal.NewMemoryBackend(), nilBarrier), ErrNilBarrier)
	assert.ErrorIs(t, svc.BindStoreFrom(context.Background(), nil, seal.NewMemoryBackend()), ErrNilStore)
}

// refusingStore reports every guarded batch as not applied, as a store does
// when a guarded key appeared between the check and the write.
type refusingStore struct {
	Store
}

func (r *refusingStore) PutAllIfAllUnchanged(context.Context, []kvstore.Guard, []kvstore.KV) (bool, error) {
	return false, nil
}

// putLayout writes one credential's full replicated-layout record set.
func putLayout(t *testing.T, st Store, name, value string) []string {
	t.Helper()
	keys := []string{
		credentialPrefix + name,
		MetaKeyPrefix + name,
		versionKey(name, 1),
		stagedKey(name, 2),
		MigrationClaimPrefix + name,
	}
	for _, key := range keys {
		require.NoError(t, st.Put(context.Background(), key, []byte(value+"@"+key)))
	}
	return keys
}

func requireValue(t *testing.T, st Store, key, want string) {
	t.Helper()
	got, err := st.Get(context.Background(), key)
	require.NoError(t, err, key)
	assert.Equal(t, want, string(got), key)
}

func requireAbsent(t *testing.T, st Store, key string) {
	t.Helper()
	keys, err := st.List(context.Background(), key)
	require.NoError(t, err)
	assert.NotContains(t, keys, key)
}

func TestMoveLayout_PartialDestinationConflictKeepsBothCopies(t *testing.T) {
	ctx := context.Background()
	from, to := seal.NewMemoryBackend(), seal.NewMemoryBackend()
	dbKeys := putLayout(t, from, "db", "old")
	otherKeys := putLayout(t, from, "other", "old")
	require.NoError(t, to.Put(ctx, MetaKeyPrefix+"db", []byte("new-meta")))

	err := moveLayout(ctx, from, to)
	var conflict *LayoutConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, []string{"db"}, conflict.Names)
	assert.Contains(t, err.Error(), "db")
	assert.NotContains(t, err.Error(), "old@", "the message never carries values")

	for _, key := range dbKeys {
		requireValue(t, from, key, "old@"+key)
	}
	requireValue(t, to, MetaKeyPrefix+"db", "new-meta")
	for _, key := range dbKeys {
		if key != MetaKeyPrefix+"db" {
			requireAbsent(t, to, key)
		}
	}
	for _, key := range otherKeys {
		requireValue(t, to, key, "old@"+key)
		requireAbsent(t, from, key)
	}
}

func TestMoveLayout_VersionOnlyDestinationConflicts(t *testing.T) {
	ctx := context.Background()
	from, to := seal.NewMemoryBackend(), seal.NewMemoryBackend()
	dbKeys := putLayout(t, from, "db", "old")
	require.NoError(t, to.Put(ctx, versionKey("db", 7), []byte("new-v7")))

	err := moveLayout(ctx, from, to)
	var conflict *LayoutConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, []string{"db"}, conflict.Names)
	for _, key := range dbKeys {
		requireValue(t, from, key, "old@"+key)
	}
	requireAbsent(t, to, credentialPrefix+"db")
}

func TestMoveLayout_MovesWholeSets(t *testing.T) {
	ctx := context.Background()
	from, to := seal.NewMemoryBackend(), seal.NewMemoryBackend()
	keys := append(putLayout(t, from, "db", "v"), putLayout(t, from, "db2", "w")...)
	require.NoError(t, to.Put(ctx, credentialPrefix+"unrelated", []byte("x")))

	require.NoError(t, moveLayout(ctx, from, to))

	for _, key := range keys {
		want := "v@" + key
		if strings.Contains(key, "db2") || strings.Contains(key, encodedName("db2")) {
			want = "w@" + key
		}
		requireValue(t, to, key, want)
	}
	for _, prefix := range layoutPrefixes {
		left, err := from.List(ctx, prefix)
		require.NoError(t, err)
		assert.Empty(t, left, "a moved set leaves the earlier store")
	}
	requireValue(t, to, credentialPrefix+"unrelated", "x")
}

// TestMoveLayout_MovesMigrationImportRecords verifies an import record moves
// with its credential, and one a deleted credential left moves on its own.
func TestMoveLayout_MovesMigrationImportRecords(t *testing.T) {
	ctx := context.Background()
	from, to := seal.NewMemoryBackend(), seal.NewMemoryBackend()
	keys := append(putLayout(t, from, "db", "v"), importKey("db", "d1"))
	require.NoError(t, from.Put(ctx, importKey("db", "d1"), []byte("v@"+importKey("db", "d1"))))
	orphan := importKey("gone", "d2")
	require.NoError(t, from.Put(ctx, orphan, []byte("v@"+orphan)))
	keys = append(keys, orphan)

	require.NoError(t, moveLayout(ctx, from, to))

	for _, key := range keys {
		requireValue(t, to, key, "v@"+key)
		requireAbsent(t, from, key)
	}
	left, err := from.List(ctx, MigrationImportPrefix)
	require.NoError(t, err)
	assert.Empty(t, left, "no import record stays in the earlier store")
}

// TestBindStoreFrom_ImportRetriedAfterMoveImportsNothing verifies a conflict
// import whose cleanup failed, retried after the store moved, finds its
// import record in the new store and imports nothing over a newer value.
func TestBindStoreFrom_ImportRetriedAfterMoveImportsNothing(t *testing.T) {
	ctx := context.Background()
	legacyA, shared, moved := seal.NewMemoryBackend(), seal.NewMemoryBackend(), seal.NewMemoryBackend()
	legacyB := &faultyStore{Store: seal.NewMemoryBackend()}
	writeLegacy(t, legacyA, "db", "v2", "v1", nil)
	writeLegacy(t, legacyB, "db", "kept", "kept-old", nil)
	require.NoError(t, newMigratingService(t, legacyA).BindStore(ctx, shared))
	nodeB := newMigratingService(t, legacyB)
	require.NoError(t, nodeB.BindStore(ctx, shared))

	// The import commits, then the note that opens the cleanup fails.
	legacyB.mu.Lock()
	legacyB.op, legacyB.key, legacyB.fired = "put", legacyMigrationNotePrefix+"db", false
	legacyB.mu.Unlock()
	require.ErrorIs(t, nodeB.ResolveMigrationConflict(ctx, "db", MigrationImport), errInjected)
	_, err := nodeB.rotateBarrier(ctx, "db", RotateOptions{NewValue: []byte("newer")})
	require.NoError(t, err)

	require.NoError(t, nodeB.BindStoreFrom(ctx, moved, shared))
	imports, err := shared.List(ctx, MigrationImportPrefix)
	require.NoError(t, err)
	assert.Empty(t, imports, "the import record leaves the earlier store")

	require.NoError(t, nodeB.ResolveMigrationConflict(ctx, "db", MigrationImport))
	assert.Equal(t, "newer", retrieveString(t, nodeB, "db"), "a retried import must not import the kept copy again")
	assert.Empty(t, legacyKeys(t, legacyB), "the kept copy is deleted")
	conflicts, err := nodeB.ListMigrationConflicts(ctx)
	require.NoError(t, err)
	assert.Empty(t, conflicts)
}

func TestMoveLayout_RefusedGuardMovesNothing(t *testing.T) {
	ctx := context.Background()
	from := &captureStore{Store: seal.NewMemoryBackend()}
	to := seal.NewMemoryBackend()
	keys := putLayout(t, from, "db", "old")

	err := moveLayout(ctx, from, &refusingStore{Store: to})
	var conflict *LayoutConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, []string{"db"}, conflict.Names)
	for _, key := range keys {
		requireAbsent(t, to, key)
	}
	for _, key := range keys {
		requireWiped(t, from.gets, key)
	}
	from.reset()
	for _, key := range keys {
		requireValue(t, from, key, "old@"+key)
	}
}

func TestBindStoreFrom_WipesTheMovedValues(t *testing.T) {
	ctx := context.Background()
	first, second := seal.NewMemoryBackend(), seal.NewMemoryBackend()
	svc := newUnboundService(t)
	require.NoError(t, svc.BindStore(ctx, first))
	require.NoError(t, svc.StoreCredential(ctx, "db", []byte("v1")))
	_, err := svc.RotateCredential(ctx, "db", RotateOptions{NewValue: []byte("v2"), GracePeriod: time.Hour})
	require.NoError(t, err)
	require.NoError(t, svc.BindStore(ctx, first))

	from := &captureStore{Store: first}
	require.NoError(t, svc.BindStoreFrom(ctx, second, from))

	require.NotEmpty(t, from.gets, "the move read the earlier store")
	for key := range from.gets {
		requireWiped(t, from.gets, key)
	}
	assert.Equal(t, "v2", retrieveString(t, svc, "db"), "the moved value is intact")
	assert.Equal(t, "v1", versionString(t, svc, "db", 1))
}

// requireMovedAndClean checks a finished move of keys: to holds each value,
// from holds none of them and no move marker is left.
func requireMovedAndClean(t *testing.T, from, to Store, name string, keys []string) {
	t.Helper()
	for _, key := range keys {
		requireValue(t, to, key, "old@"+key)
		requireAbsent(t, from, key)
	}
	assert.Empty(t, transientMoveRecords(t, to), "the move marker of %s is gone", name)
	settled, err := holdsKey(context.Background(), to, layoutSettled(name))
	require.NoError(t, err)
	assert.True(t, settled, "the settlement record of %s stays", name)
}

// transientMoveRecords lists st's move records other than the durable
// settlement records: the markers, fences and conflict records.
func transientMoveRecords(t *testing.T, st Store) []string {
	t.Helper()
	keys, err := st.List(context.Background(), LayoutMovedPrefix)
	require.NoError(t, err)
	settledRoot := LayoutMovedPrefix + layoutSettledSource + "/"
	return slices.DeleteFunc(keys, func(key string) bool { return strings.HasPrefix(key, settledRoot) })
}

// markerOf is the move-marker key of name for the source store from.
func markerOf(t *testing.T, from Store, name string) string {
	t.Helper()
	src, err := layoutSourceID(context.Background(), from)
	require.NoError(t, err)
	return LayoutMovedPrefix + src + "/" + name
}

// requireMarked checks st holds the marker key with a move id as value.
func requireMarked(t *testing.T, st Store, marker string) {
	t.Helper()
	held, err := holdsKey(context.Background(), st, marker)
	require.NoError(t, err)
	require.True(t, held, marker)
	id, err := st.Get(context.Background(), marker)
	require.NoError(t, err)
	assert.NotEmpty(t, id, "a marker carries its move id")
}

func TestLayoutSourceID_IsStable(t *testing.T) {
	ctx := context.Background()
	a, b := seal.NewMemoryBackend(), seal.NewMemoryBackend()
	first, err := layoutSourceID(ctx, a)
	require.NoError(t, err)
	require.NotEmpty(t, first)
	again, err := layoutSourceID(ctx, a)
	require.NoError(t, err)
	assert.Equal(t, first, again, "a store keeps its identity")
	other, err := layoutSourceID(ctx, b)
	require.NoError(t, err)
	assert.NotEqual(t, first, other, "two stores have distinct identities")
}

// Two source stores holding the same name: the second store's pass does
// not take the first store's marker as its own, so it keeps its copy and
// reports the conflict, and neither pass sweeps the other's markers.
func TestMoveLayout_TwoSourcesSameNameKeepBothCopies(t *testing.T) {
	ctx := context.Background()
	memA, memB, to := seal.NewMemoryBackend(), seal.NewMemoryBackend(), seal.NewMemoryBackend()
	keysA := putLayout(t, memA, "db", "old")
	keysB := putLayout(t, memB, "db", "other")
	fromA := &faultyStore{Store: memA, op: "delete", key: credentialPrefix + "db"}
	require.ErrorIs(t, moveLayout(ctx, fromA, to), errInjected)
	markerA := markerOf(t, memA, "db")
	foreign := LayoutMovedPrefix + "feedface/gone"
	require.NoError(t, to.Put(ctx, foreign, []byte("m")))

	err := moveLayout(ctx, memB, to)
	var conflict *LayoutConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, []string{"db"}, conflict.Names)
	for _, k := range keysB {
		requireValue(t, memB, k, "other@"+k)
	}
	for _, k := range keysA {
		requireValue(t, to, k, "old@"+k)
	}
	requireMarked(t, to, markerA)
	requireMarked(t, to, foreign)

	require.NoError(t, moveLayout(ctx, fromA, to))
	for _, k := range keysA {
		requireAbsent(t, memA, k)
		requireValue(t, to, k, "old@"+k)
	}
	requireAbsent(t, to, markerA)
	requireMarked(t, to, foreign)
	for _, k := range keysB {
		requireValue(t, memB, k, "other@"+k)
	}
}

// A pass that stops after the destination commit, before any source record
// is deleted, is finished by the next pass, not reported as a conflict.
func TestMoveLayout_CrashAfterCommitIsFinished(t *testing.T) {
	ctx := context.Background()
	mem, to := seal.NewMemoryBackend(), seal.NewMemoryBackend()
	keys := putLayout(t, mem, "db", "old")
	from := &faultyStore{Store: mem, op: "delete", key: credentialPrefix + "db"}

	require.ErrorIs(t, moveLayout(ctx, from, to), errInjected)
	require.True(t, from.fired)
	requireMarked(t, to, markerOf(t, mem, "db"))
	for _, key := range keys {
		requireValue(t, from, key, "old@"+key)
	}

	require.NoError(t, moveLayout(ctx, from, to), "the retry is no LayoutConflictError")
	requireMovedAndClean(t, from, to, "db", keys)
}

// A source Delete that fails mid-set leaves part of the source; the next
// pass deletes the rest.
func TestMoveLayout_SourceDeleteFailsMidSet(t *testing.T) {
	ctx := context.Background()
	mem, to := seal.NewMemoryBackend(), seal.NewMemoryBackend()
	keys := putLayout(t, mem, "db", "old")
	from := &faultyStore{Store: mem, op: "delete", key: versionKey("db", 1)}

	require.ErrorIs(t, moveLayout(ctx, from, to), errInjected)
	require.True(t, from.fired)
	requireAbsent(t, from, credentialPrefix+"db")
	requireValue(t, from, versionKey("db", 1), "old@"+versionKey("db", 1))

	require.NoError(t, moveLayout(ctx, from, to))
	requireMovedAndClean(t, from, to, "db", keys)
}

// A pass that stops after the last source Delete leaves only the marker;
// the next pass removes it.
func TestMoveLayout_LeftoverMarkerIsRemoved(t *testing.T) {
	ctx := context.Background()
	from, mem := seal.NewMemoryBackend(), seal.NewMemoryBackend()
	keys := putLayout(t, from, "db", "old")
	marker := markerOf(t, from, "db")
	to := &faultyStore{Store: mem, op: "delete", key: marker}

	require.ErrorIs(t, moveLayout(ctx, from, to), errInjected)
	require.True(t, to.fired)
	requireMarked(t, to, marker)

	require.NoError(t, moveLayout(ctx, from, to))
	requireMovedAndClean(t, from, to, "db", keys)
}

// A destination record without a move marker is still a conflict: the
// source copy is kept and no marker is written.
func TestMoveLayout_UnmarkedDestinationStillConflicts(t *testing.T) {
	ctx := context.Background()
	from, to := seal.NewMemoryBackend(), seal.NewMemoryBackend()
	keys := putLayout(t, from, "db", "old")
	require.NoError(t, to.Put(ctx, credentialPrefix+"db", []byte("new")))

	err := moveLayout(ctx, from, to)
	var conflict *LayoutConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, []string{"db"}, conflict.Names)
	for _, key := range keys {
		requireValue(t, from, key, "old@"+key)
	}
	requireValue(t, to, credentialPrefix+"db", "new")
	requireAbsent(t, to, markerOf(t, from, "db"))
}

// TestMoveLayout_ConflictOutlivesDestinationDelete: a name lost to a
// conflict stays lost. The destination copy is deleted after the conflict
// and the move runs again, as BindStoreFrom does at the next start: the
// stale source copy is not moved, so the deleted credential stays deleted.
// Once the source records are gone the conflict record goes too.
func TestMoveLayout_ConflictOutlivesDestinationDelete(t *testing.T) {
	ctx := context.Background()
	from, to := seal.NewMemoryBackend(), seal.NewMemoryBackend()
	keys := putLayout(t, from, "db", "old")
	require.NoError(t, to.Put(ctx, credentialPrefix+"db", []byte("new")))

	var conflict *LayoutConflictError
	require.ErrorAs(t, moveLayout(ctx, from, to), &conflict)
	require.NoError(t, to.Delete(ctx, credentialPrefix+"db"))

	err := moveLayout(ctx, from, to)
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, []string{"db"}, conflict.Names)
	requireAbsent(t, to, credentialPrefix+"db")
	for _, key := range keys {
		requireAbsent(t, to, key)
		requireValue(t, from, key, "old@"+key)
	}

	for _, key := range keys {
		require.NoError(t, from.Delete(ctx, key))
	}
	require.NoError(t, moveLayout(ctx, from, to))
	left, err := to.List(ctx, LayoutMovedPrefix)
	require.NoError(t, err)
	assert.Empty(t, left)
}

// A credential deleted after a move that stopped before its source records
// were gone does not come back on the next move pass.
func TestMoveLayout_DeletedCredentialNotResurrected(t *testing.T) {
	ctx := context.Background()
	first, second := seal.NewMemoryBackend(), seal.NewMemoryBackend()
	svc := newUnboundService(t)
	require.NoError(t, svc.BindStore(ctx, first))
	require.NoError(t, svc.StoreCredential(ctx, "db", []byte("v1")))

	from := &faultyStore{Store: first, op: "delete", key: credentialPrefix + "db"}
	require.ErrorIs(t, moveLayout(ctx, from, second), errInjected)
	require.NoError(t, svc.BindStore(ctx, second))
	assert.Equal(t, "v1", retrieveString(t, svc, "db"))
	require.NoError(t, svc.DeleteCredential(ctx, "db"))

	require.NoError(t, moveLayout(ctx, from, second))
	_, err := svc.retrieveBytes(ctx, "db")
	require.ErrorIs(t, err, ErrCredentialNotFound)
	for _, prefix := range layoutPrefixes {
		left, err := first.List(ctx, prefix)
		require.NoError(t, err)
		assert.Empty(t, left, "the earlier store keeps no record of the name")
	}
}

// TestMoveLayout_SettledNameFencesLaterSource covers a later source's first
// move after an earlier source's move of the same name finished and the
// credential was deleted from the destination: no marker, fence, conflict
// or credential record is left there, and the later source's stale copy must
// still not come back.
func TestMoveLayout_SettledNameFencesLaterSource(t *testing.T) {
	ctx := context.Background()
	a, b, dest := seal.NewMemoryBackend(), seal.NewMemoryBackend(), seal.NewMemoryBackend()
	svc := newUnboundService(t)
	values := []string{"from-a", "from-b"}
	for i, st := range []Store{a, b} {
		require.NoError(t, svc.BindStore(ctx, st))
		require.NoError(t, svc.StoreCredential(ctx, "db", []byte(values[i])))
		// A credential stored after the legacy migration has no claim.
		claim := MigrationClaimPrefix + "db"
		held, err := holdsKey(ctx, st, claim)
		require.NoError(t, err)
		if held {
			require.NoError(t, st.Delete(ctx, claim))
		}
	}

	require.NoError(t, moveLayout(ctx, a, dest))
	require.NoError(t, svc.BindStore(ctx, dest))
	assert.Equal(t, "from-a", retrieveString(t, svc, "db"))
	require.NoError(t, svc.DeleteCredential(ctx, "db"))

	var conflict *LayoutConflictError
	require.ErrorAs(t, moveLayout(ctx, b, dest), &conflict)
	assert.Equal(t, []string{"db"}, conflict.Names)
	_, err := svc.retrieveBytes(ctx, "db")
	require.ErrorIs(t, err, ErrCredentialNotFound)
	kept, err := holdsKey(ctx, b, credentialPrefix+"db")
	require.NoError(t, err)
	assert.True(t, kept, "the later source keeps its copy")
}
