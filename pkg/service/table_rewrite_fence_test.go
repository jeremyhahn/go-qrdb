// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// Catalog changes during a rewrite (go-qrdb#547 B1)
//
// CompleteRewrite writes the journal's Target -- derived from the definition
// AlterTable read -- back over the metadata record. A catalog change landing
// in between would be undone by that write: a dropped index resurrected, its
// incarnation (whose entries a reclaim job is deleting) listed again. So
// every catalog change refuses while a journal exists, up front and in its
// commit's compares, and CompleteRewrite refuses a record that moved anyway.
// ---------------------------------------------------------------------------

// newRewritingTable creates db.p (a parent) and db.t with index idx_email
// and constraint fk_p, then opens a rewrite of db.t.
func newRewritingTable(t *testing.T) (*TableService, *fakeCatalogStore) {
	t.Helper()
	svc, store := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{
		Database: "db", Name: "p", Columns: testColumns(), PrimaryKey: []PKColumn{{Name: "id"}},
	})
	require.NoError(t, err)
	_, err = svc.CreateTable(ctx, &CreateTableRequest{
		Database: "db", Name: "t", Columns: testColumns(), PrimaryKey: []PKColumn{{Name: "id"}},
		Indexes: []TableIndex{{Name: "idx_email", Columns: []IndexCol{{Name: "email"}}}},
		ForeignKeys: []ForeignKeyDef{{
			Name: "fk_p", Columns: []string{"id"}, ParentTable: "p", ParentColumns: []string{"id"},
		}},
	})
	require.NoError(t, err)
	openRewrite(t, svc)
	return svc, store
}

// openRewrite opens a rewrite of db.t by dropping its created_at column.
func openRewrite(t *testing.T, svc *TableService) {
	t.Helper()
	_, err := svc.AlterTable(context.Background(), &AlterTableRequest{
		Database: "db", Name: "t",
		Changes: []AlterTableChange{{Action: AlterDropColumn, Name: "created_at"}},
	})
	var rewrite *TableRewriteRequiredError
	require.ErrorAs(t, err, &rewrite, "precondition: a rewrite journal is in place")
}

// catalogChangesOfT is every catalog change of db.t that must refuse during
// a rewrite, by name.
func catalogChangesOfT(svc *TableService) map[string]func(context.Context) error {
	return map[string]func(context.Context) error{
		"CreateIndex": func(ctx context.Context) error {
			return svc.CreateIndex(ctx, &CreateTableIndexRequest{
				Database: "db", Table: "t",
				Index: TableIndex{Name: "idx_created", Columns: []IndexCol{{Name: "created_at"}}},
			})
		},
		"DropIndex": func(ctx context.Context) error { return svc.DropIndex(ctx, "db", "t", "idx_email") },
		"RenameIndex": func(ctx context.Context) error {
			return svc.RenameIndex(ctx, &RenameTableIndexRequest{Database: "db", Table: "t", From: "idx_email", To: "idx_mail"})
		},
		"AddForeignKey": func(ctx context.Context) error {
			return svc.AddForeignKey(ctx, &CreateTableForeignKeyRequest{
				Database: "db", Table: "t",
				ForeignKey: ForeignKeyDef{Name: "fk_q", Columns: []string{"id"}, ParentTable: "p", ParentColumns: []string{"id"}},
			})
		},
		"DropForeignKey": func(ctx context.Context) error { return svc.DropForeignKey(ctx, "db", "t", "fk_p") },
		"UpdateForeignKey": func(ctx context.Context) error {
			return svc.UpdateForeignKey(ctx, &UpdateTableForeignKeyRequest{
				Database: "db", Table: "t", Name: "fk_p",
				ForeignKey: ForeignKeyDef{Name: "fk_p2", Columns: []string{"id"}, ParentTable: "p", ParentColumns: []string{"id"}},
			})
		},
		"RenameTable": func(ctx context.Context) error {
			return svc.RenameTable(ctx, &RenameTableRequest{Database: "db", From: "t", To: "u"})
		},
	}
}

