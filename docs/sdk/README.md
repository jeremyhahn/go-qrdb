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
    Index() IndexClient
    TimeSeries() TimeSeriesClient
    Barrier() BarrierClient
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

## TLS Configuration

Certificate verification: configured CA file → OS trust store → reject.

```go
client, err := sdk.NewClient(
    sdk.WithCAFile("/etc/qrdb/ca.pem"),       // CA for verification
    sdk.WithCertFile("/etc/qrdb/client.pem"),  // Client cert (mTLS)
    sdk.WithKeyFile("/etc/qrdb/client-key.pem"),
)
```
