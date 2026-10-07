# Configuration Reference

QRDB uses YAML-based configuration loaded at startup via the `--config` flag.
CLI flags override YAML values when both are provided. Omitted fields inherit
production-ready defaults from `DefaultConfig()`.

```bash
qrdb start --config /etc/qrdb/qrdb.yaml
```

---

## Complete Default Configuration

The following YAML shows every field with its default value. This is the
configuration produced by `config.DefaultConfig()` and is valid as-is for a
single-node deployment.

```yaml
# =============================================================================
# QRDB Configuration Reference - All Defaults
# =============================================================================

# Run mode: "standalone" (no raft — standard database operations plus the
# always-on security core) or "raft" (single- or multi-node consensus, the
# full feature set). Empty means "raft" for full backward compatibility.
# Under "standalone", raft.join or a multi-member raft.peers list is a
# validation error whose text names the remedy: "requires mode: raft".
# mvcc / watch / lease are served, and their enabled toggles valid, in both
# modes. See docs/run-modes.md for the three-gate
# feature model (build tag > run mode > config).
# NOTE: the qrdbd daemon refuses "standalone" until its raft-free
# composition branch lands (run-modes plan, phase 3); the SDK's
# node.NewStandaloneNode is the raft-free path today.
mode: "raft"

server:
  admin_port: 63002
  grpc_port: 63003
  host: "0.0.0.0"

protocols:
  rest: true
  grpc: true
  unix: true
  mcp: true

unix:
  socket_path: "/var/run/qrdbd/qrdbd.sock"

mcp:
  port: 63005
  endpoint: "/mcp"

raft:
  data_dir: "./data"
  deployment_id: 1
  shard_id: 1
  replica_id: 1
  raft_address: "127.0.0.1:63001"
  listen_address: ""
  peers: "1=127.0.0.1:63001"
  join: false
  snapshot_entries: 0
  compaction_overhead: 0
  max_in_mem_log_size: 0
  max_total_in_mem_log_size: 0
  sm_type: "pebble"
  # sm_storage: storage backend for the storage-generic state machines
  # (sm_type disk/fast): pebble (default), file, memory. Objstore is a
  # snapshot/backup tier and is refused as the hot apply path.
  # sm_storage: "pebble"

storage:
  backend: "pebble"
  cache_size: 0
  wal_dir: ""
  max_open_files: 0

tls:
  enabled: true
  ca_file: ""
  cert_file: ""
  key_file: ""
  # Leaving ca_file/cert_file/key_file empty auto-resolves TLS material
  # (generate for single-node, or enroll into an existing cluster); there is
  # no separate "auto_tls" toggle. See the "tls" section below.

logging:
  level: "info"
  format: "text"

discovery:
  method: "static"          # default: single-node; no network auto-join
  multicast_secret: ""      # no default PSK — required when method is "multicast"; min 16 bytes
  dns_domain: ""
  # token discovery (method: "token")
  token_endpoint: ""
  token: ""
  token_cluster_size: 0

mvcc:
  # enabled: narrows the feature in either mode. Omitted (null) means enabled.
  # enabled: true

watch:
  # enabled: same semantics as mvcc.enabled.
  # enabled: true
  buffer_size: 1024
  max_watchers: 0

lease:
  # enabled: same semantics as mvcc.enabled.
  # enabled: true
  min_ttl: 5
  max_ttl: 0
  check_interval: 1

index:
  max_indexes: 0

timeseries:
  default_retention: 0
  max_metrics: 0

auth:
  enabled: true
  admin_username: "admin"
  default_tenant: "system"
  cert_ttl: "8760h"
  perm_cache_ttl: 30
  # The window during which a REVOKED certificate still authenticates.
  revocation_cache_ttl: "30s"
  separation_of_duties: false
  # What to do when a GATING audit record cannot be written -- the
  # authorization decision AND the Security Officer's PIN verification, on this
  # one setting. "fail-closed" (default) refuses the request; "fail-open"
  # permits it and counts the gap. See the note below before changing it.
  audit_failure_policy: "fail-closed"
  # Whether EVERY certificate login must present an enrolled key ("required",
  # the default) or only accounts that enrolled one ("enrolled"). Under
  # "required" a node whose bootstrap admin has no certificate binding
  # still boots, but serves ONLY the ceremony status and admin-credential
  # endpoints ("awaiting admin credential") until one binds.
  mtls_binding_policy: "required"
  # How long an invitation (users:invite) stays redeemable. Positive, at
  # most 7 days -- a token redeemable longer than that is a standing
  # credential, not an invitation.
  invitation_ttl: "24h"
  # Refuse any invitation not pinned to the invited user's key. Off by
  # default; compliance profiles that must never issue a bearer-only token
  # turn it on.
  invitation_require_key_pin: false
  # Token-bucket throttles for invitation redemption -- the one verb any
  # caller may invoke with no credential. Zero fields take the defaults
  # (0.1/s burst 5 per source, 1/s burst 30 per tenant).
  invitation_redeem_limit:
    per_source:
      rate_per_second: 0.1
      burst: 5
    per_tenant:
      rate_per_second: 1
      burst: 30

seal:
  # auth.enabled (above) defaults to true and now REQUIRES seal.enabled:
  # true too -- an auth-enabled, seal-disabled node is refused at config
  # validation with a typed error. The bootstrap admin's
  # certificate binding depends on `qrdb init` being able to bring the
  # system barrier up at all, which does not exist when the seal is off.
  # A deployment that does not want encryption runs strategy "none" below
  # rather than omitting this section; see ../barrier-seal/user.md.
  enabled: true
  strategy: "none"

cors:
  allowed_origins: ["*"]
  allowed_headers: ["Authorization", "Content-Type"]
  max_age: 3600

transport:
  max_message_bytes: 4194304    # 4 MiB; applied pre-auth, identically on every transport
  max_conns_per_peer: 256       # REST, gRPC, MCP, and native+HTTP/3 QUIC listeners
  max_streams_per_conn: 128     # native QUIC transport only

import_session:
  max_session_bytes: 67108864    # 64 MiB total staged per session
  max_chunks_per_session: 16384
  max_sessions_per_tenant: 16
  session_ttl_seconds: 3600
```

---

## Configuration Sections

### server

Configures the QRDB server's network listeners.

| Field        | Type   | Default   | Required | CLI Flag       | Description                             |
|--------------|--------|-----------|----------|----------------|-----------------------------------------|
| `admin_port` | int    | `63002`   | yes      | `--admin-port` | TCP port for the REST/admin HTTP server |
| `grpc_port`  | int    | `63003`   | no       | `--grpc-port`  | TCP port for the gRPC data-plane service |
| `host`       | string | `0.0.0.0` | yes      | `--host`       | Bind host for every API listener        |
| `advertise_host` | string | host of `raft.raft_address` | no | `--advertise-host` | Host peers and clients dial to reach this node |

**Validation rules:**

- `admin_port` must be between 1 and 65535 (inclusive).
- `grpc_port` must be between 0 and 65535 (inclusive). Set to `0` to disable the gRPC listener.
- `host` must not be empty.

**Notes:**

- The `admin_port` serves both the REST API and operational endpoints (`/status`, `/health`, `/metrics`, `/peers`, `/drain`).
- When using non-static discovery methods (multicast, dns) without a config file, the server auto-selects free ports for `admin_port` and `grpc_port`.
- `host` binds **every** API listener: REST, the HTTP/3 (QUIC) listener that
  shares the REST port, gRPC, the native QUIC transport, MCP, MySQL and
  PostgreSQL. Settable by `server.host`, `QRDB_SERVER_HOST` or `--host` under
  the documented precedence. The HTTP/3 listener takes the host the REST
  listener actually bound; if the two ever land on different bind hosts the
  daemon returns `*rest.ListenerHostMismatchError` from start rather than
  serving the same handler on a wider surface than was asked for.
- `host` is a **bind** host and is commonly a wildcard no peer can dial;
  `advertise_host` is what this node publishes — into the cluster API-endpoint
  registry and as its own admin URL in the leader-forwarding peer registry.
  A node whose advertise host resolves to a wildcard publishes nothing rather
  than an unreachable address.

---

### bootstrap

