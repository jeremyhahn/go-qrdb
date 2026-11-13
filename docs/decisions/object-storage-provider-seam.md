# What go-qrdb's side of the object-storage provider seam still lacks

> **Status: OPEN — questions for the owner** · Scope: go-qrdb
> Last verified: 2026-11-12
> SPEC row `object-storage-provider-seam` (planned). This record lists what
> go-qrdb's storage API offers the consumer today and lays each missing piece
> out as a question with options and a recommendation. Nothing here is built.

## Problem

Owner ruling 228(2) and 228(3) split the object-storage provider three ways:
go-quicbaas is the consumer and configures the provider per go-objstore
backend; go-objstore supplies the backends; go-qrdb's storage API is the seam,
and go-qrdb designs no backend of its own. Projects above go-quicbaas reach the
provider only through the go-quicbaas SDK, and go-qrdb never imports
go-quicbaas.

The seam today is the `objstore` engine
([../storage-engines/architecture.md](../storage-engines/architecture.md)):
`objstore.New(provider, settings, opts...)` in `pkg/storage/objstore` opens a
`storage.Storage` on one go-objstore backend. The node builds it in
`lifecycle.NewLocalStorage` (`pkg/server/lifecycle/local.go`) from the node's
one `storage.objstore` configuration section. So the seam serves a node
operator who picks one backend in YAML; it does not yet serve a consumer that
configures a provider per backend.

Every option below builds against the lower releases already in `go.mod`
(go-objstore v0.9.1-alpha, go-enclave v0.11.1-alpha). No option needs an API a
lower project lacks; where an option rests on a go-objstore API, the API is
named.