// TestTableService_CatalogChangesRefuseDuringARewrite: each change refuses
// up front, typed, without proposing anything, and the definition the
// rewrite will write back over is untouched.
func TestTableService_CatalogChangesRefuseDuringARewrite(t *testing.T) {
	svc, store := newRewritingTable(t)
	ctx := tenant.WithTenantID(context.Background(), "acme")
	metaBefore, err := store.GetRevision(ctx, tableMetadataKey("db", "t"))
	require.NoError(t, err)

	for name, change := range catalogChangesOfT(svc) {
		t.Run(name, func(t *testing.T) {
			before := store.executeCallsSnapshot()
			err := change(ctx)
			var inProgress *TableRewriteInProgressError
			require.ErrorAs(t, err, &inProgress)
			assert.Equal(t, before, store.executeCallsSnapshot(), "nothing is proposed")
		})
	}

	metaAfter, err := store.GetRevision(ctx, tableMetadataKey("db", "t"))
	require.NoError(t, err)
	assert.Equal(t, metaBefore.ModRevision, metaAfter.ModRevision)
	assert.Empty(t, store.keysWithPrefix(reclaimJobPrefix), "no reclaim job was published")
}

// TestTableService_CatalogChangesAreFencedOnTheJournal is the commit half: a
// rewrite opening between a change's up-front check and its commit fails the
// commit's journal compare, and the retry refuses up front.
func TestTableService_CatalogChangesAreFencedOnTheJournal(t *testing.T) {
	for _, name := range []string{"CreateIndex", "DropIndex", "RenameIndex", "AddForeignKey", "DropForeignKey", "UpdateForeignKey", "RenameTable"} {
		t.Run(name, func(t *testing.T) {
			svc, store := newTestTableService()
			ctx := tenant.WithTenantID(context.Background(), "acme")
			_, err := svc.CreateTable(ctx, &CreateTableRequest{
				Database: "db", Name: "p", Columns: testColumns(), PrimaryKey: []PKColumn{{Name: "id"}},
			})
			require.NoError(t, err)
			_, err = svc.CreateTable(ctx, &CreateTableRequest{
				Database: "db", Name: "t", Columns: testColumns(), PrimaryKey: []PKColumn{{Name: "id"}},
				Indexes: []TableIndex{{Name: "idx_email", Columns: []IndexCol{{Name: "email"}}}},
				ForeignKeys: []ForeignKeyDef{{
					Name: "fk_p", Columns: []string{"id"}, ParentTable: "p", ParentColumns: []string{"id"},
				}},
			})
			require.NoError(t, err)

			journalKey := tableDDLJournalKey("db", "t")
			journal, err := json.Marshal(&TableDDLJournal{State: DDLStateRewriting})
			require.NoError(t, err)
			store.beforeExecute = func() {
				if !store.has(journalKey) {
					store.put(journalKey, journal)
				}
			}

			err = catalogChangesOfT(svc)[name](ctx)
			var inProgress *TableRewriteInProgressError
			require.ErrorAs(t, err, &inProgress)
			assert.Empty(t, store.keysWithPrefix(reclaimJobPrefix), "the losing commit published no job")
		})
	}
}

// TestTableService_AlterTable_JournalRecordsTheMetadataRevision: the journal
// names the metadata record its Target was derived from.
func TestTableService_AlterTable_JournalRecordsTheMetadataRevision(t *testing.T) {
	svc, store := newTestTableService()
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)
	meta, err := store.GetRevision(ctx, tableMetadataKey("db", "t"))
	require.NoError(t, err)
	openRewrite(t, svc)

	journal, _, err := svc.RewriteJournal(ctx, "db", "t")
	require.NoError(t, err)
	assert.Equal(t, meta.ModRevision, journal.MetadataRevision)
}

// TestTableService_CompleteRewrite_RefusesAMovedDefinition: a metadata
// record that moved after AlterTable read it is refused, typed and without
// a retry loop, and is not overwritten.
func TestTableService_CompleteRewrite_RefusesAMovedDefinition(t *testing.T) {
	svc, store := newRewritingTable(t)
	ctx := context.Background()
	metaKey := tableMetadataKey("db", "t")
	current, err := store.Get(ctx, metaKey)
	require.NoError(t, err)
	store.put(metaKey, current) // same value, new ModRevision

	before := store.executeCallsSnapshot()
	_, err = svc.CompleteRewrite(ctx, "db", "t")
	var stale *TableDefinitionStaleError
	require.ErrorAs(t, err, &stale)
	assert.Equal(t, before, store.executeCallsSnapshot(), "refused before proposing")

	after, err := store.Get(ctx, metaKey)
	require.NoError(t, err)
	assert.Equal(t, current, after)
}

