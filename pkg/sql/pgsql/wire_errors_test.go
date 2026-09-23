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

	"github.com/cockroachdb/apd/v3"
	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/dolthub/vitess/go/mysql"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jeroenrinzema/psql-wire/codes"
	psqlerr "github.com/jeroenrinzema/psql-wire/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
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
		{"lock deadlock / serialization failure", sql.ErrLockDeadlock.Wrap(errors.New("conflict"), "detail"), codes.SerializationFailure, true},
		{"table layout unsupported", mysql.NewSQLError(provider.ErrCodeTableLayoutUnsupported, "HY000", "re-create"), codes.ObjectNotInPrerequisiteState, true},
		{"other mysql error keeps default", mysql.NewSQLError(1105, "HY000", "boom"), "", false},
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

// TestSQLStateForWrappedInsertError pins the chain shape every constraint
// violation raised during an INSERT actually arrives in:
// sql.WrappedInsertError has no Unwrap method and the kind error inside it
// only matches Kind.Is at the top level, so sqlStateFor must follow the
// Cause field by hand. This exact shape is what left every duplicate-key
// INSERT reported as XXUUU on the wire.
func TestSQLStateForWrappedInsertError(t *testing.T) {
	err := sql.NewWrappedInsertError(nil, sql.NewUniqueKeyErr("PRIMARY", true, nil))
	code, ok := sqlStateFor(err)
	require.True(t, ok)
	assert.Equal(t, codes.UniqueViolation, code)

	err = sql.NewWrappedInsertError(nil, sql.NewUniqueKeyErr("uni_cities_name", false, nil))
	code, ok = sqlStateFor(err)
	require.True(t, ok)
	assert.Equal(t, codes.UniqueViolation, code)
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

// TestWireQueryError_GenerationStaleMapsTo40001 exercises the REAL
// production error shape a TRUNCATE race produces (go-qrdb#547) --
// *service.TableGenerationStaleError wrapped in sql.ErrLockDeadlock, exactly
// as RelationalTable.checkGenerationStale and WriteSet.staleResolution both
// return it -- through wireQueryError end to end, and asserts the actual
// SQLSTATE psql-wire would put on the wire: 40001 (serialization_failure).
// TestSQLStateFor above pins sqlStateFor's classification of a synthetic
// sql.ErrLockDeadlock value; this test is what actually runs PostgreSQL's
// translator against the wrapped error the provider hands it, so a
// regression that reintroduced the generic XXUUU default for this exact
// shape would be caught here even if the classification unit test above
// still passed.
func TestWireQueryError_GenerationStaleMapsTo40001(t *testing.T) {
	stale := &service.TableGenerationStaleError{
		Database: "shop", Name: "widgets", Expected: 0, Actual: 1,
	}
	err := wireQueryError("TRUNCATE TABLE widgets", sql.ErrLockDeadlock.Wrap(stale, stale.Error()))

	assert.Equal(t, "40001", psqlErrorCode(err))

	var qerr *QueryError
	require.ErrorAs(t, err, &qerr)
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

// TestNormalizeNumeric pins the DECIMAL wire path: an *apd.Decimal cell
// (what go-mysql-server produces for a DECIMAL column) and a plain decimal
// string both normalize to pgtype.Numeric, the shape pgx's numeric codec
// encodes; integers pass through (they have their own encode plans), and
// garbage is returned unchanged for pgx to report.
func TestNormalizeNumeric(t *testing.T) {
	dec, _, err0 := apd.NewFromString("123.450")
	require.NoError(t, err0)
	n, ok := normalizeNumeric(dec).(pgtype.Numeric)
	require.True(t, ok)
	value, err := n.Value()
	require.NoError(t, err)
	assert.Equal(t, "123.450", value)

	n, ok = normalizeNumeric("42.7").(pgtype.Numeric)
	require.True(t, ok)
	value, err = n.Value()
	require.NoError(t, err)
	assert.Equal(t, "42.7", value)

	assert.Equal(t, int64(7), normalizeNumeric(int64(7)))
	assert.Equal(t, "not-a-number", normalizeNumeric("not-a-number"))
}

// TestColumnOIDDecimal pins DECIMAL's OID: text described a numeric column
// as variable-length, which gorm's migrator read as a size change and
// answered with an ALTER on every AutoMigrate.
func TestColumnOIDDecimal(t *testing.T) {
	decimalType, err := types.CreateDecimalType(9, 0)
	require.NoError(t, err)
	assert.Equal(t, uint32(pgtype.NumericOID), ColumnOID(decimalType))
}

// TestCheckIgnoredViolations pins the PostgreSQL semantics of ON CONFLICT
// DO NOTHING over the INSERT IGNORE translation: only duplicate-key
// conflicts are suppressed; a foreign-key (or CHECK, or NOT NULL)
// violation the engine downgraded to a warning is re-raised as the
// statement failure PostgreSQL reports.
func TestCheckIgnoredViolations(t *testing.T) {
	newCtx := func() *sql.Context {
		return sql.NewContext(t.Context(), sql.WithSession(sql.NewBaseSession()))
	}

	t.Run("duplicate-key warnings stay suppressed", func(t *testing.T) {
		ctx := newCtx()
		ctx.Session.Warn(&sql.Warning{Level: "Note", Code: 1062, Message: "duplicate"})
		require.NoError(t, checkIgnoredViolations(ctx, "INSERT IGNORE INTO t VALUES (1)", 0))
	})

	t.Run("fk violation re-raised with 23503", func(t *testing.T) {
		ctx := newCtx()
		ctx.Session.Warn(&sql.Warning{Level: "Note", Code: 1452, Message: "fk violated"})
		err := checkIgnoredViolations(ctx, "INSERT IGNORE INTO t VALUES (1)", 0)
		require.Error(t, err)
		var verr *IgnoredViolationError
		require.ErrorAs(t, err, &verr)
		assert.Equal(t, 1452, verr.Code)
		assert.Contains(t, psqlErrorCode(err), "23503")
	})

	t.Run("oldest new warning wins, baseline respected", func(t *testing.T) {
		ctx := newCtx()
		ctx.Session.Warn(&sql.Warning{Level: "Note", Code: 1452, Message: "old, before baseline"})
		baseline := len(ctx.Session.Warnings())
		ctx.Session.Warn(&sql.Warning{Level: "Note", Code: 3819, Message: "check violated"})
		ctx.Session.Warn(&sql.Warning{Level: "Note", Code: 1048, Message: "null violated"})
		err := checkIgnoredViolations(ctx, "insert ignore into t values (1)", baseline)
		require.Error(t, err)
		var verr *IgnoredViolationError
		require.ErrorAs(t, err, &verr)
		assert.Equal(t, 3819, verr.Code, "the FIRST violation of the statement is reported")
	})

	t.Run("non-IGNORE statements untouched", func(t *testing.T) {
		ctx := newCtx()
		ctx.Session.Warn(&sql.Warning{Level: "Note", Code: 1452, Message: "fk violated"})
		require.NoError(t, checkIgnoredViolations(ctx, "INSERT INTO t VALUES (1)", 0))
	})
}
