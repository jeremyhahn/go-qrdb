package credentials

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// getFaultStore adds a Get fault to faultyStore: op "get" fails the
// first read of key.
type getFaultStore struct {
	*faultyStore
}

func (g getFaultStore) Get(ctx context.Context, key string) ([]byte, error) {
	if err := g.fail("get", key); err != nil {
		return nil, err
	}
	return g.Store.Get(ctx, key)
}

var _ Store = getFaultStore{}

// fixedSource names the earlier store so a test knows the marker keys.
const fixedSource = "src1"

func fixedMarker(name string) string {
	return LayoutMovedPrefix + fixedSource + "/" + name
}

// TestMoveLayout_StoreFaultsMoveNothingTwice drives a store failure into
// every read and write of the move. Each one fails the pass with the
// store's error, and a clean second pass moves the whole set exactly once
// and leaves neither the source records nor a marker behind.
func TestMoveLayout_StoreFaultsMoveNothingTwice(t *testing.T) {
	for _, tc := range []struct {
		name   string
		side   string // "from" or "to"
		op     string
		key    string
		marker bool // a leftover marker of a vanished set sits in to
	}{
		{name: "source identity list", side: "from", op: "list", key: LayoutSourceKey},
		{name: "source identity read", side: "from", op: "get", key: LayoutSourceKey},
		{name: "marker probe", side: "to", op: "list", key: fixedMarker("db")},
		{name: "destination record probe", side: "to", op: "list", key: credentialPrefix + "db"},
		{name: "destination version probe", side: "to", op: "list", key: VersionKeyPrefix + encodedName("db") + "/"},
		{name: "source record read", side: "from", op: "get", key: versionKey("db", 1)},
		{name: "source presence probe", side: "from", op: "list", key: stagedKey("db", 2)},
		{name: "marker list", side: "to", op: "list", key: LayoutMovedPrefix + fixedSource + "/"},
		{name: "leftover marker delete", side: "to", op: "delete", key: fixedMarker("gone"), marker: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			fromMem, toMem := seal.NewMemoryBackend(), seal.NewMemoryBackend()
			require.NoError(t, fromMem.Put(ctx, LayoutSourceKey, []byte(fixedSource)))
			keys := putLayout(t, fromMem, "db", "old")
			if tc.marker {
				require.NoError(t, toMem.Put(ctx, fixedMarker("gone"), []byte("earlier")))
			}
			var from, to Store = fromMem, toMem
			fault := &faultyStore{op: tc.op, key: tc.key}
			if tc.side == "from" {
				fault.Store = fromMem
				from = getFaultStore{fault}
			} else {
				fault.Store = toMem
				to = getFaultStore{fault}
			}

			require.ErrorIs(t, moveLayout(ctx, from, to), errInjected)
			require.True(t, fault.fired, "the fault was reached")

			require.NoError(t, moveLayout(ctx, from, to))
			requireMovedAndClean(t, from, to, "db", keys)
			keys, err := toMem.List(ctx, LayoutMovedPrefix)
			require.NoError(t, err)
			assert.Empty(t, keys, "no marker outlives the move")
		})
	}
}

func TestLayoutSourceID_RecordWriteFailureIsReported(t *testing.T) {
	ctx := context.Background()
	mem := seal.NewMemoryBackend()
	from := &faultyStore{Store: mem, op: "putifabsent", key: LayoutSourceKey}

	_, err := layoutSourceID(ctx, from)
	require.ErrorIs(t, err, errInjected)
	requireAbsent(t, mem, LayoutSourceKey)

	id, err := layoutSourceID(ctx, from)
	require.NoError(t, err)
	assert.Len(t, id, 32, "a fresh identity is 16 random bytes in hex")
}

func TestLayoutSourceID_EmptyIdentityRefused(t *testing.T) {
	ctx := context.Background()
	from, to := seal.NewMemoryBackend(), seal.NewMemoryBackend()
	require.NoError(t, from.Put(ctx, LayoutSourceKey, []byte{}))
	keys := putLayout(t, from, "db", "old")

	_, err := layoutSourceID(ctx, from)
	require.ErrorContains(t, err, "empty credential store identity")
	require.ErrorContains(t, moveLayout(ctx, from, to), "empty credential store identity")
	for _, key := range keys {
		requireValue(t, from, key, "old@"+key)
		requireAbsent(t, to, key)
	}
}

