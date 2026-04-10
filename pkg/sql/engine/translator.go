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

// Regex patterns for PostgreSQL-to-GMS translations. Compiled once at package
// init for zero per-call allocation overhead.
var (
	// dollarParamRe matches PostgreSQL positional parameters ($1, $2, ...).
	dollarParamRe = regexp.MustCompile(`\$\d+`)

	// castRe matches PostgreSQL cast syntax (value::type). It handles
	// identifiers, string literals, numeric literals, placeholder markers,
	// backtick-quoted identifiers, and parenthesized expressions before the
	// :: operator. The type name is restricted to a single word (no spaces)
	// to avoid greedily consuming subsequent SQL keywords.
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

	// escapeStringRe matches PostgreSQL E'...' escape string literals.
	escapeStringRe = regexp.MustCompile(`(?i)\bE'([^']*(?:''[^']*)*)'`)

	// returningRe matches a RETURNING clause at the end of a statement.
	returningRe = regexp.MustCompile(`(?i)\s+RETURNING\s+.+$`)
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
// compatible SQL. It performs targeted string-level transformations rather
// than full AST translation.
type Translator struct{}

// NewTranslator creates a new Translator.
func NewTranslator() *Translator {
	return &Translator{}
}

// Translate converts a PostgreSQL query to go-mysql-server compatible SQL.
// Standard SQL passes through unchanged. Each transformation is applied
// independently in sequence.
func (t *Translator) Translate(query string) (string, error) {
	result := query

	// 1. Replace $N positional parameters with ? placeholders.
	result = dollarParamRe.ReplaceAllString(result, "?")

	// 2. Convert double-quoted identifiers to backtick-quoted identifiers.
	// This must happen before cast conversion so that backtick-quoted
	// identifiers are recognized in cast expressions like `col`::type.
	result = convertDoubleQuotedIdentifiers(result)

	// 3. Backtick-quote MySQL/GMS reserved words that appear as bare
	// identifiers. This must run after double-quote conversion (step 2)
	// so that already-backtick-quoted identifiers are skipped, and before
	// cast conversion (step 4) so the cast regex recognizes the backtick-
	// quoted identifiers.
	result = quoteReservedWords(result)

	// 4. Convert E'...' escape strings to regular string literals.
	result = escapeStringRe.ReplaceAllString(result, "'$1'")

	// 5. Convert value::type cast syntax to CAST(value AS type).
	result = castRe.ReplaceAllString(result, "CAST($1 AS $2)")

	// 6. Replace ILIKE with LIKE (case-insensitive).
	result = strings.ReplaceAll(result, "ILIKE", "LIKE")
	result = strings.ReplaceAll(result, "ilike", "LIKE")

	// 7. Remove LIMIT ALL.
	result = limitAllRe.ReplaceAllString(result, "")

	// 8. Strip RETURNING clause.
	result = returningRe.ReplaceAllString(result, "")

	return result, nil
}

// convertDoubleQuotedIdentifiers replaces double-quoted identifiers with
// backtick-quoted identifiers while preserving double quotes that appear
// inside single-quoted string literals.
func convertDoubleQuotedIdentifiers(query string) string {
	var b strings.Builder
	b.Grow(len(query))

	i := 0
	for i < len(query) {
		ch := query[i]

		// Skip over single-quoted string literals to avoid replacing
		// double quotes inside string values.
		if ch == '\'' {
			b.WriteByte(ch)
			i++
			for i < len(query) {
				if query[i] == '\'' {
					b.WriteByte(query[i])
					i++
					// Handle escaped single quotes ('').
					if i < len(query) && query[i] == '\'' {
						b.WriteByte(query[i])
						i++
						continue
					}
					break
				}
				b.WriteByte(query[i])
				i++
			}
			continue
		}

		// Replace double-quoted identifier with backtick-quoted identifier.
		if ch == '"' {
			b.WriteByte('`')
			i++
			for i < len(query) && query[i] != '"' {
				b.WriteByte(query[i])
				i++
			}
			if i < len(query) {
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
// single-quoted string literals and backtick-quoted identifiers, and wraps
// bare words that match MySQL/GMS reserved words in backticks. It tracks
// the previous word to avoid quoting reserved words that form compound SQL
// keywords (e.g., PRIMARY KEY).
func quoteReservedWords(query string) string {
	var b strings.Builder
	b.Grow(len(query) + 32) // extra capacity for added backticks

	var prevWord string // lowercase previous bare word for compound keyword detection
	i := 0
	for i < len(query) {
		ch := query[i]

		// Skip single-quoted string literals.
		if ch == '\'' {
			prevWord = ""
			b.WriteByte(ch)
			i++
			for i < len(query) {
				if query[i] == '\'' {
					b.WriteByte(query[i])
					i++
					// Handle escaped single quotes ('').
					if i < len(query) && query[i] == '\'' {
						b.WriteByte(query[i])
						i++
						continue
					}
					break
				}
				b.WriteByte(query[i])
				i++
			}
			continue
		}

		// Skip backtick-quoted identifiers (already quoted).
		if ch == '`' {
			prevWord = ""
			b.WriteByte(ch)
			i++
			for i < len(query) && query[i] != '`' {
				b.WriteByte(query[i])
				i++
			}
			if i < len(query) {
				b.WriteByte(query[i])
				i++ // skip closing backtick
			}
			continue
		}

		// Extract bare word (identifier or keyword).
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_' {
			start := i
			for i < len(query) && isIdentChar(query[i]) {
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
			for i < len(query) && isIdentChar(query[i]) {
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
