# Attested Caller — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-25

The gate has **no wire capability of its own** — no endpoint, RPC or tool to
configure or query it. It is an enforcement layer that changes the answer every
network transport gives for a **gated** permission when the calling peer is not
currently attested.

## What each transport answers on a refusal

| Transport | Refusal (any reason but `verifier-error`) | `verifier-error` |
|---|---|---|
| REST (HTTP/2, HTTP/3) | `403 Forbidden` | `503 Service Unavailable` |
| gRPC | `codes.PermissionDenied` | `codes.Unavailable` |
| gRPC over Unix socket | `codes.PermissionDenied` | `codes.Unavailable` |
| QUIC | permission denied | unavailable |
| MCP | tool error, not retryable | tool error, retryable |
| Embedded | not gated | not gated |

A refusal happens only **after** the request authenticated and RBAC granted it;
a caller without the permission gets the ordinary RBAC denial and no
attestation is attempted.

## The verifier contract

A composer supplies an `auth.PeerAttestationVerifier` (go-xkms provides one).
The gate calls `VerifyPeer(ctx, AttestedPeer{Identity, Certificate,
Thumbprint})` once per gated request. The verifier:

1. looks up cached evidence and the enrolled attestation key **by
   `Identity`** — never by anything in the request;
2. judges it without collecting anything new;
3. returns a `PeerAttestation` naming the **peer the evidence belongs to**
   (`PeerThumbprint`) and **when it was collected** (`CollectedAt`), with
   `Passed`, a `Failure` class (`Absence` or `Cryptographic` when refused,
   `None` when passed) and a `Reason`;
4. returns an **error** when it cannot decide — which the gate refuses.

## Audit record

| Field | Value |
|---|---|
| `action` | `auth.peer_attestation` |
| `result` | `denied` |
| `resource` | the gated permission |
| `subject`, `tenantID` | the authenticated peer |
| attributes | `reason`, `security_relevant` (`true`/`false`), `detail` |

The ordinary `auth.authorize` decision record is written as well, with result
`denied`.

## Versioning

Additive. The zero value of every configuration surface installs no gate, so
existing deployments are unchanged.
