// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

var errInjected = errors.New("injected store failure")

// faultyStore fails the named operation on the named key, once.
type faultyStore struct {
	Store
	mu    sync.Mutex
	op    string
	key   string
	fired bool
}

func (f *faultyStore) fail(op, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.fired && f.op == op && f.key == key {
		f.fired = true
		return errInjected
	}
	return nil
}

func (f *faultyStore) Put(ctx context.Context, key string, value []byte) error {
	if err := f.fail("put", key); err != nil {
		return err
	}
	return f.Store.Put(ctx, key, value)
}

func (f *faultyStore) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if err := f.fail("putifabsent", key); err != nil {
		return false, err
	}
	return f.Store.PutIfAbsent(ctx, key, value)
}

func (f *faultyStore) Delete(ctx context.Context, key string) error {
	if err := f.fail("delete", key); err != nil {
		return err
	}
	return f.Store.Delete(ctx, key)
}

func (f *faultyStore) List(ctx context.Context, prefix string) ([]string, error) {
	if err := f.fail("list", prefix); err != nil {
		return nil, err
	}
	return f.Store.List(ctx, prefix)
}

var _ Store = (*faultyStore)(nil)

// writeLegacy writes a credential under the old credentials/ layout: value
// as version 2, prior as retained version 1, and policy in its meta.
func writeLegacy(t *testing.T, st Store, name, value, prior string, policy *RotationPolicy) {
	t.Helper()
	ctx := context.Background()
	created := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	meta, err := json.Marshal(credentialMeta{
		CurrentVersion: 2,
		History:        []credentialMetaEntry{{Version: 1, CreatedAt: created}},
		Policy:         policy,
	})
	require.NoError(t, err)
	require.NoError(t, st.Put(ctx, legacyCredentialPrefix+name, []byte(value)))
	require.NoError(t, st.Put(ctx, legacyCredentialPrefix+name+metaSuffix, meta))
	require.NoError(t, st.Put(ctx, legacyCredentialPrefix+name+versionPrefix+"1", []byte(prior)))
}

// legacyKeys lists the legacy store's keys, sorted.
func legacyKeys(t *testing.T, st Store) []string {
	t.Helper()
	keys, err := st.List(context.Background(), legacyCredentialPrefix)
	require.NoError(t, err)
	notes, err := st.List(context.Background(), legacyMigrationNotePrefix)
	require.NoError(t, err)
	keys = append(keys, notes...)
	sort.Strings(keys)
	return keys
}

func newMigratingService(t *testing.T, legacy Store, opts ...Option) *CredentialService {
	t.Helper()
	return newUnboundServiceWith(t, append([]Option{WithLegacyStore(legacy)}, opts...)...)
}

func retrieveString(t *testing.T, svc *CredentialService, name string) string {
	t.Helper()
	got, err := svc.retrieveBytes(context.Background(), name)
	require.NoError(t, err)
	return string(got)
}

func versionString(t *testing.T, svc *CredentialService, name string, v uint64) string {
	t.Helper()
	got, err := svc.retrieveVersionBytes(context.Background(), name, v)
	require.NoError(t, err)
	return string(got)
}

var testPolicy = &RotationPolicy{Enabled: true, Interval: time.Hour, RetainVersions: 2}

// TestMigrateLegacy_SingleNode verifies a name written on one node migrates
// fully: value, versions and policy reach the replicated store, and the old
// records are deleted.
func TestMigrateLegacy_SingleNode(t *testing.T) {
	ctx := context.Background()
	legacy, shared := seal.NewMemoryBackend(), seal.NewMemoryBackend()
	writeLegacy(t, legacy, "db", "v2", "v1", testPolicy)
	al := &fakeAuditLogger{}

	svc := newMigratingService(t, legacy, WithAuditLogger(al))
	require.NoError(t, svc.BindStore(ctx, shared))

	assert.Equal(t, "v2", retrieveString(t, svc, "db"))
	assert.Equal(t, "v1", versionString(t, svc, "db", 1))
	got, err := svc.RotationPolicy(ctx, "db")
	require.NoError(t, err)
	assert.Equal(t, *testPolicy, got)
	names, err := svc.ListCredentials(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"db"}, names)
	assert.Empty(t, legacyKeys(t, legacy))
	conflicts, err := svc.ListMigrationConflicts(ctx)
	require.NoError(t, err)
	assert.Empty(t, conflicts)
	ev, ok := al.findByAction("migrate")
	require.True(t, ok)
	assert.Equal(t, audit.OutcomeSuccess, ev.Outcome)
}

