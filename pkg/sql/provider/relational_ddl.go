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
	"strings"

	sqle "github.com/dolthub/go-mysql-server/sql"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/catalog"
)

// Compile-time interface assertions.
var (
	_ sqle.TableCreator        = (*Database)(nil)
	_ sqle.IndexedTableCreator = (*Database)(nil)
	_ sqle.TableDropper        = (*Database)(nil)
	_ sqle.TableRenamer        = (*Database)(nil)
	_ sqle.CollatedDatabase    = (*Database)(nil)
	_ sqle.SchemaValidator     = (*Database)(nil)
)

// databaseCollationKey is the reserved KV key holding a database's default
// collation (see GetCollation/SetCollation). It lives in the same
// tenant/database-scoped KV namespace every relational table catalog key
// does, under the "__sys:" reserved prefix so it can never collide with a
// user table name (validateIdentifier rejects "__" as a leading prefix for
// every user-chosen identifier).
const databaseCollationKey = "__sys:db/collation"

// CreateTable creates a new user-defined relational table. See the
// TableCreator interface doc comment on the translation this performs.
func (d *Database) CreateTable(ctx *sqle.Context, name string, schema sqle.PrimaryKeySchema, collation sqle.CollationID, comment string) error {
	return d.createTable(ctx, name, schema, nil, collation, comment)
}

// CreateIndexedTable creates a new user-defined relational table whose
// primary key carries prefix lengths on one or more of its columns -- what
// MySQL requires for a TEXT-ish primary key column. idxDef.Columns is
// authoritative for the primary key's column list and prefix lengths;
// schema.PkOrdinals is not consulted (see primaryKeyColumns).
func (d *Database) CreateIndexedTable(ctx *sqle.Context, name string, schema sqle.PrimaryKeySchema, idxDef sqle.IndexDef, collation sqle.CollationID, comment string) error {
	return d.createTable(ctx, name, schema, &idxDef, collation, comment)
}

// createTable is the shared implementation behind CreateTable and
// CreateIndexedTable. It validates schema (rejecting any column
// catalog.ColumnTypeOf cannot map, including spatial types) before
// resolving TableServicer, so an unmappable CREATE TABLE fails at DDL time
// rather than being accepted and only discovered as a decode failure on the
// first row.
func (d *Database) createTable(ctx *sqle.Context, name string, schema sqle.PrimaryKeySchema, pkIdxDef *sqle.IndexDef, collation sqle.CollationID, comment string) error {
	if err := d.ValidateSchema(schema.Schema); err != nil {
		return err
	}
	if d.registry == nil {
		return &RelationalCatalogUnavailableError{Operation: "CREATE TABLE"}
	}
	if err := flushPendingWriteSet(ctx); err != nil {
		return err
	}

	tenantID := d.sessionTenantID(ctx)
	tableSvc, err := d.registry.TableService(tenantID, d.name)
	if err != nil {
		return err
	}

	columns := make([]service.ColumnDef, len(schema.Schema))
	for i, col := range schema.Schema {
		cd, cerr := columnDefFromSQL(col)
		if cerr != nil {
			return cerr
		}
		columns[i] = cd
	}

	req := &service.CreateTableRequest{
		Database:   d.name,
		Name:       name,
		Columns:    columns,
		PrimaryKey: primaryKeyColumns(schema, pkIdxDef),
		Collation:  collationString(collation),
		Comment:    comment,
	}
	_, err = tableSvc.CreateTable(ctx, req)
	return err
}

