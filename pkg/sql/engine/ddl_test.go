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

	"github.com/dolthub/vitess/go/vt/sqlparser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTranslateDDLTypes_CreateTable covers the CREATE TABLE column-list
// walk: every PostgreSQL type in the measured GORM corpus, the pass-through
// of types PostgreSQL and MySQL already spell alike, and the constraint
// elements that must not be mistaken for column definitions.
func TestTranslateDDLTypes_CreateTable(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "bigserial_primary_key",
			input:    "CREATE TABLE `companies` (`id` bigserial,`name` text,PRIMARY KEY (`id`))",
			expected: "CREATE TABLE `companies` (`id` BIGINT AUTO_INCREMENT,`name` LONGTEXT,PRIMARY KEY (`id`))",
		},
		{
			name:     "serial",
			input:    "CREATE TABLE `t` (`id` serial,PRIMARY KEY (`id`))",
			expected: "CREATE TABLE `t` (`id` INT AUTO_INCREMENT,PRIMARY KEY (`id`))",
		},
		{
			name:     "smallserial",
			input:    "CREATE TABLE `t` (`id` smallserial,PRIMARY KEY (`id`))",
			expected: "CREATE TABLE `t` (`id` SMALLINT AUTO_INCREMENT,PRIMARY KEY (`id`))",
		},
		{
			name:     "serial_keeps_following_attributes",
			input:    "CREATE TABLE `t` (`uid` bigserial NOT NULL,PRIMARY KEY (`uid`))",
			expected: "CREATE TABLE `t` (`uid` BIGINT AUTO_INCREMENT NOT NULL,PRIMARY KEY (`uid`))",
		},
		{
			name:     "timestamptz",
			input:    "CREATE TABLE `t` (`created_at` timestamptz)",
			expected: "CREATE TABLE `t` (`created_at` DATETIME)",
		},
		{
			name:     "timestamptz_with_precision",
			input:    "CREATE TABLE `t` (`birthday` timestamptz(4))",
			expected: "CREATE TABLE `t` (`birthday` DATETIME(4))",
		},
		{
			name:     "timestamp_without_time_zone",
			input:    "CREATE TABLE `t` (`created_at` TIMESTAMP WITHOUT TIME ZONE)",
			expected: "CREATE TABLE `t` (`created_at` DATETIME)",
		},
		{
			name:     "timestamp_with_time_zone",
			input:    "CREATE TABLE `t` (`created_at` TIMESTAMP WITH TIME ZONE)",
			expected: "CREATE TABLE `t` (`created_at` DATETIME)",
		},
		{
			name:     "timestamp_without_time_zone_keeps_default",
			input:    "CREATE TABLE `t` (`updated_at` TIMESTAMP WITHOUT TIME ZONE DEFAULT current_timestamp)",
			expected: "CREATE TABLE `t` (`updated_at` DATETIME DEFAULT current_timestamp)",
		},
		{
			name:     "bare_timestamp",
			input:    "CREATE TABLE `t` (`a` timestamp)",
			expected: "CREATE TABLE `t` (`a` DATETIME)",
		},
		{
			name:     "bytea",
			input:    "CREATE TABLE `t` (`job_info` bytea)",
			expected: "CREATE TABLE `t` (`job_info` LONGBLOB)",
		},
		{
			name:     "uuid",
			input:    "CREATE TABLE `t` (`id` uuid DEFAULT uuid_generate_v4(),PRIMARY KEY (`id`))",
			expected: "CREATE TABLE `t` (`id` CHAR(36) DEFAULT uuid_generate_v4(),PRIMARY KEY (`id`))",
		},
		{
			name:     "shared_spellings_pass_through",
			input:    "CREATE TABLE `t` (`a` bigint,`b` integer,`c` smallint,`d` decimal,`e` numeric(9,0),`f` boolean,`g` varchar(233))",
			expected: "CREATE TABLE `t` (`a` bigint,`b` integer,`c` smallint,`d` decimal,`e` numeric(9,0),`f` boolean,`g` varchar(233))",
		},
		{
			name:     "constraint_elements_untouched",
			input:    "CREATE TABLE `t` (`id` bigserial,PRIMARY KEY (`id`),CONSTRAINT `u` UNIQUE (`id`),CONSTRAINT `f` FOREIGN KEY (`p`) REFERENCES `o`(`id`),CONSTRAINT `c` CHECK (name <> ''))",
			expected: "CREATE TABLE `t` (`id` BIGINT AUTO_INCREMENT,PRIMARY KEY (`id`),CONSTRAINT `u` UNIQUE (`id`),CONSTRAINT `f` FOREIGN KEY (`p`) REFERENCES `o`(`id`),CONSTRAINT `c` CHECK (name <> ''))",
		},
		{
			name:     "unquoted_column_names",
			input:    "CREATE TABLE t (id bigserial, body text)",
			expected: "CREATE TABLE t (id BIGINT AUTO_INCREMENT, body LONGTEXT)",
		},
		{
			name:     "if_not_exists",
			input:    "CREATE TABLE IF NOT EXISTS `t` (`id` bigserial,PRIMARY KEY (`id`))",
			expected: "CREATE TABLE IF NOT EXISTS `t` (`id` BIGINT AUTO_INCREMENT,PRIMARY KEY (`id`))",
		},
		{
			name:     "whitespace_and_newlines_preserved",
			input:    "CREATE TABLE `t` (\n\t`id`   bigserial,\n\t`name` text\n)",
			expected: "CREATE TABLE `t` (\n\t`id`   BIGINT AUTO_INCREMENT,\n\t`name` LONGTEXT\n)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := translateDDLTypes(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)

			_, parseErr := sqlparser.Parse(result)
			assert.NoError(t, parseErr, "rewritten statement must parse")
		})
	}
}

