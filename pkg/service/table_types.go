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

package service

import (
	"strconv"
	"time"
)

// ---------------------------------------------------------------------------
// Relational table catalog
//
// This is the persisted, Raft-replicated description of user-defined
// relational tables — the tables CREATE TABLE produces, as distinct from the
// fixed engine tables (kv, metrics, vectors) and from collections.
//
// Collections are deliberately not reused for this. A collection is a
// schema-inferred document store: its Schema field is derived by sampling
// documents, and it has no concept of column order, nullability, defaults,
// primary keys, or auto-increment. A relational table needs all of those to be
// declared and stable, because a client reads them back as the table's
// definition.
// ---------------------------------------------------------------------------

// TableDef is the persisted definition of a user-defined relational table.
// It is stored as JSON under the catalog metadata key and replicated through
// Raft, so every node resolves the same schema for the same table.
type TableDef struct {
	// TableID is a database-unique, immutable identifier assigned at creation.
	//
	// Row and index keys are composed from this rather than from Name, which
	// is what makes RENAME TABLE a pure catalog compare-and-swap: no data has
	// to move, because no data key mentions the name.
	TableID uint64 `json:"table_id"`

	// Name is the current table name. It changes on rename; TableID does not.
	Name string `json:"name"`

	// Database is the database this table belongs to.
	Database string `json:"database"`

	// Columns is the physical column order and is append-only.
	//
	// Append-only is what keeps ADD COLUMN a metadata-only operation: a row
	// encoded at an earlier schema version is a prefix of the current column
	// list, so it decodes unambiguously against the current definition with
	// the trailing columns filled from their defaults. Reordering or removing
	// a column breaks that property and therefore requires a full rewrite.
	Columns []ColumnDef `json:"columns"`

	// PrimaryKey names the columns forming the primary key, in key order.
	// Empty means the table has a synthetic hidden row id.
	PrimaryKey []PKColumn `json:"primary_key,omitempty"`

	// Indexes are the secondary indexes defined on this table.
	Indexes []TableIndex `json:"indexes,omitempty"`

	// ForeignKeys are the foreign key constraints declared on this table.
	ForeignKeys []ForeignKeyDef `json:"foreign_keys,omitempty"`

	// Checks are the CHECK constraints declared on this table.
	Checks []CheckDef `json:"checks,omitempty"`

	// AutoIncColumn names the AUTO_INCREMENT column, empty when there is none.
	// At most one column may carry it, matching MySQL.
	AutoIncColumn string `json:"auto_inc_column,omitempty"`

	// SchemaVersion increments on every layout-changing DDL. Encoded rows
	// carry the version they were written at so the decoder can tell how many
	// columns are present.
	SchemaVersion uint32 `json:"schema_version"`

	// Collation is the table's default collation, empty for the server default.
	Collation string `json:"collation,omitempty"`

	// Comment is the table comment, if any.
	Comment string `json:"comment,omitempty"`

	// CreatedAt and UpdatedAt record catalog mutation times in UTC.
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ColumnDef describes a single column.
//
// Type is the canonical MySQL type string — "BIGINT UNSIGNED",
// "VARCHAR(255)", "DECIMAL(10,2)" — which is the same vocabulary
// entity.GoTypeToSQLType already produces, so both catalogs read the same way.
type ColumnDef struct {
	Name string `json:"name"`
	Type string `json:"type"`

	// Nullable reports whether the column accepts NULL.
	Nullable bool `json:"nullable"`

	// Default is the column's default as self-describing SQL text -- exactly
	// what (*sql.ColumnDefaultValue).String() emits -- and empty when there
	// is none. The text alone says whether the default is a literal or an
	// expression: an expression is parenthesized (or is one of the bare
	// CURRENT_TIMESTAMP forms MySQL writes without parentheses), and a
	// literal never can be, since the only literals able to contain a
	// parenthesis are strings and those are always quoted. No separate
	// discriminator field is stored, and none is needed; see
	// catalog.ColumnDefaultOf for the reader.
	Default string `json:"default,omitempty"`

	// AutoIncrement marks the column as AUTO_INCREMENT.
	AutoIncrement bool `json:"auto_increment,omitempty"`

	// Comment is the column comment, if any.
	Comment string `json:"comment,omitempty"`

	// Collation is the column collation, empty for the table default.
	Collation string `json:"collation,omitempty"`

	// Extra carries trailing column attributes such as
	// "ON UPDATE CURRENT_TIMESTAMP" that have no dedicated field.
	Extra string `json:"extra,omitempty"`
}

// PKColumn is one column of a primary key.
type PKColumn struct {
	Name string `json:"name"`

	// PrefixLength limits the key to the first N bytes of the column, which
	// MySQL requires for TEXT-ish key columns. Zero means the whole value.
	PrefixLength uint16 `json:"prefix_length,omitempty"`
}

// TableIndex describes a secondary index.
type TableIndex struct {
	Name    string     `json:"name"`
	Columns []IndexCol `json:"columns"`

	// Unique enforces at most one row per index value.
	Unique bool `json:"unique,omitempty"`

	// ForeignKey marks an index created to back a foreign key constraint
	// rather than one the user declared directly.
	ForeignKey bool `json:"foreign_key,omitempty"`

	// Comment is the index comment, if any.
	Comment string `json:"comment,omitempty"`
}

// IndexCol is one column of an index.
type IndexCol struct {
	Name string `json:"name"`

	// PrefixLength limits the indexed value to the first N bytes.
	PrefixLength uint16 `json:"prefix_length,omitempty"`
}

// ForeignKeyDef describes a foreign key constraint declared on a table.
//
// The declaring side (database, table) is not repeated here: it is always
// the TableDef this constraint is stored on. Neither is the parent
// database, because a constraint may only reference a table in its own
// database -- there is no key layout, and no catalog service instance, that
// spans two databases (see the key-layout comment above), so a
// cross-database reference is rejected at the SQL boundary rather than
// stored and silently unenforced.
type ForeignKeyDef struct {
	Name          string   `json:"name"`
	Columns       []string `json:"columns"`
	ParentTable   string   `json:"parent_table"`
	ParentColumns []string `json:"parent_columns"`

	// OnUpdate and OnDelete are the referential actions ("CASCADE",
	// "RESTRICT", "SET NULL", "SET DEFAULT", "NO ACTION"), empty for the
	// default (MySQL's implicit NO ACTION, which behaves as RESTRICT).
	OnUpdate string `json:"on_update,omitempty"`
	OnDelete string `json:"on_delete,omitempty"`

	// Resolved records whether the constraint has been validated against
	// the parent table -- both that the parent exists with a suitable index
	// and that every existing row already satisfies it. It is decided by
	// the SQL engine, never synthesized here, and is false for a constraint
	// added while foreign_key_checks was off, which the engine then
	// re-validates on first use.
	//
	// Zero-value false is the safe default for a catalog record written
	// before this field existed: an unresolved constraint is re-validated,
	// never skipped.
	Resolved bool `json:"resolved,omitempty"`

	// NotValid records that the constraint was declared NOT VALID, meaning
	// rows that existed when it was added were deliberately not checked.
	NotValid bool `json:"not_valid,omitempty"`

	// MatchType selects NULL-handling semantics for a multi-column
	// constraint: 0 (MATCH SIMPLE, the default) exempts a row with any NULL
	// key column from the check, 1 (MATCH FULL) does not, 2 is MATCH
	// PARTIAL. It matches sql.ForeignKeyMatchType's encoding, so 0 is also
	// the correct value for a record written before this field existed.
	MatchType uint8 `json:"match_type,omitempty"`
}

// CheckDef describes a CHECK constraint.
type CheckDef struct {
	Name       string `json:"name"`
	Expression string `json:"expression"`
	Enforced   bool   `json:"enforced"`
}

// ---------------------------------------------------------------------------
// Key layout
//
// Keys are NEVER prefixed with the tenant or database by this package. The
// KVServicer handed to the table service is already scoped to the caller's
// (tenant, database) namespace by service.Registry, which is also what applies
// the barrier, quota, and isolation rules. Composing a prefix here would apply
// a second layer of namespacing on top of that — the exact bug
// provider.WithPrescopedKV exists to prevent.
// ---------------------------------------------------------------------------

const (
	// tableCatalogPrefix is the key space holding table definitions.
	tableCatalogPrefix = "__sys:tables/"

	// tableDDLJournalSuffix marks an in-flight multi-proposal DDL operation.
	// Its presence means a rewrite was interrupted and must be resumed before
	// the table is served.
	tableDDLJournalSuffix = "/__ddl"

	// tableEpochKey is bumped inside every catalog mutation so that a cache,
	// when one is added, can detect staleness with a single read.
	tableEpochSuffix = "/__epoch"

	// tableSequencePrefix is the key space holding auto-increment sequences.
	tableSequencePrefix = "__seq:"

	// tableDataPrefix is the key space holding encoded rows.
	tableDataPrefix = "__tbl:"

	// tableIndexPrefix is the key space holding secondary index entries.
	tableIndexPrefix = "__idx:"

	// tableIDSequenceName is the per-database sequence that allocates TableID.
	tableIDSequenceName = "__tableid"
)

// tableMetadataKey returns the key holding a table's definition.
func tableMetadataKey(database, table string) string {
	return tableCatalogPrefix + database + "/" + table
}

// tableDDLJournalKey returns the key holding a table's in-flight DDL journal.
func tableDDLJournalKey(database, table string) string {
	return tableMetadataKey(database, table) + tableDDLJournalSuffix
}

// tableCatalogEpochKey returns the key holding a database's catalog epoch.
func tableCatalogEpochKey(database string) string {
	return tableCatalogPrefix + database + tableEpochSuffix
}

// tableCatalogScanPrefix returns the prefix enumerating a database's tables.
func tableCatalogScanPrefix(database string) string {
	return tableCatalogPrefix + database + "/"
}

// tableSequenceKey returns the key holding a table's auto-increment sequence.
func tableSequenceKey(database, name string) string {
	return tableSequencePrefix + database + ":" + name
}

// tableDataPrefixFor returns the prefix enumerating a table's rows. Rows are
// keyed by TableID rather than name so a rename does not move them.
func tableDataPrefixFor(database string, tableID uint64) string {
	return tableDataPrefix + database + ":" + strconv.FormatUint(tableID, 10) + "/"
}

// tableIndexPrefixFor returns the prefix enumerating one index's entries.
func tableIndexPrefixFor(database string, tableID uint64, index string) string {
	return tableIndexPrefix + database + ":" +
		strconv.FormatUint(tableID, 10) + ":" + index + "/"
}

// ---------------------------------------------------------------------------
// Requests
// ---------------------------------------------------------------------------

// CreateTableRequest contains parameters for creating a relational table.
type CreateTableRequest struct {
	Database    string          `json:"database"`
	Name        string          `json:"name"`
	Columns     []ColumnDef     `json:"columns"`
	PrimaryKey  []PKColumn      `json:"primary_key,omitempty"`
	Indexes     []TableIndex    `json:"indexes,omitempty"`
	ForeignKeys []ForeignKeyDef `json:"foreign_keys,omitempty"`
	Checks      []CheckDef      `json:"checks,omitempty"`
	Collation   string          `json:"collation,omitempty"`
	Comment     string          `json:"comment,omitempty"`
}

// DropTableRequest contains parameters for dropping a relational table.
type DropTableRequest struct {
	Database string `json:"database"`
	Name     string `json:"name"`
}

// RenameTableRequest contains parameters for renaming a relational table.
type RenameTableRequest struct {
	Database string `json:"database"`
	From     string `json:"from"`
	To       string `json:"to"`
}

// AlterAction identifies a single column mutation within an AlterTableRequest.
type AlterAction string

const (
	// AlterAddColumn appends a column. Metadata-only.
	AlterAddColumn AlterAction = "add_column"

	// AlterDropColumn removes a column. Requires a row rewrite.
	AlterDropColumn AlterAction = "drop_column"

	// AlterModifyColumn changes a column's type or attributes in place.
	// Requires a row rewrite when the encoded width or type changes.
	AlterModifyColumn AlterAction = "modify_column"

	// AlterRenameColumn changes a column's name. Metadata-only.
	AlterRenameColumn AlterAction = "rename_column"
)

// AlterTableChange is one column mutation.
type AlterTableChange struct {
	Action AlterAction `json:"action"`

	// Column is the new or replacement definition, for add and modify.
	Column ColumnDef `json:"column,omitempty"`

	// Name identifies the target column for drop, modify, and rename.
	Name string `json:"name,omitempty"`

	// NewName is the replacement name for rename.
	NewName string `json:"new_name,omitempty"`
}

// AlterTableRequest contains parameters for altering a relational table.
type AlterTableRequest struct {
	Database string             `json:"database"`
	Name     string             `json:"name"`
	Changes  []AlterTableChange `json:"changes"`

	// ExpectedRevision, when non-zero, is the catalog ModRevision the caller
	// read the definition at. The mutation is applied only if the record is
	// still at that revision, so two concurrent DDL statements cannot
	// interleave into a lost update. Zero means the service reads and retries
	// on its own.
	ExpectedRevision int64 `json:"expected_revision,omitempty"`
}

// CreateTableIndexRequest contains parameters for adding a secondary index.
type CreateTableIndexRequest struct {
	Database string     `json:"database"`
	Table    string     `json:"table"`
	Index    TableIndex `json:"index"`
}

// CreateTableForeignKeyRequest contains parameters for adding a foreign key
// constraint to a table's catalog entry.
type CreateTableForeignKeyRequest struct {
	Database   string        `json:"database"`
	Table      string        `json:"table"`
	ForeignKey ForeignKeyDef `json:"foreign_key"`
}

// UpdateTableForeignKeyRequest contains parameters for replacing an
// existing foreign key constraint in place.
type UpdateTableForeignKeyRequest struct {
	Database string `json:"database"`
	Table    string `json:"table"`

	// Name identifies the constraint being replaced. It may differ from
	// ForeignKey.Name, which is how a constraint is renamed.
	Name string `json:"name"`

	ForeignKey ForeignKeyDef `json:"foreign_key"`
}

// TableForeignKey pairs a foreign key constraint with the table that
// declares it. It is what ReferencingForeignKeys returns, since a reverse
// lookup by parent table spans every table in the database and the caller
// needs to know which one each constraint came from.
type TableForeignKey struct {
	Table      string        `json:"table"`
	ForeignKey ForeignKeyDef `json:"foreign_key"`
}

// ---------------------------------------------------------------------------
// DDL journal
// ---------------------------------------------------------------------------

// DDLState identifies the stage of an in-flight multi-proposal DDL operation.
type DDLState string

const (
	// DDLStateRewriting means rows are being re-encoded at a new schema
	// version. The table must not be served until the rewrite completes or is
	// resumed, because its rows are in mixed encodings.
	DDLStateRewriting DDLState = "rewriting"
)

// TableDDLJournal records an in-flight DDL operation so that a crash mid-way
// through a multi-proposal rewrite can be detected and resumed.
//
// A rewrite cannot be atomic: re-encoding every row exceeds what one Raft log
// entry can carry, so it is chunked across proposals. The journal is written
// in the opening compare-and-swap and deleted in the closing one, so its
// presence unambiguously means "interrupted".
type TableDDLJournal struct {
	State DDLState `json:"state"`

	// FromVersion and ToVersion bracket the schema versions being migrated
	// between, so a resumed rewrite knows which rows still need re-encoding.
	FromVersion uint32 `json:"from_version"`
	ToVersion   uint32 `json:"to_version"`

	// Target is the definition being migrated to.
	Target *TableDef `json:"target,omitempty"`

	// Changes carries the AlterTableChange list that triggered this rewrite,
	// so the rewrite executor can deterministically re-derive, for every
	// column of Target, which column of the pre-rewrite definition it came
	// from (a drop removes one, a modify replaces one in place) without
	// having to infer that mapping by diffing two column lists -- which is
	// ambiguous whenever a modify also renames a column.
	Changes []AlterTableChange `json:"changes,omitempty"`

	// StartedAt records when the operation began, in UTC.
	StartedAt time.Time `json:"started_at"`
}
