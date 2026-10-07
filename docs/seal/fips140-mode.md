<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# FIPS 140-3 Mode (`GODEBUG=fips140=only`)

This document records how go-qrdb is verified against the Go Cryptographic
Module, what had to change on the way to approved-algorithm clean, the
exemption register for anything that is deliberately not clean, and — in
[Open blockers](#open-blockers) — exactly how far from clean the module still
is. **Both modules' suites now pass under `fips140=only`, and the gate is still
not armed**, so do not read this document as a compliance claim: an unarmed
job's result is a measurement, not an enforceable property. What arming now
waits on, and why it is no longer the list of findings this document used to
name, is in [Arming the gate](#arming-the-gate).

## The job

CI runs a nightly `FIPS 140-3` job that executes the full unit suite of **both
modules** under:

```
GOFIPS140=latest GODEBUG=fips140=only go build ./...
GOFIPS140=latest GODEBUG=fips140=only make test
GOFIPS140=latest GODEBUG=fips140=only make test-sdk
```

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
FedRAMP / PCI DSS v4 / HIPAA gate has to run. Ratified by the owner 2026-08-17
(handbook `software/conventions/configuration.md` §5).

The job scope is deliberately identical to the ordinary gate — the same
`make test` **and `make test-sdk`** targets with the same build tags. A FIPS job
over a subset of the suite does not tell you the product is FIPS-clean.

**`make test-sdk` is not optional decoration.** `sdk/go` is a separate Go
module, so `make test` (`./pkg/... ./cmd/...`) does not reach a single line of
it. The job ran `make test` alone until 2026-09-02, which means the entire SDK —
every transport, `sdk/go/node`, the driver, the DAO layer and the conformance
matrix — **had never executed under `fips140=only` at all**. It does now, and
the SDK's first clean run is recorded in [Measured status](#measured-status);
read any older revision of this document as asserting nothing about the SDK.

The sdk step runs even when the root suite fails, deliberately. The job was
known-red on the root module for the whole of its history until 2026-09-12, so
a step that only ran after a green `make test` would never have run at all, and
the SDK would have stayed unmeasured while the workflow appeared to cover it.
Either module failing fails the job. Keep the arrangement now that both are
green: it is what makes the next regression on the root module cost one
measurement rather than two.

The job is **not** in `ci-gate`'s `needs` and is therefore not a required
check. No finding keeps it red any more — see [Measured
status](#measured-status) — so what is left is a decision rather than a
blocker, and [Arming the gate](#arming-the-gate) states it, what it now waits
on, and the exact change that makes the job required.

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

The SHA-1 in the deterministic role and custodian identifiers (`uuid.NewSHA1`)
was carried separately, under its own issues (#8, #10), and has since landed:
both derivations are RFC 9562 §5.8 UUIDv8 over SHA-256, with a dual-read path
for identifiers persisted before the migration. It is recorded under
[UUIDv8 identifiers](#uuidv8--sha-1-role-and-custodian-identifiers) below
rather than in [Open blockers](#open-blockers), where it used to sit.

**There is no non-approved primitive left on a go-qrdb call site, and no
non-approved primitive is reached in a dependency either.** The `go-quicraft`
barrier AEAD accounted for 693 rejections until the v0.8.0-alpha bump closed
that class; `go-truststrap`'s Noise handshake accounted for the last 15 until
`pkg/clusterenroll` stopped calling it in FIPS mode. That X25519 is still in
the dependency — it is simply not on any path this module takes when
`crypto/fips140.Enabled()`. See [Open blockers](#open-blockers) for the ruling
and for what remains permanently unavailable as a result.

### AES-GCM — `cipher.NewGCM` → `cipher.NewGCMWithRandomNonce`

SP 800-38D requires the module to own IV generation, so `cipher.NewGCM`
(arbitrary caller-supplied IVs) is rejected under `only`. Every construction
moved to `cipher.NewGCMWithRandomNonce`, which samples a 12-byte nonce itself
and prepends it to the ciphertext.

| Call site | Role |
|---|---|
| `pkg/crypto/aesgcm/aesgcm.go` | shared AES-256-GCM helper; the tenant DEK keyring (`pkg/dek`) encrypts through it |
| `pkg/seal/encfs/block.go` | encrypted-filesystem block cipher |
| `pkg/keywrap/software.go` (`SoftwareProvider.{Wrap,Unwrap}`) | the backup key-wrap provider; found during the 2026-10-06 nightly regression sweep, not part of the original remediation |
| `pkg/service/backup_service.go` (`encryptShard`/`decryptShard`) | the backup shard cipher; same discovery |

Share envelopes are HPKE (`crypto/hpke`, see below) and construct no GCM of
their own; the two earlier share-envelope sites (`pkg/custodian` and the
external-officer envelope) have been removed.

**No migration is required.** Every site already framed their output as
`nonce (12) || ciphertext || tag (16)` with a nonce drawn from `crypto/rand`,
which is byte-for-byte what `NewGCMWithRandomNonce` produces. Ciphertext
written by an older build opens under the new code and vice versa. The
envelope formats that carried the nonce in a separate field split the
module-generated prefix back off, so those wire formats were unchanged as
well — which matters, because the custodian-side inverse is third-party
client software (see [shamir-custody](../shamir-custody/)).

Note that `NewGCMWithRandomNonce` reports `NonceSize() == 0` and
`Overhead() == 28`. Code that framed on `gcm.NonceSize()` now uses a named
12-byte constant.

The construction documents a limit of 2^32 Seal operations per key, to keep the
probability of a 96-bit random-nonce collision negligible. This is not a new
constraint: every one of these call sites already drew a random 96-bit nonce
per message from `crypto/rand`, so the collision bound is unchanged. The
tightest case is `pkg/seal/encfs`, whose AEAD is bound to one file's DEK and
seals once per 4 KiB block write — 2^32 blocks is 16 TiB of writes through a
single DEK.

### X25519 — Noise NK enrollment excluded in FIPS mode

Noise_NK_25519_ChaChaPoly_SHA256 mandates X25519 key agreement, which
`crypto/ecdh` rejects under `only`. There is no approved DH to substitute, so
the METHOD is what goes rather than the primitive within it.

`pkg/clusterenroll` reads `crypto/fips140.Enabled()` through
`clusterenroll.NoiseAvailable()` and, when it is false:

- a **first node** starts its advertiser WITHOUT the Noise listener and serves
  the approved methods — SPKI-pinned TLS, DANE and direct HTTPS all fetch the
  same bundle from `/v1/ca/bootstrap` — writing no `noise-key.hex`;
- a **joining node** has Noise dropped from the truststrap method order when an
  approved method is also configured;
- a **joining node with Noise as its only method** is refused with
  `*NoiseFIPSUnavailableError`, before the first dial and before anything is
  written to the data directory.

Every exclusion logs at WARN naming FIPS. `Enabled()` rather than `Enforced()`,
for the reason given below for share-holder envelope suites.

The cost is stated plainly in
[../clustering/architecture.md](../clustering/architecture.md): a FIPS
deployment cannot use the Noise bootstrap method, in either the shared-CA-key or
the attested anchor mode, and that is permanent rather than pending an upstream
fix.

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
**panics** on a shorter key under `only`. `pkg/discovery` enforced no minimum on
`MulticastConfig.SharedSecret`, so a node configured with a short secret took
the process down on its first announcement.

`discovery.MinSharedSecretLen` is now 16 bytes, checked in
`EncodeMulticastMessage` and `DecodeMulticastMessage` — ahead of the primitive,
because a panic cannot be recovered into a config error — and surfaced as a
`*ValidationError`. `NewMulticastDiscovery` fails at construction rather than
in the announcement goroutine.

**This tightens the configuration contract.** A deployment whose
`multicast-secret` is shorter than 16 bytes must lengthen it. The secret is the
only thing authenticating membership announcements, so a short one was never
acceptable.

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

### UUIDv8 — SHA-1 role and custodian identifiers

`pkg/role.deterministicUUID` and the matching derivation in the since-removed
`pkg/custodian` built their stable identifiers with `uuid.NewSHA1`, an RFC 4122 v5 name-based
UUID. `crypto/sha1` **panics** under `only`, and both derivations run during
package initialization and tenant seeding, so this was the widest of the
findings: it took out `pkg/role`, `pkg/auth/rbac`, `pkg/bootstrap`,
`pkg/firstboot`, the since-removed `pkg/custodian` and every package that seeds a tenant — and,
because the panic happens before a test body runs, it reached any downstream
consumer that authorized through `pkg/role` at all.

Both derivations are now RFC 9562 §5.8 **UUIDv8 over SHA-256**. UUIDv8 is the
version reserved for application-defined derivations, which is the only place a
SHA-256 name-based UUID can live: RFC 4122 fixes v3 to MD5 and v5 to SHA-1. The
inputs are length-prefixed before hashing so the encoding is injective —
without framing, tenant `a/b` with role `c` and tenant `a` with role `b/c`
would collide on one identifier.

**These identifiers are persisted data** — role primary keys, both secondary
indexes, and the contents of every user record's `RoleIDs`. There is no
surviving compatibility layer for a pre-migration (UUIDv5/SHA-1) identifier:
every cluster is provisioned, or re-provisioned, under the v8 scheme, and the
SO deny-list and the separation-of-duties Owner strip match a role id against
its canonical, deterministic `role.SystemRoleID` derivation directly.

### KDF selection — a test that pinned the toolchain instead of the selector

`kdf.DefaultHasher` picks PBKDF2-SHA256 under FIPS and Argon2id otherwise,
which is correct in both modes: Argon2id is not an approved KDF.
`TestDefaultHasher_NonFIPSMode` asserted the literal Argon2id version after
clearing `GOFIPS140`, and that premise does not hold in a FIPS binary —
`seal.FIPSEnabled` consults `crypto/fips140.Enabled` before it falls back to
the variable, and `GOFIPS140` selects the module at BUILD time, so `t.Setenv`
cannot retract at run time what the linker already decided.

The expectation now routes through the `wantVersion` helper the package's other
FIPS-selection tests already use, which folds `fips140.Enabled()` into the
answer. Nothing is skipped and no coverage is dropped: the test still asserts
Argon2id on an ordinary run and asserts PBKDF2-SHA256 under the FIPS job.

## The nightly's first real regression

Both modules' suites were measured clean on 2026-09-12 (run 17302) and the
nightly went red again on every scheduled run from 2026-09-30 through
2026-10-06. The gap between those two dates is the v0.43/v0.44 trains
(tenant onboarding v2, chunked import, volume pools, the PKCS#12
server-export removal, trust-anchor bounds checking, and the strict
separation-of-duties Owner-strip) -- substantial feature work that never ran
under `fips140=only` before landing, because arming the job is still gated
on the stability record in [Arming the gate](#arming-the-gate) and nobody
runs a nightly-only job by hand. Fixed 2026-10-06; every class of defect
found is recorded below so the next regression of this shape is recognised
faster.

**The defects, in the order a reader would hit them:**

1. **A genuine production defect**, the only one of the four classes: two
   call sites still built their AEAD with `cipher.NewGCM` instead of
   `cipher.NewGCMWithRandomNonce` --
   `pkg/keywrap.SoftwareProvider.{Wrap,Unwrap}` (the backup key-wrap
   provider) and `pkg/service.{encryptShard,decryptShard}` (the backup
   shard cipher). Both are byte-compatible migrations identical in shape to
   the [AES-GCM remediation](#aes-gcm--ciphernewgcm--ciphernewgcmwithrandomnonce)
   above: every site already framed `nonce (12) || ciphertext || tag (16)`
   with a random nonce, which is exactly what the random-nonce constructor
   produces, so no wire format or stored backup changed shape.
2. **Ignored key-generation errors causing a nil-pointer panic**, three
   times over, all the same shape: a test did `key, _ := ecdh.X25519().GenerateKey(...)`,
   the call fails under `fips140=only` (X25519 is not approved), and the
   ignored error left `key` nil for the next line to dereference --
   `pkg/authfactors/mtls` (`TestParseSignerPEM`, the panic found by the
   nightly regression sweep) and two more in `pkg/keydelivery`
   (`TestRecipientFromPublicKey_MapsCurvesAndRefusesTheRest`,
   `TestPrivateKeyFor_EnforcesTheSuite`) that a FIRST panic in that
   package's own `TestRecipientFromPublicKey_FIPSModeRefusesX25519` had been
   hiding: an unrecovered panic kills the whole test binary, so every test
   after the first one in the package never ran and reported nothing --
   "FAIL" said less than it should have, the same lesson [Measured
   status](#measured-status) already draws about a build failure hiding
   every skip behind it. Every one of the three now checks the error and
   either omits the X25519 case when generation is refused or asserts the
   refusal directly (`TestRecipientFromPublicKey_FIPSModeRefusesX25519`,
   `pkg/authfactors/mtls/sign_test.go`'s `x25519_cannot_sign` subtest) --
   the same "Skipped tests" shape as the caller-nonce AEAD case below, just
   newly discovered.
3. **Test fixtures that could not be constructed under strict enforcement**
   at all: a 1024-bit RSA key (`rsa.GenerateKey` itself refuses under
   `fips140=only`, in `pkg/clusterca`, `pkg/cert`), and a SHA-1-signed
   certificate (`crypto/sha1` **panics** rather than erroring, in
   `pkg/trustanchor`'s `TestParseAnchors_RefusesSHA1Signature` -- the
   second panic found in the same regression sweep). `pkg/trustanchor`'s sibling weak-key
   test already skipped cleanly on the RSA generation error; the SHA-1 test
   did not, because the panic happens before any error can be checked, so
   the fix checks `fips140.Enforced()` up front instead of relying on an
   error return that never arrives.
4. **Test fixtures that picked a KEM or transport incidental to what they
   were actually testing**: `pkg/ceremony`, `cmd/qrdb` and
   `sdk/go/conformance` fixtures built a holder's SEPARATE envelope key
   (needed because the holder's certificate key -- Ed25519, or nothing in
   particular -- has no DHKEM mapping) with `keydelivery.KEMX25519` or the
   ML-KEM-768 hybrid specifically, by arbitrary choice rather than because
   the KEM was the subject. Both are refused under `fips140=only`
   ([X25519](#x25519--share-holder-envelope-suites-refused-in-fips-mode)).
   Fixed by substituting the always-approved `KEMP256` at exactly these
   incidental call sites; `pkg/ceremony`'s `TestRegisterOfficer_EnvelopeKEMs`
   and `pkg/keydelivery`'s `TestSuite_RoundTripEveryKEM`, where the KEM IS
   the subject, keep exercising X25519 and the hybrid everywhere except
   under real `fips140=only`, where they skip those two cases with the
   refusing error type named in the skip reason.
5. **Tests of the member-side CSR join server that need a REAL, working
   Noise NK listener**, not merely the gating decision. `pkg/clusterenroll`
   already had `forceNoiseAvailable` to simulate both rulings of its own
   `noiseAvailable` package variable regardless of the real runtime
   (precisely so skips are not needed for the gating logic itself -- see
   that variable's doc comment) -- but forcing the variable past a REAL
   `fips140=only` runtime does not make `go-truststrap`'s
   `noiseproto.GenerateStaticKey` able to generate an X25519 key; it just
   moves the failure one call deeper, into `StartJoinServer` itself, which
   this lane learned by trying it. `pkg/clusterenroll/fips_test.go` now
   also carries `skipIfNoiseUnavailable`, keyed on the real
   `NoiseAvailable()`, for every test that needs the listener to actually
   come up: roughly twenty of them, across `join_csr_test.go`,
   `join_fault_test.go`, `join_test.go` and `ca_key_never_leaves_test.go`,
   plus the same shape one level up in `pkg/server/lifecycle`, `cmd/qrdbd`
   and `sdk/go/node` (each importing `clusterenroll.NoiseAvailable()`
   directly, since `forceNoiseAvailable` is unexported). This is the same
   "permanently unreachable" conclusion as [Noise NK peer
   enrollment](#x25519--noise-nk-enrollment-excluded-in-fips-mode) above,
   just reached from the join SERVER side rather than the advertiser side.
6. **A capability gap neither of the other five classes names**, closed on
   2026-10-06: see [Strict separation of duties and `cluster:join_token`
   under FIPS](#strict-separation-of-duties-and-clusterjoin_token-under-fips)
   below.

Reproduced and fixed together because the SDK module's suite
(`make test-sdk`) had never actually been run under `fips140=only` in this
exercise before either -- the local reproduction that found class 6 only
happened because this lane ran both modules by hand, the same gap
[The job](#the-job) records about the nightly having silently skipped
`make test-sdk` for its first ten months.

### Strict separation of duties and `cluster:join_token` under FIPS

**Fixed 2026-10-06 (owner ruling); the gap below is history, not the current
state.** `permission.ClusterJoinToken` is `ScopeCross` and, by design
(`pkg/permission/cluster_join_token_scope.go`), was carried by **no built-in
role** -- only the `owner` role, which holds every permission through
`permission.All()` rather than a role grant, minted a join token by default.
Separately and for an unrelated reason, `pkg/auth/rbac.RBACAuthorizer.
stripOwnerRole` voids the `owner` role ID outright whenever strict
separation of duties is active (`docs/12-configuration.md`'s
`separation_of_duties: true`, "defense in depth") -- and `sdk/go/node/node.go`
computes that posture as `fips140.Enabled() || cfg.Auth.SeparationOfDuties`,
so **a FIPS build is always strict regardless of configuration** (same rule
as `noiseAvailable` above, applied to a different permission).

Composing those two, independently-correct rules produced a result neither
one states on its own: **in a FIPS build, no identity succeeded at
`cluster:join_token` through the default role set, including the system
tenant's own Owner.** `sdk/go/conformance`'s
`TestMintJoinToken_SystemOwnerSucceeds_TenantAdminRefused` and
`TestMintJoinToken_EmbeddedCapabilityExists` asserted the opposite
unconditionally and went red the moment they first ran under a real FIPS
build; both branched on `fips140.Enabled()` and asserted the SAME
permission-denied refusal already asserted for the tenant Admin case,
rather than success -- the state this page previously recorded as "not
remediated, only documented and tested".

**The fix is the grant, not a bypass of the strip.** The cluster **Admin**
built-in role (`permission.RoleAdmin`) now carries `permission.
ClusterJoinToken` unconditionally -- in every posture, not only under FIPS --
registered in `registerAdminGrants` (`pkg/permission/builtin_role_grants.go`)
and pinned by the golden-set drift guard in
`pkg/role/builtin_grants_equivalence_test.go`. `stripOwnerRole` only ever
removed the `owner` role id from an identity's effective roles; it never
touched Admin, so a system-tenant Admin was never affected by the strip and
mints a join token exactly as before. `TestClusterJoinToken_IsPlatformOnly`
now pins the Admin grant directly (`HasRoleGrant(RoleAdmin, ClusterJoinToken)`)
alongside the existing proof that SO, Operator, User and Auditor still carry
nothing: Admin is never a Security Officer and never a custodian, so FIPS
140-3 AS10.03 separation of duties is not reopened by this grant, and
`ScopeCross` still refuses a tenant's own Admin -- the capability remains
platform-only, just no longer Owner-only.

**The sanctioned path is now exercised for the system tenant, closing the
"unverified" note this page previously carried.**
`TestMintJoinToken_SystemOwnerSucceeds_TenantAdminRefused` gained a third
subtest, `system-admin-succeeds`, that boots a system-tenant Admin and
asserts `MintJoinToken` succeeds unconditionally (not gated on
`fips140.Enabled()`, because the grant holds in every posture), on every
remote transport. `TestMintJoinToken_EmbeddedCapabilityExists` now binds a
system-tenant Admin identity instead of Owner and asserts success
unconditionally rather than branching on FIPS. The `system-owner` subtest
and `TestMintJoinToken_ExplicitTenantGrantStillRefused` are unchanged and
remain correct: the bare system Owner is still voided by strict separation
of duties (Owner carries the permission only through `All()`, which the
strip empties), and a tenant-scoped explicit grant is still refused by
`ScopeCross` regardless of what the role carries -- neither claim depended
on whether Admin held the permission.

Verified under `GOFIPS140=latest GODEBUG=fips140=only` for the touched
packages: `go test ./pkg/permission/... ./pkg/role/...` in the root module
and `go test ./sdk/go/conformance/...` in `sdk/go`. This is a targeted
re-run, not a new full-suite measurement -- see [Reproducing this
measurement](#reproducing-this-measurement) before treating [Measured
status](#measured-status) as current for anything beyond these packages.

## Exemption register

One exemption. Every other non-approved primitive in this repository is either
remediated above or open with a tracking issue in
[Open blockers](#open-blockers) — none is silently tolerated.

A row here requires: the citation of the external format or protocol that
mandates the primitive, evidence that it protects nothing regulated, and the
narrowest isolation that keeps it out of the FIPS-gated path.

| Primitive | Call site | Mandating citation | Isolation |
|---|---|---|---|
| SHA-1, as the `localKeyId` certificate fingerprint | `pkg/cert/pkcs12.go` → `software.sslmate.com/src/go-pkcs12` | [RFC 7292](https://www.rfc-editor.org/rfc/rfc7292) §4.2 / PKCS#12 v1.1 attribute conventions; the SHA-1 fingerprint as `localKeyId` is what OpenSSL, Java keytool and Windows CryptoAPI use to bind a key to its certificate | `encodePKCS12` returns `*PKCS12FIPSUnavailableError` when `fips140.Enforced()`, so the call never executes in the FIPS-gated path. The archive's own protection (PBKDF2-HMAC-SHA256 + AES-256-CBC) is approved and unchanged. |

### Skipped tests under `fips140=only`

**Two tests skip, and neither is a masked failure.** A skip in this suite is
normally the thing the [Handling a failure](#handling-a-failure) section
forbids, so the distinction has to be written down rather than inferred from a
count: read `--- SKIP` here as *unreachable code*, not *hidden red*.

| Test | Skipped because | Where it IS covered |
|---|---|---|
| `TestWrappedRootKey_CallerNonceProvider` | Constructs a caller-nonce AEAD (`cipher.NewGCM`) to exercise the caller-nonce branch of `sealWrapped`/`openWrapped` | The full non-FIPS suite |
| `TestWrappedRootKey_RejectsMismatchedProviderNonceWidth` | Same, via `cipher.NewGCMWithNonceSize` | The full non-FIPS suite |
| `TestOpenWrapped_*` `/caller_nonce` subtests (`pkg/seal/protected_rootkey_test.go`) | Same caller-nonce `cipher.NewGCM` construction as the two rows above, reimplemented in a newer fixture that did not reuse `skipIfFIPS`; now does | The `/module_nonce` subtest of each, plus the full non-FIPS suite |
| `TestParseSignerPEM`'s `x25519_cannot_sign` subtest (`pkg/authfactors/mtls`) | `ecdh.X25519().{GenerateKey,NewPrivateKey}` both refuse under `fips140=only`, so there is no byte string that is both "a valid X25519 PKCS8 key" and "parseable" to feed `ParseSignerPEM` | The full non-FIPS suite |
| `TestRecipientFromPublicKey_FIPSModeRefusesX25519` (`pkg/keydelivery`) | Needs a real X25519 public key to prove `RecipientFromPublicKey` refuses it; `GenerateKey` itself refuses first | The full non-FIPS suite, via the same test with its simulated-FIPS branch (`withFIPSMode`) |
| `TestCheckPublicKeyStrength_RSA`'s weak-key case (`pkg/clusterca`), `TestParseCSR_RefusesWhatItCannotIssueFor`'s `1024-bit RSA` case (`pkg/cert`) | `rsa.GenerateKey(.., 1024)` itself refuses under `fips140=only`, same shape as `TestParseAnchors_RefusesWeakRSA` already in this table | The full non-FIPS suite |
| `TestParsePrivateKeyBlock_RefusesUnknownTypesAndWeakKeys`'s weak-key case (`pkg/cert`) | Same RSA-1024 generation refusal | The full non-FIPS suite |
| `TestParseAnchors_RefusesSHA1Signature` (`pkg/trustanchor`) | `crypto/sha1` **panics** (not an error return) when `x509.CreateCertificate` is asked to sign with `SHA1WithRSA`, so the fixture cannot be built at all | The full non-FIPS suite |
| `TestRegisterOfficer_EnvelopeKEMs`'s `dhkem-x25519`/`mlkem768-x25519` cases (`pkg/ceremony`), `TestSuite_RoundTripEveryKEM`'s same two cases (`pkg/keydelivery`) | The KEM under test is itself refused by `keydelivery.LookupSuite`, which is exactly the behaviour `TestLookupSuite_FIPSModeRefusesX25519Suites` already covers directly | `TestLookupSuite_FIPSModeRefusesX25519Suites`, plus the full non-FIPS suite |
| ~20 tests needing a real Noise NK listener across `pkg/clusterenroll` (`join_csr_test.go`, `join_fault_test.go`, `join_test.go`, `ca_key_never_leaves_test.go`), `pkg/server/lifecycle`, `cmd/qrdbd` and `sdk/go/node` | `StartJoinServer`/`newTestJoinServer` need `noiseproto.GenerateStaticKey` (X25519) to actually succeed, not merely the gating decision to say yes; this is class 5 from the nightly regression described above | The full non-FIPS suite |

`BarrierConfig.AEADProvider` accepts both nonce conventions go-quicraft's
`AEADProvider` contract defines — caller-nonce (`NonceSize() > 0`) and
module-nonce (`NonceSize() == 0`). The caller-nonce branch exists so that an
injected hardware provider following the older convention keeps working; without
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
opening a blob written by the module-nonce default, and opening the checked-in
pre-bump fixture — need real AES-GCM on both sides; a fake cipher would exercise
the branch while proving nothing about cross-convention compatibility.

The justification also lives on `skipIfFIPS` in `pkg/seal/unsealkey_test.go`, so
it is reachable from the code as well as from here.

## Open blockers

**Read this before describing go-qrdb as FIPS 140-3 clean.** Both modules'
suites now pass under `fips140=only` (see [Measured
status](#measured-status)), and the job is **still not a required check**. Do
not present it as an enforceable compliance gate until it is armed — see
[Arming the gate](#arming-the-gate), which is now a decision about a stability
record rather than a list of findings. Its purpose until then is to measure,
nightly, where the number cannot be quietly lost. (It ran on
every pull request until the known-red and long-horizon jobs moved off the push
path; a known-red job burning a runner slot per pull request produced a number
we already had.) That is a strict improvement over having no FIPS coverage at
all; it is not a claim of compliance.

**No finding now stands between the job and green** — but both of the last two
were closed **in this repository**, and neither upstream, so the distinction is
worth keeping rather than flattening into "clean".

The Noise X25519 handshake is closed here because `pkg/clusterenroll` no longer
reaches those call sites under `crypto/fips140.Enabled()`. go-truststrap still
performs X25519 in `pkg/noiseproto`; the method is *unavailable* to a FIPS
deployment, not substituted.

The `cipher.NewGCM` fixture is closed here by replacing the constructor.

**How the second one arrived is the part to remember.** This page previously
said "one finding, and it is in a dependency", and that was true when it was
measured. A test fixture added afterwards built its AEAD with `cipher.NewGCM`,
four `pkg/tenant` tests went red under `fips140=only`, and this page went on
asserting that the only remaining distance was upstream. **Nothing surfaced the
divergence**: the job that catches it is nightly, and nobody receives a nightly
result. It was found by a lane that happened to be measuring FIPS for the other
finding.

So the measurement on this page is only as current as the last time somebody
looked. **Re-measure before quoting it**, and treat "the job should now be
green" as a prediction to check rather than a property to state. That
prediction has since been checked and held — the figures in [Measured
status](#measured-status) are a run, not an expectation — which changes nothing
about the rule: the next fixture that reintroduces a prohibited primitive will
turn this page false again in exactly the same silence.

| Finding | Call sites | Status |
|---|---|---|
| `go-truststrap` `pkg/noiseproto` performs the Noise_NK handshake over `noise.DH25519` | `session.go`, `keys.go` | **closed here**: `pkg/clusterenroll` no longer reaches those call sites when `crypto/fips140.Enabled()`. Still open upstream, and still the reason the method is unavailable rather than substituted. |
| `pkg/tenant`'s teardown-fence test fixture built its AEAD with `cipher.NewGCM` | `teardown_fence_test.go` | **closed here**: the fixture uses `cipher.NewGCMWithRandomNonce`, which generates and prefixes the nonce itself. Test fixture only; no product path was involved. |
| `go-quicraft` `pkg/seal` built the barrier AEAD with `cipher.NewGCM` | — | **closed** by the v0.8.0-alpha bump; see below |

### The Noise finding — what was decided, and what remains true

The earlier text of this section read: *"the question is what a FIPS deployment
does about cluster enrollment, and it is a product decision rather than an
engineering one. The two candidate answers on the table are compiling
`noiseproto` out under the `fips` tag [...] or having `pkg/clusterenroll` refuse
enrollment when `fips140.Enabled()` [...] Neither is go-qrdb's call to make."*

**It was ruled, and it is the second answer with one correction.** Noise is
refused at run time on `fips140.Enabled()`, as `pkg/keydelivery` refuses an
X25519 envelope suite — not compiled out under a build tag, because a build tag would
ship two binaries whose enrollment surfaces differ invisibly while `GODEBUG` is
a run-time switch. The correction is that **enrollment itself is not refused**:

- A **first node** excludes Noise and advertises the approved methods —
  SPKI-pinned TLS, DANE and direct HTTPS, all of which fetch the same CA bundle
  from `/v1/ca/bootstrap`. The operator never chose a transport (there is no
  advertise-method option), so refusing the node's whole reason for existing
  over a method a FIPS deployment could not use would have been the wrong
  trade.
- A **joining node** that named Noise alongside an approved method has Noise
  dropped from the candidate list.
- A **joining node that named Noise and nothing else** IS refused, at
  configuration time, with an error whose first clause names FIPS.

Every exclusion logs at WARN. A bootstrap method downgraded in silence is its
own defect.

**The error message was the other half of the finding.** `noiseproto`'s
`newStaticKey` wraps any key-derivation failure in `ErrInvalidKeySize`, so a
FIPS refusal used to surface as

```
noise: invalid key size: crypto/ecdh: use of X25519 is not allowed in FIPS 140-only mode
```

whose leading clause sends an operator after a key-length bug that is not there.
go-qrdb now refuses above that call, so the string is unreachable from a FIPS
deployment of this repository; the mis-wrapping itself is a defect in the
dependency and is filed there.

### What remains FIPS-incompatible

Naming it precisely, because this was invisible for eleven nightly runs and an
accurate gap list is worth more than a claim of completeness:

| Capability | Status under `fips140=only` | Consequence |
|---|---|---|
| **Noise NK peer enrollment** (`--bootstrap-noise-key`, the first node's Noise listener) | **unavailable, permanently** | Noise_NK_25519_ChaChaPoly_SHA256 has no FIPS-approved DH to substitute. Peers enroll over SPKI, DANE or direct HTTPS. This does not go away when the dependency is fixed — there is nothing to fix it to. |
| **Attested (anchor-mode) enrollment over Noise** — `StartAnchorAdvertiser` / `FetchAnchor` | **unavailable over Noise** | Same cause. `FetchAnchor` over SPKI / DANE / direct HTTPS is unaffected. |
| X25519 and ML-KEM-768 + X25519 share-holder envelope suites | refused | see [X25519](#x25519--share-holder-envelope-suites-refused-in-fips-mode) |
| PKCS#12 export | refused under `Enforced()` | see the [exemption register](#exemption-register) |

Everything else in `pkg/` and `cmd/` is approved-algorithm clean as measured
below, and so, now, is `sdk/go` — see [Measured status](#measured-status).

### Measured status

**Measured 2026-09-12, Go 1.26.7, linux/amd64, commit `133fa56`, scheduled
nightly run 17302 on `main`.** The same tree had been dispatched half an hour
earlier as run 17271 and both agree exactly; the figures below are from the
scheduled run because that is the one that runs without anyone asking.

**These figures describe commit `133fa56` and no other.** The tree has moved
since, and the movement includes test files — the class of change that has
turned this page false twice, which is why [Reproducing this
measurement](#reproducing-this-measurement) says to re-measure after the last
commit that touches test code. They have not been re-taken against a later
commit. Read them as the last measurement, not as the current state: the
current state is whatever the most recent scheduled nightly reports, and
nothing on this page can report it for you.

What run 17302 ran, step by step, and therefore what this table can claim:

| Module | Step | Result on run 17302 |
|---|---|---|
| root | `GOFIPS140=latest GODEBUG=fips140=only go build ./...` | clean |
| root | `GOFIPS140=latest GODEBUG=fips140=only make test` | **126 packages pass, 0 fail** |
| `sdk/go` | `GOFIPS140=latest GODEBUG=fips140=only make test-sdk` | **18 packages pass, 0 fail**; 3 more have no test files, and no package reported `[build failed]` |

There is no `go build ./...` row for `sdk/go` because the job has no such step
(see [The job](#the-job)). The only compilation the SDK received under the FIPS
module is the one `go test ./...` performs on its way to running the tests, and
that is the whole of what the third row says. An earlier revision of this table
had a `go build ./...` column reading "clean" for `sdk/go`; that was a claim
about a step the job does not run, and it is withdrawn.

Rejection counts on that run, all four classes this document has ever recorded:

| Rejection | Count |
|---|---|
| `crypto/cipher: use of GCM with arbitrary IVs` | **0** |
| `crypto/ecdh: use of X25519 is not allowed` | **0** |
| `crypto/sha1` panics | **0** |
| `crypto/hmac: use of keys shorter than 112 bits` | **0** |

**This is the SDK's first measurement and it is clean.** Until 2026-09-02 the
job did not invoke `make test-sdk` at all, so the entire SDK — every transport,
`sdk/go/node`, the driver, the DAO layer and the conformance matrix — had never
executed under `fips140=only`. It has now, over 18 packages, with zero
failures and zero rejections. Any statement about the SDK in a revision of this
document older than this section was about nothing.

Both remaining root-module failures are gone. The trajectory, so the next
person can tell a real movement from a re-run:

| When | Root | Notes |
|---|---|---|
| earlier | 106 pass / 16 fail | 699 GCM rejections, almost all inside a dependency |
| after the dependency bump | 120 pass / 2 fail | 0 GCM, 15 X25519 |
| after `pkg/clusterenroll` stopped calling Noise in FIPS mode | 124 pass / 2 fail | 0 X25519; GCM back to **4** from a test fixture added in between |
| **2026-09-12, run 17302** | **126 pass / 0 fail** | the fixture now uses `cipher.NewGCMWithRandomNonce`; `pkg/service` compiles again |

The GCM count going from 0 back to 4 midway is why [Reproducing this
measurement](#reproducing-this-measurement) says to re-measure after the last
commit that touches test code. It has happened twice.

**Seven tests report `--- SKIP` on this run, and the number went up while the
suite got better — read it before reacting to it.** Four were already
registered: `TestHelperProcess_Boot1Kill`,
`TestTSEncoders_RefuseATagBlockThatOverflowsWithTheFixedFields`, and the two
caller-nonce tests under [Skipped tests under
`fips140=only`](#skipped-tests-under-fips140only), which are unreachable code
rather than hidden red. `TestHelperProcess_StandaloneNodeBoot1Kill` is the same
shape as the other helper-process entry point. The remaining two —
`TestBackupService_VerifyBackup_CorruptedEntry` and
`TestAuthFactorService_VerifyFactor_ExpiredChallenge_v2`, both in `pkg/service`
— are **unconditional `t.Skip` calls with nothing to do with FIPS**. They did
not appear; they became *visible*, because `pkg/service`'s test binary now
compiles and its skips are printed rather than swallowed by a build failure.
They are tracked separately as ordinary skip guards, which this project does
not permit.

That is the general lesson and not a footnote: a package that fails to build
reports one `FAIL` line and hides everything else it would have said. A skip
count is not a failure count, it is not self-explanatory, and a change in it
after a build failure is fixed is a change in *visibility*. Check each
`--- SKIP` against the register before reading it either way.

**No test was skipped to close the Noise finding**, and the two packages it
affected acquired no skip guards. Where a live Noise listener became unreachable
under `fips140=only`, the test asserts the contract that replaces one — an
advertiser that is present, serves no Noise, and never touched the key file —
which is strictly stronger than a skip, because a regression that resumed
reading the key file in FIPS mode fails it. See
[../clustering/engineering.md](../clustering/engineering.md#test-strategy).

**Re-measured 2026-10-06 after the nightly regression fix, by hand (the job is still not
armed, so nothing but a human running these commands measures it between
nightlies).** Both modules pass clean under `GOFIPS140=latest
GODEBUG=fips140=only`: the root suite (163 packages) and `make test-sdk` (28
packages) both report zero failures and zero panics. This is a point
measurement of one commit, not a new scheduled-run count toward [Arming the
gate](#arming-the-gate) -- the stability record that section tracks is
unaffected, and the regression this fixes is exactly the
"next fixture that reintroduces a prohibited primitive" scenario the
paragraph below already warned about, just six weeks and five feature
trains later than expected.

### Reproducing this measurement

The figures above are worth only as much as the method behind them, and two
ways of getting them wrong have already been hit in this repository:

- **Do not compare a fresh number against a number recorded earlier.** Measure
  BOTH sides yourself, from the same base commit, on the same machine, in one
  sitting. The 106/16 → 120/2 movement above was established that way — a
  worktree at the pre-bump commit and the branch, one script — precisely so the
  delta could be attributed to the dependency change rather than to whatever
  else had landed in between.
- **Re-measure after the last commit that touches test code.** A measurement
  taken before a later commit describes a tree that is no longer being shipped.
  That happened here: tests added after the A/B introduced
  `cipher.NewGCM` fixtures, which broke `pkg/seal` under FIPS and silently
  invalidated the recorded counts until the hosted review caught it.

Check the log is complete before counting it — a suite killed midway produces a
short count that looks like an improvement.

All four rejection classes now read **zero** — X25519 because the Noise
handshake is no longer reached in FIPS mode, GCM because the last fixture was
rewritten onto `cipher.NewGCMWithRandomNonce`, `crypto/sha1` because of the
UUIDv8 migration, and short-key HMAC because of the shared-secret minimum.
**Zero is the value a broken grep also returns**, so re-derive each one rather
than accepting the table:

```
GOFIPS140=latest GODEBUG=fips140=only make test 2>&1 | grep -c 'crypto/cipher: use of GCM with arbitrary IVs'
GOFIPS140=latest GODEBUG=fips140=only make test 2>&1 | grep -c 'crypto/ecdh: use of X25519 is not allowed'
```

Before the go-quicraft v0.8.0-alpha bump this read **693 of 708** GCM
rejections, every one the body of a `quicraft/seal: cipher creation failed: ...`
error and none of them a qrdb call site. That whole class is gone; the sentence
is kept as history because the attribution is the evidence the remediation
worked, not because the number is current.

**Attribute every failing test before counting it.** When this page last
recorded two failures, only one of them was a FIPS failure at all: `pkg/service`
reported `[build failed]`, which prints as a single `FAIL` line among the `ok`
lines and looks exactly like a rejection until you read it, and it failed
identically with FIPS off. A count taken without that distinction would have
said "two FIPS failures" and been wrong by half. There are no failures to
attribute today; the habit is what carries forward.

### Arming the gate

**Every precondition this section used to name is met, and the job is
deliberately still not armed.** That is a decision, taken 2026-09-12, and it
replaces a blocked state with a waiting one. What it waits on is a **stability
record**, and the two reasons are worth more than the preconditions were.

**One green run is not a track record.** The retained history of this workflow
is 27 runs. Twenty-two of them are scheduled runs on `main` between 2026-08-22
and 2026-09-11 and **every one failed**. Run 17302 on 2026-09-12 is the first
scheduled run on `main` ever to pass, and at the time of writing it is the only
one. A check made required on the strength of its first pass is a check that
starts being bypassed the first time it fails for a reason nobody can
attribute, and a bypassed gate is worth less than no gate because it also
reports.

**It is structurally the wrong shape for a required check, and the cost is
measured.** Required checks gate pull requests; this is a *nightly* job. Arming
it as written either requires a check that does not run per pull request — a
gate that can never go green on a PR — or moves it onto the push path, where it
is paid on every push. Its five green runs took 17m54s, 22m52s, 23m47s, 24m04s
and 34m57s. A required check is paid at the worst of those, not the mean, so
the honest figure is **about 35 minutes added to every pull request**, on a
runner pool that already contends with local work. The original reason for
moving it off the push path was that it took roughly 50 minutes per pull
request *and then failed*. Exactly half of that reason has gone away.

**The number, and why it is that number: fourteen consecutive green SCHEDULED
runs on `main`.** Not dispatched runs on a branch — the record has to count the
thing being required. Three arguments fix it at fourteen rather than at five or
at thirty:

- **What the record must exclude is a flake, and this suite is where one would
  hide.** It runs the race detector under `fips140=only` over packages that
  take minutes (`pkg/vector/quantization` 168s, `pkg/vector/ivf` 66s,
  `sdk/go/node` 93s). A defect that fails one run in ten survives five
  consecutive runs unseen about 59% of the time and fourteen about 23% — still
  not proof, but the difference between "we have not looked" and "we looked".
- **Five green runs today are not five independent measurements.** They cover
  three distinct commits, and two of those three differ only in the change
  being demonstrated at the time. Fourteen scheduled runs span two weeks of a
  moving tree, which is what actually tests whether the job survives other
  people's merges rather than one tree's determinism.
- **Waiting is free.** The nightly runs whether or not anyone is waiting for
  it, so the record accumulates at no cost, while arming early costs ~35
  minutes on every pull request in the interim.

Arming remains **four moves and one act** when the record is in: move the job
from `.github/workflows/nightly.yml` into `.github/workflows/ci.yml`, add
`fips` to `ci-gate`'s `needs`, add `RESULT_FIPS` to its `env` block, and add a
`check fips` call to its loop. The old instruction to do that "in the change
that closes the last finding, not later" was right about the hazard and wrong
about the trigger: a FIPS job that can pass and is not required **is** a
fail-open gate, and that is the cost being carried deliberately for the length
of the record rather than a reason to arm today. Record the count when it is
reached, in this section, with the run that completed it.

## See Also

- [compliance.md](compliance.md) — FIPS 140-3 / FedRAMP / PCI-DSS / HIPAA requirement matrices
- [shamir-custody](../shamir-custody/) — share-holder envelopes and the HPKE suites
- [Go FIPS 140-3 documentation](https://go.dev/doc/security/fips140) — `GOFIPS140`, `GODEBUG=fips140`, and the module's approved-algorithm list
- [NIST SP 800-38D](https://csrc.nist.gov/publications/detail/sp/800-38d/final) — GCM, including the IV construction requirement
- [NIST SP 800-107 Rev. 1](https://csrc.nist.gov/publications/detail/sp/800-107/rev-1/final) — §5.3.4, HMAC key length
- [RFC 7292](https://www.rfc-editor.org/rfc/rfc7292) — PKCS#12, including the `localKeyId` attribute
