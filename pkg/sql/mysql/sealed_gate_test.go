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
	"errors"
	"testing"
	"time"

	"github.com/dolthub/go-mysql-server/server"
	"github.com/dolthub/vitess/go/mysql"
	"github.com/dolthub/vitess/go/sqltypes"
	querypb "github.com/dolthub/vitess/go/vt/proto/query"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sessionguard"
)

// countingChain records which chain entry the gate passed a statement to.
type countingChain struct{ calls []string }

func (c *countingChain) ComQuery(context.Context, *mysql.Conn, string, mysql.ResultSpoolFn) error {
	c.calls = append(c.calls, "query")
	return nil
}

func (c *countingChain) ComMultiQuery(context.Context, *mysql.Conn, string, mysql.ResultSpoolFn) (string, error) {
	c.calls = append(c.calls, "multi")
	return "", nil
}

func (c *countingChain) ComPrepare(context.Context, *mysql.Conn, string, *mysql.PrepareData) ([]*querypb.Field, error) {
	c.calls = append(c.calls, "prepare")
	return nil, nil
}

func (c *countingChain) ComStmtExecute(context.Context, *mysql.Conn, *mysql.PrepareData, func(*sqltypes.Result) error) error {
	c.calls = append(c.calls, "execute")
	return nil
}

// TestSealedQueryGate_EveryEntryPoint pins every statement entry point of the
// per-query gate: closed, each is refused with the sealed refusal and nothing
// reaches the chain; open, each passes straight through.
func TestSealedQueryGate_EveryEntryPoint(t *testing.T) {
	probe := &fakeSealedProbe{}
	gate := sealedQueryGate{probe: probe}
	ctx := context.Background()
	noRows := func(*sqltypes.Result, bool) error { return nil }
	entries := map[string]func(c *countingChain) error{
		"query":   func(c *countingChain) error { return gate.Query(ctx, c, nil, "SELECT 1", noRows) },
		"parsed":  func(c *countingChain) error { return gate.ParsedQuery(c, nil, "SELECT 1", nil, noRows) },
		"multi":   func(c *countingChain) error { _, err := gate.MultiQuery(ctx, c, nil, "SELECT 1", noRows); return err },
		"prepare": func(c *countingChain) error { _, err := gate.Prepare(ctx, c, nil, "SELECT ?", nil); return err },
		"execute": func(c *countingChain) error {
			return gate.StmtExecute(ctx, c, nil, nil, func(*sqltypes.Result) error { return nil })
		},
	}
	for name, call := range entries {
		probe.sealed.Store(true)
		c := &countingChain{}
		var sealed *BarrierSealedError
		if err := call(c); !errors.As(err, &sealed) || len(c.calls) != 0 {
			t.Errorf("%s while sealed: err = %v, chain calls %v; want the sealed refusal and none", name, err, c.calls)
		}
		probe.sealed.Store(false)
		if err := call(c); err != nil || len(c.calls) != 1 {
			t.Errorf("%s while serving: err = %v, chain calls %v; want one pass-through", name, err, c.calls)
		}
	}
	if gate.Priority() != 0 {
		t.Errorf("Priority = %d, want 0: the gate runs before any other interceptor", gate.Priority())
	}
}

// initDBHandler is the inner mysql.Handler the composed gate wraps: it
// records each COM_INIT_DB and answers it as GMS does, refusing a database
// that does not exist -- the answer that would tell a sealed node's caller
// which databases exist.
type initDBHandler struct {
	mysql.Handler
	initDB []string
}

func (h *initDBHandler) ComInitDB(_ *mysql.Conn, schemaName string) error {
	h.initDB = append(h.initDB, schemaName)
	if schemaName != "ledger" {
		return errors.New("database not found: " + schemaName)
	}
	return nil
}

// composeSealedHandler applies the server's sealed-window options to inner,
// as server.NewServer does.
func composeSealedHandler(probe sealedWindowProbe, inner mysql.Handler) mysql.Handler {
	h := inner
	for _, opt := range handlerOptions(nil, probe, nil, 0) {
		_, _, h = opt(nil, nil, h)
	}
	return h
}

// TestSealedWindow_ComInitDBRefusedWhileSealed pins COM_INIT_DB to the gate:
// while sealed, selecting an existing database and a missing one get the same
// sealed refusal and neither reaches the engine, so the refusal says nothing
// about which databases exist; once open, both reach it again.
func TestSealedWindow_ComInitDBRefusedWhileSealed(t *testing.T) {
	probe := &fakeSealedProbe{}
	probe.sealed.Store(true)
	inner := &initDBHandler{}
	h := composeSealedHandler(probe, inner)
	var sealed *BarrierSealedError
	for _, db := range []string{"ledger", "missing"} {
		if err := h.ComInitDB(nil, db); !errors.As(err, &sealed) {
			t.Fatalf("COM_INIT_DB %q while sealed: err = %v, want *BarrierSealedError", db, err)
		}
	}
	if len(inner.initDB) != 0 {
		t.Fatalf("COM_INIT_DB reached the engine while sealed: %v", inner.initDB)
	}
	probe.sealed.Store(false)
	if err := h.ComInitDB(nil, "ledger"); err != nil {
		t.Fatalf("COM_INIT_DB while open: %v", err)
	}
	if len(inner.initDB) != 1 {
		t.Fatalf("COM_INIT_DB calls reaching the engine = %v, want one", inner.initDB)
	}
}

