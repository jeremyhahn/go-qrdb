# Configuration — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-23
> Conflicts with this document are defects. Fix them here first.

Test reference: `make integration-test-configuration`; coverage row: `test/integration/COVERAGE.md` `[configuration](../../docs/configuration/)`.

Configuring `qrdbd`, and confirming that what you wrote is what is running.

## Prerequisites

Access to the daemon's config file and the ability to restart it. Configuration
is read at boot — there is no runtime reconfiguration.

## How settings are resolved

Four layers, in this order:

1. **Built-in defaults** — every field starts with a production-ready value.
2. **Your YAML** — only the fields you actually write override the defaults.
   Omitting a field leaves the default; it does not zero it.
3. **`QRDB_` environment variables** — any field, named by its path upper-cased
   with separators as underscores: `server.host` is `QRDB_SERVER_HOST`.
4. **CLI flags you explicitly set** — a flag left at its own default will not
   overwrite what your YAML or environment said.

```bash
qrdbd start --config /etc/qrdb/qrdb.yaml
```

Layer 3 is read while the config file is loaded, so it needs `--config`. If you
start the daemon with no config file, only the defaults and your flags apply —
an otherwise empty file is enough to turn the environment layer on.

## A minimal file

```yaml
mode: raft
raft:
  data_dir: /var/lib/qrdb
storage:
  backend: pebble
tls:
  enabled: true
  ca_file: /etc/qrdb/ca.pem
  cert_file: /etc/qrdb/server.pem
  key_file: /etc/qrdb/server-key.pem
auth:
  enabled: true
```

There is no `audit:` block to write — audit logging is always on and is not
configured; see [../audit/](../audit/architecture.md).

Complete worked examples — minimal single node, a 3-node cluster with static
discovery, and a high-throughput MVCC setup with watches and leases — are in
[12-configuration.md](../12-configuration.md).

## Confirming it took effect

```bash
qrdb admin capabilities --output json
```

That reports what the running server actually resolved: build tags intersected
with the run mode intersected with your config, plus the mode itself. It is a
better answer than re-reading the file.

## Keeping secrets out of the file

`qrdbd`'s own file has no field that takes a secret reference, and an unknown
key such as a made-up `some_secret_field` refuses the file. `credential://` and
`literal://` references are for an application that embeds qrdb: it resolves
them in its own configuration at boot, and a field it declares protected stops
the boot when its reference cannot be resolved. See
[../credentials/user.md](../credentials/user.md).

## Choosing where the node listens

`server.host` is the bind host for every API listener — REST, its HTTP/3
sibling on the same port, gRPC, QUIC, MCP, MySQL and PostgreSQL. It defaults to
`0.0.0.0`, which answers on every interface.

To contain a node to one interface:

```yaml
server:
  host: 127.0.0.1
```

or `QRDB_SERVER_HOST=127.0.0.1`, or `--host 127.0.0.1`. It applies to all of
them at once, HTTP/3 included: the QUIC listener binds the host the REST
listener got, and the daemon refuses to start rather than serve the same API on
a wider surface than you asked for.

Two hosts that are easy to confuse:

| Field | Means | Default |
|---|---|---|
| `server.host` | where this node **binds** | `0.0.0.0` |
| `server.advertise_host` | the address peers and clients **dial** to reach it | host portion of `raft.raft_address` |

Set `advertise_host` when the API is reached over a different address than the
Raft plane (NAT, split networking). It is also the host this node publishes for
itself in the leader-forwarding peer registry, so a wrong value there sends
peers' forwarded writes to the wrong place. A node whose advertise host works
out to a wildcard publishes nothing rather than an address no peer can dial.

The cluster enrollment listener (Noise) follows `server.host` too. Give it its
own address with `bootstrap.noise_host` when enrollment should be reachable
somewhere the APIs are not.

## Locked-memory budget

