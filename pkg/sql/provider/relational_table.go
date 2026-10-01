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
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/vitess/go/mysql"
	"github.com/dolthub/vitess/go/sqltypes"
	goerrors "gopkg.in/src-d/go-errors.v1"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/rowlock"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/catalog"
)

// Compile-time interface assertions.
var (
	_ sqle.Table                 = (*RelationalTable)(nil)
	_ sqle.PrimaryKeyTable       = (*RelationalTable)(nil)
	_ sqle.StatisticsTable       = (*RelationalTable)(nil)
	_ sqle.IndexAddressableTable = (*RelationalTable)(nil)
	_ sqle.IndexedTable          = (*RelationalTable)(nil)
)

// RelationalTable implements the read path (sql.Table, sql.PrimaryKeyTable,
// sql.StatisticsTable, sql.IndexAddressableTable, sql.IndexedTable) for a
// user-defined relational table created through TableServicer -- the tables
// CREATE TABLE produces, as distinct from the KV/entity/collection tables
// the rest of this package serves. Insert/Update/Delete/Replace and DDL are
// deliberately not implemented here; see the package's later phases.
//
// Rows live under service.TableDataPrefix(def.Database, def.TableID),
// encoded with catalog.RowCodec, and are keyed with catalog.KeyCodec.
// kvSvc and mvccSvc are expected to already be scoped to the caller's
// (tenant, database) namespace -- see Database.relationalTable, which
// resolves both through service.Registry -- so RelationalTable itself never
// composes a tenant/database prefix onto a key.
type RelationalTable struct {
	def      *service.TableDef
	schema   sqle.Schema
	pkSchema sqle.PrimaryKeySchema
	rowCodec *catalog.RowCodec
	keyCodec *catalog.KeyCodec

	kvSvc   service.KVServicer
	mvccSvc service.MVCCKVServicer // nil disables RowCount/DataLength

	// estimates is the row-count estimate cache RowCount and DataLength
	// answer from, and estimateStore the (tenant, database) store this
	// table's estimates are kept under there. A table resolved through a
	// Database shares its provider's cache (see withRowEstimates); one built
	// without it gets a cache of its own, so a statement still counts it at
	// most once.
	estimates     *rowEstimates
	estimateStore estimateStore

	// tableSvc is nil unless supplied via WithTableService. It is required
	// for every DDL capability implemented in relational_table_ddl.go
	// (AlterableTable, IndexAlterableTable, IndexBuildingTable,
	// RewritableTable) but not for the read path in this file, which is why
	// it is optional here rather than a required constructor parameter --
	// see the RelationalTableOption doc comment for why.
	tableSvc service.TableServicer

	// txnSvc is nil unless supplied via WithTransactionService. It is
	// required for every DML capability in relational_writer.go
	// (Insert/Update/Delete/Replace) -- see that file's doc comments for why
	// row writes and their secondary-index maintenance are composed into a
	// single TransactionServicer.Execute call.
	txnSvc service.TransactionServicer

	// seqSvc is nil unless supplied via WithSequenceService. It backs
	// AUTO_INCREMENT value generation (relational_autoincrement.go).
	seqSvc service.SequenceServicer

	// indexSpecs resolves every secondary index in def to its columns'
	// schema ordinals, its uniqueness, and its value KeyCodecs, computed
	// once at construction and shared by the planner-facing surface
	// (GetIndexes), the read path (LookupPartitions) and the write path
	// (relational_writer.go) so all three agree on which indexes exist,
	// which columns each covers, and what physical entry keys they derive.
	// See buildIndexSpecs.
	indexSpecs []indexSpec

	// autoInc is the mutable block-allocation cursor backing
	// GetNextAutoIncrementValue. Always non-nil, even for a table with no
	// AUTO_INCREMENT column, since allocating the (unused) struct is
	// cheaper than nil-checking it everywhere.
	autoInc *sequenceBlock

	// firstAutoInc is the first AUTO_INCREMENT value this table instance
	// generated, or zero if it has generated none. It is what
	// publishLastInsertID reports as the statement's LAST_INSERT_ID, and it
	// is scoped to the statement for exactly the same reason autoInc's
	// block cursor is: Database.GetTableInsensitive builds a fresh
	// RelationalTable per table resolution, and go-mysql-server re-resolves
	// (re-analyzes) every statement, including every execution of a
	// prepared one. See relational_autoincrement.go.
	firstAutoInc atomic.Uint64

	// rowIDs is the equivalent cursor backing the hidden row id of a table
	// with no declared primary key (see relational_rowid.go). It is a
	// separate cursor over a separate sequence because a keyless table may
	// also declare an AUTO_INCREMENT column, and the two counters are
	// independent.
	rowIDs *sequenceBlock

	// pkSpec resolves the table's DECLARED primary key -- of any arity --
	// in the same shape as a secondary index, and hasPK reports whether
	// there is one at all (a table with no declared primary key is keyed by
	// a synthetic hidden row id that is never part of the SQL-visible
	// schema, so there is no primary key index to offer the planner).
	//
	// Unlike a secondary index, the primary key needs no separate physical
	// entries: a row's storage key IS its encoded primary key (see
	// catalog.KeyCodec), so a whole-tuple match is a key the caller can read
	// directly and a leading-prefix match is a bounded prefix scan over the
	// table's own data prefix. See primaryKeysFromRanges.
	pkSpec indexSpec
	hasPK  bool

	// state is the generation this handle reads and writes, the row prefix
	// derived from it, and when it was last known current (see
	// tableGeneration). It is one immutable value behind one atomic pointer:
	// TRUNCATE on this handle, and a pin renewal, publish a new value
	// rather than mutating fields a concurrent read could see half-updated.
	// def.Generation is only the value the handle was resolved with.
	state atomic.Pointer[tableGeneration]

	// now is the clock pins are measured with; time.Now outside tests. Its
	// readings carry a monotonic component, so a pin's age never depends on
	// wall-clock adjustments.
	now func() time.Time

	// postgres is true when this table is served over the PostgreSQL wire
	// protocol (see WithPostgreSQLDialect / Database.postgres). It governs
	// only Truncate's AUTO_INCREMENT/sequence behavior; every other
	// capability in this file is dialect-agnostic. See Truncate for why.
	postgres bool

	// rowLocks resolves the lock table this table's rows are locked in --
	// only when a statement first needs a lock, so a statement that takes
	// none never resolves the lease and watch services behind it -- and
	// defaultConcurrency is the database's configured concurrency mode. See
	// WithRowLocks and row_locks.go.
	rowLocks           func() (*rowlock.Table, error)
	defaultConcurrency database.Concurrency
}

// RelationalTableOption configures optional RelationalTable dependencies
// that not every caller needs -- today, exclusively TableServicer, required
// for DDL (see the tableSvc field) but not for the plain read path most
// callers (and most of this package's existing tests) only exercise. Using
// a variadic functional option here, rather than adding a required
// parameter to NewRelationalTable, keeps every existing call site
// source-compatible: a Go variadic parameter accepts zero arguments without
// any change at the call site.
type RelationalTableOption func(*RelationalTable)

// WithTableService returns a RelationalTableOption that supplies the
// TableServicer backing this table's catalog -- required for every DDL
// capability in relational_table_ddl.go (ALTER/CREATE INDEX/DROP INDEX/
// RENAME INDEX and the DROP/MODIFY COLUMN rewrite path), scoped to the same
// (tenant, database) namespace as kvSvc/mvccSvc.
func WithTableService(svc service.TableServicer) RelationalTableOption {
	return func(t *RelationalTable) {
		t.tableSvc = svc
	}
}

