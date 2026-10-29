# Unified API Server — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

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
daemon default; `qrdb database create --etcd` takes it from
`--etcd-listen-addr`, default `localhost:2379`).

## Calling from a browser — gRPC-Web

A browser cannot speak native gRPC, so the REST port can also serve every gRPC
service as gRPC-Web. It is off by default; turn it on in the config file:

```yaml
protocols:
  rest: true
  grpc: true          # required: gRPC-Web is served through the gRPC server
server:
  grpc_web:
    enabled: true
    allowed_origins:            # browser origins allowed to call; empty = none
      - https://app.example.com
    allow_credentials: true     # let the browser send its client certificate
```

There is no `qrdbd start` flag for it. Point a gRPC-Web client (grpc-web,
connect-web, or any client speaking the protocol) at
`https://db.example.com:63002/grpc-web`. Select a database with the
`x-qrdb-database` header.

A gRPC-Web call is a gRPC call: it authenticates with your client certificate
and gets exactly the status code native gRPC would give the same caller. An
`allowed_origins` entry is a full origin (`https://app.example.com`, with the
port when it is not 443) or `*`; `*` cannot be combined with
`allow_credentials`, and either mistake stops the node at startup with a
configuration error naming the key.

## WebTransport sessions on an embedded node

A program that embeds a node can serve WebTransport sessions
(draft-ietf-webtrans-http3-16) on the REST port's HTTP/3 listener. Register one
handler for one path in the server options you start the node with:

```go
n, err := node.NewStandaloneNode(ctx, cfg, []sdkserver.ServerOption{
	sdkserver.WithWebTransportHandler("/wt",
		func(sess *sdkserver.WebTransportSession, caller *auth.Identity, tenantID string) {
			defer sess.CloseWithError(0, "")
			// serve the session's streams and datagrams for caller in tenantID
		}),
})
```

A client opens `https://db.example.com:63002/wt` over HTTP/3. The session is
authenticated exactly like a REST request, with your client certificate or
token. A caller REST would refuse gets the same status (401 when
unauthenticated, for example), and the session is never opened. A request over
HTTP/1.1 or HTTP/2, or one that is not a valid WebTransport CONNECT, gets 400.
A browser's `Origin` must match the host it connects to.

Without this option the server announces no WebTransport support, and `/wt`
is an unknown path (404). A nil handler, or a path not starting with `/`, stops
the server at startup with `InvalidWebTransportHandlerError`. Deviations from
the draft are listed in
[docs/reference/README.md](../reference/README.md#the-draft-ietf-webtrans-http3-pin-and-its-deviations).

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

Everything else needs a certificate. The cluster key routes
(`/v1/cluster/nodekey`, `/v1/cluster/keydelivery`) also take no credential, but
they serve joining nodes and run their own certificate and placement checks.

Over MCP, nothing answers an anonymous caller unless the deployment's embedder
declared public tools — typically health, liveness, readiness and a login
ceremony. Then an anonymous client can `initialize`, list exactly those tools
and call them, and nothing else. Once authenticated, `tools/list` shows the
tools your grants let you call, not the whole surface.

## Binding the caller on the embedded transport

A network transport learns who is calling from the connection. The embedded
transport has no connection: whatever identity is on the `context.Context`
your code passes is the caller. A program that serves its own users through an
embedded node — a desktop application, a service that fronts qrdb with its own
login — installs a **context binder** so that no call reaches qrdb without one.

```go
n, err := node.NewStandaloneNode(ctx, cfg, nil,
	node.WithContextBinder(func(ctx context.Context) (context.Context, error) {
		id, err := sessions.Identity(ctx) // your application's own session lookup
		if err != nil {
			return ctx, err // the call fails with this error; nothing runs
		}
		return auth.WithIdentity(ctx, id), nil
	}),
)
```

The binder runs once before every call through `Node.Client()`: every KV,
SQL, barrier, ceremony, credential, IAM and admin request, and every watch
stream. It runs before authorization, so the identity it binds is the one the
permission check and the operation see. When it returns an error, the call
fails with that same error (`errors.Is` matches it) and nothing is executed.

- It binds only the client `Node.Client()` returns. The node's own internal
  calls, and the services `Node.Services()` returns, are not behind it.
- A binder that returns a `nil` context fails the call with
  `*embedded.NilBoundContextError`, which names the operation.
- `node.WithContextBinder(nil)` fails node construction with
  `*embedded.NilContextBinderError`. Leave the option out to install no
  binder; then the context you pass is used as it is.

A program that builds the transport itself passes `embedded.WithContextBinder`
to `embedded.New`, with the same behaviour.

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

`quicraft_raft_elections_total{shard_id}` counts the campaigns this node
launched for that shard, one per round of vote requests it sent. A
single-replica shard elects itself without sending any, so on a one-node
deployment the counter stays at zero.

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
conditionally — `/v1/ca/bootstrap` only when exactly one CA bundle source
(the cluster CA or a host bundle) is configured,
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
"directly on the router", the module bypassed `Handle`/`HandlePublic`/`HandleOptional`;
register it through those. If it names `Mount`, `Route`, `NotFound`, `MethodNotAllowed` or
`Use`, the module called a router method that would serve requests outside any
registration; register a subtree as `Handle(method, "/prefix/*", perm, h)`
instead.

**Everything returns 503.** The barrier is sealed, the node is draining, or this
node is not the leader and no resolver is configured. `qrdb barrier status` and
`qrdb admin status` tell you which. A `SEAL_STATE_UNCONFIRMED` code means the
barrier is not sealed: this node cannot read the cluster seal records — the
system shard has no leader, for example — and the call succeeds on retry once
it can.

**Every gRPC-Web call answers `Unimplemented` / HTTP 404.** gRPC-Web is not
enabled on that node: set `server.grpc_web.enabled: true`. A browser call that
fails before reaching the node with a CORS error means its origin is not in
`allowed_origins`.

**The same call behaves differently on two transports.** It should not — that is
the property this feature exists to guarantee, and the parity suite exists to
check. Report it as a defect rather than working around it.
