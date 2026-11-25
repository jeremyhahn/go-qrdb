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

package firstboot

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// StrictSeparationSOAdminError refuses a boot under strict separation of
// duties (auth.separation_of_duties=true) while a system-tenant account
// holds the Security Officer role together with system or tenant Admin.
// Standard mode allows naming the admin as the system SO; SPEC row 124
// forbids the combination once strict separation is in force, and the
// role-write gates alone never revisit an account that already holds both.
type StrictSeparationSOAdminError struct {
	// UserID is the account holding both roles.
	UserID uint64
	// Username is that account's username.
	Username string
	// AdminTenantID is the tenant whose Admin role the account holds.
	AdminTenantID string
}

func (e *StrictSeparationSOAdminError) Error() string {
	return fmt.Sprintf("strict separation of duties: user %d (%q) holds the Security Officer role together with Admin in tenant %q; "+
		"restart with auth.separation_of_duties=false and move the SO role to a distinct principal",
		e.UserID, e.Username, e.AdminTenantID)
}

// CheckStrictSeparationHolders fails closed at the posture change: under
// strict separation every boot path (the daemon and the embedded node)
// calls it before first boot, and it walks the system tenant's SO holders
// and refuses with *StrictSeparationSOAdminError if any of them also holds
// Admin, in the system tenant or in any tenant one of its role bindings
// names. A system tenant without an SO role yet (a first boot) has nothing
// to check. A tenant-local account holding both roles in its own tenant is
// refused on every request by the strict authorizer (rbac.WithStrictSeparation),
// whichever tenant store it lives in, so this boot check need not open every
// tenant's store.
func CheckStrictSeparationHolders(ctx context.Context, userSvc user.UserServicer, roleSvc role.NameResolver) error {
	sysCtx := tenant.WithTenantID(ctx, tenantid.System)
	so, err := roleSvc.GetRoleByName(sysCtx, tenantid.System, role.RoleNameSO)
	if err != nil {
		var notFound *role.RoleNotFoundError
		if errors.As(err, &notFound) {
			return nil
		}
		return fmt.Errorf("strict separation of duties: resolve the system SO role: %w", err)
	}
	if so == nil {
		return nil
	}
	holders, err := user.UsersHoldingRole(sysCtx, userSvc, tenantid.System, so.ID)
	if err != nil {
		return fmt.Errorf("strict separation of duties: list the system SO holders: %w", err)
	}
	for _, u := range holders {
		tenants := []string{tenantid.System}
		for _, b := range u.RoleBindings {
			if t := tenantid.Normalize(b.TenantID); !slices.Contains(tenants, t) {
				tenants = append(tenants, t)
			}
		}
		for _, t := range tenants {
			admin, err := role.HoldsSystemRole(ctx, roleSvc, t, user.HeldRoleIDs(u, t), role.RoleNameAdmin)
			if err != nil {
				return fmt.Errorf("strict separation of duties: resolve the Admin role in tenant %q: %w", t, err)
			}
			if admin {
				return &StrictSeparationSOAdminError{UserID: u.ID, Username: u.Username, AdminTenantID: t}
			}
		}
	}
	return nil
}
