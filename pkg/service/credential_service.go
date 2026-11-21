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

package service

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// CredentialServicer provides credential management operations including
// storage, retrieval, submission, deletion, enumeration, and versioned
// rotation of named credentials.
type CredentialServicer interface {
	// CredentialStore persists a credential under the given name.
	CredentialStore(ctx context.Context, name string, value []byte) error

	// CredentialRetrieveBuffer returns the credential value for the given
	// name in protected memory. For the manual strategy, this blocks until
	// the credential is submitted or the context is cancelled. The caller
	// owns the returned buffer and MUST Destroy it on every path; a transport
	// copies the value out only into its secret-bearing wire DTO, the single
	// boundary where bytes leave protected memory.
	CredentialRetrieveBuffer(ctx context.Context, name string) (*secure.Buffer, error)

	// CredentialSubmit submits a credential for the manual strategy,
	// notifying any blocked retrievers. For the barrier strategy, this
	// delegates to CredentialStore.
	CredentialSubmit(ctx context.Context, name string, value []byte) error

	// CredentialDelete removes the credential with the given name and
	// all of its non-current historical versions.
	CredentialDelete(ctx context.Context, name string) error

	// CredentialStrategy returns the strategy of the caller's store.
	CredentialStrategy(ctx context.Context) (string, error)

	// CredentialList returns the names of every stored credential.
	// Values are never returned by enumeration. Returns
	// credentials.ErrEnumerationNotSupported when the active strategy
	// cannot enumerate (reserved for future HSM-only backends).
	CredentialList(ctx context.Context) ([]string, error)

	// CredentialRotate atomically replaces the current value of name
	// with opts.NewValue, returning the previous + new version numbers
	// and the grace expiry for the previous version. Pass GracePeriod
	// == 0 to wipe the previous value immediately.
	CredentialRotate(ctx context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error)

	// CredentialRetrieveVersionBuffer returns the stored credential value
	// for a specific version in protected memory. Use version == 0 to mean
	// "current version". The caller owns the returned buffer and MUST
	// Destroy it on every path.
	CredentialRetrieveVersionBuffer(ctx context.Context, name string, version uint64) (*secure.Buffer, error)

	// CredentialListVersions returns the version history (current + all
	// non-expired grace versions) for name. Each entry's Value field is
	// nil; use CredentialRetrieveVersionBuffer to read a specific value.
	CredentialListVersions(ctx context.Context, name string) ([]credentials.CredentialVersion, error)

	// CredentialGCExpired reaps any historical versions of name whose
	// grace period has elapsed. Returns the count reaped.
	CredentialGCExpired(ctx context.Context, name string) (int, error)

	// CredentialSetRotationPolicy stores name's scheduled-rotation policy.
	// An invalid policy, or any policy under the manual strategy, is
	// *credentials.InvalidRotationPolicyError; an unknown name is
	// credentials.ErrCredentialNotFound.
	CredentialSetRotationPolicy(ctx context.Context, name string, policy credentials.RotationPolicy) error

	// CredentialRotationPolicy returns name's rotation policy and when it
	// next falls due. An unknown name is credentials.ErrCredentialNotFound.
	CredentialRotationPolicy(ctx context.Context, name string) (*CredentialRotationPolicyStatus, error)

	// CredentialMigrationConflicts lists, sorted by name, the credentials
	// whose node-local copy in the old credentials/ layout differs from
	// the replicated record another node migrated first. Each entry names
	// the parts that differ, never a value.
	CredentialMigrationConflicts(ctx context.Context) ([]credentials.CredentialMigrationConflictError, error)

	// CredentialResolveMigrationConflict settles one conflict: import
	// rotates the node-local current value in as a new version of the
	// replicated record, discard keeps the replicated record as it is.
	// Either way the node-local records are deleted. A name with no
	// conflict is credentials.ErrMigrationConflictNotFound; any other
	// resolution is credentials.ErrInvalidMigrationResolution.
	CredentialResolveMigrationConflict(ctx context.Context, name string, how credentials.MigrationResolution) error
}

// CredentialRotationPolicyStatus is a credential's rotation policy and its
// schedule. NextRotation is zero when the policy is unset or disabled.
type CredentialRotationPolicyStatus struct {
	Policy       credentials.RotationPolicy
	NextRotation time.Time
}

// WireInterval is the policy's interval in its wire form, a Go duration
// string.
func (s *CredentialRotationPolicyStatus) WireInterval() string {
	return s.Policy.Interval.String()
}

