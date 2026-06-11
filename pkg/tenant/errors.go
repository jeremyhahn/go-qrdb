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
