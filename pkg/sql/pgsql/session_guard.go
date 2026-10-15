// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
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
	"net"
	"sync"

	wire "github.com/jeroenrinzema/psql-wire"
	psqlerr "github.com/jeroenrinzema/psql-wire/errors"

	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sessionguard"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// trackingListener remembers every accepted connection by its remote address,
// so the seal guard can close the connection of a session psql-wire hands it
// only as a context. A connection forgets itself when it closes.
type trackingListener struct {
	net.Listener
	conns sync.Map // remote address -> *trackedConn
}

// Accept implements net.Listener.
func (l *trackingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	tc := &trackedConn{Conn: c, l: l, key: c.RemoteAddr().String()}
	l.conns.Store(tc.key, tc)
	return tc, nil
}

// closeConn closes the connection accepted from addr, if it is still open.
func (l *trackingListener) closeConn(addr net.Addr) {
	if addr == nil {
		return
	}
	if v, ok := l.conns.Load(addr.String()); ok {
		_ = v.(*trackedConn).Close() //nolint:errcheck // type assertion on a value this map only ever holds
	}
}

// trackedConn is an accepted connection that forgets itself when closed.
type trackedConn struct {
	net.Conn
	l    *trackingListener
	key  string
	once sync.Once
}

// Close implements net.Conn.
func (c *trackedConn) Close() error {
	c.once.Do(func() { c.l.conns.Delete(c.key) })
	return c.Conn.Close()
}

// guardSessionKey is the context key of a connection's seal guard session.
type guardSessionKey struct{}

// guardSessionHandler registers each session's connection with the seal
// guard, which closes it when a seal reaches it -- the node probe closing, or
// a cluster seal of the node, the session's tenant or any of its databases,
// or a cut-off. Closing is what ends a transaction held across the seal and
// any statement still streaming. It runs after authentication, so it knows
// the session's tenant, and refuses a session while the node is sealed.
func guardSessionHandler(guard *sessionguard.Guard, l *trackingListener) wire.SessionHandler {
	return func(ctx context.Context) (context.Context, error) {
		tenantID := tenantid.System
		if id := IdentityFromContext(ctx); id != nil {
			tenantID = tenantid.Normalize(id.TenantID)
		}
		addr := wire.RemoteAddress(ctx)
		sess, err := guard.Open(tenantID, func() { l.closeConn(addr) })
		if err != nil {
			return ctx, wireRefusal(err, psqlerr.LevelFatal)
		}
		return context.WithValue(ctx, guardSessionKey{}, sess), nil
	}
}

// releaseGuardSession forgets the connection's seal guard session, if any.
func releaseGuardSession(ctx context.Context) {
	if sess, ok := ctx.Value(guardSessionKey{}).(*sessionguard.Session); ok {
		sess.Release()
	}
}
