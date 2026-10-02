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

package main

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// DatabaseBinding exposes database management operations to the Wails frontend.
type DatabaseBinding struct {
	logger *logger.Logger
	conn   *ConnectionBinding
}

// NewDatabaseBinding creates a new DatabaseBinding backed by the given connection.
func NewDatabaseBinding(log *logger.Logger, conn *ConnectionBinding) *DatabaseBinding {
	return &DatabaseBinding{logger: log, conn: conn}
}

// Create creates a new database with the specified engine and backend.
func (b *DatabaseBinding) Create(req *transport.CreateDatabaseRequest) (*transport.DatabaseInfo, error) {
	b.logger.Debug("Create called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Create failed", "error", err)
		return nil, err
	}
	info, err := cl.Database().CreateDatabase(context.Background(), *req)
	if err != nil {
		b.logger.Error("Create failed", "error", err)
		return nil, err
	}
	b.logger.Debug("Create completed", "name", info.Name)
	return info, nil
}

// Get retrieves database information by name.
func (b *DatabaseBinding) Get(name string) (*transport.DatabaseInfo, error) {
	b.logger.Debug("Get called", "name", name)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Get failed", "error", err)
		return nil, err
	}
	info, err := cl.Database().GetDatabase(context.Background(), name)
	if err != nil {
		b.logger.Error("Get failed", "name", name, "error", err)
		return nil, err
	}
	b.logger.Debug("Get completed", "name", name)
	return info, nil
}

// List returns all databases.
func (b *DatabaseBinding) List() ([]transport.DatabaseInfo, error) {
	b.logger.Debug("List called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("List failed", "error", err)
		return nil, err
	}
	infos, err := cl.Database().ListDatabases(context.Background())
	if err != nil {
		b.logger.Error("List failed", "error", err)
		return nil, err
	}
	b.logger.Debug("List completed", "count", len(infos))
	return infos, nil
}

// Drop removes a database by name.
func (b *DatabaseBinding) Drop(name string) error {
	b.logger.Debug("Drop called", "name", name)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Drop failed", "error", err)
		return err
	}
	if err := cl.Database().DropDatabase(context.Background(), name); err != nil {
		b.logger.Error("Drop failed", "name", name, "error", err)
		return err
	}
	b.logger.Debug("Drop completed", "name", name)
	return nil
}

// UpdateProtocol updates the wire protocol configuration for a database.
func (b *DatabaseBinding) UpdateProtocol(name string, req *transport.UpdateDatabaseProtocolRequest) (*transport.DatabaseInfo, error) {
	b.logger.Debug("UpdateProtocol called", "name", name)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("UpdateProtocol failed", "error", err)
		return nil, err
	}
	req.Name = name
	info, err := cl.Database().UpdateDatabaseProtocol(context.Background(), *req)
	if err != nil {
		b.logger.Error("UpdateProtocol failed", "name", name, "error", err)
		return nil, err
	}
	b.logger.Debug("UpdateProtocol completed", "name", name)
	return info, nil
}

// UpdateRaft updates the Raft configuration for a database.
func (b *DatabaseBinding) UpdateRaft(name string, req *transport.UpdateDatabaseRaftRequest) (*transport.DatabaseInfo, error) {
	b.logger.Debug("UpdateRaft called", "name", name)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("UpdateRaft failed", "error", err)
		return nil, err
	}
	req.Name = name
	info, err := cl.Database().UpdateDatabaseRaft(context.Background(), *req)
	if err != nil {
		b.logger.Error("UpdateRaft failed", "name", name, "error", err)
		return nil, err
	}
	b.logger.Debug("UpdateRaft completed", "name", name)
	return info, nil
}