// A credential name may contain '#', so a legacy credential can carry any
// suffix a migration record ever had. It migrates with its value beside the
// credential it shares a stem with, and is never read as a migration note.
func TestMigrateLegacy_NameEndingInMigratedSurvives(t *testing.T) {
	ctx := context.Background()
	legacy, shared := seal.NewMemoryBackend(), seal.NewMemoryBackend()
	writeLegacy(t, legacy, "db", "v2", "v1", testPolicy)
	writeLegacy(t, legacy, "db#migrated", "other-v2", "other-v1", nil)

	svc := newMigratingService(t, legacy)
	require.NoError(t, svc.BindStore(ctx, shared))

	assert.Equal(t, "v2", retrieveString(t, svc, "db"))
	assert.Equal(t, "v1", versionString(t, svc, "db", 1))
	assert.Equal(t, "other-v2", retrieveString(t, svc, "db#migrated"))
	assert.Equal(t, "other-v1", versionString(t, svc, "db#migrated", 1))
	assert.Empty(t, legacyKeys(t, legacy))
	conflicts, err := svc.ListMigrationConflicts(ctx)
	require.NoError(t, err)
	assert.Empty(t, conflicts)

	later := newMigratingService(t, seal.NewMemoryBackend())
	require.NoError(t, later.BindStore(ctx, shared))
	assert.Equal(t, "v2", retrieveString(t, later, "db"))
	assert.Equal(t, "other-v2", retrieveString(t, later, "db#migrated"))
}

// TestMigrateLegacy_IdenticalLaterNode verifies a later node whose copy is
// byte-identical deletes its old records and reports no conflict.
func TestMigrateLegacy_IdenticalLaterNode(t *testing.T) {
	ctx := context.Background()
	legacyA, legacyB, shared := seal.NewMemoryBackend(), seal.NewMemoryBackend(), seal.NewMemoryBackend()
	writeLegacy(t, legacyA, "db", "v2", "v1", testPolicy)
	writeLegacy(t, legacyB, "db", "v2", "v1", testPolicy)

	nodeA := newMigratingService(t, legacyA)
	require.NoError(t, nodeA.BindStore(ctx, shared))
	// A rotation after A migrated must survive B's migration.
	_, err := nodeA.RotateCredential(ctx, "db", RotateOptions{NewValue: []byte("v3")})
	require.NoError(t, err)

	nodeB := newMigratingService(t, legacyB)
	require.NoError(t, nodeB.BindStore(ctx, shared))

	assert.Empty(t, legacyKeys(t, legacyB))
	conflicts, err := nodeB.ListMigrationConflicts(ctx)
	require.NoError(t, err)
	assert.Empty(t, conflicts)
	assert.Equal(t, "v3", retrieveString(t, nodeB, "db"))
}

