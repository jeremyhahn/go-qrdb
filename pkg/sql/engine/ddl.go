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

	// KeyedName is the type substituted instead of Name when the column
	// participates in a key of the same statement (see keyedColumns). It is
	// set for exactly those PostgreSQL types whose Name is one of MySQL's
	// unbounded TEXT/BLOB families, because MySQL cannot index such a column
	// at all without a prefix length -- "blob/text column 'code' used in key
	// specification without a key length". An empty KeyedName means the
	// substitution does not depend on key membership.
	KeyedName string

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

	// DefaultModifier is the modifier applied when KeepModifier is set and
	// the source names none. It exists because the two dialects disagree on
	// what an ABSENT modifier means: an unmodified PostgreSQL timestamp is
	// microsecond-precision (6), while an unmodified MySQL DATETIME is
	// SECOND-precision (0). Substituting bare DATETIME for bare timestamptz
	// therefore silently truncated every round-tripped time.Time to whole
	// seconds -- GORM's TestSave caught it as an updated_at that came back
	// shorter than it went in.
	DefaultModifier string
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
//     integer column whose default comes from an owned sequence. A serial
//     column covered by a key of the same statement maps to the integer
//     width PostgreSQL gives it plus MySQL's AUTO_INCREMENT; one outside
//     every key -- where MySQL's attribute is not even legal -- keeps
//     PostgreSQL's own semantics as an integer defaulted from the
//     implicitly named sequence (see columnTypeEdit and
//     TestTranslateDDLTypes_MultipleSerialColumns).
//
//   - text is PostgreSQL's unbounded string. MySQL's TEXT caps at 64 KiB,
//     so the honest stand-in is LONGTEXT, which is also what
//     gorm.io/driver/mysql itself picks for the same unbounded field.
//     bytea maps to LONGBLOB for exactly the same reason. Both carry a
//     KeyedName as well, because neither LONGTEXT nor LONGBLOB can appear
//     in a key without a prefix length -- see keyedTextType.
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
	"text":                        {Name: "LONGTEXT", KeyedName: keyedTextType},
	"bytea":                       {Name: "LONGBLOB", KeyedName: keyedBlobType},
	"uuid":                        {Name: "CHAR(36)"},
	"timestamptz":                 {Name: "DATETIME", KeepModifier: true, DefaultModifier: "(6)"},
	"timestamp":                   {Name: "DATETIME", KeepModifier: true, DefaultModifier: "(6)"},
	"timestamp without time zone": {Name: "DATETIME", KeepModifier: true, DefaultModifier: "(6)"},
	"timestamp with time zone":    {Name: "DATETIME", KeepModifier: true, DefaultModifier: "(6)"},

	// PostgreSQL's numeric/decimal without a modifier is arbitrary-precision
	// exact; MySQL's bare DECIMAL defaults to (10,0), which silently ROUNDS
	// every fractional value to an integer on insert (gorm.io/driver/postgres
	// declares plain Go floats as modifier-less "decimal", so 1.8888 stored
	// through such a column read back as 2). The widest scale MySQL can
	// carry, (65,30), is substituted only when the statement names no
	// modifier of its own; an explicit numeric(p,s) keeps (p,s).
	"numeric": {Name: "DECIMAL", KeepModifier: true, DefaultModifier: "(65,30)"},
	"decimal": {Name: "DECIMAL", KeepModifier: true, DefaultModifier: "(65,30)"},
}

// maxTypePhraseWords is the greatest number of whitespace-separated words
// any key of pgColumnTypes spans ("timestamp without time zone").
const maxTypePhraseWords = 4

