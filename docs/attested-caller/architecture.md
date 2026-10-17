# Attested Caller — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-25
> Conflicts with this document are defects. Fix them here first.

An authorization gate that admits a request for a chosen set of operations only
from a peer whose **measured state** is currently acceptable — not merely one
that holds a valid certificate.

## Problem

A certificate proves who a peer is. It says nothing about whether the machine
holding the key is still in the state it was trusted in. A compromised host
keeps its valid mTLS key, so for operations where "is this caller running what
we think it is running" matters — a reporting server that dials nodes for
attestation quotes is the motivating case — certificate validity is not the
property that decides admission.

The check had to be reusable anywhere in the stack that needs "the requester is
a trusted, currently-attested node", and qrdb already owns the one
authorization funnel every transport routes through. A gate anywhere else would
be one more parallel authenticator.

## Shape chosen

**One gate, in the shared authorization funnel, after identity and after RBAC.**

```
mTLS handshake → authenticate (identity) → RBAC grants the permission
                                          → gated permission?  ── no ──→ admit
                                                    │ yes
                                   PeerAttestationVerifier.VerifyPeer
                                          (injected; go-xkms)
                                                    │
                          verdict checked by the gate → admit / refuse
```

- **It is an authorization gate, not an authenticator.** Identity ("who is
  this peer") and state ("is its state acceptable now") are different claims.
  Keeping them apart is what lets attestation be withdrawn — evidence goes
  stale, a measurement changes — without touching authentication.
- **It runs only on a request RBAC granted.** It is a second condition on
  admission, never a substitute for the first, and a denied request costs no
  verification.
- **qrdb owns the seam; go-xkms owns the verifier.** qrdb defines
  `auth.PeerAttestationVerifier`, the verdict it returns, and the gate that
  enforces it. Quote verification, attestation-key trust, PCR baselines and the
  evidence cache are go-xkms's (and the collector's); qrdb never parses
  evidence and imports none of it.
- **The policy is per permission.** `PeerAttestationPolicy.Permissions` names
  the operations that require an attested peer; every other operation is
  unaffected. A deployment can therefore require attestation for exactly the
  surface where it matters — including a capability module's operations, which
  carry permissions like any other.

### What the gate checks, independently of the verifier

The verifier renders a verdict; the gate does not take it on trust. It refuses
unless **all** of these hold:

| Check | Refusal reason | Security event? |
|---|---|---|
| The identity was not forwarded by another node on the caller's behalf | `forwarded` | no |
| The connection carries a verified client certificate | `no-peer-certificate` | no |
| The verifier returned a verdict and no error | `verifier-error`, `no-verdict` | no |
| The verdict is about **this** peer — its thumbprint is the connection certificate's | `peer-mismatch` | **yes** |
| The evidence has a collection time | `no-evidence` | no |
| That time is not in the future | `evidence-from-future` | **yes** |
| The evidence is no older than `MaxEvidenceAge` | `evidence-stale` | no |
| The verdict passed | `state-rejected` | **yes** when the verifier classed it cryptographic |
| A passing verdict names no failure | `inconsistent-verdict` | no |

The age bound is the gate's own, applied whatever the verifier did, so a
verifier configured with a looser bound cannot widen the gate's. The verifier is
handed the **authenticated** identity and the certificate on the connection,
and must look evidence up by that identity — never by anything the request
carries — which is why "evidence for a different peer" is detectable at all.

**Every refusal is fail-closed.** In particular a verifier error is a refusal,
never an admission; it is classed "unavailable" (503 / `UNAVAILABLE`) because
it is a statement about this node rather than the peer, and every other refusal
is "forbidden" (403 / `PERMISSION_DENIED`).

### Distinguishable in the trail

A refusal is audited as **its own record** — action `auth.peer_attestation`,
result `denied`, with `reason`, `security_relevant` and the verifier's `detail`
as attributes — beside the ordinary `auth.authorize` decision record. "The peer
is who it claims but its measured state is unacceptable" is a security event,
not a login failure, and an auditor must be able to tell them apart. It also
logs at `ERROR` when security-relevant and `WARN` otherwise. Writing that record
follows `auth.audit_failure_policy` like every other gate.

