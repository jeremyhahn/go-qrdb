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
	"time"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	goerrors "gopkg.in/src-d/go-errors.v1"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// go-qrdb#547: generation fencing races.
//
// Every test in this file expresses the SAME deterministic shape rather than
// racing real goroutines against a clock: resolve a table (which pins the
// generation its statement plans against, exactly as GetTable/activeWriteSet
// do), let a TRUNCATE commit out from under that already-resolved handle,
// then "resume" by driving a write through it. Because the resolved handle's
// Generation field is never re-read until the write path itself notices a
// rejected compare-and-swap, this is byte-for-byte what a statement that
// paused after resolving the table and before proposing its write would
// observe if a concurrent TRUNCATE committed in that window -- deterministic
// and immune to scheduling flakiness, unlike an actual sleep/goroutine race.
// ---------------------------------------------------------------------------

// requireLockDeadlockStale asserts err is sql.ErrLockDeadlock-classified
// (MySQL ER_LOCK_DEADLOCK/1213, and via pkg/sql/pgsql's sql.ErrLockDeadlock.Is
// check, PostgreSQL SQLSTATE 40001 -- see wire_errors.go) AND that its cause
// is specifically a *service.TableGenerationStaleError, not the generic
// conflict every other ErrLockDeadlock-wrapped failure carries. *goerrors.Error
// (go-mysql-server's Kind.Wrap) exposes its cause only via Cause(), not
// Unwrap(), so errors.As cannot see through it on its own -- the kind error
// itself is unwrapped first, then its Cause() is checked separately.
func requireLockDeadlockStale(t *testing.T, err error) *service.TableGenerationStaleError {
	t.Helper()
	require.Error(t, err)
	assert.True(t, sqle.ErrLockDeadlock.Is(err), "err = %v (%T) must map to sql.ErrLockDeadlock", err, err)

	var kindErr *goerrors.Error
	require.True(t, errors.As(err, &kindErr), "err = %v (%T) is not a *goerrors.Error", err, err)

	var stale *service.TableGenerationStaleError
	require.True(t, errors.As(kindErr.Cause(), &stale),
		"cause = %v (%T) is not *service.TableGenerationStaleError", kindErr.Cause(), kindErr.Cause())
	return stale
}

// TestGenerationFencing_Autocommit_InsertRejectedWhenTruncateRaces pins the
// autocommit half of go-qrdb#547's required race test: a statement resolves
// the table (pinning Generation 0), a concurrent TRUNCATE commits, and the
// original handle's Insert is rejected rather than silently writing a row
// under a generation the reclaim sweeper is about to delete.
func TestGenerationFencing_Autocommit_InsertRejectedWhenTruncateRaces(t *testing.T) {
	h := newDDLHarness()
	staleTbl, def := newWriterHarnessTable(t, h, ordersCreateRequest())
	require.Equal(t, uint64(0), staleTbl.def.Generation, "precondition: table starts at generation 0")

	// The race: TRUNCATE commits AFTER staleTbl resolved the table but
	// BEFORE its Insert proposes anything -- the "pause after allocation"
	// window.
	_, err := h.tableSvc.TruncateTable(context.Background(), def.Database, def.Name, true)
	require.NoError(t, err)

	ins := staleTbl.Inserter(newTestContext())
	insErr := ins.Insert(newTestContext(), orderRow(1, "SKU-1", "east", int32(10)))

	stale := requireLockDeadlockStale(t, insErr)
	assert.Equal(t, uint64(0), stale.Expected)
	assert.Equal(t, uint64(1), stale.Actual)

	// The row must not have been written: the rejected CAS applied nothing.
	rows, err := h.kvSvc.Scan(context.Background(), service.TableDataPrefix(def.Database, def.TableID, 1))
	require.NoError(t, err)
	assert.Empty(t, rows, "a rejected INSERT must not have written its row into the new generation")
}

// TestGenerationFencing_Autocommit_UpdateRejectedWhenTruncateRaces is
// TestGenerationFencing_Autocommit_InsertRejectedWhenTruncateRaces's Update
// counterpart: an UPDATE planned against a row read at the old generation
// must not silently resurrect that row into the new, empty generation.
func TestGenerationFencing_Autocommit_UpdateRejectedWhenTruncateRaces(t *testing.T) {
	h := newDDLHarness()
	tbl, def := newWriterHarnessTable(t, h, ordersCreateRequest())
	old := orderRow(1, "SKU-1", "east", int32(10))
	require.NoError(t, tbl.Inserter(newTestContext()).Insert(newTestContext(), old))

	// Re-resolve to get the handle an UPDATE statement would plan against
	// (still generation 0, row present).
	staleDef, err := h.tableSvc.GetTable(context.Background(), def.Database, def.Name)
	require.NoError(t, err)
	staleTbl, err := NewRelationalTable(staleDef, h.kvSvc, h.mvccSvc,
		WithTableService(h.tableSvc), WithTransactionService(h.txnSvc), WithSequenceService(h.seqSvc))
	require.NoError(t, err)

	_, err = h.tableSvc.TruncateTable(context.Background(), def.Database, def.Name, true)
	require.NoError(t, err)

	upd := staleTbl.Updater(newTestContext())
	newRow := orderRow(1, "SKU-1", "east", int32(99))
	updErr := upd.Update(newTestContext(), old, newRow)

	stale := requireLockDeadlockStale(t, updErr)
	assert.Equal(t, uint64(0), stale.Expected)
	assert.Equal(t, uint64(1), stale.Actual)
}

