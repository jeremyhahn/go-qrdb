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

package catalog

import (
	"errors"
	"testing"
)

// TestTypedErrors_ImplementError exercises every typed error's Error()
// method (and Unwrap(), where present) so that each carries a useful,
// non-empty message.
func TestTypedErrors_ImplementError(t *testing.T) {
	cause := errors.New("underlying cause")

	cases := []error{
		&NilTableDefError{},
		&EmptyColumnsError{Table: "t"},
		&ColumnNotFoundError{Table: "t", Column: "c"},
		&UnsupportedColumnTypeError{Column: "c", Type: "FOO"},
		&SpatialTypeUnsupportedError{Column: "c", Type: "GEOMETRY"},
		&InvalidColumnTypeError{Column: "c", Type: "DECIMAL(x,y)", Reason: "bad"},
		&InvalidTypeStringError{Type: "???", Reason: "bad"},
		&RowColumnCountMismatchError{Table: "t", Expected: 2, Got: 1},
		&ColumnEncodeError{Table: "t", Column: "c", Err: cause},
		&ColumnDecodeError{Table: "t", Column: "c", Err: cause},
		&ValueTypeMismatchError{Column: "c", Expected: "int32", Got: "string"},
		&TruncatedValueError{Width: 4},
		&TruncatedRowError{Table: "t"},
		&InvalidRowMagicError{Table: "t"},
		&UnsupportedRowFormatError{Table: "t", Version: 9},
		&DecimalParseError{Value: "abc"},
		&DefaultValueConvertError{Column: "c", Reason: "bad"},
		&JSONEncodeError{Reason: "bad"},
		&JSONDecodeError{Reason: "bad"},
		&UnsupportedKeyColumnTypeError{Table: "t", Column: "c", Type: "DECIMAL"},
		&PrefixLengthNotSupportedError{Table: "t", Column: "c", Type: "INT"},
		&NullKeyComponentError{Column: "c"},
		&MissingRowIDError{Table: "t"},
		&KeyValueCountMismatchError{Table: "t", Expected: 1, Got: 2},
		&InvalidKeyEncodingError{Table: "t", Reason: "bad"},
		&TruncatedKeyComponentError{Table: "t", Column: "c"},
	}

	for _, err := range cases {
		t.Run(err.(interface{ Error() string }).Error(), func(t *testing.T) {
			if err.Error() == "" {
				t.Fatalf("%T.Error() returned an empty string", err)
			}
		})
	}
}

// TestValueTypeMismatchError_ColumnClause pins the two renderings of the
// message. The row codec's per-type encoders are shared across columns and so
// leave Column empty, which used to render as "value for  has Go type" -- a
// message with a hole in it where the reader looks for the column name, on
// the one error a Go-type change in the engine makes every caller read.
func TestValueTypeMismatchError_ColumnClause(t *testing.T) {
	cases := []struct {
		name string
		err  *ValueTypeMismatchError
		want string
	}{
		{
			name: "with column",
			err:  &ValueTypeMismatchError{Column: "amt", Expected: "*apd.Decimal", Got: "string"},
			want: "catalog: value for amt has Go type string, expected *apd.Decimal",
		},
		{
			name: "without column",
			err:  &ValueTypeMismatchError{Expected: "*apd.Decimal", Got: "string"},
			want: "catalog: value has Go type string, expected *apd.Decimal",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Fatalf("Error() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestColumnEncodeError_Unwrap(t *testing.T) {
	cause := errors.New("cause")
	err := &ColumnEncodeError{Table: "t", Column: "c", Err: cause}
	if !errors.Is(err, cause) {
		t.Fatal("expected errors.Is to find the wrapped cause")
	}
}

func TestColumnDecodeError_Unwrap(t *testing.T) {
	cause := errors.New("cause")
	err := &ColumnDecodeError{Table: "t", Column: "c", Err: cause}
	if !errors.Is(err, cause) {
		t.Fatal("expected errors.Is to find the wrapped cause")
	}
}
