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
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"
)

// entityModuleName is the SQLite module name used with CREATE VIRTUAL TABLE …
// USING qrdb_entity.
const entityModuleName = "qrdb_entity"

// colEntityID is the column index of the synthetic _id primary-key column.
const colEntityID = 0

// Entity index strategy numbers communicated through BestIndex → Filter.
//
//	entityIdxFullScan  — no usable constraint: enumerate all entities.
//	entityIdxIDLookup  — _id = ?: single point lookup by entity ID.
//	entityIdxEQ        — indexed field = ?: equality index lookup.
//	entityIdxRange     — indexed field with GT/GE/LT/LE: index range scan.
const (
	entityIdxFullScan = 0
	entityIdxIDLookup = 1
	entityIdxEQ       = 2
	entityIdxRange    = 3
)

// ---------------------------------------------------------------------------
// Domain types
// ---------------------------------------------------------------------------

// fieldMeta describes a single entity field for schema derivation and column
// mapping inside the virtual table.
type fieldMeta struct {
	goName     string // Go struct field name, e.g. "Email"
	jsonName   string // JSON tag name used as the SQLite column name, e.g. "email"
	sqliteType string // SQLite affinity: TEXT, INTEGER, REAL, BLOB
	indexed    bool   // true when the field carries an index
	unique     bool   // true when the index is unique
}

// entityMeta holds the metadata for one entity type as used by the virtual
// table. It is populated from a pkg/entity.Registration at registration time.
type entityMeta struct {
	entityType    string
	fields        []fieldMeta // non-PK fields, sorted alphabetically by jsonName
	idFieldJSON   string      // JSON name of the primary-key field
	marshalFunc   func(v any) ([]byte, error)
	unmarshalFunc func(data []byte) (any, error)
	newFunc       func() any
}

// EntityRow represents a deserialized entity with its uint64 primary-key ID
// and a map of json-name → value pairs for all non-PK fields.
type EntityRow struct {
	ID     uint64
	Fields map[string]any
}

// ---------------------------------------------------------------------------
// Type mapping: Go kind → SQLite affinity
// ---------------------------------------------------------------------------

// goKindToSQLiteType maps a reflect.Kind to its SQLite type affinity.
// Composite kinds (map, struct, slice for non-[]byte) fall back to TEXT via
// JSON serialisation at read/write time.
func goKindToSQLiteType(t reflect.Type) string {
	// Special-case time.Time before inspecting the kind.
	if t == reflect.TypeOf(time.Time{}) {
		return "TEXT"
	}
	// []byte → BLOB
	if t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8 {
		return "BLOB"
	}
	switch t.Kind() {
	case reflect.String:
		return "TEXT"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "INTEGER"
	case reflect.Float32, reflect.Float64:
		return "REAL"
	case reflect.Bool:
		return "INTEGER"
	default:
		// map, struct, interface, slice of non-bytes → JSON TEXT
		return "TEXT"
	}
}

// ---------------------------------------------------------------------------
// Schema derivation
// ---------------------------------------------------------------------------

// deriveFields builds the ordered []fieldMeta for all non-PK fields from a
// pkg/entity.Registration-shaped input, sorting alphabetically by JSON name.
//
// Parameters mirror entity.FieldMeta fields so this package has no import
// dependency on pkg/entity.
type rawFieldMeta struct {
	GoName       string
	JSONName     string
	GoType       reflect.Type
	GoKind       reflect.Kind
	IsPrimaryKey bool
	Indexed      bool
	Unique       bool
}

// deriveEntityMeta converts raw registration data into an entityMeta suitable
// for use by the virtual table.
func deriveEntityMeta(
	entityType string,
	rawFields []rawFieldMeta,
	idFieldJSON string,
	marshalFunc func(v any) ([]byte, error),
	unmarshalFunc func(data []byte) (any, error),
	newFunc func() any,
) *entityMeta {
	fields := make([]fieldMeta, 0, len(rawFields))
	for _, rf := range rawFields {
		if rf.IsPrimaryKey {
			continue
		}
		st := goKindToSQLiteType(rf.GoType)
		fields = append(fields, fieldMeta{
			goName:     rf.GoName,
			jsonName:   rf.JSONName,
			sqliteType: st,
			indexed:    rf.Indexed,
			unique:     rf.Unique,
		})
	}
	sort.Slice(fields, func(i, j int) bool {
		return fields[i].jsonName < fields[j].jsonName
	})
	return &entityMeta{
		entityType:    entityType,
		fields:        fields,
		idFieldJSON:   idFieldJSON,
		marshalFunc:   marshalFunc,
		unmarshalFunc: unmarshalFunc,
		newFunc:       newFunc,
	}
}

