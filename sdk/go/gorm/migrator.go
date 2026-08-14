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

package gorm

import (
	"database/sql"
	"strconv"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/migrator"
	"gorm.io/gorm/schema"
)

// indexColumnsSQL selects every column participating in every index on one
// table, ordered so that a caller grouping consecutive rows by INDEX_NAME
// reconstructs each index's column list in its declared (SEQ_IN_INDEX)
// order. Mirrors gorm.io/driver/mysql's own indexSql.
const indexColumnsSQL = `SELECT
	TABLE_NAME,
	COLUMN_NAME,
	INDEX_NAME,
	NON_UNIQUE
FROM information_schema.STATISTICS
WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
ORDER BY INDEX_NAME, SEQ_IN_INDEX`

// columnTypesSQL selects one row per column of one table from
// information_schema.columns, in declared order, carrying everything
// ColumnTypes needs to populate a migrator.ColumnType without a second
// round trip per column.
const columnTypesSQL = `SELECT
	column_name,
	column_default,
	is_nullable = 'YES',
	data_type,
	character_maximum_length,
	column_type,
	column_key,
	extra,
	column_comment,
	numeric_precision,
	numeric_scale,
	datetime_precision
FROM information_schema.columns
WHERE table_schema = ? AND table_name = ?
ORDER BY ORDINAL_POSITION`

// typeAliases maps a canonical database type name (as
// information_schema.columns.DATA_TYPE and gorm.ColumnType.DatabaseTypeName
// report it — go-mysql-server lower-cases and strips both the parenthesized
// length/precision and any " unsigned"/" auto_increment" suffix, e.g.
// "tinyint" for a column created BOOLEAN, "int" for one created INTEGER) to
// the alternate spellings GetTypeAliases must treat as the same type.
//
// Every entry exists to keep MigrateColumn from reporting a spurious
// mismatch — and therefore re-issuing ALTER TABLE ... MODIFY COLUMN on
// every AutoMigrate call — for a column whose declared type and reported
// type are the same SQL type under two different names:
//
//   - "tinyint"/"bool": DataTypeOf maps schema.Bool to the literal string
//     "boolean", but qrdb's catalog (pkg/sql/catalog.ColumnTypeOf) resolves
//     BOOLEAN to the same go-mysql-server type as TINYINT(1), so
//     information_schema reports the column back as "tinyint".
//   - "int"/"integer": a gorm:"type:integer" tag (the SQL-standard spelling)
//     names the same type DataTypeOf's own "int" does; information_schema
//     always reports "int" (go-mysql-server's canonical name), never
//     "integer".
//   - "bigint"/"int8": "int8" is PostgreSQL's spelling for the same 8-byte
//     integer MySQL calls BIGINT; a gorm:"type:int8" tag written for
//     cross-dialect portability must not read as an alter-worthy change
//     under qrdb's MySQL-flavored catalog.
//   - "varchar"/"char"/"text": CHAR and TEXT are both textual types a
//     gorm:"type:..." tag might spell explicitly for a column
//     DataTypeOf/stringType would otherwise have sized as VARCHAR(n).
//   - "datetime"/"timestamp": interchangeable temporal spellings; qrdb's
//     catalog and DataTypeOf's timeType both always emit DATETIME, but a
//     literal gorm:"type:timestamp" tag names the same underlying type.
//   - "blob"/"binary"/"varbinary": interchangeable binary spellings, the
//     Bytes/BLOB analog of the varchar/char/text group above.
//   - "decimal"/"numeric": NUMERIC is SQL-standard for the same fixed-point
//     type MySQL calls DECIMAL; floatType only ever emits "decimal", but a
//     gorm:"type:numeric" tag names the same type.
//
// Each pair is listed in both directions — not just the direction
// MigrateColumn's current lookup (keyed on the realDataType information_
// schema reports) actually consults — so this table stays correct if that
// lookup direction ever changes, and so it reads as the equivalence table
// it conceptually is rather than a lookup coupled to one caller's
// implementation detail.
var typeAliases = map[string][]string{
	"bool":      {"tinyint"},
	"tinyint":   {"bool"},
	"int":       {"integer"},
	"integer":   {"int"},
	"bigint":    {"int8"},
	"int8":      {"bigint"},
	"varchar":   {"char", "text"},
	"char":      {"varchar"},
	"text":      {"varchar"},
	"datetime":  {"timestamp"},
	"timestamp": {"datetime"},
	"blob":      {"binary", "varbinary"},
	"binary":    {"blob"},
	"varbinary": {"blob"},
	"decimal":   {"numeric"},
	"numeric":   {"decimal"},
}

