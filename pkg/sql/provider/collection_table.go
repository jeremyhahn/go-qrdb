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
	"io"
	"sort"
	"strconv"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/dolthub/vitess/go/sqltypes"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// Compile-time interface assertions.
var (
	_ sqle.Table                 = (*CollectionTable)(nil)
	_ sqle.InsertableTable       = (*CollectionTable)(nil)
	_ sqle.DeletableTable        = (*CollectionTable)(nil)
	_ sqle.ReplaceableTable      = (*CollectionTable)(nil)
	_ sqle.UpdatableTable        = (*CollectionTable)(nil)
	_ sqle.IndexAddressableTable = (*CollectionTable)(nil)
	_ sqle.IndexedTable          = (*CollectionTable)(nil)
)

// CollectionTable implements sql.Table for a document collection. Each
// collection is exposed as a SQL table with an _id BIGINT UNSIGNED primary
// key and dynamic columns derived from the collection's auto-discovered
// schema. When indexSvc is non-nil, the query planner may accelerate WHERE
// clause evaluation using QRDB secondary indexes.
type CollectionTable struct {
	collectionSvc service.CollectionServicer
	database      string
	collection    string
	schema        sqle.Schema
	fieldOrder    []string            // ordered field names (excluding _id)
	indexSvc      index.IndexServicer // nil when index acceleration is not available
}

// NewCollectionTable creates a new CollectionTable for the named collection
// within the given database. The schema is built from the collection's
// metadata, with an _id primary key and nullable columns for each discovered
// field. The optional indexSvc enables secondary index acceleration for WHERE
// clause evaluation.
func NewCollectionTable(
	collectionSvc service.CollectionServicer,
	database string,
	collection string,
	fieldSchema map[string]string,
	indexSvc index.IndexServicer,
) *CollectionTable {
	schema, fieldOrder := buildCollectionSchema(collection, fieldSchema)
	return &CollectionTable{
		collectionSvc: collectionSvc,
		database:      database,
		collection:    collection,
		schema:        schema,
		fieldOrder:    fieldOrder,
		indexSvc:      indexSvc,
	}
}

// Name returns the table name (the collection name).
func (t *CollectionTable) Name() string {
	return t.collection
}

// String returns a human-readable description.
func (t *CollectionTable) String() string {
	return t.collection
}

// Schema returns the SQL schema for this collection table.
func (t *CollectionTable) Schema(_ *sqle.Context) sqle.Schema {
	return t.schema
}

// Collation returns the table's default collation.
func (t *CollectionTable) Collation() sqle.CollationID {
	return sqle.Collation_Default
}

// Partitions returns a single-partition iterator.
func (t *CollectionTable) Partitions(_ *sqle.Context) (sqle.PartitionIter, error) {
	return sqle.PartitionsToPartitionIter(&collectionPartition{}), nil
}

// PartitionRows returns a row iterator over the documents in the given
// partition. For a standard collectionPartition it fetches all documents in
// the collection. For an indexedPartition it fetches only the documents
// whose IDs were returned by the index lookup.
func (t *CollectionTable) PartitionRows(ctx *sqle.Context, part sqle.Partition) (sqle.RowIter, error) {
	if ip, ok := part.(*indexedPartition); ok {
		return t.indexedPartitionRows(ctx, ip)
	}
	return t.fullScanRows(ctx)
}

// fullScanRows fetches all documents in the collection for SQL full-scan
// execution.
func (t *CollectionTable) fullScanRows(ctx *sqle.Context) (sqle.RowIter, error) {
	resp, err := t.collectionSvc.ListDocuments(ctx, &service.ListDocumentsRequest{
		Collection: t.collection,
		Database:   t.database,
		PageSize:   10000, // fetch all for SQL scan
	})
	if err != nil {
		return nil, err
	}

	rows := make([]sqle.Row, 0, len(resp.Documents))
	for _, doc := range resp.Documents {
		row := t.documentToRow(doc.ID, doc.Document)
		rows = append(rows, row)
	}

	return &collectionRowIter{rows: rows}, nil
}

// indexedPartitionRows fetches the documents whose IDs are held in the
// indexed partition, performing a point lookup per document ID. The raw
// keys from the index are expected to be decimal string representations
// of uint64 document IDs.
func (t *CollectionTable) indexedPartitionRows(ctx *sqle.Context, ip *indexedPartition) (sqle.RowIter, error) {
	rows := make([]sqle.Row, 0, len(ip.keys))
	for _, rawKey := range ip.keys {
		docID, err := strconv.ParseUint(string(rawKey), 10, 64)
		if err != nil {
			// Key is not a valid uint64; skip it gracefully.
			continue
		}
		resp, err := t.collectionSvc.GetDocument(ctx, &service.GetDocumentRequest{
			Collection: t.collection,
			Database:   t.database,
			ID:         docID,
		})
		if err != nil {
			// Document may have been deleted after the index lookup; skip it.
			continue
		}
		if resp != nil {
			rows = append(rows, t.documentToRow(resp.ID, resp.Document))
		}
	}
	return &indexedRowIter{rows: rows}, nil
}

