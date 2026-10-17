# Unified API Server — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06

One server, six ways to reach it. Which one you pick is a deployment and
performance question, not a capability question — the same operations are
available on all of them.

## Prerequisites

Client certificates. Every transport authenticates with mTLS; there is no
password path.

## Picking a transport

| Transport | Use it when |
|---|---|
| `unix` (default) | you are on the same host — no network, no TLS handshake cost |
| `rest` | you want curl, a browser, or an HTTP client library |
| `grpc` | you want a typed client and streaming |
| `quic` | you want low-latency over a lossy network |
| `mcp` | you are wiring the database into an agent toolchain |
| `embedded` | you are linking qrdb into your own process (desktop/GUI build for the CLI) |

```bash
qrdb kv get hello                                          # unix socket, default
qrdb --server https://db.example.com kv get hello          # scheme implies REST
qrdb --transport grpc --server grpcs://db.example.com:63003 kv get hello
```

A URL scheme on `--server` picks the transport, so you rarely need
`--transport` as well. Recognised: `https://`, `grpcs://`, `quics://`,
`unix://`, `http://`, `grpc://`, `quic://`, `mcp://`.

## Default ports

| Transport | Port |
|---|---|
| REST | 63002 |
| gRPC | 63003 |
| QUIC | 63004 (UDP) |
| MCP | 63005 |
| Unix | `/var/run/qrdbd/qrdbd.sock` |

REST and gRPC are on by default; QUIC, MCP and Unix are off until enabled. The
REST port also serves HTTP/3 on UDP. Plus, when enabled: MySQL 3306, PostgreSQL
5432, and a per-database etcd listener at the database's `listen_addr` (no
daemon default; `qrdb database create --etcd` suggests `localhost:2379`).

## Scoping your requests

**Your tenant comes from your certificate**, not from a flag. To act as a
different tenant, use that tenant's certificate.

**Your database is a flag**, and defaults to `system`:

```bash
qrdb --database billing kv list invoices/
```

Over REST that is the `X-QRDB-Database` header; over gRPC the
`x-qrdb-database` metadata value.

## Checking what the server can do

```bash
qrdb admin health           # works without credentials
qrdb admin capabilities     # what is actually compiled in and enabled
qrdb admin status
```

`admin capabilities` is the answer to almost every "why is this command
missing" question — it reports build tags intersected with run mode intersected
with configuration, plus the mode itself.

## Reaching it without credentials

Three things work with none, by design, so a client can get started:

```bash
curl -k https://db.example.com/health
curl -k https://db.example.com/v1/config/initialized
curl -k https://db.example.com/v1/ca/bootstrap     # only if configured
```

Everything else needs a certificate.

Over MCP, nothing answers an anonymous caller unless the deployment's embedder
declared public tools — typically health, liveness, readiness and a login
ceremony. Then an anonymous client can `initialize`, list exactly those tools
and call them, and nothing else. Once authenticated, `tools/list` shows the
tools your grants let you call, not the whole surface.

## Metrics

```bash
curl --cert client.pem --key client-key.pem --cacert ca.pem \
     https://db.example.com/metrics
```

Prometheus format. Authenticated, but no specific permission. Every server
serves it — `qrdbd` and a node embedded through the Go SDK alike — unless the
embedder passed `server.WithGatherer(nil)` to leave it off.

A scrape carries the Go runtime and process series, qrdb's own `qrdb_*` series,
and go-quicraft's `quicraft_*` series for the Raft groups this process hosts.
The `quicraft_*` series are the process's aggregate over every Raft host in it;
the per-shard ones carry a `shard_id` label.

### Disk sync latency

Two histograms answer "are my disks keeping up":

| Metric | What one sample is |
|---|---|
| `quicraft_logdb_fsync_latency_seconds` | one fsync of the Raft write-ahead log |
| `qrdb_storage_sync_duration_seconds{backend}` | one durable sync by a storage backend: a pebble write committed with sync (`backend="pebble"`), or one file or directory fsync (`backend="file"`) |

A pebble database opened without per-write sync (the default for a standalone
pebble database) records nothing under `pebble`: none of its writes is a sync.

The other go-quicraft series:

- Raft: `quicraft_raft_proposals_dropped_total{shard_id}`,
  `quicraft_raft_elections_total{shard_id}`, `quicraft_raft_commit_batch_size`,
  `quicraft_raft_proposal_latency_seconds`, `quicraft_raft_commit_latency_seconds`,
  `quicraft_raft_apply_latency_seconds`, `quicraft_raft_read_index_latency_seconds`,
  `quicraft_raft_proposal_batch_size`, `quicraft_raft_snapshots_total`,
  `quicraft_raft_piggyback_commits_total`, `quicraft_metrics_per_shard_enabled`;
- log storage: `quicraft_logdb_write_latency_seconds`,
  `quicraft_logdb_compaction_latency_seconds`, `quicraft_logdb_write_bytes_total`;