// TestMigrateLegacy_DifferingLaterNode verifies a later node whose copy
// differs keeps its old records untouched and reports the conflict to the
// log's caller, the audit log and the conflict list.
func TestMigrateLegacy_DifferingLaterNode(t *testing.T) {
	ctx := context.Background()
	legacyA, legacyB, shared := seal.NewMemoryBackend(), seal.NewMemoryBackend(), seal.NewMemoryBackend()
	writeLegacy(t, legacyA, "db", "v2", "v1", testPolicy)
	writeLegacy(t, legacyB, "db", "other", "v1", nil)
	writeLegacy(t, legacyB, "api", "tok", "tok0", nil)
	before := legacyKeys(t, legacyB)
	al := &fakeAuditLogger{}

	nodeA := newMigratingService(t, legacyA)
	require.NoError(t, nodeA.BindStore(ctx, shared))
	nodeB := newMigratingService(t, legacyB, WithAuditLogger(al))
	require.NoError(t, nodeB.BindStore(ctx, shared))

	conflicts, err := nodeB.ListMigrationConflicts(ctx)
	require.NoError(t, err)
	require.Len(t, conflicts, 1)
	assert.Equal(t, CredentialMigrationConflictError{Name: "db", Value: true, Policy: true}, conflicts[0])
	assert.Equal(t, "v2", retrieveString(t, nodeB, "db"), "the first node's copy is the record")
	assert.Equal(t, "tok", retrieveString(t, nodeB, "api"), "a name only B holds migrates")

	dbKeys := []string{
		legacyCredentialPrefix + "db",
		legacyCredentialPrefix + "db" + metaSuffix,
		legacyCredentialPrefix + "db" + versionPrefix + "1",
	}
	assert.Len(t, before, 6)
	assert.Equal(t, dbKeys, legacyKeys(t, legacyB), "the differing copy is kept untouched")
	value, err := legacyB.Get(ctx, legacyCredentialPrefix+"db")
	require.NoError(t, err)
	assert.Equal(t, "other", string(value))

	var conflictEv *audit.AuditEvent
	al.mu.Lock()
	for i := range al.events {
		if al.events[i].Action == "migrate" && al.events[i].Outcome == audit.OutcomeFailure {
			conflictEv = &al.events[i]
		}
	}
	al.mu.Unlock()
	require.NotNil(t, conflictEv)
	assert.Equal(t, "credential:db", conflictEv.Resource)
	assert.Equal(t, []string{"value", "policy"}, conflictEv.Attributes["conflict"])
}

// TestMigrateLegacy_DiffersFromLiveRecord verifies a copy that differs from
// a record written on the replicated layout before any node migrated is a
// conflict, and the live record is kept.
func TestMigrateLegacy_DiffersFromLiveRecord(t *testing.T) {
	ctx := context.Background()
	legacy, shared := seal.NewMemoryBackend(), seal.NewMemoryBackend()
	live := newUnboundService(t)
	require.NoError(t, live.BindStore(ctx, shared))
	require.NoError(t, live.StoreCredential(ctx, "db", []byte("live")))
	writeLegacy(t, legacy, "db", "v2", "v1", nil)

	svc := newMigratingService(t, legacy)
	require.NoError(t, svc.BindStore(ctx, shared))

	conflicts, err := svc.ListMigrationConflicts(ctx)
	require.NoError(t, err)
	require.Len(t, conflicts, 1)
	assert.True(t, conflicts[0].Value)
	assert.Equal(t, "live", retrieveString(t, svc, "db"))
	assert.Len(t, legacyKeys(t, legacy), 3)
}

// TestMigrateLegacy_CrashWritingRecords verifies a run interrupted after its
// claim, with only part of its records written, finishes on the re-run with
// the same result as an uninterrupted run.
func TestMigrateLegacy_CrashWritingRecords(t *testing.T) {
	ctx := context.Background()
	legacy, mem := seal.NewMemoryBackend(), seal.NewMemoryBackend()
	writeLegacy(t, legacy, "db", "v2", "v1", testPolicy)
	shared := &faultyStore{Store: mem, op: "putifabsent", key: credentialPrefix + "db"}

	first := newMigratingService(t, legacy)
	require.NoError(t, first.BindStore(ctx, shared), "a failed migration leaves the store bound")
	require.True(t, shared.fired)
	assert.Len(t, legacyKeys(t, legacy), 3, "nothing is deleted before the copy is written")
	// The value is read from the store itself: a retrieve through the
	// service would run the unfinished migration again first.
	_, err := mem.Get(ctx, credentialPrefix+"db")
	require.ErrorIs(t, err, seal.ErrSecretNotFound)

	rerun := newMigratingService(t, legacy)
	require.NoError(t, rerun.BindStore(ctx, shared))
	assert.Equal(t, "v2", retrieveString(t, rerun, "db"))
	assert.Equal(t, "v1", versionString(t, rerun, "db", 1))
	assert.Empty(t, legacyKeys(t, legacy))
	conflicts, err := rerun.ListMigrationConflicts(ctx)
	require.NoError(t, err)
	assert.Empty(t, conflicts)
}

