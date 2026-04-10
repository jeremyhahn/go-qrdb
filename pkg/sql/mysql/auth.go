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

//go:build qrdb_mysql

package mysql

import (
	"context"
	"crypto/tls"

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/vitess/go/mysql"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/provider"
)

// sessionBuilder creates a QRDBSession for each MySQL connection by
// extracting the TLS connection state and running it through the auth
// middleware chain.
type sessionBuilder struct {
	authMW *authmw.AuthMiddleware
}

// newSessionBuilder creates a session builder with auth middleware.
func newSessionBuilder(authMW *authmw.AuthMiddleware) *sessionBuilder {
	return &sessionBuilder{authMW: authMW}
}

// Build creates a QRDBSession for a MySQL connection. It extracts the TLS
// state from the underlying connection, authenticates via the auth
// middleware chain, and returns a QRDBSession scoped to the authenticated
// tenant. When no auth middleware is configured, a default session with
// nil identity is returned.
func (b *sessionBuilder) Build(ctx context.Context, conn *mysql.Conn, addr string) (sql.Session, error) {
	base := sql.NewBaseSessionWithClientServer(addr, sql.Client{
		Address: addr,
		User:    conn.User,
	}, conn.ConnectionID)

	// No auth middleware: return a default tenant session.
	if b.authMW == nil {
		return provider.NewSession(base, nil), nil
	}

	// Extract TLS connection state from the underlying net.Conn.
	tlsConn, ok := conn.Conn.(*tls.Conn)
	if !ok {
		return nil, &AuthenticationError{
			Err: auth.ErrNoPeerCertificate,
		}
	}
	state := tlsConn.ConnectionState()
	ctx = auth.WithTLSState(ctx, &state)

	// Run the auth middleware chain to authenticate and enrich identity.
	_, identity, err := b.authMW.Authenticate(ctx)
	if err != nil {
		return nil, &AuthenticationError{Err: err}
	}

	return provider.NewSession(base, identity), nil
}
