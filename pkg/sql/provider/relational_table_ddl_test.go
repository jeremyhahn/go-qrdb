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
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/catalog"
)

// seedRow encodes row against def's current RowCodec/KeyCodec and writes it
// directly through kvSvc, bypassing DML (a later phase) so DDL tests can
// exercise pre-existing data.
func seedRow(t *testing.T, kvSvc service.KVServicer, database string, def *service.TableDef, row sqle.Row) {
	t.Helper()
	rowCodec, err := catalog.NewRowCodec(def)
	require.NoError(t, err)
	keyCodec, err := catalog.NewKeyCodec(def)
	require.NoError(t, err)

	encodedRow, err := rowCodec.Encode(row)
	require.NoError(t, err)
	encodedKey, err := keyCodec.Encode(row)
	require.NoError(t, err)

	require.NoError(t, kvSvc.Put(context.Background(), service.TableDataPrefix(database, def.TableID, 0)+encodedKey, encodedRow))
}

// ---------------------------------------------------------------------------
// ADD COLUMN: metadata-only
// ---------------------------------------------------------------------------

func TestRelationalTable_AddColumn_MetadataOnly_OldRowsDecodeViaDefaults(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")

	mustExecQuery(t, prov, "shop", "CREATE TABLE users (id BIGINT UNSIGNED PRIMARY KEY, email VARCHAR(255))")
	def, err := h.tableSvc.GetTable(context.Background(), "shop", "users")
	require.NoError(t, err)
	seedRow(t, h.kvSvc, "shop", def, sqle.Row{uint64(1), "alice@example.com"})

	mustExecQuery(t, prov, "shop", "ALTER TABLE users ADD COLUMN active TINYINT(1) NOT NULL DEFAULT 1")

	after, err := h.tableSvc.GetTable(context.Background(), "shop", "users")
	require.NoError(t, err)
	require.Len(t, after.Columns, 3)

	// The pre-existing row -- never rewritten -- decodes with the new
	// column filled from its default.
	_, rows := mustExecQuery(t, prov, "shop", "SELECT id, email, active FROM users")
	require.Len(t, rows, 1)
	assert.Equal(t, uint64(1), rows[0][0])
	assert.Equal(t, "alice@example.com", rows[0][1])
	assert.Equal(t, int8(1), rows[0][2])

	// The row's raw bytes were never touched: still exactly 2 encoded
	// columns.
	raw, err := h.kvSvc.Scan(context.Background(), service.TableDataPrefix("shop", def.TableID, 0))
	require.NoError(t, err)
	require.Len(t, raw, 1)
	for _, data := range raw {
		atLeast, verr := catalog.RowSchemaVersionAtLeast("users", data, after.SchemaVersion)
		require.NoError(t, verr)
		assert.False(t, atLeast, "an ADD COLUMN must not re-encode the row at the new schema version")
	}
}

func TestRelationalTable_AddColumn_RejectsExplicitOrder(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	mustExecQuery(t, prov, "shop", "CREATE TABLE users (id BIGINT UNSIGNED PRIMARY KEY, email VARCHAR(255))")

	_, _, err := execQuery(prov, "shop", "ALTER TABLE users ADD COLUMN active TINYINT(1) FIRST")
	require.Error(t, err)
	var e *ColumnOrderUnsupportedError
	require.ErrorAs(t, err, &e)
}

func TestRelationalTable_AddColumn_NoTableService(t *testing.T) {
	def := testUsersDef()
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)

	err = tbl.AddColumn(newTestContext(), &sqle.Column{Name: "x", Type: types.Int64}, nil)
	var e *TableServiceUnavailableError
	require.ErrorAs(t, err, &e)
}

// ---------------------------------------------------------------------------
// DROP COLUMN: rewrite
// ---------------------------------------------------------------------------

