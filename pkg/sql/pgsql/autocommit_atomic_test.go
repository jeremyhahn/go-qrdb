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

//go:build qrdb_pgsql

package pgsql

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sqltest"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/provider"
)

// TestPGWire_AutocommitStatementIsAtomic: a multi-row INSERT outside a
// transaction whose third row is a duplicate leaves its first two rows
// absent -- a statement is its own transaction, as in PostgreSQL.
func TestPGWire_AutocommitStatementIsAtomic(t *testing.T) {
	hs, closeFn, err := sqltest.NewLockingHarness()
	require.NoError(t, err)
	t.Cleanup(closeFn)
	prov := provider.NewProviderWithEngine("qrdb", hs.KVSvc, "", provider.WithRegistry(hs.Registry))
	eng := engine.New(engine.Config{Provider: prov, Dialect: engine.DialectPostgreSQL})
	t.Cleanup(func() { _ = eng.Close() })
	h := NewParseHandler(eng, engine.NewTranslator(), "qrdb")

	ctx, err := newConnectionSessionHandler()(context.Background())
	require.NoError(t, err)
	run := func(q string) error {
		_, err := h.HandleParse(ctx, q)
		return err
	}

	require.NoError(t, run("CREATE TABLE notes (id BIGINT PRIMARY KEY, body VARCHAR(64))"))
	require.NoError(t, run("INSERT INTO notes (id, body) VALUES (1, 'a')"))
	require.Error(t, run("INSERT INTO notes (id, body) VALUES (10, 'x'), (11, 'y'), (1, 'dup')"), "row 3 duplicates id 1")

	_, rows := runSQL(t, eng, "qrdb", "", "SELECT COUNT(*) FROM notes WHERE id IN (10, 11)")
	require.Len(t, rows, 1)
	assert.EqualValues(t, 0, rows[0][0], "rows 1 and 2 of the failed statement must not be committed")
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

// TestPGWire_CommitFailureIsReported: an autocommit statement commits when
// its result is closed, and a commit that fails must fail the statement.
func TestPGWire_CommitFailureIsReported(t *testing.T) {
	hs := sqltest.NewHarness()
	def, err := hs.TableSvc.CreateTable(context.Background(), &service.CreateTableRequest{
		Database:   "qrdb",
		Name:       "notes",
		Columns:    []service.ColumnDef{{Name: "id", Type: "BIGINT", Nullable: false}},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
	})
	require.NoError(t, err)
	reg := hs.Registry.(*sqltest.FakeRegistry)
	inner, err := reg.TransactionServiceFn("", "qrdb")
	require.NoError(t, err)
	reg.TransactionServiceFn = func(string, string) (service.TransactionServicer, error) {
		return &racingTxn{TransactionServicer: inner, prefix: service.TableDataPrefix("qrdb", def.TableID, 0)}, nil
	}
	prov := provider.NewProviderWithEngine("qrdb", hs.KVSvc, "", provider.WithRegistry(hs.Registry))
	eng := engine.New(engine.Config{Provider: prov, Dialect: engine.DialectPostgreSQL})
	t.Cleanup(func() { _ = eng.Close() })
	h := NewParseHandler(eng, engine.NewTranslator(), "qrdb")
	ctx, err := newConnectionSessionHandler()(context.Background())
	require.NoError(t, err)

	_, err = h.HandleParse(ctx, "INSERT INTO notes (id) VALUES (7)")
	require.Error(t, err, "the statement's commit lost to a concurrent insert of the same key")
}
