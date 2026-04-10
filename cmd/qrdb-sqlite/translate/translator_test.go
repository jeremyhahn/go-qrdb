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

package translate

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newT is a test helper that returns a freshly constructed Translator.
func newT() *Translator { return NewTranslator() }

// --- Double-quoted identifier conversion (Rule 1) ---

func TestTranslate_DoubleQuotedIdentifiers(t *testing.T) {
	got, err := newT().Translate(`SELECT "col" FROM "tbl"`)
	require.NoError(t, err)
	assert.Equal(t, "SELECT `col` FROM `tbl`", got)
}

func TestTranslate_DoubleQuotedIdentifiers_InString(t *testing.T) {
	// Double quotes inside single-quoted string literals must not be replaced.
	got, err := newT().Translate(`SELECT 'He said "hello"' AS greeting`)
	require.NoError(t, err)
	assert.Equal(t, `SELECT 'He said "hello"' AS greeting`, got)
}

func TestTranslate_DoubleQuotedIdentifiers_EscapedDoubleQuote(t *testing.T) {
	// An escaped double-quote inside an identifier (""col"") should preserve
	// the inner literal quote character.
	got, err := newT().Translate(`SELECT "say ""hi""" AS x`)
	require.NoError(t, err)
	assert.Equal(t, "SELECT `say \"hi\"` AS x", got)
}

func TestTranslate_DoubleQuotedIdentifiers_MixedContext(t *testing.T) {
	// Identifiers and string literals containing double-quotes in the same query.
	got, err := newT().Translate(`SELECT "id", 'it''s "quoted"' FROM "users"`)
	require.NoError(t, err)
	assert.Equal(t, "SELECT `id`, 'it''s \"quoted\"' FROM `users`", got)
}

// --- String concatenation (Rule 2) ---

func TestTranslate_StringConcat(t *testing.T) {
	got, err := newT().Translate(`SELECT first_name || ' ' || last_name AS full_name FROM users`)
	require.NoError(t, err)
	// Three operands → right-to-left fold.
	assert.Equal(t, `SELECT CONCAT(first_name, CONCAT(' ', last_name)) AS full_name FROM users`, got)
}

func TestTranslate_StringConcat_TwoOperands(t *testing.T) {
	got, err := newT().Translate(`SELECT a || b FROM t`)
	require.NoError(t, err)
	assert.Equal(t, `SELECT CONCAT(a, b) FROM t`, got)
}

func TestTranslate_StringConcat_Multiple(t *testing.T) {
	// a || b || c → right-to-left fold: CONCAT(a, CONCAT(b, c))
	got, err := newT().Translate(`SELECT a || b || c FROM t`)
	require.NoError(t, err)
	assert.Equal(t, `SELECT CONCAT(a, CONCAT(b, c)) FROM t`, got)
}

func TestTranslate_StringConcat_FourOperands(t *testing.T) {
	// a || b || c || d → CONCAT(a, CONCAT(b, CONCAT(c, d)))
	got, err := newT().Translate(`SELECT a || b || c || d FROM t`)
	require.NoError(t, err)
	assert.Equal(t, `SELECT CONCAT(a, CONCAT(b, CONCAT(c, d))) FROM t`, got)
}

func TestTranslate_StringConcat_InsideStringNotTouched(t *testing.T) {
	// A literal string that contains || must not be converted to CONCAT.
	got, err := newT().Translate(`SELECT '||' FROM t`)
	require.NoError(t, err)
	assert.Equal(t, `SELECT '||' FROM t`, got)
}

// --- IFNULL → COALESCE (Rule 3) ---

func TestTranslate_IFNULL(t *testing.T) {
	got, err := newT().Translate(`SELECT IFNULL(col, 0) FROM t`)
	require.NoError(t, err)
	assert.Equal(t, `SELECT COALESCE(col, 0) FROM t`, got)
}

func TestTranslate_IFNULL_PreservesArguments(t *testing.T) {
	// Arguments must be passed through unchanged.
	got, err := newT().Translate(`SELECT IFNULL(x, 'fallback') FROM t`)
	require.NoError(t, err)
	assert.Equal(t, `SELECT COALESCE(x, 'fallback') FROM t`, got)
}

// --- SUBSTR → SUBSTRING (Rule 4) ---

func TestTranslate_SUBSTR(t *testing.T) {
	got, err := newT().Translate(`SELECT SUBSTR(name, 1, 3) FROM t`)
	require.NoError(t, err)
	assert.Equal(t, `SELECT SUBSTRING(name, 1, 3) FROM t`, got)
}

