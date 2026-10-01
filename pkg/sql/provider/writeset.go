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
	"strconv"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// WriteSet
//
// WriteSet buffers the mutations of one interactive SQL transaction (BEGIN
// ... COMMIT/ROLLBACK) and pins the MVCC revision reads inside that
// transaction are served from, bridging go-mysql-server's interactive
// sql.TransactionSession contract onto QRDB's KV layer, which offers only
// one-shot etcd-style compare-and-swap (service.TransactionServicer.Execute)
// rather than a native interactive transaction primitive.
//
// Isolation model: snapshot / first-committer-wins. StartTransaction pins
// the current MVCC revision the moment the first relational table
// participates (see bind); every read inside the transaction is served from
// the buffer first (read-your-own-writes), then from that pinned snapshot.
// Every key the transaction writes carries, at commit time, a single
// precondition -- the ModRevision (or absence) that key had at the pinned
// snapshot -- so a concurrent transaction that modified the same key after
// the snapshot was taken causes this transaction's commit to fail atomically
// as a single compare-and-swap. By default, compares cover only the keys
// actually WRITTEN, never a scan's full read set: a read-set compare would
// grow without bound across a large SELECT and blow the proposal's
// operation-count ceiling almost immediately.
//
// Which keys are compared is selectable per session -- see IsolationLevel
// and the qrdb_isolation session variable in session.go. The default,
// IsolationWritten, is exactly the model described above. No level changes
// what is APPLIED or how atomically: every level commits the same single
// proposal.
//
// A session executes one statement at a time -- go-mysql-server never calls
// two methods on the same *sqle.Context/session concurrently -- so WriteSet
// needs no internal locking of its own; every field below is mutated only
// from the single goroutine driving that session's current statement.
// ---------------------------------------------------------------------------

const (
	// writeSetMaxBytes bounds the cumulative estimated size (keys, values,
	// and per-operation framing overhead) of everything buffered in one
	// transaction, mirroring relational_writer.go's maxRowProposalBytes:
	// the entire write-set is proposed as ONE TransactionServicer.Execute
	// call at COMMIT time, which must stay comfortably under go-quicraft's
	// per-entry ceiling (proto.MaxEntrySize, 8 MiB).
	writeSetMaxBytes = 8 << 20

	// writeSetMaxOps bounds the number of distinct keys a single
	// transaction may buffer, independent of writeSetMaxBytes: it guards
	// against a pathological transaction that touches an enormous number
	// of tiny keys, which would otherwise pass the byte ceiling while
	// still producing an unreasonably large compare/op list.
	writeSetMaxOps = 100_000

	// writeSetOpOverheadBytes is a conservative fixed per-operation
	// estimate (framing, key/value length prefixes) added on top of an
	// operation's own key+value bytes, mirroring relational_writer.go's
	// rowOpOverheadBytes.
	writeSetOpOverheadBytes = 32
)

// ---------------------------------------------------------------------------
// Isolation levels
// ---------------------------------------------------------------------------

// IsolationLevel selects which keys an interactive transaction attaches a
// commit-time precondition to. It tunes CONFLICT DETECTION only -- it does
// not tune atomicity, and in particular IsolationNone does not mean "no
// atomicity".
//
// At every level the entire buffered write-set -- every row, every index
// entry, every catalog key the transaction touched -- is still applied as
// exactly ONE TransactionServicer.Execute proposal (see commit), so a
// transaction is still all-or-nothing and a row can still never become
// durable without the index entries that point at it. What the level varies
// is only the set of preconditions attached to that one proposal, i.e.
// which concurrent modifications made by OTHER transactions cause it to be
// rejected. What the transaction itself writes is unaffected.
//
// The level is a per-session setting (see the qrdb_isolation session system
// variable in session.go), sampled once when the transaction starts and
// then fixed for that transaction's lifetime.
//
// The zero value is IsolationWritten: a WriteSet constructed without an
// explicit level behaves exactly as this package always has.
type IsolationLevel uint8

