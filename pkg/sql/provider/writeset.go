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
// as a single compare-and-swap. Compares cover only the keys actually
// WRITTEN, never a scan's full read set: a read-set compare would grow
// without bound across a large SELECT and blow the proposal's operation-count
// ceiling almost immediately.
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

	savepoints map[string]int // name -> undoLog length at the time of the mark

	totalBytes int
}

// newWriteSet creates an empty, unbound WriteSet. readOnly reflects the
// TransactionCharacteristic StartTransaction was called with.
func newWriteSet(readOnly bool) *WriteSet {
	return &WriteSet{
		readOnly:   readOnly,
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

// HasBufferedWrites reports whether the transaction has buffered any
// mutation not yet visible in committed storage. Callers that would
// otherwise answer a question from committed state alone -- notably
// RelationalTable.RowCount, whose result go-mysql-server substitutes
// directly for a COUNT(*) -- use this to recognise that such an answer
// would omit the transaction's own writes.
func (ws *WriteSet) HasBufferedWrites() bool { return len(ws.entries) > 0 }

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

// ---------------------------------------------------------------------------
// Reads: buffer first (read-your-own-writes), then the pinned snapshot.
// ---------------------------------------------------------------------------

// Lookup returns the value visible to this transaction for key: the
// buffered value when key has been written (or absent when it has been
// buffer-deleted), otherwise the value at the pinned snapshot revision.
// found is false when the key does not exist in either the buffer or the
// snapshot.
func (ws *WriteSet) Lookup(ctx context.Context, key string) (value []byte, found bool, err error) {
	if e, ok := ws.entries[key]; ok {
		if e.tombstone {
			return nil, false, nil
		}
		return e.value, true, nil
	}
	if !ws.bound {
		return nil, false, nil
	}

	kv, err := ws.mvccSvc.GetAtRevision(ctx, key, ws.revision)
	if err != nil {
		if snapshotAbsent(err) {
			return nil, false, nil
		}
		return nil, false, translateSnapshotError(err)
	}
	return kv.Value, true, nil
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
func (ws *WriteSet) RangeOverlay(ctx context.Context, prefix string) (map[string][]byte, error) {
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

	if len(ws.order) >= writeSetMaxOps {
		return &WriteSetLimitError{Reason: "operation count", Size: len(ws.order) + 1, Max: writeSetMaxOps}
	}
	if ws.totalBytes+newSize > writeSetMaxBytes {
		return &WriteSetLimitError{Reason: "byte size", Size: ws.totalBytes + newSize, Max: writeSetMaxBytes}
	}

	var cmp service.Compare
	if unique {
		cmp = service.Compare{Key: []byte(key), Target: service.CompareVersion, Result: service.CompareEqual, Version: 0}
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
// ModRevision it held there.
func (ws *WriteSet) baselineCompare(ctx context.Context, key string) (service.Compare, error) {
	kv, err := ws.mvccSvc.GetAtRevision(ctx, key, ws.revision)
	if err != nil {
		if snapshotAbsent(err) {
			return service.Compare{Key: []byte(key), Target: service.CompareVersion, Result: service.CompareEqual, Version: 0}, nil
		}
		return service.Compare{}, translateSnapshotError(err)
	}
	return service.Compare{Key: []byte(key), Target: service.CompareModRevision, Result: service.CompareEqual, ModRevision: kv.ModRevision}, nil
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
// savepoint marked after this one is discarded, matching MySQL: "any
// savepoints that were set after that one are removed."
func (ws *WriteSet) RollbackTo(name string) error {
	mark, ok := ws.savepoints[name]
	if !ok {
		return &SavepointNotFoundError{Name: name}
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

	for n, m := range ws.savepoints {
		if m > mark {
			delete(ws.savepoints, n)
		}
	}
	return nil
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

// Compares returns the commit-time precondition for every buffered key, in
// insertion order.
func (ws *WriteSet) Compares() []service.Compare {
	out := make([]service.Compare, 0, len(ws.order))
	for _, k := range ws.order {
		out = append(out, ws.entries[k].compare)
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
func (ws *WriteSet) commit(ctx context.Context) (*service.TransactionResponse, error) {
	if !ws.bound || len(ws.order) == 0 {
		return &service.TransactionResponse{Succeeded: true}, nil
	}
	return ws.txnSvc.Execute(ctx, ws.Compares(), ws.Ops(), nil)
}
