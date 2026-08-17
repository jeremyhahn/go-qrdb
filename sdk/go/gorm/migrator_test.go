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

package gorm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/migrator"
	"gorm.io/gorm/schema"
)

// ---------------------------------------------------------------------------
// Offline: no *gorm.DB connection of any kind.
// ---------------------------------------------------------------------------

func TestGetTypeAliases_KnownTypesReturnDocumentedAliases(t *testing.T) {
	m := Migrator{}

	cases := map[string][]string{
		"bool":      {"tinyint"},
		"tinyint":   {"bool"},
		"int":       {"integer"},
		"integer":   {"int"},
		"bigint":    {"int8"},
		"int8":      {"bigint"},
		"varchar":   {"char"},
		"char":      {"varchar"},
		"datetime":  {"timestamp"},
		"timestamp": {"datetime"},
		"blob":      {"binary", "varbinary"},
		"binary":    {"blob"},
		"varbinary": {"blob"},
		"decimal":   {"numeric"},
		"numeric":   {"decimal"},
	}

	for dbType, want := range cases {
		t.Run(dbType, func(t *testing.T) {
			got := m.GetTypeAliases(dbType)
			gotSorted, wantSorted := append([]string{}, got...), append([]string{}, want...)
			sort.Strings(gotSorted)
			sort.Strings(wantSorted)
			if fmt.Sprint(gotSorted) != fmt.Sprint(wantSorted) {
				t.Errorf("GetTypeAliases(%q) = %v, want %v", dbType, got, want)
			}
		})
	}
}

func TestGetTypeAliases_UnknownTypeReturnsNil(t *testing.T) {
	m := Migrator{}
	if got := m.GetTypeAliases("geometry"); got != nil {
		t.Errorf("GetTypeAliases(unknown) = %v, want nil", got)
	}
}

func TestRenameIndexNotSupportedError_Error(t *testing.T) {
	err := &RenameIndexNotSupportedError{OldName: "idx_old", NewName: "idx_new"}
	got := err.Error()
	if got == "" {
		t.Fatal("Error() is empty")
	}
	for _, want := range []string{"idx_old", "idx_new", "RenameIndex"} {
		if !strings.Contains(got, want) {
			t.Errorf("Error() = %q, want it to contain %q", got, want)
		}
	}
}

func TestFieldNotFoundError_Error(t *testing.T) {
	err := &FieldNotFoundError{Field: "nonexistent"}
	got := err.Error()
	if got == "" {
		t.Fatal("Error() is empty")
	}
	if !strings.Contains(got, "nonexistent") {
		t.Errorf("Error() = %q, want it to contain the field name", got)
	}
}

// TestMigrator_RenameIndex_NotSupportedWhenConfigured exercises RenameIndex's
// DontSupportRenameIndex guard directly against a hand-built *gorm.DB — no
// embedded engine or connection is needed, since the guard returns
// *RenameIndexNotSupportedError before ever touching m.DB. Config.
// DontSupportRenameIndex itself defaults to false (see its doc comment), so
// this test sets it explicitly rather than relying on New's defaults.
func TestMigrator_RenameIndex_NotSupportedWhenConfigured(t *testing.T) {
	dialector := New(Config{}).(Dialector)
	dialector.DontSupportRenameIndex = true
	db := &gorm.DB{Config: &gorm.Config{Dialector: dialector}}

	m := dialector.Migrator(db)
	err := m.RenameIndex(nil, "idx_old", "idx_new")

	var target *RenameIndexNotSupportedError
	if !errors.As(err, &target) {
		t.Fatalf("RenameIndex() err = %v (%T), want *RenameIndexNotSupportedError", err, err)
	}
	if target.OldName != "idx_old" || target.NewName != "idx_new" {
		t.Errorf("RenameIndexNotSupportedError = %+v, want OldName=idx_old NewName=idx_new", target)
	}
}

