# Initialization Ceremony — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

## Exported surface

`sdk/go`:

```go
func (c *Client) Ceremony() transport.CeremonyClient
```

Composition-plane options (Tier 2):

```go
func WithCeremonyCA(ca ceremony.CeremonyCA) ServerOption
func WithPINBackend(backend pin.PINBackend) ServerOption
func WithFactorRegistry(reg authfactors.FactorRegistry) ServerOption
```

The CA seam:

```go
type CeremonyCA interface {
	SignCSR(csrPEM []byte, opts *SignCSROptions) (*x509.Certificate, error)
	CACertificate() (*x509.Certificate, error)
	CABundle() ([]byte, error)
	IssueCertificate(req *IssueCertRequest) (*IssuedCert, error)
}
```

Both `cert.CertProvider` (qrdb) and `ca.TrustedCA` (go-xkms) satisfy it through
thin adapters.

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

Officer configuration (`pkg/ceremony/types.go`) carries `AuthenticatorType` and
`AuthenticatorRef`. `ceremony.ParseEnrollmentSpec` parses
`<username>:<kind>:<path>`; the two-field form `<username>:<path>` means `csr`.

Share sealing picks an envelope from the officer's key algorithm: RSA-OAEP for
RSA, ECIES over P-256 / P-384 for ECDSA, and X25519 (via the birational map
from the Ed25519 public key) for Ed25519. **In FIPS 140-3 mode the Ed25519 form
is refused** with `ErrAlgorithmNotFIPSApproved`: X25519 is not an approved
algorithm and there is no approved substitute for an Ed25519 recipient, so such
an officer must enrol an RSA or NIST-curve key instead. See
[seal/fips140-mode.md](../seal/fips140-mode.md).

`ThresholdInitializer` implementations register through `ThresholdRegistry`;
qrdb ships only `NoopThresholdInitializer`
(`pkg/ceremony/threshold_noop.go`).

## States

`awaiting_init` → `enrolling` → `operational`. Single-admin
(`threshold < 2`) skips `enrolling`.

## Invariants

- **Shares are never persisted to disk** and never leave a `secure.Secret`
  (go-enclave sealed enclave) except into the claiming officer's
  hands.
- **`Store` consumes the caller's slice, wiping it.** Pinned by tests in
  `pkg/ceremony/share_store_test.go`. A heap copy would survive a core dump or
  a swapped page.
- **`Claim` destroys the enclave** and retains only the fact of the claim, so a
  second attempt is refused rather than served.
- **The officer record persists; share bytes never do.**
  `ceremony/officers/<name>` holds the certificate thumbprint or public key, the
  share **index**, the auth factor and timestamps.
- **An abandoned ceremony loses its shares** and must be re-run. Intentional.
- **PINs never reach the credential store as raw bytes.** `pin.Service.SetSOPIN`
  and `SetUserPIN` hash (Argon2id / PBKDF2 by FIPS mode) through the backend;
  the raw receive buffers are wiped with `secure.Wipe` before bootstrap returns.
- **In M-of-N the User PIN is the Shamir secret.** Its bytes are split with
  `shamir.Split` and wiped locally once the shares exist. The PIN is never
  persisted in plaintext anywhere.
- **Without `WithPINBackend` the daemon builds a default `pin.SoftwareBackend`**
  (Argon2id / PBKDF2 auto-selected by `seal.FIPSEnabled()`) over
  barrier-protected storage. Hardware backends from go-xkms inject through the
  same option; ceremony never learns which it is talking to.
- **Verification ignores the authenticator kind.** `BeginClaimCert` and
  `CompleteClaimCert` operate on the officer's CSR public key. The kind is
  routing metadata for higher layers.
- **sign-csr is an online SO PIN oracle, so failed checks lock it out and the
  comparison is constant-time.** On the boot-time sealed-window listener it is
  reachable without authentication, and the SO PIN is its only authorization.
  `SignCSRInit` checks the PIN through the ceremony's `soPINAttempts`.
  - Checks run one at a time, and a failure is counted before the next check
    starts, so concurrent guesses are no cheaper than sequential ones.
  - The first 5 failures in a row cost nothing, so an operator can mistype.
    Every later failure locks the check for a backoff that doubles from 1 s up
    to 15 min, and a correct PIN resets the count.
  - Only failed checks count. Successful signings, and requests that fail
    before the PIN is looked at, never consume anything, so anonymous load
    cannot starve a legitimate officer.
  - The comparison is `subtle.ConstantTimeCompare` over SHA-256 digests.
