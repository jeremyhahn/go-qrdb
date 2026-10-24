# Distributed Locking

> **Status: REFERENCE** · Scope: go-qrdb · Last verified: 2026-10-23
> Companion reference; the feature folders under `docs/` are the contract and win on any disagreement.

<!--
Copyright 2026 Jeremy Hahn

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

## Overview

The distributed lock service in QRDB provides safe coordination of access to shared resources across multiple processes or cluster nodes. Locks are useful for:

- **Leader election**: Selecting a single coordinator among many
- **Resource coordination**: Preventing concurrent access to critical resources
- **Mutual exclusion**: Protecting code sections from simultaneous execution
- **Distributed consensus**: Coordinating state changes across processes

The lock service (`pkg/lock`) is composed from QRDB's lease, transaction (compare-and-swap), watch and MVCC KV services; it has no consensus opcodes of its own.

Each lock carries a **fencing token** (an MVCC revision, monotonically increasing within the store that holds the lock), so a downstream service can detect a stale holder: a paused process that resumes after its lease has expired.

## Lock Model

### Lock Structure

A distributed lock is represented by the `Lock` struct:

```go
type Lock struct {
    // Key is the user-provided key without the internal prefix.
    Key string

    // FencingToken is the MVCC ModRevision at acquisition. It is
    // monotonically increasing and can be used to detect stale holders.
    FencingToken int64

    // LeaseID is the underlying lease ID that controls the TTL.
    LeaseID int64

    // HolderID is the unique identifier for the lock holder.
    HolderID string

    // Reentrant indicates whether re-acquisition by the same holder
    // is permitted.
    Reentrant bool

    // AcquireCount is the number of times a reentrant holder has
    // acquired the lock. Always 1 for non-reentrant locks.
    AcquireCount int32

    // TTL is the lease time-to-live in seconds.
    TTL int64

    // AcquiredAt is the Unix timestamp in seconds when the lock was
    // first acquired.
    AcquiredAt int64
}
```

### LockOptions

Lock acquisition is configured via `LockOptions`:

```go
type LockOptions struct {
    // Key is the resource to lock. Required.
    Key string

    // HolderID is the unique holder identifier. A UUID is generated
    // automatically when empty.
    HolderID string

    // TTL is the lease time-to-live. Required and must be positive.
    TTL time.Duration

    // Reentrant allows the same holder to re-acquire the lock,
    // incrementing AcquireCount on each acquisition.
    Reentrant bool
}
```

### System Namespace

Lock keys are stored under the `__sys:lock/` internal prefix. A KV list or scan whose prefix does not name the `__sys:` namespace filters them out, and an application key outside that namespace never collides with a lock record.

## Fencing Tokens

A fencing token is the MVCC revision of the transaction that created the lock record. `TryLock` writes it into the record so that it stays fixed while a reentrant holder re-acquires; a record without a stored token is checked against its current `ModRevision`. Because MVCC revisions increase monotonically, each new acquisition of a lock gets a higher fencing token than the previous one.

### Why Fencing Tokens Matter

Consider a distributed system where:
1. Process A acquires lock, gets fencing token 100
2. Process A is paused (e.g., garbage collection, slow network)
3. A's lease expires, the lock is released
4. Process B acquires the same lock, gets fencing token 101
5. Process A resumes and tries to release the lock

Without fencing tokens, Process A would release B's lock using stale knowledge. With fencing tokens, the service rejects A's operation because it supplies token 100 but the lock is at token 101.

### Validation in Downstream Services

A downstream service the lock protects keeps the highest fencing token it has accepted and rejects any request carrying a lower one. The lock service cannot do this check for it: a holder that paused past its TTL still holds its old token, and only the service receiving the write sees it arrive late.

```go
// Pseudocode for a downstream service guarding a resource.
func (s *Store) Write(token int64, data []byte) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    if token < s.highestToken {
        return &StaleTokenError{Token: token, Highest: s.highestToken}
    }
    s.highestToken = token
    return s.apply(data)
}
```

## Acquisition Semantics

### TryLock (Non-blocking)

`TryLock` attempts to acquire the lock immediately without blocking:

