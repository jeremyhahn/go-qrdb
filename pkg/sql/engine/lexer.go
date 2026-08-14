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

// regionKind identifies the lexical class of a protected region found by
// scanRegions.
type regionKind int

const (
	// regionStringLiteral is a plain single-quoted string literal,
	// including any doubled '' escapes.
	regionStringLiteral regionKind = iota
	// regionEscapeStringLiteral is a PostgreSQL E'...' (or e'...') escape
	// string literal. Its region includes the leading E/e.
	regionEscapeStringLiteral
	// regionDollarQuoted is a PostgreSQL dollar-quoted string, e.g. $$...$$
	// or $tag$...$tag$.
	regionDollarQuoted
	// regionLineComment is a -- comment, running to end of line or end of
	// input.
	regionLineComment
	// regionBlockComment is a /* ... */ comment. PostgreSQL nests these,
	// so scanRegions tracks nesting depth.
	regionBlockComment
)

// region is a byte range of query, [start, end), that Translate's
// transformations must treat as opaque: a string literal, comment, or
// dollar-quoted string whose content is caller data or documentation, not
// SQL to be rewritten.
type region struct {
	kind       regionKind
	start, end int
}

// scanRegions performs a single left-to-right lexical scan of query and
// returns every protected region it contains, in order: single-quoted
// string literals (plain and E-prefixed), dollar-quoted strings, line
// comments, and block comments (which PostgreSQL allows to nest). Every
// literal-aware transformation in this package re-scans its input through
// scanRegions before rewriting anything, so a rewrite is never applied to
// text inside one of these regions. scanRegions is a lexer, not a parser:
// it recognizes these four token shapes and nothing else about SQL
// grammar.
func scanRegions(query string) []region {
	var regions []region
	n := len(query)
	i := 0
	for i < n {
		switch {
		case query[i] == '\'':
			start := i
			kind := regionStringLiteral
			if i > 0 && (query[i-1] == 'E' || query[i-1] == 'e') && (i == 1 || !isIdentChar(query[i-2])) {
				kind = regionEscapeStringLiteral
				start = i - 1
			}
			i++
			for i < n {
				if query[i] == '\'' {
					i++
					if i < n && query[i] == '\'' {
						i++
						continue
					}
					break
				}
				i++
			}
			regions = append(regions, region{kind: kind, start: start, end: i})

		case query[i] == '-' && i+1 < n && query[i+1] == '-':
			start := i
			for i < n && query[i] != '\n' {
				i++
			}
			regions = append(regions, region{kind: regionLineComment, start: start, end: i})

		case query[i] == '/' && i+1 < n && query[i+1] == '*':
			start := i
			i += 2
			depth := 1
			for i < n && depth > 0 {
				switch {
				case i+1 < n && query[i] == '/' && query[i+1] == '*':
					depth++
					i += 2
				case i+1 < n && query[i] == '*' && query[i+1] == '/':
					depth--
					i += 2
				default:
					i++
				}
			}
			regions = append(regions, region{kind: regionBlockComment, start: start, end: i})

		case query[i] == '$':
			if tag, contentStart, ok := scanDollarTag(query, i); ok {
				start := i
				closer := "$" + tag + "$"
				if p := strings.Index(query[contentStart:], closer); p >= 0 {
					i = contentStart + p + len(closer)
				} else {
					i = n
				}
				regions = append(regions, region{kind: regionDollarQuoted, start: start, end: i})
			} else {
				i++
			}

		default:
			i++
		}
	}
	return regions
}

// scanDollarTag reports whether query[i:] opens a PostgreSQL dollar-quote
// delimiter -- $$ or $tag$, where tag follows unquoted-identifier rules
// (must start with a letter or underscore) -- and, if so, returns the tag
// and the offset immediately following the opening delimiter.
func scanDollarTag(query string, i int) (tag string, contentStart int, ok bool) {
	n := len(query)
	j := i + 1
	if j < n && query[j] == '$' {
		return "", j + 1, true
	}
	if j < n && (isAlpha(query[j]) || query[j] == '_') {
		k := j
		for k < n && isIdentChar(query[k]) {
			k++
		}
		if k < n && query[k] == '$' {
			return query[j:k], k + 1, true
		}
	}
	return "", 0, false
}

// isAlpha reports whether ch is an ASCII letter.
func isAlpha(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z')
}

// regionContainsPos reports whether pos falls inside one of regions, which
// must be sorted by start; scanRegions returns them in scan order, which
// is already sorted.
func regionContainsPos(regions []region, pos int) bool {
	for _, r := range regions {
		if r.start > pos {
			break
		}
		if pos < r.end {
			return true
		}
	}
	return false
}