// TestMigrateLegacy_CrashDeletingLegacy verifies a run interrupted while it
// deletes the old records finishes the deletion on the re-run, instead of
// taking what is left for a differing copy.
func TestMigrateLegacy_CrashDeletingLegacy(t *testing.T) {
	ctx := context.Background()
	mem, shared := seal.NewMemoryBackend(), seal.NewMemoryBackend()
	writeLegacy(t, mem, "db", "v2", "v1", testPolicy)
	legacy := &faultyStore{Store: mem, op: "delete", key: legacyCredentialPrefix + "db" + metaSuffix}

	first := newMigratingService(t, legacy)
	require.NoError(t, first.BindStore(ctx, shared))
	require.True(t, legacy.fired)

	rerun := newMigratingService(t, legacy)
	require.NoError(t, rerun.BindStore(ctx, shared))
	assert.Empty(t, legacyKeys(t, mem))
	conflicts, err := rerun.ListMigrationConflicts(ctx)
	require.NoError(t, err)
	assert.Empty(t, conflicts)
	assert.Equal(t, "v2", retrieveString(t, rerun, "db"))
}

// TestMigrateLegacy_RerunIsIdempotent verifies a second run after a
// complete one changes nothing.
func TestMigrateLegacy_RerunIsIdempotent(t *testing.T) {
	ctx := context.Background()
	legacy, shared := seal.NewMemoryBackend(), seal.NewMemoryBackend()
	writeLegacy(t, legacy, "db", "v2", "v1", nil)
	svc := newMigratingService(t, legacy)
	require.NoError(t, svc.BindStore(ctx, shared))
	keys, err := shared.List(ctx, "")
	require.NoError(t, err)

	again := newMigratingService(t, legacy)
	require.NoError(t, again.BindStore(ctx, shared))
	after, err := shared.List(ctx, "")
	require.NoError(t, err)
	assert.ElementsMatch(t, keys, after)
	assert.Equal(t, "v2", retrieveString(t, again, "db"))
}

// TestMigrateLegacy_UnreadableLegacyRetried verifies a migration whose
// legacy store cannot be listed leaves the store bound and runs again on
// the next verb.
func TestMigrateLegacy_UnreadableLegacyRetried(t *testing.T) {
	ctx := context.Background()
	mem, shared := seal.NewMemoryBackend(), seal.NewMemoryBackend()
	writeLegacy(t, mem, "db", "v2", "v1", nil)
	legacy := &faultyStore{Store: mem, op: "list", key: legacyCredentialPrefix}

	svc := newMigratingService(t, legacy)
	require.NoError(t, svc.BindStore(ctx, shared))
	require.NoError(t, svc.StoreCredential(ctx, "other", []byte("x")), "the store is bound")

	conflicts, err := svc.ListMigrationConflicts(ctx)
	require.NoError(t, err)
	assert.Empty(t, conflicts)
	assert.Equal(t, "v2", retrieveString(t, svc, "db"))
}

// TestMigrateLegacy_NothingToMigrate verifies a service with no legacy
// store, or of the manual strategy, migrates nothing.
func TestMigrateLegacy_NothingToMigrate(t *testing.T) {
	ctx := context.Background()
	svc := newUnboundService(t)
	require.NoError(t, svc.BindStore(ctx, seal.NewMemoryBackend()))
	require.NoError(t, svc.MigrateLegacy(ctx))

	legacy := seal.NewMemoryBackend()
	writeLegacy(t, legacy, "db", "v2", "v1", nil)
	manual, err := NewCredentialService(nil, nil, testLogger(), WithLegacyStore(legacy))
	require.NoError(t, err)
	require.NoError(t, manual.MigrateLegacy(ctx))
	assert.Len(t, legacyKeys(t, legacy), 3)
}

