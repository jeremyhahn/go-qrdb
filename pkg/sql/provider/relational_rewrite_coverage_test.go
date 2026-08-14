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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// RelationalTable.CreateIndex / DropIndex / RenameIndex: error paths
// ---------------------------------------------------------------------------

func TestRelationalTable_CreateIndex_NoTableService(t *testing.T) {
	def := testUsersDef()
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)

	err = tbl.CreateIndex(newTestContext(), sqle.IndexDef{Name: "idx", Columns: []sqle.IndexColumn{{Name: "email"}}})
	var e *TableServiceUnavailableError
	require.ErrorAs(t, err, &e)
}

func TestRelationalTable_CreateIndex_NoColumns(t *testing.T) {
	def := testUsersDef()
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil, WithTableService(&fakeTableService{}))
	require.NoError(t, err)

	err = tbl.CreateIndex(newTestContext(), sqle.IndexDef{Name: "idx"})
	var e *IndexNoColumnsError
	require.ErrorAs(t, err, &e)
}

func TestRelationalTable_CreateIndex_TableServiceError(t *testing.T) {
	def := testUsersDef()
	fake := &fakeTableService{}
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil, WithTableService(fake))
	require.NoError(t, err)

	// fakeTableService.CreateIndex always returns nil; wrap to inject an
	// error instead.
	tsErr := &erroringTableService{TableServicer: fake, createIndexErr: assert.AnError}
	tbl2, err := NewRelationalTable(def, &mockKVService{}, nil, WithTableService(tsErr))
	require.NoError(t, err)

	err = tbl2.CreateIndex(newTestContext(), sqle.IndexDef{Name: "idx", Columns: []sqle.IndexColumn{{Name: "email"}}})
	assert.ErrorIs(t, err, assert.AnError)
	_ = tbl
}

func TestRelationalTable_DropIndex_NoTableService(t *testing.T) {
	def := testUsersDef()
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)

	err = tbl.DropIndex(newTestContext(), "idx_email")
	var e *TableServiceUnavailableError
	require.ErrorAs(t, err, &e)
}

func TestRelationalTable_DropIndex_TableServiceError(t *testing.T) {
	def := testUsersDef()
	fake := &erroringTableService{TableServicer: &fakeTableService{}, dropIndexErr: assert.AnError}
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil, WithTableService(fake))
	require.NoError(t, err)

	err = tbl.DropIndex(newTestContext(), "idx_email")
	assert.ErrorIs(t, err, assert.AnError)
}

func TestRelationalTable_DropIndex_DeleteError(t *testing.T) {
	def := testUsersDef()
	kv := &mockKVService{
		listFn:   func(context.Context, string) ([]string, error) { return []string{"a"}, nil },
		deleteFn: func(context.Context, string) error { return assert.AnError },
	}
	tbl, err := NewRelationalTable(def, kv, nil, WithTableService(&fakeTableService{}))
	require.NoError(t, err)

	err = tbl.DropIndex(newTestContext(), "idx_email")
	assert.ErrorIs(t, err, assert.AnError)
}

func TestRelationalTable_RenameIndex_NoTableService(t *testing.T) {
	def := testUsersDef()
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil)
	require.NoError(t, err)

	err = tbl.RenameIndex(newTestContext(), "idx_email", "idx_email2")
	var e *TableServiceUnavailableError
	require.ErrorAs(t, err, &e)
}

func TestRelationalTable_RenameIndex_CreateIndexError(t *testing.T) {
	def := testUsersDef()
	fake := &erroringTableService{TableServicer: &fakeTableService{}, createIndexErr: assert.AnError}
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil, WithTableService(fake))
	require.NoError(t, err)

	err = tbl.RenameIndex(newTestContext(), "idx_email", "idx_email2")
	assert.ErrorIs(t, err, assert.AnError)
}

func TestRelationalTable_RenameIndex_DropIndexError(t *testing.T) {
	def := testUsersDef()
	fake := &erroringTableService{TableServicer: &fakeTableService{}, dropIndexErr: assert.AnError}
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil, WithTableService(fake))
	require.NoError(t, err)

	err = tbl.RenameIndex(newTestContext(), "idx_email", "idx_email2")
	assert.ErrorIs(t, err, assert.AnError)
}

func TestRelationalTable_RenameIndex_ScanError(t *testing.T) {
	def := testUsersDef()
	kv := &mockKVService{scanFn: func(context.Context, string) (map[string][]byte, error) { return nil, assert.AnError }}
	tbl, err := NewRelationalTable(def, kv, nil, WithTableService(&fakeTableService{}))
	require.NoError(t, err)

	err = tbl.RenameIndex(newTestContext(), "idx_email", "idx_email2")
	assert.ErrorIs(t, err, assert.AnError)
}

