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

import "strings"

// pgColumnType describes how one PostgreSQL column type is rewritten into
// the go-mysql-server type that stands in for it. Only the types a real
// PostgreSQL client has been measured emitting against this server are
// listed (see pgColumnTypes); a type with no entry is passed through
// untouched, so every MySQL-spelled type -- BIGINT, INTEGER, SMALLINT,
// DECIMAL, NUMERIC(p,s), BOOLEAN, VARCHAR(n), CHAR(n) -- which PostgreSQL
// and MySQL already spell identically needs no entry and gets none.
type pgColumnType struct {
	// Name is the go-mysql-server type substituted for the PostgreSQL
	// spelling, including any fixed length modifier of its own.
	Name string

	// AutoIncrement reports whether the column additionally gains MySQL's
	// AUTO_INCREMENT attribute. It is set for exactly the PostgreSQL
	// serial pseudo-types, which are not types at all in PostgreSQL but
	// shorthand for an integer column defaulted from an owned sequence.
	AutoIncrement bool

	// KeepModifier reports whether a parenthesized modifier following the
	// PostgreSQL type name -- timestamptz(6) -- is carried across onto
	// Name. When it is false and the source names a modifier anyway, the
	// rewrite fails with a typed error rather than dropping a modifier the
	// substituted type cannot express.
	KeepModifier bool
}

// pgColumnTypes maps a PostgreSQL type name, lowercased and with interior
// whitespace collapsed to single spaces, to its go-mysql-server stand-in.
// It is deliberately an inventory of what `gorm.io/driver/postgres` was
// measured emitting against this server across GORM's own conformance
// suite, not a transcription of PostgreSQL's type catalogue: a type nobody
// sends is a type nobody has validated the substitution for, and an
// unmapped type reaches the parser under its own name and is rejected
// there rather than being silently turned into something else.
//
// The substitutions and why each is the faithful one:
//
//   - bigserial/serial/smallserial are PostgreSQL's shorthand for an
//     integer column whose default comes from an owned sequence. MySQL
//     spells the same intent as an integer column with AUTO_INCREMENT, so
//     each maps to the integer width PostgreSQL gives it plus that
//     attribute. A table naming two serial columns is therefore rejected
//     by the engine (MySQL permits one auto-increment column per table)
//     rather than quietly losing the second column's default -- see
//     TestTranslateDDL_MultipleSerialColumns.
//
//   - text is PostgreSQL's unbounded string. MySQL's TEXT caps at 64 KiB,
//     so the honest stand-in is LONGTEXT, which is also what
//     gorm.io/driver/mysql itself picks for the same unbounded field.
//     bytea maps to LONGBLOB for exactly the same reason.
//
//   - timestamptz and timestamp both map to DATETIME rather than MySQL's
//     TIMESTAMP: MySQL's TIMESTAMP only spans 1970-2038, while DATETIME
//     spans years 1000-9999 and so can actually hold the range a
//     PostgreSQL client may send. Neither MySQL type carries a zone, so
//     no zone fidelity is lost relative to the alternative.
//
//   - uuid has no MySQL counterpart. CHAR(36) is the canonical MySQL
//     representation of a UUID's textual form and is fixed-width, so it
//     neither truncates nor pads a well-formed value.
var pgColumnTypes = map[string]pgColumnType{
	"bigserial":                   {Name: "BIGINT", AutoIncrement: true},
	"serial":                      {Name: "INT", AutoIncrement: true},
	"smallserial":                 {Name: "SMALLINT", AutoIncrement: true},
	"text":                        {Name: "LONGTEXT"},
	"bytea":                       {Name: "LONGBLOB"},
	"uuid":                        {Name: "CHAR(36)"},
	"timestamptz":                 {Name: "DATETIME", KeepModifier: true},
	"timestamp":                   {Name: "DATETIME", KeepModifier: true},
	"timestamp without time zone": {Name: "DATETIME", KeepModifier: true},
	"timestamp with time zone":    {Name: "DATETIME", KeepModifier: true},
}

