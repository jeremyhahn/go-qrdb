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

	sqle "github.com/dolthub/go-mysql-server/sql"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Autocommit statements are atomic.
//
// A statement outside an explicit transaction is its own transaction, as in
// InnoDB and PostgreSQL: all of its rows commit when it ends, or none do. A
// three-row INSERT whose third row is a duplicate leaves the first two
// absent; a multi-row UPDATE or a cascading DELETE that fails part way
// leaves nothing behind.
//
// The machinery is the interactive transaction's: when a relational table's
// editor opens an autocommit statement (StatementBegin), the statement's
// WriteSet becomes IMPLICIT. Its writes are buffered instead of proposed
// row by row, its failed statement is undone by the same statement
// savepoint an explicit transaction uses, and go-mysql-server's own
// autocommit COMMIT at the end of the statement proposes them as one
// compare-and-swap -- with the statement's row-lock release folded into the
// same proposal.
//
// An implicit write-set keeps what an autocommit statement always meant,
// and changes only its atomicity:
//
//   - It pins no snapshot: a read sees the latest committed version,
//     overlaid by the statement's own buffered writes, exactly as an
//     autocommit read always did. Binding costs no read.
//   - Its commit compares exactly what an autocommit row write always
//     compared: absence of every key it newly occupies (a primary key or a
//     UNIQUE index value), the revision a locked row was read at in
//     pessimistic mode, and the table's resolution fences. A row it merely
//     rewrites or deletes is not compared -- last-writer-wins for
//     optimistic autocommit DML, as before -- and buffering a write reads
//     nothing to derive a precondition.
//   - qrdb_isolation, a transaction setting, does not apply.
//   - A duplicate is still reported at the row (MySQL 1062), because the
//     candidate keys are checked before the row is buffered -- which is
//     what INSERT IGNORE and ON DUPLICATE KEY UPDATE rely on. One that a
//     concurrent statement commits between that check and this commit is
//     reported as the same 1062 (see uniqueCheck).
//   - Reads of a table in another database go straight to the store; a
//     write to a second database in one statement is refused
//     (CrossDatabaseTransactionError), since one proposal targets one shard.
// ---------------------------------------------------------------------------

// uniqueCheck explains a failed implicit commit: it re-reads, outside the
// statement, the keys one buffered row newly occupied, and returns that
// row's duplicate-key error if one of them is now taken.
type uniqueCheck func(ctx *sqle.Context) error

// beginImplicit makes ws the implicit transaction of one autocommit
// statement. It is idempotent for the statement's several editors.
// qrdb_isolation, a transaction setting, does not apply to it: Compares,
// recordRead and RangeOverlay all test implicit before the level.
func (ws *WriteSet) beginImplicit() {
	ws.implicit = true
}

// noteRelationalWriter records that the statement in flight writes the
// relational table name.
func (ws *WriteSet) noteRelationalWriter(name string) {
	if ws.relationalWriter == "" {
		ws.relationalWriter = name
	}
}

// clearWriterMarks forgets which kinds of table the statement in flight
// writes. The marks belong to one statement; a command boundary
// (resetStatements) and every commit clear them, and a transaction starts
// with a fresh write-set that has none, so a later statement is never
// refused for an earlier one's writes -- whether or not its caller
// brackets statements as commands.
func (ws *WriteSet) clearWriterMarks() {
	ws.relationalWriter = ""
	ws.nonTransactionalWriter = ""
}

// noteNonTransactionalWriter records, on ctx's session, that the statement
// in flight writes the non-transactional (kv, entity or collection) table
// name. go-mysql-server opens every editor of a statement before any of
// them writes, so whichever kind writes first finds the other noted and the
// statement is refused before it changes anything: a kv write applies at
// once and cannot be undone with the relational rows of an atomic
// statement.
func noteNonTransactionalWriter(ctx *sqle.Context, name string) {
	sess, ok := ctx.Session.(*QRDBSession)
	if !ok {
		return
	}
	if ws := sess.writeSet(); ws != nil && ws.nonTransactionalWriter == "" {
		ws.nonTransactionalWriter = name
	}
}

// Implicit reports whether ws is an autocommit statement's implicit
// transaction.
func (ws *WriteSet) Implicit() bool { return ws.implicit }

// implicitCompare is the commit precondition of a key an implicit
// transaction buffers: absence when the key must be newly occupied, the
// lock-grant revision when the statement locked it, and none otherwise.
// ok is false for none.
func (ws *WriteSet) implicitCompare(key string, unique bool) (cmp service.Compare, ok bool) {
	if unique {
		return absenceCompare(key), true
	}
	if c, locked := ws.current[key]; locked {
		return c.compare(key), true
	}
	return service.Compare{}, false
}

// addUniqueCheck registers how to explain a commit failure on the keys a
// buffered row newly occupies. Only an implicit transaction keeps them: an
// explicit one reports a lost commit as 1213, as InnoDB does.
func (ws *WriteSet) addUniqueCheck(check uniqueCheck) {
	if ws.implicit {
		ws.uniqueChecks = append(ws.uniqueChecks, check)
	}
}

// duplicateAfterConflict runs the implicit transaction's unique checks
// after its commit failed, returning the first duplicate-key error found,
// or nil.
func (ws *WriteSet) duplicateAfterConflict(ctx *sqle.Context) error {
	for _, check := range ws.uniqueChecks {
		if err := check(ctx); err != nil {
			return err
		}
	}
	return nil
}

// commitWith is commit with release -- ops deleting the transaction's row
// locks -- folded into the same proposal, in both its branches. folded is
// false, and nothing is proposed for release, when there is nothing to
// commit; the caller then releases the locks on their own.
func (ws *WriteSet) commitWith(ctx context.Context, release []service.Op) (resp *service.TransactionResponse, folded bool, err error) {
	if !ws.bound || len(ws.order) == 0 {
		return &service.TransactionResponse{Succeeded: true}, false, nil
	}
	compares := ws.Compares()
	for _, key := range ws.resolutionOrder {
		compares = append(compares, ws.resolutions[key].compares...)
	}
	ops := ws.Ops()
	if len(release) > 0 {
		ops = append(ops, release...)
	}
	resp, err = ws.txnSvc.Execute(ctx, compares, ops, release)
	return resp, len(release) > 0, err
}

// commitAndRelease commits the write-set and releases its row locks, in one
// proposal when the locks live in the store the write-set commits to. An
// infrastructure failure leaves both the write-set and the locks in place
// for a retry.
func (ws *WriteSet) commitAndRelease(ctx context.Context) (*service.TransactionResponse, error) {
	ws.ownerMu.Lock()
	owner := ws.owner
	ws.ownerMu.Unlock()
	if owner == nil || owner.Scope() != ws.lockScope() {
		resp, _, err := ws.commitWith(ctx, nil)
		if err != nil {
			return nil, err
		}
		_ = ws.releaseLocks(ctx)
		return resp, nil
	}
	var resp *service.TransactionResponse
	err := owner.ReleaseWith(ctx, func(release []service.Op) (bool, error) {
		r, folded, cerr := ws.commitWith(ctx, release)
		resp = r
		return folded, cerr
	})
	if err != nil && resp == nil {
		return nil, err
	}
	return resp, nil
}

// lockScope is the lock-table scope of the store the write-set commits to
// (see storeID.lockScope). Empty while unbound.
func (ws *WriteSet) lockScope() string {
	if !ws.bound {
		return ""
	}
	return storeID{tenant: ws.tenantID, db: ws.database}.lockScope()
}
