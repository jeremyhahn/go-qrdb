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
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/dolthub/vitess/go/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// IsolationLevel itself
// ---------------------------------------------------------------------------

func TestIsolationLevel_String(t *testing.T) {
	tests := []struct {
		level IsolationLevel
		want  string
	}{
		{IsolationNone, "none"},
		{IsolationWritten, "written"},
		{IsolationWrittenReads, "written+reads"},
		{IsolationLevel(200), "IsolationLevel(200)"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.level.String())
		})
	}
}

// TestIsolationLevel_ZeroValueIsTheDefault pins the property the whole
// "changing nothing changes nothing" guarantee rests on: an IsolationLevel
// nobody set means the default, not the weakest level.
func TestIsolationLevel_ZeroValueIsTheDefault(t *testing.T) {
	var zero IsolationLevel
	assert.Equal(t, IsolationWritten, zero)
	assert.Equal(t, "written", zero.String())

	var ws WriteSet
	assert.Equal(t, IsolationWritten, ws.Isolation(), "a WriteSet built without a level must behave as before the level existed")
}

func TestIsolationLevels_NamesAndOrderAgree(t *testing.T) {
	require.Len(t, isolationLevelOrder, len(isolationLevelNames), "every level must be registrable as an enum value")
	require.Len(t, isolationLevels, len(isolationLevelNames), "level spellings must be unique")

	for _, level := range isolationLevelOrder {
		name, ok := isolationLevelNames[level]
		require.True(t, ok, "level %d has no spelling", level)
		assert.Equal(t, level, isolationLevels[name], "spelling %q must round-trip", name)
	}
}

// ---------------------------------------------------------------------------
// Compare sets, asserted directly on Compares()
// ---------------------------------------------------------------------------

// newIsolatedWriteSet creates a WriteSet at level, bound to a fresh memStore
// pre-loaded with pairs.
func newIsolatedWriteSet(t *testing.T, level IsolationLevel, pairs map[string]string) (*WriteSet, *memStore) {
	t.Helper()
	store := newMemStore()
	for k, v := range pairs {
		require.NoError(t, store.Put(context.Background(), k, []byte(v)))
	}
	ws := newWriteSet(false, level)
	require.NoError(t, ws.bind(context.Background(), "t1", "db1", &memTxn{store: store}, store))
	return ws, store
}

// comparedKeys returns the keys ws would attach a precondition to at commit,
// in compare order.
func comparedKeys(ws *WriteSet) []string {
	compares := ws.Compares()
	out := make([]string, 0, len(compares))
	for _, c := range compares {
		out = append(out, string(c.Key))
	}
	return out
}

// readThenWrite drives the shape every level is judged on: the transaction
// reads read by exact key, then writes write.
func readThenWrite(t *testing.T, ws *WriteSet, read, write string) {
	t.Helper()
	_, _, err := ws.Lookup(context.Background(), read)
	require.NoError(t, err)
	require.NoError(t, ws.Put(context.Background(), write, []byte("written")))
}

func TestWriteSet_Compares_None_IsEmpty(t *testing.T) {
	ws, _ := newIsolatedWriteSet(t, IsolationNone, map[string]string{"read-me": "v"})
	readThenWrite(t, ws, "read-me", "write-me")

	assert.Empty(t, ws.Compares(), "IsolationNone attaches no preconditions at all")
	assert.Len(t, ws.Ops(), 1, "but it still proposes the write")
}

func TestWriteSet_Compares_Written_CoversWritesOnly(t *testing.T) {
	ws, _ := newIsolatedWriteSet(t, IsolationWritten, map[string]string{"read-me": "v"})
	readThenWrite(t, ws, "read-me", "write-me")

	assert.Equal(t, []string{"write-me"}, comparedKeys(ws), "the default must compare the written key and nothing else")
}

