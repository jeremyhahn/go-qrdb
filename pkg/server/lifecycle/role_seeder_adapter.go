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
//
// The store is wired WithIdentityIndex/WithAtomicIdentity, exactly like
// cmd/qrdbd's own boot-time role stores (bootRoleSvc/roleSvc in
// bootstrapUserRoleStores): a.shared already satisfies
// kvstore.MultiGuardedBatchKVStore (it is built via
// NewGuardedKVStoreAdapter), NewNamespacedKVStore preserves that capability
// on ns, and an identity index built over ns itself is -- by construction,
// not assumption -- the SAME physical store this role store writes to,
// which is WithAtomicIdentity's precondition.
//
// Without this, CreateRole's non-atomic fallback writes the primary record,
// the tenant index, and the name index as three SEPARATE sequential
// proposals with no identity claim at all (recordIdentity is a no-op when
// no identity index is configured). A concurrent caller racing the SAME
// built-in role under the SAME deterministic id -- a node's own
// registerBuiltInRoles, running concurrently on another replica while THIS
// CreateTenant call seeds the tenant it just created -- can then observe
// the primary record by id (CreateRole's own id-uniqueness precheck) before
// the name index exists, verify it is "already seeded" by NAME, find
// nothing, and refuse the whole first-boot run even though the role is (or
// is about to be) seeded correctly. Writing the claim and all three records
// as ONE guarded batch, as firstboot's own stores do, closes that window
// here too, so every caller that can seed a tenant's built-in roles agrees
// on the SAME atomicity guarantee.
func (a roleSeederAdapter) ReconcileBuiltInRoles(ctx context.Context, tenantID string) error {
	ns, err := kvstore.NewNamespacedKVStore(a.shared, tenant.IdentityNamespace(tenantID))
	if err != nil {
		return err
	}
	idx, err := tenant.NewIdentityIndex(ns)
	if err != nil {
		return err
	}
	store, err := role.NewStore(ns, role.WithIdentityIndex(idx), role.WithAtomicIdentity())
	if err != nil {
		return err
	}
	_, err = store.ReconcileBuiltInRoles(ctx, tenantID)
	return err
}
