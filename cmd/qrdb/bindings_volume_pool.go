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

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// VolumePoolBinding exposes the volume pool catalog (list, inspect,
// create, update, delete and the per-pool tenant listing) to the desktop
// GUI. Each method resolves the active connection's client and calls its
// VolumePoolClient, so every transport the CLI supports serves the GUI.
type VolumePoolBinding struct {
	logger *sdk.ConcreteLogger
	conn   *ConnectionBinding
}

// NewVolumePoolBinding creates a VolumePoolBinding bound to conn.
func NewVolumePoolBinding(log *sdk.ConcreteLogger, conn *ConnectionBinding) *VolumePoolBinding {
	return &VolumePoolBinding{logger: log, conn: conn}
}

// pools returns the active connection's volume pool client.
func (b *VolumePoolBinding) pools(op string) (transport.VolumePoolClient, error) {
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error(op+" failed", "error", err)
		return nil, err
	}
	return cl.VolumePools(), nil
}

// List returns every pool in the catalog.
func (b *VolumePoolBinding) List() ([]transport.VolumePool, error) {
	vp, err := b.pools("List")
	if err != nil {
		return nil, err
	}
	pools, err := vp.ListPools(context.Background())
	if err != nil {
		b.logger.Error("List failed", "error", err)
		return nil, err
	}
	if pools == nil {
		pools = []transport.VolumePool{}
	}
	return pools, nil
}

// Get returns one pool with its per-node reports.
func (b *VolumePoolBinding) Get(name string) (*transport.VolumePoolDetail, error) {
	vp, err := b.pools("Get")
	if err != nil {
		return nil, err
	}
	detail, err := vp.GetPool(context.Background(), name)
	if err != nil {
		b.logger.Error("Get failed", "pool", name, "error", err)
		return nil, err
	}
	return detail, nil
}

// Create adds a pool to the catalog.
func (b *VolumePoolBinding) Create(req *transport.CreateVolumePoolRequest) (*transport.VolumePool, error) {
	if req == nil {
		req = &transport.CreateVolumePoolRequest{}
	}
	vp, err := b.pools("Create")
	if err != nil {
		return nil, err
	}
	pool, err := vp.CreatePool(context.Background(), req)
	if err != nil {
		b.logger.Error("Create failed", "pool", req.Name, "error", err)
		return nil, err
	}
	return pool, nil
}

// Update changes a pool's description, capacity or overcommit. The pool
// named by name wins over any name carried in req.
func (b *VolumePoolBinding) Update(name string, req *transport.UpdateVolumePoolRequest) (*transport.VolumePool, error) {
	if req == nil {
		req = &transport.UpdateVolumePoolRequest{}
	}
	req.Name = name
	vp, err := b.pools("Update")
	if err != nil {
		return nil, err
	}
	pool, err := vp.UpdatePool(context.Background(), req)
	if err != nil {
		b.logger.Error("Update failed", "pool", name, "error", err)
		return nil, err
	}
	return pool, nil
}

// Delete removes a pool from the catalog.
func (b *VolumePoolBinding) Delete(name string) error {
	vp, err := b.pools("Delete")
	if err != nil {
		return err
	}
	if err := vp.DeletePool(context.Background(), name); err != nil {
		b.logger.Error("Delete failed", "pool", name, "error", err)
		return err
	}
	return nil
}

// ListTenants returns one page of the tenants assigned to a pool.
func (b *VolumePoolBinding) ListTenants(name string, pageSize int, pageToken string) (*transport.ListVolumePoolTenantsResponse, error) {
	vp, err := b.pools("ListTenants")
	if err != nil {
		return nil, err
	}
	resp, err := vp.ListPoolTenants(context.Background(), &transport.ListVolumePoolTenantsRequest{
		Pool:      name,
		PageSize:  pageSize,
		PageToken: pageToken,
	})
	if err != nil {
		b.logger.Error("ListTenants failed", "pool", name, "error", err)
		return nil, err
	}
	if resp.Tenants == nil {
		resp.Tenants = []transport.VolumePoolTenant{}
	}
	return resp, nil
}
