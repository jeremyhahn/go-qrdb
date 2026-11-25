package credentials

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
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
			assert.Empty(t, transientMoveRecords(t, toMem), "no marker outlives the move")
		})
	}
}

// batchHookStore runs before once, ahead of the first guarded batch: the
// window between a move's destination probe and its batch.
type batchHookStore struct {
	Store
	before func()
}

func (s *batchHookStore) PutAllIfAllUnchanged(ctx context.Context, guards []kvstore.Guard, writes []kvstore.KV) (bool, error) {
	if f := s.before; f != nil {
		s.before = nil
		f()
	}
	return s.Store.PutAllIfAllUnchanged(ctx, guards, writes)
}

// TestMoveLayout_OtherSourceCannotRestoreADeletedCredential: source A's move
// of db commits and its source delete fails, leaving A's marker; db is then
// deleted from the destination. Source B's copy of db must not come back,
// whether B probes the destination after all that or just before it.
func TestMoveLayout_OtherSourceCannotRestoreADeletedCredential(t *testing.T) {
	for _, race := range []bool{false, true} {
		name := "after"
		if race {
			name = "between probe and batch"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			memA, memB, toMem := seal.NewMemoryBackend(), seal.NewMemoryBackend(), seal.NewMemoryBackend()
			keysA := putLayout(t, memA, "db", "old")
			keysB := putLayout(t, memB, "db", "other")
			fromA := &faultyStore{Store: memA, op: "delete", key: credentialPrefix + "db"}
			interruptAndDelete := func() {
				require.ErrorIs(t, moveLayout(ctx, fromA, toMem), errInjected)
				requireMarked(t, toMem, layoutFence("db"))
				for _, k := range keysA {
					require.NoError(t, toMem.Delete(ctx, k))
				}
			}
			var to Store = toMem
			if race {
				to = &batchHookStore{Store: toMem, before: interruptAndDelete}
			} else {
				interruptAndDelete()
			}

			err := moveLayout(ctx, memB, to)
			var conflict *LayoutConflictError
			require.ErrorAs(t, err, &conflict)
			assert.Equal(t, []string{"db"}, conflict.Names)
			for _, k := range keysB {
				requireAbsent(t, toMem, k)
				requireValue(t, memB, k, "other@"+k)
			}

			// A's next pass only finishes: nothing comes back, and its
			// marker and the fence go.
			require.NoError(t, moveLayout(ctx, fromA, toMem))
			for _, k := range keysA {
				requireAbsent(t, memA, k)
				requireAbsent(t, toMem, k)
			}
			// B's conflict stands while its copy does, so a later
			// pass of B does not move it either.
			srcB, err := layoutSourceID(ctx, memB)
			require.NoError(t, err)
			left := transientMoveRecords(t, toMem)
			assert.Equal(t, []string{layoutConflictRoot(srcB) + "db"}, left, "no marker or fence outlives A's move")
			require.ErrorAs(t, moveLayout(ctx, memB, toMem), &conflict)
			for _, k := range keysB {
				requireAbsent(t, toMem, k)
			}
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

// moveBatchCounter counts the guarded move batches (those writing a move
// marker) that reach the wrapped store.
type moveBatchCounter struct {
	Store
	batches atomic.Int64
}

func (m *moveBatchCounter) PutAllIfAllUnchanged(ctx context.Context, guards []kvstore.Guard, writes []kvstore.KV) (bool, error) {
	for _, w := range writes {
		if strings.HasPrefix(w.Key, LayoutMovedPrefix) {
			m.batches.Add(1)
			break
		}
	}
	return m.Store.PutAllIfAllUnchanged(ctx, guards, writes)
}

// TestEnsureLayoutMoved_CutAfterBatchFinishesOnNextOperation: a store move
// that stops after the destination batch applied and before the source
// delete is finished by the service's next operation, which copies nothing
// a second time and deletes the earlier store's records.
func TestEnsureLayoutMoved_CutAfterBatchFinishesOnNextOperation(t *testing.T) {
	ctx := context.Background()
	first, second := seal.NewMemoryBackend(), seal.NewMemoryBackend()
	svc := newUnboundService(t)
	require.NoError(t, svc.BindStore(ctx, first))
	require.NoError(t, svc.StoreCredential(ctx, "db", []byte("v1")))

	from := &faultyStore{Store: first, op: "delete", key: credentialPrefix + "db"}
	to := &moveBatchCounter{Store: second}
	require.NoError(t, svc.BindStoreFrom(ctx, to, from), "a failed move does not fail the bind")
	require.True(t, from.fired, "the move stopped at the source delete")
	require.Equal(t, int64(1), to.batches.Load(), "the destination batch applied once")
	held, err := holdsKey(ctx, first, credentialPrefix+"db")
	require.NoError(t, err)
	require.True(t, held, "the source record outlives the cut")

	assert.Equal(t, "v1", retrieveString(t, svc, "db"), "the next operation serves the moved value")
	assert.Equal(t, int64(1), to.batches.Load(), "the finishing pass copies nothing twice")
	for _, prefix := range layoutPrefixes {
		left, err := first.List(ctx, prefix)
		require.NoError(t, err)
		assert.Empty(t, left, "the next operation deletes the earlier store's records")
	}
	assert.Empty(t, transientMoveRecords(t, second), "no marker outlives the finished move")
	svc.layoutMu.Lock()
	assert.Nil(t, svc.layoutFrom, "no move stays pending")
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