func TestTranslate_SUBSTR_TwoArg(t *testing.T) {
	got, err := newT().Translate(`SELECT SUBSTR(name, 2) FROM t`)
	require.NoError(t, err)
	assert.Equal(t, `SELECT SUBSTRING(name, 2) FROM t`, got)
}

// --- RANDOM → RAND (Rule 5) ---

func TestTranslate_RANDOM(t *testing.T) {
	got, err := newT().Translate(`SELECT RANDOM()`)
	require.NoError(t, err)
	assert.Equal(t, `SELECT RAND()`, got)
}

func TestTranslate_RANDOM_WithSpaces(t *testing.T) {
	got, err := newT().Translate(`SELECT RANDOM(  ) FROM t`)
	require.NoError(t, err)
	assert.Equal(t, `SELECT RAND() FROM t`, got)
}

// --- TOTAL → SUM (Rule 6) ---

func TestTranslate_TOTAL(t *testing.T) {
	got, err := newT().Translate(`SELECT TOTAL(amount) FROM orders`)
	require.NoError(t, err)
	assert.Equal(t, `SELECT SUM(amount) FROM orders`, got)
}

func TestTranslate_TOTAL_NotMatchingSubstring(t *testing.T) {
	// A bare word "TOTALLY" (no opening paren) must not be renamed.
	got, err := newT().Translate(`SELECT count(*) AS totally_fine FROM t`)
	require.NoError(t, err)
	assert.Contains(t, got, "totally_fine")
	assert.NotContains(t, got, "SUM(")
}

// --- GLOB → LIKE (Rule 7) ---

func TestTranslate_GLOB(t *testing.T) {
	got, err := newT().Translate(`SELECT * FROM t WHERE name GLOB '*pattern*'`)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM t WHERE name LIKE '%pattern%'`, got)
}

func TestTranslate_GLOB_QuestionMark(t *testing.T) {
	got, err := newT().Translate(`SELECT * FROM t WHERE code GLOB '??-*'`)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM t WHERE code LIKE '__-%'`, got)
}

func TestTranslate_GLOB_StarOnly(t *testing.T) {
	got, err := newT().Translate(`SELECT * FROM t WHERE path GLOB '*'`)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM t WHERE path LIKE '%'`, got)
}

func TestTranslate_GLOB_CaseInsensitive(t *testing.T) {
	got, err := newT().Translate(`SELECT * FROM t WHERE x glob 'abc*'`)
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM t WHERE x LIKE 'abc%'`, got)
}

// --- PRAGMA detection (Rule 8) ---

func TestTranslate_PRAGMA_Get(t *testing.T) {
	_, err := newT().Translate(`PRAGMA qrdb_consistency`)
	require.Error(t, err)

	var pq *PragmaQuery
	require.True(t, errors.As(err, &pq), "expected *PragmaQuery, got %T", err)
	assert.Equal(t, "qrdb_consistency", pq.Name)
	assert.Empty(t, pq.Value)
}

func TestTranslate_PRAGMA_Set(t *testing.T) {
	_, err := newT().Translate(`PRAGMA qrdb_consistency = 'eventual'`)
	require.Error(t, err)

	var pq *PragmaQuery
	require.True(t, errors.As(err, &pq))
	assert.Equal(t, "qrdb_consistency", pq.Name)
	assert.Equal(t, "eventual", pq.Value)
}

func TestTranslate_PRAGMA_SetUnquoted(t *testing.T) {
	_, err := newT().Translate(`PRAGMA qrdb_write_mode = sync`)
	require.Error(t, err)

	var pq *PragmaQuery
	require.True(t, errors.As(err, &pq))
	assert.Equal(t, "qrdb_write_mode", pq.Name)
	assert.Equal(t, "sync", pq.Value)
}

func TestTranslate_PRAGMA_CaseInsensitive(t *testing.T) {
	_, err := newT().Translate(`pragma journal_mode`)
	require.Error(t, err)

	var pq *PragmaQuery
	require.True(t, errors.As(err, &pq))
	assert.Equal(t, "journal_mode", pq.Name)
}

func TestTranslate_PRAGMA_ErrorMessage_Setter(t *testing.T) {
	pq := &PragmaQuery{Name: "cache_size", Value: "1000"}
	msg := pq.Error()
	assert.Contains(t, msg, "cache_size")
	assert.Contains(t, msg, "1000")
}