func TestWriteSet_Compares_WrittenReads_CoversWritesThenPointReads(t *testing.T) {
	ws, store := newIsolatedWriteSet(t, IsolationWrittenReads, map[string]string{"read-me": "v"})
	readThenWrite(t, ws, "read-me", "write-me")

	assert.Equal(t, []string{"write-me", "read-me"}, comparedKeys(ws), "written keys first, then point-read keys")

	rev, err := store.GetRevision(context.Background(), "read-me")
	require.NoError(t, err)
	readCmp := ws.Compares()[1]
	assert.Equal(t, service.CompareModRevision, readCmp.Target)
	assert.Equal(t, rev.ModRevision, readCmp.ModRevision, "a read key is pinned to the revision the read observed")
}

func TestWriteSet_Compares_WrittenReads_AbsentReadPinsAbsence(t *testing.T) {
	ws, _ := newIsolatedWriteSet(t, IsolationWrittenReads, nil)

	_, found, err := ws.Lookup(context.Background(), "never-existed")
	require.NoError(t, err)
	require.False(t, found)

	compares := ws.Compares()
	require.Len(t, compares, 1, "reading a missing key still establishes what the transaction observed")
	assert.Equal(t, service.CompareVersion, compares[0].Target)
	assert.EqualValues(t, 0, compares[0].Version, "\"it was not there\" is a read result that must not silently change")
}

func TestWriteSet_Compares_WrittenReads_ReadThenWrittenKeyComparedOnce(t *testing.T) {
	ws, _ := newIsolatedWriteSet(t, IsolationWrittenReads, map[string]string{"k1": "v"})

	_, _, err := ws.Lookup(context.Background(), "k1")
	require.NoError(t, err)
	require.NoError(t, ws.Put(context.Background(), "k1", []byte("v2")))

	compares := ws.Compares()
	require.Len(t, compares, 1, "a key that was read and then written carries one precondition, not two")
	assert.Equal(t, service.CompareModRevision, compares[0].Target)
}

// TestWriteSet_Compares_WrittenReads_UniqueWriteBeatsEarlierRead guards the
// one case where the read and write preconditions for a key genuinely
// differ: PutUnique demands absence unconditionally, and that demand -- the
// one enforcing PRIMARY/UNIQUE -- is the one that must survive.
func TestWriteSet_Compares_WrittenReads_UniqueWriteBeatsEarlierRead(t *testing.T) {
	ws, _ := newIsolatedWriteSet(t, IsolationWrittenReads, map[string]string{"k1": "v"})

	_, _, err := ws.Lookup(context.Background(), "k1")
	require.NoError(t, err)
	require.NoError(t, ws.PutUnique(context.Background(), "k1", []byte("v2")))

	compares := ws.Compares()
	require.Len(t, compares, 1)
	assert.Equal(t, service.CompareVersion, compares[0].Target)
	assert.EqualValues(t, 0, compares[0].Version)
}

func TestWriteSet_Compares_WrittenReads_BufferHitRecordsNoRead(t *testing.T) {
	ws, _ := newIsolatedWriteSet(t, IsolationWrittenReads, nil)

	require.NoError(t, ws.Put(context.Background(), "k1", []byte("v1")))
	_, _, err := ws.Lookup(context.Background(), "k1")
	require.NoError(t, err)

	assert.Empty(t, ws.readOrder, "read-your-own-writes never touches the snapshot, so there is nothing to pin")
	assert.Len(t, ws.Compares(), 1)
}

func TestWriteSet_Compares_WrittenReads_FirstReadWins(t *testing.T) {
	ws, store := newIsolatedWriteSet(t, IsolationWrittenReads, map[string]string{"k1": "v"})

	_, _, err := ws.Lookup(context.Background(), "k1")
	require.NoError(t, err)
	first := ws.Compares()[0]

	// Another transaction commits over k1; re-reading still answers from the
	// pinned snapshot, so the precondition must not move with it.
	require.NoError(t, store.Put(context.Background(), "k1", []byte("v2")))
	_, _, err = ws.Lookup(context.Background(), "k1")
	require.NoError(t, err)

	compares := ws.Compares()
	require.Len(t, compares, 1, "re-reading a key must not add a second precondition")
	assert.Equal(t, first, compares[0])
}