- transport: `quicraft_transport_send_latency_seconds`,
  `quicraft_transport_messages_sent_total`, `quicraft_transport_messages_received_total`
  (and their `_detailed_total` forms), `quicraft_transport_messages_dropped_total`,
  `quicraft_transport_snapshots_sent_total`, `quicraft_transport_snapshots_received_total`,
  `quicraft_transport_snapshots_rejected_total` and the snapshot byte counters;
- security: `quicraft_security_unauthorized_messages_dropped_total`,
  `quicraft_security_unauthorized_snapshots_dropped_total`.

### Slow-sync warning

A sync slower than one second logs one warning per sync, at `WARN`, whatever
the scrape interval:

```text
level=WARN msg="slow storage sync" backend=file database=orders dir=<install-dir>/data/orders took=1.42s threshold=1s
level=WARN msg="slow raft WAL fsync" wal_dir=<install-dir>/data/wal took=1.18s threshold=1s
```

`took` is the sync's duration and `threshold` the limit it crossed; `backend`,
`database` and `dir` name the storage, `wal_dir` the write-ahead log.

The threshold is `observability.slow_sync_threshold`, a Go duration. It
defaults to one second, which is etcd's slow-fdatasync threshold. It must be
greater than zero and at most one minute: `0`, a negative value or anything
above `1m` stops the node at startup with a configuration error naming the
key. It is an observation threshold for the stall signal only, NOT a
durability knob: quicraft keeps per-entry fsync with no tunable. Lowering it
makes the warning more sensitive and changes nothing about how data is
written. It covers the Raft write-ahead log and every storage backend, on
`qrdbd` and on a node embedded through the Go SDK.

```yaml
observability:
  slow_sync_threshold: 1s
```

`QRDB_OBSERVABILITY_SLOW_SYNC_THRESHOLD=250ms` overrides the file.

A steady stream of these warnings means the disk under that directory cannot
keep up with the write rate; the histograms above show by how much.

## Extending the server

Higher layers add capabilities as **modules** registered into this same server,
rather than running a second one. That is Go code at composition time, not
configuration — see [engineering.md](engineering.md). The property worth knowing
as an operator: a module can establish *who* a caller is, but the host still
decides *what they may do*, and roles always come from the store rather than
from any token a module accepts.

A module can also observe connections on every listener (`ConnObserver`), which
is how a composing host's own active-connection metrics count the connections
this server accepts for it. qrdb itself exports no such gauge.

## Troubleshooting

**A command exists in the help but returns 501 or "unimplemented".** Either the
capability needs raft mode and the server is standalone, or it needs a build tag
the binary lacks. `qrdb admin capabilities` distinguishes them.

**A route 404s that you are sure exists.** Some routes are registered
conditionally — `/v1/ca/bootstrap` only with a bootstrap certificate,
`/metrics` unless the embedder turned it off. An unregistered route is a 404,
not a 501.
`/v1/barrier/enable-encryption` is *not* one of these: it is always
registered, and refuses with a 501 (not a 404) when no encryption migrator is
configured for the deployment (standalone mode) — see
[../barrier-seal/api.md](../barrier-seal/api.md).

**The server refused to start with an authorization assertion.** A route, RPC or
tool has no permission declaration. This is the default-deny assertion doing its
job: the failure is at boot instead of an open endpoint in production.

**The MCP server refused to start naming a public tool.** A module declared a
tool public that reaches data (`PublicMCPToolRefusedError`), or named a tool
no module registered (`UnknownPublicToolError`). Only tools that reach no data
may be public; drop the name from the module's declaration.

**The server refused to start naming a route and two owners.** Two
registrations claim the same REST route — two modules, or a module and a
built-in endpoint. Nothing is allowed to take over another's route silently, so
it stops at construction. Parameter names do not distinguish routes:
`/devices/{id}` and `/devices/{deviceID}` collide. Move the module's routes
under its own path prefix. If the message says a route was registered
"directly on the router", the module bypassed `Handle`/`HandlePublic`; register
it through those. If it names `Mount`, `Route`, `NotFound`, `MethodNotAllowed` or
`Use`, the module called a router method that would serve requests outside any
registration; register a subtree as `Handle(method, "/prefix/*", perm, h)`
instead.

**Everything returns 503.** The barrier is sealed, the node is draining, or this
node is not the leader and no resolver is configured. `qrdb barrier status` and
`qrdb admin status` tell you which. A `SEAL_STATE_UNCONFIRMED` code means the
barrier is not sealed: this node cannot read the cluster seal records — the
system shard has no leader, for example — and the call succeeds on retry once
it can.

**The same call behaves differently on two transports.** It should not — that is
the property this feature exists to guarantee, and the parity suite exists to
check. Report it as a defect rather than working around it.