// RenameIndexNotSupportedError is returned by Migrator.RenameIndex when the
// caller has set Dialector.DontSupportRenameIndex true — not qrdb's own
// default (see Config), since qrdb's engine genuinely accepts a real ALTER
// TABLE ... RENAME INDEX (see docs/sql/README.md's "Relational Tables"
// section) and RenameIndex issues exactly that by default.
//
// go-gorm/mysql sets this flag itself, dynamically, for MySQL/MariaDB
// server versions that predate RENAME INDEX, and recovers by dropping and
// recreating the index under its new name instead of returning an error.
// qrdb has no such version-dependent gap and no fallback to reproduce, so a
// caller who sets DontSupportRenameIndex true anyway — to test how their
// application behaves against an older-server dialect, for instance — gets
// this clear, typed refusal instead of a confusing engine-side error.
type RenameIndexNotSupportedError struct {
	// OldName and NewName are the index names the caller asked to rename
	// between.
	OldName, NewName string
}

// Error returns a human-readable description of the refused rename.
func (e *RenameIndexNotSupportedError) Error() string {
	return "gorm: qrdb migrator: RenameIndex(" + strconv.Quote(e.OldName) + ", " +
		strconv.Quote(e.NewName) + ") is not supported with Dialector.DontSupportRenameIndex set"
}

// Migrator is qrdb's gorm.Migrator. It embeds gorm.io/gorm/migrator.Migrator
// for every method whose default SQL — driven entirely by Dialector's
// DataTypeOf/FullDataTypeOf and QuoteTo — already matches what qrdb's
// go-mysql-server engine accepts (AutoMigrate, CreateTable,
// RenameTable, AddColumn, DropColumn, MigrateColumn,
// MigrateColumnUnique, CreateView, DropView), and overrides the rest: the
// information_schema-backed introspection methods (CurrentDatabase,
// HasTable, GetTables, TableType, HasColumn, ColumnTypes, HasIndex,
// GetIndexes, HasConstraint), MySQL-specific statement shapes the base
// migrator gets wrong for this dialect (AlterColumn, RenameColumn,
// DropIndex, GetTypeAliases), DropTable (which must disable foreign key
// checking on a pinned connection — see its doc comment), RenameIndex (real
// by default, but able to report *RenameIndexNotSupportedError on request —
// see Config.DontSupportRenameIndex), and DropConstraint, whose statement
// shape the base migrator gets wrong for this engine.
//
// FOREIGN KEY needs no special handling: qrdb's relational tables implement
// go-mysql-server's sql.ForeignKeyTable, so ADD/DROP FOREIGN KEY is an
// ordinary statement the engine resolves and enforces.
type Migrator struct {
	migrator.Migrator
	Dialector
}

// Compile-time interface assertion.
var _ gorm.Migrator = Migrator{}

// Migrator returns db's schema migrator, wired with dialector as both the
// SQL-generation Dialector and the embedded base migrator.Migrator's own
// Dialector.
func (dialector Dialector) Migrator(db *gorm.DB) gorm.Migrator {
	return Migrator{
		Migrator: migrator.Migrator{
			Config: migrator.Config{
				DB:        db,
				Dialector: dialector,
			},
		},
		Dialector: dialector,
	}
}

// FullDataTypeOf returns field's full column-definition expression,
// appending a COMMENT clause when the field carries a gorm:"comment:..."
// tag — matching go-gorm/mysql, since qrdb's MySQL-flavored DDL accepts the
// same inline COMMENT syntax the base migrator.Migrator.FullDataTypeOf
// does not add on its own.
func (m Migrator) FullDataTypeOf(field *schema.Field) clause.Expr {
	expr := m.Migrator.FullDataTypeOf(field)

	if value, ok := field.TagSettings["COMMENT"]; ok {
		expr.SQL += " COMMENT " + m.Dialector.Explain("?", value)
	}

	return expr
}