// TestWriteSet_Compares_UnknownLevelFallsBackToWritten covers the defensive
// branch in Compares: an IsolationLevel that is not one of this package's
// constants can only be constructed deliberately, and must never be the
// reason a transaction commits with weaker checking than the default.
func TestWriteSet_Compares_UnknownLevelFallsBackToWritten(t *testing.T) {
	ws, _ := newIsolatedWriteSet(t, IsolationLevel(200), nil)
	require.NoError(t, ws.Put(context.Background(), "k1", []byte("v1")))

	assert.Equal(t, []string{"k1"}, comparedKeys(ws))
}

// ---------------------------------------------------------------------------
// Range reads at written+reads
// ---------------------------------------------------------------------------

func TestWriteSet_RangeOverlay_WrittenReads_Refused(t *testing.T) {
	ws, _ := newIsolatedWriteSet(t, IsolationWrittenReads, map[string]string{"p/a": "va"})

	out, err := ws.RangeOverlay(context.Background(), "p/")
	assert.Nil(t, out, "a refused range read must not answer with a partial result")

	var rangeErr *IsolationRangeReadError
	require.ErrorAs(t, err, &rangeErr)
	assert.Equal(t, "written+reads", rangeErr.Level)
	assert.Equal(t, "p/", rangeErr.Prefix)
	assert.Contains(t, rangeErr.Error(), "qrdb_isolation=written+reads")
	assert.Contains(t, rangeErr.Error(), "read by exact key")
}

func TestWriteSet_RangeOverlay_OtherLevels_Allowed(t *testing.T) {
	for _, level := range []IsolationLevel{IsolationNone, IsolationWritten} {
		t.Run(level.String(), func(t *testing.T) {
			ws, _ := newIsolatedWriteSet(t, level, map[string]string{"p/a": "va"})

			out, err := ws.RangeOverlay(context.Background(), "p/")
			require.NoError(t, err)
			assert.Equal(t, map[string][]byte{"p/a": []byte("va")}, out)
			assert.Empty(t, ws.Compares(), "a scan is never a compare at any level")
		})
	}
}

// ---------------------------------------------------------------------------
// Read-set ceilings
// ---------------------------------------------------------------------------

func TestWriteSet_RecordRead_OpCountLimitEnforced(t *testing.T) {
	ws, _ := newIsolatedWriteSet(t, IsolationWrittenReads, nil)
	ws.readOrder = make([]string, writeSetMaxOps)

	err := ws.recordRead("one-too-many", absenceCompare("one-too-many"))
	var limitErr *WriteSetLimitError
	require.ErrorAs(t, err, &limitErr)
	assert.Equal(t, "operation count", limitErr.Reason)
}

func TestWriteSet_RecordRead_ByteLimitEnforced(t *testing.T) {
	ws, _ := newIsolatedWriteSet(t, IsolationWrittenReads, nil)
	ws.totalBytes = writeSetMaxBytes

	err := ws.recordRead("k1", absenceCompare("k1"))
	var limitErr *WriteSetLimitError
	require.ErrorAs(t, err, &limitErr)
	assert.Equal(t, "byte size", limitErr.Reason)
}

// TestWriteSet_RecordRead_LimitSurfacesThroughLookup proves the ceiling is
// reported at the reading statement, not deferred to COMMIT.
func TestWriteSet_RecordRead_LimitSurfacesThroughLookup(t *testing.T) {
	ws, _ := newIsolatedWriteSet(t, IsolationWrittenReads, map[string]string{"k1": "v"})
	ws.totalBytes = writeSetMaxBytes

	_, _, err := ws.Lookup(context.Background(), "k1")
	var limitErr *WriteSetLimitError
	require.ErrorAs(t, err, &limitErr)

	_, _, err = ws.Lookup(context.Background(), "missing")
	require.ErrorAs(t, err, &limitErr, "the absent-key read path is bounded too")
}

