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

package gorm

import (
	"errors"

	gmssql "github.com/dolthub/go-mysql-server/sql"
	"gorm.io/gorm"

	qrdbdriver "github.com/jeremyhahn/go-qrdb/sdk/go/driver"
)

// mysqlErrorCodes maps a MySQL error number to the gorm sentinel error it
// represents. The numbers are byte-identical to go-gorm/mysql's own table:
// 1062 duplicate entry, 1451/1452 foreign key violation, 3819 check
// constraint violated. 1146 (unknown table) is deliberately absent — GORM
// already produces gorm.ErrRecordNotFound itself from a zero-row First, and
// mapping a missing table onto that sentinel would hide a real bug (a typo
// in a table name, a migration that never ran) behind the same error a
// caller expects for "no rows matched".
var mysqlErrorCodes = map[uint16]error{
	1062: gorm.ErrDuplicatedKey,
	1451: gorm.ErrForeignKeyViolated,
	1452: gorm.ErrForeignKeyViolated,
	3819: gorm.ErrCheckConstraintViolated,
}

// Translate converts a driver or engine error into the gorm sentinel error
// it represents, so callers can compare with errors.Is(err,
// gorm.ErrDuplicatedKey) instead of matching on message text.
//
// It tries two paths, in order:
//
//  1. err wraps a *qrdbdriver.SQLStateError — the case for every query that
//     went through sdk/go/driver, embedded or over qrdb's MySQL wire port —
//     and its Number is looked up in mysqlErrorCodes.
//  2. err is one of go-mysql-server's own typed error kinds
//     (sql.ErrPrimaryKeyViolation, sql.ErrUniqueKeyViolation,
//     sql.ErrDuplicateEntry map to gorm.ErrDuplicatedKey;
//     sql.ErrForeignKeyChildViolation and sql.ErrForeignKeyParentViolation
//     map to gorm.ErrForeignKeyViolated) — the case for a caller that talks
//     to the embedded go-mysql-server engine directly, bypassing the
//     driver.
//
// Because path 1's numbers are the same ones go-sql-driver/mysql surfaces
// as *mysql.MySQLError for the identical statement run over the MySQL wire
// port (see SQLStateError), this one table serves both without a special
// case, and a divergence between the two is a bug rather than something to
// work around here.
//
// An error matching neither path is returned unchanged.
func (dialector Dialector) Translate(err error) error {
	var sqlStateErr *qrdbdriver.SQLStateError
	if errors.As(err, &sqlStateErr) {
		if translated, ok := mysqlErrorCodes[sqlStateErr.Number]; ok {
			return translated
		}
		return err
	}

	switch {
	case gmssql.ErrPrimaryKeyViolation.Is(err),
		gmssql.ErrUniqueKeyViolation.Is(err),
		gmssql.ErrDuplicateEntry.Is(err):
		return gorm.ErrDuplicatedKey
	case gmssql.ErrForeignKeyChildViolation.Is(err),
		gmssql.ErrForeignKeyParentViolation.Is(err):
		return gorm.ErrForeignKeyViolated
	default:
		return err
	}
}
