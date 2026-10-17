# Rate Limiting — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06
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
| QUIC | `StatusResourceExhausted`, in a frame naming the permission and the refusal reason |
| MCP | `RESOURCE_EXHAUSTED` (−32008) in the tool result's error envelope, alongside the refusal text |

Every row above is reached from the same enforcement call
(`AuthMiddleware.authorize`), so a `Policy` declared for a permission is
enforced identically wherever a caller reaches that permission — not
declared once per transport, and not at risk of drifting between them.

The QUIC row read "no separate numeric status for this class of error today"
until the QUIC wire gained a status vocabulary wide enough to say it. What it
had before was a choice between two wrong answers: permission-denied, which is
terminal in every client and sends an operator to fix grants that were never
the problem, and unavailable, which at least carries the right instruction —
retry — but loses the reason and points at the node's health. See
[the API server's status vocabulary](../api-server/api.md#the-quic-status-vocabulary).

The MCP row read "no code at all" for longer and for a different reason: MCP had
no error-code vocabulary whatsoever, so there was nothing to put a code in
rather than nothing wide enough to say it. It now answers
`RESOURCE_EXHAUSTED` (−32008) — the same class gRPC answers and the sibling of
REST's 429 — because the MCP authorization gate renders the SHARED auth-failure
classification rather than a coarser retryable/not split of its own. See
[the MCP error vocabulary](../api-server/api.md#the-mcp-error-vocabulary).

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
