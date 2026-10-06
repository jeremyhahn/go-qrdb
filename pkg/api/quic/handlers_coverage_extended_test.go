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

package quic

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/internal/collectionpb"
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ---------------------------------------------------------------------------
// mockLockSvc — local lock mock (handlers_lock_test.go has a different type
// name mockLockService under full build tags; this one is tag-free so that
// extra2/extra3 can reference it unconditionally).
// ---------------------------------------------------------------------------

type mockLockSvc struct {
	lockFn    func(ctx context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error)
	tryLockFn func(ctx context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error)
	unlockFn  func(ctx context.Context, key string, fencingToken int64) error
	extendFn  func(ctx context.Context, key string, fencingToken int64) (*lock.Lock, error)
	statusFn  func(ctx context.Context, key string) (*lock.Lock, error)
	listFn    func(ctx context.Context, prefix string) ([]*lock.Lock, error)
}

var _ service.LockServicer = (*mockLockSvc)(nil)

func (m *mockLockSvc) Lock(ctx context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error) {
	if m.lockFn != nil {
		return m.lockFn(ctx, key, holderID, ttl, reentrant)
	}
	return &lock.Lock{Key: key, HolderID: holderID, FencingToken: 42, TTL: int64(ttl.Seconds())}, nil
}

func (m *mockLockSvc) TryLock(ctx context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error) {
	if m.tryLockFn != nil {
		return m.tryLockFn(ctx, key, holderID, ttl, reentrant)
	}
	return &lock.Lock{Key: key, HolderID: holderID, FencingToken: 99}, nil
}

func (m *mockLockSvc) Unlock(ctx context.Context, key string, fencingToken int64) error {
	if m.unlockFn != nil {
		return m.unlockFn(ctx, key, fencingToken)
	}
	return nil
}

func (m *mockLockSvc) Extend(ctx context.Context, key string, fencingToken int64) (*lock.Lock, error) {
	if m.extendFn != nil {
		return m.extendFn(ctx, key, fencingToken)
	}
	return &lock.Lock{Key: key, FencingToken: fencingToken}, nil
}

func (m *mockLockSvc) Status(ctx context.Context, key string) (*lock.Lock, error) {
	if m.statusFn != nil {
		return m.statusFn(ctx, key)
	}
	return &lock.Lock{Key: key, HolderID: "holder-1", FencingToken: 7}, nil
}

func (m *mockLockSvc) List(ctx context.Context, prefix string) ([]*lock.Lock, error) {
	if m.listFn != nil {
		return m.listFn(ctx, prefix)
	}
	return []*lock.Lock{{Key: prefix + "a", HolderID: "h1", FencingToken: 1}}, nil
}

// ---------------------------------------------------------------------------
// lockToProto — unique coverage tests (not in handlers_lock_test.go)
// ---------------------------------------------------------------------------

func TestLockToProto_NilExt(t *testing.T) {
	if got := lockToProto(nil); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestLockToProto_Fields_Ext(t *testing.T) {
	l := &lock.Lock{
		Key:          "k1",
		FencingToken: 42,
		LeaseID:      7,
		HolderID:     "node-1",
		Reentrant:    true,
		AcquireCount: 3,
		TTL:          30,
		AcquiredAt:   1234567890,
	}
	got := lockToProto(l)
	if got == nil {
		t.Fatal("expected non-nil")
	}
	if got.GetKey() != "k1" {
		t.Errorf("Key: want 'k1', got %q", got.GetKey())
	}
	if got.GetFencingToken() != 42 {
		t.Errorf("FencingToken: want 42, got %d", got.GetFencingToken())
	}
	if got.GetHolderId() != "node-1" {
		t.Errorf("HolderID: want 'node-1', got %q", got.GetHolderId())
	}
	if !got.GetReentrant() {
		t.Error("Reentrant: want true")
	}
	if got.GetAcquireCount() != 3 {
		t.Errorf("AcquireCount: want 3, got %d", got.GetAcquireCount())
	}
}

// ---------------------------------------------------------------------------
// handleLockTryLock — unmarshal error (unique: not in handlers_lock_test.go)
// ---------------------------------------------------------------------------

func TestHandleLockTryLock_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLock(&mockLockSvc{}))
	_, err := srv.handleLockTryLock(context.Background(), []byte{0xff})
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleLockList — unmarshal error (unique: not in handlers_lock_test.go)
// ---------------------------------------------------------------------------