func TestRelationalTable_DropColumn_Rewrites(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")

	mustExecQuery(t, prov, "shop", "CREATE TABLE users (id BIGINT UNSIGNED PRIMARY KEY, email VARCHAR(255), legacy VARCHAR(50))")
	before, err := h.tableSvc.GetTable(context.Background(), "shop", "users")
	require.NoError(t, err)
	seedRow(t, h.kvSvc, "shop", before, sqle.Row{uint64(1), "alice@example.com", "old"})
	seedRow(t, h.kvSvc, "shop", before, sqle.Row{uint64(2), "bob@example.com", "old2"})

	mustExecQuery(t, prov, "shop", "ALTER TABLE users DROP COLUMN legacy")

	after, err := h.tableSvc.GetTable(context.Background(), "shop", "users")
	require.NoError(t, err)
	require.Len(t, after.Columns, 2)
	assert.Greater(t, after.SchemaVersion, before.SchemaVersion)

	_, rows := mustExecQuery(t, prov, "shop", "SELECT id, email FROM users ORDER BY id")
	require.Len(t, rows, 2)
	assert.Equal(t, uint64(1), rows[0][0])
	assert.Equal(t, "alice@example.com", rows[0][1])
	assert.Equal(t, uint64(2), rows[1][0])
	assert.Equal(t, "bob@example.com", rows[1][1])

	// Every row is now encoded at the new schema version.
	raw, err := h.kvSvc.Scan(context.Background(), service.TableDataPrefix("shop", before.TableID, 0))
	require.NoError(t, err)
	require.Len(t, raw, 2)
	for _, data := range raw {
		atLeast, verr := catalog.RowSchemaVersionAtLeast("users", data, after.SchemaVersion)
		require.NoError(t, verr)
		assert.True(t, atLeast)
	}
}

func TestRelationalTable_DropColumn_RemovedFromIndexCatalog(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")

	mustExecQuery(t, prov, "shop", "CREATE TABLE users (id BIGINT UNSIGNED PRIMARY KEY, email VARCHAR(255))")
	require.NoError(t, h.tableSvc.CreateIndex(context.Background(), &service.CreateTableIndexRequest{
		Database: "shop", Table: "users",
		Index: service.TableIndex{Name: "idx_email", Columns: []service.IndexCol{{Name: "email"}}},
	}))

	mustExecQuery(t, prov, "shop", "ALTER TABLE users DROP COLUMN email")

	after, err := h.tableSvc.GetTable(context.Background(), "shop", "users")
	require.NoError(t, err)
	assert.Empty(t, after.Indexes, "an index referencing only the dropped column must be dropped with it")
}

// ---------------------------------------------------------------------------
// MODIFY COLUMN: rewrite
// ---------------------------------------------------------------------------

func TestRelationalTable_ModifyColumn_Rewrites(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")

	mustExecQuery(t, prov, "shop", "CREATE TABLE users (id BIGINT UNSIGNED PRIMARY KEY, age TINYINT)")
	before, err := h.tableSvc.GetTable(context.Background(), "shop", "users")
	require.NoError(t, err)
	seedRow(t, h.kvSvc, "shop", before, sqle.Row{uint64(1), int8(30)})

	mustExecQuery(t, prov, "shop", "ALTER TABLE users MODIFY COLUMN age BIGINT")

	after, err := h.tableSvc.GetTable(context.Background(), "shop", "users")
	require.NoError(t, err)
	assert.Greater(t, after.SchemaVersion, before.SchemaVersion)

	_, rows := mustExecQuery(t, prov, "shop", "SELECT id, age FROM users")
	require.Len(t, rows, 1)
	assert.Equal(t, int64(30), rows[0][1], "widened value must survive the rewrite")
}

func TestRelationalTable_ModifyColumn_RejectsPrimaryKeyFamilyChange(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")

	mustExecQuery(t, prov, "shop", "CREATE TABLE users (id BIGINT UNSIGNED PRIMARY KEY)")

	_, _, err := execQuery(prov, "shop", "ALTER TABLE users MODIFY COLUMN id VARCHAR(20)")
	require.Error(t, err)
	var e *PrimaryKeyRewriteUnsupportedError
	require.ErrorAs(t, err, &e)
}

// ---------------------------------------------------------------------------
// Rewrite: resume after interruption
// ---------------------------------------------------------------------------