const (
	// IsolationWritten compares every key the transaction WROTE against
	// the state that key had at the pinned snapshot revision. This is
	// snapshot isolation with first-committer-wins, the default, and the
	// only behavior this package had before the level became selectable.
	//
	// It guarantees: reads see one consistent snapshot for the whole
	// transaction; and no lost update on any row the transaction wrote --
	// if another transaction committed over any of those keys after this
	// one pinned its snapshot, this one is rejected with MySQL 1213
	// rather than silently overwriting the other's work.
	//
	// It does NOT guarantee serializability: a transaction that reads a
	// row, decides something from it, and then writes a DIFFERENT row is
	// unprotected, because the row it merely read carries no precondition
	// (the classic write skew). IsolationWrittenReads closes exactly that
	// hole for point reads.
	IsolationWritten IsolationLevel = iota

	// IsolationNone attaches no preconditions at all: the transaction's
	// buffered writes are applied unconditionally, last-writer-wins.
	//
	// It is for append-heavy or idempotent workloads -- audit trails,
	// event logs, upserts of externally-authoritative state -- where a
	// lost update is acceptable by construction and the 1213 a genuine
	// key overlap would produce is pure cost, since the correct client
	// response would be to retry and overwrite anyway.
	//
	// It still guarantees: the whole write-set applies atomically at one
	// revision, and reads within the transaction still come from one
	// pinned snapshot. What is given up is exclusively the CROSS-
	// TRANSACTION precondition -- a concurrent transaction's committed
	// write to a key this transaction also wrote is silently overwritten
	// instead of detected. Nothing internal to the transaction is
	// relaxed: a row and its index entries still become visible together
	// or not at all, and PRIMARY/UNIQUE constraints are still enforced by
	// the state machine, which validates them over the proposed op set
	// independently of any compare (see MVCCEngine.handleTransaction).
	IsolationNone

	// IsolationWrittenReads compares every key the transaction wrote AND
	// every key it read BY EXACT KEY (a point read through Lookup/Get --
	// a primary-key or whole-tuple unique-index lookup), approaching
	// serializability for transactions whose reads are all point reads.
	// It is what catches the write skew IsolationWritten misses: the key
	// that was only read still has to hold the value it held at the
	// pinned snapshot for the commit to succeed.
	//
	// It deliberately stops short of true serializability, and says so
	// rather than pretending otherwise: a RANGE read (any table scan or
	// multi-entry index scan) is REFUSED at this level with
	// *IsolationRangeReadError instead of being admitted with its read
	// set silently ignored. Comparing a scan's read set would grow the
	// proposal without bound and blow its operation-count ceiling almost
	// immediately, and admitting the scan while ignoring it would report
	// a guarantee that is not being enforced. Phantoms are therefore out
	// of scope by construction: at this level a transaction cannot
	// perform the range read that would expose one.
	IsolationWrittenReads
)

// isolationLevelNames maps each level to the spelling the qrdb_isolation
// session variable uses.
var isolationLevelNames = map[IsolationLevel]string{
	IsolationNone:         "none",
	IsolationWritten:      "written",
	IsolationWrittenReads: "written+reads",
}

// isolationLevelOrder fixes the order the levels are registered in as the
// qrdb_isolation enum's values, weakest first, so that the numeric aliases
// MySQL accepts for an ENUM system variable (SET qrdb_isolation = 1) read as
// increasing strictness. That ordinal is deliberately independent of the Go
// constants' own values, which instead put the DEFAULT at zero so that an
// IsolationLevel field left uninitialized means "behave exactly as before".
var isolationLevelOrder = []IsolationLevel{IsolationNone, IsolationWritten, IsolationWrittenReads}

// isolationLevels resolves a qrdb_isolation spelling back to its level. It
// is derived from isolationLevelNames so the two can never disagree.
var isolationLevels = func() map[string]IsolationLevel {
	out := make(map[string]IsolationLevel, len(isolationLevelNames))
	for level, name := range isolationLevelNames {
		out[name] = level
	}
	return out
}()

// String implements fmt.Stringer, returning the level's qrdb_isolation
// spelling.
func (l IsolationLevel) String() string {
	if name, ok := isolationLevelNames[l]; ok {
		return name
	}
	return "IsolationLevel(" + strconv.Itoa(int(l)) + ")"
}

// wsEntry is the buffered state of one physical key: its current value (or
// tombstone), the commit-time precondition established when the key was
// first touched by this transaction, and the estimated byte size it
// contributes to the write-set's running total.
type wsEntry struct {
	value     []byte
	tombstone bool
	compare   service.Compare
	size      int
}

// undoRecord captures the state of one key immediately before a single
// WriteSet mutation, so a savepoint rollback can restore it. existed is
// false when the mutation created the key's first entry in the
// write-set -- rolling that back removes the entry entirely rather than
// restoring prev, which is meaningless in that case.
type undoRecord struct {
	key     string
	existed bool
	prev    wsEntry
}

