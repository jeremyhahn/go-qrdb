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
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	sqle "github.com/dolthub/go-mysql-server"
	gmssql "github.com/dolthub/go-mysql-server/sql"
	wire "github.com/jeroenrinzema/psql-wire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sqltest"
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

// TestBuildPgTLSConfig_DynamicHookWidensClientCAs proves PG connections see
// the per-tenant Key B trust pool a shared *clienttrust.Pool widens
// ClientCAs with on every handshake: the static base carries a narrow
// (Key-A-only) pool, the dynamic hook returns a wider one, and
// buildPgTLSConfig's composed hook must return the WIDE pool, not the
// static base it cloned from. An earlier version of this function
// overwrote base.GetConfigForClient outright and would have returned the
// narrow pool here (go-qrdb#560 review).
func TestBuildPgTLSConfig_DynamicHookWidensClientCAs(t *testing.T) {
	staticPool := x509.NewCertPool()
	widePool := x509.NewCertPool()

	base := &tls.Config{
		MinVersion: tls.VersionTLS13,
		ClientCAs:  staticPool,
		GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			return &tls.Config{MinVersion: tls.VersionTLS13, ClientCAs: widePool}, nil
		},
	}

	got := buildPgTLSConfig(base, nil)
	require.NotNil(t, got.GetConfigForClient)

	perConn, err := got.GetConfigForClient(&tls.ClientHelloInfo{})
	require.NoError(t, err)
	assert.Same(t, widePool, perConn.ClientCAs)
	assert.Equal(t, tls.RequireAndVerifyClientCert, perConn.ClientAuth)
	assert.Nil(t, perConn.GetConfigForClient, "the returned per-connection config must not carry a live hook")
}

// TestBuildPgTLSConfig_NoDynamicHookStillRequiresClientCert proves the
// static (no Key B provider wired yet) path still mandates mTLS: with no
// existing GetConfigForClient and no Authenticator, buildPgTLSConfig
// returns a plain clone (no hook installed at all) with ClientAuth raised.
func TestBuildPgTLSConfig_NoDynamicHookStillRequiresClientCert(t *testing.T) {
	base := &tls.Config{MinVersion: tls.VersionTLS12}
	got := buildPgTLSConfig(base, nil)
	assert.Equal(t, tls.RequireAndVerifyClientCert, got.ClientAuth)
	assert.Nil(t, got.GetConfigForClient)
}

// TestBuildPgTLSConfig_AuthenticatorStoresTLSStateOnDynamicPath proves the
// Authenticator's VerifyConnection wrapper still runs -- and still calls
// through to whatever VerifyConnection the widened dynamic config carried
// -- when a dynamic trust-pool hook is also present, so Key B works over
// PG and the auth middleware chain still sees the handshake's TLS state.
func TestBuildPgTLSConfig_AuthenticatorStoresTLSStateOnDynamicPath(t *testing.T) {
	var dynamicVerifyCalled bool
	base := &tls.Config{
		MinVersion: tls.VersionTLS13,
		GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			return &tls.Config{
				MinVersion: tls.VersionTLS13,
				VerifyConnection: func(tls.ConnectionState) error {
					dynamicVerifyCalled = true
					return nil
				},
			}, nil
		},
	}

	mw := authmw.New(&auth.NoopAuthorizer{})
	authenticator := NewAuthenticator(mw)
	got := buildPgTLSConfig(base, authenticator)

	remoteAddr := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5432}
	perConn, err := got.GetConfigForClient(&tls.ClientHelloInfo{
		Conn: &fakeConnWithRemoteAddr{remoteAddr: remoteAddr},
	})
	require.NoError(t, err)
	require.NotNil(t, perConn.VerifyConnection)

	require.NoError(t, perConn.VerifyConnection(tls.ConnectionState{}))
	assert.True(t, dynamicVerifyCalled, "the dynamic config's own VerifyConnection must still run")

	stored, ok := authenticator.tlsState.Load(remoteAddr.String())
	require.True(t, ok, "StoreTLSState must have recorded the handshake for this remote address")
	assert.NotNil(t, stored)
}

// fakeConnWithRemoteAddr implements just enough of net.Conn for
// tls.ClientHelloInfo.Conn.RemoteAddr() to resolve.
type fakeConnWithRemoteAddr struct {
	net.Conn
	remoteAddr net.Addr
}

