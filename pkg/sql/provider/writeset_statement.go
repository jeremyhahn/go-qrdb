// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package provider

import sqle "github.com/dolthub/go-mysql-server/sql"

// Statement-level atomicity inside an interactive transaction.
//
// go-mysql-server brackets every DML statement with the table editors'
// StatementBegin and then exactly one of DiscardChanges (the statement
// failed) or StatementComplete. It calls them on EVERY editor the statement
// uses -- the target table's, each foreign-key cascade's, the ON DUPLICATE
// KEY updater's -- one call per editor, in the same order each time. Each
// relational editor forwards its call to the transaction's WriteSet, which
// keeps a stack of implicit savepoints: StatementBegin pushes the undo log's
// current length, DiscardChanges pops one and undoes everything after it,
// StatementComplete pops one and keeps the changes.
//
// All the editors of one statement push before any of them writes, so every
// mark a statement pushed is the same position and it does not matter which
// editor's call pops which. INSERT IGNORE and UPDATE IGNORE bracket each row
// on its own (go-mysql-server's checkpointing iterator), so a row that is
// ignored is undone alone and the statement's other rows stay.
//
// What a discarded statement leaves, as InnoDB's statement rollback does:
// the row locks it took and the reads it made (a read precondition records
// something the transaction saw, which no rollback can unsee -- see
// WriteSet.RollbackTo). A statement outside a transaction is its own
// implicit transaction (see writeset_autocommit.go), and the same marks make
// it atomic.

// beginStatement marks the start of a statement at the undo log's current
// position.
func (ws *WriteSet) beginStatement() {
	ws.statements = append(ws.statements, len(ws.undoLog))
}

// discardStatement undoes everything the innermost open statement buffered.
// It is a no-op when no statement is open.
func (ws *WriteSet) discardStatement() {
	if mark, ok := ws.popStatement(); ok {
		ws.undoTo(mark)
	}
}

// completeStatement keeps what the innermost open statement buffered.
func (ws *WriteSet) completeStatement() {
	ws.popStatement()
}

// popStatement removes and returns the innermost open statement's mark.
func (ws *WriteSet) popStatement() (int, bool) {
	n := len(ws.statements)
	if n == 0 {
		return 0, false
	}
	mark := ws.statements[n-1]
	ws.statements = ws.statements[:n-1]
	return mark, true
}

// resetStatements forgets every open statement mark. A command boundary
// calls it, so a statement whose editors were never closed cannot leave a
// mark behind for a later statement to roll back to.
func (ws *WriteSet) resetStatements() {
	ws.statements = ws.statements[:0]
}

// statementWriteSet returns the write-set a table editor's statement
// boundaries apply to: the open interactive transaction's, or the
// autocommit statement's implicit one.
//
// It deliberately does not go through RelationalTable.activeWriteSet: a
// statement boundary neither binds the write-set to a snapshot nor
// registers a table fence, and it must still work on a write-set aborted
// as a deadlock victim.
func statementWriteSet(ctx *sqle.Context) *WriteSet {
	if ctx == nil {
		return nil
	}
	sess, ok := ctx.Session.(*QRDBSession)
	if !ok {
		return nil
	}
	ws := sess.writeSet()
	if ws == nil {
		return nil
	}
	if ws.implicit {
		return ws
	}
	// An unreadable autocommit setting opens no statement here; the
	// statement's first write reports it (see writeSetFor).
	if explicit, err := explicitTxn(ctx); err != nil || !explicit {
		return nil
	}
	return ws
}

// openStatement returns the write-set a statement an editor is opening
// applies to: the explicit transaction's, or -- outside one -- the session's
// write-set, made the statement's implicit transaction (see
// writeset_autocommit.go). nil for a context that is not a QRDB session.
func openStatement(ctx *sqle.Context) *WriteSet {
	if ws := statementWriteSet(ctx); ws != nil {
		return ws
	}
	if ctx == nil {
		return nil
	}
	sess, ok := ctx.Session.(*QRDBSession)
	if !ok {
		return nil
	}
	ws := sess.writeSet()
	if ws == nil || ws.Bound() {
		return nil
	}
	if explicit, err := explicitTxn(ctx); err != nil || explicit {
		return nil
	}
	ws.beginImplicit()
	return ws
}
