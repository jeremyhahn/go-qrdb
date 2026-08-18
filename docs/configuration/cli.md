# Configuration — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Configuration is a **daemon** concern. `qrdbd` reads it; the `qrdb` client has
no verb that changes it, only `admin capabilities` to report what took effect.

## Loading — `qrdbd --config`

```bash
qrdbd start --config /etc/qrdb/qrdb.yaml
```

Three layers, in order:

1. built-in defaults,
2. the YAML file — **only fields present in it** override the defaults,
3. CLI flags **explicitly set** — a flag left at its own default does not
   overwrite a YAML value.

## Flag to YAML mapping

| CLI flag | YAML path |
|---|---|
| `--config` | *(names the YAML file)* |
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
