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
	"errors"
	"io"
	"sort"
	"strings"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/dolthub/vitess/go/sqltypes"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// tableName is the fixed name of the KV table.
const tableName = "kv"

// kvSchema defines the SQL schema for the KV table: a VARCHAR primary key
// and a BLOB value column.
var kvSchema = sqle.Schema{
	{
		Name:       "key",
		Type:       types.MustCreateString(sqltypes.VarChar, 65535, sqle.Collation_Default),
		Nullable:   false,
		PrimaryKey: true,
		Source:     tableName,
	},
	{
		Name:     "value",
		Type:     types.MustCreateBinary(sqltypes.Blob, 0),
		Nullable: false,
		Source:   tableName,
	},
}

// Compile-time interface assertions.
var (
	_ sqle.Table                 = (*KVTable)(nil)
	_ sqle.InsertableTable       = (*KVTable)(nil)
	_ sqle.DeletableTable        = (*KVTable)(nil)
	_ sqle.ReplaceableTable      = (*KVTable)(nil)
	_ sqle.UpdatableTable        = (*KVTable)(nil)
	_ sqle.IndexAddressableTable = (*KVTable)(nil)
	_ sqle.IndexedTable          = (*KVTable)(nil)
)

// KVTable implements sql.Table, sql.InsertableTable, sql.UpdatableTable,
// sql.DeletableTable, sql.ReplaceableTable, sql.IndexAddressableTable, and
// sql.IndexedTable for the QRDB KV store. Each instance is scoped to a key
// namespace via prefix, which every key read or written through the table
// is composed with (or, when kvSvc is already scoped to the caller's
// namespace — e.g. resolved through service.Registry.KVService — an empty
// prefix, so bare keys pass straight through). The caller (Database.
// GetTableInsensitive) computes prefix once, typically as the composite
// "<tenant>/<db>/" namespace every other protocol uses (see
// tenant.ComposeNamespace). When indexSvc is non-nil, the query planner may
// choose an index access path over a full table scan.
type KVTable struct {
	kvSvc    service.KVServicer
	prefix   string
	indexSvc index.IndexServicer // nil when index acceleration is not available
}

// NewKVTable creates a new KVTable backed by the given KV service. Every
// key the table reads or writes is composed with prefix; pass the empty
// string when kvSvc is already scoped to the desired namespace. The
// optional indexSvc enables secondary index acceleration for WHERE clause
// evaluation.
func NewKVTable(kvSvc service.KVServicer, prefix string, indexSvc index.IndexServicer) *KVTable {
	return &KVTable{
		kvSvc:    kvSvc,
		prefix:   prefix,
		indexSvc: indexSvc,
	}
}

// Name returns the table name.
func (t *KVTable) Name() string {
	return tableName
}

// String returns a human-readable description of the table.
func (t *KVTable) String() string {
	return tableName
}

// Schema returns the table's column schema.
func (t *KVTable) Schema(_ *sqle.Context) sqle.Schema {
	return kvSchema
}

// Collation returns the table's default collation.
func (t *KVTable) Collation() sqle.CollationID {
	return sqle.Collation_Default
}

// Partitions returns an iterator over the table's partitions. The KV table
// uses a single partition.
func (t *KVTable) Partitions(_ *sqle.Context) (sqle.PartitionIter, error) {
	return sqle.PartitionsToPartitionIter(&kvPartition{}), nil
}

// PartitionRows returns an iterator over the rows in the given partition.
// For a standard kvPartition it scans the full KV store using the tenant
// prefix and returns rows sorted by key. For an indexedPartition it
// fetches only the keys pre-selected by the index lookup, avoiding a
// full table scan.
func (t *KVTable) PartitionRows(ctx *sqle.Context, part sqle.Partition) (sqle.RowIter, error) {
	if ip, ok := part.(*indexedPartition); ok {
		return t.indexedPartitionRows(ctx, ip)
	}
	return t.fullScanRows(ctx)
}

// fullScanRows performs a full KV store scan using the table's namespace
// prefix and returns all rows sorted by key.
func (t *KVTable) fullScanRows(ctx *sqle.Context) (sqle.RowIter, error) {
	prefix := t.prefix
	data, err := t.kvSvc.Scan(ctx, prefix)
	if err != nil {
		return nil, err
	}

	// Sort keys for deterministic iteration order.
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	rows := make([]sqle.Row, 0, len(keys))
	for _, k := range keys {
		stripped := strings.TrimPrefix(k, prefix)
		rows = append(rows, sqle.Row{stripped, data[k]})
	}

	return sqle.RowsToRowIter(rows...), nil
}