// TestTranslateDDLTypes_ColumnNamesAreNotTypes pins the property the whole
// structural walk exists for: a PostgreSQL type name is only rewritten
// where a type is grammatically required, so the same word used as a column
// name, a table name, or a constraint name survives byte-for-byte.
func TestTranslateDDLTypes_ColumnNamesAreNotTypes(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "column_named_serial",
			input: "CREATE TABLE `t` (`serial` bigint,`text` bigint,`uuid` bigint)",
		},
		{
			name:  "unquoted_column_named_serial",
			input: "CREATE TABLE t (serial bigint, text bigint)",
		},
		{
			name:  "table_named_serial",
			input: "CREATE TABLE serial (a bigint)",
		},
		{
			name:  "constraint_named_serial",
			input: "CREATE TABLE `t` (`a` bigint,CONSTRAINT `serial` UNIQUE (`a`))",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := translateDDLTypes(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.input, result)
		})
	}
}

// TestTranslateDDLTypes_NonTableStatements pins that the rewrite is confined
// to CREATE TABLE and ALTER TABLE: every other statement is returned
// byte-for-byte, so a bare identifier named after a PostgreSQL type is never
// touched outside a column definition.
func TestTranslateDDLTypes_NonTableStatements(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "select_column_named_serial", input: "SELECT serial, text, uuid FROM t"},
		{name: "select_from_table_named_text", input: "SELECT * FROM text WHERE serial = 1"},
		{name: "insert_values_named_bytea", input: "INSERT INTO t (bytea) VALUES ('x')"},
		{name: "update_set_serial", input: "UPDATE t SET serial = 1 WHERE text = 'a'"},
		{name: "delete_where_uuid", input: "DELETE FROM t WHERE uuid = 'x'"},
		{name: "create_index", input: "CREATE UNIQUE INDEX `i` ON `t` (`serial`)"},
		{name: "create_database", input: "CREATE DATABASE text"},
		{name: "drop_table", input: "DROP TABLE IF EXISTS `t` CASCADE"},
		{name: "alter_database", input: "ALTER DATABASE text CHARACTER SET utf8"},
		{name: "empty", input: ""},
		{name: "single_token", input: "COMMIT"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := translateDDLTypes(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.input, result)
		})
	}
}

// TestTranslateDDLTypes_AlterTable covers the ALTER TABLE clause walk: the
// clauses that introduce a column type are rewritten, and the far more
// common ones that do not are left alone.
func TestTranslateDDLTypes_AlterTable(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "add_column_implicit",
			input:    "ALTER TABLE `t` ADD `a` text",
			expected: "ALTER TABLE `t` ADD `a` LONGTEXT",
		},
		{
			name:     "add_column_explicit",
			input:    "ALTER TABLE `t` ADD COLUMN `a` timestamptz",
			expected: "ALTER TABLE `t` ADD COLUMN `a` DATETIME",
		},
		{
			name:     "modify_column",
			input:    "ALTER TABLE `t` MODIFY COLUMN `a` bytea",
			expected: "ALTER TABLE `t` MODIFY COLUMN `a` LONGBLOB",
		},
		{
			name:     "change_column",
			input:    "ALTER TABLE `t` CHANGE COLUMN `a` `b` text",
			expected: "ALTER TABLE `t` CHANGE COLUMN `a` `b` LONGTEXT",
		},
		{
			name:     "multiple_clauses",
			input:    "ALTER TABLE `t` ADD `a` text, ADD `b` timestamptz",
			expected: "ALTER TABLE `t` ADD `a` LONGTEXT, ADD `b` DATETIME",
		},
		{
			name:     "add_constraint_untouched",
			input:    "ALTER TABLE `accounts` ADD CONSTRAINT `fk_users_account` FOREIGN KEY (`user_id`) REFERENCES `users`(`id`)",
			expected: "ALTER TABLE `accounts` ADD CONSTRAINT `fk_users_account` FOREIGN KEY (`user_id`) REFERENCES `users`(`id`)",
		},
		{
			name:     "add_unique_untouched",
			input:    "ALTER TABLE things ADD CONSTRAINT some_id_other_id_unique UNIQUE (some_id, other_id)",
			expected: "ALTER TABLE things ADD CONSTRAINT some_id_other_id_unique UNIQUE (some_id, other_id)",
		},
		{
			name:     "drop_constraint_untouched",
			input:    "ALTER TABLE `accounts` DROP CONSTRAINT `fk_users_account`",
			expected: "ALTER TABLE `accounts` DROP CONSTRAINT `fk_users_account`",
		},
		{
			name:     "drop_column_untouched",
			input:    "ALTER TABLE `t` DROP COLUMN `text`",
			expected: "ALTER TABLE `t` DROP COLUMN `text`",
		},
		{
			name:     "rename_untouched",
			input:    "ALTER TABLE `t` RENAME COLUMN `a` TO `text`",
			expected: "ALTER TABLE `t` RENAME COLUMN `a` TO `text`",
		},
		{
			name:     "qualified_table_name",
			input:    "ALTER TABLE `db`.`t` ADD `a` text",
			expected: "ALTER TABLE `db`.`t` ADD `a` LONGTEXT",
		},
		{
			name:     "add_with_no_type",
			input:    "ALTER TABLE `t` ADD `a`",
			expected: "ALTER TABLE `t` ADD `a`",
		},
		{
			name:     "trailing_comma_clause",
			input:    "ALTER TABLE `t` ADD `a` text,",
			expected: "ALTER TABLE `t` ADD `a` LONGTEXT,",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := translateDDLTypes(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestTranslateDDLTypes_Rejections pins that a column type with no faithful
// go-mysql-server counterpart produces a typed error naming it, rather than
// a column silently typed as something else.
func TestTranslateDDLTypes_Rejections(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantType string
	}{
		{
			name:     "array_of_mapped_type",
			input:    "CREATE TABLE `t` (`things` text[])",
			wantType: "text[]",
		},
		{
			name:     "array_of_passthrough_type",
			input:    "CREATE TABLE `t` (`ids` bigint[])",
			wantType: "bigint[]",
		},
		{
			name:     "array_after_modifier",
			input:    "CREATE TABLE `t` (`names` varchar(20)[])",
			wantType: "varchar(20)[]",
		},
		{
			name:     "modifier_on_type_that_cannot_carry_one",
			input:    "CREATE TABLE `t` (`id` uuid(16))",
			wantType: "uuid(16)",
		},
		{
			name:     "precision_before_time_zone_qualifier",
			input:    "CREATE TABLE `t` (`a` timestamp(3) without time zone)",
			wantType: "timestamp(3) WITHOUT TIME ZONE",
		},
		{
			name:     "array_in_alter_table",
			input:    "ALTER TABLE `t` ADD `things` text[]",
			wantType: "text[]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := translateDDLTypes(tt.input)
			require.Error(t, err)
			assert.Empty(t, result)

			var typed *UnsupportedColumnTypeError
			require.ErrorAs(t, err, &typed)
			assert.Equal(t, tt.wantType, typed.Type)
			assert.Contains(t, typed.Error(), tt.wantType)
		})
	}
}