// GetTypeAliases returns the alternate spellings typeAliases records as
// equivalent to databaseTypeName, so MigrateColumn does not mistake a
// column for having changed type when its declared type and its reported
// type are the same SQL type under two different names. See typeAliases'
// doc comment for why each pair exists and the Known bug this fixes: without
// it, AutoMigrate would re-ALTER every BOOLEAN (reported back as "tinyint")
// column on every single run.
func (m Migrator) GetTypeAliases(databaseTypeName string) []string {
	return typeAliases[databaseTypeName]
}

// CurrentDatabase returns the name of the database the underlying
// connection is currently using, via go-mysql-server's DATABASE() builtin —
// the same session value sdk/go/driver's embedded connector seeds from the
// DSN's database query parameter (or that a MySQL/PostgreSQL wire
// connection's own session tracks).
func (m Migrator) CurrentDatabase() (name string) {
	m.DB.Raw("SELECT DATABASE()").Row().Scan(&name)
	return
}

// HasTable reports whether dst's table exists in the current database,
// via information_schema.tables. dst may be a struct/pointer GORM can
// resolve a table name from, or a bare table name string.
func (m Migrator) HasTable(dst interface{}) bool {
	var count int64
	m.RunWithValue(dst, func(stmt *gorm.Statement) error {
		return m.DB.Raw(
			"SELECT count(*) FROM information_schema.tables WHERE table_schema = ? AND table_name = ? AND table_type = ?",
			m.CurrentDatabase(), stmt.Table, "BASE TABLE",
		).Row().Scan(&count)
	})
	return count > 0
}

// GetTables returns the names of every base table in the current database,
// via information_schema.tables.
func (m Migrator) GetTables() (tableList []string, err error) {
	err = m.DB.Raw(
		"SELECT TABLE_NAME FROM information_schema.tables WHERE table_schema = ? AND table_type = ?",
		m.CurrentDatabase(), "BASE TABLE",
	).Scan(&tableList).Error
	return
}

// TableType returns dst's table metadata (schema, name, engine-reported
// type, comment) from information_schema.tables.
func (m Migrator) TableType(dst interface{}) (tableType gorm.TableType, err error) {
	var table migrator.TableType
	err = m.RunWithValue(dst, func(stmt *gorm.Statement) error {
		return m.DB.Raw(
			"SELECT table_schema, table_name, table_type, table_comment FROM information_schema.tables WHERE table_schema = ? AND table_name = ?",
			m.CurrentDatabase(), stmt.Table,
		).Row().Scan(&table.SchemaValue, &table.NameValue, &table.TypeValue, &table.CommentValue)
	})
	return table, err
}

// DropTable drops every table in values, in reverse dependency order, with
// foreign key checking disabled for the duration.
//
// Both halves matter, and neither is what the base migrator.Migrator does:
//
//   - foreign_key_checks. qrdb's engine refuses to drop a table another
//     table still references (see go-mysql-server's ErrForeignKeyDropTable),
//     exactly as MySQL does. Dropping a set of mutually-referencing tables
//     is therefore impossible one statement at a time in any order at all
//     whenever the set contains a reference cycle -- and, for an acyclic
//     set, impossible if a table OUTSIDE the set still references one inside
//     it, which is the ordinary case when a caller drops only part of a
//     schema. MySQL's answer, which gorm.io/driver/mysql's own Migrator
//     issues for precisely this reason, is to turn the check off around the
//     drops; qrdb's engine honours the same session variable.
//
//   - A pinned connection. foreign_key_checks is SESSION-scoped, so setting
//     it on one pooled connection and issuing the DROP on another would
//     leave the check on. DB.Connection pins one connection for the whole
//     sequence.
//
// The check is restored even when a drop fails, so a failed DropTable never
// leaves the caller's connection silently unprotected.
func (m Migrator) DropTable(values ...interface{}) error {
	values = m.ReorderModels(values, false)
	return m.DB.Connection(func(tx *gorm.DB) error {
		if err := tx.Exec("SET FOREIGN_KEY_CHECKS = 0").Error; err != nil {
			return err
		}

		var dropErr error
		for i := len(values) - 1; i >= 0 && dropErr == nil; i-- {
			dropErr = m.RunWithValue(values[i], func(stmt *gorm.Statement) error {
				return tx.Exec("DROP TABLE IF EXISTS ?", m.CurrentTable(stmt)).Error
			})
		}

		if err := tx.Exec("SET FOREIGN_KEY_CHECKS = 1").Error; err != nil && dropErr == nil {
			dropErr = err
		}
		return dropErr
	})
}

