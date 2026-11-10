# Credentials and Secret Resolution — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-11-01
> Conflicts with this document are defects. Fix them here first.

Named credentials with versioned rotation, plus the SPI that resolves
`credential://` and `literal://` references in configuration at boot.

## Problem

The engine itself needs a handful of secrets to start — and some of them are
not resident anywhere at boot, because an officer has to type them. Storing
those in the user KV store would put them next to application data; leaving them
in config files would put them on disk in plaintext.

## Shape chosen

One interface, one service, **two strategies**
(`pkg/credentials/config.go:11-24`):

| Strategy | Description |
|----------|-------------|
| `manual` | Credentials held in-memory inside **go-enclave sealed enclaves** (`secure.Secret`) — never as raw, garbage-collected heap bytes. Retrieval blocks until an operator submits the credential (used during ceremony for non-resident secrets). A read opens the enclave straight into a fresh `*secure.Buffer`; a delete, and a rotation with no grace period, destroy the backing enclave. |
| `barrier` | Credentials stored as system records on the system shard's replicated KV, encrypted at rest by every node's system barrier — the default. |

A strategy that cannot enumerate its credentials returns
`ErrEnumerationNotSupported` from `ListCredentials` rather than break the
interface.

## Storage: one replicated store on the system shard

**A cluster has one credential store, not one per node.** The `barrier`
strategy keeps every record (a credential's current value, its version
history and its metadata record, which carries the rotation policy and the
rotation claim) under reserved prefixes of the system shard's replicated KV,
one per kind of record:

| Record | Key |
|--------|-----|
| current value | `__sys:credentials/<name>` |
| metadata | `__sys:credential-meta/<name>` |
| retained version `<n>` | `__sys:credential-versions/<e>/<n>` |
| staged rotation value for version `<n>` | `__sys:credential-staged/<e>/<n>` |

`<e>` is the name in unpadded URL-safe base64 and `<n>` the decimal version.
A name may be any non-empty string, so no record is told apart by a character
in its key: the kind is the prefix, the value and metadata keys end in the
whole name, and the encoded name holds no `/`, so the one `/` after it
separates it from the version. A key names exactly one record of one
credential, and no credential name can produce another record's key. This follows the layered-stack rule: one cluster, one
datastore, and system-level records replicated on the system shard. A policy,
a claim or a version written on one node is the same record on every node,
once Raft has committed it.

**The key that protects it is the system barrier's, on every node.** The
system shard's storage (its Raft log, state machine and KV) is encrypted at
rest under each node's system barrier ([../barrier-seal/](../barrier-seal/),
"Transparent at-rest encryption"), and Raft carries it between nodes over the
mutually authenticated QUIC transport. So every unsealed node reads the same
records, and no node can read them while it is sealed. No second key is
layered over the records, and none is carried between nodes. This is the
protection the system shard's other secret-bearing records already have, such
as the DEK keyring of a prefix tenant ([../tenant-dek/](../tenant-dek/)).

**The prefixes are reached only through the credential verbs.** Every record
under the four prefixes above is a secret or governs one, so the embedded
transport's KV, MVCC, batch, transaction and watch services confine each
prefix whole, the way they confine a CA's private key material: a point read, write,
compare or watch on any record under it is refused with
`HardConfinedKeyError`, and a broader scan or watch has the records filtered
out. Reading a value takes `credentials:retrieve` and is audited; the raw KV
path would bypass both. The credential service's own store reaches the prefix
through the node's KV service, not through those decorators.

**Conditional writes are Raft transactions.** The store's `PutIfAbsent` and
`CompareAndSwap` are single transactions on the system shard (a version-zero
or value compare guarding one put), evaluated inside the state machine. Two
nodes racing for the same record cannot both win, on any replica.

**The store exists once the system shard does.** The credential service is
built over the replicated KV after the node's KV host starts. A node that is
still sealed, or whose KV host has not started, serves no credential call, and
a node seal pauses the system shard, so every credential call on that node
fails closed until it unseals.

