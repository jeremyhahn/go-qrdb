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
	"crypto/tls"
	"net"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// identityContextKey is used to store the auth identity in the connection
// context for retrieval by the ParseFn and downstream query handlers.
type identityContextKey struct{}

// Authenticator performs mTLS authentication for PostgreSQL wire connections.
// It bridges between psql-wire's connection model and QRDB's auth middleware
// chain. Because psql-wire does not expose TLS connection state directly in
// its callbacks, the Authenticator maintains a thread-safe map of TLS states
// keyed by remote address, populated via a TLS VerifyConnection callback.
type Authenticator struct {
	authMW   *authmw.AuthMiddleware
	tlsState sync.Map // map[string]*tls.ConnectionState keyed by remote addr
}

// NewAuthenticator creates a new PG authenticator backed by the given
// auth middleware chain.
func NewAuthenticator(authMW *authmw.AuthMiddleware) *Authenticator {
	return &Authenticator{
		authMW: authMW,
	}
}

// StoreTLSState records the TLS connection state for the given remote
// address. This is called from the TLS VerifyConnection callback during
// TLS handshake, before the AuthStrategy or SessionHandler runs.
func (a *Authenticator) StoreTLSState(remoteAddr net.Addr, state *tls.ConnectionState) {
	a.tlsState.Store(remoteAddr.String(), state)
}

// ClearTLSState removes the TLS state for the given remote address.
// Called during connection cleanup to prevent memory leaks.
func (a *Authenticator) ClearTLSState(remoteAddr string) {
	a.tlsState.Delete(remoteAddr)
}

// Authenticate extracts the TLS state previously stored for this connection,
// runs it through the auth middleware chain, and stores the resulting
// identity in the context for later retrieval by the ParseFn.
//
// If authMW is nil (no auth configured), the context is returned as-is
// with no identity, allowing unauthenticated access.
func (a *Authenticator) Authenticate(ctx context.Context, remoteAddr net.Addr) (context.Context, error) {
	if a.authMW == nil {
		return ctx, nil
	}

	// Look up the TLS state stored during the TLS handshake.
	val, ok := a.tlsState.Load(remoteAddr.String())
	if !ok {
		return ctx, &AuthenticationError{Err: auth.ErrNoPeerCertificate}
	}

	tlsState, ok := val.(*tls.ConnectionState)
	if !ok || tlsState == nil {
		return ctx, &AuthenticationError{Err: auth.ErrNoPeerCertificate}
	}

	// Inject TLS state into context for the authenticator chain.
	ctx = auth.WithTLSState(ctx, tlsState)

	// Run the full authenticator chain (mTLS, user enrichment, etc.).
	ctx, identity, err := a.authMW.Authenticate(ctx)
	if err != nil {
		return ctx, &AuthenticationError{Err: err}
	}

	// Store identity in our package-specific context key for ParseFn access.
	if identity != nil {
		ctx = context.WithValue(ctx, identityContextKey{}, identity)
		ctx = auth.WithIdentity(ctx, identity)
	}

	return ctx, nil
}

// IdentityFromContext retrieves the auth identity stored during
// authentication. Returns nil if no identity is stored.
func IdentityFromContext(ctx context.Context) *auth.Identity {
	id, _ := ctx.Value(identityContextKey{}).(*auth.Identity)
	return id
}