// maxTypePhraseWords is the greatest number of whitespace-separated words
// any key of pgColumnTypes spans ("timestamp without time zone").
const maxTypePhraseWords = 4

// tableConstraintKeywords are the leading keywords that mark an element of
// a CREATE TABLE column list, or a clause of an ALTER TABLE, as a table
// constraint rather than a column definition. An element starting with one
// of these names no column and therefore contains no column type position;
// skipping it is what keeps a constraint named `serial` from being mistaken
// for a serial column. All keys are upper case.
var tableConstraintKeywords = map[string]bool{
	"CONSTRAINT": true,
	"PRIMARY":    true,
	"UNIQUE":     true,
	"FOREIGN":    true,
	"CHECK":      true,
	"KEY":        true,
	"INDEX":      true,
	"FULLTEXT":   true,
	"SPATIAL":    true,
	"EXCLUDE":    true,
	"PERIOD":     true,
}

// alterClauseColumnOffset maps an ALTER TABLE clause's leading keyword to
// the number of identifiers that follow it before the column's type. ADD
// and MODIFY name one identifier (the column) before its type; CHANGE
// names two (the old and new column names). A clause whose keyword has no
// entry -- DROP, RENAME, ALTER, ... -- names no type and is left alone.
// The optional COLUMN noise word is consumed separately.
var alterClauseColumnOffset = map[string]int{
	"ADD":    1,
	"MODIFY": 1,
	"CHANGE": 2,
}

// ddlTokenKind classifies a token produced by scanDDLTokens.
type ddlTokenKind int

const (
	// ddlWord is a bare word: an unquoted identifier, keyword, or number.
	ddlWord ddlTokenKind = iota
	// ddlQuoted is a backtick-quoted identifier, backticks included.
	ddlQuoted
	// ddlRegion is a value-bearing protected region -- a string literal or
	// a dollar-quoted string (see scanRegions) -- carried through as one
	// opaque token. Comments are not tokenized at all; see scanDDLTokens.
	ddlRegion
	// ddlPunct is a single punctuation byte.
	ddlPunct
)

// ddlToken is one lexical token of a statement: a byte range of the source
// and the class it belongs to. Whitespace produces no token, so a token's
// end is not necessarily its successor's start, and every rewrite is
// expressed as a substitution over the ORIGINAL byte ranges rather than by
// reassembling tokens -- whitespace, casing, and comments between tokens
// survive byte-for-byte.
type ddlToken struct {
	kind       ddlTokenKind
	start, end int
}

// scanDDLTokens tokenizes query for the DDL rewrites in this file. It is
// built on scanRegions, so a string literal, comment, or dollar-quoted
// string becomes a single opaque ddlRegion token and can never be mistaken
// for an identifier, a keyword, or a type name.
func scanDDLTokens(query string) []ddlToken {
	regions := scanRegions(query)

	var toks []ddlToken
	ri, i, n := 0, 0, len(query)
	for i < n {
		if ri < len(regions) && regions[ri].start == i {
			// A comment is skipped outright rather than tokenized: it
			// carries no syntax, so emitting a token for it would put a
			// spurious element between a column's name and its type and
			// defeat every position-based lookahead below. A value-bearing
			// region (string or dollar-quoted literal) IS a token, because
			// it occupies a grammatical slot -- a DEFAULT's value, say --
			// and must not be mistaken for a type name.
			if regions[ri].kind != regionLineComment && regions[ri].kind != regionBlockComment {
				toks = append(toks, ddlToken{kind: ddlRegion, start: i, end: regions[ri].end})
			}
			i = regions[ri].end
			ri++
			continue
		}

		ch := query[i]
		switch {
		case ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r':
			i++

		case ch == '`':
			start := i
			i++
			for i < n && query[i] != '`' {
				i++
			}
			if i < n {
				i++ // closing backtick
			}
			toks = append(toks, ddlToken{kind: ddlQuoted, start: start, end: i})

		case isIdentChar(ch):
			start := i
			for i < n && isIdentChar(query[i]) {
				i++
			}
			toks = append(toks, ddlToken{kind: ddlWord, start: start, end: i})

		default:
			toks = append(toks, ddlToken{kind: ddlPunct, start: i, end: i + 1})
			i++
		}
	}
	return toks
}

