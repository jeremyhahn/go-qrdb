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

// Package pgsql provides a PostgreSQL wire protocol server for QRDB.
// It uses psql-wire for PG wire protocol framing and routes queries
// through go-mysql-server for SQL execution against the QRDB storage
// layer.
package pgsql

import (
	"fmt"
	"strconv"
)

// ServerStartError is returned when the PostgreSQL wire protocol server
// fails to start.
type ServerStartError struct {
	Err error
}

// Error returns a human-readable description.
func (e *ServerStartError) Error() string {
	if e.Err != nil {
		return "sql/pgsql: server start failed: " + e.Err.Error()
	}
	return "sql/pgsql: server start failed"
}

// Unwrap returns the underlying error.
func (e *ServerStartError) Unwrap() error {
	return e.Err
}

// AuthenticationError is returned when mTLS authentication fails for a
// PostgreSQL wire connection.
type AuthenticationError struct {
	Err error
}

// Error returns a human-readable description.
func (e *AuthenticationError) Error() string {
	if e.Err != nil {
		return "sql/pgsql: authentication failed: " + e.Err.Error()
	}
	return "sql/pgsql: authentication failed"
}

// Unwrap returns the underlying error.
func (e *AuthenticationError) Unwrap() error {
	return e.Err
}

// QueryError is returned when a SQL query fails during execution.
type QueryError struct {
	Query string
	Err   error
}

// Error returns a human-readable description.
func (e *QueryError) Error() string {
	if e.Err != nil {
		return "sql/pgsql: query failed: " + e.Query + ": " + e.Err.Error()
	}
	return "sql/pgsql: query failed: " + e.Query
}

// Unwrap returns the underlying error.
func (e *QueryError) Unwrap() error {
	return e.Err
}

// TypeConversionError is returned when converting between go-mysql-server
// and PostgreSQL types fails.
type TypeConversionError struct {
	GMS string
	PG  string
}

// Error returns a human-readable description.
func (e *TypeConversionError) Error() string {
	return "sql/pgsql: type conversion: " + e.GMS + " -> " + e.PG
}

// ParameterBindingError is returned when a PostgreSQL wire protocol
// parameter cannot be converted to a GMS binding expression.
type ParameterBindingError struct {
	Index int
	Err   error
}

// Error returns a human-readable description.
func (e *ParameterBindingError) Error() string {
	if e.Err != nil {
		return "sql/pgsql: parameter binding failed at index " + strconv.Itoa(e.Index) + ": " + e.Err.Error()
	}
	return "sql/pgsql: parameter binding failed at index " + strconv.Itoa(e.Index)
}

// Unwrap returns the underlying error.
func (e *ParameterBindingError) Unwrap() error {
	return e.Err
}

// TranslationError is returned when PG-to-GMS query translation fails.
type TranslationError struct {
	Query string
	Err   error
}

// Error returns a human-readable description.
func (e *TranslationError) Error() string {
	if e.Err != nil {
		return "sql/pgsql: translation failed: " + e.Query + ": " + e.Err.Error()
	}
	return "sql/pgsql: translation failed: " + e.Query
}

// Unwrap returns the underlying error.
func (e *TranslationError) Unwrap() error {
	return e.Err
}

// UnsupportedParameterTypeError is returned when a bind parameter decodes,
// via its declared PostgreSQL OID, to a Go type that paramExpr has no
// binding expression for. It should not occur for any OID
// inferParamOIDs declares, since paramExpr covers all of them; it exists
// as a defensive backstop so an unrecognized decoded value is reported
// explicitly instead of guessed at.
type UnsupportedParameterTypeError struct {
	Value any
}

// Error returns a human-readable description.
func (e *UnsupportedParameterTypeError) Error() string {
	return fmt.Sprintf("sql/pgsql: unsupported parameter value type %T", e.Value)
}

// CachedPlanResultTypeError is returned when a prepared statement produces a
// result shape that differs from the one it was described with during the
// PARSE/Describe phase.
//
// A PostgreSQL client is allowed to PARSE a statement once and then BIND and
// EXECUTE it many times, reusing the RowDescription it was given at Describe
// time; pgx does exactly this by default (QueryExecModeCacheStatement), and
// so therefore does gorm.io/driver/postgres. If DDL changes the underlying
// table in between, the rows the statement now produces no longer match the
// descriptor the client is still decoding against -- silently mis-mapping
// values into the wrong columns if the widths happen to agree.
//
// PostgreSQL detects this when it revalidates the cached plan and refuses to
// execute, and this error is the same refusal: the message and the SQLSTATE
// it is delivered with (0A000, feature_not_supported) are PostgreSQL's own,
// verbatim, because clients recognise the condition by them. pgx drops the
// statement from its cache on any error, so a client that simply retries the
// same SQL re-PARSEs it and succeeds -- which is how PostgreSQL itself
// behaves and why the retry is the documented recovery rather than something
// this server should attempt on the client's behalf.
type CachedPlanResultTypeError struct {
	// Declared is the column count sent in the RowDescription at Describe
	// time; Actual is the count the statement produced at EXECUTE time.
	// Equal counts with differing column names or type OIDs are also a
	// mismatch, which is precisely the case that would otherwise corrupt
	// silently.
	Declared int
	Actual   int
}

// Error returns PostgreSQL's own wording for this condition.
func (e *CachedPlanResultTypeError) Error() string {
	return "cached plan must not change result type"
}

// BarrierSealedError is returned when a PostgreSQL connection or query
// is attempted while the storage barrier is sealed. The server is
// fail-closed during the sealed window: only the unseal-flow endpoints
// on the multi-protocol API surface accept requests until the barrier
// is unsealed. It is delivered to clients with SQLSTATE 57P03
// (cannot_connect_now).
type BarrierSealedError struct{}

// Error returns a human-readable description.
func (e *BarrierSealedError) Error() string {
	return "sql/pgsql: barrier sealed"
}
