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

package provider

import (
	"context"
	"errors"
	"strings"
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/vitess/go/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	goerrors "gopkg.in/src-d/go-errors.v1"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// FOREIGN KEY support for RelationalTable.
//
// The division of labour this file tests is deliberate and is described at
// the top of relational_foreign_key.go: qrdb persists and reports constraint
// metadata, go-mysql-server enforces it. So the assertions come in two
// flavours, and both matter:
//
//   - Metadata assertions, driven through the sql.ForeignKeyTable interface
//     directly, pinning that what goes in comes back out unchanged (the
//     engine plans enforcement from exactly these values) and that anything
//     unrepresentable is refused rather than silently weakened.
//   - Behaviour assertions, driven through real SQL text end to end, proving
//     the engine actually enforces what it was told -- a constraint that
//     round-trips perfectly but is never enforced would pass every metadata
//     test in the file.
// ---------------------------------------------------------------------------

// fkTable resolves name as a *RelationalTable through h's registry, ready
// to drive the sql.ForeignKeyTable surface directly.
func fkTable(t *testing.T, h *ddlHarness, database, name string) *RelationalTable {
	t.Helper()
	db := h.newDDLDatabase(database)
	tbl, ok, err := db.GetTableInsensitive(sqle.NewContext(context.Background()), name)
	require.NoError(t, err)
	require.True(t, ok, "table %s.%s not found", database, name)
	rel, ok := tbl.(*RelationalTable)
	require.True(t, ok, "table %s.%s is %T, want *RelationalTable", database, name, tbl)
	return rel
}

// fkParentChild creates a companies/users pair with a single declared
// foreign key and returns a provider over the same catalog.
func fkParentChild(t *testing.T, h *ddlHarness) *Provider {
	t.Helper()
	prov := h.newDDLProvider("shop")
	mustExecQuery(t, prov, "shop", `CREATE TABLE companies (id BIGINT PRIMARY KEY, name VARCHAR(64))`)
	mustExecQuery(t, prov, "shop", `CREATE TABLE users (
		id BIGINT PRIMARY KEY,
		name VARCHAR(64),
		company_id BIGINT,
		CONSTRAINT fk_users_company FOREIGN KEY (company_id) REFERENCES companies(id)
	)`)
	return prov
}

// assertFKViolation asserts err is the given foreign key violation, both as
// go-mysql-server's own error kind and as the MySQL error number a client
// actually receives -- 1452 for a child row with no parent, 1451 for a
// parent row that still has children. gorm.io/driver/mysql's error translator matches
// on those numbers to produce gorm.ErrForeignKeyViolated, so the number is
// as much part of the contract as the kind is.
//
// The error is unwrapped first: go-mysql-server wraps an INSERT-time failure
// in sql.WrappedInsertError to attach the offending row, and the kind check
// does not see through that.
func assertFKViolation(t *testing.T, err error, kind *goerrors.Kind, wantCode uint16) {
	t.Helper()
	require.Error(t, err)
	assert.True(t, kind.Is(sqle.UnwrapError(err)), "err = %v (%T)", err, err)

	sqlErr := sqle.CastSQLError(err)
	require.NotNil(t, sqlErr, "err = %v (%T) did not map to a MySQL error", err, err)
	assert.Equal(t, int(wantCode), sqlErr.Number(), "err = %v", err)
}

// ---------------------------------------------------------------------------
// Metadata round trip
// ---------------------------------------------------------------------------

// TestForeignKey_DeclaredRoundTrip pins that every field go-mysql-server
// hands to AddForeignKey comes back from GetDeclaredForeignKeys unchanged.
// The engine plans enforcement from these exact values, so a field silently
// dropped here is a constraint the engine believes it is enforcing under
// terms qrdb did not keep.
func TestForeignKey_DeclaredRoundTrip(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	mustExecQuery(t, prov, "shop", `CREATE TABLE companies (id BIGINT PRIMARY KEY)`)
	mustExecQuery(t, prov, "shop", `CREATE TABLE users (id BIGINT PRIMARY KEY, company_id BIGINT)`)

	users := fkTable(t, h, "shop", "users")
	ctx := sqle.NewContext(context.Background())

	want := sqle.ForeignKeyConstraint{
		Name:           "fk_round_trip",
		Database:       "shop",
		Table:          "users",
		ParentDatabase: "shop",
		ParentTable:    "companies",
		Columns:        []string{"company_id"},
		ParentColumns:  []string{"id"},
		OnUpdate:       sqle.ForeignKeyReferentialAction_Cascade,
		OnDelete:       sqle.ForeignKeyReferentialAction_SetNull,
		IsResolved:     true,
		IsNotValid:     true,
		MatchType:      sqle.ForeignKeyMatchType_Full,
	}
	require.NoError(t, users.AddForeignKey(ctx, want))

	got, err := users.GetDeclaredForeignKeys(ctx)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, want, got[0])
}

// TestForeignKey_ResolvedFlagIsPersistedNotSynthesized asserts IsResolved is
// stored as given rather than assumed. An unresolved constraint stored as
// resolved would skip the engine's own validation of it on first use, which
// is exactly the case a constraint added under foreign_key_checks=0 depends
// on.
func TestForeignKey_ResolvedFlagIsPersistedNotSynthesized(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	mustExecQuery(t, prov, "shop", `CREATE TABLE companies (id BIGINT PRIMARY KEY)`)
	mustExecQuery(t, prov, "shop", `CREATE TABLE users (id BIGINT PRIMARY KEY, company_id BIGINT)`)

	users := fkTable(t, h, "shop", "users")
	ctx := sqle.NewContext(context.Background())

	base := sqle.ForeignKeyConstraint{
		Name: "fk_unresolved", Database: "shop", Table: "users",
		ParentDatabase: "shop", ParentTable: "companies",
		Columns: []string{"company_id"}, ParentColumns: []string{"id"},
		IsResolved: false,
	}
	require.NoError(t, users.AddForeignKey(ctx, base))

	got, err := users.GetDeclaredForeignKeys(ctx)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.False(t, got[0].IsResolved, "IsResolved must round-trip false, not be synthesized true")

	// The engine sets it through UpdateForeignKey once it has validated the
	// parent; that is the only way it becomes true.
	resolved := base
	resolved.IsResolved = true
	require.NoError(t, users.UpdateForeignKey(ctx, "fk_unresolved", resolved))

	got, err = users.GetDeclaredForeignKeys(ctx)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.True(t, got[0].IsResolved)
}

