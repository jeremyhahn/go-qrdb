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
}

// CredentialTenantBarriers looks up a tenant's barrier. It is the seam through
// which the credential verbs reach a tenant's own named-credential store;
// *seal.BarrierRegistry satisfies it.
type CredentialTenantBarriers interface {
	GetTenant(tenantID string) (*seal.TenantBarrier, error)
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
// A tenant whose barrier is not registered is refused with the registry's
// error; there is no fallback to the node-wide store.
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
	if err != nil {
		return nil, err
	}
	return s.creds.ForBarrier(tb.Barrier())
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
