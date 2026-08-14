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

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Foreign key catalog operations.
//
// These operations are the same single-record compare-and-swap every other
// catalog mutation is, with two exceptions the tests below concentrate on:
// constraint names are unique across the DATABASE rather than one table, and
// the reverse lookup (which constraints point AT a table) is not answerable
// from the table's own record. Everything else is round-trip fidelity, which
// matters because the SQL engine plans its enforcement from exactly these
// stored values.
// ---------------------------------------------------------------------------

// fkTestColumns is a two-column layout with a key column a constraint can be
// declared over.
func fkTestColumns() []ColumnDef {
	return []ColumnDef{
		{Name: "id", Type: "BIGINT UNSIGNED"},
		{Name: "company_id", Type: "BIGINT UNSIGNED"},
	}
}

// newFKTestService builds a TableService holding a companies/users pair,
// ready for constraint operations.
func newFKTestService(t *testing.T) (*TableService, *fakeCatalogStore) {
	t.Helper()
	svc, store := newTestTableService()
	ctx := context.Background()
	for _, name := range []string{"companies", "users", "orders"} {
		_, err := svc.CreateTable(ctx, &CreateTableRequest{
			Database: "shop", Name: name, Columns: fkTestColumns(),
			PrimaryKey: []PKColumn{{Name: "id"}},
		})
		require.NoError(t, err)
	}
	return svc, store
}

func testForeignKey(name string) ForeignKeyDef {
	return ForeignKeyDef{
		Name:          name,
		Columns:       []string{"company_id"},
		ParentTable:   "companies",
		ParentColumns: []string{"id"},
		OnDelete:      "CASCADE",
		OnUpdate:      "RESTRICT",
		Resolved:      true,
		NotValid:      true,
		MatchType:     1,
	}
}

// TestTableService_AddForeignKeyRoundTrip asserts every field survives the
// catalog round trip. A field silently dropped here is a constraint the SQL
// engine plans enforcement for under terms the catalog did not keep.
func TestTableService_AddForeignKeyRoundTrip(t *testing.T) {
	svc, _ := newFKTestService(t)
	ctx := context.Background()

	want := testForeignKey("fk_users_company")
	require.NoError(t, svc.AddForeignKey(ctx, &CreateTableForeignKeyRequest{
		Database: "shop", Table: "users", ForeignKey: want,
	}))

	def, err := svc.GetTable(ctx, "shop", "users")
	require.NoError(t, err)
	require.Len(t, def.ForeignKeys, 1)
	assert.Equal(t, want, def.ForeignKeys[0])
}

// TestTableService_AddForeignKeyBumpsEpoch asserts a constraint mutation
// bumps the database catalog epoch, like every other catalog mutation, so a
// future cache keyed off it cannot miss the change.
func TestTableService_AddForeignKeyBumpsEpoch(t *testing.T) {
	svc, store := newFKTestService(t)
	ctx := context.Background()

	before, _ := store.Get(ctx, tableCatalogEpochKey("shop"))
	require.NoError(t, svc.AddForeignKey(ctx, &CreateTableForeignKeyRequest{
		Database: "shop", Table: "users", ForeignKey: testForeignKey("fk_a"),
	}))
	after, err := store.Get(ctx, tableCatalogEpochKey("shop"))
	require.NoError(t, err)
	assert.NotEqual(t, before, after)
}

