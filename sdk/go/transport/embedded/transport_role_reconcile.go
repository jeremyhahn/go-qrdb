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

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ReconcileBuiltInRoles converges the tenant's stored built-in roles onto this
// build's definitions.
func (t *Transport) ReconcileBuiltInRoles(ctx context.Context, req *transport.ReconcileRolesRequest) (*transport.ReconcileRolesResponse, error) {
	if err := t.gate("ReconcileBuiltInRoles"); err != nil {
		return nil, err
	}
	if t.roleSvc == nil {
		return nil, &transport.NotImplementedError{Method: "ReconcileBuiltInRoles"}
	}

	tenantID := ""
	if req != nil {
		tenantID = req.TenantID
	}

	report, err := t.roleSvc.ReconcileBuiltInRoles(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if report == nil {
		return &transport.ReconcileRolesResponse{TenantID: tenantID}, nil
	}

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
	return out, nil
}
