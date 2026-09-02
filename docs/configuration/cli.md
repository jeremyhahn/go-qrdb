# Configuration — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Configuration is a **daemon** concern. `qrdbd` reads it; the `qrdb` client has
no verb that changes it, only `admin capabilities` to report what took effect.

## Loading — `qrdbd --config`

```bash
qrdbd start --config /etc/qrdb/qrdb.yaml
```

Four layers, in order:

1. built-in defaults,
2. the YAML file — **only fields present in it** override the defaults,
3. `QRDB_`-prefixed environment variables — any key, named by its path
   upper-cased with separators as underscores (`server.host` →
   `QRDB_SERVER_HOST`, `seal.shamir_total` → `QRDB_SEAL_SHAMIR_TOTAL`),
4. CLI flags **explicitly set** — a flag left at its own default does not
   overwrite what the layers below said.

Layer 3 is applied by the config loader, so it needs `--config` to point at a
file. Without one, only defaults and flags apply.

## Flag to YAML mapping

| CLI flag | YAML path |
|---|---|
| `--config` | *(names the YAML file)* |
| `--host` | `server.host` |
| `--admin-port` | `server.admin_port` |
| `--grpc-port` | `server.grpc_port` |
| `--enable-rest` | `protocols.rest` |
| `--enable-grpc` | `protocols.grpc` |
| `--data-dir` | `raft.data_dir` |
| `--deployment-id` | `raft.deployment_id` |
| `--shard-id` | `raft.shard_id` |
| `--replica-id` | `raft.replica_id` |
| `--raft-address` | `raft.raft_address` |
| `--listen` | `raft.listen_address` |
| `--peers` | `raft.peers` |
| `--join` | `raft.join` |
| `--snapshot-entries` | `raft.snapshot_entries` |
| `--compaction-overhead` | `raft.compaction_overhead` |
| `--max-in-mem-log-size` | `raft.max_in_mem_log_size` |
| `--max-total-in-mem-log-size` | `raft.max_total_in_mem_log_size` |
| `--sm` | `raft.sm_type` |
| `--tls-ca-file` | `tls.ca_file` |
| `--tls-cert-file` | `tls.cert_file` |
| `--tls-key-file` | `tls.key_file` |
| `--log-level` | `logging.level` |
| `--log-format` | `logging.format` |
| `--discovery` | `discovery.method` |
| `--bootstrap-noise-host` | `bootstrap.noise_host` |
| `--bootstrap-noise-port` | `bootstrap.noise_port` |
| `--multicast-secret` | `discovery.multicast_secret` |
| `--dns-domain` | `discovery.dns_domain` |
| `--auth-enabled` | `auth.enabled` |
| `--audit-enabled` | `audit.enabled` |
| `--debug-raft` | *(forces `logging.level: debug`)* |

Feature-specific flags are documented in their own folders: `--mode`
([../run-modes/cli.md](../run-modes/cli.md)), `--enable-mysql` /
`--enable-pgsql` / `--sql-database` ([../sql/cli.md](../sql/cli.md)),
`--dynamic-membership` / `--cluster-min-peers`
([../clustering/cli.md](../clustering/cli.md)),
`--threshold` / `--total` ([../firstboot/cli.md](../firstboot/cli.md)).

## Environment variables

| Variable | Equivalent |
|---|---|
| `QRDB_SO_PIN` | `--so-pin` |
| `QRDB_USER_PIN` | `--user-pin` |
| `QRDB_BARRIER_STRATEGY` | `--barrier-strategy` |
| `QRDB_SHARES` | `--shares` |
| `QRDB_THRESHOLD` | `--threshold` |
| `QRDB_HOSTNAME` | `--hostname` |
| `QRDB_SANS` | `--sans` |
| `QRDB_SEAL_SHARE_TTL` | `seal.share_ttl` |
| `QRDB_SERVER_HOST` | `server.host` |
| `QRDB_BOOTSTRAP_NOISE_HOST` | `bootstrap.noise_host` |

The last three are examples of the general rule, not a closed list: with
`--config` in play, **any** config key can be overridden by
`QRDB_<PATH_UPPER_CASED>`. The rows above `QRDB_SEAL_SHARE_TTL` are different —
they are command-scoped bindings that do not go through the config file.

## Bind host

`server.host` is the bind host for **every** API listener: REST (and the HTTP/3
listener that shares its port), gRPC, native QUIC, MCP, MySQL and PostgreSQL.
Setting it to a specific address is a containment decision and it applies to all
of them; the HTTP/3 listener binds the host the REST listener actually got, and
the daemon refuses to start if the two ever land on different bind hosts rather
than serving the same handler on a wider surface than was asked for.

```bash
# every listener on loopback only
qrdbd start --config /etc/qrdb/qrdb.yaml --host 127.0.0.1
```

`server.host` is a **bind** host. The address peers and clients dial is
`server.advertise_host` (`--advertise-host`), which also supplies the admin URL
this node publishes for itself in the leader-forward peer registry; it defaults
to the host portion of `--raft-address`.

The Noise enrollment listener follows `server.host` unless
`bootstrap.noise_host` says otherwise.

## Confirming what took effect

```bash
qrdb admin capabilities --output json
```

This reports the **sealed** profile — build tags ∩ run mode ∩ config — plus the
`mode`. It is more reliable than reading the file back, because it is what the
running server actually resolved.

## Default ports

| Service | Port | Field |
|---|---|---|
| Raft | 63001 | `raft.raft_address` |
| REST | 63002 | `server.admin_port` |
| gRPC | 63003 | `server.grpc_port` |
| QUIC | 63004 | — |
| MySQL | 3306 | `server.mysql_port` |
| PostgreSQL | 5432 | `server.pgsql_port` |
| etcd | 2379 | `databases[].protocol.etcd.listen_addr` |

## The full reference

Every section, every field, every default, plus worked example configurations:
[12-configuration.md](../12-configuration.md).

## Parity

Nothing in this feature has a `CommandDefinition` — there is no `qrdb` verb.
`qrdbd`'s flags are exercised by the daemon's own tests.
