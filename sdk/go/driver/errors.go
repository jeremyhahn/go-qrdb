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
	"database/sql"
	"reflect"
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

// MissingServiceError is returned when a connector is constructed without a
// service it cannot function without. It is reported at construction rather
// than tolerated: a connector missing its registry degrades to a KV-only
// database that fails every CREATE TABLE, which is far harder to diagnose
// from the resulting error than from the missing argument itself.
type MissingServiceError struct {
	// Service names the missing dependency, e.g. "kv" or "registry".
	Service string
}

// Error returns a human-readable description of the missing service.
func (e *MissingServiceError) Error() string {
	return "driver: connector requires a non-nil " + e.Service + " service"
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

// UnsupportedArgumentTypeError is returned when a query argument has a Go type
// the driver cannot bind. It is reported rather than coerced: a value bound as
// the wrong type is a silently wrong query result, which is worse than a
// failed one.
type UnsupportedArgumentTypeError struct {
	// Ordinal is the 1-based position of the offending argument.
	Ordinal int

	// Value is the argument that could not be bound.
	Value any
}

// Error returns a human-readable description of the unbindable argument.
func (e *UnsupportedArgumentTypeError) Error() string {
	return "driver: cannot bind argument " + strconv.Itoa(e.Ordinal) +
		" of type " + typeNameOf(e.Value)
}

// NamedParameterUnsupportedError is returned when a named parameter reaches
// the driver. database/sql resolves sql.Named into positional arguments before
// the driver is called, so a name arriving here means that resolution was
// bypassed and the value cannot be positionally bound.
type NamedParameterUnsupportedError struct {
	Name string
}

// Error returns a human-readable description of the unsupported named parameter.
func (e *NamedParameterUnsupportedError) Error() string {
	return "driver: named parameter " + strconv.Quote(e.Name) +
		" is not supported; use positional ? placeholders"
}

// typeNameOf returns a readable Go type name for inclusion in error messages.
func typeNameOf(v any) string {
	if v == nil {
		return "<nil>"
	}
	return reflect.TypeOf(v).String()
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

// ParameterizedQueryUnsupportedError is returned when a query carrying bound
// arguments is issued over a network connection.
//
// The REST and gRPC SQL surface accepts a statement and nothing else — it has
// no field for arguments. The previous behaviour, splicing the values into the
// SQL text, is exactly the injection surface this driver removed, so the
// request is refused instead. Use an embedded connection, or qrdb's MySQL wire
// protocol, when parameters are required.
type ParameterizedQueryUnsupportedError struct {
	Scheme string
	Count  int
}

// Error returns a human-readable description of the unsupported parameters.
func (e *ParameterizedQueryUnsupportedError) Error() string {
	return "driver: " + e.Scheme + " connections cannot bind query parameters (" +
		strconv.Itoa(e.Count) + " supplied); the network SQL API accepts a statement only"
}

// ResultMetadataUnsupportedError is returned when a caller asks for result
// metadata the connection's protocol does not carry.
type ResultMetadataUnsupportedError struct {
	Field string
}

// Error returns a human-readable description of the missing metadata.
func (e *ResultMetadataUnsupportedError) Error() string {
	return "driver: " + e.Field + " is not reported by the network SQL API"
}

// PrepareError is returned when the engine cannot prepare a statement.
type PrepareError struct {
	Query string
	Err   error
}

// Error returns a human-readable description of the prepare failure.
func (e *PrepareError) Error() string {
	msg := "driver: failed to prepare query " + strconv.Quote(e.Query)
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *PrepareError) Unwrap() error {
	return e.Err
}

// TransactionUnsupportedError is returned when a transaction is requested on a
// connection whose transport cannot provide one.
//
// The REST and gRPC SQL API executes one statement per request with no session
// affinity, so BEGIN has nothing to attach to. Returning a transaction handle
// that discards COMMIT and ROLLBACK would make a rollback appear to succeed
// while changing nothing, so the request is refused.
type TransactionUnsupportedError struct {
	Scheme string
}

// Error returns a human-readable description of the unsupported transaction.
func (e *TransactionUnsupportedError) Error() string {
	return "driver: " + e.Scheme + " connections do not support transactions; " +
		"the network SQL API executes one statement per request"
}
