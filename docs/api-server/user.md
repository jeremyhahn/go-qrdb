# Unified API Server — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

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
qrdb --transport grpc --server grpcs://db.example.com:9090 kv get hello
```

A URL scheme on `--server` picks the transport, so you rarely need
`--transport` as well. Recognised: `https://`, `grpcs://`, `quics://`,
`unix://`, `http://`, `grpc://`, `quic://`, `mcp://`.

## Default ports

| Transport | Port |
|---|---|
| REST | 63002 |
| gRPC | 63003 |
| QUIC | 63004 |
| Unix | `/var/run/qrdbd/qrdbd.sock` |

Plus, when enabled: MySQL 3306, PostgreSQL 5432, and a per-database etcd
listener (2379 by default).

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

## Metrics

```bash
curl --cert client.pem --key client-key.pem --cacert ca.pem \
     https://db.example.com/metrics
```

Prometheus format. Authenticated, but no specific permission — and present only
when the server was configured with a metrics gatherer.

## Extending the server

Higher layers add capabilities as **modules** registered into this same server,
rather than running a second one. That is Go code at composition time, not
configuration — see [engineering.md](engineering.md). The property worth knowing
as an operator: a module can establish *who* a caller is, but the host still
decides *what they may do*, and roles always come from the store rather than
from any token a module accepts.

## Troubleshooting

**A command exists in the help but returns 501 or "unimplemented".** Either the
capability needs raft mode and the server is standalone, or it needs a build tag
the binary lacks. `qrdb admin capabilities` distinguishes them.

**A route 404s that you are sure exists.** Some routes are registered
conditionally — `/v1/ca/bootstrap` only with a bootstrap certificate,
`/metrics` only with a gatherer. An unregistered route is a 404, not a 501.
`/v1/barrier/enable-encryption` is *not* one of these: it is always
registered, and refuses with a 501 (not a 404) when no encryption migrator is
configured for the deployment (standalone mode) — see
[../barrier-seal/api.md](../barrier-seal/api.md).

**The server refused to start with an authorization assertion.** A route, RPC or
tool has no permission declaration. This is the default-deny assertion doing its
job: the failure is at boot instead of an open endpoint in production.

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
`qrdb admin status` tell you which.

**The same call behaves differently on two transports.** It should not — that is
the property this feature exists to guarantee, and the parity suite exists to
check. Report it as a defect rather than working around it.