func (f *fakeConnWithRemoteAddr) RemoteAddr() net.Addr { return f.remoteAddr }

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

func (m *mockAdminService) AddMember(_ context.Context, _, _ uint64, _, _ string) error {
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

func (m *mockAdminService) NodeCompaction(_ context.Context) (*compaction.Policy, error) {
	return nil, nil
}

func (m *mockAdminService) SetNodeCompaction(_ context.Context, _ *compaction.Policy) error {
	return nil
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

// ---------------------------------------------------------------------------
// Phase 9a: WithServiceRegistry / WithCollectionService / WithIndex option
// tests, and Start()-based proof that they reach the provider serving real
// sessions rather than merely being accepted and ignored.
// ---------------------------------------------------------------------------

func TestNewServer_WithServiceRegistry(t *testing.T) {
	kvSvc := &mockKVService{scanData: map[string][]byte{}}
	adminSvc := &mockAdminService{}
	reg := sqltest.NewHarness().Registry

	s := NewServer(kvSvc, adminSvc, ":5432", WithServiceRegistry(reg))

	require.NotNil(t, s.registry)
	assert.Same(t, reg, s.registry)
}

func TestNewServer_WithCollectionService(t *testing.T) {
	kvSvc := &mockKVService{scanData: map[string][]byte{}}
	adminSvc := &mockAdminService{}
	collSvc := &sqltest.FakeCollectionService{}

	s := NewServer(kvSvc, adminSvc, ":5432", WithCollectionService(collSvc))

	require.NotNil(t, s.collectionSvc)
	assert.Same(t, collSvc, s.collectionSvc)
}

func TestNewServer_WithIndex(t *testing.T) {
	kvSvc := &mockKVService{scanData: map[string][]byte{}}
	adminSvc := &mockAdminService{}
	idxSvc := &mockIndexService{}

	s := NewServer(kvSvc, adminSvc, ":5432", WithIndex(idxSvc))

	require.NotNil(t, s.indexSvc)
	assert.Same(t, idxSvc, s.indexSvc)
}

// mockIndexService is a minimal service.IndexServicer stand-in for option
// assignment tests; no method on it is ever invoked.
type mockIndexService struct {
	service.IndexServicer
}

// runSQL executes query against eng using a QRDBSession carrying tenantID,
// mirroring exactly what a real PostgreSQL session carries (see
// buildSQLContext) and what pkg/sql/query's run() does for the ad-hoc
// surface. It returns the result column names and materialized rows,
// failing t immediately on error.
func runSQL(t *testing.T, eng *sqle.Engine, dbName, tenantID, query string) ([]string, []gmssql.Row) {
	t.Helper()
	sess := provider.NewSession(gmssql.NewBaseSession(), &auth.Identity{Subject: "test", TenantID: tenantID})
	sqlCtx := gmssql.NewContext(context.Background(), gmssql.WithSession(sess))
	sqlCtx.SetCurrentDatabase(dbName)

	schema, iter, _, err := eng.Query(sqlCtx, query)
	require.NoError(t, err, "query %q", query)
	defer func() { _ = iter.Close(sqlCtx) }()

	columns := make([]string, len(schema))
	for i, col := range schema {
		columns[i] = col.Name
	}

	var rows []gmssql.Row
	for {
		row, rerr := iter.Next(sqlCtx)
		if rerr == io.EOF {
			break
		}
		require.NoError(t, rerr, "query %q", query)
		rows = append(rows, row)
	}
	return columns, rows
}

// TestServer_StartStop_WithServiceRegistry_ResolvesRelationalTable proves
// that WithServiceRegistry actually reaches the provider serving sessions
// -- not just that the option is accepted -- by creating a relational
// table through a REAL service.TableService and then reading it back
// through a query run against the server's built engine exactly as a real
// PostgreSQL client's SELECT would be.
func TestServer_StartStop_WithServiceRegistry_ResolvesRelationalTable(t *testing.T) {
	kvSvc := &mockKVService{scanData: map[string][]byte{}}
	adminSvc := &mockAdminService{}

	dataDir := t.TempDir()
	mgr := database.NewManager(slog.Default(), dataDir)
	defer func() { _ = mgr.Close() }()

	// Owned by the tenant the query runs as: a session sees only its own
	// tenant's databases.
	_, err := mgr.Create(database.DatabaseConfig{
		TenantID: "acme",
		Name:     "testdb",
		Backend:  database.BackendPebble,
		Protocol: database.ProtocolConfig{PostgreSQL: true},
	})
	require.NoError(t, err)

	h := sqltest.NewHarness()
	h.CreateTable(context.Background(), "testdb", "widgets", []service.ColumnDef{
		{Name: "id", Type: "INT"},
	})

	s := NewServer(kvSvc, adminSvc, "127.0.0.1:0",
		WithDatabaseManager(mgr),
		WithServiceRegistry(h.Registry),
	)

	require.NoError(t, s.Start())
	defer s.Stop()

	columns, rows := runSQL(t, s.sqlEngine, "testdb", "acme", "SELECT * FROM widgets")
	assert.Equal(t, []string{"id"}, columns)
	assert.Empty(t, rows)
}

// TestServer_StartStop_WithServiceRegistry_NoManager_KeepsSequenceOnTruncate
// pins go-qrdb#547 on the construction path without a database manager
// (WithServiceRegistry alone, as pkg/server composes it when no manager is
// configured). That path built its provider with no protocol, so relational
// tables took MySQL's TRUNCATE semantics on a PostgreSQL listener and
// restarted their sequence. Plain PostgreSQL TRUNCATE leaves an owned
// sequence where it was.
func TestServer_StartStop_WithServiceRegistry_NoManager_KeepsSequenceOnTruncate(t *testing.T) {
	kvSvc := &mockKVService{scanData: map[string][]byte{}}
	adminSvc := &mockAdminService{}

	h := sqltest.NewHarness()
	_, err := h.TableSvc.CreateTable(context.Background(), &service.CreateTableRequest{
		Database: "testdb",
		Name:     "widgets",
		Columns: []service.ColumnDef{
			{Name: "id", Type: "BIGINT UNSIGNED", Nullable: false, AutoIncrement: true},
			{Name: "name", Type: "VARCHAR(64)", Nullable: true},
		},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
	})
	require.NoError(t, err)

	s := NewServer(kvSvc, adminSvc, "127.0.0.1:0",
		WithDatabaseName("testdb"),
		WithServiceRegistry(h.Registry),
	)
	require.NoError(t, s.Start())
	defer s.Stop()

	runSQL(t, s.sqlEngine, "testdb", "acme", "INSERT INTO widgets (name) VALUES ('a')")
	runSQL(t, s.sqlEngine, "testdb", "acme", "INSERT INTO widgets (name) VALUES ('b')")
	runSQL(t, s.sqlEngine, "testdb", "acme", "TRUNCATE TABLE widgets")
	runSQL(t, s.sqlEngine, "testdb", "acme", "INSERT INTO widgets (name) VALUES ('c')")

	_, rows := runSQL(t, s.sqlEngine, "testdb", "acme", "SELECT id FROM widgets")
	require.Len(t, rows, 1)
	assert.Equal(t, uint64(3), rows[0][0], "PostgreSQL's plain TRUNCATE must not restart the sequence")
}

// TestServer_StartStop_WithServiceRegistry_NoManager_RestartIdentityResetsSequence
// proves the PostgreSQL wire's TRUNCATE ... RESTART IDENTITY support end to
// end: HandleParse must strip the clause the embedded grammar cannot parse,
// record it on the connection's session, and have RelationalTable.Truncate
// apply the same epoch-fenced sequence reset the MySQL wire always applies --
// unlike plain TRUNCATE on this same wire, which the sibling test above
// proves leaves the sequence alone.
func TestServer_StartStop_WithServiceRegistry_NoManager_RestartIdentityResetsSequence(t *testing.T) {
	kvSvc := &mockKVService{scanData: map[string][]byte{}}
	adminSvc := &mockAdminService{}

	h := sqltest.NewHarness()
	_, err := h.TableSvc.CreateTable(context.Background(), &service.CreateTableRequest{
		Database: "testdb",
		Name:     "widgets",
		Columns: []service.ColumnDef{
			{Name: "id", Type: "BIGINT UNSIGNED", Nullable: false, AutoIncrement: true},
			{Name: "name", Type: "VARCHAR(64)", Nullable: true},
		},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
	})
	require.NoError(t, err)

	s := NewServer(kvSvc, adminSvc, "127.0.0.1:0",
		WithDatabaseName("testdb"),
		WithServiceRegistry(h.Registry),
	)
	require.NoError(t, s.Start())
	defer s.Stop()

	parseHandler := NewParseHandler(s.sqlEngine, engine.NewTranslator(), "testdb")
	connCtx := context.WithValue(context.Background(), identityContextKey{},
		&auth.Identity{Subject: "test", TenantID: "acme"})
	connCtx, err = newConnectionSessionHandler()(connCtx)
	require.NoError(t, err)

	for _, q := range []string{
		"INSERT INTO widgets (name) VALUES ('a')",
		"INSERT INTO widgets (name) VALUES ('b')",
		"TRUNCATE TABLE widgets RESTART IDENTITY",
		"INSERT INTO widgets (name) VALUES ('c')",
	} {
		_, err := parseHandler.HandleParse(connCtx, q)
		require.NoError(t, err, "query %q", q)
	}

	_, rows := runSQL(t, s.sqlEngine, "testdb", "acme", "SELECT id FROM widgets")
	require.Len(t, rows, 1)
	assert.Equal(t, uint64(1), rows[0][0], "RESTART IDENTITY must restart the sequence")
}

// TestServer_StartStop_WithServiceRegistry_NoManager_ContinueIdentityKeepsSequence
// proves CONTINUE IDENTITY -- the default a bare TRUNCATE already carries --
// still parses as the explicit clause and does not reset the sequence.
func TestServer_StartStop_WithServiceRegistry_NoManager_ContinueIdentityKeepsSequence(t *testing.T) {
	kvSvc := &mockKVService{scanData: map[string][]byte{}}
	adminSvc := &mockAdminService{}

	h := sqltest.NewHarness()
	_, err := h.TableSvc.CreateTable(context.Background(), &service.CreateTableRequest{
		Database: "testdb",
		Name:     "widgets",
		Columns: []service.ColumnDef{
			{Name: "id", Type: "BIGINT UNSIGNED", Nullable: false, AutoIncrement: true},
			{Name: "name", Type: "VARCHAR(64)", Nullable: true},
		},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
	})
	require.NoError(t, err)

	s := NewServer(kvSvc, adminSvc, "127.0.0.1:0",
		WithDatabaseName("testdb"),
		WithServiceRegistry(h.Registry),
	)
	require.NoError(t, s.Start())
	defer s.Stop()

	parseHandler := NewParseHandler(s.sqlEngine, engine.NewTranslator(), "testdb")
	connCtx := context.WithValue(context.Background(), identityContextKey{},
		&auth.Identity{Subject: "test", TenantID: "acme"})
	connCtx, err = newConnectionSessionHandler()(connCtx)
	require.NoError(t, err)

	for _, q := range []string{
		"INSERT INTO widgets (name) VALUES ('a')",
		"INSERT INTO widgets (name) VALUES ('b')",
		"TRUNCATE TABLE widgets CONTINUE IDENTITY",
		"INSERT INTO widgets (name) VALUES ('c')",
	} {
		_, err := parseHandler.HandleParse(connCtx, q)
		require.NoError(t, err, "query %q", q)
	}

	_, rows := runSQL(t, s.sqlEngine, "testdb", "acme", "SELECT id FROM widgets")
	require.Len(t, rows, 1)
	assert.Equal(t, uint64(3), rows[0][0], "CONTINUE IDENTITY must not restart the sequence")
}

// TestServer_StartStop_WithServiceRegistry_NoManager_RestartIdentityClearsAfterStatement
// proves the per-statement flag HandleParse sets for RESTART IDENTITY does
// not leak onto a later, plain TRUNCATE on the same connection -- the defer
// in HandleParse must clear it once the statement finishes.
func TestServer_StartStop_WithServiceRegistry_NoManager_RestartIdentityClearsAfterStatement(t *testing.T) {
	kvSvc := &mockKVService{scanData: map[string][]byte{}}
	adminSvc := &mockAdminService{}

	h := sqltest.NewHarness()
	_, err := h.TableSvc.CreateTable(context.Background(), &service.CreateTableRequest{
		Database: "testdb",
		Name:     "widgets",
		Columns: []service.ColumnDef{
			{Name: "id", Type: "BIGINT UNSIGNED", Nullable: false, AutoIncrement: true},
			{Name: "name", Type: "VARCHAR(64)", Nullable: true},
		},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
	})
	require.NoError(t, err)

	s := NewServer(kvSvc, adminSvc, "127.0.0.1:0",
		WithDatabaseName("testdb"),
		WithServiceRegistry(h.Registry),
	)
	require.NoError(t, s.Start())
	defer s.Stop()

	parseHandler := NewParseHandler(s.sqlEngine, engine.NewTranslator(), "testdb")
	connCtx := context.WithValue(context.Background(), identityContextKey{},
		&auth.Identity{Subject: "test", TenantID: "acme"})
	connCtx, err = newConnectionSessionHandler()(connCtx)
	require.NoError(t, err)

	for _, q := range []string{
		"INSERT INTO widgets (name) VALUES ('a')",
		"INSERT INTO widgets (name) VALUES ('b')",
		"TRUNCATE TABLE widgets RESTART IDENTITY",
		"INSERT INTO widgets (name) VALUES ('c')",
		"TRUNCATE TABLE widgets",
		"INSERT INTO widgets (name) VALUES ('d')",
	} {
		_, err := parseHandler.HandleParse(connCtx, q)
		require.NoError(t, err, "query %q", q)
	}

	_, rows := runSQL(t, s.sqlEngine, "testdb", "acme", "SELECT id FROM widgets")
	require.Len(t, rows, 1)
	assert.Equal(t, uint64(2), rows[0][0],
		"the plain TRUNCATE after RESTART IDENTITY must not inherit its session flag")
}

// TestServer_StartStop_WithServiceRegistry_NoManager_ServesPGCatalog is the
// other half of the same wiring: without a database manager the listener is
// still PostgreSQL's, so unqualified pg_catalog relations resolve.
func TestServer_StartStop_WithServiceRegistry_NoManager_ServesPGCatalog(t *testing.T) {
	kvSvc := &mockKVService{scanData: map[string][]byte{}}
	adminSvc := &mockAdminService{}

	s := NewServer(kvSvc, adminSvc, "127.0.0.1:0",
		WithDatabaseName("testdb"),
		WithServiceRegistry(sqltest.NewHarness().Registry),
	)
	require.NoError(t, s.Start())
	defer s.Stop()

	_, rows := runSQL(t, s.sqlEngine, "testdb", "acme", "SELECT typname FROM pg_type WHERE typname = 'int8'")
	require.Len(t, rows, 1)
}

// TestServer_StartStop_WithCollectionService_ListsCollection proves that
// WithCollectionService reaches the provider serving sessions: a collection
// registered on a FakeCollectionService is visible through SHOW TABLES
// against the server's built engine.
func TestServer_StartStop_WithCollectionService_ListsCollection(t *testing.T) {
	kvSvc := &mockKVService{scanData: map[string][]byte{}}
	adminSvc := &mockAdminService{}

	dataDir := t.TempDir()
	mgr := database.NewManager(slog.Default(), dataDir)
	defer func() { _ = mgr.Close() }()

	// Owned by the tenant the query runs as: a session sees only its own
	// tenant's databases.
	_, err := mgr.Create(database.DatabaseConfig{
		TenantID: "acme",
		Name:     "testdb",
		Backend:  database.BackendPebble,
		Protocol: database.ProtocolConfig{PostgreSQL: true},
	})
	require.NoError(t, err)

	collSvc := &sqltest.FakeCollectionService{
		Infos: []service.CollectionInfo{{Name: "documents", Database: "testdb"}},
	}

	s := NewServer(kvSvc, adminSvc, "127.0.0.1:0",
		WithDatabaseManager(mgr),
		WithCollectionService(collSvc),
	)

	require.NoError(t, s.Start())
	defer s.Stop()

	_, rows := runSQL(t, s.sqlEngine, "testdb", "acme", "SHOW TABLES")
	var names []string
	for _, row := range rows {
		names = append(names, gmssqlRowString(row))
	}
	assert.Contains(t, names, "documents")
}

// gmssqlRowString extracts the first (and only) column of a SHOW TABLES
// result row as a string.
func gmssqlRowString(row gmssql.Row) string {
	if len(row) == 0 {
		return ""
	}
	s, _ := row[0].(string)
	return s
}
