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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTableEmptyDatabaseError(t *testing.T) {
	err := &TableEmptyDatabaseError{}
	assert.Equal(t, "service: database name must not be empty for table operation", err.Error())
}

func TestTableInvalidNameError(t *testing.T) {
	err := &TableInvalidNameError{Kind: "table", Name: "1bad", Reason: "must not start with a digit"}
	assert.Equal(t, `service: invalid table name "1bad": must not start with a digit`, err.Error())
}

func TestTableInvalidNameError_EmptyFields(t *testing.T) {
	err := &TableInvalidNameError{}
	assert.Equal(t, `service: invalid  name "": `, err.Error())
}

func TestTableAlreadyExistsError(t *testing.T) {
	err := &TableAlreadyExistsError{Database: "db", Name: "users"}
	assert.Equal(t, "service: table already exists: db/users", err.Error())
}

func TestTableNotFoundError(t *testing.T) {
	err := &TableNotFoundError{Database: "db", Name: "users"}
	assert.Equal(t, "service: table not found: db/users", err.Error())
}

func TestTableNoColumnsError(t *testing.T) {
	err := &TableNoColumnsError{}
	assert.Equal(t, "service: table must declare at least one column", err.Error())
}

func TestTableDuplicateColumnError(t *testing.T) {
	err := &TableDuplicateColumnError{Column: "id"}
	assert.Equal(t, "service: duplicate column: id", err.Error())
}

func TestTableColumnNotFoundError(t *testing.T) {
	err := &TableColumnNotFoundError{Column: "missing"}
	assert.Equal(t, "service: column not found: missing", err.Error())
}

func TestTableMultipleAutoIncrementError(t *testing.T) {
	err := &TableMultipleAutoIncrementError{}
	assert.Equal(t, "service: at most one column may be AUTO_INCREMENT", err.Error())
}

func TestTablePrimaryKeyColumnNotFoundError(t *testing.T) {
	err := &TablePrimaryKeyColumnNotFoundError{Column: "id"}
	assert.Equal(t, "service: primary key references unknown column: id", err.Error())
}

func TestTableUnsupportedColumnTypeError(t *testing.T) {
	err := &TableUnsupportedColumnTypeError{Column: "x", Type: "FROBNICATE"}
	assert.Equal(t, "service: column x has unsupported type: FROBNICATE", err.Error())
}

func TestTableIndexNoColumnsError(t *testing.T) {
	err := &TableIndexNoColumnsError{Index: "idx_x"}
	assert.Equal(t, "service: index idx_x must name at least one column", err.Error())
}

func TestTableIndexColumnNotFoundError(t *testing.T) {
	err := &TableIndexColumnNotFoundError{Index: "idx_x", Column: "missing"}
	assert.Equal(t, "service: index idx_x references unknown column: missing", err.Error())
}

func TestTableIndexAlreadyExistsError(t *testing.T) {
	err := &TableIndexAlreadyExistsError{Database: "db", Table: "t", Index: "idx_x"}
	assert.Equal(t, "service: index already exists: db/t/idx_x", err.Error())
}

func TestTableIndexNotFoundError(t *testing.T) {
	err := &TableIndexNotFoundError{Database: "db", Table: "t", Index: "idx_x"}
	assert.Equal(t, "service: index not found: db/t/idx_x", err.Error())
}

func TestTableNoChangesError(t *testing.T) {
	err := &TableNoChangesError{}
	assert.Equal(t, "service: alter table request must contain at least one change", err.Error())
}

func TestTableUnsupportedAlterActionError(t *testing.T) {
	err := &TableUnsupportedAlterActionError{Action: "frobnicate_column"}
	assert.Equal(t, "service: unsupported alter table action: frobnicate_column", err.Error())
}

func TestTableStaleRevisionError(t *testing.T) {
	err := &TableStaleRevisionError{Database: "db", Name: "t", Expected: 3, Actual: 7}
	assert.Equal(t, "service: table db/t expected revision 3 but current revision is 7", err.Error())
}

func TestTableRewriteRequiredError(t *testing.T) {
	err := &TableRewriteRequiredError{Database: "db", Name: "t"}
	assert.Equal(t, "service: table db/t: requested change requires a row rewrite, which has been journaled but not yet executed", err.Error())
}

func TestTableRewriteInProgressError(t *testing.T) {
	err := &TableRewriteInProgressError{Database: "db", Name: "t"}
	assert.Equal(t, "service: table db/t: rewrite in progress", err.Error())
}

func TestTableNoRewriteInProgressError(t *testing.T) {
	err := &TableNoRewriteInProgressError{Database: "db", Name: "t"}
	assert.Equal(t, "service: table db/t: no rewrite in progress", err.Error())
}

