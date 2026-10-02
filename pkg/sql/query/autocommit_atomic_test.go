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

package query

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sqltest"
)

// TestExecute_AutocommitStatementIsAtomic: a multi-row INSERT whose third
// row is a duplicate leaves its first two rows absent. Every query-service
// request is one autocommit statement, and a statement is its own
// transaction.
func TestExecute_AutocommitStatementIsAtomic(t *testing.T) {
	h := sqltest.NewHarness()
	if _, err := h.TableSvc.CreateTable(context.Background(), &service.CreateTableRequest{
		Database:   "testdb",
		Name:       "widgets",
		Columns:    []service.ColumnDef{{Name: "id", Type: "BIGINT", Nullable: false}},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	svc := NewService(newMockKV(), &mockDB{getFn: ownedKV("acme")}, WithRegistry(h.Registry))
	ctx := authedContext("acme")

	if _, err := svc.Execute(ctx, "testdb", "INSERT INTO widgets (id) VALUES (1)"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := svc.Execute(ctx, "testdb", "INSERT INTO widgets (id) VALUES (10), (11), (1)"); err == nil {
		t.Fatal("row 3 duplicates id 1; the statement must fail")
	}
	result, err := svc.Execute(ctx, "testdb", "SELECT id FROM widgets WHERE id IN (10, 11)")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if result.RowCount != 0 {
		t.Fatalf("rows 1 and 2 of the failed statement were committed: %v", result.Rows)
	}
}

// racingTxn commits a row under prefix just before the first transaction
// that writes one: a concurrent statement winning the race between this
// statement's duplicate check and its commit.
type racingTxn struct {
	service.TransactionServicer
	prefix string
	done   atomic.Bool
}

func (r *racingTxn) Execute(ctx context.Context, c []service.Compare, s, fo []service.Op) (*service.TransactionResponse, error) {
	for _, op := range s {
		if op.Type == service.OpPut && strings.HasPrefix(string(op.Key), r.prefix) && r.done.CompareAndSwap(false, true) {
			if _, err := r.TransactionServicer.Execute(ctx, nil, []service.Op{op}, nil); err != nil {
				return nil, err
			}
			break
		}
	}
	return r.TransactionServicer.Execute(ctx, c, s, fo)
}

// TestExecute_CommitFailureIsReported: an autocommit statement commits when
// its result is closed, and a commit that fails must fail the request -- not
// be dropped with the iterator.
func TestExecute_CommitFailureIsReported(t *testing.T) {
	h := sqltest.NewHarness()
	def, err := h.TableSvc.CreateTable(context.Background(), &service.CreateTableRequest{
		Database:   "testdb",
		Name:       "widgets",
		Columns:    []service.ColumnDef{{Name: "id", Type: "BIGINT", Nullable: false}},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	reg := h.Registry.(*sqltest.FakeRegistry)
	inner, err := reg.TransactionServiceFn("acme", "testdb")
	if err != nil {
		t.Fatalf("txn: %v", err)
	}
	reg.TransactionServiceFn = func(string, string) (service.TransactionServicer, error) {
		return &racingTxn{TransactionServicer: inner, prefix: service.TableDataPrefix("testdb", def.TableID, 0)}, nil
	}
	svc := NewService(newMockKV(), &mockDB{getFn: ownedKV("acme")}, WithRegistry(h.Registry))

	if _, err := svc.Execute(authedContext("acme"), "testdb", "INSERT INTO widgets (id) VALUES (7)"); err == nil {
		t.Fatal("the statement's commit lost to a concurrent insert of the same key; the request must fail")
	}
}
