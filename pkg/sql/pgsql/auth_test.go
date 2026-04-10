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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

func TestNewAuthenticator(t *testing.T) {
	a := NewAuthenticator(nil)
	require.NotNil(t, a)
	assert.Nil(t, a.authMW)
}

func TestAuthenticator_Authenticate_NilAuthMW(t *testing.T) {
	a := NewAuthenticator(nil)
	ctx := context.Background()
	addr := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5432}

	resultCtx, err := a.Authenticate(ctx, addr)
	require.NoError(t, err)
	assert.NotNil(t, resultCtx)

	// No identity should be stored when auth is nil.
	identity := IdentityFromContext(resultCtx)
	assert.Nil(t, identity)
}

func TestAuthenticator_Authenticate_NoTLSState(t *testing.T) {
	// Create a non-nil AuthMiddleware to trigger the auth path.
	mw := stubAuthMiddleware()
	a := NewAuthenticator(mw)
	ctx := context.Background()
	addr := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5432}

	// No TLS state stored for this address.
	_, err := a.Authenticate(ctx, addr)
	require.Error(t, err)

	var authErr *AuthenticationError
	assert.ErrorAs(t, err, &authErr)
}

func TestAuthenticator_StoreTLSState_And_ClearTLSState(t *testing.T) {
	a := NewAuthenticator(nil)
	addr := &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 12345}

	// Initially no state.
	_, ok := a.tlsState.Load(addr.String())
	assert.False(t, ok)

	// Store and verify.
	a.StoreTLSState(addr, nil)
	_, ok = a.tlsState.Load(addr.String())
	assert.True(t, ok)

	// Clear and verify.
	a.ClearTLSState(addr.String())
	_, ok = a.tlsState.Load(addr.String())
	assert.False(t, ok)
}

func TestIdentityFromContext_NoIdentity(t *testing.T) {
	ctx := context.Background()
	identity := IdentityFromContext(ctx)
	assert.Nil(t, identity)
}

func TestIdentityFromContext_WithIdentity(t *testing.T) {
	ctx := context.Background()
	expected := &auth.Identity{
		Subject:  "test-user",
		TenantID: "tenant-1",
		Roles:    []string{"admin"},
	}

	ctx = context.WithValue(ctx, identityContextKey{}, expected)
	identity := IdentityFromContext(ctx)
	require.NotNil(t, identity)
	assert.Equal(t, "test-user", identity.Subject)
	assert.Equal(t, "tenant-1", identity.TenantID)
	assert.Equal(t, []string{"admin"}, identity.Roles)
}

func TestIdentityFromContext_WrongType(t *testing.T) {
	ctx := context.WithValue(context.Background(), identityContextKey{}, "not-an-identity")
	identity := IdentityFromContext(ctx)
	assert.Nil(t, identity)
}

func TestAuthenticator_Authenticate_NilTLSStateValue(t *testing.T) {
	// Store a nil tls.ConnectionState to test the nil check.
	mw := stubAuthMiddleware()
	a := NewAuthenticator(mw)
	addr := &net.TCPAddr{IP: net.ParseIP("192.168.1.1"), Port: 9999}

	// Store nil *tls.ConnectionState.
	a.StoreTLSState(addr, nil)

	ctx := context.Background()
	_, err := a.Authenticate(ctx, addr)
	require.Error(t, err)

	var authErr *AuthenticationError
	assert.ErrorAs(t, err, &authErr)
}

func TestAuthenticator_Authenticate_ValidTLSState(t *testing.T) {
	// Create an AuthMiddleware with an mTLS authenticator and a valid
	// cert chain. Since we cannot easily construct a full verified chain
	// in unit tests, we test the code path that handles a non-nil
	// tls.ConnectionState but fails at the authenticator level.
	mw := authmw.New(
		&auth.NoopAuthorizer{},
		authmw.WithAuthenticator(auth.NewMTLSAuthenticator()),
	)
	a := NewAuthenticator(mw)
	addr := &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 5432}

	// Store a real (but empty) TLS state -- no verified chains.
	state := &tls.ConnectionState{}
	a.StoreTLSState(addr, state)

	ctx := context.Background()
	_, err := a.Authenticate(ctx, addr)
	require.Error(t, err)

	var authErr *AuthenticationError
	assert.ErrorAs(t, err, &authErr)
}

