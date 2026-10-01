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

package tenant

import (
	"errors"
	"fmt"
	"strconv"
	"time"
)

var (
	// ErrTenantNotFound is returned when a requested tenant does not exist
	// in the metadata store.
	ErrTenantNotFound = errors.New("tenant: not found")

	// ErrTenantHasData is returned when an isolation type change is requested
	// on a tenant that already has stored data. Migrate or delete the data
	// before changing the isolation type.
	ErrTenantHasData = errors.New("tenant: has data, isolation type cannot be changed")

	// ErrTenantAlreadyExists is returned when attempting to create a tenant
	// that already has metadata stored.
	ErrTenantAlreadyExists = errors.New("tenant: already exists")

	// ErrEmptyTenantID is returned when an empty tenant ID is provided.
	ErrEmptyTenantID = errors.New("tenant: empty tenant ID")

	// ErrEmptyTenantName is returned when an empty tenant name is provided.
	ErrEmptyTenantName = errors.New("tenant: empty tenant name")

	// ErrEmptyPassphrase is returned when a passphrase-strategy barrier
	// operation (init/unseal) is invoked without a caller-supplied
	// passphrase. There is no derived fallback: the tenant admin must
	// supply the secret.
	ErrEmptyPassphrase = errors.New("tenant: empty passphrase")

	// ErrEmptyShare is returned when a Shamir unseal-share operation is
	// invoked without a caller-supplied share value.
	ErrEmptyShare = errors.New("tenant: empty shamir share")

	// ErrNilStore is returned when a nil storage backend is provided
	// to the tenant service constructor.
	ErrNilStore = errors.New("tenant: nil storage backend")

	// ErrNilRegistry is returned when a nil barrier registry is provided
	// to the tenant service constructor.
	ErrNilRegistry = errors.New("tenant: nil barrier registry")

	// ErrNilTenantServicer is returned when a nil TenantServicer is
	// provided to the store resolver constructor.
	ErrNilTenantServicer = errors.New("tenant: nil tenant servicer")

	// ErrNilQuotas is returned when a nil quotas pointer is provided
	// to UpdateQuotas.
	ErrNilQuotas = errors.New("tenant: nil quotas")

	// ErrNilEntitlements is returned when a nil entitlements pointer is
	// provided to UpdateEntitlements.
	ErrNilEntitlements = errors.New("tenant: nil entitlements")

	// ErrTenantSealed is returned by the store resolver when a
	// barrier-isolated tenant's barrier is sealed. IAM routing fails
	// closed on this error: a barrier tenant's data is NEVER served from
	// the shared system store while its barrier is sealed.
	ErrTenantSealed = errors.New("tenant: barrier is sealed")

	// ErrSystemTenantMustBePrefix is returned when barrier isolation is
	// requested for the reserved system tenant. The system tenant must be
	// a logical-prefix tenant so its IAM data lives in the shared store and
	// remains reachable during bootstrap while tenant barriers are sealed.
	ErrSystemTenantMustBePrefix = errors.New("tenant: system tenant must use prefix isolation")

	// ErrEmptyDataRoot is returned when CreateTenant's WithDataRoot option is
	// used with an empty path. An explicit request for a dedicated-disk
	// DataRoot must name a real path; it is never silently treated as "no
	// preference" (which would fall back to pool allocation without telling
	// the caller its request was dropped).
	ErrEmptyDataRoot = errors.New("tenant: empty data root")
)

// InvalidTenantIDError is returned when a tenant ID is empty or malformed.
type InvalidTenantIDError struct {
	TenantID string
	Reason   string
}

func (e *InvalidTenantIDError) Error() string {
	return "invalid tenant ID " + e.TenantID + ": " + e.Reason
}

// InvalidTenantIDCharError is returned when a tenant ID contains a character
// that is not permitted. Tenant IDs must not contain control characters (bytes
// < 0x20), the unit-separator byte (0x1f) used as the scoped-key separator,
// or path separators ('/', '\') — tenant ids become filesystem path segments
// under barrier isolation.
type InvalidTenantIDCharError struct {
	TenantID string
	Byte     byte
}

