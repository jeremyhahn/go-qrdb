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

package vtab

import (
	"context"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	sqlite3 "github.com/mattn/go-sqlite3"
)

// collectionModuleName is the SQLite module name used with
// CREATE VIRTUAL TABLE … USING qrdb_collection.
const collectionModuleName = "qrdb_collection"

// SQL type mapping from CollectionInfo.Schema type strings to SQLite types.
// All non-PK columns are declared NULLABLE because individual documents may
// omit fields that exist in the collection schema.
var schemaTypeMap = map[string]string{
	"VARCHAR": "TEXT",
	"DOUBLE":  "REAL",
	"BOOLEAN": "INTEGER",
	"BIGINT":  "INTEGER",
	"JSON":    "TEXT",
	"BLOB":    "BLOB",
}

// collectionIDX index strategy numbers communicated through BestIndex → Filter.
//
//	colIDXFullScan    — no usable constraint: scan all documents.
//	colIDXPointLookup — _id = ?: fetch a single document by ID.
const (
	colIDXFullScan    = 0
	colIDXPointLookup = 1
)

// colID is the column index of the _id column in the virtual table.
const colID = 0

// ---------------------------------------------------------------------------
// Domain types
// ---------------------------------------------------------------------------

// collectionMeta describes a collection for schema derivation. The schema map
// holds field name → SQL type string entries sourced from CollectionInfo.Schema.
type collectionMeta struct {
	name     string            // collection name
	database string            // database name
	schema   map[string]string // field name → SQL type string (e.g. "VARCHAR")
}

// sortedFields returns the schema field names in deterministic alphabetical
// order, which is used to build the CREATE TABLE DDL and to align column
// indices between the schema declaration and xColumn callbacks.
func (m *collectionMeta) sortedFields() []string {
	fields := make([]string, 0, len(m.schema))
	for f := range m.schema {
		fields = append(fields, f)
	}
	sort.Strings(fields)
	return fields
}

// buildSchema constructs the CREATE TABLE DDL string for this collection. The
// _id column is always first and declared as the primary key; the remaining
// columns follow in alphabetical order and are all nullable.
func (m *collectionMeta) buildSchema() string {
	fields := m.sortedFields()
	var b strings.Builder
	b.WriteString("CREATE TABLE x(_id INTEGER PRIMARY KEY")
	for _, f := range fields {
		sqlType := schemaTypeToSQLite(m.schema[f])
		b.WriteString(", ")
		b.WriteString(f)
		b.WriteString(" ")
		b.WriteString(sqlType)
	}
	b.WriteString(")")
	return b.String()
}

// schemaTypeToSQLite maps a CollectionInfo schema type string to the
// corresponding SQLite affinity keyword. Unknown types default to TEXT.
func schemaTypeToSQLite(t string) string {
	if sqlType, ok := schemaTypeMap[t]; ok {
		return sqlType
	}
	return "TEXT"
}

// DocumentRow is a single document fetched from the collection store. ID is
// the document's primary key; Fields holds the document payload keyed by field
// name.
type DocumentRow struct {
	ID     uint64
	Fields map[string]any
}

// ---------------------------------------------------------------------------
// collectionStore interface
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// collectionModule — implements sqlite3.Module
// ---------------------------------------------------------------------------

// collectionModule is the virtual table module factory for qrdb_collection
// tables. One module instance is shared across all connections registered under
// the same driver name.
type collectionModule struct {
	meta      *collectionMeta
	store     collectionStore
	chunkSize int
}

// Create is called when SQLite executes CREATE VIRTUAL TABLE … USING
// qrdb_collection. It declares the dynamic schema derived from the collection
// metadata and returns a new collectionTable instance.
func (m *collectionModule) Create(c *sqlite3.SQLiteConn, _ []string) (sqlite3.VTab, error) {
	ddl := m.meta.buildSchema()
	if err := c.DeclareVTab(ddl); err != nil {
		return nil, &ModuleCreateError{Module: collectionModuleName, Err: err}
	}
	return m.newTable(), nil
}

// Connect is called when an existing virtual table is re-attached. The schema
// declaration must match Create exactly so SQLite can reconcile the table.
func (m *collectionModule) Connect(c *sqlite3.SQLiteConn, _ []string) (sqlite3.VTab, error) {
	ddl := m.meta.buildSchema()
	if err := c.DeclareVTab(ddl); err != nil {
		return nil, &ModuleCreateError{Module: collectionModuleName, Err: err}
	}
	return m.newTable(), nil
}

// DestroyModule is called when the module is unregistered. No resources to
// release at the module level.
func (m *collectionModule) DestroyModule() {}