The credential is out of scope: it is row `object-storage-credential-handle`
(PR #1404, held for the owner). This record names where a question touches it
and decides nothing about it.

## Gap table

What the consumer needs to configure a provider per backend, and whether
go-qrdb's storage API offers it today.

| # | Operation the consumer needs | Offered today? | Where (file, symbol) |
|---|---|---|---|
| 1 | Name the go-objstore backend to open | Yes | `pkg/storage/objstore/objstore.go` `New(provider, …)`; `register.go` `resolveProvider` |
| 2 | Pass the backend's non-secret settings (bucket, region, endpoint, path) | Yes, per call | `New(…, settings map[string]string, …)`; the node fills it in `lifecycle.resolveObjstoreSettings` |
| 3 | Refuse a credential carried in settings | Yes | `pkg/storage/objstore/credentials.go` `RejectCredentialSettings` (go-objstore `common.RejectCredentialSettings`) |
| 4 | Hand the backend its credential | Files only | `credentials.go` `WithCredentialFiles`, `CredentialFiles`; a handle-level route is row `object-storage-credential-handle` (#1404), not this record |
| 5 | Register a backend go-objstore does not ship | Yes, in process | `register.go` `RegisterProvider(name, ProviderCredentials, objfactory.StorageCreator)` |
| 6 | Ask which providers this binary can open | **No** | go-objstore has `factory.ListStorageBackends`; go-qrdb's own registry (`registeredProviders`) is private |
| 7 | Read back which provider an open engine runs on | Yes | `objstore.go` `(*Engine).Provider` |
| 8 | Reach the seam from the consumer's process | In process only | importing `pkg/storage/objstore`; no wire operation carries a provider choice (Q1) |
| 9 | Choose a provider per database or tenant, several backends on one node | **No** | one `storage.objstore` section per node (`lifecycle.NewLocalStorage`); `pkg/volume` providers are path-shaped (`Volume.Root`) (Q2) |
| 10 | Spread a store across several backends (a pool) | **No** | go-objstore `pool.Manager` is not used (Q4) |
| 11 | Open an archive-only backend (Glacier, Azure Archive) | **No**, refused | `RegisterProvider` refuses archiver names; go-objstore `factory.NewArchiver` (Q5) |
| 12 | Create or delete the bucket a backend writes to | **No** | go-objstore `factory.NewBucketManager` is not used (Q6) |
| 13 | Change settings or credential on an open engine | **No** | settings are fixed at `New` (Q7) |

## Questions

### Q1. How does go-quicbaas reach the seam?

- **A. In process.** go-quicbaas imports `pkg/storage/objstore` and calls
  `New` and `RegisterProvider`, as go-xkms already imports `pkg/storage`.
  Dependency: none.
- **B. Over the wire.** A go-qrdb admin operation (and SDK call) carries the
  provider name, settings and a credential reference to a running node.
  Dependency: none from a lower project; needs a wire credential route, which
  is row `object-storage-credential-handle`'s open question.
- **C. Both.** A first, B later.

**Recommendation: A.** It is what exists, it is the pattern go-xkms already
follows (so `pkg/storage/objstore` becomes a cross-repo contract like
`storage.Storage`), and it puts no credential on any wire. B cannot be decided
before #1404 is ruled.

### Q2. Provider per database instead of per node

- **A. Keep one provider per node.** go-quicbaas runs one node set per
  backend. Dependency: none.
- **B. Object-storage volume provider.** A `pkg/volume` provider named
  `objstore` whose pool record carries the go-objstore provider name, the
  non-secret settings and credential file references; a database placed on
  that pool gets an objstore engine. `volume.Volume` is a directory today
  (`Root()`), so this needs a non-path variant (a volume that yields a
  `storage.Storage` instead of a root). Dependency: go-objstore v0.9.1-alpha
  `factory.NewStorage` (already used).
- **C. Per-database storage option.** Database create takes the provider and
  settings directly. Dependency: go-objstore v0.9.1-alpha
  `factory.NewStorage`.

**Recommendation: B.** "Where a tenant's store lives" is already the volume
pool's job, and a pool registration is already a privileged operation, which
is the right gate for naming a backend and its credential files. C puts
backend settings into every database record and spreads the credential
reference across the catalog. A does not meet "configures the provider per
backend" on a shared node.

### Q3. Provider discovery

- **A. `objstore.Providers()`** returns the providers this binary can open:
  the go-objstore backends compiled in under build tags plus the names
  registered through `RegisterProvider`. Dependency: go-objstore v0.9.1-alpha
  `factory.ListStorageBackends`.
- **B. None in go-qrdb.** The consumer calls go-objstore's
  `factory.ListStorageBackends` itself. Dependency: same API, called by the
  consumer.

**Recommendation: A.** B misses every provider registered through
`RegisterProvider`, which lives only in go-qrdb's registry, and couples the
consumer to go-objstore directly when the ruling puts go-qrdb's API between
them.

### Q4. Pools of backends

- **A. No pool at the seam.** One engine, one backend; placement across
  backends is the consumer's. Dependency: none.
- **B. Engine over a go-objstore pool.** `New` accepts a `pool.Manager` and
  pool name and opens on the candidate `Pick` returns. Dependency:
  go-objstore v0.9.1-alpha `pool.Manager`, `pool.Strategy`.
- **C. go-qrdb volume pool maps to a go-objstore pool** (on top of Q2 B).
  Dependency: go-objstore v0.9.1-alpha `pool.Manager`.

**Recommendation: A.** An engine's data must stay on one backend for its
life; a `Pick` per open can place the same database on another candidate and
lose it unless a hint pins it, and pinning makes the pool a one-entry map.
Placement across backends belongs to the consumer that owns the accounts.

### Q5. Archive-only backends

- **A. Refuse them** (today): an engine needs read-write, low-latency objects.
  Dependency: none.
- **B. A separate archiver seam** for exports and snapshots, through
  go-objstore `factory.NewArchiver`. Dependency: go-objstore v0.9.1-alpha
  `factory.NewArchiver`, `common.Archiver`.

**Recommendation: A for this row.** An archive tier is a backup destination,
not a store; if the owner wants it, it is its own row, not part of the
provider seam.

### Q6. Bucket lifecycle

- **A. The consumer provisions the bucket** out of band, with its own account,
  before it hands go-qrdb the settings. Dependency: none.
- **B. The seam creates and deletes buckets** through go-objstore
  `factory.NewBucketManager`. Dependency: go-objstore v0.9.1-alpha
  `factory.NewBucketManager`, `common.BucketManager`.

**Recommendation: A.** Ruling 228(3): go-qrdb designs no backend, and bucket
creation is account administration at the provider, which the consumer owns.
B also needs a credential with bucket-admin rights inside go-qrdb.

### Q7. Changing settings or the credential on an open engine

- **A. No live change.** The consumer closes the engine and opens a new one
  with `New` (the database reopens). Dependency: none.
- **B. `(*Engine).Reconfigure(settings, …)`** swaps the go-objstore storage
  under a lock. Dependency: go-objstore v0.9.1-alpha `factory.NewStorage`.

**Recommendation: A.** go-objstore backends keep no credential handle
(`common.Credentials`), so a rotation builds a new backend either way; B adds
a concurrency surface on every read and write for a rare operation. How a
rotated credential reaches go-qrdb is row `object-storage-credential-handle`'s
question.

## Recommendation summary

| Q | Recommendation | Lower-project dependency |
|---|---|---|
| Q1 reach | A, in process | none |
| Q2 per database | B, object-storage volume provider | go-objstore v0.9.1-alpha `factory.NewStorage` |
| Q3 discovery | A, `objstore.Providers()` | go-objstore v0.9.1-alpha `factory.ListStorageBackends` |
| Q4 pools | A, none at the seam | none |
| Q5 archive | A, refuse | none |
| Q6 buckets | A, consumer provisions | none |
| Q7 live change | A, reopen | none |