// TestTableService_CompleteRewrite_CommitIsFencedOnTheDefinition: a record
// moving between CompleteRewrite's read and its commit fails the commit's
// compare; the retry sees the move and refuses.
func TestTableService_CompleteRewrite_CommitIsFencedOnTheDefinition(t *testing.T) {
	svc, store := newRewritingTable(t)
	ctx := context.Background()
	metaKey := tableMetadataKey("db", "t")
	current, err := store.Get(ctx, metaKey)
	require.NoError(t, err)
	moved := false
	store.beforeExecute = func() {
		if !moved {
			moved = true
			store.put(metaKey, current)
		}
	}

	_, err = svc.CompleteRewrite(ctx, "db", "t")
	var stale *TableDefinitionStaleError
	require.ErrorAs(t, err, &stale)
	assert.True(t, store.has(tableDDLJournalKey("db", "t")), "the journal is not consumed")
}

// TestTableService_CompleteRewrite_Succeeds is the positive control: with
// nothing moved, the rewrite completes.
func TestTableService_CompleteRewrite_Succeeds(t *testing.T) {
	svc, store := newRewritingTable(t)
	def, err := svc.CompleteRewrite(context.Background(), "db", "t")
	require.NoError(t, err)
	assert.Len(t, def.Columns, len(testColumns())-1)
	assert.False(t, store.has(tableDDLJournalKey("db", "t")))
}

// ---------------------------------------------------------------------------
// DropTable (go-qrdb#547 N1, N3)
// ---------------------------------------------------------------------------

// TestTableService_DropTable_PublishesTheReclaim: the drop deletes nothing
// under the table's data or index keyspace itself; it publishes one job
// naming both, fenced on the definition and generation it read, and deletes
// the catalog entry, generation counter and both sequences.
func TestTableService_DropTable_PublishesTheReclaim(t *testing.T) {
	svc, store := newTestTableService()
	ctx := tenant.WithTenantID(context.Background(), "acme")
	def, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)
	_, err = svc.TruncateTable(ctx, "db", "t", true)
	require.NoError(t, err)
	for _, name := range []string{TableAutoIncrementSequenceName(def.TableID), TableRowIDSequenceName(def.TableID)} {
		store.put(tableSequenceKey("db", name), encodeSequenceCounter(9))
	}
	row := tableDataPrefixFor("db", def.TableID, 1) + "row"
	store.put(row, []byte("v"))
	meta, err := store.GetRevision(ctx, tableMetadataKey("db", "t"))
	require.NoError(t, err)
	gen, err := store.GetRevision(ctx, tableGenerationKey("db", def.TableID))
	require.NoError(t, err)
	jobsBefore := store.keysWithPrefix(reclaimJobPrefix)

	require.NoError(t, svc.DropTable(ctx, &DropTableRequest{Database: "db", Name: "t"}))

	assert.True(t, store.has(row), "rows wait for the sweeper, past quiescence")
	for _, key := range []string{
		tableMetadataKey("db", "t"),
		tableGenerationKey("db", def.TableID),
		tableSequenceKey("db", TableAutoIncrementSequenceName(def.TableID)),
		tableSequenceKey("db", TableRowIDSequenceName(def.TableID)),
	} {
		assert.False(t, store.has(key), "%s is deleted with the definition", key)
	}

	job, entry := newestReclaimJob(t, store, jobsBefore)
	assert.ElementsMatch(t, []string{
		tableDataPrefixFor("db", def.TableID, 1),
		tableIndexGenerationPrefixFor("db", def.TableID, 1),
	}, job.KeyPrefixes)
	assert.Equal(t, []ReclaimFence{
		{Key: tableMetadataKey("db", "t"), ModRevision: meta.ModRevision},
		{Key: tableGenerationKey("db", def.TableID), ModRevision: gen.ModRevision},
	}, entry.Fences)
}

