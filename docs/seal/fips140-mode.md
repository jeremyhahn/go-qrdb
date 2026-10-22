# FIPS 140-3 Mode (`GODEBUG=fips140=only`)

> **Status: REFERENCE** · Scope: go-qrdb · Last verified: 2026-10-22
> Companion reference; the feature folders under `docs/` are the contract and win on any disagreement.

<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

This document records how go-qrdb is verified against the Go Cryptographic
Module, the remediation on its cryptographic paths, the exemption register for
anything that is deliberately not clean, and, in
[Open blockers](#open-blockers), what remains. **Both modules' suites pass
under `fips140=only`, and the gate is not armed**, so do not read this document
as a compliance claim: an unarmed job's result is a measurement, not an
enforceable property. What arming waits on is in
[Arming the gate](#arming-the-gate).

## The job

The `FIPS 140-3` job in `.github/workflows/nightly.yml` executes the full unit
suite of **both modules** under:

```
GOFIPS140=latest GODEBUG=fips140=only go build ./...
GOFIPS140=latest GODEBUG=fips140=only make test
GOFIPS140=latest GODEBUG=fips140=only make test-sdk
```

The workflow has no `schedule:` trigger; it runs on `workflow_dispatch`.

The `go build ./...` line runs in the **root module only**: the job has no
build step for `sdk/go`. The SDK is compiled under the FIPS module only as far
as `make test-sdk` compiles it — `go test ./...` in `sdk/go`, which builds every
package it visits, with the test build tags. Whatever is claimed about the SDK
below is a claim about that step, not about a `go build`.

`GOFIPS140` selects the module at build time. `GODEBUG` selects the enforcement
level at run time, and the level is `only`, not `on`:

| Setting | Module loaded | Non-approved algorithms |
|---|---|---|
| `fips140=on` | yes | **permitted** — MD5, SHA-1, X25519 all succeed |
| `fips140=only` | yes | rejected: the call returns an error or panics |

`on` cannot fail on the thing the job exists to catch: a regulated path can use
prohibited cryptography and the job still reports green. `only` is the setting a
FedRAMP / PCI DSS v4 / HIPAA gate has to run (handbook
`software/conventions/configuration.md` §5).

The job scope is deliberately identical to the ordinary gate — the same
`make test` **and `make test-sdk`** targets with the same build tags. A FIPS job
over a subset of the suite does not tell you the product is FIPS-clean.

**`make test-sdk` is required.** `sdk/go` is a separate Go module, so
`make test` (`./pkg/... ./cmd/... ./test/buildgates/...`) does not reach a
single line of it: every transport, `sdk/go/node`, the driver, the DAO layer
and the conformance matrix run only under `make test-sdk`.

The sdk step runs even when the root suite fails (`if: always()`), so a red
root module still yields an SDK measurement. Either module failing fails the
job.

The job is **not** in `ci-gate`'s `needs` and is therefore not a required
check. No finding keeps it red (see [Measured status](#measured-status)), so
what is left is a decision rather than a blocker;
[Arming the gate](#arming-the-gate) states it and the change that makes the
job required.

To reproduce a CI failure locally, prefix the same commands:

```
GOFIPS140=latest GODEBUG=fips140=only make test
GOFIPS140=latest GODEBUG=fips140=only make test-sdk
```

## Handling a failure

**A red FIPS job is a finding, not a flake.** There are three legitimate
responses and one forbidden one.

1. **Product code on a security path uses a non-approved primitive** → fix the
   call site to use an approved primitive. This is the common case and it is a
   real compliance defect.
2. **An external format or protocol mandates the primitive** (a header hash a
   third party computes, a checksum an external system requires) → this is not
   a compliance violation, because the frameworks govern protection of
   regulated data rather than every hash in the process. But the call must not
   execute inside the FIPS-gated path: isolate it behind a build tag or a
   package the job does not exercise, and add a row to the exemption register
   below with the citation that mandates it.
3. **A test fixture uses a weak primitive for convenience** → change the
   fixture.

Forbidden: setting the job back to `fips140=on`, adding a build tag whose
effect is to skip the job, narrowing the package list so the failing package is
not covered, or any other change that produces a green gate testing less than
it did.

## Remediation performed

The non-approved primitives on this repository's cryptographic paths have been
replaced. The AES-GCM change is byte-compatible with existing ciphertext and
existing wire formats; the rest are not free, and each states its cost below —
a configuration minimum, one enrollment path refused in FIPS mode, and one
export format refused under strict enforcement.

The deterministic role identifiers are RFC 9562 §5.8 UUIDv8 over SHA-256; see
[UUIDv8 identifiers](#uuidv8--role-identifiers).

**There is no non-approved primitive left on a go-qrdb call site, and none is
reached in a dependency either.** `go-truststrap`'s Noise handshake still uses
X25519, but `pkg/clusterenroll` does not call it when
`crypto/fips140.Enabled()` (`pkg/clusterenroll/fips.go`). See
[Open blockers](#open-blockers) for the ruling and for what remains
unavailable as a result.

### AES-GCM — `cipher.NewGCM` → `cipher.NewGCMWithRandomNonce`

SP 800-38D requires the module to own IV generation, so `cipher.NewGCM`
(arbitrary caller-supplied IVs) is rejected under `only`. Every construction
uses `cipher.NewGCMWithRandomNonce`, which samples a 12-byte nonce itself and
prepends it to the ciphertext.

| Call site | Role |
|---|---|
| `pkg/crypto/aesgcm/aesgcm.go` | shared AES-256-GCM helper; the tenant DEK keyring (`pkg/dek`) encrypts through it |
| `pkg/seal/encfs/block.go` | encrypted-filesystem block cipher |
| `pkg/keywrap/software.go` (`SoftwareProvider.{Wrap,Unwrap}`) | the backup key-wrap provider |
| `pkg/service/backup_service.go` (`encryptShard`/`decryptShard`) | the backup shard cipher |

Share envelopes are HPKE (`crypto/hpke`, see below) and construct no GCM of
their own.

**Stored ciphertext needs no migration.** Each site frames its output as
`nonce (12) || ciphertext || tag (16)` with a nonce drawn from `crypto/rand`,
which is byte-for-byte what `NewGCMWithRandomNonce` produces.

`NewGCMWithRandomNonce` reports `NonceSize() == 0` and `Overhead() == 28`, so
code that frames the output uses a named 12-byte constant, not
`gcm.NonceSize()`.

The construction documents a limit of 2^32 Seal operations per key, to keep the
probability of a 96-bit random-nonce collision negligible; this is the bound of
any random 96-bit nonce. The tightest case is `pkg/seal/encfs`, whose AEAD is
bound to one file's DEK and seals once per 4 KiB block write — 2^32 blocks is
16 TiB of writes through a single DEK.

### X25519 — Noise NK enrollment replaced by the P-256 suite in FIPS mode

Noise_NK_25519_ChaChaPoly_SHA256 mandates X25519 key agreement, which
`crypto/ecdh` rejects under `only`. The bootstrap handshake therefore has two
suites, chosen per connection by the first frame's `version || suite` header:
the Noise_NK suite, and a FIPS-approved P-256 suite (`TSBOOT1_P256_AESGCM_SHA256`:
ECDH over P-256, SHA-256 key derivation, AES-GCM) with the same shape — an
initiator that pins the responder's static public key before it dials. The
P-256 suite is always available; the Noise suite is not.

`pkg/clusterenroll` reads `crypto/fips140.Enabled()` through
`clusterenroll.NoiseAvailable()` and, when it is false:

- a **first node** serves the **P-256 suite alone** on its enrollment listener,
  generating and publishing `p256-key.hex` and no `noise-key.hex`. SPKI-pinned
  TLS, DANE and direct HTTPS fetch the same bundle from `/v1/ca/bootstrap`;
- a **member's join listener** does the same, serving P-256 only
  (`join-p256-key.hex`, no `join-key.hex`);
- a **joining node** configured with the P-256 key (`--bootstrap-p256-key`,
  `--bootstrap-join-p256-key`) enrolls over P-256 in the anchor fetch and in the
  CSR exchange alike; a Noise key configured beside it is not used;
- a **joining node with Noise as its only method** is refused with
  `*NoiseFIPSUnavailableError`, before the first dial and before anything is
  written to the data directory. The error names FIPS and the P-256 flag that
  replaces the Noise one.

Outside FIPS mode a node that holds both keys uses Noise where it can and falls
back to P-256, and both suites stay available so a mixed fleet keeps joining.
Every exclusion logs at WARN naming FIPS. `Enabled()` rather than `Enforced()`,
for the reason given below for share-holder envelope suites.

The mechanism is described in
[../clustering/architecture.md](../clustering/architecture.md#bootstrap-handshake-suites)
and the flags in [../clustering/cli.md](../clustering/cli.md).

### X25519 — share-holder envelope suites refused in FIPS mode

A share holder's envelope is HPKE-sealed to the holder's enrolled key under one
of the `pkg/keydelivery` suites: `dhkem-p256`, `dhkem-p384`, `dhkem-x25519` or
the hybrid `mlkem768-x25519`. X25519 is not an approved key agreement, so in
FIPS mode `keydelivery.LookupSuite` refuses `dhkem-x25519` and
`mlkem768-x25519` with `*keydelivery.SuiteNotApprovedError` — and so does
`RecipientFromPublicKey` for an X25519 key. Enrolment of such a holder is
therefore refused up front, before any share exists, rather than failing when
a share is sealed. The NIST-curve suites are unaffected; a holder whose key is
X25519-only must enrol a P-256 or P-384 envelope key instead. Ed25519
certificate keys have no DHKEM mapping at all and need a separate envelope key
in every mode.

The guard keys off the stack's FIPS predicate (`seal.FIPSEnabled`, the
`Enabled()`-first check) rather than `Enforced()` deliberately: under
`fips140=on` the module permits X25519, but a deployment that has asked for
FIPS mode should not be handed a non-approved envelope just because the
runtime tolerates it.

### HMAC key length — minimum multicast shared secret

SP 800-107 §5.3.4 sets the floor for an HMAC key at 112 bits, and `crypto/hmac`
**panics** on a shorter key under `only`.

`discovery.MinSharedSecretLen` is 16 bytes, checked on
`MulticastConfig.SharedSecret` in `EncodeMulticastMessage` and
`DecodeMulticastMessage` ahead of the primitive, because a panic cannot be
recovered into a config error. A short secret is reported as a
`*discovery.ValidationError` wrapped in a `*discovery.Error`, and
`NewMulticastDiscovery` fails at construction rather than in the announcement
goroutine.

A deployment's `multicast-secret` must be at least 16 bytes. The secret is the
only thing authenticating membership announcements.

### SHA-1 — PKCS#12 export refused under strict enforcement

`pkg/cert.encodePKCS12` produces a `.p12` archive via
`software.sslmate.com/src/go-pkcs12`. The archive's own protection is approved
— the `Modern2023` encoder is PBKDF2-HMAC-SHA256 with AES-256-CBC — but
PKCS#12 also carries a `localKeyId` attribute holding the SHA-1 fingerprint of
the certificate (`go-pkcs12/pkcs12.go`, `sha1.Sum(certificate.Raw)`). That is
how every consumer of the format matches a key to its certificate.

This is the exemption case: an external format mandates the primitive, and the
primitive is an interoperability identifier rather than a protection mechanism,
so it is not a compliance violation. It is nonetheless unavailable under strict
enforcement, because `crypto/sha1` panics there. `encodePKCS12` refuses up
front with a `*PKCS12FIPSUnavailableError` wrapped in the `*CertExportError`
callers already match on.

The guard is on `fips140.Enforced()`, not `Enabled()`: a deployment running
`GODEBUG=fips140=on` keeps the export, because nothing in FIPS 140-3, FedRAMP,
PCI DSS or HIPAA prohibits a SHA-1 identifier that protects nothing. Contrast
the X25519 case above, which is guarded on `Enabled()` because that primitive
really does protect a Shamir share.

### UUIDv8 — role identifiers

`crypto/sha1` **panics** under `only`, so a name-based RFC 4122 v5 UUID
(`uuid.NewSHA1`) cannot be derived. `role.SystemRoleID` derives the built-in
role identifiers through `deterministicUUID` (`pkg/role/store.go`) as an
RFC 9562 §5.8 **UUIDv8 over SHA-256**. UUIDv8 is the version reserved for
application-defined derivations, which is the only place a SHA-256 name-based
UUID can live: RFC 4122 fixes v3 to MD5 and v5 to SHA-1. The inputs are
length-prefixed before hashing so the encoding is injective — without framing,
tenant `a/b` with role `c` and tenant `a` with role `b/c` would collide on one
identifier.

**These identifiers are persisted data** — role primary keys, both secondary
indexes, and the contents of every user record's `RoleIDs`. There is no
compatibility layer for a UUIDv5 (SHA-1) identifier: every cluster is
provisioned under the v8 scheme, and the SO deny-list and the
separation-of-duties Owner strip match a role id against its
`role.SystemRoleID` derivation directly.

### Strict separation of duties and `cluster:join_token` under FIPS

`permission.ClusterJoinToken` is `ScopeCross`
(`pkg/permission/cluster_join_token_scope.go`). A FIPS build is always strict
separation of duties (`sdk/go/node/node.go`:
`fips140.Enabled() || cfg.Auth.SeparationOfDuties`; `separation_of_duties` in
[../12-configuration.md](../12-configuration.md)), and under strict separation
`RBACAuthorizer.stripOwnerRole` (`pkg/auth/rbac`) voids the `owner` role, which
otherwise holds every permission through `permission.All()`. The system Owner
therefore cannot mint a join token in a FIPS build.

The cluster **Admin** built-in role (`permission.RoleAdmin`) carries
`permission.ClusterJoinToken` in every posture, registered in
`registerAdminGrants` (`pkg/permission/builtin_role_grants.go`) and pinned by
the golden-set drift guard in `pkg/role/builtin_grants_equivalence_test.go`.
The strip never touches Admin, so a system-tenant Admin mints join tokens in
both modes. `TestClusterJoinToken_IsPlatformOnly` pins the Admin grant
(`HasRoleGrant(RoleAdmin, ClusterJoinToken)`) and proves that SO, Operator,
User and Auditor carry nothing: Admin is never a Security Officer and never a
custodian, so FIPS 140-3 AS10.03 separation of duties holds, and `ScopeCross`
still refuses a tenant's own Admin. The capability is platform-only.

`sdk/go/conformance` covers it on every remote transport:
`TestMintJoinToken_SystemOwnerSucceeds_TenantAdminRefused`'s
`system-admin-succeeds` subtest succeeds in both modes, and its `system-owner`
subtest expects the permission-denied refusal when `fips140.Enabled()`.
`TestMintJoinToken_ExplicitTenantGrantStillRefused` shows a tenant-scoped
explicit grant refused by `ScopeCross`, and
`TestMintJoinToken_EmbeddedCapabilityExists` mints as a system-tenant Admin on
the embedded transport.

### The system tenant under the strict isolation floor

A FIPS build raises the cluster-wide isolation floor to `strict` as
`qrdbd start` brings the node up (`fips140.Enabled()` in `cmd/qrdbd/start.go`),
before first boot creates the system tenant. The floor governs isolated tenants
only; first boot creates the system tenant as a prefix tenant under it, and a
second create returns `ErrTenantAlreadyExists`
(`TestTenantService_CreateTenant_S2_SystemTenantUnderStrictFloor`,
`TestKVCreateTenant_S2_SystemTenantUnderStrictFloor`).

The SO PIN conformance suite on every SDK transport builds its client with
`sotest.RequestTimeout` (60 seconds): under FIPS the SO PIN verifier runs
PBKDF2-HMAC-SHA256 at 600,000 iterations, which costs over a second of CPU
under `-race`. The KDF cost is the mode's contract.

## Test fixtures under `fips140=only`

- **Check the error from every key generation.** `ecdh.X25519().GenerateKey`
  and `rsa.GenerateKey` at 1024 bits return an error under `fips140=only`; an
  ignored error leaves a nil key, and the panic that follows kills the whole
  test binary, so every later test in the package reports nothing.
- **Guard a SHA-1 fixture on `fips140.Enforced()` up front.** `crypto/sha1`
  panics rather than returning an error, so no error check can catch it.
- **Use an approved KEM where the KEM is incidental.** A holder's separate
  envelope key uses `keydelivery.KEMP256`, or P-384 where the test needs a KEM
  other than the device's. Where the KEM is the subject
  (`TestSuite_RoundTripEveryKEM`, `TestRegisterOfficer_EnvelopeKEMs`), the
  X25519 and hybrid cases skip under `fips140=only`, naming the refusing error
  type in the skip reason.
- **A test of a live join listener pins both suites' keys.** A normal runtime
  negotiates Noise and a `fips140=only` runtime selects P-256.
  `forceNoiseAvailable` (`pkg/clusterenroll`) simulates both values of the
  gating decision, but it cannot make X25519 key generation work under a real
  `fips140=only` runtime.
- **A test of a FIPS-dependent selector folds `fips140.Enabled()` into its
  expectation**, as `wantVersion` does for `kdf.DefaultHasher`
  (`pkg/seal/kdf`). `GOFIPS140` selects the module at build time, so
  `t.Setenv` cannot retract it at run time.

## Exemption register

One exemption. Every other non-approved primitive in this repository is either
remediated above or listed in [Open blockers](#open-blockers) — none is
silently tolerated.

A row here requires: the citation of the external format or protocol that
mandates the primitive, evidence that it protects nothing regulated, and the
narrowest isolation that keeps it out of the FIPS-gated path.

| Primitive | Call site | Mandating citation | Isolation |
|---|---|---|---|
| SHA-1, as the `localKeyId` certificate fingerprint | `pkg/cert/pkcs12.go` → `software.sslmate.com/src/go-pkcs12` | [RFC 7292](https://www.rfc-editor.org/rfc/rfc7292) §4.2 / PKCS#12 v1.1 attribute conventions; the SHA-1 fingerprint as `localKeyId` is what OpenSSL, Java keytool and Windows CryptoAPI use to bind a key to its certificate | `encodePKCS12` returns `*PKCS12FIPSUnavailableError` when `fips140.Enforced()`, so the call never executes in the FIPS-gated path. The archive's own protection (PBKDF2-HMAC-SHA256 + AES-256-CBC) is approved and unchanged. |

### Skipped tests under `fips140=only`

**Each skip below is unreachable code, not a masked failure.** A skip in this suite is
normally the thing the [Handling a failure](#handling-a-failure) section
forbids, so the distinction has to be written down rather than inferred from a
count: read `--- SKIP` here as *unreachable code*, not *hidden red*.

| Test | Skipped because | Where it IS covered |
|---|---|---|
| `TestWrappedRootKey_CallerNonceProvider` | Constructs a caller-nonce AEAD (`cipher.NewGCM`) to exercise the caller-nonce branch of `sealWrapped`/`openWrapped` | The full non-FIPS suite |
| `TestWrappedRootKey_RejectsMismatchedProviderNonceWidth` | Same, via `cipher.NewGCMWithNonceSize` | The full non-FIPS suite |
| `TestOpenWrapped_DecryptsIntoProtectedMemory`'s `caller_nonce` subtest (`pkg/seal/protected_rootkey_test.go`) | Same caller-nonce `cipher.NewGCM` construction as the two rows above, through `skipIfFIPS` | Its `module_nonce` subtest, plus the full non-FIPS suite |
| `TestParseSignerPEM`'s `x25519_cannot_sign` subtest (`pkg/authfactors/mtls`) | `ecdh.X25519().{GenerateKey,NewPrivateKey}` both refuse under `fips140=only`, so there is no byte string that is both "a valid X25519 PKCS8 key" and "parseable" to feed `ParseSignerPEM` | The full non-FIPS suite |
| `TestRecipientFromPublicKey_FIPSModeRefusesX25519` (`pkg/keydelivery`) | Needs a real X25519 public key to prove `RecipientFromPublicKey` refuses it; `GenerateKey` itself refuses first | The full non-FIPS suite, via the same test with its simulated-FIPS branch (`withFIPSMode`) |
| `TestCheckPublicKeyStrength_RSA`'s weak-key case (`pkg/clusterca`), `TestParseCSR_RefusesWhatItCannotIssueFor`'s `1024-bit RSA` case (`pkg/cert`), `TestParseAnchors_RefusesWeakRSA` (`pkg/trustanchor`) | `rsa.GenerateKey(.., 1024)` itself refuses under `fips140=only` | The full non-FIPS suite |
| `TestParsePrivateKeyBlock_RefusesUnknownTypesAndWeakKeys`'s weak-key case (`pkg/cert`) | Same RSA-1024 generation refusal | The full non-FIPS suite |
| `TestParseAnchors_RefusesSHA1Signature` (`pkg/trustanchor`) | `crypto/sha1` **panics** (not an error return) when `x509.CreateCertificate` is asked to sign with `SHA1WithRSA`, so the fixture cannot be built at all | The full non-FIPS suite |
| `TestRegisterOfficer_EnvelopeKEMs`'s `dhkem-x25519`/`mlkem768-x25519` cases (`pkg/ceremony`), `TestSuite_RoundTripEveryKEM`'s same two cases (`pkg/keydelivery`) | The KEM under test is itself refused by `keydelivery.LookupSuite`, which is exactly the behaviour `TestLookupSuite_FIPSModeRefusesX25519Suites` already covers directly | `TestLookupSuite_FIPSModeRefusesX25519Suites`, plus the full non-FIPS suite |

Where the X25519 path is itself under test, the test asserts the
refusal rather than skipping. Under `fips140=only` it passes only if the
refusal is the documented typed error, and any other result fails it:

| Test | What it asserts under `fips140=only` |
|---|---|
| `TestNewDecrypter_DHKEMOpensThroughTheDevice`'s `dhkem-x25519` case, `TestNewDecrypter_HybridOpensThroughTheDevice`, and the hybrid key-shape cases of `TestNewDecrypter_Refusals` (`pkg/keydelivery`) | `LookupSuite` and `NewDecrypter` both return `*SuiteNotApprovedError` |
| The `noise-nk` subtests of `TestJoinCSR_DeviceHeldKeyJoinsThroughTheSDK` and `TestJoinCSR_RefusedTokenIsAJoinRefusedError` (`sdk/go/clusterenroll`) | `JoinCSR` pinned to a Noise key alone returns `*NoiseFIPSUnavailableError` before it dials; their `p256` subtests join (or are refused the bad token) in both modes |

A test that only needs *a KEM other than the device's*, such as
`TestShareEnvelope_OpenWithDeviceKeyOfOtherKEM` (`pkg/ceremony`) or the
mismatched-decrypter case in `pkg/server/lifecycle`, uses P-384 rather than
X25519. It therefore runs in full in both modes.

`BarrierConfig.AEADProvider` accepts both nonce conventions go-quicraft's
`AEADProvider` contract defines — caller-nonce (`NonceSize() > 0`) and
module-nonce (`NonceSize() == 0`). The caller-nonce branch exists so that an
injected hardware provider following the caller-nonce convention works; without
it, such a provider is handed a nil nonce and panics.

That branch is **provably unreachable under `fips140=only`**: a caller-nonce
AEAD is one built with `cipher.NewGCM`, and that constructor is exactly what
this enforcement level refuses. No approved way exists for any provider —
software or hardware — to return such an AEAD here, so no FIPS deployment can
reach the branch. Testing it under FIPS would mean asserting the behaviour of a
configuration the module forbids, and constructing the AEAD to do so would add a
GCM-arbitrary-IV rejection to the very count this document tracks.

The skip is keyed on `fips140.Enabled()` rather than a build tag because the
FIPS job runs the same package list with no extra tags (see [The
job](#the-job)); a `fips`-keyed tag would exclude nothing. It is a skip rather
than a stub AEAD because the assertions that matter — a caller-nonce provider
opening a blob written by the module-nonce default — need real AES-GCM on both
sides; a fake cipher would exercise
the branch while proving nothing about cross-convention compatibility.

The justification also lives on `skipIfFIPS` in `pkg/seal/unsealkey_test.go`, so
it is reachable from the code as well as from here.

## Open blockers

**Read this before describing go-qrdb as FIPS 140-3 clean.** Both modules'
suites pass under `fips140=only` (see [Measured status](#measured-status)), and
the job is **not a required check**. Do not present it as an enforceable
compliance gate until it is armed; see [Arming the gate](#arming-the-gate).
Until then it measures; it is not a claim of compliance.

**No finding stands between the job and green**, but the Noise finding is
closed in this repository, not upstream. go-truststrap's `pkg/noiseproto` still
performs the Noise_NK handshake over X25519; `pkg/clusterenroll` does not reach
it when `crypto/fips140.Enabled()`, and the P-256 suite enrolls in its place
([X25519 — Noise NK enrollment](#x25519--noise-nk-enrollment-replaced-by-the-p-256-suite-in-fips-mode)).

A test fixture that reintroduces a prohibited primitive turns the job red, and
the job runs only when dispatched, so nothing reports the regression until
someone runs it. **Re-measure before quoting [Measured status](#measured-status).**

### Noise refused at run time, not compiled out

Noise is refused at run time on `fips140.Enabled()`, as `pkg/keydelivery`
refuses an X25519 envelope suite. A build tag would ship two binaries whose
enrollment surfaces differ invisibly; `GODEBUG` is a run-time switch over one
binary. Enrollment itself is not refused: a first node keeps serving the P-256
suite, SPKI-pinned TLS, DANE and direct HTTPS, and only a joining node with
Noise as its only method fails, with `*NoiseFIPSUnavailableError`
(`pkg/clusterenroll/fips.go`). Every exclusion logs at WARN with the same
sentence from either end of the enrollment.

### What remains FIPS-incompatible

| Capability | Status under `fips140=only` | Consequence |
|---|---|---|
| **Noise NK peer enrollment** (`--bootstrap-noise-key`, the Noise suite of the members' trust-anchor listener) | **unavailable; replaced by the P-256 suite** | Noise_NK_25519_ChaChaPoly_SHA256 has no FIPS-approved DH to substitute. Peers enroll over the P-256 suite (`--bootstrap-p256-key`, `--bootstrap-join-p256-key`), SPKI, DANE or direct HTTPS. |
| **Attested (anchor-mode) enrollment over Noise** — `StartAnchorAdvertiser` / `FetchAnchor` | **unavailable over Noise** | Same cause. `FetchAnchor` over the P-256 suite, SPKI, DANE or direct HTTPS is unaffected. |
| X25519 and ML-KEM-768 + X25519 share-holder envelope suites | refused | see [X25519](#x25519--share-holder-envelope-suites-refused-in-fips-mode) |
| PKCS#12 export | refused under `Enforced()` | see the [exemption register](#exemption-register) |

Everything else in `pkg/`, `cmd/` and `sdk/go` passed in the measurement below.

### Measured status

The job's three steps, and what a green run of each claims:

| Module | Step | Green means |
|---|---|---|
| root | `GOFIPS140=latest GODEBUG=fips140=only go build ./...` | the root module compiles against the FIPS module |
| root | `GOFIPS140=latest GODEBUG=fips140=only make test` | every package under `./pkg/...`, `./cmd/...` and `./test/buildgates/...` passes |
| `sdk/go` | `GOFIPS140=latest GODEBUG=fips140=only make test-sdk` | every `sdk/go` package passes, and none reports `[build failed]` |

There is no `go build ./...` step for `sdk/go` (see [The job](#the-job)); the
SDK is compiled under the FIPS module only by `go test ./...` on its way to
running the tests.

The last measurement passed all three steps with zero failures, and read zero
for each rejection class:

| Rejection | Count |
|---|---|
| `crypto/cipher: use of GCM with arbitrary IVs` | **0** |
| `crypto/ecdh: use of X25519 is not allowed` | **0** |
| `crypto/sha1` panics | **0** |
| `crypto/hmac: use of keys shorter than 112 bits` | **0** |

**A `--- SKIP` line is not a failure, and a change in the skip count is not a
regression.** Read each one against the register before reacting:

- the `caller_nonce` subtest under [Skipped tests under
  `fips140=only`](#skipped-tests-under-fips140only), which is unreachable code;
- `TestHelperProcess_Boot1Kill` (`pkg/seal/encfs`) and
  `TestHelperProcess_StandaloneNodeBoot1Kill` (`sdk/go/node`), helper-process
  entry points that run only when re-executed by their SIGKILL test;
- `TestTSEncoders_RefuseATagBlockThatOverflowsWithTheFixedFields`
  (`pkg/protocol`), which runs only where `int` is 32 bits.

A package that fails to build reports one `FAIL` line and hides every skip it
would have printed, so the skip count rises when a build failure is fixed.

**No test skips because Noise is unavailable.** Where a live Noise listener is
unreachable under `fips140=only`, the test asserts the contract that replaces
it: an advertiser that is present, serves no Noise, and never touches the key
file. A regression that read the key file in FIPS mode fails it. See
[../clustering/engineering.md](../clustering/engineering.md#test-strategy).

### Reproducing this measurement

- **Do not compare a fresh number against a recorded one.** Measure both sides
  from the same base commit, on the same machine, in one sitting, so a delta is
  attributable to the change under test.
- **Re-measure after the last commit that touches test code.** A test fixture
  that builds its AEAD with `cipher.NewGCM` turns the job red without touching
  a product path.
- **Check the log is complete before counting it.** A suite killed midway
  produces a short count that looks like an improvement.
- **Attribute every failing test before counting it.** A package that reports
  `[build failed]` prints one `FAIL` line among the `ok` lines and looks like a
  rejection until you read it; rerun it with FIPS off to tell them apart.

Zero is also what a broken grep returns, so re-derive each rejection count
rather than accepting the table:

```
GOFIPS140=latest GODEBUG=fips140=only make test 2>&1 | grep -c 'crypto/cipher: use of GCM with arbitrary IVs'
GOFIPS140=latest GODEBUG=fips140=only make test 2>&1 | grep -c 'crypto/ecdh: use of X25519 is not allowed'
```

### Arming the gate

**No finding blocks arming; the job is deliberately not armed.** It waits on a
stability record.

- **One green run is not a track record.** A check made required on its first
  pass starts being bypassed the first time it fails for a reason nobody can
  attribute, and a bypassed gate is worse than none.
- **The job is the wrong shape for a pull-request check.** It runs `make test`
  under the race detector with a 60-minute timeout. Armed per pull request, its
  slowest run is added to every pull request; armed in the full tier, to every
  push to `main`.

**The record: fourteen consecutive green runs on `main`.** Not runs on a
branch: the record counts the thing being required.

- **The record must exclude a flake, and this suite is where one would hide.**
  It runs long race-detector packages under `fips140=only`. A defect that fails
  one run in ten survives five consecutive runs unseen about 59% of the time and
  fourteen about 23%.
- **Fourteen runs span a moving tree.** They test whether the job survives
  other merges, not one tree's determinism.

Arming is four edits, made together when the record is in:

1. Move the job from `.github/workflows/nightly.yml` into
   `.github/workflows/ci.yml`, with the full-tier `if:` unless it is armed per
   pull request.
2. Add `fips` to `ci-gate`'s `needs`.
3. Add `RESULT_FIPS: ${{ needs.fips.result }}` to `ci-gate`'s `env` block.
4. Add `check fips "$RESULT_FIPS" <pr|code|full>` to its loop.

`test/buildgates/ci_tiers_test.go` refuses a `ci-gate` job whose `needs`,
`RESULT_` variables and `check` lines disagree. Until the job is armed, a FIPS
job that can pass and is not required is a fail-open gate, carried
deliberately for the length of the record.

## See Also

- [compliance.md](compliance.md) — FIPS 140-3 / FedRAMP / PCI-DSS / HIPAA requirement matrices
- [shamir-custody](../shamir-custody/) — share-holder envelopes and the HPKE suites
- [Go FIPS 140-3 documentation](https://go.dev/doc/security/fips140) — `GOFIPS140`, `GODEBUG=fips140`, and the module's approved-algorithm list
- [NIST SP 800-38D](https://csrc.nist.gov/publications/detail/sp/800-38d/final) — GCM, including the IV construction requirement
- [NIST SP 800-107 Rev. 1](https://csrc.nist.gov/publications/detail/sp/800-107/rev-1/final) — §5.3.4, HMAC key length
- [RFC 7292](https://www.rfc-editor.org/rfc/rfc7292) — PKCS#12, including the `localKeyId` attribute
