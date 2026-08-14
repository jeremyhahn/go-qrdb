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
	"regexp"
	"strings"
)

// Regex patterns for PostgreSQL-to-GMS translations. Compiled once at
// package init for zero per-call allocation overhead. None of these are
// applied directly against raw statement text with (Find|Replace)All*;
// every use goes through applyOutsideRegions, findOutsideRegions, or
// applyCast (see lexer.go), so a match inside a string literal, comment,
// or dollar-quoted string is never mistaken for real SQL.
var (
	// dollarParamRe matches PostgreSQL positional parameters ($1, $2, ...).
	dollarParamRe = regexp.MustCompile(`\$\d+`)

	// castRe matches PostgreSQL cast syntax (value::type). It handles
	// identifiers, string literals, numeric literals, placeholder markers,
	// backtick-quoted identifiers, and parenthesized expressions before the
	// :: operator. The type name is restricted to a single word (no spaces)
	// to avoid greedily consuming subsequent SQL keywords. It is only ever
	// matched against the masked view built by maskForCast (see
	// applyCast), never against raw query text.
	castRe = regexp.MustCompile(`(` +
		`'[^']*'` + // single-quoted string literal
		`|` +
		`` + "`[^`]+`" + // backtick-quoted identifier
		`|` +
		`\([^)]+\)` + // parenthesized expression
		`|` +
		`[A-Za-z_][A-Za-z0-9_]*` + // identifier
		`|` +
		`\d+(?:\.\d+)?` + // numeric literal
		`|` +
		`\?` + // placeholder from $N replacement
		`)::([A-Za-z_][A-Za-z0-9_]*)`) // type name: single word, no spaces

	// limitAllRe matches LIMIT ALL (case-insensitive) and removes it.
	limitAllRe = regexp.MustCompile(`(?i)\bLIMIT\s+ALL\b`)

	// ilikeRe matches the exact-case ILIKE/ilike spellings Translate has
	// always normalized to LIKE; a mixed-case spelling such as IlIkE was
	// never rewritten and still isn't.
	ilikeRe = regexp.MustCompile(`ILIKE|ilike`)

	// updateOrDeleteLeadRe matches a statement's leading UPDATE or DELETE
	// keyword. Paired with returningKeywordRe, it exists only because the
	// vitess grammar this engine embeds added RETURNING to the INSERT
	// production and no other: UPDATE and DELETE plan nodes honor a
	// Returning field when the AST is built programmatically, but nothing
	// in the grammar populates it from parsed text, so these two forms
	// cannot reach the planner at all (see
	// TestUpdateAndDeleteReturning_DoNotParse in returning_test.go). This
	// pair of regexes is deliberately narrow and anchored to the
	// statement's leading keyword rather than a general parser: it exists
	// solely to turn that upstream gap into a clear, typed error instead
	// of a bare syntax error, and should be deleted the day the grammar
	// gains the token.
	updateOrDeleteLeadRe = regexp.MustCompile(`(?i)^\s*(UPDATE|DELETE)\b`)

	// returningKeywordRe matches a bare RETURNING keyword. It is only ever
	// searched for with findOutsideRegions, so a literal, comment, or
	// dollar-quoted string that merely contains the word RETURNING can
	// never trigger the UPDATE/DELETE ... RETURNING rejection above.
	returningKeywordRe = regexp.MustCompile(`(?i)\bRETURNING\b`)

	// onConflictRe matches the ON CONFLICT keyword, used to locate the
	// start of a PostgreSQL upsert clause for unsupported-construct
	// detection.
	onConflictRe = regexp.MustCompile(`(?i)\bON\s+CONFLICT\b`)

	// onConflictOnConstraintRe matches ON CONFLICT ON CONSTRAINT <name>,
	// which targets a named constraint rather than a column list. MySQL's
	// ON DUPLICATE KEY UPDATE has no equivalent: it always keys off
	// whichever unique index the conflicting row actually violates, not a
	// named one, so this form is rejected rather than silently
	// mistranslated.
	onConflictOnConstraintRe = regexp.MustCompile(`(?i)\bON\s+CONFLICT\s+ON\s+CONSTRAINT\b`)

	// onConflictWhereRe matches a bare WHERE token. Used only on the text
	// following an ON CONFLICT match: in a valid INSERT statement that can
	// only be the conflict target's partial-index predicate
	// (ON CONFLICT (cols) WHERE ... DO ...) or the DO UPDATE action's
	// conditional predicate (DO UPDATE SET ... WHERE ...) -- an INSERT has
	// no other clause a bare WHERE belongs to. Neither has a MySQL
	// equivalent.
	onConflictWhereRe = regexp.MustCompile(`(?i)\bWHERE\b`)

	// onConflictDoNothingRe matches ON CONFLICT [(cols)] DO NOTHING.
	onConflictDoNothingRe = regexp.MustCompile(`(?i)\bON\s+CONFLICT\s*(?:\([^)]*\))?\s*DO\s+NOTHING\b`)

	// onConflictDoUpdateRe matches the ON CONFLICT [(cols)] DO UPDATE SET
	// prefix of a PostgreSQL upsert clause, up to and including the SET
	// keyword, so it can be replaced wholesale with MySQL's
	// ON DUPLICATE KEY UPDATE.
	onConflictDoUpdateRe = regexp.MustCompile(`(?i)\bON\s+CONFLICT\s*(?:\([^)]*\))?\s*DO\s+UPDATE\s+SET\b`)

	// excludedColumnRe matches a column reference qualified by the EXCLUDED
	// pseudo-table PostgreSQL exposes inside DO UPDATE SET.
	//
	// BOTH spellings of the qualifier have to be accepted, because by the
	// time this runs the double-quote conversion (step 2 of Translate) has
	// already been applied: a client that wrote EXCLUDED.name still reads as
	// a bare word, but gorm.io/driver/postgres writes the qualifier quoted,
	// "excluded"."name", which arrives here as `excluded`.`name`. Matching
	// only the bare form left every driver-generated upsert with a live
	// reference to a table named "excluded" and failed it downstream with
	// "table not found: excluded". The word-boundary anchor belongs inside
	// the bare alternative only -- a backtick is not a word character, so a
	// leading \b could never match in front of one.
	excludedColumnRe = regexp.MustCompile(`(?i)(?:\bEXCLUDED|` + "`excluded`" + `)\.(` + "`[^`]+`" + `|[A-Za-z_][A-Za-z0-9_]*)`)

	// insertKeywordRe matches the leading INSERT keyword, used to inject
	// the IGNORE modifier when translating ON CONFLICT DO NOTHING, and to
	// confine the DEFAULT VALUES rewrite to INSERT statements.
	insertKeywordRe = regexp.MustCompile(`(?i)^(\s*INSERT)\b`)

	// defaultValuesRe matches PostgreSQL's all-defaults INSERT tail,
	// INSERT INTO t DEFAULT VALUES, which asks for one row composed entirely
	// of column defaults. MySQL spells the same request INSERT INTO t ()
	// VALUES (). The pattern is only ever applied to a statement whose
	// leading keyword is INSERT (see translateDefaultValues), which is the
	// only production the phrase belongs to.
	defaultValuesRe = regexp.MustCompile(`(?i)\bDEFAULT\s+VALUES\b`)

	// currentSchemaRe matches PostgreSQL's current_schema() and
	// current_database() niladic calls. Both are required to be written
	// WITH their empty argument list: PostgreSQL also accepts a bare
	// `current_schema` keyword, but requiring the parentheses is what keeps
	// an ordinary column or alias named current_schema from being rewritten
	// into a function call. Like every other pattern in this file it is only
	// ever applied through applyOutsideRegions, so the same text inside a
	// string literal or comment is left alone.
	currentSchemaRe = regexp.MustCompile(`(?i)\bcurrent_(schema|database)\s*\(\s*\)`)
)

