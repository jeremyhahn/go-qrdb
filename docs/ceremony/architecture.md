# Initialization Ceremony — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-29

The ceremony is the barrier's custody bootstrap and the home of the share
holder engine: it establishes the PINs and the CA, enrols the initial share
holders, has the barrier split its unseal key to them, and holds each sealed
envelope until its holder claims it. It is **not** the admin bootstrap — that
is [../firstboot/](../firstboot/). What share holders are and how the quorum
works is [../shamir-custody/](../shamir-custody/).

## Problem

A fresh system has no CA, no holders, no key and no admin. Getting from
nothing to a running M-of-N custody arrangement is a sequence whose order
matters: holders must exist, with CA-issued identities, before the key is
split; the split must go only to them; and no step may let the person running
the ceremony see a share.

## Shape chosen

A three-state machine:

```
awaiting_init  --Initialize()-->  enrolling  --every holder claimed-->  operational
```

- **awaiting_init** — freshly deployed; no ceremony performed.
- **enrolling** — M-of-N custody initialized; holders are claiming their
  envelopes.
- **operational** — every initial holder has claimed. In single-admin mode
  (`threshold < 2`) the state goes straight here from `awaiting_init`.

`Initialize`:

1. provisions the SO and User PINs through the `pin.PINBackend` (or verifies
   them, when barrier initialization already established them);
2. issues the server TLS certificate from the ceremony CA;
3. with `threshold >= 2`, enrols each named holder (Security Officer or
   custodian, from a CSR) through the same `register` path the holder routes
   use, then asks the barrier to initialize Shamir custody at
   `threshold`-of-`len(holders)` under the requested factor policy. The barrier
   splits its **unseal key** and the ceremony, as the barrier's distributor,
   seals one envelope per holder.

The response carries the CA bundle, its SPKI pin, the TLS certificate and each
holder's CA-issued certificate. It never carries a share, and **nothing but
the unseal key is split**: the User PIN is a PIN, not a Shamir secret.

## PIN handling — never raw bytes

PIN material is owned by a `pin.PINBackend` injected with `WithPINBackend`;
composers wire `pin.SoftwareBackend` over the node-local seal store, and
hardware deployments inject go-xkms backends through the same option.

- `pin.Provision` sets the SO PIN and then the User PIN (gated by the SO PIN),
  hashed with Argon2id, or PBKDF2 in FIPS mode.
- Every SO-PIN check anywhere in the stack goes through the backend, which
  compares hashes in constant time. Nothing compares raw PINs.
- Every SO-PIN-gated operation **fails closed** without a backend
  (`pin.ErrBackendNotConfigured`).
- Receive buffers are wiped before the call returns.

## The holder engine

`pkg/ceremony` owns one share-holder engine, `holderScope`, instantiated for
the system unseal domain, the system recovery domain and every tenant barrier.
Each scope has its own registry (public records behind the barrier), its own
envelope store and its own pending challenges. The engine:

- **enrols** holders: the CA issues a certificate with a CA-assigned identity
  (`CN=<username>`, a SPIFFE URI in the holder namespace); the CSR contributes
  only its public key. Self-enrolment and a second share for the same person
  (same username, certificate key or envelope key) are refused;
- **distributes**: as the barrier's `CeremonyDistributor` it prepares the
  roster and HPKE-seals each share to its holder into a `secure.Secret` with a
  time-to-live;
- **delivers** each envelope once, over a connection presenting the holder's
  certificate, after a signed-nonce challenge;
- **verifies** quorum submissions: as the barrier's `HolderVerifier` it issues
  and checks factor challenges against the roster.

## Share lifecycle

Shares are **never persisted, anywhere**:

1. The barrier generates the domain key and splits it.
2. Each share is HPKE-sealed to its holder's recipient key and held as a
   `secure.Secret` with a timer (24h by default). Storing a new envelope for a
   holder destroys the old one first.
