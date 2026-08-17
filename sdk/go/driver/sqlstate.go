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
	"strconv"

	gmssql "github.com/dolthub/go-mysql-server/sql"
)

// SQLStateError carries the MySQL-compatible error number and SQLSTATE that
// go-mysql-server assigned to a failure.
//
// It exists so that a caller can classify an error by code rather than by
// matching on message text. That matters most for the qrdb GORM dialector: the
// numbers here are the same ones go-sql-driver/mysql surfaces as
// *mysql.MySQLError when the identical statement runs over qrdb's MySQL wire
// port. One error-translation table therefore serves both the embedded engine
// and the wire protocol, and a divergence between the two is a bug rather than
// a special case.
//
// Codes of note: 1062 duplicate entry, 1451/1452 foreign key violation, 1146
// unknown table, 3819 check constraint violated.
type SQLStateError struct {
	// Number is the MySQL error number (e.g. 1062 for a duplicate entry).
	Number uint16

	// SQLState is the five-character SQLSTATE (e.g. "23000"), empty when
	// go-mysql-server did not assign one.
	SQLState string

	// Message is the engine's description of the failure.
	Message string

	// Query is the statement that failed, when known.
	Query string

	// Err is the underlying engine error.
	Err error
}

// Error returns a human-readable description including the MySQL error number.
func (e *SQLStateError) Error() string {
	msg := "driver: sql error " + strconv.Itoa(int(e.Number))
	if e.SQLState != "" {
		msg += " (SQLSTATE " + e.SQLState + ")"
	}
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *SQLStateError) Unwrap() error {
	return e.Err
}

// asSQLStateError converts an engine error into a *SQLStateError carrying the
// MySQL error number go-mysql-server assigns it. It returns nil for a nil
// error so it can wrap a result directly.
//
// query is recorded for diagnostics only; it is never re-parsed.
func asSQLStateError(err error, query string) error {
	if err == nil {
		return nil
	}

	sqlErr := gmssql.CastSQLError(err)
	if sqlErr == nil {
		return err
	}

	return &SQLStateError{
		Number:   uint16(sqlErr.Num),
		SQLState: sqlErr.State,
		Message:  sqlErr.Message,
		Query:    query,
		Err:      err,
	}
}