// TestForeignKey_ReverseLookup asserts GetReferencedForeignKeys finds every
// constraint pointing AT a table, across every table in the database --
// the direction the catalog is not keyed for.
func TestForeignKey_ReverseLookup(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	mustExecQuery(t, prov, "shop", `CREATE TABLE companies (id BIGINT PRIMARY KEY)`)
	mustExecQuery(t, prov, "shop", `CREATE TABLE users (id BIGINT PRIMARY KEY, company_id BIGINT,
		CONSTRAINT fk_users_company FOREIGN KEY (company_id) REFERENCES companies(id))`)
	mustExecQuery(t, prov, "shop", `CREATE TABLE orders (id BIGINT PRIMARY KEY, company_id BIGINT,
		CONSTRAINT fk_orders_company FOREIGN KEY (company_id) REFERENCES companies(id))`)
	mustExecQuery(t, prov, "shop", `CREATE TABLE unrelated (id BIGINT PRIMARY KEY)`)

	ctx := sqle.NewContext(context.Background())

	refs, err := fkTable(t, h, "shop", "companies").GetReferencedForeignKeys(ctx)
	require.NoError(t, err)
	require.Len(t, refs, 2)

	// Ordered by declaring table name, so the list is reproducible.
	assert.Equal(t, "orders", refs[0].Table)
	assert.Equal(t, "fk_orders_company", refs[0].Name)
	assert.Equal(t, "companies", refs[0].ParentTable)
	assert.Equal(t, "users", refs[1].Table)
	assert.Equal(t, "fk_users_company", refs[1].Name)

	// A table nothing points at reports none, and a table that only
	// DECLARES a constraint does not report it as one referencing itself.
	none, err := fkTable(t, h, "shop", "unrelated").GetReferencedForeignKeys(ctx)
	require.NoError(t, err)
	assert.Empty(t, none)

	none, err = fkTable(t, h, "shop", "users").GetReferencedForeignKeys(ctx)
	require.NoError(t, err)
	assert.Empty(t, none)
}

// TestForeignKey_ReverseLookupSelfReferential asserts a self-referential
// constraint appears on BOTH sides, since the table is genuinely both the
// declaring and the parent table.
func TestForeignKey_ReverseLookupSelfReferential(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	mustExecQuery(t, prov, "shop", `CREATE TABLE nodes (id BIGINT PRIMARY KEY, parent_id BIGINT,
		CONSTRAINT fk_nodes_parent FOREIGN KEY (parent_id) REFERENCES nodes(id))`)

	ctx := sqle.NewContext(context.Background())
	nodes := fkTable(t, h, "shop", "nodes")

	declared, err := nodes.GetDeclaredForeignKeys(ctx)
	require.NoError(t, err)
	require.Len(t, declared, 1)
	assert.True(t, declared[0].IsSelfReferential())

	referenced, err := nodes.GetReferencedForeignKeys(ctx)
	require.NoError(t, err)
	require.Len(t, referenced, 1)
	assert.Equal(t, "fk_nodes_parent", referenced[0].Name)
}

// TestForeignKey_DropAndUpdate covers the two mutations that are not adds:
// removing a constraint, and replacing one in place (which is also how it is
// renamed).
func TestForeignKey_DropAndUpdate(t *testing.T) {
	h := newDDLHarness()
	fkParentChild(t, h)

	ctx := sqle.NewContext(context.Background())
	users := fkTable(t, h, "shop", "users")

	declared, err := users.GetDeclaredForeignKeys(ctx)
	require.NoError(t, err)
	require.Len(t, declared, 1)

	renamed := declared[0]
	renamed.Name = "fk_renamed"
	require.NoError(t, users.UpdateForeignKey(ctx, "fk_users_company", renamed))

	declared, err = users.GetDeclaredForeignKeys(ctx)
	require.NoError(t, err)
	require.Len(t, declared, 1)
	assert.Equal(t, "fk_renamed", declared[0].Name)

	require.NoError(t, users.DropForeignKey(ctx, "fk_renamed", "users", ""))
	declared, err = users.GetDeclaredForeignKeys(ctx)
	require.NoError(t, err)
	assert.Empty(t, declared)

	// Dropping what is not there is an error, not a silent success.
	err = users.DropForeignKey(ctx, "fk_renamed", "users", "")
	assert.True(t, sqle.ErrForeignKeyNotFound.Is(err), "err = %v", err)
}

// TestForeignKey_DropRoutesByTableName asserts the tableName argument
// selects the declaring table, since go-mysql-server may route the call
// through any table's ForeignKeyTable.
func TestForeignKey_DropRoutesByTableName(t *testing.T) {
	h := newDDLHarness()
	fkParentChild(t, h)

	ctx := sqle.NewContext(context.Background())
	companies := fkTable(t, h, "shop", "companies")

	require.NoError(t, companies.DropForeignKey(ctx, "fk_users_company", "users", ""))

	declared, err := fkTable(t, h, "shop", "users").GetDeclaredForeignKeys(ctx)
	require.NoError(t, err)
	assert.Empty(t, declared)
}

// ---------------------------------------------------------------------------
// Name scoping
// ---------------------------------------------------------------------------

