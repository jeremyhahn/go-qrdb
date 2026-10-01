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

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"

	ast "github.com/dolthub/vitess/go/vt/sqlparser"
)

// Strength is the lock a statement asks for on a table's rows.
type Strength uint8

const (
	// None takes no lock.
	None Strength = iota
	// Shared is FOR SHARE / LOCK IN SHARE MODE.
	Shared
	// Exclusive is FOR UPDATE.
	Exclusive
)

// Intent is the row-lock intent of one statement.
type Intent struct {
	// locking maps a lowercased table name to the strongest lock clause of
	// any query block that reads it.
	locking map[string]Strength

	// targets holds the lowercased tables an UPDATE or DELETE writes.
	targets map[string]bool

	// NoWait is true when a lock clause carries NOWAIT.
	NoWait bool

	// SkipLocked is true when a lock clause carries SKIP LOCKED.
	SkipLocked bool
}

// none is the intent of a statement with no lock clause and no DML target.
var none = &Intent{}

// Locking returns the lock clause strength over table's rows.
func (in *Intent) Locking(table string) Strength {
	return in.locking[strings.ToLower(table)]
}

// Targets reports whether table is a target of the statement's UPDATE or
// DELETE.
func (in *Intent) Targets(table string) bool {
	return in.targets[strings.ToLower(table)]
}

// Empty reports whether the statement has no lock clause and no DML target.
func (in *Intent) Empty() bool {
	return len(in.locking) == 0 && len(in.targets) == 0
}

// cacheLimit bounds the analysis cache. Only texts that can carry a clause
// or a DML target are cached, so the working set is the application's
// distinct locking and DML statements.
const cacheLimit = 4096

// cacheKey identifies one analysis: the statement text and the parser
// options it was parsed with. The options come from the session's
// sql_mode, and the same text can parse differently under two of them
// (ANSI_QUOTES turns a double-quoted string into an identifier), so the
// text alone is not a key.
type cacheKey struct {
	opts  ast.ParserOptions
	query string
}

// cache holds one generation of analyses; when it fills, the next store
// swaps in an empty generation rather than evicting entry by entry.
var (
	cache     atomic.Pointer[sync.Map]
	cacheSize atomic.Int64
)

func init() { cache.Store(new(sync.Map)) }

// Analyze returns the lock intent of the first statement of query, parsed
// with opts after Normalize. The first statement is the current one: under
// CLIENT_MULTI_STATEMENTS the engine runs each statement of a text with the
// remainder that starts at that statement as its query text. A text that
// cannot hold a lock clause or a DML statement is answered without parsing.
// A text that does not parse has no intent: the engine refuses it on its own
// parse.
func Analyze(ctx context.Context, query string, opts ast.ParserOptions) *Intent {
	if !mayLock(query) {
		return none
	}
	key := cacheKey{opts: opts, query: query}
	if v, ok := cache.Load().Load(key); ok {
		return v.(*Intent) //nolint:errcheck // the cache only ever holds *Intent
	}
	in := analyze(ctx, query, opts)
	m := cache.Load()
	if cacheSize.Add(1) > cacheLimit {
		m = new(sync.Map)
		cache.Store(m)
		cacheSize.Store(1)
	}
	m.Store(key, in)
	return in
}

// mayLock is the cheap pre-filter: every lock clause spells "update" or
// "share", and every DML target statement starts with update or delete.
func mayLock(query string) bool {
	return containsFold(query, "update") || containsFold(query, "share") || containsFold(query, "delete")
}

// analyze parses and walks one statement.
func analyze(ctx context.Context, query string, opts ast.ParserOptions) *Intent {
	norm := Normalize(query)
	stmt, _, err := ast.ParseOneWithOptions(ctx, norm.Query, opts)
	if err != nil || stmt == nil {
		return none
	}
	in := &Intent{locking: map[string]Strength{}, targets: map[string]bool{}}
	share := shareOf(norm.Share)
	for _, c := range norm.Share {
		in.NoWait = in.NoWait || c.NoWait
		in.SkipLocked = in.SkipLocked || c.SkipLocked
	}

	switch s := stmt.(type) {
	case *ast.Update:
		addTables(in.targets, s.TableExprs, nil)
	case *ast.Delete:
		if len(s.Targets) > 0 {
			// A multi-table DELETE names its targets by alias as often as by
			// table (DELETE a FROM accounts a ...): resolve each to the base
			// table its FROM clause binds it to, which is the name the
			// storage layer asks about.
			bases := aliasBases(s.TableExprs)
			for _, t := range s.Targets {
				name := strings.ToLower(t.Name.String())
				if base, ok := bases[name]; ok {
					name = base
				}
				in.targets[name] = true
			}
		} else {
			addTables(in.targets, s.TableExprs, nil)
		}
	}

	_ = ast.Walk(func(node ast.SQLNode) (bool, error) {
		switch n := node.(type) {
		case *ast.Select:
			if hasLock(n.Lock) {
				in.addBlock(n.Lock, n.From, share)
			}
		case *ast.SetOp:
			if hasLock(n.Lock) {
				for _, sel := range setOpSelects(n) {
					in.addBlock(n.Lock, sel.From, share)
				}
			}
		}
		return true, nil
	}, stmt)

	if len(in.locking) == 0 && len(in.targets) == 0 && !in.NoWait && !in.SkipLocked {
		return none
	}
	return in
}