// TestTableService_ForeignKeyNameIsDatabaseScoped asserts a name already used
// on a DIFFERENT table is rejected. MySQL scopes constraint names to the
// database; a table-scoped check would let two tables both claim one name and
// make DROP CONSTRAINT ambiguous.
func TestTableService_ForeignKeyNameIsDatabaseScoped(t *testing.T) {
	svc, _ := newFKTestService(t)
	ctx := context.Background()

	require.NoError(t, svc.AddForeignKey(ctx, &CreateTableForeignKeyRequest{
		Database: "shop", Table: "users", ForeignKey: testForeignKey("fk_shared"),
	}))

	err := svc.AddForeignKey(ctx, &CreateTableForeignKeyRequest{
		Database: "shop", Table: "orders", ForeignKey: testForeignKey("fk_shared"),
	})
	var dup *TableForeignKeyAlreadyExistsError
	require.True(t, errors.As(err, &dup), "err = %v", err)
	assert.Equal(t, "users", dup.Table, "the error must name the table already holding the name")

	// Case-insensitively, as MySQL matches constraint names.
	err = svc.AddForeignKey(ctx, &CreateTableForeignKeyRequest{
		Database: "shop", Table: "orders", ForeignKey: testForeignKey("FK_SHARED"),
	})
	assert.True(t, errors.As(err, &dup), "err = %v", err)

	// And a different name on the same parent is fine.
	require.NoError(t, svc.AddForeignKey(ctx, &CreateTableForeignKeyRequest{
		Database: "shop", Table: "orders", ForeignKey: testForeignKey("fk_other"),
	}))

	// A name used in a different DATABASE is not a collision.
	_, err = svc.CreateTable(ctx, &CreateTableRequest{
		Database: "other", Name: "users", Columns: fkTestColumns(), PrimaryKey: []PKColumn{{Name: "id"}},
	})
	require.NoError(t, err)
	assert.NoError(t, svc.AddForeignKey(ctx, &CreateTableForeignKeyRequest{
		Database: "other", Table: "users", ForeignKey: testForeignKey("fk_shared"),
	}))
}

// TestTableService_AddForeignKeyValidation asserts a constraint the declaring
// table's own record contradicts is refused. The parent side is deliberately
// NOT validated here (see AddForeignKey's doc comment): only the SQL engine
// knows whether foreign_key_checks is on.
func TestTableService_AddForeignKeyValidation(t *testing.T) {
	svc, _ := newFKTestService(t)
	ctx := context.Background()

	add := func(fk ForeignKeyDef) error {
		return svc.AddForeignKey(ctx, &CreateTableForeignKeyRequest{
			Database: "shop", Table: "users", ForeignKey: fk,
		})
	}

	unnamed := testForeignKey("")
	assert.IsType(t, &TableInvalidNameError{}, add(unnamed))

	noCols := testForeignKey("fk_a")
	noCols.Columns = nil
	assert.IsType(t, &TableForeignKeyNoColumnsError{}, add(noCols))

	mismatched := testForeignKey("fk_a")
	mismatched.ParentColumns = []string{"id", "extra"}
	assert.IsType(t, &TableForeignKeyNoColumnsError{}, add(mismatched))

	unknownCol := testForeignKey("fk_a")
	unknownCol.Columns = []string{"nope"}
	assert.IsType(t, &TableForeignKeyColumnNotFoundError{}, add(unknownCol))

	noParent := testForeignKey("fk_a")
	noParent.ParentTable = ""
	assert.IsType(t, &TableInvalidNameError{}, add(noParent))

	// A parent table that does not exist is accepted: MySQL allows exactly
	// that while foreign_key_checks is off.
	missingParent := testForeignKey("fk_a")
	missingParent.ParentTable = "not_created_yet"
	assert.NoError(t, add(missingParent))

	// Nothing invalid was stored along the way.
	def, err := svc.GetTable(ctx, "shop", "users")
	require.NoError(t, err)
	require.Len(t, def.ForeignKeys, 1)
	assert.Equal(t, "not_created_yet", def.ForeignKeys[0].ParentTable)
}

// TestTableService_AddForeignKeyUnknownTable asserts adding to a table that
// does not exist reports it rather than creating a record.
func TestTableService_AddForeignKeyUnknownTable(t *testing.T) {
	svc, _ := newFKTestService(t)

	err := svc.AddForeignKey(context.Background(), &CreateTableForeignKeyRequest{
		Database: "shop", Table: "ghosts", ForeignKey: testForeignKey("fk_a"),
	})
	assert.IsType(t, &TableNotFoundError{}, err)

	assert.IsType(t, &TableEmptyDatabaseError{}, svc.AddForeignKey(context.Background(),
		&CreateTableForeignKeyRequest{Database: "", Table: "users", ForeignKey: testForeignKey("fk_a")}))
}

