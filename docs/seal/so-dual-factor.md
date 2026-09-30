# Security Officer dual-factor (SO PIN)

The SO dual-factor gate adds an in-band Security Officer PIN as a
second factor — on top of the mTLS SO identity — for the governance
operations that change key material or ceremony membership. It exists
for FedRAMP / FIPS 140-3 separation-of-duties postures where possession
of the SO client certificate alone must not be sufficient to rekey the
barrier or alter the custodian set.

## Activation

The gate is OFF by default and is enabled explicitly:

```yaml
auth:
  so_dual_factor: true
```

Two conditions must both hold for the gate to be installed:

1. `auth.so_dual_factor: true` (threaded as `rest.WithSODualFactor()`
   by the orchestrator), and
2. an SO authenticator is wired (`server.WithSOAuthenticator` →
   `rest.WithSOAuthenticator`), backed by the Argon2id PIN commit
   stored at SO bootstrap (`auth.SOAuthenticator.SetSOPIN`).

If the flag is set without a verifier the gate is not installed,
so a misconfigured deployment cannot lock out the SO surface behind an
unverifiable check. When disabled (default) every transport's chain is
unchanged.

## Every network transport

The gate is ONE implementation, `middleware.SOGate`, applied by every
network transport under the same two conditions. The orchestrator builds it
over the same role service, audit logger and `auth.audit_failure_policy` REST
uses and hands it to each server (`WithSOGate`); REST's `SOMiddleware` is the
same gate. It runs after authentication and authorization, before any
handler.

| Transport | Where the SO PIN travels | Refusals |
|---|---|---|
| REST (HTTP/2, HTTP/3) | `X-QRDB-SO-PIN` header | 401 / 403 / 503 |
| gRPC, Unix socket | `x-qrdb-so-pin` metadata entry | `UNAUTHENTICATED` / `PERMISSION_DENIED` / `UNAVAILABLE` |
| MCP | `X-QRDB-SO-PIN` header on the `/mcp` request | `CodeUnauthenticated` / `CodePermissionDenied` / `CodeUnavailable` |
| QUIC | the request message's `so_pin` field | `StatusUnauthenticated` / `StatusPermissionDenied` / `StatusUnavailable` |

A QUIC frame has no header section, so on QUIC the PIN rides in the request
message of the barrier-PIN operations that already carry `so_pin`
(`barrier.ShamirInitialize`, `barrier.Rekey`, `barrier.RecoveryGenerate`,
`ceremony.Initialize`, `ceremony.SignCSR`). `barrier.RecoveryDelete` takes an
empty request on every transport and so has no carrier on QUIC: with the gate
on, it is **refused** over QUIC (fail-closed) and must be issued over another
transport. The embedded transport runs in process with no authenticated
caller and is not gated.

A PIN is read only from a TLS connection. The three refusal classes are the
same everywhere: the factor itself (no TLS, no PIN, wrong PIN) is
unauthenticated; a caller without the SO role is forbidden, even one RBAC
admits to the permission, such as a token principal granted it; a role store
that cannot answer, or an audit record that cannot be written, is unavailable
and retryable.

## Gated operations

The gate applies only to routes mapped to the permissions in
`pkg/auth/middleware.SOPermissions`:

- `ceremony:initialize`, `ceremony:enroll-custodian`,
  `ceremony:revoke-custodian`, `ceremony:sign-csr`
- `seal:rotate-root-key`, `seal:rekey`, `seal:generate-recovery`,
  `seal:initialize-shamir`

All other routes pass through untouched, including over plain HTTP —
the TLS requirement below is scoped to the SO surface only.

## Request contract

For a gated route, the caller must:

1. Connect over TLS (plain-HTTP requests to SO routes get
   `401 SO PIN requires TLS 1.3` before the header is read).
2. Authenticate via mTLS as an identity holding the SO role
   (otherwise `403 SO role required`). The role is recognised by
   identifier: one of the identity's role IDs (the user record's
   `RoleIDs`) must be the ID under which the REST server's role service
   (`rest.WithRoleService`) persists the `so` role in the caller's own
   tenant (`role.HoldsSystemRole`). The role *name* is never compared, so
   an identity whose roles carry the literal string `so` — for example a
   token claim for a subject with no user record — is refused. A role
   store that cannot answer, or no role service at all, refuses with
   `503 SO role could not be resolved`: the gate fails closed rather than
   guessing.
3. Present the PIN in the `X-QRDB-SO-PIN` header (missing →
   `401 SO PIN required`; wrong → `401 SO PIN verification failed`,
   with Argon2id verification against the per-tenant commit).

Every decision — missing PIN, invalid PIN, success — is emitted to the
compliance audit log with actor, tenant, resource, action, and client
IP.

## The record is part of the decision

**A gated request is refused when its SO-factor record cannot be written.** The
gate is fail-closed by default, on the operator's `auth.audit_failure_policy` —
the same single setting that governs the authorization decision one layer out.
There is no separate SO setting.

An SO operation is the most privileged action the system offers, and an
unrecorded second factor is exactly the event the trail exists to capture. Both
halves of the check propagate a failed audit write:

- `middleware.SOMiddleware`, for the route-level dual-factor record, and
- `auth.SOAuthenticator.VerifySOPIN`, for the PIN verification itself.

What the caller sees when the audit sink is down:

| Condition | Healthy sink | Sink cannot write, `fail-closed` (default) | Sink cannot write, `fail-open` |
|---|---|---|---|
| PIN missing | `401 SO PIN required` | `503`, body names the audit failure | `401` as usual |
| PIN wrong | `401 SO PIN verification failed` | `503`, body names the audit failure | `401` as usual |
| PIN correct | handler runs | `503`, handler does **not** run | handler runs, gap counted |

`503` and not `401`, because the credential is good and what failed is the
node: `retry` is the honest instruction, and telling an officer holding a valid
PIN that the PIN is wrong would send them to re-enrol. The refusal rides
`qrdb_audit_gate_failures_total{component="so-dual-factor"|"so-auth"}` and an
`ERROR` log line; it is **not** written to the sink that just failed.

**`SetSOPIN` and `RotateSOPIN` are not gated the same way.** Their own
completion records are post-commit — the hash is already replicated through
Raft — so a failed write there is logged and counted rather than propagated;
refusing would report a completed operation as failed. The *rotation* still
verifies the old PIN through the gate above before it commits anything.

## Chain position

The middleware is the innermost REST wrapper (directly around the mux),
so it runs after the auth middleware has resolved the caller's identity
into the request context:

```
CORS → scope → sealed-window → auth → SO dual-factor → mux
```

Interaction with the sealed window: the SO-gated operations that are
also on the sealed-window allowlist (e.g. `/v1/barrier/rekey`,
`/v1/ceremony/initialize`) remain reachable while sealed, and with the
flag enabled they then require the SO PIN — the sealed-window
middleware never bypasses the SO gate because it sits further out in
the chain.
