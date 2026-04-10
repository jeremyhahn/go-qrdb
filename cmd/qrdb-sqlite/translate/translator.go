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

// Package translate converts SQLite SQL dialect to go-mysql-server (GMS)
// compatible SQL. It applies a sequential transformation pipeline of targeted
// string-level rewrites: identifier quoting, string concatenation, function
// renames, GLOB→LIKE conversion, AUTOINCREMENT stripping, and PRAGMA
// detection.
//
// Each transformation is a standalone function so that individual rules can be
// tested and reasoned about independently.
package translate

import (
	"regexp"
	"strings"
)

// Regex patterns compiled once at package init for zero per-call overhead.
var (
	// pragmaRe matches a PRAGMA statement at the start of the (trimmed) query.
	// Group 1: pragma name. Group 2 (optional): the value after = or space.
	// Handles both getter form (PRAGMA name) and setter form
	// (PRAGMA name = value or PRAGMA name(value)).
	pragmaRe = regexp.MustCompile(
		`(?i)^\s*PRAGMA\s+([A-Za-z_][A-Za-z0-9_.]*)\s*(?:=\s*(.+?)\s*)?$`,
	)

	// globRe matches a GLOB keyword with its single-quoted pattern argument.
	// It captures the pattern content so the caller can convert glob wildcards.
	// Group 1: the pattern text inside the single quotes.
	globRe = regexp.MustCompile(`(?i)\bGLOB\s+'([^']*)'`)

	// autoincrementRe matches the AUTOINCREMENT keyword as a whole word.
	autoincrementRe = regexp.MustCompile(`(?i)\bAUTOINCREMENT\b`)

	// funcRenameRe matches the function names that need renaming, capturing the
	// trailing open parenthesis so we can do a whole-word boundary match without
	// also matching substrings of longer identifiers.
	//
	// Each entry: [search-pattern, replacement] — built below.
	funcRenames = []funcRename{
		{re: regexp.MustCompile(`(?i)\bIFNULL\s*\(`), repl: "COALESCE("},
		{re: regexp.MustCompile(`(?i)\bSUBSTR\s*\(`), repl: "SUBSTRING("},
		{re: regexp.MustCompile(`(?i)\bRANDOM\s*\(\s*\)`), repl: "RAND()"},
		{re: regexp.MustCompile(`(?i)\bTOTAL\s*\(`), repl: "SUM("},
	}
)

// funcRename pairs a compiled regex with its replacement string.
type funcRename struct {
	re   *regexp.Regexp
	repl string
}

// Translator converts SQLite SQL dialect to go-mysql-server (GMS) compatible
// SQL using a sequential transformation pipeline. Each rule is applied in
// isolation to the output of the previous rule.
type Translator struct{}

// NewTranslator creates a new SQLite→GMS Translator.
func NewTranslator() *Translator {
	return &Translator{}
}

// Translate converts a SQLite SQL statement to GMS-compatible SQL.
//
// If the query is a PRAGMA statement, Translate returns ("", *PragmaQuery).
// The caller must type-assert the error to *PragmaQuery and handle the PRAGMA
// outside of the SQL engine.
//
// All other queries pass through the transformation pipeline and the
// translated SQL is returned with a nil error.
//
// An EmptyQueryError is returned for blank input.
func (t *Translator) Translate(query string) (string, error) {
	if strings.TrimSpace(query) == "" {
		return "", &EmptyQueryError{}
	}

	// Step 1: Detect PRAGMA before any other transformation. SQLite PRAGMA
	// statements are not valid SQL and must not be forwarded to GMS.
	if pq := detectPragma(query); pq != nil {
		return "", pq
	}

	result := query

	// Step 2: Convert double-quoted identifiers to backtick-quoted identifiers
	// while leaving single-quoted string content untouched.
	result = convertDoubleQuotedIdentifiers(result)

	// Step 3: Convert string concatenation operator || to CONCAT() calls.
	// Chained concatenations are folded right-to-left.
	result = convertStringConcat(result)

	// Step 4: Rename SQLite-specific functions to their GMS equivalents.
	result = renameFunctions(result)

	// Step 5: Convert GLOB patterns to LIKE patterns.
	result = convertGlob(result)

	// Step 6: Strip the AUTOINCREMENT keyword; GMS uses AUTO_INCREMENT in DDL
	// and does not recognise the SQLite spelling.
	result = stripAutoincrement(result)

	return result, nil
}