// TestTableService_DropForeignKey covers removal, including the not-found
// case, which must be an error rather than a silent success -- a caller that
// believes it dropped a constraint it did not is exactly the drift this
// catalog exists to prevent.
func TestTableService_DropForeignKey(t *testing.T) {
	svc, _ := newFKTestService(t)
	ctx := context.Background()

	require.NoError(t, svc.AddForeignKey(ctx, &CreateTableForeignKeyRequest{
		Database: "shop", Table: "users", ForeignKey: testForeignKey("fk_a"),
	}))
	require.NoError(t, svc.AddForeignKey(ctx, &CreateTableForeignKeyRequest{
		Database: "shop", Table: "users", ForeignKey: testForeignKey("fk_b"),
	}))

	require.NoError(t, svc.DropForeignKey(ctx, "shop", "users", "FK_A"))

	def, err := svc.GetTable(ctx, "shop", "users")
	require.NoError(t, err)
	require.Len(t, def.ForeignKeys, 1)
	assert.Equal(t, "fk_b", def.ForeignKeys[0].Name)

	assert.IsType(t, &TableForeignKeyNotFoundError{}, svc.DropForeignKey(ctx, "shop", "users", "fk_a"))
	assert.IsType(t, &TableInvalidNameError{}, svc.DropForeignKey(ctx, "shop", "users", ""))
	assert.IsType(t, &TableNotFoundError{}, svc.DropForeignKey(ctx, "shop", "ghosts", "fk_b"))

	// The dropped name is free again, which proves the name index is derived
	// from the records rather than kept separately and leaked.
	assert.NoError(t, svc.AddForeignKey(ctx, &CreateTableForeignKeyRequest{
		Database: "shop", Table: "orders", ForeignKey: testForeignKey("fk_a"),
	}))
}

// TestTableService_UpdateForeignKeyReplacesInPlace asserts a replacement keeps
// its position in the constraint list. The SQL engine builds its
// per-statement enforcement plan by iterating that order, so a replacement
// that moved to the end would silently reorder enforcement.
func TestTableService_UpdateForeignKeyReplacesInPlace(t *testing.T) {
	svc, _ := newFKTestService(t)
	ctx := context.Background()

	for _, name := range []string{"fk_a", "fk_b", "fk_c"} {
		require.NoError(t, svc.AddForeignKey(ctx, &CreateTableForeignKeyRequest{
			Database: "shop", Table: "users", ForeignKey: testForeignKey(name),
		}))
	}

	replacement := testForeignKey("fk_b_renamed")
	replacement.OnDelete = "SET NULL"
	require.NoError(t, svc.UpdateForeignKey(ctx, &UpdateTableForeignKeyRequest{
		Database: "shop", Table: "users", Name: "fk_b", ForeignKey: replacement,
	}))

	def, err := svc.GetTable(ctx, "shop", "users")
	require.NoError(t, err)
	require.Len(t, def.ForeignKeys, 3)
	assert.Equal(t, []string{"fk_a", "fk_b_renamed", "fk_c"},
		[]string{def.ForeignKeys[0].Name, def.ForeignKeys[1].Name, def.ForeignKeys[2].Name})
	assert.Equal(t, "SET NULL", def.ForeignKeys[1].OnDelete)
}