func (e *InvalidTenantIDCharError) Error() string {
	return fmt.Sprintf("tenant: id %q contains illegal character 0x%02x", e.TenantID, e.Byte)
}

// ReservedTenantIDError is returned when a caller attempts to create a
// tenant whose id is reserved by the platform. The set of reserved ids is:
//
//   - "system"        — the structurally privileged platform tenant.
//   - "default"       — legacy id; permanently reserved so it can never be
//     accidentally re-created.
//   - any id starting with "_"  — reserved for internal future use.
//   - any id containing ":"     — reserved for compound-id schemes.
//
// Trusted internal callers (first-boot bootstrap) bypass the check by
// passing a context marked with tenant.WithInternalCall.
type ReservedTenantIDError struct {
	TenantID string
	Reason   string
}

func (e *ReservedTenantIDError) Error() string {
	return "tenant: id " + e.TenantID + " is reserved: " + e.Reason
}

// TenantScopeError is returned when a caller bound to one tenant attempts a
// per-tenant barrier operation against a different tenant. This is the
// service-layer tenant-isolation defense: transport-level RBAC
// (AuthorizeTenant + ScopeTenantOnly) is the first gate, but in-process
// surfaces and any transport that fails to bind the true target tenant must
// still fail closed here. Callers bound to the system tenant (daemon-internal
// flows and embedded in-process hosts carry no tenant binding and resolve to
// the system tenant) are exempt.
type TenantScopeError struct {
	CallerTenantID string
	TargetTenantID string
	Op             string
}

func (e *TenantScopeError) Error() string {
	return "tenant: caller " + e.CallerTenantID + " is not permitted to " +
		e.Op + " barrier of tenant " + e.TargetTenantID
}

// CrossTenantDeniedError is returned by IAM routing services when a caller
// authenticated to one tenant supplies a different, non-empty target tenant
// in the request. IAM records (users, roles) are routed and stored strictly
// on the caller's identity tenant; only the system tenant may target another
// tenant. This mirrors the data-plane rule that the effective tenant is NEVER
// sourced from client-supplied request fields — transport-level RBAC scopes a
// caller to its own tenant, but the routing layer must independently refuse to
// act on a client-supplied tenant so a compromised or mis-wired handler cannot
// reach another tenant's IAM store. It maps to 403 Forbidden on REST,
// codes.PermissionDenied on gRPC/QUIC, and the permission-denied MCP contract.
type CrossTenantDeniedError struct {
	// IdentityTenantID is the caller's authenticated tenant.
	IdentityTenantID string
	// RequestedTenantID is the (rejected) client-supplied target tenant.
	RequestedTenantID string
	// Op names the attempted IAM operation, e.g. "create user".
	Op string
}

func (e *CrossTenantDeniedError) Error() string {
	return "tenant: caller in tenant " + e.IdentityTenantID +
		" is not permitted to " + e.Op + " in tenant " + e.RequestedTenantID
}

// QuotaExceededError is returned by IncrementQuotaUsage when adding delta
// would cause the tenant's current usage to exceed the configured limit.
type QuotaExceededError struct {
	TenantID string
	Resource string
	Current  int64
	Delta    int64
	Limit    int64
}

func (e *QuotaExceededError) Error() string {
	return fmt.Sprintf("tenant: quota exceeded for %s: %s current=%d delta=%d limit=%d",
		e.TenantID, e.Resource, e.Current, e.Delta, e.Limit)
}

// StoreError is returned when a storage operation fails within the
// tenant service.
type StoreError struct {
	Op  string
	Err error
}

func (e *StoreError) Error() string {
	return "tenant: " + e.Op + ": " + e.Err.Error()
}

func (e *StoreError) Unwrap() error {
	return e.Err
}

// BarrierError is returned when a barrier registry operation fails
// within the tenant service.
type BarrierError struct {
	TenantID string
	Op       string
	Err      error
}

