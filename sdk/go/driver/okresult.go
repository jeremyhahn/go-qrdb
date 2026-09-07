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

package driver

import (
	"errors"
	"io"

	gmssql "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
)

// drainOkResult consumes a go-mysql-server result set and extracts the
// types.OkResult a DML statement produces, always closing the iterator.
//
// go-mysql-server reports the outcome of INSERT/UPDATE/DELETE as a single row
// holding an OkResult, under a schema of one column named
// types.OkResultColumnName. That row carries the authoritative RowsAffected
// and, for an auto-increment insert, the generated InsertID.
//
// Reading it is what makes LastInsertId work. Counting iterator rows instead —
// as this driver previously did — yields 1 for every DML statement, because
// the OkResult row is the only row emitted, and yields no insert id at all.
// GORM depends on the insert id to populate a model's primary key after
// Create, so this is load-bearing rather than cosmetic.
//
// ok reports whether the result was a DML OkResult. It is false for statements
// that return a real result set (SELECT) or nothing at all (most DDL), in
// which case the caller should report zero rows affected.
func drainOkResult(ctx *gmssql.Context, schema gmssql.Schema, iter gmssql.RowIter) (res types.OkResult, ok bool, err error) {
	defer func() {
		closeErr := iter.Close(ctx)
		if err == nil && closeErr != nil {
			err = closeErr
		}
	}()

	if !isOkResultSchema(schema) {
		// Not a DML result: drain so the iterator's resources are released,
		// then report that there is no OkResult to read.
		if drainErr := drainRows(ctx, iter); drainErr != nil {
			return types.OkResult{}, false, drainErr
		}
		return types.OkResult{}, false, nil
	}

	row, rowErr := iter.Next(ctx)
	if errors.Is(rowErr, io.EOF) {
		return types.OkResult{}, false, nil
	}
	if rowErr != nil {
		return types.OkResult{}, false, rowErr
	}

	okRes, isOk := okResultOfRow(row)
	if !isOk {
		return types.OkResult{}, false, nil
	}

	// Drain any remainder so the iterator closes cleanly.
	if drainErr := drainRows(ctx, iter); drainErr != nil {
		return types.OkResult{}, false, drainErr
	}
	return okRes, true, nil
}

// isOkResultSchema reports whether schema is the single-column shape
// go-mysql-server uses to carry an OkResult.
func isOkResultSchema(schema gmssql.Schema) bool {
	return len(schema) == 1 && schema[0] != nil && schema[0].Name == types.OkResultColumnName
}

// okResultOfRow extracts an OkResult from a result row, accepting either the
// value or pointer form go-mysql-server may produce.
func okResultOfRow(row gmssql.Row) (types.OkResult, bool) {
	if len(row) != 1 {
		return types.OkResult{}, false
	}
	switch v := row[0].(type) {
	case types.OkResult:
		return v, true
	case *types.OkResult:
		if v == nil {
			return types.OkResult{}, false
		}
		return *v, true
	default:
		return types.OkResult{}, false
	}
}

// drainRows consumes the remaining rows of an iterator, discarding them.
func drainRows(ctx *gmssql.Context, iter gmssql.RowIter) error {
	for {
		_, err := iter.Next(ctx)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
