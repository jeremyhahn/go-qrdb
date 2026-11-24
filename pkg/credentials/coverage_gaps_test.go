package credentials

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

var errListFault = errors.New("list fault")

// listFaultStore fails every List while fail is set.
type listFaultStore struct {
	Store
	fail atomic.Bool
}

func (s *listFaultStore) List(ctx context.Context, prefix string) ([]string, error) {
	if s.fail.Load() {
		return nil, errListFault
	}
	return s.Store.List(ctx, prefix)
}

func doneCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func boundService(t *testing.T, st Store, opts ...Option) *CredentialService {
	t.Helper()
	svc := newUnboundServiceWith(t, opts...)
	require.NoError(t, svc.BindStore(context.Background(), st))
	return svc
}

func TestCoverage_AdmissionWaitCancelledWhileStoreRebinds(t *testing.T) {
	svc := boundService(t, newHomeStore(t))
	svc.admitMu.Lock()
	defer svc.admitMu.Unlock()
	ctx := doneCtx()

	assert.ErrorIs(t, svc.MigrateLegacy(ctx), context.Canceled)
	_, err := svc.ListMigrationConflicts(ctx)
	assert.ErrorIs(t, err, context.Canceled)
	assert.ErrorIs(t, svc.ResolveMigrationConflict(ctx, "db", MigrationImport), context.Canceled)

	err = svc.ensureLayoutMovedAdmitted(ctx)
	var opErr *OperationError
	require.ErrorAs(t, err, &opErr)
	assert.Equal(t, "move credential store", opErr.Op)
	assert.ErrorIs(t, err, context.Canceled)

	_, err = svc.RetrieveCredentialBuffer(ctx, "db")
	require.ErrorAs(t, err, &opErr)
	assert.Equal(t, "migrate", opErr.Op)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestCoverage_MigrationWaitCancelledWhileMigrationHeld(t *testing.T) {
	svc := boundService(t, newHomeStore(t))
	svc.migrateMu.Lock()
	defer svc.migrateMu.Unlock()
	ctx := doneCtx()

	assert.ErrorIs(t, svc.MigrateLegacy(ctx), context.Canceled)
	_, err := svc.ListMigrationConflicts(ctx)
	assert.ErrorIs(t, err, context.Canceled)
	assert.ErrorIs(t, svc.ResolveMigrationConflict(ctx, "db", MigrationImport), context.Canceled)
}

func TestCoverage_LayoutWaitCancelledWhileMoveHeld(t *testing.T) {
	svc := boundService(t, newHomeStore(t))
	svc.layoutMu.Lock()
	defer svc.layoutMu.Unlock()
	ctx := doneCtx()

	var opErr *OperationError
	err := svc.ensureLayoutMoved(ctx)
	require.ErrorAs(t, err, &opErr)
	assert.Equal(t, "move credential store", opErr.Op)
	assert.ErrorIs(t, err, context.Canceled)

	_, err = svc.RetrieveCredentialBuffer(ctx, "db")
	assert.ErrorIs(t, err, context.Canceled)
}

func TestCoverage_UnboundedAttemptContexts(t *testing.T) {
	svc := boundService(t, newHomeStore(t))
	svc.rotationAttemptTimeout = 0
	parent, pcancel := context.WithCancel(context.Background())
	defer pcancel()

	mctx, mcancel := svc.migrationContext(parent)
	defer mcancel()
	_, ok := mctx.Deadline()
	assert.False(t, ok, "no attempt timeout sets no deadline")

	actx, acancel := svc.attemptContext(parent)
	_, ok = actx.Deadline()
	assert.False(t, ok, "no attempt timeout sets no deadline")
	acancel()
	assert.ErrorIs(t, actx.Err(), context.Canceled)
	assert.NoError(t, parent.Err(), "cancelling the attempt leaves the caller's context")

	require.NoError(t, svc.MigrateLegacy(context.Background()))
}

func TestCoverage_BindStoreFromKeepsBindWhenMigrationFails(t *testing.T) {
	ctx := context.Background()
	legacy := &listFaultStore{Store: seal.NewMemoryBackend()}
	legacy.fail.Store(true)
	svc := newMigratingService(t, legacy)

	require.NoError(t, svc.BindStoreFrom(ctx, newHomeStore(t), newHomeStore(t)),
		"a migration that cannot finish does not fail the bind")
	assert.Nil(t, svc.layoutFrom, "the move finished before the migration ran")
	_, err := svc.ListMigrationConflicts(ctx)
	assert.ErrorIs(t, err, errListFault)

	legacy.fail.Store(false)
	conflicts, err := svc.ListMigrationConflicts(ctx)
	require.NoError(t, err)
	assert.Empty(t, conflicts)
}

func TestCoverage_AuditTenantFromBoundContext(t *testing.T) {
	al := &fakeAuditLogger{}
	svc := boundService(t, newHomeStore(t), WithAuditLogger(al))
	ctx := tenant.WithTenantID(context.Background(), "acme")

	require.NoError(t, svc.StoreCredential(ctx, "db", []byte("v1")))
	ev, ok := al.findByAction("store")
	require.True(t, ok)
	assert.Equal(t, "acme", ev.TenantID)
	assert.Empty(t, ev.Actor)
}

func TestCoverage_DeleteListStagedFailure(t *testing.T) {
	ctx := context.Background()
	st := &listFaultStore{Store: newHomeStore(t)}
	svc := boundService(t, st)
	require.NoError(t, svc.StoreCredential(ctx, "db", []byte("v1")))

	st.fail.Store(true)
	err := svc.DeleteCredential(ctx, "db")
	var opErr *OperationError
	require.ErrorAs(t, err, &opErr)
	assert.Equal(t, "list staged rotations", opErr.Op)
	assert.ErrorIs(t, err, errListFault)

	st.fail.Store(false)
	assert.Equal(t, "v1", retrieveString(t, svc, "db"), "a failed delete removes nothing")
}

func TestCoverage_DeleteSkipsMalformedStagedKey(t *testing.T) {
	ctx := context.Background()
	st := newHomeStore(t)
	svc := boundService(t, st)
	require.NoError(t, svc.StoreCredential(ctx, "db", []byte("v1")))
	stray := StagedKeyPrefix + encodedName("db") + "/not-a-version"
	require.NoError(t, st.Put(ctx, stray, []byte("x")))

	require.NoError(t, svc.DeleteCredential(ctx, "db"))
	_, err := svc.retrieveBytes(ctx, "db")
	assert.Error(t, err)
	got, err := st.Get(ctx, stray)
	require.NoError(t, err, "a key that names no staged version is not deleted")
	assert.Equal(t, []byte("x"), got)
}

var errStoreFault = errors.New("store fault")

// readDeleteFaultStore fails Get or Delete while the matching flag is set.
type readDeleteFaultStore struct {
	Store
	failGet, failDelete atomic.Bool
}

func (s *readDeleteFaultStore) Get(ctx context.Context, key string) ([]byte, error) {
	if s.failGet.Load() {
		return nil, errStoreFault
	}
	return s.Store.Get(ctx, key)
}

func (s *readDeleteFaultStore) Delete(ctx context.Context, key string) error {
	if s.failDelete.Load() {
		return errStoreFault
	}
	return s.Store.Delete(ctx, key)
}

func TestCoverage_ClearLayoutMarkerStoreFaults(t *testing.T) {
	ctx := context.Background()
	const source = "src-1"
	marker := LayoutMovedPrefix + source + "/db"

	listing := &listFaultStore{Store: newHomeStore(t)}
	listing.fail.Store(true)
	assert.ErrorIs(t, clearLayoutMarker(ctx, listing, source, "db"), errListFault)

	st := &readDeleteFaultStore{Store: newHomeStore(t)}
	require.NoError(t, st.Put(ctx, layoutFence("db"), []byte(source)))
	require.NoError(t, st.Put(ctx, marker, []byte("x")))

	st.failGet.Store(true)
	assert.ErrorIs(t, clearLayoutMarker(ctx, st, source, "db"), errStoreFault)
	st.failGet.Store(false)

	st.failDelete.Store(true)
	assert.ErrorIs(t, clearLayoutMarker(ctx, st, source, "db"), errStoreFault)
	st.failDelete.Store(false)
	held, err := holdsKey(ctx, st, layoutFence("db"))
	require.NoError(t, err)
	assert.True(t, held, "a failed delete leaves the fence")

	require.NoError(t, clearLayoutMarker(ctx, st, source, "db"))
	for _, k := range []string{layoutFence("db"), marker} {
		held, err := holdsKey(ctx, st, k)
		require.NoError(t, err)
		assert.False(t, held, k)
	}
}