1. Grant a lease with the specified TTL, in whole seconds (a positive TTL under one second becomes one second)
2. Compare-and-Swap: Assert version == 0 (key does not exist) → Put the lock bound to the lease
3. On success: Store the transaction's revision in the record as the fencing token and return the acquired `Lock`
4. On failure: Read the current holder
   - If the holder ID matches and both the request and the held lock are reentrant: Increment `AcquireCount`, revoke the new lease, update the existing lock
   - Otherwise: Revoke the new lease and return `LockHeldError` immediately

The compare-and-swap makes acquisition atomic. When the transaction returns an error but a re-read finds the key bound to the lease this attempt granted, the write committed: `TryLock` returns the lock rather than revoking the lease.

### Lock (Blocking)

`Lock` acquires the distributed lock, blocking until the lock is available or the context is canceled:

1. Call `TryLock` → if successful, return immediately
2. On `LockHeldError`:
   - Watch the lock key
   - Wait for an `EventDelete` (the lock was released or its lease expired); other events are ignored
   - On a delete event, retry `TryLock`; on another `LockHeldError`, keep waiting
   - Loop until acquired or context canceled
   - If the watch closes, retry `TryLock` once and return its result
3. On any other error: Return immediately

Waiting is driven by watch events, not polling. Every waiter on the key sees the same delete event and retries; the compare-and-swap lets one succeed, and the rest return to waiting.

### Reentrant Locks

When `LockOptions.Reentrant` is true, the same holder can call `Lock` or `TryLock` multiple times. The holder is identified by `HolderID`, so a re-acquisition must pass the same `HolderID` as the first one; an empty `HolderID` gets a fresh UUID and counts as a different holder. Both the request and the held lock must be reentrant.

- Each re-acquisition increments `AcquireCount`
- The lock keeps its original lease and fencing token; the lease granted for the attempt is revoked
- The lock must be unlocked the same number of times to fully release

Example:

```go
// First acquisition
lock1, _ := locker.Lock(ctx, lock.LockOptions{
    Key:       "resource",
    HolderID:  "worker-1",
    TTL:       10 * time.Second,
    Reentrant: true,
})
// AcquireCount = 1

// Same holder re-acquires
lock2, _ := locker.Lock(ctx, lock.LockOptions{
    Key:       "resource",
    HolderID:  "worker-1",
    TTL:       10 * time.Second,
    Reentrant: true,
})
// AcquireCount = 2, lock2.FencingToken == lock1.FencingToken

// Must unlock twice
locker.Unlock(ctx, "resource", lock2.FencingToken)  // AcquireCount = 1
locker.Unlock(ctx, "resource", lock1.FencingToken)  // AcquireCount = 0, lock released
```

## Unlock

`Unlock` releases the lock identified by a key and fencing token:

1. Read the lock and validate the fencing token against the one stored at acquisition
   - Return `LockNotFoundError` if no lock is held on the key; `Unlock` reports a failed read of a lock record the same way
   - Return `NotLockHolderError` if the token does not match
2. For reentrant locks with `AcquireCount > 1`:
   - Decrement `AcquireCount`
   - Update the lock, maintain the lease
3. For final release (`AcquireCount == 1` or non-reentrant):
   - Compare-and-Swap delete: Assert `ModRevision` still matches → Delete the lock
   - Revoke the lease

The compare-and-swap delete catches a modification of the lock between the read and the delete, and returns `NotLockHolderError` for it.

## Extend

`Extend` refreshes the TTL of an existing lock without releasing it:

1. Read the current lock value and validate the fencing token
   - Return `LockNotFoundError` if the read fails, which includes no lock being held on the key
   - Return `NotLockHolderError` if the token does not match
2. Issue a lease keepalive on the underlying lease ID
3. Return the `Lock` with the TTL the keepalive reports

An operation that may outlast the TTL extends the lock periodically.

## Status and List

`Status` returns the lock held on a key, or `nil` with no error when there is none; a failed read also returns `nil`. `List` returns every lock whose key starts with a prefix; an empty prefix lists all locks in the caller's scope.

## Standalone vs Clustered

