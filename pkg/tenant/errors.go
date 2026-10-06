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

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

var (
	// ErrTenantNotFound is returned when a requested tenant does not exist
	// in the metadata store.
	ErrTenantNotFound = errors.New("tenant: not found")

	// ErrTenantHasData is returned when an isolation type change is requested
	// on a tenant that already has stored data. Migrate or delete the data
	// before changing the isolation type.
	ErrTenantHasData = errors.New("tenant: has data, isolation type cannot be changed")

	// ErrTenantDataUnverifiable is returned when an isolation type change is
	// requested and no TenantDataProbe is wired to confirm the tenant holds
	// no data. The precondition fails closed rather than risk orphaning data.
	ErrTenantDataUnverifiable = errors.New("tenant: cannot verify the tenant holds no data, isolation type cannot be changed")

	// ErrTenantAlreadyExists is returned when attempting to create a tenant
	// that already has metadata stored.
	ErrTenantAlreadyExists = errors.New("tenant: already exists")

	// ErrTenantStoreNotAtomic is returned by CreateTenant when the tenant
	// metadata store cannot commit a record conditioned on the tenant's
	// tombstone being absent in one atomic step (kvstore.FencedCreator). It
	// is refused before any side effect rather than degraded to a check
	// followed by a write: that window is how a create racing a delete
	// committed a live record for a permanently retired id.
	ErrTenantStoreNotAtomic = errors.New("tenant: metadata store cannot commit a create atomically against the tombstone")

	// ErrEmptyTenantID is returned when an empty tenant ID is provided.
	ErrEmptyTenantID = errors.New("tenant: empty tenant ID")

	// ErrEmptyTenantName is returned when an empty tenant name is provided.
	ErrEmptyTenantName = errors.New("tenant: empty tenant name")

	// ErrEmptyPassphrase is returned when a passphrase-strategy barrier
	// operation (init/unseal) is invoked without a caller-supplied
	// passphrase. There is no derived fallback: the tenant admin must
	// supply the secret.
	ErrEmptyPassphrase = errors.New("tenant: empty passphrase")

	// ErrEmptyShare is returned when a holder quorum submission carries no
	// share value.
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

// ReservedTenantIDError is returned when a caller attempts to create or
// delete a tenant whose id is reserved by the platform. The reserved set is
// declared in exactly one place, reservedTenantIDReason (reserved.go); Reason
// carries why the id is reserved.
//
// Trusted internal callers (first-boot bootstrap) bypass the check on CREATE
// by passing a context marked with tenant.WithInternalCall. The DELETE path
// (validateDeletableTenantID) has no bypass: nothing deletes a reserved
// tenant.
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

// PlatformOperatorRequiredError's shape: a caller not bound to the system
// tenant attempting a platform-operator-only tenant provisioning operation
// (creating a tenant, or changing its quotas, entitlements or isolation
// tier). These are ScopeCross permissions in the RBAC layer
// (pkg/permission/tenant_provisioning_scope.go), and the ONE mechanism that
// refuses them, on every transport alike including the embedded one, is
// AuthMiddleware.AuthorizeTarget's ScopeCross decision -- reached through
// *rbac.PermissionDeniedError (wrapping auth.ErrUnauthorized), not this
// type. This type is retained for the wire shape pkg/api/errclass/table.go
// still classifies (403 Forbidden / PermissionDenied, exactly like
// *CrossTenantDeniedError) but nothing in this repository constructs it:
// RequirePlatformOperator, the structural check that used to produce it
// independently of AuthMiddleware, was retired (go-qrdb's "one pluggable
// auth chain on every transport" ruling) because it was a SECOND mechanism
// answering the same question the RBAC decision already answers, and the
// two could disagree.
type PlatformOperatorRequiredError struct {
	// IdentityTenantID is the caller's authenticated (or, with none bound,
	// implicit system-tenant) tenant.
	IdentityTenantID string
	// Op names the attempted operation, e.g. "create a tenant", "set quotas".
	Op string
}

func (e *PlatformOperatorRequiredError) Error() string {
	return "tenant: caller in tenant " + e.IdentityTenantID +
		" is not the platform operator and cannot " + e.Op
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
//
// transient is set at construction (see newStoreError), by the code that
// actually saw the underlying failure -- never inferred later from Op, which
// names WHERE the failure happened, not WHAT KIND it is.
type StoreError struct {
	Op  string
	Err error

	transient bool
}

func (e *StoreError) Error() string {
	return "tenant: " + e.Op + ": " + e.Err.Error()
}

func (e *StoreError) Unwrap() error {
	return e.Err
}

// ReadTransient reports whether retrying the operation that produced this
// error might succeed: the store read itself failed, as opposed to a
// definitive answer -- corrupt stored data, or a cryptographic refusal from a
// sealed-barrier-backed store -- that no retry changes. See newStoreError.
//
// Named ReadTransient rather than the shorter Transient to avoid a
// structural collision with *service.FactorMigrationRefusedError's own
// Transient() bool, an unrelated capability ("does a later migration run
// still have work to do").
func (e *StoreError) ReadTransient() bool { return e.transient }

// newStoreError builds a *StoreError and decides transience from cause
// ITSELF, not from op: a cryptographic refusal (decrypt, authentication) is
// never transient, because no retry decrypts a tampered ciphertext or
// authenticates with the wrong key. Everything else is presumed to be the
// read itself failing, which retrying can fix.
func newStoreError(op string, cause error) *StoreError {
	return &StoreError{Op: op, Err: cause, transient: isTransientCause(cause)}
}

// isTransientCause reports whether cause, the error a store's own Get/List
// returned, describes a failure worth retrying. See role.isTransientCause,
// which makes the identical decision for the role store's own StoreError;
// the two packages duplicate this rather than share a leaf package, because
// each is two lines and a shared dependency for them would outweigh the
// duplication.
func isTransientCause(cause error) bool {
	return !errors.Is(cause, seal.ErrDecryptionFailed)
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

// DataRootIsolationTypeError is returned when CreateTenant's WithVolumePool
// option is used with an isolation type that has no meaningful notion of a
// dedicated filesystem location (prefix or barrier isolation). Only
// IsolationVolume and IsolationRaft tenants are placed on a volume pool.
type DataRootIsolationTypeError struct {
	TenantID      string
	IsolationType IsolationType
}

func (e *DataRootIsolationTypeError) Error() string {
	return "tenant: a volume pool is not meaningful for tenant " + e.TenantID +
		" with isolation type " + string(e.IsolationType) + " (requires volume or raft isolation)"
}

// VolumePoolReserveError is returned when CreateTenant's reservation of a
// volume pool's capacity fails: see volume.Catalog.Reserve's own error
// classes (*volume.PoolNotFoundError, *volume.PoolCapacityExceededError,
// *volume.InvalidAssignmentPathError, *volume.ReservationConflictError) for
// why. Nothing was registered: the barrier is provisioned only after the
// reservation succeeds.
type VolumePoolReserveError struct {
	TenantID string
	Pool     string
	Err      error
}

func (e *VolumePoolReserveError) Error() string {
	return "tenant: could not reserve volume pool " + e.Pool + " for tenant " + e.TenantID + ": " + e.Err.Error()
}

func (e *VolumePoolReserveError) Unwrap() error { return e.Err }

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

// TenantDataProbeError reports that the data probe could not answer whether a
// tenant holds data, so an isolation type change was refused.
type TenantDataProbeError struct {
	TenantID string
	Err      error
}

// Error names the tenant and the cause.
func (e *TenantDataProbeError) Error() string {
	return "tenant: cannot verify tenant " + e.TenantID + " holds no data: " + e.Err.Error()
}

// Unwrap returns the probe's error.
func (e *TenantDataProbeError) Unwrap() error { return e.Err }

// InvalidPageRequestError is returned by ListDeletedTenants for a page size or
// page token the caller supplied that cannot be honoured. It is the caller's
// error, and maps to 400 / InvalidArgument.
type InvalidPageRequestError struct {
	// Field names the offending request field: "page_size" or "page_token".
	Field string
	// Reason says what is wrong with it.
	Reason string
}

func (e *InvalidPageRequestError) Error() string {
	return "tenant: invalid " + e.Field + ": " + e.Reason
}

// ResidueReconcileError is returned by ReconcileDeletedTenants when one step
// of reclaiming a retired tenant's residue on this node failed. Every step is
// idempotent, so re-issuing the reconcile resumes it; the error names the
// tenant and the step so an operator can see where it stopped.
type ResidueReconcileError struct {
	TenantID string
	// Op is the step that failed: "list-tombstones", "list-tenants",
	// "candidates", "close-runtime", "forget-barrier" or "remove".
	Op string
	// Path is the directory involved, when the step concerned one.
	Path string
	Err  error
}

func (e *ResidueReconcileError) Error() string {
	msg := "tenant: reconcile deleted tenant"
	if e.TenantID != "" {
		msg += " " + e.TenantID
	}
	msg += ": " + e.Op
	if e.Path != "" {
		msg += " " + e.Path
	}
	return msg + ": " + e.Err.Error()
}

func (e *ResidueReconcileError) Unwrap() error { return e.Err }

// ResidueReconcileFailuresError is returned by ReconcileDeletedTenants when one or
// more retired ids could not be fully reclaimed. A per-id failure does not
// stop the pass: the other ids are still reclaimed, the report describes what
// was done, and this error collects what was not. Each entry is a
// *ResidueReconcileError; errors.Is and errors.As reach every one of them.
type ResidueReconcileFailuresError struct {
	Errors []*ResidueReconcileError
}

func (e *ResidueReconcileFailuresError) Error() string {
	msg := "tenant: reconcile deleted tenants: " + strconv.Itoa(len(e.Errors)) + " failed"
	for _, err := range e.Errors {
		msg += "; " + err.Error()
	}
	return msg
}

// Unwrap returns every collected failure.
func (e *ResidueReconcileFailuresError) Unwrap() []error {
	out := make([]error, len(e.Errors))
	for i, err := range e.Errors {
		out[i] = err
	}
	return out
}