func TestRelationalTable_Rewrite_ResumesAfterInterruption(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")

	mustExecQuery(t, prov, "shop", "CREATE TABLE users (id BIGINT UNSIGNED PRIMARY KEY, email VARCHAR(255), legacy VARCHAR(50))")
	before, err := h.tableSvc.GetTable(context.Background(), "shop", "users")
	require.NoError(t, err)
	seedRow(t, h.kvSvc, "shop", before, sqle.Row{uint64(1), "alice@example.com", "old"})
	seedRow(t, h.kvSvc, "shop", before, sqle.Row{uint64(2), "bob@example.com", "old2"})

	// Simulate the opening half of a DROP COLUMN rewrite completing (the
	// journal is written) but the row migration + closing CAS never
	// running -- a crash between the two.
	_, err = h.tableSvc.AlterTable(context.Background(), &service.AlterTableRequest{
		Database: "shop", Name: "users",
		Changes: []service.AlterTableChange{{Action: service.AlterDropColumn, Name: "legacy"}},
	})
	var rewriteRequired *service.TableRewriteRequiredError
	require.ErrorAs(t, err, &rewriteRequired)

	// The table is unservable while the journal exists...
	_, err = h.tableSvc.GetTable(context.Background(), "shop", "users")
	var inProgress *service.TableRewriteInProgressError
	require.ErrorAs(t, err, &inProgress)

	// ...but resolving it through the Database (as any fresh query would)
	// transparently resumes and completes the interrupted rewrite.
	_, rows := mustExecQuery(t, prov, "shop", "SELECT id, email FROM users ORDER BY id")
	require.Len(t, rows, 2)
	assert.Equal(t, "alice@example.com", rows[0][1])
	assert.Equal(t, "bob@example.com", rows[1][1])

	after, err := h.tableSvc.GetTable(context.Background(), "shop", "users")
	require.NoError(t, err)
	require.Len(t, after.Columns, 2)

	journalData, jerr := h.kvSvc.Get(context.Background(), "__sys:tables/shop/users/__ddl")
	assert.Error(t, jerr)
	assert.Nil(t, journalData)
}

func TestRelationalTable_Rewrite_ResumeIsIdempotentOnAlreadyMigratedRows(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")

	mustExecQuery(t, prov, "shop", "CREATE TABLE users (id BIGINT UNSIGNED PRIMARY KEY, legacy VARCHAR(50))")
	before, err := h.tableSvc.GetTable(context.Background(), "shop", "users")
	require.NoError(t, err)
	seedRow(t, h.kvSvc, "shop", before, sqle.Row{uint64(1), "old"})

	mustExecQuery(t, prov, "shop", "ALTER TABLE users DROP COLUMN legacy")

	// Calling resumeRewrite again with nothing in progress must be a no-op,
	// not an error -- covers a second, racing resumer (or a second query
	// arriving after the first already finished).
	tableSvc := h.tableSvc
	err = resumeRewrite(context.Background(), tableSvc, h.kvSvc, h.txnSvc, "shop", "users")
	require.NoError(t, err)

	_, rows := mustExecQuery(t, prov, "shop", "SELECT id FROM users")
	require.Len(t, rows, 1)
}

// ---------------------------------------------------------------------------
// CREATE / DROP / RENAME INDEX, BuildIndex
// ---------------------------------------------------------------------------

func TestRelationalTable_CreateIndex_OnNonEmptyTable_BuildsPhysicalEntries(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")

	mustExecQuery(t, prov, "shop", "CREATE TABLE users (id BIGINT UNSIGNED PRIMARY KEY, email VARCHAR(255))")
	def, err := h.tableSvc.GetTable(context.Background(), "shop", "users")
	require.NoError(t, err)
	seedRow(t, h.kvSvc, "shop", def, sqle.Row{uint64(1), "alice@example.com"})
	seedRow(t, h.kvSvc, "shop", def, sqle.Row{uint64(2), "bob@example.com"})

	mustExecQuery(t, prov, "shop", "CREATE INDEX idx_email ON users (email)")

	after, err := h.tableSvc.GetTable(context.Background(), "shop", "users")
	require.NoError(t, err)
	require.Len(t, after.Indexes, 1)
	assert.Equal(t, "idx_email", after.Indexes[0].Name)

	entries, err := h.kvSvc.Scan(context.Background(), service.TableIndexPrefix("shop", def.TableID, 0, incarnationOf(after, "idx_email")))
	require.NoError(t, err)
	assert.Len(t, entries, 2, "BuildIndex must populate one physical entry per existing row")

	// Lookups keep returning every row (via the full-scan fallback, still
	// correct regardless of whether the physical index is consulted).
	_, rows := mustExecQuery(t, prov, "shop", "SELECT id FROM users WHERE email = 'alice@example.com'")
	require.Len(t, rows, 1)
	assert.Equal(t, uint64(1), rows[0][0])
}

