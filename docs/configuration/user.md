# Configuration — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06

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

Reference a credential instead of embedding a value:

```yaml
some_secret_field: "credential://smtp-password"
```

Protected fields **must** carry such a reference; they resolve at boot into
protected in-memory values. If the reference cannot be resolved, the server
fails to start rather than continuing with nothing. See
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
a wider surface than you asked for. Earlier releases took only the port for the
HTTP/3 listener and bound the wildcard, so a REST server bound to loopback still
accepted QUIC datagrams on every interface.

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
secret, Shamir shares submitted during a barrier unseal, and the Noise
enrollment key, which still uses memguard through go-truststrap. See
[cli.md](cli.md#locked-memory-outside-the-budget) for the exact scope and how
tightly each is bounded. `secure_memory_budget_mib` is therefore not yet a hard
ceiling on every page this daemon locks, which is why the derived budget leaves
headroom under the host limit.

## Ports

| Service | Default | Where to change it |
|---|---|---|
| Raft | 63001 | `raft.raft_address` |
| REST | 63002 | `server.admin_port` |
| gRPC | 63003 | `server.grpc_port` |
| QUIC | 63004 | `server.quic_port` |
| MCP | 63005 | `mcp.port` |
| Noise bootstrap | 63006 | `bootstrap.noise_port` |
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
usual causes: the run mode forbids it (standalone cannot enable MVCC, watch or
lease), or the build tag is absent. Config can only ever **narrow** what the
mode and the binary allow — it can never enable something they forbid, and
asking is an error rather than a silent no-op.

**You set `server.host` and a listener still answers on every interface.**
The HTTP/3 listener used to bind the wildcard regardless. On an older build,
check `ss -ltnup` for a `udp UNCONN *:<port>` line beside the loopback TCP line.
On a current build the daemon refuses to start rather than reach that state.

**You set `QRDB_SERVER_HOST` and nothing changed.** The environment layer is
part of loading the config file. Pass `--config`, or use `--host`.

**Your YAML value was ignored in favour of a flag you did not set.** It should
not be — only explicitly-set flags override YAML. If you see this, check whether
a wrapper script is passing the flag.

**The server refused to start with "unknown key".** You misspelled a key or a
section name, or carried one over from an old config that no longer exists
(the message names the exact path, e.g. `storage.cach_size` or
`databases[2].protocol`, and the line where it appeared). Fix or remove it —
the loader refuses the file rather than silently running with that setting
off. This replaced an earlier behaviour where an unrecognized key loaded with
no effect and the only symptom was the field it was supposed to set coming
out at its default.

**A field you omitted came out as zero.** It should not — omission leaves the
default. If you see a zero, the file loaded (so the key is real), but check
whether you meant to set it inside a different section than it landed in.

**Your config was refused with `seal.strategy: must be one of: passphrase,
shamir, none`.** An older config using `strategy: software` names a strategy
that no longer exists; change it to `passphrase`, the canonical name for the
same software-protected strategy.

**You want to change something without a restart.** Not possible for
configuration. Some runtime objects are mutable through their own APIs — per
database ([../databases/user.md](../databases/user.md)), per tenant
([../multi-tenancy/user.md](../multi-tenancy/user.md)), time-series retention
([../time-series/user.md](../time-series/user.md)).
