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

// Package lockclause reads the row-lock intent of one SQL statement:
// which query blocks carry SELECT ... FOR UPDATE / FOR SHARE / LOCK IN SHARE
// MODE, over which tables, with NOWAIT or SKIP LOCKED, and which tables an
// UPDATE or DELETE targets.
//
// go-mysql-server builds the same plan with or without a lock clause, so the
// clause never reaches a table. This package recovers it from the statement
// text itself, which every SQL surface carries on its query context.
//
// It also owns the one spelling the vitess grammar lacks: MySQL 8 and
// PostgreSQL's FOR SHARE, which Normalize rewrites to the grammar's LOCK IN
// SHARE MODE (recording any OF list, NOWAIT or SKIP LOCKED the rewrite cannot
// carry) so the engine's parser accepts it. See docs/row-locking/.
package lockclause

import (
	"strings"
)

// ShareClause is one FOR SHARE clause Normalize rewrote, with the parts LOCK
// IN SHARE MODE cannot express.
type ShareClause struct {
	// Of lists the tables or aliases of FOR SHARE OF, lowercased; empty
	// means every table in the clause's query block.
	Of []string

	// NoWait is true for FOR SHARE ... NOWAIT.
	NoWait bool

	// SkipLocked is true for FOR SHARE ... SKIP LOCKED.
	SkipLocked bool
}

// Normalized is a statement text with every FOR SHARE clause rewritten.
type Normalized struct {
	// Query is the rewritten text.
	Query string

	// Share lists the rewritten clauses in text order.
	Share []ShareClause

	edits []edit
}

// edit records one rewrite: at rewritten offset pos, newLen bytes replaced
// origLen bytes of the original.
type edit struct {
	pos, origLen, newLen int
}

// shareModeText is the grammar's spelling of a shared lock clause.
const shareModeText = "LOCK IN SHARE MODE"

// OriginalOffset maps an offset into n.Query back to the offset of the same
// position in the original text, for a caller that indexes the original
// with a position the parser reported against the rewritten one.
func (n Normalized) OriginalOffset(i int) int {
	delta := 0
	for _, e := range n.edits {
		if i < e.pos+e.newLen {
			if i > e.pos {
				// Inside a rewritten clause: map to its original end.
				return e.pos + delta + e.origLen
			}
			break
		}
		delta += e.origLen - e.newLen
	}
	return i + delta
}

// Normalize rewrites every FOR SHARE clause outside string literals, quoted
// identifiers and comments into LOCK IN SHARE MODE. A text with no such
// clause is returned unchanged, with no allocation beyond the scan.
func Normalize(query string) Normalized {
	if !containsFold(query, "share") {
		return Normalized{Query: query}
	}
	toks := tokenize(query)
	var (
		b     strings.Builder
		out   Normalized
		last  int
		wrote bool
	)
	for i := 0; i+1 < len(toks); i++ {
		if !toks[i].is("for") || !toks[i+1].is("share") {
			continue
		}
		clause, end := parseShareTail(toks, i+2)
		start := toks[i].start
		stop := toks[end-1].end
		if !wrote {
			b.Grow(len(query) + 16)
			wrote = true
		}
		b.WriteString(query[last:start])
		out.edits = append(out.edits, edit{pos: b.Len(), origLen: stop - start, newLen: len(shareModeText)})
		b.WriteString(shareModeText)
		out.Share = append(out.Share, clause)
		last = stop
		i = end - 1
	}
	if !wrote {
		return Normalized{Query: query}
	}
	b.WriteString(query[last:])
	out.Query = b.String()
	return out
}

// parseShareTail consumes the optional OF list, NOWAIT and SKIP LOCKED that
// follow FOR SHARE starting at toks[i], returning the clause and the index
// just past the last token consumed.
func parseShareTail(toks []token, i int) (ShareClause, int) {
	var c ShareClause
	if i < len(toks) && toks[i].is("of") && i+1 < len(toks) && toks[i+1].kind == tokWord {
		j := i + 1
		for j < len(toks) && toks[j].kind == tokWord {
			c.Of = append(c.Of, lastName(toks[j].ident()))
			j++
			// A qualified name is word . word; keep its last part.
			for j+1 < len(toks) && toks[j].kind == tokPunct && toks[j].text == "." && toks[j+1].kind == tokWord {
				c.Of[len(c.Of)-1] = lastName(toks[j+1].ident())
				j += 2
			}
			if j < len(toks) && toks[j].kind == tokPunct && toks[j].text == "," {
				j++
				continue
			}
			break
		}
		i = j
	}
	switch {
	case i < len(toks) && toks[i].is("nowait"):
		c.NoWait = true
		i++
	case i+1 < len(toks) && toks[i].is("skip") && toks[i+1].is("locked"):
		c.SkipLocked = true
		i += 2
	}
	return c, i
}

// lastName lowercases an identifier.
func lastName(s string) string { return strings.ToLower(s) }

// containsFold reports whether s contains sub, ASCII case-insensitively.
func containsFold(s, sub string) bool {
	n := len(sub)
	for i := 0; i+n <= len(s); i++ {
		if strings.EqualFold(s[i:i+n], sub) {
			return true
		}
	}
	return false
}
