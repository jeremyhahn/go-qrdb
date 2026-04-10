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
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	wire "github.com/jeroenrinzema/psql-wire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/provider"
)

func TestNewServer_Defaults(t *testing.T) {
	kvSvc := &mockKVService{}
	adminSvc := &mockAdminService{}
	s := NewServer(kvSvc, adminSvc, ":0")

	require.NotNil(t, s)
	assert.Equal(t, ":0", s.addr)
	assert.Equal(t, defaultDBName, s.dbName)
	assert.Nil(t, s.tlsConfig)
	assert.Nil(t, s.authMW)
	assert.NotNil(t, s.logger)
}

func TestNewServer_WithOptions(t *testing.T) {
	kvSvc := &mockKVService{}
	adminSvc := &mockAdminService{}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13}
	mw := authmw.New(&auth.NoopAuthorizer{})
	logger := slog.Default()

	s := NewServer(kvSvc, adminSvc, ":5432",
		WithTLS(tlsCfg),
		WithAuthMiddleware(mw),
		WithDatabaseName("testdb"),
		WithLogger(logger),
	)

	require.NotNil(t, s)
	assert.Equal(t, ":5432", s.addr)
	assert.Equal(t, "testdb", s.dbName)
	assert.NotNil(t, s.tlsConfig)
	assert.Equal(t, tls.VersionTLS13, int(s.tlsConfig.MinVersion))
	assert.NotNil(t, s.authMW)
	assert.NotNil(t, s.logger)
}

func TestWithTLS(t *testing.T) {
	s := &Server{}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	WithTLS(cfg)(s)
	assert.Equal(t, cfg, s.tlsConfig)
}

func TestWithTLS_Nil(t *testing.T) {
	s := &Server{}
	WithTLS(nil)(s)
	assert.Nil(t, s.tlsConfig)
}

func TestWithAuthMiddleware(t *testing.T) {
	s := &Server{}
	mw := authmw.New(&auth.NoopAuthorizer{})
	WithAuthMiddleware(mw)(s)
	assert.NotNil(t, s.authMW)
}

func TestWithAuthMiddleware_Nil(t *testing.T) {
	s := &Server{}
	WithAuthMiddleware(nil)(s)
	assert.Nil(t, s.authMW)
}

func TestWithDatabaseName(t *testing.T) {
	s := &Server{}
	WithDatabaseName("mydb")(s)
	assert.Equal(t, "mydb", s.dbName)
}

func TestWithDatabaseName_Empty(t *testing.T) {
	s := &Server{dbName: "original"}
	WithDatabaseName("")(s)
	assert.Equal(t, "", s.dbName)
}

func TestWithLogger(t *testing.T) {
	s := &Server{}
	logger := slog.Default()
	WithLogger(logger)(s)
	assert.Equal(t, logger, s.logger)
}

func TestServer_StartAndStop(t *testing.T) {
	kvSvc := &mockKVService{
		scanData: map[string][]byte{},
	}
	adminSvc := &mockAdminService{}
	s := NewServer(kvSvc, adminSvc, "127.0.0.1:0")

	err := s.Start()
	require.NoError(t, err)

	// Verify the server is listening on a real address.
	addr := s.Addr()
	assert.NotEmpty(t, addr)
	assert.NotEqual(t, "127.0.0.1:0", addr) // Should have resolved to a real port.

	// Stop should not panic.
	s.Stop()
}

func TestServer_Addr_BeforeStart(t *testing.T) {
	s := NewServer(&mockKVService{}, &mockAdminService{}, ":5432")
	assert.Equal(t, ":5432", s.Addr())
}

func TestServer_Stop_BeforeStart(t *testing.T) {
	s := NewServer(&mockKVService{}, &mockAdminService{}, ":0")
	// Stop before Start should not panic.
	s.Stop()
}

func TestServer_Stop_Idempotent(t *testing.T) {
	kvSvc := &mockKVService{
		scanData: map[string][]byte{},
	}
	adminSvc := &mockAdminService{}
	s := NewServer(kvSvc, adminSvc, "127.0.0.1:0")

	err := s.Start()
	require.NoError(t, err)

	// Multiple stops should be safe.
	s.Stop()
	s.Stop()
}

func TestDefaultDBName(t *testing.T) {
	assert.Equal(t, "qrdb", defaultDBName)
}

// ---------------------------------------------------------------------------
// WithDatabaseManager option tests
// ---------------------------------------------------------------------------

func TestWithDatabaseManager(t *testing.T) {
	dataDir := t.TempDir()
	mgr := database.NewManager(slog.Default(), dataDir)
	defer func() { _ = mgr.Close() }()

	s := &Server{}
	WithDatabaseManager(mgr)(s)
	assert.Equal(t, mgr, s.dbManager)
}

