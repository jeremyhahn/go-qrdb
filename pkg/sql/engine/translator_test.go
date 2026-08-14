// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
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

func TestNewTranslator(t *testing.T) {
	tr := NewTranslator()
	assert.NotNil(t, tr)
}

func TestTranslator_Translate_StandardSQLPassthrough(t *testing.T) {
	tr := NewTranslator()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "simple_select", input: "SELECT * FROM users", expected: "SELECT * FROM users"},
		{name: "insert", input: "INSERT INTO users (name, age) VALUES ('alice', 30)", expected: "INSERT INTO users (`name`, age) VALUES ('alice', 30)"},
		{name: "update", input: "UPDATE users SET age = 31 WHERE name = 'alice'", expected: "UPDATE users SET age = 31 WHERE `name` = 'alice'"},
		{name: "delete", input: "DELETE FROM users WHERE id = 1", expected: "DELETE FROM users WHERE id = 1"},
		{name: "create_table", input: "CREATE TABLE test (id INT PRIMARY KEY, name VARCHAR(255))", expected: "CREATE TABLE test (id INT PRIMARY KEY, `name` VARCHAR(255))"},
		{name: "empty_query", input: "", expected: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tr.Translate(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestTranslator_Translate_DollarParams(t *testing.T) {
	tr := NewTranslator()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "single_param",
			input:    "SELECT * FROM users WHERE id = $1",
			expected: "SELECT * FROM users WHERE id = ?",
		},
		{
			name:     "multiple_params",
			input:    "INSERT INTO users (name, age) VALUES ($1, $2)",
			expected: "INSERT INTO users (`name`, age) VALUES (?, ?)",
		},
		{
			name:     "high_numbered_param",
			input:    "SELECT * FROM t WHERE a = $10 AND b = $99",
			expected: "SELECT * FROM t WHERE a = ? AND b = ?",
		},
		{
			name:     "no_params",
			input:    "SELECT * FROM users",
			expected: "SELECT * FROM users",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tr.Translate(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestTranslator_Translate_CastSyntax(t *testing.T) {
	tr := NewTranslator()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "identifier_cast",
			input:    "SELECT id::integer FROM users",
			expected: "SELECT CAST(id AS integer) FROM users",
		},
		{
			name:     "string_literal_cast",
			input:    "SELECT '2024-01-01'::date FROM dual",
			expected: "SELECT CAST('2024-01-01' AS date) FROM dual",
		},
		{
			name:     "numeric_literal_cast",
			input:    "SELECT 42::text FROM dual",
			expected: "SELECT CAST(42 AS text) FROM dual",
		},
		{
			name:     "decimal_literal_cast",
			input:    "SELECT 3.14::numeric FROM dual",
			expected: "SELECT CAST(3.14 AS numeric) FROM dual",
		},
		{
			name:     "parenthesized_cast",
			input:    "SELECT (a + b)::integer FROM t",
			expected: "SELECT CAST((a + b) AS integer) FROM t",
		},
		{
			name:     "multiple_casts",
			input:    "SELECT name::text, age::integer FROM users",
			expected: "SELECT CAST(`name` AS text), CAST(age AS integer) FROM users",
		},
		{
			name:     "no_casts",
			input:    "SELECT id FROM users",
			expected: "SELECT id FROM users",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tr.Translate(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestTranslator_Translate_ILIKE(t *testing.T) {
	tr := NewTranslator()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "uppercase_ilike",
			input:    "SELECT * FROM users WHERE name ILIKE '%alice%'",
			expected: "SELECT * FROM users WHERE `name` LIKE '%alice%'",
		},
		{
			name:     "lowercase_ilike",
			input:    "SELECT * FROM users WHERE name ilike '%bob%'",
			expected: "SELECT * FROM users WHERE `name` LIKE '%bob%'",
		},
		{
			name:     "no_ilike",
			input:    "SELECT * FROM users WHERE name LIKE '%carol%'",
			expected: "SELECT * FROM users WHERE `name` LIKE '%carol%'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tr.Translate(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestTranslator_Translate_LimitAll(t *testing.T) {
	tr := NewTranslator()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "limit_all_uppercase",
			input:    "SELECT * FROM users LIMIT ALL",
			expected: "SELECT * FROM users ",
		},
		{
			name:     "limit_all_mixed_case",
			input:    "SELECT * FROM users Limit All",
			expected: "SELECT * FROM users ",
		},
		{
			name:     "limit_all_with_offset",
			input:    "SELECT * FROM users LIMIT ALL OFFSET 10",
			expected: "SELECT * FROM users  OFFSET 10",
		},
		{
			name:     "normal_limit",
			input:    "SELECT * FROM users LIMIT 10",
			expected: "SELECT * FROM users LIMIT 10",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tr.Translate(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestTranslator_Translate_DoubleQuotedIdentifiers(t *testing.T) {
	tr := NewTranslator()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "single_identifier",
			input:    `SELECT "column_name" FROM users`,
			expected: "SELECT `column_name` FROM users",
		},
		{
			name:     "multiple_identifiers",
			input:    `SELECT "first_name", "last_name" FROM "users"`,
			expected: "SELECT `first_name`, `last_name` FROM `users`",
		},
		{
			name:     "preserves_string_literals",
			input:    "SELECT * FROM users WHERE name = 'contains \"quotes\" inside'",
			expected: "SELECT * FROM users WHERE `name` = 'contains \"quotes\" inside'",
		},
		{
			name:     "mixed_quotes_and_identifiers",
			input:    `SELECT "col" FROM t WHERE name = 'has "dq" in it'`,
			expected: "SELECT `col` FROM t WHERE `name` = 'has \"dq\" in it'",
		},
		{
			name:     "no_double_quotes",
			input:    "SELECT col FROM users",
			expected: "SELECT col FROM users",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tr.Translate(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestTranslator_Translate_EscapeStrings(t *testing.T) {
	tr := NewTranslator()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple_escape_string",
			input:    "SELECT * FROM t WHERE name = E'hello'",
			expected: "SELECT * FROM t WHERE `name` = 'hello'",
		},
		{
			name:     "lowercase_e",
			input:    "SELECT * FROM t WHERE name = e'world'",
			expected: "SELECT * FROM t WHERE `name` = 'world'",
		},
		{
			name:     "escape_string_with_escaped_quotes",
			input:    "SELECT * FROM t WHERE name = E'it''s'",
			expected: "SELECT * FROM t WHERE `name` = 'it''s'",
		},
		{
			name:     "no_escape_strings",
			input:    "SELECT * FROM t WHERE name = 'normal'",
			expected: "SELECT * FROM t WHERE `name` = 'normal'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tr.Translate(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestTranslator_Translate_ReturningClause pins the current (post go-mysql-
// server bump) behavior: INSERT … RETURNING is passed through unchanged
// rather than stripped, because current vitess parses it natively and GMS
// evaluates it against the correct generated row (see
// pkg/sql/engine/returning_test.go). The old translator used to strip
// RETURNING outright, which silently discarded the clause gorm.io/driver/
// postgres depends on to populate a model's primary key after Create; this
// test used to assert that destructive behavior and has been inverted.
func TestTranslator_Translate_ReturningClause(t *testing.T) {
	tr := NewTranslator()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "returning_star",
			input:    "INSERT INTO users (name) VALUES ('alice') RETURNING *",
			expected: "INSERT INTO users (`name`) VALUES ('alice') RETURNING *",
		},
		{
			name:     "returning_specific_columns",
			input:    "INSERT INTO users (name) VALUES ('bob') RETURNING id, name",
			expected: "INSERT INTO users (`name`) VALUES ('bob') RETURNING id, `name`",
		},
		{
			name:     "no_returning",
			input:    "INSERT INTO users (name) VALUES ('carol')",
			expected: "INSERT INTO users (`name`) VALUES ('carol')",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tr.Translate(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestTranslator_Translate_UnsupportedReturning verifies that UPDATE …
// RETURNING and DELETE … RETURNING are rejected with a typed error at
// translation time, rather than silently stripped (the old behavior) or
// left to fail later with an opaque syntax error (see
// unsupportedReturningRe's doc comment for why these two forms cannot be
// parsed at all).
func TestTranslator_Translate_UnsupportedReturning(t *testing.T) {
	tr := NewTranslator()

	tests := []struct {
		name    string
		input   string
		feature string
	}{
		{
			name:    "update_with_returning",
			input:   "UPDATE users SET age = 30 WHERE id = 1 RETURNING id",
			feature: "UPDATE ... RETURNING",
		},
		{
			name:    "delete_with_returning",
			input:   "DELETE FROM users WHERE id = 1 RETURNING *",
			feature: "DELETE ... RETURNING",
		},
		{
			name:    "lowercase_update_with_returning",
			input:   "update users set age = 30 where id = 1 returning id",
			feature: "UPDATE ... RETURNING",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tr.Translate(tt.input)
			require.Error(t, err)
			assert.Empty(t, result)

			var unsupported *UnsupportedSyntaxError
			require.ErrorAs(t, err, &unsupported)
			assert.Equal(t, tt.feature, unsupported.Feature)
		})
	}
}

func TestTranslator_Translate_CombinedTransformations(t *testing.T) {
	tr := NewTranslator()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "params_and_cast",
			input:    "SELECT * FROM users WHERE id = $1::integer",
			expected: "SELECT * FROM users WHERE id = CAST(? AS integer)",
		},
		{
			name:     "ilike_with_params",
			input:    "SELECT * FROM users WHERE name ILIKE $1",
			expected: "SELECT * FROM users WHERE `name` LIKE ?",
		},
		{
			name:     "quoted_ident_with_returning",
			input:    `INSERT INTO "users" ("name") VALUES ('alice') RETURNING "id"`,
			expected: "INSERT INTO `users` (`name`) VALUES ('alice') RETURNING `id`",
		},
		{
			name:     "all_transformations",
			input:    `SELECT "col"::text FROM t WHERE name ILIKE $1 LIMIT ALL RETURNING id`,
			expected: "SELECT CAST(`col` AS text) FROM t WHERE `name` LIKE ?  RETURNING id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tr.Translate(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestTranslator_Translate_EdgeCases(t *testing.T) {
	tr := NewTranslator()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty_string",
			input:    "",
			expected: "",
		},
		{
			name:     "whitespace_only",
			input:    "   ",
			expected: "   ",
		},
		{
			name:     "escaped_single_quotes_in_string",
			input:    "SELECT * FROM t WHERE name = 'O''Brien'",
			expected: "SELECT * FROM t WHERE `name` = 'O''Brien'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tr.Translate(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestTranslator_Translate_ReservedWordQuoting(t *testing.T) {
	tr := NewTranslator()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "key_and_value_in_select",
			input:    "SELECT key, value FROM kv WHERE key = ?",
			expected: "SELECT `key`, `value` FROM kv WHERE `key` = ?",
		},
		{
			name:     "case_insensitive_KEY",
			input:    "SELECT KEY, VALUE FROM kv",
			expected: "SELECT `KEY`, `VALUE` FROM kv",
		},
		{
			name:     "mixed_case_Key",
			input:    "SELECT Key, Value FROM kv",
			expected: "SELECT `Key`, `Value` FROM kv",
		},
		{
			name:     "all_reserved_words",
			input:    "SELECT key, value, status, name, type, interval, function, condition, column, schema FROM t",
			expected: "SELECT `key`, `value`, `status`, `name`, `type`, `interval`, `function`, `condition`, `column`, `schema` FROM t",
		},
		{
			name:     "reserved_word_in_where",
			input:    "SELECT * FROM t WHERE status = 'active' AND type = 'admin'",
			expected: "SELECT * FROM t WHERE `status` = 'active' AND `type` = 'admin'",
		},
		{
			name:     "reserved_word_in_insert",
			input:    "INSERT INTO kv (key, value) VALUES ('k1', 'v1')",
			expected: "INSERT INTO kv (`key`, `value`) VALUES ('k1', 'v1')",
		},
		{
			name:     "reserved_word_in_update",
			input:    "UPDATE kv SET value = 'v2' WHERE key = 'k1'",
			expected: "UPDATE kv SET `value` = 'v2' WHERE `key` = 'k1'",
		},
		{
			name:     "preserves_non_reserved_words",
			input:    "SELECT id, age, email FROM users WHERE id = 1",
			expected: "SELECT id, age, email FROM users WHERE id = 1",
		},
		{
			name:     "preserves_reserved_word_inside_string_literal",
			input:    "SELECT * FROM t WHERE label = 'key value status'",
			expected: "SELECT * FROM t WHERE label = 'key value status'",
		},
		{
			name:     "preserves_already_backtick_quoted",
			input:    "SELECT `key`, `value` FROM kv",
			expected: "SELECT `key`, `value` FROM kv",
		},
		{
			name:     "does_not_quote_substring_of_identifier",
			input:    "SELECT keynote, subvalue, typename FROM t",
			expected: "SELECT keynote, subvalue, typename FROM t",
		},
		{
			name:     "reserved_word_with_dollar_params",
			input:    "SELECT key, value FROM kv WHERE key = $1",
			expected: "SELECT `key`, `value` FROM kv WHERE `key` = ?",
		},
		{
			name:     "reserved_word_with_cast",
			input:    "SELECT value::text FROM kv WHERE key = $1",
			expected: "SELECT CAST(`value` AS text) FROM kv WHERE `key` = ?",
		},
		{
			name:     "reserved_word_with_double_quotes",
			input:    `SELECT "key", value FROM kv`,
			expected: "SELECT `key`, `value` FROM kv",
		},
		{
			name:     "escaped_quotes_inside_string",
			input:    "SELECT * FROM t WHERE name = 'it''s a key'",
			expected: "SELECT * FROM t WHERE `name` = 'it''s a key'",
		},
		{
			name:     "numeric_prefix_not_quoted",
			input:    "SELECT 42key FROM t",
			expected: "SELECT 42key FROM t",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tr.Translate(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestTranslator_Translate_OnConflictDoUpdate verifies the rewrite GORM's
// postgres driver depends on for Upsert/OnConflict clauses: DO UPDATE SET
// with EXCLUDED column references becomes ON DUPLICATE KEY UPDATE with
// VALUES(...) references.
func TestTranslator_Translate_OnConflictDoUpdate(t *testing.T) {
	tr := NewTranslator()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "single_column_target_single_set",
			input:    "INSERT INTO users (id, name) VALUES ($1, $2) ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name",
			expected: "INSERT INTO users (id, `name`) VALUES (?, ?) ON DUPLICATE KEY UPDATE `name` = VALUES(`name`)",
		},
		{
			name:     "multi_column_target_multi_set",
			input:    "INSERT INTO users (id, name, email) VALUES ($1, $2, $3) ON CONFLICT (id, email) DO UPDATE SET name = EXCLUDED.name, email = EXCLUDED.email",
			expected: "INSERT INTO users (id, `name`, email) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE `name` = VALUES(`name`), email = VALUES(email)",
		},
		{
			name:     "returning_preserved_after_rewrite",
			input:    "INSERT INTO users (id, name) VALUES ($1, $2) ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name RETURNING id",
			expected: "INSERT INTO users (id, `name`) VALUES (?, ?) ON DUPLICATE KEY UPDATE `name` = VALUES(`name`) RETURNING id",
		},
		{
			name:     "double_quoted_conflict_target_and_excluded",
			input:    `INSERT INTO "users" ("id", "name") VALUES ($1, $2) ON CONFLICT ("id") DO UPDATE SET "name" = EXCLUDED."name"`,
			expected: "INSERT INTO `users` (`id`, `name`) VALUES (?, ?) ON DUPLICATE KEY UPDATE `name` = VALUES(`name`)",
		},
		{
			name:     "no_conflict_target_list",
			input:    "INSERT INTO users (id, name) VALUES ($1, $2) ON CONFLICT DO UPDATE SET name = EXCLUDED.name",
			expected: "INSERT INTO users (id, `name`) VALUES (?, ?) ON DUPLICATE KEY UPDATE `name` = VALUES(`name`)",
		},
		{
			name:     "no_conflict_clause",
			input:    "INSERT INTO users (id, name) VALUES ($1, $2)",
			expected: "INSERT INTO users (id, `name`) VALUES (?, ?)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tr.Translate(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestTranslator_Translate_OnConflictDoNothing verifies ON CONFLICT ... DO
// NOTHING becomes INSERT IGNORE, GORM's postgres driver's other Upsert
// shape.
func TestTranslator_Translate_OnConflictDoNothing(t *testing.T) {
	tr := NewTranslator()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "with_conflict_target",
			input:    "INSERT INTO users (id, name) VALUES ($1, $2) ON CONFLICT (id) DO NOTHING",
			expected: "INSERT IGNORE INTO users (id, `name`) VALUES (?, ?) ",
		},
		{
			name:     "without_conflict_target",
			input:    "INSERT INTO users (id, name) VALUES ($1, $2) ON CONFLICT DO NOTHING",
			expected: "INSERT IGNORE INTO users (id, `name`) VALUES (?, ?) ",
		},
		{
			name:     "lowercase_insert",
			input:    "insert into users (id, name) values ($1, $2) on conflict (id) do nothing",
			expected: "insert IGNORE into users (id, `name`) values (?, ?) ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tr.Translate(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestTranslator_Translate_UnsupportedOnConflict verifies the two ON
// CONFLICT shapes that cannot be expressed in MySQL's ON DUPLICATE KEY
// UPDATE / INSERT IGNORE syntax are rejected with a typed error rather
// than silently mistranslated.
func TestTranslator_Translate_UnsupportedOnConflict(t *testing.T) {
	tr := NewTranslator()

	tests := []struct {
		name    string
		input   string
		feature string
	}{
		{
			name:    "on_constraint_target",
			input:   "INSERT INTO users (id, name) VALUES ($1, $2) ON CONFLICT ON CONSTRAINT users_pkey DO UPDATE SET name = EXCLUDED.name",
			feature: "ON CONFLICT ON CONSTRAINT",
		},
		{
			name:    "partial_index_predicate",
			input:   "INSERT INTO users (id, name) VALUES ($1, $2) ON CONFLICT (id) WHERE active DO UPDATE SET name = EXCLUDED.name",
			feature: "ON CONFLICT ... WHERE (partial index predicate or conditional DO UPDATE)",
		},
		{
			name:    "conditional_do_update_predicate",
			input:   "INSERT INTO users (id, name) VALUES ($1, $2) ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name WHERE users.active",
			feature: "ON CONFLICT ... WHERE (partial index predicate or conditional DO UPDATE)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tr.Translate(tt.input)
			require.Error(t, err)
			assert.Empty(t, result)

			var unsupported *UnsupportedSyntaxError
			require.ErrorAs(t, err, &unsupported)
			assert.Equal(t, tt.feature, unsupported.Feature)
		})
	}
}

func TestQuoteReservedWords_EmptyString(t *testing.T) {
	result := quoteReservedWords("")
	assert.Equal(t, "", result)
}

func TestQuoteReservedWords_NoReservedWords(t *testing.T) {
	input := "SELECT id, age FROM users WHERE id = 1"
	result := quoteReservedWords(input)
	assert.Equal(t, input, result)
}

func TestQuoteReservedWords_UnclosedStringLiteral(t *testing.T) {
	// An unclosed single quote should be handled gracefully by consuming
	// to end of string without panicking.
	result := quoteReservedWords("SELECT * FROM t WHERE key = 'unclosed")
	assert.Equal(t, "SELECT * FROM t WHERE `key` = 'unclosed", result)
}

func TestQuoteReservedWords_UnclosedBacktickQuote(t *testing.T) {
	// An unclosed backtick should be handled gracefully.
	result := quoteReservedWords("SELECT `key FROM t")
	assert.Equal(t, "SELECT `key FROM t", result)
}

func TestConvertDoubleQuotedIdentifiers_UnclosedQuote(t *testing.T) {
	// An unclosed double quote should be handled gracefully by converting
	// the opening quote to a backtick without a closing backtick.
	result := convertDoubleQuotedIdentifiers(`SELECT "unclosed FROM t`)
	assert.Equal(t, "SELECT `unclosed FROM t", result)
}

func TestConvertDoubleQuotedIdentifiers_EmptyIdentifier(t *testing.T) {
	result := convertDoubleQuotedIdentifiers(`SELECT "" FROM t`)
	assert.Equal(t, "SELECT `` FROM t", result)
}

func TestConvertDoubleQuotedIdentifiers_UnclosedSingleQuote(t *testing.T) {
	// An unclosed single quote should be handled by consuming to end of string.
	result := convertDoubleQuotedIdentifiers(`SELECT * FROM t WHERE name = 'unclosed`)
	assert.Equal(t, `SELECT * FROM t WHERE name = 'unclosed`, result)
}

// TestTranslator_Translate_CorruptionRegressions is a corruption-regression
// table: for every transformation Translate performs, a string literal
// whose content merely contains that transformation's trigger text must
// round-trip byte-identical. Before Translate became literal-aware (see
// lexer.go), each of these inputs was silently corrupted, because every
// transformation was a regex or string replace applied to the whole
// statement text with no notion of where a string literal, comment, or
// dollar-quoted string began or ended.
func TestTranslator_Translate_CorruptionRegressions(t *testing.T) {
	tr := NewTranslator()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			// Measured corruption: ILIKE inside a literal became LIKE.
			name:     "ilike_trigger_in_literal",
			input:    "INSERT INTO posts (body) VALUES ('I really ILIKE this cake')",
			expected: "INSERT INTO posts (body) VALUES ('I really ILIKE this cake')",
		},
		{
			// Measured corruption: an identifier::type-shaped substring
			// inside a literal was rewritten to CAST(...).
			name:     "cast_trigger_in_literal",
			input:    "INSERT INTO posts (body) VALUES ('please cast me::text ok')",
			expected: "INSERT INTO posts (body) VALUES ('please cast me::text ok')",
		},
		{
			// Measured corruption: LIMIT ALL inside a literal was deleted.
			name:     "limit_all_trigger_in_literal",
			input:    "INSERT INTO posts (body) VALUES ('we accept LIMIT ALL orders')",
			expected: "INSERT INTO posts (body) VALUES ('we accept LIMIT ALL orders')",
		},
		{
			// Measured corruption: an E'...'-shaped substring inside an
			// outer literal was mistaken for its own escape-string
			// literal and had its quoting stripped.
			name:     "escape_string_trigger_in_literal",
			input:    "INSERT INTO posts (body) VALUES ('the answer is E''nope''')",
			expected: "INSERT INTO posts (body) VALUES ('the answer is E''nope''')",
		},
		{
			name:     "dollar_param_trigger_in_literal",
			input:    "INSERT INTO posts (body) VALUES ('call me at $1 tomorrow')",
			expected: "INSERT INTO posts (body) VALUES ('call me at $1 tomorrow')",
		},
		{
			name:     "double_quoted_text_in_literal",
			input:    `INSERT INTO posts (body) VALUES ('she said "hello" to me')`,
			expected: `INSERT INTO posts (body) VALUES ('she said "hello" to me')`,
		},
		{
			name:     "reserved_words_in_literal",
			input:    "INSERT INTO posts (body) VALUES ('the key and value have status pending')",
			expected: "INSERT INTO posts (body) VALUES ('the key and value have status pending')",
		},
		{
			name:     "trigger_text_in_line_comment",
			input:    "SELECT * FROM t -- ILIKE $1 LIMIT ALL E'x'::text ON CONFLICT DO UPDATE SET\nWHERE id = 1",
			expected: "SELECT * FROM t -- ILIKE $1 LIMIT ALL E'x'::text ON CONFLICT DO UPDATE SET\nWHERE id = 1",
		},
		{
			name:     "trigger_text_in_block_comment",
			input:    "SELECT * FROM t /* ILIKE $1 LIMIT ALL E'x'::text ON CONFLICT DO UPDATE SET */ WHERE id = 1",
			expected: "SELECT * FROM t /* ILIKE $1 LIMIT ALL E'x'::text ON CONFLICT DO UPDATE SET */ WHERE id = 1",
		},
		{
			name:     "trigger_text_in_dollar_quoted_string",
			input:    "SELECT $$ILIKE $1 LIMIT ALL E'x'::text ON CONFLICT DO UPDATE SET$$ FROM t",
			expected: "SELECT $$ILIKE $1 LIMIT ALL E'x'::text ON CONFLICT DO UPDATE SET$$ FROM t",
		},
		{
			// Trigger text sitting immediately next to a doubled ''
			// escape, so escape handling and trigger-text protection
			// have to compose correctly at the same boundary.
			name:     "trigger_text_adjacent_to_escaped_quote",
			input:    "INSERT INTO posts (body) VALUES ('it''s ILIKE that, ''really''')",
			expected: "INSERT INTO posts (body) VALUES ('it''s ILIKE that, ''really''')",
		},
		{
			// A literal whose trailing run of doubled quotes looks, to a
			// naive regex, like it could terminate the string early or
			// leave it unterminated. scanRegions must still find the
			// literal's true end.
			name:     "unterminated_looking_quote_sequence",
			input:    "INSERT INTO posts (body) VALUES ('it''s only a ''test'' really''')",
			expected: "INSERT INTO posts (body) VALUES ('it''s only a ''test'' really''')",
		},
		{
			// Real transformations firing outside the literal must still
			// work while a lookalike trigger inside the literal is left
			// alone: $1 outside becomes ?, key/value in the column list
			// are quoted, but $2 and "key" inside the literal are not.
			name:     "real_transform_and_protected_literal_together",
			input:    "INSERT INTO kv (key, value) VALUES ($1, 'the real key is ILIKE $2 not a param')",
			expected: "INSERT INTO kv (`key`, `value`) VALUES (?, 'the real key is ILIKE $2 not a param')",
		},
		{
			// A reserved word and a cast-shaped substring both appear
			// inside the literal; only the genuine cast outside it fires.
			name:     "cast_fires_outside_literal_only",
			input:    "SELECT id::integer, 'note: value::text field' FROM t",
			expected: "SELECT CAST(id AS integer), 'note: value::text field' FROM t",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tr.Translate(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}