func TestRelationalTable_DropIndex_RemovesCatalogAndPhysicalEntries(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")

	mustExecQuery(t, prov, "shop", "CREATE TABLE users (id BIGINT UNSIGNED PRIMARY KEY, email VARCHAR(255))")
	def, err := h.tableSvc.GetTable(context.Background(), "shop", "users")
	require.NoError(t, err)
	seedRow(t, h.kvSvc, "shop", def, sqle.Row{uint64(1), "alice@example.com"})

	mustExecQuery(t, prov, "shop", "CREATE INDEX idx_email ON users (email)")
	def, err = h.tableSvc.GetTable(context.Background(), "shop", "users")
	require.NoError(t, err)
	prefix := service.TableIndexPrefix("shop", def.TableID, 0, incarnationOf(def, "idx_email"))
	entries, err := h.kvSvc.Scan(context.Background(), prefix)
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	mustExecQuery(t, prov, "shop", "ALTER TABLE users DROP INDEX idx_email")

	after, err := h.tableSvc.GetTable(context.Background(), "shop", "users")
	require.NoError(t, err)
	assert.Empty(t, after.Indexes)

	// Not deleted inline: a statement that resolved the index may still be
	// reading it. The reclaim job the drop published deletes the entries
	// once that can no longer be true.
	entries, err = h.kvSvc.Scan(context.Background(), prefix)
	require.NoError(t, err)
	assert.NotEmpty(t, entries)

	sweepPastQuiescence(t, h)
	entries, err = h.kvSvc.Scan(context.Background(), prefix)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

// TestRelationalTable_RenameIndex_KeepsPhysicalEntries proves RENAME INDEX
// moves nothing: the entries stay under the index's incarnation, which the
// renamed index keeps, and no reclaim job is published.
func TestRelationalTable_RenameIndex_KeepsPhysicalEntries(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	mustExecQuery(t, prov, "shop", "CREATE TABLE users (id BIGINT UNSIGNED PRIMARY KEY, email VARCHAR(255))")
	mustExecQuery(t, prov, "shop", "CREATE INDEX idx_email ON users (email)")
	mustExecQuery(t, prov, "shop", "INSERT INTO users VALUES (1, 'alice@example.com')")
	before, err := h.tableSvc.GetTable(context.Background(), "shop", "users")
	require.NoError(t, err)

	mustExecQuery(t, prov, "shop", "ALTER TABLE users RENAME INDEX idx_email TO idx_email_v2")

	renamed, err := h.tableSvc.GetTable(context.Background(), "shop", "users")
	require.NoError(t, err)
	require.Len(t, renamed.Indexes, 1)
	assert.Equal(t, "idx_email_v2", renamed.Indexes[0].Name)
	assert.Equal(t, incarnationOf(before, "idx_email"), renamed.Indexes[0].Incarnation)

	entries, err := h.kvSvc.Scan(context.Background(), service.TableIndexPrefix("shop", renamed.TableID, 0, renamed.Indexes[0].Incarnation))
	require.NoError(t, err)
	assert.Len(t, entries, 1)
	jobs, err := h.kvSvc.Scan(context.Background(), "__reclaim:job/")
	require.NoError(t, err)
	assert.Empty(t, jobs)

	_, rows := mustExecQuery(t, prov, "shop", "SELECT id FROM users WHERE email = 'alice@example.com'")
	require.Len(t, rows, 1)
}

func TestRelationalTable_RenameIndex_NotFound(t *testing.T) {
	h := newDDLHarness()
	def, err := h.tableSvc.CreateTable(context.Background(), &service.CreateTableRequest{
		Database: "shop", Name: "users",
		Columns:    []service.ColumnDef{{Name: "id", Type: "BIGINT UNSIGNED"}},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
	})
	require.NoError(t, err)
	tbl, err := NewRelationalTable(def, h.kvSvc, h.mvccSvc, WithTableService(h.tableSvc))
	require.NoError(t, err)

	err = tbl.RenameIndex(newTestContext(), "missing", "renamed")
	var e *service.TableIndexNotFoundError
	require.ErrorAs(t, err, &e)
}

func TestRelationalTable_BuildIndex_UnknownColumn(t *testing.T) {
	def := testUsersDef()
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil, WithTableService(&fakeTableService{}))
	require.NoError(t, err)

	_, err = tbl.BuildIndex(newTestContext(), sqle.IndexDef{Name: "idx_bad", Columns: []sqle.IndexColumn{{Name: "nonexistent"}}})
	var e *ColumnNotFoundOnTableError
	require.ErrorAs(t, err, &e)
}