// TestTableService_UpdateForeignKeyNameRules asserts a rename obeys the same
// database-wide uniqueness rule as an add, while replacing a constraint under
// its own name does not collide with itself.
func TestTableService_UpdateForeignKeyNameRules(t *testing.T) {
	svc, _ := newFKTestService(t)
	ctx := context.Background()

	require.NoError(t, svc.AddForeignKey(ctx, &CreateTableForeignKeyRequest{
		Database: "shop", Table: "users", ForeignKey: testForeignKey("fk_a"),
	}))
	require.NoError(t, svc.AddForeignKey(ctx, &CreateTableForeignKeyRequest{
		Database: "shop", Table: "orders", ForeignKey: testForeignKey("fk_taken"),
	}))

	clash := testForeignKey("fk_taken")
	assert.IsType(t, &TableForeignKeyAlreadyExistsError{}, svc.UpdateForeignKey(ctx,
		&UpdateTableForeignKeyRequest{Database: "shop", Table: "users", Name: "fk_a", ForeignKey: clash}))

	same := testForeignKey("fk_a")
	same.OnUpdate = "CASCADE"
	assert.NoError(t, svc.UpdateForeignKey(ctx,
		&UpdateTableForeignKeyRequest{Database: "shop", Table: "users", Name: "fk_a", ForeignKey: same}))

	assert.IsType(t, &TableForeignKeyNotFoundError{}, svc.UpdateForeignKey(ctx,
		&UpdateTableForeignKeyRequest{Database: "shop", Table: "users", Name: "fk_missing", ForeignKey: same}))
	assert.IsType(t, &TableInvalidNameError{}, svc.UpdateForeignKey(ctx,
		&UpdateTableForeignKeyRequest{Database: "shop", Table: "users", Name: "", ForeignKey: same}))
	assert.IsType(t, &TableNotFoundError{}, svc.UpdateForeignKey(ctx,
		&UpdateTableForeignKeyRequest{Database: "shop", Table: "ghosts", Name: "fk_a", ForeignKey: same}))

	// A replacement that contradicts the declaring table is refused, so an
	// update cannot be used to smuggle in what an add would reject.
	bad := testForeignKey("fk_a")
	bad.Columns = []string{"nope"}
	assert.IsType(t, &TableForeignKeyColumnNotFoundError{}, svc.UpdateForeignKey(ctx,
		&UpdateTableForeignKeyRequest{Database: "shop", Table: "users", Name: "fk_a", ForeignKey: bad}))
}

// TestTableService_ReferencingForeignKeys covers the reverse lookup: every
// constraint in the database pointing at a given parent, ordered by declaring
// table so repeated calls agree.
func TestTableService_ReferencingForeignKeys(t *testing.T) {
	svc, _ := newFKTestService(t)
	ctx := context.Background()

	require.NoError(t, svc.AddForeignKey(ctx, &CreateTableForeignKeyRequest{
		Database: "shop", Table: "users", ForeignKey: testForeignKey("fk_users"),
	}))
	require.NoError(t, svc.AddForeignKey(ctx, &CreateTableForeignKeyRequest{
		Database: "shop", Table: "orders", ForeignKey: testForeignKey("fk_orders"),
	}))

	elsewhere := testForeignKey("fk_elsewhere")
	elsewhere.ParentTable = "users"
	require.NoError(t, svc.AddForeignKey(ctx, &CreateTableForeignKeyRequest{
		Database: "shop", Table: "orders", ForeignKey: elsewhere,
	}))

	refs, err := svc.ReferencingForeignKeys(ctx, "shop", "companies")
	require.NoError(t, err)
	require.Len(t, refs, 2)
	assert.Equal(t, "orders", refs[0].Table)
	assert.Equal(t, "fk_orders", refs[0].ForeignKey.Name)
	assert.Equal(t, "users", refs[1].Table)
	assert.Equal(t, "fk_users", refs[1].ForeignKey.Name)

	// Matching is case insensitive, and a table nothing points at reports
	// nothing rather than erroring.
	refs, err = svc.ReferencingForeignKeys(ctx, "shop", "COMPANIES")
	require.NoError(t, err)
	assert.Len(t, refs, 2)

	refs, err = svc.ReferencingForeignKeys(ctx, "shop", "users")
	require.NoError(t, err)
	require.Len(t, refs, 1)
	assert.Equal(t, "fk_elsewhere", refs[0].ForeignKey.Name)

	refs, err = svc.ReferencingForeignKeys(ctx, "shop", "orders")
	require.NoError(t, err)
	assert.Empty(t, refs)

	assert.IsType(t, &TableEmptyDatabaseError{}, mustErr(svc.ReferencingForeignKeys(ctx, "", "companies")))
	assert.IsType(t, &TableInvalidNameError{}, mustErr(svc.ReferencingForeignKeys(ctx, "shop", "")))
}