// HasColumn reports whether dst's field (or, if it does not resolve to a
// known schema field, the literal column name field) exists on dst's
// table, via information_schema.columns.
func (m Migrator) HasColumn(dst interface{}, field string) bool {
	var count int64
	m.RunWithValue(dst, func(stmt *gorm.Statement) error {
		name := field
		if stmt.Schema != nil {
			if f := stmt.Schema.LookUpField(field); f != nil {
				name = f.DBName
			}
		}
		return m.DB.Raw(
			"SELECT count(*) FROM information_schema.columns WHERE table_schema = ? AND table_name = ? AND column_name = ?",
			m.CurrentDatabase(), stmt.Table, name,
		).Row().Scan(&count)
	})
	return count > 0
}

// AlterColumn issues ALTER TABLE ... MODIFY COLUMN — qrdb's (MySQL's)
// syntax for changing a column's type in place — rather than the base
// migrator.Migrator's standard-SQL "ALTER COLUMN ... TYPE", which qrdb's
// engine does not accept.
func (m Migrator) AlterColumn(dst interface{}, fieldName string) error {
	return m.RunWithValue(dst, func(stmt *gorm.Statement) error {
		if stmt.Schema == nil {
			return &FieldNotFoundError{Field: fieldName}
		}
		field := stmt.Schema.LookUpField(fieldName)
		if field == nil {
			return &FieldNotFoundError{Field: fieldName}
		}
		return m.DB.Exec(
			"ALTER TABLE ? MODIFY COLUMN ? ?",
			m.CurrentTable(stmt), clause.Column{Name: field.DBName}, m.FullDataTypeOf(field),
		).Error
	})
}

// RenameColumn renames dst's oldName column to newName. When
// Dialector.DontSupportRenameColumn is set (the common case, since it
// requires re-declaring the column's full type — MySQL's pre-8.0 CHANGE
// syntax, which qrdb's engine also accepts) it issues ALTER TABLE ... CHANGE
// ... ...; otherwise it delegates to the base migrator.Migrator's
// standard-SQL ALTER TABLE ... RENAME COLUMN ... TO ..., which qrdb's engine
// (see docs/sql/README.md's "Relational Tables" section) also accepts.
func (m Migrator) RenameColumn(dst interface{}, oldName, newName string) error {
	if !m.Dialector.DontSupportRenameColumn {
		return m.Migrator.RenameColumn(dst, oldName, newName)
	}

	return m.RunWithValue(dst, func(stmt *gorm.Statement) error {
		var field *schema.Field
		if stmt.Schema != nil {
			if f := stmt.Schema.LookUpField(oldName); f != nil {
				oldName = f.DBName
				field = f
			}
			if f := stmt.Schema.LookUpField(newName); f != nil {
				newName = f.DBName
				field = f
			}
		}
		if field == nil {
			return &FieldNotFoundError{Field: newName}
		}
		return m.DB.Exec(
			"ALTER TABLE ? CHANGE ? ? ?",
			m.CurrentTable(stmt), clause.Column{Name: oldName}, clause.Column{Name: newName}, m.FullDataTypeOf(field),
		).Error
	})
}

