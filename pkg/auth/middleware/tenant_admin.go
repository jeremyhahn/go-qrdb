// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package middleware

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// The tenant-administration surface is the one place where the tenant a
// request NAMES is not the tenant it runs in. Every transport's entry check
// authorizes the route's permission against the caller's own tenant, and the
// built-in Admin role holds tenants:read, tenants:update, tenants:delete and
// tenants:list in every tenant -- so without a second check a tenant-local
// Admin could read, change, delete or list ANY tenant. The decisions below are
// that second check, and they live here so the transports cannot drift apart
// on it.
//
// Creating a tenant and setting its quotas, entitlements or isolation tier
// need no second check of their own: tenants:create, tenants:set_quotas,
// tenants:set_entitlements and tenants:set_isolation are ScopeCross
// (pkg/permission/tenant_provisioning_scope.go), so the entry check itself
// admits only a system-tenant identity.

// AuthorizeTenantUpdate authorizes patch against the tenant it names.
// tenants:update covers only what a tenant may edit on itself, its display
// name; a patch that changes the isolation tier additionally needs
// tenants:set_isolation, a platform-operator permission. Both are re-checks
// behind the transport's tenants:update entry check (AuthorizeTenantTarget)
// and charge no further rate-limit token.
func (m *AuthMiddleware) AuthorizeTenantUpdate(ctx context.Context, tenantID string, patch tenant.TenantPatch) error {
	// tenants:update gates the display name (and a no-op/empty patch, as a
	// baseline). A pure isolation-type change needs tenants:set_isolation
	// ONLY: the system-tenant lift tenants:update relied on for this case was
	// removed (owner ruling, 2026-10-01), and tenants:set_isolation -- a
	// platform-operator, ScopeCross permission -- already decides it on its
	// own without needing the self-scope permission alongside it.
	if patch.Name != "" || patch.IsolationType == "" {
		if err := m.AuthorizeTenantTarget(ctx, tenantID, permission.TenantsUpdate); err != nil {
			return err
		}
	}
	if patch.IsolationType != "" {
		if err := m.AuthorizeTenantTarget(ctx, tenantID, permission.TenantsSetIsolation); err != nil {
			return err
		}
	}
	return nil
}

// FilterTenantList narrows tenants, the result of TenantServicer.ListTenants,
// to what the caller may enumerate. A caller holding tenants:list:cross
// against the system tenant sees every tenant; any other caller sees only its
// own tenant, because the full list discloses every tenant's configuration --
// data_root included -- to a principal with no authority over them.
//
// The tenants:list:cross check is a CAPABILITY PROBE, not an access decision:
// the request itself was already authorized (and recorded) by the entry
// check's tenants:list, and either outcome here answers it. So the probe is
// decide-only -- it charges no rate-limit token and writes no audit record.
// Recording it would log a denial for every ordinary tenant-Admin list, and
// would make a tenant Admin's list fail whenever the audit sink could not
// record that non-event.
//
// Only an RBAC refusal narrows the list. Any other failure -- an
// unauthenticated context, a sealed tenant IAM, an RBAC resolution error --
// is returned, so the caller is refused rather than silently answered.
func (m *AuthMiddleware) FilterTenantList(ctx context.Context, tenants []tenant.TenantInfo) ([]tenant.TenantInfo, error) {
	caller := auth.IdentityFromContext(ctx)
	if caller == nil {
		return nil, auth.ErrUnauthenticated
	}
	if err := m.revalidateDelegation(ctx, caller); err != nil {
		return nil, err
	}
	err := m.decide(tenant.WithTenantID(ctx, tenantid.System), caller, permission.TenantsListCross)
	if err == nil {
		return tenants, nil
	}
	if ClassifyAuthError(err) != AuthFailureUnauthorized {
		return nil, err
	}
	own := make([]tenant.TenantInfo, 0, 1)
	for i := range tenants {
		if tenants[i].ID == caller.TenantID {
			own = append(own, tenants[i])
		}
	}
	return own, nil
}