func TestDialector_Migrator_WiresEmbeddedBaseMigrator(t *testing.T) {
	dialector := New(Config{}).(Dialector)
	db := &gorm.DB{Config: &gorm.Config{Dialector: dialector}}

	gm := dialector.Migrator(db)
	m, ok := gm.(Migrator)
	if !ok {
		t.Fatalf("Migrator() returned %T, want Migrator", gm)
	}
	if m.Migrator.DB != db {
		t.Error("embedded migrator.Migrator.DB is not the *gorm.DB passed to Dialector.Migrator")
	}
	if m.Dialector.DriverName != dialector.DriverName {
		t.Error("Migrator.Dialector was not wired from the receiver")
	}
}

// ---------------------------------------------------------------------------
// SQL-shape assertions: a real *gorm.DB (embedded target, so no qrdbd
// subprocess), but every statement runs with DryRun — GORM builds and
// captures the final SQL text instead of sending it to the connection —
// so these do not depend on any table actually existing.
// ---------------------------------------------------------------------------

type shapeModel struct {
	ID    uint   `gorm:"primaryKey"`
	Email string `gorm:"size:100"`
}

func openEmbeddedDB(t *testing.T, database string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(New(Config{DSN: "embedded://memory?database=" + database}), &gorm.Config{})
	if err != nil {
		t.Fatalf("gorm.Open(embedded://memory?database=%s): %v", database, err)
	}
	return db
}

// dialectorMigrator returns dialector.Migrator(tx) directly, bypassing
// (*gorm.DB).Migrator()'s own extra Session(&Session{}) wrapping. That
// wrapping matters here: it hands the Migrator a *gorm.DB distinct from tx,
// so an Exec issued through it never lands on the Statement ToSQL reads
// back from the tx its queryFn returned. Calling Dialector.Migrator
// directly, exactly as (*gorm.DB).Migrator() itself ultimately does but
// without the intermediate clone, keeps m.DB == tx so DryRun-captured SQL
// shape assertions actually observe what ran.
func dialectorMigrator(tx *gorm.DB) gorm.Migrator {
	return tx.Config.Dialector.(Dialector).Migrator(tx)
}

func TestMigrator_AlterColumn_UsesModifyColumnSyntax(t *testing.T) {
	db := openEmbeddedDB(t, "migrator_altercolumn")

	got := db.ToSQL(func(tx *gorm.DB) *gorm.DB {
		_ = dialectorMigrator(tx).AlterColumn(&shapeModel{}, "Email")
		return tx
	})

	want := "ALTER TABLE `shape_models` MODIFY COLUMN `email` varchar(100)"
	if got != want {
		t.Errorf("AlterColumn SQL = %q, want %q", got, want)
	}
}

func TestMigrator_AlterColumn_UnknownFieldReturnsFieldNotFoundError(t *testing.T) {
	db := openEmbeddedDB(t, "migrator_altercolumn_unknown")

	err := db.Migrator().AlterColumn(&shapeModel{}, "DoesNotExist")

	var target *FieldNotFoundError
	if !errors.As(err, &target) {
		t.Fatalf("AlterColumn() err = %v (%T), want *FieldNotFoundError", err, err)
	}
}

// TestMigrator_AlterColumn_BareTableNameReturnsFieldNotFoundError exercises
// AlterColumn's other route to *FieldNotFoundError: dst as a bare table-name
// string (as opposed to a struct GORM can parse a schema.Schema from) never
// carries a stmt.Schema at all, so the field can never be resolved.
func TestMigrator_AlterColumn_BareTableNameReturnsFieldNotFoundError(t *testing.T) {
	db := openEmbeddedDB(t, "migrator_altercolumn_bare")

	err := db.Migrator().AlterColumn("kv", "value")

	var target *FieldNotFoundError
	if !errors.As(err, &target) {
		t.Fatalf("AlterColumn() err = %v (%T), want *FieldNotFoundError", err, err)
	}
}

// TestMigrator_FullDataTypeOf_AppendsCommentTag exercises the one behavior
// Migrator.FullDataTypeOf adds on top of the base migrator.Migrator's own:
// a gorm:"comment:..." tag becomes an inline SQL COMMENT clause.
func TestMigrator_FullDataTypeOf_AppendsCommentTag(t *testing.T) {
	db := openEmbeddedDB(t, "migrator_fulldatatype_comment")

	type commentModel struct {
		ID   uint
		Name string `gorm:"comment:the name"`
	}

	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(&commentModel{}); err != nil {
		t.Fatalf("stmt.Parse: %v", err)
	}
	field := stmt.Schema.LookUpField("Name")
	if field == nil {
		t.Fatal("LookUpField(Name) = nil")
	}

	expr := db.Migrator().FullDataTypeOf(field)
	if !strings.Contains(expr.SQL, "COMMENT") {
		t.Errorf("FullDataTypeOf(Name).SQL = %q, want it to contain COMMENT", expr.SQL)
	}
}