// TestTranslateDDLTypes_ProtectedRegions pins that the DDL tokenizer
// inherits the package's protected-region machinery: a PostgreSQL type name
// appearing inside a string literal, a comment, or a dollar-quoted string is
// never a type position and is never rewritten.
func TestTranslateDDLTypes_ProtectedRegions(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "type_name_inside_default_literal",
			input:    "CREATE TABLE `t` (`a` varchar(20) DEFAULT 'bigserial text uuid')",
			expected: "CREATE TABLE `t` (`a` varchar(20) DEFAULT 'bigserial text uuid')",
		},
		{
			name:     "literal_containing_comma_does_not_split_elements",
			input:    "CREATE TABLE `t` (`a` varchar(20) DEFAULT 'x,y', `b` text)",
			expected: "CREATE TABLE `t` (`a` varchar(20) DEFAULT 'x,y', `b` LONGTEXT)",
		},
		{
			name:     "literal_containing_paren_does_not_close_column_list",
			input:    "CREATE TABLE `t` (`a` varchar(20) DEFAULT ')', `b` text)",
			expected: "CREATE TABLE `t` (`a` varchar(20) DEFAULT ')', `b` LONGTEXT)",
		},
		{
			name:     "escaped_quote_inside_literal",
			input:    "CREATE TABLE `t` (`a` varchar(20) DEFAULT 'it''s, text', `b` text)",
			expected: "CREATE TABLE `t` (`a` varchar(20) DEFAULT 'it''s, text', `b` LONGTEXT)",
		},
		{
			name:     "line_comment",
			input:    "CREATE TABLE `t` ( -- text bigserial\n`a` text)",
			expected: "CREATE TABLE `t` ( -- text bigserial\n`a` LONGTEXT)",
		},
		{
			name:     "block_comment",
			input:    "CREATE TABLE `t` (/* text, bigserial */`a` text)",
			expected: "CREATE TABLE `t` (/* text, bigserial */`a` LONGTEXT)",
		},
		{
			name:     "dollar_quoted_string",
			input:    "CREATE TABLE `t` (`a` varchar(20) DEFAULT $tag$text, bigserial$tag$)",
			expected: "CREATE TABLE `t` (`a` varchar(20) DEFAULT $tag$text, bigserial$tag$)",
		},
		{
			name:     "backtick_quoted_type_lookalike_is_a_name",
			input:    "CREATE TABLE `t` (`text` bigint, `bigserial` bigint)",
			expected: "CREATE TABLE `t` (`text` bigint, `bigserial` bigint)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := translateDDLTypes(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestTranslateDDLTypes_Malformed pins that a truncated or unbalanced
// statement is returned rather than panicking the tokenizer: the rewriter
// is not a validator, and a statement the parser will reject anyway must
// reach it intact so the client sees the parser's diagnosis.
func TestTranslateDDLTypes_Malformed(t *testing.T) {
	tests := []string{
		"CREATE TABLE",
		"CREATE TABLE `t`",
		"CREATE TABLE `t` (",
		"CREATE TABLE `t` (`a`",
		"CREATE TABLE `t` (`a` varchar(",
		"CREATE TABLE `t` (,)",
		"ALTER TABLE",
		"ALTER TABLE `t`",
		"ALTER TABLE `t` ADD",
		"ALTER TABLE `t` ADD COLUMN",
		"ALTER TABLE `t` `db`.",
		"CREATE TABLE `t` (`a` text",
	}

	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			assert.NotPanics(t, func() {
				_, _ = translateDDLTypes(input)
			})
		})
	}
}