// mysqlReservedWords contains MySQL/GMS reserved words that are NOT reserved
// in PostgreSQL. When a PostgreSQL client sends a query using these as bare
// identifiers, they must be backtick-quoted before being passed to GMS.
// All keys are lowercase; lookups must lowercase the candidate word first.
var mysqlReservedWords = map[string]bool{
	"key":       true,
	"value":     true,
	"status":    true,
	"name":      true,
	"type":      true,
	"interval":  true,
	"function":  true,
	"condition": true,
	"column":    true,
	"schema":    true,
}

// sqlKeywordPairs maps a reserved word (lowercase) to the set of preceding
// SQL keywords (lowercase) that form a compound keyword phrase. When a
// reserved word appears immediately after one of these predecessors, it is
// acting as a SQL keyword, not an identifier, and must NOT be quoted.
// Example: PRIMARY KEY, FOREIGN KEY, UNIQUE KEY.
var sqlKeywordPairs = map[string]map[string]bool{
	"key": {
		"primary": true,
		"foreign": true,
		"unique":  true,
	},
}

// Translator converts PostgreSQL-specific SQL syntax into go-mysql-server
// compatible SQL. It performs targeted, literal-aware string-level
// transformations rather than full AST translation: every transformation
// below re-scans its input for string literals, dollar-quoted strings,
// and comments (see scanRegions in lexer.go) and skips over them, so a
// value that merely looks like a SQL trigger -- ILIKE inside a comment
// body, $1 inside a stored string, a reserved word inside a quoted value
// -- is passed through byte-for-byte rather than rewritten.
type Translator struct{}