// TestGenerationFencing_Buffered_CommitRejectedWhenTruncateRaces is
// TestGenerationFencing_Autocommit_InsertRejectedWhenTruncateRaces's
// interactive-transaction counterpart: an INSERT is buffered inside an open
// BEGIN...COMMIT, a concurrent connection's TRUNCATE commits before this
// transaction commits, and COMMIT itself -- not the buffered INSERT
// statement -- is where the rejection surfaces, since the whole buffered
// write-set (including its generation fences, added independent of
// IsolationLevel; see WriteSet.RequireResolution) is proposed as one
// compare-and-swap only at commit time.
func TestGenerationFencing_Buffered_CommitRejectedWhenTruncateRaces(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	connA := newTxConn(t, prov, "shop")
	connA.mustExec(t, "CREATE TABLE widgets (id BIGINT UNSIGNED PRIMARY KEY, name VARCHAR(64))")

	connA.mustExec(t, "BEGIN")
	connA.mustExec(t, "INSERT INTO widgets (id, name) VALUES (1, 'a')")

	// A second, independent connection's TRUNCATE commits while connA's
	// transaction is still open and unflushed.
	connB := newTxConn(t, prov, "shop")
	connB.mustExec(t, "TRUNCATE TABLE widgets")

	_, _, commitErr := connA.exec("COMMIT")
	requireLockDeadlockStale(t, commitErr)
}

// TestGenerationFencing_Buffered_IsolationNoneStillFenced verifies the
// generation guard is NOT folded into the per-IsolationLevel Compares()
// dispatch: IsolationNone's own builder (noCompares) attaches no
// preconditions at all, but a transaction running at that level must still
// be rejected by a concurrent TRUNCATE, because generation fencing is a
// distinct correctness concern from the user-selectable, opt-out conflict
// detection IsolationLevel controls (see WriteSet.commit).
func TestGenerationFencing_Buffered_IsolationNoneStillFenced(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	connA := newTxConn(t, prov, "shop")
	connA.mustExec(t, "CREATE TABLE widgets (id BIGINT UNSIGNED PRIMARY KEY, name VARCHAR(64))")

	connA.mustExec(t, "SET qrdb_isolation = 'none'")
	connA.mustExec(t, "BEGIN")
	connA.mustExec(t, "INSERT INTO widgets (id, name) VALUES (1, 'a')")

	connB := newTxConn(t, prov, "shop")
	connB.mustExec(t, "TRUNCATE TABLE widgets")

	_, _, commitErr := connA.exec("COMMIT")
	requireLockDeadlockStale(t, commitErr)
}

// countingGenerationKV wraps a service.MVCCKVServicer, counting every read
// of one specific key -- the table's generation key -- whether by value (Get,
// the pin re-proof's own read) or with its revision (GetRevision, the read
// TableService.GetTable resolves the generation with), so a test can assert a
// bulk write path performs NO per-row consensus read of it (see
// TestGenerationFencing_BulkInsert_NoPerRowConsensusRead). Every other call is
// delegated unchanged, which is what makes this a spy rather than a stub.
type countingGenerationKV struct {
	service.MVCCKVServicer
	genKey string
	gets   int
}

func (c *countingGenerationKV) Get(ctx context.Context, key string) ([]byte, error) {
	if key == c.genKey {
		c.gets++
	}
	return c.MVCCKVServicer.Get(ctx, key)
}

func (c *countingGenerationKV) GetRevision(ctx context.Context, key string) (*service.KeyValue, error) {
	if key == c.genKey {
		c.gets++
	}
	return c.MVCCKVServicer.GetRevision(ctx, key)
}

// TestGenerationFencing_BulkInsert_NoPerRowConsensusRead pins go-qrdb#547's
// explicit performance requirement precisely: the generation Compare rides
// in the SAME compare-and-swap each row's own write already proposes, so a
// bulk INSERT that never loses a race performs a generation-key read count
// that does NOT scale with row count. That bound is exactly ONE, not zero:
// resolving the table AT ALL -- once per STATEMENT, via TableService.GetTable,
// exactly as relational_database.go's relationalTable does for every real
// SQL statement -- calls readGeneration, one linearizable Get. What this
// test actually pins is that 200 Insert calls on the SAME resolved handle
// (a multi-row INSERT statement's shape: sql.InsertableTable.Inserter is
// called once per statement, Insert once per row on that one handle) cost
// the SAME one read a single-row INSERT would, because checkGenerationStale
// -- the one read a lost race would ALSO trigger -- never runs when every
// row succeeds on its first attempt.
//
// An earlier version of this test constructed RelationalTable directly from
// CreateTable's own return value, bypassing GetTable (and therefore
// readGeneration) entirely, and asserted zero gets -- true for that
// construction path, but not a statement any real SQL execution produces,
// so it could not have caught a per-ROW read if one existed.
func TestGenerationFencing_BulkInsert_NoPerRowConsensusRead(t *testing.T) {
	h := newDDLHarness()
	def, err := h.tableSvc.CreateTable(context.Background(), ordersCreateRequest())
	require.NoError(t, err)

	spy := &countingGenerationKV{
		MVCCKVServicer: h.mvccSvc,
		genKey:         service.TableGenerationKey(def.Database, def.TableID),
	}
	// Resolve through GetTable, exactly as a real statement does (see
	// relational_database.go's relationalTable) -- this is what performs
	// the one, per-STATEMENT generation read the test measures. A second
	// TableService instance, over the SAME underlying store but through the
	// counting spy, is what lets GetTable's read be observed: h.tableSvc
	// itself was built over the unwrapped h.kvSvc before spy existed.
	tableSvcOverKV := service.NewTableService(spy, spy, h.txnSvc, h.seqSvc)
	resolved, err := tableSvcOverKV.GetTable(context.Background(), def.Database, def.Name)
	require.NoError(t, err)

	tbl, err := NewRelationalTable(resolved, spy, spy,
		WithTableService(tableSvcOverKV), WithTransactionService(h.txnSvc), WithSequenceService(h.seqSvc))
	require.NoError(t, err)

	ins := tbl.Inserter(newTestContext())
	const rowCount = 200
	for i := 0; i < rowCount; i++ {
		row := orderRow(uint64(i), nil, nil, nil)
		require.NoError(t, ins.Insert(newTestContext(), row))
	}

	assert.Equal(t, 1, spy.gets,
		"resolving the table costs exactly the one per-STATEMENT generation read GetTable already makes; "+
			"200 rows on that SAME resolved handle must not add a single further read")

	rows, err := h.kvSvc.Scan(context.Background(), service.TableDataPrefix(def.Database, def.TableID, 0))
	require.NoError(t, err)
	assert.Len(t, rows, rowCount)
}

