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

	"github.com/jeremyhahn/go-qrdb/pkg/sql/engine"
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
		nullable := !strings.EqualFold(action, "SET")
		return "ALTER TABLE " + groups[1] + " MODIFY " + groups[2] + " " +
			modifyColumnClause(col, col.Type.String(), nullable, col.Comment)
	}
	return translated
}

// modifyColumnClause renders everything after "MODIFY <col> " for a column:
// the type, the nullability, and -- because MySQL's MODIFY REPLACES the whole
// column definition rather than patching one attribute -- the existing
// DEFAULT, AUTO_INCREMENT, and COMMENT, which would otherwise be silently
// dropped by a statement that only meant to change one of the others.
func modifyColumnClause(col *sql.Column, typeText string, nullable bool, comment string) string {
	clause := typeText
	if nullable {
		clause += " NULL"
	} else {
		clause += " NOT NULL"
	}
	if col.Default != nil {
		clause += " DEFAULT " + col.Default.String()
	}
	if col.AutoIncrement {
		clause += " AUTO_INCREMENT"
	}
	if comment != "" {
		clause += " COMMENT " + quoteSQLString(comment)
	}
	return clause
}

// quoteSQLString renders s as a single-quoted SQL string literal.
func quoteSQLString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// pgIdent matches a PostgreSQL identifier as the client wrote it: a
// double-quoted form or a bare name. These pre-translation rewrites run on
// the RAW statement text, before pkg/sql/engine's translator has converted
// quoting -- see rewritePGOnlyDDL for why they cannot run after it.
const pgIdent = `"[^"]+"|[A-Za-z_][A-Za-z0-9_]*`

// alterColumnTypeRe matches PostgreSQL's
// ALTER TABLE <t> ALTER [COLUMN] <c> [SET DATA] TYPE <type> [USING <expr>],
// which MySQL has no spelling for at all -- its counterpart is MODIFY with a
// full column definition. gorm.io/driver/postgres emits exactly this for
// every column-type migration, always with a USING clause casting the column
// to the new type.
var alterColumnTypeRe = regexp.MustCompile(
	`(?i)^\s*ALTER\s+TABLE\s+(` + pgIdent + `)` +
		`\s+ALTER\s+(?:COLUMN\s+)?(` + pgIdent + `)` +
		`\s+(?:SET\s+DATA\s+)?TYPE\s+([A-Za-z_][A-Za-z0-9_ ]*(?:\([0-9, ]+\))?)` +
		`(?:\s+USING\s+(.+?))?\s*;?\s*$`)

// usingPlainCastRe recognises the USING expressions that are a plain cast
// of the column itself -- "col"::type (including a chained
// "col"::t1::t2, the shape gorm emits for smallint-to-boolean migrations
// as "col"::int::boolean) or CAST(col AS type) -- which carry no
// semantics beyond what MODIFY's own conversion performs: each step of a
// chain is a widening or truthiness cast of the SAME value, exactly what
// the engine's own type conversion applies row by row.
var usingPlainCastRe = regexp.MustCompile(
	`(?i)^\s*(?:(` + pgIdent + `)(?:\s*::\s*[A-Za-z_][A-Za-z0-9_ ]*(?:\([0-9, ]+\))?)+` +
		`|CAST\s*\(\s*(` + pgIdent + `)\s+AS\s+[A-Za-z_][A-Za-z0-9_ ]*(?:\([0-9, ]+\))?\s*\))\s*$`)

