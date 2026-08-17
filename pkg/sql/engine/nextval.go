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
	"fmt"

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
)

// sequenceValueDatabase is implemented by databases that can allocate the
// next value of a client-named sequence -- pkg/sql/provider's Database.
// The interface is declared here rather than importing the provider so the
// dependency keeps pointing provider -> engine.
type sequenceValueDatabase interface {
	NextSequenceValue(ctx *sql.Context, name string) (int64, error)
}

// newNextvalFunction returns PostgreSQL's nextval(sequence_name),
// allocating from the session database's sequence catalog through
// provider. Sequences materialize on first use and start at 1 (see
// service.UserSequencer), which is what lets a serial column's implicit
// sequence work without a CREATE SEQUENCE statement ever running.
//
// The allocation happens on the node executing the statement, and the
// resulting VALUE -- not the function call -- is what enters the row
// written through Raft, so every replica stores the same number; the
// counter advance itself is a linearizable commit of its own inside
// SequenceServicer.Allocate.
func newNextvalFunction(provider sql.DatabaseProvider) sql.Function {
	return sql.Function1{
		Name: "nextval",
		Fn: func(_ *sql.Context, arg sql.Expression) sql.Expression {
			return &nextval{provider: provider, arg: arg}
		},
	}
}

// nextval is the expression newNextvalFunction builds.
type nextval struct {
	provider sql.DatabaseProvider
	arg      sql.Expression
}

// Compile-time interface assertions.
var (
	_ sql.Expression                 = (*nextval)(nil)
	_ sql.NonDeterministicExpression = (*nextval)(nil)
)

// Resolved implements sql.Expression.
func (n *nextval) Resolved() bool { return n.arg.Resolved() }

// String implements sql.Expression. The rendering matters beyond
// diagnostics: information_schema.columns.column_default shows a DEFAULT
// expression via String(), and PostgreSQL clients (gorm's migrator among
// them) pattern-match nextval defaults there to classify a column as
// auto-incrementing.
func (n *nextval) String() string { return fmt.Sprintf("nextval(%s)", n.arg) }

// Type implements sql.Expression.
func (n *nextval) Type(_ *sql.Context) sql.Type { return types.Int64 }

// IsNullable implements sql.Expression.
func (n *nextval) IsNullable(_ *sql.Context) bool { return false }

// IsNonDeterministic implements sql.NonDeterministicExpression: every call
// yields a fresh value, which keeps the analyzer from caching or folding
// the expression.
func (n *nextval) IsNonDeterministic() bool { return true }

// Children implements sql.Expression.
func (n *nextval) Children() []sql.Expression { return []sql.Expression{n.arg} }

// WithChildren implements sql.Expression.
func (n *nextval) WithChildren(_ *sql.Context, children ...sql.Expression) (sql.Expression, error) {
	if len(children) != 1 {
		return nil, sql.ErrInvalidChildrenNumber.New(n, len(children), 1)
	}
	return &nextval{provider: n.provider, arg: children[0]}, nil
}

// Eval implements sql.Expression.
func (n *nextval) Eval(ctx *sql.Context, row sql.Row) (interface{}, error) {
	value, err := n.arg.Eval(ctx, row)
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, nil
	}
	name, ok := value.(string)
	if !ok {
		if b, isBytes := value.([]byte); isBytes {
			name = string(b)
		} else {
			return nil, sql.ErrInvalidArgumentDetails.New("nextval", fmt.Sprintf("%v", value))
		}
	}

	db, err := n.provider.Database(ctx, ctx.GetCurrentDatabase())
	if err != nil {
		return nil, err
	}
	seqDB, ok := db.(sequenceValueDatabase)
	if !ok {
		return nil, sql.ErrDatabaseNotFound.New(ctx.GetCurrentDatabase())
	}
	return seqDB.NextSequenceValue(ctx, name)
}
