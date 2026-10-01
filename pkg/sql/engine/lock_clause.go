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

	"github.com/dolthub/go-mysql-server/sql"
	ast "github.com/dolthub/vitess/go/vt/sqlparser"

	"github.com/jeremyhahn/go-qrdb/pkg/sql/lockclause"
)

// lockClauseParser teaches the engine's parser MySQL 8's FOR SHARE.
//
// The vitess grammar go-mysql-server ships knows FOR UPDATE and the older
// LOCK IN SHARE MODE, but not FOR SHARE -- the spelling MySQL 8 documents
// and the only one PostgreSQL has. Every text is passed through
// lockclause.Normalize before the inner parser sees it, which rewrites the
// clause outside literals and comments. The row-lock intent itself is read by
// pkg/sql/provider from the statement text (lockclause.Analyze), which
// applies the same normalisation, so the two always agree on which clause a
// statement carries. See docs/row-locking/.
type lockClauseParser struct {
	inner sql.Parser
}

var _ sql.Parser = (*lockClauseParser)(nil)

// newLockClauseParser wraps inner.
func newLockClauseParser(inner sql.Parser) sql.Parser {
	return &lockClauseParser{inner: inner}
}

// ParseSimple implements sql.Parser.
func (p *lockClauseParser) ParseSimple(query string) (ast.Statement, error) {
	return p.inner.ParseSimple(lockclause.Normalize(query).Query)
}

// Parse implements sql.Parser. The returned statement text and remainder are
// slices of the normalised text, which is what the engine goes on to bind
// and, for a multi-statement text, parse next.
func (p *lockClauseParser) Parse(ctx *sql.Context, query string, multi bool) (ast.Statement, string, string, error) {
	return p.inner.Parse(ctx, lockclause.Normalize(query).Query, multi)
}

// ParseWithOptions implements sql.Parser; see Parse.
func (p *lockClauseParser) ParseWithOptions(ctx context.Context, query string, delimiter rune, multi bool, options ast.ParserOptions) (ast.Statement, string, string, error) {
	return p.inner.ParseWithOptions(ctx, lockclause.Normalize(query).Query, delimiter, multi, options)
}

// ParseOneWithOptions implements sql.Parser. The returned index is an offset
// the caller applies to the text IT passed in, so it is mapped back from
// the normalised text to the original.
func (p *lockClauseParser) ParseOneWithOptions(ctx context.Context, query string, options ast.ParserOptions) (ast.Statement, int, error) {
	norm := lockclause.Normalize(query)
	stmt, idx, err := p.inner.ParseOneWithOptions(ctx, norm.Query, options)
	return stmt, norm.OriginalOffset(idx), err
}