// newestReclaimJob returns the one job (and its index entry) published since
// before.
func newestReclaimJob(t *testing.T, store *fakeCatalogStore, before []string) (ReclaimJob, ReclaimIndexEntry) {
	t.Helper()
	seen := make(map[string]bool, len(before))
	for _, k := range before {
		seen[k] = true
	}
	var fresh []string
	for _, k := range store.keysWithPrefix(reclaimJobPrefix) {
		if !seen[k] {
			fresh = append(fresh, k)
		}
	}
	require.Len(t, fresh, 1)
	data, err := store.Get(context.Background(), fresh[0])
	require.NoError(t, err)
	var job ReclaimJob
	require.NoError(t, json.Unmarshal(data, &job))
	entryData, err := store.Get(context.Background(), reclaimIndexKey(job.ID))
	require.NoError(t, err)
	var entry ReclaimIndexEntry
	require.NoError(t, json.Unmarshal(entryData, &entry))
	return job, entry
}

// TestTableService_DropTable_StaleResolutionIsRefused: a drop fenced on a
// resolution a TRUNCATE or DDL has since moved is refused with the typed,
// retryable error, and nothing is dropped.
func TestTableService_DropTable_StaleResolutionIsRefused(t *testing.T) {
	cases := map[string]struct {
		move  func(t *testing.T, svc *TableService, ctx context.Context)
		check func(t *testing.T, err error)
	}{
		"truncated": {
			move: func(t *testing.T, svc *TableService, ctx context.Context) {
				_, err := svc.TruncateTable(ctx, "db", "t", true)
				require.NoError(t, err)
			},
			check: func(t *testing.T, err error) {
				var stale *TableGenerationStaleError
				require.ErrorAs(t, err, &stale)
				assert.Equal(t, uint64(0), stale.Expected)
				assert.Equal(t, uint64(1), stale.Actual)
			},
		},
		"altered": {
			move: func(t *testing.T, svc *TableService, ctx context.Context) {
				_, err := svc.AlterTable(ctx, &AlterTableRequest{
					Database: "db", Name: "t",
					Changes: []AlterTableChange{{Action: AlterAddColumn, Column: ColumnDef{Name: "note", Type: "VARCHAR(8)"}}},
				})
				require.NoError(t, err)
			},
			check: func(t *testing.T, err error) {
				var stale *TableDefinitionStaleError
				require.ErrorAs(t, err, &stale)
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			svc, store := newTestTableService()
			ctx := tenant.WithTenantID(context.Background(), "acme")
			_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
			require.NoError(t, err)
			resolved, err := svc.GetTable(ctx, "db", "t")
			require.NoError(t, err)
			tc.move(t, svc, ctx)
			jobsBefore := len(store.keysWithPrefix(reclaimJobPrefix))

			err = svc.DropTable(ctx, &DropTableRequest{Database: "db", Name: "t", Resolution: resolved})
			tc.check(t, err)
			assert.True(t, IsTableResolutionStale(err))
			assert.True(t, store.has(tableMetadataKey("db", "t")), "the table is not dropped")
			assert.Len(t, store.keysWithPrefix(reclaimJobPrefix), jobsBefore, "no job is published")
		})
	}
}

// TestTableService_DropTable_CurrentResolutionDrops is the positive control.
func TestTableService_DropTable_CurrentResolutionDrops(t *testing.T) {
	svc, store := newTestTableService()
	ctx := tenant.WithTenantID(context.Background(), "acme")
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)
	_, err = svc.TruncateTable(ctx, "db", "t", true)
	require.NoError(t, err)
	resolved, err := svc.GetTable(ctx, "db", "t")
	require.NoError(t, err)

	require.NoError(t, svc.DropTable(ctx, &DropTableRequest{Database: "db", Name: "t", Resolution: resolved}))
	assert.False(t, store.has(tableMetadataKey("db", "t")))
}

// TestTableService_DropTable_RefusesWithoutAReclaimIndex: a drop orphans a
// keyspace, so like TRUNCATE it refuses without somewhere to publish the
// job, rather than leak the rows.
func TestTableService_DropTable_RefusesWithoutAReclaimIndex(t *testing.T) {
	store := newFakeCatalogStore()
	svc := NewTableService(store, store, store, NewSequenceService(store, store))
	ctx := context.Background()
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)

	err = svc.DropTable(ctx, &DropTableRequest{Database: "db", Name: "t"})
	var unconfigured *ReclaimIndexUnconfiguredError
	require.ErrorAs(t, err, &unconfigured)
	assert.True(t, store.has(tableMetadataKey("db", "t")))
}

