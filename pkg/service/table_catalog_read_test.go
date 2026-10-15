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

	"github.com/stretchr/testify/require"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// errCatalogReadTimeout is what a catalog read answers when it could not run:
// the shard between leaders, or a ReadIndex that timed out under load.
var errCatalogReadTimeout = &ReadError{Err: quicraft.ErrTimeout}

// newCatalogReadService returns a service holding table db.t, and its store.
func newCatalogReadService(t *testing.T) (*TableService, *fakeCatalogStore, context.Context) {
	t.Helper()
	svc, store := newTestTableService()
	ctx := tenant.WithTenantID(context.Background(), "acme")
	_, err := svc.CreateTable(ctx, &CreateTableRequest{Database: "db", Name: "t", Columns: testColumns()})
	require.NoError(t, err)
	return svc, store, ctx
}

// failReadsOf makes every read of key fail as a read that could not run.
func failReadsOf(store *fakeCatalogStore, key string) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.failReads = map[string]error{key: errCatalogReadTimeout}
}

// requireTheReadsOwnError asserts err is the read failure itself: retryable
// by its cause, and not the permanent answer absent would give.
func requireTheReadsOwnError(t *testing.T, err error, absent any) {
	t.Helper()
	require.Error(t, err)
	require.False(t, errors.As(err, absent), "a read that could not run was answered %T: %v", absent, err)
	require.ErrorIs(t, err, quicraft.ErrTimeout)
	require.True(t, LeadershipTransient(err), "the read's own error must stay retryable: %v", err)
}

// TestTableService_DefinitionReadThatCouldNotRunIsNotAMissingTable pins that
// only an absent definition is a missing table. Every operation that reads a
// table's definition used to answer ANY failed read with TableNotFoundError,
// a permanent answer: under contention a DROP TABLE of a table the same
// test had just created and queried was refused "table not found".
func TestTableService_DefinitionReadThatCouldNotRunIsNotAMissingTable(t *testing.T) {
	fk := ForeignKeyDef{Name: "fk_t", Columns: []string{"id"}, ParentTable: "t", ParentColumns: []string{"id"}}
	ops := map[string]func(context.Context, *TableService) error{
		"GetTable": func(ctx context.Context, svc *TableService) error {
			_, err := svc.GetTable(ctx, "db", "t")
			return err
		},
		"DropTable": func(ctx context.Context, svc *TableService) error {
			return svc.DropTable(ctx, &DropTableRequest{Database: "db", Name: "t"})
		},
		"RenameTable": func(ctx context.Context, svc *TableService) error {
			return svc.RenameTable(ctx, &RenameTableRequest{Database: "db", From: "t", To: "t2"})
		},
		"TruncateTable": func(ctx context.Context, svc *TableService) error {
			_, err := svc.TruncateTable(ctx, "db", "t", true)
			return err
		},
		"AlterTable": func(ctx context.Context, svc *TableService) error {
			_, err := svc.AlterTable(ctx, &AlterTableRequest{
				Database: "db", Name: "t",
				Changes: []AlterTableChange{{Action: AlterAddColumn, Column: ColumnDef{Name: "note", Type: "TEXT"}}},
			})
			return err
		},
		"CreateIndex": func(ctx context.Context, svc *TableService) error {
			return svc.CreateIndex(ctx, &CreateTableIndexRequest{
				Database: "db", Table: "t", Index: TableIndex{Name: "idx_email", Columns: []IndexCol{{Name: "email"}}},
			})
		},
		"DropIndex": func(ctx context.Context, svc *TableService) error {
			return svc.DropIndex(ctx, "db", "t", "idx_email")
		},
		"RenameIndex": func(ctx context.Context, svc *TableService) error {
			return svc.RenameIndex(ctx, &RenameTableIndexRequest{Database: "db", Table: "t", From: "idx_email", To: "idx_mail"})
		},
		"AddForeignKey": func(ctx context.Context, svc *TableService) error {
			return svc.AddForeignKey(ctx, &CreateTableForeignKeyRequest{Database: "db", Table: "t", ForeignKey: fk})
		},
		"DropForeignKey": func(ctx context.Context, svc *TableService) error {
			return svc.DropForeignKey(ctx, "db", "t", "fk_t")
		},
		"UpdateForeignKey": func(ctx context.Context, svc *TableService) error {
			return svc.UpdateForeignKey(ctx, &UpdateTableForeignKeyRequest{Database: "db", Table: "t", Name: "fk_t", ForeignKey: fk})
		},
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			svc, store, ctx := newCatalogReadService(t)
			failReadsOf(store, tableMetadataKey("db", "t"))
			var notFound *TableNotFoundError
			requireTheReadsOwnError(t, op(ctx, svc), &notFound)
		})
	}
}

