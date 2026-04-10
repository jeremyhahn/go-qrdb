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
	"errors"
	"io"
	"testing"
)

// TestDSNParseError verifies DSNParseError implements error with correct message.
func TestDSNParseError(t *testing.T) {
	tests := []struct {
		name    string
		dsn     string
		reason  string
		wantMsg string
	}{
		{
			name:    "empty dsn",
			dsn:     "",
			reason:  "empty DSN",
			wantMsg: `driver: invalid DSN "": empty DSN`,
		},
		{
			name:    "with dsn and reason",
			dsn:     "bad://host",
			reason:  "some parse reason",
			wantMsg: `driver: invalid DSN "bad://host": some parse reason`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &DSNParseError{DSN: tt.dsn, Reason: tt.reason}
			var e error = err
			if e == nil {
				t.Fatal("expected non-nil error")
			}
			if err.Error() != tt.wantMsg {
				t.Errorf("Error() = %q, want %q", err.Error(), tt.wantMsg)
			}
		})
	}
}

// TestUnsupportedSchemeError verifies UnsupportedSchemeError implements error.
func TestUnsupportedSchemeError(t *testing.T) {
	tests := []struct {
		name    string
		scheme  string
		wantMsg string
	}{
		{
			name:    "unknown scheme",
			scheme:  "mysql",
			wantMsg: "driver: unsupported scheme: mysql",
		},
		{
			name:    "empty scheme",
			scheme:  "",
			wantMsg: "driver: unsupported scheme: ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &UnsupportedSchemeError{Scheme: tt.scheme}
			var e error = err
			if e == nil {
				t.Fatal("expected non-nil error")
			}
			if err.Error() != tt.wantMsg {
				t.Errorf("Error() = %q, want %q", err.Error(), tt.wantMsg)
			}
		})
	}
}