func TestMigrator_DropIndex_UsesDropIndexOnSyntax(t *testing.T) {
	db := openEmbeddedDB(t, "migrator_dropindex")

	got := db.ToSQL(func(tx *gorm.DB) *gorm.DB {
		_ = dialectorMigrator(tx).DropIndex(&shapeModel{}, "idx_shape_models_email")
		return tx
	})

	want := "DROP INDEX `idx_shape_models_email` ON `shape_models`"
	if got != want {
		t.Errorf("DropIndex SQL = %q, want %q", got, want)
	}
}

// TestMigrator_DropIndex_ResolvesIndexNameFromSchema exercises DropIndex's
// stmt.Schema.LookIndex branch using indexedModel's real gorm:"index:..."
// tag, as opposed to TestMigrator_DropIndex_UsesDropIndexOnSyntax above,
// whose index name never matches any of shapeModel's declared indexes.
func TestMigrator_DropIndex_ResolvesIndexNameFromSchema(t *testing.T) {
	db := openEmbeddedDB(t, "migrator_dropindex_schema")

	got := db.ToSQL(func(tx *gorm.DB) *gorm.DB {
		_ = dialectorMigrator(tx).DropIndex(&indexedModel{}, "idx_indexed_models_email")
		return tx
	})

	want := "DROP INDEX `idx_indexed_models_email` ON `indexed_models`"
	if got != want {
		t.Errorf("DropIndex SQL = %q, want %q", got, want)
	}
}

// TestMigrator_RenameIndex_DelegatesToBaseWhenNotConfigured exercises
// RenameIndex's other branch: Dialector.DontSupportRenameIndex's own
// zero-value default (false) delegates straight to the base migrator.
// Migrator's ALTER TABLE ... RENAME INDEX ... TO ....
func TestMigrator_RenameIndex_DelegatesToBaseWhenNotConfigured(t *testing.T) {
	db := openEmbeddedDB(t, "migrator_renameindex_base")

	got := db.ToSQL(func(tx *gorm.DB) *gorm.DB {
		_ = dialectorMigrator(tx).RenameIndex(&shapeModel{}, "idx_old", "idx_new")
		return tx
	})

	if !strings.Contains(got, "RENAME INDEX") {
		t.Errorf("RenameIndex SQL = %q, want it to contain RENAME INDEX", got)
	}
}

func TestMigrator_RenameColumn_UsesChangeSyntaxWhenConfigured(t *testing.T) {
	db := openEmbeddedDB(t, "migrator_renamecolumn")
	dialector := db.Config.Dialector.(Dialector)
	dialector.DontSupportRenameColumn = true
	db.Config.Dialector = dialector

	got := db.ToSQL(func(tx *gorm.DB) *gorm.DB {
		_ = dialectorMigrator(tx).RenameColumn(&shapeModel{}, "Email", "email_address")
		return tx
	})

	if !strings.Contains(got, "CHANGE") {
		t.Errorf("RenameColumn SQL = %q, want it to contain CHANGE", got)
	}
}

// TestMigrator_RenameColumn_DelegatesToBaseWhenNotConfigured exercises
// RenameColumn's other branch: Dialector.DontSupportRenameColumn's own
// zero-value default (false) delegates straight to the base migrator.
// Migrator's standard-SQL ALTER TABLE ... RENAME COLUMN ... TO ....
func TestMigrator_RenameColumn_DelegatesToBaseWhenNotConfigured(t *testing.T) {
	db := openEmbeddedDB(t, "migrator_renamecolumn_base")

	got := db.ToSQL(func(tx *gorm.DB) *gorm.DB {
		_ = dialectorMigrator(tx).RenameColumn(&shapeModel{}, "Email", "email_address")
		return tx
	})

	if !strings.Contains(got, "RENAME COLUMN") {
		t.Errorf("RenameColumn SQL = %q, want it to contain RENAME COLUMN", got)
	}
}

