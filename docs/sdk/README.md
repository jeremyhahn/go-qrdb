<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Go SDK

The `sdk/go/` module provides a unified Go client library for interacting with a qrdbd server across all supported transports.

## Client Interface

```go
type Client interface {
    KV() KVClient
    Admin() AdminClient
    MVCC() MVCCClient
    Tx() TransactionClient
    Watch() WatchClient
    Lease() LeaseClient
    Batch() BatchClient
    Lock() LockClient
    Index() IndexClient
    TimeSeries() TimeSeriesClient
    Barrier() BarrierClient
    Vector() VectorClient        // nil when capability disabled
    Users() UserClient
    Roles() RoleClient
    Certs() CertClient
    WithTenant(tenantID string) TenantClient
    Close() error
}
```

## Transports

| Transport | Package | Protocol | Use Case |
|-----------|---------|----------|----------|
| REST | `transport/rest/` | HTTPS (HTTP/2) | General purpose, browser-friendly |
| gRPC | `transport/grpc/` | gRPC with TLS | High-performance, streaming |
| Unix | `transport/unix/` | gRPC over UDS | Local access, no network overhead |
| MCP | `transport/mcp/` | MCP over HTTP | AI/LLM tool integration |
| Embedded | `transport/embedded/` | In-process | Library mode, no network |

All transports enforce TLS 1.3 (except Unix which is inherently local and Embedded which is in-process).

## Quick Start

```go
import sdk "github.com/jeremyhahn/go-qrdb/sdk/go"

// Connect via gRPC
client, err := sdk.NewClient(
    sdk.WithTransport("grpc"),
    sdk.WithAddress("localhost:63003"),
    sdk.WithCAFile("/etc/qrdb/ca.pem"),
)
defer client.Close()

// KV operations
err = client.KV().Put(ctx, "mykey", []byte("myvalue"))
data, err := client.KV().Get(ctx, "mykey")
```

## Tenant Isolation

```go
tenant := client.WithTenant("tenant-a")
// All operations auto-prefixed with "tenant-a/"
err = tenant.KV().Put(ctx, "key", []byte("value"))
// Server stores as "tenant-a/key"
```

## Database Protocol Management

The SDK supports managing per-database wire protocol configurations for
MySQL, PostgreSQL, and etcd. Use `client.Admin()` to update protocol
settings on existing databases.

```go
// Enable etcd protocol on a database
err = client.Admin().UpdateProtocol(ctx, "k8s", transport.UpdateProtocolRequest{
    Etcd: &transport.EtcdProtocolConfig{
        Enabled:       true,
        ListenAddr:    ":2379",
        AdvertiseURLs: []string{"https://node1:2379"},
    },
})

// Disable etcd protocol
err = client.Admin().UpdateProtocol(ctx, "k8s", transport.UpdateProtocolRequest{
    Etcd: &transport.EtcdProtocolConfig{
        Enabled: false,
    },
})
```

## TypeScript SDK

A parallel TypeScript SDK is available at `sdk/typescript/` (`@qrdb/sdk` npm package).
It provides the same client interface with two transport implementations:

- **WailsClient**: Desktop IPC via Wails-generated bindings
- **RestClient**: Browser/standalone via fetch() against REST API

See [TypeScript SDK documentation](../typescript-sdk/README.md) for details.

## Error Types & Interfaces (re-exports)

The SDK re-exports canonical error sentinels and the encryption migrator interface
for use by composing services. These are error types that appear in error responses
across all transports:

```go
import qrdbsdk "github.com/jeremyhahn/go-qrdb/sdk/go"

// Encryption-related errors
_ = qrdbsdk.ErrSharedBarrierImmutable       // Tenant on shared barrier cannot enable encryption
_ = qrdbsdk.ErrMigrationInProgress         // Enable-encryption already running
_ = qrdbsdk.ErrMigrationSourceNotNone      // Invalid source strategy
_ = qrdbsdk.ErrMigrationInvalidMechanism   // Bad migration mechanism
_ = qrdbsdk.ErrMigrationInvalidAvailability
_ = qrdbsdk.ErrMigrationInvalidTarget
_ = qrdbsdk.ErrMigrationProviderUnavailable

// Encryption migrator interface (for custom integrations)
var _ qrdbsdk.EncryptionMigrator

// Entitlement errors (quota/feature gating)
var _ qrdbsdk.EntitlementError

// Barrier scope for tenant-scoped enable-encryption (value type)
scope := qrdbsdk.BarrierScope{
    Kind:     qrdbsdk.ScopeTenant,
    TenantID: "acme",
}
```

## TLS Configuration

Certificate verification: configured CA file → OS trust store → reject.

```go
client, err := sdk.NewClient(
    sdk.WithCAFile("/etc/qrdb/ca.pem"),       // CA for verification
    sdk.WithCertFile("/etc/qrdb/client.pem"),  // Client cert (mTLS)
    sdk.WithKeyFile("/etc/qrdb/client-key.pem"),
)
```
