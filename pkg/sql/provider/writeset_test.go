//go:build qrdb_sql

// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// newBoundWriteSet creates a WriteSet already bound to a fresh memStore,
// ready for Put/PutUnique/Delete/Lookup/RangeOverlay tests.
func newBoundWriteSet(t *testing.T) (*WriteSet, *memStore) {
	t.Helper()
	store := newMemStore()
	txn := &memTxn{store: store}
	ws := newWriteSet(false, IsolationWritten)
	require.NoError(t, ws.bind(context.Background(), "t1", "db1", txn, store))
	return ws, store
}

func TestWriteSet_Bind_Idempotent(t *testing.T) {
	ws, store := newBoundWriteSet(t)
	txn := &memTxn{store: store}
	require.NoError(t, ws.bind(context.Background(), "t1", "db1", txn, store))
	assert.True(t, ws.Bound())
}

func TestWriteSet_Bind_CrossDatabaseRejected(t *testing.T) {
	ws, store := newBoundWriteSet(t)
	txn := &memTxn{store: store}
	err := ws.bind(context.Background(), "t1", "db2", txn, store)
	var xdb *CrossDatabaseTransactionError
	require.ErrorAs(t, err, &xdb)
}

func TestWriteSet_Bind_PropagatesCurrentRevisionError(t *testing.T) {
	ws := newWriteSet(false, IsolationWritten)
	boom := errors.New("boom")
	mvcc := &errMVCC{revErr: boom}
	err := ws.bind(context.Background(), "t1", "db1", nil, mvcc)
	require.ErrorIs(t, err, boom)
	assert.False(t, ws.Bound())
}

// errMVCC is a minimal service.MVCCKVServicer stand-in whose
// CurrentRevision/GetAtRevision/Range return configured errors, for
// exercising WriteSet's error paths independent of memStore's happy path.
type errMVCC struct {
	service.MVCCKVServicer
	revErr   error
	getErr   error
	rangeErr error
}

func (e *errMVCC) CurrentRevision(context.Context) (int64, error) { return 0, e.revErr }

func (e *errMVCC) GetAtRevision(context.Context, string, int64) (*service.KeyValue, error) {
	return nil, e.getErr
}

func (e *errMVCC) Range(context.Context, []byte, []byte, ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
	return nil, 0, 0, e.rangeErr
}

func TestWriteSet_Lookup_UnboundReturnsNotFound(t *testing.T) {
	ws := newWriteSet(false, IsolationWritten)
	v, found, err := ws.Lookup(context.Background(), "k1")
	require.NoError(t, err)
	assert.False(t, found)
	assert.Nil(t, v)
}

func TestWriteSet_Lookup_BufferBeforeSnapshot(t *testing.T) {
	store := newMemStore()
	require.NoError(t, store.Put(context.Background(), "k1", []byte("snapshot")))
	ws := newWriteSet(false, IsolationWritten)
	require.NoError(t, ws.bind(context.Background(), "t1", "db1", &memTxn{store: store}, store))

	require.NoError(t, ws.Put(context.Background(), "k1", []byte("buffered")))
	v, found, err := ws.Lookup(context.Background(), "k1")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, []byte("buffered"), v, "a buffered write must shadow the pinned snapshot")
}

func TestWriteSet_Lookup_TombstoneHidesSnapshotValue(t *testing.T) {
	store := newMemStore()
	require.NoError(t, store.Put(context.Background(), "k1", []byte("v1")))
	ws := newWriteSet(false, IsolationWritten)
	require.NoError(t, ws.bind(context.Background(), "t1", "db1", &memTxn{store: store}, store))

	require.NoError(t, ws.Delete(context.Background(), "k1"))
	_, found, err := ws.Lookup(context.Background(), "k1")
	require.NoError(t, err)
	assert.False(t, found)
}

func TestWriteSet_Get_NotFound(t *testing.T) {
	ws, _ := newBoundWriteSet(t)
	_, err := ws.Get(context.Background(), "missing")
	var nf *service.KeyNotFoundError
	assert.ErrorAs(t, err, &nf)
}

func TestWriteSet_Get_Found(t *testing.T) {
	ws, _ := newBoundWriteSet(t)
	require.NoError(t, ws.Put(context.Background(), "k1", []byte("v1")))
	v, err := ws.Get(context.Background(), "k1")
	require.NoError(t, err)
	assert.Equal(t, []byte("v1"), v)
}