// applyOutsideRegions runs re against query and rewrites every match using
// template -- interpreted the same way as (*regexp.Regexp).Expand, so $1,
// $2, and so on refer to re's capture groups -- unless the match starts
// inside a protected region (see scanRegions). A match that starts inside
// a protected region is left byte-for-byte untouched, so a string
// literal, comment, or dollar-quoted string whose content merely contains
// re's trigger text is never rewritten.
func applyOutsideRegions(query string, re *regexp.Regexp, template string) string {
	matches := re.FindAllStringSubmatchIndex(query, -1)
	if matches == nil {
		return query
	}

	regions := scanRegions(query)

	var b strings.Builder
	b.Grow(len(query))
	last := 0
	for _, m := range matches {
		start, end := m[0], m[1]
		if regionContainsPos(regions, start) {
			continue
		}
		b.WriteString(query[last:start])
		b.Write(re.ExpandString(nil, template, query, m))
		last = end
	}
	b.WriteString(query[last:])
	return b.String()
}

// findOutsideRegions returns the [start, end) match indices of the first
// match of re in query that starts outside a protected region (see
// scanRegions), or nil if re has no such match. It exists for detection
// only -- locating a keyword such as ON CONFLICT or RETURNING -- without
// being fooled by a string literal, comment, or dollar-quoted string that
// merely contains the same text.
func findOutsideRegions(query string, re *regexp.Regexp) []int {
	matches := re.FindAllStringIndex(query, -1)
	if matches == nil {
		return nil
	}

	regions := scanRegions(query)
	for _, m := range matches {
		if !regionContainsPos(regions, m[0]) {
			return m
		}
	}
	return nil
}

// applyEscapeStrings rewrites every PostgreSQL E'...' (or e'...') escape
// string literal into a plain '...' literal by dropping the leading E.
// The literal's content, including any doubled-apostrophe escapes, is
// copied through unchanged. A plain string literal, comment, or
// dollar-quoted string is left untouched even if it happens to contain
// the text E'...'.
func applyEscapeStrings(query string) string {
	regions := scanRegions(query)

	var b strings.Builder
	b.Grow(len(query))
	last := 0
	for _, r := range regions {
		if r.kind != regionEscapeStringLiteral {
			continue
		}
		b.WriteString(query[last:r.start])
		b.WriteString(query[r.start+1 : r.end]) // drop the leading E/e
		last = r.end
	}
	b.WriteString(query[last:])
	return b.String()
}

// castMaskByte is the sentinel byte maskForCast substitutes for protected
// content. It is a non-printable control byte, so it can never satisfy
// castRe's quote, backtick, identifier, digit, or placeholder
// alternatives.
const castMaskByte = 0x01

// maskForCast returns a same-length copy of query in which every
// protected region's content is replaced by castMaskByte, with one
// exception: a plain string literal's own opening and closing quote
// characters are left in place. That exception lets castRe's own
// string-literal alternative still recognize a literal used directly as a
// cast target (e.g. '2024-01-01'::date) at its true, correctly-escaped
// boundary, while the masked interior guarantees no other alternative in
// castRe can match text that merely appears inside the literal's value.
func maskForCast(query string, regions []region) []byte {
	buf := []byte(query)
	for _, r := range regions {
		if r.kind == regionStringLiteral {
			for i := r.start + 1; i < r.end-1; i++ {
				buf[i] = castMaskByte
			}
			continue
		}
		for i := r.start; i < r.end; i++ {
			buf[i] = castMaskByte
		}
	}
	return buf
}

// applyCast rewrites PostgreSQL value::type cast syntax into
// CAST(value AS type). It matches castRe against the masked view built by
// maskForCast rather than query itself, so a cast-shaped substring that
// merely appears inside a string literal, comment, or dollar-quoted
// string can never match; every replacement it does produce is expanded
// from the real, unmasked query text.
func applyCast(query string) string {
	regions := scanRegions(query)
	masked := string(maskForCast(query, regions))

	matches := castRe.FindAllStringSubmatchIndex(masked, -1)
	if matches == nil {
		return query
	}

	var b strings.Builder
	b.Grow(len(query))
	last := 0
	for _, m := range matches {
		start, end := m[0], m[1]
		b.WriteString(query[last:start])
		b.Write(castRe.ExpandString(nil, "CAST($1 AS $2)", query, m))
		last = end
	}
	b.WriteString(query[last:])
	return b.String()
}