// Scale changes the replication factor of a dedicated-raft database.
func (b *DatabaseBinding) Scale(name string, targetReplicas int) (*transport.DatabaseInfo, error) {
	b.logger.Debug("Scale called", "name", name, "targetReplicas", targetReplicas)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Scale failed", "error", err)
		return nil, err
	}
	info, err := cl.Database().ScaleDatabase(context.Background(), transport.ScaleDatabaseRequest{
		Name:           name,
		TargetReplicas: targetReplicas,
	})
	if err != nil {
		b.logger.Error("Scale failed", "name", name, "error", err)
		return nil, err
	}
	b.logger.Debug("Scale completed", "name", name)
	return info, nil
}

// UpdateBarrier updates the barrier configuration for a database.
func (b *DatabaseBinding) UpdateBarrier(name string, req *transport.UpdateDatabaseBarrierRequest) (*transport.DatabaseInfo, error) {
	b.logger.Debug("UpdateBarrier called", "name", name)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("UpdateBarrier failed", "error", err)
		return nil, err
	}
	req.Name = name
	info, err := cl.Database().UpdateDatabaseBarrier(context.Background(), *req)
	if err != nil {
		b.logger.Error("UpdateBarrier failed", "name", name, "error", err)
		return nil, err
	}
	b.logger.Debug("UpdateBarrier completed", "name", name)
	return info, nil
}

// UpdateEtcd updates the etcd wire protocol configuration for a database.
func (b *DatabaseBinding) UpdateEtcd(name string, req *transport.UpdateDatabaseEtcdRequest) (*transport.DatabaseInfo, error) {
	b.logger.Debug("UpdateEtcd called", "name", name)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("UpdateEtcd failed", "error", err)
		return nil, err
	}
	req.Name = name
	info, err := cl.Database().UpdateDatabaseEtcd(context.Background(), *req)
	if err != nil {
		b.logger.Error("UpdateEtcd failed", "name", name, "error", err)
		return nil, err
	}
	b.logger.Debug("UpdateEtcd completed", "name", name)
	return info, nil
}

// UpdateConnection updates the connection configuration for a database.
func (b *DatabaseBinding) UpdateConnection(name string, req *transport.UpdateDatabaseConnectionRequest) (*transport.DatabaseInfo, error) {
	b.logger.Debug("UpdateConnection called", "name", name)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("UpdateConnection failed", "error", err)
		return nil, err
	}
	req.Name = name
	info, err := cl.Database().UpdateDatabaseConnection(context.Background(), *req)
	if err != nil {
		b.logger.Error("UpdateConnection failed", "name", name, "error", err)
		return nil, err
	}
	b.logger.Debug("UpdateConnection completed", "name", name)
	return info, nil
}

// UpdateCompaction sets a database's scheduled-compaction policy.
func (b *DatabaseBinding) UpdateCompaction(name string, req *transport.UpdateDatabaseCompactionRequest) (*transport.DatabaseInfo, error) {
	b.logger.Debug("UpdateCompaction called", "name", name)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("UpdateCompaction failed", "error", err)
		return nil, err
	}
	req.Name = name
	info, err := cl.Database().UpdateDatabaseCompaction(context.Background(), *req)
	if err != nil {
		b.logger.Error("UpdateCompaction failed", "name", name, "error", err)
		return nil, err
	}
	b.logger.Debug("UpdateCompaction completed", "name", name)
	return info, nil
}

// UpdateSQL updates a database's SQL settings (its concurrency mode).
func (b *DatabaseBinding) UpdateSQL(name string, req *transport.UpdateDatabaseSQLRequest) (*transport.DatabaseInfo, error) {
	b.logger.Debug("UpdateSQL called", "name", name)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("UpdateSQL failed", "error", err)
		return nil, err
	}
	req.Name = name
	info, err := cl.Database().UpdateDatabaseSQL(context.Background(), *req)
	if err != nil {
		b.logger.Error("UpdateSQL failed", "name", name, "error", err)
		return nil, err
	}
	b.logger.Debug("UpdateSQL completed", "name", name)
	return info, nil
}