- **The SO PIN needs at least 40 bits of entropy.** With the backoff at its
  cap an attacker gets about 96 guesses a day. Use, for example, 8 random
  characters from a 62-character alphabet (about 47 bits) or a 12-digit
  random number (about 40 bits).
- **`AuthenticatorType` empty means `software`**; anything outside the set
  yields `ErrInvalidAuthenticatorType`.
- **Ceremony and firstboot are both idempotent** and safe to re-run on cluster
  nodes — leader writes propagate via Raft, follower runs are no-ops.

## Migration note

The legacy `credentialSOPin` / `credentialUserPin` storage keys remain
**readable** for in-place upgrades from older deployments. New writes go
exclusively through the `pin.PINBackend`, and the legacy keys are **wiped on
the first successful PIN backend write**.

## Error semantics

| Condition | Behavior | Fails closed? |
|---|---|---|
| Threshold ≥ 2 with no `--officers` | typed `ErrNoOfficers` | **yes** |
| Claim for an already-claimed share | refused — enclave destroyed | **yes** |
| Nonce expired or signature invalid | refused | **yes** |
| `AuthenticatorType` outside the set | `ErrInvalidAuthenticatorType` | **yes** |
| Ceremony abandoned (restart mid-enrolment) | shares lost; must re-run | **yes** |
| No `CeremonyCA` wired | initialization fails | **yes** |
| Wrong SO PIN on sign-csr | `ErrSOPINMismatch`. The comparison is constant-time over SHA-256 digests, so it leaks neither where the PIN differs nor its length | **yes** |
| sign-csr while the SO PIN check is locked out (after 5 failures in a row; backoff 1 s doubling to 15 min) | `ceremony.SOPINLockedError`, surfaced as `service.SignCSRThrottledError` (unwraps to `pin.ErrPINLocked`): `429` / `RESOURCE_EXHAUSTED` on every transport, and REST sets `Retry-After` to the wait in seconds, rounded up. The PIN is not compared, so a locked-out call cannot confirm a guess | **yes** |

## Permission model

| Permission | Gates |
|---|---|
| `ceremony:initialize` | initializing the ceremony |
| `ceremony:status` | reading state |
| `ceremony:sign_csr` | signing a CSR during or after the ceremony |
| `ceremony:claim_cert` | the certificate-claim challenge pair |
| `ceremony:enroll_custodian` / `ceremony:revoke_custodian` | custodian lifecycle, and external-officer enrolment |
| `ceremony:claim_share_begin` / `ceremony:claim_share_complete` | the share-claim constants |
| **identity-gated** | the custodian and officer share-claim routes — see [../shamir-custody/api.md](../shamir-custody/api.md#why-the-claim-routes-are-identity-gated) |

## Persistence

- Officer records: `ceremony/officers/<name>` — thumbprint or public key, share
  index, auth factor, timestamps.
- Custodian metadata: `__sys:custodian/<id>`, Raft-replicated, public only.
- Shares: **go-enclave sealed enclave only** (`secure.Secret`), never on disk.

## Test strategy

- **Unit**: state transitions for both modes, `Store` wiping the caller's
  slice, `Claim` destroying the enclave and refusing a second attempt,
  enrolment-spec parsing for all three kinds and the two-field form, and
  `AuthenticatorType.Normalized()`/`Validate()`.
- **`pkg/ceremony/share_store_test.go` pins the custody invariants.** Those are
  the tests that make the memory guarantees real rather than aspirational.
- **Idempotence tests** for re-running ceremony and firstboot.
- **Error tests** per function.

## Observability

Ceremony state is reported by `CeremonyStatus`
(`awaiting_init` / `enrolling` / `operational`). Per-officer claim state comes
from the claim-status endpoint, which never returns share bytes. Ceremony
operations are audited through [../audit/](../audit/).
