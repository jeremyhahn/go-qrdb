# TypeScript SDK — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

## Exported Surface

The package name is `@qrdb/sdk`. Its package metadata is
`sdk/typescript/package.json`, with `main` set to `dist/index.js`, `types` set
to `dist/index.d.ts`, and the root export mapped to the same files.

The public API is exported from `sdk/typescript/src/index.ts`. The concrete
clients are `RestClient` in `rest.ts` and `WailsClient` in `wails.ts`.
`getClient(config?: ClientConfig)` chooses Wails when the Wails runtime has
defined `window.go` and otherwise returns the REST client. `isDesktop()` reports
the same runtime test.

The client contract is `Client` in `interfaces.ts`. It exposes connection, KV,
admin, MVCC, transactions, batches, watches, leases, locks, indexes, optional
time-series and vector clients, users, invitations, roles, certificates,
databases, SQL, seal, ceremony, officers, embedded server, daemon, collections,
tenants, volume pools, server config, backup, rebalance, credentials, audit,
and connection-manager clients. `databaseScoped(name)` returns a `Client`
whose data-plane calls target the named database.

## Invariants

REST calls convert between TypeScript camelCase DTOs and REST snake_case wire
fields where needed. Wails calls preserve the Go binding contract and normalize
wire enum and identifier fields through `transform.ts`.

Desktop-only operations fail with `DesktopOnlyError` outside Wails mode.
`timeSeries` and `vector` are typed `| null`, but `RestClient` and `WailsClient`
always return a client; a feature the server was built without fails on the
call.

## Error Semantics

The classes in `errors.ts` extend `QRDBError`, one per condition. `RestClient`
and `WailsClient` raise these:

| Error | Raised by |
|---|---|
| `TransportError` | `RestClient`, for every failed request; carries the HTTP status |
| `KeyNotFoundError` | `RestClient` `kv.get`, on a 404 |
| `NotConnectedError` | `RestClient`, on a call before `connection.connect` |
| `DesktopOnlyError` | `RestClient`, for desktop-only operations |
| `ShamirSplitMissingError`, `ShamirSplitMismatchError` | `RestClient` `tenants.barrierInit`, Shamir split rounds |
| `NotImplementedError` | `WailsClient`, for `admin.mintJoinToken` and `volumePools.*` |

`WailsClient` calls otherwise reject with the error the Go binding returns. The
other classes in `errors.ts` are not raised by either client.

`AuthFactorClient` raises `AuthFactorError`, which extends `Error` and carries
the operation and HTTP status. `AuditClient` raises a plain `Error`.

## Configuration

`ClientConfig` supplies the REST base URL. Browser deployments served by qrdbd
may use the default same-origin base URL. Desktop mode ignores REST base URL
selection and uses Wails IPC.

## Persistence

The SDK has no persistence layer. Callers own any cached connection profiles or
browser storage; the SDK only holds in-memory client state.

## Test Strategy

The package test command is `npm test` inside `sdk/typescript`; it runs
`tsc -p tsconfig.test.json` and `vitest run`. The repository frontend runs its
browser and desktop-facing suite from `cmd/qrdb/frontend` with `npm run
test:e2e` or `npm run test:e2e:real`.

The feature integration target is `make integration-test-typescript-sdk`. In
the Go integration mapping it is an empty target because this SDK is covered by
the TypeScript/Vitest and Playwright suites, not the Go CLI transport harness.
The coverage accounting row is `test/integration/COVERAGE.md` under
`typescript-sdk`.

## Observability

The SDK does not emit metrics or audit records. Observability belongs to the
server or Wails binding path that executes the requested operation.
