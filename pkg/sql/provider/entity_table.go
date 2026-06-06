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
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/dolthub/vitess/go/sqltypes"

	"github.com/jeremyhahn/go-qrdb/pkg/entity"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// Compile-time interface assertions.
var (
	_ sqle.Table                 = (*EntityTable)(nil)
	_ sqle.InsertableTable       = (*EntityTable)(nil)
	_ sqle.DeletableTable        = (*EntityTable)(nil)
	_ sqle.ReplaceableTable      = (*EntityTable)(nil)
	_ sqle.UpdatableTable        = (*EntityTable)(nil)
	_ sqle.IndexAddressableTable = (*EntityTable)(nil)
	_ sqle.IndexedTable          = (*EntityTable)(nil)
)

// EntityTable implements sql.Table for a registered entity type. Each entity
// type is exposed as a SQL table with an _id BIGINT UNSIGNED primary key and
// columns derived from the entity's Registration field metadata. When indexSvc
// is non-nil, the query planner may accelerate WHERE clause evaluation using
// QRDB secondary indexes built from fields marked Indexed in the Registration.
type EntityTable struct {
	kvSvc      service.KVServicer
	tenantID   string
	reg        *entity.Registration
	schema     sqle.Schema
	fieldOrder []string            // JSON field names in column order (excluding _id)
	indexSvc   index.IndexServicer // nil when index acceleration is not available
}

// NewEntityTable creates a new EntityTable backed by the given KV service,
// scoped to the specified tenant, and described by the entity Registration.
// The SQL schema is derived from reg.Fields. The optional indexSvc enables
// secondary index acceleration for WHERE clause evaluation on Indexed fields.
func NewEntityTable(
	kvSvc service.KVServicer,
	tenantID string,
	reg *entity.Registration,
	indexSvc index.IndexServicer,
) *EntityTable {
	schema, fieldOrder := buildEntitySchema(reg)
	return &EntityTable{
		kvSvc:      kvSvc,
		tenantID:   tenantID,
		reg:        reg,
		schema:     schema,
		fieldOrder: fieldOrder,
		indexSvc:   indexSvc,
	}
}

// Name returns the table name (the entity type name from the Registration).
func (t *EntityTable) Name() string {
	return t.reg.EntityType
}

// String returns a human-readable description.
func (t *EntityTable) String() string {
	return t.reg.EntityType
}

// Schema returns the SQL schema for this entity table.
func (t *EntityTable) Schema() sqle.Schema {
	return t.schema
}

// Collation returns the table's default collation.
func (t *EntityTable) Collation() sqle.CollationID {
	return sqle.Collation_Default
}

// Partitions returns a single-partition iterator.
func (t *EntityTable) Partitions(_ *sqle.Context) (sqle.PartitionIter, error) {
	return sqle.PartitionsToPartitionIter(&collectionPartition{}), nil
}

// PartitionRows returns a row iterator over the entities in the given
// partition. For a standard collectionPartition it performs a prefix scan
// over the entity type's key space. For an indexedPartition it fetches only
// the entities whose keys were returned by the index lookup.
func (t *EntityTable) PartitionRows(ctx *sqle.Context, part sqle.Partition) (sqle.RowIter, error) {
	if ip, ok := part.(*indexedPartition); ok {
		return t.indexedPartitionRows(ctx, ip)
	}
	return t.fullScanRows(ctx)
}

// fullScanRows performs a prefix scan over all entities of this type stored
// in the KV service under the tenant namespace.
func (t *EntityTable) fullScanRows(ctx *sqle.Context) (sqle.RowIter, error) {
	prefix := tenant.PrefixKey(t.tenantID, t.reg.EntityType+"/")
	data, err := t.kvSvc.Scan(ctx, prefix)
	if err != nil {
		return nil, err
	}

	rows := make([]sqle.Row, 0, len(data))
	for _, raw := range data {
		e, err := t.reg.UnmarshalFunc(raw)
		if err != nil {
			// Skip malformed entries; do not propagate to the SQL layer.
			continue
		}
		rows = append(rows, t.entityToRow(e))
	}

	return &indexedRowIter{rows: rows}, nil
}

