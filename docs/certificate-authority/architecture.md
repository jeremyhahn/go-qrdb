# Certificate Authority — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-10

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
	// ExportPKCS12 refuses with *CertRevokedError when certPEM's serial is revoked.
	ExportPKCS12(ctx context.Context, certPEM, keyPEM []byte, passphrase string) ([]byte, error)
	// RevokeCert reports what it found through RevokeResult, not the error
	// return: both a record match and a deny-by-serial revocation succeed.
	RevokeCert(ctx context.Context, serial string) (*RevokeResult, error)
	IsRevoked(ctx context.Context, serial string) (bool, error)
	ListCerts(ctx context.Context, tenantID string) ([]*CertRecord, error)
	CACertPEM() []byte
}
```

`DefaultCertProvider` issues ECDSA P-256 client certificates with the Go
standard library, keeps the CA private key barrier-encrypted at rest and
wiped (`secure.Wipe`) as soon as it is no longer needed in plaintext while
unsealed, and enforces revocation from replicated state on every
authenticated request.

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

**There are three enforcement points, not one.** Two of them are not the
authenticator, because two authorization paths verify a peer certificate
directly and never run the authenticator chain:

- **REST's on-behalf forwarding path** — a follower forwarding a write to the
  leader under a signed assertion. Without this gate, revoking a compromised
  cluster node's certificate would not stop it forwarding writes on behalf of
  any user it names.
- **The cluster key plane** — the routes a leader uses to deliver a dedicated
  tenant's root key to the follower nodes hosting its replicas. They are on the
  REST public allowlist on purpose, because they authorize by node identity
  plus the replicated placement table rather than by user RBAC, and that is
  exactly why the authenticator's check does not reach them. It is gated on
  BOTH sides: a receiving node refuses a delivery from a revoked peer, and a
  delivering leader refuses to seal a tenant root key to a revoked recipient.
  The second half matters more than it looks. HPKE recipient-binding guarantees
  that only the holder of the recipient key can open the envelope, so against a
  revoked node that guarantee works *for* the attacker: the delivery becomes a
  correct, verified disclosure to precisely the node the operator withdrew.

All three apply the same policy from one definition (`auth.EnforceRevocation`);
wiring passes them the same checker, so they share one cache and cannot drift.

Where such a gate is missing, what a revoked node retains is participation
rather than another node's keys — the placement gate still requires it to be
the assigned replica, recipient-binding still confines an envelope to its
intended target, and the freshness guard still bounds replay. Those residual
protections are why the cluster-key gap was ranked high rather than critical.
They are not a reason to leave an authorization sequence that cannot see a
withdrawn identity.

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
   (`certs:revoke`), on every transport, **and gated on the tenant that owns
   the serial** — see [Which tenant a certs call may act on](#which-tenant-a-certs-call-may-act-on)
   below. The key-value row is an implementation detail nothing outside
   `pkg/cert` touches.
2. **Nothing un-revokes.** There is no unrevoke operation on any transport, and
   the single code path that deletes a marker deletes only markers whose
   certificate has already expired.

### Deny-by-serial, and what bounds the namespace

Revoking a serial for which this node holds **no record** is a real CA
operation — the certificate may have been issued by a peer, or its record
pruned — and it is retained. The marker is written either way, and the call
succeeds either way.

That means a mistyped serial and a deliberate peer-issued revocation would be
indistinguishable to the caller if the distinction were reported through the
error return: both outcomes are successes, so reporting either as an error
would tell a caller that a revocation failed while the marker is durable and
enforcement is already reading it. Instead `RevokeCert` returns a
`RevokeResult` alongside `nil`, whose `Outcome` says which of the two
happened. A caller that sees deny-by-serial when it expected to match a
local record has the signal to double-check the serial, without either case
being reported as a failure.

The cost is that such a marker has no knowable expiry and is permanent. Markers
for certificates this node did issue carry the certificate's `notAfter` and are
**reaped once it passes**: an expired certificate fails validation regardless,
so dropping its marker cannot restore access. Every revocation reaps, which is
what bounds a namespace that would otherwise only grow.

## Which tenant a certs call may act on

A permission answers "may this caller do this kind of thing". It does **not**
answer "to whose certificates", and on this surface those are different
questions, because one process-wide `CertProvider` serves every tenant. Three
of the four certs verbs name their target **in the request**:

| Verb | Where the target tenant comes from | Gate |
|---|---|---|
| `certs:generate` | the request's `tenantID` | authorized against that tenant |
| `certs:list` | the request's `tenantID` | authorized against that tenant |
| `certs:revoke` | **resolved from the serial** | authorized against the owning tenant |
| `certs:export` | nowhere — none is named | none needed (see below) |

`certs:generate` and `certs:list` are checked against the tenant the request
names, not the tenant the caller is in. `certs:generate` matters most: the
`tenantID` becomes the certificate's Subject **OU**, and the mTLS authenticator
derives an identity's tenant from exactly that OU, so an unchecked cross-tenant
mint would hand the caller a CA-signed credential for another tenant rather
than merely misfiling a record.

**The target must be the caller's own tenant, for every caller.** The `certs:*`
verbs are `ScopeSelfStrict` (see
[../identity-rbac/engineering.md](../identity-rbac/engineering.md)): the
system-tenant rule does **not** lift them. A system-tenant Admin generates,
lists and revokes the system tenant's certificates and no one else's; a
certificate it minted for another tenant's user would authenticate as that
user. There is no platform provisioning path that mints for a tenant — each
tenant's own Admin issues its own users' certificates. The embedded transport,
which runs no authorization middleware, applies the same rule to the caller in
its context (`tenant.Confine`).

**BREAKING:** before this rule, `certs:generate`, `certs:list` and
`certs:revoke` were `ScopeSelf`, and a system-tenant identity could act on any
tenant's certificates.

**Issuance is not the only layer that has to hold.** The gate above is what
stops the certificate being minted; the certificate-binding gate in
[../identity-rbac/](../identity-rbac/architecture.md#what-a-certificate-actually-claims)
is what stops a minted one being believed. For an **enrolled** account — one
with at least one SPKI `CertBindings` entry — a certificate naming it that was
issued for a different key does not authenticate as it, whatever this CA
signed. That is what keeps the severity of a future unguarded issuance path at
"a bad certificate exists" rather than "an identity was handed out".

It holds only for enrolled accounts, which is why **issuing a client
certificate and enrolling it are two steps and both are the operator's**.
`GenerateClientCert` writes no binding: `POST /users/{id}/cert-bindings` does,
and until it runs the certificate authenticates on its CN and OU alone.

**Revocation does not unenrol a key, and is not meant to.** A binding is over
the `SubjectPublicKeyInfo`, so it outlives every certificate issued for that
key: revoking one certificate stops that certificate being trusted and leaves
the binding — and a re-issue for the same key authenticates again. That is the
correct behaviour for the common case, a routine rotation, where silently
dropping the binding would unenrol the very key being rotated. It also means
revocation is **not** the remediation for a compromised private key.
Withdrawing the binding is: `DELETE /users/{id}/cert-bindings/{thumbprint}`,
described in [../identity-rbac/api.md](../identity-rbac/api.md). The two are
separate operations answering separate questions, and an operator cleaning up
after a key compromise performs both.

`certs:export` needs no gate and deliberately has none: the caller supplies
both the certificate and the private key, the server holds neither, and the
archive that comes back contains only what the caller already had. There is
nothing for a tenancy check to protect. (The revocation check inside the export
is an operator signal for the same reason — it withholds nothing.)

### Revoke resolves its target, and the two authorities that follow

`RevokeCert` takes a serial, and a serial carries no tenant. So the target is
**resolved first**, through `TenantForSerial`, and the answer selects which
authority is required:

- **The serial resolves to an issued record.** The record names the owning
  tenant, and `certs:revoke` is checked against *that* tenant. A tenant admin
  revoking its own certificate is admitted; anyone reaching for another
  tenant's serial is refused — an identity in the system tenant included,
  because `certs:revoke` is `ScopeSelfStrict`.

- **The serial resolves to nothing.** This is deny-by-serial, and it requires
  `certs:revoke:cross`, which is cross-scope and therefore **system tenant
  only**.

The second rule is the substantive one. A serial that resolves to no record
names no tenant, so no tenant-local caller can show the certificate is theirs —
"revoke another tenant's serial" and "revoke a serial a peer issued" are the
**same request on the wire**, and the marker written is read by every tenant.
Admitting that on a self-scope permission means there is no cross-tenant
revocation the system can refuse. Deny-by-serial is retained in full; what
changed is that it is now recognised as the **CA-operator** capability it
always was, and the CA is a platform asset held by a single provider.

A resolution failure is **not** treated as "no owner". The tenancy of the
operation is unknown, and an unknown target is not an absent one, so the
request is refused as unavailable rather than routed into the deny-by-serial
branch — otherwise a store hiccup would silently widen the authority every
revocation runs under.

### One rule, four transports

The rule lives once, in the auth middleware, and REST, gRPC, QUIC and MCP all
call it. They reach the *same* process-wide provider, so a rule enforced on
three of four surfaces is a rule with a door beside it. In a deployment with no
authentication configured the gates are no-ops, matching every other
handler-level gate.

On MCP, `qrdb_cert_list` deserves a note: its `tenant_id` argument defaults to
the **system tenant** when omitted, so the least deliberate call an agent can
make is also the most privileged one. It is gated exactly like an explicit
cross-tenant request.

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

**A serial is unguessable, not secret.** 128 bits of randomness puts it beyond
enumeration, but it travels in the peer certificate of every mTLS connection
and in any CRL that carries it, and `certs:list` serves a tenant's serials on
request. So confidentiality of the serial is not, and was never, what keeps one
tenant from acting on another's certificate. The tenancy gates above are.

Client certificates carry `ClientAuth` and nothing else — a certificate issued
here cannot be used to impersonate a server.

### `go-pkcs12` is used in ONE direction: encode

The PKCS#12 dependency is reached from exactly one symbol in this repository,
and knowing which one is what makes its advisories answerable. Measured over
every `.go` file in the tree, tests included:

```
$ grep -rho "pkcs12\.[A-Za-z_0-9]*" $(find . -name '*.go' -not -path './.git/*') | sort | uniq -c
      1 pkcs12.Modern2023