// legacyDefinition is a TableDef as v0.20.0 through v0.21.0 stored it:
// Layout absent.
const legacyDefinition = `{"table_id":7,"name":"t","database":"db",` +
	`"columns":[{"name":"id","type":"BIGINT UNSIGNED","nullable":false}],"schema_version":1}`

// TestTableService_DropTable_LegacyTableNamesTheOldPrefixes: dropping a
// legacy table -- the remedy its refusal names -- reclaims the old layout's
// prefixes too.
func TestTableService_DropTable_LegacyTableNamesTheOldPrefixes(t *testing.T) {
	svc, store := newTestTableService()
	ctx := tenant.WithTenantID(context.Background(), "acme")
	store.put(tableMetadataKey("db", "t"), []byte(legacyDefinition))

	require.NoError(t, svc.DropTable(ctx, &DropTableRequest{Database: "db", Name: "t"}))
	job, _ := newestReclaimJob(t, store, nil)
	for _, prefix := range LegacyTableKeyPrefixes("db", 7) {
		assert.Contains(t, job.KeyPrefixes, prefix)
	}
}

// TestTableService_GetTable_LegacyLayoutWinsOverAnInterruptedRewrite (N3): a
// legacy table whose v0.20.x rewrite was interrupted resolves as what it is
// -- a legacy table, whose remedy is DROP -- not as a rewrite that nothing
// can resume; and DROP removes the journal with it.
func TestTableService_GetTable_LegacyLayoutWinsOverAnInterruptedRewrite(t *testing.T) {
	svc, store := newTestTableService()
	ctx := tenant.WithTenantID(context.Background(), "acme")
	store.put(tableMetadataKey("db", "t"), []byte(legacyDefinition))
	store.put(tableDDLJournalKey("db", "t"), []byte(`{"state":"rewriting"}`))

	_, err := svc.GetTable(ctx, "db", "t")
	var legacy *TableLayoutUnsupportedError
	require.ErrorAs(t, err, &legacy)

	require.NoError(t, svc.DropTable(ctx, &DropTableRequest{Database: "db", Name: "t"}))
	assert.False(t, store.has(tableDDLJournalKey("db", "t")))
	assert.False(t, store.has(tableMetadataKey("db", "t")))
}

// TestTableService_GetTable_JournalWithoutDefinitionIsARewrite: the reorder
// keeps a journal whose definition is unreadable reported as the rewrite.
func TestTableService_GetTable_JournalWithoutDefinitionIsARewrite(t *testing.T) {
	svc, store := newTestTableService()
	store.put(tableDDLJournalKey("db", "t"), []byte(`{"state":"rewriting"}`))

	_, err := svc.GetTable(context.Background(), "db", "t")
	var inProgress *TableRewriteInProgressError
	require.ErrorAs(t, err, &inProgress)
}

// ---------------------------------------------------------------------------
// Indexes a rewrite changes (go-qrdb#547)
//
// DROP COLUMN and a layout-changing MODIFY COLUMN re-encode every row, and
// every index covering the column: an index keeping its incarnation would
// keep entries encoded for its old column set, and one left with no columns
// would be dropped from the catalog with its entries never reclaimed. Such
// an index gets a NEW incarnation in the journal's Target (rebuilt by the
// rewrite), and CompleteRewrite -- in the fenced transaction that makes
// Target the definition -- publishes a reclaim job for every incarnation the
// definition stops listing.
// ---------------------------------------------------------------------------

// newIndexedTable creates db.t (id, a, b, c) with idx_ab (a, b), idx_b (b)
// and idx_c (c).
func newIndexedTable(t *testing.T) (*TableService, *fakeCatalogStore, *TableDef) {
	t.Helper()
	svc, store := newTestTableService()
	def, err := svc.CreateTable(tenantCtx("acme"), &CreateTableRequest{
		Database: "db", Name: "t",
		Columns: []ColumnDef{
			{Name: "id", Type: "BIGINT UNSIGNED"}, {Name: "a", Type: "VARCHAR(32)"},
			{Name: "b", Type: "VARCHAR(32)"}, {Name: "c", Type: "INT"},
		},
		PrimaryKey: []PKColumn{{Name: "id"}},
		Indexes: []TableIndex{
			{Name: "idx_ab", Columns: []IndexCol{{Name: "a"}, {Name: "b"}}},
			{Name: "idx_b", Columns: []IndexCol{{Name: "b"}}},
			{Name: "idx_c", Columns: []IndexCol{{Name: "c"}}},
		},
	})
	require.NoError(t, err)
	return svc, store, def
}