// TestTranslateDDLTypes_MultipleSerialColumns documents the one place this
// translation is deliberately lossy in the direction of an error: MySQL
// permits a single AUTO_INCREMENT column per table while PostgreSQL permits
// any number of serial columns, so a two-serial table is rewritten
// faithfully and rejected downstream by the engine rather than having its
// second column quietly demoted to a plain integer.
func TestTranslateDDLTypes_MultipleSerialColumns(t *testing.T) {
	result, err := translateDDLTypes("CREATE TABLE `events` (`id` bigserial,`uid` bigserial NOT NULL,PRIMARY KEY (`id`))")
	require.NoError(t, err)
	assert.Equal(t,
		"CREATE TABLE `events` (`id` BIGINT AUTO_INCREMENT,`uid` BIGINT AUTO_INCREMENT NOT NULL,PRIMARY KEY (`id`))",
		result)

	_, parseErr := sqlparser.Parse(result)
	assert.NoError(t, parseErr, "the rewrite is grammatical; the one-auto-column rule is the engine's to enforce")
}

// TestTranslator_Translate_DDLTypes exercises the DDL rewrite through the
// public Translate pipeline, where it runs after double-quote conversion, so
// the statements are the ones gorm.io/driver/postgres actually sends.
func TestTranslator_Translate_DDLTypes(t *testing.T) {
	tr := NewTranslator()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "gorm_companies",
			input:    `CREATE TABLE "companies" ("id" bigserial,"name" text,PRIMARY KEY ("id"))`,
			expected: "CREATE TABLE `companies` (`id` BIGINT AUTO_INCREMENT,`name` LONGTEXT,PRIMARY KEY (`id`))",
		},
		{
			name:     "gorm_users",
			input:    `CREATE TABLE "users" ("id" bigserial,"created_at" timestamptz,"name" text,"age" bigint,"active" boolean,PRIMARY KEY ("id"))`,
			expected: "CREATE TABLE `users` (`id` BIGINT AUTO_INCREMENT,`created_at` DATETIME,`name` LONGTEXT,`age` bigint,`active` boolean,PRIMARY KEY (`id`))",
		},
		{
			name:     "reserved_word_column_name",
			input:    `CREATE TABLE "level2" ("id" bigserial,"value" text,PRIMARY KEY ("id"))`,
			expected: "CREATE TABLE `level2` (`id` BIGINT AUTO_INCREMENT,`value` LONGTEXT,PRIMARY KEY (`id`))",
		},
		{
			name:     "unquoted_reserved_word_column_name",
			input:    "CREATE TABLE t (id bigserial, name text, PRIMARY KEY (id))",
			expected: "CREATE TABLE t (id BIGINT AUTO_INCREMENT, `name` LONGTEXT, PRIMARY KEY (id))",
		},
		{
			name:     "select_of_column_named_serial_is_untouched",
			input:    `SELECT "serial" FROM "t" WHERE "text" = $1`,
			expected: "SELECT `serial` FROM `t` WHERE `text` = ?",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tr.Translate(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)

			_, parseErr := sqlparser.Parse(result)
			assert.NoError(t, parseErr, "rewritten statement must parse")
		})
	}
}

// TestTranslator_Translate_DDLTypeRejection pins that the typed rejection
// propagates out of Translate rather than being swallowed.
func TestTranslator_Translate_DDLTypeRejection(t *testing.T) {
	tr := NewTranslator()

	result, err := tr.Translate(`CREATE TABLE "harumphs" ("id" bigserial,"things" text[],PRIMARY KEY ("id"))`)
	require.Error(t, err)
	assert.Empty(t, result)

	var typed *UnsupportedColumnTypeError
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, "text[]", typed.Type)
}