// rewriteAlterColumnType rewrites PostgreSQL's ALTER COLUMN ... TYPE into
// MySQL's MODIFY, preserving the column's current nullability, default,
// auto-increment, and comment (see modifyColumnClause -- MODIFY replaces the
// whole definition). It runs PRE-translation: the translator has no rule for
// this statement shape and mangles it instead (backticking the COLUMN and
// TYPE keywords as identifiers, and rewriting the USING cast into a CAST
// whose modifier lands outside the parentheses), so the raw text is the only
// form that can be matched reliably.
//
// The USING clause is honoured only when it is a plain cast of the same
// column, which is what MODIFY's own conversion already performs and the only
// form gorm emits. A USING carrying any other expression changes row VALUES
// in a way MODIFY cannot express, so the statement is left untouched for the
// engine to reject rather than silently altered to mean something else.
//
// The engine executes the produced MODIFY through the provider's
// RewriteInserter path, re-encoding every existing row under the new type --
// see pkg/sql/provider/relational_rewrite.go.
func rewriteAlterColumnType(sqlCtx *sql.Context, catalog sql.Catalog, dbName, translated string) string {
	groups := alterColumnTypeRe.FindStringSubmatch(translated)
	if groups == nil {
		return translated
	}
	tableName, columnName := unquoteIdentifier(groups[1]), unquoteIdentifier(groups[2])
	typeText, usingExpr := strings.TrimSpace(groups[3]), strings.TrimSpace(groups[4])

	if usingExpr != "" {
		castGroups := usingPlainCastRe.FindStringSubmatch(usingExpr)
		if castGroups == nil {
			return translated
		}
		castCol := castGroups[1]
		if castCol == "" {
			castCol = castGroups[2]
		}
		if !strings.EqualFold(unquoteIdentifier(castCol), columnName) {
			return translated
		}
	}

	table, ok := lookupTable(sqlCtx, catalog, dbName, tableName)
	if !ok {
		return translated
	}
	for _, col := range table.Schema(sqlCtx) {
		if !strings.EqualFold(col.Name, columnName) {
			continue
		}
		newType := engine.TranslateColumnTypeText(typeText)
		return "ALTER TABLE " + backtick(tableName) + " MODIFY " + backtick(columnName) + " " +
			modifyColumnClause(col, newType, col.Nullable, col.Comment)
	}
	return translated
}

// backtick renders a bare identifier in MySQL quoting.
func backtick(name string) string {
	return "`" + name + "`"
}

// commentOnColumnRe matches PostgreSQL's COMMENT ON COLUMN <t>.<c> IS
// <'text'|NULL>. MySQL spells the same thing as a MODIFY carrying a COMMENT
// attribute, so the rewrite restates the column with everything it already
// has plus the new comment; IS NULL clears it.
var commentOnColumnRe = regexp.MustCompile(
	`(?i)^\s*COMMENT\s+ON\s+COLUMN\s+` +
		`(` + pgIdent + `)\.(` + pgIdent + `)` +
		`\s+IS\s+('(?:[^']|'')*'|NULL)\s*;?\s*$`)

// rewriteCommentOnColumn rewrites COMMENT ON COLUMN into the MODIFY MySQL
// requires. The comment text keeps its quoted form verbatim (PostgreSQL and
// MySQL agree on ” as the escape for a quote inside a single-quoted
// literal).
func rewriteCommentOnColumn(sqlCtx *sql.Context, catalog sql.Catalog, dbName, translated string) string {
	groups := commentOnColumnRe.FindStringSubmatch(translated)
	if groups == nil {
		return translated
	}
	tableName, columnName := unquoteIdentifier(groups[1]), unquoteIdentifier(groups[2])
	commentLit := groups[3]

	table, ok := lookupTable(sqlCtx, catalog, dbName, tableName)
	if !ok {
		return translated
	}
	for _, col := range table.Schema(sqlCtx) {
		if !strings.EqualFold(col.Name, columnName) {
			continue
		}
		clause := modifyColumnClause(col, col.Type.String(), col.Nullable, "")
		if !strings.EqualFold(commentLit, "NULL") {
			clause += " COMMENT " + commentLit
		}
		return "ALTER TABLE " + backtick(tableName) + " MODIFY " + backtick(columnName) + " " + clause
	}
	return translated
}