// WriteSet buffers one interactive transaction's writes. See the package
// doc comment above for the isolation model and concurrency contract.
type WriteSet struct {
	readOnly bool

	// level is the conflict-detection scope this transaction commits
	// with, sampled from the session when the transaction started and
	// fixed thereafter. Its zero value, IsolationWritten, is the default.
	level IsolationLevel

	// bound is true once the write-set has pinned a snapshot revision and
	// resolved the TransactionServicer/MVCCKVServicer it will commit
	// through -- deferred until the first relational table actually
	// participates (see bind), since StartTransaction itself has no
	// (tenant, database) to resolve them against.
	bound    bool
	tenantID string
	database string
	txnSvc   service.TransactionServicer
	mvccSvc  service.MVCCKVServicer
	revision int64

	entries map[string]*wsEntry
	order   []string // insertion order; savepoint marks are indexes into undoLog, not this slice
	undoLog []undoRecord

	// reads holds the commit-time precondition for every key the
	// transaction has read BY EXACT KEY but not (yet) written, and
	// readOrder their first-read order. Both are populated only at
	// IsolationWrittenReads -- recordRead returns immediately at every
	// other level, so at the default the map is never even allocated and
	// a read costs exactly what it always did.
	reads     map[string]service.Compare
	readOrder []string

	savepoints map[string]int // name -> undoLog length at the time of the mark

	// statements is the stack of implicit statement savepoints: the undoLog
	// length at each open StatementBegin. See writeset_statement.go.
	statements []int

	totalBytes int

	// resolutions holds one resolutionGuard per relational table this
	// transaction has participated in (read or written), keyed by the
	// table's generation key (see RequireResolution), and resolutionOrder
	// its first-registration order. Populated by
	// RelationalTable.activeWriteSet the first time each table resolves
	// this write-set, and folded into every commit's compares regardless
	// of ws.level (see commit/Compares) -- go-qrdb#547's generation and
	// definition fences are a distinct correctness concern from the
	// user-selectable conflict-detection IsolationLevel, so IsolationNone,
	// which otherwise attaches no preconditions at all, must not skip them.
	resolutions     map[string]resolutionGuard
	resolutionOrder []string

	// rowLocks is this transaction's row-lock state -- its lock owner, the
	// current reads its locks were granted with, and whether it was rolled
	// back as a deadlock victim. See writeset_locks.go and
	// docs/row-locking/.
	rowLocks
}

// resolutionGuard is one relational table's commit-time fence on what the
// transaction resolved: the preconditions themselves (compares: the
// generation, and the definition's metadata revision when known) plus what
// they expect and enough identifying detail to report which one failed
// commit (see WriteSet.staleResolution).
type resolutionGuard struct {
	key              string
	compares         []service.Compare
	database         string
	table            string
	generation       uint64
	metadataKey      string
	metadataRevision int64
}

// newWriteSet creates an empty, unbound WriteSet. readOnly reflects the
// TransactionCharacteristic StartTransaction was called with; level is the
// conflict-detection scope the session selected (see IsolationLevel).
func newWriteSet(readOnly bool, level IsolationLevel) *WriteSet {
	return &WriteSet{
		readOnly:   readOnly,
		level:      level,
		entries:    make(map[string]*wsEntry),
		savepoints: make(map[string]int),
	}
}

// Bound reports whether the write-set has pinned a snapshot revision, i.e.
// whether any relational table has participated in this transaction yet.
func (ws *WriteSet) Bound() bool { return ws.bound }

// ReadOnly reports whether the transaction was started with
// START TRANSACTION READ ONLY.
func (ws *WriteSet) ReadOnly() bool { return ws.readOnly }

// Isolation returns the conflict-detection scope this transaction will
// commit with, fixed when the transaction started.
func (ws *WriteSet) Isolation() IsolationLevel { return ws.level }

// bind resolves the (tenant, database)-scoped TransactionServicer and
// MVCCKVServicer this write-set will read and commit through, and pins the
// snapshot revision every read inside the transaction is served from. It is
// idempotent: the first relational table to participate in the transaction
// wins, and every subsequent call is a no-op as long as it agrees on
// (tenantID, database) -- a call naming a different database returns
// *CrossDatabaseTransactionError, since a single compare-and-swap commit can
// only ever target one Raft shard.
func (ws *WriteSet) bind(ctx context.Context, tenantID, database string, txnSvc service.TransactionServicer, mvccSvc service.MVCCKVServicer) error {
	if ws.bound {
		if ws.tenantID != tenantID || ws.database != database {
			return &CrossDatabaseTransactionError{Bound: ws.database, Requested: database}
		}
		return nil
	}

	rev, err := mvccSvc.CurrentRevision(ctx)
	if err != nil {
		return err
	}

	ws.tenantID = tenantID
	ws.database = database
	ws.txnSvc = txnSvc
	ws.mvccSvc = mvccSvc
	ws.revision = rev
	ws.bound = true
	return nil
}