// WireNextRotation is the next scheduled rotation in its wire form, an
// RFC 3339 time in UTC, or "" when none is scheduled.
func (s *CredentialRotationPolicyStatus) WireNextRotation() string {
	if s.NextRotation.IsZero() {
		return ""
	}
	return s.NextRotation.UTC().Format(time.RFC3339)
}

// CredentialRotationPolicyFromWire builds a rotation policy from its wire
// form, in which the interval is a Go duration string. An empty interval is
// zero, which only a disabled policy accepts. An interval that does not
// parse is refused with *credentials.InvalidRotationPolicyError, the error
// every transport reports as an invalid argument.
func CredentialRotationPolicyFromWire(enabled bool, interval string, retainVersions int) (credentials.RotationPolicy, error) {
	var d time.Duration
	if interval != "" {
		parsed, err := time.ParseDuration(interval)
		if err != nil {
			return credentials.RotationPolicy{}, &credentials.InvalidRotationPolicyError{Reason: "interval " + strconv.Quote(interval) + " is not a duration"}
		}
		d = parsed
	}
	return credentials.RotationPolicy{Enabled: enabled, Interval: d, RetainVersions: retainVersions}, nil
}

// CredentialTenantBarriers looks up a tenant's barrier. It is the seam through
// which the credential verbs reach a tenant's own named-credential store;
// *seal.BarrierRegistry satisfies it.
type CredentialTenantBarriers interface {
	GetTenant(tenantID string) (*seal.TenantBarrier, error)
	// ListTenants returns the ID of every tenant with a barrier of its
	// own, the stores the rotation scheduler visits.
	ListTenants() []string
}

// CredentialServiceOption configures a CredentialService.
type CredentialServiceOption func(*CredentialService)

// WithCredentialTenantBarriers binds the tenant barrier lookup the credential
// verbs resolve a tenant caller's store through. Without it a tenant caller is
// refused with a CapabilityUnsupportedError: it is never served from the
// node-wide store.
func WithCredentialTenantBarriers(tenants CredentialTenantBarriers) CredentialServiceOption {
	return func(s *CredentialService) {
		s.tenants = tenants
	}
}

// CredentialService implements CredentialServicer over one named-credential
// store per tenant. The system tenant's store is the node-wide
// credentials.CredentialService it was built with; every other tenant's store
// is derived from that tenant's barrier (credentials.CredentialService.ForBarrier),
// so it lives in the tenant's own data plane, encrypted under its barrier key.
// Every verb resolves the store from the caller's tenant (tenant.FromContext),
// which the authorization middleware binds to the authenticated identity.
type CredentialService struct {
	creds   *credentials.CredentialService
	tenants CredentialTenantBarriers
	homes   atomic.Pointer[homeSourceBox]
}

// Compile-time interface check.
var _ CredentialServicer = (*CredentialService)(nil)