func TestWriteSet_PutUnique_AlwaysAbsentPrecondition(t *testing.T) {
	store := newMemStore()
	// Pre-existing key at bind time: a plain Put would derive a
	// ModRevision precondition; PutUnique must still derive Version==0.
	require.NoError(t, store.Put(context.Background(), "k1", []byte("existing")))
	ws := newWriteSet(false, IsolationWritten)
	require.NoError(t, ws.bind(context.Background(), "t1", "db1", &memTxn{store: store}, store))

	require.NoError(t, ws.PutUnique(context.Background(), "k1", []byte("new")))
	cmp := ws.Compares()[0]
	assert.Equal(t, service.CompareVersion, cmp.Target)
	assert.EqualValues(t, 0, cmp.Version)
}

func TestWriteSet_Put_DerivesAbsencePrecondition(t *testing.T) {
	ws, _ := newBoundWriteSet(t)
	require.NoError(t, ws.Put(context.Background(), "new-key", []byte("v")))
	cmp := ws.Compares()[0]
	assert.Equal(t, service.CompareVersion, cmp.Target)
	assert.EqualValues(t, 0, cmp.Version)
}

func TestWriteSet_Put_DerivesModRevisionPrecondition(t *testing.T) {
	store := newMemStore()
	require.NoError(t, store.Put(context.Background(), "k1", []byte("v0")))
	rev, err := store.GetRevision(context.Background(), "k1")
	require.NoError(t, err)

	ws := newWriteSet(false, IsolationWritten)
	require.NoError(t, ws.bind(context.Background(), "t1", "db1", &memTxn{store: store}, store))
	require.NoError(t, ws.Put(context.Background(), "k1", []byte("v1")))

	cmp := ws.Compares()[0]
	assert.Equal(t, service.CompareModRevision, cmp.Target)
	assert.Equal(t, rev.ModRevision, cmp.ModRevision)
}

func TestWriteSet_SecondTouch_PreservesFirstTouchPrecondition(t *testing.T) {
	ws, _ := newBoundWriteSet(t)
	require.NoError(t, ws.Put(context.Background(), "k1", []byte("v1")))
	firstCmp := ws.Compares()[0]

	require.NoError(t, ws.Put(context.Background(), "k1", []byte("v2")))
	require.Len(t, ws.Compares(), 1, "a second touch to the same key must not add a second compare")
	assert.Equal(t, firstCmp, ws.Compares()[0])

	v, found, err := ws.Lookup(context.Background(), "k1")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, []byte("v2"), v, "the buffered value itself must reflect the latest touch")
}

func TestWriteSet_Delete_ThenPutUnique_ReinsertWithinTx(t *testing.T) {
	store := newMemStore()
	require.NoError(t, store.Put(context.Background(), "k1", []byte("v0")))
	ws := newWriteSet(false, IsolationWritten)
	require.NoError(t, ws.bind(context.Background(), "t1", "db1", &memTxn{store: store}, store))

	require.NoError(t, ws.Delete(context.Background(), "k1"))
	require.NoError(t, ws.PutUnique(context.Background(), "k1", []byte("v2")))

	v, found, err := ws.Lookup(context.Background(), "k1")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, []byte("v2"), v)

	ops := ws.Ops()
	require.Len(t, ops, 1)
	assert.Equal(t, service.OpPut, ops[0].Type)
}

func TestWriteSet_RangeOverlay_MergesSnapshotAndBuffer(t *testing.T) {
	store := newMemStore()
	require.NoError(t, store.Put(context.Background(), "p/a", []byte("va")))
	require.NoError(t, store.Put(context.Background(), "p/b", []byte("vb")))
	ws := newWriteSet(false, IsolationWritten)
	require.NoError(t, ws.bind(context.Background(), "t1", "db1", &memTxn{store: store}, store))

	require.NoError(t, ws.Put(context.Background(), "p/c", []byte("vc")))  // buffered add
	require.NoError(t, ws.Delete(context.Background(), "p/a"))             // buffered delete
	require.NoError(t, ws.Put(context.Background(), "p/b", []byte("vb2"))) // buffered overwrite

	out, err := ws.RangeOverlay(context.Background(), "p/")
	require.NoError(t, err)
	assert.Equal(t, map[string][]byte{
		"p/b": []byte("vb2"),
		"p/c": []byte("vc"),
	}, out)
}

func TestWriteSet_RangeOverlay_Unbound(t *testing.T) {
	ws := newWriteSet(false, IsolationWritten)
	out, err := ws.RangeOverlay(context.Background(), "p/")
	require.NoError(t, err)
	assert.Empty(t, out)
}

