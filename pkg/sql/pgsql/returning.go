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

//go:build qrdb_pgsql

package pgsql

import (
	"regexp"
	"strings"

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/vitess/go/vt/sqlparser"
)

// buildReturningStatement parses translatedHead -- an UPDATE or DELETE
// statement already translated to the engine's dialect, with its RETURNING
// clause stripped -- and reattaches the clause's expression list to the
// AST's Returning field.
//
// This is the whole trick behind UPDATE/DELETE ... RETURNING on this wire:
// the embedded vitess grammar only parses RETURNING on INSERT, but the
// Update and Delete AST nodes carry a Returning field that go-mysql-server's
// planner (planbuilder/dml.go) and executor (rowexec/dml.go) fully honor
// when it is set programmatically. The clause's expressions are parsed by
// wrapping them in a throwaway SELECT, whose select list is grammatically
// identical to a RETURNING list.
func (h *ParseHandler) buildReturningStatement(translatedHead, returningList string) (sqlparser.Statement, error) {
	stmt, err := sqlparser.Parse(translatedHead)
	if err != nil {
		return nil, err
	}

	// The clause text is PostgreSQL-flavoured (double-quoted identifiers,
	// $N parameters); the translator turns it into the engine's dialect
	// exactly as it does for a real select list.
	translatedList, err := h.translator.Translate("SELECT " + returningList)
	if err != nil {
		return nil, err
	}
	selectStmt, err := sqlparser.Parse(translatedList)
	if err != nil {
		return nil, err
	}
	sel, ok := selectStmt.(*sqlparser.Select)
	if !ok {
		return nil, &TranslationError{Query: returningList}
	}

	switch node := stmt.(type) {
	case *sqlparser.Update:
		node.Returning = sel.SelectExprs
	case *sqlparser.Delete:
		node.Returning = sel.SelectExprs
	default:
		return nil, &TranslationError{Query: translatedHead}
	}
	return stmt, nil
}

// statementCarriesReturning reports whether a pre-parsed statement carries
// a reattached RETURNING clause, which is what makes a DML statement
// row-producing for RowDescription purposes.
func statementCarriesReturning(stmt sqlparser.Statement) bool {
	switch node := stmt.(type) {
	case *sqlparser.Update:
		return len(node.Returning) > 0
	case *sqlparser.Delete:
		return len(node.Returning) > 0
	}
	return false
}

// sequenceDDLRe matches the head of PostgreSQL's sequence DDL statements.
var sequenceDDLRe = regexp.MustCompile(
	`(?i)^\s*(CREATE|ALTER|DROP)\s+SEQUENCE\s+(?:IF\s+(?:NOT\s+)?EXISTS\s+)?` +
		`(?:(?:"[^"]+"|[A-Za-z_][A-Za-z0-9_]*)\s*\.\s*)?("[^"]+"|[A-Za-z_][A-Za-z0-9_]*)`)

// handleSequenceDDL executes PostgreSQL's CREATE/ALTER/DROP SEQUENCE
// against qrdb's sequence catalog, reporting whether the statement was one.
//
// qrdb sequences materialize on first use and start at 1 (see
// service.UserSequencer), which collapses the DDL surface: CREATE SEQUENCE
// and ALTER SEQUENCE are accepted as no-ops -- the sequence exists the
// moment nextval() first names it, and its type/start/increment options
// have no counterpart in a plain uint64 allocator -- while DROP SEQUENCE
// resets the counter so a re-created sequence starts at 1 again, exactly
// the observable effect PostgreSQL's drop-and-recreate has.
func (h *ParseHandler) handleSequenceDDL(sqlCtx *sql.Context, dbName, trimmed string) (handled bool, err error) {
	groups := sequenceDDLRe.FindStringSubmatch(trimmed)
	if groups == nil {
		return false, nil
	}
	verb := strings.ToUpper(groups[1])
	name := unquoteIdentifier(groups[2])

	if verb != "DROP" {
		return true, nil
	}

	db, err := h.sqlEngine.Analyzer.Catalog.Database(sqlCtx, dbName)
	if err != nil {
		return true, err
	}
	resetter, ok := db.(interface {
		ResetSequence(ctx *sql.Context, name string) error
	})
	if !ok {
		// A database without a sequence catalog has nothing to reset;
		// DROP SEQUENCE on it is as much a no-op as CREATE was.
		return true, nil
	}
	return true, resetter.ResetSequence(sqlCtx, name)
}

// addIdentityRe matches PostgreSQL's
// ALTER TABLE <t> ALTER [COLUMN] <c> ADD GENERATED {ALWAYS|BY DEFAULT} AS
// IDENTITY [(sequence options)], capturing the table, the column, and any
// explicit SEQUENCE NAME option.
var addIdentityRe = regexp.MustCompile(
	`(?is)^\s*ALTER\s+TABLE\s+(?:(?:"[^"]+"|[A-Za-z_][A-Za-z0-9_]*)\s*\.\s*)?("[^"]+"|[A-Za-z_][A-Za-z0-9_]*)` +
		`\s+ALTER\s+(?:COLUMN\s+)?("[^"]+"|[A-Za-z_][A-Za-z0-9_]*)` +
		`\s+ADD\s+GENERATED\s+(?:ALWAYS|BY\s+DEFAULT)\s+AS\s+IDENTITY` +
		`(?:\s*\((?:.*?SEQUENCE\s+NAME\s+(?:(?:"[^"]+"|[A-Za-z_][A-Za-z0-9_]*)\s*\.\s*)?("[^"]+"|[A-Za-z_][A-Za-z0-9_]*))?.*?\))?\s*;?\s*$`)

// rewriteAddIdentity rewrites ADD GENERATED ... AS IDENTITY into a
// sequence default on the column: an identity column IS an integer whose
// values come from an owned sequence, and that is exactly what a
// nextval default over qrdb's sequence catalog provides (sequences
// materialize on first use; see service.UserSequencer). The sequence takes
// the statement's explicit SEQUENCE NAME when one is given and PostgreSQL's
// implicit <table>_<column>_seq name otherwise. Start/increment/cache
// options have no counterpart in the plain allocator and are accepted
// silently, exactly as CREATE SEQUENCE's are.
func rewriteAddIdentity(_ *sql.Context, _ sql.Catalog, _, raw string) string {
	groups := addIdentityRe.FindStringSubmatch(raw)
	if groups == nil {
		return raw
	}
	table := unquoteIdentifier(groups[1])
	column := unquoteIdentifier(groups[2])
	sequence := unquoteIdentifier(groups[3])
	if sequence == "" {
		sequence = table + "_" + column + "_seq"
	}
	return "ALTER TABLE " + backtick(table) + " ALTER " + backtick(column) +
		" SET DEFAULT (NEXTVAL('" + sequence + "'))"
}
