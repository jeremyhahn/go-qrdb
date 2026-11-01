# Rate Limiting — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-27
> Conflicts with this document are defects. Fix them here first.

Rate limiting has **no dedicated wire capability of its own** — no endpoint,
RPC, or tool to configure, inspect, or reset a limit at runtime. It is a
cross-cutting enforcement layer that changes the response every transport
gives for **any** permission an operator has declared a `Policy` for.

## What every transport does when a permission is throttled

| Transport | Behavior when a caller exceeds its `Policy` |
|---|---|
| REST | `429 Too Many Requests`, JSON body `{"error": "..."}`, `Retry-After` header (seconds, rounded up to at least 1) |
| gRPC (TCP) | `codes.ResourceExhausted` |
| gRPC over Unix socket | `codes.ResourceExhausted` — identical to TCP gRPC; the unix listener shares the same interceptor |
| QUIC | `StatusResourceExhausted`, in a frame naming the permission and the refusal reason ([the QUIC status vocabulary](../api-server/api.md#the-quic-status-vocabulary)) |
| MCP | `RESOURCE_EXHAUSTED` (−32008) in the tool result's error envelope, alongside the refusal text ([the MCP error vocabulary](../api-server/api.md#the-mcp-error-vocabulary)) |
| Embedded | `*middleware.RateLimitedError`, returned to the Go caller |

Every row above is reached from the same enforcement call
(`AuthMiddleware.authorize`), so a `Policy` declared for a permission is
enforced identically wherever a caller reaches that permission — not
declared once per transport, and not at risk of drifting between them.

QUIC and MCP answer the resource-exhausted class rather than
permission-denied or unavailable: permission-denied is terminal in every
client and sends an operator to fix grants that are not the problem, and
unavailable loses the reason and points at the node's health.

## The client-address limit

The pre-authentication limit (`transport.client_rate_limit`) refuses before
a request is authenticated, with the same status per transport as the table
above: REST and MCP answer the HTTP request itself with `429` and
`Retry-After` (MCP before its JSON-RPC layer runs), gRPC, the Unix socket and
a database's etcd v3 listener `codes.ResourceExhausted` with a `google.rpc.RetryInfo` detail, QUIC
`StatusResourceExhausted` with RetryInfo. The message names the client
address, never a principal. A health check is refused like any other
request, so a client's connect probe reports the refusal.

The SQL wire listeners refuse in their own protocol's terms, at connection and per statement:

| Listener | Connection refused | Statement refused |
|---|---|---|
| MySQL | ERR packet, error 1226 `ER_USER_LIMIT_REACHED`, SQLSTATE `42000` ([MySQL server error reference](https://dev.mysql.com/doc/mysql-errors/8.4/en/server-error-reference.html#error_er_user_limit_reached)) | the same ERR packet |
| PostgreSQL | ErrorResponse, severity `FATAL`, SQLSTATE `08004` `sqlserver_rejected_establishment_of_sqlconnection`, then the connection closes | ErrorResponse, severity `ERROR`, SQLSTATE `53400` `configuration_limit_exceeded`; the session stays open ([PostgreSQL error codes](https://www.postgresql.org/docs/current/errcodes-appendix.html)) |

## What a caller learns from a refusal

The error names the **permission**, the **caller** (subject and tenant), and
a **retry-after duration**: a caller told only "denied" cannot choose to wait
the right amount, or confirm which operation is the one that is throttled.

## No configuration endpoint

There is no REST route, gRPC method, QUIC frame, or MCP tool to:

- Read a permission's currently-declared `Policy`.
- Change a `Policy` while the process is running.
- Reset, inspect, or clear an individual caller's bucket.
- Discover, from outside the process, which permissions carry a `Policy` at
  all.

`Policy` declarations are a construction-time decision made through the Go
embedding API (`ratelimit.Registry.Register`, wired through
`wiring.AuthMiddlewareConfig.RateLimitPolicies`); see
[engineering.md](engineering.md) for the exact surface and
[user.md](user.md) for a worked example.