// WithTransactionService returns a RelationalTableOption that supplies the
// TransactionServicer backing this table's DML (Insert/Update/Delete/
// Replace) in relational_writer.go -- required for every write capability,
// but not for the read path or DDL, which is why it is optional here rather
// than a required constructor parameter.
func WithTransactionService(svc service.TransactionServicer) RelationalTableOption {
	return func(t *RelationalTable) {
		t.txnSvc = svc
	}
}

// WithSequenceService returns a RelationalTableOption that supplies the
// SequenceServicer backing this table's AUTO_INCREMENT column
// (relational_autoincrement.go).
func WithSequenceService(svc service.SequenceServicer) RelationalTableOption {
	return func(t *RelationalTable) {
		t.seqSvc = svc
	}
}

// withRowEstimates shares the provider's row-count estimate cache with the
// table, keeping its estimates under store -- the (tenant, database) store
// the table's rows live in.
func withRowEstimates(estimates *rowEstimates, store estimateStore) RelationalTableOption {
	return func(t *RelationalTable) {
		t.estimates = estimates
		t.estimateStore = store
	}
}

// WithPostgreSQLDialect returns a RelationalTableOption marking the table as
// served over the PostgreSQL wire protocol rather than MySQL's, which
// changes exactly one thing: Truncate does not reset the table's
// AUTO_INCREMENT sequence (see Truncate).
func WithPostgreSQLDialect() RelationalTableOption {
	return func(t *RelationalTable) {
		t.postgres = true
	}
}

// NewRelationalTable creates a RelationalTable for def, backed by kvSvc for
// row access and mvccSvc for RowCount/DataLength. Both are expected to
// already be scoped to the caller's (tenant, database) namespace. opts
// configures optional dependencies; see RelationalTableOption.
func NewRelationalTable(def *service.TableDef, kvSvc service.KVServicer, mvccSvc service.MVCCKVServicer, opts ...RelationalTableOption) (*RelationalTable, error) {
	schema, err := catalog.SchemaOf(def)
	if err != nil {
		return nil, err
	}
	pkSchema, err := catalog.PrimaryKeySchemaOf(def)
	if err != nil {
		return nil, err
	}
	rowCodec, err := catalog.NewRowCodec(def)
	if err != nil {
		return nil, err
	}
	keyCodec, err := catalog.NewKeyCodec(def)
	if err != nil {
		return nil, err
	}
	indexSpecs, err := buildIndexSpecs(def, schema)
	if err != nil {
		return nil, err
	}

	t := &RelationalTable{
		def:        def,
		schema:     schema,
		pkSchema:   pkSchema,
		rowCodec:   rowCodec,
		keyCodec:   keyCodec,
		kvSvc:      kvSvc,
		mvccSvc:    mvccSvc,
		indexSpecs: indexSpecs,
		autoInc:    &sequenceBlock{},
		rowIDs:     &sequenceBlock{},
		now:        time.Now,
		estimates:  newRowEstimates(),
	}
	t.setGeneration(def.Generation, def.GenerationReadAt)

	if len(def.PrimaryKey) > 0 {
		pkSpec, perr := buildIndexSpec(def, schema, primaryKeyIndexOf(def))
		if perr != nil && !isIndexResolutionError(perr) {
			return nil, perr
		}
		if perr == nil {
			t.pkSpec, t.hasPK = pkSpec, true
		}
	}

	for _, opt := range opts {
		opt(t)
	}

	return t, nil
}

// Name returns the table name.
func (t *RelationalTable) Name() string {
	return t.def.Name
}

// String returns a human-readable description of the table.
func (t *RelationalTable) String() string {
	return t.def.Name
}

// Schema returns the table's column schema.
func (t *RelationalTable) Schema(_ *sqle.Context) sqle.Schema {
	return t.schema
}

// Collation returns the table's default collation, falling back to
// sqle.Collation_Default when def.Collation is empty or unrecognized.
func (t *RelationalTable) Collation() sqle.CollationID {
	if t.def.Collation == "" {
		return sqle.Collation_Default
	}
	if id, err := sqle.ParseCollation("", t.def.Collation, false); err == nil {
		return id
	}
	return sqle.Collation_Default
}

// Partitions returns an iterator over the table's partitions. A full
// (unfiltered) scan uses a single partition.
func (t *RelationalTable) Partitions(_ *sqle.Context) (sqle.PartitionIter, error) {
	return sqle.PartitionsToPartitionIter(&relationalPartition{}), nil
}

// PartitionRows returns an iterator over the rows in the given partition:
// a full scan for a relationalPartition, the pre-resolved primary-key point
// lookups for an indexedPartition, or the pre-filtered rows a secondary (or
// composite-primary-key) LookupPartitions call already computed for a
// filteredPartition.
func (t *RelationalTable) PartitionRows(ctx *sqle.Context, part sqle.Partition) (sqle.RowIter, error) {
	switch p := part.(type) {
	case *indexedPartition:
		return t.indexedPartitionRows(ctx, p)
	case *filteredPartition:
		return sqle.RowsToRowIter(p.rows...), nil
	default:
		return t.fullScanRows(ctx)
	}
}

// fullScanRows performs a full prefix scan over the table's data prefix and
// decodes every row. KVServicer.Scan makes no ordering guarantee of its
// own (see kv_table.go's fullScanRows), so the scanned keys are sorted
// client-side before decoding; because catalog.KeyCodec is order-preserving,
// that lexicographic sort is exactly primary key order.
//
// A locking read (see readLock) locks every row the scan returns -- the
// engine filters above the table, so the table cannot tell which rows the
// predicate keeps -- and returns them at their latest committed version.
func (t *RelationalTable) fullScanRows(ctx *sqle.Context) (sqle.RowIter, error) {
	g := t.gen()
	req, err := t.readLock(ctx)
	if err != nil {
		return nil, err
	}
	var data map[string][]byte
	if req.mode != 0 {
		data, err = t.lockingScan(ctx, g.dataPrefix, req)
	} else {
		data, err = t.txScan(ctx, g.dataPrefix)
	}
	if err != nil {
		return nil, err
	}

	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	rows := make([]sqle.Row, 0, len(keys))
	for _, k := range keys {
		row, derr := t.decodeRow(k[len(g.dataPrefix):], data[k])
		if derr != nil {
			return nil, derr
		}
		rows = append(rows, row)
	}
	return sqle.RowsToRowIter(rows...), nil
}

