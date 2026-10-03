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

package query

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sqltest"
)

// TestExecute_StatementResolvesEachTableOnce: the query service runs every
// request as one command, so a statement resolves each table definition
// once, as it does on the MySQL wire.
func TestExecute_StatementResolvesEachTableOnce(t *testing.T) {
	h := sqltest.NewHarness()
	h.CreateTable(context.Background(), "testdb", "widgets", []service.ColumnDef{
		{Name: "id", Type: "INT"},
		{Name: "name", Type: "VARCHAR(16)", Nullable: true},
	})
	counting := h.CountTableReads()
	svc := NewService(newMockKV(), &mockDB{getFn: ownedKV("acme")}, WithRegistry(h.Registry))
	ctx := authedContext("acme")

	if _, err := svc.Execute(ctx, "testdb", "INSERT INTO widgets (id, name) VALUES (1, 'a')"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	counting.Reset()
	if _, err := svc.Execute(ctx, "testdb", "UPDATE widgets SET name = 'b' WHERE id = 1"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := counting.Gets(); got != 1 {
		t.Fatalf("an UPDATE read its table's definition %d times, want 1", got)
	}
}
