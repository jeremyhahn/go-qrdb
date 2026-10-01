# Credentials and Secret Resolution — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Named credentials with versioned rotation, plus the SPI that resolves
`credential://` and `literal://` references in configuration at boot.

## Problem

The engine itself needs a handful of secrets to start — and some of them are
not resident anywhere at boot, because an officer has to type them. Storing
those in the user KV store would put them next to application data; leaving them
in config files would put them on disk in plaintext.

## Shape chosen

One interface, one service, **two strategies**:


`pkg/credentials/config.go:11-24`

| Strategy | Description |
|----------|-------------|
| `manual` | Credentials held in-memory inside **go-enclave sealed enclaves** (`secure.Secret`) — never as raw, garbage-collected heap bytes. Retrieval blocks until an operator submits the credential (used during ceremony for non-resident secrets). Plaintext is materialized only transiently (`Open → copy → Destroy`) on read; on delete/rotation the backing enclave is destroyed. |
| `barrier` | Credentials encrypted and stored via `seal.StorageBarrier` (encrypt-then-store on the at-rest storage backend) — the production default. |

Future HSM-only strategies will surface `ErrEnumerationNotSupported` from
`ListCredentials` rather than break the interface.

**PIN management is deliberately separate** — PINs live in `pkg/pin` because
they need backend-native lockout semantics that credentials do not. See
[../auth-factors/](../auth-factors/).

## The boundary that matters

Future HSM-only strategies will surface `ErrEnumerationNotSupported` from `ListCredentials` rather than break the interface.

## Boundary: qrdb credentials (L2) vs quicbaas secrets/KMS (L4)

These are deliberately **not** the same thing and do **not** overlap:

| | `pkg/credentials` (qrdb, L2) | quicbaas KMS / `pkg/secrets` (L4) |
|---|---|---|
| **Purpose** | Minimal, self-contained custody of the handful of **operational/boot secrets** the engine itself needs — chiefly the **ceremony `manual` strategy** (security officers submit non-resident secrets that block retrieval until provided). | Full **product secrets-management & KMS** for application/tenant secrets: rich key lifecycle, per-key authz, key-use audit, operator-settable rotation policy. |
| **Layer** | Runs in **standalone qrdb** with no quicbaas present (xkms/quicbaas embed qrdb). | Built **on top of** qrdb's engine; absent in standalone qrdb. |
| **Backing** | `manual` = in-memory go-enclave sealed enclave; `barrier` = qrdb seal `StorageBarrier` (encrypted-at-rest). | qrdb engine (KV/barrier) via the SDK, plus xkms hardware seal injection. |
| **Scope** | A small set of named engine secrets + versioned rotation. | Arbitrary tenant/app secret trees, large scale. |

### Alternatives rejected

- **A general secrets/KMS product here.** Rejected, and the reasoning is the
  table above: qrdb ships the engine's own minimal custody. quicbaas's KMS
  composes *above* it with per-key authorization, key-use audit and
  operator-settable rotation policy. Two things, two layers.
- **Storing credentials in the user KV store.** Rejected: **credentials never
  live in the user KV store.** They are distinct from the KEK/DEK key custody
  (the seal barrier) and from the Raft data plane.
- **A `secrets`/`kms` package in qrdb.** The old `secrets-envelope` was removed
  in the single-KEK/DEK refactor and was not replaced. **`pkg/credentials` is
  the only secret-bearing store in qrdb.**
- **Resolving secret references into plain strings.** Rejected — see the
  protected-field rule below.

## Secret resolution

`pkg/secret` resolves `credential://` and `literal://` references in
configuration at boot, into **enclave-backed `SecretValue` targets rather than
plain strings**. A protected config field must reference a secret, and
resolution **fails closed** when it cannot.

The design record is
[credentials/secret-resolution.md](../credentials/secret-resolution.md), which
also covers scheme ownership across the stack and how downstream layers consume
the SPI.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| Credential store, strategies, versioned rotation | `pkg/credentials` | go-qrdb |
| `CredentialServicer` — the transport-facing entry point | `pkg/service` | go-qrdb |
| Secret-reference resolver SPI, `SecretValue`, `ConfigProcessor` | `pkg/secret` | go-qrdb |
| Protected memory primitives | `pkg/secure` over **go-enclave** | go-qrdb |
| At-rest encryption for the `barrier` strategy | `pkg/seal` — see [../barrier-seal/](../barrier-seal/) | go-qrdb |
| PIN custody | `pkg/pin` — see [../auth-factors/](../auth-factors/) | go-qrdb |
| Product secrets management and KMS | — | **go-quicbaas**, above this layer |

## Trust boundaries

- **`manual` credentials live only in go-enclave sealed enclaves** — never as raw,
  garbage-collected heap bytes. Plaintext is materialized transiently
  (`Open → copy → Destroy`) on read; the enclave is destroyed on delete or
  rotation.
- **`barrier` credentials are encrypt-then-store** through the
  `seal.StorageBarrier`, so a sealed barrier makes them unreadable.
- **Retrieval under `manual` blocks until an operator submits the value.** That
  is the ceremony case: a non-resident secret exists nowhere until someone
  provides it.
- Each verb carries its own permission; **`credentials:submit`,
  `credentials:rotate` and `credentials:delete` are sensitive and distinct from
  reads**.

## What this feature deliberately does not do

- **No product secrets management.** That is quicbaas's KMS, one layer up.
- **No PIN storage.** `pkg/pin` owns PINs and their lockout semantics.
- **No KEK/DEK custody.** That is the barrier.
- **No automatic rotation policy.** Rotation is an explicit call with an
  explicit grace period.