// keyedTextType and keyedBlobType are the bounded types substituted for
// PostgreSQL's unbounded text and bytea when the column participates in a
// key. MySQL refuses to index a TEXT or BLOB column without a prefix length,
// so the alternative to bounding the column is not "an unbounded key" -- it
// is a table that cannot be created at all.
//
// Why 768 characters, and not some other number:
//
//   - InnoDB's index-key ceiling is 3072 bytes per column (DYNAMIC or
//     COMPRESSED row format), which go-mysql-server enforces verbatim as
//     analyzer.MaxBytePrefix and compares against a character count scaled by
//     the charset's maximum bytes per character.
//   - utf8mb4, the default here as on modern MySQL, is up to 4 bytes per
//     character. 3072/4 = 768, so VARCHAR(768) is the LARGEST width that is
//     indexable under every charset -- one more character can overflow the
//     byte ceiling on a 4-byte charset and be rejected.
//
// 768 is therefore chosen to preserve the most caller data that can still be
// indexed. The alternative worth naming is 191, which gorm.io/driver/mysql
// picks for the same unbounded field: 191 is a legacy of MySQL's older
// 767-byte limit (767/4 = 191) and additionally leaves room for a composite
// key of four such columns, since real InnoDB checks the SUM of a composite
// key's parts against 3072 while go-mysql-server checks only each part. That
// composite-key headroom is the one thing 768 gives up: a PRIMARY KEY over
// two text columns is accepted here and would be refused by real InnoDB.
// Truncating every keyed text column to 191 characters to buy that headroom
// would silently reject caller values between 192 and 768 characters that
// this engine can index perfectly well, which is the worse trade.
//
// bytea's ceiling is stated directly in bytes because a BLOB prefix length
// is a byte count, not a character count, so no charset scaling applies.
const (
	keyedTextType = "VARCHAR(768)"
	keyedBlobType = "VARBINARY(3072)"
)

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

// keyConstraintKeywords are the table-constraint kinds whose parenthesized
// column list becomes an index, and whose member columns therefore need a
// bounded type (see keyedColumns). It is deliberately narrower than
// tableConstraintKeywords:
//
//   - FOREIGN is absent. A FOREIGN KEY clause names the child columns of a
//     reference, and go-mysql-server does not require a prefix length on
//     them; the index a foreign key needs is the one on the REFERENCED
//     column, which is declared by that other table's own key.
//   - FULLTEXT is absent because a FULLTEXT index over a TEXT column is
//     exactly what FULLTEXT is for and needs no prefix length, so bounding
//     the column would narrow it for no reason.
//   - SPATIAL is absent because it applies to geometry types, which have no
//     PostgreSQL spelling in pgColumnTypes at all.
//   - CHECK, EXCLUDE, and PERIOD create no index.
//
// All keys are upper case.
var keyConstraintKeywords = map[string]bool{
	"PRIMARY": true,
	"UNIQUE":  true,
	"KEY":     true,
	"INDEX":   true,
}