// conflictedNode returns node B holding a differing copy of "db".
func conflictedNode(t *testing.T, opts ...Option) (*CredentialService, Store) {
	t.Helper()
	ctx := context.Background()
	legacyA, legacyB, shared := seal.NewMemoryBackend(), seal.NewMemoryBackend(), seal.NewMemoryBackend()
	writeLegacy(t, legacyA, "db", "v2", "v1", nil)
	writeLegacy(t, legacyB, "db", "kept", "kept-old", nil)
	require.NoError(t, newMigratingService(t, legacyA).BindStore(ctx, shared))
	nodeB := newMigratingService(t, legacyB, opts...)
	require.NoError(t, nodeB.BindStore(ctx, shared))
	conflicts, err := nodeB.ListMigrationConflicts(ctx)
	require.NoError(t, err)
	require.Len(t, conflicts, 1)
	return nodeB, legacyB
}

// TestResolveMigrationConflict_Import verifies importing writes the kept
// value as a new version, retains the replaced one, deletes the kept copy
// and audits what it did not carry over.
func TestResolveMigrationConflict_Import(t *testing.T) {
	ctx := context.Background()
	al := &fakeAuditLogger{}
	nodeB, legacyB := conflictedNode(t, WithAuditLogger(al))

	require.NoError(t, nodeB.ResolveMigrationConflict(ctx, "db", MigrationImport))

	assert.Equal(t, "kept", retrieveString(t, nodeB, "db"))
	assert.Equal(t, "v2", versionString(t, nodeB, "db", 2), "the replaced version is retained")
	assert.Empty(t, legacyKeys(t, legacyB))
	conflicts, err := nodeB.ListMigrationConflicts(ctx)
	require.NoError(t, err)
	assert.Empty(t, conflicts)
	ev, ok := al.findByAction("migrate_resolve")
	require.True(t, ok)
	assert.Equal(t, "import", ev.Attributes["resolution"])
	assert.Equal(t, uint64(3), ev.Attributes["new_version"])
	assert.Equal(t, []string{metaSuffix, versionPrefix + "1"}, ev.Attributes["dropped_records"])
}

// TestResolveMigrationConflict_ImportRetriedAfterCleanupFailure verifies an
// import whose cleanup failed is not imported again: the retry creates no
// version, leaves a credential rotated since in place, and only finishes
// deleting the kept copy.
func TestResolveMigrationConflict_ImportRetriedAfterCleanupFailure(t *testing.T) {
	ctx := context.Background()
	legacyA, shared := seal.NewMemoryBackend(), seal.NewMemoryBackend()
	legacyB := &faultyStore{Store: seal.NewMemoryBackend()}
	writeLegacy(t, legacyA, "db", "v2", "v1", nil)
	writeLegacy(t, legacyB, "db", "kept", "kept-old", nil)
	require.NoError(t, newMigratingService(t, legacyA).BindStore(ctx, shared))
	nodeB := newMigratingService(t, legacyB)
	require.NoError(t, nodeB.BindStore(ctx, shared))
	arm := func(op, key string) {
		legacyB.mu.Lock()
		defer legacyB.mu.Unlock()
		legacyB.op, legacyB.key, legacyB.fired = op, key, false
	}
	requireUntouched := func(msg string) {
		t.Helper()
		meta, err := nodeB.loadBarrierMeta(ctx, "db")
		require.NoError(t, err)
		assert.Equal(t, uint64(4), meta.CurrentVersion, msg)
		assert.Equal(t, "newer", retrieveString(t, nodeB, "db"), msg)
	}

	// The import commits, then the note that opens the cleanup fails.
	arm("put", legacyMigrationNotePrefix+"db")
	require.ErrorIs(t, nodeB.ResolveMigrationConflict(ctx, "db", MigrationImport), errInjected)
	assert.Equal(t, "kept", retrieveString(t, nodeB, "db"))
	_, err := nodeB.rotateBarrier(ctx, "db", RotateOptions{NewValue: []byte("newer")})
	require.NoError(t, err)

	// The retry gets as far as deleting the kept copy, which fails.
	arm("delete", legacyCredentialPrefix+"db")
	require.ErrorIs(t, nodeB.ResolveMigrationConflict(ctx, "db", MigrationImport), errInjected)
	requireUntouched("a retried import must not import the kept copy again")

	require.NoError(t, nodeB.ResolveMigrationConflict(ctx, "db", MigrationImport))
	requireUntouched("finishing the cleanup must not import the kept copy again")
	assert.Empty(t, legacyKeys(t, legacyB))
	conflicts, err := nodeB.ListMigrationConflicts(ctx)
	require.NoError(t, err)
	assert.Empty(t, conflicts)
}