// TestConnClosedError verifies ConnClosedError implements error.
func TestConnClosedError(t *testing.T) {
	err := &ConnClosedError{}
	var e error = err
	if e == nil {
		t.Fatal("expected non-nil error")
	}
	want := "driver: connection is closed"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

// TestConnClosedErrorIsError verifies ConnClosedError is usable with errors.As.
func TestConnClosedErrorIsError(t *testing.T) {
	err := &ConnClosedError{}
	var target *ConnClosedError
	if !errors.As(err, &target) {
		t.Error("errors.As should find ConnClosedError")
	}
}

// TestTxDoneError verifies TxDoneError implements error.
func TestTxDoneError(t *testing.T) {
	err := &TxDoneError{}
	var e error = err
	if e == nil {
		t.Fatal("expected non-nil error")
	}
	want := "driver: transaction has already been committed or rolled back"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

// TestTxDoneErrorIsError verifies TxDoneError is usable with errors.As.
func TestTxDoneErrorIsError(t *testing.T) {
	err := &TxDoneError{}
	var target *TxDoneError
	if !errors.As(err, &target) {
		t.Error("errors.As should find TxDoneError")
	}
}

// TestBackendCreateError verifies BackendCreateError wraps the underlying error.
func TestBackendCreateError(t *testing.T) {
	inner := io.ErrUnexpectedEOF
	err := &BackendCreateError{Backend: "pebble", Err: inner}

	wantMsg := `driver: failed to create backend "pebble": unexpected EOF`
	if err.Error() != wantMsg {
		t.Errorf("Error() = %q, want %q", err.Error(), wantMsg)
	}

	if !errors.Is(err, inner) {
		t.Error("errors.Is should find inner error via Unwrap")
	}
}

// TestBackendCreateErrorUnwrap verifies Unwrap returns the wrapped error.
func TestBackendCreateErrorUnwrap(t *testing.T) {
	inner := io.EOF
	err := &BackendCreateError{Backend: "memory", Err: inner}
	if err.Unwrap() != inner {
		t.Errorf("Unwrap() = %v, want %v", err.Unwrap(), inner)
	}
}

// TestEngineInitError verifies EngineInitError wraps the underlying error.
func TestEngineInitError(t *testing.T) {
	inner := io.ErrClosedPipe
	err := &EngineInitError{Err: inner}

	wantMsg := "driver: failed to initialize SQL engine: io: read/write on closed pipe"
	if err.Error() != wantMsg {
		t.Errorf("Error() = %q, want %q", err.Error(), wantMsg)
	}

	if !errors.Is(err, inner) {
		t.Error("errors.Is should find inner error via Unwrap")
	}
}

// TestEngineInitErrorUnwrap verifies Unwrap returns the wrapped error.
func TestEngineInitErrorUnwrap(t *testing.T) {
	inner := io.ErrNoProgress
	err := &EngineInitError{Err: inner}
	if err.Unwrap() != inner {
		t.Errorf("Unwrap() = %v, want %v", err.Unwrap(), inner)
	}
}

// TestQueryExecError verifies QueryExecError wraps the underlying error.
func TestQueryExecError(t *testing.T) {
	inner := io.ErrUnexpectedEOF
	err := &QueryExecError{Query: "SELECT 1", Err: inner}

	wantMsg := "driver: query execution failed: unexpected EOF"
	if err.Error() != wantMsg {
		t.Errorf("Error() = %q, want %q", err.Error(), wantMsg)
	}

	if !errors.Is(err, inner) {
		t.Error("errors.Is should find inner error via Unwrap")
	}
}

// TestQueryExecErrorUnwrap verifies Unwrap returns the wrapped error.
func TestQueryExecErrorUnwrap(t *testing.T) {
	inner := io.EOF
	err := &QueryExecError{Query: "BEGIN", Err: inner}
	if err.Unwrap() != inner {
		t.Errorf("Unwrap() = %v, want %v", err.Unwrap(), inner)
	}
}

// TestNetworkRequestError verifies NetworkRequestError message variants.
func TestNetworkRequestError(t *testing.T) {
	tests := []struct {
		name       string
		url        string
		statusCode int
		inner      error
		wantMsg    string
	}{
		{
			name:    "url only",
			url:     "http://localhost:8080/v1/sql/query",
			wantMsg: "driver: network request to http://localhost:8080/v1/sql/query failed",
		},
		{
			name:       "with status code",
			url:        "http://host/path",
			statusCode: 500,
			wantMsg:    "driver: network request to http://host/path failed (status 500)",
		},
		{
			name:    "with inner error",
			url:     "http://host/path",
			inner:   io.EOF,
			wantMsg: "driver: network request to http://host/path failed: EOF",
		},
		{
			name:       "with status and inner",
			url:        "http://host/path",
			statusCode: 404,
			inner:      io.ErrUnexpectedEOF,
			wantMsg:    "driver: network request to http://host/path failed (status 404): unexpected EOF",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &NetworkRequestError{URL: tt.url, StatusCode: tt.statusCode, Err: tt.inner}
			if err.Error() != tt.wantMsg {
				t.Errorf("Error() = %q, want %q", err.Error(), tt.wantMsg)
			}
		})
	}
}

// TestNetworkRequestErrorUnwrap verifies Unwrap returns the wrapped error.
func TestNetworkRequestErrorUnwrap(t *testing.T) {
	inner := io.EOF
	err := &NetworkRequestError{URL: "http://host", Err: inner}
	if err.Unwrap() != inner {
		t.Errorf("Unwrap() = %v, want %v", err.Unwrap(), inner)
	}
	if !errors.Is(err, inner) {
		t.Error("errors.Is should find inner error via Unwrap")
	}
}

// TestArgumentSubstitutionError verifies ArgumentSubstitutionError message variants.
func TestArgumentSubstitutionError(t *testing.T) {
	tests := []struct {
		name    string
		index   int
		inner   error
		wantMsg string
	}{
		{
			name:    "without inner error",
			index:   2,
			wantMsg: "driver: argument substitution failed at index 2",
		},
		{
			name:    "with inner error",
			index:   0,
			inner:   io.EOF,
			wantMsg: "driver: argument substitution failed at index 0: EOF",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &ArgumentSubstitutionError{Index: tt.index, Err: tt.inner}
			if err.Error() != tt.wantMsg {
				t.Errorf("Error() = %q, want %q", err.Error(), tt.wantMsg)
			}
		})
	}
}