// newTable creates a collectionTable that shares the module's store and meta.
func (m *collectionModule) newTable() *collectionTable {
	return &collectionTable{
		meta:      m.meta,
		store:     m.store,
		chunkSize: m.chunkSize,
	}
}

// ---------------------------------------------------------------------------
// collectionTable — implements sqlite3.VTab and sqlite3.VTabUpdater
// ---------------------------------------------------------------------------

// collectionTable is a single virtual table instance. SQLite may create
// multiple collectionTable instances for the same logical table across
// connections.
//
// rowidSeq assigns unique integer rowids to visited rows. rowidIDs maps each
// rowid back to the document's uint64 _id so Delete can recover it from the
// integer rowid SQLite passes to xUpdate.
type collectionTable struct {
	meta      *collectionMeta
	store     collectionStore
	chunkSize int

	// rowidSeq is a monotonically increasing counter for assigning rowids.
	rowidSeq atomic.Int64

	// rowidIDs maps rowid → document _id for rows visited by active cursors.
	// Entries accumulate for the lifetime of the table instance, which is
	// acceptable because the table lives only for one SQLite connection.
	rowidIDs sync.Map //nolint:govet // sync.Map must not be copied after first use
}

// nextRowid returns the next unique rowid and atomically advances the counter.
func (t *collectionTable) nextRowid() int64 {
	return t.rowidSeq.Add(1)
}

// registerRowid stores the document id under the given rowid for later lookup
// by Delete.
func (t *collectionTable) registerRowid(rowid int64, docID uint64) {
	t.rowidIDs.Store(rowid, docID)
}

// lookupRowid returns the document id associated with a rowid, and whether it
// was found.
func (t *collectionTable) lookupRowid(rowid int64) (uint64, bool) {
	v, ok := t.rowidIDs.Load(rowid)
	if !ok {
		return 0, false
	}
	return v.(uint64), true
}

// BestIndex analyses WHERE-clause constraints to choose the optimal query
// strategy. Two strategies are supported:
//
//  1. Point lookup (_id = ?)  → cost 1,    idxNum = colIDXPointLookup
//  2. Full scan (no _id cst)  → cost 1000, idxNum = colIDXFullScan
func (t *collectionTable) BestIndex(csts []sqlite3.InfoConstraint, _ []sqlite3.InfoOrderBy) (*sqlite3.IndexResult, error) {
	used := make([]bool, len(csts))

	for i, c := range csts {
		if !c.Usable || c.Column != colID || c.Op != sqlite3.OpEQ {
			continue
		}
		used[i] = true
		return &sqlite3.IndexResult{
			Used:           used,
			IdxNum:         colIDXPointLookup,
			IdxStr:         "eq",
			AlreadyOrdered: true,
			EstimatedCost:  1,
			EstimatedRows:  1,
		}, nil
	}

	return &sqlite3.IndexResult{
		Used:           used,
		IdxNum:         colIDXFullScan,
		IdxStr:         "scan",
		AlreadyOrdered: false,
		EstimatedCost:  1000,
		EstimatedRows:  1000,
	}, nil
}

// Open creates a new cursor for iterating collection documents.
func (t *collectionTable) Open() (sqlite3.VTabCursor, error) {
	return &collectionCursor{
		table:  t,
		cursor: newPrefetchCursor(t.chunkSize),
	}, nil
}

// Disconnect is called when the connection to this virtual table instance is
// closed. No persistent resources to release.
func (t *collectionTable) Disconnect() error { return nil }

// Destroy is called when the virtual table is dropped. Collection data in QRDB
// persists independently of the SQLite virtual table declaration.
func (t *collectionTable) Destroy() error { return nil }

// Insert handles INSERT INTO <collection> (_id, <fields>…) VALUES (?, …).
//
// The go-sqlite3 VTabUpdater interface passes:
//   - newRowid: provided or auto-assigned rowid (nil when _id is NULL)
//   - values:   column values in schema order: [_id, field0, field1, …]
//
// When _id is NULL the store is expected to auto-generate the document ID.
func (t *collectionTable) Insert(_ interface{}, values []interface{}) (int64, error) {
	doc, id := t.extractDocFields(values)
	ctx := context.Background()

	if id != 0 {
		doc["_id"] = id
	}

	newID, err := t.store.InsertDocument(ctx, t.meta.database, t.meta.name, doc)
	if err != nil {
		return 0, &CollectionInsertError{
			Database:   t.meta.database,
			Collection: t.meta.name,
			Err:        err,
		}
	}
	return int64(newID), nil
}