// indexedPartitionRows fetches only the entities whose keys are held in the
// indexed partition, performing a point lookup per entity key. Keys stored
// in the index have the form "entityType/id".
func (t *EntityTable) indexedPartitionRows(ctx *sqle.Context, ip *indexedPartition) (sqle.RowIter, error) {
	rows := make([]sqle.Row, 0, len(ip.keys))
	for _, rawKey := range ip.keys {
		fullKey := tenant.PrefixKey(t.tenantID, string(rawKey))
		raw, err := t.kvSvc.Get(ctx, fullKey)
		if err != nil {
			// Entity may have been deleted after the index lookup; skip it.
			continue
		}
		e, err := t.reg.UnmarshalFunc(raw)
		if err != nil {
			continue
		}
		rows = append(rows, t.entityToRow(e))
	}
	return &indexedRowIter{rows: rows}, nil
}

// entityToRow converts a typed entity (any) to a SQL row using reflection.
// row[0] is the primary-key field value. Subsequent columns map to fieldOrder
// in alphabetical order. Complex types (maps, slices, structs) are
// JSON-serialized to a string value.
func (t *EntityTable) entityToRow(e any) sqle.Row {
	rv := reflect.ValueOf(e)
	for rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}

	row := make(sqle.Row, 1+len(t.fieldOrder))
	row[0] = reflectFieldByGoName(rv, t.reg.IDFieldGo)

	// Build a lookup from JSONName → FieldMeta for O(1) access.
	metaByJSON := make(map[string]entity.FieldMeta, len(t.reg.Fields))
	for _, f := range t.reg.Fields {
		metaByJSON[f.JSONName] = f
	}

	for i, jsonName := range t.fieldOrder {
		meta, ok := metaByJSON[jsonName]
		if !ok {
			row[i+1] = nil
			continue
		}
		fv := rv.FieldByName(meta.GoName)
		if !fv.IsValid() {
			row[i+1] = nil
			continue
		}
		row[i+1] = coerceReflectToSQL(fv)
	}
	return row
}

// reflectFieldByGoName returns the Go value of the struct field with the
// given name, converted to a SQL-compatible type. Returns nil on failure.
func reflectFieldByGoName(rv reflect.Value, name string) any {
	fv := rv.FieldByName(name)
	if !fv.IsValid() {
		return nil
	}
	return coerceReflectToSQL(fv)
}

// coerceReflectToSQL converts a reflect.Value to a SQL-compatible Go value.
// Complex types (maps, slices that are not []byte, structs) are
// JSON-marshaled to a string.
func coerceReflectToSQL(fv reflect.Value) any {
	if !fv.IsValid() {
		return nil
	}
	iface := fv.Interface()
	switch fv.Kind() {
	case reflect.Map, reflect.Struct:
		data, err := json.Marshal(iface)
		if err != nil {
			return nil
		}
		return string(data)
	case reflect.Slice:
		// []byte passes through as-is; other slices become JSON.
		if fv.Type().Elem().Kind() == reflect.Uint8 {
			return iface
		}
		data, err := json.Marshal(iface)
		if err != nil {
			return nil
		}
		return string(data)
	case reflect.Pointer:
		if fv.IsNil() {
			return nil
		}
		return coerceReflectToSQL(fv.Elem())
	default:
		return iface
	}
}

// ---------------------------------------------------------------------------
// IndexAddressableTable implementation
// ---------------------------------------------------------------------------

// GetIndexes builds SQL index descriptors from the Registration fields that
// carry Indexed == true. When no IndexServicer is configured, an empty slice
// is returned and the query planner falls back to full table scans.
func (t *EntityTable) GetIndexes(_ *sqle.Context) ([]sqle.Index, error) {
	var indexes []sqle.Index
	for _, field := range t.reg.Fields {
		if !field.Indexed {
			continue
		}
		fieldType := goKindToIndexType(field.GoKind)
		indexes = append(indexes, newQRDBIndex(
			t.reg.Database,
			t.reg.EntityType,
			field.JSONName,
			fieldType,
			field.Unique,
		))
	}
	return indexes, nil
}

