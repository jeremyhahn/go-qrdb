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

// LeaseBinding exposes distributed lease management to the Wails
// frontend.
type LeaseBinding struct {
	logger *logger.Logger
	conn   *ConnectionBinding
}

// NewLeaseBinding creates a new LeaseBinding backed by the given
// connection.
func NewLeaseBinding(log *logger.Logger, conn *ConnectionBinding) *LeaseBinding {
	return &LeaseBinding{logger: log, conn: conn}
}

// Grant creates a new lease with the specified TTL in seconds.
func (b *LeaseBinding) Grant(ttl int64) (*transport.LeaseInfo, error) {
	b.logger.Debug("Grant called", "ttl", ttl)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Grant failed", "error", err)
		return nil, err
	}
	info, err := cl.Lease().LeaseGrant(context.Background(), ttl)
	if err != nil {
		b.logger.Error("Grant failed", "ttl", ttl, "error", err)
		return nil, err
	}
	b.logger.Debug("Grant completed", "leaseID", info.ID)
	return info, nil
}

// Revoke immediately expires a lease and deletes all attached keys.
func (b *LeaseBinding) Revoke(leaseID int64) error {
	b.logger.Debug("Revoke called", "leaseID", leaseID)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Revoke failed", "error", err)
		return err
	}
	if err := cl.Lease().LeaseRevoke(context.Background(), leaseID); err != nil {
		b.logger.Error("Revoke failed", "leaseID", leaseID, "error", err)
		return err
	}
	b.logger.Debug("Revoke completed", "leaseID", leaseID)
	return nil
}

// KeepAlive resets the TTL on an existing lease.
func (b *LeaseBinding) KeepAlive(leaseID int64) (*transport.LeaseInfo, error) {
	b.logger.Debug("KeepAlive called", "leaseID", leaseID)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("KeepAlive failed", "error", err)
		return nil, err
	}
	info, err := cl.Lease().LeaseKeepAlive(context.Background(), leaseID)
	if err != nil {
		b.logger.Error("KeepAlive failed", "leaseID", leaseID, "error", err)
		return nil, err
	}
	b.logger.Debug("KeepAlive completed", "leaseID", leaseID)
	return info, nil
}

// TimeToLive returns the current state of a lease including its
// remaining TTL and attached keys.
func (b *LeaseBinding) TimeToLive(leaseID int64) (*transport.LeaseInfo, error) {
	b.logger.Debug("TimeToLive called", "leaseID", leaseID)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("TimeToLive failed", "error", err)
		return nil, err
	}
	info, err := cl.Lease().LeaseTimeToLive(context.Background(), leaseID)
	if err != nil {
		b.logger.Error("TimeToLive failed", "leaseID", leaseID, "error", err)
		return nil, err
	}
	b.logger.Debug("TimeToLive completed", "leaseID", leaseID)
	return info, nil
}

// List returns all active leases.
func (b *LeaseBinding) List() ([]*transport.LeaseInfo, error) {
	b.logger.Debug("List called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("List failed", "error", err)
		return nil, err
	}
	leases, err := cl.Lease().LeaseList(context.Background())
	if err != nil {
		b.logger.Error("List failed", "error", err)
		return nil, err
	}
	b.logger.Debug("List completed", "count", len(leases))
	return leases, nil
}