// NewTranslator creates a new Translator.
func NewTranslator() *Translator {
	return &Translator{}
}

// Translate converts a PostgreSQL query to go-mysql-server compatible SQL.
// Standard SQL passes through unchanged. Each transformation is applied
// independently in sequence, and each is literal-aware: it never rewrites
// text inside a single-quoted string literal, a dollar-quoted string, a
// line comment, or a block comment (see scanRegions in lexer.go).
//
// INSERT … RETURNING is left untouched: the underlying go-mysql-server
// parser accepts it natively and evaluates the returning expressions
// against the generated row, so qrdb no longer needs to intervene. UPDATE
// … RETURNING and DELETE … RETURNING cannot be parsed at all (see
// updateOrDeleteLeadRe), so Translate rejects them up front with a typed
// *UnsupportedSyntaxError rather than letting the clause reach the parser
// and fail with an opaque syntax error.
func (t *Translator) Translate(query string) (string, error) {
	if lead := updateOrDeleteLeadRe.FindStringSubmatch(query); lead != nil {
		if findOutsideRegions(query, returningKeywordRe) != nil {
			return "", &UnsupportedSyntaxError{Feature: strings.ToUpper(lead[1]) + " ... RETURNING"}
		}
	}

	result := query

	// 1. Replace $N positional parameters with ? placeholders.
	result = applyOutsideRegions(result, dollarParamRe, "?")

	// 2. Convert double-quoted identifiers to backtick-quoted identifiers.
	// This must happen before cast conversion so that backtick-quoted
	// identifiers are recognized in cast expressions like `col`::type.
	result = convertDoubleQuotedIdentifiers(result)

	// 3. Backtick-quote MySQL/GMS reserved words that appear as bare
	// identifiers. This must run after double-quote conversion (step 2)
	// so that already-backtick-quoted identifiers are skipped, and before
	// cast conversion (step 8) so the cast regex recognizes the backtick-
	// quoted identifiers.
	result = quoteReservedWords(result)

	// 4. Rewrite PostgreSQL column types in CREATE TABLE / ALTER TABLE
	// column definitions into their go-mysql-server counterparts. Must run
	// after double-quote conversion (step 2) so a quoted column name is a
	// single backtick-quoted token to the DDL tokenizer rather than a bare
	// word that could be mistaken for a type, and after reserved-word
	// quoting (step 3) for the same reason.
	result, err := translateDDLTypes(result)
	if err != nil {
		return "", err
	}

	// 5. Rewrite INSERT ... DEFAULT VALUES into INSERT ... () VALUES ().
	// Must run before the ON CONFLICT rewrite (step 6), whose DO NOTHING
	// form prepends an IGNORE modifier to the same statement's INSERT
	// keyword: this step's own guard anchors on that keyword and is stated
	// against the statement as the client wrote it.
	result = translateDefaultValues(result)

	// 6. Rewrite ON CONFLICT ... DO UPDATE / DO NOTHING into MySQL's
	// ON DUPLICATE KEY UPDATE / INSERT IGNORE. Must run after reserved-word
	// quoting (step 3) rather than before: the text this step generates
	// (the literal keywords "ON DUPLICATE KEY UPDATE" and the VALUES(...)
	// call) must never be fed back through quoteReservedWords, or the bare
	// keyword KEY -- itself a reserved word in the quoting table -- would
	// be backtick-quoted into an invalid column reference. Running after
	// step 3 also means an EXCLUDED.col reference is already backtick-
	// quoted, which excludedColumnRe accounts for.
	if result, err = translateOnConflict(result); err != nil {
		return "", err
	}

	// 7. Convert E'...' escape strings to regular string literals. Must
	// run before cast conversion (step 8): a leading E left in place on
	// E'2024-01-01'::date would strand the E in front of the CAST(...)
	// this step produces.
	result = applyEscapeStrings(result)

	// 8. Convert value::type cast syntax to CAST(value AS type).
	result = applyCast(result)

	// 9. Replace ILIKE with LIKE (case-insensitive).
	result = applyOutsideRegions(result, ilikeRe, "LIKE")

	// 10. Remove LIMIT ALL.
	result = applyOutsideRegions(result, limitAllRe, "")

	// 11. Rewrite current_schema() / current_database() to DATABASE().
	// PostgreSQL's schema namespace and MySQL's database namespace are the
	// same level of the hierarchy here: go-mysql-server reports a table's
	// owning database as information_schema.tables.TABLE_SCHEMA, which is
	// exactly the column a PostgreSQL client compares current_schema()
	// against, so DATABASE() makes those probes resolve instead of failing
	// on an unknown function.
	result = applyOutsideRegions(result, currentSchemaRe, "DATABASE()")

	return result, nil
}