// IndexedAccess returns the table itself as an IndexedTable for the given
// lookup. EntityTable serves as both IndexAddressable and IndexedTable since
// it implements LookupPartitions.
func (t *EntityTable) IndexedAccess(_ *sqle.Context, _ sqle.IndexLookup) sqle.IndexedTable {
	return t
}

// PreciseMatch reports true, indicating that index lookups for entity rows
// are precise: the index returns exactly the matching entity keys without
// false positives that require post-filter verification.
func (t *EntityTable) PreciseMatch() bool { return true }

// LookupPartitions returns a PartitionIter containing the pre-resolved entity
// keys from the index lookup. The returned partition is consumed by
// PartitionRows to fetch only the matching entities instead of scanning the
// entire entity key space.
func (t *EntityTable) LookupPartitions(ctx *sqle.Context, lookup sqle.IndexLookup) (sqle.PartitionIter, error) {
	if t.indexSvc == nil || lookup.IsEmpty() {
		return t.Partitions(ctx)
	}

	qIdx, ok := lookup.Index.(*qrdbIndex)
	if !ok {
		return t.Partitions(ctx)
	}

	ranges := lookup.Ranges.ToRanges()
	keys, err := translateRanges(ctx, t.indexSvc, qIdx.column, ranges)
	if err != nil {
		return nil, &IndexLookupError{IndexName: qIdx.id, Err: err}
	}

	return sqle.PartitionsToPartitionIter(&indexedPartition{keys: keys}), nil
}

// ---------------------------------------------------------------------------
// DML accessors
// ---------------------------------------------------------------------------

// Inserter returns a RowInserter for the entity table.
func (t *EntityTable) Inserter(_ *sqle.Context) sqle.RowInserter {
	return &entityWriter{
		kvSvc:      t.kvSvc,
		tenantID:   t.tenantID,
		reg:        t.reg,
		fieldOrder: t.fieldOrder,
	}
}

// Deleter returns a RowDeleter for the entity table.
func (t *EntityTable) Deleter(_ *sqle.Context) sqle.RowDeleter {
	return &entityWriter{
		kvSvc:      t.kvSvc,
		tenantID:   t.tenantID,
		reg:        t.reg,
		fieldOrder: t.fieldOrder,
	}
}

// Updater returns a RowUpdater for the entity table.
func (t *EntityTable) Updater(_ *sqle.Context) sqle.RowUpdater {
	return &entityWriter{
		kvSvc:      t.kvSvc,
		tenantID:   t.tenantID,
		reg:        t.reg,
		fieldOrder: t.fieldOrder,
	}
}

// Replacer returns a RowReplacer for the entity table.
func (t *EntityTable) Replacer(_ *sqle.Context) sqle.RowReplacer {
	return &entityWriter{
		kvSvc:      t.kvSvc,
		tenantID:   t.tenantID,
		reg:        t.reg,
		fieldOrder: t.fieldOrder,
	}
}

// ---------------------------------------------------------------------------
// Schema builder
// ---------------------------------------------------------------------------

// buildEntitySchema constructs a sqle.Schema from a Registration's field
// metadata. Column 0 is always _id BIGINT UNSIGNED PRIMARY KEY NOT NULL.
// Remaining columns are derived from non-PK fields sorted alphabetically by
// JSONName for deterministic column order. Returns the schema and the ordered
// list of JSON field names (excluding _id).
func buildEntitySchema(reg *entity.Registration) (sqle.Schema, []string) {
	schema := sqle.Schema{
		{
			Name:       "_id",
			Type:       types.Uint64,
			Nullable:   false,
			PrimaryKey: true,
			Source:     reg.EntityType,
		},
	}

	// Collect non-PK fields and sort them alphabetically for determinism.
	nonPK := make([]entity.FieldMeta, 0, len(reg.Fields))
	for _, f := range reg.Fields {
		if f.IsPrimaryKey {
			continue
		}
		nonPK = append(nonPK, f)
	}
	sort.Slice(nonPK, func(i, j int) bool {
		return nonPK[i].JSONName < nonPK[j].JSONName
	})

	fieldOrder := make([]string, 0, len(nonPK))
	for _, f := range nonPK {
		colType := entitySQLType(f)
		schema = append(schema, &sqle.Column{
			Name:     f.JSONName,
			Type:     colType,
			Nullable: true,
			Source:   reg.EntityType,
		})
		fieldOrder = append(fieldOrder, f.JSONName)
	}

	return schema, fieldOrder
}