// RequireResolution registers the fence for one relational table
// participating in this transaction (see RelationalTable.resolutionFence),
// so this transaction's commit is rejected if a TRUNCATE or DDL raced it --
// independent of ws.level, since IsolationNone otherwise attaches no
// preconditions at all (see Compares/noCompares). commit applies it only
// when there is something buffered to write, so a purely read-only
// transaction pays nothing extra.
//
// A table already registered is a no-op: the first registration is what
// this whole transaction was planned against. Callers must check
// PinnedResolution BEFORE calling this -- a later statement resolving a
// different generation or definition means one moved during the
// transaction, which committing (or even reading) against would silently
// use the wrong keyspace. See RelationalTable.activeWriteSet, the only
// caller.
func (ws *WriteSet) RequireResolution(guard resolutionGuard) {
	if _, ok := ws.resolutions[guard.key]; ok {
		return
	}
	if ws.resolutions == nil {
		ws.resolutions = make(map[string]resolutionGuard)
	}
	ws.resolutions[guard.key] = guard
	ws.resolutionOrder = append(ws.resolutionOrder, guard.key)
}

// PinnedResolution reports what an earlier statement in this transaction
// already pinned for the table whose generation key is key, and whether one
// is pinned at all.
func (ws *WriteSet) PinnedResolution(key string) (resolutionGuard, bool) {
	g, ok := ws.resolutions[key]
	return g, ok
}

// generationAt reads the generation the transaction's snapshot holds for the
// table whose generation key is key -- 0 when the key did not exist at the
// snapshot revision.
func (ws *WriteSet) generationAt(ctx context.Context, key string) (uint64, error) {
	kv, err := ws.mvccSvc.GetAtRevision(ctx, key, ws.revision)
	if err != nil {
		// A generation key the snapshot predates is generation 0, whichever
		// of the two "absent at this revision" errors the store reports.
		if snapshotAbsent(err) {
			return 0, nil
		}
		return 0, err
	}
	if kv == nil {
		return 0, nil
	}
	return service.DecodeTableGeneration(kv.Value), nil
}

