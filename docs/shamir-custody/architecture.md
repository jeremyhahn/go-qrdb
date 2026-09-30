# Shamir Custody — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-29

M-of-N custody of a barrier's key material by named **share holders**: who
holds a share, how a share reaches them, how they prove who they are when they
use it, and the one quorum that every share-gated operation — unseal, seal,
root token, rekey, revocation and recovery — goes through.

The barrier itself is [../barrier-seal/](../barrier-seal/). The initialization
ceremony and the ceremony CA are [../ceremony/](../ceremony/).

## Problem

A single passphrase means one person, or one leaked secret, opens the whole
store. Splitting the key is the easy half. The hard half is that the split is
only as strong as three things around it:

- **distribution** — each share must reach exactly one person, and never sit in
  a response body, a log, a terminal buffer or a garbage-collected string;
- **submission** — a share counts toward a quorum only when the person who
  holds it presents it, authenticated, and only once; M means M people, not M
  strings that happen to be valid shares;
- **membership** — who may submit is itself security-critical and must be
  readable while the node is sealed, which is exactly when nothing encrypted
  can be read.

## Shape chosen

### One kind of share holder

A **share holder** is a named person who holds exactly one share of one key.
There are two roles, and they differ **only** in the RBAC rights their identity
carries:

| Role | `--role` | RBAC | Share handling |
|---|---|---|---|
| Security Officer | `so` | governance rights (the SO role) | identical |
| Key custodian | `custodian` | none beyond share operations | identical |

Both are enrolled the same way, receive a share the same way, and submit it the
same way. There is one registry, one envelope format, one claim protocol and
one quorum accumulator for both.

### Enrolment

An administrator or a Security Officer enrols a holder by submitting the
holder's CSR (`barrier officer register`). Nobody may enrol themselves. The
enrolment:

- has the node's **ceremony CA** issue the holder a certificate whose identity
  the CA assigns: `CN=<username>`, and a single SPIFFE URI SAN in the **holder
  namespace**, `spiffe://<trust-domain>/tenant/<tenant>/holder/<username>`. The
  CSR contributes only its public key; any subject or SAN it carries is
  ignored. The authentication layer refuses a holder-namespace identity as a
  login identity, so a holder certificate cannot be used to reach anything
  except the holder routes.
- records the holder's **envelope recipient key**: the certificate key itself
  (P-256 or P-384), or a separately enrolled HPKE key — `dhkem-p256`,
  `dhkem-p384`, `dhkem-x25519` or the post-quantum hybrid `mlkem768-x25519`.
