# Capabilities — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-22
> Conflicts with this document are defects. Fix them here first.

## Transport Matrix

| Capability | REST | gRPC | QUIC | MCP | Unix socket | Embedded Go | Permission |
|---|---|---|---|---|---|---|---|
| Read effective profile | `GET /capabilities` | `qrdb.v1.QRDBService.Capabilities` | `admin.Capabilities` | `qrdb_capabilities` | same gRPC method over UDS | `Client.Admin().Capabilities` | `admin:capabilities` |

## Schema

The response is `capabilities.Capabilities` (`transport.CapabilitiesResponse`
in the Go SDK): `mode`, core feature booleans, optional feature booleans, and
optional `unencryptedAtRest`.

Request-feature bits say that this binary honours a request field an older
server would silently drop. Today there is one, `pendingUserTTL` (proto
`pending_user_ttl`, field 21): the server keeps `CreateUser`'s pending TTL. An
older server does not send a bit, so it reads as `false`, and the Go SDK
refuses a TTL-bearing create with `*transport.CapabilityUnsupportedError`
before sending it ([rolling upgrades](../rolling-upgrades/engineering.md),
clause (5), owner ruling 1243). The bit is independent of the
`pending-user-ttl` feature gate.

`false` is intentionally ambiguous. It may mean not compiled, not valid in the
current mode, or disabled by configuration.

## Errors

Authorization failures use the normal transport mapping for
`admin:capabilities`. Service failures map through the standard admin read path:
REST returns the mapped HTTP error, gRPC/Unix return the mapped status, QUIC
returns the command error frame, and MCP wraps the failure as a
`qrdb_capabilities` tool error.