// inlineKeyKeywords are the column-constraint keywords that make the column
// they appear on a key by itself -- `"code" text PRIMARY KEY`, `"code" text
// UNIQUE` -- as opposed to the table-level constraint clauses that name
// their columns in a list. All keys are upper case.
var inlineKeyKeywords = map[string]bool{
	"PRIMARY": true,
	"UNIQUE":  true,
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

// ddlRange is a half-open range [start, end) of token indices.
type ddlRange struct {
	start, end int
}

// createTableTypeEdits returns the type substitutions for a CREATE TABLE
// statement. The column list is the first parenthesized group in the
// statement -- CREATE TABLE has no other -- and its top-level,
// comma-separated elements are each either a table constraint (skipped) or
// a column definition whose second token is the type.
//
// The list is walked twice, because whether a column's type substitution is
// correct depends on a clause that may appear AFTER it: a column whose type
// maps to one of MySQL's unbounded TEXT/BLOB families cannot be indexed, so
// a `text` column named by this same statement's PRIMARY KEY, UNIQUE, or
// INDEX clause takes the bounded KeyedName substitution instead. The first
// pass collects those key member names, the second emits the edits.
func createTableTypeEdits(query string, toks []ddlToken) ([]ddlEdit, error) {
	elements := createTableElements(query, toks)
	keyed := keyedColumns(query, toks, elements)

	var edits []ddlEdit
	for _, el := range elements {
		var err error
		if edits, err = appendColumnTypeEdit(query, toks, edits, el, keyed); err != nil {
			return nil, err
		}
	}
	return edits, nil
}

// createTableElements returns the token ranges of the top-level,
// comma-separated elements of a CREATE TABLE column list, or nil when the
// statement has no parenthesized group at all. A statement whose column list
// is never closed contributes only the elements a comma already terminated:
// the trailing fragment of a truncated statement is not an element, and the
// rewriter is not a validator -- such a statement is passed through for the
// parser to diagnose.
func createTableElements(query string, toks []ddlToken) []ddlRange {
	open := -1
	for i, t := range toks {
		if t.isPunct(query, '(') {
			open = i
			break
		}
	}
	if open < 0 {
		return nil
	}

	var elements []ddlRange
	depth := 1
	start := open + 1
	for i := open + 1; i < len(toks); i++ {
		switch {
		case toks[i].isPunct(query, '('):
			depth++
		case toks[i].isPunct(query, ')'):
			depth--
			if depth == 0 {
				return append(elements, ddlRange{start: start, end: i})
			}
		case depth == 1 && toks[i].isPunct(query, ','):
			elements = append(elements, ddlRange{start: start, end: i})
			start = i + 1
		}
	}
	return elements
}

// keyedColumns returns the set of column names, normalized by identName,
// that a CREATE TABLE statement's table-level key constraints name. Only the
// constraint kinds that create an index contribute (see
// keyConstraintKeywords); an inline column constraint is not a table
// constraint and is detected separately by inlineKeyed.
func keyedColumns(query string, toks []ddlToken, elements []ddlRange) map[string]bool {
	keyed := make(map[string]bool)
	for _, el := range elements {
		at := el.start
		if at < el.end && toks[at].word(query) == "CONSTRAINT" {
			at += 2 // CONSTRAINT <name>
		}
		if at < el.end && keyConstraintKeywords[toks[at].word(query)] {
			collectKeyColumns(query, toks, at, el.end, keyed)
		}
	}
	return keyed
}

// collectKeyColumns adds to keyed the column names listed in the first
// parenthesized group of the constraint clause spanning toks[at:end).
//
// Only the identifier in a name POSITION is collected -- the token just
// after the opening paren or a top-level comma -- so an index option that
// follows a name, such as the prefix length in `(code(10))` or a sort
// direction in `(code DESC)`, cannot be mistaken for another column.
func collectKeyColumns(query string, toks []ddlToken, at, end int, keyed map[string]bool) {
	open := -1
	for i := at; i < end; i++ {
		if toks[i].isPunct(query, '(') {
			open = i
			break
		}
	}
	if open < 0 {
		return
	}

	depth := 1
	expectName := true
	for i := open + 1; i < end && depth > 0; i++ {
		switch {
		case toks[i].isPunct(query, '('):
			depth++
		case toks[i].isPunct(query, ')'):
			depth--
		case depth != 1:
		case toks[i].isPunct(query, ','):
			expectName = true
		case expectName:
			if name := identName(query, toks[i]); name != "" {
				keyed[name] = true
				expectName = false
			}
		}
	}
}

// identName returns the normalized name a token denotes when it is an
// identifier -- backticks stripped and lower cased, so that `Code`,
// "`code`", and code all compare equal the way MySQL compares column names
// -- and "" for every token that cannot be one.
func identName(query string, t ddlToken) string {
	switch t.kind {
	case ddlWord:
		return strings.ToLower(t.text(query))
	case ddlQuoted:
		return strings.ToLower(strings.Trim(t.text(query), "`"))
	default:
		return ""
	}
}

// inlineKeyed reports whether the column definition spanning toks[at:end)
// carries an inline PRIMARY KEY or UNIQUE column constraint, which makes the
// column a key of its own without any table-level clause naming it. Only
// keywords at the definition's own paren depth count, so neither a
// parenthesized CHECK body nor a string literal that spells the same word
// can trigger it.
func inlineKeyed(query string, toks []ddlToken, at, end int) bool {
	depth := 0
	for i := at; i < end; i++ {
		switch {
		case toks[i].isPunct(query, '('):
			depth++
		case toks[i].isPunct(query, ')'):
			depth--
		case depth == 0 && inlineKeyKeywords[toks[i].word(query)]:
			return true
		}
	}
	return false
}

// appendColumnTypeEdit appends the type substitution for the CREATE TABLE
// column-list element el, or leaves edits unchanged when that element is a
// table constraint or is empty. keyed is the statement's table-level key
// membership as returned by keyedColumns.
func appendColumnTypeEdit(query string, toks []ddlToken, edits []ddlEdit, el ddlRange, keyed map[string]bool) ([]ddlEdit, error) {
	if el.end-el.start < 2 || tableConstraintKeywords[toks[el.start].word(query)] {
		return edits, nil
	}
	columnName := identName(query, toks[el.start])
	inKey := keyed[columnName] || inlineKeyed(query, toks, el.start+1, el.end)
	return appendTypeEdit(query, toks, edits, el.start+1, el.end, inKey, ddlTableName(query, toks), columnName)
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

	// An ALTER TABLE clause names one column and no table-level constraint
	// list, so the only key membership visible from here is an inline one:
	// ADD COLUMN `code` text UNIQUE. A column made a key by a SEPARATE
	// statement is out of reach by construction and keeps its unbounded
	// substitution -- see the doc comment on Translate.
	at := i + names
	column := ""
	if at-1 >= start && at-1 < len(toks) && at-1 < end {
		column = identName(query, toks[at-1])
	}
	return appendTypeEdit(query, toks, edits, at, end, inlineKeyed(query, toks, at, end), ddlTableName(query, toks), column)
}

// appendTypeEdit appends the substitution for the column type beginning at
// toks[at], which must lie within toks[:limit]. keyed reports whether the
// column participates in a key of the same statement; table and column name
// the definition the type belongs to, for the serial-to-sequence
// substitution (see columnTypeEdit), and may be empty when unknown.
func appendTypeEdit(query string, toks []ddlToken, edits []ddlEdit, at, limit int, keyed bool, table, column string) ([]ddlEdit, error) {
	edit, ok, err := columnTypeEdit(query, toks, at, limit, keyed, table, column)
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
// and a length modifier on a substituted type that cannot carry one. The
// latter is also what a keyed column that already names its own length
// hits: `text(20)` is not PostgreSQL syntax, so a mapped type carrying a
// modifier is refused by name rather than having that modifier replaced by
// KeyedName's. A type that legitimately carries its own bound -- varchar(20)
// -- has no pgColumnTypes entry, passes through untouched, and is indexable
// as written whether or not it is in a key.
//
// keyed reports whether the column participates in a key of the same
// statement; it selects mapped.KeyedName over mapped.Name for the types that
// distinguish them.
func columnTypeEdit(query string, toks []ddlToken, at, limit int, keyed bool, table, column string) (ddlEdit, bool, error) {
	if at >= limit || toks[at].kind != ddlWord {
		return ddlEdit{}, false, nil
	}

	name, mapped, next := matchColumnType(query, toks, at, limit)

	modifier, afterModifier := columnTypeModifier(query, toks, next, limit)
	if modifier != "" && mapped.Name != "" && !mapped.KeepModifier {
		return ddlEdit{}, false, &UnsupportedColumnTypeError{Type: name + modifier}
	}

	if afterModifier < limit && toks[afterModifier].isPunct(query, '[') {
		// A PostgreSQL array column -- text[], integer[], and so on. MySQL
		// has no array type, but an array VALUE on the wire is its text
		// literal ('{a,b}'), which is what every PostgreSQL client library
		// (lib/pq's pq.StringArray, pgx's array codecs in text format)
		// writes on INSERT and parses on scan. Storing that literal in a
		// text column round-trips it exactly; what is NOT supported is
		// server-side array operators, which fail in the engine by name.
		// Every bracket pair is consumed -- including a declared dimension
		// (integer[3][3]), which PostgreSQL itself treats as documentation
		// and does not enforce -- so multi-dimensional declarations resolve
		// the same way.
		end := afterModifier
		for end+1 < limit && toks[end].isPunct(query, '[') {
			closing := end + 1
			if toks[closing].kind == ddlWord && closing+1 < limit {
				closing++ // declared dimension
			}
			if !toks[closing].isPunct(query, ']') {
				break
			}
			end = closing + 1
		}
		if end == afterModifier {
			// A lone '[' with no closing ']' is not an array declaration;
			// refuse it by name as before.
			return ddlEdit{}, false, &UnsupportedColumnTypeError{Type: name + modifier + "["}
		}
		replacement := "LONGTEXT"
		if keyed {
			replacement = keyedTextType
		}
		return ddlEdit{start: toks[at].start, end: toks[end-1].end, text: replacement}, true, nil
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
	if keyed && mapped.KeyedName != "" {
		replacement = mapped.KeyedName
	}
	if mapped.KeepModifier {
		if modifier == "" {
			modifier = mapped.DefaultModifier
		}
		replacement += modifier
	}
	if mapped.AutoIncrement {
		if keyed || table == "" || column == "" {
			replacement += " AUTO_INCREMENT"
		} else {
			// A serial column OUTSIDE every key cannot be AUTO_INCREMENT
			// (MySQL permits exactly one, and only on a key), which is not
			// a corner PostgreSQL has: serial is just integer + an implicit
			// sequence default. It becomes exactly that here, against the
			// sequence PostgreSQL would implicitly name
			// (<table>_<column>_seq) -- the same name
			// gorm.io/driver/postgres later addresses with CREATE/DROP
			// SEQUENCE statements. Sequences materialize on first use (see
			// service.UserSequencer), so no CREATE SEQUENCE side effect is
			// needed for the default to work.
			replacement += " DEFAULT (NEXTVAL('" + table + "_" + column + "_seq'))"
		}
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

// TranslateColumnTypeText translates a single PostgreSQL column-type phrase
// -- "text", "timestamptz", "varchar(20)", "numeric(10,2)" -- into the
// go-mysql-server spelling, using the same substitution table CREATE TABLE
// translation uses (pgColumnTypes, including DefaultModifier semantics). A
// type with no entry is returned unchanged: the shared MySQL-dialect types
// (varchar, bigint, numeric, ...) are already what the engine speaks.
//
// It exists for pkg/sql/pgsql's catalog-aware DDL rewrites, which synthesize
// MODIFY COLUMN statements AFTER the whole-statement translator has run and
// therefore cannot lean on it for the type text they splice in.
func TranslateColumnTypeText(typeText string) string {
	trimmed := strings.TrimSpace(typeText)

	name := trimmed
	modifier := ""
	if i := strings.IndexByte(trimmed, '('); i >= 0 {
		name = strings.TrimSpace(trimmed[:i])
		modifier = strings.TrimSpace(trimmed[i:])
	}

	mapped, ok := pgColumnTypes[strings.ToLower(strings.Join(strings.Fields(name), " "))]
	if !ok || mapped.Name == "" {
		return trimmed
	}

	out := mapped.Name
	if mapped.KeepModifier {
		if modifier == "" {
			modifier = mapped.DefaultModifier
		}
		out += modifier
	}
	if mapped.AutoIncrement {
		out += " AUTO_INCREMENT"
	}
	return out
}

// ddlTableName returns the (lower-cased, unquoted) table name a CREATE
// TABLE or ALTER TABLE statement addresses, skipping IF NOT EXISTS, or ""
// when it cannot be located. Schema qualifiers are not resolved -- the
// serial-to-sequence substitution that consumes this only needs the
// PostgreSQL implicit sequence name, which is schema-independent.
func ddlTableName(query string, toks []ddlToken) string {
	i := 2 // past CREATE/ALTER TABLE
	// IF NOT EXISTS
	if i+2 < len(toks) && toks[i].word(query) == "IF" &&
		toks[i+1].word(query) == "NOT" && toks[i+2].word(query) == "EXISTS" {
		i += 3
	}
	if i >= len(toks) {
		return ""
	}
	name := identName(query, toks[i])
	// A schema-qualified name keeps its last component.
	if i+2 < len(toks) && toks[i+1].isPunct(query, '.') {
		name = identName(query, toks[i+2])
	}
	return name
}