// erroringTableService wraps a service.TableServicer, overriding specific
// methods to return injected errors, for exercising this package's error
// propagation from each individual TableServicer call.
type erroringTableService struct {
	service.TableServicer
	createIndexErr error
	dropIndexErr   error
}

func (e *erroringTableService) CreateIndex(ctx context.Context, req *service.CreateTableIndexRequest) error {
	if e.createIndexErr != nil {
		return e.createIndexErr
	}
	return e.TableServicer.CreateIndex(ctx, req)
}

func (e *erroringTableService) DropIndex(ctx context.Context, database, table, index string) error {
	if e.dropIndexErr != nil {
		return e.dropIndexErr
	}
	return e.TableServicer.DropIndex(ctx, database, table, index)
}

// ---------------------------------------------------------------------------
// RelationalTable.AddColumn: propagates columnDefFromSQL's error
// ---------------------------------------------------------------------------

func TestRelationalTable_AddColumn_GeneratedColumn(t *testing.T) {
	def := testUsersDef()
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil, WithTableService(&fakeTableService{}))
	require.NoError(t, err)

	genDef, gerr := sqle.NewColumnDefaultValue(nil, nil, false, false, true)
	require.NoError(t, gerr)
	err = tbl.AddColumn(newTestContext(), &sqle.Column{Name: "computed", Generated: genDef}, nil)
	var e *GeneratedColumnUnsupportedError
	require.ErrorAs(t, err, &e)
}

// ---------------------------------------------------------------------------
// RewriteInserter: error paths
// ---------------------------------------------------------------------------

func TestRelationalTable_RewriteInserter_AlterTableGenericError(t *testing.T) {
	def := testUsersDef()
	fake := &alterTableErrorService{TableServicer: &fakeTableService{}, err: assert.AnError}
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil, WithTableService(fake))
	require.NoError(t, err)

	_, err = tbl.RewriteInserter(newTestContext(), sqle.PrimaryKeySchema{}, sqle.PrimaryKeySchema{},
		&sqle.Column{Name: "email"}, nil, nil)
	assert.ErrorIs(t, err, assert.AnError)
}

func TestRelationalTable_RewriteInserter_RewriteJournalError(t *testing.T) {
	def := testUsersDef()
	fake := &fakeTableService{
		rewriteJournalFn: func(_ context.Context, database, name string) (*service.TableDDLJournal, *service.TableDef, error) {
			return nil, nil, assert.AnError
		},
	}
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil, WithTableService(fake))
	require.NoError(t, err)

	_, err = tbl.RewriteInserter(newTestContext(), sqle.PrimaryKeySchema{}, sqle.PrimaryKeySchema{},
		&sqle.Column{Name: "email"}, nil, nil)
	assert.ErrorIs(t, err, assert.AnError)
}

func TestRelationalTable_RewriteInserter_JournalMissingTarget(t *testing.T) {
	def := testUsersDef()
	fake := &fakeTableService{
		rewriteJournalFn: func(_ context.Context, database, name string) (*service.TableDDLJournal, *service.TableDef, error) {
			return &service.TableDDLJournal{}, def, nil
		},
	}
	tbl, err := NewRelationalTable(def, &mockKVService{}, nil, WithTableService(fake))
	require.NoError(t, err)

	_, err = tbl.RewriteInserter(newTestContext(), sqle.PrimaryKeySchema{}, sqle.PrimaryKeySchema{},
		&sqle.Column{Name: "email"}, nil, nil)
	var e *RewriteJournalMissingTargetError
	require.ErrorAs(t, err, &e)
}

// alterTableErrorService wraps a service.TableServicer, forcing AlterTable
// to fail with a generic (non-TableRewriteRequiredError) error.
type alterTableErrorService struct {
	service.TableServicer
	err error
}

func (e *alterTableErrorService) AlterTable(context.Context, *service.AlterTableRequest) (*service.TableDef, error) {
	return nil, e.err
}

// ---------------------------------------------------------------------------
// resumeRewrite: error paths
// ---------------------------------------------------------------------------

func TestResumeRewrite_JournalGenericError(t *testing.T) {
	fake := &fakeTableService{
		rewriteJournalFn: func(context.Context, string, string) (*service.TableDDLJournal, *service.TableDef, error) {
			return nil, nil, assert.AnError
		},
	}
	err := resumeRewrite(context.Background(), fake, &mockKVService{}, "shop", "users")
	assert.ErrorIs(t, err, assert.AnError)
}

