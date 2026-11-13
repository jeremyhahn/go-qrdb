# Certificate Authority — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-26
> Conflicts with this document are defects. Fix them here first.

Issuing, enrolling and revoking the client certificates that authenticate every
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
	// IssueClientCert certifies req.PublicKey; it never generates a key.
	IssueClientCert(ctx context.Context, req *IssueRequest) (*IssuedCert, error)
	// RevokeCert reports what it found through RevokeResult, not the error
	// return: both a record match and a deny-by-serial revocation succeed.
	RevokeCert(ctx context.Context, serial string) (*RevokeResult, error)
	IsRevoked(ctx context.Context, serial string) (bool, error)
	// TenantForSerial reports the tenant owning a serial's record; found ==
	// false is the deny-by-serial case.
	TenantForSerial(ctx context.Context, serial string) (tenantID string, found bool, err error)
	ListCerts(ctx context.Context, tenantID string) ([]*CertRecord, error)
}
```

`DefaultCertProvider` issues ECDSA P-256 client certificates with the Go
standard library, stores the CA key like every other replicated system
record — no additional per-record encryption of its own; this node's own
at-rest storage encryption, when configured, protects the on-disk copy,
exactly as for the cluster CA (Key A) — never holds a client's private key, and
enforces revocation from replicated state on every authenticated request.

**go-xkms provides the production alternative** — full CRL generation, OCSP,
HSM-backed CA keys (TPM2, PKCS#11, cloud KMS), extended certificate profiles,
certificate transparency logging — injected through `WithCertProvider`. **The
`CertProvider` interface is the boundary; go-qrdb never depends on go-xkms
directly.**

### No private key leaves the server, because none is made there

Every client certificate the issuance verbs and tenant onboarding produce is
for a key its holder generated:

- **Issue** (`certs:issue`) signs a certificate signing request. The CSR's own
  signature proves the requester holds the key; nothing else in it is used —
  the certificate's Subject and SAN name the account the issuer names.
- **Enrol** (`certs:enroll`) takes a certificate the requester already holds,
  issued by an outside CA the tenant registered as a trust anchor (see
  [../trust-anchors/](../trust-anchors/architecture.md)), and verifies it
  chains there and names exactly that account.
- **A tenant's first admin** supplies one of the two at tenant creation (see
  [../multi-tenancy/](../multi-tenancy/architecture.md)).

Both verbs act on an **existing** account of the caller's own tenant and bind
the certificate's SPKI thumbprint to it in the same call, so an issued
certificate is never left bound to nobody: if the binding fails after a
certificate was signed, that serial is revoked.

The server never generates a client's private key. A server that generates keys holds every key it hands out, if only
for an instant, and a response that carries one is a credential in transit.

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
  valuable secret in the system. The barrier gates whether this process will
  touch it at all — every Signer/Certificate call checks `IsSealed()` first —
  and the in-memory key is destroyed on seal, even though the stored record
  itself carries no additional encryption of its own (see "Trust boundaries"
  below).

## Why this CA exists alongside the product CA

**This is a BOOTSTRAP CA.** It mints the cluster-enrollment certificates a node
needs *before there is a cluster*, which means it cannot depend on the PKI it
bootstraps. go-qrdb never imports go-xkms, and this CA is what makes that
possible.

go-xkms ships the **product PKI** — RFC 5280 CRLs with reason codes, CRL
import, an OCSP responder, ACME revocation, HSM-backed CA keys. It already
depends on go-qrdb, so there is no rival datastore anywhere in the stack.

**Two CAs, two populations, and neither should ever answer for the other's
certificates.** The two are not consolidated: the bootstrap CA must not need
the platform it exists to bring up.

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
protections are not a reason to leave an authorization sequence that cannot see
a withdrawn identity.

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
questions, because one process-wide `CertProvider` serves every tenant. Where
the target comes from differs per verb:

| Verb | Where the target tenant comes from | Gate |
|---|---|---|
| `certs:issue`, `certs:enroll` | **the caller's own tenant** — no request names one | authorized against it |
| `certs:delegate` | **the caller's own tenant**, never the system tenant | authorized against it; refused for a delegated caller |
| `certs:list` | the request's `tenantID` | authorized against that tenant |
| `certs:revoke` | **resolved from the serial** | authorized against the owning tenant |

`certs:list` is checked against the tenant the request names, not the tenant
the caller is in. Issuance takes no tenant at all: the tenant becomes the
certificate's Subject **OU**, and the mTLS authenticator derives an identity's
tenant from exactly that OU, so a cross-tenant mint would hand the caller a
CA-signed credential for another tenant rather than merely misfiling a record.
`certs:issue` and `certs:enroll` therefore always act on the caller's own
tenant, and the account they name must exist there.

**The target must be the caller's own tenant, for every caller.** The `certs:*`
verbs are `ScopeSelfStrict` (see
[../identity-rbac/engineering.md](../identity-rbac/engineering.md)): the
system-tenant rule does **not** lift them. A system-tenant Admin issues,
lists and revokes the system tenant's certificates and no one else's; a
certificate it minted for another tenant's user would authenticate as that
user. The one platform path that certifies a key for another tenant is tenant
creation, and only for that tenant's first admin, from the admin's own CSR or
certificate (see [../multi-tenancy/](../multi-tenancy/architecture.md)); after
that, each tenant's own Admin issues its own users' certificates. The embedded
transport, which runs no authorization middleware, applies the same rule to the
caller in its context (`tenant.Confine`).

**Issuance is not the only layer that has to hold.** The gate above is what
stops the certificate being minted; the certificate-binding gate in
[../identity-rbac/](../identity-rbac/architecture.md#what-a-certificate-actually-claims)
is what stops a minted one being believed. For an **enrolled** account — one
with at least one SPKI `CertBindings` entry — a certificate naming it that was
issued for a different key does not authenticate as it, whatever this CA
signed. That is what keeps the severity of a future unguarded issuance path at
"a bad certificate exists" rather than "an identity was handed out".

It holds only for enrolled accounts. The issue and enrol verbs write the
binding themselves, in the same call. `IssueClientCert` — the provider
primitive, which an embedder may call directly — writes none: a certificate
issued that way authenticates on its CN and OU alone until
`POST /users/{id}/cert-bindings` binds it.

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

PKCS#12 packaging has no server verb and so no gate: `qrdb ca export-bundle`
runs locally on a certificate and key the caller already holds, and contacts no
server.

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
revocation the system can refuse. Deny-by-serial is retained in full, as the
**CA-operator** capability it is, and the CA is a platform asset held by a
single provider.

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

## Delegations to a service principal

A **delegation** is a short-lived client certificate a tenant's own login CA
(its Key B, which may be hardware-held through `WithCertKeyProviderFactory`)
issues to an embedding application's service principal. It names the
principal and the permissions it may exercise inside the tenant in a
**critical** X.509 extension, in the spirit of RFC 3820 proxy certificates and
SPIFFE SVIDs, and lives at most 24 hours (one hour by default). What the
authenticator does with one is described in
[../identity-rbac/](../identity-rbac/architecture.md#tenant-issued-delegations).

Decisions, and why:

- **The one mechanism.** A delegation certificate is the only way an
  embedder's service principal acts in a tenant's scope; no option, context
  variant or fallback does the same.
- **Only the tenant mints one.** `certs:delegate` is tenant-only: it acts on
  the caller's own tenant, the system tenant is refused, and so is a caller
  that is itself authenticated by a delegation, except a renewal within a
  standing policy the tenant wrote. The issuer is that tenant's
  Key B and no other key, so no platform identity — not a system-tenant Admin,
  not a node — can produce a certificate the tenant did not ask for: a node
  mints (`Node.MintDelegation`) only within such a policy, and deleting it stops
  every further mint and renewal. See
  [../identity-rbac/architecture.md](../identity-rbac/architecture.md#standing-delegation-policy).
- **A named, registered extension.** The critical extension is `tp-delegation`,
  OID `1.3.6.1.4.1.29377.101.24`, under the company's IANA Private Enterprise
  Number certificate-extension arc (29377.101).
- **The requester's key, as for issue.** The application sends a CSR; the CA
  certifies its key and takes nothing else from it. The Subject is
  `CN=qrdb-service:<principal>`, `OU=<tenant>`, so a delegation can never be
  mistaken for, or resolve to, a user account.
- **Audited before it is signed.** The `cert.delegate` event is written to the
  tenant's trail first; a node that cannot write it signs nothing.
- **Withdrawn through ordinary revocation.** A delegation is an issued
  certificate with a record like any other, listed by `certs:list` and revoked
  by serial through `certs:revoke`; the per-request revocation check above
  applies to it as to any other certificate.
- **No new token format and no new crypto.** The extension is DER read and
  written with `golang.org/x/crypto/cryptobyte`; signing is `crypto/x509` through the same
  `cakey.Provider` every other issuance uses.

## Cryptographic properties

| Property | Value |
|---|---|
| CA key algorithm | ECDSA P-256 |
| Client key | the requester's own: ECDSA P-256/384/521, RSA of at least 2048 bits, or Ed25519 (`cert.CheckClientKey`); the CLI generates ECDSA P-256 |
| CA validity | 10 years |
| Default client validity | 1 year |
| Client `ExtKeyUsage` | **ClientAuth only** |
| Serial number | 128-bit random |
| PKCS#12 encoding (client-side `ca export-bundle`) | `software.sslmate.com/src/go-pkcs12` |

**A serial is unguessable, not secret.** 128 bits of randomness puts it beyond
enumeration, but it travels in the peer certificate of every mTLS connection
and in any CRL that carries it, and `certs:list` serves a tenant's serials on
request. So confidentiality of the serial is not what keeps one
tenant from acting on another's certificate. The tenancy gates above are.

Client certificates carry `ClientAuth` and nothing else — a certificate issued
here cannot be used to impersonate a server.

### `go-pkcs12` is used in ONE direction: encode

Only `EncodePKCS12` (`pkg/cert/pkcs12.go`, through `pkcs12.Modern2023`) uses
go-pkcs12, and only the `qrdb` client calls it, locally. Nothing in this
repository decodes PKCS#12, so no `.p12` bytes reach a parser through this
code: the library's decode-side advisories are hygiene here, not exposure.
Adding a decode changes how its advisories apply.

## Identity mapping

The certificate **is** the identity:

- **CN** → the username.
- **OU** → the tenant ID.
- **SPIFFE ID SAN** → the tenant ID again, as its trust domain:
  `spiffe://<tenant>/user/<username>`. Every certificate carries it.
