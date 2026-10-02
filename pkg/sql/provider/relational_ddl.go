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
	"strings"

	sqle "github.com/dolthub/go-mysql-server/sql"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/catalog"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
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
	tableSvc, err := d.registryFor(ctx).TableService(tenantID, d.name)
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

// DropTable drops a user-defined relational table. The drop is one
// TableServicer.DropTable transaction: the catalog entry, generation
// counter and sequences go, and a ReclaimJob for the rows and index entries
// is published with them. The data is not deleted here: a statement that
// resolved the table before the drop may still be reading it, and the
// sweeper deletes it only after ReclaimQuiescence, so no reader ever sees a
// partially-deleted table.
//
// The drop is fenced on the definition and generation resolved here: a
// TRUNCATE or DDL landing in between refuses it with the same retryable
// sql.ErrLockDeadlock a stale DML statement gets, rather than dropping a
// table state the statement never saw -- and never leaking a generation
// whose job nobody would publish.
//
// A table created by v0.20.0 through v0.21.0 (service.
// TableLayoutUnsupportedError) is dropped the same way, unfenced: dropping
// it is the one operation it still supports, and the service names its
// old-layout prefixes in the job.
func (d *Database) DropTable(ctx *sqle.Context, name string) error {
	if d.registry == nil {
		return &RelationalCatalogUnavailableError{Operation: "DROP TABLE"}
	}
	if err := flushPendingWriteSet(ctx); err != nil {
		return err
	}

	tenantID := d.sessionTenantID(ctx)
	tableSvc, err := d.registryFor(ctx).TableService(tenantID, d.name)
	if err != nil {
		return err
	}
	req := &service.DropTableRequest{Database: d.name, Name: name}
	def, err := tableSvc.GetTable(ctx, d.name, name)
	var legacy *service.TableLayoutUnsupportedError
	switch {
	case errors.As(err, &legacy):
	case err != nil:
		return err
	default:
		req.Resolution = def
	}

	// The tenant travels on the context to the ReclaimJob the drop
	// publishes, exactly as TruncateTable's does.
	err = tableSvc.DropTable(tenant.WithTenantID(ctx, tenantID), req)
	if service.IsTableResolutionStale(err) {
		return sqle.ErrLockDeadlock.Wrap(err, err.Error())
	}
	if err == nil && def != nil && d.estimates != nil {
		d.estimates.forget(rowEstimateKey{store: storeID{tenant: tenantID, db: d.name}, tableID: def.TableID, generation: def.Generation})
	}
	return err
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
	tableSvc, err := d.registryFor(ctx).TableService(tenantID, d.name)
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
	kvSvc, err := d.registryFor(ctx).KVService(d.sessionTenantID(ctx), d.name)
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
	kvSvc, err := d.registryFor(ctx).KVService(d.sessionTenantID(ctx), d.name)
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
// Shared small helpers
// ---------------------------------------------------------------------------

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