// buildSchema produces the CREATE TABLE DDL string that SQLite uses to
// understand the column layout of the entity virtual table.
//
// Column 0 is always `_id INTEGER PRIMARY KEY`. Remaining columns are the
// non-PK fields sorted alphabetically by JSON name.
func buildSchema(tableName string, meta *entityMeta) string {
	var b strings.Builder
	b.WriteString("CREATE TABLE ")
	b.WriteString(tableName)
	b.WriteString(" (_id INTEGER PRIMARY KEY")
	for _, f := range meta.fields {
		b.WriteString(", ")
		b.WriteString(f.jsonName)
		b.WriteString(" ")
		b.WriteString(f.sqliteType)
	}
	b.WriteString(")")
	return b.String()
}

// ---------------------------------------------------------------------------
// entityModule — implements sqlite3.Module
// ---------------------------------------------------------------------------

// entityModule is the virtual table module factory for the qrdb_entity table.
// Each entity type gets its own module instance registered under the name
// "qrdb_entity_<entityType>" so that multiple entity types can coexist in the
// same SQLite connection.
type entityModule struct {
	meta      *entityMeta
	store     entityStore
	chunkSize int
}

// Create is called when SQLite executes CREATE VIRTUAL TABLE … USING qrdb_entity.
func (m *entityModule) Create(c *sqlite3.SQLiteConn, args []string) (sqlite3.VTab, error) {
	tableName := parseTableName(args)
	schema := buildSchema(tableName, m.meta)
	if err := c.DeclareVTab(schema); err != nil {
		return nil, &ModuleCreateError{Module: entityModuleName, Err: err}
	}
	return m.newTable(), nil
}

// Connect is called when an existing virtual table is re-attached.
func (m *entityModule) Connect(c *sqlite3.SQLiteConn, args []string) (sqlite3.VTab, error) {
	tableName := parseTableName(args)
	schema := buildSchema(tableName, m.meta)
	if err := c.DeclareVTab(schema); err != nil {
		return nil, &ModuleCreateError{Module: entityModuleName, Err: err}
	}
	return m.newTable(), nil
}

// DestroyModule is called when the module is unregistered.
func (m *entityModule) DestroyModule() {}

// newTable creates a fresh entityTable backed by this module's store and meta.
func (m *entityModule) newTable() *entityTable {
	return &entityTable{
		meta:      m.meta,
		store:     m.store,
		chunkSize: m.chunkSize,
	}
}

// parseTableName extracts the virtual table name from the args slice that
// SQLite passes to Create/Connect. args[2] is the table name per the
// go-sqlite3 convention (args[0]=module, args[1]=db, args[2]=table).
func parseTableName(args []string) string {
	if len(args) >= 3 {
		return args[2]
	}
	return "entity"
}

// ---------------------------------------------------------------------------
// entityTable — implements sqlite3.VTab and sqlite3.VTabUpdater
// ---------------------------------------------------------------------------

// entityTable is a single virtual table instance for one entity type.
// SQLite may create multiple instances across connections; each is independent.
type entityTable struct {
	meta      *entityMeta
	store     entityStore
	chunkSize int
}

// BestIndex analyses WHERE-clause constraints and communicates the cheapest
// query plan back to SQLite.
//
// Strategy selection (cheapest first):
//  1. _id = ?       → entityIdxIDLookup,  cost   1
//  2. indexed = ?   → entityIdxEQ,        cost  10
//  3. indexed range → entityIdxRange,     cost  50
//  4. full scan     → entityIdxFullScan,  cost 1000
//
// The target column index is encoded in IdxStr as a decimal string so that
// Filter knows which field the constraint applies to.
func (t *entityTable) BestIndex(csts []sqlite3.InfoConstraint, _ []sqlite3.InfoOrderBy) (*sqlite3.IndexResult, error) {
	used := make([]bool, len(csts))

	for i, c := range csts {
		if !c.Usable {
			continue
		}
		// Column 0 is _id — point lookup.
		if c.Column == colEntityID && c.Op == sqlite3.OpEQ {
			used[i] = true
			return &sqlite3.IndexResult{
				Used:           used,
				IdxNum:         entityIdxIDLookup,
				IdxStr:         fmt.Sprintf("%d", colEntityID),
				AlreadyOrdered: true,
				EstimatedCost:  1,
				EstimatedRows:  1,
			}, nil
		}
		// Columns ≥ 1 correspond to non-PK fields at index (c.Column-1).
		fieldIdx := c.Column - 1
		if fieldIdx < 0 || fieldIdx >= len(t.meta.fields) {
			continue
		}
		f := t.meta.fields[fieldIdx]
		if !f.indexed {
			continue
		}
		switch c.Op {
		case sqlite3.OpEQ:
			used[i] = true
			return &sqlite3.IndexResult{
				Used:           used,
				IdxNum:         entityIdxEQ,
				IdxStr:         fmt.Sprintf("%d", c.Column),
				AlreadyOrdered: false,
				EstimatedCost:  10,
				EstimatedRows:  10,
			}, nil
		case sqlite3.OpGT, sqlite3.OpGE, sqlite3.OpLT, sqlite3.OpLE:
			used[i] = true
			return &sqlite3.IndexResult{
				Used:           used,
				IdxNum:         entityIdxRange,
				IdxStr:         fmt.Sprintf("%d", c.Column),
				AlreadyOrdered: false,
				EstimatedCost:  50,
				EstimatedRows:  100,
			}, nil
		}
	}

	return &sqlite3.IndexResult{
		Used:           used,
		IdxNum:         entityIdxFullScan,
		IdxStr:         "",
		AlreadyOrdered: false,
		EstimatedCost:  1000,
		EstimatedRows:  1000,
	}, nil
}

