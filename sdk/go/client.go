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

// KV returns a KVClient for data-plane key-value operations.
func (c *Client) KV() transport.KVClient {
	return c.transport.(transport.KVClient)
}

// Admin returns an AdminClient for control-plane cluster operations.
func (c *Client) Admin() transport.AdminClient {
	return c.transport.(transport.AdminClient)
}

// MVCC returns an MVCCClient for MVCC-aware key-value operations with
// revision tracking, lease attachment, and range queries.
func (c *Client) MVCC() transport.MVCCClient {
	return c.transport.(transport.MVCCClient)
}

// Transaction returns a TransactionClient for atomic compare-and-swap
// transaction execution.
func (c *Client) Transaction() transport.TransactionClient {
	return c.transport.(transport.TransactionClient)
}

// Batch returns a BatchClient for atomic batch operation execution.
func (c *Client) Batch() transport.BatchClient {
	return c.transport.(transport.BatchClient)
}

// Watch returns a WatchClient for event streaming on key changes.
func (c *Client) Watch() transport.WatchClient {
	return c.transport.(transport.WatchClient)
}

// Lease returns a LeaseClient for distributed lease management.
func (c *Client) Lease() transport.LeaseClient {
	return c.transport.(transport.LeaseClient)
}

// Lock returns a LockClient for distributed lock management with
// fencing tokens for safe distributed coordination.
func (c *Client) Lock() transport.LockClient {
	return c.transport.(transport.LockClient)
}

// Index returns an IndexClient for secondary index operations.
func (c *Client) Index() transport.IndexClient {
	return c.transport.(transport.IndexClient)
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
	return c.transport.(transport.TimeSeriesClient)
}

// Vector returns a VectorClient for vector database operations including
// namespace management, vector CRUD, similarity search, and index maintenance.
func (c *Client) Vector() transport.VectorClient {
	return c.transport.(transport.VectorClient)
}

// Users returns a UserClient for user management operations.
func (c *Client) Users() transport.UserClient {
	return c.transport.(transport.UserClient)
}

// Roles returns a RoleClient for role management operations.
func (c *Client) Roles() transport.RoleClient {
	return c.transport.(transport.RoleClient)
}

// Certs returns a CertClient for certificate management operations.
func (c *Client) Certs() transport.CertClient {
	return c.transport.(transport.CertClient)
}

// Database returns a DatabaseClient for database management operations
// including creation, inspection, listing, and deletion.
func (c *Client) Database() transport.DatabaseClient {
	return c.transport.(transport.DatabaseClient)
}

// DEK returns a DEKClient for data encryption key management operations.
func (c *Client) DEK() transport.DEKClient {
	return c.transport.(transport.DEKClient)
}

// Barrier returns a BarrierClient for cryptographic barrier operations.
func (c *Client) Barrier() transport.BarrierClient {
	return c.transport.(transport.BarrierClient)
}

// Ceremony returns a CeremonyClient for initialization ceremony operations
// including M-of-N officer enrollment and share distribution.
func (c *Client) Ceremony() transport.CeremonyClient {
	return c.transport.(transport.CeremonyClient)
}

// Credentials returns a CredentialClient for named credential management.
func (c *Client) Credentials() transport.CredentialClient {
	return c.transport.(transport.CredentialClient)
}

// Collections returns a CollectionClient for document collection
// management operations.
func (c *Client) Collections() transport.CollectionClient {
	return c.transport.(transport.CollectionClient)
}

// SealStore returns a SealStoreClient for managing platform-local
// sealed credentials.
func (c *Client) SealStore() transport.SealStoreClient {
	return c.transport.(transport.SealStoreClient)
}

// CustodianGroups returns a CustodianGroupClient for managing custodian
// groups used in Shamir secret sharing.
func (c *Client) CustodianGroups() transport.CustodianGroupClient {
	return c.transport.(transport.CustodianGroupClient)
}

// Shares returns a ShareClient for submitting and tracking Shamir shares.
func (c *Client) Shares() transport.ShareClient {
	return c.transport.(transport.ShareClient)
}

// Tenants returns a TenantClient for multi-tenant operations and
// per-tenant barrier management.
func (c *Client) Tenants() transport.TenantClient {
	return c.transport.(transport.TenantClient)
}

// WithTenant returns a KVClient that automatically prefixes all keys
// with the given tenant ID for multi-tenant isolation.
func (c *Client) WithTenant(tenantID string) (transport.KVClient, error) {
	if c.closed.Load() {
		return nil, &ClientClosedError{}
	}
	return transport.WithTenant(c.KV(), tenantID)
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