Rejected: **a store per node**, over the node-local seal store. A policy set on
one node would be invisible on the others, and a claim taken on one node would
not exclude another, so a cluster would rotate a credential once per node and
the version histories would diverge. The node-local seal store holds per-node
seal keys and nothing else.

**PIN management is deliberately separate** — PINs live in `pkg/pin` because
they need backend-native lockout semantics that credentials do not. See
[../auth-factors/](../auth-factors/).

## Prefix migration

Before the store was replicated, the `barrier` strategy kept each node's
records under the `credentials/` prefix of that node's own seal store, so a
cluster could hold one copy of a credential per node. Each node moves its own
copies into `__sys:credentials/` once, under these rules.

**What moves.** Every record under the old prefix: a credential's current
value, its retained versions and its metadata record (rotation policy and
claim). The key layout under the prefix is unchanged; only the prefix moves.
A `manual`-strategy node keeps no records and has nothing to move.

**When it runs.** Once per node, when the credential service is bound to the
replicated store after the system shard starts. A migration that cannot
finish then, such as one whose node-local store is still sealed or one bound
before the system shard has a leader, leaves the service bound to the
replicated store and is logged. It runs again before the next credential
operation on that node (store, retrieve, list, rotate, delete, the rotation
policy operations and the scheduled rotation pass) and on the next conflict
list or resolve call. Until it finishes, those operations return the
migration's error and serve, list or rotate nothing: the node does not answer
from a store its own records have not reached.

**Who wins.** Per credential name, the first node to migrate wins: it writes a
migration claim beside the replicated record (the digests of its value,
versions and policy, never the value) with a conditional write, then writes
its records, then deletes its old ones. A replicated record that already
existed before any node migrated the name counts as first. A later node whose
copy is byte-identical in value, versions and policy deletes its old records.
A later node whose copy differs in any of the three keeps its old records
untouched and reports a **migration conflict**: a warning in the log, a
`migrate` audit event with outcome failure naming the differing fields, and an
entry in the conflict list.

**Idempotent after a crash.** A node that crashes part-way and runs again
reaches the same result. The claim is written before the records and marked
complete after them, so a rerun finishes the writes of an incomplete claim
that is its own and keeps any record already present. Before deleting its old
records a node writes a settled note under the old prefix and deletes the
note last, so a rerun interrupted mid-deletion finishes the deletion instead
of reading the remainder as a different copy.

**Resolving a conflict.** An operator lists a node's conflicts (permission
`credentials:list`) and settles each one (permission `credentials:rotate`)
with one of two resolutions:

- `import` writes the node's kept current value as a new version of the
  replicated record, retaining the version it replaces, exactly as a rotation
  does;
- `discard` keeps the replicated record as it is.

Either way the node's old records for that name are then deleted, and the
resolution is audited with the old records it did not carry over (the kept
version history, and the current value unless it was imported). Resolving a
name that has no conflict on the node is `NotFound`; a resolution other than
the two is `InvalidArgument`. Conflicts are per node: each node lists and
resolves only the copies it kept. The verbs are listed per transport in
[api.md](api.md) and the operator steps are in [user.md](user.md).

## Boundary: qrdb credentials (L2) vs quicbaas secrets/KMS (L4)

These are deliberately **not** the same thing and do **not** overlap:

| | `pkg/credentials` (qrdb, L2) | quicbaas KMS / `pkg/secrets` (L4) |
|---|---|---|
| **Purpose** | Minimal, self-contained custody of the handful of **operational/boot secrets** the engine itself needs — chiefly the **ceremony `manual` strategy** (security officers submit non-resident secrets that block retrieval until provided). | Full **product secrets-management & KMS** for application/tenant secrets: rich key lifecycle, per-key authz, key-use audit, operator-settable rotation policy. |
| **Layer** | Runs in **standalone qrdb** with no quicbaas present (xkms/quicbaas embed qrdb). | Built **on top of** qrdb's engine; absent in standalone qrdb. |
| **Backing** | `manual` = in-memory go-enclave sealed enclave; `barrier` = system records on the system shard's replicated KV (encrypted at rest by the system barrier). | qrdb engine (KV/barrier) via the SDK, plus xkms hardware seal injection. |
| **Scope** | A small set of named engine secrets + versioned rotation, optionally on a schedule. | Arbitrary tenant/app secret trees, large scale. |