// TestForeignKey_NameIsDatabaseScoped asserts a constraint name already used
// by ANY table in the database is rejected. MySQL scopes constraint names to
// the database, not the table; go-mysql-server only checks the declaring
// table before calling AddForeignKey, so this rule has to live here or not
// exist at all.
func TestForeignKey_NameIsDatabaseScoped(t *testing.T) {
	h := newDDLHarness()
	fkParentChild(t, h)

	ctx := sqle.NewContext(context.Background())
	prov := h.newDDLProvider("shop")
	mustExecQuery(t, prov, "shop", `CREATE TABLE orders (id BIGINT PRIMARY KEY, company_id BIGINT)`)

	orders := fkTable(t, h, "shop", "orders")
	err := orders.AddForeignKey(ctx, sqle.ForeignKeyConstraint{
		Name: "fk_users_company", Database: "shop", Table: "orders",
		ParentDatabase: "shop", ParentTable: "companies",
		Columns: []string{"company_id"}, ParentColumns: []string{"id"},
	})
	assert.True(t, sqle.ErrForeignKeyDuplicateName.Is(err), "err = %v", err)

	// And nothing was written.
	declared, derr := orders.GetDeclaredForeignKeys(ctx)
	require.NoError(t, derr)
	assert.Empty(t, declared)
}

// TestForeignKey_RenameOntoTakenNameRejected asserts the same
// database-scoped rule applies to a rename, while renaming a constraint to
// the name it already has stays a no-op rather than colliding with itself.
func TestForeignKey_RenameOntoTakenNameRejected(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	mustExecQuery(t, prov, "shop", `CREATE TABLE companies (id BIGINT PRIMARY KEY)`)
	mustExecQuery(t, prov, "shop", `CREATE TABLE users (id BIGINT PRIMARY KEY, a_id BIGINT, b_id BIGINT,
		CONSTRAINT fk_a FOREIGN KEY (a_id) REFERENCES companies(id),
		CONSTRAINT fk_b FOREIGN KEY (b_id) REFERENCES companies(id))`)

	ctx := sqle.NewContext(context.Background())
	users := fkTable(t, h, "shop", "users")
	declared, err := users.GetDeclaredForeignKeys(ctx)
	require.NoError(t, err)
	require.Len(t, declared, 2)

	clash := declared[0]
	clash.Name = declared[1].Name
	assert.True(t, sqle.ErrForeignKeyDuplicateName.Is(users.UpdateForeignKey(ctx, declared[0].Name, clash)),
		"renaming onto a name already in use must be rejected")

	same := declared[0]
	assert.NoError(t, users.UpdateForeignKey(ctx, declared[0].Name, same),
		"replacing a constraint under its own name must not collide with itself")
}

// ---------------------------------------------------------------------------
// Refusals
// ---------------------------------------------------------------------------

// TestForeignKey_UnrepresentableIsRejected asserts every constraint shape
// qrdb cannot honor is refused outright. Silently accepting one would leave
// the engine enforcing a constraint qrdb did not actually store, or storing
// one whose parent can never be resolved.
func TestForeignKey_UnrepresentableIsRejected(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	mustExecQuery(t, prov, "shop", `CREATE TABLE companies (id BIGINT PRIMARY KEY)`)
	mustExecQuery(t, prov, "shop", `CREATE TABLE users (id BIGINT PRIMARY KEY, company_id BIGINT)`)

	ctx := sqle.NewContext(context.Background())
	users := fkTable(t, h, "shop", "users")

	valid := sqle.ForeignKeyConstraint{
		Name: "fk_x", Database: "shop", Table: "users",
		ParentDatabase: "shop", ParentTable: "companies",
		Columns: []string{"company_id"}, ParentColumns: []string{"id"},
	}

	tests := []struct {
		name   string
		mutate func(fk *sqle.ForeignKeyConstraint)
		want   any
	}{
		{
			name:   "schema qualified",
			mutate: func(fk *sqle.ForeignKeyConstraint) { fk.SchemaName = "public" },
			want:   &ForeignKeySchemaUnsupportedError{},
		},
		{
			name:   "parent schema qualified",
			mutate: func(fk *sqle.ForeignKeyConstraint) { fk.ParentSchema = "public" },
			want:   &ForeignKeySchemaUnsupportedError{},
		},
		{
			name:   "cross database parent",
			mutate: func(fk *sqle.ForeignKeyConstraint) { fk.ParentDatabase = "elsewhere" },
			want:   &ForeignKeyCrossDatabaseUnsupportedError{},
		},
		{
			name:   "cross database declaring side",
			mutate: func(fk *sqle.ForeignKeyConstraint) { fk.Database = "elsewhere" },
			want:   &ForeignKeyCrossDatabaseUnsupportedError{},
		},
		{
			name:   "declares a different table",
			mutate: func(fk *sqle.ForeignKeyConstraint) { fk.Table = "orders" },
			want:   &ForeignKeyTableMismatchError{},
		},
		{
			name:   "unknown on delete action",
			mutate: func(fk *sqle.ForeignKeyConstraint) { fk.OnDelete = "EXPLODE" },
			want:   &ForeignKeyActionUnsupportedError{},
		},
		{
			name:   "unknown on update action",
			mutate: func(fk *sqle.ForeignKeyConstraint) { fk.OnUpdate = "EXPLODE" },
			want:   &ForeignKeyActionUnsupportedError{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fk := valid
			tc.mutate(&fk)
			err := users.AddForeignKey(ctx, fk)
			require.Error(t, err)
			assert.IsType(t, tc.want, err)

			declared, derr := users.GetDeclaredForeignKeys(ctx)
			require.NoError(t, derr)
			assert.Empty(t, declared, "a refused constraint must not be stored")
		})
	}
}

// TestForeignKey_UnknownLocalColumnRejected asserts a constraint naming a
// column the declaring table does not have is refused. That is knowable from
// the table's own record, so accepting it would produce a catalog entry no
// enforcement plan could ever be built from.
func TestForeignKey_UnknownLocalColumnRejected(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	mustExecQuery(t, prov, "shop", `CREATE TABLE companies (id BIGINT PRIMARY KEY)`)
	mustExecQuery(t, prov, "shop", `CREATE TABLE users (id BIGINT PRIMARY KEY, company_id BIGINT)`)

	ctx := sqle.NewContext(context.Background())
	err := fkTable(t, h, "shop", "users").AddForeignKey(ctx, sqle.ForeignKeyConstraint{
		Name: "fk_bad", Database: "shop", Table: "users",
		ParentDatabase: "shop", ParentTable: "companies",
		Columns: []string{"nope"}, ParentColumns: []string{"id"},
	})
	require.Error(t, err)
	assert.IsType(t, &service.TableForeignKeyColumnNotFoundError{}, err)
}