// ---------------------------------------------------------------------------
// Embedded-backed: information_schema introspection against the built-in
// `kv` table, which every qrdb database serves unconditionally.
//
// This deliberately never CREATE TABLEs a user-defined relational table:
// sdk/go/driver's EmbeddedConnector constructs its provider.Provider via
// provider.NewProviderWithEngine with no service.Registry wired in (see
// connector_embedded.go's getOrCreateEngine), and the relational table
// catalog is unreachable without one — every CREATE TABLE over an
// embedded:// DSN today fails with provider.RelationalCatalogUnavailable
// Error before Migrator ever gets a real user table to introspect. The
// built-in `kv` table needs no CREATE TABLE at all, so it is the only
// target these information_schema-backed methods can be verified against
// until that wiring gap is closed — a qrdb-side gap outside sdk/go/gorm,
// reported alongside this task rather than fixed by it.
// ---------------------------------------------------------------------------

func TestMigrator_CurrentDatabase(t *testing.T) {
	db := openEmbeddedDB(t, "migrator_currentdb")

	if got, want := db.Migrator().CurrentDatabase(), "migrator_currentdb"; got != want {
		t.Errorf("CurrentDatabase() = %q, want %q", got, want)
	}
}

func TestMigrator_HasTable(t *testing.T) {
	db := openEmbeddedDB(t, "migrator_hastable")

	if !db.Migrator().HasTable("kv") {
		t.Error("HasTable(kv) = false, want true")
	}
	if db.Migrator().HasTable("does_not_exist") {
		t.Error("HasTable(does_not_exist) = true, want false")
	}
}

func TestMigrator_GetTables(t *testing.T) {
	db := openEmbeddedDB(t, "migrator_gettables")

	tables, err := db.Migrator().GetTables()
	if err != nil {
		t.Fatalf("GetTables() error = %v", err)
	}
	found := false
	for _, name := range tables {
		if name == "kv" {
			found = true
		}
	}
	if !found {
		t.Errorf("GetTables() = %v, want it to contain %q", tables, "kv")
	}
}

func TestMigrator_TableType(t *testing.T) {
	db := openEmbeddedDB(t, "migrator_tabletype")

	tt, err := db.Migrator().TableType("kv")
	if err != nil {
		t.Fatalf("TableType() error = %v", err)
	}
	if got, want := tt.Name(), "kv"; got != want {
		t.Errorf("TableType().Name() = %q, want %q", got, want)
	}
	if got, want := tt.Schema(), "migrator_tabletype"; got != want {
		t.Errorf("TableType().Schema() = %q, want %q", got, want)
	}
}

func TestMigrator_HasColumn(t *testing.T) {
	db := openEmbeddedDB(t, "migrator_hascolumn")

	if !db.Migrator().HasColumn("kv", "key") {
		t.Error("HasColumn(kv, key) = false, want true")
	}
	if db.Migrator().HasColumn("kv", "does_not_exist") {
		t.Error("HasColumn(kv, does_not_exist) = true, want false")
	}

	// A struct dst (as opposed to the bare table-name string above) gives
	// RunWithValue a real stmt.Schema, exercising the LookUpField branch
	// that resolves a Go field name to its DBName before querying — no
	// table needs to actually exist for this: the count(*) query against
	// information_schema.columns simply returns 0 either way.
	if db.Migrator().HasColumn(&shapeModel{}, "Email") {
		t.Error("HasColumn(&shapeModel{}, Email) = true, want false (table was never created)")
	}
}

