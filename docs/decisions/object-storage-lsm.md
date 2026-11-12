# The object-storage LSM segment layout: the open owner decisions

> **Status: PROPOSED — pending the owner's ruling** · Scope: go-qrdb
> Last verified: 2026-11-12
> SPEC row: `object-storage-lsm` (future). Design:
> [../storage-engines/architecture.md](../storage-engines/architecture.md),
> "Target design: object-storage LSM segment layout". Each question below has
> its options, a recommendation and the reason, and the lower-project API each
> option needs. Nothing here is built.

## Problem

`pkg/storage/objstore` stores one object per key under an encoded name. It is
correct for every key, but its write and listing cost scale with key count, a
batch needs a journal object to be atomic, it sits outside the barrier (a
standalone store starts with a WARN and an `unencryptedAtRest` capability
entry), and it is refused as a per-database backend and as a Raft hot apply
path.

The design proposes an LSM segment layout instead: compose Pebble v2's remote
storage (`objstorage/remote.Storage`, `pebble.Options.Experimental`) over
go-objstore, so that an `objstore` database is the `pebble` engine whose
SSTables live in a bucket. `pkg/storage/objstore` becomes a `remote.Storage`
adapter (plus an encrypting wrapper) over go-objstore `common.Storage`; the
flat engine and its batch journal are deleted. Object names are Pebble's own
(`<hash>-<creator-id>-<file-num>.sst`), never derived from qrdb keys.

Adopting the design is decision OS-0; OS-1 to OS-7 follow from adopting it.

### Dependencies

The row builds only against the tagged releases already in `go.mod`:
go-objstore v0.9.1-alpha, go-quicraft v0.24.3-alpha, go-enclave v0.11.1-alpha,
go-codec v1.5.1-alpha, and `github.com/cockroachdb/pebble/v2` (an external
dependency qrdb already pins for the `pebble` engine). The go-objstore v0.9.1-alpha
surface the options use is `common.Storage`: `PutWithContext` (for
`CreateObject`), `GetWithContext` returning a whole-object `io.ReadCloser`
(for `ReadObject`), `ListWithContext(ctx, prefix)` (for `List`; it takes no
delimiter, so the adapter folds delimited names itself), `DeleteWithContext`
(for `Delete`), `GetMetadata` whose `Metadata.Size` serves `Size`, and
`Exists`; plus `common.Encrypter` and `common.NewEncryptedStorage` (OS-4 A).
It has **no byte-range read** (OS-6):
the one API this row would ask for, and it is filed on go-objstore first.
No question needs anything from go-quicraft, go-enclave or go-codec beyond
what qrdb already uses.

## Questions

### OS-0 — Adopt the LSM segment layout at all?

- **A: adopt it** — Pebble v2 remote storage over go-objstore; the flat engine
  and its journal are deleted when it ships (no side-by-side engines).
  Needs: go-objstore v0.9.1-alpha `common.Storage`; Pebble v2 (already pinned).
- **B: keep the one-object-per-key engine** under encoded names. Needs: none
  (shipped). Cost per key, journal per batch and the barrier gap stay.
- **C: write an in-house LSM.** Needs: go-objstore v0.9.1-alpha `common.Storage`;
  a new compaction, manifest and WAL implementation in qrdb.

**Recommendation: A.** It removes every listed cost of the shipped engine by
construction, reuses an engine qrdb already runs and tests, and keeps
go-objstore the only backend source. B leaves the problem as it is; C is the
last resort and nothing forces it.

### OS-1 — Build on Pebble's `Experimental` remote-storage surface?

- **A: proceed, pinned** — use `Options.Experimental.RemoteStorage`,
  `CreateOnShared`, `CreateOnSharedLocator`, a persisted `CreatorID`; pin the
  exact `pebble/v2` version and review any bump touching `objstorage/remote`.
  Needs: Pebble v2 (pinned); go-objstore none beyond `common.Storage`.
- **B: wait for Pebble to stabilise the surface.** Needs: an upstream Pebble
  release with a stable API; no date exists, so the row waits indefinitely.
- **C: vendor/fork the remote-storage code.** Needs: a Pebble fork qrdb
  maintains.

**Recommendation: A.** "Experimental" is a Go-API-shape caveat, not an
unsound mechanism; CockroachDB runs it in production. Exact pinning is qrdb's
practice already. Accepted consequence: a store opened with shared objects
bumps the format major version and cannot be read by an older Pebble.

### OS-2 — Where do the WAL and MANIFEST live?

- **A: local and durable, both modes** — in the same local data directory (or
  volume pool) the `pebble` engine uses; only SSTables go to the bucket.
  Needs: none (Pebble `WALDir`/`vfs.FS`).
- **B: local but ephemeral when Raft-replicated** — rely on the Raft log as the
  backstop for a hot apply path, small non-durable WAL. Needs: none; only
  meaningful if OS-7 B is ruled.
- **C: a remote WAL.** Needs: a Pebble feature that does not exist (WAL and
  MANIFEST never go through `remote.Storage`; `WALFailover` is local only).

**Recommendation: A.** A non-replicated binding has no log beneath it, so its
WAL must be durable on a local disk exactly like `backend: pebble`; one rule
for both modes is simpler and costs nothing. C is not available.

### OS-3 — Ownership: a prefix per Pebble instance, or shared across replicas?

- **A: one bucket prefix and one `CreatorID` per owning instance, never
  shared** — a replaced replica's objects are reclaimed as obsolete SSTables.
  Needs: go-objstore v0.9.1-alpha `common.Storage` (`ListWithContext` by
  prefix, `DeleteWithContext`).