// TestForeignKey_SchemaQualifiedDropRejected asserts DropForeignKey refuses a
// schema name for the same reason AddForeignKey does, rather than ignoring it
// and dropping whatever constraint carries the bare name.
func TestForeignKey_SchemaQualifiedDropRejected(t *testing.T) {
	h := newDDLHarness()
	fkParentChild(t, h)

	ctx := sqle.NewContext(context.Background())
	err := fkTable(t, h, "shop", "users").DropForeignKey(ctx, "fk_users_company", "users", "public")
	require.Error(t, err)
	assert.IsType(t, &ForeignKeySchemaUnsupportedError{}, err)
}

// TestForeignKey_ServiceUnavailable asserts every ForeignKeyTable method
// reports the missing dependency rather than panicking when the table was
// built without a TableServicer.
func TestForeignKey_ServiceUnavailable(t *testing.T) {
	h := newDDLHarness()
	fkParentChild(t, h)

	def, err := h.tableSvc.GetTable(context.Background(), "shop", "users")
	require.NoError(t, err)
	bare, err := NewRelationalTable(def, h.kvSvc, h.mvccSvc)
	require.NoError(t, err)

	ctx := sqle.NewContext(context.Background())

	_, err = bare.GetDeclaredForeignKeys(ctx)
	assert.IsType(t, &TableServiceUnavailableError{}, err)
	_, err = bare.GetReferencedForeignKeys(ctx)
	assert.IsType(t, &TableServiceUnavailableError{}, err)
	assert.IsType(t, &TableServiceUnavailableError{},
		bare.AddForeignKey(ctx, sqle.ForeignKeyConstraint{Name: "fk"}))
	assert.IsType(t, &TableServiceUnavailableError{},
		bare.DropForeignKey(ctx, "fk", "users", ""))
	assert.IsType(t, &TableServiceUnavailableError{},
		bare.UpdateForeignKey(ctx, "fk", sqle.ForeignKeyConstraint{Name: "fk"}))
	assert.IsType(t, &TableServiceUnavailableError{},
		bare.CreateIndexForForeignKey(ctx, sqle.IndexDef{Name: "i", Columns: []sqle.IndexColumn{{Name: "company_id"}}}))
}

// ---------------------------------------------------------------------------
// Editor
// ---------------------------------------------------------------------------

// TestForeignKey_EditorIsTheOrdinaryWriter asserts GetForeignKeyEditor hands
// back the table's own DML writer plus index addressability. That is what
// makes a cascading write go through the identical atomic row-plus-index
// compare-and-swap a user-issued write does, instead of a second write path
// with its own bugs.
func TestForeignKey_EditorIsTheOrdinaryWriter(t *testing.T) {
	h := newDDLHarness()
	fkParentChild(t, h)

	ctx := sqle.NewContext(context.Background())
	users := fkTable(t, h, "shop", "users")

	editor := users.GetForeignKeyEditor(ctx)
	require.NotNil(t, editor)

	typed, ok := editor.(*relationalForeignKeyEditor)
	require.True(t, ok, "editor is %T", editor)
	assert.Same(t, users, typed.table, "the editor must write through this very table")

	assert.True(t, editor.PreciseMatch())

	editorIndexes, err := editor.GetIndexes(ctx)
	require.NoError(t, err)
	tableIndexes, err := users.GetIndexes(ctx)
	require.NoError(t, err)
	assert.Equal(t, tableIndexes, editorIndexes)

	assert.Same(t, sqle.IndexedTable(users), editor.IndexedAccess(ctx, sqle.IndexLookup{}))
}

// ---------------------------------------------------------------------------
// Index backfill
// ---------------------------------------------------------------------------

// TestForeignKey_CreateIndexForForeignKeyBackfills asserts the index a
// constraint needs is populated from rows that already exist.
//
// go-mysql-server calls CreateIndexForForeignKey directly, without the
// ShouldBuildIndex/BuildIndex follow-up its DDL executor gives an ordinary
// CREATE INDEX, so an implementation that only wrote the catalog entry would
// leave the index empty. That failure is silent and inverted: every
// parent-existence lookup would find nothing, so the constraint would reject
// every child row rather than erroring.
func TestForeignKey_CreateIndexForForeignKeyBackfills(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	mustExecQuery(t, prov, "shop", `CREATE TABLE companies (id BIGINT PRIMARY KEY)`)
	mustExecQuery(t, prov, "shop", `CREATE TABLE users (id BIGINT PRIMARY KEY, company_id BIGINT)`)
	mustExecQuery(t, prov, "shop", `INSERT INTO companies VALUES (1)`)
	mustExecQuery(t, prov, "shop", `INSERT INTO users VALUES (1, 1), (2, 1), (3, 1)`)

	ctx := sqle.NewContext(context.Background())
	users := fkTable(t, h, "shop", "users")
	require.NoError(t, users.CreateIndexForForeignKey(ctx, sqle.IndexDef{
		Name:       "idx_company",
		Columns:    []sqle.IndexColumn{{Name: "company_id"}},
		Constraint: sqle.IndexConstraint_None,
	}))

	// The catalog entry is marked as foreign-key generated...
	def, err := h.tableSvc.GetTable(context.Background(), "shop", "users")
	require.NoError(t, err)
	idx := findTableIndex(def, "idx_company")
	require.NotNil(t, idx)
	assert.True(t, idx.ForeignKey)

	// ...and the physical entries exist for all three pre-existing rows.
	entries, err := h.kvSvc.List(context.Background(), service.TableIndexPrefix("shop", def.TableID, 0, incarnationOf(def, "idx_company")))
	require.NoError(t, err)
	assert.Len(t, entries, 3, "index was not backfilled from existing rows")
}

