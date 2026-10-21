# TypeScript SDK — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

No QRDB CLI command is owned by the TypeScript SDK. The package is built and
tested with npm scripts from `sdk/typescript`.

## `npm run build`

Runs `tsc` and writes the package output to `dist/`.

```sh
cd sdk/typescript
npm run build
```

## `npm test`

Runs TypeScript contract checks and Vitest.

```sh
cd sdk/typescript
npm test
```

## Frontend E2E

The desktop GUI consumes the SDK through a local file dependency. Its E2E suite
is owned by `docs/desktop-gui/`, but it is also the browser/desktop integration
proof for this SDK.

```sh
cd cmd/qrdb/frontend
npm run test:e2e
```

## Parity

`make integration-test-typescript-sdk` resolves through the feature pattern rule
and runs zero Go CLI tests by design: the SDK is not a cobra command surface,
and `test/integration/features.mk` maps no test file to it.
