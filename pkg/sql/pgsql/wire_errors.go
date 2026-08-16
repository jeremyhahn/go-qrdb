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

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/jeroenrinzema/psql-wire/codes"
	psqlerr "github.com/jeroenrinzema/psql-wire/errors"

	"github.com/jeremyhahn/go-qrdb/pkg/sql/provider"
)

// wireQueryError wraps a failed statement's error for the PostgreSQL wire,
// attaching the standard SQLSTATE the failure class carries in PostgreSQL
// itself. Without a recognised code psql-wire reports every error as XXUUU,
// which no client understands -- most consequentially
// gorm.io/driver/postgres's error translator, which turns 23505 into
// gorm.ErrDuplicatedKey and 23503 into gorm.ErrForeignKeyViolated and
// passes anything else through untranslated, so ON CONFLICT upsert
// fallbacks and duplicate-detection application flows silently break.
//
// Only failure classes with an unambiguous PostgreSQL equivalent are
// mapped; everything else keeps psql-wire's internal-error default rather
// than guessing a code the client would then act on.
func wireQueryError(query string, err error) error {
	qerr := &QueryError{Query: query, Err: err}
	if code, ok := sqlStateFor(err); ok {
		return psqlerr.WithCode(qerr, code)
	}
	return qerr
}

// sqlStateFor maps an engine error to the SQLSTATE PostgreSQL would raise
// for the same condition, or ok=false when no unambiguous mapping exists.
func sqlStateFor(err error) (codes.Code, bool) {
	var duplicate *provider.DuplicateKeyError
	switch {
	case sql.ErrUniqueKeyViolation.Is(err),
		sql.ErrPrimaryKeyViolation.Is(err),
		sql.ErrDuplicateEntry.Is(err),
		errors.As(err, &duplicate):
		return codes.UniqueViolation, true
	case sql.ErrForeignKeyChildViolation.Is(err),
		sql.ErrForeignKeyParentViolation.Is(err):
		return codes.ForeignKeyViolation, true
	case sql.ErrCheckConstraintViolated.Is(err):
		return codes.CheckViolation, true
	case sql.ErrTableNotFound.Is(err):
		return codes.UndefinedTable, true
	case sql.ErrColumnNotFound.Is(err), sql.ErrTableColumnNotFound.Is(err):
		return codes.UndefinedColumn, true
	case sql.ErrInsertIntoNonNullableProvidedNull.Is(err):
		return codes.NotNullViolation, true
	}
	return "", false
}