// TestQueryTimeoutInterceptor_EveryEntryPoint: every chain entry point runs
// under the timeout -- an expiry answers ER_QUERY_TIMEOUT, any other error
// passes through unchanged -- and the interceptor runs after the gate.
func TestQueryTimeoutInterceptor_EveryEntryPoint(t *testing.T) {
	q := queryTimeoutInterceptor{timeout: time.Millisecond}
	if q.Priority() <= (sealedQueryGate{}).Priority() {
		t.Fatal("the timeout must run after the sealed-window gate")
	}
	expire := &blockingChain{}
	var sqlErr *mysql.SQLError
	entries := map[string]func(server.Chain) error{
		"query": func(c server.Chain) error {
			return q.Query(context.Background(), c, nil, "q", nil)
		},
		"parsed": func(c server.Chain) error {
			return q.ParsedQuery(c, nil, "q", nil, nil)
		},
		"multi": func(c server.Chain) error {
			_, err := q.MultiQuery(context.Background(), c, nil, "q", nil)
			return err
		},
		"prepare": func(c server.Chain) error {
			_, err := q.Prepare(context.Background(), c, nil, "q", nil)
			return err
		},
		"execute": func(c server.Chain) error {
			return q.StmtExecute(context.Background(), c, nil, nil, nil)
		},
	}
	boom := errors.New("boom")
	for name, call := range entries {
		if err := call(expire); !errors.As(err, &sqlErr) || sqlErr.Number() != mysql.ERQueryTimeout {
			t.Errorf("%s past the timeout: err = %v, want ER_QUERY_TIMEOUT", name, err)
		}
		if err := call(&blockingChain{err: boom}); !errors.Is(err, boom) {
			t.Errorf("%s failing on its own: err = %v, want it unchanged", name, err)
		}
	}
	if (&QueryTimeoutError{Timeout: time.Second}).Error() == "" {
		t.Fatal("empty QueryTimeoutError message")
	}
}

// blockingChain blocks every entry point until its context ends and then
// reports that, or fails at once with err.
type blockingChain struct{ err error }

func (b *blockingChain) wait(ctx context.Context) error {
	if b.err != nil {
		return b.err
	}
	<-ctx.Done()
	return ctx.Err()
}

func (b *blockingChain) ComQuery(ctx context.Context, _ *mysql.Conn, _ string, _ mysql.ResultSpoolFn) error {
	return b.wait(ctx)
}

func (b *blockingChain) ComMultiQuery(ctx context.Context, _ *mysql.Conn, _ string, _ mysql.ResultSpoolFn) (string, error) {
	return "", b.wait(ctx)
}

func (b *blockingChain) ComPrepare(ctx context.Context, _ *mysql.Conn, _ string, _ *mysql.PrepareData) ([]*querypb.Field, error) {
	return nil, b.wait(ctx)
}

func (b *blockingChain) ComStmtExecute(ctx context.Context, _ *mysql.Conn, _ *mysql.PrepareData, _ func(*sqltypes.Result) error) error {
	return b.wait(ctx)
}

// TestSealedSessions_RebuiltSessionDoesNotLeak: COM_RESET_CONNECTION rebuilds
// a connection's session (GMS SessionManager.NewSession calls the builder
// again) under the same connection ID; the replaced guard session is
// released, so closing the connection leaves none behind.
func TestSealedSessions_RebuiltSessionDoesNotLeak(t *testing.T) {
	streams := clusterseal.NewStreams()
	g := sessionguard.New(nil, streams, 0)
	g.Start()
	defer g.Stop()
	s := &sealedSessions{guard: g}
	c := &mysql.Conn{ConnectionID: 7}
	if err := s.openConn(c, "acme"); err != nil {
		t.Fatal(err)
	}
	if err := s.openConn(c, "acme"); err != nil { // COM_RESET_CONNECTION
		t.Fatal(err)
	}
	if g.Len() != 1 || streams.Len() != 1 {
		t.Fatalf("after the reset: %d guard session(s), %d subscription(s); want 1 and 1", g.Len(), streams.Len())
	}
	s.release(7) // ConnectionClosed
	if g.Len() != 0 || streams.Len() != 0 {
		t.Fatalf("after close: %d guard session(s), %d subscription(s) outlive the connection", g.Len(), streams.Len())
	}
}
