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
# Under "standalone", explicitly enabling a raft-only feature
# (mvcc.enabled / watch.enabled / lease.enabled: true), raft.join, or a
# multi-member raft.peers list is a validation error whose text names the
# remedy: "requires mode: raft". See docs/run-modes.md for the three-gate
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
  auto_tls: true

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
  # enabled: raft-only toggle. Omitted (null) means enabled in raft mode.
  # Explicit true under mode "standalone" fails validation.
  # enabled: true
  compaction_retention: 1000
  revision_limit: 0

watch:
  # enabled: raft-only toggle, same semantics as mvcc.enabled.
  # enabled: true
  buffer_size: 1024
  max_watchers: 0

lease:
  # enabled: raft-only toggle, same semantics as mvcc.enabled.
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
  # What to do when a decision's audit record cannot be written.
  # "fail-closed" (default) refuses the request; "fail-open" permits it and
  # counts the gap. See the note below before changing it.
  audit_failure_policy: "fail-closed"

cors:
  allowed_origins: ["*"]
  allowed_headers: ["Authorization", "Content-Type"]
  max_age: 3600
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
cluster CA, and where the first node serves it from.

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

**Notes:**

- When `listen_address` is empty, `raft_address` is used as the bind address.
- Setting `snapshot_entries` to `0` disables automatic snapshots.
- Setting `compaction_overhead` to `0` uses the system default of 5000 entries.
- Setting `max_in_mem_log_size` or `max_total_in_mem_log_size` to `0` uses the system defaults.
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

| Field       | Type   | Default | Required                          | CLI Flag         | Description                                  |
|-------------|--------|---------|-----------------------------------|------------------|----------------------------------------------|
| `enabled`   | bool   | `true`  | no                                | N/A              | Enable TLS for all listeners                 |
| `ca_file`   | string | `""`    | yes (when `enabled && !auto_tls`) | `--tls-ca-file`  | Path to CA certificate PEM file              |
| `cert_file` | string | `""`    | yes (when `enabled && !auto_tls`) | `--tls-cert-file`| Path to TLS certificate PEM file             |
| `key_file`  | string | `""`    | yes (when `enabled && !auto_tls`) | `--tls-key-file` | Path to TLS private key PEM file             |
| `auto_tls`  | bool   | `true`  | no                                | N/A              | Auto-generate CA and certificates at startup |

**Validation rules:**

- When `enabled` is `true` and `auto_tls` is `false`, all three fields `ca_file`, `cert_file`, and `key_file` are required.
- When `auto_tls` is `true`, certificate fields may be empty; a CA and leaf certificate are generated automatically.
- When `enabled` is `false`, no TLS validation is performed.

**TLS modes:**

| Mode              | Configuration                                         | Use Case                      |
|-------------------|-------------------------------------------------------|-------------------------------|
| Auto TLS          | `enabled: true`, `auto_tls: true`                     | Development, single-node      |
| Manual TLS        | `enabled: true`, `auto_tls: false`, paths set         | Production with PKI           |
| TLS disabled      | `enabled: false`                                      | Testing behind a TLS proxy    |

**Notes:**

- When `auto_tls` is enabled, the generated CA certificate is written to `{data_dir}/ca.pem` so CLI commands (`qrdb status`, `qrdb peers`) can verify the server certificate.
- The auto-generated certificates use the CA to sign a leaf certificate for the local node. This supports both REST/gRPC client connections and mTLS on the QUIC transport for Raft peer communication.
- For production multi-node clusters, use manual TLS with a shared CA to enable mTLS between all cluster nodes.

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

| Field                  | Type  | Default | Description                                                |
|------------------------|-------|---------|------------------------------------------------------------|
| `compaction_retention` | int64 | `1000`  | Minimum number of revisions to retain after compaction      |
| `revision_limit`       | int64 | `0`     | Maximum historical revisions to retain per key              |

**Validation rules:**

- `compaction_retention` must not be negative (>= 0).
- `revision_limit` must not be negative (>= 0).

**Notes:**

- Setting `compaction_retention` to `0` disables automatic compaction retention.
- Setting `revision_limit` to `0` means unlimited revisions are retained per key.
- The `compaction_retention` value determines how many revisions survive a compaction pass. For example, with a retention of `1000`, at least the most recent 1000 revisions across all keys are preserved.

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
| `audit_failure_policy` | string | `"fail-closed"` | no | N/A         | **What happens when an authorization decision cannot be audited.** See the note below |

**Validation rules:**

