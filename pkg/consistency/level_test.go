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

package consistency

import (
	"strings"
	"testing"
)

func TestLevelString(t *testing.T) {
	tests := []struct {
		name     string
		level    Level
		expected string
	}{
		{
			name:     "strong level",
			level:    Strong,
			expected: "strong",
		},
		{
			name:     "linearizable level",
			level:    Linearizable,
			expected: "linearizable",
		},
		{
			name:     "eventual level",
			level:    Eventual,
			expected: "eventual",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.level.String()
			if got != tt.expected {
				t.Errorf("Level(%d).String() = %q, want %q", tt.level, got, tt.expected)
			}
		})
	}
}

func TestLevelStringUnknown(t *testing.T) {
	tests := []struct {
		name  string
		level Level
	}{
		{
			name:  "negative level",
			level: Level(-1),
		},
		{
			name:  "out of range level",
			level: Level(99),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.level.String()
			if !strings.HasPrefix(got, "unknown(") {
				t.Errorf("Level(%d).String() = %q, want prefix %q", tt.level, got, "unknown(")
			}
			if !strings.HasSuffix(got, ")") {
				t.Errorf("Level(%d).String() = %q, want suffix %q", tt.level, got, ")")
			}
		})
	}
}

func TestParseLevelValid(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected Level
	}{
		{
			name:     "strong lowercase",
			input:    "strong",
			expected: Strong,
		},
		{
			name:     "strong uppercase",
			input:    "STRONG",
			expected: Strong,
		},
		{
			name:     "linearizable lowercase",
			input:    "linearizable",
			expected: Linearizable,
		},
		{
			name:     "linearizable uppercase",
			input:    "LINEARIZABLE",
			expected: Linearizable,
		},
		{
			name:     "eventual lowercase",
			input:    "eventual",
			expected: Eventual,
		},
		{
			name:     "eventual uppercase",
			input:    "EVENTUAL",
			expected: Eventual,
		},
		{
			name:     "stale lowercase alias",
			input:    "stale",
			expected: Eventual,
		},
		{
			name:     "stale uppercase alias",
			input:    "STALE",
			expected: Eventual,
		},
		{
			name:     "empty string defaults to linearizable",
			input:    "",
			expected: Linearizable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseLevel(tt.input)
			if err != nil {
				t.Fatalf("ParseLevel(%q) returned unexpected error: %v", tt.input, err)
			}
			if got != tt.expected {
				t.Errorf("ParseLevel(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestParseLevelInvalid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "unknown string",
			input: "unknown",
		},
		{
			name:  "garbage input",
			input: "foobar",
		},
		{
			name:  "mixed case not matched",
			input: "Strong",
		},
		{
			name:  "whitespace only",
			input: " ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseLevel(tt.input)
			if err == nil {
				t.Fatalf("ParseLevel(%q) expected error, got nil", tt.input)
			}
			var unknownErr *UnknownLevelError
			ok := false
			if ule, castOk := err.(*UnknownLevelError); castOk {
				unknownErr = ule
				ok = true
			}
			if !ok {
				t.Fatalf("ParseLevel(%q) error type = %T, want *UnknownLevelError", tt.input, err)
			}
			if unknownErr.Level != tt.input {
				t.Errorf("UnknownLevelError.Level = %q, want %q", unknownErr.Level, tt.input)
			}
		})
	}
}

func TestIsValidTrue(t *testing.T) {
	tests := []struct {
		name  string
		level Level
	}{
		{
			name:  "strong is valid",
			level: Strong,
		},
		{
			name:  "linearizable is valid",
			level: Linearizable,
		},
		{
			name:  "eventual is valid",
			level: Eventual,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.level.IsValid() {
				t.Errorf("Level(%d).IsValid() = false, want true", tt.level)
			}
		})
	}
}

func TestIsValidFalse(t *testing.T) {
	tests := []struct {
		name  string
		level Level
	}{
		{
			name:  "negative level is invalid",
			level: Level(-1),
		},
		{
			name:  "out of range level is invalid",
			level: Level(3),
		},
		{
			name:  "large out of range level is invalid",
			level: Level(100),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.level.IsValid() {
				t.Errorf("Level(%d).IsValid() = true, want false", tt.level)
			}
		})
	}
}

func TestDefaultReturnsLinearizable(t *testing.T) {
	got := Default()
	if got != Linearizable {
		t.Errorf("Default() = %v, want %v", got, Linearizable)
	}
}

func TestDefaultIsValid(t *testing.T) {
	got := Default()
	if !got.IsValid() {
		t.Errorf("Default().IsValid() = false, want true")
	}
}
