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

package transport

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// In-memory mock DatabaseClient for testing
// ---------------------------------------------------------------------------

type mockDatabaseClient struct {
	databases map[string]*DatabaseInfo
	createErr error
	getErr    error
	listErr   error
	dropErr   error
	updateErr error
}

func newMockDatabaseClient() *mockDatabaseClient {
	return &mockDatabaseClient{
		databases: make(map[string]*DatabaseInfo),
	}
}

func (m *mockDatabaseClient) CreateDatabase(_ context.Context, req CreateDatabaseRequest) (*DatabaseInfo, error) {
	if m.createErr != nil {
		return nil, m.createErr
	}
	info := &DatabaseInfo{
		TenantID: req.TenantID,
		Name:     req.Name,
		Engine:   req.Engine,
		Backend:  req.Backend,
		Status:   "open",
	}
	m.databases[req.Name] = info
	return info, nil
}

func (m *mockDatabaseClient) GetDatabase(_ context.Context, name string) (*DatabaseInfo, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	info, ok := m.databases[name]
	if !ok {
		return nil, &DatabaseNotFoundError{Name: name}
	}
	return info, nil
}

func (m *mockDatabaseClient) ListDatabases(_ context.Context) ([]DatabaseInfo, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	result := make([]DatabaseInfo, 0, len(m.databases))
	for _, info := range m.databases {
		result = append(result, *info)
	}
	return result, nil
}

func (m *mockDatabaseClient) DropDatabase(_ context.Context, name string) error {
	if m.dropErr != nil {
		return m.dropErr
	}
	if _, ok := m.databases[name]; !ok {
		return &DatabaseNotFoundError{Name: name}
	}
	delete(m.databases, name)
	return nil
}

func (m *mockDatabaseClient) UpdateDatabaseProtocol(_ context.Context, req UpdateDatabaseProtocolRequest) (*DatabaseInfo, error) {
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	info, ok := m.databases[req.Name]
	if !ok {
		return nil, &DatabaseNotFoundError{Name: req.Name}
	}
	info.Protocol.MySQL = req.MySQL
	info.Protocol.PostgreSQL = req.PostgreSQL
	return info, nil
}

func (m *mockDatabaseClient) UpdateDatabaseEtcd(_ context.Context, req UpdateDatabaseEtcdRequest) (*DatabaseInfo, error) {
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	info, ok := m.databases[req.Name]
	if !ok {
		return nil, &DatabaseNotFoundError{Name: req.Name}
	}
	info.Protocol.Etcd = &EtcdProtocolConfig{
		Enabled:             req.Enabled,
		ListenAddr:          req.ListenAddr,
		AdvertiseClientURLs: req.AdvertiseClientURLs,
	}
	return info, nil
}

func (m *mockDatabaseClient) UpdateDatabaseRaft(_ context.Context, req UpdateDatabaseRaftRequest) (*DatabaseInfo, error) {
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	info, ok := m.databases[req.Name]
	if !ok {
		return nil, &DatabaseNotFoundError{Name: req.Name}
	}
	info.Raft = req.Raft
	return info, nil
}

func (m *mockDatabaseClient) UpdateDatabaseBarrier(_ context.Context, req UpdateDatabaseBarrierRequest) (*DatabaseInfo, error) {
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	info, ok := m.databases[req.Name]
	if !ok {
		return nil, &DatabaseNotFoundError{Name: req.Name}
	}
	info.Barrier = req.Barrier
	return info, nil
}

func (m *mockDatabaseClient) ScaleDatabase(_ context.Context, req ScaleDatabaseRequest) (*DatabaseInfo, error) {
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	info, ok := m.databases[req.Name]
	if !ok {
		return nil, &DatabaseNotFoundError{Name: req.Name}
	}
	info.Replicas = req.TargetReplicas
	return info, nil
}

func (m *mockDatabaseClient) UpdateDatabaseConnection(_ context.Context, req UpdateDatabaseConnectionRequest) (*DatabaseInfo, error) {
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	info, ok := m.databases[req.Name]
	if !ok {
		return nil, &DatabaseNotFoundError{Name: req.Name}
	}
	info.Connection = req.Connection
	return info, nil
}

func (m *mockDatabaseClient) UpdateDatabaseSQL(_ context.Context, req UpdateDatabaseSQLRequest) (*DatabaseInfo, error) {
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	info, ok := m.databases[req.Name]
	if !ok {
		return nil, &DatabaseNotFoundError{Name: req.Name}
	}
	info.SQL = req.SQL
	return info, nil
}

func (m *mockDatabaseClient) SealDatabase(_ context.Context, name string) error {
	if _, ok := m.databases[name]; !ok {
		return &DatabaseNotFoundError{Name: name}
	}
	return nil
}

func (m *mockDatabaseClient) UnsealDatabase(_ context.Context, name, _ string) error {
	if _, ok := m.databases[name]; !ok {
		return &DatabaseNotFoundError{Name: name}
	}
	return nil
}