Most of what `qrdbd` holds in protected memory — cluster keys, credentials,
PINs — comes out of one process-wide budget of locked (mlock'd) pages, sized
by `server.secure_memory_budget_mib`. It is set up once, when `start` runs,
before anything else in the process can allocate protected memory.

Leave it unset and `start` sizes it from the host: the smaller of 64 MiB and
your locked-memory limit (`ulimit -l` / `RLIMIT_MEMLOCK`) less 2 MiB of
headroom. With no limit, or with the `IPC_LOCK` capability, you get 64 MiB;
under a stock 8 MiB container limit you get 6 MiB. The startup log says which
figure was chosen and why:

```text
level=INFO msg="secure memory budget installed" budget_bytes=6291456 source="derived from RLIMIT_MEMLOCK" rlimit_memlock_source=RLIMIT_MEMLOCK rlimit_memlock_bytes=8388608
```

To get the full budget in a container or under systemd, grant the capability or
lift the limit — see [../deployment/](../deployment/). A limit too small to
leave at least 1 MiB after the headroom stops `start` with an error naming the
limit and how to raise it.

To choose the figure yourself:

```yaml
server:
  secure_memory_budget_mib: 128
```

or `--secure-memory-budget-mib 128`. `QRDB_SERVER_SECURE_MEMORY_BUDGET_MIB=128`
also works, but only alongside `--config`: environment variables override the
loaded config file and are not read without one. A value your host's limit cannot satisfy makes `start` fail immediately,
naming the limit and how to raise it, rather than starting with less
protection than configured. A negative number is refused before the server
starts: at config load when it comes from the file or the environment, and by
`start` itself when it comes from the flag.

**Not covered by the budget:** a few things lock memory outside it — sealing a
secret, and Shamir shares submitted during a barrier unseal. See
[cli.md](cli.md#locked-memory-outside-the-budget) for the exact scope and how
tightly each is bounded. `secure_memory_budget_mib` is therefore not a hard
ceiling on every page this daemon locks, which is why the derived budget leaves
headroom under the host limit.

## Raft settings

The `raft:` section configures this node's place in the Raft cluster. Each key
can also be set from the environment as `QRDB_RAFT_<KEY>`
(`raft.snapshot_entries` → `QRDB_RAFT_SNAPSHOT_ENTRIES`), with `--config`, and
by the `qrdbd start` flag shown. [cli.md](cli.md#raft-flags) lists the same
settings by flag.

```yaml
raft:
  data_dir: /var/lib/qrdb
  deployment_id: 1
  replica_id: 2
  raft_address: db2.example.com:63001
  listen_address: 0.0.0.0:63001
  peers: "1=db1.example.com:63001,2=db2.example.com:63001,3=db3.example.com:63001"
  member_identities: "1=spki:<hex>,3=spki:<hex>"
```

| Key | Flag | Type | Default | Accepted values | What it does |
|---|---|---|---|---|---|
| `data_dir` | `--data-dir` | string | `./data` | not empty | Where Raft state lives: the WAL in `wal/`, host metadata in `nodehost/`. |
| `deployment_id` | `--deployment-id` | integer | `1` | any | Keeps separate Raft clusters on one network apart. Use the same value on every node of a cluster. |
| `shard_id` | `--shard-id` | integer | `1` | raft mode: 1 to 999 | The node's own Raft group, the system shard. IDs from 1000 up are issued to databases. Leave it at 1. |
| `replica_id` | `--replica-id` | integer | `1` | raft mode: above 0 | This node's ID in the cluster; unique per node, and the ID other nodes name it by in `peers`. |
| `raft_address` | `--raft-address` | `host:port` | `127.0.0.1:63001` | raft mode: not empty | The address other nodes dial for Raft (QUIC over UDP). Its host is also the default `server.advertise_host`. |
| `listen_address` | `--listen` | `host:port` | empty, meaning `raft_address` | any | Where the Raft transport binds, when it should differ from the dialled address (NAT, `0.0.0.0`). |
| `peers` | `--peers` | comma-separated string | `1=127.0.0.1:63001` | `replicaID=host:port` entries; standalone mode allows at most one | The initial voting members. None or one entry is a single-node cluster. |
| `member_identities` | `--member-identities` | comma-separated string | empty | `replicaID=spki:<hex>` entries | The Raft identities of the other members; required on a multi-node cluster ([../clustering/user.md](../clustering/user.md)). |
| `join` | `--join` | boolean | `false` | `false` in standalone mode | Join an existing cluster instead of forming a new one. |
| `snapshot_entries` | `--snapshot-entries` | integer | `0`, meaning 100000 | more than the effective `compaction_overhead` | Applied entries between automatic snapshots. `0` keeps go-quicraft's default of 100000; it does not turn snapshots off. `18446744073709551615` does. |
| `compaction_overhead` | `--compaction-overhead` | integer | `0`, meaning 5000 | any | Log entries kept after a snapshot, so a follower that is slightly behind catches up from the log rather than a full snapshot. |
| `max_in_mem_log_size` | `--max-in-mem-log-size` | integer, bytes | `0`, no limit | any | Per-shard ceiling on the in-memory Raft log; above it proposals are slowed. |
| `max_total_in_mem_log_size` | `--max-total-in-mem-log-size` | integer, bytes | `0`, no limit | any | Ceiling on the in-memory Raft log across every shard on the node; above it writes fail with `ErrSystemBusy` until the log drains. |
| `sm_type` | `--sm` | string | `pebble` | `pebble`, `mvcc`, `memory`, `standard`, `concurrent`, `fast`, `disk`, `highperf`; `regular` is refused at start | The state machine. See [../run-modes/architecture.md](../run-modes/architecture.md#raft--single--or-multi-node). |
| `sm_backend` | `--sm-backend` | string | empty | `standard`, `concurrent`, `fast`, `disk` | The backend variant, when `sm_type` names a logical engine. |
| `sm_storage` | `--sm-storage` | string | empty, meaning `pebble` | `pebble`, `file`, `memory`; anything but `pebble` only with `sm_type` or `sm_backend` `disk` or `fast`; `objstore` is refused | The storage under the storage-generic state machines. Use the same value on every replica. See [../storage-engines/user.md](../storage-engines/user.md). |
| `lease_read` | `--lease-read` | boolean | `true` | `true` only with `check_quorum: true` | Linearizable reads served from the leader's lease without a heartbeat round. When that holds: [../key-value/engineering.md](../key-value/engineering.md#consistency-levels--pkgconsistency); what each replica needs: [../deployment/user.md](../deployment/user.md#keep-replica-clocks-disciplined). |
| `check_quorum` | `--check-quorum` | boolean | `true` | `false` only with `lease_read: false` | A leader that cannot reach a quorum within an election timeout steps down. Turning it off is unsafe; go-quicraft logs a warning when it is off. |

`--debug-raft` has no key: it raises the log level to `debug`.

Values from the file or the environment are checked when the config loads.
The same values given as flags are not; the checks go-quicraft makes when the
shard starts (`snapshot_entries` against `compaction_overhead`, `lease_read`
against `check_quorum`) apply either way.

Election and heartbeat timing are not configurable. Every networked node runs
go-quicraft's defaults, which is what lease reads rely on.

## Ports

Which ports to open, on which protocol, and who must reach each one is in
[Network ports](../clustering/user.md#network-ports). The fields that move them:

| Service | Default | Where to change it |
|---|---|---|
| Raft | 63001 | `raft.listen_address` (bind), `raft.raft_address` (advertised) |
| REST/admin | 63002 | `server.admin_port` |
| gRPC | 63003 | `server.grpc_port` |
| Native QUIC | 63004 | `server.quic_port` |
| MCP | 63005 | `mcp.port` |
| Noise trust anchor | 63006 | `bootstrap.noise_port` |
| CSR join | 63007 | `cluster_join.listen_port` |
| MySQL | 3306 | `server.mysql_port` |
| PostgreSQL | 5432 | `server.pgsql_port` |
| etcd | none; `qrdb database create --etcd` uses `localhost:2379` | `databases[].etcd.listen_addr` |

## Troubleshooting

**"validation failed for &lt;field&gt;: &lt;reason&gt;".** The message names the exact
field path. Note that validation stops at the **first** error, so fixing one may
reveal another — iterate.

**The server refused to start, naming a build tag.** You enabled a feature the
binary was not compiled with. Rebuild with the tag or remove the feature from
the config. This check is deliberately separate from ordinary validation, so
your config file stays valid or invalid independent of which binary reads it.

**A setting seems to have no effect.** Check `qrdb admin capabilities`. The
usual causes: the run mode forbids it (standalone refuses `raft.join` and more
than one `raft.peers` entry), or the build tag is absent. Config can only ever **narrow** what the
mode and the binary allow — it can never enable something they forbid, and
asking is an error rather than a silent no-op.

**You set `server.host` and a listener still answers on every interface.**
Every API listener binds `server.host`, and the REST server's HTTP/3 sibling
binds the host its TCP listener got; the daemon refuses to start rather than
let the two land on different hosts. Check `ss -ltnup` for the process
answering, and whether the `--host` flag overrides the file.

**You set `QRDB_SERVER_HOST` and nothing changed.** The environment layer is
part of loading the config file. Pass `--config`, or use `--host`.

**Your YAML value was ignored in favour of a flag you did not set.** It should
not be — only explicitly-set flags override YAML. If you see this, check whether
a wrapper script is passing the flag.

**The server refused to start with "unknown key".** You misspelled a key or a
section name, or the key names no field in this schema
(the message names the exact path, e.g. `storage.cach_size` or
`databases[2].protocol`, and the line where it appeared). Fix or remove it —
the loader refuses the file rather than silently running with that setting
off.

**A field you omitted came out as zero.** It should not — omission leaves the
default. If you see a zero, the file loaded (so the key is real), but check
whether you meant to set it inside a different section than it landed in.

**Your config was refused with `seal.strategy: must be one of: passphrase,
shamir, none`.** The value names no seal strategy; set one of the three.

**You want to change something without a restart.** Not possible for
configuration. Some runtime objects are mutable through their own APIs — per
database ([../databases/user.md](../databases/user.md)), per tenant
([../multi-tenancy/user.md](../multi-tenancy/user.md)), time-series retention
([../time-series/user.md](../time-series/user.md)).