- **B: shared SSTables across replicas** (CockroachDB-style disaggregated
  storage, followers read shared objects). Needs: changes to the go-quicraft
  apply model (replicas stop being independent appliers), which no
  go-quicraft release offers; out of scope for a storage engine.

**Recommendation: A.** It keeps qrdb's model (replication is the host's
property, every replica applies independently), keeps compaction ownership
inside one instance, and costs the same N copies a Raft group pays on local
disk today.

### OS-4 — Encryption at rest for remote SSTables

The barrier wraps `pebble.Options.FS` with `seal/encfs`; remote objects never
pass through `FS`, so today nothing encrypts them.

- **A: go-objstore's own `Encrypter`/`NewEncryptedStorage`.** Needs:
  go-objstore v0.9.1-alpha `common.Encrypter`, `common.NewEncryptedStorage`. A second crypto path with its own
  key material, outside the barrier's key custody.
- **B: provider server-side encryption only** (SSE-S3, SSE-KMS). Needs: none
  in go-objstore beyond provider settings; the local and memory providers
  have none, and the data stays outside the barrier.
- **C: an encrypting `remote.Storage` wrapper in `pkg/storage/objstore`**
  sealing each whole object with the same `seal.StorageBarrier` AEAD
  (AES-256-GCM) and per-tenant DEK `encfs` uses, before `Put`, after `Get`.
  Needs: go-objstore v0.9.1-alpha `common.Storage` only; qrdb's own barrier.

**Recommendation: C.** One cipher and one key hierarchy, the barrier's; it
works on every provider and closes the `unencryptedAtRest` gap instead of
documenting it. Provider SSE (B) may be layered under it, never instead of it.
Note for OS-6: whole-object AEAD means a ranged read must either fetch the
whole object or the wrapper must seal in fixed-size chunks; C should seal in
chunks so a future ranged read can decrypt only the chunks it covers.

### OS-5 — The encoded-name layout and the stores written in it

- **A: refuse at open, no migration** — the LSM store carries its own format
  marker; opening a `qrdb-objstore-format` version `2` store returns the typed
  format error; data moves by logical copy or backup/restore. Needs: none.
- **B: in-place migration at open** — read every key from the old layout and
  write it into the LSM. Needs: go-objstore v0.9.1-alpha `common.Storage`;
  the flat engine's decoder kept alive in the new binary.
- **C: keep both layouts readable** side by side. Needs: none; keeps the flat
  engine.

**Recommendation: A.** The shipped engine already refuses a foreign layout
rather than misread it; B and C keep the deleted engine as a compatibility
surface, which the project does not do, and backup/restore already moves data
between formats.

### OS-6 — go-objstore has no ranged read

Pebble's `remote.ObjectReader.ReadAt` wants concurrent offset reads;
go-objstore v0.9.1-alpha `common.Storage.Get`/`GetWithContext` return the
whole object only.

- **A: ask go-objstore for a ranged read on `common.Storage`** (e.g.
  `GetRange(ctx, key, offset, length)`, served by HTTP Range on s3/gcs/azure
  and `ReadAt` on local), filed on go-objstore first; until a tagged release
  has it, `ReadObject` fetches each object once per open, caches it locally by
  object name and serves `ReadAt` from the cache. Needs: go-objstore
  v0.9.1-alpha `Get` now; the ranged read from a later go-objstore tag.
- **B: whole-object fetch with a local cache, permanently.** Needs:
  go-objstore v0.9.1-alpha `Get`. Cold reads of large SSTables stay slow and
  the local cache grows with the working set.
- **C: qrdb calls provider SDKs directly for ranged reads.** Needs: provider
  SDKs in qrdb; breaks "go-objstore is the only source of backends".

**Recommendation: A.** The provider interface is go-objstore's to own; the
interim cache is a performance caveat, not a correctness one, and Pebble's
`SecondaryCacheSizeBytes` absorbs repeated block reads. Missing API, to file
on go-objstore: a byte-range read on `common.Storage` (not in v0.9.1-alpha).

### OS-7 — Lifting "objstore is refused as a hot apply path"

- **A: lift only the per-database `backend: objstore` refusal (phase 2); keep
  the Raft `sm_storage`/`state_storage` refusal until a scoped phase 3**,
  acceptance-tested under compaction and leader-failover load and proposed
  behind the cluster feature gate. Needs: none beyond OS-0 A.
- **B: lift both at once.** Needs: none in a lower project, but every replica
  then writes to a bucket under load with no tested failover story.
- **C: keep both refusals.** Needs: none; the LSM then serves only standalone
  nodes.

**Recommendation: A.** With Pebble's WAL and atomic batch the per-database
refusal has nothing left to protect; the Raft path changes what a replica
durably writes where, so it gets its own scoped phase and the feature gate
like every Raft-visible change.

## Summary of recommendations

| Question | Recommendation | Lower-project dependency |
|---|---|---|
| OS-0 adopt | A: adopt, delete the flat engine | go-objstore v0.9.1-alpha `common.Storage` |
| OS-1 Experimental surface | A: proceed, exact pin | none (Pebble v2 pinned) |
| OS-2 WAL placement | A: local, durable, both modes | none |
| OS-3 ownership | A: own prefix + `CreatorID` per instance | go-objstore v0.9.1-alpha `ListWithContext`/`DeleteWithContext` |
| OS-4 encryption | C: barrier-AEAD `remote.Storage` wrapper, chunked | go-objstore v0.9.1-alpha `common.Storage` |
| OS-5 old layout | A: refuse at open, no migration | none |
| OS-6 ranged read | A: request on go-objstore, cache meanwhile | ranged read missing from go-objstore v0.9.1-alpha |
| OS-7 hot apply path | A: per-database now, Raft in a gated phase 3 | none |