Configures TLS bootstrap for cluster enrollment: how a joining node obtains the
cluster CA, and where the first node serves it from. Joining has two
independent halves — fetching the trust anchor (`noise_key`..`addr` below) and
submitting a CSR to an admitted member for signing (`join_addr`..`join_token_env`)
— both required for a join through enrollment to succeed; see
[docs/clustering/user.md](clustering/user.md#adding-a-node-member-side-csr-join).

| Field | Type | Default | CLI Flag | Description |
|---|---|---|---|---|
| `noise_key`   | string | `""` | `--bootstrap-noise-key`  | Hex Curve25519 public key of the first node's Noise server |
| `noise_addr`  | string | `""` | `--bootstrap-noise-addr` | `host:port` of the Noise server to fetch from |
| `noise_port`  | int    | `63006` | `--bootstrap-noise-port` | TCP port the first node's Noise server listens on |
| `noise_host`  | string | `server.host` | `--bootstrap-noise-host` | Bind host for the first node's Noise server |
| `spki_pin`    | string | `""` | `--bootstrap-spki-pin`   | Hex SHA-256 SPKI pin of the bootstrap server's TLS certificate |
| `dane_dns`    | string | `""` | `--bootstrap-dane-dns`   | DNS server for DANE TLSA lookup |
| `dane_dot`    | bool   | `false` | `--bootstrap-dane-dot`  | Use DNS-over-TLS for DANE lookups |
| `direct_url`  | string | `""` | `--bootstrap-direct-url` | Full HTTPS URL for direct CA bundle download |
| `addr`        | string | `""` | `--bootstrap-addr`       | `host:port` of the first node's admin server |
| `join_addr`       | string | `""` | `--bootstrap-join-addr`       | `host:port` of an admitted member's Noise_NK join listener (member-side CSR join) |
| `join_key`        | string | `""` | `--bootstrap-join-key`        | Hex Curve25519 public key of that join listener |
| `join_http_url`   | string | `""` | `--bootstrap-join-url`        | Base admin HTTPS URL of an admitted member, for the SPKI/Direct join transport |
| `join_token_file` | string | `""` | `--bootstrap-join-token-file` | Path to a file holding the join-admission secret |
| `join_token_env`  | string | `""` | `--bootstrap-join-token-env`  | Name of an environment variable holding the join-admission secret |

**Notes:**

- `noise_host` is a bind host in the same sense as `server.host`, and defaults
  to it. The enrollment listener is the one socket a joining node reaches before
  it holds any credential, so containing a node to an interface contains
  enrollment too; give it a different address only when enrollment should be
  reachable somewhere the APIs are not.
- When neither `bootstrap.addr` nor `direct_url` is set, the bootstrap server
  URL is derived from the first static peer (or `discovery.dns_domain`) plus
  `server.admin_port`. If that port is unset the derivation **fails with
  `*clusterenroll.MissingAdminPortError`** rather than completing the URL with a
  guessed port.
- `join_token_file` and `join_token_env` are mutually exclusive; configuring
  either `join_addr`/`join_key` or `join_http_url` without one of them fails
  config validation with a typed `*ValidationError` — the join-admission
  secret is never configured inline in YAML.
- `join_addr` requires `join_key` and vice versa; `join_http_url` needs neither.
  Both transports may be configured together — the Noise_NK transport is
  preferred when both are set.

---

### cluster_join

Configures this node to sign CSRs for joining nodes as an admitted,
unsealed member (the signing side of member-side CSR join; see `bootstrap`
above for the joining side).

| Field | Type | Default | CLI Flag | Description |
|---|---|---|---|---|
| `enabled`     | bool   | `false` | `--cluster-join-enabled`      | Run the Noise_NK join listener and register the SPKI/Direct `/v1/enroll/join` route |
| `listen_host` | string | `server.host` | `--cluster-join-listen-host` | Bind host for the Noise_NK join listener |
| `listen_port` | int    | `63007` | `--cluster-join-listen-port`  | TCP port for the Noise_NK join listener |
| `token_file`  | string | `""` | `--cluster-join-token-file`   | Path to a file holding the admission secret this node checks |
| `token_env`   | string | `""` | `--cluster-join-token-env`    | Name of an environment variable holding the admission secret |

**Notes:**

- `token_file` and `token_env` are mutually exclusive; `enabled: true` with
  neither configured fails config validation.
- The SPKI/Direct route needs no listener of its own: it is registered on the
  node's existing admin HTTPS listener, reachable only once the barrier is
  unsealed — the cluster CA (Key A) this node signs with is not available
  before that.

---

### protocols

Controls which API protocols are enabled.

| Field  | Type | Default | CLI Flag        | Description                     |
|--------|------|---------|-----------------|---------------------------------|
| `rest` | bool | `true`  | `--enable-rest` | Enable the HTTP/REST admin and KV API |
| `grpc` | bool | `true`  | `--enable-grpc` | Enable the gRPC KV service      |

**Notes:**

- At least one protocol should be enabled for client access.
- Disabling both protocols still allows the Raft consensus layer to operate, but the node will not accept client requests.

---

### raft

Configures the QuicRaft consensus layer.

| Field                       | Type   | Default             | Required | CLI Flag                        | Description                                                  |
|-----------------------------|--------|---------------------|----------|---------------------------------|--------------------------------------------------------------|
| `data_dir`                  | string | `./data`            | yes      | `--data-dir`                    | Base directory for Raft data files (WAL, snapshots, metadata) |
| `deployment_id`             | uint64 | `1`                 | no       | `--deployment-id`               | Isolates Raft clusters on the same network                   |
| `shard_id`                  | uint64 | `1`                 | yes      | `--shard-id`                    | Raft shard (group) identifier                                |
| `replica_id`                | uint64 | `1`                 | yes      | `--replica-id`                  | This node's replica identifier within the shard              |
| `raft_address`              | string | `127.0.0.1:63001`   | yes      | `--raft-address`                | Advertised address for Raft peer communication               |
| `listen_address`            | string | `""`                | no       | `--listen`                      | Bind address for Raft peer communication                     |
| `peers`                     | string | `1=127.0.0.1:63001` | no       | `--peers`                       | Comma-separated initial cluster members                      |
| `member_identities`         | string | `""`                | multi-node | `--member-identities`         | Comma-separated `replicaID=spki:<hex>` Raft identities of the other replicas |
| `join`                      | bool   | `false`             | no       | `--join`                        | Join an existing cluster instead of forming a new one         |
| `snapshot_entries`          | uint64 | `0`                 | no       | `--snapshot-entries`            | Applied entries between automatic snapshots                  |
| `compaction_overhead`       | uint64 | `0`                 | no       | `--compaction-overhead`         | Log entries to keep after a snapshot for follower catch-up    |
| `max_in_mem_log_size`       | uint64 | `0`                 | no       | `--max-in-mem-log-size`         | Per-shard in-memory Raft log size limit in bytes             |
| `max_total_in_mem_log_size` | uint64 | `0`                 | no       | `--max-total-in-mem-log-size`   | Total in-memory Raft log size across all shards in bytes     |
| `sm_type`                   | string | `pebble`            | no       | `--sm`                          | Raft state machine wrapper type                              |

**Validation rules:**

- `data_dir` must not be empty.
- `shard_id` must be greater than 0.
- `replica_id` must be greater than 0.
- `raft_address` must not be empty.
- `sm_type`, when non-empty, must be one of: `pebble`, `memory`, `mvcc`.

**Valid `sm_type` values:**

| Value    | Description                                              |
|----------|----------------------------------------------------------|
| `pebble` | PebbleDB-backed persistent disk state machine (default)  |
| `memory` | In-memory state machine (data lost on restart)           |
| `mvcc`   | MVCC versioned key-value engine with revision history    |

The `sm_type` field selects which Raft state machine wrapper to use for
distributed (Raft) mode. The state machine is a thin wrapper that delegates
data operations to a store engine (`pkg/engine/`). For local/embedded mode,
the `engine` and `backend` fields in the `databases` configuration select
the store engine (`kv` or `mvcc`) and storage backend (`memory`, `file`,
`pebble`) independently, without involving a Raft state machine.

**Peers format:**

The `peers` field uses a comma-separated `replicaID=address` format:

```yaml
peers: "1=10.0.0.1:63001,2=10.0.0.2:63001,3=10.0.0.3:63001"
```

**Member identities format:**

`member_identities` names, by replica ID, the Raft identity of every other
replica — `spki:` plus the hex SHA-256 of that node's certificate public key,
as printed by `qrdbd node-identity --cert FILE`:

```yaml
member_identities: "2=spki:3f9a…,3=spki:c01d…"
```

A founding node must name every member in `peers`; a joining node at least one
existing member. The node's own entry is derived from its certificate and may be
omitted. Ignored for a single-node cluster.

**Notes:**

- When `listen_address` is empty, `raft_address` is used as the bind address.
- Setting `snapshot_entries` to `0` disables automatic snapshots.
- Setting `compaction_overhead` to `0` uses the system default of 5000 entries.
- Setting `max_in_mem_log_size` or `max_total_in_mem_log_size` to `0` uses the system defaults.
- go-quicraft's own `TransportConfig` (stream pool sizing, snapshot receive
  limits, and the inbound message-receive budget) has no field here:
  `buildHostConfig` sets only the mTLS material, so those knobs run at
  go-quicraft's built-in defaults. See `docs/configuration/cli.md` for the
  0.18.0 message-receive-budget default change.
- With non-static discovery methods and no config file, `replica_id` is auto-generated from `crypto/rand` (48-bit) and `data_dir` is suffixed with the replica ID to avoid collisions.

---

### storage

Configures the underlying storage backend.