- `admin_username` must not be empty when `enabled` is `true`.
- `default_tenant` must not be empty when `enabled` is `true`.
- `cert_ttl` must be a valid Go duration string (e.g., `"8760h"`, `"720h"`).
- `perm_cache_ttl` must not be negative (>= 0).
- `revocation_cache_ttl` must be a valid, non-negative Go duration string
  (e.g. `"30s"`, `"0"`). An unparseable value fails the boot rather than
  reverting to the default: it is a security window, and a value silently
  different from the configured one is worse than a refusal to start.
- `audit_failure_policy` must be `"fail-closed"`, `"fail-open"`, or empty
  (empty means `"fail-closed"`). An unrecognised value fails the boot rather
  than resolving to either posture: guessing fail-closed denies a fleet the
  operator meant to keep serving, and guessing fail-open weakens the security
  posture on a typo.

**Notes:**

- When `enabled` is `false`, all requests are treated as unauthenticated and all authorization checks are bypassed.
- The `admin_username` is used as the CN for the auto-generated admin client certificate at first start.
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
- `separation_of_duties` selects the RBAC posture:
  - **`false` (default, standard):** the bootstrap `admin` is also granted the `owner` role (full operational authority — `permission.All()`), so a single identity runs the entire database. This is the convenient posture for commercial deployments with no regulatory separation-of-duties requirement.
  - **`true` (strict):** the canonical six-role FIPS 140-3 / PCI-DSS least-privilege model is enforced — `admin` is control-plane only, the data plane requires the `operator` role, governance requires the `SO` role, and the SO→crypto deny-list applies. The `owner` role is neither seeded nor assigned.
  - This setting only **raises** strictness. A binary built in FIPS 140-3 mode (`GOFIPS140=latest`, i.e. `crypto/fips140.Enabled()` is true) — or run with `GODEBUG=fips140=on` — is **always** strict regardless of this flag, so a FIPS deployment cannot be relaxed into the standard posture. The `owner` role is additionally voided by the RBAC authorizer under strict mode as defense-in-depth.

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

Configures named databases with per-database protocol support. Each database
entry defines an isolated data store with its own engine, backend, and optional
protocol endpoints.

| Field      | Type   | Default | Required | Description                           |
|------------|--------|---------|----------|---------------------------------------|
| `name`     | string | --      | yes      | Unique database name                  |
| `engine`   | string | `kv`    | yes      | Store engine (`kv` or `mvcc`)         |
| `backend`  | string | `raft`  | yes      | Replication backend (`raft`)          |
| `protocol` | object | --      | no       | Per-database protocol configuration   |

#### ProtocolConfig

The `protocol` field within each database entry configures wire protocol
endpoints. This replaced the former top-level `SQLConfig` and now supports
`mysql`, `postgresql`, and `etcd` alongside each other.

| Field        | Type   | Default | Description                         |
|--------------|--------|---------|-------------------------------------|
| `mysql`      | object | --      | MySQL wire protocol configuration   |
| `postgresql` | object | --      | PostgreSQL wire protocol configuration |
| `etcd`       | object | --      | etcd v3 gRPC protocol configuration |

#### EtcdConfig

Configures the etcd-compatible gRPC endpoint for a database.

| Field                   | Type     | Default  | Required | Description                                      |
|-------------------------|----------|----------|----------|--------------------------------------------------|
| `enabled`               | bool     | `false`  | no       | Enable etcd v3 gRPC endpoint for this database   |
| `listen_addr`           | string   | `:2379`  | no       | Listen address for the etcd gRPC server          |
| `advertise_client_urls` | []string | --       | no       | URLs advertised to clients for endpoint discovery |

**Validation rules:**

- When `enabled` is `true`, the database `engine` must be `kv` and `backend` must be `raft`.
- When `enabled` is `true`, TLS and auth must be enabled at the server level.
- `listen_addr` must be a valid `host:port` or `:port` format.

**Example:**

```yaml
databases:
  - name: k8s-prod
    engine: kv
    backend: raft
    protocol:
      etcd:
        enabled: true
        listen_addr: ":2379"
        advertise_client_urls:
          - "https://node1:2379"
      mysql:
        enabled: false
      postgresql:
        enabled: false
```

**Notes:**

- Each database with `etcd: enabled` spawns its own gRPC server on the specified `listen_addr`.
- Multiple databases can each have etcd enabled on different listen addresses.
- The etcd endpoint is fully isolated per database: revision counters, watches, leases, and encryption barriers are scoped to the database's shard.
- See [etcd documentation](etcd-compatibility/architecture.md) for Kubernetes integration and client examples.

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

