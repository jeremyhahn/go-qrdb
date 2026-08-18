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

# Distributed Locking

## Overview

The distributed lock service in QRDB provides safe coordination of access to shared resources across multiple processes or cluster nodes. Locks are useful for:

- **Leader election**: Selecting a single coordinator among many
- **Resource coordination**: Preventing concurrent access to critical resources
- **Mutual exclusion**: Protecting code sections from simultaneous execution
- **Distributed consensus**: Coordinating state changes across processes

The lock service is built on top of QRDB's existing infrastructure -- leases, compare-and-swap transactions, watches, and MVCC versioning. No new consensus opcodes are required; all operations reuse the existing transaction, lease, watch, and KV services.

Unlike semaphores, QRDB locks include **fencing tokens** (MVCC revision-based, monotonically increasing) to detect stale holders and prevent split-brain scenarios where a paused process resumes after its lease has expired.

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

Lock keys are stored under the `__sys:lock/` internal prefix and are not visible in ordinary KV list operations. The prefix guarantees that application keys never conflict with lock storage.

## Fencing Tokens

A fencing token is the MVCC `ModRevision` assigned to the lock at acquisition time. Because MVCC revisions are globally ordered and monotonically increasing, each new acquisition of a lock gets a higher fencing token than the previous one.

### Why Fencing Tokens Matter

Consider a distributed system where:
1. Process A acquires lock, gets fencing token 100
2. Process A is paused (e.g., garbage collection, slow network)
3. A's lease expires, the lock is released
4. Process B acquires the same lock, gets fencing token 101
5. Process A resumes and tries to release the lock

Without fencing tokens, Process A would release B's lock using stale knowledge. With fencing tokens, the service rejects A's operation because it supplies token 100 but the lock is at token 101.

### Validation in Downstream Services

Downstream services should reject operations with stale fencing tokens:

```go
// Pseudocode for a critical operation
func criticalOperation(key string, token int64) error {
    // Validate token matches current lock state
    lock, err := lockService.Status(ctx, key)
    if err != nil {
        return err
    }
    if lock.FencingToken != token {
        return fmt.Errorf("stale fencing token: expected %d, got %d", lock.FencingToken, token)
    }
    // Proceed with critical operation
    return performWork()
}
```

## Acquisition Semantics

### TryLock (Non-blocking)

`TryLock` attempts to acquire the lock immediately without blocking:

1. Grant a lease with the specified TTL
2. Compare-and-Swap: Assert version == 0 (key does not exist) → Put the lock
3. On success: Return the acquired `Lock`
4. On failure: Check if the lock is held by the same holder (reentrant case)
   - If reentrant and same holder: Increment AcquireCount, revoke the new lease, update the existing lock
   - If held by a different owner: Return `LockHeldError` immediately

The compare-and-swap ensures the operation is atomic and recoverable on crash/failure.

### Lock (Blocking)

`Lock` acquires the distributed lock, blocking until the lock is available or the context is canceled:

1. Call `TryLock` → if successful, return immediately
2. On `LockHeldError`:
   - Watch the lock key for delete/modification events
   - Wait for `EventDelete` (indicates the lock was released)
   - On event, retry `TryLock`
   - Loop until acquired or context canceled
3. On any other error: Return immediately

The watch-based approach is event-driven with no polling overhead. Concurrent waiters are woken by a single delete event and race for the lock, with only one succeeding.

### Reentrant Locks

When `LockOptions.Reentrant` is true, the same holder can call `Lock` or `TryLock` multiple times:

- Each re-acquisition increments `AcquireCount`
- The underlying lease is reused (no new lease is granted)
- The lock must be unlocked the same number of times to fully release

Example:

```go
// First acquisition
lock1, _ := locker.Lock(ctx, LockOptions{
    Key: "resource",
    TTL: 10*time.Second,
    Reentrant: true,
})
// AcquireCount = 1

// Same holder re-acquires
lock2, _ := locker.Lock(ctx, LockOptions{
    Key: "resource",
    TTL: 10*time.Second,
    Reentrant: true,
})
// AcquireCount = 2

// Must unlock twice
locker.Unlock(ctx, "resource", lock2.FencingToken)  // AcquireCount = 1
locker.Unlock(ctx, "resource", lock1.FencingToken)  // AcquireCount = 0, lock released
```

## Unlock

`Unlock` releases the lock identified by a key and fencing token:

1. Validate the fencing token matches the current `ModRevision`
   - Return `NotLockHolderError` if token is stale
2. For reentrant locks with `AcquireCount > 1`:
   - Decrement `AcquireCount`
   - Update the lock, maintain the lease
3. For final release (`AcquireCount == 1` or non-reentrant):
   - Compare-and-Swap delete: Assert `ModRevision` still matches → Delete the lock
   - Revoke the lease to clean up attached resources

The compare-and-swap delete catches races where another process modified the lock between the read and delete.

## Extend

`Extend` refreshes the TTL of an existing lock without releasing it:

1. Read the current lock value and validate the fencing token
   - Return `NotLockHolderError` if token is stale
2. Issue a lease keepalive on the underlying lease ID
3. Return the updated `Lock` with refreshed TTL

This is useful for long-running operations that may exceed the original TTL.

## Standalone vs Clustered

| Aspect | Standalone | Clustered |
|--------|-----------|-----------|
| **Scope** | Single process | All cluster nodes |
| **Fault tolerance** | None (lock lost on crash) | Survives minority node failures |
| **Use case** | Coordinating goroutines, preventing concurrent CLI operations | True distributed coordination, leader election |
| **Fencing token validity** | Valid for the lifetime of the process | Valid across cluster and persistent restarts |
| **Consensus required** | No | Yes (all lock operations go through Raft) |

In **standalone mode**, locks are stored in an in-memory or file-backed engine without Raft replication. Fencing tokens are based on local MVCC revisions.

In **clustered mode**, locks are replicated through Raft consensus. All acquire, unlock, and extend operations must obtain Raft consensus, ensuring that all cluster nodes agree on lock state. Fencing tokens are based on the global MVCC revision and remain valid across node failures and restarts.

## REST API

The REST API exposes lock operations as JSON endpoints:

### POST /v1/lock/acquire

Blocking lock acquisition.

**Request:**
```json
{
  "key": "resource",
  "holder_id": "process-123",
  "ttl": 30,
  "reentrant": false
}
```

**Response** `200 OK`:
```json
{
  "key": "resource",
  "fencing_token": 42,
  "lease_id": 10,
  "holder_id": "process-123",
  "reentrant": false,
  "acquire_count": 1,
  "ttl": 30,
  "acquired_at": 1704067200
}
```

**Error** `409 Conflict` (already held):
```json
{
  "code": "lock_held",
  "message": "lock: held by other-process on key resource"
}
```

**Error** `400 Bad Request`:
```json
{
  "code": "invalid_ttl",
  "message": "lock: TTL must be positive"
}
```

**Example:**
```bash
curl -sk -X POST https://localhost:63002/v1/lock/acquire \
  -H "Content-Type: application/json" \
  -d '{
    "key": "resource",
    "ttl": 30,
    "reentrant": true
  }' | jq .
```

### POST /v1/lock/trylock

Non-blocking lock attempt.

**Request:**
```json
{
  "key": "resource",
  "holder_id": "process-123",
  "ttl": 30,
  "reentrant": false
}
```

**Response** `200 OK` (acquired):
```json
{
  "key": "resource",
  "fencing_token": 42,
  "lease_id": 10,
  "holder_id": "process-123",
  "reentrant": false,
  "acquire_count": 1,
  "ttl": 30,
  "acquired_at": 1704067200
}
```

**Error** `409 Conflict` (held by another):
```json
{
  "code": "lock_held",
  "message": "lock: held by other-process on key resource"
}
```