// ColumnTypes returns dst's columns from information_schema.columns,
// enriched with the live *sql.ColumnType go-mysql-server's driver reports
// for a real SELECT ... LIMIT 1 against the table (for ScanType and as a
// fallback for any field information_schema itself leaves NULL).
//
// It always returns a non-nil slice, even on error — a caller (this
// package's own AutoMigrate, or a caller like the upstream GORM conformance
// suite's TestDifferentTypeWithoutDeclaredLength/findColumnType) that logs
// the error and then unconditionally ranges over or indexes into the result
// must not be handed a nil slice to panic on. See
// TestMigrator_ColumnTypes_UnknownTableReturnsNonNilSlice for the regression
// this guards.
//
// The live probe runs first and its error is returned, matching
// gorm.io/driver/mysql: "does this table exist and can I read it?" is the
// question callers use ColumnTypes to answer. information_schema.columns
// alone cannot answer it — it reports zero rows for a nonexistent table
// exactly as it does for one that exists with no readable columns — so
// swallowing the probe error would make ColumnTypes report success for a
// table that was never created. The upstream conformance suite's
// TestMigrateIgnoreRelations asserts precisely that distinction.
func (m Migrator) ColumnTypes(dst interface{}) ([]gorm.ColumnType, error) {
	columnTypes := make([]gorm.ColumnType, 0)

	err := m.RunWithValue(dst, func(stmt *gorm.Statement) error {
		currentDatabase := m.CurrentDatabase()

		probe, err := m.DB.Session(&gorm.Session{}).Table(stmt.Table).Limit(1).Rows()
		if err != nil {
			return err
		}
		rawColumnTypes := map[string]*sql.ColumnType{}
		if cts, err := probe.ColumnTypes(); err == nil {
			for _, ct := range cts {
				rawColumnTypes[ct.Name()] = ct
			}
		}
		if err := probe.Close(); err != nil {
			return err
		}

		rows, err := m.DB.Raw(columnTypesSQL, currentDatabase, stmt.Table).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var (
				column            migrator.ColumnType
				columnKey         sql.NullString
				extra             sql.NullString
				datetimePrecision sql.NullInt64
			)
			if err := rows.Scan(
				&column.NameValue, &column.DefaultValueValue, &column.NullableValue,
				&column.DataTypeValue, &column.LengthValue, &column.ColumnTypeValue,
				&columnKey, &extra, &column.CommentValue,
				&column.DecimalSizeValue, &column.ScaleValue, &datetimePrecision,
			); err != nil {
				return err
			}

			column.PrimaryKeyValue = sql.NullBool{Bool: columnKey.String == "PRI", Valid: true}
			column.UniqueValue = sql.NullBool{Bool: columnKey.String == "UNI", Valid: true}
			column.AutoIncrementValue = sql.NullBool{Bool: strings.Contains(extra.String, "auto_increment"), Valid: true}

			if datetimePrecision.Valid {
				column.DecimalSizeValue = datetimePrecision
			}

			if m.Dialector.DontSupportNullAsDefaultValue &&
				column.DefaultValueValue.Valid && strings.EqualFold(column.DefaultValueValue.String, "NULL") {
				column.DefaultValueValue = sql.NullString{}
			}

			if ct, ok := rawColumnTypes[column.NameValue.String]; ok {
				column.SQLColumnType = ct
			}

			columnTypes = append(columnTypes, column)
		}
		return rows.Err()
	})

	return columnTypes, err
}

// HasIndex reports whether dst carries an index named name (resolved
// through dst's schema first, so a Go-level index name maps to its declared
// DBName), via information_schema.statistics.
func (m Migrator) HasIndex(dst interface{}, name string) bool {
	var count int64
	m.RunWithValue(dst, func(stmt *gorm.Statement) error {
		if stmt.Schema != nil {
			if idx := stmt.Schema.LookIndex(name); idx != nil {
				name = idx.Name
			}
		}
		return m.DB.Raw(
			"SELECT count(*) FROM information_schema.statistics WHERE table_schema = ? AND table_name = ? AND index_name = ?",
			m.CurrentDatabase(), stmt.Table, name,
		).Row().Scan(&count)
	})
	return count > 0
}

