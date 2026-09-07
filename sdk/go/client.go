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
	"reflect"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Client is the top-level QRDB SDK client. It wraps a transport and
// exposes typed KV and Admin service interfaces. The transport determines
// the communication mechanism (REST, gRPC, embedded, etc.).
type Client struct {
	transport transport.Transport
	closed    atomic.Bool
}

// NewClient creates a new SDK client using the given transport.
// The transport must already be constructed; use the appropriate
// transport package (rest, embedded, grpc) to create one.
// Returns an error if transport is nil.
func NewClient(t transport.Transport) (*Client, error) {
	if t == nil {
		return nil, &NilTransportError{}
	}
	return &Client{
		transport: t,
	}, nil
}

// Connect establishes the transport connection. For network transports
// this performs the initial handshake; for embedded transports this is
// a no-op.
func (c *Client) Connect(ctx context.Context) error {
	if c.closed.Load() {
		return &ClientClosedError{}
	}
	return c.transport.Connect(ctx)
}

// capability returns c's transport as T.
//
// Every accessor below asserts that the transport implements the capability
// interface it projects, and a transport that does not is a construction-time
// programming error -- NewClient takes a transport.Transport, a four-method
// interface, so nothing stops a caller passing one that implements no
// capability at all. This helper exists so that assertion is written once
// instead of ~25 times, and so the panic names the client method the caller
// actually called rather than only the interface it failed to satisfy.
//
// It PANICS, preserving exactly what the bare assertions it replaced did.
// Returning the zero value instead would convert an immediate, located failure
// into a nil-interface dereference somewhere later; UserAuthFactors is the one
// accessor that deliberately returns nil, and it documents that contract and
// asks callers to nil-check. Which of the two shapes the SDK should standardise
// on is an API question, filed separately -- this helper does not decide it.
func capability[T any](c *Client, method string) T {
	v, ok := any(c.transport).(T)
	if !ok {
		var want T
		panic("sdk: Client." + method + ": transport does not implement " +
			reflect.TypeOf(&want).Elem().String())
	}
	return v
}

// KV returns a KVClient for data-plane key-value operations.
func (c *Client) KV() transport.KVClient {
	return capability[transport.KVClient](c, "KV")
}

// Admin returns an AdminClient for control-plane cluster operations.
func (c *Client) Admin() transport.AdminClient {
	return capability[transport.AdminClient](c, "Admin")
}

// MVCC returns an MVCCClient for MVCC-aware key-value operations with
// revision tracking, lease attachment, and range queries.
func (c *Client) MVCC() transport.MVCCClient {
	return capability[transport.MVCCClient](c, "MVCC")
}

// Transaction returns a TransactionClient for atomic compare-and-swap
// transaction execution.
func (c *Client) Transaction() transport.TransactionClient {
	return capability[transport.TransactionClient](c, "Transaction")
}

// Batch returns a BatchClient for atomic batch operation execution.
func (c *Client) Batch() transport.BatchClient {
	return capability[transport.BatchClient](c, "Batch")
}

// Watch returns a WatchClient for event streaming on key changes.
func (c *Client) Watch() transport.WatchClient {
	return capability[transport.WatchClient](c, "Watch")
}

// Lease returns a LeaseClient for distributed lease management.
func (c *Client) Lease() transport.LeaseClient {
	return capability[transport.LeaseClient](c, "Lease")
}

// Lock returns a LockClient for distributed lock management with
// fencing tokens for safe distributed coordination.
func (c *Client) Lock() transport.LockClient {
	return capability[transport.LockClient](c, "Lock")
}

// Index returns an IndexClient for secondary index operations.
func (c *Client) Index() transport.IndexClient {
	return capability[transport.IndexClient](c, "Index")
}

// KVStore returns a kvstore.KVStore backed by the client's transport.
// This enables DAOs to operate transparently over any transport protocol
// (REST, gRPC, MCP, Unix, embedded). The returned store delegates KV
// operations to the transport's KVClient and index operations to its
// IndexClient.
func (c *Client) KVStore() KVStore {
	return &kvStoreAdapter{
		kv:    c.KV(),
		index: c.Index(),
	}
}

// TimeSeries returns a TimeSeriesClient for time-series data ingestion
// and querying.
func (c *Client) TimeSeries() transport.TimeSeriesClient {
	return capability[transport.TimeSeriesClient](c, "TimeSeries")
}