// getHookStore runs onGet after each read, with the key read, so a test can
// place another service's work between two reads of one operation.
type getHookStore struct {
	Store
	onGet func(key string)
}

func (g *getHookStore) Get(ctx context.Context, key string) ([]byte, error) {
	value, err := g.Store.Get(ctx, key)
	if g.onGet != nil {
		g.onGet(key)
	}
	return value, err
}

// Each request derives its own service, so two resolutions of one tenant's
// conflict do not share a lock: both can find the copy not yet imported
// before either commits. The second must then be refused by the write itself.
func TestResolveMigrationConflict_ImportRacingAcrossDerivedServices(t *testing.T) {
	ctx := context.Background()
	al := &fakeAuditLogger{}
	home := &getHookStore{Store: newHomeStore(t)}
	legacyA := seal.NewMemoryBackend()
	legacyB := newLegacyBarrier(t)
	writeLegacy(t, legacyA, "db", "v2", "v1", nil)
	writeLegacy(t, legacyB, "db", "kept", "kept-old", nil)
	require.NoError(t, newMigratingService(t, legacyA).BindStore(ctx, home))
	parent := newUnboundServiceWith(t, WithAuditLogger(al))
	first, err := parent.ForTenant(ctx, home, legacyB)
	require.NoError(t, err)
	second, err := parent.ForTenant(ctx, home, legacyB)
	require.NoError(t, err)
	for _, svc := range []*CredentialService{first, second} {
		conflicts, err := svc.ListMigrationConflicts(ctx)
		require.NoError(t, err)
		require.Len(t, conflicts, 1)
	}

	// The second resolution reads the metadata and finds nothing imported;
	// before it writes, the first one imports the copy and the credential
	// is rotated.
	fired := false
	home.onGet = func(key string) {
		if fired || key != metaKey("db") {
			return
		}
		fired = true
		require.NoError(t, first.ResolveMigrationConflict(ctx, "db", MigrationImport))
		_, err := first.rotateBarrier(ctx, "db", RotateOptions{NewValue: []byte("newer")})
		require.NoError(t, err)
	}
	require.NoError(t, second.ResolveMigrationConflict(ctx, "db", MigrationImport))
	home.onGet = nil
	require.True(t, fired)

	assert.Equal(t, "newer", retrieveString(t, second, "db"), "a rotation since the import is not replaced")
	meta, err := second.loadBarrierMeta(ctx, "db")
	require.NoError(t, err)
	assert.Equal(t, uint64(4), meta.CurrentVersion, "the copy is imported as one version")
	assert.Len(t, meta.Imported, 1)
	assert.Empty(t, legacyKeys(t, legacyB))
	conflicts, err := second.ListMigrationConflicts(ctx)
	require.NoError(t, err)
	assert.Empty(t, conflicts)

	var created, already int
	al.mu.Lock()
	defer al.mu.Unlock()
	for _, ev := range al.events {
		if ev.Attributes["resolution"] != string(MigrationImport) {
			continue
		}
		if _, ok := ev.Attributes["new_version"]; ok {
			created++
		}
		if ev.Attributes["already_imported"] == true {
			already++
		}
	}
	assert.Equal(t, 1, created, "exactly one resolution creates a version")
	assert.Equal(t, 1, already, "the other reports the copy already imported")
}