**Example:**
```bash
curl -sk -X POST https://localhost:63002/v1/lock/trylock \
  -H "Content-Type: application/json" \
  -d '{
    "key": "resource",
    "ttl": 30
  }' | jq .
```

### POST /v1/lock/release

Release a held lock.

**Request:**
```json
{
  "key": "resource",
  "fencing_token": 42
}
```

**Response** `204 No Content` (success, no body)

**Error** `403 Forbidden` (stale token):
```json
{
  "code": "not_lock_holder",
  "message": "lock: caller is not the lock holder for key resource (expected token 42, actual 43)"
}
```

**Error** `404 Not Found` (lock does not exist):
```json
{
  "code": "lock_not_found",
  "message": "lock: not found for key resource"
}
```

**Example:**
```bash
curl -sk -X POST https://localhost:63002/v1/lock/release \
  -H "Content-Type: application/json" \
  -d '{
    "key": "resource",
    "fencing_token": 42
  }'
```

### POST /v1/lock/extend

Extend lock TTL.

**Request:**
```json
{
  "key": "resource",
  "fencing_token": 42
}
```

**Response** `200 OK`:
```json
{
  "key": "resource",
  "fencing_token": 42,
  "lease_id": 10,
  "holder_id": "process-123",
  "reentrant": false,
  "acquire_count": 1,
  "ttl": 30,
  "acquired_at": 1704067200
}
```

**Error** `403 Forbidden` (stale token):
```json
{
  "code": "not_lock_holder",
  "message": "lock: caller is not the lock holder for key resource"
}
```

**Error** `404 Not Found`:
```json
{
  "code": "lock_not_found",
  "message": "lock: not found for key resource"
}
```

**Example:**
```bash
curl -sk -X POST https://localhost:63002/v1/lock/extend \
  -H "Content-Type: application/json" \
  -d '{
    "key": "resource",
    "fencing_token": 42
  }' | jq .
```

### GET /v1/lock/status

Query current lock state.

**Query Parameters:**
- `key` (required): The lock key

**Response** `200 OK` (lock exists):
```json
{
  "key": "resource",
  "fencing_token": 42,
  "lease_id": 10,
  "holder_id": "process-123",
  "reentrant": false,
  "acquire_count": 1,
  "ttl": 30,
  "acquired_at": 1704067200
}
```

**Response** `404 Not Found` (lock does not exist):
```json
{
  "code": "lock_not_found",
  "message": "lock: not found for key resource"
}
```

**Example:**
```bash
curl -sk 'https://localhost:63002/v1/lock/status?key=resource' | jq .
```

### GET /v1/lock/list

List active locks.

**Query Parameters:**
- `prefix` (optional): Filter locks by key prefix (empty returns all)

**Response** `200 OK`:
```json
{
  "locks": [
    {
      "key": "resource-a",
      "fencing_token": 40,
      "lease_id": 8,
      "holder_id": "process-123",
      "reentrant": false,
      "acquire_count": 1,
      "ttl": 30,
      "acquired_at": 1704067200
    },
    {
      "key": "resource-b",
      "fencing_token": 42,
      "lease_id": 10,
      "holder_id": "process-456",
      "reentrant": true,
      "acquire_count": 2,
      "ttl": 60,
      "acquired_at": 1704067180
    }
  ]
}
```

**Example:**
```bash
curl -sk 'https://localhost:63002/v1/lock/list?prefix=resource' | jq .
```

## gRPC API

The gRPC API defines lock methods as protobuf messages. Source: `pkg/api/grpc/proto/qrdbv1/qrdb.proto`

### Methods

| Method | Request | Response |
|--------|---------|----------|
| `lock.Acquire` | `LockAcquireRequest` | `LockInfo` |
| `lock.TryLock` | `LockTryLockRequest` | `LockInfo` |
| `lock.Release` | `LockReleaseRequest` | `Empty` |
| `lock.Extend` | `LockExtendRequest` | `LockInfo` |
| `lock.Status` | `LockStatusRequest` | `LockInfo` |
| `lock.List` | `LockListRequest` | `LockListResponse` |