func TestTableRewriteTargetMissingError(t *testing.T) {
	err := &TableRewriteTargetMissingError{Database: "db", Name: "t"}
	assert.Equal(t, "service: table db/t: rewrite journal has no target definition", err.Error())
}

func TestTableDDLConflictError(t *testing.T) {
	inner := &TransactionConflictError{Key: "__sys:tables/db/t"}
	err := &TableDDLConflictError{Database: "db", Name: "t", Err: inner}
	assert.Contains(t, err.Error(), "service: table db/t: DDL conflict")
	assert.Contains(t, err.Error(), inner.Error())
}

func TestTableDDLConflictError_Unwrap(t *testing.T) {
	inner := &TransactionConflictError{Key: "k"}
	err := &TableDDLConflictError{Database: "db", Name: "t", Err: inner}
	assert.Equal(t, inner, errors.Unwrap(err))

	var conflict *TransactionConflictError
	assert.True(t, errors.As(error(err), &conflict))
	assert.Same(t, inner, conflict)
}

func TestTableMetadataError(t *testing.T) {
	cause := errors.New("disk full")
	err := &TableMetadataError{Op: "marshal", Err: cause}
	assert.Equal(t, "service: table metadata marshal failed: disk full", err.Error())
}

func TestTableMetadataError_Unwrap(t *testing.T) {
	cause := errors.New("boom")
	err := &TableMetadataError{Op: "list", Err: cause}
	assert.True(t, errors.Is(err, cause))
}

func TestSequenceEmptyDatabaseError(t *testing.T) {
	err := &SequenceEmptyDatabaseError{}
	assert.Equal(t, "service: database name must not be empty for sequence operation", err.Error())
}

func TestSequenceEmptyNameError(t *testing.T) {
	err := &SequenceEmptyNameError{}
	assert.Equal(t, "service: sequence name must not be empty", err.Error())
}

func TestSequenceInvalidCountError(t *testing.T) {
	err := &SequenceInvalidCountError{}
	assert.Equal(t, "service: sequence allocation count must be greater than zero", err.Error())
}

func TestSequenceConflictError(t *testing.T) {
	err := &SequenceConflictError{Database: "db", Name: "__tableid"}
	assert.Equal(t, "service: sequence db/__tableid: too many concurrent modifications", err.Error())
}

// causeOnlyError exposes its wrapped error through Cause() alone, as
// go-mysql-server's error kinds (sql.ErrLockDeadlock) do.
type causeOnlyError struct{ cause error }

func (e *causeOnlyError) Error() string { return "wrapped: " + e.cause.Error() }
func (e *causeOnlyError) Cause() error  { return e.cause }

// wrappingError exposes its wrapped error through Unwrap.
type wrappingError struct{ err error }

func (e *wrappingError) Error() string { return e.err.Error() }
func (e *wrappingError) Unwrap() error { return e.err }

// TestIsTableResolutionStale recognizes both stale-resolution errors
// however they arrive: bare, behind Unwrap, behind Cause() (the SQL
// engine's serialization failure), or behind a mix of both.
func TestIsTableResolutionStale(t *testing.T) {
	generation := &TableGenerationStaleError{Database: "db", Name: "t", Expected: 0, Actual: 1}
	definition := &TableDefinitionStaleError{Database: "db", Name: "t", Reason: "index idx was dropped"}
	cases := map[string]struct {
		err  error
		want bool
	}{
		"nil":                              {nil, false},
		"unrelated":                        {errors.New("boom"), false},
		"unrelated behind a cause":         {&causeOnlyError{cause: errors.New("boom")}, false},
		"generation":                       {generation, true},
		"definition":                       {definition, true},
		"generation behind unwrap":         {&wrappingError{err: generation}, true},
		"definition behind a cause":        {&causeOnlyError{cause: definition}, true},
		"cause behind unwrap":              {&wrappingError{err: &causeOnlyError{cause: generation}}, true},
		"unwrap behind a cause":            {&causeOnlyError{cause: &wrappingError{err: definition}}, true},
		"layout refusal is not stale":      {&TableLayoutUnsupportedError{Database: "db", Name: "t"}, false},
		"rewrite in progress is not stale": {&TableRewriteInProgressError{Database: "db", Name: "t"}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsTableResolutionStale(tc.err))
		})
	}
}

// selfCausingError is a Cause() cycle, which the walk must survive.
type selfCausingError struct{}

func (e *selfCausingError) Error() string { return "cycle" }
func (e *selfCausingError) Cause() error  { return e }

func TestIsTableResolutionStale_CauseCycleTerminates(t *testing.T) {
	assert.False(t, IsTableResolutionStale(&selfCausingError{}))
}