// entitySQLType maps an entity.FieldMeta to the corresponding go-mysql-server
// SQL type using entity.GoTypeToSQLType for the full type-to-string mapping.
func entitySQLType(f entity.FieldMeta) sqle.Type {
	sqlTypeStr := entity.GoTypeToSQLType(f.GoType)
	switch sqlTypeStr {
	case "BIGINT UNSIGNED":
		return types.Uint64
	case "BIGINT":
		return types.Int64
	case "DOUBLE":
		return types.Float64
	case "BOOLEAN":
		return types.Boolean
	case "BLOB":
		return types.MustCreateBinary(sqltypes.Blob, 0)
	case "DATETIME":
		return types.Datetime
	case "JSON":
		return types.JSON
	default: // VARCHAR
		return types.MustCreateString(sqltypes.VarChar, 65535, sqle.Collation_Default)
	}
}

// goKindToIndexType maps a reflect.Kind to the QRDB index type string used
// by qrdbIndex for query planning and range translation.
func goKindToIndexType(k reflect.Kind) string {
	switch k {
	case reflect.String:
		return "string"
	case reflect.Uint64, reflect.Uint:
		return "uint64"
	case reflect.Int64, reflect.Int:
		return "int64"
	case reflect.Float64:
		return "float64"
	case reflect.Bool:
		return "boolean"
	default:
		return "string"
	}
}

// ---------------------------------------------------------------------------
// entityWriter
// ---------------------------------------------------------------------------

// entityWriter implements sql.RowInserter, sql.RowDeleter, sql.RowUpdater,
// and sql.RowReplacer by delegating to the KV service layer using reflection
// to marshal/unmarshal entity structs.
type entityWriter struct {
	kvSvc      service.KVServicer
	tenantID   string
	reg        *entity.Registration
	fieldOrder []string
}

// Compile-time interface assertions.
var (
	_ sqle.RowInserter = (*entityWriter)(nil)
	_ sqle.RowDeleter  = (*entityWriter)(nil)
	_ sqle.RowUpdater  = (*entityWriter)(nil)
	_ sqle.RowReplacer = (*entityWriter)(nil)
)

// StatementBegin is called before the first operation of a statement.
func (w *entityWriter) StatementBegin(_ *sqle.Context) {
	// Entity operations are applied immediately; no batching required.
}

// DiscardChanges is called if a statement encounters an error.
func (w *entityWriter) DiscardChanges(_ *sqle.Context, _ error) error {
	// Entity operations are applied immediately; nothing to discard.
	return nil
}

// StatementComplete is called after the last operation of a statement.
func (w *entityWriter) StatementComplete(_ *sqle.Context) error {
	// Entity operations are applied immediately; nothing to finalize.
	return nil
}

// Close finalizes the write operation.
func (w *entityWriter) Close(_ *sqle.Context) error {
	return nil
}

// Insert inserts an entity from a SQL row. Column 0 is _id (uint64),
// remaining columns map to fieldOrder using the Registration's field metadata.
func (w *entityWriter) Insert(ctx *sqle.Context, row sqle.Row) error {
	id, err := extractDocID(row)
	if err != nil {
		return err
	}

	e := w.reg.NewFunc()
	ev := reflect.ValueOf(e)
	for ev.Kind() == reflect.Pointer {
		ev = ev.Elem()
	}

	// Set the primary-key field.
	pkField := ev.FieldByName(w.reg.IDFieldGo)
	if pkField.IsValid() && pkField.CanSet() {
		pkField.SetUint(id)
	}

	// Build a lookup from JSONName → FieldMeta.
	metaByJSON := make(map[string]entity.FieldMeta, len(w.reg.Fields))
	for _, f := range w.reg.Fields {
		metaByJSON[f.JSONName] = f
	}

	// Set field values from the SQL row.
	for i, jsonName := range w.fieldOrder {
		colIdx := i + 1 // skip _id at index 0
		if colIdx >= len(row) || row[colIdx] == nil {
			continue
		}
		meta, ok := metaByJSON[jsonName]
		if !ok {
			continue
		}
		fv := ev.FieldByName(meta.GoName)
		if !fv.IsValid() || !fv.CanSet() {
			continue
		}
		setFieldFromSQL(fv, row[colIdx])
	}

	data, err := w.reg.MarshalFunc(e)
	if err != nil {
		return &InvalidValueError{Reason: "marshal failed: " + err.Error()}
	}

	key := tenant.PrefixKey(w.tenantID, fmt.Sprintf("%s/%s", w.reg.EntityType, strconv.FormatUint(id, 10)))
	return w.kvSvc.Put(ctx, key, data)
}

