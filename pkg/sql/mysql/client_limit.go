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

	"github.com/dolthub/go-mysql-server/server"
	"github.com/dolthub/vitess/go/mysql"
	"github.com/dolthub/vitess/go/sqltypes"
	querypb "github.com/dolthub/vitess/go/vt/proto/query"
	"github.com/dolthub/vitess/go/vt/sqlparser"

	"github.com/jeremyhahn/go-qrdb/pkg/clientlimit"
)

// WithClientLimiter charges every connection and every statement on this
// listener to the client-address bucket of l, before authentication. A
// refusal reaches the client as an ERR packet carrying MySQL's
// ER_USER_LIMIT_REACHED (error 1226, SQLSTATE 42000), the error MySQL itself
// returns when an account exceeds a resource limit such as
// MAX_CONNECTIONS_PER_HOUR or MAX_QUERIES_PER_HOUR. A nil l limits nothing.
func WithClientLimiter(l *clientlimit.Limiter) ServerOption {
	return func(s *Server) {
		s.clientLimit = l
	}
}

// clientLimitRefusal translates the limiter's refusal into the ERR packet
// the MySQL protocol defines for an exceeded resource limit. Any other error
// passes through unchanged.
func clientLimitRefusal(err error) error {
	var limited *clientlimit.RateLimitedError
	if !errors.As(err, &limited) {
		return err
	}
	return mysql.NewSQLError(mysql.ERUserLimitReached, mysql.SSClientError, "%s", limited.Error())
}

// admitClient charges one request from the connection's peer address. The
// MySQL wire carries no forwarding header, so the bucket is keyed by the
// peer address of the accepted connection.
func admitClient(l *clientlimit.Limiter, c *mysql.Conn) error {
	if l == nil {
		return nil
	}
	return clientLimitRefusal(l.Admit(c.RemoteAddr(), nil))
}

// clientLimitGate charges every statement a connection sends -- a query, a
// prepare and each execution of a prepared statement -- to its client
// address's bucket.
type clientLimitGate struct {
	limiter *clientlimit.Limiter
}

// Priority runs the gate before every other interceptor: a refused statement
// does no work at all.
func (clientLimitGate) Priority() int { return -1 }

func (g clientLimitGate) Query(ctx context.Context, chain server.Chain, c *mysql.Conn, query string, callback func(*sqltypes.Result, bool) error) error {
	if err := admitClient(g.limiter, c); err != nil {
		return err
	}
	return chain.ComQuery(ctx, c, query, callback)
}

// ParsedQuery is part of server.Interceptor, but the interceptor handler
// routes no parsed queries through the chain; the gate still charges first.
func (g clientLimitGate) ParsedQuery(chain server.Chain, c *mysql.Conn, query string, _ sqlparser.Statement, callback func(*sqltypes.Result, bool) error) error {
	if err := admitClient(g.limiter, c); err != nil {
		return err
	}
	return chain.ComQuery(context.Background(), c, query, callback)
}

func (g clientLimitGate) MultiQuery(ctx context.Context, chain server.Chain, c *mysql.Conn, query string, callback func(*sqltypes.Result, bool) error) (string, error) {
	if err := admitClient(g.limiter, c); err != nil {
		return "", err
	}
	return chain.ComMultiQuery(ctx, c, query, callback)
}

func (g clientLimitGate) Prepare(ctx context.Context, chain server.Chain, c *mysql.Conn, query string, prepare *mysql.PrepareData) ([]*querypb.Field, error) {
	if err := admitClient(g.limiter, c); err != nil {
		return nil, err
	}
	return chain.ComPrepare(ctx, c, query, prepare)
}

func (g clientLimitGate) StmtExecute(ctx context.Context, chain server.Chain, c *mysql.Conn, prepare *mysql.PrepareData, callback func(*sqltypes.Result) error) error {
	if err := admitClient(g.limiter, c); err != nil {
		return err
	}
	return chain.ComStmtExecute(ctx, c, prepare, callback)
}
