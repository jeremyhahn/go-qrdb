# Databases — API

> **Status: NORMATIVE for shipped behaviour; APPROVED, not yet implemented, for the target design** ·
> Scope: go-qrdb · Last verified: 2026-10-23
> Conflicts with this document are defects. Fix them here first.
>
> "Target" sections describe the multi-model database program in
> [architecture.md](architecture.md); they are approved and not
> implemented. OD-n refers to that document's ruled decisions.

gRPC service `qrdb.v1.QRDBService`. The Unix socket transport serves the same
gRPC wire over a UDS, so its methods match the gRPC column.

## Capability matrix

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded (`Client.Database()`) | `scp` permission |
|---|---|---|---|---|---|---|
| Create | `POST /v1/databases` | `DatabaseCreate` | `db.Create` | `qrdb_database_create` | `CreateDatabase` | `databases:create` (+ `create_raft` / `create_volume`) |
| Get | `GET /v1/databases/{name}` | `DatabaseGet` | `db.Get` | `qrdb_database_get` | `GetDatabase` | `databases:read` |
| List | `GET /v1/databases/list` | `DatabaseList` | `db.List` | `qrdb_database_list` | `ListDatabases` | `databases:list` |
| Drop | `DELETE /v1/databases/{name}` | `DatabaseDrop` | `db.Drop` | `qrdb_database_drop` | `DropDatabase` | `databases:delete` |
| Update protocol | `PATCH /v1/databases/{name}/protocol` | `DatabaseUpdateProtocol` | `db.UpdateProtocol` | `qrdb_database_update_protocol` | `UpdateDatabaseProtocol` | `databases:update` |
| Update etcd | `PATCH /v1/databases/{name}/etcd` | `DatabaseUpdateEtcd` | `db.UpdateEtcd` | `qrdb_database_update_etcd` | `UpdateDatabaseEtcd` | `databases:update` |
| Update raft | `PATCH /v1/databases/{name}/raft` | `DatabaseUpdateRaft` | `db.UpdateRaft` | `qrdb_database_update_raft` | `UpdateDatabaseRaft` | `databases:update` |
| Update barrier | `PATCH /v1/databases/{name}/barrier` | `DatabaseUpdateBarrier` | `db.UpdateBarrier` | `qrdb_database_update_barrier` | `UpdateDatabaseBarrier` | `databases:update` |
| Update SQL settings | `PATCH /v1/databases/{name}/sql` | `DatabaseUpdateSQL` | `db.UpdateSQL` | `qrdb_database_update_sql` | `UpdateDatabaseSQL` | `databases:update` |
| Update compaction policy | `PATCH /v1/databases/{name}/compaction` | `DatabaseUpdateCompaction` | `db.UpdateCompaction` | `qrdb_database_update_compaction` | `UpdateDatabaseCompaction` | `mvcc:compact` (`ScopeSelfStrict`) |
| Update connection | `PATCH /v1/databases/{name}/connection` | `DatabaseUpdateConnection` | `db.UpdateConnection` | `qrdb_database_update_connection` | `UpdateDatabaseConnection` | `databases:update` |
| Scale | `PATCH /v1/databases/{name}/scale` | `DatabaseScale` | `db.Scale` | `qrdb_database_scale` | `ScaleDatabase` | `databases:scale` |
| Seal | `POST /v1/databases/{name}/seal` | `DatabaseSeal` | `db.Seal` | `qrdb_database_seal` | `SealDatabase` | `databases:seal` |
| Unseal | `POST /v1/databases/{name}/unseal` | `DatabaseUnseal` | `db.Unseal` | `qrdb_database_unseal` | `UnsealDatabase` | `databases:unseal` |
| Barrier status | `GET /v1/databases/{name}/barrier/status` | `DatabaseBarrierStatus` | `db.BarrierStatus` | `qrdb_database_barrier_status` | `DatabaseBarrierStatus` | `databases:barrier_status` |

Fifteen capabilities, all present on all six transports. Update SQL settings
is specified in [../row-locking/api.md](../row-locking/api.md); the compaction
policy below and in [../key-value/engineering.md](../key-value/engineering.md).

**The compaction policy is the owning tenant's alone.** Setting it is
compacting, on a schedule, so it takes `mvcc:compact` rather than
`databases:update`, and only the tenant that owns the database may set it --
the system tenant included, which sets policies on its own databases only. It
is set by its own verb alone -- never by create, which would let
`databases:create` compact, and not by any configuration file. The policy is
replicated state in the database's own store (see
[../key-value/engineering.md](../key-value/engineering.md) "Scheduled
compaction"): every replica of the database's group holds it, the group's
leader runs it, and a backup carries it.

