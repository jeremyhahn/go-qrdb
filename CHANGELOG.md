# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html).

Pre-1.0, MINOR is where breaking changes live; PATCH is reserved for changes
that do not move the API. Releases before `0.2.0-alpha` predate this file and
are not reconstructed here — their history is in the git log.

## [Unreleased]

### Fixed

- **CI's devcontainer images publish under the release credential's own registry namespace, and a push to `main` on the forge publishes again**: `docker-build` runs on the release runner on a push to `main` and pushes `qrdb-ci-toolchain` and `qrdb-ci-it` under the namespace of the credential that pushes them, which it can write, and every consumer job there pulls them anonymously, because that namespace is public and the run's token cannot log in to the forge's registry; github.com still publishes to GHCR under the repository owner and pulls with the run's token.
- **A tenant Security Officer's PIN is held sealed while it is being onboarded**: `bootstrap.TenantSOConfig.PIN` is a `*secure.Secret`. The onboarding service seals the request's PIN bytes into it and wipes them. The Secret is opened only for the `SetSOPIN` commit, and `ProvisionTenant` destroys it on every return, whether the PIN was committed, the commit failed or the request was refused. The secure analyzer no longer flags the two `[]byte` PIN fields.
- **CI's devcontainer images build on `bigdisk` again and the forge publishes nothing**: a push to `main` on the forge no longer pushes them to a registry namespace the release runner's credential cannot write; every job there builds or reuses the content-hash image on its own runner, and github.com still publishes to GHCR.
- **A certificate a node issues or enrolls is admitted on its next
  handshake** (#1128). A tenant's first certificate brings its login CA into
  use. Until now the TLS client trust learned of that CA only through an
  asynchronous rebuild, so a login that dialed the moment the certificate came
  back could be refused. `SignCertificate` and `EnrollCertificate` now bring
  the node's client trust up to date before answering
  (`clienttrust.Pool.Settle`); this reads no storage when no change is pending.

- **The rolling enable-encryption coordinator-restart test no longer requires
  the restarted coordinator to make the hand-on itself** (#1167).
  `TestClusterEnableEncryption_Rolling_RF3_CoordinatorRestart` failed when the
  restarted coordinator lost leadership of the system shard before the second
  turn: the node elected then handed the turn on from the record, as
  `Coordinator.Advance` is designed to. The test now looks for the hand-on in
  every node's log since the restart, and accepts another node's only when the
  restarted node's log shows it stepped down. Two `pkg/clustermig` unit tests
  pin the condition: a stepped-down coordinator writes and logs nothing, and
  the new leader, or the coordinator leading again, hands the turn on.

- **`qrdb tenant create` accepts one file for `--admin-csr` and `--so-csr`**
  (#1042): the CSR reads were keyed by path, so the second flag left the
  admin's CSR empty.

- **Tenant creation with an SO PIN goes only over TLS** (#1042): the Go SDK
  sent `CreateTenantRequest.SOPin` on every transport without the SO PIN's
  guard, so plaintext gRPC, a Unix socket without TLS or an `http://` REST or MCP
  address carried it in the clear, and a redirect could resend it.
  `CreateTenantRequest` is now a `BarrierSecretCarrier` when `SOPin` is set:
  REST, MCP, gRPC and Unix refuse it with `SOPINRequiresTLSError` before
  anything is sent, and REST and MCP refuse a redirect to `http://` or to
  another host. QUIC is always TLS 1.3 and the embedded transport sends
  nothing over a wire. A request without a PIN is sent as before.

- **`TestNewClusteredNode_ServesALinearizableReadOnReturn` no longer fails
  a ready cluster on a loaded host.** It gave its one ReadIndex a 1-second
  deadline. go-quicraft answers nothing for a forwarded ReadIndex lost in
  transit, so on a busy runner the read timed out on a replica that was ready
  and serving: main's `make test-sdk` went red with `quicraft: timeout`.
  `NewClusteredNode` was not at fault: it returns only after
  `server.WaitForShardServing` has served a ReadIndex on that same host. The
  test now checks at the moment each founder returns: the replica must
  already know a leader, and its first read must not be refused. A read
  attempt that runs out its own `server.StartupReadAttemptTimeout` is the one
  outcome retried, up to three times. A constructor that skips the wait still
  fails on the first attempt.


- **Every `make coverage-<pkg>` target now compares its figure with
  `COVER_MIN`.** The check compared with `bc`, which the CI toolchain image
  does not have, so the comparison produced nothing and the target passed at
  any coverage, 0% included. It now compares with `awk`, and a profile with no
  total (missing or unreadable) fails. `test/buildgates/check_coverage_test.go`
  drives the check through `make` over profiles above and below the floor.
## [0.58.0-alpha] - 2026-10-27

### Added

- **The separation-of-duties posture is readable and its changes are
  audited** (#1128). Embedders read the posture a node resolved with
  `node.Node.StrictSeparation()`, and the resolver as
  `sdk.ResolveStrictSeparation`. At daemon start, and in `Node.FirstBoot` /
  `Node.FirstBootWithCeremony`, the resolved posture is compared with the
  cluster record `__sys:cluster/separation_of_duties`. On a first recording or
  a change, the record is written first; the node whose conditional write
  wins then emits one `posture-change` audit event (`previous`, `posture`,
  `fips_forced`). A record that cannot be read or written refuses the start.

- **Tenant create takes an optional Security Officer** (#1042): `so_username`,
  `so_pin` and exactly one of `so_csr_pem` / `so_cert_pem` (with
  `so_chain_pem`), on REST, gRPC, QUIC, MCP, Unix and embedded, and as
  `qrdb tenant create --so-username --so-csr|--so-cert --so-chain --so-pin`.
  The SO is enrolled with the tenant's `so` role alone and its PIN set through
  the SO authenticator; the response carries its credential as `so`, and
  `--out` writes `so-cert.pem`. A partial set or a CSR with a certificate is
  `InvalidArgument` and creates nothing; so, under strict separation of
  duties, is an SO matching the admin by username or key, while outside it an
  SO matching the admin by both is the admin, granted `so` beside `admin`, and
  a half match is refused. An isolated tenant records the SO as
  `__sys:pending-so` and creates its account right after the admin's. `sdk/go/node` now attaches the server's
  SO authenticator to tenant onboarding.

- **A conformance test holds Get-after-Delete to typed not-found on every
  engine and transport** (#1163). `TestDeleteThenGetIsTypedNotFound` in
  `sdk/go/conformance` runs Put, Delete, then Get and Exists for 50 rounds on
  every cell of the guarded-verb matrix plus a standalone objstore cell per
  transport, with short keys, keys objstore stores under a hashed name, and one
  reused key. go-qrdb's read plane already answered not-found in every cell;
  the read failure #1163 reported is not raised by go-qrdb or go-objstore.
### Added
- **`qrdb kv put --revision`** prints the written key's version and revisions (the metadata of `kv get --revision`), through `MVCC().PutWithLease` with lease 0. The key-value user guide names the three calls that report a key's version — that put, `GetRevision` and a batch of get ops — which report one version, the one a transaction's `version` compare reads (#1211).
### Added
- **A vector namespace's index parameters and metadata can change in place**
  (#1290). `VectorClient.UpdateNamespace` (REST
  `POST /vector/namespace/update`, gRPC `VectorUpdateNamespace`, QUIC
  `vector.UpdateNamespace`, MCP `qrdb_vector_update_namespace`, the Unix
  socket and the embedded client) and the CLI's
  `qrdb vector namespace update` merge each given key over the stored value,
  keep every vector, and report `requires_rebuild` when an index parameter
  changed; the index is rebuilt by `rebuild-index`, not by the update. The
  dimension cannot change: a different `--dimension` is a
  `DimensionMismatchError`, and a missing namespace a
  `NamespaceNotFoundError`, on every transport. The verb needs the new
  `vector:namespace_update` permission.
- **A non-replicated database is held by the node that created it, and every
  other node forwards to it** (#893, OD-2). The catalog record carries
  `owner_node`, the creating node's Raft address, for a memory, file, pebble or
  objstore database. Other nodes open no store for it. They list it as
  `remote`, and REST, gRPC, the Unix socket, QUIC and MCP forward its requests
  to the owner. An owner that does not serve gives
  `database.BindingOwnerUnavailableError` (Unavailable), which names it. The
  PostgreSQL and MySQL wire protocols do not forward and return the refusal.
  The catalog is the one record of databases: a node reads no `meta.json`, and
  one whose data directory still holds such a record refuses to start with
  `database.UnmigratedDatabasesError`, naming every file, rather than starting
  with those databases unlisted. A create releases an entry this node holds
  for a database another node dropped before refusing the name as a duplicate.

### Security
- **A grantor may grant only permissions it holds** (#1278). `CreateRole` and
  `UpdateRole` are refused when the role would carry a permission the caller
  does not hold in the role's tenant, and assigning a custom role is refused
  unless the assigner holds all of it. A built-in role is assigned to another
  account as before, so the tenant Admin still gives the built-in User role to
  the accounts that sign, but no principal may bind to itself a role carrying a
  permission it lacks. A tenant Admin could previously mint, widen or bind a
  role with `keys:sign` and gain the key custody the Admin/User duty split
  withholds from it. The refusal is `*role.GrantExceedsHolderError`, a
  permission denial on every transport. Breaking: a tenant Admin no longer
  creates roles carrying data-plane or key-custody permissions, and no longer
  assigns itself its tenant's SO or User role. So that a role carrying the
  database lifecycle can still be written in a tenant, the built-in Admin
  gains, in its own tenant only, `databases:create`, `databases:read`,
  `databases:list`, `databases:update`, `databases:delete`,
  `databases:create_raft`, `databases:create_volume`, `databases:scale` and
  `databases:barrier_status`, `databases:seal` and `databases:unseal`, and
  beside the tenant barrier's init, seal, unseal and rekey it already held,
  `tenant_barrier:enable_encryption`; a database's key custody
  (`databases:rekey`, `databases:initialize_shamir`, `databases:officer_*`)
  stays out of every built-in role. A role mixing Admin
  and Operator permissions is written by a second account the Admin gives
  both built-in roles. An Admin that gives User or Admin to a second account
  it controls acts through it with that role's permissions.
- **mcp-secret-argument-types** (#1129, #1130): an MCP secret, PIN or
  credential argument sent as anything but a JSON string (an array, object,
  number, boolean or `null`) is refused with `ToolArgumentError` naming it,
  before either its text or its `<name>_b64` form is decoded. `mcp.ParseString`
  had coerced such a value, reading an array or object as an absent secret, so
  `so_pin_b64` and `user_pin_b64` sent as arrays let `qrdb_barrier_initialize`
  go ahead with neither PIN provisioned. The same check covers the discovery
  shared secrets and token, the credential values, the user auth-factor
  credentials, the tenant-barrier unseal key and the invitation token
  (`docs/barrier-seal/api.md`, "Secret arguments over MCP").

### Changed
- **Dependencies**: mcp-go v1.4.0, in the root module and `sdk/go`, so
  `make deps-check` passes on `main` and every pull request.

### Fixed
- **Auth**: an authenticator that fails because its shard is electing a leader (`service.LeadershipTransient`) answers 503 / `UNAVAILABLE` on every transport, not 401; `sdk/go` re-exports `StoreUnavailableError` and `IsLeadershipTransient` so a consumer can classify it.

- **Dependencies**: go.etcd.io/etcd `api/v3`, `client/v3` and `client/pkg/v3`
  v3.7.3, in the root module, `sdk/go`, `sdk/go/gorm/conformance`,
  `test/clusterproof` and `test/performance/etcd-benchmark`, so
  `make deps-check` passes on `main` and every pull request.
## [0.58.0-alpha] - 2026-10-27

### Added
- **Signed tenant barrier init** (#1212): a two-phase init for a first admin whose key signs away from the node. `POST /v1/tenants/{id}/barrier/signed-init/begin` (gRPC `TenantBarrierService.SignedInitBegin`, QUIC `tenant.BarrierSignedInitBegin`, MCP `qrdb_tenant_barrier_signed_init_begin`) takes the signer's SubjectPublicKeyInfo and returns the epoch-1 init statement to sign and a single-use reference valid for five minutes; `…/signed-init/complete` (`SignedInitComplete`, `tenant.BarrierSignedInitComplete`, `qrdb_tenant_barrier_signed_init_complete`) redeems it with the detached signature. Every complete attempt spends its reference. The Go SDK carries `Tenants().TenantBarrierSignedInitBegin` / `TenantBarrierSignedInitComplete` on every transport, and `sdk/go` re-exports the service types and refusals as `ServiceTenantBarrierInitBeginRequest`, `ServiceTenantSignedInitError` and the rest. The CLI adds `qrdb tenant barrier-init-begin` (`--public-key`, `--statement-out`) and `qrdb tenant barrier-init-complete` (`--reference-hex`, `--sig-file`).
- `service.LeaseService.LogRenewals` counts the lease keep-alives sent
  through the raft log rather than answered by the lease primary from memory.
  `TestRaft_LeaseKeepAliveLatencyTail` now asserts that count stays at zero
  instead of bounding p90/median, which a loaded CI runner failed with nothing
  on the renewal path waiting (CI run 69207); the latencies are still logged
  for `test/performance/lease-keepalive.md`.
- **fips-140-3** (#914): a joiner configured with both a Noise key and a P-256
  key is now tested against the real runtime rather than the
  `forceNoiseAvailable` override. Under `GOFIPS140=latest GODEBUG=fips140=only`,
  `pkg/clusterenroll`'s `TestJoinCSR_DualKeyClientSelectsTheRuntimeSuite`
  selects `p256proto.SuiteP256AESGCM`, asserts the runtime refuses X25519 key
  generation and completes the CSR join, so the join never reaches X25519; it
  logs the observed `fips140` mode. `TestFetchAnchor_DualKeyBootstrapTracksTheRuntime`
  proves the anchor fetch hands go-truststrap no Noise key in that runtime.
- **fips-140-3** (#914): two tests that skipped under `fips140=only` now assert
  in both modes. `TestCheckPublicKeyStrength_RSA` (`pkg/clusterca`) builds its
  1024-bit RSA public key from a modulus, which the module does not refuse, and
  reaches the `*WeakKeyError` refusal; `TestParseSignerPEM`'s
  `x25519_cannot_sign` (`pkg/authfactors/mtls`) builds an RFC 8410 X25519
  PKCS#8 key from fixed bytes and asserts `*authfactors.EnrollmentInvalidError`
  where the module refuses to parse it and `*UnsupportedSignerError` elsewhere.
  Both leave the skip register in `docs/seal/fips140-mode.md`.
- **fips-140-3** (#914): `pkg/seal`'s `TestOpenWrapped_AuthenticationFailureIsKeyMismatch`,
  `TestOpenWrapped_CiphertextShorterThanOverheadIsKeyMismatch` and
  `TestOpenWrapped_RefusesPlaintextOutsideProtectedMemory` skipped whole under
  `fips140=only` although only their caller-nonce AEAD is refused there; each
  now runs once per nonce convention, so the module-nonce case runs in both
  modes and only the unreachable caller-nonce case skips.
- **fsync-stall-leadership** (#906): a shard leader whose followers sit in a
  WAL fsync stall keeps its leadership and term, so linearizable reads and
  admin requests are not failed by an election the stall caused; writes wait
  for a quorum's fsyncs as before. It is go-quicraft v0.24.2-alpha's
  heartbeat-under-fsync, on in every build with no knob, and the stall shows in
  `quicraft_logdb_fsync_latency_seconds`. `pkg/service`'s
  `TestFsyncStall_LeaderKeepsLeadershipThroughFollowerWALStall` proves it on
  three in-process hosts, waiting for each follower's own stalled-fsync
  observation before it reads the histogram; `sdk/go/testutil/raftharness` gains
  `WithHostOptions` and `StallFS`, the stalling WAL filesystem it uses.
- **Binary-key parity cells for every storage engine** (#1143):
  `pkg/storage/binary_key_parity_test.go` holds memory, pebble, file and both
  objstore providers to one answer for path-shaped keys (separators, `..`, a
  drive letter, control characters, an embedded NUL), a key long enough that
  objstore names it by hash, and binary keys through a batch, a rollback and a
  snapshot. Tests only; no key encoding or on-disk format changes.
- **A supported passphrase change for the system barrier** (#631).
  `BarrierChangePassphrase(current, next)` re-wraps the root key of an
  unsealed, passphrase-sealed barrier under a new passphrase; the root key and
  the data are unchanged, the next unseal takes the new passphrase and the old
  one is refused. It is on every transport — `POST /v1/barrier/change-passphrase`,
  gRPC and the Unix socket `BarrierChangePassphrase`, QUIC
  `barrier.ChangePassphrase` (the SO PIN in the request's new `so_pin`), MCP
  `qrdb_barrier_change_passphrase`, the embedded client — and in
  `qrdb barrier change-passphrase`, the TypeScript SDK's
  `seal.changePassphrase`, the desktop binding and `Node.ChangePassphrase` for
  embedders. It requires `seal:rekey` and the Security Officer PIN: every
  transport, embedded included, verifies the SO PIN before the barrier is
  reached, as for seal and rekey, and refuses the change outright on a node
  with no PIN backend; the SDK clients send the PIN bound by
  `transport.WithSOPIN`. Over REST the PIN goes both in the `X-QRDB-SO-PIN`
  header, which the authorization middleware reads, and in the body's
  `so_pin`, which the handler's own SO gate reads; the Go and TypeScript
  clients send both.
- **The objstore engine takes its provider credential by file reference.** `storage.objstore.credentials.{key_file,secret_file}` name owner-only files that `objstore.New` (`WithCredentialFiles`) reads into go-enclave handles and passes to go-objstore's `common.Credentials`; a credential inline in the YAML is refused at load, a reference the provider does not read (`key_file` on `azure`; either file on `gcs`, `local` or `memory`) is refused at open with `objstore.CredentialIgnoredError` before any file is read, and a credential file is opened without blocking so a FIFO is refused at once (#1143).
- **`objstore.RegisterProvider` adds a provider that declares its credential reads, and is safe for concurrent use.** The provider is kept in the storage seam's own synchronized registry, which `New` consults before go-objstore's factory; nothing is written into the factory's unsynchronized map, so concurrent registrations and opens cannot fault the process. A name go-objstore ships, as a storage provider or an archiver, is refused whether or not its build tag compiled it in. `New` refuses a credential inline in the settings with `objstore.CredentialSettingError` before it calls any provider, so a registered creator never receives one. `New` resolves the provider once and validates and opens with that one registration, refusing an unknown name with `objstore.UnsupportedProviderError` before any credential file is read, so a registration racing an open cannot bypass the credential check (#1143).
- **`sdk/go/storage` re-exports the objstore credential and provider-registration surface.** `WithObjStoreCredentialFiles`, `ObjStoreCredentialFiles`, `ObjStoreOption`, the `ObjStoreCredential*Error` types, `RejectObjStoreCredentialSettings` and `RegisterObjStoreProvider` alias their `pkg/storage/objstore` names, so an embedder opens `NewObjStoreStorage` with file credentials through the SDK boundary alone (#1215).
- **`sdk/go/server.SealedWindowRESTEntryMatches`** re-exports `sealedwindow.RESTEntryMatches`, the matcher the REST sealed-window gate applies to an allowlist entry, beside the `SealedWindowRESTAllowlist` it serves (#1223).
- **`sdk/go/server.NewMCPServer`** re-exports the combined MCP server constructor qrdbd's orchestrator calls (`pkg/api/mcp.NewServer`), with the `MCPServer` handle, `MCPServerOption`, `MCPTLSNotConfiguredError` and the TLS, listener, connection-bound, auth-middleware and sealed-window probe and dispatch options as `WithMCPServer*`, so an embedder builds, starts and stops the real MCP server from `sdk/go` alone (#1225).
- **`sdk.TenantBarrierInitVerifier`** is the server half of signed tenant barrier initialization, re-exported from `pkg/auth/middleware` beside the client's `transport.ProveTenantBarrierInit` (#1246). `Begin(tenantID, tlsState)` issues a nonce bound to the tenant and the connection's TLS exporter; `Complete(tenantID, tlsState, proof, key)` checks the proof against that nonce, that exporter and the key `key` names, and spends the nonce, refusing with `*sdk.FirstAdminProofError`. A facade that terminates its callers' TLS runs the exchange with it rather than a copy of qrdb's. `AuthMiddleware` now runs the same type for every transport. The aliases `NewTenantBarrierInitVerifier`, `TenantBarrierInitKey`, `TenantBarrierInitProof`, `InitChallenge`, `InitChallengeBook`, `NewInitChallengeBook`, `InitChallengeTTL`, `InitChallengeMaxOutstanding`, `InitProofReason*` and `InitTooManyChallengesError` come with it.

- **The verifiable-log API over gRPC, the Unix socket, REST, QUIC and MCP**
  (#912): the ten `Vlog*` gRPC methods, `/vlog/*` REST routes, `vlog.*` QUIC
  methods and `qrdb_vlog_*` MCP tools create and list logs, append, read
  entries, prove inclusion and consistency (RFC 9162), read and sign the head
  (a C2SP signed-note checkpoint), rotate the head-signing key and expire
  entry bodies, under the `vlog:*`
  permissions, in the caller's tenant and the request's database; no
  request names a tenant, on any transport. Every call naming a barrier-isolated
  tenant or a barrier-enabled database, sealed or not, is refused as
  `Unimplemented` (`BarrierTenantLogNotSupportedError`,
  `BarrierDatabaseLogNotSupportedError`): the log registry sits under the
  system barrier, not theirs, so `service.LogServiceConfig` requires the
  node's database registry as `Databases`. They are
  served while the node has a log service (`server.WithLogService`, wired by
  `qrdbd` and `sdk/go/node`). REST, QUIC and MCP answer in one JSON shape,
  `sdk/go/transport/vlogwire`'s, with byte fields in standard base64. Every `pkg/vlog` refusal
  carries its `pkg/api/errclass` class on every transport. Key rotation is
  gated on `vlog:sign_head`, so an Operator rotates and an Admin does not; a
  checkpoint signed before a rotation still verifies under the key it names
  ([docs/databases/api.md](docs/databases/api.md), "Verifiable logs").
- **Verifiable logs from the SDK and the CLI** (#912): `Client.Vlog()` returns
  a `transport.VlogClient` on every transport (embedded, gRPC, the Unix socket,
  REST, QUIC and MCP), with embedded serving the log service
  `embedded.WithLogService` supplies; appending to, signing or expiring a log
  that does not exist is the typed `vlog.LogNotFoundError` on every one. The
  `qrdb vlog` commands (`create`, `list`, `append`, `get`, `prove-inclusion`,
  `prove-consistency`, `head`, `sign-head`, `rotate-key`, `expire`) drive it
  over any protocol, in the database the root `--database` flag names
  ([docs/databases/user.md](docs/databases/user.md), "Keep a verifiable log").

### Changed

- **The cluster feature identity moved to the leaf package `pkg/feature`**
  (#627). `featuregate.Feature` is now `feature.Feature` and
  `featuregate.NotActiveError` is now `feature.NotActiveError`, unchanged in
  fields, message and `errors.As` behaviour; the feature names
  (`featuregate.GuardedWrites` and the rest) and the `Gate` stay in
  `pkg/cluster/featuregate`. The old declarations are deleted, with no alias.
  `(*config.Config).AdvertisedAPIEndpoints` is now
  `cluster.AdvertisedAPIEndpoints(*config.Config)`. Together they take
  `pkg/shard` and `pkg/config` out of the cluster import graph, so a lean
  standalone embedder no longer compiles `pkg/cluster` through them.
- **At-rest encryption is written against `pkg/filesys`, not Pebble's
  `vfs.FS`** (#627). `pkg/filesys` is a standard-library-only file-system
  interface; `pkg/seal/encfs.New`, `atrest.Encryptor.FS` and `Wrap`,
  `file.Encryptor`, `server.StorageFactory` and `node.ReplicaStorageFactory`
  now take and return `filesys.FS`, so the file backend and every non-Pebble
  embedder no longer compile Pebble's `vfs`. `pkg/storage/pebble` owns the one
  adapter: `VFS`, `FromVFS` and `WrapVFS`; a Pebble store is encrypted with
  `opts.FS = pebblestore.WrapVFS(opts.FS, enc.Wrap)`. `atrest.Encryptor.Apply`
  is deleted. The encrypting FS drops the Pebble-only `ReuseForWrite`,
  `SyncData`, `SyncTo`, `Fd` and path helpers, which the adapter supplies;
  `Lock` and `DiskUsage` forward to the base FS or return
  `*filesys.UnsupportedError` (`errors.ErrUnsupported`). `filesys.Default`
  and an encrypting FS over it implement `filesys.HostFS` (their names are
  host paths), and the adapter takes such an FS's lock, and reads its disk
  usage, through Pebble's `vfs.Default`: a host file's lock belongs to the
  process, so it has one owner, and a store opened through
  `pebblestore.VFS(dataPlaneFS)`, one through `WrapVFS(nil, enc.Wrap)` and a
  plain Pebble store exclude each other in one process, with the platform's
  own lock (`LockFileEx` on Windows). A composer's Pebble-backed
  `StorageFactory` or `ReplicaStorageFactory` opening through
  `pebblestore.VFS(dataPlaneFS)` with the barrier enabled therefore holds its
  database lock. `filesys.Default.OpenDir`'s handle syncs the directory on
  every platform but Windows, where its `Sync` is a no-op, as in Pebble's own
  directory handle: `FlushFileBuffers` fails on a directory there.
- **The Docker integration stack creates no host veth**:
  `test/integration/api/docker-compose.yml` runs every service in ONE network
  namespace, owned by a `netns` holder service with `network_mode: none`.
  `qrdbd`, `qrdbd-shamir` and the test runner join it with
  `network_mode: "service:netns"`, and the runner reaches the servers at
  `127.0.0.1:<port>` on their unchanged port sets instead of by service name.
  The `qrdb-integration` bridge network is gone. Its one veth per container
  start and stop cost an 80-core host about 450 uevents each. The opt-in
  Ollama overlay pulls its model at run time, so it gives the holder one
  bridge attachment: one veth for the stack, not one per service.
- **A verifiable log's checkpoint is a C2SP signed note** (#912): the bytes
  sign-head returns and records are the tlog-checkpoint text (origin, tree
  size, base64 root) and one signed-note signature line carrying the key name
  and the 4-byte key hash, not a JSON signed head. `vlog.RenderCheckpoint`,
  `vlog.ParseCheckpoint`, `vlog.VerifyCheckpoint` and
  `Descriptor.VerifyCheckpoint` replace `SignedHead.Encode` and
  `DecodeSignedHead`, which are gone; a checkpoint signed before a rotation
  verifies under the key version whose hash it carries
  ([docs/databases/architecture.md](docs/databases/architecture.md)).

### Fixed
- **A drop taken over by another no longer unfences its database on rollback**
  (#893): the drop-fence tombstone carries the drop's claim, and the store
  refuses a rollback, reclamation or clear whose claim a takeover replaced,
  in the same Raft-applied compare-and-swap, so the takeover's fence and
  reclamation stand.
- **A drop whose claim another drop took over is not reported as success**
  (#893): it returns `database.DropSupersededError`, and only the drop that
  removed the record releases the Raft-group and replica quotas.
- **A resumed drop of a dedicated-raft database cannot delete its
  successor's data** (#893): creating a dedicated-raft database claims its
  incarnation in the drop-fence marker of the store its keyspace is reclaimed
  from, and a drop tombstones that marker only from the incarnation it is
  dropping, so a reclamation resumed after the name was created again returns
  `database.DropSupersededError` and deletes none of the successor's keys,
  indexes or vectors.
- **Scaling a database records its replica count when two tenants hold the
  same name** (#893): `Manager.UpdateReplicasForTenant` resolves the caller's
  tenant's database, so the replica count no longer goes stale and a later
  drop releases the right replica quota.

- **Every node logs a feature activation, whichever path learns of it**: a
  node that adopted an activation from its replica's applied record, or from
  a linearizable refresh, enabled the feature without logging
  `feature gate: feature activation observed`, and then never logged it.
  Only `activate`'s own write logged it. A follower whose forward to the
  leader timed out was the usual case. The gate now logs each feature once
  per node, at the moment it is first enabled. The integration suites that
  wait for a node's activation (barrier-seal's cluster enable-encryption
  tests among them) timed out on such a node.

- **A user create whose handle claim times out and then commits no longer
  leaves the handle orphaned**: a create returned the claim's error before
  registering the claim for its rollback. A proposal that timed out after
  submission ("it may still commit") then committed a username or email
  index entry naming a user that was never written. Every later create of
  that name answered 409, and the lookup answered 404. A create now
  registers each claim it attempts, and the rollback's release is guarded on
  the freshly generated id, so it removes a claim that landed and nothing
  else.

- **A cancelled clustered boot stops at the step it is in**: `sdk/go/node`'s
  `NewClusteredNode` checked its ctx before opening the Raft WAL and in the
  readiness wait, but not between the WAL open, the host open and the shard
  start, so a cancel landing in the first still paid for the fsyncs of all
  three -- seconds on a contended disk (CI run 69313). ctx is now checked as
  each step ends: a boot cancelled while the WAL opens builds no host, and one
  cancelled while the host opens starts no shard; both return
  `*HostCreateError` wrapping `context.Canceled`.

- **The verifiable log's three remaining refusals are typed on every
  transport** (#912): a second create of a log, a second sign-head at a tree
  size that already has a checkpoint, and expiry on a log created without
  `allow_expire_bodies` reached a client as a bare class and message. They
  now carry the structured-refusal reasons `VLOG_LOG_EXISTS`,
  `VLOG_CHECKPOINT_EXISTS` and `VLOG_EXPIRE_NOT_ALLOWED` over REST, gRPC,
  QUIC, MCP and the Unix socket, and every SDK transport, embedded included,
  rebuilds them as `*vlog.LogExistsError`, `*vlog.CheckpointExistsError` and
  `*vlog.ExpireNotAllowedError`, so `errors.As` matches them
  ([docs/databases/api.md](docs/databases/api.md), "Verifiable logs").
- **A database create whose mark-live propose times out no longer leaves an orphaned drop-fence incarnation.** A claim of the marker that timed out at the caller could still commit after the create rolled back, so the marker named an incarnation no catalog entry held and every retry of the create was refused as existing elsewhere. `UnfenceDatabaseKeyspace` now releases an unknown-outcome claim back to the tombstone under a detached context, so the create returns the documented retryable 503 and the fence and catalog agree; a release that fails too is `*service.MarkerReleaseError`, which unwraps to the timeout.
- **An embedding request with no model hits the cache** (#658). The service
  looked the cache up under the requested model but stored under the model the
  provider used, so a request that left the model unset never hit. It now
  resolves the effective model, the request's or else the provider's
  configured default (the new `EmbeddingProvider.DefaultModel`), once and uses
  it for both the lookup and the store, so a no-model request and a request
  naming the default model share one entry
  ([docs/embeddings/api.md](docs/embeddings/api.md)).
- **`qrdb database create --backend` help names `objstore`** (#658). The help
  text was a hand-written list that had left out a valid backend. It is now
  built from `database.Backends()` (re-exported as `sdk.Backends`), the set
  `BackendType.Valid` checks a create against, so a valid backend cannot be
  missing from it.

### Changed

- **Database names belong to a tenant** (#893): a database is identified by
  its tenant and its name, so two tenants can each create a database of the
  same name and each reaches only its own. Creating a name another tenant uses
  succeeds and makes the caller's own database instead of returning
  `database.AlreadyExistsError` (409, `ALREADY_EXISTS`), which disclosed that
  the other tenant held the name; `AlreadyExistsError` now means the name is
  taken in the caller's own tenant.
- **`qrdb users cert-binding add|list|remove`** (#671). The CLI had no verb
  for enrolling a certificate binding, though the default
  `auth.mtls_binding_policy: required` refuses a certificate until one is
  enrolled. `add` takes the base64url SHA-256 SPKI thumbprint, or a PEM
  certificate through `--cert`, which also sets the expiry by default
  (`--not-after` overrides it). An expiry at or before the Unix epoch, from
  either, is refused: the wire reads a non-positive expiry as "never
  expires", which only an omitted expiry may mean. `remove` withdraws one binding, and `list`
  prints the user's bindings. They map to `AddCertBinding` and
  `RemoveCertBinding`, and work on REST, gRPC, QUIC, Unix and MCP
  (`docs/identity-rbac/cli.md`).

- CI: the `fips` job's legs are rebalanced from the measured wall times of run 68401 (#1230, ruling 221(5)): nine legs, `tenant`, `seal`, `server`, `auth`, `service`, `api-keys`, `rest`, `sdk-transport` and `sdk`, with `pkg/tenant`, `pkg/seal` and `pkg/server` each in a leg alone. Each leg has its own `timeout-minutes` from its matrix entry, its expected time with headroom and at most 45, and go test's per-package budget is that limit less 5 minutes; `test/buildgates/fips_legs_test.go` enforces both. **The `tenant` (about 42 minutes) and `seal` (about 36.5 minutes) legs stay over 30 minutes until #1234 splits those packages**; every other leg is expected under 30 (docs/seal/fips140-mode.md, "The legs").
- CI: the `fips` job splits `pkg/tenant`, `pkg/seal` and `pkg/server` by test name, each over two legs, so every leg is expected under 30 minutes and `server` under 25 (#1234, ruling 221(5)): twelve legs, with `tenant-lifecycle`, `seal-barrier` and `server-raft` selecting tests by the Makefile's `FIPS_RUN_<leg>` (`go test -run`) and `tenant`, `seal` and `server` skipping what those select (`-skip`). The limits are 30 minutes for the tenant and seal legs and 25 for the server legs. `make -s print-fips-leg-filter FIPS_LEG=<leg>` prints a leg's patterns, and the new `test/buildgates/fips_split_test.go` fails when a top-level test of a split package runs in no leg or in two. `TestStrategiesMap_ConcurrentStatusAndChangePassword_NoRace` uses the cheap test KDF, which brings it from 195 seconds to under 8 under FIPS mode.
- The rationale for ci.yml's nightly `schedule:` (ci.yml, nightly.yml,
  AGENTS.md, docs/release/architecture.md, docs/release/engineering.md,
  test/buildgates/ci_tiers_test.go)
  now cites ruling 135 and Company Agent ruling 216(1). The in-template Gitea
  schedule trigger is the one scheduler, and the owner ruling of 2026-10-11 and
  m433 forbid host cron, timers and watchers. The text no longer calls the
  restore a reversal of those rulings, and the gate's error message no longer
  tells the reader to dispatch from a coordinator loop.
- CI: a draft pull request runs no CI. Every job skips on `pull_request.draft` and CI Gate fails on a draft; a PR leaving draft starts its run with an empty commit or a merge of main (ci-standard §10 (f), ruling 214).
- CI: ci.yml carries ci-template v1.27.0's nightly `schedule:` trigger again (cron `0 4 * * *`), so the full and nightly tiers, soak included, run on main each night; `test/buildgates/ci_tiers_test.go` allows a schedule in ci.yml and automerge.yml alone. This reverses the 2026-10-11 owner ruling and Company Agent m433 on Software's order DS-3913 (m1378 (2)).

- **CI adopts ci-template v1.29.3 (#1294):** a scheduled run's concurrency group is `schedule-<repository>`, so a repository has one at a time; a new `schedule-guard` job sets `verdict` to `run`, `busy` (a push run of ci.yml on `main` is in progress or waiting) or `unreadable`, and on a scheduled run `docker-build` runs only on `run`, every other bigdisk job skipping with it (`soak` carries the clause as well); on `busy` CI Gate skips, and on any other verdict but `run` it fails closed. `test/buildgates/ci_schedule_guard_test.go` holds every bigdisk job behind the guard and runs the guard's real script against a stub `curl`.
- **CI adopts ci-template v1.29.2 (#1294):** the integration image's tag hashes `APK_INDEX_SUM`, the Alpine package index checksum `scripts/apk-index-sum.sh` reads (v1.29.0), and its build takes it as a build argument before `apk upgrade --no-cache`, so a new Alpine package release rebuilds the image; `lint-gates` runs `make lint-gates` without `-k` (v1.29.1); the code-tier jobs test docker-build's `it_image` output as well as `docs_only`, and test-typescript needs docker-build (v1.29.2). Every ci.yml job reads the sum through `make -s apk-index-sum`, which gives the script the `GO_IMAGE` pin from `scripts/tool-pins.sh` as `APK_BASE_IMAGES`: the Dockerfile's `FROM ${GO_IMAGE}` has no ARG default, so the bare script stopped every docker-build run.
- **CI adopts ci-template v1.28.0 (ruling 220(3)):** `make deps-check` is a lint-class gate on the PR tier (ruling 219(3)). It moves from the full-tier `image` steps to the `lint` job, after `make lint`, and runs on every event, a docs-only pull request included, so a module behind its dependency floor fails the pull request rather than `main` after the merge.
- **CI adopts ci-template v1.27.0 (ruling 211):** `make coverage` and the `test-clusterproof`, `coverage-fts`, `test-tags` and `test-sdk` suites are five parallel code-tier jobs instead of serial `image` steps, and the linux/arm64 release cross-build (plain and FIPS) runs on the full tier only, like `fips`.
- docs: apply the spec-review findings for api-server to collections (#658)
- **Feature documentation, replica-forwarding through watch, matches the code**
  (#658): the spec-review findings are applied — statements the code does not
  bear out are corrected or removed, content owned by another feature moves to
  its owner (the `kvstore.KVStore` surface to key-value, DAO aggregations and
  secret entity fields to the query-planner and entity-SQL companions), the
  CLI pages gain per-verb examples and exit status, and code comments citing the
  deleted `docs/08-bootstrap-auth-architecture.md` point at the feature folders.
- docs: apply the spec-review findings for configuration to identity-rbac (#658)
- docs: apply the spec-review findings for key-value to release (#658)

### Fixed

- **A node no longer fails its boot on a system shard that is still
  settling when it opens the database catalog.** Listing the catalog is a
  linearizable read, and it ran before the boot waited for a leader, so an
  election in progress, or a new leader's ReadIndex warm-up, failed the boot.
  The open now waits out the settling within the same bounded budget as the
  other boot reads (`server.SystemShardSettleBudget`). Any other failure still
  fails the boot at once.
- **A create whose catalog record cannot be removed during its rollback
  keeps its data directories.** If a create fails after its catalog record
  committed, and the rollback then cannot delete that record, the create now
  returns `database.CatalogOrphanError`, carrying both failures, and leaves
  the directories in place for the record that still names them. Before this
  change the directories were removed and the failure was only logged.
- **A create that fails clearing its predecessor's seal record rolls back
  its own tenant's database.** The rollback dropped the database by name
  alone. When another tenant held a database of the same name, the drop was
  refused as ambiguous and the failed create stayed open and recorded. It
  now drops by tenant and name.
- **`qrdb users cert-binding add|remove` document `--` before a thumbprint**
  (#671). A thumbprint is base64url (RFC 8705 `x5t#S256`), so about one in 64
  begins with `-` and the CLI read it as a flag (`unknown shorthand flag`).
  The help text and `docs/identity-rbac/cli.md` now say to put every flag
  first and the thumbprint after `--`, which ends option parsing (POSIX
  Utility Syntax Guideline 10), and the CLI tests enrol and withdraw a fixed
  `-`-leading thumbprint that way on every transport.
- **The bootstrap log names the strategy the system barrier is sealed under**
  (#671). It used to log the configured strategy, so a barrier initialized
  under `none` was logged as `strategy=passphrase` on a boot whose
  configuration named passphrase. The `system bootstrap complete` record now
  takes the barrier's active strategy, and adds `configured_strategy` when the
  configuration disagrees.
- **`service.Host` is split into a data host and a Raft-admin host** (#627,
  slice A). `service.Host` keeps the eleven methods the data services call
  (`ID`, `RaftAddress`, `NodeIdentity`, `SyncPropose`, `SyncRead`,
  `ProposeWithMode`, `StaleRead`, `ReadWithConsistency`, `FollowerRead`,
  `LeaderID`, `Close`) and no go-quicraft engine type in its method set. The
  fourteen membership, snapshot, shard-lifecycle, leadership-transfer, drain
  and introspection methods move to the new `pkg/raftadmin` package as
  `raftadmin.Host`. `service.AdminHost` composes the two, and is what
  `NewAdminService`, `NewRebalanceService`, `NewHostShardSnapshotProvider`,
  `lifecycle.Infra.Host` and `TenantReplicaHosts.StartedHosts` take;
  `NewHostTopologyProvider` takes a `raftadmin.Host`. There is no alias for
  the old 25-method interface: an embedder that passed a host to those
  constructors passes the same value, and a test double that embedded
  `service.Host` to reach admin methods embeds `service.AdminHost`.
- **Breaking: the server, storage, engine and discovery re-exports leave the
  `sdk/go` root for subpackages** (#627, slice B). A caller of the root alone
  (`*sdk.Client`, the closed-store error, the MCP error codes) no longer links
  `pkg/server`, `pkg/server/lifecycle`, `pkg/apimodule` or any protocol server
  beneath `pkg/api`; under `BUILD_TAGS` it linked 21 of those packages before.
  `make check-lean-standalone` now also runs `TestLeanStandaloneRootLinksNoServer`
  over the new `sdk/go/examples/leanroot` fixture, under the lean tag set and
  under `BUILD_TAGS`, so they cannot come back. There is no alias in the root;
  change the import and the package qualifier:

  | Symbols | Old import | New import |
  |---|---|---|
  | Server options and module registry (`ServerOption`, `With*` server options, `WithAuditLogger`, `ServiceModule`, `BaseModule`, `Module*`, `RESTRouteRegistry`, `AuthMiddleware`, `LifecycleNode`, `LifecycleInfra`, `BuildSealServicesOnly`, `CertProviderConflictError`, `CABootstrapConflictError`) | `sdk/go` | `sdk/go/server` |
  | Sealed-window listener and allowlists (`SealedWindow*`, `ServeSealedWindow`, `NewSealedWindowHandler`, `*SealedWindowAllowlist`) | `sdk/go` | `sdk/go/server` |
  | gRPC registration (`RegisterGRPCService`, `GRPCRegisterOption`, `GRPCWebConfig`, `DefaultGRPC*`) | `sdk/go` | `sdk/go/server` |
  | Storage backends (`Storage`, `Storage*` types and errors, `NewMemoryStorage`, `NewFileStorage`, `NewPebbleStorage`, `NewObjStoreStorage`, `PebbleConfig`, `GuardedStorage`) | `sdk/go` | `sdk/go/storage` |
  | Time series (`TimeSeries*`, `NewTimeSeriesStorage*`) | `sdk/go` | `sdk/go/timeseries` |
  | Vector storage (`Vector*`, `NewPebbleVectorStorage`, `PebbleVectorStorage`) | `sdk/go` | `sdk/go/vector` |
  | Vector indexes (`FlatIndex`, `HNSW*`, `IVF*`, `ErrGPUNotCompiled`) | `sdk/go` | `sdk/go/vector` |
  | State machines (`SM*`, `KVStateMachine`, `FastStateMachine`, `*Backend` state-machine backends, `With*` state-machine options) | `sdk/go` | `sdk/go/statemachine` |
  | Local backend (`LocalBackend`, `NewLocalBackend`, `SimpleStateMachine`, `SMAdapter`, `Backend*Error`) | `sdk/go` | `sdk/go/backend` |
  | Discovery (`Discovery*`, `BuildDiscovery`, `NewDiscoveryManager`, `Peer`) | `sdk/go` | `sdk/go/cluster` |

  The closed-store error (`ErrStorageClosed`, `StorageClosedError`,
  `IsStorageClosed`) stays in the root: every transport returns it. So do the
  cluster, feature-gate and membership methods on `*sdk.Client`
  (`cluster.go`, `featuregate.go`, `membership.go`), which are client API and
  not re-exports. What still links the root to the cluster engine is
  `pkg/service`, `pkg/config` → `pkg/cluster` and `pkg/seal` →
  `pkg/storage/pebble`.

  `NewTimeSeriesStorageWithDir` and `NewTimeSeriesStorageWithEncryptor` now
  return `*timeseries.TimeSeriesDirStorage` (#1207). They used to return the
  bare `*timeseries.Storage` over a Pebble database they opened and never
  exposed: no caller could reach the `TimeSeriesBatch` every write takes, and
  `Close` left the database open. `TimeSeriesDirStorage` embeds the store and
  adds `Batch`, over the database it owns, and a `Close` that closes both.
  Each new subpackage (`sdk/go/server`, `sdk/go/storage`,
  `sdk/go/timeseries`, `sdk/go/vector`) carries its own behavioural tests and
  meets the 91% coverage floor on its own.

### Fixed

- `MVCC().PutWithLease` on a state machine that keeps no revision history (the default pebble state machine) reports version 1 for the key it wrote, not 0. Presence is the only version such a state machine's transaction compares read, so a compare-and-set on the reported version now holds there too (#1211).
- **A passphrase change checks the current passphrase and cannot lose a
  race** (#631). `StorageBarrier.ChangePassword` refuses a current passphrase
  that does not open the persisted root key (`seal.ErrCredentialMismatch`),
  keeps the load generation, so the next unseal no longer logs a generation
  going backwards, and replaces the blob only through a guarded swap
  (`seal.ErrPassphraseChangeConflict`, `seal.ErrGuardedSwapUnsupported`).
  A change that overlaps another rewrite of the root key, such as a second
  passphrase change, is refused before it writes anything with
  `seal.ErrRootKeyWriteInProgress`, a retryable conflict (`409` / `Aborted`)
  rather than an internal error. A migration's adoption of its target's root key retires the source
  barrier's state persister under the same lock, and the persister reads that
  retirement only once it holds the lock, so a rotation entered before the
  adoption and resumed after it is refused (`seal.ErrRootKeyAdopted`) rather
  than writing the pre-migration key back over the migrated one.
- **Unsealing a barrier that was never initialized is
  `seal.ErrNotInitialized`**, not a corrupt root-key error, and keeps its
  identity across every transport (`BARRIER_NOT_INITIALIZED`), as do
  `BARRIER_CREDENTIAL_MISMATCH`, `BARRIER_PASSPHRASE_CHANGE_CONFLICT` and
  `BARRIER_ROOT_KEY_WRITE_IN_PROGRESS` (#631).

### Security

- **The TypeScript SDK sends a secret only over https and never follows a
  redirect with one** (#631). Every call whose body carries a passphrase, PIN,
  share, token or barrier secret is refused with `ConfigError` on an `http://`
  base URL before any network I/O, and is sent with `redirect: "error"`
  (docs/typescript-sdk/api.md, "Secret-bearing calls"). The QUIC SO gate reads
  only `so_pin` from a passphrase-change payload, so the current and next
  passphrases are never decoded a second time.
- `make security` passes on main again: gosec's G204 at
  `test/buildgates/lean_standalone_test.go`'s `go list` call (#1207) carries the
  repo's `#nosec G204` justification, as the other build gates' `go list` calls do.
- `make security`'s `gosec-sdk` passes too: gosec's G122 in the `filesContain`
  helpers of `sdk/go/storage` and `sdk/go/timeseries` (#1207) is fixed by walking
  and reading under one `os.Root` opened on the directory, not by a waiver.
- A distributed Shamir share that reaches its time-to-live (`CeremonyConfig.ShareTTL`) has its enclave destroyed before the store reports it expired. The expiry timer used to drop the share from the store's index and mark it expired, release the lock, and only then destroy the enclave, so for that window `Exists`, `Expired` and `Claim` reported a wiped share whose plaintext could still be opened. The destroy now runs under the store's lock, ahead of the index update.

## [0.57.0-alpha] - 2026-10-26

### Changed

- **`service.Host` is split into a data host and a Raft-admin host** (#627,
  slice A). `service.Host` keeps the eleven methods the data services call
  (`ID`, `RaftAddress`, `NodeIdentity`, `SyncPropose`, `SyncRead`,
  `ProposeWithMode`, `StaleRead`, `ReadWithConsistency`, `FollowerRead`,
  `LeaderID`, `Close`) and no go-quicraft engine type in its method set. The
  fourteen membership, snapshot, shard-lifecycle, leadership-transfer, drain
  and introspection methods move to the new `pkg/raftadmin` package as
  `raftadmin.Host`. `service.AdminHost` composes the two, and is what
  `NewAdminService`, `NewRebalanceService`, `NewHostShardSnapshotProvider`,
  `lifecycle.Infra.Host` and `TenantReplicaHosts.StartedHosts` take;
  `NewHostTopologyProvider` takes a `raftadmin.Host`. There is no alias for
  the old 25-method interface: an embedder that passed a host to those
  constructors passes the same value, and a test double that embedded
  `service.Host` to reach admin methods embeds `service.AdminHost`. A lease
  `TimeToLive` a follower refuses on a host that implements `service.Host`
  alone still names the leader's replica ID, with an empty leader address,
  since such a host cannot report the shard's membership.
- **At-rest encryption is written against `pkg/filesys`, not Pebble's
  `vfs.FS`** (#627). `pkg/filesys` is a standard-library-only file-system
  interface; `pkg/seal/encfs.New`, `atrest.Encryptor.FS` and `Wrap`,
  `file.Encryptor`, `server.StorageFactory` and `node.ReplicaStorageFactory`
  now take and return `filesys.FS`, so the file backend and every non-Pebble
  embedder no longer compile Pebble's `vfs`. `pkg/storage/pebble` owns the one
  adapter: `VFS`, `FromVFS` and `WrapVFS`; a Pebble store is encrypted with
  `opts.FS = pebblestore.WrapVFS(opts.FS, enc.Wrap)`. `atrest.Encryptor.Apply`
  is deleted. The encrypting FS drops the Pebble-only `ReuseForWrite`,
  `SyncData`, `SyncTo`, `Fd` and path helpers, which the adapter supplies;
  `Lock` and `DiskUsage` forward to the base FS or return
  `*filesys.UnsupportedError` (`errors.ErrUnsupported`). `filesys.Default`
  and an encrypting FS over it implement `filesys.HostFS` (their names are
  host paths), and the adapter takes such an FS's lock, and reads its disk
  usage, through Pebble's `vfs.Default`: a host file's lock belongs to the
  process, so it has one owner, and a store opened through
  `pebblestore.VFS(dataPlaneFS)`, one through `WrapVFS(nil, enc.Wrap)` and a
  plain Pebble store exclude each other in one process, with the platform's
  own lock (`LockFileEx` on Windows). A composer's Pebble-backed
  `StorageFactory` or `ReplicaStorageFactory` opening through
  `pebblestore.VFS(dataPlaneFS)` with the barrier enabled therefore holds its
  database lock. `filesys.Default.OpenDir`'s handle syncs the directory on
  every platform but Windows, where its `Sync` is a no-op, as in Pebble's own
  directory handle: `FlushFileBuffers` fails on a directory there.
- **go-quicraft v0.24.2-alpha, go-enclave v0.10.0-alpha, go-truststrap
  v0.7.0-alpha and go-objstore v0.7.0-alpha in the root, SDK, GORM
  conformance and cluster-proof modules** (#944); go-quicraft/test/performance
  moves to v0.24.2-alpha in the two performance modules. Every call site
  adopts the upstream renames, with no alias left behind:
  - go-quicraft's `Host` accessors lost their `Get` prefix (`LeaderID`,
    `LeaderIDAndTerm`, `ShardInfo`, `ShardMembership`, `NodeHostInfo`,
    `LogRange`, `LogReader`, `NewSession`, `NewSessionWithOwner`,
    `NoOPSession`), and so did every go-qrdb interface that mirrors them
    (`cluster`, `featuregate`, `backend/raft`, `service`, `server`, `qrdbd`).
  - `DiagRaftState` returns a `quicraft.RaftStateDiag`; the encryption
    replication-progress probe reads its `Committed` field.
  - go-enclave's protected-memory types are interfaces: `pkg/secure` holds an
    `enclave.ByteAccess`, an `enclave.Sealed` and an `enclave.Allocator`, and
    `Secret.Open` opens through `Sealed.OpenIn`, failing with
    `*enclave.CapabilityError` for a backend whose handles expose no bytes.
    `sdk/go/secure.SetAllocator`, `ProcessAllocator` and
    `NewAllocatorWithinRLimit` take and return `secure.Allocator` by value
    rather than `*secure.Allocator`.
  - go-quicraft removed `shamir.Share`; `sdk/go`'s `ShamirShare` alias is
    replaced by `ShamirProtectedShare` (`shamir.ProtectedShare`).
  - go-objstore's `factory.NewStorage` takes a `common.Credentials` of
    go-enclave handles. The objstore storage engine passes none, so a cloud
    provider authenticates through its SDK's default credential chain, and a
    credential key in the engine's `settings` (`accessKey`, `secretKey`,
    `accountKey` and the like) now fails the open with go-objstore's
    `*common.CredentialSettingError`.
- **The lean-standalone import gate forbids only go-quicraft's Raft engine and
  the `pkg/api` subpackages** (#627, ruling 193). On the go-quicraft side the
  gate forbids only the NodeHost package (`go-quicraft/pkg`) and its
  `pkg/internal/...` engine. The seal and Shamir primitives, and the other
  public leaf packages, no longer count as violations. On the `pkg/api` side
  the gate forbids only the subpackages, so the root's contract types are
  allowed. The 31 ratchet entries those edges held are deleted, and every
  edge into the engine stays listed. `make check-lean-standalone` proves each
  narrowed rule against a synthetic forbidden edge.
- **`qrdb barrier unseal` no longer requires `--passphrase`** (#671). Omitted,
  it sends an empty secret, which unseals a `none` barrier sealed by hand; a
  passphrase barrier refuses it.

### Fixed

- **A `none` node unseals its barrier itself after a restart** (#671). qrdbd
  waited in the sealed window for an empty unseal call, against
  `docs/seal/README.md` and `docs/cluster-verification`. A barrier whose
  persisted root key names `none` is now unsealed at boot. One migrated to a
  keyed strategy, or not yet initialized, is left sealed.
- **The clusterproof workload backs off after a failed operation** (#671).
  A client retried a refusing or timing-out node in a tight loop, flooding
  the history and the linearizability checker. Every failed dial or
  operation is now followed by an exponential, equal-jittered pause from
  `redial_backoff` up to the new `max_backoff` (default `4s`), reset by a
  success. Over gRPC the status code classifies the outcome:
  `InvalidArgument`, `Unauthenticated`, `PermissionDenied` and
  `ResourceExhausted` fail a write; `Unavailable` leaves it indeterminate,
  because gRPC also returns it for a connection that broke after the request
  was sent, and drops the connection so a redial that records nothing gates
  the next operation.

### Fixed

- **A one-node cluster founded under qrdbd-managed TLS can grow** (#671).
  Before this fix, a founder with itself as its only peer ran without a Raft
  transport and seeded no member identity, not even its own. Restarting it
  with a second peer and running `admin member add` then committed a
  membership with no identity for replica 1. The new member refused every
  heartbeat and vote from the founder as `missing_identity`, so no leader was
  elected. A transportless founder now seeds its own identity from the
  certificate it holds, in both `qrdbd` and the SDK node. A cluster founded
  before this fix keeps the membership it committed and has to be founded
  again to grow. `docs/clustering/user.md` gives the procedure for growing a
  one-node cluster.

- **The Security Officer separation checks hold only under strict separation
  of duties** (#1042): `auth.separation_of_duties`, forced on in FIPS 140-3
  mode and decided by `wiring.ResolveStrictSeparation` for every node path,
  now gates the SO-exclusive grant and revoke of the `so` role
  (`middleware.WithStrictSeparation`) and the tenant SO being distinct from
  the first admin, as it already gated the Owner role's voiding. In the
  standard posture the tenant Admin grants and revokes `so` like any other
  role, and may be named the tenant's SO.

- **CI builds its images once per run and every job pulls them by digest**
  (DS-3516): `docker-build` pushes the toolchain and integration images to the
  forge's registry (the Gitea registry, derived from `github.server_url`, or
  `ghcr.io` on GitHub), as `qrdb-ci-toolchain` and
  `qrdb-ci-it` tagged `<content-hash>-<trust>`, and outputs their `@sha256:`
  references; `lint`, `image`, `fips`, the integration legs and `soak` pull
  exactly those through `scripts/ci-registry.sh`, on any runner, and fail
  rather than rebuild when they cannot. `make integration-test` given a digest
  `IT_IMAGE` requires the pulled image instead of building it, and the
  reaper removes stale pulled images, never a name of an image any container
  uses. `test/buildgates/ci_image_digest_test.go`
  holds every consumer to the pull.
- **A push to `main` publishes the CI images from the release runner**
  (DS-3516, ruling 169): on the forge, `docker-build` runs on the runner
  labelled `release` on a push to `main`, and on `bigdisk`, pushing nothing,
  on every other event; github.com keeps `ubuntu-latest`. The push reads the
  release runner's credential contract alone (ruling 173, claude-agents#329):
  it goes to `RELEASE_REGISTRY` and logs in with `RELEASE_REGISTRY_USER` and
  `RELEASE_REGISTRY_TOKEN` from the runner's env file, not a repository
  secret, and no `env:` in ci.yml sets any of the three so none can mask
  them. `scripts/ci-registry.sh` names the credential a refused or empty
  login used, never its value. `test/buildgates/ci_image_digest_test.go`
  fails when `docker-build` runs anywhere but `release` on a push to `main`,
  on `release` on any other event, or when any other job can run there.

## [0.56.0-alpha] - 2026-10-26

### Removed

- **`auth.spiffe_trust_domain` is gone; `auth.cluster_trust_domain` is the one
  trust-domain setting** (#1150). It pins the mTLS authenticator, names the
  built-in CA's trust domain and is the trust domain no tenant may be. A
  configuration file that still carries `spiffe_trust_domain` fails the load as
  an unknown key. `config.AuthConfig.SPIFFETrustDomain` and
  `EffectiveSPIFFETrustDomain` are deleted; `lifecycle.Infra.SPIFFETrustDomain`
  and `wiring.AuthMiddlewareConfig.SPIFFETrustDomain` are now
  `ClusterTrustDomain`.

### Changed

- **Tenant creation refuses an ID that cannot be a SPIFFE trust domain**
  (#1150). A tenant ID is the trust domain of its principals'
  `spiffe://<tenant>/<kind>/<name>` IDs, so `OnboardTenant`, on every transport
  and in `qrdb tenant create`, refuses before anything is written an ID that
  does not round-trip through a SPIFFE ID (an uppercase letter, for instance)
  and an ID equal to `auth.cluster_trust_domain`, with
  `*tenant.TenantTrustDomainError`, classified as invalid input (400 /
  `InvalidArgument`). Such a tenant could never be issued a client
  certificate or admit a principal.

### Fixed

- **The Pebble vector store no longer reaches a closed DB when Close races an
  operation**: `PebbleVectorStorage` checked its closed flag and then used the
  DB, and `Close` closed the DB before setting the flag, so an operation that
  passed the check while Close ran used a closed `pebble.DB` (a `pebble:
  closed` panic or an error from inside Pebble), and two concurrent Closes both
  called `db.Close`. Every operation now holds the close gate
  `pkg/storage/pebble` uses from its closed check to its last DB call; Close
  waits for in-flight operations, marks the store closed and closes the DB
  exactly once, and a later or concurrent Close returns nil. Calls after Close
  return `ErrStorageClosed`.
- **An async vector compaction or index rebuild no longer outlives the node**
  (#1171): the jobs ran on no context the node controlled, so one in flight at
  shutdown reached the vector Pebble DB after it closed. The vector service now
  runs them on its own background context, and the node closes the service
  before the vector storage. `Close` cancels every job and returns only once
  each has stopped, on every call, a concurrent second one included, and the
  service refuses every later request (a rebuild, a compaction, or any read or
  write, `BatchDeleteVectors`, `ListVectors`, `UpdateMetadata` and
  `SearchByID` among them) with `VectorServiceClosedError` before it touches
  storage.

### Added

- **`sdk/go` re-exports `WithRegisterCeremonyCA`** (#1129), beside the other
  gRPC registration options: a composer registering qrdb's gRPC service on its
  own server (gRPC or the Unix socket) attaches its `sdk.CeremonyCA`, so
  `CeremonyInitialize`'s CSR-sign and generate paths issue the bootstrap
  admin's certificate from that CA instead of failing with
  `*ceremony.AdminCredentialRequiresCAError`. That error now reaches a remote
  client typed: every transport carries it as the structured refusal
  `ADMIN_CREDENTIAL_REQUIRES_CA` (metadata `path`), and the SDK rebuilds it, so
  `errors.As` matches it, `Path` included, over gRPC, Unix, REST, QUIC and MCP.
- **MCP barrier and ceremony secrets travel as base64, losslessly** (#1130).
  Every secret argument of the barrier, tenant-barrier, holder, database and
  ceremony tools (`secret`, `passphrase`, `barrier_secret`, `so_pin`,
  `user_pin`, including `qrdb_database_unseal`, `qrdb_database_create`,
  `qrdb_barrier_rekey_start` and `qrdb_barrier_officer_revoke`) has a
  `<name>_b64` sibling in
  standard padded base64 (RFC 4648 §4), so bytes that are not UTF-8, or that
  begin with a byte-order mark, reach the barrier exactly. The text form stays;
  a call carrying both forms is refused with `SecretArgumentConflictError`, and
  invalid base64 with `ValueDecodeError`. The Go SDK's MCP client always sends
  `<name>_b64`. `qrdb_barrier_unseal`'s `secret` and
  `qrdb_ceremony_sign_csr`'s `so_pin` are no longer schema-`required`, since
  either form satisfies them; the handler still refuses a call with neither.
- **`RegisterGRPCService` registers `qrdb.v1.TenantBarrierService`** (#1156)
  through the new `WithRegisterTenantBarrierService` option, gated by the
  middleware from `WithRegisterAuthMiddleware` (nil: the no-auth posture);
  `sdk/go` re-exports both options. A composer that builds its own
  `*grpc.Server` now serves the tenant quorum and holder-claim RPCs, and the
  Unix socket server registers the service through the same option instead of
  a separate call.
- **The vanguard blocker is a reproducible probe** (#1151): `make
  check-vanguard-probe` runs `test/vanguardprobe`, a module of its own, which
  asserts that `connectrpc.com/vanguard` v0.5.0's `vanguardgrpc` hands a
  method handler a nil gRPC interceptor and a peer with no TLS state, the
  reasons `pkg/api/grpcweb` stays hand-rolled. It fails, naming the
  replacement, the day a vanguard release no longer does either; run it at
  each release. It is not part of `make test` and no qrdb build links it.
- **`sdk/go` resolves the verified client chain of a connection the node's
  trust pool admitted** (#1181). The dynamic client-auth trust pool remaps a
  verifying `ClientAuth` mode to a non-verifying one and verifies the peer
  itself, so such a connection's `tls.ConnectionState.VerifiedChains` is
  empty. `sdk/go` re-exports `ResolveVerifiedChains`, `IssuerVerifier`,
  `ErrNoPeerCertificate` and `TrustStateUnavailableError`, and
  `node.Node.IssuerVerifier()` returns the node's own pool, the one its mTLS
  authenticator verifies against (`node.ErrTrustPoolNotWired` before boot
  builds it and after `Close`). A handler mounted on a node listener calls
  `sdk.ResolveVerifiedChains(ctx, state, verifier)` instead of reading
  `PeerCertificates[0]` or building a verifier of its own.

### Fixed

- **A failed gRPC `Exists` names itself, and MCP collection results carry
  document IDs as strings**: the `sdk/go/transport/grpc` client's `Exists`
  returned its `RequestError` with `Method` `"Get"`; it now reports
  `"Exists"`, and a `NotFound` is still `false`. The `id` in the results of
  `qrdb_collection_insert`, `qrdb_collection_get`, `qrdb_collection_update`
  and `qrdb_collection_list_documents` is a decimal string, as the `id`
  argument already is, and the `sdk/go/transport/mcp` client refuses a JSON
  number there. An empty document page is `[]` rather than `null`.

## [0.55.0-alpha] - 2026-10-24

### Fixed

- **MCP collection document IDs travel as decimal strings, losslessly**: the
  `id` argument of `qrdb_collection_get`, `qrdb_collection_update` and
  `qrdb_collection_delete` was a JSON number, so a default `unix_nano` ID
  (above 2^53) was rounded on the way to the server and the document reported
  not found. The argument is now a decimal string on the server tools and in
  the `sdk/go/transport/mcp` client; the number form is refused.
- **A PostgreSQL extended-protocol statement runs at each Execute, not at
  Parse**: a cached parameterless SELECT sees rows written after its first run,
  and a prepared DROP SEQUENCE or DROP TABLE ... CASCADE takes effect at each
  Execute rather than once at Parse.

- **A push to `main` no longer cancels the `main` CI run in progress**:
  `ci.yml`'s concurrency group on `main` carries the commit as well as the
  event, so every merge keeps a completed run; `cancel-in-progress` stays a
  literal `true`, so a newer push to a pull request still cancels that pull
  request's older run.

- **`github.com/labstack/echo/v4` is v4.16.1** (indirect, through the Wails
  devserver): v4.15.3 carries GHSA-cxf3-9h37-7rr8 (critical) and three high
  advisories, which took the image job's Trivy scan red on `main`.
- **`make check-mod-tidy` reports a module it could not check as UNKNOWN, not untidy**: when `go mod tidy -diff` fails without a diff (a download timeout), the gate prints the tool's error and says tidiness is unknown, instead of telling the reader to tidy a tidy module.

- **`sdk/go/transport/mcp` refuses `UpdateDatabaseBarrier` with no barrier
  configuration**: a request whose `Barrier` was nil was dereferenced and
  panicked. It is now refused with a `RequestError` before the call, rather
  than sent without `enabled`, which the `qrdb_database_update_barrier` tool
  reads as a request to disable the barrier.

- **`sdk/go/transport/grpc` `VerifyFactor` and `RevokeFactor` return nil on
  success**: both passed the RPC's error to `mapGRPCError` unconditionally,
  which turns a nil error into a `RequestError`, so every successful factor
  verification and revocation over gRPC was reported as a failure.

### Changed

- **`mvcchost.New` opens its MVCC state machine in memory** (#1030): each
  Host gets its own in-memory Pebble filesystem, so a test built over it
  writes nothing to disk.
- **The `sdk/go` index re-exports are `ServiceIndexServicer` and
  `NewServiceIndexService`** (#928), in the `ServiceBatchServicer` family;
  `sdk.IndexServicer` and `sdk.NewIndexService` are removed.

## [0.54.0-alpha] - 2026-10-23

### Added

- **`sdk/go` re-exports `role.EffectivePermissions`** (#1089), so a composer
  that issues tokens below authz expands a role's whole-segment wildcards
  (`objstore:*`, `*:read`, `*:*`) through the SDK instead of copying the
  expansion.

## [0.53.0-alpha] - 2026-10-23

### Security

- **The embedded transport binds its caller before every dispatch**
  (#1084). `embedded.WithContextBinder`, re-exported as
  `node.WithContextBinder`, runs once before every unary and streaming call,
  ahead of the auth middleware; its error fails the call unchanged with no
  handler reached, and the context it returns is the one authorized and
  served. A nil binder fails `embedded.New` with `NilContextBinderError`; a
  binder that returns a nil context fails the call with
  `NilBoundContextError`. With no binder installed nothing changes.

## [0.52.0-alpha] - 2026-10-23

### Added

- **The tenant Admin creates and updates roles in its own tenant** (#940).
- **Tenant `Status` is enforced in the auth chain**, and a tenant carries
  `Metadata` and `Description`, on every transport (#809, #792).
- **The tenant-scoped stored-policy API for ABAC** on every transport (#899).
- **Device onboarding, OTA activate and OTA confirm are tenant-scoped
  permissions** (#902, #903), and `audit:delete` is a permission (#993).
- **PIN lockout and ceremony challenge expiry read the composer's clock**
  through the seam, not the wall clock (#904).
- **A restarted coordinator resumes a rolling `cluster enable-encryption`**
  (#894).
- **FIPS 140-3 is a required gate in CI's full tier** (#20).

### Changed

- **An encrypted `file` store names its records under a barrier-held key**
  (#911). A record's file name is the HMAC-SHA-256 (RFC 2104) of its key under
  the store's random record-name key, kept in `.qrdb-file-names` and written
  through the barrier, so a name can no longer be matched to a guessed key. On
  a store opened while the barrier is sealed, `Exists` and `Delete` now fail
  with `seal.ErrSealed`, as `Get` and `Set` already did. **Breaking (on-disk
  format):** the format marker is version `4`, and a version `3` data
  directory, plaintext or encrypted, is refused at open. See
  `docs/storage-engines/architecture.md`.

- **The feature documentation names what the code has** (#658): every
  `docs/<feature>/` folder and the numbered guides were checked name by name
  against the code, and claims about behaviour the code lacks were removed.

### Fixed

- **A write or read that meets a leadership transfer is `503`, not `500`**
  (#775).
- **A concurrent user update is a retryable `ErrConflict`** on every transport
  (#938).
- **A tenant replica starts when its barrier unseals**, not on the next poll.
- **Client trust:** a chain is refused as unavailable, not invalid, while a
  trust read fails, and a loaded Key B set survives a Key B read that times
  out.
- **The `cors` config block reaches the REST server** (#658).
- **The sealed-window listener closes the connection that ends its window**
  (#1031).
- **`transport.BuildTLSConfig` returns a copy of the caller's `tls.Config`**
  instead of changing it (#949).
- **An isolation-policy change refusal is `failed-precondition`** (#658).
- **RAG ingest, delete and config keys match `docs/rag`** (#658).
- **Every storage sync is timed, and a synced vector batch is durable**
  (#905).

## [0.51.0-alpha] - 2026-10-21

### Added

- **A FIPS-approved P-256 bootstrap suite beside Noise NK** (#118). The anchor
  advertiser and the member join listener serve `TSBOOT1_P256_AESGCM_SHA256`
  as well as Noise NK, and log and expose both public keys. A node in FIPS
  140-3 mode holds no Noise key and enrolls over P-256 only; outside it, a
  joiner holding both keys uses Noise. New: `qrdbd --bootstrap-p256-key` and
  `--bootstrap-join-p256-key`, config `bootstrap.p256_key` and
  `bootstrap.join_p256_key` (validated), and the P-256 enrollment surface
  re-exported from the SDK. **Breaking (wire):** the CSR join exchange now
  opens with the `version || suite` header, so every member upgrades before
  a node joins through a changed listener. See
  `docs/clustering/architecture.md`.

- **ABAC condition values may be RFC 8259 quoted strings** (#756, #900; #819,
  #945). A condition value is parsed as a JSON string, so quoting is the one
  standard escape.
- **A workload SPIFFE ID names its tenant as the trust domain** (#804; #885),
  and `DelegatedContext` presents a tenant-issued delegation in-process (#873;
  #887).
- **Per-node tenant barrier init through the `BarrierKeyProvider`**, created as
  passphrase from the start (#683; #812).
- **gRPC-Web for hosted modules** served through the gRPC server's own handler
  (#808; #831), and a `HandleOptional` route mode on `ModuleHTTPRegistrar`
  (#783; #786).
- **Users suspend and unsuspend on every transport and the CLI**, with typed
  refusals on QUIC and MCP (#801, #807; #815).
- **RBAC: own permissions, endpoint `Authorize`, the network `RBACClient` and
  the CLI** (#753; #793); device-onboarding and OTA activate/confirm
  permissions, carried by the built-in Admin (#737; #825).
- **`sdk/go` re-exports**: `WithRegisterPINBackend` (#881; #884),
  `SystemBarrierInitRefusedError` and `ceremony.InitializeOptions` (#823;
  #830), `CompactionPolicy` (#820; #821), `IndexServicer` and
  `NewIndexService` (#928; #937), `ServiceBatchServicer` with the batch
  revision on every transport (#797; #800).
- **A tenant create that is undone leaves a tombstone** every serving node
  reconciles and acknowledges (#774; #829).

### Changed

- **A rolling seal migration hands the turn on only after the owner's Raft log
  caught up** (#803; #827). `TenantOwner` is decided by the hydrator, never
  carried from the verifier (#897; #932).
- **CI**: ci-template v1.22.1 and v1.23, one image job and cache volumes (#798,
  #810; #802, #817), the integration image built once per runner (#813; #814,
  #841), the main concurrency group per event (#933), and `deps-check`
  reporting a module release younger than 7 days instead of failing (#941);
  the Trivy gates take the database from the forge's bridge first (#770).
- **Dependencies**: roaring v2.30.0 (#926), genproto rpc (#888), quic-go
  v0.64.1 (#850), wails v2.17.0 (#790).
- **Docs and gates**: standards index and status banners (#752, #634; #757,
  #768), SPEC and ROADMAP rows (#734, #927, #928; #765, #782, #788, #931),
  deadcode deletion (#350; #811).
- **Trains**: #729, #746, #763, #766, #780, #789.
- **Tests**: coverage added across the packages (#832, #833, #834, #835, #836, #837, #838, #839, #840, #842, #844, #845, #846,
  #848, #849, #851, #852, #853, #854, #855, #856, #857, #858, #859, #860, #861,
  #862, #863, #864, #865, #866, #867, #868, #869, #870, #871, #872, #874, #875,
  #883, #889; #767), the coverage baseline refreshed (#886),
  and timing-dependent tests made deterministic (#773, #779, #784, #799, #816,
  #818, #822, #843, #877); the mocked Playwright tier serves the production
  bundle (#880, #892).

- **The isolation policy governs isolated tenants only** (**BREAKING**). A
  shared (`prefix`) tenant is never refused by the cluster floor or by its own
  `strict` opt-in, and a strict floor is not refused over shared tenants.
  `tenant.StrictRequiresDedicatedStorageError` is deleted; the tighten-only
  rule on the floor and on a tenant's policy is unchanged.
- **A home store for every isolated tenant, in every mode** (**BREAKING**).
  Every `barrier`, `volume` and `raft` tenant keeps its identity records --
  users, roles, factors, invitations, the DEK keyring -- in its own home store,
  whatever its isolation policy; the policy decides only operator verbs,
  backups and relay. Raft mode serves the home from a dedicated Raft group on a
  WAL under the tenant's key, and standalone from `tenant.LocalHomes`, a store
  under the tenant's barrier. A node whose copy has not caught up refuses with
  a retryable `lifecycle.TenantHomeCatchingUpError`. A tenant's policy may be
  tightened at any time; `tenant.StrictPolicyTightenError` and
  `tenant.StrictHomeUnsupportedError` are deleted. The system shard holds none
  of them, and the shared-tenant-key namespace that held an `operator-access`
  tenant's records is deleted with its store: `seal.NewTenantSharedKVStore`,
  `seal.TenantSharedNamespace`, and that namespace's range in
  `service.TenantKeyPrefixes` are gone. There is no migration: pre-release
  isolated tenants are re-created.
- **A `barrier` tenant is registered and restored the way a `volume` tenant
  is** (**BREAKING**). `seal.BarrierRegistry.RegisterBarrierTenant` replaces
  `RegisterTenantPooled` and `RegisterTenantPooledSystemManaged`, which are
  deleted. The tenant takes a generic-pool slot, its store moves from
  `{poolBase}/{tenant}` to `{DataRoot}/barrier`, and it gets a tenant-root
  record, so `RestoreVolumeTenants` brings it back at boot (system-managed
  unsealed, self-managed sealed). Registration now needs the system barrier
  unsealed. Existing `barrier` tenants are re-created.

### Fixed

- **Every node serves CA operations from the replicated Key A**, and Admin
  holds `tenant_barrier:status` (#812; #943).
- **Concurrent database creates write their metadata without colliding** (#882),
  and a failed create removes only the directory it made (#876).
- **Index writes surface old-value read faults** and batch duplicate puts
  (#847, #842).
- **`enable-encryption` authorizes per scope and forwards to the leader on every
  transport** (#791, #794; #787).
- **Tenant teardown sees shards still starting** (#778), and **a lease renewal
  cannot succeed for a lease its expiry is revoking** (#772).
- **The main build restored** after #885 (#935).

- **A value a barrier stores is no longer lost to a WAL epoch purge.** The
  system barrier and every isolated tenant's barrier double as their Raft
  host's WAL barrier. After a snapshot, go-quicraft purged every epoch below
  its WAL and snapshot floor, including the epochs of values the barrier had
  stored and of tenant root keys wrapped by `system-barrier`. Each unseal
  rotates the epoch, so after one restart and one snapshot those values stopped
  decrypting: an auto-unsealed tenant stayed sealed on that node after its next
  restart, and its home group never started there. A `StorageBarrier` now pins
  every epoch it encrypts under, and on unseal every epoch its blob still
  knows, so the purge leaves them alone.
- **A `barrier` tenant survives a restart.** Nothing restored a `barrier`
  tenant's barrier at boot, so after a restart every request for it failed
  `barrier is sealed`.
- **A user auth factor add or rotate naming an id already in use is refused
  instead of overwriting it.** The new factor's primary record was written
  unconditionally, so an add reusing an existing id replaced that factor's
  record (another user's included), and an add rolled back afterwards deleted
  it. The record is now reserved behind an absence guard: the add or rotate
  returns `ErrFactorAlreadyExists` (`409` / `ALREADY_EXISTS`) before anything
  is written, the original factor keeps authenticating, and a rollback only
  removes a record its own add created.
- **Reads wait out a new leader's first commit** (go-qrdb#668). A leader
  elected a moment ago refuses a ReadIndex with `quicraft.ErrReadIndexNotReady`
  until its no-op commits in its term; the Raft host adapter now holds a
  linearizable, follower or committed-membership read (`SyncRead`,
  `FollowerRead`, `SyncGetShardMembership`) through that refusal within its
  10 s leader-wait bound and the caller's deadline, as etcd holds a
  linearizable read through an election. An admin `member add` /
  `add-observer` right after a membership change that triggered an election
  no longer fails its member-identity read `UNAVAILABLE`. At the bound the
  read fails with `*raft.LeaderWaitError` wrapping the sentinel, still
  `UNAVAILABLE` / REST 503. Proposals are not held for it.
### Added

- **Transfer a shard's leadership to a named replica, on every transport.**
  `Client.Cluster().TransferLeadership(ctx, shardID, replicaID)` and
  `qrdb admin member transfer-leadership <replica-id> --shard-id N` ask the
  shard's leader to hand leadership to a voter and return once that voter
  leads, or fail within five seconds with `sdk.LeaderTransferTimeoutError`
  (a stopped or partitioned target). It is served on REST
  (`POST /membership/transfer-leadership`), gRPC/Unix (`TransferLeadership`),
  QUIC (`admin.TransferLeadership`), MCP (`qrdb_transfer_leadership`) and the
  embedded transport; any node serves it, a follower forwarding it to the
  leader. On the wire the timeout is the refusal `LEADER_TRANSFER_TIMEOUT`
  (`503` / `UNAVAILABLE`, metadata `shard_id`, `from_replica_id`,
  `to_replica_id`, `timeout_ms`), which the SDK rebuilds so `errors.As`
  matches on every transport; `transport.RequestError` gains `Reason` and
  `Metadata` to carry it. An unknown shard or replica is
  `sdk.UnknownShardError` / `sdk.UnknownReplicaError`; an observer, witness or
  removed target is `sdk.LeaderTransferTargetError`. Server-side it is
  `service.LeaderTransferrer`, implemented by `AdminService` on the same wait
  loop `RemoveMember` uses to move leadership off a leader it removes, and
  authorized as `admin:remove_member`. `LeaderTransferTimeoutError` gains
  `ToReplicaID`. See `docs/clustering/api.md`.
### Changed

- **Every direct dependency on its latest release** (go-qrdb#764), in the
  root, `sdk/go`, `sdk/go/gorm/conformance` and `test/clusterproof` modules
  in lockstep: `github.com/go-chi/chi/v5` v5.3.2 -> v5.5.0,
  `github.com/jeroenrinzema/psql-wire` v0.18.0 -> v0.20.0,
  `github.com/mark3labs/mcp-go` v0.45.0 -> v1.3.0,
  `github.com/quic-go/quic-go` v0.63.0 -> v0.64.0,
  `github.com/wailsapp/wails/v2` v2.12.0 -> v2.16.0,
  `golang.org/x/net` v0.60.0 -> v0.61.0,
  `golang.org/x/tools` v0.51.0 -> v0.52.0,
  `google.golang.org/genproto/googleapis/rpc` v0.0.0-20260810153831-ec0a7760b754
  -> v0.0.0-20261012220534-4341edf3f941,
  `google.golang.org/grpc` v1.83.2 -> v1.84.0 and
  `software.sslmate.com/src/go-pkcs12` v0.7.3 -> v0.7.4. `test/performance/qrdb`
  takes `github.com/quic-go/quic-go` v0.61.0 -> v0.64.0 and
  `google.golang.org/grpc` v1.83.0 -> v1.84.0. mcp-go v1 replaces
  `github.com/invopop/jsonschema` with `github.com/google/jsonschema-go` and
  `github.com/santhosh-tekuri/jsonschema/v6` in the indirect graph.
  - **grpc v1.84.0 is taken.** The 0.31.0-alpha hold for GO-2026-6443 no
    longer applies: `govulncheck` does not flag v1.84.0.
  - **psql-wire v0.20:** the PostgreSQL server's `HandleParse` takes the new
    `wire.Query` parse argument.
  - **mcp-go v1:** `tools/list` is narrowed to the tools the caller may call
    in an after-list hook rather than with `WithToolFilter`, because since v1 a
    tool filter also refuses `tools/call` ahead of the authorization
    middleware, so a refused call would answer "tool not found" without
    `AuthorizeMCP`'s `permission_denied`, its audit record or its rate-limit
    charge.
  - **Held:** go-enclave stays at v0.8.0-alpha in the root and `sdk/go`;
    v0.9.0-alpha is released, but go-quicraft, go-objstore and go-truststrap
    have not yet released on it. `test/performance/qrdb` keeps go-qrdb and
    `sdk/go` at v0.7.0-alpha: its Dockerfile, the module's only build,
    replaces go-qrdb with the build context, and moving both to the newest
    tag waits on a build of the module against that tag's published source.

### Security

- **Go 1.27.2 → 1.27.3**, the release that fixes the `crypto/x509`
  vulnerability `govulncheck` reported as GO-2026-6809. The `go` directive of
  every module, the CI image's `GO_IMAGE` pin in `scripts/tool-pins.sh` and
  the Dockerfiles move to it. No dependency version changes.

## [0.50.0-alpha] - 2026-10-16

### Added

- **A one-time factor claim: a conditional auth factor add** (go-qrdb#655).
  `qrdb auth factor add` takes `--if-no-active-type` (enroll only when the
  user has no active factor of that type) and `--expected-version N` (enroll
  only when the user's factor set is at that version), and reports the
  committed factor-set version beside the record (JSON `factor_set_version`,
  omitted for an unconditional add); `qrdb auth factor list` prints it. The
  decision is one guarded write on a per-user factor-set record, committed
  with `PutIfAbsent` or `CompareAndSwap` on the exact bytes it was decided
  against, so of N racing callers exactly one lands. No new Raft opcode. A
  failed condition is `*auth.FactorConditionFailedError` (`errors.Is`
  `auth.ErrFactorConditionFailed`), classified `AlreadyExists`, and travels as
  a structured refusal, reason `FACTOR_CONDITION_FAILED` in the `qrdb.v1`
  domain, on REST, gRPC, Unix, QUIC and MCP; the SDK clients decode it back
  into the typed error on every transport, embedded included, and the SDK
  re-exports the condition surface. A contended factor-set commit is
  `Aborted`, and a conditional add over a store with no guarded write is
  refused with `ErrUserAuthFactorAtomicStoreRequired`. On a store with
  multi-guarded batches, Add, Remove and Rotate commit their records and
  indexes in one batch with the factor set, so a hard Delete landing first
  refuses the add with `FactorNotFoundError` instead of leaving indexes that
  name a deleted factor.
- **A hard Delete for a user auth factor, so a rolled-back add frees its
  credential** (go-qrdb#684). `Remove` revokes and keeps the factor's
  credential and SPKI claims, so a caller undoing an add it had just made
  could never bind the same certificate again. `Delete` removes the factor,
  its user index entry, every claim naming it and its place in the factor set
  in one multi-key guarded batch (one Raft proposal), after which the same
  credential binds to the same user again. It refuses a revoked factor
  (`ErrFactorRevoked`, `410` / `FAILED_PRECONDITION`), so no revocation can be
  undone, needs `force` for the user's last active factor as `Remove` does,
  and returns not-found for a factor already deleted. Exposed as REST
  `POST /v1/users/factors/delete`, gRPC/Unix `UserAuthFactorService/Delete`,
  QUIC `userauthfactor.Delete`, MCP `qrdb_user_authfactor_delete`, the SDK's
  `UserAuthFactorClient.DeleteAuthFactor` on all six transports, and
  `qrdb auth factor delete <factor-id> [--force]`; it carries the
  `user_authfactors:add` permission and is audited as `delete`. The tenant
  barrier's encrypted store now implements the multi-key guarded batch, so
  Delete commits there as it does on the plaintext store.
- **Replica state hashes.** A node reports, for every replica it runs
  (isolated tenants' included), the applied index and a SHA-256 digest of the
  state machine at exactly that index, answered locally without a proposal:
  two replicas of a shard at the same applied index must report the same
  hash, on every storage backend. Exposed as REST `GET /admin/state-hashes`
  (repeatable `shard`), gRPC/Unix `GetStateHashes`, QUIC
  `admin.GetStateHashes`, MCP `qrdb_state_hashes`, the SDK capability
  `transport.ReplicaStateHasher` on all six transports, and
  `qrdb admin state-hash [--shard N]...`. Gated by the new `admin:state_hash`
  permission (system tenant only), granted to the built-in Admin role. The
  digest is defined in `pkg/statedigest` and documented in
  `docs/clustering/`.
- **An embedder composes every key and identity seam through `sdk/go` alone**
  (go-qrdb#626). The SDK re-exports the CA-key types (`sdk.CAKeyProvider`,
  `sdk.CAKeyProviderFactory`, `sdk.CAKeyAdapter`) and the volume types
  (`sdk.VolumeKeyProvider`, `sdk.VolumeKey`). `node.WithCertProvider` replaces
  the built-in login CA with the embedder's own (`sdk.ClientCertProvider`), and
  `node.WithCertKeyProviderFactory` keeps it but signs each tenant's client
  certificates with a key the embedder holds; a node given both fails with
  `sdk.CertProviderConflictError`. `sdk.NewDatabaseDAO` and
  `sdk.NewDatabaseDAOTransactional` build DAOs over a named database and never
  fall back to the default store (`sdk.DAODatabaseStoreUnresolvedError`);
  `sdk.NewClientDAOFactory` resolves names through a client.
  `sdk.SpiffeID.URI` builds the tenant SPIFFE SAN that `sdk.TenantFromCert`
  parses, and refuses one that would not read back identical. A build gate
  compiles and tests an embedder module outside this one, `GOWORK=off`, that
  imports nothing but `sdk/go/...`.

- **A tenant delegates to an embedder's service principal** (go-qrdb#626).
  `qrdb ca delegate` (REST `POST /certs/delegate`, gRPC `IssueDelegation`,
  QUIC `cert.Delegate`, MCP `qrdb_cert_delegate`, and the SDK's
  `CertClient.IssueDelegation` on every transport) has the tenant's own login CA certify the
  principal's CSR as a short-lived client certificate (one hour by default, at
  most 24) naming the principal and the delegated permissions in a critical
  extension. The new `certs:delegate` permission is tenant-only and held by the
  built-in SO and Admin roles: the system tenant cannot issue one, nor can a
  delegated caller. The mTLS authenticator verifies the certificate on every
  transport -- issued by that tenant's CA, within its lifetime, not revoked,
  naming the principal the node set with `node.WithServicePrincipal` -- and the
  request holds the delegated scope intersected with the principal's own
  grants, re-verified when forwarded to the leader and audited to the tenant's
  trail on every use. A node with no service principal, `qrdbd` included,
  accepts none. The SDK re-exports the grant (`sdk.DelegationGrant`,
  `sdk.DelegationFromCertificate`, `sdk.DelegationCheckLifetime`). The
  extension is `tp-delegation`, OID `1.3.6.1.4.1.29377.101.24`.
- **`RetrieveCredentialBuffer` / `RetrieveCredentialVersionBuffer`** on
  `credentials.CredentialService`, and `CredentialRetrieveBuffer` /
  `CredentialRetrieveVersionBuffer` on `service.CredentialServicer` and on the
  SDK's `transport.CredentialClient` (all six transports). A retrieved
  credential travels as a go-enclave-backed `*secure.Buffer` the caller
  `Destroy`s, from the store through the service, the embedded transport and the
  `credential://` resolver (`secret.NewSecretValueFromBuffer`) to the CLI's
  output. The secret-bearing wire DTO of each transport is the one documented
  place bytes leave protected memory
  (`service.CredentialWireValue`; on the SDK side `transport.WireValue` /
  `transport.BufferFromBase64` decode REST and MCP responses directly into
  protected memory, and `transport.BufferFromWire` moves the protobuf-owned
  slice in for gRPC, Unix and QUIC); see
  `docs/credentials/architecture.md`, "Protected-memory boundary". The wire
  contract is unchanged (#22).
- **`make check-no-deprecated-credential-retrieve`** (Lint job): no Go file in
  the repository may reintroduce a deleted `[]byte` credential retrieval method
  declaration, and the unambiguous store-level removed selectors are blocked.
  Protobuf wire RPC methods with the same names are allowed because they return
  protobuf response messages, not credential heap slices (#22).
- **`lease.checkpoint_interval` (default `5m`, at most `1h`) and
  `lease.failover_grace` (default `5s`)** configure the lease checkpoints and
  the extra life a new leader gives every lease (see Changed).
- **`protocol.OpLeaseCheckpoint`**, gated by the `lease-checkpoint` cluster
  feature, replicates a batch of leases' remaining TTLs together with the
  applied Raft index they were sampled at. Each renewal through the log
  records its own applied index, and every replica discards a lease's
  checkpoint when a renewal applied after the sample, so a restarted replica
  never restores a TTL shorter than the holder's last renewal (#681).
- **One-entry range deletes for keyspace reclaim** (go-qrdb#457).
  `protocol.OpDeleteRange` removes every live key in `[start, end)` in one
  Raft entry, and `KVService.DeleteRange` proposes it only once the
  range-delete cluster feature gate is active for the shard, so a
  mixed-version cluster never applies an opcode an old replica would refuse.
  `storage.Storage` and `storage.Batch` gain `DeleteRange` on memory, file,
  pebble, objstore, partition and namespace. Both engines, every `sm_type`
  and the Pebble state machine apply it as the batch of single deletes over
  the live client keys in the range: one entry, one revision, index entries
  and lease bindings released, one watch DELETE per key in key order, as etcd
  reports a DeleteRange. Vector records are left in place;
  `SimpleStateMachine` refuses the opcode, and bounds that are not a range
  are `statemachine.InvalidRangeError`, not an apply halt. Dropping a
  database reclaims each owned prefix in range deletes of at most 1,024 keys,
  falling back to per-key deletes on a shard that has not activated the gate.
  A buffered batch's reads and iterators now cost the range rather than every
  pending op.
- **`dao.SecretBytes`, an entity field held in protected memory**
  (go-qrdb#633). It holds key material in a go-enclave `secure.Buffer`.
  MessagePack encodes it as one raw bin item into an exact-size record and
  decodes the item straight into a new protected buffer; JSON, YAML, TOML,
  BSON, CBOR and ProtoBuf are refused at codec build (`CodecCreationError`
  wrapping `SecretCodecUnsupportedError`). The type refuses `encoding/json`
  and `TextMarshaler` and redacts under `fmt`; a secret field cannot be
  indexed (`SecretFieldIndexedError`) and gets no SQL bridge column. The DAO
  destroys the secrets of every entity it decodes for its own use and does
  not hand back, and every buffer a failed decode allocated. The SDK
  re-exports the type, constructors and errors.
- **A rebalance reports why each failed move failed.** `ExecuteRebalance`
  records one `RebalanceMoveFailure` per failed move -- shard, source,
  destination, the dedicated-group phase that failed and the cause -- and
  REST, gRPC, QUIC, Unix, MCP, embedded, the SDK and the `qrdb` CLI carry it.
- **The SDK re-exports the ABAC condition parser** (go-qrdb#755, go-qrdb#760).

### Changed

- **The `(qrdb.v1.permission)` method option is public** (**BREAKING**,
  go-qrdb#626). Its file moved to `qrdb/v1/permission.proto` under the sdk/go
  module's `proto/` directory, with Go package
  `github.com/jeremyhahn/go-qrdb/sdk/go/proto/qrdb/v1`, so a service module
  built outside go-qrdb can annotate its RPCs and be authorized by the shared
  interceptor. The option's name and field number (51000) are unchanged, and so
  is every annotated RPC. `pkg/api/grpc/proto/qrdbv1/authz.proto` and its
  `E_Permission` are deleted; import the SDK package instead. `make proto`
  puts `sdk/go/proto` first on the include path.
- **Lease keep-alive is answered by the leader from memory** (go-qrdb#275).
  A renewal no longer waits on a Raft log write: the leader renews in memory,
  following etcd's lessor. Every `lease.checkpoint_interval` (default `5m`)
  it checkpoints the remaining TTL of each lease longer than the interval, and
  it checkpoints every lease before a scale-down or replica move transfers
  leadership away. A renewal of a checkpointed lease goes through the log and
  clears the checkpoint. A new leader first applies its predecessor's log, then
  counts every lease from its checkpoint (or full TTL) plus the failover grace;
  a restarted replica counts from the checkpoint too. A new leader therefore
  never grants a lease more than its holder was renewed to, plus the grace.
  When the leader cannot answer locally, the replicated renewal is answered
  from its apply result rather than a second linearized read. The etcd
  `LeaseKeepAlive` stream answers TTL 0 for a lease that is gone, as etcd does.
  Until the `lease-checkpoint` gate is open, every renewal, the leader's
  included, still goes through the log. Only the leader answers a held
  lease's remaining TTL: a follower refuses with `LeaseTTLNotLeaderError`,
  which gRPC, Unix, QUIC, MCP, REST and embedded forward to the leader and the
  etcd listener answers Unavailable.
  `TestRaft_LeaseKeepAliveLatencyTail` holds p90/median under 3x on real disk,
  including under a disk-sync writer; test/performance/lease-keepalive.md
  records the before/after table.
- **The file backend encrypts at rest through the barrier's encfs**
  (go-qrdb#630). Every node composition opens it as it opens pebble: the
  standalone daemon, the SDK node, the embedded server and a raft replica's
  `sm_storage: file`. Record files and the commit journal are ciphertext.
  Format version 3 records the at-rest posture in the marker (`3/plaintext` or
  `3/encrypted`) and a store is refused under the other posture with
  `AtRestMismatchError`; under a sealed barrier record reads and writes fail
  with `seal.ErrSealed`. A node encryption migration re-encrypts a file store
  in the same journaled swap as a Pebble one (`database.ReencryptFileStore`,
  `VerifyReencryptedFileStore`), and qrdbd's raft system migration names its
  stores by sm type and sm storage, so `enable-encryption` no longer fails on
  a storage-generic or in-memory state machine. The seal-time refusal of
  disk/fast over file `sm_storage` is deleted
  (`statemachine.SMStorageUnencryptedError`, qrdbd's
  `SealedSMStoragePlaintextError` and the SDK mapping). `atrest.Encryptor`
  gains `Wrap(base)`.
- **Tenant onboarding is bounded in the service, the same on every
  transport.** `OnboardTenant` runs under `DefaultTenantOnboardingTimeout`
  (30 s; `TenantOnboardingDeps.Timeout` overrides it), a caller's sooner
  deadline still applies, and the audit outcome is recorded under the
  caller's context. REST ran onboarding under a 5 s bound sized for one read,
  so a loaded cluster ended onboardings part-way and the undo retired the
  tenant id; gRPC, QUIC and MCP had no bound. Every other REST tenant
  endpoint, barrier init included, now passes the caller's deadline through
  instead of a fixed 5 s, as gRPC, QUIC, MCP and the Unix socket do.
- **A stalled database create is answered within the server's deadline.**
  `CreateWithCredentials` and `CreateForTenantWithCredentials` run under
  `DefaultDatabaseCreateTimeout` (10 s, the replica-forwarding bound). A
  create that runs out of it is rolled back -- quota reservations, the
  half-created database and its placement -- and refused as
  `DatabaseCreateTimeoutError`, 503 / `UNAVAILABLE`, on every transport; a
  caller's own deadline or cancellation is returned as it is. `errclass`
  classifies go-quicraft's `ErrTimeout` as DeadlineExceeded, `ErrCanceled` as
  Canceled, and a temporary refusal (busy, dropped, shard not ready, no
  leader yet) as Unavailable.
- **Key material stays in protected memory through the unwrap and the SO PIN
  check** (go-qrdb#145). A new tenant DEK is generated into a go-enclave
  buffer and wrapped from it; a completed Shamir quorum hands the
  reconstructed key to the unwrap in place, and the SO gate hands the
  verifier the opened PIN in place, with no heap copy. The `pkg/secure`
  borrow guard now reports only a goroutine reentering a buffer it is
  borrowing.
- **Auth lookups with no fallback outlast a leader election** (go-qrdb#654).
  The user lookup and the RBAC role resolve keep their 500 ms read bound where
  the system tenant's stale-read fallback can answer, and take 5 s for every
  other tenant, so a ReadIndex slowed by an election no longer becomes a 503.
- **Lease-read linearizability documented against go-quicraft's monotonic
  lease** (go-qrdb#678). Lease reads are linearizable, with no stale-read
  window, while replica clock rates differ by under 1 %; wall-clock steps do
  not affect the lease; near or after expiry the read falls back to the
  heartbeat round. `docs/deployment` requires chrony on every replica and
  identical `ElectionRTT` and `RTTMillisecond`.
- **Listener and Raft configuration docs read from the code** (go-qrdb#671,
  go-qrdb#669). `docs/deployment/user.md` carries the one canonical listener
  table and a 3-voter firewall recipe, and `docs/configuration` documents
  every `qrdbd start` Raft flag and `raft.*` key. Corrected along the way:
  `snapshot_entries: 0` selects go-quicraft's default of 100000 rather than
  disabling snapshots, and the enrollment listener runs on every admitted
  member, not only the first node.
- **Docs clean-up** (go-qrdb#735, go-qrdb#745). The docs bodies, stamps,
  records, legacy layout and dated rulings are cleaned up.

### Fixed

- **Tenant create answers only once this node trusts the certificate it
  returns** (go-qrdb#740). A CSR-issued admin certificate is signed by the
  tenant's new login CA. Before answering, the create rebuilds the answering
  node's client-certificate trust, so the next TLS handshake on that node
  admits the certificate. Other nodes pick it up within their 2-second rebuild
  interval. If the rebuild fails, the failure is logged and the create still
  answers; the next periodic rebuild admits the certificate.

- **Shared hooked TLS config no longer leaks one listener's tuning into
  another** (go-qrdb#741). `clienttrust.ReassertListenerTLS` applies a
  listener's ALPN and version floor to a clone of what the dynamic hook
  returns, never to the returned config itself, so listeners sharing one
  hooked config (native QUIC ALPN among them) no longer race to overwrite each
  other's settings.
- **A user auth factor add or rotate naming an id already in use is refused
  instead of overwriting it.** The new factor's primary record was written
  unconditionally, so an add reusing an existing id replaced that factor's
  record (another user's included), and an add rolled back afterwards deleted
  it. The record is now reserved behind an absence guard: the add or rotate
  returns `ErrFactorAlreadyExists` (`409` / `ALREADY_EXISTS`) before anything
  is written, the original factor keeps authenticating, and a rollback only
  removes a record its own add created.
- **Reads wait out a new leader's first commit** (go-qrdb#668). A leader
  elected a moment ago refuses a ReadIndex with `quicraft.ErrReadIndexNotReady`
  until its no-op commits in its term; the Raft host adapter now holds a
  linearizable, follower or committed-membership read (`SyncRead`,
  `FollowerRead`, `SyncGetShardMembership`) through that refusal within its
  10 s leader-wait bound and the caller's deadline, as etcd holds a
  linearizable read through an election. An admin `member add` /
  `add-observer` right after a membership change that triggered an election
  no longer fails its member-identity read `UNAVAILABLE`. At the bound the
  read fails with `*raft.LeaderWaitError` wrapping the sentinel, still
  `UNAVAILABLE` / REST 503. Proposals are not held for it.
- **A submitted Shamir share no longer outlives its quorum round**
  (go-qrdb#79). Shares were already held in go-enclave protected memory, but
  an expired round was destroyed only when the next share for the same
  operation arrived, and nothing destroyed a round on seal, close, unseal or a
  change of share set. Now a round arms an expiry timer when it opens. Seal and
  close destroy every open round. Redistributing a domain (rekey, revocation,
  recovery-key generation) or deleting its recovery keys destroys the rounds
  opened on the old share set. An unseal destroys the unseal and recovery
  rounds it ends. Admission re-checks the barrier's state under the round
  lock, so a submission racing a seal or unseal cannot open a round after the
  sweep.
- **A rebalance move or scale-down no longer strands a two-voter group without
  quorum.** The departing replica was dropped from the placement before its
  Raft removal committed, so its node's reconciler stopped and purged it while
  the removal still needed its vote. The surviving leader then lost quorum,
  and the move failed at `remove-node`. Now the replica is evicted from Raft
  first, after a compare-and-swap confirms the caller's placement snapshot.
  The new `placement.Store.RemoveReplica` then drops it from the placement.
- **A forwarded admin write no longer waits past its deadline for a leader.**
  The replica-forwarding leader lookup in QUIC `ForwardToLeader`, gRPC
  `forwardAdminWrite` and MCP `forwardToLeader` is bounded by the forward
  timeout; when no leader is known in time the write is refused with
  `AdminCredentialNotLeaderError`.
- **Cluster seal and clear survive a leader election** (go-qrdb#654).
  `clusterseal` retries the seal and clear writes within a 15 s
  `SettleBudget`, adopts a write that timed out but committed, and retries the
  acknowledgement within `AckTimeout`.
- **A row-lock deadlock aborts exactly one victim per cycle.** Two requests
  on one cycle could both find it and both abort, so the survivor of a
  two-party deadlock failed with 1213 too. Each wait-for edge now carries the
  create revision of the waiter record that made it, and a requester is the
  victim only of a cycle whose newest edge is its own, so a cycle has exactly
  one victim, as in InnoDB; an older request waits for the victim to release.
- **A sealed node's volume reporter closes with a typed refusal.**
  `volume.Reporter.Close` on a sealed node returns
  `*volume.NodeSealedError{Op: "revoke"}`.
- **The boot tenant restore waits out a settling system shard.** Each of
  `RestoreVolumeTenants`' system-shard reads was attempted once, so a read
  lost to the restart's leaderless window left a tenant unrestored on that
  node until the next restart, or restored an auto-unseal tenant sealed.
  `BarrierRegistry` gains `WithStartupReadRetry`, wired to
  `server.WhileSystemShardSettling`, so each read is retried within that
  bound on the settling class only.
- **Keyspace reclaim reports a non-MVCC system shard's index refusal once**
  (go-qrdb#457).
- **The CLI watch integration tests wait for the event, not a fixed sleep**
  (go-qrdb#750). `startCLIWatch` runs `qrdb watch --timeout 0`, issues probe
  puts until the watch reports one, and waits for the expected event under a
  30 s deadline, reading the watch output through a mutex-guarded buffer.
- **The RBAC permission resolve waits out a system-shard election, as the
  user lookup does** (go-qrdb#749). `resolvePermissionSet` made one bounded
  attempt and, for every tenant but the system tenant, failed at once with
  `*ResolveError` on a not-leader or leader-unknown error, so a request that
  authenticated through an election was then refused by the authorizer. The
  settle loop moves out of `AuthMiddleware.lookupUser` into one helper,
  `service.SettleLeadership`, used by both. Each attempt keeps its 500 ms bound
  and the system-tenant StaleRead fallback; a definitive `RoleNotFoundError`
  or `ValidationError` is still final at once.
- **A REST tenant create is bounded by the server's WriteTimeout, not the 5 s
  read bound** (go-qrdb#749). The whole provisioning chain ran under
  `tenantRequestTimeout`, so on a loaded standalone node the CA-key proposal
  could time out as 503 and the create was undone. Create is now bounded by
  `tenantProvisionTimeout`, the server's WriteTimeout (30 s, now a named
  constant).
- **A node joins the trust pool's refresh loop before closing the storage it
  reads** (go-qrdb#749). `clienttrust.Pool.Run` ran as a bare goroutine that
  `Close` only cancelled, so a refresh in flight could still hold a Pebble
  iterator when the storage closed, and Pebble panicked with "element has
  outstanding references"; a migration rebuild never stopped the old loop.
  The loop now runs on a cancellable context with a done channel, and `Close`,
  `teardownDataPlane` and a failed wiring pass stop it and wait before the
  storage is closed.

### Removed

- **Breaking: the `[]byte` credential retrieve methods** (#22):
  `credentials.CredentialService.RetrieveCredential`,
  `RetrieveCredentialVersion`, `service.CredentialServicer.CredentialRetrieve`,
  `CredentialRetrieveVersion`, and the SDK transport `CredentialClient` /
  transport implementations' `CredentialRetrieve` and
  `CredentialRetrieveVersion`. Go callers use `RetrieveCredentialBuffer`,
  `RetrieveCredentialVersionBuffer`, `CredentialRetrieveBuffer`, or
  `CredentialRetrieveVersionBuffer` and `Destroy` the returned
  `*secure.Buffer`. `secret.CredentialRetrieveFunc` and
  `secret.CredentialRetriever` take the Buffer form. REST, gRPC, QUIC, MCP and
  Unix wire operations keep their endpoint/RPC/tool names and payloads; only
  the exported Go byte-returning surface is removed.

## [0.49.0-alpha] - 2026-10-15

### Added

- **`node.WithIdentityHydrator`.** A composer supplies the
  `embedded.IdentityHydrator` the unified-login ceremony uses to turn the
  resolved account and verified factor into the session identity, for example
  to make the login Subject the immutable user id (`Principal.ID`). Without it
  the Subject stays the username. Only the login ceremony is affected; network
  requests still take their identity from the authentication chain. See
  `docs/identity-rbac/api.md`, "Login identity".
- **Numeric secondary indexes with a documented total order** (go-qrdb#35).
  A numeric index stores exact, order-preserving decimal terms
  (`index.EncodeNumericValue`): `-Inf` < negatives < `0` < positives < `+Inf`,
  integers and floats in one order, `-0` equal to `0`, no rounding of integers
  beyond 2^53, and `NaN` never indexed and refused as a scan bound
  (`index.InvalidNumericBoundError`, `InvalidArgument` on every transport). The
  encoding is versioned (`index.NumericEncodingVersion`,
  `IndexConfig.EncodingVersion`): a stale definition is refused with
  `index.StaleEncodingError`, every create clears and backfills, and a
  same-owner re-create asking for a different value type rebuilds the index.
  `qrdb index create --numeric` and `kvstore.EntityIndex.Numeric` request it;
  the DAO sets it for an indexed integer or float field without a custom
  marshaler, and numeric single-field indexes return to the query planner, so
  `Age >= 2` is an index scan that returns the records holding 2 and 10.
  `planner.Plan` gains `RangeStartExclusive`/`RangeEndExclusive`, which the DAO
  honours exactly. Tests: a property test against a brute-force filter, and
  `--numeric` range scans on every transport in `TestIndex_CLI_PerProtocol`.

- **Tenant barrier init proves possession of the first admin's key.** Init is
  now two calls on one TLS 1.3 connection: `init/begin` returns a single-use
  nonce bound to the connection's exporter (five minutes), and `init/complete`
  carries a signature over the tenant, nonce and exporter by the key whose SPKI
  the pending first admin records (`pkg/initproof`). A replayed, expired,
  other-connection or wrong-key proof is refused with `FirstAdminProofError`
  (401 / `UNAUTHENTICATED`). It runs on REST, gRPC, the Unix socket, QUIC and
  MCP; the embedded transport is in process and asks none. The SDK takes a
  `crypto.Signer` (`TenantBarrierInitRequest.Signer`), and `qrdb tenant
  barrier-init` signs with `--key-file`. **Breaking:** the REST route
  `POST /v1/tenants/{id}/barrier/init`, the gRPC `TenantBarrierService.Init`
  RPC, the QUIC `tenant.BarrierInit` method and the `qrdb_tenant_barrier_init`
  MCP tool are replaced by `init/begin` + `init/complete`; the TypeScript
  `tenants.barrierInit` throws `DesktopOnlyError` where the node asks a proof a
  browser cannot compute.

- **DPoP (RFC 9449) request metadata for embedder authenticators**
  (go-qrdb#646). The request context carries the `DPoP` proof and the method
  and URI it is bound to as `auth.DPoPRequestFromContext(ctx)` (SDK:
  `sdk.DPoPRequestFromContext`), a `DPoPRequest{Proof, Method, URI}`, on REST
  and MCP over HTTP/2 and HTTP/3 (the `DPoP` header) and on gRPC and
  gRPC-over-UDS (the `dpop` metadata). qrdb only carries the data: it does not
  verify proofs, track `jti` replay or issue nonces. Native QUIC, embedded and
  MCP stdio requests never carry it.

- **A `file` data directory can be shared between processes** (go-qrdb#632).
  Writes are serialized across processes by an advisory lock on
  `.qrdb-file-lock` in the directory, so the guarded writes (`SaveIfAbsent`,
  `CompareAndSwap`, `CompareAndDelete`) stay atomic across them. A new optional
  `storage.GuardedStorage` interface (re-exported by the SDK) offers
  `CompareAndSwap(ctx, key, expected, next) (bool, error)` on raw bytes, and
  Memory, File, Pebble and ObjStore implement it; a mismatch or an absent key
  is `false` with a nil error and never creates the key. Only the File engine's
  guarantee holds across processes. The `file` engine also implements
  `storage.Exclusive`, which runs a read-check-write under that lock.

### Changed

- **Sealing a node's barrier pauses every shard on it first, and unsealing
  resumes them.** After the seal record commits and before the keys leave
  memory, `service.SealWholeNode` pauses every shard on the node, the system
  shard included (`Host.PauseShard`), so no log write runs against a sealed
  barrier; the unseal restores the keys, then resumes the shards
  (`Host.ResumeShard`), and the node rejoins in place. No new Raft opcode; the
  pause is not persisted, so a restarted node comes up unpaused. A paused
  shard's `ErrShardPaused` answers Unavailable on every transport. From the
  pause until the resume no tenant database shard or volume tenant host starts
  on the node: a start in that window is refused with `seal.ErrSealed`.
  `BarrierRegistry.SetSystemBarrier` moves every observer of the replaced
  barrier to the new one, so a node sealed after an encryption migration still
  resumes its shards and unlocks its volumes when the keyed barrier unseals,
  and locks its volumes when it seals. The
  node-barrier acknowledgement wait now reads the acknowledgements from the
  node's own copy of the system shard (`clusterseal.KV.ScanStale`) and bounds
  the whole wait, its own acknowledgement write included, by the
  acknowledgement timeout: the last node to look no longer waits on a quorum
  that its peers' pause has removed. See `docs/barrier-seal/architecture.md`.

- **A sealed node serves the unseal call and its initialized flag, and nothing
  else.** On REST, gRPC, Unix, QUIC, MCP and embedded, every other call —
  health, node status and ceremony status included — is refused with one typed sealed refusal: REST `503
  {"code":"SEALED"}`, gRPC/Unix `UNAVAILABLE` with `ErrorInfo{qrdb.v1,
  SEALED}`, QUIC `StatusUnavailable` with the same `ErrorInfo`, MCP
  `UNAVAILABLE`/`SEALED`, and embedded `*BarrierSealedError`. The SDK maps
  each to `transport.BarrierSealedError` (`transport.IsBarrierSealed`), the
  CLI prints `transport: barrier sealed`, and `qrdbdharness` readiness waits
  read it as up and sealed. First-boot verbs are served only before
  enrollment closes; the `ENROLLMENT_CLOSED` 403 is replaced by the sealed
  refusal. A sealed barrier runs only the `unseal` and `recover` quorum
  rounds: `seal`, `root-token` and `rekey` are refused with `ErrSealed`. A
  client certificate authenticates the unseal caller and grants nothing past
  the seal. See `docs/seal/sealed-window.md` (#695).

- **Barrier status is a sealed node's one status answer: sealed, and whether
  it is initialized.** Before the first boot and after it, a node whose
  barrier is sealed answers barrier status on every transport's sealed
  surface with `sealed: true` and the new `initialized` flag
  (`BarrierStatusResponse.initialized`, field 8 on gRPC; `Initialized:` in
  the CLI's text output) and nothing else — no strategy, share counts,
  `initialized_at` or seal record. The barrier reads the flag once at boot
  and caches it (`seal.StorageBarrier.InitializedCached`), so the answer
  reads no store. A node held or cut off by a cluster seal still refuses
  status; ceremony status stays refused while sealed
  (`sealedwindow.VerbBarrierStatus`, `ListenerOnly`). An unsealed node
  reports `initialized: true`. gRPC, Unix and QUIC now report an
  uninitialized barrier's `initialized_at` empty, as REST and MCP do, instead
  of the zero time (`grpc.BarrierStatusToProto`). The desktop GUI's
  needs-init check reads the flag (#659, #695).

- **go-quicraft v0.22.0-alpha in the root, SDK, GORM conformance,
  cluster-proof, qrdb benchmark and kvcompare modules.** The root module, `sdk/go`,
  `sdk/go/gorm/conformance` and `test/clusterproof` move from go-quicraft
  v0.20.0 to v0.22.0-alpha, the planned 0.21.0-rc.3, and
  `test/performance/qrdb` and `test/performance/cmd/kvcompare` move
  go-quicraft/test/performance to v0.22.0-alpha. The release carries the
  leader-executed request primitive, holds a shard while it is sealed instead
  of latching it failed, and fails a forwarded read the leader cannot serve yet
  with `ErrReadIndexNotReady` instead of holding it until the caller's
  deadline. MVS also raises prometheus/client_golang to v1.25.0,
  klauspost/compress to v1.20.1 and golang.org/x/{crypto,sync,sys,term,text}.
  No qrdb code changes (#659).

- **BREAKING: the login Subject is the immutable user id, not the username**
  (go-qrdb#670). Once a caller resolves to an account, `Identity.Subject` is
  that account's user id in decimal (`auth.UserSubject`), set only by
  `Identity.BindUser`, on every transport — REST, gRPC, QUIC, the Unix socket,
  MCP and embedded — so renaming the account or changing its email leaves
  every Subject reader naming the same caller. The username-as-Subject path is
  deleted, with no flag or alias. What a deployment sees change: the audit
  `actor` and the enroller fields on ceremonies, barriers and tenants record
  the id; rate-limit buckets and lockout counters are keyed by it; ABAC
  `subject.id` is the id and `subject.name` is the account name
  (`Identity.AccountName()`), so a policy matching usernames on `subject.id`
  must move to `subject.name`; SQL grantees and the embedded driver's SQL user
  stay the account name. An unresolved Subject that is id-shaped is refused
  with `middleware.UnresolvedUserSubjectError` (401 / `UNAUTHENTICATED`), so an
  embedder's bearer authenticator must set `UserID`. Embedded `FinishLogin`
  binds the Subject after its hydrator runs, which can no longer choose it, and
  refuses a principal with no id. `node.WithIdentityHydrator` injects that
  hydrator. A certificate's CN still selects the account by username, so after
  a rename the holder re-certifies under the new name; a binding over the same
  key carries over. Tests: `TestUsers_CLI_SubjectIsUserID_PerProtocol`, on every
  transport through the real CLI and in process for embedded.

- **Breaking: tenant barrier init resolves its strategy through
  `seal.BarrierKeyProvider`.** Each node asks the provider for the strategy
  sealing its own key for a tenant barrier, naming the barrier, its node
  identity, the strategy ID, an optional key reference and the epoch. Inject a
  provider with `server.WithBarrierKeyProvider`, `lifecycle.Infra.BarrierKeyProvider`
  or `sdk.WithBarrierKeyProvider`; it wins over the `StrategyRegistry`, and the
  default `seal.NewBarrierKeyProvider` maps the registry onto the seam.
  `tenant.WithStrategyRegistry` is deleted (use `tenant.WithBarrierKeyProvider`),
  and `TenantBarrierInitWithStrategy` takes variadic
  `tenant.TenantBarrierInitOption`s. See `docs/barrier-seal/architecture.md`,
  "The barrier key seam".

- **Deleting a role takes it away from everyone who holds it** (go-qrdb#697).
  `qrdb roles delete` strips the role from every account holding it before it
  removes the role, so no holder needs unassigning first. From the moment the
  delete starts the role grants nothing, and a role-assignment write or an
  update to the role is refused (`role.RoleDeletingError`, 400 /
  `INVALID_ARGUMENT`). A delete that fails part way leaves the role in place,
  marked (`role.HolderRevocationError`); running the same delete again
  finishes it. A role service that cannot reach the user store refuses the
  delete (`role.HolderRevokerRequiredError`, 501 / `UNIMPLEMENTED`), and holders
  reassigned through every sweep fail it with
  `user.RoleRevocationIncompleteError` (409 / `ABORTED`; retry).
- **The built-in User role may change and remove its own passwords.** It now
  carries `passwords:write` and `passwords:delete` beside `passwords:read`,
  `passwords:lock` and `passwords:unlock`, self-scoped like every
  `passwords:*` permission: the authorizer confines them to the caller's
  tenant.
- **An unconfirmed cluster seal state is no longer reported as sealed.** A node
  cut off from the seal records — its lease lapsed, for example while the
  system shard has no leader — or that has not yet read them refuses with
  `SEAL_STATE_UNCONFIRMED`, which a client retries like any `UNAVAILABLE`:
  REST `503` with `Retry-After: 5`; gRPC and Unix `UNAVAILABLE` with
  `ErrorInfo{reason: "SEAL_STATE_UNCONFIRMED", domain: "qrdb.v1"}`; QUIC
  `wire.StatusUnavailable` with that reason; MCP code `Unavailable` with that
  reason; embedded `*clusterseal.SealStateUnconfirmedError`; MySQL error 1105;
  PostgreSQL SQLSTATE `57P03`; etcd `UNAVAILABLE`. `SEALED` now means a cluster
  seal holds the node or its node record is sealed.
- **An unsealed `qrdbd` node waits for its system shard's leader for as long as
  it runs.** Each node unseals on its own, so a quorum can form long after one
  node is ready; the node logs `still waiting for a Raft leader` with the
  `no raft leader after ...` detail once every 120 election timeouts and serves
  nothing until a leader is known, instead of failing to start. SIGINT/SIGTERM
  still ends the wait at once.
- **`qrdbd` keeps every port it serves on bound across the sealed-window
  handover** (go-qrdb#693). The REST listener, the HTTP/3 socket beside it,
  gRPC, the QUIC transport and MCP are bound once, before the first server
  starts, and handed to the sealed-window listener, the node's own server and
  every server an in-process encryption migration rebuilds, so no other
  process or outbound connection can take a port between them. A failed bind
  is `*ServerSocketBindError`, naming the protocol, network and address.
  Embedders get the same with the new `server.WithQUICPacketConn` and
  `server.WithMCPListener`.
- **A `qrdbd` node that leads the system shard hands leadership to a peer**
  before an in-process encryption migration recycles it, and closes its Raft
  host once another replica leads or 5 seconds pass, so its peers' system-shard
  reads — trust resolution in the TLS handshake and each request's revocation
  check — are not left forwarded to a node that no longer answers.
- **Concurrent writes on a standalone node share one commit.** Proposals that
  arrive while an `Update` is in progress are applied together in the next
  one, and each is acknowledged only after the `Update` that applied it
  returns. The `file` engine writes a batch's distinct keys concurrently, at
  most 32 at a time, so their fsyncs overlap.

### Fixed

- **A dedicated tenant can be created with its first admin again.**
  `tenant create` for a self-managed `barrier`, `volume` or `raft` tenant
  failed with `tenant: barrier is sealed`. The admin's CSR was sent to the
  tenant's own login CA (Key B), whose records live behind the barrier that
  admin has yet to initialize. Such an admin is now signed by the cluster CA
  (Key A) through `ceremony.AdminCSRSigner`, the way the system admin is
  signed at first boot. The certificate's first OU names the tenant, so it
  authenticates only as that tenant's admin. Composers can inject their own
  signer through `bootstrap.Dependencies.PendingAdminSigner` /
  `service.TenantOnboardingDeps.PendingAdminSigner`. A node serving
  bring-your-own TLS has no cluster CA. It still signs a system-managed
  tenant's admin with Key B, which serves from creation. It refuses a
  self-managed tenant's CSR before the tenant is created, as first boot does
  (`ceremony.AdminCredentialRequiresCAError`, `FailedPrecondition`). Before
  this, the CSR failed after the tenant was written, and the undo retired
  the id. Bring the admin's own certificate (`--admin-cert`,
  `--trust-anchors`) instead. `prefix` tenants are unchanged.
- **A membership change no longer fails because it arrived mid-election.**
  `admin member add`, `add-observer`, `promote` and `remove` were refused
  with `not leader for shard N, leader unknown` -- a refusal no transport can
  forward -- whenever the shard was electing. They now wait, up to 10s, until
  the node knows a leader, then propose or forward as before; no leader in
  that time is `*service.ShardLeaderTimeoutError` (Unavailable, HTTP 503) with
  nothing proposed. The change is never proposed twice: one that timed out
  may still have committed.
- **A range query over a numeric field could silently return part of the
  answer** (go-qrdb#35): text-ordered terms put `"10"` before `"2"`. Numeric
  fields are now indexed numerically (see Added). Alongside it:
  - A range planned on an index took its bounds straight to the half-open
    `[start, end)` scan, and the consumed predicate was not re-checked:
    `Field > v` returned the records equal to `v`, and `Field <= v` and
    `BETWEEN a AND v` left them out. The DAO now scans the exact interval.
  - `Field PREFIX p` planned on an index returned every value sorting after
    `p` (`"bz"` and `"charlie"` for `PREFIX "br"`): the scan has no upper
    bound and the predicate was dropped. It is now kept as a post-filter, and
    PREFIX on a numeric field is answered by a full scan.
  - The Unix-socket transport's `IndexCreate` dropped the `numeric` and
    `case_insensitive` flags, so an index created over it was a
    case-sensitive text index whatever the caller asked for.

- **The `objstore` backend's commit journal is out of the tree a listing
  walks.** The local provider lists by walking its directory, and the walk
  fails when an entry it read vanishes before it is stat'ed. The journal is
  created and removed by every batch, so a time-series query racing a write
  failed with `lstat ...qrdb-objstore-journal: no such file or directory`. The
  local provider now keeps its journal in a sibling `<path>.journal` tree.

- **A request sent right after a leader change no longer fails with `not
  leader`.** A follower that briefly knows no leader for a shard holds a
  proposal or linearizable read, and re-runs it once a leader is known, on
  every transport. Nothing has been proposed when it is held, so the retry
  cannot apply a command twice. The hold is bounded by 10 seconds and the
  caller's deadline; at the bound the request fails with `*raft.LeaderWaitError`,
  still a not-leader 503 to the client. Stale reads are not affected.
- **A boot-time read can no longer hang a node's startup** (go-qrdb#668). Each
  attempt of a startup read is bounded to 5 seconds, and go-quicraft's
  retryable `ErrReadIndexNotReady` — a new leader that has not yet committed in
  its term — and transient not-leader answers are retried until the read's
  startup deadline. A boot that runs out fails with `*server.StartupReadError`,
  logged at ERROR, naming the read, its budget and the attempts made.
- **A restarted `qrdbd` node no longer hangs starting its feature gate.** A
  leader that has not yet committed in its term drops a follower's ReadIndex
  without answering it, so the gate's activation read could wait forever and
  the node never reached its listeners. `qrdbd` and `sdk/go/node` now bound
  each start attempt by `server.StartupReadAttemptTimeout` and retry it
  (`server.WhileSystemShardSettling` in `qrdbd`). `Client.StartFeatureGate`'s
  context bounds the start only; the advertisement and the gate run on until
  they are closed.
- **A lease grant no longer reports a lease it granted as not found.** The
  grant read the lease back after its apply, and the expiry sweep can revoke a
  short lease in between, so a successful grant failed with
  `*service.LeaseNotFoundError`. The grant now answers with what its apply
  granted (the assigned ID and the requested TTL, all of it remaining). On a
  tenant-scoped grant, a lease the sweep revoked before its ownership record
  was written is returned the same way, and the scope then finds it expired
  like any lease whose TTL ran out; lease IDs are never reused.
- **Raft groups on one node no longer share an audit chain.** Each state
  machine now has its own audit `Chain`; a shared one numbered one group's
  events after another's, and opening or recovering either group overwrote the
  other's chain heads.

## [0.48.0-alpha] - 2026-10-14

### Added

- **Public MCP tools.** A `ServiceModule` implementing
  `apimodule.MCPPublicToolLister` (`PublicMCPTools() []string`) names MCP
  tools an unauthenticated caller may reach — health, liveness, readiness, a
  login ceremony. Such a caller can `initialize`, list exactly those tools and
  call them; every other request is answered 401 as before. The server refuses
  at startup a declared tool that carries any permission but `admin:health`
  (`*middleware.PublicMCPToolRefusedError`) or a name no tool answers
  (`*mcp.UnknownPublicToolError`). An authenticated `tools/list` shows only the
  tools the caller's grants permit.

### Changed

- **A node proposes nothing on its own behalf while its barrier is sealed.**
  The feature-gate advertisement, the member record, the dynamic membership
  manager and the lease-expiry sweep consult the node barrier: a
  `cluster.Advertiser` composed with `WithAdvertiseSealProbe` (and a
  `MembershipManager` with `WithMembershipSealProbe`) skips its keep-alive
  while sealed and refuses a registration with `*cluster.NodeSealedError`,
  retried after the unseal; `Lessor.RunExpiry` composed with
  `lease.WithExpiryHold` revokes nothing while sealed and gives every lease a
  full TTL when the hold is released.
- **`qrdbd` waits for the system shard leader for 120 election timeouts**
  (15 seconds single-node, 4 minutes for a cluster on the default Raft tuning)
  instead of a fixed number of attempts. The `no raft leader after ...` error
  names the budget and the time spent, and SIGINT/SIGTERM ends the wait.
- **Replica forwarding reconnects to a target** that fails its connection,
  for up to half of `replica_forwarding.timeout`, before counting it
  unreachable and moving to the next replica.
- **The tenant barrier-ready hook also runs when a holder share completes an
  unseal quorum** (`TenantBarrierQuorumSubmit`), never below the threshold,
  for another quorum op, or on a failed submission.
- **The MCP SDK transport bounds each call by its configured timeout**,
  including the `initialize` handshake, as the REST, gRPC and QUIC transports
  do; a caller's earlier deadline still wins.

- **CI runs in two tiers.** A pull request runs the PR tier (`lint`,
  `build`, `test`, `test-sdk`, `test-typescript`, `security`); a push to
  `main`, a `workflow_dispatch` or a pull request labelled `ci:full` adds the
  full tier (`integration-standalone`, the `integration-features` legs,
  `trivy`). `ci-gate` stays the one required
  check: each check line is tagged `pr` or `full`, and a skip passes only for
  a `full` job in a run that did not select the full tier. `nightly.yml` no
  longer runs on a schedule (dispatched by hand), and `release-lockstep.yml`
  joins the ref-keyed `cancel-in-progress` group. `test/buildgates/ci_tiers_test.go`
  holds the tags to the jobs. See `docs/release/architecture.md`, "CI tiers".

- **CI runs the self-spawned integration suites as one job per feature.**
  `integration-features` is a matrix with one leg per feature that
  `test/integration/features.mk` maps a file to (40 today), each running
  `make integration-test-<feature>` under its own `timeout-minutes: 45`, in
  parallel, with `fail-fast: false` and `GOMAXPROCS=8`. It replaces the
  `integration-cluster` and `integration-cluster-tenancy` jobs and the
  integration-standalone job's in-memory auth step. `make -s
  print-integration-features` prints the legs, and
  `test/buildgates/feature_jobs_gate_test.go` fails when ci.yml's literal list
  differs from it. `ci-gate` reads the job's aggregate result, which fails
  when any leg fails. Removed: `make integration-test-cluster-tenancy`,
  `CLUSTER_TENANCY_RUN`, `CLUSTER_SHARD`/`CLUSTER_SHARDS`,
  `make check-cluster-shards`, `make test-cluster-shards` and
  `scripts/cluster-shard.sh`. `make integration-test-cluster` stays for a
  whole local run, with a package timeout of `INTEGRATION_GO_TIMEOUT`
  (default `100m`). See `docs/release/engineering.md`, "Per-feature
  integration jobs".

- **Breaking: `node.WithAuthenticators` takes a chain position.** Its
  signature is now `WithAuthenticators(position auth.ChainPosition,
  authenticators ...auth.Authenticator)`, with `ChainBeforeMTLS` (the
  authenticator decides every request it accepts, including one that presents
  a client certificate), `ChainAfterMTLS` (the previous behaviour) and
  `ChainReplaceMTLS` (mTLS's decision is removed; the transport's TLS stays).
  `wiring.AuthMiddlewareConfig.ExtraAuthenticators` is now
  `[]auth.AuthenticatorPlacement`, assembled by `auth.AuthenticatorChain`. An
  invalid position (including the zero value) is refused with
  `*auth.InvalidChainPositionError`, and a chain left empty by
  `ChainReplaceMTLS` with `*auth.EmptyAuthenticatorChainError`; both are
  re-exported from `sdk/go`. To keep the old order, pass `sdk.ChainAfterMTLS`.
- **`golang.org/x/net` v0.59.0 → v0.60.0** in the root, `sdk/go`,
  `sdk/go/gorm/conformance` and `test/performance/qrdb` modules, fixing the
  HTTP/2 vulnerabilities `govulncheck` reported against the REST server
  (GO-2026-6617, GO-2026-6612).
- **Go 1.27.1 → 1.27.2.** The `go` directive of every module, the CI, release
  and nightly `GO_VERSION`, and the Dockerfiles move to the release that
  fixes the thirteen standard-library vulnerabilities `govulncheck` reported
  (among them `html/template`, reached through the REST server). No action pin
  or dependency version changes.

- **The system barrier resolves its sealing strategy through the strategy
  registry.** `seal.NewSystemStrategyRegistry` holds qrdb's software
  strategies overlaid by a composer's `server.WithStrategyRegistry` registry,
  and `seal.ResolveSystemStrategies` builds the configured strategy and the
  one the persisted root key names. Both `qrdbd` and `sdk/go/node` use it; the
  node's `WithSealStrategy` registers its strategy under its own ID.
- **The server TLS leaf key is a `crypto.Signer`.** `server.WithServerTLSKey`
  (and `sdk/go` `WithServerTLSKey`) composes a device-held signer in; only its
  public half reaches the CA and handshakes sign through it. Unset, each
  transport's auto-provisioned leaf gets a fresh in-memory P-256 key.
  `ceremony.ServerTLSConfig` takes the signer as its third argument, and
  `ceremony.CeremonyCA.IssueCertificate` certifies the caller's
  `IssueCertRequest.PublicKey` (required; `*ceremony.IssueCertKeyError`
  without it) instead of generating a key.
- **HPKE recipients open through a `keydelivery.Decrypter`.**
  `keydelivery.NewReceiver` takes a `Decrypter` instead of an
  `*ecdsa.PrivateKey`, and `ceremony.ShareEnvelope.Open` takes a
  `keydelivery.Decrypter`. `keydelivery.NewDecrypter` builds one over a
  device-held `ecdh.KeyExchanger`, so the private key never enters the
  process. `sdk/go/node` `WithKeyDeliveryDecrypter` and
  `lifecycle.RaftTenantClusterConfig.Decrypter` compose a node's device-held
  decrypter into the key plane; unset, the node's software key is converted.
- **A clustered node's mTLS identity can be a `crypto.Signer`.**
  `lifecycle.RaftTenantClusterConfig.Identity` (a `*tls.Certificate`) carries
  the node identity into the cluster key plane. `sdk/go/node` takes it from
  `WithRaftMTLS`'s `Certificate` when set, else from the `Cert`/`Key` pair, and
  derives its member identity and cluster-join certificate from the same leaf,
  so a node whose key is held on a TPM or HSM forms a cluster with no key PEM.
  A key plane the identity cannot serve reports `*lifecycle.NodeIdentityError`
  in its warning.

### Removed

- **`ceremony.IssuedCert.KeyPEM`.** A CA no longer generates or returns a leaf
  private key; the caller holds it.
- **`lifecycle.RaftTenantClusterConfig.CertPEM` and `KeyPEM`.** Replaced by
  `Identity`; no key PEM flows into the cluster key plane.
- **`server.WithSealStrategy` and `CryptoOptions.SealStrategy`.** Nothing
  consumed them; inject a system-barrier strategy with `sdk/go/node`'s
  `WithSealStrategy` or through `server.WithStrategyRegistry`.

### Fixed

- **`RebalanceService.ExecuteRebalance` refuses a plan whose placement
  records cannot be read**, with `*service.RebalancePlacementReadError`,
  instead of moving a dedicated tenant shard by the generic voter add.
- **First boot on a recycled or unsealed node no longer proposes the issued
  admin certificate again** once the replica holds its record; a first write
  that meets a leader election is retried for up to 10 s.
- **A first-boot admin username collision whose record is not yet readable**
  is retried within a bounded window and converges on the winner, instead of
  being refused as a squatter.
- **Enabling encryption on an embedded `Node` stops the volume-pool reporter**
  before closing the old data plane's storage, so the rebuild no longer fails
  with `storage is closed`.
- **The cluster-seal monitor no longer spends its boot exemption** on a read
  that began before the feature gate finished loading its activations.

## [0.47.0-alpha] - 2026-10-08

### Changed

- **go-enclave v0.8.0-alpha in every module.** Every protected-memory read is
  a scoped borrow (`Buffer.WithBytes`, `secure.CopyOut`). Every go-enclave
  `Destroy` error is handled: `secure.LogRelease` logs a release problem a
  caller cannot return (WARN, or ERROR for a corrupted canary), and
  `seal.DestroyCredentials` replaces `creds.Secret.Destroy()` for a
  caller-owned seal credential.
- **go-quicraft v0.20.0, go-objstore v0.4.0-alpha, go-truststrap
  v0.6.0-alpha.** `github.com/awnumar/memguard` is no longer in any module's
  graph: go-enclave is the one protected-memory implementation. The benchmark
  harnesses take go-quicraft's `test/performance` v0.20.0, whose result errors
  are typed (`perfresult.NoResultsError` replaces `ErrNoResults`).

### Removed

- **`secure.Buffer.Bytes`.** It handed out a view of locked pages with no
  borrow held; read through `WithBytes`, or take a wipeable copy with
  `secure.CopyOut`. The secure analyzer's rule 4 (`//secure:unborrowed`),
  which policed `Bytes`, is retired with it.
- **MinIO support.** The `WITH_MINIO` make flag and its `minio` build tag are
  gone, and no `minio` objstore provider is documented or compiled in. S3 is
  served by the `s3` provider (`WITH_AWS_S3`).

## [0.46.0-alpha] - 2026-10-08

### Added

- **A node-sanctioned service principal for an embedder's own in-process
  calls** (#609). `node.WithServicePrincipal(name)` and
  `Node.ServiceContext(ctx)` bind the identity `qrdb-service:<name>`, in the
  system tenant, to calls made through the node's embedded client. Its
  authority is only the permissions the embedder registers under that name
  with `sdk.RegisterPermissionRoleGrant`. The node's authorizer decides each
  call as it does any caller's: a call outside the grant is refused with
  `*sdk.PermissionDeniedError`, and every decision is audited under the
  principal's subject. `node.ErrNoServicePrincipal` is returned by a node
  configured without one. The principal is refused on every network
  transport: REST/HTTP/3, gRPC, the Unix socket, QUIC, MCP, SQL, etcd, both
  on-behalf assertion paths and module fallbacks. The refusal applies to a
  presented identity in the `qrdb-service:` namespace and to one that carries
  the principal marker. It is `*sdk.ServicePrincipalPresentedError`, which
  unwraps to `sdk.ErrUnauthenticated`, so the transport answers 401 /
  `Unauthenticated`. `sdk.ServicePrincipalSubjectPrefix` and
  `sdk.IsServicePrincipal` are re-exported.

### Changed

- **CI tool pins moved to the upstream latest, in step with the runner image**
  (#613): golangci-lint v2.13.2 → v2.14.0, govulncheck v1.7.0 → v1.8.0 (gosec
  stays at v2.29.0, already the latest) and Go 1.27.0 → 1.27.1 in the
  workflows, Dockerfiles and every `go.mod`. golangci-lint v2.14.0 bundles
  gosec v2.29.0, so `GOLANGCI_GOSEC_VERSION` moves to v2.29.0 and the gosec
  exclusions in `.golangci.yml` were re-measured: G602 is now clean on both
  modules and its entry is removed; the G115 counts are 312 / 125.

### Fixed

- **A node that takes over a group no longer refuses a cluster feature that
  was activated long before.** The feature gate learned of another node's
  activation only on a refresh that waited on the system shard's leader, so
  on a slow disk, or across a leader change, a new leader could refuse the
  feature's writes with `*featuregate.NotActiveError` for tens of seconds.
  One case was the scheduled-compaction samples and compactions. The gate
  now adopts every activation record its own replica has applied, on each
  advertisement event and at the start of each refresh. The startup
  downgrade check stays linearizable. `featuregate.ActivationKV` gains
  `ListStale`, which `service.KVServicer` already implements.

## [0.45.0-alpha] - 2026-10-07

### Highlights

- **Central tenant and target authorization layer exported from the SDK**
  (#587), with node auth accessors for downstream modules.
- **Storage-generic engine and per-database bindings, phase 1 and 2B/2C.**
  The per-database `engine` field is removed (every database exposes every
  compiled-in engine), the catalog authority and placement cover every
  backend, and drop is fenced under a binding on every engine.
- **Strict tenant isolation, phase 5.** The `strict-isolation` feature, the
  isolation policy floor, and the `ScopeSelf` system lift removed: a system
  admin acting in another tenant needs the explicit `:cross` permissions.
- **FIPS 140-3.** The nightly FIPS regression fixed across four defect
  classes, and `cluster:join_token` granted to Admin under FIPS.
- **Security and reliability.** Replicated audit phase 2, tenant DEK seam,
  first boot over every transport, cluster integration suite armed on every
  push, per-feature `make integration-test-<feature>` targets, and the CI
  gates (coverage, gosec, publication-bound, release-alpha fixtures) green.

### Removed

- **The per-database `engine` field** (`kv`/`timeseries`/`vector`), across the
  proto (`DatabaseCreateRequest`/`DatabaseInfoResponse` field 2 is now
  `reserved 2; reserved "engine";`), every transport (REST, gRPC, QUIC, MCP,
  Unix, embedded), the CLI (`qrdb database create --engine`, the `ENGINE`
  column and info line), `pkg/config`'s `databases[].engine`, the SQL driver
  DSN's `engine=` parameter, and `pkg/sql/provider`'s per-engine table
  gating -- every database now exposes every compiled-in engine's SQL tables
  (OD-12). `database.EngineType`, its constants, and
  `database.InvalidEngineError`/`database.EtcdEngineUnsupportedError` are
  deleted. A create request still carrying `engine` is refused as an unknown
  field rather than silently accepted or ignored -- REST and QUIC decode the
  body with `encoding/json`'s `DisallowUnknownFields`, MCP's create tool
  rejects any argument outside its declared set
  (`pkg/api/mcp/wire.RejectUnknownArgs`), and gRPC/Unix have no `Engine`
  field to set at all once the proto field is reserved (OD-13, ruled A). A
  `meta.json` written by a version before this one and still carrying
  `"engine"` is refused outright as `*database.LegacyMetadataError`
  (`FailedPrecondition`) rather than silently stripped and rewritten: the
  no-backward-compat ruling of 2026-10-02 supersedes OD-11's original
  rewrite-on-open plan for this file, and the affected feature doc
  (`docs/databases/engineering.md`) is corrected to match.
- **`seal.AbsoluteDataRootRecordError` and `seal.TenantRootUnresolvedError`.**
  Both were pre-catalog leftovers: a tenant's location has been a volume pool
  name and slot resolved on each node since the catalog landed, so neither a
  tenant-root record naming a path nor an unresolved pool/slot pair is a
  shape production code can still construct. `pkg/api/errclass`'s
  classification table and tests are updated to match.

- **`ceremony.ErrNotImplemented` and the SDK's `ErrCeremonyNotImplemented`
  alias.** Both were the last trace of the pre-unification `CeremonyExtension`
  mechanism that the unified-share-holders rewrite (2026-09-29) deleted: a
  `SetExtension` seam the composers never called, which is why #559 found
  claim-share and unseal-share submission answering `ErrNotImplemented` on
  every transport.
  That mechanism no longer exists -- `CeremonyService.BeginClaimShare`,
  `CompleteClaimShare` and `StorageBarrier.SubmitHolderShare` are wired
  directly, with identical composition in `cmd/qrdbd` and `sdk/go/node`
  through the single `lifecycle.BuildServices` -> `ceremonyService` path, so
  there was nothing left for either composer to call this error from.
  Neither symbol had a reference anywhere in the tree. Re-verified end to
  end (`TestShareHolders_ConformAcrossEverySDKTransport` on every SDK
  transport, and `TestBarrierCLI_ShamirLifecycle_PerProtocol` against the
  real `qrdbd` binary on every protocol) before removing them. Closes #559.

### Added

- **Module hosts can authorize against a caller-named tenant, and reach the
  node's live auth middleware directly** (go-qrdb#587):
  - `tenant.OwnTenant` (`sdk.OwnTenant`) and `sdk.CrossTenantDeniedError`:
    `EffectiveTenant`'s requested-vs-identity rule with no exception for the
    system tenant, for a caller enforcing "no caller, system included, ever
    acts on another tenant" today.
  - `sdk/go/node.Node.AuthMiddleware` / `Node.Authorizer`: race-free
    accessors onto the node's live `*middleware.AuthMiddleware` and its
    composed RBAC[+ABAC] authorizer, so an embedded/Unix host calls
    `AuthorizeTenantTarget` directly instead of capturing the authorizer
    indirectly through `WithAuthorizerWrapper`.
  - `pkg/apimodule.ModuleTenantTargetAware` / `TenantTargetAuthorizer`
    (`sdk.ModuleTenantTargetAware` / `sdk.TenantTargetAuthorizer`): an
    optional `ServiceModule` extension through which the unified server
    hands a module its tenant-target authorizer before any of the module's
    `Register*` hooks run, so a module's handler on any transport (REST,
    gRPC, unix, QUIC, MCP) can authorize its own operation against a
    caller-named target tenant under the host's RBAC ScopeCross/ScopeSelf
    rules. `*middleware.AuthMiddleware.Authorizer()` is the new accessor
    the Node methods above read.
- **`qrdbd start` fails loudly by default when a configured volume pool
  cannot be opened** (decision D10, `docs/volume-pools/`): a missing mount,
  an absent device or an unregistered provider now fails startup outright
  with a typed `*volume.OpenAtBootError` naming every inaccessible pool and
  the reason, instead of silently refusing just that pool's tenants while
  the node keeps booting. The new `--allow-degraded-volume-boot` flag (and
  its config-file equivalent, `storage.allow_degraded_volume_boot`) is the
  explicit opt-in to the old behavior: start every volume and database this
  node can and skip the rest. A skipped pool is now logged at ERROR
  (previously WARN), reported in the node's health
  (`qrdb volume-pool get`), and exported as the new
  `qrdb_volume_pool_ready{pool}` gauge. A request for a database on a
  skipped pool already got `*volume.PoolUnavailableError`, classified
  `Unavailable` on every transport, but only when the request happened to
  reach it: `TenantHostRegistry.Services` resolved a tenant that was never
  registered (every tenant on a skipped pool, exactly this case) to a
  generic "tenant not found" first. It now falls back to the volume
  allocator directly, so the real cause surfaces instead. `tenant get`
  (unlike `tenant list`) never showed `Refused:` at all --
  `TenantService.GetTenant` skipped the resolution check `ListTenants`
  already ran; both now agree. The REST and MCP client SDKs separately
  dropped a tenant's `refusal` field on decode (gRPC, QUIC and Unix
  already carried it through `tenantconv.ToTenantInfo`), and the MCP
  *server* tool handlers lost it on encode (`tenant.TenantInfo.Refusal` is
  `json:"-"`, so `json.Marshal` alone drops it; REST's own handler already
  rebuilt it, MCP's did not) -- both ends are fixed on both transports.

- **The tenant DEK keyring wraps every version's key through the
  `pkg/keywrap` KEK seam** instead of storing it in plaintext: `pkg/dek`
  gains `KeyringOption`s `WithKeyWrapRegistry`/`WithKeyWrapProvider`
  (install a go-xkms-registered hardware or cloud-KMS provider — the
  SAME registry/provider the backup feature already consumes, so one
  registration reaches both), `WithKeyDir` (the built-in `"software"`
  provider's confined key directory) and `WithKeyRef` (the tenant-scoped
  key reference a new version wraps under; defaults to the tenant ID
  itself). `lifecycle.Infra.DEKKeyDir`, `server.WithDEKKeyDir` and
  `qrdbd start --dek-key-dir` wire it through every composition root;
  `qrdb dek status` and every transport's wire shape now show each
  version's `provider_id`/`key_ref` (`VersionInfo`, never key material),
  for audit. There is no plaintext escape hatch: resolving the built-in
  provider with no key directory configured refuses minting a key with a
  typed, server-side error (`*dek.KeyDirNotConfiguredError`) rather than
  falling back to a plaintext key. A record written before this
  integration existed is refused with `*dek.LegacyKeyringFormatError`,
  never migrated silently.
- **Every admitted, unsealed member now serves the cluster-CA trust anchor,
  closing a single point of failure.** Before this, `cmd/qrdbd` registered
  `/v1/ca/bootstrap` and ran the Noise anchor listener only on the one node
  that happened to generate (or resume serving) the self-issued cluster CA
  (Key A); once that node was gone for good, no new node could ever join
  again, no matter how many other members were alive and unsealed. The fix
  (`servesAnchor`, `startMemberAnchorAdvertiser` in `cmd/qrdbd/bootstrap.go`)
  adds no second copy of Key A and no new key transport: every member
  already caches the identical CA certificate, and now serves it from
  exactly that cache. `clusterenroll.FetchAnchor`/`Join` need no change —
  only which member's `--bootstrap-addr`/`--bootstrap-join-url` a new node
  may point at. See `docs/clustering/architecture.md`, "Every admitted,
  unsealed member serves the anchor."

- **`tenant barrier-init --strategy shamir` takes its Security Officer/
  custodian roster inline** (`--officers`/`--custodians`,
  `username=path/to/csr.pem`, repeatable), mirroring `qrdb init`'s own
  inline roster for the system barrier: a dedicated tenant's own IAM is
  encrypted behind the very barrier its officers exist to help
  initialize, so standalone `tenant barrier-officer register` cannot
  reach it beforehand. The roster is validated as one pure batch (the
  same validator the system barrier's own M-of-N init path uses,
  `ceremony.ScopedCeremonyService.InitializeShamirWithRoster`) and
  registered atomically with initialization; any later failure --
  registration, signing, or the barrier's own Shamir split -- rolls
  every already-registered officer back, leaving nothing persisted. A
  roster supplied on any strategy but Shamir is refused outright, not
  silently discarded, as is an invalid factor policy. Re-initializing an
  already-initialized Shamir tenant barrier is refused before touching
  the registry or any officer, leaving the original threshold,
  configuration and holder set unchanged. Available on every transport:
  REST, gRPC, QUIC, MCP, Unix and the embedded transport.
- **MySQL-wire and embedded-driver proof that `INSERT ... ON DUPLICATE KEY
  UPDATE ... RETURNING` returns a row matching its declared schema**
  (docs/sql/upstream-gaps.md #2). The engine-level repair
  (`pkg/sql/engine/dml_returning.go`) already projected the RETURNING
  expressions against the post-update tuple instead of leaking
  go-mysql-server's old+new concatenation; it was pinned only at the engine
  layer. `pkg/sql/mysql/dml_returning_wire_test.go` now proves it over a
  real MySQL wire connection, and `sdk/go/driver/dml_returning_e2e_test.go`
  proves it through `database/sql` itself -- the surface whose own
  `*driver.RowWidthError` would have caught an unrepaired row.
  `upstream-gaps.md` is updated to record the gap as worked around, mirroring
  its sibling entry for item 1.

### Changed

- **`make release-alpha`, run by hand, replaces the Release workflow's
  release bot** (owner ruling 2026-10-06: "all our projects should follow
  the same standards" as go-quicraft, go-xkms and go-quicbaas). The project
  agent now cuts and pushes a release's tags itself, with its own existing
  git identity, any time CI is green on `main`; there is no longer a
  `RELEASE_BOT_TOKEN`/`RELEASE_BOT_SIGNING_KEY` secret or a
  `RELEASE_BOT_NAME`/`RELEASE_BOT_EMAIL` variable to provision.
  `.github/workflows/release.yml` is now `push: tags: ["v*"]`-triggered and
  creates no tag: it validates the tag, `VERSION` and `CHANGELOG.md` against
  what the push just published (retrying the lockstep audit, since an atomic
  push does not bound how soon a fetch observes every sibling tag), re-runs
  the matching ci.yml build/lint/test commands against the exact tagged
  tree, and publishes that version's `CHANGELOG.md` section as the release
  notes through the forge's own releases API. `scripts/release-bot-git.sh`
  and `scripts/git-extraheader.sh`, both bot-only plumbing, are removed with
  no replacement.

- **`pkg/fts`'s real implementation is now covered by CI (#610).**
  `WITH_QRDB_FTS` defaults to 0 and `qrdb_fts` was excluded from
  `TEST_TAGS_SETS`, so nothing in CI ever built or ran the real analyzer,
  tokenizer and inverted index — only the build-tag stub. `make coverage-fts`
  (`go test -race -tags "$BUILD_TAGS qrdb_fts" ./pkg/fts/...` plus a coverage
  check against the new `test/coverage-baseline-fts.txt`, measured under that
  tag set) now runs as its own step in CI's `test` job, gating every push.
  `make coverage-fts-baseline` regenerates its baseline.

### Fixed

- **A time-series write never shared the Raft apply batch with the rest of
  its entry.** `MVCCEngine`'s time-series handlers decoded their payload and
  wrote straight to `timeseries.Storage`'s own, separate PebbleDB-backed
  store, discarding the `storage.Batch` the apply loop handed them — so a
  time-series point committed independently of, and could outlive, a failed
  commit of the KV/MVCC write in the same entry, and time series worked at
  all only when the database happened to be pebble-backed (the only backend
  `timeseries.Storage` could wrap). `pkg/timeseries.Storage` now wraps a
  `storage.Storage` instead of a `*pebble.DB`: every write method takes the
  caller's `storage.Batch` and stages into it, and every read goes through
  the Storage's own `storage.Reader`, so time series works identically over
  memory, file, pebble and objstore and commits atomically with the rest of
  its entry on every one of them (`pkg/engine.TestMVCCEngine_TSAndKVWriteInOneApplyBatch_CommitFailureLeavesNeither`).
- **A unique secondary index built over `index.StoragePebbleIndex` never
  enforced its constraint.** The index's own enforcement read
  `IndexConfig.Type == IndexTypeUnique`, but `MVCCEngine`'s index-create
  handler only ever set `IndexConfig.Unique`, so a unique index created
  through the MVCC engine silently accepted duplicate values. The index now
  reads `IndexConfig.Unique` (matching `MemoryIndex`/`PebbleIndex`), and also
  implements `IsUnique()` and a batch-aware `GetInBatch`, so a uniqueness
  check made against one entry of a Raft apply batch sees a conflicting write
  staged earlier in the same batch rather than only committed storage.
- **A time-series retention policy was stored but never enforced**
  (`TSSetRetention`/`TSGetRetention` round-tripped the policy; no point
  ever expired, so a series grew without bound). A leader-driven
  `pkg/server` retention scheduler now sweeps each store once a minute,
  listing its written metrics and, for each with a `duration_ns` policy,
  proposing one `TSApplyRetention(metric, cutoff)` Raft command — the
  leader's own clock, never a replica's — that deletes points older than
  cutoff deterministically on every replica
  (`timeseries.Storage.ApplyRetention`, bounded to
  `MaxRetentionDeletesPerApply` points per command so a large backlog is
  swept over several ticks). The new opcode is gated behind
  `featuregate.TimeSeriesRetention` so a mixed-binary cluster never sends
  it to a replica that cannot apply it. Works standalone and raft, and
  the drop fence on a shared store's databases is already applied
  through the namespaced time-series service's write guard.
- **`TRUNCATE TABLE`'s effect on a keyless table's hidden row id was an
  open question, pinned by nothing** (go-qrdb#547 follow-up). The
  generation-scoped `TRUNCATE` landed in v0.22.0-alpha decided and tested
  that `TRUNCATE` resets `AUTO_INCREMENT` on MySQL and leaves it alone on
  PostgreSQL, but never said what happens to a keyless table's separate
  hidden row id counter (`TableRowIDSequenceName`), and no test exercised
  it either way. Ruling: `TRUNCATE` does not reset it, on either dialect —
  the row id is never exposed to SQL, so a MySQL compatibility answer
  cannot be copied, and leaving it strictly monotonic matches the
  never-reused design this package already gives index incarnations,
  rather than let a future row's id alias an earlier, already-reclaimed
  one. `TableService.TruncateTable` already implemented this (its
  `resetSequence` parameter names only `TableAutoIncrementSequenceName`),
  so no production code changes; pinned by
  `TestRelationalTable_Truncate_DoesNotResetHiddenRowID`
  (`pkg/sql/provider`) and `TestTableService_TruncateTable_RowIDSequenceUntouched`
  (`pkg/service`), and recorded in docs/sql/engineering.md.
- **A DAO built `WithDatabase` over the engine-backed `lifecycle.kvStoreAdapter`
  had no end-to-end proof its qualified index
  (`{database}.{entityType}.{FieldName}`) was ever maintained.**
  `kvStoreAdapter.RegisterEntityIndexes` already turned the qualified name
  into a correctly-scoped index via `go-qrdb#410`'s key-scope mechanism, but
  every test of that path drove a `mockIndexServicer`, which can only show
  the adapter's `CreateIndex` request was shaped correctly — not that a REAL
  `KVEngine` maintains the index on a write that follows registration and
  answers a query against it non-empty. A new integration test
  (`pkg/server/lifecycle/kv_adapter_real_engine_test.go`) drives the write
  through a real engine and asserts the non-empty result (go-qrdb#38).
- **Nightly FIPS 140-3 went red (go-qrdb#579), on four independent defect
  classes, not one.** A genuine production bug: `pkg/keywrap.SoftwareProvider`
  (the backup key-wrap provider) and `pkg/service.{encryptShard,decryptShard}`
  (the backup shard cipher) still built AES-GCM with `cipher.NewGCM` instead
  of `cipher.NewGCMWithRandomNonce`, byte-compatibly migrated like every
  other site in [the original AES-GCM
  remediation](docs/seal/fips140-mode.md#aes-gcm--ciphernewgcm--ciphernewgcmwithrandomnonce).
  Three ignored key-generation errors (`key, _ := ecdh.X25519().GenerateKey(...)`)
  caused nil-pointer panics under `fips140=only` across `pkg/authfactors/mtls`
  and `pkg/keydelivery` — the second of which an unrecovered first panic had
  been hiding, since a panic kills the whole test binary and every
  subsequent test in that package never ran. Several test fixtures could not
  be constructed under strict enforcement at all (a 1024-bit RSA key, a
  SHA-1-signed certificate — `crypto/sha1` panics rather than erroring) or
  picked a KEM/transport incidental to what they tested
  (`keydelivery.KEMX25519` for a holder whose certificate key simply needs
  *some* separate envelope key; the member-side CSR join server, which needs
  a real Noise NK listener that cannot come up under `fips140=only` no
  matter how its own gating variable is forced). See
  [docs/seal/fips140-mode.md](docs/seal/fips140-mode.md#go-qrdb579-the-nightlys-first-real-regression)
  for the full accounting, including a capability gap the fix only
  documents rather than remediates: a FIPS build is always strict
  separation-of-duties (`fips140.Enabled() || cfg.Auth.SeparationOfDuties`),
  which voids the `owner` role, and `cluster:join_token` is carried by no
  other built-in role — so no identity mints a cluster join token through
  the default role set under FIPS, including the system tenant's own Owner.
- **`cluster:join_token` is now granted to the cluster Admin built-in role**
  (owner ruling, 2026-10-06), closing the capability gap the previous entry
  only documented: strict separation of duties — always on in a FIPS build —
  voids the `owner` role id, and before this change `cluster:join_token` had
  no other built-in-role grant, so no identity could mint a cluster join
  token under FIPS by default. The grant applies unconditionally, not only
  under FIPS, so a system-tenant Admin now mints a join token and admits a
  node the same way in every posture; a tenant's own Admin still cannot,
  since the permission stays `ScopeCross`. Neither Security Officer nor any
  custodian/holder surface gained it, so FIPS 140-3 AS10.03 separation of
  duties is unaffected. See
  [docs/seal/fips140-mode.md](docs/seal/fips140-mode.md#strict-separation-of-duties-and-clusterjoin_token-under-fips).
- **Documentation and comments describing `sign-csr` as reachable after the
  initialization ceremony goes operational.** `CeremonyService.SignCSRInit`
  refuses every call once the ceremony leaves `enrolling` state, on every
  transport, regardless of seal state or session — there never was an
  operational path back to it. Corrected in docs/ceremony/{user,cli}.md,
  docs/ceremony-sdk.md (which also said the verb issues a login certificate;
  it only ever issues a holder certificate) and
  docs/certificate-authority/architecture.md.
- **`docs/auth-factors/engineering.md` showed `WithFactorRegistry` taking
  `authfactors.FactorRegistry` by value**; the real signature takes
  `*authfactors.FactorRegistry`.
- **Stale "pending Phase 2" TODO comments and doc sections** claiming the
  auth-factor and audit permission constants (`permission.AuthFactor*`,
  `permission.AuditQuery`) were not yet wired, when they already gate every
  route/frame/tool registration. Removed from
  `pkg/api/rest/handlers_authfactor.go`, `pkg/api/quic/authfactor_frame.go`,
  `pkg/api/mcp/authfactor_tool.go`, `sdk/go/transport/authfactor_types.go`,
  `sdk/go/transport/rest/transport_audit.go`, `docs/auth-factors/api.md` and
  `docs/audit/{api,engineering}.md`.
- **`docs/shamir-custody/api.md`'s distribution capability matrix was
  missing its Embedded column.**
- **Several `docs/*/engineering.md` and `docs/*/cli.md` files claimed the
  cluster integration stage (`make integration-test-cluster`) is not wired
  into CI.** It runs as the `integration-cluster` and
  `integration-cluster-tenancy` jobs, both required by `ci-gate`. Corrected
  in docs/clustering/{engineering,cli}.md, docs/backup-restore/{cli,
  engineering}.md, docs/barrier-seal/cli.md, docs/distributed-locks/
  engineering.md, docs/run-modes/engineering.md and docs/key-value/
  engineering.md.
- **`AGENTS.md`'s Layer Ownership section named a `WithFirstBoot` server
  option that does not exist** (first boot composes through
  `sdk/go/node`'s `Node.FirstBoot`/`Node.FirstBootWithCeremony` over
  `firstboot.Dependencies`, not a `With*` option) **and said `StrategyShamir`
  lives in go-quicraft.** It is implemented in go-qrdb
  (`pkg/seal/strategy_shamir.go`); only `StrategyNone` and
  `StrategyPassphrase` are go-quicraft L1 strategies aliased here.
- **A rebalance `MOVE` submitted via REST to a node outside the dedicated
  group could fail with a false "forwarding loop" error.** The node picked
  a forwarding target by guessing any member of the group
  (`resolveGroupLeader`'s fallback for a host with no local Raft state to
  consult); if the guessed member happened to be both the group's current
  leader and the `MOVE`'s own source, `resolveMoveLeader`'s proactive
  leadership transfer needed a second forwarding hop that
  `forwardToLeader`'s loop guard rejected as a genuine loop. Observed
  directly in practice. `resolveMoveLeader` (`pkg/service/rebalance_service.go`)
  now never guesses the `MOVE`'s source as a forwarding target, bounding the
  chain at 3 deterministic hops -- exactly what `forwardToLeader`'s
  `maxForwardHops` already tolerates -- while genuine loops are still
  rejected. New integration coverage drives the previously unreachable
  path directly: a 3-node cluster, a dedicated group, `MOVE` submitted
  from the one node that is not a member
  (`TestIsolationRaft_Rebalance_Move_SubmittedFromNonMember`).
- **`credential delete` under the `barrier` strategy left a deleted
  credential's version history retrievable.** `DeleteCredential` removed only
  the current value (`credentials/<name>`), never the `#meta` sidecar or any
  `#v<N>` grace-period history `RotateCredential` had written, so
  `RetrieveCredentialVersion` could still return a prior value after the
  credential was reported deleted on every transport (REST, gRPC, QUIC, MCP,
  Unix) — a secret-hygiene and compliance defect. `DeleteCredential` now tears
  down the current value, the meta sidecar, and every historical version
  (`pkg/credentials/versioned.go`'s new `deleteBarrierAll`, which shares its
  per-version deletion loop with `gcBarrier` instead of duplicating it). The
  `manual` strategy already did this correctly and is unchanged.
- **Two flaky tests found by the v0.45 test-health audit (#610).**
  `TestDefaultCertProvider_WatchForDeletions_ReestablishBackoffGrowsAndCaps`
  (`pkg/cert`) asserted wall-clock gaps between `reestablishWatch`'s backoff
  retries with a 15ms tolerance, and failed under full-suite `-race` load when
  scheduling jitter alone exceeded it; `reestablishWatch` now waits through a
  `watchReestablishWait` seam (defaulting to `time.After`) that the test
  replaces with a recording stub, so it asserts the computed backoff sequence
  (grows, then caps) instead of real elapsed time.
  `TestWithPacketConn_Refusals/on_another_port` (`pkg/api/rest`) bound a TCP
  listener and a UDP socket on separate ephemeral ports and `t.Fatal`'d on a
  same-port collision — possible because TCP and UDP have separate kernel
  port spaces — instead of avoiding it; it now retries the UDP bind
  (`bindUDPOnDifferentPort`) until the port differs, deterministically.
- **Vector similarity search silently returned nothing, or ranked under the
  wrong metric, after a restart.** `vectorService.loadNamespaces` registered
  a blank HNSW index hardcoded to the L2 metric for every namespace the
  store already held at startup, which permanently satisfied the lazy
  rebuild's "index already exists" check -- so `ensureHNSWIndex` never ran
  the branch that reads a namespace's real configured metric back from
  storage and scans its vectors into the rebuilt index. `CreateNamespace`'s
  non-Raft path carried the identical hardcoded-L2 defect one level up,
  independent of restart: a `COSINE` or `DOT_PRODUCT` namespace was searched
  under the wrong metric from the moment it was created. All three call
  sites now derive the index's metric from one function
  (`distanceMetricFromString`), and `loadNamespaces` builds each namespace's
  index through the same rebuild path a lazy `ensureHNSWIndex` call uses.
  Pinned against a real PebbleDB-backed store by
  `TestVectorService_RestartPreservesConfiguredMetric` and
  `TestVectorService_SnapshotInstallPreservesConfiguredMetric`
  (`pkg/service/vector_restart_test.go`), both verified to fail against the
  pre-fix code.

- **`qrdb_database_create`'s new strict argument check (above) refused a
  legitimate call carrying `mode`.** `CreateDatabaseRequest.Mode` is a
  pre-existing, QUIC-only field (REST, gRPC/Unix and -- until now -- MCP all
  silently drop it; no CLI flag sets it, so no in-repo caller depends on it
  working, only on it not erroring) that `createDatabaseKnownArgs` omitted
  when it was built for OD-13's unknown-argument rule. `mode` is added to
  the known-argument set, restoring MCP's prior silent-no-op behavior
  exactly rather than turning a latent, unrelated cross-transport gap into
  a new refusal.
- **Ceremony-initialize's admin-credential step answered its own refusal on
  a follower instead of forwarding to the leader, on every transport** (node
  parity, owner ruling 2026-10-06: "all the nodes are equal..."). The
  admin-credential step of `qrdb init` must run on the system shard's leader
  (`service.CeremonyServiceWithLeaderGate`), and a follower refusing it with
  `*service.AdminCredentialNotLeaderError` left the operator to retry against
  another node by hand -- every other system-shard admin write (`member
  add`/`remove`/`promote`, `rebalance execute`, guarded KV ops) already
  forwards instead. Fixed first on REST (`tryForwardWrite`/`forwardToLeader`,
  which now also recognizes this error shape), then extended to every other
  transport that serves it: gRPC's `qrdbServiceServer.CeremonyInitialize` and
  QUIC's `handleStream` and MCP's tool middleware now recognize the identical
  error and relay it through a new `ForwardToLeader` method on their
  existing per-transport forwarder, resolving the leader's address through
  the same `replicaforward.Locator.MemberByAddress` the per-database
  forwarding path uses. The Unix socket forwards through the same gRPC
  interceptor its per-database path uses, and the embedded transport through
  its QUIC client transport, exactly as each already does for a
  per-database refusal. No new resolution logic was added on any
  transport, only a single-hop send through plumbing each already had. See
  [docs/replica-forwarding/architecture.md](docs/replica-forwarding/architecture.md#what-this-feature-deliberately-does-not-do)
  and [docs/clustering/architecture.md](docs/clustering/architecture.md#trust-boundaries).
- **The route-registration-parity build gate (`test/buildgates`) only covered
  REST.** A registration or dispatch table conditioned on leadership,
  first-node, genesis or per-node-file identity on any of the other five
  transports (gRPC service/method registration and interceptors, QUIC's
  frame-method dispatch table, MCP's tool registration, the Unix socket, the
  embedded transport's handler wiring) would have shipped undetected. The
  gate now scans all six transports from one shared detector
  (`roleConditionReason`), through two reusable extraction shapes -- a named
  gate-call argument for REST and QUIC, every `if` condition in the
  registration function(s) for gRPC, MCP, Unix and Embedded -- and each
  transport has its own `_Detects` control test proving the gate can still
  fail.
- **Leader forwarding could bounce a write request back to the node that
  sent it, instead of to the actual leader.** go-quicraft can report this
  node itself as the leader in the same breath it refuses a proposal as
  not-leader -- a readiness lag right after a group's first leader
  election -- and `tryForwardWrite` had no check against that: it forwarded
  anyway, dialing this node's own admin listener, re-entering the identical
  handler, and hitting the identical condition again. Unlike every other
  transient condition on this path, that self-loop never resolved on its
  own; it only ran out the hop-count guard and failed closed with a 508.
  `tryForwardWrite` now recognizes a `NotLeaderError` naming its own raft
  address and declines to forward, falling through to the ordinary (and now
  correctly 503, not 500) `NotLeaderError` classification instead.
- **A member-side CSR-join leaf could never actually be dialed as a Raft
  peer.** `clustertls.IdentityLeafTemplate` carried only a SPIFFE URI SAN for
  the joining node's identity, never any IP/DNS SAN; go-quicraft's Raft QUIC
  transport dials a peer by its raft address and runs the ordinary x509 SAN
  check against it, with no SPIFFE-aware substitute, so a newly admitted
  member's connection from every other member failed TLS verification
  outright (`x509: cannot validate certificate ... because it doesn't
  contain any IP SANs`) and it could never catch up or serve. The leaf now
  also carries the joining node's own address — recovered from its SPIFFE
  node URI, classified as an IP or DNS SAN — never the signing member's own
  addresses. Found and fixed while building the 3-node integration coverage
  for the cluster-CA anchor fix above, which is the first test in this repo
  to carry a member-side CSR-joined node through to a verified functioning
  Raft vote.
- **A dedicated tenant's Shamir barrier could not be initialized at all**
  on a tenant whose barrier shell `tenant create` had already registered
  under the node's default strategy: `tenant barrier-init --strategy
  shamir` always failed closed with `qrdb/seal: shamir not configured`.
  `seal.BarrierRegistry.ReconfigureTenantShamir` now installs the Shamir
  strategy and configuration on an existing shell, honoring an explicit
  `--strategy shamir` choice exactly like `InitializeTenantWithStrategy`
  already does for passphrase/none.
- **A data race on the per-barrier Shamir configuration** between
  `SetShamirConfig`/`ReconfigureTenantShamir` and a concurrent `Status()`
  poll, quorum TTL lookup, or `InitializeShamir` call, caught by `go test
  -race` (none of these guarded `config.Shamir` before this). A
  dedicated mutex now guards every access; a published `*ShamirConfig`
  is replaced, never mutated in place.
- **A tenant-scope `enable-encryption` landing right after a system-scope
  recycle could be refused or misrouted (#584).** `server.scopedEncryptionMigrator`'s
  entitlement check (`CheckBarrierMode`) and isolation lookup (`isRaftTenant`)
  read the tenant service's backing raft host, which has just elected a fresh
  leader during the system-scope recycle and briefly answers
  `quicraft.ErrReadIndexNotReady`. Both reads now retry through the same
  `whenReadIndexReady` helper `pkg/server/tenant_residue.go` already uses for
  the tenant-reconcile and list-tenants paths, instead of surfacing the
  transient error (entitlement check) or failing closed to the volume
  mechanism (isolation lookup).

### Changed

- **docs/multi-tenancy/architecture.md: the 2026-10-01 per-replica-tenant-keys
  ruling is confirmed BLOCKED on the home-store migration, with evidence.**
  An attempt to implement it (independent per-node root keys for
  system-managed `raft` tenants, deleting `seal.ExportRootKeyContext`/
  `ImportRootKey`) was built and then reverted after a real 3-node
  integration run showed it breaks tenant authentication: user/role/auth
  records are encrypted under the tenant's root key and replicated through
  the SHARED SYSTEM SHARD (`pkg/tenant/store_resolver.go`'s `resolveBarrier`,
  "IAM must be resolvable on EVERY replica"), with the ciphertext itself —
  not just the at-rest bytes — baked into the Raft-replicated value
  (`pkg/seal/tenant_shared_kvstore.go`). Every node must therefore hold the
  SAME tenant key purely to authenticate requests, independent of which
  database replicas it hosts; this holds today for every `raft`/`barrier`/
  `volume` tenant, not only the ones the strict-isolation redesign targets.
  The doc's "Per-node-independent key design" and "Per-replica tenant keys"
  sections now state this as a verified prerequisite dependency rather than
  an open question, so the next attempt knows the home-store migration (or
  an equivalent decoupling of identity from the system shard) has to land
  first. No production code changed; `ExportRootKeyContext`/`ImportRootKey`/
  `ImportSealedBlob` are unchanged.

- **BREAKING: `pkg/timeseries.Storage`'s constructor and every write method
  changed shape.** `New` now takes a `storage.Storage` instead of a
  `*pebble.DB`; `Write`, `WritePoint`, `WriteBatch`, `DeleteSeries`,
  `SetRetentionPolicy` and `ApplyRetention` each take a `storage.Batch` as
  their first argument instead of committing one of their own. `NewWithDir`,
  `StorageOption`, `WithEncryptor` and `DB()` are removed from this package;
  the equivalent directory-opening convenience
  (`sdk.NewTimeSeriesStorageWithDir` / `sdk.NewTimeSeriesStorageWithEncryptor`)
  now lives in the SDK, which owns Pebble directly for that standalone use
  case. `pkg/timeseries` no longer imports Pebble at all, enforced by a new
  `test/buildgates` gate alongside `pkg/engine`.
- **BREAKING: every database backend — memory, file, pebble and objstore —
  now composes the MVCC-capable engine.** The plain KV engine
  (`pkg/engine/kv_engine.go`) is no longer composed by
  `pkg/server/lifecycle.NewLocalEngine` or `cmd/qrdb`'s embedded binding for
  any backend: secondary indexes are storage-backed
  (`index.StoragePebbleIndex`, now used unconditionally rather than only
  when a `*pebble.DB` was supplied) and work identically over every
  `storage.Storage`. `engine.WithMVCCPebbleDB` and `engine.WithKVPebbleDB`
  are removed along with the dead `*pebble.DB` fields they set.
  `pkg/engine.TestEngineConformance` runs the same KV, MVCC, transaction,
  CAS, lease, watch, secondary-index, collection, relational-smoke-test and
  time-series scenario against memory, file, pebble and local-provider
  objstore.
- **The objstore storage engine's `Batch` is now atomic (all-or-nothing
  after recovery), matching the file backend**, rather than best-effort. A
  commit publishes a single journal object before touching any key, applies
  the batch, then clears the journal; an interrupted commit is replayed in
  full at the next `New`, and one interrupted before the journal became
  durable leaves no trace. New typed errors: `objstore.JournalCorruptError`,
  `objstore.JournalWriteError`.
- **BREAKING:** `tenant.TenantServicer.TenantBarrierInitWithStrategy`
  gained a trailing `roster *tenant.TenantBarrierShamirRoster` parameter.
  Every existing caller passing `nil` is unaffected; nil (or an empty
  roster) keeps the pre-existing behavior of initializing against
  officers already enrolled standalone.

- **BREAKING: the objstore storage engine stores any key, binary or long,
  under an encoded object name.** It used to name each object with its key
  verbatim, so a binary key, a key holding NUL, control characters, `..` or
  `//`, or one past the provider's 1,024-byte object-name limit was refused
  by the go-objstore provider. A key is now stored as `k/` + its segmented
  lowercase hex, or, when that name would be too long, as `h/` + its
  SHA-256 with the key carried in a verified record. Every key up to the
  65,535-byte global cap round-trips on every provider, and byte order and
  prefix listing are preserved. `objstore.New` marks an empty store with a
  `qrdb-objstore-format` object and refuses a non-empty store without that
  mark (`FormatMarkerMissingError`) or with another version
  (`FormatVersionMismatchError`). A store written by an earlier release is
  therefore refused, not misread, and there is no migration (see
  docs/storage-engines). New typed errors: `objstore.RecordCorruptError`
  and `objstore.KeyMismatchError`.

- **Promote member** (docs/clustering). A new `service.MemberPromoter`
  capability (`AdminService.PromoteMember`) promotes a committed observer of
  a shard to a voter in place, keeping its committed address and identity;
  nothing restarts. Served on every transport: `POST /membership/promote`
  (REST, with leader forwarding), gRPC/Unix `PromoteMember`, QUIC
  `admin.PromoteMember`, the `qrdb_promote_member` MCP tool, the embedded
  transport's `PromoteMember`, `Client.Cluster().PromoteReplica` in the Go
  SDK, and `qrdb admin member promote <replica-id> --shard-id`, gated by
  `admin:add_member` (the same grant `member add` needs). Server-side it is a
  distinct interface from `AdminServicer`, not a method on it, mirroring
  `ClusterJoinTokenMinter`: every existing `AdminServicer` test double keeps
  compiling. Client-side, since every SDK transport now implements it for
  real, it is a plain method on `transport.AdminClient` like
  `AddMember`/`RemoveMember` — no capability type assertion needed.

- **A CSR-enrolled node is admissible once a cluster feature has activated**
  (docs/clustering, `pkg/joinadvertise`). Previously, once any
  `pkg/cluster/featuregate` feature activated, `qrdb admin member add` of a
  node that joined through `qrdb enroll` / member-side CSR join was refused
  with `*featuregate.UnsupportedReplicaError` until the node itself started
  and advertised — which, not yet being a member, it generally could not do
  first; the engineering doc had documented this as a known gap. The joiner
  now sends its own replica ID and claimed features
  (`featuregate.SupportedNames()`) in its join request, and
  `clusterenroll.HandleJoinRequest`, once it has signed and admitted the
  leaf, publishes a short-lived (15-minute) pre-membership advertisement for
  it through the new `clusterenroll.FeatureAdvertiser` interface
  (implemented by `pkg/joinadvertise.Advertiser` over the node's own lease
  and MVCC-protocol KV surfaces, wired in by both `pkg/server/lifecycle`'s
  Noise_NK listener and `pkg/server`'s REST `/v1/enroll/join` route), filtered
  to the feature names the signing member's own binary knows. `member add`
  then sees a live advertisement and admits the node normally. A failure to
  advertise is logged, not fatal: the certificate is still issued, and
  `member add` surfaces the same refusal it always would with no advertiser
  configured.

- **Invitation self-enrolment** (docs/identity-rbac): the way an ordinary
  user gets a first certificate for a key nobody else ever held.
  `users:invite` (ScopeSelfStrict, Admin and SO) creates a PENDING account
  in the caller's own tenant -- or re-invites one still pending -- and
  returns a single-use, short-lived enrolment token exactly once; only its
  SHA-256 digest is stored. Redemption is UNAUTHENTICATED on REST
  (`POST /invitations/redeem`, a public route), native QUIC
  (`invitation.Redeem`, the one frame method served with no permission at
  all), MCP (a separate public `/mcp/enroll` endpoint carrying exactly that
  one tool, since `/mcp` itself answers nothing to an unauthenticated
  caller) and embedded; the redeemer presents the tenant, the token and a
  CSR for their own key (or a bring-your-own certificate over it, verified
  against the tenant's trust anchors), and the account is activated with
  the binding in the one conditional commit that requires it still pending
  (`user.CertActivator.ActivateWithCertBinding`). Every refusal from token
  lookup onward collapses into one opaque, unauthenticated answer, so a
  bad, expired, already-used or ceremony-role-held token cannot be told
  apart.
  - Optional key pinning (`invite.WithKeyPin`): an invitation may be pinned
    to the invited user's SPKI thumbprint, sent out of band, so a stolen
    token redeems with no key but the one it was pinned to.
    `auth.invitation_require_key_pin` refuses every unpinned invitation.
  - Redemption is throttled per source address and per tenant
    (`auth.invitation_redeem_limit.{per_source,per_tenant}`, token-bucket
    policies on `pkg/ratelimit`); a throttled redemption gets the same
    opaque answer as a bad token.
  - `auth.invitation_ttl` (default `24h`, capped at 7 days) bounds how long
    a token stays redeemable.
  - New Go SDK surface: `transport.InvitationClient` (`InviteUser`,
    `RedeemInvitation`), implemented by the embedded, REST and native QUIC
    transports.
  - Not yet implemented: gRPC and the Unix socket (blocked on `protoc`
    availability in the environment this was built in), the TypeScript SDK,
    desktop bindings, and the CLI.

- **Replicated audit chain, phase 1** (docs/audit). `protocol.OpAuditAppend`,
  gated by the new cluster feature `featuregate.AuditAppend`, assigns the
  sequence number and hash digest at apply time in both engine families
  (`pkg/store/pebble.go`, `pkg/engine/mvcc_engine.go`), so every replica
  computes the identical chain from the identical committed entry. Pruning
  carries a low-watermark in the head record and reads nothing from
  storage, so the deleted range is identical regardless of how
  `go-quicraft` happens to pack a batch; it never deletes the newest event
  and is capped per command (`audit.MaxPrunePerCommand`). The reserved
  raw-keyspace table (`pkg/audit.TablePrefix`) rides the same
  snapshot/restore path as the lease table, tested across restart and
  snapshot install on both state machine kinds, and is unreachable through
  a direct `Get`, a transaction compare or an `OpGuarded` compare on every
  engine family (`statemachine.IsReservedKey`, `CompareKeys`), closing the
  same leak the lease table already had on `main`. Both composition roots
  (`cmd/qrdbd/start.go`'s raft wiring and
  `pkg/server/lifecycle.NewLocalEngine`'s standalone wiring) attach the
  chain to every mvcc/pebble state machine unconditionally, exactly like
  the lessor, so no per-node configuration can make one replica of a group
  diverge from another. The service-layer wiring that composes this into
  the existing `AuditLogger` interface -- the opt-in entitlement check, the
  `EmitGated`/`EmitBestEffort` routing, the NDJSON-export-only rewiring --
  is not yet implemented; nothing proposes the new opcode yet. See
  [docs/audit/architecture.md](docs/audit/architecture.md#cluster-wide-replicated-trail).
- **A role binding names only its holder's own tenant (owner ruling,
  superseding the cross-tenant part of #192).** No binding grants anything
  outside its holder's own tenant, and a tenant's own administrators act only
  in their tenant. A system-tenant user could hold a `RoleBinding` naming any
  tenant, and through it administer that tenant's users and roles on every
  transport. Writing a binding that names any tenant other than the user's
  own is now refused with `*user.RoleBindingTenantError` (400 /
  `InvalidArgument`) on REST, gRPC, the unix socket, QUIC, MCP and embedded,
  and by the user store itself, with or without RBAC. The role-assignment
  gate refuses it after authorizing `roles:assign` in the user's tenant,
  before any role is resolved.
  Known gap: a system-tenant Admin can still administer a tenant's users and
  roles through its ordinary roles. Strict tenant isolation, which closes it,
  has not landed.

- **Role bindings are own-tenant only, and the route-level target
  resolution that cross-tenant bindings needed is removed.** Every
  transport's entry permission check now runs in the caller's own tenant.
  Operations that act on a tenant the request names keep their
  handler-level re-check: tenant barrier, tenant record, certificates,
  audit query and enable-encryption. For a caller outside the system tenant,
  a request naming another tenant's user or role is answered not-found, and
  one naming another tenant on a create, list or name lookup is refused with
  `*tenant.CrossTenantDeniedError`.
  Removed from `pkg/auth/middleware`:
  - `Target`, `SelfTarget`, `TenantTarget`, `UserTarget`, `RoleTarget`,
    `ResolveTarget`, `AuthorizeTarget` and `WithRoleService`;
  - `RegisterGRPCTargets` and `GRPCTargetFunc`;
  - `RESTTargetFunc`, `RESTRouteRegistry.SetTarget`, `RESTPathUser`,
    `RESTPathRole`, `RESTPathTenant`, `RESTQueryTenant`, `RESTBody` and
    `RESTTargetBodyLimit`;
  - `QUICTargetFunc`, `MCPTargetFunc` and both registries' `SetTarget`;
  - `TargetBodyInvalidError`, `TargetBodyTooLargeError` and
    `InvalidGRPCTargetError`.

  Also removed: `pkg/api/grpc.IdentityTargets`. `AuthorizeQUIC` and
  `AuthorizeMCP` lose their payload and argument parameters.

  The REST create, lookup and role-assignment routes keep a 1 MiB body bound
  (413) at the handler. There is no compatibility path for a cross-tenant
  binding already stored: clear it before upgrading
  (`qrdb users update --clear-role-bindings`), and administer that tenant
  from an account in it holding its role.

- **Strict tenant isolation: ruled design (docs only).** `docs/multi-tenancy/`
  now carries the owner-ruled contract (2026-10-01, S4 ruled 2026-10-02) for
  keeping a strict tenant's records out of the system tenant. Nothing in the
  design is implemented yet, except where noted below.
  - **Home store.** Every per-tenant record the platform does not need in
    order to place, route, size or retire the tenant moves into a per-tenant
    home store. For `raft` and `barrier` tenants this is a replicated tenant
    home group.
  - **Per-node keys.** Each node holds its own replica key, and no tenant key
    is transported between nodes.
  - **Passthrough.** A node that does not serve a tenant relays the client's
    still-encrypted connection to one that does, routed by SNI.
  - **Isolation floor.** A cluster floor (`strict` by default, or
    `operator-access`) can only be tightened, and a tenant may opt into
    `strict`. A strict tenant always has storage of its own.
  - **First admin (S7) already shipped independently of this ruling**, on
    2026-10-03, as part of tenant onboarding: `admin_username` plus a CSR or
    bring-your-own certificate is required at `tenant create` today, and
    barrier init admits only that admin's certificate. The design doc is
    corrected accordingly; what remains unshipped is the home store, the
    per-node replica keys and the client fan-out across a serving set.
  - **Inventory and plan.** The documents record where every per-tenant record
    lives today, the closed list of records that stay in the system tenant,
    the rulings and a phased implementation plan. Under S4, a strict tenant's
    Raft log is under the tenant key from day one, on a mandatory,
    fail-closed dedicated WAL.
  - **Defects found**, independent of the policy: the etcd listener admits
    the system tenant; a system identity can mint certificates for another
    tenant; audit query is unconfined; system-tenant users may hold bindings
    in any tenant; shard ids are allocated per node.
- **Object storage target design: Pebble remote-storage LSM (docs only).**
  `docs/storage-engines/` records the owner-ruled target design (2026-10-02)
  for `pkg/storage/objstore`: move from one go-objstore object per key — which
  hits go-objstore's NUL-byte, 1024-byte and 255-byte-segment key limits, and
  costs one PUT per write plus listing cost — to an LSM segment layout, by
  composing Pebble v2's `objstorage`/`remote` support (already a qrdb
  dependency) over go-objstore rather than writing a new LSM. Segment object
  names become short and opaque instead of key-derived. Seven decisions still
  need the owner (OS-1..OS-7), each recorded with a recommendation, alongside
  a phased implementation plan and acceptance tests. Design only; no code
  changes.

### Security

- **A system-tenant subject has one rate-limit bucket, not two.** The auth
  middleware's rate-limit key used `identity.TenantID` as given; an empty
  `TenantID` IS the system tenant (`tenantid.Normalize`), so a caller
  authenticated with no tenant bound spent a different bucket than one
  authenticated against the system tenant explicitly, and could double its
  effective budget by alternating between the two. The key now normalizes
  the tenant before folding it in, exactly as `CheckLoginRateLimit` already
  did. See [docs/rate-limit/](docs/rate-limit/).
- **Anonymous embedded share-holder claims are throttled.** A claim begin
  or complete made in-process with no identity on the context is admitted
  without RBAC (the holder's signed nonce is its own authorization) --
  which on a network transport never happens, but is the ordinary case on
  the embedded transport composed with no `AuthMiddleware` -- so it was
  previously unthrottled and a caller could probe holder usernames and
  roster entries in-process as fast as it liked. Every anonymous claim
  begin and complete, on every scope (system unseal and recovery, tenant
  and database barriers), is now charged to its barrier's bucket under the
  same engine and anonymous policy the ceremony already applies to quorum
  challenges (`embedded.WithHolderActLimiter`, installed by `sdk/go/node`
  with a per-node random key prefix), and refused over it with
  `*embedded.HolderActRateLimitedError`. A claim carrying an identity is
  unaffected: it is rate-limited by the auth middleware instead, exactly
  as before. See [docs/shamir-custody/architecture.md](docs/shamir-custody/architecture.md).
- **BREAKING: user key-value keys beginning `__vec:` are refused on every
  write verb and on Get, on both engines.** Put, PutIfAbsent, Delete,
  CompareAndSwap, CompareAndDelete, Batch, Transaction and Guarded now refuse
  a key in that namespace before staging anything, on `MVCCEngine` and
  `KVEngine` alike; an exact-key Get of one is refused the same way. This
  closes two residual gaps the phase-1 fix (B3, below) did not reach: a
  transaction COMPARE and an `OpGuarded` compare could still read a raw
  `__vec:` key's stored bytes, and the compare's own result
  (`CompareResultLess`/`Greater`, or whether the guard held) answered a
  binary-search probe against them -- an extraction oracle, not merely an
  unauthorized write (go-qrdb vector-replicated re-review, B3-residual). Both
  engines now refuse every compare key in a transaction or a guard before any
  compare is evaluated. A `*vector.ForgedKeyError` now classifies as
  PermissionDenied on every transport (`pkg/api/errclass`); it previously fell
  through to each transport's generic, inconsistent default.
  - **Pre-upgrade user data stored under a key beginning `__vec:` is not
    migrated.** No prior release reserved this namespace, so no prior release
    could have written vector records there, but an operator's own data could
    coincidentally begin with those bytes. Old systems are obsolete and ship
    no migration code or scanner for this (consistent with
    `docs/databases/architecture.md` OD-11, superseded in the phase-1 entry
    below): an upgrade that carries such a key will have it refused on the
    first read or write after upgrading, not migrated or deleted automatically.

### Added

- **Vector index, checkpoint and determinism (lane 1B, phase 2 of 5).** Every
  committed vector command now applies a post-commit effect to an in-memory
  ANN index (`pkg/engine.VectorIndexManager`), on both `MVCCEngine` and
  `KVEngine`: HNSW layer assignment and IVF k-means seeding are pure
  functions of the namespace and ID/training generation
  (`vector.DeterministicLayer`, `vector.DeterministicSeed`), never a clock or
  a shared random source, so every replica builds the identical index from
  the identical log. A namespace's index is checkpointed to
  `<dir>/vector-index/<ns>/<seq>.idx` (temp name, synced, renamed; stale
  checkpoints removed) through the binding's `atrest.Encryptor`, and on open
  (`WarmVectorIndex`) a namespace loads its newest valid checkpoint and
  replays the mutation log from there, or rebuilds from the vector records
  when no checkpoint validates -- a checkpoint is a cache, never the only
  copy of anything: deleting one costs a rebuild, never data. A namespace
  whose index is not yet ready answers a search with the new, retryable
  `service.VectorIndexWarmingError`; get, list and metadata search are
  unaffected. `service.VectorSearchQuery` is the new engine `Lookup` query
  this is served through. `WithMVCCVectorIndex` / `WithKVVectorIndex`
  configure a checkpoint directory and encryptor on an engine; without them
  an engine behaves exactly as it did in phase 1. Still not wired to a real
  database binding, Raft snapshot, or any transport -- phases 3 and 4.
- **Vector engine foundation (lane 1B, phase 1 of 5).** New Raft opcodes
  (`protocol.OpVectorNamespaceCreate` and seven siblings, 70-77), a
  `vector-engine` cluster feature gate (`pkg/cluster/featuregate`), and apply
  handlers on both `MVCCEngine` and `KVEngine` (`pkg/engine/vector_handlers.go`)
  that read and write vector namespaces, vectors, and their metadata index
  entirely inside the shared `storage.Batch` -- the same apply path every
  other engine command uses, on every local backend (memory, file, pebble,
  objstore). This is additive: nothing existing is wired to it yet, and the
  node-local Pebble vector store (`pkg/server/lifecycle`) is untouched and
  still serves production traffic pending the remaining phases (index +
  checkpoint + determinism; Raft snapshot + multi-node parity; service-layer
  rewrite and transport parity; backup/restore). See
  [docs/vectors/engineering.md](docs/vectors/engineering.md) "Target: the
  vector engine".
- `pkg/vector`: namespace and vector record types, their binary encoding, and
  the `__vec:` key layout, independent of `pkg/engine` and `pkg/storage`
  (go-qrdb vector-replicated audit, 2026-10-02).

### Fixed

- **docs(vectors): the metadata-index and ID key encoding in
  `docs/vectors/engineering.md` is corrected from raw big-endian bytes to hex.**
  A raw 8-byte big-endian ID or sequence is NUL for every leading byte below
  its value -- the common case, since small sequential IDs have the MOST
  leading zero bytes -- and go-objstore's local provider refuses a key
  containing a NUL byte. Found by running the new engine-foundation test
  matrix across every local backend; see the "Deviation from an earlier
  revision" note in that document's "Keys in the database's store" section.
  `docs/databases/architecture.md` OD-11 is marked superseded: no operator
  import command for the node-local vector stores is built; old systems are
  obsolete and ship no migration code.
- **Phase-1 review findings (B1-B4, N1-N3, N6-N7, N9).** Non-deterministic
  stored record bytes from unsorted map iteration (B1); a refused
  `OpVectorBatchDelete` naming a duplicate ID partially committing on
  MVCCEngine (B2); an ordinary user KV write able to forge a key under the
  reserved `__vec:` namespace on both engines, read side included (B3); an
  empty-string metadata value never indexed or un-indexed (B4); an explicit
  vector ID of `math.MaxUint64` wrapping the namespace's ID allocator to 0
  (N1); a namespace-name validator looser than the documented DNS-label rule
  (N3); one param-block codec shared between `pkg/protocol` and `pkg/vector`
  instead of two (N6). See
  [docs/vectors/engineering.md](docs/vectors/engineering.md) for the
  corrected mutation-log-window (N4) and count/byte-totals-and-quotas (N5)
  documentation, and the one-store-per-database precondition now stated as
  an assertion (N9).
- **Phase-1 re-review findings (B3-residual, namespace-name validation).**
  Every vector apply handler that names a namespace -- not only create --
  now validates the name before it reads the namespace record, returning the
  same typed `*vector.InvalidNamespaceNameError` create already used. A
  database drop's key reclaim does not yet know about `__vec:` keys; this is
  recorded as a phase-4 forward hazard and acceptance test in
  [docs/vectors/engineering.md](docs/vectors/engineering.md), not fixed here
  (neither phase 1 nor phase 2 wires a real per-database binding for it to
  matter against).

### Security

- **`cakey.SoftwareProvider` (Key A, the cluster CA, and Key B, the
  per-tenant login CA) now holds its private key in go-enclave protected
  memory, not a bare `*ecdsa.PrivateKey` on the heap.** The key is sealed
  into a `secure.Secret` (AES-256-GCM under the process Sealer) whenever
  established, opened into an mlocked `secure.Buffer` only for the
  duration of one `Sign` call, and the `Secret` is destroyed outright on
  `Seal` -- closing the gap recorded in
  [docs/barrier-seal/architecture.md](docs/barrier-seal/architecture.md#ca-key-lifecycle).
  The `cakey.Provider`/`crypto.Signer` seam callers use is unchanged: no
  Provider API exposes the key, before or after. The on-disk record format
  and the CA-at-rest ruling (no additional AEAD wrap of the stored record)
  are unaffected -- this closes the IN-MEMORY gap only.

## [0.44.0-alpha] - 2026-10-06

### Highlights

- **Member-side cluster enrolment.** Join tokens, a join-request approval
  queue (`qrdb admin join-request list|approve|reject`), renewal of lapsed
  admissions, retirement of join identities, and pre-membership feature
  advertisement, so a CSR-enrolled node is admissible once a cluster
  feature has activated.
- **Invitation self-enrolment.** `users:invite` and an unauthenticated,
  throttled, single-use redemption with optional key pinning, on every
  transport (`qrdb users invite`, `qrdb enroll`).
- **Promote member.** An observer is promoted to a voter in place on every
  transport (`qrdb admin member promote`, `add-observer`).
- **Tenant Shamir inline roster.** `tenant barrier-init --strategy shamir
  --officers/--custodians`. Re-initialization is refused, and a data race on
  the per-barrier Shamir configuration is fixed.
- **Replicated audit chain, phase 1** (`OpAuditAppend`, feature-gated).
- **Replicated vector index, phases 1–2.**
- Removed the pre-catalog `seal.AbsoluteDataRootRecordError` and
  `seal.TenantRootUnresolvedError`.

Full detail for each item follows in the sections below.

### Removed
- **`tenant.strict_isolation` config key** (`pkg/config`'s
  `TenantConfig.StrictIsolation`, and the `Infra.StrictTenantIsolation` /
  `service.WithStrictTenantIsolation` plumbing beneath it). It governed only
  vector namespace creation under no tenant context, which made it
  dangerously misleading for a reader looking for the deployment's actual
  tenant isolation posture. Its behavior is folded into the new cluster
  isolation-policy floor (see Added, below): a `strict` floor now refuses the
  same un-tenanted vector namespace creation, read live through the new
  `tenant.ClusterIsolationPolicyReader` seam
  (`service.WithClusterIsolationPolicyReader`) rather than a boot-time
  snapshot taken before the tenant service existed. A FIPS-140 build still
  forces strict, now by tightening the cluster floor itself (best-effort, at
  boot) rather than toggling a vector-only flag.

- **`seal.AbsoluteDataRootRecordError` and `seal.TenantRootUnresolvedError`.**
  Both were pre-catalog leftovers: a tenant's location has been a volume pool
  name and slot resolved on each node since the catalog landed, so neither a
  tenant-root record naming a path nor an unresolved pool/slot pair is a
  shape production code can still construct. `pkg/api/errclass`'s
  classification table and tests are updated to match.

### Added

- **The tenant isolation-policy setting** (`pkg/tenant/isolation_policy.go`,
  owner-ruled design: docs/multi-tenancy/architecture.md#the-isolation-policy):
  a cluster-wide floor (`ClusterIsolationPolicyStrict` | `*OperatorAccess`,
  tighten-only compare-and-swap) and a per-tenant opt-in
  (`TenantIsolationPolicyStrict`, tighten-only). A prefix tenant cannot be
  strict — a prefix tenant has no store and no key of its own — so creating
  one under a strict floor, or asking for strict on one, is refused with
  `*tenant.StrictRequiresDedicatedStorageError` before any write, enforced in
  `CreateTenant` and `UpdateTenant` on both the seal-enabled `TenantService`
  and the no-seal `kvTenantService`. This slice ships the Go service API
  (`ClusterIsolationPolicy`/`SetClusterIsolationPolicy` on both tenant
  services, `tenant.WithIsolationPolicy`, `tenant.TenantPatch.IsolationPolicy`)
  and its unit tests; no transport or CLI surface exposes it yet.
- **`tenants:read:cross`, `tenants:delete:cross` and `tenants:update:cross`
  permissions** (`pkg/permission/cross_scope.go`), replacing the
  system-tenant lift `tenants:read`/`tenants:delete`/`tenants:update` relied
  on for reaching another tenant's registry entry (see the breaking RBAC
  change under Changed, below). Granted to the built-in Admin role;
  `AuthMiddleware.AuthorizeTenantTarget` tries the self-scope permission
  first and falls back to the cross one only when the target differs from
  the caller's own tenant. `tenants:update:cross` covers only the display
  name; isolation-type changes remain gated by `tenants:set_isolation`
  (already `ScopeCross`), unaffected by this addition.
- **Replicated audit chain, phase 2: something now proposes
  `protocol.OpAuditAppend`, and every transport can read it back.** Phase 1
  (shipped earlier) built the apply-time mechanism -- `pkg/audit.Chain`, the
  opcode, the feature gate -- but nothing ever proposed to it, so production
  audit stayed node-local only. This closes that gap: a per-tenant opt-in
  entitlement (`quota.Entitlements.AllowReplicatedAudit`, off by default),
  `service.ReplicatedLogger`/`service.OptInAuditLogger` (the `audit.AuditLogger`
  that actually encodes and proposes, gated by the entitlement so an
  unentitled tenant's `Emit` never reaches Raft), and
  `service.SystemShardAuditResolver` (refusing a dedicated-isolation tenant
  explicitly rather than guessing its shard). `lifecycle.BuildServices` wires
  this into every composition's audit sink automatically (`cmd/qrdbd` and the
  SDK node), with the isolation/entitlement lookups resolved lazily against
  `Node.Tenant` since the resolver is built before it exists.
  Also new: `pkg/audit.Chain.Verify`, a hash-continuity check for the
  replicated chain (a different shape from the node-local file chain's own
  `Verify` -- no files, rotation or order, because one tenant has one
  Raft-replicated chain), and a `--replicated` read/verify surface on every
  transport -- REST (`?replicated=true`, `GET /v1/audit/replicated/verify`),
  gRPC/Unix (`AuditService.Query{replicated:true}`,
  `VerifyReplicatedChain`), QUIC (`audit.Query{"replicated":true}`,
  `audit.VerifyReplicatedChain`), MCP (`qrdb_audit_query{replicated:true}`,
  `qrdb_audit_verify_replicated`), the embedded transport and Go SDK
  (`AuditFilter.Replicated`, `AuditClient.VerifyReplicatedChain`), and
  `qrdb audit query --replicated` / `qrdb audit verify --replicated
  [--tenant-id ID]`. See `docs/audit/architecture.md#cluster-wide-replicated-trail`.

- **`tenant barrier-init --strategy shamir` takes its Security Officer/
  custodian roster inline** (`--officers`/`--custodians`,
  `username=path/to/csr.pem`, repeatable), mirroring `qrdb init`'s own
  inline roster for the system barrier: a dedicated tenant's own IAM is
  encrypted behind the very barrier its officers exist to help
  initialize, so standalone `tenant barrier-officer register` cannot
  reach it beforehand. The roster is validated as one pure batch (the
  same validator the system barrier's own M-of-N init path uses,
  `ceremony.ScopedCeremonyService.InitializeShamirWithRoster`) and
  registered atomically with initialization; any later failure --
  registration, signing, or the barrier's own Shamir split -- rolls
  every already-registered officer back, leaving nothing persisted. A
  roster supplied on any strategy but Shamir is refused outright, not
  silently discarded, as is an invalid factor policy. Re-initializing an
  already-initialized Shamir tenant barrier is refused before touching
  the registry or any officer, leaving the original threshold,
  configuration and holder set unchanged. Available on every transport:
  REST, gRPC, QUIC, MCP, Unix and the embedded transport.

### Fixed

- **An invitation lookup or redemption during a storage outage was reported
  as "no such invitation" instead of as the fault it was.**
  `kvstore.KVStore.Get` documents `(nil, nil)` on a miss, and `pkg/invite`'s
  `DAOStore.find` and `.checkUsable` collapsed every failure from the DAO
  into `ErrNotFound`/"not yet redeemed", even though `pkg/dao`'s
  `genericDAO.Get` already reports a genuine miss as the typed
  `dao.NotFoundError` distinct from any other error. Both now use
  `dao.IsNotFound` to tell the two apart and report a real storage fault as
  `*invite.StoreError`, which `pkg/api/errclass` classifies as Internal;
  `InvitationService.RedeemInvitation` lets that type through instead of
  folding it into the single opaque `*InvitationInvalidError` a bad,
  expired or used token gets, so an outage surfaces as a retryable server
  fault rather than as every presented token looking forged.
- **An idle MySQL connection, including one holding an open server-side
  cursor the client stopped fetching from, was held forever** along with
  everything its statement held: QRDB never set vitess
  `ListenerConfig.ConnReadTimeout` / `ConnWriteTimeout`, so
  `sql.query_timeout` -- which bounds a cursor only while it produces rows,
  never while it waits for the next `COM_STMT_FETCH` -- was the only bound
  on the connection, and it does not reach this case. Two new settings,
  `sql.wait_timeout` (default `8h`, MySQL's own default) and
  `sql.write_timeout` (default `60s`, MySQL's `net_write_timeout` default),
  are wired to those vitess fields in `pkg/sql/mysql/server.go`'s
  `(*Server).Start`, the single site that constructs the MySQL listener,
  so an idle connection -- cursor or not -- is closed and its statement's
  resources released after `wait_timeout`. `SELECT @@wait_timeout` now
  reports the configured value. The companion upstream data race on vitess
  `Conn.StatusFlags` (`docs/sql/upstream-gaps.md` item 5) is unaffected and
  remains tracked there as an upstream-only gap.
- **A dedicated tenant's Shamir barrier could not be initialized at all**
  on a tenant whose barrier shell `tenant create` had already registered
  under the node's default strategy: `tenant barrier-init --strategy
  shamir` always failed closed with `qrdb/seal: shamir not configured`.
  `seal.BarrierRegistry.ReconfigureTenantShamir` now installs the Shamir
  strategy and configuration on an existing shell, honoring an explicit
  `--strategy shamir` choice exactly like `InitializeTenantWithStrategy`
  already does for passphrase/none.
- **A data race on the per-barrier Shamir configuration** between
  `SetShamirConfig`/`ReconfigureTenantShamir` and a concurrent `Status()`
  poll, quorum TTL lookup, or `InitializeShamir` call, caught by `go test
  -race` (none of these guarded `config.Shamir` before this). A
  dedicated mutex now guards every access; a published `*ShamirConfig`
  is replaced, never mutated in place.

### Changed

- **docs/multi-tenancy/architecture.md: the 2026-10-01 per-replica-tenant-keys
  ruling is confirmed BLOCKED on the home-store migration, with evidence.**
  An attempt to implement it (independent per-node root keys for
  system-managed `raft` tenants, deleting `seal.ExportRootKeyContext`/
  `ImportRootKey`) was built and then reverted after a real 3-node
  integration run showed it breaks tenant authentication: user/role/auth
  records are encrypted under the tenant's root key and replicated through
  the SHARED SYSTEM SHARD (`pkg/tenant/store_resolver.go`'s `resolveBarrier`,
  "IAM must be resolvable on EVERY replica"), with the ciphertext itself —
  not just the at-rest bytes — baked into the Raft-replicated value
  (`pkg/seal/tenant_shared_kvstore.go`). Every node must therefore hold the
  SAME tenant key purely to authenticate requests, independent of which
  database replicas it hosts; this holds today for every `raft`/`barrier`/
  `volume` tenant, not only the ones the strict-isolation redesign targets.
  The doc's "Per-node-independent key design" and "Per-replica tenant keys"
  sections now state this as a verified prerequisite dependency rather than
  an open question, so the next attempt knows the home-store migration (or
  an equivalent decoupling of identity from the system shard) has to land
  first. No production code changed; `ExportRootKeyContext`/`ImportRootKey`/
  `ImportSealedBlob` are unchanged. Re-checked after the isolation-policy
  slice: it moves no record and tenant identity routing ignores the policy,
  so per-replica keys remain blocked for strict tenants until the home store
  ships, and non-strict dedicated tenants need the shared key by design; the
  per-mode reasons are in the "Per-replica tenant keys" section.

- **BREAKING: the system tenant no longer reaches another tenant's
  `ScopeSelf` data or IAM records** (owner ruling, 2026-10-01: strict tenant
  isolation). `rbac.Authorize`'s `ScopeSelf` case and `tenant.EffectiveTenant`
  both drop their system-tenant lift, unconditionally — this does not depend
  on the new isolation-policy setting above; it applies under every policy
  value. A system-tenant identity creating, reading, updating or deleting a
  user, role, auth factor, invitation or any other `ScopeSelf`-gated record in
  a different tenant is now refused with `*tenant.CrossTenantDeniedError`
  (routing-layer calls: `CreateUser`, `CreateRole`, auth-factor enrolment,
  etc.) or `*rbac.PermissionDeniedError` (RBAC entry checks). A by-id
  operation (`GetUser`, `DeleteRole`, ...) from an identity that cannot reach
  the record's owning tenant now falls back to the caller's own tenant and
  reports not-found, same as it already did for a non-system caller.
  `AuthorizeTenantUpdate`'s base `tenants:update` gate is now conditional on
  the patch actually touching the display name, so a pure isolation-type
  change needs only `tenants:set_isolation` (unaffected, already
  `ScopeCross`) — previously the unconditional `tenants:update` check made
  this path depend on the very lift this change removes.
- **BREAKING:** `tenant.TenantServicer.TenantBarrierInitWithStrategy`
  gained a trailing `roster *tenant.TenantBarrierShamirRoster` parameter.
  Every existing caller passing `nil` is unaffected; nil (or an empty
  roster) keeps the pre-existing behavior of initializing against
  officers already enrolled standalone.

- **Promote member** (docs/clustering). A new `service.MemberPromoter`
  capability (`AdminService.PromoteMember`) promotes a committed observer of
  a shard to a voter in place, keeping its committed address and identity;
  nothing restarts. Served on every transport: `POST /membership/promote`
  (REST, with leader forwarding), gRPC/Unix `PromoteMember`, QUIC
  `admin.PromoteMember`, the `qrdb_promote_member` MCP tool, the embedded
  transport's `PromoteMember`, `Client.Cluster().PromoteReplica` in the Go
  SDK, and `qrdb admin member promote <replica-id> --shard-id`, gated by
  `admin:add_member` (the same grant `member add` needs). Server-side it is a
  distinct interface from `AdminServicer`, not a method on it, mirroring
  `ClusterJoinTokenMinter`: every existing `AdminServicer` test double keeps
  compiling. Client-side, since every SDK transport now implements it for
  real, it is a plain method on `transport.AdminClient` like
  `AddMember`/`RemoveMember` — no capability type assertion needed.

- **A CSR-enrolled node is admissible once a cluster feature has activated**
  (docs/clustering, `pkg/joinadvertise`). Previously, once any
  `pkg/cluster/featuregate` feature activated, `qrdb admin member add` of a
  node that joined through `qrdb enroll` / member-side CSR join was refused
  with `*featuregate.UnsupportedReplicaError` until the node itself started
  and advertised — which, not yet being a member, it generally could not do
  first; the engineering doc had documented this as a known gap. The joiner
  now sends its own replica ID and claimed features
  (`featuregate.SupportedNames()`) in its join request, and
  `clusterenroll.HandleJoinRequest`, once it has signed and admitted the
  leaf, publishes a short-lived (15-minute) pre-membership advertisement for
  it through the new `clusterenroll.FeatureAdvertiser` interface
  (implemented by `pkg/joinadvertise.Advertiser` over the node's own lease
  and MVCC-protocol KV surfaces, wired in by both `pkg/server/lifecycle`'s
  Noise_NK listener and `pkg/server`'s REST `/v1/enroll/join` route), filtered
  to the feature names the signing member's own binary knows. `member add`
  then sees a live advertisement and admits the node normally. A failure to
  advertise is logged, not fatal: the certificate is still issued, and
  `member add` surfaces the same refusal it always would with no advertiser
  configured.

- **Invitation self-enrolment** (docs/identity-rbac): the way an ordinary
  user gets a first certificate for a key nobody else ever held.
  `users:invite` (ScopeSelfStrict, Admin and SO) creates a PENDING account
  in the caller's own tenant -- or re-invites one still pending -- and
  returns a single-use, short-lived enrolment token exactly once; only its
  SHA-256 digest is stored. Redemption is UNAUTHENTICATED on REST
  (`POST /invitations/redeem`, a public route), native QUIC
  (`invitation.Redeem`, the one frame method served with no permission at
  all), MCP (a separate public `/mcp/enroll` endpoint carrying exactly that
  one tool, since `/mcp` itself answers nothing to an unauthenticated
  caller) and embedded; the redeemer presents the tenant, the token and a
  CSR for their own key (or a bring-your-own certificate over it, verified
  against the tenant's trust anchors), and the account is activated with
  the binding in the one conditional commit that requires it still pending
  (`user.CertActivator.ActivateWithCertBinding`). Every refusal from token
  lookup onward collapses into one opaque, unauthenticated answer, so a
  bad, expired, already-used or ceremony-role-held token cannot be told
  apart.
  - Optional key pinning (`invite.WithKeyPin`): an invitation may be pinned
    to the invited user's SPKI thumbprint, sent out of band, so a stolen
    token redeems with no key but the one it was pinned to.
    `auth.invitation_require_key_pin` refuses every unpinned invitation.
  - Redemption is throttled per source address and per tenant
    (`auth.invitation_redeem_limit.{per_source,per_tenant}`, token-bucket
    policies on `pkg/ratelimit`); a throttled redemption gets the same
    opaque answer as a bad token.
  - `auth.invitation_ttl` (default `24h`, capped at 7 days) bounds how long
    a token stays redeemable.
  - New Go SDK surface: `transport.InvitationClient` (`InviteUser`,
    `RedeemInvitation`), implemented by the embedded, REST and native QUIC
    transports.
  - Not yet implemented: gRPC and the Unix socket (blocked on `protoc`
    availability in the environment this was built in), the TypeScript SDK,
    desktop bindings, and the CLI.

- **Replicated audit chain, phase 1** (docs/audit). `protocol.OpAuditAppend`,
  gated by the new cluster feature `featuregate.AuditAppend`, assigns the
  sequence number and hash digest at apply time in both engine families
  (`pkg/store/pebble.go`, `pkg/engine/mvcc_engine.go`), so every replica
  computes the identical chain from the identical committed entry. Pruning
  carries a low-watermark in the head record and reads nothing from
  storage, so the deleted range is identical regardless of how
  `go-quicraft` happens to pack a batch; it never deletes the newest event
  and is capped per command (`audit.MaxPrunePerCommand`). The reserved
  raw-keyspace table (`pkg/audit.TablePrefix`) rides the same
  snapshot/restore path as the lease table, tested across restart and
  snapshot install on both state machine kinds, and is unreachable through
  a direct `Get`, a transaction compare or an `OpGuarded` compare on every
  engine family (`statemachine.IsReservedKey`, `CompareKeys`), closing the
  same leak the lease table already had on `main`. Both composition roots
  (`cmd/qrdbd/start.go`'s raft wiring and
  `pkg/server/lifecycle.NewLocalEngine`'s standalone wiring) attach the
  chain to every mvcc/pebble state machine unconditionally, exactly like
  the lessor, so no per-node configuration can make one replica of a group
  diverge from another. The service-layer wiring that composes this into
  the existing `AuditLogger` interface -- the opt-in entitlement check, the
  `EmitGated`/`EmitBestEffort` routing, the NDJSON-export-only rewiring --
  is not yet implemented; nothing proposes the new opcode yet. See
  [docs/audit/architecture.md](docs/audit/architecture.md#cluster-wide-replicated-trail).
- **A role binding names only its holder's own tenant (owner ruling,
  superseding the cross-tenant part of #192).** No binding grants anything
  outside its holder's own tenant, and a tenant's own administrators act only
  in their tenant. A system-tenant user could hold a `RoleBinding` naming any
  tenant, and through it administer that tenant's users and roles on every
  transport. Writing a binding that names any tenant other than the user's
  own is now refused with `*user.RoleBindingTenantError` (400 /
  `InvalidArgument`) on REST, gRPC, the unix socket, QUIC, MCP and embedded,
  and by the user store itself, with or without RBAC. The role-assignment
  gate refuses it after authorizing `roles:assign` in the user's tenant,
  before any role is resolved.
  Known gap: a system-tenant Admin can still administer a tenant's users and
  roles through its ordinary roles. Strict tenant isolation, which closes it,
  has not landed.

- **Role bindings are own-tenant only, and the route-level target
  resolution that cross-tenant bindings needed is removed.** Every
  transport's entry permission check now runs in the caller's own tenant.
  Operations that act on a tenant the request names keep their
  handler-level re-check: tenant barrier, tenant record, certificates,
  audit query and enable-encryption. For a caller outside the system tenant,
  a request naming another tenant's user or role is answered not-found, and
  one naming another tenant on a create, list or name lookup is refused with
  `*tenant.CrossTenantDeniedError`.
  Removed from `pkg/auth/middleware`:
  - `Target`, `SelfTarget`, `TenantTarget`, `UserTarget`, `RoleTarget`,
    `ResolveTarget`, `AuthorizeTarget` and `WithRoleService`;
  - `RegisterGRPCTargets` and `GRPCTargetFunc`;
  - `RESTTargetFunc`, `RESTRouteRegistry.SetTarget`, `RESTPathUser`,
    `RESTPathRole`, `RESTPathTenant`, `RESTQueryTenant`, `RESTBody` and
    `RESTTargetBodyLimit`;
  - `QUICTargetFunc`, `MCPTargetFunc` and both registries' `SetTarget`;
  - `TargetBodyInvalidError`, `TargetBodyTooLargeError` and
    `InvalidGRPCTargetError`.

  Also removed: `pkg/api/grpc.IdentityTargets`. `AuthorizeQUIC` and
  `AuthorizeMCP` lose their payload and argument parameters.

  The REST create, lookup and role-assignment routes keep a 1 MiB body bound
  (413) at the handler. There is no compatibility path for a cross-tenant
  binding already stored: clear it before upgrading
  (`qrdb users update --clear-role-bindings`), and administer that tenant
  from an account in it holding its role.

- **Strict tenant isolation: ruled design (docs only).** `docs/multi-tenancy/`
  now carries the owner-ruled contract (2026-10-01, S4 ruled 2026-10-02) for
  keeping a strict tenant's records out of the system tenant. Nothing in the
  design is implemented yet, except where noted below.
  - **Home store.** Every per-tenant record the platform does not need in
    order to place, route, size or retire the tenant moves into a per-tenant
    home store. For `raft` and `barrier` tenants this is a replicated tenant
    home group.
  - **Per-node keys.** Each node holds its own replica key, and no tenant key
    is transported between nodes.
  - **Passthrough.** A node that does not serve a tenant relays the client's
    still-encrypted connection to one that does, routed by SNI.
  - **Isolation floor.** A cluster floor (`strict` by default, or
    `operator-access`) can only be tightened, and a tenant may opt into
    `strict`. A strict tenant always has storage of its own.
  - **First admin (S7) already shipped independently of this ruling**, on
    2026-10-03, as part of tenant onboarding: `admin_username` plus a CSR or
    bring-your-own certificate is required at `tenant create` today, and
    barrier init admits only that admin's certificate. The design doc is
    corrected accordingly; what remains unshipped is the home store, the
    per-node replica keys and the client fan-out across a serving set.
  - **Inventory and plan.** The documents record where every per-tenant record
    lives today, the closed list of records that stay in the system tenant,
    the rulings and a phased implementation plan. Under S4, a strict tenant's
    Raft log is under the tenant key from day one, on a mandatory,
    fail-closed dedicated WAL.
  - **Defects found**, independent of the policy: the etcd listener admits
    the system tenant; a system identity can mint certificates for another
    tenant; audit query is unconfined; system-tenant users may hold bindings
    in any tenant; shard ids are allocated per node.
- **Object storage target design: Pebble remote-storage LSM (docs only).**
  `docs/storage-engines/` records the owner-ruled target design (2026-10-02)
  for `pkg/storage/objstore`: move from one go-objstore object per key — which
  hits go-objstore's NUL-byte, 1024-byte and 255-byte-segment key limits, and
  costs one PUT per write plus listing cost — to an LSM segment layout, by
  composing Pebble v2's `objstorage`/`remote` support (already a qrdb
  dependency) over go-objstore rather than writing a new LSM. Segment object
  names become short and opaque instead of key-derived. Seven decisions still
  need the owner (OS-1..OS-7), each recorded with a recommendation, alongside
  a phased implementation plan and acceptance tests. Design only; no code
  changes.

### Security

- **BREAKING: user key-value keys beginning `__vec:` are refused on every
  write verb and on Get, on both engines.** Put, PutIfAbsent, Delete,
  CompareAndSwap, CompareAndDelete, Batch, Transaction and Guarded now refuse
  a key in that namespace before staging anything, on `MVCCEngine` and
  `KVEngine` alike; an exact-key Get of one is refused the same way. This
  closes two residual gaps the phase-1 fix (B3, below) did not reach: a
  transaction COMPARE and an `OpGuarded` compare could still read a raw
  `__vec:` key's stored bytes, and the compare's own result
  (`CompareResultLess`/`Greater`, or whether the guard held) answered a
  binary-search probe against them -- an extraction oracle, not merely an
  unauthorized write (go-qrdb vector-replicated re-review, B3-residual). Both
  engines now refuse every compare key in a transaction or a guard before any
  compare is evaluated. A `*vector.ForgedKeyError` now classifies as
  PermissionDenied on every transport (`pkg/api/errclass`); it previously fell
  through to each transport's generic, inconsistent default.
  - **Pre-upgrade user data stored under a key beginning `__vec:` is not
    migrated.** No prior release reserved this namespace, so no prior release
    could have written vector records there, but an operator's own data could
    coincidentally begin with those bytes. Old systems are obsolete and ship
    no migration code or scanner for this (consistent with
    `docs/databases/architecture.md` OD-11, superseded in the phase-1 entry
    below): an upgrade that carries such a key will have it refused on the
    first read or write after upgrading, not migrated or deleted automatically.

### Added

- **Vector index, checkpoint and determinism (lane 1B, phase 2 of 5).** Every
  committed vector command now applies a post-commit effect to an in-memory
  ANN index (`pkg/engine.VectorIndexManager`), on both `MVCCEngine` and
  `KVEngine`: HNSW layer assignment and IVF k-means seeding are pure
  functions of the namespace and ID/training generation
  (`vector.DeterministicLayer`, `vector.DeterministicSeed`), never a clock or
  a shared random source, so every replica builds the identical index from
  the identical log. A namespace's index is checkpointed to
  `<dir>/vector-index/<ns>/<seq>.idx` (temp name, synced, renamed; stale
  checkpoints removed) through the binding's `atrest.Encryptor`, and on open
  (`WarmVectorIndex`) a namespace loads its newest valid checkpoint and
  replays the mutation log from there, or rebuilds from the vector records
  when no checkpoint validates -- a checkpoint is a cache, never the only
  copy of anything: deleting one costs a rebuild, never data. A namespace
  whose index is not yet ready answers a search with the new, retryable
  `service.VectorIndexWarmingError`; get, list and metadata search are
  unaffected. `service.VectorSearchQuery` is the new engine `Lookup` query
  this is served through. `WithMVCCVectorIndex` / `WithKVVectorIndex`
  configure a checkpoint directory and encryptor on an engine; without them
  an engine behaves exactly as it did in phase 1. Still not wired to a real
  database binding, Raft snapshot, or any transport -- phases 3 and 4.
- **Vector engine foundation (lane 1B, phase 1 of 5).** New Raft opcodes
  (`protocol.OpVectorNamespaceCreate` and seven siblings, 70-77), a
  `vector-engine` cluster feature gate (`pkg/cluster/featuregate`), and apply
  handlers on both `MVCCEngine` and `KVEngine` (`pkg/engine/vector_handlers.go`)
  that read and write vector namespaces, vectors, and their metadata index
  entirely inside the shared `storage.Batch` -- the same apply path every
  other engine command uses, on every local backend (memory, file, pebble,
  objstore). This is additive: nothing existing is wired to it yet, and the
  node-local Pebble vector store (`pkg/server/lifecycle`) is untouched and
  still serves production traffic pending the remaining phases (index +
  checkpoint + determinism; Raft snapshot + multi-node parity; service-layer
  rewrite and transport parity; backup/restore). See
  [docs/vectors/engineering.md](docs/vectors/engineering.md) "Target: the
  vector engine".
- `pkg/vector`: namespace and vector record types, their binary encoding, and
  the `__vec:` key layout, independent of `pkg/engine` and `pkg/storage`
  (go-qrdb vector-replicated audit, 2026-10-02).

### Fixed

- **docs(vectors): the metadata-index and ID key encoding in
  `docs/vectors/engineering.md` is corrected from raw big-endian bytes to hex.**
  A raw 8-byte big-endian ID or sequence is NUL for every leading byte below
  its value -- the common case, since small sequential IDs have the MOST
  leading zero bytes -- and go-objstore's local provider refuses a key
  containing a NUL byte. Found by running the new engine-foundation test
  matrix across every local backend; see the "Deviation from an earlier
  revision" note in that document's "Keys in the database's store" section.
  `docs/databases/architecture.md` OD-11 is marked superseded: no operator
  import command for the node-local vector stores is built; old systems are
  obsolete and ship no migration code.
- **Phase-1 review findings (B1-B4, N1-N3, N6-N7, N9).** Non-deterministic
  stored record bytes from unsorted map iteration (B1); a refused
  `OpVectorBatchDelete` naming a duplicate ID partially committing on
  MVCCEngine (B2); an ordinary user KV write able to forge a key under the
  reserved `__vec:` namespace on both engines, read side included (B3); an
  empty-string metadata value never indexed or un-indexed (B4); an explicit
  vector ID of `math.MaxUint64` wrapping the namespace's ID allocator to 0
  (N1); a namespace-name validator looser than the documented DNS-label rule
  (N3); one param-block codec shared between `pkg/protocol` and `pkg/vector`
  instead of two (N6). See
  [docs/vectors/engineering.md](docs/vectors/engineering.md) for the
  corrected mutation-log-window (N4) and count/byte-totals-and-quotas (N5)
  documentation, and the one-store-per-database precondition now stated as
  an assertion (N9).
- **Phase-1 re-review findings (B3-residual, namespace-name validation).**
  Every vector apply handler that names a namespace -- not only create --
  now validates the name before it reads the namespace record, returning the
  same typed `*vector.InvalidNamespaceNameError` create already used. A
  database drop's key reclaim does not yet know about `__vec:` keys; this is
  recorded as a phase-4 forward hazard and acceptance test in
  [docs/vectors/engineering.md](docs/vectors/engineering.md), not fixed here
  (neither phase 1 nor phase 2 wires a real per-database binding for it to
  matter against).

## [0.43.0-alpha] - 2026-10-06

Compliance credentials, tenant onboarding, replica forwarding, chunked import, volume pools and backup encryption; main's CI is fully green again. This release contains BREAKING changes; see the entries below. Highlights:

- **Compliance credentials.**
  - `sign-csr` issues holder certificates only.
  - `mtls_binding_policy` defaults to `required`.
  - A node boots into "awaiting admin credential" instead of refusing to boot.
  - Sensitive ceremony secrets live in `secure.Buffer`.
- **Tenant onboarding.**
  - A tenant's first admin is created at tenant create, from a CSR or a certificate.
  - Tenant trust anchors and cert enrollment.
  - The admin cert-binding guard, and `cert.revoke` audit events.
- **Replica forwarding.** A node that holds no replica forwards to the placement leader on REST, gRPC and QUIC; streams are refused.
- **Chunked import.**
  - Chunked unary import sessions replace BatchImport, with staging unreachable from the client data plane.
  - Commits are retry-safe across leader changes.
  - Per-peer connection caps on every transport, including native QUIC and HTTP/3.
- **Volume pools.**
  - Per-tenant volume pools with a `KeyProvider` hook (software default; go-xkms supplies hardware).
  - Placement filtering, and stores closed before a volume is locked.
- **Backups.** Backups are encrypted by default through the `keywrap` hook; there's a restore path and an opt-in for plaintext.
- **Embedded authorization.** Every embedded-transport method is authorized; forwarded calls are checked twice.
- **Storage.** The file backend uses a hashed flat layout, with a global 64 KiB key cap.
- **GORM.** The stock gorm mysql dialector; the custom dialector is deleted.
- **Fixes.**
  - Concurrent first boot no longer kills losing nodes on the admin cert binding.
  - Memory-backend nodes keep their seal store in memory.
  - Path traversal in `qrdb certs export`.
  - HTTP/3 shutdown race.
  - The cluster integration suite is now required on every PR.

### Added

- **Replica forwarding** (docs/replica-forwarding). A node that holds no
  replica of a `raft`-isolated database forwards a request for it to a
  replica, on the caller's behalf, and returns the replica's answer, instead
  of refusing with `NotAReplicaError`. It works over REST, gRPC, the Unix
  socket, QUIC and MCP, and for the embedded client.
  - The original caller travels as the signed on-behalf assertion, bound to
    the request on every transport. The target re-authorizes the caller with
    roles it resolves itself. A request with no authenticated caller is not
    forwarded.
  - Only a node certificate the cluster CA issued may forward: REST, gRPC,
    the Unix socket and QUIC share one on-behalf gate that, with an issuer
    verifier wired, requires `IsClusterCA`. On a BYO-TLS node the operator's
    peer CA is the cluster CA; a tenant CA's leaf with a forged node SAN is
    refused.
  - Hop limit 1. At most two targets answer: a follower refusing a write
    names the leader, and the one retry goes there. An unreachable replica
    is skipped. A forward that failed after it may have reached a target is
    never retried.
  - Streams are refused with the replica set (MCP's bounded `qrdb_watch` is
    forwarded).
  - A per-shard leader cache, read lock-free, remembers where a forward was
    served. A placement or member-record change invalidates it on every
    node.
  - Configured by `replica_forwarding.enabled` (default on) and
    `replica_forwarding.timeout` (default `10s`), or `--replica-forwarding`
    and `--replica-forwarding-timeout`. A server asked to forward without a
    CA pool (`server.WithCACertPool`) forwards nothing and says so once.
  - Metrics `qrdb_replica_forward_total{protocol,outcome}` and
    `qrdb_replica_forward_duration_seconds{protocol}`.
- **The replica refusal is structured on every transport.** A
  `NotAReplicaError` carries its replica set, and a follower's `NotLeader`
  carries its leader:
  - REST: `code`/`metadata` in the error body;
  - gRPC and the Unix socket: a `google.rpc.ErrorInfo` detail in domain
    `qrdb.v1`;
  - QUIC: the same detail in the error frame (`wire.ErrorPayload.Info`);
  - MCP: the error envelope's `reason`/`metadata`.
  `NotAReplicaError` also carries the placement's `LeaderReplicaID`.
- **Every raft-mode node publishes its lease-backed member record** (Raft
  address, API endpoints, liveness) through `cluster.Advertiser`, with or
  without `--dynamic-membership`. This applies to `qrdbd` and to clustered
  `sdk/go/node` nodes.
- QUIC method `forward.OnBehalf`, served only to a cluster node, carries a
  forwarded request: an envelope of the caller's method and payload, plus
  the assertion. The frame format is unchanged.
- `forward.SignCaller`, `forward.NodeIdentity` and `forward.RPCBinding` sign
  and verify on-behalf assertions for any transport.
  `AuthMiddleware.AuthenticateHTTP` and `AuthenticateQUICOnBehalf` verify
  them on MCP and QUIC; the gRPC unary interceptor verifies them for gRPC
  and the Unix socket.

- **The embedded client forwards as the caller its embedder configures**
  (`node.WithEmbeddedCaller`, a client certificate and its key). A database-
  scoped embedded client's data-plane call that its node refuses as a
  non-replica is sent, over QUIC, as the `forward.OnBehalf` request a QUIC
  client's forward sends, signed for that caller; the replica authorizes the
  caller, never the node. Each forward authenticates the certificate as the
  QUIC listener authenticates a client's, through the node's one
  authentication chain (the mTLS authenticator, or one a composer added, such
  as an attested-host authenticator). With no caller configured the call is
  refused with `NotAReplicaError`. Streams are not forwarded. New surface:
  `embedded.WithReplicaForwarding` and `embedded.ReplicaForwarder`,
  `quic.NewFrameTransport` (SDK), `quic.FrameForwarder` (`pkg/api/quic`),
  `Server.ReplicaForwarder` and `Server.AuthenticateCertificate`.
- `lifecycle.Node.PlacementChanges`, the placement table's change feed: every
  placement written or deleted, as the node applies it. Replica forwarding
  invalidates its leader cache from it. `Node.PlacementStore` keeps no feed,
  so the tenant shard reconciler's convergence is unchanged.
- **`--backup-key-dir`** (`qrdbd start`): the directory the built-in `"software"` key-wrap provider's key files must live inside. Required to create or restore an encrypted backup with the built-in provider; kept outside `--data-dir`/`--backup-dir` on purpose, so a backup made with a key from this directory is still restorable after `--data-dir` is lost entirely. `--restore-decryption-key-file`'s value is now a bare file NAME inside this directory, never a path.
- **`server.WithKeyDir`** mirrors `--backup-key-dir` for every embedder that composes through `pkg/server`/`sdk/go/node` (both the seal-enabled and seal-disabled construction paths) rather than `cmd/qrdbd` directly, forwarded via the same `ResolveCryptoOptions`/`lifecycle.Infra.KeyDir` path `WithStrategyRegistry` already uses.
- **Tenant onboarding with the first admin.** A tenant is created together
  with its first admin's login credential, or not at all:
  `service.TenantOnboardingServicer.OnboardTenant`, wired with
  `server.WithTenantOnboarding` and reached by tenant create on every
  transport. The request names `admin_username` and exactly one of
  `admin_csr_pem` (signed by the new tenant's own login CA, Key B, through
  `bootstrap.KeyBSigner`) or `admin_cert_pem` (+ optional `admin_chain_pem`),
  which must chain to `trust_anchors_pem` registered in the same call, carry
  the admin username as its CN and name the new tenant. The response carries
  the admin's certificate, serial and SPKI thumbprint, never a private key.
  Every refusable input is judged before anything is written, because a
  deleted tenant id is retired; a failure after the first write is undone in
  reverse (`bootstrap.ProvisioningUndoneError`, whose `UndoFailures` lists any
  undo step that failed). CLI: `qrdb tenant create --admin-username U
  (--admin-csr F | --admin-cert F [--admin-chain F]) [--trust-anchors F]
  [--out DIR]`.
- **Pending tenant admins** (`pkg/tenantadmin`). A `barrier`, `volume` or
  `raft` tenant keeps its accounts behind its own barrier, which does not
  exist at creation, so its first admin's certificate is issued or verified
  and recorded as the tenant's pending admin, and the response reports
  `awaits_barrier_init`. The account is created, granted the tenant's Admin
  role and bound by `bootstrap.CompletePendingTenantAdmin`, which qrdb's
  composers register as the tenant service's barrier-ready hook
  (`tenant.WithBarrierReadyHook`): it runs after the barrier is initialized,
  after each unseal, and on each status read that finds it unsealed, and is
  idempotent. `middleware.WithPendingTenantAdmins` and
  `wiring.AuthMiddlewareConfig.PendingTenantAdmins` give the trust-on-first-init
  window the record (see Security).
- **Certificate issue and enrolment for existing accounts.**
  `service.CertEnrollmentServicer` (`SignCertificate`, `EnrollCertificate`),
  wired with `server.WithCertEnrollment`, behind new verbs on all six
  transports: REST `POST /certs/issue` and `POST /certs/enroll`, gRPC
  `IssueCert` and `EnrollCert`, QUIC `cert.Issue` and `cert.Enroll`, MCP
  `qrdb_cert_issue` and `qrdb_cert_enroll`, and the Go SDK's
  `CertClient.IssueCert` and `CertClient.EnrollCert` (`IssueCertRequest`,
  `EnrollCertRequest`, `EnrolledCert`). Issue signs a CSR with the caller's
  own tenant's Key B CA; enrol verifies a bring-your-own certificate against
  the tenant's trust anchors and that it authenticates as exactly that
  account. Both act only on an existing account of the caller's own tenant
  (`*user.UserNotFoundError` otherwise) and bind the certificate's SPKI to it
  in the same call; if the binding fails after signing, the serial is revoked
  (`service.CertBindingFailedError`). New permissions `certs:issue` and
  `certs:enroll`, `ScopeSelfStrict`, held by the built-in SO and Admin roles.
  CLI: `qrdb ca issue --username U [--csr F] [--ttl D] [--out DIR]` (without
  `--csr` the key is generated locally and written with the certificate to
  `--out`), `qrdb ca enroll --username U --cert F [--chain F]`, and
  `qrdb ca csr --username U --out DIR`, which makes a key and a CSR locally.
- `cert.ParseCSR` and `cert.CheckClientKey` (`*cert.InvalidCSRError`): a CSR's
  signature must verify, and the key must be ECDSA P-256, P-384 or P-521, RSA
  of at least 2048 bits, or Ed25519. Only the CSR's key is used. `pkg/certenroll`
  holds the shared issue-from-CSR and bring-your-own verification steps.
  `pkg/cert/certtest` is a test helper that generates a key locally and calls
  `IssueClientCert`.
- **Per-tenant trust anchors** (`pkg/trustanchor`): CA certificates a tenant
  registers so its users can bring certificates from their own CA, at most
  `trustanchor.MaxAnchorsPerTenant` (32) per tenant. Verbs on all six
  transports: REST `GET|POST /v1/tenants/{id}/trust-anchors` and
  `DELETE /v1/tenants/{id}/trust-anchors/{fingerprint}`, gRPC
  `TrustAnchorAdd`/`TrustAnchorList`/`TrustAnchorRemove`, QUIC
  `tenant.TrustAnchorAdd`/`TrustAnchorList`/`TrustAnchorRemove`, MCP
  `qrdb_trust_anchor_add`/`_list`/`_remove`, and the Go SDK's
  `TenantClient.AddTrustAnchors`/`ListTrustAnchors`/`RemoveTrustAnchor`.
  Permissions `trust_anchors:add`, `trust_anchors:list` and
  `trust_anchors:remove`, `ScopeSelfStrict`. CLI: `qrdb tenant trust-anchors
  add|list|remove`. The built-in store (`trustanchor.NewKVRegistry`) can be
  replaced with `server.WithTrustAnchors`, which `sdk/go/node` and the
  `qrdb_embedded` binding (`EmbeddedServerBinding.SetTrustAnchors`) forward.
- `cert.WithNewCAHook`: run when a `DefaultCertProvider` first comes to hold
  a login CA (a tenant's Key B minted on its first issuance, or loaded after a
  restart). The lifecycle wires it, with every trust-anchor change, into
  `lifecycle.Node.ClientTrustChanges`, which `Node.WireTrustAnchors`
  subscribes the client trust pool's `Invalidate` to: a tenant onboarded a
  moment ago logs in on that node at once instead of after the pool's
  two-second cache. `server.WithTrustAnchors` injects an embedder's trust
  store (resolved by `server.ResolveCryptoOptions`).
- TypeScript SDK: `certs.issueCert`, `certs.enrollCert`,
  `certs.issueCertWithLocalKey` (desktop transport only; the REST transport
  throws `DesktopOnlyError`), and `tenants.addTrustAnchors`,
  `tenants.listTrustAnchors`, `tenants.removeTrustAnchor`. Desktop bindings:
  `CertBinding.Issue` and `CertBinding.Enroll`, and `TenantBinding`'s
  trust-anchor methods.

### Security

- **BREAKING: every API transport now applies one small pre-auth message
  cap, instead of a transport-specific limit sized for a single-request
  bulk import.** Before this change, gRPC and Unix accepted a 101 MiB
  message, QUIC's wire protocol allocated up to 64 MiB for a single
  stream's declared payload length *before checking who was asking* (and
  with up to 1000 concurrent streams per connection, before this change,
  that multiplies), REST had no body cap at all, and MCP's cap was sized
  to carry a base64-encoded import file. Any TLS peer that never
  authenticates could force these allocations. Every transport now shares
  one configurable cap, `transport.max_message_bytes`, defaulting to 4 MiB
  (`config.DefaultTransportMaxMessageBytes`) with no larger-message
  opt-in: REST's request body (`http.MaxBytesReader`, the outermost
  middleware layer, ahead of authentication, CORS and routing), gRPC's and
  Unix's `MaxRecvMsgSize`/`MaxSendMsgSize`, QUIC's wire frame payload
  (`wire.ReadRequestLimited`, which checks the frame's declared length
  against the cap *before* allocating a buffer for it), and MCP's
  JSON-RPC request body. A caller with a larger payload (bulk import)
  uses the chunked import session API below instead of a single oversized
  request.
- **New per-peer connection caps on every TCP AND QUIC listener.**
  `transport.max_conns_per_peer` (default 256) bounds how many concurrent
  connections a single remote IP may hold open against REST's HTTP/2
  listener, gRPC, MCP, native QUIC, and REST's own HTTP/3 (QUIC) listener
  -- the same configured value, enforced by the same accounting on all
  five. Previously none of them bounded this at all — on MCP the only
  thing standing in a single peer's way was the process's
  open-file-descriptor limit, a GLOBAL budget one peer could exhaust on
  behalf of every other caller. `pkg/api/connlimit.Counter` is the shared
  primitive: `connlimit.Listener` applies it on REST's, gRPC's and MCP's
  raw TCP accept, ahead of TLS negotiation; `connlimit.QUICListener`
  applies the identical accounting to native QUIC's `*quic.Transport`
  listener and to the `*quic.Listener` REST's HTTP/3 server serves via
  `http3.Server.ServeListener` (rather than handing it a bare
  `net.PacketConn` via `Serve`, which has no listener to wrap). A
  connection over its peer's cap is refused at `Accept` -- closed with
  `connlimit.QUICOverCapErrorCode` on QUIC/HTTP-3, before any stream
  exists on it -- and its slot is released when the connection's own
  context ends (`context.AfterFunc(conn.Context(), release)`), covering
  every way a QUIC connection ends (idle timeout, either side's
  `CloseWithError`, a transport error) without this package needing to
  know which. The Unix domain socket has no remote-IP concept and is
  unaffected. QUIC additionally has `transport.max_streams_per_conn`
  (default 128, down from a fixed 1000), bounding concurrent streams per
  connection (go-qrdb#576).
- New config section `transport` (`pkg/config.TransportConfig`):
  `max_message_bytes`, `max_conns_per_peer`, `max_streams_per_conn`. See
  `docs/12-configuration.md`.
- **Fixed: the gRPC and Unix servers no longer cap their own RESPONSES at
  the pre-auth receive cap.** Both used to set `grpc.MaxSendMsgSize` to the
  same `transport.max_message_bytes` bound as `grpc.MaxRecvMsgSize`, so an
  ordinary `kv range`/`scan` whose matches exceeded 4 MiB failed with
  `codes.ResourceExhausted` on the SERVER side regardless of what the
  caller was willing to receive — the pre-auth exposure this cap exists for
  is entirely about an unauthenticated peer forcing an allocation on
  RECEIVE, and says nothing about a response the server itself chooses to
  send. `MaxSendMsgSize` is no longer set (grpc-go's own server-side
  default is effectively unbounded); the Go SDK's gRPC and Unix clients
  correspondingly raise their own `MaxCallRecvMsgSize` past grpc-go's 4 MiB
  CLIENT default (`transport.DefaultClientMaxRecvMessageBytes`), so neither
  side truncates a response the other was willing to send.
- **BREAKING: the single-message `BatchImport` RPC is deleted outright, no
  compat, replaced by a chunked import session** (`ImportBegin`/
  `ImportAppend`/`ImportCommit`/`ImportAbort`) identical on every
  transport (REST, gRPC, Unix, QUIC, MCP, embedded), both SDKs, and the
  CLI. A session carries a dataset of any size as many small, ordinary,
  authenticated calls instead of one message no transport's pre-auth cap
  can admit: each chunk is staged as replicated, lease-bound state under a
  reserved raw key range (`\x00qrdb.import/`, `pkg/importstage`) written
  only through a dedicated Raft command, never through the MVCC store or
  the KV engine's own `Put` path — on every `raft.sm_type`, not only
  `mvcc` — exactly like the lease table's own storage, so a staged record
  is structurally unreachable from a client's `Get`/`Range`/`Put` of the
  same literal key, not merely hidden from enumeration. It is bound to the
  caller's (tenant, identity) at `ImportBegin` and re-checked on every
  subsequent call against the SAME (tenant, database) namespace
  `ImportBegin` was called through — a session begun on one database can no
  longer be appended to, committed, or aborted through a different
  namespaced database wrapper of the same tenant — and reclaimed by the
  ordinary lease-expiry cascade if abandoned. `ImportCommit` parses and
  applies every staged chunk, at most 8 MiB of encoded rows per Raft
  proposal (`--transactional` above that ceiling is refused outright, with
  no non-atomic fallback); a proposal whose outcome is genuinely ambiguous
  aborts the whole commit without revoking the session, so it stays
  retryable, rather than silently reporting success as it could before.
  - `qrdb batch import` keeps its existing flags and output; it now
    drives the session transparently through the Go SDK's
    `transport.ImportFileChunked` helper. Every transport but MCP chunks
    at `transport.max_message_bytes`; MCP, whose JSON-RPC body carries a
    chunk as base64 text, chunks smaller
    (`transport.DefaultMCPImportChunkBytes`) so the base64-encoded chunk
    plus its envelope still fits the same cap — a 3 MiB chunk previously
    encoded to exactly the cap's size, leaving no room for the envelope
    around it, and every MCP import above roughly 3 MiB failed outright.
    There is no longer a maximum import file size on any transport.
  - The Go SDK's `transport.BatchClient` gains `ImportBegin`/
    `ImportAppend`/`ImportCommit`/`ImportAbort`; `ImportFile` is
    unchanged at the call site but now drives the session underneath.
  - The desktop app and the TypeScript SDK's Wails binding inherit the
    chunked session transparently (they call the same Go SDK
    `ImportFile`); the TypeScript SDK's REST client does not implement
    batch import at all, unaffected by this change.
  - `import_session:` (`pkg/config.ImportSessionConfig`) is now actually
    wired into every composer that builds a `BatchServicer` — the shared
    system shard (`pkg/server/lifecycle`), the per-tenant `IsolationVolume`
    and `IsolationRaft` hosts (`pkg/server`), and the embedded transport
    (`sdk/go/transport/embedded`) — instead of silently running every
    session at the package defaults regardless of configuration.
  - See `docs/transactions/` ("Import session") for the full design.

- **Volume pool management is now Tier 1: every capability on all six
  transports** (docs/volume-pools/api.md). `volume.Service` (list, inspect,
  create, update, delete a pool, list a pool's assigned tenants) is wired
  into REST (`/v1/volume-pools...`), gRPC (`VolumePool*` RPCs on
  `qrdb.v1.QRDBService`, regenerated proto), Unix (the same gRPC wire over a
  UDS), QUIC (`volumepool.*` frame methods), MCP (`qrdb_volume_pool_*`
  tools), and the embedded transport, all sourced from the SAME catalog
  instance already attached to the barrier registry
  (`seal.BarrierRegistry.VolumeService`) -- no composer needs a second copy.
  Adds the Go SDK's `Client.VolumePools()` (replacing the removed in-process
  `VolumePoolClient`, decision D8), the TypeScript SDK's `VolumePoolClient`
  (REST-backed; not yet implemented in desktop/Wails mode), and the CLI's
  `qrdb volume-pool` command group (list/get/create/update/delete/tenants).
  Every capability is `ScopeCross`-gated behind the renamed
  `volume_pools:{list,read,create,update,delete}` permissions (previously
  dead, pre-catalog `volumepool:*` placeholders); `pkg/api/errclass` gains a
  `volumeRules` block classifying every `pkg/volume` typed error by cause
  (`PoolNotFoundError` -> NotFound, `PoolCapacityExceededError` ->
  ResourceExhausted, ...), checked ahead of `tenant.VolumePoolReserveError`'s
  own catch-all.
- The tenant surface gains `volume_pool`/`refusal` (`TenantRefusal`:
  `pool_unavailable` | `provider_missing`) and
  `TenantEntitlements.allowed_volume_pools` on every transport and the CLI's
  `tenant set-entitlement --allowed-volume-pools`; `pkg/tenant.RefusalOf`
  classifies a `ListTenants` refusal into its wire form once, reused by
  every transport. `pkg/volume`'s wire-facing catalog types (`Pool`,
  `Assignment`, `PoolView`, `PoolDetail`, ...) gain the `camelCase` JSON
  tags docs/volume-pools/api.md specifies.
- `docs/volume-pools/`: the owner's ruling on named volume pools (2026-10-04)
  is recorded as a "Decisions" section across all five documents, replacing
  the prior "PROPOSED" status. Decision D6 (encrypted volumes and the
  cluster seal) is replaced by a "seal-lifecycle volumes" design: LUKS is a
  `VolumeProvider` concern, not a `SealStrategy`; the optional hook is
  `volume.Lockable` (`Unlock(ctx, VolumeKey)` / `Lock(ctx)`); qrdb derives
  the per-volume key from the unsealed system barrier with HKDF and never
  persists it; ordering (unlock after unseal and before stores open, lock
  after stores close and before the key wipe) is enforced by qrdb. Tenant
  moves (D5) are deferred to a Phase 2 noted throughout the folder.
- `pkg/volume`: the `VolumeProvider` seam -- `Provider`, `Volume`,
  `Registry`, the dependency-free `dir` provider, and the `Lockable` /
  `VolumeKey` / `DeriveVolumeKey` extension decision D6 rules. No catalog,
  assignment or management-service implementation yet; that is the next
  slice of the Phase 1 build.
- `pkg/seal`: `StorageBarrier.OnSealing`, the sealing-side counterpart to
  the existing `OnUnsealed` hook, and `VolumeLifecycle`, which tracks a
  node's `Lockable` pool volumes and unlocks/locks them in step with the
  system barrier's own unseal/seal, proven with a fake `Lockable` covering
  the ordering and both failure paths.
- `quota.Entitlements` gains `AllowedVolumePools` and the `AllowsVolumePool`
  / `DefaultVolumePool` accessors, fail-closed on an empty list, ahead of
  the volume-pools entitlement check at tenant create/move.
- `placement.ReplicaFilter` gains the tenant id, and `WithReplicaFilter`
  now ANDs filters attached by separate calls instead of the later call
  overwriting the earlier one -- ahead of a volume-pools pool-health
  filter composing alongside the existing feature-gate filter.
- `pkg/volume`: `Catalog`, the replicated pool catalog, tenant assignments
  and node health reports (`__sys:volumepool/{pools,assign,nodes}/`), with
  no node-local file. It implements both `volume.Service` (the management
  surface) and `volume.Allocator` (`Reserve`/`Resolve`/`Release`, the
  narrower seam pkg/tenant and pkg/seal's `BarrierRegistry` depend on
  instead). `Reserve`/`Release` use one Raft transaction each so a pool's
  `ReservedBytes` and its tenant's `Assignment` move together.
  `MoveTenant`/`GetMove` return `*volume.MoveNotImplementedError`: tenant
  moves are Phase 2 (decision D5).
- `pkg/seal`: `BarrierRegistry.HasVolumePool`, the single presence gate
  ("a volume.Allocator AND a VolumeResolver are both attached") composition
  layers (`pkg/server.TenantInfraConfig`, `cmd/qrdbd`, `sdk/go/node`) use in
  place of the old "is a `*pool.VolumePoolSubsystem` configured" check, and
  `BarrierRegistry.VolumeAllocator`, which returns the attached allocator so
  `pkg/server/lifecycle.BuildServices` can forward it into the
  `TenantService` it constructs.
- `pkg/server/lifecycle`: `buildTenantServiceOpts` now wires
  `tenant.WithVolumeAllocator` from `Infra.BarrierRegistry`, and
  `NewVolumeCatalogKVServicer` adapts a `service.KVServicer` to
  `volume.KVServicer` -- translating a miss into `(nil, nil)`, the contract
  `volume.Catalog.Reserve`/`Resolve` require to recognize an absent
  Assignment/Pool rather than treat `service.KVServicer`'s own typed
  not-found error as a hard failure.
- `cmd/qrdbd` and `sdk/go/node`: full volume-pools composition (Step 5).
  Both daemon and embedded-node boot paths now: open every configured pool
  through a `volume.Registry`, Track a `Lockable` volume on the node's
  `seal.VolumeLifecycle` before any unseal attempt, attach
  `seal.WithVolumeResolver` / `WithPerTenantRegistry` early, build the
  replicated `volume.Catalog` directly over the node's own host (via
  `NewVolumeCatalogKVServicer`) and attach it as `seal.WithVolumeAllocator`
  BEFORE `lifecycle.BuildServices` -- the `TenantService` it constructs
  needs the allocator at construction time -- and publish the node's
  volume-pool health on a lease via `volume.Reporter` once the data-plane
  services exist. `sdk/go/node.Node` gains `stopVolumeReporter`, called
  before `svcNode`/`host` teardown on every close and failure-rollback path:
  `volume.Reporter.Close` is synchronous, but its refresh goroutine calls
  live data-plane services on every tick, so it must stop first.
- **Certificate revocation is audited (FedRAMP AU-2, PCI DSS 10.2).**
  `DefaultCertProvider.RevokeCert` now emits `cert.revoke` on the compliance
  trail, including deny-by-serial (a serial this node holds no record for);
  the event carries the serial, whether a local record was found, and the
  record's own tenant when one exists. Wired via the new
  `cert.WithAuditLogger` provider option; issuance and enrolment remain
  audited one layer up, by `CertEnrollmentService` (`cert.issue`/
  `cert.enroll`), since `certs:revoke` has no single caller every transport
  funnels through the way issue and enrol do.
- `users:add_cert_binding` is held to the certificate target guard on every
  transport (`AuthMiddleware.AuthorizeCertBindingTarget`): it binds a
  thumbprint to any account named, so it now refuses a Security Officer
  account (their keys come only from the ceremony) and an Owner unless the
  caller is one, exactly as `certs:issue` and `certs:enroll` do. The guard is
  one instance per node (`lifecycle.Node.CertTargets`), reaching the
  middleware through `wiring.AuthMiddlewareConfig.CertTargetGuard`; a
  middleware built without one refuses the verb
  (`*middleware.CertTargetGuardRequiredError`).
- The built-in tenant Admin role is granted `users:add_cert_binding`: a
  tenant Admin does everything in its tenant except Security Officer key
  custody. The certificate target guard still refuses binding a key to an
  SO, or (unless the caller is an Owner) an Owner. Existing deployments pick
  the grant up through built-in role reconciliation.
- Trust-anchor verbs are confined to the caller's own tenant in
  `CertEnrollmentService` (`AddTrustAnchors`, `ListTrustAnchors`,
  `RemoveTrustAnchor` apply `tenant.Confine`), so every transport inherits it
  from one place. The REST handlers for `/v1/tenants/{id}/trust-anchors` also
  run the `trust_anchors:*` tenancy gate themselves, as gRPC, QUIC and MCP
  already did; before this they relied only on the route-level target, and a
  tenant Admin could have added or removed another tenant's anchors -- and then
  enrolled certificates that log in there -- once that target was removed.

### Changed

- A volume pool's catalog record (name, provider, capacity) is a
  management-plane object an operator creates explicitly -- it is never
  derived from a node's `storage.volume_pools` config, which only tells
  THAT node which local directory or device to map a pool name onto. A
  tenant create against an unregistered pool name now fails with
  `*volume.PoolNotFoundError` rather than succeeding against an
  implicitly-materialized pool.
- **BREAKING: no client private key is generated on the server.**
  `cert.CertProvider.GenerateClientCert(ctx, *GenerateRequest) (*CertBundle,
  error)` is replaced by `IssueClientCert(ctx, *cert.IssueRequest{Username,
  TenantID, PublicKey, TTL}) (*cert.IssuedCert{CertPEM, Serial, NotAfter},
  error)`, which certifies a public key the requester supplies.
  `cert.GenerateRequest` and `cert.CertBundle` are removed.
- **BREAKING: certificate generation is removed from every surface.** REST
  `POST /certs/generate`, gRPC `GenerateCert`, QUIC `cert.Generate`, MCP
  `qrdb_cert_generate`, the Go SDK's `CertClient.GenerateCert`
  (`GenerateCertRequest`, `CertBundle`), the TypeScript SDK's `certs.generate`
  (`GenerateCertRequest`, `CertBundle`), the desktop `CertBinding.Generate`,
  and the `certs:generate` permission (`permission.CertsGenerate`). Use the
  issue and enrol verbs above.
- **BREAKING: `qrdb ca issue` takes no `--tenant-id`.** Issuance always acts
  on the caller's own tenant, and the account must already exist; the key is
  made on the client, never on the server.
- **BREAKING: tenant create requires the first admin.** `admin_username` and
  exactly one of `admin_csr_pem` / `admin_cert_pem` are required on every
  transport (`*service.TenantOnboardingFieldError`,
  `*bootstrap.MissingAdminCredentialError`,
  `*bootstrap.ConflictingAdminCredentialError`). gRPC `TenantCreate` returns
  `TenantCreateResponse{tenant, admin}` instead of `TenantResponse`; the REST
  response keeps the tenant's fields and adds `admin`; the Go SDK's
  `CreateTenantResponse` gains `Admin` and `CreateTenantRequest` gains the
  admin fields; the TypeScript SDK mirrors both. An empty isolation type over
  the API still means `prefix`.
- **BREAKING: `bootstrap.ProvisionTenant` no longer generates keys.** In cert
  mode it takes exactly one of `AdminCSRPEM` (signed via `Dependencies.Signer`)
  or `AdminCertPEM` (+ `AdminChainPEM`, verified against `TrustAnchorsPEM`) and
  refuses neither with `MissingAdminCredentialError`; `Dependencies.CA` is no
  longer used by it. `TenantProvisionResult.AdminKeyPEM` is removed;
  `AdminCertSerial` and `AdminAwaitsBarrierInit` are added. New:
  `TenantProvisionConfig.Exclusive`, `Dependencies.TrustAnchors`,
  `Dependencies.PendingAdmins`, `Dependencies.TrustDomain`, `Dependencies.Now`.
  An empty `Isolation` still defaults to `barrier` for a direct caller, so a
  cert-mode call with no isolation now needs `Dependencies.PendingAdmins`
  (`*bootstrap.MissingProvisioningSeamError` without it).
- **BREAKING: `auth.IssuerVerifier.VerifyIssuer` takes the verified chain.**
  `VerifyIssuer(ctx, tenantID string, chain []*x509.Certificate) error`
  replaces the issuer-only form; the issuer is `chain[1]`, and failing the
  cluster-CA, static and Key B checks the chain may contain one of the
  tenant's registered trust anchors at any depth above the leaf.
- **go-quicraft bumped to v0.18.0** (root, `sdk/go`, `sdk/go/gorm/conformance`).
  `DefaultMaxMessageReceiveMemory` is removed upstream; the inbound
  message-receive budget is now derived from `MessageReceiveBudgetFrames`
  (default 4, 512 MiB at the defaults, was a fixed 256 MiB). go-qrdb never
  set the old constant or field, so this needed no code change; the Raft
  host's `TransportConfig` still carries only `MTLSConfig` from
  `buildHostConfig` and runs every other go-quicraft transport default
  unmodified (documented in `docs/configuration/cli.md` and
  `docs/configuration/engineering.md`; see `docs/12-configuration.md`).
  Every proposal, config change and session failure after the Raft
  hand-off now reports `quicraft.MaybeApplied(err) == true`; every
  service-layer propose helper already defaulted an unrecognized
  post-hand-off failure to `service.ErrOutcomeUnknown` (only
  `*quicraft.NotLeaderError`, a pre-hand-off refusal, maps to
  `service.ErrNotStored`), so that classification needed no change.
  `HealthStatus.DiagnosticsDropped` (the process-wide count of go-quicraft
  diagnostics a blocked log handler has dropped) is now surfaced through
  `service.HealthStatus`, the admin Health RPC/response on every transport
  (REST, gRPC, QUIC, the Unix socket, MCP, embedded), and the Go SDK's
  `transport.HealthResponse`. `OnShardFailed` firing exactly once per shard
  instance and `SnapshotRecoveryExhaustedError` have no existing go-qrdb
  consumer (go-qrdb registers no `quicraft.EventListener` today), so there
  was nothing to audit for correctness there.

### Fixed

- **Two lease-backed compensation paths could destroy a write that had
  actually committed.** `lock.Service.TryLock` and
  `BatchService.ImportBegin` each grant a lease, then propose a write bound
  to it; both used to revoke the lease unconditionally on ANY propose
  error, including `service.ErrOutcomeUnknown` -- which go-quicraft 0.18
  now reports for a far wider set of post-hand-off failures. Revoking in
  that case deletes every key bound to the lease, including one the
  ambiguous write may have just durably created. `TryLock` now re-reads the
  key and recovers the lock when it finds the lease it just granted;
  `ImportBegin` now skips the revoke on `errors.Is(err, ErrOutcomeUnknown)`
  and leaves the session for its own TTL, exactly like `ImportCommit`'s
  existing best-effort release.

- **A restarted self-issued-CA node no longer exits right after its
  unseal.** The boot's cluster-CA check read the CA record from the system
  shard while a leader elected a moment earlier was still refusing
  linearizable reads (`read index not ready`), and treated that as fatal, so
  `qrdbd` exited with "cluster CA persistence check failed". The check now
  waits out that warm-up, as it already did an election in progress, within
  the same 30-second bound.

- **A failed `DropTable` no longer leaves `FOREIGN_KEY_CHECKS` off for
  whoever the connection pool hands that connection to next.** The stock
  GORM migrator's `DropTable` sets it for the drop and restores it only once
  every drop in the batch succeeds; on a failure it returns first. The
  embedded driver's `ResetSession` now restores every session system
  variable a caller changed back to this connection's own baseline --
  `FOREIGN_KEY_CHECKS` included, and a connector option such as a DSN's
  `concurrency=` left untouched -- when the connection is checked back out
  of the pool, so the setting never reaches a later caller. A variable
  `ResetSession` cannot read or restore fails the reset closed: the
  connection is marked bad and discarded (`driver.ErrBadConn`), the same as
  a transaction it cannot roll back, rather than going back to the pool
  with that variable still at whatever the previous caller left it
  (`sdk/go/driver/conn_embedded.go`'s `resetSessionVariables`,
  `*driver.SessionVariableResetError`).

- **The GORM conformance wire targets run again.** Their harness created the
  certificate-only `gorm` user without an email login key, which `users
  create` has refused since the key became required. The mysql and pgsql
  targets therefore stopped before running a test. Expired allowlist entries
  are deleted, not re-dated: pgsql's ten untriaged `pgsql-wire-gap` entries
  and both TestSelectWithUpdate entries. The mysql target still fails
  intermittently on TestSelectWithUpdate, a timestamp assertion that write
  latency pushes across a second boundary
  (`sdk/go/gorm/conformance/README.md`).

- **An embedded node honours the configured tenant defaults, and composes
  member-side CSR join, as `qrdbd` does.** `sdk/go/node` ignored
  `tenant.default_quotas` and `tenant.default_entitlements` (a tenant created
  through an embedded node got unlimited quotas and prefix-only
  entitlements whatever the configuration said); it now reads them through
  the same `ToQuotas`/`ToEntitlements` qrdbd uses, with
  `WithDefaultTenantEntitlements` still overriding the entitlements. And it
  never recorded its own join identity or started the join listener, so an
  embedded node with a cluster CA key -- its own, or one injected with
  `WithClusterCAKeyProvider` (an HSM or KMS) -- could not mint a join token
  or sign a joiner's CSR. That composition now lives in
  `lifecycle.StartClusterJoin`, which qrdbd and `sdk/go/node` both call.
- **An embedded node on operator-supplied TLS has the operator's CA as its
  cluster CA, as `qrdbd` does (ruling 5).** A clustered `sdk/go/node` node
  handed its cluster identity (`WithRaftMTLS`) and no cluster CA key of its
  own self-issued a cluster CA anyway, so after the issuer-tenant binding
  landed no peer -- every node certificate issued by the operator's CA --
  could forward to it on a caller's behalf, and the operator's own client
  certificates no longer authenticated for the system tenant. Such a node is
  now BYO-TLS (`lifecycle.Infra.NoClusterCAKey`), and its trust pool's cluster
  CA is resolved by `lifecycle.TrustPoolClusterCA`, extracted from `qrdbd`
  and shared. As on a BYO-TLS `qrdbd` node, verbs that sign with the cluster
  CA refuse until the node is given a cluster CA key.
- **A clustered embedded node is a full member of `raft` tenant isolation.**
  `sdk/go/node` built the tenant infrastructure but none of the multi-node
  half `qrdbd` composed in `cmd/qrdbd`, so on an embedded node a raft
  tenant's key never arrived from a peer (every request for the tenant was
  "tenant not found"), its databases' followers were never added, and a
  dedicated tenant MOVE had no key deliverer. That composition -- the cluster
  key plane, the key distributor, the rebalance dependencies, the replica host
  and the peer admin registry -- now lives in `pkg/server/lifecycle`
  (`ComposeRaftTenantCluster`, `NewPeerAdminRegistry`), and `qrdbd` and
  `sdk/go/node` both call it; the `qrdbd`-local copy is deleted. An embedded
  node needs a REST listener for the key routes, as a `qrdbd` node does.
- **A raft tenant's observer replica could be promoted before it existed.**
  The tenant shard reconciler took a replica whose shard had never started
  (its tenant key not yet delivered) for one that had finished joining,
  marked it `ObserverReady`, and the leader then added it to the voting set
  with no data. It is now marked ready only once its shard runs.
- **A dedicated-raft rebalance MOVE that fails logs why.** The failure was
  counted and its cause dropped.
- **qrdbd publishes its member record in the background.** Boot no longer
  waits on the replicated write, and a SIGTERM during boot no longer fails
  `qrdbd start` with "failed to register self".
- **First boot could crash a node with `register role <name> failed: role: id
  already exists`** when several seeders raced the SAME built-in role under
  its deterministic `role.SystemRoleID`: concurrent nodes' own first-boot
  (`pkg/firstboot.registerBuiltInRoles`) racing each other, a tenant's
  `CreateTenant`-triggered role seeder (`pkg/server/lifecycle`'s
  `roleSeederAdapter`) racing first-boot's, and two concurrent
  `bootstrap.ProvisionTenant` calls for the same new tenant racing each
  other's `seedTenantRoles`. Three call sites, all converging on the same
  two causes: `roleSeederAdapter` built its role store without
  `WithIdentityIndex`/`WithAtomicIdentity`, so its `CreateRole` fell back to
  three separate, non-atomic proposals (primary record, then tenant index,
  then name index) — a concurrent racer could observe the primary record by
  id before the name index existed, fail its own "already seeded?"
  verification by name, and refuse the whole run. It is now wired
  atomically, exactly like cmd/qrdbd's boot-time role stores. Separately, a
  create collision's verification was either a single, un-retried read
  (`firstboot.builtInRoleAlreadySeeded`, `role.ReconcileBuiltInRoles`'s own
  `seedMissingRole`) or missing entirely (`bootstrap.seedTenantRoles` never
  checked `RoleIDExistsError` at all, only `RoleNameExistsError`, and never
  verified a name collision was actually its own canonical definition
  before accepting it): the LOSING side of a genuine atomic race can be
  told "guard failed" before its own local StaleRead has replicated the
  WINNING write. All three now retry briefly and verify both collision
  kinds against the canonical definition before concluding a collision is a
  real, unexplained conflict rather than an idempotent re-attempt racing
  visibility — `seedTenantRoles` reuses `firstboot`'s now-exported
  `BuiltInRoleAlreadySeededWithin` rather than a third copy of the same
  check. A record that resolves but disagrees with the canonical definition
  still fails, on every path. Regression-tested over a real go-quicraft
  cluster (`TestRegisterBuiltInRoles_ConcurrentSeedersConverge`,
  `TestReconcileBuiltInRoles_ConcurrentCallersConverge`,
  `TestProvisionTenant_ConcurrentCallersConverge`) and with
  `TestClusterEnableEncryption_TenantScope_RF3 -count=10`.
- **A `Put` of a key followed by a `Put` of a `/` prefix of that key (either
  order) could kill the shard on `raft.sm_storage: file`, including
  `sm_type: disk` — a security and availability bug a probe found, with two
  more ways discovered in review of the first fix.** The file backend
  originally mapped each key to a single file, with `/` bytes read as
  directory levels, so a key that is a `/` prefix of another resolved under
  the exact path that other key's parent directory needed. `SM.Apply`
  returning the resulting error is fatal to go-quicraft: `lastApplied` stops
  advancing on that replica and the shard goes dark for every subsequent
  linearizable read. Because the system database lives on the system shard,
  that took authentication down with it. An empty-key batch tripped the
  identical failure mode before go-qrdb#136's classification fix landed;
  this closes the other half the same probe surfaced. Review of a first fix
  (one directory per key) then found: **(B1)** an adversarially deep key
  (thousands of short `/`-delimited segments) produced a path longer than
  `PATH_MAX`, so `os.MkdirAll` returned `ENAMETOOLONG` — the identical fatal
  shape, from a different cause; **(B2/B3)** a refused op inside a batch left
  the commit journal published before the refusal was discovered, so
  recovery replayed the same refusal on every future `Open` and failed it
  permanently, and a refused batch or transaction could partially apply.
  The file backend's key-to-path mapping is now a **hashed, flat,
  content-addressed layout**: each key's record is named by the hex SHA-256
  digest of the key (no hand-rolled hashing), sharded two directory levels
  deep, holding `[keyLen:4 BE][key][value]` so a read can verify the stored
  key matches the one asked for. The path has constant length and depth
  whatever the key, closing B1 and the original collision at once, since
  there is no path derived from the key's content or length at all —
  `file.KeySegmentTooLongError`, an interim measure from the one-directory
  layout, no longer exists. `commitBufferedOps` now validates every op in a
  batch BEFORE writing the commit journal, closing B2/B3: a refused batch
  leaves neither a journal nor a partial write, and the node reopens
  cleanly. `New` writes a format-version marker into a fresh data directory
  and refuses to open a non-empty one that lacks a matching marker
  (`FormatMarkerMissingError`, `FormatVersionMismatchError`) — a validity
  check, not migration code, that also refuses the superseded
  one-directory-per-key layout in place. Every directory `MkdirAll` creates
  now has its parent fsynced immediately, and `FastBackend`'s async-write
  failure logs no longer include the raw key. See
  [docs/storage-engines/architecture.md](docs/storage-engines/architecture.md#the-file-engine-a-hashed-flat-content-addressed-layout-breaking)
  (go-qrdb#592).
- **A 65,534- or 65,535-byte key panicked `Apply` on every replica and every
  state machine type, and a 65,526-65,533-byte MVCC put silently stored the
  wrong value — a focused re-review of the fix above found this in every
  2-byte key-length decoder it touched (go-qrdb#592).** Each one computed a
  bounds check, or the slice it then took, as `2 + keyLen` (or
  `2 + keyLen + 8`, ahead of a fixed-width lease ID) while `keyLen` was
  still the 2-byte field's own `uint16` — arithmetic that wraps a few bytes
  short of 65,536 and lets the wrapped, too-small value pass a narrower
  check or silently shift every following field's offset, instead of being
  refused. `pkg/protocol.DecodeKeyLen` is now the one shared, bounds-checked
  primitive every 2-byte-prefixed key decoder in the tree calls — in
  `pkg/protocol` itself (`DecodeMVCCPut`, `DecodeMVCCDelete`, which
  `pkg/store`'s pebble and MVCC state machines, and every state machine in
  `pkg/statemachine`, share), in `pkg/statemachine`'s own base-KV
  `handlePut`/`handleDelete` (`kv_statemachine.go`, `regular.go`,
  `concurrent.go`, `fast.go`, `putifabsent.go` — now one shared
  `decodeKeyValuePayload` instead of five copies of the defect), and in
  `pkg/engine/mvcc_engine.go`'s own base-KV handlers — rather than each
  computing the sum in the field's own width. `validateClientKey` enforces
  no length cap at Apply at all, by design: Apply must be a pure,
  deterministic function of the committed log, and the cap is a
  proposal-time policy (`protocol.CheckKeyLen` inside
  `EncodeOp`/`EncodeCompare`, and every other encoder's own structural
  frame width) rather than a replica-local decision that could diverge
  two replicas running different binary versions during a rolling
  upgrade. A key already committed to the log, however long, applies
  identically on every replica and every `sm_type`, including on replay.
  A global maximum key length of 65,535 bytes is enforced at PROPOSAL
  time on every wire encoder whose key field could otherwise describe a
  longer one (`protocol.CheckKeyLen` inside `EncodeOp`, `EncodeCompare`);
  the ones already bounded to the identical value by their own two-byte
  key frame (`protocol.EncodeCompareAndSwap`/`EncodeCompareAndDelete`,
  go-quicraft's `kv.EncodePut`/`EncodePutIfAbsent`/`EncodeDelete`) were
  left as is, and a pre-existing, independently-defined duplicate of the
  same 65,535-byte cap in the batch-import path now references the one
  canonical constant.
- **A concurrent `Set` or `Delete` to an unrelated key could abort an
  iterator scan or `List` entirely, or make `Snapshot` return a silently
  truncated result with a nil error, on `raft.sm_storage: file`
  (go-qrdb#592).** Both scans used `filepath.Walk` without expecting a
  per-entry `ENOENT` — a renamed-away `.tmp-set-*` temp file, or a record a
  concurrent `Delete` removed, between `Walk` reading a directory and
  stat'ing or reading an entry inside it — and `Snapshot` discarded the
  iterator's own `Error()` instead of returning it. Both now skip a
  per-entry `ENOENT` (never the engine's own root directory failing to
  stat, which is a real fault) and keep walking, and `Snapshot` propagates
  a load it could not complete rather than returning a truncated result as
  if it were whole.
- **The file engine's format-marker check refused to initialize a mount
  root whose only content was `lost+found`**, the directory `mkfs`/`fsck`
  creates at the root of a freshly formatted filesystem (root-owned, mode
  `0700` by convention) — never written by any layout this engine has had,
  and never meaningful to it. `New` excludes `lost+found` when deciding
  whether a directory is fresh, while still refusing one that also holds
  genuine old-layout content. A first fix stopped there and still failed:
  `cleanupTempSetFiles`, `List`, and the iterator (so `Snapshot`, which
  loads through it) all walked the tree with `filepath.Walk` rooted at the
  engine's own root, and `filepath.Walk` reads a directory's contents
  BEFORE it ever calls back for that directory — too late for the callback
  to skip a root-level entry it cannot read. All three now list the root
  with `os.ReadDir` and walk `filepath.Walk` only from entries that already
  look like one of the engine's own two shard-directory levels
  (`isShardDirName`), so a genuinely unreadable `lost+found` is never
  opened at all, not merely tolerated after the fact.
- **`pkg/storage/file`'s `New` is also what backs `BackendFile` databases,
  every database's barrier-metadata store regardless of that database's
  own configured backend, partitions, and the repository `qrdbd restore
  --backup-dir` reads from** — not only `raft.sm_storage: file`. The
  breaking-change note below, and
  [docs/storage-engines/architecture.md](docs/storage-engines/architecture.md#the-file-engine-a-hashed-flat-content-addressed-layout-breaking),
  now say so explicitly, and
  [docs/backup-restore/user.md](docs/backup-restore/user.md) documents that
  a backup made before this change cannot be restored by a binary that
  carries it.
- A concurrent `Set` that found its shard directory already created by a
  racing sibling `Set` could return before that directory's creation was
  durably published to ITS parent: `mkdirAllDurable`'s "already exists" and
  "lost the creation race" paths now fsync the parent unconditionally
  rather than trust that the directory's actual creator has already done
  so. Because that makes every `Set` pay a parent fsync even for a shard
  directory this `Engine` instance already confirmed durable, `Set` now
  checks a per-instance, concurrency-safe cache (`ensureShardDirDurable`)
  first and skips `mkdirAllDurable` entirely on a hit, so only the FIRST
  `Set` into any given shard directory pays the cost. `New` also removes
  any `.tmp-set-*` crash leftovers from an interrupted `Set` under the
  engine root at Open, and the `docs/storage-engines` wording that
  described this layout change as replacing two PRIOR RELEASES is
  corrected: the intermediate one-directory-per-key design never shipped —
  it existed only between commits of this same unreleased change.
- Tenant onboarding refuses an admin username the CA could never certify
  (one that does not survive the SPIFFE round trip under
  `auth.spiffe_trust_domain`) before anything is written, instead of after
  the tenant exists -- which would have retired its id.
- Completing a pending tenant admin adopts an existing account of that name
  only when it holds the tenant's Admin role; any other account is refused
  (`*bootstrap.PendingAdminAccountConflictError`) and never bound to the
  onboarded admin's key.
- **BREAKING (Go API): a pending tenant admin is recorded create-if-absent.**
  `tenantadmin.Store.Put` is replaced by `Create`, which writes atomically
  (`kvstore.AtomicKVStore.PutIfAbsent`) and never replaces a record
  (`*tenantadmin.AlreadyPendingError`), as `qrdb init` never replaces an
  established first admin.

### Changed — BREAKING

- **BREAKING: `sdk/go/driver`'s in-process MySQL dialect now returns
  go-sql-driver's `*mysql.MySQLError` for every engine error GMS classifies
  with a MySQL error number,** from `Exec`, `Query`, `Prepare`, row iteration
  and `COMMIT`, instead of qrdb's own `*driver.SQLStateError`. A caller that
  type-asserted `*driver.SQLStateError` must assert `*mysql.MySQLError`
  (`Number`, `SQLState`, `Message`) instead; `*driver.SQLStateError` is
  removed. A classified error is now that `*mysql.MySQLError` bare -- the
  same type and values the identical statement produces over qrdb's MySQL
  wire port -- with no wrapped error underneath, so it no longer unwraps to
  qrdb's own `pkg/rowlock` or `pkg/sql/provider` error types the way
  `*driver.SQLStateError` did; `errors.As` against those types stops
  matching on a classified failure. An UNclassified failure (the engine's
  1105 fallback, which is what a context cancellation or one of qrdb's own
  typed errors becomes when GMS cannot assign it a number) is unaffected:
  it keeps its typed chain inside `*driver.QueryExecError` /
  `*driver.PrepareError`, so `errors.Is(err, context.Canceled)` and
  `errors.As` against qrdb's error types still work there. The REST and
  gRPC transports are unchanged.
- **BREAKING: `sdk/go/gorm`'s own GORM dialector, migrator, data-type mapping,
  clause builders and error translator are deleted.** `Open` and `NewConfig`
  now build the stock `gorm.io/driver/mysql` dialector, configured with
  qrdb's driver name and defaults; `DB.Dialector` is genuinely
  `*mysql.Dialector`. Behaviour lost with the custom dialector: MySQL error
  3819 no longer maps to `gorm.ErrCheckConstraintViolated`; caller-supplied
  savepoint names are not validated before they are concatenated into
  `SAVEPOINT`/`ROLLBACK TO SAVEPOINT`; the dialector's `Name()` cannot be
  overridden. See `docs/sql/gorm.md`.

- **BREAKING: the file storage engine's on-disk layout changed; no
  migration (go-qrdb#592).** `raft.sm_storage: file` (used by `sm_type:
  disk` and `fast`) used to map each key to a single file, with `/` bytes
  read as directory levels; it now maps each key to a record named by the
  hex SHA-256 digest of the key, sharded two directory levels deep,
  holding `[keyLen:4 BE][key][value]`. This is wider than
  `raft.sm_storage: file` alone: `file.New` also backs `BackendFile`
  databases, every database's barrier-metadata store regardless of that
  database's own configured backend, partitions, and the repository
  `qrdbd restore --backup-dir` reads from. A data directory written by
  the one-file-per-key layout this replaces — the only layout that has
  ever shipped; an intermediate one-directory-per-key design existed only
  between commits of this same unreleased change and was never in a
  release — is not read by this one; `New` refuses it with a typed error
  naming the format rather than misreading it, and ignores `lost+found`
  (the directory `mkfs`/`fsck` creates at a mount root) when deciding
  whether a directory is fresh. There is no migration code, per the
  project's no-backward-compatibility policy: start the replica over
  raft's restore or backup/restore path, or reseed from a backup, rather
  than upgrading the directory in place. A backup taken before this
  change cannot be restored into a binary that carries it, for the
  identical reason: the backup's bytes are the old layout, and the
  restoring binary opens them as data directories under the new one. See
  [docs/storage-engines/architecture.md](docs/storage-engines/architecture.md#the-file-engine-a-hashed-flat-content-addressed-layout-breaking)
  and [docs/backup-restore/user.md](docs/backup-restore/user.md).
- **`RestoreBackup` is now atomic: every target shard is authenticated and decrypted before any is imported.** A security review of the backup-encrypt-default feature found that a tampered shard aborted only that shard, leaving every shard already imported in place with no top-level error. Restore now runs two passes -- verify all, then import all -- and a failure anywhere in the first pass returns a single typed `*BackupVerificationFailedError` and imports nothing.
- **Restore refuses when a backup's stored metadata and its envelope disagree about encryption.** An envelope present while the metadata claims `encryption: "none"` (or the reverse) is now `*BackupMetadataMismatchError`, closing a downgrade path where metadata could be edited independently of the cryptographic artifact.
- **Each backup now carries an authenticated manifest** (format version, algorithm, key-wrap provider/reference, backup ID, destination, and a SHA-256 digest per shard, sealed under the backup's own DEK) that restore authenticates before touching any shard. A restore with no explicit target now defaults to the manifest's own recorded shard set rather than whatever the artifact still has, so a shard dropped from storage after creation is caught instead of silently restoring fewer shards with no error.
- **A tampered shard's own ciphertext now reports `*BackupShardIntegrityError`**, not `keywrap.UnwrapFailedError` (a wrong-KEK/wrapped-DEK failure one layer up) -- the prior reuse reported plain ciphertext tampering as "wrong key-encrypting key".
- **The plaintext-override audit record (backup create and restore) is now gating, not best-effort.** It is emitted after the backup/restore has its ID and carries it, before any shard is touched; under this node's audit failure policy (`BackupService.WithAuditFailurePolicy`/`SetAuditFailurePolicy`, default `FailClosed`) an unrecordable override now refuses the operation instead of proceeding silently. `lifecycle.BuildServices` now always wires the compliance audit logger and failure policy into the Backup service (it previously built it with neither), and `sdk/go/node`'s seal-disabled construction paths get the same wiring.
- **`keywrap.SoftwareProvider`'s key files are now confined to one configured directory**, opened as an `*os.Root` (`NewSoftwareProvider(keyDir)`). `KeyRef`/`DecryptionKeyRef` are bare file NAMES inside it, never paths: a value containing a path separator, `""`, `"."`, or `".."` is refused, a file read is capped at 33 bytes regardless of its real size, and every failure collapses into one detail-free `*KeyAccessError` logged with full detail on the server only -- closing a filesystem oracle and a denial-of-service lever (`/dev/zero`) a caller-controlled, unconstrained key-reference path previously allowed.
- **A collision between an injected `WithKeyWrapProvider` and an already-registered provider ID is now a typed, returned error**, not discarded. `NewBackupService` now returns `(BackupServicer, error)`.
- **`pkg/server` gains `WithKeyWrapRegistry`/`WithKeyWrapProvider`**, mirroring `WithStrategyRegistry`'s shape and `CryptoOptions` forwarding, so an embedder's key-wrap provider reaches the Backup service through the same composition path hardware seal strategies already use. `cmd/qrdbd`'s offline, boot-time disaster-recovery restore path now resolves an injected provider through the same seam its live daemon startup uses, rather than building its own disconnected default registry.
- **`cmd/qrdbd/restore.go`'s offline restore no longer collapses every `DecryptBackupShard` failure other than a decryption failure into "shard not in artifact".** Only a genuinely absent shard (`*service.BackupShardNotFoundError`) is reported that way; every other cause (a tampered manifest, a metadata mismatch, an unregistered provider) now reports as a decrypt-phase `*RestoreBackupError` naming the real cause.

### Security

- **BREAKING: backups are genuinely encrypted (`aes256`) by default, on
  every surface, through a new KEK-provider seam (`pkg/keywrap`).**
  `BackupOptions.Encryption`/`BackupRequest.Encryption` left empty no
  longer means plaintext: it now defaults to AES-256-GCM and then requires
  a key reference (`BackupOptions.KeyRef` / `key_ref` on the wire),
  refusing with a new typed error, `BackupEncryptionRequiredError`
  (`InvalidArgument`), when neither is supplied. Plaintext is now reachable
  only by naming `Encryption: "none"` explicitly, which is logged at WARN
  and, where a compliance audit logger is configured
  (`BackupService.WithAuditLogger`/`SetAuditLogger`), recorded in the audit
  trail. Each backup now mints a fresh per-backup data-encrypting key
  (DEK), encrypts every shard under it with AES-256-GCM (AAD binds a shard
  to its own backup), and wraps the DEK under a key-encrypting key (KEK)
  resolved through `keywrap.Provider` — qrdb ships a `"software"` provider
  (a 32-byte key file); go-xkms registers hardware or cloud-KMS providers
  via `WithKeyWrapRegistry`/`WithKeyWrapProvider`. The wrapped DEK, key
  reference, and provider ID are persisted in the backup's own envelope
  (`BackupInfo.KeyProviderID` is the only piece exposed on the wire);
  restore resolves and unwraps it automatically, needing no key material
  supplied at all in the common case. A wrong key or an unregistered
  provider refuses with the same typed `BackupDecryptionFailedError`
  (`InvalidArgument`), deliberately not distinguished from each other; a
  tampered SHARD's own ciphertext is a different typed error,
  `*BackupShardIntegrityError`, checked at a different layer (see the
  Fixed entry above) -- the two are never conflated.
  **BREAKING: the inline raw-key path is retired, not merely
  deprecated — `BackupOptions.EncryptionKey`/`RestoreOptions.DecryptionKey`
  (Go SDK), `encryption_key`/`decryption_key` (REST/gRPC/QUIC/MCP wire
  fields), and the CLI's `--encryption-key`/`--decryption-key` flags are
  all removed.** Every key is now referenced through a provider —
  `BackupOptions.KeyRef`/`RestoreOptions.DecryptionKeyRef`, `key_ref`/
  `decryption_key_ref` on the wire, `--encryption-key-file`/
  `--decryption-key-file` on the CLI — never passed as bytes over the wire
  or in a flag value. **Any script or CI job that relied on the old
  plaintext default, or on `--encryption-key`, must now pass
  `--encryption-key-file <name>` or `--encryption none`** (CLI), or the
  equivalent field on the REST, gRPC, QUIC, MCP, Unix and embedded
  transports, the Go and TypeScript SDKs, and the desktop/web Backup
  Manager. The desktop/web UI's encryption selector defaulted to `"none"`
  and used the wrong wire value (`"aes256gcm"` instead of the server's
  `"aes256"`) -- both are fixed, and the UI now always sends the field
  explicitly rather than omitting it on a plaintext choice, with an inline
  warning when plaintext is selected; its key field now names a key file,
  not a path and not a secret-bytes input. See
  [docs/key-wrapping/](docs/key-wrapping/architecture.md) and
  [docs/backup-restore/](docs/backup-restore/architecture.md).

- **`tenant.default_entitlements.allowed_volume_pools` could not be set from
  config, so every `volume`/`raft` tenant create was refused with
  `volume_pool:<none>` on a freshly configured node.** The docs
  (`docs/volume-pools/engineering.md`) always named this key, and
  `quota.Entitlements.AllowedVolumePools` always existed and was fail-closed
  on empty, but `config.TenantEntitlementDefaults` -- the YAML-loadable
  struct `ToEntitlements` converts -- never had a matching field, so the
  value loaded from every operator's `qrdbd.yaml` was silently discarded.
  Found running this lane's volume/pool/tenant/restart integration slice
  against a real daemon, not caught by any unit test because none builds a
  `TenantEntitlementDefaults` from YAML and checks the resulting
  `Entitlements.AllowedVolumePools` round-trips. Added the field and wired
  it through `ToEntitlements`.

- **`qrdb barrier seal --so-pin` sends the SO PIN.** The CLI parsed the flag
  and dropped it, so once `qrdb init` had provisioned an SO PIN every CLI seal
  was refused with `pin: invalid PIN` on every transport. The command now binds
  the PIN with `transport.WithSOPIN`, which every network transport already
  reads into the request's `so_pin` field. The Go SDK's
  `BarrierSeal(ctx)` is unchanged: a caller binds the PIN to `ctx`.
- **The sign-csr state refusal names the state it runs in.**
  `ceremony.ErrNotInitializedOrEnrolling` read "requires enrolling or
  operational state", but sign-csr is pre-operational only; it now reads
  "sign-csr requires the enrolling state". `ReissueAdminCredential`, which
  shared that sentinel for a different refusal, returns the new
  `ceremony.ErrNotInitialized` (`qrdb.ErrCeremonyNotInitialized` in the Go
  SDK) before `qrdb init` has run.
- **Followers now actually leave "awaiting admin credential" without a
  restart.** `WatchForAdminBound` watched the bare, un-namespaced admin
  record key, but every production composer namespaces the user store by
  `tenant.IdentityNamespace` before it reaches the replicated KV plane, so
  the real committed key is `"system/"` plus that record key. A bind
  committed between the initial lookup and the watch's own registration
  was also invisible to both; the watch now re-checks once immediately
  after registering. A lookup failure other than "no admin account yet"
  ended the watch permanently instead of retrying it. `cmd/qrdbd`'s watch
  goroutine was untracked at shutdown; it is now waited on. `sdk/go/node`
  decided this posture once, before any embedder had run first boot, so a
  fresh node whose first boot created the admin unbound stayed "not
  awaiting" until a restart; `Node.FirstBoot`/`FirstBootWithCeremony` now
  re-decide it immediately afterward.
- **The per-source SO PIN lockout keyed an unauthenticated caller by
  host:port, not host.** A rotating ephemeral client port -- or a new
  connection per request behind a pooling reverse proxy -- gave a fresh
  counter every time, defeating the isolation entirely; every such
  "addr:" principal also counted toward the 64-failure aggregate cap meant
  for authenticated principals only, so enough distinct unauthenticated
  hosts guessing together could lock out both a different host and an
  authenticated officer. The address is now keyed by host alone (IPv6
  normalized, zone-aware), and "addr:" principals no longer participate
  in, or are refused by, the aggregate cap.
- **`ReissueAdminCredential` refused outright on a BYO-TLS node (no
  configured `CeremonyCA`), and separately risked a nil-pointer panic
  reading the CA's bundle directly.** BYOC reissue needs no `CeremonyCA`
  at all; it now resolves the trust bundle the same way `Initialize` does.

### Security

- **The desktop `ServerInitWizard` now drives `ceremony.initialize`,
  retiring the former `seal.initialize`/`seal.initializeShamir` +
  `officers.registerOfficer` two-call flow.** `qrdb init` is the ONLY
  system bootstrap, so a holder could no longer be registered on a
  ceremony-backed system barrier before that one call. Share holders are
  now collected locally (no per-holder network call) and submitted
  together with the barrier secret/officers and the bootstrap admin
  credential in the single request; the admin's certificate and every
  holder's certificate come back in that same response. On the desktop
  (Wails) client, a generated bootstrap admin private key is saved to a
  local file by the Go binding and never crosses the IPC boundary to the
  page at all -- `CeremonyInitializeResponse.adminKeyPath` names where it
  was saved, instead of `adminKeyPem`. See
  [docs/desktop-gui/engineering.md](docs/desktop-gui/engineering.md).

- **`ConnectionManager`'s embedded-server creation wizard drives the same
  `ceremony.initialize` call, for every barrier mode including `none`.**
  `BarrierInitialize` refuses a ceremony-backed system barrier outright,
  exactly like `BarrierInitializeShamir` does, so the wizard's former
  `seal.initialize`/`seal.initializeShamir` calls -- made after the new
  embedded server was already started, and for Shamir after pausing the
  apply to enrol holders against it -- no longer worked for any mode.
  Reuses `CeremonyOfficerForms` and `StepAdminCredential` (new "Officers"
  and "Admin" wizard steps, holders collected before the server is even
  created) rather than a second implementation.

- **BREAKING: `CeremonyInitializeRequest.secret` and
  `CeremonyInitializeResponse.admin_key_pem` are `bytes`, not `string`.**
  A Go string is immutable and can never be wiped from memory after use
  the way a `[]byte` can, so the system barrier passphrase and a
  generated bootstrap admin's private key -- the two most sensitive
  values this call ever carries -- are now wipeable end to end. On the
  wire (REST, gRPC, QUIC, Unix, the embedded SDK) both fields are
  base64-encoded, matching `BarrierInitializeRequest.secret`'s existing
  convention; a caller sending or reading either as a plain string must
  switch to base64. The MCP tool's `secret` argument is the one
  exception and is unaffected: it is plain text, like `passphrase` on
  the barrier and tenant-barrier tools, decoded through the same
  wipeable-bytes path those arguments already use. `WipeAdminKeyPEM`
  now runs on every transport, not only REST, and every conversion
  between the wire type and the service/proto layers makes exactly the
  one, unavoidable copy crossing that boundary -- never a second,
  equally unwiped one further down.

- **`CeremonyOfficerRequest`/`CeremonyOfficerConfig` carry the full holder
  shape, on every transport.** `qrdb init` is the ONLY system bootstrap, so
  it is also the only place Shamir custody over the system barrier is ever
  established -- a holder who needed a separate share-envelope key, an
  additional authentication factor, or a designated factor had no way to
  get one through it. Each officer in a `CeremonyInitialize` request now
  carries `encryption_kem`/`encryption_key`, `factors` and `designated`,
  the same fields `OfficerRegisterRequest` (`POST /v1/barrier/officers`)
  takes, across REST, gRPC, QUIC, Unix, the embedded SDK, MCP, the Go SDK
  and the TypeScript SDK. `Initialize` registers every officer through the
  identical holder-engine call `BarrierOfficerRegister` makes, so there is
  exactly one registration path for the rich holder data, not a shortcut
  that only the post-init route supports. `qrdb init` gains
  `--officers-file`, a JSON document naming every holder with the full
  shape; it is mutually exclusive with `--officers`/`--custodians`, whose
  `username=path/to/csr.pem` form cannot express the new fields. See
  [docs/ceremony/cli.md](docs/ceremony/cli.md) and
  [docs/ceremony/api.md](docs/ceremony/api.md).

- **BREAKING: `ceremony sign-csr` issues HOLDER certificates only.**
  `admin`, `operator`, `user` and `auditor` are removed from the role
  profile sign-csr accepts; only `so` and `custodian` remain. Before this
  change, an SO holding only the SO PIN — reachable before unseal, with no
  other authentication — could mint a certificate for `CN=admin, OU=system`
  (or any other username/role) and, under the then-default
  `auth.mtls_binding_policy: enrolled`, that certificate authenticated as
  the named account on CN/OU alone. A login certificate for a user-facing
  role is now issued by the tenant Admin through the certs CSR-sign or BYOC
  path, which requires an authenticated, authorized caller. `sign-csr`
  refuses every other role with `ceremony.ErrInvalidRole`, naming the
  replacement path.
- **BREAKING: `auth.mtls_binding_policy` defaults to `required`.** The zero
  value and an omitted or empty configuration value now resolve to
  `auth.MTLSBindingRequired`; `enrolled` — the prior default, which admits
  an unenrolled account on its certificate's CN and tenant alone — is now an
  explicit, visible opt-out that does **not** meet FedRAMP (NIST 800-53
  AC-5/AC-6/IA-2/IA-5/IA-5(2)), HIPAA (164.312(a)(1)/(d)/164.308(a)(4)) or
  PCI DSS 4.0 (3.6/3.7, 7.2, 8.2, 8.3). Every composition in this module
  (`qrdbd`, `sdk/go/node`, embedded) now enrols the bootstrap admin's
  certificate as part of first boot, so a default boot does not lock itself
  out.
- **A holder certificate always authenticates, regardless of
  `auth.mtls_binding_policy`.** `auth.mtls_binding_policy` names an
  ACCOUNT's certificate enrolment; a share holder (`auth.IsHolderIdentity`
  -- a Security Officer or custodian certificate the ceremony CA signed) is
  by design never an account, so the policy's rationale does not reach it.
  `finalizeIdentity`'s holder branch no longer calls
  `checkUnresolvedCertLogin` -- the one place this carve-out lives,
  mirroring how `onbehalf.go`'s own `holderprofile.Is` check works -- so a
  holder identity is always admitted, with no roles, under `required` as
  much as `enrolled`. Its binding is the ceremony/officer roster, checked by
  thumbprint at the handler layer for every `permission.IdentityGated` verb
  (share claim begin/complete, quorum begin/submit); every other permission
  still denies a holder identity exactly as before, since RBAC refuses an
  empty role set regardless of how it got there -- including officer
  registration itself (`seal:officer_register`/`tenant_barrier:officer_register`/
  `databases:officer_register`), a real, registered RBAC permission requiring
  an Admin or SO role grant on every transport, never `IdentityGated`: a
  holder can only ever complete its own enrolment or claim, never register
  another holder. Checking the binding policy before that roster check would have refused a
  holder legitimately claiming its own share before `decide()` is ever
  reached, which is the bug this fixes.
- **BREAKING: The bootstrap admin's certificate is issued through `qrdb init`
  (`pkg/ceremony.CeremonyService.Initialize`), never generated as a
  per-node file.** `cmd/qrdbd`'s `generateAdminClientCert`, which wrote
  `{data_dir}/admin-cert.pem` and `admin-key.pem` on each node independently
  (auto-TLS mode), is removed: a per-node admin private key file violated
  both the no-node-local-state rule (the key must never be written anywhere,
  and admin state must survive backup/restore in the cluster's own
  replicated store) and the generate path's own rule (dev/test/evaluation
  only, key returned exactly once over the wire and never persisted). `qrdb
  init --admin-csr <path>` or `--admin-cert <path> [--admin-cert-pool
  <path>]` select the production-grade paths; `--admin-generate` selects
  the dev/test/evaluation-only path explicitly, loudly flagged as not
  meeting FedRAMP, HIPAA or PCI DSS 4.0 -- omitting all three is refused,
  with no credential-less default.
- **BREAKING: `qrdb init` is the only system bootstrap.** It brings the
  system barrier up itself (a new `--passphrase` flag, required exactly
  when `--barrier-strategy=passphrase`) in the same sealed-window call that
  runs the ceremony and issues the bootstrap admin's credential.
  `BarrierService.BarrierInitialize` (`qrdb barrier init`) now refuses when
  it was built with a ceremony -- qrdbd's own system barrier shape, always
  ceremony-backed -- with a typed `*SystemBarrierInitRefusedError` naming
  the replacement: that REST route has only ever been the system barrier,
  with no tenant or database scope, so there is no case in which a
  ceremony-backed node still needs it to succeed. A barrier built WITHOUT a
  ceremony (the embedded SDK's library-level composition, which has no
  ceremony concept at all) delegates to the underlying barrier unchanged, so
  this refusal never reaches a composition that never had `qrdb init` to
  begin with. `BarrierInitializeShamir` (`barrier init-shamir`, the system
  barrier's own Shamir-split root key) and `tenant barrier-init` (a tenant's
  own barrier) are unrelated and untouched. `auth.enabled` now also requires
  `seal.enabled`: an auth-enabled, seal-disabled node is refused at config
  validation, since the bootstrap admin's certificate binding depends on
  `qrdb init` being able to bring the system barrier up at all. The seal's
  default strategy changes from `passphrase` to `none` accordingly, so a
  deployment that wants no encryption no longer has to disable the seal to
  get it. A flag-only `qrdbd start` (no `--config`) defaults the same way and
  is validated through the same `config.Validate`, so a bare `qrdbd start`
  boots rather than refusing its own defaults.

- **BREAKING: the embedded transport authorizes the tenant-administration
  surface exactly like the network transports.** A security probe found that
  `GetTenant`, `ListTenants`, `UpdateTenant`, `DeleteTenant`,
  `ReconcileDeletedTenants` and every `TenantBarrier*` operation ran no
  tenancy or permission check at all on the embedded transport: a caller
  bound to tenant `acme` could delete `globex` or the system tenant. Each
  operation now authorizes through the same `pkg/auth/middleware` decision
  (`AuthMiddleware.AuthorizeTarget`, `AuthorizeTenantUpdate`,
  `AuthorizeTenantBarrierInit`, `AuthorizeTenantBarrierSealedOp`) and the same
  `pkg/permission` table every network transport authorizes it through —
  there is no second, embedded-only permission table — so a tenant-scoped
  identity acting outside its own tenant is refused, rate limits apply, and
  the decision is audited with the caller's tenant. The per-tenant share-claim
  and quorum handshakes (`TenantBarrierShareClaimBegin/Complete`,
  `TenantBarrierQuorumBegin/Submit`) carry no RBAC permission of their own,
  matching `permission.IdentityGated` on every network transport, so a
  Security Officer or custodian without an RBAC role is still authenticated
  through the factor it presents — formalized as an explicit
  `requireIdentity` check in the full-surface widening below, which also
  retires the quorum pair's `claimContext` provenance marking as the no-op
  it was (that marker's certificate-binding fallback is read only by the
  share-CLAIM handshake, never by quorum).
  **BREAKING** for an embedder that composed the embedded transport with an
  `AuthMiddleware` and relied on this gap: a caller that previously reached
  another tenant's record or barrier now needs the system tenant (for the
  tenant-record and plan operations) or that tenant's own identity (for its
  barrier). An embedder that installs no `AuthMiddleware` is unaffected — RBAC
  stays opt-in, exactly as on every network transport run with
  `--auth-enabled=false`.
  `sdk/go/transport/embedded/tenant_authz_test.go`'s
  `TestTenantClientAuthorizationSurface_CoversEveryMethod` fails the build if
  a future `transport.TenantClient` method ships with no entry declaring how
  it is authorized.
- **BREAKING, widened: the embedded transport authorizes its FULL
  operation surface, not only the tenant-administration one above.** One
  pluggable auth chain on all transports means KV, MVCC, transactions,
  batch, watch, leases, locks, index, timeseries, vector, collections, RAG,
  embedding, users, roles, certificates, credentials, tenant DEK, backup,
  rebalance, databases (including the per-database barrier), SQL, ceremony,
  the node barrier, and cluster admin now all authorize through the SAME
  `pkg/auth/middleware` call and the SAME `pkg/permission` entry the
  equivalent REST/gRPC/QUIC/MCP route resolves to — transcribed from
  `pkg/api/quic/dispatch.go`, not a parallel table
  (`sdk/go/transport/embedded/transport_authz_registry.go`). Users and
  roles resolve their target (the tenant a user or role belongs to) the
  same way `pkg/api/quic/targets.go` resolves it. Certificates additionally
  keep `tenant.Confine`'s unconditional structural check (a certificate
  authenticates as the tenant its Subject names, independent of whether
  RBAC is configured) alongside the new RBAC grant check. `EmitAudit`
  stamps the caller's own tenant onto every event, overwriting whatever a
  caller supplied, unconditionally. `SealStore*`, which has no
  network-exposed route and so no `pkg/permission` entry of its own,
  requires the new `seal:store_manage` permission (`ScopeCross`, like
  every other node-barrier permission, so only a system-tenant identity
  reaches it). `FactorClient`'s four methods (a different sub-client from
  the one above) authorize against the tenant the named user belongs to,
  with the same `user_authfactors` permissions the network's
  `userauthfactor.*` route family checks for the identical underlying
  service. `VolumePoolClient`, `PermissionClient`, and
  `RBACClient.ListPermissions` are gated the same way as every other
  `*Transport` method; `RBACClient.Authorize` is a policy-evaluation
  primitive deciding about an explicit identity argument, never an
  ambient caller identity, so it carries no permission of its own.
  `Health` and the sealed-window bootstrap/unseal verbs
  (`BarrierInitialize`, `BarrierUnseal`, `BarrierInitializeShamir`,
  `OfficerRegister`, `RecoveryOfficerRegister`, `CeremonyInitialize`,
  `CeremonySignCSR`) are public, matching every protocol's boot-time
  sealed-window listener: an embedded node has no IAM store to
  authenticate against while sealed, so the embedder must be able to
  drive these in-process, gated instead by their own domain credential.
  The share-claim and quorum handshakes (`ShareClaimBegin/Complete`,
  `BarrierQuorumBegin/Submit`, and their `TenantBarrier*`/`DatabaseBarrier*`
  siblings) are `IdentityGated`: an authenticated caller is required, rate
  limited and audited, exactly as `permission.IdentityGated` is on every
  network transport.
  **BREAKING** for an embedder that composed the embedded transport with an
  `AuthMiddleware`: a caller that previously reached KV, a user, a role, a
  certificate, or any other operation in this list without holding its
  permission now needs it. An embedder with no `AuthMiddleware` is
  unaffected.
  The buildgate widens accordingly:
  `transport_authz_coverage_test.go`'s
  `TestTransportClientAuthorizationSurface_CoversEveryMethod` reflects over
  every exported method of the concrete embedded `*Transport` type (not
  only `transport.Client`, which omits RAG, embedding, auth-factor, SQL,
  and the guarded-write KV verbs) and fails the build if any one of them —
  present or future — has no declared authorization mechanism, and a
  sibling gate does the same over every sub-client's own concrete type
  (`FactorClient`, `RBACClient`, `AuditClient`, `LoginClient`,
  `SessionClient`, `VolumePoolClient`, `PermissionClient`), closing the
  blind spot reflection over `*Transport` alone cannot see. A behavioral
  gate (`TestAmbientOperations_DenyANoPermissionIdentity`) goes further
  than declaration: it invokes every declared operation with a
  no-permission identity and asserts a real denial, which is how two
  pre-existing ordering bugs (`ResolvePermissions`,
  `GetUserByCertThumbprint` checking a service-nil guard before
  authorizing) were found and fixed. `operationPermissions` itself is now
  checked against `pkg/api/quic`'s own method-permission table by
  `transport_authz_quic_drift_test.go`, method by method, so it cannot
  silently drift from what the network transports enforce.
  Login attempts (`LoginClient.BeginLogin`/`FinishLogin`, the embedded
  SDK's own pre-authentication ceremony) are now rate-limited through
  `AuthMiddleware.CheckLoginRateLimit` — a new, minimal seam in
  `pkg/auth/middleware` that reuses the exact rate-limit engine and
  `(tenant, subject, permission)` key `authorize()`'s own rate-limit step
  uses for an authenticated caller, and now also audits a refusal the same
  way `authorize()` audits every other decision. `FinishLogin` charges the
  login attempt's RESOLVED principal (never the caller-supplied lookup
  key/hint, which is attacker-controlled before any proof is checked)
  under `permission.UserAuthFactorsLogin`, the same permission a network
  transport's login-factor verb carries. `BeginLogin` charges a
  tenant-wide, fixed-subject bucket under a SEPARATE sentinel permission,
  `permission.LoginBeginAttempt` — deliberately not
  `UserAuthFactorsLogin`: the two throttles are different threat models
  (volumetric challenge-issuance spam at `BeginLogin` vs. per-account
  brute force at `FinishLogin`) that need independently tunable
  `ratelimit.Policy` values, and sharing one `Permission` key makes that
  impossible to tune in production, not only in a test. Like
  `permission.IdentityGated`, `LoginBeginAttempt` is deliberately not
  registered in the permission catalog: it is a rate-limit dimension key,
  never an RBAC grant.

### Added

- **`TestMultiNode_ExactlyOneCredentialWinsAndIsBound` (go-qrdb#548
  review: B4).** N simulated nodes, each with its own genuinely distinct
  admin certificate chaining to the same ceremony CA, race to record
  one via `WritePendingAdminCert`'s create-if-absent guard -- exactly
  one wins. ALL N nodes then read back whatever record won and call
  `firstboot.Initialize` with it concurrently, as N nodes that each
  independently resolved the SAME replicated pending-cert record would.
  Exactly one admin account exists afterward, bound to exactly the
  winning credential's SPKI thumbprint; none of the other N-1
  credentials are ever bound to anything, and no second admin account
  is created, including under `-race`.
- **`service.CeremonyServiceWithAdminRebind` binds the credential it
  reissues, in the same call, or refuses (go-qrdb#548 review: B5,
  "admin reissue atomicity").** Reissuing a certificate against an
  already-booted, already-serving node used to return it to the caller
  with nothing binding it to the admin account -- the daemon's own
  pending-admin-cert hand-off and first boot only ever run once, at
  startup, so a RUNTIME retry (an operator re-running `qrdb init`
  against a node that has already finished booting) produced a
  credential that looked successful but authenticated nothing. The
  wrapper now takes a `firstboot.CertBinder` and binds the reissued
  certificate's SPKI thumbprint directly, before returning: no
  `CertBinder` wired, the admin not existing yet, or the bind call
  itself failing are each a typed refusal
  (`*service.AdminRebindNoBinderError`,
  `*service.AdminRebindAdminNotFoundError`, `*service.AdminRebindBindError`)
  instead of a result -- this wrapper never hands back an unbound
  credential. `NewCeremonyServiceWithAdminRebind` takes the binder as a
  new, required constructor argument.
- **`pkg/firstboot.Initialize` repairs an admin that exists but was never
  bound (go-qrdb#548 review: B6, "admin create+bind").** Creating the
  admin user (`createAdminUser`) and binding its certificate
  (`deps.Binder.BindAdminCert`) are two separate writes; a crash between
  them used to be permanent under the `created`-only gate -- a retry saw
  `created == false` and skipped binding forever, even with a credential
  available to repair it. Step 4 now also runs whenever the resolved
  admin currently holds no live certificate binding
  (`verifyAdminBindings`), the SAME predicate `checkBindingPosture` and
  `verifyRequiredOutcome` already applied, so a retry converges the
  account to fully bound rather than leaving it locked out under
  `required`. `checkBindingPosture`'s pre-flight now allows an existing,
  unbound admin through when a credential path and a `CertBinder` are
  wired (so Initialize can repair it), while still refusing outright, as
  before, when the refusal reason is one issuing a certificate cannot
  fix (an account the lifecycle or permitted-method policy blocks) or
  when nothing is wired to repair with at all.
- **`qrdb init` validates every admin-credential input before any side
  effect (go-qrdb#548 review: B3, "init is atomic").** Selection conflicts,
  none-selected, the BYOC chain check, and the requires-a-CA refusal for
  CSR-sign/generate now run before `Initialize` provisions the SO/user
  PINs, brings the system barrier up, or issues the TLS certificate --
  previously all three ran only afterward, so a malformed request left the
  barrier initialized and the PINs provisioned before the error surfaced,
  relying on their own idempotence to converge on a corrected retry rather
  than never needing it. `ceremony.CeremonyService.resolveCABundlePEM` and
  the new `validateAdminCredentialInputs` are pure (no CA write, no
  network call beyond the read-only `CABundle()` every Initialize already
  needed); the actual CSR-sign/generate/adopt work stays where it was,
  since a caller that passes validation is now guaranteed it will not fail
  on input shape.
- `pkg/ceremony`: `InitializeOptions`/`InitResult` carry the bootstrap
  admin's credential selection (`AdminCSRPEM`/`AdminCertPEM`/
  `AdminCertPoolPEM`) and result (`AdminCertPEM`/`AdminKeyPEM`/
  `AdminCredentialSource`). `ReissueAdminCredential` re-runs the
  admin-credential step on an already-initialized ceremony, for the retry
  path below. `LastIssuedAdminCertPEM` exposes the in-memory result to the
  composer that must durably record it.
- `pkg/firstboot`: `UserCertBinder`, the canonical `CertBinder` over qrdb's
  own user store; `PendingAdminCertStore`/`WritePendingAdminCert`/
  `AwaitPendingAdminCert`/`ClearPendingAdminCert`, the durable hand-off
  between ceremony initialize and first boot's binding, surviving a crash
  between the two. The write is create-if-absent (`PutIfAbsent`, no new
  Raft opcode): on a multi-node cluster every node that ran the sealed-window
  ceremony races to record its own issued credential, exactly one wins, and
  every later writer gets the typed `*AdminCredentialAlreadyPendingError`
  instead of silently overwriting the record first boot will bind.
- `pkg/service`: `CeremonyServiceWithAdminRebind` routes a second
  `CeremonyInitialize` call on an already-initialized ceremony to the
  admin-credential retry path, refusing with `*AdminAlreadyBoundError` once
  the admin already holds a live binding. `CeremonyServiceWithLeaderGate`
  refuses the same call up front on a Raft follower, naming the current
  leader, instead of attempting and failing partway through.
- `qrdb init`: `--admin-csr`, `--admin-cert`, `--admin-cert-pool`,
  `--admin-generate` flags select the bootstrap admin's credential path.
  **Exactly one of `--admin-csr`, `--admin-cert` or `--admin-generate` is
  required; there is no credential-less default** (review follow-up to the
  above): omitting all three is refused locally, before any call, the same
  way supplying more than one is. `--admin-generate` prints a loud,
  explicit non-production warning (to stderr, so it does not corrupt
  `--output json`/`yaml`). `--passphrase` (and `--barrier-strategy none`)
  bring the system barrier up in the same call.
- `ceremony.InitializeOptions`/`service.CeremonyInitializeOptions` carry
  the same `AdminGenerate bool` explicitly, mutually exclusive with
  `AdminCSRPEM`/`AdminCertPEM`; `Initialize` refuses with a new typed
  `*ceremony.NoAdminCredentialSelectedError` when none of the three is set,
  replacing the prior implicit "neither set" default.
- REST, gRPC, Unix, QUIC, MCP and embedded: `POST /v1/ceremony/initialize`
  (and its non-REST equivalents) accept `admin_csr_pem`/`admin_cert_pem`/
  `admin_cert_pool_pem`/`admin_generate`/`secret` and return
  `admin_cert_pem`/`admin_key_pem`/`admin_credential_source` identically on
  every transport. gRPC, Unix and QUIC share one protobuf message
  (`CeremonyInitializeRequest`/`Response` in `qrdb.proto`, regenerated) and
  one conversion package (`pkg/api/holderpb` server-side,
  `sdk/go/transport/internal/holderconv` client-side) -- the same fields
  REST/MCP/embedded already carried via `pkg/api/holderwire`.
- **`qrdb init` now works on a BYO-TLS node.** Server TLS identity and the
  CA that issues login/holder certificates are separate concerns:
  `ceremony.Initialize`, called with no `CeremonyCA` (a node whose TLS
  bundle carries no CA private key), no longer self-issues a TLS
  certificate -- it pins the configured server certificate's own public
  key instead, and returns that certificate unchanged. The
  bring-your-own-certificate admin-credential path needs no CA at all on
  such a node (it defaults to the node's own configured client-CA trust
  anchor rather than a CA's bundle); CSR-sign and generate are refused with
  a typed `*AdminCredentialRequiresCAError` naming BYOC as the supported
  path, since both need a CA that can sign. REST no longer carries its own
  nil-CA guard ahead of `Initialize`: the ceremony service handles a nil
  `CeremonyCA` directly (above), the same way on every transport.
- **sign-csr is PreOperationalOnly.** `CeremonyService.SignCSRInit` now
  refuses once the ceremony reaches `StateOperational`, not only before
  `StateAwaitingInit`, and `sealedwindow.VerbCeremonySignCSR` carries the
  matching `PreOperationalOnly` flag: a node restarted sealed after the
  ceremony already completed can no longer mint a holder certificate
  through either gate, closing the same post-operational hole the officer
  and recovery-officer registration verbs were already closed for.
- Errclass rows for `ceremony.ConflictingAdminCredentialInputsError` (400 /
  `InvalidArgument`), `NoAdminCredentialSelectedError` (400 / `InvalidArgument`),
  `AdminCredentialRejectedError`, `AdminCredentialIssuanceError`,
  `SystemBarrierInitError`, `service.AdminAlreadyBoundError`,
  `firstboot.NoPendingAdminCredentialError`, `AdminCredentialAlreadyPendingError`,
  and `ceremony.ErrInvalidRole`.
- **go-qrdb#548 NOT READY follow-up, item 1: a refused init no longer leaves
  state behind, and a retry no longer strands the barrier key.**
  `validateAdminCredentialInputs`'s CSR-sign branch now parses the CSR and
  verifies its self-signature (`parseAndVerifyCSR`, shared with
  `signHolderCSR`) before any side effect, instead of discovering a
  malformed CSR only once PINs were provisioned and the barrier brought up.
  The BYOC branch now enforces the full admin-certificate checklist --
  CN=admin, system tenant as the first Subject OU, no holder-profile OU,
  and a KeyUsage/ExtKeyUsage shape that supports client TLS auth -- via new
  `validateAdminCertProfile`. `seal.StorageBarrier.VerifyCredentials`
  re-derives a candidate root key from the persisted sealed blob and
  compares it to the live key in constant time, with no state change;
  `initializeSystemBarrierLocked`'s "already unsealed" branch now calls it
  instead of treating "already unsealed" as unconditionally safe, and
  refuses a mismatch with new `*ceremony.SystemBarrierCredentialMismatchError`
  rather than reporting the ceremony operational under a secret that does
  not actually open the barrier.
- **go-qrdb#548 NOT READY follow-up, item 2: the B6 repair now runs in
  `qrdbd` itself.** `runFirstBoot`'s fast path previously skipped
  reconciliation for any existing admin already marked `System: true`,
  without checking whether it held a live certificate binding -- the one
  place in the real daemon deciding whether `pkg/firstboot`'s own
  `needsBinding` repair ever got a chance to run. It now also checks
  `user.AdminLoginBlocker` (the exact predicate `needsBinding` is built
  from) and falls through to repair whenever the existing admin cannot
  actually log in by certificate, not only when its `System` flag is unset.
- **go-qrdb#548 NOT READY follow-up, item 3: a node whose admin is unbound
  no longer refuses to boot under `required` -- it boots "awaiting admin
  credential".** The documented retry ("re-run `qrdb init` against the
  leader") was unreachable under the default `required` posture because
  the daemon aborted before any listener started. It now starts every
  listener -- REST, gRPC, native QUIC, MCP, the embedded transport, the Unix
  socket transport -- with a new gate installed on each
  (`server.WithAwaitingAdminCredentialProbe`, and the transport-specific
  equivalents): while `firstboot.AwaitingAdminCredentialProbe` reports
  awaiting, only ceremony status and ceremony-initialize (the
  admin-credential retry `CeremonyServiceWithAdminRebind` already provides)
  are served, admitted with no authentication at all since there is no
  bound identity to authenticate as; every other request is refused with
  new `*middleware.AwaitingAdminCredentialError` (503, retryable). The
  probe re-reads the linearizable user store at a bounded interval and
  flips to "serving normally" on its own, with no restart, the moment a
  certificate binds. `pkg/api/sealedwindow` gained the matching
  `AwaitingAdminCredentialVerbs` derivation, reusing the canonical verb
  table rather than a second allowlist.
- **go-qrdb#548 NOT READY follow-up, item 6: one shared ceremony
  admin-credential wiring.** `cmd/qrdbd` composed
  `CeremonyServiceWithAdminRebind` and `CeremonyServiceWithLeaderGate`
  around ceremony-initialize by hand; `sdk/go/node` served the raw,
  unwrapped ceremony service, and the embedded transport's own ceremony
  RPCs were never wired to anything at all. New
  `service.ComposeCeremonyServiceWithAdminCredentialWiring` (built on new
  exported `service.HostLeaderChecker`, moved out of `cmd/qrdbd`) is now
  the one place that composes both decorators, used by every composer; new
  `firstboot.WriteIssuedAdminCertIfAny` is the matching shared helper for
  recording an issued admin certificate into the pending-admin-cert
  hand-off, which `sdk/go/node` never did on its own before this.
- **go-qrdb#560 security review B1: reissuing the bootstrap admin's
  certificate now requires the SO PIN.**
  `ceremony.CeremonyService.ReissueAdminCredential` verified nothing
  before minting and binding a fresh admin credential, and every
  transport's awaiting-mode passthrough (REST, gRPC/Unix, MCP) reached it
  through `CeremonyServiceWithAdminRebind`'s retry path. It now calls
  `pin.RequireSOPIN` first, propagating a typed `pin` package refusal
  before any side effect (the CA bundle read, the issuance, all of it);
  `service.CeremonyInitializeOptions.SOPin` is forwarded into the core
  options the same way `Initialize` itself already does. New
  forwarding/refusal regression tests on every transport
  (`TestHandleCeremonyInitialize_SOPINRefusalPropagates`,
  `TestGRPC_CeremonyInitialize_SOPINRefusalPropagates`,
  `TestMCP_CeremonyInitialize_ForwardsAllFields` +
  `_SOPINRefusalPropagates`).
- **go-qrdb#560 security review B1: the awaiting-admin-credential gate no
  longer re-reads the user store on the request path.**
  `firstboot.AwaitingAdminCredentialProbe.AwaitingAdminCredential` held a
  mutex across a linearizable lookup of up to 3s on every request once its
  cache went stale. The probe's state is now decided ONCE at boot
  (`Initialize`, from the same bounded `VerifyAdminCertBindingWithin` call
  a `required`-posture composer already performs) and left only by the new
  `AdminBound` event -- called by `CeremonyServiceWithAdminRebind` after a
  reissue actually binds a certificate while the node is serving -- never
  by polling, so a running node no longer re-enters "awaiting" merely
  because that certificate later expires (recovered instead by restarting
  into awaiting mode, then a fresh SO-PIN-gated reissue). A failed
  boot-time lookup is NOT "awaiting": `Initialize` returns it as a typed
  `*firstboot.AdminLookupError` for the composer to log, while the probe
  itself is left serving normally rather than additionally locking the
  node down over an infrastructure fault distinct from the admin's own
  binding state. `NewCeremonyServiceWithAdminRebind` and
  `service.ComposeCeremonyServiceWithAdminCredentialWiring` take a new
  `firstboot.AdminBoundNotifier` parameter; `cmd/qrdbd` and `sdk/go/node`
  both construct the probe's stable pointer before ceremony composition
  and call `Initialize` on it once their own boot-time lookup resolves.
- **go-qrdb#560 security review B2: the pending-admin-cert hand-off no
  longer poisons itself.** `firstboot.ClearPendingAdminCert` wrote the
  "consumed" marker unconditionally, on every successful first boot,
  including the many boots where nothing was ever pending at all (an
  auth-disabled node never runs a ceremony, so the key never existed; on
  ANY node, every boot after the first). That poisoned the key
  permanently -- `WritePendingAdminCert`'s create-if-absent could never
  succeed again, so a LATER ceremony on the same cluster could never hand
  a credential to first boot to bind. It now writes the marker only on a
  real pending-to-consumed transition. Separately,
  `firstboot.ResolveAdminCertForFirstBoot`'s non-blocking read (any policy
  other than `required`) returned the literal "consumed" marker bytes
  back as if they were a certificate; it now filters the marker exactly
  as `AwaitPendingAdminCert`'s bounded-wait branch already did. Together
  these were the root cause a04e9d38d patched around for the
  auth-disabled case alone (a stale "consumed" record read back as
  unparseable PEM on the second boot) -- that carve-out is reverted here,
  no longer needed for any policy.
- **go-qrdb#560 security review B3: a refused M-of-N `qrdb init` no
  longer leaves partial state behind.** `CeremonyService.Initialize`
  enrolled officers one at a time, synchronously signing and persisting
  each before moving to the next; a malformed CSR (or a key shared with
  an earlier officer in the SAME batch) on the LAST officer meant every
  prior one had already been signed and persisted by the time the
  refusal happened -- on top of the SO/User PINs already being
  provisioned, the TLS certificate already issued, and the bootstrap
  admin credential already minted. New `validateOfficerCredentials`
  pre-validates every officer's CSR, key kind, encryption KEM/key and
  factor set, and refuses a key any two officers in the batch share, as a
  batch, purely, with no side effect -- called alongside
  `validateInitOptions`/`validateAdminCredentialInputs`, before
  `pin.Provision`, the TLS issue or `issueAdminCredential` run at all.
- **go-qrdb#560 review: two more cmd/qrdb findings closed.**
  `CeremonyBinding.Initialize` now wipes the caller's own `req.Secret`,
  not only its internal clone. The local admin-key output path is now
  verified creatable and writable BEFORE the server is asked to mint the
  generated admin credential (`ensureAdminKeySavable`): discovering a
  save failure only afterward would leave the bootstrap admin eventually
  bound to a key nobody holds.
- **go-qrdb#560 review: holder certificates can no longer reach
  authenticated-only routes with no permission check (GET /metrics).**
  A holder certificate (an officer's or custodian's, signed by the
  ceremony CA) carries no roles by design and is admitted only for its
  own narrow IdentityGated verb set (share claim, quorum); REST's
  "authenticated but no specific permission" routes checked only
  authentication, not whether the identity was a holder's. New
  `HolderIdentityNotPermittedError` (403) refuses it. gRPC and MCP have
  no equivalent gap: every method there already requires a registered
  permission, which RBAC's empty-role denial already protects.
- **go-qrdb#560 review: the desktop "Re-initialize Barrier" card is
  removed.** It drove `seal.initialize`/`seal.initializeShamir` against
  an ALREADY-initialized system barrier, which the server now refuses
  outright (`qrdb init`/`ceremony.initialize` is the only system
  bootstrap); every click failed. `ceremony.initialize` cannot replace
  it either -- it refuses a second call on an already-initialized
  ceremony by design. The still-live path to change an already-keyed
  system barrier's strategy remains the Enable-Encryption migration
  section (none -> keyed); there is no in-place reconfiguration of an
  already-keyed barrier. The "Configure Encryption" card (a FRESH,
  never-initialized barrier) is unaffected and keeps the same
  `seal.initialize`/`seal.initializeShamir` calls, which the server does
  not refuse there.
- **go-qrdb#560 re-review, B1: `ReissueAdminCredential` now requires a
  PROVISIONED SO PIN, checked before anything that could tell an
  unauthenticated caller the admin's binding state.** It called
  `pin.RequireSOPIN`, which admits an UNPROVISIONED backend as "nothing to
  check yet" -- the right default for a brand-new node, never the right
  answer for a reissue, since an already-initialized ceremony always
  provisioned its SO PIN during `Initialize`. It now calls the new
  `pin.RequireProvisionedSOPIN` instead, which refuses with
  `pin.ErrPINNotSet` rather than passing. Separately,
  `service.CeremonyServiceWithAdminRebind.CeremonyInitialize` checked
  whether the admin was already bound (`*service.AdminAlreadyBoundError`)
  and the admin-lookup read BEFORE verifying the SO PIN at all -- an
  unauthenticated caller with no PIN, or the wrong one, could learn the
  admin's binding state merely by attempting a reissue. The new
  `ceremony.CeremonyService.VerifyReissueSOPIN` is now called FIRST, before
  either read.
- **go-qrdb#560 re-review, B2: a node whose admin is unbound, with no
  pending credential, now boots into "awaiting admin credential" instead
  of shutting down.** Under `auth.mtls_binding_policy: required`, an
  unbound or expired admin with nothing pending made
  `firstboot.ResolveAdminCertForFirstBoot`'s bounded wait time out with
  `*firstboot.NoPendingAdminCredentialError`, which `runFirstBoot`
  (`cmd/qrdbd`) and `sdk/go/node`'s `FirstBootWithCeremony` both returned
  as a fatal boot error -- the exact lockout the "awaiting admin
  credential" boot state (go-qrdb#548 review item 3) exists to avoid, just
  reached one call earlier than that fix covered. Both composers now log
  and continue with no certificate to bind instead. Separately,
  `firstboot.checkBindingPosture`/`verifyRequiredOutcome` no longer refuse
  Initialize pre-flight or outcome merely because no admin-credential path
  is configured at all (the everyday case for this exact scenario) --
  only the genuine wiring bug (a path IS configured but no `CertBinder` is)
  still refuses before any write. New `firstboot.HasAdminCertPath` exports
  the same "was there anything to bind" test to composers judging the
  outcome against their own store.
- **go-qrdb#560 re-review, B3: `validateOfficerCredentials`'s batch
  pre-flight now applies the IDENTICAL checks `enrol` does, through one
  shared `validateOfficerEnrolment`.** The batch pre-flight carried its
  OWN partial copy -- CSR/key kind/KEM/factors only, missing role
  validity, `validPrincipalName` and `ErrSelfEnrolment` -- so a batch
  where only a LATER officer had a bad username, a bad role, or named
  itself as its own enroller slipped past the pre-flight, letting every
  officer before it in the same call be signed and persisted before the
  defect was discovered, and blocking a corrected retry on "name already
  taken" forever. `Initialize`'s M-of-N officer loop is now also
  transactional for whatever can still fail AFTER that pre-flight (a CA
  signing fault, a persist error): a failure rolls back every officer this
  call already enrolled, in reverse order, and best-effort un-provisions
  the SO/User PINs via the new `pin.ProvisionRollback` optional capability
  (implemented by `pin.SoftwareBackend`) when the backend supports it.
- **go-qrdb#560 re-review: ceremony state now survives a restart.**
  `ceremony.NewCeremonyService` always started at `StateAwaitingInit`,
  regardless of whether the barrier it was built over had already been
  taken through the ceremony in an earlier process -- a composer's
  awaiting-mode passthrough then ran a FULL `Initialize` against an
  already-initialized ceremony after a restart, which could mint an
  unbound admin certificate and re-provision the SO/User PINs for
  whoever called first. State is now derived at construction from the
  barrier's own durable, plaintext-readable records
  (`barrier.CeremonyOperational`/`barrier.Initialized`, the SAME records
  `EnrollmentOpen`'s own restart-survival check already relied on) --
  no new node-local state file.

## [0.42.0-alpha] - 2026-10-05

The tenant login CA ("Key B") and the go-xkms SDK and security fixes. This release contains BREAKING changes; see the entries below. Highlights:

- **Tenant login CA (Key B).**
  - Client listeners trust each tenant's login CA through `pkg/clienttrust.Pool`; peer and Raft trust only the cluster CA.
  - No CA names are advertised, and chains are verified in `VerifyConnection`.
  - Key B fails closed on its own, without affecting cluster-CA or operator-CA logins, and a reseal drops Key B trust.
  - Client certificates require ClientAuth EKU.
- **Per-tenant key providers.** `cert.WithKeyProviderFactory` and `server.WithCertKeyProviderFactory` take a `cakey.ProviderFactory`, so go-xkms HSM/KMS providers hold one key per tenant.
- **Security fixes.**
  - Tenant admins can no longer change their own quotas or entitlements on the embedded transport.
  - Audit queries are confined to the caller's tenant.
  - Pre-auth message, stream and connection limits.
- **SDK additions for embedders.**
  - Guarded DAO writes and an atomic multi-record batch over every transport.
  - UnitOfWork's non-atomic fallback is removed.
  - Collision-safe deterministic IDs (SHA-256 truncation, create-if-absent, natural-key verification).
  - The secure atomic file writer and rate limiting are exported.
  - In-memory nodes no longer need a data directory.

### Fixed

- **`database barrier-officer`/`barrier-init-shamir`/`barrier-quorum`/
  `barrier-rekey` were non-functional on every real boot with auth enabled
  (the default) — a 501 `Unimplemented` ("database share holders service is
  not configured") on every transport.** `NewProvisioningAuthorizedDatabaseService`
  wraps `databaseSvc` whenever auth is enabled, and embeds the
  `service.DatabaseServicer` *interface*, not the concrete `*DatabaseService`.
  Embedding an interface value only promotes that interface's own methods,
  never an additive interface the underlying concrete value also satisfies —
  so `s.databaseSvc.(service.DatabaseHolderServicer)` failed in production
  even though `*DatabaseService` has always implemented it.
  `provisioningAuthorizedDatabaseService` now declares all ten
  `DatabaseHolderServicer` methods itself as pure passthroughs to the inner
  service, guarded by a compile-time assertion
  (`var _ service.DatabaseHolderServicer = (*provisioningAuthorizedDatabaseService)(nil)`)
  and a unit test that a capability-only inner is never delegated to while a
  holder-capable inner always is, so a wrapper that embeds an interface
  instead of the concrete type can't silently drop methods again.
- **`docs/databases/cli.md`** still said `--barrier-strategy shamir` was
  refused with `UnsupportedBarrierStrategyError` "until per-database Shamir
  share holders ship" — stale; the feature has shipped. Corrected and
  cross-linked to the Share-holder verbs section.
- **Integration coverage: membership, join tokens and cluster unseal.** `admin member add/remove` now adds and removes real spare nodes, since the `guarded-writes` gate correctly refuses an address that is not advertising. `admin join-token mint` is proven single-use on every transport, with the typed refusal on BYO-TLS nodes. `barrier cluster-unseal` runs a full seal, restart and unseal cycle through the CLI on every transport.
- **MCP `qrdb_ceremony_sign_csr` returns the signed certificate.** The handler marshalled the internal result type, whose fields carry no JSON tags, instead of the wire `SignCSRInitResponse`. So `qrdb ceremony sign-csr` over MCP exited 0 with an empty certificate. The other transports were unaffected.
- **Verified: `qrdb audit query --tenant-id` is confined to the caller's own tenant on every transport (go-qrdb#572).** The server-side confinement (`tenant.ConfineAuditFilter`) already covered REST, gRPC, Unix, QUIC, MCP and the embedded client; added a CLI-driven, real-daemon integration test (`TestAudit_CLI_TenantConfinement_PerProtocol`) proving a foreign `--tenant-id` is refused and the caller's own (or an omitted one) succeeds through the real client binary on all five network transports.
- **Integration coverage: every system-scope `barrier` passphrase and Shamir verb runs on all five transports.** This covers officer register, list, claim and claim-begin/complete; quorum seal, unseal and root-token; rekey; the recovery domain; `ceremony sign-csr`; `batch import`; `audit verify`; node and database compaction. The CLI test binary is now built with the daemon's full tag set, so the SQL protocol surface is actually exercised.
- **The standalone integration stage's Docker image baked `qrdbd` and `qrdb` from a literal, narrower tag list than `BUILD_TAGS`** (`'codec_json raft qrdb_ts qrdb_vector'` for `qrdbd`, `'qrdb_ts qrdb_vector'` for `qrdb`), so the server and the CLI driving it had each compiled a different, incomplete feature set — fourteen standalone tests failed on "unknown flag" for `--sql-concurrency`, `--etcd`, `--mysql`, `--postgresql`. `.devcontainer/Dockerfile`'s integration stage now takes a `QRDB_BUILD_TAGS` build ARG that `make integration-image` overrides with `BUILD_TAGS` itself, and a new buildgate (`test/buildgates/docker_build_tags_test.go`) asserts the ARG's default stays byte-for-byte equal to `make -s print-build-tags` so the two cannot drift apart silently again.
- **The standalone Shamir server's bootstrap entrypoint signed the bare claim nonce instead of the domain-separated claim message** `pkg/ceremony/claim_message.go`'s `verifyShareClaim` checks against, so every officer's `claim/complete` failed "nonce signature verification failed" and the holders file the standalone Shamir CLI tests depend on came up with zero holders every run. The entrypoint (`test/integration/api/docker-compose.yml`) now signs `ShareClaimMessage`, the same message the product's own claim verification expects.

- **The documented Key B authentication parity gap is closed.** A Key
  B-issued login certificate now authenticates on the client-facing
  listeners of both the qrdbd daemon composer and an `sdk/go/node`-embedded
  one that supplies its own TLS config via `server.WithTLS`. Proven directly
  for all five remote transports in
  `sdk/go/conformance/key_b_login_test.go`.
- **A latent QUIC ALPN-negotiation bug the dynamic trust pool would have
  triggered in production is fixed before it shipped.** `pkg/api/quic/
  server.go` clones the shared TLS config and pins its own ALPN protocol,
  but `Clone()` copies a `GetConfigForClient` closure's reference, not its
  effect: the closure still cloned from the PRE-pin config, so any QUIC
  connection that took the dynamic (Key B) path would have been served the
  wrong ALPN list and failed the handshake. The listener now wraps the
  dynamic hook and re-pins its ALPN list on whatever config the hook returns.
- **The same closure-replacement hazard existed on REST, gRPC, Unix and
  MCP's TLS 1.3 `MinVersion` floor**, not only QUIC's ALPN pin: each
  `WithTLS` cloned and mutated the supplied config directly, so a
  connection taking the dynamic trust-pool path would have been served
  whatever `MinVersion` the pre-override config carried -- measured as a
  TLS 1.2 downgrade. All four now compose with the same
  `clienttrust.ReassertListenerTLS` helper QUIC's ALPN pin uses.
- **`getCAMaterial` and `ExportPKCS12` could mix up two tenants' CA
  certificates under concurrent calls.** Both read back the single
  process-wide `p.caCertPEM`/`p.caCert` cache after resolving a specific
  tenant's provider, so a second tenant's concurrent call landing in
  between could hand the first tenant's caller the SECOND tenant's CA
  certificate. Both now resolve and return the specific tenant's own
  certificate directly from its `cakey.Provider`, never through the shared
  cache.
- **`TestGuardedVerbConformanceMatrix`'s leased-transaction-write assertion
  was pinning behavior the product had already and deliberately stopped
  having.** "statemachine: the KV state machines keep durable leases" gave
  every KV-family `raft.sm_type` (`memory`/`standard`, `fast`, `disk`, and
  their siblings) a real lease table, so a transaction write naming a lease
  the state machine actually granted now binds its key to it; only the
  legacy `regular` type still refuses one outright. The conformance test
  still asserted a blanket capability refusal for every non-default
  `sm_type`, which that change made false without updating it. The
  assertion now grants a real lease and expects the bound write to succeed,
  and separately proves an unknown lease ID is still refused with
  `*transport.LeaseNotFoundError` -- not a capability refusal, since the
  capability is present and only the named lease is missing.
  `docs/transactions/architecture.md` and `docs/key-value/engineering.md`
  carried the same stale claim and are corrected alongside it.
- **`ExecuteTransaction` and `ExecuteBatch` mapped every remote transport's
  "lease not found" refusal to a generic not-found error instead of
  `*transport.LeaseNotFoundError`**, on gRPC, Unix, QUIC, MCP and REST. Each
  transport's generic not-found mapping has no way to know a NotFound
  response is about a lease rather than a key -- unlike the dedicated
  lease-by-ID RPCs, which already carry this disambiguation because the
  caller supplies the lease ID being asked about. `checkResultError`'s only
  NotFound-classed outcome for a transaction or batch command is exactly
  this refusal, so each transport now reconstructs the typed error from the
  lease ID already present in the request's own ops, the same structural
  certainty the lease-by-ID endpoints already relied on, rather than
  guessing from the server's message text (the substring classification
  go-qrdb#289 deleted). The embedded transport was unaffected: it decodes
  `service.LeaseNotFoundError` directly instead of going through a shared
  status code.

### Changed — BREAKING

- **Every backward-compatibility affordance in the tree has been removed
  outright; nothing redirects to its replacement anymore.** Per the owner's
  no-backward-compatibility ruling, an old command, alias or API shape is
  deleted rather than kept as a stub. There is no migration: callers move to
  the canonical name directly.
  - **CLI redirect stubs deleted.** `qrdb certs` (renamed to `qrdb ca`) and
    `qrdb ceremony init` (collapsed into the root `qrdb init`) are no longer
    registered at all; running either now gets cobra's ordinary "unknown
    command" error instead of a typed redirect message.
    `CanonicalRedirectError` and the now-unused `newCertsCmd` aggregator
    (orphaned since the `ca` command group replaced it) are gone, along with
    the `annotationSkipClient` plumbing that existed only to let the
    redirect stubs run without a reachable daemon.
  - **SDK: dead Shamir split/combine aliases removed.** `sdk.ShamirSplit`,
    `sdk.ShamirCombine` and `sdk.ShamirIntegrityError` are gone; they had no
    remaining callers and their doc comments already pointed external users
    at `go-quicraft/pkg/crypto/shamir`'s `SplitProtected`/`CombineProtected`
    directly.
  - **SDK: `seal.Barrier`/`seal.NewBarrier` and `sdk.Barrier`/`sdk.NewBarrier`
    removed.** Use `seal.StorageBarrier`/`seal.NewStorageBarrier` (and the
    SDK's `StorageBarrier`/`NewStorageBarrier` aliases) directly.
  - **SDK: `sdk.ErrEmbeddingProviderInvalid` removed.** It was never produced;
    match `*EmbeddingProviderNotFoundError` instead
    (`pkg/embedding.ErrProviderInvalid` is also gone).
  - **SDK: the `ErrCode*` error-code aliases removed from the Go SDK.** Use
    the canonical `Err*` names (`dberrors.ErrNotFound`, etc.), already
    re-exported under those names.
  - **Time-series and vector storage: `WithBarrier`/`NewTimeSeriesStorageWithBarrier`/
    `TimeSeriesWithBarrier`/`WithEncryptingFS` removed** from
    `pkg/timeseries`, `pkg/storage/vector`, `pkg/store` and their SDK
    re-exports. Use `WithEncryptor(atrest.New(barrier))` (and the SDK's
    `TimeSeriesWithEncryptor`/`NewTimeSeriesStorageWithEncryptor`), which all
    three packages already supported and which these shapes only wrapped.
  - **`protocol.EncodeIndexCreate`/`DecodeIndexCreate` removed.** They could
    not express the case-insensitive index flag; use
    `EncodeIndexCreateOpts`/`DecodeIndexCreateOpts`, which every production
    caller already used.
  - Two self-test-only dead error types, `PasswordAuthDeprecatedError` and
    `DeprecatedOperationError` (`pkg/api`), are removed: neither had a
    production caller.
  - **`pkg/secret.SecretValue.Bytes()` and `.String()` are removed.** They
    were the one exception v0.41.0-alpha's context-threaded seal API left in
    place, opening under a fixed `context.Background()` rather than a
    caller-supplied context. Read the plaintext through the ctx-taking
    `WithBytes(ctx, fn)` or `Open(ctx)` instead; `SecretValue` no longer
    implements `fmt.Stringer`.
  - **`seal.strategy: software` alias removed.** It named the same strategy
    as `passphrase` and is now refused like any other unknown value:
    `seal.strategy: must be one of: passphrase, shamir, none`. Change
    `strategy: software` to `strategy: passphrase` in any config. The same
    alias in `pkg/quota`'s `Entitlements.AllowsBarrierMode` is also gone.
  - **REST `?stale=true` removed; use `?consistency=eventual`.** The two
    named the same read mode. A request using the old parameter name is now
    read as an unrecognized query parameter rather than silently switched to
    eventual consistency.
  - **System-role-ID UUIDv5→UUIDv8 dual-read compatibility removed.**
    `role.ResolveSystemRoleID`, `role.SystemRoleIDs` and their SDK
    re-exports (`sdk.ResolveSystemRoleID`, `sdk.SystemRoleIDs`) are gone, along
    with `rbac.InvalidRoleLookupError` and `rbac.CrossTenantFallbackError`,
    which existed only to support that path. Every system role ID is now the
    single canonical `role.SystemRoleID(tenantID, roleName)` derivation
    (RFC 9562 §5.8 UUIDv8 over SHA-256); there is no alternate legacy
    (UUIDv5/SHA-1) identifier a role can be persisted under, and nothing to
    resolve. System roles are undeletable and name-immutable, so this
    removes no reachable behavior on a cluster that has ever run a
    FIPS-140-3-migrated release.
  - **`pkg/pin` legacy Argon2id parameter defaults removed.** A stored PIN
    record's hash parameters are always read from the record; a record
    carrying no parameters now fails closed instead of falling back to a
    hardcoded legacy default.
  - A buildgate (`test/buildgates/no_deprecated_test.go`) now fails the build
    if any non-generated, non-vendored Go source or `.proto` file declares
    `Deprecated:`/`deprecated = true`, or if the CLI defines a cobra
    `Deprecated` field or a redirect stub, so none of this creeps back.
- **`securefile.WriteFileAtomic` refuses a pre-existing symlink at its
  target (#580).** Checked with `os.Lstat` before anything is written,
  reported as the new typed `*SymlinkRefusedError`, instead of the previous
  unconditional `rename(2)`, which replaced the symlink itself (not
  whatever it pointed at) with a plain file. This reaches every
  `pkg/clustertls`/`pkg/clusterenroll` write of cluster CA material, an
  enrollment key, or the role file -- a deployment that points its data
  directory at a **Kubernetes Secret volume mount** (every file under one
  is itself a symlink the kubelet swaps on rotation) now gets the typed
  refusal on every boot that writes, where it previously got the symlink
  silently severed into a plain file. See
  [docs/clustering/engineering.md](docs/clustering/engineering.md) and
  [docs/clustering/user.md](docs/clustering/user.md)'s troubleshooting
  entry.
- **`UnitOfWork.Commit` now REQUIRES the store to implement
  `kvstore.MultiGuardedBatchKVStore` (#589), and that changes which
  permission `Commit` needs.** It refuses with
  `*dao.AtomicBatchUnsupportedError` otherwise; the sequential
  apply-with-best-effort-compensation fallback is removed entirely
  (coordinator ruling) because it was not atomic against a concurrent
  reader. The removed fallback applied each queued operation as a plain
  `kv.Put`/`kv.Delete` (`kv:put`/`kv:delete`); the guarded-batch path
  `Commit` always takes now is the same `ExecuteTransaction` RPC
  `PutAllIfAllUnchanged` uses, gated by `transaction:execute` on every
  transport. A caller holding only `kv:put`/`kv:delete` could commit a
  `UnitOfWork` before and is refused with `PermissionDenied` now; the
  built-in `Operator` role holds both and is unaffected. See
  [docs/transactions/engineering.md](docs/transactions/engineering.md).

- **`qrdb completion` and `qrdbd completion` failed with a connection error
  on a host with no reachable daemon** instead of printing their static
  script: `cmd/qrdb/root.go`'s `PersistentPreRunE` always tried to build an
  SDK client first. Fixed by `isCompletionCmd`, anchored at exactly the
  root's direct `completion` child and that child's own direct children
  (bash/zsh/fish/powershell) -- not a walk of the whole parent chain, so a
  command elsewhere in the tree that merely happens to be named
  "completion" is never also exempted.
- **A new prefix tenant had no built-in roles until its first `roles
  reconcile` call**, contradicting docs/identity-rbac/architecture.md's
  "each tenant seeds its own built-in roles." `tenant.TenantService.CreateTenant`
  now seeds them in the same operation for prefix and system tenants (whose
  IAM store is reachable immediately); a dedicated-isolation tenant's own
  store may not exist or be unsealed yet, so it is still left to a later
  `roles reconcile`, unchanged from before.
- **`data_root` is removed from the tenant surface**, replaced by
  `volume_pool` per docs/volume-pools/api.md: `TenantCreateRequest`,
  `TenantResponse`, and `DeletedTenant` reserve their old `data_root` field
  numbers (5, 8, and 4 respectively) rather than reassign them; the Go SDK's
  `CreateTenantRequest.DataRoot`, `TenantInfo.DataRoot`, and
  `DeletedTenant.DataRoot` fields and the TS SDK's `dataRoot` fields are gone,
  replaced by `VolumePool`/`volumePool`; the CLI's `tenant create --data-root`
  flag is gone, replaced by `--pool`; and the desktop GUI's Data Root field is
  gone, replaced by a Volume Pool field shown only for the volume/raft
  isolation tiers.

### Security

- **New: an injected Key B provider is now resolved per tenant.**
  `cert.WithKeyProviderFactory`/`server.WithCertKeyProviderFactory` let an
  embedder supply a `cakey.ProviderFactory`, resolved per tenant
  (`ProviderForTenant(tenantID)`) exactly like the built-in software path.
  A single provider shared across every tenant would make the
  issuer-tenant binding (`IsTenantIssuer`) vacuous: every tenant's
  presented certificate would verify against the SAME injected CA, so the
  binding would answer true for whichever tenant a certificate's Subject OU
  merely claimed, regardless of which tenant the signing key was actually
  provisioned for (owner ruling, go-qrdb#591's review). An embedder
  resolves its own key per tenant via `ProviderForTenant`/`TenantIDs`.
  `cert.DefaultCertProvider.CACertPEMs` enumerates a factory-backed
  deployment's tenants via `factory.TenantIDs(ctx)`, since qrdb's own store
  has no record of a factory-backed tenant's CA.
- **BREAKING: per-tenant Key B CAs replace the single shared login CA.**
  Every tenant now has its OWN registered Key B CA
  (`__sys:cert_ca/{tenantID}/`), and the issuer-tenant binding
  (`auth.IssuerVerifier`, above) enforces that a tenant's login certificates
  are anchored at ITS OWN CA, not any other tenant's. **An existing tenant
  login certificate anchored at the OLD single shared CA stops
  authenticating once the binding is wired**, because that CA is not the
  new per-tenant one the binding checks against. There is no migration
  code: old systems reissue affected tenants' login certificates against
  their own CA. This is a pre-publication breaking change with zero
  external consumers to migrate at the time it landed.
- **A verified on-behalf cluster-forwarding assertion now requires the
  cluster CA as the peer certificate's issuer, not merely a node-shaped
  SPIFFE SAN.** `forward.ClusterPeer` checks only the SAN's SHAPE; with
  every tenant's Key B CA trusted on the same client-facing listener, a Key
  B leaf carrying a forged node SPIFFE SAN could otherwise present an
  honored on-behalf assertion AS THE CLUSTER. `AuthMiddleware.
  authenticateOnBehalf` now also requires `IssuerVerifier.IsClusterCA`
  against the verified chain's direct issuer, failing closed when unknown.
- **The dynamic Key B trust pool and the issuer-tenant binding could
  disagree about whether Key B was trusted at all.** Each composer wired
  `SetKeyBProvider` without a corresponding cluster-CA resolver reaching the
  binding (or vice versa), so Key B could be trusted at the TLS layer with
  the binding unenforced at the authentication layer, or built as two
  independently-constructed pools that drifted out of sync with each other.
  `clienttrust.Pool` is now the ONE object both halves read, resolving the
  cluster CA fresh and unconditionally on every check (see `pkg/clienttrust`
  above); a transient resolution failure withdraws Key B trust for that
  call only, never stickily.
- **Every per-handshake scan of `__sys:cert_ca/` parsing every tenant's CA
  was an unauthenticated, pre-auth cost an attacker controlled the rate
  of**, measured at ~20ms at 1,000 tenants. The widened trust pool is now
  cached for 2 seconds, invalidated immediately on a CA create/rotation
  (`SetKeyBProvider`/`SetClusterCA`), cutting the measured cost to ~20µs.
- **`IsTenantIssuer` and `CACertPEMs` could create certificate authority
  material as a side effect of being asked about it.** `IsTenantIssuer`
  called the built-in per-tenant provider's `Certificate` directly, which
  auto-initializes a brand-new CA key via `cakey.SoftwareProvider.material`
  on first use -- so an authentication-path probe for a tenant that had
  never established a CA (including a nonexistent tenant named by a
  presented leaf) minted one. `IsTenantIssuer` is now load-only: a tenant
  with no established CA answers `false`, never creating one.
  `Initialize`/`Unseal`/`getCAMaterial`'s own auto-init path is now
  serialized per tenant for the same reason: two callers racing the
  first-ever issuance for a brand-new tenant could both observe "nothing
  exists" and both generate and persist a CA key, with whichever write
  landed last silently winning over the other caller's already-cached,
  now-orphaned in-memory key.
- **BREAKING: a TLS handshake's trust resolution can never generate a CA,
  and is bounded by a deadline derived from the handshake itself, never
  `context.Background()`.** `ceremony.CeremonyCA.CACertificate` and
  `clienttrust.ClusterCA.CACertificate` now take a `context.Context`.
  `ceremony.ProviderCA.CACertificate` prefers the new
  `cakey.CertOnlyProvider.CertificateOnly` when the wrapped provider offers
  it, so the dynamic client-auth trust pool's cluster-CA resolution
  (`pkg/clienttrust.Pool`) can never establish Key A as a side effect of a
  connection attempt -- before this, the first TLS handshake a listener
  received was ALSO what lazily bootstrapped a self-issued cluster's Key A,
  an accidental side effect of an inbound connection. Every composer
  (`cmd/qrdbd`, `sdk/go/node`) now calls the new
  `lifecycle.EstablishClusterCA` once, deliberately, after `BuildServices`
  succeeds, to establish Key A explicitly instead.
- **A deleted tenant's Key B CA stayed in every node's trust-pool cache.**
  `DefaultCertProvider` now watches the replicated `__sys:cert_ca/` prefix
  and the tenant tombstone prefix through the node's own `Watch` service; a
  delete on a tenant's `material`/`certificate` record, or the appearance of
  its tombstone, evicts `perTenant[tenantID]` and invalidates
  `clienttrust.Pool`'s cached trust bundle (`Pool.SetKeyBProvider` now
  subscribes through the new `clienttrust.InvalidationSource` interface).
  This runs on every node, not only the one that handled the delete, and a
  factory-backed tenant with no store record is still covered by the
  tombstone watch. Proven on a 3-replica Raft cluster
  (`sdk/go/node/cert_eviction_cluster_test.go`); a literal
  delete-then-recreate-same-ID test is not possible against the real API,
  since tenant IDs are tombstoned permanently by design, so the test
  triggers eviction directly through the node's own privileged KV service
  instead.
- **`IsTenantIssuer` took an exclusive lock on every call to rebuild the
  trust-pool's Key B cache.** `clienttrust.Pool`'s cache rebuild is now
  singleflighted (`golang.org/x/sync/singleflight`), so N concurrent
  handshakes hitting an expired cache trigger exactly one rebuild, and
  `DefaultCertProvider.resolveKeyProvider`'s per-tenant provider lookup now
  takes a read lock for the cached fast path, escalating to a write lock
  only to construct a tenant's provider the first time.
- **A boot-time mutation of a LIVE listener's shared `*tls.Config` was a
  genuine, reproducible data race.** `cmd/qrdbd`'s sealed-window listener
  can still be completing an in-flight handshake against
  `bundle.TLSConfig` when `pkg/api/rest.Server.Stop`'s 5-second shutdown
  timeout expires and boot proceeds to install the auth-derived
  `ClientAuth`/`ClientCAs` default -- previously by writing those fields on
  the shared config directly, racing `configForClient`'s reads of the same
  fields on every handshake. `TLSBundle.clientAuth` is now an
  `atomic.Pointer`, and `wireClientTrustPool`'s `GetConfigForClient` hook
  returns a `Clone()` with the atomic override applied, so every handshake
  sees a consistent snapshot instead of a config being mutated under it.
  Proven with a `-race` test driving real handshakes concurrently with
  `SetClientAuthMode`.
- **The embedded transport cannot be used to set a tenant's own plan
  (go-qrdb#574).** `tenants:create`, `tenants:set_quotas`,
  `tenants:set_entitlements` and an isolation-tier change are
  platform-operator-only (ScopeCross) on every network transport, enforced
  by `AuthMiddleware`'s entry check before the request reaches the tenant
  service. The embedded transport runs no `AuthMiddleware` and had no
  equivalent check at all: `Transport.CreateTenant`, `UpdateTenantQuotas`
  and `UpdateTenantEntitlements` called straight into the `TenantServicer`,
  so an embedder that itself binds a tenant-scoped `auth.Identity` onto the
  context before calling (`auth.WithIdentity`, the same exported function
  `AuthMiddleware` uses to bind an authenticated REQUEST's identity for the
  network transports -- a DIFFERENT, unrelated call, since the embedded
  transport's methods read whatever the caller already put on `ctx` and run
  no middleware pipeline of their own) could create tenants or raise its
  own plan's quotas and entitlements. New
  `tenant.RequirePlatformOperator` enforces the identical rule directly
  (typed `*tenant.PlatformOperatorRequiredError`, mapped to
  `PermissionDenied`); `Transport.UpdateTenant` calls it only when the
  patch also changes `IsolationType`, mirroring
  `AuthMiddleware.AuthorizeTenantUpdate`. An embedded caller with no bound
  identity -- a trusted in-process host with no auth wired up, the
  existing default -- resolves to the system tenant exactly as `tenant.Confine`
  already treats it for certificates and audit queries, so this adds no
  new restriction for that case.

### Added

- Integration coverage for `qrdb completion`/`qrdbd completion`
  (bash/zsh/fish/powershell), `qrdbd node-identity`, and `tenant
  list-deleted`/`reconcile-deleted`, closing a CLI-coverage audit finding
  that all three had none.
- `pkg/clienttrust`: the ONE shared implementation of the dynamic
  client-auth trust pool AND the issuer-tenant binding (`IsClusterCA`,
  `VerifyIssuer`, implementing the new `auth.IssuerVerifier` interface) --
  deliberately the SAME object, so Key B is never trusted at the TLS layer
  without the binding also being enforceable at the authentication layer.
  `Pool.Wire` installs `tls.Config.GetConfigForClient` on a composer's own
  `*tls.Config`, verifying a presented client certificate against Key A
  (the config's own `ClientCAs`, read fresh on every connection) plus every
  tenant's Key B CA certificate (`Pool.SetKeyBProvider`, installed once the
  `CertProvider` exists post-unseal). `Pool.SetClusterCA` is resolved fresh
  and unconditionally on every check too, for the cluster-CA EXEMPTION
  (both at the TLS layer -- adding the cluster CA to the widened pool --
  and the issuer-tenant binding) -- never configuring it (no cluster-CA
  concept at all) still trusts Key B via the per-tenant binding alone;
  an installed resolver that starts FAILING (a reseal, a transient store
  failure) withdraws Key B trust entirely, for that call only. The widened
  pool is cached for 2 seconds (`SetKeyBProvider`/`SetClusterCA` invalidate
  it immediately), mitigating the per-handshake cost of scanning and
  parsing every tenant's CA certificate (measured at ~20ms at 1,000
  tenants uncached, ~20µs cached). A Key B CA created, or rotated to an
  HSM-backed one, after the listener started is picked up within that
  window, with no restart.
  - `clienttrust.ReassertListenerTLS` is the ONE shared helper every
    listener-specific TLS override (REST/gRPC/Unix/MCP's TLS 1.3
    `MinVersion` floor, QUIC's exclusive ALPN protocol pin) composes with,
    rather than each hand-rolling the same clone-and-rewrap: a bare
    `Clone()` of a config carrying a dynamic `GetConfigForClient` hook
    copies the hook's closure, not its effect, silently dropping the
    override on any connection that takes the hook's widened-pool path.
  - `cmd/qrdbd`'s `TLSBundle.wireClientTrustPool`/`SetKeyBProvider`/
    `SetClusterCA`/`TrustPool` and `sdk/go/node`'s `wireClientAndServer`
    (which now builds ONE pool and wires it to both the TLS layer and the
    auth middleware) both wire this package rather than keeping independent
    copies of the logic; the `qrdb_embedded` composer opens no network TLS
    listener, so there is nothing to wire it onto there. The
    peer/Raft/enrollment transport is unaffected: it already built its own
    trust pool directly from Key A's certificate PEM, never from a
    client-facing listener's config, so a Key B leaf continues to be refused
    there (now proven directly, `pkg/clustertls/peer_trust_domain_test.go`).
- `auth.IssuerVerifier`: a verified client certificate's issuing CA must
  be the cluster CA (Key A, any tenant) or the Key B CA registered for the
  tenant the certificate's own Subject names -- never a foreign tenant's Key
  B CA. Checked against EVERY verified chain `MTLSAuthenticator.Authenticate`
  is handed, succeeding if any one satisfies it, and separately in
  `AuthMiddleware.authenticateOnBehalf` (which never runs the authenticator
  chain, so does not inherit the first check) against the on-behalf
  forwarding path's peer certificate, so a Key B-signed node-SAN leaf cannot
  forward a request as the cluster. `cert.DefaultCertProvider.IsTenantIssuer`
  is the seam it reads Key B's registered CA from, LOAD-ONLY -- a probe for
  a nonexistent tenant answers false rather than minting a CA to answer
  itself. `pkg/auth/wiring.BuildAuthMiddleware` wires the SAME verifier into
  both consumers from one `IssuerVerifier` config field, giving `cmd/qrdbd`
  and `sdk/go/node` parity in one place. A mismatch is
  `*auth.IssuerTenantMismatchError`. `Pool.IsStaticallyTrusted` adds a third
  exemption alongside the cluster CA: a certificate already trusted via the
  listener's OWN static `ClientCAs` (an operator-supplied additional client
  CA, or a deployment establishing mTLS identity through its own CA
  independent of Key A/Key B) is pre-existing trust the binding does not
  retroactively restrict -- Key B's dynamic widening is additive to that
  trust, and the binding polices the addition, not what was already there.
- `cert.DefaultCertProvider.Initialize`/`Unseal`/`getCAMaterial`'s
  auto-init-on-first-use of a tenant's built-in CA key is now serialized per
  tenant: two callers racing the first-ever certificate issuance for a
  brand-new tenant previously could both observe "no CA exists yet" and both
  generate and persist one, producing a leaf signed by whichever key lost
  the race to be cached in memory while a DIFFERENT key was the one actually
  persisted last.
- **A memory-engine node no longer needs a vector data directory
  (#578).** `pkg/storage/vector.MemoryVectorStorage` implements
  `vector.Storage` entirely in process memory. `lifecycle.Infra` gains a
  `Backend database.BackendType` field, consulted only when `DataDir` is
  empty -- a supplied directory is still used for Pebble-backed vector
  storage regardless of the primary engine's own backend type (unchanged:
  `sdk/go/driver/node` deliberately roots even a memory-backend node's
  vector store in a real directory when it has one). With an empty
  `DataDir`, `Backend == database.BackendMemory` selects the in-memory
  store, exactly as before; any OTHER backend with an empty `DataDir` is
  now refused with the typed `*lifecycle.EmptyVectorDataDirError` instead
  of silently handed an in-memory vector store that would lose data on
  every restart -- previously that silent fallback happened for ANY
  backend whenever `DataDir` was empty, which a persistent engine's own
  misconfiguration could trigger. `sdk/go/node` and `cmd/qrdbd` both set
  `Backend` from the same configuration value (`databases[0].backend`)
  they already use to choose the primary engine, so a memory-engine node
  continues to need no vector data directory exactly as before.
- **`sdk/go` re-exports the atomic, mode-enforcing file writer (#580).**
  `pkg/util/securefile.WriteFileAtomic` (moved out of `pkg/clustertls`) and
  new `CreateExclusive` (O_EXCL, create-only, typed `*FileExistsError`) are
  re-exported from `sdk/go`. `WriteFileAtomic` also now refuses a
  pre-existing symlink at its target -- see **Changed — BREAKING** below.
- **`sdk/go` re-exports `pkg/ratelimit`, wired into the embedded node
  (#398).** `WithRateLimitPolicies` and `WithRateLimiter` are new
  `sdk/go/node.NodeOption`s, threaded into the same
  `wiring.AuthMiddlewareConfig` the daemon builds. `sdk.TokenBucketLimiter`'s
  two construction options are re-exported as `WithRateLimitIdleTTL` and
  `WithRateLimitSweepInterval`, not the upstream `pkg/ratelimit.WithIdleTTL`/
  `WithSweepInterval` names: "idle TTL" and "sweep interval" name no
  rate-limiting concept on their own at the SDK root, matching every other
  identifier in this surface's `RateLimit*` prefix. No alias is kept under
  the unprefixed names.
- **DAO atomic primitives reach every SDK transport (#589).**
  `sdk.KVStoreAdapter` now forwards `PutIfAbsent`/`CompareAndSwap`/
  `CompareAndDelete` to the transport's `GuardedKVClient` capability, so
  `GenericDAO.SaveIfAbsent` works over every transport instead of always
  refusing with `AtomicWriteUnsupportedError`. `GenericDAO` gains
  `CompareAndSwap` and `DeleteIfMatch`, both single guarded store
  operations rather than `SaveWithCondition`'s read-then-write.
  `kvstore.KV` gains a `Delete` field so a guarded batch can remove one key
  and write others in the same atomic step. **The guarded multi-record
  batch write now reaches every transport and the Go SDK.**
  `sdk.Client.KVStore()`'s adapter implements
  `kvstore.MultiGuardedBatchKVStore` (`PutAllIfUnchanged` /
  `PutAllIfAllUnchanged`) by translating guards into `Compare`s and writes
  into `Op`s and issuing exactly one `ExecuteTransaction` call -- the same
  RPC `TransactionClient` already carries on REST, gRPC, QUIC, Unix, MCP
  and embedded, as one atomic Raft/MVCC transaction end to end. No new
  proto message or per-transport wire verb was added: duplicating
  `ExecuteTransaction` under a new name would not reach anywhere it
  cannot already go. `UnitOfWork.Commit` now requires the store to
  implement `MultiGuardedBatchKVStore` and the sequential
  apply-with-best-effort-compensation fallback is removed entirely
  (coordinator ruling) -- see **Changed — BREAKING** below, which also
  covers the permission change that comes with it. `sdk/go` gains
  `NewDAOUnitOfWork`, `RegisterDAOSave`, `RegisterDAODelete` and
  `WithDAOUnitOfWorkTimeout` -- the generic functions a `DAOUnitOfWork`
  needs to be constructed and populated from outside the module, which the
  existing type-only re-export could not reach. `kvstore.CompareAndSwapWithCurrent`
  returns the current value on a failed compare. `dao.CryptoFieldHashGenerator` /
  `CryptoDynamicFieldHashGenerator` hash a natural key with SHA-256,
  truncated to 64 bits (the standard construction; an earlier revision of
  this change XOR-folded the digest's four lanes instead, on the mistaken
  belief that folding resists a chosen-collision attack truncation does
  not), instead of FNV-1a;
  `FieldHashGenerator`/`DynamicFieldHashGenerator` are unchanged, since
  swapping their hash in place would orphan already-persisted entities. A
  move to string entity IDs (the issue's other suggested option) is an
  architecture decision left to the owner.
  **`GenericDAO.Save` and `UnitOfWork.Commit` close the write-path gap a
  hash-derived ID leaves open, and apply the coordinator ruling on what an
  occupied ID means (go-qrdb#589 review).** A NEW entity (`EntityID() == 0`)
  whose generator implements the new `dao.DeterministicIDGenerator`
  interface is created through the store's atomic conditional write. A
  record already at that ID carrying the SAME natural-key value is the SAME
  logical entity: the write is applied as an UPDATE, via `CompareAndSwap`
  against the record just read so it stays atomic against a concurrent
  writer (bounded retries on a lost race; exhausting them refuses with the
  new `*dao.SaveContentionExhaustedError`, never a collision). A record
  carrying a DIFFERENT natural-key value is a genuine collision, refused
  with `*dao.IDCollisionError` rather than silently overwritten.
  `RegisterSave` carries this same guard into `UnitOfWork.Commit`'s one
  atomic batch, so a collision refuses the WHOLE batch -- nothing in it
  applies, not even an operation queued alongside the colliding save that
  carried no guard of its own. The new `GenericDAO.GetByNaturalKey(ctx,
  value)` is the read-side half, deriving the lookup ID and calling
  `dao.VerifyNaturalKey` on the result before returning it, in one call
  that cannot be split apart and only have half of it remembered; it
  refuses with `*dao.UnsupportedIDGeneratorError` when the configured
  generator is not deterministic. Only the create/save paths verify the
  natural key (`Save`, `SaveIfAbsent`, `UnitOfWork`'s queued saves,
  `GetByNaturalKey`); `Get(id)`, `Page`, `ForEachPage` and `Delete` do not,
  since none of them carry a natural-key value to check a record against.
  **An empty key in a guarded batch now refuses with the
  typed `kvstore.EmptyKeyError`** (`sdk.KVEmptyKeyError`) instead of a
  generic error reconstructed from an RPC transport's wire response:
  `sdk.KVStoreAdapter.PutAllIfAllUnchanged`, `pkg/server/lifecycle`'s
  `ExecuteGuardedBatch`, and all four of `pkg/kvstore`'s namespaced guarded
  wrappers check every guard's and write's key before forwarding, so the
  refusal matches what an embedded, same-process caller already got.

## [0.41.0-alpha] - 2026-10-04

Upgrades go-quicraft to v0.17.0 and go-enclave to v0.7.0-alpha (go-qrdb#590). This release contains BREAKING API changes; see the entries below. Highlights:

- **Context-threaded seal API.** Every call that can reach a key provider now takes the caller's `context.Context`. This includes credentials and software-strategy constructors, barrier rotation and purge, and opening secrets. A hardware-backed provider can no longer stall a call indefinitely. Embedders update their call sites.
- **Frozen enclaves.** go-enclave now returns read-only memory from `Open`; qrdb copies into its own writable buffers wherever it needs to write.
- **Errors.**
  - `Destroy` failures are reported, never dropped.
  - `ErrSealerWiped`, `ErrEnclaveDestroyed` and `ErrOpenFailed` are classified on every transport.
  - A cancelled context maps to cancellation, not an internal error.
  - go-quicraft's membership sentinels (`ErrDuplicateMemberAddress`, `ErrDuplicateMemberIdentity`, `ErrSharedPeerCertificate`) are matched by type.
- **No format change.** v0.40.x data directories open unchanged.

### Changed

- **BREAKING: go-quicraft v0.17.0 and go-enclave v0.7.0-alpha.** Every qrdb
  function that can reach a go-enclave sealer (a key provider that may block)
  now takes a leading `ctx context.Context`, threaded from the real caller
  context through every layer rather than `context.Background()`:
  - `pkg/secure`: `NewSecret(ctx, src)`, `NewSecretFromBuffer(ctx, buf)`,
    `(*Secret).Open(ctx)`.
  - `sdk/go/secure`: the same two constructors, plus `WipeAll(ctx)`.
  - `pkg/seal`: `MustCredentials(ctx, secret)`, `CredentialsFor(ctx, secret)`,
    and the re-exported `NewCredentials`/`NewSoftwareStrategy`/
    `NewSoftwareStrategyWithParams`/`NewSoftwareStrategyFIPS` (go-quicraft's
    own signatures). `StorageBarrier.GetMasterKey(ctx)` and
    `StorageBarrier.ExportRootKeyContext(ctx)`.
  - `pkg/secret`: `NewSecretValue(ctx, raw, value, strategy)`,
    `(*SecretValue).WithBytes(ctx, fn)`, `(*SecretValue).Open(ctx)`. The
    deprecated `Bytes()`/`String()` accessors keep their fixed signatures
    (the latter is `fmt.Stringer`) and open under `context.Background()`,
    documented as the one exception.
  - `pkg/auth/middleware`: `NewSOFactor(ctx, pin, isSecure, clientIP)`.
  - `pkg/clustermig`: `(*LocalCredentials).Hold(ctx, m, credential)`.
- **`(*secure.Secret).Open` copies a FROZEN plaintext into a fresh writable
  buffer before returning.** go-enclave v0.7.0-alpha's `SealedEnclave.Open`/
  `OpenWith` now return read-only memory — a write through it faults. Rather
  than push that onto every caller, `Secret.Open` copies the recovered
  plaintext into its own freshly allocated, writable `secure.Buffer` and
  destroys the frozen source, so `Buffer`'s existing "always writable"
  contract holds for every caller unchanged. This was the only production
  write-into-an-opened-enclave site in the tree; every other opened buffer in
  `pkg/seal`/`pkg/ceremony`/`pkg/credentials` already read through `WithBytes`
  (safe on frozen memory) or was allocated directly via `secure.NewBuffer`
  (never frozen).
- `enclave.MemoryEnclave.Destroy` now returns an error (`DestroyErr` is
  gone). `pkg/secure.Buffer.DestroyErr` keeps its own name and contract
  unchanged; `pkg/seal`'s one direct `*enclave.MemoryEnclave.Destroy` call
  (the reconstructed Shamir secret in quorum completion) now logs a non-fatal
  release problem at WARN instead of silently discarding it.
- Added `ErrDuplicateMemberAddress`, `ErrDuplicateMemberIdentity` and
  `ErrSharedPeerCertificate` rows to `pkg/api/errclass`'s classification
  table (`AlreadyExists` / `AlreadyExists` / `FailedPrecondition`), ahead of
  `service.MembershipError`'s and `service.ShardStartError`'s blanket
  `Internal` fallback, so a membership add or `StartShard` refused by one of
  go-quicraft's new re-exported sentinels reports the right class on every
  transport instead of a server fault.
- `pkg/auth/middleware`'s `SOPINUnavailableError` classification now declines
  (falls through to `errclass`'s `contextRules`) when the underlying cause is
  a cancelled or timed-out context, rather than reporting every protected-
  memory failure as `AuthFailureUnavailable`. go-enclave v0.7.0-alpha's
  context contract means a context ending during the SO PIN's seal or open is
  now a real, newly-possible cause of that error, and it must classify as a
  cancellation/deadline, not a node-side fault.

## [0.40.1-alpha] - 2026-10-04

A hotfix for v0.40.0-alpha, which could not boot multi-node clusters, plus durable leases and placement data-loss fixes. This release contains BREAKING changes; see the entries below. Highlights:

- **Cluster CA boot fixes.**
  - A node restarts cleanly after its local CA key cache is deleted.
  - Nodes using operator-supplied TLS boot with no cluster CA key; CA-dependent verbs are refused with a typed error.
  - Startup checks retry while the Raft leader settles instead of crashing the boot.
- **Durable leases on every KV state machine.** Leases persist in the replicated store and survive restarts and snapshots; the lease table is hidden from reads. BREAKING: `--sm regular` is refused, and `WithKVLeaseManager` is renamed `WithKVLessor`.
- **Placement data-loss fixes.** A node that a placement does not name never serves or opens that database, and returns a typed `NotAReplica` error; tenant directories are never opened or deleted by another node.

### Fixed

- **Multi-node Raft clusters could not boot (v0.40.0-alpha).** Three
  independent defects in the replicated cluster CA (Key A) bootstrap path:
  - A restart that kept `ca.pem` but had deleted `ca-key.pem` (the ordinary
    case once persistence is confirmed) refused with "adopted key is not
    valid EC PRIVATE KEY PEM": `cakey.SoftwareProvider.AdoptIfAbsent` only
    took its load-from-store path when BOTH the key and the certificate were
    empty. An absent key now always loads the persisted material; a cached
    certificate offered alongside it must still match, or the call is a
    `*cakey.MaterialMismatchError`, keeping the split-brain check.
  - A BYO-TLS node (`--tls-ca-file`/`--tls-cert-file`/`--tls-key-file`, an
    operator-supplied CA with no key) refused to boot with "cakey: provider
    has not been initialized": `cmd/qrdbd` offered the operator's CA
    certificate to the built-in cluster CA provider as bootstrap material,
    and `lifecycle.BuildServices` always constructed that provider even with
    nothing to adopt. `TLSBundle.ExplicitTLS` now marks this path, qrdbd
    withholds the operator's certificate from the provider, and
    `lifecycle.Infra.NoClusterCAKey` leaves `Node.ClusterCAProv` nil instead
    of self-generating a cluster CA key nobody asked for. The node boots and
    serves; every CA-dependent verb (`ceremony-sign-csr`, officer
    registration, member-side CSR join) already refuses with its own typed
    error when no provider exists.
  - A Raft follower's cluster-CA persistence check failed outright on
    "not leader for shard 1, leader unknown" immediately after
    `waitForLeaderElection` returned: leadership was still settling.
    `confirmClusterCAPersisted` now retries a transient
    `*service.NotLeaderError` with bounded exponential backoff (30s budget),
    bounded by the shutdown context, and returns any other error class
    immediately. `server.SystemShardSettling` was separately found to
    recognize only the service-layer `*service.NotLeaderError` and not its
    transport-layer counterpart (`*sdk/go/transport.NotLeaderError`, which
    `sdk/go/transport/embedded` reconstructs with no `Unwrap` chain back to
    the service type) — a caller behind the embedded transport, such as the
    feature gate's self-advertisement, never retried the same settling
    window. Both typed not-leader errors are now recognized.
- **Two more startup races surfaced while reproducing the above against a
  real multi-node cluster, both in the same "not-leader while the system
  shard settles" family, at call sites with no retry at all:**
  - `provisionDatabases`'s database drop-fence write raced the same
    settling window `awaitLeader`'s local leader check cannot see (it only
    confirms a leader is known, not that this node's own write path has
    caught up), failing boot with "database: failed to update the drop
    fence ...: not leader for shard 1, leader unknown" on a follower.
    Every write now retries through `server.WhileSystemShardSettling`.
  - `startFeatureGate`'s self-advertisement unconditionally needs a leased
    membership write, which the `memory` and `disk` state machine types do
    not implement; the resulting `*transport.CapabilityUnsupportedError`
    ("statemachine: lease not configured") was fatal, so the node — the
    leader included — exited, stranding every follower. The node now
    boots and serves without the optional advertisement instead, exactly
    as if no feature gate had been built.

### Changed — BREAKING

- **`qrdbd --sm regular` is refused at start.** The legacy `regular` state
  machine keeps no leases, and cluster membership and the feature gate are
  lease-backed, so a node on it could never register itself. Start fails with
  `SMLeasesUnsupportedError`; use `--sm standard`, the same in-memory state
  machine with leases. The SDK's clustered node refuses it the same way
  (`statemachine.KVOptionsNotApplicableError`).
- **`statemachine.WithKVLeaseManager` is replaced by `WithKVLessor`.** A
  `KVStateMachine` keeps durable leases in a `lease.Lessor`, its own or the one
  injected; the in-memory `LeaseManager` path it took before is gone.
  `statemachine.StorageGenericFactory` and `ResolveTypedWithStorage` take
  `KVOption`s.

### Fixed

- **The memory, concurrent, highperf, fast and disk state machines keep
  durable leases, so a cluster on them forms.** Every `KVStateMachine` answered
  a lease grant with "lease not configured", so a multi-node cluster on any of
  them failed to start: each node's lease-backed membership registration was
  refused. They now keep leases exactly as the `pebble` state machine does: in
  the lease table under `\x00qrdb.lease/` in their own keyspace, staged into the
  same atomic apply set as the write that caused each change, carried in
  snapshots and covered by the replica hash. The lessor is rebuilt from the
  table when the disk state machine opens and after any snapshot recovery, and
  starts empty with an in-memory state machine. A put, batch or transaction
  naming a lease binds the key; a later plain write or delete releases it; a
  compare-and-swap keeps it; revoking the lease deletes exactly the keys still
  bound to it. `qrdbd` and the SDK's clustered node build these state machines
  with the node's lessor, so the leader's expiry sweeper revokes their expired
  leases. The lease table is refused to client writes and hidden from reads and
  enumeration. No new opcode and no feature gate.

### Security

- **BREAKING: a node never opens or deletes a tenant directory another node
  recorded.** A `volume` or `raft` tenant's directory was recorded as an
  absolute path -- in its tenant-root record, its tenant record and its
  placement replicas -- by the node that allocated it, and every other node
  trusted that path. On hosts that share a filesystem, a node opened another
  node's live Pebble directory, and the placement reconciler's demotion purge
  deleted another node's data. A tenant's location is now recorded as the
  volume pool and relative slot it was given, and each node resolves that slot
  to its own pool path (`BarrierRegistry.LocalTenantDataRoot`); tenant records
  and placement replicas carry no path. A record that names one is refused
  rather than trusted: `*seal.AbsoluteDataRootRecordError`,
  `*tenant.PersistedDataRootError` and `*placement.ReplicaDataRootError`. There
  is no migration; re-create such a tenant. Every removal of a tenant or shard
  directory (`BarrierRegistry.RemoveDataRoot`,
  `RaftBackendFactory.CloseAndPurge`) refuses any path not strictly inside one
  of this node's own volume-pool paths
  (`*seal.DataRootOutsideLocalRootsError`).
- **BREAKING: `tenant create --data-root` is refused.** An explicit data root
  named one node's disk. The request -- `data_root` on every transport and
  `tenant.WithDataRoot` -- is refused with `*tenant.DataRootRetiredError` (400 /
  `INVALID_ARGUMENT`) before anything is created; a dedicated disk is
  configured as a volume pool. `seal.BarrierRegistry.RegisterVolumeTenant`,
  `ReattachVolumeTenant` and `ImportTenant` no longer take a data root, and
  `placement.PlaceRequest.DataRootForNode` is removed.
- **One tenant whose record names a path no longer stops a node from
  booting.** `ListTenants` returns it beside every other tenant, marked
  refused (`TenantInfo.Refusal` carries its typed error); boot skips it with an
  ERROR log and never opens its data. `DeleteTenant` removes such a tenant --
  its record, tombstoning the id, and the barrier records that would restore it
  -- without touching any directory, and the refusal's message says so:
  delete the tenant and re-create it on a named volume pool. The deleted-tenant
  reconciliation treats it as live and protects both the path it names and its
  pool directory.
- **A tenant whose location record cannot be written is not created.** A
  `volume` or `raft` registration that cannot persist its tenant-root record --
  including behind a sealed system barrier -- is undone and fails with
  `*seal.TenantRootPersistError`, rather than serving a tenant the next restart
  would not find.
- **Directory removal resolves symbolic links before deciding.**
  `BarrierRegistry.RemoveDataRoot` and `RaftBackendFactory.CloseAndPurge`
  compare the path both as given and with every link resolved on both sides,
  so a tenant directory -- or any directory above it -- that links outside the
  pool is refused with `*seal.DataRootOutsideLocalRootsError`, and a path that
  cannot be resolved is refused with its cause.

### Removed

- The explicit-data-root error vocabulary, unreachable since `--data-root` is
  refused: `tenant.ErrEmptyDataRoot`, `tenant.InvalidDataRootError`,
  `tenant.DataRootNotEmptyError`, `tenant.DataRootOverlapError`,
  `tenant.DataRootIsolationTypeError`, `seal.DataRootClaimedError` and
  `seal.ClaimedRootCheckError`. A tenant's directory is `{pool path}/{tenant
  id}` and a retired id is never reused, so no create can claim another
  tenant's directory.

### Fixed

- **A node a database's placement does not name never serves it.** It used to
  resolve the database through its own catalog and start a single-node shard
  of its own; a write acknowledged there never reached the real replicas and
  was lost when the reconciler purged the impostor. It now refuses with
  `*placement.NotAReplicaError`, which carries the replica set and is
  `UNAVAILABLE` (503) on every transport. A placement replica that names a
  path is `*placement.ReplicaDataRootError` (`FAILED_PRECONDITION`), and every
  refusal the placement resolver cannot return is logged -- at ERROR, or DEBUG
  for a node that is simply not a replica.

## [0.40.0-alpha] - 2026-10-04

The CA key provider seam and replicated cluster CA. This release contains BREAKING changes; see the entries below. Highlights:

- **`pkg/cakey`: the CA key provider seam.** Both CA keys (the cluster CA and the login CA) are reached only through `cakey.Provider` (`Signer(ctx)`, `Certificate(ctx)`), shaped like go-xkms `CASigningIdentity`. qrdb ships the software provider, and go-xkms injects HSM or KMS providers.
  - The software provider stores the certificate and key as separate records, written atomically.
  - Its signer is opaque: it re-checks the barrier on every Sign, and `Seal` revokes it.
- **The cluster CA is replicated.** Any member can sign a joiner's CSR. The CA key material is unreachable through every client data path, including the embedded transport, entity verbs and bulk import.
- **Single-use, identity-bound join tokens** (`cluster:join_token`, ScopeCross), on all six transports and both SDKs.
  - The CSR and proof of possession are validated before the token is consumed.
  - Join identities are reserved with a TTL, and existing members are refused.
  - Weak CSR keys are refused.
- **`ceremony sign-csr` is refused in the post-operational sealed window.**

### Added

- `pkg/cakey`: the CA key provider seam (`Provider`, `SoftwareProvider`,
  `StaticProvider`, `Adapter`). Both CA keys (the cluster CA and the login
  CA) obtain their signing key only through this interface, so an embedder
  (go-xkms) can inject an HSM/TPM/KMS-backed provider with no glue code and
  qrdb never holds the key.
- **Member-side CSR join uses single-use, identity-bound join tokens**,
  replacing the earlier static shared admission secret. An admin mints a
  token bound to a joining node's SPIFFE node identity and an expiry
  (`clusterenroll.MintJoinToken`); only its SHA-256 digest is stored in the
  replicated join-token store, and it is consumed atomically on first use
  (compare-and-delete) — a second presentation, an expired token, or one
  bound to a foreign trust domain, a tenant path, or an already-admitted
  identity is refused identically to an unknown token, so the response
  carries no token-validity oracle. The issued leaf carries the token's
  bound identity as its sole URI SAN, never the joiner's self-reported
  `node_uri` and never the signing member's own SANs. Join attempts are
  rate-limited and locked out per source address, shared across the Noise_NK
  and SPKI/Direct transports so switching transports mid-attack gains
  nothing; `JoinServer` also bounds concurrent in-flight Noise_NK
  handshakes.
- An admin mints a join token through `POST /v1/cluster/join-tokens` (REST),
  `qrdb admin join-token mint <node-id>` (CLI), the gRPC `MintJoinToken` RPC,
  QUIC, Unix, or the `qrdb_mint_join_token` MCP tool, all backed by a new
  `service.ClusterJoinTokenMinter` interface and permission
  `cluster:join_token` (`ScopeCross`, platform-only — no built-in role
  carries it by default). Implemented on all six transports (REST, gRPC,
  QUIC, Unix, MCP, embedded) and their SDK clients.
- `pkg/config.ResolveJoinToken` refuses a group- or world-readable
  `--bootstrap-join-token-file`, one not owned by the process's own user, or
  a resolved token shorter than `MinJoinTokenLength`, each with a typed
  error.
- The joiner verifies the leaf a signing member returns actually chains to
  the anchor it fetched and carries its own public key
  (`*clusterenroll.JoinedLeafVerifyError` otherwise), `cakey.AdoptIfAbsent`
  refuses a key and certificate that do not belong to each other
  (`*cakey.KeyCertMismatchError`), and `--bootstrap-join-url` /
  `bootstrap.join_http_url` must use `https://`.
- A sealed signing member refuses every join attempt — valid token or
  not — with the same generic message, checked before the token is ever
  consumed: consuming it first and discovering the seal only at issuance
  would both burn a legitimately minted single-use token for nothing and
  leak "this token was valid" to a prober.

### Changed

- **The cluster CA key (Key A) is stored like every other replicated system
  record — no additional encryption layered on by `pkg/cakey` itself.** An
  earlier revision wrapped it with the writing node's own
  `seal.EncryptionBarrier` before persisting it; because the record is
  REPLICATED and every node's barrier is its own, not shared cluster-wide,
  every node other than the one that wrote it failed to decrypt its own
  copy. Key A is now protected by whatever protects every other system-shard
  record on that node: its own at-rest storage encryption when one is
  configured, cluster mTLS while Raft replicates the write, and nothing at
  all under seal strategy `none`. It is persisted through
  `kvstore.MultiGuardedBatchKVStore` (`__sys:cluster_ca/`) as two separate
  records -- the private key and the public certificate -- written
  together in one atomic batch (`PutAllIfAllUnchanged`, guarded on both
  being absent), closing the same torn-write window a single combined
  record once did, while letting `Certificate(ctx)` read only the
  certificate record on a cold cache: the key record is never fetched, let
  alone parsed, unless something calls `Signer`. The `crypto.Signer`
  `Signer` returns is opaque (cannot be type-asserted back to
  `*ecdsa.PrivateKey`, and `Public()` returns a copy, not a pointer into
  the live key) and re-checks both the barrier's sealed state and the
  provider's own `Seal` generation on every `Sign` call, so a `Signer`
  obtained before either refuses afterward even though the caller is still
  holding it. `AdoptIfAbsent` refuses with a typed mismatch error, rather
  than silently discarding the loser, when the stored certificate differs
  from the one supplied to it. No node ever writes a CA private key to
  `{dataDir}/ca-key.pem` as its durable copy again — see
  `docs/certificate-authority/architecture.md`.
- Client-facing listeners (REST, gRPC, Unix, QUIC, MCP) trust the login CA
  (Key B) dynamically, loaded from the replicated store after unseal, in
  addition to the cluster CA; the peer/Raft/enrollment transport continues
  to trust the cluster CA only.
- `ceremony-sign-csr` is `PreOperationalOnly`, matching `officer-register`:
  minting a new CSR-signed certificate requires the barrier to be unsealed
  once the init ceremony is operational. The ceremony CA now goes through
  the `cakey.Provider` seam on both sides of unseal (`NewProviderCA` over a
  `StaticProvider` before persist, and over the replicated `ClusterCAProv`
  after), rather than a raw `LocalCA` built directly from `CAKeyPEM`.
- Member-side CSR issuance ships: `clusterenroll.Join` no longer refuses
  unconditionally with `*EnrollmentUnavailableError`. See
  `docs/clustering/architecture.md`.
- The first node's resumed enrollment listener requires only `ca.pem` to
  restart, not `ca-key.pem` — which `cmd/qrdbd` deletes from disk once Key A
  is confirmed durable in the replicated store, on every boot after the
  first successful one. Requiring it made a first node's own second
  restart fail closed forever; see `docs/clustering/engineering.md`.

### Removed

- The static, shared `--cluster-join-token-file` / `--cluster-join-token-env`
  flags and YAML config (`cluster_join.token_file` / `cluster_join.token_env`)
  on the signing-member side. A signing member now validates a presented
  token against the single-use, identity-bound tokens an admin mints into
  the replicated store, not a value it was configured with. The joiner-side
  `--bootstrap-join-token-file` / `--bootstrap-join-token-env` flags are
  unchanged in shape; they now carry a minted single-use token instead of a
  static shared secret.

## [0.39.0-alpha] - 2026-10-04

Recovery train: stranded fixes brought onto main. Highlights:

- **State hashes never see a half-applied batch or a half-restored snapshot (#584)**, on the concurrent, disk and standard state machines. Taking a hash briefly blocks applies; it is a diagnostic path.
- **No false 403 for tenant logins.** User and role lookups no longer consult the system-confined stale-read fallback for non-system tenants.
- **A per-push multi-node tenancy suite runs in CI.**
- **Compaction follow-ups:**
  - etcd answers Unavailable before compaction is activated;
  - an empty prefix is refused outside a dedicated keyspace;
  - reclaim is scoped to the database's incarnation;
  - a typed `CompactionPolicyReadError` is surfaced.
- **Enable-encryption migrations run on the request's context** on every transport.
- **`check-public-releasable` refuses tracked executables.**
- **Test fixes:** lockout and rate-limit tests are deterministic, and `pkg/config` coverage is restored.

### Added

- **The multi-node tenant suites gate every push.** CI's new
  `integration-cluster-tenancy` job runs `make integration-test-cluster-tenancy`,
  the cluster stage narrowed to the tenant suites by `CLUSTER_TENANCY_RUN`, and the
  CI result gate requires it. The full cluster stage stays nightly.
  `test/buildgates/cluster_tenancy_gate_test.go` keeps the selection, the job and
  the gate from drifting apart. This is phase 0 of the strict tenant isolation
  plan; the selection was measured under shared-host load before it was armed.
  The gate's test-function count is build-constraint aware (a file tagged
  `!integration` cannot inflate it), and it asserts the workflow's `on:` names
  `push` and `pull_request` and that the job carries no `if:`.
- **`make check-public-releasable` refuses a tracked compiled executable,
  whatever its content.** A 60 MB `cmd/qrdb/qrdb` built in place once reached
  a commit and was caught only because its debug info happened to name a home
  directory. The gate now classifies a tracked file by its leading bytes --
  ELF, Mach-O (thin or fat) or PE -- and fails it regardless of what it
  contains; an unreadable header fails closed. An executable tracked on
  purpose is listed by exact path, with its reason, in `EXECUTABLE_EXEMPT` at
  the top of `scripts/check-public-releasable.sh`; the list is empty today.

### Fixed

- **etcd listener: a compaction before activation is retryable.** A `Compact` refused because the
  cluster had not yet activated `prefix-compaction` (`featuregate.NotActiveError`) answered
  `codes.Internal`, which kube-apiserver's compactor does not retry. It is `codes.Unavailable`
  now, as every other transport classifies it.
- **A slow user lookup no longer denies a tenant's users.** When the
  linearizable user lookup missed its 500 ms budget, the auth middleware consulted
  its StaleRead fallback. That store holds the system tenant only, so for any
  other tenant's user it answered "no such user". The middleware treated that as
  authoritative, let the identity through with no roles, and returned a
  definitive 403 to a correctly enrolled user. It now consults the fallback for
  the system tenant only, and other tenants get the retryable 503 the transient
  error describes. Found by the measurement that armed the tenancy job: it
  reddened `TestIsolationRaft_Scale_RestartSurvival` once in ten runs.
- **The RBAC role-resolution StaleRead fallback now has the same
  system-tenant confinement as the user-lookup fallback, applied to all
  three places it is read: permission resolution, the SO deny-list, and the
  strict-SoD Owner strip.** `resolvePermissionSet`, `systemRoleIDs` and
  `staleMayDenote` consult `rolesFallback` only when the tenant in question
  is the system tenant; otherwise the primary's transient error stands. The
  fallback is confined to the system tenant's identity namespace and cannot
  answer for any other tenant's roles, so consulting it unconditionally let
  it read "no persisted record" as "no legacy override, use the canonical
  identifier" — correct for a store with full visibility into the tenant and
  wrong for one simply blind to it. Every one of a tenant's role ids then
  missed the canonical-only set and fell to `staleMayDenote`, which treats an
  identifier the replica cannot account for as one that MAY be the system
  role being checked: an ordinary tenant role was wrongly recognised as SO (a
  false 403 under FIPS 140-3 AS10.03) or wrongly stripped by the
  strict-separation-of-duties Owner strip, whenever the primary role store
  missed its 500 ms resolve budget.
- **A role or tenant store authorization failure now classifies as a
  retryable 503/Unavailable when, and only when, it is genuinely transient
  — never by guessing from a store error's free-text label.**
  `pkg/auth/middleware.ClassifyAuthError` did not recognise `*rbac.ResolveError`
  at all, so every authorization failure that reached it as one — a role
  resolve, the SO deny-list, or the strict-SoD Owner strip failing to reach
  the role store — fell through to the caller's own default instead of a
  retryable 503, even when the underlying cause (a timeout, the store
  unreachable) was transient. `resolveErrorIsTransient` is now an explicit
  ALLOW-list: `context.DeadlineExceeded`/`Canceled`; `tenant.ErrTenantSealed`/
  `seal.ErrSealed` (a barrier-isolated tenant's role store resolved to a
  sealed barrier); and a cause declaring itself transient through a small
  `transientMarker` interface (`ReadTransient() bool`). `*role.StoreError`
  and `*tenant.StoreError` both implement it, set only by their own
  package's `newStoreError` constructor at the point the underlying cause is
  first observed — never inferred from `Op`/`Detail`, which name WHERE a
  failure happened, not WHAT KIND it is. A decrypt or seal-authentication
  failure (`seal.ErrDecryptionFailed` — a tampered ciphertext, or the wrong
  key) is marked definitive, never transient, since no retry decrypts it. A
  definitive cause — the role was deleted or never existed
  (`*role.RoleNotFoundError`), the request was malformed
  (`*role.ValidationError`), or a corrupt stored record — is never retried
  against the fallback and never classifies as retryable: marking every
  `*rbac.ResolveError` retryable would turn a deleted role, which
  `DeleteRole` does not cascade from the users who still hold it, into a
  PERMANENT 503 for every request they made.
- **`resolvePermissionSet` now bounds its primary (linearizable) role
  resolve with a 500 ms timeout on every tenant, not only when a StaleRead
  fallback is configured for it.** This changes observable behavior only for
  an embedder that has not configured a role-service fallback at all, or for
  a non-system tenant (the fallback only ever serves the system tenant):
  previously that resolve ran against the caller's own context with no added
  bound, so a request with no deadline of its own could hang forever on a
  stalled ReadIndex instead of getting the retryable `*rbac.ResolveError`
  its eventual failure describes. An embedder with a configured fallback
  sees no change, since the system tenant's resolve was already bounded the
  same way.
- **Enable-encryption runs its migration on the request's context without
  the request's cancellation** (`context.WithoutCancel`) on every transport
  (REST, gRPC, Unix, QUIC, MCP, embedded), instead of `context.Background()`.
  The migration still outlives the request, and now carries the caller's
  identity and tenant, so the migration's own tenant-scope checks see the
  real caller on every transport alike.
- **A database stays inspectable when its compaction policy cannot be read.** `GetDatabase`
  failed outright when the policy read from the database's store failed, so a degraded raft
  database could not be inspected, and every `Update*` reported a failure after its change had
  committed. The rest of the information is now reported with the policy marked unavailable --
  `DatabaseInfo.compactionUnavailable` (gRPC `compaction_unavailable`, field 18; the CLI shows
  "Compaction: unavailable"), unknown rather than off -- and the cause is logged as
  `service.CompactionPolicyReadError`.
- **A shared store refuses the empty compaction prefix.** The empty prefix -- and the empty
  compaction-state scope -- is a whole store: one database's on a dedicated raft group, every
  database at once on the system shard or a volume tenant's host. The MVCC service took it on
  any store; it is refused now (`service.CompactPrefixInvalidError`, nothing proposed) unless the
  service is built with the new `service.WithDedicatedKeyspace`, which only dedicated raft
  groups' services are.
- **A drop clears only its own incarnation's compaction record.** Reclaiming a dropped
  database's keyspace cleared the compaction record under the database's name whatever
  incarnation had set it, so a re-issued drop of an old incarnation could take the policy of a
  database created again under the name. `database.KeyspaceReclaimer.ReclaimDatabaseKeyspace`
  takes the dropped incarnation, and only a record bound to it is cleared.
- **`GetHash` could land mid-batch through `DiskAdapter`, and mid-recovery
  through either state machine.** `DiskAdapter.Update` applied one entry
  per call to `KVStateMachine.Update`, which itself takes and releases the
  batch lock, so the lock was released and reacquired between every pair of
  entries handed to it as one batch -- exactly the gap the batch lock exists
  to close, reopened one layer up. Fixed by taking the lock once around
  `DiskAdapter`'s own per-entry loop and calling a new unlocked
  `KVStateMachine.applyEntries` for each entry instead of the locking
  `Update`. Snapshot recovery had the same gap from the other direction:
  `ConcurrentStateMachine.RecoverFromSnapshot` and
  `KVStateMachine.RecoverSnapshot` took no lock at all, so a hash could land
  after recovery cleared the state but before every entry was restored.
  Both now hold the batch lock exclusively for the whole recovery, the same
  way `GetHash` itself does.

## [0.38.0-alpha] - 2026-10-03

Strict configuration loading. This release contains BREAKING changes; see the entries below. Highlights:

- **qrdbd refuses a configuration file with an unknown key.** The typed `UnknownFieldError` names the key's full path and line (for example `databases[0].protocol`), so a mistyped or outdated setting can no longer be silently ignored.
- **Standard YAML features still work.** Anchors, aliases and `<<` merge keys load, and a typo inside an aliased or merged block is caught too.
- **A key for a feature compiled out of this binary is still recognized.** It is refused with the existing "not compiled in" error, not reported as unknown.
- **Documentation fixes.** Every documented example now loads under the strict check. Stale examples documented settings that do not exist (`tls.auto_tls`, a top-level `cluster:` block, `storage.engine`, nested `databases[].protocol`).

### Changed — BREAKING

- **`qrdbd` refuses a config file that carries a key the schema does not
  recognize.** `config.Load` now decodes the YAML file strictly
  before merging it with defaults and the environment: a stale or
  misspelled key -- the motivating case was a documented-but-wrong
  `databases[].protocol.etcd`, which loaded cleanly and had no effect --
  now fails the load with a typed `*config.UnknownFieldError` naming the
  key's full path (`databases[2].protocol`), the file and, where known, the
  line. A key for a feature this binary was not compiled with (e.g.
  `protocols.mysql` without build tag `qrdb_mysql`) is unaffected: it is
  still a recognized field, refused instead by the existing, separate
  `Config.ValidateCapabilities` "not compiled in" error. Operators carrying
  a stale key in their config must remove or correct it before upgrading;
  every example config shipped under `configs/`, `test/integration/api/`
  and the `docs/` reference now loads under the stricter check.

## [0.37.0-alpha] - 2026-10-03

Protocol build tags. This release contains BREAKING changes; see the entries below. Highlights:

- **Each protocol family is a Makefile flag, on by default**: `WITH_QRDB_MYSQL`, `WITH_QRDB_PGSQL`, `WITH_QRDB_SQL` and `WITH_QRDB_ETCD`. Turning one off removes all of that family's code, flags, config wiring and dependencies from the binary. With all four off, qrdbd is 44% smaller and contains no vitess, go-mysql-server or etcd code.
- **A protocol's flags exist only in builds that include it.** Elsewhere they are unknown flags, and a config that enables the protocol is refused with a typed "not compiled in" error.
- **Enforced by buildgates.** There are per-family dependency checks and an every-file-is-tagged check. `make test-tags` runs the cmd tests under every reduced build in CI.
- **Fixed:**
  - REST and MCP database create, the MCP and embedded SDK clients, and YAML `databases[]` now carry the etcd, MySQL and PostgreSQL settings instead of silently dropping them;
  - `pkg/api/errclass` no longer links vitess into every build;
  - `--enable-mysql` on a build without MySQL is now refused, no longer ignored.

### Fixed

- **`make coverage`'s package discovery now carries the same build tags its
  own `go test` call does.** `BULK_PKGS` was computed with an untagged
  `go list ./pkg/... ./cmd/...`, so a package whose every file carries a
  build tag has no buildable source under it and `go list` drops it from
  `./pkg/...` silently — not an error, just absent from the list `go test`
  is handed. `pkg/sql/mysql` and `pkg/sql/pgsql` have been in that state
  since they were first tagged, and were simply never added to
  `test/coverage-baseline.txt`, so the ratchet's MISSING check — which
  fires only for a package the baseline names — had nothing to compare
  against and stayed quiet: the coverage gate has never measured either
  package. Tagging `pkg/sql`'s other eight packages and `pkg/api/etcd` the
  same way (below) hit the same discovery bug on packages the baseline
  already named, turning eight more silent gaps into eight loud MISSING
  failures — the exposure, not a new defect. Fixed by adding `$(TAG_FLAG)`
  to the `go list` call.
- **`pkg/api/errclass` no longer links vitess into every build.** It
  imported `github.com/dolthub/vitess/go/mysql` with no build constraint,
  solely to build two numbered-error samples for a classifier parity test —
  linking vitess into every binary, including one built with
  `WITH_QRDB_SQL=0`, because `pkg/api/errclass` sits in every transport's
  import graph. Replaced with a local, dependency-free sample type
  (`numberedSampleError`) carrying only the `Number() int` shape the
  classifier matches on. Three test files making the same mistake at a
  smaller scale (`pkg/api/{grpc,quic,rest}/table_resolution_stale_test.go`,
  `pkg/api/rest/handlers_sql_test.go`, `pkg/api/rest/route_mapping_guard_test.go`)
  are split so the `qrdb_sql`-only part of each lives in its own
  `qrdb_sql`-tagged file, with a `!qrdb_sql` counterpart, rather than
  carrying no constraint at all.
- **`qrdbd start --enable-mysql` / `--enable-pgsql` on a binary built
  without the matching tag is refused, not silently dropped.**
  `resolveStartFlags` already called `Config.ValidateCapabilities` when
  `--config` named a YAML file; a protocol enabled by flag alone, with no
  `--config`, skipped that call entirely, so `startMySQLFn` / `startPgSQLFn`
  stayed `nil` and the daemon started anyway with the flag quietly having
  no effect. `runStart` now calls `ValidateCapabilities` itself, before the
  secure-memory budget or TLS bootstrap material is touched, so the typed
  `config.ValidationError` fires for every invocation shape, not only
  `--config`.
- **`StartEtcd` refuses with the same typed `service.CapabilityUnsupportedError`
  every transport maps to Unimplemented when etcd support is not compiled
  in**, naming the missing `qrdb_etcd` build tag, instead of silently
  returning `nil` and accepting a configuration it could never serve.
- **An etcd configuration is now refused before it is ever persisted, on
  every transport.** `DatabaseService.Create`, `CreateForTenant`,
  `UpdateEtcd` and `UpdateEtcdForTenant` all call a new
  `refuseEtcdNotCompiled` guard before reaching the manager: a request
  enabling etcd on a binary without `qrdb_etcd` gets the typed
  `*service.CapabilityUnsupportedError` and nothing is written, rather
  than writing the config and simply never starting a listener for it (the
  previous commit's `StartEtcd` fix covers the node-level config path;
  this closes the same gap at the per-database config path). Chose
  refusal over tagging the service methods out entirely: this project's
  transports are tier-1 on every binary — the RPC/REST surface is
  identical regardless of build tags, with capability differences
  expressed as typed refusals rather than absent endpoints — so removing
  `UpdateEtcd`'s signature under `!qrdb_etcd` would also require tagging
  every handler above it in `pkg/api/{rest,grpc,quic,mcp,unix}` and would
  make transport discovery disagree between binaries for no server-side
  benefit.
- **`qrdbd start`'s capability gate now reports `buildServerConfig`'s own
  error instead of silently skipping itself.** It only ran
  `ValidateCapabilities` when `buildServerConfig` succeeded; a failure
  there (its one failure mode is an unset bind host) used to fall through
  the gate unexamined rather than surface, the same silent-skip class of
  bug `ValidateCapabilities` exists to close one line above.
- **`sql/query/owner_check_test.go` picked up the `qrdb_sql` tag every
  other file in that package already carries.** It arrived untagged
  through a `main` merge (a new file in an already-tagged directory, not
  touched by the original bulk-tagging pass), and `make vet-tags` caught
  it: `undefined: mockDB` under any combination lacking `qrdb_sql`.
- **Three `cmd/qrdbd`/`cmd/qrdb` tests that only failed at RUN TIME under a
  reduced build, not at compile time** — `make vet-tags` could not see
  any of them: two `TestRunStart_SQLDatabase*` tests asserted
  `*UnknownSMError` unconditionally after setting
  `--enable-mysql`/`--enable-pgsql`, not accounting for the capability
  gate refusing first on a binary without the tag;
  `TestDatabaseCmd_SubcommandCount` hard-coded a count and name set that
  includes `update-etcd`/`update-protocol`/`update-sql`, all three
  tag-conditional. See Added, below, for the gate that now catches this
  class going forward.
- **`qrdb database update-protocol` registered both `--mysql` and
  `--postgresql` unconditionally inside its own `qrdb_sql` tag**, so a
  `qrdb_sql` build with only one wire protocol compiled in (the
  NOMYSQL/NOPGSQL rungs) still exposed a flag for the protocol it does not
  have. Each flag now carries its own tag.
- **`test/buildgates/etcd_dependency_test.go`'s dependency prefix widened
  from `go.etcd.io/etcd/` to the whole `go.etcd.io/` organization** — the
  claim being checked is "nothing under go.etcd.io is reachable without
  qrdb_etcd," and the narrower prefix would have missed a go.etcd.io
  module outside the `etcd/` path.
- **`pkg/sql/internal/sessionguard/sessionguard.go` and
  `pkg/sql/pgcatalog/sleep.go`** (plus their tests) arrived through a
  `main` merge with no build constraint at all — new files in
  already-tagged directories, added by a change that never touched the
  tagging convention and so never tripped `make vet-tags` (both
  typecheck fine under every declared combination, tagged or not).
  Tagged all four `qrdb_sql`.
- **REST's and MCP's `database create` silently dropped an etcd
  protocol block on every build.** `pkg/api/rest/handlers_database.go`'s
  `createDatabaseRequest.Protocol` had no `Etcd` field at all; MCP's
  `qrdb_database_create` tool had no `etcd`/`etcd_listen_addr`/
  `etcd_advertise_urls` arguments; the MCP and embedded SDK clients
  (`sdk/go/transport/{mcp,embedded}/transport_database.go`) silently
  dropped `req.Protocol.Etcd` even where the request type already
  carried it. gRPC, QUIC and Unix were already correct on inspection,
  both server and client side.
- **`databases:` YAML entries' own `mysql`/`postgresql` flags were
  silently dropped at provisioning time, regardless of build tags,
  every boot.** `configuredDatabases` (`cmd/qrdbd/provision.go`) built
  each entry's `Protocol` from nothing; only the separate
  `--sql-database` special case ever applied one. Found while adding
  the equivalent `etcd` field to the same function.
- **A `databases:` YAML entry enabling etcd on a binary without
  `qrdb_etcd` provisioned silently instead of refusing.** Boot-time
  provisioning creates databases through `*database.Manager` directly,
  bypassing `pkg/service` entirely, so the capability check added for
  `Create`/`CreateForTenant`/`UpdateEtcd`/`UpdateEtcdForTenant` never
  ran for it. `provisionDatabases` now calls the same
  `service.RefuseEtcdNotCompiled` (exported for this purpose) before
  provisioning.
- **`docs/etcd-compatibility/cli.md`** quoted a refusal message
  (`not implemented: requires build tag qrdb_etcd`) that was never the
  actual error text. Now quotes the real one.

### Added

- **Two more declared build-tag rungs: NOMYSQL and NOSQLFAMILY**
  (`make print-tag-sets`), swept by `make vet-tags` like every other
  declared combination: the default tag set with `qrdb_mysql` alone
  removed, and with `qrdb_mysql`, `qrdb_pgsql` and `qrdb_sql` all removed.
  They exist because "the package compiles" was never the property that
  mattered for the MySQL build-tag boundary — "what is in the binary's
  dependency graph" is, and nothing short of vetting these exact
  combinations checks it.
- **`test/buildgates/mysql_dependency_test.go`** asserts that property
  directly with `go list -deps` over `cmd/qrdbd` and `cmd/qrdb`: under the
  default tags, both reach vitess, go-mysql-server and `pkg/sql/mysql`
  (the discovery floor, so an empty scan cannot read as clean); with
  `qrdb_mysql` off, `pkg/sql/mysql` and go-mysql-server's listener package
  are gone while the SQL engine (and vitess underneath it) legitimately
  remains if SQL or PostgreSQL is still on; with `qrdb_sql`, `qrdb_mysql`
  and `qrdb_pgsql` all off, vitess and go-mysql-server are absent from both
  binaries entirely.
- **PostgreSQL and etcd get the same build-tag treatment as MySQL.** Every
  non-test and test file under `pkg/sql/{catalog,engine,infoschema,
  lockclause,pgcatalog,privilege,provider,query}` now carries
  `//go:build qrdb_sql` directly, rather than being gated only at import
  sites; every file under `pkg/api/etcd` carries `//go:build qrdb_etcd`.
  `test/buildgates/pgsql_dependency_test.go` and
  `test/buildgates/etcd_dependency_test.go` prove `jeroenrinzema/psql-wire`
  and `go.etcd.io/etcd/*` are absent from both binaries' dependency graphs
  with the matching flag off, the same way the MySQL test proves it for
  vitess. `WITH_QRDB_SQL=0` now forces `WITH_QRDB_MYSQL` and
  `WITH_QRDB_PGSQL` to `0` as well (the clearer of the two directions: a
  binary with no SQL engine cannot serve either wire protocol, so leaving
  them independently settable would let the Makefile accept a
  configuration the build can't honor); either `WITH_QRDB_MYSQL=1` or
  `WITH_QRDB_PGSQL=1` forces `WITH_QRDB_SQL=1`. New declared rungs NOPGSQL
  and NOETCD (plus their `+integration` variants) are swept by
  `make vet-tags`.
- **`--enable-mysql`/`--mysql-port` and `--enable-pgsql`/`--pgsql-port`
  flags, and the `qrdb sql` / `qrdb database update-protocol` /
  `qrdb database update-etcd` subcommands and `--etcd*` create flags, are
  registered only from files carrying the matching build tag**, with a
  `_stub.go` no-op counterpart under the negated tag — the same pattern
  `cmd/qrdb/timeseries_enabled.go`/`timeseries_stub.go` already established.
  A binary built without a protocol no longer exposes a flag or subcommand
  for it that would do nothing (or crash) if used.
- **`docs/sql/architecture.md`, `docs/sql/cli.md`, `docs/sql/engineering.md`,
  `docs/etcd-compatibility/architecture.md` and `docs/etcd-compatibility/cli.md`**
  now spell out exactly what each `WITH_QRDB_*` flag removes, and the
  single site that constructs the MySQL wire listener
  (`pkg/sql/mysql/server.go`'s call into `go-mysql-server/server.NewServer`,
  which is what calls vitess's listener constructor).
- **Every SQL-only CLI surface is tagged `qrdb_sql`**: `qrdbd start
  --sql-database` (`start_sql_flags.go`/`_stub.go`), `qrdb database
  update-sql` and `database create`'s `--sql-concurrency`
  (`database_sql_cmd.go`/`_stub.go`, the same apply-closure shape
  `database_etcd_cmd.go` uses for `--etcd*`), and `database
  update-protocol`'s `--mysql`/`--postgresql` further split to their own
  `qrdb_mysql`/`qrdb_pgsql` tags. `--host`'s help text now lists only the
  protocols this binary actually binds it to
  (`compiledProtocolListeners`), built from the same per-protocol appender
  functions as the flag registration.
- **`make test-tags`** (`scripts/test-tags.sh`) RUNS, not merely vets,
  `./cmd/qrdbd/...` and `./cmd/qrdb/...` under the default build and the
  four NOMYSQL/NOPGSQL/NOSQLFAMILY/NOETCD rungs. `make vet-tags` proves
  the tree typechecks under every declared combination but has no
  opinion on whether a test that compiles there also passes — a
  hard-coded subcommand count or a test asserting success on a flag a
  reduced build never registers typechecks fine and only fails at run
  time, which is exactly what surfaced writing this round's CLI changes
  (see Fixed, above). Scoped to the command surface rather than the whole
  tree and excludes the `+integration` rungs; see the script's header for
  the full scoping rationale. Now also runs in CI's Test job
  (`.github/workflows/ci.yml`, after the coverage gate) rather than only
  on demand, and its scope (`TEST_TAGS_PKGS`) covers `pkg/service`
  (non-recursive) in addition to the command surface, so
  `database_etcd_capability_test.go`'s `!qrdb_etcd` refusal tests
  actually run under the NOETCD rung.
- **`test/buildgates/sql_etcd_tag_coverage_test.go`** asserts that EVERY
  `.go` file under `pkg/sql/` carries a build constraint satisfiable
  only when `qrdb_sql`, `qrdb_mysql` or `qrdb_pgsql` is on (the SQL
  family — `pkg/sql/mysql`/`pgsql` carry their own narrower tag, not a
  compound with `qrdb_sql`), and every file under `pkg/api/etcd/` one
  satisfiable only when `qrdb_etcd` is on. Uses `go/build/constraint`
  to parse and evaluate each file's `//go:build` line rather than
  substring-matching the tag name, with a measured discovery floor so
  an empty walk cannot read as clean — the enforcement
  `pkg/sql/internal/sessionguard/sessionguard.go` and
  `pkg/sql/pgcatalog/sleep.go` (see Fixed, above) should have tripped
  and did not.
- **`sdk/go/conformance/database_create_etcd_test.go`** walks all six
  SDK transports and asserts `CreateDatabase` with `Protocol.Etcd` set
  actually enables it end to end, through a real node over real mTLS
  for the remote ones — the cross-transport conformance check for the
  REST/MCP etcd parity bug (see Fixed, above).

### Changed — BREAKING

- **A binary built with `WITH_QRDB_MYSQL=0`, `WITH_QRDB_PGSQL=0`,
  `WITH_QRDB_SQL=0` or `WITH_QRDB_ETCD=0` no longer registers that
  protocol's (or engine's) CLI surface at all** — cobra reports `unknown
  flag` / `unknown command` rather than silently accepting a setting the
  binary can never act on. Per tag:
  - `qrdb_mysql` off: `qrdbd start --enable-mysql`/`--mysql-port`; `qrdb
    database update-protocol`'s `--mysql` flag (the subcommand itself
    stays if `qrdb_sql` is still on — see `qrdb_sql` below).
  - `qrdb_pgsql` off: `qrdbd start --enable-pgsql`/`--pgsql-port`; `qrdb
    database update-protocol`'s `--postgresql` flag.
  - `qrdb_sql` off: `qrdbd start --sql-database`; `qrdb sql`; `qrdb
    database update-protocol` and `update-sql` (whole subcommands); `qrdb
    database create`'s `--sql-concurrency` flag. (Also forces
    `qrdb_mysql`/`qrdb_pgsql` off — see Added, below.)
  - `qrdb_etcd` off: `qrdb database update-etcd`; `database create`'s
    `--etcd`, `--etcd-listen-addr`, `--etcd-advertise-urls` flags.

  Scripts that pass these flags unconditionally to a reduced build must
  branch on the build's declared capabilities first; anyone building a
  custom `qrdbd`/`qrdb` with one of these tags off and scripting against
  the now-removed flag needs to drop it from the script, not from the
  daemon's behavior — the flag never did anything on that build anyway.
- **`WITH_QRDB_SQL=0` alone now removes MySQL and PostgreSQL too.** The
  Makefile forces `WITH_QRDB_MYSQL` and `WITH_QRDB_PGSQL` to `0` whenever
  `WITH_QRDB_SQL=0` is given, even if either was left at its own default of
  `1`. Before this, `make build WITH_QRDB_SQL=0` with no other flag named
  left `WITH_QRDB_MYSQL`/`WITH_QRDB_PGSQL` at `1`, which forced
  `WITH_QRDB_SQL` straight back to `1` — removing the SQL layer required
  naming all three flags, silently, with no build-time signal that the one
  flag given had no effect. Naming all three explicitly still works
  identically; only the one-flag case changes.
- **A `databases:` YAML entry's own `mysql`/`postgresql` flags are now
  honoured.** `configuredDatabases` previously dropped them at every boot
  (see Fixed, above); a node restarted on an unchanged config file that
  already set `mysql: true` or `postgresql: true` on one of its
  `databases:` entries now provisions that database visible over the
  corresponding wire protocol, where it was previously provisioned with
  neither regardless of the file. An operator relying on the old silent
  no-op (for example, a config that sets `mysql: true` on a database not
  actually meant to be reachable over MySQL) will see that database start
  answering MySQL clients on the next restart; audit `databases:` entries
  for stale `mysql`/`postgresql` flags before upgrading.

## [0.36.0-alpha] - 2026-10-03

Holder audit and Shamir custody follow-ups. This release contains BREAKING changes; see the entries below. Highlights:

- **Every share-holder action is audited** into the owning tenant's trail, including claims and unseal quorums inside the sealed window. The compliance trail is opened before the seal subsystem and fails closed. Outcomes are fixed reason tokens, never raw error text.
- **Holder events follow `auth.audit_failure_policy`.** Register, claim, rekey-start and unenrol record `permitted` before they take effect, then success or failure. Under fail-closed, an unrecordable action changes nothing.
- **Barrier events join the same trail,** with one emitter per event. The desktop app and the SDK embedded server can open the trail too (off by default). The trail is confined to its directory through `os.OpenRoot`.
- **Shamir initialization is race-free.** Registration, unenrolment and a second initialization are refused while initialization or recovery-key generation is in flight, at the database, tenant and system scopes.
- **Database custody follow-ups:**
  - registration past N is refused before initialization;
  - a new pre-init unenrol verb (`databases:officer_unenrol`) is on every transport;
  - an out-of-range M-of-N is refused at create;
  - the wizard enforces 2 ≤ M ≤ N ≤ 255;
  - the panel uses the server's `initializedAt`.

### Added

- **Share-holder events are on the compliance audit trail, in every scope.**
  The holder engine records `holder-register`, `holder-claim`,
  `holder-quorum-submit`, `holder-rekey-start` and `holder-revoked` --
  refusals included -- for the system, recovery, tenant and database scopes,
  keyed by the scope's owning tenant, so a tenant's auditors read its holders'
  events and nobody else's do. Records name the scope, the holder and the
  operation, and never a share, an envelope, a PIN, a signature, a factor
  response or a key. New: `ceremony.HolderDeps.Audit`,
  `ceremony.CeremonyService.SetAuditLogger`, `lifecycle.Infra.AuditLogger`,
  and `StartRekey` on `ceremony.CeremonyService` and
  `ceremony.ScopedCeremonyService`, through which every rekey and revocation
  of a holder set now opens. `sdk/go/node` hands the caller's
  `server.WithAuditLogger` trail to the holder engine.
  A registration, a claim and a rekey opening are recorded before they take
  effect and follow the node's one audit failure posture
  (`auth.audit_failure_policy`, `ceremony.HolderDeps.AuditPolicy`,
  `lifecycle.Infra.AuditFailurePolicy`): under the default `fail-closed` a
  record the trail refuses refuses the operation with `*audit.GateError`,
  leaving a claimed envelope held and claimable. That gating record's outcome
  is the new `audit.OutcomePermitted` (`permitted`), never `success`: a second
  record carries the operation's `success` or `failure` once it has run, so
  the hash-chained trail never holds a success for an effect that did not
  happen. Input validation -- a registration's factor names (empty, repeated
  or unknown) and its designated factor included -- runs
  before the gate. A refusal's reason is a
  fixed token, never an error's text, and a caller-chosen holder name that
  is not a valid principal name is recorded as `(invalid-name)`.
  `seal.RekeyParams.Admit` is the hook the barrier calls once a rekey is
  validated and before its round opens.

- **Unenrol a database holder before initialization, on every transport.**
  Before a Shamir database's custody is initialized a holder holds no share,
  so one enrolled by mistake, or one whose key is lost before it was used, is
  removed without a quorum and its place freed: REST
  `POST /v1/databases/{name}/barrier/officers/unenrol` (204), gRPC and the
  Unix socket `DatabaseBarrierOfficerUnenrol`, QUIC
  `db.BarrierOfficerUnenrol`, MCP `qrdb_database_barrier_officer_unenrol`,
  the Go SDK's `DatabaseClient.DatabaseBarrierOfficerUnenrol` on all six
  transports, the CLI `qrdb database barrier-officer unenrol`, the desktop
  `OfficerBinding.UnenrolDatabaseHolder`, the TypeScript SDK's
  `officers.unenrolDatabaseOfficer`, and a **Remove** in the database view.
  New permission `databases:officer_unenrol` (self-scoped, like the rest of
  `databases:*`). Once initialized, unenrolment is refused with
  `ceremony.ErrUnenrolInitialized` and a holder is removed by revocation. The
  removal is audited (`holder-unenrol`), recorded before the holder is
  removed and refused under the `fail-closed` audit posture when the trail
  cannot record it. An unenrolment -- or a second initialization -- while the
  database's custody is being initialized is refused with
  `ceremony.ErrInitializationInProgress` (`ABORTED`): the barrier reads the
  holder set twice, and a holder removed between the reads left it
  initialized with no share set until a restart. Database custody is now
  initialized through `ceremony.ScopedCeremonyService.InitializeShamir`.
  **BREAKING** for implementers of `transport.DatabaseClient` and
  `service.DatabaseHolderServicer` in Go, and of the TypeScript SDK's
  `OfficerClient` (`unenrolDatabaseOfficer`): each gains the method.
- **The barriers' own events are on the compliance trail.** Every barrier --
  system, tenant, database -- records `initialize`, `unseal`, `seal`,
  `quorum-complete`, `distribute` and the rest on the node's one trail
  through `seal.NewComplianceAuditLogger`, keyed by the owning tenant, naming
  the barrier, with allow-listed details and no key material. qrdbd and the
  SDK node wire the system barrier and their tenant registry
  (`seal.WithAuditTrail`) to the sink opened before the sealed window; the
  database barrier manager takes it through `database.WithBarrierAuditTrail`.
  Before, `seal.BarrierConfig.AuditLogger` was never configured, so these
  events reached no trail. `audit.OpenTrail` is the one trail composition.
- **The SDK's flat `embedded.NewServer` keeps a compliance trail when
  configured** (`ServerConfig.AuditTrail`, off by default; relative paths are
  under `DataDir`). It is `audit.OpenTrail`, opened before anything else and
  failing the start with `*embedded.AuditTrailError` when it cannot be
  opened; the barrier records on it and the transport serves the audit query
  from it.
- **The desktop's embedded server keeps a compliance trail when enabled**
  (`EmbeddedConfig.Audit`, off by default; `audit-events.ndjson` under the
  data directory unless `path` says otherwise). It is opened before the seal
  barrier and a configured trail that cannot be opened stops the start.
- **`database.barrierStatus(name)` in the TypeScript SDK** (**BREAKING** for
  implementers of the TypeScript SDK's `DatabaseClient`, which gains the
  method), on REST and the
  desktop binding (`DatabaseBinding.BarrierStatus`): a database barrier's
  sealed state, strategy and `initializedAt`, absent until the barrier is
  first initialized. The database view's share-holder panel reads it to tell
  a Shamir database's pending custody from an initialized one, instead of
  inferring it from the holders' share indices.

### Fixed

- **The embedded transport's `CeremonySignCSR` refuses a nil request** with
  `*embedded.InvalidRequestError`, as `CeremonyInitialize` does, instead of
  dereferencing it -- directly, and through `Request`, where a typed nil
  passed the request type check.
- **A symbolic link can no longer carry the compliance trail out of its data
  directory.** The trail followed links: one at the trail's name, or at a
  directory on its path, sent the desktop's, the SDK embedded server's and
  qrdbd's trail -- its writes, rotation renames, retention removals and
  reads -- wherever it pointed. The chain file logger now opens the trail's
  directory as an `os.Root` and resolves every file operation inside it; a
  link that resolves outside refuses the trail (`*audit.FileError`) and a
  name that climbs out is `audit.ErrOutsideTrailDir`. New:
  `audit.NewFileLoggerIn` and `audit.OpenTrailIn(dataDir, name)`, which every
  composer now opens its trail with; `NewFileLogger` and `OpenTrail` confine
  to the file's own directory.
- **A holder registration racing a Shamir initialization no longer wedges
  the barrier.** The barrier marks itself initialized and then reads the
  holder set again to split the key; a registration landing between the two
  was admitted -- the barrier already read as initialized, so a database's
  N no longer limited it -- and the split failed with "2 shares were
  requested but 3 holders are enrolled", the barrier rolled back and needing
  a restart. A registration while the barrier's custody is being initialized
  is now refused with `ceremony.ErrInitializationInProgress` (`ABORTED`;
  retry), like an unenrolment, in every scope: the tenant service's Shamir
  initialization goes through the tenant's holder authority
  (`ceremony.ScopedCeremonyService.InitializeShamir`), and the system
  barrier's -- the ceremony's initialization and
  `BarrierService.BarrierInitializeShamir` -- through the new
  `ceremony.CeremonyService.InitializeShamir`. The recovery key's generation
  is held to the same rule through the new
  `ceremony.CeremonyService.GenerateRecoveryKeys`
  (`BarrierService.BarrierGenerateRecoveryKeys` included): a recovery
  holder's registration or unenrolment, or a second generation, while it
  runs is refused rather than changing the holder set between the roster and
  the sealed shares.
- **A Shamir database can no longer be enrolled past N before
  initialization.** Two browser tabs, the database wizard beside the database
  view, or the CLI could enrol more holders than the M-of-N fixed at create;
  initialization then refused the count, and no holder can be revoked before
  initialization, so the database was stuck sealed. Registration past N on a
  database barrier not yet initialized is now refused with
  `ceremony.HolderLimitReachedError` (`FAILED_PRECONDITION` on every
  transport); once initialized, a new holder enrols and joins at the next
  rekey. The system barrier's and a tenant's initialization take the M-of-N
  with the request, so they are not limited. The database view says why
  **Initialize** cannot run for a database enrolled past N before this fix.
- **A database create with a Shamir M-of-N the seal cannot split is refused
  at create.** Outside 2 <= M <= N <= 255 the create answers
  `database.InvalidShamirSchemeError` (`INVALID_ARGUMENT`, 400 on REST) on
  every transport, before anything is reserved or persisted; before, N > 255
  was accepted and refused only when custody was initialized.
- **The database wizard holds a Shamir M-of-N to whole numbers and N <= 255**,
  the bound the server's seal enforces, instead of letting a fractional or
  oversized split through to a refusal at initialization.
- **The desktop app's mocked test tier answers a Shamir database as the server
  does:** a secret is refused before missing custody, an M-of-N the seal
  cannot split is wrapped as `BarrierCreateError`, a second initialization is
  refused, and the error texts are the server's.

### Changed

- **One emitter per audit event.** The barrier no longer emits `quorum-share`
  or `rekey-start`: a share submission is the holder engine's
  `holder-quorum-submit` and a rekey opening its `holder-rekey-start`, so a
  barrier and its holder authority on the same trail do not record the same
  event twice.
- **`qrdbd` opens its compliance audit trail before the sealed window.** The
  trail used to be opened after unseal, so the envelope claims and unseal
  quorums a sealed node serves were recorded nowhere. It is now opened before
  the seal subsystem, once per process, and start still fails closed when it
  cannot be opened -- now before any barrier is built. The process-inspection
  opt-out remains its first record.

## [0.35.1-alpha] - 2026-10-03

Sealing test hardening. Highlights:

- **Tests pin how the database quorum is routed and how the database unseal gate is wired.** Two read-only accessors back them: `seal.BarrierRegistry.TenantUnsealRefused` and `database.DatabaseBarrierManager.UnsealRefused`.
- **The gRPC barrier-seal tests now wire an SO PIN backend, as a real composer does.** They were red under tag sets without `qrdb_ts`.

### Added

- `seal.BarrierRegistry.TenantUnsealRefused` and
  `database.DatabaseBarrierManager.UnsealRefused` report whether the node
  unseal's gate refuses a tenant or a database -- its cluster seal record is
  sealed or cannot be read -- so the verdict is observable, not only applied.

## [0.35.0-alpha] - 2026-10-03

Cluster-wide sealing. This release contains BREAKING changes; see the entries below. Highlights:

- **A replicated seal record seals the cluster.** Each node keeps its own seal key and keys are never transported. A seal writes the cluster record before any key is wiped, and every node enforces it. A node cut off from the record stops serving but keeps its key.
- **A system seal seals the whole node:** every database barrier, every tenant barrier and the system barrier, through one node-seal path. On unseal, system-managed tenants and databases that the node seal sealed come back. Self-managed tenants, passphrase databases and Shamir databases stay sealed until their owner or quorum unseals them, and an owner's explicit seal is never undone.
- **Share-holder quorum seals go through the cluster seal,** writing the record first, for node, tenant and database barriers.
- **A fresh cluster serves immediately,** with no refusal window while the feature activates.
- **Sealing ends the affected watches and SQL sessions.** MySQL `COM_RESET_CONNECTION` no longer leaks a session.
- **BREAKING:** `sql.query_timeout` now defaults to 30s.
- **Known issue:** MySQL server-side cursors (`qrdb_mysql` builds) have an upstream data race, and an idle cursor isn't bounded by the timeout. See docs/sql.

### Security

- **BREAKING: sealing the node barrier seals the whole node.** A seal of the
  system barrier sealed only that barrier: every unsealed tenant barrier kept
  its root key and DEKs in memory while the node reported sealed. Every
  system-barrier seal -- the single-admin seal on every transport and a
  share-holder seal quorum, and (as before) a cluster seal record applied by
  each node -- now seals every tenant barrier first, then the system barrier
  (`BarrierRegistry.SealAll`), and ends every open stream. On unseal the
  system barrier unseals first and brings its system-managed (auto-unseal)
  tenants back with it (`StorageBarrier.OnUnsealed`) -- only those the node
  seal itself sealed, never one sealed by its own admin or its cluster record
  (`seal.WithTenantUnsealGate`, `BarrierRegistry.PendingTenantUnseals`); a
  self-managed tenant stays sealed until its own admin unseals it, which
  operators must now plan for. Every encrypted database is sealed with the
  node, its keys wiped, and stays sealed through the node unseal until its
  owner -- or its Shamir holder quorum -- unseals it; every node-seal path
  (the single-admin seal, a holder seal quorum, a node seal record, and the
  embedded node's shutdown and failed-boot seals) goes through one function,
  `service.SealWholeNode`. Closing the database manager wipes every database
  barrier first, so no shutdown, failed boot or teardown leaves a database
  key in memory. A tenant's own seal or unseal is ordered
  against the node seal, so neither can land between the node seal's check
  of a tenant and its seal of it. A cut-off still keeps every key. New:
  `service.WithBarrierRegistry`, `service.SealWholeNode`,
  `seal.BarrierRegistry.SealTenants`, `database.Manager.SealBarrierDatabases`,
  `DatabaseBarrierManager.SealAll`,
  `SetUnsealGate`, `PendingUnseals` and `SubmitHolderShare`.
- **Database barrier operations are owner-only, the system tenant included.**
  `databases:seal`, `databases:unseal` and `databases:barrier_status` are now
  `ScopeSelfStrict`, and `DatabaseService` resolves the database through the
  caller's own tenant; another tenant's database answers as missing. They were
  `ScopeSelf`, which lets a system-tenant identity act on any tenant's
  database barrier.
- **Tenant barrier seal and unseal refuse the system tenant** in
  `TenantService` as well as in RBAC.
- **A share-holder seal quorum seals cluster-wide, recording first.** Once the
  cluster has activated cluster-seal, a completed seal quorum of a Shamir
  node, tenant or database barrier writes the barrier's cluster seal record
  before any key is wiped, then every node seals it -- the node barrier's
  as the whole node. A record that definitely was not written leaves every
  key in place and returns the sealer's typed error
  (`*clusterseal.SealEpochConflictError`); one whose write cannot be
  confirmed seals this node anyway (`*clusterseal.RecordNotConfirmedError`),
  as a single caller's seal does. A tenant or database unseal quorum clears
  the record, sealing again if the clear cannot be written, and a tenant's
  unseal quorum is ordered against the node seal. Before activation a node
  seal quorum seals this node whole. New: `seal.StorageBarrier.SubmitHolderShareWith`,
  `seal.BarrierRegistry.SubmitTenantHolderShare`,
  `database.DatabaseBarrierManager.SealAuthorized` and `RequiresQuorum`.
- **The cluster enable-encryption record carries no credential.** The
  replicated `__sys:encmig/` intent no longer holds per-node HPKE envelopes of
  the operator's passphrase. The node that accepted the request recycles with
  the request's passphrase held in protected memory; every other owner reads
  its own from `<seal.migration_credentials_dir>/system` or `tenant-<id>`
  (regular file, mode 0600 or stricter, owned by the daemon's user) and waits
  in phase `awaiting-credential` -- not a failed attempt -- until it is there.
  The directory is opened once (`O_DIRECTORY|O_NOFOLLOW`), vetted on that
  descriptor, and the file opened relative to it, so a directory swapped in
  after the check is never read.
  A Shamir target needs none. Breaking for Go callers:
  `clustermig.ClusterMigration.Credentials`, `CredentialSealer` and
  `CredentialOpener` are gone; `NewCoordinator` and `Coordinator.Request` drop
  their credential arguments; `NewReconciler` takes a
  `clustermig.CredentialSource` (`clustermig.LocalCredentials`). New
  configuration: `seal.migration_credentials_dir`.
- **`keydelivery.WithoutReplayGuard` is removed.** Its only user was the
  migration credential receiver, which no longer exists; every receiver now
  keeps its replay guard.

### Added

- **Cluster sealing.** Once every node runs a release with the `cluster-seal`
  feature and the cluster has activated it, a seal of the node, tenant or
  database barrier reaches every node: the node that receives it writes a
  replicated seal record, and every node seals the barrier with its own key --
  gate closed, open watch streams ended, the tenant's shards stopped, the key
  wiped. A node-barrier seal waits up to `seal.cluster_ack_timeout` for every
  member's acknowledgement before wiping keys. The record carries no key
  material. Each node unseals with its own credential; `qrdb barrier
  cluster-unseal` (`POST /v1/barrier/cluster-unseal`, `BarrierClusterUnseal`,
  `barrier.ClusterUnseal`, `qrdb_barrier_cluster_unseal`, SDK
  `BarrierClusterUnseal`) clears the node record once the node is unsealed,
  and a tenant or database unseal clears its own record. A node that cannot
  read the records for `seal.cluster_lease` (15 s) stops serving, keeps its
  keys, and resumes on the next read; a node restarted under a sealed node
  record starts held. Every barrier status -- node, tenant and database, on
  every transport and in the CLI -- carries a `cluster_seal` object; before
  activation it reports `active: false` and seals stay node-local. New
  configuration: `seal.cluster_lease`, `seal.cluster_poll_interval`,
  `seal.cluster_ack_timeout`.

### Fixed

- **A node no longer exits at boot when the system shard loses its leader
  between provisioning and the feature gate's first read.** The read now waits
  out an election (`service.NotLeaderError` with no known leader) as it already
  waited out a new leader's ReadIndex warm-up, up to the same 15 s. A rolling
  enable-encryption, which restarts every node in turn, hit it.
- **The in-process embedded client no longer serves a sealed node.** The
  network listeners refused every request outside the sealed-window allowlist
  while the node barrier was sealed, but `sdk/go/node`'s embedded client kept
  serving through it. It now gates on the same probe and allowlist
  (`embedded.WithSealProbe`, `server.NodeSealProbe`,
  `sealedwindow.EmbeddedAllowlist`), including while a cluster seal holds the
  node, cuts it off or is not yet confirmed. The gate covers every method of
  every sub-client the transport hands out (audit, factor, login, RBAC,
  session, permission, volume pool) as well, and an unscoped embedded watch
  subscribes to the node's cluster-seal stream registry
  (`embedded.WithSealStreams`), so a seal ends a watch opened before it, with
  the seal's refusal as the terminal frame. The stream guard is shared with the
  server as `watch.Guarded` and `clusterseal.Streams.GuardWatcher`.
- **A stream opened as a seal lands no longer outlives it.** Every watch and
  etcd stream checked its refusal, opened, and only then subscribed to the seal
  stream registry, so a seal or cut-off that landed in between ended nothing
  and the stream lived on (events kept flowing after a cut-off). A stream now
  subscribes first and asks its refusal after
  (`clusterseal.Streams.SubscribeServing`; `GuardWatcher` now takes the
  refusal and an open function), on every transport: REST, gRPC, unix, QUIC,
  MCP, embedded and etcd. A missed seal ends streams again after its wipe.
- **A seal closes the SQL sessions it reaches.** A MySQL server-side cursor
  opened before a seal kept serving rows -- vitess answers `COM_STMT_FETCH`
  without consulting the statement gate -- and a transaction held across a seal
  stayed open. Both SQL listeners now close a session's connection when the
  node's seal probe closes or a cluster seal of the node, the session's tenant
  or any of its databases, or a cut-off, reaches it
  (`clusterseal.Streams.SubscribeTenantServing`, `mysql.WithSealStreams`,
  `pgsql.WithSealStreams`).
- **BREAKING: `sql.query_timeout` is enforced on the MySQL wire, and the
  connection watcher is off.** The setting (default `30s`) was documented but
  enforced nowhere, so it now takes effect with that default: a statement that
  ran longer -- long DDL, a large backfill -- fails where it used to succeed;
  raise `sql.query_timeout`, or set it to `0`, before upgrading if you run
  such statements. Every MySQL statement -- and a server-side cursor while it
  produces rows -- is now cancelled at it with `ER_QUERY_TIMEOUT` (3024)
  (`mysql.WithQueryTimeout`, `config.SQLConfig.QueryTimeoutDuration`;
  `0` disables it). go-mysql-server's connection watcher is disabled: it read
  the socket concurrently with vitess while a cursor was open, a data race. A
  client that disconnects therefore no longer cancels its statement before the
  timeout.
- **BREAKING: `sql.query_timeout` is enforced on the PostgreSQL wire too.** Every simple
  query, extended-protocol statement executed at Parse, and Bind/Execute of a
  parameterized statement's portal is cancelled at the timeout with SQLSTATE
  `57014` (`query_canceled`), as PostgreSQL's `statement_timeout` is
  (`pgsql.WithQueryTimeout`). `pg_sleep(seconds)` is now served.
- **A seal reaches SQL statements prepared before it.** The PostgreSQL wire
  gated Parse only, so Bind/Execute of a statement prepared while the node
  served still ran its query (or wrote rows read before the seal); it is now
  refused with SQLSTATE `57P03`. MySQL `COM_INIT_DB` (`USE`) is refused while
  sealed as well, for any name, rather than reporting whether the database
  exists.
- **A cluster-sealed tenant's shards do not start before the node has read its
  record.** `qrdbd` and `sdk/go/node` started the tenant shard reconciler
  before installing the seal monitor, when the sealer it asks refuses nothing,
  and `BuildTenantInfra` eagerly started every auto-unseal tenant at boot. The
  reconciler now starts after the monitor is installed and also holds a shard
  whose database record is sealed, and a node that installs a monitor starts no
  tenant runtime at boot: the reconciler and first access start it once the
  record allows.
- **A clustered node does not serve before it knows whether cluster sealing is
  active.** Until its feature gate has read the cluster's activations
  (`featuregate.Gate.ActivationsLoaded`), the node refuses as it does while
  unconfirmed. `sdk/go/node`, whose gate starts in the background, served in
  that window before; it now reads the activations before it installs the
  seal monitor and serves, as `qrdbd` does, and a cluster that has activated
  a feature the binary lacks fails its boot. New:
  `server.WhileSystemShardSettling`, the settling wait both share.
- **Cluster-seal activation opens no refusal window.** The seal monitor reads
  the records, and renews its lease, before the feature is activated too,
  enforcing nothing; when the feature turns on the lease is already fresh, so
  a cluster with no seal record never refuses requests, or closes SQL
  connections, for the activation.

### Changed

- **Breaking:** `service.BarrierServicer` gains
  `BarrierClusterUnseal(ctx) (*clusterseal.Report, error)`; every implementation
  must add it.
- **Breaking:** `service.DatabaseServicer.DatabaseBarrierStatus` returns
  `*service.DatabaseBarrierStatusInfo` (the `*seal.BarrierStatus` embedded, plus
  `ClusterSeal`) instead of `*seal.BarrierStatus`; every implementation and
  caller must change.
- **Breaking:** `clusterseal.ActivationReader` (an interface introduced in this
  release) requires `ActivationsLoaded() bool`; `*featuregate.Gate` provides it.
- **Breaking: the system tenant no longer seals, unseals or reads another
  tenant's database barrier.** `databases:seal`, `databases:unseal` and
  `databases:barrier_status` are `ScopeSelfStrict`, and `DatabaseService`
  answers another tenant's database as missing (`*database.NotFoundError`) for
  every caller, the system tenant included. A platform operator who drove a
  tenant's database barrier must now do it as that tenant. The system tenant
  keeps full control of its own databases.
- **Upgrade: clear old enable-encryption records first.** The replicated
  `__sys:encmig/` records written by earlier releases carry HPKE-sealed
  per-node credentials; this release neither reads nor removes them, and
  carries no code to. Let every in-flight cluster enable-encryption finish, or
  delete its record, before upgrading: earlier releases are obsolete, and a
  record of theirs left behind is not migrated.
- **Docs: the cluster sealing contract.** `docs/barrier-seal/` now specifies
  how a seal reaches every node of a cluster: a replicated seal record per node,
  tenant or database barrier that carries no key material, enforcement by every
  node on its own key (gate closed, streams ended, shards stopped, key wiped),
  per-node unseal with an authorized cluster-unseal record, a serving lease
  (about 15 s) that stops a cut-off node from serving while it keeps its keys,
  seal authority that never crosses tenants, and a `cluster-seal` feature behind
  the cluster feature gate. The enable-encryption migration record stops
  carrying credentials: each node supplies its own.
- **Docs corrections.** The `ShareExchangeCoordinator` was described as cluster
  auto-unseal; nothing constructs it. Tenant raft replicas were described as
  holding independent keys; they share the tenant root key, and only the
  wrapping key is per node. A tenant rekey was described as available; no
  transport exposes one. The user guide said nodes exchange shares between
  themselves; none does. A database seal was correctly described as per node,
  and now states when that stops being true.

### Known issues

- **MySQL server-side cursors** (builds with `qrdb_mysql` /
  `WITH_QRDB_MYSQL` only). A cursor -- `COM_STMT_EXECUTE` with a cursor flag,
  then `COM_STMT_FETCH` -- is served by the upstream vitess and
  go-mysql-server stack, which has a known data race on vitess
  `Conn.StatusFlags` between go-mysql-server's handler and the connection
  goroutine, and an idle open cursor is not bounded by `sql.query_timeout`.
  Clients that need bounded resource use should not request cursors: keep
  `useCursorFetch=false` with MySQL Connector/J, and do not set
  `CURSOR_TYPE_READ_ONLY` with libmysqlclient. See
  `docs/sql/upstream-gaps.md`.

## [0.34.2-alpha] - 2026-10-03

The database wizard offers Shamir custody. Highlights:

- **The desktop and web database wizard offers Shamir M-of-N.** It creates the database sealed, enrols exactly N share holders through the unified holder model, then initializes. The page administers holders only: claims and share submissions stay in the CLI and desktop Go, and no share or key ever enters the page.
- **"Finish later" resumes from the database view.** Enrolment and initialization continue where they stopped.

### Changed

- **The database wizard offers Shamir and enrols the database's share
  holders.** The web UI's and the desktop app's database wizard creates a
  Shamir database sealed (`barrier: {enabled: true, mode: "shamir",
  sharesRequired, sharesTotal}`, no secret), then a holders step enrols exactly
  N holders through the unified holder model (`registerDatabaseOfficer`, each
  from the holder's own CSR) and initializes custody
  (`initializeDatabaseShamir`). The wizard administers holders only: claims
  and quorum submissions stay with the holders' own keys in the qrdb CLI or
  the desktop app, and no share, envelope or holder key enters the page. The
  wizard no longer withholds Shamir itself -- the client-side
  `unavailableSealModes` map and `SealModeUnavailableError` are removed -- and
  refuses it only on the server's answer, such as
  `ShamirCustodyNotConfiguredError` from a node with no share-holder custody
  wired, which leaves no database behind. A database left sealed by the
  wizard's "Finish later" resumes its enrolment in the database view
  (`DatabaseHoldersPanel`): the remaining holders' forms and Initialize,
  enabled once exactly N are enrolled.

## [0.34.1-alpha] - 2026-10-03

Sealed-node hardening. Highlights:

- **Registering a tenant volume fails closed while the system barrier is sealed.** Before, a direct registry call skipped its claimed-root overlap guard.
- **`Node.Close` waits for the background sweepers to exit** before stopping the services they call into.

### Security

- **`BarrierRegistry.RegisterVolumeTenant`/`ReattachVolumeTenant` refuse
  with `seal.ErrSealed` while the system barrier is sealed.** Called
  directly on the registry (bypassing `TenantService.CreateTenant`, whose
  own tombstone check already fails closed through the sealed barrier),
  they previously proceeded to pick a pool slot and open a Pebble store:
  `refuseClaimedRoot`'s DataRoot-overlap check can't read the tenant-root
  records it compares against while sealed, so it treated "sealed" the
  same as "nothing is claimed" and returned nil instead of refusing. The
  check runs after the existing wiring preconditions (empty tenant ID, no
  volume pool, no per-tenant registry configured), so a misconfigured but
  unsealed call still reports its own error.

### Fixed

- **`Node.Close` now waits for the lease-expiry sweeper, the reclaim sweeper
  and (when tenant isolation wired one) the tenant shard reconciler to
  actually exit before stopping the services they call into.** It already
  cancelled the shared background context first; cancelling alone does not
  block until a goroutine's current tick notices, so `svcNode.Stop` could
  race one of them calling back into the very service graph it was tearing
  down. Matches the wait the wire-failure path in `completeClusteredBoot`
  already had.

## [0.34.0-alpha] - 2026-10-03

Unified share holders. This release contains BREAKING changes; see the entries below. Highlights:

- **One share-holder model.** Security Officers and key custodians are one holder type with a role. The parallel custodian subsystem, raw-share storage (`ceremony.ShareService`) and every unauthenticated or batch share entry point are deleted.
- **Shares reach only their holder.** Each share is HPKE-sealed to the holder's enrolled key. The suites are algorithm-agile; X25519 is refused in FIPS mode. A share that is never claimed is destroyed.
- **One authenticated quorum.** Unseal, seal, root token, rekey and holder revocation all go through one quorum. Each holder is authenticated by any auth factor and counted once.
- **Quorum-gated rekey and holder revocation**, plus an independent recovery key.
- **Shamir custody for database barriers**, with holder management in the CLI, the desktop app and the web UI.
- **SO PIN hardening.** One lockout across every SO PIN check; hashed, constant-time checks that fail closed; no anonymous PIN oracle.
- **Strict ownership.** The system tenant no longer reaches other tenants' databases on any transport, etcd included.

### Added

- **`pin.WithLockoutClock`** sets the clock the SO PIN lockout's backoffs,
  aggregate window and stale-counter eviction run on.
  `pin.NewLockoutBackend` and `lifecycle.NewSealStorePINBackend` take it as a
  variadic `pin.LockoutOption`, so every existing call is unchanged. It
  configures only the first decoration of a backend, the one every consumer
  shares.

- **`transport.RequestError.RetryAfter`, re-exported as `sdk.RequestError`.**
  A refusal that names how long to wait before a retry can succeed -- a
  throttled quorum request or sign-csr, an SO PIN lockout, a rate-limited
  caller -- now gives that delay to the SDK caller on every transport. REST
  reads `Retry-After`, gRPC and the Unix socket read the `google.rpc.RetryInfo`
  detail, QUIC reads the error frame's `RetryInfo`, and MCP reads
  `retry_after_seconds`. The embedded transport wraps such a refusal in a
  `RequestError` carrying its own delay; it still unwraps to the refusal's
  type. A failure that names no delay has `RetryAfter` zero.

- **Shamir custody of a database barrier.** A database on a dedicated store
  can be created with `barrier: {enabled: true, mode: "shamir",
  sharesRequired: M, sharesTotal: N}` and no secret; it is created sealed and
  its key is split M-of-N to share holders enrolled for that database alone,
  on the one share-holder model (scope (tenant, database), unseal domain
  only). Enrolment, listing, initialization (a factor policy; the M-of-N is
  the create's), claim, quorum seal/unseal, rekey and revocation are Tier-1 on
  all six transports: REST `/v1/databases/{name}/barrier/...`, gRPC/Unix
  `DatabaseBarrier*`, QUIC `db.Barrier*`, MCP `qrdb_database_barrier_*` and
  the embedded SDK, with the CLI verbs `qrdb database barrier-officer`,
  `barrier-init-shamir`, `barrier-quorum` and `barrier-rekey`, the desktop
  `OfficerBinding` (`HolderScope.Database`, `InitializeDatabaseShamir`) and
  the TypeScript SDK's `OfficerClient`. New self-scoped permissions
  `databases:officer_register`, `officer_list`, `officer_revoke`, `rekey` and
  `initialize_shamir`. The web UI's database wizard offers Shamir beside the
  passphrase and enrols the holders and initializes after the create; the
  database view lists the holders and opens rekey and revocation rounds.
  Per-database barriers now live in a barrier registry of their own, so a
  database name can no longer collide with a tenant id. Refusals classify as
  `ShamirSecretNotAllowedError` (400), `NotShamirBarrierError` and
  `ShamirCustodyNotConfiguredError` (409).
- **The audit log's tamper-evidence hash chain can now be verified.**
  `pkg/audit.Verify` is the single canonical entry point (a `Verifier`
  interface `*ChainFileLogger` implements and `aggregateLogger` delegates to),
  exposed as `qrdb audit verify` and as a Tier-1 capability on all six
  transports: REST `GET /v1/audit/verify`, gRPC/Unix `AuditService.VerifyChain`,
  QUIC `audit.VerifyChain`, the MCP tool `qrdb_audit_verify`, and the embedded
  SDK's `AuditClient.VerifyChain`, gated by the new `audit:verify` permission
  (granted to the Security Officer and Auditor roles). A tamper verdict
  (`Verified: false`) is reported as a normal result, never a transport error;
  an error return means the check could not be PERFORMED at all (unsupported
  backend, missing file, or a rotation race) (#453). Those errors classify the
  same on every transport, from the one table in `pkg/api/errclass`
  (unsupported → `Unimplemented`, busy/rotation/locked → `Unavailable`,
  cancellation and deadline as themselves), and the QUIC and MCP
  SDK clients report an unsupported backend as `transport.NotImplementedError`
  like the others. At most one verification scan runs at a time; concurrent
  callers are refused with `VerificationBusyError`.
- **The embedded-server wizard enrols share holders before a Shamir
  initialization.** Its Shamir mode called `initializeShamir` with no holder
  enrolled, which the server refuses. It now starts the embedded server,
  pauses for the operator to enrol exactly the configured number of holders
  (username, role, CSR; the issued certificate is shown to hand back), and
  only then initializes. The enrolment forms are one component shared with
  the setup wizard.
- **The mocked Playwright tier joins the TypeScript CI job.** The
  `test-typescript` job (`make test-typescript`) runs the mocked Playwright
  tier after the TypeScript SDK's type check, tests and build and
  svelte-check over the web UI. The Playwright specs
  that encoded the retired custody model are ported to share holders; the
  real-backend tier claims and submits with a headless `qrdb` CLI
  (`QRDB_BIN`, built by `make build-cli-headless`).
- **Re-initializing a barrier to Shamir in the Seal Manager enrols its
  holders first,** as the setup wizards do.
- **Tenant share holders in the web UI.** The Tenant Manager gains a
  Holders panel per tenant: it lists the Shamir tenant barrier's holders with
  role, share index and claim state, and opens rekey and revocation rounds
  (`startTenantRekey`, `revokeTenantOfficer`). The rekey/revoke form is one
  component shared with the Seal Manager's system-barrier panel.
- **Unified share holders.** Security Officers and key custodians are one
  kind of share holder, distinguished only by role (`so` | `custodian`) and
  the RBAC rights that role carries. Both are enrolled the same way — by an
  Admin or a Security Officer, never by themselves — from a CSR whose public
  key is all the CA takes: the ceremony CA assigns `CN=<username>`,
  `OU=[<scope tenant>, <profile>]` and no SAN, and the holder certificate
  authenticates with no roles (v0.28.1's officer-certificate contract, see
  Security). Enrolment records an
  envelope recipient key (the certificate key, or a separate HPKE key) and
  the holder's authentication factors: `mtls` always, plus any factor the
  composer registered with `WithFactorRegistry`. One person holds one share,
  in one role, per scope. See `docs/shamir-custody/`.
- **Algorithm-agile HPKE (`pkg/keydelivery`).** A fixed suite registry —
  `dhkem-p256`, `dhkem-p384`, `dhkem-x25519` and the post-quantum hybrid
  `mlkem768-x25519` — shared by node key delivery and share envelopes.
- **The `mtls` authentication factor (`pkg/authfactors/mtls`).** Possession
  of the enrolled certificate key, proved by signing a server nonce, so it
  works on every transport; when the caller arrived over a verified TLS
  session, the session certificate must also be the enrolled one.
- **The holder roster.** Each key domain's wrapped-key blob (now version 2)
  carries the public list of its holders, their share indexes and their
  factors' public material, with the roster's SHA-256 digest in the AEAD
  associated data: readable while sealed, and any edit makes the key fail to
  open (`ErrUnsealKeyMismatch`). A factor policy — `designated` (default) or
  `any-enrolled` — is chosen per share set and bound into the roster.
- **One authenticated quorum.** Unseal, seal, root token, rekey completion
  and recovery all run through `StorageBarrier.SubmitHolderShare`: a share
  counts only for a holder in the roster, not being revoked, not already
  counted in the round, whose factor proof verifies against the roster over
  a challenge issued for exactly that round, and whose share carries its own
  index. Served on every transport as `quorum begin` / `quorum submit`
  (REST `POST /v1/barrier/quorum/{begin,submit}`, gRPC/Unix
  `BarrierQuorumBegin`/`BarrierQuorumSubmit`, QUIC `barrier.QuorumBegin`/
  `barrier.QuorumSubmit`, MCP `qrdb_barrier_quorum_begin`/`_submit`,
  embedded), reachable through the sealed window, with tenant equivalents.
- **Quorum-gated rekey and holder revocation.** `BarrierRekeyStart` /
  `BarrierOfficerRevoke` (REST `POST /v1/barrier/rekey/start`,
  `POST /v1/barrier/officers/revoke`; permissions `seal:rekey` and the new
  `seal:officer_revoke`, plus the SO PIN) open a round that completes only
  when M current holders submit; revocation removes the holder, is completed
  by M of the remaining holders, and is refused when M would exceed N−1.
  Tenant forms: `tenant_barrier:rekey`, `tenant_barrier:officer_revoke`.
- **An independent recovery key.** Recovery generation wraps the root key
  under its own key with its own blob and roster, split to the recovery
  holders; it unseals with `op: recover` and is rekeyed on its own.
- **Holder CLI and desktop verbs.** `qrdb barrier officer
  register|list|claim|claim-begin|claim-complete|revoke`, `qrdb barrier quorum
  begin|submit`, `qrdb barrier rekey`, their `qrdb tenant barrier-officer`,
  `barrier-quorum`, `barrier-rekey` forms, and `qrdb init --custodians
  --policy`. The desktop `OfficerBinding` offers `RegisterHolder`,
  `ListHolders`, `ClaimEnvelope`, `SubmitShare`, `StartRekey`,
  `RevokeHolder`; keys stay in the calling process.
- **Share-holder errors in the one classification table.**
  `pkg/api/errclass` classifies every holder, quorum and envelope error, so
  the same failure answers the same way on REST, gRPC, Unix, QUIC, MCP and
  embedded.
- **Test support:** `pkg/seal/sealtest.Custody` (in-memory distributor and
  verifier for code that sits on a Shamir barrier) and `pkg/pin/pintest`.
- **The retry delay of a throttled quorum request or sign-csr, and of an SO
  PIN lockout, reaches gRPC, Unix-socket and MCP callers.** It was sent only
  as REST's `Retry-After`. gRPC and the Unix socket now attach a
  `google.rpc.RetryInfo` status detail with the exact delay, and MCP sets
  `retry_after_seconds` in the error envelope. One rule
  (`errclass.RetryAfter`) decides which refusals carry a delay on every
  transport. QUIC carries it too, in the error frame's payload (below).
- **QUIC error frames carry the retry delay.** The body of every non-OK
  QUIC response frame is now a `google.rpc.Status` (`wire.ErrorPayload`,
  `wire.EncodeErrorPayload` / `wire.DecodeErrorPayload`): the message, and a
  `google.rpc.RetryInfo` detail carrying the exact delay when the refusal
  names one -- what a gRPC caller receives for the same refusal. The Go SDK
  decodes it in the same release; a body that does not decode is reported as
  `*wire.PayloadDecodeError` on the `RequestError`, never shown as the
  server's message.
- **Auth-layer refusals carry their retry delay on every transport.** A
  rate-limited caller (`middleware.RateLimitedError`) and a locked-out tenant
  SO PIN (`auth.SOPINLockedError`, from the SO gate) now carry the delay as
  gRPC / Unix `RetryInfo`, REST `Retry-After` (the SO middleware included),
  MCP `retry_after_seconds` and in the QUIC error frame; previously only the
  REST authorization path sent it. `middleware.RetryAfter` names these
  refusals and `errclass.RetryAfter` consults it.

### Security

- **Every share-holder scope signs through one checked path.** Holder
  enrolment in every scope -- system, recovery, tenant and the database scope
  this release adds -- the initial ceremony and `ceremony sign-csr` sign
  through `signHolderCSR` with `SignCSROptions` naming the registered holder
  and the scope's tenant, and every certificate a CA returns is checked the
  same way: `CN=<username>`, `OU=[<tenant>, <profile>]` in separate RDNs and
  no Subject Alternative Name (`*CertSubjectError`), not a CA, no
  certificate-signing key usage, exactly the requested key usage and the
  requested extended key usages as a set, duplicates refused
  (`*IssuedCertCapabilityError`). Holder certificates are recognised only by
  their profile OU (`pkg/holderprofile`); they authenticate with no roles and
  RBAC refuses them every permission.

- **A system-tenant identity could act on every tenant's databases.** The
  database service lifted the system tenant past the ownership check on every
  per-database verb (get, list, drop, every update, seal, unseal, barrier
  status and the share-holder verbs), a system-tenant create honoured a
  tenant named in the request (the QUIC create passed it through), the
  embedded transport's get, list, drop and update verbs called the
  node-wide service methods for any caller, the SQL `DROP DATABASE` manager
  path dropped any database by name for a system session, and a database's
  etcd listener admitted the system tenant. There is no cross-tenant database
  access: every caller, the system tenant's Admin, Security Officer and
  Auditor included, is confined to its own tenant's databases by one
  ownership check (`DatabaseService.ownedDatabase`); another tenant's
  database answers `NotFound`, exactly as a missing one does, on all six
  transports.
- **The SO PIN lockout could be bypassed by spending guesses on any SO PIN
  check but sign-csr, rekey and revocation.** Present in v0.25.2-alpha. The
  lockout was a counter inside the ceremony service, so only the ceremony's
  own checks counted. The transports compared the SO PIN for seal,
  recovery-key generation and both initialize paths straight against the PIN
  backend, uncounted and unlimited, and sign-csr, seal and initialization are
  reachable anonymously in the sealed window. The lockout now lives in the PIN
  backend: `pin.NewLockoutBackend` decorates a `pin.PINBackend` and counts
  every failed SO PIN comparison (`VerifySOPIN`, `SetSOPIN` with a current
  PIN, `ChangeSOPIN`, `SetUserPIN`, `ResetLockout`). It is installed once,
  where the node's backend is chosen (`lifecycle.NewSealStorePINBackend`
  returns it decorated; `server.WithPINBackend` decorates an injected backend
  when the option is built; both are idempotent), so the ceremony and every
  transport share one counter: 5 free failures, then a backoff doubling from
  1 s to 15 min during which every SO PIN check, the right PIN included, is
  refused with `pin.SOPINLockedError`. The ceremony's own counter is deleted.
  `ResetLockout` now verifies the SO PIN itself, because the software
  backend's no-op answered nil for any PIN.
- **Anonymous SO PIN guesses could lock the Security Officer out, and the
  tenant SO PIN had no lockout.** The SO PIN lockout now counts each caller
  separately (`auth.CallerPrincipal`): the anonymous sealed-window caller,
  and each authenticated identity or verified client certificate (by its
  leaf digest), under an aggregate cap of 64 authenticated failures per
  15-minute window and at most 4096 tracked principals per key. Neither
  anonymous wrong PINs nor a tenant user's or share holder's certificate can
  lock out the officer's rekey or revocation; several principals guessing
  together can engage the cap for one window, but never against a known-good
  principal -- one that has presented the correct SO PIN before, persisted
  (`auth.SOPINKnownGoodStore`; the software PIN backend and the tenant SO
  authenticator implement it) so the exemption survives a restart. The
  record holds unkeyed, domain-separated SHA-256 digests of key and
  principal, so it does not store principal ids (a reader can still confirm a
  guessed id); the store is called under no lock, each call bounded by
  `auth.SOPINKnownGoodTimeout` (5 s), so a slow Raft-backed store delays only
  the check that asked, never another tenant's; a save merges with the stored
  set, so nodes sharing a record keep each other's entries; and a failed load
  is retried no sooner than `auth.SOPINKnownGoodRetry` (30 s). Each key has
  its own lock, held across its PIN verification, so one tenant's SO PIN
  check (a KDF) no longer serializes every other tenant's. A holder
  certificate's principal is `holder:<tenant>/<subject>`, so a holder and the
  user of the same name share neither a counter nor known-good status. Stale
  per-principal counters are evicted before new principals fall back to the
  overflow counter, and `pin.NewLockoutBackend` returns one decoration per raw
  backend, so consumers of one backend share one budget. The backend is
  recognised by pointer identity; a backend that is not a pointer is refused
  -- its SO PIN checks fail closed with `pin.LockoutBackendKindError` --
  instead of getting a fresh budget per decoration or, for a struct holding
  an unhashable field, panicking. The counting is one implementation, `auth.SOPINLockout`, behind
  both the node SO PIN (`pin.NewLockoutBackend`) and each tenant's SO PIN
  (`auth.SOAuthenticator.VerifySOPIN`, previously unlimited); a locked
  tenant PIN answers 429 with its retry delay. Every transport's
  `WithPINBackend` (REST, gRPC, Unix, QUIC, MCP, embedded), gRPC's
  `WithRegisterPINBackend` and `ceremony.CeremonyService.SetPINBackend` now
  install the lockout too (idempotently), so a raw backend handed straight to
  a transport is still throttled.
- **Initializing an initialized barrier was an anonymous SO PIN oracle.**
  Present in v0.25.2-alpha. Both initialize paths verified a supplied SO PIN
  before the barrier refused the call as already initialized, so a right PIN
  and a wrong one got different answers on a barrier that could never be
  initialized again. Every transport now calls
  `service.ProvisionInitializePINs`, which answers
  `seal.ErrAlreadyInitialized` before comparing any PIN; the calls no longer
  count against the lockout. A failure to read the initialization record is
  returned rather than treated as "not initialized".

- **A holder registered while sealed took over an enrolled holder at unseal.**
  Registering a share holder while the barrier was sealed and already
  initialized cached the record unchecked. A registry read that failed while
  sealed counted as a free name. The next unseal wrote the cached record over
  the persisted holder of the same name, so the registrant became that holder.
  This applied to the system, tenant and database scopes. Registration on a
  sealed, initialized barrier is now refused
  (`ceremony.ErrRegistrationWhileSealed`, `FAILED_PRECONDITION`), and a read
  failure other than "not found" is an error. The unseal reconciliation writes
  only holders storage does not have. A cached record with a different
  identity under a persisted name is evicted, the persisted holder kept, and
  the conflict audited on the barrier's audit log
  (`holder-record-conflict`); the reconciliation carries on rather than
  failing, so the conflict cannot wedge the scope's holder operations.
- **The quorum-challenge request and the share submission could be used to
  deny an unseal, and to enumerate holders.** The sealed window admits both
  anonymously. Naming real holders filled a scope's 256-challenge table, so
  no holder could get one; a name outside the roster was refused differently
  from a real one; and a submission said whether its holder was in the
  roster, being revoked or had already submitted before checking its proof.
  Now:
  - a holder keeps at most 2 challenges per operation, the oldest evicted
    first;
  - a challenge requested over a connection presenting the holder's own
    certificate can never be evicted by a request that does not present it;
    a request that finds every slot bound gets its challenge, kept apart
    from the holder's slots, rather than an error;
  - the table is capped at what the roster can use;
  - an unknown holder, or a factor the policy does not admit, gets a decoy
    challenge issued by the requested factor itself from a decoy enrollment
    it derives (`authfactors.DecoyEnroller`, implemented by mtls), from a
    seed persisted with the roster and the name, so -- like a real
    holder's -- it has the factor's shape and the same public material on
    every request and across restarts; every request derives one, holder or
    not, so both cost the same. The decoy is stored like a real challenge,
    so a submission against it runs the same verification and fails the
    same way;
  - `seal.HolderAuthenticationError` is opaque across the transport
    boundary: one fixed message, no holder name, and no unwrap to the
    verifier's reason, which goes only to the server log and audit;
  - a submission's proof is verified before anything about the holder or
    the round is checked, so an unverified submission answers
    `HolderAuthenticationError` whoever it names; only a verified one learns
    `HolderRevokedError` or `HolderAlreadySubmittedError`;
  - every request and every submission passes a per-caller throttle on the
    shared rate-limit engine (a bucket per kind): 2/s burst 10 per
    identity or TLS-verified certificate, 5/s burst 20 for the anonymous
    bucket, which every other caller shares (an unverified certificate is
    anyone's to mint, so it buys no bucket of its own; a flood of
    anonymous requests can starve another anonymous caller until the bucket
    refills, never a holder with a verified certificate). A
    refusal is `ceremony.QuorumRateLimitedError`, `RESOURCE_EXHAUSTED`, sent
    with `Retry-After` on REST.
- **A late share submission destroyed a newer quorum round.** A submission
  read the roster before taking the quorum lock. If a rekey replaced the
  roster in between and a round had opened on the new one, the late
  submission discarded that round as stale, with the shares in it, and was
  counted against the old roster. The roster is now re-read under the lock
  when the digests differ. A stale submission, or `StartRekey`, is refused
  with `seal.StaleRosterError` (`ABORTED`, retryable).
- **A failed redistribution destroyed the unclaimed shares in force, and a
  revoked holder could come back.** A distribution destroyed the previous
  envelopes before the blob for the new ones was persisted. A failed persist
  therefore left holders who had not yet claimed with no share for the blob
  still in force, or a share for a blob that was never stored. A revocation
  deleted the revoked holder's record only after persisting the blob, so a
  failed delete brought the holder back into the next distribution. Now:
  - envelopes are staged and swapped in only once the blob is durable;
  - redistributions of one domain are serialised on the barrier, so a second
    one can no longer discard the first's staged envelopes, and `Committed`
    refuses with `ceremony.StagedDistributionMissingError` (changing nothing)
    when no envelopes are staged for the distribution it is told is durable,
    instead of recording a share set no holder could claim;
  - a holder who took part in an earlier distribution but is absent from the
    roster in force is never dealt a share again;
  - the domain key is generated into protected memory, and
    `seal.CeremonyDistributor.Distribute` takes it as a `*secure.Buffer` it
    borrows rather than a plain `[]byte`.
- **An mtls quorum answer was a signature over the bare nonce.** Any other
  protocol that had a holder sign 32 server-chosen bytes with the same key
  produced a valid quorum answer, and one answer was not bound to the scope,
  key domain or operation it was issued for. The mtls answer is now a
  signature over a domain-separated message binding the challenge ID and the
  quorum purpose (`quorum:<claim label>:<domain>:<op>`), verified against the
  server-held challenge.
- **A share claim and an mtls factor answer were the same signature.** Both
  were the holder's certificate key over a bare server nonce, so a signature
  obtained for one verified as the other. A claim now signs the
  domain-separated share-claim message: `qrdb/share-claim/v1`, the barrier's
  claim label, the holder and the nonce. `ceremony.SignShareClaim` and
  `sdk.SignShareClaim` produce it, and the CLI's `claim-begin` prints the
  exact message for an external signer. A claim signed over the bare nonce is
  refused.
- **Rekey and holder-revocation SO PIN guesses were unlimited, and an
  unprovisioned SO PIN admitted any PIN.** Each transport checked the rekey
  start and revocation SO PIN itself, outside the ceremony's lockout, and
  `pin.RequireSOPIN` admitted the call when no SO PIN had been provisioned.
  The barrier service now verifies it (`BarrierRekeyStart` and
  `BarrierOfficerRevoke` take the PIN) through
  `ceremony.CeremonyService.AuthorizeSOPIN`. That counts against the node's
  one SO PIN lockout, and refuses with `pin.ErrPINNotSet` while no SO PIN is
  provisioned (`pin.RequireProvisionedSOPIN`).
- **A database named like a tenant reached that tenant's barrier.** The
  per-database barrier manager kept its barriers in the seal subsystem's
  tenant registry, keyed by database name, so sealing, unsealing or reading
  the barrier status of one's own database named `acme` acted on tenant
  `acme`'s barrier when the database had none of its own. Database barriers
  now live in a registry of the manager's own over the system barrier;
  `database.NewDatabaseBarrierManager` takes the system barrier and returns an
  error (**BREAKING** for direct callers).
- **Share submissions are authenticated and counted once per holder.** The
  unauthenticated string-share unseal, seal-vote, root-token and recovery
  entry points are gone; one person who has seen M shares can no longer act
  as M people.
- **Shares reach only their holder.** Each share is HPKE-sealed to its
  holder's enrolled key, held only in go-enclave protected memory with a
  time-to-live, delivered once to the holder's own mTLS session and wiped.
  No response other than the claim carries share material, and the claim
  carries it sealed.
- **The CA assigns holder identities.** A CSR can no longer choose its own
  subject or SANs.
- **SO PIN checks are hashed and constant-time, and fail closed.** Every SO
  PIN comparison goes through the `pin.PINBackend`; raw PINs are never
  compared with `!=`. Every SO-PIN-gated operation is refused without a PIN
  backend (REST 412, `FAILED_PRECONDITION` elsewhere). Seal, rekey start and
  holder revocation are among them. v0.24.0-alpha skipped the barrier SO PIN
  check on a node with no PIN backend. A backend with no SO PIN provisioned
  yet still admits the call. An embedded or `sdk/go/node` node has its
  seal-store PIN backend by default.
- **Transports wipe the share bytes they decode** from a quorum submission.
- **A Shamir barrier can no longer be initialized from a credential.**
  `Initialize`/`InitializeWithStrategy` on a Shamir barrier wrote an envelope
  with no wrapped key and no roster, which could never be unsealed once
  sealed; they now refuse with `seal.ErrShamirInitializeRequiresHolders`.
  That covers `bootstrap.InitializeTenantBarrier` on a pre-registered Shamir
  tenant barrier and `sdk/go/node`'s `Node.Unseal` on an uninitialized one,
  and `qrdb barrier init --barrier-strategy shamir` is refused locally.
- **X25519 envelope suites are refused in FIPS mode.** `dhkem-x25519` and
  `mlkem768-x25519` are not FIPS 140-3 approved; in FIPS mode
  `keydelivery.LookupSuite` refuses them with `SuiteNotApprovedError`, so
  such a holder is refused at enrolment rather than at sealing.
- **Membership changes need the quorum.** Adding, removing or re-policing
  holders is a rekey, and a rekey completes only with M current holders.
- **A system-scope encryption migration to Shamir no longer loses the share
  envelopes.** The in-process data-plane restart it triggers built a new
  ceremony service and discarded the envelopes the migration had just
  distributed, so the barrier came back sealed with no holder able to open it
  and every datum on the node was unreachable. The restart now carries the
  running ceremony service across (`ceremony.CeremonyService.Rebind` moves it
  to the rebuilt barrier), in the daemon and in `sdk/go/node`, so the holders
  claim from the sealed node and a quorum unseals it.
- **A Shamir initialization whose share distribution fails is rolled back.**
  The barrier used to stay initialized and unsealed with a root key no holder
  had a share of, and every retry was refused as already initialized.
  `InitializeShamir` now discards the key, seals, and deletes the persisted
  root key, wrapped key and metadata, so storage and status read as
  uninitialized. The epoch barrier cannot leave the initialized state within
  the process, so a retry needs a node restart; until then initialization
  answers the new `seal.ErrRestartRequired` (FailedPrecondition). A rollback
  that fails returns `seal.ShamirInitRollbackError` (Internal).
- **The REST sealed window no longer admits holder revocation.** The claim
  routes were allowlisted by an `/v1/barrier/officers/` prefix, which also let
  `POST /v1/barrier/officers/revoke` through while the barrier was sealed. The
  allowlist now names the claim routes as one-segment patterns
  (`/v1/barrier/officers/{username}/claim/begin` and `.../complete`, and the
  recovery forms), so a sealed node answers the revocation 503.
- **A distributed Shamir share is destroyed if it is not claimed in time.**
  Every envelope held for a share holder now carries its own expiry timer
  (`CeremonyConfig.ShareTTL`, default `ceremony.DefaultShareTTL` = 24h) and is
  wiped at its deadline. A late claim is refused with the new
  `ceremony.ErrShareExpired` (`sdk/go.ErrCeremonyShareExpired`) at claim
  begin and complete, in the Gone class (REST 410, gRPC/Unix
  `FAILED_PRECONDITION`); the remedy is a rekey. Replacing a held share now destroys the old enclave
  instead of dropping it for the collector.
- **The barrier root key is decrypted straight into protected memory.**
  Unwrapping the Shamir-wrapped root key (unseal, root-token verification)
  now opens the AEAD in place into a `pkg/secure` buffer and hands it to the
  recovery strategy without an intermediate heap slice; an AEAD that returns
  memory it allocated itself is refused with the new
  `seal.UnprotectedDecryptError`.

### Fixed

- **A completed database rekey was reported as failed when its M-of-N could
  not be recorded.** The quorum's last submission had already retired every
  old share when the catalog write failed, and the holder was told the
  submission failed. The completed outcome is now returned and the failure
  logged.
- **Turning an existing database's barrier on or off answered 500.** The
  refusal (`BarrierMigrationNotSupportedError`) is now FailedPrecondition
  (409) on every transport: it needs a data migration that is not performed.
- **The transactions docs said a branch could carry get and range ops.** The
  code has always refused them (`UnsupportedTransactionOpError`, 400 /
  `INVALID_ARGUMENT`) rather than risk a read defaulting to a write; the
  documents now say so, and their examples use put and delete.
- **Real-backend Playwright specs** that had drifted from the server: barrier
  secrets are sent as base64 (`[]byte` fields), a created user carries the
  email that is its login key, and the transaction spec pins the get-op
  refusal.
- A reopened keyed barrier (`ReopenKeyed`) kept neither the holder verifier
  nor the quorum TTL, so no holder could unseal it after an encryption
  migration.
- `sdk/go/node` did not hand the ceremony CA to the ceremony service, and the
  full boot discarded the sealed-window ceremony state, losing envelopes not
  yet claimed; the sealed window also ran without the PIN backend.
- The embedded transport's `CeremonyInitialize` dropped each holder's role,
  the factor policy and the returned holder certificates.
- `qrdb init --shares` was checked only for range; the key was silently split
  to however many holders were named. It must now equal the number of
  `--officers` plus `--custodians`.
- `seal.ErrShamirUnsealRequiresQuorum` fell through to REST 500 and gRPC
  `INTERNAL`; it now maps like `ErrShamirBarrierRequiresQuorum` (422,
  `FAILED_PRECONDITION`). Ceremony initialization errors (`ErrNoOfficers`,
  `ErrInvalidThreshold`, `ErrAlreadyInitialized`) are now classified instead
  of answering 500.
- The QUIC transport classifies claim errors like every other transport.
- The sealed-window allowlist named a `shamir-verify` verb no transport
  served.

### Removed — BREAKING

- **`ceremony.BarrierScope.TenantID()` is removed** (shipped in
  v0.28.1-alpha). Nothing in the module used it once every scope signed
  through `SignCSROptions`, which names the tenant explicitly; a caller
  derives a scope's tenant from the tenant it built the scope with
  (`ceremony.ScopeTenant(tenantID)`).
- **The parallel custodian subsystem is deleted**: `pkg/custodian`,
  `ceremony.CeremonyExtension`, `CeremonyEnrollExternalOfficer`, the
  custodian-group service on every transport, and the ceremony's own share
  claims. Custodians are share holders with `--role custodian`.
- **The empty Custodian RBAC role**: `role.RoleNameCustodian` and the SDK's
  `RoleNameCustodian` are removed, and `role.BuiltInRoles` returns five roles
  (SO, Admin, Operator, User, Auditor). The role granted nothing; a key
  custodian is a share holder, not an RBAC principal. A tenant seeded by an
  earlier build keeps its stored `custodian` role record, which grants nothing
  and is no longer a reserved system role name, so any user still assigned it
  keeps resolving; remove the assignment and the role at leisure.
- **Batch and unauthenticated share entry points**: `UnsealWithShare(s)`,
  `SubmitSealVote`, `GenerateRootToken`, `RecoverWithKeys`, the one-call
  `Rekey`, and the `ShareExchangeCoordinator` skeleton (never wired; nodes do
  not exchange shares).
- **REST routes**: `/v1/barrier/shamir/unseal-share`,
  `/v1/barrier/shamir/unseal-shares`, `/v1/barrier/shamir/seal-vote`,
  `/v1/barrier/rekey`, `/v1/barrier/recovery/recover`,
  `/v1/barrier/root-token`, `/v1/barrier/officers/claim-status`,
  `/v1/barrier/unseal-share`, `/v1/ceremony/claim-cert/{begin,complete}`,
  `/v1/ceremony/officer-share/{begin,complete}`,
  `/v1/ceremony/claim-share/{begin,complete}`,
  `/v1/ceremony/enroll-custodian`, `/v1/ceremony/revoke-custodian`,
  `/v1/init/enroll-external-officer`,
  `/v1/tenants/{id}/barrier/{unseal-share,seal-vote}`,
  `/v1/tenants/{id}/barrier/officers/claim-status`.
- **gRPC/Unix RPCs**: `BarrierShamirUnsealShare(s)`, `BarrierRekey`,
  `BarrierOfficerClaimStatus`, `BarrierShamirSealVote`,
  `BarrierRecoveryRecover`, `BarrierRootToken`, `CeremonyClaimCert*`,
  `CeremonyBeginClaimShare`, `CeremonyCompleteClaimShare`,
  `CeremonyClaimShare*`, `CeremonyEnrollExternalOfficer`,
  `CeremonySubmitUnsealShare`, and `TenantBarrierService` `UnsealShare`,
  `OfficerClaimStatus`, `ShamirSealVote`. QUIC methods and MCP tools of the
  same operations are removed with them.
- **Permissions**: `seal:recover_with_keys`, `seal:submit_seal_vote`,
  `barrier:unseal-share`, `ceremony:enroll_custodian`,
  `ceremony:revoke_custodian`, `ceremony:claim_cert`,
  `ceremony:claim_share_begin`, `ceremony:claim_share_complete`,
  `tenant_barrier:claim_share`, `tenant_barrier:seal_vote`. The Custodian
  role now grants nothing; it is kept so persisted role references resolve.
- **CLI verbs**: `barrier unseal-shares`, `barrier shamir-seal-vote`,
  `barrier root-token`, `barrier recovery recover`, `barrier officer
  claim-status`, `barrier recovery officer …`, `ceremony claim-cert …`,
  `ceremony claim-share …`, `ceremony claim-share-begin|complete`,
  `ceremony submit-unseal-share`, `tenant barrier-shamir-seal-vote`, and
  `tenant barrier-unseal --share`.
- **`transport.TenantBarrierUnsealRequest.Share`**: the credential unseal
  takes `Key` only.
- **The User-PIN Shamir split in `qrdb init --officers`**: the ceremony now
  splits only the barrier's unseal key, to the named holders.
- **Credential-mode Shamir and legacy direct mode**: a Shamir barrier always
  holds its key under unseal-key indirection with a roster. A barrier whose
  wrapped blob predates the roster answers `seal.ErrHolderRosterMissing`;
  there is no migration (pre-release).
- **Dead error sentinels** of the removed flows in `pkg/ceremony`,
  `pkg/seal` and their `sdk/go` re-exports.
- **`tenant.SystemTenantID`, `tenant.IsSystemTenant` and `tenant.Normalize`
  moved to the new leaf package `pkg/tenantid`** as `tenantid.System`,
  `tenantid.IsSystem` and `tenantid.Normalize`. `pkg/tenant` imports
  `pkg/ceremony` and `pkg/auth`, so those packages could not name the system
  tenant; the leaf package imports nothing from this module. The SDK aliases
  (`sdk/go.SystemTenantID`, `IsSystemTenant`, `NormalizeTenantID`) are
  unchanged.
- **`ceremony.ShareService` is deleted.** It stored raw Shamir share bytes in
  the replicated store (`SubmitShare`) and returned them (`ListShares`,
  `GetShare`), contradicting the rule that share material is never persisted.
  Removed with it: `ceremony.ShareServicer`, `ceremony.NewShareService`,
  `ceremony.ShareServiceError`, `lifecycle.Node.Share`,
  `lifecycle.ShareServiceError`, the embedded `WithShareService` option,
  `transport.ShareClient` and its request/response types (`SubmitShareRequest`,
  `SubmitShareResponse`, `ShareInfo`, `ListSharesResponse`,
  `ShareCollectionStatus`), the MCP stubs, and the SDK's `Client.Shares()`,
  `sdk/go.ShareServicer`, `sdk/go.ShareService`, `sdk/go.NewShareService` and
  `sdk/go.CeremonyShareServiceError` aliases. There is no replacement: shares
  leave the node only through the barrier's ceremony distributor and the
  one-time claim.

### Changed — BREAKING

- **RSA share-holder keys are refused at enrolment.** A holder's certificate
  key must be ECDSA P-256, P-384, P-521 or Ed25519; an RSA CSR is refused with
  `ceremony.UnsupportedHolderKeyError` (`INVALID_ARGUMENT`). No per-name decoy
  can be derived for an RSA key without revealing it, so an RSA holder was
  distinguishable from a stranger. Re-enrol any RSA holder with an EC or
  Ed25519 key before the next distribution.
- **`seal.HolderAuthenticationError` no longer unwraps or names its cause.**
  Its message is fixed and its `Err` is for the server's log and audit only;
  a caller that inspected the verifier's error through it must not.
- **The system tenant no longer reaches other tenants' databases.** A
  system-tenant caller's database verbs act only on databases the system
  tenant owns (`TenantID` empty or `system`); another tenant's database
  answers `NotFound`. `ListForTenant` for the system tenant lists only its
  own databases. A system-tenant create always creates a system-owned
  database, ignoring a `tenant_id` in the request. The embedded transport's
  `GetDatabase`, `ListDatabases`, `DropDatabase` and `UpdateDatabase*` now go
  through the tenant-scoped service methods. A database's etcd listener
  refuses every caller outside the owning tenant, the system tenant included.
  A platform operator who managed tenants' databases through the system
  tenant must act as the owning tenant.
- **An `mtls` factor answer signs a domain-separated message, not the bare
  nonce.** The answer is now a signature over `mtls.ChallengeMessage`:
  `qrdb/mtls-challenge/v1`, the challenge ID, the purpose the issuer bound
  the challenge to (`method_data.purpose`; for a quorum challenge
  `quorum:<claim label>:<domain>:<op>`) and the nonce, NUL-separated. Use
  `mtls.SignChallenge`; a client that signs outside qrdb must sign that
  message. A bare-nonce signature no longer verifies. The CLI's `quorum
  submit` signs it.
- **BREAKING: the QUIC listener accepts only the ALPN protocol `qrdb-quic/3`.**
  Under `qrdb-quic/3` the body of every error frame is a `google.rpc.Status`
  rather than bare text (see Added); the stream discipline is
  `qrdb-quic/2`'s. The listener no longer accepts `qrdb-quic/2`, and the Go
  SDK offers only `qrdb-quic/3` (`wire.ProtocolV3`, which replaces
  `wire.ProtocolV2`). Upgrade servers and clients together.
- **`errclass.RetryAfterSeconds` is now `middleware.RetryAfterSeconds`**, so
  the auth-layer renderers, which `errclass` imports, round the same way.
  The REST authorization path's `Retry-After` now rounds up rather than to
  the nearest second, so it is never early.
- **The QUIC table of gRPC codes moved into the wire package**
  (`wire.StatusForGRPCCode`, `wire.GRPCCodeForStatus`), beside the statuses
  it maps, so the server and the SDK frame error bodies the same way.
- **A watch request whose payload does not decode answers QUIC
  `INVALID_ARGUMENT`** rather than the generic `ERROR`, as every other
  undecodable request already did.
- **The ad-hoc SQL query service reads the catalog owner-checked.** Its
  engine and SQL-settings lookups used `DatabaseService.Get` by name, so a
  session's settings could come from another tenant's database of that name.
  They now use `GetForTenant`, except for the configured default SQL
  database, which every tenant addresses as its own namespace
  (`query.WithSharedDefaultDatabase`, wired from `sql.default_database`, the
  same exception the registry and the wire protocols make).
- **Database ownership is one function.** `database.OwnedBy(owner,
  tenantID)` (both normalized, so an untagged database is the system
  tenant's) and `database.Manager.GetOwned` replace the restatements in the
  database service, the service registry, the catalog, the dedicated-raft
  info source, the etcd listener and the SQL provider. The manager's
  `GetForTenant`, `ListForTenant`, `DropForTenant` and `Update*ForTenant`
  now hold the system tenant to the ownership rule instead of refusing it
  with `TenantIDRequiredError`, so the database service no longer forks
  around them. `NewRaftBackendFactoryFromManager` drops its `systemTenant`
  parameter, which only fed the restated check.
- **`pin.RequireSOPIN`, `pin.RequireProvisionedSOPIN` and `pin.Provision`
  take a `context.Context`** (the caller selects the lockout counter), as
  does `ceremony.CeremonyService.AuthorizeSOPIN`. The lockout constants are
  `auth.SOPINFreeFailures`, `auth.SOPINBaseBackoff` and `auth.SOPINMaxBackoff`.
- **`ceremony.SOPINLockedError` is now `pin.SOPINLockedError`.** The lockout
  moved from the ceremony service into the PIN backend
  (`pin.NewLockoutBackend`); the error moved with it and still unwraps to
  `pin.ErrPINLocked`. `lifecycle.NewSealStorePINBackend` returns a
  `pin.PINBackend` (the decorated backend) rather than a
  `*pin.SoftwareBackend`. A composer that hands a PIN backend to the ceremony
  without going through `lifecycle` or `server.WithPINBackend` must decorate
  it with `pin.NewLockoutBackend` itself.
- **`seal.HolderVerifier` gains `AdmitSubmission(ctx, domain) error`.** The
  barrier asks it before examining a share submission at all; pkg/ceremony
  throttles there. Implementations of the interface must add it.
- **`service.BarrierServicer` gains `BarrierInitialized(ctx) (bool, error)`.**
  The initialize paths use it to refuse an initialized barrier before any PIN
  is compared. Implementations of the interface must add it.
- **`service.BarrierServicer.BarrierRekeyStart` and `BarrierOfficerRevoke`
  take the SO PIN.** The service verifies it; transports no longer do.
- **Rekeying or revoking a holder of the system barrier requires a
  provisioned SO PIN.** Until now a node with a PIN backend but no SO PIN
  provisioned opened rekey and revocation rounds for any PIN, or none. They
  are now refused with `pin.ErrPINNotSet` (`412` / `FAILED_PRECONDITION`) on
  every transport until an SO PIN is provisioned, by the initialization
  ceremony or by barrier initialization with `so_pin`. An operator who rekeys
  or revokes on such a node must provision an SO PIN first and present it in
  `so_pin` (`--so-pin` on the CLI). Tenant and database barriers, whose rekey
  and revocation are RBAC-only, are unaffected.
- **`seal.CeremonyDistributor.Distribute` takes the domain key as a
  `*secure.Buffer`.** It used to take a plain `[]byte`. The distributor
  borrows the buffer (`WithBytes`) only to split it, and neither retains nor
  destroys it. It stages the sealed shares, and `Committed` for the same
  roster swaps them in. A custom distributor must split under the borrow and
  defer replacing its previous shares to `Committed`.
- **`ceremony.TenantCeremonyService` is `ceremony.ScopedCeremonyService`**
  (and `TenantCeremonyRegistry` is `ScopedCeremonyRegistry`): one holder
  authority type serves a tenant barrier (`NewTenantCeremonyService`) and a
  database barrier (`NewDatabaseCeremonyService`, scope
  `ceremony.ScopeDatabase(tenant, database)`).
- **Holder enrolment and claim wire types.** `OfficerRegisterRequest` gains
  `role`, `encryption_kem`, `encryption_key`, `factors` and `designated`; its
  response carries the CA-issued certificate, role, designated factor, KEM
  and enrolled factors, and no share index. `ShareClaimCompleteResponse`
  carries the sealed `envelope`, not share bytes.
- **`BarrierInitializeShamirRequest`** takes `threshold`, `total_shares`
  (which must equal the enrolled holder count) and `policy`; there is no
  secret. Recovery generation takes `threshold`, `policy` and the SO PIN and
  splits to every enrolled recovery holder.
- **A desktop or embedded holder can claim its envelope.** The embedded
  transport has no connection to present a certificate, so its claims were
  refused outright. An in-process claim (`ceremony.WithInProcessClaim`) is now
  bound to the holder's enrolled certificate and released only for a
  signature over the fresh claim nonce made with that certificate's key, the
  proof the `mtls` quorum factor verifies. A presented certificate is still
  checked, and the network transports keep requiring one.
- **A share claim without a client certificate is Unauthenticated on every
  network transport.** gRPC and Unix answered `INVALID_ARGUMENT`, QUIC
  PermissionDenied and MCP Internal. The one typed error is now
  `service.ClientCertRequiredError`, classified Unauthenticated in
  `pkg/api/errclass` (REST 401, gRPC/Unix/QUIC/MCP `UNAUTHENTICATED`). The
  per-transport types it replaces are removed: `grpc.ClientCertRequiredError`,
  `quic.ClientCertRequiredError`, the REST and MCP unexported twins and the
  SDK's `embedded.ClientCertRequiredError`; match
  `*service.ClientCertRequiredError` instead.
- **The TypeScript SDK and web UI follow the share-holder model.** They called
  routes the server no longer serves (`/v1/barrier/shamir/unseal-share`,
  `/unseal-shares`, `/seal-vote`, `/v1/barrier/officers/claim-status`, the
  tenant `unseal-share` and `seal-vote`) and read share bytes from claims.
  `OfficerClient` now administers holders only: `registerOfficer`,
  `registerRecoveryOfficer`, `listOfficers`, `startRekey`, `revokeOfficer` and
  their tenant forms (`registerTenantOfficer`, `listTenantOfficers`,
  `startTenantRekey`, `revokeTenantOfficer`), with the enrolment fields `role`,
  `encryptionKem`, `encryptionKey`, `factors` and `designated`; the claim and
  seal-vote methods are removed. `SealClient.initializeShamir` takes
  `{threshold, totalShares, policy, soPin, userPin}` and no secret;
  `SealClient.unseal` and `TenantClient.barrierUnseal` serve passphrase
  barriers only, and `TenantClient.barrierUnsealShare` is removed. The web UI
  drops the officer claim page and the share and seal-vote inputs: a sealed
  Shamir barrier shows which command its holders run, and the Seal Manager
  enrols, lists, rekeys and revokes holders. Holder keys never enter the page;
  holders claim and submit from the CLI or the desktop app.
- **The desktop `SealBinding`**: `InitializeShamir(threshold, total, policy)`;
  `Unseal` refuses a Shamir barrier, which its holders unseal. The embedded
  node no longer auto-unseals a Shamir barrier.

## [0.33.2-alpha] - 2026-10-03

The audit chain verifier no longer reports a missing log on an honest trail while the log rotates. Highlights:

- **Fixed a false tamper result during rotation.** Rotation now carries a sequence counter, and verification retries while a rotation is in progress. A rotation that stalls, fails or panics can no longer hide a deleted log or a broken link.

### Fixed

- **Audit chain verification no longer reports a stalled rotation as a missing log.**
  `ChainFileLogger.VerifyChain`/`VerifyChainSet` judged a missing active file on its directory
  listings alone. A writer descheduled between a rotation's rename and its re-open -- on a loaded
  host, for longer than the verifier's whole retry budget -- left every attempt the same listing
  with the active name absent, and the verifier reported the audit log missing
  (`*FileError`, "file does not exist") on an honest trail. The logger now keeps a rotation
  sequence that `rotate()` makes odd across the rename and re-open -- even again on every return
  path, a failed rename or reopen and a recovered panic included -- and the verifier counts an
  attempt only if it read the same even value on both sides of its listings; a stalled rotation
  is a `*ConcurrentRotationError` ("the trail moved"), never a fault. It takes no lock, and the
  verifier no longer takes the writer's lock to read the path.

## [0.33.1-alpha] - 2026-10-03

A clustered SDK boot that fails can now be retried cleanly. Highlights:

- **A failed or cancelled clustered boot leaves the node sealed and retryable.** Every tenant barrier is sealed, and every data-plane accessor is cleared.
- **A retried boot is fully alive.** Lease reaping, the reclaim sweep and tenant shard reconciliation now run on a live context after a retry.
- **The readiness wait honours the caller's context.**

### Fixed

- **The readiness wait honours the caller's context.** `NewClusteredNode`,
  `Node.Unseal`, the per-tenant volume host and the dedicated raft shards
  waited on `context.Background()`, holding a caller that gave up for up to
  15 seconds; they now return the context's error promptly. A wait that has
  already found the shard serving reports success even if the context is
  cancelled at that moment. (The wait itself -- a founder returns only once
  a quorum has elected a leader and that leader has committed in its term,
  within a 15s budget -- shipped in 0.30.1-alpha; this entry is the ctx fix
  on top of it, not a new wait.)
- **A clustered SDK boot that fails can be retried.** A failed or cancelled
  boot left the barrier reporting unsealed with its key already wiped and no
  boot pending, so the next `Unseal` reported a node that never booted as
  ready. The node is now left sealed, and the next `Unseal` boots it.
- **A cancelled boot seals every tenant barrier, not only the system one.**
  On the tenant-isolation path, `BuildTenantInfra` unseals every auto-unseal
  tenant barrier (`BarrierRegistry.RestoreVolumeTenants`) before re-attaching
  each tenant's per-tenant Host; the failure defer sealed only the system
  `StorageBarrier`, so a cancellation landing in that reattach left every
  restored tenant's DEK live in memory while `IsSealed()` reported `true`.
  The defer now seals through the `BarrierRegistry` (`SealAll`) when one is
  present -- tenants, then the system barrier -- falling back to the bare
  barrier seal otherwise, and clears the host and feature gate a failed
  attempt built (restoring the pre-attempt services node rather than the
  stopped post-boot one) so a retried `Unseal` never hands out a component
  the failed attempt already closed. `wireClientAndServer` publishes the
  `AuthFactorService`, `UserAuthFactorService` and `ABACPolicyReloader`
  handles, and the scoped encryption migrator, before steps that can still
  fail -- an admin-binding check honouring `ctx`, or the multi-protocol
  server failing to start -- so the same failure left those accessors
  handing out services built over the stopped host too. The defer now also
  calls `retractDataPlaneServices` and clears the migrator, mirroring
  `Close`, unbinds the auth-chain vocabulary (`n.authMethods`) so it falls
  back to the built-in tokens instead of consulting a middleware built over
  the stopped `svcNode`, and detaches the replicated volume-pool cursor and
  the cluster-wide tenant-slot store (new `ReplicatedStateStore.Unbind` and
  `BarrierRegistry.DetachReplicatedSlotStore`) -- defensive hygiene against a
  dangling reference to the stopped replicated KV, not a fallback that
  serves a request: a failed boot leaves the node in its pre-boot SEALED
  state, and `TenantService.CreateTenant` fails closed (`seal.ErrSealed`)
  before reaching a pool slot either way, because its first read goes
  through the system barrier itself. A `SealAll` failure is now logged
  rather than discarded.
- **The retried boot's background goroutines now run on a live context.**
  The lease-expiry sweeper, the reclaim sweeper and (when tenant isolation
  wires one) the tenant shard reconciler all run on `Node.background()`'s
  node-lifetime context; the wire-failure branch cancelled `bgCancel` but
  left `bgCtx` set to the now-cancelled context, and `background()` makes a
  fresh one only when `bgCtx` is nil -- so a retried boot's sweepers and
  reconciler ran on a context already `Done` and exited at once: no lease
  reaping (`#174` regressing), no reclaim sweep, no reconcile. The failure
  defer now clears `bgCtx` alongside `bgCancel`, and a new `bgWG` tracks
  each of those goroutines so the wire-failure branch can wait for them to
  actually exit before `svcNode.Stop` -- cancelling alone does not block
  until a goroutine's current tick notices, so a bare cancel raced
  `svcNode.Stop` against whichever of them woke up last.

## [0.33.0-alpha] - 2026-10-03

Per-tenant compaction, database wizard encryption, and discovery parity. This release contains BREAKING changes; see the entries below. Highlights:

- **Compaction is per tenant database, never store-global.** Each database gets its own compaction floor. Reads above the floor keep etcd semantics: the value live at the floor stays readable. Another database's compaction never cancels your watches.
- **Scheduled compaction policies are replicated state.** They live in the database's own store and run on the group leader only, so they survive failover and restart and are covered by backup. The platform node-keyspace policy is set through a new platform-only verb (`admin:node_compaction`) on every transport, not node config.
- **One feature-gate error:** `featuregate.NotActiveError` (503 / Unavailable) replaces the per-feature errors.
- **Desktop database wizard:** collects an encryption passphrase and actually creates the barrier. Shamir is disabled until per-database share holders ship.
- **Raft discovery parity:** the Go SDK, the TS SDK and MCP can set every discovery block. Secrets stay write-only and are bound to their destination, including top-level peers.

### Fixed

- **The desktop database wizard's barrier works.** It sent the barrier block
  without `enabled` and with no secret, so the server never created a barrier
  and the step had no effect. Choosing the passphrase barrier now asks for the
  passphrase and a confirmation and creates the database with `enabled: true`
  and that passphrase. The passphrase is write-only on the client: it is
  cleared once the create request is built (whether the create succeeds or
  fails) and when the wizard is cancelled, never logged, and shown nowhere --
  the summary says only that it is set. The barrier now defaults to none, so
  a barrier is chosen, never implied. Leaving the wizard by any route clears
  the passphrase. The Shamir card is disabled, with a tooltip, until
  per-database Shamir share holders ship, and the create step refuses a
  Shamir barrier itself should the card ever let one through; a per-database Shamir barrier
  request -- from any client -- is refused with
  `database.UnsupportedBarrierStrategyError` as 400 / `INVALID_ARGUMENT`
  instead of 500. The TypeScript SDK's
  `CreateDatabaseRequest.barrier` is a `DatabaseBarrierRequest`, which carries
  the `secret`.

### Added

- **Per-database scheduled compaction, off by default.** A database's owning tenant may set
  a compaction policy -- `revision` (keep the store's last N revisions) or `period` (keep the
  history newer than a duration) -- and the compaction scheduler
  (`server.DefaultCompactionInterval`, one minute; `server.WithCompactionInterval`) compacts
  that database, and only that database. A database without a policy is never compacted
  implicitly. **The policy and a period policy's revision samples are replicated state in the
  database's own store** -- one reserved record (`"\x00qrdb.compaction/" + scope`,
  `pkg/compaction`) beside the durable lease table, written only through the new Raft opcode
  `OpCompactionState` (16) behind the `prefix-compaction` cluster feature -- never the node's
  catalog and never a file: every replica holds it, snapshots and backups carry it, the
  replica hash covers it, and only the leader of the database's group runs it, so a leader
  change continues the schedule, retention clock included, and a standalone node keeps it
  across restarts. Samples are capped at `retain_period`/64 per period; samples the store's
  revisions cannot have produced are never a target; every scheduled proposal carries the
  database's drop fence for the incarnation the policy was set for, and dropping a database
  clears its record. On every transport: REST
  `PATCH /v1/databases/{name}/compaction`, gRPC and the Unix socket `DatabaseUpdateCompaction`
  (the policy is `DatabaseInfoResponse.compaction`, protobuf field 17),
  QUIC `db.UpdateCompaction`, MCP `qrdb_database_update_compaction`, and the embedded
  transport; the Go SDK `DatabaseClient.UpdateDatabaseCompaction` and `DatabaseInfo.Compaction`;
  the CLI `qrdb database update-compaction --mode --retain-revisions --retain-period`; the
  TypeScript SDK `client.database.updateCompaction(name, compaction?)` with
  `DatabaseCompactionConfig`/`CompactionMode` and `DatabaseInfo.compaction` (REST and the
  desktop client's `DatabaseBinding.UpdateCompaction`). There is no configuration-file form:
  a `compaction` key under `databases[]` is not read. Setting a policy requires
  `mvcc:compact` (`ScopeSelfStrict`), never
  `databases:update`, and create does not take one; every caller, the system tenant included,
  reaches only its own databases (another tenant's answers as missing). An invalid policy is
  `compaction.InvalidPolicyError` (400 / `INVALID_ARGUMENT`). Proved on all six
  transports in `sdk/go/conformance` (`TestCompactionPolicy_OnEveryTransport`); across a
  leader failover of a three-node cluster and a standalone restart in `sdk/go/node`
  (`TestClusteredNodes_CompactionContinuesAfterLeaderFailover`,
  `TestStandaloneNode_CompactionScheduleSurvivesRestart`); and through snapshot recovery and
  the replica hash in `pkg/store` (`TestCompactionState_*`).
- **Platform node-keyspace compaction, off by default.** A policy of the same
  `off|revision|period` type as a database's compacts the node keyspace -- every key outside
  all `<tenant>/<db>/` namespaces: the node's `__sys:` records and the system tenant's raw
  keyspace -- on the compaction scheduler. It is set by a new platform-only verb on every
  transport: REST `GET`/`PUT /admin/node-compaction`, gRPC and the Unix socket
  `GetNodeCompaction`/`SetNodeCompaction`, QUIC `admin.GetNodeCompaction`/
  `admin.SetNodeCompaction`, MCP `qrdb_node_compaction`/`qrdb_update_node_compaction`, and the
  embedded transport; the Go SDK `AdminClient.NodeCompaction`/`SetNodeCompaction`; the CLI
  `qrdb admin node-compaction` and `qrdb admin update-node-compaction --mode
  --retain-revisions --retain-period`; the TypeScript SDK `client.admin.nodeCompaction()` /
  `setNodeCompaction(compaction?)` (REST and the desktop client's `AdminBinding`). Both need the
  new permission `admin:node_compaction`, which is `ScopeCross` -- only a system-tenant identity
  can exercise it; the built-in Admin role carries it -- and the admin service also refuses a
  caller bound to any other tenant (`tenant.CrossTenantDeniedError`), so a tenant's Admin, or a
  tenant role granted the permission, is refused on all six transports. The policy is the
  `compaction.NodeScope` record of the shared store's replicated compaction table, written by
  `OpCompactionState` like a database's and run by the shared store's leader only. The
  compaction skips every key with a database namespace by construction
  (`tenantid.NamespaceOf`), so no tenant database's history, the system tenant's own included,
  is touched; its floor (`[0x06]`) is kept apart from the per-database floors. New Raft opcode
  `OpMVCCCompactNode` (15) behind the cluster feature `node-compaction`; new
  `service.NodeKeyspaceCompactor` and `mvcc.Store.CompactNodeKeyspace`/`CompactNodeKeyspaceInBatch`;
  `service.NodeCompactionUnavailableError` (422 / `FAILED_PRECONDITION`) on a node whose shared
  store keeps no MVCC history. There is no configuration-file form: `mvcc.node_compaction` is not
  read. Proved on all six transports, a tenant refused on each, in `sdk/go/conformance`
  (`TestNodeKeyspaceCompaction_OnEveryTransport`).

### Security

- **MVCC compaction no longer destroys other tenants' history.** Every prefix-isolated
  tenant's databases share one MVCC store, and `Compact` discarded every key's history below
  the revision -- so the system tenant, the one identity `mvcc:compact` admitted, destroyed
  every tenant's time-travel history, as-of reads, watch replay window and restore window in
  one call. Compaction is now per database: the store keeps a compaction floor per keyspace
  prefix, a database's namespaced view compacts only its own `<tenant>/<database>/` prefix
  (its whole shard under raft isolation), and reads, watches, restores and change scans are
  checked against the floors their keys fall under. No one compacts another tenant's history:
  `mvcc:compact` is `ScopeSelfStrict`, so the system tenant compacts only its own databases,
  through the etcd listener as on every other transport. Proved on all six transports in
  `sdk/go/conformance` (`TestCompaction_*`).

### Changed — BREAKING

- **BREAKING: one refusal for every feature-gated operation.** `shard.FeatureNotActiveError`
  is replaced by `featuregate.NotActiveError` (`Feature`, and `ShardID` when the feature is
  needed of one group's replicas), which every operation that waits on cluster-feature
  activation returns: a raft database create before `cluster-shard-ids`, and a prefix, node
  or compaction-state command before `prefix-compaction` or `node-compaction`. It is classified
  Unavailable on every transport (REST 503, gRPC/Unix `UNAVAILABLE`, QUIC
  `StatusUnavailable`) because it clears once activation completes. `shard.WithActivation`
  takes a `featuregate.Feature`.

- **BREAKING: `Compact` compacts the scoped database's history, and only its owning tenant
  may call it.** It used to compact the whole store and was reserved to the system tenant;
  `mvcc:compact` moved from `ScopeCross` to `ScopeSelfStrict`, and `sdk.PermissionMVCCCompact`
  (a cross-scope export) is removed. A tenant role holding `mvcc:compact` -- the built-in
  Operator does -- can now compact its own databases. `service.CompactSystemOnlyError` is
  removed.
- **BREAKING: there is no store-global compaction.** The raw shard service's `Compact` refuses
  with `service.CompactScopeRequiredError` (REST 422, gRPC/QUIC `FAILED_PRECONDITION`): the
  unscoped embedded client, a server built without a service registry, and
  `mvcc.Store.Compact`/`CompactInBatch` and `mvcc.RaftStorage.Compact`/`CompactedRevision` are
  gone. Keys outside every database namespace are compacted only by the platform's
  node-keyspace policy (see Added), never by `Compact`.
- **BREAKING: Raft opcode 13 (`OpMVCCCompact`) is retired** and replaced by
  `OpMVCCCompactPrefix` (14, payload `[revision:8][prefixLen:2][prefix]`), proposed only once
  the new cluster feature `prefix-compaction` is enabled (`featuregate.NotActiveError`, 503 /
  `UNAVAILABLE`, before then). A log holding opcode 13 is refused as an unknown opcode. The compacted marker
  `[0x04]` becomes one floor key per prefix (`[0x04][prefix]`); an existing `[0x04]` loads as
  the floor of the empty prefix.
- **BREAKING: compaction validates its revision.** Below 1 is
  `service.CompactRevisionInvalidError`, above the current revision
  `service.CompactRevisionFutureError` (etcd: `ErrGRPCFutureRev`), at or below the database's
  floor `service.RevisionCompactedError` (etcd: `ErrGRPCCompacted`); the state machine
  re-checks all three at apply and answers in the result. A compacted window of
  `MVCCScanChangesQuery` is now refused instead of returning an empty key set, and the etcd
  listener's `Range` below the floor answers `ErrGRPCCompacted` instead of `Internal`.
- **BREAKING: Go API.** `statemachine.MVCCStore.Compact` (`sdk.MVCCStore`) is removed: no
  implementation existed, and the state machines of `pkg/statemachine` -- regular, concurrent,
  fast and kv -- no longer dispatch compaction, refusing it as an unknown opcode like any verb
  they are not built with; MVCC compaction is the MVCC engine's (`pkg/engine`).
  `watch.HistoryReplayer.CompactedRevision(key, rangeEnd []byte)`
  (was `CompactedRevision()`), `mvcc.Store.CompactedRevision` replaced by
  `CompactionFloor`/`CompactedRevisionForKey`/`CompactedRevisionForRange`/`CompactedRevisionForPrefix`,
  and `mvcc.CompactedKey` by `CompactionFloorKey(prefix)`. New: `service.PrefixCompactor`,
  `featuregate.PrefixCompaction`.
- **BREAKING: `mvcc.compaction_retention` and `mvcc.revision_limit` are removed.** Nothing read
  either key; both advertised a node-wide retention that would compact every tenant's history
  implicitly.

### Fixed

- **Concurrent service construction no longer panics on metric registration.**
  `metrics.NewCounter`, `NewGauge` and `NewHistogram` registered outside the lookup's lock, so
  two nodes building the same services at once in one process both registered a metric and
  the second panicked with a duplicate registration.
- **A change scan reads only its own keyspace.** A watch replay or an
  `MVCCScanChangesQuery` of one database read every key's data entries in the change log
  before filtering, so another database compacting mid-scan failed it -- cancelling the first
  database's watch with a compaction error naming its own floor. `mvcc.Store.ScanChanges` now
  takes the caller's key matcher and applies it before reading anything else.
- **A past-revision `Range` answers for every key live at that revision.** It enumerated the
  current keys, so a key deleted since was silently left out; it now enumerates the keys with
  a data entry in the range, and a `CountOnly` range at a revision counts the keys live then.
- **A read racing a compaction reports the compaction.** `GetAtRevision` and `Range` checked
  the floor and then read with no snapshot, so a compaction committing in between made the
  read come back absent. A compaction now publishes its floor before its deletions are
  visible, and every read at a revision re-checks the floor after reading
  (`mvcc.RevisionCompactedError`).
- **Compaction drops a tombstone live at the floor**, as etcd does, whether or not the key was
  written again; the apply path refuses a prefix compaction whose prefix is not exactly one
  database's keyspace.
- **Compaction keeps the value live at the floor.** Compacting to a revision kept only each
  key's newest entry, so a read above the floor but below a key's newest write -- a key written
  at 1, 3 and 5, compacted to 3, read at 4 -- answered "revision not found", and a `Range` at
  that revision silently left the key out, breaking the contract a client listing just above
  the compaction point (`kube-apiserver`) relies on. Compaction now follows etcd's rule: each
  key keeps its newest entry at or below the revision and everything above it, and loses only
  older entries; a kept tombstone goes when the key has a newer entry. Both per-database and
  node-keyspace compaction. Proved in `pkg/mvcc` (every floor of a fixture), through the apply
  path in `pkg/engine`, and on all six transports
  (`TestCompaction_KeepsTheValueLiveAtTheFloorOnEveryTransport`).
- **etcd listener: a `Watch` from a compacted revision is etcd's compacted watch.** A start
  revision at or below the database's compaction floor was refused with a response an etcd
  client cannot match to its create, so `clientv3`'s `Watch` call never returned. It is now
  created and then cancelled with `CompactRevision` set to the floor, which the client reports
  as `rpctypes.ErrCompacted`; a watch whose replay compaction overtakes ends the same way. The
  hub's refusal is the new `watch.CompactedError`, which matches `watch.ErrCompacted` and
  carries the floor covering the watched range. Any other refused create is etcd's failed
  create (`Created`, `Canceled`, watch ID `-1`, the reason) instead of a hang, a watch that
  ends for a reason is cancelled with it instead of going silent, and the stream's sends are
  serialized (gRPC forbids concurrent sends on a stream, and each watch is forwarded on its own
  goroutine). Proved against the etcd client itself (`TestEtcdClient_*` in `pkg/api/etcd`).

- **MCP reaches every Raft field, and a Raft update keeps the blocks it sends
  back.** `qrdb_database_create` and `qrdb_database_update_raft` built only the
  discovery block `discovery_method` named, so every MCP Raft update removed the
  other blocks and their secrets, and MCP had no arguments for the top-level
  `peers` or `localNodeCount`, the multicast `announceInterval` and
  `discoverTimeout`, the DNS `lookupTimeout` or the token `timeout`. Both tools
  now take all of them (`raft_peers` / `peers`, `raft_local_node_count` /
  `local_node_count`, `multicast_announce_interval`,
  `multicast_discover_timeout`, `dns_lookup_timeout`, `token_timeout`), and a
  block is sent when `discovery_method` names it or any of its arguments is
  given.
- **Both SDKs can set Raft discovery.** The SDKs' Raft request type had no
  discovery fields, so no SDK caller could set discovery on any transport, and
  the Go SDK's MCP transport also dropped `peers` and `localNodeCount`. The Go
  SDK's `transport.DatabaseRaftConfig` and the TypeScript SDK's
  `DatabaseRaftConfig` now carry `discovery` and the `static`, `multicast`,
  `dns` and `token` blocks (`StaticDiscoveryConfig`, `MulticastDiscoveryConfig`,
  `DNSDiscoveryConfig`, `TokenDiscoveryConfig`) with their write-only secrets,
  and every Go SDK transport -- REST, gRPC, Unix, QUIC, MCP and embedded --
  sends all of it. gRPC and Unix share one request converter. The TypeScript
  `DatabaseRaftInfo` no longer extends `DatabaseRaftConfig`, since a response
  block reports a secret only as set; it declares the same top-level fields.
  The TypeScript SDK now also exports `DatabaseRaftInfo` and the discovery
  info and request types.

### Security

- **A kept static discovery secret stays with the top-level peers too.**
  Static discovery reaches a database's top-level Raft `peers` as well as the
  static block's, but a Raft update compared only the static block's, so
  re-pointing the top-level `peers` without the secret kept the stored secret
  for the new peers. The static destination is now the union of both lists
  (when `discovery` is `static` or unset), compared as a set; changing it
  without the secret is refused with `database.DiscoverySecretRebindError`.
  Destinations compare literally -- no name resolution or normalisation --
  which `docs/databases/api.md` now states.

## [0.32.0-alpha] - 2026-10-02

Strict tenant isolation, phase 2: cluster-wide shard IDs. This release contains BREAKING changes; see the entries below. Highlights:

- **Raft shard IDs are unique across the cluster.** They are claimed from one replicated record, in standalone and Raft modes alike. Two nodes can no longer create two Raft groups under one ID, and a dropped database's ID is never issued again. The node-local `shard-high-water` file and the per-node allocator are removed.
- **SECURITY, BREAKING: callers can no longer choose a shard ID.** The field is gone from database create and Raft update on every transport, both SDKs and the CLI (gRPC field reserved). Raft creates are refused until the `cluster-shard-ids` feature is active. A cluster that already holds raft-isolated databases must be rebuilt rather than upgraded in place.
- **Raft discovery secrets on update:** an omitted secret is kept only while its destination (peers, multicast group, token endpoint) is unchanged. Re-pointing a secret without resending it is refused.
- **`storageCodec`** is reported on every transport. Database configuration validation errors are 400s, not 500s. `make test-typescript` mirrors the CI job.

### Security

- **BREAKING: a database create can no longer choose its shard ID, on any
  transport.** A raft database create claimed a cluster-wide shard ID only when
  the request named none. A request that named one -- `shardId` on REST, QUIC
  and the SDKs, `shard_id` on gRPC, `raft_shard_id` on MCP, `--shard-id` on the
  CLI -- was recorded in the receiving node's ledger alone and never checked
  against what other nodes hold. One tenant could therefore place a database on
  another tenant's Raft group, or on a reserved shard such as the system shard.
  What changed:
  - The field is removed from every transport's create and Raft-update request:
    - REST, QUIC and MCP JSON (the key is now ignored);
    - gRPC, which reserves field 2 of `DatabaseRaftConfig`;
    - the Go and TypeScript SDKs' `DatabaseRaftConfig`;
    - the CLI's `database update-raft --shard-id`.
  - A database's shard ID is reported only as `DatabaseInfo.shardId`.
  - As defence in depth, `database.Manager` refuses a caller's shard ID with
    `database.CallerShardIDError` (400 / `INVALID_ARGUMENT`), and
    `Manager.UpdateRaft` keeps the issued ID.

### Added

- **`make test-typescript`** runs exactly what CI's `test-typescript` job runs
  -- the TypeScript SDK's `npm ci`, `tsc`, `npm test` and build, then
  `svelte-check` on the desktop frontend against the built SDK -- and the job
  now runs the target.

### Fixed

- **Every transport reports a database's storage codec.** `storageCodec` is
  in `DatabaseInfo` on the wire (protobuf `storage_codec = 16`), in the Go SDK
  (`DatabaseInfo.StorageCodec`) and in the TypeScript SDK. QUIC and MCP had
  reported it only while they sent the internal record, and no transport did
  once every response came from one converter. **BREAKING:** the Raft block of
  a response no longer repeats the shard ID -- the database's is
  `DatabaseInfo.shardId`; protobuf reserves field 2 of `DatabaseRaftInfo`, and
  the Go SDK's `DatabaseRaftInfo.ShardID` is removed.
- **The user shard range belongs to databases.** Shard IDs from
  `shard.UserMin` (1000) up are issued by the cluster to databases. A node's
  `raft.shard_id` in that range is a configuration validation error, and an
  admin start-shard naming one is refused with `service.UserRangeShardError`
  (400 / `INVALID_ARGUMENT`) on every transport.
- **A Raft update keeps the secrets it leaves out.** Secrets are write-only,
  so a client that read a database's Raft settings and wrote them back sent
  none: the update cleared the static shared secret and failed validation for
  multicast and token discovery. A discovery block without its secret now
  keeps the stored one; sending a secret rotates it; leaving a block out
  removes it. A kept secret stays with what it authenticates: an update that
  changes the static peer set, the multicast group or interface, or the token
  endpoint without resending the secret is refused
  (`database.DiscoverySecretRebindError`, 400), so a caller cannot point a
  secret it never saw at a destination of its choosing. MCP now builds the
  static block whether or not the request has a secret, and takes its peers
  (`static_peers`), as the other transports do. A Raft update that would remove a raft-backed database's Raft
  configuration -- its shard ID with it -- is refused
  (`database.RaftConfigRequiredError`, 400). Database configuration
  validation failures are 400 / `INVALID_ARGUMENT` on every transport instead
  of 500. `database.WithShardIDClaimer`, added in this cycle, is removed: no composition has the
  replicated store when it builds the Manager, so `SetShardIDClaimer`, now
  safe to call while the Manager serves, is the one way to set the claimer.
- **Shard IDs are unique across the cluster, not only per node.** Every
  node's allocator started at 1000 and moved past only the IDs that node had
  issued, so two nodes creating raft databases were handed the same shard ID:
  two Raft groups under one ID. A database dropped on one node and created
  again through another could also be handed the dropped incarnation's ID,
  which the drop fence relies on never happening.
  - **How IDs are issued now.** Every raft database create claims its ID from
    one replicated high-water mark in the system store
    (`__sys:shard/high-water`, `shard.Claimer`) with a compare-and-swap, in
    standalone and raft mode alike.
  - **No node-local shard state.** The per-node counter (`shard.Allocator`,
    `database.WithAllocator`) and its `{dataDir}/shard-high-water` file are
    removed, so a node restored from a backup onto an empty data directory
    never re-issues an ID. Nothing migrates the old file.
    `Manager.SetShardIDClaimer` sets the claimer.
  - **Refusals.** `database.Manager.Provision` refuses a Raft configuration
    (`database.ProvisionRaftError`), because nodes provisioning on their own
    cannot agree on a shard ID; no shipped path provisions one. A create that
    cannot claim refuses with `database.ShardIDClaimError` (503 / retryable)
    instead of falling back to a per-node ID.
  - **New cluster feature `cluster-shard-ids`.** Once every node claims, a
    binary that allocates per node can neither start nor be admitted as a
    replica.
  - **BREAKING: raft creates wait for `cluster-shard-ids`.** On a node whose
    groups can hold other binaries, a raft database create is refused with
    `shard.FeatureNotActiveError` (503 / `UNAVAILABLE`, retryable) until the
    feature is active cluster-wide, and nothing is claimed. That closes the
    rolling-upgrade window in which an old node allocates per node while a new
    one claims.
  - **BREAKING: a cluster that holds raft databases cannot be upgraded in
    place.** Their shard IDs were issued per node and may collide, and the
    high-water mark could re-issue one. Rebuild such a cluster on this release
    and move the data across; nothing seeds or migrates the old IDs.
  - This is phase 2 of the strict tenant isolation plan.

## [0.31.0-alpha] - 2026-10-02

Dependency release. This release contains BREAKING changes; see the entries below. Highlights:

- **go-quicraft v0.16.0, go-truststrap v0.4.0-alpha, go-enclave v0.6.2-alpha.** go-enclave v0.7.0-alpha is held back: go-quicraft v0.16.0 does not compile against it. That needs a go-quicraft release built on enclave v0.7.
- **BREAKING (wire):** go-truststrap v0.4 adds a suite header to the Noise bootstrap wire. qrdb runs the bootstrap server itself, so a mixed-version cluster cannot enroll a new member. Finish a rolling upgrade before adding nodes.
- **Third-party patch and minor updates** (pebble 2.1.7, etcd 3.7.2, pgx 5.11, chi, mysql driver, roaring, mongo-driver, x/net, x/tools). Held back: grpc stays on v1.83.2 because v1.84.0 introduces GO-2026-6443; psql-wire and mcp-go stay at their current majors.
- **`make vulncheck`** runs govulncheck in the root and sdk/go modules.
- **Fix:** the file storage engine's `List` returns raw keys instead of their percent-encoded paths.

### Dependencies

- **go-quicraft v0.15.0-alpha -> v0.16.0** (the project's first non-`-alpha`
  tag) in the root, `sdk/go` and `sdk/go/gorm/conformance` modules, in
  lockstep as the dependency-resolution convention requires.
- **go-truststrap v0.2.0-alpha -> v0.4.0-alpha.** **BREAKING (wire,
  upstream):** every bootstrap connection's first frame now carries a
  two-byte `version || suite` header, incompatible with the
  v0.2.0-alpha/v0.3.0-alpha wire. **This is qrdb-to-qrdb, not qrdb-to-an-
  external-dependency:** `pkg/clusterenroll` runs both halves of the
  handshake with this release's go-truststrap. The first node of a cluster
  runs the Noise bootstrap **server** (`startNoiseAdvertiser` /
  `noiseboot.NewServer`, `noiseboot` being
  `go-truststrap/pkg/noiseproto/bootstrap` directly) from the moment it
  generates or loads the cluster CA, on every boot (`ResumeAdvertiser`); a
  joining node is the **client** (`truststrap.AutoFetch`) once member-side
  CSR join ships (the join path is currently refused up front with
  `EnrollmentUnavailableError`, go-qrdb#241, so no client dials today, but
  the server is live now). A node on this release cannot complete the
  handshake against a peer's server on the old wire, and vice versa: **a
  mixed-version cluster cannot enroll a new member until every node able to
  advertise is on this release.** The fleet moves together; the old wire is
  obsolete, not a compatibility target. See
  [docs/clustering/user.md](docs/clustering/user.md#upgrading-a-cluster) for
  the operational note. `bootstrap.Server` was also made single-use in
  v0.3.0-alpha (restart removed); `pkg/clusterenroll` already builds one
  `*noiseboot.Server` per `Advertiser` and never restarts it in place, so
  this needed no qrdb change. Noted for `lane/ca-keys-replicated`, which is
  building member-side CSR join on `noiseproto`: the suite-negotiation API
  (`ServerConfig.P256StaticKey`, `ClientConfig.ServerP256StaticKey`,
  `ClientConfig.Suite`, `bootstrap.SuiteNoiseNK`/`SuiteP256AESGCM`) is new
  surface to adopt on that branch, not a removal.
- **go-enclave v0.6.1-alpha -> v0.6.2-alpha only** (bugfixes: a negative
  `BudgetError.InUse` race, several release/rollback accounting fixes, a
  `ModeRootKey` sealer no longer re-unsealing its root after a successful
  first unseal, sticky canary-corruption reporting across a refused
  release). **go-enclave v0.7.0-alpha, which the owner asked for by name, is
  explicitly HELD BACK.** v0.7.0-alpha is a breaking MINOR release threading
  `context.Context` through `NewSealed`, `(*Sealer).Seal`,
  `(*SealedEnclave).Open`/`OpenWith` and the `KeyProtector`/`RootUnsealer`
  provider seam, and freezing the plaintext `Open`/`OpenWith` return
  read-only. Go's MVS selects the highest version any module in the graph
  requires, so requiring go-enclave v0.7.0-alpha here while go-quicraft
  v0.16.0's own `go.mod` asks for v0.6.2-alpha would still select v0.7.0-alpha
  for go-quicraft's build too — and go-quicraft v0.16.0 does not compile
  against it: 15 call sites across `pkg/seal/{barrier.go,strategy.go,
  strategy_software.go}` call `enclave.NewSealed` and
  `(*SealedEnclave).Open` without the new `ctx` parameter. Reproduced in a
  scratch module (`GOWORK=off go build -gcflags="all=-e" ./...` against
  `github.com/jeremyhahn/go-quicraft/pkg/seal`); the same module builds
  go-quicraft v0.16.0 cleanly against v0.6.2-alpha, the version its `go.mod`
  already declares. go-quicraft needs its own go-enclave v0.7 adoption before
  qrdb can take it; see the upgrade guide at go-enclave `docs/README.md`
  ("Upgrading from v0.6 to v0.7") for the call shapes that migration needs.
- **go-objstore v0.3.0-alpha and go-codec v1.4.0 verified already latest** —
  no change.
- **Third-party, direct requires, patch/minor only:**
  `github.com/RoaringBitmap/roaring/v2` v2.15.0 -> v2.29.0,
  `github.com/cockroachdb/pebble/v2` v2.1.4 -> v2.1.7,
  `github.com/go-chi/chi/v5` v5.3.0 -> v5.3.2,
  `github.com/go-sql-driver/mysql` v1.9.3 -> v1.10.1 (root and
  `sdk/go/gorm/conformance`),
  `github.com/jackc/pgx/v5` v5.10.0 -> v5.11.0,
  `github.com/tidwall/btree` v1.8.1 -> v1.8.2,
  `go.etcd.io/etcd/api/v3` and `go.etcd.io/etcd/client/v3` v3.6.8 -> v3.7.2,
  `go.mongodb.org/mongo-driver` v1.17.9 -> v1.17.10,
  `golang.org/x/net` v0.58.0 -> v0.59.0,
  `golang.org/x/tools` v0.49.0 -> v0.51.0,
  `gorm.io/driver/postgres` v1.6.2 -> v1.6.3 (`sdk/go/gorm/conformance`
  only). `protoc`/`protoc-gen-go`/`protoc-gen-go-grpc` pins and
  `google.golang.org/protobuf` are already current and untouched.
- **Held back, with reasons:**
  - **`google.golang.org/grpc` stays at v1.83.2.** v1.84.0 is affected by
    **GO-2026-6443**, a server panic on a request missing the `:authority`
    pseudo-header, reachable through this repository's gRPC API server
    (`pkg/api/unix/server.go:725` and the main gRPC listener). No tagged
    grpc-go release fixes it yet and `govulncheck` against `dev/main`
    reports zero findings; taking v1.84.0 now would introduce a
    remotely-triggerable DoS. Re-evaluate once a patched tag exists.
  - `github.com/jeroenrinzema/psql-wire` v0.18.0 (latest v0.20.0 changes
    `ParseFn`'s signature; `pkg/sql/pgsql/server.go`'s `wire.NewServer` call
    no longer type-checks against it — a pre-1.0 breaking change this release
    does not adopt).
  - `github.com/mark3labs/mcp-go` v0.45.0 (latest v1.1.1 **builds** cleanly
    against every qrdb call site, but changes the MCP transport's
    unauthenticated-request error shape: `TestWithAuthenticators_BearerAuthenticatesOnEveryNetworkTransport/mcp`
    in `sdk/go/node` — the Tier-1 auth-parity test across all six transports —
    fails under it with "no credential: refusal is not unauthenticated:
    transport error: authorization required". This is a behavioral
    regression in a Tier-1 capability, not a compile break, so it is held
    back rather than silently shipped; confirmed green again at v0.45.0.
    Holding mcp-go back also means its transitive pulls
    (`buger/jsonparser`, `mailru/easyjson`, `google/jsonschema-go`) stay off
    every module's dependency graph; `jsonparser`/`easyjson` in particular
    must stay at v1.1.2/v0.9.2 — a stray trial of mcp-go v1.1.1 had briefly
    downgraded them to v1.1.1/v0.7.7 via MVS, and v1.1.1 reintroduces
    **GO-2026-4514**. `govulncheck` confirms zero findings at the restored
    versions.)
  - Every `cloud.google.com/go/*`, `github.com/aws/aws-sdk-go-v2/*`,
    `github.com/wailsapp/wails/v2`, and similar large transitive graphs
    pulled in only by `github.com/dolthub/go-mysql-server`'s indirect
    dependencies are untouched: they are not direct requires of this
    repository and move with `go-mysql-server`'s own releases.
- **TypeScript SDK (`sdk/typescript`) and desktop frontend
  (`cmd/qrdb/frontend`):** `npm outdated` and `npm update` run in both.
  `sdk/typescript`'s `typescript` and `vitest` are already at the latest
  version their declared ranges allow; the next versions available
  (`typescript` 7.x, `vitest` 5.x) are major bumps, held back. `tsc` and
  `vitest run` stay green (8 files, 183 tests). The frontend's
  `@fontsource-variable/inter`, `@playwright/test`, `@tailwindcss/vite`,
  `@types/node`, `daisyui`, `svelte` and `svelte-check` move to the latest
  version their ranges allow; `@sveltejs/vite-plugin-svelte`, `lucide-svelte`,
  `typescript` and `vite` have only major-version updates available and are
  held back. `svelte-check` is green (0 errors) after rebuilding
  `sdk/typescript`'s `dist/`, which the frontend's `file:` dependency on
  `@qrdb/sdk` resolves against.
- **`make vulncheck`**, backed by `govulncheck` (pinned alongside the
  repository's other analysis tools), runs in `make lint` (so the CI Lint
  job gates it) and is also runnable standalone per module
  (`make vulncheck`, `make vulncheck-sdk`). It is how GO-2026-6443 and
  GO-2026-4514 were caught before either reached `dev/main`.

### Fixed

- **`pkg/storage/file`'s `List` now decodes the percent-encoded path back to
  the raw key it returns**, found while extending a sibling fix's parity
  tests to the file backend. It was the one place in that engine that read
  the on-disk path without reversing `encodeKeyToPath`
  (`pkg/storage/file/keyenc.go`), so a key that needed escaping (anything
  outside printable ASCII, including a NUL-byte-prefixed key like the lease
  table's) came back from `List` as the literal percent-encoded string
  rather than the key that was `Set`, and a prefix search for the real
  (unescaped) prefix matched nothing even though the key existed.
  `Get`/`Set`/`Delete`/`Exists` and the iterator were unaffected; only
  `List` had the gap.

## [0.30.1-alpha] - 2026-10-02

Fix release. Highlights:

- **A shard is handed out only once it can serve a linearizable read.** Every host-open path now waits for the new leader to commit in its term, not just for a leader to be known: the tenant volume host, dedicated per-database shards, the SDK clustered node, the qrdbd system shard (before its listeners start), and the desktop embedded server. This fixes intermittent "read index not ready" failures right after a node or shard starts. `NewClusteredNode` now blocks until a quorum has elected and committed (within a 15s budget).

### Fixed

- **A shard is handed out only once it can serve a linearizable read.** Every
  path that opens or reopens a shard -- the per-tenant volume host, the
  dedicated per-database raft shards (including an encryption migration's
  reopen), the system shard before `qrdbd` serves, and the desktop embedded
  server -- waited only until a leader was known, and `sdk/go/node`'s
  `NewClusteredNode` waited for nothing unless tenant isolation was on: three
  founders booted together came back before any election. A leader elected a moment ago refuses every ReadIndex until it
  commits in its term, so the first read after an open could fail with
  `quicraft: read index not ready` (seen as intermittent failures after a
  tenant encryption migration and right after cluster formation). They now
  wait through `server.WaitForShardServing` for both, in one 15s budget, and
  time out with `*server.ShardLeaderWaitError` or the new
  `*server.ShardReadWaitError`. `qrdbd`'s best-effort ReadIndex warm-up is
  replaced by the wait, and a node that cannot serve refuses to start with
  `SystemShardServingError`. The desktop bindings' `LeaderElectionTimeoutError` is
  removed; a shard that cannot serve reports `ShardStartError` wrapping the
  wait's error.

## [0.30.0-alpha] - 2026-10-02

Database response hardening. This release contains BREAKING changes; see the entries below. Highlights:

- **SECURITY: a database's secrets are write-only on every transport.** Raft discovery shared secrets and tokens, and the barrier secret, were returned by get, list, create and update on REST, gRPC, unix, QUIC and MCP. Responses now carry `sharedSecretSet` / `tokenSet` flags and no secret field. A single converter (`pkg/api/dbwire`) builds every database response, and a single SDK decoder replaces the per-transport copies.
- **Parity:** QUIC and MCP report `shardId`, `dataDir` and `tenantId` again. `dedicatedWal`, the discovery blocks, `protocol.etcd` and `tenantId` are typed in both SDKs. The TypeScript SDK reads `shardId` and `dataDir`, the keys REST sends.
- **CI runs the TypeScript SDK and the desktop frontend's svelte-check.**

### Security

- **BREAKING: a database's discovery secrets are write-only.** Every read of a
  database -- get, list, and the response to every update -- returned the
  stored Raft discovery secrets (`raft.static.sharedSecret`,
  `raft.multicast.sharedSecret`, `raft.token.token`) on REST, gRPC, the Unix
  socket, QUIC, MCP, the embedded transport and the desktop bindings, to
  anyone allowed to read the database's configuration. No response carries
  them now; each is reported only as `sharedSecretSet` / `tokenSet`.
  - **Typed so it cannot come back.** The responses are read views with no
    field for a secret: in the Go SDK `DatabaseInfo.Raft` is a
    `transport.DatabaseRaftInfo` and `DatabaseInfo.Barrier` a
    `transport.DatabaseBarrierInfo` (callers of the old field types must
    move); in protobuf `DatabaseInfoResponse.raft` and `.barrier` are the new
    `DatabaseRaftInfo` and `DatabaseBarrierInfo`, with the secret field numbers
    `reserved` and the other numbers unchanged; in the TypeScript SDK
    `DatabaseInfo.raft` is a `DatabaseRaftInfo`. Request types still carry the
    secrets.
  - **One converter.** Every transport builds its response in `pkg/api/dbwire`
    (the gRPC and Unix SDKs decode it in `transport/internal/dbconv`); the
    per-transport copies are gone. QUIC and MCP had sent the server's internal
    snake_case record, which the Go SDK decoded as a zero shard ID, data
    directory and tenant; they now send the same shape as REST.
  - Proven on every transport by a test that stores each secret through the
    real service and reads it back.

### Added

- **Parity of `DatabaseInfo` across transports and SDKs.** `dedicatedWal` is
  now in the protobuf response (`dedicated_wal = 15`) and the Go SDK; the Raft
  discovery settings (method, static, multicast, DNS, token -- minus secrets)
  and `protocol.etcd` are typed in both SDKs; REST reports `tenantId` like gRPC.
  The Go and TypeScript wire contract tests hold both SDKs to the REST response
  with no exception list.
- **CI runs the TypeScript SDK and the desktop frontend.** A `test-typescript`
  job, required by the CI gate, runs `npm ci`, `tsc`, `npm test` and the build
  in `sdk/typescript`, then `svelte-check` in `cmd/qrdb/frontend` against the
  built SDK. The frontend's seventeen type errors are fixed; none changed what
  the frontend sends.

### Fixed

- **The TypeScript SDK reads a database's shard ID and data directory.**
  `DatabaseInfo` declared `shard_id` and `data_dir`, but REST (and the desktop
  bindings) send `shardId` and `dataDir`, so both were always undefined; the
  desktop database views never showed either. The fields are now `shardId` and
  `dataDir` (**BREAKING** for TypeScript callers that read the old names).
  A recorded REST response is now decoded by a Go handler test, which also
  holds the Go SDK's `transport.DatabaseInfo` to it, and by a TypeScript SDK
  test, and `npm test` in `sdk/typescript` typechecks the tests before running
  them. The desktop database wizard no longer carries a shard ID.

## [0.29.1-alpha] - 2026-10-02

Fix release. Highlights:

- **CI build gate green again.** v0.29.0's refused-unsigned-holder-forward path wrote a fixed 502 for a received error and failed the `test/buildgates` error-routing gate. It is now a `LeaderForwardError` like the other forwarding failures; behaviour is unchanged.

### Fixed

- **A refused unsigned holder forward is classified like every other forwarding
  failure.** v0.29.0 wrote the holder refusal straight as a 502, bypassing the
  error-mapping rule that `test/buildgates` enforces for REST handlers, which
  left that gate red. The refusal is now a `*rest.LeaderForwardError` wrapping
  `*rest.HolderForwardUnsignedError`, the same shape as the other two forwarding
  failures in `forwardToLeader`. The status (502), the fixed message and
  `errors.As` reachability of the holder error are unchanged.

## [0.29.0-alpha] - 2026-10-02

Holder-certificate hardening, following the v0.28.1 security hotfix. This release contains BREAKING changes; see the entries below. Highlights:

- **The holder mark survives leader forwarding.** A forwarded officer or custodian request is still recognised as a holder by the leader, and gets no roles. A holder request is never forwarded unsigned (502).
- **A holder certificate that can sign certificates is refused.** A CA certificate, cert-sign key usage, or any key usage or EKU set other than the one requested is refused on every holder entry point, whatever CA is composed in.
- **The CeremonyCA signing contract is stated as a MUST:** sign with `SignCSROptions.RawSubject()`, take nothing from the CSR but its key, and issue a leaf with exactly the requested usages.
- **`custodian` and `security-officer` are reserved tenant ids.** Holder profile names move to the new leaf package `pkg/holderprofile`.

### Security

- **A holder certificate stays a holder when its request is forwarded to the
  leader.** The on-behalf assertion a follower signs carried only the subject
  and tenant, so the leader resolved a forwarded holder certificate named
  `admin` to the `admin` account and its roles. The assertion now carries the
  holder profile (`forward.CallerAssertion.HolderProfile`, omitted for users
  so their assertions are unchanged), and the leader re-marks the identity; a
  profile it does not recognise is refused. A holder request is never
  forwarded unsigned, because an unsigned forward is authorized as the
  forwarding node: a follower without a signing identity, or whose signing
  fails, refuses it with 502 (`rest.HolderForwardUnsignedError`), whose body
  is a fixed message; the signing failure is logged and kept for `errors.Is`,
  never sent to the client. In a cluster mixing this release with an earlier
  one, an earlier leader ignores the field.
- **BREAKING: the holder profile names live in the new leaf package
  `pkg/holderprofile`.** `auth.ProfileCustodian` and
  `auth.ProfileSecurityOfficer` are removed in favour of
  `holderprofile.Custodian` and `holderprofile.SecurityOfficer`, with
  `holderprofile.Is` as the predicate; `auth.HolderProfile` uses it. There are
  no aliases. The leaf lets `pkg/auth` and `pkg/tenant` share the names without
  `pkg/tenant` depending on `pkg/auth` for them.
- **The ceremony refuses a holder certificate that is a sub-CA.** A
  `CeremonyCA` returning a certificate with the right subject was trusted with
  what the certificate could do. One that is a CA, carries certificate-signing
  key usage or any key usage other than the requested one (CRL signing
  included), or carries a set of extended key usages other than exactly the
  requested set (order aside, duplicates refused) is now refused with
  `*ceremony.IssuedCertCapabilityError` naming the field, on every signing
  path, and nothing is recorded. A holder certificate able to sign
  certificates would be a sub-CA under the cluster CA and could mint any login.
- **BREAKING: `custodian` and `security-officer` are reserved tenant ids.** An
  officer certificate issued before v0.28.1-alpha carries its profile as its
  first Subject OU, which the mTLS authenticator reads as the tenant, so a
  tenant by either name would be the tenant those certificates authenticate
  in. Creating or deleting one is refused with `*tenant.ReservedTenantIDError`
  (400 / `INVALID_ARGUMENT`), like `system` and `default`.

### Changed

- **The `CeremonyCA` signing contract is a stated MUST.** The `CeremonyCA` and
  `SignCSROptions` doc comments and docs/ceremony now say that every CA,
  including one composed in from outside qrdb, MUST sign with
  `SignCSROptions.RawSubject()`, carry no SAN, take only the CSR's key, and
  issue a leaf with exactly the requested key usage and extended key usages --
  and that a non-conforming CA's certificates are refused, fail-closed, with
  `*ceremony.CertSubjectError` or `*ceremony.IssuedCertCapabilityError`.

### Fixed

- **docs/shamir-custody/user.md no longer says an officer certificate reaches
  the unseal step.** It reaches the officer claim steps only; unseal shares are
  submitted as a user or an administrator. Documentation only.

## [0.28.1-alpha] - 2026-10-02

Security hotfix. This release contains BREAKING changes; see the entries below. Highlights:

- **CRITICAL: registering an officer can no longer mint a login.** Officer, recovery, `ceremony sign-csr` and initial-ceremony certificates name the registered holder and the scope's tenant; the CSR contributes only its key. Holder certificates authenticate with no roles.
- **Audit queries are confined to the caller's own tenant on every transport**, the system tenant included. `audit:read:cross` is removed.
- **No caller mints, lists or revokes another tenant's certificates**, the system tenant included. One revocation target is shared by every transport.

### Security

- **CRITICAL, BREAKING: registering an officer can no longer mint a login.**
  The ceremony CA is the node's cluster CA, which the mTLS listener trusts for
  client logins, and `LocalCA.SignCSR` copied the CSR's Subject and SANs. A
  tenant Admin registering a tenant-barrier officer with a CSR for
  `CN=admin, OU=system` received a certificate that authenticated as the
  system tenant's `admin` -- under the default `auth.mtls_binding_policy`, an
  account with no enrolled binding is admitted on CN and OU alone -- and
  `OU=globex` or a SPIFFE SAN for globex worked the same way. The same copy
  ran on system and recovery officer registration, `ceremony sign-csr` and the
  initial M-of-N ceremony. Every one of those paths now names the registered
  holder and the scope's tenant in `ceremony.SignCSROptions` (new `TenantID`
  and `Username`), and the CA issues `CN=<username>`,
  `OU=[<scope tenant>, <profile>]` -- one RDN per OU, so a long tenant id stays
  first -- with **no Subject Alternative Name**: the CSR contributes only its
  key. The ceremony re-checks every certificate a `CeremonyCA` returns
  (Subject, no SANs, the CSR's own key) and refuses one that differs with
  `*ceremony.CertSubjectError`, so a CA composed in from outside qrdb is held to
  the contract too (`SignCSROptions.Subject` and `RawSubject` state it). As
  defence in depth, a certificate carrying a holder profile OU
  (`security-officer`, `custodian`) is marked by the mTLS authenticator
  (`auth.HolderProfile`) and is never resolved to a user account: it
  authenticates with no roles and reaches only the identity-gated ceremony
  surface. The officer certificate layout changes from `OU=[<csr ou>...,
  <profile>]` to `OU=[<scope tenant>, <profile>]`; `ceremony.CSRTenantSANError`
  is removed. New leaf package `pkg/tenantid` holds the system tenant id that
  `pkg/ceremony` needs (`tenant.SystemTenantID` is it).
- **BREAKING: an audit query reads only the caller's own tenant's events, on
  every transport.** The query took `tenant_id` from the request unchecked on
  REST (`GET /v1/audit/events`), gRPC and the unix socket
  (`AuditService/Query`), QUIC (`audit.Query`), MCP (`qrdb_audit_query`) and
  the embedded client, and an omitted `tenant_id` read every tenant's events.
  The audit sinks are node-local and shared by every tenant, so a tenant Admin
  holding `audit:query` read the whole node's trail. Every transport now
  confines the filter with `tenant.ConfineAuditFilter` before a logger is read:
  an omitted `tenant_id` reads the caller's own tenant, and one naming any other
  tenant is refused (REST 403, gRPC and unix `PERMISSION_DENIED`, QUIC
  `StatusPermissionDenied`, the MCP permission-denied tool code, and
  `*tenant.CrossTenantDeniedError` on embedded). There is no system-tenant
  exception: a system-tenant caller reads the system tenant's events plus the
  platform events that carry no tenant (`audit.Filter.Untenanted`, never decoded
  from a request), and is refused every other tenant's. The tenant match runs
  before pagination, so no time range, filter or cursor widens it. New:
  `tenant.CallerTenant`, `tenant.Confine` and `tenant.ConfineAuditFilter`.
- **BREAKING: no caller can mint, list or revoke another tenant's
  certificates -- the system tenant included.** `certs:generate` was
  authorized against the request body's tenant under `ScopeSelf`, which the
  system-tenant rule lifts, so a system-tenant Admin could mint a CA-signed
  client certificate naming another tenant's user; under the default
  `auth.mtls_binding_policy` that certificate authenticates as the user.
  `certs:list` and `certs:revoke` had the same lift. `certs:generate`,
  `certs:list`, `certs:revoke`, `certs:read` and `certs:export` are now
  `ScopeSelfStrict`, so on REST, gRPC, unix, QUIC and MCP the target tenant
  must be the caller's own for every caller; a request naming another tenant
  is refused with the transport's permission-denied form. The embedded
  transport, which runs no authorization middleware, applies the same rule
  with `tenant.Confine`: `GenerateCert` and `ListCerts` confine the named
  tenant, and `RevokeCert` confines the serial's owner, or the system tenant
  for a deny-by-serial revocation (`*tenant.CrossTenantDeniedError`).
  `certs:revoke:cross` -- the deny-by-serial revocation of a serial no tenant
  owns -- stays `ScopeCross`. A platform tool that issued certificates into
  customer tenants must now run as each tenant's own Admin.
- **BREAKING: `audit:read:cross` is removed.** The permission was declared
  `ScopeCross`, granted to the built-in Admin role and re-exported as
  `sdk.PermissionAuditReadCross`, and nothing in qrdb enforced it -- yet its
  name promised a read of every tenant's audit trail. Under the owner ruling no
  caller reads another tenant's audit events, so the permission, its grant, its
  "Cross-Tenant Audit" category and its SDK name are deleted. A downstream
  consumer naming `sdk.PermissionAuditReadCross` no longer compiles. A custom
  role stored with `audit:read:cross` keeps the inert string, but creating or
  updating a role that names it is now refused with
  `*role.InvalidPermissionError`; the built-in Admin loses it on its next
  reconcile.

### Fixed

- **A custodian's audit events name its tenant.** Registration and revocation
  (refusals included) in `pkg/custodian`, and the share-claim and unseal-share
  steps in `pkg/ceremony`, now set `TenantID` to the custodian's tenant. With
  audit queries confined to the caller's tenant, an event without one is a
  platform event only the system tenant reads, so a tenant's auditors could
  not see their own custodians' activity.
- **One revocation target for every transport.** `middleware.CertRevokeTarget`
  states what a revocation is authorized as -- the serial's owner with
  `certs:revoke`, or the system tenant with `certs:revoke:cross` for a serial no
  tenant owns -- and both `AuthorizeCertRevoke` and the embedded transport use
  it.

- **The REST, QUIC and MCP SDK clients speak the audit query's wire shape.**
  The server's wire types are `audit.Filter` and `audit.Page`, whose JSON names
  (`tenant_id`, `client_ip`, `request_id`, `prev_hash`) differ from the SDK's
  `transport.AuditFilter` and `transport.AuditEvent`. The QUIC client sent the
  filter's tenant as `tenantID`, which the server never read, so a QUIC query
  silently ignored its tenant; and the REST, QUIC and MCP clients decoded every
  event without its tenant, client IP, request id and chain hash. They now
  convert through `sdk/go/transport/internal/auditconv`, which the embedded
  client shares.

## [0.28.0-alpha] - 2026-10-02

SQL correctness. This release contains BREAKING changes; see the entries below. Highlights:

- **Autocommit statements are atomic.** Each one runs as an implicit one-statement transaction: all its rows commit together, or none do. Its row-lock release is folded into the same write. The statement is bounded like a transaction (8 MiB / 100,000 operations).
- **`SET autocommit = 0` is a real multi-statement transaction:** COMMIT keeps, ROLLBACK discards.
- A statement that writes both relational and kv/entity/collection tables is refused.
- **Every SQL surface resolves each table once per statement:** MySQL wire, PostgreSQL wire, query service and embedded driver.
- **Embedded driver:** within a `sql.Tx` or `sql.Conn`, running a statement while a query's rows are open is refused, as the network MySQL/PostgreSQL drivers do. Ping stays valid. A reset with any statement still open discards the connection instead of handing on its transaction and locks.
- **Row locks:** one lease per connection, with a revoke fallback that deletes records. A fresh owner on a dead lease retries once.

### Changed

- **Behaviour: an autocommit SQL statement is atomic.** A statement outside
  an explicit transaction now commits all of its rows or none, as in InnoDB
  and PostgreSQL. A three-row `INSERT` whose third row is a duplicate leaves
  the first two absent; previously each row was its own commit and they
  stayed. This holds on the MySQL and PostgreSQL wires, the ad-hoc query
  service (REST, gRPC, Unix, QUIC, MCP, embedded) and the embedded driver.
  Reads, commit preconditions and duplicate reporting (1062, including a key
  taken concurrently before the commit) are unchanged; `qrdb_isolation` does
  not apply to an autocommit statement. A statement that writes two
  databases is refused. The statement's row-lock release is folded into its
  commit, so a pessimistic single-row `UPDATE`/`DELETE` is two Raft
  proposals instead of three. The query service and the PostgreSQL wire now
  report an error raised when a result is closed -- the commit -- instead of
  dropping it.
- **BREAKING (behaviour): an autocommit SQL statement is bounded like a
  transaction -- 8 MiB and 100,000 buffered operations.** A bulk
  `INSERT`, `UPDATE` or `DELETE` outside a transaction previously
  committed row by row and had no statement-wide limit; one that exceeds
  either bound now fails with `provider.WriteSetLimitError` and writes
  nothing. Split such a statement into batches below the bounds.
- **BREAKING (behaviour): a SQL statement writing a relational table and a
  `kv`, entity or collection table is refused**
  (`provider.MixedTransactionalWriteError`) before it writes either. The
  relational half is now atomic and the other applies at once, so such a
  statement could fail with its non-transactional half already committed.
- **BREAKING (behaviour): within a `sql.Tx` or `sql.Conn` on the embedded
  driver, running a statement while a query's rows are open is refused**
  (`provider.CommandInProgressError`), as the network MySQL and PostgreSQL
  drivers refuse it; read or close the rows first. In 0.27.0-alpha such a
  statement ran; each statement is now a command on the connection's session
  (see the per-statement resolution entry below), and one begun inside
  another would discard the open statement's pending writes. `Ping` on such
  a connection succeeds without running a statement; a pooled connection
  reset with a statement still open is reported bad and discarded, so its
  transaction, write-set and row locks never reach the next caller. New:
  `provider.QRDBSession.CommandOpen`.
- **Every SQL surface resolves each table, view and servicer once per
  statement.** In 0.27.0-alpha only the MySQL wire delivered the statement
  boundaries this memoization needs; the PostgreSQL wire (each executed
  statement, and the describe of each parameterized statement at Parse),
  the ad-hoc query service (each request) and the embedded driver (`Exec`,
  `Prepare`, and a `Query` until its rows are closed) now bracket each
  statement as a command too.
- **The SQL point-latency benchmark target is `make bench-sqlpk`** (was
  `make bench-sql-pk` in 0.27.0-alpha), matching its package,
  `test/performance/sqlpk`.

### Removed

- **The row-by-row relational write path.** Every relational write is part
  of a transaction (explicit, or the autocommit statement's implicit one).
  A write on a `*sql.Context` without a QRDB session -- which no SQL surface
  creates -- is refused with `provider.TransactionSessionRequiredError`.
- **BREAKING (Go API): `rowlock.NewOwner`.** An owner is made from the
  session whose lease it uses, `rowlock.NewSession().NewOwner()`; nothing
  outside the package's own tests used an owner with a private lease.

### Fixed

- **`SET autocommit = 0` starts a real transaction.** go-mysql-server marks
  only `BEGIN`/`START TRANSACTION` as an explicit transaction, so with
  autocommit off each relational write committed at once and `ROLLBACK`
  undid nothing. A session with autocommit off now runs one transaction
  until `COMMIT` or `ROLLBACK`, as InnoDB does, and a write to a `kv`,
  entity or collection table inside it is refused with
  `NonTransactionalTableError`, as inside `BEGIN`.
- **Row-lock lease edge cases.** A transaction's first lock request on a
  connection whose lease had ended unnoticed (expired or swept between
  keepalives) failed with 1213 although it held nothing; it now withdraws,
  and retries once on a fresh lease. A lease the session retires (a failed
  release, `Close`, idleness) is marked lost before it is detached, so another
  owner on it can no longer believe it holds a lock the revoke deleted. An
  owner released while its enqueue was in flight deleted nothing and left the
  record on the connection's lease; it now deletes it and fails with
  `OwnerReleasedError`. A lease grant no longer holds the session's lock, and
  of two concurrent grants the loser is revoked. A connection's end revokes
  its lease first and deletes no record one by one -- unless that revoke
  fails, in which case each transaction deletes its own records rather than
  leave them until the lease expires.

## [0.27.0-alpha] - 2026-10-02

Standalone parity (#582) and durable leases. Highlights:

- **Standalone mode serves watch, leases, locks and MVCC** with the same API and semantics as raft mode, through the same engine and the same tenant/database scoping, on all six transports and the SDK. File and objstore backends serve watch and plain transactions; MVCC, leases, locks and revision compares are refused with a typed error there.
- **Leases are durable in both modes.** Lease records and the ID sequence are written in the same apply batch. They are rebuilt on open and snapshot restore, and every recovered lease gets one full TTL. This fixes raft-mode bugs where a restart left leased keys (including lock records) stuck forever, reused lease IDs, and diverged replicas.
- A put naming an unknown lease is refused at apply. A re-put moves a key to its new lease. Keys with a leading `0x00` byte are reserved and refused.

### Added

- **Standalone mode serves watch, lease, lock and MVCC.** A standalone node (the
  daemon's `mode: standalone`, `sdk/go/node.NewStandaloneNode`, and the flat
  `embedded.NewServer`) serves the same API as a raft node, with the same
  semantics and errors, on REST, gRPC, Unix, QUIC, MCP and the embedded
  transport: prefix watch with revision resume and cancellation, lease grant,
  keep-alive, revoke and expiry, locks, and MVCC revisions and historical
  reads. They used to be refused with `requires mode: raft`, which pushed
  hosts onto in-process fallbacks and one-member raft clusters. The families
  are composed over the same engine, lessor and watch hub as raft mode's MVCC
  state machine and resolved through the same per-(tenant, database) registry,
  so tenant confinement is identical. A standalone node runs the lease-expiry
  sweeper itself (`lifecycle.StartLocalLeaseExpiry`), and its leases survive a
  restart like raft mode's. The file and objstore backends keep no revisions
  or leases: they serve watch and value/version transactions, and refuse MVCC,
  lease, lock, revision compares and any lease-carrying write per call with
  `CapabilityUnsupportedError`; an MCP watch there ends with
  `WatchStartRevisionUnsupportedError`. The capability report shows `mvcc`,
  `watch`, `lease` and `lock` true in both modes, and `mvcc.enabled`,
  `watch.enabled` and `lease.enabled` are valid under standalone. The etcd
  listener, cluster membership, rebalance and shard backup stay raft-only.
  `sdk/go/conformance` runs one watch, lease and lock suite against standalone
  and raft nodes on all six transports.

### Fixed

- **The flat embedded server delivers watches.** `embedded.NewServer` built its
  engine without a watch hub, so a watch opened through it never received an
  event; it also composed no watch or lock service and ran no lease expiry.
- **A transaction op naming a lease is refused on the file and objstore
  engines.** The plain KV engine keeps no leases, but its transaction handler
  stored a leased put as an ordinary key that never expired. The whole op set
  is now refused with `CapabilityUnsupportedError` and writes nothing. Plain
  value/version transactions are still served there.
- **A lease revoke whose key delete fails halts the apply on the MVCC engine**,
  as on the Pebble state machine. It was logged and skipped, leaving the key
  with nothing to expire it on that replica alone.
- **A failed standalone apply no longer leaves the lessor ahead of the
  store.** A standalone node keeps serving after a failed apply, so
  `MVCCEngine.Update` now re-reads the committed lease table into the lessor
  when an apply fails (`engine.LessorResyncError` when that read fails too).
- **The Pebble state machine refuses user keys in its reserved `0x00`
  namespace** (`statemachine.ReservedKeyError`), on every write path. A user
  write could overwrite the lease table or the last-applied index.
- **The `qrdb` GUI's embedded server expires its leases.** It gave its MVCC
  engine an anonymous lessor nothing swept, and composed no lock service; it
  now composes the engine through `lifecycle.NewLocalEngine`, sweeps it, and
  serves locks.
- **The lease-expiry sweeper is stopped before the backend closes.**
  `lifecycle.StartLocalLeaseExpiry` returns a stop function that waits for
  the sweeper to exit; the daemon, `node.Node`, `embedded.Server` and the GUI
  binding call it before closing the backend it revokes through.
- **The embedded transport keeps a refusal's type under a lock or an MVCC
  read.** A lease refusal underneath a lock, and `service.MVCCBackendRequiredError`,
  reached embedded callers as untyped errors; they are now
  `transport.CapabilityUnsupportedError`, as on every network transport.

- **Revoking or expiring a lease no longer deletes a key that was written again
  under another lease, or with none.** A key stayed in every lease any of its
  values had named. The MVCC put paths attached the key to the new lease
  without detaching it from the old one, and on the Pebble state machine the
  delete-path detach never ran, because the previous value it read carried no
  lease. Revoking or expiring the old lease then deleted the newer value. A
  service that re-registered under a fresh lease while its previous
  registration's lease was still alive lost the new registration when the old
  lease expired. A key now belongs to the lease its latest write named. A put
  naming another lease moves it, a put naming none (including a plain base-KV
  put) releases it, and every delete releases it. The binding is written in
  the same apply batch as the value, on puts, deletes, batches and
  transactions, on both the Pebble and the MVCC state machine.
  Compare-and-swap still keeps the key's lease. `Revoke` returns its keys in
  key order, so every replica emits the delete events in the same order. On
  the MVCC engine a revoke now retracts the deleted keys' secondary-index
  entries, as any other delete does.
- **Leases survive a restart and a snapshot recovery.** The lessor lived only
  in memory. A restarted node, or a replica recovering from a snapshot, came
  back with no leases: their keys never expired, keep-alive answered
  not-found, and the next grant reused ID 1, so lease IDs diverged across
  replicas. The lease table (each lease's granted TTL, every key binding, and
  the lease-ID sequence) is now persisted under the reserved
  `\x00qrdb.lease/` prefix, carried in snapshots and covered by the replica
  hash. It is restored when a state machine opens or recovers. A restored
  lease starts a full TTL. Leases granted before this release are not in the
  table and do not survive the upgrade's restart, which is what happened to
  them on every restart before.
- **A write naming a lease that no longer exists is refused at apply.** The
  namespaced view checks lease ownership before proposing. A revoke or expiry
  committed between that check and the write let the write through as a key
  bound to no lease, which never expires. The state machine now refuses it with
  the same `service.LeaseNotFoundError` (`NOT_FOUND`) the pre-proposal check
  returns, and a batch or transaction naming one stages none of its
  operations. `pkg/rowlock` relies on that type to report a lost lock. A write naming a lease
  on a state machine built without a lessor is refused as unimplemented
  instead of being written lease-less.
- **`pkg/rowlock`: the deadlock tests no longer race their own requests.** A
  test waited only for the first request's record to be queued, not for that
  request to finish its deadlock check. When the second request completed the
  cycle first, the first request found the cycle too and became the victim,
  failing about one run in thirty under `-race`. The tests now wait until the
  earlier requests are blocked in `wait`, which is entered only after the
  check.

### Changed — BREAKING

- **`embedded.WithStandaloneMode` is removed**, with the registry gate
  (`raftOnlyGatedRegistry`) it mirrored: there is no longer a family a
  standalone node withholds by design. Drop the option. `server.WithStandaloneMode`
  remains and now refuses only the etcd listener.
- **`lifecycle.NewLocalEngine` takes the node's lessor**
  (`NewLocalEngine(backend, stg, pdb, hub, lr)`). It used to build a private
  lessor that nothing swept; pass the lessor the expiry sweeper runs over.

- **`pkg/lease`: mutations take the apply batch.** `Lessor.Grant(w, ttl)` and
  `Lessor.Revoke(w, id)` take a `lease.Writer`. `AttachKey` and `DetachKey` are
  replaced by `Bind(w, key, leaseID)`, where `lease.NoLease` releases the key,
  and `Unbind(w, key)`. `GrantWithID` is removed, since IDs now come from the
  persisted sequence. New: `Restore`, `Has`, `LeaseOf`, `IsTableKey`,
  `TablePrefix`, and the typed `TableWriteError`, `TableReadError` and
  `CorruptTableError`. A state machine opening over a corrupt lease table
  fails with `CorruptTableError` rather than guessing which keys a lease owns.

## [0.26.0-alpha] - 2026-10-01

Merge train 5. Highlights:

- **DROP DATABASE drop fence (#482).** A database drop is atomic with every write: a write that races the drop is refused, not half-applied. Guarded writes travel behind a cluster feature gate and are used only once every node in the cluster supports them. Another tenant's dropping database answers as missing.
- **SQL primary-key statements no longer scale with table size.** The planner now uses a per-table row-count estimate (COUNT(*) stays exact), so a PK point read at 64k rows drops from about 127 ms to about 1.6 ms. Pessimistic row locking holds one lock lease per connection instead of one per statement.
- **Watch** accepts a start revision above current and is no longer cancelled by a racing compaction (#514). KVStore comparability (#66) and the seal guarded-delete arm (#552) are documented.

### Added

- **Cluster feature gate (`pkg/cluster/featuregate`).** A command only newer binaries
  understand is sent anywhere in the cluster only once every node in it -- not just a given
  Raft group's current members -- runs a binary that advertises the feature: a later
  membership change can bring an old binary into a group whose current members all advertise
  today, and go-quicraft catches a rejoining or newly added replica up by replaying the
  group's committed log, which would already hold the command. Each node advertises its
  features in a lease-backed record in the system store
  (`__sys:cluster/features/nodes/{replicaID}`), so a stopped or rolled-back node stops
  counting when its lease expires; a dedicated shard matches its members to records by raft
  address. Once every node advertises a feature it is activated permanently (witnesses
  included), and a node whose binary lacks an activated feature refuses to start with
  `featuregate.UnsupportedFeatureError`. Separately, and before activation too, a replica is
  added to a group only on a node that advertises every feature that group's CURRENT members
  already use -- member add, dynamic membership, scale-up, rebalance and the placement
  scheduler all ask first and are refused with `featuregate.UnsupportedReplicaError` (REST
  409, gRPC/QUIC `FAILED_PRECONDITION`). `qrdbd` withdraws its advertisement on a graceful
  stop; after a crash it lapses with the lease. **After activation, no node may be started or
  added on an older release.** A brand-new `qrdbd` node advertises only once it is a member,
  so after activation it cannot yet be added with `member add`. Guarded writes are the first
  gated feature; standalone nodes and hosts with no transport use them from the start, and a
  single-node cluster activates them at once. A node embedded through the Go SDK cannot refuse
  to start after it is built; it logs the refusal and reports its health as `degraded`. New:
  `service.FeatureGatedHost` and `service.ReplicaAdmitter`,
  `raftbackend.WithFeatureGate` / `NewFeatureGate`, `server.WithFeatureGate` and
  `TenantInfraConfig.FeatureGate`, `placement.WithReplicaFilter`,
  `sdk.Client.StartFeatureGate` and `NewMembershipStoreAt`, and `cluster.Member.Features`.
  **Downstream composers:** a `raftbackend.HostAdapter` over a host with a transport and no
  `WithFeatureGate` now proposes every write UNGUARDED -- no feature is enabled without a
  gate -- so a composer that builds its own adapters must attach the node's gate to each of
  them, or its drop fence falls back to the check made when the database is resolved.
- **Guarded write commands.** A new Raft opcode, `protocol.OpGuarded`, wraps any write
  command with compares (the transaction compare encoding; `version` and `value` targets)
  that the state machine evaluates at the entry's own log position. It is served by the four
  `pkg/statemachine` state machines, `PebbleStateMachine`, `MVCCStateMachine`, `KVEngine` and
  `MVCCEngine`; `SimpleStateMachine` refuses it. A command without the wrapper is
  byte-for-byte unchanged and applies unconditionally. See `docs/databases/engineering.md`.
  A refused guard travels the same typed-kind encoding as every other state-machine
  refusal (`protocol.AppErrorKind`): a new `AppErrorGuardRefused` kind carries the key of
  the compare that failed, decoded ahead of the generic kind dispatch so a refused drop
  fence renders as `database.DroppingError` (409 / `ABORTED`) and any other refused guard
  as `service.WriteGuardRefusedError` (409 / `ABORTED`) through the one `pkg/api/errclass`
  table every transport already renders from -- neither type needed its own per-transport
  mapper. `service.WriteGuardError` (a guard that could not be built -- an unencodable
  compare, a command already guarded) is 400 / `INVALID_ARGUMENT`, the same shape as
  `EncodeRequestError`: raised before anything is proposed.
- `embedded.WithServiceRegistry`: the embedded transport resolves collection calls and SQL
  queries that name a database through the same per-(tenant, database) registry as the
  network transports when the composition configures one. `sdk/go/node` wires its server's
  registry in.

### Changed

- **Breaking:** `TimeSeriesServicer.SetRetentionPolicy` takes a `context.Context` first, so
  a retention change carries the request's database scope like every other write.
- **Breaking:** `database.KeyspaceReclaimer` gains `FenceDatabaseKeyspace` and
  `UnfenceDatabaseKeyspace(ctx, tenantID, database, incarnation)`; every implementation must
  add them.
- **Breaking:** `service.StoreResolver` gains `FenceTarget`, returning a
  `service.FenceStore`; every implementation must add it.
- **Breaking:** `service.ReclaimKV` gains `Put`; every implementation must add it.
- **Embedded collections move to the per-database keyspace.** When a service registry is
  configured, collection calls through the embedded transport that name a database now read
  and write that database's `<tenant>/<db>/` keyspace -- the one the network transports use
  and `DROP DATABASE` reclaims -- instead of the root-keyed layout the embedded transport
  wrote before. Collections an embedder wrote through the old path are not visible through
  the new one; migrate them as for the earlier collection namespace change (export with the
  previous binary, re-import).
- Database metadata records an `incarnation` (see Fixed). Metadata without one reopens with
  an empty incarnation and keeps working.

- **SQL primary-key point statements no longer cost time proportional to the
  table.** go-mysql-server asks a relational table for its row count several
  times while it plans every statement, and `RowCount` answered with an exact,
  linearizable count of the whole table: about 1.7 µs per row, so a
  `SELECT`, `UPDATE` or `DELETE` by primary key took about 110 ms at 64,000
  rows. It now answers from a per-table-generation estimate, counted with a
  local read and refreshed in the background every ten seconds, and reports
  it as inexact. Over the MySQL wire, at 64,000 rows, the median point
  `SELECT` went from 127 ms to 1.6 ms and `UPDATE` from 152 ms to 3.4 ms; see
  `test/performance/SQL_PK_LATENCY.md`. `information_schema.tables`'
  `TABLE_ROWS` and `DATA_LENGTH` report the same estimate, so they may be up
  to about ten seconds stale. `SELECT COUNT(*)` is unaffected and always
  exact: it is no longer answered from the row count, and counts the rows
  instead.
- **A SQL statement resolves each table, view and servicer once.** Table
  lookups within one statement — and every table `information_schema` lists —
  each re-resolved the session's tenant with a linearizable read for each of
  five servicers and re-read the table definition. Statements that cannot
  change a definition (`SELECT`, `INSERT`, `UPDATE`, `DELETE`, `REPLACE`,
  `SHOW`, `DESCRIBE`, `EXPLAIN`) now memoize them for the statement, on
  surfaces that deliver statement boundaries (the MySQL wire). DDL is seen by
  the next statement, as before.
- **Pessimistic autocommit statements share their connection's lock lease.**
  Every locking transaction granted a lease before its first lock and revoked
  it after its last, two Raft proposals on every pessimistic autocommit
  `UPDATE`, `DELETE` and `INSERT`. A connection now holds one lease per
  database it locks in, renewed while it lives and revoked when it ends or
  after ten seconds unused; each transaction deletes its own lock records
  when it ends. A crashed connection's locks still expire with its lease, a
  deadlock victim is still rolled back, and an autocommit statement still
  releases its locks when it ends. New in `pkg/rowlock`: `Session`,
  `NewSession`, `Session.NewOwner`, `Session.Close`.

### Fixed

- **Dynamic membership works on the default `pebble` state machine.** A lease-attached put
  (`MVCCKVService.PutWithLease` / `PutWithLeaseAndMode`) applied on a state machine without
  MVCC revision history and then failed its revision read-back, so `--dynamic-membership`
  on `--sm pebble` failed to register each node and the node did not start. The composer now
  declares a state machine without revision history (`service.WithoutRevisionHistory`,
  `lifecycle.Infra.NoRevisionHistory`, from the state machine's descriptor), and the put
  there reports the key, value and lease it wrote with all revisions `0`, without a
  read-back; on an MVCC state machine it still returns the write's full revision metadata.
  The etcd API refuses to start over a state machine without revision history
  (`etcd.RevisionHistoryRequiredError`), and its `Put` answers `FAILED_PRECONDITION` there
  rather than revision 0.

- **DROP DATABASE is atomic with every write to the database.** Each database now has a
  fence marker in the Raft group holding its data (the system shard, a volume tenant's Host
  shard, or a dedicated-raft database's own shard). While the database is live the marker
  holds the database's incarnation, and every write its views propose carries "the marker
  holds my incarnation" in the same Raft command. A drop fences the database locally, so no
  new handle resolves it, and sets the marker to a tombstone before reclaiming anything. A
  write resolved and proposed before the drop but applied after the fence is refused at
  apply time with `database.DroppingError` (REST 409 / gRPC and QUIC `ABORTED`) instead of
  landing in a keyspace being reclaimed; a write applied before the fence is reclaimed with
  the rest. Because handles are scoped to an incarnation, a handle from before a drop is
  still refused after the name is created again. A failed drop always restores the marker,
  under a context the failed request's cancellation cannot reach, and stays fenced if the
  store cannot be reached. A dedicated-raft database is additionally fenced in its replicated
  placement entry, cleared on a failed drop only once the store marker is confirmed
  restored; a shard cached for a dropped incarnation is evicted and its directory purged
  rather than served, and a never-cached dedicated-raft database can be dropped. Shard IDs
  are never issued twice on a node, dropped ones included (a persisted high-water mark), so
  a re-created dedicated-raft database always gets a new shard. The marker is claimed only
  from absent or the tombstone: creating a name already live on another node is refused
  with `database.AlreadyExistsError` (`Elsewhere` set) rather than taking it over. A name
  this node dropped is refused with `database.ReclaimerRequiredError` by a manager with no
  keyspace reclaimer, and `qrdbd` now provisions its configured databases only after the
  data plane has attached one, so a restart can no longer re-create a dropped name that
  refuses every write. A placement read failure on the fence path surfaces as itself rather
  than as an unknown database, and a collection service cached for a dropped database is
  never reused by the name's next incarnation.
  Vector writes, which are node-local rather than Raft proposals, keep a check made
  immediately before the write.
- **Guarded writes never reach a mixed-version cluster.** A replica running a binary that
  predates guarded writes would refuse one and write nothing while its peers applied it. The
  new cluster feature gate (below) sends a guarded write anywhere only once the feature is
  activated cluster-wide -- every node, not just a given group's current members, so a later
  membership change can never introduce an old binary that the group's log already has the
  command in; until then every group's writes are proposed unguarded and the drop fence is
  checked when the database is resolved, as before guarded writes.
- **The default database cannot be dropped.** Every drop entry point refuses `system` with
  `database.DefaultDatabaseDropError` (REST 400 / gRPC and QUIC `INVALID_ARGUMENT`) before
  touching anything.
- A second concurrent drop of one database is `ABORTED` on QUIC as on gRPC and REST, rather
  than an internal error.
- `SimpleStateMachine` reports a command it cannot decode as a refusal in the result instead
  of the success value, so such a write is no longer acknowledged unwritten.

- **A watch opened at a start revision above current is accepted and stays
  open.** It opens as a live watch that withholds every change below the
  requested revision and delivers from that revision on, matching etcd. It
  was previously cancelled in-band with a compacted error when compaction
  raced past its start revision before the replay goroutine checked, even
  though it never needed a historical scan. The hub now reads the replay
  boundary in the same critical section that registers the watcher, and
  applies the compaction check only to a replay it actually attempted. The
  watch docs listed a 400 / `INVALID_ARGUMENT` refusal for this case; no
  transport ever enforced it, and the docs now describe the accepted
  behaviour.

- **A serialization failure or lock-wait timeout raised during an index
  lookup kept its error code.** `RelationalTable.LookupPartitions` wrapped
  every error in `IndexLookupError`, which hid go-mysql-server's
  `ErrLockDeadlock` (MySQL 1213, "retry") and MySQL's own 1205 / 3572 from
  the client. It was masked until now because planning's exact row count
  usually raised the same stale-definition error first.

### Removed

- `provider.WriteSet.HasBufferedWrites`. Its one caller was `RowCount`, which
  needed it to stop reporting a committed-only count as exact inside a
  transaction; `RowCount` is never exact now.

## [0.25.2-alpha] - 2026-10-01

Security patch. It closes a family of privilege escalations in which a tenant-scoped identity could act on node-wide or cluster-wide resources. The cause was a permission check that compared the caller's tenant with itself on routes that name no tenant.

Node seal and ceremony operations, cluster membership, shard control, rebalance, node unseal shares and backup/restore are now system-tenant only, on every transport. See the BREAKING entries below.

### Security

- **BREAKING: a tenant's Admin or SO can no longer change cluster
  membership.** `admin:add_member`, `admin:remove_member`, `admin:drain` and
  `admin:snapshot` were ScopeSelf, and the built-in Admin and SO roles grant
  them in every tenant. Their routes (`POST /membership/add`,
  `/membership/remove`, `/drain`, `/snapshot`, and the same methods and tools
  on gRPC, Unix, QUIC and MCP) address shards by id and name no tenant, and
  the admin service does no tenant check, so any tenant's Admin could remove
  replicas from the system shard and break quorum for every tenant, or drain
  the node. They are now `ScopeCross`, with `admin:start_shard`,
  `admin:stop_shard`, `admin:shard_state`, `admin:topology` (which enumerates
  every tenant's raft group and node address) and every `rebalance:*`
  permission (`rebalance:execute` moves shards between nodes); no built-in role
  below Owner carries those, but a tenant's SO could grant them to itself
  through a custom role. `admin:peers`, `admin:status`, `admin:health` and
  `admin:capabilities` stay ScopeSelf: the etcd listener's tenant-facing
  `Cluster.MemberList` is authorized with `admin:peers` against the
  database's owning tenant. A tenant tool that drove these operations must run
  as the `system` tenant.
- **BREAKING: shard backup and restore are system-tenant only.** `backup:*`
  was ScopeSelf on routes that name no tenant. A backup is of shards, not of
  a tenant -- the backup service has no tenant model -- so it holds every
  tenant's data on them, and a restore overwrites every tenant's state. No
  built-in role below Owner carries `backup:*`, but a tenant's SO could grant
  it to itself through a custom role and take, or roll back, every tenant's
  data. Every `backup:*` permission is now `ScopeCross`, re-exported as
  `sdk.PermissionBackup{Create,Delete,List,Read,Restore,Verify}`.
- **BREAKING: a tenant's Custodian can no longer submit shares to the node
  barrier.** `barrier:unseal-share` was ScopeSelf and every tenant's built-in
  Custodian role grants it, so a tenant custodian could submit a wrong share
  into an in-progress node unseal and poison the accumulation. The node
  barrier's custodians are the `system` tenant's, enrolled by its Security
  Officer; a tenant custodian's shares go to its own barrier's identity-gated
  routes. It is now `ScopeCross`. Anonymous unseal-share on the boot-time
  sealed-window listener runs no RBAC and is unchanged.
- **BREAKING: a tenant can no longer enrol or list the node barrier's
  Security Officers.** `seal:officer_register` (`POST /v1/barrier/officers`
  and `POST /v1/barrier/recovery/officers`) and `seal:officer_list`
  (`GET /v1/barrier/officers`, `GET /v1/barrier/officers/claim-status`) were
  ScopeSelf. None of these routes names a tenant, so the check compared the
  caller's tenant with itself and admitted every holder: a tenant's SO, which
  holds both through its own tenant's built-in SO role, or a tenant Admin
  granted them by a custom role, could plant officers in the node barrier's
  unseal and recovery domains, and read back the node's officer names and
  certificate thumbprints. Every rekey, revocation and recovery-key generation
  sends a share to every registered officer, so M planted officers could
  reconstruct the node unseal key or mint a root token through the quorum.
  Both permissions are now `ScopeCross`, refused for every non-system identity
  on REST, gRPC, Unix, QUIC, MCP and the embedded `RBACClient`. Anonymous
  first-boot registration on the boot-time sealed-window listener runs no RBAC
  and is unchanged. A tenant role that relied on the gap must move to the
  `system` tenant, or manage its own tenant's officers through
  `tenant_barrier:officer_register` / `tenant_barrier:officer_list`.
- **BREAKING: the rest of the node barrier's lifecycle is system-tenant only
  too.** The same vacuous ScopeSelf covered `seal:bootstrap` (initialize and
  root-token generation), `seal:unseal`, `seal:recover_with_keys`,
  `seal:submit_seal_vote`, `seal:enable_encryption` -- whose body-supplied
  `tenant_id` let a tenant-local holder migrate the node barrier or another
  tenant's; migrating any barrier is a platform-operator act with no
  tenant-side route -- and the node key ceremony's `ceremony:initialize`,
  `ceremony:enroll_custodian` (custodians and external officers, who receive
  node shares), `ceremony:revoke_custodian` and `ceremony:sign_csr` (which
  signs with the cluster CA). All are now `ScopeCross`. The built-in SO, Admin
  and Operator roles of a non-system tenant keep the grants and are refused
  them; the same roles in the `system` tenant are unaffected. `seal:status`,
  `ceremony:status` and the share-claim verbs stay ScopeSelf.
- The Go SDK re-exports every new cross-scope permission
  (`sdk.PermissionSealOfficerRegister`, `sdk.PermissionSealOfficerList`,
  `sdk.PermissionSealBootstrap`, `sdk.PermissionSealUnseal`,
  `sdk.PermissionSealRecoverWithKeys`, `sdk.PermissionSealEnableEncryption`,
  `sdk.PermissionSealSubmitSealVote`, `sdk.PermissionCeremonyInitialize`,
  `sdk.PermissionCeremonyEnrollCustodian`,
  `sdk.PermissionCeremonyRevokeCustodian`, `sdk.PermissionCeremonySignCSR`,
  `sdk.PermissionBarrierUnsealShare`, `sdk.PermissionAdminDrain`,
  `sdk.PermissionAdminSnapshot`, `sdk.PermissionAdminStartShard`,
  `sdk.PermissionAdminStopShard`, `sdk.PermissionAdminShardState`,
  `sdk.PermissionAdminTopology` and `sdk.PermissionRebalance{Execute,Plan,Report,Stats}`;
  `sdk.PermissionAdminAddMember` and `sdk.PermissionAdminRemoveMember` already
  existed and are now cross-scope).

## [0.25.1-alpha] - 2026-10-01

Fixes clustered qrdbd under system barrier protection `none`, which could not start on go-quicraft 0.15; encrypts the SDK clustered node's Raft WAL; and lets a host supply the `/v1/ca/bootstrap` bundle instead of colliding with the route. BREAKING (on disk): a sealed SDK clustered node refuses a WAL written by an earlier release; start it from an empty WAL directory.

### Changed — BREAKING

- **The SDK clustered node encrypts its Raft WAL, through the same code path
  as qrdbd.** `node.NewClusteredNode` built its host `WithoutEncryption`
  whatever the seal, so every committed key and value sat in cleartext in the
  WAL until compaction. qrdbd and the SDK node now both configure the WAL with
  `database.NewRaftWAL`:
  - seal disabled: `WithoutEncryption`;
  - a single member, or system protection `none`: go-quicraft's own WAL
    `WithBarrier`, which encrypts once encryption is enabled;
  - a cluster whose system barrier encrypts: the multiplex (shared
    system-barrier WAL plus per-tenant dedicated WALs).

  The SDK node now hands the multiplex to its tenant infrastructure, so
  `dedicatedWal` databases get their own tenant-barrier WAL as on qrdbd. The
  WAL directory records its format in `.qrdb-wal-format`. A WAL in the other
  format, including an SDK node's unmarked pre-0.25.1 plaintext WAL under a
  barrier, is refused with `*database.RaftWALFormatError` and never converted.
  Start such a node from an empty WAL directory; a replica re-syncs from its
  peers. qrdbd's own unmarked WALs are adopted, because its format did not
  change.

### Added

- **`sdk.WithBootstrapCABundle` (`server.WithBootstrapCABundle`): a host
  supplies the `/v1/ca/bootstrap` bundle and qrdb serves the one route.** The
  provider, `func(ctx) ([]byte, error)`, is read per request, and only its
  CERTIFICATE blocks are served. Configuring it together with qrdb's cluster
  CA (`WithCABootstrap`) refuses startup with `*sdk.CABootstrapConflictError`.

### Fixed

- **`/v1/ca/bootstrap` is no longer claimed when nothing is served there.**
  The REST server reserved the path whether or not a bundle was configured, so
  a host serving its own first-contact bundle there (go-xkms's xkmsd)
  panicked at boot with a route collision. With no source configured the path
  is not registered at all.
- **A cluster booting under system protection `none` starts again.**
  go-quicraft 0.15 accepts a caller-supplied LogDB beside `WithBarrier` only
  if it declares itself encrypted at rest. The multiplex's shared waldb
  truthfully reports unprotected while the system barrier is in plaintext
  mode, so every node of such a cluster was refused at host creation. Such a
  node now takes go-quicraft's own WAL under the barrier, which is the same
  directory and format. Dedicated-WAL shards use the shared WAL until the
  next boot after encryption is enabled. `TestClusterEnableEncryption_SystemScope_RF3`
  and `_TenantScope_RF3` pass again.
- **The auth CLI integration test assigns the auditor role by id.** `--roles`
  takes role ids. The fixture passed the name `auditor`, which role-reference
  validation correctly refuses. This was a test defect, not replication: the
  role was seeded under its derived id.

## [0.25.0-alpha] - 2026-10-01

Adds the per-tenant DEK keyring on every transport, bearer authenticators on gRPC and Unix, and a sealed-window surface hosts mount. BREAKING: the unused cluster-DEK and tenant-DEK-manager APIs are removed (see Changed — BREAKING), and `qrdbd`'s `<data-dir>/dek` directory is no longer read and may be deleted.

### Changed — BREAKING

- **`transport.DEKClient` is the tenant keyring and nothing else.** The
  cluster-DEK verbs (`DEKInitialize`, `DEKGetStatus`, `DEKRotate`,
  `DEKGetCurrent`, `DEKSyncToNode`) and `TenantDEKGetOrCreate`,
  `TenantDEKDelete`, `TenantDEKList` and `TenantDEKGetStatus` are removed with
  their request/response types; `TenantDEKRotate` now returns the keyring
  status. None of them worked against a server: no transport served them, the
  gRPC, QUIC and Unix clients were stubs, and the REST and MCP clients called
  routes and tools that did not exist. `service.DEKServicer` is rewritten to
  match.

- **The old tenant-DEK hierarchy is deleted.** `dek.ClusterDEKManager` held its
  key in process memory only, was never initialised by the daemon, was never
  persisted and discarded the old key on rotation; `dek.TenantDEKManager`
  stored keys wrapped by it in a node-local Pebble directory, so a restart or a
  second node could read none of them. Removed with them:
  `seal.TenantKeyManager` (no production caller) and `sdk.TenantKeyManager`,
  the `sdk/go/dek` package, `server.WithTenantDEKManager`,
  `lifecycle.Infra.TenantDEKManager`, and `qrdbd`'s `<data-dir>/dek` store,
  which is no longer opened and may be deleted.

- **Tenant teardown has no DEK-shred step.** `tenant.DEKShredder`,
  `TeardownWiring.SetDEKShredder`, `tenant.WithDEKShredder`,
  `tenant.DEKShredError` and their `sdk` re-exports are removed. The keyring is
  a record in the tenant's own store, so it is unusable from the fence on and
  is removed by the barrier destroy and the keyspace reclaim. The order is now
  fence, destroy barrier, reclaim keyspace; persisting the retirement before
  anything irreversible (#483) is unchanged.

### Added

- **Per-tenant data encryption keyring, served on every transport.** A tenant
  can now encrypt and decrypt its own data under a versioned AES-256-GCM key
  that never leaves the server — `TenantDEKEncrypt`, `TenantDEKDecrypt`,
  `TenantDEKRewrap`, `TenantDEKRotate`, `TenantDEKRetire` and
  `TenantDEKStatus` on REST (`/v1/dek/*`), gRPC and Unix, QUIC (`dek.*`), MCP
  (`qrdb_dek_*`), the embedded transport and `Client.DEK()`, plus a
  `qrdb dek` command group. See `docs/tenant-dek/`.

  The keyring is one record in the tenant's own resolved store, so it is
  wrapped by the tenant's barrier (a dedicated-isolation tenant) or the system
  barrier (a prefix tenant), replicated through the system shard, durable
  across restarts, unreachable while the tenant is sealed and gone with the
  tenant. Rotation keeps old versions decrypting; rewrap moves a ciphertext to
  the current version without returning its plaintext; retire destroys an old
  version and refuses the current one. Ciphertext carries its key version in
  an internal header and binds the header, the tenant and caller-supplied
  associated data into the AEAD.

  Every call acts on the caller's own tenant. A request naming another tenant
  is refused with PermissionDenied / 403, from the system tenant too.

  Six new `ScopeSelf` permissions: `dek:encrypt`, `dek:decrypt`, `dek:rewrap`
  (crypto-use, denied to the Security Officer), `dek:rotate`, `dek:retire`,
  `dek:status`. Built-in grants: User — encrypt, decrypt; Operator — encrypt,
  decrypt, rewrap, status; Admin — rotate, rewrap, retire, status.

  A tenant on barrier protection `none` keeps its keyring record without
  at-rest encryption, exactly as it keeps the rest of its data; encrypt and
  decrypt still work.

- **Embedder bearer authenticators work on gRPC and Unix, and may declare
  their login method unobservable.** The gRPC and Unix auth interceptors now
  carry the caller's `authorization` metadata into the same context carrier
  REST and MCP fill from the `Authorization` header
  (`auth.HTTPAuthorizationFromContext`), so an authenticator registered with
  `node.WithAuthenticators` sees the credential on every one of them. The
  carrier and the decline sentinel are re-exported as
  `sdk.HTTPAuthorizationFromContext` and `sdk.ErrUnauthenticated`, so such an
  authenticator is written against the SDK alone. An
  authenticator whose tokens are minted by the host after its own login
  ceremony implements the new `auth.UnobservableMethodAuthenticator`
  (`sdk.UnobservableMethodAuthenticator`). When it reports true, the chain
  skips the account's permitted-method check, as it does for a capability
  module's session token, and the host enforces the policy at token issue.
  Every other step still runs. Native QUIC frames carry no credential, so a
  bearer does not authenticate there; go-quicbaas's QUIC is HTTP/3 REST and
  carries the header.

- **Hosts mount qrdb's sealed surface instead of copying it.**
  `sdk.NewSealedWindowHandler(barrier, ceremony, credential, probe)` returns
  the whole sealed allowlist as an `http.Handler` behind qrdb's sealed gate:
  barrier status, initialize and unseal, Shamir and recovery unseal, officer
  registration and share claims, the ceremony verbs and the root token.
  `GET /health` answers the sealed envelope.
  `sdk.SealedWindowMCPTools(...)` returns the matching MCP tools, each already
  gated and error-coded. `sdk.NewSealedWindowListenerProbe(barrier, ceremony)`
  builds the one probe every mounted transport shares. qrdb's own boot-time
  listener is built from the same constructors
  (`rest.NewSealedWindowServer`, `mcp.NewSealedWindowServer`), so there is
  one implementation. `qrdb_health` answers `sealed` on a window with no
  admin service, as gRPC `Health` does. See docs/seal/sealed-window.md.

### Fixed

- **Every transport refuses a standalone node's raft-only families with one
  typed error.** MVCC, watch, lease and lock now answer
  `*transport.CapabilityUnsupportedError` naming `requires mode: raft` on
  REST, gRPC, Unix, QUIC, MCP and the embedded transport alike. Before this:
  the embedded transport answered `*embedded.NilServiceError` (the error
  reserved for a wiring defect; it still is, via the new
  `embedded.WithStandaloneMode` set by `node.NewStandaloneNode`); REST
  registered the lease and lock routes only when a service was wired and
  answered a router 404, now it attaches them and answers 501; MCP did not
  register the MVCC, watch, lease and lock tools and answered "tool not
  found", now it registers them and answers `UNIMPLEMENTED`. Watch refusals
  on gRPC, Unix, QUIC and MCP arrive as the stream's terminal response, whose
  `Err()` is the same typed error; MCP's watch poll no longer re-polls the
  refusal forever. `sdk/go/conformance/standalone_refusal_test.go` pins every
  transport.

- **`node.Client().Lock()` works on a raft node.** The lock service was never
  passed to the in-process transport, so the lock family answered a
  wiring-defect error in the one mode that serves it.

- **Deleting a dedicated-isolation tenant now reclaims its records in the
  shared store.** Its identity records live under
  `seal.TenantSharedNamespace(tenant)` (`__tiam:<tenant>/`), which
  `service.TenantKeyPrefixes` did not cover, so they survived the delete —
  as ciphertext under a destroyed barrier, or as plaintext under barrier
  protection `none`. The reclaim now covers the range.

- **`make fmt` and `make lint` pass together again.** Six test doubles
  returned two composite literals in one `return`. go1.27's `gofmt` and
  golangci-lint's gofmt indent that form oppositely, so the tree failed one
  check or the other. They now return named values, which both formatters
  accept. The change is to test code only.

- **A database-scoped embedded transport keeps the standalone refusal.**
  `Client.DatabaseScoped` on a standalone node dropped the standalone flag,
  so the raft-only families fell back to `NilServiceError` there.

## [0.24.0-alpha] - 2026-10-01

Merge train 3. This release contains BREAKING changes; see the "Changed — BREAKING" entries below before upgrading. Highlights:

- **Optimistic or pessimistic SQL concurrency.** A per-database `sql.concurrency` setting and a `SET qrdb_concurrency` session override choose between them. Row locks are replicated and lease-bound, and requests wait in FIFO order. Deadlock detection, lock-wait timeouts, `SELECT … FOR UPDATE` / `FOR SHARE` and `NOWAIT` follow InnoDB semantics, and a failed statement inside a transaction is undone as a whole. See docs/row-locking/.
- **Security hardening across every transport:**
  - per-database barriers only on the data path; sealed databases fail closed and their open streams end;
  - etcd confinement;
  - tenant-owned SQL CREATE/DROP;
  - RAG confinement;
  - `databases:create_raft` / `create_volume`;
  - lease ownership on writes;
  - the boot-time sealed window refuses requests after unseal;
  - 0-RTT refused on QUIC and HTTP/3;
  - the SO PIN is held in protected memory and sent only over TLS;
  - node-barrier SO operations are system-tenant only;
  - route checks read the same request values as the handlers.
- **Identity:** role bindings and attributes take effect, and there is one source of truth for factors.
- **SDK surface:**
  - auth-method tokens;
  - typed atomic-write refusal;
  - MCP tool-error contract;
  - per-node auth-method vocabulary;
  - the SO PIN set with `WithSOPIN` covers every SO-gated call on every transport.
- **Process hardening** against same-user memory reads at start.
- **SQL sequences:** an epoch fence for sequence blocks; MySQL `TRUNCATE` resets `AUTO_INCREMENT`.
- **Release governance:**
  - the Release workflow is the only tag creator;
  - VERSION is authoritative;
  - the public-releasability gate loads its patterns from outside the tree.
- **Test reliability:**
  - a cross-process port lock;
  - the bootstrap-admin guard race is fixed;
  - stale integration tests are corrected.

### Changed — BREAKING

- **New interface methods.** `service.DatabaseServicer` gains `UpdateSQL` and
  `UpdateSQLForTenant`, and the Go SDK's `transport.DatabaseClient` interface
  gains `UpdateDatabaseSQL` (which `transport.TenantDatabaseClient` forwards).
  Every
  implementation outside this repository — a composer's own service, a test
  double, a custom transport — must add them to keep compiling.
- **TypeScript SDK (`@qrdb/sdk`): the wire-protocol names now match the Go
  SDK, and the old names carry the SQL settings.** `DatabaseClient.updateSQL`
  is renamed `updateProtocol`, `DatabaseSQLConfig` (`{mysql, postgresql}`) is
  renamed `DatabaseProtocolConfig` — including the type of
  `DatabaseInfo.protocol` and `CreateDatabaseRequest.protocol` — and the old
  `UpdateDatabaseSQLRequest` is `UpdateDatabaseProtocolRequest`. The freed names
  are the per-database SQL settings: `updateSQL(name, {concurrency})`
  (`PATCH /v1/databases/{name}/sql`; omit the settings to restore the
  defaults), `DatabaseSQLConfig {concurrency?: 'optimistic' | 'pessimistic'}`
  and an `sql` field on `DatabaseInfo` and `CreateDatabaseRequest`. A caller
  passing `{mysql, postgresql}` to `updateSQL` no longer type-checks; rename the
  call to `updateProtocol`. The desktop client's binding call is fixed with it:
  the protocol toggle invoked a `DatabaseBinding.UpdateSQL` the binding did not
  have.
- **MySQL error 1235 (`ER_NOT_SUPPORTED_YET`) is classified `Unimplemented`**
  by `pkg/api/errclass` (501 / `UNIMPLEMENTED` / `StatusUnimplemented`). It
  was unclassified, so each transport answered with its internal-error
  fallback. `SKIP LOCKED` is what raises it.
- **PostgreSQL wire: `SET qrdb_<name>` reaches the engine.** It was accepted
  and ignored, whatever the name; an unknown `qrdb_*` setting, or an invalid
  value for a known one, is now an error. `SET LOCAL lock_timeout` and `SET
  LOCAL qrdb_*` are refused with SQLSTATE 0A000 instead of succeeding as a
  no-op.
- **`SET GLOBAL innodb_lock_wait_timeout` is refused**; the variable is
  session-scoped (see Added).
- **A per-database barrier is accepted only where it is on the database's data
  path, so a non-system tenant can no longer create one.** Until this release a
  barrier was accepted on a `backend: raft` database and on any database of a
  volume- or raft-isolated tenant, although none of those is served from the
  store the barrier wraps — the barrier was decorative. Those requests now fail
  with `database.BarrierOffDataPathError` (`422` / `FAILED_PRECONDITION`). The
  one remaining shape is a non-raft database with an explicit `--data-dir`,
  which only the system tenant can set, so until per-database storage bindings
  land (they restore per-database barriers for every tenant and backend, with
  the barrier on the data path) tenants cannot have one. A barrier-enabled
  record already on disk -- a raft database's, or any database's of a volume-
  or raft-isolated tenant -- still opens, sealed, fails closed until unsealed,
  and is named in a startup warning.
- **`service.BarrierSealedError` names the sealed database in `Database`.** A
  refusal because a database's own barrier is sealed now carries the database
  in `Database` and reads `barrier sealed for database: <name> (tenant: <id>)`;
  `TenantID` alone, and the `barrier sealed for tenant: <id>` text, now mean
  the tenant's barrier. A client that took every `BarrierSealedError` to be the
  tenant's must check `Database` first.
- **etcd listeners need TLS, the node's authentication and a client
  certificate.** A listener no longer starts without TLS and authentication; a
  client without a certificate, or from a tenant other than the database's
  owner, is refused. Only the mapped RPCs are served: `Auth.*`, member
  changes, `Defragment`, `Snapshot`, `Hash`, `HashKV`, `MoveLeader` and
  `Downgrade` are refused with `PERMISSION_DENIED` (previously stubs, no-ops or
  `Unimplemented`). Keys are stored under the database's
  `<tenant>/<database>/` namespace rather than at the shard root, so data
  written through an etcd listener before this release is not visible through
  it; export it with the previous binary and re-import it. `Compact` on the
  shared shard is reserved to the system tenant, as on every other transport.
- **SQL `CREATE DATABASE` / `DROP DATABASE` now require `databases:create` /
  `databases:delete`**, and a tenant session creates a database its tenant
  owns (counted against its quota) and can drop only its own. A role that
  relied on reaching either statement without those grants, or a tenant that
  relied on dropping a database it does not own, is refused.
- **RAG needs the node's database registry.** It is bound by the server at
  start; a server composed without one refuses RAG with
  `service.CapabilityUnsupportedError`. `qrdbd`, `sdk/go/node` and the
  `node://` driver all compose one.
  `service.NewScopedRAGStore` takes a `*service.RAGBinding`.
- **`service.NewRAGService` takes a `*service.ScopedRAGStore`** instead of a
  `KVServicer`. RAG documents ingested before this release sit at the store
  root and are no longer read; re-ingest them.
- **`database.TenantAccessDeniedError` is removed.** Nothing returns it: a
  tenant addressing another tenant's database now gets
  `database.NotFoundError` (`404` / `NOT_FOUND`) instead of `403` /
  `PERMISSION_DENIED`. A SQL session sees only its own tenant's databases in
  `HasDatabase` and `USE`, as it already did in `SHOW DATABASES`.
- **`wal` and `nodehost` are no longer valid database names.** Creating one
  fails with `database.ReservedNameError` (`400` / `INVALID_ARGUMENT`). A
  database of either name created before this release still opens.
- **`Client.DatabaseScoped` returns `(*Client, error)`** and
  `transport.DatabaseScoper.WithDatabase` returns `(Transport, error)`, so a
  transport that cannot scope refuses instead of returning the unscoped
  client. Scoping to `""` or `system` returns the receiver.
- **Creating a raft-backed or volume-isolated database needs its provisioning
  permission.** A role holding `databases:create` but not
  `databases:create_raft` can no longer create a `backend: raft` database or
  any database of a raft-isolated tenant; one without `databases:create_volume`
  can no longer create a database of a volume-isolated tenant. The built-in
  roles that grant database management hold both.
- **etcd is served only by an `engine: kv` database on a node in mode
  `raft`.** Enabling etcd on a timeseries or vector database is refused at
  create and on update with `database.EtcdEngineUnsupportedError` (`400` /
  `INVALID_ARGUMENT`); it would have served that engine's internal keys. A
  standalone node's etcd listener refuses to start
  (`service.CapabilityUnsupportedError`, "requires mode: raft"): MVCC, leases
  and watches are raft-only on every other transport, and the listener used to
  serve them anyway. `backend: raft` is not required -- the documentation
  said it was, and nothing checked it.
- **The sealed-window allowlist is trimmed to verbs that are justified as
  anonymous.** Officer list, rekey, recovery-key generate / delete / status,
  external officer enrollment (`/v1/init/enroll-external-officer`,
  `CeremonyEnrollExternalOfficer`), and the `/v1/init/` and `/v1/bootstrap/`
  REST prefixes are no longer reachable while the barrier is sealed, on any
  transport: they answer `503` / `UNAVAILABLE`. Rekey and the recovery-key
  verbs already refused while sealed. Use `barrier officer claim-status`
  during first boot, and `barrier officer list` once the node is unsealed.
  The REST allowlist can now hold method-qualified entries (`POST
  /v1/barrier/officers`), so a composer matching entries itself must use
  `sealedwindow.RESTEntryMatches`.
- **The boot-time sealed-window listener refuses every request once the
  barrier is unsealed**, with `503` and `{"code":"SEALED_WINDOW_CLOSED"}` on
  REST, and `UNAVAILABLE` with `sealed-window listener closed: barrier
  unsealed` on gRPC, Unix, QUIC and MCP. A client polling it across the unseal
  retries against the regular listener. It no longer starts the MySQL or
  PostgreSQL wire protocols, which carry no sealed-window verb.
- Writing `roleIDs` (`CreateUser`, `UpdateUser`) or `roleBindings` or `attributes`
  (`UpdateUser`) requires `roles:assign` in the user's tenant, and in the tenant of every
  binding written, on every transport including embedded; `AssignRole` and `UnassignRole` go
  through the same gate (`roles:assign` / `roles:unassign` in the user's tenant, and an assigned
  role is validated like any other). Every referenced role must exist
  and belong to that tenant itself -- the system tenant's built-in roles are not assignable in
  other tenants, which seed their own (400 / `INVALID_ARGUMENT`) -- whether or not RBAC is enabled; a node
  with no role service refuses the write (501 / `UNIMPLEMENTED`).
- User attributes reach ABAC policies only as `subject.user_attrs.<key>` (match key
  `user_attrs.<key>`), never as the authenticated `subject.attrs.<key>`, so an attribute
  cannot forge a certificate- or token-derived value.
- `User.Factors` is removed. Factors and MFA state are read from the auth-factor store once
  per response; if that read fails the request fails with 503 / `UNAVAILABLE` instead of
  reporting no second factor.
- **`service.SequenceServicer` carries an epoch** (#547).
  `Allocate` returns `(first, epoch, err)`, and a new `Epoch` method reads a
  sequence's current epoch. `Reset`, and a MySQL-dialect `TRUNCATE TABLE`'s
  sequence reset, advance the epoch in the same compare-and-swap that zeroes
  the counter. Implementations and callers outside this module must follow
  the new signature. The stored value grows from 8 to 16 bytes, counter
  first; a counter-only value written by an earlier release is read as that
  counter at epoch 0, so no sequence restarts on upgrade.
- **Upgrade note: finish upgrading every node before running
  `ALTER TABLE ... AUTO_INCREMENT = 0` or a MySQL `TRUNCATE TABLE`** (#547).
  During a mixed-version window, a node still on an earlier release rewrites
  a sequence's stored value as 8 bytes whenever it allocates, which resets
  its epoch to 0. A later reset can then bring the epoch back to a value
  that a block reserved before the first reset still carries, and that block
  would be accepted as current.
- **An embedded create is tenant-scoped and provisioning-checked.** The
  embedded transport's `CreateDatabase` called the system-scoped `Create`; it
  now creates through `CreateForTenant` for the context's tenant (owned by it,
  inside its quota; a context with no tenant creates for the system tenant).
  On an `sdk/go/node` node with request authentication, a raft-backed or
  volume-isolated create through it needs an identity in the context holding
  `databases:create_raft` / `databases:create_volume`, as on every other
  transport.
- A `roleBindings` entry on a user outside the system tenant must name that user's own tenant;
  any other is refused with `user.RoleBindingTenantError` (400 / `INVALID_ARGUMENT`), because
  such a user is refused every request on another tenant before a binding is consulted and the
  binding could never grant. A system-tenant user may hold bindings in any tenant. A binding's
  tenant is stored normalized, so an empty tenant is stored as `system`.
- MCP user records render `certBindings[].kind` as its string label (`spki_thumbprint`,
  `full_cert_fingerprint`, `piv_attested`) instead of the numeric ordinal, matching REST, gRPC
  and QUIC, and omit `certBindings[].notAfter` when the binding has no expiry instead of
  sending the zero time.

### Added

- `rest.SealedWindowListenerMiddleware`,
  `grpc.SealedWindowListenerUnaryInterceptor` /
  `SealedWindowListenerStreamInterceptor` (re-exported from the Go SDK), a
  `WithSealedWindowListener` option on the REST, gRPC, Unix, QUIC and MCP
  servers, and `sealedwindow.Decide` / `RESTEntryMatches`: the gate of a
  boot-time sealed-window listener, which refuses every request once the
  barrier is unsealed and anonymous officer registration once enrollment has
  closed (`sealedwindow.ListenerProbe`, re-exported as
  `sdk.SealedWindowListenerProbe`).
- `ceremony.CeremonyService.EnrollmentOpen`,
  `seal.StorageBarrier.MarkCeremonyOperational` / `CeremonyOperational` /
  `Initialized`.
- `forward.ClusterPeer` and `forward.MaxBodyBytes`, the cluster-node check
  and the body bound a receiver applies to a forwarded on-behalf request
  before reading its body.
- `ceremony.SOPINLockedError` and `service.SignCSRThrottledError`.
- `sealedwindow.WithAnonymous` / `Anonymous`, the mark the boot-time
  sealed-window listener sets on every request it admits.
- `pkg/authfactors/authfactorstest`: a software `authfactors.Factor` for tests
  that drive the real auth-factor service, with a strict variant that
  verifies a possession proof over each nonce and a permissive one. Imported
  by tests only.
- `sdk.TenantFromClientCert` (and `sdk.ErrInvalidClientCertificate`): qrdb's
  certificate-to-tenant rule, extracted from
  `MTLSAuthenticator.TenantFromCert` into `auth.TenantFromClientCert` and
  re-exported, so a composing host can resolve a verified certificate's user
  before any tenant is bound: derive the tenant, bind it with
  `sdk.ContextWithTenant(ctx, sdk.NormalizeTenantID(t))`, then call
  `GetUserByCertThumbprint` within it. The authenticator delegates to the same
  function, so there is one rule. No cross-tenant thumbprint index is added.
  `pinnedTrustDomain == ""` accepts any trust domain and is for tests only;
  composers must pin.
- `sdk.WithSOPIN(ctx, pin) (ctx, release, err)`: the Go SDK presents the SO
  dual-factor PIN on every network transport -- the `X-QRDB-SO-PIN` header on
  REST and MCP, `x-qrdb-so-pin` metadata on gRPC and the Unix socket, the
  request's `so_pin` field on QUIC. The PIN is sealed in protected memory,
  never logged, and destroyed by `release`; per-request plaintext copies are
  wiped once written. `err` is a protected-memory failure sealing it. Every
  network transport also fills the request's own `so_pin` field
  (`BarrierSeal`, `BarrierInitializeShamir`, `BarrierRekey`,
  `BarrierGenerateRecoveryKeys`, `CeremonyInitialize`, `CeremonySignCSR`) from
  the bound PIN when the request carries none, because the server reads that
  field too; a caller never sets `SOPin` by hand. A request whose own `SOPin`
  differs is refused with `sdk.SOPINConflictError` on every transport.
- Every transport's user response carries `roleBindings`, `attributes`, the metadata of the
  account's enrolled auth `factors` (never their credential data), and `mfaEnabled`,
  `mfaMethods` and `mfaEnrolledAt` derived from those factors. `UpdateUser` sets or clears
  `roleBindings` and `attributes` on every transport.
- A role binding grants its role in authorization checks that target the binding's tenant,
  and nowhere else. REST, gRPC, unix, QUIC and MCP resolve the tenant a request acts on — the
  tenant of the user or role it names, or the tenant a per-tenant barrier operation names —
  before the route-level permission check and authorize against it, so a binding for tenant A
  works on every route that acts on A. Tenant administration (a tenant's record, quotas and
  entitlements) stays checked against the caller's own tenant. A tenant named in a request body
  or payload is read by decoding it into the handler's own request type, so the check authorizes
  exactly the value the handler acts on; a body that does not decode is refused with 400 (QUIC
  `StatusInvalidArgument`) and one over 1 MiB with 413. MCP reads an id argument the way its tools
  do, a JSON number or a string. The FIPS 140-3 Security Officer deny-list and strict separation of duties
  apply to bound roles as they do to held ones.
- `embedded.WithAuthMiddleware`; `sdk/go/node` hands the embedded transport the same
  `AuthMiddleware` its network transports enforce with. The SDK re-exports
  `RoleNotAssignableError`, `RoleServiceRequiredError`, `RoleBindingTenantError` and
  `UserFactorLookupError`.
- **The permitted-authentication-method rule is exported.**
  `CheckAuthMethod(user, method)` in `pkg/auth/middleware` is the function the
  authentication gate runs, re-exported as `sdk.CheckAuthMethod` together with
  `sdk.AuthMethodNotPermittedError`, so a composing host that runs its own login
  ceremony enforces `User.AuthMethods` without restating the rule. The method
  tokens gain `auth.MethodPIV` (`"piv"`) and `auth.MethodWebAuthn`
  (`"webauthn"`) beside `MethodMTLS`, each re-exported by the SDK.
- **Every cross-scope permission and the RBAC denial type cross the SDK
  boundary.** `sdk/go/types_permission_cross.go` names each `ScopeCross`
  permission (`sdk.PermissionAuditReadCross`, the tenants, volume-pool,
  registration-config and credentials families, `sdk.PermissionMVCCCompact`,
  and the node-barrier SO permissions `sdk.PermissionSealSeal`,
  `sdk.PermissionSealRekey`, `sdk.PermissionSealRotateRootKey`,
  `sdk.PermissionSealGenerateRecovery` and `sdk.PermissionSealInitializeShamir`,
  which are now system-tenant only), and a test fails when the registry holds one the SDK does not name.
  `sdk.PermissionDeniedError` aliases `rbac.PermissionDeniedError`, so a
  consumer tells a denial apart from an authorizer that failed to decide.
- **`SaveIfAbsent`'s refusal is typed and crosses the SDK boundary.**
  `dao.AtomicWriteUnsupportedError` (entity type, plus the store's refusal when
  there is one) is re-exported as `sdk.DAOAtomicWriteUnsupportedError`. It
  matches `sdk.ErrKVAtomicWriteUnsupported`, so an SDK consumer fails closed on
  a non-atomic store without importing `pkg/dao`.
- **The MCP tool-error contract is exported through the SDK.**
  `sdk/go/types_mcp_wire.go` re-exports `pkg/api/mcp/wire`: `sdk.MCPCode` and
  its seventeen constants, `sdk.MCPErrorPayload`, `sdk.MCPErrorEnvelope`,
  `sdk.NewMCPErrorEnvelope`, `sdk.DecodeMCPErrorPayload` and `sdk.MCPCodes`. A
  module's MCP tools already receive the classified envelope from the server's
  error-code middleware when they return an error; a test now pins that.
- **A capability module can observe connections on every listener.**
  `apimodule.ConnObserver` (`ConnOpened` / `ConnClosed`, with the
  `apimodule.Transport` the connection arrived on) is an optional
  `ServiceModule` extension; the unified server reports each connection
  accepted on REST (TCP and HTTP/3), gRPC, unix, native QUIC and MCP exactly
  once opened and once closed. A composing host keeps its own active-connection
  metrics without a parallel listener. Re-exported as `sdk.ModuleConnObserver`,
  `sdk.ModuleTransport` and `sdk.ModuleTransport*`. A server whose modules do
  not implement it wraps no listener.
- **The secure analyzer's rule 4: an unborrowed `secure.Buffer.Bytes` read
  must say why** (#428). `Bytes` holds no lock once it returns, so a
  concurrent destroy can unmap the pages mid-read. Every reference outside a
  `_test.go` file now needs a `//secure:unborrowed <injected-interface|allocates-protected|nested-enclave> <reason>`
  directive trailing the read or standing alone on the line above it; `make secure-analyzer` (CI's Lint
  job) fails without it, and `make test-secure-analyzer` plants a bare read to
  prove the gate goes red. The eleven reads left on `main` each reach code rule 6 keeps out of a borrow
  (a consumer-supplied interface, or a protected allocation) and are marked: the cluster DEK wrap (2, the
  subject of the protected-barrier design in #145) and nine in the Shamir
  unseal, recovery, root-token and custodian paths that the share-holder
  rewrite replaces. The `SecretValue.WithBytes` and nested-enclave reads the
  issue listed were already converted by the go-enclave adoption.
- **`securecopy`: the secure analyzer now checks lifetimes, not only
  declarations** (#459). A copy of protected bytes into ordinary memory
  (`append`, `copy`, `bytes.Clone`, `string(...)`, or a call that returns one)
  must be wiped or consumed after it is made, or returned to a caller that
  then owns it. Consumers and copy-returners are inferred across packages from
  go-enclave's `Wipe`/`NewSealed`; `//secure:consumes` declares a contract
  inference cannot see, and `//secure:unwiped <reason>` justifies a copy at
  its site. A `defer secure.Wipe(x)` written before the copy is reported, since
  it wipes what `x` held when the defer ran. It runs in the same
  `make secure-analyzer` sweep, and the self-test plants the unwiped copy-out
  that motivated it. 24 copy-outs were measured; 2 carry `//secure:unwiped`.
  What it cannot see (path sensitivity, aliases, interface dispatch, views
  handed to callees) is listed in `docs/barrier-seal/engineering.md`.
- **`make check-public-releasable`, a whole-tree publication gate, runs in the
  Lint job.** Every tracked file publishes, so the gate rejects any tracked
  file that names the private development fleet: an address on one of its
  subnets, one of its host names, or a path under the owner's home directory
  or the lab mount. It also rejects tracked per-machine agent state
  (`.serena/`, `.claude/`, any `.env`). It reads every path in the index,
  including binary files and symlink values, and it fails closed if git fails
  or a file goes unread. A line whose meaning *is* the value can carry an
  inline `public-release-ok: <reason>` waiver. `make test-public-releasable`
  covers the gate's rejecting paths. The fleet's values are not in the
  tracked tree: the gate loads them at run time from an untracked pattern
  file (`PUBLIC_RELEASE_PATTERNS_FILE`, the handbook checkout beside the
  repository, or `$XDG_CONFIG_HOME/go-qrdb/public-release-patterns`) and
  fails closed without one. CI reads the `PUBLIC_RELEASE_PATTERNS` secret.
- **A Release workflow is the only path that creates release tags.**
  `.github/workflows/release.yml` is `workflow_dispatch` only. It reads
  `VERSION` from `main`, which is authoritative, and runs
  `make release-alpha` as a dedicated release bot. That creates signed tags
  for the root module, `sdk/go` and `sdk/go/gorm/conformance`, and publishes
  them in one atomic push. It then fetches the remote's tags and runs the
  lockstep audit over them. **BREAKING for anyone cutting releases by hand:**
  `release-alpha` no longer writes VERSION or commits. It tags the merged
  VERSION/CHANGELOG commit and refuses a `VERSION=` argument that VERSION on
  `HEAD` does not name ("merge the VERSION/CHANGELOG bump first"), an unsigned
  `HEAD`, and a `HEAD` the remote's `main` does not hold. It pushes only the
  tags. The bot token is never persisted in the checkout
  (`persist-credentials: false`); only the push step receives it, as a
  per-step header (`scripts/git-extraheader.sh`). `docs/release/` covers
  the flow and the owner's one-time setup (tag protection for `v*`,
  `sdk/go/v*` and `sdk/go/gorm/conformance/v*`, plus the
  `RELEASE_BOT_TOKEN` / `RELEASE_BOT_SIGNING_KEY` secrets and
  `RELEASE_BOT_NAME` / `RELEASE_BOT_EMAIL` variables).
- **SQL row locking and pessimistic concurrency** (docs/row-locking).
  `SELECT ... FOR UPDATE`, `FOR SHARE` and `LOCK IN SHARE MODE` (and
  PostgreSQL's `FOR NO KEY UPDATE` / `FOR KEY SHARE`) now take real row locks
  in every mode, with `NOWAIT`; go-mysql-server discarded the clause. A
  database's new `sql.concurrency` (`optimistic`, the default, or
  `pessimistic`) and the session override `SET qrdb_concurrency` make DML in
  pessimistic mode lock the rows it touches and read them at their latest
  committed version, so concurrent writers of a row wait instead of failing at
  `COMMIT` with 1213. The commit-time compare-and-swap stays in both modes.
  - The lock table (`pkg/rowlock`) is lease-bound records in the database's own
    store over the existing transaction, lease and watch operations -- no new
    Raft command. Waiters queue FIFO by create revision; a crashed holder's
    locks expire with its lease; a leader change loses nothing.
  - Lock-wait timeout per session: `innodb_lock_wait_timeout` (now accepting
    MySQL's 1..1073741824 seconds, default 50; go-mysql-server allowed only 1;
    session scope only -- `SET GLOBAL` is refused, since the engine's globals
    are shared by every tenant on the node) and PostgreSQL `lock_timeout`, failing the statement with MySQL 1205 /
    SQLSTATE 55P03. `NOWAIT` is 3572 / 55P03.
  - A pessimistic `UPDATE` or `DELETE` never writes a row that changed while it
    waited: a row it did not read under its lock (a cascade's child rows) is
    compared with what it matched once the lock is granted, and a mismatch or
    a vanished row fails the statement with 1213.
  - Deadlocks are detected at enqueue; the victim's whole transaction is rolled
    back with 1213 / 40P01, and its later statements fail (25P02 on PostgreSQL)
    until the client ends it. `SKIP LOCKED` is refused (1235 / 0A000).
  - Lock errors are classified on every transport through `pkg/api/errclass`
    (Aborted; SKIP LOCKED Unimplemented).
  - Metrics: `qrdb_sql_row_lock_requests_total{mode,outcome}`,
    `qrdb_sql_row_lock_wait_seconds{mode}`, `qrdb_sql_row_lock_waiters`.
- **Per-database SQL settings** on all six transports, the CLI and YAML:
  `UpdateDatabaseSQL` (`PATCH /v1/databases/{name}/sql`, gRPC/Unix
  `DatabaseUpdateSQL`, QUIC `db.UpdateSQL`, MCP `qrdb_database_update_sql`,
  embedded `UpdateDatabaseSQL`), an `sql` field on create and on database info,
  `qrdb database update-sql --concurrency` and `qrdb database create
  --sql-concurrency`, and `databases[].sql.concurrency` in the configuration
  file. The in-process driver takes a `concurrency=` DSN parameter.
- `sealedwindow.Admission`, `Names.Transition` and the per-transport
  `*Transition` derivations; `ListenerProbe.Admit`.

### Changed

- **BREAKING (Go API):** `middleware.SOMiddleware` takes a
  `role.NameResolver` as its second parameter. `rest.Server` passes its
  role service (`rest.WithRoleService`); a server with the gate enabled and
  no role service refuses SO-gated routes with `503`.
- `auth.Identity.HasRole` documents that it compares role identifiers, and
  its parameter is renamed `roleID`.
- REST `POST /v1/authfactors/{id}/verify` reads the body's `factor_name`
  (which the SDK REST client already sent) and passes it to the service, like
  the other five transports, so a mismatch is refused rather than ignored.
- **BREAKING (gRPC API and generated Go client):** `BarrierRecoveryDelete`
  takes `BarrierRecoveryDeleteRequest{so_pin}` instead of
  `google.protobuf.Empty`, so QUIC can carry the SO PIN for it. The wire is
  compatible (an empty request encodes exactly as `Empty` did); Go code
  calling or implementing `QRDBServiceClient`/`QRDBServiceServer.BarrierRecoveryDelete`
  must pass or accept `*qrdbv1.BarrierRecoveryDeleteRequest`. The SDK's
  `BarrierDeleteRecoveryKeys(ctx)` signature is unchanged.
- **Breaking: one marker for "this store cannot write atomically".**
  `kvstore.ErrAtomicWriteUnsupported` (`sdk.ErrKVAtomicWriteUnsupported`) is
  the only one. `dao.ErrAtomicWriteUnsupported`, `database.ErrAtomicWriteUnsupported`,
  `custodian.ErrAtomicWriteUnsupported` and `invite.ErrAtomicWriteUnsupported`
  are removed: the DAO refuses with the typed `dao.AtomicWriteUnsupportedError`,
  and the database backends, the service layer, the custodian service and the
  invitation store return the kvstore marker itself.
  `sdk.ErrInviteAtomicWriteUnsupported` is now an alias of
  `sdk.ErrKVAtomicWriteUnsupported` (same value; its message is the kvstore
  one). The marker, and the DAO's typed refusal, also match
  `kvstore.ErrNotApplied` (new SDK alias `sdk.ErrKVNotApplied`): a capability
  refusal writes nothing, so a caller need not reconcile. Before, a caller
  testing one marker missed a refusal carrying another; the server's own KV
  adapter, for one, refused with the database sentinel, which neither the
  kvstore nor the dao marker matched.
- **Breaking: a permitted-method policy names methods this node can
  authenticate with, or it is refused.** The vocabulary is the built-in `mtls`,
  `piv` and `webauthn` plus the `Name()` of every authenticator in the node's
  own chain, including embedder-supplied ones (`ExtraAuthenticators`, the SDK
  node's `WithAuthenticators`). It is per node, never process-wide: the auth
  middleware exposes it (`AuthMiddleware.IsAuthMethod`), and the node's user
  stores receive it through the new `user.WithAuthMethodVocabulary` option
  (`lifecycle.Infra.AuthMethodVocabulary`), late-bound through
  `middleware.AuthMethodVocabulary`. `CreateUser` and `UpdateUser` refuse any
  other token with the typed
  `user.UnknownAuthMethodError` (`sdk.UserUnknownAuthMethodError`), a
  validation error answered 400 / `INVALID_ARGUMENT` on every transport. A
  misspelled token used to be stored and then match nothing, silently refusing
  the method it named. Stored policies are not re-validated: they read back
  unchanged, an update may keep an entry the policy already held (but not add
  a new unknown one), and the authentication gate compares with the new
  `auth.CanonicalAuthMethod` (no vocabulary check), so no existing account is
  locked out. `auth.IsBuiltinAuthMethod`, `auth.BuiltinAuthMethods` and
  `auth.CanonicalAuthMethod` are re-exported by the SDK.
- **`secure.StdinProvider` reads a seekable stream in bulk** (#440). When
  standard input can seek (redirected from a file), the passphrase line is read
  straight into the mlocked buffer in one or two `read(2)` calls instead of one
  per byte, and the bytes read past the newline are sought back, so the rest of
  the stream is still there for its next reader. A pipe or terminal cannot give
  bytes back, so it is still read one byte per call rather than over-read. A
  seek-back the stream refuses fails the read with `*ProviderError`. On
  either path, 100 consecutive empty reads (`(0, nil)`) fail the read with
  `io.ErrNoProgress` instead of spinning forever.
- **The module forge's address is no longer a Makefile default.** Set
  `GITEA_HOST` in the gitignored `.devcontainer/.env` (documented in the new
  `.devcontainer/.env.example`) or in the environment. A `.env` that has a
  token but no host now fails with an error naming the variable. The devcontainer
  compose services resolve modules from the public proxy when `GITEA_HOST` is
  unset.
- **`make gosec`, `gosec-sdk` and `gosec-sarif` scan test variants
  (`-tests`), at `-severity=low`, and skip generated code.** All three take
  their analysis flags from one `GOSEC_FLAGS` variable, so they cannot
  drift. Every site the lower floor surfaced was fixed or annotated with its
  reason. `pkg/index` replaces an `unsafe` float cast with `math.Float64bits`.
  G104 (unchecked errors) is excluded in `_test.go` files only, where every
  site was, so production code stays armed. The REST auth-factor round-trip
  test now verifies the server against its certificate instead of skipping
  verification. The `export_test.go` idiom is gone from
  `pkg/seal` and `sdk/go`, because gosec's loader cannot typecheck an external
  test package that relies on it. Those tests now run in-package.
- **The coverage baseline records the build tags it was measured under, and
  the gate refuses a profile measured under any other set.** Coverage depends
  on which files the tags compile. On the same tree, `pkg/api/grpc`, `quic`
  and `mcp` read 90.7/97.0/91.3% under `BUILD_TAGS` but 77.1/88.6/80.7% under
  `codec_json raft` alone, which lanes had misread as regressions.
  `coverratchet` takes `-tags`, and `make print-build-tags` prints the set.
  The baseline was regenerated under `BUILD_TAGS`. No number was lowered;
  seven packages that measured fractionally under their baseline, within the
  tolerance, keep their previous number.
- **`pkg/server` is at 91.5%, above the 91% floor** (baseline 90.71). New
  tests cover the raft observer promoter, `StartedAsObserver`, and
  `CloseAndPurge`. `CloseAndPurge` is the demotion teardown that deletes a
  demoted replica's ciphertext, and it had no test.
- **`pkg/ratelimit` measures the same coverage on every run.** Its coverage
  used to alternate between 95.60% and 96.70% depending on thread timing,
  which could fail the coverage gate at random. The token-bucket decision is
  now a pure function (`take`), and `Allow`'s compare-and-swap loop only
  retries it. Tests trigger the reaper with explicit ticks instead of sleeping
  on the wall clock. Five consecutive runs produced identical profiles at
  97.75%; only two unreachable panic guards are uncovered.
- **BREAKING: `tenant.ResidueReconcileErrors` is renamed
  `tenant.ResidueReconcileFailuresError`**, to follow the `XxxError` naming
  every other error type here uses (errname). Its fields, message and
  `Unwrap() []error` are unchanged. `errclass.Conditions`, `errclass.Condition`
  and `errclass.RuleCount` move out of a test-only `export_test.go` into the
  package proper, so gosec's `-tests` loader can typecheck the transport
  parity test that reads them.
- **Documentation: approved multi-model database design.** `docs/databases/`,
  `docs/vectors/` and `docs/time-series/` now carry the approved design in which
  every database serves every engine (KV, MVCC and transactions, SQL,
  collections, secondary indexes, vector, time series, leases and watches) on the
  storage backend it chose, for every tenant isolation mode, through one
  per-database storage binding. They also cover removing the database `engine`
  field and the node-wide backend overrides, migrating existing data, a phased
  implementation plan, and the owner's rulings on its sixteen design decisions
  (every recommendation accepted on 2026-09-30; the drop fence and every new Raft
  command ship behind a cluster feature gate). The same change
  corrects claims those folders made about shipped behaviour: a database's
  `backend` does not decide where most databases' data lives; `objstore` is
  refused when a database store is opened; vector data lives in one node-local
  store under the node key, and its `shard_id` selects nothing; similarity search
  returns nothing on namespaces loaded at startup until the index is rebuilt;
  time-series retention policies are stored but never applied; and no check
  refuses etcd on a non-raft database. No code changes.
- **BREAKING (gRPC API and generated Go client):** `BarrierSeal` takes
  `BarrierSealRequest{so_pin}` instead of `google.protobuf.Empty`, so gRPC and
  the Unix socket can carry the barrier SO PIN. The wire is compatible (an empty
  request encodes exactly as `Empty` did). Go code calling or implementing
  `QRDBServiceClient`/`QRDBServiceServer.BarrierSeal` must pass or accept
  `*qrdbv1.BarrierSealRequest`. The SDK's `BarrierSeal(ctx)` signature is
  unchanged, and every SDK transport fills `so_pin` from `sdk.WithSOPIN`.

### Removed

- The tracked `.serena/` project state, the `.claude/skills` directory and the
  `tmp-probe` developer diagnostic. The first two were already in
  `.gitignore`, and the new gate keeps them out.

### Fixed

- **A standalone `sdk/go/node` node serves its named databases.** Its server
  was composed with neither the database catalog nor a database service
  registry, so no database other than the node's own resolved: a
  database-scoped client was refused outright, and databases created at
  runtime were not reopened at the next boot. The node now opens its catalog
  itself (reopening every database a previous run created) and composes the
  server with it and with a registry whose default entry is the node's own
  service graph (`lifecycle.SystemDatabase`), as `qrdbd` does; the clustered
  node's server gains the same wiring. A scoped embedded client serves exactly
  the families its unscoped transport serves, so on a standalone node MVCC,
  leases and watches stay refused.
- **Unix SDK: a successful `VerifyFactor` or `RevokeFactor` no longer returns
  an error.** `mapGRPCError` turned a nil gRPC error into a `RequestError`, so
  both calls reported failure on success.
- The admin account first boot creates in the system tenant is now the System user, so
  `DeleteUser` refuses it. A cluster whose admin predates this is promoted on its next start,
  and at most one System user can exist in a cluster.
- **`SaveIfAbsent` reports a store's call-time atomic refusal as its typed
  refusal.** A store that implements `AtomicKVStore` but refused the write came
  back as a bare store error. It is now `*dao.AtomicWriteUnsupportedError`,
  the same as a store without the method, with the store's refusal kept in the
  chain. Any other store error is still returned unchanged.
- **`sdk/go/driver`: the embedded `Rows.Next` no longer panics on a row
  wider than its columns.** It wrote `dest[i]` for every cell the engine
  returned. `database/sql` sizes `dest` from `Columns()`, so a wider row
  indexed past it inside the caller's `rows.Next`. A row of any other width
  now returns `*driver.RowWidthError`. A narrower row used to leave the
  previous row's values in the slots it did not fill, and it now gets the same
  error. gosec's G602 found this once the severity floor was lowered.
- **A reset no longer lets a session emit AUTO_INCREMENT values from a block
  reserved before it** (#547). After `ALTER TABLE ... AUTO_INCREMENT = 0` or
  a MySQL `TRUNCATE TABLE`, a session still holding part of a pre-reset block
  kept handing those values out while the restarted counter handed them out
  again, so concurrent inserts collided with MySQL error 1062. The block
  allocator now checks a block's epoch once per statement that draws from a
  block it did not reserve, and refills from the post-reset counter when a
  reset has intervened, on this node or any other. A statement costs at most
  one extra linearizable read, however many rows it inserts.
- **PostgreSQL `TRUNCATE TABLE` keeps the table's sequence on every server
  construction path** (#547). A PostgreSQL server composed with
  `WithServiceRegistry` but no database manager built its provider without
  the PostgreSQL protocol, so its relational tables took MySQL's `TRUNCATE`
  semantics and restarted `AUTO_INCREMENT` at 1, and unqualified
  `pg_catalog` relations did not resolve. `pgsql.Server.Start` now
  applies `WithProtocol(ProtocolPostgreSQL)` on both paths. MySQL `TRUNCATE
  TABLE` resets `AUTO_INCREMENT` to 1, matching MySQL.
- **`SET qrdb_isolation` over the PostgreSQL wire took no effect.** Every `SET`
  was a no-op there; `SET [SESSION] qrdb_*` now reaches the engine and
  `lock_timeout` is translated.
- A `COMMIT` that loses its compare-and-swap now ends the session's
  transaction, as InnoDB's rollback on ER_LOCK_DEADLOCK does, instead of
  leaving go-mysql-server in a transaction that no longer exists.
- **A `UNIQUE` index was not enforced by an `UPDATE` inside a transaction that
  left the row's primary key unchanged.** Two rows of one transaction could be
  given the same value and both commit; a value a committed row held failed
  only at `COMMIT`. Such an `UPDATE` now fails with 1062 at the statement, as it
  does in autocommit.
- **A statement that failed part way inside a transaction kept its partial
  effects.** `INSERT ... VALUES (1), (2), (5)` failing on a duplicate `5` left
  rows 1 and 2 to be committed; a foreign-key cascade that timed out on a
  locked child row left its parent deleted. A failed DML statement is now undone
  whole — across every table it edited — while the transaction's earlier
  statements are kept, as in InnoDB. Outside a transaction a multi-row statement
  is still not atomic: each row is its own commit.
- **Auth-factor names have one normalisation.** `authfactors.NormalizeName`
  (case-insensitive) keys factors in `FactorRegistry` (Register, Lookup,
  Unregister) and in `NewAuthFactorService`, which also uses it for dispatch.
  Two factors whose names normalise alike, such as `PIV` and `piv`, are now
  refused with `*authfactors.FactorAlreadyRegisteredError`; before, the second
  silently replaced the first. A nil factor is refused with
  `*authfactors.FactorRegisterError`. `FactorRegistry.List` reports normalised
  names.
- **A user update that raced another update of an index handle failed with a
  bogus conflict.** Re-claiming a username, email or certificate thumbprint
  could find the key held and then, before reading who held it, see the
  holder release it; the update reported the handle as taken (for a
  thumbprint, "already bound to user 0") instead of taking the now-free key.
  The claim is now retried, and a key that changes hands on every attempt
  fails with `user.HandleContendedError`. A rollback that finds its released
  handle already re-inserted for the same user no longer reports it as
  `HandleReclaimedError`. Together these turned a lost concurrent withdrawal
  of the bootstrap admin's keys into an `IndexCompensationError` rather than
  the expected lockout refusal.
- **`qrdbdharness` handed the same port to concurrent test processes.** The
  port reservation table was per process and every process walked the same
  window from the same base, so two `go test` package processes each
  bind-verified a port the other had just released and both used it: a
  listener failed with `bind: address already in use`, or two in-test Raft
  clusters dialled each other and never elected a leader. Every reserved port
  is now also locked across processes by an abstract Unix socket held for the
  life of the process, so callers of `ReserveTCPPorts`/`ReserveUDPPorts` need
  no change. Every reserved port is also verified free on both TCP and UDP,
  so a REST admin port -- whose HTTP/3 sibling binds the same number on UDP
  -- can be taken from either function.

### Security

- **A sealed database now fails closed on every engine and every transport
  (critical).** Sealing a per-database barrier set the database's status to
  `sealed`, but no request path read that status: a database served from the
  shared system shard, a volume tenant's host or a raft group stayed readable
  and writable while reported sealed. The service registry every transport
  resolves through now refuses every accessor for a sealed database the caller
  owns with `service.BarrierSealedError`, which gains a `Database` field and
  maps to `503` / `UNAVAILABLE`. Another tenant naming the database still sees
  not-found.
- `database.SharedStoreBarrierUnsupportedError` was answered as an internal
  error (`500` / `INTERNAL`) on every transport, contrary to the documented
  `FAILED_PRECONDITION`; it and the new `BarrierOffDataPathError` now map to
  Unprocessable -- `422` on REST, `FAILED_PRECONDITION` on gRPC, Unix, QUIC
  and MCP -- because the caller cannot change a database's backend or
  data_dir (the documentation said `400`).
- **The per-database etcd listener is confined to its database and
  authenticates every request (high).** It was wired to the node's raw,
  un-namespaced system-shard services, so any etcd client reaching the port
  read and wrote every tenant's keys, and its only "authentication" was the
  stub `Auth` service. The listener now resolves its MVCC, transaction, watch
  and lease services through the per-(tenant, database) service registry for
  the database's owning tenant, authenticates every RPC with the node's
  authenticator chain, refuses callers from other tenants with a generic
  `etcd: permission denied` that names neither the database nor its owner
  (the detail is logged server-side), checks a
  default-deny RPC-to-permission map, and re-resolves the database on every
  RPC so a sealed or dropped database is refused. Leases are confined to the
  (tenant, database) that granted them on every transport, etcd included: lease
  IDs are global, and the lease view used to list, keep alive and revoke any
  tenant's lease by ID; ownership is now recorded at grant and a foreign ID is
  answered exactly as a missing one (not-found; `Revoke` is the idempotent
  no-op success and revokes nothing; a write attaching it is refused and
  writes nothing). It refuses to start without
  TLS and authentication (`etcd.AuthRequiredError`) or without a catalog and
  registry to confine it (`etcd.ScopeUnavailableError`). An open `Watch` or
  `LeaseKeepAlive` stream ends with the database's typed refusal the moment
  the database is sealed or dropped, as every transport's watch stream does.
- **SQL `DROP DATABASE` can no longer drop another tenant's database, and SQL
  `CREATE DATABASE` is owned, metered and authorized (critical).** Over the
  MySQL and PostgreSQL wire, `DROP DATABASE` called the database manager by
  name with no ownership check, and the privilege plane deferred the statement
  to an RBAC check nothing performed, so any tenant could drop any tenant's
  database; `CREATE DATABASE` created a system-owned database outside the
  caller's quota. Both statements now go through the tenant-scoped
  `DatabaseService` (`CreateForTenant` / `DropForTenant`) and check
  `databases:create` / `databases:delete` for the session's tenant. A listener
  without that service or an authorizer refuses both to non-system sessions
  with `provider.DatabaseLifecycleUnavailableError`. New options:
  `provider.WithDatabaseService`, `provider.WithLifecycleAuthorizer`,
  `mysql.WithDatabaseService`, `pgsql.WithDatabaseService`.
- **RAG chunk text and document records are confined to the caller's tenant
  and database (medium).** They were keyed `rag:{namespace}:{document}:{chunk}`
  in the shared store with no tenant or database, so two tenants using the same
  namespace and document ID overwrote each other's chunk text, a retrieval
  returned the other tenant's text for its own vector hits, and listing and
  deleting reached across tenants; RAG also ignored database seal, ownership,
  quota and isolation, and its vectors were scoped by tenant only, so a
  document in one database overwrote another database's vector. Chunk text,
  document records and vectors are now all resolved through the node's
  per-(tenant, database) service registry on every call
  (`service.RAGBinding`, bound by the server at start; `service.ScopedRAGStore`
  and `service.NewScopedVectorStoreAdapter`), so a sealed database refuses
  RAG, an unknown or foreign `X-QRDB-Database` is a not-found, and vectors are
  per database. On a node with request authentication a RAG call that carries
  no tenant is refused (`service.RAGTenantRequiredError`) instead of acting
  for the system tenant. REST and gRPC/Unix now also bind the request's database as the
  canonical `tenant.DatabaseFromContext` scope, as QUIC and MCP already did.
- **Another tenant's database is answered as missing (medium).** Database
  names are one node-wide namespace, and a tenant addressing another tenant's
  database got an access denial (`403` / `PERMISSION_DENIED`) where a missing
  name got not-found, so it could probe which names other tenants hold; SQL
  `HasDatabase` and `USE` also answered for every tenant's databases. Every
  tenant-scoped database operation (get, drop, update, seal, unseal, barrier
  status) now returns `database.NotFoundError` for another tenant's database,
  and the SQL provider answers only for the session tenant's databases -- and
  for the configured default SQL database (`sql.default_database`), which
  every tenant has in its own namespace, exactly as the service registry
  resolves it. The
  ad-hoc SQL query (`/sql/query`, `SQLQuery`) resolves the database through
  the service registry before the node-wide catalog, so an unused name and
  another tenant's name get the same not-found. The
  catalog re-key that scopes names per tenant — and closes the last signal, a
  create colliding with another tenant's name — is separate work.
- **Database names that collide with node state are reserved (medium).** A
  volume-isolated tenant's database lives at `{DataRoot}/databases/<name>`,
  beside the tenant's own Raft host, so a database named `wal` or `nodehost`
  was created inside that host's WAL or node-host directory. Both names (any
  case) are now refused for every database with `database.ReservedNameError`
  before any directory is made. Database-catalog refusals also map to their
  documented status on gRPC, Unix, QUIC and MCP, where they were answered as
  internal errors: not found is `NOT_FOUND`, a taken name `ALREADY_EXISTS`, an
  invalid or reserved name `INVALID_ARGUMENT`.
- **A database-scoped client is never handed the default database.** Over the
  embedded transport `Client.DatabaseScoped(name)` returned the unscoped
  client, so a caller that named database X read and wrote the node's default
  database. The embedded transport now implements `transport.DatabaseScoper`
  through the node's per-(tenant, database) service registry
  (`embedded.WithServiceRegistry`, wired by `sdk/go/node`), resolving each
  data-plane call -- KV, MVCC, transactions, batches, watches, leases, indexes,
  time series, vectors, collections, locks, RAG -- as the network transports'
  server does, so a sealed or dropped database is refused; without a registry,
  and over any transport that cannot scope, `DatabaseScoped` refuses with
  `transport.DatabaseScopeUnsupportedError`. Embedded SQL queries resolve
  through the same registry: they used to read the node's default store under
  the database's prefix, which served a sealed database's data and, on a scoped
  transport, prefixed the already-scoped store a second time.
- **A node reports, and warns at startup, when seal is enabled but its data
  is stored unencrypted (high, report only).** The barrier's encrypted
  filesystem covers a standalone node's `pebble` backend, not its `file` or
  `objstore` backend, which kept data in plaintext with nothing saying so (a
  raft node refuses at start any state machine storage that would persist
  plaintext, so it reports no gap). Every
  composition root now logs one WARNING per gap at boot, and the capability
  report carries them as `UnencryptedAtRest` (`unencrypted_at_rest` field 20
  in `CapabilitiesResponse`) on REST, gRPC, Unix, QUIC, MCP and embedded, and
  in `qrdb admin capabilities`. Encrypting those stores is separate work.
- **`databases:create_raft` and `databases:create_volume` are enforced
  (high).** Both were documented as required in addition to `databases:create`
  and checked nowhere, so any holder of `databases:create` could provision a
  dedicated Raft group (`backend: raft`, or any database of a raft-isolated
  tenant) or a database on a volume-isolated tenant's volume. Every create
  path -- REST, gRPC, Unix, QUIC, MCP and SQL `CREATE DATABASE` -- now checks
  them against the caller's tenant through the database service the server
  composes (`service.NewProvisioningAuthorizedDatabaseService`); a refusal is
  `service.ProvisioningDeniedError`, `403` / `PERMISSION_DENIED`.
- **The boot-time sealed-window listener served anonymous requests against an
  unsealed barrier (high).** That listener has no authentication middleware,
  because while the barrier is sealed there is no identity plane. Its gate
  became a pass-through the moment the barrier unsealed, and the listener kept
  serving until the next 250 ms poll noticed and `Stop` completed. In that
  window every route registered on it ran anonymously against an unsealed
  barrier, including `DELETE /v1/barrier/recovery` (proven by
  `TestServeSealedWindow_RefusesAnonymousRequestsAfterUnseal`, which destroyed
  the recovery keys with an anonymous request on the unfixed tree) and the
  credential routes. The gate now samples the barrier on every request and
  refuses everything once it is unsealed, on REST, gRPC, Unix, QUIC and MCP.
- **Anonymous sealed-window verbs are limited to liveness, credential
  possession, first-boot trust on first use, and handler-bound handshakes.**
  The officer list disclosed officer enrollment anonymously. Recovery-key
  delete, rekey and external officer enrollment were destructive or
  SO-only verbs that were allowlisted, and only the service's own refusal
  while sealed kept the first two inert.
- **A forwarded on-behalf request no longer lets any cluster-CA certificate
  make the leader buffer an unbounded body (medium).** The REST middleware
  read the whole body of a request carrying on-behalf headers, with no bound,
  to compute the digest the assertion binds. It did this before verifying the
  assertion, and for any certificate chaining to the cluster CA, including a
  user's. It now refuses a presenter that is not a cluster node
  (`forward.ClusterPeer`) before reading anything, and reads a cluster node's
  body under `forward.MaxBodyBytes`: 16 MiB + 64 KiB, the largest body a REST
  handler forwards.
- **MCP authenticates every request before the MCP layer reads it, and bounds
  the body (medium).** With authentication configured, a request that no
  authenticator accepted went on to mcp-go without an identity, and only tool
  calls were refused. `initialize`, `ping` and `tools/list` were answered to
  anonymous callers, and `tools/list` enumerates the whole admin tool surface.
  mcp-go also parsed bodies of any size. Every request to `/mcp` is now
  authenticated at the HTTP layer and refused with `401` if it fails, with
  `WWW-Authenticate` when a bearer challenge is configured. An authenticated
  caller's body is bounded at the batch-import limit plus 1 MiB of envelope.
  On a listener without authentication (the boot-time sealed-window
  listener, or a node with authentication off), every caller is anonymous,
  and the bound is 1 MiB.
- **gRPC server reflection is no longer exempt from authentication (low).**
  The gRPC and Unix interceptors let `/grpc.reflection.*` through with no
  credentials. qrdb registers no reflection service, but a composer that
  registered one exposed the node's whole schema anonymously. Reflection is
  now authenticated and default-denied like any other unmapped method. A
  composer that wants it anonymous declares it through
  `GRPCPublicMethodLister`.
- **ceremony-sign-csr compares the SO PIN in constant time and is throttled
  (medium).** The endpoint is reachable without authentication on the
  boot-time sealed-window listener, and the SO PIN is its only authorization.
  It compared the PIN with `!=` and answered every guess immediately, which
  made it a timing side channel and an unthrottled online PIN oracle. The
  comparison is now `subtle.ConstantTimeCompare` over SHA-256 digests.
  Checks run one at a time, and each failure is counted before the next
  check. After 5 failures in a row, every further failure locks the check
  for a backoff that doubles from 1 s up to 15 min. A correct PIN resets the
  count. Only failed checks count, so anonymous load cannot starve a
  legitimate officer. While locked, the call fails with
  `service.SignCSRThrottledError`, which unwraps to `pin.ErrPINLocked` and is
  answered `429` / `RESOURCE_EXHAUSTED` on every transport. The REST 429
  carries `Retry-After` with the wait in seconds, rounded up. The SO PIN needs
  at least 40 bits of entropy (docs/ceremony).
- **Neither the native QUIC listener nor REST's HTTP/3 listener accepts 0-RTT
  any more (medium).** The native listener set `Allow0RTT`. The HTTP/3
  listener had no QUIC configuration, and quic-go's `http3.Server` allows 0-RTT
  by default. On both, a resumed client's early data was accepted before the
  handshake completed. 0-RTT data is replayable, so an on-path attacker could
  resend a captured request, including a state-changing one, and have it
  processed again. A resumed client now completes a full handshake first. The
  HTTP/3 listener also takes the native listener's idle, keep-alive and
  stream limits.
- **Anonymous officer enrollment closes once the ceremony is over, including
  after a restart while sealed (high).** Officer and recovery-officer
  registration CA-sign any CSR they are given, and they are anonymous on the
  boot-time sealed-window listener. They were flagged `PreOperationalOnly`,
  but nothing enforced that. Ceremony state lives in memory, so a daemon
  restarted while sealed started a fresh, pre-operational ceremony and gave an
  anonymous caller a certificate signed by the cluster CA. The listener now
  refuses both verbs once enrollment has closed. It closes when the ceremony
  is operational, when the barrier records an operational ceremony (a marker
  written to the barrier's plaintext store, readable while sealed), or when
  the barrier is initialized. It fails closed. The refusal is `403`
  `ENROLLMENT_CLOSED` / `PERMISSION_DENIED`. The ceremony enforces the same
  rule itself, under its own lock: `RegisterOfficer` and
  `RegisterRecoveryOfficer` refuse an anonymous request with
  `ceremony.ErrEnrollmentClosed` once enrollment has closed.
- **The anonymous officer claim status no longer names the officers
  (medium).** `officer-claim-status` is anonymous on the boot-time
  sealed-window listener, so the first-boot wizard can follow claims, and it
  returned every officer's username, certificate thumbprint and auth factor
  to any caller. That listener now answers with the share index, enrolment
  and claim times and pending state only. `BarrierOfficerClaimStatus` redacts
  by default, for every caller whose context carries no authenticated
  identity and for every request the sealed-window listener marks anonymous
  (`sealedwindow.WithAnonymous`). An authenticated caller on a regular
  listener still gets the full record. The wizard keys its claim table by share index and names
  rows from its own registrations.
- **The SO dual-factor gate recognises a real Security Officer, and only
  one.** `middleware.SOMiddleware` checked `identity.HasRole("so")`, but an
  authenticated identity's `Roles` holds role *identifiers* (from the user
  record's `RoleIDs`). Every real officer was refused with
  `403 SO role required`, and an identity whose roles carried the literal
  string `so` (a token claim for a subject with no user record) passed the
  role step; the SO PIN was still required. The gate now resolves the SO
  role through the role store with the new `role.HoldsSystemRole`: the
  caller must carry the identifier under which its own tenant's `so` role is
  persisted. Names never match. A role store that cannot answer refuses with
  `503`.
- **The SO dual-factor gate now guards every network transport, not only
  REST.** With `auth.so_dual_factor` on, the SO-gated operations
  (`middleware.SOPermissions`: barrier rekey, Shamir initialize, recovery
  generate, root-key rotation, ceremony initialize, custodian enrol and revoke,
  CSR signing) were reachable over gRPC, the Unix socket, QUIC and MCP by any
  caller RBAC admitted, such as a token principal granted `seal:rekey`, with
  no SO role and no SO PIN. The gate is now one implementation,
  `middleware.SOGate`, which the orchestrator installs on all of them
  (`WithSOGate` on each server). The PIN travels in `x-qrdb-so-pin` metadata
  on gRPC and Unix, in the `X-QRDB-SO-PIN` header on MCP, and in the request's
  `so_pin` field on QUIC. The Go SDK sends it on every network transport
  from `sdk.WithSOPIN` (see Added). Refusals are typed
  (`SOFactorError`, `SORoleRequiredError`, `SORoleUnresolvedError`) and map to
  the same class on every transport.
- **`VerifyFactor` is dispatched to the enrollment's own factor.** The
  caller's factor name used to choose the factor that verified a challenge,
  and the enrollment was consulted only when the name was empty. With two
  factors registered, a caller could answer a strong factor's challenge
  through a weaker one: reproduced on gRPC, Unix, QUIC, MCP and embedded with
  a junk response. The service now loads the enrollment, dispatches to its
  factor, and refuses a non-empty caller name that differs with the typed
  `*authfactors.FactorMismatchError` (`AuthFactorMismatchError` in `sdk/go`):
  400 on REST, `INVALID_ARGUMENT` on gRPC and Unix, `StatusInvalidArgument` on
  QUIC, `CodeInvalidArgument` on MCP. The challenge is consumed either way. A
  challenge whose enrollment was revoked after it was issued is now refused
  too (`*authfactors.EnrollmentRevokedError`). Factor names are compared
  through the registry's own normalisation (case-insensitive), which the
  registry now also applies when a factor is registered, so a factor whose
  `Name()` is not lower case can be enrolled at all.
- **A client certificate whose SPIFFE SANs name more than one tenant, or that
  carries any SPIFFE SAN outside the pinned trust domain, is refused.** The
  certificate-to-tenant rule read only the first tenant SAN, so the issuer's
  SAN order chose the tenant, and a foreign-domain SAN after a good one was
  ignored. Every URI SAN is now examined, on login and in
  `sdk.TenantFromClientCert` alike. The refusal is the new
  `*auth.CertificateTenantError` (`sdk.ClientCertificateTenantError`), which
  still matches `ErrInvalidCertificate`, so every transport answers as
  before; an ambiguous certificate carries the new
  `*spiffe.AmbiguousTenantError` (`sdk.SpiffeAmbiguousTenantError`).
- **`qrdbd start` makes its memory unreadable to other processes before it
  holds any key** (#554). mlock kept protected memory out of swap, but a
  process running as the same user could still read every enclave through
  ptrace, `/proc/<pid>/mem` or a core file. `start` now calls the new
  `secure.HardenProcess` first: `prctl(PR_SET_DUMPABLE, 0)` on Linux,
  `ptrace(PT_DENY_ATTACH)` on macOS, and a logged no-op where no mechanism
  exists (Windows). It is unconditional; `QRDB_SECURE_ALLOW_PROCESS_INSPECTION=true`
  leaves the process inspectable for a debugging session and logs a warning.
  A malformed opt-out value, or a platform refusal, fails `start` with
  `*ProcessInspectionOptOutError` / `*ProcessHardeningError`. Re-exported from
  `sdk/go/secure` (with `AllowProcessInspectionEnv` and both error types) so
  embedding binaries apply the same posture at their own startup. Operators:
  `gdb -p`/`dlv attach` now need the opt-out, a crash writes no core file, and
  non-root monitoring agents lose `qrdbd`'s `/proc/<pid>/` entries.
  Hardening is the first statement of the start command, before any
  configuration is read. A tracer already attached (`TracerPid` in
  `/proc/self/status` not 0) fails start with `*ProcessTracedError`. In FIPS
  140-3 mode the opt-out is refused (`*ProcessInspectionOptOutRefusedError`),
  and an honoured opt-out is written to the compliance audit trail
  (`disable-inspection-hardening`); a node that cannot record it does not run.
  `HardenProcess` returns the outcome (`ProcessHardened`,
  `ProcessInspectionAllowed`, `ProcessHardeningUnavailable`) with the error.
- **The `qrdb` desktop and CLI binary hardens itself the same way** (#554). It
  unseals barriers and reads passphrases, PINs and shares too, so it calls
  `secure.HardenProcess` before the GUI/CLI branch, honours the same
  `QRDB_SECURE_ALLOW_PROCESS_INSPECTION` opt-out, and exits 1 on a refusal. Its
  warning goes to standard error so it never mixes into a command's output.
- **No anonymous request on the boot-time sealed-window listener runs across
  the unseal (medium).** The listener's gate checked the barrier when a
  request arrived, so a request admitted while sealed could finish after
  another request had unsealed the barrier, running anonymously against an
  unsealed barrier. Requests now hold the listener's `sealedwindow.Admission`
  until they complete. The verbs that can initialize or unseal the barrier
  (`sealedwindow.Names.Transition`) hold it exclusively, so they wait for
  the requests in flight, and every later request is refused.
- **A tenant's Security Officer can no longer operate the node barrier.**
  `seal:rekey`, `seal:rotate-root-key`, `seal:generate-recovery` and
  `seal:initialize-shamir` were ScopeSelf. For an operation that names no
  tenant, ScopeSelf compares the caller's tenant with itself, so the check was
  vacuous: an SO of tenant A, presenting tenant A's own valid SO PIN, could
  rekey or regenerate recovery keys for the node barrier that protects every
  tenant -- on every transport, gate on or off. They are now `ScopeCross`:
  system-tenant identities only.
- **The Go SDK sends the SO PIN only over TLS, and only to the configured
  host.** `sdk.WithSOPIN` attached the PIN on gRPC and Unix connections with
  insecure credentials, on MCP to `http://` URLs, and on REST to every redirect
  hop, including a cross-host or `http` one. gRPC and Unix now send it only over
  TLS credentials. REST and MCP send it only on an `https` hop to the configured
  host, checked on every hop. A PIN-bound call otherwise fails before anything
  is sent, with the new `sdk.SOPINRequiresTLSError` (plaintext) or
  `sdk.SOPINForeignHostError` (a redirect elsewhere).
- **A configured barrier SO PIN is required to seal the node barrier on every
  transport, and only the system tenant may seal it.** REST and MCP verified
  the request's `so_pin` against the PIN backend. gRPC and the Unix socket took
  `google.protobuf.Empty` for `BarrierSeal`, and QUIC ignored the request body,
  so on those three transports the configured PIN was skipped. They now verify
  it the same way. Separately, `seal:seal` was ScopeSelf. For an operation that
  names no tenant, ScopeSelf passes vacuously, so another tenant's Admin or
  Operator could seal the node barrier and stop every tenant's data plane. It
  is now `ScopeCross`, like the other node-barrier verbs.
- **The SO dual-factor gate fails closed without authentication on every
  transport.** The orchestrator installed the gate on gRPC, Unix, QUIC and MCP
  only when an auth middleware was configured, so with `auth.so_dual_factor`
  on and authentication off, those four transports skipped the gate. REST
  already refused in that case. One predicate (`so_dual_factor` set and an SO
  authenticator wired) now governs all five transports. With no one to
  authenticate, the gate refuses every SO-gated operation.
- **The SO PIN is held in protected memory, on the server and in the Go
  SDK.** The server's per-request SO factor (`middleware.SOFactor.PIN`) and
  the SDK's PIN bound by `sdk.WithSOPIN` were plain byte slices, which
  `make secure-analyzer` refuses. Both are now a `secure.Secret`: sealed when
  the PIN is read, opened only for the verifier or the wire write, and
  destroyed when the gate has decided or `release` runs. The server builds
  the factor with the new `middleware.NewSOFactor` (and
  `middleware.SOFactorFromHTTPRequest` for REST and MCP). A PIN the node
  cannot hold in protected memory is refused with the new
  `middleware.SOPINUnavailableError` (503 / `Unavailable`), not reported as a
  bad PIN.

### Known limits

- **A database's seal is enforced per node.** Its barrier and its `sealed`
  status are node-local, not replicated: sealing it on one node of a cluster
  does not seal it on the others, and a node on which it is unsealed keeps
  serving it. Seal it on every node that serves it.
- **A barrier off the data path is only as good as its seal.** A
  barrier-enabled record written before this release refused that shape (a
  raft database's, or a volume- or raft-isolated tenant's) fails closed while
  sealed, and once unsealed its data is served unencrypted from the store the
  barrier does not wrap.
- **A stream's permission is decided when it opens.** A change to the
  caller's roles applies to its next etcd stream or watch, not to one already
  open. (A seal or a drop does end open streams.)
- **Database names are still one node-wide namespace.** Another tenant's
  database is answered as missing everywhere except creation: a create naming
  a database another tenant holds fails with `database.AlreadyExistsError`.

## [0.23.0-alpha] - 2026-09-30

Tracks go-quicraft v0.15.0-alpha. BREAKING: wipe each node's Raft WAL directory and re-sync from peers when upgrading.

### Changed — BREAKING

- **go-quicraft v0.14.1-alpha → v0.15.0-alpha** (and go-enclave v0.6.0-alpha →
  v0.6.1-alpha) in all three modules. go-quicraft 0.15 changes its WAL
  format in place (hard-state record `0x02` → `0x0d`; the remove-node record
  `0x06` is retired): **before upgrading a node, wipe its Raft WAL directory
  and let it re-sync from its peers** (or restore from a snapshot). A replica
  removed with `RemoveData` is retired for good; rejoining needs a new replica
  ID. Snapshot artifacts exported by earlier builds carry no integrity trailer
  and are refused on import; export them again after upgrading. The release
  also hardens snapshot integrity end to end and keeps lease reads safe across
  follower restarts.
- `database.MultiplexLogDB` implements the new `logdb.LogDB.NodeRemoved`,
  dispatching to the WAL that owns the shard.

## [0.22.0-alpha] - 2026-09-30

Merge train 2: transactions and guarded batches on every state machine type, O(1) TRUNCATE, one error-classification table, node lifecycle and go-quicraft 0.14 member identities, tenancy reconcile and atomic tenant create, certificate/auth posture (SPIFFE SAN, mTLS binding policy, single-owner REST routes, attested-caller gate), and collection lifecycle states.

### Added

- **Pre-bound listeners for the embedded server** (#374).
  `server.WithRESTListener`, `server.WithRESTPacketConn` and
  `server.WithGRPCListener` (re-exported as `sdk.WithRESTListener`,
  `sdk.WithRESTPacketConn`, `sdk.WithGRPCListener`) hand the server sockets the
  embedder already bound, so a kernel-assigned port is never released between
  reservation and serving — on TCP or on the HTTP/3 UDP side. A handed-over
  socket outlives any one server, so REST, HTTP/3 and gRPC keep answering on
  the same port after an in-process encryption migration rebuilds the server;
  the SDK node closes the sockets in `Node.Close` and when
  `NewStandaloneNode` / `NewClusteredNode` return an error, and a `Start` that
  refuses (including for a disabled protocol, `*server.PreboundSocketError`)
  closes them. A
  pre-bound HTTP/3 socket is held to the TCP listener's host and port. Where
  pre-binding is impossible — the multi-node Raft transport, which go-quicraft
  binds and advertises itself — port 0 is refused with a
  `*cluster.RaftPortZeroError`, by the SDK node and by `qrdbd start` alike.
  A zero admin or gRPC port on the SDK node still selects the fixed default.
- **Shared-prefix tenants have real per-tenant secondary indexes** (#40,
  priority 1). A DAO over a `StoreResolver`-served store for a `prefix`
  tenant now answers `QueryByIndex` from an engine-maintained index confined to
  that tenant, instead of refusing with `kvstore.ErrIndexUnsupported` and
  planning a full scan. `lifecycle.kvStoreAdapter` hands the engine the
  caller's absolute key prefix made relative to the namespace the index name
  carries, so the maintained scope is exactly the prefix the keys carry for a
  `{tenant}/` store, a `{tenant}/{database}/` namespace and a DAO built
  `WithDatabase` alike; a name outside its keys' namespace is still refused.
  `KVEngine.PutWithIndexes`/`DeleteWithIndexes` select indexes by scope, like
  the Raft apply path, instead of by the key's first path segment. Encrypted
  tenant stores (`barrier`, `volume`, `raft`) still refuse index operations.
  **Behaviour change for direct engine users:** an UNSCOPED index — created
  with neither a namespace in its name nor a declared key prefix — is now
  maintained by `PutWithIndexes`/`DeleteWithIndexes` for every key, exactly
  as the Raft apply path has always maintained it. Before, those two helpers
  updated an index only when its name began with the key's first path segment
  (`{entityType}.`), so an unscoped index missed writes the Raft path indexed.
  Indexes registered through `RegisterEntityType` are scoped to
  `{entityType}/` and are unaffected. To keep an index to one keyspace,
  declare its key prefix.
- **Retired tenant ids are enumerable** (#470). `ListDeletedTenants` returns a
  page of deleted tenant ids with their deletion records, in ascending id
  order — a paged range read over the tombstone prefix, with an opaque page
  token. It is on every transport: REST `GET /v1/tenants/deleted/list`, gRPC
  and Unix `TenantListDeleted`, QUIC `tenant.ListDeleted`, MCP
  `qrdb_tenant_list_deleted`, the embedded client, and
  `qrdb tenant list-deleted`. New cross-scope permission
  `tenants:list_deleted`.
- **Every node reclaims a deleted tenant's directories** (#469). A node that
  was down during a tenant delete, or held the tenant without hosting a shard,
  kept its directories and the record that restored its barrier at the next
  boot. A reconcile pass now runs at boot, before any tenant barrier is
  restored, and on demand as `ReconcileDeletedTenants` on every transport (REST
  `POST /v1/tenants/deleted/reconcile`, gRPC and Unix `TenantReconcileDeleted`,
  QUIC `tenant.ReconcileDeleted`, MCP `qrdb_tenant_reconcile_deleted`, the
  embedded client) and as `qrdb tenant reconcile-deleted`. The tombstone is its
  only predicate, and it never removes a directory a live tenant claims; what
  it leaves is reported with the reason. New cross-scope permission
  `tenants:reconcile_deleted`, and `seal.BarrierRegistry.ReleaseTenant` /
  `DropTenantRecords` / `TenantDataRootCandidates`. The live set it guards
  with is read strictly (an unreadable tenant record aborts the pass) and
  re-read before each removal; a non-canonical recorded path is never removed;
  and the records naming a directory are dropped only after it is removed, so
  a failed removal is finished by the next pass. `DestroyTenant` now removes
  the `DataRoot` before dropping those records, for the same reason, and
  registering a `volume`/`raft` tenant at a `DataRoot` another tenant's record
  on the node still names is refused (`seal.DataRootClaimedError`, surfaced as
  `tenant.DataRootOverlapError`; when the claimant is a retired tenant the
  error says so and its new `Remedy` names `qrdb tenant reconcile-deleted`; a
  failure to read the records to check is `seal.ClaimedRootCheckError`).
  Recorded paths are judged **as recorded**: `TenantDataRootCandidates` returns
  them uncleaned (de-duplicated on the cleaned form), so a dotted record such
  as `/srv/qrdb/x/..` is skipped as `not-canonical` instead of being cleaned
  into its parent and removed. A directory inside a reserved root is refused
  unless it lies strictly inside a volume-pool base, so a pool carved out of
  the data directory stays reclaimable. A `DataRoot` that is a **mount point**
  is reclaimed: the new `seal.BarrierRegistry.RemoveDataRoot` empties the
  directory and treats an emptied root refusing rmdir with `EBUSY`/`EXDEV` as
  reclaimed, instead of failing forever (`DestroyTenant` and the reconcile both
  use it; the filesystem is injectable through `seal.DirectoryRemover` /
  `seal.WithDirectoryRemover`). A failure for one retired id no longer stops
  the pass: per-id failures are collected and returned as
  `tenant.ResidueReconcileErrors` alongside the report, and only a failure to
  read the tombstones or the live set aborts. Every skipped path is logged at
  `WARN`, and an id already fully reclaimed on the node (no directory, and no
  state per the new `seal.BarrierRegistry.HasTenantState`) is passed over
  untouched. On the wire, a partly failed pass answers success with its
  report, whose new `failed` list (`tenant.FailedReclaim`; protobuf
  `TenantResidueFailure`, field 4 of `TenantReconcileDeletedResponse`) names
  each failed id; `tenant.ReconcileResponseError` is the one rule every
  transport applies. The Go SDK returns the response and a
  `transport.ReconcileDeletedTenantsError` together, and
  `qrdb tenant reconcile-deleted` prints the report with a `failed` row per id
  before exiting non-zero.
- **`tenant.DataRootOverlapError` is a caller error on every transport.** A
  create whose `DataRoot` overlaps a volume pool, a reserved data root or
  another tenant's `DataRoot` -- a retired tenant's unreclaimed one included,
  whose message carries the remedy -- was unmapped and reported as 500 /
  `INTERNAL`. It is now 409 on REST and `FAILED_PRECONDITION` on gRPC, the
  Unix socket, QUIC and MCP; the embedded transport returns the typed error.
- **Client certificates carry the SPIFFE tenant SAN when a trust domain is
  configured.** With `auth.spiffe_trust_domain` set, the built-in CA mints
  `spiffe://<trust-domain>/tenant/<tenant>/user/<username>` into every client
  certificate, so the authenticator's trust-domain pin is exercised by the
  deployment's own certificates: they resolve through the pinned SAN, and a node
  pinned to a different domain refuses them. With no trust domain the
  certificate is unchanged and carries no SubjectAltName. A certificate whose
  SAN would not parse back to its Subject's tenant and username is refused with
  `cert.SPIFFEIdentityError` and nothing is minted, and the ceremony CA's
  `SignCSR` refuses a CSR whose tenant SPIFFE SAN disagrees with its OU
  (`ceremony.CSRTenantSANError`). `auth.spiffe_trust_domain` is validated at
  config load with the same round trip. New: `cert.WithTrustDomain`,
  `lifecycle.Infra.SPIFFETrustDomain`, `spiffe.ValidateTrustDomain`,
  `spiffe.TrustDomainError` (carries the rejected value).
- **`auth.mtls_binding_policy: enrolled | required`.** `enrolled` (the default)
  is today's behaviour: an account's certificate bindings are enforced once it
  has one. `required` refuses every certificate login that does not present a
  key enrolled on the account it names — an account with no bindings, and a
  certificate naming no account, get 401 with
  `middleware.CertBindingRequiredError`, which quotes the thumbprint to enrol.
  Because it is a lockout risk, a node under `required` **refuses to boot**
  when the bootstrap admin holds no unexpired binding
  (`firstboot.AdminCertBindingRequiredError`), and `firstboot.Initialize`
  refuses before writing anything when it could not bind the admin it would
  create, and verifies the admin it produced. The SDK node refuses to start with
  an existing unbound admin and runs first boot under its own policy through
  `Node.FirstBoot` (only that path guarantees no lockout on an embedded node).
  Every user store under `required` refuses every change that would leave the
  bootstrap admin unable to log in by certificate -- deleting, renaming,
  disabling or suspending it, dropping mtls from its methods, or leaving it
  with no live binding, whichever verb makes it -- with
  `user.BootstrapAdminLockoutError{Reason}` (409 on REST, FAILED_PRECONDITION
  on gRPC and QUIC, CodeFailedPrecondition on MCP), judged inside the store's
  conditional write. The recovery path for an admin locked out by expiry is to
  switch to `enrolled` and restart. The boot check also refuses an admin whose
  lifecycle forbids login or whose method policy excludes mtls, through the
  same predicate (`user.AdminLoginBlocker`). `AddCertBinding` now refuses an
  expiry that is not in the future, for every account
  (`user.CertBindingExpiredError`: 400 / INVALID_ARGUMENT). New:
  `user.WithLogger`; `lifecycle.UserStoreOptions` takes the store's logger.
  The daemon judges the admin through the linearizable store with a bounded
  wait, so a follower that has not yet replicated the admin's binding does not
  refuse to boot. The admin's deadline is exported as
  `qrdb_auth_admin_cert_binding_not_after_timestamp_seconds`, refreshed on
  every boot check and every committed change to the admin, with a warning
  inside 30 days under `required`. An unrecognised value fails configuration validation and the
  boot. Forwarded on-behalf requests,
  module session tokens and a sealed tenant's IAM window are not gated, as
  documented. New: `auth.MTLSBindingPolicy`, `auth.ParseMTLSBindingPolicy`,
  `middleware.WithMTLSBindingPolicy`, `wiring.AuthMiddlewareConfig.MTLSBindingPolicy`,
  `firstboot.Dependencies.MTLSBindingPolicy`, `firstboot.VerifyAdminCertBinding`,
  `firstboot.VerifyAdminCertBindingWithin`, `node.(*Node).FirstBoot`,
  `bootstrap.SystemConfig.MTLSBindingPolicy`, and their `sdk/go` re-exports.
- **Attested-caller gate.** For the permissions a composer names, a request
  that authenticated and that RBAC granted is admitted only when the injected
  `auth.PeerAttestationVerifier` reports the peer on the connection as
  currently attested: fresh evidence (the gate enforces its own
  `MaxEvidenceAge`), for that peer's certificate, that passed. Every other
  outcome — no evidence, stale or future-dated evidence, evidence for another
  peer, a rejected state, a verifier error, no peer certificate, a forwarded
  (on-behalf) identity — refuses,
  and a refusal is audited as its own `auth.peer_attestation` record with the
  reason and whether it is a security event. One option gates REST, gRPC,
  unix, QUIC and MCP, and the tenant-barrier bootstrap windows are gated too;
  the embedded transport has no peer and is not gated.
  qrdb owns only the seam: the verifier is go-xkms's. New:
  `auth.PeerAttestationVerifier`, `middleware.NewPeerAttestationGate`,
  `middleware.WithPeerAttestationGate`,
  `wiring.AuthMiddlewareConfig.PeerAttestation`,
  `node.WithPeerAttestationGate`, and `sdk/go` re-exports. Docs:
  `docs/attested-caller/`.
- **Collection lifecycle states.** A collection is `creating` until every
  declared index exists, `active` while it serves documents, and `dropping`
  from the moment a drop starts. Every state is reported by info and list; only
  an `active` collection serves document reads and writes
  (`service.CollectionNotActiveError`, 409 / `FAILED_PRECONDITION`).
- **Collection sweeper.** The server finishes collections an interrupted create
  or drop left `creating` or `dropping` for more than five minutes — a stuck
  drop is completed, a stuck create is unwound — so nobody has to re-issue them
  (`server.WithCollectionSweep` tunes it). The shared store's databases are
  found from a replicated collection work journal and swept by the system
  shard's leader (`server.WithCollectionSweepLeader`, wired by the daemon), so
  a database only another node has catalogued is still finished after a
  leadership change; a raft tenant's shard is swept by its leader, and volume
  hosts and node-local dedicated stores on every node that holds them. A
  record whose fields do not derive from one another is reported and left
  alone rather than acted on, and a database with stuck work that stays
  unresolvable is logged as a warning. Deleting a tenant removes its journal
  entries (`__sys:collection-work/{tenant}/`) with the rest of its keyspace.
- **Persisted secondary-index ownership.** Index-create commands carry the
  creating object's owner and index-drop commands can be owner-checked
  (`IndexServicer.DropOwnedIndex`), both as trailing wire extensions that
  existing log entries decode unchanged.
- `CollectionServicer.UpsertDocument`, a guarded insert-or-replace that
  reports what it displaced.
- **Collection index definitions and lifecycle state on every transport.**
  `CreateCollectionRequest` carries `indexes` (and `storage_codec`, which only
  the embedded transport carried before) on REST, gRPC, Unix, QUIC, MCP and
  embedded, and `CollectionInfo` reports `indexes`, `state` and `state_since`.
  The protobuf messages gain `CollectionIndexDefinition`,
  `CreateCollectionRequest.storage_codec`/`indexes` and
  `CollectionInfoResponse.storage_codec`/`indexes`/`state`/`state_since`.
  `qrdb collection create` takes a repeatable `--index FIELD[,unique][,sparse]`,
  and `info`/`list` show the state.

### Changed — BREAKING

- **Relational tables created by v0.20.0 through v0.21.0 must be dropped and
  re-created; there is no migration.** Those releases wrote rows and index entries under
  a key layout that carried neither a table generation nor an index incarnation, which
  `TRUNCATE TABLE` in O(1) and background reclamation both require. Such a table is now
  refused, never read as empty: every read, write and `TRUNCATE` fails with MySQL error
  1459 (`ER_TABLE_NEEDS_UPGRADE`; PostgreSQL SQLSTATE 55000), gRPC/Unix/MCP
  `FAILED_PRECONDITION`, QUIC `StatusFailedPrecondition` or REST 412, naming the table
  and the remedy. `DROP TABLE` still works and reclaims the old data; then `CREATE
  TABLE` it again and reload its rows. A legacy table whose v0.20.x `ALTER` rewrite was
  interrupted is droppable too; `information_schema` lists it with the refusal as its
  comment.
- **DDL on a table refuses while its `ALTER TABLE ... DROP/MODIFY COLUMN` rewrite is
  unfinished.** `CREATE`/`DROP`/`RENAME INDEX`, `ADD`/`DROP`/`UPDATE FOREIGN KEY` and
  `RENAME TABLE` now fail with the rewrite-in-progress error, as `ALTER` and `TRUNCATE`
  already did, instead of committing a change the rewrite's completion would undo. `DROP
  TABLE` still works.
- **Every `--sm` / `raft.sm_type` serves transactions and guarded batches, and
  starts (go-qrdb#136).** `memory`, `standard`, `concurrent`, `fast`, `disk`,
  `highperf` and `regular` now decide `OpTransaction` and `OpBatch` against
  their own keyspace — the one `Put`, `Get` and the guarded verbs use — with the
  default pebble state machine's compare semantics (value and version compares;
  create/mod-revision compares refused with the same error). The start-time
  refusal 0.21.0-alpha added for these types no longer fires, so a deployment
  that stayed on an earlier release to keep one of them can upgrade.
  `configs/qrdb-dev.yaml` is back on `sm_type: memory`.
- **The `mvcc-fast` sm_type is removed, with `pkg/mvccadapter`.** Its MVCC
  store lived in a keyspace separate from the KV plane — a transaction compare
  could not see a plain `Put`, and the transaction service's opcodes were
  refused outright — and it kept that store in a temporary directory. A config
  naming it is refused at load. Use `mvcc` for revisions, or any other type
  for transactions.
- **`statemachine.Backend` gains a required `ApplyAtomic(muts []Mutation)
  error`.** A transaction or batch commits all or nothing through it: one
  `storage.Batch` on `DiskBackend`, one acquisition of every touched shard's
  lock on `FastBackend` and the in-memory backends (`FastBackend` writes
  through to storage asynchronously, in apply order, as `Put` does — its
  durability is the Raft log's, since each replica is rebuilt from the log and
  snapshots at start). A `Backend` implemented outside this repository must
  add it.
- **Under the seal barrier, `--sm disk`/`fast` storage is encrypted at rest or
  refused.** That storage was never wrapped by the barrier, so a replica's data
  sat on disk in plaintext beside a barrier the operator enabled. Over
  `--sm-storage pebble` it is now opened on the barrier's encfs, like the
  `pebble` and `mvcc` state machines. `--sm-storage file` cannot be encrypted
  and is refused at start (`SealedSMStoragePlaintextError`; registry:
  `statemachine.SMStorageUnencryptedError`); `memory` persists nothing and is
  allowed. `sdk/go/node.NewClusteredNode` applies the same rule and no longer
  refuses every non-default `sm_type` under the barrier.
- **Empty keys are refused on every write path, and the disk state machine's
  applied-index key is reserved.** `TransactionService` and `BatchService`
  refuse an empty key before proposing it (`service.InvalidKeyError` inside
  `EncodeRequestError`, invalid-argument on every transport); a state machine
  that receives one anyway refuses it as an application result
  (`statemachine.InvalidKeyError`, a new `protocol.RefusalError` declaration
  the Apply classifier encodes) instead of halting Apply on every replica. A
  client write to `"\x00__qrdb_sm_last_applied__"` is refused on every
  registry state machine.
- **`statemachine.SetDataDirRoot` and `SetSMStorage` are removed.** Storage is
  passed per resolution: `statemachine.ResolveTypedWithStorage(smType,
  smBackend, StorageConfig{Root, Kind})`, and storage-generic state machines
  register through `RegisterStorageGeneric`. `sdk/go/node.NewClusteredNode`
  now honours `raft.sm_storage` (it ignored it), and refuses a non-pebble
  `sm_storage` for a state machine that carries its own storage.
- **The node capability report names the real sm_type.** A raft node's profile
  is narrowed by its state machine's descriptor
  (`statemachine.NarrowCapabilities`): a node on `memory` no longer reports
  MVCC, watch, lease, lock, index or time series, and the default `pebble`
  node no longer reports MVCC, which it never served. Every descriptor —
  the registry's and `store.PebbleDescriptor`/`MVCCDescriptor` — is held to
  what its state machine does by a behaviour test.
- **`server.NewTenantHostRegistryFromServicer` is renamed
  `server.NewTenantHostRegistryFromLookup`** and takes a `server.TenantLookup`
  (any value with `GetTenant(ctx, id) (*tenant.TenantInfo, error)`) instead of
  a `tenant.TenantServicer` (#383). *Migration:* rename the call. A
  `tenant.TenantServicer` still satisfies `TenantLookup`, so an argument that
  is never swapped passes unchanged; a composition that rebuilds its tenant
  service in place passes a lookup that resolves the current one on each call.
- **`server.TenantInfraConfig.TenantLookup` is required** whenever
  `BuildTenantInfra` builds the `IsolationVolume` host registry (`BarrierReg`,
  `TenantSvc` and `VolumePool` all set) (#383). Without it `BuildTenantInfra`
  returns `*server.TenantInfraConfigError{Field: "TenantLookup"}` before any
  side effect. *Migration:* set `TenantLookup` — to the same value as
  `TenantSvc` if that service is never replaced for the life of the process,
  otherwise to a lookup that resolves the current service.
- **`tenant.TenantServicer` gains two methods**, `ListDeletedTenants(ctx,
  pageToken string, pageSize int) (*tenant.DeletedTenantsPage, error)` and
  `ReconcileDeletedTenants(ctx) (*tenant.ReconcileReport, error)` (#470,
  #469), and `transport.TenantClient` gains `ListDeletedTenants` and
  `ReconcileDeletedTenants`. *Migration:* a type that implements either
  interface outside this module must add both methods. A wrapper that embeds
  the interface inherits them. A test double that models no retirements
  returns an empty `DeletedTenantsPage` and a zero `ReconcileReport`.

### Changed

- **`TRUNCATE TABLE` is O(1) regardless of table size.** Relational row and index keys
  carry a generation number; `TRUNCATE` advances it in one transaction and publishes a
  durable reclaim job for the old generation instead of scanning and deleting every row.
  A statement or transaction that raced it fails with MySQL error 1213 / SQLSTATE 40001
  (retry the statement), never with rows from the wrong generation.
- **Index entries are keyed by an index incarnation, not the index name.** An incarnation
  is allocated when the index is created and never handed out twice within a table, so `ALTER TABLE ... RENAME
  INDEX` is a catalog-only change that moves no data, and a new index created under an
  old name never shares a keyspace with the entries being reclaimed for the old one.
- **`DROP INDEX` reclaims its entries in the background.** They are no longer deleted
  inline, so a statement still reading the index is never cut short; one that outlives
  the drop fails with 1213 / 40001.
- **Orphaned keyspace is reclaimed in the background, never under a live reader.** A
  sweeper on every node deletes a truncated generation or a dropped index's entries only
  after every statement that could still be reading them has finished or been failed with
  the retryable error above. Each job is swept by the one node that can reach its store --
  the system leader for the shared store, the publishing node for a node-local store, a
  dedicated Raft group's own leader -- through the same tenant and database view it was
  written through, with bounded work per sweep that resumes where the last one stopped.
- **`DROP TABLE` reclaims its rows in the background.** The catalog entry, generation
  counter and sequences go in one transaction that publishes a reclaim job for the rows
  and index entries, so a statement still reading the table is never cut short, and a
  crash can leave only the whole table or the job that reclaims it. A `DROP TABLE` that
  races a `TRUNCATE` or DDL fails with 1213 / 40001 instead of leaking the new
  generation's rows.
- **The sweeper deletes each page of orphaned keys in one transaction**, one Raft
  proposal per page rather than one per key.
- **A stale table resolution is retryable on every transport.** A statement whose table
  was truncated or whose definition changed while it ran now reaches gRPC, Unix-socket
  and MCP clients as `ABORTED`, QUIC clients as `StatusAborted` and REST clients as 409,
  matching MySQL 1213 / SQLSTATE 40001, instead of an internal error.
- **DML is fenced on the table definition it was planned against.** An `INSERT`,
  `UPDATE` or `DELETE` whose statement resolved the table before a concurrent DDL
  committed fails with 1213 / 40001 instead of maintaining the wrong set of indexes, so a
  new index can no longer miss rows written while it was created.
- **One error classification for every transport.** `pkg/api/errclass` decides what kind of
  failure a typed error is; REST, gRPC (and through it Unix and MCP) and QUIC each render that
  class through one total table of their own. The twelve REST and nine gRPC per-family
  mappers and QUIC's own ladder are deleted, and `TestEveryTransportAgreesOnEveryCondition`
  drives every row of the table through every transport's real mapping. New exported views:
  `rest.ServiceErrorStatus`, `quic.ServiceErrorStatus`, `mcp.ServiceErrorCode`, alongside the
  existing `grpc.ServiceErrorCode`.
- Rows where the transports disagreed now have one answer everywhere (breaking):
  - a system user or role's immutability is 403 / `PERMISSION_DENIED` (gRPC said
    `FAILED_PRECONDITION`), as the identity contract documents;
  - a credential version that is expired or never existed is 404 / `NOT_FOUND` (REST said
    410, gRPC said `FAILED_PRECONDITION` for expired), as the credentials contract documents;
  - removing the last auth factor is 409 / `FAILED_PRECONDITION` (gRPC said
    `ALREADY_EXISTS`), as the auth-factors contract documents;
  - a share claim with no verified client certificate is 401 / `UNAUTHENTICATED` on every
    transport (gRPC said `INVALID_ARGUMENT` from the barrier and tenant-barrier handlers and
    `INTERNAL` from its mapper, QUIC said `PERMISSION_DENIED`);
  - `credentials.ErrInvalidStrategy` and `ErrNilBarrier` are internal faults (gRPC said
    `FAILED_PRECONDITION`).
- Conditions no transport classified, and every transport therefore answered as an internal
  fault, now have a class: a duplicate embedding provider is 409 / `ALREADY_EXISTS`; a
  database barrier operation with no barrier manager is 501 / `UNIMPLEMENTED`; an
  unresolvable reclaim store (reported over whatever its resolver said), a leader transfer
  that did not finish and a shard snapshot export that timed out are 503 / `UNAVAILABLE`; a
  factor migration that left records behind is 409 / `FAILED_PRECONDITION`.
- The REST status, peers, capabilities, shard start/stop/state, topology and rebalance
  handlers answer a service error with its class's status rather than a fixed 500, and a
  malformed `POST /users/lookup` body is a `ReadBodyError` like every other handler's. A
  build gate now fails a REST handler that pairs a fixed status with an error it received.
- **A state machine's refusal keeps its kind** (breaking). `service.SMNotConfiguredError`, the
  one type every non-capability refusal shared, is removed: REST answered it 501 and gRPC and
  QUIC `INTERNAL`, and neither was true of all of it. The encoded result now carries a
  `protocol.AppErrorKind`, and each kind has its own type: an absent subsystem (no lessor, no
  time-series store, create/mod-revision compares on a non-MVCC backend, a transaction on a
  server with no transaction servicer) is `CapabilityUnsupportedError` (501 /
  `UNIMPLEMENTED`), whose message names the missing subsystem (`statemachine: lease not
  configured`, `statemachine: timeseries not configured`) where it said `engine: not open` or
  `pebbledb: store not open` of a store that was open (`engine.NotOpenError`, whose only
  producers those refusals were, is removed); an invalid TTL or time-series point is `CommandRejectedError` (400 /
  `INVALID_ARGUMENT`); a unique-index or lease-ID collision is `CommandConflictError` (409 /
  `ALREADY_EXISTS`); a closed lessor or time-series store is `SubsystemClosedError` (503 /
  `UNAVAILABLE`); an undecodable committed command is `CommandCorruptError` (500 /
  `DATA_LOSS`); any other subsystem failure is `ApplyFaultError` (500 / `INTERNAL`).
  `protocol.EncodeAppError` takes the kind.
- A caller's own deadline or cancellation (`context.DeadlineExceeded`, `context.Canceled`) is
  classified as such on every transport — 504 / `DEADLINE_EXCEEDED` and `CANCELED` — rather than
  as an internal fault of whatever wrapper carried it.
- **The Go SDK's REST client types every 501** as `transport.CapabilityUnsupportedError`
  (inside the `RequestError` that keeps the status), not only the guarded verbs', so the
  go-qrdb#126 exclusion in the conformance matrix is retired: every transport is held to the
  typed capability refusal.
- MCP consults a projected module's own gRPC status only for an error the table does not
  classify, the same order QUIC uses, so a module status can no longer shadow qrdb's own
  classification.
- **go-quicraft v0.11.0-alpha → v0.14.1-alpha** (quic-go v0.62.0 → v0.63.0 with
  it). Two of its breaking changes reach go-qrdb: a Raft member is admitted by
  its node identity alone (below), and every `NewHost` must state its at-rest
  encryption. Every host go-qrdb builds now does — `WithBarrier` where the seal
  barrier encrypts the Raft log as before, `WithoutEncryption` where it did not
  (a seal-disabled daemon, the embedded SDK node, the desktop binding), so no
  node changes what it writes. **Wipe dev data directories before upgrading**:
  go-quicraft changed its WAL chunk and snapshot frame formats in place.
- **A failed `CreateCollection` leaves nothing behind.** It used to publish the
  collection before building its indexes, so a failed index build left a live,
  partially indexed collection that could not be re-created. The create now
  builds the indexes first and unwinds on failure; a retry succeeds.
- **A collection never adopts an index it did not create**, and its drop
  removes only the indexes it owns. A declared index whose name is already
  held by another index refuses the create (409 / `FAILED_PRECONDITION`). Index
  definitions on a service without an index service are refused instead of
  being recorded and silently not built.
- **Collection document writes count against `MaxStorageBytes`**, by body
  bytes, on every transport including SQL. Enforcement is immediate: a tenant
  already over its limit through documents has its next document write
  refused. Every release is of exactly what its own guarded write removed, so
  concurrent updates and deletes never leak or double-release bytes; a drop —
  a caller's or the sweeper's — releases exactly the bytes its teardown
  removed, and a `field_hash` re-insert of an existing natural key is charged
  once, not once per insert. The remaining drift is an over-count from writes
  that race a drop or have an unknown outcome; it is permanent for prefix and
  raft tenants (docs/collections/engineering.md).
- **Each create of a collection keeps its documents and its `increment`
  counter in a keyspace of its own**
  (`__col:{database}:{collection}#{incarnation}/`,
  `__collection:{collection}#{incarnation}`). A collection created before this
  release keeps the unsuffixed keys it already has; no migration is needed.
  A new collection's name may no longer contain `#`
  (`service.CollectionInvalidNameError`, 400 / `INVALID_ARGUMENT`); an existing
  collection with such a name keeps working.
- **A document field named `id`, or an object-valued `fields` as a
  document's only field, is refused** (`service.CollectionReservedFieldError`,
  400 / `INVALID_ARGUMENT`) instead of being accepted, charged against quota,
  and stored as something else -- the `id` field silently dropped, the
  `fields` object unwrapped into the document.
- **`DeleteDocument` refuses a collection that is being created or dropped**,
  like every other document verb (409 / `FAILED_PRECONDITION`); the drop
  removes every document itself.
- **`REPLACE INTO`, `INSERT … ON DUPLICATE KEY UPDATE` and `UPDATE` on a
  collection table are guarded writes.** A concurrent writer's value is never
  lost silently; the losing statement fails with MySQL 1213 / `SQLSTATE 40001`
  and can be retried. An `UPDATE` that changes `_id` and cannot tell whether
  its release of the old `_id` took effect leaves both documents and says so
  (`provider.DocumentMoveOutcomeUnknownError`) instead of withdrawing the new
  copy.
- **Breaking (embedders):** `statemachine.IndexManager.DropIndex` takes
  `protocol.IndexDropOptions`, and an `IndexManager` must enforce the owner
  carried in `IndexCreateOptions.Owner`; `index.IndexServicer` gains
  `DropOwnedIndex`; `service.CollectionServicer` gains `UpsertDocument`, so
  every implementation or test double of it must add the method.
- **The explicit index API can no longer drop an index a collection owns**;
  it is refused with 409 / `FAILED_PRECONDITION`. Drop the collection instead.
  Explicit and pre-ownership indexes drop as before.
- **Upgrade every node before issuing collection creates or drops.** The
  collection lifecycle, index ownership and the owner-checked index drop are
  decided on every replica's apply path; a node still running an earlier
  release ignores the owner on an index command and would apply an
  owner-checked drop unconditionally. Rolling upgrades are safe while no
  collection is created or dropped.
- **A module can no longer silently take over a REST route.** Built-in routes
  used to yield to a module that claimed the same method and pattern — and so
  did a second module claiming the first one's route. Every REST route now has
  exactly one owner: a second claim fails server construction with
  `rest.RouteCollisionError` naming both claimants. The guard lives in qrdb,
  where the live router is, so a host mounting a module needs no copy of the
  built-in route table. **Breaking** for a module that relied on overriding a
  built-in: move its routes under its own prefix. A host that re-registers
  qrdb's built-in routes from its own modules — one known downstream host
  re-registers 22 of them — now fails construction with
  `RouteCollisionError` on upgrade and must drop those registrations.
- **Route identity follows chi.** `middleware.RESTRouteKey` erases path-parameter
  names and keeps regexp constraints, so `/a/{id}` and `/a/{deviceID}` are one
  route to the registry as they are to chi.
- **Direct router registrations are recorded.** A module route registered on
  `HTTPRegistrar`'s embedded router that collides with a permission-recorded
  route fails construction with `rest.DirectRouteCollisionError`, and
  registering after `RegisterHTTP` returns panics with
  `apimodule.RegistrationClosedError`. **Breaking**: a module may no longer
  call `Mount`, `Route`, `NotFound`, `MethodNotAllowed` or `Use` on the
  registrar's router — each serves requests no registration describes — and
  gets `apimodule.UnsupportedRouterMethodError`; serve a subtree as
  `Handle(method, "/prefix/*", perm, h)`. An unmatched request under a module's
  route prefix is refused with 404 instead of reaching the router's fallback.
  New: `apimodule.RouteClaim`, `apimodule.UnsupportedRouterMethodError`,
  `HTTPRegistrar.DirectRoutes`, `HTTPRegistrar.Freeze`,
  `RESTRouteRegistry.RegisterModulePrefix`/`UnderModulePrefix`.

### Removed

- 46 exported error types that no code path returned, each with a passing test that could
  only observe its own `Error()` string (go-qrdb#243), across `pkg/api`, `pkg/api/etcd`,
  `pkg/api/quic`, `pkg/audit`, `pkg/dao`, `pkg/entity`, `pkg/lease`, `pkg/mvcc`,
  `pkg/planner`, `pkg/rag`, `pkg/routing`, `pkg/service`, `pkg/sql/*`,
  `pkg/storage/partition` and `pkg/vector/*`, plus two SDK types nothing produced:
  `transport.BackupVerifyError`, which only an unreachable embedded-transport arm returned
  (backup verification reports a failed check in its result, never as an error), and
  `transport.ImbalanceReportError`, which no transport constructed. A caller matching one with `errors.As` had
  written a branch that could never be taken.

### Fixed

- `DROP TABLE` deletes a table's `TRUNCATE` generation counter in the same transaction as
  its catalog entry, and refuses to commit over a `TRUNCATE` that raced it, so a failed or
  interrupted drop can no longer leave a resolvable table reading generation 0 or a counter
  nothing will ever delete.
- A read-only transaction that first touches a table after a `TRUNCATE` or DDL committed
  since its snapshot fails with 1213 / 40001 instead of returning no rows -- including
  through a table handle resolved before a `TRUNCATE` its snapshot already includes.
- A dropped index can no longer be resurrected, and its incarnation handed out again, by
  an `ALTER` rewrite completing over a definition that changed after the rewrite began.
- A long statement that re-proves its table mid-rewrite checks the index incarnations it
  reads, not only the generation.
- `CREATE INDEX`'s build is fenced on the definition listing the new index, so a `DROP
  INDEX` landing mid-build fails the build instead of leaving orphaned entries.
- `ALTER TABLE ... DROP COLUMN` and a type-changing `MODIFY COLUMN` rebuild every index on
  the column under a new index incarnation. The old entries, which were encoded for the old
  column set, are reclaimed in the background, and so are the entries of an index the drop
  leaves with no columns. Before, the index kept those stale entries or leaked them. A
  `UNIQUE` index narrowed onto repeating values now fails the `ALTER` and leaves the table
  unchanged.
- QUIC answers precisely for every family its ladder never listed — identity, tenants,
  credentials, PINs outside the barrier handlers, auth factors, ceremonies, migrations,
  backup, collections, embedding, RAG, SQL, databases, quotas and placement — instead of
  `StatusError`. Because the Go SDK's QUIC transport rebuilds `transport.KeyNotFoundError`
  from `StatusNotFound`, a missing tenant, credential, factor, officer or backup now surfaces
  to an SDK-over-QUIC caller as `KeyNotFoundError` where it used to be a generic request
  error.
- The relational catalog (tables, views, sequences), typed-entity, paging and SQL-privilege
  errors are classified on every transport: their request guards answer 400 /
  `INVALID_ARGUMENT`, missing objects 404, taken names 409 / `ALREADY_EXISTS`, lost
  compare-and-swaps 409 / `ABORTED`, a table mid-rewrite 503, and catalog corruption 500 /
  `DATA_LOSS`; `service.SubsystemNotConfiguredError` is 501 / `UNIMPLEMENTED`. All of them
  answered 500 / `INTERNAL`.
- gRPC, Unix and MCP classify the backup, embedding, RAG, SQL, database, quota and placement
  families, `seal`'s already-initialized/not-initialized/already-unsealed/invalid-credential
  refusals and `authfactors.ErrHardwareUnavailable`, which fell to `INTERNAL`; REST
  classifies credential re-submission (409), a disabled user record (409), a draining node
  (503) and an invalid ceremony authenticator type (400), which fell to 500. Authentication
  and authorization refusals that reach a service mapper answer 401/403/429/503 on every
  transport, as QUIC's already did.
- gRPC handlers no longer build a status from an error's own text: 161 inline
  `status.Error(code, err.Error())` sites render through the one mapper, and a build gate
  (`test/buildgates/error_mapping_test.go`) fails the next one. The gRPC transport's own
  decode refusals (metadata, vector ID, TTL, document, signature, timestamp) declare their
  class.
- **The vector service's errors reach callers as what they are.** `VectorService` reported
  every failure through the untyped `pkg/errors.QRDBError` code vocabulary, which no transport
  mapped, so 68 of its 100 error sites answered 500: a missing namespace, a dimension mismatch,
  a duplicate namespace and a strict-isolation refusal were all "internal server error". It now
  returns the service's typed errors — `NamespaceNotFoundError`, `VectorNotFoundError`,
  `NamespaceExistsError`, `DimensionMismatchError`, `InvalidVectorError` (all five declared,
  mapped and never constructed until now), plus the new `VectorJobNotFoundError`,
  `VectorServiceClosedError` and `VectorOperationError` — and a storage error is typed by the
  sentinel storage raised rather than by the call site, so a storage fault during a lookup is
  no longer reported as "not found". Breaking for any caller that matched `QRDBError.Code` on a
  vector call; match the types with `errors.As`.
- **Batches on the non-default state machines are all or nothing.** They were
  applied key by key: a concurrent read could see part of one, a crash could
  leave part of one on disk, and the service layer's batch opcodes were
  refused.
- **The `disk` state machine closes its storage when it closes.** It left the
  Pebble store open, holding its directory lock, so a replica restarted in the
  same process could not reopen its own data.
- **The `disk` state machine commits each entry's log index with its writes**
  — single-key guarded verbs as well as transactions — and the `fast` state
  machine starts each replica from an empty keyspace (one storage batch), so
  neither can replay an entry over its own result after a restart and decide
  it the other way.
- **`List` and `Scan` on the `concurrent` and `highperf` state machines never
  see part of a transaction or batch**: they hold every shard's read lock for
  the whole sweep.
- **Clustered nodes form again on go-quicraft ≥ 0.13** (#556). A node with a
  Raft transport admits a member by its node identity (`spki:` + SHA-256 of the
  certificate public key) and by nothing else, and `NewClusteredNode` and
  `qrdbd start` never supplied one, so a multi-node cluster was refused at
  `StartShard`. The other replicas' identities are now configured in
  `raft.member_identities` / `--member-identities` (`replicaID=spki:<hex>`),
  the node's own is derived from its transport certificate, and a local entry
  that contradicts the certificate is refused before anything is written.
  `qrdbd node-identity --cert FILE` and `node.MemberIdentityFromPEM` print a
  node's identity from its certificate; each node also logs its own at start.
  A dedicated per-database Raft shard founded on a clustered host states the
  local replica's identity too.
- **Every add path commits the replica's member identity** (#556). go-quicraft
  commits identities in the shard membership; go-qrdb now reads them from
  there (`SyncGetShardMembership`) and calls the `*WithIdentity` APIs: member
  add, rebalance, dynamic membership, the placement reconciler's followers and
  promotions, founding a multi-replica dedicated database, and the admin
  `StartShard` (which builds `MemberIdentities` as the founders do). **Breaking
  (SDK):** `AdminClient.AddMember`, `AdminServicer.AddMember` and
  `ClusterClient.AddReplica` take an optional identity, carried on all six
  transports and by `qrdb admin member add --identity`; `service.Host` swaps
  `RequestAddNode` for `RequestAddNodeWithIdentity` and gains
  `SyncGetShardMembership` and `NodeIdentity`. A brand-new node needs the
  identity supplied once (a typed refusal says how); one that contradicts a
  committed identity is refused, for the system shard too, and an address the
  committed membership binds to two identities is refused as ambiguous, as is
  a replica ID the membership already places at another address. A
  target shard that exists but cannot be read is a retryable refusal rather
  than a fallback to the host's identity. Rebalance refuses to move the system
  shard (`*service.RebalanceSystemShardMoveError` — every host already hosts
  it) or to add a shard to a host its committed membership already names as a
  voter, observer or witness, picks a replica ID past every one the membership
  has used, and logs every refused move with its typed cause.
- **`node.Node.Close` is ordered against `Unseal` and `EnableEncryption`** (#226).
  Per the owner's ruling, `Close` now blocks on an in-flight boot or
  system-scope migration and then tears down what it built, so a boot can no
  longer republish the data plane after `Close` returned and a migration can
  no longer run against a barrier `Close` retracted. A boot or migration still
  waiting when `Close` begins is refused rather than queued, so a graceful
  server stop inside `Close` cannot wait on a transport-delivered
  `EnableEncryption` that is waiting on `Close`. A tenant-scope migration is
  admitted shared (`server.TenantMigrationAdmission`), so `Close` waits for it
  too and refuses a late one; this supersedes the 0.21.0 `migrateMu`, which
  covered system-scope migrations only. `Close` is idempotent;
  `Unseal` and `EnableEncryption` on a closed node return the new sentinel
  `node.ErrNodeClosed` (previously `EnableEncryption` answered a closed node
  with `*MigrationNotSupportedError`). It is `service.ErrNodeClosed`, which
  every transport answers as retryable — 503 on REST, Unavailable on gRPC,
  unix, QUIC and MCP — where a transport-delivered refusal previously read as
  an internal error.
- **Raft-backed SDK tests no longer charge node boot to the calls under test**
  (#418). Each `sdk/go` raft coordination test took its 60s call budget before
  booting its node, so on a starved host the election consumed it and a lease
  call failed with `quicraft: timeout` — reproduced under load exactly as the
  docs-only PR saw it. The budget (`raftCallBudget`) now starts once the node is
  writable; readiness waits log how long they took and fail as "SLOW, not
  broken" when a leader accepted the write but the commit starved, apart from
  "no leader". The placement-reconciler tests wait for the watch subscription
  and for convergence instead of sleeping 10–30ms and allowing 500ms.
- **A tenant-scope `enable-encryption` works after a system-scope one** on an
  embedded node (#383). `TenantHostRegistry` resolved each tenant's `DataRoot`
  through the first boot's tenant service, which a system-scope migration stops
  and replaces, so every later tenant-scope migration failed with "storage is
  closed". The registry now takes a `server.TenantLookup` consulted at use
  time, and `server.TenantInfraConfig.TenantLookup` is required whenever the
  `IsolationVolume` registry is built. `NewTenantHostRegistryFromServicer` is
  renamed `NewTenantHostRegistryFromLookup` and accepts any `TenantLookup`.
- **`node.Client().Tenants()` works.** The embedded client of an
  `sdk/go/node` node was never given the tenant service, so every
  `TenantClient` verb answered "TenantServicer must not be nil" while the same
  verbs worked on every network transport of the same node.
- **Creating a tenant is atomic against deleting it** (#484). `CreateTenant`'s
  tombstone check was a read and its metadata write a plain put, so a delete
  landing between them left a live tenant record, and for `volume`/`raft` a
  barrier restored on every restart, under a permanently retired id. The
  record is now committed through the new `kvstore.FencedCreator`
  capability (`PutIfAllAbsent`): written only while the record and the
  tombstone are both absent, in one transaction on the replicated store. A
  create that loses to a retirement destroys the barrier it provisioned; two
  concurrent creates of one id have exactly one winner. A metadata store
  without the capability is refused with `tenant.ErrTenantStoreNotAtomic`
  before anything is provisioned. `seal.MemoryBackend`, `seal.StorageAdapter`,
  `seal.StorageBarrier` and `kvstore.MemoryKVStore` implement it; the
  seal-disabled tenant service now runs over a transaction-backed adapter.
- A write into a collection that was being dropped could land after the drop
  had listed its documents and outlive it; document writes are now fenced
  against a concurrent drop and retracted if they lose.
- The schema discovery an insert performs could write a dropped collection's
  metadata back into existence, or revert a drop in progress; metadata writes
  are now guarded swaps.
- A drop that resumed after the collection had been dropped by someone else and
  created again could delete the new collection's documents, restart its
  counter and remove its metadata, and a write decided against the old
  collection could overwrite and then delete a document of the new one. Every
  create now has its own document keyspace and counter, so nothing working on
  an earlier incarnation of a name can reach a later one.
- Listing a collection's documents sorted every key with an insertion sort,
  quadratic in the collection's size; it now uses the standard library's sort.

## [0.21.3-alpha] - 2026-09-30

Security hotfix: tenant isolation on the tenant-administration surface of every network transport (REST, gRPC, Unix socket, QUIC, MCP).

### Changed — BREAKING

- **A tenant's plan is set by the platform operator only (owner ruling
  2026-09-29).** `tenants:create`, `tenants:set_quotas`,
  `tenants:set_entitlements` and `tenants:set_isolation` are now cross-scope
  (`ScopeCross`, `pkg/permission/tenant_provisioning_scope.go`) instead of
  self-scoped: only an identity in the `system` tenant can exercise them. A
  tenant's own Admin keeps managing everything inside its tenant, but can no
  longer create tenants, raise its own quotas, grant itself entitlements or
  change its own isolation tier. The built-in Admin role carries all four in
  every tenant, like its other cross-scope grants, and gains
  `tenants:set_quotas`; outside `system` they are inert. The SO role's
  `tenants:create`, `tenants:set_isolation` and `tenants:set_entitlements`
  grants are cross-scope for the same reason.
- **The quotas operation checks `tenants:set_quotas`, not `tenants:update`**,
  on every transport: `POST /v1/tenants/{id}/quotas`, gRPC/Unix
  `TenantUpdateQuotas` (the `qrdb.v1.permission` proto option), QUIC
  `tenant.UpdateQuotas` and MCP `qrdb_tenant_update_quotas`. A custom role that
  granted quotas through `tenants:update` must grant `tenants:set_quotas`.
- **`tenants:update` covers only the display name.** An update that changes
  `isolation_type` also needs `tenants:set_isolation` and is refused whole
  without it.
- **`tenants:list` returns only the caller's own tenant** unless the caller
  holds `tenants:list:cross` against the `system` tenant. The
  `tenants:list:cross` check is a capability probe: it charges no rate-limit
  token and writes no audit record.
- **`tenants:delete` stays self-scoped (owner ruling 2026-09-29):** a tenant's
  Admin may delete its own tenant, and no other.
- **A tenant can no longer re-scope a built-in permission.**
  `permission.TenantScopeStore.SetScope` refuses a permission that has a
  built-in scope with `*BuiltInScopeOverrideError`, and `ResolveScope` fails
  closed on a stored definition of one. Per-tenant scopes remain available for
  caller-defined permissions.
- **Changing a tenant's isolation type is refused unless the tenant is
  verifiably empty, on every transport.** The check moved from the REST handler
  into the tenant service (`tenant.IsolationChangeWiring`,
  `service.TenantDataProbe`, wired by `server.New`). With no data probe wired,
  or when the probe fails, the change is refused (`ErrTenantDataUnverifiable`,
  `*TenantDataProbeError`: 503 / `UNAVAILABLE`); an embedder constructing a
  tenant service directly must wire one with `tenant.WithTenantDataProbe` or
  `SetTenantDataProbe` to change isolation types. QUIC now answers
  `ErrTenantHasData` with `StatusFailedPrecondition` instead of the generic
  error status.

### Security

- **A tenant's Admin could read, change and delete ANY tenant.** Every network
  transport authorized `tenants:read`, `tenants:update`, `tenants:delete` and
  the quotas and entitlements operations against the CALLER's tenant, where a
  tenant-local Admin holds them, and then acted on the tenant the request
  named. Proven on v0.21.1-alpha with the built-in roles: an `acme` Admin
  answered 200/204 reading, renaming, re-quota-ing, re-entitling and deleting
  `globex`. Each handler now re-decides against the tenant the request names
  (`AuthMiddleware.AuthorizeTenantTarget`, `AuthorizeTenantUpdate`); the
  re-check charges no second rate-limit token and records only a refusal.
- **A tenant's Admin could create tenants and raise its own plan.** An `acme`
  Admin created tenants (201) and set `acme`'s own quotas and entitlements
  (200). Closed by the cross-scope plan permissions above.
- **`tenants:list` disclosed every tenant, `data_root` included, to any tenant
  Admin.** It is narrowed to the caller's own tenant
  (`AuthMiddleware.FilterTenantList`); a refusal the audit policy could not
  record still refuses the request rather than answering it.
- **The barrier bootstrap windows overrode the rate limiter.** The
  trust-on-first-init and sealed-window fallbacks read a `*RateLimitedError` as
  an RBAC denial they could override, so a rate-limit policy on
  `tenant_barrier:init` or `tenant_barrier:unseal` was never enforced for the
  callers those windows admit (a `Burst: 1` unseal policy admitted every
  unseal). Both now return the rate-limit refusal immediately, beside the
  audit-failure check: 429 / `RESOURCE_EXHAUSTED`.
- **A tenant id containing `/` (or `\`, a control byte, `.` or `..`) is a 400 /
  `INVALID_ARGUMENT` on every transport**, checked before authorization on
  every by-id operation. On create it was a 500 / `INTERNAL`
  (`*tenant.InvalidTenantIDCharError` was unmapped); `tenant.ValidateTenantIDShape`
  is now exported for the transports.
- **gRPC, the Unix socket, QUIC, MCP and the embedded transport changed the
  isolation type of a tenant that held data.** Only REST checked that the
  tenant was empty first, so a change through any other transport orphaned
  the tenant's data in its old namespace. The tenant service now enforces it
  for every transport (see above).
- **A tenant could re-scope a built-in permission** through its per-tenant
  scope definitions -- for example back to self-scope for the tenant plan
  permissions. Refused (see above).
- **RBAC resolved an identity's permissions under the request's target
  tenant.** The role store refuses a role outside the context tenant, so a
  `system` operator's first decision against another tenant failed whenever
  the permission cache was cold. The network transports masked it only because
  their entry check, made against the caller's own tenant, warmed the cache
  first. Permissions are now resolved under the identity's own tenant, as the
  SO and Owner role checks already were.

### Fixed

- **Integration fixtures that created users without an email.** Since email
  became the tenant-unique login key, `POST /users` without one is a 400, and
  the raft-isolation, enable-encryption, tenant CLI, SQL and user/role
  integration tests failed at fixture setup before reaching what they test.
  They now pass an email.

## [0.21.2-alpha] - 2026-09-30

Security hotfix: the cluster CA private key never leaves the node that holds it (/v1/ca/bootstrap and every enrollment method serve the certificate only). Treat existing cluster CA keys as compromised; joining new nodes is unavailable until member-side issuance lands.

### Security

- **The cluster CA private key no longer leaves the node that holds it, in
  any form.** Before this release, `GET /v1/ca/bootstrap` served the cluster
  CA certificate and **the CA private key** on every first node of a
  multi-node cluster. The route is public (it is reached before the caller
  holds a credential), so anyone who could reach a first node's admin port
  could read the key and mint any cluster node identity. Every node that
  joined over SPKI-pinned TLS, DANE or direct HTTPS also received the key and
  wrote it to its data directory as `ca-key.pem`. The Noise NK advertiser
  handed the same key-bearing bundle to go-truststrap, whose handler stripped
  the key before it reached the wire. Now `/v1/ca/bootstrap` and the Noise
  advertiser serve the CA certificate only. Any key material handed to them
  is filtered out (`clustertls.CertificatesOnly`), and a joining node never
  writes `ca-key.pem`.

  **Treat the cluster CA key of every existing multi-node cluster as
  compromised.** It was readable without authentication from every first
  node's admin port, and it sits on disk on every joined node. Rotate the
  cluster CA:
  1. Generate a new cluster CA on one node, the one that will hold it.
  2. Issue a new node certificate for every member from the new CA.
  3. Restart the members with a trust bundle holding both CAs, so old and new
     certificates keep verifying during the change.
  4. Once every member presents a certificate from the new CA, remove the old
     CA from the bundle.
  5. Delete `ca-key.pem` from every node except the one holding the new CA.
  6. Reissue client and officer certificates that chain to the old CA.

  Until the cluster CA has been rotated, restrict network access to every
  node's admin port. See `docs/clustering/user.md`, "Adding a node".

### Changed — BREAKING

- **Joining an existing cluster through enrollment is unavailable in this
  release.** A node started with bootstrap flags (`--bootstrap-noise-key`,
  `--bootstrap-spki-pin`, `--bootstrap-dane-dns`, `--bootstrap-direct-url`)
  refuses to start with `*clusterenroll.EnrollmentUnavailableError`
  (`EnrollmentUnavailableError` in `cmd/qrdbd` and `sdk/go/clusterenroll`).
  The refusal comes before any network dial and before anything is written
  to the data directory. Earlier releases enrolled a node by giving it the CA
  private key. Member-side issuance, where the joining node submits a CSR
  that a running member signs, lands in a following release. Until then, add
  a node by issuing its certificate on the node that holds the cluster CA and
  starting it with `--tls-ca-file`, `--tls-cert-file` and `--tls-key-file`.
  This affects `clusterenroll.Join` and the join paths of `Resolve` and
  `ResolveCA`. `ResolveCA`'s join branch no longer returns a CA key, because a
  joining node has none. `FetchAnchor` and the first-node and single-node
  paths are unchanged.
- **`WithCABootstrap` takes the CA certificate only**, in `pkg/api/rest`
  and `pkg/server`: `WithCABootstrap(certPEM []byte)`. The key parameter is
  gone. Input with no `CERTIFICATE` block registers no route.
- **Removed:** `clustertls.GenerateLeafFromCA` and its `sdk/go/clustertls`
  re-export, which minted a leaf from a CA key received over the network.
  Also removed: `clusterenroll.JoinError` (`sdk/go/clusterenroll.JoinError`,
  `cmd/qrdbd.BootstrapError`), which only that path returned.
  `clustertls.CertificatesOnly` is added.

## [0.21.1-alpha] - 2026-09-29

Security hotfix: REST percent-encoded path authorization bypass (authorize and route on the same decoded path).

### Security

- **REST authorization and routing no longer disagree on percent-encoded
  paths.** Authorization resolved the decoded `r.URL.Path`, but chi routed on
  `r.URL.RawPath` whenever the request carried an escape such as `%2F`, so a
  request could be authorized as one route and served by another. Proven on
  v0.21.0-alpha: `DELETE /v1/databases/prod%2Fx` dropped database `prod` for
  any authenticated caller under a deny-all authorizer (the same shape reached
  `/admin/shards/{id}`, `/ts/series/{metric}` and `/users/{id}`); a module route
  authorized under a weak permission could be served by a stronger route's
  handler; and a public route's decoded path could open a protected handler
  with no authentication. The REST handler chain (shared by the HTTP/3
  listener) now canonicalises every request to its decoded path before the
  sealed-window, authentication, SO and routing layers, so all of them resolve
  the same route. Wildcard routes such as `/kv/*` still receive keys with
  encoded characters, decoded, as before; a name containing `/` is not
  addressable through a single-segment route. Upgrade immediately.

## [0.21.0-alpha] - 2026-09-29

Merge train 1: go-enclave protected memory, atomic boot-time identity claims, QUIC qrdb-quic/2, per-tenant auth-factor challenges, PQ k-means empty-cluster repair.

### Added

- **`sdk/go/secret.SealFailedError`** re-exports `pkg/secret.SealFailedError`
  (go-qrdb#114 review), so an SDK-only consumer can `errors.As` against a
  `NewSecretValue` sealing failure without importing the internal `pkg/secret`
  path.
- **`sdk/go/secure.ErrBudgetExhausted`, `ErrLockedMemoryUnavailable`,
  `ErrReleasedWithoutWipe`, `ErrSealerWiped`, `ErrSealFailed`,
  `ErrMaterialTooLarge`, `ErrSealedAuthentication`, `ErrPlaintextEscaped`,
  `ErrInvalidBudget`, `ErrNilAllocator`, `ErrBudgetExceedsRLimit`,
  `ErrLimitDiscoveryFailed`**, and the **`RLimitError`/`LimitDiscoveryError`**
  types, re-export the go-enclave errors this package's own public API can
  actually return (go-qrdb#114 review), so an SDK-only consumer can
  `errors.Is`/`errors.As` against them without importing raw `go-enclave`.

### Changed — BREAKING

- **`pkg/secure`/`sdk/go/secure` no longer wrap memguard; they wrap
  `github.com/jeremyhahn/go-enclave` v0.6.0-alpha (go-qrdb#114, go-qrdb#553).**
  `Buffer` and `Secret`'s own public API (`NewBuffer`, `NewBufferFromBytes`,
  `Bytes`, `WithBytes`, `Size`, `Destroy`, `Wipe`, `NewSecret`,
  `NewSecretFromBuffer`, `Open`) is UNCHANGED — a failed allocation now
  returns a typed, recoverable error (`errors.Is` against
  `enclave.ErrBudgetExhausted` / `enclave.ErrLockedMemoryUnavailable`, or
  `*InvalidSizeError`) instead of memguard's process-wide purge-then-panic.
  `Buffer` gains `DestroyErr() error`, the checked form of `Destroy()` that
  surfaces `enclave.ErrReleasedWithoutWipe` (a denied mprotect during
  release) instead of discarding it — `Destroy()` itself is unchanged and
  still discards it, for the many existing `defer buf.Destroy()` call sites.
  What IS removed, with no replacement in `pkg/secure`:
  - `secure.CatchInterrupt()`, `secure.EnablePurge()`, `secure.Purge() error`,
    `secure.PurgeOrPanic()`, `secure.NotInitializedError` — these belonged to
    memguard's process-signal-driven purge lifecycle, which go-enclave does
    not have. **Migration:** a binary that owned this lifecycle now installs
    a process-wide locked-memory budget once at startup with
    `sdk/go/secure.SetAllocator` (built via
    `sdk/go/secure.NewAllocatorWithinRLimit`) and sweeps it on orderly
    shutdown with `sdk/go/secure.WipeAll() (int, error)` — see
    `cmd/qrdbd/start.go`'s `runStart` for the reference wiring, including
    treating `sdk/go/secure.ErrAllocatorInstalled` as non-fatal and checking
    `WipeAll`'s remaining-enclave count. Neither call installs a signal
    handler; the owning binary keeps driving its own graceful shutdown.
  - `pkg/secure/analyzer`'s rule 1 (no raw `memguard` import) no longer
    exempts `pkg/secure` itself — the import is banned everywhere in this
    repository now, with no carve-out, since `pkg/secure` no longer needs it.
- **`SecretValue.WithBytes` (`pkg/secret`, re-exported by `sdk/go/secret`)
  now holds the transient buffer's borrow for the callback's entire call
  (go-qrdb#114 review).** Previously the callback received an alias whose
  borrow had already been released, so a concurrent `WipeAll` could unmap the
  pages while it was reading them. Now a `WipeAll` during the callback skips
  that buffer and counts it in its remaining total, and, with the borrow guard
  armed (`GuardBorrows` / `EnableBorrowGuard`), a callback that calls back into
  `pkg/secure` / `sdk/go/secure` is reported as a nested borrow. **Migration:**
  a callback must not retain the slice (unchanged), should not call back into
  the secure-memory package, and should not block longer than a shutdown
  sweep can wait for it.
- **`ConfigProcessor.Process` fails closed when a resolved plain-string value
  cannot be read back (go-qrdb#114 review).** It used to write `""` into the
  field and count it resolved; the field now keeps its reference, the path is
  reported `Unresolved`, and the new `secret.ResolvedReadError` (re-exported by
  `sdk/go/secret`) is recorded in `Errors`.
- **An unconfigured `server.secure_memory_budget_mib` is derived from the
  host (go-qrdb#114 review; owner ruling 2026-09-25).** The default is now `0`,
  meaning `qrdbd start` installs the smaller of 64 MiB and `RLIMIT_MEMLOCK`
  less 2 MiB of headroom, and logs the figure and its source. A stock 8 MiB
  container limit therefore starts with a 6 MiB budget instead of failing. A
  limit that leaves less than 1 MiB fails `start` with
  `SecureMemoryLimitTooSmallError`. An explicit budget above the limit still
  fails `start`, and the error now says how to raise the limit. The compose
  files, the devcontainer and the runtime image now ship or document
  `IPC_LOCK` and an unlimited memlock ulimit; see `docs/deployment/`.
  `sdk/go/secure` re-exports `DiscoverLockedMemoryLimit`,
  `LockedMemoryLimit` and `LimitSource`.
- **`secure.NewSecretFromBuffer` seals straight from the input buffer's
  pages** — no intermediate plaintext copy, and no admitted allocation, so an
  exhausted budget no longer makes it fail. The input is consumed on every
  path past the nil/destroyed checks, including a sealing failure.
- **`pkg/secret.NewSecretValue` returns `(*SecretValue, error)`, not
  `*SecretValue` (go-qrdb#114 review).** It used to swallow a
  `secure.NewSecret` sealing failure and return a `SecretValue` whose
  accessors silently reported empty — indistinguishable from a reference that
  legitimately resolves to nothing, so a credential could vanish from
  configuration with no error anywhere. A sealing failure is now
  `*secret.SealFailedError`. `sdk/go/secret.NewSecretValue` is a function-value
  alias and inherits the new signature automatically; every caller — in this
  repository and downstream (go-xkms's `NewLiteralSecretValue` wraps this
  function) — must be updated to check the error.
- **BREAKING: `qrdbd start` refuses every `--sm` / `raft.sm_type` other than `pebble`, `mvcc`
  and `mvcc-fast`, on EVERY start, not only the first.** `memory`, `standard`, `concurrent`,
  `fast`, `disk`, `highperf` and `regular` have no transaction subsystem, and role and user
  creation now commits atomically through one. The check runs right after leader election and
  exits with `auth bootstrap failed at "atomic role/user creation requires an --sm state
  machine with transaction support"`. **An already-bootstrapped deployment on one of those
  types stops starting after this upgrade**, and cannot switch type because its data belongs to
  that state machine: stay on the previous release until that state machine gains transaction
  support. `configs/qrdb-dev.yaml` moves from `sm_type: memory` to `pebble` so it still starts;
  it returns to memory once every state machine supports guarded batches. A follower defers to the
  leader's probe (only the leader can propose); any other inconclusive probe also stops the
  start.
- **BREAKING: the QUIC listener accepts only the ALPN protocol `qrdb-quic/2`.**
  The frames are unchanged; under `qrdb-quic/2` the client closes its side of
  each request stream only after it has read the response, and the server
  waits (bounded) for that close before it counts a request as finished. The
  listener no longer accepts `qrdb-quic`, and the Go SDK offers only
  `qrdb-quic/2` and refuses a connection on which anything else was selected.
  A Go SDK older than this release cannot connect to a current server, and a
  current Go SDK cannot connect to an older server: upgrade both together.

### Fixed

- **Multi-node first boot no longer races on built-in roles and the admin user.** A role or
  user create commits its identity claim, primary record and every secondary index as ONE
  guarded proposal (`kvstore.MultiGuardedBatchKVStore.PutAllIfAllUnchanged`), so a leadership
  change can no longer leave a claim without its record. A role guards its id claim and its
  name together; a user guards its id claim, username and optional email together. First boot
  verifies that a built-in role or admin already created by another node matches the canonical
  definition instead of trusting the collision.
- A lost response on that proposal is reconciled by polling the written keys (every 10 ms, up
  to 5 s) rather than one read, so a create that committed on the cluster but was not yet
  applied locally is not reported as failed and still emits its audit event. A losing create's
  conflict diagnosis polls the same way, so a node reading stale local state still gets the
  typed already-exists error first boot resolves.
- A guard expecting a present, empty value (`kvstore.Guard{Value: []byte{}}`) no longer matches
  an absent key.
- `user.UserIDExistsError` (re-exported as `sdk.UserIDExistsError`) is a conflict on every
  transport: REST 409, gRPC / Unix `ALREADY_EXISTS`, QUIC `StatusAlreadyExists`, MCP
  `CodeAlreadyExists`. It previously fell through to an internal error.
- QUIC answers a taken username (`user.UsernameExistsError`) or email
  (`user.EmailExistsError`) with `StatusAlreadyExists`, as REST and gRPC already did, instead
  of the generic `StatusError`. A failed rollback wrapping any of the three stays
  `StatusDataLoss`.
- **Four production faults in the enable-encryption migration path are fixed.**
  `sdk/go/node`'s `Node.Close` could segfault when it ran concurrently with a
  still-in-flight system-scope encryption migration; it now blocks on the
  same lock the migration holds for its entire recycle, so Close always
  waits for an in-flight migration to finish before tearing anything down.
  The QUIC server could sever a request's connection before it delivered an
  already-decided `"migration_started"` response, reporting "server shutting
  down" instead of the real answer when a migration's synchronous phase
  finished faster than the accept window; it now waits for in-flight
  requests, each confirmed delivered by the `qrdb-quic/2` acknowledgement,
  before closing connections, and ends open watch streams at once instead of
  waiting out its grace period behind them. The recycle could then fail to
  restart the QUIC listener with "address already in use", leaving a
  migrated node without one; the listener now releases its UDP port before
  its stop returns. A node with no seal store could segfault the same way
  on a nil concrete storage handle passed through an interface parameter;
  the seal store is now held as `seal.LegacyStorage` end to end, and a node
  without one refuses before tearing anything down.
- **Enable-encryption errors are classified the same way on every transport.**
  `sdk/go/node`'s `MigrationNotSupportedError` matches
  `service.ErrMigrationProviderUnavailable` through `errors.Is`, so REST
  answers `501` and gRPC, unix, QUIC and MCP `Unimplemented`; REST and gRPC
  previously answered it as an internal error. QUIC now answers every
  migration error (invalid target or option, source not `none`, migration
  in progress, shared-barrier tenant) with the status gRPC gives it instead
  of its generic `StatusError`. A tenant-scope request arriving while a
  system-scope migration is rebuilding the data plane gets the new
  `service.DataPlaneRecyclingError`, `503` / `Unavailable` (retry),
  instead of a refusal, and a failure inside a migration the node accepted is
  the new `MigrationReopenError`, not a refusal.

### Changed

- The routed role/user services an embedded node exposes ask the state machine whether it
  supports guarded batches (`kvstore.ProbeGuardedBatch`) instead of trusting the store's type,
  and only when a role or user is CREATED in the system tenant; reads, lookups and permission
  resolution never send the probe. A definitive "unsupported"
  (`kvstore.GuardedBatchUnsupportedError`, matching `kvstore.ErrAtomicWriteUnsupported`) keeps
  the system tenant on the sequential path; an inconclusive probe fails that one create and is
  asked again.
- A create refused in a way that proves nothing was written (`kvstore.ErrNotApplied`: a
  propose-time `NotLeader`, an unsupported capability) returns at once instead of waiting out
  the 5 s lost-response reconciliation.
- First boot verifies an existing admin by its role, `System` flag and lifecycle, not only its
  username, and retries a generated-id collision with some other user using a fresh id
  instead of aborting.

## [0.20.6-alpha] - 2026-09-25

### Fixed

- A node started with `seal.enabled: false` logs one explicit warning that data at rest is
  plaintext. The warning keys on the configuration decision, not on a missing barrier.
- `enable-encryption` on a seal-disabled node no longer panics the daemon; a concurrent
  second migration request is refused with `seal.ErrMigrationInProgress` (409 /
  `FAILED_PRECONDITION`) instead of tearing down the data plane.
- `enable-encryption` is registered on every transport (REST, gRPC, QUIC, MCP, Unix,
  embedded) and refuses with one typed error where it is unavailable. The embedded, QUIC
  and MCP SDK clients now forward it instead of returning `NotImplementedError`.

### Fixed

- **Enable-encryption reaches the node on every SDK transport.** The embedded
  Go SDK transport now carries real `EncryptionMigrator` wiring from
  `sdk/go/node` (mirroring how the network transports receive theirs), so it
  migrates when a migrator is configured and refuses with the typed
  `service.ErrMigrationProviderUnavailable` only when one truly isn't. The
  QUIC and MCP Go SDK clients now forward `BarrierEnableEncryption` to their
  already-working server handlers instead of returning an unconditional
  `NotImplementedError` stub. The MCP server also registers
  `qrdb_barrier_enable_encryption` unconditionally, closing a gap where a
  standalone node with seal disabled (no barrier service and no migrator)
  returned tool-not-found instead of the typed refusal every other transport
  gives in that state.

## [0.20.5-alpha] - 2026-09-24

### Fixed

- The CI coverage gate no longer fails at random on `pkg/vector/flat`. The insertion-sort
  swap branch in `sortAllResults` was covered only when `sync.Map`'s randomized iteration
  happened to produce an unsorted order (a 1.9-point swing under CPU contention); a
  deterministic test now covers it on every run.

## [0.20.4-alpha] - 2026-09-24

### Fixed

- **k-means++ initialisation no longer hangs.** `pkg/vector/ivf` and `pkg/vector/quantization`
  recomputed every chosen centroid's distance on each selection (O(n·k²·d)); they now keep a
  running minimum (O(n·k·d)). One `DefaultConfig` training call under `-race` drops from ~77s
  to under a second, and the IVF coverage profile is produced again.
- Degenerate training data (fewer distinct points than centroids) always yields exactly k
  initial centroids. Distance overflow (±Inf, NaN) is clamped before weighting, so selection
  never degenerates into Inf/NaN arithmetic. Signed metrics weight by the raw minimum distance.
- `KMeans.Cluster` refuses mismatched vector dimensions with a typed
  `KMeansDimensionMismatchError`.

### Fixed

- **k-means++ initialisation in `pkg/vector/ivf` and `pkg/vector/quantization`
  is O(n·k·d), not O(n·k²·d).** Both implementations picked each centroid by
  rescanning every previously-chosen centroid from scratch; at the shipped
  defaults (256 centroids) that quadratic term, amplified by the race
  detector's per-access instrumentation, drove a k-means test past testing's
  10-minute alarm and killed the test binary before it could write
  `pkg/vector/ivf`'s coverage profile — surfacing in CI as the package
  reporting **missing** coverage rather than a normal test failure. Both now
  track a running minimum distance, updated incrementally against only the
  newly-added centroid.
- **Degenerate k-means++ input (fewer distinct points than the requested
  centroid count) could silently return fewer centroids than requested,**
  violating an invariant every caller downstream assumes. The fallback now
  draws uniformly at random instead of scanning for an "unchosen" point that
  may not exist.
- **The k-means++ running-minimum comparison now uses the metric's raw
  (unsquared) distance, not its square, before taking a minimum** — required
  for signed metrics such as `DotProductDistance`, where comparing squares
  first can pick the wrong centroid as nearest and silently change the
  weighted-sampling distribution.
- **The k-means++ running minimum is clamped against float32 overflow and
  NaN** instead of being assigned directly, so a distance computation that
  overflows to `+Inf` cannot propagate into the weighted-sampling sum. In
  `pkg/vector/ivf`, the sampling weight itself (the clamped minimum squared)
  is now computed in float64, since squaring a large-but-finite clamped
  value — including the clamp's own sentinel, `math.MaxFloat32` — can still
  overflow float32 on its own.
- `ivf.KMeans.Cluster` now validates every vector's dimensionality itself and
  returns the typed `api.KMeansDimensionMismatchError`, rather than letting a
  caller who invokes `Cluster` directly (bypassing `Index.Train`'s own check)
  feed a mismatched vector into the distance functions' overflow-prone
  length-mismatch sentinel.

## [0.20.3-alpha] - 2026-09-24

### Changed

- CI guards every committed generated artifact (coverage baseline, deadcode baseline,
  ifacesweep findings, lockstep baseline) with a per-artifact allowlist. Each
  path-bearing column must be a go-qrdb import path or a repo-relative path. Absolute
  workstation paths, `..` traversal, header-only artifacts and unknown artifacts all
  fail the Lint job.

## [0.20.2-alpha] - 2026-09-23

### Fixed

- **Prefix and range scans no longer walk the whole store.** Metadata keys sort by length
  first, so the keys of one prefix are not one contiguous run, and `List`, `Scan` and `Range`
  read every metadata key in the store and filtered. They now seek once per key length
  (`mvcc.ForEachMetaKeyInRange`) and read only the matching keys. Output order and `Limit`
  are unchanged. go-quicbaas measured ~3.6 s to list 3,648 records before this (#560).

## [0.20.1-alpha] - 2026-09-23

### Fixed

- **`secrets:*` permissions are own-tenant only.** They now use `ScopeSelfStrict`: a tenant's
  principal can reach only its own vault, and the system tenant is confined to its own vault
  rather than lifted over every tenant.
- **Auth failures that could not be recorded are distinguishable from a sealed tenant.**
  `AuthFailureUnavailable` is split into two classes, "could not evaluate" and "could not
  record the decision". They map to the same transport status but can no longer be conflated
  in control flow, and an outer audit-gate failure wins over its inner cause.
- `qrdbd` no longer has a window during signal-handler reset in which a SIGTERM kills the
  process without cleanup.

### Documentation

- `pkg/secret` states its requiredness boundary. A required secret is guaranteed only for a
  leaf the walk reaches; whether a containing section is present is the consumer's `Validate()`.

## [0.20.0-alpha] - 2026-09-23

### Changed — BREAKING

- **`User.Enabled` is removed; `Lifecycle` is the only account-state field.**
  The derived `enabled` boolean is gone from the domain type and from every
  transport — REST, gRPC (`UserResponse` field 5 and the name `enabled` are now
  reserved), QUIC, MCP, the Unix socket and the embedded SDK — and from the
  `qrdb users` output, which shows `Lifecycle` / `LIFECYCLE` in its place. Read
  `lifecycle` instead. A record with no lifecycle state now FAILS CLOSED: it
  cannot authenticate and cannot be transitioned. The decode-time fallback that
  derived a state from `enabled` is removed with no migration; a record written
  before the lifecycle field existed must be recreated.
- **`User.GroupIDs` is removed from qrdb entirely.** It is gone from the
  domain type, `CreateUserRequest` and `UpdateUserRequest`, every transport
  (REST/QUIC `group_ids`/`groupIDs`; gRPC `group_ids` on `CreateUserRequest`,
  `UpdateUserProtoRequest` and `UserResponse` plus `has_group_ids`, all now
  reserved; MCP `group_ids` and `clear_group_ids`), the Go SDK
  (`transport.User`, the request types, and `NormalizeGroupIDs`) and the store's
  audit details. qrdb authorization resolves user → role bindings → roles →
  permissions and no path read the field; a layer that needs group membership
  stores it itself.

### Removed — BREAKING

- **`database.StandaloneBackend.Storage()` is removed** (and with it from the
  SDK's `StandaloneBackend` alias). Writes through the raw store bypassed the
  backend's write mutex, which is what makes its guarded verbs atomic; nothing
  in production called it.

### Fixed

- **A missing lease is reported as not-found on every transport.** Lease
  time-to-live for an absent lease now returns the typed `LeaseNotFoundError`
  from both lessor-backed state machines (`mvcc` and the default `pebble`)
  instead of a generic invalid-query error, and every transport maps it to its
  not-found status. An expired lease is indistinguishable from an absent one.
- `DROP TABLE` reclaims the table's sequence counters.

### Changed

- CI enforces a blocking coverage gate: a repo-wide floor plus a per-package
  ratchet against `test/coverage-baseline.txt`.
- Trivy blocks on every push; the auth integration suite runs per push and the
  cluster integration suite nightly.

## [0.19.0-alpha] - 2026-09-17

MINOR, not PATCH. The release adds a scope to the permission vocabulary and
moves an existing permission family onto it, which changes who may exercise
those permissions — additive for the vocabulary, but a tenancy-reach change for
`escrow:*`, and pre-1.0 MINOR is where surface changes live (release-versioning
convention §1).

This release exists to carry `ScopeSelfStrict` and the `KDFDescriber` seam to
go-xkms: the first closes a cross-tenant escalation on escrowed key material,
and the second unblocks every password-change path in that project.

### Added

- **`permission.ScopeSelfStrict` — a permission confined to the identity's own
  tenant for EVERY identity, the system tenant included.** `ScopeSelf` lifts the
  system tenant across all tenants and `ScopeTenantOnly` denies it outright;
  neither expresses "every tenant is its own boundary, the system tenant
  included" — a permission the system tenant may exercise on its own data and
  on nobody else's. The authorizer gains the matching arm (own tenant admits,
  anything else denies, with no lift), `Scope.String`/`Valid`/`ParseScope` learn
  the `self-strict` label, and the SDK gains
  `transport.PermissionScopeSelfStrict` carrying that same label — the wire form
  is identical whichever layer wrote it, so a persisted per-tenant override
  decodes.

### Changed

- **`escrow:create/list/recover/revoke` move from `ScopeSelf` to
  `ScopeSelfStrict`.** They were liftable, so an identity in the system tenant
  could escrow, list, recover or revoke **any** tenant's key material — a
  cross-tenant escalation wearing an admin role. Each tenant, the system tenant
  included, now reaches its own escrowed material and no other's. The duty
  classification (`DutyGovernance`, never `DutyCryptoUse`) and the built-in role
  grants are unchanged.

- `seal.ChangePassword` identifies the strategy it replaces through the new
  exported `seal.KDFDescriber` interface (`KDFVersion()` + `KDFParams()`)
  instead of asserting the concrete `*seal.SoftwareStrategy`. A strategy that
  composes a software one — the shape a consumer reaches for to add auditing or
  metrics around sealing — carries the very KDF parameters the re-seal needs,
  and was being refused for its type rather than its behaviour. Extending the
  seam is what this repo prescribes when an interface proves insufficient,
  rather than having consumers keep a duplicate strategy of their own.
- `seal.ErrPassphraseStrategyNotSoftware` now states the real condition
  ("does not report its KDF parameters"). The sentinel's name and identity are
  unchanged, so `errors.Is` callers are unaffected; only the message differs.

- **The embedding manager's default provider may be one the embedder
  registers.** `NewManager` validated `Config.Provider` against a literal list
  of the four built-ins, so a config naming an out-of-tree provider — an NPU
  backend, an OpenVINO sidecar through the existing provider bridge — was
  refused with `ErrProviderInvalid` before any provider could be registered.
  The asymmetry was the tell: `RegisterProvider` applies no allowlist, so a
  consumer could register a provider it could not then select as the default.
  The list is gone; an unknown name is answered where it is first used, by
  `*ProviderNotFoundError`. The built-ins' own rules are untouched.

### Fixed

- **The standalone gate refuses with a service-layer error every transport
  classifies.** Under mode `standalone` the raft-only gate refused MVCC, watch,
  lease and lock with `*rest.ServiceNotImplementedError` — a REST transport type
  only the REST transport classified. REST answered 501; gRPC, unix, QUIC and
  MCP fell through to their unclassified default and reported the refusal as an
  internal fault. Those tell a client library opposite things: "the server
  broke, retry" versus "this node cannot do that, fall back". A consumer running
  standalone and calling an MVCC verb was told its server was broken, for a
  deployment mode. The gate, and the per-database registry's "not configured"
  refusals beside it, now return `*service.CapabilityUnsupportedError`, which
  every transport's classifier already maps to "unsupported".

- **Two `pkg/service` skip guards were coverage gaps wearing a green tick.**
  Both claimed the path they abandoned needed real storage; neither claim held —
  the backup registry is an in-memory map in the same package. The tests now
  corrupt a captured shard and assert `VerifyBackup` reports the mismatch with
  both checksums, and that `RestoreBackup(ValidateFirst)` refuses with
  `BackupChecksumMismatchError` before touching a shard.

### Security

- **trivy scans the frontend's dev dependencies; nanoid and postcss bumped past
  three HIGHs.** trivy's npm parser excludes a lockfile's dev dependencies
  unless `--include-dev-deps` is passed, so the blocking Security gate was green
  on three HIGH advisories — CVE-2026-67213 and CVE-2026-67214 in nanoid 3.3.12,
  CVE-2026-73646 in postcss 8.5.15 — that `npm audit` reported and that sat
  squarely at the CRITICAL,HIGH floor. A scope gap, reporting nothing about a
  whole dependency tree. Owner ruling (2026-09-16): dev dependencies are in
  scope on the blocking leg, because vite and esbuild produce the shipped
  bundle and a compromised build tool is a supply-chain path into the artifact.

## [0.18.0-alpha] - 2026-09-16

MINOR, not PATCH. The release adds a permission family to the registry:
new vocabulary is additive for existing callers, but pre-1.0 MINOR is where
surface changes live (release-versioning convention §1), and a consumer that
gates escrow routes on these permissions cannot resolve them from an earlier
tag at all.

This release exists to carry `escrow:create`, `escrow:recover`, `escrow:list`
and `escrow:revoke` to go-xkms, whose escrow subsystem is implemented and
wired but cannot be exposed on any network transport until the permissions it
gates those routes with exist here.

### Added

- **A key-escrow permission family** (`escrow:create`, `escrow:recover`,
  `escrow:list`, `escrow:revoke`). go-xkms's escrow subsystem sends wrapped
  key material to an external custodian (KMIP or a federation agent) and
  reconstructs it back, and every key-material route it exposes is gated by a
  permission pair drawn from this registry — there was no escrow family for
  it to use. Classified `DutyGovernance` (key entry/output, the same duty as
  `keys:import`/`keys:export`, not a cryptographic primitive), scoped
  `ScopeSelf`, and granted in full to the Security Officer and Admin roles,
  with `escrow:list` (metadata only — never key material) also granted to
  Auditor. qrdb owns the vocabulary; it ships no escrow implementation of its
  own, matching the existing PIV/SSH/Passwords split.

## [0.17.0-alpha] - 2026-09-13

MINOR, not PATCH. Of the 99 commits on `main` between `v0.16.3-alpha` and this
release, 20 carry a breaking marker in their subject, and three more (#402,
#429, #485) change an exported interface or contract without one. Pre-1.0,
MINOR is where breakage lives (release-versioning convention §1).

This release exists to carry `pkg/secure/analyzer/scope.go` to consumers. At
`0.16.3-alpha` the analyzer's only non-test source file is `analyzer.go`, so
without the four scoping clauses it reports generated protobuf, `_test.go`
files and wire DTOs as rule-3 findings — a gate wired against the pinned
release is red for reasons no change in the consuming repository can fix.
`scope.go` landed in `6d0c17b` (#429, #450) and has been unreachable to
consumers ever since.

### Added

- **The MCP transport carries the guarded write verbs** (#127).
  `sdk/go/transport/mcp` now implements `transport.GuardedKVClient`, so a
  consumer's `client.KV().(transport.GuardedKVClient)` succeeds over MCP as it
  already did over embedded, gRPC, unix, QUIC and REST (#119/#124/#125/#126).
  Three new tools carry the verbs — `qrdb_put_if_absent`,
  `qrdb_compare_and_delete`, `qrdb_compare_and_swap` — each requiring the
  permission of the write it performs (`kv:put`, `kv:delete`, `kv:put`), each
  taking its values base64-encoded, and each answering with a single-field JSON
  object (`{"stored": …}`, `{"deleted": …}`, `{"swapped": …}`) that mirrors the
  REST response bodies. A refused guard is a SUCCESSFUL tool result carrying
  false, never a tool error: losing the race is the normal outcome of a claim.

  Every handler makes exactly ONE call, to `service.KVServicer`'s matching
  atomic method — no `Get` before it, no read-then-write fallback after it —
  and that is asserted rather than asserted-in-prose. The MCP transport gets
  the guarded conformance matrix's four full-passage cells and the
  cross-transport contention suite's MCP row (`sdk/go/conformance`), plus a
  delegation test that counts which service methods the handler reached.
  Verified by mutation: replacing the single `PutIfAbsent` call with a `Get`
  followed by a `Put` leaves every sequential assertion green and turns both
  the delegation counters and the contention suite red.

  A client with the verbs talking to an MCP server without the tools gets
  `transport.CapabilityUnsupportedError`, matching the version-skew half of the
  contract the other transports carry. The classification is by JSON-RPC error
  CODE, not message text.

  **Known limit:** MCP does not walk the matrix's four typed-refusal cells,
  which require a transport that can present the multi-key transaction plane's
  capability refusal *by type*. MCP reports a service-layer failure as a tool
  result carrying a message string, so that refusal reaches the client as
  prose; admitting it would mean loosening `classifyRefusal` from a type
  assertion back to a substring match. Excluded and named
  (`transportKind.carriesTypedCapabilityRefusal`) rather than papered over;
  #169's error-typing sweep owns that surface.

- **Rate limiting is centralized in the auth middleware** (#201, #394), one
  limiter rather than a decision each handler made for itself.
- **Typed lease not-found and lease-expired across gRPC, unix, MCP and QUIC**
  (#385), so the condition is carried rather than reconstructed from prose.
- **`pkg/secure/analyzer` is armed as a build gate, with rule 3 scoped to
  where it can be satisfied** (#429, #450) — `scope.go`'s four clauses exempt
  generated protobuf, `_test.go` files, flag targets and wire DTOs, which are
  not holders of key material and cannot satisfy the rule. **This is the change
  this release exists to publish.**
- **A Trivy reporting floor below the blocking one** (#522). `make trivy`
  gated on `CRITICAL,HIGH` and that was the only severity list in the
  repository, so no leg could report a MEDIUM and a green Security signal
  could not distinguish "no MEDIUMs" from "nothing looked". Two real
  advisories, both fixed upstream, sat under that floor for the whole of the
  repository's history. The blocking floor does not move; `make trivy-report`
  scans `MEDIUM,LOW` as its own nightly step after the gate, exits nonzero on
  a finding so the signal can go red, gates nothing, and prints `UNKNOWN`
  without failing on it. `test/buildgates/trivy_severity_test.go` pins the
  split by parsing the Makefile and nightly workflow: every severity band is
  named by some leg, the gate names nothing below HIGH, the report leg is
  unreachable from `security`/`ci`/`_ci` transitively, and nightly runs the
  gate then the report. A local review of the first cut found the gate passed
  with the report leg commented out, made transitively blocking, or unplugged
  from nightly, and fixing those found a fourth: a column-0 make comment
  between recipe lines truncated the parse. All four are watched reds.
- **The FIPS document claims only what was measured** (#523). Nightly run
  17302 on `133fa56` is the first *scheduled* `fips140=only` run on `main`
  ever to pass, after twenty-two consecutive failures: root `make test` 126
  packages pass / 0 fail, `sdk/go` `make test-sdk` 18 / 0, with zero
  GCM-arbitrary-IV, X25519, `crypto/sha1` and short-key HMAC rejections.
  `docs/seal/fips140-mode.md` states step by step what that run executed,
  pins the figures to that commit with a statement that the tree has moved
  since, and **withdraws** the earlier claim that `go build ./...` was clean
  for `sdk/go`: the job does not run that step, so the SDK is compiled only
  as far as `go test` compiles it. Arming the job as a required check now
  waits on a stability record, fourteen consecutive green scheduled runs,
  rather than on preconditions that are met.

- **A red nightly opens a tracking issue, a still-red one rewrites it in place,
  and a green one closes it** (#478, #525).
- **`docs/coordination/`** (#173, #404) — the five-document feature folder the
  coordination surface never had.

### Changed — BREAKING

- **A refusal travels in the result payload, and no longer halts the shard**
  (#520, #527). Raft treats any non-nil error out of `Update` as a fatal apply
  failure: `lastApplied` stops advancing, and every subsequent linearizable
  read on that shard — on any engine sharing it, on keys written and read
  successfully microseconds earlier — waits out the caller's deadline and
  reports a consensus timeout. The MVCC engine had **no** apply classification
  at all, and the time-series stub could not say a refusal was a refusal, so
  one refused time-series write cost the shard its reads. Measured, not
  theorised, on both engines (#129 on KV, #520 on MVCC).

  `statemachine.ResolveApplyError` is now THE classification seam for the
  repository, and every `Update` in the tree routes through it. **The error
  declares which it is**: an error implementing `protocol.CapabilityError` —
  an `error` with a `Capability() string` method naming the absent verb or
  subsystem — is a refusal and is encoded into `Result.Data` with a capability
  marker, which the service layer turns into a typed
  `CapabilityUnsupportedError` for the caller. Everything else is a fault and
  still halts Apply. Silence means fatal; only a declaration means benign, so a
  forgotten marker costs availability on one shard loudly rather than
  swallowing a storage fault and serving diverged state silently.

  `test/buildgates` finds every method in `pkg/` taking `[]sm.Entry` and
  requires it to route through `ResolveApplyError` or to be listed, with a
  reason, as a delegator — default-deny, because the absence of a call is not
  something a reviewer reading one diff can see. A refused batch or transaction
  now stages nothing: opcodes are validated before any operation in the set is
  applied. **Reachable by any client able to propose an opcode this state
  machine does not implement.**

- **The MCP transport carries seventeen JSON-RPC error codes, and the
  substring classification is deleted** (#438). `pkg/api/mcp` had **no**
  error-code mapping at all — a tool failure reached a client as a sentence —
  so every typed condition the SDK promises degraded to matching words in
  prose. `sdk/go/transport/mcp` classified four conditions with
  `strings.Contains(msg, "not found")` and one with `"lease expired"`, which is
  fail-open in the direction that costs most: a transport failure whose text
  happened to carry the phrase read as a successful determination of absence.

  The new `pkg/api/mcp/wire` package declares one code per gRPC status code,
  by MEANING, exactly as the QUIC wire does since #437 — so a consumer keeps
  one mental model across transports instead of acquiring a second. The
  numbers are JSON-RPC's, from the −32000..−32099 band reserved for
  implementation-defined server errors, and three of them (−32002, −32005,
  −32010) are deliberately left unassigned to the names the portfolio's error
  convention already gives them.

  | condition | code |
  |---|---|
  | not found (key, user, role, lease, revision) | `NOT_FOUND` (−32004) |
  | barrier sealed, store closed, not leader, draining | `UNAVAILABLE` (−32006) |
  | capability this node was not built with | `UNIMPLEMENTED` (−32014) |
  | throttled caller | `RESOURCE_EXHAUSTED` (−32008) |
  | unclassified server fault | `INTERNAL` (−32603) |

  **What changes on the wire.** A service-layer failure used to leave as a
  JSON-RPC error pinned to `INTERNAL_ERROR`; it is now a tool RESULT carrying
  `isError`, which is what the MCP specification asks for and the only envelope
  a tool handler controls. The text content is byte-identical to what it was,
  so a client that only reads text sees no difference; the code rides in
  `structuredContent` as `{"error": {"code", "status", "message", "tool"}}`.

  **What changes in the SDK.** The nine single-subject user methods return
  `*transport.UserNotFoundError` naming the id or key **the caller supplied**,
  where they previously returned `*transport.KeyNotFoundError` (four of them)
  or a generic request error (five). `transport.RequestError.StatusCode` now
  carries the JSON-RPC code on MCP. An unrecognised code from a newer server
  degrades to the generic request error, never to a misclassification.

  The server owns no classification clauses of its own: it consults the gRPC
  transport's per-family classifiers through the new
  `grpc.ServiceErrorCode` and renders the result, so a condition cannot mean
  one thing over gRPC and another over MCP.

  One message test remains, behind a code gate: the lease expired/absent
  split, which no transport in this repository can separate by code because
  gRPC maps both to `NOT_FOUND`. It fires only once the code has already said
  not-found; it is not the ungated classification this change deleted.

- **Two barrier-passphrase config fields are `*secure.Buffer`, and an unusable
  one is now an error rather than a downgrade** (#429). Arming
  `pkg/secure/analyzer` as a build gate left exactly two findings that were
  neither a wire DTO, a generated struct, a flag target nor a test fixture —
  two Go config structs that a caller fills from its own key source and that
  live as long as the process they configure. Those are *holders* of key
  material, which is the population rule 3 exists for, so both changed type:

  | surface | before | after |
  |---|---|---|
  | `bootstrap.SystemConfig.Passphrase`, re-exported as `sdk.BootstrapSystemConfig` | `string` | `*secure.Buffer` |
  | `embedded.BarrierConfig.Passphrase` (`sdk/go/transport/embedded`) | `[]byte` | `*secure.Buffer` |

  Use `secure.NewBufferFromBytes`; `sdk/go/secure` re-exports the type and its
  constructors, so no consumer needs the internal `pkg/secure` path. The
  caller keeps ownership — neither `BootstrapSystem` nor `NewServer` destroys
  the buffer; each copies out of it once, inside a `WithBytes` borrow, at the
  point the material becomes the `seal.Credentials` the barrier demands.
  `string` cannot be zeroized at all and `[]byte` only by the caller's own
  discipline; a `Buffer` is mlocked, canary-guarded and wiped on `Destroy`.

  **A `*secure.Buffer` has a LIFECYCLE that neither previous type had, and
  that is a behaviour change, not only a type change.** `Buffer.WithBytes`
  returns the same `*NilBufferError` for a nil receiver, a destroyed buffer
  and a zero value, so the obvious single error check reads all three as "no
  passphrase configured". For `embedded.BarrierConfig` that is not a wrong
  error message, it is a **wrong security posture**: on a fresh data dir the
  resulting `seal.StrategyNone` barrier initializes successfully, `IsSealed()`
  is false, `encfs` wraps the Pebble data plane with a root key sealed by
  nothing, and an embedder that asked for a passphrase barrier gets plaintext
  at rest, permanently, with nothing said about it. The two cases are
  therefore separated:

  - `Passphrase == nil` — **not configured**. Unchanged behaviour: with
    `StrategyPassphrase` or the empty default the embedded transport comes up
    with no barrier, which go-quicraft announces at initialize (`barrier
    performs NO encryption`).
  - `Passphrase != nil` but unreadable — **a configuration error**.
    `NewServer` fails with the new `*BarrierPassphraseError` and does not
    fall back. Destroying the buffer after one server and constructing another
    is exactly what the ownership rule above invites, and it is the sequence
    that must fail loudly.

  Covered by four shapes in both directions — nil, destroyed, zero value,
  live — in `sdk/go/transport/embedded/barrier_passphrase_test.go`. Three of
  them are red without the guard; the live case additionally asserts the
  written value is not on disk in the clear. `pkg/bootstrap` keeps treating an
  unreadable buffer as `*MissingPassphraseError`, which is the error that
  branch already returned when the field was a `string`, and no barrier is
  weakened there.

  **Also corrected in the same change:** `BarrierConfig.Passphrase`'s doc
  claimed that with no passphrase `NewServer` "falls back to the deterministic
  bootstrap passphrase keyed by deployment ID 0". No such derivation exists
  anywhere in `sdk/go` — the only thing resembling it is a string a test
  builds and passes to `BarrierInitialize` explicitly — and the code ten lines
  into `setupBarrier` says so itself: "there is no deployment-derived
  passphrase". On a public SDK field whose doc is the only thing an embedder
  reads, the gap between "derives a passphrase" and "comes up with no barrier"
  is the gap between encrypted and not.

  **Consumer verification (pre-publication exception,
  `conventions/release-versioning.md`):** inside this repository the changed
  fields are referenced only by `cmd/qrdbd`, `sdk/go/types_bootstrap.go` and
  the two packages' own tests, all updated here. go-xkms and go-quicbaas were
  not checked out on the host that made this change and could not be grepped;
  both are filed rather than assumed — go-xkms#550 and go-quicbaas#830. Not a
  zero-consumer change and not claimed as one.

- **Barrier credentials are wipeable bytes at every layer** (#184). Every field
  that carries a caller-supplied barrier credential changed from `string` to
  `[]byte`: `BarrierInitializeRequest.Secret`, `BarrierUnsealRequest.Secret`,
  `BarrierInitializeShamirRequest.Secret`,
  `BarrierEnableEncryptionRequest.Passphrase` and
  `TenantBarrierInitRequest.Passphrase` in `sdk/go/transport`; the matching
  `secret` / `passphrase` fields in `qrdb.proto` (`string` → `bytes`); the
  `secret` / `passphrase` parameters on `service.BarrierServicer` and
  `tenant.TenantServicer`; and the REST and QUIC JSON request structs on both
  the client and the server.

  A Go `string` is immutable and therefore **cannot be zeroized**. A consumer
  holding a passphrase in protected memory had to convert it to a `string` to
  populate the request, and that copy then lived on the heap until the
  collector happened to reuse the page — recoverable from a core dump or a
  memory scrape for an unbounded period. The field type was the only thing
  forcing that, and `TenantBarrierUnsealRequest.Key` was already `[]byte`, so
  the fix is to make the outliers match rather than to add anything.

  The type change makes the credential *wipeable*; the rest of the change
  makes it *wiped*. The caller owns the slice it supplies and may zeroize it
  the moment the call returns, because no transport, service or sealing
  strategy retains it.

  **What "retains nothing" does and does not cover.** Wiping the slice destroys
  that slice and every alias of it. It does not reach the transient copy each
  encoder necessarily makes — base64 in a REST body, protobuf's out-of-place
  marshalling, the MCP JSON-RPC body. Those are unreferenced once the call
  completes and the collector reclaims them, but they are not zeroized, and
  nothing at this layer can zeroize them: serializing a value means copying it
  and the wire format owns that copy. The claim is that the copy the CALLER
  holds and the copy the DAEMON decodes are both destroyable on demand, which
  a string never was — not that no plaintext exists anywhere. The daemon owns the slice its decoder produced and
  `secure.Wipe`s it before the handler returns — on the success path and on
  every error path, including a rejected unseal, which is the case that
  matters most. `enable-encryption` is the one deliberate exception: its
  migration outlives the request, so ownership passes to that goroutine and
  the wipe is deferred there instead.

  **Wire format.** proto3 encodes `bytes` and `string` identically, so gRPC,
  Unix and QUIC protobuf frames are byte-for-byte unchanged. REST JSON and the
  QUIC tenant JSON frame now carry these fields as **base64**, the same
  convention every other `[]byte` field on those surfaces already uses. The
  TypeScript SDK encodes and decodes this for its callers; its own API is
  unchanged and still takes plaintext.

  **MCP is the documented exception.** An MCP tool argument is a JSON string
  by definition of the protocol, so `pkg/api/mcp.secretArg` and
  `sdk/go/transport/mcp.secretArg` are single, commented conversion points on
  each side. Callers who cannot accept that residue should not drive barrier
  operations over MCP.

  **Not covered:** Shamir share fields and SO/User PINs are still text strings
  on every transport. They are secrets too; the gap is stated in
  `docs/barrier-seal/api.md` rather than left implied.

  **Consumer verification (pre-publication exception,
  `conventions/release-versioning.md`):** inside this repository the changed
  types are referenced only by `sdk/go/transport/*`, `pkg/api/*`, `pkg/service`,
  `pkg/tenant` and `cmd/qrdb`, all updated here. go-xkms, go-quicbaas and
  go-trusted-platform were **not checked out on the host that made this
  change** and could not be grepped. go-quicbaas is a known consumer — it is
  the repository that raised this issue — and will need a mechanical update at
  its call sites, after which it can delete the `passphraseString` conversion
  it documents as residue today. This is not a zero-consumer change and is not
  claimed as one.

- **`RevokeCert` reports what it did, on every transport** (#287).
  `cert.CertProvider.RevokeCert`, `transport.CertClient.RevokeCert` (`sdk/go`)
  and the gRPC/Unix `RevokeCert` RPC now return a result alongside the error:
  `(*cert.RevokeResult, error)` in `pkg/cert`, `(*transport.RevokeResult,
  error)` in the SDK, `RevokeCertResponse{record_found}` on the wire (the RPC
  previously returned `google.protobuf.Empty`). `RevokeResult.RecordFound`
  (a projection of `RevokeOutcome`) distinguishes a certificate this node
  holds a `CertRecord` for from a deny-by-serial revocation of one it does
  not — the certificate may have been issued by a peer, or its record
  pruned. Both are successes: the revocation marker is written
  unconditionally, before the outcome is even determined, so a mistyped
  serial was previously indistinguishable from a deliberate revocation of a
  peer-issued certificate. Reporting that distinction as an error would have
  misreported the deliberate case — the one deny-by-serial exists to
  protect — as a failure. `qrdb ca revoke` / `qrdb certs revoke` now prints
  which case occurred.

  **Consumer verification (pre-publication exception,
  `conventions/release-versioning.md`):** `RevokeCert` and `ExportPKCS12` are
  referenced only inside go-qrdb's own `pkg/cert`, `pkg/api/*` and
  `sdk/go/transport/*`. go-xkms, go-quicbaas and go-trusted-platform were not
  checked out on the host that made this change; go-trusted-ca matches on
  the method name but has no go-qrdb dependency in its `go.mod`, so its
  `RevokeCert` is an unrelated API, not a consumer of this one. Zero verified
  external consumers at the time of this change.

- **`/certs/*` is gated on the tenant the REQUEST names, not the caller's**
  (#474, #506). `certs:generate` took its `tenantID` from the request body
  unchecked and wrote it into the certificate's Subject OU, which the mTLS
  authenticator reads as the identity's tenant — so a caller could mint a
  CA-signed certificate that authenticates as another tenant. Reachable on four
  transports.
- **The QUIC wire carries gRPC's seventeen conditions, not six** (#437, #461),
  by meaning rather than by message text; four lanes were blocked on the
  missing vocabulary.
- **`LockServicer` resolves per (tenant, database)** (#455, #487). It was a
  process singleton over pre-namespace engines, so two tenants sharing a shard
  under `prefix` isolation took the same lock, and `List`/`Status` enumerated
  each other's keys and holder ids.
- **Collection documents are (tenant, database)-namespaced** (#441, #454), so
  the collection service resolves like its twelve siblings.
- **Auth factors are per-tenant** (#393, #463), routed through
  `tenant.StoreResolver`.
- **The SO-PIN gate fails closed** on the same lever as the authorisation gate
  (#445, #471).
- **A deleted tenant id is retired, not released** (#413, #468).
- **`DROP DATABASE` reclaims the keyspace**, not just the directory (#412,
  #458).
- **`DropCollection` reclaims the collection's ID sequence** (#411, #449) —
  `SequenceServicer` gains a delete verb.
- **An explicit `_id` reserves the counter, and an insert never overwrites
  silently** (#447, #465).
- **A collection table honours the `_id` it declares as its primary key**
  (#467, #475). `REPLACE INTO` never replaced, `ON DUPLICATE KEY UPDATE` never
  updated, and `UPDATE ... SET _id` destroyed the document.
- **`Emit` reports whether the record landed, and an unrecordable
  authorisation decision is refused** (#335, #443).
- **An evicted entity watch says why** (#496, #501) — a terminal variant on
  `WatchEntityEvent`, and the one consumer that would have dropped it.
- **A revoked peer certificate no longer authorizes on the cluster key plane**
  (#285, #390).
- **`gosec`'s `-tags` is comma-separated** (#295, #397); the gate had been
  scanning the untagged tree.

- **A cert binding can be withdrawn** (#518). `AddCertBinding` shipped on all
  six transports and nothing removed a binding on any of them, so a binding
  written today could not be withdrawn through any API. Since #505 made the
  binding set what decides an account's mTLS admission, an enrolled key was an
  irrevocable *credential* rather than a stale record — and revocation is not
  the remedy, because a binding is over the `SubjectPublicKeyInfo` and outlives
  every certificate issued for that key, so a re-issue for the same key
  authenticates again. The exploitability is bounded (re-issuance needs
  `certs:generate`, gated on the target tenant by #506); what it cost was an
  operator who could not clean up after a key compromise.

  `RemoveCertBinding(userID, thumbprint)` is now on all six transports —
  `DELETE /users/{id}/cert-bindings/{thumbprint}`, the `RemoveCertBinding` RPC
  (gRPC and unix), `user.RemoveCertBinding` (QUIC),
  `qrdb_user_remove_cert_binding` (MCP) and `Client.Users()` (embedded) — and in
  the TypeScript SDK. It removes the record entry, the tenant-scoped SPKI index
  entry and the legacy un-scoped one.

  **A narrow verb, not a whole-set replacement.** `UpdateUserRequest.CertBindings`
  already replaced the set and maintained the index; exposing *that* over a
  transport would make a caller's snapshot of the rest of the set part of every
  withdrawal, and committing a snapshot is how a concurrent enrolment
  resurrects a binding that was just withdrawn.

  **`users:remove_cert_binding` is a separate permission**, not folded into
  `users:add_cert_binding`. Enrolment only narrows an account; withdrawing its
  LAST binding widens it back to admitting any certificate whose CN and OU name
  it. Both permissions now declare `ScopeSelf` explicitly rather than inheriting
  it, because `LookupScope` returns `ScopeSelf` for anything nobody tagged and
  these two decide which certificates authenticate as an account.

  **Revocation still does not drop a binding, on purpose.** Revoking during a
  routine rotation would otherwise unenrol the key being rotated. The two are
  separate operations answering separate questions, and an operator cleaning up
  after a key compromise performs both.

- **`ServiceModule` gains `RegisterQUIC`** (#402). A fourth registration hook
  beside `RegisterGRPC` (which serves gRPC and unix), `RegisterHTTP` and
  `RegisterMCP`, so a module registered once reaches every network transport
  instead of every one but QUIC. A type that implements the interface directly
  rather than embedding `BaseModule` must add the method.

- **A first node can advertise cluster enrollment under `GODEBUG=fips140=only`**
  (#485). Noise_NK_25519_ChaChaPoly_SHA256 performs an X25519 handshake, which
  is not a FIPS 140-3 approved key-agreement scheme, so `crypto/ecdh` refuses
  it. `clusterenroll.StartAdvertiser` returned that refusal as an error and the
  node failed to start: **cluster enrollment did not degrade in FIPS mode, it
  did not start.** The nightly `FIPS 140-3` job had reported this correctly for
  eleven consecutive runs.

  Noise is now excluded rather than attempted, on `crypto/fips140.Enabled()` —
  `Enabled()`, not `Enforced()`, matching the call `pkg/ceremony` makes for an
  Ed25519 external officer, because a deployment that asked for FIPS mode
  should not be handed non-approved cryptography just because `fips140=on`
  tolerates it. The ruling splits on the operator's stated intent:

  - **A first node** excludes Noise and comes up advertising the approved
    methods. It never chose a transport — there is no advertise-method option —
    and SPKI-pinned TLS, DANE and direct HTTPS all fetch the same CA bundle
    from `/v1/ca/bootstrap`. Refusing to boot would mean no multi-node cluster
    under FIPS at all, over a method a FIPS deployment could not use.
  - **A joining node that named Noise alongside an approved method** has Noise
    dropped from the truststrap method order; `AutoFetch` is a fallback chain by
    construction, so a preference list is what was given.
  - **A joining node that named Noise and nothing else** is refused at
    configuration time with `*clusterenroll.NoiseFIPSUnavailableError`, before
    the first dial and before anything reaches the data directory.

  **No downgrade is silent.** Both exclusions log at WARN naming FIPS, the
  method dropped and the approved methods to use instead, and the refusal's
  message leads with FIPS. That matters for the message as much as the
  behaviour: go-truststrap wraps a FIPS refusal in its `ErrInvalidKeySize`, so
  what an operator used to see was `noise: invalid key size: crypto/ecdh: use
  of X25519 ...` — a leading clause that sends them after a key-length bug that
  is not there. qrdb now refuses above that call, so the string is unreachable
  from a FIPS deployment; the mis-wrapping is filed upstream.

  **BREAKING, in the sense that matters here: a non-`nil` `Advertiser` now
  means "this node advertises enrollment", not "a Noise listener is running."**
  `cmd/qrdbd` registers `/v1/ca/bootstrap` on exactly that condition, and
  collapsing the two again would leave a FIPS first node unreachable by *every*
  method rather than by one — silently, which is the shape go-qrdb#241 exists to
  keep out of this package. Ask the new `(*Advertiser).NoiseEnabled()` when the
  Noise listener specifically is the question. `clusterenroll.NoiseAvailable()`
  and `*NoiseFIPSUnavailableError` are exported and re-exported from
  `sdk/go/clusterenroll`.

  No test was skipped to get there, and the ruling is asserted in BOTH runtimes:
  `noiseAvailable` is an overridable variable, as `readFileFn` already is, and
  method selection was split into a `buildAutoConfig` that performs no
  cryptography — which is what lets "a non-FIPS node still selects Noise" run
  under `fips140=only` as well as under `fips140=off`. Where a live Noise
  listener became genuinely unreachable, the test asserts the contract that
  replaces one rather than skipping, so a regression that resumed reading
  `noise-key.hex` in FIPS mode fails it.

  Measured, Go 1.26.6, `GOFIPS140=latest GODEBUG=fips140=only make test`:
  **124 pass / 2 fail** against 120/2 before, with X25519 rejections at **0**
  from 15. `pkg/clusterenroll` and `cmd/qrdbd` are green. The two survivors are
  separate pre-existing defects, both filed: `pkg/tenant` has a `cipher.NewGCM`
  test fixture, and `pkg/service`'s test binary does not compile at all (which
  is not a FIPS failure — it fails with FIPS off too). See
  `docs/seal/fips140-mode.md` and `docs/clustering/`. **Both survivors were
  closed later in this release**: the `pkg/tenant` fixture now uses an AEAD
  FIPS admits (#511), and `pkg/service`'s test double gained the `PutIfAbsent`
  its interface requires (#502).

### Changed

- `AGENTS.md` holds the rules and `CLAUDE.md` is the symlink (#376) — a
  symlink is a mode-120000 blob in git, so every plumbing reader of the old
  arrangement got a nine-byte path instead of the rules, silently and with exit
  status 0.
- `vet-tags` is armed in CI (#386). The recorded blocker was an untidy
  `go.mod`; the measured blocker was a missing tag on the
  `sdk/go/gorm/conformance` leg.
- `sdk/go` is under the linter it had never been under (#364).
- **A project's workflows run CI and nothing else.** The label-triggered
  review and auto-merge workflows this range added and iterated (`v7` through
  `v26`) are removed again: QuicBaaS is the sole orchestrator, and a review
  engine duplicated into every repository was the defect, since a fix reached
  one copy and silently missed the rest. Known gap, accepted by the owner:
  until QuicBaaS can post a verdict, no automated review runs here and merges
  need a human.
- **Exported interface methods with no non-test caller are enumerated and
  gated** (#271, #401): `test/tools/ifacesweep` records the findings per tag
  set, and CI refuses a new one.
- Build hygiene: module credentials point at the forge's new address and are
  written once, not three times (#363); `Dockerfile.gpu` names its final
  stage so the GPU integration lane can build (#365); the devcontainer stops
  rebuilding its 6.5GB image on every source edit (#294, #389).

### Fixed

- **A cert-binding write no longer commits a stale set** (#518).
  `AddCertBinding` read the record and then called `UpdateUser`, which read it
  again and guarded on ITS read — so the window between the two reads was
  unguarded, and an enrolment that read the set before a concurrent withdrawal
  committed would swap its stale set in cleanly afterwards, putting the
  withdrawn credential back. Both binding verbs now commit against the bytes
  they computed from (`updateUserAt`), which makes that a LOST swap, and retry
  the lost swap against whatever won — correct because "add T" and "remove T"
  say what to do to a set without depending on what the caller last saw it
  contain. Exhausting the bound reports `*ConcurrentUpdateError`, because an
  operator withdrawing a compromised key must never read a success that did not
  happen.

- **`secure.StdinProvider` no longer leaves the passphrase in an unwipeable
  heap buffer** (#184). It read through a `bufio.Reader`, which staged the
  passphrase in an ordinary heap allocation that was never wiped — in the one
  provider whose entire purpose is to keep passphrases out of unprotected
  memory. It now reads a bounded line one byte at a time straight into an
  mlocked `Buffer`, which is destroyed (and so zeroed) on every exit path
  including the error ones. Two behaviour changes follow: the provider no
  longer **over-reads** past the newline, so a subsequent reader of the same
  stream still sees the remainder; and input longer than
  `secure.MaxStdinPassphraseLen` (1024) is **refused** rather than truncated,
  because silently accepting a prefix would unseal against a credential the
  operator did not type.

- **`ExportPKCS12` refuses a revoked certificate** (#287). It parses the
  supplied `certPEM`, derives the serial with `FormatSerial` (the single
  definition of that format), and refuses with `*CertRevokedError` when
  `IsRevoked` says the serial is revoked. There is no override flag: a
  forensic or archival export of a revoked bundle needs that added
  deliberately, not defaulted. This is an OPERATOR SIGNAL, not a containment
  control — the caller already supplies `certPEM`/`keyPEM`, so refusal
  withholds nothing it did not already have; its value is that the system
  stops silently packaging a credential it will not honour anywhere in the
  cluster. Gives `cert.CertRevokedError` its first producer: both REST (409)
  and gRPC (`FAILED_PRECONDITION`) mappings were previously unreachable
  except through a mock.

- **Teardown is fenced** (#483, #488). `DeleteTenant` makes the retirement
  durable before the irreversible shred, so a failed tombstone write no longer
  leaves a live tenant whose data is permanently undecryptable. `DROP DATABASE`
  stops deleting other tenants' unattributable collection records (#481). The
  fixture uses an AEAD FIPS admits (#511).
- **`StandaloneBackend` is an `AtomicBackend`** (#466, #490), so the collection
  insert is a guarded write and `PutIfAbsent` works on the dedicated-store
  path.
- **The audit chain spans a rotation, and the verifier knows it** (#452, #476).
- **A cancelled watch releases its goroutine, and a watch that ended for a
  reason says so** (#405, #406, #494) — on the embedded transport and the
  namespaced wrapper. The last two sites of the same pattern closed in #515: a
  refused start revision keeps its type (#495), and a cancelled namespaced
  watch releases its goroutines (#499).
- **Secondary-index maintenance is scoped to the keys an index owns** (#410,
  #448).
- **A closed PebbleDB store returns `ErrStorageClosed` instead of panicking
  `pebble: closed`** (#371, #435).
- **`DropCollection` drops the indexes `CreateCollection` built** (#399, #439).
- **`qrdb_watch` stops crediting its cursor from a replay it never delivered**
  (#388).
- **`pkg/discovery` compiles on darwin** (#359) — the `socket(2)` flags move
  behind a platform seam.
- **`secure` holds the borrow across seven traced reads, and the deadlock is
  loud** (#74, #427).
- **`sdk/go/node` resolves the scoped encryption router's tenant leg lazily**
  (#384).

### Security

- **Cross-tenant certificate issuance is closed** (#474, #506) — see
  `/certs/*` above. A caller could mint a CA-signed certificate that
  authenticates as another tenant, on four transports.
- **An enrolled account's mTLS login is decided by its SPKI binding**, not by
  the certificate's CN and OU (#505, #516). Fail-closed per account: an account
  that has enrolled a binding is refused a certificate that does not match it;
  an account with none authenticates as before.
- **`FinishLogin` resolves the factor on the login session's tenant, not the
  caller's `ctx`** (#303, #392).
- **Every request body is bounded** on the REST plane (#378), and a comment
  claiming a bound six handlers lacked is gone.
- **Every workflow action is pinned by commit digest, and it is gated** (#367,
  #407). A tag is a ref its owner can move, and at the time the review job
  held a write-scoped token; that job is gone (see Changed), the pin gate
  stays.
- **Volume-pool paths are validated before they are created** (#292, #366),
  from one shared guard: absolute and canonical, no symlink, and no overlap
  with another candidate in the same request. The request type is
  wire-shaped, so the guard lands before the change that makes it reachable
  from a transport. `gosec` cannot see this sink: G304 covers opens, not
  directory creation.
- **The module-fetch token never touches disk** (#326, #368). The docker
  build's Gitea credentials reach `go mod download` as a BuildKit secret
  sourced from the environment, not from a temp file an EXIT trap was
  supposed to remove and did not on SIGKILL.
- **`gosec` scans the tag-gated files and the `sdk` module** (#377), and the
  91 test-file findings are triaged with the two measured reasons `-tests`
  still cannot go on recorded (#395, #423).
- **`software.sslmate.com/src/go-pkcs12` moves to v0.7.3 in all three
  modules** (#522) for GHSA-mpwr-8vm7-h73f, an authentication bypass in the
  package's decode functions. Measured rather than assumed: this repository
  calls exactly one symbol from it, the `Modern2023` encoder behind
  `ExportPKCS12`, and never decodes a `.p12`, so the bump is hygiene, not
  exposure; the grep is recorded in `docs/certificate-authority/architecture.md`
  beside the dependency.
- **`echarts` moves to 6.1.0 in the frontend** (#522) for CVE-2026-45249, an
  XSS. The major bump was blocked only by `svelte-echarts`, which nothing
  imported; it is removed. The production build, `svelte-check` and the 31
  time-series Playwright tests were run against the new major.
- **`google.golang.org/grpc` moves to v1.83.2 for CVE-2026-84445, and a stale
  trivy database now refuses to certify rather than certifying blindly**
  (#451, #477).

## [0.16.3-alpha] - 2026-09-06

### Added

- **`RoleServicer.ReconcileBuiltInRoles(ctx, tenantID)` — the ONE sanctioned
  writer to a built-in role's permissions** (#354). A built-in role is
  immutable by design, and the guard's own comment recorded the assumption it
  rested on: "there is no built-in permission reconcile path on boot, so this
  refusal costs no existing caller." That expired the first time a permission
  was renamed. Roles are copied out of `BuiltInRoles()` when a tenant is
  provisioned and never touched again, so a tenant created before the rename
  keeps the retired verbs forever and never gains the live ones — while update
  is refused, delete-and-reseed is refused, and create-over-the-top collides
  with the name index. Reconciliation is admitted because its target is not
  arbitrary: it is `BuiltInRoles()` itself, the definition the guard exists to
  protect. Every other caller is still refused. It is idempotent (a converged
  tenant costs ZERO store writes), touches only `IsSystemRoleName` names so
  custom roles survive untouched, and repairs Owner when present without ever
  creating it — Owner is posture-conditional. It must be called AFTER module
  permission registration, because `BuiltInRoles()` reads the registry at call
  time. Reported per role as `ReconcileReport` / `RoleReconcileResult`
  (permissions added, permissions removed, created).
- **`Role.RetiredPermissions` — drift visible on read, before the repair runs**
  (#354). Derived on every read from the live permission registry, listing the
  stored permissions this build no longer defines, and NEVER persisted:
  `marshalRole` is the single seam every store write passes through and clears
  it. Surfaced on `GetRole`, `GetRoleByName` and `ListRoles`, and on every
  transport's role payload. `Role.RetiredPermissionError()` returns the typed
  `*RetiredPermissionError`; no read path returns it as a failure, because a
  listing that failed on drift would hide exactly the roles an operator needs
  to see.
- **`permission.Unregistered(perms)`** (#354) — screens a whole permission list
  against the registry under ONE read lock. Every role read now screens its
  permissions and Owner holds `All()`, so a loop over `IsRegistered` would take
  a global RWMutex per permission on the authorization path.
- **The reconcile verb on every transport and in the CLI** (#354): REST
  `POST /roles/reconcile`, gRPC/Unix `ReconcileBuiltInRoles`, QUIC
  `role.ReconcileBuiltIn`, MCP `qrdb_role_reconcile_builtin`, embedded, and
  `qrdb roles reconcile [--tenant ID]`. It carries `roles:create` rather than a
  permission of its own: reconciliation IS seeding applied to an existing
  tenant, and a dedicated verb would be absent from every role seeded before it
  existed — so nobody would hold the grant needed to repair that exact absence.
  `qrdb roles get` prints a `Retired:` line. Re-exported by `sdk/go`:
  `ReconcileReport`, `RoleReconcileResult`, `RetiredPermissionError`,
  `RetiredPermissions`; and by `sdk/go/transport`:
  `RoleClient.ReconcileBuiltInRoles`, `ReconcileRolesRequest`,
  `ReconcileRolesResponse`, `RoleReconcileResult`, `Role.RetiredPermissions`.

### Fixed

- **`docs/identity-rbac/api.md`'s role capability matrix had drifted from the
  code** (#354). `GET /roles/by-name/{name}`, `GET /roles`,
  `POST /permissions/resolve`, `permission.List` and `permission.Resolve` named
  routes and methods that do not exist; corrected to `GET /roles/by-name?…`,
  `GET /roles/list`, `POST /roles/resolve-permissions`, `role.ListPermissions`
  and `role.ResolvePermissions`.

## [0.16.2-alpha] - 2026-09-05

### Added

- **`service.AttestationRecorder` — the trusted-server path for recording
  what an enrollment ceremony proved** (#342). `AddAttested` / `RotateAttested`
  take a typed `auth.VerifiedAttestation{Level, AAGUID}` and are the ONE
  writer of the server-owned `attest_*` factor-metadata namespace. The
  production user auth-factor service implements it alongside
  `UserAuthFactorServicer`; the in-process host that verified the attestation
  reaches it by type assertion, as it reaches `ActiveFactorChecker`. It is
  deliberately absent from the servicer interface and from every transport —
  no handler maps to it, so nothing on any wire can produce a verified
  attestation. Caller-supplied `attest_*` keys are refused on this path
  exactly as on `Add` (#234's protection is intact), and a level outside
  `none` / `software` / `hardware` is refused with `ErrInvalidAttestation`
  before anything is written. Re-exported by `sdk/go`: `AttestationRecorder`,
  `VerifiedAttestation`, `AttestationLevel` and its three values,
  `Unattested`, `MetadataAttestLevel`, `MetadataAttestAAGUID`,
  `ReservedMetadataPrefix`, `InvalidAttestationError` / `ErrInvalidAttestation`,
  and — missing since #234 — `ReservedMetadataKeyError` / `ErrReservedMetadata`.

### Changed

- **Every `Add` and `Rotate` on the user auth-factor service now records
  `attest_level=none`** (#342). A factor written without a ceremony proved
  nothing, and the server says so rather than leaving the key absent, so a
  record that proved nothing is distinguishable from one written before the
  field existed. The response carries the persisted metadata, stamp included.
  Callers that stamped `none` themselves to get this distinction must stop:
  the key is now refused as input on every path and written by the server.

### Fixed

- **`sdk.UserServicer` now returns `sdk.UserNotFoundError`** (#343).
  go-qrdb#289 (v0.16.1-alpha) moved `sdk.UserNotFoundError` from an alias of
  `pkg/user.UserNotFoundError` to an alias of `transport.UserNotFoundError`
  so every wire transport matched one type — but the re-exported servicer
  kept returning the service type, which the SDK no longer exported, so a
  servicer consumer honouring the `sdk/go` boundary could not classify a
  missing user at all (go-quicbaas's first-boot admin creation failed on
  its email-uniqueness check). `pkg/user.UserNotFoundError` is now an alias
  of the transport type: ONE type, returned by the servicer in-process and
  by every transport, matched by one `errors.As` against
  `sdk.UserNotFoundError`. The embedded transport's service-to-transport
  translation is deleted — there is nothing left to translate. The message
  is `user: not found: <id|lookup>` whichever layer returns it.

## [0.16.1-alpha] - 2026-09-05

### Added

- **`orchestrator:task_readmit` and `orchestrator:task_block`** (#339), beside
  `orchestrator:task_claim` in the broker data-plane `orchestrator` family.
  `task_readmit` returns a blocked orchestration task to the pending queue;
  `task_block` parks a task as blocked (`needs_info` / `blocked` / `escalated`)
  with a reason. Both are `ScopeTenantOnly` like every other verb in the family
  — denied to the system tenant, confined to the identity's own tenant — and are
  placed exactly where `task_claim` is: Operator holds them (and Admin through
  `orchestrator:*`); User and Auditor do not. The family stays qrdb-owned, which
  is what lets go-quicbaas register the two verbs it maps on its own transports
  without forking the resource.

## [0.16.0-alpha] - 2026-09-02

### Added

- **A real account lifecycle on `user.User`** (#196). `Enabled bool` could not
  express the states an identity actually moves through, so every consumer above
  qrdb carried its own — go-quicbaas kept a `BrokerProfile` side-record with a
  state enum plus separate `suspended` and `locked` booleans, an encoding that
  admits contradictions (locked *and* active) with no rule saying which wins.
  `Lifecycle` is now the single source of truth — `pending` · `active` ·
  `suspended` · `locked` · `disabled` · `deleting` — with a fixed transition
  table, and `Enabled` is a derived projection (`Lifecycle == active`) kept
  because it is the field existing clients read. `User.CanAuthenticate()` is the
  check, and it fails closed on a state the build does not recognize.
  `deleting` is **terminal**: a deletion writes it before its first mutation, so
  a credential ceremony that finishes late cannot leave a fresh orphan behind
  (the shape go-quicbaas#290 needs to close its registration-during-deletion
  TOCTOU). Re-asserting a state is a no-op, so a partially completed deletion is
  resumable.
- **`GroupIDs`, `AuthMethods` and `LastAuth` on `user.User`** (#196), settable
  through `CreateUserRequest` and `UpdateUserRequest`. `GroupIDs` stores the
  membership EDGE — qrdb owns no Group entity, exactly as `RoleIDs` holds role
  identifiers without owning the roles; a group's name and the roles it confers
  belong to the layer that performs the authorization consuming them.
  `AuthMethods` is a policy statement of what an account may present, distinct
  from `Factors`, which records what it has enrolled. `LastAuth` is a high-water
  mark that never moves backwards, so overlapping logins written out of order
  cannot roll an account's last-seen time back.
- **`NormalizeGroupIDs` / `NormalizeAuthMethods`** (#196) — the canonical
  identity-set normalizers, re-exported under `sdk/go` beside `NormalizeEmail`.
  Group ids preserve case (opaque minted tokens); auth methods fold case (a
  closed protocol vocabulary).
- **Every new identity field on every transport** (#196). REST, gRPC, QUIC, MCP,
  unix and all six `sdk/go/transport` clients carry `lifecycle`, `groupIDs`,
  `authMethods` and `lastAuth` on both the response and the create/update
  requests. `enabled` collapses five distinct states into one `false`, and a
  field the domain stores while the wire drops it is a field a remote consumer
  cannot see — which is how a consumer ends up keeping its own copy in the first
  place.
- **The clear-versus-leave-alone distinction survives every wire** (#196).
  `nil` leaves a set alone and an empty non-nil slice CLEARS it, and each
  transport carries that in its own idiom: JSON gets it free (absent / `null` /
  `[]`), gRPC needs explicit `has_group_ids` / `has_auth_methods` flags because
  proto3 cannot distinguish an absent repeated field from an empty one, and the
  MCP tools take `clear_group_ids` / `clear_auth_methods` because a flat tool
  argument cannot either. Without them a membership could be granted but never
  **revoked**: an empty list would read as "no change" and the revocation would
  report success while doing nothing.
- **The MCP identifier lists are JSON arrays, not comma-separated strings**
  (#196). A group id is an opaque minted token, so a comma is a character it may
  legitimately contain: CSV split such an id into two memberships and dropped
  blank elements the store refuses, writing a different authorization graph than
  the caller asked for while reporting success. A malformed list is now reported
  rather than read as "leave alone", because quietly not changing an
  authorization list the caller asked to change is the same defect wearing a
  different hat.

### Changed

- **`AuthMethods` is enforced at the authentication gate**, not merely stored
  (#196). `pkg/auth/middleware` now carries the name of the authenticator that
  succeeded and refuses with `AuthMethodNotPermittedError` (403 /
  `PermissionDenied`) when the account's policy is non-empty and does not list
  it. Storing the policy without checking it made method withdrawal fail
  **open**: a user restricted to `webauthn` could still walk in through the
  mTLS authenticator, because the credential was valid and nothing compared it
  against the policy. An empty policy states no restriction, so no account that
  predates the field is affected; and the check applies only where the method is
  observable to qrdb, because a forwarded assertion and a capability module's
  session token carry none it could evaluate. That second limit is carried by an
  explicit flag rather than by an empty name — an authenticator that names
  itself `""` is observed, not unobservable, and conflating the two would have
  let it bypass the policy through the ordinary chain.
- **`DeleteUser` claims the record by marking it `deleting` before the cascade**
  (#196), and the cascade commits against the marked version. Until the mark
  landed the delete had not won the record: another writer could still bind a
  credential or move a handle, and the cascade was sweeping a snapshot that no
  longer described the account. The account is now unauthenticable from the
  FIRST write of the delete rather than the last.
- **A terminal record accepts no mutation at all** (#196). `UpdateUser`, the
  `LastAuth` merge path and `AddCertBinding` all refuse it. Refusing only the
  lifecycle transition left every other field writable, so a certificate could
  still be bound to an account whose deletion had begun — the exact orphan the
  marker exists to prevent. Re-asserting the terminal state stays permitted, so
  a partially completed deletion is still resumable.
- **Lifecycle refusals map to client-actionable statuses** (#196).
  `InvalidLifecycleError` is HTTP 400 / gRPC `InvalidArgument`; a
  `LifecycleTransitionError` is HTTP 409 / gRPC `FailedPrecondition`. Neither
  had a mapping arm, so the commonest instance — an `EnableUser` racing a
  delete — surfaced as a 500 / `Internal`: a server fault reported for a refusal
  the server made deliberately, and one no client could learn to expect.
- **A `LastAuth`-only update is a commutative merge that retries** (#196)
  instead of returning `ConcurrentUpdateError`. `LastAuth` is written on the
  login path while ordinary updates are where an administrator edits roles;
  routing the login through the general path made its timestamp lost — and lost
  *below* the true high-water mark, because the loser computed its max against a
  base the winner had already replaced — whenever the two overlapped. The retry
  is bounded and sleepless, and the merge writes nothing at all when the stored
  value is already at or past the request.
- **`EnableUser` and `DisableUser` are lifecycle transitions**, not boolean sets
  (#196). Both are refused with a `LifecycleTransitionError` on an account
  already marked `deleting` — a plain `Enabled = true` would have revived an
  account whose deletion was in flight. Repeating either verb is still a no-op.
- **Records written before this release need no migration** (#196). A decode
  with no `lifecycle` derives one from `Enabled` (`true → active`,
  `false → disabled`) and re-derives `Enabled` from it, so no pre-upgrade
  account is locked out by an empty state and the explicit field is picked up
  the next time the record is written. A state that is present but unrecognized
  — written by a newer node mid-upgrade — is left exactly as written.

### Fixed

- **`docs/identity-rbac/engineering.md` had drifted** (#196): its `User` struct
  still showed a `string` ID and no `Email`. Corrected alongside the new fields.
- **`sdk/go/go.mod` required a stale root** (#196). It named
  `github.com/jeremyhahn/go-qrdb v0.10.0-alpha` while the tags had reached
  v0.15.0-alpha. Its `replace ... => ../..` keeps the repository building, but a
  `replace` in a dependency module is ignored by consumers, so an external
  module taking `sdk/go` alone resolved a root five releases behind it. Bumped
  in the release commit that cuts this version, and verified against a throwaway
  external consumer.
- **The release notes for 0.13.0-alpha, 0.14.0-alpha and 0.15.0-alpha were never
  written** (#196), so `make check-version-reconciled` — the check that exists
  precisely to catch this — had been failing on `main` since v0.13.0-alpha:
  VERSION read `0.15.0-alpha` while the newest CHANGELOG release heading still
  read `0.12.0-alpha`. The three sections are reconstructed below from the
  release commits and tag annotations, which restores the check to green.

## [0.15.0-alpha] - 2026-09-01

Notes reconstructed for a release that shipped without them (see the
[Unreleased] entry for #196, which found the gap). Content is taken from the
release commit and the tag annotation, not re-derived.

### Added

- **`Email` is the canonical tenant-unique login key** (#191). `User.Email` and
  `User.Name`, a tenant-scoped email secondary index, and `GetUserByEmail`
  across REST, gRPC, QUIC, MCP, the SDK and the CLI.

### Changed

- **`CreateUser` refuses a user with no email login key** unless the caller sets
  `CreateUserRequest.WithoutEmailLoginKey` (#191). A certificate-only or
  bootstrap identity is legitimate; an ordinary user silently created with no
  entry in the index every login resolves through is not. **Breaking.**

## [0.14.0-alpha] - 2026-08-31

Notes reconstructed from the tag annotation; see the note under 0.15.0-alpha.

### Added

- **Deferred tenant-admin enrollment seam** (#189, #193).
  `AdminEnrollmentMode` on `ProvisionTenant` — `cert` (default) or `deferred`
  (pending-invite) — with fail-closed pending-admin validation including a
  canonical-factor-store check (`service.ActiveFactorChecker` /
  `HasActiveFactor`), and `user.CreateUserRequest.Pending`.
- Registration-config ownership documented (it belongs to quicbaas
  `iam.self_service_registration`, not here).

## [0.13.0-alpha] - 2026-08-28

Notes reconstructed from the tag annotation; see the note under 0.15.0-alpha.

### Added

- **Coordination surface for control-plane consumers** (#166, #168) —
  lock / lease / CAS / watch. Hard acknowledged-deadline lease-loss model,
  `LeaseLostError` on late acks, a terminal-error contract on `WatchStream`, and
  bounded cleanup revoke.

### Fixed

- **`dek_accesses` counts reads, not attempts** (#164) — the counter is
  incremented inside the borrow.
- **`ChangePassword` leaves the barrier still checking passwords** (#161).
- **`withCurrentDEK`'s purge borrow is held across the callback**, with both
  escapes fenced (#82).

### Security

- Bumped echo to v4.15.3 for CVE-2026-55677 (#155).

## [0.12.0-alpha] - 2026-08-25

The guarded-verb plane: the conditional writes that arbitrate a cross-process
race are now reachable on every engine, in both deployment modes, and over the
remote transports — not only through the embedded server. A 12-cell conformance
matrix is the gate that says so, and it found two real defects on its way in.

### Added

- **`CompareAndSwap` on the transport boundary** (#121). `service.KVServicer`
  has carried all three guarded verbs since #56, but only `PutIfAbsent` and
  `CompareAndDelete` reached `transport.GuardedKVClient` in #119. A consumer
  holding the capability could arbitrate a create and a release and still had to
  read-then-write to *edit* — reopening the window the capability exists to
  close, over a network round trip on a remote transport. Implemented on the
  embedded and gRPC transports; QUIC, unix, REST and MCP are tracked as
  #124–#127.
- **The guarded verbs over gRPC** (#119, #123): `PutIfAbsent` and
  `CompareAndDelete` as RPCs, delegating to the service layer's atomic
  primitives with no client-side emulation.
- **`transport.GuardedKVClient`** (#119) — a capability sub-interface detected
  by type assertion, so a transport that cannot honour the verbs does not
  promise them.
- **`CompareAndSwap` and transactions on the file KV engine** (#117, #120), at
  proven parity with the MVCC engine.
- **A 12-cell conformance matrix** (#121): `{file, memory, pebble}` ×
  `{standalone, raft}` × `{embedded, remote-gRPC}`, asserting identical guarded
  semantics on every cell. It runs in-process in the ordinary test pass rather
  than forking the daemon, so a regression turns something red the same day.
- **`kvstore.GuardedBatchKVStore`** (#21) — a capability sub-interface for
  applying several writes atomically, conditional on one key still holding an
  expected value. Detected by type assertion, like `AtomicKVStore`; a store
  without a transaction plane does not advertise it.
- **A typed capability refusal** (#141). `transport.CapabilityUnsupportedError`,
  produced on every transport, so a consumer can detect "this node was not built
  to do that" with `errors.As` and degrade gracefully instead of parsing an
  error string.

### Changed

- **A capability refusal answers `Unimplemented` over gRPC, not `Internal`**
  (#141). `Internal` means the server broke; a client library alerts on it and
  retries an operation that can never succeed. REST answers 501 for the same
  condition, as it did before. **Consumers keying on `codes.Internal` for this
  condition must update.**
- **Registry capability descriptors now describe what the state machines can
  actually do** (#135). The `memory`, `standard`, `concurrent`, `fast`, `disk`
  and `highperf` state machines declared `mvcc`, `transaction`, `lease`, `index`
  and `timeseries` and could perform none of them — every registry factory
  constructs with zero options, so those subsystems are nil. Those flags are
  removed, and `regular` gains the `batch` capability it always had.
  **`sdk.SMDescriptor` consumers will see different `Capabilities` values.**
  Nothing in the database gates an operation on these flags; they are
  informational, and they were misinforming.

### Fixed

- **An unimplemented opcode no longer wedges the raft shard** (#129).
  `KVStateMachine.Update` returned `InvalidOpcodeError` to Raft, which treats a
  non-nil `Update` error as a fatal apply failure and trips the shard's circuit
  breaker. Measured: one ordinary authorized `CompareAndDelete` — an opcode no
  state machine in the package implements — permanently removed the shard from
  service. Every later proposal failed, including plain `Put`, and a
  linearizable read never returned at all. Reachable through the SDK's own
  guarded verbs with nothing more than `kv:delete`. Such an operation is now
  refused with a typed error and the shard keeps serving.
- **A batch naming an unimplemented operation is refused whole** (#129). The
  batch handlers applied operations in turn and only refused on *reaching* the
  unknown one, committing the earlier writes and their watch notifications while
  reporting the batch refused. Opcodes are now validated before anything is
  applied.
- **Transactions are served over the remote transports in standalone mode**
  (#121). `sdk/go/node` composed the transaction service only on the raft path,
  so `ExecuteTransaction` worked over the embedded transport and returned
  "transaction service is not configured" over gRPC on the same node — the
  engine/mode/transport non-uniformity #117 rules out.
- **A custodian enrolment is written as one guarded transaction** (#21). Its
  three keys — the primary record, the tenant index, and the username-index
  commit — are applied atomically, conditional on the registration still owning
  the username claim. Claims expire, so a registration that stalled past the TTL
  could previously lose its claim between an ownership check and its write, land
  that write on top of the winner's, and leave the winner's published index
  pointing at the loser's record: two callers returning success with different
  sealed envelopes, one record surviving, nothing reporting it.
- **A claim is released with a compare-and-delete** (#21), so seal-failure
  cleanup can no longer remove a claim another registration has legitimately
  taken over in the meantime.
- **Shamir share and secret custody moved into protected memory** (#76).
- **A data race in the QUIC e2e index-flags test harness** (#131), which could
  also make its polarity assertion pass for the wrong reason.

### Changed — BREAKING (sdk/go)

- **`IndexScan` expresses an open bound** (#115), so a one-sided range returns
  its matches. A consumer building index scans must update its bound
  construction.

### Scope of the guarded plane in this release

Stated explicitly, because "identical on every cell" has a boundary:

- **Standalone** — all three backends serve the full guarded plane on both the
  embedded and gRPC transports.
- **Raft, default `sm_type`** (`""`, `pebble`, `mvcc`) — full guarded plane.
- **Raft, non-default `sm_type`** (`memory`, `standard`, `concurrent`, `fast`,
  `disk`, `highperf`) — `PutIfAbsent` works and is genuinely atomic, being a
  native KV opcode. `CompareAndSwap`, `CompareAndDelete` and transactions
  **refuse with a typed error and the node keeps serving**. Those state machines
  never had a working MVCC store, and they are refused outright when the seal
  barrier is enabled. Giving them one is tracked as #136; when it lands, those
  matrix cells flip from refusal to full passage.
- **The file engine refuses `create_revision`/`mod_revision` transaction
  compares.** This is designed behaviour, not a limitation to be worked around:
  the engine keeps no MVCC revisions, so a revision compare would read 0, two
  concurrent writers would both compare equal and both commit, and one update
  would be lost with no signal. The native `PutIfAbsent` verb works there, so no
  capability is unreachable — only the etcd revision idiom.
- **Remote transports** — embedded and gRPC. QUIC, unix, REST and MCP do not
  carry the guarded verbs yet (#124–#127); their `GuardedKVClient` assertion
  correctly reports false until they do.

### Internal

- CI now runs the `sdk/go` module's tests and requires them in the gate (#132).
  They had never been executed by CI, so the entire guarded-verb transport plane
  this release is built on had merged on green runs that never exercised it.

## [0.11.0-alpha] - 2026-08-24

### Added

- **Standalone-mode transactions and compare-and-swap** (#62, #109). The
  standalone server now serves the transaction plane and CompareAndSwap, so a
  single-node deployment has the same conditional-write primitives a cluster
  does. This is the primitive `xkmsd` needs to fresh-boot on a qrdb backend
  (unblocks go-xkms #61).
- **`kvstore.CompareAndSwap`** (#81) — the guarded edit the atomic set was
  missing: replace a value only when the current value matches the expected one.
- **`sdk/go` `GuardedSwap`** (#78) — an optimistic-CAS loop over the transaction
  plane that retries a read-modify-write until it commits with no conflicting
  writer.
- **Write results now report whether a failed write was stored** (#103), stated
  at the layer that knows rather than inferred by the caller.
- **`sdk/go` re-exports `UsableAsMapKey`** (#68), so a consumer can guard a
  per-store cache without reaching past the SDK boundary.

### Changed — BREAKING (sdk/go)

- **Index creation carries case-sensitivity end to end** (#34, #108). The
  `sdk/go` index-create surface (`kvstore_adapter`, every transport's
  `transport_index`) gains a case-sensitivity flag and its request shape
  changes. A consumer pinning `sdk/go` must update its index-create calls.

### Fixed

- **A guarded write is durable before it acknowledges** (#94, #110). The state
  machine persists the write before returning success, so an acknowledged
  guarded write survives a crash.
- **Built-in roles are actually immutable** (#105): the `System` flag is derived
  from the role name rather than trusted from the stored record, so a built-in
  role cannot be made editable by clearing the flag.
- **Vector IVF clustering is seedable** (#99), and the recall test is fixed at
  its source — the single shared random init — instead of by averaging
  downstream of it.
- **The quota reservation is kept on every ambiguous write, not just the swap**
  (#98): a write whose stored/not-stored outcome is unknown no longer leaks
  quota.
- **The cert watcher is released on every `runStart` return, not only the clean
  one** (#97).
- **Snapshot framing is bounds-checked through the same predicate the encoders
  use** (#80).
- **`OpPutIfAbsent` is applied everywhere, and first boot reports its real
  error** (#87).
- **A version condition on an entity with no version is refused, not
  fabricated** (#75).
- **MVCC recognises the absence spelling every storage engine actually
  produces** (#77).
- **A failed compare read stalls the transaction instead of silently picking a
  branch** (#64).
- **An empty index tag is a registration error, not a silent no-op** (#67).
- **A not-configured subsystem is encoded so its reader can decode it** (#70).
- **A DAO miss is reported as NOT FOUND over both stores the SDK ships** (#65).
- **One bounds-checked length writer in the protocol**, so the omission cannot
  compile (#53).

### Fixed — SECURITY

- **Purge-class memguard calls are coordinated against live borrows** (#72), so
  a purge cannot free memory another goroutine is still reading.
- **The unseal ceremony fails closed on an unreadable share and holds the PIN
  borrow across the KDF** (#91).

## [0.10.0-alpha] - 2026-08-20

### Fixed — SECURITY

- **`StoreResolver` no longer hands every shared-prefix tenant the same
  unconfined store** (#41). It returned `r.shared` itself for the system
  tenant, every `IsolationPrefix` tenant and every unknown tenant, with no
  keyspace prefix applied. A `dao.GenericDAO` keys entities as
  `{entityType}/{id}` and has no tenant concept, so two prefix tenants' DAOs
  occupied one undifferentiated keyspace: measured on a two-tenant node, one
  tenant's `Count()` returned another's rows and one tenant's `QueryByIndex`
  returned another's rows.

  The data-level exposure pre-dates v0.8.0-alpha; the index-level exposure
  became reachable in v0.8.0-alpha, which made those index paths real.

  `Resolve` now returns a store confined to `{tenant}/` — the tenant alone.
  This store serves IDENTITY (users, roles, permission scopes, invitations),
  and identity belongs to a tenant rather than to one of its databases: a user
  must authenticate whichever database a request names. Namespacing it per
  database was measured to break login outright, because firstboot writes the
  admin user with no database bound to its context.

  `tenant.ComposeNamespace` remains the DATA plane's `{tenant}/{database}/`
  formula, applied by the transports and the SQL provider; that scoping is
  unaffected.

  Either way the isolation is a property of the store rather than of every
  caller remembering to prefix.

  Concrete consequence: `invite.TenantStore` documented that "a redemption
  attempted against the wrong tenant resolves to a different store and returns
  ErrNotFound". That was true for barrier tenants and **false for prefix
  tenants**, whose invitations shared one keyspace — and `Entity.TenantID` was
  written but never compared, so nothing else caught it. An invitation issued
  for prefix tenant A was redeemable through tenant B. The fix is what makes
  the documented guarantee true; the doc comment now records the correction.

### Changed — BREAKING (key layout)

- **Every record a shared-prefix tenant reaches through `StoreResolver` moves
  under `{tenant}/`.** This includes the reserved `__sys:` keyspace —
  users, roles, permission scopes — and DAO-keyed entities alike. There is one
  tenancy mechanism, the namespace, and no prefix exempt from it.

  **Pre-release policy** (`release-versioning.md` §1): backward compatibility,
  migrations and upgrade paths are non-goals before public release. The key
  layout changed clean. **Wipe and re-bootstrap dev deployments** — that is the
  upgrade path. Nothing is "orphaned"; the old layout simply ceases to exist.

### Fixed — regression introduced by the isolation above

- **Every IAM operation that names a record by its globally unique ID now
  reaches the tenant that HOLDS it** (#41). Confining each tenant's keyspace
  broke `users delete <id>`, `users get/enable/disable <id>`,
  `roles delete <id>` and `roles assign --role <id>` for any record created
  with `--tenant-id`: the by-id methods routed on the CALLER's tenant, and the
  admin's tenant is `system`, so the record was looked up in a store it does
  not live in. Measured on the integration stack: five CLI suites failing
  identically across all five transports with `user: not found` /
  `role: not found`.

  They worked before only by accident — every prefix tenant shared one
  keyspace, so "the caller's store" and "the record's store" were the same
  store. That accident is exactly what #41 removes, so the by-id route is now
  made explicit.

  `tenant.IdentityIndex` is a system-plane id → tenant map living in the
  system tenant's identity namespace — `tenant.IdentityNamespace`, the same
  single formula, at `__sys:identity_idx/tenant/{kind}/{id}`. An entry carries
  the id and the tenant and **nothing else**: it necessarily sits outside a
  barrier tenant's encryption, so it may leak existence and placement, never
  a username, role name or permission. It is written and removed by the same
  functions that write and remove the record — `CreateUser`/`DeleteUser`,
  `CreateRole`/`DeleteRole` — alongside their other index writes, because a
  mapping kept in step by a sibling caller is how every instance of this class
  began.

  The entry is a CLAIM, not a write. `CreateRoleRequest.ID` is
  caller-supplied — it travels from the REST and gRPC surfaces straight into
  the store — so a last-writer-wins map would let one tenant create a role
  carrying ANOTHER tenant's role id, take over that id's entry, and silently
  redirect every by-id operation an administrator performs on it. `Record`
  now refuses an id ANY tenant already holds — the claimant's own tenant
  included, because a create proceeding on a claim it did not win can have
  that claim released out from under it by a concurrent same-tenant delete,
  leaving a record with no entry and no way to reach it by id. A claim also
  requires a store that can settle it in one operation
  (`kvstore.AtomicKVStore`); there is no read-then-write mode, because two
  proposers would both observe "absent" and both win. `Record`
  refuses a held id with `*tenant.IdentityClaimConflictError` (
  surfaced to callers as `*role.RoleIDExistsError` → 409 / `AlreadyExists`,
  naming the CALLER's tenant so the refusal does not disclose where the id
  lives), `Forget` releases only a claim the named tenant holds, and the claim
  is settled BEFORE the record is written so a refused capture leaves nothing
  behind. `CreateRole` additionally refuses a duplicate id within the tenant,
  closing a pre-existing silent overwrite.

  `tenant.OwningTenant` is the one read-path rule both routers share: an
  unindexed id routes to the caller's own tenant (reach never widens); an
  indexed id routes to its owner when the caller may reach it; and a caller
  who may NOT reach it is routed to its own tenant, where the id is genuinely
  absent — a `CrossTenantDeniedError` there would confirm to a caller who
  supplied only an id both that the record exists and where it lives. A sealed
  owning barrier surfaces `tenant.ErrTenantSealed`; there is no fallback read.

  No CLI or API surface changes. `role.ResolvePermissions` is deliberately NOT
  index-routed: it resolves the caller's own bindings during authorization, and
  routing it globally would let a binding naming another tenant's role import
  that role's permissions.

- **`pkg/engine.KVEngine` applies `kv.OpPutIfAbsent`.** The opcode already
  existed in the command format, `kvstore.AtomicKVStore` advertised it, and
  `service.KVService.PutIfAbsent` proposed it — but of the three state
  machines only `PebbleStateMachine` (the `qrdbd --sm pebble` default) and
  `MVCCEngine` had a handler. On `--sm memory`, `--sm concurrent` and every
  `pkg/server/lifecycle` embedding, a conditional write failed the **entire
  proposal** with `engine: unsupported operation: 0x04`. The handler mirrors
  its two siblings exactly: the existence check runs inside the apply path so
  every replica decides identically, a declined write is reported through
  `Result.Value` (`kv.ResultNotStored`) rather than as an error that would
  stall consensus, secondary and compound indexes are maintained on the stored
  path, and the watch notification carries no `PrevKV` because the guard
  established the key was absent. All three implementations are now pinned by
  the same contract, including "of N conditional puts to the same key, exactly
  one stores, under every apply grouping".

- **`kvstore.AtomicKVStore` gains `CompareAndDelete`, and every state machine
  implements it** (#41). `PutIfAbsent` guarded the create; the release was a
  plain delete, and a guarded create paired with an unguarded delete is half a
  primitive. Two releasers both read holder A, the first deletes, tenant B
  legitimately claims the now-absent id, and the second delete removes **B's**
  claim — leaving B holding a record with no routing entry, the state the
  index exists to prevent, reached through the release path. No amount of
  re-reading closes it, because the window is between the read and the delete.

  New `protocol.OpCompareAndDelete`, handled by `KVEngine`,
  `PebbleStateMachine` and `MVCCEngine` alike, with the comparison evaluated
  inside the apply path so every replica decides identically. A refused delete
  is reported through `Result.Value`, never as an error that would stall
  consensus; a failed guard READ refuses rather than deciding, exactly as on
  the conditional-put path. `tenant.IdentityIndex.Forget` now releases through
  it.

  Encrypting wrappers (`seal.StorageBarrier`, `seal.atomicTenantSharedKVStore`,
  `database.BarrierBackend`) guard on the **ciphertext they read**, not on a
  re-encryption of the expected value: encryption is randomised, so a
  re-encrypted guard would refuse every time, while the observed ciphertext
  still refuses whenever anything wrote in between.

  One carrier genuinely cannot: `seal.StorageBackend` is a type alias for
  go-quicraft's interface, which has `PutIfAbsent` but not its release half.
  Those two call sites discover the capability and refuse with
  `seal.ErrGuardedDeleteUnsupported` rather than degrading — every backend in
  this repository implements it.

### Added

- `tenant.IdentityIndex`, `tenant.NewIdentityIndex`,
  `tenant.NewResolvedIdentityIndex`, `tenant.OwningTenant`,
  `user.WithIdentityIndex`, `role.WithIdentityIndex`,
  `tenant.IdentityClaimConflictError`, `role.RoleIDExistsError` — see above.
  The Option form keeps the index out of single-tenant embeddings, which have
  nothing to route.
- `kvstore.NewNamespacedKVStore` — a `KVStore` decorator confining every
  operation, including index registration and queries, to a key prefix. Only
  the variant over an atomic inner store implements `AtomicKVStore`, so the
  capability assertion answers honestly. An empty prefix is an error rather
  than a pass-through.
- `kvstore.EntityIndex.KeyPrefix` and `ResolvedPrefix` — the key prefix an
  index covers. Stores previously assumed `{entityType}/`, which holds only
  while the entity type is the LEADING key segment; a namespaced store breaks
  that and would silently index nothing.

### Known limitation

- **Shared-prefix tenants trade engine-backed indexes for isolation — the
  system tenant included** (#40 restores them). `KVEngine` decides which
  indexes a write maintains via `entityTypeFromKey`, which returns the FIRST
  path segment — so a namespaced key `acme/system/workitem/1` reads as entity
  type `acme`. The adapter refuses the namespace-qualified index name and the
  store refuses subsequent index queries with `kvstore.ErrIndexUnsupported`.
  Query RESULTS stay correct: `pkg/dao` detects the refusal and plans a full
  scan, so only the access path is slower. Teaching the engine about
  namespaced keys is #40's priority-1 mechanism.

## [0.9.0-alpha] - 2026-08-20

MINOR rather than PATCH: no qrdb API moves here, but `go-quicraft` advances TWO
minors and changes cryptographic behaviour on the AES-GCM data path, which this
module had to absorb at one call site.

### Changed

- **`go-quicraft` v0.6.2-alpha to v0.8.0-alpha**, `go-objstore` v0.2.2-alpha to
  v0.3.0-alpha. `go-truststrap` (v0.2.0-alpha) and `go-codec` (v1.4.0) were
  already current.

- **The wrapped-root-key envelope absorbs go-quicraft's AEAD nonce contract
  change.** In v0.7.0-alpha `pkg/seal` moved to `cipher.NewGCMWithRandomNonce`
  — the only GCM construction approved under `GODEBUG=fips140=only`, since SP
  800-38D requires the module itself to own nonce generation. Measured at the
  two pins:

  | | v0.6.2-alpha | v0.8.0-alpha |
  |---|---|---|
  | construction | `cipher.NewGCM` | `cipher.NewGCMWithRandomNonce` |
  | `aead.NonceSize()` | 12 | 0 |
  | `aead.Overhead()` | 16 | 28 |

  `pkg/seal/unsealkey.go` was the one place in this repository that took a raw
  `cipher.AEAD` from that package and framed its own nonce: `wrapRootKey` sized
  a nonce from `aead.NonceSize()` and persisted it in the `Nonce` field of the
  `wrappedRootKey` JSON envelope, beside `Ciphertext`, and `resolveDirectSecret`
  length-checked that field against `aead.NonceSize()` before `Open`.

  **A straight bump would have been silent and would have cost the store.**
  Nothing fails to compile — `cipher.AEAD` is unchanged — and a fresh barrier
  works, because new writes and new reads stay consistent with each other. But
  the write path starts emitting a ZERO-length nonce with a 60-byte ciphertext
  where the format has always carried 12 and 48, and the read path rejects every
  blob written before the bump with `ErrWrappedRootKeyCorrupt`. That blob is the
  barrier root key wrapped under the unseal key: an existing barrier stops
  unsealing. Measured on a real pre-bump blob, not inferred.

  The fix keeps the persisted bytes IDENTICAL in both directions rather than
  migrating them. `NewGCMWithRandomNonce` emits `nonce ‖ ciphertext ‖ tag`,
  which is exactly the pair the envelope already stored in two fields, so
  `wrapRootKey` splits the module-generated prefix back off and `unwrapRootKey`
  re-joins it before `Open`. Old blobs open unchanged, new blobs are
  indistinguishable from old ones, and **no migration is required** — which is
  the point: a wrapped root key is written once at initialization and read on
  every unseal for the life of the barrier, so a format change here would need a
  migration executed while the barrier is still sealed, i.e. while the key
  material needed to authenticate it is by definition unavailable.
  `pkg/ceremony`'s `sealEnvelope` splits the same prefix for the same reason;
  this is that pattern, not a new one.

  The envelope's nonce-length check now reads `SymmetricAlgorithm.NonceSize()`
  — the WIRE width, which stayed 12 — instead of `cipher.AEAD.NonceSize()`,
  which is now zero and would accept a nonce field of any length at all.

  Decoding and decryption of the envelope moved out of `resolveDirectSecret`
  into `unwrapRootKey`, so the format rules have one home and are reachable from
  a test without standing up a storage backend.

### Added

- **`pkg/seal/unsealkey_test.go`** gains tests that open a wrapped-root-key blob
  produced by this repository at go-quicraft v0.6.2-alpha, checked in as bytes
  rather than regenerated. Regenerating it would let the test drift onto the new
  format and start asserting only that the new code reads its own output — which
  is precisely the failure mode that makes this class of break invisible. It
  also asserts the write path still emits the 12/48 field widths, that the AAD
  still binds the blob to its `RootKeyPath` (sealing with one barrier and
  opening with another, so the assertion cannot pass merely because the AAD
  changed), and that a nonce field of the wrong width is rejected. All
  mutation-verified.

### FIPS 140-3 posture — the caller-supplied-IV class is GONE

Measured on both sides of this bump from the SAME base commit (414291c), one
script, one machine, Go 1.26.6, `GOFIPS140=latest GODEBUG=fips140=only`,
`-race`, and the build tags `make test` uses. Measuring both sides rather than
comparing against the figure recorded for `0.7.0-alpha` is deliberate: a count
taken by a different run on a differently loaded machine is not a baseline, and
masked findings make counts move for reasons that have nothing to do with the
change under test.

| | before (go-quicraft v0.6.2-alpha) | after (v0.8.0-alpha) |
|---|---|---|
| packages passing | 106 | **120** |
| packages failing | 16 | **2** |
| `use of GCM with arbitrary IVs is not allowed in FIPS 140-only mode` | **699** | **0** |
| `use of X25519 is not allowed in FIPS 140-only mode` | 15 | 15 |

**Fourteen packages move red to green, and there are zero regressions** — the
before-failing set is a strict superset of the after-failing set.

The movement is attributable, not merely coincident: every one of the 699
rejections was the AES-GCM-with-caller-IV construction that go-quicraft
v0.7.0-alpha replaced with `cipher.NewGCMWithRandomNonce`, and the count is
exactly zero afterwards. That whole class is closed for this module.

The X25519 count is *unchanged at 15*, which is the correct outcome rather than
a disappointing one: it is a different finding with a different owner. The two
packages still failing — `cmd/qrdbd` and `pkg/clusterenroll` — fail on
`clusterenroll: bootstrap server (noise) failed to start: noise: invalid key
size: crypto/ecdh: use of X25519 is not allowed in FIPS 140-only mode`. That is
go-truststrap's Noise handshake, closable only by compiling its `pkg/noiseproto`
`fips` variant, which this module's FIPS job does not pass `-tags fips` to. The
same tests fail identically before the bump and pass in the non-FIPS run.

Re-measured at the branch head after the last commit touching test code, not
carried forward from the earlier A/B run: **120 pass / 2 fail / 0 GCM / 15
X25519**, the same figures and the same failing pair. The re-run was not
ceremony — tests added after the A/B briefly introduced `cipher.NewGCM`
fixtures that broke `pkg/seal` under FIPS and invalidated these counts until the
hosted review caught it. A measurement describes the tree it was taken on.

**Two tests now skip under `fips140=only`, and neither is a masked failure.**
`TestWrappedRootKey_CallerNonceProvider` and
`TestWrappedRootKey_RejectsMismatchedProviderNonceWidth` construct a
caller-nonce AEAD to exercise the caller-nonce branch of
`sealWrapped`/`openWrapped`. A caller-nonce AEAD is one built with
`cipher.NewGCM` — exactly what this enforcement level refuses — so that branch
is provably unreachable in a FIPS deployment: no provider, software or hardware,
has an approved way to return such an AEAD. Both run in full outside FIPS. The
skip is keyed on `fips140.Enabled()` rather than a build tag because the FIPS
job passes no extra tags, and it is a skip rather than a stub AEAD because the
assertions that matter need real AES-GCM on both sides. Registered in
`docs/seal/fips140-mode.md` under "Skipped tests" with the same reasoning, so a
future count does not read them as hidden red. (Five tests skip on that run in
total; three predate this change.)

Nothing here claims this module operates under `fips140=only`; two packages
still fail and the job stays out of `ci-gate` while they do.

### Not affected

- Every other AEAD site in this repository — `pkg/crypto/aesgcm`,
  `pkg/dek/crypto.go`, `pkg/ceremony/external_officer.go`,
  `pkg/custodian/share_envelope.go` — already constructs
  `cipher.NewGCMWithRandomNonce` from the standard library directly and passes a
  nil nonce. None of them goes through go-quicraft's provider.
- go-quicraft v0.8.0-alpha's `MemoryEnclave` borrow discipline (`WithBytes`,
  `WithValue`, mutators draining borrows, no callback re-entry) reaches nothing
  here. This module's entire use of `pkg/enclave` is `enclave.WipeBytes`, a free
  function over ordinary slices, and its Shamir use is entirely the string
  `Share` API — no `ProtectedShare`, `SplitProtected` or `CombineProtected`.
  `pkg/secure`'s `enclave` field is this repository's own `memguard.Enclave`
  wrapper, unrelated to go-quicraft's type of the same name.

## [0.8.0-alpha] - 2026-08-19

MINOR, not PATCH: the `kvstore.KVStore` index contract changes (a store that
cannot index MUST now return `ErrIndexUnsupported`; `(nil, nil)` is
forbidden), `EntityIndex` gains fields, and `seal.NewTenantSharedKVStore`
returns a type that no longer always satisfies `AtomicKVStore`. Pre-1.0, MINOR
is where added API and breakage both live.

### Fixed

- **`QueryIndex` no longer lies on `StoreResolver`-returned stores** (#31, #32).
  Every `kvstore.KVStore` a consumer could reach implemented `QueryIndex` as
  `return nil, nil`, and `pkg/dao`'s query planner built its index catalog from
  the entity's struct tags by reflection rather than from what the store could
  serve. A field tagged `index:"true"` therefore routed to `QueryIndex` and came
  back with **zero results and a nil error**, while the same field WITHOUT the
  tag fell back to a full scan and was correct — so adding the index is what
  broke the query, and it failed toward "nothing found" with nothing logged.
  - `lifecycle.kvStoreAdapter` — the store the system tenant resolves to — now
    maintains **real** secondary indexes by routing registration and lookups to
    the node's `IndexServicer`, which reaches the same `KVEngine` that applies
    its writes. Index entries are maintained inside the Raft-applied batch,
    backfilled on registration, and replicated.
  - Stores that genuinely cannot index (`seal.tenantSharedKVStore`,
    `seal.tenantBarrierKVStore`, `embedded.serverKVAdapter`, the `cmd/qrdb`
    and `cmd/qrdbd` KV service adapters when built without a registerer or
    lookup, and any adapter built without an index subsystem) now return the
    new typed `kvstore.ErrIndexUnsupported` instead of an empty result. So do
    the definitions a store cannot serve even when it can serve others: a
    COMPOUND definition on any adapter whose compound query methods refuse, a
    case-insensitive one on the engine-backed paths (#34), and a
    database-qualified name the engine will not maintain (#38). Registration
    success is a capability statement, so it may not be given for something
    the store could never answer.
  - `pkg/dao` probes the capability at construction, **per index kind**. A
    refusing store yields an empty planner catalog for that kind, so tagged
    fields are answered by **full scan and are correct**; a direct
    `QueryByIndex`/`ScanByIndex`/compound call fails closed with the typed
    error. Single-field and compound definitions are registered in separate
    calls because a store can serve one and not the other — the lifecycle
    adapter has real single-field indexes through the engine and no compound
    verb at all — so a compound refusal no longer costs a working single-field
    index, and a compound query over a store without compound support falls
    back to a scan rather than erroring. `dao.New` no longer discards the
    result of `RegisterEntityIndexes`: any non-capability error now fails
    construction.
  - **`WithDatabase` DAOs registered an index name they never queried.**
    `pkg/dao` resolves a database-scoped index as
    `{database}.{entityType}.{field}`, but every store composed the
    unqualified `{entityType}.{field}` itself, so such a DAO advertised an
    index, planned a lookup, and queried a name nobody created. `EntityIndex`
    gains an `IndexName` field carrying the caller's resolved name, which
    every store now uses verbatim via `EntityIndex.ResolvedName` — except the
  engine-backed `lifecycle.kvStoreAdapter`, which REFUSES a qualified name
  (tracked as #38). `KVEngine.filterIndexes` decides which indexes a write
  maintains by matching the name against the key's entity type, and the
  adapter cannot see the engine's database setting — so a qualified name would
  be created, never updated, and answer every query empty with a nil error.
  Refusing makes a `WithDatabase` DAO over a lifecycle store full-scan, which
  is correct.
  - The SDK transport adapter composed `entityType + "." + ei.FieldName` for a
    COMPOUND definition, where `FieldName` is empty — creating a garbage index
    named `{entityType}.` while reporting success. It now refuses compound
    definitions.
- **`seal.tenantSharedKVStore` no longer advertises atomicity it may not
  have** (#30). `kvstore.AtomicKVStore` is optional and discovered by type
  assertion precisely so a caller can fail closed at CONSTRUCTION, but a
  single wrapper type that always defined `PutIfAbsent` made that assertion
  succeed unconditionally — a guard that always passes is not a guard. The
  wrapper is now two types, and only the one over an atomic shared store
  carries the method.
- **`seal.tenantSharedKVStore.PutIfAbsent` no longer degrades silently** (#30).
  When the backing shared store was not an `AtomicKVStore` it fell back to
  `Exists`-then-`Put` — the exact TOCTOU the capability exists to eliminate,
  delivered while reporting success, so two nodes could both win a
  create-if-absent race. It now returns `kvstore.ErrAtomicWriteUnsupported`.
- **The system tenant has an atomic path again** (#30).
  `lifecycle.kvStoreAdapter` had no `PutIfAbsent` at all, so
  `Resolve(ctx, SystemTenantID)` returned a store with no atomic write even
  though the engine beneath it has a native compare-and-set. It now implements
  `kvstore.AtomicKVStore` by delegating to `service.KVServicer.PutIfAbsent`.

### Added

- **`kvstore.MemoryKVStore` / `sdk.NewMemoryKVStore`** (#29) — a supported
  in-process `KVStore` with genuine single-process atomicity and **real** index
  maintenance on the write path. Consumers can unit-test DAO code without
  booting a node and without hand-writing a twelve-method index-aware fake per
  repository. Unique indexes are enforced on the write path
  (`*index.UniqueViolationError`), so a test store never accepts a duplicate
  the real engine would reject; `CaseSensitive` and `Sparse` are honoured
  rather than accepted and ignored (a case-insensitive index that never
  normalized would answer a differently cased probe with an empty result, and
  a unique one would admit both `A` and `a`); and a registration whose
  backfill fails publishes no index at all rather than leaving a half-filled
  one visible. A value it cannot DECODE under an indexed key prefix is now
  refused (`UnindexableValueError`) rather than stored unindexed: storing it
  reported successful index maintenance that did not happen, leaving the
  record invisible to every index query and outside every unique constraint,
  both with nil errors — reachable simply by configuring a DAO with the
  MsgPack or CBOR codec this change re-exports. Index READS hold the store's
  read lock for the whole lookup rather than just long enough to fetch the
  index pointer: a `Put` updates an index in several steps, and a reader that
  released the lock early could observe the key under neither the old term nor
  the new one and report "no matches" with a nil error. The document `null`
  is rejected too: it unmarshals into a nil map WITHOUT error, so it passed as
  a decodable object and then produced no index entries — the record existed
  under an indexed prefix and was invisible to every index query. An empty
  object `{}` remains acceptable, being a real object that simply carries no
  value for the field.
- **`sdk.AtomicKVStore`** (#28) — SDK consumers can now make the fail-closed
  capability assertion at construction without importing `pkg/kvstore`, which
  `docs/00` L3 forbids.
- **`sdk.ErrKVIndexUnsupported`, `sdk.ErrKVAtomicWriteUnsupported`** (#28) — the
  typed refusals, reachable with `errors.Is` from a consumer.
- **The missing DAO surface at `sdk/go`** (#28): `DAOWithShard`, `DAOWithCodec`,
  `DAOWithTimeout`, `DAOWithPartition`, `DAOWithDatabase`; `DAOCodecType` with
  `DAOCodecJSON`/`DAOCodecMsgPack`/`DAOCodecCBOR`; `DAOSortDirection` with
  `DAOSortAscending`/`DAOSortDescending`; and `DAOUnitOfWork`,
  `DAOTransactional`, `DAOVersioned`, `DAOTimestamped`. They carry the `DAO`
  prefix because the SDK root already has `ClientOption` namesakes —
  `sdk.WithTimeout` is a `ClientOption`, so an unprefixed re-export would fail
  at every call site with a type mismatch.

### Changed

- `kvstore.KVStore`'s index-method contract is explicit: a store that cannot
  index MUST return `ErrIndexUnsupported`; `(nil, nil)` is forbidden because it
  is indistinguishable from a maintained index with no matches. A store that
  cannot serve a definition MUST refuse it at registration rather than skipping
  it and returning nil — skipping tells the caller an index exists.
- `RegisterEntityIndexes` is documented as ACCUMULATING per entity type rather
  than replacing, which `pkg/dao`'s split single/compound registration relies
  on.
- `kvstore.EntityIndex` gains `IndexName` and the `ResolvedName` helper.
- The five `TestKVStoreAdapter_*_NoOp` and five `TestTenantBarrierKVStore_*_NoOp`
  tests asserted the `(nil, nil)` behaviour and passed, which is why the defect
  survived — a green test declared it the intended contract. They now assert the
  typed refusal, or are replaced by tests that assert engine-backed answers.

### Known limitations

- **`index:"case_insensitive"` is refused on the engine-backed and transport
  paths** (tracked as #34). `protocol.EncodeIndexCreate` carries no
  case-sensitivity flag, so `KVEngine.handleIndexCreate` builds every index
  case-sensitive; accepting the definition would create a case-SENSITIVE index
  while reporting success, and a lookup for `"Alpha"` over a stored `"alpha"`
  would return empty with a nil error. `lifecycle.kvStoreAdapter` and the SDK
  transport adapter therefore refuse it, so a DAO plans a correct full scan.
  Carrying the flag changes a Raft command format that existing logs replay
  through, which is its own change. Only an explicit `case_insensitive` tag is
  affected: `index.ParseIndexTags` defaults to case-sensitive, so ordinary
  `index:"true"` and `index:"unique"` fields keep their real engine index.
- **The SDK transport adapter refuses an OPEN range bound** (tracked as #36).
  `transport.IndexClient.IndexScan` takes plain strings, so there is no way to
  express "unbounded": a nil bound arrived as `""`, which every index reads as
  an explicit empty bound and REST rejects as a missing parameter. Since the
  planner leaves one side nil for every one-sided range, `Team >= "beta"`
  became `["beta", "")` and returned empty with a nil error. `ScanIndex` now
  refuses a nil bound so the caller sees the incapability rather than a wrong
  answer.
- **Numerically ordered fields are no longer advertised to the query
  planner** (#35 restores them properly). Index terms are decimal text
  compared lexicographically, so `"10"` sorts before `"2"` and `Age >= 2`
  returned an INCOMPLETE result — worse than empty, because a partial answer
  looks like a real one. This change is what makes those index paths live, so
  leaving the ordering enabled would have converted a uniformly-empty answer
  into a silently-partial one.
  `buildCatalog` therefore omits numeric single-field indexes and compound
  indexes with a numeric member, and the planner chooses a correct full scan.
  The cost is the sound numeric EQUALITY lookup losing its index — slower, not
  wrong — and the direct `QueryByIndex` path is unaffected. The surgical fix
  (the planner refusing only RANGE plans on such fields, using the `FieldType`
  it already carries) changes `pkg/planner`'s documented contract, which is an
  owner decision; #35 carries it with the analysis.
- **Barrier-encrypted tenant stores still cannot index.**
  `seal.tenantSharedKVStore` and `seal.tenantBarrierKVStore` hold only
  ciphertext, so nothing beneath them can read a field value. Indexing above the
  barrier would put the indexed value into an index KEY in the shared store, in
  the clear, defeating the isolation those types exist to provide; a
  keyed/deterministic index term preserves it for exact match but destroys the
  ordering range scans need. That is a cryptographic design decision and is not
  made here. Until it is ruled on these stores refuse, and a DAO over them
  answers correctly by full scan.

## [0.7.0-alpha] - 2026-08-19

MINOR rather than PATCH: no qrdb API moves here, but `go-truststrap` advances a
MINOR of its own and reaches consumers through this module's requirement graph,
so anything composing both takes the change transitively.

### Changed

- **`go-truststrap` v0.1.4-alpha to v0.2.0-alpha.** It splits `pkg/noiseproto`
  on a `fips` build tag — `dh.go` (`//go:build !fips`) performs X25519,
  `dh_fips.go` (`//go:build fips`) refuses with `ErrFIPSUnsupported`. That tag
  is the mechanism by which the Noise finding below gets closed. It is not
  closed here: this module's FIPS job does not pass `-tags fips`, so it still
  compiles the `!fips` variant.

### Fixed

- **`make test` sets an explicit `-timeout=30m`.** It previously set none and
  inherited `go test`'s 10m per-package default, which `pkg/seal` had been
  living inside by a margin of ten seconds: `589.797s` on the last passing run
  against a 600s budget — 98.3% — and then `600.442s` on the next one, which
  failed. Nothing about the package changed between those two runs; it is slow
  by design (Argon2id under `-race`) and the budget was always too small for it.
  The failure shape is the reason this mattered more than the delay: the timeout
  kills the whole test binary and names whichever test was starting when the
  alarm fired, so an exhausted budget reads as a hang in an unrelated package.
  Bounded rather than removed, so a genuine deadlock still fails; the CI job
  already allows 90 minutes and `test/integration` keeps its own `-timeout=25m`,
  which still applies because a later `-timeout` wins.

### FIPS 140-3 posture

Measured on this commit — Go 1.26.6, `GOFIPS140=latest GODEBUG=fips140=only`,
`-race`, and the same build tags `make test` uses:

    122 packages: 106 pass, 16 fail
    693 GCM rejections across 15 packages
     23 X25519 rejections across 3 packages

**This bump moves no package from red to green.** The pass/fail split and the
GCM count are identical to the pre-bump measurement recorded in `ci.yml`, and
the count is stated here so the next person does not have to re-derive it to
find that out. Both remaining findings originate outside this module:

- **GCM with caller-supplied IVs** — every rejection originates in
  `go-quicraft@v0.6.2-alpha/pkg/seal` (`algorithm.go`, `strategy_software.go`).
  qrdb's own five GCM construction sites already use
  `cipher.NewGCMWithRandomNonce`, so there is no qrdb-side residue. Fixed
  upstream in `go-quicraft@v0.7.0-alpha`; taking that bump is gated on the
  wrapped-root-key dual-read migration (go-qrdb#23), because the new
  construction reports `NonceSize() == 0` and an existing sealed store would
  stop unsealing.
- **X25519 in the Noise handshake** — `pkg/clusterenroll` (7), `pkg/ceremony`
  (8) and `cmd/qrdbd` (8). Noise_NK has no approved DH to move to, so this
  never closes by moving to a newer version; it closes by Noise being absent
  from a FIPS build. `pkg/ceremony` is newly counted here: it previously failed
  on GCM before reaching the Noise path.

Neither finding is closable by editing the FIPS job, and it stays out of
`ci-gate` while they stand. Nothing in this repository claims the module
operates under `fips140=only`.

## [0.6.0-alpha] - 2026-08-18

MINOR, not PATCH: pre-1.0 this is where breakage lives, and this release moves
the derivation of persisted role and custodian identifiers, tightens the
multicast shared-secret contract, and removes `statemachine.CachedDiskBackend`.

Two housekeeping defects are closed here rather than left to rot. `VERSION` had
read `0.5.7-alpha` through both the `v0.5.8-alpha` and `v0.5.9-alpha` tags — the
unreconciled-sources-of-truth failure `release-versioning.md` §2 warns about —
and neither of those tags got a CHANGELOG section, so their content was still
sitting under `[Unreleased]`. It is folded into this entry. `v0.5.9-alpha` in
particular carried the `CachedDiskBackend` removal, a breaking change published
as a PATCH; consumers that pinned back to `v0.5.8-alpha` over it should move
forward to this release.

**Consumers on `v0.5.9-alpha` or earlier should upgrade.** The UUIDv8 migration
below is what stops `pkg/role` panicking in package init under
`GODEBUG=fips140=only`; it landed after `v0.5.9-alpha` was cut, so every tagged
release before this one still takes down any downstream binary that authorizes
through `pkg/role` in a FIPS build.

### Added

- **Protected resolution targets for `ConfigProcessor` (`pkg/secret`).** A
  config field declared as `secret.SecretValue` (or `*secret.SecretValue`)
  instead of `string` is now resolved in place inside its own memguard
  enclave, so a resolved secret reaches the consumer without ever resting in
  an unwipeable Go string. `SecretValue` gained YAML/JSON codecs that decode
  from — and re-encode to — the plain reference string, so config documents
  are unchanged and a re-serialized config emits the reference rather than
  the plaintext (a non-scalar node — and JSON `null`, which is a configuration
  mistake rather than an empty secret — fails with the new
  `SecretValueDecodeError`). `Raw` is left intact by the walk.

  The protected target is deliberately stricter than a plain `string` field:
  every non-nil target the walk reaches is either resolved or reported in
  `Unresolved`, never skipped in silence. A target holding nothing (a null or
  absent key), an inline `literal://` secret, a bare value, or a scheme no
  resolver claims is refused with the new `UnresolvableTargetError` — an
  inline secret carries the plaintext in `Raw` itself, an ordinary Go string
  that outlives the walk, so sealing it would leave a second unprotected copy
  and break the guarantee the field type advertises. A target the walk cannot
  write to (boxed by value in an unsettable interface) is refused with the new
  `UnwritableTargetError`; boxed targets it can write to are resolved through
  an addressable copy and assigned back. An optional secret is spelled as a
  `*SecretValue`, where an absent or null key leaves the pointer nil — the one
  case the walk passes over. That is also what closes the YAML null gap:
  `yaml.v3` zeroes a null destination before the unmarshaler is consulted, so
  `password:` and `password: null` never reach `UnmarshalYAML` and are refused
  at the walk instead of at decode time, where JSON rejects its `null` token.

  A protected-target failure fails the call: `Process` returns
  `ErrUnresolvedSecretURI` whenever the new `ProcessResult.ProtectedUnresolved`
  is non-empty, with or without `WithFailClosed()`, because choosing the type
  is itself the request to fail closed on that field. `Process` also destroys a
  target's existing enclave before refilling it, so a failed refresh over an
  already-resolved config leaves the field empty rather than letting a revoked
  credential keep authenticating while its backend is down.

  Additive throughout: `Process`'s signature is unchanged, the deprecated
  string path keeps its exact behaviour — including that its failures stay
  opt-in diagnostics under `WithFailClosed()` — and no existing consumer
  breaks on upgrade.
- **Redacted display paths for `SecretValue` (`pkg/secret`).** `SecretValue`
  now implements `fmt.Formatter` and `slog.LogValuer`, which `fmt` and
  `log/slog` consult ahead of `fmt.Stringer`. `%v`, `%s`, `%+v`, `fmt.Sprint`
  and structured logging of a config struct holding a resolved secret therefore
  render the redacted description instead of the plaintext. New `Redacted()`
  exposes the same description directly: it shows the reference only when a
  resolver established that the reference points at a secret rather than
  carrying one, judging by provenance rather than syntax — the registry falls
  back to `StrategyLiteral` for anything no resolver claims, so a
  reference-shaped `https://user:pw@host` or `unknown://pw` is the secret in
  fact and is redacted, as is any value not yet resolved. Only an explicit
  `String()`/`Bytes()` call still materializes plaintext, and only
  serialization still emits `Raw` verbatim — reproducing the configuration
  document is what serializing it means.
- **`WithNoPlaintextStrings()` migration ratchet (`pkg/secret`).** With this
  processor option set, a plain `string` field holding a reference is left
  untouched — its secret is never fetched — and its path is reported in the
  new `ProcessResult.PlaintextSkipped` (always a subset of `Unresolved`). A
  consumer turns it on, converts the reported fields to `SecretValue` one at
  a time, and adds `WithFailClosed()` once the list is empty to refuse any
  boot that would produce a plaintext string. Both re-exported from
  `sdk/go/secret`.
- **Raft state machines over any storage backend (run-modes phase 5).**
  New `raft.sm_storage` (`--sm-storage`) selects the storage backend the
  storage-generic state machines (`sm_type: disk`/`fast`) compose:
  `pebble` (default), `file`, or `memory`, rooted per-(shard, replica)
  under the data dir. Objstore is refused as the hot apply path with a
  pointed error — it is a snapshot/backup tier. The pebble-native SMs
  (`pebble`, `mvcc`) reject a non-pebble selection.
- **File storage backend durability.** `pkg/storage/file` writes are now
  crash-safe: Set is temp-file + fsync + atomic rename + directory fsync;
  Delete fsyncs the parent; batch commits are all-or-nothing via a
  CRC-verified commit journal published before any per-key file is
  touched and replayed at open (a corrupt journal fails the open rather
  than serving a half-applied batch). New optional `storage.Syncer` seam.
  Acceptance: raft single-node survives a hard kill + restart over
  `--sm disk --sm-storage file`, and a 3-node cluster replicates over
  file-backed SM storage (test/integration/sm_storage_file_test.go).

- **Run-modes requirements ratified** ([docs/run-modes.md](docs/run-modes.md)):
  standalone = no Raft (standard database operations + always-on security
  core over any non-raft storage backend); raft (single- or multi-node) =
  MVCC/watch/lease/lock/transactions/etcd/cluster ops. Feature availability
  follows a three-gate model (build tag > run mode > config) with an
  explicit always-compiled CORE. The daemon's `mode: standalone` and the
  capabilities upgrade land in subsequent phases of the approved plan.

- **Feature profile system (run-modes phase 2).** `pkg/capabilities` now
  seals an effective per-deployment profile at boot
  (`capabilities.Seal(cfg.EffectiveCapabilities())`): the intersection of
  build tags, run mode, and config. `Capabilities` gains `mode` plus
  `lock`/`cluster`/`backup`/`rebalance`; the proto, REST/gRPC/QUIC/MCP/
  embedded transports, SDK DTO, and `qrdb admin capabilities` all carry
  the new fields (additive wire shapes). Before a composition root seals,
  `Get` keeps the historical compile-time fallback.
- **`mode` config key** (`"standalone"`/`"raft"`, empty = raft) with
  validation: under standalone, explicitly enabling `mvcc`/`watch`/
  `lease`, `raft.join`, or a multi-member `raft.peers` list fails with
  `requires mode: raft`; standalone skips the raft identity requirements
  (only `raft.data_dir` stays mandatory). New optional `enabled` toggles
  on `mvcc`/`watch`/`lease` narrow the raft profile.
- **Boot-time build gate** `Config.ValidateCapabilities()`: a wire
  protocol enabled in config but not compiled in is refused with an error
  naming the build tag. (Between phases 2 and 3 the daemon refused
  `mode: standalone` with a pointed error; phase 3 replaced the refusal
  with the real composition branch.)
- **Typed refusal hints.** REST `ServiceNotImplementedError` and the
  gRPC/QUIC/service `ServiceNotConfiguredError` gained an optional `Hint`
  appended to the message (e.g. `requires mode: raft`).

- **`mode: standalone` in the daemon (run-modes phase 3).** `qrdbd` now
  composes the raft-free data plane (`lifecycle.NewLocalStorage`/
  `NewLocalEngine` → `backend.LocalBackend`) when `mode: standalone` (or
  `--mode standalone`) is set: no raft host, no WAL, no election — boot
  drops from seconds to milliseconds. Standard database operations plus
  the always-on security core (mTLS/RBAC/IAM, seal barrier incl. encfs,
  audit) serve as in raft mode; raft-only endpoint families (MVCC
  revisions, watch, lease, lock, etcd-style Txn, cluster/backup/
  rebalance, encryption migration) are refused on every transport with
  501/Unimplemented naming the remedy `requires mode: raft`. The SQL
  catalog keeps its internal MVCC/transaction plumbing (standard ops).
- **One canonical local composition.** The three duplicate
  storage/engine composition sites (SDK node, embedded transport, and
  now the daemon) collapse into `lifecycle.NewLocalStorage`/
  `NewLocalEngine`; the embedded `database/sql` driver gains objstore
  support as a side effect.
- **SDK standalone gating (behavior change, as announced below).**
  `NewStandaloneNode` no longer exposes the MVCC/watch/lease/transaction/
  lock service families; standalone nodes report `mode: standalone` in
  capabilities with those features false. `NewClusteredNode` is
  unchanged. `service.AdminService` gains `WithCapabilityProfile` /
  `lifecycle.Infra.Profile` so each node in a multi-node process reports
  its own truth.
- REST's `capabilitiesResponse` now carries the full profile (a phase-2
  funnel gap), and REST maps `ServiceNotImplementedError` to 501 from
  registry-resolved paths (previously 500).

- **Per-mode exemplars and integration suites (run-modes phase 4).**
  `configs/qrdb-standalone-{pebble,memory,file}.yaml` exemplars;
  `configs/qrdb-standalone-mvcc.yaml` renamed to
  `qrdb-raft-single-mvcc.yaml` (it always was a single-node RAFT config —
  the old name contradicted the ratified terminology).
  `TestStandaloneMode_StandardOpsAndRefusals_PerProtocol` drives a
  `--mode standalone` daemon over all five transports: standard ops
  green, capability truthfulness, raft-only refusals; plus per-backend
  boot coverage. The unix SDK transport's capabilities mapping gained the
  new profile fields (the last funnel site). Downstream notified:
  go-quicbaas#4.

- **Release tag lockstep check.** `make check-tag-lockstep`
  (`scripts/check-tag-lockstep.sh`) checks release-versioning.md P2: every
  root `vX.Y.Z` tag must have an `sdk/go/vX.Y.Z` tag at the same commit, and
  no `sdk/go` tag may exist without its root. The new `Release Lockstep`
  workflow runs it on version-tag pushes in both namespaces (`v*` and
  `sdk/go/v*`) — within a minute of publication, rather than days later in a
  consumer's dependency bump — and re-audits the whole tag set on every merge
  to `main`. It detects a skewed release; it
  cannot prevent one, because a tag is visible the moment it is pushed
  (go-qrdb#13 tracks enforcement). The preventive step is running the check
  before publishing both tags in one atomic push
  (`git push --atomic origin vX.Y.Z sdk/go/vX.Y.Z`). `make test-tag-lockstep` covers
  the rejection branches against throwaway repositories, since this
  repository's own tag set is permanently valid and exercises only the
  accepting branch. `v0.5.9-alpha` was tagged on the root module alone, so
  `go get` at latest resolved the root to `v0.5.9-alpha` and `sdk/go` to
  `v0.5.8-alpha` — a pair that does not compile, because the older `sdk/go`
  still aliased the symbols removed below. The tag has since been cut.
  Reported by go-xkms (#6).

### Deprecated

- **Plaintext-into-`string` secret resolution (`pkg/secret`).** Resolving a
  secret into a plain `string` config field is the plain path the stack is
  moving off; use a `SecretValue` field target, and
  `WithNoPlaintextStrings()` to find the fields that still need converting.
  `SecretValue.String()` and `SecretValue.Bytes()` are likewise deprecated in
  favour of `WithBytes`/`Open` (and `Redacted()` for anything displayed).
  Nothing is removed and the deliberate accessors keep their behaviour: the
  exported signatures stay as they are until the stack's coordinated breaking
  release, so the whole dependency chain can move in one wave rather than repo
  by repo. `String()` still returns plaintext because it is the documented way
  to hand a secret to an API that takes a string, and silently redacting it
  would break those callers at run time with no compile error; the accidental
  paths (`%v`, `fmt.Sprint`, structured logging) are redacted now, as noted
  above, so what remains deferred is only the deliberate call.

### Removed

- **`statemachine.CachedDiskBackend` and `NewCachedDiskBackend`**, with
  their `sdk/go` aliases — a breaking change to an exported API, shipped
  in `v0.5.9-alpha`. Both were a reserved placeholder: a methodless struct
  no registry ever provided, so the `cached-disk` SM backend passed config
  validation and then failed at boot. There is no replacement and nothing
  to migrate — no build that compiled against these symbols was running a
  cached disk backend. The disk-backed path is `sm_type: disk`, now
  composable over any storage backend via `raft.sm_storage`.

### Fixed — SECURITY

- **`GOFIPS140=off` was read as a FIPS request.** SO PIN hashing in
  `pkg/auth` picked its KDF with `os.Getenv("GOFIPS140") != ""`, so the one
  value that explicitly means "not FIPS" selected PBKDF2-SHA256, and
  `crypto/fips140.Enabled()` was never consulted at all. Every
  FIPS-conditional path in the tree now routes through a single predicate,
  `seal.FIPSEnabled` (re-exported from `go-quicraft/pkg/seal`): the Go
  runtime's FIPS 140-3 module state first, `GOFIPS140` as an environmental
  fallback, and `off` honoured as disabled. `pkg/seal/kdf.DefaultHasher`
  loses the same env-only test, and `pkg/pin.AutoDetectHashConfig` — which
  was blind to the environment entirely — now honours a `GOFIPS140` FIPS
  request. Stored hashes are unaffected: each record carries its own
  algorithm/version tag and verification still routes by that tag, so a
  hash written in either mode continues to verify in the other.

- **A resolver reporting success with no value is now refused
  (`pkg/secret`).** `SecretResolver` requires a non-nil `SecretValue` on
  success, but `ResolverRegistry.Resolve` did not enforce it: a resolver
  returning `(nil, nil)` panicked the plain string path on the nil
  dereference, and would have let a protected target be counted as resolved
  while holding nothing. The registry now fails such a resolver with
  `ResolutionFailedError`, so the path is recorded in `Unresolved` and
  `WithFailClosed()` refuses the boot.
- **`ConfigProcessor` no longer destroys a `SecretValue` config field.** The
  walker descended into a `SecretValue`, saw its exported `Raw` reference as
  an ordinary resolvable string field, and overwrote it with the resolved
  plaintext while leaving the enclave empty — making the documented "hold the
  secret in a `SecretValue`" pattern strictly worse than a plain `string`
  field. A `SecretValue` is now a protected leaf the walk resolves into and
  never descends through.

### Changed — protected memory

- **The live cluster DEK is held in mlocked memory, and never copied out.**
  `dek.ClusterDEKManager` kept the plaintext cluster DEK in an ordinary
  `[]byte` for the manager's whole lifetime. It is now a `*secure.Buffer`
  (mlocked, guard-paged, wiped on destroy): the key is generated straight
  into protected memory rather than being copied there, and the superseded
  buffer is destroyed on `Rotate` and on `Close`.

  **BREAKING (service seam):** `ClusterDEKManager.GetCurrentDEK() ([]byte,
  error)` is gone, and no exported API replaces it: the key is now applied
  inside the manager by `WrapKey(plaintext []byte) ([]byte, error)` and
  `UnwrapKey(wrapped []byte) ([]byte, error)`, which perform the AES-256-GCM
  key wrapping under the manager's state lock. Neither a heap-backed copy nor
  a slice aliasing protected memory crosses the API boundary, so no caller
  can retain the key past a call or mutate the live key.
  `ClusterDEKManager.CurrentVersion()` is the checked
  counterpart of `GetDEKVersion` for callers that must distinguish
  "version 0" from "no DEK". Correspondingly,
  `service.DEKServicer.GetCurrentDEK(ctx) ([]byte, uint64, error)` becomes
  `CurrentDEKVersion(ctx) (uint64, error)`: the DEK bytes never crossed that
  boundary usefully — the embedded transport already discarded them, and no
  transport exposes DEK material.

### Changed — FIPS 140-3 identifiers

- **Deterministic role and custodian identifiers moved from UUIDv5 (SHA-1) to
  RFC 9562 §5.8 UUIDv8 over SHA-256.** `crypto/sha1` panics under
  `GODEBUG=fips140=only`, and both derivations run during package
  initialization and tenant seeding, so before this change a FIPS build died
  before a single test body ran — including in any downstream binary that
  authorizes through `pkg/role`. UUIDv8 is the version reserved for
  application-defined derivations, and it is the only one available: RFC 4122
  fixes v3 to MD5 and v5 to SHA-1, so no versioned name-based UUID can carry an
  approved hash. Inputs are length-prefixed before hashing so the encoding is
  injective — unframed, tenant `a/b` with role `c` and tenant `a` with role
  `b/c` would collide on one identifier. The 36-character UUID text shape is
  unchanged, so no index key or wire format reformats.

  **These identifiers are persisted data**, so the change is versioned rather
  than a flag day: writes emit only the v8 form and reads accept both. A
  cluster seeded by an older build keeps its v5 identifiers, and code holding a
  tenant plus a role NAME must not re-derive — on such a cluster the v8 value
  names no record at all. Use `role.ResolveSystemRoleID`, which answers from
  the store's name index and falls back to the canonical value, and
  `role.SystemRoleIDs`, which returns every identifier denoting the role so
  that the SO deny-list and the separation-of-duties Owner strip still match a
  grant made before the migration. Writing a canonical identifier straight into
  a legacy cluster's user records produces a grant that resolves to no role and
  silently strips the user's permissions.

### Fixed — release integrity

- **Every previously published tag recorded an unresolvable requirement for its
  own SDK submodule.** The root `go.mod` recorded
  `github.com/jeremyhahn/go-qrdb/sdk/go v0.0.0-00010101000000-000000000000`, a
  placeholder that resolves to nothing. A `replace` directive only applies in
  the main module, so the in-repo `=> ./sdk/go` masked it for every check run
  inside this repository — local builds, CI, `go list`, module-zip inspection —
  while a downstream consumer got:

  ```
  github.com/jeremyhahn/go-qrdb/sdk/go@v0.0.0-00010101000000-000000000000:
      invalid version: unknown revision 000000000000
  ```

  `pkg/ceremony`, `pkg/api/rest` and `cmd/qrdb` import `sdk/go/transport`, so
  the failure is reachable rather than theoretical: a scratch module that does
  `go get github.com/jeremyhahn/go-qrdb@v0.5.9-alpha` and imports
  `pkg/ceremony` cannot even `go mod tidy`. Reproduced with `GOWORK=off`.

  **Why nobody hit it.** Every current consumer — go-xkms, go-quicbaas,
  go-trusted-platform — carries its own explicit
  `require github.com/jeremyhahn/go-qrdb/sdk/go` line at a real version,
  because each also imports `sdk/go/transport` directly. Their own requirement
  satisfies the import, and the root module's nonsense version is never
  consulted. The defect therefore bites a NEW consumer that reaches a root
  package without independently depending on the SDK, which is precisely what
  the mandated throwaway-consumer check constructs. Latent, not dormant.

  The reciprocal pin was stale in the same way: `sdk/go` required the root at
  `v0.4.0-alpha`, five minor versions behind.

  Both cross-module requirements now name the release they ship in
  (`v0.6.0-alpha`), which the lockstep tagging rule guarantees exists — the
  root and `sdk/go` tags are pushed atomically. The `replace` directives stay,
  because in-repo development must still build against the working tree rather
  than a published copy of its own code.

  This is the defect `release-versioning.md` §2 mandates a throwaway-consumer
  check for, and the reason that check is mandatory: no test that runs inside
  the repository can see it. It is also the same failure that condemned
  `v0.2.0-alpha`, which means it regressed and went unnoticed for many
  releases.

### Fixed — tests

- **`TestDefaultHasher_NonFIPSMode` pinned the toolchain instead of the
  selector.** It cleared `GOFIPS140` and asserted the literal Argon2id version,
  but `seal.FIPSEnabled` consults `crypto/fips140.Enabled` before falling back
  to the variable, and `GOFIPS140` selects the module at BUILD time — `t.Setenv`
  cannot retract at run time what the linker already decided. The expectation
  now routes through the `wantVersion` helper the package's other FIPS-selection
  tests already use. No coverage is dropped and nothing is skipped: the test
  asserts Argon2id on an ordinary run and PBKDF2-SHA256 under the FIPS job,
  which is the correct answer for a FIPS binary because Argon2id is not an
  approved KDF. This was the last go-qrdb-side failure under
  `GODEBUG=fips140=only`; the remainder are upstream (see
  `docs/seal/fips140-mode.md`).

## [0.5.8-alpha] - 2026-08-17

### Added

- **Live ABAC policy reload.** `pkg/auth/abac.Decorator` now supports
  `Reload(policies []*abac.Policy) error`: the new set is compiled and
  validated before an atomic pointer swap, so a failed reload leaves the
  previous policy set fully in force and `Authorize` reads lock-free under
  concurrent reloads. `abac.WithLiveReload` keeps the decorator reloadable
  even when zero policies are configured at construction. `sdk/go/node.Node`
  exposes the seam via `WithABACLiveReload` / `Node.ABACPolicyReloader()`;
  `pkg/auth/wiring.AuthorizerConfig`/`AuthMiddlewareConfig` gained an
  `Authorizer` override field so the embedded in-process transport and every
  network transport's `AuthMiddleware` share one authorizer instance, and a
  reload reaches all of them identically. `sdk/go/types_abac.go` re-exports
  `ABACPolicyReloader` and `ABACWithLiveReload`.

## [0.5.7-alpha] - 2026-08-17

### Fixed — SECURITY-ADJACENT

- **Module-registered public routes serve anonymously.** The REST
  middleware short-circuited only its hardcoded built-in allowlist before
  authentication; a module's `HandlePublic` routes (WebAuthn
  registration/login ceremonies, OIDC discovery) still demanded a peer
  certificate once `client_auth` was enabled, breaking every anonymous
  first-contact ceremony. Public registry routes now short-circuit
  pre-authentication exactly like the built-ins.

## [0.5.6-alpha] - 2026-08-17

### Added

- **Pre-resolved scope permissions** (`auth.Identity.ScopePermissions`):
  a TRUSTED in-process issuance path (a module authentication fallback
  validating a platform token) may carry the token's `scp`-derived
  `resource:action` set on the identity. The RBAC authorizer unions them
  with store-resolved role permissions — additive only, SO deny-list
  still overrides — so client-credential/app tokens whose subject has no
  user record authorize for exactly what the token was issued for. Never
  wire-deserialized; the on-behalf path constructs fresh identities and
  cannot carry them.

## [0.5.5-alpha] - 2026-08-17

### Changed

- **Built-in routes yield to module registrations.** The documented
  "modules register first and take precedence" contract is now formal:
  a built-in route whose method+pattern a capability module already
  claimed is skipped (debug-logged) instead of colliding. Module-vs-
  module duplicates still fail construction loudly.
- qrdbd auth bootstrap no longer opens an unchained `audit.log` side
  file (the decision bridge made it obsolete); a nonexistent data dir no
  longer fails auth bootstrap.

## [0.5.4-alpha] - 2026-08-16

### Added

- SDK audit-record aliases (`qrdbsdk.NodeAuditEvent` / `NodeAuditFilter` /
  `NodeAuditPage`): composing hosts emit into the node's chained audit
  sink through the SDK boundary without importing `pkg/audit`.

## [0.5.3-alpha] - 2026-08-16

### Added

- **Module authentication fallback** (`apimodule.ModuleAuthFallback`, SDK
  `qrdbsdk.ModuleAuthFallback`): a capability module whose HTTP surface
  accepts credentials qrdb's own authenticators do not understand (a
  session-token plane) may authenticate the request when the host chain
  produces no identity. The fallback identity flows through the SAME
  store-resolved enrichment and the ONE authorizer as an mTLS identity.
- **REST route-collision guard**: registering the same method+pattern
  twice (two modules, or a module and the built-in surface) fails
  construction with a descriptive panic instead of silently shadowing.

### Changed — one audit plane

- qrdbd's per-decision auth audit now rides the tamper-evident chained
  event log via the decision bridge — identical wiring to embedded nodes;
  the unchained side file is gone.
- The legacy second audit system (`audit.New`, kv/file/multi backends,
  the `audit:` config block, `DaemonAuditConfig`) is DELETED. The chained
  `AuditLogger` (Emit/Query) is the only audit sink; the `Logger`/`Entry`
  middleware contract remains as the decision-bridge interface.
- Audit wire tags are snake_case (`tenant_id`, `client_ip`,
  `request_id`, `prev_hash`), closing the documented cross-project
  serialization mismatch.

## [0.5.2-alpha] - 2026-08-16

### Changed — SECURITY

- **Authentication is now the zero value.** `AuthConfig.Enabled` became a
  pointer (`*bool`) with `IsEnabled()`: an embedder that builds a
  `DaemonConfig` literal without touching the Auth block gets an
  AUTHENTICATED unified server; only an explicit `false` disables. This
  closes a live regression where both downstream daemons (xkmsd,
  quicbaasd) served every qrdb-native plane unauthenticated because the
  plain-bool zero value silently disabled the middleware. Use
  `config.BoolPtr` / `qrdbsdk.BoolPtr` for explicit values.

### Added

- **Per-decision audit on embedded nodes** (`audit.NewDecisionLogger`):
  the auth middleware's decision trail now bridges onto the node's
  chained compliance event log — authorization decisions land in the
  same tamper-evident chain as every other audited operation, matching
  qrdbd. Closes the documented SDK-node TODO.

## [0.5.1-alpha] - 2026-08-16

### Fixed

- **SDK error exports restored**: `sdk/go/types_errors.go` was accidentally
  truncated in 0.5.0-alpha to only the new `NotLeaderError` re-export,
  dropping `QRDBError`, `ErrorCode`, the error constructors and every
  other error alias. The full surface is restored with `NotLeaderError`
  appended. 0.5.0-alpha should not be depended upon.

## [0.5.0-alpha] - 2026-08-16

### Added

- **ABAC authorization decorator** (`pkg/auth/abac`): attribute-based
  access control layered over the RBAC authorizer, porting the core of
  go-quicbaas's IAM policy engine (YAML policy schema, compiler, resource
  trie, condition parser, evaluator) into its canonical qrdb home. Hybrid
  semantics: explicit deny overrides RBAC allow, explicit allow grants
  beyond RBAC, unmatched requests delegate to RBAC. Policies are
  tenant-scoped; the reserved system tenant's policies apply platform-wide
  as guardrails. Enabled in qrdbd via `auth.abac.{enabled,policy_dir}`
  (fail-closed validation and boot) and in embedded nodes via
  `node.WithABACPolicyDir` / `node.WithABACPolicies`. SDK re-exports in
  `sdk/go/types_abac.go` (`ABACPolicy`, `DecorateAuthorizer`, ...) plus
  `qrdbsdk.DaemonAuthABACConfig`. Policies load once at construction;
  hot-reload is a documented follow-up. See `docs/auth/abac.md`.

- **Embedded-node auth enforcement** (`pkg/auth/wiring`): the SDK node
  (`NewStandaloneNode`/`NewClusteredNode`) now constructs the SAME auth
  middleware qrdbd builds — RBAC authorizer (+ABAC decoration), mTLS/SPKI
  authenticator, adaptive/enforced mode — and passes it to the unified
  multi-protocol server. Previously an embedded node served every
  permission-gated route unauthenticated. `BuildAuthMiddleware` /
  `BuildAuthorizer` are shared by `cmd/qrdbd` and the SDK node (one
  construction path, no divergence).

- **Per-RPC public affordance for module gRPC services**
  (`apimodule.GRPCPublicMethodLister`, re-exported as
  `qrdbsdk.ModuleGRPCPublicMethodLister`): modules may declare exact gRPC
  methods that bypass authentication, symmetric with REST's
  `HandlePublic`. Registered method names are validated at startup
  (typed error on malformed names). Built-in health/reflection exemptions
  now genuinely bypass authentication rather than only the permission
  lookup.

- **Module MCP tool permissions** (`apimodule.MCPToolPermissioner`): the
  unified MCP server consumes per-tool permission declarations from
  registered modules.

### Fixed

- **Sealed store crash durability (data-loss grade)**: the seal store —
  the only persisted copy of the barrier root key and epoch state — was
  opened with `SyncWrites: false` (a rationale valid only for
  Raft-replicated stores). A crash could lose the root-key write; the
  next boot silently minted a fresh root key, permanently orphaning every
  file encrypted under the lost key (`quicraft/seal: decryption failed`
  on the data-plane and raft snapshot stores). All seal/DEK stores now
  force `SyncWrites: true`; SIGKILL-subprocess regression tests prove a
  killed node reboots and serves its data (`pkg/seal/encfs`,
  `sdk/go/node`).

## [0.4.0-alpha] - 2026-08-15

### Added

- **ServiceModule MCP tool authorization** (`apimodule.MCPToolPermissioner`):
  a module that registers MCP tools may now declare each tool's required
  permission; the unified server feeds the declarations into the same MCP
  tool registry the built-in tools use, so module tool calls are authorized
  by the one middleware path instead of being unconditionally denied. A
  module tool registered without a declaration remains fail-closed.
- **SDK re-exports for module hosts**: `qrdbsdk.WithServerTLS` (the
  TLS-only transports' certificate source, required by hosts that terminate
  TLS with their own material), `qrdbsdk.NewModuleHTTPRegistrar` (capture
  registrar for module tests), `qrdbsdk.ModuleMCPToolPermissioner`, and
  `qrdbsdk.PermissionIdentityGated` (the identity-gated sentinel).

### Changed

- `github.com/jeremyhahn/go-quicraft` pinned to `v0.4.2-alpha`
  (`batch.EncodeBatch` returns `([]byte, error)`; the error is propagated
  through `pkg/batch`).

### Fixed

- `pkg/api/mcp` default-tag test builds: the shared `CredentialServicer`
  mock lived in a file constrained to the full feature-tag set while an
  untagged test file used it; the mock now lives untagged so every tag
  combination compiles.

## [0.3.0-alpha] - 2026-08-14

### Added — the unified API server seam

Phases 0-2 and phase 3 wave 1 of
`software/plans/unified-api-server-2026-08-14.md`, the work that lets a host
process expose go-qrdb's own control plane instead of reimplementing it per
transport. New exported API, so MINOR.

- `pkg/apimodule`: `ServiceModule` and `BaseModule` with `Start`/`Stop`, a
  duplicate-name check, and a startup assertion that every registered route
  carries a permission. `WithModule` fans a module out across the transports.
  Re-exported from `sdk/go`. Additive — nothing existing changes shape.
- The control plane's own admin, rebalance and user-auth-factor surfaces are
  served through the seam.
- `pkg/sql/privilege`: GRANT, REVOKE and SHOW GRANTS parsing, rewriting and
  enforcement over `service.PrivilegeService`, plus `pkg/sql/pgcatalog`.

### Changed

- REST runs on chi/v5 v5.3.0. SSE `/watch`, on-behalf-of and the cluster-peer
  exempt routes are unchanged, gated by the pre-existing watch and
  barrier-authz tests passing unmodified.
- One authorization path: `authz.proto` carries `option (auth.permission)`, a
  reflection interceptor reads it, and `Perm_*` constants replace the five
  hand-maintained permission tables. Default-deny, the `IdentityGated`
  sentinel, the public-route allowlists and on-behalf-of handling are retained.
  QUIC and MCP gain method registries with mapping guard tests and REST a route
  guard, so a handler added without a permission now fails a test instead of
  shipping unauthenticated.

### Removed

- `pkg/auth/middleware/mapping.go` and the five permission tables it fed.
- The `pkg/sql/internal/privprobe` scaffolding, superseded by `pkg/sql/privilege`.

## [0.2.1-alpha] - 2026-08-14

### Fixed

- The two in-repo modules pinned each other at `v0.1.0-alpha`, so a consumer
  taking `sdk/go v0.2.0-alpha` resolved the parent module to the release that
  still had Avro, and the build failed with `undefined: codec.Avro` — the new
  SDK pulls go-codec v1.4.0, which the old parent's `pkg/dao` cannot compile
  against. Both modules carry a local-path `replace` at each other, which
  applies only in a main module, so every in-repo build resolved through the
  working tree and nothing surfaced the stale requirement. `sdk/go` now
  requires the parent at `v0.2.1-alpha` and the root requires `sdk/go` at the
  same version.

`v0.2.0-alpha` and `sdk/go/v0.2.0-alpha` are superseded and must not be
pinned. The tags remain, per the immutability rule.

## [0.2.0-alpha] - 2026-08-14

### Removed — BREAKING: the Avro codec is gone from both modules

`github.com/hamba/avro` is archived upstream and carries reachable advisories
GO-2026-5046, GO-2026-5047 and GO-2026-5048 with no fix forthcoming and no
production-grade replacement, so Avro leaves the product stack entirely (owner
decision, 2026-08-14; the same decision produced go-codec v1.4.0). These
exported identifiers no longer exist:

- `pkg/codec`: `TypeAvro`, `AvroCodec`
- `pkg/dao`: `CodecAvro`

The `codec_avro` build tag and the `WITH_CODEC_AVRO` Makefile switch are gone
with them, as are the `.avro` and `.avsc` entries in extension-based format
detection and Avro's membership in `probeableCodecs`. A caller that selected
Avro must choose another codec; the JSON, YAML, TOML, MsgPack, BSON, CBOR and
ProtoBuf codecs are unaffected.

### Added

- `cmd/qrdb` and `cmd/qrdbd` sources, and the Wails frontend, are actually in
  the repository. They had never been committed: the root `.gitignore` carried
  unanchored `qrdb` and `qrdbd` patterns intended for the compiled binaries,
  and a gitignore pattern without a slash matches at any depth and matches
  directories, so both command trees — 222 Go files — were excluded. `HEAD` did
  not build.
- `workflow_dispatch` trigger on the CI workflow (ci-template v1.3), so a run
  lost to a runner crash or host overload can be retried without a commit.

### Changed

- `github.com/jeremyhahn/go-codec` pinned to `v1.4.0`, the Avro-free release.
- `github.com/jeremyhahn/go-quicraft` pinned to `v0.3.1-alpha`.
- `golang.org/x/crypto` to `v0.55.0` and `golang.org/x/text` to `v0.41.0` in
  the `sdk/go/gorm/conformance` module, clearing ten HIGH advisories
  (CVE-2026-39828 through CVE-2026-46597, and CVE-2026-56852).

### Fixed

- HTTP/3 listener errors were compared against `http.ErrServerClosed` with
  `!=`, and the pgsql row collector compared against `io.EOF` with `==`; a
  wrapped sentinel from either was mishandled. Both now use `errors.Is`.
- The eight registry mocks in the transport and SQL provider tests implement
  `service.Registry.PrivilegeService`, and the MVCC error stubs are referenced
  by the names they actually have. Without these no test binary in the
  affected packages compiled, which also made `gosec` exit non-zero after
  reporting no findings.
- `TestRunStart_OpenAll_DatabasesDirPermissionError` provoked its failure with
  a 0o000 directory, which root ignores; under CI it started a real node and
  hung until the test binary's deadline. It now uses a regular file where the
  directory belongs, failing with `ENOTDIR` for root and non-root alike.
- `TestNewStartCmd_UnknownSM_NoConfig` read the package directory as its data
  directory, picking up a stray committed `data/ca.pem` and failing before it
  reached the assertion it was written for.

### Security

- `cmd/qrdbd/data/` is ignored. A locally run daemon issues itself a CA, node
  and admin key there, and the directory was previously covered only as
  collateral of the unanchored `qrdbd` pattern.
- Playwright's generated `playwright-report/` and `test-results/` output no
  longer ships in the repository.

[Unreleased]: https://github.com/jeremyhahn/go-qrdb/compare/v0.17.0-alpha...HEAD
[0.17.0-alpha]: https://github.com/jeremyhahn/go-qrdb/compare/v0.16.3-alpha...v0.17.0-alpha
[0.6.0-alpha]: https://github.com/jeremyhahn/go-qrdb/compare/v0.5.9-alpha...v0.6.0-alpha
[0.3.0-alpha]: https://github.com/jeremyhahn/go-qrdb/compare/v0.2.1-alpha...v0.3.0-alpha
[0.2.1-alpha]: https://github.com/jeremyhahn/go-qrdb/compare/v0.2.0-alpha...v0.2.1-alpha
[0.2.0-alpha]: https://github.com/jeremyhahn/go-qrdb/compare/v0.1.1-alpha...v0.2.0-alpha