// HasReturning reports whether query carries a RETURNING clause, ignoring
// the word wherever it appears inside a string literal, a comment, or a
// dollar-quoted string (see scanRegions).
//
// It exists so a protocol front end can tell, before execution, that a
// statement which is not a SELECT nevertheless produces a result set, and
// must therefore describe its result columns to the client. A PostgreSQL
// client learns a statement's shape from the Describe phase of the extended
// query protocol, which happens BEFORE any row exists, so answering "no
// columns" for an INSERT … RETURNING and then producing a row is a protocol
// violation the client cannot recover from.
//
// Only the RETURNING form the engine actually supports can reach a caller of
// this function through Translate: UPDATE … RETURNING and DELETE … RETURNING
// are rejected up front with a typed error (see Translate), so a surviving
// RETURNING is necessarily an INSERT's.
func HasReturning(query string) bool {
	return findOutsideRegions(query, returningKeywordRe) != nil
}

// translateDefaultValues rewrites PostgreSQL's all-defaults insert,
// INSERT INTO t DEFAULT VALUES, into MySQL's spelling of the same request,
// INSERT INTO t () VALUES (). Both ask for exactly one row in which every
// column takes its declared default, so the rewrite is a change of spelling
// and not of meaning.
//
// It is confined to statements whose leading keyword is INSERT, the only
// production the phrase belongs to, and the match itself is located with
// findOutsideRegions, so the words DEFAULT VALUES inside a string literal,
// a comment, or a dollar-quoted string are left alone. Every other
// statement, and every INSERT that names its own column list, is returned
// byte-for-byte unchanged.
func translateDefaultValues(query string) string {
	if insertKeywordRe.FindStringIndex(query) == nil {
		return query
	}
	return applyOutsideRegions(query, defaultValuesRe, "() VALUES ()")
}

// translateOnConflict rewrites a PostgreSQL ON CONFLICT upsert clause into
// its MySQL equivalent. It returns query unchanged if there is no ON
// CONFLICT clause, and a typed *UnsupportedSyntaxError if the clause has a
// shape that has no MySQL equivalent (see unsupportedOnConflict).
//
// ON CONFLICT (cols) DO UPDATE SET a = EXCLUDED.a, b = EXCLUDED.b becomes
// ON DUPLICATE KEY UPDATE a = VALUES(a), b = VALUES(b): VALUES(col) is
// MySQL's equivalent reference to the row that was proposed for insertion.
//
// ON CONFLICT (cols) DO NOTHING becomes INSERT IGNORE. MySQL has no direct
// "do nothing on conflict" clause, but INSERT IGNORE demotes the
// constraint-violation error on every row of the statement to a warning
// and skips that row, which is the same net effect DO NOTHING has for a
// plain upsert.
//
// In both cases the conflict target's column list is dropped: MySQL's ON
// DUPLICATE KEY UPDATE and INSERT IGNORE key off of whichever unique index
// a row's values collide with, not a caller-specified column list.
// PostgreSQL, by contrast, requires the ON CONFLICT target to name (or
// infer) a specific unique index and errors if the row's conflict doesn't
// match it. A statement relying on PostgreSQL rejecting a conflict against
// some *other* unique index will therefore behave differently under this
// translation -- it succeeds against QRDB where it would have errored
// against PostgreSQL. This is a deliberate, documented divergence of the
// descoped translator, not an oversight.
func translateOnConflict(query string) (string, error) {
	if err := unsupportedOnConflict(query); err != nil {
		return "", err
	}

	if loc := findOutsideRegions(query, onConflictDoNothingRe); loc != nil {
		query = query[:loc[0]] + query[loc[1]:]
		return applyOutsideRegions(query, insertKeywordRe, "${1} IGNORE"), nil
	}

	if findOutsideRegions(query, onConflictDoUpdateRe) != nil {
		query = applyOutsideRegions(query, onConflictDoUpdateRe, "ON DUPLICATE KEY UPDATE")
		query = applyOutsideRegions(query, excludedColumnRe, "VALUES($1)")
		return query, nil
	}

	return query, nil
}

