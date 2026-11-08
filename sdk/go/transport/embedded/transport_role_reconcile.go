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

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ReconcileBuiltInRoles converges the tenant's stored built-in roles onto this
// build's definitions.
func (t *Transport) ReconcileBuiltInRoles(ctx context.Context, req *transport.ReconcileRolesRequest) (*transport.ReconcileRolesResponse, error) {
	ctx, err := t.admit(ctx, "ReconcileBuiltInRoles")
	if err != nil {
		return nil, err
	}
	if t.roleSvc == nil {
		return nil, &transport.NotImplementedError{Method: "ReconcileBuiltInRoles"}
	}

	tenantID := ""
	if req != nil {
		tenantID = req.TenantID
	}
	// roles:create, not a verb of its own -- a dedicated permission would be
	// missing from every role seeded before it existed, so nobody could
	// hold the grant needed to repair that absence. Matches the QUIC
	// dispatcher's role.ReconcileBuiltIn registration exactly.
	if err := t.authorizeIdentityTenant(ctx, tenantID, permission.RolesCreate); err != nil {
		return nil, err
	}
	if err := t.authMW.AuthorizeSOExclusive(ctx, tenantID, "reconcile built-in roles"); err != nil {
		return nil, err
	}

	report, err := t.roleSvc.ReconcileBuiltInRoles(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if report == nil {
		return &transport.ReconcileRolesResponse{TenantID: tenantID}, nil
	}
	return reconcileReportToTransport(report), nil
}

// ReconcileBuiltInRolesAllTenants converges the stored built-in roles of every
// tenant on this node onto this build's definitions (role.ReconcileAllTenants).
// The caller must be a platform operator holding roles:reconcile_all, the gate
// every network transport applies.
func (t *Transport) ReconcileBuiltInRolesAllTenants(ctx context.Context) (*transport.ReconcileAllTenantsRolesResponse, error) {
	ctx, err := t.admit(ctx, "ReconcileBuiltInRolesAllTenants")
	if err != nil {
		return nil, err
	}
	if t.roleSvc == nil {
		return nil, &transport.NotImplementedError{Method: "ReconcileBuiltInRolesAllTenants"}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
	}
	if err := t.authorizeTenantSelf(ctx, permission.RolesReconcileAll); err != nil {
		return nil, err
	}
	if err := t.authMW.AuthorizeSOExclusive(ctx, tenantid.System, "reconcile built-in roles in every tenant"); err != nil {
		return nil, err
	}

	report, err := role.ReconcileAllTenants(ctx, t.roleSvc, t.tenantSvc)
	if err != nil {
		return nil, err
	}

	out := &transport.ReconcileAllTenantsRolesResponse{}
	for i := range report.Tenants {
		out.Tenants = append(out.Tenants, *reconcileReportToTransport(&report.Tenants[i]))
	}
	for _, sk := range report.Skipped {
		out.Skipped = append(out.Skipped, transport.RoleReconcileSkippedTenant{TenantID: sk.TenantID, Reason: sk.Reason, Detail: sk.Detail})
	}
	for _, f := range report.Failed {
		out.Failed = append(out.Failed, transport.RoleReconcileFailedTenant{TenantID: f.TenantID, Error: f.Error})
	}
	return out, nil
}

// reconcileReportToTransport converts one tenant's role.ReconcileReport to its
// transport representation.
func reconcileReportToTransport(report *role.ReconcileReport) *transport.ReconcileRolesResponse {
	out := &transport.ReconcileRolesResponse{TenantID: report.TenantID}
	for _, r := range report.Roles {
		out.Roles = append(out.Roles, transport.RoleReconcileResult{
			RoleID:  r.RoleID,
			Name:    r.Name,
			Created: r.Created,
			Added:   permissionsToStrings(r.Added),
			Removed: permissionsToStrings(r.Removed),
		})
	}
	return out
}