// TestIndexBuildInserter_RejectedWhenTruncateRaces pins CREATE INDEX's
// build to the same fence: a TRUNCATE committing between the table's
// resolution and the build's flush must fail it rather than write new index
// entries into a generation about to be reclaimed.
func TestIndexBuildInserter_RejectedWhenTruncateRaces(t *testing.T) {
	h := newDDLHarness()
	def, err := h.tableSvc.CreateTable(context.Background(), &service.CreateTableRequest{
		Database: "shop", Name: "users",
		Columns:    []service.ColumnDef{{Name: "id", Type: "BIGINT UNSIGNED"}, {Name: "email", Type: "VARCHAR(255)"}},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
	})
	require.NoError(t, err)
	seedRow(t, h.kvSvc, "shop", def, sqle.Row{uint64(1), "alice@example.com"})
	require.NoError(t, h.tableSvc.CreateIndex(context.Background(), &service.CreateTableIndexRequest{
		Database: "shop", Table: "users",
		Index: service.TableIndex{Name: "idx_email", Columns: []service.IndexCol{{Name: "email"}}},
	}))

	staleDef, err := h.tableSvc.GetTable(context.Background(), "shop", "users")
	require.NoError(t, err)
	staleTbl, err := NewRelationalTable(staleDef, h.kvSvc, h.mvccSvc,
		WithTableService(h.tableSvc), WithTransactionService(h.txnSvc))
	require.NoError(t, err)

	_, err = h.tableSvc.TruncateTable(context.Background(), "shop", "users", true)
	require.NoError(t, err)

	inserter, err := staleTbl.BuildIndex(newTestContext(), sqle.IndexDef{Name: "idx_email", Columns: []sqle.IndexColumn{{Name: "email"}}})
	require.NoError(t, err)
	require.NoError(t, inserter.Insert(newTestContext(), sqle.Row{uint64(1), "alice@example.com"}))
	err = inserter.Close(newTestContext())
	requireLockDeadlockStale(t, err)
}

// TestIndexBuildInserter_RejectedWhenTheIndexIsDroppedMidBuild (go-qrdb#547
// N5): a DROP INDEX landing between the build's start and a chunk publishes
// a reclaim of the index's incarnation; the chunk is fenced on the
// definition that listed the index, so it fails instead of writing entries
// the reclaim has already swept past.
func TestIndexBuildInserter_RejectedWhenTheIndexIsDroppedMidBuild(t *testing.T) {
	h := newDDLHarness()
	def, err := h.tableSvc.CreateTable(context.Background(), &service.CreateTableRequest{
		Database: "shop", Name: "users",
		Columns:    []service.ColumnDef{{Name: "id", Type: "BIGINT UNSIGNED"}, {Name: "email", Type: "VARCHAR(255)"}},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
	})
	require.NoError(t, err)
	tbl, err := NewRelationalTable(def, h.kvSvc, h.mvccSvc,
		WithTableService(h.tableSvc), WithTransactionService(h.txnSvc))
	require.NoError(t, err)
	require.NoError(t, h.tableSvc.CreateIndex(context.Background(), &service.CreateTableIndexRequest{
		Database: "shop", Table: "users",
		Index: service.TableIndex{Name: "idx_email", Columns: []service.IndexCol{{Name: "email"}}},
	}))

	inserter, err := tbl.BuildIndex(newTestContext(), sqle.IndexDef{Name: "idx_email", Columns: []sqle.IndexColumn{{Name: "email"}}})
	require.NoError(t, err)
	built, err := h.tableSvc.GetTable(context.Background(), "shop", "users")
	require.NoError(t, err)
	incarnation := incarnationOf(built, "idx_email")
	require.NoError(t, h.tableSvc.DropIndex(context.Background(), "shop", "users", "idx_email"))

	require.NoError(t, inserter.Insert(newTestContext(), sqle.Row{uint64(1), "alice@example.com"}))
	stale := requireDefinitionStale(t, inserter.Close(newTestContext()))
	assert.Contains(t, stale.Error(), "index build")

	entries, err := h.kvSvc.Scan(context.Background(), service.TableIndexPrefix("shop", def.TableID, 0, incarnation))
	require.NoError(t, err)
	assert.Empty(t, entries, "no entry lands in the dropped incarnation")
}