All methods include standard gRPC error codes:
- `OK` (0): Success
- `INVALID_ARGUMENT` (3): Invalid TTL, empty key
- `NOT_FOUND` (5): Lock does not exist
- `ALREADY_EXISTS` (6): Lock held by another holder
- `FAILED_PRECONDITION` (9): Stale fencing token
- `CANCELLED` (1): Context canceled
- `DEADLINE_EXCEEDED` (4): Request timeout

## CLI Reference

### lock acquire

Blocking lock acquisition.

```bash
qrdb lock acquire <key> --ttl <seconds> [--holder <id>] [--reentrant] [--timeout <seconds>]
```

**Options:**
- `--ttl`: Required. Lock time-to-live in seconds
- `--holder`: Optional. Holder ID (UUID generated if not provided)
- `--reentrant`: Flag. Enable reentrant acquisition
- `--timeout`: Optional. Command timeout (default: 10s)

**Example:**
```bash
qrdb lock acquire myresource --ttl 30
qrdb lock acquire leader --ttl 60 --holder node-1 --reentrant
```

### lock trylock

Non-blocking lock attempt.

```bash
qrdb lock trylock <key> --ttl <seconds> [--holder <id>] [--reentrant]
```

**Options:**
- `--ttl`: Required. Lock time-to-live in seconds
- `--holder`: Optional. Holder ID
- `--reentrant`: Flag. Enable reentrant acquisition

**Example:**
```bash
qrdb lock trylock myresource --ttl 30
```

### lock release

Release a held lock.

```bash
qrdb lock release <key> --token <fencing-token>
```

**Options:**
- `--token`: Required. The fencing token returned by acquire/trylock

**Example:**
```bash
qrdb lock release myresource --token 42
```

### lock extend

Extend lock TTL.

```bash
qrdb lock extend <key> --token <fencing-token>
```

**Options:**
- `--token`: Required. The fencing token of the held lock

**Example:**
```bash
qrdb lock extend myresource --token 42
```

### lock status

Query lock state.

```bash
qrdb lock status <key>
```

**Example:**
```bash
qrdb lock status myresource
```

### lock list

List active locks.

```bash
qrdb lock list [--prefix <prefix>]
```

**Options:**
- `--prefix`: Optional. Filter locks by key prefix

**Example:**
```bash
qrdb lock list
qrdb lock list --prefix resource-
```

## SDK Client Interface

### Go SDK

The Go SDK exposes locks through the `LockClient` interface:

```go
type LockClient interface {
    LockAcquire(ctx context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*Lock, error)
    LockTryLock(ctx context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*Lock, error)
    LockRelease(ctx context.Context, key string, fencingToken int64) error
    LockExtend(ctx context.Context, key string, fencingToken int64) (*Lock, error)
    LockStatus(ctx context.Context, key string) (*Lock, error)
    LockList(ctx context.Context, prefix string) ([]*Lock, error)
}
```

All methods are safe for concurrent use and respect the context deadline.

### TypeScript SDK

The TypeScript SDK exposes the same interface through the `LockClient` class:

```typescript
interface LockClient {
    acquire(key: string, holderID: string, ttl: number, reentrant: boolean): Promise<LockInfo>
    tryLock(key: string, holderID: string, ttl: number, reentrant: boolean): Promise<LockInfo>
    release(key: string, fencingToken: number): Promise<void>
    extend(key: string, fencingToken: number): Promise<LockInfo>
    status(key: string): Promise<LockInfo | null>
    list(prefix?: string): Promise<LockInfo[]>
}
```

## Transport Coverage

All transports implement the full lock API:

| Transport | Mechanism | File |
|-----------|-----------|------|
| Embedded | Direct service call | `sdk/go/transport/embedded/transport_lock.go` |
| REST | HTTP JSON POST/GET | `sdk/go/transport/rest/transport_lock.go` |
| gRPC | Protobuf unary RPC | `sdk/go/transport/grpc/transport_lock.go` |
| QUIC | Protobuf over QUIC | `sdk/go/transport/quic/transport_lock.go` |
| Unix | gRPC over UDS | `sdk/go/transport/unix/transport_lock.go` |

## Usage Examples

### Simple Mutex

```go
import (
    "context"
    "fmt"
    "time"

    sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

func main() {
    client, _ := sdk.NewClient(sdk.WithTransport("grpc"))
    defer client.Close()

    ctx := context.Background()
    lock, err := client.Lock().LockAcquire(ctx, "myresource", "", 30*time.Second, false)
    if err != nil {
        panic(err)
    }
    defer client.Lock().LockRelease(ctx, "myresource", lock.FencingToken)

    fmt.Println("Critical section protected by lock")
    // Perform critical work...
}
```

### Leader Election

```go
func leaderElection(nodeID string) error {
    client, _ := sdk.NewClient(sdk.WithTransport("grpc"))
    defer client.Close()

    ctx := context.Background()

    for {
        // Try to acquire leader lock
        lock, err := client.Lock().LockTryLock(ctx, "leader", nodeID, 60*time.Second, false)
        if err != nil {
            // Another node is leader; wait for release
            watchLock(ctx, client)
            continue
        }

        fmt.Println("I am the leader")
        runLeaderTasks(ctx, client, lock)

        // Release on exit
        _ = client.Lock().LockRelease(ctx, "leader", lock.FencingToken)
        break
    }

    return nil
}

func watchLock(ctx context.Context, client sdk.Client) {
    watcher, _ := client.Watch().Watch(ctx, "__sys:lock/leader")
    defer watcher.Close()

    for range watcher.Events() {
        // Lock released, retry
        return
    }
}
```

### Resource Coordination with Fencing Token Validation

```go
type DatabaseConnection struct {
    client sdk.Client
    lock *Lock
}

func (db *DatabaseConnection) ExecuteWithLock(ctx context.Context, query string) error {
    lock, err := db.client.Lock().LockAcquire(ctx, "db-write", "", 10*time.Second, false)
    if err != nil {
        return err
    }
    defer db.client.Lock().LockRelease(ctx, "db-write", lock.FencingToken)

    // Attach fencing token to the query metadata
    return db.execute(ctx, query, lock.FencingToken)
}

func (db *DatabaseConnection) execute(ctx context.Context, query string, token int64) error {
    // Upstream service validates token before accepting query
    // (prevents executing with stale lock knowledge)
    return db.executeInternal(ctx, query, token)
}
```

### TypeScript Example

```typescript
import { RestClient } from "@qrdb/sdk";

async function main() {
    const client = new RestClient("https://localhost:63002");
    
    try {
        // Acquire lock
        const lock = await client.lock.acquire("myresource", "", 30, false);
        console.log("Lock acquired:", lock.fencingToken);

        // Do work...
        await doImportantWork();

        // Release lock
        await client.lock.release("myresource", lock.fencingToken);
    } catch (err) {
        console.error("Lock error:", err.message);
    }
}
```

## Error Handling

Errors returned by lock operations are typed for proper error handling:

| Error | HTTP | gRPC | Description |
|-------|------|------|-------------|
| `LockHeldError` | `409 Conflict` | `ALREADY_EXISTS` | Lock held by another holder |
| `NotLockHolderError` | `403 Forbidden` | `FAILED_PRECONDITION` | Stale fencing token on unlock/extend |
| `LockNotFoundError` | `404 Not Found` | `NOT_FOUND` | Key not currently locked |
| `ErrEmptyKey` | `400 Bad Request` | `INVALID_ARGUMENT` | Lock key is empty |
| `ErrInvalidTTL` | `400 Bad Request` | `INVALID_ARGUMENT` | TTL must be positive |

Example error handling:

```go
lock, err := client.Lock().LockAcquire(ctx, "resource", "", 30*time.Second, false)

var held *lock.LockHeldError
if errors.As(err, &held) {
    fmt.Printf("Lock held by %s\n", held.HolderID)
    // Wait and retry
}

var notHolder *lock.NotLockHolderError
if errors.As(err, &notHolder) {
    fmt.Printf("Stale token: expected %d, actual %d\n", notHolder.ExpectedToken, notHolder.ActualToken)
    // Log and abandon
}

var notFound *lock.LockNotFoundError
if errors.As(err, &notFound) {
    fmt.Println("Lock was released or never acquired")
    // Handle release
}
```

## Best Practices

### TTL Sizing

Choose a TTL that balances false expiry and stale lock detection:

- **Too short** (e.g., 1s): False expiry when network/process latency exceeds TTL
- **Too long** (e.g., 3600s): Slow stale lock detection (full hour to recover)
- **Good range** (10-60s): Reasonable recovery time with low false positivity

For long-running operations, use `Extend()` to refresh the TTL periodically rather than acquiring with a very long TTL.

### Fencing Token Validation

Always validate fencing tokens in downstream services:

```go
// Bad: ignoring token
func criticalWork(ctx context.Context) {
    lock, _ := client.Lock().LockAcquire(ctx, "resource", "", 30*time.Second, false)
    defer client.Lock().LockRelease(ctx, "resource", lock.FencingToken)

    // If this service crashes and restarts, it might proceed
    // with stale lock knowledge
    executeQuery(ctx, "UPDATE important_table")
}

// Good: passing token to verifier
func criticalWork(ctx context.Context) {
    lock, _ := client.Lock().LockAcquire(ctx, "resource", "", 30*time.Second, false)
    defer client.Lock().LockRelease(ctx, "resource", lock.FencingToken)

    // Downstream verifies token before accepting
    executeQuery(ctx, "UPDATE important_table", lock.FencingToken)
}
```

### Avoiding Deadlocks

Always use context with a timeout when acquiring locks:

```go
// Bad: no timeout
lock, err := client.Lock().LockAcquire(context.Background(), "resource", "", 30*time.Second, false)

// Good: context with timeout
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
lock, err := client.Lock().LockAcquire(ctx, "resource", "", 30*time.Second, false)
if err == context.DeadlineExceeded {
    fmt.Println("Failed to acquire lock within timeout")
}
```

### Reentrant Locks

Use reentrant locks sparingly. They are useful for protecting shared state within a single component that calls itself recursively, but can obscure lock dependencies:

```go
// Appropriate: recursive function protecting shared state
func (t *Tree) Insert(value int, reentrant bool) error {
    lock, err := locker.Lock(ctx, LockOptions{
        Key: "tree",
        TTL: 30*time.Second,
        Reentrant: reentrant,
    })
    if err != nil {
        return err
    }
    defer locker.Unlock(ctx, "tree", lock.FencingToken)

    // Safe to call Insert recursively
    return t.insertNode(value)
}

// Poor: reentrant locks hiding dependencies
func (s *Service) Handle(event Event) {
    s.Lock()
    defer s.Unlock()

    // Unclear what state is protected
    s.AnotherMethod()  // also acquires lock
}
```

### Cleanup on Context Cancellation

Ensure locks are released even when context is canceled:

```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

lock, err := client.Lock().LockAcquire(ctx, "resource", "", 30*time.Second, false)
if err != nil {
    // Context may be canceled here
    return err
}

// Ensure cleanup even if ctx is canceled
defer func() {
    // Use a background context for cleanup to avoid context cancellation
    releaseCtx := context.Background()
    client.Lock().LockRelease(releaseCtx, "resource", lock.FencingToken)
}()

// Do work with lock
return doWork(ctx)
```

## See Also

- [06-leases.md](leases/engineering.md) -- Lease management and TTL enforcement
- [03-transactions.md](transactions/engineering.md) -- Compare-and-swap transactions
- [05-consistency.md](05-consistency.md) -- Consistency levels and routing