// TestWriteSet_RecordRead_CountsTowardWriteCeiling shows read and write
// preconditions share one budget, because they share one proposal.
func TestWriteSet_RecordRead_CountsTowardWriteCeiling(t *testing.T) {
	ws, _ := newIsolatedWriteSet(t, IsolationWrittenReads, nil)
	ws.readOrder = make([]string, writeSetMaxOps)

	err := ws.Put(context.Background(), "k1", []byte("v1"))
	var limitErr *WriteSetLimitError
	require.ErrorAs(t, err, &limitErr)
	assert.Equal(t, "operation count", limitErr.Reason)
}

func TestWriteSet_RecordRead_NoopBelowWrittenReads(t *testing.T) {
	for _, level := range []IsolationLevel{IsolationNone, IsolationWritten} {
		t.Run(level.String(), func(t *testing.T) {
			ws, _ := newIsolatedWriteSet(t, level, nil)
			require.NoError(t, ws.recordRead("k1", absenceCompare("k1")))
			assert.Nil(t, ws.reads, "the read map must not even be allocated below written+reads")
			assert.Empty(t, ws.readOrder)
			assert.Zero(t, ws.totalBytes)
		})
	}
}

// ---------------------------------------------------------------------------
// Savepoints do not un-read
// ---------------------------------------------------------------------------

func TestWriteSet_Savepoint_DoesNotUndoReads(t *testing.T) {
	ws, _ := newIsolatedWriteSet(t, IsolationWrittenReads, map[string]string{"read-me": "v"})

	ws.Mark("sp1")
	_, _, err := ws.Lookup(context.Background(), "read-me")
	require.NoError(t, err)
	require.NoError(t, ws.Put(context.Background(), "write-me", []byte("v")))

	require.NoError(t, ws.RollbackTo("sp1"))

	assert.Equal(t, []string{"read-me"}, comparedKeys(ws),
		"the buffered write is undone, but a value the transaction already saw cannot be un-seen")
}

// ---------------------------------------------------------------------------
// Commit outcomes per level
// ---------------------------------------------------------------------------

// commitAfterConcurrentWrite runs the write-skew shape at level: the
// transaction point-reads readKey and writes writeKey, another writer commits
// over readKey, and the transaction then commits. It reports whether the
// commit succeeded.
func commitAfterConcurrentWrite(t *testing.T, level IsolationLevel) bool {
	t.Helper()
	ws, store := newIsolatedWriteSet(t, level, map[string]string{"read-me": "v1"})
	readThenWrite(t, ws, "read-me", "write-me")

	require.NoError(t, store.Put(context.Background(), "read-me", []byte("v2")))

	resp, err := ws.commit(context.Background())
	require.NoError(t, err)
	return resp.Succeeded
}

// TestWriteSet_Commit_WrittenReads_CatchesWhatWrittenMisses is the reason
// IsolationWrittenReads exists: the transaction decided from a row it only
// read, and at the default nothing notices that row moved underneath it.
func TestWriteSet_Commit_WrittenReads_CatchesWhatWrittenMisses(t *testing.T) {
	assert.True(t, commitAfterConcurrentWrite(t, IsolationWritten),
		"the default compares written keys only, so a changed read key goes unnoticed")
	assert.False(t, commitAfterConcurrentWrite(t, IsolationWrittenReads),
		"written+reads must reject a transaction whose read moved underneath it")
	assert.True(t, commitAfterConcurrentWrite(t, IsolationNone),
		"none compares nothing")
}