### Alternatives rejected

- **A general secrets/KMS product here.** Rejected, and the reasoning is the
  table above: qrdb ships the engine's own minimal custody. quicbaas's KMS
  composes *above* it with per-key authorization, key-use audit and
  operator-settable rotation policy. Two things, two layers.
- **Storing credentials in the user KV store.** Rejected: **credentials never
  live in the user KV store.** They are system records under the reserved
  `__sys:` prefix of the system shard, which no user KV call reads, writes or
  lists, and they are distinct from the KEK/DEK key custody (the seal
  barrier).
- **A `secrets`/`kms` package in qrdb.** Rejected: **`pkg/credentials` is
  the only secret-bearing store in qrdb.**
- **Resolving secret references into plain strings.** Rejected — see the
  protected-field rule below.

## Secret resolution

`pkg/secret` resolves `credential://` and `literal://` references in an
embedding application's configuration at boot (`qrdbd`'s own configuration
carries none). A `credential://` reference resolves into an **enclave-backed
`SecretValue` target rather than a plain string**. A protected config field
must reference a secret held elsewhere, so it refuses an inline `literal://`
value, and resolution **fails closed** when it cannot.

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
  garbage-collected heap bytes. A read opens the enclave straight into a
  fresh `*secure.Buffer`; the enclave is destroyed on delete, and on a
  rotation with no grace period.
- **`barrier` credentials are system records on the system shard**, encrypted
  at rest by each node's system barrier. A sealed node has paused the system
  shard and wiped its keys, so it cannot read them.
- **Retrieval under `manual` blocks until an operator submits the value.** That
  is the ceremony case: a non-resident secret exists nowhere until someone
  provides it.
- Each verb carries its own permission; **`credentials:submit`,
  `credentials:rotate` and `credentials:delete` are sensitive and distinct from
  reads**.

## Tenant stores

**Every tenant has its own named-credential store; the platform's store is the
system tenant's.** `service.CredentialService` resolves the store from the
caller's tenant on every call. The system tenant, and a caller with no tenant,
gets the node-wide store. A tenant with a dedicated barrier gets a
`barrier`-strategy store over that barrier, built by
`credentials.CredentialService.ForBarrier`, which shares the node-wide store's
audit logger. So a tenant's credential operations land in the same audit trail.
The `credentials:*` permissions are tenant self-scope
(`pkg/permission/self_scope.go`): a tenant administrator can grant them inside
the tenant, and a grant never reaches another tenant's store or the platform's.
A list returns the caller's tenant's names only, and a name may exist in many
tenants with different values.

A tenant's store is refused, never substituted, in three cases:

| Tenant | Result |
|---|---|
| On the shared system barrier (`prefix` isolation) | `CapabilityUnsupportedError`: the tenant has no dedicated barrier to keep a credential store under |
| With a dedicated barrier that is sealed | `BarrierSealedError` |
| On a node with no barrier registry bound | `CapabilityUnsupportedError`: tenant credential stores are not bound |

**A tenant on the shared barrier is refused rather than given a key prefix
inside the system store.** The system barrier has no per-tenant key namespace
for credentials, and serving the node-wide store to a tenant would hand it the
platform's secrets. `pkg/secret` refuses the same tenants for the same reason
([secret-resolution.md](secret-resolution.md)). A tenant that needs
credentials is onboarded with `barrier`, `volume` or `raft` isolation.

**Rotations of one name are not serialized against each other.** Two
concurrent `Rotate` calls on the same name in the same store each read the
current version and write the next one, and the later write wins. This is a
known property of the store, the same in every tenant and in the system store.
Serializing those writes is the job of the row-lock work at the same point,
not of this feature.

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

The exported Go interfaces and implementations have no `[]byte` form of these
methods; Go callers get only the `*secure.Buffer` forms. REST, gRPC, QUIC, MCP
and Unix carry credential retrieve responses as secret-bearing wire DTOs at the
process boundary.