- enrols the holder's **authentication factors**. `mtls` — possession of the
  certificate key — is always enrolled. Additional factors (FIDO2, PIV,
  PKCS#11, …) are enrolled through whatever `authfactors.Factor`
  implementations the composer registered with `WithFactorRegistry`; go-qrdb
  ships none of them itself. One factor is **designated** (`mtls` by default).
- refuses a second holder with the same username, certificate key or envelope
  key in the same scope: **one person holds one share, in one role, per
  scope.**

No share exists at enrolment. Shares come only from a distribution.

### Distribution

Shares are produced in exactly one place: the barrier's distributor. Shamir
initialization, recovery-key generation, the encryption migration and every
rekey (including revocation) call the same `redistribute` path:

1. The distributor snapshots the active holders into a **holder roster** —
   each holder's username, role, share index, certificate thumbprint,
   designated factor and every enrolled factor's public material — with the
   **factor policy** and threshold for this share set and a fresh random
   distribution identifier.
2. A fresh 256-bit domain key is generated; the root key is AEAD-wrapped under
   it with the roster's SHA-256 digest in the associated data; the domain key is
   split M-of-N and each share is **HPKE-sealed to its holder's recipient key**
   into a share envelope, with the roster digest bound into the envelope.
3. Only after every envelope is sealed is the wrapped blob persisted. If
   distribution fails, the previous blob — and so the previous share set —
   stays in force.

Initialization has no previous share set, so a distribution that fails during
`InitializeShamir` is **rolled back**: the new root key is discarded, the
barrier is sealed, and the persisted root key, wrapped key and metadata are
deleted, so storage and status read as uninitialized. The process's epoch
barrier cannot return to uninitialized, so a retry needs a node restart;
until then, initialization is refused with `ErrRestartRequired`. A rollback
that cannot delete what was persisted returns a `ShamirInitRollbackError`
carrying both failures.

Envelopes live **only in go-enclave protected memory**, with a time-to-live
(24 hours by default). They are never persisted.

### Claim

Each holder claims its own envelope, **once**, over a connection that presents
its enrolled certificate: `claim/begin` issues a nonce (5-minute TTL), the
holder signs it with the certificate key, and `claim/complete` returns the
envelope and wipes the server's copy. The holder opens the envelope offline
with its recipient private key. Nobody else — not the administrator, not the
node operator — ever sees the share.

### The one quorum accumulator

Every share-gated operation is a **quorum round** run by one accumulator,
`StorageBarrier.SubmitHolderShare`:

| Operation | Domain | Runs when the round completes |
|---|---|---|
| `unseal` | unseal | unseals the sealed barrier |
| `seal` | unseal | seals an unsealed Shamir barrier |
| `root-token` | unseal | mints a one-time root token |
| `rekey` | unseal or recovery | redistributes the domain's key (a pending rekey or revocation) |
| `recover` | recovery | unseals the sealed barrier from the recovery key |

A holder submits by asking for a **challenge** (`quorum begin`), answering it
with an admitted factor, and submitting the answer with the share it opened
from its envelope (`quorum submit`). A share counts only when:

1. the holder is in the domain's roster and is not being removed by this
   round;
2. the holder has not already contributed to this round — each holder counts
   once;
3. the holder's factor proof verifies against the public material **in the
   roster**, for a factor the roster's policy admits, over a challenge issued
   for exactly this domain, operation, holder and roster; and
4. the share's x-coordinate is the holder's own share index, so no holder can
   submit someone else's share or two shares.

When M holders have been counted the round reconstructs the domain key and
must open the wrapped blob with it. Because the roster digest is in that
blob's associated data, the roster the holders were checked against is exactly
the roster the reconstructed key authenticates.

### The roster is readable while sealed

The roster holds nothing secret, so it is stored in clear inside the wrapped
blob. That is what lets a **sealed** node authenticate share submissions. It
is also tamper-evident: any edit to the roster — adding a holder, swapping a
public key, relaxing the policy — changes its digest and makes the wrapped key
fail to open with `ErrUnsealKeyMismatch`.

### Factor policy

The policy is chosen per share set when the key is distributed, and is bound
into the roster:

| Policy | Admits |
|---|---|
| `designated` (default) | exactly the one factor fixed at enrolment; a holder who enrolled a hardware factor cannot be authenticated by a weaker one |
| `any-enrolled` | any factor the holder enrolled |

The `mtls` factor proves possession of the certificate key by signing the
server's challenge nonce, so it works on every transport, including those with
no TLS session. When the caller did arrive over a verified TLS session, the
session's client certificate must also be the holder's enrolled certificate.

### Unseal-key indirection and domains

The root key is never split. Each **domain** has its own random key that
AEAD-wraps the root key, and that domain key is what is split:

- the **unseal domain** (`<RootKeyPath>.wrapped`) — its holders unseal, seal,
  mint root tokens and rekey;
- the **recovery domain** (`<RootKeyPath>.recovery.wrapped`) — an independent
  key, generated on demand, split to its own recovery holders with its own
  roster and policy. It can unseal the barrier (`recover`) and be rekeyed; a
  rekey of one domain never touches the other.

The associated data binds the root-key path, the domain and the roster digest,
so a blob cannot be moved between barriers, between tenants, between domains,
or onto a different roster.

### Rekey and revocation

A rekey redistributes a domain's key to every holder then active, at a new
threshold and policy if asked, and **cryptographically revokes every earlier
share**, claimed or not: the old shares reconstruct a key that no longer opens
the blob. The root key never rotates and no stored data is re-encrypted.

Starting a rekey needs RBAC authorization (and, on the system barrier, the SO
PIN), but **completing it needs a fresh M-of-N quorum of the current
holders**. No administrator can change who holds the key alone.

Adding a holder is: enrol them, then rekey. **Revoking a holder is a rekey that
removes them**: the round is completed by M of the *remaining* holders, the key
is redistributed to the N−1 who remain, and the revocation is refused up front
when the threshold would exceed N−1.

## Two tiers of custody

```
Root key (256-bit, never split, never rotated)
  |
  +-- wrapped under the UNSEAL key  --  split M-of-N to the unseal holders
  |       Security Officers and custodians, each with a sealed envelope
  |       and their own factors; quorum: unseal, seal, root-token, rekey
  |
  +-- wrapped under the RECOVERY key  --  split M'-of-N' to the recovery holders
          an independent key and roster; quorum: recover, rekey
```

**No single holder holds a key.** Every operation that uses a key needs M
distinct, authenticated holders present.

## Alternatives rejected

- **Returning shares from initialization or rekey.** It puts every share in one
  response, in one process, at one moment. Shares leave the node only as
  envelopes sealed to one holder each.
- **A separate custodian subsystem.** An earlier design kept custodians in
  their own registry, envelope format, claim protocol and unseal accumulator,
  with shares entered at enrolment by an officer. It broke split knowledge (the
  enrolling officer handled raw shares) and gave the store two quorum paths to
  secure. It has been removed; custodians are holders with a role.
- **Counting shares instead of people.** An unauthenticated share endpoint
  lets one person who has seen M shares act as M people. Every submission is
  bound to a holder, a factor proof and that holder's share index.
- **Keeping the holder list behind the barrier.** A sealed node could not read
  it, so it could not authenticate the holders who unseal it. The roster is
  public and bound to the key instead.
- **Splitting the root key directly.** Rotating custody would mean rotating the
  root key and re-encrypting every byte of stored data.
- **RBAC-gating claims and submissions.** Holders use them while the barrier is
  sealed, before or without any user store. The routes are identity-gated and
  the handler enforces the real authorization: roster membership, certificate
  thumbprint, nonce signature and factor proof.
- **Letting one administrator seal a Shamir barrier.** If M holders are needed
  to open it, one person must not be able to close it; seal is a quorum
  operation.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| Roster, quorum accumulator, redistribution, rekey, recovery, root token | `pkg/seal` | go-qrdb |
| Holder registry, enrolment, envelopes, claim, challenges, holder verification | `pkg/ceremony` — see [../ceremony/](../ceremony/) | go-qrdb |
| HPKE suite registry | `pkg/keydelivery` | go-qrdb |
| `mtls` factor | `pkg/authfactors/mtls` | go-qrdb |
| SO PIN | `pkg/pin` — see [../auth-factors/](../auth-factors/) | go-qrdb |
| GF(256) Shamir primitive, protected shares | — | **go-quicraft** |
| Hardware factors (FIDO2, PIV, PKCS#11, TPM 2.0) | — | **go-xkms**, injected through `WithFactorRegistry` |

## Trust boundaries

- **No single holder holds a key**, and no operator or administrator ever
  handles a share.
- **A share is delivered exactly once, to its holder.** The envelope is sealed
  to the holder's key; the server's copy is wiped on claim or at its deadline.
- **Every submission is authenticated and counted once per holder** against a
  roster that is bound to the key it unlocks.
- **Shares never rest as garbage-collected strings.** Held envelopes are
  go-enclave sealed secrets; a submitted share is held in a go-enclave
  protected share; the transports wipe the share bytes they decoded; the
  reconstructed key is wiped after use.
- **Membership changes need the quorum.** Adding, removing or re-policing
  holders is a rekey, and a rekey completes only with M current holders.
- **Holder certificates are not login credentials.** The holder SPIFFE
  namespace is refused by the authentication layer.

## What this feature deliberately does not do

- **No share recovery.** A lost envelope is replaced by a rekey, never
  re-issued.
- **No share storage of any kind.** No API accepts share bytes for storage and
  no store holds them.
- **No hardware factors of its own.** They are go-xkms implementations reaching
  go-qrdb through the factor registry.
- **No multi-node custody.** Custody is per node: each node's barrier has its
  own holders and shares.
- **No quorum on `none` or `passphrase` barriers.**
- **No holder keys in the web UI.** The web UI and the TypeScript SDK
  administer holders — enrol, list, rekey, revoke, status — and nothing more.
  Claiming an envelope and submitting a share use the holder's private keys,
  which stay in the qrdb CLI or the desktop app (where the Go binding answers
  the `mtls` factor and opens the envelope in process). A sealed Shamir
  barrier's web view says which command its holders run and waits for them.

## Decided: not in the browser

Claiming an envelope and submitting a share are **not** offered in the browser,
by the owner's decision. A browser path was designed, using the JavaScript HPKE
library `@hpke/core` over WebCrypto, and rejected for two reasons:

- **The library is unaudited.** `@hpke/core` passes the RFC 9180 test vectors
  and is widely adopted, but it has not been formally audited, and the envelope
  is the only thing standing between a share and its holder.
- **Cross-site scripting would become key disclosure.** Holder private keys in
  a browser tab can be reached by anything that runs script in the page, so an
  XSS bug would expose keys, not just a session.

Claims and submissions go through the qrdb CLI or the desktop app, where the
holder's keys stay in Go. The web UI administers holders and shows which
command they run. This will be revisited when an audited JavaScript HPKE
implementation exists.

## Companion documents

- [../seal/so-dual-factor.md](../seal/so-dual-factor.md) — SO PIN
  authentication of governance operations.
- [../seal/sealed-window.md](../seal/sealed-window.md) — the listener that
  serves holders while the node is sealed.