func TestWriteSet_Commit_None_LastWriterWins(t *testing.T) {
	ws, store := newIsolatedWriteSet(t, IsolationNone, map[string]string{"k1": "original"})
	require.NoError(t, ws.Put(context.Background(), "k1", []byte("mine")))

	// A concurrent transaction commits over the very key this one buffered:
	// at IsolationWritten this is the textbook 1213.
	require.NoError(t, store.Put(context.Background(), "k1", []byte("theirs")))

	resp, err := ws.commit(context.Background())
	require.NoError(t, err)
	require.True(t, resp.Succeeded)

	got, err := store.Get(context.Background(), "k1")
	require.NoError(t, err)
	assert.Equal(t, []byte("mine"), got, "the last writer to commit wins outright")
}

// TestWriteSet_Commit_WrittenReads_ReadOnlyTransactionNeverConflicts pins the
// deliberate decision not to validate a read set with no writes behind it.
func TestWriteSet_Commit_WrittenReads_ReadOnlyTransactionNeverConflicts(t *testing.T) {
	ws, store := newIsolatedWriteSet(t, IsolationWrittenReads, map[string]string{"k1": "v1"})

	_, _, err := ws.Lookup(context.Background(), "k1")
	require.NoError(t, err)
	require.NoError(t, store.Put(context.Background(), "k1", []byte("v2")))

	resp, err := ws.commit(context.Background())
	require.NoError(t, err)
	assert.True(t, resp.Succeeded, "a transaction that wrote nothing cannot lose an update")
}

// ---------------------------------------------------------------------------
// The qrdb_isolation session variable
// ---------------------------------------------------------------------------

func TestQRDBSession_IsolationVariable_DefaultsToWritten(t *testing.T) {
	ctx, sess := newTxContext(true)

	value, err := sess.GetSessionVariable(ctx, IsolationVariable)
	require.NoError(t, err, "the variable must be registered for every session")
	assert.Equal(t, "written", value)
	assert.Equal(t, IsolationWritten, sess.isolationLevel(ctx))
}

func TestQRDBSession_IsolationVariable_SetPerLevel(t *testing.T) {
	for _, level := range isolationLevelOrder {
		t.Run(level.String(), func(t *testing.T) {
			ctx, sess := newTxContext(true)
			require.NoError(t, sess.SetSessionVariable(ctx, IsolationVariable, level.String()))
			assert.Equal(t, level, sess.isolationLevel(ctx))
		})
	}
}

func TestQRDBSession_IsolationVariable_RejectsUnknownValue(t *testing.T) {
	ctx, sess := newTxContext(true)

	err := sess.SetSessionVariable(ctx, IsolationVariable, "serializable")
	require.Error(t, err, "a level this engine does not implement must be refused, not approximated")
	assert.Equal(t, IsolationWritten, sess.isolationLevel(ctx), "a refused SET leaves the session where it was")
}

func TestQRDBSession_IsolationVariable_SessionScopedOnly(t *testing.T) {
	ctx, _ := newTxContext(true)

	err := sqle.SystemVariables.SetGlobal(ctx, IsolationVariable, "none")
	require.Error(t, err, "SET GLOBAL must not be able to change every other connection's isolation")
	assert.True(t, sqle.ErrSystemVariableSessionOnly.Is(err), "got %v", err)
}

// TestQRDBSession_IsolationVariable_IsNotMysqlTransactionIsolation pins the
// deliberate refusal to alias MySQL's lock-based level names onto qrdb's
// optimistic ones: SET TRANSACTION ISOLATION LEVEL still writes MySQL's own
// variable and still does not touch this one.
func TestQRDBSession_IsolationVariable_IsNotMysqlTransactionIsolation(t *testing.T) {
	ctx, sess := newTxContext(true)

	require.NoError(t, sess.SetSessionVariable(ctx, "transaction_isolation", "SERIALIZABLE"))

	value, err := sess.GetSessionVariable(ctx, IsolationVariable)
	require.NoError(t, err)
	assert.Equal(t, "written", value)
	assert.Equal(t, IsolationWritten, sess.isolationLevel(ctx))
}

