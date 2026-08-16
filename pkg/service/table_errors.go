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

import "strconv"

// ---------------------------------------------------------------------------
// Table reference / identifier errors
// ---------------------------------------------------------------------------

// TableEmptyDatabaseError is returned when a database name is empty in a
// table catalog operation.
type TableEmptyDatabaseError struct{}

// Error returns a human-readable description.
func (e *TableEmptyDatabaseError) Error() string {
	return "service: database name must not be empty for table operation"
}

// TableInvalidNameError is returned when a table, column, or index
// identifier is empty or does not satisfy the identifier grammar (letters,
// digits, underscores; must not start with a digit; must not start with the
// "__" prefix reserved for catalog system keys).
type TableInvalidNameError struct {
	// Kind identifies what the invalid name names: "table", "column", or
	// "index".
	Kind   string
	Name   string
	Reason string
}

// Error returns a human-readable description.
func (e *TableInvalidNameError) Error() string {
	return "service: invalid " + e.Kind + " name " + strconv.Quote(e.Name) + ": " + e.Reason
}

// ---------------------------------------------------------------------------
// Existence errors
// ---------------------------------------------------------------------------

// TableAlreadyExistsError is returned when attempting to create a table
// that already exists in the given database.
type TableAlreadyExistsError struct {
	Database string
	Name     string
}

// Error returns a human-readable description.
func (e *TableAlreadyExistsError) Error() string {
	return "service: table already exists: " + e.Database + "/" + e.Name
}

// TableNotFoundError is returned when an operation references a table that
// does not exist.
type TableNotFoundError struct {
	Database string
	Name     string
}

// Error returns a human-readable description.
func (e *TableNotFoundError) Error() string {
	return "service: table not found: " + e.Database + "/" + e.Name
}

// ---------------------------------------------------------------------------
// Schema validation errors
// ---------------------------------------------------------------------------

// TableNoColumnsError is returned when a CreateTable request declares no
// columns.
type TableNoColumnsError struct{}

// Error returns a human-readable description.
func (e *TableNoColumnsError) Error() string {
	return "service: table must declare at least one column"
}

// TableDuplicateColumnError is returned when a table definition names the
// same column twice (case-insensitively).
type TableDuplicateColumnError struct {
	Column string
}

// Error returns a human-readable description.
func (e *TableDuplicateColumnError) Error() string {
	return "service: duplicate column: " + e.Column
}

// TableColumnNotFoundError is returned when an ALTER TABLE change (drop,
// modify, rename) references a column that does not exist on the table.
type TableColumnNotFoundError struct {
	Column string
}

// Error returns a human-readable description.
func (e *TableColumnNotFoundError) Error() string {
	return "service: column not found: " + e.Column
}

// TableMultipleAutoIncrementError is returned when a table definition would
// carry more than one AUTO_INCREMENT column. At most one is permitted,
// matching MySQL.
type TableMultipleAutoIncrementError struct{}

// Error returns a human-readable description.
func (e *TableMultipleAutoIncrementError) Error() string {
	return "service: at most one column may be AUTO_INCREMENT"
}

// TablePrimaryKeyColumnNotFoundError is returned when a PRIMARY KEY clause
// names a column that is not declared on the table.
type TablePrimaryKeyColumnNotFoundError struct {
	Column string
}

// Error returns a human-readable description.
func (e *TablePrimaryKeyColumnNotFoundError) Error() string {
	return "service: primary key references unknown column: " + e.Column
}

// TableUnsupportedColumnTypeError is returned when a column declares a type
// string that is not one of the supported SQL type keywords.
type TableUnsupportedColumnTypeError struct {
	Column string
	Type   string
}

// Error returns a human-readable description.
func (e *TableUnsupportedColumnTypeError) Error() string {
	return "service: column " + e.Column + " has unsupported type: " + e.Type
}

// ---------------------------------------------------------------------------
// Index validation errors
// ---------------------------------------------------------------------------

// TableIndexNoColumnsError is returned when an index definition names no
// columns.
type TableIndexNoColumnsError struct {
	Index string
}

// Error returns a human-readable description.
func (e *TableIndexNoColumnsError) Error() string {
	return "service: index " + e.Index + " must name at least one column"
}

// TableIndexColumnNotFoundError is returned when an index definition names a
// column that is not declared on the table.
type TableIndexColumnNotFoundError struct {
	Index  string
	Column string
}

// Error returns a human-readable description.
func (e *TableIndexColumnNotFoundError) Error() string {
	return "service: index " + e.Index + " references unknown column: " + e.Column
}

// TableIndexAlreadyExistsError is returned when attempting to create an
// index whose name is already in use on the table.
type TableIndexAlreadyExistsError struct {
	Database string
	Table    string
	Index    string
}

// Error returns a human-readable description.
func (e *TableIndexAlreadyExistsError) Error() string {
	return "service: index already exists: " + e.Database + "/" + e.Table + "/" + e.Index
}

// TableIndexNotFoundError is returned when an operation references an index
// that does not exist on the table.
type TableIndexNotFoundError struct {
	Database string
	Table    string
	Index    string
}

