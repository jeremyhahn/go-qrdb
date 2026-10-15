# Verifiable log: the audit chain, generalised into a database type

**Status (2026-10-15): approved with corrections** (Company Agent review,
2026-10-15). Nothing here is implemented. Every item under [Decided](#decided)
names its source; [Design](#design) is the design that implements them, and its
[§9](#9-choices-decided-in-review) records the choices the review decided. One
choice is still open, with go-xkms: [Open questions](#open-questions).

## Decided

Each item is stated as a fact the design does not reopen, with its source.

- **One database type.** go-qrdb's whole part in the trusted service plane is a
  *verifiable log*, created per tenant like key-value, time-series and vector:
  append-only (no update, no delete), sequence-numbered entries, a Merkle tree
  over them, inclusion proofs, consistency proofs between tree sizes,
  checkpoints, and tree heads signed by the log owner's key (the CA key for
  go-xkms's transparency log, a tenant key for a tenant's own log). It is served
  on every transport and in the SDK with the tenant's isolation, DEK, barrier
  and replication. *Source: owner ruling 2026-10-14, the verifiable-log type and
  the service-plane design, relayed by the Company Agent 23:26Z.*
- **No PKI semantics.** What is logged, the signed-head policy and any CA or
  service meaning belong to go-xkms (its transparency log is a CA service) and
  to go-quicbaas (which appends its own records through go-xkms). go-qrdb owns
  the primitive. *Source: owner ruling 2026-10-14, relayed 23:26Z.*
- **Engine surface:** create, append, get, proof-inclusion, proof-consistency,
  head, and a sign-head hook — a seam go-xkms implements with its KMS or
  hardware signer. go-qrdb ships only the software signer over a tenant key.
  *Source: owner ruling 2026-10-14, relayed 23:26Z.*
- **No new state machine.** The log generalises `pkg/audit.Chain`. There is one
  apply-time opcode like `protocol.OpAuditAppend`, behind a cluster feature gate
  for mixed-version groups, dispatched by both state-machine families, attached
  unconditionally in the Raft factory and in `lifecycle.NewLocalEngine`, and
  restored on open and on snapshot. The sequence is assigned at apply.
  *Source: Company Agent amendment, 2026-10-14 23:18Z.*
- **Three generalisations:** a Merkle tree with inclusion and consistency
  proofs; named per-tenant log instances; owner-signed heads. A head is signed
  outside apply and appended as a record. The audit trail becomes one log
  instance. *Source: Company Agent amendment, 2026-10-14 23:18Z.*
- **Move, never delete and rewrite.** `pkg/audit.Chain` and its tests evolve in
  place or move, with their history, to the destination package.
  *Source: owner rule, 2026-10-14 23:29Z.*
- **The review's corrections:** `vlog:sign_head` is an Operator permission,
  never Admin, as `keys:sign` is, and Admin creates and expires logs
  ([Authorization](#authorization)); the per-state-machine audit chain is a fix
  of its own that lands ahead of the move ([Order](#order), step 1).
  *Source: Company Agent review, 2026-10-15.*

## What already exists

The replicated audit chain is most of the mechanism already
([../audit/architecture.md](../audit/architecture.md#cluster-wide-replicated-trail)):

| Piece | Where | What it does today |
|---|---|---|
| `audit.Chain` | `pkg/audit/chain.go` | In-memory head per tenant (`headState{seq, lo, digest}`), restored from storage, updated only in Apply. `Append` assigns the next sequence and a linear SHA-256 digest (`digest = SHA-256(prev ‖ event JSON)`, `PrevHash` embedded in the event); `Prune` drops a retired prefix from the in-memory head alone; `Query` and `Verify` read. |
| The table | `pkg/audit/table.go` | A reserved raw-keyspace table, `TablePrefix = "\x00qrdb.audit/"`, with `h/<tenant>` head records and `e/<tenant>/<seq>` event records. `IsTableKey` keeps it out of KV `Range` and watch. |
| The opcode | `pkg/protocol/audit.go` | `OpAuditAppend` (79) with sub-types `AuditSubtypeAppend` and `AuditSubtypePrune`; `EncodeAuditAppend` refuses a zero timestamp or a tenant mismatch before proposal. |
| The gate | `pkg/cluster/featuregate/featuregate.go` | `featuregate.AuditAppend` (`"audit-append"`). |
| Dispatch | `pkg/engine/mvcc_engine.go` (`handleAuditAppend`), `pkg/store/pebble.go` (`pebbleHandleAuditAppend`) | Both families apply the opcode, answer `audit.QueryRequest` / `audit.VerifyRequest` in their read path, restore the chain on open and snapshot recovery, and resync it after a failed apply. |
| Attachment | `pkg/store/factory.go` (`WithAuditChain`), `pkg/store/mvcc.go`, `pkg/store/pebble.go` (`SetAuditChain`), `pkg/engine/mvcc_engine.go` (`WithMVCCAuditChain`), `cmd/qrdbd/start.go` (`buildSMCreateFunc`), `pkg/server/lifecycle/local.go` (`NewLocalEngine`) | A chain is attached unconditionally to the system shard's state machine — the only group `buildSMCreateFunc`'s factory starts — and to every local engine. Tenant and database groups (`newDataPlaneCreateFunc`, `pkg/server/tenant_host.go:507`) get none. |
| Proposer and reader | `pkg/service/audit_replicated_logger.go` (`ReplicatedLogger`), `audit_optin_logger.go`, `audit_shard_resolver.go`, `audit_errors.go` | Routes every append to the system shard (`SystemShardAuditResolver`, `pkg/service/audit_shard_resolver.go:75`; a dedicated-isolation tenant is refused with `DedicatedTenantAuditNotSupportedError`), checks the gate, sets the timestamp, proposes; reads back through `SyncRead`. |
| Node-local trail | `pkg/audit/chain_file_logger.go`, `chain_verify.go`, `trail.go`, `trail_dir.go`, `verify.go` | The hash-chained NDJSON file a node writes, its rotation-set verifier and the trail composition. A separate mechanism from the replicated chain. |

**One fix that lands first, on its own.** `Chain.Restore`
(`pkg/audit/chain.go:102`) replaces the whole head table with what one state
machine's storage holds, which is why its doc comment (`pkg/audit/chain.go:73`)
requires exactly one chain per state machine instance. Today that holds only
because of how the chain is wired, not by construction: `cmd/qrdbd/start.go:1419`
builds one chain per node and hands it to `buildSMCreateFunc`
(`cmd/qrdbd/start.go:1628`), whose factory is stored at
`cmd/qrdbd/start.go:1632` and used by `initializeNode`
(`cmd/qrdbd/start.go:3297`) to start the single system shard
(`cmd/qrdbd/start.go:3425`); the offline restore (`cmd/qrdbd/restore.go:204`,
`:223`) restarts that same shard, and each open re-runs `Restore` from its own
storage. `lifecycle.NewLocalEngine` already builds its own chain per call
(`pkg/server/lifecycle/local.go:152`). The node-wide instance is one factory
reuse away from the defect: any second group started from that factory would
share heads with the system shard, and named per-tenant logs attach to tenant
and database groups, which `newDataPlaneCreateFunc` creates many per node
(`pkg/server/tenant_host.go:131`, `pkg/server/raft_backend_factory.go:908`).
So the fix is a separate change ahead of the move ([Order](#order), step 1):
the create function builds a fresh chain for every state machine it creates,
there is no node-wide instance in `cmd/qrdbd/start.go`, and the constraint
holds by construction.

## Design

**Constraints the design holds to.** Both modes, standalone and Raft (§1);
nothing node-local (§1); strict tenant isolation (§8); every cryptographic
primitive from an audited library (§4, §6, §9). These are the platform's
standing rules, applied here rather than ruled for this design.

### 1. Modes

The log lives in the state machine's own storage, under a reserved raw-keyspace
table, so the same code serves both modes with no branch on mode:

- **Raft.** An append is a proposal through the tenant's resolved shard — the
  system shard for the system tenant, the tenant's own group for an isolated
  tenant — and every replica applies it identically. Reads that must be current
  (head, proofs at the latest size) are linearizable reads through the group;
  any node serves them, forwarding as replica forwarding already does.
- **Standalone.** `lifecycle.NewLocalEngine` attaches the log exactly as it
  attaches the audit chain today, and a proposal applies locally through the
  same handler. A standalone node is one replica; nothing about the handler
  knows the difference.

Nothing is node-local: entries, tree nodes, heads, checkpoints, log descriptors
and the software signer's sealed key are all records in the tenant's resolved
store, so a backup of the tenant is a backup of its logs.

### 2. The opcode and its sub-types

One new opcode, `OpVerifiableLog`, in a new `pkg/protocol/vlog.go` beside
`audit.go`, framed the same way: `[subtype:1][tenantIDLen:2][tenantID][body]`.
The body names the log (database and log name) and carries the sub-type's
fields.

| Sub-type | Body | Apply does |
|---|---|---|
| `Create` | log name, descriptor (origin, hash, verifier keys) | Refuses a name that exists; stages the descriptor record and an empty head. |
| `Append` | one entry, or a bounded batch of entries | Assigns each entry the next index, stages the entry, its leaf hash and every tree node the append completes, and the new head (§4). Returns the first assigned index. |
| `Checkpoint` | tree size, root hash, signed checkpoint bytes | Checks that the size is no larger than the log's size and that the root is the tree's root at that size (§5); stages the checkpoint record. |
| `ExpireBodies` | cutoff index | Only on a log whose descriptor allows it, a flag set at create — the audit instance's does, for its retention (§9, decision 8). Proposed by an Admin (`vlog:expire`) or by the audit retention scheduler. Deletes entry bodies below the cutoff; leaf and node hashes stay, so the tree and every proof remain valid. |

There is no sub-type that updates or deletes an entry, and no per-log delete:
a log dies with its database, through the drop and keyspace reclaim every
database type already uses
([../databases/architecture.md](../databases/architecture.md)).

Refusals follow the audit handler's rule exactly: a malformed body, a missing
log or an out-of-range checkpoint is a deterministic consequence of the
committed bytes, answered with `statemachine.RefusalResult`; a table-write
fault is local to the replica and halts apply.

### 3. The feature gate, and a mixed-version group

A new gate, `featuregate.VerifiableLog` (`"verifiable-log"`), in
`pkg/cluster/featuregate/featuregate.go`. A binary that carries the log
advertises it.

- **Before the gate is enabled for a group** — some replica runs a binary
  without the log — the service refuses every create, append and checkpoint
  for a log on that group with a typed `LogFeatureNotActiveError` before
  proposing, as `ReplicatedLogger.Emit` refuses with
  `AuditFeatureNotActiveError` today. Reads of a log that does not yet exist
  answer not-found. Nothing is proposed, so nothing diverges.
- **The audit instance keeps working unchanged while the gate is closed.** The
  service goes on proposing `OpAuditAppend`, and every binary — old and new —
  applies it as today: the linear chain only. No new binary writes a Merkle
  node for an `OpAuditAppend`, so old and new replicas stay identical.
- **Once the gate is enabled** for the group, the service proposes audit events
  as `OpVerifiableLog`/`Append` to the audit instance (§7). The switch is a
  property of the committed log, not of any node's view: every entry before it
  is an `OpAuditAppend`, every entry after it an `OpVerifiableLog`, and every
  replica applies both identically.
- After activation (every node advertises it), the gate is permanent, as for
  every gate: a binary without the log refuses to start.

`OpAuditAppend`'s apply stays in both dispatch tables. Its entries are in
existing Raft logs and snapshots, and a replica replaying them must apply them
as they were applied. Proposers stop emitting it once the gate is enabled.

### 4. Apply-time sequencing and the tree

Everything in apply follows `audit.Chain`'s rules, which exist because Raft may
pack any number of committed entries into one `Update` call:

- **The index is assigned at apply** from the log's in-memory head, never from
  the proposal.
- **Apply never depends on whether an earlier entry of the same `Update` call is
  visible in storage.** `Chain.Prune` was first written to read storage and
  answered differently for one batch than for four. The log keeps, per state
  machine, the in-memory head of each log and the tree nodes it staged in the
  current `Update` call, and reads through them.
- **Hashing happens at apply** (§9, decision 3). The log keeps each log's
  *compact range* — the root hashes of the perfect subtrees covering
  `[0, size)`, at most one per bit of `size` — in its head. Appending a leaf
  hashes it, merges it into the compact range, and reports every perfect
  subtree node the merge completes; apply stages each completed node. That is
  one leaf hash and on average one node hash per entry, and on average two node
  records per entry. The Merkle algorithms (leaf and node hashing, the compact
  range, proof construction and verification) come from `github.com/transparency-dev/merkle` (§9, decision 1); go-qrdb writes no hashing of its own.
- **The head record** holds the size, the compact range and, for the audit
  instance only, the linear digest and low-watermark it keeps today (§7).

Records, all under one new raw-keyspace table that `IsTableKey`'s successor
keeps out of KV `Range`, watch, MVCC and every protocol shim:

| Record | Key (under the log's prefix) | Value |
|---|---|---|
| Descriptor | `d` | origin, hash, verifier keys, the policy flags the owner set at create |
| Head | `h` | size, compact range, (audit instance) linear digest and low-watermark |
| Entry | `e/<index>` | the entry bytes as appended |
| Tree node | `n/<level>/<index>` | the hash of a completed perfect subtree |
| Checkpoint | `c/<size>` | the signed checkpoint for that size |

The log's prefix is `<table prefix><tenant>/<database>/<log>/`, with the
tenant-id rule `validateTenantID` enforces today extended to database and log
names (no `/`), so no name can carve a segment inside another's range.

**Proofs are reads.** An inclusion proof for index *i* at size *n*, and a
consistency proof between sizes *m* and *n*, are each a bounded set of node
identifiers the library computes; the read fetches those node hashes (every one
is a stored perfect subtree, or a short rehash of stored ones the library
describes) — O(log n) point reads, no tree walk. Proofs at any size up to the
current one are served, because every perfect node ever completed is stored.

### 5. Signed heads, outside apply

A signature is never computed in apply: the signer is a key the owner holds,
possibly in a device, and apply must be a pure function of committed bytes.

1. A caller with the sign-head permission asks the service to sign the head of
   a log (on demand — §9, decision 4).
2. The service reads the head through the group (size *n*, root *r*), builds
   the checkpoint body for (origin, *n*, *r*) as a C2SP checkpoint (§9, decision 2), and asks the sign-head seam (§6) for the owner's signature.
3. The service verifies the returned signature against the log's registered
   verifier key, then proposes `OpVerifiableLog`/`Checkpoint` with *n*, *r* and
   the signed bytes.
4. Apply checks *n* ≤ size and that *r* is the tree's root at *n*, computed from
   the stored perfect nodes covering `[0, n)` read through the current call's
   staged nodes. It does **not** verify the signature: signature verification
   is done by the proposing service and by every client, and keeping it out of
   apply keeps apply independent of a verifier library's version in a
   mixed-version group. A checkpoint whose root does not match is refused.
5. The checkpoint is appended as a record (`c/<size>`), append-only. Reading the
   latest checkpoint, or the one at a size, is the `head` operation.

A checkpoint is not a leaf of the tree it describes: a head that entered its own
tree would describe a tree it had already made stale. The record is beside the
tree, append-only, in the same table and the same snapshot.

**Key rotation** is a descriptor change through the same opcode: the
descriptor's verifier keys are an append-only list, and a checkpoint names the
key that signed it. Which key signs, and when keys rotate, is the owner's
policy.

### 6. The sign-head seam

In `pkg/vlog` (§9, decision 7). `HeadSigner` has the shape of
`golang.org/x/mod/sumdb/note`'s `Signer` (`Name`, `KeyHash`, `Sign`, in that
module's `sumdb/note/note.go`) with a context added, so a signer of any
signature type plugs into the note library unchanged: the signer, not go-qrdb,
owns the signature type and its encoding.

```go
// HeadSigner signs a log's checkpoints with the log owner's key.
type HeadSigner interface {
	// Name is the key name the checkpoint's signature line carries.
	Name() string
	// KeyHash is the key hash the signature line carries.
	KeyHash() uint32
	// Sign returns the signature over msg, the checkpoint body, in the
	// signer's signature type.
	Sign(ctx context.Context, msg []byte) ([]byte, error)
}

// HeadSignerProvider returns the signer for one log's owner key.
type HeadSignerProvider interface {
	HeadSigner(ctx context.Context, req HeadSignerRequest) (HeadSigner, error)
}

// HeadSignerRequest names the log and the owner's key for it.
type HeadSignerRequest struct {
	TenantID string
	Database string
	Log      string
	// KeyRef is the owner's opaque reference to its key, recorded in the
	// descriptor at create. go-qrdb never interprets it.
	KeyRef string
}
```

- **Injection:** `WithHeadSignerProvider(p HeadSignerProvider) ServerOption` in
  `pkg/server/seam_options.go`, beside `WithDEKProvider`, composed through
  `sdk/go/node` like the other seams. Nil keeps the software provider.
- **The software provider go-qrdb ships** generates one Ed25519 key per log at
  create (`crypto/ed25519` over `crypto/rand`, the standard library's validated
  module), seals the private key with the tenant's DEK through `dek.Provider`
  (`pkg/dek/provider.go`), with the additional data binding tenant, database,
  log and key version, and stores it as a record in the tenant's resolved store:
  replicated, under the tenant's barrier, and fails closed when the tenant is
  sealed. Its public key is the descriptor's verifier key. Any node can sign; the
  key never leaves the server.
- **What go-xkms implements:** a `HeadSignerProvider` that resolves `KeyRef` to
  its KMS or hardware key, and for its transparency log returns a signer over
  the CA's key (the same `crypto.Signer` source `pkg/cakey.Provider` exposes),
  in the signature type the [open question](#open-questions) settles for a
  non-Ed25519 key. go-quicbaas composes go-xkms's provider; it does not
  implement one.

### 7. The audit trail as one log instance

The replicated audit chain becomes the log named `audit` in each tenant's
reserved system database, with a descriptor that allows `ExpireBodies` and keeps
the linear digest. Its existing records keep their keys under
`"\x00qrdb.audit/"`: the audit instance's prefix **is** the existing table, so
the upgrade moves no data.

- **Existing data reads unchanged.** Every record `OpAuditAppend` wrote stays
  where it is, `Query` reads it as it does, and `Verify`'s linear fold covers it
  as it does.
- **The tree starts at the first `OpVerifiableLog` append** to the instance, at
  sequence *S₀*, as leaf 0; the descriptor records *S₀*. That first leaf's bytes
  are the marshaled event with its `PrevHash`, the linear digest of everything
  before it, so the tree commits transitively to the whole pre-upgrade chain.
  No backfill command, no migration of stored records.
- **The linear chain continues** for the audit instance: each append still sets
  `PrevHash` and advances the linear digest, so `ChainContinuityReport` and
  every existing verifier keep their meaning. Named logs do not keep a linear
  digest; their Merkle root is their head.
- **Retention** stays a replicated, deterministic, bounded command: on the
  audit instance `Prune` becomes `ExpireBodies`, which removes event bodies and
  keeps the tree (§9, decision 8).
- **The node-local trail is unchanged.** `ChainFileLogger` is a different
  mechanism — one node's file, written before the store is unsealed, and the
  sink an audit-write failure falls back on — and this design neither moves nor
  generalises it.

### 8. Isolation, barrier, DEK and replication

- **Isolation.** A log is addressed by (tenant, database, log). The service
  confines the tenant exactly as audit confinement does (`tenant.Confine`); the
  resolver routes to the tenant's own store; the key prefix contains the tenant,
  so a read never leaves the tenant's range even inside a shared table. In
  strict mode every record is in the tenant's own store.
- **Barrier.** The tenant's (or the database's) barrier encrypts the records at
  rest, below apply, as it does for every other record. Apply therefore hashes
  the entry as appended.
- **DEK.** The DEK seals the software signer's key. A caller that wants entries
  the platform cannot read encrypts them with `DEKService` before appending; the
  leaf then commits to the ciphertext, which is the caller's choice and the
  standard shape for a confidential transparency log.
- **Replication.** Raft, through the tenant's group, like everything else.
- **Limits.** One entry is bounded by the pre-authentication request cap every
  transport applies, and an `Append` batch by a per-command maximum, as
  `MaxPrunePerCommand` bounds a prune.

### 9. Choices decided in review

Each was an open question on the proposed page; the decision and its source are
the Company Agent review, 2026-10-15. The alternatives considered are kept with
the reason each lost.

1. **Merkle library and hash: `github.com/transparency-dev/merkle`,** RFC 9162 /
   RFC 6962 tree hashing over SHA-256 (`0x00` leaf and `0x01` node prefixes),
   through its `rfc6962` hasher and its `compact` and `proof` packages. The
   compact range gives exactly the apply-time node visitor §4 needs, and it is
   the hash every CT log and verifier uses. Not taken:
   `golang.org/x/mod/sumdb/tlog` — the same tree hashing and already an indirect
   dependency, but shaped around tiles and its own hash reader, a less direct fit
   for per-append node staging; it stays compatible because the tree is the same.
   Trillian as a service — a second datastore, refused by the single-datastore
   rule. One new dependency.
2. **Checkpoint format: C2SP `tlog-checkpoint` in a C2SP `signed-note`**:
   origin, size and root as text lines, signatures as note lines, witnessable by
   the C2SP cosignature ecosystem. `golang.org/x/mod/sumdb/note` signs and
   verifies its Ed25519 type, the only type that module defines (`algEd25519`, in
   its `sumdb/note/note.go`); that is the software provider's type. Not taken: an
   RFC 6962 `SignedTreeHead` structure (binary, not witnessable by the note
   ecosystem, superseded by RFC 9162) and a go-qrdb format (nothing would verify
   it). **The signature type for a non-Ed25519 owner key is still open**, to
   agree with go-xkms ([Open questions](#open-questions)).
3. **Hashing at apply** (§4): the tree is always current and atomic with the
   entry, with one proposal per append and about two SHA-256 and two record
   writes per entry. Not taken: an integrator proposing tree hashes after commit
   (proofs lag appends, two proposals, a second moving part, replicas re-checking
   its hashes), and per-node computation from committed entries (node-local
   derived state).
4. **Heads signed on demand** (§5): `SignHead` signs the current head when a
   caller holding `vlog:sign_head` asks; the cadence is the owner's policy. Not
   taken: a per-log interval acted on by a group-leader scheduler, which would put
   signing policy in go-qrdb; it stays additive if a consumer asks for it.
5. **A build tag on the service and the transports:** `WITH_QRDB_VLOG ?= 1`,
   tag `qrdb_vlog`, like `WITH_QRDB_TS` and `WITH_QRDB_VECTOR`. Off removes the
   named-log service, its transport handlers, the SDK surface and the CLI, and
   `admin capabilities` omits `vlog`. The engine core, the opcode's apply and the
   audit instance are never tagged: the audit trail depends on them, and a
   replica must apply what its group commits whatever its build, so a binary
   built without the tag still advertises the gate.
6. **Tiles later.** Proofs and checkpoints go through the service now; C2SP
   `tlog-tiles` is added later as a read-side rendering of the stored node
   records (§4 stores every perfect node, so a tile is a read of 256 of them),
   with no change to storage.
7. **The package is `pkg/vlog`.** The generic half of `chain.go` and all of
   `table.go` move there with their history (the [move table](#the-move-table));
   `pkg/audit` keeps the audit instance (event validation, `PrevHash`, `Query`,
   `Verify`) and the node-local trail, and `audit.Chain` stays there as the
   audit instance's adapter. Not taken: generalising `Chain` in place in
   `pkg/audit`, which would make the audit package own a database type; and the
   name `pkg/tlog`, which collides with `golang.org/x/mod/sumdb/tlog`.
8. **Retention by `ExpireBodies`** (§2, §7): allowed only on a log whose
   descriptor allows it; bodies below the cutoff are removed, leaf and node hashes
   stay, so the tree and every proof stay valid and an expired entry reads as
   expired. Not taken: no retention on the audit instance (a compliance trail
   that must age out would grow without bound), and keeping the audit chain
   outside the log type (contradicts the ruling that the audit trail becomes one
   log instance).

## The move table

Every file of `pkg/audit`'s replicated chain and every caller, with where it
goes. The destination package is `pkg/vlog` (§9, decision 7).

**How a move is made.** A whole file moves with `git mv` in a commit that
changes nothing else, so `git log --follow` finds its history. Where a file
splits, the moving functions are cut and pasted unchanged in one commit and
generalised in the next, so `git blame -C -C` follows them. No file is deleted
and rewritten.

### pkg/audit

| Source | Destination | Kept | Generalised |
|---|---|---|---|
| `pkg/audit/table.go` | `pkg/vlog/table.go` (`git mv`) | `TablePrefix` (the audit instance's prefix), the head/event key builders, `encodeHead`/`decodeHead`, `prefixEnd`, `IsTableKey`, `MaxPrunePerCommand` | Keys built from a log prefix; node, descriptor and checkpoint records added; the head record gains the compact range; `IsTableKey` covers both prefixes. |
| `pkg/audit/chain.go` — `Chain`, `headState`, `Writer`, `NewChain`, `Restore`, `validateTenantID`, the sequencing and staging half of `Append`, the head arithmetic of `Prune` | `pkg/vlog/log.go` (cut and paste, then generalise) | The apply-time head, restore-then-apply contract, the `Writer` staging interface, the no-storage-read rule and its doc comment | `Chain` becomes the per-state-machine log registry keyed by (tenant, database, log); `Append` adds leaf hashing and node staging; `Prune` becomes `ExpireBodies`; create, checkpoint and proof reads added. |
| `pkg/audit/chain.go` — the audit half: `Append`'s event validation, JSON encoding and `PrevHash` fold, `Query`, `Verify`, `ChainContinuityReport`, `decodeReplicatedDigest` | stays in `pkg/audit/chain.go` | All of it, behaviour unchanged | `Append`, `Query` and `Verify` call the audit instance of `pkg/vlog` instead of touching the table themselves. |
| `pkg/audit/chain_errors.go` | split: `TableWriteError`, `TableReadError`, `CorruptTableError`, `InvalidTenantIDError` to `pkg/vlog/errors.go`; `EncodeEventError`, `MissingTimestampError`, `InvalidCursorError`, `TenantMismatchError` stay | Every type, message and `Unwrap` | `pkg/audit` re-exports nothing; callers that match the moved types (`handleAuditAppend`, `pebbleHandleAuditAppend`) import `pkg/vlog`. |
| `pkg/audit/query_request.go` | stays | `QueryRequest`, `VerifyRequest` | New read requests (`vlog.GetRequest`, `InclusionRequest`, `ConsistencyRequest`, `HeadRequest`) sit beside them in `pkg/vlog`. |
| `pkg/audit/chain_verify.go`, `chain_file_logger.go`, `trail.go`, `trail_dir.go`, `verify.go` | stay | All of it | Nothing: the node-local trail is out of scope (§7). |
| `pkg/audit/table_test.go` | `pkg/vlog/table_test.go` (`git mv`) | `TestIsTableKey`, `TestEventSeq`, `TestDecodeHead` | Cases for the new records and prefixes. |
| `pkg/audit/chain_test.go` | split as `chain.go` splits | Sequencing, restore, prune-cap and tenant-id tests move to `pkg/vlog/log_test.go`; tenant-mismatch, empty-tenant, zero-timestamp, query and pagination tests stay | Determinism-across-replicas and restore tests run over named logs and the tree. |
| `pkg/audit/chain_fault_test.go` | split the same way | Restore-path fault tests move; query-path fault tests stay | Fault injection on node and checkpoint records. |
| `pkg/audit/chain_verify_replicated_test.go` | stays | All of it | Run over an instance that crossed the upgrade (§7), proving the linear verify still holds. |
| `pkg/audit/chain_errors_test.go` | splits with `chain_errors.go` | Every case | None. |
| `pkg/audit/chain_bench_test.go`, `chain_file_logger_test.go`, `chain_rotation*_test.go`, `chain_verify_helpers_test.go`, `trail_test.go`, `trail_confine_test.go`, `verify_test.go` | stay | All | None — they test the node-local trail. |

### Callers

| Source | Destination | Kept | Generalised |
|---|---|---|---|
| `pkg/protocol/audit.go` | stays | `OpAuditAppend`, both sub-types, the encoders and `DecodeAuditCommand` | `pkg/protocol/vlog.go` adds `OpVerifiableLog` with the same framing. |
| `pkg/cluster/featuregate/featuregate.go` | stays | `AuditAppend` | Adds `VerifiableLog`. |
| `pkg/engine/mvcc_engine.go` | stays | `handleAuditAppend`, the `audit.QueryRequest`/`VerifyRequest` read cases, restore on open, `resyncAuditChain` | `WithMVCCAuditChain` becomes `WithMVCCLogs`; a `handleVerifiableLog` entry in the dispatch map; the new read requests; restore and resync cover every log. |
| `pkg/store/pebble.go`, `pkg/store/mvcc.go`, `pkg/store/factory.go` | stay | `pebbleHandleAuditAppend`, `restoreAuditChain`, the read cases | `SetAuditChain`/`WithAuditChain` become `SetLogs`/`WithLogs`, renamed in place with every caller; the same additions as the MVCC engine. |
| `cmd/qrdbd/start.go` (`buildSMCreateFunc`) | stays | Unconditional attachment | Step 1: the node-wide `audit.NewChain()` (`cmd/qrdbd/start.go:1419`) goes, and the create function `buildSMCreateFunc` returns builds a fresh chain for each state machine it creates; from step 3 that chain is the log registry carrying the audit instance. |
| `pkg/server/tenant_host.go` (`newDataPlaneCreateFunc`) | stays | The barrier, lessor, watch hub and time-series options | Attaches a log registry built **inside** the returned create function, one per state machine, so the many tenant and database groups on a node never share heads. |
| `pkg/server/lifecycle/local.go` (`NewLocalEngine`), `pkg/server/lifecycle/lifecycle.go` | stay | Unconditional attachment; the aggregate audit logger | Attach the registry; compose the log service. |
| `pkg/service/audit_replicated_logger.go`, `audit_optin_logger.go`, `audit_shard_resolver.go`, `audit_errors.go` | stay | Entitlement, routing, timestamp-at-proposer, the gate check | `Emit` proposes to the audit instance through the log service once `VerifiableLog` is enabled for the group, `OpAuditAppend` before; the resolver is shared with the log service. |
| Audit transports: `pkg/api/rest/handlers_audit.go`, `pkg/api/grpc/audit_server.go`, `pkg/api/quic/audit_frame.go`, `pkg/api/mcp/audit_tool.go`, `pkg/api/unix/audit_cmd.go`; `sdk/go/transport/{rest,grpc,quic,mcp,unix}/transport_audit.go`, `sdk/go/transport/embedded/audit.go`, `sdk/go/transport/audit_types.go`, `sdk/go/types_audit.go`; `cmd/qrdb/audit.go`, `cmd/qrdb/bindings_audit.go` | stay | Every audit capability and its shape | Unchanged; the audit instance is also reachable through the log surface (§ [Surface](#surface)) by a caller holding the log permissions on its own tenant. |

### Order

Each step leaves the tree green; the destination is wired before the old copy
goes.

1. **A separate change that lands first** (Company Agent review, 2026-10-15):
   the create function `buildSMCreateFunc` returns builds a fresh
   `audit.NewChain()` for every state machine instance it creates, and
   `cmd/qrdbd/start.go` keeps no node-wide instance, so one chain per state
   machine holds by construction rather than by today's wiring. It carries the
   test that two groups started from one create function on one node keep
   separate heads across open and recovery. No behaviour change for the system
   shard.
2. `git mv` `table.go` and its test to `pkg/vlog`; cut and paste the generic half
   of `chain.go`, its errors and their tests, unchanged; `pkg/audit` calls them.
   No behaviour change.
3. Generalise in `pkg/vlog`: named instances, the tree and node records, proofs.
   The audit instance reads and writes exactly as before.
4. `OpVerifiableLog`, the gate, dispatch in both families, restore and resync;
   `newDataPlaneCreateFunc` builds one registry per state machine the same way,
   and step 1's test runs over data-plane groups too.
5. The service, the seam and its software provider, transports, SDK, CLI.
6. The audit service switches to the audit instance behind the gate.

## Surface

### Engine

`pkg/vlog` exposes `Create`, `Append`, `Get`, `Inclusion`, `Consistency`,
`Head` (latest checkpoint and the unsigned current size and root) and
`Checkpoint` (the apply half of sign-head), each on (tenant, database, log).

### Service layer

`pkg/service/vlog_service.go`, following `VectorService`:

```go
type LogService interface {
	CreateLog(ctx context.Context, req *CreateLogRequest) (*CreateLogResponse, error)
	ListLogs(ctx context.Context, req *ListLogsRequest) (*ListLogsResponse, error)
	Append(ctx context.Context, req *AppendRequest) (*AppendResponse, error)
	GetEntries(ctx context.Context, req *GetEntriesRequest) (*GetEntriesResponse, error)
	ProveInclusion(ctx context.Context, req *InclusionRequest) (*InclusionResponse, error)
	ProveConsistency(ctx context.Context, req *ConsistencyRequest) (*ConsistencyResponse, error)
	Head(ctx context.Context, req *HeadRequest) (*HeadResponse, error)
	SignHead(ctx context.Context, req *SignHeadRequest) (*SignHeadResponse, error)
	ExpireBodies(ctx context.Context, req *ExpireBodiesRequest) (*ExpireBodiesResponse, error)
}
```

Every protocol handler delegates to it. A proof response carries the hashes, the
leaf index and both sizes, so a client verifies it with the same library
without trusting the server.

### Transports

The six client transports, in one capability matrix in the feature folder's
`api.md`:

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded | Permission |
|---|---|---|---|---|---|---|
| Create log | `POST /vlog/log` | `VlogCreate` | `vlog.Create` | `qrdb_vlog_create` | `CreateLog` | `vlog:create` |
| List logs | `GET /vlog/logs` | `VlogList` | `vlog.List` | `qrdb_vlog_list` | `ListLogs` | `vlog:list` |
| Append | `POST /vlog/append` | `VlogAppend` | `vlog.Append` | `qrdb_vlog_append` | `Append` | `vlog:append` |
| Get entries | `GET /vlog/entries` | `VlogGet` | `vlog.Get` | `qrdb_vlog_get` | `GetEntries` | `vlog:get` |
| Inclusion proof | `GET /vlog/proof/inclusion` | `VlogProveInclusion` | `vlog.ProveInclusion` | `qrdb_vlog_prove_inclusion` | `ProveInclusion` | `vlog:proof` |
| Consistency proof | `GET /vlog/proof/consistency` | `VlogProveConsistency` | `vlog.ProveConsistency` | `qrdb_vlog_prove_consistency` | `ProveConsistency` | `vlog:proof` |
| Head | `GET /vlog/head` | `VlogHead` | `vlog.Head` | `qrdb_vlog_head` | `Head` | `vlog:head` |
| Sign head | `POST /vlog/head/sign` | `VlogSignHead` | `vlog.SignHead` | `qrdb_vlog_sign_head` | `SignHead` | `vlog:sign_head` |
| Expire bodies | `POST /vlog/expire` | `VlogExpireBodies` | `vlog.ExpireBodies` | `qrdb_vlog_expire_bodies` | `ExpireBodies` | `vlog:expire` |

REST is `pkg/api/rest`, gRPC adds the methods to
`pkg/api/grpc/proto/qrdbv1/qrdb.proto` (the Unix socket serves the same gRPC
wire), QUIC is `pkg/api/quic`, MCP is `pkg/api/mcp`, embedded is
`sdk/go/transport/embedded`. Every request carries the database the way the
vector and time-series requests do. The compatibility protocols (etcd, MySQL,
PostgreSQL) carry no log: they have no shape for one, as they have none for a
vector namespace.

### SDK

`sdk/go/transport` gains the request and response types and a `LogProvider`
interface each transport implements; `sdk/go` gains `Client.Vlog()` (nil when
the transport does not implement it, as `Client.Audit()` is) and a verification
helper that checks an inclusion or consistency proof and a checkpoint's
signature against a verifier key, with the same library the server uses.

### CLI

`qrdb vlog create|list|append|get|prove-inclusion|prove-consistency|head|sign-head|expire`,
on every transport, with `--database` like the vector commands; `prove-*` and
`head` take `--verify` with a verifier key to check the answer client-side.

### Authorization

New permissions in `pkg/permission/constants.go`: `vlog:create`, `vlog:list`,
`vlog:append`, `vlog:get`, `vlog:proof`, `vlog:head`, `vlog:expire` and
`vlog:sign_head`. Each is tenant-scoped, like the vector permissions, and is
listed in `pkg/permission/self_scope.go` as `keys:sign` is. The built-in roles
are the ones `pkg/permission/role_grant.go` names (`RoleAdmin`, `RoleOperator`,
`RoleUser`, `RoleAuditor`), and the grants go in
`pkg/permission/builtin_role_grants.go`:

| Role | Granted |
|---|---|
| Admin (`registerAdminGrants`) | `vlog:create`, `vlog:expire`, `vlog:list`, `vlog:append`, `vlog:get`, `vlog:proof`, `vlog:head` — Admin creates logs and expires entry bodies on the logs that allow it. **Never `vlog:sign_head`.** |
| Operator (`registerOperatorGrants`) | `vlog:sign_head`, beside `keys:sign` in the same grant list, and `vlog:list`, `vlog:append`, `vlog:get`, `vlog:proof`, `vlog:head` |
| User (`registerUserGrants`) | `vlog:list`, `vlog:append`, `vlog:get`, `vlog:proof`, `vlog:head` |
| Auditor (`registerAuditorGrants`) | `vlog:list`, `vlog:get`, `vlog:proof`, `vlog:head` — read-only, as the role is |

**Signing a head is a use of the owner's key, so it follows `keys:sign`:** an
Operator permission, never an Admin one (Company Agent review, 2026-10-15).
`vlog:sign_head` is declared in `DutyCryptoUse` by
`registerCryptoUseDuties` in `pkg/permission/builtin_duties.go`, beside
`keys:sign`, so the separation-of-duties guard keeps it off the Security
Officer as well. Creating a log and expiring its bodies are configuration of
the tenant's data, which is Admin's. A cross-tenant request is refused by
`tenant.Confine` before the store is reached, as for audit.

## Open questions

The review decided the other seven ([§9](#9-choices-decided-in-review)). One
remains, the half of the checkpoint question the review left to agree with
go-xkms.

1. **The signed-note signature type for a non-Ed25519 owner key** — ECDSA
   P-256 at least, the CA key go-xkms's transparency log signs with.
   *To agree with go-xkms and record in both pages* (Company Agent review,
   2026-10-15).
   *Context:* the checkpoint is a C2SP signed-note (§9, decision 2), and
   `golang.org/x/mod/sumdb/note` defines only the Ed25519 type. Because the seam
   leaves the signature type and its encoding to the signer (§6), go-qrdb's
   storage, apply and service do not change with this choice; the SDK's
   verification helper and every client verifier do. The candidate forms:
   - (a) **The signed-note signature type for RFC 6962 tree-head signatures**
     (identifier per c2sp.org/signed-note, to confirm): the CA key signs an
     RFC 6962 `TreeHeadSignature` over the checkpoint's size and root with a
     timestamp the signer sets, carried as a note signature line. *Standard
     verifier support:* stock `golang.org/x/mod/sumdb/note` verifiers do not
     accept it; a verifier must implement the type. *CT tooling:* the signed
     bytes are the ones a CT log's signed tree head carries, so tooling built
     for RFC 6962 logs can check go-xkms's transparency log heads. *Custom
     verifiers:* only for the note wrapping, not for the signature itself.
   - (b) **A plain ECDSA signature over the note text,** if c2sp.org/signed-note
     registers such a type (its existence and identifier to confirm). *Standard
     verifier support:* only where a note library implements it. *CT tooling:*
     none, the signed bytes are not a tree-head structure. *Custom verifiers:*
     needed by most consumers. Simplest for the signer; if the spec registers no
     such type it would be a private type, which the format decision rules out.
   - (c) **Co-signing:** every checkpoint carries an Ed25519 signature line from
     a log key, which stock note verifiers and C2SP witnesses accept, and the CA
     key's signature line in form (a). *Standard verifier support:* full, on the
     Ed25519 line. *CT tooling:* on the CA line. *Custom verifiers:* none needed
     for the Ed25519 line. The cost is a second key per log, whose custody is
     go-xkms's to decide, two signatures per head, and the question of which key
     a consumer must trust, which is go-xkms's policy.
   - *Recommendation: (a)* for go-xkms's CA key: its transparency log is
     CT-shaped and its consumers have CT tooling, and one key stays the one trust
     root the rulings name. (c) remains additive without a change in go-qrdb,
     since a signed note carries any number of signature lines. A tenant's own
     log keeps the software provider's Ed25519 type.

## Test plan

- **Unit.** In `pkg/vlog`: determinism across replicas for the same committed
  entries packed into one `Update` call versus many (the test the audit chain
  already has, over the tree); restore on open and after a failed apply; proofs
  verified by the library's own verifier at every size up to a few thousand
  entries; checkpoint refusal for a wrong root or a size past the head;
  `ExpireBodies` leaving every proof valid; name validation. In both state
  machine families: dispatch, refusal versus halt classification, and the read
  requests. In `pkg/service`: confinement, the gate refusal and the
  `OpAuditAppend` → `OpVerifiableLog` switch. In `pkg/permission`: the grant
  table above, row by row — `vlog:sign_head` refused for an Admin and the
  Security Officer and granted to an Operator, `vlog:create` and `vlog:expire`
  granted to an Admin. The per-state-machine chain (Order, step 1): two groups
  from one create function keep separate heads across open and recovery. The seam: the software provider's
  sealed key round trip and a fault-injecting fake provider.
- **The mixed-version gate test.** A three-replica group with one replica on a
  binary that does not advertise `verifiable-log`: create and append are refused
  before proposal, audit events keep flowing as `OpAuditAppend`, and after the
  replica upgrades the gate opens and every replica's table is byte-identical.
- **Snapshot and restore.** A replica that recovers from a snapshot taken
  mid-log serves the same head, proofs and checkpoints as its peers, and its
  next append produces the same nodes; a tenant backup and restore preserves
  every log.
- **The upgrade.** A chain written by `OpAuditAppend`, then extended through the
  audit instance: `Verify` still verifies, and leaf 0 carries the pre-upgrade
  `PrevHash`.
- **Integration, per feature.** A new `INTTEST_verifiable_log` entry in
  `test/integration/features.mk` (and the feature folder the coverage gate
  requires), for `make integration-test-verifiable-log`; the existing
  `make integration-test-audit` re-run for the audit instance.
- **CLI end to end on every transport.** Each `qrdb vlog` command, table-driven
  over REST, gRPC, QUIC, MCP, Unix socket and embedded, asserting the output —
  entries, proof hashes, a checkpoint that `--verify` accepts — in standalone and
  on a three-node Raft cluster, including a tenant admin creating a log and a
  second tenant refused.

## What this backs

**The SPEC row** (`docs/SPEC.md`, Remaining work):

> | verifiable-log | planned | A database type created per tenant like key-value, time-series and vector: append-only (no update, no delete), sequence-numbered entries, a Merkle tree over them, inclusion proofs and consistency proofs between tree sizes, checkpoints, and tree heads signed by the log owner's own key (the CA key for go-xkms's transparency log, a tenant key for a tenant's own log); served on every transport and in the SDK with the tenant's isolation, DEK, barrier and replication; no PKI semantics. Engine surface: create, append, get, proof-inclusion, proof-consistency, head, and a sign-head hook. It generalises `pkg/audit.Chain` with no new state machine, in standalone and Raft modes: one apply-time opcode behind a feature gate for mixed-version groups, dispatched by both state-machine families, the sequence assigned at apply; a head's signature is produced outside apply by the owner's key and appended as a record; the audit trail becomes one log instance. The provider side of the seam with go-xkms's transparency log. | [databases/](databases/) |

**The seam rows on the other side**, named here and written in those projects'
own SPECs:

- **go-xkms:** the transparency log as a CA service over a go-qrdb verifiable
  log, and the `HeadSignerProvider` implementation over its KMS and hardware
  keys and its CA key.
- **go-quicbaas:** appending its own records to the transparency log through
  go-xkms, composing go-xkms's provider.