// withIsolationVariable temporarily replaces the registered qrdb_isolation
// definition with replacement, restoring the real one afterwards, so the
// defensive branches of isolationLevel -- reachable only from a session built
// against a registry this package's init never shaped -- can be exercised.
func withIsolationVariable(t *testing.T, replacement sqle.SystemVariable) {
	t.Helper()
	sqle.SystemVariables.AddSystemVariables([]sqle.SystemVariable{replacement})
	t.Cleanup(func() {
		sqle.SystemVariables.AddSystemVariables([]sqle.SystemVariable{isolationSystemVariable()})
	})
}

func TestQRDBSession_IsolationLevel_UnknownVariableDefaults(t *testing.T) {
	sess := NewSession(sqle.NewBaseSession(), nil)
	ctx := sqle.NewContext(context.Background(), sqle.WithSession(sess))

	// A session whose variable map predates registration entirely.
	require.NoError(t, sess.SetSessionVariable(ctx, IsolationVariable, "none"))
	require.Equal(t, IsolationNone, sess.isolationLevel(ctx))
	delete(sess.GetAllSessionVariables(), IsolationVariable) // copy; proves the map handed out is not the live one

	stripped := NewSession(strippedBaseSession(), nil)
	strippedCtx := sqle.NewContext(context.Background(), sqle.WithSession(stripped))
	assert.Equal(t, IsolationWritten, stripped.isolationLevel(strippedCtx))
}

// strippedBaseSession builds a BaseSession whose system variable map was
// created while no registry existed at all, which is the only way a session
// can genuinely lack qrdb_isolation.
func strippedBaseSession() *sqle.BaseSession {
	saved := sqle.SystemVariables
	sqle.SystemVariables = nil
	defer func() { sqle.SystemVariables = saved }()
	return sqle.NewBaseSession()
}

func TestQRDBSession_IsolationLevel_NonStringValueDefaults(t *testing.T) {
	withIsolationVariable(t, &sqle.MysqlSystemVariable{
		Name:    IsolationVariable,
		Scope:   sqle.GetMysqlScope(sqle.SystemVariableScope_Session),
		Dynamic: true,
		Type:    types.NewSystemIntType(IsolationVariable, 0, 10, false),
		Default: int64(7),
	})

	sess := NewSession(sqle.NewBaseSession(), nil)
	ctx := sqle.NewContext(context.Background(), sqle.WithSession(sess))
	assert.Equal(t, IsolationWritten, sess.isolationLevel(ctx))
}

func TestQRDBSession_IsolationLevel_UnrecognizedSpellingDefaults(t *testing.T) {
	withIsolationVariable(t, &sqle.MysqlSystemVariable{
		Name:    IsolationVariable,
		Scope:   sqle.GetMysqlScope(sqle.SystemVariableScope_Session),
		Dynamic: true,
		Type:    types.NewSystemStringType(IsolationVariable),
		Default: "not-a-level",
	})

	sess := NewSession(sqle.NewBaseSession(), nil)
	ctx := sqle.NewContext(context.Background(), sqle.WithSession(sess))
	assert.Equal(t, IsolationWritten, sess.isolationLevel(ctx))
}

// ---------------------------------------------------------------------------
// StartTransaction samples the session's level
// ---------------------------------------------------------------------------

func TestQRDBSession_StartTransaction_DefaultsToWritten(t *testing.T) {
	ctx, sess := newTxContext(true)

	_, err := sess.StartTransaction(ctx, sqle.ReadWrite)
	require.NoError(t, err)
	assert.Equal(t, IsolationWritten, sess.writeSet().Isolation())
}

func TestQRDBSession_StartTransaction_SamplesSessionLevel(t *testing.T) {
	ctx, sess := newTxContext(true)
	require.NoError(t, sess.SetSessionVariable(ctx, IsolationVariable, "written+reads"))

	_, err := sess.StartTransaction(ctx, sqle.ReadWrite)
	require.NoError(t, err)
	assert.Equal(t, IsolationWrittenReads, sess.writeSet().Isolation())
}

