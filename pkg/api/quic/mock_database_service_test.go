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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package quic

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// mockDatabaseServiceQuic implements service.DatabaseServicer for the
// database-barrier handler tests in handlers_database_barrier_test.go.

type mockDatabaseServiceQuic struct {
	sealFn          func(ctx context.Context, name string) error
	unsealFn        func(ctx context.Context, name string, creds seal.Credentials) error
	barrierStatusFn func(name string) (*seal.BarrierStatus, error)
}

func (m *mockDatabaseServiceQuic) Create(_ database.DatabaseConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{}, nil
}
func (m *mockDatabaseServiceQuic) Get(_ string) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{}, nil
}
func (m *mockDatabaseServiceQuic) List() ([]database.DatabaseInfo, error) {
	return []database.DatabaseInfo{}, nil
}
func (m *mockDatabaseServiceQuic) Drop(_ context.Context, _ string) error { return nil }
func (m *mockDatabaseServiceQuic) UpdateProtocol(_ string, _ database.ProtocolConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{}, nil
}
func (m *mockDatabaseServiceQuic) UpdateEtcd(_ string, _ *database.EtcdConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{}, nil
}
func (m *mockDatabaseServiceQuic) UpdateRaft(_ string, _ *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{}, nil
}
func (m *mockDatabaseServiceQuic) UpdateBarrier(_ string, _ *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{}, nil
}
func (m *mockDatabaseServiceQuic) UpdateConnection(_ string, _ *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{}, nil
}
func (m *mockDatabaseServiceQuic) SealDatabase(ctx context.Context, name string) error {
	if m.sealFn != nil {
		return m.sealFn(ctx, name)
	}
	return nil
}
func (m *mockDatabaseServiceQuic) UnsealDatabase(ctx context.Context, name string, creds seal.Credentials) error {
	if m.unsealFn != nil {
		return m.unsealFn(ctx, name, creds)
	}
	return nil
}

// DatabaseBarrierStatus adapts databaseBarrierStatusSeal to the service
// interface, with no cluster seal state.
func (m *mockDatabaseServiceQuic) DatabaseBarrierStatus(ctx context.Context, name string) (*service.DatabaseBarrierStatusInfo, error) {
	st, err := m.databaseBarrierStatusSeal(ctx, name)
	if st == nil {
		return nil, err
	}
	return &service.DatabaseBarrierStatusInfo{BarrierStatus: st}, err
}

func (m *mockDatabaseServiceQuic) databaseBarrierStatusSeal(_ context.Context, name string) (*seal.BarrierStatus, error) {
	if m.barrierStatusFn != nil {
		return m.barrierStatusFn(name)
	}
	return &seal.BarrierStatus{Sealed: false, Strategy: "software"}, nil
}
func (m *mockDatabaseServiceQuic) CreateForTenant(ctx context.Context, cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
	return m.Create(cfg)
}
func (m *mockDatabaseServiceQuic) GetForTenant(ctx context.Context, name string) (*database.DatabaseInfo, error) {
	return m.Get(name)
}
func (m *mockDatabaseServiceQuic) ListForTenant(ctx context.Context) ([]database.DatabaseInfo, error) {
	return m.List()
}
func (m *mockDatabaseServiceQuic) DropForTenant(ctx context.Context, name string) error {
	return m.Drop(context.Background(), name)
}
func (m *mockDatabaseServiceQuic) UpdateProtocolForTenant(ctx context.Context, name string, protocol database.ProtocolConfig) (*database.DatabaseInfo, error) {
	return m.UpdateProtocol(name, protocol)
}
func (m *mockDatabaseServiceQuic) UpdateEtcdForTenant(ctx context.Context, name string, etcd *database.EtcdConfig) (*database.DatabaseInfo, error) {
	return m.UpdateEtcd(name, etcd)
}
func (m *mockDatabaseServiceQuic) UpdateRaftForTenant(ctx context.Context, name string, raft *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	return m.UpdateRaft(name, raft)
}
func (m *mockDatabaseServiceQuic) UpdateBarrierForTenant(ctx context.Context, name string, barrier *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	return m.UpdateBarrier(name, barrier)
}
func (m *mockDatabaseServiceQuic) UpdateConnectionForTenant(ctx context.Context, name string, conn *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	return m.UpdateConnection(name, conn)
}

func (m *mockDatabaseServiceQuic) UpdateSQL(name string, sqlCfg *database.SQLConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: name, SQL: sqlCfg}, nil
}

func (m *mockDatabaseServiceQuic) UpdateSQLForTenant(_ context.Context, name string, sqlCfg *database.SQLConfig) (*database.DatabaseInfo, error) {
	return m.UpdateSQL(name, sqlCfg)
}

func (m *mockDatabaseServiceQuic) UpdateCompactionForTenant(_ context.Context, name string, policy *compaction.Policy) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: name, Compaction: policy}, nil
}
func (m *mockDatabaseServiceQuic) ScaleForTenant(_ context.Context, name string, targetReplicas int) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: name, Replicas: targetReplicas}, nil
}
