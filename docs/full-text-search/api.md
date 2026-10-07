# Full-Text Search — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

## Capability matrix

| Capability | REST | gRPC / Unix | QUIC | MCP | Embedded | `scp` permission |
|---|---|---|---|---|---|---|
| Index a document | MISSING | MISSING | MISSING | MISSING | `fts.FTSIndex.IndexDocument` (in-process import) | none defined |
| Remove a document | MISSING | MISSING | MISSING | MISSING | `fts.FTSIndex.RemoveDocument` | none defined |
| Search (AND) | MISSING | MISSING | MISSING | MISSING | `fts.FTSIndex.Search` | none defined |
| Search (OR) | MISSING | MISSING | MISSING | MISSING | `fts.FTSIndex.SearchOr` | none defined |
| Report FTS availability | `GET /capabilities` | `Capabilities` | `admin.Capabilities` | `qrdb_capabilities` | `Client.Admin().Capabilities` | `admin:capabilities` |

**Every row but the last is MISSING on all five network transports.**

`pkg/fts` is an in-process library with no `sdk/go` alias file, no transport
handler on any protocol, and no permission constants. The "Embedded" column
above is not the embedded *transport* — it is a direct Go import of `pkg/fts`,
which is a different and weaker thing: it bypasses the SDK boundary entirely.

Under the repo's export obligation, a qrdb capability with no SDK surface is
not shippable, so this is a **tracked defect** rather than a scope fence. It is
written as `MISSING` here, per the feature-doc convention, so the gap is
recorded rather than hidden by omission.

Closing it would need, at minimum:

- a permission vocabulary (`fts:index`, `fts:search`, `fts:remove` or similar) —
  none exists today;
- a `service` layer entry point that resolves tenant and database scope, since
  `pkg/fts` currently takes a raw `storage.Storage` from its caller;
- `sdk/go/transport` client interface plus the five transport implementations;
- a `qrdb fts` CLI verb group with `_PerProtocol` integration tests.

The shape of that surface is an architecture decision and belongs to the owner,
not to this document.

## Capability reporting

The one thing observable over the wire is whether the daemon was compiled with
FTS:

```bash
qrdb admin capabilities
```

`fts` is registered in `pkg/capabilities` under the `qrdb_fts` build tag,
alongside `sql`, `mysql`, `pgsql`, `etcd`, `vector` and `timeSeries`.

## Versioning

Not applicable — there are no routes to version or deprecate.
