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
	"errors"
	"sync/atomic"
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

func TestMemoizable(t *testing.T) {
	tests := []struct {
		query string
		want  bool
	}{
		{"SELECT 1", true},
		{"  select * from t", true},
		{"(SELECT 1) UNION (SELECT 2)", true},
		{"WITH c AS (SELECT 1) SELECT * FROM c", true},
		{"INSERT INTO t VALUES (1)", true},
		{"update t set v = 1", true},
		{"DELETE FROM t", true},
		{"REPLACE INTO t VALUES (1)", true},
		{"SHOW TABLES", true},
		{"DESCRIBE t", true},
		{"DESC t", true},
		{"EXPLAIN SELECT 1", true},
		{"TABLE t", true},
		{"VALUES ROW(1)", true},
		{"/* plain comment */ SELECT 1", true},
		{"-- note\nSELECT 1", true},
		{"# note\nSELECT 1", true},
		{"CREATE TABLE t (id INT)", false},
		{"ALTER TABLE t ADD COLUMN c INT", false},
		{"DROP TABLE t", false},
		{"TRUNCATE TABLE t", false},
		{"RENAME TABLE a TO b", false},
		{"CREATE INDEX i ON t (c)", false},
		{"CALL p()", false},
		{"EXECUTE s", false},
		{"/*!50000 CREATE TABLE t (id INT) */", false},
		{"/*+ hint */ SELECT 1", false},
		{"/* unterminated", false},
		{"-- unterminated", false},
		{"SELECTED", false},
		{"", false},
		{"   ", false},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			assert.Equal(t, tt.want, memoizable(tt.query))
		})
	}
}

// scopedContext returns a context on a session with a command open, running
// query.
func scopedContext(query string) (*sqle.Context, *QRDBSession) {
	sess := NewSession(sqle.NewBaseSession(), nil)
	_ = sess.CommandBegin()
	return sqle.NewContext(context.Background(), sqle.WithSession(sess), sqle.WithQuery(query)), sess
}

func TestActiveScope(t *testing.T) {
	t.Run("nil context", func(t *testing.T) {
		assert.Nil(t, activeScope(nil))
	})
	t.Run("not a QRDB session", func(t *testing.T) {
		assert.Nil(t, activeScope(sqle.NewContext(context.Background(), sqle.WithQuery("SELECT 1"))))
	})
	t.Run("statement text absent", func(t *testing.T) {
		ctx, _ := scopedContext("")
		assert.Nil(t, activeScope(ctx))
		assert.NotNil(t, activeScope(ctx.WithQuery("SELECT 1")), "an empty-text lookup does not bind the scope")
	})
	t.Run("no command open", func(t *testing.T) {
		sess := NewSession(sqle.NewBaseSession(), nil)
		ctx := sqle.NewContext(context.Background(), sqle.WithSession(sess), sqle.WithQuery("SELECT 1"))
		assert.Nil(t, activeScope(ctx))
	})
	t.Run("memoized statement", func(t *testing.T) {
		ctx, _ := scopedContext("SELECT 1")
		scope := activeScope(ctx)
		require.NotNil(t, scope)
		assert.Same(t, scope, activeScope(ctx), "one scope for the whole statement")
	})
	t.Run("DDL statement", func(t *testing.T) {
		ctx, _ := scopedContext("ALTER TABLE t ADD COLUMN c INT")
		assert.Nil(t, activeScope(ctx))
	})
	t.Run("another statement on the same command", func(t *testing.T) {
		ctx, sess := scopedContext("SELECT 1")
		require.NotNil(t, activeScope(ctx))
		other := sqle.NewContext(context.Background(), sqle.WithSession(sess), sqle.WithQuery("SELECT 2"))
		assert.Nil(t, activeScope(other), "a scope serves only the statement it was opened for")
	})
	t.Run("command ended", func(t *testing.T) {
		ctx, sess := scopedContext("SELECT 1")
		sess.CommandEnd()
		assert.Nil(t, activeScope(ctx))
	})
	t.Run("each command gets a fresh scope", func(t *testing.T) {
		ctx, sess := scopedContext("SELECT 1")
		first := activeScope(ctx)
		sess.CommandEnd()
		require.NoError(t, sess.CommandBegin())
		second := activeScope(ctx)
		require.NotNil(t, second)
		assert.NotSame(t, first, second)
	})
}

