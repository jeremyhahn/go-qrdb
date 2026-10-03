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
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/stretchr/testify/assert"
)

func TestDatabaseNotFoundError(t *testing.T) {
	err := &DatabaseNotFoundError{Name: "mydb"}
	assert.Equal(t, "provider: database not found: mydb", err.Error())
}

func TestDatabaseNotFoundError_EmptyName(t *testing.T) {
	err := &DatabaseNotFoundError{Name: ""}
	assert.Equal(t, "provider: database not found: ", err.Error())
}

func TestInvalidKeyError(t *testing.T) {
	err := &InvalidKeyError{Key: "bad\x00key"}
	assert.Equal(t, "provider: invalid key: bad\x00key", err.Error())
}

func TestInvalidKeyError_EmptyKey(t *testing.T) {
	err := &InvalidKeyError{Key: ""}
	assert.Equal(t, "provider: invalid key: ", err.Error())
}

func TestInvalidValueError(t *testing.T) {
	err := &InvalidValueError{Reason: "too large"}
	assert.Equal(t, "provider: invalid value: too large", err.Error())
}

func TestInvalidValueError_EmptyReason(t *testing.T) {
	err := &InvalidValueError{Reason: ""}
	assert.Equal(t, "provider: invalid value: ", err.Error())
}

// ---------------------------------------------------------------------------
// CreateDatabaseError tests
// ---------------------------------------------------------------------------

func TestCreateDatabaseError(t *testing.T) {
	inner := errors.New("already exists")
	err := &CreateDatabaseError{Name: "mydb", Err: inner}
	assert.Equal(t, "provider: create database failed: mydb: already exists", err.Error())
}

func TestCreateDatabaseError_Unwrap(t *testing.T) {
	inner := errors.New("underlying error")
	err := &CreateDatabaseError{Name: "mydb", Err: inner}
	assert.ErrorIs(t, err, inner)
}

func TestCreateDatabaseError_EmptyName(t *testing.T) {
	inner := errors.New("fail")
	err := &CreateDatabaseError{Name: "", Err: inner}
	assert.Equal(t, "provider: create database failed: : fail", err.Error())
}

// ---------------------------------------------------------------------------
// DropDatabaseError tests
// ---------------------------------------------------------------------------

func TestDropDatabaseError(t *testing.T) {
	inner := errors.New("not found")
	err := &DropDatabaseError{Name: "mydb", Err: inner}
	assert.Equal(t, "provider: drop database failed: mydb: not found", err.Error())
}

func TestDropDatabaseError_Unwrap(t *testing.T) {
	inner := errors.New("underlying error")
	err := &DropDatabaseError{Name: "mydb", Err: inner}
	assert.ErrorIs(t, err, inner)
}

func TestDropDatabaseError_EmptyName(t *testing.T) {
	inner := errors.New("fail")
	err := &DropDatabaseError{Name: "", Err: inner}
	assert.Equal(t, "provider: drop database failed: : fail", err.Error())
}

// ---------------------------------------------------------------------------
// MutableOperationNotSupportedError tests
// ---------------------------------------------------------------------------

func TestMutableOperationNotSupportedError(t *testing.T) {
	err := &MutableOperationNotSupportedError{Operation: "CREATE DATABASE"}
	assert.Equal(t, "provider: CREATE DATABASE not supported without database manager", err.Error())
}

func TestMutableOperationNotSupportedError_DropDatabase(t *testing.T) {
	err := &MutableOperationNotSupportedError{Operation: "DROP DATABASE"}
	assert.Equal(t, "provider: DROP DATABASE not supported without database manager", err.Error())
}

func TestMutableOperationNotSupportedError_EmptyOperation(t *testing.T) {
	err := &MutableOperationNotSupportedError{Operation: ""}
	assert.Equal(t, "provider:  not supported without database manager", err.Error())
}

// TestErrorsImplementErrorInterface verifies that all error types satisfy
// the error interface.
func TestErrorsImplementErrorInterface(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"DatabaseNotFoundError", &DatabaseNotFoundError{Name: "db"}},
		{"InvalidKeyError", &InvalidKeyError{Key: "k"}},
		{"InvalidValueError", &InvalidValueError{Reason: "r"}},
		{"CreateDatabaseError", &CreateDatabaseError{Name: "db", Err: errors.New("e")}},
		{"DropDatabaseError", &DropDatabaseError{Name: "db", Err: errors.New("e")}},
		{"MutableOperationNotSupportedError", &MutableOperationNotSupportedError{Operation: "op"}},
		{"IndexLookupError", &IndexLookupError{IndexName: "idx", Err: errors.New("e")}},
		{"RangeTranslationError", &RangeTranslationError{Reason: "bad range"}},
		{"UnsupportedRangeTypeError", &UnsupportedRangeTypeError{RangeType: "99"}},
		{"RelationalCatalogUnavailableError", &RelationalCatalogUnavailableError{Operation: "CREATE TABLE"}},
		{"TableServiceUnavailableError", &TableServiceUnavailableError{Table: "t"}},
		{"GeneratedColumnUnsupportedError", &GeneratedColumnUnsupportedError{Column: "c"}},
		{"ColumnOrderUnsupportedError", &ColumnOrderUnsupportedError{Table: "t", Column: "c"}},
		{"RewriteRequiredError", &RewriteRequiredError{Table: "t", Column: "c", Operation: "DROP COLUMN"}},
		{"RewriteRequiresColumnError", &RewriteRequiresColumnError{Table: "t"}},
		{"RewriteJournalMissingTargetError", &RewriteJournalMissingTargetError{Database: "db", Name: "t"}},
		{"RewriteColumnNotFoundError", &RewriteColumnNotFoundError{Column: "c"}},
		{"RewriteColumnMismatchError", &RewriteColumnMismatchError{Database: "db", Name: "t"}},
		{"RowRewriteError", &RowRewriteError{Key: "k", Err: errors.New("e")}},
		{"PrimaryKeyRewriteUnsupportedError", &PrimaryKeyRewriteUnsupportedError{Table: "t"}},
		{"IndexNoColumnsError", &IndexNoColumnsError{Index: "idx"}},
		{"ColumnNotFoundOnTableError", &ColumnNotFoundOnTableError{Table: "t", Column: "c"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NotEmpty(t, tt.err.Error())
		})
	}
}