func TestTranslate_PRAGMA_ErrorMessage_Getter(t *testing.T) {
	pq := &PragmaQuery{Name: "page_size"}
	msg := pq.Error()
	assert.Contains(t, msg, "page_size")
	assert.NotContains(t, msg, "=")
}

// --- AUTOINCREMENT stripping (Rule 9) ---

func TestTranslate_AUTOINCREMENT(t *testing.T) {
	got, err := newT().Translate(
		`CREATE TABLE t (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT)`,
	)
	require.NoError(t, err)
	assert.NotContains(t, got, "AUTOINCREMENT")
	assert.Contains(t, got, "PRIMARY KEY")
	assert.Contains(t, got, "name TEXT")
}

func TestTranslate_AUTOINCREMENT_CaseInsensitive(t *testing.T) {
	got, err := newT().Translate(
		`CREATE TABLE t (id INTEGER PRIMARY KEY autoincrement, name TEXT)`,
	)
	require.NoError(t, err)
	assert.NotContains(t, strings.ToUpper(got), "AUTOINCREMENT")
}

// --- Passthrough: standard SQL unchanged ---

func TestTranslate_Passthrough(t *testing.T) {
	q := `SELECT id, name FROM users WHERE id = 42 ORDER BY name LIMIT 10`
	got, err := newT().Translate(q)
	require.NoError(t, err)
	assert.Equal(t, q, got)
}

func TestTranslate_Passthrough_INSERT(t *testing.T) {
	q := `INSERT INTO t (a, b) VALUES (1, 'hello')`
	got, err := newT().Translate(q)
	require.NoError(t, err)
	assert.Equal(t, q, got)
}

func TestTranslate_Passthrough_UPDATE(t *testing.T) {
	q := `UPDATE t SET a = 1 WHERE b = 2`
	got, err := newT().Translate(q)
	require.NoError(t, err)
	assert.Equal(t, q, got)
}

// --- Empty query error ---

func TestTranslate_EmptyQuery(t *testing.T) {
	_, err := newT().Translate("")
	require.Error(t, err)

	var eq *EmptyQueryError
	assert.True(t, errors.As(err, &eq))
}

func TestTranslate_WhitespaceQuery(t *testing.T) {
	_, err := newT().Translate("   \t\n  ")
	require.Error(t, err)

	var eq *EmptyQueryError
	assert.True(t, errors.As(err, &eq))
}

func TestTranslate_EmptyQueryError_Message(t *testing.T) {
	e := &EmptyQueryError{}
	assert.NotEmpty(t, e.Error())
}

// --- Case-insensitive function renaming ---

func TestTranslate_CaseInsensitive(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains string
		absent   string
	}{
		{"IFNULL uppercase", `SELECT IFNULL(a, b)`, "COALESCE(", "IFNULL("},
		{"IFNULL lowercase", `SELECT ifnull(a, b)`, "COALESCE(", ""},
		{"IFNULL mixedcase", `SELECT IfNull(a, b)`, "COALESCE(", ""},
		{"SUBSTR uppercase", `SELECT SUBSTR(x, 1, 2)`, "SUBSTRING(", "SUBSTR("},
		{"SUBSTR lowercase", `SELECT substr(x, 1, 2)`, "SUBSTRING(", ""},
		{"RANDOM uppercase", `SELECT RANDOM()`, "RAND()", "RANDOM("},
		{"RANDOM lowercase", `SELECT random()`, "RAND()", ""},
		{"TOTAL uppercase", `SELECT TOTAL(x)`, "SUM(", "TOTAL("},
		{"TOTAL lowercase", `SELECT total(x)`, "SUM(", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := newT().Translate(tc.input)
			require.NoError(t, err)
			assert.Contains(t, got, tc.contains)
			if tc.absent != "" {
				assert.NotContains(t, strings.ToUpper(got), strings.ToUpper(tc.absent))
			}
		})
	}
}

// --- Nested functions ---

func TestTranslate_NestedFunctions(t *testing.T) {
	got, err := newT().Translate(`SELECT IFNULL(SUBSTR(name, 1, 3), 'default') FROM t`)
	require.NoError(t, err)
	assert.Equal(t, `SELECT COALESCE(SUBSTRING(name, 1, 3), 'default') FROM t`, got)
}

func TestTranslate_NestedFunctions_TOTAL_IFNULL(t *testing.T) {
	got, err := newT().Translate(`SELECT IFNULL(TOTAL(price), 0) FROM orders`)
	require.NoError(t, err)
	assert.Equal(t, `SELECT COALESCE(SUM(price), 0) FROM orders`, got)
}