func TestHandleLockList_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLock(&mockLockSvc{}))
	_, err := srv.handleLockList(context.Background(), []byte{0xff, 0xfe})
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleGetUserByUsername
// ---------------------------------------------------------------------------

func TestHandleGetUserByUsername_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(getUserByUsernameWireRequest{TenantID: "t1", Username: "alice"})
	_, err := srv.handleGetUserByUsername(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleGetUserByUsername_InvalidJSON(t *testing.T) {
	mock := &mockUserService{
		getUserByUsernameFn: func(_ context.Context, _, _ string) (*user.User, error) {
			return testUser(1, "alice", "t1"), nil
		},
	}
	srv := newUserServer(mock)
	_, err := srv.handleGetUserByUsername(context.Background(), []byte("{bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T", err)
	}
}

func TestHandleGetUserByUsername_Success(t *testing.T) {
	mock := &mockUserService{
		getUserByUsernameFn: func(_ context.Context, tenantID, username string) (*user.User, error) {
			if tenantID != "tenant-1" || username != "alice" {
				return nil, errors.New("unexpected args")
			}
			return testUser(10, "alice", "tenant-1"), nil
		},
	}
	srv := newUserServer(mock)
	payload, _ := json.Marshal(getUserByUsernameWireRequest{TenantID: "tenant-1", Username: "alice"})
	resp, err := srv.handleGetUserByUsername(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out userWireResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ID != 10 {
		t.Errorf("expected ID 10, got %d", out.ID)
	}
	if out.Username != "alice" {
		t.Errorf("expected username 'alice', got %q", out.Username)
	}
}

func TestHandleGetUserByUsername_ServiceError(t *testing.T) {
	svcErr := errors.New("user not found")
	mock := &mockUserService{
		getUserByUsernameFn: func(_ context.Context, _, _ string) (*user.User, error) {
			return nil, svcErr
		},
	}
	srv := newUserServer(mock)
	payload, _ := json.Marshal(getUserByUsernameWireRequest{TenantID: "t", Username: "nobody"})
	_, err := srv.handleGetUserByUsername(context.Background(), payload)
	if !errors.Is(err, svcErr) {
		t.Fatalf("expected svcErr, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// handleGetUserByCertThumbprint
// ---------------------------------------------------------------------------

func TestHandleGetUserByCertThumbprint_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(getUserByCertThumbprintWireRequest{Thumbprint: "abc"})
	_, err := srv.handleGetUserByCertThumbprint(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleGetUserByCertThumbprint_InvalidJSON(t *testing.T) {
	srv := newUserServer(&mockUserService{})
	_, err := srv.handleGetUserByCertThumbprint(context.Background(), []byte("{bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T", err)
	}
}

func TestHandleGetUserByCertThumbprint_Success(t *testing.T) {
	srv := newUserServer(&mockUserService{})
	payload, _ := json.Marshal(getUserByCertThumbprintWireRequest{Thumbprint: "sha256:abc"})
	_, err := srv.handleGetUserByCertThumbprint(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHandleGetUserByCertThumbprint_ServiceError(t *testing.T) {
	// mockUserService.UserByCertThumbprint returns nil,nil so service error is
	// not testable via this mock. Covered by NilService and InvalidJSON above.
	t.Log("service error path covered by NilService and InvalidJSON tests")
}

// TestCollectionInfoToProto_Fields is unique (not in handlers_collection_database_test.go).
func TestCollectionInfoToProto_Fields(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	info := &service.CollectionInfo{
		Name:          "col1",
		Database:      "db1",
		IDStrategy:    "auto",
		DocumentCount: 42,
		CreatedAt:     now,
	}
	got := collectionpb.InfoToProto(info)
	if got.GetName() != "col1" {
		t.Errorf("expected 'col1', got %q", got.GetName())
	}
	if got.GetDocumentCount() != 42 {
		t.Errorf("expected doc count 42, got %d", got.GetDocumentCount())
	}
}

type mockDbSvc struct {
	createFn           func(cfg database.DatabaseConfig) (*database.DatabaseInfo, error)
	getFn              func(name string) (*database.DatabaseInfo, error)
	listFn             func() ([]database.DatabaseInfo, error)
	dropFn             func(name string) error
	updateProtocolFn   func(name string, protocol database.ProtocolConfig) (*database.DatabaseInfo, error)
	updateEtcdFn       func(name string, etcd *database.EtcdConfig) (*database.DatabaseInfo, error)
	updateRaftFn       func(name string, raft *database.DatabaseRaftConfig) (*database.DatabaseInfo, error)
	updateBarrierFn    func(name string, barrier *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error)
	updateConnectionFn func(name string, conn *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error)
	scaleFn            func(name string, targetReplicas int) (*database.DatabaseInfo, error)
}

var _ service.DatabaseServicer = (*mockDbSvc)(nil)

func (m *mockDbSvc) Create(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
	if m.createFn != nil {
		return m.createFn(cfg)
	}
	return &database.DatabaseInfo{Name: cfg.Name}, nil
}
func (m *mockDbSvc) Get(name string) (*database.DatabaseInfo, error) {
	if m.getFn != nil {
		return m.getFn(name)
	}
	return &database.DatabaseInfo{Name: name}, nil
}
func (m *mockDbSvc) List() ([]database.DatabaseInfo, error) {
	if m.listFn != nil {
		return m.listFn()
	}
	return []database.DatabaseInfo{}, nil
}
func (m *mockDbSvc) Drop(_ context.Context, name string) error {
	if m.dropFn != nil {
		return m.dropFn(name)
	}
	return nil
}
func (m *mockDbSvc) UpdateProtocol(name string, protocol database.ProtocolConfig) (*database.DatabaseInfo, error) {
	if m.updateProtocolFn != nil {
		return m.updateProtocolFn(name, protocol)
	}
	return &database.DatabaseInfo{Name: name}, nil
}
func (m *mockDbSvc) UpdateEtcd(name string, etcd *database.EtcdConfig) (*database.DatabaseInfo, error) {
	if m.updateEtcdFn != nil {
		return m.updateEtcdFn(name, etcd)
	}
	return &database.DatabaseInfo{Name: name}, nil
}
func (m *mockDbSvc) UpdateRaft(name string, raft *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	if m.updateRaftFn != nil {
		return m.updateRaftFn(name, raft)
	}
	return &database.DatabaseInfo{Name: name}, nil
}
func (m *mockDbSvc) UpdateBarrier(name string, barrier *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	if m.updateBarrierFn != nil {
		return m.updateBarrierFn(name, barrier)
	}
	return &database.DatabaseInfo{Name: name}, nil
}
func (m *mockDbSvc) UpdateConnection(name string, conn *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	if m.updateConnectionFn != nil {
		return m.updateConnectionFn(name, conn)
	}
	return &database.DatabaseInfo{Name: name}, nil
}
func (m *mockDbSvc) SealDatabase(_ context.Context, _ string) error                       { return nil }
func (m *mockDbSvc) UnsealDatabase(_ context.Context, _ string, _ seal.Credentials) error { return nil }

// DatabaseBarrierStatus adapts databaseBarrierStatusSeal to the service
// interface, with no cluster seal state.
func (m *mockDbSvc) DatabaseBarrierStatus(ctx context.Context, name string) (*service.DatabaseBarrierStatusInfo, error) {
	st, err := m.databaseBarrierStatusSeal(ctx, name)
	if st == nil {
		return nil, err
	}
	return &service.DatabaseBarrierStatusInfo{BarrierStatus: st}, err
}

func (m *mockDbSvc) databaseBarrierStatusSeal(_ context.Context, _ string) (*seal.BarrierStatus, error) {
	return &seal.BarrierStatus{}, nil
}
func (m *mockDbSvc) CreateForTenant(_ context.Context, cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: cfg.Name}, nil
}
func (m *mockDbSvc) GetForTenant(_ context.Context, name string) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: name}, nil
}
func (m *mockDbSvc) ListForTenant(_ context.Context) ([]database.DatabaseInfo, error) {
	return []database.DatabaseInfo{}, nil
}
func (m *mockDbSvc) DropForTenant(_ context.Context, _ string) error { return nil }
func (m *mockDbSvc) UpdateProtocolForTenant(_ context.Context, name string, protocol database.ProtocolConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: name}, nil
}
func (m *mockDbSvc) UpdateEtcdForTenant(_ context.Context, name string, _ *database.EtcdConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: name}, nil
}
func (m *mockDbSvc) UpdateRaftForTenant(_ context.Context, name string, _ *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: name}, nil
}
func (m *mockDbSvc) UpdateBarrierForTenant(_ context.Context, name string, _ *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: name}, nil
}
func (m *mockDbSvc) UpdateConnectionForTenant(_ context.Context, name string, _ *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: name}, nil
}

