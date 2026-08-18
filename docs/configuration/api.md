# Configuration — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Configuration is read at boot and is **not** mutable over the wire. Two things
are observable, and one of them is public.

## Capability matrix

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded | `scp` permission |
|---|---|---|---|---|---|---|
| Is the system initialized? | `GET /v1/config/initialized` | MISSING | MISSING | MISSING | — | **public** |
| Effective capability profile (incl. `mode`) | `GET /capabilities` | `Capabilities` | `admin.Capabilities` | `qrdb_capabilities` | `Client.Admin().Capabilities` | `admin:capabilities` |
| Change configuration | **not available** | — | — | — | — | n/a |

`GET /v1/config/initialized` is REST-only, marked `MISSING` elsewhere per the
convention. It is one of very few unauthenticated routes, and it exists because
a client in the bootstrap window has no credentials — which also means it could
not complete the mTLS handshake the other transports require.

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
config, plus a `mode` field (`standalone` or `raft`).

A capability reported `false` is three-way ambiguous by design — not compiled,
mode-gated, or config-disabled. See
[../run-modes/api.md](../run-modes/api.md#reporting-the-effective-profile).

## Load and validation contract

| Stage | Behaviour |
|---|---|
| Defaults | `DefaultConfig()` populates every field |
| YAML | only fields present override |
| Flags | only explicitly-set flags override |
| `Validate()` | returns the **first** error as `*ValidationError` with the field path and reason |
| `ValidateCapabilities()` | separate, called by composition roots; fails naming the missing build tag |

`*LoadError` is returned when the YAML cannot be read or parsed, and implements
`Unwrap()`.

## Secret references

Configuration fields may carry `credential://` or `literal://` references,
resolved at boot into enclave-backed values rather than plain strings. **A
protected field must reference a secret, and resolution fails closed.** This is
a composition-plane (Tier 2) surface with no wire endpoint — see
[../credentials/api.md](../credentials/api.md#secret-resolution-spi).

## Versioning

`GET /v1/config/initialized` is under `/v1/`; `GET /capabilities` is
unversioned. Neither is deprecated. Within the configuration schema,
`seal.strategy: software` is a **deprecated alias** for `passphrase`,
canonicalized at load.