// TestNewRelationalErrors_Unwrap verifies Unwrap on every new error type
// that wraps another error.
func TestNewRelationalErrors_Unwrap(t *testing.T) {
	inner := errors.New("underlying")
	assert.ErrorIs(t, &RowRewriteError{Key: "k", Err: inner}, inner)
}

// ---------------------------------------------------------------------------
// IndexLookupError
// ---------------------------------------------------------------------------

func TestIndexLookupError(t *testing.T) {
	inner := errors.New("timeout")
	err := &IndexLookupError{IndexName: "users.Email", Err: inner}
	assert.Equal(t, "provider: index lookup failed: users.Email: timeout", err.Error())
}

func TestIndexLookupError_Unwrap(t *testing.T) {
	inner := errors.New("timeout")
	err := &IndexLookupError{IndexName: "idx", Err: inner}
	assert.ErrorIs(t, err, inner)
}

func TestIndexLookupError_EmptyIndexName(t *testing.T) {
	inner := errors.New("fail")
	err := &IndexLookupError{IndexName: "", Err: inner}
	assert.Equal(t, "provider: index lookup failed: : fail", err.Error())
}

// ---------------------------------------------------------------------------
// RangeTranslationError
// ---------------------------------------------------------------------------

func TestRangeTranslationError(t *testing.T) {
	err := &RangeTranslationError{Reason: "unsupported range type *provider.badRange"}
	assert.Equal(t, "provider: range translation failed: unsupported range type *provider.badRange", err.Error())
}

func TestRangeTranslationError_EmptyReason(t *testing.T) {
	err := &RangeTranslationError{Reason: ""}
	assert.Equal(t, "provider: range translation failed: ", err.Error())
}

// ---------------------------------------------------------------------------
// UnsupportedRangeTypeError
// ---------------------------------------------------------------------------

func TestUnsupportedRangeTypeError(t *testing.T) {
	err := &UnsupportedRangeTypeError{RangeType: "99"}
	assert.Equal(t, "provider: unsupported range type: 99", err.Error())
}

func TestUnsupportedRangeTypeError_Named(t *testing.T) {
	err := &UnsupportedRangeTypeError{RangeType: "RangeType_Invalid"}
	assert.Equal(t, "provider: unsupported range type: RangeType_Invalid", err.Error())
}

// TestAutoIncrementUnavailableError_IsNoAutoIncrementCol pins that a table
// with no AUTO_INCREMENT column reports the sentinel go-mysql-server's
// AutoIncrementTable contract defines for exactly that condition.
//
// go-mysql-server builds information_schema.TABLES by calling
// PeekNextAutoIncrementValue on every AutoIncrementTable and aborts the whole
// iteration on any error that is not sql.ErrNoAutoIncrementCol. Without this
// match, information_schema.TABLES was empty for every database, so a MySQL
// client could not tell whether a table existed.
func TestAutoIncrementUnavailableError_IsNoAutoIncrementCol(t *testing.T) {
	err := error(&AutoIncrementUnavailableError{Table: "widgets"})

	if !errors.Is(err, sqle.ErrNoAutoIncrementCol) {
		t.Fatalf("errors.Is(%v, sql.ErrNoAutoIncrementCol) = false, want true", err)
	}

	// The typed error must still be recoverable, with its table name intact.
	var typed *AutoIncrementUnavailableError
	if !errors.As(err, &typed) {
		t.Fatal("errors.As must still recover *AutoIncrementUnavailableError")
	}
	if typed.Table != "widgets" {
		t.Errorf("Table = %q, want %q", typed.Table, "widgets")
	}
}

// TestAutoIncrementUnavailableError_MatchesSentinelMessage pins the other
// half of the go-mysql-server contract: SHOW CREATE TABLE tolerates this
// condition by comparing the rendered message against
// sql.ErrNoAutoIncrementCol.Error() rather than by errors.Is (see
// sql/rowexec/show_iters.go). A qrdb-specific message therefore made
// SHOW CREATE TABLE fail outright for every table without an AUTO_INCREMENT
// column, which is most of them.
func TestAutoIncrementUnavailableError_MatchesSentinelMessage(t *testing.T) {
	err := error(&AutoIncrementUnavailableError{Table: "widgets"})

	if err.Error() != sqle.ErrNoAutoIncrementCol.Error() {
		t.Fatalf("Error() = %q, want %q", err.Error(), sqle.ErrNoAutoIncrementCol.Error())
	}
}