// TestForeignKey_OrdinaryCreateIndexIsNotMarkedForeignKey is the negative
// half of the previous test: an index the user declared must not be labelled
// as one generated for a constraint, since that label is what tells
// SHOW CREATE TABLE and the drop-index guard the two apart.
func TestForeignKey_OrdinaryCreateIndexIsNotMarkedForeignKey(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	mustExecQuery(t, prov, "shop", `CREATE TABLE users (id BIGINT PRIMARY KEY, email VARCHAR(64))`)
	mustExecQuery(t, prov, "shop", `CREATE INDEX idx_email ON users (email)`)

	def, err := h.tableSvc.GetTable(context.Background(), "shop", "users")
	require.NoError(t, err)
	idx := findTableIndex(def, "idx_email")
	require.NotNil(t, idx)
	assert.False(t, idx.ForeignKey)
}

// ---------------------------------------------------------------------------
// Enforcement, end to end through real SQL
// ---------------------------------------------------------------------------

// TestForeignKey_ChildViolationRejected asserts a child row with no parent is
// refused, and that nothing was written.
func TestForeignKey_ChildViolationRejected(t *testing.T) {
	h := newDDLHarness()
	prov := fkParentChild(t, h)

	mustExecQuery(t, prov, "shop", `INSERT INTO companies VALUES (1, 'acme')`)
	mustExecQuery(t, prov, "shop", `INSERT INTO users VALUES (1, 'bob', 1)`)

	_, _, err := execQuery(prov, "shop", `INSERT INTO users VALUES (2, 'eve', 99)`)
	assertFKViolation(t, err, sqle.ErrForeignKeyChildViolation, mysql.ErNoReferencedRow2)

	_, rows := mustExecQuery(t, prov, "shop", `SELECT id FROM users`)
	assert.Len(t, rows, 1, "the rejected child row must not have been written")

	// A NULL key column is exempt from the check, matching MATCH SIMPLE.
	mustExecQuery(t, prov, "shop", `INSERT INTO users VALUES (3, 'nobody', NULL)`)
}

// TestForeignKey_ParentDeleteRestricted asserts deleting a parent that still
// has children is refused under the default (RESTRICT-equivalent) action.
func TestForeignKey_ParentDeleteRestricted(t *testing.T) {
	h := newDDLHarness()
	prov := fkParentChild(t, h)

	mustExecQuery(t, prov, "shop", `INSERT INTO companies VALUES (1, 'acme')`)
	mustExecQuery(t, prov, "shop", `INSERT INTO users VALUES (1, 'bob', 1)`)

	_, _, err := execQuery(prov, "shop", `DELETE FROM companies WHERE id = 1`)
	assertFKViolation(t, err, sqle.ErrForeignKeyParentViolation, mysql.ERRowIsReferenced2)

	_, rows := mustExecQuery(t, prov, "shop", `SELECT id FROM companies`)
	assert.Len(t, rows, 1, "the restricted parent must not have been deleted")

	// Removing the child first makes the same DELETE succeed.
	mustExecQuery(t, prov, "shop", `DELETE FROM users WHERE id = 1`)
	mustExecQuery(t, prov, "shop", `DELETE FROM companies WHERE id = 1`)
	_, rows = mustExecQuery(t, prov, "shop", `SELECT id FROM companies`)
	assert.Empty(t, rows)
}

// TestForeignKey_CascadeDeleteAndUpdate asserts ON DELETE CASCADE and ON
// UPDATE CASCADE actually propagate. This is the strongest evidence that the
// engine, not qrdb, is doing the enforcing: nothing in this package moves a
// child row when its parent changes.
func TestForeignKey_CascadeDeleteAndUpdate(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	mustExecQuery(t, prov, "shop", `CREATE TABLE companies (id BIGINT PRIMARY KEY, name VARCHAR(64))`)
	mustExecQuery(t, prov, "shop", `CREATE TABLE users (
		id BIGINT PRIMARY KEY,
		company_id BIGINT,
		CONSTRAINT fk_users_company FOREIGN KEY (company_id) REFERENCES companies(id)
			ON DELETE CASCADE ON UPDATE CASCADE
	)`)
	mustExecQuery(t, prov, "shop", `INSERT INTO companies VALUES (1, 'acme'), (2, 'globex')`)
	mustExecQuery(t, prov, "shop", `INSERT INTO users VALUES (1, 1), (2, 2), (3, 1)`)

	mustExecQuery(t, prov, "shop", `UPDATE companies SET id = 10 WHERE id = 1`)
	_, rows := mustExecQuery(t, prov, "shop", `SELECT id, company_id FROM users ORDER BY id`)
	require.Len(t, rows, 3)
	assert.Equal(t, int64(10), rows[0][1], "child must follow the parent key")
	assert.Equal(t, int64(2), rows[1][1])
	assert.Equal(t, int64(10), rows[2][1])

	mustExecQuery(t, prov, "shop", `DELETE FROM companies WHERE id = 10`)
	_, rows = mustExecQuery(t, prov, "shop", `SELECT id FROM users ORDER BY id`)
	require.Len(t, rows, 1, "both children of the deleted parent must be gone")
	assert.Equal(t, int64(2), rows[0][0])
}

// TestForeignKey_SetNullOnDelete asserts ON DELETE SET NULL nulls the child
// key rather than deleting the child.
func TestForeignKey_SetNullOnDelete(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	mustExecQuery(t, prov, "shop", `CREATE TABLE companies (id BIGINT PRIMARY KEY)`)
	mustExecQuery(t, prov, "shop", `CREATE TABLE users (
		id BIGINT PRIMARY KEY,
		company_id BIGINT,
		CONSTRAINT fk_users_company FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE SET NULL
	)`)
	mustExecQuery(t, prov, "shop", `INSERT INTO companies VALUES (1)`)
	mustExecQuery(t, prov, "shop", `INSERT INTO users VALUES (1, 1)`)

	mustExecQuery(t, prov, "shop", `DELETE FROM companies WHERE id = 1`)
	_, rows := mustExecQuery(t, prov, "shop", `SELECT id, company_id FROM users`)
	require.Len(t, rows, 1)
	assert.Nil(t, rows[0][1], "child key must have been nulled, not deleted")
}

