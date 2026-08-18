<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# TypeScript SDK

The `@qrdb/sdk` npm package at `sdk/typescript/` provides a standalone,
reusable TypeScript client library for interacting with QRDB. It mirrors
the Go SDK pattern with typed interfaces, environment-aware transport
selection, and comprehensive error types.

## Package Structure

```
sdk/typescript/
  package.json          - @qrdb/sdk, version 0.1.0, ESM
  tsconfig.json         - Strict TypeScript compilation
  src/
    index.ts            - Barrel exports (public API surface)
    interfaces.ts       - All client interfaces (18 interfaces)
    types.ts            - All request/response types
    factory.ts          - Environment detection + client factory
    wails.ts            - WailsClient (wraps Wails-generated IPC bindings)
    rest.ts             - RestClient (fetch() against REST API)
    errors.ts           - 25 typed error classes
```

## Client Interface

Source: `sdk/typescript/src/interfaces.ts`

```typescript
export interface Client {
    readonly connection: ConnectionClient;
    readonly kv: KVClient;
    readonly admin: AdminClient;
    readonly mvcc: MVCCClient;
    readonly transaction: TransactionClient;
    readonly batch: BatchClient;
    readonly watch: WatchClient;
    readonly lease: LeaseClient;
    readonly index: IndexClient;
    readonly timeSeries: TimeSeriesClient | null;  // null when capability disabled
    readonly vector: VectorClient | null;           // null when capability disabled
    readonly users: UserClient;
    readonly roles: RoleClient;
    readonly certs: CertClient;
    readonly seal: SealClient;
    readonly wizard: WizardClient;
    readonly embeddedServer: EmbeddedServerClient;
}
```

The `timeSeries` and `vector` properties are nullable because these are
compile-time features gated behind build tags in the QRDB server binary.
The GUI checks `capabilities.timeSeries` and `capabilities.vector` to
determine whether to render the corresponding views.

## Service Interfaces

| Interface              | Methods | Description                                  |
|------------------------|---------|----------------------------------------------|
| `ConnectionClient`     | 4       | Connect, disconnect, isConnected, getVersion |
| `KVClient`             | 6       | put, get, delete, list, exists, scan         |
| `AdminClient`          | 7       | health, status, peers, add/remove member, snapshot, drain, capabilities |
| `MVCCClient`           | 6       | putWithLease, getRevision, getAtRevision, range, compact, currentRevision |
| `TransactionClient`    | 1       | execute (compare-and-swap)                   |
| `BatchClient`          | 1       | execute (atomic multi-op)                    |
| `WatchClient`          | 2       | watch, watchPrefix                           |
| `LeaseClient`          | 5       | grant, revoke, keepAlive, timeToLive, list   |
| `IndexClient`          | 4       | create, drop, query, scan                    |
| `TimeSeriesClient`     | 10      | write, writeBatch, query, aggregate, writePoint, richQuery, deleteSeries, get/setRetention, listMetrics |
| `VectorClient`         | 20      | Namespace CRUD, vector CRUD, search (4 modes), index maintenance, compaction |
| `UserClient`           | 7       | create, get, update, delete, list, enable, disable |
| `RoleClient`           | 8       | create, get, update, delete, list, assign, unassign, listPermissions |
| `CertClient`           | 4       | generate, export, revoke, list               |
| `SealClient`           | 5       | initialize, initializeShamir, unseal, seal, status |
| `WizardClient`         | 4       | isFirstRun, saveState, loadState, getConfigDir |
| `EmbeddedServerClient` | 3       | startEmbedded, stopEmbedded, isEmbedded      |

## Factory

Source: `sdk/typescript/src/factory.ts`

```typescript
export async function getClient(config?: ClientConfig): Promise<Client> {
    if (isWailsRuntime()) {
        const { WailsClient } = await import('./wails.js');
        return new WailsClient();
    }
    const { RestClient } = await import('./rest.js');
    return new RestClient(config?.baseUrl ?? '');
}

export function isDesktop(): boolean {
    return isWailsRuntime();
}
```

Environment detection checks for the Wails-generated `window.go`
namespace. When present, the factory returns a `WailsClient` that
communicates via IPC. Otherwise, it returns a `RestClient` that uses
`fetch()`. Both transport modules are dynamically imported to avoid
bundling unused code.

## Transport Implementations

### WailsClient

Source: `sdk/typescript/src/wails.ts`

Wraps the auto-generated TypeScript bindings that Wails produces from the
Go binding structs. Each method call becomes an IPC message to the Go
process running the embedded QRDB server.

Desktop-only features (`WizardClient`, `EmbeddedServerClient`) are fully
functional in this transport. Attempting to use them from the `RestClient`
raises a `DesktopOnlyError`.

### RestClient

Source: `sdk/typescript/src/rest.ts`

Uses the built-in `fetch()` API to communicate with the qrdbd REST API.
No external HTTP dependencies are required. Request and response bodies
are JSON-serialized.

The `baseUrl` parameter is required when constructing a `RestClient`
directly. When using `getClient()`, it defaults to `''` (same origin)
for browser deployments served by qrdbd.

## Watch Streaming

Source: `sdk/typescript/src/interfaces.ts`

```typescript
export interface WatchHandle {
    readonly id: number;
    onEvent(callback: (events: readonly WatchEvent[]) => void): () => void;
    onCancel(callback: (response: WatchResponse) => void): () => void;
    cancel(): Promise<void>;
}
```