func incarnationNamed(t *testing.T, def *TableDef, name string) uint64 {
	t.Helper()
	for _, idx := range def.Indexes {
		if idx.Name == name {
			return idx.Incarnation
		}
	}
	t.Fatalf("%s has no index %s", def.Name, name)
	return 0
}

func hasIndexNamed(def *TableDef, name string) bool {
	for _, idx := range def.Indexes {
		if idx.Name == name {
			return true
		}
	}
	return false
}

// TestTableService_DropColumn_ReincarnatesTheIndexesItChanges: the journal's
// Target gives a narrowed index a fresh incarnation, drops an emptied one,
// and leaves an untouched one alone.
func TestTableService_DropColumn_ReincarnatesTheIndexesItChanges(t *testing.T) {
	svc, _, def := newIndexedTable(t)
	_, err := svc.AlterTable(tenantCtx("acme"), &AlterTableRequest{
		Database: "db", Name: "t", Changes: []AlterTableChange{{Action: AlterDropColumn, Name: "b"}},
	})
	var rewrite *TableRewriteRequiredError
	require.ErrorAs(t, err, &rewrite)

	journal, _, err := svc.RewriteJournal(tenantCtx("acme"), "db", "t")
	require.NoError(t, err)
	target := journal.Target
	assert.Greater(t, incarnationNamed(t, target, "idx_ab"), def.IndexIncarnations, "a narrowed index is a new incarnation")
	assert.False(t, hasIndexNamed(target, "idx_b"), "an index left with no columns is dropped")
	assert.Equal(t, incarnationNamed(t, def, "idx_c"), incarnationNamed(t, target, "idx_c"), "an untouched index keeps its incarnation")
	assert.Equal(t, incarnationNamed(t, target, "idx_ab"), target.IndexIncarnations)
}

// TestTableService_ModifyColumn_ReincarnatesTheIndexesOnIt: a MODIFY that
// changes the column's encoding re-incarnates every index on it.
func TestTableService_ModifyColumn_ReincarnatesTheIndexesOnIt(t *testing.T) {
	svc, _, def := newIndexedTable(t)
	_, err := svc.AlterTable(tenantCtx("acme"), &AlterTableRequest{
		Database: "db", Name: "t",
		Changes: []AlterTableChange{{Action: AlterModifyColumn, Name: "c", Column: ColumnDef{Name: "c", Type: "BIGINT"}}},
	})
	var rewrite *TableRewriteRequiredError
	require.ErrorAs(t, err, &rewrite)

	journal, _, err := svc.RewriteJournal(tenantCtx("acme"), "db", "t")
	require.NoError(t, err)
	assert.Greater(t, incarnationNamed(t, journal.Target, "idx_c"), def.IndexIncarnations)
	assert.Equal(t, incarnationNamed(t, def, "idx_ab"), incarnationNamed(t, journal.Target, "idx_ab"))
}

// TestTableService_RenameColumn_KeepsIncarnations: a rename re-encodes
// nothing, so it keeps every incarnation.
func TestTableService_RenameColumn_KeepsIncarnations(t *testing.T) {
	svc, _, def := newIndexedTable(t)
	updated, err := svc.AlterTable(tenantCtx("acme"), &AlterTableRequest{
		Database: "db", Name: "t",
		Changes: []AlterTableChange{{Action: AlterRenameColumn, Name: "b", NewName: "bb"}},
	})
	require.NoError(t, err)
	for _, name := range []string{"idx_ab", "idx_b", "idx_c"} {
		assert.Equal(t, incarnationNamed(t, def, name), incarnationNamed(t, updated, name), name)
	}
}