// TestArgumentSubstitutionErrorUnwrap verifies Unwrap returns the wrapped error.
func TestArgumentSubstitutionErrorUnwrap(t *testing.T) {
	inner := io.ErrClosedPipe
	err := &ArgumentSubstitutionError{Index: 1, Err: inner}
	if err.Unwrap() != inner {
		t.Errorf("Unwrap() = %v, want %v", err.Unwrap(), inner)
	}
	if !errors.Is(err, inner) {
		t.Error("errors.Is should find inner error via Unwrap")
	}
}

// TestUnsupportedIsolationError verifies UnsupportedIsolationError message.
func TestUnsupportedIsolationError(t *testing.T) {
	tests := []struct {
		name    string
		level   sql.IsolationLevel
		wantMsg string
	}{
		{
			name:    "read uncommitted",
			level:   sql.LevelReadUncommitted,
			wantMsg: "driver: unsupported isolation level: " + sql.LevelReadUncommitted.String(),
		},
		{
			name:    "repeatable read",
			level:   sql.LevelRepeatableRead,
			wantMsg: "driver: unsupported isolation level: " + sql.LevelRepeatableRead.String(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &UnsupportedIsolationError{Level: tt.level}
			var e error = err
			if e == nil {
				t.Fatal("expected non-nil error")
			}
			if err.Error() != tt.wantMsg {
				t.Errorf("Error() = %q, want %q", err.Error(), tt.wantMsg)
			}
		})
	}
}

// TestConnectorClosedError verifies ConnectorClosedError implements error.
func TestConnectorClosedError(t *testing.T) {
	err := &ConnectorClosedError{}
	var e error = err
	if e == nil {
		t.Fatal("expected non-nil error")
	}
	want := "driver: connector is closed"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

// TestConnectorClosedErrorIsError verifies ConnectorClosedError is usable with errors.As.
func TestConnectorClosedErrorIsError(t *testing.T) {
	err := &ConnectorClosedError{}
	var target *ConnectorClosedError
	if !errors.As(err, &target) {
		t.Error("errors.As should find ConnectorClosedError")
	}
}

// TestNetworkResponseError verifies NetworkResponseError implements error.
func TestNetworkResponseError(t *testing.T) {
	tests := []struct {
		name    string
		message string
		wantMsg string
	}{
		{
			name:    "with message",
			message: "key not found",
			wantMsg: "driver: server error: key not found",
		},
		{
			name:    "empty message",
			message: "",
			wantMsg: "driver: server error: ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &NetworkResponseError{Message: tt.message}
			var e error = err
			if e == nil {
				t.Fatal("expected non-nil error")
			}
			if err.Error() != tt.wantMsg {
				t.Errorf("Error() = %q, want %q", err.Error(), tt.wantMsg)
			}
		})
	}
}

// TestStmtClosedError verifies StmtClosedError implements error.
func TestStmtClosedError(t *testing.T) {
	err := &StmtClosedError{}
	var e error = err
	if e == nil {
		t.Fatal("expected non-nil error")
	}
	want := "driver: statement is closed"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

// TestStmtClosedErrorIsError verifies StmtClosedError is usable with errors.As.
func TestStmtClosedErrorIsError(t *testing.T) {
	err := &StmtClosedError{}
	var target *StmtClosedError
	if !errors.As(err, &target) {
		t.Error("errors.As should find StmtClosedError")
	}
}

// TestRowsClosedError verifies RowsClosedError implements error.
func TestRowsClosedError(t *testing.T) {
	err := &RowsClosedError{}
	var e error = err
	if e == nil {
		t.Fatal("expected non-nil error")
	}
	want := "driver: rows are closed"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

// TestRowsClosedErrorIsError verifies RowsClosedError is usable with errors.As.
func TestRowsClosedErrorIsError(t *testing.T) {
	err := &RowsClosedError{}
	var target *RowsClosedError
	if !errors.As(err, &target) {
		t.Error("errors.As should find RowsClosedError")
	}
}