## Scheduled rotation

A named credential under the `barrier` strategy can carry a rotation policy:
`enabled`, an `interval`, and `retain_versions`, the number of versions kept
after a rotation, the current one included. An owner ruling replaced the
earlier "no automatic rotation policy" non-goal: the engine's own secrets
need scheduled rotation even in a standalone qrdb with no KMS above it. The
policy is set and read through `CredentialServicer` on every transport, under
`credentials:rotate`. The `manual` strategy refuses every policy, because its
values come from an officer, not the engine.

**Every node runs the scheduler, and the cluster rotates a credential once per
interval.** Each node checks every `seal.credential_rotation_check_period`
(default `1m`). A due credential is claimed by a compare-and-swap on its
metadata record that records an expiring claim, and only the node holding the
claim calls the value source, so a source that changes a password or issues a
key outside the store runs once per rotation, not once per node. The rest see
the claim and do nothing. No leader election or extra coordination record is
needed. The record is the lock: it lives in the replicated store on the system
shard (above), and the compare-and-swap is one Raft transaction, so exactly
one node takes each claim.

**Every writer of the current value commits once.** A rotation writes the
retained previous version, the new current value and the metadata record in
one guarded batch, one Raft transaction that applies only while the metadata
record and the current value are exactly what the rotation read. A scheduled
rotation first stages its new value in a batch that also names the staged
version in the metadata, and commits only while the record still carries its
claim and still names that version; a manual rotation or a stored value that
lands in between clears the name, and the scheduled rotation then writes
nothing. Storing a value goes through the same batch, guarded on the metadata.
The store must therefore offer the guarded batch
(`kvstore.MultiGuardedBatchKVStore`); one that does not is refused with
`ErrStoreNotAtomic`.

**A rotation that does not complete is retried, not skipped.** The schedule
moves one interval forward only when the claimed rotation lands. A rotation
that fails releases its claim and leaves the credential due, so the next check
retries it. A claim whose holder crashed lapses after a five-minute lease, and
the credential is due again, on any node and after a restart. The lease is
timed from the moment the claim is taken, not from the start of the check, and
the holder runs the value source and the rotation under a deadline of 30
seconds or the lease's own expiry, whichever is sooner, so it has stopped
writing before another node may take the claim. That bound is per credential:
an attempt past it releases its claim and stays due, and the pass goes on to
the next credential. The scheduler puts no deadline on the pass as a whole,
so one slow value source does not starve the credentials after it. Ending the claim, by completing or releasing it, gets its own 30-second
bound that starts once the rotation has returned, so a rotation that used its
whole lease can still settle its claim.

**Retention is retried apart from the rotation.** A completed rotation marks
the metadata record with a pending cleanup, and clears the mark only once the
expired-version reaping and the trim to `retain_versions` have both succeeded.
If either fails, every later check retries the cleanup, without calling the
value source again, until it lands. A transient storage fault therefore leaves
an old version readable for one check period, not for a whole interval. Every writer of the metadata record (the policy, the
claim, a rotation, the history trim) updates it conditionally and re-reads on
a conflict, so none overwrites another: a policy disabled during a rotation
stays disabled.

**The new value comes from a seam.** `credentials.WithRotationValueSource`
injects a `RotationValueSource`, which returns the next value as a
`*secure.Buffer`. Minting a credential means acting where the credential is
used: changing a database password at the database, or issuing a key at its
provider. That knowledge belongs to whoever embeds qrdb, not to the engine.
`qrdbd` ships no source. Without one, a due rotation fails with
`ErrNoRotationValueSource` and the credential stays due. A failing source
releases the claim, so it never uses up an interval.

## What this feature deliberately does not do

- **No product secrets management.** That is quicbaas's KMS, one layer up.
- **No PIN storage.** `pkg/pin` owns PINs and their lockout semantics.
- **No KEK/DEK custody.** That is the barrier.
- **No built-in source of new credential values.** Scheduled rotation calls a
  `RotationValueSource` the embedding application supplies; qrdb does not know
  how to mint a value for an arbitrary secret, and `qrdbd` ships no source.