// TestIndexBuildInserter_UnchangedDefinitionBuilds is its positive control:
// DML landing mid-build does not move the definition, and the build
// completes.
func TestIndexBuildInserter_UnchangedDefinitionBuilds(t *testing.T) {
	h := newDDLHarness()
	def, err := h.tableSvc.CreateTable(context.Background(), &service.CreateTableRequest{
		Database: "shop", Name: "users",
		Columns:    []service.ColumnDef{{Name: "id", Type: "BIGINT UNSIGNED"}, {Name: "email", Type: "VARCHAR(255)"}},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
	})
	require.NoError(t, err)
	tbl, err := NewRelationalTable(def, h.kvSvc, h.mvccSvc,
		WithTableService(h.tableSvc), WithTransactionService(h.txnSvc))
	require.NoError(t, err)
	require.NoError(t, h.tableSvc.CreateIndex(context.Background(), &service.CreateTableIndexRequest{
		Database: "shop", Table: "users",
		Index: service.TableIndex{Name: "idx_email", Columns: []service.IndexCol{{Name: "email"}}},
	}))

	inserter, err := tbl.BuildIndex(newTestContext(), sqle.IndexDef{Name: "idx_email", Columns: []sqle.IndexColumn{{Name: "email"}}})
	require.NoError(t, err)
	require.NoError(t, inserter.Insert(newTestContext(), sqle.Row{uint64(1), "alice@example.com"}))
	require.NoError(t, inserter.Close(newTestContext()))
}

// TestGenerationFencing_Buffered_LaterStatementRefusedWhenTruncateRacesMidTransaction
// pins the fix for a long-buffered-transaction consistency gap: a
// transaction's reads are served from the MVCC snapshot revision pinned
// when it FIRST bound to a table (WriteSet.bind), but each STATEMENT
// resolves a fresh RelationalTable whose Generation reflects whatever is
// CURRENT right now, not what the pinned snapshot saw. Without this fix, a
// TRUNCATE committing (from another connection) between two statements of
// the SAME open transaction would let the second statement silently read or
// write under the NEW generation's key prefix while still serving values
// from the OLD, pre-truncate snapshot -- producing empty or otherwise wrong
// results instead of failing outright. It also could never be caught at
// COMMIT for a read-only transaction, since a read-only WriteSet.commit
// proposes nothing at all.
//
// The fix refuses the statement immediately, at resolution
// (RelationalTable.activeWriteSet), the instant a later statement's fresh
// Generation disagrees with what an earlier statement in the SAME
// transaction already pinned (WriteSet.PinnedResolution).
func TestGenerationFencing_Buffered_LaterStatementRefusedWhenTruncateRacesMidTransaction(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	connA := newTxConn(t, prov, "shop")
	connA.mustExec(t, "CREATE TABLE widgets (id BIGINT UNSIGNED PRIMARY KEY, name VARCHAR(64))")
	connA.mustExec(t, "INSERT INTO widgets (id, name) VALUES (1, 'a')")

	connA.mustExec(t, "BEGIN")
	// First statement: binds the WriteSet, pinning both the MVCC snapshot
	// revision (bind) and this table's generation (RequireResolution) at 0.
	_, rows := connA.mustExec(t, "SELECT id FROM widgets")
	require.Len(t, rows, 1)

	// A concurrent connection's TRUNCATE commits while connA's transaction
	// is still open.
	connB := newTxConn(t, prov, "shop")
	connB.mustExec(t, "TRUNCATE TABLE widgets")

	// The second statement resolves the table fresh, sees generation 1, and
	// must be refused immediately rather than silently reading generation
	// 1's data (empty) through the generation-0 pinned snapshot.
	_, _, err := connA.exec("SELECT id FROM widgets")
	requireLockDeadlockStale(t, err)
}

