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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEscapeBackslashesInStringLiterals covers the dialect gap that made a
// backslash inside a PostgreSQL string literal mean something else by the
// time go-mysql-server's MySQL lexer read it.
//
// PostgreSQL (standard_conforming_strings on, the default since 9.1) treats a
// backslash in '...' as an ordinary character. MySQL treats it as an escape
// introducer, and go-mysql-server does not honour the NO_BACKSLASH_ESCAPES
// sql_mode that would turn that off -- verified directly against the engine.
// So the rewrite happens here, in the text, where the two dialects meet.
func TestEscapeBackslashesInStringLiterals(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  string
	}{
		{
			name:  "a query with no backslash is returned unchanged",
			query: `SELECT * FROM t WHERE name = 'plain'`,
			want:  `SELECT * FROM t WHERE name = 'plain'`,
		},
		{
			name:  "a Windows path keeps its separators",
			query: `SELECT * FROM t WHERE p = 'C:\temp'`,
			want:  `SELECT * FROM t WHERE p = 'C:\\temp'`,
		},
		{
			// The injection shape: a value ending in a backslash would
			// otherwise escape the closing quote and let everything after
			// it be read as SQL.
			name:  "a trailing backslash cannot escape its own closing quote",
			query: `SELECT * FROM t WHERE a = 'x\' AND b = 'safe'`,
			want:  `SELECT * FROM t WHERE a = 'x\\' AND b = 'safe'`,
		},
		{
			name:  "every literal in a statement is rewritten",
			query: `INSERT INTO t (a, b) VALUES ('x\1', 'y\2')`,
			want:  `INSERT INTO t (a, b) VALUES ('x\\1', 'y\\2')`,
		},
		{
			name:  "a doubled quote inside a literal is preserved",
			query: `SELECT 'it''s a \path'`,
			want:  `SELECT 'it''s a \\path'`,
		},
		{
			// PostgreSQL's escape-string syntax processes backslash escapes
			// exactly as MySQL does, so its text is already in the target
			// dialect and doubling would corrupt it: E'\n' is a newline in
			// both, E'\\n' is a literal backslash-n in both.
			name:  "an E-string is left alone",
			query: `SELECT E'line\nbreak'`,
			want:  `SELECT E'line\nbreak'`,
		},
		{
			name:  "an E-string and a plain literal are treated differently in one statement",
			query: `SELECT E'a\n', 'b\n'`,
			want:  `SELECT E'a\n', 'b\\n'`,
		},
		{
			name:  "a backslash outside any literal is untouched",
			query: `SELECT a \ b FROM t`,
			want:  `SELECT a \ b FROM t`,
		},
		{
			name:  "a comment is not a literal",
			query: `SELECT 1 -- a windows path C:\temp`,
			want:  `SELECT 1 -- a windows path C:\temp`,
		},
		{
			// Dollar-quoted text has no escape processing in either dialect.
			name:  "a dollar-quoted string is left alone",
			query: `SELECT $$raw \n text$$`,
			want:  `SELECT $$raw \n text$$`,
		},
		{
			name:  "an already-doubled backslash is doubled again, as it must be",
			query: `SELECT 'a\\b'`,
			want:  `SELECT 'a\\\\b'`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, escapeBackslashesInStringLiterals(tt.query))
		})
	}
}

// TestTranslateEscapesBackslashLiterals pins the rewrite through the whole
// Translate pipeline, which is where ordering matters: it has to run before
// applyEscapeStrings collapses E'...' into '...', because after that the two
// kinds cannot be told apart and an E-string's escapes would be doubled.
func TestTranslateEscapesBackslashLiterals(t *testing.T) {
	translator := NewTranslator()

	tests := []struct {
		name  string
		query string
		want  string
	}{
		{
			name:  "a plain literal is escaped for the MySQL lexer",
			query: `SELECT * FROM "t" WHERE "p" = 'C:\temp'`,
			want:  "SELECT * FROM `t` WHERE `p` = 'C:\\\\temp'",
		},
		{
			name:  "an E-string is normalised without being doubled",
			query: `SELECT E'line\nbreak'`,
			want:  `SELECT 'line\nbreak'`,
		},
		{
			name:  "a parameterised statement is unaffected",
			query: `SELECT * FROM "t" WHERE "p" = $1`,
			want:  "SELECT * FROM `t` WHERE `p` = ?",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := translator.Translate(tt.query)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestTranslateUnquotesRowCounts covers PostgreSQL's coercion of an
// unknown-typed literal in a row-count position, which MySQL does not have.
//
// pgx renders every bind parameter as a quoted literal when it interpolates a
// simple-protocol query, and gorm issues LIMIT $1 on every First/Take/Last, so
// LIMIT '1' is what a perfectly ordinary GORM query looks like by the time it
// reaches the engine.
func TestTranslateUnquotesRowCounts(t *testing.T) {
	translator := NewTranslator()

	tests := []struct {
		name  string
		query string
		want  string
	}{
		{
			name:  "a quoted LIMIT is unquoted",
			query: `SELECT * FROM "t" LIMIT '1'`,
			want:  "SELECT * FROM `t` LIMIT 1",
		},
		{
			name:  "a quoted OFFSET is unquoted",
			query: `SELECT * FROM "t" LIMIT '10' OFFSET '20'`,
			want:  "SELECT * FROM `t` LIMIT 10 OFFSET 20",
		},
		{
			name:  "an unquoted row count is untouched",
			query: `SELECT * FROM "t" LIMIT 5`,
			want:  "SELECT * FROM `t` LIMIT 5",
		},
		{
			// PostgreSQL rejects this too, so the engine's own error is the
			// honest answer rather than a guess at what was meant.
			name:  "a non-numeric quoted row count is left for the engine to reject",
			query: `SELECT * FROM "t" LIMIT 'abc'`,
			want:  "SELECT * FROM `t` LIMIT 'abc'",
		},
		{
			name:  "the word LIMIT inside a literal is not a row count",
			query: `SELECT * FROM "t" WHERE note = 'LIMIT ''1'''`,
			want:  "SELECT * FROM `t` WHERE note = 'LIMIT ''1'''",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := translator.Translate(tt.query)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