| Field            | Type  | Default  | Required | Description                                       |
|------------------|-------|----------|----------|---------------------------------------------------|
| `backend`        | string | `pebble` | no       | Storage engine selection                          |
| `cache_size`     | int64  | `0`      | no       | PebbleDB block cache size in bytes                |
| `wal_dir`        | string | `""`     | no       | Separate directory for the PebbleDB WAL           |
| `max_open_files` | int    | `0`      | no       | Maximum number of open files for PebbleDB         |

**Validation rules:**

- `backend`, when non-empty, must be one of: `pebble`, `memory`.
- `cache_size` must not be negative (>= 0).
- `max_open_files` must not be negative (>= 0).

**Valid `backend` values:**

| Value    | Description                                           |
|----------|-------------------------------------------------------|
| `pebble` | PebbleDB persistent storage (default, production use) |
| `memory` | In-memory storage (data lost on restart, testing use) |

**Notes:**

- Setting `cache_size` to `0` uses the PebbleDB default cache size.
- Setting `max_open_files` to `0` uses the PebbleDB default file limit.
- When `wal_dir` is empty, the WAL is stored in the default data directory. Placing the WAL on a separate disk can improve write throughput by isolating sequential WAL writes from random SSTable I/O.

---

### tls

Configures TLS for all network listeners (REST, gRPC, Raft).

| Field            | Type     | Default   | Required | CLI Flag          | Description                                       |
|------------------|----------|-----------|----------|-------------------|----------------------------------------------------|
| `enabled`        | bool     | `true`    | no       | N/A               | Enable TLS for all listeners                       |
| `ca_file`        | string   | `""`      | no       | `--tls-ca-file`   | Path to CA certificate PEM file                     |
| `cert_file`      | string   | `""`      | no       | `--tls-cert-file` | Path to TLS certificate PEM file                    |
| `key_file`       | string   | `""`      | no       | `--tls-key-file`  | Path to TLS private key PEM file                    |
| `client_auth`    | string   | `""`      | no       | `--tls-client-auth` | Client certificate requirement; see below        |
| `min_version`    | string   | `"TLS1.3"`| no       | N/A               | Minimum TLS version: `"TLS1.2"` or `"TLS1.3"`       |
| `additional_cas` | []string | `[]`      | no       | N/A               | Extra CA PEM files layered onto the client-CA pool  |

**There is no `auto_tls` field.** TLS material resolution is not a boolean —
it is `resolveTLS`'s ordered fallback, driven by what is and is not set:

1. `ca_file`, `cert_file` and `key_file` all set → loaded from those paths.
2. Otherwise, cached certificates in `raft.data_dir` from a previous start, if
   present → loaded from there.
3. Otherwise → generated (single node) or obtained by enrolling into an
   existing cluster (joining node), via `pkg/clusterenroll`.

Leaving all three paths empty is the common case (single node, or a node
enrolling into a cluster); setting all three is manual PKI. Setting only some
of the three is not a supported partial state.

**Validation rules:**

- `client_auth`, when non-empty, must be one of: `none`, `request`, `require`,
  `require_and_verify`. Empty derives the mode from whether `auth.enabled` is
  true (see [identity-rbac](identity-rbac/architecture.md)).
- `min_version`, when non-empty, must be one of: `TLS1.2`, `TLS1.3`.
- When `enabled` is `false`, no TLS validation is performed.

**Notes:**

- A generated or enrolled CA certificate is written under `raft.data_dir` so
  CLI commands (`qrdb status`, `qrdb peers`) can verify the server certificate
  on a later start.
- The generated/enrolled certificate supports both REST/gRPC client
  connections and mTLS on the QUIC transport for Raft peer communication.
- For production multi-node clusters, set `ca_file`/`cert_file`/`key_file`
  explicitly from a shared CA to enable mTLS between all cluster nodes.

---

### logging

Configures structured logging output.

| Field    | Type   | Default | CLI Flag       | Description                        |
|----------|--------|---------|-----------     |------------------------------------|
| `level`  | string | `info`  | `--log-level`  | Minimum log level                  |
| `format` | string | `text`  | `--log-format` | Log output format                  |

**Validation rules:**

- `level`, when non-empty, must be one of: `debug`, `info`, `warn`, `error`.
- `format`, when non-empty, must be one of: `text`, `json`.

**Valid `level` values:**

| Value   | Description                                              |
|---------|----------------------------------------------------------|
| `debug` | Verbose output including Raft protocol internals         |
| `info`  | Standard operational messages (default)                  |
| `warn`  | Warnings about potentially problematic conditions        |
| `error` | Only errors that require operator attention               |

**Valid `format` values:**

| Value  | Description                                       |
|--------|---------------------------------------------------|
| `text` | Human-readable key=value format (default)         |
| `json` | Structured JSON, suitable for log aggregation     |

**Notes:**

- The `--debug-raft` CLI flag forces the log level to `debug` regardless of the configured level.
- The logger is configured early in the startup sequence so all pre-host log lines use the requested format and level.

---

### discovery

Configures peer discovery for cluster formation. The discovery engine
(`pkg/discovery`) and bootstrap orchestration (`pkg/cluster`) are described in
detail in [discovery.md](clustering/architecture.md).

| Field                       | Type   | Default      | CLI Flag                   | Description                                          |
|-----------------------------|--------|--------------|----------------------------|------------------------------------------------------|
| `method`                    | string | `static`     | `--discovery`              | Discovery mechanism (default: single-node static)    |
| `multicast_secret`          | string | `""`         | `--multicast-secret`       | Shared PSK for multicast auth; **required** for multicast, no default, minimum 16 bytes |
| `multicast_group`           | string | `""`         | —                          | Override multicast address (host:port)               |
| `dns_domain`                | string | `""`         | `--dns-domain`             | DNS domain for DNS SRV peer discovery                |
| `dns_service`               | string | `_raft`      | —                          | SRV service label                                    |
| `dns_proto`                 | string | `_udp`       | —                          | SRV protocol label                                   |
| `token_endpoint`            | string | `""`         | `--discovery-endpoint`     | Token discovery service URL (token method)           |
| `token`                     | string | `""`         | `--discovery-token`        | Shared bootstrap token (token method)                |
| `token_cluster_size`        | int    | `0`          | `--discovery-cluster-size` | Expected cluster size for token discovery            |
| `allow_insecure_token_http` | bool   | `false`      | `--discovery-insecure-http`| Permit http:// token endpoints (dev/testing only)    |
| `min_peers`                 | int    | `0` (=3)     | `--cluster-min-peers`      | Minimum cluster size for bootstrap quorum + dynamic-membership removal guard |
| `dynamic_membership`        | bool   | `false`      | `--dynamic-membership`     | Enable opt-in lease/watch dynamic Raft membership (off by default)  |

**Validation rules:**

- `method`, when non-empty, must be one of: `static`, `multicast`, `dns`, `token`.

**Valid `method` values:**

| Value       | Description                                                            |
|-------------|------------------------------------------------------------------------|
| `static`    | Peers from `raft.peers` (default; empty/single entry = single node)    |
| `multicast` | Peers discover each other via authenticated multicast (opt-in, PSK req) |
| `dns`       | Peers are discovered via DNS SRV records                               |
| `token`     | Peers register with a shared-token HTTP discovery service (etcd-style) |

**Notes:**

- **Default is single-node `static`** with no network auto-join and no shared secret — the production-safe default. Multi-node discovery (multicast/dns/token) is an explicit opt-in.
- **Static discovery** requires the `raft.peers` field to contain the complete initial member list. A single entry (`1=127.0.0.1:63001`) creates a single-node cluster with transport disabled.
- **Multicast discovery** requires an operator-chosen `multicast_secret` shared across all nodes (the protocol is authenticated-only; there is deliberately **no well-known default** PSK). The secret must be at least 16 bytes: it is the HMAC-SHA256 key, and FIPS 140-3 (SP 800-107 §5.3.4) sets the floor for an HMAC key at 112 bits — a shorter secret is rejected at construction with a `ValidationError`, and would otherwise panic under `GODEBUG=fips140=only` (see [seal/fips140-mode.md](seal/fips140-mode.md)). The default minimum cluster size for bootstrap is 3 nodes.
- **DNS discovery** requires `dns_domain` to be set. It looks up SRV records at `{dns_service}.{dns_proto}.{dns_domain}` (default `_raft._udp.{dns_domain}`).
- **Token discovery** requires `token_endpoint` and `token`. Each node registers via the endpoint and polls until `token_cluster_size` (or `min_peers`) nodes have registered. The endpoint must use `https://` unless `allow_insecure_token_http` is set. The token is sent as an `Authorization: Bearer` header, never in the URL.
- When using multicast, dns, or token discovery without a config file, the server auto-generates `replica_id`, selects free ports, and appends the replica ID to `data_dir` to avoid collisions.

---

### mvcc