func (m *mockDatabaseClient) DatabaseBarrierStatus(_ context.Context, name string) (*DatabaseBarrierStatus, error) {
	if _, ok := m.databases[name]; !ok {
		return nil, &DatabaseNotFoundError{Name: name}
	}
	return &DatabaseBarrierStatus{Sealed: false, Strategy: "software"}, nil
}

// failingDatabaseClient returns errors for all operations.
type failingDatabaseClient struct {
	err error
}

func (f *failingDatabaseClient) CreateDatabase(_ context.Context, _ CreateDatabaseRequest) (*DatabaseInfo, error) {
	return nil, f.err
}

func (f *failingDatabaseClient) GetDatabase(_ context.Context, _ string) (*DatabaseInfo, error) {
	return nil, f.err
}

func (f *failingDatabaseClient) ListDatabases(_ context.Context) ([]DatabaseInfo, error) {
	return nil, f.err
}

func (f *failingDatabaseClient) DropDatabase(_ context.Context, _ string) error {
	return f.err
}

func (f *failingDatabaseClient) UpdateDatabaseProtocol(_ context.Context, _ UpdateDatabaseProtocolRequest) (*DatabaseInfo, error) {
	return nil, f.err
}

func (f *failingDatabaseClient) UpdateDatabaseEtcd(_ context.Context, _ UpdateDatabaseEtcdRequest) (*DatabaseInfo, error) {
	return nil, f.err
}

func (f *failingDatabaseClient) UpdateDatabaseRaft(_ context.Context, _ UpdateDatabaseRaftRequest) (*DatabaseInfo, error) {
	return nil, f.err
}

func (f *failingDatabaseClient) UpdateDatabaseBarrier(_ context.Context, _ UpdateDatabaseBarrierRequest) (*DatabaseInfo, error) {
	return nil, f.err
}

func (f *failingDatabaseClient) ScaleDatabase(_ context.Context, _ ScaleDatabaseRequest) (*DatabaseInfo, error) {
	return nil, f.err
}

func (f *failingDatabaseClient) UpdateDatabaseConnection(_ context.Context, _ UpdateDatabaseConnectionRequest) (*DatabaseInfo, error) {
	return nil, f.err
}

func (f *failingDatabaseClient) UpdateDatabaseSQL(_ context.Context, _ UpdateDatabaseSQLRequest) (*DatabaseInfo, error) {
	return nil, f.err
}

func (f *failingDatabaseClient) SealDatabase(_ context.Context, _ string) error {
	return f.err
}

func (f *failingDatabaseClient) UnsealDatabase(_ context.Context, _, _ string) error {
	return f.err
}

func (f *failingDatabaseClient) DatabaseBarrierStatus(_ context.Context, _ string) (*DatabaseBarrierStatus, error) {
	return nil, f.err
}

// ---------------------------------------------------------------------------
// WithTenantDatabase constructor tests
// ---------------------------------------------------------------------------

func TestWithTenantDatabase_Success(t *testing.T) {
	mock := newMockDatabaseClient()
	tc, err := WithTenantDatabase(mock, "acme")
	if err != nil {
		t.Fatalf("WithTenantDatabase failed: %v", err)
	}
	if tc == nil {
		t.Fatal("expected non-nil TenantDatabaseClient")
	}
	if tc.TenantID() != "acme" {
		t.Errorf("expected tenantID 'acme', got %q", tc.TenantID())
	}
}

func TestWithTenantDatabase_EmptyTenantID(t *testing.T) {
	mock := newMockDatabaseClient()
	_, err := WithTenantDatabase(mock, "")
	if err == nil {
		t.Fatal("expected error for empty tenantID")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("expected ConfigError, got %T", err)
	}
	if cfgErr.Field != "tenantID" {
		t.Errorf("expected field 'tenantID', got %q", cfgErr.Field)
	}
	if !strings.Contains(cfgErr.Reason, "empty") {
		t.Errorf("expected reason to mention 'empty', got %q", cfgErr.Reason)
	}
}

func TestWithTenantDatabase_ForwardSlash(t *testing.T) {
	mock := newMockDatabaseClient()
	_, err := WithTenantDatabase(mock, "acme/corp")
	if err == nil {
		t.Fatal("expected error for tenantID with forward slash")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("expected ConfigError, got %T", err)
	}
	if !strings.Contains(cfgErr.Reason, "path separator") {
		t.Errorf("expected reason about path separators, got %q", cfgErr.Reason)
	}
}

func TestWithTenantDatabase_BackSlash(t *testing.T) {
	mock := newMockDatabaseClient()
	_, err := WithTenantDatabase(mock, "acme\\corp")
	if err == nil {
		t.Fatal("expected error for tenantID with backslash")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("expected ConfigError, got %T", err)
	}
}

