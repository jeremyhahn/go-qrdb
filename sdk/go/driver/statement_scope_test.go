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

//go:build qrdb_sql

package driver

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/provider"
)

// countingTableService counts the table definitions read through it.
type countingTableService struct {
	service.TableServicer
	gets atomic.Int32
}

func (c *countingTableService) GetTable(ctx context.Context, database, name string) (*service.TableDef, error) {
	c.gets.Add(1)
	return c.TableServicer.GetTable(ctx, database, name)
}

// TestEmbedded_OverlappingStatementsAreRefused: a Query's statement lasts
// until its rows are closed, and a second statement on the same connection
// meanwhile is refused rather than run inside the first's command, where it
// could drop the first statement's pending writes or be served the first's
// resolution. database/sql never overlaps statements on one connection; a
// caller driving the driver directly can, so the connection itself refuses.
func TestEmbedded_OverlappingStatementsAreRefused(t *testing.T) {
	ctx := context.Background()
	cfg := &DSNConfig{Scheme: SchemeEmbedded, Database: "overlap", Backend: "memory", Engine: "kv"}
	dc, err := NewEmbeddedConnector(cfg, WithRegistry(newRelationalRegistry())).Connect(ctx)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = dc.Close() })
	c, ok := dc.(*embeddedConn)
	if !ok {
		t.Fatalf("connection is %T", dc)
	}
	for _, q := range []string{"CREATE TABLE widgets (id BIGINT PRIMARY KEY)", "INSERT INTO widgets (id) VALUES (1), (2)"} {
		if _, err := c.ExecContext(ctx, q, nil); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	rows, err := c.QueryContext(ctx, "SELECT id FROM widgets", nil)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	_, err = c.ExecContext(ctx, "INSERT INTO widgets (id) VALUES (3)", nil)
	var inProgress *provider.CommandInProgressError
	if !errors.As(err, &inProgress) {
		t.Fatalf("a statement while another's rows are open = %v, want CommandInProgressError", err)
	}
	if _, err := c.QueryContext(ctx, "SELECT 1", nil); !errors.As(err, &inProgress) {
		t.Fatalf("a query while another's rows are open = %v, want CommandInProgressError", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := c.ExecContext(ctx, "INSERT INTO widgets (id) VALUES (3)", nil); err != nil {
		t.Fatalf("the next statement after the rows close runs normally: %v", err)
	}
}

// TestEmbedded_StatementResolvesEachTableOnce: the embedded driver runs
// every statement as one command, so a statement resolves each table
// definition once, as it does on the MySQL wire -- for Exec, and for a Query
// whose rows are read after it returns.
func TestEmbedded_StatementResolvesEachTableOnce(t *testing.T) {
	reg := newRelationalRegistry()
	counting := &countingTableService{TableServicer: reg.tableSvc}
	reg.tableSvc = counting
	db := openRelationalTestDBWithRegistry(t, "scope", reg)

	if _, err := db.Exec("CREATE TABLE widgets (id BIGINT PRIMARY KEY, name VARCHAR(16))"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if _, err := db.Exec("INSERT INTO widgets (id, name) VALUES (1, 'a')"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	counting.gets.Store(0)
	if _, err := db.Exec("UPDATE widgets SET name = ? WHERE id = ?", "b", 1); err != nil {
		t.Fatalf("UPDATE: %v", err)
	}
	if got := counting.gets.Load(); got != 1 {
		t.Fatalf("an UPDATE read its table's definition %d times, want 1", got)
	}

	rows, err := db.Query("SELECT name FROM widgets WHERE id = ?", 1)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	var name string
	for rows.Next() {
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if name != "b" {
		t.Fatalf("name = %q, want b", name)
	}
	if _, err := db.Exec("ALTER TABLE widgets ADD COLUMN extra INT"); err != nil {
		t.Fatalf("ALTER: %v", err)
	}
	if _, err := db.Exec("UPDATE widgets SET extra = 1 WHERE id = 1"); err != nil {
		t.Fatalf("UPDATE after DDL: %v", err)
	}
}
