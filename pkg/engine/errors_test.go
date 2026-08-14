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

package engine

import (
	"errors"
	"testing"
)

func TestNotOpenError_Error(t *testing.T) {
	err := &NotOpenError{}
	if got := err.Error(); got != "engine: not open" {
		t.Errorf("Error() = %q, want %q", got, "engine: not open")
	}
}

func TestNotOpenError_ErrorsAs(t *testing.T) {
	var target *NotOpenError
	err := error(&NotOpenError{})
	if !errors.As(err, &target) {
		t.Error("errors.As should match *NotOpenError")
	}
}

func TestNotOpenError_ErrorsIs(t *testing.T) {
	err := error(&NotOpenError{})
	var otherErr *UnsupportedOperationError
	if errors.As(err, &otherErr) {
		t.Error("errors.As should not match *UnsupportedOperationError for *NotOpenError")
	}
}

func TestUnsupportedOperationError_Error(t *testing.T) {
	err := &UnsupportedOperationError{Op: 0xab}
	if got := err.Error(); got != "engine: unsupported operation: 0xab" {
		t.Errorf("Error() = %q, want %q", got, "engine: unsupported operation: 0xab")
	}
}

func TestUnsupportedOperationError_ErrorZero(t *testing.T) {
	err := &UnsupportedOperationError{Op: 0x00}
	if got := err.Error(); got != "engine: unsupported operation: 0x00" {
		t.Errorf("Error() = %q, want %q", got, "engine: unsupported operation: 0x00")
	}
}

func TestUnsupportedOperationError_ErrorsAs(t *testing.T) {
	var target *UnsupportedOperationError
	err := error(&UnsupportedOperationError{Op: 0xff})
	if !errors.As(err, &target) {
		t.Error("errors.As should match *UnsupportedOperationError")
	}
	if target.Op != 0xff {
		t.Errorf("Op = 0x%02x, want 0xff", target.Op)
	}
}

func TestUnsupportedOperationError_ErrorsIs(t *testing.T) {
	err := error(&UnsupportedOperationError{Op: 0x42})
	var otherErr *NotOpenError
	if errors.As(err, &otherErr) {
		t.Error("errors.As should not match *NotOpenError for *UnsupportedOperationError")
	}
}

func TestInvalidQueryError_Error(t *testing.T) {
	err := &InvalidQueryError{Reason: "bad type"}
	if got := err.Error(); got != "engine: invalid query: bad type" {
		t.Errorf("Error() = %q, want %q", got, "engine: invalid query: bad type")
	}
}

func TestInvalidQueryError_ErrorEmpty(t *testing.T) {
	err := &InvalidQueryError{Reason: ""}
	if got := err.Error(); got != "engine: invalid query: " {
		t.Errorf("Error() = %q, want %q", got, "engine: invalid query: ")
	}
}

func TestInvalidQueryError_ErrorsAs(t *testing.T) {
	var target *InvalidQueryError
	err := error(&InvalidQueryError{Reason: "unsupported query type"})
	if !errors.As(err, &target) {
		t.Error("errors.As should match *InvalidQueryError")
	}
	if target.Reason != "unsupported query type" {
		t.Errorf("Reason = %q, want %q", target.Reason, "unsupported query type")
	}
}

func TestInvalidQueryError_ErrorsIs(t *testing.T) {
	err := error(&InvalidQueryError{Reason: "test"})
	var otherErr *UnsupportedOperationError
	if errors.As(err, &otherErr) {
		t.Error("errors.As should not match *UnsupportedOperationError for *InvalidQueryError")
	}
}

func TestInvalidQueryError_UnsupportedQueryType_True(t *testing.T) {
	err := &InvalidQueryError{Reason: "unsupported query type"}
	if !err.UnsupportedQueryType() {
		t.Error("UnsupportedQueryType() = false, want true for the Lookup switch fallback reason")
	}
}

func TestInvalidQueryError_UnsupportedQueryType_False(t *testing.T) {
	tests := []string{
		"",
		"index not found: idx1",
		"compound index not found: idx1",
		"lessor not configured",
		"lease not found",
		"time-series storage not configured",
	}
	for _, reason := range tests {
		err := &InvalidQueryError{Reason: reason}
		if err.UnsupportedQueryType() {
			t.Errorf("UnsupportedQueryType() = true for reason %q, want false", reason)
		}
	}
}

func TestHexByte(t *testing.T) {
	tests := []struct {
		name string
		b    byte
		want string
	}{
		{name: "zero", b: 0x00, want: "00"},
		{name: "low", b: 0x0a, want: "0a"},
		{name: "mid", b: 0x42, want: "42"},
		{name: "high", b: 0xff, want: "ff"},
		{name: "upper_nibble", b: 0xf0, want: "f0"},
		{name: "lower_nibble", b: 0x0f, want: "0f"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hexByte(tt.b); got != tt.want {
				t.Errorf("hexByte(0x%02x) = %q, want %q", tt.b, got, tt.want)
			}
		})
	}
}

func TestHexByte_Boundary(t *testing.T) {
	if got := hexByte(0x9a); got != "9a" {
		t.Errorf("hexByte(0x9a) = %q, want %q", got, "9a")
	}
}
