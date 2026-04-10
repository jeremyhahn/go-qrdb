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

import "strconv"

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