// indexedPartitionRows fetches only the entity keys held in the indexed
// partition and builds rows from the KV service point lookups.
func (t *KVTable) indexedPartitionRows(ctx *sqle.Context, ip *indexedPartition) (sqle.RowIter, error) {
	prefix := t.prefix
	rows := make([]sqle.Row, 0, len(ip.keys))
	for _, rawKey := range ip.keys {
		key := prefix + string(rawKey)
		val, err := t.kvSvc.Get(ctx, key)
		if err != nil {
			// Key may have been deleted after the index lookup; skip it.
			continue
		}
		stripped := strings.TrimPrefix(key, prefix)
		rows = append(rows, sqle.Row{stripped, val})
	}
	return &indexedRowIter{rows: rows}, nil
}

// Inserter returns a RowInserter for this table.
func (t *KVTable) Inserter(ctx *sqle.Context) sqle.RowInserter {
	return &kvWriter{kvSvc: t.kvSvc, prefix: t.prefix}
}

// Deleter returns a RowDeleter for this table.
func (t *KVTable) Deleter(ctx *sqle.Context) sqle.RowDeleter {
	return &kvWriter{kvSvc: t.kvSvc, prefix: t.prefix}
}

// Updater returns a RowUpdater for this table.
func (t *KVTable) Updater(ctx *sqle.Context) sqle.RowUpdater {
	return &kvWriter{kvSvc: t.kvSvc, prefix: t.prefix}
}

// Replacer returns a RowReplacer for this table.
func (t *KVTable) Replacer(ctx *sqle.Context) sqle.RowReplacer {
	return &kvWriter{kvSvc: t.kvSvc, prefix: t.prefix}
}

// ---------------------------------------------------------------------------
// IndexAddressableTable implementation
// ---------------------------------------------------------------------------

// GetIndexes returns all secondary indexes registered for the KV table. When
// no IndexServicer is configured, an empty slice is returned and the query
// planner falls back to full table scans.
//
// A node whose state machine was built without the index subsystem answers
// the same way, for the same reason. The two are one condition described at
// two layers -- there is no index to consult either way -- and the "memory"
// state machine is exactly such a node. Propagating that as a query error
// instead failed every SELECT carrying a WHERE clause on it, over both wire
// protocols, because the planner asks for a table's indexes before it can
// choose a scan.
func (t *KVTable) GetIndexes(ctx *sqle.Context) ([]sqle.Index, error) {
	if t.indexSvc == nil {
		return nil, nil
	}
	names, err := t.indexSvc.ListIndexes(ctx)
	if err != nil {
		var notConfigured *service.SubsystemNotConfiguredError
		if errors.As(err, &notConfigured) {
			return nil, nil
		}
		return nil, err
	}
	indexes := make([]sqle.Index, 0, len(names))
	for _, name := range names {
		indexes = append(indexes, newQRDBIndex(
			"",        // database not known at this layer
			tableName, // "kv"
			name,
			"string", // default to string type; schema introspection is not available here
			false,
		))
	}
	return indexes, nil
}

// IndexedAccess returns the table itself as an IndexedTable for the given
// lookup. The KVTable serves as both IndexAddressable and IndexedTable
// since it implements LookupPartitions.
func (t *KVTable) IndexedAccess(_ *sqle.Context, _ sqle.IndexLookup) sqle.IndexedTable {
	return t
}

// PreciseMatch reports true, indicating that index lookups for KV table
// rows are precise: the index returns exactly the matching keys without
// false positives that require post-filter verification.
func (t *KVTable) PreciseMatch() bool { return true }

// LookupPartitions returns a PartitionIter containing the pre-resolved
// entity keys from the index lookup embedded in the given IndexLookup. The
// returned partition is consumed by LookupPartitionRows (via PartitionRows)
// to fetch only the matching rows instead of scanning the entire table.
func (t *KVTable) LookupPartitions(ctx *sqle.Context, lookup sqle.IndexLookup) (sqle.PartitionIter, error) {
	if t.indexSvc == nil || lookup.IsEmpty() {
		return t.Partitions(ctx)
	}

	qIdx, ok := lookup.Index.(*qrdbIndex)
	if !ok {
		return t.Partitions(ctx)
	}

	ranges := lookup.Ranges.ToRanges()
	keys, err := translateRanges(ctx, t.indexSvc, qIdx.firstColumn(), ranges)
	if err != nil {
		return nil, &IndexLookupError{IndexName: qIdx.id, Err: err}
	}

	return sqle.PartitionsToPartitionIter(&indexedPartition{keys: keys}), nil
}

// ---------------------------------------------------------------------------
// kvPartition
// ---------------------------------------------------------------------------

// kvPartition is a single-partition implementation for the KV table.
type kvPartition struct{}

// Key returns the partition key.
func (p *kvPartition) Key() []byte {
	return []byte("default")
}

// ---------------------------------------------------------------------------
// kvWriter
// ---------------------------------------------------------------------------

// kvWriter implements sql.RowInserter, sql.RowDeleter, sql.RowUpdater,
// and sql.RowReplacer by delegating to the KV service layer.
type kvWriter struct {
	kvSvc  service.KVServicer
	prefix string
}

