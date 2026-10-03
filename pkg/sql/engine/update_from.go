//go:build qrdb_sql

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

// updateFromLeadRe matches the head of PostgreSQL's UPDATE ... FROM form:
// UPDATE <target> SET. It runs after identifier-quoting translation, so the
// target is either backtick-quoted or bare.
var updateFromLeadRe = regexp.MustCompile(
	"(?is)^\\s*UPDATE\\s+(`[^`]+`|[A-Za-z_][A-Za-z0-9_.]*)\\s+SET\\s")

// translateUpdateFrom rewrites PostgreSQL's
//
//	UPDATE t SET assignments FROM others WHERE cond
//
// into MySQL's multi-table form
//
//	UPDATE t, others SET assignments WHERE cond
//
// which is the same join-update: PostgreSQL's FROM list joins extra
// relations the SET and WHERE clauses may reference, exactly what MySQL
// spells by listing every relation after UPDATE. Each unqualified
// assignment target is qualified with the target table, because in the
// multi-table form an assignment names a column of ANY listed relation and
// MySQL would otherwise report an ambiguity PostgreSQL never had --
// PostgreSQL's assignment targets always belong to the target table.
//
// The FROM and WHERE keywords are located at parenthesis depth zero outside
// string literals and comments, so a subquery's own FROM/WHERE never splits
// the statement. A statement without a top-level FROM is returned
// unchanged.
func translateUpdateFrom(query string) string {
	lead := updateFromLeadRe.FindStringSubmatchIndex(query)
	if lead == nil {
		return query
	}
	target := query[lead[2]:lead[3]]
	setStart := lead[1]

	fromKw, fromEnd := findTopLevelKeyword(query, setStart, "FROM")
	if fromKw < 0 {
		return query
	}
	whereKw, _ := findTopLevelKeyword(query, fromEnd, "WHERE")

	assignments := strings.TrimSpace(query[setStart:fromKw])
	var fromList, rest string
	if whereKw < 0 {
		fromList = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(query[fromEnd:]), ";"))
	} else {
		fromList = strings.TrimSpace(query[fromEnd:whereKw])
		rest = " " + strings.TrimSpace(query[whereKw:])
	}
	if fromList == "" {
		return query
	}

	return "UPDATE " + target + ", " + fromList +
		" SET " + qualifyAssignments(target, assignments) + rest
}

// qualifyAssignments prefixes each top-level assignment's unqualified
// column with the target table. An already-qualified target (one whose
// left-hand side contains a dot before its top-level =) is left as
// written.
func qualifyAssignments(target, assignments string) string {
	parts := splitTopLevel(assignments, ',')
	for i, part := range parts {
		trimmed := strings.TrimSpace(part)
		eq := indexTopLevel(trimmed, '=')
		if eq < 0 || strings.Contains(trimmed[:eq], ".") {
			parts[i] = trimmed
			continue
		}
		parts[i] = target + "." + strings.TrimSpace(trimmed[:eq]) + trimmed[eq:]
	}
	return strings.Join(parts, ", ")
}

// findTopLevelKeyword returns the [start, end) of the first occurrence of
// keyword in query at or after from that sits at parenthesis depth zero,
// outside every protected region (string literals, comments, dollar-quoted
// strings) and outside backtick-quoted identifiers, on word boundaries.
// It returns (-1, -1) when there is none.
func findTopLevelKeyword(query string, from int, keyword string) (int, int) {
	regions := scanRegions(query)
	depth := 0
	inBacktick := false
	n, k := len(query), len(keyword)
	for i := from; i < n; i++ {
		if regionContainsPos(regions, i) {
			continue
		}
		ch := query[i]
		if ch == '`' {
			inBacktick = !inBacktick
			continue
		}
		if inBacktick {
			continue
		}
		switch ch {
		case '(':
			depth++
		case ')':
			depth--
		default:
			if depth == 0 && i+k <= n && strings.EqualFold(query[i:i+k], keyword) &&
				(i == 0 || !isIdentChar(query[i-1])) &&
				(i+k == n || !isIdentChar(query[i+k])) {
				return i, i + k
			}
		}
	}
	return -1, -1
}

// splitTopLevel splits s on sep occurrences at parenthesis depth zero
// outside quotes and backtick identifiers.
func splitTopLevel(s string, sep byte) []string {
	regions := scanRegions(s)
	depth := 0
	inBacktick := false
	var parts []string
	last := 0
	for i := 0; i < len(s); i++ {
		if regionContainsPos(regions, i) {
			continue
		}
		switch ch := s[i]; {
		case ch == '`':
			inBacktick = !inBacktick
		case inBacktick:
		case ch == '(':
			depth++
		case ch == ')':
			depth--
		case ch == sep && depth == 0:
			parts = append(parts, s[last:i])
			last = i + 1
		}
	}
	return append(parts, s[last:])
}

