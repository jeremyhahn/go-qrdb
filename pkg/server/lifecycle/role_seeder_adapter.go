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

package lifecycle

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// roleSeederAdapter satisfies tenant.RoleSeeder for a PREFIX tenant by
// building a role.Store scoped to the SAME "{tenantID}/" namespace
// tenant.StoreResolver.sharedFor confines every other prefix-tenant IAM
// read/write to (tenant.IdentityNamespace) -- the exact namespace `roles
// list`/`roles reconcile` read from moments after tenant.CreateTenant
// returns. It cannot simply wrap the already-constructed, non-tenant-routed
// role.RoleServicer (role.NewStore(shared), unnamespaced): that instance's
// own internal tenant-scoped KEYS still live directly on the shared store's
// root, one level of confinement short of where the ROUTED role service
// (built from a StoreResolver, itself built from n.Tenant -- this very
// tenant.CreateTenant call -- which does not exist yet) will later look.
//
// It exists at this composition layer, not in pkg/tenant, because pkg/role
// already imports pkg/tenant (for ReconcileBuiltInRoles's own tenant
// scoping and for tenant.IdentityNamespace), so pkg/tenant cannot import
// pkg/role back.
type roleSeederAdapter struct {
	shared kvstore.KVStore
}

// ReconcileBuiltInRoles implements tenant.RoleSeeder.
func (a roleSeederAdapter) ReconcileBuiltInRoles(ctx context.Context, tenantID string) error {
	ns, err := kvstore.NewNamespacedKVStore(a.shared, tenant.IdentityNamespace(tenantID))
	if err != nil {
		return err
	}
	store, err := role.NewStore(ns)
	if err != nil {
		return err
	}
	_, err = store.ReconcileBuiltInRoles(ctx, tenantID)
	return err
}