The semantics are identical in every run mode. In embedded and standalone mode the compare-and-swap and the lease-expiry sweep are local; clustered, both go through Raft. A caller does not change code between them.

A lock survives a restart in both modes, because its lease is durable: a lock held when the server stops is still held by the same holder afterwards. A standalone node on the file or objstore backend keeps no leases, so every lock verb there is refused with a typed capability error. See [distributed-locks/architecture.md](distributed-locks/architecture.md#standalone-vs-clustered).

## API

Every verb is served on REST, gRPC, the Unix socket, QUIC, MCP and the embedded SDK. [distributed-locks/api.md](distributed-locks/api.md) is the contract for each transport: paths and method names, request and response schemas, the `(tenant, database)` scope, and the status and error mapping.

REST paths are unversioned (`/lock/...`) and served on the REST port (`server.admin_port`, 63002 by default; gRPC is `server.grpc_port`, 63003). `ttl` is in seconds and `holder_id` is optional:

```bash
CURL="curl -s --cert client.pem --key client-key.pem --cacert ca.pem"

# Acquire, blocking until the lock is free
$CURL -X POST https://db.example.com:63002/lock/acquire \
  -H "Content-Type: application/json" \
  -d '{"key": "resource", "holder_id": "worker-1", "ttl": 30}'

# Try once; 409 when another holder has the lock
$CURL -X POST https://db.example.com:63002/lock/trylock \
  -H "Content-Type: application/json" \
  -d '{"key": "resource", "holder_id": "worker-1", "ttl": 30}'

# Extend, then release, with the fencing token from the acquire response
$CURL -X POST https://db.example.com:63002/lock/extend \
  -H "Content-Type: application/json" \
  -d '{"key": "resource", "fencing_token": 42}'
$CURL -X POST https://db.example.com:63002/lock/release \
  -H "Content-Type: application/json" \
  -d '{"key": "resource", "fencing_token": 42}'

# Status of one lock, and every lock under a prefix
$CURL 'https://db.example.com:63002/lock/status?key=resource'
$CURL 'https://db.example.com:63002/lock/list?prefix=resource'
```

## CLI Reference

`qrdb lock` has one verb per operation. [distributed-locks/cli.md](distributed-locks/cli.md) documents every flag, its default and the `(tenant, database)` scope.

```bash
qrdb lock acquire myresource --ttl 30            # blocks; --wait <seconds> bounds the wait
qrdb lock acquire leader --ttl 60 --holder node-1 --reentrant
qrdb lock trylock myresource --ttl 30            # fails at once when the lock is held
qrdb lock extend myresource --token 42
qrdb lock release myresource --token 42
qrdb lock status myresource
qrdb lock list --prefix resource-
```

## SDK Client Interface

### Go SDK

The Go SDK exposes locks through the `transport.LockClient` interface (`sdk/go/transport/client.go`), returned by `Client.Lock()`:

```go
type LockClient interface {
    LockAcquire(ctx context.Context, req *LockAcquireRequest) (*LockInfo, error)
    LockTryLock(ctx context.Context, req *LockAcquireRequest) (*LockInfo, error)
    LockRelease(ctx context.Context, req *LockReleaseRequest) error
    LockExtend(ctx context.Context, req *LockExtendRequest) (*LockInfo, error)
    LockStatus(ctx context.Context, key string) (*LockInfo, error)
    LockList(ctx context.Context, prefix string) (*LockListResponse, error)
}
```

`LockAcquireRequest` carries `Key`, `HolderID`, `TTL` (seconds) and `Reentrant`; `LockReleaseRequest` and `LockExtendRequest` carry `Key` and `FencingToken` (`sdk/go/transport/lock_types.go`).

### TypeScript SDK

The TypeScript SDK exposes the same operations through the `LockClient` interface (`sdk/typescript/src/interfaces.ts`), reached as `client.lock`:

```typescript
interface LockClient {
    acquire(req: LockAcquireRequest): Promise<LockInfo>
    tryLock(req: LockAcquireRequest): Promise<LockInfo>
    release(req: LockReleaseRequest): Promise<void>
    extend(req: LockExtendRequest): Promise<LockInfo>
    status(key: string): Promise<LockInfo | null>
    list(prefix?: string): Promise<LockListResponse>
}
```

## Transport Coverage

All transports implement the full lock API:

| Transport | Mechanism | File |
|-----------|-----------|------|
| Embedded | Direct service call | `sdk/go/transport/embedded/lock.go` |
| REST | HTTP JSON POST/GET | `sdk/go/transport/rest/transport_lock.go` |
| gRPC | Protobuf unary RPC | `sdk/go/transport/grpc/transport_lock.go` |
| QUIC | Protobuf over QUIC | `sdk/go/transport/quic/transport_lock.go` |
| Unix | gRPC over UDS | `sdk/go/transport/unix/transport_lock.go` |
| MCP | MCP tool call | `sdk/go/transport/mcp/transport_lock.go` |

## Usage Examples

### Simple Mutex

```go
import (
    "context"
    "log"

    sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
    "github.com/jeremyhahn/go-qrdb/sdk/go/transport"
    "github.com/jeremyhahn/go-qrdb/sdk/go/transport/grpc"
)

func main() {
    t, err := grpc.New(
        transport.WithAddress("db.example.com:63003"),
        transport.WithTLS("client.pem", "client-key.pem", "ca.pem"),
    )
    if err != nil {
        log.Fatal(err)
    }
    client, err := sdk.NewClient(t)
    if err != nil {
        log.Fatal(err)
    }
    defer client.Close()

    ctx := context.Background()
    l, err := client.Lock().LockAcquire(ctx, &transport.LockAcquireRequest{Key: "myresource", TTL: 30})
    if err != nil {
        log.Fatal(err)
    }
    defer client.Lock().LockRelease(ctx, &transport.LockReleaseRequest{Key: "myresource", FencingToken: l.FencingToken})

    // Critical section, protected by the lock.
}
```

### Leader Election

`LockAcquire` blocks until the lock is free, so a node waits for leadership by acquiring and keeps it by extending well inside the TTL:

```go
func runAsLeader(ctx context.Context, client *sdk.Client, nodeID string) error {
    l, err := client.Lock().LockAcquire(ctx, &transport.LockAcquireRequest{
        Key: "leader", HolderID: nodeID, TTL: 60,
    })
    if err != nil {
        return err
    }
    defer client.Lock().LockRelease(context.Background(), &transport.LockReleaseRequest{
        Key: "leader", FencingToken: l.FencingToken,
    })

    ticker := time.NewTicker(20 * time.Second)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-ticker.C:
            // An error here means leadership is lost: stop leader work.
            if _, err := client.Lock().LockExtend(ctx, &transport.LockExtendRequest{
                Key: "leader", FencingToken: l.FencingToken,
            }); err != nil {
                return err
            }
        }
    }
}
```

### Resource Coordination with Fencing Token Validation

```go
func executeWithLock(ctx context.Context, client *sdk.Client, query string) error {
    l, err := client.Lock().LockAcquire(ctx, &transport.LockAcquireRequest{Key: "db-write", TTL: 10})
    if err != nil {
        return err
    }
    defer client.Lock().LockRelease(ctx, &transport.LockReleaseRequest{Key: "db-write", FencingToken: l.FencingToken})

    // The downstream service rejects a token lower than the highest it has seen.
    return execute(ctx, query, l.FencingToken)
}
```

### TypeScript Example

```typescript
import { RestClient } from "@qrdb/sdk";

async function main() {
    const client = new RestClient("https://db.example.com:63002");

    const lock = await client.lock.acquire({ key: "myresource", ttl: 30 });
    try {
        await doImportantWork(lock.fencingToken);
    } finally {
        await client.lock.release({ key: "myresource", fencing_token: lock.fencingToken });
    }
}
```

## Error Handling

The lock service returns typed errors. [distributed-locks/api.md](distributed-locks/api.md#status-and-error-mapping) maps every failure on every transport.

| Error | HTTP | gRPC | Description |
|-------|------|------|-------------|
| `LockHeldError` | `409 Conflict` | `ABORTED` | `trylock` on a lock held by another holder |
| `NotLockHolderError` | `403 Forbidden` | `PERMISSION_DENIED` | Stale fencing token on unlock/extend |
| `LockNotFoundError` | `404 Not Found` | `NOT_FOUND` | Key not currently locked |
| `ErrEmptyKey` | `500 Internal Server Error` | `INTERNAL` | Lock key is empty (unclassified) |
| `ErrInvalidTTL` | `500 Internal Server Error` | `INTERNAL` | TTL is not positive (unclassified) |

The embedded transport returns these errors as the lock service produced them, re-exported as `sdk.LockHeldError`, `sdk.NotLockHolderError` and `sdk.LockNotFoundError`. A network transport returns a transport error carrying the status above.

```go
_, err := client.Lock().LockTryLock(ctx, &transport.LockAcquireRequest{Key: "resource", TTL: 30})

var held *sdk.LockHeldError
if errors.As(err, &held) {
    fmt.Printf("Lock held by %s\n", held.HolderID)
}

var notHolder *sdk.NotLockHolderError
if errors.As(err, &notHolder) {
    fmt.Printf("Stale token: expected %d, actual %d\n", notHolder.ExpectedToken, notHolder.ActualToken)
}

var notFound *sdk.LockNotFoundError
if errors.As(err, &notFound) {
    fmt.Println("Lock was released or never acquired")
}
```

## Best Practices

### TTL Sizing

Choose a TTL that balances false expiry and stale lock detection:

- **Too short** (e.g., 1s): False expiry when network/process latency exceeds TTL
- **Too long** (e.g., 3600s): Slow stale lock detection (full hour to recover)
- **Middle range** (10-60s): Recovery within a minute, with room for ordinary latency

The TTL is whole seconds on every transport. For a long-running operation, call `LockExtend` periodically rather than acquiring with a very long TTL.

### Fencing Token Validation

Pass the fencing token to every downstream service the lock protects, and have that service reject a token lower than the highest it has seen. A holder that paused past its TTL then carries a stale token, and the service refuses it:

```go
l, err := client.Lock().LockAcquire(ctx, &transport.LockAcquireRequest{Key: "resource", TTL: 30})
if err != nil {
    return err
}
defer client.Lock().LockRelease(ctx, &transport.LockReleaseRequest{Key: "resource", FencingToken: l.FencingToken})

// Bad: executeQuery(ctx, "UPDATE important_table")
// Good: the downstream service checks the token before it accepts the write.
return executeQuery(ctx, "UPDATE important_table", l.FencingToken)
```

### Avoiding Deadlocks

The lock service does no deadlock detection: acquire locks in a consistent order. Bound every blocking acquisition with a context deadline:

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
l, err := client.Lock().LockAcquire(ctx, &transport.LockAcquireRequest{Key: "resource", TTL: 30})
if err != nil {
    return err // not acquired within 5 seconds, or refused
}
```

### Reentrant Locks

Use reentrant locks sparingly. They suit a single component that calls itself recursively, but can obscure lock dependencies. Every acquisition must name the same `HolderID`:

```go
func (t *Tree) Insert(ctx context.Context, value int) error {
    l, err := t.locker.Lock(ctx, lock.LockOptions{
        Key:       "tree",
        HolderID:  t.holderID,
        TTL:       30 * time.Second,
        Reentrant: true,
    })
    if err != nil {
        return err
    }
    defer t.locker.Unlock(ctx, "tree", l.FencingToken)

    // Safe to call Insert recursively.
    return t.insertNode(ctx, value)
}
```

### Cleanup on Context Cancellation

Release with a context that the work's cancellation does not cancel:

```go
l, err := client.Lock().LockAcquire(ctx, &transport.LockAcquireRequest{Key: "resource", TTL: 30})
if err != nil {
    return err
}
defer func() {
    _ = client.Lock().LockRelease(context.WithoutCancel(ctx), &transport.LockReleaseRequest{
        Key: "resource", FencingToken: l.FencingToken,
    })
}()

return doWork(ctx)
```

## See Also

- [leases/engineering.md](leases/engineering.md) -- Lease management and TTL enforcement
- [transactions/engineering.md](transactions/engineering.md) -- Compare-and-swap transactions
- [05-consistency.md](05-consistency.md) -- Consistency levels and routing
