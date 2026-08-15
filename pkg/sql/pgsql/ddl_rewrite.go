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
	"strconv"
	"strings"

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
)

// rewriteCatalogAwareDDL applies each catalog-aware PostgreSQL DDL rewrite in
// turn, returning the first one that changes the statement.
//
// pkg/sql/engine's translator handles every PostgreSQL form that can be
// rewritten by looking at the statement alone. The forms here cannot: each
// needs something only the catalog knows -- a column's current type, whether
// a name is an index, whether a column is text-shaped. They live beside
// inferParamOIDs, which reads the catalog for the same kind of reason.
//
// Every rewrite returns its input unchanged when it cannot resolve what it
// needs, so an unresolvable statement is reported by the engine against what
// the caller actually wrote rather than against a guess. The forms are
// mutually exclusive, so the order they are tried in carries no meaning.
func rewriteCatalogAwareDDL(sqlCtx *sql.Context, catalog sql.Catalog, dbName, translated string) string {
	for _, rewrite := range []func(*sql.Context, sql.Catalog, string, string) string{
		rewriteAlterColumnNullability,
		rewriteDropConstraint,
		rewriteCreateIndexKeyLength,
	} {
		if out := rewrite(sqlCtx, catalog, dbName, translated); out != translated {
			return out
		}
	}
	return translated
}

// alterColumnNullabilityRe matches PostgreSQL's
// ALTER TABLE <table> ALTER COLUMN <column> {SET|DROP} NOT NULL, capturing the
// table, the column, and which of the two it is.
//
// Identifiers are matched both bare and backtick-quoted because this runs on
// the TRANSLATED statement, by which point pkg/sql/engine has already
// converted PostgreSQL's double quotes to backticks.
var alterColumnNullabilityRe = regexp.MustCompile(
	"(?i)^\\s*ALTER\\s+TABLE\\s+(`[^`]+`|[A-Za-z_][A-Za-z0-9_]*)" +
		"\\s+ALTER\\s+(?:COLUMN\\s+)?(`[^`]+`|[A-Za-z_][A-Za-z0-9_]*)" +
		"\\s+(SET|DROP)\\s+NOT\\s+NULL\\s*;?\\s*$")

// rewriteAlterColumnNullability rewrites PostgreSQL's
// ALTER COLUMN … SET/DROP NOT NULL into MySQL's MODIFY, which is the only
// spelling go-mysql-server accepts.
//
// # Why this is not in the translator
//
// MySQL has no statement that changes a column's nullability alone: MODIFY
// restates the column's whole definition, so the rewrite has to name the
// column's CURRENT type, and that is not in the statement text. Everything in
// pkg/sql/engine's translator is a pure string transformation by design;
// resolving a type means reading the catalog, which only a layer holding an
// engine can do. This one therefore lives here, beside inferParamOIDs, which
// consults the catalog for the same kind of reason.
//
// A statement whose table or column cannot be resolved is returned unchanged
// rather than guessed at, so the engine reports what it could not find
// against the caller's own statement.
func rewriteAlterColumnNullability(sqlCtx *sql.Context, catalog sql.Catalog, dbName, translated string) string {
	groups := alterColumnNullabilityRe.FindStringSubmatch(translated)
	if groups == nil {
		return translated
	}
	tableName, columnName, action := unquoteIdentifier(groups[1]), unquoteIdentifier(groups[2]), groups[3]

	table, ok := lookupTable(sqlCtx, catalog, dbName, tableName)
	if !ok {
		return translated
	}

	for _, col := range table.Schema(sqlCtx) {
		if !strings.EqualFold(col.Name, columnName) {
			continue
		}
		nullability := "NULL"
		if strings.EqualFold(action, "SET") {
			nullability = "NOT NULL"
		}
		return "ALTER TABLE " + groups[1] + " MODIFY " + groups[2] + " " +
			col.Type.String() + " " + nullability
	}
	return translated
}

// unquoteIdentifier strips the backticks pkg/sql/engine's translator puts
// around a quoted identifier, leaving a bare name unchanged.
func unquoteIdentifier(name string) string {
	return strings.Trim(name, "`")
}

// dropConstraintRe matches PostgreSQL's
// ALTER TABLE <table> DROP CONSTRAINT <name>, capturing both.
var dropConstraintRe = regexp.MustCompile(
	"(?i)^\\s*ALTER\\s+TABLE\\s+(`[^`]+`|[A-Za-z_][A-Za-z0-9_]*)" +
		"\\s+DROP\\s+CONSTRAINT\\s+(?:IF\\s+EXISTS\\s+)?(`[^`]+`|[A-Za-z_][A-Za-z0-9_]*)\\s*;?\\s*$")