```

`pkcs12.Modern2023` is the **encoder**, at `pkg/cert/pkcs12.go`, and
`ExportPKCS12` is its only caller. There is no `Decode`, `DecodeChain`,
`DecodeTrustStore` or `ToPEM` anywhere in this repository. **Nothing here ever
reads a PKCS#12 archive back**, so no attacker-supplied `.p12` bytes reach a
parser through this code path: the CA writes bundles, and the consumer that
imports one is somebody else's software.

That matters because the library's advisories are largely about the parsing
half. `GHSA-mpwr-8vm7-h73f` — "authentication bypass in Decode functions",
fixed in v0.7.2 — is the worked example: read against a caller that only
encodes it is **hygiene rather than exposure**, and the dependency is kept
current for the ordinary reason rather than because this repository was
exposed to that bug. The scanners disagreed about it for a measurable reason
and both were right: trivy flagged the package VERSION, govulncheck was silent
because the affected SYMBOLS are not in the call graph.

**Re-measure the grep rather than citing this paragraph.** The claim is about
the call graph on the day it was taken, and the first `DecodeChain` added to a
truststore-import feature invalidates it silently. The direction of use is a
property to check, not one to inherit. In particular, a clean `govulncheck` is
not in general evidence about a `trivy` finding — the call-graph argument above
is what settles this one, and settles only this one.

## Identity mapping

The certificate **is** the identity:

- **CN** → the username.
- **OU** → the tenant ID.
- **SPIFFE tenant SAN** → the tenant ID again, under the configured trust
  domain — present only when `auth.spiffe_trust_domain` is set.
- The **SPKI thumbprint** is what a user record binds to.

### Which derivation a generated certificate uses

The mTLS authenticator derives a tenant from the tenant-scoped SPIFFE URI SAN
(`spiffe://<trust-domain>/tenant/<tenant>/<kind>/<name>`) when one is present,
with the configured trust domain **pinned**, and from the first Subject OU
otherwise. What `GenerateClientCert` emits decides which of the two resolves
the certificates this CA issues:

| `auth.spiffe_trust_domain` | Certificate carries | Tenant resolves through |
|---|---|---|
| unset (the default) | CN and OU only — no SubjectAltName extension at all | the OU fallback |
| set | CN, OU, and `spiffe://<trust-domain>/tenant/<tenant>/user/<username>` | the pinned SAN |

The first row is byte-for-byte the certificate this CA issued before the SAN
existed, so a deployment that never configured a trust domain is unaffected.
The second row is what makes the pin a live control for this CA's own
certificates: the same certificate presented to a node pinned to a different
trust domain is refused, fail-closed, and does not fall back to its OU.

**The kind is `user` and the name is the CN.** Every certificate this CA issues
authenticates as a user-store account named by its CN; node certificates are a
separate shape (`spiffe://<trust-domain>/node/<id>`) minted by the daemon's
node-certificate path and never by this one.

**Neither of qrdb's CAs mints a certificate whose SAN and OU disagree.** The
built-in CA's `GenerateClientCert` and the ceremony CA's `SignCSR` both refuse
one; a certificate issued by a CA outside qrdb is outside this guarantee, and
the authenticator's trust-domain pin is what bounds those. For
`GenerateClientCert` the SAN is built,
then parsed back with the same parser the authenticator applies to a presented
certificate, and it must return exactly the tenant, kind and name that were
asked for — before the CA key is touched. A path separator in the tenant or the
username, or a trust domain that is not a bare host, fails that and is refused
with `*SPIFFEIdentityError`; nothing is signed and nothing is recorded. The
reason is not tidiness: a certificate whose SAN named one tenant and whose OU
named another would authenticate as different tenants depending on whether the
node it reached had a pin set.