// alterColumnDefaultRe matches PostgreSQL's
// ALTER TABLE <t> ALTER [COLUMN] <c> {SET DEFAULT <expr> | DROP DEFAULT}.
// go-mysql-server's grammar accepts exactly this statement -- the only reason
// it cannot pass through untouched is the translator's reserved-word step,
// which backticks the COLUMN keyword as if it were an identifier. The rewrite
// re-emits the statement in MySQL quoting with the optional COLUMN keyword
// omitted, leaving the default expression verbatim.
var alterColumnDefaultRe = regexp.MustCompile(
	`(?i)^\s*ALTER\s+TABLE\s+(` + pgIdent + `)` +
		`\s+ALTER\s+(?:COLUMN\s+)?(` + pgIdent + `)` +
		`\s+(SET\s+DEFAULT\s+.+?|DROP\s+DEFAULT)\s*;?\s*$`)

// rewriteAlterColumnDefault re-quotes the SET/DROP DEFAULT form. It needs no
// catalog: nothing about the column is restated.
func rewriteAlterColumnDefault(_ *sql.Context, _ sql.Catalog, _ string, raw string) string {
	groups := alterColumnDefaultRe.FindStringSubmatch(raw)
	if groups == nil {
		return raw
	}
	return "ALTER TABLE " + backtick(unquoteIdentifier(groups[1])) +
		" ALTER " + backtick(unquoteIdentifier(groups[2])) + " " + strings.TrimSpace(groups[3])
}

// rewritePGOnlyDDL applies the rewrites that must see the RAW statement,
// before the translator touches it. The second return reports whether a
// rewrite fired; when it did, the produced statement is complete MySQL and
// the caller must NOT run the translator over it -- the translator would
// backtick its keywords all over again.
func rewritePGOnlyDDL(sqlCtx *sql.Context, catalog sql.Catalog, dbName, raw string) (string, bool) {
	for _, rewrite := range []func(*sql.Context, sql.Catalog, string, string) string{
		rewriteAlterColumnType,
		rewriteAlterColumnDefault,
		rewriteCommentOnColumn,
		rewriteDropIndex,
		rewriteAlterIndexRename,
		rewriteGormIndexIntrospection,
		rewriteAddIdentity,
	} {
		if out := rewrite(sqlCtx, catalog, dbName, raw); out != raw {
			return out, true
		}
	}
	return raw, false
}