The TypeScript SDK sets it with `client.database.updateCompaction(name,
compaction?)` (`DatabaseCompactionConfig`: `mode` `'off' | 'revision' |
'period'`, `retainRevisions`, `retainPeriod`) -- `PATCH
/v1/databases/{name}/compaction` over REST, `DatabaseBinding.UpdateCompaction`
in the desktop client -- and reads it back as `DatabaseInfo.compaction`. When
the server cannot read the policy from the database's store -- within 5 s, or
the caller's own deadline if shorter: a raft database just created may still be
electing its leader -- `compaction` is absent and `compactionUnavailable` is
true: the policy is unknown, not off, and the rest of the information stands.
Omitting `compaction` turns the policy off.

**Note the additive permissions.** Creating a raft-backed database checks
`databases:create_raft` *in addition to* `databases:create`, as does every
database of a raft-isolated tenant; every database of a volume-isolated tenant
checks `databases:create_volume`. Neither replaces the base grant. They apply to
every create path -- REST, gRPC, Unix, QUIC, MCP and SQL `CREATE DATABASE` --
and a refusal answers `403` / `PERMISSION_DENIED`.

## Database scope on data-plane requests

Every data-plane capability in every other feature carries a database scope:

| Transport | Mechanism |
|---|---|
| REST | `X-QRDB-Database` header |
| gRPC / Unix | `x-qrdb-database` metadata value |
| QUIC / MCP | equivalent request field |
| Embedded | `Client.DatabaseScoped(name)`, resolved through the node's service registry; refused with `transport.DatabaseScopeUnsupportedError` when the embedded transport has none |

Absent, the server's default database (`system`) is used.

A DAO is placed in a named database the same way:
`sdk.NewDatabaseDAO(sdk.NewClientDAOFactory(client), name, ...)` (and
`sdk.NewDatabaseDAOTransactional`) builds it over
`client.DatabaseScoped(name).KVStore()`, and refuses with
`sdk.DAODatabaseStoreUnresolvedError` rather than fall back to the default
database. The DAO surface is in
[../secondary-indexes/engineering.md](../secondary-indexes/engineering.md).

## Schemas

`POST /v1/databases` — `CreateDatabaseRequest`:

Shipped:

```json
{ "name": "billing", "backend": "raft",
  "dataDir": "<install-dir>/data/billing",
  "mode": "distributed", "replicas": 3,
  "protocol": { "mysql": false, "postgresql": true,
                "etcd": { "enabled": false, "listenAddr": ":2379" } },
  "barrier": { "enabled": true, "mode": "passphrase",
               "secret": "correct horse battery" },
  "connection": { "transport": "tcp", "address": "db2.example.com:63003" } }
```

Response — `DatabaseInfo`, which adds `tenantId`, `status`, `shardId`,
`storageCodec` (absent for the node's default codec) and `dedicatedWal`. A
Raft block reports no shard ID of its own: the database's is `shardId`.
`status` is `remote` on a node that does not hold a non-replicated database
(OD-2): another node, the catalog record's `owner_node`, holds it, and this
node forwards its requests there. The Go `database.DatabaseInfo` carries that
node as `OwnerNode`; the transport shape below does not.
Every transport sends the same shape: REST, QUIC, MCP, the embedded transport and the desktop bindings emit the JSON form, gRPC and the
Unix socket the protobuf `DatabaseInfoResponse`, both produced by one converter
(`pkg/api/dbwire`). Its keys are pinned by a recorded REST response that the Go
handler test (`TestDatabaseInfoWire_*`) and the TypeScript SDK test
(`database-info-wire.test.ts`) both decode, with no exceptions in either
direction.

**Secrets are write-only.** A request may set a discovery secret
(`raft.static.sharedSecret`, `raft.multicast.sharedSecret`,
`raft.token.token`); no response on any transport returns it. A response
reports only that it is set:

```json
"raft": { "clusterMode": "distributed", "discovery": "static",
  "static":    { "peers": ["db.example.com:63001"], "sharedSecretSet": true },
  "multicast": { "multicastGroup": "239.255.0.1:63100", "sharedSecretSet": true },
  "token":     { "endpoint": "https://db.example.com/join", "clusterSize": 3, "tokenSet": true } }
```

The response types cannot carry a secret: `DatabaseRaftInfo` in the Go SDK,
the protobuf and the TypeScript SDK has a `*Set` flag where the request has
the secret. The barrier passphrase is never stored, so `DatabaseBarrierInfo`
has no field for it and no response reports it at all. On gRPC the secret
field numbers are `reserved` in the response messages.

**A Raft update keeps a secret it leaves out.** A Raft update replaces the
whole Raft block, and a client that reads a database's settings back has no
secrets to send. So in an update:

- a discovery block **with** its secret field sets that secret (this is how a
  secret is rotated);
- a discovery block **without** its secret field keeps the stored secret --
  including where validation needs one, as for multicast and token discovery;
- a discovery block **left out** is removed, secret and all (this is how a
  secret is removed: disable that discovery method);
- a kept secret stays with **what it authenticates** -- the static peer set,
  the multicast group and interface, the token endpoint. An update that
  changes that destination without sending the secret is refused with
  `database.DiscoverySecretRebindError` (400 / `INVALID_ARGUMENT`): keeping
  the secret would hand it, unseen, to a destination the caller chose. Send
  the secret with the new destination.
  - The static peer set is the static block's `peers` together with the
    top-level `peers` when `discovery` is `static` or unset (static discovery
    reaches both). It is a set: order, duplicates, and which of the two lists
    holds an address do not matter. Under another discovery method the
    top-level `peers` are not part of it.
  - Destinations compare **literally**. No name is resolved and no address is
    normalised, so `db.example.com:63001` and `DB.example.com:63001`, or a
    host name and its IP address, are different destinations: rewriting one
    as the other needs the secret.

The response to the update reports each kept secret as set.

A raft-backed database's Raft configuration carries its shard ID, so an
update can change it but not remove it: a Raft update with no configuration is
refused with `database.RaftConfigRequiredError` (400 / `INVALID_ARGUMENT`).
Configuration validation failures -- an unknown cluster mode or discovery
method, a missing discovery block, secret, domain, endpoint or token, an
invalid cluster size, mode, storage codec or etcd listen address -- are
400 / `INVALID_ARGUMENT` on every transport.

**There is no `engine` field.** A request carrying it on REST,
QUIC or MCP is refused as an unknown field, and on gRPC the field numbered 2
in `DatabaseCreateRequest` and `DatabaseInfoResponse` is
`reserved 2; reserved "engine";` so there is no field to set (OD-13).
`backend` is `memory`, `file`, `pebble`, `raft` or `objstore`. The backend
does not decide where most databases' data lives, and
`objstore` is refused when the store is opened — see
[architecture.md](architecture.md) "Current state".

### Target: create and info (remaining, unshipped)

`backend` becomes optional; when absent the run mode's default (`raft` in raft
mode, `pebble` in standalone mode) is written into the record (OD-3). `raft.stateStorage` selects a raft
database's state-machine store (OD-8).