// TestTableService_CompleteRewrite_ReclaimsRetiredIncarnations: the
// transaction that makes Target the definition publishes one job naming
// every incarnation Target no longer lists -- the narrowed index's old one
// and the emptied index's -- fenced on the metadata record and generation.
func TestTableService_CompleteRewrite_ReclaimsRetiredIncarnations(t *testing.T) {
	svc, store, def := newIndexedTable(t)
	ctx := tenantCtx("acme")
	_, err := svc.AlterTable(ctx, &AlterTableRequest{
		Database: "db", Name: "t", Changes: []AlterTableChange{{Action: AlterDropColumn, Name: "b"}},
	})
	var rewrite *TableRewriteRequiredError
	require.ErrorAs(t, err, &rewrite)
	require.Empty(t, store.keysWithPrefix(reclaimJobPrefix), "nothing is reclaimed while the old definition is live")
	meta, err := store.GetRevision(ctx, tableMetadataKey("db", "t"))
	require.NoError(t, err)

	var jobsAtCommit []string
	store.beforeExecute = func() { jobsAtCommit = store.keysWithPrefix(reclaimJobPrefix) }
	completed, err := svc.CompleteRewrite(ctx, "db", "t")
	require.NoError(t, err)
	assert.Empty(t, jobsAtCommit, "the job commits with the definition, not before it")

	job, entry := newestReclaimJob(t, store, nil)
	assert.ElementsMatch(t, []string{
		tableIndexPrefixFor("db", def.TableID, 0, incarnationNamed(t, def, "idx_ab")),
		tableIndexPrefixFor("db", def.TableID, 0, incarnationNamed(t, def, "idx_b")),
	}, job.KeyPrefixes)
	assert.Equal(t, []ReclaimFence{
		{Key: tableMetadataKey("db", "t"), ModRevision: meta.ModRevision},
		{Key: tableGenerationKey("db", def.TableID), ModRevision: 0},
	}, entry.Fences)
	assert.Greater(t, incarnationNamed(t, completed, "idx_ab"), def.IndexIncarnations)
}

// TestTableService_CompleteRewrite_UnchangedIndexesPublishNothing: a rewrite
// touching no index publishes no job.
func TestTableService_CompleteRewrite_UnchangedIndexesPublishNothing(t *testing.T) {
	svc, store, _ := newIndexedTable(t)
	ctx := tenantCtx("acme")
	_, err := svc.AlterTable(ctx, &AlterTableRequest{
		Database: "db", Name: "t",
		Changes: []AlterTableChange{{Action: AlterModifyColumn, Name: "id", Column: ColumnDef{Name: "id", Type: "BIGINT UNSIGNED", Nullable: true}}},
	})
	var rewrite *TableRewriteRequiredError
	require.ErrorAs(t, err, &rewrite)
	_, err = svc.CompleteRewrite(ctx, "db", "t")
	require.NoError(t, err)
	assert.Empty(t, store.keysWithPrefix(reclaimJobPrefix))
}

// TestTableService_AbortRewrite_ReclaimsTheUnusedIncarnations: an aborted
// rewrite may have built entries for Target's fresh incarnations; the abort
// publishes their reclaim in the transaction that discards the journal.
func TestTableService_AbortRewrite_ReclaimsTheUnusedIncarnations(t *testing.T) {
	svc, store, def := newIndexedTable(t)
	ctx := tenantCtx("acme")
	_, err := svc.AlterTable(ctx, &AlterTableRequest{
		Database: "db", Name: "t", Changes: []AlterTableChange{{Action: AlterDropColumn, Name: "b"}},
	})
	var rewrite *TableRewriteRequiredError
	require.ErrorAs(t, err, &rewrite)
	journal, _, err := svc.RewriteJournal(ctx, "db", "t")
	require.NoError(t, err)

	require.NoError(t, svc.AbortRewrite(ctx, "db", "t"))
	job, _ := newestReclaimJob(t, store, nil)
	assert.Equal(t, []string{
		tableIndexPrefixFor("db", def.TableID, 0, incarnationNamed(t, journal.Target, "idx_ab")),
	}, job.KeyPrefixes)
	live, err := svc.GetTable(ctx, "db", "t")
	require.NoError(t, err)
	assert.Equal(t, incarnationNamed(t, def, "idx_ab"), incarnationNamed(t, live, "idx_ab"), "the live definition is untouched")
}