- The **SPKI thumbprint** is what a user record binds to.

### How an issued certificate names its tenant

A tenant is a SPIFFE trust domain. The mTLS authenticator requires two names
and requires them to agree: the first Subject OU, and the trust domain of the
workload SPIFFE URI SAN (`spiffe://<tenant>/<kind>/<name>`, kind `user` or
`svc`). There is no fallback in either direction. The node's own trust domain,
`auth.cluster_trust_domain` (`qrdb.cluster` when unset), is the cluster's: a certificate that names
no tenant is under it, and no tenant may be it. A certificate with the SAN but
no OU under any other trust domain, with an OU but no SAN, whose OU names the
cluster trust domain, or whose OU and trust domain name different tenants
(which is how a foreign trust domain presents) is refused, fail-closed
(`*auth.CertificateTenantError`, which matches `auth.ErrInvalidCertificate`).

Every certificate either of qrdb's CAs issues therefore carries
`CN=<username>`, `OU=<tenant>` and `spiffe://<tenant>/user/<username>`. The
issuer still needs the cluster trust domain, to refuse a tenant equal to it:
an issuer handed an empty or invalid one refuses before the CA key is touched
(`*cert.SPIFFEIdentityError`, wrapping `spiffe.ErrTrustDomainEmpty` or
`spiffe.ErrTrustDomainInvalid`), and one asked to issue for a tenant equal to
it refuses with `auth.ErrTenantIsClusterTrustDomain`.

**The kind is `user` and the name is the CN.** Every certificate this CA issues
authenticates as a user-store account named by its CN; node certificates are a
separate shape (`spiffe://<trust-domain>/node/<id>`, no OU) minted by the
daemon's node-certificate path and never by this one.

**Neither of qrdb's CAs mints a certificate whose SAN and OU disagree.** Both
the built-in CA's `IssueClientCert` and the ceremony CA's `SignCSR` build the
SAN from the same tenant and username that fill the OU and CN; a certificate
issued by a CA outside qrdb is outside this guarantee, and the authenticator's
agreement check and cluster trust-domain checks are what bound those. For
`IssueClientCert` the SAN is built only from a tenant that is a SPIFFE trust
domain name and a username that is one SPIFFE path segment, the same rules the
authenticator's parser applies to a presented certificate — before the CA key
is touched. A tenant that is not a trust domain name, or a path separator in
the username, is refused with `*SPIFFEIdentityError`; nothing is signed and
nothing is recorded.