func (e *BarrierError) Error() string {
	return "tenant: barrier " + e.Op + " for " + e.TenantID + ": " + e.Err.Error()
}

func (e *BarrierError) Unwrap() error {
	return e.Err
}

// InvalidDataRootError is returned when CreateTenant's WithDataRoot option is
// used with a path that is not a usable dedicated-disk location (relative,
// non-canonical, or unreadable). See validateExplicitDataRoot.
type InvalidDataRootError struct {
	DataRoot string
	Reason   string
}

func (e *InvalidDataRootError) Error() string {
	return "tenant: invalid data root " + e.DataRoot + ": " + e.Reason
}

// DataRootNotEmptyError is returned when CreateTenant's WithDataRoot option
// names an existing, non-empty directory. A fresh tenant is never registered
// on top of another tenant's (or a deleted tenant's) leftover data; the
// operator must point the option at an empty or not-yet-existing path.
type DataRootNotEmptyError struct {
	DataRoot string
}

func (e *DataRootNotEmptyError) Error() string {
	return "tenant: data root " + e.DataRoot + " is not empty"
}

// DataRootOverlapError is returned when CreateTenant's WithDataRoot option
// names a dedicated-disk path that overlaps a reserved storage location: the
// configured volume-pool base path, the system data dir, or an existing
// tenant's DataRoot. Two paths overlap when they are equal or one is a
// filesystem ancestor of the other. Nesting a tenant's dedicated disk inside
// the pool, the system tree, or another tenant's tree causes data-confusion,
// so the request is rejected up front.
type DataRootOverlapError struct {
	// DataRoot is the operator-supplied path that was rejected.
	DataRoot string
	// Conflict is the reserved path it overlaps.
	Conflict string
	// Kind describes the reserved path (e.g. "volume pool", "system data dir",
	// or "tenant <id>").
	Kind string
}

func (e *DataRootOverlapError) Error() string {
	return "tenant: data root " + e.DataRoot + " overlaps " + e.Kind + " path " + e.Conflict
}

// DataRootIsolationTypeError is returned when CreateTenant's WithDataRoot
// option is used with an isolation type that has no meaningful notion of a
// dedicated filesystem DataRoot (prefix or barrier isolation). Only
// IsolationVolume and IsolationRaft tenants get a dedicated DataRoot.
type DataRootIsolationTypeError struct {
	TenantID      string
	IsolationType IsolationType
}

func (e *DataRootIsolationTypeError) Error() string {
	return "tenant: data root is not meaningful for tenant " + e.TenantID +
		" with isolation type " + string(e.IsolationType) + " (requires volume or raft isolation)"
}

// InvalidStrategyError is returned when the caller requests a seal strategy
// that is not registered in the injected StrategyRegistry, or when an empty
// strategy ID is supplied to TenantBarrierInitWithStrategy.
type InvalidStrategyError struct {
	Strategy  string
	Available []string
}

func (e *InvalidStrategyError) Error() string {
	if len(e.Available) == 0 {
		return "tenant: invalid seal strategy: " + e.Strategy
	}
	avail := e.Available[0]
	for _, a := range e.Available[1:] {
		avail += ", " + a
	}
	return "tenant: invalid seal strategy: " + e.Strategy + " (available: " + avail + ")"
}

// TenantDeletedError is returned by CreateTenant when the requested tenant id
// has already been used and deleted.
//
// A deleted tenant id is retired permanently. Tenant ids are caller-supplied
// names, so "is this the same principal as the one that held the id before?"
// is a question the system cannot answer -- and a delete that silently
// answered "yes" would re-attach a new principal to the previous principal's
// keyspace, which is what go-qrdb#413 measured. The refusal is the answer.
//
// It maps to 409 Conflict on REST and codes.AlreadyExists on gRPC/QUIC: the
// id is not available, in the same sense ErrTenantAlreadyExists means it is
// not available.
type TenantDeletedError struct {
	// TenantID is the retired id.
	TenantID string
	// DeletedAt is when the id was retired. It is the zero time when the
	// tombstone could not be decoded, which does not change the refusal.
	DeletedAt time.Time
}