// Open creates a new cursor for iterating the virtual table.
func (t *entityTable) Open() (sqlite3.VTabCursor, error) {
	return &entityCursor{
		table:  t,
		cursor: newPrefetchCursor(t.chunkSize),
	}, nil
}

// Disconnect is called when the connection to this virtual table is closed.
func (t *entityTable) Disconnect() error { return nil }

// Destroy is called when the virtual table is dropped.
func (t *entityTable) Destroy() error { return nil }

// Insert handles INSERT INTO <entity> (...) VALUES (...).
//
// The go-sqlite3 VTabUpdater interface passes:
//   - newRowid: the provided or auto-assigned rowid
//   - values:   [_id, ...fields] matching the schema column order
//
// When _id is NULL an auto-ID is generated from the current unix nanosecond
// timestamp.
func (t *entityTable) Insert(newRowid interface{}, values []interface{}) (int64, error) {
	id, data, err := t.extractIDAndFields(newRowid, values)
	if err != nil {
		return 0, err
	}
	ctx := context.Background()
	if err := t.store.PutEntity(ctx, t.meta.entityType, id, data); err != nil {
		return 0, &EntityPutError{EntityType: t.meta.entityType, ID: id, Err: err}
	}
	return int64(id), nil
}

// Update handles UPDATE <entity> SET … WHERE _id = ?.
//
// The go-sqlite3 VTabUpdater interface passes:
//   - newRowid: the (possibly changed) rowid
//   - values:   [_id, ...fields] as the new column values
func (t *entityTable) Update(newRowid interface{}, values []interface{}) error {
	id, data, err := t.extractIDAndFields(newRowid, values)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if err := t.store.PutEntity(ctx, t.meta.entityType, id, data); err != nil {
		return &EntityPutError{EntityType: t.meta.entityType, ID: id, Err: err}
	}
	return nil
}

// Delete handles DELETE FROM <entity> WHERE _id = ?.
//
// rowid is the integer rowid assigned to the row during the preceding scan.
func (t *entityTable) Delete(rowid interface{}) error {
	id := toUint64(rowid)
	if id == 0 {
		return nil
	}
	ctx := context.Background()
	if err := t.store.DeleteEntity(ctx, t.meta.entityType, id); err != nil {
		return &EntityDeleteError{EntityType: t.meta.entityType, ID: id, Err: err}
	}
	return nil
}

// extractIDAndFields unpacks the _id and all non-PK field values from the
// values slice provided by go-sqlite3. values[0] is _id; values[1:] are the
// non-PK field values in schema column order.
//
// When _id is nil (auto-ID on INSERT), a unix nanosecond timestamp is used.
func (t *entityTable) extractIDAndFields(newRowid interface{}, values []interface{}) (uint64, map[string]any, error) {
	// Determine the entity ID.
	var id uint64
	if values[0] == nil {
		// Auto-generate an ID from the current time in nanoseconds.
		id = uint64(time.Now().UnixNano())
	} else {
		id = toUint64(values[0])
		if id == 0 {
			id = uint64(time.Now().UnixNano())
		}
	}

	// Build the field map from the remaining column values.
	data := make(map[string]any, len(t.meta.fields))
	for i, f := range t.meta.fields {
		if i+1 >= len(values) {
			break
		}
		data[f.jsonName] = coerceToField(values[i+1], f.sqliteType)
	}
	return id, data, nil
}

// ---------------------------------------------------------------------------
// entityCursor — implements sqlite3.VTabCursor
// ---------------------------------------------------------------------------