**The ceremony CA takes nothing from a CSR but its key.** The ceremony CA is
the node's cluster CA — the CA the mTLS listener trusts for client logins — so
whatever Subject an officer certificate carries is a login claim. It is
therefore never taken from the CSR. Every signing path (system, tenant and
recovery officer registration, `ceremony sign-csr`, and the initial M-of-N
ceremony) names the registered holder and the registering scope's tenant in
`ceremony.SignCSROptions`, and the CA issues `CN=<username>`,
`OU=[<scope tenant>, <profile>]` and exactly one Subject Alternative Name,
`spiffe://<scope tenant>/user/<username>` — the CSR's own
URI, DNS, IP and e-mail SANs are dropped. Each OU is its own RDN, so the
tenant stays the first OU whatever its length. A trust domain or name that does
not make a valid SPIFFE ID is refused before signing
(`*ceremony.CertSubjectError`, field `SubjectAltName`). The ceremony then
re-checks every certificate a `CeremonyCA` returns — Subject, that its SANs are
exactly that one URI, and that it certifies the CSR's own key — and refuses one
that differs
(`*ceremony.CertSubjectError`), so a CA composed in from outside qrdb is held
to the same contract.

**A holder certificate is never a user login.** A certificate whose OU carries
a holder profile (`security-officer` or `custodian`, in any position) is marked
by the mTLS authenticator (`auth.HolderProfile`), and the authentication
middleware never resolves it to a user account: it authenticates with no roles
and reaches only the identity-gated ceremony surface, where the handler binds
it by certificate thumbprint. An officer registered as `admin` therefore does
not become the tenant's `admin` user, binding policy or not.

**The mark survives leader forwarding.** A follower that forwards a holder's
request to the leader signs the holder profile into the on-behalf assertion
(`forward.CallerAssertion.HolderProfile`), and the leader re-marks the
identity, so it resolves to no account there either. An assertion naming a
profile the leader does not recognise is refused. A holder request is never
forwarded unsigned — an unsigned forward is authorized as the forwarding node
— so a follower that cannot sign the assertion refuses it with 502
(`UnsignedForwardRefusedError`).

**The trust domain is validated when configuration loads.** A value that does
not survive being written into a SPIFFE ID and parsed back (`/`, `?`, `@`,
whitespace) fails the load with a validation error on `auth.cluster_trust_domain`
rather than making the CA refuse every certificate at issue time.

**Rotation keeps the binding.** A user's binding is over the
SubjectPublicKeyInfo, which the SAN is not part of, so a key re-issued under the
same CN and tenant keeps authenticating. A certificate without the SAN does not
authenticate: it must be re-issued, and the re-issued certificate
binds the same key.

**The provider and the authenticator must read the same value.** Both come from
`auth.cluster_trust_domain`, the one trust-domain setting; a node whose CA minted under one domain while its
authenticator pinned another would refuse every certificate it issued.