// indexedPartitionRows fetches only the rows whose primary keys are held in
// the indexed partition, performing one KV point read per key -- the
// accelerated path LookupPartitions builds for an exact-match primary key
// predicate.
func (t *RelationalTable) indexedPartitionRows(ctx *sqle.Context, ip *indexedPartition) (sqle.RowIter, error) {
	g := t.gen()
	req, err := t.readLock(ctx)
	if err != nil {
		return nil, err
	}
	if req.mode != 0 {
		return t.lockedPartitionRows(ctx, g, ip, req)
	}
	rows := make([]sqle.Row, 0, len(ip.keys))
	for _, key := range ip.keys {
		raw, err := t.txGet(ctx, g.dataPrefix+string(key))
		if err != nil {
			if isNotFound(err) {
				// Deleted after the index lookup; skip it.
				continue
			}
			// Anything else -- a failed read, or the typed stale-generation
			// error -- is not "no such row" and must not be reported as one.
			return nil, err
		}
		row, err := t.decodeRow(string(key), raw)
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	return &indexedRowIter{rows: rows}, nil
}

// lockedPartitionRows is indexedPartitionRows for a locking read: every
// looked-up key is locked, then read at its latest committed version.
func (t *RelationalTable) lockedPartitionRows(ctx *sqle.Context, g *tableGeneration, ip *indexedPartition, req lockRequest) (sqle.RowIter, error) {
	keys := make([]string, len(ip.keys))
	for i, k := range ip.keys {
		keys[i] = g.dataPrefix + string(k)
	}
	values, err := t.lockAndRead(ctx, keys, req)
	if err != nil {
		return nil, err
	}
	rows := make([]sqle.Row, 0, len(keys))
	for i, k := range keys {
		raw, ok := values[k]
		if !ok {
			continue
		}
		row, derr := t.decodeRow(string(ip.keys[i]), raw)
		if derr != nil {
			return nil, derr
		}
		rows = append(rows, row)
	}
	return &indexedRowIter{rows: rows}, nil
}

// ---------------------------------------------------------------------------
// PrimaryKeyTable
// ---------------------------------------------------------------------------

// PrimaryKeySchema returns the table's primary key schema.
func (t *RelationalTable) PrimaryKeySchema(_ *sqle.Context) sqle.PrimaryKeySchema {
	return t.pkSchema
}

// ---------------------------------------------------------------------------
// StatisticsTable
// ---------------------------------------------------------------------------

// RowCount returns the planner's estimate of the table's row count, and
// reports it as NOT exact.
//
// go-mysql-server calls this several times while it plans every statement
// (index costing, table-scan statistics, DataLength) and once per table for
// information_schema.tables, so it must not cost time proportional to the
// table: the value comes from the provider's per-generation estimate cache
// (see row_estimate.go), counted with a local read rather than a
// linearizable one and refreshed in the background.
//
// Reporting it as inexact is what keeps COUNT(*) correct. go-mysql-server's
// replaceCountStar rule substitutes this value for a COUNT(*) only when the
// exact flag is set (sql/analyzer/replace_count_star.go: `err == nil &&
// exact`); with it clear, COUNT(*) is answered by the ordinary scan path,
// which reads the latest committed rows -- and, inside a transaction, the
// write-set overlay with the transaction's own uncommitted rows.
func (t *RelationalTable) RowCount(ctx *sqle.Context) (uint64, bool, error) {
	n, err := t.rowEstimate(ctx)
	return n, false, err
}

// DataLength estimates the table's on-disk size as its estimated row count
// (see RowCount) multiplied by the encoded size of a synthetic row built from
// every column's zero value -- a reasonable proxy in the absence of a
// maintained running total, since QRDB's row encoding (catalog.RowCodec) is
// otherwise fixed-width per column except for variable-length
// string/binary/JSON columns, which zero to their minimum (empty) encoding
// here.
func (t *RelationalTable) DataLength(ctx *sqle.Context) (uint64, error) {
	count, err := t.rowEstimate(ctx)
	if err != nil {
		return 0, err
	}
	return count * uint64(t.averageRowSize()), nil
}

// statsReadOptions is the consistency a row-count estimate is read at: a
// local read of this node's state machine, with no Raft round trip. An
// estimate a follower counts a few entries behind the leader is still an
// estimate.
var statsReadOptions = consistency.ReadOptions{Consistency: consistency.Eventual, AllowStale: true}

// rowEstimate returns the estimated row count of the handle's current
// generation from the estimate cache, counting it on a miss.
func (t *RelationalTable) rowEstimate(ctx *sqle.Context) (uint64, error) {
	if t.mvccSvc == nil {
		return 0, &RelationalMVCCUnavailableError{Table: t.def.Name}
	}
	g := t.gen()
	key := rowEstimateKey{store: t.estimateStore, tableID: t.def.TableID, generation: g.generation}
	return t.estimates.estimate(providerContext(ctx), key, func(rctx context.Context) (uint64, error) {
		prefix := []byte(g.dataPrefix)
		_, count, _, err := t.mvccSvc.RangeWithConsistency(rctx, prefix, prefixEnd(prefix), statsReadOptions, service.WithCountOnly())
		if err != nil {
			return 0, err
		}
		return uint64(max(count, 0)), nil
	})
}

// averageRowSize returns the encoded size of a synthetic all-zero-value row,
// used by DataLength as a stand-in for the table's true average row size.
func (t *RelationalTable) averageRowSize() int {
	row := make(sqle.Row, len(t.schema))
	for i, col := range t.schema {
		row[i] = col.Type.Zero()
	}
	return t.rowCodec.EncodedSize(row)
}

// ---------------------------------------------------------------------------
// IndexAddressableTable / IndexedTable
// ---------------------------------------------------------------------------

// primaryIndexName is the reserved index name MySQL gives a table's primary
// key, and the ID RelationalTable reports (and resolves) its own primary key
// index under. MySQL forbids a user-declared index from taking this name, so
// it can never collide with a secondary index's catalog name.
const primaryIndexName = "PRIMARY"

// GetIndexes returns the table's declared primary key and every secondary
// index declared in the table definition, so the query planner can consider
// all of them for WHERE clause acceleration.
//
// Both are reported from the resolved indexSpecs rather than re-derived from
// def, so the planner is offered exactly the indexes this table's read and
// write paths can actually serve -- and, for a multi-column index, every one
// of its columns in declared order. That column list is what
// go-mysql-server turns into information_schema.STATISTICS rows (one per
// column, numbered by SEQ_IN_INDEX) and, from those, into
// information_schema.COLUMNS' COLUMN_KEY: reporting only the leading column
// of a composite UNIQUE index is what makes MySQL's 'MUL' show up as 'UNI'.
// It is also what go-mysql-server matches a FOREIGN KEY's referenced
// columns against, so a composite primary key that reported fewer columns
// than it has could not be the parent of a composite foreign key at all
// (plan.ResolveForeignKey rejects it with ErrForeignKeyMissingReferenceIndex).
func (t *RelationalTable) GetIndexes(_ *sqle.Context) ([]sqle.Index, error) {
	indexes := make([]sqle.Index, 0, len(t.indexSpecs)+1)
	if t.hasPK {
		indexes = append(indexes, newRelationalTableIndex(
			t.def.Database, t.def.Name, primaryIndexName, t.pkSpec.indexColumns(), true, ""))
	}
	for _, spec := range t.indexSpecs {
		indexes = append(indexes, newRelationalTableIndex(
			t.def.Database, t.def.Name, spec.name, spec.indexColumns(), spec.unique, spec.comment))
	}
	return indexes, nil
}

// IndexedAccess returns the table itself as an IndexedTable for the given
// lookup. RelationalTable serves as both IndexAddressable and IndexedTable
// since it implements LookupPartitions.
func (t *RelationalTable) IndexedAccess(_ *sqle.Context, _ sqle.IndexLookup) sqle.IndexedTable {
	return t
}

// PreciseMatch reports true: LookupPartitions always answers the exact
// pushed-down predicate, either by an accelerated primary-key point lookup
// or by scanning every row and re-evaluating the same range bounds the
// planner computed (see filteredScanRows), never by returning a superset the
// caller would need to re-filter.
func (t *RelationalTable) PreciseMatch() bool { return true }

// LookupPartitions answers an index-accelerated predicate. An exact-match
// predicate on the table's single-column primary key (WHERE pk = ?, or a
// planner-expanded IN-list of OR'd equalities) is answered with one KV point
// read per value, since the primary key is the row's own storage key (see
// catalog.KeyCodec) -- no separate index structure is needed. An exact-match
// predicate on a secondary index -- on the whole index, or on any leading
// prefix of its columns -- is answered from that index's physical entries
// (see indexSpec, secondaryIndexKeysFromRanges): a whole-tuple match on a
// unique index with one KV point read, anything else with one bounded prefix
// scan per distinct value. Every other shape -- a range predicate, a
// predicate that leaves a middle index column unbound, or a predicate on a
// column with no secondary index -- is answered by scanning every row and
// evaluating the same range bounds the planner computed, via each column's
// own sql.Type.Compare (filteredScanRows), so the returned rows are always
// exactly correct even though that path is not accelerated.
func (t *RelationalTable) LookupPartitions(ctx *sqle.Context, lookup sqle.IndexLookup) (sqle.PartitionIter, error) {
	if lookup.IsEmpty() {
		return t.Partitions(ctx)
	}
	qIdx, ok := lookup.Index.(*qrdbIndex)
	if !ok {
		return t.Partitions(ctx)
	}

	ranges := lookup.Ranges.ToRanges()

	ordinals, keys, exact, err := t.lookupKeys(ctx, qIdx, ranges)
	if err != nil {
		return nil, lookupFailure(qIdx.id, err)
	}
	if exact {
		return sqle.PartitionsToPartitionIter(&indexedPartition{keys: keys}), nil
	}
	if ordinals == nil {
		// The lookup names an index this table cannot resolve, so there is
		// no column set to evaluate the pushed-down ranges against. This is
		// unreachable for an index GetIndexes reported (every one of those
		// is either the primary key or an indexSpec resolved against this
		// same schema); it is kept as the safe answer for a lookup that
		// somehow arrives for a different table's index.
		return t.Partitions(ctx)
	}

	rows, err := t.filteredScanRows(ctx, ordinals, ranges)
	if err != nil {
		return nil, lookupFailure(qIdx.id, err)
	}
	return sqle.PartitionsToPartitionIter(&filteredPartition{rows: rows}), nil
}

// lookupFailure reports a failed index lookup on index. An error that
// already carries the classification a client acts on -- a go-mysql-server
// error kind such as sql.ErrLockDeadlock (MySQL 1213, "retry"), or a MySQL
// error with its own number (1205, 3572) -- is returned as it is: both are
// recognised by a type assertion on the OUTERMOST error, so wrapping one in
// *IndexLookupError would turn a retryable serialization failure or a
// lock-wait timeout into an unclassified failure. The test is on the
// outermost error for the same reason: one already buried under an
// unclassified wrapper reaches no client as classified, so wrapping it
// hides nothing. Anything else is wrapped.
func lookupFailure(index string, err error) error {
	switch err.(type) { //nolint:errorlint // the client tests the outermost error only; see above
	case *goerrors.Error, *mysql.SQLError:
		return err
	}
	return &IndexLookupError{IndexName: index, Err: err}
}

// lookupKeys resolves qIdx back to the table's primary key or to one of its
// indexSpecs and attempts an accelerated key derivation for ranges. It
// returns the schema ordinals of the resolved index's columns (nil when the
// index cannot be resolved at all), the derived primary keys, and whether
// those keys answer ranges exactly. When exact is false the caller must fall
// back to filteredScanRows over the returned ordinals.
func (t *RelationalTable) lookupKeys(ctx *sqle.Context, qIdx *qrdbIndex, ranges []sqle.Range) (ordinals []int, keys [][]byte, exact bool, err error) {
	if qIdx.id == primaryIndexName && t.hasPK {
		keys, exact, err = t.primaryKeysFromRanges(ctx, ranges)
		return t.pkSpec.ordinals(), keys, exact, err
	}
	spec, found := t.indexSpecByName(qIdx.id)
	if !found {
		return nil, nil, false, nil
	}
	keys, exact, err = t.secondaryIndexKeysFromRanges(ctx, spec, ranges)
	return spec.ordinals(), keys, exact, err
}

// primaryKeysFromRanges returns the storage keys of every row matching one
// of ranges, resolved without any separate index structure: a row's storage
// key IS its encoded primary key.
//
// A range that binds every primary key column to a point value (WHERE pk =
// ?, each arm of a planner-expanded IN-list, or the whole of a composite
// key) yields that key directly. A range that binds only a LEADING PREFIX of
// a composite key is answered by a bounded prefix scan over the table's own
// data prefix, which is exactly the set of rows sharing that prefix because
// catalog.KeyCodec's component encodings are self-delimiting and
// order-preserving. Anything else -- a genuine range bound, or a bound
// column sitting after an unbound one -- reports exact false so the caller
// falls back to filteredScanRows. See indexSpec.pointPrefix.
func (t *RelationalTable) primaryKeysFromRanges(ctx *sqle.Context, ranges []sqle.Range) (keys [][]byte, exact bool, err error) {
	g := t.gen()
	seen := make(map[string]struct{}, len(ranges))
	add := func(key string) {
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		keys = append(keys, []byte(key))
	}

	for _, r := range ranges {
		encoded, bound, ok, perr := t.pkSpec.pointPrefix(ctx, r)
		if perr != nil {
			return nil, false, perr
		}
		if !ok {
			return nil, false, nil
		}
		if bound == len(t.pkSpec.columns) {
			add(encoded)
			continue
		}

		data, serr := t.txScan(ctx, g.dataPrefix+encoded)
		if serr != nil {
			return nil, false, serr
		}
		scanned := make([]string, 0, len(data))
		for k := range data {
			scanned = append(scanned, k)
		}
		// KVServicer.Scan answers with an unordered map; sorting restores
		// primary key order, which is what a prefix scan of an
		// order-preserving key encoding means (and what MySQL returns).
		sort.Strings(scanned)
		for _, k := range scanned {
			add(k[len(g.dataPrefix):])
		}
	}
	return keys, true, nil
}

// indexSpecByName returns the indexSpec named name, matched
// case-insensitively as MySQL matches index names, and false when this
// table declares no such index.
func (t *RelationalTable) indexSpecByName(name string) (indexSpec, bool) {
	for _, spec := range t.indexSpecs {
		if strings.EqualFold(spec.name, name) {
			return spec, true
		}
	}
	return indexSpec{}, false
}

// secondaryIndexKeysFromRanges is the secondary-index equivalent of
// equalityKeysFromRanges: it returns the primary keys of every row matching
// one of ranges, resolved through spec's physical index entries (see
// indexEntryKey, lookupIndexValue) rather than range_translator.go's
// byte-string index encoding -- see the package's Phase 6 doc comments for
// why that path is not reused here.
//
// exact is false, signalling the caller to fall back to filteredScanRows,
// whenever a range is not a point match on a leading prefix of spec's
// columns (see indexSpec.pointPrefix for the exact conditions, including
// why a nullable unbound column disqualifies the index); in that case keys
// is always nil.
func (t *RelationalTable) secondaryIndexKeysFromRanges(ctx *sqle.Context, spec indexSpec, ranges []sqle.Range) (keys [][]byte, exact bool, err error) {
	seen := make(map[string]struct{}, len(ranges))
	for _, r := range ranges {
		encoded, bound, ok, perr := spec.pointPrefix(ctx, r)
		if perr != nil {
			return nil, false, perr
		}
		if !ok {
			return nil, false, nil
		}

		matched, merr := t.lookupIndexValue(ctx, spec, encoded, bound == len(spec.columns))
		if merr != nil {
			return nil, false, merr
		}
		for _, k := range matched {
			ks := string(k)
			if _, dup := seen[ks]; !dup {
				seen[ks] = struct{}{}
				keys = append(keys, k)
			}
		}
	}
	return keys, true, nil
}

// pointPrefix reports whether r binds a leading prefix of the index's
// columns to point (single-value) equalities, leaving every remaining
// column entirely unbound, and if so returns that prefix's encoded key and
// how many columns it covers.
//
// Three conditions make a range unusable, each returning ok false so the
// caller falls back to an exact filtered scan:
//
//   - A column with a prefix length. Its entries carry a truncated value,
//     so an entry match is not a value match (see indexSpec.prefixed).
//   - Anything other than a leading run of point equalities followed by
//     entirely-unbound columns: a genuine range bound, a NULL bound, or a
//     bound column sitting after an unbound one (WHERE a = 1 AND c = 3 over
//     an index on (a, b, c)), which no single prefix scan can answer.
//   - A NULLABLE column left unbound. A row with a NULL in any indexed
//     column has no index entry at all (see indexSpec.encode), so a prefix
//     scan that stops before such a column would silently omit exactly
//     those rows.
func (s indexSpec) pointPrefix(ctx *sqle.Context, r sqle.Range) (encoded string, bound int, ok bool, err error) {
	if s.prefixed {
		return "", 0, false, nil
	}
	mysqlRange, isMySQL := r.(sqle.MySQLRange)
	if !isMySQL || len(mysqlRange) != len(s.columns) {
		return "", 0, false, nil
	}

	vals := make([]any, 0, len(s.columns))
	for i, colExpr := range mysqlRange {
		if colExpr.Type() == sqle.RangeType_All {
			break
		}
		typed, pointOK, cerr := pointValue(ctx, s.columns[i].colType, colExpr)
		if cerr != nil {
			return "", 0, false, cerr
		}
		if !pointOK {
			return "", 0, false, nil
		}
		vals = append(vals, typed)
	}
	if len(vals) == 0 {
		return "", 0, false, nil
	}
	for _, rest := range mysqlRange[len(vals):] {
		if rest.Type() != sqle.RangeType_All {
			return "", 0, false, nil
		}
	}
	for _, c := range s.columns[len(vals):] {
		if c.nullable {
			return "", 0, false, nil
		}
	}

	encoded, err = s.codecs[len(vals)-1].EncodeValues(vals)
	if err != nil {
		return "", 0, false, err
	}
	return encoded, len(vals), true, nil
}

// pointValue reports whether colExpr is a degenerate closed range -- a
// single value, which is how the planner expresses both "col = ?" and each
// arm of an expanded IN-list -- and returns that value converted to t's own
// Go representation, ready for KeyCodec encoding. A NULL bound is reported
// as not a point value: NULL contributes no index entry and never satisfies
// an equality, so it must be answered by the filtered-scan path instead.
func pointValue(ctx *sqle.Context, t sqle.Type, colExpr sqle.MySQLRangeColumnExpr) (any, bool, error) {
	if colExpr.Type() != sqle.RangeType_ClosedClosed {
		return nil, false, nil
	}
	if !sqle.MySQLRangeCutIsBinding(colExpr.LowerBound) || !sqle.MySQLRangeCutIsBinding(colExpr.UpperBound) {
		return nil, false, nil
	}

	lower := sqle.GetMySQLRangeCutKey(colExpr.LowerBound)
	upper := sqle.GetMySQLRangeCutKey(colExpr.UpperBound)
	cmp, err := t.Compare(ctx, lower, upper)
	if err != nil {
		return nil, false, err
	}
	if cmp != 0 {
		// A genuine (non-degenerate) closed range, not a point match.
		return nil, false, nil
	}

	typed, _, err := t.Convert(ctx, lower)
	if err != nil {
		return nil, false, err
	}
	if typed == nil {
		return nil, false, nil
	}
	return typed, true, nil
}

// lookupIndexValue returns the primary keys of every row whose spec-indexed
// tuple begins with encodedVal, read from spec's physical index entries.
//
// wholeTuple selects between the two shapes: a unique index addressed by its
// complete tuple is a single KV point read, since that key can hold at most
// one entry (see indexEntryKey's doc comment for why a unique index needs no
// primary-key suffix). Everything else -- a non-unique index, or a
// leading-prefix lookup against either kind -- is a bounded prefix scan over
// exactly the entries whose tuple starts with encodedVal, made possible by
// every encoded component being self-delimiting (see catalog.KeyCodec /
// index.EncodeCompoundValue), so the scan can never spill into an entry
// whose value merely shares a byte prefix.
//
// A multi-entry result is returned in physical index-entry key order --
// indexed value, then primary key, both order-preserving encodings -- which
// is the order MySQL's own index scan yields. KVServicer.Scan answers with a
// map and so has no order of its own; returning its iteration order would
// make the row order of every multi-row indexed lookup differ from one
// execution of the same query to the next.
func (t *RelationalTable) lookupIndexValue(ctx *sqle.Context, spec indexSpec, encodedVal string, wholeTuple bool) ([][]byte, error) {
	g := t.gen()
	prefix := service.TableIndexPrefix(t.def.Database, t.def.TableID, g.generation, spec.incarnation)

	if spec.unique && wholeTuple {
		raw, err := t.txGet(ctx, prefix+encodedVal)
		if err != nil {
			if isNotFound(err) {
				return nil, nil
			}
			return nil, err
		}
		return [][]byte{append([]byte(nil), raw...)}, nil
	}

	entries, err := t.txScan(ctx, prefix+encodedVal)
	if err != nil {
		return nil, err
	}
	entryKeys := make([]string, 0, len(entries))
	for k := range entries {
		entryKeys = append(entryKeys, k)
	}
	sort.Strings(entryKeys)

	keys := make([][]byte, 0, len(entryKeys))
	for _, k := range entryKeys {
		keys = append(keys, append([]byte(nil), entries[k]...))
	}
	return keys, nil
}

// filteredScanRows scans every row of the table and keeps only those whose
// columns at ordinals satisfy ranges, using each column's own
// sql.Type.Compare against the exact bounds the query planner computed (see
// rowMatchesRanges). ordinals are the resolved index's columns in index
// order, which is the order the planner's per-column range expressions
// arrive in.
//
// This is the fallback used whenever an accelerated lookup is not available
// (a range predicate, a partially-bound composite index, or any predicate
// on a column with no usable index); it costs the same as a full table scan
// but, unlike a plain full scan, is safe to return directly to a caller that
// has already dropped the predicate as redundant (see PreciseMatch).
func (t *RelationalTable) filteredScanRows(ctx *sqle.Context, ordinals []int, ranges []sqle.Range) ([]sqle.Row, error) {
	g := t.gen()
	data, err := t.txScan(ctx, g.dataPrefix)
	if err != nil {
		return nil, err
	}

	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	colTypes := make([]sqle.Type, len(ordinals))
	for i, ordinal := range ordinals {
		colTypes[i] = t.schema[ordinal].Type
	}

	rows, matched, err := t.matchingRows(ctx, g, keys, data, colTypes, ordinals, ranges)
	if err != nil {
		return nil, err
	}

	// A locking read locks the rows the range selects, then re-reads them
	// at their latest committed version and applies the range again: a row
	// that changed out of it while the lock was awaited is not returned.
	req, err := t.readLock(ctx)
	if err != nil || req.mode == 0 {
		return rows, err
	}
	current, err := t.lockAndRead(ctx, matched, req)
	if err != nil {
		return nil, err
	}
	rows, _, err = t.matchingRows(ctx, g, matched, current, colTypes, ordinals, ranges)
	return rows, err
}

// matchingRows decodes data's rows at keys, in order, and keeps those whose
// columns at ordinals satisfy ranges, returning them and their keys. A key
// absent from data is skipped.
func (t *RelationalTable) matchingRows(ctx *sqle.Context, g *tableGeneration, keys []string, data map[string][]byte, colTypes []sqle.Type, ordinals []int, ranges []sqle.Range) ([]sqle.Row, []string, error) {
	var (
		rows    []sqle.Row
		matched []string
	)
	for _, k := range keys {
		raw, ok := data[k]
		if !ok {
			continue
		}
		row, derr := t.decodeRow(k[len(g.dataPrefix):], raw)
		if derr != nil {
			return nil, nil, derr
		}
		match, merr := rowMatchesRanges(ctx, colTypes, ordinals, row, ranges)
		if merr != nil {
			return nil, nil, merr
		}
		if match {
			rows = append(rows, row)
			matched = append(matched, k)
		}
	}
	return rows, matched, nil
}

// ---------------------------------------------------------------------------
// Interactive-transaction-aware KV access
//
// Every read and write path in this package that touches raw KV keys goes
// through activeWriteSet/txGet/txScan (or, on the write side,
// relational_writer.go's own use of activeWriteSet) rather than t.kvSvc
// directly, so it transparently participates in an open interactive
// transaction -- buffer first (read-your-own-writes), then the pinned
// snapshot -- without any behavior change for the overwhelming common case
// of an autocommit statement. See WriteSet's package doc comment for the
// isolation model.
// ---------------------------------------------------------------------------

// activeWriteSet returns the WriteSet backing ctx's open interactive
// transaction, bound to this table's (tenant, database) TransactionServicer
// and MVCCKVServicer, or nil when there is no such transaction to
// participate in.
//
// nil is returned -- not an error -- for every autocommit statement
// (ctx.GetIgnoreAutoCommit() false): go-mysql-server starts an implicit,
// per-statement transaction before every single query, not only after an
// explicit BEGIN/START TRANSACTION (see engine.beginTransaction), so
// gating on GetIgnoreAutoCommit rather than merely "a write-set exists" is
// what keeps ordinary autocommit reads and writes on their original,
// zero-added-latency code path -- no pinned-revision read, no buffering --
// exactly as before this table gained interactive-transaction support.
//
// Every resolution also registers this table's generation and definition as
// a commit-time fence (see WriteSet.RequireResolution and go-qrdb#547): a
// TRUNCATE or DDL racing this transaction then fails its commit rather than
// letting it write rows into a generation about to be reclaimed, or maintain
// an index set that is no longer the table's. This happens for a read-only
// participation too, harmlessly -- WriteSet.commit only ever proposes the
// fence when there are buffered writes to commit alongside it.
func (t *RelationalTable) activeWriteSet(ctx *sqle.Context) (*WriteSet, error) {
	if !ctx.GetIgnoreAutoCommit() {
		return nil, nil
	}
	sess, ok := ctx.Session.(*QRDBSession)
	if !ok {
		return nil, nil
	}
	ws := sess.writeSet()
	if ws == nil {
		return nil, nil
	}
	if ws.aborted != nil {
		// Rolled back as a deadlock victim: nothing the transaction does
		// any longer can be made durable, so nothing it reads may be
		// served either, until the client ends it (see abortWith).
		return nil, abortedError(ws.aborted)
	}

	if t.mvccSvc == nil {
		return nil, &RelationalMVCCUnavailableError{Table: t.def.Name}
	}
	if t.txnSvc == nil {
		return nil, &TransactionServiceUnavailableError{Table: t.def.Name}
	}
	if err := ws.bind(ctx, sess.TenantID(), t.def.Database, t.txnSvc, t.mvccSvc); err != nil {
		return nil, err
	}

	g := t.gen()
	fence := t.resolutionFence(g)
	if pinned, ok := ws.PinnedResolution(fence.key); ok {
		// An earlier statement in this SAME transaction already pinned this
		// table. Refusing HERE, at resolution, rather than deferring to
		// WriteSet.commit, is load-bearing: this transaction's reads are
		// served from the snapshot revision pinned when it FIRST bound (see
		// bind), and that snapshot knows nothing of a TRUNCATE or DDL that
		// committed since -- continuing would read or write under the
		// table's CURRENT key prefixes and index set while resolving values
		// from the older snapshot, silently producing empty or wrong
		// results. Deferring to commit would not even catch it reliably: a
		// read-only transaction's commit proposes nothing at all.
		if pinned.generation != g.generation {
			stale := &service.TableGenerationStaleError{
				Database: t.def.Database, Name: t.def.Name,
				Expected: pinned.generation, Actual: g.generation,
			}
			return nil, sqle.ErrLockDeadlock.Wrap(stale, t.def.Name)
		}
		if pinned.metadataRevision != 0 && t.def.MetadataRevision != 0 && pinned.metadataRevision != t.def.MetadataRevision {
			return nil, t.definitionStale("its definition changed during this transaction")
		}
		return ws, nil
	}
	// First registration. The snapshot may have been bound earlier, by
	// another table: if this table's generation or definition was written
	// after that snapshot, the snapshot holds neither this generation's rows
	// nor this definition's index entries, and reading it would return
	// them as absent. Refuse instead.
	//
	// The revision comparison alone is not enough: it catches a generation
	// written AFTER the snapshot, but not a handle resolved BEFORE a
	// TRUNCATE that the snapshot already includes -- that handle names an
	// older generation than the snapshot holds, with a revision below the
	// snapshot's. So the generation the snapshot actually holds is read and
	// compared too; it is one read, once per table per transaction.
	snapshotGeneration, serr := ws.generationAt(ctx, fence.key)
	if serr != nil {
		return nil, serr
	}
	if t.def.GenerationRevision > ws.revision || snapshotGeneration != g.generation {
		stale := &service.TableGenerationStaleError{
			Database: t.def.Database, Name: t.def.Name,
			Expected: snapshotGeneration, Actual: g.generation,
		}
		return nil, sqle.ErrLockDeadlock.Wrap(stale, t.def.Name)
	}
	if t.def.MetadataRevision > ws.revision {
		return nil, t.definitionStale("its definition changed after this transaction began")
	}
	ws.RequireResolution(fence)
	return ws, nil
}

// txGet reads key through the active write-set when one is open (buffer
// first, then the pinned snapshot), otherwise falls back to a direct,
// linearizable KVServicer.Get exactly as before this table gained
// interactive-transaction support. Returns *service.KeyNotFoundError when
// key does not exist, matching KVServicer.Get's contract either way.
//
// Either result -- a value or a miss -- is only returned once
// confirmGenerationPin has passed: a miss on a generation being reclaimed
// must surface as the typed stale-generation error, never as "no such row".
func (t *RelationalTable) txGet(ctx *sqle.Context, key string) ([]byte, error) {
	ws, err := t.activeWriteSet(ctx)
	if err != nil {
		return nil, err
	}
	var val []byte
	if ws != nil {
		val, err = ws.Get(ctx, key)
	} else {
		val, err = t.kvSvc.Get(ctx, key)
	}
	if err != nil && !isNotFound(err) {
		return nil, err
	}
	if perr := t.confirmGenerationPin(ctx); perr != nil {
		return nil, perr
	}
	return val, err
}

// txScan scans prefix through the active write-set when one is open
// (snapshot overlaid with buffered writes), otherwise falls back to a
// direct, linearizable KVServicer.Scan exactly as before this table gained
// interactive-transaction support. Like txGet, its result is returned only
// once confirmGenerationPin has passed.
func (t *RelationalTable) txScan(ctx *sqle.Context, prefix string) (map[string][]byte, error) {
	ws, err := t.activeWriteSet(ctx)
	if err != nil {
		return nil, err
	}
	var data map[string][]byte
	if ws != nil {
		data, err = ws.RangeOverlay(ctx, prefix)
	} else {
		data, err = t.kvSvc.Scan(ctx, prefix)
	}
	if err != nil {
		return nil, err
	}
	if perr := t.confirmGenerationPin(ctx); perr != nil {
		return nil, perr
	}
	return data, nil
}

// tableGeneration is one immutable view of the generation a RelationalTable
// uses: the generation number, the row prefix derived from it, and when it
// was last known to be current -- the start of its pin (a zero time means
// unknown, so the next read re-proves it). Every operation loads it once
// (gen) and uses that one value throughout, so a TRUNCATE on the same handle
// can never leave an operation writing rows under one generation and
// fencing on another.
type tableGeneration struct {
	generation uint64
	dataPrefix string
	pinnedAt   time.Time
}

// gen returns the handle's current generation state.
func (t *RelationalTable) gen() *tableGeneration {
	return t.state.Load()
}

// setGeneration publishes generation, pinned from pinnedAt, as the handle's
// state.
func (t *RelationalTable) setGeneration(generation uint64, pinnedAt time.Time) {
	t.state.Store(&tableGeneration{
		generation: generation,
		dataPrefix: service.TableDataPrefix(t.def.Database, t.def.TableID, generation),
		pinnedAt:   pinnedAt,
	})
}

// confirmGenerationPin is the reader half of go-qrdb#547's reader-quiescence
// guarantee (the sweeper half is service.ReclaimQuiescence). It runs after a
// read and before that read's result is used.
//
// While the pin is younger than service.TableGenerationPinTTL, the read is
// known complete before any reclamation of this generation could begin --
// the sweeper waits longer than that after the TRUNCATE that orphans it
// commits, and the pin was taken before that commit -- so it costs nothing.
// Once the pin is older, the resolution is re-proven (reproveResolution):
// unchanged, the pin is renewed from the instant before that read; changed,
// the statement fails with the typed, retryable
// *service.TableGenerationStaleError (or *service.TableDefinitionStaleError
// for a dropped index or table) rather than return rows that may be torn
// between a keyspace and its reclamation.
func (t *RelationalTable) confirmGenerationPin(ctx *sqle.Context) error {
	g := t.gen()
	if !g.pinnedAt.IsZero() && t.now().Sub(g.pinnedAt) < service.TableGenerationPinTTL {
		return nil
	}
	renewedAt := t.now()
	if err := t.reproveResolution(ctx, g); err != nil {
		return err
	}
	// Renew only the pin this check proved: if the handle moved to another
	// generation meanwhile, that state carries its own pin.
	t.state.CompareAndSwap(g, &tableGeneration{generation: g.generation, dataPrefix: g.dataPrefix, pinnedAt: renewedAt})
	return nil
}

// reproveResolution re-proves that what this statement resolved is still
// what the catalog holds: the same table, at the same generation, still
// listing every index incarnation the statement may read. A dropped index's
// entries are reclaimed under the same quiescence as a truncated generation,
// so a statement that resolved it must not outlive the pin on it either.
//
// While the catalog is mid-rewrite GetTable reports the rewrite rather than
// a definition, so the committed definition is read through the rewrite
// journal instead and proven the same way: TRUNCATE, and index DDL, are
// refused during a rewrite, but a rewrite interrupted and resumed spans
// statements, and the proof must not weaken for its duration. Without a
// TableServicer the generation is the one thing that can be re-proven, and
// is.
func (t *RelationalTable) reproveResolution(ctx *sqle.Context, g *tableGeneration) error {
	if t.tableSvc == nil {
		return t.checkGenerationStale(ctx, g)
	}
	live, err := t.tableSvc.GetTable(ctx, t.def.Database, t.def.Name)
	var inRewrite *service.TableRewriteInProgressError
	if errors.As(err, &inRewrite) {
		_, live, err = t.tableSvc.RewriteJournal(ctx, t.def.Database, t.def.Name)
		// The rewrite completed between the two reads: the committed
		// definition is readable again, and is proven like any other.
		var completed *service.TableNoRewriteInProgressError
		if errors.As(err, &completed) {
			live, err = t.tableSvc.GetTable(ctx, t.def.Database, t.def.Name)
		}
	}
	var notFound *service.TableNotFoundError
	if errors.As(err, &notFound) {
		return t.definitionStale("the table was dropped")
	}
	if err != nil {
		return err
	}
	if live.TableID != t.def.TableID {
		return t.definitionStale("the table was replaced")
	}
	if live.Generation != g.generation {
		stale := &service.TableGenerationStaleError{
			Database: t.def.Database, Name: t.def.Name,
			Expected: g.generation, Actual: live.Generation,
		}
		return sqle.ErrLockDeadlock.Wrap(stale, stale.Error())
	}
	// Index entries are keyed by incarnation, so a renamed index still reads
	// correctly; one whose incarnation is gone was dropped, and its entries
	// are being reclaimed.
	for _, idx := range t.def.Indexes {
		if !hasIncarnation(live, idx.Incarnation) {
			return t.definitionStale("index " + idx.Name + " was dropped")
		}
	}
	return nil
}

// hasIncarnation reports whether def still lists an index with incarnation.
func hasIncarnation(def *service.TableDef, incarnation uint64) bool {
	for _, idx := range def.Indexes {
		if idx.Incarnation == incarnation {
			return true
		}
	}
	return false
}

// definitionStale reports the typed, retryable definition-change error the
// same way a stale generation is reported: as sql.ErrLockDeadlock.
func (t *RelationalTable) definitionStale(reason string) error {
	stale := &service.TableDefinitionStaleError{Database: t.def.Database, Name: t.def.Name, Reason: reason}
	return sqle.ErrLockDeadlock.Wrap(stale, stale.Error())
}

// columnOrdinal returns the schema index of the column named name, or -1
// when no such column exists.
func (t *RelationalTable) columnOrdinal(name string) int {
	for i, c := range t.schema {
		if c.Name == name {
			return i
		}
	}
	return -1
}

// ---------------------------------------------------------------------------
// relationalPartition
// ---------------------------------------------------------------------------

// relationalPartition is the single-partition implementation for a full
// (unfiltered) RelationalTable scan.
type relationalPartition struct{}

// Key returns the partition key.
func (p *relationalPartition) Key() []byte {
	return []byte("default")
}

// ---------------------------------------------------------------------------
// filteredPartition
// ---------------------------------------------------------------------------

// filteredPartition carries rows already matched and decoded by
// filteredScanRows, so PartitionRows only needs to hand them back.
type filteredPartition struct {
	rows []sqle.Row
}

// Key returns a fixed partition key; there is always exactly one filtered
// partition per LookupPartitions call that takes this path.
func (p *filteredPartition) Key() []byte {
	return []byte("filtered")
}

// ---------------------------------------------------------------------------
// Range containment
// ---------------------------------------------------------------------------

// rowMatchesRanges reports whether row satisfies any one of ranges: a
// logical OR across ranges (matching how the query planner expresses an
// IN-list or an OR'd predicate) of a logical AND across each range's
// per-column expressions (matching how it expresses a conjunction over a
// multi-column index).
//
// ordinals maps each range column position to the row column it constrains,
// in index order, and colTypes carries those columns' declared SQL types --
// which govern comparison order, and in general disagree with byte order.
// A range narrower than ordinals constrains only its leading columns; the
// rest are unconstrained.
func rowMatchesRanges(ctx *sqle.Context, colTypes []sqle.Type, ordinals []int, row sqle.Row, ranges []sqle.Range) (bool, error) {
	for _, r := range ranges {
		mysqlRange, ok := r.(sqle.MySQLRange)
		if !ok || len(mysqlRange) == 0 {
			continue
		}
		match, err := rowMatchesAllColumns(ctx, colTypes, ordinals, row, mysqlRange)
		if err != nil {
			return false, err
		}
		if match {
			return true, nil
		}
	}
	return false, nil
}

// rowMatchesAllColumns reports whether row satisfies every one of a single
// range's per-column expressions.
func rowMatchesAllColumns(ctx *sqle.Context, colTypes []sqle.Type, ordinals []int, row sqle.Row, mysqlRange sqle.MySQLRange) (bool, error) {
	for i, colExpr := range mysqlRange {
		if i >= len(ordinals) {
			break
		}
		var val any
		if ordinal := ordinals[i]; ordinal < len(row) {
			val = row[ordinal]
		}
		match, err := rowMatchesRange(ctx, colTypes[i], val, colExpr)
		if err != nil {
			return false, err
		}
		if !match {
			return false, nil
		}
	}
	return true, nil
}

// rowMatchesRange reports whether val satisfies the single-column range
// expression colExpr, dispatching on its RangeType exactly as
// range_translator.go's translateMySQLRange does for index-backed tables,
// but by comparing typed values (via t.Compare) instead of translating to
// pkg/index's byte-string index encoding -- required here because a
// RelationalTable column's declared SQL type governs comparison order (e.g.
// numeric columns), which does not in general agree with ASCII-text byte
// order.
func rowMatchesRange(ctx *sqle.Context, t sqle.Type, val any, colExpr sqle.MySQLRangeColumnExpr) (bool, error) {
	switch colExpr.Type() {
	case sqle.RangeType_Empty:
		return false, nil
	case sqle.RangeType_All:
		return true, nil
	case sqle.RangeType_EqualNull:
		return val == nil, nil
	case sqle.RangeType_ClosedClosed:
		return withinBounds(ctx, t, val, colExpr, true, true)
	case sqle.RangeType_OpenOpen:
		return withinBounds(ctx, t, val, colExpr, false, false)
	case sqle.RangeType_OpenClosed:
		return withinBounds(ctx, t, val, colExpr, false, true)
	case sqle.RangeType_ClosedOpen:
		return withinBounds(ctx, t, val, colExpr, true, false)
	case sqle.RangeType_GreaterThan:
		return aboveLower(ctx, t, val, colExpr, false)
	case sqle.RangeType_GreaterOrEqual:
		return aboveLower(ctx, t, val, colExpr, true)
	case sqle.RangeType_LessThanOrNull:
		if val == nil {
			return true, nil
		}
		return belowUpper(ctx, t, val, colExpr, false)
	case sqle.RangeType_LessOrEqualOrNull:
		if val == nil {
			return true, nil
		}
		return belowUpper(ctx, t, val, colExpr, true)
	default:
		return false, &UnsupportedRangeTypeError{RangeType: fmt.Sprintf("%v", colExpr.Type())}
	}
}

// withinBounds reports whether val falls within both the lower and upper
// bounds of colExpr, with the given inclusivity.
func withinBounds(ctx *sqle.Context, t sqle.Type, val any, colExpr sqle.MySQLRangeColumnExpr, lowerInclusive, upperInclusive bool) (bool, error) {
	ok, err := aboveLower(ctx, t, val, colExpr, lowerInclusive)
	if err != nil || !ok {
		return false, err
	}
	return belowUpper(ctx, t, val, colExpr, upperInclusive)
}

// aboveLower reports whether val is above (or, when inclusive, at or above)
// colExpr's lower bound. An unbound lower cut always matches.
func aboveLower(ctx *sqle.Context, t sqle.Type, val any, colExpr sqle.MySQLRangeColumnExpr, inclusive bool) (bool, error) {
	if !sqle.MySQLRangeCutIsBinding(colExpr.LowerBound) {
		return true, nil
	}
	bound := sqle.GetMySQLRangeCutKey(colExpr.LowerBound)
	cmp, err := t.Compare(ctx, val, bound)
	if err != nil {
		return false, err
	}
	if inclusive {
		return cmp >= 0, nil
	}
	return cmp > 0, nil
}

// belowUpper reports whether val is below (or, when inclusive, at or below)
// colExpr's upper bound. An unbound upper cut always matches.
func belowUpper(ctx *sqle.Context, t sqle.Type, val any, colExpr sqle.MySQLRangeColumnExpr, inclusive bool) (bool, error) {
	if !sqle.MySQLRangeCutIsBinding(colExpr.UpperBound) {
		return true, nil
	}
	bound := sqle.GetMySQLRangeCutKey(colExpr.UpperBound)
	cmp, err := t.Compare(ctx, val, bound)
	if err != nil {
		return false, err
	}
	if inclusive {
		return cmp <= 0, nil
	}
	return cmp < 0, nil
}

// ---------------------------------------------------------------------------
// Index field type mapping
// ---------------------------------------------------------------------------

// indexFieldTypeOf maps a go-mysql-server sql.Type to the coarse field-type
// bucket qrdbIndex uses ("string", "uint64", "int64", "float64", "boolean"),
// mirroring entity_table.go's entitySQLType/goKindToIndexType mapping.
func indexFieldTypeOf(t sqle.Type) string {
	switch t.Type() {
	case sqltypes.Uint8, sqltypes.Uint16, sqltypes.Uint24, sqltypes.Uint32, sqltypes.Uint64,
		sqltypes.Year, sqltypes.Bit, sqltypes.Set, sqltypes.Enum:
		return "uint64"
	case sqltypes.Int8:
		if nt, ok := t.(sqle.NumberType); ok && nt.DisplayWidth() == 1 {
			return "boolean"
		}
		return "int64"
	case sqltypes.Int16, sqltypes.Int24, sqltypes.Int32, sqltypes.Int64:
		return "int64"
	case sqltypes.Float32, sqltypes.Float64:
		return "float64"
	default:
		return "string"
	}
}

// ---------------------------------------------------------------------------
// prefixEnd
// ---------------------------------------------------------------------------

// prefixEnd returns the key immediately following the last key that has
// prefix as a prefix -- the exclusive upper bound of a half-open [start,
// end) range scan over exactly that prefix. Returns nil (meaning "no upper
// bound") when prefix is empty or consists entirely of 0xFF bytes.
func prefixEnd(prefix []byte) []byte {
	end := make([]byte, len(prefix))
	copy(end, prefix)
	for i := len(end) - 1; i >= 0; i-- {
		if end[i] < 0xFF {
			end[i]++
			return end[:i+1]
		}
	}
	return nil
}