// TestQRDBSession_StartTransaction_LevelFixedForTransaction pins the sampling
// point: changing the session variable mid-transaction must not retroactively
// change how work already buffered is checked.
func TestQRDBSession_StartTransaction_LevelFixedForTransaction(t *testing.T) {
	ctx, sess := newTxContext(true)

	_, err := sess.StartTransaction(ctx, sqle.ReadWrite)
	require.NoError(t, err)
	require.NoError(t, sess.SetSessionVariable(ctx, IsolationVariable, "none"))

	assert.Equal(t, IsolationWritten, sess.writeSet().Isolation())

	_, err = sess.StartTransaction(ctx, sqle.ReadWrite)
	require.NoError(t, err)
	assert.Equal(t, IsolationNone, sess.writeSet().Isolation(), "the next transaction picks the new level up")
}

// ---------------------------------------------------------------------------
// End to end, through the SQL engine
// ---------------------------------------------------------------------------

// newIsolationAccounts builds a two-row table over a shared store and returns
// a provider serving it.
func newIsolationAccounts(t *testing.T) *Provider {
	t.Helper()
	h := newDDLHarness()
	prov := h.newDDLProvider("bank")
	setup := newTxConn(t, prov, "bank")
	setup.mustExec(t, "CREATE TABLE accounts (id BIGINT UNSIGNED PRIMARY KEY, balance INT)")
	setup.mustExec(t, "INSERT INTO accounts (id, balance) VALUES (1, 100), (2, 100)")
	return prov
}

func TestSQLIsolation_DefaultsToWritten(t *testing.T) {
	prov := newIsolationAccounts(t)
	conn := newTxConn(t, prov, "bank")

	_, rows := conn.mustExec(t, "SELECT @@qrdb_isolation")
	require.Len(t, rows, 1)
	assert.Equal(t, "written", rows[0][0])
}

func TestSQLIsolation_IsPerSession(t *testing.T) {
	prov := newIsolationAccounts(t)
	a := newTxConn(t, prov, "bank")
	b := newTxConn(t, prov, "bank")

	a.mustExec(t, "SET qrdb_isolation = 'none'")

	_, rowsA := a.mustExec(t, "SELECT @@qrdb_isolation")
	_, rowsB := b.mustExec(t, "SELECT @@qrdb_isolation")
	assert.Equal(t, "none", rowsA[0][0])
	assert.Equal(t, "written", rowsB[0][0], "one session's setting must not leak into another's")
}

func TestSQLIsolation_RejectsUnknownLevel(t *testing.T) {
	prov := newIsolationAccounts(t)
	conn := newTxConn(t, prov, "bank")

	_, _, err := conn.exec("SET qrdb_isolation = 'repeatable read'")
	require.Error(t, err, "a MySQL level name is not a qrdb level and must not be silently accepted")
}

// TestSQLIsolation_None_BothCommit_LastWriterWins is the observable contrast
// with TestTx_ConcurrentUpdate_OneGets1213, which runs the identical race at
// the default and rejects one of the two.
func TestSQLIsolation_None_BothCommit_LastWriterWins(t *testing.T) {
	prov := newIsolationAccounts(t)
	a := newTxConn(t, prov, "bank")
	b := newTxConn(t, prov, "bank")

	a.mustExec(t, "SET qrdb_isolation = 'none'")
	b.mustExec(t, "SET qrdb_isolation = 'none'")

	a.mustExec(t, "BEGIN")
	b.mustExec(t, "BEGIN")
	a.mustExec(t, "UPDATE accounts SET balance = 11 WHERE id = 1")
	b.mustExec(t, "UPDATE accounts SET balance = 22 WHERE id = 1")
	a.mustExec(t, "COMMIT")
	b.mustExec(t, "COMMIT")

	other := newTxConn(t, prov, "bank")
	_, rows := other.mustExec(t, "SELECT balance FROM accounts WHERE id = 1")
	require.Len(t, rows, 1)
	assert.EqualValues(t, 22, rows[0][0], "the second committer's value must be the durable one")
}