// TestResolveMigrationConflict_Discard verifies discarding deletes the kept
// copy, leaves the replicated record and audits every dropped record.
func TestResolveMigrationConflict_Discard(t *testing.T) {
	ctx := context.Background()
	al := &fakeAuditLogger{}
	nodeB, legacyB := conflictedNode(t, WithAuditLogger(al))

	require.NoError(t, nodeB.ResolveMigrationConflict(ctx, "db", MigrationDiscard))

	assert.Equal(t, "v2", retrieveString(t, nodeB, "db"))
	assert.Empty(t, legacyKeys(t, legacyB))
	ev, ok := al.findByAction("migrate_resolve")
	require.True(t, ok)
	assert.Equal(t, "discard", ev.Attributes["resolution"])
	assert.Equal(t, []string{metaSuffix, versionPrefix + "1", "current"}, ev.Attributes["dropped_records"])
	assert.ErrorIs(t, nodeB.ResolveMigrationConflict(ctx, "db", MigrationDiscard), ErrMigrationConflictNotFound)
}

// TestMigrateLegacy_NamesContainingHashKeepTheirConflicts verifies a legacy
// credential whose name holds a '#' migrates, conflicts and resolves under
// its own name: "db#prod" is not a record of "db", and "#lead" is not a
// record of the empty name. A "db" with its own metadata and version beside
// "db#prod" stays a second credential.
func TestMigrateLegacy_NamesContainingHashKeepTheirConflicts(t *testing.T) {
	ctx := context.Background()
	for _, how := range []MigrationResolution{MigrationImport, MigrationDiscard} {
		t.Run(string(how), func(t *testing.T) {
			legacyA, legacyB, shared := seal.NewMemoryBackend(), seal.NewMemoryBackend(), seal.NewMemoryBackend()
			for _, name := range []string{"db#prod", "#lead"} {
				writeLegacy(t, legacyA, name, name+"-v2", name+"-v1", nil)
				writeLegacy(t, legacyB, name, name+"-kept", name+"-kept-old", nil)
			}
			require.NoError(t, newMigratingService(t, legacyA).BindStore(ctx, shared))
			nodeB := newMigratingService(t, legacyB)
			require.NoError(t, nodeB.BindStore(ctx, shared))

			conflicts, err := nodeB.ListMigrationConflicts(ctx)
			require.NoError(t, err)
			names := make([]string, 0, len(conflicts))
			for _, c := range conflicts {
				names = append(names, c.Name)
			}
			sort.Strings(names)
			require.Equal(t, []string{"#lead", "db#prod"}, names)

			for _, name := range names {
				require.NoError(t, nodeB.ResolveMigrationConflict(ctx, name, how))
				want := name + "-v2"
				if how == MigrationImport {
					want = name + "-kept"
					assert.Equal(t, name+"-v2", versionString(t, nodeB, name, 2), "the replaced version is retained")
				}
				assert.Equal(t, want, retrieveString(t, nodeB, name))
				assert.Equal(t, name+"-v1", versionString(t, nodeB, name, 1))
				assert.ErrorIs(t, nodeB.ResolveMigrationConflict(ctx, name, how), ErrMigrationConflictNotFound)
			}
			assert.Empty(t, legacyKeys(t, legacyB))
			conflicts, err = nodeB.ListMigrationConflicts(ctx)
			require.NoError(t, err)
			assert.Empty(t, conflicts)
		})
	}

	t.Run("a credential beside one whose name extends it", func(t *testing.T) {
		legacy, shared := seal.NewMemoryBackend(), seal.NewMemoryBackend()
		writeLegacy(t, legacy, "db", "db-v2", "db-v1", nil)
		writeLegacy(t, legacy, "db#prod", "prod-v2", "prod-v1", nil)
		svc := newMigratingService(t, legacy)
		require.NoError(t, svc.BindStore(ctx, shared))

		conflicts, err := svc.ListMigrationConflicts(ctx)
		require.NoError(t, err)
		assert.Empty(t, conflicts)
		assert.Empty(t, legacyKeys(t, legacy))
		names, err := svc.ListCredentials(ctx)
		require.NoError(t, err)
		assert.Equal(t, []string{"db", "db#prod"}, names)
		assert.Equal(t, "db-v2", retrieveString(t, svc, "db"))
		assert.Equal(t, "db-v1", versionString(t, svc, "db", 1))
		assert.Equal(t, "prod-v2", retrieveString(t, svc, "db#prod"))
		assert.Equal(t, "prod-v1", versionString(t, svc, "db#prod", 1))
	})
}