// staleResolution reports the first registered table fence that no longer
// holds, distinguishing a TRUNCATE or DDL race (go-qrdb#547) from an
// ordinary write conflict when commit's compare-and-swap fails (see
// QRDBSession.CommitTransaction): a *service.TableGenerationStaleError or a
// *service.TableDefinitionStaleError, or nil when every fence still holds.
// Fences are checked in registration order so a transaction touching several
// tables reports the first one it looked at, matching statement order.
//
// These are plain reads through the MVCCKVServicer the transaction already
// bound, run at most once and only after commit has already failed -- never
// on the successful path.
func (ws *WriteSet) staleResolution(ctx context.Context) (stale error, err error) {
	for _, key := range ws.resolutionOrder {
		g := ws.resolutions[key]
		live, err := ws.mvccSvc.Get(ctx, key)
		if err != nil {
			if !isNotFound(err) {
				return nil, err
			}
			live = nil
		}
		// Compared as decoded generation NUMBERS, not raw bytes: an absent
		// generation key (0) is fenced against an EMPTY value rather than
		// EncodeTableGeneration(0)'s eight zero bytes (generationCompare).
		if liveGeneration := service.DecodeTableGeneration(live); liveGeneration != g.generation {
			return &service.TableGenerationStaleError{
				Database: g.database, Name: g.table,
				Expected: g.generation, Actual: liveGeneration,
			}, nil
		}
		if g.metadataRevision == 0 {
			continue
		}
		meta, err := ws.mvccSvc.GetRevision(ctx, g.metadataKey)
		switch {
		case err != nil && isNotFound(err):
			return &service.TableDefinitionStaleError{Database: g.database, Name: g.table, Reason: "the table was renamed or dropped"}, nil
		case err != nil:
			return nil, err
		case meta.ModRevision != g.metadataRevision:
			return &service.TableDefinitionStaleError{Database: g.database, Name: g.table, Reason: "its definition changed"}, nil
		}
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// Reads: buffer first (read-your-own-writes), then the pinned snapshot.
// ---------------------------------------------------------------------------

// Lookup returns the value visible to this transaction for key: the
// buffered value when key has been written (or absent when it has been
// buffer-deleted), otherwise the value at the pinned snapshot revision.
// found is false when the key does not exist in either the buffer or the
// snapshot.
//
// Lookup is this package's only point-read entry point (Get delegates to
// it), so at IsolationWrittenReads it is also where a read's commit-time
// precondition is recorded -- from the very snapshot read the answer
// itself comes from, never a second one. A key served from the buffer
// records nothing: the transaction has already written it, so it already
// carries a write precondition, and by read-your-own-writes the buffered
// answer did not come from the snapshot at all.
func (ws *WriteSet) Lookup(ctx context.Context, key string) (value []byte, found bool, err error) {
	if e, ok := ws.entries[key]; ok {
		if e.tombstone {
			return nil, false, nil
		}
		return e.value, true, nil
	}
	if c, ok := ws.current[key]; ok {
		// A key this transaction locked is read as of its lock grant, not
		// its snapshot (see recordCurrent).
		if rerr := ws.recordRead(key, c.compare(key)); rerr != nil {
			return nil, false, rerr
		}
		return c.value, c.found, nil
	}
	if !ws.bound {
		return nil, false, nil
	}

	kv, err := ws.mvccSvc.GetAtRevision(ctx, key, ws.revision)
	if err != nil {
		if snapshotAbsent(err) {
			if rerr := ws.recordRead(key, absenceCompare(key)); rerr != nil {
				return nil, false, rerr
			}
			return nil, false, nil
		}
		return nil, false, translateSnapshotError(err)
	}
	if rerr := ws.recordRead(key, modRevisionCompare(key, kv.ModRevision)); rerr != nil {
		return nil, false, rerr
	}
	return kv.Value, true, nil
}

// recordRead remembers, at IsolationWrittenReads only, the commit-time
// precondition cmp for a key this transaction read by exact key, so that
// Compares can require the key to still hold that state when the
// transaction commits. It is a no-op at every other level, which is what
// keeps IsolationWritten's read path byte-for-byte what it always was.
//
// The first read of a key wins, exactly as the first write of a key does
// (see recordTouch): every read in the transaction is served from the same
// pinned snapshot, so re-deriving could only ever produce the same compare.
//
// Read preconditions count against the same write-set ceilings the buffered
// writes do -- they occupy the same single proposal -- so a transaction that
// point-reads its way past them is refused with *WriteSetLimitError at the
// offending statement rather than at COMMIT.
func (ws *WriteSet) recordRead(key string, cmp service.Compare) error {
	if ws.level != IsolationWrittenReads {
		return nil
	}
	if _, ok := ws.reads[key]; ok {
		return nil
	}

	size := len(key) + writeSetOpOverheadBytes
	if len(ws.order)+len(ws.readOrder) >= writeSetMaxOps {
		return &WriteSetLimitError{Reason: "operation count", Size: len(ws.order) + len(ws.readOrder) + 1, Max: writeSetMaxOps}
	}
	if ws.totalBytes+size > writeSetMaxBytes {
		return &WriteSetLimitError{Reason: "byte size", Size: ws.totalBytes + size, Max: writeSetMaxBytes}
	}

	if ws.reads == nil {
		ws.reads = make(map[string]service.Compare)
	}
	ws.reads[key] = cmp
	ws.readOrder = append(ws.readOrder, key)
	ws.totalBytes += size
	return nil
}

// snapshotAbsent reports whether a pinned-revision read failed because the key
// simply did not exist as of that revision, which is an answer rather than a
// failure.
//
// Two distinct errors mean that, depending on what the store found. A key that
// has never been written at all yields *service.KeyNotFoundError. A key whose
// oldest version is NEWER than the pinned revision -- or, in an MVCC store
// keyed by (key, revision), a key with no data entry at or before it -- yields
// *service.RevisionNotFoundError instead (see pkg/mvcc.Store.getDataAtRevision
// and findAtRevision). Both describe the same fact from the transaction's
// point of view: as of the snapshot, the key was not there.
//
// Treating only the first as absence made every interactive transaction fail
// on its first read of a not-yet-existing key against a real MVCC store, while
// passing against test doubles that return KeyNotFoundError for both.
// *service.RevisionCompactedError is deliberately NOT included: that one says
// the snapshot itself is gone, which is a genuine failure of the transaction.
func snapshotAbsent(err error) bool {
	if isNotFound(err) {
		return true
	}
	var noRevision *service.RevisionNotFoundError
	return errors.As(err, &noRevision)
}

// Get implements the same contract as service.KVServicer.Get (a
// *service.KeyNotFoundError for a missing key), so a RelationalTable read
// path can substitute it for a direct KVServicer.Get call without changing
// its error handling.
func (ws *WriteSet) Get(ctx context.Context, key string) ([]byte, error) {
	v, found, err := ws.Lookup(ctx, key)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, &service.KeyNotFoundError{Key: key}
	}
	return v, nil
}

// RangeOverlay returns every key-value pair visible to this transaction
// under prefix: the pinned snapshot's entries, with every key this
// transaction has buffered under that prefix overlaid (a put overrides or
// adds; a delete removes). The returned map has the same shape
// service.KVServicer.Scan returns, so a RelationalTable read path can use it
// as a drop-in replacement.
//
// At IsolationWrittenReads a range read is refused outright with
// *IsolationRangeReadError. That level's contract is that every key the
// transaction read is compared at commit, and a scan's read set cannot be
// compared -- it grows without bound and would blow the proposal's
// operation-count ceiling. Serving the scan anyway and quietly leaving its
// read set uncompared would report a guarantee that is not being enforced,
// so the transaction is told, at the statement that asked, exactly what
// this level cannot do.
func (ws *WriteSet) RangeOverlay(ctx context.Context, prefix string) (map[string][]byte, error) {
	if ws.level == IsolationWrittenReads {
		return nil, &IsolationRangeReadError{Level: ws.level.String(), Prefix: prefix}
	}

	out := make(map[string][]byte)

	if ws.bound {
		end := prefixEnd([]byte(prefix))
		kvs, _, _, err := ws.mvccSvc.Range(ctx, []byte(prefix), end, service.WithRevision(ws.revision))
		if err != nil {
			return nil, translateSnapshotError(err)
		}
		for _, kv := range kvs {
			out[string(kv.Key)] = kv.Value
		}
	}

	for key, c := range ws.current {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if c.found {
			out[key] = c.value
		} else {
			delete(out, key)
		}
	}

	for _, key := range ws.order {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		e := ws.entries[key]
		if e.tombstone {
			delete(out, key)
		} else {
			out[key] = e.value
		}
	}
	return out, nil
}

// translateSnapshotError maps a compacted pinned revision to the typed,
// non-silent refusal a long-running transaction must surface (see
// TransactionSnapshotExpiredError) rather than letting the raw
// *service.RevisionCompactedError propagate.
func translateSnapshotError(err error) error {
	var rce *service.RevisionCompactedError
	if errors.As(err, &rce) {
		return &TransactionSnapshotExpiredError{Requested: rce.Requested, Compacted: rce.Compacted}
	}
	return err
}

// ---------------------------------------------------------------------------
// Writes
// ---------------------------------------------------------------------------

// Put buffers key=value, deriving its commit-time precondition from the
// pinned snapshot: absence (Version==0) if key did not exist there,
// otherwise equality against the ModRevision it held there. Subsequent
// calls for the same key update the buffered value but never re-derive the
// precondition -- see recordTouch.
func (ws *WriteSet) Put(ctx context.Context, key string, value []byte) error {
	return ws.recordTouch(ctx, key, value, false, false)
}

// PutUnique buffers key=value with an unconditional "must be absent"
// (Version==0) commit-time precondition, regardless of the pinned
// snapshot's state -- for a key that is genuinely new at the pinned
// snapshot this is identical to what Put would derive, but PutUnique skips
// the snapshot read entirely, since the answer never depends on it. It is
// the buffered-transaction equivalent of the Version==0 Compare
// relational_writer.go's autocommit Insert/Update use to enforce a
// PRIMARY/UNIQUE constraint (see that file's package doc comment).
func (ws *WriteSet) PutUnique(ctx context.Context, key string, value []byte) error {
	return ws.recordTouch(ctx, key, value, false, true)
}

// Delete buffers a deletion of key, deriving its commit-time precondition
// from the pinned snapshot exactly as Put does.
func (ws *WriteSet) Delete(ctx context.Context, key string) error {
	return ws.recordTouch(ctx, key, nil, true, false)
}

// recordTouch is the shared implementation behind Put/PutUnique/Delete. On
// a key's first touch within this transaction it establishes the
// commit-time precondition (unique: always absence; otherwise derived from
// the pinned snapshot via baselineCompare) and enforces the write-set size
// ceilings (see WriteSetLimitError). On a later touch to the same key, the
// original precondition is preserved unchanged -- by read-your-own-writes,
// nothing external can have altered the key between the pinned snapshot and
// this transaction's own first write to it, so re-deriving would be both
// wasted work and, for a key this transaction has since buffer-deleted and
// re-created, actively wrong (it would derive from the pre-transaction
// snapshot state of a key this transaction itself just vacated).
func (ws *WriteSet) recordTouch(ctx context.Context, key string, value []byte, tombstone, unique bool) error {
	if ws.readOnly {
		return &ReadOnlyTransactionError{}
	}

	newSize := len(key) + len(value) + writeSetOpOverheadBytes

	entry, existed := ws.entries[key]
	if existed {
		delta := newSize - entry.size
		if ws.totalBytes+delta > writeSetMaxBytes {
			return &WriteSetLimitError{Reason: "byte size", Size: ws.totalBytes + delta, Max: writeSetMaxBytes}
		}
		ws.undoLog = append(ws.undoLog, undoRecord{key: key, existed: true, prev: *entry})
		ws.totalBytes += delta
		entry.value = value
		entry.tombstone = tombstone
		entry.size = newSize
		return nil
	}

	if len(ws.order)+len(ws.readOrder) >= writeSetMaxOps {
		return &WriteSetLimitError{Reason: "operation count", Size: len(ws.order) + len(ws.readOrder) + 1, Max: writeSetMaxOps}
	}
	if ws.totalBytes+newSize > writeSetMaxBytes {
		return &WriteSetLimitError{Reason: "byte size", Size: ws.totalBytes + newSize, Max: writeSetMaxBytes}
	}

	var cmp service.Compare
	if unique {
		cmp = absenceCompare(key)
	} else {
		var err error
		cmp, err = ws.baselineCompare(ctx, key)
		if err != nil {
			return err
		}
	}

	ws.entries[key] = &wsEntry{value: value, tombstone: tombstone, compare: cmp, size: newSize}
	ws.order = append(ws.order, key)
	ws.undoLog = append(ws.undoLog, undoRecord{key: key, existed: false})
	ws.totalBytes += newSize
	return nil
}

// baselineCompare resolves the commit-time precondition for a key's first
// touch by reading its state at the pinned snapshot revision: absence
// (Version==0) if it did not exist there, otherwise equality against the
// ModRevision it held there. A key the transaction locked is compared
// against the state it was read in when its lock was granted instead (see
// recordCurrent): that is the version the transaction's statements worked
// from.
func (ws *WriteSet) baselineCompare(ctx context.Context, key string) (service.Compare, error) {
	if c, ok := ws.current[key]; ok {
		return c.compare(key), nil
	}
	kv, err := ws.mvccSvc.GetAtRevision(ctx, key, ws.revision)
	if err != nil {
		if snapshotAbsent(err) {
			return absenceCompare(key), nil
		}
		return service.Compare{}, translateSnapshotError(err)
	}
	return modRevisionCompare(key, kv.ModRevision), nil
}

// absenceCompare builds the precondition "key did not exist at the pinned
// snapshot and must still not exist at commit". Version==0 is how the store
// spells absence: a key that has never been written, or has been deleted,
// has no version.
func absenceCompare(key string) service.Compare {
	return service.Compare{Key: []byte(key), Target: service.CompareVersion, Result: service.CompareEqual, Version: 0}
}

// modRevisionCompare builds the precondition "key must still hold the
// revision it held at the pinned snapshot at commit time".
func modRevisionCompare(key string, modRevision int64) service.Compare {
	return service.Compare{Key: []byte(key), Target: service.CompareModRevision, Result: service.CompareEqual, ModRevision: modRevision}
}

// ---------------------------------------------------------------------------
// Savepoints
// ---------------------------------------------------------------------------

// Mark records name as a savepoint at the write-set's current position. A
// name already in use is silently replaced, matching SQL SAVEPOINT
// semantics (re-issuing SAVEPOINT with the same name moves it).
func (ws *WriteSet) Mark(name string) {
	ws.savepoints[name] = len(ws.undoLog)
}

// RollbackTo undoes every buffered mutation recorded since name's mark, in
// reverse order, restoring each affected key to its pre-mark state (or
// removing it entirely if the mark predates its first touch). The
// savepoint itself remains valid -- a later statement may continue and a
// subsequent RollbackTo the same name is still well-defined -- but any
// savepoint marked after this one is discarded, matching MySQL, which removes
// any savepoints that were set after the one being rolled back to.
//
// Read preconditions recorded at IsolationWrittenReads are deliberately NOT
// undone: rolling back a buffered write un-writes it, but nothing can
// un-read a value the transaction has already seen and may have branched
// on. InnoDB draws the same line -- ROLLBACK TO SAVEPOINT releases no row
// locks acquired since the mark.
func (ws *WriteSet) RollbackTo(name string) error {
	mark, ok := ws.savepoints[name]
	if !ok {
		return &SavepointNotFoundError{Name: name}
	}
	ws.undoTo(mark)

	for n, m := range ws.savepoints {
		if m > mark {
			delete(ws.savepoints, n)
		}
	}
	return nil
}

// undoTo undoes every buffered mutation recorded at or after position mark
// of the undo log, in reverse order, restoring each affected key to its
// state before the mark (or removing it entirely if the mark predates its
// first touch). A mark beyond the end of the log -- one taken before the
// write-set was emptied by abortWith -- has nothing left to undo.
func (ws *WriteSet) undoTo(mark int) {
	if mark >= len(ws.undoLog) {
		return
	}
	for i := len(ws.undoLog) - 1; i >= mark; i-- {
		rec := ws.undoLog[i]
		if !rec.existed {
			if e, ok := ws.entries[rec.key]; ok {
				ws.totalBytes -= e.size
			}
			delete(ws.entries, rec.key)
			continue
		}
		prev := rec.prev
		if cur, ok := ws.entries[rec.key]; ok {
			ws.totalBytes -= cur.size
		}
		ws.totalBytes += prev.size
		ws.entries[rec.key] = &prev
	}
	ws.undoLog = ws.undoLog[:mark]
	ws.rebuildOrder()
}

// Release discards name from the set of active savepoints without
// affecting any buffered data, matching SQL RELEASE SAVEPOINT.
func (ws *WriteSet) Release(name string) error {
	if _, ok := ws.savepoints[name]; !ok {
		return &SavepointNotFoundError{Name: name}
	}
	delete(ws.savepoints, name)
	return nil
}

// rebuildOrder drops every key no longer present in ws.entries from
// ws.order (following a RollbackTo that deleted it), preserving the
// relative order of every key that remains.
func (ws *WriteSet) rebuildOrder() {
	kept := ws.order[:0]
	for _, k := range ws.order {
		if _, ok := ws.entries[k]; ok {
			kept = append(kept, k)
		}
	}
	ws.order = kept
}

// ---------------------------------------------------------------------------
// Commit
// ---------------------------------------------------------------------------

// compareBuilders dispatches Compares to the builder for each isolation
// level, so adding a level is adding an entry here and a builder below
// rather than another arm of a conditional threaded through the commit
// path.
var compareBuilders = map[IsolationLevel]func(*WriteSet) []service.Compare{
	IsolationNone:         (*WriteSet).noCompares,
	IsolationWritten:      (*WriteSet).writtenCompares,
	IsolationWrittenReads: (*WriteSet).writtenAndReadCompares,
}

// Compares returns the commit-time preconditions this transaction's single
// compare-and-swap carries, which keys those cover being determined by the
// transaction's isolation level (see IsolationLevel).
//
// A level with no builder -- only reachable by constructing an
// IsolationLevel value that is not one of this package's constants -- falls
// back to IsolationWritten, the default, so an out-of-range value can never
// silently weaken a transaction's conflict detection.
func (ws *WriteSet) Compares() []service.Compare {
	build, ok := compareBuilders[ws.level]
	if !ok {
		build = (*WriteSet).writtenCompares
	}
	return build(ws)
}

// noCompares is IsolationNone's builder: the write-set commits
// unconditionally.
func (ws *WriteSet) noCompares() []service.Compare { return nil }

// writtenCompares is IsolationWritten's builder: the precondition of every
// buffered key, in insertion order.
func (ws *WriteSet) writtenCompares() []service.Compare {
	out := make([]service.Compare, 0, len(ws.order))
	for _, k := range ws.order {
		out = append(out, ws.entries[k].compare)
	}
	return out
}

// writtenAndReadCompares is IsolationWrittenReads' builder: every written
// key's precondition, then every point-read key's, in first-read order.
//
// A key that was read and later written contributes only its WRITE
// precondition. Both derive from the same pinned snapshot, so for an
// ordinary Put the two are identical and the second would be redundant;
// for a PutUnique they deliberately differ (see that method), and it is the
// write's unconditional absence requirement -- the one enforcing the
// PRIMARY/UNIQUE constraint -- that must be the one to survive.
func (ws *WriteSet) writtenAndReadCompares() []service.Compare {
	out := ws.writtenCompares()
	for _, k := range ws.readOrder {
		if _, written := ws.entries[k]; written {
			continue
		}
		out = append(out, ws.reads[k])
	}
	return out
}

// Ops returns the write operation for every buffered key, in insertion
// order.
func (ws *WriteSet) Ops() []service.Op {
	out := make([]service.Op, 0, len(ws.order))
	for _, k := range ws.order {
		e := ws.entries[k]
		if e.tombstone {
			out = append(out, service.Op{Type: service.OpDelete, Key: []byte(k)})
		} else {
			out = append(out, service.Op{Type: service.OpPut, Key: []byte(k), Value: e.value})
		}
	}
	return out
}

// commit applies the entire buffered write-set as one
// TransactionServicer.Execute compare-and-swap. An unbound write-set (no
// relational table ever participated -- e.g. a transaction that only ever
// SELECTed from legacy tables, or committed immediately after BEGIN) and a
// bound-but-empty one (every statement was a read) both commit trivially
// without proposing anything, since there is nothing to make durable.
//
// That holds at IsolationWrittenReads too, where a read-only transaction
// may have accumulated read preconditions but no writes. Proposing them
// alone would only ask "did anything I read change?", and a transaction
// that wrote nothing cannot lose an update or skew a write no matter what
// the answer is -- it already observed one consistent snapshot, and its
// effect on the database is empty. So a read-only transaction is never
// failed for losing a race it was not in, at any level.
func (ws *WriteSet) commit(ctx context.Context) (*service.TransactionResponse, error) {
	if !ws.bound || len(ws.order) == 0 {
		return &service.TransactionResponse{Succeeded: true}, nil
	}
	compares := ws.Compares()
	for _, key := range ws.resolutionOrder {
		compares = append(compares, ws.resolutions[key].compares...)
	}
	return ws.txnSvc.Execute(ctx, compares, ws.Ops(), nil)
}
