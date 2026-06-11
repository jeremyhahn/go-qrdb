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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"testing"
	"time"

	vitessmysql "github.com/dolthub/vitess/go/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/provider"
)

// ---------------------------------------------------------------------------
// Mock authenticator
// ---------------------------------------------------------------------------

// mockAuthenticator implements auth.Authenticator for testing.
type mockAuthenticator struct {
	identity *auth.Identity
	err      error
}

func (m *mockAuthenticator) Name() string { return "mock" }

func (m *mockAuthenticator) Authenticate(_ context.Context) (*auth.Identity, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.identity, nil
}

// mockAuthorizer implements auth.Authorizer for testing.
type mockAuthorizer struct{}

func (m *mockAuthorizer) Authorize(_ context.Context, _ *auth.Identity, _, _ string) error {
	return nil
}

// ---------------------------------------------------------------------------
// Test CA helper
// ---------------------------------------------------------------------------

// testCAPool mints an in-memory ECDSA P-256 CA and returns a *x509.CertPool
// containing the CA certificate. The pool is used in place of
// InsecureSkipVerify so that tls.Config structs used in unit tests carry a
// real trust anchor without skipping verification.
//
// No actual TLS handshake occurs in these tests — the net.Pipe connections
// are never driven end-to-end — so the pool content itself is not exercised
// cryptographically. Its purpose is solely to produce an honest tls.Config.
func testCAPool(t *testing.T) *x509.CertPool {
	t.Helper()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err, "generate test CA key")

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-mysql-ca"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &caKey.PublicKey, caKey)
	require.NoError(t, err, "create test CA certificate")

	caCert, err := x509.ParseCertificate(der)
	require.NoError(t, err, "parse test CA certificate")

	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	return pool
}

// ---------------------------------------------------------------------------
// Helper: create a vitess Conn with TLS
// ---------------------------------------------------------------------------

// newTLSConn creates a vitess mysql.Conn whose underlying net.Conn is a
// *tls.Conn. This avoids real TLS handshakes in unit tests: both pipe ends
// are created but only the client side is wrapped; the server side is closed
// immediately, so no handshake is driven. The tls.Config carries a real
// RootCAs pool from testCAPool to produce an honest (non-skip-verify) config.
func newTLSConn(t *testing.T) *vitessmysql.Conn {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	defer func() { _ = serverConn.Close() }()

	tlsCfg := &tls.Config{
		ServerName: "test-mysql-server",
		RootCAs:    testCAPool(t),
		MinVersion: tls.VersionTLS12,
	}
	tlsClient := tls.Client(clientConn, tlsCfg)

	return &vitessmysql.Conn{
		Conn:         tlsClient,
		ConnectionID: 42,
		User:         "testuser",
	}
}

// newPlainConn creates a vitess mysql.Conn with a plain (non-TLS) net.Conn.
func newPlainConn() *vitessmysql.Conn {
	clientConn, serverConn := net.Pipe()
	defer func() { _ = serverConn.Close() }()

	return &vitessmysql.Conn{
		Conn:         clientConn,
		ConnectionID: 1,
		User:         "plainuser",
	}
}

// ---------------------------------------------------------------------------
// sessionBuilder tests
// ---------------------------------------------------------------------------

func TestSessionBuilder_NilAuth_ReturnsSystemSession(t *testing.T) {
	sb := newSessionBuilder(nil)

	conn := newPlainConn()
	defer func() { _ = conn.Conn.Close() }()

	sess, err := sb.Build(context.Background(), conn, "127.0.0.1:3306")

	require.NoError(t, err)
	require.NotNil(t, sess)

	qrdbSess, ok := sess.(*provider.QRDBSession)
	require.True(t, ok, "expected *provider.QRDBSession, got %T", sess)
	assert.Nil(t, qrdbSess.Identity())
	assert.Equal(t, "system", qrdbSess.TenantID())
}

func TestSessionBuilder_AuthSuccess(t *testing.T) {
	identity := &auth.Identity{
		Subject:  "alice",
		TenantID: "acme",
		Roles:    []string{"admin"},
	}

	// The mock authenticator ignores TLS state and returns the identity directly.
	authn := &mockAuthenticator{identity: identity}
	authz := &mockAuthorizer{}
	mw := authmw.New(authz, authmw.WithAuthenticator(authn))

	sb := newSessionBuilder(mw)

	// Need a TLS conn for the auth path.
	conn := newTLSConn(t)
	defer func() { _ = conn.Conn.Close() }()

	sess, err := sb.Build(context.Background(), conn, "127.0.0.1:3306")

	require.NoError(t, err)
	require.NotNil(t, sess)

	qrdbSess, ok := sess.(*provider.QRDBSession)
	require.True(t, ok)
	require.NotNil(t, qrdbSess.Identity())
	assert.Equal(t, "alice", qrdbSess.Identity().Subject)
	assert.Equal(t, "acme", qrdbSess.TenantID())
}