// writeSkew runs the case IsolationWritten cannot see: session A reads row 1
// by exact key and writes a different row, while session B commits over row 1
// underneath it. It returns A's COMMIT error, if any.
func writeSkew(t *testing.T, level IsolationLevel) error {
	t.Helper()
	prov := newIsolationAccounts(t)
	a := newTxConn(t, prov, "bank")
	b := newTxConn(t, prov, "bank")

	a.mustExec(t, "SET qrdb_isolation = '"+level.String()+"'")
	a.mustExec(t, "BEGIN")

	_, rows := a.mustExec(t, "SELECT balance FROM accounts WHERE id = 1")
	require.Len(t, rows, 1, "the read must be a point read, not a scan")
	require.EqualValues(t, 100, rows[0][0])

	// A decides from what it read and writes somewhere else entirely.
	a.mustExec(t, "INSERT INTO accounts (id, balance) VALUES (3, 100)")

	// B invalidates the premise, autocommit.
	b.mustExec(t, "UPDATE accounts SET balance = 0 WHERE id = 1")

	_, _, err := a.exec("COMMIT")
	return err
}

// TestSQLIsolation_WrittenReads_CatchesWriteSkew is level 3's justifying
// test: the identical transaction commits happily at the default because the
// key it merely READ carries no precondition there.
func TestSQLIsolation_WrittenReads_CatchesWriteSkew(t *testing.T) {
	require.NoError(t, writeSkew(t, IsolationWritten),
		"the default cannot see a conflict on a key the transaction only read")

	err := writeSkew(t, IsolationWrittenReads)
	require.Error(t, err, "written+reads must reject it")

	sqlErr := sqle.CastSQLError(err)
	require.NotNil(t, sqlErr)
	assert.EqualValues(t, mysql.ERLockDeadlock, sqlErr.Num, "a read conflict is still a 1213 the client can retry")
	assert.EqualValues(t, 1213, sqlErr.Num)
	assert.Equal(t, "40001", sqlErr.State)
}

func TestSQLIsolation_WrittenReads_RangeReadRefused(t *testing.T) {
	prov := newIsolationAccounts(t)
	conn := newTxConn(t, prov, "bank")

	conn.mustExec(t, "SET qrdb_isolation = 'written+reads'")
	conn.mustExec(t, "BEGIN")

	_, _, err := conn.exec("SELECT balance FROM accounts")
	require.Error(t, err, "a table scan cannot be compared and must not be served as if it could")

	var rangeErr *IsolationRangeReadError
	require.ErrorAs(t, err, &rangeErr)
	assert.Equal(t, "written+reads", rangeErr.Level)

	sqlErr := sqle.CastSQLError(err)
	require.NotNil(t, sqlErr)
	assert.NotEqualValues(t, mysql.ERLockDeadlock, sqlErr.Num, "this is a refusal, not a conflict: retrying unchanged cannot help")
}

func TestSQLIsolation_WrittenReads_PointWorkloadCommits(t *testing.T) {
	prov := newIsolationAccounts(t)
	conn := newTxConn(t, prov, "bank")

	conn.mustExec(t, "SET qrdb_isolation = 'written+reads'")
	conn.mustExec(t, "BEGIN")
	conn.mustExec(t, "SELECT balance FROM accounts WHERE id = 1")
	conn.mustExec(t, "INSERT INTO accounts (id, balance) VALUES (4, 7)")
	conn.mustExec(t, "COMMIT")

	other := newTxConn(t, prov, "bank")
	_, rows := other.mustExec(t, "SELECT balance FROM accounts WHERE id = 4")
	require.Len(t, rows, 1)
	assert.EqualValues(t, 7, rows[0][0], "an uncontended point-read/point-write transaction commits normally")
}
