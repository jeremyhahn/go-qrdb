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

package catalog

import (
	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// The synthetic row id
//
// A table that declares no PRIMARY KEY is keyed by a hidden, monotonically
// increasing 64-bit row id, exactly as InnoDB keys such a table by its own
// internal DB_ROW_ID. Three consequences follow, and every one of them is
// what makes a keyless table behave like MySQL's:
//
//  1. Two identical rows are distinct rows. They get different row ids, so
//     they occupy different storage keys and neither collides with the
//     other. There is no cardinality counter and no content hash anywhere in
//     this package.
//  2. A table scan returns rows in insertion order. The row id is encoded
//     with the same order-preserving numeric encoding KeyCodec gives any
//     other unsigned key column, so lexicographic key order is row id order.
//  3. A secondary index entry points at a STABLE row key. Nothing a user can
//     write changes a row's row id, so an UPDATE never re-keys the row and
//     never has to move index entries that its own values did not change.
//
// The row id is materialized as a go-mysql-server hidden system column
// (sql.Column.HiddenSystem) appended to the end of the table's schema. That
// is what keeps it invisible: go-mysql-server excludes a HiddenSystem column
// from SELECT * star expansion, from an INSERT's implicit column list, from
// information_schema.COLUMNS, and from SHOW CREATE TABLE / SHOW COLUMNS,
// and rejects any statement that names it -- while still including it in the
// row handed to a table's RowUpdater/RowDeleter, which is precisely the
// combination a hidden physical key needs.
//
// It is deliberately NOT stored inside the encoded row (see RowCodec): the
// row id IS the storage key, so writing it into the value too would be
// redundant. The stored bytes of a keyless table's row are byte-identical to
// those of the same row in a keyed table; the row id is injected back into
// the hidden slot on read, from the key it was read under.
// ---------------------------------------------------------------------------

// RowIDColumnName is the name of the synthetic, hidden system column that
// carries the row id of a table declaring no PRIMARY KEY.
//
// It carries sql.HiddenSystemColumnPrefix so that sql.IsHiddenSystemColumn
// reports true for it, which is what makes go-mysql-server reject a query
// that names it (see the validateNoHiddenSystemColumns analyzer rule). It
// deliberately does not use sql.HiddenSystemColumnName, whose
// "!hidden!<index>!<position>!0" shape is reserved for the backing columns
// of a functional index and is matched as such by
// sql.IsHiddenSystemColumnForIndex.
const RowIDColumnName = sqle.HiddenSystemColumnPrefix + "rowid"

// rowIDType is the SQL type of the hidden row id column. Unsigned 64-bit
// matches both SequenceServicer's counter width and KeyCodec's unsigned
// numeric key encoding, so no value a sequence can hand out is unencodable
// and no encoded key is ambiguous.
var rowIDType = types.Uint64

// HasRowID reports whether def is keyed by the synthetic hidden row id --
// that is, whether it declares no PRIMARY KEY of its own. A table for which
// this is true has one more column in the schema SchemaOf returns, and one
// more slot in the rows RowCodec encodes and decodes, than it has declared
// columns.
func HasRowID(def *service.TableDef) bool {
	return def != nil && len(def.PrimaryKey) == 0
}

// rowIDColumn builds the hidden system column SchemaOf appends to a keyless
// table's schema, attributed to the table named source.
//
// Nullable is true and no default is declared, which together are what let
// the value arrive as NULL: go-mysql-server's INSERT planning projects every
// schema column, filling one the statement did not name from its default
// (nil here), and then rejects a NULL in a non-nullable column before the
// row ever reaches the table (see insertIter.validateNullability). Declaring
// the column nullable is therefore what reserves the slot for
// relationalWriter.Insert to allocate into. The column is never NULL in a
// stored row: a row is only ever written under a key encoded from it, and
// KeyCodec refuses to encode a NULL key component.
func rowIDColumn(source string) *sqle.Column {
	return &sqle.Column{
		Name:         RowIDColumnName,
		Type:         rowIDType,
		Nullable:     true,
		HiddenSystem: true,
		Source:       source,
	}
}
