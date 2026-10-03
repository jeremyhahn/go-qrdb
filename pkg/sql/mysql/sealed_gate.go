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
	"sync"
	"time"

	gms "github.com/dolthub/go-mysql-server"
	"github.com/dolthub/go-mysql-server/server"
	"github.com/dolthub/vitess/go/mysql"
	"github.com/dolthub/vitess/go/sqltypes"
	querypb "github.com/dolthub/vitess/go/vt/proto/query"
	"github.com/dolthub/vitess/go/vt/sqlparser"

	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sessionguard"
)

// sealedQueryGate refuses every query on an open session while the probe
// reports sealed -- the per-query half of the sealed-window gate, as pgsql
// has it. The session builder refuses new sessions; this refuses the next
// statement of a session established while the node still served, so a
// node whose barrier seals, or whom a cluster seal holds, cuts off or has
// not yet confirmed, stops serving its open connections too.
type sealedQueryGate struct {
	probe sealedWindowProbe
}

var _ server.Interceptor = sealedQueryGate{}

// refuse returns the sealed refusal while the probe is closed.
func (g sealedQueryGate) refuse() error {
	if g.probe != nil && g.probe.IsSealed() {
		return &BarrierSealedError{}
	}
	return nil
}

// Priority runs the gate before any other interceptor.
func (sealedQueryGate) Priority() int { return 0 }

func (g sealedQueryGate) Query(ctx context.Context, chain server.Chain, c *mysql.Conn, query string, callback func(*sqltypes.Result, bool) error) error {
	if err := g.refuse(); err != nil {
		return err
	}
	return chain.ComQuery(ctx, c, query, callback)
}

// ParsedQuery is part of server.Interceptor, but the interceptor handler
// routes no parsed queries through the chain (it has no ComParsedQuery); the
// gate still refuses first, then runs the query text through the chain.
func (g sealedQueryGate) ParsedQuery(chain server.Chain, c *mysql.Conn, query string, _ sqlparser.Statement, callback func(*sqltypes.Result, bool) error) error {
	if err := g.refuse(); err != nil {
		return err
	}
	return chain.ComQuery(context.Background(), c, query, callback)
}

func (g sealedQueryGate) MultiQuery(ctx context.Context, chain server.Chain, c *mysql.Conn, query string, callback func(*sqltypes.Result, bool) error) (string, error) {
	if err := g.refuse(); err != nil {
		return "", err
	}
	return chain.ComMultiQuery(ctx, c, query, callback)
}

func (g sealedQueryGate) Prepare(ctx context.Context, chain server.Chain, c *mysql.Conn, query string, prepare *mysql.PrepareData) ([]*querypb.Field, error) {
	if err := g.refuse(); err != nil {
		return nil, err
	}
	return chain.ComPrepare(ctx, c, query, prepare)
}

func (g sealedQueryGate) StmtExecute(ctx context.Context, chain server.Chain, c *mysql.Conn, prepare *mysql.PrepareData, callback func(*sqltypes.Result) error) error {
	if err := g.refuse(); err != nil {
		return err
	}
	return chain.ComStmtExecute(ctx, c, prepare, callback)
}

// handlerOptions composes the server's statement handling into the
// go-mysql-server handler, each part only when configured: the sealed-window
// gate (probe) refuses every query, prepare and execute; the query timeout
// bounds each; sealedConnHandler refuses COM_INIT_DB, which no interceptor
// sees, and releases each connection's seal guard session when it closes.
func handlerOptions(probe sealedWindowProbe, sessions *sealedSessions, queryTimeout time.Duration) []server.Option {
	var chain server.InterceptorChain
	interceptors := 0
	if probe != nil {
		chain.WithInterceptor(sealedQueryGate{probe: probe})
		interceptors++
	}
	if queryTimeout > 0 {
		chain.WithInterceptor(queryTimeoutInterceptor{timeout: queryTimeout})
		interceptors++
	}
	var opts []server.Option
	if interceptors > 0 {
		opts = append(opts, chain.Option())
	}
	if probe != nil || sessions != nil {
		opts = append(opts, func(e *gms.Engine, sm *server.SessionManager, h mysql.Handler) (*gms.Engine, *server.SessionManager, mysql.Handler) {
			return e, sm, sealedConnHandler{Handler: h, gate: sealedQueryGate{probe: probe}, sessions: sessions}
		})
	}
	return opts
}

// queryTimeoutInterceptor bounds every statement to timeout: the statement
// runs under a context that expires then, and one that does is answered
// ER_QUERY_TIMEOUT (3024), the code MySQL's own max_execution_time raises
// and clients already classify.
type queryTimeoutInterceptor struct {
	timeout time.Duration
}

var _ server.Interceptor = queryTimeoutInterceptor{}

// Priority runs the timeout after the sealed-window gate.
func (queryTimeoutInterceptor) Priority() int { return 1 }

// bounded runs fn under the timeout and maps an expiry to ER_QUERY_TIMEOUT.
func (q queryTimeoutInterceptor) bounded(ctx context.Context, fn func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, q.timeout)
	defer cancel()
	err := fn(ctx)
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return mysql.NewSQLError(mysql.ERQueryTimeout, mysql.SSUnknownSQLState,
			"%s", (&QueryTimeoutError{Timeout: q.timeout}).Error())
	}
	return err
}

