<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# FIPS 140-3 Mode (`GODEBUG=fips140=only`)

This document records how go-qrdb is verified against the Go Cryptographic
Module, what had to change on the way to approved-algorithm clean, the
exemption register for anything that is deliberately not clean, and — in
[Open blockers](#open-blockers) — exactly how far from clean the module still
is. **go-qrdb does not yet operate under `fips140=only`**; do not read this
document as a compliance claim.

## The job

CI runs a nightly `FIPS 140-3` job that executes the full unit suite of **both
modules** under:

```
GOFIPS140=latest GODEBUG=fips140=only go build ./...
GOFIPS140=latest GODEBUG=fips140=only make test
GOFIPS140=latest GODEBUG=fips140=only make test-sdk
```

`GOFIPS140` selects the module at build time. `GODEBUG` selects the enforcement
level at run time, and the level is `only`, not `on`:

| Setting | Module loaded | Non-approved algorithms |
|---|---|---|
| `fips140=on` | yes | **permitted** — MD5, SHA-1, X25519 all succeed |
| `fips140=only` | yes | rejected: the call returns an error or panics |

`on` cannot fail on the thing the job exists to catch: a regulated path can use
prohibited cryptography and the job still reports green. `only` is the setting a
FedRAMP / PCI DSS v4 / HIPAA gate has to run. Ratified by the owner 2026-08-17
(`automatethethings` `software/conventions/configuration.md` §5).

The job scope is deliberately identical to the ordinary gate — the same
`make test` **and `make test-sdk`** targets with the same build tags. A FIPS job
over a subset of the suite does not tell you the product is FIPS-clean.

**`make test-sdk` is not optional decoration.** `sdk/go` is a separate Go
module, so `make test` (`./pkg/... ./cmd/...`) does not reach a single line of
it. The job ran `make test` alone until 2026-09-02, which means the entire SDK —
every transport, `sdk/go/node`, the driver, the DAO layer and the conformance
matrix — **had never executed under `fips140=only` at all**. Nothing in this
document has ever asserted a FIPS property about the SDK, and until this job has
had a clean run over both modules, nothing should.

The sdk step runs even when the root suite fails, deliberately. This job is
known-red on the root module (see [Open blockers](#open-blockers)), so a step
that only ran after a green `make test` would never run at all, and the SDK
would stay unmeasured while the workflow appeared to cover it. Either module
failing fails the job.

The job is **not** in `ci-gate`'s `needs` and is therefore not a required
check — see [Open blockers](#open-blockers) for the two findings that keep it
red and [Arming the gate](#arming-the-gate) for the exact change that makes it
required.

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

**Every remaining FIPS failure in this repository is upstream.** As of the
measurement below there is not one non-approved primitive left on a go-qrdb
call site — all 15 remaining rejections come from `go-truststrap`'s Noise
handshake. The `go-quicraft` barrier AEAD accounted for 693 of them until the
v0.8.0-alpha bump closed that class; see [Open blockers](#open-blockers).

### AES-GCM — `cipher.NewGCM` → `cipher.NewGCMWithRandomNonce`

SP 800-38D requires the module to own IV generation, so `cipher.NewGCM`
(arbitrary caller-supplied IVs) is rejected under `only`. Every construction
moved to `cipher.NewGCMWithRandomNonce`, which samples a 12-byte nonce itself
and prepends it to the ciphertext.

| Call site | Role |
|---|---|
| `pkg/crypto/aesgcm/aesgcm.go` | shared AES-256-GCM helper |
| `pkg/dek/crypto.go` | DEK wrap / unwrap |
| `pkg/seal/encfs/block.go` | encrypted-filesystem block cipher |
| `pkg/custodian/share_envelope.go` | Shamir share envelope |
| `pkg/ceremony/external_officer.go` | external-officer share envelope |

**No migration is required.** All five sites already framed their output as
`nonce (12) || ciphertext || tag (16)` with a nonce drawn from `crypto/rand`,
which is byte-for-byte what `NewGCMWithRandomNonce` produces. Ciphertext
written by an older build opens under the new code and vice versa. The two
envelope formats that carry the nonce in a separate field
(`custodian.SealedEnvelope.Nonce`, and the ceremony envelope's packed layout)
split the module-generated prefix back off, so those wire formats are unchanged
as well — which matters, because the custodian-side inverse is third-party
client software (see [custodians.md](custodians.md)).

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

### X25519 — refused for Ed25519 external officers in FIPS mode

Ed25519 is a signature algorithm, so sealing a Shamir share to an Ed25519
officer key requires the birational map to X25519 — and X25519 is not on the
approved list, so `crypto/ecdh` rejects it under `only`. There is no approved
substitute for an Ed25519 recipient.

`pkg/ceremony` now refuses that enrollment explicitly when
`crypto/fips140.Enabled()` reports FIPS mode, returning
`ErrAlgorithmNotFIPSApproved` instead of letting a low-level ECDH failure
surface as a generic seal error. Sealing a share of the barrier root key is a
regulated path, so the enrollment is refused rather than completed with
cryptography the deployment may not use. RSA and NIST-curve (P-256, P-384)
officer keys are unaffected; an officer whose key is Ed25519 must enrol one of
those instead.

The guard keys off `Enabled()` rather than `Enforced()` deliberately: under
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

`pkg/role.deterministicUUID` and the matching derivation in `pkg/custodian`
built their stable identifiers with `uuid.NewSHA1`, an RFC 4122 v5 name-based
UUID. `crypto/sha1` **panics** under `only`, and both derivations run during
package initialization and tenant seeding, so this was the widest of the
findings: it took out `pkg/role`, `pkg/auth/rbac`, `pkg/bootstrap`,
`pkg/firstboot`, `pkg/custodian` and every package that seeds a tenant — and,
because the panic happens before a test body runs, it reached any downstream
consumer that authorized through `pkg/role` at all.

Both derivations are now RFC 9562 §5.8 **UUIDv8 over SHA-256**. UUIDv8 is the
version reserved for application-defined derivations, which is the only place a
SHA-256 name-based UUID can live: RFC 4122 fixes v3 to MD5 and v5 to SHA-1. The
inputs are length-prefixed before hashing so the encoding is injective —
without framing, tenant `a/b` with role `c` and tenant `a` with role `b/c`
would collide on one identifier.

**These identifiers are persisted data** — role primary keys, both secondary
indexes, and the contents of every user record's `RoleIDs` — so the change is
versioned, not a flag day. Writes emit only the v8 form; reads accept both.
Re-deriving from a tenant and a role NAME is the direction that needs care,
because on an already-seeded cluster the v8 identifier names no record:
`role.ResolveSystemRoleID` answers from the store's name index and falls back
to the canonical value, and `role.SystemRoleIDs` returns the whole set so that
the SO deny-list and the separation-of-duties Owner strip match a legacy grant
instead of letting it escape a control that exists to void it.

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

**Read this before describing go-qrdb as FIPS 140-3 clean: it is not, yet.**
The `FIPS 140-3` job fails today, it is **not** a required check, and it must
not be presented as an enforceable compliance gate until it passes. Its purpose
until then is to measure the remaining distance under the enforcement level the
policy requires, nightly, where the number cannot be quietly lost. (It ran on
every pull request until the known-red and long-horizon jobs moved off the push
path; a known-red job burning a runner slot per pull request produced a number
we already had.) That is a strict improvement over having no FIPS coverage at
all; it is not a claim of compliance.

**One finding** now stands between the job and green, and it is **in a
dependency**. It cannot be closed from this repository: the composition
direction is one-way, and go-qrdb consumes the module as a tagged release.

| Finding | Call sites | Effect on the suite | Tracking |
|---|---|---|---|
| `go-truststrap` `pkg/noiseproto` performs the Noise_NK handshake over `noise.DH25519` | `session.go`, `keys.go` | `pkg/clusterenroll` and `cmd/qrdbd` fail with `crypto/ecdh: use of X25519 is not allowed` | open upstream in `go-truststrap`, against `pkg/noiseproto` — two findings, both unfixed as of the v0.8.0-alpha bump |

**The `go-quicraft` AEAD finding is CLOSED.** It read, until the go-quicraft
v0.8.0-alpha bump landed here:

> `go-quicraft` `pkg/seal` builds the barrier AEAD with `cipher.NewGCM` —
> every sealed-storage test fails, covering `pkg/atrest`, `pkg/seal`,
> `pkg/seal/encfs`, `pkg/ceremony`, `pkg/service`, `pkg/store` and more.

and it predicted that the bump "turns 14 of the 16 failing packages green on
its own", leaving `pkg/clusterenroll` and `cmd/qrdbd` red. **Measured on both
sides of the bump from the same base commit, that is exactly what happened**:
14 packages moved red to green, no regressions, and the two survivors are the
predicted pair. The GCM-with-arbitrary-IV rejection count went from 699 to
**zero**; the X25519 count did not move, because it is the other finding.

Taking that release was not a free bump — go-quicraft v0.7.0-alpha changed the
exported AEAD's calling convention, and `pkg/seal/unsealkey.go` had to absorb it
without moving the wrapped-root-key wire format. See the CHANGELOG entry for
v0.9.0-alpha.

**go-truststrap has not fixed its finding, and it is not a bug fix.**
`pkg/noiseproto/keys.go` on `main` still calls `curve25519.X25519`. Noise_NK has
no FIPS-approved DH to move to, so there is nothing to substitute; the question
is what a FIPS deployment does about cluster enrollment, and it is a product
decision rather than an engineering one. The two candidate answers on the table
are compiling `noiseproto` out under the `fips` tag — the precedent go-quicraft
set for ChaCha20 — or having `pkg/clusterenroll` refuse enrollment when
`fips140.Enabled()`, exactly as `pkg/ceremony` already refuses an Ed25519
external officer. **Neither is go-qrdb's call to make.**

Consumers whose own FIPS jobs pass `-tags fips` would compile go-truststrap's
`noiseproto` fips variant and close this; go-qrdb's job does not pass that tag,
which is why the finding is still counted here.

**go-truststrap has not, and it is not a bug fix.** `pkg/noiseproto/keys.go` on
`main` still calls `curve25519.X25519`. Noise_NK has no FIPS-approved DH to move
to, so there is nothing to substitute; the question is what a FIPS deployment
does about cluster enrollment, and it is a product decision rather than an
engineering one. The two candidate answers on the table are compiling
`noiseproto` out under the `fips` tag — the precedent go-quicraft just set for
ChaCha20 — or having `pkg/clusterenroll` refuse enrollment when
`fips140.Enabled()`, exactly as `pkg/ceremony` already refuses an Ed25519
external officer. **Neither is go-qrdb's call to make.**

### Measured status

Go 1.26.6, linux/amd64, `GOFIPS140=latest GODEBUG=fips140=only make test`:
**120 packages pass, 2 fail**, and both attribute to the one finding above.
`go build ./...` against the module is clean.

**These figures are the ROOT MODULE ONLY.** `make test` is
`./pkg/... ./cmd/...`; the `sdk/go` module has no measurement here because
until 2026-09-02 nothing had ever run it under `fips140=only` — the job did not
invoke `make test-sdk`. It does now, and the first nightly run after that
change is what produces the SDK's numbers. Until they appear in this table, the
count above is a statement about `pkg/` and `cmd/` and about nothing else, and
the SDK's status is **unknown rather than clean**. When the numbers arrive,
record them; if the SDK turns out not to be FIPS-clean, that is a finding to add
to [Open blockers](#open-blockers) — not a reason to narrow the job's scope,
which is forbidden here for the reason stated at the top of this section.

| Blocker | Failing packages |
|---|---|
| Noise X25519 | `pkg/clusterenroll`, `cmd/qrdbd` |

Rejection counts on the same run: **0** `use of GCM with arbitrary IVs`, **15**
`use of X25519 is not allowed`.

Previously **106 pass / 16 fail** with 699 GCM rejections. The go-quicraft
v0.8.0-alpha bump closed the GCM finding outright; see [Open
blockers](#open-blockers) for the before/after and how it was attributed.

Five tests report `--- SKIP` on this run. Three predate this section; **two are
the caller-nonce tests** registered under [Skipped tests under
`fips140=only`](#skipped-tests-under-fips140only), and they are unreachable
code rather than hidden red. A skip count is not a failure count, and neither
is it self-explanatory — check a `--- SKIP` against that register before
reading it either way.

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

**Zero** occurrences of `crypto/cipher: use of GCM with arbitrary IVs is not
allowed in FIPS 140-only mode` remain. All **15** rejections in the current run
are `crypto/ecdh: use of X25519 is not allowed`, from the Noise handshake. There
are zero `crypto/sha1` panics, which is what the UUIDv8 migration was for, and
zero `crypto/hmac: use of keys shorter than 112 bits`, which is what the
shared-secret minimum was for. Re-derive the attribution with:

```
GOFIPS140=latest GODEBUG=fips140=only make test 2>&1 | grep -c 'crypto/cipher: use of GCM with arbitrary IVs'
GOFIPS140=latest GODEBUG=fips140=only make test 2>&1 | grep -c 'crypto/ecdh: use of X25519 is not allowed'
```

Before the go-quicraft v0.8.0-alpha bump this read **693 of 708** GCM
rejections, every one the body of a `quicraft/seal: cipher creation failed: ...`
error and none of them a qrdb call site. That whole class is gone; the sentence
is kept as history because the attribution is the evidence the remediation
worked, not because the number is current.

Every failing test in both remaining packages carries the X25519 rejection in
its own output. There is no residue: no failing test in this suite is explained
by anything other than a dependency's non-approved primitive.

### Arming the gate

Once the remaining finding is closed, this module bumps to approved-algorithm
dependency releases, **and the SDK has had a clean run** (see [Measured
status](#measured-status) — its status is currently unknown, not clean), `fips`
becomes a required check. The job lives in `.github/workflows/nightly.yml`, so
arming it is four moves and they are one act: move the job into
`.github/workflows/ci.yml`, add `fips` to `ci-gate`'s `needs`, add
`RESULT_FIPS` to its `env` block, and add a `check fips` call to its loop. Do
that in the change that closes the last finding, not later — a FIPS job that can
pass and is not required is the fail-open gate this document exists to argue
against.

## See Also

- [compliance.md](compliance.md) — FIPS 140-3 / FedRAMP / PCI-DSS / HIPAA requirement matrices
- [custodians.md](custodians.md) — custodian share-delivery protocol and envelope format
- [Go FIPS 140-3 documentation](https://go.dev/doc/security/fips140) — `GOFIPS140`, `GODEBUG=fips140`, and the module's approved-algorithm list
- [NIST SP 800-38D](https://csrc.nist.gov/publications/detail/sp/800-38d/final) — GCM, including the IV construction requirement
- [NIST SP 800-107 Rev. 1](https://csrc.nist.gov/publications/detail/sp/800-107/rev-1/final) — §5.3.4, HMAC key length
- [RFC 7292](https://www.rfc-editor.org/rfc/rfc7292) — PKCS#12, including the `localKeyId` attribute