// Compile-time interface assertions.
var (
	_ sqle.RowInserter = (*kvWriter)(nil)
	_ sqle.RowDeleter  = (*kvWriter)(nil)
	_ sqle.RowUpdater  = (*kvWriter)(nil)
	_ sqle.RowReplacer = (*kvWriter)(nil)
)

// StatementBegin is called before the first operation of a statement.
func (w *kvWriter) StatementBegin(ctx *sqle.Context) {
	// KV operations are applied immediately; no batching required. The
	// statement is told it writes a non-transactional table, so it cannot
	// also write a relational one (see noteNonTransactionalWriter).
	noteNonTransactionalWriter(ctx, tableName)
}

// DiscardChanges is called if a statement encounters an error.
func (w *kvWriter) DiscardChanges(_ *sqle.Context, _ error) error {
	// KV operations are applied immediately; nothing to discard.
	return nil
}

// StatementComplete is called after the last operation of a statement.
func (w *kvWriter) StatementComplete(_ *sqle.Context) error {
	// KV operations are applied immediately; nothing to finalize.
	return nil
}

// Insert inserts a row into the KV store. The row must contain [key, value]
// where key is a string and value is a []byte.
func (w *kvWriter) Insert(ctx *sqle.Context, row sqle.Row) error {
	if err := requireAutocommit(ctx, tableName); err != nil {
		return err
	}
	key, value, err := extractKeyValue(row)
	if err != nil {
		return err
	}
	prefixed := w.prefix + key
	return w.kvSvc.Put(ctx, prefixed, value)
}

// Delete deletes a row from the KV store. The row must contain at least
// a key field.
func (w *kvWriter) Delete(ctx *sqle.Context, row sqle.Row) error {
	if err := requireAutocommit(ctx, tableName); err != nil {
		return err
	}
	key, err := extractKey(row)
	if err != nil {
		return err
	}
	prefixed := w.prefix + key
	return w.kvSvc.Delete(ctx, prefixed)
}

// Update updates a row in the KV store. If the key changed, the old key is
// deleted and the new key-value pair is inserted. Otherwise, the value is
// overwritten in place.
func (w *kvWriter) Update(ctx *sqle.Context, old sqle.Row, new sqle.Row) error {
	if err := requireAutocommit(ctx, tableName); err != nil {
		return err
	}
	oldKey, err := extractKey(old)
	if err != nil {
		return err
	}
	newKey, newValue, err := extractKeyValue(new)
	if err != nil {
		return err
	}

	prefixedNew := w.prefix + newKey
	if err := w.kvSvc.Put(ctx, prefixedNew, newValue); err != nil {
		return err
	}

	// If the key changed, delete the old entry.
	if oldKey != newKey {
		prefixedOld := w.prefix + oldKey
		return w.kvSvc.Delete(ctx, prefixedOld)
	}

	return nil
}

// Close finalizes the write operation.
func (w *kvWriter) Close(_ *sqle.Context) error {
	return nil
}

// ---------------------------------------------------------------------------
// Row helpers
// ---------------------------------------------------------------------------

// extractKey extracts the key string from a SQL row. The key is expected
// at index 0.
func extractKey(row sqle.Row) (string, error) {
	if len(row) < 1 {
		return "", &InvalidKeyError{Key: ""}
	}
	key, ok := row[0].(string)
	if !ok {
		return "", &InvalidKeyError{Key: ""}
	}
	if key == "" {
		return "", &InvalidKeyError{Key: ""}
	}
	return key, nil
}

// extractKeyValue extracts the key string and value bytes from a SQL row.
// The key is expected at index 0 and the value at index 1.
func extractKeyValue(row sqle.Row) (string, []byte, error) {
	key, err := extractKey(row)
	if err != nil {
		return "", nil, err
	}
	if len(row) < 2 {
		return "", nil, &InvalidValueError{Reason: "row missing value column"}
	}
	value, err := coerceValue(row[1])
	if err != nil {
		return "", nil, err
	}
	return key, value, nil
}

// coerceValue converts a SQL row value to []byte. It accepts []byte and
// string types.
func coerceValue(v interface{}) ([]byte, error) {
	switch val := v.(type) {
	case []byte:
		return val, nil
	case string:
		return []byte(val), nil
	case nil:
		return nil, &InvalidValueError{Reason: "value is nil"}
	default:
		return nil, &InvalidValueError{Reason: "unsupported value type"}
	}
}

// ---------------------------------------------------------------------------
// kvRowIter
// ---------------------------------------------------------------------------

// kvRowIter is a RowIter that iterates over a slice of rows.
type kvRowIter struct {
	rows []sqle.Row
	idx  int
}

// Next returns the next row or io.EOF when exhausted.
func (it *kvRowIter) Next(_ *sqle.Context) (sqle.Row, error) {
	if it.idx >= len(it.rows) {
		return nil, io.EOF
	}
	row := it.rows[it.idx]
	it.idx++
	return row, nil
}

// Close releases resources held by the iterator.
func (it *kvRowIter) Close(_ *sqle.Context) error {
	it.rows = nil
	return nil
}