func (e *TenantDeletedError) Error() string {
	if e.DeletedAt.IsZero() {
		return "tenant: id " + e.TenantID + " was deleted and cannot be reused"
	}
	return "tenant: id " + e.TenantID + " was deleted at " +
		e.DeletedAt.UTC().Format(time.RFC3339) + " and cannot be reused"
}

// TenantHasDatabasesError is returned by DeleteTenant when the tenant still
// owns databases.
//
// Teardown is bottom-up: a database's own drop reclaims the keyspace that
// database owns (go-qrdb#412), so refusing here keeps every layer's teardown
// bounded and makes the leak structurally impossible rather than merely
// handled. The error names the databases so the caller knows exactly what to
// drop.
//
// It maps to 409 Conflict on REST and codes.FailedPrecondition on gRPC/QUIC.
type TenantHasDatabasesError struct {
	// TenantID is the tenant whose deletion was refused.
	TenantID string
	// Databases are the names still owned by the tenant, sorted.
	Databases []string
}

func (e *TenantHasDatabasesError) Error() string {
	list := ""
	for i, d := range e.Databases {
		if i > 0 {
			list += ", "
		}
		list += d
	}
	return "tenant: " + e.TenantID + " still owns " +
		strconv.Itoa(len(e.Databases)) + " database(s) (" + list +
		"); drop them before deleting the tenant"
}

// TeardownError wraps every failure DeleteTenant can report from the far side
// of its fence -- the barrier destroy and the keyspace reclaim.
//
// It exists because the message is the sharp edge of this call. A failure
// BEFORE the fence leaves the tenant exactly as it was, and its error can be
// read as "nothing changed". A failure AFTER it cannot: the id is retired
// permanently, the metadata record is gone, and the tenant no longer resolves
// for anybody. An operator who reads "reclaim failed" as "the delete did not
// happen", shrugs and moves on has left a half-torn-down tenant behind, so
// the sentence has to say what is already true before it says what failed.
//
// Every wrapped step is idempotent, which is why the remedy is always the
// same one: re-issue DeleteTenant.
//
// It wraps rather than replaces, so errors.As still finds *BarrierError and
// *KeyspaceReclaimError underneath it and a transport that
// maps those keeps mapping them.
type TeardownError struct {
	TenantID string
	Err      error
}

func (e *TeardownError) Error() string {
	return "tenant: " + e.TenantID + " is retired and no longer resolvable, but its teardown did not finish; " +
		"re-issue DeleteTenant to complete it: " + e.Err.Error()
}

func (e *TeardownError) Unwrap() error { return e.Err }

// KeyspaceReclaimError is returned by DeleteTenant when the tenant's
// keyspace could not be reclaimed.
//
// It is raised AFTER the fence has committed, so the id is already retired
// and unusable when it surfaces: what failed is the reclamation of the bytes,
// and the error reaches the caller wrapped in *TeardownError. Re-issuing
// DeleteTenant is the remedy -- the reclamation is idempotent, and the delete
// of an already-tombstoned tenant resumes it.
type KeyspaceReclaimError struct {
	TenantID string
	Err      error
}

func (e *KeyspaceReclaimError) Error() string {
	return "tenant: keyspace reclaim for " + e.TenantID + ": " + e.Err.Error()
}

func (e *KeyspaceReclaimError) Unwrap() error { return e.Err }

// DatabaseListError is returned by DeleteTenant when the tenant's databases
// could not be enumerated, so the bottom-up refusal cannot be evaluated. It
// fails the deletion rather than proceeding: an unanswerable "does this
// tenant still own databases?" is not the same answer as "no".
type DatabaseListError struct {
	TenantID string
	Err      error
}

func (e *DatabaseListError) Error() string {
	return "tenant: list databases for " + e.TenantID + ": " + e.Err.Error()
}

func (e *DatabaseListError) Unwrap() error { return e.Err }