// Inserter returns a RowInserter for the collection table.
func (t *CollectionTable) Inserter(_ *sqle.Context) sqle.RowInserter {
	return &collectionWriter{
		collectionSvc: t.collectionSvc,
		database:      t.database,
		collection:    t.collection,
		fieldOrder:    t.fieldOrder,
	}
}

// Deleter returns a RowDeleter for the collection table.
func (t *CollectionTable) Deleter(_ *sqle.Context) sqle.RowDeleter {
	return &collectionWriter{
		collectionSvc: t.collectionSvc,
		database:      t.database,
		collection:    t.collection,
		fieldOrder:    t.fieldOrder,
	}
}

// Updater returns a RowUpdater for the collection table.
func (t *CollectionTable) Updater(_ *sqle.Context) sqle.RowUpdater {
	return &collectionWriter{
		collectionSvc: t.collectionSvc,
		database:      t.database,
		collection:    t.collection,
		fieldOrder:    t.fieldOrder,
	}
}

// Replacer returns a RowReplacer for the collection table.
func (t *CollectionTable) Replacer(_ *sqle.Context) sqle.RowReplacer {
	return &collectionWriter{
		collectionSvc: t.collectionSvc,
		database:      t.database,
		collection:    t.collection,
		fieldOrder:    t.fieldOrder,
	}
}

// documentToRow converts a document to a SQL row. Column 0 is _id,
// followed by fields in fieldOrder. Missing fields are NULL.
func (t *CollectionTable) documentToRow(id uint64, doc map[string]interface{}) sqle.Row {
	row := make(sqle.Row, 1+len(t.fieldOrder))
	row[0] = id
	for i, field := range t.fieldOrder {
		val, exists := doc[field]
		if !exists {
			row[i+1] = nil
			continue
		}
		row[i+1] = coerceToSQL(val)
	}
	return row
}

// ---------------------------------------------------------------------------
// IndexAddressableTable implementation
// ---------------------------------------------------------------------------

// GetIndexes returns all secondary indexes registered for this collection.
// When no IndexServicer is configured, an empty slice is returned and the
// query planner falls back to full table scans.
func (t *CollectionTable) GetIndexes(ctx *sqle.Context) ([]sqle.Index, error) {
	if t.indexSvc == nil {
		return nil, nil
	}
	names, err := t.indexSvc.ListIndexes(ctx)
	if err != nil {
		return nil, err
	}
	indexes := make([]sqle.Index, 0, len(names))
	for _, name := range names {
		indexes = append(indexes, newQRDBIndex(
			t.database,
			t.collection,
			name,
			"string", // default to string; schema introspection is not available here
			false,
		))
	}
	return indexes, nil
}

// IndexedAccess returns the table itself as an IndexedTable for the given
// lookup. The CollectionTable serves as both IndexAddressable and
// IndexedTable since it implements LookupPartitions.
func (t *CollectionTable) IndexedAccess(_ *sqle.Context, _ sqle.IndexLookup) sqle.IndexedTable {
	return t
}

// PreciseMatch reports true, indicating that index lookups for collection
// rows are precise: the index returns exactly the matching document IDs
// without false positives that require post-filter verification.
func (t *CollectionTable) PreciseMatch() bool { return true }

// LookupPartitions returns a PartitionIter containing the pre-resolved
// document IDs from the index lookup embedded in the given IndexLookup. The
// returned partition is consumed by PartitionRows to fetch only the matching
// documents instead of scanning the entire collection.
func (t *CollectionTable) LookupPartitions(ctx *sqle.Context, lookup sqle.IndexLookup) (sqle.PartitionIter, error) {
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
// Schema builder
// ---------------------------------------------------------------------------

// buildCollectionSchema constructs a sqle.Schema from a collection's field
// type map. Returns the schema and the ordered list of field names.
func buildCollectionSchema(tableName string, fieldSchema map[string]string) (sqle.Schema, []string) {
	// _id is always the first column.
	schema := sqle.Schema{
		{
			Name:       "_id",
			Type:       types.Uint64,
			Nullable:   false,
			PrimaryKey: true,
			Source:     tableName,
		},
	}

	// Sort field names for deterministic column order.
	fields := make([]string, 0, len(fieldSchema))
	for name := range fieldSchema {
		fields = append(fields, name)
	}
	sort.Strings(fields)

	for _, name := range fields {
		colType := sqlTypeFromString(fieldSchema[name])
		schema = append(schema, &sqle.Column{
			Name:     name,
			Type:     colType,
			Nullable: true,
			Source:   tableName,
		})
	}

	return schema, fields
}

// sqlTypeFromString maps a schema type string to a go-mysql-server type.
func sqlTypeFromString(t string) sqle.Type {
	switch t {
	case "VARCHAR":
		return types.MustCreateString(sqltypes.VarChar, 65535, sqle.Collation_Default)
	case "DOUBLE":
		return types.Float64
	case "BOOLEAN":
		return types.Boolean
	case "BIGINT":
		return types.Int64
	case "JSON":
		return types.JSON
	default:
		return types.MustCreateString(sqltypes.VarChar, 65535, sqle.Collation_Default)
	}
}

// coerceToSQL converts a Go value from the document to a SQL-compatible
// value. Maps and slices are serialized to JSON strings.
func coerceToSQL(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}, []interface{}:
		data, err := json.Marshal(val)
		if err != nil {
			return nil
		}
		return string(data)
	default:
		return v
	}
}