// TestResolveMigrationConflict_Refused verifies an invalid name, an unknown
// resolution and a name with no conflict are refused and keep the copy.
func TestResolveMigrationConflict_Refused(t *testing.T) {
	ctx := context.Background()
	nodeB, legacyB := conflictedNode(t)

	assert.ErrorIs(t, nodeB.ResolveMigrationConflict(ctx, "", MigrationImport), ErrEmptyCredentialName)
	assert.ErrorIs(t, nodeB.ResolveMigrationConflict(ctx, "db", "merge"), ErrInvalidMigrationResolution)
	assert.ErrorIs(t, nodeB.ResolveMigrationConflict(ctx, "nope", MigrationImport), ErrMigrationConflictNotFound)
	assert.Len(t, legacyKeys(t, legacyB), 3)
}

// TestResolveMigrationConflict_ImportWithoutValue verifies a kept copy with
// no current value cannot be imported and is kept.
func TestResolveMigrationConflict_ImportWithoutValue(t *testing.T) {
	ctx := context.Background()
	nodeB, legacyB := conflictedNode(t)
	require.NoError(t, legacyB.Delete(ctx, legacyCredentialPrefix+"db"))

	assert.ErrorIs(t, nodeB.ResolveMigrationConflict(ctx, "db", MigrationImport), ErrCredentialNotFound)
	assert.Len(t, legacyKeys(t, legacyB), 2)
}

// The old API accepted any non-blank name, so a node may hold credentials
// whose names end like a metadata or version record of another credential.
// Each migrates as itself unless validated metadata accounts for it.
func TestMigrateLegacy_NamesEndingInRecordSuffixesAreCredentials(t *testing.T) {
	ctx := context.Background()

	t.Run("beside an unrotated credential", func(t *testing.T) {
		legacy, shared := seal.NewMemoryBackend(), seal.NewMemoryBackend()
		own := map[string]string{"db": "db-value", "db#meta": "meta-secret", "db#v1": "v1-secret"}
		for name, value := range own {
			require.NoError(t, legacy.Put(ctx, legacyCredentialPrefix+name, []byte(value)))
		}
		svc := newMigratingService(t, legacy)
		require.NoError(t, svc.BindStore(ctx, shared))

		conflicts, err := svc.ListMigrationConflicts(ctx)
		require.NoError(t, err)
		assert.Empty(t, conflicts)
		names, err := svc.ListCredentials(ctx)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"db", "db#meta", "db#v1"}, names)
		for name, value := range own {
			assert.Equal(t, value, retrieveString(t, svc, name), name)
		}
		meta, err := svc.loadBarrierMeta(ctx, "db")
		require.NoError(t, err)
		assert.Zero(t, meta.CurrentVersion)
		assert.Empty(t, meta.History)
		assert.Empty(t, legacyKeys(t, legacy))
	})

	t.Run("beside a rotated credential", func(t *testing.T) {
		legacy, shared := seal.NewMemoryBackend(), seal.NewMemoryBackend()
		writeLegacy(t, legacy, "db", "v2", "v1", nil)
		require.NoError(t, legacy.Put(ctx, legacyCredentialPrefix+"db#v7", []byte("other")))
		svc := newMigratingService(t, legacy)
		require.NoError(t, svc.BindStore(ctx, shared))

		names, err := svc.ListCredentials(ctx)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"db", "db#v7"}, names)
		assert.Equal(t, "other", retrieveString(t, svc, "db#v7"))
		assert.Equal(t, "v2", retrieveString(t, svc, "db"))
		assert.Equal(t, "v1", versionString(t, svc, "db", 1))
		assert.Empty(t, legacyKeys(t, legacy))
	})
}