// text returns the source text of t.
func (t ddlToken) text(query string) string { return query[t.start:t.end] }

// word reports t's upper-cased text when t is a bare word, and "" for
// every other token class, so a caller comparing against a keyword can
// never match a backtick-quoted identifier or a string literal that
// happens to spell the same word.
func (t ddlToken) word(query string) string {
	if t.kind != ddlWord {
		return ""
	}
	return strings.ToUpper(query[t.start:t.end])
}

// isPunct reports whether t is the single punctuation byte ch.
func (t ddlToken) isPunct(query string, ch byte) bool {
	return t.kind == ddlPunct && query[t.start] == ch
}

// ddlEdit is a substitution of the byte range [start, end) of the source
// statement with text.
type ddlEdit struct {
	start, end int
	text       string
}

// ddlRewriters dispatches a statement's leading keyword to the rewrite that
// knows where that statement's column type positions are. A statement whose
// leading keyword has no entry -- every SELECT, INSERT, UPDATE, DELETE, and
// every DDL form other than CREATE TABLE and ALTER TABLE -- is passed
// through untouched, which is what confines the type substitutions in this
// file to column-definition context and leaves a bare identifier named
// `serial`, `text`, or `uuid` anywhere else in the language alone.
var ddlRewriters = map[string]func(string, []ddlToken) ([]ddlEdit, error){
	"CREATE": createTableTypeEdits,
	"ALTER":  alterTableTypeEdits,
}

// translateDDLTypes rewrites every PostgreSQL column type appearing in a
// column-definition position of a CREATE TABLE or ALTER TABLE statement
// into the go-mysql-server type that stands in for it (see pgColumnTypes),
// and returns query unchanged for every other statement.
//
// It is a structural rewrite, not a pattern match: the statement is
// tokenized through the same protected-region lexer the rest of this
// package uses, the column list is walked by paren depth and top-level
// commas, and a substitution is only ever made at the token position where
// a column's type is required to be. A table constraint, a column NAME, a
// value inside a string literal, and any occurrence at all outside a
// CREATE/ALTER TABLE are therefore untouched even when they spell a
// PostgreSQL type name exactly.
func translateDDLTypes(query string) (string, error) {
	toks := scanDDLTokens(query)
	if len(toks) < 2 || toks[1].word(query) != "TABLE" {
		return query, nil
	}

	rewrite, ok := ddlRewriters[toks[0].word(query)]
	if !ok {
		return query, nil
	}

	edits, err := rewrite(query, toks)
	if err != nil {
		return "", err
	}
	return applyDDLEdits(query, edits), nil
}

// applyDDLEdits returns query with every edit applied. Edits are produced
// in ascending, non-overlapping source order by their callers.
func applyDDLEdits(query string, edits []ddlEdit) string {
	if len(edits) == 0 {
		return query
	}

	var b strings.Builder
	b.Grow(len(query) + 16*len(edits))
	last := 0
	for _, e := range edits {
		b.WriteString(query[last:e.start])
		b.WriteString(e.text)
		last = e.end
	}
	b.WriteString(query[last:])
	return b.String()
}

// createTableTypeEdits returns the type substitutions for a CREATE TABLE
// statement. The column list is the first parenthesized group in the
// statement -- CREATE TABLE has no other -- and its top-level,
// comma-separated elements are each either a table constraint (skipped) or
// a column definition whose second token is the type.
func createTableTypeEdits(query string, toks []ddlToken) ([]ddlEdit, error) {
	open := -1
	for i, t := range toks {
		if t.isPunct(query, '(') {
			open = i
			break
		}
	}
	if open < 0 {
		return nil, nil
	}

	var edits []ddlEdit
	depth := 1
	start := open + 1
	for i := open + 1; i < len(toks); i++ {
		switch {
		case toks[i].isPunct(query, '('):
			depth++
		case toks[i].isPunct(query, ')'):
			depth--
			if depth == 0 {
				return appendColumnTypeEdit(query, toks, edits, start, i)
			}
		case depth == 1 && toks[i].isPunct(query, ','):
			var err error
			if edits, err = appendColumnTypeEdit(query, toks, edits, start, i); err != nil {
				return nil, err
			}
			start = i + 1
		}
	}
	return edits, nil
}