func TestResumeRewrite_JournalMissingTarget(t *testing.T) {
	def := testUsersDef()
	fake := &fakeTableService{
		rewriteJournalFn: func(context.Context, string, string) (*service.TableDDLJournal, *service.TableDef, error) {
			return &service.TableDDLJournal{}, def, nil
		},
	}
	err := resumeRewrite(context.Background(), fake, &mockKVService{}, "shop", "users")
	var e *RewriteJournalMissingTargetError
	require.ErrorAs(t, err, &e)
}

func TestResumeRewrite_PrimaryKeyIncompatible(t *testing.T) {
	current := testUsersDef()
	target := testUsersDef()
	target.PrimaryKey = nil // primary key dropped entirely: unsupported
	fake := &fakeTableService{
		rewriteJournalFn: func(context.Context, string, string) (*service.TableDDLJournal, *service.TableDef, error) {
			return &service.TableDDLJournal{Target: target}, current, nil
		},
	}
	err := resumeRewrite(context.Background(), fake, &mockKVService{}, "shop", "users")
	var e *PrimaryKeyRewriteUnsupportedError
	require.ErrorAs(t, err, &e)
}

func TestResumeRewrite_CompleteRewriteGenericError(t *testing.T) {
	h := newDDLHarness()
	current := testUsersDef()
	current.TableID = 999
	target := testUsersDef()
	target.TableID = 999
	target.SchemaVersion = 2

	fake := &fakeTableService{
		rewriteJournalFn: func(context.Context, string, string) (*service.TableDDLJournal, *service.TableDef, error) {
			return &service.TableDDLJournal{Target: target}, current, nil
		},
		completeRewriteFn: func(context.Context, string, string) (*service.TableDef, error) {
			return nil, assert.AnError
		},
	}
	err := resumeRewrite(context.Background(), fake, h.kvSvc, "shop", "users")
	assert.ErrorIs(t, err, assert.AnError)
}

func TestResumeRewrite_NoneInProgress_IsSuccess(t *testing.T) {
	err := resumeRewrite(context.Background(), &fakeTableService{}, &mockKVService{}, "shop", "users")
	assert.NoError(t, err)
}

// ---------------------------------------------------------------------------
// runRewrite: error paths
// ---------------------------------------------------------------------------

func TestRunRewrite_ColumnRemapError(t *testing.T) {
	current := testUsersDef()
	target := testUsersDef()
	err := runRewrite(context.Background(), &mockKVService{}, "shop", current, target,
		[]service.AlterTableChange{{Action: service.AlterDropColumn, Name: "does_not_exist"}})
	var e *RewriteColumnNotFoundError
	require.ErrorAs(t, err, &e)
}

func TestRunRewrite_ColumnMismatch(t *testing.T) {
	current := testUsersDef()
	target := testUsersDef()
	target.Columns = append([]service.ColumnDef(nil), target.Columns...)
	target.Columns[1].Type = "TEXT" // diverges from what dropping "age" would actually produce
	err := runRewrite(context.Background(), &mockKVService{}, "shop", current, target,
		[]service.AlterTableChange{{Action: service.AlterDropColumn, Name: "age"}})
	var e *RewriteColumnMismatchError
	require.ErrorAs(t, err, &e)
}

func TestRunRewrite_ScanError(t *testing.T) {
	current := testUsersDef()
	target := testUsersDef()
	target.Columns = target.Columns[:2]
	target.SchemaVersion++
	kv := &mockKVService{scanFn: func(context.Context, string) (map[string][]byte, error) { return nil, assert.AnError }}
	err := runRewrite(context.Background(), kv, "shop", current, target,
		[]service.AlterTableChange{{Action: service.AlterDropColumn, Name: "age"}})
	assert.ErrorIs(t, err, assert.AnError)
}

func TestRunRewrite_DecodeError(t *testing.T) {
	current := testUsersDef()
	target := testUsersDef()
	target.Columns = target.Columns[:2]
	target.SchemaVersion++
	kv := &mockKVService{scanFn: func(context.Context, string) (map[string][]byte, error) {
		return map[string][]byte{"badkey": []byte("not a valid row")}, nil
	}}
	err := runRewrite(context.Background(), kv, "shop", current, target,
		[]service.AlterTableChange{{Action: service.AlterDropColumn, Name: "age"}})
	var e *RowRewriteError
	require.ErrorAs(t, err, &e)
}

