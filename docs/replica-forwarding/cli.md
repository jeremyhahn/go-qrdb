# Replica Forwarding — CLI

> **Status: SHIPPED — REST, gRPC, Unix socket, QUIC, MCP and embedded.** ·
> Scope: go-qrdb · Last verified: 2026-10-05

No CLI verbs. Forwarding is server behaviour: every `qrdb` verb that addresses
a `raft`-isolated database works through any node, over every network
transport, and gets a replica's answer.

Daemon configuration (`qrdbd`):

| Flag | Config key | Default | Meaning |
|---|---|---|---|
| `--replica-forwarding` | `replica_forwarding.enabled` | `true` | forward a request for a database this node holds no replica of. Off, the node refuses such a request with the replica set. Only a raft-mode node with a placement table forwards |
| `--replica-forwarding-timeout` | `replica_forwarding.timeout` | `10s` | bound on one forward, connection included (a Go duration greater than zero) |

A flag given on the command line wins over the config file, and any key can be
set from the environment as `QRDB_REPLICA_FORWARDING_ENABLED` or
`QRDB_REPLICA_FORWARDING_TIMEOUT`.

```yaml
replica_forwarding:
  enabled: true
  timeout: 10s
```

```bash
qrdbd start --replica-forwarding=false   # refuse instead of forwarding
```

A node verifies a replica's certificate against its cluster CA
(`--tls-ca-file`, or the CA the node was enrolled with). An embedding
application that runs nodes itself passes the same pool as
`server.WithCACertPool`. A server asked to forward without one forwards
nothing: it logs `replica forwarding off: no CA pool` once and answers the
refusal.

An embedding application has no flag to set: its node forwards its embedded
client's calls as the caller it configures with `node.WithEmbeddedCaller`
(see [api.md](api.md#embedded)), and the two settings above apply to it as to
`qrdbd`.

A watch on a database through a node that holds no replica fails with the
replica set, so the client can reconnect there:

```bash
qrdb --server https://db3.example.com:63002 --database orders kv watch orders/
# Error: placement: node ... is not a replica of acme/orders ...; its replicas are on ...
```