func TestRelationalTable_BuildIndex_NoColumns(t *testing.T) {
	def := testUsersDef()
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil, WithTableService(&fakeTableService{}))
	require.NoError(t, err)

	_, err = tbl.BuildIndex(newTestContext(), sqle.IndexDef{Name: "idx_bad"})
	var e *IndexNoColumnsError
	require.ErrorAs(t, err, &e)
}

func TestRelationalTable_ShouldBuildIndex_AlwaysTrue(t *testing.T) {
	def := testUsersDef()
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)

	should, err := tbl.ShouldBuildIndex(newTestContext(), sqle.IndexDef{})
	require.NoError(t, err)
	assert.True(t, should)
}

// ---------------------------------------------------------------------------
// AlterableTable: DropColumn/ModifyColumn are unreachable through
// RewritableTable
// ---------------------------------------------------------------------------

func TestRelationalTable_DropColumn_DirectCallReturnsRewriteRequired(t *testing.T) {
	def := testUsersDef()
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)

	err = tbl.DropColumn(newTestContext(), "email")
	var e *RewriteRequiredError
	require.ErrorAs(t, err, &e)
}

func TestRelationalTable_ModifyColumn_DirectCallReturnsRewriteRequired(t *testing.T) {
	def := testUsersDef()
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)

	err = tbl.ModifyColumn(newTestContext(), "email", &sqle.Column{Name: "email", Type: types.Int64}, nil)
	var e *RewriteRequiredError
	require.ErrorAs(t, err, &e)
}

// Updater's real DML behavior is covered in relational_writer_test.go
// (Phase 6); AlterableTable's Updater method itself is exercised there via
// the same relationalWriter every RelationalTable DML method returns.

// ---------------------------------------------------------------------------
// CommentedTable
// ---------------------------------------------------------------------------

func TestRelationalTable_Comment(t *testing.T) {
	def := testUsersDef()
	def.Comment = "user accounts"
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)
	assert.Equal(t, "user accounts", tbl.Comment())
}

// ---------------------------------------------------------------------------
// Interface assertions
// ---------------------------------------------------------------------------

func TestRelationalTable_ImplementsDDLInterfaces(t *testing.T) {
	def := testUsersDef()
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)

	var st sqle.Table = tbl
	_, ok := st.(sqle.AlterableTable)
	assert.True(t, ok)
	_, ok = st.(sqle.IndexAlterableTable)
	assert.True(t, ok)
	_, ok = st.(sqle.IndexBuildingTable)
	assert.True(t, ok)
	_, ok = st.(sqle.RewritableTable)
	assert.True(t, ok)
	_, ok = st.(sqle.CommentedTable)
	assert.True(t, ok)
}

// ---------------------------------------------------------------------------
// ModifyColumn as RENAME COLUMN
// ---------------------------------------------------------------------------

