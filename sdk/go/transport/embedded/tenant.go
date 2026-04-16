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

package embedded

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// mapTenantInfo converts a tenant.TenantInfo to a transport.TenantInfo.
func mapTenantInfo(info *tenant.TenantInfo) transport.TenantInfo {
	ti := transport.TenantInfo{
		ID:            info.ID,
		Name:          info.Name,
		IsolationType: transport.TenantIsolationType(info.EffectiveIsolationType()),
		CreatedAt:     info.CreatedAt,
		UpdatedAt:     info.UpdatedAt,
	}
	if info.Quotas != nil {
		ti.Quotas = mapQuotas(info.Quotas)
	}
	return ti
}

// mapQuotas converts a quota.Quotas to a transport.TenantQuotas pointer.
func mapQuotas(q *quota.Quotas) *transport.TenantQuotas {
	return &transport.TenantQuotas{
		MaxDatabases:    q.MaxDatabases,
		MaxRaftGroups:   q.MaxRaftGroups,
		MaxCollections:  q.MaxCollections,
		MaxKeys:         q.MaxKeys,
		MaxStorageBytes: q.MaxStorageBytes,
		MaxUsers:        q.MaxUsers,
		MaxRoles:        q.MaxRoles,
		MaxCertificates: q.MaxCertificates,
		MaxBackups:      q.MaxBackups,
		MaxVectorDims:   q.MaxVectorDims,
		MaxTSMetrics:    q.MaxTSMetrics,
	}
}

// WithTenantService injects a TenantServicer into the transport.
func WithTenantService(svc tenant.TenantServicer) Option {
	return func(t *Transport) {
		t.tenantSvc = svc
	}
}

// CreateTenant creates a new tenant by delegating to the TenantServicer.
func (t *Transport) CreateTenant(ctx context.Context, req *transport.CreateTenantRequest) (*transport.CreateTenantResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
	}

	info, err := t.tenantSvc.CreateTenant(ctx, req.ID, req.Name, tenant.IsolationType(req.IsolationType))
	if err != nil {
		return nil, err
	}

	return &transport.CreateTenantResponse{
		Tenant: mapTenantInfo(info),
	}, nil
}

// GetTenant retrieves a tenant by ID by delegating to the TenantServicer.
func (t *Transport) GetTenant(ctx context.Context, tenantID string) (*transport.GetTenantResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
	}

	info, err := t.tenantSvc.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	return &transport.GetTenantResponse{
		Tenant: mapTenantInfo(info),
	}, nil
}

// ListTenants lists all tenants by delegating to the TenantServicer.
func (t *Transport) ListTenants(ctx context.Context) (*transport.ListTenantsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
	}

	tenants, err := t.tenantSvc.ListTenants(ctx)
	if err != nil {
		return nil, err
	}

	result := make([]transport.TenantInfo, 0, len(tenants))
	for _, info := range tenants {
		result = append(result, mapTenantInfo(&info))
	}

	return &transport.ListTenantsResponse{
		Tenants: result,
	}, nil
}

// UpdateTenant applies a partial update to an existing tenant by delegating
// to the TenantServicer. Non-empty patch fields replace the stored value.
func (t *Transport) UpdateTenant(ctx context.Context, req *transport.UpdateTenantRequest) (*transport.UpdateTenantResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
	}

	patch := tenant.TenantPatch{
		Name:          req.Name,
		IsolationType: tenant.IsolationType(req.IsolationType),
	}
	info, err := t.tenantSvc.UpdateTenant(ctx, req.TenantID, patch)
	if err != nil {
		return nil, err
	}

	return &transport.UpdateTenantResponse{
		Tenant: mapTenantInfo(info),
	}, nil
}

// UpdateTenantQuotas updates the resource quotas for a tenant by
// delegating to the TenantServicer.
func (t *Transport) UpdateTenantQuotas(ctx context.Context, req *transport.UpdateTenantQuotasRequest) (*transport.UpdateTenantQuotasResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
	}

	q := &quota.Quotas{
		MaxDatabases:    req.Quotas.MaxDatabases,
		MaxRaftGroups:   req.Quotas.MaxRaftGroups,
		MaxCollections:  req.Quotas.MaxCollections,
		MaxKeys:         req.Quotas.MaxKeys,
		MaxStorageBytes: req.Quotas.MaxStorageBytes,
		MaxUsers:        req.Quotas.MaxUsers,
		MaxRoles:        req.Quotas.MaxRoles,
		MaxCertificates: req.Quotas.MaxCertificates,
		MaxBackups:      req.Quotas.MaxBackups,
		MaxVectorDims:   req.Quotas.MaxVectorDims,
		MaxTSMetrics:    req.Quotas.MaxTSMetrics,
	}

	info, err := t.tenantSvc.UpdateQuotas(ctx, req.TenantID, q)
	if err != nil {
		return nil, err
	}

	return &transport.UpdateTenantQuotasResponse{
		Tenant: mapTenantInfo(info),
	}, nil
}

// DeleteTenant deletes a tenant by delegating to the TenantServicer.
func (t *Transport) DeleteTenant(ctx context.Context, tenantID string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return &NilServiceError{Service: "TenantServicer"}
	}

	return t.tenantSvc.DeleteTenant(ctx, tenantID)
}

// TenantBarrierInit initializes a per-tenant barrier by delegating to
// the TenantServicer.
func (t *Transport) TenantBarrierInit(ctx context.Context, req *transport.TenantBarrierInitRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return &NilServiceError{Service: "TenantServicer"}
	}

	return t.tenantSvc.TenantBarrierInit(ctx, req.TenantID, req.Threshold, req.Shares)
}

// TenantBarrierUnseal unseals a per-tenant barrier by delegating to
// the TenantServicer.
func (t *Transport) TenantBarrierUnseal(ctx context.Context, req *transport.TenantBarrierUnsealRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return &NilServiceError{Service: "TenantServicer"}
	}

	// Prefer share over key for unseal material.
	key := req.Key
	if len(req.Share) > 0 {
		key = req.Share
	}

	return t.tenantSvc.TenantBarrierUnseal(ctx, req.TenantID, key)
}