```json
{ "name": "billing", "backend": "raft", "replicas": 3,
  "raft": { "clusterMode": "distributed", "stateStorage": "pebble" },
  "protocol": { "mysql": false, "postgresql": true },
  "barrier": { "enabled": true, "mode": "shamir",
               "sharesRequired": 3, "sharesTotal": 5 } }
```

`DatabaseInfo` gains a read-only `binding` block reporting what the resolver
decided:

```json
{ "name": "billing", "tenantId": "acme", "backend": "raft", "status": "open",
  "shardId": 4107, "replicas": 3,
  "binding": { "kind": "raft", "nodes": ["node-1", "node-2", "node-3"],
               "encryption": "tenant+database", "generation": 1 } }
```

`kind` is `local` or `raft`; `nodes` is the owning node of a local binding or
the replica nodes of a raft one; `encryption` is `node`, `tenant` or
`tenant+database`; `generation` increments each time the name is created, so a
dropped-and-recreated database is distinguishable from its predecessor.

`DatabaseInfoResponse` gains a `DatabaseBindingInfo binding` message field.
The `connection` block and the update-connection capability are removed on
every transport (OD-9).

### Updates, scale and unseal

`PATCH /v1/databases/{name}/compaction` — the database's scheduled-compaction
policy, off unless set:

```json
{ "compaction": { "mode": "revision", "retainRevisions": 10000 } }
{ "compaction": { "mode": "period", "retainPeriod": "24h" } }
{ "compaction": { "mode": "off" } }
```

`mode` is `off`, `revision` or `period`; `retainRevisions` (>= 1) belongs to
`revision` and `retainPeriod` (a Go duration, >= `1s`) to `period`, and a field
that does not belong to the mode is refused. An absent `compaction` turns the
policy off. `DatabaseInfo` reports it as `compaction`. On gRPC:
`DatabaseUpdateCompactionRequest{name, compaction}` with
`DatabaseCompactionConfig{mode, retain_revisions, retain_period}`, and
`DatabaseInfoResponse.compaction`.

`PATCH /v1/databases/{name}/scale` — `{"target_replicas": 5}`. The database name
travels in the **path**, not the body (`json:"-"` on the request struct).

`POST /v1/databases/{name}/unseal` — carries the unseal `secret`.

