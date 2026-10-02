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

//go:build qrdb_mysql

package mysql

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sqltest"
)

// TestMySQLWire_AutocommitStatementIsAtomic: a multi-row autocommit INSERT
// whose third row is a duplicate leaves its first two rows absent, as in
// InnoDB -- a statement outside a transaction is its own transaction.
func TestMySQLWire_AutocommitStatementIsAtomic(t *testing.T) {
	db := newRowLockWire(t)

	_, err := db.Exec("INSERT INTO accounts VALUES (10, 1), (11, 1), (1, 1)")
	require.Error(t, err, "row 3 duplicates id 1")

	var n int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM accounts WHERE id IN (10, 11)").Scan(&n))
	assert.Zero(t, n, "rows 1 and 2 of the failed statement must not be committed")
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM accounts").Scan(&n))
	assert.Equal(t, 2, n)
}

// TestMySQLWire_AutocommitOff_IsOneTransaction: SET autocommit = 0 opens a
// multi-statement transaction, as in InnoDB; COMMIT makes all of it
// durable and ROLLBACK none of it.
func TestMySQLWire_AutocommitOff_IsOneTransaction(t *testing.T) {
	db := newRowLockWire(t)
	a, b := pinned(t, db), pinned(t, db)

	mustExec(t, a, "SET autocommit = 0")
	mustExec(t, a, "INSERT INTO accounts VALUES (10, 1)")
	mustExec(t, a, "INSERT INTO accounts VALUES (11, 1)")
	count := func(c *sql.Conn) int {
		var n int
		require.NoError(t, c.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM accounts WHERE id IN (10, 11)").Scan(&n))
		return n
	}
	assert.Equal(t, 2, count(a), "the transaction reads its own writes")
	assert.Zero(t, count(b), "nothing is visible to another session before COMMIT")
	mustExec(t, a, "COMMIT")
	assert.Equal(t, 2, count(b), "COMMIT keeps every statement's writes")

	mustExec(t, a, "INSERT INTO accounts VALUES (12, 1)")
	mustExec(t, a, "ROLLBACK")
	var n int
	require.NoError(t, b.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM accounts WHERE id = 12").Scan(&n))
	assert.Zero(t, n, "ROLLBACK undoes the transaction")
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

// TestMySQLWire_CommitFailureIsReported: an autocommit statement commits at
// its end, and a commit lost to a concurrent insert of the same key reaches
// the client as the duplicate it is (1062).
func TestMySQLWire_CommitFailureIsReported(t *testing.T) {
	h, closeFn, err := sqltest.NewLockingHarness()
	require.NoError(t, err)
	t.Cleanup(closeFn)
	def, err := h.TableSvc.CreateTable(context.Background(), &service.CreateTableRequest{
		Database:   "qrdb",
		Name:       "notes",
		Columns:    []service.ColumnDef{{Name: "id", Type: "BIGINT", Nullable: false}},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
	})
	require.NoError(t, err)
	reg := h.Registry.(*sqltest.FakeRegistry)
	inner, err := reg.TransactionServiceFn("", "qrdb")
	require.NoError(t, err)
	reg.TransactionServiceFn = func(string, string) (service.TransactionServicer, error) {
		return &racingTxn{TransactionServicer: inner, prefix: service.TableDataPrefix("qrdb", def.TableID, 0)}, nil
	}

	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithDatabaseName("qrdb"),
		WithServiceRegistry(h.Registry),
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
	require.NoError(t, srv.Start())
	t.Cleanup(srv.Stop)
	cfg := gomysql.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = srv.Addr()
	cfg.User = "qrdb"
	cfg.DBName = "qrdb"
	cfg.AllowNativePasswords = true
	cfg.Timeout = 30 * time.Second
	db, err := sql.Open("mysql", cfg.FormatDSN())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec("INSERT INTO notes (id) VALUES (7)")
	require.Error(t, err)
	var myErr *gomysql.MySQLError
	require.ErrorAs(t, err, &myErr)
	assert.Equal(t, uint16(1062), myErr.Number)
}