func TestWriteSet_ReadOnly_RejectsWrites(t *testing.T) {
	store := newMemStore()
	ws := newWriteSet(true, IsolationWritten)
	require.NoError(t, ws.bind(context.Background(), "t1", "db1", &memTxn{store: store}, store))

	err := ws.Put(context.Background(), "k1", []byte("v1"))
	var ro *ReadOnlyTransactionError
	assert.ErrorAs(t, err, &ro)

	err = ws.Delete(context.Background(), "k1")
	assert.ErrorAs(t, err, &ro)

	err = ws.PutUnique(context.Background(), "k1", []byte("v1"))
	assert.ErrorAs(t, err, &ro)
}

func TestWriteSet_Commit_Empty(t *testing.T) {
	ws := newWriteSet(false, IsolationWritten)
	resp, err := ws.commit(context.Background())
	require.NoError(t, err)
	assert.True(t, resp.Succeeded)
}

func TestWriteSet_Commit_BoundNoWrites(t *testing.T) {
	ws, _ := newBoundWriteSet(t)
	resp, err := ws.commit(context.Background())
	require.NoError(t, err)
	assert.True(t, resp.Succeeded)
}

func TestWriteSet_Commit_AppliesBufferedWrites(t *testing.T) {
	ws, store := newBoundWriteSet(t)
	require.NoError(t, ws.Put(context.Background(), "k1", []byte("v1")))
	require.NoError(t, ws.PutUnique(context.Background(), "k2", []byte("v2")))

	resp, err := ws.commit(context.Background())
	require.NoError(t, err)
	assert.True(t, resp.Succeeded)

	v1, err := store.Get(context.Background(), "k1")
	require.NoError(t, err)
	assert.Equal(t, []byte("v1"), v1)
	v2, err := store.Get(context.Background(), "k2")
	require.NoError(t, err)
	assert.Equal(t, []byte("v2"), v2)
}

func TestWriteSet_Commit_ConflictDoesNotApply(t *testing.T) {
	ws, store := newBoundWriteSet(t)
	require.NoError(t, ws.Put(context.Background(), "k1", []byte("v1")))

	// A concurrent writer commits over the same key after our snapshot was
	// pinned, so our buffered Put's ModRevision precondition (absence, in
	// this case) no longer holds.
	require.NoError(t, store.Put(context.Background(), "k1", []byte("concurrent")))

	resp, err := ws.commit(context.Background())
	require.NoError(t, err)
	assert.False(t, resp.Succeeded)

	v, err := store.Get(context.Background(), "k1")
	require.NoError(t, err)
	assert.Equal(t, []byte("concurrent"), v, "a losing commit must never apply its buffered writes")
}

// ---------------------------------------------------------------------------
// Savepoints
// ---------------------------------------------------------------------------

func TestWriteSet_Savepoint_MarkAndRollback(t *testing.T) {
	ws, _ := newBoundWriteSet(t)
	require.NoError(t, ws.Put(context.Background(), "k1", []byte("v1")))
	ws.Mark("sp1")
	require.NoError(t, ws.Put(context.Background(), "k2", []byte("v2")))
	require.NoError(t, ws.Put(context.Background(), "k1", []byte("v1-updated")))

	require.NoError(t, ws.RollbackTo("sp1"))

	_, found, err := ws.Lookup(context.Background(), "k2")
	require.NoError(t, err)
	assert.False(t, found, "a key created after the savepoint must be gone")

	v1, found, err := ws.Lookup(context.Background(), "k1")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, []byte("v1"), v1, "a key modified after the savepoint must revert to its pre-mark value")

	assert.Len(t, ws.order, 1)
	assert.Equal(t, []string{"k1"}, ws.order)
}

func TestWriteSet_Savepoint_RollbackRemovesLaterSavepoints(t *testing.T) {
	ws, _ := newBoundWriteSet(t)
	require.NoError(t, ws.Put(context.Background(), "k1", []byte("v1")))
	ws.Mark("sp1")
	require.NoError(t, ws.Put(context.Background(), "k2", []byte("v2")))
	ws.Mark("sp2")
	require.NoError(t, ws.Put(context.Background(), "k3", []byte("v3")))

	require.NoError(t, ws.RollbackTo("sp1"))

	err := ws.RollbackTo("sp2")
	var notFound *SavepointNotFoundError
	assert.ErrorAs(t, err, &notFound, "a savepoint marked after the rollback target must itself be discarded")
}