func TestMigrator_ColumnTypes(t *testing.T) {
	db := openEmbeddedDB(t, "migrator_columntypes")

	cts, err := db.Migrator().ColumnTypes("kv")
	if err != nil {
		t.Fatalf("ColumnTypes() error = %v", err)
	}
	if cts == nil {
		t.Fatal("ColumnTypes() returned a nil slice; must always be non-nil, even empty (see the package's known-bug note)")
	}

	names := make(map[string]gorm.ColumnType, len(cts))
	for _, ct := range cts {
		names[ct.Name()] = ct
	}
	if _, ok := names["key"]; !ok {
		t.Fatalf("ColumnTypes() = %v, want a %q column", cts, "key")
	}
	if _, ok := names["value"]; !ok {
		t.Fatalf("ColumnTypes() = %v, want a %q column", cts, "value")
	}
	if got, want := strings.ToLower(names["key"].DatabaseTypeName()), "varchar"; got != want {
		t.Errorf("key column DatabaseTypeName() = %q, want %q", got, want)
	}
}

// TestMigrator_ColumnTypes_UnknownTableErrorsWithNonNilSlice pins both
// halves of ColumnTypes' contract for a table that does not exist.
//
// The error is the load-bearing half: "does this table exist?" is what
// callers use ColumnTypes to answer (the upstream GORM conformance suite's
// TestMigrateIgnoreRelations asserts exactly that, through findColumnType),
// and information_schema.columns alone cannot answer it — it returns zero
// rows for a nonexistent table just as it would for a readable one with no
// columns. The live SELECT ... LIMIT 1 probe is what distinguishes them, so
// its error is returned rather than swallowed, matching
// gorm.io/driver/mysql.
//
// The non-nil slice is the second half, and it must hold even on that error
// path: a caller (findColumnType among others) logs the error and then
// unconditionally ranges over the result, which a nil slice would not
// survive.
func TestMigrator_ColumnTypes_UnknownTableErrorsWithNonNilSlice(t *testing.T) {
	db := openEmbeddedDB(t, "migrator_columntypes_missing")

	cts, err := db.Migrator().ColumnTypes("does_not_exist")
	if err == nil {
		t.Error("ColumnTypes(does_not_exist) error = nil, want a nonexistent-table error")
	}
	if cts == nil {
		t.Fatal("ColumnTypes() returned a nil slice; must always be non-nil, even empty — a nil slice here is exactly the regression documented in this package's known-bug note (a caller that unconditionally dereferences a result panics)")
	}
	if len(cts) != 0 {
		t.Errorf("ColumnTypes(does_not_exist) = %v, want empty", cts)
	}
}

func TestMigrator_HasIndex_And_GetIndexes(t *testing.T) {
	db := openEmbeddedDB(t, "migrator_indexes")

	if db.Migrator().HasIndex("kv", "does_not_exist") {
		t.Error("HasIndex(kv, does_not_exist) = true, want false")
	}

	idxs, err := db.Migrator().GetIndexes("kv")
	if err != nil {
		t.Fatalf("GetIndexes() error = %v", err)
	}
	if idxs == nil {
		t.Error("GetIndexes() returned a nil slice, want non-nil (even if empty)")
	}
}

// indexedModel carries a real gorm:"index:..." tag so
// TestMigrator_HasIndex_ResolvesIndexNameFromSchema can exercise the
// stmt.Schema.LookIndex branch, which resolves a Go-level index name to its
// declared name before querying information_schema.statistics — as opposed
// to TestMigrator_HasIndex_And_GetIndexes above, which only ever queries a
// bare table-name string with no schema.Schema (and so no index) to resolve
// against.
type indexedModel struct {
	ID    uint
	Email string `gorm:"index:idx_indexed_models_email"`
}

func TestMigrator_HasIndex_ResolvesIndexNameFromSchema(t *testing.T) {
	db := openEmbeddedDB(t, "migrator_hasindex_schema")

	// The table was never created, so this must still resolve to false, not
	// error — the point of this test is the name-resolution branch inside
	// HasIndex, not a true result.
	if db.Migrator().HasIndex(&indexedModel{}, "idx_indexed_models_email") {
		t.Error("HasIndex(&indexedModel{}, idx_indexed_models_email) = true, want false (table was never created)")
	}
}

func TestMigrator_HasConstraint(t *testing.T) {
	db := openEmbeddedDB(t, "migrator_hasconstraint")

	if db.Migrator().HasConstraint("kv", "does_not_exist") {
		t.Error("HasConstraint(kv, does_not_exist) = true, want false")
	}
}

