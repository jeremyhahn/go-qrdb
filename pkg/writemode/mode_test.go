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

package writemode

import (
	"errors"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

func Test_Constants_iotaValues(t *testing.T) {
	tests := []struct {
		name string
		mode Mode
		want uint8
	}{
		{name: "Sync is 0", mode: Sync, want: 0},
		{name: "WALDurable is 1", mode: WALDurable, want: 1},
		{name: "Batched is 2", mode: Batched, want: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if uint8(tt.mode) != tt.want {
				t.Errorf("%s = %d, want %d", tt.name, uint8(tt.mode), tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// FromString - valid inputs
// ---------------------------------------------------------------------------

func Test_FromString_validModes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  Mode
	}{
		{name: "empty defaults to sync", input: "", want: Sync},
		{name: "sync", input: "sync", want: Sync},
		{name: "wal_durable canonical", input: "wal_durable", want: WALDurable},
		{name: "wal alias", input: "wal", want: WALDurable},
		{name: "async alias", input: "async", want: WALDurable},
		{name: "batched canonical", input: "batched", want: Batched},
		{name: "batch alias", input: "batch", want: Batched},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FromString(tt.input)
			if err != nil {
				t.Fatalf("FromString(%q) returned unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("FromString(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// FromString - invalid inputs
// ---------------------------------------------------------------------------

func Test_FromString_invalidReturnsInvalidModeError(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "unknown string", input: "turbo"},
		{name: "uppercase sync", input: "SYNC"},
		{name: "mixed case batched", input: "Batched"},
		{name: "numeric zero", input: "0"},
		{name: "leading whitespace", input: " sync"},
		{name: "trailing whitespace", input: "sync "},
		{name: "hyphenated variant", input: "wal-durable"},
		{name: "special characters", input: "!@#$"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := FromString(tt.input)
			if err == nil {
				t.Fatalf("FromString(%q) expected error, got nil", tt.input)
			}
			var target *InvalidModeError
			if !errors.As(err, &target) {
				t.Fatalf("FromString(%q) error type = %T, want *InvalidModeError", tt.input, err)
			}
			if target.Value != tt.input {
				t.Errorf("InvalidModeError.Value = %q, want %q", target.Value, tt.input)
			}
		})
	}
}

func Test_FromString_invalidReturnsSyncAsZeroValue(t *testing.T) {
	got, err := FromString("garbage")
	if err == nil {
		t.Fatal("FromString(\"garbage\") expected error, got nil")
	}
	if got != Sync {
		t.Errorf("FromString(\"garbage\") returned mode %v on error, want Sync (%v)", got, Sync)
	}
}

// ---------------------------------------------------------------------------
// String
// ---------------------------------------------------------------------------

func Test_String_canonicalNames(t *testing.T) {
	tests := []struct {
		name string
		mode Mode
		want string
	}{
		{name: "sync", mode: Sync, want: "sync"},
		{name: "wal_durable", mode: WALDurable, want: "wal_durable"},
		{name: "batched", mode: Batched, want: "batched"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.mode.String(); got != tt.want {
				t.Errorf("Mode(%d).String() = %q, want %q", tt.mode, got, tt.want)
			}
		})
	}
}

func Test_String_unknownMode(t *testing.T) {
	tests := []struct {
		name string
		mode Mode
	}{
		{name: "value 3", mode: Mode(3)},
		{name: "value 255", mode: Mode(255)},
		{name: "value 128", mode: Mode(128)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.mode.String()
			if !strings.HasPrefix(got, "unknown(") {
				t.Errorf("Mode(%d).String() = %q, want prefix %q", tt.mode, got, "unknown(")
			}
			if !strings.HasSuffix(got, ")") {
				t.Errorf("Mode(%d).String() = %q, want suffix %q", tt.mode, got, ")")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Validate
// ---------------------------------------------------------------------------

func Test_Validate_validModes(t *testing.T) {
	tests := []struct {
		name string
		mode Mode
	}{
		{name: "sync", mode: Sync},
		{name: "wal_durable", mode: WALDurable},
		{name: "batched", mode: Batched},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.mode.Validate(); err != nil {
				t.Errorf("Mode(%d).Validate() returned unexpected error: %v", tt.mode, err)
			}
		})
	}
}

func Test_Validate_invalidModes(t *testing.T) {
	tests := []struct {
		name string
		mode Mode
	}{
		{name: "just above range", mode: Mode(3)},
		{name: "max uint8", mode: Mode(255)},
		{name: "mid range", mode: Mode(100)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.mode.Validate()
			if err == nil {
				t.Fatalf("Mode(%d).Validate() expected error, got nil", tt.mode)
			}
			var target *InvalidModeError
			if !errors.As(err, &target) {
				t.Fatalf("Mode(%d).Validate() error type = %T, want *InvalidModeError", tt.mode, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// IsSynchronous
// ---------------------------------------------------------------------------

func Test_IsSynchronous_validModes(t *testing.T) {
	tests := []struct {
		name string
		mode Mode
		want bool
	}{
		{name: "sync is synchronous", mode: Sync, want: true},
		{name: "wal_durable is not synchronous", mode: WALDurable, want: false},
		{name: "batched is synchronous", mode: Batched, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.mode.IsSynchronous(); got != tt.want {
				t.Errorf("Mode(%d).IsSynchronous() = %v, want %v", tt.mode, got, tt.want)
			}
		})
	}
}

func Test_IsSynchronous_outOfRange(t *testing.T) {
	m := Mode(255)
	got := m.IsSynchronous()
	if got {
		t.Errorf("Mode(255).IsSynchronous() = true, want false")
	}
}

// ---------------------------------------------------------------------------
// IsAsync
// ---------------------------------------------------------------------------

func Test_IsAsync_validModes(t *testing.T) {
	tests := []struct {
		name string
		mode Mode
		want bool
	}{
		{name: "sync is not async", mode: Sync, want: false},
		{name: "wal_durable is async", mode: WALDurable, want: true},
		{name: "batched is not async", mode: Batched, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.mode.IsAsync(); got != tt.want {
				t.Errorf("Mode(%d).IsAsync() = %v, want %v", tt.mode, got, tt.want)
			}
		})
	}
}

func Test_IsAsync_outOfRange(t *testing.T) {
	m := Mode(255)
	got := m.IsAsync()
	if got {
		t.Errorf("Mode(255).IsAsync() = true, want false")
	}
}

// ---------------------------------------------------------------------------
// RequiresQuorum
// ---------------------------------------------------------------------------

func Test_RequiresQuorum_validModes(t *testing.T) {
	tests := []struct {
		name string
		mode Mode
		want bool
	}{
		{name: "sync requires quorum", mode: Sync, want: true},
		{name: "wal_durable does not require quorum", mode: WALDurable, want: false},
		{name: "batched requires quorum", mode: Batched, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.mode.RequiresQuorum(); got != tt.want {
				t.Errorf("Mode(%d).RequiresQuorum() = %v, want %v", tt.mode, got, tt.want)
			}
		})
	}
}

func Test_RequiresQuorum_outOfRange(t *testing.T) {
	m := Mode(255)
	got := m.RequiresQuorum()
	if got {
		t.Errorf("Mode(255).RequiresQuorum() = true, want false")
	}
}

// ---------------------------------------------------------------------------
// Round-trip: String -> FromString -> original
// ---------------------------------------------------------------------------

func Test_FromString_roundTrip(t *testing.T) {
	modes := []Mode{Sync, WALDurable, Batched}
	for _, m := range modes {
		t.Run(m.String(), func(t *testing.T) {
			parsed, err := FromString(m.String())
			if err != nil {
				t.Fatalf("FromString(%q) returned unexpected error: %v", m.String(), err)
			}
			if parsed != m {
				t.Errorf("round-trip failed: %v -> %q -> %v", m, m.String(), parsed)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// InvalidModeError
// ---------------------------------------------------------------------------

func Test_InvalidModeError_errorMessage(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{
			name:  "unknown string value",
			value: "turbo",
			want:  `quicraft/writemode: invalid write mode: "turbo"`,
		},
		{
			name:  "numeric overflow value",
			value: "99",
			want:  `quicraft/writemode: invalid write mode: "99"`,
		},
		{
			name:  "empty value",
			value: "",
			want:  `quicraft/writemode: invalid write mode: ""`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &InvalidModeError{Value: tt.value}
			if got := err.Error(); got != tt.want {
				t.Errorf("InvalidModeError{Value: %q}.Error() = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func Test_InvalidModeError_fromValidateContainsNumericValue(t *testing.T) {
	m := Mode(42)
	err := m.Validate()
	if err == nil {
		t.Fatal("Mode(42).Validate() expected error, got nil")
	}
	got := err.Error()
	if !strings.Contains(got, "42") {
		t.Errorf("error message %q does not contain numeric value %q", got, "42")
	}
}