// TestTableService_ReferencingForeignKeysSelfReferential asserts a table
// pointing at itself is reported by the reverse lookup, since it genuinely is
// both the declaring and the parent table.
func TestTableService_ReferencingForeignKeysSelfReferential(t *testing.T) {
	svc, _ := newFKTestService(t)
	ctx := context.Background()

	self := testForeignKey("fk_self")
	self.ParentTable = "users"
	require.NoError(t, svc.AddForeignKey(ctx, &CreateTableForeignKeyRequest{
		Database: "shop", Table: "users", ForeignKey: self,
	}))

	refs, err := svc.ReferencingForeignKeys(ctx, "shop", "users")
	require.NoError(t, err)
	require.Len(t, refs, 1)
	assert.Equal(t, "users", refs[0].Table)
}

// TestTableService_ReferencingForeignKeysIgnoresNonTableKeys asserts the
// catalog scan skips the database's epoch key and any table's DDL journal
// key, both of which live under the same prefix as the table records and are
// not table definitions.
func TestTableService_ReferencingForeignKeysIgnoresNonTableKeys(t *testing.T) {
	svc, store := newFKTestService(t)
	ctx := context.Background()

	// A rewrite journal, written by an AlterTable that needs one.
	_, err := svc.AlterTable(ctx, &AlterTableRequest{
		Database: "shop", Name: "orders",
		Changes: []AlterTableChange{{Action: AlterDropColumn, Name: "company_id"}},
	})
	var rewrite *TableRewriteRequiredError
	require.True(t, errors.As(err, &rewrite), "err = %v", err)

	journal, jerr := store.Get(ctx, tableDDLJournalKey("shop", "orders"))
	require.NoError(t, jerr)
	require.NotNil(t, journal)

	require.NoError(t, svc.AddForeignKey(ctx, &CreateTableForeignKeyRequest{
		Database: "shop", Table: "users", ForeignKey: testForeignKey("fk_users"),
	}))

	// The scan must not try to decode the epoch value or the journal as a
	// table definition, and must still find the one real constraint.
	refs, rerr := svc.ReferencingForeignKeys(ctx, "shop", "companies")
	require.NoError(t, rerr)
	require.Len(t, refs, 1)
	assert.Equal(t, "fk_users", refs[0].ForeignKey.Name)
}

// TestTableService_ForeignKeyScanErrorPropagates asserts a failed catalog
// scan is reported rather than being read as "no constraints", which would
// turn a transport failure into a silently unenforced constraint.
func TestTableService_ForeignKeyScanErrorPropagates(t *testing.T) {
	svc, store := newFKTestService(t)
	ctx := context.Background()

	store.injectScanErr = errors.New("scan unavailable")
	_, err := svc.ReferencingForeignKeys(ctx, "shop", "companies")
	var meta *TableMetadataError
	require.True(t, errors.As(err, &meta), "err = %v", err)
	assert.Equal(t, "scan", meta.Op)

	store.injectScanErr = errors.New("scan unavailable")
	err = svc.AddForeignKey(ctx, &CreateTableForeignKeyRequest{
		Database: "shop", Table: "users", ForeignKey: testForeignKey("fk_a"),
	})
	require.True(t, errors.As(err, &meta), "err = %v", err)
}