// ---------------------------------------------------------------------------
// UNIQUE reconciliation: MigrateColumnUnique and the DropConstraint
// resolution it depends on.
//
// COLUMN_KEY is ambiguous under qrdb (it reads "UNI" for a column covered by
// a single-column unique index just as it does for one covered by a UNIQUE
// constraint), which is why this package overrides both. The tests below
// drive each branch with a hand-built gorm.ColumnType — the Unique() answer
// is the entire input that matters — and read back the statement that
// branch produced under DryRun.
// ---------------------------------------------------------------------------

// uniqueTagModel's name column carries both a `unique` tag (field.Unique) and
// a named unique index (field.UniqueIndex), so a single parsed schema can
// supply a field for either half of the reconciliation.
type uniqueTagModel struct {
	ID   uint   `gorm:"primaryKey"`
	Name string `gorm:"size:60;unique;index:my_unique_index,unique"`
}

// uniqueIndexModel's name column declares a named unique index and no
// unique constraint — the combination that reaches the unique-index half of
// the reconciliation without going through a UNIQUE constraint first.
type uniqueIndexModel struct {
	ID   uint   `gorm:"primaryKey"`
	Name string `gorm:"size:60;index:my_unique_index,unique"`
}

// plainModel's name column declares neither a unique constraint nor a unique
// index, so a database reporting it as unique can only be reporting a
// leftover the field no longer asks for.
type plainModel struct {
	ID   uint   `gorm:"primaryKey"`
	Name string `gorm:"size:60"`
}

// columnUnique returns the gorm.ColumnType MigrateColumnUnique reads,
// carrying only the Unique() answer under test. valid false models a driver
// that cannot report uniqueness at all, which must short-circuit.
func columnUnique(unique, valid bool) gorm.ColumnType {
	return migrator.ColumnType{UniqueValue: sql.NullBool{Bool: unique, Valid: valid}}
}

// fieldOf parses model and returns its named field, failing the test if the
// schema cannot be parsed. ParseIndexes is required for field.UniqueIndex to
// be populated, exactly as gorm's own AutoMigrate does before calling
// MigrateColumnUnique.
func fieldOf(t *testing.T, db *gorm.DB, model interface{}, name string) *schema.Field {
	t.Helper()
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(model); err != nil {
		t.Fatalf("Parse(%T): %v", model, err)
	}
	_ = stmt.Schema.ParseIndexes()
	field := stmt.Schema.LookUpField(name)
	if field == nil {
		t.Fatalf("LookUpField(%q) on %T = nil", name, model)
	}
	return field
}

// ddlRecorder is a gorm logger that records every DDL statement its *gorm.DB
// runs. MigrateColumnUnique issues its statements through the separate
// query/exec sessions GetQueryAndExecTx hands out, so (*gorm.DB).ToSQL —
// which reads back only the Statement of the one *gorm.DB it was given —
// cannot observe them; a logger sees all of them regardless of session.
//
// Only DDL is kept: the introspection queries MigrateColumnUnique runs
// against information_schema on the way to a decision are noise for these
// tests, which are about which schema change it decided on.
type ddlRecorder struct {
	logger.Interface
	statements []string
}

// ddlPrefixes are the statement prefixes ddlRecorder keeps.
var ddlPrefixes = []string{"ALTER TABLE", "CREATE INDEX", "CREATE UNIQUE INDEX", "DROP INDEX"}

// Trace records sql when it is DDL. gorm calls this once per statement, on
// the goroutine that ran it, and every test using ddlRecorder is
// single-goroutine, so the append needs no synchronization.
func (r *ddlRecorder) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	sql, _ := fc()
	for _, prefix := range ddlPrefixes {
		if strings.HasPrefix(sql, prefix) {
			r.statements = append(r.statements, sql)
			return
		}
	}
}

// openRecordingDB opens the same embedded *gorm.DB openEmbeddedDB does, with
// a ddlRecorder installed as its logger.
func openRecordingDB(t *testing.T, database string) (*gorm.DB, *ddlRecorder) {
	t.Helper()
	recorder := &ddlRecorder{Interface: logger.Discard}
	db, err := gorm.Open(
		New(Config{DSN: "embedded://memory?database=" + database}),
		&gorm.Config{Logger: recorder},
	)
	if err != nil {
		t.Fatalf("gorm.Open(embedded://memory?database=%s): %v", database, err)
	}
	return db, recorder
}