func TestWithDatabaseManager_Nil(t *testing.T) {
	s := &Server{}
	WithDatabaseManager(nil)(s)
	assert.Nil(t, s.dbManager)
}

func TestServer_StartStop_WithDatabaseManager(t *testing.T) {
	kvSvc := &mockKVService{scanData: map[string][]byte{}}
	adminSvc := &mockAdminService{}

	dataDir := t.TempDir()
	mgr := database.NewManager(slog.Default(), dataDir)
	defer func() { _ = mgr.Close() }()

	// Create a database so the manager-backed provider has something to serve.
	_, err := mgr.Create(database.DatabaseConfig{
		Name:    "testdb",
		Engine:  database.EngineKV,
		Backend: database.BackendPebble,
	})
	require.NoError(t, err)

	s := NewServer(kvSvc, adminSvc, "127.0.0.1:0", WithDatabaseManager(mgr))

	err = s.Start()
	require.NoError(t, err)

	addr := s.Addr()
	assert.NotEmpty(t, addr)
	assert.NotEqual(t, "127.0.0.1:0", addr)

	s.Stop()
}

func TestServer_StartStop_WithoutDatabaseManager(t *testing.T) {
	kvSvc := &mockKVService{scanData: map[string][]byte{}}
	adminSvc := &mockAdminService{}

	// No database manager: legacy single-database mode.
	s := NewServer(kvSvc, adminSvc, "127.0.0.1:0")

	err := s.Start()
	require.NoError(t, err)

	assert.Nil(t, s.dbManager)
	assert.NotEmpty(t, s.Addr())

	s.Stop()
}

func TestServer_StartWithAuth(t *testing.T) {
	kvSvc := &mockKVService{scanData: map[string][]byte{}}
	adminSvc := &mockAdminService{}
	mw := authmw.New(&auth.NoopAuthorizer{})

	// Create a minimal TLS config (no real certs needed for startup test).
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13}

	s := NewServer(kvSvc, adminSvc, "127.0.0.1:0",
		WithTLS(tlsCfg),
		WithAuthMiddleware(mw),
	)

	err := s.Start()
	require.NoError(t, err)

	addr := s.Addr()
	assert.NotEmpty(t, addr)
	assert.NotEqual(t, "127.0.0.1:0", addr)

	s.Stop()
}

func TestServer_StartWithTLSNoAuth(t *testing.T) {
	kvSvc := &mockKVService{scanData: map[string][]byte{}}
	adminSvc := &mockAdminService{}

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}

	s := NewServer(kvSvc, adminSvc, "127.0.0.1:0",
		WithTLS(tlsCfg),
	)

	err := s.Start()
	require.NoError(t, err)

	addr := s.Addr()
	assert.NotEmpty(t, addr)

	s.Stop()
}

func TestServer_StartWithAuthNoTLS(t *testing.T) {
	kvSvc := &mockKVService{scanData: map[string][]byte{}}
	adminSvc := &mockAdminService{}
	mw := authmw.New(&auth.NoopAuthorizer{})

	s := NewServer(kvSvc, adminSvc, "127.0.0.1:0",
		WithAuthMiddleware(mw),
	)

	err := s.Start()
	require.NoError(t, err)

	addr := s.Addr()
	assert.NotEmpty(t, addr)

	s.Stop()
}

func TestServer_StartWithCustomDB(t *testing.T) {
	kvSvc := &mockKVService{scanData: map[string][]byte{}}
	adminSvc := &mockAdminService{}

	s := NewServer(kvSvc, adminSvc, "127.0.0.1:0",
		WithDatabaseName("customdb"),
	)

	err := s.Start()
	require.NoError(t, err)
	assert.NotEmpty(t, s.Addr())

	s.Stop()
}

func TestServer_StartWithTLSAndVerifyConnection(t *testing.T) {
	kvSvc := &mockKVService{scanData: map[string][]byte{}}
	adminSvc := &mockAdminService{}
	mw := authmw.New(&auth.NoopAuthorizer{})

	// TLS config with existing VerifyConnection callback.
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
		VerifyConnection: func(cs tls.ConnectionState) error {
			return nil
		},
	}

	s := NewServer(kvSvc, adminSvc, "127.0.0.1:0",
		WithTLS(tlsCfg),
		WithAuthMiddleware(mw),
	)

	err := s.Start()
	require.NoError(t, err)
	s.Stop()
}