In desktop mode, watch events are delivered via Wails `runtime.EventsEmit`
and received through the Wails runtime event listener. The `WailsClient`
wraps this into the `WatchHandle` interface.

In browser mode, the `RestClient` uses Server-Sent Events (SSE) or
long-polling against the REST watch endpoint.

The `onEvent` callback receives batches of events for efficiency. It
returns an unsubscribe function for cleanup. `cancel()` releases
server-side resources.

## Error Types

Source: `sdk/typescript/src/errors.ts`

All errors extend the base `QRDBError` class:

```typescript
export class QRDBError extends Error {
    constructor(message: string) {
        super(message);
        this.name = 'QRDBError';
    }
}
```

### Transport Errors

| Error Class        | Condition                                    |
|--------------------|----------------------------------------------|
| `ConfigError`      | Invalid transport configuration field        |
| `ConnectionError`  | Connection to server failed                  |
| `NotConnectedError`| Operation requires active connection         |
| `ClosedError`      | Operation on closed transport                |
| `TransportError`   | HTTP request or RPC call failed              |
| `TLSError`         | TLS configuration or handshake failure       |

### Service Errors

| Error Class              | Condition                                    |
|--------------------------|----------------------------------------------|
| `NotLeaderError`         | Node is not Raft leader for shard            |
| `KeyNotFoundError`       | Key does not exist in store                  |
| `RevisionCompactedError` | Requested revision has been compacted        |
| `RevisionNotFoundError`  | Requested revision does not exist            |
| `TransactionFailedError` | Transaction preconditions failed             |
| `LeaseNotFoundError`     | Lease ID does not exist                      |
| `LeaseExpiredError`      | Lease has expired                            |
| `WatchCanceledError`     | Watch stream was canceled                    |
| `IndexExistsError`       | Index name already exists                    |
| `IndexNotFoundError`     | Index name not found                         |

### Vector Errors

| Error Class              | Condition                                    |
|--------------------------|----------------------------------------------|
| `VectorNotFoundError`    | Vector ID not found in namespace             |
| `NamespaceNotFoundError` | Vector namespace does not exist              |
| `NamespaceExistsError`   | Namespace name already taken                 |
| `DimensionMismatchError` | Vector dimensionality does not match config  |

### Management Errors

| Error Class        | Condition                                    |
|--------------------|----------------------------------------------|
| `UserNotFoundError`| User ID does not exist                       |
| `UserExistsError`  | Username already exists in tenant            |
| `RoleNotFoundError`| Role ID does not exist                       |
| `RoleExistsError`  | Role name already exists in tenant           |
| `CertNotFoundError`| Certificate serial not found                 |

### Feature Errors

| Error Class          | Condition                                  |
|----------------------|--------------------------------------------|
| `FeatureDisabledError`| Compile-time feature not enabled in server |
| `DesktopOnlyError`   | Feature requires Wails desktop mode        |
| `NotImplementedError`| Transport method not yet implemented       |

## npm Package Configuration

Source: `sdk/typescript/package.json`

```json
{
  "name": "@qrdb/sdk",
  "version": "0.1.0",
  "type": "module",
  "main": "dist/index.js",
  "types": "dist/index.d.ts",
  "exports": {
    ".": {
      "types": "./dist/index.d.ts",
      "import": "./dist/index.js"
    }
  },
  "license": "Apache-2.0",
  "peerDependencies": {
    "@wailsapp/runtime": "^2.x"
  },
  "peerDependenciesMeta": {
    "@wailsapp/runtime": { "optional": true }
  },
  "devDependencies": {
    "typescript": "^5.7.0"
  }
}
```

The REST transport has zero external dependencies -- it uses the built-in
`fetch()` API. The Wails runtime is an optional peer dependency required
only for desktop mode.

## Frontend Integration

The QRDB frontend references the SDK as a local file dependency:

Source: `cmd/qrdb/frontend/package.json`

```json
{
  "dependencies": {
    "@qrdb/sdk": "file:../../../sdk/typescript"
  }
}
```

This enables Vite to resolve the SDK during development with hot module
replacement and during production builds with tree-shaking.

## Usage

### Browser / Web Application

```typescript
import { getClient } from '@qrdb/sdk';

const client = await getClient({ baseUrl: 'https://qrdb.example.com:63002' });

// KV operations
await client.kv.put('mykey', 'myvalue');
const value = await client.kv.get('mykey');

// Watch for changes
const handle = await client.watch.watchPrefix('/config/', {});
handle.onEvent((events) => {
    for (const event of events) {
        console.log(event.type, event.key);
    }
});
```

### Desktop (Wails)

```typescript
import { getClient, isDesktop } from '@qrdb/sdk';

// Auto-detects Wails runtime, no baseUrl needed
const client = await getClient();

if (isDesktop()) {
    // Desktop-only: start embedded server
    await client.embeddedServer.startEmbedded(config);
}

// Same API regardless of transport
await client.kv.put('mykey', 'myvalue');
```

## Cross-References

- [GUI System](../gui/README.md) -- Desktop GUI that consumes this SDK
- [Go SDK](../api-server/engineering.md) -- Go client library with 5 transports
- [API Reference](../api-server/api.md) -- REST API endpoints used by `RestClient`
- [CLI Reference](../api-server/cli.md) -- CLI commands (same binary as GUI)