### Alternatives rejected

- **An authenticator in the chain** that produces an identity only for attested
  peers. Rejected by ruling: it would make "attested" unrevocable independently
  of "authenticated", and conflate two claims.
- **Fresh evidence per request.** Rejected: a quote per request is a TPM
  operation on the request path. Evidence comes from a background collector's
  cache; the gate takes a maximum age instead.
- **A per-transport gate.** Rejected: the gate sits in the one funnel the
  network transports share, so one installation covers all of them.
- **Implementing a verifier in qrdb.** Rejected by the layer ownership: qrdb is
  the software-only interface layer; attestation verification is go-xkms's.

## Transport coverage

| Transport | Gated | How |
|---|---|---|
| REST (HTTP/2, HTTP/3) | yes | `RESTMiddleware` → `authorize` |
| gRPC | yes | unary interceptor per call; stream interceptor once, when the stream opens |
| Unix socket | yes | the same gRPC interceptors |
| QUIC | yes | `AuthorizeQUIC` → `authorize` |
| MCP | yes | `AuthorizeMCP` → `authorize` |
| Tenant-targeted handlers on any of the above | yes | `AuthorizeTenant` → `authorize` |
| Embedded (in-process) | **no** | there is no peer: the caller is the process itself |
| etcd-compatible listener | **no** | it does not route through the auth middleware at all, so it never reaches `authorize` |
| MySQL and PostgreSQL wire frontends | **no** | they authenticate through the middleware but authorize with their own SQL privilege checks, never `authorize` |

The gate lives in `authorize`, so it applies exactly where `authorize` runs.
A deployment that must attest every caller of an operation has to keep that
operation off the three frontends in the last rows.

A transport that authenticates without a verified client certificate has no
peer to attest, so a gated operation reached that way is refused with
`no-peer-certificate`.

**`IdentityGated` routes are gated on their recorded permission only.** Such a
route is registered with the `permission.IdentityGated` sentinel and does its
real authorization in the handler (an officer registry lookup, a thumbprint
match). The gate sees the sentinel, so it applies to that route only if the
policy lists `permission.IdentityGated` itself — which gates every such route —
and never on the permission the handler checks internally. The tenant-barrier
bootstrap windows are the exception: they authorize through
`AuthorizeTenant` with the real barrier permission, and that is gated.

## Trust boundaries

- **The evidence is looked up by the authenticated identity**, and the verdict
  must name the connection's certificate. A peer cannot borrow another's
  evidence, and a request cannot choose whose evidence is consulted.
- **Forwarded requests are refused, first and explicitly.** A write a follower
  forwards to the leader arrives on the forwarding node's connection, so the
  certificate on it is the node's, not the original caller's, and no verdict
  about it could be a verdict about the caller. The gate refuses an on-behalf
  identity before asking the verifier anything, with its own reason
  `forwarded` — **not** a security event, because it is how a follower routes a
  write, not evidence of anything wrong. **Send gated writes to the leader
  directly**; a gated operation a follower serves itself (a read) is attested
  at that follower, against its own evidence cache. Trusting the forwarder's
  gate instead would need a deliberate decision.
- **gRPC streams are attested when they open, not per message.** The stream
  interceptor authorizes once, at stream start; a peer whose evidence goes
  stale during a long-lived stream keeps that stream until it closes. Unary
  RPCs and every other transport are checked per request. A gated operation
  that must be re-attested continuously belongs on a unary call.
- **Public routes are not gated.** They never reach authorization; a gated
  permission must be one that is authorized.

## What this feature deliberately does not do

- **No evidence collection, no quote verification, no baseline management.**
  Those are go-xkms's and the collector's.
- **No configuration-file surface.** The verifier is code injected by the
  composer, so the gate is composed in code too; see [cli.md](cli.md).
- **No gating of the embedded transport.**