func TestWithTenantDatabase_DotDot(t *testing.T) {
	mock := newMockDatabaseClient()
	_, err := WithTenantDatabase(mock, "acme..corp")
	if err == nil {
		t.Fatal("expected error for tenantID with '..'")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("expected ConfigError, got %T", err)
	}
}

func TestWithTenantDatabase_PathTraversal(t *testing.T) {
	mock := newMockDatabaseClient()
	_, err := WithTenantDatabase(mock, "..")
	if err == nil {
		t.Fatal("expected error for tenantID '..'")
	}
}

// ---------------------------------------------------------------------------
// TenantID accessor
// ---------------------------------------------------------------------------

func TestTenantDatabaseClient_TenantID(t *testing.T) {
	mock := newMockDatabaseClient()
	tc, err := WithTenantDatabase(mock, "tenant-42")
	if err != nil {
		t.Fatal(err)
	}
	if tc.TenantID() != "tenant-42" {
		t.Errorf("expected 'tenant-42', got %q", tc.TenantID())
	}
}

func TestTenantDatabaseClient_TenantID_Stable(t *testing.T) {
	mock := newMockDatabaseClient()
	tc, err := WithTenantDatabase(mock, "stable-id")
	if err != nil {
		t.Fatal(err)
	}
	// Calling TenantID multiple times returns the same value.
	if tc.TenantID() != tc.TenantID() {
		t.Error("TenantID returned different values on consecutive calls")
	}
}

// ---------------------------------------------------------------------------
// CreateDatabase tests
// ---------------------------------------------------------------------------

func TestTenantDatabaseClient_CreateDatabase_InjectsTenantID(t *testing.T) {
	mock := newMockDatabaseClient()
	tc, _ := WithTenantDatabase(mock, "acme")
	ctx := context.Background()

	info, err := tc.CreateDatabase(ctx, CreateDatabaseRequest{
		Name:    "mydb",
		Engine:  "kv",
		Backend: "pebble",
	})
	if err != nil {
		t.Fatalf("CreateDatabase failed: %v", err)
	}
	if info.TenantID != "acme" {
		t.Errorf("expected tenantID 'acme' on result, got %q", info.TenantID)
	}
	if info.Name != "mydb" {
		t.Errorf("expected name 'mydb', got %q", info.Name)
	}

	// Verify the underlying mock received the tenant ID.
	stored := mock.databases["mydb"]
	if stored == nil {
		t.Fatal("expected database to be stored in mock")
	}
	if stored.TenantID != "acme" {
		t.Errorf("expected stored tenantID 'acme', got %q", stored.TenantID)
	}
}

func TestTenantDatabaseClient_CreateDatabase_InnerError(t *testing.T) {
	innerErr := &RequestError{Method: "CreateDatabase", Err: errors.New("disk full")}
	tc, _ := WithTenantDatabase(&failingDatabaseClient{err: innerErr}, "acme")

	_, err := tc.CreateDatabase(context.Background(), CreateDatabaseRequest{
		Name:    "mydb",
		Engine:  "kv",
		Backend: "pebble",
	})
	if err == nil {
		t.Fatal("expected error from inner CreateDatabase")
	}
	var reqErr *RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T", err)
	}
}

func TestTenantDatabaseClient_CreateDatabase_OverridesTenantID(t *testing.T) {
	mock := newMockDatabaseClient()
	tc, _ := WithTenantDatabase(mock, "acme")
	ctx := context.Background()

	// Even if the caller sets a different TenantID, it gets overridden.
	info, err := tc.CreateDatabase(ctx, CreateDatabaseRequest{
		TenantID: "evil-tenant",
		Name:     "mydb",
		Engine:   "kv",
		Backend:  "pebble",
	})
	if err != nil {
		t.Fatalf("CreateDatabase failed: %v", err)
	}
	if info.TenantID != "acme" {
		t.Errorf("expected tenantID 'acme' (overridden), got %q", info.TenantID)
	}
}

// ---------------------------------------------------------------------------
// GetDatabase tests
// ---------------------------------------------------------------------------

func TestTenantDatabaseClient_GetDatabase_OwnedByTenant(t *testing.T) {
	mock := newMockDatabaseClient()
	mock.databases["mydb"] = &DatabaseInfo{
		TenantID: "acme",
		Name:     "mydb",
		Engine:   "kv",
		Backend:  "pebble",
		Status:   "open",
	}

	tc, _ := WithTenantDatabase(mock, "acme")
	info, err := tc.GetDatabase(context.Background(), "mydb")
	if err != nil {
		t.Fatalf("GetDatabase failed: %v", err)
	}
	if info.Name != "mydb" {
		t.Errorf("expected name 'mydb', got %q", info.Name)
	}
	if info.TenantID != "acme" {
		t.Errorf("expected tenantID 'acme', got %q", info.TenantID)
	}
}