// TestRelationalTable_ModifyColumn_RenameOnlyIsMetadataOnly covers the one
// shape go-mysql-server routes to ModifyColumn directly: RENAME COLUMN, whose
// executor copies the existing column, swaps the name, and never consults
// ShouldRewriteTable. A pure rename must succeed as a catalog-only change --
// the row codec is positional, so no stored row needs touching -- and the
// rename must follow through into key structures that name the column.
func TestRelationalTable_ModifyColumn_RenameOnlyIsMetadataOnly(t *testing.T) {
	h := newDDLHarness()
	def, err := h.tableSvc.CreateTable(context.Background(), &service.CreateTableRequest{
		Database: "shop", Name: "users",
		Columns: []service.ColumnDef{
			{Name: "id", Type: "BIGINT UNSIGNED"},
			{Name: "email", Type: "VARCHAR(255)", Nullable: true},
		},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
	})
	require.NoError(t, err)

	tbl, err := NewRelationalTable(def, h.kvSvc, h.mvccSvc, WithTableService(h.tableSvc))
	require.NoError(t, err)

	// What buildRenameColumn constructs: the SAME column, new name.
	emailType, terr := catalog.ColumnTypeOf(service.ColumnDef{Name: "email", Type: "VARCHAR(255)", Nullable: true})
	require.NoError(t, terr)
	renamed := &sqle.Column{Name: "email_address", Type: emailType, Nullable: true, Source: "users"}
	require.NoError(t, tbl.ModifyColumn(newTestContext(), "email", renamed, nil))

	got, err := h.tableSvc.GetTable(context.Background(), "shop", "users")
	require.NoError(t, err)
	names := make([]string, len(got.Columns))
	for i, c := range got.Columns {
		names[i] = c.Name
	}
	assert.Equal(t, []string{"id", "email_address"}, names)
}

// TestRelationalTable_ModifyColumn_RenamePrimaryKeyColumnFollowsThrough pins
// that renaming a column the PRIMARY KEY names updates the key's reference as
// well. Without that, the definition's PrimaryKey names a column that no
// longer exists and the key codec refuses to serve the table at all -- a
// rename that bricks the table.
func TestRelationalTable_ModifyColumn_RenamePrimaryKeyColumnFollowsThrough(t *testing.T) {
	h := newDDLHarness()
	def, err := h.tableSvc.CreateTable(context.Background(), &service.CreateTableRequest{
		Database: "shop", Name: "orders",
		Columns: []service.ColumnDef{
			{Name: "id", Type: "BIGINT UNSIGNED"},
			{Name: "note", Type: "VARCHAR(64)", Nullable: true},
		},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
	})
	require.NoError(t, err)

	tbl, err := NewRelationalTable(def, h.kvSvc, h.mvccSvc, WithTableService(h.tableSvc))
	require.NoError(t, err)

	renamed := &sqle.Column{Name: "order_id", Type: types.Uint64, Source: "orders"}
	require.NoError(t, tbl.ModifyColumn(newTestContext(), "id", renamed, nil))

	got, err := h.tableSvc.GetTable(context.Background(), "shop", "orders")
	require.NoError(t, err)
	require.Len(t, got.PrimaryKey, 1)
	assert.Equal(t, "order_id", got.PrimaryKey[0].Name)

	// The table must still be servable: building a RelationalTable over the
	// renamed definition exercises the key codec against the new name.
	_, err = NewRelationalTable(got, h.kvSvc, h.mvccSvc, WithTableService(h.tableSvc))
	require.NoError(t, err)
}

// TestRelationalTable_ModifyColumn_TypeChangeStillRefused pins that anything
// beyond a pure rename keeps the fail-closed posture: a type change reaching
// ModifyColumn directly means something bypassed the rewrite path, and
// applying it as metadata would leave stored rows in a mixed encoding.
func TestRelationalTable_ModifyColumn_TypeChangeStillRefused(t *testing.T) {
	h := newDDLHarness()
	def, err := h.tableSvc.CreateTable(context.Background(), &service.CreateTableRequest{
		Database: "shop", Name: "things",
		Columns: []service.ColumnDef{
			{Name: "id", Type: "BIGINT UNSIGNED"},
			{Name: "email", Type: "VARCHAR(255)", Nullable: true},
		},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
	})
	require.NoError(t, err)

	tbl, err := NewRelationalTable(def, h.kvSvc, h.mvccSvc, WithTableService(h.tableSvc))
	require.NoError(t, err)

	// Same name, different type: NOT a rename.
	err = tbl.ModifyColumn(newTestContext(), "email", &sqle.Column{Name: "email", Type: types.Int64, Source: "things"}, nil)
	var e *RewriteRequiredError
	require.ErrorAs(t, err, &e)
}
