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
	"database/sql/driver"
	"errors"
	"testing"
	"time"
)

// TestSubstituteArgs_NoArgs verifies query is returned unchanged when no args.
func TestSubstituteArgs_NoArgs(t *testing.T) {
	query := "SELECT * FROM users"
	got, err := substituteArgs(query, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != query {
		t.Errorf("got %q, want %q", got, query)
	}
}

// TestSubstituteArgs_NoArgsEmpty verifies empty args slice is treated as no args.
func TestSubstituteArgs_NoArgsEmpty(t *testing.T) {
	query := "SELECT * FROM users"
	got, err := substituteArgs(query, []driver.NamedValue{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != query {
		t.Errorf("got %q, want %q", got, query)
	}
}

// TestSubstituteArgs_Types verifies correct substitution for each supported type.
func TestSubstituteArgs_Types(t *testing.T) {
	now := time.Date(2024, 1, 15, 10, 30, 45, 0, time.UTC)

	tests := []struct {
		name  string
		query string
		args  []driver.NamedValue
		want  string
	}{
		{
			name:  "string arg",
			query: "SELECT * FROM t WHERE name = ?",
			args:  []driver.NamedValue{{Ordinal: 1, Value: "alice"}},
			want:  "SELECT * FROM t WHERE name = 'alice'",
		},
		{
			name:  "int64 arg",
			query: "SELECT * FROM t WHERE id = ?",
			args:  []driver.NamedValue{{Ordinal: 1, Value: int64(42)}},
			want:  "SELECT * FROM t WHERE id = 42",
		},
		{
			name:  "float64 arg",
			query: "SELECT * FROM t WHERE score > ?",
			args:  []driver.NamedValue{{Ordinal: 1, Value: float64(3.14)}},
			want:  "SELECT * FROM t WHERE score > 3.14",
		},
		{
			name:  "bool true",
			query: "SELECT * FROM t WHERE active = ?",
			args:  []driver.NamedValue{{Ordinal: 1, Value: true}},
			want:  "SELECT * FROM t WHERE active = TRUE",
		},
		{
			name:  "bool false",
			query: "SELECT * FROM t WHERE deleted = ?",
			args:  []driver.NamedValue{{Ordinal: 1, Value: false}},
			want:  "SELECT * FROM t WHERE deleted = FALSE",
		},
		{
			name:  "nil arg",
			query: "SELECT * FROM t WHERE val = ?",
			args:  []driver.NamedValue{{Ordinal: 1, Value: nil}},
			want:  "SELECT * FROM t WHERE val = NULL",
		},
		{
			name:  "bytes arg",
			query: "INSERT INTO t VALUES (?)",
			args:  []driver.NamedValue{{Ordinal: 1, Value: []byte("hello")}},
			want:  "INSERT INTO t VALUES ('hello')",
		},
		{
			name:  "time arg",
			query: "SELECT * FROM t WHERE created > ?",
			args:  []driver.NamedValue{{Ordinal: 1, Value: now}},
			want:  "SELECT * FROM t WHERE created > '2024-01-15 10:30:45'",
		},
		{
			name:  "multiple args",
			query: "INSERT INTO t (a, b) VALUES (?, ?)",
			args: []driver.NamedValue{
				{Ordinal: 1, Value: "foo"},
				{Ordinal: 2, Value: int64(99)},
			},
			want: "INSERT INTO t (a, b) VALUES ('foo', 99)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := substituteArgs(tt.query, tt.args)
			if err != nil {
				t.Fatalf("substituteArgs() unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSubstituteArgs_QuotedStringShouldNotSubstitute verifies ? inside single quotes is skipped.
func TestSubstituteArgs_QuotedStringShouldNotSubstitute(t *testing.T) {
	query := "SELECT '?' FROM t WHERE id = ?"
	args := []driver.NamedValue{{Ordinal: 1, Value: int64(1)}}
	got, err := substituteArgs(query, args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "SELECT '?' FROM t WHERE id = 1"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestSubstituteArgs_TooFewArgs verifies error when not enough args are provided.
func TestSubstituteArgs_TooFewArgs(t *testing.T) {
	query := "INSERT INTO t VALUES (?, ?)"
	args := []driver.NamedValue{{Ordinal: 1, Value: "only one"}}
	_, err := substituteArgs(query, args)
	if err == nil {
		t.Fatal("expected error for too few args")
	}
	var subErr *ArgumentSubstitutionError
	if !errors.As(err, &subErr) {
		t.Errorf("expected *ArgumentSubstitutionError, got %T: %v", err, err)
	}
}

// TestSubstituteArgs_StringWithSingleQuote verifies escaping of single quotes.
func TestSubstituteArgs_StringWithSingleQuote(t *testing.T) {
	query := "INSERT INTO t (name) VALUES (?)"
	args := []driver.NamedValue{{Ordinal: 1, Value: "O'Brien"}}
	got, err := substituteArgs(query, args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "INSERT INTO t (name) VALUES ('O''Brien')"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestFormatArg_AllTypes verifies formatArg handles every documented type.
func TestFormatArg_AllTypes(t *testing.T) {
	now := time.Date(2025, 6, 20, 8, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		value interface{}
		want  string
	}{
		{name: "nil", value: nil, want: "NULL"},
		{name: "int64 zero", value: int64(0), want: "0"},
		{name: "int64 positive", value: int64(100), want: "100"},
		{name: "int64 negative", value: int64(-5), want: "-5"},
		{name: "float64", value: float64(1.5), want: "1.5"},
		{name: "float64 whole", value: float64(10.0), want: "10"},
		{name: "bool true", value: true, want: "TRUE"},
		{name: "bool false", value: false, want: "FALSE"},
		{name: "string", value: "hello", want: "'hello'"},
		{name: "string with quote", value: "it's", want: "'it''s'"},
		{name: "bytes", value: []byte("data"), want: "'data'"},
		{name: "time", value: now, want: "'2025-06-20 08:00:00'"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := formatArg(tt.value)
			if err != nil {
				t.Fatalf("formatArg() unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("formatArg(%v) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

// TestFormatArg_DefaultType verifies the default fallback for unsupported types.
func TestFormatArg_DefaultType(t *testing.T) {
	type customType struct{ val int }
	c := customType{val: 42}
	got, err := formatArg(c)
	if err != nil {
		t.Fatalf("formatArg() unexpected error: %v", err)
	}
	// The default path wraps the value in single quotes with %v formatting.
	if len(got) < 2 || got[0] != '\'' || got[len(got)-1] != '\'' {
		t.Errorf("formatArg(customType) = %q, expected single-quoted string", got)
	}
}

// TestEscapeSQL_NoQuotes verifies no-op when string has no quotes.
func TestEscapeSQL_NoQuotes(t *testing.T) {
	s := "hello world"
	got := escapeSQL(s)
	if got != s {
		t.Errorf("escapeSQL(%q) = %q, want %q", s, got, s)
	}
}

// TestEscapeSQL_WithQuotes verifies escaping of single quotes.
func TestEscapeSQL_WithQuotes(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "it's", want: "it''s"},
		{input: "'quoted'", want: "''quoted''"},
		{input: "no quotes", want: "no quotes"},
		{input: "double''escaped", want: "double''''escaped"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := escapeSQL(tt.input)
			if got != tt.want {
				t.Errorf("escapeSQL(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestEscapeSQL_WithBackslash verifies backslashes are doubled so they cannot
// escape the surrounding single-quoted literal (SQL-injection guard).
func TestEscapeSQL_WithBackslash(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: `a\b`, want: `a\\b`},
		{input: `trailing\`, want: `trailing\\`},
		{input: `\'`, want: `\\''`},
		{input: `c:\path\to`, want: `c:\\path\\to`},
		{input: "no escapes", want: "no escapes"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := escapeSQL(tt.input)
			if got != tt.want {
				t.Errorf("escapeSQL(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestSubstituteArgs_StringWithBackslash proves a trailing backslash in an
// argument cannot terminate the quoted literal early and break out of it.
func TestSubstituteArgs_StringWithBackslash(t *testing.T) {
	query := "INSERT INTO t (name) VALUES (?)"
	args := []driver.NamedValue{{Ordinal: 1, Value: `evil\`}}
	got, err := substituteArgs(query, args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `INSERT INTO t (name) VALUES ('evil\\')`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestCountPlaceholders verifies placeholder counting.
func TestCountPlaceholders(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  int
	}{
		{name: "no placeholders", query: "SELECT 1", want: 0},
		{name: "one placeholder", query: "SELECT * FROM t WHERE id = ?", want: 1},
		{name: "two placeholders", query: "INSERT INTO t VALUES (?, ?)", want: 2},
		{name: "placeholder in quoted string", query: "SELECT '?' FROM t", want: 0},
		{name: "mixed quoted and unquoted", query: "SELECT '?' FROM t WHERE id = ?", want: 1},
		{name: "multiple quoted strings", query: "SELECT '?', '?' FROM t WHERE a=? AND b=?", want: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := countPlaceholders(tt.query)
			if got != tt.want {
				t.Errorf("countPlaceholders(%q) = %d, want %d", tt.query, got, tt.want)
			}
		})
	}
}
