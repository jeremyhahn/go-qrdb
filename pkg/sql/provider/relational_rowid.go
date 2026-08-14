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
	sqle "github.com/dolthub/go-mysql-server/sql"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// The hidden row id of a table with no declared PRIMARY KEY.
//
// See pkg/sql/catalog's rowid.go for what the row id is and why it exists.
// This file is the two halves that need a table, a sequence and a storage
// key to work with:
//
//	allocateRowID  fills the hidden slot of a row about to be INSERTed,
//	               from a Raft-committed sequence.
//	decodeRow      fills the hidden slot of a row just read, from the
//	               storage key it was read under.
//
// UPDATE and DELETE need neither: go-mysql-server hands both of them the
// full table row, hidden column included (see rowexec's updateIter and
// deleteIter, which slice the child row by the table's schema positions
// rather than by a projection), and no SET can ever name the hidden column
// (the planner rejects that; see assignmentExprsToUpdateExprs), so an
// UPDATE's new row carries the same row id its old row did and the row is
// rewritten in place under the same key.
// ---------------------------------------------------------------------------

// rowIDSequenceName returns the SequenceServicer name backing t's hidden row
// id. It is distinct from autoIncSequenceName because a table with no
// PRIMARY KEY may still declare an AUTO_INCREMENT column, in which case both
// sequences are live at once and must not share a counter.
func (t *RelationalTable) rowIDSequenceName() string {
	return service.TableRowIDSequenceName(t.def.TableID)
}

// allocateRowID returns row with its hidden row id slot filled, allocating a
// fresh row id from the table's Raft-committed sequence when the slot is
// empty. It returns row unchanged for a table with a declared PRIMARY KEY
// (which has no such slot), for a row that is not the table's full schema
// width (which catalog.KeyCodec.Encode rejects for itself, with the typed
// error that names the real problem), and for a row that already carries a
// row id.
//
// The already-filled case is what makes this safe to call unconditionally on
// every INSERT attempt: go-mysql-server's REPLACE and ON DUPLICATE KEY
// paths, and this package's own compare-and-swap retry loop, can all call
// Insert more than once for the same logical row, and re-allocating each
// time would burn a row id per attempt for no benefit.
//
// A fresh slice is returned rather than mutating row in place: the row
// belongs to go-mysql-server's insert plan, which goes on using it after
// Insert returns (for RETURNING, for ON DUPLICATE KEY UPDATE, and to hand
// back to Insert again on a REPLACE retry), and a hidden column it never
// filled is not this writer's to overwrite.
func (t *RelationalTable) allocateRowID(ctx *sqle.Context, row sqle.Row) (sqle.Row, error) {
	if !t.keyCodec.Synthetic() || len(row) != len(t.schema) {
		return row, nil
	}
	slot := len(row) - 1
	if row[slot] != nil {
		return row, nil
	}
	if t.seqSvc == nil {
		return nil, &SequenceServiceUnavailableError{Table: t.def.Name}
	}

	id, err := t.rowIDs.next(ctx, t.seqSvc, t.def.Database, t.rowIDSequenceName())
	if err != nil {
		return nil, err
	}

	out := make(sqle.Row, len(row))
	copy(out, row)
	out[slot] = id
	return out, nil
}

// decodeRow decodes a stored row value and, for a table keyed by the hidden
// row id, fills that row id back in from pk -- the encoded storage key the
// value was read under, with the table's data prefix already stripped.
//
// Every read path goes through here rather than calling RowCodec.Decode
// directly, because a row whose hidden slot is left nil is a row that cannot
// subsequently be UPDATEd or DELETEd: its key is unrecoverable, and
// catalog.KeyCodec.Encode would reject it as a NULL key component.
func (t *RelationalTable) decodeRow(pk string, data []byte) (sqle.Row, error) {
	row, err := t.rowCodec.Decode(data)
	if err != nil {
		return nil, err
	}
	if !t.keyCodec.Synthetic() {
		return row, nil
	}
	vals, err := t.keyCodec.Decode(pk)
	if err != nil {
		return nil, err
	}
	row[len(row)-1] = vals[0]
	return row, nil
}