// TestForeignKey_SelfReferential asserts a table pointing at itself is
// enforced, including the row that points at nothing.
func TestForeignKey_SelfReferential(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	mustExecQuery(t, prov, "shop", `CREATE TABLE nodes (
		id BIGINT PRIMARY KEY,
		parent_id BIGINT,
		CONSTRAINT fk_nodes_parent FOREIGN KEY (parent_id) REFERENCES nodes(id)
	)`)

	mustExecQuery(t, prov, "shop", `INSERT INTO nodes VALUES (1, NULL)`)
	mustExecQuery(t, prov, "shop", `INSERT INTO nodes VALUES (2, 1)`)

	_, _, err := execQuery(prov, "shop", `INSERT INTO nodes VALUES (3, 99)`)
	assertFKViolation(t, err, sqle.ErrForeignKeyChildViolation, mysql.ErNoReferencedRow2)

	_, _, err = execQuery(prov, "shop", `DELETE FROM nodes WHERE id = 1`)
	assertFKViolation(t, err, sqle.ErrForeignKeyParentViolation, mysql.ERRowIsReferenced2)
}

// TestForeignKey_AddedOverExistingRows covers ALTER TABLE ADD CONSTRAINT on a
// table that already holds data: rows that satisfy the constraint must be
// accepted (and their backing index populated, so the constraint is
// enforceable afterwards), and rows that violate it must refuse the ALTER.
func TestForeignKey_AddedOverExistingRows(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	mustExecQuery(t, prov, "shop", `CREATE TABLE companies (id BIGINT PRIMARY KEY)`)
	mustExecQuery(t, prov, "shop", `CREATE TABLE users (id BIGINT PRIMARY KEY, company_id BIGINT)`)
	mustExecQuery(t, prov, "shop", `CREATE TABLE orders (id BIGINT PRIMARY KEY, company_id BIGINT)`)
	mustExecQuery(t, prov, "shop", `INSERT INTO companies VALUES (1)`)
	mustExecQuery(t, prov, "shop", `INSERT INTO users VALUES (1, 1), (2, 1)`)
	mustExecQuery(t, prov, "shop", `INSERT INTO orders VALUES (1, 404)`)

	mustExecQuery(t, prov, "shop",
		`ALTER TABLE users ADD CONSTRAINT fk_u FOREIGN KEY (company_id) REFERENCES companies(id)`)

	// Enforceable afterwards, which requires the backfilled index.
	_, _, err := execQuery(prov, "shop", `DELETE FROM companies WHERE id = 1`)
	assertFKViolation(t, err, sqle.ErrForeignKeyParentViolation, mysql.ERRowIsReferenced2)

	_, _, err = execQuery(prov, "shop",
		`ALTER TABLE orders ADD CONSTRAINT fk_o FOREIGN KEY (company_id) REFERENCES companies(id)`)
	assertFKViolation(t, err, sqle.ErrForeignKeyChildViolation, mysql.ErNoReferencedRow2)

	declared, err := fkTable(t, h, "shop", "orders").GetDeclaredForeignKeys(sqle.NewContext(context.Background()))
	require.NoError(t, err)
	assert.Empty(t, declared, "a constraint the engine refused must not be left in the catalog")
}

// TestForeignKey_DropTableGuarded asserts a table other tables still point at
// cannot be dropped, and can be once the constraint is gone.
func TestForeignKey_DropTableGuarded(t *testing.T) {
	h := newDDLHarness()
	prov := fkParentChild(t, h)

	_, _, err := execQuery(prov, "shop", `DROP TABLE companies`)
	require.Error(t, err)
	assert.True(t, sqle.ErrForeignKeyDropTable.Is(err), "err = %v", err)

	mustExecQuery(t, prov, "shop", `ALTER TABLE users DROP FOREIGN KEY fk_users_company`)
	mustExecQuery(t, prov, "shop", `DROP TABLE companies`)
}

// TestForeignKey_DropTableRemovesItsConstraints asserts dropping the
// DECLARING table takes its constraints with it, so the parent stops
// reporting a reference to a table that no longer exists.
func TestForeignKey_DropTableRemovesItsConstraints(t *testing.T) {
	h := newDDLHarness()
	prov := fkParentChild(t, h)

	mustExecQuery(t, prov, "shop", `DROP TABLE users`)

	refs, err := fkTable(t, h, "shop", "companies").GetReferencedForeignKeys(sqle.NewContext(context.Background()))
	require.NoError(t, err)
	assert.Empty(t, refs)
}

// ---------------------------------------------------------------------------
// Read-only surface
// ---------------------------------------------------------------------------

// TestForeignKey_ReadOnlySurfaceRefusesFKDDL is a security guard, not a
// feature test. The ad-hoc SQL query surface served over REST/gRPC/QUIC/Unix/
// MCP is authorized as a READ operation, and readOnlyTable/readOnlyDatabase
// keep DML and DDL away from it structurally, through the Go type system,
// rather than with a runtime permission check. Adding sql.ForeignKeyTable to
// RelationalTable must not have punched a hole in that: FOREIGN KEY DDL is
// DDL.
func TestForeignKey_ReadOnlySurfaceRefusesFKDDL(t *testing.T) {
	h := newDDLHarness()
	fkParentChild(t, h)

	users := fkTable(t, h, "shop", "users")

	// The concrete table is a ForeignKeyTable; the read-only wrapper is not,
	// so go-mysql-server's own type assertion against it fails and every FK
	// DDL statement is refused with ErrNoForeignKeySupport.
	_, ok := sqle.Table(users).(sqle.ForeignKeyTable)
	require.True(t, ok, "the concrete table must implement ForeignKeyTable")

	_, ok = newReadOnlyTable(users).(sqle.ForeignKeyTable)
	assert.False(t, ok, "readOnlyTable must not expose ForeignKeyTable")

	roDB := newReadOnlyDatabase(h.newDDLDatabase("shop"))
	_, ok = roDB.(sqle.TableCreator)
	assert.False(t, ok, "readOnlyDatabase must not expose TableCreator")
	_, ok = roDB.(sqle.TableDropper)
	assert.False(t, ok, "readOnlyDatabase must not expose TableDropper")
}