// indexTopLevel returns the index of the first sep in s at parenthesis
// depth zero outside quotes and backtick identifiers, or -1.
func indexTopLevel(s string, sep byte) int {
	regions := scanRegions(s)
	depth := 0
	inBacktick := false
	for i := 0; i < len(s); i++ {
		if regionContainsPos(regions, i) {
			continue
		}
		switch ch := s[i]; {
		case ch == '`':
			inBacktick = !inBacktick
		case inBacktick:
		case ch == '(':
			depth++
		case ch == ')':
			depth--
		case ch == sep && depth == 0:
			return i
		}
	}
	return -1
}

// emptyCreateTableRe matches CREATE TABLE with an EMPTY column list, which
// PostgreSQL permits and MySQL's grammar refuses outright. It runs after
// identifier-quoting translation, so the name is backtick-quoted or bare.
var emptyCreateTableRe = regexp.MustCompile(
	"(?is)^(\\s*CREATE\\s+TABLE\\s+(?:IF\\s+NOT\\s+EXISTS\\s+)?(?:`[^`]+`|[A-Za-z_][A-Za-z0-9_.]*))\\s*\\(\\s*\\)\\s*;?\\s*$")

// zeroColumnName is the placeholder column a zero-column CREATE TABLE is
// given so the engine's grammar accepts it. gorm's AutoMigrate then adds
// the model's real columns beside it; scanning ignores columns a struct
// does not name, so the placeholder is inert. The name deliberately does
// NOT start with "__": that prefix is reserved by the service layer's
// column-name validation.
const zeroColumnName = "qrdb_zero_column"

// translateEmptyCreateTable rewrites PostgreSQL's zero-column CREATE TABLE
// into one declaring a single inert placeholder column.
func translateEmptyCreateTable(query string) string {
	groups := emptyCreateTableRe.FindStringSubmatch(query)
	if groups == nil {
		return query
	}
	return groups[1] + " (`" + zeroColumnName + "` TINYINT NULL)"
}

// bareCurrentTimestampDefaultRe matches DEFAULT current_timestamp (no
// parentheses) immediately following a DATETIME(p) type produced by the
// timestamp translation, capturing the precision.
var bareCurrentTimestampDefaultRe = regexp.MustCompile(
	`(?i)(DATETIME\(([0-9])\)(?:\s+NOT\s+NULL|\s+NULL)?\s+DEFAULT\s+)current_timestamp\b([^(]|$)`)

// alignCurrentTimestampPrecision rewrites DEFAULT current_timestamp to
// carry the fractional-seconds precision of the DATETIME column it
// defaults, which MySQL requires to match: PostgreSQL's plain timestamp
// translates to DATETIME(6) (see pgColumnTypes), and the engine refuses
// DATETIME(6) DEFAULT CURRENT_TIMESTAMP because the bare function is
// precision zero.
func alignCurrentTimestampPrecision(query string) string {
	return bareCurrentTimestampDefaultRe.ReplaceAllString(query, "${1}CURRENT_TIMESTAMP(${2})${3}")
}

// bareNextvalDefaultRe matches DEFAULT nextval(...) with no wrapping
// parentheses -- the spelling PostgreSQL's SET DEFAULT uses -- capturing
// the call. MySQL's grammar requires an expression default to be
// parenthesized.
var bareNextvalDefaultRe = regexp.MustCompile(`(?i)\b(DEFAULT\s+)(nextval\s*\((?:[^()]|\([^()]*\))*\))`)

// wrapNextvalDefault parenthesizes DEFAULT nextval(...) so the engine's
// grammar accepts it as an expression default.
func wrapNextvalDefault(query string) string {
	return applyOutsideRegions(query, bareNextvalDefaultRe, "${1}(${2})")
}

// publicSchemaQualifierRe matches a public. schema qualifier ahead of an
// identifier -- quoted or bare -- in either quoting style.
var publicSchemaQualifierRe = regexp.MustCompile("(?i)(?:`public`|\\bpublic)\\s*\\.\\s*([`A-Za-z_])")

// StripPublicSchemaQualifier removes PostgreSQL's public. schema qualifier:
// the public schema and the session's current database are the same level
// of the hierarchy on this engine (current_schema() already translates to
// DATABASE() for the same reason), so a public-qualified name resolves to
// the bare name. String literals and comments are never touched.
func StripPublicSchemaQualifier(query string) string {
	return applyOutsideRegions(query, publicSchemaQualifierRe, "${1}")
}

// SplitTopLevelStatements splits query on the semicolons that separate
// top-level statements -- outside string literals, comments, dollar-quoted
// strings, and parentheses -- dropping empty fragments. A query with no
// top-level semicolon (or only a trailing one) returns a single element.
func SplitTopLevelStatements(query string) []string {
	parts := splitTopLevel(query, ';')
	statements := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			statements = append(statements, trimmed)
		}
	}
	if len(statements) == 0 {
		return []string{""}
	}
	return statements
}
