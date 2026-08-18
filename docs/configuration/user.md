# Configuration — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Configuring `qrdbd`, and confirming that what you wrote is what is running.

## Prerequisites

Access to the daemon's config file and the ability to restart it. Configuration
is read at boot — there is no runtime reconfiguration.

## How settings are resolved

Three layers, in this order:

1. **Built-in defaults** — every field starts with a production-ready value.
2. **Your YAML** — only the fields you actually write override the defaults.
   Omitting a field leaves the default; it does not zero it.
3. **CLI flags you explicitly set** — a flag left at its own default will not
   overwrite what your YAML said.

```bash
qrdbd start --config /etc/qrdb/qrdb.yaml
```

## A minimal file

```yaml
mode: raft
raft:
  data_dir: /var/lib/qrdb
storage:
  engine: pebble
tls:
  enabled: true
  ca_file: /etc/qrdb/ca.pem
  cert_file: /etc/qrdb/server.pem
  key_file: /etc/qrdb/server-key.pem
auth:
  enabled: true
audit:
  enabled: true
```

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

## Ports

| Service | Default | Where to change it |
|---|---|---|
| Raft | 63001 | `raft.raft_address` |
| REST | 63002 | `server.admin_port` |
| gRPC | 63003 | `server.grpc_port` |
| QUIC | 63004 | — |
| MySQL | 3306 | `server.mysql_port` |
| PostgreSQL | 5432 | `server.pgsql_port` |
| etcd | 2379 | per database |

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

**Your YAML value was ignored in favour of a flag you did not set.** It should
not be — only explicitly-set flags override YAML. If you see this, check whether
a wrapper script is passing the flag.

**A field you omitted came out as zero.** It should not — omission leaves the
default. If you see a zero, check that the surrounding section is spelled
correctly; a mistyped section name means every field under it is absent.

**"software" appears in your config but the server reports "passphrase".**
That is correct: `seal.strategy: software` is a deprecated alias, canonicalized
once at load. Every consumer sees `passphrase`.

**You want to change something without a restart.** Not possible for
configuration. Some runtime objects are mutable through their own APIs — per
database ([../databases/user.md](../databases/user.md)), per tenant
([../multi-tenancy/user.md](../multi-tenancy/user.md)), time-series retention
([../time-series/user.md](../time-series/user.md)).