// appendColumnTypeEdit appends the type substitution for the CREATE TABLE
// column-list element spanning toks[start:end), or leaves edits unchanged
// when that element is a table constraint or is empty.
func appendColumnTypeEdit(query string, toks []ddlToken, edits []ddlEdit, start, end int) ([]ddlEdit, error) {
	if end-start < 2 || tableConstraintKeywords[toks[start].word(query)] {
		return edits, nil
	}
	return appendTypeEdit(query, toks, edits, start+1, end)
}

// alterTableTypeEdits returns the type substitutions for an ALTER TABLE
// statement: the type named by each ADD, MODIFY, or CHANGE clause that
// introduces or redefines a column. Every other clause -- DROP, RENAME,
// ALTER COLUMN ... SET/DROP DEFAULT, and any ADD of a table constraint --
// names no type and contributes no edit.
func alterTableTypeEdits(query string, toks []ddlToken) ([]ddlEdit, error) {
	start := alterClausesStart(query, toks)

	var edits []ddlEdit
	depth := 0
	clause := start
	for i := start; i < len(toks); i++ {
		switch {
		case toks[i].isPunct(query, '('):
			depth++
		case toks[i].isPunct(query, ')'):
			depth--
		case depth == 0 && toks[i].isPunct(query, ','):
			var err error
			if edits, err = appendAlterClauseEdit(query, toks, edits, clause, i); err != nil {
				return nil, err
			}
			clause = i + 1
		}
	}
	return appendAlterClauseEdit(query, toks, edits, clause, len(toks))
}

// alterClausesStart returns the token index at which an ALTER TABLE
// statement's clauses begin, skipping the ALTER TABLE keywords and the
// possibly schema-qualified table name.
func alterClausesStart(query string, toks []ddlToken) int {
	i := 2 // past ALTER TABLE
	if i < len(toks) {
		i++ // table name
	}
	if i+1 < len(toks) && toks[i].isPunct(query, '.') {
		i += 2 // .name
	}
	return i
}

// appendAlterClauseEdit appends the type substitution named by the ALTER
// TABLE clause spanning toks[start:end), if that clause names a column
// type at all.
func appendAlterClauseEdit(query string, toks []ddlToken, edits []ddlEdit, start, end int) ([]ddlEdit, error) {
	if start >= end {
		return edits, nil
	}

	names, ok := alterClauseColumnOffset[toks[start].word(query)]
	if !ok {
		return edits, nil
	}

	i := start + 1
	if i < end && toks[i].word(query) == "COLUMN" {
		i++ // optional COLUMN noise word
	}
	if i < end && tableConstraintKeywords[toks[i].word(query)] {
		return edits, nil // ADD CONSTRAINT / ADD UNIQUE / ADD INDEX ...
	}
	return appendTypeEdit(query, toks, edits, i+names, end)
}

// appendTypeEdit appends the substitution for the column type beginning at
// toks[at], which must lie within toks[:limit].
func appendTypeEdit(query string, toks []ddlToken, edits []ddlEdit, at, limit int) ([]ddlEdit, error) {
	edit, ok, err := columnTypeEdit(query, toks, at, limit)
	if err != nil {
		return nil, err
	}
	if !ok {
		return edits, nil
	}
	return append(edits, edit), nil
}