// GetIndexes returns dst's indexes, grouped from information_schema.
// statistics's one-row-per-indexed-column rows into one gorm.Index per
// distinct index name, columns in their declared (SEQ_IN_INDEX) order.
//
// Each element is a *migrator.Index, not a migrator.Index value, matching
// gorm.io/driver/mysql. That distinction is observable: a caller comparing
// this result against its own expected []gorm.Index — as the upstream GORM
// conformance suite's TestMigrateWithUniqueIndexAndUnique does through
// testify's assert.ElementsMatch — compares interface values, and an
// interface holding a *migrator.Index never equals one holding a
// migrator.Index no matter how identical the fields are.
func (m Migrator) GetIndexes(dst interface{}) ([]gorm.Index, error) {
	indexes := make([]gorm.Index, 0)

	err := m.RunWithValue(dst, func(stmt *gorm.Statement) error {
		rows, err := m.DB.Raw(indexColumnsSQL, m.CurrentDatabase(), stmt.Table).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()

		var (
			order  []string
			byName = map[string]*migrator.Index{}
		)
		for rows.Next() {
			var tableName, columnName, indexName string
			var nonUnique int64
			if err := rows.Scan(&tableName, &columnName, &indexName, &nonUnique); err != nil {
				return err
			}
			idx, ok := byName[indexName]
			if !ok {
				idx = &migrator.Index{
					TableName:       tableName,
					NameValue:       indexName,
					PrimaryKeyValue: sql.NullBool{Bool: indexName == "PRIMARY", Valid: true},
					UniqueValue:     sql.NullBool{Bool: nonUnique == 0, Valid: true},
				}
				byName[indexName] = idx
				order = append(order, indexName)
			}
			idx.ColumnList = append(idx.ColumnList, columnName)
		}
		if err := rows.Err(); err != nil {
			return err
		}

		for _, name := range order {
			indexes = append(indexes, byName[name])
		}
		return nil
	})

	return indexes, err
}

// DropIndex issues DROP INDEX ... ON ... — qrdb's (MySQL's) syntax — rather
// than the base migrator.Migrator's standard-SQL "DROP INDEX ... ON ?",
// which differs only in how the table is resolved for a dynamic (db.Table(
// ...)) statement; this mirrors go-gorm/mysql's own DropIndex exactly for
// that reason.
func (m Migrator) DropIndex(dst interface{}, name string) error {
	return m.RunWithValue(dst, func(stmt *gorm.Statement) error {
		if stmt.Schema != nil {
			if idx := stmt.Schema.LookIndex(name); idx != nil {
				name = idx.Name
			}
		}
		return m.DB.Exec("DROP INDEX ? ON ?", clause.Column{Name: name}, m.CurrentTable(stmt)).Error
	})
}

// RenameIndex delegates to the base migrator.Migrator's ALTER TABLE ...
// RENAME INDEX ... TO ..., which qrdb's engine accepts directly (see
// docs/sql/README.md's "Relational Tables" section) — the default, since
// Dialector.DontSupportRenameIndex itself defaults to false (see Config).
// When the caller sets that field true, this always returns
// *RenameIndexNotSupportedError instead.
func (m Migrator) RenameIndex(dst interface{}, oldName, newName string) error {
	if m.Dialector.DontSupportRenameIndex {
		return &RenameIndexNotSupportedError{OldName: oldName, NewName: newName}
	}
	return m.Migrator.RenameIndex(dst, oldName, newName)
}

// HasConstraint reports whether dst carries a constraint named name, via
// information_schema.table_constraints — populated by go-mysql-server from
// whichever of CHECK, UNIQUE (including PRIMARY KEY), and FOREIGN KEY the
// underlying table implementation actually reports; qrdb's relational
// tables report UNIQUE (through their index support) and FOREIGN KEY
// (through sql.ForeignKeyTable), but not CHECK — they do not implement
// sql.CheckAlterableTable — so a CHECK constraint is the one kind this can
// never observe. See docs/sql/README.md.
func (m Migrator) HasConstraint(dst interface{}, name string) bool {
	var count int64
	m.RunWithValue(dst, func(stmt *gorm.Statement) error {
		constraintName, table := name, stmt.Table
		if constraint, guessedTable := m.GuessConstraintInterfaceAndTable(stmt, name); constraint != nil {
			constraintName, table = constraint.GetName(), guessedTable
		}
		return m.DB.Raw(
			"SELECT count(*) FROM information_schema.table_constraints WHERE constraint_schema = ? AND table_name = ? AND constraint_name = ?",
			m.CurrentDatabase(), table, constraintName,
		).Row().Scan(&count)
	})
	return count > 0
}

