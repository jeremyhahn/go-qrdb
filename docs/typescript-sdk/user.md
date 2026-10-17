# TypeScript SDK — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06
> Conflicts with this document are defects. Fix them here first.

## Prerequisites

Use Node.js with npm. The package source is `sdk/typescript`, and the QRDB
frontend already depends on it through `file:../../../sdk/typescript`.

## Browser Usage

```ts
import { getClient } from "@qrdb/sdk";

const client = await getClient({ baseUrl: "https://db.example.com:63002" });
await client.kv.put("hello", "world");
const value = await client.kv.get("hello");
```

## Desktop Usage

```ts
import { getClient, isDesktop } from "@qrdb/sdk";

const client = await getClient();
if (isDesktop()) {
  await client.embeddedServer.isEmbedded();
}
```

## Build And Test

```sh
cd sdk/typescript
npm test
```

For the GUI integration path:

```sh
cd cmd/qrdb/frontend
npm run test:e2e
```

The feature target is `make integration-test-typescript-sdk`; it is intentionally
empty in the Go integration harness. Its coverage accounting row is
`test/integration/COVERAGE.md` under `typescript-sdk`.

## Troubleshooting

`DesktopOnlyError` means the call requires Wails desktop mode. Use the desktop
app, or avoid the embedded-server and other desktop-only clients from browser
code.

`NotImplementedError` means the selected client does not expose that operation
yet. Use the desktop path if the operation is implemented only through Wails
bindings, or call the Go SDK/CLI until the TypeScript surface grows.
