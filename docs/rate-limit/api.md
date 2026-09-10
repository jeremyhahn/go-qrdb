# Rate Limiting — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-10

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
| QUIC | An authorization-error frame naming the permission and the refusal reason; no separate numeric status for this class of error today |
| MCP | The tool call's error result carries the refusal text; no numeric status code — MCP errors are text, not codes |

Every row above is reached from the same enforcement call
(`AuthMiddleware.authorize`), so a `Policy` declared for a permission is
enforced identically wherever a caller reaches that permission — not
declared once per transport, and not at risk of drifting between them.

## What a caller learns from a refusal

The error names the **permission**, the **caller** (subject and tenant), and
a **retry-after duration** — the same "carry the number a caller needs to
act on the refusal" shape the key-value feature's request-size bound
established: a caller told only "denied" cannot choose to wait the right
amount, or confirm which operation is the one that is actually throttled.

## No configuration endpoint

There is no REST route, gRPC method, QUIC frame, or MCP tool to:

- Read a permission's currently-declared `Policy`.
- Change a `Policy` while the process is running.
- Reset, inspect, or clear an individual caller's bucket.
- Discover, from outside the process, which permissions carry a `Policy` at
  all.

`Policy` declarations are a construction-time decision made through the Go
embedding API (`ratelimit.Registry.Register`, wired through
`wiring.AuthMiddlewareConfig`); see [engineering.md](engineering.md) for the
exact surface and [user.md](user.md) for a worked example. A runtime
configuration and introspection surface is future work, not part of this
capability.
