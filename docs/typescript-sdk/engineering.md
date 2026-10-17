# TypeScript SDK — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06
> Conflicts with this document are defects. Fix them here first.

## Exported Surface

The package name is `@qrdb/sdk`. Its package metadata is
`sdk/typescript/package.json`, with `main` set to `dist/index.js`, `types` set
to `dist/index.d.ts`, and the root export mapped to the same files.

The public API is exported from `sdk/typescript/src/index.ts`. The concrete
clients are `RestClient` in `rest.ts` and `WailsClient` in `wails.ts`.
`getClient(config?: ClientConfig)` chooses Wails when the runtime exposes the
Wails namespace and otherwise returns the REST client. `isDesktop()` reports
the same runtime test.

The client contract is `Client` in `interfaces.ts`. It exposes connection, KV,
admin, MVCC, transactions, batches, watches, leases, locks, indexes, optional
time-series and vector clients, users, invitations, roles, certificates,
databases, SQL, seal, ceremony, officers, embedded server, daemon, collections,
tenants, volume pools, server config, backup, rebalance, credentials, audit,
and connection-manager clients.

## Invariants

REST calls convert between TypeScript camelCase DTOs and REST snake_case wire
fields where needed. Wails calls preserve the Go binding contract and normalize
wire enum and identifier fields through `transform.ts`.

Desktop-only operations fail with `DesktopOnlyError` outside Wails mode.
Compile-time feature absence is represented with nullable feature clients or
`FeatureDisabledError`, depending on the operation.

## Error Semantics

All SDK-specific errors extend `QRDBError` from `errors.ts`. Transport setup and
runtime failures use `ConfigError`, `ConnectionError`, `NotConnectedError`,
`ClosedError`, `TransportError`, and `TLSError`. Service-level conditions map
to typed errors such as `KeyNotFoundError`, `NotLeaderError`,
`TransactionFailedError`, `LeaseNotFoundError`, `WatchCanceledError`,
`IndexExistsError`, vector namespace errors, user/role errors,
`CertNotFoundError`, `FeatureDisabledError`, `DesktopOnlyError`, and
`NotImplementedError`.

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
