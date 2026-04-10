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
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// Mock services
// ---------------------------------------------------------------------------

// mockKVService implements service.KVServicer for unit testing.
type mockKVService struct{}

func (m *mockKVService) Put(_ context.Context, _ string, _ []byte) error { return nil }
func (m *mockKVService) Get(_ context.Context, key string) ([]byte, error) {
	return nil, &service.KeyNotFoundError{Key: key}
}
func (m *mockKVService) Delete(_ context.Context, _ string) error           { return nil }
func (m *mockKVService) List(_ context.Context, _ string) ([]string, error) { return nil, nil }
func (m *mockKVService) Scan(_ context.Context, _ string) (map[string][]byte, error) {
	return nil, nil
}
func (m *mockKVService) GetStale(_ context.Context, _ string) ([]byte, error) { return nil, nil }
func (m *mockKVService) ListStale(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}
func (m *mockKVService) ScanStale(_ context.Context, _ string) (map[string][]byte, error) {
	return nil, nil
}
func (m *mockKVService) PutEntity(_ context.Context, _ *service.PutEntityRequest) (*service.PutEntityResponse, error) {
	return nil, nil
}
func (m *mockKVService) GetEntity(_ context.Context, _ *service.GetEntityRequest) (*service.GetEntityResponse, error) {
	return nil, nil
}
func (m *mockKVService) DeleteEntity(_ context.Context, _ *service.DeleteEntityRequest) (*service.DeleteEntityResponse, error) {
	return nil, nil
}
func (m *mockKVService) ListEntities(_ context.Context, _ *service.ListEntitiesRequest) (*service.ListEntitiesResponse, error) {
	return nil, nil
}
func (m *mockKVService) PutWithMode(_ context.Context, _ string, _ []byte, _ writemode.Mode) error {
	return nil
}
func (m *mockKVService) DeleteWithMode(_ context.Context, _ string, _ writemode.Mode) error {
	return nil
}
func (m *mockKVService) GetWithConsistency(_ context.Context, key string, _ consistency.ReadOptions) ([]byte, error) {
	return nil, &service.KeyNotFoundError{Key: key}
}
func (m *mockKVService) ListWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) ([]string, error) {
	return nil, nil
}
func (m *mockKVService) ScanWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) (map[string][]byte, error) {
	return nil, nil
}
func (m *mockKVService) ExistsWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) (bool, error) {
	return false, nil
}

// mockAdminService implements service.AdminServicer for unit testing.
type mockAdminService struct{}

func (m *mockAdminService) Health(_ context.Context) (*service.HealthStatus, error) {
	return &service.HealthStatus{Status: "ok", Leader: true}, nil
}
func (m *mockAdminService) Status(_ context.Context) (*service.StatusInfo, error) {
	return &service.StatusInfo{NodeID: "test"}, nil
}
func (m *mockAdminService) Peers(_ context.Context) (*service.PeersInfo, error) {
	return &service.PeersInfo{}, nil
}
func (m *mockAdminService) AddMember(_ context.Context, _, _ uint64, _ string) error { return nil }
func (m *mockAdminService) RemoveMember(_ context.Context, _, _ uint64) error        { return nil }
func (m *mockAdminService) RequestSnapshot(_ context.Context, _ uint64) error        { return nil }
func (m *mockAdminService) Drain(_ context.Context) error                            { return nil }
func (m *mockAdminService) Capabilities(_ context.Context) (*capabilities.Capabilities, error) {
	caps := capabilities.Get()
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

// ---------------------------------------------------------------------------
// NewServer tests
// ---------------------------------------------------------------------------

func TestNewServer_DefaultValues(t *testing.T) {
	kvSvc := &mockKVService{}
	adminSvc := &mockAdminService{}

	s := NewServer(kvSvc, adminSvc, "127.0.0.1:3306")

	require.NotNil(t, s)
	assert.Equal(t, "127.0.0.1:3306", s.addr)
	assert.Equal(t, "qrdb", s.dbName)
	assert.NotNil(t, s.logger)
	assert.Nil(t, s.tlsConfig)
	assert.Nil(t, s.authMW)
	assert.Nil(t, s.listener)
	assert.Nil(t, s.mysqlSrv)
	assert.Nil(t, s.sqlEngine)
}

func TestNewServer_WithDatabaseName(t *testing.T) {
	kvSvc := &mockKVService{}
	adminSvc := &mockAdminService{}

	s := NewServer(kvSvc, adminSvc, "127.0.0.1:3306", WithDatabaseName("mydb"))

	assert.Equal(t, "mydb", s.dbName)
}

func TestNewServer_WithTLS(t *testing.T) {
	kvSvc := &mockKVService{}
	adminSvc := &mockAdminService{}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13}

	s := NewServer(kvSvc, adminSvc, "127.0.0.1:3306", WithTLS(tlsCfg))

	assert.Equal(t, tlsCfg, s.tlsConfig)
}

func TestNewServer_WithAuthMiddleware(t *testing.T) {
	kvSvc := &mockKVService{}
	adminSvc := &mockAdminService{}
	authz := &mockAuthorizer{}
	mw := authmw.New(authz)

	s := NewServer(kvSvc, adminSvc, "127.0.0.1:3306", WithAuthMiddleware(mw))

	assert.NotNil(t, s.authMW)
}