// detectPragma reports whether query is a PRAGMA statement. If it is, a
// *PragmaQuery describing the name and optional value is returned; otherwise
// nil is returned.
func detectPragma(query string) *PragmaQuery {
	m := pragmaRe.FindStringSubmatch(strings.TrimSpace(query))
	if m == nil {
		return nil
	}
	value := strings.Trim(m[2], "'\"")
	return &PragmaQuery{Name: m[1], Value: value}
}

// convertDoubleQuotedIdentifiers replaces double-quoted SQL identifiers with
// backtick-quoted identifiers. It uses a character-by-character state machine
// that skips over single-quoted string literals so that double quotes embedded
// inside string values are never modified.
//
// SQLite allows double-quoted identifiers (ANSI SQL style). GMS uses backticks.
func convertDoubleQuotedIdentifiers(query string) string {
	var b strings.Builder
	b.Grow(len(query))

	i := 0
	for i < len(query) {
		ch := query[i]

		switch ch {
		case '\'':
			// Copy single-quoted string literal verbatim.
			b.WriteByte(ch)
			i++
			for i < len(query) {
				c := query[i]
				b.WriteByte(c)
				i++
				if c == '\'' {
					// Handle escaped single-quote ('').
					if i < len(query) && query[i] == '\'' {
						b.WriteByte(query[i])
						i++
						continue
					}
					break
				}
			}

		case '"':
			// Replace double-quoted identifier with backtick-quoted identifier.
			b.WriteByte('`')
			i++
			for i < len(query) {
				c := query[i]
				if c == '"' {
					// Handle escaped double-quote ("").
					if i+1 < len(query) && query[i+1] == '"' {
						b.WriteByte('"')
						i += 2
						continue
					}
					break
				}
				b.WriteByte(c)
				i++
			}
			b.WriteByte('`')
			if i < len(query) {
				i++ // skip closing "
			}

		default:
			b.WriteByte(ch)
			i++
		}
	}

	return b.String()
}