// ---------------------------------------------------------------------------
// collectionPartition
// ---------------------------------------------------------------------------

type collectionPartition struct{}

func (p *collectionPartition) Key() []byte {
	return []byte("default")
}

// ---------------------------------------------------------------------------
// collectionRowIter
// ---------------------------------------------------------------------------

type collectionRowIter struct {
	rows []sqle.Row
	idx  int
}

func (it *collectionRowIter) Next(_ *sqle.Context) (sqle.Row, error) {
	if it.idx >= len(it.rows) {
		return nil, io.EOF
	}
	row := it.rows[it.idx]
	it.idx++
	return row, nil
}

func (it *collectionRowIter) Close(_ *sqle.Context) error {
	it.rows = nil
	return nil
}

// ---------------------------------------------------------------------------
// collectionWriter
// ---------------------------------------------------------------------------

type collectionWriter struct {
	collectionSvc service.CollectionServicer
	database      string
	collection    string
	fieldOrder    []string
}

// Compile-time interface assertions.
var (
	_ sqle.RowInserter = (*collectionWriter)(nil)
	_ sqle.RowDeleter  = (*collectionWriter)(nil)
	_ sqle.RowUpdater  = (*collectionWriter)(nil)
	_ sqle.RowReplacer = (*collectionWriter)(nil)
)

func (w *collectionWriter) StatementBegin(_ *sqle.Context) {}

func (w *collectionWriter) DiscardChanges(_ *sqle.Context, _ error) error {
	return nil
}

func (w *collectionWriter) StatementComplete(_ *sqle.Context) error {
	return nil
}

func (w *collectionWriter) Close(_ *sqle.Context) error {
	return nil
}

// Insert inserts a document from a SQL row. Column 0 is _id (may be nil
// for auto-generation), remaining columns map to fieldOrder.
func (w *collectionWriter) Insert(ctx *sqle.Context, row sqle.Row) error {
	if err := requireAutocommit(ctx, w.collection); err != nil {
		return err
	}
	doc := w.rowToDocument(row)
	_, err := w.collectionSvc.InsertDocument(ctx, &service.InsertDocumentRequest{
		Collection: w.collection,
		Database:   w.database,
		Document:   doc,
	})
	return err
}

// Delete deletes a document by _id from column 0.
func (w *collectionWriter) Delete(ctx *sqle.Context, row sqle.Row) error {
	if err := requireAutocommit(ctx, w.collection); err != nil {
		return err
	}
	id, err := extractDocID(row)
	if err != nil {
		return err
	}
	_, delErr := w.collectionSvc.DeleteDocument(ctx, &service.DeleteDocumentRequest{
		Collection: w.collection,
		Database:   w.database,
		ID:         id,
	})
	return delErr
}

// Update updates a document. Deletes the old ID if it changed, then
// inserts the new document.
func (w *collectionWriter) Update(ctx *sqle.Context, old sqle.Row, new sqle.Row) error {
	if err := requireAutocommit(ctx, w.collection); err != nil {
		return err
	}
	oldID, err := extractDocID(old)
	if err != nil {
		return err
	}
	newID, err := extractDocID(new)
	if err != nil {
		return err
	}

	doc := w.rowToDocument(new)

	if oldID != newID {
		_, _ = w.collectionSvc.DeleteDocument(ctx, &service.DeleteDocumentRequest{
			Collection: w.collection,
			Database:   w.database,
			ID:         oldID,
		})
	}

	_, updateErr := w.collectionSvc.UpdateDocument(ctx, &service.UpdateDocumentRequest{
		Collection: w.collection,
		Database:   w.database,
		ID:         newID,
		Document:   doc,
	})
	return updateErr
}

// rowToDocument converts a SQL row to a document field map.
func (w *collectionWriter) rowToDocument(row sqle.Row) map[string]interface{} {
	doc := make(map[string]interface{}, len(w.fieldOrder))
	for i, field := range w.fieldOrder {
		colIdx := i + 1 // skip _id at index 0
		if colIdx < len(row) && row[colIdx] != nil {
			doc[field] = row[colIdx]
		}
	}
	return doc
}

// extractDocID extracts the _id (uint64) from column 0 of a SQL row.
func extractDocID(row sqle.Row) (uint64, error) {
	if len(row) < 1 || row[0] == nil {
		return 0, &InvalidKeyError{Key: ""}
	}
	switch v := row[0].(type) {
	case uint64:
		return v, nil
	case int64:
		return uint64(v), nil //nolint:gosec // G115: SQL engine may pass int64
	case float64:
		return uint64(v), nil //nolint:gosec // G115: SQL engine may pass float64
	case string:
		parsed, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return 0, &InvalidKeyError{Key: v}
		}
		return parsed, nil
	default:
		return 0, &InvalidKeyError{Key: ""}
	}
}