Configures multi-tenant isolation behavior, particularly for vector namespaces and
strict mode enforcement.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `strict_isolation` | bool | `false` | Reject vector namespace creation when the authenticated identity carries no (non-system) tenant |

**Strict Mode Behavior:**

When `strict_isolation: true`:

1. **Vector namespace creation** requires the authenticated identity to carry a
   non-system tenant. An identity resolving to the `system` tenant is rejected with
   `403 Forbidden` (`tenant required`).
2. **System tenant assumption** is disabled — there is no implicit fallback to the `system`
   tenant. The tenant comes from the authenticated mTLS identity (see [scoping.md](scoping.md)),
   so creation must be performed by an identity that belongs to a real tenant.
3. **Use cases**: Multi-tenant SaaS deployments, regulatory environments (HIPAA, PCI-DSS)
   where implicit data ownership is unacceptable.

**Standard Mode (default, strict_isolation: false):**

When `strict_isolation: false`:

1. **Un-tenanted identities pool into `system`.** An identity with no explicit tenant
   resolves to the structurally privileged `system` tenant. Vector namespaces created
   by such identities are owned by `system`.
2. **This is NOT cross-tenant isolation.** Standard mode is a single-tenant convenience
   that makes un-owned and system-owned namespaces indistinguishable. It does not
   prevent an identity from accidentally writing to another tenant's namespace if it
   somehow resolves to the same tenant; it does not audit or gate implicit system-tenant
   ownership; and it emits a startup WARN log to make the posture visible.
3. **Suitable for** single-tenant deployments, development environments, or applications
   where all callers are trusted and tenant partitioning is not a security boundary.
4. **Not suitable for** multi-tenant SaaS, HIPAA, PCI-DSS, or any deployment where
   namespace ownership must be enforced per authenticated identity.

**FIPS 140-3 Enforcement:**

When the binary is compiled in FIPS 140-3 mode (`GOFIPS140=latest` or `GODEBUG=fips140=on`),
strict isolation is **always** enabled regardless of this configuration flag. A FIPS-compiled
process cannot be relaxed into standard mode and will always reject un-tenanted namespace
operations.

**Example configuration:**

```yaml
tenant:
  strict_isolation: false  # (default) use system tenant for un-tenanted requests

# OR for strict mode:

tenant:
  strict_isolation: true   # reject un-tenanted requests
```

**Notes:**

- The tenant is derived from the authenticated identity, not a request header (see
  [scoping.md](scoping.md)).
- Vector namespaces are tenant-partitioned in both modes; strict mode additionally
  refuses to create a namespace for an identity that has no real tenant (i.e. the
  `system` tenant), preventing accidental shared/un-owned namespaces.

---

## Configuration Loading

### Load Order

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

| Error Type        | Returned When                                      |
|-------------------|-----------------------------------------------------|
| `*LoadError`      | The YAML file cannot be read or parsed              |
| `*ValidationError`| A configuration field has an invalid value          |

Both error types implement the `error` interface. `LoadError` also implements
`Unwrap()` for use with `errors.Is` and `errors.As`.

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
  enabled: true
  auto_tls: true

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
  auto_tls: false
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
  auto_tls: false
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
  auto_tls: false
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
  auto_tls: false
  ca_file: "/etc/qrdb/tls/ca.pem"
  cert_file: "/etc/qrdb/tls/node.pem"
  key_file: "/etc/qrdb/tls/node-key.pem"

logging:
  level: "warn"
  format: "json"

discovery:
  method: "static"

mvcc:
  compaction_retention: 5000
  revision_limit: 100

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
- **`mvcc.compaction_retention: 5000`** -- Retains 5000 revisions after compaction, balancing history depth with storage efficiency.
- **`mvcc.revision_limit: 100`** -- Caps per-key revision history to 100 entries, preventing unbounded growth for frequently updated keys.
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
| `--audit-enabled`               | `audit.enabled`                    |

---

## Port Summary

| Service | Default Port | Protocol        | Configuration Field  |
|---------|-------------|-----------------|----------------------|
| Raft    | 63001       | QUIC (internal) | `raft.raft_address`  |
| REST    | 63002       | HTTPS           | `server.admin_port`  |
| gRPC    | 63003       | gRPC + TLS      | `server.grpc_port`   |
| etcd    | 2379        | gRPC + mTLS     | `databases[].protocol.etcd.listen_addr` |