// convertStringConcat replaces the SQLite || string-concatenation operator
// with CONCAT() function calls. Chained concatenation (a || b || c) is
// converted right-to-left, producing CONCAT(a, CONCAT(b, c)).
//
// Strategy: scan for || operators outside of quoted strings, collect all the
// operand positions in a chain, then fold right-to-left. For each chain we:
//  1. Walk the query left-to-right, tracking quoted contexts.
//  2. When a || is found, record the current operand start/end and the || position.
//  3. After collecting all operands in the chain, emit the right-to-left folded
//     CONCAT expression and then the remainder of the query.
//
// Each "chain" is a maximal sequence of || operators not separated by commas,
// semicolons, or unbalanced parentheses at depth 0. In practice, users write:
//
//	SELECT a || b || c FROM t
//	SELECT IFNULL(x || y, 'default') FROM t
func convertStringConcat(query string) string {
	if !strings.Contains(query, "||") {
		return query
	}

	var result strings.Builder
	result.Grow(len(query) + 32)

	i := 0
	for i < len(query) {
		// Attempt to start a concat chain. We look for a token followed by ||.
		// Scan forward: copy characters to result until we hit a || outside quotes.
		// When we find ||, backtrack in the result buffer to extract the left
		// operand, then read ahead for all chained operands.

		ch := query[i]

		// Pass through single-quoted strings verbatim.
		if ch == '\'' {
			result.WriteByte(ch)
			i++
			for i < len(query) {
				c := query[i]
				result.WriteByte(c)
				i++
				if c == '\'' {
					if i < len(query) && query[i] == '\'' {
						result.WriteByte(query[i])
						i++
						continue
					}
					break
				}
			}
			continue
		}

		// Pass through backtick-quoted identifiers verbatim.
		if ch == '`' {
			result.WriteByte(ch)
			i++
			for i < len(query) && query[i] != '`' {
				result.WriteByte(query[i])
				i++
			}
			if i < len(query) {
				result.WriteByte(query[i])
				i++
			}
			continue
		}

		// Detect || operator.
		if ch == '|' && i+1 < len(query) && query[i+1] == '|' {
			// Extract the left operand: it is the last whitespace-delimited
			// token already written to result.
			soFar := result.String()
			leftToken, leftStart := extractConcatToken(soFar)

			// Consume the || operator and any surrounding whitespace.
			i += 2
			for i < len(query) && (query[i] == ' ' || query[i] == '\t') {
				i++
			}

			// Collect all operands in this chain. The first operand is leftToken.
			// Read subsequent operands separated by ||.
			operands := []string{leftToken}
			for {
				// Read the next operand.
				tok, end := readConcatOperand(query, i)
				if tok == "" {
					break
				}
				operands = append(operands, tok)
				i = end

				// Peek ahead for another || operator, preserving the
				// position so that trailing whitespace is not consumed when
				// there is no following ||.
				saved := i
				for i < len(query) && (query[i] == ' ' || query[i] == '\t') {
					i++
				}

				// Check for another || operator.
				if i+1 < len(query) && query[i] == '|' && query[i+1] == '|' {
					i += 2
					for i < len(query) && (query[i] == ' ' || query[i] == '\t') {
						i++
					}
					continue
				}
				// No following ||: restore position so the outer loop emits
				// the whitespace and remaining SQL correctly.
				i = saved
				break
			}

			// Fold operands right-to-left: CONCAT(a, CONCAT(b, c)).
			expr := operands[len(operands)-1]
			for k := len(operands) - 2; k >= 0; k-- {
				expr = "CONCAT(" + operands[k] + ", " + expr + ")"
			}

			// Write everything in result up to the start of the left token,
			// then the folded expression.
			result.Reset()
			result.WriteString(soFar[:leftStart])
			result.WriteString(expr)
			continue
		}

		result.WriteByte(ch)
		i++
	}

	return result.String()
}

// extractConcatToken returns the last SQL token in s and the byte index where
// it starts. A "token" here is a single SQL term: an identifier, string
// literal, numeric literal, function call, or parenthesised expression. The
// scan walks backward from the end of s, skipping trailing whitespace.
func extractConcatToken(s string) (token string, start int) {
	j := len(s) - 1
	// Skip trailing whitespace.
	for j >= 0 && (s[j] == ' ' || s[j] == '\t') {
		j--
	}
	if j < 0 {
		return "", 0
	}
	end := j + 1

	// Parenthesised expression or function call ending with ')'.
	if s[j] == ')' {
		depth := 0
		for j >= 0 {
			if s[j] == ')' {
				depth++
			} else if s[j] == '(' {
				depth--
				if depth == 0 {
					break
				}
			}
			j--
		}
		// j is now at the matching '('. Scan back to include the function name.
		j--
		for j >= 0 && isIdentOrDotChar(s[j]) {
			j--
		}
		return s[j+1 : end], j + 1
	}

	// Single-quoted string.
	if s[j] == '\'' {
		j--
		for j >= 0 {
			if s[j] == '\'' {
				if j > 0 && s[j-1] == '\'' {
					j -= 2
					continue
				}
				break
			}
			j--
		}
		return s[j:end], j
	}

	// Backtick-quoted identifier.
	if s[j] == '`' {
		j--
		for j >= 0 && s[j] != '`' {
			j--
		}
		return s[j:end], j
	}

	// Bare identifier or numeric literal: stop at whitespace or punctuation.
	for j >= 0 && isIdentOrDotChar(s[j]) {
		j--
	}
	return s[j+1 : end], j + 1
}