// entityCursor iterates rows returned by an entity query.
//
// currentID holds the _id value of the current row. SQLite uses Rowid() to
// identify rows for DELETE and UPDATE operations; returning the entity's own
// _id here lets Delete/Update receive the correct entity identifier directly.
type entityCursor struct {
	table     *entityTable
	cursor    *prefetchCursor
	currentID int64
}

// Filter populates the cursor based on the query plan chosen by BestIndex.
func (c *entityCursor) Filter(idxNum int, idxStr string, vals []interface{}) error {
	c.cursor.reset()
	c.currentID = 0

	ctx := context.Background()
	meta := c.table.meta

	switch idxNum {
	case entityIdxIDLookup:
		id := toUint64(vals[0])
		if id == 0 {
			c.cursor.load(nil)
			return nil
		}
		fields, err := c.table.store.GetEntity(ctx, meta.entityType, id)
		if err != nil {
			return &EntityGetError{EntityType: meta.entityType, ID: id, Err: err}
		}
		if fields == nil {
			c.cursor.load(nil)
			return nil
		}
		c.cursor.load([]row{entityRowToRow(id, fields, meta)})

	case entityIdxEQ:
		colIdx := parseColIdx(idxStr)
		fieldIdx := colIdx - 1
		if fieldIdx < 0 || fieldIdx >= len(meta.fields) {
			c.cursor.load(nil)
			return nil
		}
		fieldName := meta.fields[fieldIdx].jsonName
		value := anyToString(vals[0])
		rows, err := c.table.store.QueryByIndex(ctx, meta.entityType, fieldName, value)
		if err != nil {
			return &EntityIndexError{EntityType: meta.entityType, FieldName: fieldName, Err: err}
		}
		c.cursor.load(entityRowsToRows(rows, meta))

	case entityIdxRange:
		colIdx := parseColIdx(idxStr)
		fieldIdx := colIdx - 1
		if fieldIdx < 0 || fieldIdx >= len(meta.fields) {
			c.cursor.load(nil)
			return nil
		}
		fieldName := meta.fields[fieldIdx].jsonName
		var start, end string
		if len(vals) >= 1 {
			start = anyToString(vals[0])
		}
		if len(vals) >= 2 {
			end = anyToString(vals[1])
		}
		rows, err := c.table.store.ScanByIndex(ctx, meta.entityType, fieldName, start, end)
		if err != nil {
			return &EntityIndexError{EntityType: meta.entityType, FieldName: fieldName, Err: err}
		}
		c.cursor.load(entityRowsToRows(rows, meta))

	default: // entityIdxFullScan
		rows, err := c.table.store.ScanEntities(ctx, meta.entityType)
		if err != nil {
			return &EntityScanError{EntityType: meta.entityType, Err: err}
		}
		c.cursor.load(entityRowsToRows(rows, meta))
	}

	if !c.cursor.isEOF() {
		c.currentID = c.readCurrentID()
	}
	return nil
}

// Next advances the cursor to the next row.
func (c *entityCursor) Next() error {
	if err := c.cursor.next(); err != nil {
		if _, ok := err.(*CursorEOFError); ok {
			return nil
		}
		return err
	}
	if !c.cursor.isEOF() {
		c.currentID = c.readCurrentID()
	}
	return nil
}

// readCurrentID extracts the _id value from column 0 of the current row.
// Returns 0 when the cursor is at EOF or the value cannot be converted.
func (c *entityCursor) readCurrentID() int64 {
	val, err := c.cursor.column(colEntityID)
	if err != nil {
		return 0
	}
	switch v := val.(type) {
	case int64:
		return v
	case uint64:
		return int64(v)
	}
	return 0
}

// EOF returns true when there are no more rows to return.
func (c *entityCursor) EOF() bool {
	return c.cursor.isEOF()
}

// Column writes the value for column col into ctx.
//
// Column 0 is _id (INTEGER). Columns 1+ are non-PK fields in schema order.
func (c *entityCursor) Column(ctx *sqlite3.SQLiteContext, col int) error {
	val, err := c.cursor.column(col)
	if err != nil {
		return err
	}
	if col == colEntityID {
		switch v := val.(type) {
		case int64:
			ctx.ResultInt64(v)
		case uint64:
			ctx.ResultInt64(int64(v))
		case nil:
			ctx.ResultNull()
		default:
			ctx.ResultNull()
		}
		return nil
	}
	// Non-PK field: derive the SQLite type from metadata.
	fieldIdx := col - 1
	if fieldIdx < 0 || fieldIdx >= len(c.table.meta.fields) {
		ctx.ResultNull()
		return nil
	}
	resultColumnValue(ctx, val, c.table.meta.fields[fieldIdx].sqliteType)
	return nil
}