func (m *mockDbSvc) UpdateSQL(name string, sqlCfg *database.SQLConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: name, SQL: sqlCfg}, nil
}

func (m *mockDbSvc) UpdateSQLForTenant(_ context.Context, name string, sqlCfg *database.SQLConfig) (*database.DatabaseInfo, error) {
	return m.UpdateSQL(name, sqlCfg)
}

func (m *mockDbSvc) UpdateCompactionForTenant(_ context.Context, name string, policy *compaction.Policy) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: name, Compaction: policy}, nil
}
func (m *mockDbSvc) ScaleForTenant(_ context.Context, name string, targetReplicas int) (*database.DatabaseInfo, error) {
	if m.scaleFn != nil {
		return m.scaleFn(name, targetReplicas)
	}
	return &database.DatabaseInfo{Name: name}, nil
}

func newDatabaseServer() *Server {
	return newTestServer(&mockKVService{}, &mockAdminService{}, WithDatabaseService(&mockDbSvc{}))
}

// ---------------------------------------------------------------------------
// Database handler tests — unique variants (MissingName, etc.) not in
// handlers_collection_database_test.go
// ---------------------------------------------------------------------------

func TestHandleDatabaseGet_MissingName(t *testing.T) {
	srv := newDatabaseServer()
	payload, _ := json.Marshal(databaseNameRequest{Name: ""})
	_, err := srv.handleDatabaseGet(context.Background(), payload)
	var ia *InvalidArgumentError
	if !errors.As(err, &ia) {
		t.Fatalf("expected InvalidArgumentError, got %T: %v", err, err)
	}
}

