# Attested Caller — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-25
> Conflicts with this document are defects. Fix them here first.

## What it is

Some operations should only be performed by a machine that is not just
*authorized* but *currently in a trusted state* — its measured boot and software
state recently attested and found acceptable. The attested-caller gate enforces
that: for the operations a deployment names, a request is admitted only when
the peer's latest attestation evidence is fresh, belongs to that peer, and
passed verification.

It is off unless the product you run turns it on; a stock `qrdbd` gates
nothing. Products built on qrdb (for example those that include go-xkms's
attestation verifier) enable it for the operations that need it.

## What a caller sees

| You see | It means |
|---|---|
| `403` / `PERMISSION_DENIED` naming `no-evidence` or `evidence-stale` | your machine has no recent attestation on record. Let the attestation collector run, then retry. |
| `403` / `PERMISSION_DENIED` naming `state-rejected` | your latest evidence was checked and refused — for example a measurement does not match the baseline. This is recorded as a security event. |
| `403` / `PERMISSION_DENIED` naming `peer-mismatch` or `evidence-from-future` | the evidence on record does not belong to the certificate you connected with, or is dated after now. Recorded as a security event. |
| `403` / `PERMISSION_DENIED` naming `no-peer-certificate` | you did not connect with a client certificate, so there is no peer to attest. |
| `403` / `PERMISSION_DENIED` naming `forwarded` | you sent a gated write to a follower, which forwarded it to the leader under its own certificate. Send gated writes to the leader. |
| `503` / `UNAVAILABLE` naming `verifier-error` | the node could not reach a verdict. Retry; if it persists, the node's attestation configuration needs attention. |

You only ever see these for an operation you are **otherwise permitted** to
perform: the gate runs after the ordinary permission check.

## For operators

**Tell a gate refusal from an ordinary denial** in the audit trail: a gate
refusal has its own record with action `auth.peer_attestation`, and attributes
`reason` and `security_relevant`. Filter on `security_relevant=true` for the
events that warrant investigation; `no-evidence` and `evidence-stale` are
operational (the collector is behind), not attacks.

**Send gated writes to the leader.** A write a follower forwards to the leader
arrives with the follower's certificate, not the original caller's, and is
refused with reason `forwarded` (not a security event). A gated read a follower
serves itself is attested at that follower.

**Long-lived gRPC streams are checked when they open.** Evidence that goes stale
mid-stream does not end the stream; close and reopen it to be re-checked.

**The in-process embedded interface is not gated** — there is no remote peer to
attest.

## Troubleshooting

**Everything gated returns `evidence-stale`.** The evidence collector has not
refreshed within the gate's maximum age. Check the collector, not qrdb.

**Everything gated returns `verifier-error`.** The verifier is failing — often
a missing trust anchor or baseline in its policy. It is refused rather than
admitted by design.

**A gated call works on one node and not another.** The gate runs on the node
that received the request, with that node's evidence cache. Check the evidence
each node holds for the caller.
