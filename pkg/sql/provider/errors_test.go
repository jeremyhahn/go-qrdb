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

func TestTableNotFoundError(t *testing.T) {
	err := &TableNotFoundError{Database: "qrdb", Table: "users"}
	assert.Equal(t, "provider: table not found: qrdb.users", err.Error())
}

func TestTableNotFoundError_EmptyFields(t *testing.T) {
	err := &TableNotFoundError{Database: "", Table: ""}
	assert.Equal(t, "provider: table not found: .", err.Error())
}

func TestDuplicateKeyError(t *testing.T) {
	err := &DuplicateKeyError{Key: "foo"}
	assert.Equal(t, "provider: duplicate key: foo", err.Error())
}

func TestDuplicateKeyError_EmptyKey(t *testing.T) {
	err := &DuplicateKeyError{Key: ""}
	assert.Equal(t, "provider: duplicate key: ", err.Error())
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
		{"TableNotFoundError", &TableNotFoundError{Database: "db", Table: "t"}},
		{"DuplicateKeyError", &DuplicateKeyError{Key: "k"}},
		{"InvalidKeyError", &InvalidKeyError{Key: "k"}},
		{"InvalidValueError", &InvalidValueError{Reason: "r"}},
		{"CreateDatabaseError", &CreateDatabaseError{Name: "db", Err: errors.New("e")}},
		{"DropDatabaseError", &DropDatabaseError{Name: "db", Err: errors.New("e")}},
		{"MutableOperationNotSupportedError", &MutableOperationNotSupportedError{Operation: "op"}},
		{"IndexLookupError", &IndexLookupError{IndexName: "idx", Err: errors.New("e")}},
		{"RangeTranslationError", &RangeTranslationError{Reason: "bad range"}},
		{"UnsupportedRangeTypeError", &UnsupportedRangeTypeError{RangeType: "99"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NotEmpty(t, tt.err.Error())
		})
	}
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
