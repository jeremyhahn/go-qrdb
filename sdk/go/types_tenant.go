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

// IsSystemTenant reports whether id denotes the structurally privileged
// system tenant. It returns true for both SystemTenantID and the empty
// string, since an unbound tenant resolves to the system tenant (see
// TenantFromContext / ContextWithTenant). It is the canonical predicate for
// "is this the system scope" and should be used instead of comparing
// against SystemTenantID directly. (Aliases tenant.IsSystemTenant.)
var IsSystemTenant = tenant.IsSystemTenant

// NormalizeTenantID folds an absent tenant ("" — no tenant binding) onto
// SystemTenantID and returns every other value unchanged.
//
// It is the enforcement half of IsSystemTenant's precondition: a consumer
// that binds a tenant onto a context, a session or an identity calls this at
// that boundary, so IsSystemTenant is never asked about an unnormalized
// value further in. A boundary that folds only one of the two — the context
// but not the identity — is the defect #188 records, because the
// authorization predicate reads the identity. (Aliases tenant.Normalize.)
var NormalizeTenantID = tenant.Normalize

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

// ErrTenantBarrierNotEnabled is returned by the per-tenant barrier surface when
// a barrier operation is attempted on a node whose seal subsystem is disabled
// (no seal: configuration — SealBarrier == nil). Per-tenant barriers REQUIRE
// the seal subsystem; the kv-backed tenant service fails closed with this
// sentinel for every barrier verb. (Aliases pkg/tenant.ErrBarrierNotEnabled;
// named with the Tenant prefix per the SDK convention for pkg/tenant sentinels.
// A composing service should map it to 422 Unprocessable Entity on REST and
// codes.FailedPrecondition on gRPC, matching qrdb's own transport canon.)
var ErrTenantBarrierNotEnabled = tenant.ErrBarrierNotEnabled

// ErrTenantStoreSealed is returned by TenantStoreResolver.Resolve, and by
// every service built over a routed store, when a barrier-isolated tenant's
// barrier is sealed: IAM and the ceremony-time auth-factor surface both fail
// closed on this rather than ever serving that tenant from the shared system
// store. (Aliases pkg/tenant.ErrTenantSealed; named ErrTenantStoreSealed,
// matching TenantStoreResolver above, to avoid colliding with the seal
// package's ErrTenantSealed, which is a distinct sentinel raised by barrier
// operations rather than store resolution — the two conditions read alike
// but are checked with errors.Is against different values, and a consumer
// that only recognizes one will not recognize the other. A composing service
// should map it to 503 Service Unavailable / codes.Unavailable, matching
// qrdb's own transport canon.)
var ErrTenantStoreSealed = tenant.ErrTenantSealed

// ---------------------------------------------------------------------------
// Tenant teardown (go-qrdb#413)
//
// Deleting a tenant RETIRES its id. The id cannot be used again, because the
// system cannot answer "is this the same principal?" for a caller-supplied
// name, and answering it wrongly re-attaches a new principal to the previous
// one's keyspace.
//
// A consumer provisioning tenants therefore has to handle one new outcome --
// "that id was used and deleted" -- which is why the error type is aliased
// here rather than left behind an opaque string.
// ---------------------------------------------------------------------------

// TenantTombstone is the durable record of a deleted tenant id: the id, the
// display name and isolation type it had, its dedicated DataRoot when it had
// one, and when it was created and deleted. (Aliases pkg/tenant.Tombstone.)
type TenantTombstone = tenant.Tombstone

// TenantDeletedError is returned by CreateTenant when the requested id has
// already been used and deleted. It carries the retirement timestamp. A
// composing service should map it to 409 Conflict on REST and
// codes.AlreadyExists on gRPC, matching qrdb's own transport canon.
// (Aliases pkg/tenant.TenantDeletedError.)
type TenantDeletedError = tenant.TenantDeletedError

// TenantHasDatabasesError is returned by DeleteTenant while the tenant still
// owns databases, and names them. Teardown is bottom-up: each database's own
// drop reclaims the keyspace that database owns, so the tenant's is refused
// until they are gone. Maps to 409 Conflict / codes.FailedPrecondition.
// (Aliases pkg/tenant.TenantHasDatabasesError.)
type TenantHasDatabasesError = tenant.TenantHasDatabasesError

// TenantTeardownError wraps every DeleteTenant failure that happens AFTER the
// tenant id has been durably retired -- the shred, the barrier destroy and the
// keyspace reclaim. Seeing it means the tenant is already gone from every
// reader's view and the remedy is always to re-issue the delete, which resumes.
// A failure BEFORE the retirement is NOT wrapped in it and leaves the tenant
// intact. Maps to 500 Internal / codes.Internal.
// (Aliases pkg/tenant.TeardownError.)
type TenantTeardownError = tenant.TeardownError

// TenantKeyspaceReclaimError is returned by DeleteTenant when the tenant's
// keyspace could not be reclaimed. It surfaces AFTER the id is retired, so
// the security half of the deletion has taken effect and re-issuing the
// delete resumes the reclamation. (Aliases pkg/tenant.KeyspaceReclaimError.)
type TenantKeyspaceReclaimError = tenant.KeyspaceReclaimError

// TenantDatabaseListError is returned by DeleteTenant when the tenant's
// databases could not be enumerated, so the bottom-up refusal cannot be
// evaluated. (Aliases pkg/tenant.DatabaseListError.)
type TenantDatabaseListError = tenant.DatabaseListError

// ---------------------------------------------------------------------------
// Tenant teardown seams (Tier 2 -- composition plane)
//
// Each is a Go interface a composition supplies in-process, so none of them
// can be shipped over a transport. They are wired through TenantTeardownWiring
// on the TenantServicer the node built.
// ---------------------------------------------------------------------------

// TenantDatabaseLister reports the databases a tenant still owns; it is what
// DeleteTenant consults for its bottom-up refusal. qrdb's own database
// manager satisfies it. (Aliases pkg/tenant.DatabaseLister.)
type TenantDatabaseLister = tenant.DatabaseLister

// TenantKeyspaceReclaimer deletes the data a tenant owns in the shared store.
// qrdb's KeyspaceReclaimService satisfies it, and the same instance serves
// DROP DATABASE. (Aliases pkg/tenant.KeyspaceReclaimer.)
type TenantKeyspaceReclaimer = tenant.KeyspaceReclaimer

// TenantTeardownWiring is the post-construction wiring surface for the two
// seams above, implemented by every TenantServicer qrdb ships. A composition
// resolves it from the tenant service it built. (Aliases
// pkg/tenant.TeardownWiring.)
type TenantTeardownWiring = tenant.TeardownWiring

// WithTenantDatabaseLister injects the catalog DeleteTenant consults before
// it refuses a tenant that still owns databases.
// (Aliases pkg/tenant.WithDatabaseLister.)
var WithTenantDatabaseLister = tenant.WithDatabaseLister

// WithTenantKeyspaceReclaimer injects the reclaimer DeleteTenant runs after
// the retirement commits. (Aliases pkg/tenant.WithKeyspaceReclaimer.)
var WithTenantKeyspaceReclaimer = tenant.WithKeyspaceReclaimer