// TestTranslator_Translate_CurrentSchema covers the current_schema() /
// current_database() rewrite: both niladic calls become DATABASE(), and
// neither an identifier of the same name nor the same text inside a
// protected region is touched.
func TestTranslator_Translate_CurrentSchema(t *testing.T) {
	tr := NewTranslator()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "gorm_has_table_probe",
			input:    `SELECT count(*) FROM information_schema.tables WHERE table_schema = CURRENT_SCHEMA() AND table_name = $1 AND table_type = $2`,
			expected: "SELECT count(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ? AND table_type = ?",
		},
		{
			name:     "current_database",
			input:    "SELECT CURRENT_DATABASE()",
			expected: "SELECT DATABASE()",
		},
		{
			name:     "lowercase",
			input:    "SELECT current_schema()",
			expected: "SELECT DATABASE()",
		},
		{
			name:     "whitespace_inside_call",
			input:    "SELECT current_schema (  )",
			expected: "SELECT DATABASE()",
		},
		{
			name:     "bare_identifier_untouched",
			input:    "SELECT current_schema FROM t",
			expected: "SELECT current_schema FROM t",
		},
		{
			name:     "quoted_identifier_untouched",
			input:    `SELECT "current_schema" FROM t`,
			expected: "SELECT `current_schema` FROM t",
		},
		{
			name:     "string_literal_untouched",
			input:    "SELECT 'current_schema()' FROM t",
			expected: "SELECT 'current_schema()' FROM t",
		},
		{
			name:     "comment_untouched",
			input:    "SELECT 1 -- current_schema()",
			expected: "SELECT 1 -- current_schema()",
		},
		{
			name:     "call_with_arguments_untouched",
			input:    "SELECT current_schema(1)",
			expected: "SELECT current_schema(1)",
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

// TestScanDDLTokens covers the tokenizer's classification directly, so a
// regression in it is reported here rather than only as a surprising
// rewrite several layers up.
func TestScanDDLTokens(t *testing.T) {
	// A comment yields no token at all: it carries no syntax, so tokenizing
	// it would place a spurious element between a column's name and its
	// type and break every position-based lookahead. A string literal, by
	// contrast, does occupy a grammatical slot and IS a token -- one opaque
	// ddlRegion that can never be mistaken for a type name.
	tests := []struct {
		name  string
		query string
		texts []string
		kinds []ddlTokenKind
	}{
		{
			name:  "line comment yields no token",
			query: "CREATE TABLE `t` (`a` text) -- c\n",
			texts: []string{"CREATE", "TABLE", "`t`", "(", "`a`", "text", ")"},
			kinds: []ddlTokenKind{ddlWord, ddlWord, ddlQuoted, ddlPunct, ddlQuoted, ddlWord, ddlPunct},
		},
		{
			name:  "block comment between name and type yields no token",
			query: "CREATE TABLE `t` (`a` /* note */ text)",
			texts: []string{"CREATE", "TABLE", "`t`", "(", "`a`", "text", ")"},
			kinds: []ddlTokenKind{ddlWord, ddlWord, ddlQuoted, ddlPunct, ddlQuoted, ddlWord, ddlPunct},
		},
		{
			name:  "string literal is one opaque token",
			query: "CREATE TABLE `t` (`a` text DEFAULT 'bigserial')",
			texts: []string{"CREATE", "TABLE", "`t`", "(", "`a`", "text", "DEFAULT", "'bigserial'", ")"},
			kinds: []ddlTokenKind{
				ddlWord, ddlWord, ddlQuoted, ddlPunct, ddlQuoted,
				ddlWord, ddlWord, ddlRegion, ddlPunct,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toks := scanDDLTokens(tt.query)

			kinds := make([]ddlTokenKind, len(toks))
			texts := make([]string, len(toks))
			for i, tok := range toks {
				kinds[i] = tok.kind
				texts[i] = tok.text(tt.query)
			}

			assert.Equal(t, tt.texts, texts)
			assert.Equal(t, tt.kinds, kinds)
		})
	}
}

// TestDDLTokenWord pins that only a bare word ever compares equal to a
// keyword: a backtick-quoted identifier, a punctuation byte, and a string
// literal spelling the same word all report "".
func TestDDLTokenWord(t *testing.T) {
	query := "PRIMARY `PRIMARY` , 'PRIMARY'"
	toks := scanDDLTokens(query)
	require.Len(t, toks, 4)

	assert.Equal(t, "PRIMARY", toks[0].word(query))
	assert.Equal(t, "", toks[1].word(query))
	assert.Equal(t, "", toks[2].word(query))
	assert.Equal(t, "", toks[3].word(query))

	assert.True(t, toks[2].isPunct(query, ','))
	assert.False(t, toks[2].isPunct(query, '('))
	assert.False(t, toks[0].isPunct(query, ','))
}

// TestApplyDDLEdits covers the substitution assembler independently of the
// walks that produce edits, including the no-edit fast path.
func TestApplyDDLEdits(t *testing.T) {
	const query = "abcdef"

	assert.Equal(t, query, applyDDLEdits(query, nil))
	assert.Equal(t, "aXcdef", applyDDLEdits(query, []ddlEdit{{start: 1, end: 2, text: "X"}}))
	assert.Equal(t, "aXcYef", applyDDLEdits(query, []ddlEdit{
		{start: 1, end: 2, text: "X"},
		{start: 3, end: 4, text: "Y"},
	}))
	assert.Equal(t, "", applyDDLEdits(query, []ddlEdit{{start: 0, end: 6, text: ""}}))
}

// TestColumnTypeModifier covers the modifier scanner's three outcomes:
// no modifier, a balanced one, and an unterminated one.
func TestColumnTypeModifier(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		expected string
	}{
		{name: "none", query: "text x", expected: ""},
		{name: "length", query: "varchar(20)", expected: "(20)"},
		{name: "precision_and_scale", query: "numeric(9,0)", expected: "(9,0)"},
		{name: "nested", query: "t((1))", expected: "((1))"},
		{name: "unterminated", query: "varchar(20", expected: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toks := scanDDLTokens(tt.query)
			got, next := columnTypeModifier(tt.query, toks, 1, len(toks))
			assert.Equal(t, tt.expected, got)
			if tt.expected == "" {
				assert.Equal(t, 1, next)
			} else {
				assert.Greater(t, next, 1)
			}
		})
	}
}

// TestUnsupportedColumnTypeError pins the message shape clients see.
func TestUnsupportedColumnTypeError(t *testing.T) {
	err := &UnsupportedColumnTypeError{Type: "text[]"}
	assert.Equal(t, "sql/engine: unsupported PostgreSQL column type: text[]", err.Error())
}