// rewriteDropConstraint rewrites PostgreSQL's DROP CONSTRAINT into the MySQL
// statement that drops the same object.
//
// PostgreSQL has one namespace for constraints, so one statement drops any of
// them. MySQL has three: a unique constraint is an index and is dropped with
// DROP INDEX, a foreign key is dropped with DROP FOREIGN KEY, and only a
// CHECK constraint is dropped with DROP CHECK. go-mysql-server routes the
// PostgreSQL spelling to the last of those, so a unique constraint --
// which is what gorm.io/driver/postgres drops when it removes a unique index
// -- failed with "the table does not support check constraint operations".
//
// The name is resolved against the table to decide which it is. A name that
// matches neither an index nor a foreign key is left alone: it may be a real
// CHECK constraint, which the original statement already spells correctly.
func rewriteDropConstraint(sqlCtx *sql.Context, catalog sql.Catalog, dbName, translated string) string {
	groups := dropConstraintRe.FindStringSubmatch(translated)
	if groups == nil {
		return translated
	}
	tableName, constraintName := unquoteIdentifier(groups[1]), unquoteIdentifier(groups[2])

	table, ok := lookupTable(sqlCtx, catalog, dbName, tableName)
	if !ok {
		return translated
	}

	if indexed, ok := table.(sql.IndexAddressable); ok {
		indexes, err := indexed.GetIndexes(sqlCtx)
		if err == nil {
			for _, index := range indexes {
				if strings.EqualFold(index.ID(), constraintName) {
					return "ALTER TABLE " + groups[1] + " DROP INDEX " + groups[2]
				}
			}
		}
	}

	if fkTable, ok := table.(sql.ForeignKeyTable); ok {
		fks, err := fkTable.GetDeclaredForeignKeys(sqlCtx)
		if err == nil {
			for _, fk := range fks {
				if strings.EqualFold(fk.Name, constraintName) {
					return "ALTER TABLE " + groups[1] + " DROP FOREIGN KEY " + groups[2]
				}
			}
		}
	}

	return translated
}

// createIndexRe matches CREATE [UNIQUE] INDEX [IF NOT EXISTS] <name> ON
// <table> (<columns>), capturing the table and the parenthesised column list.
var createIndexRe = regexp.MustCompile(
	"(?i)^\\s*CREATE\\s+(?:UNIQUE\\s+)?INDEX\\s+(?:IF\\s+NOT\\s+EXISTS\\s+)?" +
		"(?:`[^`]+`|[A-Za-z_][A-Za-z0-9_]*)\\s+ON\\s+(`[^`]+`|[A-Za-z_][A-Za-z0-9_]*)" +
		"\\s*\\(([^)]*)\\)\\s*;?\\s*$")

// indexKeyPrefixLength is the prefix length given to a text column that is
// indexed after the fact.
//
// It matches the bound pkg/sql/engine already applies to a text column that
// participates in a key at CREATE TABLE time (keyedTextType), so a column
// indexed later is indexed to the same width as one indexed up front rather
// than to some second, arbitrary bound.
const indexKeyPrefixLength = 768

// rewriteCreateIndexKeyLength adds a prefix length to each text-shaped column
// in a CREATE INDEX statement.
//
// PostgreSQL's `text` maps to MySQL's LONGTEXT (see pkg/sql/engine's type
// table), and MySQL refuses to index a TEXT or BLOB column without a prefix
// length -- "blob/text column used in key specification without a key
// length". CREATE TABLE already handles this by bounding a text column that
// participates in a key, but a column indexed by a LATER statement is already
// LONGTEXT by then, and only a prefix length can index it.
//
// A column that is not text-shaped is left exactly as written, so an ordinary
// index is unaffected.
func rewriteCreateIndexKeyLength(sqlCtx *sql.Context, catalog sql.Catalog, dbName, translated string) string {
	groups := createIndexRe.FindStringSubmatch(translated)
	if groups == nil {
		return translated
	}

	table, ok := lookupTable(sqlCtx, catalog, dbName, unquoteIdentifier(groups[1]))
	if !ok {
		return translated
	}
	schema := table.Schema(sqlCtx)

	columns := strings.Split(groups[2], ",")
	changed := false
	for i, column := range columns {
		name := strings.TrimSpace(column)
		if name == "" || strings.Contains(name, "(") {
			continue // already carries a prefix length, or is not a plain column
		}
		if !columnNeedsKeyPrefix(schema, unquoteIdentifier(name)) {
			continue
		}
		columns[i] = " " + name + "(" + strconv.Itoa(indexKeyPrefixLength) + ")"
		changed = true
	}
	if !changed {
		return translated
	}

	return strings.Replace(translated, "("+groups[2]+")", "("+strings.Join(columns, ",")+")", 1)
}

// columnNeedsKeyPrefix reports whether the named column is one MySQL refuses
// to index without a prefix length.
func columnNeedsKeyPrefix(schema sql.Schema, name string) bool {
	for _, col := range schema {
		if !strings.EqualFold(col.Name, name) {
			continue
		}
		return types.IsTextBlob(col.Type)
	}
	return false
}