// Vector returns a VectorClient for vector database operations including
// namespace management, vector CRUD, similarity search, and index maintenance.
func (c *Client) Vector() transport.VectorClient {
	return capability[transport.VectorClient](c, "Vector")
}

// Users returns a UserClient for user management operations.
func (c *Client) Users() transport.UserClient {
	return capability[transport.UserClient](c, "Users")
}

// Roles returns a RoleClient for role management operations.
func (c *Client) Roles() transport.RoleClient {
	return capability[transport.RoleClient](c, "Roles")
}

// Certs returns a CertClient for certificate management operations.
func (c *Client) Certs() transport.CertClient {
	return capability[transport.CertClient](c, "Certs")
}

// Database returns a DatabaseClient for database management operations
// including creation, inspection, listing, and deletion.
func (c *Client) Database() transport.DatabaseClient {
	return capability[transport.DatabaseClient](c, "Database")
}

// DEK returns a DEKClient for data encryption key management operations.
func (c *Client) DEK() transport.DEKClient {
	return capability[transport.DEKClient](c, "DEK")
}

// Barrier returns a BarrierClient for cryptographic barrier operations.
func (c *Client) Barrier() transport.BarrierClient {
	return capability[transport.BarrierClient](c, "Barrier")
}

// Ceremony returns a CeremonyClient for initialization ceremony operations
// including M-of-N officer enrollment and share distribution.
func (c *Client) Ceremony() transport.CeremonyClient {
	return capability[transport.CeremonyClient](c, "Ceremony")
}

// Credentials returns a CredentialClient for named credential management.
func (c *Client) Credentials() transport.CredentialClient {
	return capability[transport.CredentialClient](c, "Credentials")
}

// Collections returns a CollectionClient for document collection
// management operations.
func (c *Client) Collections() transport.CollectionClient {
	return capability[transport.CollectionClient](c, "Collections")
}

// SealStore returns a SealStoreClient for managing platform-local
// sealed credentials.
func (c *Client) SealStore() transport.SealStoreClient {
	return capability[transport.SealStoreClient](c, "SealStore")
}

// AuthFactors returns an AuthFactorClient for custodian auth factor enrollment,
// challenge issuance, verification, listing, and revocation. This is the
// ceremony-time enrolment surface used by custodian unseal flows.
//
// For the user-side N-factor model (User -> []AuthFactor) per
// docs/08-bootstrap-auth-architecture.md §5e, use UserAuthFactors() instead.
func (c *Client) AuthFactors() transport.AuthFactorClient {
	return capability[transport.AuthFactorClient](c, "AuthFactors")
}

// UserAuthFactors returns the user-side N-factor client (M14 interface) per
// docs/08-bootstrap-auth-architecture.md §5e. It is distinct from the
// ceremony-time AuthFactors() accessor: the user-side surface models a
// 1-to-many relationship between Users and AuthFactors and exposes Add /
// List / Get / Remove / Rotate / Relabel / Login. Hardware verification
// (FIDO2 / PIV / mTLS) is performed by the caller; this RPC only resolves
// (Type, LookupKey) tuples to a User.
//
// Returns nil when the underlying transport does not implement the
// UserAuthFactorClient interface (e.g., the embedded transport which
// currently only exposes the legacy AuthFactorClient surface). Callers
// SHOULD nil-check the result and fall back appropriately.
func (c *Client) UserAuthFactors() transport.UserAuthFactorClient {
	if uac, ok := c.transport.(transport.UserAuthFactorClient); ok {
		return uac
	}
	return nil
}

// CustodianGroups returns a CustodianGroupClient for managing custodian
// groups used in Shamir secret sharing.
func (c *Client) CustodianGroups() transport.CustodianGroupClient {
	return capability[transport.CustodianGroupClient](c, "CustodianGroups")
}

// Shares returns a ShareClient for submitting and tracking Shamir shares.
func (c *Client) Shares() transport.ShareClient {
	return capability[transport.ShareClient](c, "Shares")
}

// Tenants returns a TenantClient for multi-tenant operations and
// per-tenant barrier management.
func (c *Client) Tenants() transport.TenantClient {
	return capability[transport.TenantClient](c, "Tenants")
}

