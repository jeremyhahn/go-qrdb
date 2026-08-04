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

package sdk

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TenantStoreResolver maps a tenant ID to the kvstore.KVStore that backs that
// tenant's IAM data. Barrier-isolated tenants route through their own
// TenantBarrier; prefix/system tenants use the shared system store.
// Resolution fails closed (ErrTenantSealed) for a sealed barrier tenant.
// (Aliases pkg/tenant.StoreResolver.)
type TenantStoreResolver = tenant.StoreResolver

// ContextWithTenant returns a context that binds the given tenant ID as the
// target tenant for tenant-aware operations such as RBAC().Authorize. The
// RBAC authorizer derives the request's target tenant from the context; bind
// the acting identity's tenant here so a self-scope permission resolves
// against the identity's own tenant. An empty tenantID leaves the context
// resolving to the system tenant.
func ContextWithTenant(ctx context.Context, tenantID string) context.Context {
	return tenant.WithTenantID(ctx, tenantID)
}

// TenantFromContext returns the tenant ID bound to the context by
// ContextWithTenant with comma-ok semantics. ok is false when no tenant is
// bound or the bound value is empty — such contexts resolve to the implicit
// system tenant, but no SystemTenantID fallback is applied here so callers
// can distinguish an explicit binding from the default.
func TenantFromContext(ctx context.Context) (string, bool) {
	return tenant.IDFromContext(ctx)
}

// ---------------------------------------------------------------------------
// Tenant metadata types
//
// Note: ServiceTenantInfo, ServiceTenantBarrierStatus, and
// ServiceTenantServicer are re-exported in types_service.go. The aliases below
// cover the remaining tenant primitives and use a Tenant-prefix where a plain
// name would collide with another package's symbol in the flat SDK.
// ---------------------------------------------------------------------------

// TenantPatch is a sparse update to a tenant's metadata.
type TenantPatch = tenant.TenantPatch

// TenantIsolationType identifies a tenant's isolation mode. (Aliases
// pkg/tenant.IsolationType.)
type TenantIsolationType = tenant.IsolationType

// CreateTenantOption configures optional CreateTenant behavior. (Aliases
// pkg/tenant.CreateTenantOption.) Re-exported so SDK consumers that
// implement the first-boot TenantCreator seam can name the variadic
// option type without importing pkg/tenant directly.
type CreateTenantOption = tenant.CreateTenantOption

// TenantIsolationBarrier is the per-tenant barrier isolation mode. (Aliases
// pkg/tenant.IsolationBarrier.)
const TenantIsolationBarrier = tenant.IsolationBarrier

// SystemTenantID is the identifier of the reserved system tenant.
const SystemTenantID = tenant.SystemTenantID

// ErrTenantExists is returned by the tenant store when creating a tenant whose
// id already exists. (Aliases pkg/tenant.ErrTenantAlreadyExists; named
// ErrTenantExists to avoid colliding with the seal package's
// ErrTenantAlreadyExists, which is a distinct sentinel.)
var ErrTenantExists = tenant.ErrTenantAlreadyExists

// ErrTenantMissing is returned by the tenant service when an operation names a
// tenant that does not exist (e.g. GetTenant / UpdateQuotas / UpdateEntitlements
// on an unknown tenant). (Aliases pkg/tenant.ErrTenantNotFound; named
// ErrTenantMissing to avoid colliding with the seal package's ErrTenantNotFound,
// which is a distinct sentinel. A composing service should map it to 404.)
var ErrTenantMissing = tenant.ErrTenantNotFound
