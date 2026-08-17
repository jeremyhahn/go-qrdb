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
	"sync"

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/plan"
)

// This file repairs two upstream go-mysql-server defects in DML RETURNING
// (docs/sql/upstream-gaps.md #1 and #2) through the engine's OWN extension
// points — rowexec.BaseBuilder.PriorityBuilder and the pass-through behaviour
// of its accumulator selection — rather than by forking the dependency:
//
//  1. INSERT IGNORE ... RETURNING silently discards its rows. An ignoring
//     insert is built as a plan.CheckpointingTableEditorIter, and upstream's
//     AddAccumulatorIter has no case for that type, so the statement falls
//     through to the default accumulator and degrades to an OkResult. The
//     inner insert iterator ALREADY projects and yields the RETURNING rows;
//     they are swallowed one wrapper up.
//
//  2. INSERT ... ON DUPLICATE KEY UPDATE ... RETURNING emits, for each row
//     that took the update path, the OLD and NEW tuples concatenated instead
//     of the RETURNING projection — a row wider than the declared schema.
//
// Both matter because they are exactly what the PostgreSQL translator emits
// for GORM's bread-and-butter statements: ON CONFLICT DO NOTHING RETURNING
// becomes INSERT IGNORE ... RETURNING (every association create), and
// ON CONFLICT DO UPDATE ... RETURNING becomes the upsert form.
//
// The mechanism: dmlReturningBuilder intercepts the two broken statement
// shapes at build time, delegates the actual build to the base builder, and
// wraps the produced iterator in dmlReturningIter. The wrapper's TYPE is the
// repair for defect 1 — upstream's AddAccumulatorIter and getRowHandler both
// pass an iterator type they do not recognise through untouched, so the
// RETURNING rows the inner iterator yields are no longer replaced by an
// OkResult, and the statement keeps the node's own (returning) schema. The
// wrapper's Next is the repair for defect 2 — it recognises the
// old+new-concatenation shape by width and applies the node's RETURNING
// projection to the NEW tuple.
//
// Statement shapes upstream handles correctly (plain INSERT ... RETURNING,
// and all DML without RETURNING) are deliberately not intercepted.

// dmlReturningBuilder is the sql.NodeExecBuilder installed as the base
// builder's PriorityBuilder by New.
type dmlReturningBuilder struct {
	// base is the rowexec.BaseBuilder this builder is installed on. Calling
	// it re-enters this builder (the base consults its PriorityBuilder
	// first), which inFlight breaks: a node currently being delegated is
	// declined on re-entry, so the base's own implementation builds it.
	base sql.NodeExecBuilder

	// inFlight holds the nodes currently being delegated to the base
	// builder, keyed by node identity. Plans are per-statement values, so a
	// node pointer never legitimately builds twice concurrently.
	inFlight sync.Map
}

var _ sql.NodeExecBuilder = (*dmlReturningBuilder)(nil)

// Build intercepts InsertInto nodes carrying RETURNING expressions together
// with either IGNORE or an ON DUPLICATE KEY UPDATE clause. Returning
// (nil, nil) declines the node, which is the PriorityBuilder contract for
// "build it the normal way".
func (b *dmlReturningBuilder) Build(ctx *sql.Context, n sql.Node, r sql.Row) (sql.RowIter, error) {
	ins, ok := n.(*plan.InsertInto)
	if !ok || len(ins.Returning) == 0 {
		return nil, nil
	}
	if !ins.Ignore && len(ins.OnDupExprs.AllExpressions()) == 0 {
		// Plain INSERT ... RETURNING: upstream projects this correctly.
		return nil, nil
	}
	if _, building := b.inFlight.Load(n); building {
		return nil, nil
	}

	b.inFlight.Store(n, struct{}{})
	defer b.inFlight.Delete(n)

	inner, err := b.base.Build(ctx, n, r)
	if err != nil {
		return nil, err
	}
	return &dmlReturningIter{
		child:     inner,
		destWidth: len(ins.Destination.Schema(ctx)),
		returning: ins.Returning,
	}, nil
}

// dmlReturningIter forwards the rows the inner insert iterator yields,
// repairing the one malformed shape the duplicate-update path produces.
//
// It deliberately implements ONLY Next and Close. In particular it must not
// implement sql.MutableRowIter: upstream's AddAccumulatorIter unwraps that
// interface and would descend past this wrapper to the iterator whose type
// triggers the OkResult degradation this wrapper exists to prevent.
type dmlReturningIter struct {
	child     sql.RowIter
	destWidth int
	returning []sql.Expression
}

var _ sql.RowIter = (*dmlReturningIter)(nil)

// Next returns the child's next row. Rows already as wide as the RETURNING
// projection pass through untouched — the insert path projects its own rows.
// A row exactly twice the destination schema's width is the duplicate-update
// path's old+new concatenation; the RETURNING expressions are evaluated
// against the NEW tuple, whose field indexes they were resolved against.
//
// The two widths can only collide (2×destination == len(returning)) when the
// statement returns more expressions than the destination has columns twice
// over a single-column table; no GORM statement has that shape, and a
// collision passes the row through rather than corrupting it.
func (i *dmlReturningIter) Next(ctx *sql.Context) (sql.Row, error) {
	var row sql.Row
	for {
		var err error
		row, err = i.child.Next(ctx)
		if err != nil {
			// A suppressed conflict surfaces as sql.IgnorableError, which
			// the accumulator this wrapper displaced would have absorbed.
			// PostgreSQL's ON CONFLICT DO NOTHING RETURNING yields no row
			// for a suppressed tuple, so absorb it here and move on.
			// Direct assertion is deliberate: go-mysql-server errors do
			// not implement Unwrap, and IgnorableError arrives at the top
			// of the chain from the engine itself.
			if _, ignorable := err.(sql.IgnorableError); ignorable { //nolint:errorlint // GMS errors carry no Unwrap chain
				continue
			}
			return nil, err
		}
		break
	}
	if i.destWidth == 0 || len(row) != 2*i.destWidth || len(row) == len(i.returning) {
		return row, nil
	}

	newTuple := row[i.destWidth:]
	projected := make(sql.Row, len(i.returning))
	for k, expr := range i.returning {
		value, evalErr := expr.Eval(ctx, newTuple)
		if evalErr != nil {
			return nil, evalErr
		}
		projected[k] = value
	}
	return projected, nil
}

// Close closes the child iterator.
func (i *dmlReturningIter) Close(ctx *sql.Context) error {
	return i.child.Close(ctx)
}