// Update handles UPDATE <collection> SET field = ? … WHERE _id = ?.
//
// The go-sqlite3 VTabUpdater interface passes:
//   - newRowid: the (possibly changed) rowid — unused for collections
//   - values:   column values in schema order: [_id, field0, field1, …]
func (t *collectionTable) Update(_ interface{}, values []interface{}) error {
	doc, id := t.extractDocFields(values)
	if id == 0 {
		return nil
	}
	ctx := context.Background()
	if err := t.store.UpdateDocument(ctx, t.meta.database, t.meta.name, id, doc); err != nil {
		return &CollectionUpdateError{
			Database:   t.meta.database,
			Collection: t.meta.name,
			ID:         id,
			Err:        err,
		}
	}
	return nil
}

// Delete handles DELETE FROM <collection> WHERE _id = ?.
//
// The go-sqlite3 VTabUpdater interface passes the integer rowid assigned by
// the cursor's Rowid() call during the preceding Filter/Next scan. We recover
// the document _id from the rowidIDs registry.
func (t *collectionTable) Delete(rowid interface{}) error {
	docID, ok := t.resolveDocID(rowid)
	if !ok || docID == 0 {
		return nil
	}
	ctx := context.Background()
	if err := t.store.DeleteDocument(ctx, t.meta.database, t.meta.name, docID); err != nil {
		return &CollectionDeleteError{
			Database:   t.meta.database,
			Collection: t.meta.name,
			ID:         docID,
			Err:        err,
		}
	}
	return nil
}

// extractDocFields unpacks column values into a document field map and
// extracts the _id value. values is ordered as [_id, field0, field1, …]
// where the non-_id fields correspond to the alphabetically sorted schema
// fields from collectionMeta.sortedFields().
func (t *collectionTable) extractDocFields(values []interface{}) (doc map[string]any, id uint64) {
	doc = make(map[string]any)

	if len(values) == 0 {
		return doc, 0
	}

	// values[0] is the _id column.
	if values[0] != nil {
		id = toUint64(values[0])
	}

	// values[1:] correspond to the sorted schema fields.
	fields := t.meta.sortedFields()
	for i, f := range fields {
		idx := i + 1 // offset past _id
		if idx >= len(values) {
			break
		}
		doc[f] = values[idx]
	}
	return doc, id
}

// resolveDocID extracts the document _id from a rowid value passed to xUpdate.
// The rowid may arrive as an int64 (assigned by the cursor's Rowid method)
// which must be mapped back to the original uint64 _id via the rowidIDs
// registry. A direct uint64 or int64 value representing the _id is also
// accepted for DELETE … WHERE _id = ? queries.
func (t *collectionTable) resolveDocID(rowid interface{}) (uint64, bool) {
	switch v := rowid.(type) {
	case int64:
		// Try the registry first (rowid assigned during a scan).
		if docID, ok := t.lookupRowid(v); ok {
			return docID, true
		}
		// Fall back to treating the int64 as a direct document ID (DELETE WHERE
		// _id = ? without a preceding scan).
		if v > 0 {
			return uint64(v), true
		}
		return 0, false
	case uint64:
		return v, v > 0
	case float64:
		id := uint64(v)
		return id, id > 0
	default:
		return 0, false
	}
}

// ---------------------------------------------------------------------------
// collectionCursor — implements sqlite3.VTabCursor
// ---------------------------------------------------------------------------

// collectionCursor iterates documents returned by a collection query. It wraps
// prefetchCursor for row buffering and tracks the current rowid for SQLite.
type collectionCursor struct {
	table  *collectionTable
	cursor *prefetchCursor
	rowid  int64 // current rowid, assigned from table.nextRowid()
}

// Filter populates the cursor based on the query plan chosen by BestIndex.
//
//	colIDXPointLookup — get a single document by _id = vals[0].
//	colIDXFullScan    — list all documents in the collection.
func (c *collectionCursor) Filter(idxNum int, _ string, vals []interface{}) error {
	c.cursor.reset()
	c.rowid = 0

	ctx := context.Background()

	switch idxNum {
	case colIDXPointLookup:
		id := toUint64(vals[0])
		fields, err := c.table.store.GetDocument(ctx, c.table.meta.database, c.table.meta.name, id)
		if err != nil {
			return &CollectionGetError{
				Database:   c.table.meta.database,
				Collection: c.table.meta.name,
				ID:         id,
				Err:        err,
			}
		}
		if fields == nil {
			c.cursor.load(nil)
			return nil
		}
		c.cursor.load([]row{c.buildRow(id, fields)})

	default: // colIDXFullScan
		docs, err := c.table.store.ListDocuments(ctx, c.table.meta.database, c.table.meta.name)
		if err != nil {
			return &CollectionListError{
				Database:   c.table.meta.database,
				Collection: c.table.meta.name,
				Err:        err,
			}
		}
		rows := make([]row, len(docs))
		for i, d := range docs {
			rows[i] = c.buildRow(d.ID, d.Fields)
		}
		c.cursor.load(rows)
	}

	if !c.cursor.isEOF() {
		c.rowid = c.table.nextRowid()
		c.registerCurrentID()
	}
	return nil
}