func TestWriteSet_Savepoint_RollbackTwiceToSameMark(t *testing.T) {
	ws, _ := newBoundWriteSet(t)
	require.NoError(t, ws.Put(context.Background(), "k1", []byte("v1")))
	ws.Mark("sp1")
	require.NoError(t, ws.Put(context.Background(), "k2", []byte("v2")))

	require.NoError(t, ws.RollbackTo("sp1"))
	require.NoError(t, ws.Put(context.Background(), "k3", []byte("v3")))
	require.NoError(t, ws.RollbackTo("sp1"), "a savepoint must remain valid for a second rollback")

	_, found, _ := ws.Lookup(context.Background(), "k3")
	assert.False(t, found)
}

func TestWriteSet_Savepoint_Release(t *testing.T) {
	ws, _ := newBoundWriteSet(t)
	ws.Mark("sp1")
	require.NoError(t, ws.Release("sp1"))

	err := ws.Release("sp1")
	var notFound *SavepointNotFoundError
	assert.ErrorAs(t, err, &notFound)

	err = ws.RollbackTo("sp1")
	assert.ErrorAs(t, err, &notFound)
}

func TestWriteSet_Savepoint_NotFound(t *testing.T) {
	ws, _ := newBoundWriteSet(t)
	err := ws.RollbackTo("nope")
	var notFound *SavepointNotFoundError
	assert.ErrorAs(t, err, &notFound)
}

// ---------------------------------------------------------------------------
// Limits
// ---------------------------------------------------------------------------

func TestWriteSet_ByteLimit_Enforced(t *testing.T) {
	ws, _ := newBoundWriteSet(t)
	huge := make([]byte, writeSetMaxBytes)

	err := ws.Put(context.Background(), "k1", huge)
	var limitErr *WriteSetLimitError
	require.ErrorAs(t, err, &limitErr)
	assert.Equal(t, "byte size", limitErr.Reason)
}

func TestWriteSet_OpCountLimit_Enforced(t *testing.T) {
	ws, _ := newBoundWriteSet(t)
	ws.entries = make(map[string]*wsEntry, writeSetMaxOps)
	for i := 0; i < writeSetMaxOps; i++ {
		ws.entries[string(rune(i))] = &wsEntry{}
		ws.order = append(ws.order, string(rune(i)))
	}

	err := ws.Put(context.Background(), "one-too-many", []byte("v"))
	var limitErr *WriteSetLimitError
	require.ErrorAs(t, err, &limitErr)
	assert.Equal(t, "operation count", limitErr.Reason)
}

// ---------------------------------------------------------------------------
// Snapshot expiry
// ---------------------------------------------------------------------------

func TestWriteSet_BaselineCompare_TranslatesRevisionCompacted(t *testing.T) {
	compactedErr := &service.RevisionCompactedError{Requested: 5, Compacted: 10}
	mvcc := &errMVCC{getErr: compactedErr}
	ws := newWriteSet(false, IsolationWritten)
	require.NoError(t, ws.bind(context.Background(), "t1", "db1", nil, &errMVCC{revErr: nil}))
	ws.mvccSvc = mvcc // swap in the erroring MVCC after a successful bind

	err := ws.Put(context.Background(), "k1", []byte("v1"))
	var expired *TransactionSnapshotExpiredError
	require.ErrorAs(t, err, &expired)
	assert.EqualValues(t, 5, expired.Requested)
	assert.EqualValues(t, 10, expired.Compacted)
}

func TestWriteSet_RangeOverlay_TranslatesRevisionCompacted(t *testing.T) {
	compactedErr := &service.RevisionCompactedError{Requested: 5, Compacted: 10}
	ws := newWriteSet(false, IsolationWritten)
	require.NoError(t, ws.bind(context.Background(), "t1", "db1", nil, &errMVCC{revErr: nil}))
	ws.mvccSvc = &errMVCC{rangeErr: compactedErr}

	_, err := ws.RangeOverlay(context.Background(), "p/")
	var expired *TransactionSnapshotExpiredError
	require.ErrorAs(t, err, &expired)
}

func TestWriteSet_Lookup_TranslatesRevisionCompacted(t *testing.T) {
	compactedErr := &service.RevisionCompactedError{Requested: 5, Compacted: 10}
	ws := newWriteSet(false, IsolationWritten)
	require.NoError(t, ws.bind(context.Background(), "t1", "db1", nil, &errMVCC{revErr: nil}))
	ws.mvccSvc = &errMVCC{getErr: compactedErr}

	_, _, err := ws.Lookup(context.Background(), "k1")
	var expired *TransactionSnapshotExpiredError
	require.ErrorAs(t, err, &expired)
}