// ---------------------------------------------------------------------------
// Referential action mapping
// ---------------------------------------------------------------------------

// TestForeignKeyAction_RoundTrip pins the persisted-text mapping in both
// directions for every action go-mysql-server can act on, plus the empty
// string ForeignKeyDef documents as "the default".
func TestForeignKeyAction_RoundTrip(t *testing.T) {
	actions := []sqle.ForeignKeyReferentialAction{
		sqle.ForeignKeyReferentialAction_DefaultAction,
		sqle.ForeignKeyReferentialAction_Restrict,
		sqle.ForeignKeyReferentialAction_Cascade,
		sqle.ForeignKeyReferentialAction_NoAction,
		sqle.ForeignKeyReferentialAction_SetNull,
		sqle.ForeignKeyReferentialAction_SetDefault,
	}
	for _, want := range actions {
		got, err := foreignKeyAction(string(want))
		require.NoError(t, err, "action %q", want)
		assert.Equal(t, want, got)

		lower, err := foreignKeyAction(strings.ToLower(string(want)))
		require.NoError(t, err)
		assert.Equal(t, want, lower, "action matching must be case insensitive")
	}

	got, err := foreignKeyAction("")
	require.NoError(t, err)
	assert.Equal(t, sqle.ForeignKeyReferentialAction_DefaultAction, got)

	_, err = foreignKeyAction("SET FIRE")
	assert.IsType(t, &ForeignKeyActionUnsupportedError{}, err)
}

// TestForeignKeyConstraintOf_RejectsUnknownStoredAction asserts a catalog
// record carrying an action this build cannot act on is reported rather than
// downgraded to the default -- downgrading would quietly turn a stored
// CASCADE into a RESTRICT.
func TestForeignKeyConstraintOf_RejectsUnknownStoredAction(t *testing.T) {
	_, err := foreignKeyConstraintOf("shop", "users", service.ForeignKeyDef{
		Name: "fk", ParentTable: "companies",
		Columns: []string{"company_id"}, ParentColumns: []string{"id"},
		OnDelete: "MELT",
	})
	assert.IsType(t, &ForeignKeyActionUnsupportedError{}, err)

	_, err = foreignKeyConstraintOf("shop", "users", service.ForeignKeyDef{
		Name: "fk", ParentTable: "companies",
		Columns: []string{"company_id"}, ParentColumns: []string{"id"},
		OnUpdate: "MELT",
	})
	assert.IsType(t, &ForeignKeyActionUnsupportedError{}, err)
}

// ---------------------------------------------------------------------------
// Error propagation
// ---------------------------------------------------------------------------

// foreignKeyErrorService injects failures into the individual TableServicer
// calls the ForeignKeyTable surface makes, so each one's error path can be
// exercised without a live failure mode.
type foreignKeyErrorService struct {
	service.TableServicer
	getTableErr    error
	referencingErr error
	addErr         error
	dropErr        error
	updateErr      error
	createIndexErr error
}

func (e *foreignKeyErrorService) GetTable(ctx context.Context, database, name string) (*service.TableDef, error) {
	if e.getTableErr != nil {
		return nil, e.getTableErr
	}
	return e.TableServicer.GetTable(ctx, database, name)
}

func (e *foreignKeyErrorService) ReferencingForeignKeys(ctx context.Context, database, parent string) ([]service.TableForeignKey, error) {
	if e.referencingErr != nil {
		return nil, e.referencingErr
	}
	return e.TableServicer.ReferencingForeignKeys(ctx, database, parent)
}

func (e *foreignKeyErrorService) AddForeignKey(ctx context.Context, req *service.CreateTableForeignKeyRequest) error {
	if e.addErr != nil {
		return e.addErr
	}
	return e.TableServicer.AddForeignKey(ctx, req)
}

func (e *foreignKeyErrorService) DropForeignKey(ctx context.Context, database, table, name string) error {
	if e.dropErr != nil {
		return e.dropErr
	}
	return e.TableServicer.DropForeignKey(ctx, database, table, name)
}

func (e *foreignKeyErrorService) UpdateForeignKey(ctx context.Context, req *service.UpdateTableForeignKeyRequest) error {
	if e.updateErr != nil {
		return e.updateErr
	}
	return e.TableServicer.UpdateForeignKey(ctx, req)
}

func (e *foreignKeyErrorService) CreateIndex(ctx context.Context, req *service.CreateTableIndexRequest) error {
	if e.createIndexErr != nil {
		return e.createIndexErr
	}
	return e.TableServicer.CreateIndex(ctx, req)
}

// fkTableWith rebuilds users as a RelationalTable backed by svc, so an
// injected catalog failure is observed through the real table.
func fkTableWith(t *testing.T, h *ddlHarness, svc service.TableServicer) *RelationalTable {
	t.Helper()
	def, err := h.tableSvc.GetTable(context.Background(), "shop", "users")
	require.NoError(t, err)
	tbl, err := NewRelationalTable(def, h.kvSvc, h.mvccSvc,
		WithTableService(svc),
		WithTransactionService(h.txnSvc),
		WithSequenceService(h.seqSvc),
	)
	require.NoError(t, err)
	return tbl
}