// TestMigrator_MigrateColumnUnique drives every branch of the UNIQUE
// reconciliation from the two inputs that decide it — what the database
// reports through gorm.ColumnType.Unique(), and what the field declares —
// and asserts the DDL each combination produces.
//
// No table exists in any of these cases, so information_schema reports no
// constraint and no index: the "database side" of each decision is fixed and
// the field/ColumnType side is what varies.
func TestMigrator_MigrateColumnUnique(t *testing.T) {
	const addConstraint = "ALTER TABLE `unique_tag_models` ADD CONSTRAINT `uni_unique_tag_models_name` UNIQUE (`name`)"
	createTheIndexOn := func(table string) string {
		return "CREATE UNIQUE INDEX `my_unique_index` ON `" + table + "`(`name`)"
	}

	cases := map[string]struct {
		model     interface{}
		fieldName string
		unique    bool
		valid     bool
		want      []string
	}{
		// A driver that cannot report uniqueness gives no basis for any
		// change, so nothing is issued on a guess.
		"unreportable uniqueness is skipped": {
			model: &uniqueTagModel{}, fieldName: "name", unique: false, valid: false,
		},
		// MySQL reports every primary key column as unique; reconciling it
		// against the `unique` tag would fight the primary key itself.
		"primary key is skipped": {
			model: &uniqueTagModel{}, fieldName: "id", unique: true, valid: true,
		},
		// The unambiguous direction: not unique in the database, declared
		// unique by the field. (No table exists, so the ADD CONSTRAINT
		// fails at the engine and reconciliation stops there — which is
		// itself the contract: a failed schema change is reported, not
		// stepped over.)
		"not unique, unique declared: constraint created": {
			model: &uniqueTagModel{}, fieldName: "name", unique: false, valid: true,
			want: []string{addConstraint},
		},
		"not unique, nothing declared: nothing issued": {
			model: &plainModel{}, fieldName: "name", unique: false, valid: true,
		},
		// A unique index the field declares and the database does not have
		// is created, whichever direction the column key reported.
		"not unique, unique index declared: index created": {
			model: &uniqueIndexModel{}, fieldName: "name", unique: false, valid: true,
			want: []string{createTheIndexOn("unique_index_models")},
		},
		"unique, unique index declared: index created": {
			model: &uniqueIndexModel{}, fieldName: "name", unique: true, valid: true,
			want: []string{createTheIndexOn("unique_index_models")},
		},
		// The ambiguous direction. "UNI" alone cannot say whether a UNIQUE
		// constraint exists, so table_constraints is consulted — it reports
		// none, so the declared constraint is created rather than assumed.
		"unique, unique declared, no constraint found: constraint created": {
			model: &uniqueTagModel{}, fieldName: "name", unique: true, valid: true,
			want: []string{addConstraint},
		},
		// The regression that motivated overriding MigrateColumnUnique: the
		// base migrator.Migrator issues a DROP CONSTRAINT here on the
		// strength of "UNI" alone, for a constraint that does not exist —
		// on every AutoMigrate run.
		"unique, nothing declared: no DROP CONSTRAINT on a guess": {
			model: &plainModel{}, fieldName: "name", unique: true, valid: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			db, recorder := openRecordingDB(t, "migrator_mcu")
			field := fieldOf(t, db, tc.model, tc.fieldName)

			_ = db.Migrator().(Migrator).MigrateColumnUnique(tc.model, field, columnUnique(tc.unique, tc.valid))

			if len(recorder.statements) != len(tc.want) {
				t.Fatalf("MigrateColumnUnique ran %q, want %q", recorder.statements, tc.want)
			}
			for i, want := range tc.want {
				if recorder.statements[i] != want {
					t.Fatalf("MigrateColumnUnique ran %q, want %q", recorder.statements, tc.want)
				}
			}
		})
	}
}