// unquoteIdentifier strips the quoting from an identifier -- the backticks
// pkg/sql/engine's translator puts around one, or the double quotes a
// PostgreSQL client wrote (the pre-translation rewrites match the raw text).
// A bare name passes through unchanged.
func unquoteIdentifier(name string) string {
	return strings.Trim(name, "`\"")
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

	// Foreign keys are resolved BEFORE indexes: an engine-side foreign key
	// carries a backing index under the same name, so a name matching both
	// is the constraint, and rewriting it as DROP INDEX would be refused
	// with "cannot drop index: used by foreign key".
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

// dropTableCascadeRe matches DROP TABLE [IF EXISTS] <table>[, <table>...]
// CASCADE. Only the CASCADE form is matched: a plain DROP TABLE keeps
// engine (RESTRICT) semantics, which refuse to drop a table other tables
// still reference.
var dropTableCascadeRe = regexp.MustCompile(
	`(?i)^\s*DROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?((?:"[^"]+"|[A-Za-z_][A-Za-z0-9_]*)(?:\s*,\s*(?:"[^"]+"|[A-Za-z_][A-Za-z0-9_]*))*)\s+CASCADE\s*;?\s*$`)

// dropTableCascadeEffect implements the CASCADE half of PostgreSQL's DROP
// TABLE: its effect, run at each execution of the statement, drops every
// foreign key on OTHER tables that references a table being dropped, so the
// DROP itself then passes the engine's referenced-by validation. It returns
// nil for any other statement.
//
// The effect acts by side effect and leaves the statement text untouched -- the
// engine parses the trailing CASCADE fine; it is only the incoming
// references that made execution refuse. Failures resolving or dropping a
// constraint are deliberately swallowed: the DROP TABLE that follows will
// then fail with the engine's own referenced-by error, which is strictly
// more informative than anything this helper could synthesize.
func dropTableCascadeEffect(catalog sql.Catalog, raw string) statementEffect {
	groups := dropTableCascadeRe.FindStringSubmatch(raw)
	if groups == nil {
		return nil
	}

	dropped := make(map[string]bool)
	for _, name := range strings.Split(groups[1], ",") {
		dropped[strings.ToLower(unquoteIdentifier(strings.TrimSpace(name)))] = true
	}
	return func(sqlCtx *sql.Context) error {
		dropForeignKeysTo(sqlCtx, catalog, sqlCtx.GetCurrentDatabase(), dropped)
		return nil
	}
}

// dropForeignKeysTo drops every foreign key of database dbName that aims at
// one of the dropped tables from a table that is not itself dropped.
func dropForeignKeysTo(sqlCtx *sql.Context, catalog sql.Catalog, dbName string, dropped map[string]bool) {
	db, err := catalog.Database(sqlCtx, dbName)
	if err != nil {
		return
	}
	tableNames, err := db.GetTableNames(sqlCtx)
	if err != nil {
		return
	}
	for _, tableName := range tableNames {
		if dropped[strings.ToLower(tableName)] {
			// The table is going away with the DROP; its outgoing
			// constraints die with it.
			continue
		}
		table, ok := lookupTable(sqlCtx, catalog, dbName, tableName)
		if !ok {
			continue
		}
		fkTable, ok := table.(sql.ForeignKeyTable)
		if !ok {
			continue
		}
		fks, err := fkTable.GetDeclaredForeignKeys(sqlCtx)
		if err != nil {
			continue
		}
		for _, fk := range fks {
			if dropped[strings.ToLower(fk.ParentTable)] {
				_ = fkTable.DropForeignKey(sqlCtx, fk.Name, "", "")
			}
		}
	}
}

// dropIndexRe matches PostgreSQL's DROP INDEX [CONCURRENTLY] [IF EXISTS]
// [<schema>.]<name>, capturing the index name. The schema qualifier
// gorm.io/driver/postgres writes is the literal call CURRENT_SCHEMA(),
// which the translator would mangle to DATABASE(), so this rewrite must
// run before translation.
var dropIndexRe = regexp.MustCompile(
	`(?i)^\s*DROP\s+INDEX\s+(?:CONCURRENTLY\s+)?(?:IF\s+EXISTS\s+)?` +
		`(?:(?:CURRENT_SCHEMA\(\)|"[^"]+"|[A-Za-z_][A-Za-z0-9_]*)\s*\.\s*)?` +
		`("[^"]+"|[A-Za-z_][A-Za-z0-9_]*)\s*;?\s*$`)

// rewriteDropIndex rewrites PostgreSQL's DROP INDEX into MySQL's
// table-scoped form. PostgreSQL indexes live in a schema-wide namespace, so
// the statement names no table; MySQL's DROP INDEX needs one. The owning
// table is resolved by searching the database's tables for the index name.
// An index that resolves to no table is left alone so the engine reports
// its own error for the original statement.
func rewriteDropIndex(sqlCtx *sql.Context, catalog sql.Catalog, dbName, raw string) string {
	groups := dropIndexRe.FindStringSubmatch(raw)
	if groups == nil {
		return raw
	}
	indexName := unquoteIdentifier(groups[1])

	db, err := catalog.Database(sqlCtx, dbName)
	if err != nil {
		return raw
	}
	tableNames, err := db.GetTableNames(sqlCtx)
	if err != nil {
		return raw
	}
	for _, tableName := range tableNames {
		table, ok := lookupTable(sqlCtx, catalog, dbName, tableName)
		if !ok {
			continue
		}
		indexed, ok := table.(sql.IndexAddressableTable)
		if !ok {
			continue
		}
		indexes, err := indexed.GetIndexes(sqlCtx)
		if err != nil {
			continue
		}
		for _, idx := range indexes {
			if strings.EqualFold(idx.ID(), indexName) {
				return "ALTER TABLE " + backtick(tableName) +
					" DROP INDEX " + backtick(indexName)
			}
		}
	}
	return raw
}

// alterIndexRenameRe matches PostgreSQL's
// ALTER INDEX [IF EXISTS] [<schema>.]<old> RENAME TO <new>, the statement
// gorm.io/driver/postgres emits for Migrator.RenameIndex. As with DROP
// INDEX, the schema qualifier is usually the literal CURRENT_SCHEMA().
var alterIndexRenameRe = regexp.MustCompile(
	`(?i)^\s*ALTER\s+INDEX\s+(?:IF\s+EXISTS\s+)?` +
		`(?:(?:CURRENT_SCHEMA\(\)|"[^"]+"|[A-Za-z_][A-Za-z0-9_]*)\s*\.\s*)?` +
		`("[^"]+"|[A-Za-z_][A-Za-z0-9_]*)\s+RENAME\s+TO\s+` +
		`("[^"]+"|[A-Za-z_][A-Za-z0-9_]*)\s*;?\s*$`)

// rewriteAlterIndexRename rewrites PostgreSQL's schema-scoped ALTER INDEX
// ... RENAME TO into MySQL's table-scoped ALTER TABLE ... RENAME INDEX,
// resolving the owning table the same way rewriteDropIndex does. An index
// that resolves to no table is left alone for the engine to report.
func rewriteAlterIndexRename(sqlCtx *sql.Context, catalog sql.Catalog, dbName, raw string) string {
	groups := alterIndexRenameRe.FindStringSubmatch(raw)
	if groups == nil {
		return raw
	}
	oldName, newName := unquoteIdentifier(groups[1]), unquoteIdentifier(groups[2])

	db, err := catalog.Database(sqlCtx, dbName)
	if err != nil {
		return raw
	}
	tableNames, err := db.GetTableNames(sqlCtx)
	if err != nil {
		return raw
	}
	for _, tableName := range tableNames {
		table, ok := lookupTable(sqlCtx, catalog, dbName, tableName)
		if !ok {
			continue
		}
		indexed, ok := table.(sql.IndexAddressableTable)
		if !ok {
			continue
		}
		indexes, err := indexed.GetIndexes(sqlCtx)
		if err != nil {
			continue
		}
		for _, idx := range indexes {
			if strings.EqualFold(idx.ID(), oldName) {
				return "ALTER TABLE " + backtick(tableName) +
					" RENAME INDEX " + backtick(oldName) + " TO " + backtick(newName)
			}
		}
	}
	return raw
}

// gormIndexIntrospectionRe recognises the pg_index-based per-column index
// query gorm.io/driver/postgres's Migrator.GetIndexes issues (a constant
// in that driver). Its pg_index/pg_constraint join and `= ANY(indkey)`
// int2vector membership test cannot be served by this engine, so the whole
// statement is substituted -- see rewriteGormIndexIntrospection.
var gormIndexIntrospectionRe = regexp.MustCompile(
	`(?is)^\s*SELECT\s+ct\.relname\s+AS\s+table_name,.*i\.indisprimary\s+AS\s+primary,.*FROM\s+pg_index\s+i\b.*ct\.relname\s*=\s*\$1`)

// rewriteGormIndexIntrospection substitutes gorm's pg_index join with an
// equivalent scan of pkg/sql/pgcatalog's qrdb_index_columns relation, which
// serves the same five columns under the same names with the upstream
// query's own filters (no primary key, no constraint-backed unique, no
// foreign-key backing index) already applied. The single bind parameter --
// the table name -- is preserved positionally.
func rewriteGormIndexIntrospection(_ *sql.Context, _ sql.Catalog, _, raw string) string {
	if !gormIndexIntrospectionRe.MatchString(raw) {
		return raw
	}
	return "SELECT `table_name`, `index_name`, `non_unique`, `primary`, `column_name`" +
		" FROM `qrdb_index_columns` WHERE `table_name` = ?"
}