// DropTable drops a user-defined relational table, reclaiming its row and
// secondary-index data before removing the catalog entry.
//
// Ordering matters here and is deliberate: TableServicer.DropTable only
// ever deletes catalog metadata (see its doc comment), so reclaiming the
// row/index data and deleting the catalog entry cannot be a single atomic
// operation the way every other DDL mutation in this package is (a single
// compare-and-swap). Data is reclaimed FIRST and the catalog entry is
// deleted LAST: if a crash interrupts reclamation partway through, the
// table's catalog entry is still present (GetTable still resolves it,
// listing it, describing it, and re-issuing DROP TABLE will all still
// work), so the operation is safely retryable -- a retried DropTable simply
// re-scans the (now smaller) data/index prefixes, deleting nothing twice
// with any lasting effect, and finishes by deleting the now-fully-reclaimed
// catalog entry. The alternative ordering (catalog delete first) would
// instead leave a genuinely unreclaimable orphan on a crash: TableID is
// never reused (see SequenceServicer), so once the catalog record naming it
// is gone, nothing will ever again compute that TableID's data/index
// prefixes to finish deleting them.
func (d *Database) DropTable(ctx *sqle.Context, name string) error {
	if d.registry == nil {
		return &RelationalCatalogUnavailableError{Operation: "DROP TABLE"}
	}
	if err := flushPendingWriteSet(ctx); err != nil {
		return err
	}

	tenantID := d.sessionTenantID(ctx)
	tableSvc, err := d.registry.TableService(tenantID, d.name)
	if err != nil {
		return err
	}
	def, err := tableSvc.GetTable(ctx, d.name, name)
	if err != nil {
		return err
	}

	kvSvc, err := d.registry.KVService(tenantID, d.name)
	if err != nil {
		return err
	}
	batchSvc, err := d.registry.BatchService(tenantID, d.name)
	if err != nil {
		return err
	}

	if err := reclaimTableData(ctx, kvSvc, batchSvc, d.name, def); err != nil {
		return err
	}

	seqSvc, err := d.registry.SequenceService(tenantID, d.name)
	if err != nil {
		return err
	}
	if err := reclaimTableSequences(ctx, seqSvc, d.name, def); err != nil {
		return err
	}

	return tableSvc.DropTable(ctx, &service.DropTableRequest{Database: d.name, Name: name})
}

// RenameTable renames a user-defined relational table. Row and index keys
// are composed from the table's immutable TableID (see
// service.TableDataPrefix/TableIndexPrefix), never from its name, so this
// is a pure catalog operation: no data key changes and no data moves.
func (d *Database) RenameTable(ctx *sqle.Context, oldName, newName string) error {
	if d.registry == nil {
		return &RelationalCatalogUnavailableError{Operation: "RENAME TABLE"}
	}
	if err := flushPendingWriteSet(ctx); err != nil {
		return err
	}

	tenantID := d.sessionTenantID(ctx)
	tableSvc, err := d.registry.TableService(tenantID, d.name)
	if err != nil {
		return err
	}
	return tableSvc.RenameTable(ctx, &service.RenameTableRequest{Database: d.name, From: oldName, To: newName})
}

// GetCollation returns the database's default collation, or
// sqle.Collation_Default when none has ever been set (or the registry is
// unavailable, or nothing has been resolved yet for this session's
// tenant) -- GetCollation has no error return, so any failure resolving the
// stored value falls back to the server default rather than surfacing to
// the caller.
//
// The collation is stored as a plain KV value in the same tenant/database
// KV namespace every relational table catalog key uses, under
// databaseCollationKey. This is a database-level setting distinct from any
// individual table's own collation (RelationalTable.Collation), and QRDB
// has no other persisted "database record" of its own to carry it on --
// unlike table metadata, which already has a natural home in
// TableServicer's catalog.
func (d *Database) GetCollation(ctx *sqle.Context) sqle.CollationID {
	if d.registry == nil {
		return sqle.Collation_Default
	}
	kvSvc, err := d.registry.KVService(d.sessionTenantID(ctx), d.name)
	if err != nil {
		return sqle.Collation_Default
	}
	raw, err := kvSvc.Get(ctx, databaseCollationKey)
	if err != nil || len(raw) == 0 {
		return sqle.Collation_Default
	}
	id, err := sqle.ParseCollation("", string(raw), false)
	if err != nil {
		return sqle.Collation_Default
	}
	return id
}

// SetCollation updates the database's default collation. See the doc
// comment on GetCollation for where and why it is stored.
func (d *Database) SetCollation(ctx *sqle.Context, collation sqle.CollationID) error {
	if d.registry == nil {
		return &RelationalCatalogUnavailableError{Operation: "ALTER DATABASE COLLATE"}
	}
	if err := flushPendingWriteSet(ctx); err != nil {
		return err
	}
	kvSvc, err := d.registry.KVService(d.sessionTenantID(ctx), d.name)
	if err != nil {
		return err
	}
	return kvSvc.Put(ctx, databaseCollationKey, []byte(collationString(collation)))
}