// CreateConstraint creates the CHECK or UNIQUE constraint name resolves to
// on dst, delegating to the base migrator.Migrator's ALTER TABLE ... ADD
// CONSTRAINT ... — the same statement go-gorm/mysql issues. go-mysql-server
// resolves a UNIQUE constraint through the same sql.IndexAlterableTable
// qrdb's relational tables already implement for CREATE/DROP INDEX, and
// rejects a CHECK constraint until they also implement
// sql.CheckAlterableTable (see docs/sql/README.md's "Relational Tables"
// section) — either way this is a real SQL statement whose acceptance is
// the engine's call, not something this Migrator preempts.
//
// A FOREIGN KEY is likewise an ordinary statement: qrdb's relational tables
// implement sql.ForeignKeyTable, so the engine resolves and enforces it.
//
// CreateConstraint itself is not overridden — the embedded
// migrator.Migrator's statement is already correct for this engine.

// DropConstraint drops the constraint name resolves to on dst.
//
// A UNIQUE constraint is dropped as an index rather than through the base
// migrator.Migrator's ALTER TABLE ... DROP CONSTRAINT. go-mysql-server
// cannot resolve a bare DROP CONSTRAINT against qrdb's relational tables:
// it dispatches the statement to the check-constraint path, which those
// tables do not implement, so the engine answers "the table does not
// support check constraint operations" even for a constraint it created
// itself moments earlier through ADD CONSTRAINT ... UNIQUE. Since
// go-mysql-server backs a UNIQUE constraint with an ordinary unique index
// (information_schema.statistics reports it, and ALTER TABLE ... DROP INDEX
// removes it), dropping the index is both what actually works and what the
// constraint physically is. This mirrors the resolution gorm.io/driver/
// mysql performs under its DontSupportDropConstraint flag; qrdb needs no
// flag because DROP CONSTRAINT never works here.
func (m Migrator) DropConstraint(dst interface{}, name string) error {
	return m.dropConstraint(dst, name)
}

// dropConstraint drops a constraint already known not to be a
// relationship-derived foreign key: a CHECK constraint through MySQL's
// ALTER TABLE ... DROP CHECK (whose acceptance is the engine's call, exactly
// as for CreateConstraint), and anything else — a UNIQUE constraint, or a
// bare name gorm's schema introspection cannot resolve — through the unique
// index that backs it. A name matching no index at all is a no-op rather
// than an error, since the constraint it names is already absent.
func (m Migrator) dropConstraint(dst interface{}, name string) error {
	return m.RunWithValue(dst, func(stmt *gorm.Statement) error {
		constraint, table := m.GuessConstraintInterfaceAndTable(stmt, name)
		if constraint != nil {
			name = constraint.GetName()
			if _, isCheck := constraint.(*schema.CheckConstraint); isCheck {
				return m.DB.Exec(
					"ALTER TABLE ? DROP CHECK ?",
					clause.Table{Name: table}, clause.Column{Name: name},
				).Error
			}
		}
		if !m.HasIndex(dst, name) {
			return nil
		}
		return m.DB.Exec(
			"ALTER TABLE ? DROP INDEX ?",
			clause.Table{Name: table}, clause.Column{Name: name},
		).Error
	})
}

