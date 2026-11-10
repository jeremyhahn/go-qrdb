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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// resumeRewrite: error paths
// ---------------------------------------------------------------------------

func TestResumeRewrite_JournalGenericError(t *testing.T) {
	fake := &fakeTableService{
		rewriteJournalFn: func(context.Context, string, string) (*service.TableDDLJournal, *service.TableDef, error) {
			return nil, nil, assert.AnError
		},
	}
	err := resumeRewrite(context.Background(), fake, &mockKVService{}, nil, "shop", "users")
	assert.ErrorIs(t, err, assert.AnError)
}

func TestResumeRewrite_JournalMissingTarget(t *testing.T) {
	def := testUsersDef()
	fake := &fakeTableService{
		rewriteJournalFn: func(context.Context, string, string) (*service.TableDDLJournal, *service.TableDef, error) {
			return &service.TableDDLJournal{}, def, nil
		},
	}
	err := resumeRewrite(context.Background(), fake, &mockKVService{}, nil, "shop", "users")
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
	err := resumeRewrite(context.Background(), fake, &mockKVService{}, nil, "shop", "users")
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
	err := resumeRewrite(context.Background(), fake, h.kvSvc, h.txnSvc, "shop", "users")
	assert.ErrorIs(t, err, assert.AnError)
}

// ---------------------------------------------------------------------------
// runRewrite: error paths
// ---------------------------------------------------------------------------

func TestRunRewrite_ColumnRemapError(t *testing.T) {
	current := testUsersDef()
	target := testUsersDef()
	err := runRewrite(context.Background(), &mockKVService{}, nil, "shop", current, &service.TableDDLJournal{Target: target,
		Changes: []service.AlterTableChange{{Action: service.AlterDropColumn, Name: "does_not_exist"}}})
	var e *RewriteColumnNotFoundError
	require.ErrorAs(t, err, &e)
}

func TestRunRewrite_ColumnMismatch(t *testing.T) {
	current := testUsersDef()
	target := testUsersDef()
	target.Columns = append([]service.ColumnDef(nil), target.Columns...)
	target.Columns[1].Type = "TEXT" // diverges from what dropping "age" would actually produce
	err := runRewrite(context.Background(), &mockKVService{}, nil, "shop", current, &service.TableDDLJournal{Target: target,
		Changes: []service.AlterTableChange{{Action: service.AlterDropColumn, Name: "age"}}})
	var e *RewriteColumnMismatchError
	require.ErrorAs(t, err, &e)
}

func TestRunRewrite_ScanError(t *testing.T) {
	current := testUsersDef()
	target := testUsersDef()
	target.Columns = target.Columns[:2]
	target.SchemaVersion++
	kv := &mockKVService{scanFn: func(context.Context, string) (map[string][]byte, error) { return nil, assert.AnError }}
	err := runRewrite(context.Background(), kv, nil, "shop", current, &service.TableDDLJournal{Target: target,
		Changes: []service.AlterTableChange{{Action: service.AlterDropColumn, Name: "age"}}})
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
	err := runRewrite(context.Background(), kv, nil, "shop", current, &service.TableDDLJournal{Target: target,
		Changes: []service.AlterTableChange{{Action: service.AlterDropColumn, Name: "age"}}})
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

	kv := &mockKVService{scanFn: h.kvSvc.Scan}
	txn := &errTxnServicer{err: assert.AnError}
	err = runRewrite(context.Background(), kv, txn, "shop", def, &service.TableDDLJournal{Target: &target,
		Changes: []service.AlterTableChange{{Action: service.AlterDropColumn, Name: "legacy"}}})
	assert.ErrorIs(t, err, assert.AnError)
}

// errTxnServicer is a service.TransactionServicer whose Execute always
// returns err, simulating an infrastructure failure (as opposed to a lost
// compare-and-swap, which reports Succeeded: false with a nil error).
type errTxnServicer struct {
	service.TransactionServicer
	err error
}

func (e *errTxnServicer) Execute(context.Context, []service.Compare, []service.Op, []service.Op) (*service.TransactionResponse, error) {
	return nil, e.err
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

	dataPrefix := service.TableDataPrefix("shop", def.TableID, 0)

	// First pass migrates the row.
	require.NoError(t, runRewrite(context.Background(), h.kvSvc, h.txnSvc, "shop", def, &service.TableDDLJournal{Target: &target,
		Changes: []service.AlterTableChange{{Action: service.AlterDropColumn, Name: "legacy"}}}))
	migrated, err := h.kvSvc.Scan(context.Background(), dataPrefix)
	require.NoError(t, err)
	require.Len(t, migrated, 1)

	// A second pass over the same (now-migrated) data must not error,
	// re-decode it with the stale codec, or rewrite it.
	require.NoError(t, runRewrite(context.Background(), h.kvSvc, h.txnSvc, "shop", def, &service.TableDDLJournal{Target: &target,
		Changes: []service.AlterTableChange{{Action: service.AlterDropColumn, Name: "legacy"}}}))
	again, err := h.kvSvc.Scan(context.Background(), dataPrefix)
	require.NoError(t, err)
	assert.Equal(t, migrated, again)
}

// TestRunRewrite_TruncateRaceFailsTheRewrite pins go-qrdb#547's explicit
// scope for relational_rewrite.go: a TRUNCATE that commits after current was
// resolved but before runRewrite writes its converted rows back must fail
// the rewrite's attempt with *service.TableGenerationStaleError, rather than
// let it write re-encoded rows into a generation the reclaim sweeper is
// already draining. current here plays exactly the role a paused ALTER TABLE
// DROP/MODIFY COLUMN statement's already-resolved TableDef would.
func TestRunRewrite_TruncateRaceFailsTheRewrite(t *testing.T) {
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

	// The race: TRUNCATE commits after def (== current) was resolved but
	// before runRewrite gets to its post-conversion generation check.
	_, err = h.tableSvc.TruncateTable(context.Background(), "shop", "users", true)
	require.NoError(t, err)

	err = runRewrite(context.Background(), h.kvSvc, h.txnSvc, "shop", def, &service.TableDDLJournal{Target: &target,
		Changes: []service.AlterTableChange{{Action: service.AlterDropColumn, Name: "legacy"}}})
	var stale *service.TableGenerationStaleError
	require.ErrorAs(t, err, &stale)
	assert.Equal(t, uint64(0), stale.Expected)
	assert.Equal(t, uint64(1), stale.Actual)

	// The rewrite must not have written into the new generation: it failed
	// before the write-back loop ever ran.
	rows, err := h.kvSvc.Scan(context.Background(), service.TableDataPrefix("shop", def.TableID, 1))
	require.NoError(t, err)
	assert.Empty(t, rows)
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
	assert.Equal(t, def.Columns, cols)
	want := make([]int, len(def.Columns))
	for i := range want {
		want[i] = i
	}
	assert.Equal(t, want, remap, "an ignored change must leave the identity remap")
}

// ---------------------------------------------------------------------------
// columnListsPhysicallyEqual
// ---------------------------------------------------------------------------

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