Configures the MVCC (Multi-Version Concurrency Control) versioned key-value engine. This section applies when `raft.sm_type` is set to `mvcc`.

| Field     | Type | Default | Description                                   |
|-----------|------|---------|-----------------------------------------------|
| `enabled` | bool | (raft)  | Raft-only toggle; omitted means enabled in raft mode |

There is no compaction setting. MVCC history is compacted per database, only
by the tenant that owns it: an explicit `Compact`, or the database's own
compaction policy. The node keyspace -- keys outside every tenant database --
is compacted only by the platform's policy, set with `qrdb admin
update-node-compaction` (system tenant only). Both policies are replicated
records set through the API, not configuration. *(Corrected: this section
documented `node_compaction`, which is no longer read.)* See
[key-value/engineering.md](key-value/engineering.md). *(Corrected: this section
documented `compaction_retention` and `revision_limit`; nothing read either
key, and both are removed.)*

**Features requiring `raft.sm_type: mvcc`:**

The following KV features are **only** available when using the MVCC state machine. The default `pebble` state machine does not retain revision history and refuses these operations with a clear error message:

- **As-of reads** (`mvcc:get_at_revision`) — read the value of a key at an earlier MVCC revision.
- **Watch replay** (`watch --start-revision`) — stream changes starting from a historical revision.
- **Selective restore** (`mvcc:restore`) — logical undo of a bounded key set to an earlier revision.

For these features, you must configure `raft.sm_type: mvcc` before the daemon starts. The state machine type cannot be changed on a running instance.

---

### watch

Configures the real-time watch notification system for key change events.

| Field          | Type | Default | Description                                         |
|----------------|------|---------|-----------------------------------------------------|
| `buffer_size`  | int  | `1024`  | Per-watcher event channel buffer size               |
| `max_watchers` | int  | `0`     | Maximum number of concurrent watchers allowed       |

**Validation rules:**

- `buffer_size` must not be negative (>= 0).
- `max_watchers` must not be negative (>= 0).

**Notes:**

- Larger `buffer_size` values reduce back-pressure on publishers at the cost of increased memory usage per watcher.
- Setting `max_watchers` to `0` allows unlimited concurrent watchers. In production, consider setting a limit to bound memory usage.

---

### lease

Configures the distributed lease management system.

| Field            | Type  | Default | Description                                             |
|------------------|-------|---------|---------------------------------------------------------|
| `min_ttl`        | int64 | `5`     | Minimum lease TTL in seconds                            |
| `max_ttl`        | int64 | `0`     | Maximum lease TTL in seconds                            |
| `check_interval` | int64 | `1`     | Interval in seconds between lease expiry checks         |

**Validation rules:**

- `min_ttl` must not be negative (>= 0).
- `max_ttl` must not be negative (>= 0).
- When `max_ttl` is greater than 0, `min_ttl` must not exceed `max_ttl`.
- `check_interval` must not be negative (>= 0).

**Notes:**

- Requests for TTLs shorter than `min_ttl` are clamped to `min_ttl`.
- Requests for TTLs longer than `max_ttl` are clamped to `max_ttl`.
- Setting `max_ttl` to `0` allows unlimited lease durations.
- Lower `check_interval` values improve expiry accuracy at the cost of CPU usage. A value of `1` second provides a good balance for most workloads.

---

### index

Configures the secondary indexing system.

| Field         | Type | Default | Description                                  |
|---------------|------|---------|----------------------------------------------|
| `max_indexes` | int  | `0`     | Maximum number of secondary indexes allowed  |

**Validation rules:**

- `max_indexes` must not be negative (>= 0).

**Notes:**

- Setting `max_indexes` to `0` allows unlimited secondary indexes.
- Each secondary index consumes additional storage and incurs write amplification. Set a limit appropriate for your workload.

---

### timeseries

Configures the time-series storage engine.

| Field               | Type  | Default | Description                                                |
|---------------------|-------|---------|------------------------------------------------------------|
| `default_retention` | int64 | `0`     | Default data retention period in seconds                   |
| `max_metrics`       | int64 | `0`     | Maximum number of distinct metric names allowed            |

**Validation rules:**

- `default_retention` must not be negative (>= 0).
- `max_metrics` must not be negative (>= 0).

**Notes:**

- Setting `default_retention` to `0` disables automatic data retention. Data points are retained indefinitely.
- Setting `max_metrics` to `0` allows unlimited distinct metric names.
- Data points older than `default_retention` seconds are eligible for automatic deletion.

---

### auth

Configures authentication and role-based access control (RBAC).

| Field              | Type   | Default     | Required | CLI Flag           | Description                                  |
|--------------------|--------|-------------|----------|--------------------|----------------------------------------------|
| `enabled`          | bool   | `true`      | no       | `--auth-enabled`   | Enable authentication and RBAC               |
| `admin_username`   | string | `"admin"`   | no       | N/A                | Default admin username (certificate CN)      |
| `default_tenant`   | string | `"system"`  | no       | N/A                | Bootstrap admin tenant ID (certificate OU)   |
| `cert_ttl`         | string | `"8760h"`   | no       | N/A                | Default certificate TTL (1 year)             |
| `perm_cache_ttl`   | int    | `30`        | no       | N/A                | Permission cache TTL in seconds              |
| `revocation_cache_ttl` | string | `"30s"` | no       | N/A                | **The window during which a revoked certificate still authenticates.** See the note below |
| `separation_of_duties` | bool | `false`   | no       | N/A                | Enforce strict FIPS/PCI role separation      |
| `audit_failure_policy` | string | `"fail-closed"` | no | N/A         | **What happens when a gating decision cannot be audited** — the authorization decision and the SO PIN verification alike. See the note below |
| `spiffe_trust_domain` | string | `""`       | no       | N/A                | SPIFFE trust domain the mTLS authenticator **pins**, and the one the built-in CA mints client-certificate tenant SANs under. See the note below |
| `mtls_binding_policy` | string | `"required"` | no     | N/A                | **Whether every certificate login must present an enrolled key** (`"required"`, the default) or only accounts that enrolled one are held to it (`"enrolled"`). See the note below |
| `invitation_ttl`   | string | `"24h"`     | no       | N/A                | How long a `users:invite` token stays redeemable. Positive, at most 7 days |
| `invitation_require_key_pin` | bool | `false` | no   | N/A                | Refuse any invitation not pinned to the invited user's key            |
| `invitation_redeem_limit` | object | see below | no  | N/A                | `per_source`/`per_tenant` token-bucket throttles for invitation redemption, each `{rate_per_second, burst}` |

**Validation rules:**

- `enabled: true` requires `seal.enabled: true`: the bootstrap
  admin's certificate binding depends on `qrdb init` being able to bring
  the system barrier up at all, which does not exist when the seal is off.
  Refused with a typed `*ValidationError` on `seal.enabled` naming the fix
  (`seal.strategy: none` for a deployment that wants no encryption). See
  [../barrier-seal/user.md](../barrier-seal/user.md).
- `admin_username` must not be empty when `enabled` is `true`.
- `default_tenant` must not be empty when `enabled` is `true`.
- `cert_ttl` must be a valid Go duration string (e.g., `"8760h"`, `"720h"`).
- `perm_cache_ttl` must not be negative (>= 0).
- `revocation_cache_ttl` must be a valid, non-negative Go duration string
  (e.g. `"30s"`, `"0"`). An unparseable value fails the boot rather than
  reverting to the default: it is a security window, and a value silently
  different from the configured one is worse than a refusal to start.
- `spiffe_trust_domain`, when set, must survive being written into a SPIFFE ID
  and parsed back unchanged (no `/`, `?`, `#`, `@` or whitespace). The
  validation error quotes the rejected value (`spiffe.TrustDomainError`).
- `mtls_binding_policy` must be `"enrolled"`, `"required"`, or empty (empty
  means `"required"`, so "omitted" cannot drift from the secure-by-default
  posture). An unrecognised value fails configuration validation — and, for
  a caller that builds its configuration without validating it, the boot —
  rather than resolving to either posture.
- `audit_failure_policy` must be `"fail-closed"`, `"fail-open"`, or empty
  (empty means `"fail-closed"`). An unrecognised value fails the boot rather
  than resolving to either posture: guessing fail-closed denies a fleet the
  operator meant to keep serving, and guessing fail-open weakens the security
  posture on a typo.
- `invitation_ttl` must be a positive Go duration string of at most 7 days
  (168h); empty means `"24h"`.
- `invitation_redeem_limit.{per_source,per_tenant}.{rate_per_second,burst}`
  must each be non-negative and finite; a zero field takes its documented
  default rather than disabling the bucket.

**Notes:**

