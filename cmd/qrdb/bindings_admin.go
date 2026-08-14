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

// AdminBinding exposes cluster administration operations to the Wails frontend.
type AdminBinding struct {
	logger *logger.Logger
	conn   *ConnectionBinding
}

// NewAdminBinding creates a new AdminBinding backed by the given connection.
func NewAdminBinding(log *logger.Logger, conn *ConnectionBinding) *AdminBinding {
	return &AdminBinding{logger: log, conn: conn}
}

// Health returns the health status of the connected QRDB node.
func (b *AdminBinding) Health() (*transport.HealthResponse, error) {
	b.logger.Debug("Health called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Health failed", "error", err)
		return nil, err
	}
	resp, err := cl.Admin().Health(context.Background())
	if err != nil {
		b.logger.Error("Health failed", "error", err)
		return nil, err
	}
	b.logger.Debug("Health completed")
	return resp, nil
}

// Status returns detailed status information about the node and its shards.
func (b *AdminBinding) Status() (*transport.StatusResponse, error) {
	b.logger.Debug("Status called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Status failed", "error", err)
		return nil, err
	}
	resp, err := cl.Admin().Status(context.Background())
	if err != nil {
		b.logger.Error("Status failed", "error", err)
		return nil, err
	}
	b.logger.Debug("Status completed")
	return resp, nil
}

// Peers returns the peer membership for all loaded shards.
func (b *AdminBinding) Peers() (*transport.PeersResponse, error) {
	b.logger.Debug("Peers called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Peers failed", "error", err)
		return nil, err
	}
	resp, err := cl.Admin().Peers(context.Background())
	if err != nil {
		b.logger.Error("Peers failed", "error", err)
		return nil, err
	}
	b.logger.Debug("Peers completed")
	return resp, nil
}

// AddMember adds a new replica to the specified shard.
func (b *AdminBinding) AddMember(shardID, replicaID uint64, raftAddr string) error {
	b.logger.Debug("AddMember called", "shardID", shardID, "replicaID", replicaID, "raftAddr", raftAddr)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("AddMember failed", "error", err)
		return err
	}
	if err := cl.Admin().AddMember(context.Background(), shardID, replicaID, raftAddr); err != nil {
		b.logger.Error("AddMember failed", "error", err)
		return err
	}
	b.logger.Debug("AddMember completed")
	return nil
}

// RemoveMember removes a replica from the specified shard.
func (b *AdminBinding) RemoveMember(shardID, replicaID uint64) error {
	b.logger.Debug("RemoveMember called", "shardID", shardID, "replicaID", replicaID)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("RemoveMember failed", "error", err)
		return err
	}
	if err := cl.Admin().RemoveMember(context.Background(), shardID, replicaID); err != nil {
		b.logger.Error("RemoveMember failed", "error", err)
		return err
	}
	b.logger.Debug("RemoveMember completed")
	return nil
}

// RequestSnapshot triggers an asynchronous snapshot for the specified shard.
func (b *AdminBinding) RequestSnapshot(shardID uint64) error {
	b.logger.Debug("RequestSnapshot called", "shardID", shardID)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("RequestSnapshot failed", "error", err)
		return err
	}
	if err := cl.Admin().RequestSnapshot(context.Background(), shardID); err != nil {
		b.logger.Error("RequestSnapshot failed", "error", err)
		return err
	}
	b.logger.Debug("RequestSnapshot completed")
	return nil
}

// Drain transitions the host into drain mode where new proposals are
// rejected while in-flight operations complete.
func (b *AdminBinding) Drain() error {
	b.logger.Debug("Drain called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Drain failed", "error", err)
		return err
	}
	if err := cl.Admin().Drain(context.Background()); err != nil {
		b.logger.Error("Drain failed", "error", err)
		return err
	}
	b.logger.Debug("Drain completed")
	return nil
}

// Capabilities returns the compile-time feature set of the QRDB binary.
func (b *AdminBinding) Capabilities() (*transport.CapabilitiesResponse, error) {
	b.logger.Debug("Capabilities called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Capabilities failed", "error", err)
		return nil, err
	}
	resp, err := cl.Admin().Capabilities(context.Background())
	if err != nil {
		b.logger.Error("Capabilities failed", "error", err)
		return nil, err
	}
	b.logger.Debug("Capabilities completed")
	return resp, nil
}