func TestRunRewrite_PutError(t *testing.T) {
	h := newDDLHarness()
	def, err := h.tableSvc.CreateTable(context.Background(), &service.CreateTableRequest{
		Database: "shop", Name: "users",
		Columns:    []service.ColumnDef{{Name: "id", Type: "BIGINT UNSIGNED"}, {Name: "legacy", Type: "VARCHAR(50)"}},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
	})
	require.NoError(t, err)
	seedRow(t, h.kvSvc, "shop", def, sqle.Row{uint64(1), "old"})

	target := *def
	target.Columns = def.Columns[:1]
	target.SchemaVersion++

	kv := &mockKVService{
		scanFn: h.kvSvc.Scan,
		putFn:  func(context.Context, string, []byte) error { return assert.AnError },
	}
	err = runRewrite(context.Background(), kv, "shop", def, &target,
		[]service.AlterTableChange{{Action: service.AlterDropColumn, Name: "legacy"}})
	assert.ErrorIs(t, err, assert.AnError)
}

func TestRunRewrite_SkipsAlreadyMigratedRows(t *testing.T) {
	h := newDDLHarness()
	def, err := h.tableSvc.CreateTable(context.Background(), &service.CreateTableRequest{
		Database: "shop", Name: "users",
		Columns:    []service.ColumnDef{{Name: "id", Type: "BIGINT UNSIGNED"}, {Name: "legacy", Type: "VARCHAR(50)"}},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
	})
	require.NoError(t, err)
	seedRow(t, h.kvSvc, "shop", def, sqle.Row{uint64(1), "old"})

	target := *def
	target.Columns = def.Columns[:1]
	target.SchemaVersion++

	// First pass migrates the row.
	require.NoError(t, runRewrite(context.Background(), h.kvSvc, "shop", def, &target,
		[]service.AlterTableChange{{Action: service.AlterDropColumn, Name: "legacy"}}))

	// A second pass over the same (now-migrated) data must not error or
	// attempt to re-decode it with the stale codec.
	require.NoError(t, runRewrite(context.Background(), h.kvSvc, "shop", def, &target,
		[]service.AlterTableChange{{Action: service.AlterDropColumn, Name: "legacy"}}))
}

// ---------------------------------------------------------------------------
// columnRemap / columnRemapIndexOf
// ---------------------------------------------------------------------------

func TestColumnRemap_ModifyColumnNotFound(t *testing.T) {
	def := testUsersDef()
	_, _, err := columnRemap(def, []service.AlterTableChange{{Action: service.AlterModifyColumn, Name: "missing"}})
	var e *RewriteColumnNotFoundError
	require.ErrorAs(t, err, &e)
}

func TestColumnRemap_UnknownActionIgnored(t *testing.T) {
	def := testUsersDef()
	cols, remap, err := columnRemap(def, []service.AlterTableChange{{Action: service.AlterAddColumn}})
	require.NoError(t, err)
	assert.Len(t, cols, len(def.Columns))
	assert.Len(t, remap, len(def.Columns))
}

// ---------------------------------------------------------------------------
// columnListsPhysicallyEqual
// ---------------------------------------------------------------------------

func TestColumnListsPhysicallyEqual(t *testing.T) {
	a := []service.ColumnDef{{Name: "id", Type: "BIGINT UNSIGNED"}}
	b := []service.ColumnDef{{Name: "id", Type: "BIGINT UNSIGNED"}}
	assert.True(t, columnListsPhysicallyEqual(a, b))
}

func TestColumnListsPhysicallyEqual_DifferentLength(t *testing.T) {
	a := []service.ColumnDef{{Name: "id", Type: "BIGINT UNSIGNED"}}
	b := []service.ColumnDef{}
	assert.False(t, columnListsPhysicallyEqual(a, b))
}

func TestColumnListsPhysicallyEqual_DifferentName(t *testing.T) {
	a := []service.ColumnDef{{Name: "id", Type: "BIGINT UNSIGNED"}}
	b := []service.ColumnDef{{Name: "other", Type: "BIGINT UNSIGNED"}}
	assert.False(t, columnListsPhysicallyEqual(a, b))
}

func TestColumnListsPhysicallyEqual_DifferentType(t *testing.T) {
	a := []service.ColumnDef{{Name: "id", Type: "BIGINT UNSIGNED"}}
	b := []service.ColumnDef{{Name: "id", Type: "INT"}}
	assert.False(t, columnListsPhysicallyEqual(a, b))
}