// --- String literals must not be modified ---

func TestTranslate_StringLiterals_NotModified(t *testing.T) {
	// A single-quoted string that contains SQL keywords must not be renamed.
	// The keyword-rename functions operate on the full query string but the
	// regex word-boundary + open-paren requirement means bare keywords inside
	// a literal that are not followed by ( are not renamed.
	got, err := newT().Translate(`SELECT 'RANDOM TOTAL SUBSTR' AS keywords FROM t`)
	require.NoError(t, err)
	assert.Equal(t, `SELECT 'RANDOM TOTAL SUBSTR' AS keywords FROM t`, got)
}

func TestTranslate_StringLiterals_EscapedSingleQuote(t *testing.T) {
	// The state machine must correctly handle '' inside a single-quoted string.
	got, err := newT().Translate(`SELECT 'it''s fine' FROM t`)
	require.NoError(t, err)
	assert.Equal(t, `SELECT 'it''s fine' FROM t`, got)
}

// --- Comment preservation ---

func TestTranslate_CommentPreserved_LineComment(t *testing.T) {
	q := `SELECT id FROM t -- fetch the id`
	got, err := newT().Translate(q)
	require.NoError(t, err)
	assert.Equal(t, q, got)
}

func TestTranslate_CommentPreserved_BlockComment(t *testing.T) {
	q := `SELECT /* all rows */ id FROM t`
	got, err := newT().Translate(q)
	require.NoError(t, err)
	assert.Equal(t, q, got)
}

// --- Complex / multi-rule queries ---

func TestTranslate_ComplexQuery(t *testing.T) {
	// Combines: double-quoted identifiers, IFNULL, SUBSTR, and ||.
	input := `SELECT IFNULL(SUBSTR("first_name", 1, 1) || '. ' || "last_name", 'Unknown') FROM "employees"`
	got, err := newT().Translate(input)
	require.NoError(t, err)

	assert.Contains(t, got, "`first_name`")
	assert.Contains(t, got, "`last_name`")
	assert.Contains(t, got, "`employees`")
	assert.Contains(t, got, "COALESCE(")
	assert.Contains(t, got, "SUBSTRING(")
	assert.Contains(t, got, "CONCAT(")
	assert.NotContains(t, got, "||")
}

func TestTranslate_ComplexQuery_GlobAndAutoincrement(t *testing.T) {
	input := `CREATE TABLE logs (id INTEGER PRIMARY KEY AUTOINCREMENT, path TEXT); SELECT * FROM logs WHERE path GLOB '/var/*'`
	got, err := newT().Translate(input)
	require.NoError(t, err)

	assert.NotContains(t, got, "AUTOINCREMENT")
	assert.NotContains(t, got, "GLOB")
	assert.Contains(t, got, "LIKE")
	assert.Contains(t, got, "/var/%")
}

func TestTranslate_ComplexQuery_DDL_AllRules(t *testing.T) {
	// DDL exercising double-quoted identifiers and AUTOINCREMENT.
	ddl := `CREATE TABLE "products" (` +
		`id INTEGER PRIMARY KEY AUTOINCREMENT, ` +
		`"name" TEXT, ` +
		`price REAL)`
	got, err := newT().Translate(ddl)
	require.NoError(t, err)
	assert.Contains(t, got, "`products`")
	assert.Contains(t, got, "`name`")
	assert.NotContains(t, got, "AUTOINCREMENT")
}

// --- Internal helpers (white-box unit tests) ---

func TestConvertDoubleQuotedIdentifiers_EmptyString(t *testing.T) {
	assert.Equal(t, "", convertDoubleQuotedIdentifiers(""))
}

func TestConvertDoubleQuotedIdentifiers_NoQuotes(t *testing.T) {
	q := "SELECT id FROM t"
	assert.Equal(t, q, convertDoubleQuotedIdentifiers(q))
}

func TestConvertStringConcat_NoOp(t *testing.T) {
	q := "SELECT a, b FROM t"
	assert.Equal(t, q, convertStringConcat(q))
}

func TestConvertStringConcat_InBacktickIdentifier(t *testing.T) {
	// A backtick-quoted identifier token is skipped; no CONCAT should appear.
	q := "SELECT `a||b` FROM t"
	result := convertStringConcat(q)
	assert.Equal(t, q, result)
}

func TestRenameFunctions_UnknownFunction(t *testing.T) {
	q := "SELECT COALESCE(a, b) FROM t"
	assert.Equal(t, q, renameFunctions(q))
}