// unsupportedOnConflict reports a typed error if query contains an ON
// CONFLICT clause shape that cannot be expressed in MySQL's ON DUPLICATE
// KEY UPDATE / INSERT IGNORE syntax: a named-constraint target, or a WHERE
// predicate (which, in a valid INSERT statement, can only be the conflict
// target's partial-index predicate or the DO UPDATE action's conditional
// predicate). It returns nil if query has no ON CONFLICT clause at all.
func unsupportedOnConflict(query string) error {
	loc := findOutsideRegions(query, onConflictRe)
	if loc == nil {
		return nil
	}
	if findOutsideRegions(query, onConflictOnConstraintRe) != nil {
		return &UnsupportedSyntaxError{Feature: "ON CONFLICT ON CONSTRAINT"}
	}
	if findOutsideRegions(query[loc[1]:], onConflictWhereRe) != nil {
		return &UnsupportedSyntaxError{Feature: "ON CONFLICT ... WHERE (partial index predicate or conditional DO UPDATE)"}
	}
	return nil
}

// convertDoubleQuotedIdentifiers replaces double-quoted identifiers with
// backtick-quoted identifiers, skipping every protected region (see
// scanRegions) so a double quote inside a string literal, comment, or
// dollar-quoted string is left untouched.
func convertDoubleQuotedIdentifiers(query string) string {
	regions := scanRegions(query)

	var b strings.Builder
	b.Grow(len(query))

	ri := 0
	i := 0
	n := len(query)
	for i < n {
		if ri < len(regions) && regions[ri].start == i {
			b.WriteString(query[i:regions[ri].end])
			i = regions[ri].end
			ri++
			continue
		}

		ch := query[i]

		// Replace double-quoted identifier with backtick-quoted identifier.
		if ch == '"' {
			b.WriteByte('`')
			i++
			for i < n && query[i] != '"' {
				b.WriteByte(query[i])
				i++
			}
			if i < n {
				b.WriteByte('`')
				i++ // skip closing "
			}
			continue
		}

		b.WriteByte(ch)
		i++
	}

	return b.String()
}

// isIdentChar reports whether ch is valid inside a SQL identifier
// (letter, digit, or underscore).
func isIdentChar(ch byte) bool {
	return ch == '_' || (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9')
}

// quoteReservedWords scans the query character by character, skipping
// every protected region (see scanRegions) and backtick-quoted
// identifiers, and wraps bare words that match MySQL/GMS reserved words in
// backticks. It tracks the previous word to avoid quoting reserved words
// that form compound SQL keywords (e.g., PRIMARY KEY).
func quoteReservedWords(query string) string {
	regions := scanRegions(query)

	var b strings.Builder
	b.Grow(len(query) + 32) // extra capacity for added backticks

	var prevWord string // lowercase previous bare word for compound keyword detection
	ri := 0
	i := 0
	n := len(query)
	for i < n {
		if ri < len(regions) && regions[ri].start == i {
			prevWord = ""
			b.WriteString(query[i:regions[ri].end])
			i = regions[ri].end
			ri++
			continue
		}

		ch := query[i]

		// Skip backtick-quoted identifiers (already quoted).
		if ch == '`' {
			prevWord = ""
			b.WriteByte(ch)
			i++
			for i < n && query[i] != '`' {
				b.WriteByte(query[i])
				i++
			}
			if i < n {
				b.WriteByte(query[i])
				i++ // skip closing backtick
			}
			continue
		}

		// Extract bare word (identifier or keyword).
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_' {
			start := i
			for i < n && isIdentChar(query[i]) {
				i++
			}
			word := query[start:i]
			lower := strings.ToLower(word)

			// Check if this reserved word forms a compound SQL keyword
			// with the previous word (e.g., PRIMARY KEY).
			isCompound := false
			if predecessors, ok := sqlKeywordPairs[lower]; ok {
				isCompound = predecessors[prevWord]
			}

			if mysqlReservedWords[lower] && !isCompound {
				b.WriteByte('`')
				b.WriteString(word)
				b.WriteByte('`')
			} else {
				b.WriteString(word)
			}
			prevWord = lower
			continue
		}

		// Digits starting a token (numeric literal) - consume the whole
		// number so that trailing identifier chars (e.g., 42abc) don't
		// get misinterpreted. In practice SQL identifiers never start
		// with a digit, but this keeps the scanner robust.
		if ch >= '0' && ch <= '9' {
			prevWord = ""
			for i < n && isIdentChar(query[i]) {
				b.WriteByte(query[i])
				i++
			}
			continue
		}

		// Whitespace and punctuation - don't reset prevWord on whitespace
		// so compound keywords separated by spaces still work.
		if ch != ' ' && ch != '\t' && ch != '\n' && ch != '\r' {
			prevWord = ""
		}
		b.WriteByte(ch)
		i++
	}

	return b.String()
}
