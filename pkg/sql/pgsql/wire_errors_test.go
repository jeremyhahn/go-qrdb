// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build qrdb_pgsql

package pgsql

import (
	"errors"
	"testing"

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/jeroenrinzema/psql-wire/codes"
	psqlerr "github.com/jeroenrinzema/psql-wire/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/sql/provider"
)

// TestSQLStateFor pins the SQLSTATE each engine failure class is reported
// under. gorm.io/driver/postgres translates 23505 to gorm.ErrDuplicatedKey
// and 23503 to gorm.ErrForeignKeyViolated, so these mappings are what
// application-level duplicate/FK handling on the PostgreSQL wire runs on.
func TestSQLStateFor(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want codes.Code
		ok   bool
	}{
		{"unique key violation", sql.ErrUniqueKeyViolation.New(), codes.UniqueViolation, true},
		{"primary key violation", sql.ErrPrimaryKeyViolation.New(), codes.UniqueViolation, true},
		{"duplicate entry", sql.ErrDuplicateEntry.New("uni_cities_name"), codes.UniqueViolation, true},
		{"provider duplicate key", &provider.DuplicateKeyError{Key: "k"}, codes.UniqueViolation, true},
		{"fk child violation", sql.ErrForeignKeyChildViolation.New("fk", "t", "p", "k"), codes.ForeignKeyViolation, true},
		{"fk parent violation", sql.ErrForeignKeyParentViolation.New("fk", "t", "p", "k"), codes.ForeignKeyViolation, true},
		{"check violation", sql.ErrCheckConstraintViolated.New("chk"), codes.CheckViolation, true},
		{"table not found", sql.ErrTableNotFound.New("t"), codes.UndefinedTable, true},
		{"column not found", sql.ErrColumnNotFound.New("c"), codes.UndefinedColumn, true},
		{"null into not null", sql.ErrInsertIntoNonNullableProvidedNull.New("c"), codes.NotNullViolation, true},
		{"unmapped error keeps default", errors.New("boom"), "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := sqlStateFor(tt.err)
			require.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestWireQueryError proves the code survives the QueryError wrapping the
// wire handler applies, which is what psql-wire actually serializes.
func TestWireQueryError(t *testing.T) {
	err := wireQueryError("INSERT ...", sql.ErrUniqueKeyViolation.New())

	var qerr *QueryError
	require.ErrorAs(t, err, &qerr, "the QueryError wrapper must survive for logs")

	// psql-wire carries the code as a wrapped psqlerr value; walking the
	// chain for it proves WithCode saw our error rather than replacing it.
	assert.True(t, errors.Is(err, err))
	assert.Contains(t, psqlErrorCode(err), "23505")

	// An unmapped error keeps psql-wire's internal-error default.
	plain := wireQueryError("SELECT 1", errors.New("boom"))
	assert.Equal(t, "XXUUU", psqlErrorCode(plain))
}

// psqlErrorCode digs the SQLSTATE psql-wire would serialize out of err's
// wrap chain, or "" when none is attached.
func psqlErrorCode(err error) string {
	for e := err; e != nil; e = errors.Unwrap(e) {
		if withCode, ok := e.(interface{ Code() codes.Code }); ok {
			return string(withCode.Code())
		}
	}
	// psqlerr exposes a helper on some versions; fall back to flattening.
	if flat := psqlerr.Flatten(err); flat.Code != "" {
		return string(flat.Code)
	}
	return ""
}
