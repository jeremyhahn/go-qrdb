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
	gomysql "github.com/go-sql-driver/mysql"

	gmssql "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/vitess/go/mysql"
)

// engineError returns the error the embedded connection surfaces for err, a
// failure the SQL engine reported, using wrap for a failure it keeps typed.
//
// A failure go-mysql-server classifies with a MySQL error number is returned
// as go-sql-driver's *mysql.MySQLError -- Number, SQLState and Message -- the
// very type and values the same statement produces over qrdb's MySQL wire
// port. It is returned bare, not wrapped, because that is the contract
// gorm.io/driver/mysql's error translator is written against: it asserts the
// error IS a *mysql.MySQLError, so 1062 becomes gorm.ErrDuplicatedKey and
// 1451/1452 gorm.ErrForeignKeyViolated with the stock dialector, exactly as
// over the wire.
//
// A failure the engine could NOT classify -- go-mysql-server's
// ER_UNKNOWN_ERROR fallback, which is what a context cancellation or one of
// qrdb's own typed errors (a relational catalog that is unavailable, a
// write-set limit, a cross-database transaction) becomes -- is not given a
// number it does not have. It keeps its typed chain inside wrap(err), so
// errors.Is(err, context.Canceled) and errors.As against qrdb's error types
// keep working; over the wire those same failures can only arrive as a
// message.
func engineError(err error, wrap func(error) error) error {
	if err == nil {
		return nil
	}
	sqlErr := gmssql.CastSQLError(err)
	if sqlErr == nil || sqlErr.Num == mysql.ERUnknownError {
		return wrap(err)
	}
	return asMySQLError(sqlErr)
}

// asMySQLError renders an engine SQL error as go-sql-driver's error type.
func asMySQLError(sqlErr *mysql.SQLError) *gomysql.MySQLError {
	out := &gomysql.MySQLError{Number: uint16(sqlErr.Num), Message: sqlErr.Message}
	copy(out.SQLState[:], sqlErr.State)
	return out
}

// keepTyped is engineError's wrap for a call site that adds no context of its
// own: the error is returned as it is.
func keepTyped(err error) error { return err }