// TestGenerationFencing_Buffered_ReadOnlyTransactionIsRefusedNotSilent is
// TestGenerationFencing_Buffered_LaterStatementRefusedWhenTruncateRacesMidTransaction's
// most important case: a PURELY read-only transaction, whose commit
// (WriteSet.commit) proposes nothing at all and therefore could never have
// caught the staleness on its own. Before this fix there was no other point
// at which a read-only transaction's generation mismatch was ever checked.
func TestGenerationFencing_Buffered_ReadOnlyTransactionIsRefusedNotSilent(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	connA := newTxConn(t, prov, "shop")
	connA.mustExec(t, "CREATE TABLE widgets (id BIGINT UNSIGNED PRIMARY KEY, name VARCHAR(64))")
	connA.mustExec(t, "INSERT INTO widgets (id, name) VALUES (1, 'a')")

	connA.mustExec(t, "START TRANSACTION READ ONLY")
	_, rows := connA.mustExec(t, "SELECT id FROM widgets")
	require.Len(t, rows, 1)

	connB := newTxConn(t, prov, "shop")
	connB.mustExec(t, "TRUNCATE TABLE widgets")

	_, _, err := connA.exec("SELECT id FROM widgets")
	requireLockDeadlockStale(t, err)

	// COMMIT itself must still succeed trivially (a read-only transaction's
	// commit proposes nothing) -- the refusal above is what protects the
	// client, not the commit.
	_, _, err = connA.exec("COMMIT")
	assert.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Reader quiescence: the generation pin (go-qrdb#547)
// ---------------------------------------------------------------------------

// pinClock is a settable clock for RelationalTable.now.
type pinClock struct{ t time.Time }

func (c *pinClock) now() time.Time { return c.t }

// resolvedOrders creates shop.orders with n rows and returns a table handle
// resolved through GetTable -- the way a statement resolves it -- on a
// settable clock, plus a spy counting generation-key reads.
func resolvedOrders(t *testing.T, n int) (*ddlHarness, *RelationalTable, *countingGenerationKV, *pinClock) {
	t.Helper()
	h := newDDLHarness()
	writer, def := newWriterHarnessTable(t, h, ordersCreateRequest())
	ins := writer.Inserter(newTestContext())
	for i := range n {
		require.NoError(t, ins.Insert(newTestContext(), orderRow(uint64(i+1), nil, nil, nil)))
	}

	spy := &countingGenerationKV{MVCCKVServicer: h.mvccSvc, genKey: service.TableGenerationKey(def.Database, def.TableID)}
	// A TableService over the spy, so the re-proof's own generation read
	// (through GetTable) is counted too.
	tableSvcOverSpy := service.NewTableService(spy, spy, h.txnSvc, h.seqSvc,
		service.WithReclaimIndex(h.txnSvc, service.StorePlacementShared, "harness"))
	clk := &pinClock{t: time.Now()}
	resolved, err := h.tableSvc.GetTable(context.Background(), def.Database, def.Name)
	require.NoError(t, err)
	resolved.GenerationReadAt = clk.t
	tbl, err := NewRelationalTable(resolved, spy, spy,
		WithTableService(tableSvcOverSpy), WithTransactionService(h.txnSvc), WithSequenceService(h.seqSvc))
	require.NoError(t, err)
	tbl.now = clk.now
	return h, tbl, spy, clk
}

func scanAll(ctx *sqle.Context, tbl *RelationalTable) ([]sqle.Row, error) {
	iter, err := tbl.PartitionRows(ctx, &relationalPartition{})
	if err != nil {
		return nil, err
	}
	return sqle.RowIterToRows(ctx, iter)
}

// TestGenerationPin_FreshPinCostsNoRead proves the guarantee is free on the
// common path: a read under a pin younger than TableGenerationPinTTL adds no
// generation read.
func TestGenerationPin_FreshPinCostsNoRead(t *testing.T) {
	_, tbl, spy, clk := resolvedOrders(t, 3)
	clk.t = clk.t.Add(service.TableGenerationPinTTL - time.Millisecond)

	rows, err := scanAll(newTestContext(), tbl)
	require.NoError(t, err)
	assert.Len(t, rows, 3)
	assert.Zero(t, spy.gets)
}

// TestGenerationPin_OldPinIsReprovenAndRenewed proves an expired pin costs
// one generation read that renews it, not one per read thereafter.
func TestGenerationPin_OldPinIsReprovenAndRenewed(t *testing.T) {
	_, tbl, spy, clk := resolvedOrders(t, 3)
	clk.t = clk.t.Add(service.TableGenerationPinTTL)

	for range 3 {
		rows, err := scanAll(newTestContext(), tbl)
		require.NoError(t, err)
		assert.Len(t, rows, 3)
	}
	assert.Equal(t, 1, spy.gets, "one re-proof renews the pin for the reads after it")
}

// TestGenerationPin_ReclaimedGenerationIsATypedErrorNotEmptyRows is the
// finding this exists for: a statement still reading a generation after
// TRUNCATE orphaned it and the sweeper deleted its keys must get the typed,
// retryable stale-generation error -- never an empty or partial result.
func TestGenerationPin_ReclaimedGenerationIsATypedErrorNotEmptyRows(t *testing.T) {
	h, tbl, _, clk := resolvedOrders(t, 3)
	def := tbl.def

	_, err := h.tableSvc.TruncateTable(context.Background(), def.Database, def.Name, true)
	require.NoError(t, err)
	// The sweeper may only act ReclaimQuiescence after it first observes the
	// job -- past this reader's pin TTL -- and then deletes the old keys.
	clk.t = clk.t.Add(service.ReclaimQuiescence)
	for _, prefix := range []string{
		service.TableDataPrefix(def.Database, def.TableID, 0),
		service.TableIndexGenerationPrefix(def.Database, def.TableID, 0),
	} {
		keys, err := h.kvSvc.List(context.Background(), prefix)
		require.NoError(t, err)
		for _, k := range keys {
			require.NoError(t, h.kvSvc.Delete(context.Background(), k))
		}
	}

	rows, err := scanAll(newTestContext(), tbl)
	stale := requireLockDeadlockStale(t, err)
	assert.Empty(t, rows)
	assert.Equal(t, uint64(0), stale.Expected)
	assert.Equal(t, uint64(1), stale.Actual)

	_, err = tbl.PartitionRows(newTestContext(), &indexedPartition{keys: [][]byte{[]byte("anything")}})
	requireLockDeadlockStale(t, err)
}

// TestGenerationPin_UnknownReadTimeIsProvenOnFirstRead proves a definition
// with no GenerationReadAt is treated as expired, never as fresh.
func TestGenerationPin_UnknownReadTimeIsProvenOnFirstRead(t *testing.T) {
	h := newDDLHarness()
	_, def := newWriterHarnessTable(t, h, ordersCreateRequest())
	require.True(t, def.GenerationReadAt.IsZero(), "precondition: CreateTable reports no read time")
	spy := &countingGenerationKV{MVCCKVServicer: h.mvccSvc, genKey: service.TableGenerationKey(def.Database, def.TableID)}
	tbl, err := NewRelationalTable(def, spy, spy)
	require.NoError(t, err)

	_, err = scanAll(newTestContext(), tbl)
	require.NoError(t, err)
	assert.Equal(t, 1, spy.gets)
}

// TestGenerationPin_TruncateRenewsTheHandlesPin proves a table handle that
// ran TRUNCATE itself reads its NEW generation without a re-proof.
func TestGenerationPin_TruncateRenewsTheHandlesPin(t *testing.T) {
	_, tbl, spy, _ := resolvedOrders(t, 2)
	tbl.now = time.Now
	_, err := tbl.Truncate(newTestContext())
	require.NoError(t, err)
	spy.gets = 0 // the TRUNCATE's own read of the counter it advances
	rows, err := scanAll(newTestContext(), tbl)
	require.NoError(t, err)
	assert.Empty(t, rows)
	assert.Zero(t, spy.gets, "the new generation's pin is fresh: no re-proof")
}

// TestGenerationPin_DroppedIndexOutlivesNoPin is DROP INDEX's half of the
// reader-quiescence guarantee: a dropped index's entries are reclaimed under
// the same quiescence as a truncated generation, so a statement that resolved
// the table before the drop must fail typed once its pin needs re-proving,
// not read an index the sweeper may be emptying.
func TestGenerationPin_DroppedIndexOutlivesNoPin(t *testing.T) {
	h, tbl, _, clk := resolvedOrders(t, 2)
	require.NoError(t, h.tableSvc.DropIndex(context.Background(), "shop", "orders", "idx_warehouse"))

	clk.t = clk.t.Add(service.TableGenerationPinTTL)
	_, err := scanAll(newTestContext(), tbl)
	require.Error(t, err)
	assert.True(t, sqle.ErrLockDeadlock.Is(err), "err = %v must be the retryable 1213/40001 class", err)
	var kindErr *goerrors.Error
	require.True(t, errors.As(err, &kindErr))
	var stale *service.TableDefinitionStaleError
	require.True(t, errors.As(kindErr.Cause(), &stale), "cause = %v", kindErr.Cause())
	assert.Contains(t, stale.Error(), "idx_warehouse")
}

// TestGenerationPin_RenamedIndexKeepsReading proves a rename does NOT fail a
// long statement: the renamed index keeps its incarnation, and with it the
// keyspace the statement is reading.
func TestGenerationPin_RenamedIndexKeepsReading(t *testing.T) {
	h, tbl, _, clk := resolvedOrders(t, 2)
	require.NoError(t, h.tableSvc.RenameIndex(context.Background(), &service.RenameTableIndexRequest{
		Database: "shop", Table: "orders", From: "idx_warehouse", To: "idx_wh",
	}))

	clk.t = clk.t.Add(service.TableGenerationPinTTL)
	rows, err := scanAll(newTestContext(), tbl)
	require.NoError(t, err)
	assert.Len(t, rows, 2)
}

// TestGenerationPin_DroppedTableOutlivesNoPin proves a statement outliving a
// DROP TABLE fails typed instead of reading a keyspace being deleted.
func TestGenerationPin_DroppedTableOutlivesNoPin(t *testing.T) {
	h, tbl, _, clk := resolvedOrders(t, 1)
	require.NoError(t, h.tableSvc.DropTable(context.Background(), &service.DropTableRequest{Database: "shop", Name: "orders"}))

	clk.t = clk.t.Add(service.TableGenerationPinTTL)
	_, err := scanAll(newTestContext(), tbl)
	require.Error(t, err)
	var kindErr *goerrors.Error
	require.True(t, errors.As(err, &kindErr))
	var stale *service.TableDefinitionStaleError
	require.True(t, errors.As(kindErr.Cause(), &stale), "cause = %v", kindErr.Cause())
}

// TestGenerationPin_ReplacedTableOutlivesNoPin proves a table dropped and
// recreated under the same name -- a new TableID -- is not mistaken for the
// one the statement resolved.
func TestGenerationPin_ReplacedTableOutlivesNoPin(t *testing.T) {
	h, tbl, _, clk := resolvedOrders(t, 1)
	require.NoError(t, h.tableSvc.DropTable(context.Background(), &service.DropTableRequest{Database: "shop", Name: "orders"}))
	_, err := h.tableSvc.CreateTable(context.Background(), ordersCreateRequest())
	require.NoError(t, err)

	clk.t = clk.t.Add(service.TableGenerationPinTTL)
	_, err = scanAll(newTestContext(), tbl)
	var kindErr *goerrors.Error
	require.True(t, errors.As(err, &kindErr), "err = %v", err)
	var stale *service.TableDefinitionStaleError
	require.True(t, errors.As(kindErr.Cause(), &stale))
	assert.Contains(t, stale.Error(), "replaced")
}

// TestGenerationPin_ReproofDuringARewriteReadsTheCommittedDefinition proves
// a catalog mid-rewrite -- where GetTable reports the rewrite, not a
// definition -- still lets a long statement re-prove its resolution through
// the rewrite journal's committed definition, rather than fail for an
// unrelated reason.
func TestGenerationPin_ReproofDuringARewriteReadsTheCommittedDefinition(t *testing.T) {
	h, tbl, spy, clk := resolvedOrders(t, 2)
	_, err := h.tableSvc.AlterTable(context.Background(), &service.AlterTableRequest{
		Database: "shop", Name: "orders",
		Changes: []service.AlterTableChange{{Action: service.AlterDropColumn, Name: "qty"}},
	})
	var rewrite *service.TableRewriteRequiredError
	require.ErrorAs(t, err, &rewrite, "precondition: a rewrite journal is now in place")

	clk.t = clk.t.Add(service.TableGenerationPinTTL)
	rows, err := scanAll(newTestContext(), tbl)
	require.NoError(t, err)
	assert.Len(t, rows, 2)
	assert.Equal(t, 1, spy.gets, "the journal fallback re-proves the generation with one read")
}

// TestGenerationPin_ReproofDuringARewriteSeesADroppedIndex is B1's reader
// half: the mid-rewrite fallback once re-proved only the generation, so a
// statement that resolved an index dropped just before the rewrite began
// kept reading it past its pin, while the drop's reclaim job deleted it. The
// fallback proves the index incarnations too.
func TestGenerationPin_ReproofDuringARewriteSeesADroppedIndex(t *testing.T) {
	h, tbl, _, clk := resolvedOrders(t, 2)
	require.NoError(t, h.tableSvc.DropIndex(context.Background(), "shop", "orders", "idx_warehouse"))
	_, err := h.tableSvc.AlterTable(context.Background(), &service.AlterTableRequest{
		Database: "shop", Name: "orders",
		Changes: []service.AlterTableChange{{Action: service.AlterDropColumn, Name: "qty"}},
	})
	var rewrite *service.TableRewriteRequiredError
	require.ErrorAs(t, err, &rewrite, "precondition: a rewrite journal is now in place")

	clk.t = clk.t.Add(service.TableGenerationPinTTL)
	_, err = scanAll(newTestContext(), tbl)
	stale := requireDefinitionStale(t, err)
	assert.Contains(t, stale.Error(), "idx_warehouse")
}

// TestGenerationFencing_Buffered_FirstTouchAfterTruncateIsRefused is the
// snapshot half the per-statement pin cannot see: the transaction's snapshot
// is bound by ANOTHER table, a TRUNCATE commits elsewhere, and only then does
// the transaction first touch the truncated table. Its fresh resolution
// names the new generation, which the older snapshot holds no rows for; the
// statement must fail typed rather than return an empty result.
func TestGenerationFencing_Buffered_FirstTouchAfterTruncateIsRefused(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	connA := newTxConn(t, prov, "shop")
	connA.mustExec(t, "CREATE TABLE t1 (id BIGINT UNSIGNED PRIMARY KEY)")
	connA.mustExec(t, "CREATE TABLE t2 (id BIGINT UNSIGNED PRIMARY KEY)")
	connA.mustExec(t, "INSERT INTO t1 (id) VALUES (1)")
	connA.mustExec(t, "INSERT INTO t2 (id) VALUES (1)")

	connA.mustExec(t, "START TRANSACTION READ ONLY")
	_, rows := connA.mustExec(t, "SELECT id FROM t2")
	require.Len(t, rows, 1)

	connB := newTxConn(t, prov, "shop")
	connB.mustExec(t, "TRUNCATE TABLE t1")

	_, rows, err := connA.exec("SELECT id FROM t1")
	assert.Empty(t, rows)
	requireLockDeadlockStale(t, err)
}

// TestGenerationFencing_Buffered_FirstTouchAfterDDLIsRefused is the same gap
// for a definition change: an index created after the snapshot has no
// entries in it, so a first touch of the table must refuse rather than read
// an index as empty.
func TestGenerationFencing_Buffered_FirstTouchAfterDDLIsRefused(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	connA := newTxConn(t, prov, "shop")
	connA.mustExec(t, "CREATE TABLE t1 (id BIGINT UNSIGNED PRIMARY KEY, email VARCHAR(64))")
	connA.mustExec(t, "CREATE TABLE t2 (id BIGINT UNSIGNED PRIMARY KEY)")
	connA.mustExec(t, "INSERT INTO t1 (id, email) VALUES (1, 'a@example.com')")
	connA.mustExec(t, "INSERT INTO t2 (id) VALUES (1)")

	connA.mustExec(t, "START TRANSACTION READ ONLY")
	connA.mustExec(t, "SELECT id FROM t2")

	connB := newTxConn(t, prov, "shop")
	connB.mustExec(t, "CREATE UNIQUE INDEX e ON t1 (email)")

	_, _, err := connA.exec("SELECT id FROM t1 WHERE email = 'a@example.com'")
	require.Error(t, err)
	assert.True(t, sqle.ErrLockDeadlock.Is(err), "err = %v", err)
	var kindErr *goerrors.Error
	require.True(t, errors.As(err, &kindErr))
	var stale *service.TableDefinitionStaleError
	require.True(t, errors.As(kindErr.Cause(), &stale), "cause = %v", kindErr.Cause())
}

// requireDefinitionStale asserts err is the retryable definition-change
// error.
func requireDefinitionStale(t *testing.T, err error) *service.TableDefinitionStaleError {
	t.Helper()
	require.Error(t, err)
	assert.True(t, sqle.ErrLockDeadlock.Is(err), "err = %v (%T) must map to sql.ErrLockDeadlock", err, err)
	var kindErr *goerrors.Error
	require.True(t, errors.As(err, &kindErr), "err = %v (%T)", err, err)
	var stale *service.TableDefinitionStaleError
	require.True(t, errors.As(kindErr.Cause(), &stale), "cause = %v (%T)", kindErr.Cause(), kindErr.Cause())
	return stale
}

// TestDefinitionFence_InsertResolvedBeforeCreateIndexIsRefused is the index
// maintenance race: an INSERT planned before CREATE INDEX commits after it.
// Its index maintenance was computed for the old index set, so committing
// would leave the new index permanently without this row's entry. The
// definition fence refuses it instead.
func TestDefinitionFence_InsertResolvedBeforeCreateIndexIsRefused(t *testing.T) {
	h := newDDLHarness()
	_, def := newWriterHarnessTable(t, h, ordersCreateRequest())
	resolved, err := h.tableSvc.GetTable(context.Background(), def.Database, def.Name)
	require.NoError(t, err)
	stale, err := NewRelationalTable(resolved, h.kvSvc, h.mvccSvc,
		WithTableService(h.tableSvc), WithTransactionService(h.txnSvc), WithSequenceService(h.seqSvc))
	require.NoError(t, err)

	require.NoError(t, h.tableSvc.CreateIndex(context.Background(), &service.CreateTableIndexRequest{
		Database: def.Database, Table: def.Name,
		Index: service.TableIndex{Name: "idx_qty", Columns: []service.IndexCol{{Name: "qty"}}},
	}))

	err = stale.Inserter(newTestContext()).Insert(newTestContext(), orderRow(1, "SKU-1", "east", int32(5)))
	requireDefinitionStale(t, err)
	rows, err := h.kvSvc.Scan(context.Background(), service.TableDataPrefix(def.Database, def.TableID, 0))
	require.NoError(t, err)
	assert.Empty(t, rows, "the refused INSERT must not have committed")
}

// TestDefinitionFence_UpdateAndDeleteResolvedBeforeDDLAreRefused covers the
// other two DML paths the same way, against a DROP INDEX: an UPDATE or
// DELETE planned against the old index set would maintain an index whose
// entries are being reclaimed.
func TestDefinitionFence_UpdateAndDeleteResolvedBeforeDDLAreRefused(t *testing.T) {
	h := newDDLHarness()
	writer, def := newWriterHarnessTable(t, h, ordersCreateRequest())
	require.NoError(t, writer.Inserter(newTestContext()).Insert(newTestContext(), orderRow(1, "SKU-1", "east", int32(5))))
	resolved, err := h.tableSvc.GetTable(context.Background(), def.Database, def.Name)
	require.NoError(t, err)
	stale, err := NewRelationalTable(resolved, h.kvSvc, h.mvccSvc,
		WithTableService(h.tableSvc), WithTransactionService(h.txnSvc), WithSequenceService(h.seqSvc))
	require.NoError(t, err)

	require.NoError(t, h.tableSvc.DropIndex(context.Background(), def.Database, def.Name, "idx_warehouse"))

	err = stale.Updater(newTestContext()).Update(newTestContext(),
		orderRow(1, "SKU-1", "east", int32(5)), orderRow(1, "SKU-1", "west", int32(5)))
	requireDefinitionStale(t, err)
	err = stale.Deleter(newTestContext()).Delete(newTestContext(), orderRow(1, "SKU-1", "east", int32(5)))
	requireDefinitionStale(t, err)
}

// TestDefinitionFence_BufferedCommitAfterDDLIsRefused proves a buffered
// transaction's commit carries the same fence: DDL that commits while it is
// open fails its COMMIT, reported as the definition change.
func TestDefinitionFence_BufferedCommitAfterDDLIsRefused(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	connA := newTxConn(t, prov, "shop")
	connA.mustExec(t, "CREATE TABLE widgets (id BIGINT UNSIGNED PRIMARY KEY, name VARCHAR(64))")

	connA.mustExec(t, "BEGIN")
	connA.mustExec(t, "INSERT INTO widgets (id, name) VALUES (1, 'a')")

	connB := newTxConn(t, prov, "shop")
	connB.mustExec(t, "CREATE INDEX n ON widgets (name)")

	_, _, err := connA.exec("COMMIT")
	requireDefinitionStale(t, err)
}

// TestGenerationState_TruncateIsRaceFreeWithReaders is run under -race: the
// generation, row prefix and pin a handle uses are published as one value,
// so readers on other goroutines never observe a TRUNCATE on the same handle
// half-applied.
func TestGenerationState_TruncateIsRaceFreeWithReaders(t *testing.T) {
	_, tbl, _, _ := resolvedOrders(t, 1)
	tbl.now = time.Now
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 200 {
			g := tbl.gen()
			assert.Equal(t, service.TableDataPrefix(tbl.def.Database, tbl.def.TableID, g.generation), g.dataPrefix,
				"a generation and its row prefix must never be observed apart")
		}
	}()
	for range 5 {
		_, err := tbl.Truncate(newTestContext())
		require.NoError(t, err)
	}
	<-done
	assert.Equal(t, uint64(5), tbl.gen().generation)
}

// TestGenerationFencing_Buffered_HandleOlderThanTheSnapshotIsRefused is the
// case the revision comparison cannot see (go-qrdb#547 N4): a handle
// resolved BEFORE a TRUNCATE, first touched by a transaction whose snapshot
// was bound AFTER it. The handle's generation revision is below the
// snapshot's, yet the snapshot holds the new generation, not the handle's.
// The first registration compares the generations themselves and refuses.
func TestGenerationFencing_Buffered_HandleOlderThanTheSnapshotIsRefused(t *testing.T) {
	h, tbl, _, _ := resolvedOrders(t, 1)
	_, err := h.tableSvc.TruncateTable(context.Background(), "shop", "orders", true)
	require.NoError(t, err)

	ctx := txContext()
	_, err = ctx.Session.(*QRDBSession).StartTransaction(ctx, sqle.ReadWrite)
	require.NoError(t, err)

	_, err = tbl.activeWriteSet(ctx)
	stale := requireLockDeadlockStale(t, err)
	assert.Equal(t, uint64(1), stale.Expected, "the generation the snapshot holds")
	assert.Equal(t, uint64(0), stale.Actual, "the generation the handle resolved")
}

// TestGenerationFencing_Buffered_CurrentHandleRegisters is its positive
// control: a handle at the snapshot's generation registers.
func TestGenerationFencing_Buffered_CurrentHandleRegisters(t *testing.T) {
	_, tbl, _, _ := resolvedOrders(t, 1)
	ctx := txContext()
	_, err := ctx.Session.(*QRDBSession).StartTransaction(ctx, sqle.ReadWrite)
	require.NoError(t, err)

	ws, err := tbl.activeWriteSet(ctx)
	require.NoError(t, err)
	require.NotNil(t, ws)
}
