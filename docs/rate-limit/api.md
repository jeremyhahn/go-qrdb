# Rate Limiting — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-22
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