// hasLock reports whether lock is a lock clause: the parser attaches an
// empty Lock to every SELECT that has none.
func hasLock(lock *ast.Lock) bool {
	return lock != nil && lock.Type != ""
}

// shareOf merges the OF lists of every FOR SHARE clause; nil means no FOR
// SHARE clause named any table, so every shared block keeps all of its
// tables.
func shareOf(clauses []ShareClause) map[string]bool {
	var of map[string]bool
	for _, c := range clauses {
		for _, t := range c.Of {
			if of == nil {
				of = map[string]bool{}
			}
			of[t] = true
		}
	}
	return of
}

// addBlock records the tables of one locking query block.
func (in *Intent) addBlock(lock *ast.Lock, from ast.TableExprs, shareOfTables map[string]bool) {
	text := strings.ToLower(lock.Type)
	strength := Exclusive
	of := map[string]bool(nil)
	if strings.Contains(text, "share") {
		strength = Shared
		of = shareOfTables
	}
	if strings.Contains(text, "nowait") {
		in.NoWait = true
	}
	if strings.Contains(text, "skip locked") {
		in.SkipLocked = true
	}
	for _, t := range lock.Tables {
		if of == nil {
			of = map[string]bool{}
		}
		of[strings.ToLower(t.Name.String())] = true
	}
	tables := map[string]bool{}
	addTables(tables, from, of)
	for name := range tables {
		if in.locking[name] < strength {
			in.locking[name] = strength
		}
	}
}

// addTables adds the base tables of exprs to out, lowercased, without
// descending into derived tables (a subquery locks nothing unless it carries
// its own clause). When of is non-nil only tables whose name or alias it
// lists are added.
func addTables(out map[string]bool, exprs ast.TableExprs, of map[string]bool) {
	for _, e := range exprs {
		switch t := e.(type) {
		case *ast.AliasedTableExpr:
			name, ok := t.Expr.(ast.TableName)
			if !ok {
				continue
			}
			base := strings.ToLower(name.Name.String())
			alias := strings.ToLower(t.As.String())
			if of != nil && !of[base] && (alias == "" || !of[alias]) {
				continue
			}
			out[base] = true
		case *ast.JoinTableExpr:
			addTables(out, ast.TableExprs{t.LeftExpr, t.RightExpr}, of)
		case *ast.ParenTableExpr:
			addTables(out, t.Exprs, of)
		}
	}
}

// aliasBases maps every alias bound by exprs to its base table, lowercased.
// A table without an alias maps to itself. Derived tables bind no base
// table and are skipped.
func aliasBases(exprs ast.TableExprs) map[string]string {
	out := map[string]string{}
	var walk func(ast.TableExprs)
	walk = func(exprs ast.TableExprs) {
		for _, e := range exprs {
			switch t := e.(type) {
			case *ast.AliasedTableExpr:
				name, ok := t.Expr.(ast.TableName)
				if !ok {
					continue
				}
				base := strings.ToLower(name.Name.String())
				out[base] = base
				if alias := strings.ToLower(t.As.String()); alias != "" {
					out[alias] = base
				}
			case *ast.JoinTableExpr:
				walk(ast.TableExprs{t.LeftExpr, t.RightExpr})
			case *ast.ParenTableExpr:
				walk(t.Exprs)
			}
		}
	}
	walk(exprs)
	return out
}

// setOpSelects flattens a set operation into its SELECT arms.
func setOpSelects(op *ast.SetOp) []*ast.Select {
	var out []*ast.Select
	for _, arm := range []ast.SelectStatement{op.Left, op.Right} {
		switch a := arm.(type) {
		case *ast.Select:
			out = append(out, a)
		case *ast.SetOp:
			out = append(out, setOpSelects(a)...)
		case *ast.ParenSelect:
			if sel, ok := a.Select.(*ast.Select); ok {
				out = append(out, sel)
			}
		}
	}
	return out
}