func TestConvertGlob_NoGlob(t *testing.T) {
	q := "SELECT * FROM t WHERE x = 1"
	assert.Equal(t, q, convertGlob(q))
}

func TestStripAutoincrement_NoKeyword(t *testing.T) {
	q := "CREATE TABLE t (id INTEGER PRIMARY KEY)"
	assert.Equal(t, q, stripAutoincrement(q))
}

func TestDetectPragma_NonPragma(t *testing.T) {
	assert.Nil(t, detectPragma("SELECT 1"))
}

func TestDetectPragma_SchemaQualified(t *testing.T) {
	// SQLite PRAGMAs can be schema-qualified: PRAGMA main.cache_size
	pq := detectPragma("PRAGMA main.cache_size")
	require.NotNil(t, pq)
	assert.Equal(t, "main.cache_size", pq.Name)
}

func TestDetectPragma_EmptyString(t *testing.T) {
	assert.Nil(t, detectPragma(""))
}

// --- Coverage gap tests: extractConcatToken and readConcatOperand paths ---

func TestTranslate_StringConcat_FunctionCallOperand(t *testing.T) {
	// Left operand is a function call: SUBSTR(x, 1, 3) || ' suffix'
	got, err := newT().Translate(`SELECT SUBSTR(name, 1, 3) || ' suffix' FROM t`)
	require.NoError(t, err)
	// After SUBSTR→SUBSTRING rename: SUBSTRING(name, 1, 3) || ' suffix'
	// Then concat conversion.
	assert.Contains(t, got, "CONCAT(")
	assert.NotContains(t, got, "||")
}

func TestTranslate_StringConcat_ParenExprOperand(t *testing.T) {
	// Left operand is a parenthesised subexpression.
	got, err := newT().Translate(`SELECT (a + b) || c FROM t`)
	require.NoError(t, err)
	assert.Contains(t, got, "CONCAT(")
	assert.NotContains(t, got, "||")
}

func TestTranslate_StringConcat_BacktickOperand(t *testing.T) {
	// Both operands are backtick-quoted identifiers (after double-quote conversion).
	got, err := newT().Translate("SELECT `first` || `last` FROM t")
	require.NoError(t, err)
	assert.Equal(t, "SELECT CONCAT(`first`, `last`) FROM t", got)
}

func TestTranslate_StringConcat_NumericOperand(t *testing.T) {
	// A numeric literal as right operand.
	got, err := newT().Translate(`SELECT name || 42 FROM t`)
	require.NoError(t, err)
	assert.Equal(t, `SELECT CONCAT(name, 42) FROM t`, got)
}

func TestTranslate_StringConcat_StringLiteralLeftOperand(t *testing.T) {
	// Left operand is a string literal.
	got, err := newT().Translate(`SELECT 'prefix' || name FROM t`)
	require.NoError(t, err)
	assert.Equal(t, `SELECT CONCAT('prefix', name) FROM t`, got)
}

func TestTranslate_StringConcat_FunctionCallRightOperand(t *testing.T) {
	// Right operand is a function call.
	got, err := newT().Translate(`SELECT name || LOWER(suffix) FROM t`)
	require.NoError(t, err)
	assert.Equal(t, `SELECT CONCAT(name, LOWER(suffix)) FROM t`, got)
}

func TestTranslate_StringConcat_ParenExprRightOperand(t *testing.T) {
	// Right operand is a parenthesised subexpression.
	got, err := newT().Translate(`SELECT prefix || (a + b) FROM t`)
	require.NoError(t, err)
	assert.Contains(t, got, "CONCAT(prefix,")
	assert.NotContains(t, got, "||")
}

func TestTranslate_StringConcat_BacktickRightOperand(t *testing.T) {
	// Right operand is a backtick-quoted identifier.
	got, err := newT().Translate("SELECT name || `suffix` FROM t")
	require.NoError(t, err)
	assert.Contains(t, got, "CONCAT(name, `suffix`)")
}

// Additional stripAutoincrement coverage: double-space collapsed.
func TestStripAutoincrement_CollapseSpaces(t *testing.T) {
	// After stripping AUTOINCREMENT between two words, a double space appears.
	result := stripAutoincrement("PRIMARY KEY AUTOINCREMENT NOT NULL")
	assert.NotContains(t, result, "  ")
	assert.NotContains(t, result, "AUTOINCREMENT")
}

