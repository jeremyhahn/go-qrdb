# Shared-CA-key enrollment does not work over the Noise transport

> **Status: ARCHIVE — historical record. Do NOT build from this document.**
> Last verified: 2026-10-13 · resolved; the owner ruled the cluster CA private key is never distributed.

**Status:** resolved. The owner ruled that the cluster CA private key is
never distributed, on any transport — the Noise handler's behaviour
described below was the correct one, and SPKI, DANE and Direct now match it,
because the advertiser and `/v1/ca/bootstrap` serve the certificate only.

**Member-side CSR issuance has since shipped** (see
[clustering/architecture.md](clustering/architecture.md#member-side-csr-join-key-a-cluster-ca)):
a joiner fetches the anchor certificate exactly as this document describes,
generates its own key pair, and sends a CSR; an existing, unsealed member
signs it with the replicated cluster CA key (Key A) and returns the leaf.
The CA private key still never crosses the wire in either direction.
`clusterenroll.Join` no longer refuses unconditionally with
`EnrollmentUnavailableError` once a join token is configured. The rest of
this document is kept as the record of the defect that motivated the
"never distribute the key" ruling in the first place.

## Symptom

A node that enrolls with `clusterenroll.Join` (or `ResolveCA` with bootstrap
flags) against a first node started by `Resolve` path 5 fails with:

```
clusterenroll: TLS bootstrap failed: clustertls: key parse failed (CA private key):
no EC PRIVATE KEY block found in PEM data
```

## Mechanism

The two ends agree; the transport between them does not.

- `Resolve` path 5 starts `StartAdvertiser`, whose `clusterBundleProvider.CABundle()`
  serves the CA **certificate concatenated with the CA private key** — the
  shared-CA-key enrollment mode.
- `Join` calls `clustertls.GenerateLeafFromCA`, which requires exactly that
  cert+key bundle so the joining node can mint its own leaf.
- In between, go-truststrap's Noise handler
  (`pkg/noiseproto/bootstrap/handler.go`, `handleGetCABundle`) parses the
  provider's bundle with `parsePEMBundle` — which decodes **only `CERTIFICATE`
  blocks** — and then rebuilds the response PEM from those parsed certificates.
  The `EC PRIVATE KEY` block is structurally dropped.

The stripping is arguably correct for a trust-bootstrap protocol: distributing
a CA **signing key** to every joining node is a strong claim to make, and the
Noise handler declines to make it. The defect is that qrdb's advertiser and
joiner were built assuming it does.

Verified 2026-08-15 by fetching from a live path-5 advertiser: the bundle came
back as a single 407-byte `CERTIFICATE` block and nothing else.

## Scope

Transport-specific. The other bootstrap paths return the served body verbatim
and therefore DO carry the key:

| Path   | Preserves the CA key | Where |
|--------|---|---|
| Noise  | **no** — rebuilt from parsed certs | `noiseproto/bootstrap/handler.go` |
| SPKI   | yes — raw body | `truststrap/spki.go` |
| Direct | yes — raw body | `truststrap/direct.go` |

`pkg/clusterenroll` tests now cover successful enrollment over the SPKI path
(`join_spki_test.go`), which is why the package's `Join` coverage is real
rather than error-path only.

## Why it was not caught

`Join` had **0% test coverage** until 2026-08-15. Nothing exercised the
advertiser and the joiner against each other.

## Options (resolved: option 1, extended)

Owner decision — this was an architecture question, not a bug fix. The
ruling took option 1 and added the missing half: anchor mode is canonical,
`Join`-over-Noise's shared-CA-key shape is removed, and the joining node's
leaf is now issued through a member-side CSR-sign exchange rather than left
unspecified.

1. **Anchor mode is canonical.** `pkg/clusterenroll/anchor.go` already
   implements anchor-only distribution (`StartAnchorAdvertiser` +
   `FetchAnchor`), where the joining node keeps its own key and gets a leaf
   issued rather than receiving the CA key. If that is the intended multi-node
   path, the shared-CA-key advertiser on `Resolve` path 5 is the thing to
   change, and `Join`-over-Noise should be removed rather than repaired.
2. **Keep shared-CA-key, change the transport contract.** Teach truststrap's
   Noise handler to pass a bundle through unmodified (or add an explicit
   full-bundle request type). This makes the CA signing key a routine
   over-the-wire artifact, which deserves a deliberate decision.
3. **Keep shared-CA-key, restrict the transport.** Document that shared-CA-key
   enrollment requires SPKI or Direct, and make `Resolve` path 5 refuse to
   advertise a cert+key bundle over Noise instead of serving one that will be
   silently truncated.

Until one is chosen, multi-node enrollment over Noise with the shared-CA-key
advertiser does not work, and no test asserts that it does.