The OU line is why issuance acts only on the caller's own tenant: minting a
certificate for a tenant is minting a credential that authenticates as that
tenant. See
[Which tenant a certs call may act on](#which-tenant-a-certs-call-may-act-on).

That is why issuance is a privileged operation and why revocation matters: there
is no password to change. Revocation is the only way to withdraw an identity
before its certificate expires, which is why it is enforced rather than merely
recorded — see "Where revocation is enforced".

## CA key lifecycle

The CA key follows the barrier seal/unseal lifecycle. The barrier GATES
every step below — IsSealed() is checked first, and every operation
returns BarrierSealedError while sealed — but does not ENCRYPT the stored
record itself; see "The CA key
provider seam" below and "Trust boundaries" for what actually protects it
at rest:

```
Bootstrap (barrier unsealed)
  ├─ Generate ECDSA P-256 CA key pair
  ├─ Encode the key as the "material" record and the self-signed cert as
  │  the SEPARATE "certificate" record (see "The CA key provider seam",
  │  below, for why these are two records, not one combined one)
  ├─ kvStore.PutAllIfAllUnchanged(__sys:cert_ca/{tenantID}/material,
  │  __sys:cert_ca/{tenantID}/certificate) -- ONE atomic guarded batch,
  │  guarded on BOTH being absent
  └─ Seal the key into protected memory (secure.Secret); cache the cert in the clear

Unseal (load-only; auto-establishes on a cold cache with nothing persisted
yet -- see Initialize)
  ├─ Load BOTH the material and certificate records from KVStore
  ├─ Decode the key from material, the cert from certificate
  └─ Seal the key into protected memory (secure.Secret); cache the cert in the clear

CertificateOnly (the handshake / trust-decision path -- IsTenantIssuer,
CACertPEMs' per-tenant load)
  ├─ Read ONLY the certificate record; the material record is never
  │  fetched, let alone parsed, and NOTHING is ever generated here
  └─ Cache the cert; a cold store with neither record yet answers
     *NotInitializedError, never a freshly-minted CA

Seal
  ├─ Destroy the cached secure.Secret outright (wipes the sealed key;
  │  the cached certificate, public data, is NOT cleared)
  └─ All operations return BarrierSealedError

Issue Certificate (barrier unsealed)
  ├─ getCAMaterial() → this tenant's own crypto.Signer + cert, resolved fresh
  ├─ x509.CreateCertificate(CA signs the requester's public key)
  ├─ Persist CertRecord to KVStore
  └─ Return IssuedCert
```

## The CA key provider seam (`pkg/cakey`)

go-qrdb is embedded as the
datastore and consensus engine under go-xkms and other projects that bring
their own key custody (software, TPM 2.0, PKCS#11, cloud KMS). Neither CA in
this repository may hard-code where its key lives: both obtain their signing
key **only** through one qrdb-defined interface, `cakey.Provider`.

```go
package cakey

type Provider interface {
	// Signer returns a crypto.Signer backed by the CA's private key. A
	// hardware or remote backend (HSM, TPM, KMS) routes Sign to the device;
	// the key itself never enters this process.
	Signer(ctx context.Context) (crypto.Signer, error)

	// Certificate returns the CA's own certificate (public data).
	Certificate(ctx context.Context) (*x509.Certificate, error)
}
```

Every signing path in this repository reduces to "get a signer, get the CA
cert, call `x509.CreateCertificate`", and each takes a `cakey.Provider`:

- `ceremony.NewProviderCA(p)` builds the `ceremony.CeremonyCA` behind
  `SignCSR` and `IssueCertificate`; it resolves signer and certificate from
  `p` on every call, so a re-seal, a re-unseal or an HSM failover is
  reflected immediately.
- `cert.DefaultCertProvider` holds one `cakey.Provider` per tenant
  (`cakey.SoftwareProvider` by default, or one from an injected factory), and
  its `getCAMaterial` returns a `crypto.Signer`, not key bytes.
- `clusterca.IssueLeafForKey(ctx, provider, pub, ...)` issues node leaves
  and signs member-side cluster-join CSRs (see
  [../clustering/architecture.md](../clustering/architecture.md)).

**Never return or export a CA private key; only sign through the Signer.**
`cakey.Provider` is the enforcement point.

### `cakey.SoftwareProvider` — the built-in backend

`cakey.SoftwareProvider` is what runs when nothing is injected: an ECDSA
P-256 key, persisted through `kvstore.MultiGuardedBatchKVStore` — qrdb's
atomic multi-key batch capability, not the Raft log specifically — under a
prefix the caller supplies. It backs BOTH CA keys: **Key A, the cluster CA**,
under `__sys:cluster_ca/` — no tenant, one per cluster — and **Key B, the
per-tenant login CA**, one `cakey.SoftwareProvider` instance per tenant
under `__sys:cert_ca/{tenantID}/`, resolved and cached lazily by
`pkg/cert.DefaultCertProvider.resolveKeyProvider`. Both read and write the
identical record shape this section describes. For an isolated tenant the Key B records are in the
tenant's home store rather than the system store, and only the public CA
certificate is published to the system store, at
`__sys:cert_ca_pub/{tenantID}/certificate` (see `engineering.md`, "Storage
schema", and `docs/multi-tenancy/`).

**Every node serves every CA operation from the replicated Key A.** Key A
is not the property of the node that generated it: its records under
`__sys:cluster_ca/` replicate to every member, and once a node's barrier is
unsealed its `ClusterCAProv` reads them, so holder enrolment, `sign-csr`,
CSR-signed admin certificates, node leaves and member-side join signing are
served on any member. Reads are local; a write (the first adoption of the
key) goes through the Raft leader like every other system-shard record. A
node started on operator-supplied TLS certificates (`--tls-ca-file`,
`--tls-cert-file`, `--tls-key-file`) has no Key A and so no CA: it serves
every operation that only verifies a certificate and refuses only the ones
that sign. Holder enrolment refuses with `ErrCANotInitialized`
(`FAILED_PRECONDITION`); `sign-csr` refuses earlier, because such a node's
ceremony never reaches the `enrolling` state that verb requires.

The key and certificate are two records (`{prefix}material` for the private
key, `{prefix}certificate` for the public CA certificate), written together
in one atomic batch (`kvstore.MultiGuardedBatchKVStore.PutAllIfAllUnchanged`,
guarded on both being absent). `Certificate(ctx)` on a cold cache reads and
parses only the certificate record; the private-key record is fetched only
when something calls `Signer`. The guarded batch makes "does either record
already exist" and "write both" one indivisible step, so concurrent
generation (several nodes racing to become the cluster's first) converges on
exactly one CA, and a crash or a write failure cannot leave one record
without the other.

The `crypto.Signer` `Signer` returns is opaque (`cakey.barrierCheckedSigner`
internally): it cannot be type-asserted back to `*ecdsa.PrivateKey`, its
`Public()` returns a deep copy rather than a pointer into the live key, and
it re-checks both the barrier's sealed state AND the provider's own `Seal`
generation on every `Sign` call — a `Signer` obtained before either `Seal`
(an explicit, provider-local key revocation, independent of the barrier)
or an external reseal refuses on its next `Sign`, even though the caller
never goes back through the provider to find out.

**The record carries no encryption layered on by this package.** Key A's
record is replicated and every node's barrier is its own, so a record
wrapped with the writing node's `seal.EncryptionBarrier` could be decrypted
by no other node. What protects Key A is
the same thing that protects every other system-shard record: this node's
own at-rest storage encryption when one is configured (`pkg/seal/encfs`,
transparent below `kvstore.KVStore`), cluster mTLS while Raft replicates the
write, and nothing at all under seal strategy `none` — see
[../backup-restore/architecture.md](../backup-restore/architecture.md#ca-key-material)
for what that means for a backup, and that document's [Trust
boundaries](../backup-restore/architecture.md#trust-boundaries) section for
why `backup:create`/`backup:read` are full access to this key material, not
a narrower "backup data" scope, and belong only to fully trusted operators.
`seal.EncryptionBarrier` is a constructor argument that `SoftwareProvider`
consults only for `IsSealed()`: CA material is generated, adopted, signed
with, or read only while unsealed.

Because `kvstore.MultiGuardedBatchKVStore` is the same interface in
standalone mode (the local store, backed by the same in-process
transaction plane every other guarded store in this tree uses) and Raft
mode (the replicated store), `SoftwareProvider` has no mode-specific
branch and is tested against both, including two providers with two
INDEPENDENT barriers over one shared store -- standing in for two cluster
nodes with their own, unrelated at-rest encryption -- both reading the
same CA.

### Injection

An embedder supplies its own CA keys through `sdk/go/node` options:

- `WithClusterCAKeyProvider(cakey.Provider)` — replaces Key A: one key,
  cluster-wide, no tenant concept.
- `WithCertKeyProviderFactory(cakey.ProviderFactory)` (also a `pkg/server`
  `ServerOption` of the same name) — replaces Key B:
  `pkg/cert`'s login CA. **Not a bare `cakey.Provider`** — see
  `cakey.ProviderFactory`, below, for why Key B's seam is asymmetric with
  Key A's.

**What `WithClusterCAKeyProvider` actually wires, precisely, per
composition root.** The option itself flows into exactly one place,
`lifecycle.Infra.ClusterCAKeyProvider`, and `lifecycle.BuildServices`
(shared by every composition root: `cmd/qrdbd`, `sdk/go/node`, the embedded
SDK) sets `Node.ClusterCAProv` from it unconditionally. Two of the three
capabilities named by this seam follow from THAT alone, for every caller:

- **The ceremony CA** — `ceremony.NewProviderCA(ClusterCAProv)` replaces the
  bootstrap-window `StaticProvider` CA via `ReplaceCA` once `ClusterCAProv`
  exists, so `ceremony-sign-csr` signs through the injected provider.
- **Node-leaf issuance** — whatever in that composition root calls
  `clusterca.IssueLeafForKey` reads the signing key through `ClusterCAProv`,
  the same seam.

**Member-side CSR join signing is the third capability, and
`lifecycle.StartClusterJoin` wires it, not `lifecycle.BuildServices`.**
`cmd/qrdbd`'s `start` and a clustered `sdk/go/node` node both call it with
the node's `ClusterCAProv`. With a cluster CA key it records the node's own
identity as admitted and, when join is enabled, starts the join listener
(`clusterenroll.StartJoinServer`), contributes the `/v1/enroll/join` route
(`server.WithClusterJoin`, through `ClusterJoin.ServerOptions`) and binds the
admin join-token verb (`service.ClusterJoinTokenMinter`) to the store the
listener consumes tokens from (see
[../clustering/architecture.md](../clustering/architecture.md#join-token-minting)).
Without a cluster CA key (operator-supplied TLS) there is nothing to sign a
joiner's CSR with, and an enabled join is not started.

**Two options, not one role-parameterized option.** Key A and Key B are
separate trust domains (a leaked login CA must not mint node
certificates); one option accepting a "role" argument would let a caller
wire the *same* provider instance to both roles by mistake, silently
collapsing the separation the two-key design exists to hold. Two distinctly
named options make that mistake require two separate, deliberate lines.

**The two options are not symmetric in shape, either, and that is deliberate.** Key A has no tenant concept — one cluster, one key — so
`WithClusterCAKeyProvider` takes a bare `cakey.Provider`. Key B is
per-tenant BY DEFINITION: the built-in path already constructs one
`cakey.SoftwareProvider` per tenant (its own storage prefix, its own
Subject), and an injected backend must do the same, one key per tenant, or
the issuer-tenant binding (`pkg/clienttrust.Pool.VerifyIssuer`, via
`cert.CertProvider`'s `IsTenantIssuer`) becomes vacuous: every tenant's
presented certificate would verify against the SAME injected CA, so
`IsTenantIssuer` would answer true for whichever tenant a certificate's
Subject OU merely CLAIMED, regardless of which tenant the signing key was
actually provisioned for. `WithCertKeyProviderFactory` therefore takes a
`cakey.ProviderFactory` — `ProviderForTenant(tenantID) (Provider, error)`
plus `TenantIDs(ctx) ([]string, error)` for `CACertPEMs`' own enumeration —
not a `cakey.Provider`, and this package deliberately offers no adapter
that lifts a single `Provider` into a `ProviderFactory` by ignoring the
tenant argument: that shortcut is the defect this seam's shape exists to
make impossible to reach by accident, not a convenience to preserve. An
HSM/KMS-backed factory holds one key per tenant and resolves it by tenant
ID, exactly like the software provider.

When a provider (or, for Key B, a provider resolved from the factory) is
injected, qrdb's built-in `SoftwareProvider` for that key is never
constructed, never generates a key pair, and never reads or writes
`__sys:cluster_ca/`/`__sys:cert_ca/{tenantID}/` — the key is wherever the
injected provider says it is, and qrdb stores at most a certificate.

### `cakey.Adapter` — the no-glue seam for an xkms backend

An xkms-backed CA already implements `pkg/ca.CASigningIdentity.Signer(ctx)
(crypto.Signer, error)` and `pkg/ca.CACertificateAccessor.Certificate(ctx,
attrs) (*x509.Certificate, error)` (closed over its own `types.KeyAttributes`
for the one key this CA cares about). `cakey.Adapter` composes exactly those
two method values into a `Provider`:

```go
provider := cakey.Adapter{
	SignerFunc:      xkmsCA.Signer,
	CertificateFunc: func(ctx context.Context) (*x509.Certificate, error) {
		return xkmsCA.Certificate(ctx, issuingKeyAttrs)
	},
}
```

go-qrdb never imports go-xkms — the fit above is verified in `pkg/cakey`'s
own tests against a same-shaped local double, not against the real xkms
type.

### Bootstrap crash window (Key A, first node)

Key A must exist before the cluster — and before any store — does. The
first node:

1. Generates Key A in memory.
2. Uses it immediately to sign its own node leaf and, if the initial M-of-N
   ceremony runs before the barrier exists, the first officers' CSRs — via a
   `cakey.StaticProvider` wrapping the in-memory signer, so this step goes
   through the same `Provider` seam as every later signature rather than a
   separate code path.
3. Once the barrier is unsealed, the first signing call against the
   replicated `cakey.SoftwareProvider` (constructed with
   `cakey.WithAdoptMaterial`) adopts this same in-memory key into the store
   if it holds none yet, or loads whatever a prior boot already persisted.
   This happens lazily, on first use, rather than synchronously at startup:
   in Raft mode the replicated store is not readable for a brief window
   right after the shard starts (no leader elected yet), so reading it
   eagerly at construction would race that window on every boot, not only
   the bootstrap one.

If the node crashes between steps 1 and 3, no node certificate has been
issued under the lost key yet (the only thing it has signed is this node's
own bootstrap leaf, regenerated on restart), so the next boot regenerating
a fresh Key A is safe: it is a retry, not a rollback.

### Signing only while unsealed

`cakey.SoftwareProvider.Signer`/`Certificate` return `*BarrierSealedError`
while sealed — there is no "always decryptable" exemption, because the
replicated store itself cannot be opened while sealed (encfs). The one
exception is the in-memory `StaticProvider` window in step 2 above, which
holds no store or barrier reference at all.

This is why `ceremony-sign-csr` (`sealedwindow.VerbCeremonySignCSR`) is
`PreOperationalOnly`, the same flag `officer-register` already carries: its
anonymous, sealed-window exemption is justified only before the init
ceremony completes, while Key A is still the in-memory `StaticProvider`.
After the ceremony is operational, `SignCSRInit` (`pkg/ceremony/ceremony.go`)
refuses every call outright, on every transport, regardless of seal state or
session: it only accepts a call while the ceremony is still `enrolling`, so
there is no post-unseal path to mint a new officer CSR this way.
**Verifying a certificate an officer already holds needs only the CA
certificate**, which is public and already part of every node's cached TLS
identity — so an existing officer can always unseal; minting a *new*
CSR-signed holder certificate through this verb is only possible during the
ceremony, never after.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| `cakey.Provider`, `cakey.SoftwareProvider`, `cakey.StaticProvider`, `cakey.Adapter` | `pkg/cakey` | go-qrdb |
| `CertProvider` interface, `DefaultCertProvider` (Key B, over a `cakey.Provider`) | `pkg/cert` | go-qrdb |
| Cluster CA (Key A, over a `cakey.Provider`) | `pkg/clusterca`, `pkg/clustertls`, `pkg/clusterenroll` — see [../clustering/](../clustering/) | go-qrdb |
| At-rest protection of the CA key | `pkg/seal` — see [../barrier-seal/](../barrier-seal/) | go-qrdb |
| Certificate-to-user binding | `pkg/user` — see [../identity-rbac/](../identity-rbac/) | go-qrdb |
| Ceremony CA adapter (`LocalCA` over raw PEM, `ProviderCA` over `cakey.Provider`) | `pkg/ceremony` — see [../ceremony/](../ceremony/) | go-qrdb |
| CRL, OCSP, HSM-backed CA, profiles, CT | — | **go-xkms**, injected via `WithCertProvider` / `WithClusterCAKeyProvider` / `WithCertKeyProviderFactory` |

`ceremony.CeremonyCA` is satisfied by `ceremony.LocalCA` (raw PEM, used only
for the pre-store `StaticProvider` window), `ceremony.ProviderCA` (any
`cakey.Provider`), `cert.CertProvider`, and go-xkms's `ca.TrustedCA`, through
thin adapters, which is what lets the ceremony run against any of them.

## Key B trust on the client-facing listeners

A Key B-issued certificate authenticates to this node's own mTLS listeners
on every transport. Key B's CA certificates are generated lazily, inside a
sealed barrier, per tenant, so a client CA pool built once at TLS setup
(`ClientCACertPool`, Key A plus operator-configured client CA files) cannot
hold them; `test/integration/api/cli_tenant_identity_ca_test.go` asserts the
login.

The trust pool is therefore dynamic, not seeded at boot, and it is ONE
implementation (`pkg/clienttrust`) every composer wires rather than three
copies of the same logic:

- **Client-facing listeners (REST, gRPC, Unix, QUIC, MCP)** verify a
  presented client certificate against Key A **and** every tenant's Key B CA
  certificate, read via `DefaultCertProvider.CACertPEMs` into a trust
  snapshot the pool rebuilds in the background (below).
  `clienttrust.Pool.Wire` installs `tls.Config.GetConfigForClient` on the
  composer's own `*tls.Config`, reading its `ClientCAs` field fresh on every
  connection (not captured at Wire time) as the Key A baseline;
  `clienttrust.Pool.SetKeyBProvider` installs the live `cert.CertProvider`
  once it exists (after the first unseal), so a Key B CA created — or
  rotated to an HSM-backed one via `WithCertKeyProviderFactory` — after the
  listener started is picked up at the next rebuild, not only at boot.
  `GetConfigForClient` returning `(base, nil)` — the unmodified base
  config, never `nil` — keeps the original, static (Key A-only) config
  whenever the current snapshot carries no widened pool: `base` is `cfg`
  itself when `Wire`'s caller installed no prior `GetConfigForClient` hook
  (the ordinary case), or whatever a composed PRIOR hook itself returned,
  so that hook's own effect is never discarded on this path. This is what
  happens before the first rebuild after `SetKeyBProvider`, and whenever
  the last rebuild's `CACertPEMs` refused with `BarrierSealedError` — the
  ordinary outcome while sealed. A `CACertPEMs` read that fails for any
  other reason (a replicated read that runs out of time on a busy node) says
  nothing about the CA set, so the rebuild keeps the previous snapshot's
  trust across it, provided that snapshot granted widening over the same
  static pool and cluster CA. The kept snapshot keeps its build time, so
  the three-period age limit below still withdraws it when Key B reads keep
  failing. A chain refused meanwhile is refused as trust state unavailable
  (a retryable 503), not as an invalid certificate.
  - **The pool and the issuer-tenant binding are the SAME object.**
    `clienttrust.Pool` is both the TLS-layer widening decision and the
    authentication-layer binding check (`IsClusterCA`, `VerifyIssuer` —
    see "Issuer-tenant binding"). `SetClusterCA` installs the resolver BOTH
    halves check — every rebuild resolves it for the TLS half, and the
    authentication layer resolves it fresh on every call
    — but its absence is not one thing: a deployment with no cluster-CA
    concept at all (`SetClusterCA` never called, or called with `nil`)
    still trusts Key B once `SetKeyBProvider` is called, gated purely by
    the per-tenant check (`VerifyIssuer`'s `IsTenantIssuer` branch, which
    needs no cluster CA to operate — only the cluster-CA EXEMPTION does);
    a resolver that WAS installed and is CURRENTLY failing (a reseal, a
    transient store failure) withdraws Key B trust entirely from the
    snapshot that rebuild publishes, until a later rebuild succeeds —
    never stickily. The composer's obligation — unconditionally
    wiring this SAME pool as the `auth.IssuerVerifier` at the
    authentication layer, regardless of whether a cluster CA happens to
    exist — is what keeps Key B from being trusted at the TLS layer with
    the binding unenforced at the authentication layer. `Pool` cannot verify that obligation for itself;
    see `SetKeyBProvider`'s own doc comment.
  - **The handshake never reads storage, and the rebuild can never
    generate a CA.** `GetConfigForClient` only loads an
    immutable snapshot from an `atomic.Pointer` and, at most, clones a
    pool; `Pool.Run`, started once by the composer for the life of the
    process, builds that snapshot from storage. A storage read held up by
    a leader election (the replicated read waits up to 10 seconds for a
    leader) would outlast the handshake's 5-second deadline, so the
    handshake returns at once with the last snapshot built. The
    rebuild's own read is bounded as a second line of defence
    (`refreshTimeout`, 2 seconds, well inside the handshake deadline). The
    cluster-CA half of the rebuild
    (`ceremony.ProviderCA.CACertificate`) prefers
    `cakey.CertOnlyProvider.CertificateOnly` when the wrapped provider
    offers it, so trust-pool maintenance can never establish a self-issued
    cluster's Key A as a side effect — every composer instead calls the
    `lifecycle.EstablishClusterCA` once, deliberately, during startup.
  - **The snapshot is rebuilt on every invalidation and every 2 seconds**
    (`poolCacheTTL`). The handshake pays none of the cost of scanning and
    parsing every tenant's CA certificate.
    `Invalidate` — fired by the `CertProvider`'s tenant-deletion watch and
    by trust-anchor changes — schedules a rebuild without blocking its
    caller, and any number of invalidations before the rebuild runs
    collapse into one. A newly created or rotated Key B CA with no
    invalidation behind it is picked up within the 2-second period.
    `SetKeyBProvider`, `SetClusterCA` and `SetTrustAnchors` withdraw the
    widened pool at once and schedule a rebuild, so a provider or
    cluster-CA swap never serves trust read from the previous one; a
    rebuild that was already reading when a setter ran is discarded rather
    than published (compare-and-swap). A snapshot older than three periods
    is treated as withdrawn, so a stopped rebuild loop, or one whose Key B
    reads keep timing out, falls back to the static baseline instead of
    serving old Key B trust indefinitely.
  - **Every listener-specific TLS override composes with
    `clienttrust.ReassertListenerTLS`**, not a bare clone-and-mutate:
    `GetConfigForClient`'s returned config REPLACES the connection's
    effective `tls.Config` wholesale, so REST/gRPC/Unix/MCP's TLS 1.3
    `MinVersion` floor and QUIC's exclusive ALPN protocol pin would
    otherwise be silently lost on any connection that takes the dynamic
    (widened) path, as a TLS 1.2 downgrade or an ALPN mismatch.
    `ReassertListenerTLS` clones the
    base config, applies the override, and — when the base already carries
    a dynamic hook — wraps it so the SAME override is reasserted on
    whatever config the hook returns.
  - **qrdbd**: `TLSBundle.wireClientTrustPool`/`SetKeyBProvider`/
    `SetClusterCA` (`cmd/qrdbd/tls.go`) delegate to `clienttrust.Pool`
    directly; `TLSBundle.TrustPool()` exposes the same instance as the
    `auth.IssuerVerifier` `bootstrapAuthEnforcement` wires into the auth
    middleware, and `runStart` starts its `Run` loop under the process's
    signal context.
  - **sdk/go/node**: `wireClientAndServer` builds ONE `clienttrust.Pool`,
    calls `SetClusterCA`/`SetKeyBProvider` on it, starts its `Run` loop
    under the node's background context, passes it as the
    `auth.IssuerVerifier` to `wiring.BuildAuthMiddleware`, and later calls
    `Wire` on whatever `*tls.Config` the embedder supplied via
    `server.WithTLS` (resolved with `server.ResolveTLSConfig`) — the SAME
    pool object, not two independently-constructed ones. An embedder that
    instead relies on `WithCeremonyCA` auto-provisioning (no explicit
    `WithTLS`) has no config to wire this onto; that composition's
    listeners keep whatever posture `ceremony.ServerTLSConfig` and the auth
    middleware already give them.
  - **`qrdb_embedded`** (the Wails binding, `cmd/qrdb/bindings_server.go`)
    opens no network TLS listener at all — only the in-process embedded
    transport — so there is nothing for this seam to widen there.
  - A real login proof across all five client-facing transports lives in
    `sdk/go/conformance/key_b_login_test.go`
    (`TestKeyBLogin_AuthenticatesOnEveryRemoteTransport`): a certificate
    issued through a node's own Key B `CertProvider`, with NO client CA
    seeded into the listener ahead of time, authenticates and completes an
    authorized call.
- **The on-behalf cluster-forwarding path is gated the same way.** A
  follower's forwarded write is authenticated by `forward.ClusterPeer`,
  which checks only that the peer certificate carries a node-scoped SPIFFE
  SAN — a SHAPE check, not an issuer check. With every tenant's Key B CA
  trusted on the same listener, a Key B leaf carrying a forged node SPIFFE
  SAN would otherwise pass that check and present an honored assertion AS
  THE CLUSTER. `AuthMiddleware.authenticateOnBehalf` additionally requires
  the verified chain's direct issuer to satisfy `IssuerVerifier.IsClusterCA`
  — the SAME `clienttrust.Pool` the listener's TLS layer and the mTLS
  authenticator use — before honoring the assertion, failing closed when no
  verifier is wired.
- **The peer/Raft/enrollment transport trusts Key A only, never Key B.** It
  never shares the client-facing listeners' `*tls.Config`: `initializeNode`
  (`cmd/qrdbd/start.go`) constructs `quicraftconfig.MTLSConfig` directly from
  the TLS bundle's `CACertPEM` field (Key A) and hands it to
  `buildHostConfig`, so a Key B leaf (a tenant login certificate) presented
  there is refused without any extra code -- the two keys are separate trust
  domains at the transport layer, not only at the issuance layer.
  `cmd/qrdbd/peer_trust_domain_test.go`
  (`TestPeerTransport_TrustsClusterCAOnly_KeyBLeafRefused`) reproduces the same
  `ClientCAs`/`ClientAuth` shape over real `crypto/tls` and proves a Key B
  leaf is refused there while a Key-A-signed node leaf is accepted.

## Issuer-tenant binding

Trusting every tenant's Key B CA on ONE shared listener raises a question: a
presented
certificate verifies against SOME CA in the pool, but which one, and does it
match the tenant the certificate's own Subject OU (or SPIFFE SAN) claims?
Without an answer, tenant A's Key B CA could mint a leaf naming tenant B in
its OU, and it would authenticate as B — the dynamic pool verifies the
signature, not which tenant the signing CA was ever issued for.

`auth.IssuerVerifier` — a two-method interface (`IsClusterCA`,
`VerifyIssuer`) implemented by `clienttrust.Pool`, the SAME object that
widens the listener's TLS `ClientCAs` — closes this. It is checked in two
places, because the chain's trust anchor is only reliably available in one
of them from the TLS handshake state and has to be threaded separately into
the other:

- `MTLSAuthenticator.Authenticate` calls `verifyIssuer`, which tries EVERY
  verified chain `tls.ConnectionState.VerifiedChains` reports (a TLS stack
  can present more than one when multiple roots in the pool could validate
  the same leaf) and succeeds if ANY chain's direct issuer (`chain[1]`, the
  certificate that actually signed the leaf) is ONE OF THREE things:
  1. the cluster CA (Key A — which legitimately signs holder and node
     certificates for any tenant, checked first and exempted via
     `IsClusterCA`);
  2. already present in the listener's OWN static `ClientCAs` pool —
     whatever the composer put there directly via its own `WithTLS`, BEFORE
     Key B ever widened it (`Pool.IsStaticallyTrusted`). This is
     pre-existing trust the composer already explicitly granted (an
     operator-supplied additional client CA file, or a deployment/test
     harness establishing real mTLS identity through its own CA,
     independent of Key A/Key B entirely); Key B's dynamic widening is
     additive to it, and the binding exists to police THAT addition, not
     to retroactively impose tenant-matching on a CA trusted before Key B
     was ever in the picture. The static pool is captured ONCE, at `Wire`
     time — a fixed snapshot of THIS listener's own configuration, never
     resampled per connection or per handshake, so one connection's view of
     a concurrently-mutated config can never leak into another's
     authentication decision. Scoped tightly in a second way too: the
     exemption is refused outright when the issuer is ALSO a registered Key
     B CA for some tenant, regardless of which — an operator (or a test
     harness) that accidentally adds a tenant's Key B CA to the static
     `ClientCAs` must not thereby grant that CA a blanket, any-tenant pass;
     it stays scoped to its own tenant via the ordinary per-tenant check
     below, exactly as if the static pool had never contained it;
  3. the Key B CA **registered for the tenant the leaf's own Subject
     names** (`VerifyIssuer`, which reads
     `cert.DefaultCertProvider.IsTenantIssuer` — a byte-identical DER
     comparison, not a Subject-field comparison, and LOAD-ONLY: a tenant
     with no established CA answers false, never auto-creating one just to
     answer the probe).

  Failing those, the chain may contain one of **that tenant's registered
  trust anchors** at any depth above the leaf, which is why
  `VerifyIssuer(ctx, tenantID, chain)` takes the whole verified chain rather
  than the issuer alone. See [../trust-anchors/engineering.md](../trust-anchors/engineering.md).

  A mismatch is `*auth.IssuerTenantMismatchError`, matching
  `ErrInvalidCertificate` like `CertificateTenantError`.
- `AuthMiddleware.authenticateOnBehalf` calls `IsClusterCA` directly against
  every verified chain's direct issuer — see "the on-behalf cluster-forwarding
  path is gated the same way" above — because that path never runs the
  authenticator chain at all, and so does not inherit the check above.

`pkg/auth/wiring.BuildAuthMiddleware` — the single constructor `cmd/qrdbd`
and `sdk/go/node` both call — wires the SAME `IssuerVerifier` into both
consumers from one `IssuerVerifier` config field: a composer passes its
`clienttrust.Pool` (already populated via `SetClusterCA`/`SetKeyBProvider`
for the TLS layer), and `BuildAuthMiddleware` installs it as
`auth.WithIssuerVerifier` on the mTLS authenticator AND as
`middleware.WithIssuerVerifier` on the on-behalf path. **This is NOT purely
additive**: wiring the pool at all makes `VerifyIssuer` fail closed for any
certificate whose issuer is neither the cluster CA nor a Key B CA the
provider can confirm — including, deliberately, a certificate signed by an
operator-supplied additional client CA that is neither — which is why the
pool must be wired consistently with whatever the listener's TLS layer
actually trusts (see `SetClusterCA`/`SetKeyBProvider` above). A `nil`
`IssuerVerifier` (a composer with no cluster CA resolved at all) leaves the
check unenforced on both paths — but that is the ONLY case left
unenforced, not every case lacking a Key B provider.

A composer's own handler mounted on a node listener reaches the same pool
through the SDK, never through a verifier it builds itself and never by
trusting `PeerCertificates[0]`: `node.Node.IssuerVerifier()` returns the
node's `clienttrust.Pool` as an `sdk.IssuerVerifier`
(`node.ErrTrustPoolNotWired` before boot builds it and after `Close`), and
`sdk.ResolveVerifiedChains(ctx, state, verifier)` — `auth.ResolveVerifiedChains`
re-exported — returns the chain the pool verified for a connection whose
`VerifiedChains` the `ClientAuth` remap left empty. It refuses with
`sdk.ErrNoPeerCertificate` (no certificate), `sdk.ErrInvalidClientCertificate`
(one the pool does not trust) or `*sdk.TrustStateUnavailableError` (refused
while the pool could not read its trust state; retry).

## Trust boundaries

- **Each tenant's CA key is stored like every other replicated system
  record — no additional per-record encryption of its own** (see "Trust boundaries" in the
  backup-restore feature for what actually protects it). The IN-MEMORY cache is a different
  matter: whenever established, the key lives in a `secure.Secret`
  (go-enclave protected memory, AES-256-GCM under the process Sealer),
  never a bare `*ecdsa.PrivateKey`; `Signer` opens it into an mlocked
  `secure.Buffer` for exactly the duration of one `Sign` call and destroys
  the Buffer before returning. Sealing destroys the `Secret` outright (not
  the cached certificate, public data); every mutating operation then
  returns `BarrierSealedError`.
- **`getCAMaterial()` resolves the SPECIFIC tenant's CA directly** through
  the `cakey.Provider` it just resolved (`prov.Signer`/`prov.Certificate`),
  never through any shared, process-wide cache. `IssueClientCert`, its
  caller, holds no lock across the call, so two tenants' calls can
  interleave; returning anything other than THIS resolution's own answer
  would risk handing one tenant's caller a different tenant's CA.
- **The built-in per-tenant CA key is never auto-created by a read.**
  `IsTenantIssuer` — reached on the authentication path for whatever tenant
  a presented leaf CLAIMS to be, including tenants that do not exist — is
  LOAD-ONLY: `loadOnlyCertificate` calls the built-in
  `cakey.SoftwareProvider`'s `CertificateOnly` (cert-only, load-only --
  WITHOUT ever creating one and WITHOUT loading or decoding the private
  key; see `cakey.CertOnlyProvider`), never `Certificate` (which
  auto-establishes a brand-new CA the first time none exists) and never
  `Unseal` (which loads the key record into memory alongside the
  certificate one, on every call), so a tenant with no established CA
  answers `false`, never minting one as a side effect of being asked about.
  `Initialize`'s own auto-init-on-first-use path is safe under concurrency
  without any lock of `pkg/cert`'s own: `cakey.SoftwareProvider.Initialize`
  writes its two records through `PutAllIfAllUnchanged`, the guarded batch
  that makes "does it already exist" and "write it" one indivisible step,
  so two callers racing the first-ever issuance for a brand-new tenant
  converge on exactly one CA rather than each writing its own candidate.
- **No client private key exists on the server.** `IssueClientCert` is handed
  a public key; the private half never reaches it.
- **`CACertPEMs()` checks the barrier explicitly** and refuses with
  `BarrierSealedError` while sealed: outside Raft mode the plaintext
  records are reachable even while sealed, and a runtime reseal must make
  the dynamic client-auth trust pool drop Key B at its next rebuild (within
  the 2-second period), not merely whenever the underlying store happens
  to become unreadable. A connection already established keeps its
  handshake; its per-request issuer check (`VerifyIssuer`) reads the store
  fresh and refuses a Key B leaf while sealed.
- Issuance, enrolment, revocation and listing are separately permissioned
  (`certs:issue`, `certs:enroll`, `certs:revoke`, `certs:list`).
- **An injected `cakey.ProviderFactory` (`WithKeyProviderFactory` /
  `WithCertKeyProviderFactory`) replaces the built-in per-tenant default
  one tenant at a time, never with one shared CA.** `resolveKeyProvider`
  calls
  `factory.ProviderForTenant(tenantID)` for the SAME `tenantID` every other
  accessor on the path is resolving for, so `IsTenantIssuer` compares each
  tenant's CLAIMED certificate against THAT tenant's own registered CA,
  exactly as it does for the built-in path — a certificate issued under
  tenant X's injected CA never satisfies tenant Y's binding. `CACertPEMs`
  enumerates the tenants to widen `ClientCAs` with via
  `factory.TenantIDs(ctx)`, since qrdb's own `__sys:cert_ca/` scan finds
  nothing for a factory-backed tenant (its material lives in the embedder's
  own backend, never qrdb's store). **Switching an already-running
  deployment from the built-in default to an injected factory (or back)
  drops every per-tenant CA it is switching away from**: nothing migrates
  existing tenants' Key B CAs onto the newly injected factory, and the
  built-in per-tenant material under `__sys:cert_ca/{tenantID}/` is not
  deleted but is not consulted — any login certificate still anchored at an
  orphaned per-tenant CA stops authenticating. This is a deliberate,
  operator-chosen transition, not an upgrade path.

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