// readConcatOperand reads a single SQL token from query starting at position
// start. It returns the token text and the index one past the token's last
// character. Returns ("", start) if no token can be read.
func readConcatOperand(query string, start int) (string, int) {
	i := start
	if i >= len(query) {
		return "", start
	}

	ch := query[i]

	// Single-quoted string.
	if ch == '\'' {
		j := i + 1
		for j < len(query) {
			if query[j] == '\'' {
				j++
				if j < len(query) && query[j] == '\'' {
					j++
					continue
				}
				break
			}
			j++
		}
		return query[i:j], j
	}

	// Backtick-quoted identifier.
	if ch == '`' {
		j := i + 1
		for j < len(query) && query[j] != '`' {
			j++
		}
		if j < len(query) {
			j++
		}
		return query[i:j], j
	}

	// Parenthesised expression.
	if ch == '(' {
		depth := 0
		j := i
		for j < len(query) {
			if query[j] == '(' {
				depth++
			} else if query[j] == ')' {
				depth--
				if depth == 0 {
					j++
					break
				}
			}
			j++
		}
		return query[i:j], j
	}

	// Identifier or function call (identifier optionally followed by (...)).
	if isIdentStartChar(ch) || (ch >= '0' && ch <= '9') {
		j := i
		for j < len(query) && isIdentOrDotChar(query[j]) {
			j++
		}
		// Check for function call parentheses.
		// Skip whitespace between name and (.
		k := j
		for k < len(query) && (query[k] == ' ' || query[k] == '\t') {
			k++
		}
		if k < len(query) && query[k] == '(' {
			depth := 0
			for k < len(query) {
				if query[k] == '(' {
					depth++
				} else if query[k] == ')' {
					depth--
					if depth == 0 {
						k++
						break
					}
				}
				k++
			}
			return query[i:k], k
		}
		return query[i:j], j
	}

	return "", start
}

// isIdentOrDotChar reports whether ch is valid inside a SQL identifier or
// numeric literal, including dot for schema-qualified names.
func isIdentOrDotChar(ch byte) bool {
	return ch == '_' || ch == '.' || ch == '$' ||
		(ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
		(ch >= '0' && ch <= '9')
}

// isIdentStartChar reports whether ch can start a SQL identifier.
func isIdentStartChar(ch byte) bool {
	return ch == '_' || ch == '$' ||
		(ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z')
}

// renameFunctions performs case-insensitive whole-word replacement of SQLite
// function names with their GMS equivalents. Replacements operate on the full
// query string; because the patterns include the opening parenthesis they
// cannot accidentally match bare identifiers that share a prefix.
//
// Renamed functions:
//   - IFNULL( → COALESCE(
//   - SUBSTR(  → SUBSTRING(
//   - RANDOM() → RAND()
//   - TOTAL(   → SUM(
func renameFunctions(query string) string {
	for _, r := range funcRenames {
		query = r.re.ReplaceAllStringFunc(query, func(match string) string {
			return r.repl
		})
	}
	return query
}

// convertGlob replaces SQLite GLOB expressions with SQL LIKE expressions,
// converting glob wildcard characters to their LIKE equivalents:
//   - * → %  (match any sequence of characters)
//   - ? → _  (match exactly one character)
//
// Only the pattern content inside the single quotes is modified; the keyword
// GLOB is replaced with LIKE.
func convertGlob(query string) string {
	return globRe.ReplaceAllStringFunc(query, func(match string) string {
		// Extract the pattern from inside the quotes.
		sub := globRe.FindStringSubmatch(match)
		if sub == nil {
			return match
		}
		pattern := sub[1]
		pattern = strings.ReplaceAll(pattern, "*", "%")
		pattern = strings.ReplaceAll(pattern, "?", "_")
		return "LIKE '" + pattern + "'"
	})
}

// stripAutoincrement removes all occurrences of the AUTOINCREMENT keyword
// (case-insensitive, whole-word). GMS does not support the SQLite spelling;
// the AUTO_INCREMENT attribute is set separately in DDL when needed.
func stripAutoincrement(query string) string {
	result := autoincrementRe.ReplaceAllString(query, "")
	// Collapse any double spaces that stripping may have introduced.
	for strings.Contains(result, "  ") {
		result = strings.ReplaceAll(result, "  ", " ")
	}
	return strings.TrimSpace(result)
}