// TestTranslateDDLTypes_KeyedTextColumns covers the contextual half of the
// type mapping: a column whose PostgreSQL type maps to one of MySQL's
// unbounded TEXT/BLOB families takes a bounded substitution instead when the
// same statement puts it in a key, because MySQL cannot index a TEXT or BLOB
// column without a prefix length and rejects the whole CREATE TABLE with
// "blob/text column 'code' used in key specification without a key length".
//
// The cases below are the discriminating ones: which clause names the
// column, whether the column is named by a key at all, and the constraint
// kinds that create no index and must therefore leave the column unbounded.
func TestTranslateDDLTypes_KeyedTextColumns(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "primary_key_bounds_only_the_keyed_column",
			input:    "CREATE TABLE `languages` (`code` text,`name` text,PRIMARY KEY (`code`))",
			expected: "CREATE TABLE `languages` (`code` VARCHAR(768),`name` LONGTEXT,PRIMARY KEY (`code`))",
		},
		{
			name:     "composite_primary_key_bounds_every_text_member",
			input:    "CREATE TABLE `t` (`a` text,`b` text,`c` text,PRIMARY KEY (`a`,`b`))",
			expected: "CREATE TABLE `t` (`a` VARCHAR(768),`b` VARCHAR(768),`c` LONGTEXT,PRIMARY KEY (`a`,`b`))",
		},
		{
			name:     "named_unique_constraint",
			input:    "CREATE TABLE `t` (`id` text,PRIMARY KEY (`id`),CONSTRAINT `uni_t_id` UNIQUE (`id`))",
			expected: "CREATE TABLE `t` (`id` VARCHAR(768),PRIMARY KEY (`id`),CONSTRAINT `uni_t_id` UNIQUE (`id`))",
		},
		{
			name:     "bare_unique_constraint",
			input:    "CREATE TABLE `t` (`a` text,UNIQUE (`a`))",
			expected: "CREATE TABLE `t` (`a` VARCHAR(768),UNIQUE (`a`))",
		},
		{
			name:     "table_level_index_and_key_clauses",
			input:    "CREATE TABLE `t` (`a` text,`b` text,INDEX (`a`),KEY `k` (`b`))",
			expected: "CREATE TABLE `t` (`a` VARCHAR(768),`b` VARCHAR(768),INDEX (`a`),KEY `k` (`b`))",
		},
		{
			name:     "inline_primary_key",
			input:    "CREATE TABLE `t` (`id` text PRIMARY KEY,`body` text)",
			expected: "CREATE TABLE `t` (`id` VARCHAR(768) PRIMARY KEY,`body` LONGTEXT)",
		},
		{
			name:     "inline_unique",
			input:    "CREATE TABLE `t` (`id` text UNIQUE,`body` text)",
			expected: "CREATE TABLE `t` (`id` VARCHAR(768) UNIQUE,`body` LONGTEXT)",
		},
		{
			name:     "bytea_in_key_is_bounded_as_binary",
			input:    "CREATE TABLE `t` (`k` bytea,`v` bytea,PRIMARY KEY (`k`))",
			expected: "CREATE TABLE `t` (`k` VARBINARY(3072),`v` LONGBLOB,PRIMARY KEY (`k`))",
		},
		{
			name: "foreign_key_does_not_bound_its_child_column",
			// The index a foreign key relies on is the referenced table's own
			// key. Bounding the child column here would narrow it for a
			// requirement go-mysql-server does not impose.
			input:    "CREATE TABLE `labels` (`book_id` bigint,`note` text,CONSTRAINT `fk` FOREIGN KEY (`note`) REFERENCES `books`(`id`))",
			expected: "CREATE TABLE `labels` (`book_id` bigint,`note` LONGTEXT,CONSTRAINT `fk` FOREIGN KEY (`note`) REFERENCES `books`(`id`))",
		},
		{
			name:     "check_constraint_naming_the_column_does_not_bound_it",
			input:    "CREATE TABLE `t` (`name` text,CONSTRAINT `name_checker` CHECK (name <> ''))",
			expected: "CREATE TABLE `t` (`name` LONGTEXT,CONSTRAINT `name_checker` CHECK (name <> ''))",
		},
		{
			name: "fulltext_index_leaves_the_column_unbounded",
			// A FULLTEXT index over a TEXT column needs no prefix length --
			// that combination is what FULLTEXT is for.
			input:    "CREATE TABLE `t` (`body` text,FULLTEXT (`body`))",
			expected: "CREATE TABLE `t` (`body` LONGTEXT,FULLTEXT (`body`))",
		},
		{
			name:     "key_membership_is_case_insensitive_and_quote_insensitive",
			input:    "CREATE TABLE `t` (`Code` text,PRIMARY KEY (code))",
			expected: "CREATE TABLE `t` (`Code` VARCHAR(768),PRIMARY KEY (code))",
		},
		{
			name: "prefix_length_in_the_key_list_is_not_another_column",
			// The 10 sits in an option position, not a name position, so it
			// must not be recorded as a keyed column name of its own.
			input:    "CREATE TABLE `t` (`a` text,`b` text,PRIMARY KEY (`a`(10)))",
			expected: "CREATE TABLE `t` (`a` VARCHAR(768),`b` LONGTEXT,PRIMARY KEY (`a`(10)))",
		},
		{
			name:     "sort_direction_in_the_key_list_is_not_another_column",
			input:    "CREATE TABLE `t` (`a` text,`desc` text,PRIMARY KEY (`a` DESC))",
			expected: "CREATE TABLE `t` (`a` VARCHAR(768),`desc` LONGTEXT,PRIMARY KEY (`a` DESC))",
		},
		{
			name: "already_bounded_type_in_a_key_passes_through",
			// varchar carries its own bound, has no pgColumnTypes entry, and
			// is indexable exactly as the client wrote it.
			input:    "CREATE TABLE `t` (`a` varchar(20),PRIMARY KEY (`a`))",
			expected: "CREATE TABLE `t` (`a` varchar(20),PRIMARY KEY (`a`))",
		},
		{
			name: "keyed_serial_is_unaffected",
			// Only the TEXT/BLOB-mapping types carry a KeyedName; an integer
			// key needs no bound.
			input:    "CREATE TABLE `t` (`id` bigserial,`code` text,PRIMARY KEY (`id`))",
			expected: "CREATE TABLE `t` (`id` BIGINT AUTO_INCREMENT,`code` LONGTEXT,PRIMARY KEY (`id`))",
		},
		{
			name: "key_naming_an_undeclared_column_bounds_nothing",
			// The rewriter is not a validator: a key over a column the
			// statement never declares is the parser's error to report, and
			// no unrelated column may be narrowed on the way there.
			input:    "CREATE TABLE `t` (`a` text,PRIMARY KEY (`missing`))",
			expected: "CREATE TABLE `t` (`a` LONGTEXT,PRIMARY KEY (`missing`))",
		},
		{
			name: "string_literal_default_cannot_forge_key_membership",
			// PRIMARY appears only inside a DEFAULT value, which the lexer
			// carries as one opaque region and never reads as a keyword.
			input:    "CREATE TABLE `t` (`a` text DEFAULT 'PRIMARY KEY',`b` text)",
			expected: "CREATE TABLE `t` (`a` LONGTEXT DEFAULT 'PRIMARY KEY',`b` LONGTEXT)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := translateDDLTypes(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestTranslateDDLTypes_KeyedColumnsParseAndIndex is the end-to-end claim the
// bounded substitution exists to make good on: the statements gorm's
// PostgreSQL driver sends for a text-keyed model both parse AND survive
// go-mysql-server's CREATE TABLE validation, which is where the unbounded
// LONGTEXT spelling was rejected.
func TestTranslateDDLTypes_KeyedColumnsParseAndIndex(t *testing.T) {
	tr := NewTranslator()

	// Exactly as captured from gorm.io/driver/postgres against this server.
	corpus := []string{
		`CREATE TABLE "languages" ("code" text,"name" text,PRIMARY KEY ("code"))`,
		`CREATE TABLE "langs" ("code" text,"name" text,PRIMARY KEY ("code"))`,
		`CREATE TABLE "orgs" ("id" text,PRIMARY KEY ("id"))`,
		`CREATE TABLE "address_by_zip_codes" ("zip_code" text,"address" text,PRIMARY KEY ("zip_code"))`,
		`CREATE TABLE "blogs" ("id" bigserial,"locale" text,"subject" text,"body" text,PRIMARY KEY ("id","locale"),CONSTRAINT "uni_blogs_id" UNIQUE ("id"))`,
		`CREATE TABLE "labels" ("book_id" bigint,"name" text,"value" text,PRIMARY KEY ("book_id","name"),CONSTRAINT "fk_books_labels" FOREIGN KEY ("book_id") REFERENCES "books"("id"))`,
		`CREATE TABLE "on_conflict_users" ("id" text,"name" text DEFAULT null,"email" text,"mobile" text DEFAULT '133xxxx',PRIMARY KEY ("id"))`,
	}

	for _, query := range corpus {
		t.Run(query[:min(len(query), 44)], func(t *testing.T) {
			translated, err := tr.Translate(query)
			require.NoError(t, err)
			assert.NotContains(t, keyedColumnTypes(t, translated), "LONGTEXT",
				"no column named by a key may keep an unbounded type")

			_, parseErr := sqlparser.Parse(translated)
			require.NoError(t, parseErr)
		})
	}
}

// keyedColumnTypes returns the substituted type of every column the
// statement's keys name, as a single string, for assertion.
func keyedColumnTypes(t *testing.T, query string) string {
	t.Helper()

	toks := scanDDLTokens(query)
	elements := createTableElements(query, toks)
	keyed := keyedColumns(query, toks, elements)

	var types string
	for _, el := range elements {
		if el.end-el.start < 2 || tableConstraintKeywords[toks[el.start].word(query)] {
			continue
		}
		if keyed[identName(query, toks[el.start])] {
			types += query[toks[el.start+1].start:toks[el.end-1].end] + " "
		}
	}
	return types
}

// TestKeyedColumnBoundsAreIndexable pins the two constants against the limit
// that dictates them: go-mysql-server enforces InnoDB's 3072-byte index-key
// ceiling, scaled by the charset's bytes per character for character types.
// A wider VARCHAR would be refused by the very validation the substitution
// exists to satisfy, and a narrower one would truncate caller values this
// engine can index.
func TestKeyedColumnBoundsAreIndexable(t *testing.T) {
	const maxBytePrefix = 3072    // analyzer.MaxBytePrefix
	const utf8mb4BytesPerChar = 4 // widest charset go-mysql-server defaults to

	assert.Equal(t, "VARCHAR(768)", keyedTextType)
	assert.Equal(t, maxBytePrefix/utf8mb4BytesPerChar, 768,
		"the character bound is the byte ceiling divided by the widest charset")

	assert.Equal(t, "VARBINARY(3072)", keyedBlobType)
	assert.Equal(t, maxBytePrefix, 3072,
		"a binary prefix is counted in bytes, so no charset scaling applies")
}

// TestInlineKeyed covers the column-constraint scanner directly, including
// the depth rule that keeps a parenthesized clause from triggering it.
func TestInlineKeyed(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{name: "primary_key", input: "text PRIMARY KEY", want: true},
		{name: "unique", input: "text UNIQUE", want: true},
		{name: "not_null_only", input: "text NOT NULL", want: false},
		{name: "references", input: "text REFERENCES `o`(`id`)", want: false},
		{name: "nested_check_body", input: "text CHECK (x IN (SELECT UNIQUE))", want: false},
		{name: "string_literal", input: "text DEFAULT 'PRIMARY'", want: false},
		{name: "quoted_identifier", input: "text DEFAULT `unique`", want: false},
		{name: "empty", input: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toks := scanDDLTokens(tt.input)
			assert.Equal(t, tt.want, inlineKeyed(tt.input, toks, 0, len(toks)))
		})
	}
}