// ---------------------------------------------------------------------------
// validateRewritablePrimaryKey / primaryKeyColumnFamilyChanged / findColumnByName
// ---------------------------------------------------------------------------

func TestValidateRewritablePrimaryKey_DifferentLength(t *testing.T) {
	current := testUsersDef()
	target := testUsersDef()
	target.PrimaryKey = append(target.PrimaryKey, service.PKColumn{Name: "email"})
	err := validateRewritablePrimaryKey(current, target)
	var e *PrimaryKeyRewriteUnsupportedError
	require.ErrorAs(t, err, &e)
}

func TestValidateRewritablePrimaryKey_DifferentPrefixLength(t *testing.T) {
	current := testUsersDef()
	target := testUsersDef()
	target.PrimaryKey = []service.PKColumn{{Name: "id", PrefixLength: 5}}
	err := validateRewritablePrimaryKey(current, target)
	var e *PrimaryKeyRewriteUnsupportedError
	require.ErrorAs(t, err, &e)
}

func TestValidateRewritablePrimaryKey_FamilyChanged(t *testing.T) {
	current := testUsersDef()
	target := testUsersDef()
	target.Columns = append([]service.ColumnDef(nil), target.Columns...)
	target.Columns[0].Type = "VARCHAR(20)" // id: BIGINT UNSIGNED -> VARCHAR
	err := validateRewritablePrimaryKey(current, target)
	var e *PrimaryKeyRewriteUnsupportedError
	require.ErrorAs(t, err, &e)
}

func TestValidateRewritablePrimaryKey_Compatible(t *testing.T) {
	current := testUsersDef()
	target := testUsersDef()
	target.Columns = append([]service.ColumnDef(nil), target.Columns...)
	target.Columns[0].Type = "INT UNSIGNED" // widen within the numeric family
	err := validateRewritablePrimaryKey(current, target)
	assert.NoError(t, err)
}

func TestFindColumnByName_NotFound(t *testing.T) {
	def := testUsersDef()
	_, ok := findColumnByName(def, "does_not_exist")
	assert.False(t, ok)
}

// ---------------------------------------------------------------------------
// rewriteRowInserter.Close: every branch, exercised directly
// ---------------------------------------------------------------------------

func TestRewriteRowInserter_Close_RunRewriteError(t *testing.T) {
	current := testUsersDef()
	target := testUsersDef()
	ri := &rewriteRowInserter{
		kvSvc:    &mockKVService{scanFn: func(context.Context, string) (map[string][]byte, error) { return nil, assert.AnError }},
		tableSvc: &fakeTableService{},
		database: "shop", name: "users",
		current: current,
		journal: &service.TableDDLJournal{
			Target:  target,
			Changes: []service.AlterTableChange{{Action: service.AlterModifyColumn, Name: "email", Column: service.ColumnDef{Type: "VARCHAR(255)"}}},
		},
	}
	err := ri.Close(newTestContext())
	assert.ErrorIs(t, err, assert.AnError)
}

func TestRewriteRowInserter_Close_CompleteRewriteGenericError(t *testing.T) {
	current := testUsersDef()
	target := testUsersDef()
	ri := &rewriteRowInserter{
		kvSvc: &mockKVService{},
		tableSvc: &fakeTableService{
			completeRewriteFn: func(context.Context, string, string) (*service.TableDef, error) {
				return nil, assert.AnError
			},
		},
		database: "shop", name: "users",
		current: current,
		journal: &service.TableDDLJournal{
			Target:  target,
			Changes: []service.AlterTableChange{{Action: service.AlterModifyColumn, Name: "email", Column: service.ColumnDef{Type: "VARCHAR(255)"}}},
		},
	}
	err := ri.Close(newTestContext())
	assert.ErrorIs(t, err, assert.AnError)
}

func TestRewriteRowInserter_Close_CompleteRewriteRacedAway(t *testing.T) {
	current := testUsersDef()
	target := testUsersDef()
	ri := &rewriteRowInserter{
		kvSvc:    &mockKVService{},
		tableSvc: &fakeTableService{}, // default CompleteRewrite -> TableNoRewriteInProgressError
		database: "shop", name: "users",
		current: current,
		journal: &service.TableDDLJournal{
			Target:  target,
			Changes: []service.AlterTableChange{{Action: service.AlterModifyColumn, Name: "email", Column: service.ColumnDef{Type: "VARCHAR(255)"}}},
		},
	}
	err := ri.Close(newTestContext())
	assert.NoError(t, err, "a racing completion must be treated as success, not an error")
}
