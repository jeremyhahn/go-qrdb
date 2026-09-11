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
	"errors"
	"io"
	"sort"
	"strconv"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/expression"
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
	return documentToRow(t.fieldOrder, id, doc)
}

// documentToRow is the single projection from a document to a SQL row, shared
// by the read path (CollectionTable.PartitionRows) and the write path
// (collectionWriter, which needs the row of an EXISTING document to report a
// duplicate key). Two implementations of this mapping would be two chances to
// disagree about the column order, and the row the writer hands back to
// go-mysql-server is fed straight into Delete and Update.
func documentToRow(fieldOrder []string, id uint64, doc map[string]interface{}) sqle.Row {
	row := make(sqle.Row, 1+len(fieldOrder))
	row[0] = id
	for i, field := range fieldOrder {
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

// docIDColumnDefault is the DEFAULT on the _id column: zero, which this
// table reads as "no claim -- let the collection allocate one".
//
// Without a default the column is NOT NULL with nothing to fall back on, so
// `INSERT INTO <collection> (field) VALUES (...)` was rejected outright by
// go-mysql-server with "Field '_id' doesn't have a default value" and the
// collection's own ID strategy was unreachable over SQL: every insert had to
// name an ID, and every ID it named was then discarded (go-qrdb#467).
//
// Zero is the right spelling of "allocate one" rather than an invented
// sentinel: it is MySQL's convention for an AUTO_INCREMENT column, it is
// already what the service layer means by a zero "_id"
// (service.extractExplicitID), and zero is not a document ID -- every other
// collection verb refuses it with CollectionInvalidDocumentIDError.
//
// Spelled as a composite literal rather than through sqle.NewColumnDefaultValue
// because that constructor only fills these same fields and cannot fail: going
// through it would put an unreachable error branch on a package-level variable,
// whose only honest handling at initialisation is a panic.
var docIDColumnDefault = &sqle.ColumnDefaultValue{
	Expr:    expression.NewLiteral(uint64(0), types.Uint64),
	OutType: types.Uint64,
	Literal: true,
}

// buildCollectionSchema constructs a sqle.Schema from a collection's field
// type map. Returns the schema and the ordered list of field names.
//
// The identity column is emitted here and ONLY here. A field named "_id"
// discovered in the collection's schema map is skipped rather than emitted as
// an ordinary column: it names the same thing column 0 already names, and
// declaring it twice gave `SELECT *` two columns called `_id`
// (go-qrdb#467). The service no longer records it -- see
// service.documentFields -- but a collection written before that change still
// carries it in its stored metadata, and this projection is what the table is
// built from.
func buildCollectionSchema(tableName string, fieldSchema map[string]string) (sqle.Schema, []string) {
	// _id is always the first column.
	schema := sqle.Schema{
		{
			Name:       service.DocumentIDField,
			Type:       types.Uint64,
			Nullable:   false,
			PrimaryKey: true,
			Default:    docIDColumnDefault,
			Source:     tableName,
		},
	}

	// Sort field names for deterministic column order.
	fields := make([]string, 0, len(fieldSchema))
	for name := range fieldSchema {
		if name == service.DocumentIDField {
			continue
		}
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

// Insert inserts a document from a SQL row.
//
// Column 0 is the document's _id and it is a CLAIM on that identity, carried
// through to the service layer exactly as every other transport carries it:
// as the document's own "_id". The claim reserves an `increment` collection's
// counter and is refused if another document already holds it, so a document
// written over SQL and read back over REST, gRPC, QUIC or MCP is the same
// document under the same ID (go-qrdb#447, go-qrdb#467). A zero or absent
// column 0 is not a claim -- the collection allocates from its own ID
// strategy, which is what the column's DEFAULT 0 expresses.
//
// A refused claim is reported in go-mysql-server's own vocabulary rather than
// as the service's error, because the refusal is load-bearing beyond the
// error message: `REPLACE INTO` and `INSERT ... ON DUPLICATE KEY UPDATE` are
// both implemented upstream as "insert, and on a primary-key violation carry
// the EXISTING row into a delete or an update" (sql/rowexec/insert.go). Both
// statements were silently inserting a second document instead of replacing
// or updating the first, for want of that error.
func (w *collectionWriter) Insert(ctx *sqle.Context, row sqle.Row) error {
	if err := requireAutocommit(ctx, w.collection); err != nil {
		return err
	}

	doc := w.rowToDocument(row)
	id, err := claimedDocID(row)
	if err != nil {
		return err
	}
	if id != 0 {
		doc[service.DocumentIDField] = id
	}

	// The retry loop exists for one outcome: the insert is refused because an
	// ID is occupied, and by the time the occupant is read back to describe
	// the duplicate it has been deleted. Nothing is then in the way, so the
	// insert is reattempted rather than failed with a duplicate-key error
	// naming a row that no longer exists. This mirrors relationalWriter's
	// treatment of a transient compare-and-swap failure (resolveDuplicate).
	for attempt := 0; attempt < writeMaxAttempts; attempt++ {
		_, insErr := w.collectionSvc.InsertDocument(ctx, &service.InsertDocumentRequest{
			Collection: w.collection,
			Database:   w.database,
			Document:   doc,
		})
		if insErr == nil {
			return nil
		}

		var exists *service.CollectionDocumentExistsError
		if !errors.As(insErr, &exists) {
			return insErr
		}

		if dupErr := w.duplicateKeyError(ctx, exists.ID); dupErr != nil {
			return dupErr
		}
	}
	return &TransactionConflictRetriesExhaustedError{Table: w.collection}
}

// duplicateKeyError describes a refused insert as a go-mysql-server primary
// key violation carrying the row that is in the way.
//
// It returns NIL when the occupying document can no longer be read -- it was
// deleted between the refusal and this read -- because there is then no
// duplicate to report: nothing is in the way any more and the caller should
// reattempt the insert rather than fail it with an error naming a row that
// does not exist.
func (w *collectionWriter) duplicateKeyError(ctx *sqle.Context, id uint64) error {
	resp, err := w.collectionSvc.GetDocument(ctx, &service.GetDocumentRequest{
		Collection: w.collection,
		Database:   w.database,
		ID:         id,
	})
	if err != nil || resp == nil {
		return nil
	}
	return sqle.NewUniqueKeyErr("PRIMARY", true, documentToRow(w.fieldOrder, resp.ID, resp.Document))
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

// Update updates a document.
//
// An UPDATE that changes _id is a RE-IDENTIFICATION: the document has to be
// stored under the new ID and released from the old one. The new ID is
// claimed FIRST, through the same insert path an INSERT uses, so it reserves
// the counter and is refused if another document already holds it; only once
// the document is safely under its new ID is the old one released.
//
// The order matters and was previously the other way round. `UPDATE events
// SET _id = 99 WHERE _id = 1` deleted document 1 and then called
// UpdateDocument for 99, which requires the document to already exist and so
// failed with a not-found -- leaving the statement reporting an error and the
// document gone (go-qrdb#467). A re-identification that cannot complete must
// leave the document where it was.
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
	// Zero is a legal claim on INSERT -- it means "allocate one" -- and is not
	// one here. An UPDATE names the document it is writing, and zero is not a
	// document: every collection verb but insert refuses it. Letting it fall
	// through to the re-identification below would move the document to a
	// freshly allocated ID, which is the silent substitution this whole change
	// removes, one statement over.
	if newID == 0 {
		return &InvalidKeyError{Key: "0"}
	}

	if oldID != newID {
		if insErr := w.Insert(ctx, new); insErr != nil {
			return insErr
		}
		_, delErr := w.collectionSvc.DeleteDocument(ctx, &service.DeleteDocumentRequest{
			Collection: w.collection,
			Database:   w.database,
			ID:         oldID,
		})
		return delErr
	}

	_, updateErr := w.collectionSvc.UpdateDocument(ctx, &service.UpdateDocumentRequest{
		Collection: w.collection,
		Database:   w.database,
		ID:         newID,
		Document:   w.rowToDocument(new),
	})
	return updateErr
}

// rowToDocument converts a SQL row to a document field map.
//
// It returns the document's DATA only. Column 0 is the document's identity,
// not one of its fields -- the collection service spells the same distinction
// as service.documentFields -- so the projection starts at column 1, which is
// where fieldOrder starts. A caller that needs the identity too reads it with
// claimedDocID or extractDocID and adds it under service.DocumentIDField.
func (w *collectionWriter) rowToDocument(row sqle.Row) map[string]interface{} {
	doc := make(map[string]interface{}, len(w.fieldOrder)+1)
	for i, field := range w.fieldOrder {
		colIdx := i + 1 // skip _id at index 0
		if colIdx < len(row) && row[colIdx] != nil {
			doc[field] = row[colIdx]
		}
	}
	return doc
}

// claimedDocID returns the document ID an INSERT row claims through column 0,
// and zero when it claims none.
//
// It differs from extractDocID in exactly one way, and the difference is the
// verb: UPDATE and DELETE address a document that exists, so a missing ID is
// an error there. An INSERT may legitimately leave the collection to allocate
// one, which both an absent column 0 and an explicit zero express (see
// docIDColumnDefault). A value that is present but cannot be read as an ID is
// an error on every verb.
func claimedDocID(row sqle.Row) (uint64, error) {
	if len(row) < 1 || row[0] == nil {
		return 0, nil
	}
	return extractDocID(row)
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
