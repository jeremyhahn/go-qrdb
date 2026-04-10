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
	"database/sql"
	"strconv"
)

// DSNParseError is returned when a DSN string cannot be parsed.
type DSNParseError struct {
	DSN    string
	Reason string
}

// Error returns a human-readable description of the parse failure.
func (e *DSNParseError) Error() string {
	return "driver: invalid DSN " + strconv.Quote(e.DSN) + ": " + e.Reason
}

// UnsupportedSchemeError is returned when the DSN scheme is not recognized.
type UnsupportedSchemeError struct {
	Scheme string
}

// Error returns a human-readable description of the unsupported scheme.
func (e *UnsupportedSchemeError) Error() string {
	return "driver: unsupported scheme: " + e.Scheme
}

// ConnClosedError is returned when an operation is attempted on a closed connection.
type ConnClosedError struct{}

// Error returns a human-readable description of the closed connection.
func (e *ConnClosedError) Error() string {
	return "driver: connection is closed"
}

// TxDoneError is returned when an operation is attempted on a finished transaction.
type TxDoneError struct{}

// Error returns a human-readable description of the finished transaction.
func (e *TxDoneError) Error() string {
	return "driver: transaction has already been committed or rolled back"
}

// BackendCreateError is returned when storage backend creation fails.
type BackendCreateError struct {
	Backend string
	Err     error
}

// Error returns a human-readable description of the backend failure.
func (e *BackendCreateError) Error() string {
	return "driver: failed to create backend " + strconv.Quote(e.Backend) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *BackendCreateError) Unwrap() error {
	return e.Err
}

// EngineInitError is returned when the SQL engine fails to initialize.
type EngineInitError struct {
	Err error
}

// Error returns a human-readable description of the engine init failure.
func (e *EngineInitError) Error() string {
	return "driver: failed to initialize SQL engine: " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *EngineInitError) Unwrap() error {
	return e.Err
}

// QueryExecError is returned when SQL query execution fails.
type QueryExecError struct {
	Query string
	Err   error
}

// Error returns a human-readable description of the query failure.
func (e *QueryExecError) Error() string {
	return "driver: query execution failed: " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *QueryExecError) Unwrap() error {
	return e.Err
}

// NetworkRequestError is returned when a network request to a qrdb server fails.
type NetworkRequestError struct {
	URL        string
	StatusCode int
	Err        error
}

// Error returns a human-readable description of the network failure.
func (e *NetworkRequestError) Error() string {
	msg := "driver: network request to " + e.URL + " failed"
	if e.StatusCode != 0 {
		msg += " (status " + strconv.Itoa(e.StatusCode) + ")"
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *NetworkRequestError) Unwrap() error {
	return e.Err
}

// ArgumentSubstitutionError is returned when a query argument cannot be substituted.
type ArgumentSubstitutionError struct {
	Index int
	Err   error
}

// Error returns a human-readable description of the argument substitution failure.
func (e *ArgumentSubstitutionError) Error() string {
	msg := "driver: argument substitution failed at index " + strconv.Itoa(e.Index)
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *ArgumentSubstitutionError) Unwrap() error {
	return e.Err
}

// UnsupportedIsolationError is returned when an unsupported transaction
// isolation level is requested.
type UnsupportedIsolationError struct {
	Level sql.IsolationLevel
}

// Error returns a human-readable description of the unsupported isolation level.
func (e *UnsupportedIsolationError) Error() string {
	return "driver: unsupported isolation level: " + e.Level.String()
}

// ConnectorClosedError is returned when Connect is called on a closed connector.
type ConnectorClosedError struct{}

// Error returns a human-readable description of the closed connector.
func (e *ConnectorClosedError) Error() string {
	return "driver: connector is closed"
}

// NetworkResponseError is returned when the server returns an error in
// the JSON response body.
type NetworkResponseError struct {
	Message string
}

// Error returns the server-provided error message.
func (e *NetworkResponseError) Error() string {
	return "driver: server error: " + e.Message
}

// StmtClosedError is returned when an operation is attempted on a closed statement.
type StmtClosedError struct{}

// Error returns a human-readable description of the closed statement.
func (e *StmtClosedError) Error() string {
	return "driver: statement is closed"
}

// RowsClosedError is returned when an operation is attempted on closed rows.
type RowsClosedError struct{}

// Error returns a human-readable description of the closed rows.
func (e *RowsClosedError) Error() string {
	return "driver: rows are closed"
}