func TestTenantDatabaseClient_GetDatabase_CrossTenantDenied(t *testing.T) {
	mock := newMockDatabaseClient()
	mock.databases["otherdb"] = &DatabaseInfo{
		TenantID: "other-tenant",
		Name:     "otherdb",
		Engine:   "kv",
		Backend:  "pebble",
		Status:   "open",
	}

	tc, _ := WithTenantDatabase(mock, "acme")
	_, err := tc.GetDatabase(context.Background(), "otherdb")
	if err == nil {
		t.Fatal("expected access denied error for cross-tenant GetDatabase")
	}
	var accessErr *TenantDatabaseAccessDeniedError
	if !errors.As(err, &accessErr) {
		t.Fatalf("expected TenantDatabaseAccessDeniedError, got %T", err)
	}
	if accessErr.TenantID != "acme" {
		t.Errorf("expected tenantID 'acme', got %q", accessErr.TenantID)
	}
	if accessErr.Database != "otherdb" {
		t.Errorf("expected database 'otherdb', got %q", accessErr.Database)
	}
}

func TestTenantDatabaseClient_GetDatabase_NotFound(t *testing.T) {
	mock := newMockDatabaseClient()
	tc, _ := WithTenantDatabase(mock, "acme")

	_, err := tc.GetDatabase(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent database")
	}
	var notFound *DatabaseNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected DatabaseNotFoundError, got %T", err)
	}
}