**The ceremony CA checks what a CSR asks for.** `SignCSR` copies a CSR's URI
SANs into the certificate it signs, so a tenant SPIFFE SAN in the CSR would
become a tenant claim that CA vouches for. A malformed tenant SPIFFE ID, or one
naming a tenant other than the Subject's first OU (or any tenant when the
Subject has no OU), is refused with `*ceremony.CSRTenantSANError` and nothing is
signed. Other URIs — node identities, other schemes — are signed as before.

**The trust domain is validated when configuration loads.** A value that does
not survive being written into a SPIFFE ID and parsed back (`/`, `?`, `@`,
whitespace) fails the load with a validation error on `auth.spiffe_trust_domain`
rather than making the CA refuse every certificate at issue time.

**Rotation is unaffected.** A user's binding is over the SubjectPublicKeyInfo,
which the SAN is not part of, so a key enrolled under a SAN-less certificate
keeps authenticating when it is re-issued with the SAN added.

**The provider and the authenticator must read the same value.** Both come from
`auth.spiffe_trust_domain`; a node whose CA minted under one domain while its
authenticator pinned another would refuse every certificate it issued.

The OU line is why `certs:generate` is gated on the tenant the request names
rather than on the caller's own: minting a certificate for a tenant is minting
a credential that authenticates as that tenant. See
[Which tenant a certs call may act on](#which-tenant-a-certs-call-may-act-on).

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
  ├─ secure.Wipe(caKey) → zero memory
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