// NewCredentialService creates a new CredentialServicer whose system-tenant
// store is c. Pass WithCredentialTenantBarriers to serve tenant callers from
// their own stores.
func NewCredentialService(c *credentials.CredentialService, opts ...CredentialServiceOption) CredentialServicer {
	s := &CredentialService{creds: c}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// store resolves the caller's named-credential store. The system tenant (and
// a call that carries no tenant) gets the node-wide store; any other tenant
// gets a store over its own barrier, built per call because an
// enable-encryption rekey can swap the tenant's barrier
// (StorageBarrier.ReopenKeyed) and a cached store would serve the stale one.
// A tenant with no dedicated barrier is refused with
// *CapabilityUnsupportedError and a sealed one with *BarrierSealedError;
// there is no fallback to the node-wide store.
func (s *CredentialService) store(ctx context.Context) (*credentials.CredentialService, error) {
	tenantID := tenant.FromContext(ctx)
	if tenantid.IsSystem(tenantID) {
		return s.creds, nil
	}
	if s.tenants == nil {
		return nil, &CapabilityUnsupportedError{
			Message: "credentials: tenant credential stores are not bound on this node",
		}
	}
	tb, err := s.tenants.GetTenant(tenantID)
	if errors.Is(err, seal.ErrTenantNotFound) {
		// A tenant on the shared system barrier has no barrier of its own
		// to keep a store under, and the node-wide store is the system
		// tenant's: refuse rather than share it.
		return nil, &CapabilityUnsupportedError{
			Message: "credentials: tenant " + strconv.Quote(tenantID) + " has no dedicated barrier to keep a credential store under",
		}
	}
	if err != nil {
		return nil, err
	}
	if tb.IsSealed() {
		return nil, &BarrierSealedError{TenantID: tenantID}
	}
	box := s.homes.Load()
	if box == nil {
		// The barrier is node-local: a store kept under it would hold a
		// different policy, value and claim on every node.
		return nil, &CapabilityUnsupportedError{
			Message: "credentials: tenant home stores are not attached on this node",
		}
	}
	kv, err := box.source.HomeStore(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	home, err := credentials.NewKVStore(kv)
	if err != nil {
		return nil, err
	}
	return s.creds.ForTenant(ctx, home, tb.Barrier())
}

// homeSourceBox lets an interface value sit behind an atomic.Pointer.
type homeSourceBox struct{ source tenant.HomeStoreSource }

// SetTenantHomes attaches the source of isolated tenants' home stores, where
// a tenant caller's named credentials live: on a cluster a Raft group of the
// tenant's own, so a policy, value or claim written through one node is the
// same record on every node. The source is attached after the service is
// built, because the stores that host the homes start later; until then, or
// after a nil src detaches it, a tenant caller is refused with a
// CapabilityUnsupportedError. It is safe to call concurrently with the
// credential verbs.
func (s *CredentialService) SetTenantHomes(src tenant.HomeStoreSource) {
	if src == nil {
		s.homes.Store(nil)
		return
	}
	s.homes.Store(&homeSourceBox{source: src})
}

// CredentialWireValue copies a retrieved credential out of protected memory
// into the plain []byte that a transport's secret-bearing response DTO
// carries, and destroys buf before it returns, on success and on failure.
//
// This is the single boundary where a credential leaves protected memory: a
// value written into a response is plaintext in encoding/json, net/http,
// protobuf and the transport's own buffers, none of which this repository can
// wipe, so the value is protected from the point this repository first owns it
// to this call and no further. A transport that encodes the DTO itself (REST,
// MCP) wipes the returned slice as soon as it has been written; one that hands
// the DTO to a framework which marshals it after the handler returns (gRPC,
// QUIC) cannot, and says so in its documentation
// (docs/credentials/architecture.md, "Protected-memory boundary").
func CredentialWireValue(buf *secure.Buffer) ([]byte, error) {
	defer buf.Destroy()
	return secure.CopyOut(buf)
}

// CredentialStore persists a credential under the given name in the caller's
// store.
func (s *CredentialService) CredentialStore(ctx context.Context, name string, value []byte) error {
	c, err := s.store(ctx)
	if err != nil {
		return err
	}
	return c.StoreCredential(ctx, name, value)
}

// CredentialRetrieveBuffer returns the credential value for the given name
// from the caller's store, in protected memory.
func (s *CredentialService) CredentialRetrieveBuffer(ctx context.Context, name string) (*secure.Buffer, error) {
	c, err := s.store(ctx)
	if err != nil {
		return nil, err
	}
	return c.RetrieveCredentialBuffer(ctx, name)
}

// CredentialSubmit submits a credential to the caller's store.
func (s *CredentialService) CredentialSubmit(ctx context.Context, name string, value []byte) error {
	c, err := s.store(ctx)
	if err != nil {
		return err
	}
	return c.SubmitCredential(ctx, name, value)
}

// CredentialDelete removes a credential, and its historical versions, from
// the caller's store.
func (s *CredentialService) CredentialDelete(ctx context.Context, name string) error {
	c, err := s.store(ctx)
	if err != nil {
		return err
	}
	return c.DeleteCredential(ctx, name)
}

// CredentialStrategy returns the strategy of the caller's store: the node's
// configured strategy for the system tenant, the barrier strategy for any
// other tenant.
func (s *CredentialService) CredentialStrategy(ctx context.Context) (string, error) {
	c, err := s.store(ctx)
	if err != nil {
		return "", err
	}
	return c.Strategy(), nil
}

// CredentialList returns the names of every credential in the caller's store.
func (s *CredentialService) CredentialList(ctx context.Context) ([]string, error) {
	c, err := s.store(ctx)
	if err != nil {
		return nil, err
	}
	return c.ListCredentials(ctx)
}

// CredentialRotate atomically replaces the current value of name in the
// caller's store with opts.NewValue, returning version metadata + grace
// expiry.
func (s *CredentialService) CredentialRotate(ctx context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error) {
	c, err := s.store(ctx)
	if err != nil {
		return nil, err
	}
	return c.RotateCredential(ctx, name, opts)
}

// CredentialRetrieveVersionBuffer returns the stored credential value for a
// specific version (or the current version when version == 0) from the
// caller's store, in protected memory.
func (s *CredentialService) CredentialRetrieveVersionBuffer(ctx context.Context, name string, version uint64) (*secure.Buffer, error) {
	c, err := s.store(ctx)
	if err != nil {
		return nil, err
	}
	return c.RetrieveCredentialVersionBuffer(ctx, name, version)
}

// CredentialListVersions returns the version history (current + grace) for
// name in the caller's store. Each entry's Value field is nil.
func (s *CredentialService) CredentialListVersions(ctx context.Context, name string) ([]credentials.CredentialVersion, error) {
	c, err := s.store(ctx)
	if err != nil {
		return nil, err
	}
	return c.ListCredentialVersions(ctx, name)
}

// CredentialGCExpired reaps historical versions of name in the caller's store
// whose grace period has elapsed. Returns the count reaped.
func (s *CredentialService) CredentialGCExpired(ctx context.Context, name string) (int, error) {
	c, err := s.store(ctx)
	if err != nil {
		return 0, err
	}
	return c.GCExpiredVersions(ctx, name)
}

// CredentialSetRotationPolicy stores name's scheduled-rotation policy.
func (s *CredentialService) CredentialSetRotationPolicy(ctx context.Context, name string, policy credentials.RotationPolicy) error {
	c, err := s.store(ctx)
	if err != nil {
		return err
	}
	return c.SetRotationPolicy(ctx, name, policy)
}

// CredentialRotationPolicy returns name's rotation policy and its schedule.
func (s *CredentialService) CredentialRotationPolicy(ctx context.Context, name string) (*CredentialRotationPolicyStatus, error) {
	c, err := s.store(ctx)
	if err != nil {
		return nil, err
	}
	// One read of the metadata record: two separate reads could straddle a
	// concurrent SetRotationPolicy and report a status that never existed.
	policy, next, err := c.RotationStatus(ctx, name)
	if err != nil {
		return nil, err
	}
	return &CredentialRotationPolicyStatus{Policy: policy, NextRotation: next}, nil
}

// CredentialMigrationConflicts implements CredentialServicer.
func (s *CredentialService) CredentialMigrationConflicts(ctx context.Context) ([]credentials.CredentialMigrationConflictError, error) {
	c, err := s.store(ctx)
	if err != nil {
		return nil, err
	}
	return c.ListMigrationConflicts(ctx)
}

// CredentialResolveMigrationConflict implements CredentialServicer.
func (s *CredentialService) CredentialResolveMigrationConflict(ctx context.Context, name string, how credentials.MigrationResolution) error {
	c, err := s.store(ctx)
	if err != nil {
		return err
	}
	return c.ResolveMigrationConflict(ctx, name, how)
}

// CredentialRotateDue carries out every rotation policy due at now, in the
// system store and in the store of every tenant whose barrier is unsealed,
// each under that tenant's context, and returns how many credentials it
// rotated. A sealed tenant is skipped; its policies fall due at the first
// check after it is unsealed. A tenant whose rotations fail is reported as a
// *CredentialTenantRotationError and the sweep goes on. It is not a transport
// verb: the server's rotation scheduler calls it on its own period.
func (s *CredentialService) CredentialRotateDue(ctx context.Context, now time.Time) (int, error) {
	rotated, err := s.creds.RotateDue(ctx, now)
	if s.tenants == nil {
		return rotated, err
	}
	errs := []error{err}
	ids := s.tenants.ListTenants()
	slices.Sort(ids)
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		if tenantid.IsSystem(id) {
			continue
		}
		tctx := tenant.WithTenantID(ctx, id)
		c, err := s.store(tctx)
		var sealed *BarrierSealedError
		var unbound *CapabilityUnsupportedError
		if errors.As(err, &sealed) || errors.As(err, &unbound) {
			// A sealed barrier's store cannot be read until it is
			// unsealed, and its policies fall due at the first check
			// after that; a tenant removed since the listing has none.
			continue
		}
		if err == nil {
			var n int
			n, err = c.RotateDue(tctx, now)
			rotated += n
		}
		if err != nil {
			errs = append(errs, &CredentialTenantRotationError{TenantID: id, Cause: err})
		}
	}
	return rotated, errors.Join(errs...)
}

// CredentialTenantRotationError reports the scheduled rotations of one
// tenant's credential store that CredentialRotateDue could not carry out.
type CredentialTenantRotationError struct {
	TenantID string
	Cause    error
}

func (e *CredentialTenantRotationError) Error() string {
	return "credentials: scheduled rotation for tenant " + strconv.Quote(e.TenantID) + ": " + e.Cause.Error()
}

func (e *CredentialTenantRotationError) Unwrap() error { return e.Cause }