- When `enabled` is `false`, all requests are treated as unauthenticated and all authorization checks are bypassed. `enabled: false` is the only way to run with `seal.enabled: false` too (see the `seal` section below) -- an auth-enabled node requires the seal.
- The `admin_username` is used as the CN for the bootstrap admin's certificate, issued and bound through the `qrdb init` ceremony (see [../firstboot/user.md](firstboot/user.md)) rather than auto-generated by the daemon itself.
- The `default_tenant` is used as the OU for the admin certificate, establishing the bootstrap admin's membership in the structurally privileged `system` tenant.
- The `perm_cache_ttl` controls how long resolved permissions are cached per user in the RBAC authorizer's `sync.Map`. Setting to `0` disables caching.
- **`revocation_cache_ttl` is a security window, not a performance knob.** Certificate revocation is enforced on the authentication path of every transport, on every request. To keep that off the replicated store in the overwhelmingly common not-revoked case, a NOT-REVOKED answer is cached for this duration — which makes it the maximum time a certificate that has *just been revoked* continues to authenticate on a node that had already seen it. Setting it to `0` closes the window entirely, at the cost of one key-value read per authenticated request. A REVOKED answer is cached without expiry and is unaffected, because revocation in go-qrdb is a one-way door (nothing un-revokes). See [certificate-authority/architecture.md](certificate-authority/architecture.md).
- **`audit_failure_policy` is a compliance setting, not a tuning knob.** qrdb
  audits every authorization decision. When the audit sink cannot write the
  record, the default posture **refuses the request** (HTTP 503 / gRPC
  `UNAVAILABLE`), because an unrecorded grant is precisely the event audit
  exists to prevent. Setting `"fail-open"` makes the node serve requests it
  cannot record: the trail acquires a gap whose size is exactly
  `qrdb_auth_audit_write_failures_total{outcome="permitted"}`, and every
  startup logs a `WARN` naming the property given up. It is not a fallback —
  nothing degrades to it automatically and nothing retries and then permits.
  For FedRAMP / PCI DSS / HIPAA, running `"fail-open"` is a reportable
  condition. See [audit/architecture.md](audit/architecture.md) and
  [audit/user.md](audit/user.md).
- **It governs the Security Officer's second factor too, and it is the ONLY key
  that does.** The authorization decision and the SO PIN verification are two
  distinct assertions about the same request; both refuse an unrecordable
  outcome under `"fail-closed"`, and `"fail-open"` gives up both at once. Their
  gaps are sized by `qrdb_auth_audit_write_failures_total{outcome="permitted"}`
  and `qrdb_audit_gate_failures_total{component,outcome="permitted"}`
  respectively. There is deliberately no `auth.so_audit_failure_policy`: two
  keys would let a deployment end up recording one assertion and not the other.
  See [seal/so-dual-factor.md](seal/so-dual-factor.md).