func (q queryTimeoutInterceptor) Query(ctx context.Context, chain server.Chain, c *mysql.Conn, query string, callback func(*sqltypes.Result, bool) error) error {
	return q.bounded(ctx, func(ctx context.Context) error { return chain.ComQuery(ctx, c, query, callback) })
}

// ParsedQuery is part of server.Interceptor; the interceptor handler routes
// no parsed queries through the chain, but the timeout still applies.
func (q queryTimeoutInterceptor) ParsedQuery(chain server.Chain, c *mysql.Conn, query string, _ sqlparser.Statement, callback func(*sqltypes.Result, bool) error) error {
	return q.bounded(context.Background(), func(ctx context.Context) error { return chain.ComQuery(ctx, c, query, callback) })
}

func (q queryTimeoutInterceptor) MultiQuery(ctx context.Context, chain server.Chain, c *mysql.Conn, query string, callback func(*sqltypes.Result, bool) error) (string, error) {
	var rest string
	err := q.bounded(ctx, func(ctx context.Context) error {
		var err error
		rest, err = chain.ComMultiQuery(ctx, c, query, callback)
		return err
	})
	return rest, err
}

func (q queryTimeoutInterceptor) Prepare(ctx context.Context, chain server.Chain, c *mysql.Conn, query string, prepare *mysql.PrepareData) ([]*querypb.Field, error) {
	var fields []*querypb.Field
	err := q.bounded(ctx, func(ctx context.Context) error {
		var err error
		fields, err = chain.ComPrepare(ctx, c, query, prepare)
		return err
	})
	return fields, err
}

func (q queryTimeoutInterceptor) StmtExecute(ctx context.Context, chain server.Chain, c *mysql.Conn, prepare *mysql.PrepareData, callback func(*sqltypes.Result) error) error {
	return q.bounded(ctx, func(ctx context.Context) error { return chain.ComStmtExecute(ctx, c, prepare, callback) })
}

// sealedConnHandler refuses COM_INIT_DB (the protocol's USE) while the
// probe is closed -- answered by the engine, it would tell the caller whether
// the named database exists, which a sealed node must not -- and releases a
// connection's seal guard session when it closes.
//
// It wraps the handler in a struct that embeds only mysql.Handler, so any
// OPTIONAL interface the wrapped handler implements and vitess discovers by
// type assertion is hidden from vitess: today that is
// mysql.BinlogReplicaHandler (COM_REGISTER_REPLICA / COM_BINLOG_DUMP*), which
// this server does not serve -- the replication protocol is not a qrdb
// surface, and vitess answers those commands as unsupported. A future option
// that needs one must be forwarded here explicitly, not assumed.
type sealedConnHandler struct {
	mysql.Handler
	gate     sealedQueryGate
	sessions *sealedSessions
}

// ComInitDB refuses while sealed and otherwise selects the database.
func (h sealedConnHandler) ComInitDB(c *mysql.Conn, schemaName string) error {
	if err := h.gate.refuse(); err != nil {
		return err
	}
	return h.Handler.ComInitDB(c, schemaName)
}

// ConnectionClosed releases the connection's seal guard session, then lets
// the wrapped handler close it.
func (h sealedConnHandler) ConnectionClosed(c *mysql.Conn) {
	h.sessions.release(c.ConnectionID)
	h.Handler.ConnectionClosed(c)
}

// sealedSessions maps each open MySQL connection to its seal guard session
// (sessionguard.Session): the guard closes the connection when a seal reaches
// it, which is the only thing that ends a server-side cursor -- vitess answers
// COM_STMT_FETCH itself, so no statement gate sees it -- or a transaction
// held across the seal.
type sealedSessions struct {
	guard *sessionguard.Guard
	open  sync.Map // connection ID (uint32) -> *sessionguard.Session
}

// open registers conn, of tenantID, with the guard. While the node is sealed
// it refuses with *BarrierSealedError.
func (s *sealedSessions) openConn(conn *mysql.Conn, tenantID string) error {
	if s == nil {
		return nil
	}
	sess, err := s.guard.Open(tenantID, conn.Close)
	if err != nil {
		var sealed *sessionguard.SealedError
		if errors.As(err, &sealed) {
			return &BarrierSealedError{}
		}
		return err
	}
	// COM_RESET_CONNECTION rebuilds the session under the same connection
	// ID: the session it replaces is released, not leaked.
	if old, loaded := s.open.Swap(conn.ConnectionID, sess); loaded {
		old.(*sessionguard.Session).Release() //nolint:errcheck // type assertion on a value this map only ever holds
	}
	return nil
}

// release forgets connection id's guard session, if it has one.
func (s *sealedSessions) release(id uint32) {
	if s == nil {
		return
	}
	if v, ok := s.open.LoadAndDelete(id); ok {
		v.(*sessionguard.Session).Release() //nolint:errcheck // type assertion on a value this map only ever holds
	}
}