func TestServer_ServeErrorLogging(t *testing.T) {
	// Test the serve() error logging path where wireSrv.Serve returns a
	// non-context-cancellation error. We use a custom listener that
	// returns a non-net.ErrClosed error from Accept() to bypass
	// psql-wire's graceful close handling.
	kvSvc := &mockKVService{scanData: map[string][]byte{}}
	adminSvc := &mockAdminService{}

	// Capture log output to verify error is logged.
	var buf logBuffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	s := NewServer(kvSvc, adminSvc, "127.0.0.1:0", WithLogger(logger))

	// Build the wire server manually so we can call serve() directly
	// with our error-producing listener.
	dbProvider := provider.NewProvider(s.dbName, s.kvSvc)
	sqlEngine := engine.New(engine.Config{Provider: dbProvider})
	translator := engine.NewTranslator()
	parseHandler := NewParseHandler(sqlEngine, translator, s.dbName)

	wireSrv, err := wire.NewServer(parseHandler.HandleParse, wire.Logger(logger))
	require.NoError(t, err)
	s.wireSrv = wireSrv

	// Create a listener that returns an error on Accept().
	acceptErr := errors.New("accept: connection reset by peer")
	errListener := &errorListener{err: acceptErr}

	// Run serve() in a goroutine with a non-canceled context.
	ctx := context.Background()
	done := make(chan struct{})
	go func() {
		s.serve(ctx, errListener)
		close(done)
	}()

	// Wait for the goroutine to finish.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("serve() did not return within timeout")
	}

	// Verify that the error was logged (the default case in the select).
	logged := buf.String()
	assert.Contains(t, logged, "pgsql: server error")
}

func TestServer_StartBadAddress(t *testing.T) {
	kvSvc := &mockKVService{scanData: map[string][]byte{}}
	adminSvc := &mockAdminService{}

	s := NewServer(kvSvc, adminSvc, "invalid-addr-no-port")
	err := s.Start()
	require.Error(t, err)

	var startErr *ServerStartError
	assert.ErrorAs(t, err, &startErr)
}

func TestSessionHandler_NilRemoteAddr(t *testing.T) {
	authenticator := NewAuthenticator(nil)
	handler := sessionHandler(authenticator)

	// Context without remote address (nil).
	ctx := context.Background()
	resultCtx, err := handler(ctx)
	require.NoError(t, err)
	assert.NotNil(t, resultCtx)
}

func TestCloseHandler_NilRemoteAddr(t *testing.T) {
	authenticator := NewAuthenticator(nil)
	handler := closeHandler(authenticator)

	// Context without remote address.
	ctx := context.Background()
	err := handler(ctx)
	require.NoError(t, err)
}

func TestCloseHandler_WithAddr(t *testing.T) {
	authenticator := NewAuthenticator(nil)
	addr := &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 5432}

	// Pre-store some TLS state.
	authenticator.StoreTLSState(addr, &tls.ConnectionState{})
	_, ok := authenticator.tlsState.Load(addr.String())
	require.True(t, ok)

	// The close handler requires wire.RemoteAddress in the context,
	// which is set by psql-wire internally. Since we cannot use
	// psql-wire's internal setter, we test the authenticator directly.
	authenticator.ClearTLSState(addr.String())
	_, ok = authenticator.tlsState.Load(addr.String())
	assert.False(t, ok)
}

// mockAdminService is a minimal admin service mock for testing server
// construction without requiring a Raft cluster.
type mockAdminService struct{}

func (m *mockAdminService) Health(_ context.Context) (*service.HealthStatus, error) {
	return &service.HealthStatus{Status: "ok"}, nil
}

func (m *mockAdminService) Status(_ context.Context) (*service.StatusInfo, error) {
	return &service.StatusInfo{}, nil
}

func (m *mockAdminService) Peers(_ context.Context) (*service.PeersInfo, error) {
	return &service.PeersInfo{}, nil
}

func (m *mockAdminService) AddMember(_ context.Context, _, _ uint64, _ string) error {
	return nil
}

func (m *mockAdminService) RemoveMember(_ context.Context, _, _ uint64) error {
	return nil
}

func (m *mockAdminService) RequestSnapshot(_ context.Context, _ uint64) error {
	return nil
}

func (m *mockAdminService) Drain(_ context.Context) error {
	return nil
}

func (m *mockAdminService) Capabilities(_ context.Context) (*capabilities.Capabilities, error) {
	caps := capabilities.Capabilities{}
	return &caps, nil
}

func (m *mockAdminService) StartShard(_ context.Context, _ *service.StartShardRequest) error {
	return nil
}
func (m *mockAdminService) StopShard(_ context.Context, _ uint64) error { return nil }
func (m *mockAdminService) GetShardState(_ context.Context, _ uint64) (*service.ShardStateResponse, error) {
	return nil, nil
}
func (m *mockAdminService) GetTopology(_ context.Context) (*service.TopologyResponse, error) {
	return nil, nil
}

// logBuffer is a thread-safe bytes.Buffer for capturing slog output
// in concurrent test scenarios.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// errorListener is a net.Listener that always returns an error on Accept.
// Used to test the serve() error logging path.
type errorListener struct {
	err error
}

func (l *errorListener) Accept() (net.Conn, error) {
	return nil, l.err
}

func (l *errorListener) Close() error {
	return nil
}

func (l *errorListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0}
}