// columnTypeEdit resolves the PostgreSQL column type beginning at toks[at]
// and returns the substitution for it. It reports ok=false when the type
// has no entry in pgColumnTypes, which is the pass-through case: the type
// reaches the parser under its own name, so a MySQL-spelled type is
// accepted as-is and an unknown one is rejected by the parser rather than
// being guessed at.
//
// It returns a typed *UnsupportedColumnTypeError, never a silent
// substitution, for the two shapes that cannot be expressed at all: an
// array type (PostgreSQL's `text[]`, which MySQL has no counterpart for)
// and a length modifier on a substituted type that cannot carry one.
func columnTypeEdit(query string, toks []ddlToken, at, limit int) (ddlEdit, bool, error) {
	if at >= limit || toks[at].kind != ddlWord {
		return ddlEdit{}, false, nil
	}

	name, mapped, next := matchColumnType(query, toks, at, limit)

	modifier, afterModifier := columnTypeModifier(query, toks, next, limit)
	if modifier != "" && mapped.Name != "" && !mapped.KeepModifier {
		return ddlEdit{}, false, &UnsupportedColumnTypeError{Type: name + modifier}
	}

	if afterModifier < limit && toks[afterModifier].isPunct(query, '[') {
		// modifier is included so the rejection names the type as the
		// caller wrote it: "varchar(20)[]", not "varchar[]". An error that
		// silently reshapes the offending text is harder to act on than one
		// that quotes it back.
		return ddlEdit{}, false, &UnsupportedColumnTypeError{Type: name + modifier + "[]"}
	}
	if mapped.KeepModifier && afterModifier < limit {
		if w := toks[afterModifier].word(query); w == "WITH" || w == "WITHOUT" {
			return ddlEdit{}, false, &UnsupportedColumnTypeError{Type: name + modifier + " " + w + " TIME ZONE"}
		}
	}

	if mapped.Name == "" {
		return ddlEdit{}, false, nil
	}

	replacement := mapped.Name
	if mapped.KeepModifier {
		replacement += modifier
	}
	if mapped.AutoIncrement {
		replacement += " AUTO_INCREMENT"
	}

	return ddlEdit{start: toks[at].start, end: toks[afterModifier-1].end, text: replacement}, true, nil
}

// matchColumnType resolves the longest run of bare words starting at
// toks[at] that names a key of pgColumnTypes, falling back to the single
// word at toks[at]. It returns that name as written, its mapping (the zero
// pgColumnType when the name is unmapped), and the token index just past
// the name.
func matchColumnType(query string, toks []ddlToken, at, limit int) (string, pgColumnType, int) {
	for words := maxTypePhraseWords; words > 1; words-- {
		if at+words > limit {
			continue
		}
		phrase, ok := typePhrase(query, toks, at, words)
		if !ok {
			continue
		}
		if mapped, found := pgColumnTypes[phrase]; found {
			return phrase, mapped, at + words
		}
	}

	name := toks[at].text(query)
	return name, pgColumnTypes[strings.ToLower(name)], at + 1
}

// typePhrase joins the words of toks[at:at+words] with single spaces,
// lowercased, reporting false unless every one of them is a bare word.
func typePhrase(query string, toks []ddlToken, at, words int) (string, bool) {
	parts := make([]string, words)
	for i := 0; i < words; i++ {
		if toks[at+i].kind != ddlWord {
			return "", false
		}
		parts[i] = strings.ToLower(toks[at+i].text(query))
	}
	return strings.Join(parts, " "), true
}

// columnTypeModifier returns a type's parenthesized length or precision
// modifier as written -- "(20)", "(9,0)" -- when toks[at] opens one, along
// with the token index just past it. It returns "" and at unchanged when
// the type has no modifier.
func columnTypeModifier(query string, toks []ddlToken, at, limit int) (string, int) {
	if at >= limit || !toks[at].isPunct(query, '(') {
		return "", at
	}

	depth := 0
	for i := at; i < limit; i++ {
		switch {
		case toks[i].isPunct(query, '('):
			depth++
		case toks[i].isPunct(query, ')'):
			depth--
			if depth == 0 {
				return query[toks[at].start:toks[i].end], i + 1
			}
		}
	}
	return "", at
}