- **`spiffe_trust_domain` pins tenant derivation and shapes the certificates
  the built-in CA issues.** When set, the mTLS authenticator derives a
  caller's tenant from the certificate's tenant-scoped SPIFFE URI SAN
  (`spiffe://<trust-domain>/tenant/<tenant>/<kind>/<name>`) and refuses one
  under any other trust domain, fail-closed; and the CA adds
  `spiffe://<trust-domain>/tenant/<tenant>/user/<username>` to every client
  certificate it mints, so the pin covers this deployment's own certificates.
  The CA refuses to mint one whose SAN would not parse back to the tenant and
  username in its Subject (a `/` in either). The value itself must be a bare
  host name that round-trips through a SPIFFE ID; anything else fails the load. When unset, certificates carry no
  SAN and the tenant is read from the Subject OU. Set the same value on every
  node. See
  [certificate-authority/architecture.md](certificate-authority/architecture.md#which-derivation-a-generated-certificate-uses).
- **`mtls_binding_policy: "required"` (the default) closes the
  unenrolled-account hole and is a lockout risk.** Under `"enrolled"`, an
  account that has enrolled a certificate binding admits only that key, and
  an account that has not admits any certificate from the CA whose CN and
  tenant name it. `"required"` refuses the second kind — and a certificate
  that names no account — with 401. A node still **boots** under
  `"required"` when the bootstrap admin does not hold an unexpired binding
  (which is why `qrdb init` -- see [../firstboot/user.md](firstboot/user.md)
  -- issues and binds one in the same call that brings the system barrier
  up), but comes up in "awaiting admin credential," serving only the two
  verbs that can repair it, rather than admitting nobody able to.
  To relax to `"enrolled"` temporarily (e.g. while migrating), set it
  explicitly; roll back by removing it or setting `"required"`. Set the
  same value on every node. See [identity-rbac/architecture.md](identity-rbac/architecture.md#the-fleet-wide-posture-authmtls_binding_policy)
  and [identity-rbac/user.md](identity-rbac/user.md#moving-a-fleet-to-required-certificate-enrolment).
- `separation_of_duties` selects the RBAC posture:
  - **`false` (default, standard):** the bootstrap `admin` is also granted the `owner` role (full operational authority — `permission.All()`), so a single identity runs the entire database. This is the convenient posture for commercial deployments with no regulatory separation-of-duties requirement.
  - **`true` (strict):** the canonical six-role FIPS 140-3 / PCI-DSS least-privilege model is enforced — `admin` is control-plane only, the data plane requires the `operator` role, governance requires the `SO` role, and the SO→crypto deny-list applies. The `owner` role is neither seeded nor assigned.
  - This setting only **raises** strictness. A binary built in FIPS 140-3 mode (`GOFIPS140=latest`, i.e. `crypto/fips140.Enabled()` is true) — or run with `GODEBUG=fips140=on` — is **always** strict regardless of this flag, so a FIPS deployment cannot be relaxed into the standard posture. The `owner` role is additionally voided by the RBAC authorizer under strict mode as defense-in-depth.

---

### seal

Configures the storage barrier that encrypts data at rest. See
[barrier-seal/user.md](barrier-seal/user.md) for the full operational guide
(choosing a strategy, first run, unsealing, M-of-N, migrating to encryption
in place) and [firstboot/user.md](firstboot/user.md) for how `qrdb init`
brings the barrier up.

| Field | Type | Default | Required | CLI Flag | Description |
|---|---|---|---|---|---|
| `enabled` | bool | `true` | no | N/A | Enable the storage barrier. **Required when `auth.enabled` is `true`** — see Validation rules. |
| `strategy` | string | `"none"` | no | `--barrier-strategy` (`qrdb init`, `qrdb barrier init-shamir`) | `"none"` \| `"passphrase"` \| `"shamir"` \| a hardware strategy ID registered by a go-xkms composition. Any other value, including `"software"`, is refused. |
| `root_key_path` | string | `"__sys:seal/root_key"` | no | N/A | Storage key the sealed root-key blob is persisted under. |
| `shamir_threshold` | int | — | when `strategy: shamir` | `--threshold` | Minimum shares required to unseal; `>= 2` and `<= shamir_total`. |
| `shamir_total` | int | — | when `strategy: shamir` | `--shares` / `--total` | Total Shamir shares to generate. |
| `share_ttl` | string | `"15m"` | no | N/A (env `QRDB_SEAL_SHARE_TTL`) | How long an unseal-share quorum session stays open before submitted shares expire. |
| `credential_strategy` | string | `"barrier"` | no | N/A | `"manual"` \| `"barrier"` — where named credentials (SO/user PINs, rotated credentials) are stored. |

**Validation rules:**

- `enabled: false` while `auth.enabled` is `true` (the default for both)
  fails configuration validation with a typed `*ValidationError` on
  `seal.enabled`, naming `seal.strategy: none` as the fix: the bootstrap
  admin's certificate binding depends on `qrdb init` being able to bring
  the system barrier up at all, which does not exist when the seal is off.
  Set `auth.enabled: false` too for a node that genuinely wants neither.
- `strategy` must be one of the accepted values (or the `"software"`
  alias) when `enabled` is `true`; an unrecognised value fails the boot.
- `shamir_threshold`/`shamir_total` are validated only when `strategy` is
  `"shamir"`.

**Notes:**

- A fresh node's system barrier is brought up ONLY by `qrdb init`, which
  runs in the sealed window before anything else is unsealed and issues
  the bootstrap admin's credential in the same call.
  `qrdb barrier init` targeting the system barrier is refused with a typed
  error pointing back to `qrdb init`; it remains how a tenant admin
  initializes their own tenant barrier (`qrdb tenant barrier-init`) after
  the system is bootstrapped.
- `strategy: "none"` is a barrier-present-but-unencrypted strategy (the
  at-rest store is wrapped in zero-epoch/plaintext format), not the same as
  `enabled: false` (no barrier object at all). `"none"` is the canonical
  starting state for later migrating to a keyed strategy in place via
  `qrdb barrier enable-encryption`; `enabled: false` has no such path.

---

### audit

> **There is no `audit:` block, and there is no `audit.Config`.** This section
> described one — `enabled`, `kv_backend`, `file_backend`, `file_path`, and a
> KV-backed sink under `__sys:audit/` — and none of those keys is read by any
> code. Setting them has no effect. The description predates the sink rework;
> it is corrected here rather than left in place.

Audit logging is **always on** and is composed at the node's wiring site, not
configured. Every node builds three sinks behind one fan-out:

| Sink | Destination |
|------|-------------|
| `ChainFileLogger` | `{data_dir}/audit-events.ndjson` — append-only NDJSON, SHA-256 hash chain, mode `0600`, size/daily rotation, 90-day retention |
| `RingBuffer` | the last 10,000 events, in memory, for `qrdb audit query` |
| `SlogSink` | mirrored to the operational log stream |

There is no master switch: FedRAMP, PCI DSS and HIPAA require a trail even
where RBAC is disabled.

The one audit-related setting is [`auth.audit_failure_policy`](#auth) — what
the authorization middleware does when a decision's audit record cannot be
written. See [audit/architecture.md](audit/architecture.md),
[audit/engineering.md](audit/engineering.md) and
[audit/user.md](audit/user.md).

---

### cors

Configures Cross-Origin Resource Sharing headers for the REST API.

| Field             | Type     | Default                              | Required | Description                            |
|-------------------|----------|--------------------------------------|----------|----------------------------------------|
| `allowed_origins` | []string | `["*"]`                              | no       | Allowed origins for CORS               |
| `allowed_headers` | []string | `["Authorization", "Content-Type"]`  | no       | Allowed request headers                |
| `max_age`         | int      | `3600`                               | no       | Preflight cache duration in seconds    |

**Validation rules:**

- `allowed_origins` must not be empty when CORS is configured.
- `max_age` must not be negative (>= 0).

**Notes:**

- Setting `allowed_origins` to `["*"]` allows all origins. For production, restrict to specific domains.
- The `max_age` value controls how long browsers cache preflight (OPTIONS) responses. A value of `3600` (1 hour) is suitable for most deployments.
- CORS headers are only added when the `cors` section is present in the configuration. Omitting the section entirely disables CORS.

---

### databases

Configures named databases. Each entry defines an isolated data store with
its own backend and optional per-database wire protocols. *(Corrected: there
is no per-database `engine` field. Lane 1C deleted it outright -- every
database serves every compiled-in engine, OD-12/OD-13 -- and a config file
still carrying `engine` is refused by the strict decode below, not ignored.)*

| Field        | Type   | Default | Required | Description                                          |
|--------------|--------|---------|----------|-------------------------------------------------------|
| `name`       | string | --      | yes      | Unique database name                                  |
| `backend`    | string | --      | yes      | Storage backend: `memory`, `file`, `pebble`, `raft`, or `objstore` |
| `mysql`      | bool   | `false` | no       | Enable the MySQL wire protocol for this database       |
| `postgresql` | bool   | `false` | no       | Enable the PostgreSQL wire protocol for this database  |
| `etcd`       | object | --      | no       | etcd v3 gRPC protocol configuration; see `EtcdConfig` below |
| `sql`        | object | --      | no       | Per-database SQL settings; see `DatabaseSQLConfig` below |

`mysql`, `postgresql`, `etcd` and `sql` sit directly on the database entry —
flat, there is no `protocol` wrapper field. (Corrected 2026-10-03: earlier
versions of this section showed a nested `protocol:` object with `mysql`
and `postgresql` themselves as nested objects; `config.DatabaseConfig` has
no `protocol` field, `MySQL`/`PostgreSQL` are plain booleans, `Etcd` is a
pointer directly on the entry, and a nested block here is silently dropped
by the YAML decoder rather than applied.) `mysql`/`postgresql` replaced the
former top-level `SQLConfig`.

**`DatabaseSQLConfig`** (the `sql` field):

| Field         | Type   | Default       | Description                                         |
|---------------|--------|---------------|------------------------------------------------------|
| `concurrency` | string | `"optimistic"`| Row-locking mode: `"optimistic"` or `"pessimistic"`. See [row-locking](row-locking/architecture.md). |

#### EtcdConfig

Configures the etcd-compatible gRPC endpoint for a database. Unlike `mysql`
and `postgresql`, which are plain booleans, `etcd` is an object (it needs a
listen address and advertise URLs) — but it is still flat on the database
entry, not nested under anything else.

| Field                   | Type     | Default  | Required | Description                                      |
|-------------------------|----------|----------|----------|--------------------------------------------------|
| `enabled`               | bool     | `false`  | no       | Enable etcd v3 gRPC endpoint for this database   |
| `listen_addr`           | string   | `:2379`  | no       | Listen address for the etcd gRPC server          |
| `advertise_client_urls` | []string | --       | no       | URLs advertised to clients for endpoint discovery |

**Validation rules:**

- `name` must not be empty and must be unique across `databases`.
- `backend` must be one of: `memory`, `file`, `pebble`, `raft`, `objstore`.
- `mysql` or `postgresql`, when `true` on any database, require TLS and auth
  enabled at the server level (`tls.enabled`, `auth.enabled`).
- `etcd.enabled`, when `true`, requires TLS and auth enabled at the server
  level (there is no longer an `engine` constraint to check); a binary without
  the `qrdb_etcd` build tag refuses it at boot (`service.RefuseEtcdNotCompiled`),
  checked over every configured database before any of them provisions.
  See [etcd-compatibility](etcd-compatibility/architecture.md) for the full
  contract, including the CLI/API verbs (`qrdb database create --etcd` /
  `update-etcd`) that set it without a restart.
- `sql.concurrency`, when set, must be `"optimistic"` or `"pessimistic"`.

**Example:**

```yaml
databases:
  - name: app
    backend: pebble
    mysql: true
    sql:
      concurrency: pessimistic
  - name: k8s-prod
    backend: raft
    etcd:
      enabled: true
      listen_addr: ":2379"
      advertise_client_urls:
        - "https://node1:2379"
  - name: cache
    backend: memory
```

**Notes:**

- The top-level `sql:` section (below) configures settings shared by every SQL
  connection (`default_database`, `max_connections`, `query_timeout`,
  `wait_timeout`, `write_timeout`); it does not enable a protocol. A database's own `mysql`/`postgresql` fields are what
  turn the wire protocols on, and `protocols.mysql`/`protocols.postgresql`
  must also be enabled server-wide (and compiled in — see
  `ValidateCapabilities`).

---

### embedding

Configures the embedding service for vector generation. When disabled (no providers
configured), all embedding API calls return `EmbeddingProviderNotFoundError`.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `default_provider` | string | `""` | Name of the provider used when none is specified in a request |
| `cache_capacity` | int | `0` | Max embeddings in LRU cache; 0 disables caching |
| `providers` | []EmbeddingProviderConfig | `[]` | List of configured embedding provider endpoints |

#### EmbeddingProviderConfig

Configures a single OpenAI-compatible embedding provider endpoint.

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `name` | string | -- | yes | Unique provider identifier within the registry |
| `base_url` | string | -- | yes | OpenAI-compatible API base URL (e.g., `http://localhost:11434/v1`) |
| `api_key` | string | `""` | no | Bearer token for authentication; leave empty for unauthenticated endpoints |
| `model` | string | -- | yes | Default embedding model name used when request does not specify one |
| `timeout` | string | `"30s"` | no | Per-request timeout in Go duration format (e.g., `"60s"`) |
| `max_retries` | int | `3` | no | Retry attempts for transient failures (5xx responses, network errors) |

**Validation rules:**

- Each provider name must be unique.
- `base_url` must be a valid URL.
- `model` must not be empty.

**Example configuration with Ollama:**

```yaml
embedding:
  default_provider: "ollama"
  cache_capacity: 10000
  providers:
    - name: "ollama"
      base_url: "http://localhost:11434/v1"
      model: "mxbai-embed-large"
      timeout: "30s"
      max_retries: 3
```

**Notes:**

- Caching is optional and improves performance by avoiding duplicate embedding requests.
- Environment variable substitution is supported: `api_key: "${OPENAI_API_KEY}"`.
- The embedding service is required by the RAG pipeline.

---

### rag

Configures the Retrieval-Augmented Generation (RAG) pipeline for document ingestion
and semantic retrieval.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `default_chunk_strategy` | string | `"fixed"` | Chunking strategy: `"fixed"`, `"sentence"`, or `"paragraph"` |
| `default_chunk_size` | int | `512` | Target chunk size in characters |
| `default_overlap` | int | `50` | Overlapping characters/sentences between adjacent chunks |
| `max_document_size` | int64 | `0` | Max document size in bytes; 0 = unlimited |
| `default_top_k` | int | `10` | Default number of retrieval results when caller does not specify |

**Chunking strategies:**

| Strategy | Description | Use Case |
|----------|-------------|----------|
| `fixed` | Fixed-size chunks with character overlap | Prose, unstructured text (default) |
| `sentence` | Chunks split on sentence boundaries | Preserving semantic units |
| `paragraph` | Chunks split on paragraph boundaries | Essays, articles with clear paragraph structure |

**Example configuration:**

```yaml
rag:
  default_chunk_strategy: "fixed"
  default_chunk_size: 512
  default_overlap: 50
  max_document_size: 0
  default_top_k: 10
```

**Validation rules:**

- `default_chunk_strategy` must be one of: `"fixed"`, `"sentence"`, `"paragraph"`.
- `default_chunk_size` must be greater than 0.
- `default_overlap` must be non-negative.
- `max_document_size` of 0 means unlimited.

**Notes:**

- RAG pipeline uses the embedding service to generate vectors for documents and queries.
- Chunk size significantly impacts retrieval quality; 256-1024 characters is typical.
- Overlap prevents losing context at chunk boundaries; 50+ characters is recommended.
- Document size limits protect against memory exhaustion from very large documents.

---

### tenant

Configures multi-tenant default quotas and entitlements. See
[docs/multi-tenancy/](multi-tenancy/architecture.md) for the full isolation
model.

**Vector namespace creation and the cluster isolation-policy floor:**

There is no `tenant.strict_isolation` configuration key — it was removed
(go-qrdb v0.45) because it governed only vector namespace creation, which made
it dangerously misleading for a compliance reader looking for the tenant
isolation posture. Its behavior is folded into the real, cluster-wide
isolation-policy floor
([docs/multi-tenancy/architecture.md#the-isolation-policy](multi-tenancy/architecture.md#the-isolation-policy)):

- **Cluster floor is `strict`** (including a FIPS 140-3 build, which always
  tightens the floor to `strict`): vector namespace creation requires the
  authenticated identity to carry a non-system tenant. An identity resolving
  to the `system` tenant is rejected with `403 Forbidden` (`tenant required`).
- **Cluster floor is `operator-access`** (today's behavior, and the default
  for any deployment that has not set the floor): un-tenanted identities pool
  into `system`. This is a single-tenant convenience, not cross-tenant
  isolation — it does not prevent an identity from accidentally writing to
  another tenant's namespace if it somehow resolves to the same tenant.

**Notes:**

- The tenant is derived from the authenticated identity, not a request header (see
  [scoping.md](scoping.md)).
- Vector namespaces are tenant-partitioned in both modes; strict mode additionally
  refuses to create a namespace for an identity that has no real tenant (i.e. the
  `system` tenant), preventing accidental shared/un-owned namespaces.

---

### transport

Bounds resources every API transport (REST, gRPC, the Unix domain socket,
native QUIC, MCP) admits to an unauthenticated TLS peer, before any identity
is established. These apply identically on every transport; there is no
larger-message opt-in.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `max_message_bytes` | int | `4194304` (4 MiB) | Caps a single request or response message any transport accepts or produces: the REST request body, the gRPC and Unix `MaxRecvMsgSize`, the QUIC wire frame payload (checked against this bound before a buffer is allocated for it), and the MCP JSON-RPC request body |
| `max_conns_per_peer` | int | `256` | Caps the number of concurrent connections a single remote IP may hold open against REST's HTTP/2 listener, gRPC, MCP, native QUIC, and REST's own HTTP/3 (QUIC) listener. A negative value means unlimited |
| `max_streams_per_conn` | int | `128` | Caps the number of concurrent streams a single QUIC connection may have open against the native QUIC transport. A negative value means unlimited |

**Example configuration:**

```yaml
transport:
  max_message_bytes: 4194304
  max_conns_per_peer: 256
  max_streams_per_conn: 128
```

**Notes:**

- A caller with a payload larger than `max_message_bytes` splits it across
  multiple calls; there is no larger-message exception for any RPC,
  including bulk import.
- `max_conns_per_peer` has no effect on the Unix domain socket (no
  remote-IP concept). On native QUIC and REST's HTTP/3 listener it bounds
  concurrent CONNECTIONS per peer, the same as on the TCP transports;
  `max_streams_per_conn` separately bounds concurrent STREAMS within a
  single admitted QUIC connection.
- Zero resolves each field to its default; a negative `max_message_bytes`
  is refused at config validation rather than silently disabling the cap.

---

### import_session

Bounds a chunked bulk-import session (`ImportBegin`/`ImportAppend`/
`ImportCommit`/`ImportAbort`, see `docs/transactions/`), independently of
the per-call `transport.max_message_bytes` cap every individual chunk is
also subject to.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `max_session_bytes` | int64 | `67108864` (64 MiB) | Total staged bytes a single session may accumulate across all its chunks |
| `max_chunks_per_session` | int | `16384` | Chunk-count ceiling per session |
| `max_sessions_per_tenant` | int | `16` | Concurrent (not yet committed, aborted, or expired) sessions a tenant may hold open at once |
| `session_ttl_seconds` | int64 | `3600` | The session's lease TTL in seconds; renewed on every `ImportAppend`, so only an abandoned session ever expires |

**Example configuration:**

```yaml
import_session:
  max_session_bytes: 67108864
  max_chunks_per_session: 16384
  max_sessions_per_tenant: 16
  session_ttl_seconds: 3600
```

**Notes:**

- Zero or negative resolves each field to its default — there is no way to
  configure an unbounded session.
- A session over `max_session_bytes` or `max_chunks_per_session` is
  refused at the `ImportAppend` that would exceed it; the chunks already
  staged are not discarded, so a caller that lowered its chunk size can
  still commit what fits.
- An abandoned session (the caller never calls `ImportCommit` or
  `ImportAbort`) is reclaimed when `session_ttl_seconds` elapses, through
  the same lease-expiry mechanism as any other lease-bound key — see
  `docs/leases/`.

---

## Configuration Loading

### Load Order

0. The YAML file is decoded **strictly** before anything else: a key that
   names no field in the `Config` schema fails the load as
   `*UnknownFieldError`, naming the key's full path (e.g.
   `databases[2].protocol`) and, where known, the file and line.
1. `DefaultConfig()` initializes all fields with production-ready defaults.
2. The YAML file (if provided via `--config`) is unmarshaled on top of the defaults. Only fields present in the YAML file override the defaults.
3. CLI flags that are explicitly set override the corresponding YAML values.

### Validation

After loading and merging, the configuration is validated by `Config.Validate()`.
Validation checks every field and returns the first error encountered as a
`*ValidationError` containing the field path and reason:

```
config: validation failed for "raft.shard_id": must be greater than 0
```

### Error Types

| Error Type           | Returned When                                              |
|-----------------------|-------------------------------------------------------------|
| `*LoadError`          | The YAML file cannot be read or parsed                      |
| `*UnknownFieldError`  | The YAML file carries a key that names no field in the schema |
| `*ValidationError`    | A configuration field has an invalid value                   |

All three error types implement the `error` interface. `LoadError` also
implements `Unwrap()` for use with `errors.Is` and `errors.As`.

A key for a feature this binary was not compiled with (e.g. `protocols.mysql`
without build tag `qrdb_mysql`) is never an `*UnknownFieldError` -- `pkg/config`
declares every field unconditionally regardless of build tags. It is refused
instead by the separate `Config.ValidateCapabilities()`, naming the missing
tag.

---

## Example Configurations

### Minimal Single-Node

The simplest deployment. Uses in-memory storage, static discovery, auto-generated
TLS, and disables the QUIC transport (no remote peers).

```yaml
server:
  admin_port: 63002
  grpc_port: 63003
  host: "0.0.0.0"

raft:
  data_dir: "./data"
  shard_id: 1
  replica_id: 1
  raft_address: "127.0.0.1:63001"
  sm_type: "memory"

storage:
  backend: "memory"

tls:
  enabled: true  # ca_file/cert_file/key_file omitted: auto-generated

logging:
  level: "debug"
  format: "text"

discovery:
  method: "static"
```

### 3-Node Cluster with Static Discovery

A production cluster with three nodes using PebbleDB storage, manual TLS
certificates, and static peer discovery.

**Node 1** (`/etc/qrdb/qrdb.yaml` on `10.0.0.1`):

```yaml
server:
  admin_port: 63002
  grpc_port: 63003
  host: "0.0.0.0"

protocols:
  rest: true
  grpc: true

raft:
  data_dir: "/var/lib/qrdb"
  deployment_id: 1
  shard_id: 1
  replica_id: 1
  raft_address: "10.0.0.1:63001"
  listen_address: "0.0.0.0:63001"
  peers: "1=10.0.0.1:63001,2=10.0.0.2:63001,3=10.0.0.3:63001"
  # Identities of the other replicas: qrdbd node-identity --cert <their cert>
  member_identities: "2=spki:<node-2 identity>,3=spki:<node-3 identity>"
  snapshot_entries: 10000
  compaction_overhead: 5000
  sm_type: "pebble"
  # sm_storage: storage backend for the storage-generic state machines
  # (sm_type disk/fast): pebble (default), file, memory. Objstore is a
  # snapshot/backup tier and is refused as the hot apply path.
  # sm_storage: "pebble"

storage:
  backend: "pebble"
  cache_size: 1073741824  # 1 GB
  wal_dir: "/var/lib/qrdb/wal"
  max_open_files: 4096

tls:
  enabled: true
  ca_file: "/etc/qrdb/tls/ca.pem"
  cert_file: "/etc/qrdb/tls/node1.pem"
  key_file: "/etc/qrdb/tls/node1-key.pem"

logging:
  level: "info"
  format: "json"

discovery:
  method: "static"
```

**Node 2** (`/etc/qrdb/qrdb.yaml` on `10.0.0.2`):

```yaml
server:
  admin_port: 63002
  grpc_port: 63003
  host: "0.0.0.0"

raft:
  data_dir: "/var/lib/qrdb"
  deployment_id: 1
  shard_id: 1
  replica_id: 2
  raft_address: "10.0.0.2:63001"
  listen_address: "0.0.0.0:63001"
  peers: "1=10.0.0.1:63001,2=10.0.0.2:63001,3=10.0.0.3:63001"
  # Identities of the other replicas: qrdbd node-identity --cert <their cert>
  member_identities: "1=spki:<node-1 identity>,3=spki:<node-3 identity>"
  snapshot_entries: 10000
  compaction_overhead: 5000
  sm_type: "pebble"
  # sm_storage: storage backend for the storage-generic state machines
  # (sm_type disk/fast): pebble (default), file, memory. Objstore is a
  # snapshot/backup tier and is refused as the hot apply path.
  # sm_storage: "pebble"

storage:
  backend: "pebble"
  cache_size: 1073741824
  wal_dir: "/var/lib/qrdb/wal"
  max_open_files: 4096

tls:
  enabled: true
  ca_file: "/etc/qrdb/tls/ca.pem"
  cert_file: "/etc/qrdb/tls/node2.pem"
  key_file: "/etc/qrdb/tls/node2-key.pem"

logging:
  level: "info"
  format: "json"

discovery:
  method: "static"
```

**Node 3** (`/etc/qrdb/qrdb.yaml` on `10.0.0.3`):

```yaml
server:
  admin_port: 63002
  grpc_port: 63003
  host: "0.0.0.0"

raft:
  data_dir: "/var/lib/qrdb"
  deployment_id: 1
  shard_id: 1
  replica_id: 3
  raft_address: "10.0.0.3:63001"
  listen_address: "0.0.0.0:63001"
  peers: "1=10.0.0.1:63001,2=10.0.0.2:63001,3=10.0.0.3:63001"
  # Identities of the other replicas: qrdbd node-identity --cert <their cert>
  member_identities: "1=spki:<node-1 identity>,2=spki:<node-2 identity>"
  snapshot_entries: 10000
  compaction_overhead: 5000
  sm_type: "pebble"
  # sm_storage: storage backend for the storage-generic state machines
  # (sm_type disk/fast): pebble (default), file, memory. Objstore is a
  # snapshot/backup tier and is refused as the hot apply path.
  # sm_storage: "pebble"

storage:
  backend: "pebble"
  cache_size: 1073741824
  wal_dir: "/var/lib/qrdb/wal"
  max_open_files: 4096

tls:
  enabled: true
  ca_file: "/etc/qrdb/tls/ca.pem"
  cert_file: "/etc/qrdb/tls/node3.pem"
  key_file: "/etc/qrdb/tls/node3-key.pem"

logging:
  level: "info"
  format: "json"

discovery:
  method: "static"
```

### High-Throughput MVCC with Watches and Leases

A configuration tuned for high-throughput MVCC workloads with real-time watch
notifications, lease management, secondary indexes, and time-series data.

```yaml
server:
  admin_port: 63002
  grpc_port: 63003
  host: "0.0.0.0"

protocols:
  rest: true
  grpc: true

raft:
  data_dir: "/var/lib/qrdb"
  deployment_id: 1
  shard_id: 1
  replica_id: 1
  raft_address: "10.0.0.1:63001"
  listen_address: "0.0.0.0:63001"
  peers: "1=10.0.0.1:63001,2=10.0.0.2:63001,3=10.0.0.3:63001"
  # Identities of the other replicas: qrdbd node-identity --cert <their cert>
  member_identities: "2=spki:<node-2 identity>,3=spki:<node-3 identity>"
  snapshot_entries: 50000
  compaction_overhead: 10000
  max_in_mem_log_size: 10485760       # 10 MB per shard
  max_total_in_mem_log_size: 104857600 # 100 MB total
  sm_type: "mvcc"

storage:
  backend: "pebble"
  cache_size: 4294967296   # 4 GB block cache
  wal_dir: "/mnt/nvme/qrdb-wal"
  max_open_files: 16384

tls:
  enabled: true
  ca_file: "/etc/qrdb/tls/ca.pem"
  cert_file: "/etc/qrdb/tls/node.pem"
  key_file: "/etc/qrdb/tls/node-key.pem"

logging:
  level: "warn"
  format: "json"

discovery:
  method: "static"

watch:
  buffer_size: 4096
  max_watchers: 10000

lease:
  min_ttl: 5
  max_ttl: 86400          # 24 hours
  check_interval: 1

index:
  max_indexes: 64

timeseries:
  default_retention: 604800  # 7 days
  max_metrics: 10000
```

**Tuning notes for this configuration:**

- **`raft.snapshot_entries: 50000`** -- Takes a snapshot every 50k entries to bound log replay time during recovery.
- **`raft.compaction_overhead: 10000`** -- Retains 10k entries after snapshot for slow followers to catch up without requiring a full snapshot transfer.
- **`raft.max_in_mem_log_size: 10485760`** -- Limits per-shard in-memory log to 10 MB, enabling rate limiting under sustained write pressure.
- **`storage.cache_size: 4294967296`** -- Allocates 4 GB to the PebbleDB block cache to minimize disk reads for hot keys.
- **`storage.wal_dir: "/mnt/nvme/qrdb-wal"`** -- Isolates WAL writes on a dedicated NVMe device to eliminate I/O contention with SSTable compaction.
- **`storage.max_open_files: 16384`** -- Raises the file descriptor limit to handle large LSM trees without thrashing.
- **`watch.buffer_size: 4096`** -- Large event buffers reduce back-pressure on the watch publisher under bursty write loads.
- **`watch.max_watchers: 10000`** -- Caps concurrent watchers to bound memory allocation.
- **`lease.max_ttl: 86400`** -- Prevents clients from acquiring leases longer than 24 hours.
- **`timeseries.default_retention: 604800`** -- Automatically expires time-series data points older than 7 days.

---

## CLI Flag to YAML Field Mapping

When both a YAML config file and CLI flags are provided, CLI flags take
precedence. The following table maps CLI flags to their YAML equivalents.

| CLI Flag                        | YAML Path                          |
|---------------------------------|------------------------------------|
| `--config`                      | N/A (specifies the YAML file path) |
| `--admin-port`                  | `server.admin_port`                |
| `--grpc-port`                   | `server.grpc_port`                 |
| `--enable-rest`                 | `protocols.rest`                   |
| `--enable-grpc`                 | `protocols.grpc`                   |
| `--data-dir`                    | `raft.data_dir`                    |
| `--deployment-id`               | `raft.deployment_id`               |
| `--shard-id`                    | `raft.shard_id`                    |
| `--replica-id`                  | `raft.replica_id`                  |
| `--raft-address`                | `raft.raft_address`                |
| `--listen`                      | `raft.listen_address`              |
| `--peers`                       | `raft.peers`                       |
| `--join`                        | `raft.join`                        |
| `--snapshot-entries`            | `raft.snapshot_entries`            |
| `--compaction-overhead`         | `raft.compaction_overhead`         |
| `--max-in-mem-log-size`         | `raft.max_in_mem_log_size`         |
| `--max-total-in-mem-log-size`   | `raft.max_total_in_mem_log_size`   |
| `--sm`                          | `raft.sm_type`                     |
| `--tls-ca-file`                 | `tls.ca_file`                      |
| `--tls-cert-file`               | `tls.cert_file`                    |
| `--tls-key-file`                | `tls.key_file`                     |
| `--log-level`                   | `logging.level`                    |
| `--log-format`                  | `logging.format`                   |
| `--discovery`                   | `discovery.method`                 |
| `--multicast-secret`            | `discovery.multicast_secret`       |
| `--dns-domain`                  | `discovery.dns_domain`             |
| `--debug-raft`                  | N/A (forces `logging.level: debug`)|
| `--auth-enabled`                | `auth.enabled`                     |

There is no `--audit-enabled` flag or `audit.enabled` key -- audit logging is
always on; see the [audit](#audit) section above.

---

## Port Summary

| Service | Default Port | Protocol        | Configuration Field  |
|---------|-------------|-----------------|----------------------|
| Raft    | 63001       | QUIC (internal) | `raft.raft_address`  |
| REST    | 63002       | HTTPS           | `server.admin_port`  |
| gRPC    | 63003       | gRPC + TLS      | `server.grpc_port`   |
| etcd    | 2379        | gRPC + mTLS     | `databases[].etcd.listen_addr` |