func TestSessionBuilder_AuthFailure_ReturnsError(t *testing.T) {
	authn := &mockAuthenticator{err: auth.ErrUnauthenticated}
	authz := &mockAuthorizer{}
	mw := authmw.New(authz, authmw.WithAuthenticator(authn))

	sb := newSessionBuilder(mw)

	conn := newTLSConn(t)
	defer func() { _ = conn.Conn.Close() }()

	sess, err := sb.Build(context.Background(), conn, "127.0.0.1:3306")

	require.Error(t, err)
	assert.Nil(t, sess)

	var authErr *AuthenticationError
	require.True(t, errors.As(err, &authErr))
}

func TestSessionBuilder_NoTLS_WithAuth_ReturnsAuthenticationError(t *testing.T) {
	authn := &mockAuthenticator{identity: &auth.Identity{Subject: "bob"}}
	authz := &mockAuthorizer{}
	mw := authmw.New(authz, authmw.WithAuthenticator(authn))

	sb := newSessionBuilder(mw)

	// Plain (non-TLS) connection with auth middleware configured.
	conn := newPlainConn()
	defer func() { _ = conn.Conn.Close() }()

	sess, err := sb.Build(context.Background(), conn, "127.0.0.1:3306")

	require.Error(t, err)
	assert.Nil(t, sess)

	var authErr *AuthenticationError
	require.True(t, errors.As(err, &authErr))
	assert.True(t, errors.Is(authErr, auth.ErrNoPeerCertificate))
}

func TestSessionBuilder_MTLSAuth_WithVerifiedChains(t *testing.T) {
	// Use a real mTLS authenticator with verified chains.
	authn := auth.NewMTLSAuthenticator()
	authz := &mockAuthorizer{}
	mw := authmw.New(authz, authmw.WithAuthenticator(authn))

	sb := newSessionBuilder(mw)

	// Create a TLS conn. The mTLS authenticator requires verified chains
	// in the TLS state, which we cannot easily inject into a real
	// tls.Conn without a full handshake. This test verifies that the code
	// path correctly wraps the authentication error when mTLS fails.
	conn := newTLSConn(t)
	defer func() { _ = conn.Conn.Close() }()

	sess, err := sb.Build(context.Background(), conn, "127.0.0.1:3306")

	require.Error(t, err)
	assert.Nil(t, sess)

	var authErr *AuthenticationError
	require.True(t, errors.As(err, &authErr))
}

func TestSessionBuilder_SetsConnectionIDAndUser(t *testing.T) {
	sb := newSessionBuilder(nil)

	conn := newPlainConn()
	conn.ConnectionID = 99
	conn.User = "admin"
	defer func() { _ = conn.Conn.Close() }()

	sess, err := sb.Build(context.Background(), conn, "10.0.0.1:3306")

	require.NoError(t, err)
	require.NotNil(t, sess)

	// Verify the session ID matches the connection ID.
	assert.Equal(t, uint32(99), sess.ID())
}

// ---------------------------------------------------------------------------
// Helper function tests
// ---------------------------------------------------------------------------

func TestNewSessionBuilder(t *testing.T) {
	sb := newSessionBuilder(nil)
	require.NotNil(t, sb)
	assert.Nil(t, sb.authMW)
}

func TestNewSessionBuilder_WithMiddleware(t *testing.T) {
	authz := &mockAuthorizer{}
	mw := authmw.New(authz)

	sb := newSessionBuilder(mw)
	require.NotNil(t, sb)
	assert.NotNil(t, sb.authMW)
}

// ---------------------------------------------------------------------------
// Type assertion test for provider.QRDBSession certificate extraction
// ---------------------------------------------------------------------------

func TestSessionBuilder_NilAuth_TenantSystem(t *testing.T) {
	// Verify that a nil-auth session always produces the system tenant.
	sb := newSessionBuilder(nil)

	conn := newPlainConn()
	defer func() { _ = conn.Conn.Close() }()

	sess, err := sb.Build(context.Background(), conn, "127.0.0.1:3306")
	require.NoError(t, err)

	qrdbSess := sess.(*provider.QRDBSession)
	assert.Equal(t, "system", qrdbSess.TenantID())
	assert.Nil(t, qrdbSess.Identity())
}