// Error returns a human-readable description.
func (e *TableIndexNotFoundError) Error() string {
	return "service: index not found: " + e.Database + "/" + e.Table + "/" + e.Index
}

// ---------------------------------------------------------------------------
// FOREIGN KEY errors
// ---------------------------------------------------------------------------

// TableForeignKeyAlreadyExistsError is returned when adding a foreign key
// whose name is already in use. Unlike an index name, a foreign key name is
// unique across the whole database in MySQL, so Table names the table that
// already holds the name -- which is not necessarily the table the
// constraint was being added to.
type TableForeignKeyAlreadyExistsError struct {
	Database string
	Table    string
	Name     string
}

// Error returns a human-readable description.
func (e *TableForeignKeyAlreadyExistsError) Error() string {
	return "service: foreign key already exists: " + e.Database + "/" + e.Table + "/" + e.Name
}

// TableForeignKeyNotFoundError is returned when an operation references a
// foreign key that is not declared on the table.
type TableForeignKeyNotFoundError struct {
	Database string
	Table    string
	Name     string
}

// Error returns a human-readable description.
func (e *TableForeignKeyNotFoundError) Error() string {
	return "service: foreign key not found: " + e.Database + "/" + e.Table + "/" + e.Name
}

// TableForeignKeyNoColumnsError is returned when a foreign key definition
// names no columns, or a different number of local and parent columns.
type TableForeignKeyNoColumnsError struct {
	Name string
}

// Error returns a human-readable description.
func (e *TableForeignKeyNoColumnsError) Error() string {
	return "service: foreign key " + e.Name +
		" must name at least one column and an equal number of parent columns"
}

// TableForeignKeyColumnNotFoundError is returned when a foreign key
// definition names a local column that is not declared on the table.
type TableForeignKeyColumnNotFoundError struct {
	Name   string
	Column string
}

// Error returns a human-readable description.
func (e *TableForeignKeyColumnNotFoundError) Error() string {
	return "service: foreign key " + e.Name + " references unknown column: " + e.Column
}

// ---------------------------------------------------------------------------
// ALTER TABLE errors
// ---------------------------------------------------------------------------

// TableNoChangesError is returned when an AlterTableRequest carries no
// changes.
type TableNoChangesError struct{}

// Error returns a human-readable description.
func (e *TableNoChangesError) Error() string {
	return "service: alter table request must contain at least one change"
}

// TableUnsupportedAlterActionError is returned when an AlterTableChange
// names an action outside the AlterAction enumeration.
type TableUnsupportedAlterActionError struct {
	Action string
}

// Error returns a human-readable description.
func (e *TableUnsupportedAlterActionError) Error() string {
	return "service: unsupported alter table action: " + e.Action
}

// TableStaleRevisionError is returned when an AlterTableRequest sets
// ExpectedRevision and the catalog record's current ModRevision no longer
// matches it. Unlike TableDDLConflictError, this is a deterministic
// client-precondition failure -- the caller observed the table at an older
// revision and must re-read before retrying -- so it is not retried
// internally; retrying against the same stale ExpectedRevision can never
// succeed, because MVCC revisions only move forward.
type TableStaleRevisionError struct {
	Database string
	Name     string
	Expected int64
	Actual   int64
}

// Error returns a human-readable description.
func (e *TableStaleRevisionError) Error() string {
	return "service: table " + e.Database + "/" + e.Name + " expected revision " +
		strconv.FormatInt(e.Expected, 10) + " but current revision is " +
		strconv.FormatInt(e.Actual, 10)
}

// TableRewriteRequiredError is returned by AlterTable when a change (drop
// or modify column) requires re-encoding existing rows at a new schema
// layout. The opening compare-and-swap that records the TableDDLJournal has
// already been committed; the row rewrite itself is executed by a later
// phase (the rewrite executor) not implemented by this service.
type TableRewriteRequiredError struct {
	Database string
	Name     string
}

// Error returns a human-readable description.
func (e *TableRewriteRequiredError) Error() string {
	return "service: table " + e.Database + "/" + e.Name +
		": requested change requires a row rewrite, which has been journaled but not yet executed"
}

// TableRewriteInProgressError is returned by GetTable (and any other
// catalog read) when a TableDDLJournal is present for the table. Its rows
// are in a mixed encoding until the rewrite completes or is resumed, so the
// table must not be served.
type TableRewriteInProgressError struct {
	Database string
	Name     string
}

// Error returns a human-readable description.
func (e *TableRewriteInProgressError) Error() string {
	return "service: table " + e.Database + "/" + e.Name + ": rewrite in progress"
}

// TableNoRewriteInProgressError is returned by RewriteJournal and
// CompleteRewrite when no TableDDLJournal is present for the table -- either
// because no rewrite was ever started, or because a racing caller (another
// node resuming the same interrupted rewrite) already completed it. Callers
// that are themselves trying to resume or complete a rewrite treat this as
// success rather than failure: there is nothing left to do.
type TableNoRewriteInProgressError struct {
	Database string
	Name     string
}

// Error returns a human-readable description.
func (e *TableNoRewriteInProgressError) Error() string {
	return "service: table " + e.Database + "/" + e.Name + ": no rewrite in progress"
}

