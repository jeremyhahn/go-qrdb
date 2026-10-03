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

package engine

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	ast "github.com/dolthub/vitess/go/vt/sqlparser"

	"github.com/dolthub/go-mysql-server/sql"
)

// This file repairs an upstream go-mysql-server BIND-TIME defect: an
// aggregate function inside a scalar subquery under HAVING is deduplicated
// against the OUTER query's aggregations by its rendered name.
//
// planbuilder's buildAggregateFunc registers each aggregation under the
// string form of its resolved expression — "avg(users.age)" — and reuses an
// existing registration found by that name through the scope chain
// (gb.outScope.getExpr(aggName, true); the true means "search parents").
// buildHaving hands the HAVING scope the outer groupBy, so when the
// subquery's own SELECT binds AVG(age) over ITS OWN instance of the same
// table, the name collides, the inner aggregate becomes a GetField reference
// to the outer group's column, and the subquery loses its aggregation
// entirely. It then yields one row per source row — N copies of the OUTER
// group's aggregate — and a scalar comparison fails with "the subquery
// returned more than 1 row" (GORM's TestSubQueryWithHaving, on every
// target).
//
// The collision key is the RESOLVED table name, and an aliased table
// resolves under its alias. Renaming the subquery's tables therefore makes
// the inner aggregation's name unique, the dedup finds no match, and the
// subquery keeps its own aggregation — verified to produce the correct
// result. That is what this repair does: for every scalar subquery under a
// HAVING clause, each unaliased base table in the subquery's FROM gains a
// fresh generated alias, and qualified column references to the old name
// within the same SELECT are rewritten to match.
//
// It is installed as the engine's parser (sql.BuilderOverrides.Parser), the
// one seam every entry path shares, so the repair holds on the native, MySQL
// and PostgreSQL surfaces alike. Statements without a HAVING-with-subquery
// pass through untouched.

// havingSubqueryAliasCounter feeds fresh alias names. Global rather than
// per-statement so that a statement nesting several repaired subqueries can
// never collide with itself regardless of walk order.
var havingSubqueryAliasCounter atomic.Uint64

// havingSubqueryParser wraps the engine's real parser and repairs every
// statement it produces.
type havingSubqueryParser struct {
	inner sql.Parser
}

var _ sql.Parser = (*havingSubqueryParser)(nil)

// newHavingSubqueryParser wraps inner, which is never nil in practice —
// engine.New passes the default MySQL parser.
func newHavingSubqueryParser(inner sql.Parser) sql.Parser {
	return &havingSubqueryParser{inner: inner}
}

func (p *havingSubqueryParser) ParseSimple(query string) (ast.Statement, error) {
	stmt, err := p.inner.ParseSimple(query)
	if err == nil {
		repairHavingSubqueries(stmt)
	}
	return stmt, err
}

func (p *havingSubqueryParser) Parse(ctx *sql.Context, query string, multi bool) (ast.Statement, string, string, error) {
	stmt, parsed, remainder, err := p.inner.Parse(ctx, query, multi)
	if err == nil {
		repairHavingSubqueries(stmt)
	}
	return stmt, parsed, remainder, err
}

func (p *havingSubqueryParser) ParseWithOptions(ctx context.Context, query string, delimiter rune, multi bool, options ast.ParserOptions) (ast.Statement, string, string, error) {
	stmt, parsed, remainder, err := p.inner.ParseWithOptions(ctx, query, delimiter, multi, options)
	if err == nil {
		repairHavingSubqueries(stmt)
	}
	return stmt, parsed, remainder, err
}

func (p *havingSubqueryParser) ParseOneWithOptions(ctx context.Context, query string, options ast.ParserOptions) (ast.Statement, int, error) {
	stmt, idx, err := p.inner.ParseOneWithOptions(ctx, query, options)
	if err == nil {
		repairHavingSubqueries(stmt)
	}
	return stmt, idx, err
}

// repairHavingSubqueries finds every SELECT with a HAVING clause and gives
// the tables of each subquery under that clause fresh aliases. A nil or
// non-SELECT statement passes through untouched.
func repairHavingSubqueries(stmt ast.Statement) {
	if stmt == nil {
		return
	}
	_ = ast.Walk(func(node ast.SQLNode) (bool, error) {
		sel, ok := node.(*ast.Select)
		if !ok || sel.Having == nil {
			return true, nil
		}
		_ = ast.Walk(func(h ast.SQLNode) (bool, error) {
			if sq, ok := h.(*ast.Subquery); ok {
				aliasSubqueryTables(sq.Select)
				// aliasSubqueryTables walks nested selects itself; do not
				// re-enter through this walk as well.
				return false, nil
			}
			return true, nil
		}, sel.Having.Expr)
		return true, nil
	}, stmt)
}

// aliasSubqueryTables renames every unaliased base table in each SELECT of
// the given statement, rewriting same-SELECT qualified column references to
// follow the rename.
//
// Only unaliased plain tables are touched: a table the query already aliases
// resolves under that alias, which does not collide with the outer scope's
// table-qualified aggregation names. Qualified references inside DEEPER
// subqueries that correlate to a renamed table are not rewritten — those
// would need full scope resolution to do faithfully, and the statement
// shapes this repair exists for (GORM's, and plain scalar aggregates
// generally) qualify columns within the same SELECT or not at all.
func aliasSubqueryTables(node ast.SQLNode) {
	_ = ast.Walk(func(n ast.SQLNode) (bool, error) {
		sel, ok := n.(*ast.Select)
		if !ok {
			return true, nil
		}

		renames := make(map[string]string)
		for _, tableExpr := range sel.From {
			aliased, ok := tableExpr.(*ast.AliasedTableExpr)
			if !ok || !aliased.As.IsEmpty() {
				continue
			}
			tableName, ok := aliased.Expr.(ast.TableName)
			if !ok {
				continue
			}
			fresh := fmt.Sprintf("__qrdb_hsq%d", havingSubqueryAliasCounter.Add(1))
			aliased.As = ast.NewTableIdent(fresh)
			renames[strings.ToLower(tableName.Name.String())] = fresh
		}
		if len(renames) == 0 {
			return true, nil
		}

		rewriteQualifiers := func(clause ast.SQLNode) {
			if clause == nil {
				return
			}
			_ = ast.Walk(func(c ast.SQLNode) (bool, error) {
				if _, nested := c.(*ast.Subquery); nested {
					// A nested subquery re-resolves names in its own scope;
					// it is aliased by its own visit of this walk.
					return false, nil
				}
				if col, ok := c.(*ast.ColName); ok {
					if col.Qualifier.DbQualifier.IsEmpty() && !col.Qualifier.Name.IsEmpty() {
						if fresh, renamed := renames[strings.ToLower(col.Qualifier.Name.String())]; renamed {
							col.Qualifier.Name = ast.NewTableIdent(fresh)
						}
					}
				}
				return true, nil
			}, clause)
		}

		rewriteQualifiers(sel.SelectExprs)
		rewriteQualifiers(sel.Where)
		rewriteQualifiers(sel.GroupBy)
		rewriteQualifiers(sel.Having)
		rewriteQualifiers(sel.OrderBy)

		return true, nil
	}, node)
}