// MigrateColumnUnique reconciles field's UNIQUE declaration against what the
// database reports for its column, replacing the base migrator.Migrator's
// version, which is written for dialects where gorm.ColumnType.Unique() is
// unaffected by unique indexes.
//
// qrdb's is not: information_schema.columns.COLUMN_KEY reads "UNI" for a
// column covered by a single-column unique index just as it does for one
// covered by a UNIQUE constraint (measured, not assumed — a gorm:"uniqueIndex"
// field reports COLUMN_KEY "UNI"). Under the base implementation that makes
// every unique-indexed, non-unique-tagged column look like a stray UNIQUE
// constraint to be dropped, so AutoMigrate issues a DROP CONSTRAINT for a
// constraint that never existed on every single run — the bug the upstream
// conformance suite's TestMigrateMySQLWithCustomizedTypes exists to catch.
//
// The reconciliation below is gorm.io/driver/mysql's, for the same reason it
// is: "UNI" is ambiguous, so the constraint's existence has to be settled by
// asking information_schema.table_constraints (HasConstraint) rather than
// inferred from the column key.
func (m Migrator) MigrateColumnUnique(dst interface{}, field *schema.Field, columnType gorm.ColumnType) error {
	unique, ok := columnType.Unique()
	if !ok || field.PrimaryKey {
		return nil // skip primary key
	}

	queryTx, execTx := m.GetQueryAndExecTx()
	return m.RunWithValue(dst, func(stmt *gorm.Statement) error {
		// The `unique` tag is boolean-only, so the constraint name is fixed.
		constraint := m.DB.NamingStrategy.UniqueName(stmt.Table, field.DBName)

		if !unique {
			if field.Unique {
				if err := execTx.Migrator().CreateConstraint(dst, constraint); err != nil {
					return err
				}
			}
			return m.createUniqueIndexIfMissing(queryTx, execTx, dst, field)
		}

		if err := m.dropRedundantUniqueIndexes(queryTx, execTx, dst, field, constraint); err != nil {
			return err
		}

		hasConstraint := queryTx.Migrator().HasConstraint(dst, constraint)
		switch {
		case field.Unique && !hasConstraint:
			if err := execTx.Migrator().CreateConstraint(dst, constraint); err != nil {
				return err
			}
		case !field.Unique && hasConstraint:
			// The column is only unique because of the constraint, and the
			// field no longer asks for one; any unique index the field does
			// ask for is recreated below.
			if err := execTx.Migrator().DropConstraint(dst, constraint); err != nil {
				return err
			}
		}
		return m.createUniqueIndexIfMissing(queryTx, execTx, dst, field)
	})
}

// dropRedundantUniqueIndexes removes every index redundantUniqueIndexNames
// identifies as no longer wanted on field.
func (m Migrator) dropRedundantUniqueIndexes(queryTx, execTx *gorm.DB, dst interface{}, field *schema.Field, constraint string) error {
	indexes, _ := queryTx.Migrator().GetIndexes(dst)
	for _, name := range redundantUniqueIndexNames(indexes, field, constraint) {
		if err := execTx.Migrator().DropIndex(dst, name); err != nil {
			return err
		}
	}
	return nil
}

// redundantUniqueIndexNames returns the name of every single-column unique
// index on field that is neither the UNIQUE constraint's own backing index
// (constraint) nor the index field's gorm:"uniqueIndex" tag names — left in
// place, each would keep reporting COLUMN_KEY "UNI" for a column whose
// declaration no longer asks to be unique.
//
// A multi-column index is never redundant, even when field leads it: its
// uniqueness is over the whole column tuple, so dropping it would change
// what the table enforces rather than just how the column reports.
func redundantUniqueIndexNames(indexes []gorm.Index, field *schema.Field, constraint string) []string {
	var names []string
	for _, index := range indexes {
		if unique, ok := index.Unique(); !ok || !unique {
			continue
		}
		if columns := index.Columns(); len(columns) != 1 || columns[0] != field.DBName {
			continue
		}
		if name := index.Name(); name != constraint && name != field.UniqueIndex {
			names = append(names, name)
		}
	}
	return names
}

// createUniqueIndexIfMissing creates the index field's gorm:"uniqueIndex"
// tag names when the field declares one and the database does not have it —
// including the case where it was just dropped along with the UNIQUE
// constraint whose backing index shared its column.
func (m Migrator) createUniqueIndexIfMissing(queryTx, execTx *gorm.DB, dst interface{}, field *schema.Field) error {
	if field.UniqueIndex == "" || queryTx.Migrator().HasIndex(dst, field.UniqueIndex) {
		return nil
	}
	return execTx.Migrator().CreateIndex(dst, field.UniqueIndex)
}