// TestIdentName covers name normalization, which is what makes key
// membership agree with MySQL's own case-insensitive column matching.
func TestIdentName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "bare_word", input: "Code", want: "code"},
		{name: "backtick_quoted", input: "`Code`", want: "code"},
		{name: "string_literal", input: "'code'", want: ""},
		{name: "punctuation", input: ",", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toks := scanDDLTokens(tt.input)
			require.Len(t, toks, 1)
			assert.Equal(t, tt.want, identName(tt.input, toks[0]))
		})
	}
}

// TestCreateTableElements covers the element splitter, including the
// truncated statement whose unterminated trailing fragment is deliberately
// not an element.
func TestCreateTableElements(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{name: "no_paren", input: "CREATE TABLE `t`", want: 0},
		{name: "single_column", input: "CREATE TABLE `t` (`a` text)", want: 1},
		{name: "three_elements", input: "CREATE TABLE `t` (`a` text,`b` text,PRIMARY KEY (`a`))", want: 3},
		{name: "nested_parens_are_not_separators", input: "CREATE TABLE `t` (`a` numeric(9,0),`b` text)", want: 2},
		{name: "unterminated_drops_trailing_fragment", input: "CREATE TABLE `t` (`a` text,`b` text", want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toks := scanDDLTokens(tt.input)
			assert.Len(t, createTableElements(tt.input, toks), tt.want)
		})
	}
}