// ValidateSchema rejects any column whose type catalog.ColumnTypeOf cannot
// map to a persisted TableDef column -- GEOMETRY and the other spatial
// types, and anything else outside the vocabulary TableServicer accepts --
// at CREATE/ALTER TABLE time, before any row is ever written under the
// rejected schema. It reuses catalog.TypeStringOf/ColumnTypeOf's own
// spatial-type rejection rather than re-declaring the spatial type list
// here, so the two catalogs never drift out of sync about what they
// consider a spatial type.
func (d *Database) ValidateSchema(schema sqle.Schema) error {
	for _, col := range schema {
		typeStr := catalog.TypeStringOf(col.Type)
		if _, err := catalog.ColumnTypeOf(service.ColumnDef{Name: col.Name, Type: typeStr}); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// GMS Column -> service.ColumnDef / TableIndex translation
// ---------------------------------------------------------------------------

// columnDefFromSQL translates a go-mysql-server column into the persisted
// ColumnDef vocabulary. Generated (virtual/stored-expression) columns are
// rejected: TableDef has no concept of a generated column, and silently
// dropping the generation expression would leave the column looking like an
// ordinary one with no way to populate it.
func columnDefFromSQL(col *sqle.Column) (service.ColumnDef, error) {
	if col.Generated != nil {
		return service.ColumnDef{}, &GeneratedColumnUnsupportedError{Column: col.Name}
	}

	cd := service.ColumnDef{
		Name:          col.Name,
		Type:          catalog.TypeStringOf(col.Type),
		Nullable:      col.Nullable,
		AutoIncrement: col.AutoIncrement,
		Comment:       col.Comment,
		Extra:         col.Extra,
	}
	if col.Default != nil && col.Default.Expr != nil {
		// (*sql.ColumnDefaultValue).String() is self-describing: it emits a
		// literal verbatim and an expression parenthesized (bar the bare
		// CURRENT_TIMESTAMP forms), so the persisted text alone says which
		// of the two it is. Persisting Expr.String() instead would drop
		// those parentheses and force a separate discriminator field
		// alongside it; see the ColumnDef.Default doc comment.
		cd.Default = col.Default.String()
	}
	return cd, nil
}

// primaryKeyColumns builds the persisted primary-key column list for a
// CREATE TABLE request. When pkIdxDef is non-nil (CreateIndexedTable), its
// Columns -- which carry prefix lengths -- are authoritative for both
// membership and order; otherwise the primary key is read from
// schema.PkOrdinals with no prefix length, matching a plain CreateTable.
func primaryKeyColumns(schema sqle.PrimaryKeySchema, pkIdxDef *sqle.IndexDef) []service.PKColumn {
	if pkIdxDef != nil && len(pkIdxDef.Columns) > 0 {
		cols := make([]service.PKColumn, len(pkIdxDef.Columns))
		for i, c := range pkIdxDef.Columns {
			cols[i] = service.PKColumn{Name: c.Name, PrefixLength: prefixLengthOf(c.Length)}
		}
		return cols
	}

	cols := make([]service.PKColumn, len(schema.PkOrdinals))
	for i, ord := range schema.PkOrdinals {
		cols[i] = service.PKColumn{Name: schema.Schema[ord].Name}
	}
	return cols
}

// prefixLengthOf clamps a GMS index column length (which may be zero,
// meaning "no prefix", or negative, which should never occur but is
// defended against regardless) to the uint16 PKColumn.PrefixLength/
// IndexCol.PrefixLength range.
func prefixLengthOf(length int64) uint16 {
	switch {
	case length <= 0:
		return 0
	case length > 65535:
		return 65535
	default:
		return uint16(length)
	}
}

// collationString renders a GMS collation ID as the persisted TableDef.
// Collation text, treating the unspecified collation the same as "no
// collation declared" (empty string) so a table/database created without an
// explicit COLLATE clause round-trips back to sqle.Collation_Default rather
// than pinning an arbitrary concrete collation it never actually requested.
func collationString(c sqle.CollationID) string {
	if c == sqle.Collation_Unspecified {
		return ""
	}
	return c.Name()
}

// ---------------------------------------------------------------------------
// DropTable data reclamation
// ---------------------------------------------------------------------------

const (
	// ddlChunkMaxOps bounds the number of operations grouped into a single
	// chunked DDL data-reclamation proposal, guarding against a pathological
	// proposal op count even when every individual operation is tiny.
	ddlChunkMaxOps = 500

	// ddlChunkMaxBytes bounds the estimated encoded size of a single chunked
	// DDL data-reclamation proposal. It is set well under go-quicraft's
	// per-entry ceiling (8 MiB) to leave headroom for op framing overhead
	// this estimate does not account for exactly.
	ddlChunkMaxBytes = 4 << 20 // 4 MB

	// ddlOpOverheadBytes is a conservative fixed per-operation estimate
	// (framing, key/value length prefixes) added on top of an op's own
	// key+value bytes when deciding where to cut a chunk.
	ddlOpOverheadBytes = 32
)

// reclaimTableData deletes every row and secondary-index entry belonging to
// def, chunked so no single proposal exceeds ddlChunkMaxOps/ddlChunkMaxBytes.
// See the doc comment on Database.DropTable for why this must run before the
// catalog entry is deleted, not after.
func reclaimTableData(ctx context.Context, kvSvc service.KVServicer, batchSvc service.BatchServicer, database string, def *service.TableDef) error {
	prefixes := make([]string, 0, 1+len(def.Indexes))
	prefixes = append(prefixes, service.TableDataPrefix(database, def.TableID))
	for _, idx := range def.Indexes {
		prefixes = append(prefixes, service.TableIndexPrefix(database, def.TableID, idx.Name))
	}
	for _, prefix := range prefixes {
		if err := deleteByPrefix(ctx, kvSvc, batchSvc, prefix); err != nil {
			return err
		}
	}
	return nil
}

// reclaimTableSequences deletes the two sequence counters keyed by def's
// TableID: the AUTO_INCREMENT counter and the hidden row-id counter.
//
// Both are deleted unconditionally, without first asking whether the table
// declared an AUTO_INCREMENT column or lacked a PRIMARY KEY. SequenceServicer
// documents Delete as idempotent and forbids an implementation from returning
// a not-found error, precisely so a teardown can be re-issued; an absent
// counter is already the replicated state and proposes nothing. Reading the
// column definitions to decide would add a branch that can be wrong -- a table
// altered to drop its AUTO_INCREMENT column still has the counter -- in
// exchange for nothing.
//
// It runs BEFORE the catalog entry is deleted, for the same reason
// reclaimTableData does: both counter names are derived from the immutable
// TableID, and after the catalog row is gone there is nothing left that maps
// this table's name to that id. A failure here therefore leaves a droppable
// table rather than an unreachable pair of counters, and DROP TABLE is
// retryable.
//
// This is DROP's teardown, not TRUNCATE's. TRUNCATE reuses reclaimTableData
// but must not reuse this: MySQL resets a truncated table's AUTO_INCREMENT
// rather than removing it, and this package does neither today (go-qrdb#547).
func reclaimTableSequences(ctx context.Context, seqSvc service.SequenceServicer, database string, def *service.TableDef) error {
	for _, name := range []string{
		service.TableAutoIncrementSequenceName(def.TableID),
		service.TableRowIDSequenceName(def.TableID),
	} {
		if err := seqSvc.Delete(ctx, database, name); err != nil {
			return err
		}
	}
	return nil
}

// deleteByPrefix deletes every key under prefix, proposing the deletes in
// chunks via BatchServicer rather than one at a time, so reclaiming a large
// table's data does not issue one Raft round trip per row.
func deleteByPrefix(ctx context.Context, kvSvc service.KVServicer, batchSvc service.BatchServicer, prefix string) error {
	keys, err := kvSvc.List(ctx, prefix)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return nil
	}

	ops := make([]service.Op, len(keys))
	for i, k := range keys {
		ops[i] = service.Op{Type: service.OpDelete, Key: []byte(k)}
	}
	return executeDDLChunks(ctx, batchSvc, ops)
}

// executeDDLChunks proposes ops in bounded chunks (see buildDDLChunks)
// through batchSvc, in order, stopping at the first error.
func executeDDLChunks(ctx context.Context, batchSvc service.BatchServicer, ops []service.Op) error {
	for _, chunk := range buildDDLChunks(ops) {
		if _, err := batchSvc.Execute(ctx, chunk); err != nil {
			return err
		}
	}
	return nil
}

// buildDDLChunks splits ops into chunks bounded by both ddlChunkMaxOps and
// an estimated ddlChunkMaxBytes, mirroring the same chunking discipline
// service.BatchService's own import path applies against go-quicraft's
// per-entry proposal ceiling. Every chunk contains at least one op
// regardless of size, since a single oversized op has nowhere smaller to
// go; ddlOpOverheadBytes gives enough headroom below the real 8 MiB Raft
// entry ceiling that this is not expected to matter in practice for the
// key-only (DropTable reclamation) and row (rewrite) payloads this is used
// for.
func buildDDLChunks(ops []service.Op) [][]service.Op {
	if len(ops) == 0 {
		return nil
	}

	var chunks [][]service.Op
	cur := make([]service.Op, 0, min(len(ops), ddlChunkMaxOps))
	curBytes := 0

	for _, op := range ops {
		opBytes := len(op.Key) + len(op.Value) + ddlOpOverheadBytes
		if len(cur) > 0 && (len(cur) >= ddlChunkMaxOps || curBytes+opBytes > ddlChunkMaxBytes) {
			chunks = append(chunks, cur)
			cur = make([]service.Op, 0, min(len(ops), ddlChunkMaxOps))
			curBytes = 0
		}
		cur = append(cur, op)
		curBytes += opBytes
	}
	if len(cur) > 0 {
		chunks = append(chunks, cur)
	}
	return chunks
}

// ---------------------------------------------------------------------------
// Shared small helpers
// ---------------------------------------------------------------------------

// deleteAllByPrefix deletes every key under prefix one at a time through
// kvSvc.Delete. It is used by RelationalTable's index maintenance
// (DropIndex, RenameIndex), which -- unlike Database.DropTable's bulk
// reclamation -- has no BatchServicer available and operates at the same
// one-entry-at-a-time granularity as BuildIndex's population loop.
func deleteAllByPrefix(ctx context.Context, kvSvc service.KVServicer, prefix string) error {
	keys, err := kvSvc.List(ctx, prefix)
	if err != nil {
		return err
	}
	for _, k := range keys {
		if err := kvSvc.Delete(ctx, k); err != nil {
			return err
		}
	}
	return nil
}

// indexValueKeyCodec builds a catalog.KeyCodec for encoding a secondary
// index's column values in the same order-preserving format primary keys
// use, by constructing a synthetic "primary key" over cols within def's
// column list. This is what lets BuildIndex and the secondary-index lookup
// path reuse KeyCodec's encoding -- including its multi-component composite
// encoding, which composite PRIMARY KEYs already depend on -- instead of
// duplicating it.
//
// cols may be any leading prefix of an index's columns: because KeyCodec
// concatenates self-delimiting per-component encodings, a codec over the
// first k columns produces exactly the byte prefix the full codec produces
// for the same k values. See indexSpec.codecs.
func indexValueKeyCodec(def *service.TableDef, cols []sqle.IndexColumn) (*catalog.KeyCodec, error) {
	pk := make([]service.PKColumn, len(cols))
	for i, col := range cols {
		pk[i] = service.PKColumn{Name: col.Name, PrefixLength: prefixLengthOf(col.Length)}
	}
	synthetic := &service.TableDef{
		Name:       def.Name,
		Columns:    def.Columns,
		PrimaryKey: pk,
	}
	return catalog.NewKeyCodec(synthetic)
}

// tableIndexFromDef translates a GMS index definition into the persisted
// TableIndex catalog vocabulary.
func tableIndexFromDef(indexDef sqle.IndexDef) (service.TableIndex, error) {
	if len(indexDef.Columns) == 0 {
		return service.TableIndex{}, &IndexNoColumnsError{Index: indexDef.Name}
	}
	cols := make([]service.IndexCol, len(indexDef.Columns))
	for i, c := range indexDef.Columns {
		cols[i] = service.IndexCol{Name: c.Name, PrefixLength: prefixLengthOf(c.Length)}
	}
	return service.TableIndex{
		Name:    indexDef.Name,
		Columns: cols,
		Unique:  indexDef.IsUnique(),
		Comment: indexDef.Comment,
	}, nil
}

// findTableIndex returns a pointer to the index named name (matched
// case-insensitively) within def.Indexes, or nil when none matches.
func findTableIndex(def *service.TableDef, name string) *service.TableIndex {
	for i := range def.Indexes {
		if strings.EqualFold(def.Indexes[i].Name, name) {
			return &def.Indexes[i]
		}
	}
	return nil
}
