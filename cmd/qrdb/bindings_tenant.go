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

// TenantBinding exposes tenant management operations to the Wails frontend.
type TenantBinding struct {
	logger *logger.Logger
	conn   *ConnectionBinding
}

// NewTenantBinding creates a new TenantBinding backed by the given connection.
func NewTenantBinding(log *logger.Logger, conn *ConnectionBinding) *TenantBinding {
	return &TenantBinding{logger: log, conn: conn}
}

// Create creates a new tenant with the given ID and name.
func (b *TenantBinding) Create(req *transport.CreateTenantRequest) (*transport.CreateTenantResponse, error) {
	b.logger.Debug("Create called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Create failed", "error", err)
		return nil, err
	}
	resp, err := cl.Tenants().CreateTenant(context.Background(), req)
	if err != nil {
		b.logger.Error("Create failed", "error", err)
		return nil, err
	}
	b.logger.Debug("Create completed", "tenantID", resp.Tenant.ID)
	return resp, nil
}

// Get retrieves a tenant by its globally unique ID.
func (b *TenantBinding) Get(id string) (*transport.GetTenantResponse, error) {
	b.logger.Debug("Get called", "id", id)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Get failed", "error", err)
		return nil, err
	}
	resp, err := cl.Tenants().GetTenant(context.Background(), id)
	if err != nil {
		b.logger.Error("Get failed", "id", id, "error", err)
		return nil, err
	}
	b.logger.Debug("Get completed", "id", id)
	return resp, nil
}

// List returns all registered tenants.
func (b *TenantBinding) List() (*transport.ListTenantsResponse, error) {
	b.logger.Debug("List called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("List failed", "error", err)
		return nil, err
	}
	resp, err := cl.Tenants().ListTenants(context.Background())
	if err != nil {
		b.logger.Error("List failed", "error", err)
		return nil, err
	}
	b.logger.Debug("List completed")
	return resp, nil
}

// Delete removes a tenant by ID.
func (b *TenantBinding) Delete(id string) error {
	b.logger.Debug("Delete called", "id", id)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Delete failed", "error", err)
		return err
	}
	if err := cl.Tenants().DeleteTenant(context.Background(), id); err != nil {
		b.logger.Error("Delete failed", "id", id, "error", err)
		return err
	}
	b.logger.Debug("Delete completed", "id", id)
	return nil
}

// BarrierInit initializes the per-tenant cryptographic barrier.
func (b *TenantBinding) BarrierInit(req *transport.TenantBarrierInitRequest) error {
	b.logger.Debug("BarrierInit called", "tenantID", req.TenantID)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("BarrierInit failed", "error", err)
		return err
	}
	if _, err := cl.Tenants().TenantBarrierInit(context.Background(), req); err != nil {
		b.logger.Error("BarrierInit failed", "tenantID", req.TenantID, "error", err)
		return err
	}
	b.logger.Debug("BarrierInit completed", "tenantID", req.TenantID)
	return nil
}

// BarrierUnseal submits key material to unseal the per-tenant barrier.
func (b *TenantBinding) BarrierUnseal(req *transport.TenantBarrierUnsealRequest) error {
	b.logger.Debug("BarrierUnseal called", "tenantID", req.TenantID)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("BarrierUnseal failed", "error", err)
		return err
	}
	if err := cl.Tenants().TenantBarrierUnseal(context.Background(), req); err != nil {
		b.logger.Error("BarrierUnseal failed", "tenantID", req.TenantID, "error", err)
		return err
	}
	b.logger.Debug("BarrierUnseal completed", "tenantID", req.TenantID)
	return nil
}

// BarrierStatus returns the current sealed/initialized state and active
// strategy metadata for the per-tenant barrier.
func (b *TenantBinding) BarrierStatus(tenantID string) (*transport.TenantBarrierStatusResponse, error) {
	b.logger.Debug("BarrierStatus called", "tenantID", tenantID)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("BarrierStatus failed", "error", err)
		return nil, err
	}
	status, err := cl.Tenants().TenantBarrierStatus(context.Background(), &transport.TenantBarrierStatusRequest{TenantID: tenantID})
	if err != nil {
		b.logger.Error("BarrierStatus failed", "tenantID", tenantID, "error", err)
		return nil, err
	}
	b.logger.Debug("BarrierStatus completed", "tenantID", tenantID, "sealed", status.Sealed)
	return status, nil
}
