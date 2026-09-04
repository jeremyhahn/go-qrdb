# Certificate Authority — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-03

Issuing, exporting and revoking the client certificates that authenticate every
caller. Authentication is mTLS throughout the store, so this is the feature that
mints identity.

## Problem

Every transport authenticates with mTLS, so a client with no certificate cannot
do anything. Something has to issue them — and it has to do so without becoming
the place a production PKI is expected to live.

## Shape chosen

A narrow `CertProvider` interface with a **deliberately modest default
implementation**, and a composition seam for a real one.

```go
type CertProvider interface {
	GenerateClientCert(ctx context.Context, req *GenerateRequest) (*CertBundle, error)
	ExportPKCS12(ctx context.Context, certPEM, keyPEM []byte, passphrase string) ([]byte, error)
	RevokeCert(ctx context.Context, serial string) error
	IsRevoked(ctx context.Context, serial string) (bool, error)
	ListCerts(ctx context.Context, tenantID string) ([]*CertRecord, error)
	CACertPEM() []byte
}
```

`DefaultCertProvider` issues ECDSA P-256 client certificates with the Go
standard library, keeps the CA private key barrier-encrypted at rest and in
`memguard` while unsealed, and enforces revocation from replicated state on
every authenticated request.

**go-xkms provides the production alternative** — full CRL generation, OCSP,
HSM-backed CA keys (TPM2, PKCS#11, cloud KMS), extended certificate profiles,
certificate transparency logging — injected through `WithCertProvider`. **The
`CertProvider` interface is the boundary; go-qrdb never depends on go-xkms
directly.**

### Alternatives rejected

- **Shipping a full PKI here.** Rejected: CRL distribution, OCSP responders,
  profile management and CT logging are a product in themselves and they belong
  with the hardware crypto in go-xkms. What qrdb needs is enough CA to
  bootstrap its own mTLS.
- **A revocation *list* rather than a blocklist.** The default provider records
  revocation as a key per serial (`__sys:certs_revoked/{serial}`) and does not
  generate a CRL. That is a real limitation, and it is the main reason a
  deployment with external relying parties wants the go-xkms provider.
- **Leaving the CA key outside the barrier.** Rejected: the CA key is the most
  valuable secret in the system. It is barrier-encrypted at rest and destroyed
  in memory on seal.

## Why this CA exists alongside the product CA

**This is a BOOTSTRAP CA.** It mints the cluster-enrollment certificates a node
needs *before there is a cluster*, which means it cannot depend on the PKI it
bootstraps. go-qrdb never imports go-xkms, and this CA is what makes that
possible.

go-xkms ships the **product PKI** — RFC 5280 CRLs with reason codes, CRL
import, an OCSP responder, ACME revocation, HSM-backed CA keys. It already
depends on go-qrdb, so there is no rival datastore anywhere in the stack.

**Two CAs, two populations, and neither should ever answer for the other's
certificates.** This is recorded here because the duplication looks like
something to consolidate, and consolidating it would break cluster bootstrap:
the bootstrap CA would then need the very platform it exists to bring up.

### Why there is no CRL and no OCSP here

Deliberate, not an omission.

A CRL exists so a relying party that **cannot query the issuer** can still
check status offline. Every consumer of a certificate issued here is inside the
cluster's own trust domain, every node can query, and the query is a **local
replicated read** rather than a network round trip.

An OCSP responder would also import the classic availability dilemma —
responder unreachable means either fail open (accept revoked certificates) or
fail closed (outage) — which replicated state does not have.

Signing is the other half of the argument. A CRL must be signed by the CA key,
so generating CRLs here would put a signing key on every node for an artifact
nobody in this trust domain needs.

**The CA certificate carries `x509.KeyUsageCRLSign` and nothing generates a
CRL. That is deliberate.** The key usage records what the CA is permitted to
do; this deployment chooses not to.

## Where revocation is enforced

**On the authentication path, on every request — not in a TLS handshake
callback.**

A `tls.Config` `VerifyConnection` / `VerifyPeerCertificate` hook fires exactly
once, when the connection is established. On QUIC, where a client holds one
long-lived connection and multiplexes every request over it, a peer revoked
after the handshake would stay authorised for the life of that connection.
The authenticator runs per request, so a revocation takes effect while a
session is already open.

```
request -> AuthMiddleware -> MTLSAuthenticator
                               |
                               +-- RevocationChecker (bounded-TTL cache)
                               |     |
                               |     +-- CertProvider.IsRevoked -> replicated KV
                               |
                               +-- revoked?      -> 401, no identity produced
                               +-- lookup failed -> 503, retryable (fail CLOSED)
                               +-- barrier sealed -> admitted (see below)
```

**There are two enforcement points, not one**, because REST's on-behalf
forwarding path — a follower forwarding a write to the leader under a signed
assertion — verifies the forwarding node's certificate directly and never runs
the authenticator chain. Both apply the same policy from one definition
(`auth.EnforceRevocation`); wiring passes them the same checker, so they share
one cache and cannot drift. Without the second gate, revoking a compromised
cluster node's certificate would not stop it forwarding writes on behalf of any
user it names.

Three outcomes, and the two failure modes are deliberately different:

- **Revoked** — authentication fails. No identity is produced, so no
  authorization decision is even reached.
- **The lookup failed** — authentication fails **closed**, reported as a
  retryable unavailability rather than a credential rejection. Admitting a
  request whose revocation status is unknown would reinstate the exact defect
  enforcement exists to remove, during precisely the degraded conditions an
  attacker would choose.
- **There is no revocation state to read** — the request is **admitted**. The
  only producer is a sealed barrier, where the markers have not been
  decryptable since boot. Failing closed there would make the barrier
  *unsealable*, because the unseal endpoints are themselves reached over mTLS.
  What such a request can reach is separately confined to the sealed-window
  allowlist, which is not the certificate's data plane. This is a stated
  fail-open, bounded to a window in which the store is not serving data at all.

### The cache TTL is a security window

Consulting replicated state on every request is the strictest posture and the
default is not quite that: a NOT-REVOKED answer is cached for
`auth.revocation_cache_ttl` (default `30s`).

**That value is the maximum time a certificate that has just been revoked
continues to authenticate on a node that had already seen it.** Set it to `0`
to close the window entirely, at the cost of a key-value read per request.

A REVOKED answer is cached without expiry and is unaffected by the setting,
because revocation here is a one-way door (below) and therefore cannot become
wrong.

### Revocation is a one-way door

The revocation marker is a key-value row, and under a key-value row **delete
access is the power to silently un-revoke** — something a signed, monotonic CRL
has no analogue for.

Two properties close that gap, and neither is a key-range ACL: the `__sys:`
prefix is collision-avoidance and tenant confinement, not authorization, and no
prefix is exempt from tenant namespacing.

1. **Revoke is reachable only through the certificate service, under RBAC**
   (`certs:revoke`), on every transport. The key-value row is an implementation
   detail nothing outside `pkg/cert` touches.
2. **Nothing un-revokes.** There is no unrevoke operation on any transport, and
   the single code path that deletes a marker deletes only markers whose
   certificate has already expired.

### Deny-by-serial, and what bounds the namespace

Revoking a serial for which this node holds **no record** is a real CA
operation — the certificate may have been issued by a peer, or its record
pruned — and it is retained. The marker is written either way, and the call
succeeds either way.

That means a mistyped serial and a deliberate peer-issued revocation are
currently indistinguishable to the caller. It is a **reporting gap**, tracked
separately, and it is deliberately not closed by returning an error: both
outcomes are successes, so reporting either as an error would tell a caller
that a revocation failed while the marker is durable and enforcement is
already reading it.

The cost is that such a marker has no knowable expiry and is permanent. Markers
for certificates this node did issue carry the certificate's `notAfter` and are
**reaped once it passes**: an expired certificate fails validation regardless,
so dropping its marker cannot restore access. Every revocation reaps, which is
what bounds a namespace that would otherwise only grow.

## Cryptographic properties

| Property | Value |
|---|---|
| CA key algorithm | ECDSA P-256 |
| Client key algorithm | ECDSA P-256 |
| CA validity | 10 years |
| Default client validity | 1 year |
| Client `ExtKeyUsage` | **ClientAuth only** |
| Serial number | 128-bit random |
| PKCS#12 export | `software.sslmate.com/src/go-pkcs12` |

Client certificates carry `ClientAuth` and nothing else — a certificate issued
here cannot be used to impersonate a server.

## Identity mapping

The certificate **is** the identity:

- **CN** → the username.
- **OU** → the tenant ID.
- The **SPKI thumbprint** is what a user record binds to.

That is why issuance is a privileged operation and why revocation matters: there
is no password to change. Revocation is the only way to withdraw an identity
before its certificate expires, which is why it is enforced rather than merely
recorded — see "Where revocation is enforced".

## CA key lifecycle

The CA key follows the barrier seal/unseal lifecycle:

```
Bootstrap (barrier unsealed)
  ├─ Generate ECDSA P-256 CA key pair
  ├─ barrier.Encrypt(caKeyDER) → wrapped blob
  ├─ Store wrapped blob at __sys:cert_ca/{tenantID}/wrapped_key
  ├─ Store CA cert PEM at __sys:cert_ca/{tenantID}/cert_pem
  └─ Cache CA key in memory

Unseal
  ├─ Load wrapped blob from KVStore
  ├─ barrier.Decrypt(wrappedBlob) → caKeyDER
  └─ Cache CA key in memory

Seal
  ├─ memguard.WipeBytes(caKey) → zero memory
  ├─ nil CA key and cert references
  └─ All operations return BarrierSealedError

Generate Certificate (barrier unsealed)
  ├─ getCAMaterial() → copy of cached CA key + cert
  ├─ Generate ECDSA P-256 client key pair
  ├─ x509.CreateCertificate(CA signs client)
  ├─ Persist CertRecord to KVStore

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| `CertProvider` interface, `DefaultCertProvider` | `pkg/cert` | go-qrdb |
| At-rest protection of the CA key | `pkg/seal` — see [../barrier-seal/](../barrier-seal/) | go-qrdb |
| Certificate-to-user binding | `pkg/user` — see [../identity-rbac/](../identity-rbac/) | go-qrdb |
| Ceremony CA adapter | `pkg/ceremony` — see [../ceremony/](../ceremony/) | go-qrdb |
| CRL, OCSP, HSM-backed CA, profiles, CT | — | **go-xkms**, injected via `WithCertProvider` |

`ceremony.CeremonyCA` is satisfied by both `cert.CertProvider` and go-xkms's
`ca.TrustedCA` through thin adapters, which is what lets the ceremony run
against either.

## Trust boundaries

- **The CA private key is barrier-encrypted at rest** and lives in protected
  memory while unsealed. Sealing destroys it; every mutating operation then
  returns `BarrierSealedError`.
- **`getCAMaterial()` returns a copy** to prevent aliasing the cached key.
- **Client private key DER is wiped immediately after PEM encoding.**
- **`CACertPEM()` needs no barrier check** — the CA certificate is public data
  and is deliberately still served while sealed.
- Issuance, revocation and listing are separately permissioned
  (`certs:generate`, `certs:revoke`, `certs:list`).

## What this feature deliberately does not do

- **No CRL generation and no OCSP.** Deliberate — see "Why there is no CRL and
  no OCSP here" above. Revocation is enforced from replicated state, which
  works for every consumer inside the cluster trust domain. A deployment whose
  certificates are checked by **external** relying parties needs the go-xkms
  provider.
- **No unrevoke.** Revocation is a one-way door by construction, not by policy.
- **No ACME.** That is go-xkms.
- **No server certificate issuance as a product surface** — the daemon's own TLS
  material is a bootstrap concern, see [../firstboot/](../firstboot/).
- **No certificate renewal workflow.** Issue a new one and revoke the old.