3. The holder claims it — `claim/begin`, sign the share-claim message over the
   nonce ([../shamir-custody/api.md](../shamir-custody/api.md#schemas)),
   `claim/complete` —
   and the held copy is destroyed.
4. An unclaimed envelope is destroyed at its deadline; only a rekey replaces
   it.

If the node restarts before every holder has claimed, the unclaimed envelopes
are gone; the claimed ones, and the key, are unaffected. A rekey by M holders
redistributes.

## The CA seam

```go
type CeremonyCA interface {
    SignCSR(ctx context.Context, csrPEM []byte, opts *SignCSROptions) (*x509.Certificate, error)
    CACertificate() (*x509.Certificate, error)
    CABundle() ([]byte, error)
    IssueCertificate(ctx context.Context, req *IssueCertRequest) (*IssuedCert, error)
}
```

`SignCSROptions` carries the **Subject** and **URIs** the ceremony assigns. An
implementation must write those — and nothing from the CSR but its public key —
into the certificate. `ceremony.LocalCA` does; qrdb's `cert.CertProvider` and
go-xkms's CA satisfy the interface through adapters that must do the same.

**The `SignCSR` contract is a MUST, and it is enforced.** The ceremony CA signs
under the node's cluster CA, which the mTLS listener trusts for client logins,
so every certificate it issues is a login claim. Every `CeremonyCA` —
qrdb's `LocalCA` and any CA composed in from outside, such as go-xkms's —
**MUST**:

- sign with `SignCSROptions.RawSubject()` as the certificate's raw Subject:
  `CN=<Username>`, `OU=[<TenantID>, <Profile>]`, one RDN per attribute so the
  tenant stays the first OU. Encoding the same `pkix.Name` through
  `x509.Certificate.Subject` packs the OUs into one sorted DER SET, which can
  put the profile first;
- carry **no** Subject Alternative Name, and take nothing from the CSR but its
  public key, after checking its proof of possession;
- issue a leaf: not a CA, no certificate-signing key usage, key usage
  exactly `SignCSROptions.KeyUsage`, and extended key usages exactly the set
  `SignCSROptions.ExtKeyUsage` names (order aside, no duplicates).

A CA that does not conform is **refused, fail-closed**: the ceremony re-checks
every certificate a `CeremonyCA` returns and records nothing for it, returning
`*ceremony.CertSubjectError` (Subject, SANs or key differ) or
`*ceremony.IssuedCertCapabilityError` (a CA, certificate signing or another
key usage, or another set of extended key usages).

## Threshold initializers

`ThresholdInitializer` supports hardware-native threshold ceremonies;
implementations are registered through `ThresholdRegistry` and ship in go-xkms.
qrdb ships only `NoopThresholdInitializer`.

## Relationship to firstboot

`ceremony.Initialize` is **barrier-only**.

| Concern | Package | Function |
|---------|---------|----------|
| PINs, CA, server certificate, initial holders, Shamir custody | `pkg/ceremony` | `ceremony.Initialize` |
| System tenant, built-in roles, platform admin user and client cert | `pkg/firstboot` | `firstboot.Initialize` |

Daemon startup runs the ceremony, then firstboot. Both are idempotent.

## Alternatives rejected

- **Splitting the User PIN as the Shamir secret.** An earlier ceremony split
  the User PIN and handed shares out through a separate officer-share
  protocol. It made a login credential into key material and gave the store a
  second share path. The ceremony now enrols holders and lets the barrier split
  its own unseal key through the one distributor.
- **Adopting holder certificates or bare public keys.** Enrolling an existing
  certificate or key let the enroller choose the identity. Holders are enrolled
  from a CSR only and the CA assigns the identity.
- **Persisting shares server side.** Shares belong to holders.
- **A single monolithic bootstrap.** Barrier custody and admin identity have
  different trust models and different failure modes.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| Ceremony state machine, holder engine, envelopes, CA seam | `pkg/ceremony` | go-qrdb |
| PIN backend contract and software implementation | `pkg/pin` — see [../auth-factors/](../auth-factors/) | go-qrdb |
| Barrier, roster, quorum | `pkg/seal` — see [../shamir-custody/](../shamir-custody/) | go-qrdb |
| Admin/tenant/role bootstrap | `pkg/firstboot` — see [../firstboot/](../firstboot/) | go-qrdb |
| Hardware factors, threshold initializers | — | **go-xkms**, injected |
| Shamir GF(256) primitive | — | **go-quicraft** |

## Trust boundaries

- **Envelopes live only in `secure.Secret` enclaves**, destroyed on claim or at
  their deadline.
- **What persists is the holder record**: certificate, envelope recipient key,
  factor public material, share index, timestamps. **Never a share.**
- **Certificate identity is CA-assigned.**
- **The claim is a signed-nonce challenge over a connection presenting the
  enrolled certificate.**

## What this feature deliberately does not do

- **No admin, tenant or role creation.** That is firstboot.
- **No share recovery.** A lost envelope is replaced by a rekey.
- **No hardware factors or threshold ceremony of its own.**
- **No `qrdb ceremony init` verb.** The root `qrdb init` is the entry point.