// Delete deletes an entity by _id from column 0.
func (w *entityWriter) Delete(ctx *sqle.Context, row sqle.Row) error {
	id, err := extractDocID(row)
	if err != nil {
		return err
	}
	key := tenant.PrefixKey(w.tenantID, fmt.Sprintf("%s/%s", w.reg.EntityType, strconv.FormatUint(id, 10)))
	return w.kvSvc.Delete(ctx, key)
}

// Update updates an entity. Deletes the old key if the ID changed, then
// inserts the new entity under the new key.
func (w *entityWriter) Update(ctx *sqle.Context, old sqle.Row, newRow sqle.Row) error {
	oldID, err := extractDocID(old)
	if err != nil {
		return err
	}
	newID, err := extractDocID(newRow)
	if err != nil {
		return err
	}

	if oldID != newID {
		oldKey := tenant.PrefixKey(w.tenantID, fmt.Sprintf("%s/%s", w.reg.EntityType, strconv.FormatUint(oldID, 10)))
		// Best-effort deletion of the old key; ignore errors.
		_ = w.kvSvc.Delete(ctx, oldKey)
	}

	return w.Insert(ctx, newRow)
}

// setFieldFromSQL sets a struct field's value from a SQL row value, handling
// type coercion between SQL engine types and Go types via reflection.
func setFieldFromSQL(fv reflect.Value, val any) {
	if val == nil {
		return
	}
	rv := reflect.ValueOf(val)

	// If the types are directly assignable, use that path first.
	if rv.Type().AssignableTo(fv.Type()) {
		fv.Set(rv)
		return
	}

	// Attempt conversion between compatible numeric/string types.
	if rv.Type().ConvertibleTo(fv.Type()) {
		fv.Set(rv.Convert(fv.Type()))
		return
	}

	// Handle cross-type numeric coercions from SQL engine representations.
	switch fv.Kind() { //nolint:exhaustive // only numeric + string conversions needed
	case reflect.Uint64, reflect.Uint, reflect.Uint32, reflect.Uint16, reflect.Uint8:
		switch v := val.(type) {
		case int64:
			fv.SetUint(uint64(v)) //nolint:gosec // G115: SQL engine may pass int64
		case float64:
			fv.SetUint(uint64(v)) //nolint:gosec // G115: SQL engine may pass float64
		case string:
			if u, err := strconv.ParseUint(v, 10, 64); err == nil {
				fv.SetUint(u)
			}
		}
	case reflect.Int64, reflect.Int, reflect.Int32, reflect.Int16, reflect.Int8:
		switch v := val.(type) {
		case uint64:
			fv.SetInt(int64(v)) //nolint:gosec // G115: safe for typical entity ID ranges
		case float64:
			fv.SetInt(int64(v))
		case string:
			if i, err := strconv.ParseInt(v, 10, 64); err == nil {
				fv.SetInt(i)
			}
		}
	case reflect.Float64, reflect.Float32:
		switch v := val.(type) {
		case int64:
			fv.SetFloat(float64(v))
		case uint64:
			fv.SetFloat(float64(v))
		case string:
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				fv.SetFloat(f)
			}
		}
	case reflect.String:
		switch v := val.(type) {
		case []byte:
			fv.SetString(string(v))
		default:
			fv.SetString(fmt.Sprintf("%v", val))
		}
	case reflect.Bool:
		switch v := val.(type) {
		case int8:
			fv.SetBool(v != 0)
		case int64:
			fv.SetBool(v != 0)
		case string:
			if b, err := strconv.ParseBool(v); err == nil {
				fv.SetBool(b)
			}
		}
	}
}