func TestNewServer_WithLogger(t *testing.T) {
	kvSvc := &mockKVService{}
	adminSvc := &mockAdminService{}
	logger := slog.Default()

	s := NewServer(kvSvc, adminSvc, "127.0.0.1:3306", WithLogger(logger))

	assert.Equal(t, logger, s.logger)
}

func TestNewServer_MultipleOptions(t *testing.T) {
	kvSvc := &mockKVService{}
	adminSvc := &mockAdminService{}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13}
	authz := &mockAuthorizer{}
	mw := authmw.New(authz)
	logger := slog.Default()

	s := NewServer(kvSvc, adminSvc, "127.0.0.1:3306",
		WithTLS(tlsCfg),
		WithAuthMiddleware(mw),
		WithDatabaseName("custom"),
		WithLogger(logger),
	)

	assert.Equal(t, tlsCfg, s.tlsConfig)
	assert.NotNil(t, s.authMW)
	assert.Equal(t, "custom", s.dbName)
	assert.Equal(t, logger, s.logger)
}

// ---------------------------------------------------------------------------
// Addr tests
// ---------------------------------------------------------------------------

func TestAddr_BeforeStart(t *testing.T) {
	kvSvc := &mockKVService{}
	adminSvc := &mockAdminService{}

	s := NewServer(kvSvc, adminSvc, "127.0.0.1:3306")

	assert.Equal(t, "127.0.0.1:3306", s.Addr())
}

// ---------------------------------------------------------------------------
// Start / Stop lifecycle tests
// ---------------------------------------------------------------------------

func TestStartStop(t *testing.T) {
	kvSvc := &mockKVService{}
	adminSvc := &mockAdminService{}

	// Use port 0 for ephemeral port allocation.
	s := NewServer(kvSvc, adminSvc, "127.0.0.1:0")

	err := s.Start()
	require.NoError(t, err)

	// Addr should return the resolved address with an actual port.
	addr := s.Addr()
	assert.NotEqual(t, "127.0.0.1:0", addr)
	assert.Contains(t, addr, "127.0.0.1:")

	// The server should have initialized all components.
	assert.NotNil(t, s.sqlEngine)
	assert.NotNil(t, s.mysqlSrv)
	assert.NotNil(t, s.listener)

	// Stop should be clean.
	s.Stop()
}

func TestStop_NilServer(t *testing.T) {
	kvSvc := &mockKVService{}
	adminSvc := &mockAdminService{}

	s := NewServer(kvSvc, adminSvc, "127.0.0.1:0")

	// Stop without Start should not panic.
	s.Stop()
}

func TestStart_InvalidAddress(t *testing.T) {
	kvSvc := &mockKVService{}
	adminSvc := &mockAdminService{}

	// Bind to an invalid address to force a listen error.
	s := NewServer(kvSvc, adminSvc, "999.999.999.999:99999")

	err := s.Start()
	require.Error(t, err)

	var startErr *ServerStartError
	require.True(t, assert.ErrorAs(t, err, &startErr))
}

// ---------------------------------------------------------------------------
// DefaultDBName constant test
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// WithDatabaseManager option tests
// ---------------------------------------------------------------------------

func TestNewServer_WithDatabaseManager(t *testing.T) {
	kvSvc := &mockKVService{}
	adminSvc := &mockAdminService{}

	dataDir := t.TempDir()
	mgr := database.NewManager(slog.Default(), dataDir)
	defer func() { _ = mgr.Close() }()

	s := NewServer(kvSvc, adminSvc, "127.0.0.1:3306", WithDatabaseManager(mgr))

	require.NotNil(t, s)
	assert.Equal(t, mgr, s.dbManager)
}

func TestNewServer_WithDatabaseManager_Nil(t *testing.T) {
	kvSvc := &mockKVService{}
	adminSvc := &mockAdminService{}

	s := NewServer(kvSvc, adminSvc, "127.0.0.1:3306", WithDatabaseManager(nil))

	require.NotNil(t, s)
	assert.Nil(t, s.dbManager)
}

func TestStartStop_WithDatabaseManager(t *testing.T) {
	kvSvc := &mockKVService{}
	adminSvc := &mockAdminService{}

	dataDir := t.TempDir()
	mgr := database.NewManager(slog.Default(), dataDir)
	defer func() { _ = mgr.Close() }()

	// Create a database in the manager so the provider has something
	// to serve.
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
	assert.NotEqual(t, "127.0.0.1:0", addr)
	assert.Contains(t, addr, "127.0.0.1:")
	assert.NotNil(t, s.sqlEngine)

	s.Stop()
}

func TestStartStop_WithoutDatabaseManager(t *testing.T) {
	kvSvc := &mockKVService{}
	adminSvc := &mockAdminService{}

	// No database manager: legacy single-database mode.
	s := NewServer(kvSvc, adminSvc, "127.0.0.1:0")

	err := s.Start()
	require.NoError(t, err)

	assert.NotNil(t, s.sqlEngine)
	assert.Nil(t, s.dbManager)

	s.Stop()
}

// ---------------------------------------------------------------------------
// DefaultDBName constant test
// ---------------------------------------------------------------------------

func TestDefaultDBName(t *testing.T) {
	assert.Equal(t, "qrdb", defaultDBName)
}