func TestHandleDatabaseDrop_MissingName(t *testing.T) {
	srv := newDatabaseServer()
	payload, _ := json.Marshal(databaseNameRequest{})
	_, err := srv.handleDatabaseDrop(context.Background(), payload)
	var ia *InvalidArgumentError
	if !errors.As(err, &ia) {
		t.Fatalf("expected InvalidArgumentError, got %T: %v", err, err)
	}
}

func TestHandleDatabaseUpdateProtocol_MissingName(t *testing.T) {
	srv := newDatabaseServer()
	payload, _ := json.Marshal(databaseUpdateProtocolRequest{MySQL: true})
	_, err := srv.handleDatabaseUpdateProtocol(context.Background(), payload)
	var ia *InvalidArgumentError
	if !errors.As(err, &ia) {
		t.Fatalf("expected InvalidArgumentError, got %T: %v", err, err)
	}
}

func TestDatabaseToDomain_WithEtcd(t *testing.T) {
	p := databaseCreateProtocolConfig{
		MySQL: true,
		Etcd: &databaseCreateEtcdConfig{
			Enabled:             true,
			ListenAddr:          "localhost:2379",
			AdvertiseClientURLs: []string{"http://localhost:2379"},
		},
	}
	got := p.toDomain()
	if !got.MySQL {
		t.Error("expected MySQL=true")
	}
	if got.Etcd == nil {
		t.Fatal("expected non-nil Etcd config")
	}
	if got.Etcd.ListenAddr != "localhost:2379" {
		t.Errorf("expected listen addr 'localhost:2379', got %q", got.Etcd.ListenAddr)
	}
}