// Additional convertGlob coverage: nil submatch guard (unreachable in practice
// but documents the defensive branch).
func TestConvertGlob_PatternOnly(t *testing.T) {
	// A GLOB with an empty pattern.
	got, err := newT().Translate(`SELECT * FROM t WHERE x GLOB ''`)
	require.NoError(t, err)
	assert.Contains(t, got, "LIKE ''")
}

// extractConcatToken: string-literal left operand (backward scan path).
func TestExtractConcatToken_StringLiteral(t *testing.T) {
	tok, start := extractConcatToken("SELECT 'hello'")
	assert.Equal(t, "'hello'", tok)
	assert.Equal(t, 7, start)
}

// extractConcatToken: backtick-quoted identifier (backward scan path).
func TestExtractConcatToken_BacktickIdent(t *testing.T) {
	tok, start := extractConcatToken("SELECT `col`")
	assert.Equal(t, "`col`", tok)
	assert.Equal(t, 7, start)
}

// extractConcatToken: parenthesised expression (backward scan path).
func TestExtractConcatToken_Paren(t *testing.T) {
	tok, start := extractConcatToken("SELECT (a + b)")
	assert.Equal(t, "(a + b)", tok)
	assert.Equal(t, 7, start)
}

// extractConcatToken: function call (backward scan path).
func TestExtractConcatToken_FuncCall(t *testing.T) {
	tok, start := extractConcatToken("SELECT LOWER(x)")
	assert.Equal(t, "LOWER(x)", tok)
	assert.Equal(t, 7, start)
}

// readConcatOperand: empty input returns ("", start).
func TestReadConcatOperand_Empty(t *testing.T) {
	tok, end := readConcatOperand("", 0)
	assert.Equal(t, "", tok)
	assert.Equal(t, 0, end)
}

// readConcatOperand: parenthesised expression.
func TestReadConcatOperand_Paren(t *testing.T) {
	tok, end := readConcatOperand("(a + b) rest", 0)
	assert.Equal(t, "(a + b)", tok)
	assert.Equal(t, 7, end)
}

// readConcatOperand: backtick-quoted identifier.
func TestReadConcatOperand_BacktickIdent(t *testing.T) {
	tok, end := readConcatOperand("`col` rest", 0)
	assert.Equal(t, "`col`", tok)
	assert.Equal(t, 5, end)
}

// readConcatOperand: string literal.
func TestReadConcatOperand_StringLiteral(t *testing.T) {
	tok, end := readConcatOperand("'hello' rest", 0)
	assert.Equal(t, "'hello'", tok)
	assert.Equal(t, 7, end)
}

// readConcatOperand: numeric literal.
func TestReadConcatOperand_Numeric(t *testing.T) {
	tok, end := readConcatOperand("42 rest", 0)
	assert.Equal(t, "42", tok)
	assert.Equal(t, 2, end)
}

// readConcatOperand: function call with nested parens.
func TestReadConcatOperand_FuncCall(t *testing.T) {
	tok, end := readConcatOperand("LOWER(x) rest", 0)
	assert.Equal(t, "LOWER(x)", tok)
	assert.Equal(t, 8, end)
}

// readConcatOperand: unrecognised first character (e.g. punctuation at depth 0).
func TestReadConcatOperand_UnrecognisedChar(t *testing.T) {
	tok, end := readConcatOperand("@invalid", 0)
	assert.Equal(t, "", tok)
	assert.Equal(t, 0, end)
}

// extractConcatToken: empty string returns ("", 0).
func TestExtractConcatToken_EmptyString(t *testing.T) {
	tok, start := extractConcatToken("")
	assert.Equal(t, "", tok)
	assert.Equal(t, 0, start)
}

// extractConcatToken: whitespace only returns ("", 0).
func TestExtractConcatToken_WhitespaceOnly(t *testing.T) {
	tok, start := extractConcatToken("   ")
	assert.Equal(t, "", tok)
	assert.Equal(t, 0, start)
}

// convertStringConcat: no || in query is a fast-path no-op.
func TestConvertStringConcat_NoDoubleBar(t *testing.T) {
	q := "SELECT a, b FROM t WHERE x = 1"
	assert.Equal(t, q, convertStringConcat(q))
}

// Concat with escaped single-quote inside string literal operand.
func TestTranslate_StringConcat_EscapedQuoteInOperand(t *testing.T) {
	got, err := newT().Translate(`SELECT 'it''s' || name FROM t`)
	require.NoError(t, err)
	assert.Contains(t, got, "CONCAT(")
	assert.NotContains(t, got, "||")
}