func TestAuthenticator_Authenticate_InvalidTypeInMap(t *testing.T) {
	// Store a non-*tls.ConnectionState value in the map to exercise the
	// type assertion failure path.
	mw := authmw.New(&auth.NoopAuthorizer{})
	a := NewAuthenticator(mw)
	addr := &net.TCPAddr{IP: net.ParseIP("10.0.0.2"), Port: 5433}

	// Manually store a wrong type.
	a.tlsState.Store(addr.String(), "not-a-tls-state")

	ctx := context.Background()
	_, err := a.Authenticate(ctx, addr)
	require.Error(t, err)

	var authErr *AuthenticationError
	assert.ErrorAs(t, err, &authErr)
}

func TestAuthenticator_StoreTLSState_Concurrent(t *testing.T) {
	a := NewAuthenticator(nil)
	done := make(chan struct{})

	// Write from multiple goroutines to verify thread safety.
	for i := 0; i < 10; i++ {
		go func(idx int) {
			addr := &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 5432 + idx}
			a.StoreTLSState(addr, &tls.ConnectionState{})
			a.ClearTLSState(addr.String())
			done <- struct{}{}
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestAuthenticator_Authenticate_SuccessfulAuth(t *testing.T) {
	// Build a mock authenticator that always returns a valid identity,
	// bypassing the need for real mTLS certificate chains.
	expected := &auth.Identity{
		Subject:  "pgsql-user",
		TenantID: "tenant-42",
		Roles:    []string{"reader", "writer"},
	}

	mw := authmw.New(
		&auth.NoopAuthorizer{},
		authmw.WithAuthenticator(&testAuthenticator{identity: expected}),
	)
	a := NewAuthenticator(mw)
	addr := &net.TCPAddr{IP: net.ParseIP("192.168.1.100"), Port: 5432}

	// Store a valid (non-nil) TLS state so the lookup succeeds.
	state := &tls.ConnectionState{ServerName: "qrdb.local"}
	a.StoreTLSState(addr, state)

	ctx := context.Background()
	resultCtx, err := a.Authenticate(ctx, addr)
	require.NoError(t, err)

	// Verify the identity was stored in the package-specific context key.
	identity := IdentityFromContext(resultCtx)
	require.NotNil(t, identity)
	assert.Equal(t, "pgsql-user", identity.Subject)
	assert.Equal(t, "tenant-42", identity.TenantID)
	assert.Equal(t, []string{"reader", "writer"}, identity.Roles)

	// Verify the identity was also stored via auth.WithIdentity.
	authIdentity := auth.IdentityFromContext(resultCtx)
	require.NotNil(t, authIdentity)
	assert.Equal(t, "pgsql-user", authIdentity.Subject)
}

// testAuthenticator is a mock auth.Authenticator for testing. It always
// returns the configured identity without inspecting TLS state.
type testAuthenticator struct {
	identity *auth.Identity
}

func (a *testAuthenticator) Authenticate(_ context.Context) (*auth.Identity, error) {
	return a.identity, nil
}

func (a *testAuthenticator) Name() string {
	return "test"
}

// stubAuthMiddleware creates a minimal AuthMiddleware for testing. It uses
// a noop authorizer and no authenticators, which means Authenticate will
// fail with ErrUnauthenticated unless TLS state is properly set up.
func stubAuthMiddleware() *authmw.AuthMiddleware {
	return authmw.New(&auth.NoopAuthorizer{})
}