// TestTableService_DropColumnDropsReferencingConstraints asserts a column
// drop takes any constraint naming it with it. The SQL layer refuses this
// case outright (ErrForeignKeyDropColumn), so this is the catalog's own last
// line of defense for a direct service caller: leaving the constraint behind
// would leave a record referencing a column that no longer exists.
func TestTableService_DropColumnDropsReferencingConstraints(t *testing.T) {
	svc, _ := newFKTestService(t)
	ctx := context.Background()

	keep := testForeignKey("fk_keep")
	keep.Columns = []string{"id"}
	require.NoError(t, svc.AddForeignKey(ctx, &CreateTableForeignKeyRequest{
		Database: "shop", Table: "users", ForeignKey: keep,
	}))
	require.NoError(t, svc.AddForeignKey(ctx, &CreateTableForeignKeyRequest{
		Database: "shop", Table: "users", ForeignKey: testForeignKey("fk_drop"),
	}))

	_, err := svc.AlterTable(ctx, &AlterTableRequest{
		Database: "shop", Name: "users",
		Changes: []AlterTableChange{{Action: AlterDropColumn, Name: "company_id"}},
	})
	var rewrite *TableRewriteRequiredError
	require.True(t, errors.As(err, &rewrite), "err = %v", err)

	journal, _, jerr := svc.RewriteJournal(ctx, "shop", "users")
	require.NoError(t, jerr)
	require.NotNil(t, journal.Target)
	require.Len(t, journal.Target.ForeignKeys, 1)
	assert.Equal(t, "fk_keep", journal.Target.ForeignKeys[0].Name)
}

// TestTableService_DropColumnDropsSelfReferentialParentColumn asserts the
// same for a self-referential constraint's PARENT columns, which are the only
// parent columns that live on this same table.
func TestTableService_DropColumnDropsSelfReferentialParentColumn(t *testing.T) {
	svc, _ := newFKTestService(t)
	ctx := context.Background()

	self := ForeignKeyDef{
		Name: "fk_self", Columns: []string{"id"},
		ParentTable: "users", ParentColumns: []string{"company_id"},
	}
	require.NoError(t, svc.AddForeignKey(ctx, &CreateTableForeignKeyRequest{
		Database: "shop", Table: "users", ForeignKey: self,
	}))

	_, err := svc.AlterTable(ctx, &AlterTableRequest{
		Database: "shop", Name: "users",
		Changes: []AlterTableChange{{Action: AlterDropColumn, Name: "company_id"}},
	})
	var rewrite *TableRewriteRequiredError
	require.True(t, errors.As(err, &rewrite), "err = %v", err)

	journal, _, jerr := svc.RewriteJournal(ctx, "shop", "users")
	require.NoError(t, jerr)
	require.NotNil(t, journal.Target)
	assert.Empty(t, journal.Target.ForeignKeys)
}

// TestTableService_ForeignKeyFieldsDefaultOnLegacyRecord asserts a catalog
// record written before ForeignKeyDef gained Resolved/NotValid/MatchType
// decodes with the safe defaults: unresolved (so the engine re-validates
// rather than trusting it), valid, MATCH SIMPLE.
func TestTableService_ForeignKeyFieldsDefaultOnLegacyRecord(t *testing.T) {
	svc, store := newFKTestService(t)
	ctx := context.Background()

	legacy := `{"table_id":1,"name":"users","database":"shop",` +
		`"columns":[{"name":"id","type":"BIGINT UNSIGNED","nullable":false},` +
		`{"name":"company_id","type":"BIGINT UNSIGNED","nullable":true}],` +
		`"foreign_keys":[{"name":"fk_legacy","columns":["company_id"],` +
		`"parent_table":"companies","parent_columns":["id"]}],"schema_version":1}`
	_, err := store.Execute(ctx, nil, []Op{{
		Type: OpPut, Key: []byte(tableMetadataKey("shop", "users")), Value: []byte(legacy),
	}}, nil)
	require.NoError(t, err)

	def, err := svc.GetTable(ctx, "shop", "users")
	require.NoError(t, err)
	require.Len(t, def.ForeignKeys, 1)
	fk := def.ForeignKeys[0]
	assert.False(t, fk.Resolved)
	assert.False(t, fk.NotValid)
	assert.Equal(t, uint8(0), fk.MatchType)
	assert.Equal(t, "", fk.OnDelete)
	assert.Equal(t, "", fk.OnUpdate)
}

// mustErr discards a value and returns the error, so a two-result call can be
// asserted on inline.
func mustErr[T any](_ T, err error) error { return err }