// TestTableService_DefinitionReadOfAnAbsentTableIsStillNotFound is the other
// half: a definition the store reports absent is a missing table, through
// the same reads.
func TestTableService_DefinitionReadOfAnAbsentTableIsStillNotFound(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()
	var notFound *TableNotFoundError

	_, err := svc.GetTable(ctx, "db", "missing")
	require.ErrorAs(t, err, &notFound)
	require.ErrorAs(t, svc.DropTable(ctx, &DropTableRequest{Database: "db", Name: "missing"}), &notFound)
	require.ErrorAs(t, svc.DropIndex(ctx, "db", "missing", "idx"), &notFound)
	require.ErrorAs(t, svc.DropForeignKey(ctx, "db", "missing", "fk"), &notFound)
}

// TestTableService_JournalReadThatCouldNotRunIsNotNoRewrite pins the same
// rule for the rewrite journal. TableNoRewriteInProgressError is a final
// answer -- a caller of CompleteRewrite treats it as the rewrite having
// completed -- so a journal read that could not run must not give it.
func TestTableService_JournalReadThatCouldNotRunIsNotNoRewrite(t *testing.T) {
	ops := map[string]func(context.Context, *TableService) error{
		"RewriteJournal": func(ctx context.Context, svc *TableService) error {
			_, _, err := svc.RewriteJournal(ctx, "db", "t")
			return err
		},
		"CompleteRewrite": func(ctx context.Context, svc *TableService) error {
			_, err := svc.CompleteRewrite(ctx, "db", "t")
			return err
		},
		"AbortRewrite": func(ctx context.Context, svc *TableService) error {
			return svc.AbortRewrite(ctx, "db", "t")
		},
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			svc, store, ctx := newCatalogReadService(t)
			openRewrite(t, svc)
			failReadsOf(store, tableDDLJournalKey("db", "t"))
			var none *TableNoRewriteInProgressError
			requireTheReadsOwnError(t, op(ctx, svc), &none)
		})
	}

	// GetTable reads the journal to decide whether a rewrite is open; a
	// journal read that could not run must not decide "no rewrite" and hand
	// back the pre-rewrite definition as if it were settled.
	t.Run("GetTable", func(t *testing.T) {
		svc, store, ctx := newCatalogReadService(t)
		openRewrite(t, svc)
		failReadsOf(store, tableDDLJournalKey("db", "t"))
		_, err := svc.GetTable(ctx, "db", "t")
		var none *TableNoRewriteInProgressError
		requireTheReadsOwnError(t, err, &none)
	})

	// RewriteJournal then reads the live definition.
	t.Run("RewriteJournalDefinition", func(t *testing.T) {
		svc, store, ctx := newCatalogReadService(t)
		openRewrite(t, svc)
		failReadsOf(store, tableMetadataKey("db", "t"))
		_, _, err := svc.RewriteJournal(ctx, "db", "t")
		var notFound *TableNotFoundError
		requireTheReadsOwnError(t, err, &notFound)
	})
}