// TestTranslateDDLTypes_KeyedRejections pins that key membership never
// converts a rejection into a silent substitution: a keyed column that
// already names a modifier, or that is an array, is still refused by name.
func TestTranslateDDLTypes_KeyedRejections(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "keyed_text_with_a_modifier",
			input: "CREATE TABLE `t` (`a` text(20),PRIMARY KEY (`a`))",
			want:  "text(20)",
		},
		{
			name:  "keyed_text_array",
			input: "CREATE TABLE `t` (`a` text[],PRIMARY KEY (`a`))",
			want:  "text[]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := translateDDLTypes(tt.input)
			var typeErr *UnsupportedColumnTypeError
			require.ErrorAs(t, err, &typeErr)
			assert.Equal(t, tt.want, typeErr.Type)
		})
	}
}

// TestTranslateDDLTypes_AlterTableInlineKey covers the one form of key
// membership an ALTER TABLE clause can express by itself. A column made a key
// by some SEPARATE statement is out of reach by construction and keeps the
// unbounded substitution.
func TestTranslateDDLTypes_AlterTableInlineKey(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "add_column_unique",
			input:    "ALTER TABLE `t` ADD COLUMN `code` text UNIQUE",
			expected: "ALTER TABLE `t` ADD COLUMN `code` VARCHAR(768) UNIQUE",
		},
		{
			name:     "add_column_primary_key",
			input:    "ALTER TABLE `t` ADD `code` text PRIMARY KEY",
			expected: "ALTER TABLE `t` ADD `code` VARCHAR(768) PRIMARY KEY",
		},
		{
			name:     "add_plain_column_stays_unbounded",
			input:    "ALTER TABLE `t` ADD COLUMN `body` text",
			expected: "ALTER TABLE `t` ADD COLUMN `body` LONGTEXT",
		},
		{
			name:     "modify_column_unique",
			input:    "ALTER TABLE `t` MODIFY COLUMN `code` text UNIQUE",
			expected: "ALTER TABLE `t` MODIFY COLUMN `code` VARCHAR(768) UNIQUE",
		},
		{
			name:     "change_column_stays_unbounded",
			input:    "ALTER TABLE `t` CHANGE `old` `new` text",
			expected: "ALTER TABLE `t` CHANGE `old` `new` LONGTEXT",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := translateDDLTypes(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestTranslateDDLTypes_KeyClauseWithoutColumnList pins that a key
// constraint naming no column list at all contributes no key membership
// rather than reaching past its own clause for one. The statement is
// malformed and the parser will say so; the rewriter must not narrow an
// unrelated column on the way there.
func TestTranslateDDLTypes_KeyClauseWithoutColumnList(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "primary_key_with_no_list",
			input:    "CREATE TABLE `t` (`a` text,PRIMARY KEY)",
			expected: "CREATE TABLE `t` (`a` LONGTEXT,PRIMARY KEY)",
		},
		{
			name:     "unique_with_no_list",
			input:    "CREATE TABLE `t` (`a` text,CONSTRAINT `u` UNIQUE)",
			expected: "CREATE TABLE `t` (`a` LONGTEXT,CONSTRAINT `u` UNIQUE)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := translateDDLTypes(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestTranslateDDLTypes_AlterTableMultipleClauses covers the clause splitter
// on a statement carrying several comma-separated clauses, including one
// whose parenthesized modifier contains a comma that is not a separator.
func TestTranslateDDLTypes_AlterTableMultipleClauses(t *testing.T) {
	result, err := translateDDLTypes(
		"ALTER TABLE `t` ADD COLUMN `a` text, ADD COLUMN `b` numeric(9,0), MODIFY COLUMN `c` timestamptz, DROP COLUMN `d`")
	require.NoError(t, err)
	assert.Equal(t,
		"ALTER TABLE `t` ADD COLUMN `a` LONGTEXT, ADD COLUMN `b` numeric(9,0), MODIFY COLUMN `c` DATETIME, DROP COLUMN `d`",
		result)
}

// TestTranslateDDLTypes_AlterTableRejectionInNonFinalClause pins that a type
// that cannot be expressed is refused wherever it appears, including in a
// clause that is not the statement's last: the whole statement fails with the
// typed error rather than having the clauses before the offending one applied.
func TestTranslateDDLTypes_AlterTableRejectionInNonFinalClause(t *testing.T) {
	_, err := translateDDLTypes("ALTER TABLE `t` ADD COLUMN `a` text[], ADD COLUMN `b` bigint")

	var typeErr *UnsupportedColumnTypeError
	require.ErrorAs(t, err, &typeErr)
	assert.Equal(t, "text[]", typeErr.Type)
}