func TestMemoRegistry_ResolvesEachServicerOncePerStatement(t *testing.T) {
	reg := &fakeRelationalRegistry{kvSvc: newMemStore(), mvccSvc: newMemStore()}
	d := &Database{name: "shop", registry: reg}
	ctx, _ := scopedContext("SELECT 1")

	for range 3 {
		r := d.registryFor(ctx)
		_, err := r.KVService("acme", "shop")
		require.NoError(t, err)
		_, err = r.MVCCService("acme", "shop")
		require.NoError(t, err)
		_, err = r.TableService("acme", "shop")
		require.NoError(t, err)
		for _, resolve := range []func(string, string) (any, error){
			func(a, b string) (any, error) { return r.TransactionService(a, b) },
			func(a, b string) (any, error) { return r.SequenceService(a, b) },
			func(a, b string) (any, error) { return r.LeaseService(a, b) },
			func(a, b string) (any, error) { return r.WatchService(a, b) },
			func(a, b string) (any, error) { return r.CollectionService(a, b) },
		} {
			_, err := resolve("acme", "shop")
			require.NoError(t, err)
		}
	}
	perKind := map[string]int{}
	for _, call := range reg.calls {
		perKind[call.service]++
	}
	assert.Equal(t, map[string]int{"kv": 1, "mvcc": 1, "table": 1, "collection": 1}, perKind,
		"every servicer the double records is resolved once")

	before := len(reg.calls)
	_, err := d.registryFor(ctx).KVService("acme", "billing")
	require.NoError(t, err)
	assert.Len(t, reg.calls, before+1, "another database is resolved on its own")
}

func TestMemoRegistry_FailedResolutionIsNotMemoized(t *testing.T) {
	sealed := errors.New("barrier sealed")
	reg := &fakeRelationalRegistry{kvErr: sealed}
	d := &Database{name: "shop", registry: reg}
	ctx, _ := scopedContext("SELECT 1")

	_, err := d.registryFor(ctx).KVService("acme", "shop")
	require.ErrorIs(t, err, sealed)
	_, err = d.registryFor(ctx).KVService("acme", "shop")
	require.ErrorIs(t, err, sealed)
	assert.Len(t, reg.calls, 2)
}

func TestRegistryFor_WithoutScopeIsTheRegistry(t *testing.T) {
	reg := &fakeRelationalRegistry{}
	d := &Database{name: "shop", registry: reg}
	ctx, _ := scopedContext("DROP TABLE t")
	assert.Same(t, service.Registry(reg), d.registryFor(ctx))

	none := &Database{name: "shop"}
	sel, _ := scopedContext("SELECT 1")
	assert.Nil(t, none.registryFor(sel))
}

func TestTableDef_MemoizesFoundAndAbsentTables(t *testing.T) {
	ctx, _ := scopedContext("SELECT 1")
	var calls int
	def := &service.TableDef{Name: "t"}
	found := func() (*service.TableDef, error) { calls++; return def, nil }
	for range 3 {
		got, err := tableDef(ctx, "acme", "shop", "t", found)
		require.NoError(t, err)
		assert.Same(t, def, got)
	}
	assert.Equal(t, 1, calls)

	absent := func() (*service.TableDef, error) {
		calls++
		return nil, &service.TableNotFoundError{Database: "shop", Name: "gone"}
	}
	for range 2 {
		_, err := tableDef(ctx, "acme", "shop", "gone", absent)
		var nf *service.TableNotFoundError
		require.ErrorAs(t, err, &nf)
	}
	assert.Equal(t, 2, calls, "an absent table is remembered as absent")

	boom := errors.New("read failed")
	failing := func() (*service.TableDef, error) { calls++; return nil, boom }
	for range 2 {
		_, err := tableDef(ctx, "acme", "shop", "other", failing)
		require.ErrorIs(t, err, boom)
	}
	assert.Equal(t, 4, calls, "a failed read is not memoized")
}