func TestTenantDatabaseClient_GetDatabase_InnerError(t *testing.T) {
	innerErr := &RequestError{Method: "GetDatabase", Err: errors.New("unavailable")}
	tc, _ := WithTenantDatabase(&failingDatabaseClient{err: innerErr}, "acme")

	_, err := tc.GetDatabase(context.Background(), "mydb")
	if err == nil {
		t.Fatal("expected error from inner GetDatabase")
	}
	var reqErr *RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// ListDatabases tests
// ---------------------------------------------------------------------------

func TestTenantDatabaseClient_ListDatabases_FiltersToTenant(t *testing.T) {
	mock := newMockDatabaseClient()
	mock.databases["acme-db1"] = &DatabaseInfo{TenantID: "acme", Name: "acme-db1", Status: "open"}
	mock.databases["acme-db2"] = &DatabaseInfo{TenantID: "acme", Name: "acme-db2", Status: "open"}
	mock.databases["other-db"] = &DatabaseInfo{TenantID: "other", Name: "other-db", Status: "open"}

	tc, _ := WithTenantDatabase(mock, "acme")
	dbs, err := tc.ListDatabases(context.Background())
	if err != nil {
		t.Fatalf("ListDatabases failed: %v", err)
	}
	if len(dbs) != 2 {
		t.Fatalf("expected 2 databases, got %d", len(dbs))
	}
	for _, db := range dbs {
		if db.TenantID != "acme" {
			t.Errorf("expected all databases to belong to 'acme', got tenantID %q", db.TenantID)
		}
	}
}

func TestTenantDatabaseClient_ListDatabases_EmptyResult(t *testing.T) {
	mock := newMockDatabaseClient()
	mock.databases["other-db"] = &DatabaseInfo{TenantID: "other", Name: "other-db", Status: "open"}

	tc, _ := WithTenantDatabase(mock, "acme")
	dbs, err := tc.ListDatabases(context.Background())
	if err != nil {
		t.Fatalf("ListDatabases failed: %v", err)
	}
	if len(dbs) != 0 {
		t.Errorf("expected 0 databases, got %d", len(dbs))
	}
}

func TestTenantDatabaseClient_ListDatabases_InnerError(t *testing.T) {
	innerErr := &RequestError{Method: "ListDatabases", Err: errors.New("unavailable")}
	tc, _ := WithTenantDatabase(&failingDatabaseClient{err: innerErr}, "acme")

	_, err := tc.ListDatabases(context.Background())
	if err == nil {
		t.Fatal("expected error from inner ListDatabases")
	}
	var reqErr *RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// DropDatabase tests
// ---------------------------------------------------------------------------

func TestTenantDatabaseClient_DropDatabase_OwnedByTenant(t *testing.T) {
	mock := newMockDatabaseClient()
	mock.databases["mydb"] = &DatabaseInfo{TenantID: "acme", Name: "mydb", Status: "open"}

	tc, _ := WithTenantDatabase(mock, "acme")
	err := tc.DropDatabase(context.Background(), "mydb")
	if err != nil {
		t.Fatalf("DropDatabase failed: %v", err)
	}
	if _, ok := mock.databases["mydb"]; ok {
		t.Error("expected database to be removed from mock")
	}
}

func TestTenantDatabaseClient_DropDatabase_CrossTenantDenied(t *testing.T) {
	mock := newMockDatabaseClient()
	mock.databases["otherdb"] = &DatabaseInfo{TenantID: "other-tenant", Name: "otherdb", Status: "open"}

	tc, _ := WithTenantDatabase(mock, "acme")
	err := tc.DropDatabase(context.Background(), "otherdb")
	if err == nil {
		t.Fatal("expected access denied error for cross-tenant DropDatabase")
	}
	var accessErr *TenantDatabaseAccessDeniedError
	if !errors.As(err, &accessErr) {
		t.Fatalf("expected TenantDatabaseAccessDeniedError, got %T", err)
	}
	// Verify the database was NOT dropped.
	if _, ok := mock.databases["otherdb"]; !ok {
		t.Error("expected database to still exist after denied drop")
	}
}

func TestTenantDatabaseClient_DropDatabase_NotFound(t *testing.T) {
	mock := newMockDatabaseClient()
	tc, _ := WithTenantDatabase(mock, "acme")

	err := tc.DropDatabase(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent database")
	}
	var notFound *DatabaseNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected DatabaseNotFoundError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// UpdateDatabaseProtocol tests
// ---------------------------------------------------------------------------

func TestTenantDatabaseClient_UpdateProtocol_Success(t *testing.T) {
	mock := newMockDatabaseClient()
	mock.databases["mydb"] = &DatabaseInfo{TenantID: "acme", Name: "mydb", Status: "open"}

	tc, _ := WithTenantDatabase(mock, "acme")
	info, err := tc.UpdateDatabaseProtocol(context.Background(), UpdateDatabaseProtocolRequest{
		Name:  "mydb",
		MySQL: true,
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseProtocol failed: %v", err)
	}
	if !info.Protocol.MySQL {
		t.Error("expected MySQL protocol to be enabled")
	}
}

func TestTenantDatabaseClient_UpdateProtocol_CrossTenantDenied(t *testing.T) {
	mock := newMockDatabaseClient()
	mock.databases["otherdb"] = &DatabaseInfo{TenantID: "other-tenant", Name: "otherdb", Status: "open"}

	tc, _ := WithTenantDatabase(mock, "acme")
	_, err := tc.UpdateDatabaseProtocol(context.Background(), UpdateDatabaseProtocolRequest{
		Name:  "otherdb",
		MySQL: true,
	})
	if err == nil {
		t.Fatal("expected access denied error for cross-tenant UpdateDatabaseProtocol")
	}
	var accessErr *TenantDatabaseAccessDeniedError
	if !errors.As(err, &accessErr) {
		t.Fatalf("expected TenantDatabaseAccessDeniedError, got %T", err)
	}
}

func TestTenantDatabaseClient_UpdateProtocol_NotFound(t *testing.T) {
	mock := newMockDatabaseClient()
	tc, _ := WithTenantDatabase(mock, "acme")

	_, err := tc.UpdateDatabaseProtocol(context.Background(), UpdateDatabaseProtocolRequest{
		Name:  "nonexistent",
		MySQL: true,
	})
	if err == nil {
		t.Fatal("expected error for nonexistent database")
	}
	var notFound *DatabaseNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected DatabaseNotFoundError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// UpdateDatabaseEtcd tests
// ---------------------------------------------------------------------------

func TestTenantDatabaseClient_UpdateEtcd_Success(t *testing.T) {
	mock := newMockDatabaseClient()
	mock.databases["mydb"] = &DatabaseInfo{TenantID: "acme", Name: "mydb", Status: "open"}

	tc, _ := WithTenantDatabase(mock, "acme")
	info, err := tc.UpdateDatabaseEtcd(context.Background(), UpdateDatabaseEtcdRequest{
		Name:       "mydb",
		Enabled:    true,
		ListenAddr: "0.0.0.0:2379",
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseEtcd failed: %v", err)
	}
	if info.Protocol.Etcd == nil {
		t.Fatal("expected etcd protocol config to be set")
	}
	if !info.Protocol.Etcd.Enabled {
		t.Error("expected etcd to be enabled")
	}
}

func TestTenantDatabaseClient_UpdateEtcd_CrossTenantDenied(t *testing.T) {
	mock := newMockDatabaseClient()
	mock.databases["otherdb"] = &DatabaseInfo{TenantID: "other-tenant", Name: "otherdb", Status: "open"}

	tc, _ := WithTenantDatabase(mock, "acme")
	_, err := tc.UpdateDatabaseEtcd(context.Background(), UpdateDatabaseEtcdRequest{
		Name:    "otherdb",
		Enabled: true,
	})
	if err == nil {
		t.Fatal("expected access denied error")
	}
	var accessErr *TenantDatabaseAccessDeniedError
	if !errors.As(err, &accessErr) {
		t.Fatalf("expected TenantDatabaseAccessDeniedError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// UpdateDatabaseRaft tests
// ---------------------------------------------------------------------------

func TestTenantDatabaseClient_UpdateRaft_Success(t *testing.T) {
	mock := newMockDatabaseClient()
	mock.databases["mydb"] = &DatabaseInfo{TenantID: "acme", Name: "mydb", Status: "open"}

	tc, _ := WithTenantDatabase(mock, "acme")
	info, err := tc.UpdateDatabaseRaft(context.Background(), UpdateDatabaseRaftRequest{
		Name: "mydb",
		Raft: &DatabaseRaftConfig{ClusterMode: "multi"},
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseRaft failed: %v", err)
	}
	if info.Raft == nil {
		t.Fatal("expected raft config to be set")
	}
	if info.Raft.ClusterMode != "multi" {
		t.Errorf("expected cluster mode 'multi', got %q", info.Raft.ClusterMode)
	}
}

func TestTenantDatabaseClient_UpdateRaft_CrossTenantDenied(t *testing.T) {
	mock := newMockDatabaseClient()
	mock.databases["otherdb"] = &DatabaseInfo{TenantID: "other-tenant", Name: "otherdb", Status: "open"}

	tc, _ := WithTenantDatabase(mock, "acme")
	_, err := tc.UpdateDatabaseRaft(context.Background(), UpdateDatabaseRaftRequest{
		Name: "otherdb",
		Raft: &DatabaseRaftConfig{ClusterMode: "multi"},
	})
	if err == nil {
		t.Fatal("expected access denied error")
	}
	var accessErr *TenantDatabaseAccessDeniedError
	if !errors.As(err, &accessErr) {
		t.Fatalf("expected TenantDatabaseAccessDeniedError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// UpdateDatabaseBarrier tests
// ---------------------------------------------------------------------------

func TestTenantDatabaseClient_UpdateBarrier_Success(t *testing.T) {
	mock := newMockDatabaseClient()
	mock.databases["mydb"] = &DatabaseInfo{TenantID: "acme", Name: "mydb", Status: "open"}

	tc, _ := WithTenantDatabase(mock, "acme")
	info, err := tc.UpdateDatabaseBarrier(context.Background(), UpdateDatabaseBarrierRequest{
		Name:    "mydb",
		Barrier: &DatabaseBarrierConfig{Enabled: true, Mode: "shamir"},
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseBarrier failed: %v", err)
	}
	if info.Barrier == nil {
		t.Fatal("expected barrier config to be set")
	}
	if !info.Barrier.Enabled {
		t.Error("expected barrier to be enabled")
	}
}

func TestTenantDatabaseClient_UpdateBarrier_CrossTenantDenied(t *testing.T) {
	mock := newMockDatabaseClient()
	mock.databases["otherdb"] = &DatabaseInfo{TenantID: "other-tenant", Name: "otherdb", Status: "open"}

	tc, _ := WithTenantDatabase(mock, "acme")
	_, err := tc.UpdateDatabaseBarrier(context.Background(), UpdateDatabaseBarrierRequest{
		Name:    "otherdb",
		Barrier: &DatabaseBarrierConfig{Enabled: true},
	})
	if err == nil {
		t.Fatal("expected access denied error")
	}
	var accessErr *TenantDatabaseAccessDeniedError
	if !errors.As(err, &accessErr) {
		t.Fatalf("expected TenantDatabaseAccessDeniedError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// UpdateDatabaseConnection tests
// ---------------------------------------------------------------------------

func TestTenantDatabaseClient_UpdateConnection_Success(t *testing.T) {
	mock := newMockDatabaseClient()
	mock.databases["mydb"] = &DatabaseInfo{TenantID: "acme", Name: "mydb", Status: "open"}

	tc, _ := WithTenantDatabase(mock, "acme")
	info, err := tc.UpdateDatabaseConnection(context.Background(), UpdateDatabaseConnectionRequest{
		Name:       "mydb",
		Connection: &DatabaseConnectionConfig{Transport: "quic", Address: "10.0.0.1:9000"},
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseConnection failed: %v", err)
	}
	if info.Connection == nil {
		t.Fatal("expected connection config to be set")
	}
	if info.Connection.Transport != "quic" {
		t.Errorf("expected transport 'quic', got %q", info.Connection.Transport)
	}
}

func TestTenantDatabaseClient_UpdateConnection_CrossTenantDenied(t *testing.T) {
	mock := newMockDatabaseClient()
	mock.databases["otherdb"] = &DatabaseInfo{TenantID: "other-tenant", Name: "otherdb", Status: "open"}

	tc, _ := WithTenantDatabase(mock, "acme")
	_, err := tc.UpdateDatabaseConnection(context.Background(), UpdateDatabaseConnectionRequest{
		Name:       "otherdb",
		Connection: &DatabaseConnectionConfig{Transport: "quic", Address: "10.0.0.1:9000"},
	})
	if err == nil {
		t.Fatal("expected access denied error")
	}
	var accessErr *TenantDatabaseAccessDeniedError
	if !errors.As(err, &accessErr) {
		t.Fatalf("expected TenantDatabaseAccessDeniedError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// Error message tests
// ---------------------------------------------------------------------------

func TestTenantDatabaseAccessDeniedError_Message(t *testing.T) {
	err := &TenantDatabaseAccessDeniedError{TenantID: "acme", Database: "secret-db"}
	msg := err.Error()
	if !strings.Contains(msg, "acme") {
		t.Errorf("expected error message to contain 'acme', got %q", msg)
	}
	if !strings.Contains(msg, "secret-db") {
		t.Errorf("expected error message to contain 'secret-db', got %q", msg)
	}
	if !strings.Contains(msg, "access denied") {
		t.Errorf("expected error message to contain 'access denied', got %q", msg)
	}
}

func TestTenantDatabaseAccessDeniedError_ErrorInterface(t *testing.T) {
	var err error = &TenantDatabaseAccessDeniedError{TenantID: "t1", Database: "db1"}
	if err.Error() == "" {
		t.Error("expected non-empty error message")
	}
}

// ---------------------------------------------------------------------------
// Compile-time interface check verification
// ---------------------------------------------------------------------------

func TestTenantDatabaseClient_ImplementsDatabaseClient(t *testing.T) {
	var _ DatabaseClient = (*TenantDatabaseClient)(nil)
}

// ---------------------------------------------------------------------------
// Multi-tenant isolation test
// ---------------------------------------------------------------------------

func TestTenantDatabaseClient_Isolation(t *testing.T) {
	mock := newMockDatabaseClient()
	ctx := context.Background()

	tc1, _ := WithTenantDatabase(mock, "tenant1")
	tc2, _ := WithTenantDatabase(mock, "tenant2")

	// Each tenant creates a database.
	_, err := tc1.CreateDatabase(ctx, CreateDatabaseRequest{Name: "shared-name", Engine: "kv", Backend: "pebble"})
	if err != nil {
		t.Fatal(err)
	}

	// tenant2 tries to access tenant1's database.
	_, err = tc2.GetDatabase(ctx, "shared-name")
	if err == nil {
		t.Fatal("expected access denied for cross-tenant GetDatabase")
	}
	var accessErr *TenantDatabaseAccessDeniedError
	if !errors.As(err, &accessErr) {
		t.Fatalf("expected TenantDatabaseAccessDeniedError, got %T", err)
	}

	// tenant2 tries to drop tenant1's database.
	err = tc2.DropDatabase(ctx, "shared-name")
	if err == nil {
		t.Fatal("expected access denied for cross-tenant DropDatabase")
	}
	if !errors.As(err, &accessErr) {
		t.Fatalf("expected TenantDatabaseAccessDeniedError, got %T", err)
	}

	// tenant1 can still see its database.
	dbs, err := tc1.ListDatabases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(dbs) != 1 {
		t.Errorf("expected 1 database for tenant1, got %d", len(dbs))
	}

	// tenant2 sees no databases.
	dbs, err = tc2.ListDatabases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(dbs) != 0 {
		t.Errorf("expected 0 databases for tenant2, got %d", len(dbs))
	}
}

// ---------------------------------------------------------------------------
// CRUD roundtrip test
// ---------------------------------------------------------------------------

func TestTenantDatabaseClient_CreateGetDropRoundtrip(t *testing.T) {
	mock := newMockDatabaseClient()
	tc, _ := WithTenantDatabase(mock, "acme")
	ctx := context.Background()

	// Create.
	info, err := tc.CreateDatabase(ctx, CreateDatabaseRequest{
		Name:    "roundtrip-db",
		Engine:  "kv",
		Backend: "pebble",
	})
	if err != nil {
		t.Fatalf("CreateDatabase failed: %v", err)
	}
	if info.Name != "roundtrip-db" {
		t.Errorf("expected name 'roundtrip-db', got %q", info.Name)
	}

	// Get.
	info, err = tc.GetDatabase(ctx, "roundtrip-db")
	if err != nil {
		t.Fatalf("GetDatabase failed: %v", err)
	}
	if info.TenantID != "acme" {
		t.Errorf("expected tenantID 'acme', got %q", info.TenantID)
	}

	// List.
	dbs, err := tc.ListDatabases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(dbs) != 1 {
		t.Errorf("expected 1 database, got %d", len(dbs))
	}

	// Drop.
	err = tc.DropDatabase(ctx, "roundtrip-db")
	if err != nil {
		t.Fatalf("DropDatabase failed: %v", err)
	}

	// Get after drop should fail.
	_, err = tc.GetDatabase(ctx, "roundtrip-db")
	if err == nil {
		t.Fatal("expected error after drop")
	}
	var notFound *DatabaseNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected DatabaseNotFoundError, got %T", err)
	}

	// List after drop should be empty.
	dbs, err = tc.ListDatabases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(dbs) != 0 {
		t.Errorf("expected 0 databases after drop, got %d", len(dbs))
	}
}

// ---------------------------------------------------------------------------
// SealDatabase tests
// ---------------------------------------------------------------------------

func TestTenantDatabaseClient_SealDatabase_OwnedByTenant(t *testing.T) {
	mock := newMockDatabaseClient()
	mock.databases["mydb"] = &DatabaseInfo{TenantID: "acme", Name: "mydb", Status: "open"}

	tc, _ := WithTenantDatabase(mock, "acme")
	if err := tc.SealDatabase(context.Background(), "mydb"); err != nil {
		t.Fatalf("SealDatabase failed: %v", err)
	}
}

func TestTenantDatabaseClient_SealDatabase_CrossTenantDenied(t *testing.T) {
	mock := newMockDatabaseClient()
	mock.databases["otherdb"] = &DatabaseInfo{TenantID: "other-tenant", Name: "otherdb", Status: "open"}

	tc, _ := WithTenantDatabase(mock, "acme")
	err := tc.SealDatabase(context.Background(), "otherdb")
	if err == nil {
		t.Fatal("expected access denied error for cross-tenant SealDatabase")
	}
	var accessErr *TenantDatabaseAccessDeniedError
	if !errors.As(err, &accessErr) {
		t.Fatalf("expected TenantDatabaseAccessDeniedError, got %T", err)
	}
}

func TestTenantDatabaseClient_SealDatabase_GetError(t *testing.T) {
	innerErr := &RequestError{Method: "GetDatabase", Err: errors.New("unavailable")}
	tc, _ := WithTenantDatabase(&failingDatabaseClient{err: innerErr}, "acme")

	err := tc.SealDatabase(context.Background(), "mydb")
	if err == nil {
		t.Fatal("expected error propagated from GetDatabase")
	}
}

// ---------------------------------------------------------------------------
// UnsealDatabase tests
// ---------------------------------------------------------------------------

func TestTenantDatabaseClient_UnsealDatabase_OwnedByTenant(t *testing.T) {
	mock := newMockDatabaseClient()
	mock.databases["mydb"] = &DatabaseInfo{TenantID: "acme", Name: "mydb", Status: "sealed"}

	tc, _ := WithTenantDatabase(mock, "acme")
	if err := tc.UnsealDatabase(context.Background(), "mydb", "secret123"); err != nil {
		t.Fatalf("UnsealDatabase failed: %v", err)
	}
}

func TestTenantDatabaseClient_UnsealDatabase_CrossTenantDenied(t *testing.T) {
	mock := newMockDatabaseClient()
	mock.databases["otherdb"] = &DatabaseInfo{TenantID: "other-tenant", Name: "otherdb", Status: "sealed"}

	tc, _ := WithTenantDatabase(mock, "acme")
	err := tc.UnsealDatabase(context.Background(), "otherdb", "secret")
	if err == nil {
		t.Fatal("expected access denied error for cross-tenant UnsealDatabase")
	}
	var accessErr *TenantDatabaseAccessDeniedError
	if !errors.As(err, &accessErr) {
		t.Fatalf("expected TenantDatabaseAccessDeniedError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// DatabaseBarrierStatus tests
// ---------------------------------------------------------------------------

func TestTenantDatabaseClient_DatabaseBarrierStatus_OwnedByTenant(t *testing.T) {
	mock := newMockDatabaseClient()
	mock.databases["mydb"] = &DatabaseInfo{TenantID: "acme", Name: "mydb", Status: "open"}

	tc, _ := WithTenantDatabase(mock, "acme")
	status, err := tc.DatabaseBarrierStatus(context.Background(), "mydb")
	if err != nil {
		t.Fatalf("DatabaseBarrierStatus failed: %v", err)
	}
	if status == nil {
		t.Fatal("expected non-nil DatabaseBarrierStatus")
	}
	if status.Sealed {
		t.Error("expected unsealed status from mock")
	}
}

func TestTenantDatabaseClient_DatabaseBarrierStatus_CrossTenantDenied(t *testing.T) {
	mock := newMockDatabaseClient()
	mock.databases["otherdb"] = &DatabaseInfo{TenantID: "other-tenant", Name: "otherdb", Status: "open"}

	tc, _ := WithTenantDatabase(mock, "acme")
	_, err := tc.DatabaseBarrierStatus(context.Background(), "otherdb")
	if err == nil {
		t.Fatal("expected access denied error for cross-tenant DatabaseBarrierStatus")
	}
	var accessErr *TenantDatabaseAccessDeniedError
	if !errors.As(err, &accessErr) {
		t.Fatalf("expected TenantDatabaseAccessDeniedError, got %T", err)
	}
}

func TestTenantDatabaseClient_DatabaseBarrierStatus_NotFound(t *testing.T) {
	mock := newMockDatabaseClient()
	tc, _ := WithTenantDatabase(mock, "acme")

	_, err := tc.DatabaseBarrierStatus(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent database")
	}
	var notFound *DatabaseNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected DatabaseNotFoundError, got %T", err)
	}
}
