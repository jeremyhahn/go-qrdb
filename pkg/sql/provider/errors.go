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

// Package provider implements the go-mysql-server DatabaseProvider interface
// backed by QRDB's KV service layer. It maps SQL operations to KV Put, Get,
// Delete, and Scan calls, enabling SQL access to the distributed key-value
// store.
package provider

import (
	"strconv"

	sqle "github.com/dolthub/go-mysql-server/sql"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// DatabaseNotFoundError is returned when a requested database does not
// exist in the provider.
type DatabaseNotFoundError struct {
	Name string
}

// Error returns a human-readable description.
func (e *DatabaseNotFoundError) Error() string {
	return "provider: database not found: " + e.Name
}

// InvalidKeyError is returned when a key value cannot be used as a KV
// store key (e.g., empty string or wrong type).
type InvalidKeyError struct {
	Key string
}

// Error returns a human-readable description.
func (e *InvalidKeyError) Error() string {
	return "provider: invalid key: " + e.Key
}

// InvalidValueError is returned when a value cannot be stored (e.g.,
// nil or unsupported type).
type InvalidValueError struct {
	Reason string
}

// Error returns a human-readable description.
func (e *InvalidValueError) Error() string {
	return "provider: invalid value: " + e.Reason
}

// DatabaseLifecycleUnavailableError is returned when a non-system SQL session
// issues CREATE DATABASE or DROP DATABASE on a provider that has no
// tenant-scoped database service or no lifecycle authorizer: without them the
// statement could be neither authorized nor attributed to the session's
// tenant, so it is refused rather than run as an unmetered, unowned
// operator action.
type DatabaseLifecycleUnavailableError struct {
	Operation string
	TenantID  string
}

// Error returns a human-readable description.
func (e *DatabaseLifecycleUnavailableError) Error() string {
	return "provider: " + e.Operation + " is not available to tenant " + e.TenantID +
		": no tenant-scoped database lifecycle is configured"
}

// CreateDatabaseError is returned when a CREATE DATABASE operation fails.
type CreateDatabaseError struct {
	Name string
	Err  error
}

// Error returns a human-readable description.
func (e *CreateDatabaseError) Error() string {
	return "provider: create database failed: " + e.Name + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *CreateDatabaseError) Unwrap() error {
	return e.Err
}

// DropDatabaseError is returned when a DROP DATABASE operation fails.
type DropDatabaseError struct {
	Name string
	Err  error
}

// Error returns a human-readable description.
func (e *DropDatabaseError) Error() string {
	return "provider: drop database failed: " + e.Name + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *DropDatabaseError) Unwrap() error {
	return e.Err
}

// MutableOperationNotSupportedError is returned when a mutable operation
// (CREATE/DROP DATABASE) is attempted on a legacy single-database provider
// that has no database manager configured.
type MutableOperationNotSupportedError struct {
	Operation string
}

// Error returns a human-readable description.
func (e *MutableOperationNotSupportedError) Error() string {
	return "provider: " + e.Operation + " not supported without database manager"
}

// ReservedDatabaseNameError is returned when CREATE DATABASE or DROP
// DATABASE names a schema the SQL layer reserves for itself:
//
//   - MySQLSchemaName, which every GRANT, REVOKE, and SHOW GRANTS statement
//     resolves; allowing a user database to take the name would make those
//     statements resolve the user's database instead, and make the user's
//     database unreachable.
//   - pgcatalog.SchemaName, which every PostgreSQL client's schema
//     introspection resolves, for the same reason. It is additionally the
//     only write against the PostgreSQL system catalogue that is reachable
//     at all: none of its relations implements a write-capability
//     interface, so DML against one is refused during analysis.
type ReservedDatabaseNameError struct {
	Name string
}

// Error returns a human-readable description.
func (e *ReservedDatabaseNameError) Error() string {
	return "provider: database name is reserved: " + e.Name
}

// IndexLookupError is returned when an index lookup operation fails
// during SQL query execution.
type IndexLookupError struct {
	IndexName string
	Err       error
}

// Error returns a human-readable description including the index name
// and the underlying cause.
func (e *IndexLookupError) Error() string {
	return "provider: index lookup failed: " + e.IndexName + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for errors.Is and errors.As
// unwrapping.
func (e *IndexLookupError) Unwrap() error {
	return e.Err
}

// RangeTranslationError is returned when a SQL range filter produced by
// the query planner cannot be translated to a QRDB index operation.
type RangeTranslationError struct {
	Reason string
}

// Error returns a human-readable description of the translation failure.
func (e *RangeTranslationError) Error() string {
	return "provider: range translation failed: " + e.Reason
}

// UnsupportedRangeTypeError is returned when a MySQLRangeColumnExpr has
// a RangeType that cannot be mapped to a QRDB index operation.
type UnsupportedRangeTypeError struct {
	RangeType string
}

// Error returns a human-readable description including the unsupported
// range type.
func (e *UnsupportedRangeTypeError) Error() string {
	return "provider: unsupported range type: " + e.RangeType
}

// RelationalMVCCUnavailableError is returned when a RelationalTable's
// RowCount or DataLength is called without an MVCCKVServicer configured.
type RelationalMVCCUnavailableError struct {
	Table string
}

// Error returns a human-readable description.
func (e *RelationalMVCCUnavailableError) Error() string {
	return "provider: mvcc service unavailable for relational table: " + e.Table
}

// ---------------------------------------------------------------------------
// Relational DDL errors
// ---------------------------------------------------------------------------

// RelationalCatalogUnavailableError is returned by a Database DDL method
// (CREATE/DROP/RENAME TABLE, ALTER DATABASE COLLATE) when no
// service.Registry was configured (see WithRegistry), so there is no
// TableServicer to carry out the operation.
type RelationalCatalogUnavailableError struct {
	Operation string
}

// Error returns a human-readable description.
func (e *RelationalCatalogUnavailableError) Error() string {
	return "provider: relational catalog unavailable for " + e.Operation + " (no registry configured)"
}

// TableServiceUnavailableError is returned by a RelationalTable DDL method
// when the table was constructed without WithTableService.
type TableServiceUnavailableError struct {
	Table string
}

// Error returns a human-readable description.
func (e *TableServiceUnavailableError) Error() string {
	return "provider: table service unavailable for relational table: " + e.Table
}

// GeneratedColumnUnsupportedError is returned when a CREATE TABLE, ADD
// COLUMN, or MODIFY COLUMN statement declares a generated (virtual or
// stored-expression) column, which TableDef has no way to represent.
type GeneratedColumnUnsupportedError struct {
	Column string
}

// Error returns a human-readable description.
func (e *GeneratedColumnUnsupportedError) Error() string {
	return "provider: generated columns are not supported: " + e.Column
}

// ColumnOrderUnsupportedError is returned when ADD COLUMN specifies a
// FIRST/AFTER position, which cannot be honored without breaking the
// append-only invariant that keeps ADD COLUMN metadata-only (see
// service.TableDef.Columns's doc comment).
type ColumnOrderUnsupportedError struct {
	Table  string
	Column string
}

// Error returns a human-readable description.
func (e *ColumnOrderUnsupportedError) Error() string {
	return "provider: table " + e.Table + ": ADD COLUMN " + e.Column +
		" cannot honor a requested column position (columns are always appended)"
}

// RewriteRequiredError is returned by RelationalTable.DropColumn and
// ModifyColumn -- methods that should never be called directly, since
// ShouldRewriteTable always requests a rewrite for the same change (see
// their doc comments).
type RewriteRequiredError struct {
	Table     string
	Column    string
	Operation string
}

// Error returns a human-readable description.
func (e *RewriteRequiredError) Error() string {
	return "provider: table " + e.Table + ": " + e.Operation + " " + e.Column + " requires a rewrite; must be driven through RewriteInserter"
}

// RewriteRequiresColumnError is returned when RewriteInserter is called
// with a nil oldColumn -- which ShouldRewriteTable never requests a rewrite
// for -- indicating go-mysql-server called it in a shape this table does
// not expect.
type RewriteRequiresColumnError struct {
	Table string
}

// Error returns a human-readable description.
func (e *RewriteRequiresColumnError) Error() string {
	return "provider: table " + e.Table + ": rewrite requested with no column to drop or modify"
}

// RewriteJournalMissingTargetError is returned when a DDL rewrite journal
// is present but carries no target definition, which should never happen
// for a journal TableService itself wrote.
type RewriteJournalMissingTargetError struct {
	Database string
	Name     string
}

// Error returns a human-readable description.
func (e *RewriteJournalMissingTargetError) Error() string {
	return "provider: table " + e.Database + "/" + e.Name + ": rewrite journal has no target definition"
}

// RewriteColumnNotFoundError is returned when a rewrite journal's Changes
// reference a column that is not present in the table's pre-rewrite
// definition.
type RewriteColumnNotFoundError struct {
	Column string
}

// Error returns a human-readable description.
func (e *RewriteColumnNotFoundError) Error() string {
	return "provider: rewrite references unknown column: " + e.Column
}

// RewriteColumnMismatchError is returned when replaying a rewrite journal's
// Changes against the pre-rewrite column list does not reproduce the
// journal's own target column list, indicating the executor and
// TableService's AlterTable have drifted out of sync about what a change
// produces.
type RewriteColumnMismatchError struct {
	Database string
	Name     string
}

// Error returns a human-readable description.
func (e *RewriteColumnMismatchError) Error() string {
	return "provider: table " + e.Database + "/" + e.Name + ": rewrite executor's column projection does not match the journaled target"
}

// RowRewriteError is returned when re-encoding a single row during a DDL
// rewrite fails, identifying the offending row's storage key.
type RowRewriteError struct {
	Key string
	Err error
}

// Error returns a human-readable description.
func (e *RowRewriteError) Error() string {
	return "provider: rewrite failed for row " + e.Key + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *RowRewriteError) Unwrap() error {
	return e.Err
}

// RewriteChunkConflictError is returned when a rewrite write-back chunk's
// compare-and-swap fails for a reason other than a stale generation (see
// runRewrite in relational_rewrite.go) -- unexpected, since the generation
// Compare is the only precondition a chunk's proposal carries, but reported
// explicitly rather than silently treated as success.
type RewriteChunkConflictError struct {
	Database string
	Name     string
}

// Error returns a human-readable description.
func (e *RewriteChunkConflictError) Error() string {
	return "provider: table " + e.Database + "." + e.Name + ": rewrite write-back chunk lost its compare-and-swap for an unexplained reason"
}

// PrimaryKeyRewriteUnsupportedError is returned when a DROP COLUMN or
// MODIFY COLUMN would change the physical encoding of the table's primary
// key -- a different column set or order, a different prefix length, or a
// column moving between the numeric and string key-encoding families. See
// the doc comment on validateRewritablePrimaryKey for why this is rejected
// outright rather than attempted.
type PrimaryKeyRewriteUnsupportedError struct {
	Table string
}

// Error returns a human-readable description.
func (e *PrimaryKeyRewriteUnsupportedError) Error() string {
	return "provider: table " + e.Table + ": this change would alter the primary key's physical encoding, which is not supported"
}

// IndexNoColumnsError is returned when an index definition names no
// columns -- CREATE INDEX and BuildIndex both require at least one.
type IndexNoColumnsError struct {
	Index string
}

// Error returns a human-readable description.
func (e *IndexNoColumnsError) Error() string {
	return "provider: index " + e.Index + " must name at least one column"
}

// ColumnNotFoundOnTableError is returned when an index definition (or
// other DDL operation) references a column that does not exist on the
// table's current schema.
type ColumnNotFoundOnTableError struct {
	Table  string
	Column string
}

// Error returns a human-readable description.
func (e *ColumnNotFoundOnTableError) Error() string {
	return "provider: table " + e.Table + " has no column named " + e.Column
}

// ---------------------------------------------------------------------------
// Relational FOREIGN KEY errors
//
// Every error here is a refusal to STORE a constraint, never a report that
// one was violated. A violation is go-mysql-server's to raise (see
// relational_foreign_key.go's opening comment), and it raises it as
// sql.ErrForeignKeyChildViolation / sql.ErrForeignKeyParentViolation, which
// carry the MySQL 1452 / 1451 codes clients match on.
// ---------------------------------------------------------------------------

// ForeignKeySchemaUnsupportedError is returned when a foreign key names a
// schema. QRDB databases have no schema layer (Database does not implement
// sql.SchemaDatabase), so a schema-qualified constraint could only be
// stored with the schema dropped, silently retargeting it at whatever table
// carries the same bare name.
type ForeignKeySchemaUnsupportedError struct {
	Name string
}

// Error returns a human-readable description.
func (e *ForeignKeySchemaUnsupportedError) Error() string {
	return "provider: foreign key " + e.Name + ": schema-qualified foreign keys are not supported"
}

// ForeignKeyCrossDatabaseUnsupportedError is returned when a foreign key
// would reference a table in a different database. Catalog, row, and index
// keys are all scoped to a single database and the services serving them
// are resolved per (tenant, database), so there is no read path from one
// database's table to another's for a parent-row check to use.
type ForeignKeyCrossDatabaseUnsupportedError struct {
	Name           string
	Database       string
	ParentDatabase string
}

// Error returns a human-readable description.
func (e *ForeignKeyCrossDatabaseUnsupportedError) Error() string {
	return "provider: foreign key " + e.Name + ": cross-database references are not supported (" +
		e.Database + " -> " + e.ParentDatabase + ")"
}

// ForeignKeyTableMismatchError is returned when a foreign key names a
// declaring table other than the one it is being stored on, which would
// mean persisting it against a record it does not describe.
type ForeignKeyTableMismatchError struct {
	Name     string
	Declared string
	Table    string
}

// Error returns a human-readable description.
func (e *ForeignKeyTableMismatchError) Error() string {
	return "provider: foreign key " + e.Name + " declares table " + e.Declared +
		" but is being stored on " + e.Table
}

// ForeignKeyActionUnsupportedError is returned when a foreign key carries a
// referential action outside the set go-mysql-server can act on. Storing
// one would leave the behavior on a parent-row change undefined.
type ForeignKeyActionUnsupportedError struct {
	Action string
}

// Error returns a human-readable description.
func (e *ForeignKeyActionUnsupportedError) Error() string {
	return "provider: unsupported foreign key referential action: " + e.Action
}

// ---------------------------------------------------------------------------
// Relational DML errors
// ---------------------------------------------------------------------------

// TransactionServiceUnavailableError is returned by a RelationalTable DML
// method (Insert/Update/Delete/Replace) when the table was constructed
// without WithTransactionService.
type TransactionServiceUnavailableError struct {
	Table string
}

// Error returns a human-readable description.
func (e *TransactionServiceUnavailableError) Error() string {
	return "provider: transaction service unavailable for relational table: " + e.Table
}

// CommandInProgressError is returned by QRDBSession.CommandBegin when the
// session's previous command has not ended: a statement was begun on a
// connection while another statement -- typically a query whose rows are
// still open -- is in flight. Close the open rows first.
type CommandInProgressError struct{}

// Error returns a human-readable description.
func (e *CommandInProgressError) Error() string {
	return "provider: another statement is still in progress on this connection; close its rows first"
}

// MixedTransactionalWriteError refuses a statement that writes both a
// relational table and a non-transactional one (the kv, entity or
// collection tables). A statement is atomic -- its own transaction outside
// an explicit one -- and a non-transactional write is applied at once and
// cannot be rolled back with it, so the statement is refused before it
// writes either. Write the two in separate statements.
type MixedTransactionalWriteError struct {
	Relational       string
	NonTransactional string
}

// Error returns a human-readable description.
func (e *MixedTransactionalWriteError) Error() string {
	return "provider: a statement cannot write both relational table " + e.Relational +
		" and non-transactional table " + e.NonTransactional + "; write them in separate statements"
}

// TransactionSessionRequiredError is returned by a RelationalTable DML
// method called on a context that carries no QRDB session. Every relational
// write is part of a transaction -- an explicit one, or the autocommit
// statement's implicit one -- and the transaction lives on the session; a
// context without one has nothing to buffer the write into or commit it
// with. Every SQL surface creates a session per connection.
type TransactionSessionRequiredError struct {
	Table string
}

// Error returns a human-readable description.
func (e *TransactionSessionRequiredError) Error() string {
	return "provider: a write to relational table " + e.Table + " requires a QRDB session"
}

// SequenceServiceUnavailableError is returned by a RelationalTable
// AUTO_INCREMENT method when the table declares an AUTO_INCREMENT column
// but was constructed without WithSequenceService.
type SequenceServiceUnavailableError struct {
	Table string
}

// Error returns a human-readable description.
func (e *SequenceServiceUnavailableError) Error() string {
	return "provider: sequence service unavailable for relational table: " + e.Table
}

// AutoIncrementUnavailableError is returned by a RelationalTable
// AUTO_INCREMENT method when the table declares no AUTO_INCREMENT column.
type AutoIncrementUnavailableError struct {
	Table string
}

// Error returns go-mysql-server's own sql.ErrNoAutoIncrementCol message,
// verbatim and derived from the sentinel itself so the two can never drift.
//
// A typed error that discards its own Table field from its message needs
// justifying, and the justification is that go-mysql-server tolerates this
// one condition through two different mechanisms, and only one of them looks
// at the error's identity. SHOW CREATE TABLE compares the *rendered string*:
//
//	if err != nil && err.Error() != sql.ErrNoAutoIncrementCol.Error() {
//	        return "", err
//	}
//
// (sql/rowexec/show_iters.go), while information_schema.TABLES uses
// errors.Is (sql/information_schema/tables_table.go). Satisfying only
// errors.Is -- which the Unwrap below does -- left SHOW CREATE TABLE failing
// outright with a qrdb-specific message for every table without an
// AUTO_INCREMENT column, which is most of them. The table name is not lost,
// only unprinted: it stays on the struct, reachable with errors.As.
func (e *AutoIncrementUnavailableError) Error() string {
	return sqle.ErrNoAutoIncrementCol.Error()
}

// Unwrap returns go-mysql-server's sql.ErrNoAutoIncrementCol so that
// errors.Is recognizes this condition as the sentinel the engine's own
// AutoIncrementTable contract defines for it.
//
// This is not cosmetic. go-mysql-server calls PeekNextAutoIncrementValue on
// every AutoIncrementTable while building information_schema.TABLES and
// tolerates exactly one error from it -- ErrNoAutoIncrementCol -- treating
// anything else as fatal for the whole iteration (see
// sql/information_schema/tables_table.go). Returning a qrdb-only error for a
// table with no AUTO_INCREMENT column therefore emptied
// information_schema.TABLES for EVERY database, which is what a MySQL client
// reads to answer "does this table exist": GORM's Migrator.HasTable, for one,
// then reported false for tables that plainly existed, and AutoMigrate
// re-issued CREATE TABLE and failed with "table already exists".
func (e *AutoIncrementUnavailableError) Unwrap() error {
	return sqle.ErrNoAutoIncrementCol
}

// RowKeyTooLargeError is returned when an encoded primary key or
// secondary-index entry key exceeds the size this table's write path
// enforces at statement time (maxRowKeySize), so the failing statement is
// named at the point of the write rather than surfacing later as an
// eventual go-quicraft proposal-size rejection.
type RowKeyTooLargeError struct {
	Table string
	Size  int
	Max   int
}

// Error returns a human-readable description.
func (e *RowKeyTooLargeError) Error() string {
	return "provider: table " + e.Table + ": encoded key size " + strconv.Itoa(e.Size) + " exceeds maximum " + strconv.Itoa(e.Max)
}

// RowValueTooLargeError is returned when an encoded row exceeds the size
// this table's write path enforces at statement time (maxRowValueSize).
type RowValueTooLargeError struct {
	Table string
	Size  int
	Max   int
}

// Error returns a human-readable description.
func (e *RowValueTooLargeError) Error() string {
	return "provider: table " + e.Table + ": encoded row size " + strconv.Itoa(e.Size) + " exceeds maximum " + strconv.Itoa(e.Max)
}

// RowTransactionTooLargeError is returned when a single INSERT/UPDATE/
// DELETE's estimated proposal size (row plus every secondary-index entry
// it touches) exceeds this table's write path's per-statement ceiling
// (maxRowProposalBytes), which stays comfortably under go-quicraft's
// per-entry ceiling (proto.MaxEntrySize).
type RowTransactionTooLargeError struct {
	Table string
	Size  int
	Max   int
}

// Error returns a human-readable description.
func (e *RowTransactionTooLargeError) Error() string {
	return "provider: table " + e.Table + ": transaction size " + strconv.Itoa(e.Size) + " exceeds maximum " + strconv.Itoa(e.Max)
}

// TransactionConflictRetriesExhaustedError is returned when an Insert or
// Update exhausts writeMaxAttempts compare-and-swap retries without either
// succeeding or resolving to a definite duplicate-key cause (see
// resolveDuplicate) -- an unusual condition indicating sustained, repeated
// contention on the same row or index entry.
type TransactionConflictRetriesExhaustedError struct {
	Table string
}

// Error returns a human-readable description.
func (e *TransactionConflictRetriesExhaustedError) Error() string {
	return "provider: table " + e.Table + ": exhausted retries resolving a transaction conflict"
}

// ---------------------------------------------------------------------------
// Interactive transaction errors (Phase 8: BEGIN/COMMIT/ROLLBACK)
// ---------------------------------------------------------------------------

// NonTransactionalTableError is returned when a write reaches a KV, entity,
// or collection table (which always apply immediately, autocommit-only --
// see kv_table.go/entity_table.go/collection_table.go) while an interactive
// transaction is open (BEGIN was issued and not yet COMMIT/ROLLBACK). These
// legacy table kinds have no buffered write-set to participate in, so
// silently applying the write immediately would let it escape the open
// transaction's atomicity and visibility boundary -- and, worse, survive a
// later ROLLBACK. Migrating them onto the write-set is future work; until
// then this table kind must be used autocommit-only.
type NonTransactionalTableError struct {
	Table string
}

// Error returns a human-readable description.
func (e *NonTransactionalTableError) Error() string {
	return "provider: table " + e.Table + " does not support interactive transactions (autocommit only); issue this statement outside BEGIN/COMMIT"
}

// TransactionConflictError is returned by QRDBSession.CommitTransaction when
// an interactive transaction's buffered write-set loses its optimistic
// compare-and-swap: at least one key the transaction wrote was modified by
// another, concurrently-committed transaction between this transaction's
// pinned snapshot revision (see WriteSet) and its own commit attempt. Key
// names the first buffered key in the write-set as a representative
// diagnostic; because the whole write-set commits as a single
// compare-and-swap, the underlying TransactionServicer response does not
// identify which specific compare failed.
//
// It wraps *service.TransactionConflictError (see Unwrap) so this package's
// own tests and any direct Go caller of WriteSet/QRDBSession -- notably the
// embedded SDK driver, which never crosses go-mysql-server's wire-protocol
// boundary -- can still reach it via errors.As.
//
// go-mysql-server's SQL engine and the MySQL/PostgreSQL wire listeners never
// see this type directly: CommitTransaction wraps it a second time in
// sql.ErrLockDeadlock (see that method), which is what go-mysql-server's own
// sql.CastSQLError recognizes and maps to MySQL error 1213 / SQLSTATE 40001
// (ER_LOCK_DEADLOCK) -- the code database/sql clients and GORM's retry
// middleware recognize as "retry the transaction". sql.ErrLockDeadlock is
// implemented by a third-party error-kind library (gopkg.in/src-d/go-errors.v1)
// that predates and does not interoperate with the standard library's
// errors.Unwrap chain, so that outer wrapping is deliberately the last step
// applied, after every internal errors.As-based check this package needs has
// already run against the unwrapped value.
type TransactionConflictError struct {
	Key string
	Err *service.TransactionConflictError
}

// Error returns a human-readable description.
func (e *TransactionConflictError) Error() string {
	msg := "provider: transaction conflict"
	if e.Key != "" {
		msg += " on key " + e.Key
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error for errors.Is and errors.As
// unwrapping.
func (e *TransactionConflictError) Unwrap() error {
	return e.Err
}

// TransactionSnapshotExpiredError is returned when an interactive
// transaction's pinned snapshot revision has been compacted before the
// transaction could commit -- e.g. a long-running transaction left open
// while background compaction advanced past the revision it pinned. The
// transaction can never be completed consistently at that point and must be
// rolled back and retried; this is surfaced as a distinct typed error
// rather than silently downgrading to a fresh (non-snapshot-consistent)
// read.
type TransactionSnapshotExpiredError struct {
	Requested int64
	Compacted int64
}

// Error returns a human-readable description.
func (e *TransactionSnapshotExpiredError) Error() string {
	return "provider: transaction snapshot revision " + strconv.Itoa(int(e.Requested)) +
		" has been compacted (earliest available: " + strconv.Itoa(int(e.Compacted)) + "); rollback and retry"
}

// WriteSetLimitError is returned when a single statement's write, buffered
// into an interactive transaction's WriteSet, would push the transaction's
// cumulative buffered size past a configured ceiling (byte size or
// operation count). It is raised at the buffering statement rather than
// deferred to the eventual COMMIT, so the failing statement is named.
type WriteSetLimitError struct {
	Reason string
	Size   int
	Max    int
}

// Error returns a human-readable description.
func (e *WriteSetLimitError) Error() string {
	return "provider: transaction write-set " + e.Reason + " " + strconv.Itoa(e.Size) + " exceeds maximum " + strconv.Itoa(e.Max)
}

// SavepointNotFoundError is returned by RollbackToSavepoint or
// ReleaseSavepoint when the named savepoint was never marked (via SAVEPOINT)
// on the transaction's write-set, or has already been rolled back past.
type SavepointNotFoundError struct {
	Name string
}

// Error returns a human-readable description.
func (e *SavepointNotFoundError) Error() string {
	return "provider: savepoint not found: " + e.Name
}

// CrossDatabaseTransactionError is returned when an interactive
// transaction's write-set, already bound to one (tenant, database) pair's
// TransactionServicer/MVCCKVServicer by an earlier statement, is asked to
// buffer a write against a relational table belonging to a different
// database. A single compare-and-swap commit can only ever target one Raft
// shard, so a transaction spanning multiple databases cannot be committed
// atomically and is rejected outright rather than silently applied as two
// separate, non-atomic commits.
type CrossDatabaseTransactionError struct {
	Bound     string
	Requested string
}

// Error returns a human-readable description.
func (e *CrossDatabaseTransactionError) Error() string {
	return "provider: transaction already bound to database " + e.Bound + "; cannot also write to " + e.Requested
}

// ReadOnlyTransactionError is returned when a write is attempted inside an
// interactive transaction started with START TRANSACTION READ ONLY.
type ReadOnlyTransactionError struct{}

// Error returns a human-readable description.
func (e *ReadOnlyTransactionError) Error() string {
	return "provider: cannot write in a READ ONLY transaction"
}

// IsolationRangeReadError is returned when a statement inside a transaction
// running at qrdb_isolation='written+reads' performs a RANGE read -- a table
// scan, or an index scan that can match more than one entry -- rather than a
// point read by exact key.
//
// That level's contract is that every key the transaction read is compared
// at commit time, and a scan's read set cannot be: it grows without bound
// across a large SELECT and would blow the commit proposal's operation-count
// ceiling almost immediately (see WriteSet's doc comment). The two honest
// responses are to refuse the read or to refuse the level; silently serving
// the scan while leaving its read set uncompared is not one of them, because
// it would report a guarantee that is not being enforced. This error is the
// first: the transaction is told at the statement that asked, before any
// work is wasted, precisely which capability the level does not have.
//
// It is deliberately NOT reported as MySQL 1213 / SQLSTATE 40001: it is not
// a conflict, and retrying the transaction unchanged would only produce it
// again. The transaction should either address the row by an exact key
// (a primary-key or whole-tuple unique-index equality predicate) or run at
// qrdb_isolation='written'.
type IsolationRangeReadError struct {
	Level  string
	Prefix string
}

// Error returns a human-readable description.
func (e *IsolationRangeReadError) Error() string {
	return "provider: qrdb_isolation=" + e.Level + " cannot compare the read set of a range scan over " +
		strconv.Quote(e.Prefix) + "; read by exact key, or use qrdb_isolation='written' for this transaction"
}

// SequencesUnavailableError is returned when a database has no relational
// catalog to serve client-named sequences from (KV-only shapes).
type SequencesUnavailableError struct {
	Database string
}

// Error returns a human-readable description.
func (e *SequencesUnavailableError) Error() string {
	return "provider: database " + e.Database + " does not support sequences"
}

// DocumentMoveOutcomeUnknownError is returned when an UPDATE that changes a
// collection document's `_id` stored the document under its new ID and then
// could not tell whether releasing the old ID removed the old document: the
// release failed with an error that is not a lost race. The old document may
// or may not still exist, so the copy under the new ID is deliberately LEFT --
// withdrawing it could leave the document nowhere -- and the caller must read
// both IDs to learn which the collection now holds. It matches
// service.ErrOutcomeUnknown.
type DocumentMoveOutcomeUnknownError struct {
	Collection string
	OldID      uint64
	NewID      uint64
	Err        error
}

// Error returns a human-readable description.
func (e *DocumentMoveOutcomeUnknownError) Error() string {
	return "provider: moving document " + strconv.FormatUint(e.OldID, 10) + " to " + strconv.FormatUint(e.NewID, 10) +
		" in collection " + e.Collection + ": releasing the old id failed with an unknown outcome, and both ids are left: " + e.Err.Error()
}

// Unwrap returns the release's error.
func (e *DocumentMoveOutcomeUnknownError) Unwrap() error {
	return e.Err
}

// Is reports the service.ErrOutcomeUnknown marker.
func (e *DocumentMoveOutcomeUnknownError) Is(target error) bool {
	return target == service.ErrOutcomeUnknown
}