func TestViewDef_MemoizesFoundAndAbsentViews(t *testing.T) {
	ctx, _ := scopedContext("SELECT * FROM v")
	var calls int
	view := &service.ViewDef{Name: "v"}
	for range 2 {
		got, err := viewDef(ctx, "acme", "shop", "v", func() (*service.ViewDef, error) { calls++; return view, nil })
		require.NoError(t, err)
		assert.Same(t, view, got)
	}
	for range 2 {
		_, err := viewDef(ctx, "acme", "shop", "t", func() (*service.ViewDef, error) {
			calls++
			return nil, &service.ViewNotFoundError{Database: "shop", Name: "t"}
		})
		var nf *service.ViewNotFoundError
		require.ErrorAs(t, err, &nf)
	}
	assert.Equal(t, 2, calls, "one lookup per name per statement, found or absent")
}

func TestStatementScope_ViewsResolveThroughTheScope(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	c := commandConn{newTxConn(t, prov, "shop")}
	c.run(t, "CREATE TABLE t (id BIGINT PRIMARY KEY)")
	c.run(t, "INSERT INTO t (id) VALUES (1), (2)")
	c.run(t, "CREATE VIEW v AS SELECT id FROM t WHERE id > 1")

	rows := c.run(t, "SELECT id FROM v")
	require.Len(t, rows, 1)
	rows = c.run(t, "SELECT id FROM t")
	assert.Len(t, rows, 2)

	c.run(t, "DROP VIEW v")
	require.Error(t, c.try("SELECT id FROM v"), "a dropped view is gone for the next statement")
}

func TestTableDef_WithoutScopeAlwaysResolves(t *testing.T) {
	ctx, _ := scopedContext("CREATE TABLE t (id INT)")
	var calls int
	resolve := func() (*service.TableDef, error) { calls++; return &service.TableDef{}, nil }
	for range 3 {
		_, err := tableDef(ctx, "acme", "shop", "t", resolve)
		require.NoError(t, err)
	}
	assert.Equal(t, 3, calls)
}

func TestReferencingForeignKeys_MemoizedPerStatement(t *testing.T) {
	ctx, _ := scopedContext("DELETE FROM parent")
	var calls int
	refs := []service.TableForeignKey{{Table: "child"}}
	resolve := func() ([]service.TableForeignKey, error) { calls++; return refs, nil }
	for range 3 {
		got, err := referencingForeignKeys(ctx, "acme", "shop", "parent", resolve)
		require.NoError(t, err)
		assert.Equal(t, refs, got)
	}
	assert.Equal(t, 1, calls)

	boom := errors.New("scan failed")
	for range 2 {
		_, err := referencingForeignKeys(ctx, "acme", "shop", "other", func() ([]service.TableForeignKey, error) {
			calls++
			return nil, boom
		})
		require.ErrorIs(t, err, boom)
	}
	assert.Equal(t, 3, calls)

	plain, _ := scopedContext("ALTER TABLE parent ADD COLUMN c INT")
	_, err := referencingForeignKeys(plain, "acme", "shop", "parent", resolve)
	require.NoError(t, err)
	assert.Equal(t, 4, calls)
}

// ---------------------------------------------------------------------------
// Through the engine
// ---------------------------------------------------------------------------

// countingTableService counts GetTable calls, delegating everything.
type countingTableService struct {
	service.TableServicer
	gets atomic.Int32
}

func (c *countingTableService) GetTable(ctx context.Context, database, name string) (*service.TableDef, error) {
	c.gets.Add(1)
	return c.TableServicer.GetTable(ctx, database, name)
}

// commandConn runs every statement as one command, the way the MySQL
// wire's handler does.
type commandConn struct{ *txConn }

func (c commandConn) run(t *testing.T, query string) []sqle.Row {
	t.Helper()
	rows, err := c.command(query)
	require.NoError(t, err, query)
	return rows
}

func (c commandConn) try(query string) error {
	_, err := c.command(query)
	return err
}

