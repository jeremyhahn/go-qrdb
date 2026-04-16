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

package mcp

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.TenantClient = (*Transport)(nil)

// CreateTenant creates a new tenant. This method is not yet implemented for
// the MCP transport and requires corresponding MCP tool definitions.
func (t *Transport) CreateTenant(ctx context.Context, req *transport.CreateTenantRequest) (*transport.CreateTenantResponse, error) {
	return nil, &transport.NotImplementedError{Method: "CreateTenant"}
}

// GetTenant retrieves a tenant by ID. This method is not yet implemented for
// the MCP transport and requires corresponding MCP tool definitions.
func (t *Transport) GetTenant(ctx context.Context, tenantID string) (*transport.GetTenantResponse, error) {
	return nil, &transport.NotImplementedError{Method: "GetTenant"}
}

// ListTenants lists all tenants. This method is not yet implemented for the
// MCP transport and requires corresponding MCP tool definitions.
func (t *Transport) ListTenants(ctx context.Context) (*transport.ListTenantsResponse, error) {
	return nil, &transport.NotImplementedError{Method: "ListTenants"}
}

// DeleteTenant deletes a tenant. This method is not yet implemented for the
// MCP transport and requires corresponding MCP tool definitions.
func (t *Transport) DeleteTenant(ctx context.Context, tenantID string) error {
	return &transport.NotImplementedError{Method: "DeleteTenant"}
}

// TenantBarrierInit initializes a per-tenant barrier. This method is not yet
// implemented for the MCP transport and requires corresponding MCP tool
// definitions.
func (t *Transport) TenantBarrierInit(ctx context.Context, req *transport.TenantBarrierInitRequest) error {
	return &transport.NotImplementedError{Method: "TenantBarrierInit"}
}

// TenantBarrierUnseal unseals a per-tenant barrier. This method is not yet
// implemented for the MCP transport and requires corresponding MCP tool
// definitions.
func (t *Transport) TenantBarrierUnseal(ctx context.Context, req *transport.TenantBarrierUnsealRequest) error {
	return &transport.NotImplementedError{Method: "TenantBarrierUnseal"}
}

// UpdateTenant applies a partial update to an existing tenant. This method is
// not yet implemented for the MCP transport and requires corresponding MCP
// tool definitions.
func (t *Transport) UpdateTenant(ctx context.Context, req *transport.UpdateTenantRequest) (*transport.UpdateTenantResponse, error) {
	return nil, &transport.NotImplementedError{Method: "UpdateTenant"}
}