func TestWriteSet_BaselineCompare_PropagatesOtherErrors(t *testing.T) {
	boom := errors.New("boom")
	ws := newWriteSet(false, IsolationWritten)
	require.NoError(t, ws.bind(context.Background(), "t1", "db1", nil, &errMVCC{revErr: nil}))
	ws.mvccSvc = &errMVCC{getErr: boom}

	err := ws.Put(context.Background(), "k1", []byte("v1"))
	assert.ErrorIs(t, err, boom)
}

// ---------------------------------------------------------------------------
// Snapshot absence
//
// A real MVCC store reports a key that has no version at or before the pinned
// revision as *service.RevisionNotFoundError, not *service.KeyNotFoundError.
// Both mean "absent as of the snapshot" and both must be treated as such:
// classifying only the latter made every interactive transaction fail on its
// first read of a not-yet-existing key, while passing against test doubles
// that return KeyNotFoundError for the same case.
// ---------------------------------------------------------------------------

func TestWriteSet_Lookup_RevisionNotFoundIsAbsence(t *testing.T) {
	ws := newWriteSet(false, IsolationWritten)
	require.NoError(t, ws.bind(context.Background(), "t1", "db1", nil, &errMVCC{}))
	ws.mvccSvc = &errMVCC{getErr: &service.RevisionNotFoundError{Revision: 7}}

	v, found, err := ws.Lookup(context.Background(), "k1")
	require.NoError(t, err)
	assert.False(t, found)
	assert.Nil(t, v)
}

func TestWriteSet_Get_RevisionNotFoundIsKeyNotFound(t *testing.T) {
	ws := newWriteSet(false, IsolationWritten)
	require.NoError(t, ws.bind(context.Background(), "t1", "db1", nil, &errMVCC{}))
	ws.mvccSvc = &errMVCC{getErr: &service.RevisionNotFoundError{Revision: 7}}

	_, err := ws.Get(context.Background(), "k1")
	var notFound *service.KeyNotFoundError
	require.ErrorAs(t, err, &notFound)
}

func TestWriteSet_BaselineCompare_RevisionNotFoundYieldsAbsencePrecondition(t *testing.T) {
	ws := newWriteSet(false, IsolationWritten)
	require.NoError(t, ws.bind(context.Background(), "t1", "db1", nil, &errMVCC{}))
	ws.mvccSvc = &errMVCC{getErr: &service.RevisionNotFoundError{Revision: 7}}

	require.NoError(t, ws.Put(context.Background(), "k1", []byte("v1")))
	cmp := ws.entries["k1"].compare
	assert.Equal(t, service.CompareVersion, cmp.Target)
	assert.EqualValues(t, 0, cmp.Version)
}

// TestWriteSet_GenerationAt_AbsentAtTheSnapshotIsGenerationZero: a table
// never truncated has no generation key, which a real MVCC store reports at
// the snapshot revision as *service.RevisionNotFoundError. That is
// generation 0, not a failure -- every table's first touch in an
// interactive transaction reads it.
func TestWriteSet_GenerationAt_AbsentAtTheSnapshotIsGenerationZero(t *testing.T) {
	for name, absent := range map[string]error{
		"no version at revision": &service.RevisionNotFoundError{Revision: 7},
		"key never written":      &service.KeyNotFoundError{Key: "g"},
	} {
		t.Run(name, func(t *testing.T) {
			ws := newWriteSet(false, IsolationWritten)
			require.NoError(t, ws.bind(context.Background(), "t1", "db1", nil, &errMVCC{}))
			ws.mvccSvc = &errMVCC{getErr: absent}

			generation, err := ws.generationAt(context.Background(), "g")
			require.NoError(t, err)
			assert.Zero(t, generation)
		})
	}
}

// TestWriteSet_GenerationAt_CompactedSnapshotIsAnError: a snapshot the store
// no longer holds is a genuine failure.
func TestWriteSet_GenerationAt_CompactedSnapshotIsAnError(t *testing.T) {
	ws := newWriteSet(false, IsolationWritten)
	require.NoError(t, ws.bind(context.Background(), "t1", "db1", nil, &errMVCC{}))
	ws.mvccSvc = &errMVCC{getErr: &service.RevisionCompactedError{Requested: 3, Compacted: 9}}

	_, err := ws.generationAt(context.Background(), "g")
	var compacted *service.RevisionCompactedError
	require.ErrorAs(t, err, &compacted)
}

func TestSnapshotAbsent_Classification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"key never written", &service.KeyNotFoundError{Key: "k"}, true},
		{"no version at revision", &service.RevisionNotFoundError{Revision: 3}, true},
		{"snapshot compacted away", &service.RevisionCompactedError{Requested: 3, Compacted: 9}, false},
		{"unrelated failure", errors.New("boom"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, snapshotAbsent(tt.err))
		})
	}
}