// command runs query between CommandBegin and CommandEnd on a context that
// carries the statement text, as the MySQL handler builds it.
func (c commandConn) command(query string) ([]sqle.Row, error) {
	if err := c.sess.CommandBegin(); err != nil {
		return nil, err
	}
	defer c.sess.CommandEnd()
	sqlCtx := sqle.NewContext(context.Background(), sqle.WithSession(c.sess), sqle.WithQuery(query))
	sqlCtx.SetCurrentDatabase(c.db)
	_, iter, _, err := c.eng.Query(sqlCtx, query)
	if err != nil {
		return nil, err
	}
	return sqle.RowIterToRows(sqlCtx, iter)
}

// countedHarness is a DDL harness whose registry counts table-catalog reads.
func countedHarness(t *testing.T) (*ddlHarness, *Provider, *countingTableService) {
	t.Helper()
	h := newDDLHarness()
	counting := &countingTableService{TableServicer: h.tableSvc}
	h.registry.(*fakeRelationalRegistry).tableSvcFn = func(string, string) (service.TableServicer, error) {
		return counting, nil
	}
	return h, h.newDDLProvider("shop"), counting
}

func TestStatementScope_OneResolutionPerStatement(t *testing.T) {
	h, prov, counting := countedHarness(t)
	c := commandConn{newTxConn(t, prov, "shop")}
	c.run(t, "CREATE TABLE t (id BIGINT PRIMARY KEY, v VARCHAR(16))")
	c.run(t, "INSERT INTO t (id, v) VALUES (1, 'a'), (2, 'b')")
	reg := h.registry.(*fakeRelationalRegistry)

	for _, stmt := range []string{
		"SELECT v FROM t WHERE id = 1",
		"UPDATE t SET v = 'c' WHERE id = 1",
		"DELETE FROM t WHERE id = 2",
		"SELECT TABLE_ROWS FROM information_schema.tables WHERE TABLE_SCHEMA = 'shop'",
	} {
		counting.gets.Store(0)
		reg.mu.Lock()
		reg.calls = nil
		reg.mu.Unlock()

		c.run(t, stmt)

		assert.Equal(t, int32(1), counting.gets.Load(), "%s reads the definition once", stmt)
		reg.mu.Lock()
		perKind := map[string]int{}
		for _, call := range reg.calls {
			perKind[call.service]++
		}
		reg.mu.Unlock()
		for kind, n := range perKind {
			assert.Equal(t, 1, n, "%s resolves %s once", stmt, kind)
		}
	}
}

func TestStatementScope_DDLFromAnotherSessionIsSeenByTheNextStatement(t *testing.T) {
	_, prov, _ := countedHarness(t)
	a := commandConn{newTxConn(t, prov, "shop")}
	b := commandConn{newTxConn(t, prov, "shop")}
	a.run(t, "CREATE TABLE t (id BIGINT PRIMARY KEY)")
	a.run(t, "INSERT INTO t (id) VALUES (1)")

	require.Error(t, a.try("SELECT c FROM t"))
	b.run(t, "ALTER TABLE t ADD COLUMN c VARCHAR(8)")
	rows := a.run(t, "SELECT id, c FROM t")
	require.Len(t, rows, 1)

	require.Error(t, a.try("SELECT * FROM u"), "no such table yet")
	b.run(t, "CREATE TABLE u (id BIGINT PRIMARY KEY)")
	a.run(t, "SELECT * FROM u")

	b.run(t, "DROP TABLE u")
	require.Error(t, a.try("SELECT * FROM u"), "a dropped table is gone for the next statement")
}

func TestStatementScope_DDLSeesItsOwnChanges(t *testing.T) {
	_, prov, counting := countedHarness(t)
	c := commandConn{newTxConn(t, prov, "shop")}
	c.run(t, "CREATE TABLE p (id BIGINT PRIMARY KEY)")
	counting.gets.Store(0)
	c.run(t, "CREATE TABLE ch (id BIGINT PRIMARY KEY, pid BIGINT, CONSTRAINT fk FOREIGN KEY (pid) REFERENCES p (id))")
	assert.Greater(t, counting.gets.Load(), int32(1), "DDL re-reads the catalog rather than serving a memoized definition")

	c.run(t, "INSERT INTO p (id) VALUES (1)")
	c.run(t, "INSERT INTO ch (id, pid) VALUES (1, 1)")
	require.Error(t, c.try("INSERT INTO ch (id, pid) VALUES (2, 9)"), "the foreign key created by DDL is enforced")
}
