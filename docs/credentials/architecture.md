# Credentials and Secret Resolution — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-07
> Conflicts with this document are defects. Fix them here first.

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

## Protected-memory boundary

A retrieved credential is returned to the caller as a go-enclave-backed
`*secure.Buffer`, not a `[]byte` (handbook `security-review.md` §6). The
protected-memory handle runs from the store to the last point at which a
caller must hold bytes:

| Hop | Surface | Form of the value |
|---|---|---|
| Store | `credentials.CredentialService.RetrieveCredentialBuffer`, `RetrieveCredentialVersionBuffer` | `*secure.Buffer`. `manual` opens the enclave straight into a fresh Buffer; `barrier` moves the barrier's decrypted heap slice into one, wiping the slice (the decrypt itself happens in the barrier, below this feature) |
| Service | `service.CredentialServicer.CredentialRetrieveBuffer`, `CredentialRetrieveVersionBuffer` | `*secure.Buffer`, passed through |
| In-process consumers | embedded SDK transport, `pkg/secret` `CredentialResolver` | `*secure.Buffer`. The resolver seals it into a `SecretValue` with `secret.NewSecretValueFromBuffer`, which consumes the Buffer without a heap copy |
| **Wire** | REST, gRPC, QUIC, MCP, Unix handlers | **bytes, by design** (below) |
| SDK client | `transport.CredentialClient.CredentialRetrieveBuffer`, `CredentialRetrieveVersionBuffer` | `*secure.Buffer`, rebuilt from the decoded wire value |
| **Output** | `qrdb credential retrieve`, `retrieve-version`; the desktop binding | **bytes, by design** (below) |

**The secret-bearing wire DTO is the single boundary where bytes leave
protected memory on the server.** A response body, a protobuf message and an
MCP tool result are all byte or string containers owned by an encoder, so the
value has to be copied out of the Buffer to be sent. That copy is
`service.CredentialWireValue`, the only place a handler does it: it copies the
value out under a held borrow (`secure.CopyOut`) and destroys the Buffer in the
same call, so the protected copy does not outlive the hand-off. REST, QUIC and
MCP wipe the copied slice as soon as it has been encoded. gRPC cannot: it
marshals the response after the handler returns, so the slice is released to
the garbage collector and not wiped. This posture is accepted rather than
engineered around — every transport is TLS-protected and
the credential is by definition about to cross a process boundary. The same
holds in the other direction, and each SDK remote transport builds the Buffer
itself from its own response. REST and MCP decode the base64 text straight into the Buffer's window
(`transport.WireValue`, `transport.BufferFromBase64`), so the plaintext never
exists as a heap slice there. gRPC, Unix and QUIC decode a protobuf `bytes`
field, which the protobuf runtime allocates; the transport moves it into the
Buffer with `transport.BufferFromWire` and wipes it, and QUIC also wipes the
whole response frame. In every case the framework's own read buffers (the HTTP
body, the JSON decoder's scratch, the MCP tool-result string, gRPC's receive
buffer) are outside this package's control and are not wiped.

**The CLI and the desktop binding are the output boundary.** The CLI writes the
value to the terminal or pipe inside the Buffer's borrow (`printSecretBuffer`)
and destroys the Buffer; the desktop binding converts it to a string for the
frontend, which cannot hold a Buffer. Nothing downstream of either is protected
memory, and that is the point at which the operator asked to see the value.

The two copies that are never wiped, the gRPC handlers' response field and the
desktop binding's string, each carry a `//secure:unwiped` directive naming the
framework that encodes the value after the function returns, so
`make secure-analyzer` holds every other copy-out to a wipe.

OWNER ruling, 2026-10-07: the `[]byte` forms of these methods
(`RetrieveCredential`, `RetrieveCredentialVersion`, `CredentialRetrieve`,
`CredentialRetrieveVersion`) are deleted now from exported Go interfaces and
implementations, with no compatibility code. Only the `*secure.Buffer` forms
remain for Go callers. The wire contract is unchanged: REST, gRPC, QUIC, MCP
and Unix still carry credential retrieve responses as secret-bearing wire DTOs
at the process boundary.

## What this feature deliberately does not do

- **No product secrets management.** That is quicbaas's KMS, one layer up.
- **No PIN storage.** `pkg/pin` owns PINs and their lockout semantics.
- **No KEK/DEK custody.** That is the barrier.
- **No automatic rotation policy.** Rotation is an explicit call with an
  explicit grace period.