// TableRewriteTargetMissingError is returned by CompleteRewrite when a
// TableDDLJournal is present but carries no Target definition, which should
// never happen for a journal this service itself wrote (AlterTable always
// sets Target before persisting the journal) and therefore indicates
// catalog corruption rather than an ordinary race.
type TableRewriteTargetMissingError struct {
	Database string
	Name     string
}

// Error returns a human-readable description.
func (e *TableRewriteTargetMissingError) Error() string {
	return "service: table " + e.Database + "/" + e.Name + ": rewrite journal has no target definition"
}

// ---------------------------------------------------------------------------
// Concurrency errors
// ---------------------------------------------------------------------------

// TableDDLConflictError is returned when a catalog compare-and-swap loses
// its race against a concurrent DDL statement on the same table after
// exhausting its bounded retries. It wraps the *TransactionConflictError
// from the losing attempt so errors.As(err, &TransactionConflictError{})
// continues to work for callers that only care about the generic CAS
// conflict, while callers that want table/database context can inspect this
// type directly.
type TableDDLConflictError struct {
	Database string
	Name     string
	Err      *TransactionConflictError
}

// Error returns a human-readable description.
func (e *TableDDLConflictError) Error() string {
	return "service: table " + e.Database + "/" + e.Name +
		": DDL conflict, too many concurrent modifications: " + e.Err.Error()
}

// Unwrap returns the underlying transaction conflict error for use with
// errors.Is and errors.As.
func (e *TableDDLConflictError) Unwrap() error {
	return e.Err
}

// ---------------------------------------------------------------------------
// Metadata errors
// ---------------------------------------------------------------------------

// TableMetadataError is returned when a table catalog metadata operation
// (marshal, unmarshal, list) fails.
type TableMetadataError struct {
	Op  string
	Err error
}

// Error returns a human-readable description.
func (e *TableMetadataError) Error() string {
	return "service: table metadata " + e.Op + " failed: " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *TableMetadataError) Unwrap() error {
	return e.Err
}

// ---------------------------------------------------------------------------
// Sequence errors
// ---------------------------------------------------------------------------

// SequenceEmptyDatabaseError is returned when a database name is empty in a
// sequence operation.
type SequenceEmptyDatabaseError struct{}

// Error returns a human-readable description.
func (e *SequenceEmptyDatabaseError) Error() string {
	return "service: database name must not be empty for sequence operation"
}

// SequenceEmptyNameError is returned when a sequence name is empty.
type SequenceEmptyNameError struct{}

// Error returns a human-readable description.
func (e *SequenceEmptyNameError) Error() string {
	return "service: sequence name must not be empty"
}

// SequenceInvalidCountError is returned when Allocate is called with n == 0.
// There is no meaningful block to hand out for a zero-sized request.
type SequenceInvalidCountError struct{}

// Error returns a human-readable description.
func (e *SequenceInvalidCountError) Error() string {
	return "service: sequence allocation count must be greater than zero"
}

// SequenceConflictError is returned when a sequence's compare-and-swap
// update loses its race against concurrent allocators after exhausting its
// bounded retries.
type SequenceConflictError struct {
	Database string
	Name     string
}

// Error returns a human-readable description.
func (e *SequenceConflictError) Error() string {
	return "service: sequence " + e.Database + "/" + e.Name +
		": too many concurrent modifications"
}

// ViewAlreadyExistsError indicates a CREATE VIEW named a view that already
// exists in the database.
type ViewAlreadyExistsError struct {
	Database string
	Name     string
}

// Error returns a human-readable description.
func (e *ViewAlreadyExistsError) Error() string {
	return "service: view already exists: " + e.Database + "/" + e.Name
}

// ViewNotFoundError indicates the named view does not exist.
type ViewNotFoundError struct {
	Database string
	Name     string
}

// Error returns a human-readable description.
func (e *ViewNotFoundError) Error() string {
	return "service: view not found: " + e.Database + "/" + e.Name
}

// ViewDefinitionEmptyError indicates a CREATE VIEW carried no defining
// SELECT. A view with no definition can never be served, so it is refused at
// creation rather than discovered at first use.
type ViewDefinitionEmptyError struct {
	Database string
	Name     string
}

// Error returns a human-readable description.
func (e *ViewDefinitionEmptyError) Error() string {
	return "service: view definition is empty: " + e.Database + "/" + e.Name
}

// TableCheckAlreadyExistsError indicates an ADD CHECK named a constraint the
// table already carries.
type TableCheckAlreadyExistsError struct {
	Table string
	Check string
}

// Error returns a human-readable description.
func (e *TableCheckAlreadyExistsError) Error() string {
	return "service: check constraint already exists: " + e.Table + "/" + e.Check
}

// TableCheckNotFoundError indicates a DROP CHECK named a constraint the table
// does not carry.
type TableCheckNotFoundError struct {
	Table string
	Check string
}

// Error returns a human-readable description.
func (e *TableCheckNotFoundError) Error() string {
	return "service: check constraint not found: " + e.Table + "/" + e.Check
}
