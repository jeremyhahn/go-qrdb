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
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// mockLockSvc is a tag-free lock mock (handlers_lock_test.go's
// mockLockService is a different type under full build tags), so every test
// file can reference it unconditionally.

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

// TestCollectionInfoToProto_Fields is unique (not in handlers_collection_database_test.go).
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
