//go:build qrdb_sql

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

package lockclause

import "strings"

// tokenKind classifies a token.
type tokenKind uint8

const (
	// tokWord is a bare or backtick-quoted identifier or keyword.
	tokWord tokenKind = iota
	// tokPunct is any other single significant byte.
	tokPunct
)

// token is one significant token: string literals, double-quoted strings
// and comments are skipped entirely, so a clause spelled inside one is never
// matched.
type token struct {
	kind       tokenKind
	text       string
	quoted     bool
	start, end int
}

// is reports whether t is the bare keyword kw, case-insensitively. A
// backtick-quoted identifier is never a keyword.
func (t token) is(kw string) bool {
	return t.kind == tokWord && !t.quoted && strings.EqualFold(t.text, kw)
}

// ident returns the identifier t names, without its quoting.
func (t token) ident() string {
	if !t.quoted {
		return t.text
	}
	return strings.ReplaceAll(t.text, "``", "`")
}

// tokenize scans query into significant tokens, MySQL-dialect: '...' and
// "..." are string literals (with backslash escapes and doubled quotes),
// `...` is a quoted identifier, and --, # and /* */ start comments.
func tokenize(query string) []token {
	var toks []token
	n := len(query)
	i := 0
	for i < n {
		c := query[i]
		switch {
		case c == '\'' || c == '"':
			i = skipString(query, i)
		case c == '`':
			start := i
			j := i + 1
			for j < n {
				if query[j] == '`' {
					if j+1 < n && query[j+1] == '`' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			end := min(j+1, n)
			toks = append(toks, token{kind: tokWord, text: query[start+1 : min(j, n)], quoted: true, start: start, end: end})
			i = end
		case c == '#':
			i = skipLine(query, i)
		case c == '-' && i+1 < n && query[i+1] == '-':
			i = skipLine(query, i)
		case c == '/' && i+1 < n && query[i+1] == '*':
			j := strings.Index(query[i+2:], "*/")
			if j < 0 {
				i = n
			} else {
				i += 2 + j + 2
			}
		case isWordByte(c):
			start := i
			for i < n && isWordByte(query[i]) {
				i++
			}
			toks = append(toks, token{kind: tokWord, text: query[start:i], start: start, end: i})
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f':
			i++
		default:
			toks = append(toks, token{kind: tokPunct, text: query[i : i+1], start: i, end: i + 1})
			i++
		}
	}
	return toks
}

// skipString returns the offset just past the string literal opening at i.
func skipString(query string, i int) int {
	quote := query[i]
	n := len(query)
	j := i + 1
	for j < n {
		switch query[j] {
		case '\\':
			j += 2
			continue
		case quote:
			if j+1 < n && query[j+1] == quote {
				j += 2
				continue
			}
			return j + 1
		}
		j++
	}
	return n
}

// skipLine returns the offset of the end of the line containing i.
func skipLine(query string, i int) int {
	if j := strings.IndexByte(query[i:], '\n'); j >= 0 {
		return i + j
	}
	return len(query)
}

// isWordByte reports whether c can be part of a bare identifier or keyword.
func isWordByte(c byte) bool {
	return c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c >= 0x80
}