// TestForeignKey_CatalogErrorsPropagate asserts a catalog failure surfaces
// rather than being read as "this table declares no constraints", which would
// turn a transport failure into an unenforced constraint.
func TestForeignKey_CatalogErrorsPropagate(t *testing.T) {
	h := newDDLHarness()
	fkParentChild(t, h)
	ctx := sqle.NewContext(context.Background())

	boom := errors.New("catalog unavailable")

	tbl := fkTableWith(t, h, &foreignKeyErrorService{TableServicer: h.tableSvc, getTableErr: boom})
	_, err := tbl.GetDeclaredForeignKeys(ctx)
	assert.ErrorIs(t, err, boom)

	tbl = fkTableWith(t, h, &foreignKeyErrorService{TableServicer: h.tableSvc, referencingErr: boom})
	_, err = tbl.GetReferencedForeignKeys(ctx)
	assert.ErrorIs(t, err, boom)

	valid := sqle.ForeignKeyConstraint{
		Name: "fk_new", Database: "shop", Table: "users",
		ParentDatabase: "shop", ParentTable: "companies",
		Columns: []string{"company_id"}, ParentColumns: []string{"id"},
	}

	tbl = fkTableWith(t, h, &foreignKeyErrorService{TableServicer: h.tableSvc, addErr: boom})
	assert.ErrorIs(t, tbl.AddForeignKey(ctx, valid), boom)

	tbl = fkTableWith(t, h, &foreignKeyErrorService{TableServicer: h.tableSvc, dropErr: boom})
	assert.ErrorIs(t, tbl.DropForeignKey(ctx, "fk_users_company", "users", ""), boom)

	tbl = fkTableWith(t, h, &foreignKeyErrorService{TableServicer: h.tableSvc, updateErr: boom})
	assert.ErrorIs(t, tbl.UpdateForeignKey(ctx, "fk_users_company", valid), boom)

	tbl = fkTableWith(t, h, &foreignKeyErrorService{TableServicer: h.tableSvc, createIndexErr: boom})
	assert.ErrorIs(t, tbl.CreateIndexForForeignKey(ctx, sqle.IndexDef{
		Name: "idx", Columns: []sqle.IndexColumn{{Name: "company_id"}},
	}), boom)
}

// TestForeignKey_UnrepresentableStoredConstraintIsReported asserts a stored
// constraint this build cannot translate is reported by BOTH read paths
// rather than being skipped. Skipping it would hand the engine a shorter
// constraint list than the catalog actually holds -- silently dropping a
// constraint at read time is the same failure as never storing it.
func TestForeignKey_UnrepresentableStoredConstraintIsReported(t *testing.T) {
	h := newDDLHarness()
	fkParentChild(t, h)
	ctx := context.Background()

	// Write an action no build can act on, straight into the catalog,
	// bypassing the validation that would have refused it.
	corrupt := service.ForeignKeyDef{
		Name: "fk_corrupt", Columns: []string{"company_id"},
		ParentTable: "companies", ParentColumns: []string{"id"},
		OnDelete: "DISINTEGRATE",
	}
	require.NoError(t, h.tableSvc.UpdateForeignKey(ctx, &service.UpdateTableForeignKeyRequest{
		Database: "shop", Table: "users", Name: "fk_users_company", ForeignKey: corrupt,
	}))

	sqlCtx := sqle.NewContext(ctx)
	_, err := fkTable(t, h, "shop", "users").GetDeclaredForeignKeys(sqlCtx)
	assert.IsType(t, &ForeignKeyActionUnsupportedError{}, err)

	_, err = fkTable(t, h, "shop", "companies").GetReferencedForeignKeys(sqlCtx)
	assert.IsType(t, &ForeignKeyActionUnsupportedError{}, err)
}

// TestForeignKey_CreateIndexForForeignKeyRejectsUnusableIndex asserts an
// index definition that names no columns, or a column the table does not
// have, is refused with nothing written. The catalog's own validation
// catches both before the backfill runs, which is why the errors below come
// from pkg/service rather than from this package.
func TestForeignKey_CreateIndexForForeignKeyRejectsUnusableIndex(t *testing.T) {
	h := newDDLHarness()
	fkParentChild(t, h)
	ctx := sqle.NewContext(context.Background())
	users := fkTable(t, h, "shop", "users")

	assert.IsType(t, &IndexNoColumnsError{},
		users.CreateIndexForForeignKey(ctx, sqle.IndexDef{Name: "idx_empty"}))

	assert.IsType(t, &service.TableIndexColumnNotFoundError{},
		users.CreateIndexForForeignKey(ctx, sqle.IndexDef{
			Name: "idx_ghost", Columns: []sqle.IndexColumn{{Name: "nope"}},
		}))

	def, err := h.tableSvc.GetTable(context.Background(), "shop", "users")
	require.NoError(t, err)
	assert.Nil(t, findTableIndex(def, "idx_empty"))
	assert.Nil(t, findTableIndex(def, "idx_ghost"))
}

// TestForeignKey_BackfillIndexRefusesUnbuildableDefinition covers the
// backfill's own guard directly. It is unreachable through
// CreateIndexForForeignKey today because the catalog rejects the same shapes
// first (see the previous test), but it is the guard that stands between a
// bad definition and an index that exists, is empty, and therefore makes the
// constraint reject every child row -- a failure that is silent and
// inverted, so it is worth pinning independently of what happens to run
// first.
func TestForeignKey_BackfillIndexRefusesUnbuildableDefinition(t *testing.T) {
	h := newDDLHarness()
	fkParentChild(t, h)
	ctx := sqle.NewContext(context.Background())
	users := fkTable(t, h, "shop", "users")

	assert.IsType(t, &IndexNoColumnsError{},
		users.backfillIndex(ctx, sqle.IndexDef{Name: "idx_empty"}))

	assert.IsType(t, &ColumnNotFoundOnTableError{},
		users.backfillIndex(ctx, sqle.IndexDef{
			Name: "idx_ghost", Columns: []sqle.IndexColumn{{Name: "nope"}},
		}))
}