func TestLayoutSetOf_UnreadableSegmentMovesByItself(t *testing.T) {
	ctx := context.Background()
	from, to := seal.NewMemoryBackend(), seal.NewMemoryBackend()
	odd := VersionKeyPrefix + "!!not-base64/1"
	require.NoError(t, from.Put(ctx, odd, []byte("odd")))
	keys := putLayout(t, from, "db", "old")

	name, enc := layoutSetOf(VersionKeyPrefix, odd)
	assert.Equal(t, "!!not-base64", name)
	assert.Equal(t, "!!not-base64", enc)

	require.NoError(t, moveLayout(ctx, from, to))
	requireValue(t, to, odd, "odd")
	requireAbsent(t, from, odd)
	requireMovedAndClean(t, from, to, "db", keys)
}

func TestFinishLayoutSet_SkipsAKeyAlreadyGone(t *testing.T) {
	ctx := context.Background()
	from, to := seal.NewMemoryBackend(), seal.NewMemoryBackend()
	move := layoutMove{source: fixedSource, id: "m1"}
	require.NoError(t, from.Put(ctx, credentialPrefix+"db", []byte("old")))
	require.NoError(t, to.Put(ctx, move.marker("db"), []byte(move.id)))
	set := &layoutSet{name: "db", enc: encodedName("db"),
		keys: []string{MetaKeyPrefix + "db", credentialPrefix + "db"}}

	require.NoError(t, finishLayoutSet(ctx, from, to, move, set))
	requireAbsent(t, from, credentialPrefix+"db")
	requireAbsent(t, to, move.marker("db"))
}

// TestEnsureLayoutMoved_FailedMoveIsRetried: a store failure is not a
// conflict, so the service keeps the earlier store and the next call
// finishes the move.
func TestEnsureLayoutMoved_FailedMoveIsRetried(t *testing.T) {
	ctx := context.Background()
	first, second := seal.NewMemoryBackend(), seal.NewMemoryBackend()
	keys := putLayout(t, first, "db", "old")
	from := &faultyStore{Store: first, op: "list", key: LayoutSourceKey}
	svc := newUnboundService(t)

	require.NoError(t, svc.BindStoreFrom(ctx, second, from), "a failed move does not fail the bind")
	require.True(t, from.fired)
	svc.layoutMu.Lock()
	pending := svc.layoutFrom
	svc.layoutMu.Unlock()
	require.NotNil(t, pending, "the earlier store is kept for a retry")

	require.NoError(t, svc.ensureLayoutMoved(ctx))
	requireMovedAndClean(t, first, second, "db", keys)
	svc.layoutMu.Lock()
	assert.Nil(t, svc.layoutFrom)
	svc.layoutMu.Unlock()
}

func TestEnsureLayoutMoved_ReportsTheAttemptDeadline(t *testing.T) {
	first, second := seal.NewMemoryBackend(), seal.NewMemoryBackend()
	putLayout(t, first, "db", "old")
	svc := newUnboundService(t)
	require.NoError(t, svc.BindStore(context.Background(), second))
	svc.rotationAttemptTimeout = time.Hour
	svc.layoutMu.Lock()
	svc.layoutFrom = &faultyStore{Store: first, op: "list", key: LayoutSourceKey}
	svc.layoutMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := svc.ensureLayoutMoved(ctx)
	var opErr *OperationError
	require.ErrorAs(t, err, &opErr)
	assert.Equal(t, "move credential store", opErr.Op)
	assert.ErrorIs(t, err, errInjected)
	assert.True(t, errors.Is(err, context.Canceled), "the expired attempt is named with the store error")
}

// TestEnsureLayoutMoved_UnboundStoreKeepsTheSource: with no store bound
// there is nowhere to move to, so the call fails and the earlier store is
// kept for the next attempt.
func TestEnsureLayoutMoved_UnboundStoreKeepsTheSource(t *testing.T) {
	first := seal.NewMemoryBackend()
	keys := putLayout(t, first, "db", "old")
	svc := newUnboundService(t)
	svc.layoutMu.Lock()
	svc.layoutFrom = first
	svc.layoutMu.Unlock()

	require.Error(t, svc.ensureLayoutMoved(context.Background()))
	svc.layoutMu.Lock()
	assert.NotNil(t, svc.layoutFrom, "the earlier store is kept")
	svc.layoutMu.Unlock()
	for _, key := range keys {
		requireValue(t, first, key, "old@"+key)
	}
}