On MCP, `qrdb_database_unseal`'s `secret` and `qrdb_database_create`'s
`barrier_secret` each take a text form and a `<name>_b64` form, standard
base64 that carries any bytes exactly; a call carrying both is refused, and
so is a value that is not a JSON string, in either form or in
`static_shared_secret`, `multicast_shared_secret` or `token_value`. The
convention is the barrier tools' own
([../barrier-seal/api.md](../barrier-seal/api.md#secret-arguments-over-mcp)).
The Go SDK's MCP client always sends `<name>_b64`.

`DatabaseRaftConfig` fields: `clusterMode` (`none|local|distributed`),
`replicaId`, `raftAddress`, `peers`, `localNodeCount`. **There is no
`shardId`**: the cluster issues a database's shard ID at create, it never
changes, and it is reported only as `DatabaseInfo.shardId`. gRPC reserves
field 2 (`shard_id`) of `DatabaseRaftConfig`; on the JSON transports the key is
not a field and is ignored. See
[engineering.md](engineering.md#cluster-wide-shard-ids).

On MCP the Raft configuration is flat tool arguments, reaching every field the
other transports carry. `qrdb_database_create` names the top-level fields with
a `raft_` prefix; `qrdb_database_update_raft` names them bare. The discovery
arguments are named alike in both.

| Field | `qrdb_database_create` | `qrdb_database_update_raft` |
|---|---|---|
| `clusterMode` | `raft_cluster_mode` | `cluster_mode` |
| `replicaId` | `raft_replica_id` | `replica_id` |
| `raftAddress` | `raft_address` | `raft_address` |
| `peers` | `raft_peers` (array) | `peers` (array) |
| `localNodeCount` | `raft_local_node_count` | `local_node_count` |
| `discovery` | `discovery_method` | `discovery_method` |
| `static` | `static_peers` (array), `static_shared_secret` | same |
| `multicast` | `multicast_shared_secret`, `multicast_group`, `multicast_interface`, `multicast_min_peers`, `multicast_announce_interval`, `multicast_discover_timeout` | same |
| `dns` | `dns_domain`, `dns_service`, `dns_proto`, `dns_lookup_timeout` | same |
| `token` | `token_endpoint`, `token_value`, `token_cluster_size`, `token_timeout` | same |

A discovery block is sent when `discovery_method` names it or any of its
arguments is given -- an empty string counts, which is how a block whose only
stored field is its secret is sent back without the secret. A block not sent
is absent from the request, so an update removes it with its secret, as
leaving a block out does on every other transport.

Both SDKs set every one of these fields. The request type -- `DatabaseRaftConfig`
in the Go SDK (`transport.DatabaseRaftConfig`) and the TypeScript SDK -- carries
`discovery` and the `static`, `multicast`, `dns` and `token` blocks
(`StaticDiscoveryConfig`, `MulticastDiscoveryConfig`, `DNSDiscoveryConfig`,
`TokenDiscoveryConfig`), each secret included and omitted when empty, and every
Go SDK transport (REST, gRPC, Unix, QUIC, MCP, embedded) sends all of it. The
request keys are pinned by a recorded request,
`sdk/typescript/test/fixtures/rest-database-raft-request.json`: the Go test
`TestDatabaseRaftRequestWire_*` holds it to the Go SDK's encoding and decodes it
through the REST handler refusing unknown keys, and the TypeScript test
`database-raft-request-wire.test.ts` holds the TypeScript types to it.

`DatabaseBarrierConfig` fields: `enabled`, `mode` (`passphrase|shamir`),
`sharesRequired`, `sharesTotal`, `secret`. On a create with `enabled` and
`mode: "passphrase"`, the server initializes the barrier from `secret` in the
same call; `secret` is request-only, never persisted and never returned. The
TypeScript SDK types it only on the create request's `DatabaseBarrierRequest`,
so the `DatabaseBarrierConfig` a `DatabaseInfo` reports cannot carry it. A
create with `mode: "shamir"` registers an uninitialized barrier at
`sharesRequired`-of-`sharesTotal` that its share holders' custody initializes —
see the share-holder matrix below. Either strategy is accepted only where the
barrier is on the database's data path: a non-raft database with its own
`dataDir`, which only the system tenant can set (`BarrierOffDataPathError`,
`SharedStoreBarrierUnsupportedError` otherwise). A non-system tenant therefore
cannot create a barrier-backed database of either kind.

`DatabaseConnectionConfig` fields: `transport` (`tcp|quic`), `address`,
`caFile`, `certFile`, `keyFile`.

## Capability matrix — database barrier share holders

A database barrier with Shamir custody has the unseal domain only; the holder
model, error classes and identity gating are
[../shamir-custody/api.md](../shamir-custody/api.md)'s. `{name}` is the
database, resolved in the caller's tenant.

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded (`Client.Database()`) | `scp` permission |
|---|---|---|---|---|---|---|
| Enrol holder | `POST /v1/databases/{name}/barrier/officers` | `DatabaseBarrierOfficerRegister` | `db.BarrierOfficerRegister` | `qrdb_database_barrier_officer_register` | `DatabaseBarrierOfficerRegister` | `databases:officer_register` |
| List holders | `GET /v1/databases/{name}/barrier/officers` | `DatabaseBarrierOfficerList` | `db.BarrierOfficerList` | `qrdb_database_barrier_officer_list` | `DatabaseBarrierOfficerList` | `databases:officer_list` |
| Initialize Shamir custody | `POST /v1/databases/{name}/barrier/shamir/initialize` | `DatabaseBarrierShamirInitialize` | `db.BarrierShamirInitialize` | `qrdb_database_barrier_shamir_initialize` | `DatabaseBarrierShamirInitialize` | `databases:initialize_shamir` |
| Claim begin | `POST /v1/databases/{name}/barrier/officers/{username}/claim/begin` | `DatabaseBarrierShareClaimBegin` | `db.BarrierShareClaimBegin` | `qrdb_database_barrier_share_claim_begin` | `DatabaseBarrierShareClaimBegin` | **identity-gated** |
| Claim complete | `POST /v1/databases/{name}/barrier/officers/{username}/claim/complete` | `DatabaseBarrierShareClaimComplete` | `db.BarrierShareClaimComplete` | `qrdb_database_barrier_share_claim_complete` | `DatabaseBarrierShareClaimComplete` | **identity-gated** |
| Quorum challenge | `POST /v1/databases/{name}/barrier/quorum/begin` | `DatabaseBarrierQuorumBegin` | `db.BarrierQuorumBegin` | `qrdb_database_barrier_quorum_begin` | `DatabaseBarrierQuorumBegin` | **identity-gated** |
| Quorum submission | `POST /v1/databases/{name}/barrier/quorum/submit` | `DatabaseBarrierQuorumSubmit` | `db.BarrierQuorumSubmit` | `qrdb_database_barrier_quorum_submit` | `DatabaseBarrierQuorumSubmit` | **identity-gated** |
| Start rekey | `POST /v1/databases/{name}/barrier/rekey/start` | `DatabaseBarrierRekeyStart` | `db.BarrierRekeyStart` | `qrdb_database_barrier_rekey_start` | `DatabaseBarrierRekeyStart` | `databases:rekey` |
| Revoke holder | `POST /v1/databases/{name}/barrier/officers/revoke` | `DatabaseBarrierOfficerRevoke` | `db.BarrierOfficerRevoke` | `qrdb_database_barrier_officer_revoke` | `DatabaseBarrierOfficerRevoke` | `databases:officer_revoke` |
| Unenrol holder (before initialization) | `POST /v1/databases/{name}/barrier/officers/unenrol` (204) | `DatabaseBarrierOfficerUnenrol` | `db.BarrierOfficerUnenrol` | `qrdb_database_barrier_officer_unenrol` | `DatabaseBarrierOfficerUnenrol` | `databases:officer_unenrol` |

Unseal and seal of a Shamir database barrier have no route of their own: each
is the quorum submission with `op` `unseal` or `seal`; a single caller's
`db.Seal` / `POST /v1/databases/{name}/seal` is refused (422) and a secret
unseal is refused by the strategy. These permissions are self-scoped like the
rest of `databases:*`: no built-in role but Owner carries them, and a holder
identity holds none of them.

Initialization request: `{"policy": "designated"}` — the M-of-N is the
`sharesRequired`/`sharesTotal` the database was created with, and exactly
`sharesTotal` holders must be enrolled. The initialization response is
share-free: `{"threshold", "total_shares", "share_count"}`. Until initialization no more than
`sharesTotal` can be: one more is refused with
`ceremony.HolderLimitReachedError` (409, `FAILED_PRECONDITION`). Before
initialization a holder holds no share, so one enrolled by mistake is removed
by unenrolment (`{"username"}`, 204) with no quorum, and its place is free for
another; once the custody is initialized unenrolment is refused with
`ceremony.ErrUnenrolInitialized` (409, `FAILED_PRECONDITION`) and a holder is
removed by revocation.
`GET /v1/databases/{name}/barrier/status` reports `initialized_at` once the
custody is initialized and omits it until then; the TypeScript SDK's
`database.barrierStatus(name)` reports it as `initializedAt` on both of its
transports. A create with `mode: "shamir"`
carries no `secret`; one that does is refused. A completed rekey records its
new M-of-N on the database's configuration. If that write fails, the rekey is
still reported complete, because it is: the old shares no longer open the
barrier. The failure is logged.

Every request body is the system holder request of
[../shamir-custody/api.md](../shamir-custody/api.md); the SDK types embed it
beside `database` (`DatabaseOfficerRegisterRequest`, ...), and the gRPC
messages nest it (`DatabaseBarrierOfficerRegisterRequest{database, officer}`).
Rekey takes `{"threshold", "policy"}` and revocation
`{"username", "threshold"}`. A client refuses a request that names no database
with `DatabaseNameRequiredError` before it is sent.

## Verifiable logs

The `LogService` capabilities ([architecture.md](architecture.md),
"Transports"), one row per capability. The REST, gRPC / Unix, QUIC and MCP
columns are served while the node has a log service, and none of them
otherwise; the embedded column is served while the embedded client has one
(`embedded.WithLogService`). The SDK's `Client.Vlog()` returns a
`transport.VlogClient` over any of them, and the `qrdb vlog` commands
([user.md](user.md), "Keep a verifiable log") drive it. Each method is
authorized by its permission before the service is reached.

The TypeScript SDK's `client.vlog` is a `VlogClient` (`sdk/typescript`): the
`RestClient` drives the REST routes below, and the `WailsClient` calls the
desktop GUI's `VlogBinding` (`cmd/qrdb/bindings_vlog.go`), which drives
`Client.Vlog()` over the connection profile's transport. Both take and answer
the `vlogwire` shapes with camelCase field names, byte fields as base64
strings:

| Capability | TypeScript `VlogClient` | Wails `VlogBinding` |
|---|---|---|
| Create log | `create(req)` | `Create` |
| List logs | `list()` | `List` |
| Append | `append(req)` | `Append` |
| Get entries | `get(req)` | `Get` |
| Inclusion proof | `proveInclusion(req)` | `ProveInclusion` |
| Consistency proof | `proveConsistency(req)` | `ProveConsistency` |
| Head | `head(log)` | `Head` |
| Sign head | `signHead(log)` | `SignHead` |
| Rotate key | `rotateKey(req)` | `RotateKey` |
| Expire bodies | `expireBodies(req)` | `ExpireBodies` |

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
| Rotate key | `POST /vlog/key/rotate` | `VlogRotateKey` | `vlog.RotateKey` | `qrdb_vlog_rotate_key` | `RotateKey` | `vlog:sign_head` |
| Expire bodies | `POST /vlog/expire` | `VlogExpireBodies` | `vlog.ExpireBodies` | `qrdb_vlog_expire_bodies` | `ExpireBodies` | `vlog:expire` |

Rotating the head-signing key is a use of that key, like signing a head, so
it is gated on `vlog:sign_head`: an Operator rotates, and an Admin is refused.
A rotation appends the new key to the log's verifier keys and changes no other
state. Every head signed after it names the new key's version, and a
checkpoint signed before it still verifies under the key it names.

A log is named in the caller's tenant and the request's database: the
`X-QRDB-Database` header on REST and the `x-qrdb-database` metadata on gRPC,
`system` when absent, as for vectors; the QUIC frame's database; the MCP
session's. No request names a tenant, on any transport: a `tenant_id` in a
QUIC payload or MCP tool call is ignored. A node built without the verifiable-log feature answers every method 501 /
`UNIMPLEMENTED`. A follower that is not the group's leader forwards a REST
request to the leader when leader forwarding is configured.

On REST the writes take a JSON body and the reads a query, named as in the
QUIC and MCP payloads below. Every response is the `sdk/go/transport/vlogwire` result
shape QUIC and MCP answer with too, so one decoder reads all three; byte
fields (an entry, a root, a hash, a signature, a checkpoint, a public key) are
base64:

| Route | Request |
|---|---|
| `GET /vlog/logs` | none; the logs of the request's database |
| `POST /vlog/log` | `{"log", "origin", "verifier_keys", "allow_expire_bodies", "signing_key_type"}`; `signing_key_type` is `ed25519`, `ecdsa-p256-sha256` or absent for a log with no key |
| `POST /vlog/append` | `{"log", "entries"}` |
| `GET /vlog/entries` | `log`, and optional `from` and `limit` |
| `GET /vlog/proof/inclusion` | `log`, `index`, `size` |
| `GET /vlog/proof/consistency` | `log`, `size1`, `size2` |
| `GET /vlog/head` | `log` |
| `POST /vlog/head/sign` | `{"log"}` |
| `POST /vlog/key/rotate` | `{"log", "signing_key_type"}`; `signing_key_type` is `ed25519` or `ecdsa-p256-sha256`, and is required |
| `POST /vlog/expire` | `{"log", "cutoff"}` |

A malformed body is refused 400 with `ReadBodyError`, a missing query
parameter with `MissingParameterError` and one that is not a decimal unsigned
integer with `InvalidParameterError`, before the service is reached.

| Condition | REST | gRPC / Unix |
|---|---|---|
| Invalid tenant, log name, descriptor, append batch, key type, or an empty checkpoint | 400 | `INVALID_ARGUMENT` |
| Log or checkpoint already exists | 409 | `ALREADY_EXISTS` |
| Unknown log or key | 404 | `NOT_FOUND` |
| Size or index beyond the tree | 416 | `OUT_OF_RANGE` |
| Expiry on a log that does not allow it, checkpoint root mismatch, invalid signing key | 409 | `FAILED_PRECONDITION` |
| Key version conflict | 409 | `ABORTED` |
| Head signature failure | 500 | `INTERNAL` |
| Corrupt log table | 500 | `DATA_LOSS` |
| Verifiable logs not compiled in, or no log service on the node | 501 | `UNIMPLEMENTED` |
| Verifiable-log feature not active on the group | 503 | `UNAVAILABLE` |

Three refusals also carry a structured-refusal reason beside the class, on
every transport, and every SDK transport rebuilds the typed `pkg/vlog` error
from it, so a client matches them with `errors.As` rather than the message:

| Refusal | Reason | Metadata | SDK error |
|---|---|---|---|
| Creating a log that already exists | `VLOG_LOG_EXISTS` | `tenant_id`, `database`, `log` | `*vlog.LogExistsError` |
| A second signed head at a tree size | `VLOG_CHECKPOINT_EXISTS` | `size` | `*vlog.CheckpointExistsError` |
| Expiry on a log created without `allow_expire_bodies` | `VLOG_EXPIRE_NOT_ALLOWED` | `tenant_id`, `database`, `log` | `*vlog.ExpireNotAllowedError` |

A missing or malformed metadata field leaves its field zero; the reason alone
selects the type.

## Verifiable-log head-signing keys

Creating a verifiable log with a signing key type asks the node's
`vlog.HeadSignerProvider` for the key; the software provider is the
default, and a composer passes a hardware or KMS one with
`WithHeadSignerProvider` ([engineering.md](engineering.md), "Verifiable-log
head-signing seam"). The answer names the key version, the provider's
reference and the public key (PKIX DER) a signed head verifies against; the
private key is never in an answer. A rotation racing another is a conflict.

Both are refused, before the provider is asked, while the
`verifiable-log-keys` feature is not yet active on every replica of the
tenant's group (`LogFeatureNotActiveError`), and every log call is refused for
a barrier-isolated tenant (`BarrierTenantLogNotSupportedError`) and for a
barrier-enabled database (`BarrierDatabaseLogNotSupportedError`), sealed or
not: their logs and keys would be stored under the system barrier rather than
their own ([architecture.md](architecture.md), "Isolation"). Both refusals are
`Unimplemented` on every transport: `501 Not Implemented` over REST,
`UNIMPLEMENTED` over gRPC.

## Verifiable-log payloads over QUIC and MCP

A QUIC request payload and an MCP tool's arguments carry the same fields:
`log`; `origin`, `verifier_keys`,
`allow_expire_bodies` and `signing_key_type` to create; `entries` to append;
`from` and `limit` to get; `index` and `size` for an inclusion proof; `size1`
and `size2` for a consistency proof; `signing_key_type` to rotate the key;
`cutoff` to expire. QUIC payloads and
every result are JSON, the result shapes those of `sdk/go/transport/vlogwire`; byte
fields (entry bodies, hashes, roots, signatures, checkpoints, public keys) are
standard base64 (RFC 4648). A result carries a head key's version and public
key, never the provider's key reference. An MCP argument that is missing,
not an array of strings where one is required, or not base64 is refused as a
tool error before the service is reached. A log error carries the class
`pkg/api/errclass` gives it on every transport: an unknown log is `NotFound`, a
size or index past the tree `OutOfRange`, an expiry on a log that does not
allow it `FailedPrecondition`.

## Status and error mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| Success | 200 | `OK` |
| Database already exists in the caller's tenant | 409 | `ALREADY_EXISTS` — `database.AlreadyExistsError`; another tenant's database of the same name is not a conflict, and the create makes the caller's own |
| Name held in the store by another database's incarnation that the catalog does not record | 409 | `ALREADY_EXISTS` — `database.AlreadyExistsError` with `Elsewhere` set; that database is untouched |
| Name reserved for node or tenant state (`wal`, `nodehost`, `__home`) or otherwise invalid | 400 | `INVALID_ARGUMENT` — `database.ReservedNameError` / `database.InvalidNameError` |
| Unknown database, or another tenant's database | 404 | `NOT_FOUND` — never implicitly created; another tenant's database is indistinguishable from a missing one |
| Barrier enabled on a shared store | 422 | `FAILED_PRECONDITION` — `database.SharedStoreBarrierUnsupportedError` |
| Barrier enabled off the data path (raft backend, volume/raft-isolated tenant) | 422 | `FAILED_PRECONDITION` — `database.BarrierOffDataPathError` |
| Barrier strategy other than `passphrase` or `shamir` (`UnsupportedBarrierStrategyError`) | 400 | `INVALID_ARGUMENT` |
| A `secret` on a `shamir` create (`ShamirSecretNotAllowedError`) | 400 | `INVALID_ARGUMENT` |
| A `shamir` create whose M-of-N is outside 2 <= `sharesRequired` <= `sharesTotal` <= 255 (`InvalidShamirSchemeError`), refused before anything is created | 400 | `INVALID_ARGUMENT` |
| A holder operation on a database with no Shamir barrier (`NotShamirBarrierError`) | 409 | `FAILED_PRECONDITION` |
| A `shamir` create on a node with no holder CA (`ShamirCustodyNotConfiguredError`) | 409 | `FAILED_PRECONDITION` |
| A holder enrolled past `sharesTotal` before initialization (`ceremony.HolderLimitReachedError`) | 409 | `FAILED_PRECONDITION` |
| A holder unenrolled once the custody is initialized (`ceremony.ErrUnenrolInitialized`) | 409 | `FAILED_PRECONDITION` — revoke instead |
| A holder registered or unenrolled, or custody initialized, while an initialization is in flight (`ceremony.ErrInitializationInProgress`) | 409 | `ABORTED` — retry |
| Changing a Shamir barrier's strategy or M-of-N by update (`BarrierMigrationNotSupportedError`) | 409 | `FAILED_PRECONDITION` — rekey instead |
| Turning an existing database's barrier on or off (`BarrierMigrationNotSupportedError`) | 409 | `FAILED_PRECONDITION` |
| `cluster_mode` / `replicas` on a non-raft backend | 400 | `INVALID_ARGUMENT` |
| `target_replicas < 1` | 400 | `INVALID_ARGUMENT` |
| Target: etcd enabled on a non-`raft` database | 400 | `FAILED_PRECONDITION` — `database.EtcdRequiresRaftBackendError` |
| Wrong unseal secret | 403 | `PERMISSION_DENIED` — the database stays sealed |
| A create or barrier update carrying a secret, an unseal, or a holder's quorum submission, on a connection without TLS or redirected to `http` | not sent | not sent — the SDK returns `sdk.BarrierSecretRequiresTLSError`, naming the transport and the kind of secret, never its value |
| A REST or MCP create, barrier update, unseal or quorum submission carrying a secret, redirected (307 or 308) to a host other than the one it was sent to | not sent — the SDK returns `sdk.BarrierSecretForeignHostError`, naming the transport, the kind of secret and both hosts, never its value. A redirect to the same host is followed | not applicable — gRPC and Unix do not redirect |
| Operating on a sealed database | 503 | `UNAVAILABLE` |
| Operating on a database whose drop is in flight | 409 | `ABORTED` — `database.DroppingError` |
| A second `DROP DATABASE` for the same database while one is already running on this node | 409 | `ABORTED` — `database.DropInProgressError` |
| A write that was accepted before a drop and reached the store after it | 409 | `ABORTED` — `database.DroppingError`; nothing was written |
| Any request naming a dropped database once the drop has removed its catalog record, on any node | 404 | `NOT_FOUND` — the database does not exist; the catalog is replicated, so every node releases it |
| `DROP DATABASE` of the default database (`system`) | 400 | `INVALID_ARGUMENT` — `database.DefaultDatabaseDropError`; it can never be dropped |
| A create or Raft update that names a shard ID (reachable only through a library call; no transport carries one) | 400 | `INVALID_ARGUMENT` — `database.CallerShardIDError` |
| A cluster-wide shard ID could not be claimed (store unavailable, sustained contention) | 503 | `UNAVAILABLE` — `database.ShardIDClaimError` / `shard.ClaimContentionError`; nothing was created, retry |
| A create that did not finish within the server's create deadline (10 s, or the caller's own deadline when that is sooner) -- a consensus step met no leader or no quorum | 503 | `UNAVAILABLE` — `service.DatabaseCreateTimeoutError`; everything the create had done is rolled back, retry. A caller whose own deadline ran out first gets 504 / `DEADLINE_EXCEEDED` |
| The replicated shard high-water mark is corrupt | 500 | `INTERNAL` — `shard.HighWaterCorruptError` |
| Invalid compaction policy (unknown mode, a field of another mode, `retainRevisions < 1`, `retainPeriod` unparseable or under `1s`) | 400 | `INVALID_ARGUMENT` — `compaction.InvalidPolicyError` |
| Compaction policy on another tenant's database (the system tenant included) | 404 | `NOT_FOUND` — indistinguishable from a missing database |
| Missing permission | 403 | `PERMISSION_DENIED` |
| A create carrying `engine` on REST, QUIC or MCP; gRPC/Unix have no field to set (OD-13) | 400 | `INVALID_ARGUMENT` |
| Target: `backend: raft` on a standalone node | 400 | `FAILED_PRECONDITION` — `database.RaftBackendUnavailableError` |
| Target: raft options on a non-raft backend | 400 | `INVALID_ARGUMENT` — `database.RaftOptionsRequireRaftBackendError` |
| Target: barrier on `backend: memory` | 400 | `FAILED_PRECONDITION` — `database.BarrierRequiresPersistentBackendError` |
| Target: `data_dir` outside a volume/raft tenant's root | 400 | `INVALID_ARGUMENT` — `database.DataDirOutsideTenantRootError` |
| Target: backend conflicts with the tenant's isolation (OD-7) | 400 | `FAILED_PRECONDITION` — `database.BackendIsolationConflictError` |
| Target: a database's binding cannot be opened | 503 | `UNAVAILABLE` — `database.BindingUnavailableError` |
| A non-replicated database held by another node, forwarded there, and the owner did not serve the request (OD-2) | 503 | `UNAVAILABLE` — `database.BindingOwnerUnavailableError`, naming the owner. QUIC and MCP return their Unavailable code; the PostgreSQL and MySQL wire protocols do not forward and return the `placement.NotAReplicaError` refusal, also Unavailable |
| Target: the cluster feature gate for a command is not yet open on every member | 503 | `UNAVAILABLE` — `feature.NotActiveError`, the one feature-gate refusal |

## Pagination

`ListDatabases` returns every database in the tenant and is not paginated; the
count is bounded by operator action.

## Versioning

Database REST paths are under `/v1/`. The target design's removal of
`connection` (OD-9) is breaking and ships in a MINOR release; there is no
deprecation period in the pre-1.0 series.
