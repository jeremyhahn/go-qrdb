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
	"testing"

	gmssql "github.com/dolthub/go-mysql-server/sql"
)

// TestSQLStateError_Message verifies the rendered message includes the number
// and, when present, the SQLSTATE.
func TestSQLStateError_Message(t *testing.T) {
	tests := []struct {
		name string
		err  *SQLStateError
		want string
	}{
		{
			name: "number, state and message",
			err:  &SQLStateError{Number: 1062, SQLState: "23000", Message: "duplicate entry"},
			want: "driver: sql error 1062 (SQLSTATE 23000): duplicate entry",
		},
		{
			name: "no state",
			err:  &SQLStateError{Number: 1105, Message: "unknown error"},
			want: "driver: sql error 1105: unknown error",
		},
		{
			name: "number only",
			err:  &SQLStateError{Number: 1146},
			want: "driver: sql error 1146",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSQLStateError_Unwrap verifies the underlying engine error stays
// reachable through errors.Is.
func TestSQLStateError_Unwrap(t *testing.T) {
	inner := errors.New("inner")
	err := &SQLStateError{Number: 1, Err: inner}

	if !errors.Is(err, inner) {
		t.Error("errors.Is should find the inner error via Unwrap")
	}
}

// TestAsSQLStateError_NilPassthrough verifies a nil error stays nil so the
// helper can wrap a result directly.
func TestAsSQLStateError_NilPassthrough(t *testing.T) {
	if err := asSQLStateError(nil, "SELECT 1"); err != nil {
		t.Errorf("asSQLStateError(nil) = %v, want nil", err)
	}
}

// TestAsSQLStateError_AssignsMySQLNumber verifies a go-mysql-server error is
// mapped to the MySQL error number a wire client would receive for the same
// failure. That correspondence is what lets one error-translation table serve
// both the embedded engine and the MySQL wire protocol.
func TestAsSQLStateError_AssignsMySQLNumber(t *testing.T) {
	tests := []struct {
		name       string
		engineErr  error
		wantNumber uint16
	}{
		{
			name:       "table not found",
			engineErr:  gmssql.ErrTableNotFound.New("missing"),
			wantNumber: 1146,
		},
		{
			name:       "duplicate entry",
			engineErr:  gmssql.ErrPrimaryKeyViolation.New(),
			wantNumber: 1062,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := asSQLStateError(tt.engineErr, "SELECT 1")

			var stateErr *SQLStateError
			if !errors.As(err, &stateErr) {
				t.Fatalf("error = %v (%T), want *SQLStateError", err, err)
			}
			if stateErr.Number != tt.wantNumber {
				t.Errorf("Number = %d, want %d", stateErr.Number, tt.wantNumber)
			}
			if stateErr.Query != "SELECT 1" {
				t.Errorf("Query = %q, want SELECT 1", stateErr.Query)
			}
			if !errors.Is(err, tt.engineErr) {
				t.Error("original engine error is not reachable via errors.Is")
			}
		})
	}
}
