<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# FIPS 140-3 Mode (`GODEBUG=fips140=only`)

This document records how go-qrdb is verified against the Go Cryptographic
Module, what had to change to make the codebase approved-algorithm clean, and
the exemption register for anything that is deliberately not clean.

## The gate

CI runs a `FIPS 140-3` job that executes the full unit suite under:

```
GOFIPS140=latest GODEBUG=fips140=only go build ./...
GOFIPS140=latest GODEBUG=fips140=only make test
```

`GOFIPS140` selects the module at build time. `GODEBUG` selects the enforcement
level at run time, and the level is `only`, not `on`:

| Setting | Module loaded | Non-approved algorithms |
|---|---|---|
| `fips140=on` | yes | **permitted** — MD5, SHA-1, X25519 all succeed |
| `fips140=only` | yes | rejected: the call returns an error or panics |

`on` cannot fail on the thing the gate exists to catch: a regulated path can use
prohibited cryptography and the job still reports green. `only` is the setting a
FedRAMP / PCI DSS v4 / HIPAA gate has to run. Ratified by the owner 2026-08-17
(`automatethethings` `software/conventions/configuration.md` §5).

The job scope is deliberately identical to the ordinary `Test` job — the same
`make test` target with the same build tags. A FIPS gate over a subset of the
suite does not tell you the product is FIPS-clean.

To reproduce a CI failure locally, prefix the same command:

```
GOFIPS140=latest GODEBUG=fips140=only make test
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

Every non-approved primitive this repository owns has been replaced. The
AES-GCM change is byte-compatible with existing ciphertext and existing wire
formats; the rest are not free, and each states its cost below — identifiers
that change value, a configuration minimum, one enrollment path refused in FIPS
mode, and one export format refused under strict enforcement.

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

### SHA-1 — UUID version 5 → RFC 9562 version 8 over SHA-256

`crypto/sha1` **panics** under `only`. Two deterministic identifiers were
derived with `uuid.NewSHA1`, which is UUID version 5 and is defined to hash
with SHA-1:

| Call site | Identifier |
|---|---|
| `pkg/role/store.go` (`deterministicUUID`, via `SystemRoleID`) | built-in role IDs |
| `pkg/custodian/service.go` | deterministic custodian IDs |

Both now use `pkg/uuidv8`, which derives an RFC 9562 version 8 UUID from
SHA-256. SHA-1 here protects nothing — it is a naming construct — but a
name-based identifier is not exempt from the gate, and the alternative to
changing it is a process that dies on its first role lookup.

**These identifiers change value, and that is an upgrade concern.** They are
derived from `(tenantID, name)` on every call rather than allocated, so a
cluster whose nodes all run this build is self-consistent and a fresh
deployment is unaffected.

An **existing** store is not. `bootstrap.seedTenantRoles` treats
`RoleNameExistsError` as idempotent success, so on upgrade the persisted role
records keep their version 5 IDs while `role.SystemRoleID` now returns version
8 values. The two consumers that match on the derived ID —
`pkg/auth/rbac` (the Owner/SO deny-list) and `bootstrap`'s Admin@system grant —
would then reference IDs no record carries. Re-seeding is not automatic and no
migration ships with this change; an operator upgrading a store that already
holds system roles needs one, and whether that is a rewrite of the role records
or a re-bootstrap is an owner decision. The alternative — keeping SHA-1 — is
not available: `crypto/sha1` panics under `only`, so the process dies on its
first role lookup.

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

## Exemption register

One exemption. Every other non-approved primitive in this repository has been
remediated rather than exempted.

A row here requires: the citation of the external format or protocol that
mandates the primitive, evidence that it protects nothing regulated, and the
narrowest isolation that keeps it out of the FIPS-gated path.

| Primitive | Call site | Mandating citation | Isolation |
|---|---|---|---|
| SHA-1, as the `localKeyId` certificate fingerprint | `pkg/cert/pkcs12.go` → `software.sslmate.com/src/go-pkcs12` | [RFC 7292](https://www.rfc-editor.org/rfc/rfc7292) §4.2 / PKCS#12 v1.1 attribute conventions; the SHA-1 fingerprint as `localKeyId` is what OpenSSL, Java keytool and Windows CryptoAPI use to bind a key to its certificate | `encodePKCS12` returns `*PKCS12FIPSUnavailableError` when `fips140.Enforced()`, so the call never executes in the FIPS-gated path. The archive's own protection (PBKDF2-HMAC-SHA256 + AES-256-CBC) is approved and unchanged. |

## Open blockers

The FIPS job is **not yet wired into `ci-gate`**. Two first-party dependencies
still use non-approved cryptography, which qrdb cannot fix from this repository
(the composition direction is one-way):

| Dependency | Call sites | Effect on qrdb | Tracking |
|---|---|---|---|
| `go-quicraft` `pkg/seal` | `algorithm.go`, `strategy_software.go` build the barrier AEAD with `cipher.NewGCM` | every sealed-storage test fails with `quicraft/seal: cipher creation failed`, covering `pkg/atrest`, `pkg/seal`, `pkg/seal/encfs`, `pkg/ceremony`, `pkg/service`, `pkg/store` and more | `jeremyhahn/go-quicraft#5` |
| `go-truststrap` `pkg/noiseproto` | Noise_NK handshake over `noise.DH25519` | `pkg/clusterenroll` fails with `crypto/ecdh: use of X25519 is not allowed` | `jeremyhahn/go-truststrap#5` |

Arming the gate once both ship approved-algorithm releases and this module
bumps to them is a three-line change in `.github/workflows/ci.yml`: add `fips`
to `ci-gate`'s `needs`, add `RESULT_FIPS` to its `env` block, and add a
`check fips` call to its loop. The job runs on every PR in the meantime, so its
status is visible rather than hypothetical.

## See Also

- [compliance.md](compliance.md) — FIPS 140-3 / FedRAMP / PCI-DSS / HIPAA requirement matrices
- [custodians.md](custodians.md) — custodian share-delivery protocol and envelope format
- [Go FIPS 140-3 documentation](https://go.dev/doc/security/fips140) — `GOFIPS140`, `GODEBUG=fips140`, and the module's approved-algorithm list
- [NIST SP 800-38D](https://csrc.nist.gov/publications/detail/sp/800-38d/final) — GCM, including the IV construction requirement
- [NIST SP 800-107 Rev. 1](https://csrc.nist.gov/publications/detail/sp/800-107/rev-1/final) — §5.3.4, HMAC key length
- [RFC 9562](https://www.rfc-editor.org/rfc/rfc9562) — §5.8, UUID version 8
- [RFC 7292](https://www.rfc-editor.org/rfc/rfc7292) — PKCS#12, including the `localKeyId` attribute