func TestRedundantUniqueIndexNames(t *testing.T) {
	db := openEmbeddedDB(t, "migrator_redundant_indexes")
	field := fieldOf(t, db, &uniqueTagModel{}, "name")
	const constraint = "uni_unique_tag_models_name"

	index := func(name string, unique bool, columns ...string) gorm.Index {
		return &migrator.Index{
			NameValue:   name,
			ColumnList:  columns,
			UniqueValue: sql.NullBool{Bool: unique, Valid: true},
		}
	}

	cases := map[string]struct {
		indexes []gorm.Index
		want    []string
	}{
		"leftover single-column unique index is redundant": {
			indexes: []gorm.Index{index("idx_leftover", true, "name")},
			want:    []string{"idx_leftover"},
		},
		"the constraint's own backing index is kept": {
			indexes: []gorm.Index{index(constraint, true, "name")},
		},
		"the field's declared unique index is kept": {
			indexes: []gorm.Index{index("my_unique_index", true, "name")},
		},
		"a non-unique index is kept": {
			indexes: []gorm.Index{index("idx_plain", false, "name")},
		},
		"an index on another column is kept": {
			indexes: []gorm.Index{index("idx_other", true, "email")},
		},
		"a multi-column index is kept even when the field leads it": {
			indexes: []gorm.Index{index("idx_pair", true, "name", "email")},
		},
		"only the redundant ones are selected from a mixed set": {
			indexes: []gorm.Index{
				index(constraint, true, "name"),
				index("my_unique_index", true, "name"),
				index("idx_leftover", true, "name"),
				index("idx_pair", true, "name", "email"),
			},
			want: []string{"idx_leftover"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := redundantUniqueIndexNames(tc.indexes, field, constraint)
			if len(got) != len(tc.want) {
				t.Fatalf("redundantUniqueIndexNames() = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("redundantUniqueIndexNames() = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestRedundantUniqueIndexNames_UnreportedUniquenessIsKept covers the
// Unique() comma-ok branch separately: an index whose driver cannot say
// whether it is unique is never dropped on a guess.
func TestRedundantUniqueIndexNames_UnreportedUniquenessIsKept(t *testing.T) {
	db := openEmbeddedDB(t, "migrator_redundant_unreported")
	field := fieldOf(t, db, &uniqueTagModel{}, "name")

	indexes := []gorm.Index{&migrator.Index{
		NameValue:   "idx_unknown",
		ColumnList:  []string{"name"},
		UniqueValue: sql.NullBool{},
	}}

	if got := redundantUniqueIndexNames(indexes, field, "uni_unique_tag_models_name"); len(got) != 0 {
		t.Errorf("redundantUniqueIndexNames() = %v, want none", got)
	}
}

// checkModel carries a CHECK constraint so DropConstraint's check branch is
// reachable. qrdb's engine rejects the resulting statement (its relational
// tables implement no check constraints), which is exactly why this asserts
// the statement's shape under DryRun rather than its outcome.
type checkModel struct {
	ID   uint `gorm:"primaryKey"`
	Age  int  `gorm:"check:age_positive,age > 0"`
	Name string
}

func TestMigrator_DropConstraint_CheckUsesDropCheckSyntax(t *testing.T) {
	db := openEmbeddedDB(t, "migrator_dropconstraint_check")

	got := db.ToSQL(func(tx *gorm.DB) *gorm.DB {
		_ = dialectorMigrator(tx).DropConstraint(&checkModel{}, "age_positive")
		return tx
	})

	want := "ALTER TABLE `check_models` DROP CHECK `age_positive`"
	if got != want {
		t.Errorf("DropConstraint SQL = %q, want %q", got, want)
	}
}

// TestMigrator_DropConstraint_AbsentIndexIsNoOp covers the other end of the
// resolution: a UNIQUE constraint name is dropped through the index backing
// it, so a name matching no index has nothing to drop and must not be an
// error. The table here was never created, so no index can match.
func TestMigrator_DropConstraint_AbsentIndexIsNoOp(t *testing.T) {
	db := openEmbeddedDB(t, "migrator_dropconstraint_absent")

	if err := db.Migrator().DropConstraint(&uniqueTagModel{}, "uni_unique_tag_models_name"); err != nil {
		t.Errorf("DropConstraint() error = %v, want nil (nothing to drop)", err)
	}
}