// Next advances the cursor to the next document.
func (c *collectionCursor) Next() error {
	if err := c.cursor.next(); err != nil {
		if _, ok := err.(*CursorEOFError); ok {
			return nil
		}
		return err
	}
	if !c.cursor.isEOF() {
		c.rowid = c.table.nextRowid()
		c.registerCurrentID()
	}
	return nil
}

// EOF returns true when there are no more documents to return.
func (c *collectionCursor) EOF() bool {
	return c.cursor.isEOF()
}

// Column writes the value for the requested column index into ctx.
//
// Column 0 is _id (INTEGER); columns 1…N are the sorted schema fields.
// Missing field values are returned as NULL so that documents with partial
// fields satisfy the nullable column contract.
func (c *collectionCursor) Column(ctx *sqlite3.SQLiteContext, col int) error {
	val, err := c.cursor.column(col)
	if err != nil {
		return err
	}
	writeColumnValue(ctx, val)
	return nil
}

// Rowid returns the unique identifier for the current row. The rowid is
// assigned during Filter/Next and registered in the table's rowidIDs map so
// that Delete can recover the document _id from the integer rowid.
func (c *collectionCursor) Rowid() (int64, error) {
	return c.rowid, nil
}

// Close releases cursor resources.
func (c *collectionCursor) Close() error {
	c.cursor.reset()
	return nil
}

// buildRow constructs a prefetchCursor row for a document. The row values are
// laid out as [_id, field0, field1, …] matching the alphabetical field order
// declared in the virtual table schema. Fields absent from the document are
// represented as nil (NULL).
func (c *collectionCursor) buildRow(id uint64, fields map[string]any) row {
	schemaFields := c.table.meta.sortedFields()
	values := make([]any, 1+len(schemaFields))
	values[0] = int64(id) // _id as INTEGER
	for i, f := range schemaFields {
		values[i+1] = fields[f] // nil when field is absent
	}
	return row{values: values}
}

// registerCurrentID records the mapping from the current rowid to the current
// document's _id. It is a no-op when the cursor is at EOF.
func (c *collectionCursor) registerCurrentID() {
	if c.cursor.isEOF() {
		return
	}
	v, err := c.cursor.column(colID)
	if err != nil {
		return
	}
	if id, ok := v.(int64); ok && id > 0 {
		c.table.registerRowid(c.rowid, uint64(id))
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// writeColumnValue writes val into a SQLiteContext using the correct result
// method for the Go type. nil is written as NULL; int64 as ResultInt64; float64
// as ResultDouble; []byte as ResultBlob; all other types as ResultText.
func writeColumnValue(ctx *sqlite3.SQLiteContext, val any) {
	if val == nil {
		ctx.ResultNull()
		return
	}
	switch v := val.(type) {
	case int64:
		ctx.ResultInt64(v)
	case int:
		ctx.ResultInt64(int64(v))
	case int32:
		ctx.ResultInt64(int64(v))
	case uint64:
		ctx.ResultInt64(int64(v))
	case float64:
		ctx.ResultDouble(v)
	case float32:
		ctx.ResultDouble(float64(v))
	case bool:
		if v {
			ctx.ResultInt64(1)
		} else {
			ctx.ResultInt64(0)
		}
	case []byte:
		ctx.ResultBlob(v)
	case string:
		ctx.ResultText(v)
	default:
		ctx.ResultText(toString(val))
	}
}

// toUint64 converts an arbitrary SQLite value to a uint64 document ID.
// It handles the common types returned by go-sqlite3: int64, float64, and
// string representations of integers.
func toUint64(v interface{}) uint64 {
	if v == nil {
		return 0
	}
	switch n := v.(type) {
	case int64:
		if n < 0 {
			return 0
		}
		return uint64(n)
	case uint64:
		return n
	case float64:
		if n < 0 {
			return 0
		}
		return uint64(n)
	case int:
		if n < 0 {
			return 0
		}
		return uint64(n)
	}
	return 0
}
