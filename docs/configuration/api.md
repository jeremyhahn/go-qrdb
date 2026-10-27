# Configuration — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-26 at main 851fd06fc3
> Conflicts with this document are defects. Fix them here first.

Configuration is read at boot and is **not** mutable over the wire. Two things
are observable, and one of them is public.

## Capability matrix

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded | `scp` permission |
|---|---|---|---|---|---|---|
| Is the system initialized? | `GET /v1/config/initialized` | not offered | not offered | not offered | — | **public** |
| Effective capability profile (incl. `mode`) | `GET /capabilities` | `Capabilities` | `admin.Capabilities` | `qrdb_capabilities` | `Client.Admin().Capabilities` | `admin:capabilities` |
| Change configuration | **not available** | — | — | — | — | n/a |

`GET /v1/config/initialized` is REST-only by design, not a gap. It is one of
very few unauthenticated routes, and it exists because a client in the
bootstrap window has no credentials — which also means it could not complete
the mTLS handshake the other transports require.

## Initialization response

`GET /v1/config/initialized` takes no parameters. When the request reaches the
handler, it answers `200` with:

```json
{"initialized": true}
```

`initialized` is `true` once the barrier records an initialization time. It is
`false` when the server has no barrier service, or when reading the barrier
status fails: the handler reports no error, so `false` means "not known to be
initialized".

The request does not always reach the handler. Two gates wrap the REST router,
and the route is on neither allowlist, so a bootstrap client must handle a
`503` whose body carries `code` instead of `initialized`. Every refusal below
is `503 Service Unavailable` with `Content-Type: application/json` and
`Retry-After: 5`:

| Node state | `code` | `error` |
|---|---|---|
| Barrier sealed (the sealed-window gate) | `SEALED` | `barrier sealed` |
| Seal state cannot be confirmed (the sealed-window gate) | `SEAL_STATE_UNCONFIRMED` | `clusterseal: this node cannot confirm the cluster seal state; retry` |
| Boot-time sealed-window listener after the barrier unseals | `SEALED_WINDOW_CLOSED` | `sealed-window listener closed: barrier unsealed` |
| Awaiting the admin credential (only ceremony status and ceremony initialize are served) | `AWAITING_ADMIN_CREDENTIAL` | `node is awaiting admin credential: only ceremony status and ceremony initialize (the admin-credential retry) are served until an admin certificate binds` |

A `SEALED_WINDOW_CLOSED` refusal means the client should reconnect to the
regular listener; the others clear on their own, and the client retries after
the `Retry-After` interval.

**There is no configuration-mutation capability on any transport.**
Configuration is read at boot; changing it is an edit plus a restart. Recorded
explicitly so the absence is read as a decision rather than a gap.

Some *runtime* objects that feel configuration-shaped do have APIs, and they
live in their own features: per-database protocol and barrier settings
([../databases/api.md](../databases/api.md)), tenant quotas and entitlements
([../multi-tenancy/api.md](../multi-tenancy/api.md)), retention policies
([../time-series/api.md](../time-series/api.md)).

## Capability profile response

`GET /capabilities` returns the **sealed** profile: build tags ∩ run mode ∩
config, plus a `mode` field (`standalone` or `raft`). It takes no parameters.

```json
{"mode": "raft", "kv": true, "mvcc": true, "transaction": true, "batch": true,
 "watch": true, "lease": true, "lock": true, "index": true, "cluster": true,
 "backup": true, "rebalance": true, "timeSeries": true, "vector": true,
 "sql": true, "mysql": false, "postgresql": false, "etcd": true, "fts": false,
 "unencryptedAtRest": ["..."]}
```

`unencryptedAtRest` is omitted when empty. A caller without
`admin:capabilities` is refused by the authorization middleware; an error from
the admin service is mapped to its HTTP status by the shared error table
([../api-server/](../api-server/)).

A capability reported `false` is three-way ambiguous by design — not compiled,
mode-gated, or config-disabled. See
[../run-modes/api.md](../run-modes/api.md#reporting-the-effective-profile).

## Load and validation contract

| Stage | Behaviour |
|---|---|
| Strict decode | a key naming no field in the schema fails the load as `*UnknownFieldError`, naming the key's full path and, where known, its line |
| Defaults | `DefaultConfig()` populates every field |
| YAML | only fields present override |
| Environment | `QRDB_`-prefixed variables override the file; read only when `--config` names one |
| Flags | only explicitly-set flags override |
| `Validate()` | returns the **first** error as `*ValidationError` with the field path and reason |
| `ValidateCapabilities()` | separate, called by composition roots; fails naming the missing build tag |

`*LoadError` is returned when the YAML cannot be read or parsed, and implements
`Unwrap()`. A key for a feature not compiled into this binary is not an
unknown key — the schema always declares it — and surfaces only through
`ValidateCapabilities()`.

## Secret references

`qrdbd`'s configuration has no field that takes a secret reference. An
application embedding qrdb resolves `credential://` and `literal://`
references in its own configuration at boot, into enclave-backed values rather
than plain strings; **a protected field whose reference cannot be resolved
fails the boot.** This is a composition-plane (Tier 2) surface with no wire endpoint — see
[../credentials/api.md](../credentials/api.md#secret-resolution-spi).

## Versioning

`GET /v1/config/initialized` is under `/v1/`; `GET /capabilities` is
unversioned. Neither is deprecated.