// Rowid returns the entity's _id as the SQLite rowid. Because _id is declared
// INTEGER PRIMARY KEY in the schema, SQLite treats Rowid() as the authoritative
// row identifier and passes this value back to Delete/Update operations.
func (c *entityCursor) Rowid() (int64, error) {
	return c.currentID, nil
}

// Close releases cursor resources.
func (c *entityCursor) Close() error {
	c.cursor.reset()
	return nil
}

// ---------------------------------------------------------------------------
// Row conversion helpers
// ---------------------------------------------------------------------------

// entityRowToRow converts a single entityRow into a cursor row whose values
// align with the virtual table's schema column order:
// [_id, field[0], field[1], ...] where fields are sorted by JSON name.
func entityRowToRow(id uint64, fields map[string]any, meta *entityMeta) row {
	vals := make([]any, 1+len(meta.fields))
	vals[0] = int64(id)
	for i, f := range meta.fields {
		vals[i+1] = fields[f.jsonName]
	}
	return row{values: vals}
}

// entityRowsToRows converts a slice of EntityRow values into cursor rows.
func entityRowsToRows(rows []EntityRow, meta *entityMeta) []row {
	out := make([]row, len(rows))
	for i, er := range rows {
		out[i] = entityRowToRow(er.ID, er.Fields, meta)
	}
	return out
}

// ---------------------------------------------------------------------------
// Value coercion helpers
// ---------------------------------------------------------------------------

// resultColumnValue writes val into the SQLiteContext using the correct result
// method for the given SQLite type affinity.
func resultColumnValue(ctx *sqlite3.SQLiteContext, val any, sqliteType string) {
	if val == nil {
		ctx.ResultNull()
		return
	}
	switch sqliteType {
	case "INTEGER":
		switch v := val.(type) {
		case int64:
			ctx.ResultInt64(v)
		case int:
			ctx.ResultInt64(int64(v))
		case uint64:
			ctx.ResultInt64(int64(v))
		case float64:
			ctx.ResultInt64(int64(v))
		case bool:
			if v {
				ctx.ResultInt64(1)
			} else {
				ctx.ResultInt64(0)
			}
		default:
			ctx.ResultNull()
		}
	case "REAL":
		switch v := val.(type) {
		case float64:
			ctx.ResultDouble(v)
		case float32:
			ctx.ResultDouble(float64(v))
		case int64:
			ctx.ResultDouble(float64(v))
		default:
			ctx.ResultNull()
		}
	case "BLOB":
		switch v := val.(type) {
		case []byte:
			ctx.ResultBlob(v)
		default:
			ctx.ResultNull()
		}
	default: // TEXT
		switch v := val.(type) {
		case string:
			ctx.ResultText(v)
		case time.Time:
			ctx.ResultText(v.UTC().Format(time.RFC3339Nano))
		default:
			// Composite types are JSON-serialised.
			b, err := json.Marshal(v)
			if err != nil {
				ctx.ResultNull()
				return
			}
			ctx.ResultText(string(b))
		}
	}
}

// coerceToField converts a raw SQLite value to the Go value appropriate for a
// field with the given SQLite type affinity.
func coerceToField(v any, sqliteType string) any {
	if v == nil {
		return nil
	}
	switch sqliteType {
	case "INTEGER":
		switch vv := v.(type) {
		case int64:
			return vv
		case float64:
			return int64(vv)
		case bool:
			if vv {
				return int64(1)
			}
			return int64(0)
		case string:
			return vv
		}
	case "REAL":
		switch vv := v.(type) {
		case float64:
			return vv
		case int64:
			return float64(vv)
		}
	case "BLOB":
		switch vv := v.(type) {
		case []byte:
			return vv
		case string:
			return []byte(vv)
		}
	}
	// TEXT and fallback: return as-is.
	return v
}

// anyToString converts an arbitrary SQLite value to a string for use in index
// queries. The result is always a string representation suitable for
// lexicographic comparison.
func anyToString(v any) string {
	if v == nil {
		return ""
	}
	switch vv := v.(type) {
	case string:
		return vv
	case []byte:
		return string(vv)
	case int64:
		return int64ToStr(vv)
	case float64:
		return float64ToStr(vv)
	}
	return fmt.Sprintf("%v", v)
}

// parseColIdx parses the decimal column index encoded in IdxStr by BestIndex.
// Returns 0 on parse failure.
func parseColIdx(s string) int {
	n := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0
		}
		n = n*10 + int(ch-'0')
	}
	return n
}