// WithTenant returns a KVClient that automatically prefixes all keys
// with the given tenant ID for multi-tenant isolation.
func (c *Client) WithTenant(tenantID string) (transport.KVClient, error) {
	if c.closed.Load() {
		return nil, &ClientClosedError{}
	}
	return transport.WithTenant(c.KV(), tenantID)
}

// DatabaseScoped returns a client whose data-plane operations target the named
// database within the authenticated tenant's keyspace. The returned client
// shares the underlying connection but carries its own immutable database
// scope, so scopes never leak across clients and no global mutable state is
// involved. Scoping to the default ("system") database or the empty string
// yields an unscoped client.
//
// When the active transport does not support per-database scoping (e.g. the
// embedded transport, which operates on a single in-process database), the
// receiver is returned unchanged.
func (c *Client) DatabaseScoped(name string) *Client {
	if c.closed.Load() {
		return c
	}
	scoper, ok := c.transport.(transport.DatabaseScoper)
	if !ok {
		return c
	}
	return &Client{transport: scoper.WithDatabase(name)}
}

// Permissions returns a PermissionClient exposing the read-only
// permission registry. The transport must implement
// transport.PermissionProvider; otherwise the returned client is nil
// and callers should fall back appropriately.
func (c *Client) Permissions() transport.PermissionClient {
	if c.closed.Load() {
		return nil
	}
	if p, ok := c.transport.(transport.PermissionProvider); ok {
		return p.PermissionClient()
	}
	return nil
}

// Audit returns an AuditClient that can emit and query compliance-grade
// audit events. The transport must implement transport.AuditProvider and
// have an AuditLogger configured; otherwise the returned client is nil and
// callers should fall back appropriately.
func (c *Client) Audit() transport.AuditClient {
	if c.closed.Load() {
		return nil
	}
	if p, ok := c.transport.(transport.AuditProvider); ok {
		return p.AuditClient()
	}
	return nil
}

// RBAC returns an RBACClient that exposes the qrdb auth.Authorizer.
// Honours the Phase 0 system-tenant rule transitively — the underlying
// authorizer performs all gating.
func (c *Client) RBAC() transport.RBACClient {
	if c.closed.Load() {
		return nil
	}
	if p, ok := c.transport.(transport.RBACProvider); ok {
		return p.RBACClient()
	}
	return nil
}

// Factors returns a FactorClient for managing user authentication
// factors (FIDO2 / PIV / mTLS) and verifying signed challenges.
func (c *Client) Factors() transport.FactorClient {
	if c.closed.Load() {
		return nil
	}
	if p, ok := c.transport.(transport.FactorProvider); ok {
		return p.FactorClient()
	}
	return nil
}

// Login returns a LoginClient that wraps qrdb's login resolver and
// session issuer. BeginLogin / FinishLogin perform the challenge /
// signature exchange; ResolveIdentity validates an existing bearer.
func (c *Client) Login() transport.LoginClient {
	if c.closed.Load() {
		return nil
	}
	if p, ok := c.transport.(transport.LoginProvider); ok {
		return p.LoginClient()
	}
	return nil
}

// Sessions returns a SessionClient for resolving and revoking bearer
// tokens issued by Login.FinishLogin. Issuance lives on LoginClient;
// validation / invalidation lives here.
func (c *Client) Sessions() transport.SessionClient {
	if c.closed.Load() {
		return nil
	}
	if p, ok := c.transport.(transport.SessionProvider); ok {
		return p.SessionClient()
	}
	return nil
}

// VolumePools returns a VolumePoolClient that wraps the configured
// objstore pool.Manager. List / Pick are read operations; RegisterPool
// is admin-gated upstream by the volumepool:create permission.
func (c *Client) VolumePools() transport.VolumePoolClient {
	if c.closed.Load() {
		return nil
	}
	if p, ok := c.transport.(transport.VolumePoolProvider); ok {
		return p.VolumePoolClient()
	}
	return nil
}

// Healthy returns true if the transport connection is healthy.
func (c *Client) Healthy(ctx context.Context) bool {
	if c.closed.Load() {
		return false
	}
	return c.transport.Healthy(ctx)
}

// Close shuts down the client and releases all resources held by the
// underlying transport. After Close returns, all operations will return
// ClientClosedError.
func (c *Client) Close() error {
	if c.closed.Swap(true) {
		return nil // already closed
	}
	return c.transport.Close()
}
